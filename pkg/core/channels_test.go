package core

import (
	"context"
	"testing"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestSubscribeToChannel(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	registerAgent(t, hub, "agent-ch-01", "proj-a")
	registerAgent(t, hub, "agent-ch-02", "proj-b")

	if err := hub.Channels().Subscribe(ctx, "agent-ch-01", "channel:deploys"); err != nil {
		t.Fatalf("Subscribe agent-ch-01: %v", err)
	}
	if err := hub.Channels().Subscribe(ctx, "agent-ch-02", "channel:deploys"); err != nil {
		t.Fatalf("Subscribe agent-ch-02: %v", err)
	}

	channels, err := hub.Channels().ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}

	var found *ChannelInfo
	for i := range channels {
		if channels[i].Name == "deploys" {
			found = &channels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("channel %q not found in list; got %v", "deploys", channels)
	}
	if found.SubscriberCount != 2 {
		t.Errorf("expected 2 subscribers, got %d", found.SubscriberCount)
	}

	// Unsubscribe one agent and verify count drops.
	if err := hub.Channels().Unsubscribe(ctx, "agent-ch-01", "channel:deploys"); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	channels, err = hub.Channels().ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels after unsubscribe: %v", err)
	}
	for i := range channels {
		if channels[i].Name == "deploys" {
			if channels[i].SubscriberCount != 1 {
				t.Errorf("expected 1 subscriber after unsubscribe, got %d", channels[i].SubscriberCount)
			}
			return
		}
	}
	// Channel may be gone (0 subscribers) — that's also acceptable.
}

func TestSubscribeToTask(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	registerAgent(t, hub, "requester-t", "req-proj-t")
	registerAgent(t, hub, "assignee-t", "ass-proj-t")
	registerAgent(t, hub, "observer-t", "obs-proj-t")

	// Create task — requester and assignee are auto-subscribed by CreateTask.
	task, err := hub.Tasks().CreateTask(ctx, "requester-t", "assignee-t", "My Task", "Details")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Observer subscribes to the task target via ChannelManager.
	if err := hub.Channels().Subscribe(ctx, "observer-t", "task:"+task.TaskID); err != nil {
		t.Fatalf("Subscribe observer to task: %v", err)
	}

	// Clear captured notifications before the action under test.
	notifier.mu.Lock()
	notifier.notifications = make(map[string][]Notification)
	notifier.mu.Unlock()

	// Assignee accepts the task — should notify requester and observer.
	_, err = hub.Tasks().UpdateTask(ctx, "assignee-t", task.TaskID, TaskUpdate{
		Status: protocol.TaskStatusAccepted,
	})
	if err != nil {
		t.Fatalf("UpdateTask accept: %v", err)
	}

	// Requester should receive task_updated.
	reqNotifs := notifier.received("requester-t")
	var requesterGot bool
	for _, n := range reqNotifs {
		if n.Type == "task_updated" {
			requesterGot = true
			break
		}
	}
	if !requesterGot {
		t.Errorf("requester-t did not receive task_updated; got %v", reqNotifs)
	}

	// Observer should receive task_updated.
	obsNotifs := notifier.received("observer-t")
	var observerGot bool
	for _, n := range obsNotifs {
		if n.Type == "task_updated" {
			observerGot = true
			break
		}
	}
	if !observerGot {
		t.Errorf("observer-t did not receive task_updated; got %v", obsNotifs)
	}
}

func TestSubscribeInvalidTarget(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	registerAgent(t, hub, "agent-inv", "proj-inv")

	cases := []struct {
		target string
		desc   string
	}{
		{"invalid:target", "wrong prefix"},
		{"channel:", "empty channel name"},
		{"task:nonexistent-task-id", "nonexistent task"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			err := hub.Channels().Subscribe(ctx, "agent-inv", tc.target)
			if err == nil {
				t.Errorf("expected error for target %q (%s), got nil", tc.target, tc.desc)
			}
		})
	}
}

func TestChannelMessageRouting(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	registerAgent(t, hub, "a1", "proj-a1")
	registerAgent(t, hub, "a2", "proj-a2")
	registerAgent(t, hub, "a3", "proj-a3")

	// Subscribe a1 and a2 to the channel; a3 is not subscribed.
	if err := hub.Channels().Subscribe(ctx, "a1", "channel:deploys"); err != nil {
		t.Fatalf("Subscribe a1: %v", err)
	}
	if err := hub.Channels().Subscribe(ctx, "a2", "channel:deploys"); err != nil {
		t.Fatalf("Subscribe a2: %v", err)
	}

	// a1 publishes a message to the channel.
	msg := &protocol.Message{
		From: "a1",
		To:   "channel:deploys",
		Type: protocol.MessageTypeContext,
		Body: "deploy complete",
	}
	if err := hub.Messages().Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// a2 (subscribed, not sender) should receive message.new.
	a2Notifs := notifier.received("a2")
	var a2Got bool
	for _, n := range a2Notifs {
		if n.Type == "message.new" {
			a2Got = true
			break
		}
	}
	if !a2Got {
		t.Errorf("a2 did not receive message.new; got %v", a2Notifs)
	}

	// a1 (sender) also subscribed — routeToChannel delivers to ALL subscribers
	// including sender per current implementation. Verify a3 (not subscribed) got nothing.
	a3Notifs := notifier.received("a3")
	for _, n := range a3Notifs {
		if n.Type == "message.new" {
			t.Errorf("a3 (not subscribed) received message.new unexpectedly")
		}
	}
}
