package store

import (
	"context"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// --- Legacy types (removed in Task 5; kept here for transitional compilation) ---

// QueueEntry holds per-target queue statistics returned by QueueStats/PeerQueueStats.
type QueueEntry struct {
	Target string    `json:"target"`
	Count  int       `json:"count"`
	Oldest time.Time `json:"oldest"`
}

// MessageFilter contains optional filters for listing messages.
type MessageFilter struct {
	ConversationID string
	To             string
	From           string
	Unread         bool
}

// TaskFilter contains optional filters for listing tasks.
type TaskFilter struct {
	ConversationID string
	Requester      string
	Assignee       string
	Status         protocol.TaskStatus
}

// AgentFilter contains optional filters for listing agents.
type AgentFilter struct {
	Status  protocol.AgentStatus // zero value means no filter
	PeerHub string               // filter by peer hub; empty means no filter
}

// ConversationFilter contains optional filters for listing conversations.
type ConversationFilter struct {
	Participant string // agent ID that must appear in participants
	IsTask      *bool  // nil = no filter; true = tasks only; false = messages only
	Closed      *bool  // nil = no filter
	Assignee    string
	Requester   string
}

// DeliveryMark holds one row from delivery_state.
type DeliveryMark struct {
	ConversationID string
	LastEventID    string
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

	// --- Conversations ---

	// SaveConversation persists a new conversation.
	SaveConversation(ctx context.Context, conv *protocol.Conversation) error

	// GetConversation retrieves a conversation by ID. Returns nil, nil when not found.
	GetConversation(ctx context.Context, conversationID string) (*protocol.Conversation, error)

	// ListConversations returns conversations matching the filter.
	ListConversations(ctx context.Context, filter ConversationFilter) ([]*protocol.Conversation, error)

	// CloseConversation marks a conversation as closed with the given reason.
	CloseConversation(ctx context.Context, conversationID string, reason protocol.ConversationCloseReason) error

	// --- Events ---

	// AppendEvent persists a new event to a conversation's stream.
	AppendEvent(ctx context.Context, ev *protocol.Event) error

	// GetEvent retrieves an event by ID. Returns nil, nil when not found.
	GetEvent(ctx context.Context, eventID string) (*protocol.Event, error)

	// ListEventsSince returns events in a conversation after (exclusive) afterEventID,
	// ordered by timestamp ascending. Pass "" to get all events from the start.
	ListEventsSince(ctx context.Context, conversationID, afterEventID string) ([]*protocol.Event, error)

	// LatestStatusEvent returns the most recent status event for a conversation,
	// or nil if none exists.
	LatestStatusEvent(ctx context.Context, conversationID string) (*protocol.Event, error)

	// --- Delivery State ---

	// GetDeliveryMark returns the last delivered event ID for a target in a conversation.
	// Returns "", nil when no mark exists (meaning start from the beginning).
	GetDeliveryMark(ctx context.Context, targetType, targetID, conversationID string) (string, error)

	// SetDeliveryMark upserts the last delivered event ID for a target in a conversation.
	SetDeliveryMark(ctx context.Context, targetType, targetID, conversationID, lastEventID string) error

	// ListPendingDelivery returns all (conversationID, lastEventID) pairs where the
	// target has undelivered events (i.e. delivery_state rows for this target).
	ListPendingDelivery(ctx context.Context, targetType, targetID string) ([]DeliveryMark, error)

	// InitDeliveryTargets ensures delivery_state rows exist for all participants
	// of the conversation, with lastEventID="" for any that don't yet have a row.
	InitDeliveryTargets(ctx context.Context, conv *protocol.Conversation) error

	// --- Subscriptions (pub/sub) ---

	// Subscribe registers agentID as a subscriber to target (e.g. "channel:general").
	Subscribe(ctx context.Context, agentID, target string) error

	// Unsubscribe removes a subscription.
	Unsubscribe(ctx context.Context, agentID, target string) error

	// GetSubscribers returns agent IDs subscribed to target.
	GetSubscribers(ctx context.Context, target string) ([]string, error)

	// ListChannels returns all distinct channel names (strips the "channel:" prefix).
	ListChannels(ctx context.Context) ([]string, error)

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

	// --- Legacy: Messages (removed in Task 5) ---

	SaveMessage(ctx context.Context, msg *protocol.Message) error
	GetMessage(ctx context.Context, messageID string) (*protocol.Message, error)
	ListMessages(ctx context.Context, filter MessageFilter) ([]*protocol.Message, error)
	AckMessage(ctx context.Context, messageID string) error
	DeleteMessagesBefore(ctx context.Context, before time.Time) error

	// --- Legacy: Tasks (removed in Task 5) ---

	SaveTask(ctx context.Context, task *protocol.Task) error
	GetTask(ctx context.Context, taskID string) (*protocol.Task, error)
	UpdateTask(ctx context.Context, task *protocol.Task) error
	ListTasks(ctx context.Context, filter TaskFilter) ([]*protocol.Task, error)
	DeleteCompletedTasksBefore(ctx context.Context, before time.Time) error

	// --- Legacy: Attachments (removed in Task 5) ---

	SaveAttachment(ctx context.Context, att *protocol.Attachment) error
	GetAttachment(ctx context.Context, attachmentID string) (*protocol.Attachment, error)
	ListAttachments(ctx context.Context, taskID string) ([]*protocol.Attachment, error)
	DeleteAttachmentsBefore(ctx context.Context, before time.Time) ([]string, error)

	// --- Legacy: Conversation mutations (removed in Task 5) ---

	UpdateConversation(ctx context.Context, conv *protocol.Conversation) error
	TouchConversation(ctx context.Context, conversationID string) error
	ListStaleConversations(ctx context.Context, before time.Time) ([]*protocol.Conversation, error)
	DeleteConversationsBefore(ctx context.Context, before time.Time) error

	// --- Legacy: Offline message queue (removed in Task 5) ---

	EnqueueMessage(ctx context.Context, recipientAgentID string, msg *protocol.Message) error
	DequeueMessages(ctx context.Context, recipientAgentID string) ([]*protocol.Message, error)
	QueuedMessageCount(ctx context.Context, recipientAgentID string) (int, error)

	// --- Legacy: Federation message queue (removed in Task 5) ---

	EnqueuePeerMessage(ctx context.Context, peerID string, env *protocol.PeerEnvelope) error
	DequeuePeerMessages(ctx context.Context, peerID string) ([]*protocol.PeerEnvelope, error)
	QueueStats(ctx context.Context) ([]QueueEntry, error)
	PeerQueueStats(ctx context.Context) ([]QueueEntry, error)

	// --- Lifecycle ---

	// Close releases any resources held by the store.
	Close() error
}
