package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// MessageRouter handles all message routing logic: filling defaults, resolving
// conversations, persisting messages, and dispatching to recipients.
type MessageRouter struct {
	store store.Store
	hub   *Hub
}

func newMessageRouter(s store.Store, h *Hub) *MessageRouter {
	return &MessageRouter{store: s, hub: h}
}

// Send processes and routes a message. It fills in defaults (ID, timestamp,
// priority), resolves or creates a conversation, creates an Event of type
// "message", and appends it via the SyncEngine (which handles delivery to local
// agents and federation). Falls back to direct notification routing when no
// SyncEngine is configured (broadcast, channel, and federation paths).
func (r *MessageRouter) Send(ctx context.Context, msg *protocol.Message) error {
	// Fill defaults.
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	if msg.Priority == "" {
		msg.Priority = protocol.PriorityNormal
	}

	// Broadcast and channel messages don't go through conversation/event model.
	switch {
	case msg.To == "*":
		r.routeBroadcast(ctx, msg)
		return nil
	case strings.HasPrefix(msg.To, "channel:"):
		return r.routeToChannel(ctx, msg)
	}

	// Resolve or create conversation. This also validates the recipient exists.
	conv, err := r.getOrCreateConversation(ctx, msg)
	if err != nil {
		return fmt.Errorf("messages: conversation: %w", err)
	}
	msg.ConversationID = conv.ConversationID

	// If the recipient is on a peer hub, route via federation regardless of SyncEngine.
	agent, resolveErr := r.hub.Agents().Resolve(ctx, msg.To)
	if resolveErr == nil && agent.PeerHub != "" {
		fed := r.hub.Federation()
		if fed == nil {
			return fmt.Errorf("agent %s is on peer hub %s but federation is not enabled", msg.To, agent.PeerHub)
		}
		_, err := fed.ForwardMessage(ctx, agent.PeerHub, msg)
		return err
	}

	// Use SyncEngine if available: create an Event and append it.
	if eng := r.hub.Sync(); eng != nil {
		ev := &protocol.Event{
			ConversationID: conv.ConversationID,
			Type:           protocol.EventTypeMessage,
			FromAgent:      msg.From,
			Data: protocol.EventData{
				Body:        msg.Body,
				MessageType: msg.Type,
				Priority:    msg.Priority,
				InReplyTo:   msg.InReplyTo,
			},
		}
		return eng.AppendEvent(ctx, ev)
	}

	// Fallback path (no SyncEngine): persist message and route directly.
	if err := r.store.TouchConversation(ctx, conv.ConversationID); err != nil {
		return fmt.Errorf("messages: touch conversation: %w", err)
	}
	if err := r.store.SaveMessage(ctx, msg); err != nil {
		return fmt.Errorf("messages: save: %w", err)
	}

	if strings.HasPrefix(msg.To, "task:") {
		return r.routeToTaskSubscribers(ctx, msg)
	}
	return r.routeToAgent(ctx, msg)
}

// routeToChannel sends the message to all subscribers of the channel target.
func (r *MessageRouter) routeToChannel(ctx context.Context, msg *protocol.Message) error {
	subscribers, err := r.store.GetSubscribers(ctx, msg.To)
	if err != nil {
		return fmt.Errorf("messages: get channel subscribers: %w", err)
	}
	notif := Notification{Type: "message.new", Payload: msg}
	for _, agentID := range subscribers {
		r.hub.NotifyAgent(agentID, notif)
	}
	return nil
}

// routeToTaskSubscribers sends the message to all subscribers of a task target.
func (r *MessageRouter) routeToTaskSubscribers(ctx context.Context, msg *protocol.Message) error {
	subscribers, err := r.store.GetSubscribers(ctx, msg.To)
	if err != nil {
		return fmt.Errorf("messages: get task subscribers: %w", err)
	}
	notif := Notification{Type: "message.new", Payload: msg}
	for _, agentID := range subscribers {
		r.hub.NotifyAgent(agentID, notif)
	}
	return nil
}

