package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/pkg/protocol"
)

// syncBuffer is a thread-safe bytes.Buffer for use in tests with the race detector.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *syncBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) Bytes() []byte {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return append([]byte(nil), sb.buf.Bytes()...)
}

func (sb *syncBuffer) Len() int {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Len()
}

// testNotificationWriter returns a notificationWriter backed by a syncBuffer.
func testNotificationWriter() (*notificationWriter, *syncBuffer) {
	buf := &syncBuffer{}
	return &notificationWriter{w: buf}, buf
}

// fakeHubMux creates an hubMux with a writable notifications channel (no real conn).
func fakeHubMux() *hubMux {
	return &hubMux{
		notifications: make(chan hub.RPCNotification, 64),
	}
}

// parseChannelNotification decodes the JSON written to the buffer and returns
// the outer JSON-RPC envelope and the inner channelNotificationParams.
func parseChannelNotification(t *testing.T, data []byte) (map[string]any, channelNotificationParams) {
	t.Helper()

	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("unmarshal outer envelope: %v (raw: %s)", err, string(data))
	}

	if envelope["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", envelope["jsonrpc"])
	}
	if envelope["method"] != "notifications/claude/channel" {
		t.Errorf("expected method notifications/claude/channel, got %v", envelope["method"])
	}

	paramsRaw, err := json.Marshal(envelope["params"])
	if err != nil {
		t.Fatalf("re-marshal params: %v", err)
	}

	var cp channelNotificationParams
	if err := json.Unmarshal(paramsRaw, &cp); err != nil {
		t.Fatalf("unmarshal channelNotificationParams: %v", err)
	}

	return envelope, cp
}

// runListenAndCapture starts listenHubNotifications in a goroutine, pushes
// the given notification, waits for the buffer to have output, and returns it.
func runListenAndCapture(t *testing.T, notif hub.RPCNotification) []byte {
	t.Helper()

	mux := fakeHubMux()
	nw, buf := testNotificationWriter()
	logger := slog.Default()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		listenHubNotifications(ctx, mux, nw, logger)
		close(done)
	}()

	mux.notifications <- notif

	// Poll for output with timeout.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			<-done
			if buf.Len() == 0 {
				t.Fatal("timeout: no output written by listenHubNotifications")
			}
			return buf.Bytes()
		default:
			if buf.Len() > 0 {
				cancel()
				<-done
				return buf.Bytes()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestNotificationWriter_WriteNotification(t *testing.T) {
	nw, buf := testNotificationWriter()

	params := channelNotificationParams{

		Content: "hello",
		Meta:    map[string]string{"event": "test"},
	}

	if err := nw.writeNotification("notifications/claude/channel", params); err != nil {
		t.Fatalf("writeNotification: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", got["jsonrpc"])
	}
	if got["method"] != "notifications/claude/channel" {
		t.Errorf("expected method notifications/claude/channel, got %v", got["method"])
	}
	if got["id"] != nil {
		t.Errorf("notification must not have id field, got %v", got["id"])
	}
}

func TestListenHubNotifications_MessageNew(t *testing.T) {
	ts := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	payload := protocol.Message{
		ID:             "msg-1",
		ConversationID: "conv-1",
		From:           "agent-a",
		To:             "agent-b",
		Type:           protocol.MessageTypeQuestion,
		Body:           "Hello from A",
		Priority:       protocol.PriorityNormal,
		Timestamp:      ts,
		InReplyTo:      "msg-0",
		NoReply:        true,
	}

	notif := hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params: map[string]any{
			"type":    "message.new",
			"payload": payload,
		},
	}

	data := runListenAndCapture(t, notif)
	_, cp := parseChannelNotification(t, data)

	if cp.Content != "Hello from A" {
		t.Errorf("message: got %q, want %q", cp.Content, "Hello from A")
	}
	if cp.Meta["from"] != "agent-a" {
		t.Errorf("meta.from: got %q, want %q", cp.Meta["from"], "agent-a")
	}
	if cp.Meta["type"] != "QUESTION" {
		t.Errorf("meta.type: got %q, want %q", cp.Meta["type"], "QUESTION")
	}
	if cp.Meta["conversation"] != "conv-1" {
		t.Errorf("meta.conversation: got %q, want %q", cp.Meta["conversation"], "conv-1")
	}
	if cp.Meta["in_reply_to"] != "msg-0" {
		t.Errorf("meta.in_reply_to: got %q, want %q", cp.Meta["in_reply_to"], "msg-0")
	}
	if cp.Meta["noreply"] != "true" {
		t.Errorf("meta.noreply: got %q, want %q", cp.Meta["noreply"], "true")
	}
	if cp.Meta["ts"] != "2026-03-28T12:00:00Z" {
		t.Errorf("meta.ts: got %q, want %q", cp.Meta["ts"], "2026-03-28T12:00:00Z")
	}
}

