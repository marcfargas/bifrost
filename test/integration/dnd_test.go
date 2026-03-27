package integration_test

import (
	"context"
	"testing"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/test/testutil"
)

// TestDNDQueuesAndFlushes enables DND for an agent, sends 3 messages (which
// should be queued), disables DND, and verifies all 3 messages are flushed
// as notifications.
func TestDNDQueuesAndFlushes(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

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

	// Messages should be queued, not delivered live.
	liveDeliveries := notifier.MessagesFor(receiver.AgentID)
	if len(liveDeliveries) != 0 {
		t.Errorf("DND: expected 0 live deliveries while DND active, got %d", len(liveDeliveries))
	}

	// Verify queue count.
	count, err := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("queued count: %v", err)
	}
	if count != 3 {
		t.Errorf("queued count = %d, want 3", count)
	}

	// Disable DND — should flush all 3 messages.
	flushed, err := hub.DND().Disable(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("disable DND: %v", err)
	}
	if flushed != 3 {
		t.Errorf("flushed messages = %d, want 3", flushed)
	}

	// All 3 should now be delivered via the notifier.
	deliveredMsgs := notifier.MessagesFor(receiver.AgentID)
	if len(deliveredMsgs) != 3 {
		t.Errorf("flushed deliveries = %d, want 3", len(deliveredMsgs))
	}

	// Queue should be empty now.
	count, err = hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("queued count after flush: %v", err)
	}
	if count != 0 {
		t.Errorf("queued count after flush = %d, want 0", count)
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

	// Urgent message should have been delivered live.
	liveDeliveries := notifier.MessagesFor(receiver.AgentID)
	if len(liveDeliveries) != 1 {
		t.Errorf("live deliveries = %d, want 1 (urgent only)", len(liveDeliveries))
	} else if liveDeliveries[0].Priority != protocol.PriorityUrgent {
		t.Errorf("delivered message priority = %q, want %q", liveDeliveries[0].Priority, protocol.PriorityUrgent)
	}

	// Normal message should be queued.
	count, err := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("queued count: %v", err)
	}
	if count != 1 {
		t.Errorf("queued count = %d, want 1 (normal message only)", count)
	}
}
