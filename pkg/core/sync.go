// Package core — sync.go
// SyncEngine is the single delivery mechanism for all events.
// It delivers to local agents via NotifyAgent and to peer hubs via
// ConversationSyncer. DND is checked on this (receiving) hub.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// ConversationSyncer is implemented by the federation manager.
// It is called by the sync engine when events must be pushed to a peer hub.
type ConversationSyncer interface {
	SyncConversation(ctx context.Context, peerID string, conv *protocol.Conversation, events []*protocol.Event) error
}

// SyncEngine delivers events from the event store to all targets registered
// in delivery_state. It is the only delivery path for both local and federated
// delivery.
type SyncEngine struct {
	store  store.Store
	hub    *Hub
	syncer ConversationSyncer // nil if federation disabled
	logger *slog.Logger

	mu     sync.Mutex
	nudges map[string]struct{} // conversation IDs pending a sync pass
	wakeup chan struct{}
}

// NewSyncEngine creates a sync engine. Call Start to begin processing.
func NewSyncEngine(s store.Store, h *Hub, logger *slog.Logger) *SyncEngine {
	if logger == nil {
		logger = slog.Default()
	}
	return &SyncEngine{
		store:  s,
		hub:    h,
		logger: logger,
		nudges: make(map[string]struct{}),
		wakeup: make(chan struct{}, 1),
	}
}

// SetSyncer sets the federation syncer. Safe to call before Start.
func (e *SyncEngine) SetSyncer(s ConversationSyncer) {
	e.mu.Lock()
	e.syncer = s
	e.mu.Unlock()
}

// Start runs the sync engine until ctx is cancelled.
func (e *SyncEngine) Start(ctx context.Context) {
	go e.loop(ctx)
}

// Nudge schedules a sync pass for the given conversation.
// Non-blocking: if a pass is already scheduled, the call is a no-op.
func (e *SyncEngine) Nudge(conversationID string) {
	e.mu.Lock()
	e.nudges[conversationID] = struct{}{}
	e.mu.Unlock()
	select {
	case e.wakeup <- struct{}{}:
	default:
	}
}

func (e *SyncEngine) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wakeup:
			e.mu.Lock()
			pending := make([]string, 0, len(e.nudges))
			for id := range e.nudges {
				pending = append(pending, id)
			}
			e.nudges = make(map[string]struct{})
			e.mu.Unlock()

			for _, convID := range pending {
				if err := e.syncConversation(ctx, convID); err != nil {
					e.logger.Error("sync conversation failed", "conversation_id", convID, "error", err)
				}
			}
		}
	}
}

// syncConversation delivers any new events in conversationID to all registered
// targets in delivery_state.
func (e *SyncEngine) syncConversation(ctx context.Context, convID string) error {
	conv, err := e.store.GetConversation(ctx, convID)
	if err != nil {
		return fmt.Errorf("sync: get conversation: %w", err)
	}
	if conv == nil {
		return nil // conversation may have been deleted
	}

	// Collect all agent targets for this conversation.
	agentMarks, err := e.store.ListPendingDelivery(ctx, "agent", "")
	if err != nil {
		return fmt.Errorf("sync: list agent delivery: %w", err)
	}
	for _, mark := range agentMarks {
		if mark.ConversationID != convID {
			continue
		}
		if err := e.deliverToAgent(ctx, conv, mark); err != nil {
			e.logger.Warn("deliver to agent failed",
				"conversation_id", convID, "agent_id", mark.TargetID, "error", err)
		}
	}

	// Collect all peer targets for this conversation.
	peerMarks, err := e.store.ListPendingDelivery(ctx, "peer", "")
	if err != nil {
		return fmt.Errorf("sync: list peer delivery: %w", err)
	}
	for _, mark := range peerMarks {
		if mark.ConversationID != convID {
			continue
		}
		if err := e.deliverToPeer(ctx, conv, mark); err != nil {
			e.logger.Warn("deliver to peer failed",
				"conversation_id", convID, "peer_id", mark.TargetID, "error", err)
		}
	}

	return nil
}

