# Plan 4: Direct MCP Mode + Claude Code Plugin Packaging

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Claude Code can connect to the bifrost hub directly via MCP Streamable HTTP (no shim process), and bifrost ships as a Claude Code plugin with guided setup, binary version management, and clear configuration examples.

**Architecture:** The hub gains an optional HTTP listener (`/mcp` endpoint) that speaks MCP Streamable HTTP + SSE. Each connected MCP client gets a session mapped to an `agent_id`. The plugin directory packages the Go binary with a `.mcp.json`, a `plugin.json`, and a `/bifrost:setup` skill. A binary version checker ensures the correct bifrost version is available.

**Tech Stack:** Go 1.22+, `modelcontextprotocol/go-sdk` (`mcp.NewStreamableHTTPHandler`), `net/http`

**Spec:** `docs/superpowers/specs/2026-03-27-bifrost-design.md`

**Dependencies:** Plans 1-3 must be complete (hub core, shim, federation). This plan adds a new transport and packaging layer.

---

### Task 1: MCP HTTP Transport — Session and Handler

**Files:**
- Create: `internal/transport/mcp_http.go`
- Create: `internal/transport/mcp_session.go`
- Test: `internal/transport/mcp_http_test.go`

- [ ] **Step 1: Write mcp_session.go — per-client session state**

```go
// internal/transport/mcp_session.go
package transport

import (
	"sync"
	"time"
)

// MCPSession tracks the state of a single MCP HTTP client connected to the hub.
type MCPSession struct {
	SessionID   string
	AgentID     string
	ProjectName string
	Hostname    string
	Username    string
	LocalPath   string
	CreatedAt   time.Time
	LastSeen    time.Time
	Registered  bool // true after bifrost_whoami or bifrost_list_agents first call
}

// MCPSessionStore manages active MCP HTTP sessions.
type MCPSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*MCPSession // session ID -> session
	byAgent  map[string]string      // agent ID -> session ID
}

// NewMCPSessionStore creates a new session store.
func NewMCPSessionStore() *MCPSessionStore {
	return &MCPSessionStore{
		sessions: make(map[string]*MCPSession),
		byAgent:  make(map[string]string),
	}
}

// Create adds a new session to the store. Returns the session.
func (s *MCPSessionStore) Create(sessionID string) *MCPSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := &MCPSession{
		SessionID: sessionID,
		CreatedAt: time.Now(),
		LastSeen:  time.Now(),
	}
	s.sessions[sessionID] = sess
	return sess
}

// Get retrieves a session by ID. Returns nil if not found.
func (s *MCPSessionStore) Get(sessionID string) *MCPSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID]
}

// GetByAgent retrieves the session ID for a given agent ID.
func (s *MCPSessionStore) GetByAgent(agentID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sid, ok := s.byAgent[agentID]
	return sid, ok
}

// Register binds an agent ID to a session. Called during the first tool
// invocation that provides project identity info.
func (s *MCPSessionStore) Register(sessionID, agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	sess.AgentID = agentID
	sess.Registered = true
	s.byAgent[agentID] = sessionID
}

// Touch updates the last-seen timestamp for a session.
func (s *MCPSessionStore) Touch(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sessionID]; ok {
		sess.LastSeen = time.Now()
	}
}

// Remove deletes a session and its agent mapping.
func (s *MCPSessionStore) Remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	if sess.AgentID != "" {
		delete(s.byAgent, sess.AgentID)
	}
	delete(s.sessions, sessionID)
}

// ActiveSessions returns the count of active sessions.
func (s *MCPSessionStore) ActiveSessions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// AllSessions returns a snapshot of all sessions.
func (s *MCPSessionStore) AllSessions() []*MCPSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*MCPSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, sess)
	}
	return result
}
```

- [ ] **Step 2: Write mcp_http.go — Streamable HTTP endpoint with tool registration**

```go
// internal/transport/mcp_http.go
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPHTTPTransport runs the MCP Streamable HTTP endpoint on the hub.
type MCPHTTPTransport struct {
	hub      *hub.Hub
	cfg      config.MCPConfig
	logger   *slog.Logger
	sessions *MCPSessionStore
	handler  *mcp.StreamableHTTPHandler
	server   *http.Server
	mu       sync.Mutex
	running  bool
}

// NewMCPHTTPTransport creates a new MCP HTTP transport for the hub.
func NewMCPHTTPTransport(h *hub.Hub, cfg config.MCPConfig, logger *slog.Logger) *MCPHTTPTransport {
	t := &MCPHTTPTransport{
		hub:      h,
		cfg:      cfg,
		logger:   logger,
		sessions: NewMCPSessionStore(),
	}
	return t
}

// Start begins listening for MCP HTTP connections.
func (t *MCPHTTPTransport) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		return fmt.Errorf("mcp_http: already running")
	}

	// Create the StreamableHTTPHandler. Each new session gets its own MCP Server
	// with the bifrost tools registered. The getServer callback is called once
	// per new MCP session (new Mcp-Session-Id).
	t.handler = mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return t.newSessionServer(req)
	}, &mcp.StreamableHTTPOptions{})

	mux := http.NewServeMux()
	mux.Handle("/mcp", t.handler)

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(t.cfg.Port))
	t.server = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mcp_http: listen on %s: %w", addr, err)
	}

	t.running = true
	t.logger.Info("MCP HTTP transport started", "addr", addr)

	go func() {
		if err := t.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.logger.Error("MCP HTTP transport serve error", "err", err)
		}
	}()

	go func() {
		<-ctx.Done()
		t.Stop()
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.logger.Info("MCP HTTP transport stopping")
	return t.server.Shutdown(ctx)
}

// Sessions returns the session store (used by the hub for notifications).
func (t *MCPHTTPTransport) Sessions() *MCPSessionStore {
	return t.sessions
}

// newSessionServer creates a fresh MCP Server instance for a new HTTP session.
// The go-sdk handles session ID assignment and Mcp-Session-Id headers.
func (t *MCPHTTPTransport) newSessionServer(req *http.Request) *mcp.Server {
	srv := mcp.NewServer(
		"bifrost",
		protocol.ProtocolVersion,
		&mcp.ServerOptions{
			Instructions: mcpInstructions,
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{ListChanged: true},
				Experimental: map[string]any{
					"claude/channel": map[string]any{},
				},
			},
			Logger: t.logger,
			InitializedHandler: func(ctx context.Context, ir *mcp.InitializedRequest) {
				// Session is now fully initialized. We can extract the
				// session ID from the server and create our internal session.
				sid := srv.SessionID()
				t.sessions.Create(sid)
				t.logger.Info("MCP HTTP session initialized", "session_id", sid)
			},
		},
	)

	// Register all bifrost tools on this server instance.
	t.registerTools(srv)

	return srv
}

const mcpInstructions = `Messages from other agents arrive as <channel source="bifrost" ...>.
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

// registerTools adds all bifrost MCP tools to the given server.
func (t *MCPHTTPTransport) registerTools(srv *mcp.Server) {
	t.registerWhoami(srv)
	t.registerListAgents(srv)
	t.registerSend(srv)
	t.registerListConversations(srv)
	t.registerCreateTask(srv)
	t.registerUpdateTask(srv)
	t.registerGetTask(srv)
	t.registerListTasks(srv)
	t.registerSubscribe(srv)
	t.registerListChannels(srv)
	t.registerDND(srv)
}

