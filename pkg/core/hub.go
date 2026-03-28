package core

import (
	"context"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// Notifier is implemented by transport layers to deliver notifications to
// connected agents. Notify returns true if the agent was reachable.
type Notifier interface {
	Notify(agentID string, notification Notification) bool
}

// Notification carries an event type and arbitrary payload to an agent.
type Notification struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// FederationForwarder is the interface the hub core uses to forward messages
// to peer hubs. This avoids an import cycle with internal/federation.
type FederationForwarder interface {
	// ForwardMessage sends a message to a peer hub.
	ForwardMessage(ctx context.Context, peerID string, msg *protocol.Message) (protocol.DeliveryStatus, error)

	// ForwardTaskCreate sends a task creation to the assignee's peer hub.
	ForwardTaskCreate(ctx context.Context, peerID string, task *protocol.Task, attachments []*protocol.PeerAttachmentData) error

	// ForwardTaskUpdate sends a task update to a peer hub.
	ForwardTaskUpdate(ctx context.Context, peerID string, task *protocol.Task) error

	// BroadcastAgentStatus notifies all peers about an agent status change.
	BroadcastAgentStatus(ctx context.Context, agentID string, status protocol.AgentStatus)
}

// Hub is the central orchestrator. It wires together the persistent store,
// agent registry, and message router. Transport layers attach themselves as
// Notifiers; the Hub never imports transport packages.
type Hub struct {
	store         store.Store
	mu            sync.RWMutex
	notifiers     []Notifier
	federation    FederationForwarder // nil if federation is disabled
	agents        *AgentRegistry
	messages      *MessageRouter
	tasks         *TaskManager
	attachments   *AttachmentManager
	channels      *ChannelManager
	dnd           *DNDManager
	conversations *ConversationManager
}

// NewHub creates a Hub backed by the given store and wires agent registry,
// message router, and task manager to it.
func NewHub(s store.Store) *Hub {
	h := &Hub{store: s}
	h.agents = newAgentRegistry(s, h)
	h.messages = newMessageRouter(s, h)
	h.tasks = newTaskManager(s, h)
	h.channels = newChannelManager(s, h)
	h.dnd = newDNDManager(s, h, true)
	h.conversations = newConversationManager(s, h, 10*time.Minute)
	return h
}

// NewHubWithConfig creates a Hub that also wires an AttachmentManager backed
// by dataDir with the given maxFileSize string (e.g. "10MB"). Pass "" for no
// limit.
func NewHubWithConfig(s store.Store, dataDir, maxFileSize string) (*Hub, error) {
	h := NewHub(s)
	am, err := NewAttachmentManager(s, dataDir, maxFileSize)
	if err != nil {
		return nil, err
	}
	h.attachments = am
	return h, nil
}

// SetFederation sets the federation forwarder for cross-hub routing.
// Safe to call concurrently.
func (h *Hub) SetFederation(f FederationForwarder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.federation = f
}

// Federation returns the federation forwarder (may be nil if federation is
// not enabled).
func (h *Hub) Federation() FederationForwarder {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.federation
}

// AddNotifier registers a transport-layer notifier. Safe to call concurrently.
func (h *Hub) AddNotifier(n Notifier) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifiers = append(h.notifiers, n)
}

// NotifyAgent attempts to deliver a notification to a specific agent by trying
// each registered notifier in order. Returns Delivered on first success, or
// QueuedOffline if no notifier could reach the agent.
func (h *Hub) NotifyAgent(agentID string, notif Notification) protocol.DeliveryStatus {
	h.mu.RLock()
	notifiers := make([]Notifier, len(h.notifiers))
	copy(notifiers, h.notifiers)
	h.mu.RUnlock()

	for _, n := range notifiers {
		if n.Notify(agentID, notif) {
			return protocol.DeliveryStatusDelivered
		}
	}
	return protocol.DeliveryStatusQueuedOffline
}

// NotifyAll delivers a notification to all online agents, optionally excluding
// one (pass empty string to exclude nobody). Offline agents are silently skipped.
func (h *Hub) NotifyAll(notif Notification, excludeAgentID string) {
	agents, err := h.store.ListAgents(context.TODO(), store.AgentFilter{Status: protocol.AgentStatusOnline})
	if err != nil {
		return
	}
	for _, a := range agents {
		if a.AgentID == excludeAgentID {
			continue
		}
		h.NotifyAgent(a.AgentID, notif)
	}
}

// Agents returns the agent registry.
func (h *Hub) Agents() *AgentRegistry { return h.agents }

// Messages returns the message router.
func (h *Hub) Messages() *MessageRouter { return h.messages }

// Tasks returns the task manager.
func (h *Hub) Tasks() *TaskManager { return h.tasks }

// Attachments returns the attachment manager, or nil if not configured.
func (h *Hub) Attachments() *AttachmentManager { return h.attachments }

// Channels returns the channel manager.
func (h *Hub) Channels() *ChannelManager { return h.channels }

// DND returns the DND manager.
func (h *Hub) DND() *DNDManager { return h.dnd }

// Conversations returns the conversation manager.
func (h *Hub) Conversations() *ConversationManager { return h.conversations }

// Store returns the underlying store.
func (h *Hub) Store() store.Store { return h.store }
