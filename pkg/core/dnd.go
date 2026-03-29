package core

import (
	"context"
	"fmt"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// DNDManager handles Do-Not-Disturb state for agents: enabling/disabling DND,
// deciding whether an event should be skipped, and flushing pending events on exit.
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

// Disable sets the agent back to online and flushes all pending events via the
// sync engine. Returns the number of conversations that had pending events.
func (d *DNDManager) Disable(ctx context.Context, agentID string) (int, error) {
	// Count pending marks BEFORE flushing so the count reflects what was queued.
	marks, err := d.store.ListPendingDelivery(ctx, "agent", agentID)
	if err != nil {
		return 0, fmt.Errorf("dnd: list pending: %w", err)
	}
	count := len(marks)

	if err := d.store.UpdateAgentStatus(ctx, agentID, protocol.AgentStatusOnline, ""); err != nil {
		return 0, fmt.Errorf("dnd: disable: %w", err)
	}
	if err := d.hub.Sync().FlushForAgent(ctx, agentID); err != nil {
		return 0, fmt.Errorf("dnd: flush: %w", err)
	}
	return count, nil
}

// ShouldQueue reports whether the event should be skipped (not notified) for
// an agent in DND. Returns true when DND is on and event is not urgent.
func (d *DNDManager) ShouldQueue(agent *protocol.Agent, ev *protocol.Event) bool {
	if agent.Status != protocol.AgentStatusDND {
		return false
	}
	if d.urgentBreaksThrough && ev.Data.Priority == protocol.PriorityUrgent {
		return false
	}
	return true
}

// QueuedCount returns the number of pending delivery marks for the agent
// (i.e. conversations with undelivered events).
func (d *DNDManager) QueuedCount(ctx context.Context, agentID string) (int, error) {
	marks, err := d.store.ListPendingDelivery(ctx, "agent", agentID)
	if err != nil {
		return 0, err
	}
	return len(marks), nil
}