func TestListenHubNotifications_Ping(t *testing.T) {
	notif := hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params: map[string]any{
			"type": "ping",
			"payload": map[string]string{
				"message": "PONG! Welcome to bifrost",
				"hub":     "local",
			},
		},
	}

	data := runListenAndCapture(t, notif)
	_, cp := parseChannelNotification(t, data)

	if cp.Content != "PONG! Welcome to bifrost" {
		t.Errorf("message: got %q, want %q", cp.Content, "PONG! Welcome to bifrost")
	}
	if cp.Meta["event"] != "ping" {
		t.Errorf("meta.event: got %q, want %q", cp.Meta["event"], "ping")
	}
	if cp.Meta["hub"] != "local" {
		t.Errorf("meta.hub: got %q, want %q", cp.Meta["hub"], "local")
	}
}

func TestListenHubNotifications_AgentRegistered(t *testing.T) {
	notif := hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params: map[string]any{
			"type": "agent.registered",
			"payload": protocol.Agent{
				AgentID:     "agent-x",
				ProjectName: "my-project",
				DisplayName: "Marc@my-project",
			},
		},
	}

	data := runListenAndCapture(t, notif)
	_, cp := parseChannelNotification(t, data)

	if cp.Content != "Agent Marc@my-project has joined" {
		t.Errorf("message: got %q, want %q", cp.Content, "Agent Marc@my-project has joined")
	}
	if cp.Meta["agent"] != "Marc@my-project" {
		t.Errorf("meta.agent: got %q, want %q", cp.Meta["agent"], "Marc@my-project")
	}
	if cp.Meta["event"] != "agent_joined" {
		t.Errorf("meta.event: got %q, want %q", cp.Meta["event"], "agent_joined")
	}
}

func TestListenHubNotifications_AgentRegistered_FallbackProjectName(t *testing.T) {
	notif := hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params: map[string]any{
			"type": "agent.registered",
			"payload": protocol.Agent{
				AgentID:     "agent-y",
				ProjectName: "fallback-project",
				DisplayName: "", // empty — should use ProjectName
			},
		},
	}

	data := runListenAndCapture(t, notif)
	_, cp := parseChannelNotification(t, data)

	if cp.Content != "Agent fallback-project has joined" {
		t.Errorf("message: got %q, want %q", cp.Content, "Agent fallback-project has joined")
	}
}

func TestListenHubNotifications_AgentDeregistered(t *testing.T) {
	notif := hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params: map[string]any{
			"type": "agent.deregistered",
			"payload": map[string]string{
				"agent_id": "agent-gone",
			},
		},
	}

	data := runListenAndCapture(t, notif)
	_, cp := parseChannelNotification(t, data)

	if cp.Content != "Agent agent-gone has left" {
		t.Errorf("message: got %q, want %q", cp.Content, "Agent agent-gone has left")
	}
	if cp.Meta["agent"] != "agent-gone" {
		t.Errorf("meta.agent: got %q, want %q", cp.Meta["agent"], "agent-gone")
	}
	if cp.Meta["event"] != "agent_left" {
		t.Errorf("meta.event: got %q, want %q", cp.Meta["event"], "agent_left")
	}
}

