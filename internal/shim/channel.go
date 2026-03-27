package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/internal/transport"
)

// hubMux multiplexes reads on a hub connection, separating RPC responses
// (messages with an "id" field) from unsolicited notifications (no "id").
type hubMux struct {
	conn *transport.Conn
	log  *slog.Logger

	// pending tracks outstanding RPC calls: id -> response channel.
	mu      sync.Mutex
	pending map[uint64]chan<- hub.RPCResponse
	nextID  atomic.Uint64

	// notifications is the channel for hub push notifications.
	notifications chan hub.RPCNotification
}

// newHubMux creates a multiplexer and starts a background read loop.
// The read loop exits when ctx is cancelled or the connection is closed.
func newHubMux(ctx context.Context, conn *transport.Conn, log *slog.Logger) *hubMux {
	m := &hubMux{
		conn:          conn,
		log:           log,
		pending:       make(map[uint64]chan<- hub.RPCResponse),
		notifications: make(chan hub.RPCNotification, 64),
	}
	go m.readLoop(ctx)
	return m
}

// rpcCall sends a JSON-RPC request to the hub and waits for the response.
func (m *hubMux) rpcCall(ctx context.Context, method string, params any) (*hub.RPCResponse, error) {
	id := m.nextID.Add(1)

	ch := make(chan hub.RPCResponse, 1)
	m.mu.Lock()
	m.pending[id] = ch
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
	}()

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	req := hub.RPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  paramsJSON,
	}

	if err := m.conn.Send(req); err != nil {
		return nil, fmt.Errorf("send rpc: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		return &resp, nil
	}
}

// readLoop continuously reads from the hub connection and dispatches messages.
func (m *hubMux) readLoop(ctx context.Context) {
	for {
		var raw json.RawMessage
		if err := m.conn.Receive(&raw); err != nil {
			if ctx.Err() != nil || err == io.EOF {
				return
			}
			m.log.Error("hub read error", "error", err)
			return
		}

		// Peek at the message to determine if it has an "id" field.
		var peek struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			m.log.Warn("hub message unmarshal peek failed", "error", err)
			continue
		}

		if peek.ID != nil {
			// This is an RPC response.
			var resp hub.RPCResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				m.log.Warn("hub response unmarshal failed", "error", err)
				continue
			}
			m.routeResponse(resp)
		} else {
			// This is a notification.
			var notif hub.RPCNotification
			if err := json.Unmarshal(raw, &notif); err != nil {
				m.log.Warn("hub notification unmarshal failed", "error", err)
				continue
			}
			select {
			case m.notifications <- notif:
			default:
				m.log.Warn("hub notification channel full, dropping", "method", notif.Method)
			}
		}
	}
}

// routeResponse routes an RPC response to its pending caller.
func (m *hubMux) routeResponse(resp hub.RPCResponse) {
	// The ID can be a float64 from JSON unmarshaling.
	var id uint64
	switch v := resp.ID.(type) {
	case float64:
		id = uint64(v)
	case json.Number:
		n, _ := v.Int64()
		id = uint64(n)
	default:
		m.log.Warn("hub response with unrecognized id type", "id", resp.ID)
		return
	}

	m.mu.Lock()
	ch, ok := m.pending[id]
	m.mu.Unlock()

	if ok {
		ch <- resp
	} else {
		m.log.Warn("hub response for unknown id", "id", id)
	}
}

// notificationWriter can write raw JSON-RPC notification messages to the MCP
// stdio output. It wraps an io.Writer with synchronization.
type notificationWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// writeNotification sends a raw JSON-RPC notification to the MCP client via
// stdout, bypassing the MCP SDK which doesn't support custom notification methods.
func (nw *notificationWriter) writeNotification(method string, params any) error {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	data = append(data, '\n')

	nw.mu.Lock()
	defer nw.mu.Unlock()
	_, err = nw.w.Write(data)
	return err
}

