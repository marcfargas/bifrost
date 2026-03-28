package integration_test

import (
	"context"
	"testing"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"github.com/marcfargas/bifrost/test/testutil"
)

// TestFullTaskLifecycle creates a task, simulates a clarification exchange,
// accepts, moves to in_progress, and completes with a summary. It verifies
// notifications at each step and that the conversation stays open after completion.
func TestFullTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	requester := testutil.TestAgent("requester")
	assignee := testutil.TestAgent("assignee")

	if err := hub.Agents().Register(ctx, requester); err != nil {
		t.Fatalf("register requester: %v", err)
	}
	if err := hub.Agents().Register(ctx, assignee); err != nil {
		t.Fatalf("register assignee: %v", err)
	}

	// Create task — assignee should receive task_requested notification.
	notifier.Clear()
	task, err := hub.Tasks().CreateTask(ctx, requester.AgentID, "assignee", "Test task", "Do something")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.Status != protocol.TaskStatusRequested {
		t.Errorf("initial status = %q, want %q", task.Status, protocol.TaskStatusRequested)
	}

	requested := notifier.NotificationsOfType(assignee.AgentID, "task_requested")
	if len(requested) != 1 {
		t.Errorf("assignee task_requested notifications = %d, want 1", len(requested))
	}

	// Simulate clarification: requester sends a message directly to the assignee
	// using the conversation ID so it's appended to the task conversation.
	clarification := &protocol.Message{
		From:           requester.AgentID,
		To:             assignee.AgentID,
		Type:           protocol.MessageTypeQuestion,
		Body:           "Can you clarify the requirements?",
		Priority:       protocol.PriorityNormal,
		ConversationID: task.ConversationID,
	}
	if err := hub.Messages().Send(ctx, clarification); err != nil {
		t.Fatalf("send clarification: %v", err)
	}

	// Assignee accepts the task.
	notifier.Clear()
	updated, err := hub.Tasks().UpdateTask(ctx, assignee.AgentID, task.TaskID, core.TaskUpdate{
		Status: protocol.TaskStatusAccepted,
	})
	if err != nil {
		t.Fatalf("accept task: %v", err)
	}
	if updated.Status != protocol.TaskStatusAccepted {
		t.Errorf("accepted status = %q, want %q", updated.Status, protocol.TaskStatusAccepted)
	}

	// Requester should receive task_updated notification; assignee (caller) should not.
	taskUpdated := notifier.NotificationsOfType(requester.AgentID, "task_updated")
	if len(taskUpdated) != 1 {
		t.Errorf("requester task_updated notifications after accept = %d, want 1", len(taskUpdated))
	}
	assigneeUpdated := notifier.NotificationsOfType(assignee.AgentID, "task_updated")
	if len(assigneeUpdated) != 0 {
		t.Errorf("assignee should not receive task_updated when they are the caller, got %d", len(assigneeUpdated))
	}

	// Move to in_progress.
	notifier.Clear()
	updated, err = hub.Tasks().UpdateTask(ctx, assignee.AgentID, task.TaskID, core.TaskUpdate{
		Status: protocol.TaskStatusInProgress,
	})
	if err != nil {
		t.Fatalf("in_progress task: %v", err)
	}
	if updated.Status != protocol.TaskStatusInProgress {
		t.Errorf("in_progress status = %q, want %q", updated.Status, protocol.TaskStatusInProgress)
	}

	inProgressNotifs := notifier.NotificationsOfType(requester.AgentID, "task_updated")
	if len(inProgressNotifs) != 1 {
		t.Errorf("requester task_updated after in_progress = %d, want 1", len(inProgressNotifs))
	}

	// Complete with summary.
	notifier.Clear()
	updated, err = hub.Tasks().UpdateTask(ctx, assignee.AgentID, task.TaskID, core.TaskUpdate{
		Status:  protocol.TaskStatusCompleted,
		Summary: "Completed successfully",
	})
	if err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if updated.Status != protocol.TaskStatusCompleted {
		t.Errorf("completed status = %q, want %q", updated.Status, protocol.TaskStatusCompleted)
	}
	if updated.Summary != "Completed successfully" {
		t.Errorf("summary = %q, want %q", updated.Summary, "Completed successfully")
	}

	completedNotifs := notifier.NotificationsOfType(requester.AgentID, "task_updated")
	if len(completedNotifs) != 1 {
		t.Errorf("requester task_updated after complete = %d, want 1", len(completedNotifs))
	}

	// Verify the conversation is still open after task completion.
	conv, err := hub.Conversations().Get(ctx, task.ConversationID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv == nil {
		t.Fatalf("conversation not found after task completion")
	}
	if conv.Closed {
		t.Errorf("conversation should still be open after task completion, got closed=true")
	}
}

