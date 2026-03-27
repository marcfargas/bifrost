package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// AgentNotFoundError is returned by Resolve when no agent matches the address.
type AgentNotFoundError struct {
	Address   string
	Available []string
}

func (e *AgentNotFoundError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("agent not found: %q (no agents registered)", e.Address)
	}
	return fmt.Sprintf("agent not found: %q (available: %s)", e.Address, strings.Join(e.Available, ", "))
}

// AgentRegistry manages agent lifecycle: registration, deregistration, and
// alias-based address resolution.
type AgentRegistry struct {
	store store.Store
	hub   *Hub
}

func newAgentRegistry(s store.Store, h *Hub) *AgentRegistry {
	return &AgentRegistry{store: s, hub: h}
}

// Register marks an agent online, computes its aliases, persists it, then
// recomputes aliases across all agents to remove conflicts, notifies other
// online agents, and flushes any queued messages for this agent.
func (r *AgentRegistry) Register(ctx context.Context, agent *protocol.Agent) error {
	now := time.Now()
	agent.Status = protocol.AgentStatusOnline
	agent.ConnectedAt = now
	agent.LastSeen = now
	agent.Aliases = r.computeAliases(ctx, agent)

	if err := r.store.UpsertAgent(ctx, agent); err != nil {
		return fmt.Errorf("agents: upsert: %w", err)
	}

	if err := r.recomputeAllAliases(ctx); err != nil {
		return fmt.Errorf("agents: recompute aliases: %w", err)
	}

	// Notify all other online agents.
	r.hub.NotifyAll(Notification{
		Type:    "agent.registered",
		Payload: agent,
	}, agent.AgentID)

	// Broadcast status to federated peers.
	if fed := r.hub.Federation(); fed != nil {
		fed.BroadcastAgentStatus(ctx, agent.AgentID, protocol.AgentStatusOnline)
	}

	// Flush any queued messages.
	r.flushQueue(ctx, agent.AgentID)

	return nil
}

// Deregister marks an agent offline and notifies other online agents.
func (r *AgentRegistry) Deregister(ctx context.Context, agentID string) error {
	if err := r.store.UpdateAgentStatus(ctx, agentID, protocol.AgentStatusOffline, ""); err != nil {
		return fmt.Errorf("agents: deregister: %w", err)
	}

	r.hub.NotifyAll(Notification{
		Type:    "agent.deregistered",
		Payload: map[string]string{"agent_id": agentID},
	}, agentID)

	// Broadcast status to federated peers.
	if fed := r.hub.Federation(); fed != nil {
		fed.BroadcastAgentStatus(ctx, agentID, protocol.AgentStatusOffline)
	}

	return nil
}

// Resolve finds an agent by alias, agent_id, or returns an error with available agents.
// Resolution order: exact agent_id match first, then case-insensitive alias match.
// If a name matches exactly one agent (local or remote), that agent is returned.
// If a name matches multiple agents, it is treated as ambiguous (aliases should
// have been de-duplicated by recomputeAllAliases, but this handles edge cases).
func (r *AgentRegistry) Resolve(ctx context.Context, address string) (*protocol.Agent, error) {
	addr := strings.TrimPrefix(address, "agent:")

	// Exact ID match.
	agent, err := r.store.GetAgent(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("agents: resolve: %w", err)
	}
	if agent != nil {
		return agent, nil
	}

	// Case-insensitive alias search across all agents (local + remote).
	all, err := r.store.ListAgents(ctx, store.AgentFilter{})
	if err != nil {
		return nil, fmt.Errorf("agents: list for resolve: %w", err)
	}

	lower := strings.ToLower(addr)
	var localMatches []*protocol.Agent
	var remoteMatches []*protocol.Agent

	for _, a := range all {
		for _, alias := range a.Aliases {
			if strings.ToLower(alias) == lower {
				if a.PeerHub == "" {
					localMatches = append(localMatches, a)
				} else {
					remoteMatches = append(remoteMatches, a)
				}
				break
			}
		}
	}

	// Exactly one match total — return it.
	totalMatches := len(localMatches) + len(remoteMatches)
	if totalMatches == 1 {
		if len(localMatches) == 1 {
			return localMatches[0], nil
		}
		return remoteMatches[0], nil
	}

	// Build available list for the error (0 matches or ambiguous).
	available := make([]string, 0, len(all))
	for _, a := range all {
		available = append(available, a.AgentID)
		available = append(available, a.Aliases...)
	}

	return nil, &AgentNotFoundError{Address: address, Available: available}
}

// computeAliases derives candidate aliases from an agent's ProjectName and
// DisplayName. Aliases are lower-cased, space-collapsed strings.
func (r *AgentRegistry) computeAliases(_ context.Context, agent *protocol.Agent) []string {
	seen := map[string]struct{}{}
	var aliases []string

	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.Join(strings.Fields(s), "-")
		if s == "" || s == agent.AgentID {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		aliases = append(aliases, s)
	}

	add(agent.ProjectName)
	add(agent.DisplayName)

	return aliases
}

// recomputeAllAliases scans all agents and removes any alias that appears on
// more than one agent (conflict resolution). The updated agents are re-persisted.
func (r *AgentRegistry) recomputeAllAliases(ctx context.Context) error {
	all, err := r.store.ListAgents(ctx, store.AgentFilter{})
	if err != nil {
		return err
	}

	// Count how many agents claim each alias.
	counts := map[string]int{}
	for _, a := range all {
		for _, alias := range a.Aliases {
			counts[alias]++
		}
	}

	// For any agent that has a conflicting alias, rewrite its aliases list and
	// persist. We recompute from the canonical sources to keep things clean.
	for _, a := range all {
		original := a.Aliases
		fresh := r.computeAliases(ctx, a)

		// Keep only aliases that are unique across all agents.
		var kept []string
		for _, alias := range fresh {
			if counts[alias] == 1 {
				kept = append(kept, alias)
			}
		}

		// Check whether the alias list actually changed before writing.
		changed := len(kept) != len(original)
		if !changed {
			for i, k := range kept {
				if i >= len(original) || original[i] != k {
					changed = true
					break
				}
			}
		}

		if changed {
			a.Aliases = kept
			if err := r.store.UpsertAgent(ctx, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecomputeAliases recomputes and deduplicates aliases across all agents (local + federated).
// This is the public API called by the federation manager after syncing remote agents.
func (r *AgentRegistry) RecomputeAliases(ctx context.Context) {
	r.recomputeAllAliases(ctx)
}

// flushQueue dequeues all persisted messages for agentID and delivers them.
func (r *AgentRegistry) flushQueue(ctx context.Context, agentID string) {
	msgs, err := r.store.DequeueMessages(ctx, agentID)
	if err != nil {
		return
	}
	for _, msg := range msgs {
		r.hub.NotifyAgent(agentID, Notification{
			Type:    "message.new",
			Payload: msg,
		})
	}
}
