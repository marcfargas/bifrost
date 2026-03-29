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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := newTestHub(t)

	notifier := newTestNotifier()
	hub.AddNotifier(notifier)
	hub.Sync().Start(ctx)

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

	// The sync engine checks DND on delivery — event is in delivery_state but
	// won't be pushed via NotifyAgent while DND is active.
	// Notifier should have received no "event.new" notifications for receiver.
	notifs := notifier.received(receiver.AgentID)
	for _, n := range notifs {
		if n.Type == "event.new" {
			t.Errorf("expected no event.new notifications during DND, got one")
		}
	}

	// QueuedCount returns pending delivery marks (conversations with undelivered events).
	count, err := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("QueuedCount: %v", err)
	}
	if count == 0 {
		t.Errorf("expected at least 1 pending delivery mark, got %d", count)
	}
}

func TestDNDUrgentBreaksThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := newTestHub(t)

	notifier := newTestNotifier()
	hub.AddNotifier(notifier)
	hub.Sync().Start(ctx)

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

	// Give the sync engine a moment to process.
	time.Sleep(50 * time.Millisecond)

	// Notifier must have received the urgent event.
	notifs := notifier.received(receiver.AgentID)
	var found bool
	for _, n := range notifs {
		if n.Type == "event.new" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("urgent message was not delivered through DND; got %d notifications", len(notifs))
	}
}

func TestDNDDisableFlushesQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := newTestHub(t)

	notifier := newTestNotifier()
	hub.AddNotifier(notifier)
	hub.Sync().Start(ctx)

	_, receiver := registerDNDTestAgents(t, hub)

	// Enable DND.
	if err := hub.DND().Enable(ctx, receiver.AgentID, "focus time"); err != nil {
		t.Fatalf("Enable DND: %v", err)
	}

	// Send 3 normal messages.
	for range 3 {
		msg := &protocol.Message{
			From:     "dnd-sender",
			To:       "dnd-receiver",
			Type:     protocol.MessageTypeContext,
			Body:     "queued message",
			Priority: protocol.PriorityNormal,
		}
		if err := hub.Messages().Send(ctx, msg); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	// Give sync engine a moment.
	time.Sleep(50 * time.Millisecond)

	// Confirm pending delivery marks exist.
	count, err := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("QueuedCount: %v", err)
	}
	if count == 0 {
		t.Errorf("expected pending delivery marks before disable, got %d", count)
	}

	// Disable DND — should flush all pending events.
	_, err = hub.DND().Disable(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("Disable DND: %v", err)
	}

	// Give flush a moment to process.
	time.Sleep(50 * time.Millisecond)

	// Agent should be back online.
	agent, err := hub.Store().GetAgent(ctx, receiver.AgentID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent.Status != protocol.AgentStatusOnline {
		t.Errorf("expected agent status online after disable, got %s", agent.Status)
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

	normalEv := &protocol.Event{Data: protocol.EventData{Priority: protocol.PriorityNormal}}
	urgentEv := &protocol.Event{Data: protocol.EventData{Priority: protocol.PriorityUrgent}}

	tests := []struct {
		name  string
		agent *protocol.Agent
		ev    *protocol.Event
		want  bool
	}{
		{"online agent normal ev", onlineAgent, normalEv, false},
		{"online agent urgent ev", onlineAgent, urgentEv, false},
		{"dnd agent normal ev", dndAgent, normalEv, true},
		{"dnd agent urgent ev (breaks through)", dndAgent, urgentEv, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dnd.ShouldQueue(tc.agent, tc.ev)
			if got != tc.want {
				t.Errorf("ShouldQueue(%s, priority=%s) = %v, want %v",
					tc.agent.Status, tc.ev.Data.Priority, got, tc.want)
			}
		})
	}
}
