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

const mcpInstructions = `Messages from other agents arrive as <channel source="bifrost" ...>.
Use bifrost_send to reply — set the "to" field to the agent name from the "from" attribute.
Address agents by name. If the name is ambiguous, the tool will list available agents — pick the right one and retry.
Addressing: use "agent:name" for agents, "channel:name" for broadcast channels, "task:id" for task-scoped messages.
When you receive a task request (type="task_requested"), review it carefully. If you need clarification, send a QUESTION message before accepting.
Use bifrost_dnd to enable Do Not Disturb when you need focus time.`

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

	// 13. Wait for MCP server to finish (stdin closed).
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
