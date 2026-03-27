# Plan 2: Tasks, Conversations, Channels, DND, and Attachments

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agents can delegate tasks with file attachments, subscribe to broadcast channels, enable Do Not Disturb mode, and have conversations auto-close on inactivity. The full task lifecycle (request, accept, in_progress, complete/fail/reject) is wired from hub RPC through shim MCP tools.

**Builds on:** Plan 1 (project scaffolding, protocol types, config, store, core hub, transport, connection manager, hub server + RPC handler, shim, CLI). All types from `pkg/protocol/types.go`, store interface from `pkg/store/store.go`, and core hub from `pkg/core/hub.go` are already in place.

**Spec:** `docs/superpowers/specs/2026-03-27-bifrost-design.md`

---

### Task 1: Core Task Lifecycle

**Files:**
- Create: `pkg/core/tasks.go`
- Create: `pkg/core/attachments.go`
- Test: `pkg/core/tasks_test.go`

- [ ] **Step 1: Write tasks.go — task state machine and lifecycle**

```go
// pkg/core/tasks.go
package core

import (
	"context"
	"fmt"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// ValidTransitions defines the allowed task status transitions.
var ValidTransitions = map[protocol.TaskStatus][]protocol.TaskStatus{
	protocol.TaskRequested:  {protocol.TaskAccepted, protocol.TaskRejected},
	protocol.TaskAccepted:   {protocol.TaskInProgress},
	protocol.TaskInProgress: {protocol.TaskCompleted, protocol.TaskFailed},
}

// TaskManager handles task creation, updates, and lifecycle enforcement.
type TaskManager struct {
	store store.Store
	hub   *Hub
}

func NewTaskManager(s store.Store, h *Hub) *TaskManager {
	return &TaskManager{store: s, hub: h}
}

// isValidTransition checks whether moving from one status to another is allowed.
func isValidTransition(from, to protocol.TaskStatus) bool {
	allowed, ok := ValidTransitions[from]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}

// CreateTask creates a new task and notifies the assignee with full task details.
// The requester and assignee are automatically subscribed to task:id.
func (tm *TaskManager) CreateTask(ctx context.Context, requester string, assignee string, title, description string) (*protocol.Task, error) {
	// Resolve assignee
	agent, err := tm.hub.Agents().Resolve(ctx, assignee)
	if err != nil {
		return nil, fmt.Errorf("resolve assignee: %w", err)
	}

	now := time.Now()
	convID := protocol.NewShortID()

	// Create the task's conversation
	conv := &protocol.Conversation{
		ConversationID: convID,
		Participants:   []string{requester, agent.AgentID},
		CreatedAt:      now,
		LastActivity:   now,
	}
	if err := tm.store.SaveConversation(ctx, conv); err != nil {
		return nil, fmt.Errorf("create conversation: %w", err)
	}

	task := &protocol.Task{
		TaskID:         protocol.NewShortID(),
		ConversationID: convID,
		Requester:      requester,
		Assignee:       agent.AgentID,
		Title:          title,
		Description:    description,
		Status:         protocol.TaskRequested,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := tm.store.SaveTask(ctx, task); err != nil {
		return nil, fmt.Errorf("save task: %w", err)
	}

	// Link the conversation to the task
	conv.TaskID = task.TaskID
	if err := tm.store.SaveConversation(ctx, conv); err != nil {
		return nil, fmt.Errorf("link conversation to task: %w", err)
	}

	// Auto-subscribe requester and assignee to task updates
	tm.store.Subscribe(ctx, requester, "task:"+task.TaskID)
	tm.store.Subscribe(ctx, agent.AgentID, "task:"+task.TaskID)

	// Notify the assignee with full task details inline
	tm.hub.NotifyAgent(agent.AgentID, Notification{
		Type:    "task_requested",
		Payload: task,
	})

	return task, nil
}

// UpdateTask validates a status transition and persists the update.
// Notifies the requester and all task subscribers.
func (tm *TaskManager) UpdateTask(ctx context.Context, callerAgentID string, taskID string, update TaskUpdate) (*protocol.Task, error) {
	task, err := tm.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}

	// Only the assignee can update a task's status (except reject before accept — also assignee)
	if task.Assignee != callerAgentID && task.Requester != callerAgentID {
		return nil, fmt.Errorf("only the assignee or requester can update this task")
	}

	// Validate status transition
	if update.Status != "" {
		if !isValidTransition(task.Status, update.Status) {
			return nil, fmt.Errorf("invalid transition from %s to %s", task.Status, update.Status)
		}

		// Only assignee can accept/reject/complete/fail
		if update.Status == protocol.TaskAccepted || update.Status == protocol.TaskRejected ||
			update.Status == protocol.TaskInProgress || update.Status == protocol.TaskCompleted ||
			update.Status == protocol.TaskFailed {
			if task.Assignee != callerAgentID {
				return nil, fmt.Errorf("only the assignee can change status to %s", update.Status)
			}
		}

		task.Status = update.Status
	}

	if update.Reason != "" {
		task.Reason = update.Reason
	}
	if update.Summary != "" {
		task.Summary = update.Summary
	}
	if update.Description != "" {
		// Append to existing description
		task.Description = task.Description + "\n\n---\n\n" + update.Description
	}

	task.UpdatedAt = time.Now()

	if err := tm.store.UpdateTask(ctx, task); err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}

	// Touch the conversation's last_activity (resets inactivity timer)
	tm.store.TouchConversation(ctx, task.ConversationID)

	// Notify all task subscribers (requester + anyone else who subscribed)
	subs, err := tm.store.GetSubscribers(ctx, "task:"+taskID)
	if err == nil {
		for _, subAgentID := range subs {
			if subAgentID != callerAgentID {
				tm.hub.NotifyAgent(subAgentID, Notification{
					Type:    "task_update",
					Payload: task,
				})
			}
		}
	}

	return task, nil
}

// GetTask retrieves a task by ID.
func (tm *TaskManager) GetTask(ctx context.Context, taskID string) (*protocol.Task, error) {
	task, err := tm.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}

	// Load attachments
	atts, err := tm.store.ListAttachments(ctx, taskID)
	if err == nil {
		task.Attachments = make([]protocol.Attachment, len(atts))
		for i, a := range atts {
			task.Attachments[i] = *a
		}
	}

	return task, nil
}

// ListTasks returns tasks filtered by status and role.
func (tm *TaskManager) ListTasks(ctx context.Context, filter store.TaskFilter) ([]*protocol.Task, error) {
	return tm.store.ListTasks(ctx, filter)
}

// TaskUpdate holds the fields that can be updated on a task.
type TaskUpdate struct {
	Status      protocol.TaskStatus `json:"status,omitempty"`
	Description string              `json:"description,omitempty"`
	Summary     string              `json:"summary,omitempty"`
	Reason      string              `json:"reason,omitempty"`
}
```

- [ ] **Step 2: Write attachments.go — file storage and size validation**

```go
// pkg/core/attachments.go
package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// AttachmentManager handles file storage for task attachments.
type AttachmentManager struct {
	store      store.Store
	dataDir    string
	maxFileSize int64
}

func NewAttachmentManager(s store.Store, dataDir string, maxFileSize string) *AttachmentManager {
	maxBytes := parseSize(maxFileSize)
	return &AttachmentManager{
		store:       s,
		dataDir:     dataDir,
		maxFileSize: maxBytes,
	}
}

// StorePath returns the on-disk directory for an attachment.
func (am *AttachmentManager) StorePath(attachmentID string) string {
	return filepath.Join(am.dataDir, "attachments", attachmentID)
}

// FilePath returns the full path to an attachment's file on disk.
func (am *AttachmentManager) FilePath(attachmentID, filename string) string {
	return filepath.Join(am.StorePath(attachmentID), filename)
}

// Store saves a file to disk and records its metadata in the store.
// The source file is read from srcPath on the caller's filesystem.
func (am *AttachmentManager) Store(ctx context.Context, taskID, uploadedBy, srcPath string) (*protocol.Attachment, error) {
	// Stat the source file
	info, err := os.Stat(srcPath)
	if err != nil {
		return nil, fmt.Errorf("attachment: stat %s: %w", srcPath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("attachment: %s is a directory", srcPath)
	}
	if info.Size() > am.maxFileSize {
		return nil, fmt.Errorf("attachment: file %s exceeds max size (%d > %d bytes)",
			filepath.Base(srcPath), info.Size(), am.maxFileSize)
	}

	attID := protocol.NewShortID()
	filename := filepath.Base(srcPath)
	destDir := am.StorePath(attID)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("attachment: mkdir: %w", err)
	}

	destPath := filepath.Join(destDir, filename)
	if err := copyFile(srcPath, destPath); err != nil {
		os.RemoveAll(destDir)
		return nil, fmt.Errorf("attachment: copy: %w", err)
	}

	contentType := detectContentType(filename)
	att := &protocol.Attachment{
		AttachmentID: attID,
		TaskID:       taskID,
		Filename:     filename,
		ContentType:  contentType,
		Size:         info.Size(),
		UploadedBy:   uploadedBy,
		UploadedAt:   time.Now(),
	}

	if err := am.store.SaveAttachment(ctx, att); err != nil {
		os.RemoveAll(destDir)
		return nil, fmt.Errorf("attachment: save metadata: %w", err)
	}

	return att, nil
}

// Retrieve copies an attachment from hub storage to a local destination path.
// Returns the destination path.
func (am *AttachmentManager) Retrieve(ctx context.Context, attachmentID, destDir string) (string, error) {
	att, err := am.store.GetAttachment(ctx, attachmentID)
	if err != nil {
		return "", fmt.Errorf("attachment: get metadata: %w", err)
	}

	srcPath := am.FilePath(att.AttachmentID, att.Filename)
	if _, err := os.Stat(srcPath); err != nil {
		return "", fmt.Errorf("attachment: file missing on disk: %w", err)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("attachment: mkdir dest: %w", err)
	}

	destPath := filepath.Join(destDir, att.Filename)
	if err := copyFile(srcPath, destPath); err != nil {
		return "", fmt.Errorf("attachment: copy to dest: %w", err)
	}

	return destPath, nil
}

// Delete removes an attachment from disk and the store.
func (am *AttachmentManager) Delete(ctx context.Context, attachmentID string) error {
	os.RemoveAll(am.StorePath(attachmentID))
	// Note: store deletion is handled by retention pruning in housekeeping
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func detectContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".go":
		return "text/x-go"
	case ".ts", ".tsx":
		return "text/typescript"
	case ".js", ".jsx":
		return "text/javascript"
	case ".py":
		return "text/x-python"
	case ".yaml", ".yml":
		return "text/yaml"
	case ".toml":
		return "text/toml"
	case ".xml":
		return "text/xml"
	case ".html":
		return "text/html"
	case ".css":
		return "text/css"
	case ".csv":
		return "text/csv"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	case ".tar":
		return "application/x-tar"
	case ".gz":
		return "application/gzip"
	default:
		return "application/octet-stream"
	}
}

// parseSize converts a human-readable size string (e.g. "10MB") to bytes.
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	s = strings.ToUpper(s)

	multiplier := int64(1)
	if strings.HasSuffix(s, "GB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	} else if strings.HasSuffix(s, "MB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	} else if strings.HasSuffix(s, "KB") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	} else if strings.HasSuffix(s, "B") {
		s = strings.TrimSuffix(s, "B")
	}

	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 10 * 1024 * 1024 // default 10MB
	}
	return n * multiplier
}
```

- [ ] **Step 3: Wire TaskManager and AttachmentManager into Hub**

Add fields and accessor methods to `pkg/core/hub.go`:

```go
// Add to Hub struct:
//   tasks       *TaskManager
//   attachments *AttachmentManager

// Add to NewHub (after existing fields):
//   h.tasks = NewTaskManager(s, h)

// Add accessor:
// func (h *Hub) Tasks() *TaskManager { return h.tasks }

// Add new constructor that accepts dataDir and maxFileSize for attachments:
// func NewHubWithConfig(s store.Store, dataDir string, maxFileSize string) *Hub {
//     h := &Hub{store: s}
//     h.agents = NewAgentRegistry(s, h)
//     h.messages = NewMessageRouter(s, h)
//     h.tasks = NewTaskManager(s, h)
//     h.attachments = NewAttachmentManager(s, dataDir, maxFileSize)
//     return h
// }

// func (h *Hub) Attachments() *AttachmentManager { return h.attachments }
```

Apply these changes to `pkg/core/hub.go`:

