package federation

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

func newTestQueue(t *testing.T) (*Queue, store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewQueue(s, slog.Default()), s
}

func TestQueueEnqueueMessage(t *testing.T) {
	q, s := newTestQueue(t)
	ctx := context.Background()

	msg := &protocol.Message{
		ID:             "m1",
		ConversationID: "c1",
		From:           "agent-a",
		To:             "agent-b",
		Type:           protocol.MessageTypeContext,
		Body:           "hello",
		Priority:       protocol.PriorityNormal,
		Timestamp:      time.Now(),
	}

	if err := q.EnqueueMessage(ctx, "peer-x", msg); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Verify it's in the store
	envs, err := s.DequeuePeerMessages(ctx, "peer-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 {
		t.Errorf("expected 1, got %d", len(envs))
	}
	if envs[0].Method != "peer.message" {
		t.Errorf("expected peer.message, got %s", envs[0].Method)
	}
}

func TestQueueEnqueueTaskCreate(t *testing.T) {
	q, s := newTestQueue(t)
	ctx := context.Background()

	task := &protocol.Task{
		TaskID:         "t1",
		ConversationID: "c1",
		Requester:      "a1",
		Assignee:       "a2",
		Title:          "Build API",
		Description:    "REST endpoints",
		Status:         protocol.TaskStatusRequested,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := q.EnqueueTaskCreate(ctx, "peer-y", task, nil); err != nil {
		t.Fatalf("enqueue task: %v", err)
	}

	envs, _ := s.DequeuePeerMessages(ctx, "peer-y")
	if len(envs) != 1 || envs[0].Method != "peer.task_create" {
		t.Error("expected 1 peer.task_create envelope")
	}
}

func TestQueueFlushSuccess(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// Enqueue 3 messages
	for i := 0; i < 3; i++ {
		msg := &protocol.Message{
			ID:             fmt.Sprintf("m%d", i),
			ConversationID: "c1",
			From:           "a",
			To:             "b",
			Type:           protocol.MessageTypeContext,
			Body:           fmt.Sprintf("msg %d", i),
			Priority:       protocol.PriorityNormal,
			Timestamp:      time.Now(),
		}
		q.EnqueueMessage(ctx, "peer-z", msg) //nolint:errcheck
	}

	// Flush with a sender that always succeeds
	var sent []*protocol.PeerEnvelope
	count, err := q.Flush(ctx, "peer-z", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		sent = append(sent, env)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("expected 3 sent, got %d", count)
	}
	if len(sent) != 3 {
		t.Errorf("expected 3 in sent slice, got %d", len(sent))
	}
}

func TestQueueFlushPartialFailure(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// Enqueue 3 messages
	for i := 0; i < 3; i++ {
		msg := &protocol.Message{
			ID:             fmt.Sprintf("pf%d", i),
			ConversationID: "c1",
			From:           "a",
			To:             "b",
			Type:           protocol.MessageTypeContext,
			Body:           fmt.Sprintf("msg %d", i),
			Priority:       protocol.PriorityNormal,
			Timestamp:      time.Now(),
		}
		q.EnqueueMessage(ctx, "peer-partial", msg) //nolint:errcheck
	}

	// Sender fails on second message
	callCount := 0
	count, err := q.Flush(ctx, "peer-partial", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		callCount++
		if callCount == 2 {
			return fmt.Errorf("network error")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 sent, got %d", count)
	}

	// Remaining 2 should be re-enqueued
	count2, err := q.Flush(ctx, "peer-partial", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count2 != 2 {
		t.Errorf("expected 2 remaining, got %d", count2)
	}
}

func TestQueueFlushEmpty(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	count, err := q.Flush(ctx, "peer-empty", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		t.Error("sender should not be called for empty queue")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

func TestQueueDepth(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// Initially empty
	depth, err := q.Depth(ctx, "peer-depth")
	if err != nil {
		t.Fatal(err)
	}
	if depth != 0 {
		t.Errorf("expected 0, got %d", depth)
	}

	// Enqueue 2 messages
	for i := 0; i < 2; i++ {
		msg := &protocol.Message{
			ID:             fmt.Sprintf("d%d", i),
			ConversationID: "c1",
			From:           "a",
			To:             "b",
			Type:           protocol.MessageTypeContext,
			Body:           "body",
			Priority:       protocol.PriorityNormal,
			Timestamp:      time.Now(),
		}
		q.EnqueueMessage(ctx, "peer-depth", msg) //nolint:errcheck
	}

	depth, err = q.Depth(ctx, "peer-depth")
	if err != nil {
		t.Fatal(err)
	}
	if depth != 2 {
		t.Errorf("expected 2, got %d", depth)
	}

	// Depth should not consume messages — flush should still see 2
	count, err := q.Flush(ctx, "peer-depth", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected flush to see 2, got %d", count)
	}
}
