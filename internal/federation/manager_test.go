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

func TestManagerSyncConversation(t *testing.T) {
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

	conv := &protocol.Conversation{
		ConversationID: "conv-1",
		Participants:   []string{"local-agent", "remote-agent"},
		CreatedAt:      time.Now(),
	}
	events := []*protocol.Event{
		{
			ID:             "ev-1",
			ConversationID: "conv-1",
			Type:           protocol.EventTypeMessage,
			FromAgent:      "local-agent",
			Timestamp:      time.Now(),
			Data:           protocol.EventData{Body: "hello remote"},
		},
	}

	err := mgr.SyncConversation(ctx, "peer-remote", conv, events)
	if err != nil {
		t.Fatalf("SyncConversation: %v", err)
	}

	// Give the write loop a moment to pick up the envelope from sendCh
	time.Sleep(50 * time.Millisecond)

	// Verify envelope was sent with the new method name
	sent := conn.Sent()
	if len(sent) == 0 {
		t.Fatal("no envelopes sent")
	}
	if sent[0].Method != "peer.conversation_sync" {
		t.Errorf("expected peer.conversation_sync, got %s", sent[0].Method)
	}
}

func TestManagerSyncConversationFailsWhenDisconnected(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	conv := &protocol.Conversation{
		ConversationID: "conv-1",
		Participants:   []string{"local-agent", "remote-agent"},
		CreatedAt:      time.Now(),
	}
	events := []*protocol.Event{
		{ID: "ev-1", ConversationID: "conv-1", Type: protocol.EventTypeMessage},
	}

	err := mgr.SyncConversation(ctx, "peer-away", conv, events)
	if err == nil {
		t.Error("expected error when peer is not connected")
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

func TestManagerHandleConversationSync(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Prepare a conversation_sync envelope as if received from a remote peer.
	conv := protocol.ConvMeta{
		ID:           "conv-sync-1",
		Participants: []string{"local-agent", "remote-agent"},
		CreatedAt:    time.Now(),
	}
	events := []*protocol.Event{
		{
			ID:             "ev-sync-1",
			ConversationID: "conv-sync-1",
			Type:           protocol.EventTypeMessage,
			FromAgent:      "remote-agent",
			Timestamp:      time.Now(),
			Data:           protocol.EventData{Body: "hello from remote"},
		},
	}
	payload, _ := json.Marshal(protocol.PeerConversationSyncPayload{
		Conversation: conv,
		Events:       events,
	})
	env := &protocol.PeerEnvelope{
		Method: "peer.conversation_sync", ID: "r1", Version: protocol.ProtocolVersion,
		From: "peer-sender", Payload: payload,
	}

	// Handle the envelope (simulates receiving from peer).
	mgr.handlePeerEnvelope(ctx, "peer-sender", env)

	// Verify conversation was saved.
	saved, err := s.GetConversation(ctx, "conv-sync-1")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if saved == nil {
		t.Fatal("expected conversation to be saved")
	}

	// Verify event was appended.
	evts, err := s.ListEventsSince(ctx, "conv-sync-1", "")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evts) != 1 {
		t.Errorf("expected 1 event, got %d", len(evts))
	}
}