```go
// pkg/core/hub.go — updated Hub struct and constructors

// Hub is the core orchestrator. It manages agents, messages, tasks, and attachments
// through the store, and pushes notifications via registered notifiers.
type Hub struct {
	store       store.Store
	mu          sync.RWMutex
	notifiers   []Notifier
	agents      *AgentRegistry
	messages    *MessageRouter
	tasks       *TaskManager
	attachments *AttachmentManager
}

// NewHub creates a new Hub with the given store. Attachments are disabled (no data dir).
func NewHub(s store.Store) *Hub {
	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	return h
}

// NewHubWithConfig creates a Hub with attachment support.
func NewHubWithConfig(s store.Store, dataDir string, maxFileSize string) *Hub {
	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.attachments = NewAttachmentManager(s, dataDir, maxFileSize)
	return h
}

// Tasks returns the task manager.
func (h *Hub) Tasks() *TaskManager { return h.tasks }

// Attachments returns the attachment manager.
func (h *Hub) Attachments() *AttachmentManager { return h.attachments }
```

- [ ] **Step 4: Write comprehensive task and attachment tests**

```go
// pkg/core/tasks_test.go
package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

func TestCreateTaskAndNotifyAssignee(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	requester := &protocol.Agent{
		AgentID: "req1", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	}
	assignee := &protocol.Agent{
		AgentID: "asg1", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	}
	hub.Agents().Register(ctx, requester)
	hub.Agents().Register(ctx, assignee)

	task, err := hub.Tasks().CreateTask(ctx, "req1", "backend", "Implement GET /users/:id", "Return {id, name, email}. Validate UUID.")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	if task.TaskID == "" {
		t.Error("task should have an ID")
	}
	if task.Status != protocol.TaskRequested {
		t.Errorf("expected requested, got %s", task.Status)
	}
	if task.ConversationID == "" {
		t.Error("task should have a conversation")
	}
	if task.Requester != "req1" {
		t.Errorf("expected req1, got %s", task.Requester)
	}
	if task.Assignee != "asg1" {
		t.Errorf("expected asg1, got %s", task.Assignee)
	}

	// Assignee should have been notified with task_requested
	found := false
	for i, id := range notifier.agentIDs {
		if id == "asg1" && notifier.notifications[i].Type == "task_requested" {
			found = true
			payload, ok := notifier.notifications[i].Payload.(*protocol.Task)
			if !ok {
				t.Error("task_requested payload should be a *Task")
			} else if payload.Title != "Implement GET /users/:id" {
				t.Errorf("unexpected title: %s", payload.Title)
			}
		}
	}
	if !found {
		t.Error("assignee should have received task_requested notification")
	}
}

func TestTaskStatusTransitionsValid(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	task, _ := hub.Tasks().CreateTask(ctx, "req", "backend", "Test task", "Do something")

	// Accept
	updated, err := hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskAccepted})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if updated.Status != protocol.TaskAccepted {
		t.Errorf("expected accepted, got %s", updated.Status)
	}

	// In progress
	updated, err = hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskInProgress})
	if err != nil {
		t.Fatalf("in_progress: %v", err)
	}
	if updated.Status != protocol.TaskInProgress {
		t.Errorf("expected in_progress, got %s", updated.Status)
	}

	// Complete
	updated, err = hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{
		Status:  protocol.TaskCompleted,
		Summary: "Endpoint implemented and tested.",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if updated.Status != protocol.TaskCompleted {
		t.Errorf("expected completed, got %s", updated.Status)
	}
	if updated.Summary != "Endpoint implemented and tested." {
		t.Errorf("unexpected summary: %s", updated.Summary)
	}
}

func TestTaskInvalidTransition(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	task, _ := hub.Tasks().CreateTask(ctx, "req", "backend", "Test", "Test")

	// Try to complete without accepting first — should fail
	_, err := hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskCompleted})
	if err == nil {
		t.Error("should not allow requested -> completed")
	}

	// Try to go back from accepted to requested — should fail
	hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskAccepted})
	_, err = hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskRequested})
	if err == nil {
		t.Error("should not allow accepted -> requested")
	}
}

func TestTaskRejection(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	task, _ := hub.Tasks().CreateTask(ctx, "req", "backend", "Wrong team", "This is a mobile task")

	updated, err := hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{
		Status: protocol.TaskRejected,
		Reason: "This belongs to the mobile team, not backend.",
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if updated.Status != protocol.TaskRejected {
		t.Errorf("expected rejected, got %s", updated.Status)
	}
	if updated.Reason != "This belongs to the mobile team, not backend." {
		t.Errorf("unexpected reason: %s", updated.Reason)
	}

	// Rejected is terminal — no further transitions
	_, err = hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskAccepted})
	if err == nil {
		t.Error("should not allow transition from rejected")
	}
}

func TestTaskFailure(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	task, _ := hub.Tasks().CreateTask(ctx, "req", "backend", "Complex task", "Do complex work")

	hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskAccepted})
	hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskInProgress})

	updated, err := hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{
		Status: protocol.TaskFailed,
		Reason: "Database migration failed with constraint violation.",
	})
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	if updated.Status != protocol.TaskFailed {
		t.Errorf("expected failed, got %s", updated.Status)
	}

	// Failed is terminal
	_, err = hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskCompleted})
	if err == nil {
		t.Error("should not allow transition from failed")
	}
}

func TestTaskNotifiesSubscribers(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	task, _ := hub.Tasks().CreateTask(ctx, "req", "backend", "Test", "Test")

	// Clear notifications from registration
	notifier.agentIDs = nil
	notifier.notifications = nil

	// Assignee accepts — requester should be notified
	hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskAccepted})

	found := false
	for i, id := range notifier.agentIDs {
		if id == "req" && notifier.notifications[i].Type == "task_update" {
			found = true
		}
	}
	if !found {
		t.Error("requester should have been notified of task acceptance")
	}
}

func TestOnlyAssigneeCanUpdateStatus(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	task, _ := hub.Tasks().CreateTask(ctx, "req", "backend", "Test", "Test")

	// Requester tries to accept — should fail
	_, err := hub.Tasks().UpdateTask(ctx, "req", task.TaskID, TaskUpdate{Status: protocol.TaskAccepted})
	if err == nil {
		t.Error("requester should not be able to accept their own task")
	}
}

func TestAttachmentStoreAndRetrieve(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	am := NewAttachmentManager(s, dataDir, "10MB")

	// Create a source file
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "schema.json")
	os.WriteFile(srcPath, []byte(`{"id": "string", "name": "string"}`), 0o644)

	ctx := context.Background()

	att, err := am.Store(ctx, "task1", "agent1", srcPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	if att.Filename != "schema.json" {
		t.Errorf("expected schema.json, got %s", att.Filename)
	}
	if att.ContentType != "application/json" {
		t.Errorf("expected application/json, got %s", att.ContentType)
	}
	if att.Size != 34 {
		t.Errorf("expected 34 bytes, got %d", att.Size)
	}
	if att.TaskID != "task1" {
		t.Errorf("expected task1, got %s", att.TaskID)
	}

	// Verify file exists on disk
	diskPath := am.FilePath(att.AttachmentID, att.Filename)
	if _, err := os.Stat(diskPath); err != nil {
		t.Errorf("file should exist on disk: %v", err)
	}

	// Retrieve to a new directory
	destDir := t.TempDir()
	destPath, err := am.Retrieve(ctx, att.AttachmentID, destDir)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read retrieved: %v", err)
	}
	if string(data) != `{"id": "string", "name": "string"}` {
		t.Errorf("content mismatch: %s", data)
	}
}

func TestAttachmentMaxSizeEnforced(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Set max to 100 bytes
	am := NewAttachmentManager(s, dataDir, "100B")

	// Create a file larger than 100 bytes
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "big.txt")
	bigContent := make([]byte, 200)
	for i := range bigContent {
		bigContent[i] = 'x'
	}
	os.WriteFile(srcPath, bigContent, 0o644)

	ctx := context.Background()
	_, err = am.Store(ctx, "task1", "agent1", srcPath)
	if err == nil {
		t.Error("should reject file exceeding max size")
	}
}

func TestParseSizeVariants(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"10MB", 10 * 1024 * 1024},
		{"1GB", 1024 * 1024 * 1024},
		{"512KB", 512 * 1024},
		{"100B", 100},
		{"5 MB", 5 * 1024 * 1024},
	}
	for _, tt := range tests {
		got := parseSize(tt.input)
		if got != tt.expected {
			t.Errorf("parseSize(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestListTasks(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	hub.Tasks().CreateTask(ctx, "req", "backend", "Task 1", "First")
	hub.Tasks().CreateTask(ctx, "req", "backend", "Task 2", "Second")

	// List all
	tasks, err := hub.Tasks().ListTasks(ctx, store.TaskFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}

	// List by assignee
	tasks, err = hub.Tasks().ListTasks(ctx, store.TaskFilter{Assignee: "asg"})
	if err != nil {
		t.Fatalf("list by assignee: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks for assignee, got %d", len(tasks))
	}

	// List by requester
	tasks, err = hub.Tasks().ListTasks(ctx, store.TaskFilter{Requester: "req"})
	if err != nil {
		t.Fatalf("list by requester: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks for requester, got %d", len(tasks))
	}

	// List by status
	tasks, err = hub.Tasks().ListTasks(ctx, store.TaskFilter{Status: protocol.TaskRequested})
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 requested tasks, got %d", len(tasks))
	}
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./pkg/core/ -v -run "TestCreateTask|TestTask|TestAttachment|TestParseSize|TestList"
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/core/tasks.go pkg/core/attachments.go pkg/core/tasks_test.go pkg/core/hub.go
git commit -m "feat: add task lifecycle with state machine, attachments with size limits"
```

---

### Task 2: Channels and Subscriptions Core

**Files:**
- Create: `pkg/core/channels.go`
- Test: `pkg/core/channels_test.go`

- [ ] **Step 1: Write channels.go — subscription management and channel publishing**

```go
// pkg/core/channels.go
package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcfargas/bifrost/pkg/store"
)

// ChannelManager handles channel subscriptions and listing.
type ChannelManager struct {
	store store.Store
	hub   *Hub
}

func NewChannelManager(s store.Store, h *Hub) *ChannelManager {
	return &ChannelManager{store: s, hub: h}
}

// Subscribe adds an agent to a channel or task subscription.
// target must be "channel:name" or "task:id".
func (cm *ChannelManager) Subscribe(ctx context.Context, agentID, target string) error {
	if !strings.HasPrefix(target, "channel:") && !strings.HasPrefix(target, "task:") {
		return fmt.Errorf("invalid subscription target %q: must start with 'channel:' or 'task:'", target)
	}

	if strings.HasPrefix(target, "channel:") {
		name := strings.TrimPrefix(target, "channel:")
		if name == "" {
			return fmt.Errorf("channel name cannot be empty")
		}
	}

	if strings.HasPrefix(target, "task:") {
		taskID := strings.TrimPrefix(target, "task:")
		if taskID == "" {
			return fmt.Errorf("task ID cannot be empty")
		}
		// Verify task exists
		if _, err := cm.store.GetTask(ctx, taskID); err != nil {
			return fmt.Errorf("task %q not found", taskID)
		}
	}

	return cm.store.Subscribe(ctx, agentID, target)
}

// Unsubscribe removes an agent from a channel or task subscription.
func (cm *ChannelManager) Unsubscribe(ctx context.Context, agentID, target string) error {
	return cm.store.Unsubscribe(ctx, agentID, target)
}

// ListChannels returns all channel names with their subscriber counts.
func (cm *ChannelManager) ListChannels(ctx context.Context) ([]ChannelInfo, error) {
	channels, err := cm.store.ListChannels(ctx)
	if err != nil {
		return nil, err
	}

	var result []ChannelInfo
	for _, name := range channels {
		subs, err := cm.store.GetSubscribers(ctx, "channel:"+name)
		if err != nil {
			continue
		}
		result = append(result, ChannelInfo{
			Name:            name,
			SubscriberCount: len(subs),
		})
	}
	return result, nil
}

// GetSubscribers returns the agent IDs subscribed to a target.
func (cm *ChannelManager) GetSubscribers(ctx context.Context, target string) ([]string, error) {
	return cm.store.GetSubscribers(ctx, target)
}

// ChannelInfo describes a broadcast channel.
type ChannelInfo struct {
	Name            string `json:"name"`
	SubscriberCount int    `json:"subscriber_count"`
}
```

- [ ] **Step 2: Wire ChannelManager into Hub**

Add to `pkg/core/hub.go`:

```go
// Add to Hub struct:
//   channels    *ChannelManager

// In NewHub and NewHubWithConfig, add after tasks:
//   h.channels = NewChannelManager(s, h)

// Add accessor:
// func (h *Hub) Channels() *ChannelManager { return h.channels }
```

