// Package transport provides connection management for the Bifrost hub.
//
// This file implements the MCP Streamable HTTP endpoint that the hub serves
// directly. Claude Code connects via "type": "url" in settings.json.
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/detect"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpHTTPInstructions = `Messages from other agents arrive as <channel source="bifrost" ...>.
Use bifrost_send to reply — set the "to" field to the agent name from
the "from" attribute. Address agents by name (e.g., "api-backend").
If the name is ambiguous, the tool will list available agents — pick
the right one and retry.

Addressing: use "agent:name" for agents, "channel:name" for broadcast
channels, "task:id" for task-scoped messages.

When you receive a task request (type="task_requested"), review it
carefully. If you need clarification, send a QUESTION message in the
task's conversation before accepting. Accept with bifrost_update_task.

Use bifrost_dnd to enable Do Not Disturb when you need focus time.
Remember to disable it when you're ready for messages again.`

// MCPHTTPTransport runs the MCP Streamable HTTP endpoint on the hub.
type MCPHTTPTransport struct {
	hub      *core.Hub
	cfg      config.MCPConfig
	logger   *slog.Logger
	sessions *MCPSessionStore
	server   *http.Server
	addr     string // actual listening address after Start
	mu       sync.Mutex
	running  bool
}

// NewMCPHTTPTransport creates a new MCP HTTP transport for the hub.
func NewMCPHTTPTransport(h *core.Hub, cfg config.MCPConfig, logger *slog.Logger) *MCPHTTPTransport {
	return &MCPHTTPTransport{
		hub:      h,
		cfg:      cfg,
		logger:   logger,
		sessions: NewMCPSessionStore(),
	}
}

// Start begins listening for MCP HTTP connections.
func (t *MCPHTTPTransport) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		return fmt.Errorf("mcp_http: already running")
	}

	// Create the StreamableHTTPHandler. Each new session gets its own MCP
	// Server with the bifrost tools registered.
	handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return t.newSessionServer()
	}, &mcp.StreamableHTTPOptions{})

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	listenAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(t.cfg.Port))
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("mcp_http: listen on %s: %w", listenAddr, err)
	}

	t.addr = ln.Addr().String()
	t.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	t.running = true
	t.logger.Info("MCP HTTP transport started", "addr", t.addr)

	go func() {
		if err := t.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.logger.Error("MCP HTTP transport serve error", "err", err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = t.Stop()
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server.
func (t *MCPHTTPTransport) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.running {
		return nil
	}

	t.running = false
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.logger.Info("MCP HTTP transport stopping")
	return t.server.Shutdown(shutdownCtx)
}

// Addr returns the actual listening address (host:port).
func (t *MCPHTTPTransport) Addr() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.addr
}

// Port returns the actual port the HTTP server is listening on.
// Useful when configured with port 0 (OS-assigned). Returns 0 if not started.
func (t *MCPHTTPTransport) Port() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.addr == "" {
		return 0
	}
	_, portStr, err := net.SplitHostPort(t.addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

// Sessions returns the session store (used by the hub for notifications).
func (t *MCPHTTPTransport) Sessions() *MCPSessionStore {
	return t.sessions
}

// Notify implements core.Notifier. It delivers a notification to the MCP
// session bound to the given agent. For now this is a placeholder that logs
// the notification — full SSE push is wired in a later task.
func (t *MCPHTTPTransport) Notify(agentID string, notification core.Notification) bool {
	sid, ok := t.sessions.GetByAgent(agentID)
	if !ok {
		return false
	}
	t.logger.Debug("MCP HTTP notification",
		"agent_id", agentID,
		"session_id", sid,
		"type", notification.Type,
	)
	// TODO(P4-T2): push notification via SSE to the MCP session.
	return true
}

// newSessionServer creates a fresh MCP Server instance for a new HTTP session.
// The go-sdk StreamableHTTPHandler calls this once per new MCP session.
func (t *MCPHTTPTransport) newSessionServer() *mcp.Server {
	// Generate a session ID and pre-create the session in our store. The
	// GetSessionID callback lets us capture the ID that the SDK will use.
	sessionID := protocol.NewID()
	sess := t.sessions.Create(sessionID)
	t.logger.Info("MCP HTTP session created", "session_id", sessionID)

	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "bifrost",
			Version: protocol.ProtocolVersion,
		},
		&mcp.ServerOptions{
			Instructions: mcpHTTPInstructions,
			Logger:       t.logger,
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{ListChanged: true},
				Experimental: map[string]any{
					"claude/channel": map[string]any{},
				},
			},
			GetSessionID: func() string {
				return sessionID
			},
		},
	)

	// Register all bifrost tools, capturing the session for each closure.
	t.registerAllTools(srv, sess)

	return srv
}