// sessionFromCtx extracts the MCP session ID from the request context
// (set by the go-sdk StreamableHTTPHandler) and returns our MCPSession.
func (t *MCPHTTPTransport) sessionFromCtx(ctx context.Context) (*MCPSession, error) {
	sid := mcp.SessionIDFromContext(ctx)
	if sid == "" {
		return nil, fmt.Errorf("no MCP session in context")
	}
	sess := t.sessions.Get(sid)
	if sess == nil {
		return nil, fmt.Errorf("unknown MCP session: %s", sid)
	}
	t.sessions.Touch(sid)
	return sess, nil
}

// ensureRegistered checks that the session has been bound to an agent.
// If not, returns an error prompting the client to call bifrost_whoami first.
func (t *MCPHTTPTransport) ensureRegistered(sess *MCPSession) error {
	if !sess.Registered {
		return fmt.Errorf("agent not registered: call bifrost_whoami first to register this session")
	}
	return nil
}

// --- Tool: bifrost_whoami ---

type WhoamiParams struct {
	ProjectName string `json:"project_name"`
	LocalPath   string `json:"local_path"`
	DisplayName string `json:"display_name,omitempty"`
}

func (t *MCPHTTPTransport) registerWhoami(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_whoami",
		Description: "Register this agent and return its identity, aliases, hub info, and DND status.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"project_name": map[string]any{"type": "string", "description": "Project name (directory name or from package.json/go.mod)"},
				"local_path":   map[string]any{"type": "string", "description": "Working directory path"},
				"display_name": map[string]any{"type": "string", "description": "Optional human-friendly display name"},
			},
			Required: []string{"project_name", "local_path"},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params WhoamiParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}

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
			Status:          protocol.AgentOnline,
			ConnectedAt:     sess.CreatedAt,
			LastSeen:        time.Now(),
			ProtocolVersion: protocol.ProtocolVersion,
			Capabilities:    detectCapabilities(params.LocalPath),
		}

		result, err := t.hub.RegisterAgent(ctx, agent)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_list_agents ---

type ListAgentsParams struct {
	Status string `json:"status,omitempty"` // "online" | "all"
}

func (t *MCPHTTPTransport) registerListAgents(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_list_agents",
		Description: "List connected agents with aliases, status, and hub origin.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"status": map[string]any{"type": "string", "enum": []string{"online", "all"}, "description": "Filter by status. Default: online."},
			},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params ListAgentsParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		status := params.Status
		if status == "" {
			status = "online"
		}

		agents, err := t.hub.ListAgents(ctx, status)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(agents)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_send ---

type SendParams struct {
	To             string `json:"to"`
	Body           string `json:"body"`
	Type           string `json:"type,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	InReplyTo      string `json:"in_reply_to,omitempty"`
	Priority       string `json:"priority,omitempty"`
	Files          []string `json:"files,omitempty"`
}

func (t *MCPHTTPTransport) registerSend(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_send",
		Description: "Send a message to an agent, channel, or task.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"to":              map[string]any{"type": "string", "description": "Recipient: agent:name, channel:name, or task:id"},
				"body":            map[string]any{"type": "string", "description": "Message body"},
				"type":            map[string]any{"type": "string", "enum": []string{"QUESTION", "ANSWER", "CONTEXT", "STATUS", "ERROR"}, "description": "Message type. Default: CONTEXT"},
				"conversation_id": map[string]any{"type": "string", "description": "Continue an existing conversation"},
				"in_reply_to":     map[string]any{"type": "string", "description": "Message ID for threading"},
				"priority":        map[string]any{"type": "string", "enum": []string{"low", "normal", "urgent"}, "description": "Priority. Default: normal"},
				"files":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "File paths to attach (only valid for task:id targets)"},
			},
			Required: []string{"to", "body"},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params SendParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		msgType := protocol.MessageType(params.Type)
		if msgType == "" {
			msgType = protocol.MsgContext
		}
		priority := protocol.Priority(params.Priority)
		if priority == "" {
			priority = protocol.PriorityNormal
		}

		msg := protocol.Message{
			From:           sess.AgentID,
			To:             params.To,
			Type:           msgType,
			Body:           params.Body,
			ConversationID: params.ConversationID,
			InReplyTo:      params.InReplyTo,
			Priority:       priority,
			Timestamp:      time.Now(),
		}

		result, err := t.hub.SendMessage(ctx, msg, params.Files)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_list_conversations ---

type ListConversationsParams struct {
	ActiveOnly *bool `json:"active_only,omitempty"`
}

func (t *MCPHTTPTransport) registerListConversations(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_list_conversations",
		Description: "List conversations this agent participates in.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"active_only": map[string]any{"type": "boolean", "description": "Only show active conversations. Default: true"},
			},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params ListConversationsParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		activeOnly := true
		if params.ActiveOnly != nil {
			activeOnly = *params.ActiveOnly
		}

		convs, err := t.hub.ListConversations(ctx, sess.AgentID, activeOnly)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(convs)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_create_task ---

type CreateTaskParams struct {
	Assignee    string   `json:"assignee"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Files       []string `json:"files,omitempty"`
}

func (t *MCPHTTPTransport) registerCreateTask(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_create_task",
		Description: "Create a task and assign it to another agent.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"assignee":    map[string]any{"type": "string", "description": "Agent name or ID to assign the task to"},
				"title":       map[string]any{"type": "string", "description": "Short task title"},
				"description": map[string]any{"type": "string", "description": "Detailed context, spec, requirements"},
				"files":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "File paths to attach"},
			},
			Required: []string{"assignee", "title", "description"},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params CreateTaskParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		result, err := t.hub.CreateTask(ctx, sess.AgentID, params.Assignee, params.Title, params.Description, params.Files)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_update_task ---