// channelNotificationParams represents the params of a
// notifications/claude/channel notification.
type channelNotificationParams struct {
	Channel string            `json:"channel"`
	Message string            `json:"message"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// listenHubNotifications reads hub notifications and emits MCP
// notifications/claude/channel events. It blocks until ctx is cancelled.
func listenHubNotifications(ctx context.Context, mux *hubMux, nw *notificationWriter, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case notif := <-mux.notifications:
			if notif.Method != "bifrost.notification" {
				log.Debug("ignoring hub notification", "method", notif.Method)
				continue
			}

			// Parse the notification params.
			data, err := json.Marshal(notif.Params)
			if err != nil {
				log.Warn("marshal hub notification params failed", "error", err)
				continue
			}

			var payload struct {
				Type         string `json:"type"`
				From         string `json:"from"`
				Body         string `json:"body"`
				Conversation string `json:"conversation_id"`
				Timestamp    string `json:"timestamp"`
				AgentName    string `json:"agent_name"`
				TaskID       string `json:"task_id"`
				Title        string `json:"title"`
				Description  string `json:"description"`
				MessageType  string `json:"message_type"`
				Status       string `json:"status"`
				Summary      string `json:"summary"`
				Reason       string `json:"reason"`
				Requester    string `json:"requester"`
				Assignee     string `json:"assignee"`
			}
			if err := json.Unmarshal(data, &payload); err != nil {
				log.Warn("unmarshal hub notification payload failed", "error", err)
				continue
			}

			cp := channelNotificationParams{
				Channel: "bifrost",
				Meta:    make(map[string]string),
			}

			switch payload.Type {
			case "message":
				cp.Message = payload.Body
				cp.Meta["from"] = payload.From
				if payload.MessageType != "" {
					cp.Meta["type"] = payload.MessageType
				}
				if payload.Conversation != "" {
					cp.Meta["conversation"] = payload.Conversation
				}
				if payload.Timestamp != "" {
					cp.Meta["ts"] = payload.Timestamp
				}
			case "agent_joined":
				cp.Message = fmt.Sprintf("Agent %s has joined", payload.AgentName)
				cp.Meta["agent"] = payload.AgentName
				cp.Meta["event"] = "agent_joined"
			case "agent_left":
				cp.Message = fmt.Sprintf("Agent %s has left", payload.AgentName)
				cp.Meta["agent"] = payload.AgentName
				cp.Meta["event"] = "agent_left"
			case "task_requested":
				cp.Message = fmt.Sprintf("Task requested: %s", payload.Title)
				cp.Meta["event"] = "task_requested"
				if payload.TaskID != "" {
					cp.Meta["task_id"] = payload.TaskID
				}
				if payload.Title != "" {
					cp.Meta["title"] = payload.Title
				}
				if payload.Description != "" {
					cp.Meta["description"] = payload.Description
				}
				if payload.Requester != "" {
					cp.Meta["requester"] = payload.Requester
				}
				if payload.Assignee != "" {
					cp.Meta["assignee"] = payload.Assignee
				}
			case "task_update":
				cp.Message = fmt.Sprintf("Task %s updated: status=%s", payload.TaskID, payload.Status)
				cp.Meta["event"] = "task_update"
				if payload.TaskID != "" {
					cp.Meta["task_id"] = payload.TaskID
				}
				if payload.Status != "" {
					cp.Meta["status"] = payload.Status
				}
				if payload.Title != "" {
					cp.Meta["title"] = payload.Title
				}
				if payload.Summary != "" {
					cp.Meta["summary"] = payload.Summary
				}
				if payload.Reason != "" {
					cp.Meta["reason"] = payload.Reason
				}
				if payload.Assignee != "" {
					cp.Meta["assignee"] = payload.Assignee
				}
			default:
				cp.Message = string(data)
				cp.Meta["event"] = payload.Type
			}

			if err := nw.writeNotification("notifications/claude/channel", cp); err != nil {
				log.Warn("failed to write channel notification", "error", err)
			}
		}
	}
}
