package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcfargas/bifrost/pkg/store"
)

// ChannelInfo describes a channel and its current subscriber count.
type ChannelInfo struct {
	Name            string
	SubscriberCount int
}

// ChannelManager handles channel subscription lifecycle.
type ChannelManager struct {
	store store.Store
	hub   *Hub
}

func newChannelManager(s store.Store, h *Hub) *ChannelManager {
	return &ChannelManager{store: s, hub: h}
}

// Subscribe registers agentID as a subscriber to target.
// target must start with "channel:" or "task:", have a non-empty name/id after
// the prefix, and for task: targets the task must already exist.
func (m *ChannelManager) Subscribe(ctx context.Context, agentID, target string) error {
	if err := m.validateTarget(ctx, target); err != nil {
		return err
	}
	if err := m.store.Subscribe(ctx, agentID, target); err != nil {
		return fmt.Errorf("channels: subscribe: %w", err)
	}
	return nil
}

// Unsubscribe removes agentID's subscription to target.
func (m *ChannelManager) Unsubscribe(ctx context.Context, agentID, target string) error {
	if err := m.store.Unsubscribe(ctx, agentID, target); err != nil {
		return fmt.Errorf("channels: unsubscribe: %w", err)
	}
	return nil
}

// ListChannels returns all known channels along with their subscriber counts.
func (m *ChannelManager) ListChannels(ctx context.Context) ([]ChannelInfo, error) {
	names, err := m.store.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("channels: list: %w", err)
	}
	infos := make([]ChannelInfo, 0, len(names))
	for _, name := range names {
		subs, err := m.store.GetSubscribers(ctx, "channel:"+name)
		if err != nil {
			return nil, fmt.Errorf("channels: get subscribers for %q: %w", name, err)
		}
		infos = append(infos, ChannelInfo{
			Name:            name,
			SubscriberCount: len(subs),
		})
	}
	return infos, nil
}

// GetSubscribers returns the agent IDs subscribed to target.
func (m *ChannelManager) GetSubscribers(ctx context.Context, target string) ([]string, error) {
	subs, err := m.store.GetSubscribers(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("channels: get subscribers: %w", err)
	}
	return subs, nil
}

// validateTarget ensures target has a valid prefix and non-empty name/id. For
// task targets it also confirms the task exists.
func (m *ChannelManager) validateTarget(ctx context.Context, target string) error {
	switch {
	case strings.HasPrefix(target, "channel:"):
		name := strings.TrimPrefix(target, "channel:")
		if name == "" {
			return fmt.Errorf("channels: invalid target %q: channel name must not be empty", target)
		}
		return nil

	case strings.HasPrefix(target, "task:"):
		id := strings.TrimPrefix(target, "task:")
		if id == "" {
			return fmt.Errorf("channels: invalid target %q: task id must not be empty", target)
		}
		task, err := m.store.GetTask(ctx, id)
		if err != nil {
			return fmt.Errorf("channels: look up task %q: %w", id, err)
		}
		if task == nil {
			return fmt.Errorf("channels: invalid target %q: task not found", target)
		}
		return nil

	default:
		return fmt.Errorf("channels: invalid target %q: must start with \"channel:\" or \"task:\"", target)
	}
}
