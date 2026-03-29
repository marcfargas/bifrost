package protocol

import (
	"encoding/json"
	"time"
)

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

// Conversation groups events between participants.
// Tasks are conversations with IsTask=true.
type Conversation struct {
	ConversationID string                  `json:"conversation_id"`
	Participants   []string                `json:"participants"`
	IsTask         bool                    `json:"is_task"`
	Title          string                  `json:"title,omitempty"`
	Assignee       string                  `json:"assignee,omitempty"`
	Requester      string                  `json:"requester,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
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

// PeerStatus represents the current connectivity state of a peer hub.
type PeerStatus string

const (
	PeerStatusConnected    PeerStatus = "connected"
	PeerStatusUnreachable  PeerStatus = "unreachable"
	PeerStatusDisconnected PeerStatus = "disconnected"
)

// PeerTransport identifies the protocol used to reach a peer hub.
type PeerTransport string

const (
	PeerTransportLibp2p PeerTransport = "libp2p"
	PeerTransportMDNS   PeerTransport = "mdns"
	PeerTransportDirect PeerTransport = "direct"
)

// Peer represents a remote Bifrost hub connected via federation.
type Peer struct {
	PeerID       string        `json:"peer_id"`
	DisplayName  string        `json:"display_name,omitempty"`
	Transport    PeerTransport `json:"transport"`
	Address      string        `json:"address"`
	Token        string        `json:"token,omitempty"`
	Status       PeerStatus    `json:"status"`
	LastSeen     time.Time     `json:"last_seen"`
	ConnectedAt  time.Time     `json:"connected_at"`
	FailCount    int           `json:"fail_count"`
	ProtoVersion string        `json:"proto_version"`
}

// PeerEnvelope is the top-level wrapper for all peer-to-peer protocol messages.
type PeerEnvelope struct {
	Method  string          `json:"method"`
	ID      string          `json:"id"`
	Version string          `json:"version"`
	From    string          `json:"from"`
	Payload json.RawMessage `json:"payload"`
}

// PeerSyncAgentsPayload carries the list of agents a hub wishes to advertise.
type PeerSyncAgentsPayload struct {
	Agents []*Agent `json:"agents"`
}

// PeerAttachmentData holds inline file content for a cross-hub task attachment.
type PeerAttachmentData struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"` // base64-encoded by json.Marshal
}

// PeerAgentStatusPayload notifies a peer of an agent status change.
type PeerAgentStatusPayload struct {
	AgentID   string      `json:"agent_id"`
	Status    AgentStatus `json:"status"`
	DNDReason string      `json:"dnd_reason,omitempty"`
}

// PeerHeartbeatPayload is sent periodically to confirm liveness.
type PeerHeartbeatPayload struct {
	Timestamp time.Time `json:"timestamp"`
}

// EventType classifies what happened in a conversation.
type EventType string

const (
	EventTypeMessage            EventType = "message"
	EventTypeStatus             EventType = "status"
	EventTypeMetadata           EventType = "metadata"
	EventTypeFile               EventType = "file"
	EventTypeParticipantAdded   EventType = "participant.added"
	EventTypeParticipantRemoved EventType = "participant.removed"
)

// MaxFileSizeBytes is the maximum inline file size for file events.
const MaxFileSizeBytes = 256 * 1024 // 256KB

// Event is a single occurrence in a conversation stream.
type Event struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Type           EventType `json:"type"`
	FromAgent      string    `json:"from_agent"`
	Data           EventData `json:"data"`
	Timestamp      time.Time `json:"timestamp"`
}

// EventData is the polymorphic payload of an Event. Fields are populated
// based on the event type; unused fields are zero-valued.
type EventData struct {
	// message event
	Body        string      `json:"body,omitempty"`
	MessageType MessageType `json:"message_type,omitempty"`
	Priority    Priority    `json:"priority,omitempty"`
	InReplyTo   string      `json:"in_reply_to,omitempty"`

	// status event
	NewStatus TaskStatus `json:"new_status,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Reason    string     `json:"reason,omitempty"`

	// metadata event
	Field string `json:"field,omitempty"`
	Value string `json:"value,omitempty"`

	// file event
	Filename    string `json:"filename,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Content     []byte `json:"content,omitempty"` // base64 via json.Marshal, max 256KB

	// participant.added / participant.removed
	AgentID string `json:"agent_id,omitempty"`
}

// ConvMeta carries conversation metadata for federation sync.
type ConvMeta struct {
	ID           string    `json:"id"`
	Participants []string  `json:"participants"`
	IsTask       bool      `json:"is_task"`
	Title        string    `json:"title,omitempty"`
	Assignee     string    `json:"assignee,omitempty"`
	Requester    string    `json:"requester,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// PeerConversationSyncPayload is sent via peer.conversation_sync.
type PeerConversationSyncPayload struct {
	Conversation ConvMeta `json:"conversation"`
	Events       []*Event `json:"events"`
}

// PeerConversationSyncAck acknowledges a peer.conversation_sync.
type PeerConversationSyncAck struct {
	ConversationID string `json:"conversation_id"`
	LastEventID    string `json:"last_event_id"`
}

// TaskView is a read projection of a task conversation derived from events.
// It is never persisted — it is computed on demand by the sync engine.
type TaskView struct {
	ConversationID string     `json:"conversation_id"`
	Title          string     `json:"title"`
	Assignee       string     `json:"assignee"`
	Requester      string     `json:"requester"`
	Status         TaskStatus `json:"status"`
	Summary        string     `json:"summary,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// PeerResponse is the acknowledgement sent in reply to a peer envelope.
type PeerResponse struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
