package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

func TestConversationNewSchema(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	conv := &protocol.Conversation{
		ConversationID: "conv-001",
		Participants:   []string{"agent-a", "agent-b"},
		IsTask:         false,
		CreatedAt:      now,
	}
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}
	got, err := s.GetConversation(ctx, "conv-001")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got == nil {
		t.Fatal("expected conversation, got nil")
	}
	if got.IsTask {
		t.Error("IsTask should be false")
	}
	if len(got.Participants) != 2 {
		t.Errorf("Participants: want 2 got %d", len(got.Participants))
	}
}

func TestTaskConversation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	conv := &protocol.Conversation{
		ConversationID: "task-conv-001",
		Participants:   []string{"requester-a", "assignee-b"},
		IsTask:         true,
		Title:          "Fix the thing",
		Assignee:       "assignee-b",
		Requester:      "requester-a",
		CreatedAt:      now,
	}
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	isTask := true
	convs, err := s.ListConversations(ctx, store.ConversationFilter{IsTask: &isTask})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(convs) != 1 {
		t.Fatalf("want 1 task conversation, got %d", len(convs))
	}
	if convs[0].Title != "Fix the thing" {
		t.Errorf("Title: want %q got %q", "Fix the thing", convs[0].Title)
	}
}

func TestAppendAndListEvents(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	conv := &protocol.Conversation{
		ConversationID: "ev-conv-001",
		Participants:   []string{"agent-a", "agent-b"},
		CreatedAt:      now,
	}
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	ev1 := &protocol.Event{
		ID: "ev-001", ConversationID: "ev-conv-001",
		Type: protocol.EventTypeMessage, FromAgent: "agent-a",
		Data:      protocol.EventData{Body: "hello", Priority: protocol.PriorityNormal},
		Timestamp: now,
	}
	ev2 := &protocol.Event{
		ID: "ev-002", ConversationID: "ev-conv-001",
		Type: protocol.EventTypeMessage, FromAgent: "agent-b",
		Data:      protocol.EventData{Body: "world", Priority: protocol.PriorityNormal},
		Timestamp: now.Add(time.Second),
	}

	if err := s.AppendEvent(ctx, ev1); err != nil {
		t.Fatalf("AppendEvent ev1: %v", err)
	}
	if err := s.AppendEvent(ctx, ev2); err != nil {
		t.Fatalf("AppendEvent ev2: %v", err)
	}

	all, err := s.ListEventsSince(ctx, "ev-conv-001", "")
	if err != nil {
		t.Fatalf("ListEventsSince all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 events, got %d", len(all))
	}

	since, err := s.ListEventsSince(ctx, "ev-conv-001", "ev-001")
	if err != nil {
		t.Fatalf("ListEventsSince since ev-001: %v", err)
	}
	if len(since) != 1 || since[0].ID != "ev-002" {
		t.Errorf("want [ev-002], got %v", since)
	}
}

func TestLatestStatusEvent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	conv := &protocol.Conversation{
		ConversationID: "status-conv", Participants: []string{"a", "b"}, IsTask: true, CreatedAt: now,
	}
	_ = s.SaveConversation(ctx, conv)

	_ = s.AppendEvent(ctx, &protocol.Event{
		ID: "s1", ConversationID: "status-conv", Type: protocol.EventTypeStatus,
		FromAgent: "b", Data: protocol.EventData{NewStatus: protocol.TaskStatusAccepted},
		Timestamp: now,
	})
	_ = s.AppendEvent(ctx, &protocol.Event{
		ID: "s2", ConversationID: "status-conv", Type: protocol.EventTypeStatus,
		FromAgent: "b", Data: protocol.EventData{NewStatus: protocol.TaskStatusInProgress},
		Timestamp: now.Add(time.Second),
	})

	ev, err := s.LatestStatusEvent(ctx, "status-conv")
	if err != nil {
		t.Fatalf("LatestStatusEvent: %v", err)
	}
	if ev == nil {
		t.Fatal("expected status event, got nil")
	}
	if ev.Data.NewStatus != protocol.TaskStatusInProgress {
		t.Errorf("NewStatus: want in_progress got %q", ev.Data.NewStatus)
	}
}

func TestDeliveryState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	conv := &protocol.Conversation{
		ConversationID: "ds-conv", Participants: []string{"agent-a", "agent-b"}, CreatedAt: now,
	}
	_ = s.SaveConversation(ctx, conv)

	if err := s.InitDeliveryTargets(ctx, conv); err != nil {
		t.Fatalf("InitDeliveryTargets: %v", err)
	}

	mark, err := s.GetDeliveryMark(ctx, "agent", "agent-a", "ds-conv")
	if err != nil {
		t.Fatalf("GetDeliveryMark: %v", err)
	}
	if mark != "" {
		t.Errorf("initial mark should be empty, got %q", mark)
	}

	if err := s.SetDeliveryMark(ctx, "agent", "agent-a", "ds-conv", "ev-007"); err != nil {
		t.Fatalf("SetDeliveryMark: %v", err)
	}
	mark, err = s.GetDeliveryMark(ctx, "agent", "agent-a", "ds-conv")
	if err != nil {
		t.Fatalf("GetDeliveryMark after set: %v", err)
	}
	if mark != "ev-007" {
		t.Errorf("mark: want ev-007 got %q", mark)
	}

	pending, err := s.ListPendingDelivery(ctx, "agent", "agent-a")
	if err != nil {
		t.Fatalf("ListPendingDelivery: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending, got %d", len(pending))
	}
	if pending[0].ConversationID != "ds-conv" {
		t.Errorf("ConversationID: want ds-conv got %q", pending[0].ConversationID)
	}
}
