package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"github.com/marcfargas/bifrost/test/testutil"
)

// testHubWithAttachments creates a Hub with an AttachmentManager backed by a
// temp directory. The hub and store are cleaned up when the test ends.
func testHubWithAttachments(t *testing.T) *core.Hub {
	t.Helper()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "bifrost_test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("testHubWithAttachments: open sqlite: %v", err)
	}
	hub, err := core.NewHubWithConfig(s, dataDir, "10MB")
	if err != nil {
		t.Fatalf("testHubWithAttachments: new hub: %v", err)
	}
	t.Cleanup(func() {
		if err := hub.Store().Close(); err != nil {
			t.Logf("testHubWithAttachments: close store: %v", err)
		}
	})
	return hub
}

// TestTaskWithAttachments creates a task, attaches a file, retrieves the task
// to verify the attachment is listed, then retrieves the file contents and
// checks they match the original.
func TestTaskWithAttachments(t *testing.T) {
	ctx := context.Background()
	hub := testHubWithAttachments(t)

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
	task, err := hub.Tasks().CreateTask(ctx, uploader.AgentID, "att-worker", "Attachment test", "see attachment")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Write a temporary file to attach.
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "data.txt")
	fileContent := []byte("hello bifrost attachments\n")
	if err := os.WriteFile(srcPath, fileContent, 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	// Store the attachment.
	am := hub.Attachments()
	if am == nil {
		t.Fatal("AttachmentManager is nil — hub not configured with attachments")
	}
	att, err := am.Store(ctx, task.TaskID, uploader.AgentID, srcPath)
	if err != nil {
		t.Fatalf("store attachment: %v", err)
	}
	if att.AttachmentID == "" {
		t.Error("attachment ID is empty")
	}
	if att.Filename != "data.txt" {
		t.Errorf("filename = %q, want %q", att.Filename, "data.txt")
	}

	// GetTask should now list the attachment ID.
	retrieved, err := hub.Tasks().GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if len(retrieved.Attachments) != 1 {
		t.Fatalf("task attachments = %d, want 1", len(retrieved.Attachments))
	}
	if retrieved.Attachments[0] != att.AttachmentID {
		t.Errorf("attachment ID = %q, want %q", retrieved.Attachments[0], att.AttachmentID)
	}

	// Retrieve the attachment to a destination directory and verify contents.
	destDir := t.TempDir()
	destPath, err := am.Retrieve(ctx, att.AttachmentID, destDir)
	if err != nil {
		t.Fatalf("retrieve attachment: %v", err)
	}

	gotContent, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read retrieved file: %v", err)
	}
	if string(gotContent) != string(fileContent) {
		t.Errorf("retrieved content = %q, want %q", string(gotContent), string(fileContent))
	}

	// Confirm the retrieved filename matches.
	if filepath.Base(destPath) != "data.txt" {
		t.Errorf("retrieved filename = %q, want %q", filepath.Base(destPath), "data.txt")
	}

	// Verify the attachment size matches.
	if att.Size != int64(len(fileContent)) {
		t.Errorf("attachment size = %d, want %d", att.Size, len(fileContent))
	}

	// Verify the TaskID is linked correctly.
	if att.TaskID != task.TaskID {
		t.Errorf("attachment TaskID = %q, want %q", att.TaskID, task.TaskID)
	}

	// Verify UploadedBy is set.
	if att.UploadedBy != uploader.AgentID {
		t.Errorf("UploadedBy = %q, want %q", att.UploadedBy, uploader.AgentID)
	}

	// Transition task through to completion to test end-to-end.
	_, err = hub.Tasks().UpdateTask(ctx, worker.AgentID, task.TaskID, core.TaskUpdate{
		Status: protocol.TaskStatusAccepted,
	})
	if err != nil {
		t.Fatalf("accept task: %v", err)
	}
	_, err = hub.Tasks().UpdateTask(ctx, worker.AgentID, task.TaskID, core.TaskUpdate{
		Status: protocol.TaskStatusInProgress,
	})
	if err != nil {
		t.Fatalf("in_progress task: %v", err)
	}
	completed, err := hub.Tasks().UpdateTask(ctx, worker.AgentID, task.TaskID, core.TaskUpdate{
		Status:  protocol.TaskStatusCompleted,
		Summary: "Done with attachment",
	})
	if err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if completed.Status != protocol.TaskStatusCompleted {
		t.Errorf("completed status = %q, want %q", completed.Status, protocol.TaskStatusCompleted)
	}
}