// routeBroadcast delivers to all online agents, including remote agents on
// peer hubs (forwarded once per peer hub via the federation forwarder).
func (r *MessageRouter) routeBroadcast(ctx context.Context, msg *protocol.Message) {
	agents, err := r.store.ListAgents(ctx, store.AgentFilter{Status: protocol.AgentStatusOnline})
	if err != nil {
		// Fall back to local-only broadcast on store error.
		r.hub.NotifyAll(Notification{Type: "message.new", Payload: msg}, msg.From)
		return
	}

	// Track which peer hubs have already received a forward for this broadcast.
	peerForwarded := make(map[string]bool)

	for _, a := range agents {
		if a.AgentID == msg.From {
			continue
		}
		if a.PeerHub != "" {
			// Forward once per peer hub.
			if !peerForwarded[a.PeerHub] {
				peerForwarded[a.PeerHub] = true
				if fed := r.hub.Federation(); fed != nil {
					fed.ForwardMessage(ctx, a.PeerHub, msg) //nolint:errcheck
				}
			}
		} else {
			r.hub.NotifyAgent(a.AgentID, Notification{Type: "message.new", Payload: msg})
		}
	}
}

// routeToAgent resolves the recipient, checks DND, and either delivers or
// queues the message. If the agent is on a peer hub, the message is forwarded
// via the federation forwarder.
func (r *MessageRouter) routeToAgent(ctx context.Context, msg *protocol.Message) error {
	agent, err := r.hub.Agents().Resolve(ctx, msg.To)
	if err != nil {
		return err
	}

	// If the agent is on a peer hub, forward via federation instead of local delivery.
	if agent.PeerHub != "" {
		fed := r.hub.Federation()
		if fed == nil {
			return fmt.Errorf("agent %s is on peer hub %s but federation is not enabled", msg.To, agent.PeerHub)
		}
		_, err := fed.ForwardMessage(ctx, agent.PeerHub, msg)
		return err
	}

	// DND check: queue unless the DND manager says to deliver.
	if r.hub.DND() != nil && r.hub.DND().ShouldQueue(agent, msg) {
		if err := r.store.EnqueueMessage(ctx, agent.AgentID, msg); err != nil {
			return fmt.Errorf("messages: enqueue (dnd): %w", err)
		}
		return nil
	}

	status := r.hub.NotifyAgent(agent.AgentID, Notification{
		Type:    "message.new",
		Payload: msg,
	})

	if status == protocol.DeliveryStatusQueuedOffline {
		if err := r.store.EnqueueMessage(ctx, agent.AgentID, msg); err != nil {
			return fmt.Errorf("messages: enqueue (offline): %w", err)
		}
	}

	return nil
}

// getOrCreateConversation finds an existing open conversation between msg.From
// and msg.To, or creates a new one. If msg.ConversationID is already set, it
// trusts that value.
func (r *MessageRouter) getOrCreateConversation(ctx context.Context, msg *protocol.Message) (*protocol.Conversation, error) {
	if msg.ConversationID != "" {
		conv, err := r.store.GetConversation(ctx, msg.ConversationID)
		if err != nil {
			return nil, err
		}
		if conv != nil {
			return conv, nil
		}
	}

	// Look for an open conversation with both participants.
	open := false
	convs, err := r.store.ListConversations(ctx, store.ConversationFilter{
		Participant: msg.From,
		Closed:      &open,
	})
	if err != nil {
		return nil, err
	}
	for _, c := range convs {
		if slices.Contains(c.Participants, msg.To) {
			return c, nil
		}
	}

	// Resolve the recipient agent to validate it exists and get the canonical agent ID.
	// This also catches unknown-agent errors before creating a conversation.
	agent, err := r.hub.Agents().Resolve(ctx, msg.To)
	if err != nil {
		return nil, err
	}

	// Create a new one with a short ID.
	now := time.Now()
	conv := &protocol.Conversation{
		ConversationID: shortID(),
		Participants:   []string{msg.From, agent.AgentID},
		CreatedAt:      now,
	}
	if err := r.store.SaveConversation(ctx, conv); err != nil {
		return nil, err
	}

	// Initialize delivery targets so the SyncEngine can deliver events.
	if eng := r.hub.Sync(); eng != nil {
		if err := r.store.InitDeliveryTargets(ctx, conv); err != nil {
			return nil, fmt.Errorf("messages: init delivery targets: %w", err)
		}
	}

	return conv, nil
}

// shortID generates a compact unique identifier for conversations.
func shortID() string {
	return uuid.New().String()[:8]
}