type UpdateTaskParams struct {
	TaskID      string `json:"task_id"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Files       []string `json:"files,omitempty"`
}

func (t *MCPHTTPTransport) registerUpdateTask(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_update_task",
		Description: "Update a task's status or add context/files.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"task_id":     map[string]any{"type": "string", "description": "Task ID"},
				"status":      map[string]any{"type": "string", "enum": []string{"accepted", "in_progress", "completed", "failed", "rejected"}, "description": "New status"},
				"description": map[string]any{"type": "string", "description": "Append context or notes"},
				"summary":     map[string]any{"type": "string", "description": "Completion summary"},
				"reason":      map[string]any{"type": "string", "description": "Rejection/failure reason"},
				"files":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional file attachments"},
			},
			Required: []string{"task_id"},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params UpdateTaskParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		result, err := t.hub.UpdateTask(ctx, sess.AgentID, params.TaskID, params.Status, params.Description, params.Summary, params.Reason, params.Files)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_get_task ---

type GetTaskParams struct {
	TaskID             string `json:"task_id"`
	IncludeAttachments bool   `json:"include_attachments,omitempty"`
}

func (t *MCPHTTPTransport) registerGetTask(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_get_task",
		Description: "Get full task details, optionally downloading attachments.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"task_id":              map[string]any{"type": "string", "description": "Task ID"},
				"include_attachments":  map[string]any{"type": "boolean", "description": "Download attachments to temp dir and return paths. Default: false"},
			},
			Required: []string{"task_id"},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params GetTaskParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		result, err := t.hub.GetTask(ctx, sess.AgentID, params.TaskID, params.IncludeAttachments)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_list_tasks ---

type ListTasksParams struct {
	Status string `json:"status,omitempty"`
	Role   string `json:"role,omitempty"` // "requester" | "assignee" | "subscriber"
}

func (t *MCPHTTPTransport) registerListTasks(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_list_tasks",
		Description: "List tasks, optionally filtered by status or role.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"status": map[string]any{"type": "string", "enum": []string{"requested", "accepted", "in_progress", "completed", "failed", "rejected"}, "description": "Filter by status"},
				"role":   map[string]any{"type": "string", "enum": []string{"requester", "assignee", "subscriber"}, "description": "Filter by this agent's role"},
			},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params ListTasksParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		result, err := t.hub.ListTasks(ctx, sess.AgentID, params.Status, params.Role)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_subscribe ---

type SubscribeParams struct {
	Target string `json:"target"` // "channel:name" or "task:id"
}

func (t *MCPHTTPTransport) registerSubscribe(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_subscribe",
		Description: "Subscribe to a broadcast channel or task updates.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"target": map[string]any{"type": "string", "description": "Target: channel:name or task:id"},
			},
			Required: []string{"target"},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params SubscribeParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		result, err := t.hub.Subscribe(ctx, sess.AgentID, params.Target)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_list_channels ---

type ListChannelsParams struct{}

func (t *MCPHTTPTransport) registerListChannels(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_list_channels",
		Description: "List broadcast channels with subscriber counts.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params ListChannelsParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		result, err := t.hub.ListChannels(ctx)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Tool: bifrost_dnd ---

type DNDParams struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

func (t *MCPHTTPTransport) registerDND(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "bifrost_dnd",
		Description: "Enable or disable Do Not Disturb mode.",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"enabled": map[string]any{"type": "boolean", "description": "true to enable DND, false to disable"},
				"reason":  map[string]any{"type": "string", "description": "Reason displayed to agents who try to message you"},
			},
			Required: []string{"enabled"},
		},
	}, func(ctx context.Context, _ *mcp.ServerSession, params DNDParams) (*mcp.CallToolResult, error) {
		sess, err := t.sessionFromCtx(ctx)
		if err != nil {
			return toolError(err), nil
		}
		if err := t.ensureRegistered(sess); err != nil {
			return toolError(err), nil
		}

		result, err := t.hub.SetDND(ctx, sess.AgentID, params.Enabled, params.Reason)
		if err != nil {
			return toolError(err), nil
		}

		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.TextContent{Text: string(data)}},
		}, nil
	})
}

// --- Helpers ---

func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Text: err.Error()}},
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
	u, _ := os.UserHomeDir()
	if u == "" {
		return "unknown"
	}
	// Extract username from home dir path as a fallback.
	// Prefer os/user but keep it simple and avoid CGO.
	return filepath.Base(u)
}

func detectCapabilities(localPath string) []string {
	// Delegate to the detect package (implemented in Plan 1).
	return detect.ProjectCapabilities(localPath)
}
```

Note: Add the missing imports at the top of the file — `os`, `path/filepath`, and the `detect` package import (`github.com/marcfargas/bifrost/pkg/detect`). They are used by `localHostname`, `localUsername`, and `detectCapabilities`.

- [ ] **Step 3: Write mcp_http_test.go — unit tests for session store and transport creation**

```go
// internal/transport/mcp_http_test.go
package transport

import (
	"testing"
)

func TestMCPSessionStore_CreateAndGet(t *testing.T) {
	store := NewMCPSessionStore()

	sess := store.Create("sess-1")
	if sess == nil {
		t.Fatal("expected non-nil session")
	}
	if sess.SessionID != "sess-1" {
		t.Errorf("expected session ID sess-1, got %s", sess.SessionID)
	}

	got := store.Get("sess-1")
	if got == nil {
		t.Fatal("expected to find session")
	}
	if got.SessionID != "sess-1" {
		t.Errorf("expected session ID sess-1, got %s", got.SessionID)
	}

	missing := store.Get("nonexistent")
	if missing != nil {
		t.Error("expected nil for missing session")
	}
}

func TestMCPSessionStore_Register(t *testing.T) {
	store := NewMCPSessionStore()
	store.Create("sess-1")

	store.Register("sess-1", "agent-abc")

	sess := store.Get("sess-1")
	if !sess.Registered {
		t.Error("expected session to be registered")
	}
	if sess.AgentID != "agent-abc" {
		t.Errorf("expected agent ID agent-abc, got %s", sess.AgentID)
	}

	sid, ok := store.GetByAgent("agent-abc")
	if !ok {
		t.Error("expected to find session by agent")
	}
	if sid != "sess-1" {
		t.Errorf("expected session ID sess-1, got %s", sid)
	}
}

func TestMCPSessionStore_Remove(t *testing.T) {
	store := NewMCPSessionStore()
	store.Create("sess-1")
	store.Register("sess-1", "agent-abc")

	store.Remove("sess-1")

	if store.Get("sess-1") != nil {
		t.Error("expected session to be removed")
	}
	if _, ok := store.GetByAgent("agent-abc"); ok {
		t.Error("expected agent mapping to be removed")
	}
	if store.ActiveSessions() != 0 {
		t.Errorf("expected 0 active sessions, got %d", store.ActiveSessions())
	}
}

func TestMCPSessionStore_Touch(t *testing.T) {
	store := NewMCPSessionStore()
	sess := store.Create("sess-1")
	original := sess.LastSeen

	// Touch should update LastSeen.
	store.Touch("sess-1")
	updated := store.Get("sess-1")
	if updated.LastSeen.Before(original) {
		t.Error("expected LastSeen to be updated")
	}
}

func TestMCPSessionStore_AllSessions(t *testing.T) {
	store := NewMCPSessionStore()
	store.Create("sess-1")
	store.Create("sess-2")
	store.Create("sess-3")

	all := store.AllSessions()
	if len(all) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(all))
	}
}

func TestMCPSessionStore_RegisterNonexistentSession(t *testing.T) {
	store := NewMCPSessionStore()

	// Should not panic on nonexistent session.
	store.Register("nonexistent", "agent-abc")

	if _, ok := store.GetByAgent("agent-abc"); ok {
		t.Error("should not register agent for nonexistent session")
	}
}

func TestMCPSessionStore_RemoveNonexistent(t *testing.T) {
	store := NewMCPSessionStore()

	// Should not panic.
	store.Remove("nonexistent")
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/transport/ -v -run TestMCPSession
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/transport/mcp_session.go internal/transport/mcp_http.go internal/transport/mcp_http_test.go
git commit -m "feat: add MCP Streamable HTTP transport with session management"
```

---

### Task 2: Hub Integration — Wire MCP HTTP Transport into the Hub Server

**Files:**
- Modify: `internal/hub/server.go`
- Modify: `internal/hub/connections.go`
- Modify: `internal/hub/notifications.go`

- [ ] **Step 1: Add MCP HTTP transport lifecycle to server.go**

Add the MCP HTTP transport start/stop to the hub server orchestrator. The transport is only started when `hub.mcp.enabled = true`.

```go
// Add to internal/hub/server.go — in the Server struct:

type Server struct {
	// ... existing fields ...
	mcpHTTP *transport.MCPHTTPTransport // nil if MCP HTTP is disabled
}

// Add to the Start method, after the local socket/pipe listener starts:

func (s *Server) startMCPHTTP(ctx context.Context) error {
	if !s.cfg.Hub.MCP.Enabled {
		s.logger.Info("MCP HTTP transport disabled")
		return nil
	}

	s.mcpHTTP = transport.NewMCPHTTPTransport(s.hub, s.cfg.Hub.MCP, s.logger)
	if err := s.mcpHTTP.Start(ctx); err != nil {
		return fmt.Errorf("hub: start MCP HTTP: %w", err)
	}

	// Register the MCP HTTP transport with the connection manager so
	// notifications can be routed to HTTP-connected agents.
	s.connections.RegisterTransport("mcp_http", s.mcpHTTP)

	return nil
}

// Add to the Stop method, before closing the local socket:

func (s *Server) stopMCPHTTP() {
	if s.mcpHTTP != nil {
		if err := s.mcpHTTP.Stop(); err != nil {
			s.logger.Error("MCP HTTP transport stop error", "err", err)
		}
	}
}
```

- [ ] **Step 2: Extend connections.go — support MCP HTTP agent connections**

Add an interface that the connection manager uses to deliver notifications across transports. The MCP HTTP transport implements this interface by sending MCP notifications through the go-sdk server session's SSE stream.

```go
// Add to internal/hub/connections.go:

// Transport is implemented by each transport layer to allow the connection
// manager to deliver notifications to agents regardless of how they connected.
type Transport interface {
	// CanDeliver returns true if this transport can deliver to the given agent.
	CanDeliver(agentID string) bool

	// Deliver sends a notification to the given agent.
	Deliver(ctx context.Context, agentID string, notification []byte) error
}

// RegisterTransport adds a named transport to the connection manager.
func (cm *ConnectionManager) RegisterTransport(name string, t Transport) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.transports[name] = t
}

// In the ConnectionManager struct, add:
//   transports map[string]Transport
//
// Initialize in NewConnectionManager:
//   transports: make(map[string]Transport),
```

- [ ] **Step 3: Implement Transport interface on MCPHTTPTransport**

```go
// Add to internal/transport/mcp_http.go:

// CanDeliver returns true if the given agent has an active MCP HTTP session.
func (t *MCPHTTPTransport) CanDeliver(agentID string) bool {
	_, ok := t.sessions.GetByAgent(agentID)
	return ok
}

// Deliver sends a claude/channel notification to the agent's MCP HTTP session
// via the SSE stream managed by the go-sdk StreamableHTTPHandler.
func (t *MCPHTTPTransport) Deliver(ctx context.Context, agentID string, notification []byte) error {
	sid, ok := t.sessions.GetByAgent(agentID)
	if !ok {
		return fmt.Errorf("mcp_http: no session for agent %s", agentID)
	}

	sess := t.sessions.Get(sid)
	if sess == nil {
		return fmt.Errorf("mcp_http: session %s not found", sid)
	}

	// The go-sdk handles SSE delivery when we send a notification through
	// the MCP Server instance. We need to find the server session and push
	// the notification. The handler maintains server instances per session.
	//
	// We use the handler's SendNotification method which routes to the
	// correct SSE stream via session ID.
	return t.handler.SendNotification(ctx, sid, "notifications/claude/channel", notification)
}
```

Note: The exact API for sending notifications to a specific session depends on the go-sdk version. If `StreamableHTTPHandler.SendNotification` is not available, the alternative is to keep a reference to each `*mcp.Server` instance created in `newSessionServer` and call `srv.SendNotificationToClient` on it. In that case, add a `servers map[string]*mcp.Server` field to `MCPHTTPTransport` and populate it in `newSessionServer`:

```go
// Alternative approach — store server references:

// Add field to MCPHTTPTransport:
//   servers map[string]*mcp.Server  // session ID -> MCP server

// In newSessionServer, after creating srv:
//   t.servers[srv.SessionID()] = srv

// In Deliver:
func (t *MCPHTTPTransport) Deliver(ctx context.Context, agentID string, notification []byte) error {
	sid, ok := t.sessions.GetByAgent(agentID)
	if !ok {
		return fmt.Errorf("mcp_http: no session for agent %s", agentID)
	}

	t.mu.Lock()
	srv, ok := t.servers[sid]
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("mcp_http: no MCP server for session %s", sid)
	}

	// Send claude/channel notification via the server's SSE stream.
	return srv.SendNotificationToClient(ctx, "notifications/claude/channel", notification)
}
```

- [ ] **Step 4: Update notifications.go — route through transports**

```go
// Modify internal/hub/notifications.go — in the Notify method:

func (d *Dispatcher) Notify(ctx context.Context, agentID string, notification []byte) error {
	// First try local socket/pipe connections (existing logic).
	if conn := d.connections.GetLocal(agentID); conn != nil {
		return conn.Send(notification)
	}

	// Then try registered transports (MCP HTTP, TCP, etc.).
	for name, t := range d.connections.Transports() {
		if t.CanDeliver(agentID) {
			d.logger.Debug("delivering via transport", "transport", name, "agent", agentID)
			return t.Deliver(ctx, agentID, notification)
		}
	}

	// Agent not reachable via any transport — queue the message.
	return d.queue(agentID, notification)
}
```

```go
// Add to ConnectionManager:

// Transports returns a snapshot of all registered transports.
func (cm *ConnectionManager) Transports() map[string]Transport {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make(map[string]Transport, len(cm.transports))
	for k, v := range cm.transports {
		result[k] = v
	}
	return result
}
```

- [ ] **Step 5: Commit**

```bash
git add internal/hub/server.go internal/hub/connections.go internal/hub/notifications.go internal/transport/mcp_http.go
git commit -m "feat: wire MCP HTTP transport into hub server and notification dispatcher"
```

---

### Task 3: Hub CLI Flag and Config — Enable MCP HTTP via Config/CLI

**Files:**
- Modify: `internal/config/defaults.go`
- Modify: `cmd/bifrost/main.go` (hub start command)

- [ ] **Step 1: Ensure defaults for MCP config**

Verify `internal/config/defaults.go` already sets MCP defaults. It should, since the config struct was defined in Plan 1. Confirm:

```go
// In internal/config/defaults.go — DefaultConfig() should include:

func DefaultConfig() Config {
	return Config{
		Hub: HubConfig{
			// ... existing defaults ...
			MCP: MCPConfig{
				Enabled: false,
				Port:    7433,
			},
		},
		// ... rest of defaults ...
	}
}
```

If not present, add the `MCP` field with the above defaults.

- [ ] **Step 2: Add CLI flags for MCP HTTP to the hub start command**

```go
// In cmd/bifrost/main.go — hubStartCmd:

func init() {
	// ... existing flags ...

	hubStartCmd.Flags().Bool("hub.mcp.enabled", false, "Enable MCP Streamable HTTP endpoint")
	hubStartCmd.Flags().Int("hub.mcp.port", 7433, "MCP HTTP listen port")
}
```

Ensure the flag override logic in the `hub start` command applies these flags to the loaded config before starting the server:

```go
// In the hub start RunE:

if cmd.Flags().Changed("hub.mcp.enabled") {
	v, _ := cmd.Flags().GetBool("hub.mcp.enabled")
	cfg.Hub.MCP.Enabled = v
}
if cmd.Flags().Changed("hub.mcp.port") {
	v, _ := cmd.Flags().GetInt("hub.mcp.port")
	cfg.Hub.MCP.Port = v
}
```

- [ ] **Step 3: Commit**

```bash
git add internal/config/defaults.go cmd/bifrost/main.go
git commit -m "feat: add CLI flags and config defaults for MCP HTTP transport"
```

---

### Task 4: Plugin Packaging — plugin.json, .mcp.json, README

**Files:**
- Create: `plugin/.claude-plugin/plugin.json`
- Create: `plugin/.mcp.json`
- Create: `plugin/README.md`

- [ ] **Step 1: Write plugin.json**

```json
{
  "name": "bifrost",
  "version": "0.1.0",
  "description": "Cross-agent communication hub for Claude Code. Enables AI agents in separate projects to discover each other, exchange messages, delegate tasks, and collaborate.",
  "author": "marcfargas",
  "license": "LGPL-3.0",
  "homepage": "https://github.com/marcfargas/bifrost",
  "repository": "https://github.com/marcfargas/bifrost",
  "keywords": ["mcp", "agents", "collaboration", "multi-agent", "communication"]
}
```

- [ ] **Step 2: Write .mcp.json**

```json
{
  "mcpServers": {
    "bifrost": {
      "command": "bifrost",
      "args": ["shim"],
      "env": {}
    }
  }
}
```

- [ ] **Step 3: Write plugin/README.md**

```markdown
# Bifrost — Claude Code Plugin

Cross-agent communication hub for Claude Code. Enables AI agents running in
separate projects to discover each other, exchange messages, delegate tasks,
and collaborate — locally or across the internet.

## Installation

### Option A: As a Claude Code plugin (recommended)

```bash
claude plugin install marcfargas/bifrost
```

This installs the plugin and its MCP server configuration. The `bifrost`
binary must be on your PATH (see Binary Setup below).

### Option B: Manual MCP configuration

Add to your `.claude/settings.json`:

**Shim mode** (recommended — auto-starts hub, works offline):
```json
{
  "mcpServers": {
    "bifrost": {
      "command": "bifrost",
      "args": ["shim"]
    }
  }
}
```

**Direct MCP mode** (connects to a running hub via HTTP):
```json
{
  "mcpServers": {
    "bifrost": {
      "type": "url",
      "url": "http://localhost:7433/mcp"
    }
  }
}
```

Direct mode requires the hub to be running with MCP HTTP enabled:
```bash
bifrost hub start --hub.mcp.enabled=true
```

## Binary Setup

The `bifrost` binary must be available on your PATH.

### From GitHub Releases

Download the latest release for your platform from
[GitHub Releases](https://github.com/marcfargas/bifrost/releases).

### From source

```bash
go install github.com/marcfargas/bifrost/cmd/bifrost@latest
```

## Usage

Once installed, Claude Code agents automatically discover each other through
bifrost. Use the `/bifrost:setup` skill for guided configuration.

### Available tools

| Tool | Description |
|------|-------------|
| `bifrost_whoami` | Show this agent's identity and hub info |
| `bifrost_list_agents` | List connected agents |
| `bifrost_send` | Send a message to an agent, channel, or task |
| `bifrost_list_conversations` | List active conversations |
| `bifrost_create_task` | Delegate a task to another agent |
| `bifrost_update_task` | Update task status |
| `bifrost_get_task` | Get task details |
| `bifrost_list_tasks` | List tasks |
| `bifrost_subscribe` | Subscribe to channels or task updates |
| `bifrost_list_channels` | List broadcast channels |
| `bifrost_dnd` | Toggle Do Not Disturb mode |

### Connection modes

| Mode | How it works | Best for |
|------|-------------|----------|
| **Shim** | Plugin spawns `bifrost shim`, which auto-starts the hub | Single machine, zero config |
| **Direct MCP** | Claude Code connects to hub via HTTP | Shared hub, multiple users |
| **Plugin** | `claude --channels plugin:bifrost` | Full plugin integration |

## Configuration

Bifrost works with zero configuration for local use. For advanced setup
(federation, remote access), see the main project documentation.

Config file location:
- Linux: `~/.config/bifrost/config.toml`
- macOS: `~/Library/Application Support/bifrost/config.toml`
- Windows: `%APPDATA%/bifrost/config.toml`

## License

LGPL-3.0
```

- [ ] **Step 4: Commit**

```bash
git add plugin/.claude-plugin/plugin.json plugin/.mcp.json plugin/README.md
git commit -m "feat: add Claude Code plugin packaging (plugin.json, .mcp.json, README)"
```

---

### Task 5: Plugin Skill — /bifrost:setup

**Files:**
- Create: `plugin/skills/setup/SKILL.md`

- [ ] **Step 1: Write SKILL.md**

```markdown
---
name: setup
description: Guided setup and status check for the Bifrost cross-agent communication hub.
---

# /bifrost:setup

You are helping the user set up and verify their Bifrost installation. Follow
these steps in order, stopping if any step fails and explaining how to fix it.

## Step 1: Check binary

Run this command to check if the `bifrost` binary is installed and get its version:

```bash
bifrost version
```

**If the command fails** (not found):
- Tell the user: "The `bifrost` binary is not on your PATH."
- Offer two options:
  1. Install from source: `go install github.com/marcfargas/bifrost/cmd/bifrost@latest`
  2. Download from GitHub Releases: `https://github.com/marcfargas/bifrost/releases`
- After installation, re-run `bifrost version` to verify.

**If it succeeds**, show the version and protocol version. Continue to Step 2.

## Step 2: Check hub status

Run this command to check if a hub is already running:

```bash
bifrost hub status
```

**If the hub is running:**
- Show the status output (connected agents, uptime, listeners).
- Continue to Step 3.

**If the hub is NOT running:**
- Ask the user: "No hub is running. Would you like to:"
  1. **Start one now** (foreground): `bifrost hub start`
  2. **Start in background**: `bifrost hub start -d`
  3. **Skip** — the shim will auto-start a hub when needed.
- If the user wants to start with MCP HTTP enabled, use:
  `bifrost hub start -d --hub.mcp.enabled=true`
- After starting, re-run `bifrost hub status` to confirm.

## Step 3: Show connected agents

Run:

```bash
bifrost agents
```

- Display the list of connected agents with their project names, aliases, and status.
- If no agents are connected, explain: "No agents connected yet. When you start Claude Code sessions with the bifrost plugin or MCP server configured, they will appear here automatically."

## Step 4: Check MCP configuration

Read the user's Claude Code settings to see if bifrost is configured:

```bash
cat ~/.claude/settings.json 2>/dev/null || echo "No settings.json found"
```

On Windows, check `%APPDATA%/claude/settings.json` instead.

**If bifrost is already configured** in `mcpServers`:
- Show the current configuration.
- Confirm it looks correct.

**If bifrost is NOT configured:**
- Ask the user which mode they prefer:
  1. **Shim mode** (recommended): Add to settings.json:
     ```json
     {
       "mcpServers": {
         "bifrost": {
           "command": "bifrost",
           "args": ["shim"]
         }
       }
     }
     ```
  2. **Direct MCP mode**: Add to settings.json:
     ```json
     {
       "mcpServers": {
         "bifrost": {
           "type": "url",
           "url": "http://localhost:7433/mcp"
         }
       }
     }
     ```
     Remind the user that direct mode requires the hub to be running with
     `--hub.mcp.enabled=true`.

## Step 5: Federation (optional)

Ask the user: "Would you like to set up federation with another machine?"

**If yes:**
- Ask: "Are you creating a NEW federation or JOINING an existing one?"
  - **New**: Run `bifrost peer new` and show the generated magic code.
    Tell the user: "Share this code with the other machine. They should run:
    `bifrost peer join <CODE>`"
  - **Join**: Ask for the magic code, then run `bifrost peer join <CODE>`.
    Show the connection result and the list of remote agents.

**If no:**
- Skip. Tell the user: "You can set up federation later with `bifrost peer new` or the `bifrost_peer` tool."

## Step 6: Summary

Display a summary of the setup:

```
Bifrost Setup Summary
=====================
Binary:      bifrost vX.Y.Z (protocol vX.Y.Z)
Hub:         running / not running (auto-start via shim)
Agents:      N connected
MCP Config:  shim mode / direct mode / not configured
Federation:  N peers / not configured
```

Tell the user: "Setup complete. Your Claude Code agents can now communicate through Bifrost. Start a new Claude Code session to see it in action."
```

- [ ] **Step 2: Commit**

```bash
git add plugin/skills/setup/SKILL.md
git commit -m "feat: add /bifrost:setup skill for guided installation and configuration"
```

---

### Task 6: Binary Version Check and Auto-Download

**Files:**
- Create: `internal/release/version_check.go`
- Test: `internal/release/version_check_test.go`

- [ ] **Step 1: Write version_check.go**

```go
// internal/release/version_check.go
package release

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// GitHubRepo is the repository for bifrost releases.
	GitHubRepo = "marcfargas/bifrost"

	// DownloadURLTemplate is the pattern for release binary downloads.
	// Placeholders: version, os, arch, extension.
	DownloadURLTemplate = "https://github.com/%s/releases/download/v%s/bifrost_%s_%s_%s%s"
)

