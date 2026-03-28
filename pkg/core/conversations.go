package core

import (
	"context"
	"fmt"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// ConversationManager handles conversation lifecycle: auto-close on inactivity,
// activity tracking, and retrieval.
type ConversationManager struct {
	store             store.Store
	hub               *Hub
	inactivityTimeout time.Duration
}

func newConversationManager(s store.Store, h *Hub, inactivityTimeout time.Duration) *ConversationManager {
	return &ConversationManager{
		store:             s,
		hub:               h,
		inactivityTimeout: inactivityTimeout,
	}
}

// CloseStale finds all open conversations whose last_activity is older than the
// configured inactivity timeout and closes them with the "inactivity" reason.
// It returns the number of conversations closed.
func (m *ConversationManager) CloseStale(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-m.inactivityTimeout)
	convs, err := m.store.ListStaleConversations(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("conversations: list stale: %w", err)
	}
	closed := 0
	for _, conv := range convs {
		if err := m.store.CloseConversation(ctx, conv.ConversationID, protocol.ConversationCloseReasonInactivity); err != nil {
			return closed, fmt.Errorf("conversations: close %s: %w", conv.ConversationID, err)
		}
		closed++
	}
	return closed, nil
}

// Touch updates the last_activity timestamp for a conversation to now.
func (m *ConversationManager) Touch(ctx context.Context, convID string) error {
	return m.store.TouchConversation(ctx, convID)
}

// List returns conversations matching the given filter.
func (m *ConversationManager) List(ctx context.Context, filter store.ConversationFilter) ([]*protocol.Conversation, error) {
	convs, err := m.store.ListConversations(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("conversations: list: %w", err)
	}
	return convs, nil
}

// Get retrieves a single conversation by ID. Returns nil, nil when not found.
func (m *ConversationManager) Get(ctx context.Context, convID string) (*protocol.Conversation, error) {
	conv, err := m.store.GetConversation(ctx, convID)
	if err != nil {
		return nil, fmt.Errorf("conversations: get %s: %w", convID, err)
	}
	return conv, nil
}

// LinkToTask marks a conversation as a task conversation by setting IsTask=true.
func (m *ConversationManager) LinkToTask(ctx context.Context, convID, _ string) error {
	conv, err := m.store.GetConversation(ctx, convID)
	if err != nil {
		return fmt.Errorf("conversations: get %s: %w", convID, err)
	}
	if conv == nil {
		return fmt.Errorf("conversations: not found: %s", convID)
	}
	conv.IsTask = true
	if err := m.store.UpdateConversation(ctx, conv); err != nil {
		return fmt.Errorf("conversations: update %s: %w", convID, err)
	}
	return nil
}
