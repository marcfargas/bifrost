package federation

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// mockPeerConn implements PeerConn for testing.
type mockPeerConn struct {
	id      string
	sendBuf []*protocol.PeerEnvelope
	recvCh  chan *protocol.PeerEnvelope
	mu      sync.Mutex
	closed  bool
}

func newMockPeerConn(id string) *mockPeerConn {
	return &mockPeerConn{
		id:     id,
		recvCh: make(chan *protocol.PeerEnvelope, 32),
	}
}

func (c *mockPeerConn) PeerID() string { return c.id }

func (c *mockPeerConn) Send(_ context.Context, env *protocol.PeerEnvelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendBuf = append(c.sendBuf, env)
	return nil
}

func (c *mockPeerConn) Receive(ctx context.Context) (*protocol.PeerEnvelope, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case env := <-c.recvCh:
		return env, nil
	}
}

func (c *mockPeerConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *mockPeerConn) Sent() []*protocol.PeerEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]*protocol.PeerEnvelope, len(c.sendBuf))
	copy(cp, c.sendBuf)
	return cp
}

// mockTransport implements Transport for testing.
type mockTransport struct {
	name  string
	conns []*mockPeerConn
	mu    sync.Mutex
}

func newMockTransport(name string) *mockTransport {
	return &mockTransport{name: name}
}

func (t *mockTransport) Name() string { return t.name }

func (t *mockTransport) Start(_ context.Context, _ chan<- PeerConn) error {
	return nil
}

func (t *mockTransport) Connect(_ context.Context, address string) (PeerConn, error) {
	conn := newMockPeerConn("peer-" + address)
	t.mu.Lock()
	t.conns = append(t.conns, conn)
	t.mu.Unlock()
	return conn, nil
}

func (t *mockTransport) Stop() error { return nil }

func newTestManager(t *testing.T) (*Manager, store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	hub := core.NewHub(s)
	cfg := config.Defaults()
	logger := slog.Default()

	mgr := NewManager(hub, s, &cfg, "local-hub-id", logger)
	return mgr, s
}

func TestManagerForwardMessage(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Simulate a connected peer
	conn := newMockPeerConn("peer-remote")
	peer := &protocol.Peer{
		PeerID: "peer-remote", Status: protocol.PeerStatusConnected,
		LastSeen: time.Now(), ConnectedAt: time.Now(),
		ProtoVersion: protocol.ProtocolVersion,
	}
	s.UpsertPeer(ctx, peer)
	mgr.addPeerConn(ctx, "peer-remote", conn, peer)

	msg := &protocol.Message{
		ID: "m1", ConversationID: "c1", From: "local-agent",
		To: "remote-agent", Type: protocol.MessageTypeQuestion,
		Body: "hello remote", Priority: protocol.PriorityNormal,
		Timestamp: time.Now(),
	}

	status, err := mgr.ForwardMessage(ctx, "peer-remote", msg)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if status != protocol.DeliveryStatusDelivered {
		t.Errorf("expected delivered, got %s", status)
	}

	// Give the write loop a moment to pick up the message from sendCh
	time.Sleep(50 * time.Millisecond)

	// Verify envelope was sent (check sendCh was used via mock's sendBuf)
	sent := conn.Sent()
	if len(sent) == 0 {
		t.Fatal("no envelopes sent")
	}
	if sent[0].Method != "peer.message" {
		t.Errorf("expected peer.message, got %s", sent[0].Method)
	}
}

func TestManagerForwardMessageQueuedWhenDisconnected(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Peer exists in store but is not connected
	peer := &protocol.Peer{
		PeerID: "peer-away", Status: protocol.PeerStatusDisconnected,
		LastSeen: time.Now(), ConnectedAt: time.Now(),
		ProtoVersion: protocol.ProtocolVersion,
	}
	s.UpsertPeer(ctx, peer)

	msg := &protocol.Message{
		ID: "m2", ConversationID: "c1", From: "local-agent",
		To: "remote-agent", Type: protocol.MessageTypeContext,
		Body: "queued message", Priority: protocol.PriorityNormal,
		Timestamp: time.Now(),
	}

	status, err := mgr.ForwardMessage(ctx, "peer-away", msg)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if status != protocol.DeliveryStatusQueuedUnreachable {
		t.Errorf("expected queued (hub unreachable), got %s", status)
	}

	// Verify message was queued
	queued, err := s.DequeuePeerMessages(ctx, "peer-away")
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(queued) != 1 {
		t.Errorf("expected 1 queued, got %d", len(queued))
	}
}