// VersionInfo holds parsed version output from `bifrost version`.
type VersionInfo struct {
	Version         string
	ProtocolVersion string
}

// CheckBinary verifies that the bifrost binary is on PATH and returns its version.
// Returns an error if the binary is not found or cannot be executed.
func CheckBinary() (*VersionInfo, error) {
	path, err := exec.LookPath("bifrost")
	if err != nil {
		return nil, fmt.Errorf("bifrost binary not found on PATH: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run bifrost version: %w", err)
	}

	return parseVersionOutput(string(out))
}

// parseVersionOutput extracts version and protocol version from the output
// of `bifrost version`. Expected format:
//
//	bifrost v0.1.0 (protocol v1.0.0)
func parseVersionOutput(output string) (*VersionInfo, error) {
	output = strings.TrimSpace(output)
	// Format: "bifrost v0.1.0 (protocol v1.0.0)"
	var ver, proto string
	_, err := fmt.Sscanf(output, "bifrost v%s (protocol v%s", &ver, &proto)
	if err != nil {
		return nil, fmt.Errorf("unexpected version output format: %q", output)
	}
	// Remove trailing ')' from protocol version.
	proto = strings.TrimSuffix(proto, ")")
	return &VersionInfo{
		Version:         ver,
		ProtocolVersion: proto,
	}, nil
}

