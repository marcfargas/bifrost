package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/test/testutil"
)

// TestDNDQueuesAndFlushes enables DND for an agent, sends 3 messages (which
// should be held back by the sync engine), disables DND, and verifies all
// events are flushed as notifications.
func TestDNDQueuesAndFlushes(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)
	hub.Sync().Start(ctx)

	sender := testutil.TestAgent("dnd-sender")
	receiver := testutil.TestAgent("dnd-receiver")

	if err := hub.Agents().Register(ctx, sender); err != nil {
		t.Fatalf("register sender: %v", err)
	}
	if err := hub.Agents().Register(ctx, receiver); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

	// Enable DND for receiver.
	if err := hub.DND().Enable(ctx, receiver.AgentID, "in meeting"); err != nil {
		t.Fatalf("enable DND: %v", err)
	}

	// Clear registration noise.
	notifier.Clear()

	// Send 3 normal-priority messages to the receiver.
	for i := range 3 {
		msg := &protocol.Message{
			From:     sender.AgentID,
			To:       receiver.AgentID,
			Type:     protocol.MessageTypeContext,
			Body:     "queued message",
			Priority: protocol.PriorityNormal,
		}
		if err := hub.Messages().Send(ctx, msg); err != nil {
			t.Fatalf("send message %d: %v", i, err)
		}
	}

	// Give the sync engine a moment to attempt delivery.
	time.Sleep(50 * time.Millisecond)

	// Messages should be held back (DND), not delivered live.
	liveDeliveries := notifier.NotificationsOfType(receiver.AgentID, "event.new")
	if len(liveDeliveries) != 0 {
		t.Errorf("DND: expected 0 live deliveries while DND active, got %d", len(liveDeliveries))
	}

	// Verify pending delivery marks exist (QueuedCount uses ListPendingDelivery).
	count, err := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("queued count: %v", err)
	}
	if count == 0 {
		t.Errorf("queued count = %d, want > 0", count)
	}

	// Disable DND — should flush all pending events.
	_, err = hub.DND().Disable(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("disable DND: %v", err)
	}

	// Give flush a moment to process.
	time.Sleep(50 * time.Millisecond)

	// Agent should be back online.
	agent, err := hub.Store().GetAgent(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if agent.Status != protocol.AgentStatusOnline {
		t.Errorf("agent status = %q, want online", agent.Status)
	}
}

// TestDNDUrgentBreaksThrough enables DND with urgentBreaksThrough=true (the
// default), sends one urgent message (should be delivered) and one normal
// message (should be queued).
func TestDNDUrgentBreaksThrough(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)
	hub.Sync().Start(ctx)

	sender := testutil.TestAgent("dnd-u-sender")
	receiver := testutil.TestAgent("dnd-u-receiver")

	if err := hub.Agents().Register(ctx, sender); err != nil {
		t.Fatalf("register sender: %v", err)
	}
	if err := hub.Agents().Register(ctx, receiver); err != nil {
		t.Fatalf("register receiver: %v", err)
	}

	// Enable DND.
	if err := hub.DND().Enable(ctx, receiver.AgentID, "do not disturb"); err != nil {
		t.Fatalf("enable DND: %v", err)
	}

	notifier.Clear()

	// Send an urgent message — should break through DND.
	urgentMsg := &protocol.Message{
		From:     sender.AgentID,
		To:       receiver.AgentID,
		Type:     protocol.MessageTypeContext,
		Body:     "URGENT: server down",
		Priority: protocol.PriorityUrgent,
	}
	if err := hub.Messages().Send(ctx, urgentMsg); err != nil {
		t.Fatalf("send urgent message: %v", err)
	}

	// Send a normal message — should be queued.
	normalMsg := &protocol.Message{
		From:     sender.AgentID,
		To:       receiver.AgentID,
		Type:     protocol.MessageTypeContext,
		Body:     "just checking in",
		Priority: protocol.PriorityNormal,
	}
	if err := hub.Messages().Send(ctx, normalMsg); err != nil {
		t.Fatalf("send normal message: %v", err)
	}

	// Give the sync engine a moment to process.
	time.Sleep(50 * time.Millisecond)

	// Urgent event should have been delivered live; normal event should be held.
	liveDeliveries := notifier.NotificationsOfType(receiver.AgentID, "event.new")
	urgentDelivered := 0
	normalDelivered := 0
	for _, n := range liveDeliveries {
		if ev, ok := n.Payload.(*protocol.Event); ok {
			if ev.Data.Priority == protocol.PriorityUrgent {
				urgentDelivered++
			} else {
				normalDelivered++
			}
		}
	}
	if urgentDelivered == 0 {
		t.Errorf("urgent event was not delivered through DND")
	}
	if normalDelivered > 0 {
		t.Errorf("normal event was delivered through DND, want 0 deliveries")
	}
}
