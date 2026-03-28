// Package shim implements the MCP stdio server that Claude Code spawns.
// It connects to the local Bifrost hub, registers the agent, serves MCP tools
// over stdio, and forwards hub notifications as notifications/claude/channel.
package shim

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/pkg/detect"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lockedWriter wraps an io.Writer with a mutex so that multiple goroutines
// (the MCP SDK transport and the notificationWriter) can safely share a single
// output stream (os.Stdout).
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}

// lockedWriteCloser adapts a lockedWriter to io.WriteCloser (Close is a no-op).
type lockedWriteCloser struct {
	*lockedWriter
}

func (lockedWriteCloser) Close() error { return nil }

// Options configures the shim entry point.
type Options struct {
	ProjectName string
	DisplayName string
	ProjectDir  string
}

const mcpInstructions = `Bifrost connects you to other Claude Code agents running in different projects.

MESSAGES: Use bifrost_send for quick questions, context sharing, and status updates.
Messages from other agents arrive as <channel source="bifrost" from="..." type="..." ...>.
Reply with bifrost_send — set "to" to the agent name from the "from" attribute.
Address agents by name (e.g. "agent:api-backend"). If ambiguous, the tool lists available agents.

TASKS: Use bifrost_request_task when asking another agent to DO WORK (implement a feature,
fix a bug, run tests, deploy, etc). Tasks have a lifecycle:
  1. You request a task with bifrost_request_task (assignee, title, description)
  2. The assignee receives it and can ask clarifying questions via bifrost_send
  3. The assignee accepts with bifrost_update_task (status: "accepted")
  4. The assignee works, updates progress (status: "in_progress")
  5. The assignee completes (status: "completed", summary) or rejects/fails
  6. You get notified of each status change

Use bifrost_send for CONVERSATION. Use bifrost_request_task for WORK REQUESTS.
If another agent asks you to implement something via bifrost_send, suggest they
request a task instead so the work is tracked.

CHANNELS: Subscribe to broadcast topics with bifrost_subscribe (e.g. "channel:deploys").
DND: Use bifrost_dnd to pause incoming messages when you need focus.`

// Run starts the shim: connects to the hub, registers, serves MCP tools over
// stdio, and forwards hub notifications to Claude Code. It blocks until stdin
// closes or the context is cancelled.
func Run(ctx context.Context, opts Options) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// 1. Resolve project dir.
	projectDir := opts.ProjectDir
	if projectDir == "" {
		var err error
		projectDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}

	// 2. Detect project info.
	projInfo := detect.DetectProject(projectDir)
	projectName := opts.ProjectName
	if projectName == "" {
		projectName = projInfo.Name
	}

	// 3. Build agent identity.
	hostname, _ := os.Hostname()
	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("USERNAME")
	}

	agentID := protocol.AgentIDFrom(hostname, projectDir)
	displayName := opts.DisplayName
	if displayName == "" {
		displayName = fmt.Sprintf("%s@%s", username, projectName)
	}

	agent := &protocol.Agent{
		AgentID:         agentID,
		Username:        username,
		Hostname:        hostname,
		LocalPath:       projectDir,
		ProjectName:     projectName,
		DisplayName:     displayName,
		Capabilities:    projInfo.Capabilities,
		Status:          protocol.AgentStatusOnline,
		ConnectedAt:     time.Now(),
		LastSeen:        time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}

	// 4. Connect to hub (auto-start if needed).
	hubConn, err := connectToHub(ctx)
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer hubConn.Close()

	// 5. Start the hub connection multiplexer.
	mux := newHubMux(ctx, hubConn, log)

	// 6. Create MCP server with tools and capabilities.
	// Registration with the hub happens AFTER the MCP server starts, so that
	// the ping notification (and any early messages) arrive when the notification
	// writer is ready to emit them via the MCP transport.
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "bifrost-shim",
			Version: protocol.ProtocolVersion,
		},
		&mcp.ServerOptions{
			Instructions: mcpInstructions,
			Logger:       log,
			Capabilities: &mcp.ServerCapabilities{
				Experimental: map[string]any{
					"claude/channel": map[string]any{},
				},
			},
		},
	)

	// 7. Set up a shared locked writer for stdout so the MCP SDK transport
	// and our notification writer don't interleave output.
	sharedOut := &lockedWriter{w: os.Stdout}
	nw := &notificationWriter{w: sharedOut}

	globalDND.interval = 5 * time.Minute
	registerTools(server, mux, agent, nw)

	// 8. Start listening for hub notifications in background.
	notifCtx, notifCancel := context.WithCancel(ctx)
	defer notifCancel()
	go listenHubNotifications(notifCtx, mux, nw, log)

	// 9. Deregister on shutdown.
	defer func() {
		deregCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = mux.rpcCall(deregCtx, "hub.deregister", map[string]string{"agent_id": agentID})
		log.Info("deregistered from hub", "agent_id", agentID)
	}()

	// 10. Run MCP server over stdio in a goroutine. We need it running before
	// registering with the hub so the MCP transport is ready to emit the ping
	// notification that arrives on registration.
	mcpErr := make(chan error, 1)
	go func() {
		mcpErr <- server.Run(ctx, &mcp.IOTransport{
			Reader: os.Stdin,
			Writer: lockedWriteCloser{sharedOut},
		})
	}()

	// Give the MCP transport a moment to start reading stdin.
	time.Sleep(50 * time.Millisecond)

	// 11. NOW register with the hub — the notification listener and MCP
	// transport are both running, so the ping will be emitted correctly.
	resp, err := mux.rpcCall(ctx, "hub.register", agent)
	if err != nil {
		return fmt.Errorf("hub.register call: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub.register error: %s", resp.Error.Message)
	}
	log.Info("registered with hub", "agent_id", agentID)

	// 12. Start heartbeat goroutine.
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go runHeartbeat(heartbeatCtx, mux, agentID, log)

	// 13. Start the reconnection goroutine. It watches the disconnected channel
	// on the mux and re-establishes the hub connection when it drops.
	go runReconnect(ctx, mux, agent, nw, log)

	// 14. Wait for MCP server to finish (stdin closed).
	return <-mcpErr
}

