package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// newTestHubWithConvTimeout creates a Hub with a custom conversation inactivity timeout.
func newTestHubWithConvTimeout(t *testing.T, timeout time.Duration) *Hub {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "bifrost_conv_test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("newTestHubWithConvTimeout: open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := NewHub(s)
	h.conversations = newConversationManager(s, h, timeout)
	return h
}

// saveStaleConversation saves a conversation with last_activity set to the given time.
func saveStaleConversation(t *testing.T, s store.Store, convID string, lastActivity time.Time) *protocol.Conversation {
	t.Helper()
	ctx := context.Background()
	conv := &protocol.Conversation{
		ConversationID: convID,
		Participants:   []string{"agent-a", "agent-b"},
		CreatedAt:      lastActivity,
	}
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("saveStaleConversation %s: %v", convID, err)
	}
	// SaveConversation sets last_activity = now. Back-date it so CloseStale sees it as stale.
	sq, ok := s.(*store.SQLiteStore)
	if !ok {
		t.Fatalf("saveStaleConversation: store is not *store.SQLiteStore")
	}
	if err := sq.SetLastActivity(ctx, convID, lastActivity); err != nil {
		t.Fatalf("saveStaleConversation SetLastActivity %s: %v", convID, err)
	}
	return conv
}

func TestConversationAutoCloseOnInactivity(t *testing.T) {
	ctx := context.Background()
	hub := newTestHubWithConvTimeout(t, 1*time.Second)

	// Create a conversation with last_activity 2 seconds in the past — stale by 1s timeout.
	old := time.Now().Add(-2 * time.Second)
	conv := saveStaleConversation(t, hub.store, "conv-stale-1", old)

	count, err := hub.Conversations().CloseStale(ctx)
	if err != nil {
		t.Fatalf("CloseStale: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 conversation closed, got %d", count)
	}

	// Verify it's actually closed in the store.
	got, err := hub.store.GetConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got == nil {
		t.Fatal("conversation not found after CloseStale")
	}
	if !got.Closed {
		t.Error("expected conversation to be closed")
	}
	if got.ClosedReason != protocol.ConversationCloseReasonInactivity {
		t.Errorf("expected close reason %q, got %q", protocol.ConversationCloseReasonInactivity, got.ClosedReason)
	}
}

func TestConversationActivityResetsTimer(t *testing.T) {
	ctx := context.Background()
	hub := newTestHubWithConvTimeout(t, 1*time.Second)

	// Create a conversation with last_activity 2 seconds in the past — would be stale.
	old := time.Now().Add(-2 * time.Second)
	conv := saveStaleConversation(t, hub.store, "conv-touch-1", old)

	// Touch the conversation to reset the timer to now.
	if err := hub.Conversations().Touch(ctx, conv.ConversationID); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	// CloseStale should not close it now — last_activity is fresh.
	count, err := hub.Conversations().CloseStale(ctx)
	if err != nil {
		t.Fatalf("CloseStale: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 conversations closed after touch, got %d", count)
	}

	// Verify it's still open.
	got, err := hub.store.GetConversation(ctx, conv.ConversationID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got == nil {
		t.Fatal("conversation not found")
	}
	if got.Closed {
		t.Error("expected conversation to remain open after touch")
	}
}

func TestTaskCompletionResetsConversationTimer(t *testing.T) {
	ctx := context.Background()
	// Use 10m timeout so the conversation won't auto-close during the test.
	hub := newTestHubWithConvTimeout(t, 10*time.Minute)

	registerAgent(t, hub, "conv-requester", "req-proj-conv")
	registerAgent(t, hub, "conv-assignee", "ass-proj-conv")

	// Create a task — this creates a linked conversation.
	task, err := hub.Tasks().CreateTask(ctx, "conv-requester", "conv-assignee", "Conv Timer Task", "desc")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	convID := task.ConversationID
	if convID == "" {
		t.Fatal("task has no conversation ID")
	}

	// Drive the task through its full lifecycle to completed.
	// Each UpdateTask call touches the conversation's last_activity.
	steps := []struct {
		caller string
		status protocol.TaskStatus
	}{
		{"conv-assignee", protocol.TaskStatusAccepted},
		{"conv-assignee", protocol.TaskStatusInProgress},
		{"conv-assignee", protocol.TaskStatusCompleted},
	}
	for _, step := range steps {
		task, err = hub.Tasks().UpdateTask(ctx, step.caller, task.TaskID, TaskUpdate{Status: step.status})
		if err != nil {
			t.Fatalf("UpdateTask to %s: %v", step.status, err)
		}
	}

	if task.Status != protocol.TaskStatusCompleted {
		t.Fatalf("expected task completed, got %s", task.Status)
	}

	// The conversation should still be OPEN — task completion touches the timer,
	// it does not close the conversation.
	got, err := hub.store.GetConversation(ctx, convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got == nil {
		t.Fatal("conversation not found after task completion")
	}
	if got.Closed {
		t.Error("expected conversation to remain OPEN after task completion (task completion only touches the timer)")
	}
}

func TestListConversationsActiveOnly(t *testing.T) {
	ctx := context.Background()
	hub := newTestHubWithConvTimeout(t, 10*time.Minute)

	now := time.Now()

	// Save an open conversation.
	openConv := &protocol.Conversation{
		ConversationID: "conv-list-open",
		Participants:   []string{"a", "b"},
		CreatedAt:      now,
		Closed:         false,
	}
	if err := hub.store.SaveConversation(ctx, openConv); err != nil {
		t.Fatalf("SaveConversation (open): %v", err)
	}

	// Save a conversation and then close it.
	closedConv := &protocol.Conversation{
		ConversationID: "conv-list-closed",
		Participants:   []string{"a", "b"},
		CreatedAt:      now,
		Closed:         false,
	}
	if err := hub.store.SaveConversation(ctx, closedConv); err != nil {
		t.Fatalf("SaveConversation (to-be-closed): %v", err)
	}
	if err := hub.store.CloseConversation(ctx, closedConv.ConversationID, protocol.ConversationCloseReasonExplicit); err != nil {
		t.Fatalf("CloseConversation: %v", err)
	}

	// List with active-only filter (Closed = false).
	isFalse := false
	active, err := hub.Conversations().List(ctx, store.ConversationFilter{Closed: &isFalse})
	if err != nil {
		t.Fatalf("List (active only): %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active conversation, got %d", len(active))
	}
	if len(active) > 0 && active[0].ConversationID != "conv-list-open" {
		t.Errorf("expected conv-list-open, got %q", active[0].ConversationID)
	}

	// List with closed-only filter (Closed = true).
	isTrue := true
	closed, err := hub.Conversations().List(ctx, store.ConversationFilter{Closed: &isTrue})
	if err != nil {
		t.Fatalf("List (closed only): %v", err)
	}
	if len(closed) != 1 {
		t.Errorf("expected 1 closed conversation, got %d", len(closed))
	}
	if len(closed) > 0 && closed[0].ConversationID != "conv-list-closed" {
		t.Errorf("expected conv-list-closed, got %q", closed[0].ConversationID)
	}

	// List all (no filter on Closed).
	all, err := hub.Conversations().List(ctx, store.ConversationFilter{})
	if err != nil {
		t.Fatalf("List (all): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 total conversations, got %d", len(all))
	}
}