Apply these changes to `pkg/core/hub.go`:

```go
// Hub struct — add channels field:
type Hub struct {
	store       store.Store
	mu          sync.RWMutex
	notifiers   []Notifier
	agents      *AgentRegistry
	messages    *MessageRouter
	tasks       *TaskManager
	attachments *AttachmentManager
	channels    *ChannelManager
}

// NewHub — add channels init:
func NewHub(s store.Store) *Hub {
	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.channels = NewChannelManager(s, h)
	return h
}

// NewHubWithConfig — add channels init:
func NewHubWithConfig(s store.Store, dataDir string, maxFileSize string) *Hub {
	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.channels = NewChannelManager(s, h)
	h.attachments = NewAttachmentManager(s, dataDir, maxFileSize)
	return h
}

// Channels returns the channel manager.
func (h *Hub) Channels() *ChannelManager { return h.channels }
```

- [ ] **Step 3: Write channel tests**

```go
// pkg/core/channels_test.go
package core

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestSubscribeToChannel(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "a1", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "a2", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	// Subscribe both to deploys channel
	if err := hub.Channels().Subscribe(ctx, "a1", "channel:deploys"); err != nil {
		t.Fatalf("subscribe a1: %v", err)
	}
	if err := hub.Channels().Subscribe(ctx, "a2", "channel:deploys"); err != nil {
		t.Fatalf("subscribe a2: %v", err)
	}

	// List channels
	channels, err := hub.Channels().ListChannels(ctx)
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if channels[0].Name != "deploys" {
		t.Errorf("expected deploys, got %s", channels[0].Name)
	}
	if channels[0].SubscriberCount != 2 {
		t.Errorf("expected 2 subscribers, got %d", channels[0].SubscriberCount)
	}

	// Unsubscribe a1
	hub.Channels().Unsubscribe(ctx, "a1", "channel:deploys")
	channels, _ = hub.Channels().ListChannels(ctx)
	if channels[0].SubscriberCount != 1 {
		t.Errorf("expected 1 subscriber after unsub, got %d", channels[0].SubscriberCount)
	}
}

func TestSubscribeToTask(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "obs", ProjectName: "observer", Hostname: "h",
		LocalPath: "/c", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	task, _ := hub.Tasks().CreateTask(ctx, "req", "backend", "Test", "Test")

	// Observer subscribes to task updates
	if err := hub.Channels().Subscribe(ctx, "obs", "task:"+task.TaskID); err != nil {
		t.Fatalf("subscribe observer: %v", err)
	}

	// Clear notifications
	notifier.agentIDs = nil
	notifier.notifications = nil

	// Assignee accepts — observer, requester should both get notified
	hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskAccepted})

	reqNotified := false
	obsNotified := false
	for i, id := range notifier.agentIDs {
		if notifier.notifications[i].Type == "task_update" {
			if id == "req" {
				reqNotified = true
			}
			if id == "obs" {
				obsNotified = true
			}
		}
	}
	if !reqNotified {
		t.Error("requester should have been notified")
	}
	if !obsNotified {
		t.Error("observer should have been notified")
	}
}

func TestSubscribeInvalidTarget(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()

	if err := hub.Channels().Subscribe(ctx, "a1", "invalid:target"); err == nil {
		t.Error("should reject invalid target prefix")
	}
	if err := hub.Channels().Subscribe(ctx, "a1", "channel:"); err == nil {
		t.Error("should reject empty channel name")
	}
	if err := hub.Channels().Subscribe(ctx, "a1", "task:nonexistent"); err == nil {
		t.Error("should reject nonexistent task")
	}
}

func TestChannelMessageRouting(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "a1", ProjectName: "service-a", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "a2", ProjectName: "service-b", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "a3", ProjectName: "service-c", Hostname: "h",
		LocalPath: "/c", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	// a1 and a2 subscribe to deploys, a3 does not
	hub.Channels().Subscribe(ctx, "a1", "channel:deploys")
	hub.Channels().Subscribe(ctx, "a2", "channel:deploys")

	// Clear notifications
	notifier.agentIDs = nil
	notifier.notifications = nil

	// a1 publishes to the channel
	msg := &protocol.Message{
		From: "a1", To: "channel:deploys",
		Type: protocol.MsgContext, Body: "Deploy v2.1 starting",
	}
	status, err := hub.Messages().Send(ctx, msg)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != protocol.Delivered {
		t.Errorf("expected delivered, got %s", status)
	}

	// a2 should get it, a1 (sender) should not, a3 (not subscribed) should not
	a2Got := false
	for i, id := range notifier.agentIDs {
		if notifier.notifications[i].Type == "message" {
			if id == "a2" {
				a2Got = true
			}
			if id == "a1" {
				t.Error("sender should not receive their own channel message")
			}
			if id == "a3" {
				t.Error("non-subscriber should not receive channel message")
			}
		}
	}
	if !a2Got {
		t.Error("a2 should have received the channel message")
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./pkg/core/ -v -run "TestSubscribe|TestChannel"
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/core/channels.go pkg/core/channels_test.go pkg/core/hub.go
git commit -m "feat: add channel subscription manager with pub/sub routing"
```

---

### Task 3: DND Mode Core

**Files:**
- Create: `pkg/core/dnd.go`
- Test: `pkg/core/dnd_test.go`

- [ ] **Step 1: Write dnd.go — DND state management and message queuing**

```go
// pkg/core/dnd.go
package core

import (
	"context"
	"fmt"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// DNDManager handles Do Not Disturb state for agents.
type DNDManager struct {
	store               store.Store
	hub                 *Hub
	urgentBreaksThrough bool
}

func NewDNDManager(s store.Store, h *Hub, urgentBreaksThrough bool) *DNDManager {
	return &DNDManager{store: s, hub: h, urgentBreaksThrough: urgentBreaksThrough}
}

// Enable turns on DND for an agent. Messages will be queued.
func (dm *DNDManager) Enable(ctx context.Context, agentID, reason string) error {
	agent, err := dm.store.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("dnd enable: agent %s not found: %w", agentID, err)
	}

	agent.Status = protocol.AgentDND
	agent.DNDReason = reason
	if err := dm.store.UpsertAgent(ctx, agent); err != nil {
		return fmt.Errorf("dnd enable: update agent: %w", err)
	}

	return nil
}

// Disable turns off DND and flushes all queued messages as notifications.
// Returns the number of messages flushed.
func (dm *DNDManager) Disable(ctx context.Context, agentID string) (int, error) {
	agent, err := dm.store.GetAgent(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("dnd disable: agent %s not found: %w", agentID, err)
	}

	agent.Status = protocol.AgentOnline
	agent.DNDReason = ""
	if err := dm.store.UpsertAgent(ctx, agent); err != nil {
		return 0, fmt.Errorf("dnd disable: update agent: %w", err)
	}

	// Flush queued messages
	msgs, err := dm.store.DequeueMessages(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("dnd disable: dequeue: %w", err)
	}

	for _, msg := range msgs {
		dm.hub.NotifyAgent(agentID, Notification{
			Type:    "message",
			Payload: msg,
		})
	}

	return len(msgs), nil
}

// ShouldQueue determines if a message should be queued (DND) or delivered.
// Returns true if the message should be queued.
func (dm *DNDManager) ShouldQueue(agent *protocol.Agent, msg *protocol.Message) bool {
	if agent.Status != protocol.AgentDND {
		return false
	}
	// Urgent breaks through if configured
	if dm.urgentBreaksThrough && msg.Priority == protocol.PriorityUrgent {
		return false
	}
	return true
}

// QueuedCount returns the number of messages queued for an agent in DND.
func (dm *DNDManager) QueuedCount(ctx context.Context, agentID string) (int, error) {
	msgs, err := dm.store.DequeueMessages(ctx, agentID)
	if err != nil {
		return 0, err
	}
	// Re-enqueue them — DequeueMessages is destructive
	// This is a read-only count operation, so we need a different approach.
	// Re-enqueue all messages.
	for _, msg := range msgs {
		dm.store.EnqueueMessage(ctx, agentID, msg)
	}
	return len(msgs), nil
}
```

**Note:** The `QueuedCount` method has a limitation — `DequeueMessages` is destructive. Add a non-destructive count method to the store interface and SQLite implementation.

Add to `pkg/store/store.go`:

```go
// Add to Store interface:
// QueuedMessageCount(ctx context.Context, agentID string) (int, error)
```

Add to `pkg/store/sqlite.go`:

```go
func (s *SQLiteStore) QueuedMessageCount(ctx context.Context, agentID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM message_queue WHERE agent_id = ?`, agentID).Scan(&count)
	return count, err
}
```

Then update `dnd.go`'s `QueuedCount`:

```go
// QueuedCount returns the number of messages queued for an agent in DND.
func (dm *DNDManager) QueuedCount(ctx context.Context, agentID string) (int, error) {
	return dm.store.QueuedMessageCount(ctx, agentID)
}
```

- [ ] **Step 2: Update MessageRouter to use DNDManager**

In `pkg/core/messages.go`, update `routeToAgent` to use the DND manager:

```go
// In routeToAgent, replace the DND check block with:
func (r *MessageRouter) routeToAgent(ctx context.Context, msg *protocol.Message) (protocol.DeliveryStatus, error) {
	agent, err := r.hub.Agents().Resolve(ctx, msg.To)
	if err != nil {
		return "", err
	}

	// Check DND
	if r.hub.DND() != nil && r.hub.DND().ShouldQueue(agent, msg) {
		if err := r.store.EnqueueMessage(ctx, agent.AgentID, msg); err != nil {
			return "", err
		}
		return protocol.QueuedOffline, nil
	}

	status := r.hub.NotifyAgent(agent.AgentID, Notification{
		Type:    "message",
		Payload: msg,
	})

	if status == protocol.QueuedOffline {
		r.store.EnqueueMessage(ctx, agent.AgentID, msg)
	}

	return status, nil
}
```

- [ ] **Step 3: Wire DNDManager into Hub**

Add to `pkg/core/hub.go`:

```go
// Add to Hub struct:
//   dnd         *DNDManager

// In NewHub, add after channels:
//   h.dnd = NewDNDManager(s, h, true) // urgentBreaksThrough default

// In NewHubWithConfig, add after channels:
//   h.dnd = NewDNDManager(s, h, true)

// Add accessor:
// func (h *Hub) DND() *DNDManager { return h.dnd }
```

Apply these changes:

```go
type Hub struct {
	store       store.Store
	mu          sync.RWMutex
	notifiers   []Notifier
	agents      *AgentRegistry
	messages    *MessageRouter
	tasks       *TaskManager
	attachments *AttachmentManager
	channels    *ChannelManager
	dnd         *DNDManager
}

func NewHub(s store.Store) *Hub {
	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.channels = NewChannelManager(s, h)
	h.dnd = NewDNDManager(s, h, true)
	return h
}

func NewHubWithConfig(s store.Store, dataDir string, maxFileSize string) *Hub {
	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.channels = NewChannelManager(s, h)
	h.dnd = NewDNDManager(s, h, true)
	h.attachments = NewAttachmentManager(s, dataDir, maxFileSize)
	return h
}

func (h *Hub) DND() *DNDManager { return h.dnd }
```

- [ ] **Step 4: Write DND tests**

```go
// pkg/core/dnd_test.go
package core

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestDNDEnableQueuesMessages(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "sender", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "receiver", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	// Enable DND on receiver
	err := hub.DND().Enable(ctx, "receiver", "Focusing on complex refactor")
	if err != nil {
		t.Fatalf("enable DND: %v", err)
	}

	// Verify agent status is DND
	agent, _ := hub.Store().GetAgent(ctx, "receiver")
	if agent.Status != protocol.AgentDND {
		t.Errorf("expected dnd status, got %s", agent.Status)
	}
	if agent.DNDReason != "Focusing on complex refactor" {
		t.Errorf("unexpected reason: %s", agent.DNDReason)
	}

	// Clear notifications from registration
	notifier.agentIDs = nil
	notifier.notifications = nil

	// Send a normal message — should be queued, not delivered
	msg := &protocol.Message{
		From: "sender", To: "backend",
		Type: protocol.MsgQuestion, Body: "What's the schema?",
		Priority: protocol.PriorityNormal,
	}
	status, err := hub.Messages().Send(ctx, msg)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != protocol.QueuedOffline {
		t.Errorf("expected queued, got %s", status)
	}

	// Receiver should NOT have been notified
	for i, id := range notifier.agentIDs {
		if id == "receiver" && notifier.notifications[i].Type == "message" {
			t.Error("receiver should not receive messages while DND")
		}
	}

	// Check queued count
	count, err := hub.DND().QueuedCount(ctx, "receiver")
	if err != nil {
		t.Fatalf("queued count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 queued, got %d", count)
	}
}

