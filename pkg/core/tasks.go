package core

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// ValidTransitions defines the allowed task status transitions.
var ValidTransitions = map[protocol.TaskStatus][]protocol.TaskStatus{
	protocol.TaskStatusRequested:  {protocol.TaskStatusAccepted, protocol.TaskStatusRejected},
	protocol.TaskStatusAccepted:   {protocol.TaskStatusInProgress},
	protocol.TaskStatusInProgress: {protocol.TaskStatusCompleted, protocol.TaskStatusFailed},
}

// TaskUpdate carries the fields that may be changed in a single UpdateTask call.
type TaskUpdate struct {
	Status      protocol.TaskStatus
	Description string // appended to existing description when non-empty
	Summary     string
	Reason      string
}

// TaskManager handles task lifecycle: creation, updates, and retrieval.
type TaskManager struct {
	store store.Store
	hub   *Hub
}

func newTaskManager(s store.Store, h *Hub) *TaskManager {
	return &TaskManager{store: s, hub: h}
}

// isValidTransition reports whether transitioning from → to is allowed.
func isValidTransition(from, to protocol.TaskStatus) bool {
	allowed, ok := ValidTransitions[from]
	if !ok {
		return false
	}
	return slices.Contains(allowed, to)
}

// CreateTask creates a new task from requester to assignee. It resolves the
// assignee via the agent registry, creates a dedicated conversation, persists
// the task, links the conversation, subscribes both parties to "task:<id>",
// and notifies the assignee.
func (m *TaskManager) CreateTask(ctx context.Context, requesterID, assigneeAddr, title, description string) (*protocol.Task, error) {
	assignee, err := m.hub.Agents().Resolve(ctx, assigneeAddr)
	if err != nil {
		return nil, fmt.Errorf("tasks: resolve assignee: %w", err)
	}

	now := time.Now()

	// Generate IDs up front so conversation and task can reference each other.
	taskID := uuid.New().String()
	convID := uuid.New().String()[:8]

	// Create the conversation already linked to the task.
	conv := &protocol.Conversation{
		ConversationID: convID,
		Participants:   []string{requesterID, assignee.AgentID},
		TaskID:         taskID,
		CreatedAt:      now,
		LastActivity:   now,
	}
	if err := m.store.SaveConversation(ctx, conv); err != nil {
		return nil, fmt.Errorf("tasks: save conversation: %w", err)
	}

	task := &protocol.Task{
		TaskID:         taskID,
		ConversationID: convID,
		Requester:      requesterID,
		Assignee:       assignee.AgentID,
		Title:          title,
		Description:    description,
		Status:         protocol.TaskStatusRequested,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := m.store.SaveTask(ctx, task); err != nil {
		return nil, fmt.Errorf("tasks: save task: %w", err)
	}

	// Subscribe both parties to the task channel.
	target := "task:" + task.TaskID
	if err := m.store.Subscribe(ctx, requesterID, target); err != nil {
		return nil, fmt.Errorf("tasks: subscribe requester: %w", err)
	}
	if err := m.store.Subscribe(ctx, assignee.AgentID, target); err != nil {
		return nil, fmt.Errorf("tasks: subscribe assignee: %w", err)
	}

	// Notify assignee via the message router — this handles local delivery,
	// federation forwarding, DND queuing, and offline queuing automatically.
	// Tasks are conversations: the task notification is a message in the
	// task's conversation, routed like any other message.
	taskMsg := &protocol.Message{
		ID:             protocol.NewID(),
		ConversationID: task.ConversationID,
		From:           requesterID,
		To:             "agent:" + assignee.AgentID,
		Type:           protocol.MessageTypeContext,
		Body:           "Task requested: " + task.Title + "\n\n" + task.Description,
		Priority:       protocol.PriorityNormal,
	}
	m.hub.Messages().Send(ctx, taskMsg)

	// Also push the structured task notification for channel display.
	m.hub.NotifyAgent(assignee.AgentID, Notification{
		Type:    "task_requested",
		Payload: task,
	})

	return task, nil
}

// UpdateTask validates the caller, validates any status transition, applies
// updates, and notifies all task subscribers except the caller.
func (m *TaskManager) UpdateTask(ctx context.Context, callerAgentID, taskID string, update TaskUpdate) (*protocol.Task, error) {
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("tasks: task not found: %s", taskID)
	}

	// Only the assignee or requester may update a task.
	if callerAgentID != task.Assignee && callerAgentID != task.Requester {
		return nil, fmt.Errorf("tasks: unauthorized: caller %q is not assignee or requester", callerAgentID)
	}

	// Validate status transition if a status change is requested.
	if update.Status != "" && update.Status != task.Status {
		if !isValidTransition(task.Status, update.Status) {
			return nil, fmt.Errorf("tasks: invalid transition %s → %s", task.Status, update.Status)
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
		if task.Description != "" {
			task.Description += "\n" + update.Description
		} else {
			task.Description = update.Description
		}
	}

	task.UpdatedAt = time.Now()

	if err := m.store.UpdateTask(ctx, task); err != nil {
		return nil, fmt.Errorf("tasks: update task: %w", err)
	}

	// Touch the conversation.
	if task.ConversationID != "" {
		_ = m.store.TouchConversation(ctx, task.ConversationID)
	}

	// Notify all subscribers except the caller via the message router.
	// This handles federation, DND, and offline queuing automatically.
	subscribers, err := m.store.GetSubscribers(ctx, "task:"+taskID)
	if err == nil {
		statusMsg := fmt.Sprintf("Task %s: %s", task.Status, task.Title)
		if task.Summary != "" {
			statusMsg += "\n" + task.Summary
		}
		if task.Reason != "" {
			statusMsg += "\nReason: " + task.Reason
		}

		for _, agentID := range subscribers {
			if agentID == callerAgentID {
				continue
			}
			// Send as a message — the router handles federation/offline/DND.
			m.hub.Messages().Send(ctx, &protocol.Message{
				ID:             protocol.NewID(),
				ConversationID: task.ConversationID,
				From:           callerAgentID,
				To:             "agent:" + agentID,
				Type:           protocol.MessageTypeStatus,
				Body:           statusMsg,
				Priority:       protocol.PriorityNormal,
			})
			// Also push the structured notification for channel display.
			m.hub.NotifyAgent(agentID, Notification{Type: "task_updated", Payload: task})
		}
	}

	return task, nil
}

// GetTask loads a task along with its attachments list.
func (m *TaskManager) GetTask(ctx context.Context, taskID string) (*protocol.Task, error) {
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: get task: %w", err)
	}
	if task == nil {
		return nil, nil
	}

	atts, err := m.store.ListAttachments(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: list attachments: %w", err)
	}
	ids := make([]string, 0, len(atts))
	for _, a := range atts {
		ids = append(ids, a.AttachmentID)
	}
	task.Attachments = ids

	return task, nil
}

// ListTasks delegates filtering to the store.
func (m *TaskManager) ListTasks(ctx context.Context, filter store.TaskFilter) ([]*protocol.Task, error) {
	tasks, err := m.store.ListTasks(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}
	return tasks, nil
}
