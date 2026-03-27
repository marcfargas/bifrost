package core

import (
	"context"
	"sync"

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
	Type    string
	Payload any
}

// Hub is the central orchestrator. It wires together the persistent store,
// agent registry, and message router. Transport layers attach themselves as
// Notifiers; the Hub never imports transport packages.
type Hub struct {
	store     store.Store
	mu        sync.RWMutex
	notifiers []Notifier
	agents    *AgentRegistry
	messages  *MessageRouter
}

// NewHub creates a Hub backed by the given store and wires agent registry and
// message router to it.
func NewHub(s store.Store) *Hub {
	h := &Hub{store: s}
	h.agents = newAgentRegistry(s, h)
	h.messages = newMessageRouter(s, h)
	return h
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

// Store returns the underlying store.
func (h *Hub) Store() store.Store { return h.store }
