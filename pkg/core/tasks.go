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
// Tasks are conversations with IsTask=true. There is no separate task table;
// task state is derived from conversation metadata and the event stream.
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
// assignee via the agent registry, creates a conversation with IsTask=true,
// initialises delivery targets, appends a message event via the SyncEngine,
// and notifies the assignee. Returns a *protocol.Task built from the
// conversation so that callers (handler.go) see the same shape as before.
func (m *TaskManager) CreateTask(ctx context.Context, requesterID, assigneeAddr, title, description string) (*protocol.Task, error) {
	assignee, err := m.hub.Agents().Resolve(ctx, assigneeAddr)
	if err != nil {
		return nil, fmt.Errorf("tasks: resolve assignee: %w", err)
	}

	now := time.Now()

	// The conversation ID doubles as the task ID in the new model.
	convID := uuid.New().String()[:8]

	conv := &protocol.Conversation{
		ConversationID: convID,
		Participants:   []string{requesterID, assignee.AgentID},
		IsTask:         true,
		Title:          title,
		Assignee:       assignee.AgentID,
		Requester:      requesterID,
		CreatedAt:      now,
	}
	if err := m.store.SaveConversation(ctx, conv); err != nil {
		return nil, fmt.Errorf("tasks: save conversation: %w", err)
	}

	// Initialise delivery state so the SyncEngine delivers to both participants.
	if err := m.store.InitDeliveryTargets(ctx, conv); err != nil {
		return nil, fmt.Errorf("tasks: init delivery targets: %w", err)
	}

	// Append the initial message event with the task description.
	ev := &protocol.Event{
		ConversationID: convID,
		Type:           protocol.EventTypeMessage,
		FromAgent:      requesterID,
		Data: protocol.EventData{
			Body:        "Task requested: " + title + "\n\n" + description,
			MessageType: protocol.MessageTypeContext,
			Priority:    protocol.PriorityNormal,
		},
	}

	if eng := m.hub.Sync(); eng != nil {
		if err := eng.AppendEvent(ctx, ev); err != nil {
			return nil, fmt.Errorf("tasks: append event: %w", err)
		}
	}

	// Also subscribe both parties to "task:<convID>" for legacy channel routing.
	target := "task:" + convID
	_ = m.store.Subscribe(ctx, requesterID, target)
	_ = m.store.Subscribe(ctx, assignee.AgentID, target)

	// Push the structured task notification to the assignee for channel display.
	task := convToTask(conv, protocol.TaskStatusRequested, "", "", now, now)
	task.Description = description
	m.hub.NotifyAgent(assignee.AgentID, Notification{
		Type:    "task_requested",
		Payload: task,
	})

	return task, nil
}

// UpdateTask validates the caller, validates any status transition, appends a
// status event via the SyncEngine, and notifies all task subscribers except
// the caller. The taskID parameter is the ConversationID in the new model.
func (m *TaskManager) UpdateTask(ctx context.Context, callerAgentID, taskID string, update TaskUpdate) (*protocol.Task, error) {
	// Look up the conversation (taskID == conversationID).
	conv, err := m.store.GetConversation(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: get conversation: %w", err)
	}
	if conv == nil || !conv.IsTask {
		return nil, fmt.Errorf("tasks: task not found: %s", taskID)
	}

	// Only assignee or requester may update.
	if callerAgentID != conv.Assignee && callerAgentID != conv.Requester {
		return nil, fmt.Errorf("tasks: unauthorized: caller %q is not assignee or requester", callerAgentID)
	}

	// Derive current status from the latest status event.
	currentStatus, summary, reason, updatedAt, err := m.latestStatus(ctx, taskID)
	if err != nil {
		return nil, err
	}

	// Validate status transition if a new status is requested.
	if update.Status != "" && update.Status != currentStatus {
		if !isValidTransition(currentStatus, update.Status) {
			return nil, fmt.Errorf("tasks: invalid transition %s → %s", currentStatus, update.Status)
		}
	}

	newStatus := currentStatus
	if update.Status != "" {
		newStatus = update.Status
	}
	if update.Summary != "" {
		summary = update.Summary
	}
	if update.Reason != "" {
		reason = update.Reason
	}

	now := time.Now()

	// Append a status event.
	ev := &protocol.Event{
		ConversationID: taskID,
		Type:           protocol.EventTypeStatus,
		FromAgent:      callerAgentID,
		Data: protocol.EventData{
			NewStatus: newStatus,
			Summary:   summary,
			Reason:    reason,
		},
	}

	if eng := m.hub.Sync(); eng != nil {
		if err := eng.AppendEvent(ctx, ev); err != nil {
			return nil, fmt.Errorf("tasks: append status event: %w", err)
		}
	}

	// If there was a description update, append a message event too.
	if update.Description != "" {
		descEv := &protocol.Event{
			ConversationID: taskID,
			Type:           protocol.EventTypeMessage,
			FromAgent:      callerAgentID,
			Data: protocol.EventData{
				Body:        update.Description,
				MessageType: protocol.MessageTypeContext,
				Priority:    protocol.PriorityNormal,
			},
		}
		if eng := m.hub.Sync(); eng != nil {
			_ = eng.AppendEvent(ctx, descEv)
		}
	}

	task := convToTask(conv, newStatus, summary, reason, conv.CreatedAt, now)
	_ = updatedAt // consumed above

	// Notify all task subscribers (except the caller) via structured notification.
	subscribers, err := m.store.GetSubscribers(ctx, "task:"+taskID)
	if err == nil {
		for _, agentID := range subscribers {
			if agentID == callerAgentID {
				continue
			}
			m.hub.NotifyAgent(agentID, Notification{Type: "task_updated", Payload: task})
		}
	}

	return task, nil
}

