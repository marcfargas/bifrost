package federation

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestDirectTransportStartStop(t *testing.T) {
	dt := NewDirectTransport(DirectTransportConfig{
		HubID:  "hub-direct-1",
		Listen: "127.0.0.1:0",
		Logger: slog.Default(),
	})

	if dt.Name() != "direct" {
		t.Errorf("expected name direct, got %s", dt.Name())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	incoming := make(chan PeerConn, 4)
	if err := dt.Start(ctx, incoming); err != nil {
		t.Fatal(err)
	}

	if dt.Addr() == "" {
		t.Error("addr should be set after start")
	}

	if err := dt.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestDirectTransportConnectAndAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger := slog.Default()

	// Start server hub
	server := NewDirectTransport(DirectTransportConfig{
		HubID:  "hub-server",
		Listen: "127.0.0.1:0",
		Logger: logger,
	})
	incomingServer := make(chan PeerConn, 4)
	if err := server.Start(ctx, incomingServer); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	// Start client hub
	client := NewDirectTransport(DirectTransportConfig{
		HubID:  "hub-client",
		Listen: "127.0.0.1:0",
		Logger: logger,
	})
	incomingClient := make(chan PeerConn, 4)
	if err := client.Start(ctx, incomingClient); err != nil {
		t.Fatal(err)
	}
	defer client.Stop()

	// Client connects to server
	conn, err := client.Connect(ctx, server.Addr())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if conn.PeerID() != "hub-server" {
		t.Errorf("expected peer ID hub-server, got %s", conn.PeerID())
	}

	// Server should receive the incoming connection
	select {
	case incoming := <-incomingServer:
		if incoming.PeerID() != "hub-client" {
			t.Errorf("expected incoming from hub-client, got %s", incoming.PeerID())
		}

		// Test bidirectional messaging
		testMsg := &protocol.Message{
			ID:             "test-1",
			ConversationID: "c1",
			From:           "agent-s",
			To:             "agent-c",
			Type:           protocol.MessageTypeContext,
			Body:           "hello direct",
			Priority:       protocol.PriorityNormal,
			Timestamp:      time.Now(),
		}
		payload, _ := json.Marshal(protocol.PeerMessagePayload{Message: testMsg})
		env := &protocol.PeerEnvelope{
			Method:  "peer.message",
			ID:      "e1",
			Version: protocol.ProtocolVersion,
			From:    "hub-server",
			Payload: payload,
		}

		// Server sends to client
		if err := incoming.Send(ctx, env); err != nil {
			t.Fatalf("server send: %v", err)
		}

		// Client receives
		received, err := conn.Receive(ctx)
		if err != nil {
			t.Fatalf("client receive: %v", err)
		}
		if received.Method != "peer.message" {
			t.Errorf("expected peer.message, got %s", received.Method)
		}

		incoming.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for incoming connection on server")
	}
}

func TestDirectTransportTokenPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger := slog.Default()

	server := NewDirectTransport(DirectTransportConfig{
		HubID: "hub-s", Listen: "127.0.0.1:0", Logger: logger,
	})
	incomingS := make(chan PeerConn, 4)
	if err := server.Start(ctx, incomingS); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	client := NewDirectTransport(DirectTransportConfig{
		HubID: "hub-c", Listen: "127.0.0.1:0", Logger: logger,
	})
	incomingC := make(chan PeerConn, 4)
	if err := client.Start(ctx, incomingC); err != nil {
		t.Fatal(err)
	}
	defer client.Stop()

	// First connection (new pairing)
	conn1, err := client.Connect(ctx, server.Addr())
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	conn1.Close()
	<-incomingS // consume incoming

	// Client should now have a token for the server peer ID (not address)
	client.mu.RLock()
	token, hasToken := client.tokens["hub-s"]
	client.mu.RUnlock()
	if !hasToken || token == "" {
		t.Error("client should have stored a token for hub-s")
	}

	// Second connection (should use stored token)
	conn2, err := client.Connect(ctx, server.Addr())
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	conn2.Close()
	<-incomingS // consume incoming
}
