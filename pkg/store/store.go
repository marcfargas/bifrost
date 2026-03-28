package store

import (
	"context"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// QueueEntry holds per-target queue statistics returned by QueueStats/PeerQueueStats.
type QueueEntry struct {
	Target string    `json:"target"`
	Count  int       `json:"count"`
	Oldest time.Time `json:"oldest"`
}

// AgentFilter contains optional filters for listing agents.
type AgentFilter struct {
	Status  protocol.AgentStatus // zero value means no filter
	PeerHub string               // filter by peer hub; empty means no filter
}

// MessageFilter contains optional filters for listing messages.
type MessageFilter struct {
	ConversationID string
	To             string
	From           string
	Unread         bool // if true, only return unacknowledged messages
}

// TaskFilter contains optional filters for listing tasks.
type TaskFilter struct {
	ConversationID string
	Requester      string
	Assignee       string
	Status         protocol.TaskStatus // zero value means no filter
}

// ConversationFilter contains optional filters for listing conversations.
type ConversationFilter struct {
	Participant string // agent ID that must appear in participants
	TaskID      string
	Closed      *bool // nil means no filter; pointer allows explicit true/false
}

// Store defines the full persistence interface for Bifrost.
// All methods accept a context.Context as the first parameter.
type Store interface {
	// --- Agents ---

	// UpsertAgent inserts or updates an agent record. Conflict on agent_id triggers update.
	UpsertAgent(ctx context.Context, agent *protocol.Agent) error

	// GetAgent retrieves an agent by ID. Returns nil, nil when not found.
	GetAgent(ctx context.Context, agentID string) (*protocol.Agent, error)

	// ListAgents returns agents matching the filter. An empty filter returns all.
	ListAgents(ctx context.Context, filter AgentFilter) ([]*protocol.Agent, error)

	// DeleteAgent removes an agent by ID.
	DeleteAgent(ctx context.Context, agentID string) error

	// UpdateAgentStatus sets the status (and optionally dnd_reason) of an agent.
	UpdateAgentStatus(ctx context.Context, agentID string, status protocol.AgentStatus, dndReason string) error

	// TouchAgent updates last_seen to now for the given agent.
	TouchAgent(ctx context.Context, agentID string) error

	// --- Messages ---

	// SaveMessage persists a new message.
	SaveMessage(ctx context.Context, msg *protocol.Message) error

	// GetMessage retrieves a message by ID. Returns nil, nil when not found.
	GetMessage(ctx context.Context, messageID string) (*protocol.Message, error)

	// ListMessages returns messages matching the filter.
	ListMessages(ctx context.Context, filter MessageFilter) ([]*protocol.Message, error)

	// AckMessage marks a message as acknowledged.
	AckMessage(ctx context.Context, messageID string) error

	// DeleteMessagesBefore removes messages with timestamp before the cutoff.
	DeleteMessagesBefore(ctx context.Context, before time.Time) error

	// --- Conversations ---

	// SaveConversation persists a new conversation.
	SaveConversation(ctx context.Context, conv *protocol.Conversation) error

	// GetConversation retrieves a conversation by ID. Returns nil, nil when not found.
	GetConversation(ctx context.Context, conversationID string) (*protocol.Conversation, error)

	// ListConversations returns conversations matching the filter.
	ListConversations(ctx context.Context, filter ConversationFilter) ([]*protocol.Conversation, error)

	// UpdateConversation replaces the stored conversation with the provided value (full update).
	UpdateConversation(ctx context.Context, conv *protocol.Conversation) error

	// CloseConversation marks a conversation as closed with the given reason.
	CloseConversation(ctx context.Context, conversationID string, reason protocol.ConversationCloseReason) error

	// TouchConversation updates last_activity to now for the given conversation.
	TouchConversation(ctx context.Context, conversationID string) error

	// ListStaleConversations returns open conversations whose last_activity is before the cutoff.
	ListStaleConversations(ctx context.Context, before time.Time) ([]*protocol.Conversation, error)

	// DeleteConversationsBefore removes closed conversations created before the cutoff.
	DeleteConversationsBefore(ctx context.Context, before time.Time) error

	// --- Tasks ---

	// SaveTask persists a new task.
	SaveTask(ctx context.Context, task *protocol.Task) error

	// GetTask retrieves a task by ID. Returns nil, nil when not found.
	GetTask(ctx context.Context, taskID string) (*protocol.Task, error)

	// UpdateTask replaces the stored task with the provided value (full update).
	UpdateTask(ctx context.Context, task *protocol.Task) error

	// ListTasks returns tasks matching the filter.
	ListTasks(ctx context.Context, filter TaskFilter) ([]*protocol.Task, error)

	// DeleteCompletedTasksBefore removes tasks with status completed/failed/rejected
	// whose updated_at is before the cutoff.
	DeleteCompletedTasksBefore(ctx context.Context, before time.Time) error

	// --- Attachments ---

	// SaveAttachment persists attachment metadata.
	SaveAttachment(ctx context.Context, att *protocol.Attachment) error

	// GetAttachment retrieves attachment metadata by ID. Returns nil, nil when not found.
	GetAttachment(ctx context.Context, attachmentID string) (*protocol.Attachment, error)

	// ListAttachments returns attachments for the given task.
	ListAttachments(ctx context.Context, taskID string) ([]*protocol.Attachment, error)

	// DeleteAttachmentsBefore removes attachments uploaded before the cutoff and
	// returns the attachment IDs that were deleted (for external file cleanup).
	DeleteAttachmentsBefore(ctx context.Context, before time.Time) ([]string, error)

	// --- Subscriptions (pub/sub) ---

	// Subscribe registers agentID as a subscriber to target (e.g. "channel:general" or "task:<id>").
	Subscribe(ctx context.Context, agentID, target string) error

	// Unsubscribe removes a subscription.
	Unsubscribe(ctx context.Context, agentID, target string) error

	// GetSubscribers returns agent IDs subscribed to target.
	GetSubscribers(ctx context.Context, target string) ([]string, error)

	// ListChannels returns all distinct channel names (strips the "channel:" prefix).
	ListChannels(ctx context.Context) ([]string, error)

	// --- Offline message queue ---

	// EnqueueMessage adds a message to the persistent queue for a recipient agent.
	EnqueueMessage(ctx context.Context, recipientAgentID string, msg *protocol.Message) error

	// DequeueMessages atomically retrieves and deletes all queued messages for
	// the recipient agent, returning them in enqueue order.
	DequeueMessages(ctx context.Context, recipientAgentID string) ([]*protocol.Message, error)

	// QueuedMessageCount returns the number of messages currently queued for
	// the recipient agent without removing them.
	QueuedMessageCount(ctx context.Context, recipientAgentID string) (int, error)

	// --- Peers ---

	// UpsertPeer inserts or updates a peer record. Conflict on peer_id triggers update.
	UpsertPeer(ctx context.Context, peer *protocol.Peer) error

	// GetPeer retrieves a peer by ID. Returns nil, nil when not found.
	GetPeer(ctx context.Context, peerID string) (*protocol.Peer, error)

	// ListPeers returns all peers ordered by connected_at DESC.
	ListPeers(ctx context.Context) ([]*protocol.Peer, error)

	// DeletePeer removes a peer by ID.
	DeletePeer(ctx context.Context, peerID string) error

	// UpdatePeerStatus sets the status and fail_count of a peer and records last_seen.
	UpdatePeerStatus(ctx context.Context, peerID string, status protocol.PeerStatus, failCount int) error

	// TouchPeer updates last_seen to now and resets fail_count to 0 for the given peer.
	TouchPeer(ctx context.Context, peerID string) error

	// --- Federation message queue ---

	// EnqueuePeerMessage adds a peer envelope to the outbound queue for a peer hub.
	EnqueuePeerMessage(ctx context.Context, peerID string, env *protocol.PeerEnvelope) error

	// DequeuePeerMessages atomically retrieves and deletes all queued envelopes for
	// the given peer hub, returning them in enqueue order.
	DequeuePeerMessages(ctx context.Context, peerID string) ([]*protocol.PeerEnvelope, error)

	// QueueStats returns per-agent message queue counts with oldest enqueue time.
	QueueStats(ctx context.Context) ([]QueueEntry, error)

	// PeerQueueStats returns per-peer message queue counts with oldest enqueue time.
	PeerQueueStats(ctx context.Context) ([]QueueEntry, error)

	// --- Lifecycle ---

	// Close releases any resources held by the store.
	Close() error
}