// GetTask derives a *protocol.Task from the conversation and its event stream.
// The taskID parameter is the ConversationID.
func (m *TaskManager) GetTask(ctx context.Context, taskID string) (*protocol.Task, error) {
	conv, err := m.store.GetConversation(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: get conversation: %w", err)
	}
	if conv == nil || !conv.IsTask {
		return nil, nil
	}

	status, summary, reason, updatedAt, err := m.latestStatus(ctx, taskID)
	if err != nil {
		return nil, err
	}

	task := convToTask(conv, status, summary, reason, conv.CreatedAt, updatedAt)
	return task, nil
}

// ListTasks returns task conversations matching the given filter.
func (m *TaskManager) ListTasks(ctx context.Context, filter store.TaskFilter) ([]*protocol.Task, error) {
	isTask := true
	convFilter := store.ConversationFilter{
		IsTask:    &isTask,
		Assignee:  filter.Assignee,
		Requester: filter.Requester,
	}
	if filter.ConversationID != "" {
		// Fetch single conversation by ID.
		conv, err := m.store.GetConversation(ctx, filter.ConversationID)
		if err != nil {
			return nil, fmt.Errorf("tasks: list: %w", err)
		}
		if conv == nil || !conv.IsTask {
			return nil, nil
		}
		task, err := m.GetTask(ctx, conv.ConversationID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, nil
		}
		return []*protocol.Task{task}, nil
	}

	convs, err := m.store.ListConversations(ctx, convFilter)
	if err != nil {
		return nil, fmt.Errorf("tasks: list conversations: %w", err)
	}

	tasks := make([]*protocol.Task, 0, len(convs))
	for _, conv := range convs {
		task, err := m.GetTask(ctx, conv.ConversationID)
		if err != nil || task == nil {
			continue
		}
		// Apply status filter if set.
		if filter.Status != "" && task.Status != filter.Status {
			continue
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// latestStatus derives the current task status from the latest status event.
// If no status event exists, the task is in "requested" state.
func (m *TaskManager) latestStatus(ctx context.Context, conversationID string) (
	status protocol.TaskStatus, summary, reason string, updatedAt time.Time, err error,
) {
	ev, err := m.store.LatestStatusEvent(ctx, conversationID)
	if err != nil {
		return "", "", "", time.Time{}, fmt.Errorf("tasks: latest status event: %w", err)
	}
	if ev == nil {
		// No status event yet — task is in its initial state.
		conv, getErr := m.store.GetConversation(ctx, conversationID)
		if getErr != nil {
			return "", "", "", time.Time{}, getErr
		}
		if conv != nil {
			return protocol.TaskStatusRequested, "", "", conv.CreatedAt, nil
		}
		return protocol.TaskStatusRequested, "", "", time.Now(), nil
	}
	return ev.Data.NewStatus, ev.Data.Summary, ev.Data.Reason, ev.Timestamp, nil
}

// convToTask builds a *protocol.Task from a Conversation and derived state.
// TaskID is set to ConversationID since there is no separate task table.
func convToTask(
	conv *protocol.Conversation,
	status protocol.TaskStatus,
	summary, reason string,
	createdAt, updatedAt time.Time,
) *protocol.Task {
	return &protocol.Task{
		TaskID:         conv.ConversationID, // TaskID == ConversationID in new model
		ConversationID: conv.ConversationID,
		Requester:      conv.Requester,
		Assignee:       conv.Assignee,
		Title:          conv.Title,
		Status:         status,
		Summary:        summary,
		Reason:         reason,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}
}
