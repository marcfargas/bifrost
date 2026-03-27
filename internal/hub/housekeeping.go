package hub

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/pkg/core"
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
//  2. Prune old messages.
//  3. Prune old completed/failed/rejected tasks.
//  4. Prune old closed conversations.
//  5. Prune old attachments and remove their files from disk.
func doHousekeeping(ctx context.Context, cfg *config.Config, h *core.Hub) {
	now := time.Now()
	s := h.Store()

	// 1. Close stale conversations.
	if cfg.Conversations.InactivityTimeout.Duration > 0 {
		_, _ = h.Conversations().CloseStale(ctx)
	}

	// 2. Prune old messages.
	if cfg.Retention.Messages.Duration > 0 {
		cutoff := now.Add(-cfg.Retention.Messages.Duration)
		_ = s.DeleteMessagesBefore(ctx, cutoff)
	}

	// 3. Prune old completed tasks.
	if cfg.Retention.CompletedTasks.Duration > 0 {
		cutoff := now.Add(-cfg.Retention.CompletedTasks.Duration)
		_ = s.DeleteCompletedTasksBefore(ctx, cutoff)
	}

	// 4. Prune old closed conversations.
	if cfg.Retention.Conversations.Duration > 0 {
		cutoff := now.Add(-cfg.Retention.Conversations.Duration)
		_ = s.DeleteConversationsBefore(ctx, cutoff)
	}

	// 5. Prune old attachments and remove their files from disk.
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
}
