package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/protocol"
)

// errReconnecting is returned by rpcCall when the hub connection is being
// re-established after a drop.
var errReconnecting = errors.New("reconnecting to hub")

// hubMux multiplexes reads on a hub connection, separating RPC responses
// (messages with an "id" field) from unsolicited notifications (no "id").
type hubMux struct {
	log *slog.Logger

	// connMu protects conn. rpcCall holds a read lock while sending;
	// reconnect holds the write lock while swapping the connection.
	connMu sync.RWMutex
	conn   *transport.Conn

	// pending tracks outstanding RPC calls: id -> response channel.
	mu      sync.Mutex
	pending map[uint64]chan<- hub.RPCResponse
	nextID  atomic.Uint64

	// notifications is the channel for hub push notifications.
	notifications chan hub.RPCNotification

	// disconnected is closed by readLoop when the connection drops.
	// The reconnection goroutine in shim.go watches this channel.
	// A new channel is created before each reconnect attempt.
	disconnected chan struct{}
	discMu       sync.Mutex // protects replacement of disconnected
}

// newHubMux creates a multiplexer and starts a background read loop.
// The read loop exits when ctx is cancelled or the connection is closed.
func newHubMux(ctx context.Context, conn *transport.Conn, log *slog.Logger) *hubMux {
	m := &hubMux{
		conn:          conn,
		log:           log,
		pending:       make(map[uint64]chan<- hub.RPCResponse),
		notifications: make(chan hub.RPCNotification, 64),
		disconnected:  make(chan struct{}),
	}
	go m.readLoop(ctx)
	return m
}

// disconnectedCh returns the current disconnected channel (safe to call
// concurrently — reads under discMu).
func (m *hubMux) disconnectedCh() <-chan struct{} {
	m.discMu.Lock()
	defer m.discMu.Unlock()
	return m.disconnected
}

// signalDisconnect closes the current disconnected channel once.
// It then installs a fresh (open) channel so the reconnection goroutine
// can arm itself again for the next drop.
func (m *hubMux) signalDisconnect() {
	m.discMu.Lock()
	ch := m.disconnected
	m.disconnected = make(chan struct{})
	m.discMu.Unlock()
	close(ch)
}

// swapConn replaces the underlying connection under the write lock and
// returns the old connection so the caller can close it.
func (m *hubMux) swapConn(newConn *transport.Conn) *transport.Conn {
	m.connMu.Lock()
	defer m.connMu.Unlock()
	old := m.conn
	m.conn = newConn
	return old
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

	// Capture the disconnect channel BEFORE sending so that if the connection
	// drops between the send and the select, we still observe the close.
	discCh := m.disconnectedCh()

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

	// Acquire read lock only for the send so reconnect (write lock) can proceed.
	m.connMu.RLock()
	if m.conn == nil {
		m.connMu.RUnlock()
		return nil, errReconnecting
	}
	sendErr := m.conn.Send(req)
	m.connMu.RUnlock()

	if sendErr != nil {
		return nil, fmt.Errorf("send rpc: %w", sendErr)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-discCh:
		return nil, errReconnecting
	case resp := <-ch:
		return &resp, nil
	}
}