// runHeartbeat sends periodic heartbeat RPCs to the hub.
func runHeartbeat(ctx context.Context, mux *hubMux, agentID string, log *slog.Logger) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := mux.rpcCall(ctx, "hub.heartbeat", map[string]string{"agent_id": agentID})
			if err != nil {
				log.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

// reconnectBackoff returns the backoff duration for the given attempt number
// (0-based). Starts at 1 s, doubles each attempt, capped at 30 s.
func reconnectBackoff(attempt int) time.Duration {
	const (
		base = time.Second
		max  = 30 * time.Second
	)
	d := base << uint(attempt) // 1s, 2s, 4s, 8s, 16s, 32s…
	if d > max || d <= 0 {     // guard overflow
		return max
	}
	return d
}

// runReconnect watches the mux's disconnected channel and re-establishes the
// hub connection when it drops. After a successful reconnect it re-registers
// the agent and emits a channel notification to Claude Code.
func runReconnect(ctx context.Context, mux *hubMux, agent *protocol.Agent, nw *notificationWriter, log *slog.Logger) {
	for {
		// Wait for a disconnect signal or context cancellation.
		select {
		case <-ctx.Done():
			return
		case <-mux.disconnectedCh():
		}

		log.Warn("hub connection lost — reconnecting")

		// Attempt reconnection with exponential backoff.
		for attempt := 0; ; attempt++ {
			backoff := reconnectBackoff(attempt)
			log.Warn("reconnect attempt", "attempt", attempt+1, "backoff", backoff)

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			newConn, err := connectToHub(ctx)
			if err != nil {
				log.Warn("reconnect failed", "attempt", attempt+1, "error", err)
				continue
			}

			// Swap in the new connection under write lock, close the old one.
			old := mux.swapConn(newConn)
			if old != nil {
				_ = old.Close()
			}

			// Start a new read loop on the fresh connection.
			go mux.readLoop(ctx)

			// Re-register with the hub.
			regResp, regErr := mux.rpcCall(ctx, "hub.register", agent)
			if regErr != nil {
				log.Warn("re-register failed after reconnect", "error", regErr)
				// Swap back to nil so rpcCall returns errReconnecting, then retry.
				_ = mux.swapConn(nil)
				if newConn != nil {
					_ = newConn.Close()
				}
				continue
			}
			if regResp.Error != nil {
				log.Warn("re-register RPC error after reconnect", "msg", regResp.Error.Message)
			}

			log.Info("reconnected and re-registered with hub", "agent_id", agent.AgentID)

			// Notify Claude Code that the connection has been restored.
			_ = nw.writeNotification("notifications/claude/channel", channelNotificationParams{
				Content: "Reconnected to bifrost hub",
				Meta:    map[string]string{"event": "reconnected"},
			})

			break // success — outer loop will arm the next disconnect watch
		}
	}
}
