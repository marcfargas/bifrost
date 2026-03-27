package hub

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// Handler dispatches JSON-RPC 2.0 requests to the appropriate hub operation.
type Handler struct {
	hub     *core.Hub
	connMgr *ConnManager
}

// NewHandler creates a Handler backed by the given Hub and ConnManager.
func NewHandler(h *core.Hub, cm *ConnManager) *Handler {
	return &Handler{hub: h, connMgr: cm}
}

// Handle dispatches req to the appropriate method handler and returns an
// RPCResponse. Returns nil only when no response should be sent (not currently
// used, but reserved for notifications).
func (h *Handler) Handle(ctx context.Context, conn *transport.Conn, req *RPCRequest) *RPCResponse {
	switch req.Method {
	case "hub.register":
		return h.handleRegister(ctx, conn, req)
	case "hub.deregister":
		return h.handleDeregister(ctx, req)
	case "hub.heartbeat":
		return h.handleHeartbeat(ctx, req)
	case "hub.list_agents":
		return h.handleListAgents(ctx, req)
	case "msg.send":
		return h.handleSendMessage(ctx, req)
	case "hub.list_conversations":
		return h.handleListConversations(ctx, req)
	case "task.create":
		return h.handleCreateTask(ctx, req)
	case "task.update":
		return h.handleUpdateTask(ctx, req)
	case "task.get":
		return h.handleGetTask(ctx, req)
	case "task.list":
		return h.handleListTasks(ctx, req)
	case "task.attach":
		return h.handleAttachFile(ctx, req)
	case "channel.subscribe":
		return h.handleSubscribe(ctx, req)
	case "channel.unsubscribe":
		return h.handleUnsubscribe(ctx, req)
	case "channel.list":
		return h.handleListChannels(ctx, req)
	case "dnd.set":
		return h.handleDNDSet(ctx, req)
	case "dnd.status":
		return h.handleDNDStatus(ctx, req)
	default:
		return rpcError(req.ID, -32601, "method not found")
	}
}

// handleRegister parses an Agent from params, validates the protocol version,
// registers it with the hub, and records the connection in the ConnManager.
func (h *Handler) handleRegister(ctx context.Context, conn *transport.Conn, req *RPCRequest) *RPCResponse {
	var agent protocol.Agent
	if err := json.Unmarshal(req.Params, &agent); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if !protocol.CompatibleWith(agent.ProtocolVersion) {
		return rpcError(req.ID, -32600,
			"incompatible protocol version: "+agent.ProtocolVersion+
				" (hub requires major version "+protocol.ProtocolVersion+")")
	}

	if err := h.hub.Agents().Register(ctx, &agent); err != nil {
		return rpcError(req.ID, -32000, "register failed: "+err.Error())
	}

	h.connMgr.Add(agent.AgentID, conn)

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  agent,
	}
}

// handleDeregister parses an agent_id from params and marks the agent offline.
func (h *Handler) handleDeregister(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.AgentID == "" {
		return rpcError(req.ID, -32602, "agent_id is required")
	}

	if err := h.hub.Agents().Deregister(ctx, params.AgentID); err != nil {
		return rpcError(req.ID, -32000, "deregister failed: "+err.Error())
	}

	h.connMgr.Remove(params.AgentID)

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]bool{"ok": true},
	}
}

// handleHeartbeat touches an agent's last_seen timestamp.
func (h *Handler) handleHeartbeat(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.AgentID == "" {
		return rpcError(req.ID, -32602, "agent_id is required")
	}

	if err := h.hub.Store().TouchAgent(ctx, params.AgentID); err != nil {
		return rpcError(req.ID, -32000, "heartbeat failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]bool{"ok": true},
	}
}

// handleListAgents lists agents, optionally filtered by status.
func (h *Handler) handleListAgents(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		Status string `json:"status"`
	}
	// Params are optional — ignore unmarshal errors for missing/null params.
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}

	filter := store.AgentFilter{}
	if params.Status != "" {
		filter.Status = protocol.AgentStatus(params.Status)
	}

	agents, err := h.hub.Store().ListAgents(ctx, filter)
	if err != nil {
		return rpcError(req.ID, -32000, "list agents failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  agents,
	}
}