// readLoop continuously reads from the hub connection and dispatches messages.
// When the connection drops it signals disconnected and returns.
func (m *hubMux) readLoop(ctx context.Context) {
	for {
		var raw json.RawMessage

		m.connMu.RLock()
		conn := m.conn
		m.connMu.RUnlock()

		if conn == nil {
			return
		}

		if err := conn.Receive(&raw); err != nil {
			if ctx.Err() != nil {
				// Context cancelled — clean shutdown, no reconnect needed.
				return
			}
			if err != io.EOF {
				m.log.Error("hub read error", "error", err)
			}
			// Connection dropped — signal disconnect. Any rpcCall goroutines
			// blocked in their select will wake up on the disconnect channel
			// and return errReconnecting. drainPending is no longer needed
			// because all rpcCall goroutines watch disconnectedCh.
			m.signalDisconnect()
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
// stdio output. The underlying io.Writer must already be safe for concurrent
// use (e.g. a lockedWriter shared with the MCP transport).
type notificationWriter struct {
	w io.Writer
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

	_, err = nw.w.Write(data)
	return err
}

// channelNotificationParams represents the params of a
// notifications/claude/channel notification.
type channelNotificationParams struct {
	Content string            `json:"content"`
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
			log.Info("received hub notification", "method", notif.Method)
			if notif.Method != "bifrost.notification" {
				log.Info("ignoring hub notification (not bifrost.notification)", "method", notif.Method)
				continue
			}

			// The hub sends: {"type": "...", "payload": <object>}
			// where type is "message.new", "agent.registered", "agent.deregistered",
			// "task_requested", "task_updated", etc.
			data, err := json.Marshal(notif.Params)
			if err != nil {
				log.Warn("marshal hub notification params failed", "error", err)
				continue
			}

			var envelope struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				log.Warn("unmarshal hub notification envelope failed", "error", err)
				continue
			}

			cp := channelNotificationParams{
				Meta: make(map[string]string),
			}

			switch envelope.Type {
			case "event.new":
				var ev protocol.Event
				if err := json.Unmarshal(envelope.Payload, &ev); err != nil {
					log.Warn("unmarshal event payload failed", "error", err)
					continue
				}
				switch ev.Type {
				case protocol.EventTypeMessage:
					cp.Content = ev.Data.Body
					cp.Meta["from"] = ev.FromAgent
					cp.Meta["type"] = string(ev.Data.MessageType)
					cp.Meta["conversation"] = ev.ConversationID
					cp.Meta["ts"] = ev.Timestamp.Format("2006-01-02T15:04:05Z07:00")
					if ev.Data.InReplyTo != "" {
						cp.Meta["in_reply_to"] = ev.Data.InReplyTo
					}
				case protocol.EventTypeStatus:
					cp.Content = fmt.Sprintf("Task status: %s", ev.Data.NewStatus)
					cp.Meta["event"] = "task_updated"
					cp.Meta["conversation"] = ev.ConversationID
					cp.Meta["status"] = string(ev.Data.NewStatus)
					if ev.Data.Summary != "" {
						cp.Meta["summary"] = ev.Data.Summary
					}
					if ev.Data.Reason != "" {
						cp.Meta["reason"] = ev.Data.Reason
					}
				case protocol.EventTypeFile:
					cp.Content = fmt.Sprintf("File: %s (%d bytes)", ev.Data.Filename, ev.Data.Size)
					cp.Meta["event"] = "file"
					cp.Meta["conversation"] = ev.ConversationID
					cp.Meta["filename"] = ev.Data.Filename
				default:
					cp.Content = fmt.Sprintf("Event: %s in %s", ev.Type, ev.ConversationID)
					cp.Meta["event"] = string(ev.Type)
					cp.Meta["conversation"] = ev.ConversationID
				}
			case "agent.registered":
				var agent protocol.Agent
				if err := json.Unmarshal(envelope.Payload, &agent); err != nil {
					log.Warn("unmarshal agent.registered payload failed", "error", err)
					continue
				}
				name := agent.ProjectName
				if agent.DisplayName != "" {
					name = agent.DisplayName
				}
				cp.Content = fmt.Sprintf("Agent %s has joined", name)
				cp.Meta["agent"] = name
				cp.Meta["event"] = "agent_joined"
			case "agent.deregistered":
				var info map[string]string
				if err := json.Unmarshal(envelope.Payload, &info); err != nil {
					log.Warn("unmarshal agent.deregistered payload failed", "error", err)
					continue
				}
				cp.Content = fmt.Sprintf("Agent %s has left", info["agent_id"])
				cp.Meta["agent"] = info["agent_id"]
				cp.Meta["event"] = "agent_left"
			case "task_requested":
				var task protocol.TaskView
				if err := json.Unmarshal(envelope.Payload, &task); err != nil {
					log.Warn("unmarshal task_requested payload failed", "error", err)
					continue
				}
				cp.Content = fmt.Sprintf("Task requested: %s", task.Title)
				cp.Meta["event"] = "task_requested"
				cp.Meta["task_id"] = task.ConversationID
				cp.Meta["title"] = task.Title
				cp.Meta["requester"] = task.Requester
				cp.Meta["assignee"] = task.Assignee
			case "ping":
				var info map[string]string
				if err := json.Unmarshal(envelope.Payload, &info); err != nil {
					log.Warn("unmarshal ping payload failed", "error", err)
					continue
				}
				cp.Content = info["message"]
				cp.Meta["event"] = "ping"
				if hub, ok := info["hub"]; ok {
					cp.Meta["hub"] = hub
				}
			default:
				cp.Content = string(data)
				cp.Meta["event"] = envelope.Type
			}

			log.Info("emitting channel notification", "type", envelope.Type, "message_preview", cp.Content[:min(len(cp.Content), 50)])
			if err := nw.writeNotification("notifications/claude/channel", cp); err != nil {
				log.Warn("failed to write channel notification", "error", err)
			} else {
				log.Info("channel notification written successfully")
			}
		}
	}
}
