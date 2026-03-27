package core

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// registerDNDTestAgents registers a sender and a receiver in hub and returns them.
func registerDNDTestAgents(t *testing.T, hub *Hub) (sender, receiver *protocol.Agent) {
	t.Helper()
	ctx := context.Background()

	sender = &protocol.Agent{
		AgentID:     "dnd-sender",
		ProjectName: "dnd-sender-project",
		Username:    "sender",
		Hostname:    "host",
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}
	receiver = &protocol.Agent{
		AgentID:     "dnd-receiver",
		ProjectName: "dnd-receiver-project",
		Username:    "receiver",
		Hostname:    "host",
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}

	if err := hub.Agents().Register(ctx, sender); err != nil {
		t.Fatalf("Register sender: %v", err)
	}
	if err := hub.Agents().Register(ctx, receiver); err != nil {
		t.Fatalf("Register receiver: %v", err)
	}

	return sender, receiver
}

func TestDNDEnableQueuesMessages(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	_, receiver := registerDNDTestAgents(t, hub)

	// Enable DND for receiver.
	if err := hub.DND().Enable(ctx, receiver.AgentID, "in a meeting"); err != nil {
		t.Fatalf("Enable DND: %v", err)
	}

	// Send a normal-priority message.
	msg := &protocol.Message{
		From:     "dnd-sender",
		To:       "dnd-receiver",
		Type:     protocol.MessageTypeQuestion,
		Body:     "hello while dnd",
		Priority: protocol.PriorityNormal,
	}
	if err := hub.Messages().Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Notifier must NOT have received it.
	if notifs := notifier.received(receiver.AgentID); len(notifs) > 0 {
		t.Errorf("expected no notifications delivered, got %d", len(notifs))
	}

	// Queue count must be 1.
	count, err := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("QueuedCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected queue count 1, got %d", count)
	}
}

func TestDNDUrgentBreaksThrough(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	_, receiver := registerDNDTestAgents(t, hub)

	// Enable DND for receiver.
	if err := hub.DND().Enable(ctx, receiver.AgentID, "busy"); err != nil {
		t.Fatalf("Enable DND: %v", err)
	}

	// Send an urgent message — should break through.
	msg := &protocol.Message{
		From:     "dnd-sender",
		To:       "dnd-receiver",
		Type:     protocol.MessageTypeQuestion,
		Body:     "urgent!",
		Priority: protocol.PriorityUrgent,
	}
	if err := hub.Messages().Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Notifier must have received the urgent message.
	notifs := notifier.received(receiver.AgentID)
	var found bool
	for _, n := range notifs {
		if n.Type == "message.new" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("urgent message was not delivered through DND; got %d notifications", len(notifs))
	}

	// Queue should be empty (urgent message was not queued).
	count, err := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("QueuedCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected empty queue for urgent message, got %d", count)
	}
}

func TestDNDDisableFlushesQueue(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	_, receiver := registerDNDTestAgents(t, hub)

	// Enable DND.
	if err := hub.DND().Enable(ctx, receiver.AgentID, "focus time"); err != nil {
		t.Fatalf("Enable DND: %v", err)
	}

	// Send 3 normal messages.
	for i := range 3 {
		msg := &protocol.Message{
			From:     "dnd-sender",
			To:       "dnd-receiver",
			Type:     protocol.MessageTypeContext,
			Body:     "queued message",
			Priority: protocol.PriorityNormal,
		}
		_ = i
		if err := hub.Messages().Send(ctx, msg); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	// Confirm 3 queued.
	count, err := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("QueuedCount: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 queued messages before disable, got %d", count)
	}

	// Disable DND — should flush all 3.
	flushed, err := hub.DND().Disable(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("Disable DND: %v", err)
	}
	if flushed != 3 {
		t.Errorf("expected 3 flushed, got %d", flushed)
	}

	// Agent should be back online.
	agent, err := hub.Store().GetAgent(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.Status != protocol.AgentStatusOnline {
		t.Errorf("expected agent status online after disable, got %s", agent.Status)
	}

	// Queue must be empty.
	count, err = hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("QueuedCount after disable: %v", err)
	}
	if count != 0 {
		t.Errorf("expected queue empty after disable, got %d", count)
	}

	// Notifier should have received 3 flush notifications.
	notifs := notifier.received(receiver.AgentID)
	flushNotifs := 0
	for _, n := range notifs {
		if n.Type == "message.new" {
			flushNotifs++
		}
	}
	if flushNotifs != 3 {
		t.Errorf("expected 3 message.new notifications on flush, got %d", flushNotifs)
	}
}

func TestDNDShouldQueue(t *testing.T) {
	hub := newTestHub(t)
	dnd := hub.DND()

	dndAgent := &protocol.Agent{
		AgentID: "agent-dnd",
		Status:  protocol.AgentStatusDND,
	}
	onlineAgent := &protocol.Agent{
		AgentID: "agent-online",
		Status:  protocol.AgentStatusOnline,
	}

	normalMsg := &protocol.Message{Priority: protocol.PriorityNormal}
	urgentMsg := &protocol.Message{Priority: protocol.PriorityUrgent}

	tests := []struct {
		name  string
		agent *protocol.Agent
		msg   *protocol.Message
		want  bool
	}{
		{"online agent normal msg", onlineAgent, normalMsg, false},
		{"online agent urgent msg", onlineAgent, urgentMsg, false},
		{"dnd agent normal msg", dndAgent, normalMsg, true},
		{"dnd agent urgent msg (breaks through)", dndAgent, urgentMsg, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dnd.ShouldQueue(tc.agent, tc.msg)
			if got != tc.want {
				t.Errorf("ShouldQueue(%s, priority=%s) = %v, want %v",
					tc.agent.Status, tc.msg.Priority, got, tc.want)
			}
		})
	}
}