func TestManagerSyncAgents(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Build a sync_agents envelope
	remoteAgents := []*protocol.Agent{
		{
			AgentID: "remote-a1", ProjectName: "mobile-app",
			Status: protocol.AgentStatusOnline, Username: "bob",
			Hostname: "bob-laptop", LocalPath: "/dev/mobile",
			ConnectedAt: time.Now(), LastSeen: time.Now(),
			ProtocolVersion: protocol.ProtocolVersion,
		},
	}
	payload, _ := json.Marshal(protocol.PeerSyncAgentsPayload{Agents: remoteAgents})
	env := &protocol.PeerEnvelope{
		Method: "peer.sync_agents", ID: "s1", Version: protocol.ProtocolVersion,
		From: "peer-bob", Payload: payload,
	}

	mgr.handlePeerEnvelope(ctx, "peer-bob", env)

	// Verify remote agent was registered with peer_hub tag
	agent, err := s.GetAgent(ctx, "remote-a1")
	if err != nil {
		t.Fatalf("get remote agent: %v", err)
	}
	if agent == nil {
		t.Fatal("remote agent not found")
	}
	if agent.PeerHub != "peer-bob" {
		t.Errorf("expected peer_hub=peer-bob, got %s", agent.PeerHub)
	}
	if agent.ProjectName != "mobile-app" {
		t.Errorf("expected mobile-app, got %s", agent.ProjectName)
	}
}

func TestManagerHandlePeerDisconnect(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Register a remote agent from a peer
	agent := &protocol.Agent{
		AgentID: "remote-x", ProjectName: "infra", PeerHub: "peer-x",
		Status: protocol.AgentStatusOnline, Username: "alice",
		Hostname: "alice-pc", LocalPath: "/dev/infra",
		ConnectedAt: time.Now(), LastSeen: time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}
	s.UpsertAgent(ctx, agent)

	peer := &protocol.Peer{
		PeerID: "peer-x", Status: protocol.PeerStatusConnected,
		LastSeen: time.Now(), ConnectedAt: time.Now(),
		ProtoVersion: protocol.ProtocolVersion,
	}
	s.UpsertPeer(ctx, peer)

	conn := newMockPeerConn("peer-x")
	mgr.addPeerConn(ctx, "peer-x", conn, peer)

	// Disconnect
	mgr.handlePeerDisconnect("peer-x")

	// Agent should be deleted (pruned) on disconnect; it will be re-synced on reconnect.
	got, _ := s.GetAgent(ctx, "remote-x")
	if got != nil {
		t.Fatalf("expected agent to be deleted on disconnect, but it still exists with status %s", got.Status)
	}

	// Peer should be disconnected
	gotPeer, _ := s.GetPeer(ctx, "peer-x")
	if gotPeer == nil {
		t.Fatal("peer not found after disconnect")
	}
	if gotPeer.Status != protocol.PeerStatusDisconnected {
		t.Errorf("expected disconnected, got %s", gotPeer.Status)
	}
}

func TestManagerFlushPeerQueue(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Queue some messages for a peer
	payload, _ := json.Marshal(protocol.PeerMessagePayload{
		Message: &protocol.Message{
			ID: "q1", ConversationID: "c1", From: "a", To: "b",
			Type: protocol.MessageTypeContext, Body: "queued",
			Priority: protocol.PriorityNormal, Timestamp: time.Now(),
		},
	})
	env := &protocol.PeerEnvelope{
		Method: "peer.message", ID: "r1", Version: protocol.ProtocolVersion,
		From: "local-hub-id", Payload: payload,
	}
	s.EnqueuePeerMessage(ctx, "peer-flush", env)

	// Connect the peer
	conn := newMockPeerConn("peer-flush")
	peer := &protocol.Peer{
		PeerID: "peer-flush", Status: protocol.PeerStatusConnected,
		LastSeen: time.Now(), ConnectedAt: time.Now(),
		ProtoVersion: protocol.ProtocolVersion,
	}
	s.UpsertPeer(ctx, peer)
	mgr.addPeerConn(ctx, "peer-flush", conn, peer)

	// Flush
	mgr.flushPeerQueue(ctx, "peer-flush")

	// Give the write loop a moment to process
	time.Sleep(50 * time.Millisecond)

	// Verify message was sent
	sent := conn.Sent()
	if len(sent) == 0 {
		t.Fatal("no envelopes sent after flush")
	}

	// Verify queue is empty
	remaining, _ := s.DequeuePeerMessages(ctx, "peer-flush")
	if len(remaining) != 0 {
		t.Errorf("expected empty queue after flush, got %d", len(remaining))
	}
}
