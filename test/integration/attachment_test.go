// Package integration_test — attachment_test.go
// Attachments are replaced by file events in the conversation sync model.
// This file contains a placeholder test that verifies task creation still works
// without the legacy attachment table.
package integration_test

import (
	"context"
	"testing"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/test/testutil"
)

// TestTaskWithoutAttachments verifies that tasks can be created and progressed
// through the full lifecycle without the legacy attachments table.
func TestTaskWithoutAttachments(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	uploader := testutil.TestAgent("att-uploader")
	worker := testutil.TestAgent("att-worker")

	if err := hub.Agents().Register(ctx, uploader); err != nil {
		t.Fatalf("register uploader: %v", err)
	}
	if err := hub.Agents().Register(ctx, worker); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	// Create a task.
	task, err := hub.Tasks().CreateTask(ctx, uploader.AgentID, "att-worker", "File event test", "use file events now")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.TaskID == "" {
		t.Fatal("task ID is empty")
	}

	// Transition task through to completion.
	for _, update := range []core.TaskUpdate{
		{Status: protocol.TaskStatusAccepted},
		{Status: protocol.TaskStatusInProgress},
		{Status: protocol.TaskStatusCompleted, Summary: "Done"},
	} {
		task, err = hub.Tasks().UpdateTask(ctx, worker.AgentID, task.TaskID, update)
		if err != nil {
			t.Fatalf("update task to %s: %v", update.Status, err)
		}
	}

	if task.Status != protocol.TaskStatusCompleted {
		t.Errorf("final status = %q, want %q", task.Status, protocol.TaskStatusCompleted)
	}
}
