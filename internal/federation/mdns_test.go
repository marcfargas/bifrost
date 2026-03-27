package federation

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestMDNSTransportStartStop(t *testing.T) {
	mt := NewMDNSTransport(MDNSTransportConfig{
		HubID:  "test-hub-1",
		Logger: slog.Default(),
	})

	if mt.Name() != "mdns" {
		t.Errorf("expected name mdns, got %s", mt.Name())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	incoming := make(chan PeerConn, 4)
	if err := mt.Start(ctx, incoming); err != nil {
		t.Fatal(err)
	}

	if mt.port == 0 {
		t.Error("port should be assigned after start")
	}

	if err := mt.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestMDNSTransportConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger := slog.Default()

	// Start hub A.
	mtA := NewMDNSTransport(MDNSTransportConfig{HubID: "hub-a", Logger: logger})
	incomingA := make(chan PeerConn, 4)
	if err := mtA.Start(ctx, incomingA); err != nil {
		t.Fatal(err)
	}
	defer mtA.Stop()

	// Start hub B and connect to A directly (bypassing mDNS discovery for test reliability).
	mtB := NewMDNSTransport(MDNSTransportConfig{HubID: "hub-b", Logger: logger})
	incomingB := make(chan PeerConn, 4)
	if err := mtB.Start(ctx, incomingB); err != nil {
		t.Fatal(err)
	}
	defer mtB.Stop()

	// Direct connect B -> A.
	addr := mtA.listener.Addr().String()
	conn, err := mtB.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if conn.PeerID() == "" {
		t.Error("peer ID should not be empty")
	}

	// A should accept the connection.
	select {
	case incoming := <-incomingA:
		defer incoming.Close()
		if incoming.PeerID() == "" {
			t.Error("incoming peer ID should not be empty")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for incoming connection on A")
	}
}

func TestMDNSTransportImplementsInterface(t *testing.T) {
	var _ Transport = (*MDNSTransport)(nil)
}