// handleSendMessage parses a Message from params and routes it via the hub.
func (h *Handler) handleSendMessage(ctx context.Context, req *RPCRequest) *RPCResponse {
	var msg protocol.Message
	if err := json.Unmarshal(req.Params, &msg); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if err := h.hub.Messages().Send(ctx, &msg); err != nil {
		if notFound, ok := errors.AsType[*core.AgentNotFoundError](err); ok {
			resp := rpcError(req.ID, -32001, err.Error())
			resp.Error.Data = notFound.Available
			return resp
		}
		return rpcError(req.ID, -32000, "send failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  msg,
	}
}

// handleListConversations lists conversations, optionally filtering to active only.
func (h *Handler) handleListConversations(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		ActiveOnly bool `json:"active_only"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}

	filter := store.ConversationFilter{}
	if params.ActiveOnly {
		closed := false
		filter.Closed = &closed
	}

	convs, err := h.hub.Store().ListConversations(ctx, filter)
	if err != nil {
		return rpcError(req.ID, -32000, "list conversations failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  convs,
	}
}

// handleCreateTask parses task creation params, creates the task, and
// optionally stores file attachments.
func (h *Handler) handleCreateTask(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		Requester   string   `json:"requester"`
		Assignee    string   `json:"assignee"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Files       []string `json:"files"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.Requester == "" {
		return rpcError(req.ID, -32602, "requester is required")
	}
	if params.Assignee == "" {
		return rpcError(req.ID, -32602, "assignee is required")
	}
	if params.Title == "" {
		return rpcError(req.ID, -32602, "title is required")
	}

	task, err := h.hub.Tasks().CreateTask(ctx, params.Requester, params.Assignee, params.Title, params.Description)
	if err != nil {
		return rpcError(req.ID, -32000, "create task failed: "+err.Error())
	}

	if len(params.Files) > 0 && h.hub.Attachments() != nil {
		for _, filePath := range params.Files {
			if _, storeErr := h.hub.Attachments().Store(ctx, task.TaskID, params.Requester, filePath); storeErr != nil {
				return rpcError(req.ID, -32000, "store attachment failed: "+storeErr.Error())
			}
		}
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  task,
	}
}

// handleUpdateTask parses task update params, applies the update, and
// optionally stores file attachments.
func (h *Handler) handleUpdateTask(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID     string                 `json:"agent_id"`
		TaskID      string                 `json:"task_id"`
		Status      protocol.TaskStatus    `json:"status"`
		Description string                 `json:"description"`
		Summary     string                 `json:"summary"`
		Reason      string                 `json:"reason"`
		Files       []string               `json:"files"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.AgentID == "" {
		return rpcError(req.ID, -32602, "agent_id is required")
	}
	if params.TaskID == "" {
		return rpcError(req.ID, -32602, "task_id is required")
	}

	update := core.TaskUpdate{
		Status:      params.Status,
		Description: params.Description,
		Summary:     params.Summary,
		Reason:      params.Reason,
	}

	task, err := h.hub.Tasks().UpdateTask(ctx, params.AgentID, params.TaskID, update)
	if err != nil {
		return rpcError(req.ID, -32000, "update task failed: "+err.Error())
	}

	if len(params.Files) > 0 && h.hub.Attachments() != nil {
		for _, filePath := range params.Files {
			if _, storeErr := h.hub.Attachments().Store(ctx, task.TaskID, params.AgentID, filePath); storeErr != nil {
				return rpcError(req.ID, -32000, "store attachment failed: "+storeErr.Error())
			}
		}
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  task,
	}
}

// handleGetTask retrieves a task by ID, including its attachment list.
func (h *Handler) handleGetTask(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.TaskID == "" {
		return rpcError(req.ID, -32602, "task_id is required")
	}

	task, err := h.hub.Tasks().GetTask(ctx, params.TaskID)
	if err != nil {
		return rpcError(req.ID, -32000, "get task failed: "+err.Error())
	}
	if task == nil {
		return rpcError(req.ID, -32001, "task not found: "+params.TaskID)
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  task,
	}
}

// handleListTasks lists tasks with optional status, requester, assignee, and
// limit filters.
func (h *Handler) handleListTasks(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		Status    protocol.TaskStatus `json:"status"`
		Requester string              `json:"requester"`
		Assignee  string              `json:"assignee"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}

	filter := store.TaskFilter{
		Status:    params.Status,
		Requester: params.Requester,
		Assignee:  params.Assignee,
	}

	tasks, err := h.hub.Tasks().ListTasks(ctx, filter)
	if err != nil {
		return rpcError(req.ID, -32000, "list tasks failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  tasks,
	}
}

// handleAttachFile stores a single file attachment for a task.
func (h *Handler) handleAttachFile(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID  string `json:"agent_id"`
		TaskID   string `json:"task_id"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.AgentID == "" {
		return rpcError(req.ID, -32602, "agent_id is required")
	}
	if params.TaskID == "" {
		return rpcError(req.ID, -32602, "task_id is required")
	}
	if params.FilePath == "" {
		return rpcError(req.ID, -32602, "file_path is required")
	}

	if h.hub.Attachments() == nil {
		return rpcError(req.ID, -32000, "attachment storage not configured")
	}

	att, err := h.hub.Attachments().Store(ctx, params.TaskID, params.AgentID, params.FilePath)
	if err != nil {
		return rpcError(req.ID, -32000, "attach file failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  att,
	}
}

// handleSubscribe subscribes an agent to a channel or task target.
func (h *Handler) handleSubscribe(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
		Target  string `json:"target"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.AgentID == "" {
		return rpcError(req.ID, -32602, "agent_id is required")
	}
	if params.Target == "" {
		return rpcError(req.ID, -32602, "target is required")
	}

	if err := h.hub.Channels().Subscribe(ctx, params.AgentID, params.Target); err != nil {
		return rpcError(req.ID, -32000, "subscribe failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]bool{"ok": true},
	}
}

// handleUnsubscribe removes an agent's subscription to a target.
func (h *Handler) handleUnsubscribe(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
		Target  string `json:"target"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.AgentID == "" {
		return rpcError(req.ID, -32602, "agent_id is required")
	}
	if params.Target == "" {
		return rpcError(req.ID, -32602, "target is required")
	}

	if err := h.hub.Channels().Unsubscribe(ctx, params.AgentID, params.Target); err != nil {
		return rpcError(req.ID, -32000, "unsubscribe failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]bool{"ok": true},
	}
}

// handleListChannels returns all known channels with their subscriber counts.
func (h *Handler) handleListChannels(ctx context.Context, req *RPCRequest) *RPCResponse {
	channels, err := h.hub.Channels().ListChannels(ctx)
	if err != nil {
		return rpcError(req.ID, -32000, "list channels failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  channels,
	}
}

// handleDNDSet enables or disables DND mode for an agent. When disabling, any
// queued messages are flushed as notifications.
func (h *Handler) handleDNDSet(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.AgentID == "" {
		return rpcError(req.ID, -32602, "agent_id is required")
	}

	if params.Enabled {
		if err := h.hub.DND().Enable(ctx, params.AgentID, params.Reason); err != nil {
			return rpcError(req.ID, -32000, "dnd enable failed: "+err.Error())
		}
		return &RPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]bool{"ok": true},
		}
	}

	flushed, err := h.hub.DND().Disable(ctx, params.AgentID)
	if err != nil {
		return rpcError(req.ID, -32000, "dnd disable failed: "+err.Error())
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"ok": true, "flushed": flushed},
	}
}

// handleDNDStatus returns the current DND state for an agent.
func (h *Handler) handleDNDStatus(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}
	if params.AgentID == "" {
		return rpcError(req.ID, -32602, "agent_id is required")
	}

	agent, err := h.hub.Store().GetAgent(ctx, params.AgentID)
	if err != nil {
		return rpcError(req.ID, -32000, "get agent failed: "+err.Error())
	}
	if agent == nil {
		return rpcError(req.ID, -32001, "agent not found: "+params.AgentID)
	}

	queued, err := h.hub.DND().QueuedCount(ctx, params.AgentID)
	if err != nil {
		return rpcError(req.ID, -32000, "queued count failed: "+err.Error())
	}

	enabled := agent.Status == protocol.AgentStatusDND
	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"enabled": enabled,
			"reason":  agent.DNDReason,
			"queued":  queued,
		},
	}
}

// rpcError constructs an RPCResponse carrying an error.
func rpcError(id any, code int, msg string) *RPCResponse {
	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: msg,
		},
	}
}