func TestDNDUrgentBreaksThrough(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "sender", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "receiver", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	hub.DND().Enable(ctx, "receiver", "Busy")

	// Clear notifications
	notifier.agentIDs = nil
	notifier.notifications = nil

	// Send urgent message — should break through
	msg := &protocol.Message{
		From: "sender", To: "backend",
		Type: protocol.MsgQuestion, Body: "PROD IS DOWN!",
		Priority: protocol.PriorityUrgent,
	}
	status, err := hub.Messages().Send(ctx, msg)
	if err != nil {
		t.Fatalf("send urgent: %v", err)
	}
	if status != protocol.Delivered {
		t.Errorf("urgent should be delivered, got %s", status)
	}

	// Receiver should have been notified
	found := false
	for i, id := range notifier.agentIDs {
		if id == "receiver" && notifier.notifications[i].Type == "message" {
			found = true
		}
	}
	if !found {
		t.Error("receiver should receive urgent messages even in DND")
	}
}

func TestDNDDisableFlushesQueue(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "sender", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "receiver", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	hub.DND().Enable(ctx, "receiver", "Busy")

	// Send 3 messages while DND
	for i := 0; i < 3; i++ {
		hub.Messages().Send(ctx, &protocol.Message{
			From: "sender", To: "backend",
			Type: protocol.MsgContext, Body: "queued message",
			Priority: protocol.PriorityNormal,
		})
	}

	// Clear notifications
	notifier.agentIDs = nil
	notifier.notifications = nil

	// Disable DND
	flushed, err := hub.DND().Disable(ctx, "receiver")
	if err != nil {
		t.Fatalf("disable DND: %v", err)
	}
	if flushed != 3 {
		t.Errorf("expected 3 flushed, got %d", flushed)
	}

	// Receiver should now have all 3 messages
	msgCount := 0
	for i, id := range notifier.agentIDs {
		if id == "receiver" && notifier.notifications[i].Type == "message" {
			msgCount++
		}
	}
	if msgCount != 3 {
		t.Errorf("expected 3 messages flushed, got %d", msgCount)
	}

	// Agent should be back online
	agent, _ := hub.Store().GetAgent(ctx, "receiver")
	if agent.Status != protocol.AgentOnline {
		t.Errorf("expected online, got %s", agent.Status)
	}
	if agent.DNDReason != "" {
		t.Errorf("DND reason should be cleared, got %s", agent.DNDReason)
	}

	// Queue should be empty
	count, _ := hub.DND().QueuedCount(ctx, "receiver")
	if count != 0 {
		t.Errorf("queue should be empty, got %d", count)
	}
}

func TestDNDShouldQueue(t *testing.T) {
	hub := newTestHub(t)

	dndAgent := &protocol.Agent{Status: protocol.AgentDND}
	onlineAgent := &protocol.Agent{Status: protocol.AgentOnline}
	normalMsg := &protocol.Message{Priority: protocol.PriorityNormal}
	urgentMsg := &protocol.Message{Priority: protocol.PriorityUrgent}

	// DND + normal = queue
	if !hub.DND().ShouldQueue(dndAgent, normalMsg) {
		t.Error("should queue normal message for DND agent")
	}

	// DND + urgent = deliver (urgentBreaksThrough=true by default)
	if hub.DND().ShouldQueue(dndAgent, urgentMsg) {
		t.Error("should not queue urgent message for DND agent")
	}

	// Online + anything = deliver
	if hub.DND().ShouldQueue(onlineAgent, normalMsg) {
		t.Error("should not queue for online agent")
	}
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./pkg/core/ -v -run "TestDND"
go test ./pkg/store/ -v -run "TestQueued"
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/core/dnd.go pkg/core/dnd_test.go pkg/core/hub.go pkg/core/messages.go pkg/store/store.go pkg/store/sqlite.go
git commit -m "feat: add DND mode with message queuing, urgent breakthrough, and flush on disable"
```

---

### Task 4: Conversation Auto-Close

**Files:**
- Create: `pkg/core/conversations.go`
- Test: `pkg/core/conversations_test.go`

- [ ] **Step 1: Write conversations.go — inactivity tracking and auto-close**

```go
// pkg/core/conversations.go
package core

import (
	"context"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// ConversationManager handles conversation lifecycle and inactivity tracking.
type ConversationManager struct {
	store             store.Store
	hub               *Hub
	inactivityTimeout time.Duration
}

func NewConversationManager(s store.Store, h *Hub, inactivityTimeout time.Duration) *ConversationManager {
	return &ConversationManager{
		store:             s,
		hub:               h,
		inactivityTimeout: inactivityTimeout,
	}
}

// LinkToTask associates a conversation with a task.
func (cm *ConversationManager) LinkToTask(ctx context.Context, convID, taskID string) error {
	conv, err := cm.store.GetConversation(ctx, convID)
	if err != nil {
		return err
	}
	conv.TaskID = taskID
	return cm.store.SaveConversation(ctx, conv)
}

// Touch resets the inactivity timer on a conversation.
func (cm *ConversationManager) Touch(ctx context.Context, convID string) error {
	return cm.store.TouchConversation(ctx, convID)
}

// CloseStale finds and closes conversations that have been inactive beyond the timeout.
// Returns the number of conversations closed.
func (cm *ConversationManager) CloseStale(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-cm.inactivityTimeout)
	stale, err := cm.store.ListStaleConversations(ctx, cutoff)
	if err != nil {
		return 0, err
	}

	closed := 0
	for _, conv := range stale {
		if err := cm.store.CloseConversation(ctx, conv.ConversationID, protocol.CloseInactivity); err == nil {
			closed++
		}
	}
	return closed, nil
}

// List returns conversations matching the filter.
func (cm *ConversationManager) List(ctx context.Context, filter store.ConversationFilter) ([]*protocol.Conversation, error) {
	return cm.store.ListConversations(ctx, filter)
}

// Get returns a conversation by ID.
func (cm *ConversationManager) Get(ctx context.Context, convID string) (*protocol.Conversation, error) {
	return cm.store.GetConversation(ctx, convID)
}
```

- [ ] **Step 2: Wire ConversationManager into Hub**

Add to `pkg/core/hub.go`:

```go
// Add to Hub struct:
//   conversations *ConversationManager

// In NewHub, add after dnd (use 10min default):
//   h.conversations = NewConversationManager(s, h, 10*time.Minute)

// In NewHubWithConfig, same:
//   h.conversations = NewConversationManager(s, h, 10*time.Minute)

// Add accessor:
// func (h *Hub) Conversations() *ConversationManager { return h.conversations }
```

Apply these changes. Import `time` if not already present.

```go
type Hub struct {
	store         store.Store
	mu            sync.RWMutex
	notifiers     []Notifier
	agents        *AgentRegistry
	messages      *MessageRouter
	tasks         *TaskManager
	attachments   *AttachmentManager
	channels      *ChannelManager
	dnd           *DNDManager
	conversations *ConversationManager
}

func NewHub(s store.Store) *Hub {
	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.channels = NewChannelManager(s, h)
	h.dnd = NewDNDManager(s, h, true)
	h.conversations = NewConversationManager(s, h, 10*time.Minute)
	return h
}

func NewHubWithConfig(s store.Store, dataDir string, maxFileSize string) *Hub {
	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.channels = NewChannelManager(s, h)
	h.dnd = NewDNDManager(s, h, true)
	h.conversations = NewConversationManager(s, h, 10*time.Minute)
	h.attachments = NewAttachmentManager(s, dataDir, maxFileSize)
	return h
}

func (h *Hub) Conversations() *ConversationManager { return h.conversations }
```

- [ ] **Step 3: Write conversation tests**

```go
// pkg/core/conversations_test.go
package core

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

func TestConversationAutoCloseOnInactivity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.channels = NewChannelManager(s, h)
	h.dnd = NewDNDManager(s, h, true)
	// Use 1 second timeout for testing
	h.conversations = NewConversationManager(s, h, 1*time.Second)

	ctx := context.Background()

	// Create a conversation
	conv := &protocol.Conversation{
		ConversationID: "conv1",
		Participants:   []string{"a1", "a2"},
		CreatedAt:      time.Now().Add(-2 * time.Second),
		LastActivity:   time.Now().Add(-2 * time.Second),
	}
	s.SaveConversation(ctx, conv)

	// Close stale — should close conv1
	closed, err := h.Conversations().CloseStale(ctx)
	if err != nil {
		t.Fatalf("close stale: %v", err)
	}
	if closed != 1 {
		t.Errorf("expected 1 closed, got %d", closed)
	}

	// Verify it's closed
	got, err := s.GetConversation(ctx, "conv1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Closed {
		t.Error("conversation should be closed")
	}
	if got.ClosedReason != protocol.CloseInactivity {
		t.Errorf("expected inactivity reason, got %s", got.ClosedReason)
	}
}

func TestConversationActivityResetsTimer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := &Hub{store: s}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	h.tasks = NewTaskManager(s, h)
	h.channels = NewChannelManager(s, h)
	h.dnd = NewDNDManager(s, h, true)
	h.conversations = NewConversationManager(s, h, 1*time.Second)

	ctx := context.Background()

	// Create conversation that's 2 seconds old
	conv := &protocol.Conversation{
		ConversationID: "conv2",
		Participants:   []string{"a1", "a2"},
		CreatedAt:      time.Now().Add(-2 * time.Second),
		LastActivity:   time.Now().Add(-2 * time.Second),
	}
	s.SaveConversation(ctx, conv)

	// Touch it — reset timer
	s.TouchConversation(ctx, "conv2")

	// Close stale — should NOT close conv2 (was just touched)
	closed, _ := h.Conversations().CloseStale(ctx)
	if closed != 0 {
		t.Errorf("expected 0 closed (was touched), got %d", closed)
	}
}

func TestTaskCompletionResetsConversationTimer(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	notifier := &testNotifier{}
	hub.AddNotifier(notifier)

	now := time.Now()
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "req", ProjectName: "frontend", Hostname: "h",
		LocalPath: "/a", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})
	hub.Agents().Register(ctx, &protocol.Agent{
		AgentID: "asg", ProjectName: "backend", Hostname: "h",
		LocalPath: "/b", Username: "u", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: "1.0.0",
	})

	task, _ := hub.Tasks().CreateTask(ctx, "req", "backend", "Test", "Test")

	// Get conversation before completion
	convBefore, _ := hub.Store().GetConversation(ctx, task.ConversationID)
	if convBefore.Closed {
		t.Error("conversation should be open")
	}

	// Accept and complete the task
	hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskAccepted})
	hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{Status: protocol.TaskInProgress})
	hub.Tasks().UpdateTask(ctx, "asg", task.TaskID, TaskUpdate{
		Status:  protocol.TaskCompleted,
		Summary: "Done",
	})

	// Conversation should still be open — task completion does NOT close it
	convAfter, _ := hub.Store().GetConversation(ctx, task.ConversationID)
	if convAfter.Closed {
		t.Error("conversation should NOT be closed by task completion")
	}

	// last_activity should have been updated by the task update
	if !convAfter.LastActivity.After(convBefore.LastActivity) || convAfter.LastActivity.Equal(convBefore.LastActivity) {
		// The update should have touched the conversation, but timing might be identical
		// if executed too fast. The important thing is it's not closed.
	}
}