// TestTaskRejectionFlow creates a task and rejects it. After rejection no
// further status transitions should be accepted.
func TestTaskRejectionFlow(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	requester := testutil.TestAgent("req")
	assignee := testutil.TestAgent("asgn")

	if err := hub.Agents().Register(ctx, requester); err != nil {
		t.Fatalf("register requester: %v", err)
	}
	if err := hub.Agents().Register(ctx, assignee); err != nil {
		t.Fatalf("register assignee: %v", err)
	}

	task, err := hub.Tasks().CreateTask(ctx, requester.AgentID, "asgn", "Reject me", "Please reject")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Reject the task.
	updated, err := hub.Tasks().UpdateTask(ctx, assignee.AgentID, task.TaskID, core.TaskUpdate{
		Status: protocol.TaskStatusRejected,
		Reason: "Out of scope",
	})
	if err != nil {
		t.Fatalf("reject task: %v", err)
	}
	if updated.Status != protocol.TaskStatusRejected {
		t.Errorf("rejected status = %q, want %q", updated.Status, protocol.TaskStatusRejected)
	}
	if updated.Reason != "Out of scope" {
		t.Errorf("reason = %q, want %q", updated.Reason, "Out of scope")
	}

	// Verify requester received task_updated notification for the rejection.
	rejectedNotifs := notifier.NotificationsOfType(requester.AgentID, "task_updated")
	if len(rejectedNotifs) == 0 {
		t.Errorf("requester expected task_updated notification on rejection, got 0")
	}

	// Attempt further transition from rejected — must fail.
	_, err = hub.Tasks().UpdateTask(ctx, assignee.AgentID, task.TaskID, core.TaskUpdate{
		Status: protocol.TaskStatusAccepted,
	})
	if err == nil {
		t.Errorf("expected error when transitioning from rejected, got nil")
	}

	_, err = hub.Tasks().UpdateTask(ctx, assignee.AgentID, task.TaskID, core.TaskUpdate{
		Status: protocol.TaskStatusInProgress,
	})
	if err == nil {
		t.Errorf("expected error when transitioning from rejected to in_progress, got nil")
	}
}

// TestTaskListByRole creates tasks between agents and verifies that list filters
// correctly by requester and assignee role.
func TestTaskListByRole(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	alpha := testutil.TestAgent("alpha")
	beta := testutil.TestAgent("beta")
	gamma := testutil.TestAgent("gamma")

	for _, a := range []*protocol.Agent{alpha, beta, gamma} {
		if err := hub.Agents().Register(ctx, a); err != nil {
			t.Fatalf("register %s: %v", a.AgentID, err)
		}
	}

	// alpha requests two tasks: one to beta, one to gamma.
	_, err := hub.Tasks().CreateTask(ctx, alpha.AgentID, "beta", "Alpha→Beta task", "desc")
	if err != nil {
		t.Fatalf("create alpha→beta task: %v", err)
	}
	_, err = hub.Tasks().CreateTask(ctx, alpha.AgentID, "gamma", "Alpha→Gamma task", "desc")
	if err != nil {
		t.Fatalf("create alpha→gamma task: %v", err)
	}
	// beta requests a task to gamma.
	_, err = hub.Tasks().CreateTask(ctx, beta.AgentID, "gamma", "Beta→Gamma task", "desc")
	if err != nil {
		t.Fatalf("create beta→gamma task: %v", err)
	}

	// List tasks where alpha is requester — should return 2.
	alphaRequested, err := hub.Tasks().ListTasks(ctx, store.TaskFilter{Requester: alpha.AgentID})
	if err != nil {
		t.Fatalf("list alpha requested: %v", err)
	}
	if len(alphaRequested) != 2 {
		t.Errorf("alpha requested tasks = %d, want 2", len(alphaRequested))
	}

	// List tasks where gamma is assignee — should return 2.
	gammaAssigned, err := hub.Tasks().ListTasks(ctx, store.TaskFilter{Assignee: gamma.AgentID})
	if err != nil {
		t.Fatalf("list gamma assigned: %v", err)
	}
	if len(gammaAssigned) != 2 {
		t.Errorf("gamma assigned tasks = %d, want 2", len(gammaAssigned))
	}

	// List tasks where beta is assignee — should return 1.
	betaAssigned, err := hub.Tasks().ListTasks(ctx, store.TaskFilter{Assignee: beta.AgentID})
	if err != nil {
		t.Fatalf("list beta assigned: %v", err)
	}
	if len(betaAssigned) != 1 {
		t.Errorf("beta assigned tasks = %d, want 1", len(betaAssigned))
	}

	// List tasks where beta is requester — should return 1.
	betaRequested, err := hub.Tasks().ListTasks(ctx, store.TaskFilter{Requester: beta.AgentID})
	if err != nil {
		t.Fatalf("list beta requested: %v", err)
	}
	if len(betaRequested) != 1 {
		t.Errorf("beta requested tasks = %d, want 1", len(betaRequested))
	}
}
