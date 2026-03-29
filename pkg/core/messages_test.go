package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// waitForNotification polls notifier.received(agentID) until a notification
// of the given type appears or the timeout expires.
func waitForNotification(notifier *testNotifier, agentID, notifType string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, n := range notifier.received(agentID) {
			if n.Type == notifType {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	// Start the SyncEngine so events are delivered asynchronously.
	hub.Sync().Start(ctx)

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

	// Message should have been assigned an ID.
	if msg.ID == "" {
		t.Error("message ID was not filled in by Send")
	}

	// The receiver should receive an event.new notification via the SyncEngine
	// (delivery is asynchronous; wait up to 500ms).
	if !waitForNotification(notifier, "receiver-01", "event.new", 500*time.Millisecond) {
		t.Errorf("receiver-01 did not receive event.new notification within timeout; got: %v",
			notifier.received("receiver-01"))
	}
}

func TestSendMessageToUnknownAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

	var notFound *AgentNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("expected *AgentNotFoundError, got %T: %v", err, err)
	}
}