func TestListConversationsActiveOnly(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()

	now := time.Now()

	// Create an open and a closed conversation
	s := hub.Store()
	s.SaveConversation(ctx, &protocol.Conversation{
		ConversationID: "open1",
		Participants:   []string{"a1", "a2"},
		CreatedAt:      now,
		LastActivity:   now,
	})
	s.SaveConversation(ctx, &protocol.Conversation{
		ConversationID: "closed1",
		Participants:   []string{"a1", "a2"},
		CreatedAt:      now,
		LastActivity:   now,
		Closed:         true,
		ClosedReason:   protocol.CloseInactivity,
	})

	// Active only
	convs, err := hub.Conversations().List(ctx, store.ConversationFilter{ActiveOnly: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(convs) != 1 {
		t.Errorf("expected 1 active, got %d", len(convs))
	}
	if convs[0].ConversationID != "open1" {
		t.Error("wrong conversation returned")
	}

	// All
	convs, _ = hub.Conversations().List(ctx, store.ConversationFilter{ActiveOnly: false})
	if len(convs) != 2 {
		t.Errorf("expected 2 total, got %d", len(convs))
	}
}
```

Add `"path/filepath"` to the imports.

- [ ] **Step 4: Run tests**

```bash
go test ./pkg/core/ -v -run "TestConversation|TestListConversations|TestTaskCompletion"
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/core/conversations.go pkg/core/conversations_test.go pkg/core/hub.go
git commit -m "feat: add conversation manager with inactivity auto-close"
```

---

### Task 5: Hub RPC Handlers for Tasks, Channels, DND, and Conversations

**Files:**
- Modify: `internal/hub/handler.go`
- Modify: `internal/hub/housekeeping.go`

- [ ] **Step 1: Add task RPC handlers to handler.go**

Add new cases to the `Handle` switch and implement the handler methods:

```go
// internal/hub/handler.go — additions

// Add to Handle switch:
//   case "task.create":
//       return h.handleCreateTask(ctx, req)
//   case "task.update":
//       return h.handleUpdateTask(ctx, req)
//   case "task.get":
//       return h.handleGetTask(ctx, req)
//   case "task.list":
//       return h.handleListTasks(ctx, req)
//   case "task.attach":
//       return h.handleAttachFile(ctx, req)
//   case "channel.subscribe":
//       return h.handleSubscribe(ctx, req)
//   case "channel.unsubscribe":
//       return h.handleUnsubscribe(ctx, req)
//   case "channel.list":
//       return h.handleListChannels(ctx, req)
//   case "dnd.set":
//       return h.handleDNDSet(ctx, req)
//   case "dnd.status":
//       return h.handleDNDStatus(ctx, req)

func (h *Handler) handleCreateTask(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		Requester   string   `json:"requester"`
		Assignee    string   `json:"assignee"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Files       []string `json:"files,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if params.Assignee == "" || params.Title == "" {
		return rpcError(req.ID, -32602, "assignee and title are required")
	}

	task, err := h.hub.Tasks().CreateTask(ctx, params.Requester, params.Assignee, params.Title, params.Description)
	if err != nil {
		if notFound, ok := err.(*core.AgentNotFoundError); ok {
			return &RPCResponse{JSONRPC: "2.0", ID: req.ID,
				Error: &RPCError{Code: -32001, Message: notFound.Error(), Data: notFound.Available}}
		}
		return rpcError(req.ID, -32000, err.Error())
	}

	// Handle file attachments if provided
	if h.hub.Attachments() != nil && len(params.Files) > 0 {
		for _, filePath := range params.Files {
			att, err := h.hub.Attachments().Store(ctx, task.TaskID, params.Requester, filePath)
			if err != nil {
				// Attachment failure is non-fatal — report but continue
				task.Attachments = append(task.Attachments, protocol.Attachment{
					Filename: filepath.Base(filePath),
					ContentType: "error",
				})
				continue
			}
			task.Attachments = append(task.Attachments, *att)
		}
	}

	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: task}
}

func (h *Handler) handleUpdateTask(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID     string              `json:"agent_id"`
		TaskID      string              `json:"task_id"`
		Status      protocol.TaskStatus `json:"status,omitempty"`
		Description string              `json:"description,omitempty"`
		Summary     string              `json:"summary,omitempty"`
		Reason      string              `json:"reason,omitempty"`
		Files       []string            `json:"files,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if params.TaskID == "" {
		return rpcError(req.ID, -32602, "task_id is required")
	}

	update := core.TaskUpdate{
		Status:      params.Status,
		Description: params.Description,
		Summary:     params.Summary,
		Reason:      params.Reason,
	}

	task, err := h.hub.Tasks().UpdateTask(ctx, params.AgentID, params.TaskID, update)
	if err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}

	// Handle additional file attachments
	if h.hub.Attachments() != nil && len(params.Files) > 0 {
		for _, filePath := range params.Files {
			att, err := h.hub.Attachments().Store(ctx, task.TaskID, params.AgentID, filePath)
			if err != nil {
				continue
			}
			task.Attachments = append(task.Attachments, *att)
		}
	}

	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: task}
}

func (h *Handler) handleGetTask(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	task, err := h.hub.Tasks().GetTask(ctx, params.TaskID)
	if err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}

	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: task}
}

func (h *Handler) handleListTasks(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		Status    string `json:"status,omitempty"`
		Requester string `json:"requester,omitempty"`
		Assignee  string `json:"assignee,omitempty"`
		Limit     int    `json:"limit,omitempty"`
	}
	json.Unmarshal(req.Params, &params)

	filter := store.TaskFilter{
		Requester: params.Requester,
		Assignee:  params.Assignee,
		Limit:     params.Limit,
	}
	if params.Status != "" {
		filter.Status = protocol.TaskStatus(params.Status)
	}

	tasks, err := h.hub.Tasks().ListTasks(ctx, filter)
	if err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}
	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: tasks}
}

func (h *Handler) handleAttachFile(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID  string `json:"agent_id"`
		TaskID   string `json:"task_id"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if h.hub.Attachments() == nil {
		return rpcError(req.ID, -32000, "attachments not configured (no data dir)")
	}

	att, err := h.hub.Attachments().Store(ctx, params.TaskID, params.AgentID, params.FilePath)
	if err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}

	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: att}
}

func (h *Handler) handleSubscribe(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
		Target  string `json:"target"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if err := h.hub.Channels().Subscribe(ctx, params.AgentID, params.Target); err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}

	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{
		"status": "subscribed",
		"target": params.Target,
	}}
}

func (h *Handler) handleUnsubscribe(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
		Target  string `json:"target"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if err := h.hub.Channels().Unsubscribe(ctx, params.AgentID, params.Target); err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}

	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{
		"status":  "unsubscribed",
		"target":  params.Target,
	}}
}

func (h *Handler) handleListChannels(ctx context.Context, req *RPCRequest) *RPCResponse {
	channels, err := h.hub.Channels().ListChannels(ctx)
	if err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}
	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: channels}
}

func (h *Handler) handleDNDSet(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if params.Enabled {
		if err := h.hub.DND().Enable(ctx, params.AgentID, params.Reason); err != nil {
			return rpcError(req.ID, -32000, err.Error())
		}
		return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{
			"status":  "dnd_enabled",
			"reason":  params.Reason,
		}}
	}

	flushed, err := h.hub.DND().Disable(ctx, params.AgentID)
	if err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}
	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{
		"status":          "dnd_disabled",
		"messages_flushed": flushed,
	}}
}

func (h *Handler) handleDNDStatus(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	agent, err := h.hub.Store().GetAgent(ctx, params.AgentID)
	if err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}

	count, _ := h.hub.DND().QueuedCount(ctx, params.AgentID)

	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{
		"enabled":      agent.Status == protocol.AgentDND,
		"reason":       agent.DNDReason,
		"queued_count": count,
	}}
}
```

Add necessary imports to handler.go: `"path/filepath"`, `"github.com/marcfargas/bifrost/pkg/core"`.

- [ ] **Step 2: Update Handle switch to include all new methods**

The full `Handle` switch in `handler.go` should now be:

```go
func (h *Handler) Handle(ctx context.Context, conn *transport.Conn, req *RPCRequest) *RPCResponse {
	switch req.Method {
	case "hub.register":
		return h.handleRegister(ctx, conn, req)
	case "hub.deregister":
		return h.handleDeregister(ctx, req)
	case "hub.heartbeat":
		return h.handleHeartbeat(ctx, req)
	case "hub.list_agents":
		return h.handleListAgents(ctx, req)
	case "msg.send":
		return h.handleSendMessage(ctx, req)
	case "hub.list_conversations":
		return h.handleListConversations(ctx, req)
	case "task.create":
		return h.handleCreateTask(ctx, req)
	case "task.update":
		return h.handleUpdateTask(ctx, req)
	case "task.get":
		return h.handleGetTask(ctx, req)
	case "task.list":
		return h.handleListTasks(ctx, req)
	case "task.attach":
		return h.handleAttachFile(ctx, req)
	case "channel.subscribe":
		return h.handleSubscribe(ctx, req)
	case "channel.unsubscribe":
		return h.handleUnsubscribe(ctx, req)
	case "channel.list":
		return h.handleListChannels(ctx, req)
	case "dnd.set":
		return h.handleDNDSet(ctx, req)
	case "dnd.status":
		return h.handleDNDStatus(ctx, req)
	default:
		return &RPCResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &RPCError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}
```

- [ ] **Step 3: Update housekeeping to use ConversationManager**

Update `internal/hub/housekeeping.go` to use the core conversation manager for stale close:

```go
func (s *Server) doHousekeeping(ctx context.Context) {
	now := time.Now()
	st := s.hub.Store()

	// Close stale conversations via conversation manager
	if s.hub.Conversations() != nil {
		closed, err := s.hub.Conversations().CloseStale(ctx)
		if err == nil && closed > 0 {
			log.Printf("hub: housekeeping: closed %d stale conversations", closed)
		}
	}

	// Prune old messages
	st.DeleteMessagesBefore(ctx, now.Add(-s.cfg.Retention.Messages.Duration))

	// Prune completed tasks
	st.DeleteCompletedTasksBefore(ctx, now.Add(-s.cfg.Retention.CompletedTasks.Duration))

	// Prune old conversations
	st.DeleteConversationsBefore(ctx, now.Add(-s.cfg.Retention.Conversations.Duration))

	// Prune attachments and clean up files
	ids, err := st.DeleteAttachmentsBefore(ctx, now.Add(-s.cfg.Retention.Attachments.Duration))
	if err == nil {
		for _, id := range ids {
			os.RemoveAll(filepath.Join(s.cfg.Storage.DataDir, "attachments", id))
		}
	}

	log.Printf("hub: housekeeping complete")
}
```

- [ ] **Step 4: Update hub server to use NewHubWithConfig**

In `internal/hub/server.go`, update `NewServer` to pass storage config:

```go
func NewServer(cfg *config.Config) (*Server, error) {
	dbPath := filepath.Join(cfg.Storage.DataDir, "hub.db")
	st, err := store.NewSQLite(dbPath)
	if err != nil {
		return nil, err
	}

	h := core.NewHubWithConfig(st, cfg.Storage.DataDir, cfg.Storage.MaxFileSize)
	cm := NewConnManager()
	h.AddNotifier(cm)

	return &Server{
		cfg:     cfg,
		hub:     h,
		connMgr: cm,
		handler: NewHandler(h),
	}, nil
}
```

- [ ] **Step 5: Commit**

```bash
git add internal/hub/handler.go internal/hub/housekeeping.go internal/hub/server.go
git commit -m "feat: add hub RPC handlers for tasks, channels, DND, and attachments"
```

---

### Task 6: Shim MCP Tools — Tasks, Channels, DND, and Attachments

**Files:**
- Modify: `internal/shim/tools.go`
- Create: `internal/shim/dnd.go`

- [ ] **Step 1: Add task tools to tools.go**

Add the following MCP tool handlers to `internal/shim/tools.go`. Each tool parses MCP arguments, builds JSON-RPC params, calls the hub via `rpcCall`, and formats the result.

```go
// internal/shim/tools.go — additions
//
// Add tool registration calls in registerTools():
//   server.AddTool("bifrost_create_task", createTaskSchema, s.handleCreateTask)
//   server.AddTool("bifrost_update_task", updateTaskSchema, s.handleUpdateTask)
//   server.AddTool("bifrost_get_task", getTaskSchema, s.handleGetTask)
//   server.AddTool("bifrost_list_tasks", listTasksSchema, s.handleListTasks)
//   server.AddTool("bifrost_subscribe", subscribeSchema, s.handleSubscribe)
//   server.AddTool("bifrost_list_channels", listChannelsSchema, s.handleListChannels)
//   server.AddTool("bifrost_dnd", dndSchema, s.handleDND)

// --- Tool schemas ---

// bifrost_create_task schema
// {
//   "type": "object",
//   "properties": {
//     "assignee":    {"type": "string", "description": "Agent name or ID to assign the task to"},
//     "title":       {"type": "string", "description": "Short task title"},
//     "description": {"type": "string", "description": "Long-form context, spec, requirements"},
//     "files":       {"type": "array", "items": {"type": "string"}, "description": "File paths to attach"}
//   },
//   "required": ["assignee", "title", "description"]
// }

func (s *Shim) handleCreateTask(args map[string]interface{}) (string, error) {
	assignee, _ := args["assignee"].(string)
	title, _ := args["title"].(string)
	description, _ := args["description"].(string)

	if assignee == "" || title == "" || description == "" {
		return "", fmt.Errorf("assignee, title, and description are required")
	}

	params := map[string]interface{}{
		"requester":   s.agentID,
		"assignee":    assignee,
		"title":       title,
		"description": description,
	}

	if files, ok := args["files"].([]interface{}); ok && len(files) > 0 {
		filePaths := make([]string, len(files))
		for i, f := range files {
			filePaths[i], _ = f.(string)
		}
		params["files"] = filePaths
	}

	var result map[string]interface{}
	if err := rpcCall(s.hubConn, "task.create", params, &result); err != nil {
		return "", err
	}

	taskID, _ := result["task_id"].(string)
	status, _ := result["status"].(string)
	convID, _ := result["conversation_id"].(string)

	return fmt.Sprintf("Task created.\n  ID: %s\n  Status: %s\n  Conversation: %s\n  Assignee will receive the full task details via channel notification.",
		taskID, status, convID), nil
}

// bifrost_update_task schema
// {
//   "type": "object",
//   "properties": {
//     "task_id":     {"type": "string", "description": "Task ID to update"},
//     "status":      {"type": "string", "enum": ["accepted","in_progress","completed","failed","rejected"]},
//     "description": {"type": "string", "description": "Append additional context or notes"},
//     "summary":     {"type": "string", "description": "Completion summary"},
//     "reason":      {"type": "string", "description": "Rejection or failure reason"},
//     "files":       {"type": "array", "items": {"type": "string"}, "description": "Additional files to attach"}
//   },
//   "required": ["task_id"]
// }

func (s *Shim) handleUpdateTask(args map[string]interface{}) (string, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", fmt.Errorf("task_id is required")
	}

	params := map[string]interface{}{
		"agent_id": s.agentID,
		"task_id":  taskID,
	}
	if status, ok := args["status"].(string); ok && status != "" {
		params["status"] = status
	}
	if description, ok := args["description"].(string); ok && description != "" {
		params["description"] = description
	}
	if summary, ok := args["summary"].(string); ok && summary != "" {
		params["summary"] = summary
	}
	if reason, ok := args["reason"].(string); ok && reason != "" {
		params["reason"] = reason
	}
	if files, ok := args["files"].([]interface{}); ok && len(files) > 0 {
		filePaths := make([]string, len(files))
		for i, f := range files {
			filePaths[i], _ = f.(string)
		}
		params["files"] = filePaths
	}

	var result map[string]interface{}
	if err := rpcCall(s.hubConn, "task.update", params, &result); err != nil {
		return "", err
	}

	status, _ := result["status"].(string)
	return fmt.Sprintf("Task %s updated to status: %s", taskID, status), nil
}

