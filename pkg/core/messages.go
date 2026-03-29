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

	// Use SyncEngine to create an Event and append it.
	// The SyncEngine handles delivery to both local agents and peer hubs via
	// delivery_state and ConversationSyncer.
	eng := r.hub.Sync()
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

// routeBroadcast delivers to all online local agents.
// Remote (peer hub) agents receive broadcasts via the sync engine's
// conversation delivery path when the conversation is set up with peer targets.
func (r *MessageRouter) routeBroadcast(ctx context.Context, msg *protocol.Message) {
	agents, err := r.store.ListAgents(ctx, store.AgentFilter{Status: protocol.AgentStatusOnline})
	if err != nil {
		// Fall back to local-only broadcast on store error.
		r.hub.NotifyAll(Notification{Type: "message.new", Payload: msg}, msg.From)
		return
	}

	for _, a := range agents {
		if a.AgentID == msg.From {
			continue
		}
		if a.PeerHub == "" {
			r.hub.NotifyAgent(a.AgentID, Notification{Type: "message.new", Payload: msg})
		}
	}
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
	if err := r.store.InitDeliveryTargets(ctx, conv); err != nil {
		return nil, fmt.Errorf("messages: init delivery targets: %w", err)
	}

	return conv, nil
}

// shortID generates a compact unique identifier for conversations.
func shortID() string {
	return uuid.New().String()[:8]
}
