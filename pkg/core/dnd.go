package core

import (
	"context"
	"fmt"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// DNDManager handles Do-Not-Disturb state for agents: enabling/disabling DND,
// deciding whether a message should be queued, and flushing the queue on exit.
type DNDManager struct {
	store               store.Store
	hub                 *Hub
	urgentBreaksThrough bool
}

func newDNDManager(s store.Store, h *Hub, urgentBreaksThrough bool) *DNDManager {
	return &DNDManager{
		store:               s,
		hub:                 h,
		urgentBreaksThrough: urgentBreaksThrough,
	}
}

// Enable sets an agent's status to DND with the given reason.
func (d *DNDManager) Enable(ctx context.Context, agentID, reason string) error {
	if err := d.store.UpdateAgentStatus(ctx, agentID, protocol.AgentStatusDND, reason); err != nil {
		return fmt.Errorf("dnd: enable: %w", err)
	}
	return nil
}

// Disable sets the agent back to online and flushes all queued messages as
// notifications. Returns the number of messages flushed.
func (d *DNDManager) Disable(ctx context.Context, agentID string) (int, error) {
	if err := d.store.UpdateAgentStatus(ctx, agentID, protocol.AgentStatusOnline, ""); err != nil {
		return 0, fmt.Errorf("dnd: disable: %w", err)
	}

	msgs, err := d.store.DequeueMessages(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("dnd: dequeue: %w", err)
	}

	for _, msg := range msgs {
		d.hub.NotifyAgent(agentID, Notification{
			Type:    "message.new",
			Payload: msg,
		})
	}

	return len(msgs), nil
}

// ShouldQueue reports whether the message should be queued rather than
// delivered. Returns true when the agent is in DND and the message is not
// urgent (or urgentBreaksThrough is false).
func (d *DNDManager) ShouldQueue(agent *protocol.Agent, msg *protocol.Message) bool {
	if agent.Status != protocol.AgentStatusDND {
		return false
	}
	if d.urgentBreaksThrough && msg.Priority == protocol.PriorityUrgent {
		return false
	}
	return true
}

// QueuedCount returns the number of messages currently queued for the agent
// without removing them.
func (d *DNDManager) QueuedCount(ctx context.Context, agentID string) (int, error) {
	return d.store.QueuedMessageCount(ctx, agentID)
}