// registerAllTools adds all 11 bifrost MCP tools to the given server.
func (t *MCPHTTPTransport) registerAllTools(srv *mcp.Server, sess *MCPSession) {
	t.registerWhoami(srv, sess)
	t.registerListAgents(srv, sess)
	t.registerSend(srv, sess)
	t.registerListConversations(srv, sess)
	t.registerCreateTask(srv, sess)
	t.registerUpdateTask(srv, sess)
	t.registerGetTask(srv, sess)
	t.registerListTasks(srv, sess)
	t.registerSubscribe(srv, sess)
	t.registerListChannels(srv, sess)
	t.registerDND(srv, sess)
	t.registerPeer(srv, sess)
}

// touchSession updates last-seen and returns an error if the session requires
// registration but hasn't been registered yet.
func (t *MCPHTTPTransport) touchSession(sess *MCPSession) {
	t.sessions.Touch(sess.SessionID)
}

// ensureRegistered checks that the session has been bound to an agent.
func ensureRegistered(sess *MCPSession) error {
	if !sess.Registered {
		return fmt.Errorf("agent not registered: call bifrost_whoami first to register this session")
	}
	return nil
}

// --- Tool: bifrost_whoami ---

type whoamiParams struct {
	ProjectName string `json:"project_name" jsonschema:"Project name (directory name or from package.json/go.mod)"`
	LocalPath   string `json:"local_path" jsonschema:"Working directory path"`
	DisplayName string `json:"display_name,omitempty" jsonschema:"Optional human-friendly display name"`
}