// VersionMatch checks if the installed binary version matches the expected version.
func VersionMatch(installed, expected string) bool {
	return strings.TrimSpace(installed) == strings.TrimSpace(expected)
}

// DownloadBinary downloads the bifrost binary for the current platform from
// GitHub releases and places it in the given directory.
// Returns the path to the downloaded binary.
func DownloadBinary(ctx context.Context, version, destDir string) (string, error) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}

	url := fmt.Sprintf(DownloadURLTemplate, GitHubRepo, version, version, goos, goarch, ext)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download bifrost v%s: %w", version, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download bifrost v%s: HTTP %d", version, resp.StatusCode)
	}

	binaryName := "bifrost" + ext
	destPath := filepath.Join(destDir, binaryName)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create dest dir: %w", err)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("write binary: %w", err)
	}

	return destPath, nil
}

// EnsureBinary checks that the correct version of bifrost is available.
// If missing or wrong version, it downloads from GitHub releases.
// Returns the path to the binary.
func EnsureBinary(ctx context.Context, expectedVersion string) (string, error) {
	info, err := CheckBinary()
	if err == nil && VersionMatch(info.Version, expectedVersion) {
		// Correct version already installed.
		path, _ := exec.LookPath("bifrost")
		return path, nil
	}

	// Need to download. Place in user's local bin directory.
	var destDir string
	switch runtime.GOOS {
	case "windows":
		destDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "bifrost", "bin")
	case "darwin":
		home, _ := os.UserHomeDir()
		destDir = filepath.Join(home, ".local", "bin")
	default: // linux
		home, _ := os.UserHomeDir()
		destDir = filepath.Join(home, ".local", "bin")
	}

	return DownloadBinary(ctx, expectedVersion, destDir)
}
```

- [ ] **Step 2: Write tests**

```go
// internal/release/version_check_test.go
package release

