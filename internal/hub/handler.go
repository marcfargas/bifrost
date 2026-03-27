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