func TestListenHubNotifications_TaskRequested(t *testing.T) {
	notif := hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params: map[string]any{
			"type": "task_requested",
			"payload": protocol.Task{
				TaskID:      "task-1",
				Title:       "Fix the bug",
				Description: "There is a bug in X",
				Requester:   "agent-a",
				Assignee:    "agent-b",
			},
		},
	}

	data := runListenAndCapture(t, notif)
	_, cp := parseChannelNotification(t, data)

	expected := fmt.Sprintf("Task requested: %s\n%s", "Fix the bug", "There is a bug in X")
	if cp.Content != expected {
		t.Errorf("message: got %q, want %q", cp.Content, expected)
	}
	if cp.Meta["event"] != "task_requested" {
		t.Errorf("meta.event: got %q, want %q", cp.Meta["event"], "task_requested")
	}
	if cp.Meta["task_id"] != "task-1" {
		t.Errorf("meta.task_id: got %q, want %q", cp.Meta["task_id"], "task-1")
	}
	if cp.Meta["title"] != "Fix the bug" {
		t.Errorf("meta.title: got %q, want %q", cp.Meta["title"], "Fix the bug")
	}
	if cp.Meta["requester"] != "agent-a" {
		t.Errorf("meta.requester: got %q, want %q", cp.Meta["requester"], "agent-a")
	}
	if cp.Meta["assignee"] != "agent-b" {
		t.Errorf("meta.assignee: got %q, want %q", cp.Meta["assignee"], "agent-b")
	}
}

func TestListenHubNotifications_TaskUpdated(t *testing.T) {
	notif := hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params: map[string]any{
			"type": "task_updated",
			"payload": protocol.Task{
				TaskID:   "task-2",
				Title:    "Deploy",
				Status:   protocol.TaskStatusCompleted,
				Assignee: "agent-c",
				Summary:  "Deployed successfully",
				Reason:   "all green",
			},
		},
	}

	data := runListenAndCapture(t, notif)
	_, cp := parseChannelNotification(t, data)

	expected := "Task task-2 updated: status=completed"
	if cp.Content != expected {
		t.Errorf("message: got %q, want %q", cp.Content, expected)
	}
	if cp.Meta["event"] != "task_updated" {
		t.Errorf("meta.event: got %q", cp.Meta["event"])
	}
	if cp.Meta["task_id"] != "task-2" {
		t.Errorf("meta.task_id: got %q", cp.Meta["task_id"])
	}
	if cp.Meta["status"] != "completed" {
		t.Errorf("meta.status: got %q", cp.Meta["status"])
	}
	if cp.Meta["title"] != "Deploy" {
		t.Errorf("meta.title: got %q", cp.Meta["title"])
	}
	if cp.Meta["assignee"] != "agent-c" {
		t.Errorf("meta.assignee: got %q", cp.Meta["assignee"])
	}
	if cp.Meta["summary"] != "Deployed successfully" {
		t.Errorf("meta.summary: got %q", cp.Meta["summary"])
	}
	if cp.Meta["reason"] != "all green" {
		t.Errorf("meta.reason: got %q", cp.Meta["reason"])
	}
}

func TestListenHubNotifications_UnknownType(t *testing.T) {
	notif := hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params: map[string]any{
			"type":    "some.future.event",
			"payload": map[string]string{"foo": "bar"},
		},
	}

	data := runListenAndCapture(t, notif)
	_, cp := parseChannelNotification(t, data)

	if cp.Meta["event"] != "some.future.event" {
		t.Errorf("meta.event: got %q, want %q", cp.Meta["event"], "some.future.event")
	}
	// Message should be the raw JSON of the full params (not just payload).
	if cp.Content == "" {
		t.Error("expected non-empty message for unknown type")
	}
}

func TestListenHubNotifications_IgnoresNonBifrostNotification(t *testing.T) {
	mux := fakeHubMux()
	nw, buf := testNotificationWriter()
	logger := slog.Default()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		listenHubNotifications(ctx, mux, nw, logger)
		close(done)
	}()

	// Send a non-bifrost.notification method — should be ignored.
	mux.notifications <- hub.RPCNotification{
		JSONRPC: "2.0",
		Method:  "some.other.method",
		Params:  map[string]string{"x": "y"},
	}

	// Give it time to process, then send a real one to prove the loop continued.
	time.Sleep(100 * time.Millisecond)

	// Should have zero output so far.
	if buf.Len() > 0 {
		t.Errorf("expected no output for non-bifrost.notification, got %d bytes", buf.Len())
	}

	cancel()
	<-done
}
