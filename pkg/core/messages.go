package core

import (
	"context"
	"fmt"
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
// priority), resolves or creates a conversation, persists the message, then
// routes based on the To field.
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

	// Resolve or create conversation.
	conv, err := r.getOrCreateConversation(ctx, msg)
	if err != nil {
		return fmt.Errorf("messages: conversation: %w", err)
	}
	msg.ConversationID = conv.ConversationID

	// Touch conversation activity.
	if err := r.store.TouchConversation(ctx, conv.ConversationID); err != nil {
		return fmt.Errorf("messages: touch conversation: %w", err)
	}

	// Persist message.
	if err := r.store.SaveMessage(ctx, msg); err != nil {
		return fmt.Errorf("messages: save: %w", err)
	}

	// Route.
	switch {
	case strings.HasPrefix(msg.To, "channel:"):
		return r.routeToChannel(ctx, msg)
	case strings.HasPrefix(msg.To, "task:"):
		return r.routeToTaskSubscribers(ctx, msg)
	case msg.To == "*":
		r.routeBroadcast(ctx, msg)
		return nil
	default:
		return r.routeToAgent(ctx, msg)
	}
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

// routeBroadcast delivers to all online agents.
func (r *MessageRouter) routeBroadcast(ctx context.Context, msg *protocol.Message) {
	r.hub.NotifyAll(Notification{Type: "message.new", Payload: msg}, msg.From)
}

// routeToAgent resolves the recipient, checks DND, and either delivers or
// queues the message.
func (r *MessageRouter) routeToAgent(ctx context.Context, msg *protocol.Message) error {
	agent, err := r.hub.Agents().Resolve(ctx, msg.To)
	if err != nil {
		return err
	}

	// DND check: queue unless urgent.
	if agent.Status == protocol.AgentStatusDND && msg.Priority != protocol.PriorityUrgent {
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
		for _, p := range c.Participants {
			if p == msg.To {
				return c, nil
			}
		}
	}

	// Create a new one with a short ID.
	now := time.Now()
	conv := &protocol.Conversation{
		ConversationID: shortID(),
		Participants:   []string{msg.From, msg.To},
		CreatedAt:      now,
		LastActivity:   now,
	}
	if err := r.store.SaveConversation(ctx, conv); err != nil {
		return nil, err
	}
	return conv, nil
}

// shortID generates a compact unique identifier for conversations.
func shortID() string {
	return uuid.New().String()[:8]
}