import (
	"testing"
)

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantVer string
		wantProto string
		wantErr bool
	}{
		{
			name:      "standard format",
			input:     "bifrost v0.1.0 (protocol v1.0.0)",
			wantVer:   "0.1.0",
			wantProto: "1.0.0",
		},
		{
			name:      "with trailing newline",
			input:     "bifrost v1.2.3 (protocol v1.0.0)\n",
			wantVer:   "1.2.3",
			wantProto: "1.0.0",
		},
		{
			name:    "garbage input",
			input:   "not a version string",
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := parseVersionOutput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Version != tt.wantVer {
				t.Errorf("version: got %q, want %q", info.Version, tt.wantVer)
			}
			if info.ProtocolVersion != tt.wantProto {
				t.Errorf("protocol: got %q, want %q", info.ProtocolVersion, tt.wantProto)
			}
		})
	}
}

func TestVersionMatch(t *testing.T) {
	tests := []struct {
		installed string
		expected  string
		want      bool
	}{
		{"0.1.0", "0.1.0", true},
		{"0.1.0", "0.2.0", false},
		{" 0.1.0 ", "0.1.0", true}, // whitespace tolerance
		{"1.0.0", "1.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.installed+"_vs_"+tt.expected, func(t *testing.T) {
			got := VersionMatch(tt.installed, tt.expected)
			if got != tt.want {
				t.Errorf("VersionMatch(%q, %q) = %v, want %v", tt.installed, tt.expected, got, tt.want)
			}
		})
	}
}

func TestDownloadURLTemplate(t *testing.T) {
	// Verify the URL template produces valid URLs.
	url := fmt.Sprintf(DownloadURLTemplate, GitHubRepo, "0.1.0", "0.1.0", "linux", "amd64", "")
	expected := "https://github.com/marcfargas/bifrost/releases/download/v0.1.0/bifrost_0.1.0_linux_amd64"
	if url != expected {
		t.Errorf("got %q, want %q", url, expected)
	}

	urlWin := fmt.Sprintf(DownloadURLTemplate, GitHubRepo, "0.1.0", "0.1.0", "windows", "amd64", ".exe")
	expectedWin := "https://github.com/marcfargas/bifrost/releases/download/v0.1.0/bifrost_0.1.0_windows_amd64.exe"
	if urlWin != expectedWin {
		t.Errorf("got %q, want %q", urlWin, expectedWin)
	}
}
```

Note: Add `"fmt"` to the import block in the test file for `TestDownloadURLTemplate`.

- [ ] **Step 3: Run tests**

```bash
go test ./internal/release/ -v
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
mkdir -p internal/release
git add internal/release/version_check.go internal/release/version_check_test.go
git commit -m "feat: add binary version check and auto-download from GitHub releases"
```

---

### Task 7: Integration Tests — Direct MCP Mode

**Files:**
- Create: `test/integration/mcp_http_test.go`

- [ ] **Step 1: Write integration test — full MCP HTTP flow**

This test starts a hub with MCP HTTP enabled, connects an MCP client via HTTP, registers an agent, sends a message, and verifies channel notification delivery via SSE.

```go
// test/integration/mcp_http_test.go
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	hubpkg "github.com/marcfargas/bifrost/internal/hub"
)