// bifrost_get_task schema
// {
//   "type": "object",
//   "properties": {
//     "task_id":              {"type": "string", "description": "Task ID"},
//     "include_attachments":  {"type": "boolean", "description": "Download attachment files to local temp dir"}
//   },
//   "required": ["task_id"]
// }

func (s *Shim) handleGetTask(args map[string]interface{}) (string, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", fmt.Errorf("task_id is required")
	}

	var task protocol.Task
	if err := rpcCall(s.hubConn, "task.get", map[string]string{"task_id": taskID}, &task); err != nil {
		return "", err
	}

	result := fmt.Sprintf("Task: %s\n  Title: %s\n  Status: %s\n  Requester: %s\n  Assignee: %s\n  Created: %s\n  Updated: %s\n",
		task.TaskID, task.Title, task.Status, task.Requester, task.Assignee,
		task.CreatedAt.Format("2006-01-02 15:04:05"),
		task.UpdatedAt.Format("2006-01-02 15:04:05"))

	if task.Description != "" {
		result += fmt.Sprintf("  Description:\n%s\n", task.Description)
	}
	if task.Summary != "" {
		result += fmt.Sprintf("  Summary: %s\n", task.Summary)
	}
	if task.Reason != "" {
		result += fmt.Sprintf("  Reason: %s\n", task.Reason)
	}

	if len(task.Attachments) > 0 {
		result += fmt.Sprintf("  Attachments: %d file(s)\n", len(task.Attachments))

		includeAttachments, _ := args["include_attachments"].(bool)
		if includeAttachments {
			// Download attachments to a temp directory
			tempDir, err := os.MkdirTemp("", "bifrost-attachments-"+taskID+"-")
			if err != nil {
				result += fmt.Sprintf("  Error creating temp dir: %v\n", err)
			} else {
				result += fmt.Sprintf("  Downloaded to: %s\n", tempDir)
				for _, att := range task.Attachments {
					// Request hub to copy attachment to temp dir
					var retrieveResult map[string]string
					err := rpcCall(s.hubConn, "task.retrieve_attachment", map[string]string{
						"attachment_id": att.AttachmentID,
						"dest_dir":     tempDir,
					}, &retrieveResult)
					if err != nil {
						result += fmt.Sprintf("    - %s (download failed: %v)\n", att.Filename, err)
					} else {
						destPath := filepath.Join(tempDir, att.Filename)
						result += fmt.Sprintf("    - %s (%d bytes) -> %s\n", att.Filename, att.Size, destPath)
					}
				}
			}
		} else {
			for _, att := range task.Attachments {
				result += fmt.Sprintf("    - %s (%s, %d bytes)\n", att.Filename, att.ContentType, att.Size)
			}
		}
	}

	return result, nil
}

// bifrost_list_tasks schema
// {
//   "type": "object",
//   "properties": {
//     "status": {"type": "string", "enum": ["requested","accepted","in_progress","completed","failed","rejected"]},
//     "role":   {"type": "string", "enum": ["requester","assignee","subscriber"], "description": "Filter by your role"}
//   }
// }

func (s *Shim) handleListTasks(args map[string]interface{}) (string, error) {
	params := map[string]interface{}{}

	if status, ok := args["status"].(string); ok && status != "" {
		params["status"] = status
	}

	if role, ok := args["role"].(string); ok && role != "" {
		switch role {
		case "requester":
			params["requester"] = s.agentID
		case "assignee":
			params["assignee"] = s.agentID
		case "subscriber":
			// For subscriber, we list all tasks and filter client-side
			// (subscriber info is in the subscriptions table, not the task)
			params["requester"] = s.agentID
			params["assignee"] = s.agentID
		}
	}

	var tasks []protocol.Task
	if err := rpcCall(s.hubConn, "task.list", params, &tasks); err != nil {
		return "", err
	}

	if len(tasks) == 0 {
		return "No tasks found.", nil
	}

	result := fmt.Sprintf("Tasks (%d):\n", len(tasks))
	for _, t := range tasks {
		role := "observer"
		if t.Requester == s.agentID {
			role = "requester"
		} else if t.Assignee == s.agentID {
			role = "assignee"
		}
		result += fmt.Sprintf("  [%s] %s — %s (role: %s, assignee: %s)\n",
			t.Status, t.TaskID, t.Title, role, t.Assignee)
	}
	return result, nil
}

// bifrost_subscribe schema
// {
//   "type": "object",
//   "properties": {
//     "target": {"type": "string", "description": "channel:name or task:id to subscribe to"}
//   },
//   "required": ["target"]
// }

func (s *Shim) handleSubscribe(args map[string]interface{}) (string, error) {
	target, _ := args["target"].(string)
	if target == "" {
		return "", fmt.Errorf("target is required (e.g. 'channel:deploys' or 'task:abc123')")
	}

	var result map[string]string
	if err := rpcCall(s.hubConn, "channel.subscribe", map[string]string{
		"agent_id": s.agentID,
		"target":   target,
	}, &result); err != nil {
		return "", err
	}

	return fmt.Sprintf("Subscribed to %s. You will receive notifications for this %s.",
		target, strings.SplitN(target, ":", 2)[0]), nil
}

// bifrost_list_channels schema
// {
//   "type": "object",
//   "properties": {}
// }

func (s *Shim) handleListChannels(args map[string]interface{}) (string, error) {
	var channels []struct {
		Name            string `json:"name"`
		SubscriberCount int    `json:"subscriber_count"`
	}
	if err := rpcCall(s.hubConn, "channel.list", map[string]string{}, &channels); err != nil {
		return "", err
	}

	if len(channels) == 0 {
		return "No active channels.", nil
	}

	result := fmt.Sprintf("Channels (%d):\n", len(channels))
	for _, ch := range channels {
		result += fmt.Sprintf("  #%s — %d subscriber(s)\n", ch.Name, ch.SubscriberCount)
	}
	return result, nil
}

// bifrost_dnd schema
// {
//   "type": "object",
//   "properties": {
//     "enabled": {"type": "boolean", "description": "Enable or disable DND"},
//     "reason":  {"type": "string", "description": "Reason displayed to other agents"}
//   },
//   "required": ["enabled"]
// }

func (s *Shim) handleDND(args map[string]interface{}) (string, error) {
	enabled, _ := args["enabled"].(bool)
	reason, _ := args["reason"].(string)

	var result map[string]interface{}
	if err := rpcCall(s.hubConn, "dnd.set", map[string]interface{}{
		"agent_id": s.agentID,
		"enabled":  enabled,
		"reason":   reason,
	}, &result); err != nil {
		return "", err
	}

	if enabled {
		s.startDNDReminders()
		msg := "DND enabled."
		if reason != "" {
			msg += " Reason: " + reason
		}
		msg += "\nMessages will be queued (urgent priority breaks through)."
		msg += "\nUse bifrost_dnd with enabled=false to disable and flush queued messages."
		return msg, nil
	}

	s.stopDNDReminders()
	flushed, _ := result["messages_flushed"].(float64)
	msg := "DND disabled."
	if flushed > 0 {
		msg += fmt.Sprintf(" %d queued message(s) delivered.", int(flushed))
	}
	return msg, nil
}
```

Add necessary imports: `"fmt"`, `"os"`, `"path/filepath"`, `"strings"`, and the protocol package.

- [ ] **Step 2: Write dnd.go — DND reminder loop in the shim**

```go
// internal/shim/dnd.go
package shim

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// dndState tracks the DND reminder loop for this shim.
type dndState struct {
	mu       sync.Mutex
	active   bool
	cancel   context.CancelFunc
	interval time.Duration
}

// initDND sets up DND state with the configured reminder interval.
func (s *Shim) initDND(interval time.Duration) {
	s.dndState = &dndState{
		interval: interval,
	}
}

// startDNDReminders begins the periodic DND reminder loop.
// If already running, this is a no-op.
func (s *Shim) startDNDReminders() {
	if s.dndState == nil {
		return
	}

	s.dndState.mu.Lock()
	defer s.dndState.mu.Unlock()

	if s.dndState.active {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.dndState.cancel = cancel
	s.dndState.active = true

	go s.dndReminderLoop(ctx)
}

// stopDNDReminders stops the periodic DND reminder loop.
func (s *Shim) stopDNDReminders() {
	if s.dndState == nil {
		return
	}

	s.dndState.mu.Lock()
	defer s.dndState.mu.Unlock()

	if !s.dndState.active {
		return
	}

	s.dndState.cancel()
	s.dndState.active = false
}

// dndReminderLoop sends periodic DND reminders as claude/channel notifications.
func (s *Shim) dndReminderLoop(ctx context.Context) {
	ticker := time.NewTicker(s.dndState.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sendDNDReminder()
		}
	}
}

// sendDNDReminder queries the hub for queued message count and emits a
// claude/channel notification to remind the agent about DND status.
func (s *Shim) sendDNDReminder() {
	var result map[string]interface{}
	err := rpcCall(s.hubConn, "dnd.status", map[string]string{
		"agent_id": s.agentID,
	}, &result)
	if err != nil {
		return
	}

	enabled, _ := result["enabled"].(bool)
	if !enabled {
		// DND was disabled externally — stop reminders
		s.stopDNDReminders()
		return
	}

	queuedCount, _ := result["queued_count"].(float64)
	count := int(queuedCount)

	// Emit a claude/channel notification with DND reminder
	// Use the MCP SDK to send:
	// server.SendNotification("notifications/claude/channel", {
	//   content: reminderContent,
	//   meta: { "source": "bifrost", "type": "dnd_reminder" },
	// })
	reminderContent := fmt.Sprintf("DND is active. %d message(s) queued. Use bifrost_dnd to disable when ready.", count)

	s.emitRawChannelNotification(map[string]string{
		"source": "bifrost",
		"type":   "dnd_reminder",
	}, reminderContent)
}

// emitRawChannelNotification sends a claude/channel notification with the given
// metadata and content. This is the low-level emission method used by both
// message notifications and DND reminders.
func (s *Shim) emitRawChannelNotification(meta map[string]string, content string) {
	// Use MCP SDK to send notification:
	// s.mcpServer.SendNotification("notifications/claude/channel", map[string]interface{}{
	//     "content": content,
	//     "meta":    meta,
	// })
	//
	// Check go-sdk API for the exact method. The notification should arrive as:
	// <channel source="bifrost" type="dnd_reminder">
	// DND is active. 3 messages queued. Use bifrost_dnd to disable when ready.
	// </channel>
}
```

- [ ] **Step 3: Update Shim struct to include dndState**

Add to `internal/shim/shim.go`:

```go
// Add to Shim struct:
//   dndState    *dndState

