// Package shim implements the MCP stdio server that Claude Code spawns.
// It connects to the local Bifrost hub, registers the agent, serves MCP tools
// over stdio, and forwards hub notifications as notifications/claude/channel.
package shim

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/marcfargas/bifrost/pkg/detect"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

	// 6. Register with hub.
	resp, err := mux.rpcCall(ctx, "hub.register", agent)
	if err != nil {
		return fmt.Errorf("hub.register call: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub.register error: %s", resp.Error.Message)
	}
	log.Info("registered with hub", "agent_id", agentID)

	// 7. Create MCP server with tools and capabilities.
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

	registerTools(server, mux, agent)

	// 8. Set up notification writer (writes raw JSON-RPC to stdout).
	nw := &notificationWriter{w: os.Stdout}

	// 9. Start listening for hub notifications in background.
	notifCtx, notifCancel := context.WithCancel(ctx)
	defer notifCancel()
	go listenHubNotifications(notifCtx, mux, nw, log)

	// 10. Start heartbeat goroutine.
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go runHeartbeat(heartbeatCtx, mux, agentID, log)

	// 11. Deregister on shutdown.
	defer func() {
		deregCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = mux.rpcCall(deregCtx, "hub.deregister", map[string]string{"agent_id": agentID})
		log.Info("deregistered from hub", "agent_id", agentID)
	}()

	// 12. Run MCP server over stdio (blocks until stdin closes).
	return server.Run(ctx, &mcp.StdioTransport{})
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
