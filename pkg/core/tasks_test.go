package core

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// registerAgent is a test helper that registers an agent and returns it.
func registerAgent(t *testing.T, hub *Hub, id, project string) *protocol.Agent {
	t.Helper()
	ctx := context.Background()
	a := &protocol.Agent{
		AgentID:     id,
		ProjectName: project,
		Username:    "user",
		Hostname:    "host",
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}
	if err := hub.Agents().Register(ctx, a); err != nil {
		t.Fatalf("registerAgent %s: %v", id, err)
	}
	return a
}

func TestCreateTask(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	registerAgent(t, hub, "requester-01", "requester-proj")
	registerAgent(t, hub, "assignee-01", "assignee-proj")

	task, err := hub.Tasks().CreateTask(ctx, "requester-01", "assignee-01", "Do the thing", "Details here")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if task.TaskID == "" {
		t.Error("task ID is empty")
	}
	if task.Status != protocol.TaskStatusRequested {
		t.Errorf("expected status %q, got %q", protocol.TaskStatusRequested, task.Status)
	}
	if task.Requester != "requester-01" {
		t.Errorf("expected requester %q, got %q", "requester-01", task.Requester)
	}
	if task.Assignee != "assignee-01" {
		t.Errorf("expected assignee %q, got %q", "assignee-01", task.Assignee)
	}

	// Assignee should have been notified.
	notifs := notifier.received("assignee-01")
	var found bool
	for _, n := range notifs {
		if n.Type == "task_requested" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("assignee-01 did not receive task_requested notification; got %d notifications", len(notifs))
	}
}

func TestUpdateTaskAccept(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	registerAgent(t, hub, "requester-01", "req-proj")
	registerAgent(t, hub, "assignee-01", "ass-proj")

	task, err := hub.Tasks().CreateTask(ctx, "requester-01", "assignee-01", "Task", "Desc")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	updated, err := hub.Tasks().UpdateTask(ctx, "assignee-01", task.TaskID, TaskUpdate{
		Status: protocol.TaskStatusAccepted,
	})
	if err != nil {
		t.Fatalf("UpdateTask (accept): %v", err)
	}
	if updated.Status != protocol.TaskStatusAccepted {
		t.Errorf("expected status %q, got %q", protocol.TaskStatusAccepted, updated.Status)
	}
}

func TestUpdateTaskInvalidTransition(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	registerAgent(t, hub, "requester-01", "req-proj2")
	registerAgent(t, hub, "assignee-01", "ass-proj2")

	task, err := hub.Tasks().CreateTask(ctx, "requester-01", "assignee-01", "Task", "Desc")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// requested → completed is not valid.
	_, err = hub.Tasks().UpdateTask(ctx, "assignee-01", task.TaskID, TaskUpdate{
		Status: protocol.TaskStatusCompleted,
	})
	if err == nil {
		t.Fatal("expected error for invalid transition requested→completed, got nil")
	}
}

func TestUpdateTaskReject(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	registerAgent(t, hub, "requester-01", "req-proj3")
	registerAgent(t, hub, "assignee-01", "ass-proj3")

	task, err := hub.Tasks().CreateTask(ctx, "requester-01", "assignee-01", "Task", "Desc")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	updated, err := hub.Tasks().UpdateTask(ctx, "assignee-01", task.TaskID, TaskUpdate{
		Status: protocol.TaskStatusRejected,
		Reason: "too busy",
	})
	if err != nil {
		t.Fatalf("UpdateTask (reject): %v", err)
	}
	if updated.Status != protocol.TaskStatusRejected {
		t.Errorf("expected status %q, got %q", protocol.TaskStatusRejected, updated.Status)
	}
	if updated.Reason != "too busy" {
		t.Errorf("expected reason %q, got %q", "too busy", updated.Reason)
	}
}

func TestUpdateTaskComplete(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	registerAgent(t, hub, "requester-01", "req-proj4")
	registerAgent(t, hub, "assignee-01", "ass-proj4")

	task, err := hub.Tasks().CreateTask(ctx, "requester-01", "assignee-01", "Task", "Desc")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	steps := []struct {
		caller string
		status protocol.TaskStatus
	}{
		{"assignee-01", protocol.TaskStatusAccepted},
		{"assignee-01", protocol.TaskStatusInProgress},
		{"assignee-01", protocol.TaskStatusCompleted},
	}
	for _, step := range steps {
		task, err = hub.Tasks().UpdateTask(ctx, step.caller, task.TaskID, TaskUpdate{Status: step.status})
		if err != nil {
			t.Fatalf("UpdateTask to %s: %v", step.status, err)
		}
	}

	if task.Status != protocol.TaskStatusCompleted {
		t.Errorf("expected completed, got %q", task.Status)
	}
}

func TestUpdateTaskUnauthorized(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)
	notifier := newTestNotifier()
	hub.AddNotifier(notifier)

	registerAgent(t, hub, "requester-01", "req-proj5")
	registerAgent(t, hub, "assignee-01", "ass-proj5")
	registerAgent(t, hub, "outsider-01", "out-proj5")

	task, err := hub.Tasks().CreateTask(ctx, "requester-01", "assignee-01", "Task", "Desc")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	_, err = hub.Tasks().UpdateTask(ctx, "outsider-01", task.TaskID, TaskUpdate{
		Status: protocol.TaskStatusAccepted,
	})
	if err == nil {
		t.Fatal("expected unauthorized error, got nil")
	}
}