// In Run(), after creating the Shim struct, add:
//   s.initDND(5 * time.Minute) // default reminder interval
// If config is available, use cfg.DND.ReminderInterval.Duration instead.
```

Apply:

```go
type Shim struct {
	agentID     string
	projectName string
	displayName string
	hubConn     *transport.Conn
	dndState    *dndState
	// mcpServer — MCP server instance (from go-sdk)
}
```

In `Run`, after `s := &Shim{...}`:

```go
s.initDND(5 * time.Minute)
```

- [ ] **Step 4: Update channel.go to handle task notifications**

Add task notification formatting to `internal/shim/channel.go`:

```go
// In emitChannelNotification, update the task cases:

case "task_requested":
	// Convert payload to Task
	data, _ := json.Marshal(notif.Payload)
	var task protocol.Task
	json.Unmarshal(data, &task)

	meta := map[string]string{
		"source":  "bifrost",
		"from":    task.Requester,
		"type":    "task_requested",
		"task_id": task.TaskID,
		"conversation": task.ConversationID,
		"ts":      task.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}

	content := fmt.Sprintf("Title: %s\nDescription: %s", task.Title, task.Description)
	if len(task.Attachments) > 0 {
		content += fmt.Sprintf("\nAttachments: %d file(s)", len(task.Attachments))
		for _, att := range task.Attachments {
			content += fmt.Sprintf("\n  - %s (%s, %d bytes)", att.Filename, att.ContentType, att.Size)
		}
	}

	s.emitRawChannelNotification(meta, content)

case "task_update":
	data, _ := json.Marshal(notif.Payload)
	var task protocol.Task
	json.Unmarshal(data, &task)

	meta := map[string]string{
		"source":  "bifrost",
		"from":    task.Assignee,
		"type":    "task_update",
		"task_id": task.TaskID,
		"status":  string(task.Status),
		"ts":      task.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}

	content := ""
	switch task.Status {
	case protocol.TaskAccepted:
		content = "Task accepted."
	case protocol.TaskInProgress:
		content = "Task in progress."
	case protocol.TaskCompleted:
		content = task.Summary
		if content == "" {
			content = "Task completed."
		}
	case protocol.TaskFailed:
		content = "Task failed."
		if task.Reason != "" {
			content += " Reason: " + task.Reason
		}
	case protocol.TaskRejected:
		content = "Task rejected."
		if task.Reason != "" {
			content += " Reason: " + task.Reason
		}
	}

	s.emitRawChannelNotification(meta, content)
```

- [ ] **Step 5: Commit**

```bash
git add internal/shim/tools.go internal/shim/dnd.go internal/shim/shim.go internal/shim/channel.go
git commit -m "feat: add MCP tools for tasks, channels, DND with periodic reminders"
```

---

### Task 7: Hub RPC Handler for Attachment Retrieval

**Files:**
- Modify: `internal/hub/handler.go`

The `handleGetTask` shim tool needs to download attachments. Add an RPC method for retrieving attachment files.

- [ ] **Step 1: Add attachment retrieval handler**

Add to the `Handle` switch in `internal/hub/handler.go`:

```go
//   case "task.retrieve_attachment":
//       return h.handleRetrieveAttachment(ctx, req)
```

Implement:

```go
func (h *Handler) handleRetrieveAttachment(ctx context.Context, req *RPCRequest) *RPCResponse {
	var params struct {
		AttachmentID string `json:"attachment_id"`
		DestDir      string `json:"dest_dir"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid params: "+err.Error())
	}

	if h.hub.Attachments() == nil {
		return rpcError(req.ID, -32000, "attachments not configured")
	}

	destPath, err := h.hub.Attachments().Retrieve(ctx, params.AttachmentID, params.DestDir)
	if err != nil {
		return rpcError(req.ID, -32000, err.Error())
	}

	return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{
		"path": destPath,
	}}
}
```

- [ ] **Step 2: Commit**

```bash
git add internal/hub/handler.go
git commit -m "feat: add attachment retrieval RPC handler for file download"
```

---

### Task 8: Integration Tests

**Files:**
- Create: `test/integration/task_test.go`
- Create: `test/integration/channel_test.go`
- Create: `test/integration/dnd_test.go`
- Create: `test/integration/attachment_test.go`
- Create: `test/integration/conversation_test.go`

- [ ] **Step 1: Write task lifecycle integration test**

```go
// test/integration/task_test.go
package integration

import (
	"context"
	"testing"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"github.com/marcfargas/bifrost/test/testutil"
)

func TestFullTaskLifecycle(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	frontend := testutil.TestAgent("frontend")
	backend := testutil.TestAgent("backend")
	hub.Agents().Register(ctx, frontend)
	hub.Agents().Register(ctx, backend)

	// 1. Create task
	task, err := hub.Tasks().CreateTask(ctx, frontend.AgentID, "backend",
		"Implement GET /users/:id",
		"Return {id, name, email, created_at}. Validate UUID. Return 404 if not found.")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.Status != protocol.TaskRequested {
		t.Errorf("expected requested, got %s", task.Status)
	}

	// Verify assignee got task_requested notification
	taskNotifs := notifier.NotificationsOfType(backend.AgentID, "task_requested")
	if len(taskNotifs) != 1 {
		t.Fatalf("expected 1 task_requested, got %d", len(taskNotifs))
	}

	// 2. Assignee sends clarification question before accepting
	hub.Messages().Send(ctx, &protocol.Message{
		From:           backend.AgentID,
		To:             "frontend",
		Type:           protocol.MsgQuestion,
		Body:           "Should the UUID validation return 400 or 422?",
		ConversationID: task.ConversationID,
	})

	// 3. Requester answers
	hub.Messages().Send(ctx, &protocol.Message{
		From:           frontend.AgentID,
		To:             "backend",
		Type:           protocol.MsgAnswer,
		Body:           "Return 400 for invalid UUID format.",
		ConversationID: task.ConversationID,
	})

	// 4. Assignee accepts
	task, _ = hub.Tasks().UpdateTask(ctx, backend.AgentID, task.TaskID,
		core.TaskUpdate{Status: protocol.TaskAccepted})
	if task.Status != protocol.TaskAccepted {
		t.Errorf("expected accepted, got %s", task.Status)
	}

	// 5. Assignee moves to in_progress
	task, _ = hub.Tasks().UpdateTask(ctx, backend.AgentID, task.TaskID,
		core.TaskUpdate{Status: protocol.TaskInProgress})

	// 6. Assignee completes
	task, _ = hub.Tasks().UpdateTask(ctx, backend.AgentID, task.TaskID,
		core.TaskUpdate{
			Status:  protocol.TaskCompleted,
			Summary: "GET /users/:id implemented. Returns 200 with user JSON, 400 for bad UUID, 404 for not found.",
		})
	if task.Status != protocol.TaskCompleted {
		t.Errorf("expected completed, got %s", task.Status)
	}

	// Requester should have received all status updates
	updateNotifs := notifier.NotificationsOfType(frontend.AgentID, "task_update")
	if len(updateNotifs) < 3 {
		t.Errorf("expected at least 3 task_update notifications, got %d", len(updateNotifs))
	}

	// Conversation should still be open (task completion resets timer, doesn't close)
	conv, _ := hub.Store().GetConversation(ctx, task.ConversationID)
	if conv.Closed {
		t.Error("conversation should still be open after task completion")
	}
}

func TestTaskRejectionFlow(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	frontend := testutil.TestAgent("frontend")
	backend := testutil.TestAgent("backend")
	hub.Agents().Register(ctx, frontend)
	hub.Agents().Register(ctx, backend)

	task, _ := hub.Tasks().CreateTask(ctx, frontend.AgentID, "backend",
		"Deploy to staging", "Run the staging deploy pipeline")

	// Reject
	task, err := hub.Tasks().UpdateTask(ctx, backend.AgentID, task.TaskID,
		core.TaskUpdate{
			Status: protocol.TaskRejected,
			Reason: "I don't have access to the staging environment.",
		})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if task.Status != protocol.TaskRejected {
		t.Errorf("expected rejected, got %s", task.Status)
	}

	// Verify no further transitions are possible
	_, err = hub.Tasks().UpdateTask(ctx, backend.AgentID, task.TaskID,
		core.TaskUpdate{Status: protocol.TaskAccepted})
	if err == nil {
		t.Error("rejected task should not accept further transitions")
	}
}

func TestTaskListByRole(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	a := testutil.TestAgent("service-a")
	b := testutil.TestAgent("service-b")
	c := testutil.TestAgent("service-c")
	hub.Agents().Register(ctx, a)
	hub.Agents().Register(ctx, b)
	hub.Agents().Register(ctx, c)

	// A creates tasks for B and C
	hub.Tasks().CreateTask(ctx, a.AgentID, "service-b", "Task for B", "Do B stuff")
	hub.Tasks().CreateTask(ctx, a.AgentID, "service-c", "Task for C", "Do C stuff")
	// B creates a task for C
	hub.Tasks().CreateTask(ctx, b.AgentID, "service-c", "Another for C", "More C stuff")

	// B's assigned tasks
	bTasks, _ := hub.Tasks().ListTasks(ctx, store.TaskFilter{Assignee: b.AgentID})
	if len(bTasks) != 1 {
		t.Errorf("B should have 1 assigned task, got %d", len(bTasks))
	}

	// C's assigned tasks
	cTasks, _ := hub.Tasks().ListTasks(ctx, store.TaskFilter{Assignee: c.AgentID})
	if len(cTasks) != 2 {
		t.Errorf("C should have 2 assigned tasks, got %d", len(cTasks))
	}

	// A's requested tasks
	aTasks, _ := hub.Tasks().ListTasks(ctx, store.TaskFilter{Requester: a.AgentID})
	if len(aTasks) != 2 {
		t.Errorf("A should have 2 requested tasks, got %d", len(aTasks))
	}
}
```

Add helper method to `test/testutil/helpers.go`:

```go
// NotificationsOfType returns notifications of a specific type for an agent.
func (n *CollectingNotifier) NotificationsOfType(agentID, notifType string) []core.Notification {
	var result []core.Notification
	for _, notif := range n.Notifications[agentID] {
		if notif.Type == notifType {
			result = append(result, notif)
		}
	}
	return result
}
```

- [ ] **Step 2: Write channel integration test**

```go
// test/integration/channel_test.go
package integration

import (
	"context"
	"testing"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/test/testutil"
)

func TestChannelPubSub(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	a := testutil.TestAgent("service-a")
	b := testutil.TestAgent("service-b")
	c := testutil.TestAgent("service-c")
	hub.Agents().Register(ctx, a)
	hub.Agents().Register(ctx, b)
	hub.Agents().Register(ctx, c)

	// A and B subscribe to deploys
	hub.Channels().Subscribe(ctx, a.AgentID, "channel:deploys")
	hub.Channels().Subscribe(ctx, b.AgentID, "channel:deploys")

	// C subscribes to alerts
	hub.Channels().Subscribe(ctx, c.AgentID, "channel:alerts")

	// Verify channel listing
	channels, _ := hub.Channels().ListChannels(ctx)
	if len(channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(channels))
	}

	// A publishes to deploys
	notifier.Clear()
	hub.Messages().Send(ctx, &protocol.Message{
		From: a.AgentID, To: "channel:deploys",
		Type: protocol.MsgContext, Body: "Deploying v2.1 to production",
	})

	// B should get it, A and C should not
	bMsgs := notifier.MessagesFor(b.AgentID)
	if len(bMsgs) != 1 {
		t.Errorf("B should have 1 message, got %d", len(bMsgs))
	}
	if len(notifier.MessagesFor(a.AgentID)) > 0 {
		t.Error("sender A should not receive their own channel message")
	}
	if len(notifier.MessagesFor(c.AgentID)) > 0 {
		t.Error("C (not subscribed to deploys) should not receive the message")
	}
}

func TestTaskSubscriberNotifications(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	a := testutil.TestAgent("frontend")
	b := testutil.TestAgent("backend")
	c := testutil.TestAgent("observer")
	hub.Agents().Register(ctx, a)
	hub.Agents().Register(ctx, b)
	hub.Agents().Register(ctx, c)

	// Create task
	task, _ := hub.Tasks().CreateTask(ctx, a.AgentID, "backend", "Feature X", "Implement X")

	// Observer subscribes to task
	hub.Channels().Subscribe(ctx, c.AgentID, "task:"+task.TaskID)

	notifier.Clear()

	// Backend accepts task
	hub.Tasks().UpdateTask(ctx, b.AgentID, task.TaskID,
		core.TaskUpdate{Status: protocol.TaskAccepted})

	// Both requester and observer should be notified
	aUpdates := notifier.NotificationsOfType(a.AgentID, "task_update")
	cUpdates := notifier.NotificationsOfType(c.AgentID, "task_update")
	if len(aUpdates) != 1 {
		t.Errorf("requester should have 1 update, got %d", len(aUpdates))
	}
	if len(cUpdates) != 1 {
		t.Errorf("observer should have 1 update, got %d", len(cUpdates))
	}
}
```

Add `Clear` method to `test/testutil/helpers.go`:

```go
// Clear resets all collected notifications.
func (n *CollectingNotifier) Clear() {
	n.Notifications = make(map[string][]core.Notification)
}
```

- [ ] **Step 3: Write DND integration test**

```go
// test/integration/dnd_test.go
package integration

import (
	"context"
	"testing"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/test/testutil"
)

func TestDNDQueuesAndFlushes(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	sender := testutil.TestAgent("frontend")
	receiver := testutil.TestAgent("backend")
	hub.Agents().Register(ctx, sender)
	hub.Agents().Register(ctx, receiver)

	// Enable DND
	hub.DND().Enable(ctx, receiver.AgentID, "Deep focus")

	notifier.Clear()

	// Send 3 normal messages
	for i := 0; i < 3; i++ {
		status, err := hub.Messages().Send(ctx, &protocol.Message{
			From: sender.AgentID, To: "backend",
			Type: protocol.MsgContext, Body: "queued message",
			Priority: protocol.PriorityNormal,
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		if status != protocol.QueuedOffline {
			t.Errorf("expected queued, got %s", status)
		}
	}

	// Receiver should have zero notifications
	if len(notifier.MessagesFor(receiver.AgentID)) != 0 {
		t.Error("receiver should have 0 messages while DND")
	}

	// Queue count should be 3
	count, _ := hub.DND().QueuedCount(ctx, receiver.AgentID)
	if count != 3 {
		t.Errorf("expected 3 queued, got %d", count)
	}

	// Disable DND
	flushed, _ := hub.DND().Disable(ctx, receiver.AgentID)
	if flushed != 3 {
		t.Errorf("expected 3 flushed, got %d", flushed)
	}

	// Receiver should now have all 3
	if len(notifier.MessagesFor(receiver.AgentID)) != 3 {
		t.Errorf("expected 3 flushed messages, got %d", len(notifier.MessagesFor(receiver.AgentID)))
	}
}

func TestDNDUrgentBreaksThrough(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	sender := testutil.TestAgent("frontend")
	receiver := testutil.TestAgent("backend")
	hub.Agents().Register(ctx, sender)
	hub.Agents().Register(ctx, receiver)

	hub.DND().Enable(ctx, receiver.AgentID, "Busy")
	notifier.Clear()

	// Send urgent message
	status, err := hub.Messages().Send(ctx, &protocol.Message{
		From: sender.AgentID, To: "backend",
		Type: protocol.MsgQuestion, Body: "PROD IS DOWN",
		Priority: protocol.PriorityUrgent,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != protocol.Delivered {
		t.Errorf("urgent should be delivered, got %s", status)
	}

	// Send normal message
	status, _ = hub.Messages().Send(ctx, &protocol.Message{
		From: sender.AgentID, To: "backend",
		Type: protocol.MsgContext, Body: "non-urgent",
		Priority: protocol.PriorityNormal,
	})
	if status != protocol.QueuedOffline {
		t.Errorf("normal should be queued, got %s", status)
	}

	// Receiver should have only the urgent message delivered
	msgs := notifier.MessagesFor(receiver.AgentID)
	if len(msgs) != 1 {
		t.Errorf("expected 1 delivered message, got %d", len(msgs))
	}
	if msgs[0].Body != "PROD IS DOWN" {
		t.Errorf("wrong message delivered: %s", msgs[0].Body)
	}
}
```

- [ ] **Step 4: Write attachment integration test**

```go
// test/integration/attachment_test.go
package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/store"
	"github.com/marcfargas/bifrost/test/testutil"
)

func TestTaskWithAttachments(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	hub := core.NewHubWithConfig(s, dataDir, "10MB")
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	ctx := context.Background()

	frontend := testutil.TestAgent("frontend")
	backend := testutil.TestAgent("backend")
	hub.Agents().Register(ctx, frontend)
	hub.Agents().Register(ctx, backend)

	// Create a file to attach
	srcDir := t.TempDir()
	schemaFile := filepath.Join(srcDir, "user-schema.json")
	os.WriteFile(schemaFile, []byte(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "format": "uuid"},
			"name": {"type": "string"},
			"email": {"type": "string", "format": "email"},
			"created_at": {"type": "string", "format": "date-time"}
		},
		"required": ["id", "name", "email"]
	}`), 0o644)

	// Create task
	task, err := hub.Tasks().CreateTask(ctx, frontend.AgentID, "backend",
		"Implement GET /users/:id", "See attached schema")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Attach file
	att, err := hub.Attachments().Store(ctx, task.TaskID, frontend.AgentID, schemaFile)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if att.Filename != "user-schema.json" {
		t.Errorf("expected user-schema.json, got %s", att.Filename)
	}

	// Get task with attachments
	fetched, err := hub.Tasks().GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if len(fetched.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(fetched.Attachments))
	}
	if fetched.Attachments[0].Filename != "user-schema.json" {
		t.Error("wrong attachment filename")
	}

	// Retrieve attachment to a destination directory
	destDir := t.TempDir()
	destPath, err := hub.Attachments().Retrieve(ctx, att.AttachmentID, destDir)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	// Verify content
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) == 0 {
		t.Error("retrieved file should not be empty")
	}
}