func (t *MCPHTTPTransport) registerWhoami(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_whoami",
		Description: "Register this agent and return its identity, aliases, hub info, and DND status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params whoamiParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)

		hostname := localHostname()
		username := localUsername()
		agentID := protocol.AgentIDFrom(hostname, params.LocalPath)

		// Update session state.
		sess.ProjectName = params.ProjectName
		sess.Hostname = hostname
		sess.Username = username
		sess.LocalPath = params.LocalPath
		t.sessions.Register(sess.SessionID, agentID)

		// Register with the hub core.
		agent := protocol.Agent{
			AgentID:         agentID,
			ProjectName:     params.ProjectName,
			DisplayName:     params.DisplayName,
			Hostname:        hostname,
			Username:        username,
			LocalPath:       params.LocalPath,
			Status:          protocol.AgentStatusOnline,
			ConnectedAt:     sess.CreatedAt,
			LastSeen:        time.Now(),
			ProtocolVersion: protocol.ProtocolVersion,
			Capabilities:    detect.DetectProject(params.LocalPath).Capabilities,
		}

		if err := t.hub.Agents().Register(ctx, &agent); err != nil {
			return toolError(err.Error()), nil, nil
		}

		data, _ := json.MarshalIndent(agent, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_list_agents ---

type listAgentsParams struct {
	Status string `json:"status,omitempty" jsonschema:"optional filter by agent status (online, idle, offline, dnd)"`
}

func (t *MCPHTTPTransport) registerListAgents(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_list_agents",
		Description: "List agents connected to the Bifrost hub. Optionally filter by status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params listAgentsParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}

		filter := store.AgentFilter{}
		if params.Status != "" {
			filter.Status = protocol.AgentStatus(params.Status)
		}

		agents, err := t.hub.Store().ListAgents(ctx, filter)
		if err != nil {
			return toolError(err.Error()), nil, nil
		}

		data, _ := json.MarshalIndent(agents, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_send ---

type sendParams struct {
	To             string   `json:"to" jsonschema:"recipient address: agent:name, channel:name, or task:id"`
	Body           string   `json:"body" jsonschema:"message body text"`
	Type           string   `json:"type,omitempty" jsonschema:"message type: QUESTION, ANSWER, CONTEXT, STATUS, ERROR (default CONTEXT)"`
	ConversationID string   `json:"conversation_id,omitempty" jsonschema:"continue an existing conversation"`
	InReplyTo      string   `json:"in_reply_to,omitempty" jsonschema:"message ID for threading"`
	Priority       string   `json:"priority,omitempty" jsonschema:"message priority: low, normal, urgent (default normal)"`
	Files          []string `json:"files,omitempty" jsonschema:"file paths to attach (only valid for task:id targets)"`
}

func (t *MCPHTTPTransport) registerSend(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_send",
		Description: "Send a message to another agent, channel, or task in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params sendParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}

		msgType := protocol.MessageType(params.Type)
		if msgType == "" {
			msgType = protocol.MessageTypeContext
		}
		priority := protocol.Priority(params.Priority)
		if priority == "" {
			priority = protocol.PriorityNormal
		}

		msg := protocol.Message{
			ID:             protocol.NewID(),
			From:           sess.AgentID,
			To:             params.To,
			Type:           msgType,
			Body:           params.Body,
			ConversationID: params.ConversationID,
			InReplyTo:      params.InReplyTo,
			Priority:       priority,
			Timestamp:      time.Now(),
		}

		if err := t.hub.Messages().Send(ctx, &msg); err != nil {
			if notFound, ok := err.(*core.AgentNotFoundError); ok {
				result := notFound.Error()
				if len(notFound.Available) > 0 {
					avail, _ := json.MarshalIndent(notFound.Available, "", "  ")
					result += "\n\nAvailable agents:\n" + string(avail)
				}
				return toolError(result), nil, nil
			}
			return toolError(err.Error()), nil, nil
		}

		data, _ := json.MarshalIndent(msg, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Message sent.\n" + string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_list_conversations ---

type listConversationsParams struct {
	ActiveOnly *bool `json:"active_only,omitempty" jsonschema:"if true, show only active conversations"`
}

func (t *MCPHTTPTransport) registerListConversations(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_list_conversations",
		Description: "List conversations in the Bifrost hub.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params listConversationsParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}

		filter := store.ConversationFilter{}
		activeOnly := true
		if params.ActiveOnly != nil {
			activeOnly = *params.ActiveOnly
		}
		if activeOnly {
			closed := false
			filter.Closed = &closed
		}

		convs, err := t.hub.Store().ListConversations(ctx, filter)
		if err != nil {
			return toolError(err.Error()), nil, nil
		}

		data, _ := json.MarshalIndent(convs, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_create_task ---

type createTaskParams struct {
	Assignee    string   `json:"assignee" jsonschema:"agent address to assign the task to"`
	Title       string   `json:"title" jsonschema:"short task title"`
	Description string   `json:"description,omitempty" jsonschema:"full task description"`
	Files       []string `json:"files,omitempty" jsonschema:"optional list of local file paths to attach"`
}

func (t *MCPHTTPTransport) registerCreateTask(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_create_task",
		Description: "Create a new task assigned to another agent in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params createTaskParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}
		if params.Assignee == "" {
			return toolError("assignee is required"), nil, nil
		}
		if params.Title == "" {
			return toolError("title is required"), nil, nil
		}

		task, err := t.hub.Tasks().CreateTask(ctx, sess.AgentID, params.Assignee, params.Title, params.Description)
		if err != nil {
			return toolError(err.Error()), nil, nil
		}

		// Store file attachments if provided.
		if len(params.Files) > 0 && t.hub.Attachments() != nil {
			for _, filePath := range params.Files {
				if _, storeErr := t.hub.Attachments().Store(ctx, task.TaskID, sess.AgentID, filePath); storeErr != nil {
					return toolError("store attachment failed: " + storeErr.Error()), nil, nil
				}
			}
		}

		data, _ := json.MarshalIndent(task, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Task created.\n" + string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_update_task ---

type updateTaskParams struct {
	TaskID      string   `json:"task_id" jsonschema:"ID of the task to update"`
	Status      string   `json:"status,omitempty" jsonschema:"new status: accepted, in_progress, completed, failed, rejected"`
	Description string   `json:"description,omitempty" jsonschema:"updated task description"`
	Summary     string   `json:"summary,omitempty" jsonschema:"completion summary"`
	Reason      string   `json:"reason,omitempty" jsonschema:"reason for rejection or failure"`
	Files       []string `json:"files,omitempty" jsonschema:"optional list of local file paths to attach"`
}

func (t *MCPHTTPTransport) registerUpdateTask(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_update_task",
		Description: "Update the status or details of a task in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params updateTaskParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}
		if params.TaskID == "" {
			return toolError("task_id is required"), nil, nil
		}

		update := core.TaskUpdate{
			Status:      protocol.TaskStatus(params.Status),
			Description: params.Description,
			Summary:     params.Summary,
			Reason:      params.Reason,
		}

		task, err := t.hub.Tasks().UpdateTask(ctx, sess.AgentID, params.TaskID, update)
		if err != nil {
			return toolError(err.Error()), nil, nil
		}

		// Store file attachments if provided.
		if len(params.Files) > 0 && t.hub.Attachments() != nil {
			for _, filePath := range params.Files {
				if _, storeErr := t.hub.Attachments().Store(ctx, task.TaskID, sess.AgentID, filePath); storeErr != nil {
					return toolError("store attachment failed: " + storeErr.Error()), nil, nil
				}
			}
		}

		data, _ := json.MarshalIndent(task, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Task updated.\n" + string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_get_task ---

type getTaskParams struct {
	TaskID string `json:"task_id" jsonschema:"ID of the task to retrieve"`
}

func (t *MCPHTTPTransport) registerGetTask(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_get_task",
		Description: "Get full details of a task by ID, including status, description, and attachment list.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params getTaskParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}
		if params.TaskID == "" {
			return toolError("task_id is required"), nil, nil
		}

		task, err := t.hub.Tasks().GetTask(ctx, params.TaskID)
		if err != nil {
			return toolError(err.Error()), nil, nil
		}
		if task == nil {
			return toolError("task not found: " + params.TaskID), nil, nil
		}

		data, _ := json.MarshalIndent(task, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_list_tasks ---

type listTasksParams struct {
	Status    string `json:"status,omitempty" jsonschema:"filter by status: requested, accepted, in_progress, completed, failed, rejected"`
	Requester string `json:"requester,omitempty" jsonschema:"filter by requester agent ID"`
	Assignee  string `json:"assignee,omitempty" jsonschema:"filter by assignee agent ID"`
}

func (t *MCPHTTPTransport) registerListTasks(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_list_tasks",
		Description: "List tasks in the Bifrost network, optionally filtered by status, requester, or assignee.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params listTasksParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}

		filter := store.TaskFilter{
			Status:    protocol.TaskStatus(params.Status),
			Requester: params.Requester,
			Assignee:  params.Assignee,
		}

		tasks, err := t.hub.Tasks().ListTasks(ctx, filter)
		if err != nil {
			return toolError(err.Error()), nil, nil
		}

		if len(tasks) == 0 {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "No tasks found."}},
			}, nil, nil
		}

		data, _ := json.MarshalIndent(tasks, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_subscribe ---

type subscribeParams struct {
	Target string `json:"target" jsonschema:"channel or task target to subscribe to (e.g. channel:general, task:id)"`
}

func (t *MCPHTTPTransport) registerSubscribe(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_subscribe",
		Description: "Subscribe to a channel or task target to receive notifications from it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params subscribeParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}
		if params.Target == "" {
			return toolError("target is required"), nil, nil
		}

		if err := t.hub.Channels().Subscribe(ctx, sess.AgentID, params.Target); err != nil {
			return toolError(err.Error()), nil, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Subscribed to %s.", params.Target)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_list_channels ---

func (t *MCPHTTPTransport) registerListChannels(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_list_channels",
		Description: "List all known channels in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}

		channels, err := t.hub.Channels().ListChannels(ctx)
		if err != nil {
			return toolError(err.Error()), nil, nil
		}

		data, _ := json.MarshalIndent(channels, "", "  ")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_dnd ---

type dndParams struct {
	Enabled bool   `json:"enabled" jsonschema:"true to enable DND, false to disable"`
	Reason  string `json:"reason,omitempty" jsonschema:"optional reason shown to other agents while in DND mode"`
}

func (t *MCPHTTPTransport) registerDND(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_dnd",
		Description: "Enable or disable Do Not Disturb mode. While enabled, incoming messages are queued.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params dndParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}

		if params.Enabled {
			if err := t.hub.DND().Enable(ctx, sess.AgentID, params.Reason); err != nil {
				return toolError(err.Error()), nil, nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "DND mode enabled. Incoming messages will be queued."}},
			}, nil, nil
		}

		flushed, err := t.hub.DND().Disable(ctx, sess.AgentID)
		if err != nil {
			return toolError(err.Error()), nil, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("DND mode disabled. %d queued messages flushed.", flushed)}},
		}, nil, nil
	})
}

// --- Tool: bifrost_peer ---

type peerParams struct {
	Action string `json:"action" jsonschema:"action to perform: 'new' to generate a magic code, 'join' to connect using a code"`
	Code   string `json:"code,omitempty" jsonschema:"magic code (required for 'join' action)"`
}

func (t *MCPHTTPTransport) registerPeer(srv *mcp.Server, sess *MCPSession) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_peer",
		Description: "Manage federation peering. Use 'new' to generate a magic code, or 'join' with a code to connect to a remote hub.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, params peerParams) (*mcp.CallToolResult, any, error) {
		t.touchSession(sess)
		if err := ensureRegistered(sess); err != nil {
			return toolError(err.Error()), nil, nil
		}

		// Peer operations are not available via the direct MCP transport yet.
		// The hub's federation forwarder is set at the hub level, not exposed
		// directly. For now, return a clear message.
		return toolError("peer operations are not yet available via the direct MCP transport"), nil, nil
	})
}

// --- Helpers ---

func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}

func localHostname() string {
	h, _ := os.Hostname()
	if h == "" {
		h = "unknown"
	}
	return h
}

func localUsername() string {
	u := os.Getenv("USER")
	if u == "" {
		u = os.Getenv("USERNAME")
	}
	if u == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			u = filepath.Base(home)
		}
	}
	if u == "" {
		u = "unknown"
	}
	return u
}
