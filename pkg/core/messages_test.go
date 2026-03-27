package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// testNotifier captures all notifications delivered through it.
type testNotifier struct {
	mu            sync.Mutex
	notifications map[string][]Notification
}

func newTestNotifier() *testNotifier {
	return &testNotifier{
		notifications: make(map[string][]Notification),
	}
}

func (n *testNotifier) Notify(agentID string, notif Notification) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notifications[agentID] = append(n.notifications[agentID], notif)
	return true
}

func (n *testNotifier) received(agentID string) []Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.notifications[agentID]
}

func TestSendMessageDelivered(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	sender := &protocol.Agent{
		AgentID:     "sender-01",
		ProjectName: "sender-project",
		Username:    "alice",
		Hostname:    "host",
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}
	receiver := &protocol.Agent{
		AgentID:     "receiver-01",
		ProjectName: "receiver-project",
		Username:    "bob",
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

	msg := &protocol.Message{
		From: "sender-01",
		To:   "receiver-01",
		Type: protocol.MessageTypeQuestion,
		Body: "hello",
	}

	if err := hub.Messages().Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// The receiver should have received a message.new notification.
	notifs := notifier.received("receiver-01")
	var found bool
	for _, n := range notifs {
		if n.Type == "message.new" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("receiver-01 did not receive message.new notification; got %d notifications", len(notifs))
	}

	// Message should have been assigned an ID.
	if msg.ID == "" {
		t.Error("message ID was not filled in by Send")
	}
}

func TestSendMessageToUnknownAgent(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	msg := &protocol.Message{
		From: "sender-01",
		To:   "nonexistent-agent",
		Type: protocol.MessageTypeQuestion,
		Body: "hello",
	}

	err := hub.Messages().Send(ctx, msg)
	if err == nil {
		t.Fatal("expected error sending to unknown agent, got nil")
	}

	if _, ok := err.(*AgentNotFoundError); !ok {
		t.Errorf("expected *AgentNotFoundError, got %T: %v", err, err)
	}
}
