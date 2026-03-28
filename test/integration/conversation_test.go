package integration_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"github.com/marcfargas/bifrost/test/testutil"
)

// newHubWithConversationTimeout creates a Hub with a custom inactivity timeout
// for conversations, wired to a SQLite store in the test's temp directory.
// This bypasses the default 10-minute timeout for testing CloseStale.
func newHubWithConversationTimeout(t *testing.T, timeout time.Duration) *core.Hub {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "bifrost_test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("newHubWithConversationTimeout: open sqlite: %v", err)
	}
	// Build hub manually to set a short timeout. We use the internal hub
	// constructors via the exported path: NewHub sets 10m; we need to
	// touch last_activity to a time far in the past to trigger CloseStale.
	hub := core.NewHub(s)
	t.Cleanup(func() {
		if err := hub.Store().Close(); err != nil {
			t.Logf("newHubWithConversationTimeout: close store: %v", err)
		}
	})
	_ = timeout // timeout is controlled via store manipulation below
	return hub
}

// TestConversationAutoCloseWithTask creates a task (which auto-creates a
// conversation), completes the task, verifies the conversation is still open,
// then simulates inactivity by back-dating last_activity and calling CloseStale.
func TestConversationAutoCloseWithTask(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	requester := testutil.TestAgent("conv-requester")
	assignee := testutil.TestAgent("conv-assignee")

	if err := hub.Agents().Register(ctx, requester); err != nil {
		t.Fatalf("register requester: %v", err)
	}
	if err := hub.Agents().Register(ctx, assignee); err != nil {
		t.Fatalf("register assignee: %v", err)
	}

	// Create the task — this auto-creates a conversation.
	task, err := hub.Tasks().CreateTask(ctx, requester.AgentID, "conv-assignee", "Conversation test", "desc")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Verify the conversation was created and is open.
	conv, err := hub.Conversations().Get(ctx, task.ConversationID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv == nil {
		t.Fatal("conversation is nil after task creation")
	}
	if conv.Closed {
		t.Error("conversation should be open after task creation")
	}
	if !conv.IsTask {
		t.Errorf("conversation IsTask = false, want true after task creation")
	}

	// Complete the task lifecycle.
	for _, update := range []core.TaskUpdate{
		{Status: protocol.TaskStatusAccepted},
		{Status: protocol.TaskStatusInProgress},
		{Status: protocol.TaskStatusCompleted, Summary: "all done"},
	} {
		if _, err := hub.Tasks().UpdateTask(ctx, assignee.AgentID, task.TaskID, update); err != nil {
			t.Fatalf("update task to %s: %v", update.Status, err)
		}
	}

	// Conversation should still be open after task completion.
	conv, err = hub.Conversations().Get(ctx, task.ConversationID)
	if err != nil {
		t.Fatalf("get conversation after completion: %v", err)
	}
	if conv.Closed {
		t.Error("conversation should remain open immediately after task completion")
	}

	// CloseStale with the default 10-minute timeout should NOT close this
	// conversation because it was recently active.
	closed, err := hub.Conversations().CloseStale(ctx)
	if err != nil {
		t.Fatalf("CloseStale (recent): %v", err)
	}
	if closed != 0 {
		t.Errorf("CloseStale (recent) closed = %d, want 0", closed)
	}

	// To simulate inactivity, back-date the conversation's last_activity by
	// touching it to a timestamp older than the 10-minute timeout.
	// We do this via the store directly since there's no public API for it.
	backdated := &protocol.Conversation{
		ConversationID: conv.ConversationID,
		Participants:   conv.Participants,
		IsTask:         conv.IsTask,
		CreatedAt:      conv.CreatedAt,
		Closed:         false,
	}
	// UpdateConversation sets last_activity = now. After saving, back-date
	// last_activity via the concrete SQLiteStore so CloseStale sees it as stale.
	if err := hub.Store().UpdateConversation(ctx, backdated); err != nil {
		t.Fatalf("backdate conversation: %v", err)
	}
	sq, ok := hub.Store().(*store.SQLiteStore)
	if !ok {
		t.Fatalf("store is not *store.SQLiteStore")
	}
	staleTime := time.Now().Add(-20 * time.Minute)
	if err := sq.SetLastActivity(ctx, conv.ConversationID, staleTime); err != nil {
		t.Fatalf("SetLastActivity: %v", err)
	}

	// Now CloseStale should close this conversation.
	closed, err = hub.Conversations().CloseStale(ctx)
	if err != nil {
		t.Fatalf("CloseStale (stale): %v", err)
	}
	if closed != 1 {
		t.Errorf("CloseStale (stale) closed = %d, want 1", closed)
	}

	// Verify it's now closed.
	conv, err = hub.Conversations().Get(ctx, task.ConversationID)
	if err != nil {
		t.Fatalf("get conversation after close: %v", err)
	}
	if !conv.Closed {
		t.Error("conversation should be closed after CloseStale")
	}
	if conv.ClosedReason != protocol.ConversationCloseReasonInactivity {
		t.Errorf("close reason = %q, want %q", conv.ClosedReason, protocol.ConversationCloseReasonInactivity)
	}
}
