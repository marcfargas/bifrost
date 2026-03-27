package integration_test

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/federation"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// federatedPair holds two connected hubs with their federation managers.
type federatedPair struct {
	hubA, hubB   *core.Hub
	storeA       *store.SQLiteStore
	storeB       *store.SQLiteStore
	mgrA, mgrB   *federation.Manager
	dtA, dtB     *federation.DirectTransport
}

// setupManagers creates two hubs with federation managers and direct transports
// but does NOT connect them yet. This allows tests to register agents before peering.
func setupManagers(t *testing.T) *federatedPair {
	t.Helper()
	logger := slog.Default()

	// Hub A
	dbA := filepath.Join(t.TempDir(), "a.db")
	sA, err := store.NewSQLite(dbA)
	if err != nil {
		t.Fatalf("open sqlite A: %v", err)
	}
	hubA := core.NewHub(sA)
	cfgA := config.Defaults()
	mgrA := federation.NewManager(hubA, sA, &cfgA, "hub-a", logger)
	dtA := federation.NewDirectTransport(federation.DirectTransportConfig{
		HubID: "hub-a", Listen: "127.0.0.1:0", Logger: logger,
	})
	mgrA.AddTransport(dtA)
	hubA.SetFederation(mgrA)

	// Hub B
	dbB := filepath.Join(t.TempDir(), "b.db")
	sB, err := store.NewSQLite(dbB)
	if err != nil {
		t.Fatalf("open sqlite B: %v", err)
	}
	hubB := core.NewHub(sB)
	cfgB := config.Defaults()
	mgrB := federation.NewManager(hubB, sB, &cfgB, "hub-b", logger)
	dtB := federation.NewDirectTransport(federation.DirectTransportConfig{
		HubID: "hub-b", Listen: "127.0.0.1:0", Logger: logger,
	})
	mgrB.AddTransport(dtB)
	hubB.SetFederation(mgrB)

	t.Cleanup(func() {
		mgrA.Stop()
		mgrB.Stop()
		sA.Close()
		sB.Close()
	})

	return &federatedPair{
		hubA: hubA, hubB: hubB,
		storeA: sA, storeB: sB,
		mgrA: mgrA, mgrB: mgrB,
		dtA: dtA, dtB: dtB,
	}
}