// TestMCPHTTPDirectMode verifies the full lifecycle of a direct MCP HTTP
// connection: initialize session, register agent, list agents, send message.
func TestMCPHTTPDirectMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start hub with MCP HTTP enabled on a random port.
	cfg := config.DefaultConfig()
	cfg.Hub.MCP.Enabled = true
	cfg.Hub.MCP.Port = 0 // let OS pick a free port
	cfg.Storage.DataDir = t.TempDir()

	srv, err := hubpkg.NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("create hub: %v", err)
	}

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	defer srv.Stop()

	port := srv.MCPHTTPPort()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	// --- Phase 1: Initialize MCP session ---

	initReq := jsonRPCRequest("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "test-client",
			"version": "1.0.0",
		},
	}, 1)

	resp, sessionID := doMCPRequest(t, baseURL, "", initReq)
	if sessionID == "" {
		t.Fatal("expected Mcp-Session-Id header in response")
	}

	// Verify the server declared claude/channel capability.
	var initResult struct {
		Result struct {
			Capabilities struct {
				Experimental map[string]any `json:"experimental"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &initResult); err != nil {
		t.Fatalf("parse init response: %v", err)
	}
	if _, ok := initResult.Result.Capabilities.Experimental["claude/channel"]; !ok {
		t.Error("expected claude/channel in experimental capabilities")
	}

	// Send initialized notification.
	notif := jsonRPCNotification("notifications/initialized", nil)
	doMCPRequest(t, baseURL, sessionID, notif)

	// --- Phase 2: Register agent via bifrost_whoami ---

	whoamiReq := jsonRPCRequest("tools/call", map[string]any{
		"name": "bifrost_whoami",
		"arguments": map[string]any{
			"project_name": "test-project-alpha",
			"local_path":   "/tmp/test/alpha",
		},
	}, 2)

	whoamiResp, _ := doMCPRequest(t, baseURL, sessionID, whoamiReq)
	assertNoError(t, whoamiResp, "bifrost_whoami")

	// --- Phase 3: List agents (should see self) ---

	listReq := jsonRPCRequest("tools/call", map[string]any{
		"name":      "bifrost_list_agents",
		"arguments": map[string]any{"status": "all"},
	}, 3)

	listResp, _ := doMCPRequest(t, baseURL, sessionID, listReq)
	assertNoError(t, listResp, "bifrost_list_agents")

	if !strings.Contains(string(listResp), "test-project-alpha") {
		t.Error("expected to see test-project-alpha in agent list")
	}

	// --- Phase 4: Connect a second agent and exchange messages ---

	// Initialize second session.
	initReq2 := jsonRPCRequest("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client-2", "version": "1.0.0"},
	}, 1)
	_, sessionID2 := doMCPRequest(t, baseURL, "", initReq2)
	doMCPRequest(t, baseURL, sessionID2, jsonRPCNotification("notifications/initialized", nil))

	// Register second agent.
	whoami2 := jsonRPCRequest("tools/call", map[string]any{
		"name": "bifrost_whoami",
		"arguments": map[string]any{
			"project_name": "test-project-beta",
			"local_path":   "/tmp/test/beta",
		},
	}, 2)
	doMCPRequest(t, baseURL, sessionID2, whoami2)

	// Open SSE stream on second agent's session to receive notifications.
	sseCtx, sseCancel := context.WithTimeout(ctx, 10*time.Second)
	defer sseCancel()

	sseCh := openSSEStream(t, sseCtx, baseURL, sessionID2)

	// First agent sends message to second agent.
	sendReq := jsonRPCRequest("tools/call", map[string]any{
		"name": "bifrost_send",
		"arguments": map[string]any{
			"to":   "test-project-beta",
			"body": "Hello from alpha!",
			"type": "QUESTION",
		},
	}, 4)
	sendResp, _ := doMCPRequest(t, baseURL, sessionID, sendReq)
	assertNoError(t, sendResp, "bifrost_send")

	if !strings.Contains(string(sendResp), "delivered") {
		t.Errorf("expected delivery confirmation, got: %s", sendResp)
	}

	// Wait for SSE notification on second agent.
	select {
	case event := <-sseCh:
		if !strings.Contains(event, "Hello from alpha!") {
			t.Errorf("expected message content in SSE event, got: %s", event)
		}
		if !strings.Contains(event, "bifrost") {
			t.Errorf("expected bifrost source in SSE event, got: %s", event)
		}
	case <-sseCtx.Done():
		t.Fatal("timed out waiting for SSE notification")
	}
}

// TestMCPHTTPDisabledByDefault verifies MCP HTTP is not started when disabled.
func TestMCPHTTPDisabledByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := config.DefaultConfig()
	cfg.Hub.MCP.Enabled = false
	cfg.Hub.MCP.Port = 7433
	cfg.Storage.DataDir = t.TempDir()

	srv, err := hubpkg.NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("create hub: %v", err)
	}

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	defer srv.Stop()

	// Attempting to connect to MCP HTTP port should fail.
	client := &http.Client{Timeout: 2 * time.Second}
	_, err = client.Get("http://127.0.0.1:7433/mcp")
	if err == nil {
		t.Error("expected connection to fail when MCP HTTP is disabled")
	}
}

// TestMCPHTTPUnregisteredAgent verifies that tools require registration.
func TestMCPHTTPUnregisteredAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg := config.DefaultConfig()
	cfg.Hub.MCP.Enabled = true
	cfg.Hub.MCP.Port = 0
	cfg.Storage.DataDir = t.TempDir()

	srv, err := hubpkg.NewServer(cfg, nil)
	if err != nil {
		t.Fatalf("create hub: %v", err)
	}

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	defer srv.Stop()

	port := srv.MCPHTTPPort()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	// Initialize session.
	initReq := jsonRPCRequest("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "1.0.0"},
	}, 1)
	_, sessionID := doMCPRequest(t, baseURL, "", initReq)
	doMCPRequest(t, baseURL, sessionID, jsonRPCNotification("notifications/initialized", nil))

	// Try to list agents without calling bifrost_whoami first.
	listReq := jsonRPCRequest("tools/call", map[string]any{
		"name":      "bifrost_list_agents",
		"arguments": map[string]any{},
	}, 2)

	listResp, _ := doMCPRequest(t, baseURL, sessionID, listReq)

	// Should get an error telling user to call bifrost_whoami.
	if !strings.Contains(string(listResp), "bifrost_whoami") {
		t.Errorf("expected error mentioning bifrost_whoami, got: %s", listResp)
	}
}

// --- Test helpers ---

func jsonRPCRequest(method string, params any, id int) []byte {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      id,
	}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	return data
}

func jsonRPCNotification(method string, params any) []byte {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	return data
}

func doMCPRequest(t *testing.T, baseURL, sessionID string, body []byte) ([]byte, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" && sessionID == "" {
		// For the first request, the session ID should be in the response.
		sid = resp.Header.Get("Mcp-Session-Id")
	}

	return data, sid
}

func assertNoError(t *testing.T, resp []byte, toolName string) {
	t.Helper()

	var result struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("parse %s response: %v (raw: %s)", toolName, err, resp)
	}
	if result.Result.IsError {
		t.Errorf("%s returned error: %s", toolName, resp)
	}
}

func openSSEStream(t *testing.T, ctx context.Context, baseURL, sessionID string) <-chan string {
	t.Helper()
	ch := make(chan string, 10)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)

	go func() {
		defer close(ch)

		client := &http.Client{Timeout: 0} // no timeout for SSE
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()

		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				ch <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	return ch
}
```

- [ ] **Step 2: Run integration tests**

```bash
go test ./test/integration/ -v -run TestMCPHTTP -count=1
```

Expected: all three tests pass.

- [ ] **Step 3: Commit**

```bash
git add test/integration/mcp_http_test.go
git commit -m "test: add integration tests for direct MCP HTTP mode"
```

---

### Task 8: Hub Server — MCPHTTPPort Accessor and Port 0 Support

**Files:**
- Modify: `internal/hub/server.go`
- Modify: `internal/transport/mcp_http.go`

The integration tests use port 0 (OS-assigned) for test isolation. The hub and transport need to support this.

- [ ] **Step 1: Add port accessor to MCPHTTPTransport**

```go
// Add to internal/transport/mcp_http.go:

// Port returns the actual port the HTTP server is listening on.
// Useful when configured with port 0 (OS-assigned).
func (t *MCPHTTPTransport) Port() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.listener == nil {
		return 0
	}
	return t.listener.Addr().(*net.TCPAddr).Port
}
```

Refactor the `Start` method to store the listener:

```go
// Add field to MCPHTTPTransport:
//   listener net.Listener

// In Start, change the listener creation:
func (t *MCPHTTPTransport) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		return fmt.Errorf("mcp_http: already running")
	}

	t.handler = mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return t.newSessionServer(req)
	}, &mcp.StreamableHTTPOptions{})

	mux := http.NewServeMux()
	mux.Handle("/mcp", t.handler)

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(t.cfg.Port))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mcp_http: listen on %s: %w", addr, err)
	}

	t.listener = ln
	t.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	t.running = true
	actualAddr := ln.Addr().(*net.TCPAddr)
	t.logger.Info("MCP HTTP transport started", "addr", actualAddr.String(), "port", actualAddr.Port)

	go func() {
		if err := t.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.logger.Error("MCP HTTP transport serve error", "err", err)
		}
	}()

	go func() {
		<-ctx.Done()
		t.Stop()
	}()

	return nil
}
```

- [ ] **Step 2: Add MCPHTTPPort to hub Server**

```go
// Add to internal/hub/server.go:

// MCPHTTPPort returns the actual port the MCP HTTP transport is listening on.
// Returns 0 if MCP HTTP is not enabled or not started.
func (s *Server) MCPHTTPPort() int {
	if s.mcpHTTP == nil {
		return 0
	}
	return s.mcpHTTP.Port()
}
```

- [ ] **Step 3: Commit**

```bash
git add internal/transport/mcp_http.go internal/hub/server.go
git commit -m "feat: support OS-assigned port (port 0) for MCP HTTP transport"
```

---

### Task 9: Claude Code Configuration Examples

**Files:**
- Modify: `plugin/README.md` (already contains examples from Task 4 — verify correctness)

- [ ] **Step 1: Verify all three configuration examples are documented**

The `plugin/README.md` from Task 4 already contains the three modes. Verify by reading the file and confirming these exact JSON blocks are present:

**Direct MCP mode:**
```json
{
  "mcpServers": {
    "bifrost": {
      "type": "url",
      "url": "http://localhost:7433/mcp"
    }
  }
}
```

**Shim mode:**
```json
{
  "mcpServers": {
    "bifrost": {
      "command": "bifrost",
      "args": ["shim"]
    }
  }
}
```

**Plugin mode:**
```bash
claude --channels plugin:bifrost
```

All three are already in the README from Task 4. No additional changes needed.

- [ ] **Step 2: Add a configuration examples section to the main DESIGN.md**

Append the following section to the end of `DESIGN.md` (before the closing):

```markdown
## Claude Code Configuration

### Direct MCP mode (no shim, connects to running hub)

Requires: hub running with `--hub.mcp.enabled=true`

```json
{
  "mcpServers": {
    "bifrost": {
      "type": "url",
      "url": "http://localhost:7433/mcp"
    }
  }
}
```

### Shim mode (recommended, auto-starts hub)

```json
{
  "mcpServers": {
    "bifrost": {
      "command": "bifrost",
      "args": ["shim"]
    }
  }
}
```

### Plugin mode

```bash
claude --channels plugin:bifrost
```
```

- [ ] **Step 3: Commit**

```bash
git add DESIGN.md
git commit -m "docs: add Claude Code configuration examples to DESIGN.md"
```

---

### Summary

| Task | Description | Files | Commit |
|------|-------------|-------|--------|
| 1 | MCP HTTP transport + session store | `internal/transport/mcp_session.go`, `mcp_http.go`, `mcp_http_test.go` | `feat: add MCP Streamable HTTP transport with session management` |
| 2 | Wire transport into hub server | `internal/hub/server.go`, `connections.go`, `notifications.go`, `mcp_http.go` | `feat: wire MCP HTTP transport into hub server and notification dispatcher` |
| 3 | CLI flags and config defaults | `internal/config/defaults.go`, `cmd/bifrost/main.go` | `feat: add CLI flags and config defaults for MCP HTTP transport` |
| 4 | Plugin packaging | `plugin/.claude-plugin/plugin.json`, `plugin/.mcp.json`, `plugin/README.md` | `feat: add Claude Code plugin packaging` |
| 5 | /bifrost:setup skill | `plugin/skills/setup/SKILL.md` | `feat: add /bifrost:setup skill` |
| 6 | Binary version check | `internal/release/version_check.go`, `version_check_test.go` | `feat: add binary version check and auto-download` |
| 7 | Integration tests | `test/integration/mcp_http_test.go` | `test: add integration tests for direct MCP HTTP mode` |
| 8 | Port 0 support | `internal/transport/mcp_http.go`, `internal/hub/server.go` | `feat: support OS-assigned port for MCP HTTP transport` |
| 9 | Configuration examples | `DESIGN.md` | `docs: add Claude Code configuration examples` |

**Total:** 9 tasks, 14 files created/modified, 9 commits.

**Key design decisions:**
- Each MCP HTTP session maps 1:1 to an agent via `MCPSessionStore`
- Agent identity derived from hostname + local_path (same as shim mode)
- `bifrost_whoami` is the registration gate — other tools require it first
- SSE stream carries `claude/channel` notifications (push messages to connected agents)
- Hub creates a fresh `mcp.Server` per session via `NewStreamableHTTPHandler`'s `getServer` callback
- Port 0 support enables parallel integration tests without port conflicts
- Plugin packaging is minimal — just metadata and a pointer to the Go binary
