package protocol

import "time"

// AgentStatus represents the current availability state of an agent.
type AgentStatus string

const (
	AgentStatusOnline      AgentStatus = "online"
	AgentStatusIdle        AgentStatus = "idle"
	AgentStatusOffline     AgentStatus = "offline"
	AgentStatusUnreachable AgentStatus = "unreachable"
	AgentStatusDND         AgentStatus = "dnd"
)

// MessageType classifies the purpose of a message.
type MessageType string

const (
	MessageTypeQuestion MessageType = "QUESTION"
	MessageTypeAnswer   MessageType = "ANSWER"
	MessageTypeContext  MessageType = "CONTEXT"
	MessageTypeStatus   MessageType = "STATUS"
	MessageTypeError    MessageType = "ERROR"
)

// Priority indicates the urgency of a message or task.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityUrgent Priority = "urgent"
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusRequested  TaskStatus = "requested"
	TaskStatusAccepted   TaskStatus = "accepted"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusRejected   TaskStatus = "rejected"
)

// ConversationCloseReason explains why a conversation was closed.
type ConversationCloseReason string

const (
	ConversationCloseReasonInactivity ConversationCloseReason = "inactivity"
	ConversationCloseReasonExplicit   ConversationCloseReason = "explicit"
)

// DeliveryStatus describes the delivery outcome of a message.
type DeliveryStatus string

const (
	DeliveryStatusDelivered         DeliveryStatus = "delivered"
	DeliveryStatusQueuedOffline     DeliveryStatus = "queued_offline"
	DeliveryStatusQueuedUnreachable DeliveryStatus = "queued_unreachable"
)

// Agent represents a registered agent in the Bifrost network.
type Agent struct {
	AgentID         string      `json:"agent_id"`
	Aliases         []string    `json:"aliases,omitempty"`
	Username        string      `json:"username"`
	Hostname        string      `json:"hostname"`
	LocalPath       string      `json:"local_path"`
	ProjectName     string      `json:"project_name"`
	Capabilities    []string    `json:"capabilities,omitempty"`
	DisplayName     string      `json:"display_name,omitempty"`
	Status          AgentStatus `json:"status"`
	DNDReason       string      `json:"dnd_reason,omitempty"`
	ConnectedAt     time.Time   `json:"connected_at"`
	LastSeen        time.Time   `json:"last_seen"`
	ProtocolVersion string      `json:"protocol_version"`
	PeerHub         string      `json:"peer_hub,omitempty"`
}

// Message is a single unit of communication between agents.
type Message struct {
	ID             string      `json:"id"`
	ConversationID string      `json:"conversation_id"`
	From           string      `json:"from"`
	To             string      `json:"to"`
	Type           MessageType `json:"type"`
	Subject        string      `json:"subject,omitempty"`
	Body           string      `json:"body"`
	InReplyTo      string      `json:"in_reply_to,omitempty"`
	Priority       Priority    `json:"priority"`
	Timestamp      time.Time   `json:"timestamp"`
	Acknowledged   bool        `json:"acknowledged"`
	NoReply        bool        `json:"no_reply,omitempty"`
}

// Conversation groups related messages between participants.
type Conversation struct {
	ConversationID string                  `json:"conversation_id"`
	Participants   []string                `json:"participants"`
	TaskID         string                  `json:"task_id,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
	LastActivity   time.Time               `json:"last_activity"`
	Closed         bool                    `json:"closed"`
	ClosedReason   ConversationCloseReason `json:"closed_reason,omitempty"`
}

// Task represents a unit of delegated work between agents.
type Task struct {
	TaskID         string     `json:"task_id"`
	ConversationID string     `json:"conversation_id"`
	Requester      string     `json:"requester"`
	Assignee       string     `json:"assignee"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Status         TaskStatus `json:"status"`
	Reason         string     `json:"reason,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	Attachments    []string   `json:"attachments,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Attachment is a file associated with a task.
type Attachment struct {
	AttachmentID string    `json:"attachment_id"`
	TaskID       string    `json:"task_id"`
	Filename     string    `json:"filename"`
	ContentType  string    `json:"content_type"`
	Size         int64     `json:"size"`
	UploadedBy   string    `json:"uploaded_by"`
	UploadedAt   time.Time `json:"uploaded_at"`
}