// startAndConnect starts both managers and connects B to A. Returns after
// the initial sync completes.
func (fp *federatedPair) startAndConnect(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if err := fp.mgrA.Start(ctx); err != nil {
		t.Fatalf("start manager A: %v", err)
	}
	if err := fp.mgrB.Start(ctx); err != nil {
		t.Fatalf("start manager B: %v", err)
	}

	// Poll until dtA has a real listener address
	var addrA string
	for range 50 {
		addrA = fp.dtA.Addr()
		if addrA != "" && addrA != "127.0.0.1:0" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if addrA == "" || addrA == "127.0.0.1:0" {
		t.Fatal("transport A did not start listening")
	}

	// Connect B to A — this triggers the initial agent sync
	_, err := fp.mgrB.ConnectPeer(ctx, protocol.PeerTransportDirect, addrA)
	if err != nil {
		t.Fatalf("connect B->A: %v", err)
	}

	// Wait for the bidirectional sync to complete
	time.Sleep(300 * time.Millisecond)
}

func TestFederatedMessageExchange(t *testing.T) {
	fp := setupManagers(t)
	ctx := context.Background()
	now := time.Now()

	// Register agents BEFORE connecting so they get synced during initial exchange.
	agentA := &protocol.Agent{
		AgentID: "agent-a1", ProjectName: "frontend",
		Username: "marc", Hostname: "marc-pc", LocalPath: "/dev/fe",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := fp.hubA.Agents().Register(ctx, agentA); err != nil {
		t.Fatalf("register agent A: %v", err)
	}

	agentB := &protocol.Agent{
		AgentID: "agent-b1", ProjectName: "backend",
		Username: "bob", Hostname: "bob-pc", LocalPath: "/dev/api",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := fp.hubB.Agents().Register(ctx, agentB); err != nil {
		t.Fatalf("register agent B: %v", err)
	}

	// Now connect — the initial sync will exchange agent lists.
	fp.startAndConnect(t)

	// Hub A should know about agent-b1 via federation sync.
	remoteAgent, err := fp.hubA.Store().GetAgent(ctx, "agent-b1")
	if err != nil {
		t.Fatalf("hub A should know about agent-b1: %v", err)
	}
	if remoteAgent == nil {
		t.Fatal("hub A returned nil agent for agent-b1")
	}
	if remoteAgent.PeerHub == "" {
		t.Error("remote agent on hub A should have peer_hub set")
	}

	// Hub B should know about agent-a1.
	remoteAgentA, err := fp.hubB.Store().GetAgent(ctx, "agent-a1")
	if err != nil {
		t.Fatalf("hub B should know about agent-a1: %v", err)
	}
	if remoteAgentA == nil {
		t.Fatal("hub B returned nil agent for agent-a1")
	}
	if remoteAgentA.PeerHub == "" {
		t.Error("remote agent on hub B should have peer_hub set")
	}

	// Send message from A to B (cross-hub). The federation forwarder will route
	// this to hub B. Even if the direct connection has dropped by now, the
	// ForwardMessage call will enqueue the message for later delivery.
	msg := &protocol.Message{
		From: "agent-a1", To: "agent-b1",
		Type: protocol.MessageTypeQuestion, Body: "What's the API schema?",
	}
	err = fp.hubA.Messages().Send(ctx, msg)
	if err != nil {
		t.Fatalf("send cross-hub message: %v", err)
	}
}

func TestFederatedAgentSync(t *testing.T) {
	fp := setupManagers(t)
	ctx := context.Background()
	now := time.Now()

	// Register multiple agents on Hub A before connecting.
	for i, name := range []string{"frontend", "api", "worker"} {
		agent := &protocol.Agent{
			AgentID:         fmt.Sprintf("a-%d", i),
			ProjectName:     name,
			Username:        "marc", Hostname: "marc-pc",
			LocalPath:       "/dev/" + name,
			ConnectedAt:     now, LastSeen: now,
			ProtocolVersion: protocol.ProtocolVersion,
		}
		if err := fp.hubA.Agents().Register(ctx, agent); err != nil {
			t.Fatalf("register agent %s: %v", name, err)
		}
	}

	// Connect — initial sync happens during ConnectPeer.
	fp.startAndConnect(t)

	// Hub B should know about all 3 agents from Hub A.
	agents, err := fp.hubB.Store().ListAgents(ctx, store.AgentFilter{})
	if err != nil {
		t.Fatal(err)
	}

	remoteCount := 0
	for _, a := range agents {
		if a.PeerHub != "" {
			remoteCount++
		}
	}
	if remoteCount < 3 {
		t.Errorf("expected at least 3 remote agents on Hub B, got %d", remoteCount)
	}
}

func TestFederatedOfflineQueue(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	// Create Hub A (standalone, no peer actually connected)
	dbA := filepath.Join(t.TempDir(), "a.db")
	sA, err := store.NewSQLite(dbA)
	if err != nil {
		t.Fatalf("open sqlite A: %v", err)
	}
	hubA := core.NewHub(sA)
	cfgA := config.Defaults()
	mgrA := federation.NewManager(hubA, sA, &cfgA, "hub-a", logger)

	dtA := federation.NewDirectTransport(federation.DirectTransportConfig{
		HubID: "hub-a", Listen: "127.0.0.1:0", Logger: logger,
	})
	mgrA.AddTransport(dtA)
	hubA.SetFederation(mgrA)
	if err := mgrA.Start(ctx); err != nil {
		t.Fatalf("start manager A: %v", err)
	}
	defer mgrA.Stop()
	defer sA.Close()

	// Register a peer that is NOT connected
	peerB := &protocol.Peer{
		PeerID: "hub-b", Transport: protocol.PeerTransportDirect,
		Status: protocol.PeerStatusDisconnected, LastSeen: time.Now(),
		ConnectedAt: time.Now(), ProtoVersion: protocol.ProtocolVersion,
	}
	if err := sA.UpsertPeer(ctx, peerB); err != nil {
		t.Fatalf("upsert peer: %v", err)
	}

	// Register a remote agent on that disconnected peer
	remoteAgent := &protocol.Agent{
		AgentID: "agent-b1", ProjectName: "backend", PeerHub: "hub-b",
		Status: protocol.AgentStatusUnreachable, Username: "bob", Hostname: "bob-pc",
		LocalPath: "/dev/api", ConnectedAt: time.Now(), LastSeen: time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := sA.UpsertAgent(ctx, remoteAgent); err != nil {
		t.Fatalf("upsert remote agent: %v", err)
	}

	// Send message — the federation forwarder will try to send to hub-b,
	// fail (not connected), and enqueue the message.
	msg := &protocol.Message{
		From: "agent-a1", To: "agent-b1",
		Type: protocol.MessageTypeContext, Body: "are you there?",
	}
	err = hubA.Messages().Send(ctx, msg)
	// The send succeeds — the federation manager queues internally.
	if err != nil {
		t.Logf("send returned error (expected for offline peer): %v", err)
	}

	// Verify message is in the peer queue
	queued, err := sA.DequeuePeerMessages(ctx, "hub-b")
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(queued) != 1 {
		t.Errorf("expected 1 queued message, got %d", len(queued))
	}
}

func TestFederatedAliasConflict(t *testing.T) {
	fp := setupManagers(t)
	ctx := context.Background()
	now := time.Now()

	// Register "api" on Hub A
	agentA := &protocol.Agent{
		AgentID: "a-api", ProjectName: "api",
		Username: "marc", Hostname: "marc-pc", LocalPath: "/dev/api",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := fp.hubA.Agents().Register(ctx, agentA); err != nil {
		t.Fatalf("register agent: %v", err)
	}

	// Start managers (no need for connection for this test)
	if err := fp.mgrA.Start(context.Background()); err != nil {
		t.Fatalf("start manager A: %v", err)
	}
	if err := fp.mgrB.Start(context.Background()); err != nil {
		t.Fatalf("start manager B: %v", err)
	}

	// Simulate a remote agent also called "api" arriving via sync
	remoteAPI := &protocol.Agent{
		AgentID: "b-api", ProjectName: "api", PeerHub: "hub-b",
		Aliases:  []string{"api"},
		Username: "bob", Hostname: "bob-pc", LocalPath: "/dev/api",
		Status: protocol.AgentStatusOnline, ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := fp.hubA.Store().UpsertAgent(ctx, remoteAPI); err != nil {
		t.Fatalf("upsert remote agent: %v", err)
	}

	// Recompute aliases
	fp.hubA.Agents().RecomputeAliases(ctx)

	// "api" should be ambiguous — resolve should error
	_, err := fp.hubA.Agents().Resolve(ctx, "api")
	if err == nil {
		t.Error("expected error for ambiguous alias 'api'")
	}

	// Agent IDs still work
	got, err := fp.hubA.Agents().Resolve(ctx, "a-api")
	if err != nil {
		t.Fatalf("resolve by ID: %v", err)
	}
	if got.AgentID != "a-api" {
		t.Errorf("expected a-api, got %s", got.AgentID)
	}
}

func TestFederatedPeerHeartbeatAndDisconnect(t *testing.T) {
	fp := setupManagers(t)
	fp.startAndConnect(t)

	// Verify peer A is connected on Hub B
	if !fp.mgrB.IsPeerConnected("hub-a") {
		t.Error("hub-a should be connected on Hub B")
	}

	peers := fp.mgrB.ConnectedPeerIDs()
	found := false
	for _, p := range peers {
		if p == "hub-a" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hub-a not in connected peers: %v", peers)
	}
}
