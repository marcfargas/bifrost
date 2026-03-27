package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// Queue manages buffering of messages when peer hubs are unreachable.
// It sits between the federation manager and the store, providing
// a higher-level API for queuing and flushing.
type Queue struct {
	store  store.Store
	logger *slog.Logger
	mu     sync.Mutex
}

// NewQueue creates a new offline message queue.
func NewQueue(s store.Store, logger *slog.Logger) *Queue {
	if logger == nil {
		logger = slog.Default()
	}
	return &Queue{store: s, logger: logger}
}

// EnqueueMessage queues a message for delivery to a peer hub.
func (q *Queue) EnqueueMessage(ctx context.Context, peerID string, msg *protocol.Message) error {
	payload, err := json.Marshal(protocol.PeerMessagePayload{Message: msg})
	if err != nil {
		return fmt.Errorf("marshal message for queue: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.message",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    "", // will be set by manager on flush
		Payload: payload,
	}

	return q.store.EnqueuePeerMessage(ctx, peerID, env)
}

// EnqueueTaskCreate queues a task creation for a peer hub.
func (q *Queue) EnqueueTaskCreate(ctx context.Context, peerID string, task *protocol.Task, attachments []*protocol.PeerAttachmentData) error {
	payload, err := json.Marshal(protocol.PeerTaskCreatePayload{
		Task:        task,
		Attachments: attachments,
	})
	if err != nil {
		return fmt.Errorf("marshal task create for queue: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.task_create",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		Payload: payload,
	}

	return q.store.EnqueuePeerMessage(ctx, peerID, env)
}

// EnqueueTaskUpdate queues a task update for a peer hub.
func (q *Queue) EnqueueTaskUpdate(ctx context.Context, peerID string, task *protocol.Task) error {
	payload, err := json.Marshal(protocol.PeerTaskUpdatePayload{Task: task})
	if err != nil {
		return fmt.Errorf("marshal task update for queue: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.task_update",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		Payload: payload,
	}

	return q.store.EnqueuePeerMessage(ctx, peerID, env)
}

// Flush dequeues all pending messages for a peer and sends them via the provided sender function.
// Returns the number of successfully sent messages and any error from dequeuing.
// On partial failure, unsent messages (including the failed one) are re-enqueued.
func (q *Queue) Flush(ctx context.Context, peerID string, sender func(ctx context.Context, env *protocol.PeerEnvelope) error) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	envelopes, err := q.store.DequeuePeerMessages(ctx, peerID)
	if err != nil {
		return 0, fmt.Errorf("dequeue: %w", err)
	}

	sent := 0
	for i, env := range envelopes {
		if err := sender(ctx, env); err != nil {
			// Re-enqueue remaining messages (including the failed one)
			for _, remaining := range envelopes[i:] {
				q.store.EnqueuePeerMessage(ctx, peerID, remaining) //nolint:errcheck
			}
			q.logger.Warn("flush interrupted, re-enqueued remaining",
				"peer_id", peerID, "sent", sent, "remaining", len(envelopes)-i)
			return sent, nil
		}
		sent++
	}

	if sent > 0 {
		q.logger.Info("flushed queued messages", "peer_id", peerID, "count", sent)
	}

	return sent, nil
}

// Depth returns the number of queued messages for a peer without removing them.
func (q *Queue) Depth(ctx context.Context, peerID string) (int, error) {
	envelopes, err := q.store.DequeuePeerMessages(ctx, peerID)
	if err != nil {
		return 0, err
	}

	// DequeuePeerMessages deletes, so re-enqueue them
	for _, env := range envelopes {
		q.store.EnqueuePeerMessage(ctx, peerID, env) //nolint:errcheck
	}

	return len(envelopes), nil
}
