package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// dndState manages the DND reminder loop lifecycle.
type dndState struct {
	mu       sync.Mutex
	active   bool
	cancel   context.CancelFunc
	interval time.Duration
}

// globalDND is the package-level DND state used by the bifrost_dnd tool handler.
var globalDND = &dndState{
	interval: 5 * time.Minute,
}

// start begins the DND reminder loop if not already active.
func (d *dndState) start(ctx context.Context, mux *hubMux, agentID string, nw *notificationWriter) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.active {
		return
	}

	loopCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.active = true

	go d.reminderLoop(loopCtx, mux, agentID, nw)
}

// stop halts the DND reminder loop.
func (d *dndState) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.active {
		return
	}

	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.active = false
}

// reminderLoop periodically checks for queued messages and emits a channel
// notification reminding the agent that messages are waiting.
func (d *dndState) reminderLoop(ctx context.Context, mux *hubMux, agentID string, nw *notificationWriter) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.sendReminder(ctx, mux, agentID, nw)
		}
	}
}

// sendReminder queries dnd.status and emits a notification if messages are queued.
func (d *dndState) sendReminder(ctx context.Context, mux *hubMux, agentID string, nw *notificationWriter) {
	resp, err := mux.rpcCall(ctx, "dnd.status", map[string]any{"agent_id": agentID})
	if err != nil {
		slog.Warn("dnd reminder: status query failed", "error", err)
		return
	}
	if resp.Error != nil {
		slog.Warn("dnd reminder: status error", "message", resp.Error.Message)
		return
	}

	// Parse result to get queued count.
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return
	}

	var status struct {
		Queued int `json:"queued"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return
	}

	if status.Queued == 0 {
		return
	}

	msg := fmt.Sprintf("[DND] You have %d queued message(s). Use bifrost_dnd to disable DND and receive them.", status.Queued)
	emitRawChannelNotification(nw, map[string]string{
		"event":    "dnd_reminder",
		"agent_id": agentID,
	}, msg)
}

// emitRawChannelNotification sends a raw notifications/claude/channel notification
// to the MCP client via the notificationWriter.
func emitRawChannelNotification(nw *notificationWriter, meta map[string]string, content string) {
	params := channelNotificationParams{
		Content: content,
		Meta:    meta,
	}
	if err := nw.writeNotification("notifications/claude/channel", params); err != nil {
		slog.Warn("dnd: failed to write channel notification", "error", err)
	}
}