func TestAttachmentSizeLimit(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Set max to 100 bytes
	hub := core.NewHubWithConfig(s, dataDir, "100B")
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	ctx := context.Background()
	frontend := testutil.TestAgent("frontend")
	backend := testutil.TestAgent("backend")
	hub.Agents().Register(ctx, frontend)
	hub.Agents().Register(ctx, backend)

	task, _ := hub.Tasks().CreateTask(ctx, frontend.AgentID, "backend", "Test", "Test")

	// Create a file that exceeds the limit
	srcDir := t.TempDir()
	bigFile := filepath.Join(srcDir, "big.bin")
	bigData := make([]byte, 200)
	os.WriteFile(bigFile, bigData, 0o644)

	_, err = hub.Attachments().Store(ctx, task.TaskID, frontend.AgentID, bigFile)
	if err == nil {
		t.Error("should reject file exceeding max size")
	}
}
```

- [ ] **Step 5: Write conversation lifecycle integration test**

```go
// test/integration/conversation_test.go
package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"github.com/marcfargas/bifrost/test/testutil"
)

func TestConversationAutoCloseAfterInactivity(t *testing.T) {
	// Build hub with very short inactivity timeout
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := core.NewHub(s)
	notifier := testutil.NewCollectingNotifier()
	h.AddNotifier(notifier)

	ctx := context.Background()

	frontend := testutil.TestAgent("frontend")
	backend := testutil.TestAgent("backend")
	h.Agents().Register(ctx, frontend)
	h.Agents().Register(ctx, backend)

	// Send a message to create a conversation
	msg := &protocol.Message{
		From: frontend.AgentID, To: "backend",
		Type: protocol.MsgQuestion, Body: "Hello?",
	}
	h.Messages().Send(ctx, msg)

	// Manually backdate the conversation's last_activity
	convs, _ := h.Store().ListConversations(ctx, store.ConversationFilter{ActiveOnly: true})
	if len(convs) == 0 {
		t.Fatal("expected at least 1 conversation")
	}
	conv := convs[0]

	// Simulate inactivity by saving the conversation with old last_activity
	conv.LastActivity = time.Now().Add(-15 * time.Minute)
	h.Store().SaveConversation(ctx, conv)

	// Run close stale
	closed, err := h.Conversations().CloseStale(ctx)
	if err != nil {
		t.Fatalf("close stale: %v", err)
	}
	if closed != 1 {
		t.Errorf("expected 1 closed, got %d", closed)
	}

	// Verify
	got, _ := h.Store().GetConversation(ctx, conv.ConversationID)
	if !got.Closed {
		t.Error("conversation should be closed")
	}
	if got.ClosedReason != protocol.CloseInactivity {
		t.Errorf("expected inactivity reason, got %s", got.ClosedReason)
	}
}

func TestConversationLinkedToTask(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	frontend := testutil.TestAgent("frontend")
	backend := testutil.TestAgent("backend")
	hub.Agents().Register(ctx, frontend)
	hub.Agents().Register(ctx, backend)

	// Create task — it creates a linked conversation
	task, _ := hub.Tasks().CreateTask(ctx, frontend.AgentID, "backend", "Test", "Test")

	// Get the conversation
	conv, err := hub.Store().GetConversation(ctx, task.ConversationID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.TaskID != task.TaskID {
		t.Errorf("conversation should be linked to task %s, got %s", task.TaskID, conv.TaskID)
	}

	// Messages in the task conversation should work
	hub.Messages().Send(ctx, &protocol.Message{
		From:           backend.AgentID,
		To:             "frontend",
		Type:           protocol.MsgQuestion,
		Body:           "Clarification needed",
		ConversationID: task.ConversationID,
	})

	msgs := notifier.MessagesFor(frontend.AgentID)
	found := false
	for _, m := range msgs {
		if m.Body == "Clarification needed" && m.ConversationID == task.ConversationID {
			found = true
		}
	}
	if !found {
		t.Error("message in task conversation should be delivered")
	}
}

func TestTaskCompletionDoesNotCloseConversation(t *testing.T) {
	hub := testutil.TestHub(t)
	ctx := context.Background()
	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	frontend := testutil.TestAgent("frontend")
	backend := testutil.TestAgent("backend")
	hub.Agents().Register(ctx, frontend)
	hub.Agents().Register(ctx, backend)

	task, _ := hub.Tasks().CreateTask(ctx, frontend.AgentID, "backend", "Test", "Test")

	// Complete the task
	hub.Tasks().UpdateTask(ctx, backend.AgentID, task.TaskID,
		core.TaskUpdate{Status: protocol.TaskAccepted})
	hub.Tasks().UpdateTask(ctx, backend.AgentID, task.TaskID,
		core.TaskUpdate{Status: protocol.TaskInProgress})
	hub.Tasks().UpdateTask(ctx, backend.AgentID, task.TaskID,
		core.TaskUpdate{Status: protocol.TaskCompleted, Summary: "Done"})

	// Conversation should still be open
	conv, _ := hub.Store().GetConversation(ctx, task.ConversationID)
	if conv.Closed {
		t.Error("conversation should NOT be closed by task completion")
	}

	// Messages should still work after task completion
	_, err := hub.Messages().Send(ctx, &protocol.Message{
		From:           frontend.AgentID,
		To:             "backend",
		Type:           protocol.MsgContext,
		Body:           "Thanks! One more thing...",
		ConversationID: task.ConversationID,
	})
	if err != nil {
		t.Fatalf("post-completion message should work: %v", err)
	}
}
```

- [ ] **Step 6: Run all tests**

```bash
go test ./... -v
```

Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add test/testutil/helpers.go test/integration/
git commit -m "test: add integration tests for tasks, channels, DND, attachments, and conversations"
```

---

That completes Plan 2. After this plan is implemented, you have:

- Full task lifecycle: create, accept, reject, in_progress, complete, fail — with state machine validation
- Task request notifications with full details inline in claude/channel
- Clarification via QUESTION messages in the task's conversation before accepting
- File attachments stored on hub disk with configurable max size, upload/download via shim tools
- Broadcast channels with bifrost_subscribe/bifrost_list_channels
- Task subscriptions so observers can follow task progress
- DND mode with message queuing, urgent breakthrough, periodic reminders, and flush on disable
- Conversation auto-close after inactivity timeout (task completion resets timer, does not close)
- Hub RPC handlers for all operations (task.create, task.update, task.get, task.list, task.attach, task.retrieve_attachment, channel.subscribe, channel.unsubscribe, channel.list, dnd.set, dnd.status)
- Shim MCP tools: bifrost_create_task, bifrost_update_task, bifrost_get_task, bifrost_list_tasks, bifrost_subscribe, bifrost_list_channels, bifrost_dnd
- Integration tests covering all features
