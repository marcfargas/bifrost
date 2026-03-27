package integration_test

import (
	"context"
	"testing"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/test/testutil"
)

// TestChannelPubSub verifies that only subscribed agents receive messages
// published to a channel.
func TestChannelPubSub(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	alpha := testutil.TestAgent("chan-alpha")
	beta := testutil.TestAgent("chan-beta")
	gamma := testutil.TestAgent("chan-gamma") // will NOT subscribe

	for _, a := range []*protocol.Agent{alpha, beta, gamma} {
		if err := hub.Agents().Register(ctx, a); err != nil {
			t.Fatalf("register %s: %v", a.AgentID, err)
		}
	}

	// alpha and beta subscribe to the channel; gamma does not.
	if err := hub.Channels().Subscribe(ctx, alpha.AgentID, "channel:general"); err != nil {
		t.Fatalf("alpha subscribe: %v", err)
	}
	if err := hub.Channels().Subscribe(ctx, beta.AgentID, "channel:general"); err != nil {
		t.Fatalf("beta subscribe: %v", err)
	}

	// Clear registration noise before the publish.
	notifier.Clear()

	// alpha publishes a message to the channel.
	msg := &protocol.Message{
		From:     alpha.AgentID,
		To:       "channel:general",
		Type:     protocol.MessageTypeContext,
		Body:     "Hello channel",
		Priority: protocol.PriorityNormal,
	}
	if err := hub.Messages().Send(ctx, msg); err != nil {
		t.Fatalf("publish to channel: %v", err)
	}

	// Both alpha and beta should receive the message (channel routes to all subscribers).
	alphaMsgs := notifier.MessagesFor(alpha.AgentID)
	if len(alphaMsgs) == 0 {
		t.Errorf("alpha (subscriber) expected message, got 0")
	}

	betaMsgs := notifier.MessagesFor(beta.AgentID)
	if len(betaMsgs) == 0 {
		t.Errorf("beta (subscriber) expected message, got 0")
	}

	// gamma (non-subscriber) should receive nothing.
	gammaMsgs := notifier.MessagesFor(gamma.AgentID)
	if len(gammaMsgs) != 0 {
		t.Errorf("gamma (non-subscriber) expected 0 messages, got %d", len(gammaMsgs))
	}
}

// TestTaskSubscriberNotifications verifies that an observer who subscribes to
// a task channel receives task_updated notifications when the task is updated.
func TestTaskSubscriberNotifications(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	requester := testutil.TestAgent("tsn-requester")
	assignee := testutil.TestAgent("tsn-assignee")
	observer := testutil.TestAgent("tsn-observer")

	for _, a := range []*protocol.Agent{requester, assignee, observer} {
		if err := hub.Agents().Register(ctx, a); err != nil {
			t.Fatalf("register %s: %v", a.AgentID, err)
		}
	}

	// Create a task — requester and assignee are auto-subscribed.
	task, err := hub.Tasks().CreateTask(ctx, requester.AgentID, "tsn-assignee", "Observer test", "desc")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Observer explicitly subscribes to the task channel.
	if err := hub.Channels().Subscribe(ctx, observer.AgentID, "task:"+task.TaskID); err != nil {
		t.Fatalf("observer subscribe: %v", err)
	}

	// Clear noise from registration and task creation.
	notifier.Clear()

	// Assignee accepts the task.
	_, err = hub.Tasks().UpdateTask(ctx, assignee.AgentID, task.TaskID, core.TaskUpdate{
		Status: protocol.TaskStatusAccepted,
	})
	if err != nil {
		t.Fatalf("accept task: %v", err)
	}

	// Observer should receive a task_updated notification.
	observerNotifs := notifier.NotificationsOfType(observer.AgentID, "task_updated")
	if len(observerNotifs) == 0 {
		t.Errorf("observer expected task_updated notification, got 0")
	}

	// Requester (non-caller) should also receive it.
	requesterNotifs := notifier.NotificationsOfType(requester.AgentID, "task_updated")
	if len(requesterNotifs) == 0 {
		t.Errorf("requester expected task_updated notification, got 0")
	}
}