// deliverToAgent pushes new events in a conversation to a single local agent.
func (e *SyncEngine) deliverToAgent(ctx context.Context, conv *protocol.Conversation, mark store.DeliveryMark) error {
	agentID := mark.TargetID

	agent, err := e.store.GetAgent(ctx, agentID)
	if err != nil || agent == nil {
		return nil // agent gone; leave mark in place for reconnect
	}

	events, err := e.store.ListEventsSince(ctx, conv.ConversationID, mark.LastEventID)
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}
	if len(events) == 0 {
		return nil
	}

	for _, ev := range events {
		// DND check: receiving hub decides.
		if agent.Status == protocol.AgentStatusDND {
			if ev.Data.Priority != protocol.PriorityUrgent {
				continue // skip; mark not advanced
			}
		}

		status := e.hub.NotifyAgent(agentID, Notification{
			Type:    "event.new",
			Payload: ev,
		})

		if status == protocol.DeliveryStatusDelivered {
			if err := e.store.SetDeliveryMark(ctx, "agent", agentID, conv.ConversationID, ev.ID); err != nil {
				e.logger.Warn("set delivery mark failed", "agent_id", agentID, "event_id", ev.ID)
			}
		}
		// If offline: leave mark where it is; event stays in stream for reconnect.
	}
	return nil
}

// deliverToPeer pushes new events in a conversation to a peer hub via
// ConversationSyncer.
func (e *SyncEngine) deliverToPeer(ctx context.Context, conv *protocol.Conversation, mark store.DeliveryMark) error {
	if e.syncer == nil {
		return nil
	}
	peerID := mark.TargetID

	events, err := e.store.ListEventsSince(ctx, conv.ConversationID, mark.LastEventID)
	if err != nil {
		return fmt.Errorf("list events for peer: %w", err)
	}
	if len(events) == 0 {
		return nil
	}

	if err := e.syncer.SyncConversation(ctx, peerID, conv, events); err != nil {
		// Peer unreachable — leave mark; retry on reconnect.
		e.logger.Info("peer sync failed, will retry on reconnect",
			"peer_id", peerID, "conversation_id", conv.ConversationID, "error", err)
		return nil
	}

	// Advance mark to last successfully sent event.
	last := events[len(events)-1]
	return e.store.SetDeliveryMark(ctx, "peer", peerID, conv.ConversationID, last.ID)
}

// AppendEvent is the single write path: persist an event and nudge the engine.
func (e *SyncEngine) AppendEvent(ctx context.Context, ev *protocol.Event) error {
	if ev.ID == "" {
		ev.ID = uuid.New().String()
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	if err := e.store.AppendEvent(ctx, ev); err != nil {
		return fmt.Errorf("sync: append event: %w", err)
	}
	e.Nudge(ev.ConversationID)
	return nil
}

// BatchSyncForPeer sends all pending conversations to a peer that just
// reconnected. Called by the federation manager on peer connect.
func (e *SyncEngine) BatchSyncForPeer(ctx context.Context, peerID string) error {
	marks, err := e.store.ListPendingDelivery(ctx, "peer", peerID)
	if err != nil {
		return fmt.Errorf("batch sync: list pending: %w", err)
	}
	for _, mark := range marks {
		conv, err := e.store.GetConversation(ctx, mark.ConversationID)
		if err != nil || conv == nil {
			continue
		}
		if err := e.deliverToPeer(ctx, conv, mark); err != nil {
			e.logger.Warn("batch sync delivery failed",
				"peer_id", peerID, "conversation_id", mark.ConversationID, "error", err)
		}
	}
	return nil
}

// FlushForAgent delivers all pending events to an agent that just came online
// or disabled DND.
func (e *SyncEngine) FlushForAgent(ctx context.Context, agentID string) error {
	marks, err := e.store.ListPendingDelivery(ctx, "agent", agentID)
	if err != nil {
		return fmt.Errorf("flush: list pending: %w", err)
	}
	for _, mark := range marks {
		conv, err := e.store.GetConversation(ctx, mark.ConversationID)
		if err != nil || conv == nil {
			continue
		}
		if err := e.deliverToAgent(ctx, conv, mark); err != nil {
			e.logger.Warn("flush delivery failed",
				"agent_id", agentID, "conversation_id", mark.ConversationID, "error", err)
		}
	}
	return nil
}
