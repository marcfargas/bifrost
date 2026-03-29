package hub

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// runHousekeeping runs periodic maintenance tasks until ctx is cancelled.
func runHousekeeping(ctx context.Context, cfg *config.Config, h *core.Hub) {
	interval := cfg.Hub.HousekeepingInterval.Duration
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			doHousekeeping(ctx, cfg, h)
		}
	}
}

// doHousekeeping performs one round of maintenance:
//  1. Close stale conversations via ConversationManager.
//  2. Prune old attachments and remove their files from disk.
//  3. Prune stale offline agents.
func doHousekeeping(ctx context.Context, cfg *config.Config, h *core.Hub) {
	now := time.Now()
	s := h.Store()

	// 1. Close stale conversations.
	if cfg.Conversations.InactivityTimeout.Duration > 0 {
		_, _ = h.Conversations().CloseStale(ctx)
	}

	// 2. Prune old attachments and remove their files from disk.
	if cfg.Retention.Attachments.Duration > 0 {
		cutoff := now.Add(-cfg.Retention.Attachments.Duration)
		dataDir := cfg.Storage.DataDir
		if dataDir == "" {
			dataDir = config.DataDir()
		}
		attachmentsDir := filepath.Join(dataDir, "attachments")

		deleted, err := s.DeleteAttachmentsBefore(ctx, cutoff)
		if err == nil {
			for _, id := range deleted {
				_ = os.Remove(filepath.Join(attachmentsDir, id))
			}
		}
	}

	// 3. Prune stale offline agents.
	if cfg.Retention.AgentOfflineTTL.Duration > 0 {
		pruneOfflineAgents(ctx, cfg, h, now)
	}
}

// pruneOfflineAgents deletes agents whose status is "offline" and whose
// last_seen timestamp is older than the configured agent_offline_ttl.
func pruneOfflineAgents(ctx context.Context, cfg *config.Config, h *core.Hub, now time.Time) {
	cutoff := now.Add(-cfg.Retention.AgentOfflineTTL.Duration)
	s := h.Store()

	agents, err := s.ListAgents(ctx, store.AgentFilter{Status: protocol.AgentStatusOffline})
	if err != nil {
		slog.Error("housekeeping: list offline agents", "err", err)
		return
	}

	for _, agent := range agents {
		if agent.LastSeen.IsZero() || agent.LastSeen.After(cutoff) {
			continue
		}
		if err := s.DeleteAgent(ctx, agent.AgentID); err != nil {
			slog.Error("housekeeping: delete stale agent", "agent_id", agent.AgentID, "err", err)
			continue
		}
		slog.Info("housekeeping: pruned stale offline agent",
			"agent_id", agent.AgentID,
			"display_name", agent.DisplayName,
			"last_seen", agent.LastSeen,
		)
	}
}
