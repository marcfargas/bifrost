package core_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// fakeNotifier records notifications delivered to agents.
type fakeNotifier struct {
	mu    sync.Mutex
	calls []core.Notification
}

func (f *fakeNotifier) Notify(agentID string, n core.Notification) bool {
	f.mu.Lock()
	f.calls = append(f.calls, n)
	f.mu.Unlock()
	return true
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// offlineNotifier always returns false (agent unreachable).
type offlineNotifier struct{}

func (o *offlineNotifier) Notify(_ string, _ core.Notification) bool { return false }

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

func newTestHub(t *testing.T) (*core.Hub, *store.SQLiteStore) {
	t.Helper()
	s := newTestStore(t)
	h := core.NewHub(s)
	return h, s
}

func makeConv(id string, participants []string, now time.Time) *protocol.Conversation {
	return &protocol.Conversation{
		ConversationID: id,
		Participants:   participants,
		CreatedAt:      now,
	}
}

func makeEvent(id, convID, fromAgent string, priority protocol.Priority, now time.Time) *protocol.Event {
	return &protocol.Event{
		ID:             id,
		ConversationID: convID,
		Type:           protocol.EventTypeMessage,
		FromAgent:      fromAgent,
		Data:           protocol.EventData{Body: "ping", Priority: priority},
		Timestamp:      now,
	}
}

// TestSyncEngineLocalDelivery: append event, verify agent gets notification.
func TestSyncEngineLocalDelivery(t *testing.T) {
	ctx := context.Background()
	h, s := newTestHub(t)
	notifier := &fakeNotifier{}
	h.AddNotifier(notifier)

	eng := core.NewSyncEngine(s, h, nil)
	eng.Start(ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)

	agent := &protocol.Agent{
		AgentID: "agent-recv", Status: protocol.AgentStatusOnline,
		ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0",
	}
	if err := s.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	conv := makeConv("sync-conv-1", []string{"agent-send", "agent-recv"}, now)
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}
	if err := s.InitDeliveryTargets(ctx, conv); err != nil {
		t.Fatalf("InitDeliveryTargets: %v", err)
	}

	ev := makeEvent("ev-001", "sync-conv-1", "agent-send", protocol.PriorityNormal, now)
	if err := eng.AppendEvent(ctx, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Give the sync engine time to deliver.
	time.Sleep(100 * time.Millisecond)

	if notifier.count() == 0 {
		t.Error("expected at least one notification to agent-recv, got none")
	}
}

// TestSyncEngineAdvancesMark: after delivery, mark is advanced to the event ID.
func TestSyncEngineAdvancesMark(t *testing.T) {
	ctx := context.Background()
	h, s := newTestHub(t)
	notifier := &fakeNotifier{}
	h.AddNotifier(notifier)

	eng := core.NewSyncEngine(s, h, nil)
	eng.Start(ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)

	agent := &protocol.Agent{
		AgentID: "agent-mark", Status: protocol.AgentStatusOnline,
		ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0",
	}
	if err := s.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	conv := makeConv("sync-conv-2", []string{"agent-sender", "agent-mark"}, now)
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}
	if err := s.InitDeliveryTargets(ctx, conv); err != nil {
		t.Fatalf("InitDeliveryTargets: %v", err)
	}

	ev := makeEvent("ev-mark-001", "sync-conv-2", "agent-sender", protocol.PriorityNormal, now)
	if err := eng.AppendEvent(ctx, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	mark, err := s.GetDeliveryMark(ctx, "agent", "agent-mark", "sync-conv-2")
	if err != nil {
		t.Fatalf("GetDeliveryMark: %v", err)
	}
	if mark != "ev-mark-001" {
		t.Errorf("delivery mark: want ev-mark-001, got %q", mark)
	}
}

// TestSyncEngineDNDSkips: DND agent doesn't get notification, mark not advanced.
func TestSyncEngineDNDSkips(t *testing.T) {
	ctx := context.Background()
	h, s := newTestHub(t)
	notifier := &fakeNotifier{}
	h.AddNotifier(notifier)

	eng := core.NewSyncEngine(s, h, nil)
	eng.Start(ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)

	agent := &protocol.Agent{
		AgentID: "agent-dnd", Status: protocol.AgentStatusDND,
		ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0",
	}
	if err := s.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}

	conv := makeConv("sync-conv-dnd", []string{"agent-x", "agent-dnd"}, now)
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}
	if err := s.InitDeliveryTargets(ctx, conv); err != nil {
		t.Fatalf("InitDeliveryTargets: %v", err)
	}

	// Normal-priority event — should be skipped for DND agent.
	ev := makeEvent("ev-dnd-001", "sync-conv-dnd", "agent-x", protocol.PriorityNormal, now)
	if err := eng.AppendEvent(ctx, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// The DND agent notifier should not have been called for this notification
	// (agent-x also has a delivery mark, but no notifier registered for it specifically;
	// here we verify that the mark for agent-dnd was NOT advanced).
	mark, err := s.GetDeliveryMark(ctx, "agent", "agent-dnd", "sync-conv-dnd")
	if err != nil {
		t.Fatalf("GetDeliveryMark: %v", err)
	}
	if mark != "" {
		t.Errorf("DND agent mark should not be advanced, got %q", mark)
	}

	// Now send an urgent event — it should break through DND.
	evUrgent := makeEvent("ev-dnd-002", "sync-conv-dnd", "agent-x", protocol.PriorityUrgent,
		now.Add(time.Millisecond))
	if err := eng.AppendEvent(ctx, evUrgent); err != nil {
		t.Fatalf("AppendEvent urgent: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	markAfter, err := s.GetDeliveryMark(ctx, "agent", "agent-dnd", "sync-conv-dnd")
	if err != nil {
		t.Fatalf("GetDeliveryMark after urgent: %v", err)
	}
	if markAfter != "ev-dnd-002" {
		t.Errorf("urgent event should advance mark, got %q", markAfter)
	}
}

// fakeSyncer records peer sync calls.
type fakeSyncer struct {
	mu    sync.Mutex
	calls []*protocol.Event
	err   error
}

func (f *fakeSyncer) SyncConversation(_ context.Context, _ string, _ *protocol.Conversation, events []*protocol.Event) error {
	f.mu.Lock()
	f.calls = append(f.calls, events...)
	f.mu.Unlock()
	return f.err
}

func (f *fakeSyncer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestBatchSyncForPeer: multiple pending events delivered on reconnect.
func TestBatchSyncForPeer(t *testing.T) {
	ctx := context.Background()
	h, s := newTestHub(t)
	syncer := &fakeSyncer{}

	eng := core.NewSyncEngine(s, h, nil)
	eng.SetSyncer(syncer)
	// Don't start the loop — BatchSyncForPeer is a direct call.

	now := time.Now().UTC().Truncate(time.Millisecond)

	// Create a conversation with a peer delivery target.
	conv := makeConv("sync-conv-peer", []string{"agent-local"}, now)
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}
	// Register peer delivery state with empty mark (no events delivered yet).
	if err := s.SetDeliveryMark(ctx, "peer", "peer-hub-1", "sync-conv-peer", ""); err != nil {
		t.Fatalf("SetDeliveryMark: %v", err)
	}

	// Append two events directly to store (bypassing sync loop).
	ev1 := makeEvent("ev-peer-001", "sync-conv-peer", "agent-local", protocol.PriorityNormal,
		now)
	ev2 := makeEvent("ev-peer-002", "sync-conv-peer", "agent-local", protocol.PriorityNormal,
		now.Add(time.Millisecond))
	if err := s.AppendEvent(ctx, ev1); err != nil {
		t.Fatalf("AppendEvent ev1: %v", err)
	}
	if err := s.AppendEvent(ctx, ev2); err != nil {
		t.Fatalf("AppendEvent ev2: %v", err)
	}

	// Simulate peer reconnect.
	if err := eng.BatchSyncForPeer(ctx, "peer-hub-1"); err != nil {
		t.Fatalf("BatchSyncForPeer: %v", err)
	}

	if syncer.count() != 2 {
		t.Errorf("expected 2 events delivered to peer, got %d", syncer.count())
	}

	// Mark should be advanced to the last event.
	mark, err := s.GetDeliveryMark(ctx, "peer", "peer-hub-1", "sync-conv-peer")
	if err != nil {
		t.Fatalf("GetDeliveryMark: %v", err)
	}
	if mark != "ev-peer-002" {
		t.Errorf("peer mark: want ev-peer-002, got %q", mark)
	}
}

// TestBatchSyncForPeer_SyncerError: if syncer fails, mark is not advanced.
func TestBatchSyncForPeer_SyncerError(t *testing.T) {
	ctx := context.Background()
	h, s := newTestHub(t)
	syncer := &fakeSyncer{err: errors.New("peer unreachable")}

	eng := core.NewSyncEngine(s, h, nil)
	eng.SetSyncer(syncer)

	now := time.Now().UTC().Truncate(time.Millisecond)

	conv := makeConv("sync-conv-err", []string{"agent-a"}, now)
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}
	if err := s.SetDeliveryMark(ctx, "peer", "peer-hub-err", "sync-conv-err", ""); err != nil {
		t.Fatalf("SetDeliveryMark: %v", err)
	}

	ev := makeEvent("ev-err-001", "sync-conv-err", "agent-a", protocol.PriorityNormal, now)
	if err := s.AppendEvent(ctx, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	if err := eng.BatchSyncForPeer(ctx, "peer-hub-err"); err != nil {
		t.Fatalf("BatchSyncForPeer: %v", err)
	}

	// Mark should remain empty because syncer failed.
	mark, err := s.GetDeliveryMark(ctx, "peer", "peer-hub-err", "sync-conv-err")
	if err != nil {
		t.Fatalf("GetDeliveryMark: %v", err)
	}
	if mark != "" {
		t.Errorf("mark should not advance on syncer error, got %q", mark)
	}
}
