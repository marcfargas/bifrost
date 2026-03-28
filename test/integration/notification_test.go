package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// TestNotificationPipeline tests the full path:
// hub.Messages().Send → core.NotifyAgent → ConnManager.Notify → socket → JSON-RPC notification
func TestNotificationPipeline(t *testing.T) {
	// 1. Create hub with store.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := core.NewHub(s)
	cm := hub.NewConnManager()
	h.AddNotifier(cm)

	ctx := context.Background()

	// 2. Create a socket pair to simulate shim↔hub connection.
	serverConn, clientConn := net.Pipe()
	serverTransport := transport.NewConn(serverConn)
	clientTransport := transport.NewConn(clientConn)
	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
	})

	// 3. Register agent A (the sender) — no connection needed, just in the store.
	agentA := &protocol.Agent{
		AgentID:         "agent-a",
		Aliases:         []string{"sender"},
		Username:        "marc",
		Hostname:        "test",
		LocalPath:       "/a",
		ProjectName:     "sender",
		Status:          protocol.AgentStatusOnline,
		ConnectedAt:     time.Now(),
		LastSeen:        time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}
	h.Agents().Register(ctx, agentA)

	// 4. Register agent B (the receiver) with a real connection.
	agentB := &protocol.Agent{
		AgentID:         "agent-b",
		Aliases:         []string{"receiver"},
		Username:        "marc",
		Hostname:        "test",
		LocalPath:       "/b",
		ProjectName:     "receiver",
		Status:          protocol.AgentStatusOnline,
		ConnectedAt:     time.Now(),
		LastSeen:        time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}
	// Add connection BEFORE registering — Register triggers notifications
	// and net.Pipe is unbuffered.
	cm.Add("agent-b", serverTransport)
	// Register in a goroutine because it will try to notify existing agents
	// via the pipe, which blocks until the reader drains.
	go h.Agents().Register(ctx, agentB)

	// 5. Start reader BEFORE sending (net.Pipe is unbuffered — write blocks until read).
	var raw json.RawMessage
	done := make(chan error, 1)
	go func() {
		// There may be agent_joined notifications first — drain until we get message.new
		for {
			var r json.RawMessage
			if err := clientTransport.Receive(&r); err != nil {
				done <- err
				return
			}
			// Check if this is a message.new notification
			var peek struct {
				Params struct {
					Type string `json:"type"`
				} `json:"params"`
			}
			json.Unmarshal(r, &peek)
			if peek.Params.Type == "message.new" {
				raw = r
				done <- nil
				return
			}
			// Otherwise it's an agent_joined or other notification — skip
		}
	}()

	// Give the reader goroutine a moment to start
	time.Sleep(10 * time.Millisecond)

	// 6. Send a message from A to B.
	msg := &protocol.Message{
		From:     "agent-a",
		To:       "receiver",
		Type:     protocol.MessageTypeQuestion,
		Body:     "Can you hear me?",
		Priority: protocol.PriorityNormal,
	}
	if err := h.Messages().Send(ctx, msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("receive on client side failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: no message.new notification received on client side within 3s")
	}

	t.Logf("Raw notification received: %s", string(raw))

	// 7. Parse as RPCNotification.
	var notif hub.RPCNotification
	if err := json.Unmarshal(raw, &notif); err != nil {
		t.Fatalf("unmarshal notification: %v", err)
	}

	if notif.Method != "bifrost.notification" {
		t.Fatalf("expected method bifrost.notification, got %s", notif.Method)
	}

	// 8. Parse the params as {type, payload}.
	paramsJSON, err := json.Marshal(notif.Params)
	if err != nil {
		t.Fatalf("re-marshal params: %v", err)
	}
	t.Logf("Params JSON: %s", string(paramsJSON))

	var envelope struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(paramsJSON, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	t.Logf("Envelope type: %s", envelope.Type)

	if envelope.Type == "" {
		t.Fatal("envelope type is empty — the notification structure doesn't match {type, payload}")
	}

	// 9. Parse the payload as a Message.
	var received protocol.Message
	if err := json.Unmarshal(envelope.Payload, &received); err != nil {
		t.Fatalf("unmarshal message payload: %v", err)
	}

	if received.Body != "Can you hear me?" {
		t.Errorf("expected 'Can you hear me?', got %q", received.Body)
	}
	if received.From != "agent-a" {
		t.Errorf("expected from agent-a, got %s", received.From)
	}

	t.Log("Full notification pipeline works!")
}
