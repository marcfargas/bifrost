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

// TestPingOnConnect verifies that when an agent registers, the hub sends
// a "ping" notification that arrives on the client socket — proving the
// full notification loop (hub → ConnManager → socket → client) works.
func TestPingOnConnect(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := core.NewHub(s)
	cm := hub.NewConnManager()
	h.AddNotifier(cm)
	handler := hub.NewHandler(h, cm)

	ctx := context.Background()

	// Create socket pair.
	serverConn, clientConn := net.Pipe()
	serverT := transport.NewConn(serverConn)
	clientT := transport.NewConn(clientConn)
	t.Cleanup(func() { serverConn.Close(); clientConn.Close() })

	// Start reading on client side BEFORE registration (pipe is unbuffered).
	type notification struct {
		raw json.RawMessage
		err error
	}
	notifCh := make(chan notification, 10)
	go func() {
		for {
			var raw json.RawMessage
			err := clientT.Receive(&raw)
			notifCh <- notification{raw, err}
			if err != nil {
				return
			}
		}
	}()

	time.Sleep(10 * time.Millisecond)

	// Register via the handler (same as real hub does).
	agentJSON, _ := json.Marshal(protocol.Agent{
		AgentID:         "test-ping",
		Username:        "marc",
		Hostname:        "test",
		LocalPath:       "/test",
		ProjectName:     "test-project",
		DisplayName:     "marc@test",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     time.Now(),
		LastSeen:        time.Now(),
	})
	req := &hub.RPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "hub.register",
		Params:  agentJSON,
	}
	resp := handler.Handle(ctx, serverT, req)
	if resp.Error != nil {
		t.Fatalf("register failed: %s", resp.Error.Message)
	}

	// Wait for the ping notification.
	select {
	case n := <-notifCh:
		if n.err != nil {
			t.Fatalf("receive error: %v", n.err)
		}
		t.Logf("Received: %s", string(n.raw))

		// Parse and verify it's a ping.
		var rpcNotif hub.RPCNotification
		json.Unmarshal(n.raw, &rpcNotif)
		if rpcNotif.Method != "bifrost.notification" {
			t.Fatalf("expected bifrost.notification, got %s", rpcNotif.Method)
		}

		paramsJSON, _ := json.Marshal(rpcNotif.Params)
		var envelope struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		json.Unmarshal(paramsJSON, &envelope)
		if envelope.Type != "ping" {
			t.Fatalf("expected ping type, got %s", envelope.Type)
		}

		var info map[string]string
		json.Unmarshal(envelope.Payload, &info)
		if info["message"] == "" {
			t.Error("ping message is empty")
		}
		t.Logf("Ping message: %s", info["message"])

	case <-time.After(3 * time.Second):
		t.Fatal("timeout: no ping notification received")
	}
}

// TestNotificationPipeline tests the full path:
// hub.Messages().Send → SyncEngine → core.NotifyAgent → ConnManager.Notify → socket → JSON-RPC notification
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Sync().Start(ctx)

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
		// There may be agent_joined notifications first — drain until we get event.new
		// (SyncEngine now sends event.new instead of message.new).
		for {
			var r json.RawMessage
			if err := clientTransport.Receive(&r); err != nil {
				done <- err
				return
			}
			var peek struct {
				Params struct {
					Type string `json:"type"`
				} `json:"params"`
			}
			json.Unmarshal(r, &peek)
			if peek.Params.Type == "event.new" {
				raw = r
				done <- nil
				return
			}
			// Otherwise it's an agent_joined or other notification — skip
		}
	}()

	// Give the reader goroutine a moment to start.
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
		t.Fatal("timeout: no event.new notification received on client side within 3s")
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

	// 9. Parse the payload as an Event (SyncEngine sends *protocol.Event).
	var received protocol.Event
	if err := json.Unmarshal(envelope.Payload, &received); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}

	if received.Data.Body != "Can you hear me?" {
		t.Errorf("expected 'Can you hear me?', got %q", received.Data.Body)
	}
	if received.FromAgent != "agent-a" {
		t.Errorf("expected from agent-a, got %s", received.FromAgent)
	}

	t.Log("Full notification pipeline works!")
}
