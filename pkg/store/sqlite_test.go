package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

func newTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAgentCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	agent := &protocol.Agent{
		AgentID:         "agent-001",
		Aliases:         []string{"alpha", "a1"},
		Username:        "marc",
		Hostname:        "devbox",
		LocalPath:       "/home/marc/proj",
		ProjectName:     "bifrost",
		Capabilities:    []string{"tools", "context"},
		DisplayName:     "Marc Dev",
		Status:          protocol.AgentStatusOnline,
		DNDReason:       "",
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: "1.0",
		PeerHub:         "",
	}

	// Insert
	if err := s.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	// Read and verify fields
	got, err := s.GetAgent(ctx, "agent-001")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got == nil {
		t.Fatal("expected agent, got nil")
	}
	if got.AgentID != agent.AgentID {
		t.Errorf("AgentID: want %q got %q", agent.AgentID, got.AgentID)
	}
	if got.Username != agent.Username {
		t.Errorf("Username: want %q got %q", agent.Username, got.Username)
	}
	if len(got.Aliases) != 2 || got.Aliases[0] != "alpha" || got.Aliases[1] != "a1" {
		t.Errorf("Aliases: want [alpha a1] got %v", got.Aliases)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "tools" {
		t.Errorf("Capabilities: want [tools context] got %v", got.Capabilities)
	}
	if got.Status != protocol.AgentStatusOnline {
		t.Errorf("Status: want online got %q", got.Status)
	}
	if !got.ConnectedAt.Equal(now) {
		t.Errorf("ConnectedAt: want %v got %v", now, got.ConnectedAt)
	}

	// List
	agents, err := s.ListAgents(ctx, store.AgentFilter{})
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}

	// Update status
	if err := s.UpdateAgentStatus(ctx, "agent-001", protocol.AgentStatusDND, "in meeting"); err != nil {
		t.Fatalf("UpdateAgentStatus: %v", err)
	}
	got, _ = s.GetAgent(ctx, "agent-001")
	if got.Status != protocol.AgentStatusDND {
		t.Errorf("status after update: want dnd got %q", got.Status)
	}
	if got.DNDReason != "in meeting" {
		t.Errorf("dnd_reason: want 'in meeting' got %q", got.DNDReason)
	}

	// Touch
	before := time.Now()
	if err := s.TouchAgent(ctx, "agent-001"); err != nil {
		t.Fatalf("TouchAgent: %v", err)
	}
	got, _ = s.GetAgent(ctx, "agent-001")
	if got.LastSeen.Before(before.Add(-time.Second)) {
		t.Errorf("LastSeen not updated: %v", got.LastSeen)
	}

	// List with status filter (no match)
	filtered, err := s.ListAgents(ctx, store.AgentFilter{Status: protocol.AgentStatusOnline})
	if err != nil {
		t.Fatalf("ListAgents filtered: %v", err)
	}
	if len(filtered) != 0 {
		t.Errorf("expected 0 online agents, got %d", len(filtered))
	}

	// Delete
	if err := s.DeleteAgent(ctx, "agent-001"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	got, err = s.GetAgent(ctx, "agent-001")
	if err != nil {
		t.Fatalf("GetAgent after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestMessageCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	msg := &protocol.Message{
		ID:             "msg-001",
		ConversationID: "conv-001",
		From:           "agent-a",
		To:             "agent-b",
		Type:           protocol.MessageTypeQuestion,
		Subject:        "Hello",
		Body:           "Are you there?",
		Priority:       protocol.PriorityNormal,
		Timestamp:      now,
		Acknowledged:   false,
	}

	if err := s.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	// Get by ID
	got, err := s.GetMessage(ctx, "msg-001")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got == nil {
		t.Fatal("expected message, got nil")
	}
	if got.Body != msg.Body {
		t.Errorf("Body: want %q got %q", msg.Body, got.Body)
	}
	if got.Acknowledged {
		t.Error("expected unacknowledged")
	}

	// List unread for recipient
	unread, err := s.ListMessages(ctx, store.MessageFilter{To: "agent-b", Unread: true})
	if err != nil {
		t.Fatalf("ListMessages unread: %v", err)
	}
	if len(unread) != 1 {
		t.Fatalf("expected 1 unread, got %d", len(unread))
	}

	// Ack
	if err := s.AckMessage(ctx, "msg-001"); err != nil {
		t.Fatalf("AckMessage: %v", err)
	}

	// Verify no unread
	unread, err = s.ListMessages(ctx, store.MessageFilter{To: "agent-b", Unread: true})
	if err != nil {
		t.Fatalf("ListMessages after ack: %v", err)
	}
	if len(unread) != 0 {
		t.Errorf("expected 0 unread after ack, got %d", len(unread))
	}

	// Not found returns nil
	missing, err := s.GetMessage(ctx, "no-such-id")
	if err != nil {
		t.Fatalf("GetMessage missing: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for missing message")
	}
}

func TestMessageQueue(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC()
	msg1 := &protocol.Message{
		ID:        "q-msg-001",
		From:      "agent-a",
		To:        "agent-b",
		Type:      protocol.MessageTypeContext,
		Body:      "queued payload 1",
		Priority:  protocol.PriorityNormal,
		Timestamp: now,
	}
	msg2 := &protocol.Message{
		ID:        "q-msg-002",
		From:      "agent-a",
		To:        "agent-b",
		Type:      protocol.MessageTypeContext,
		Body:      "queued payload 2",
		Priority:  protocol.PriorityNormal,
		Timestamp: now,
	}

	if err := s.EnqueueMessage(ctx, "agent-b", msg1); err != nil {
		t.Fatalf("EnqueueMessage 1: %v", err)
	}
	if err := s.EnqueueMessage(ctx, "agent-b", msg2); err != nil {
		t.Fatalf("EnqueueMessage 2: %v", err)
	}

	// Dequeue — should return both in order
	msgs, err := s.DequeueMessages(ctx, "agent-b")
	if err != nil {
		t.Fatalf("DequeueMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Body != "queued payload 1" {
		t.Errorf("first dequeued body: want %q got %q", "queued payload 1", msgs[0].Body)
	}
	if msgs[1].Body != "queued payload 2" {
		t.Errorf("second dequeued body: want %q got %q", "queued payload 2", msgs[1].Body)
	}

	// Dequeue again — should be empty
	msgs, err = s.DequeueMessages(ctx, "agent-b")
	if err != nil {
		t.Fatalf("DequeueMessages second call: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 after dequeue, got %d", len(msgs))
	}
}

func TestSubscriptions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Subscribe two agents to a channel
	if err := s.Subscribe(ctx, "agent-a", "channel:general"); err != nil {
		t.Fatalf("Subscribe agent-a to general: %v", err)
	}
	if err := s.Subscribe(ctx, "agent-b", "channel:general"); err != nil {
		t.Fatalf("Subscribe agent-b to general: %v", err)
	}

	// Subscribe to a task target
	if err := s.Subscribe(ctx, "agent-a", "task:task-001"); err != nil {
		t.Fatalf("Subscribe agent-a to task: %v", err)
	}

	// Get subscribers for channel
	subs, err := s.GetSubscribers(ctx, "channel:general")
	if err != nil {
		t.Fatalf("GetSubscribers: %v", err)
	}
	if len(subs) != 2 {
		t.Errorf("expected 2 subscribers, got %d: %v", len(subs), subs)
	}

	// List channels — prefix should be stripped
	channels, err := s.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d: %v", len(channels), channels)
	}
	if channels[0] != "general" {
		t.Errorf("channel name: want 'general' got %q", channels[0])
	}

	// Unsubscribe agent-b
	if err := s.Unsubscribe(ctx, "agent-b", "channel:general"); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	// Verify count dropped
	subs, err = s.GetSubscribers(ctx, "channel:general")
	if err != nil {
		t.Fatalf("GetSubscribers after unsubscribe: %v", err)
	}
	if len(subs) != 1 {
		t.Errorf("expected 1 subscriber after unsubscribe, got %d", len(subs))
	}
	if subs[0] != "agent-a" {
		t.Errorf("remaining subscriber: want 'agent-a' got %q", subs[0])
	}

	// Idempotent subscribe
	if err := s.Subscribe(ctx, "agent-a", "channel:general"); err != nil {
		t.Fatalf("duplicate Subscribe should not error: %v", err)
	}
	subs, _ = s.GetSubscribers(ctx, "channel:general")
	if len(subs) != 1 {
		t.Errorf("duplicate subscribe inflated count: got %d", len(subs))
	}
}
