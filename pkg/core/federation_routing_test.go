package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// mockFederation implements FederationForwarder for testing.
type mockFederation struct {
	mu               sync.Mutex
	statusBroadcasts []struct {
		AgentID string
		Status  protocol.AgentStatus
	}
}

func (m *mockFederation) BroadcastAgentStatus(_ context.Context, agentID string, status protocol.AgentStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusBroadcasts = append(m.statusBroadcasts, struct {
		AgentID string
		Status  protocol.AgentStatus
	}{agentID, status})
}

func TestMessageRouteToRemoteAgent(t *testing.T) {
	hub := newTestHub(t)
	fed := &mockFederation{}
	hub.SetFederation(fed)

	ctx := context.Background()
	now := time.Now()

	// Register a local agent.
	localAgent := &protocol.Agent{
		AgentID:         "local-1",
		ProjectName:     "frontend",
		Username:        "marc",
		Hostname:        "myhost",
		LocalPath:       "/dev/fe",
		Status:          protocol.AgentStatusOnline,
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := hub.Agents().Register(ctx, localAgent); err != nil {
		t.Fatal(err)
	}

	// Insert a remote agent (from peer hub) directly into the store.
	remoteAgent := &protocol.Agent{
		AgentID:         "remote-1",
		ProjectName:     "backend",
		Username:        "bob",
		Hostname:        "bob-pc",
		LocalPath:       "/dev/api",
		Status:          protocol.AgentStatusOnline,
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: protocol.ProtocolVersion,
		PeerHub:         "peer-bob",
	}
	if err := hub.Store().UpsertAgent(ctx, remoteAgent); err != nil {
		t.Fatal(err)
	}

	// Send message from local to remote — goes through SyncEngine which routes
	// to the peer hub via ConversationSyncer. The send itself must not error.
	msg := &protocol.Message{
		From: "local-1",
		To:   "remote-1",
		Type: protocol.MessageTypeQuestion,
		Body: "What's the API schema?",
	}
	if err := hub.Messages().Send(ctx, msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Verify a conversation was created with both participants.
	convs, err := hub.Store().ListConversations(ctx, store.ConversationFilter{Participant: "local-1"})
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(convs) == 0 {
		t.Fatal("expected a conversation to be created")
	}
}

func TestMessageRouteToRemoteAgentNoFederation(t *testing.T) {
	hub := newTestHub(t)
	// Federation is NOT set — but the SyncEngine handles peer delivery, so
	// sending to a remote agent should succeed (SyncEngine will retry on peer connect).

	ctx := context.Background()
	now := time.Now()

	remoteAgent := &protocol.Agent{
		AgentID:         "remote-1",
		ProjectName:     "backend",
		PeerHub:         "peer-bob",
		Status:          protocol.AgentStatusOnline,
		Username:        "bob",
		Hostname:        "bob-pc",
		LocalPath:       "/dev/api",
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := hub.Store().UpsertAgent(ctx, remoteAgent); err != nil {
		t.Fatal(err)
	}

	msg := &protocol.Message{
		From: "local-1",
		To:   "remote-1",
		Type: protocol.MessageTypeContext,
		Body: "test",
	}
	// Now succeeds — delivery is deferred to sync engine, not immediate federation call.
	if err := hub.Messages().Send(ctx, msg); err != nil {
		t.Errorf("unexpected error sending to remote agent without active federation: %v", err)
	}
}

func TestAgentRegisterBroadcastsToFederation(t *testing.T) {
	hub := newTestHub(t)
	fed := &mockFederation{}
	hub.SetFederation(fed)

	ctx := context.Background()

	agent := &protocol.Agent{
		AgentID:         "new-agent",
		ProjectName:     "mobile",
		Username:        "alice",
		Hostname:        "alice-pc",
		LocalPath:       "/dev/mobile",
		ConnectedAt:     time.Now(),
		LastSeen:        time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := hub.Agents().Register(ctx, agent); err != nil {
		t.Fatal(err)
	}

	fed.mu.Lock()
	defer fed.mu.Unlock()
	if len(fed.statusBroadcasts) != 1 {
		t.Errorf("expected 1 broadcast, got %d", len(fed.statusBroadcasts))
	} else if fed.statusBroadcasts[0].Status != protocol.AgentStatusOnline {
		t.Errorf("expected online, got %s", fed.statusBroadcasts[0].Status)
	}
}

func TestBroadcastSkipsRemoteAgents(t *testing.T) {
	hub := newTestHub(t)
	fed := &mockFederation{}
	hub.SetFederation(fed)

	ctx := context.Background()
	now := time.Now()

	// Insert local agents directly into the store (no WebSocket connections).
	for _, name := range []string{"fe", "be"} {
		a := &protocol.Agent{
			AgentID:         "local-" + name,
			ProjectName:     name,
			Username:        "marc",
			Hostname:        "myhost",
			LocalPath:       "/dev/" + name,
			Status:          protocol.AgentStatusOnline,
			ConnectedAt:     now,
			LastSeen:        now,
			ProtocolVersion: protocol.ProtocolVersion,
		}
		if err := hub.Store().UpsertAgent(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	// Insert a remote agent on peer-bob.
	if err := hub.Store().UpsertAgent(ctx, &protocol.Agent{
		AgentID:         "remote-mob",
		ProjectName:     "mobile",
		PeerHub:         "peer-bob",
		Status:          protocol.AgentStatusOnline,
		Username:        "bob",
		Hostname:        "bob-pc",
		LocalPath:       "/dev/mob",
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: protocol.ProtocolVersion,
	}); err != nil {
		t.Fatal(err)
	}

	msg := &protocol.Message{
		From: "local-fe",
		To:   "*",
		Type: protocol.MessageTypeContext,
		Body: "deploy starting",
	}
	if err := hub.Messages().Send(ctx, msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Broadcast no longer directly forwards to peer hubs — the SyncEngine handles
	// peer delivery via conversation events. BroadcastAgentStatus is only for agent
	// status changes, not messages.
	fed.mu.Lock()
	defer fed.mu.Unlock()
	if len(fed.statusBroadcasts) != 0 {
		t.Errorf("expected no status broadcasts from message send, got %d", len(fed.statusBroadcasts))
	}
}
