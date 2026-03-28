package shim

import (
	"context"
	"testing"
	"time"
)

func TestDNDState_StartStop(t *testing.T) {
	d := &dndState{
		interval: 50 * time.Millisecond,
	}

	// Initially not active.
	if d.active {
		t.Fatal("expected inactive on creation")
	}

	// Stop on inactive state should be a no-op (no panic).
	d.stop()

	ctx := context.Background()
	// We pass nil mux and agentID — the reminder loop will try rpcCall which
	// would panic on nil mux, but we cancel before the first tick fires.
	// Use a very long interval to guarantee the ticker doesn't fire.
	d.interval = 1 * time.Hour
	d.start(ctx, nil, "test-agent", nil)

	if !d.active {
		t.Fatal("expected active after start")
	}

	// Starting again is a no-op — should not panic or reset.
	d.start(ctx, nil, "test-agent", nil)
	if !d.active {
		t.Fatal("expected still active after redundant start")
	}

	d.stop()
	if d.active {
		t.Fatal("expected inactive after stop")
	}

	// Double stop is safe.
	d.stop()
	if d.active {
		t.Fatal("expected still inactive after double stop")
	}
}

func TestDNDState_ContextCancellation(t *testing.T) {
	d := &dndState{
		interval: 1 * time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.start(ctx, nil, "test-agent", nil)

	if !d.active {
		t.Fatal("expected active")
	}

	// Cancelling the parent context should make the reminderLoop exit.
	cancel()
	time.Sleep(50 * time.Millisecond)

	// The dndState.active flag is only set by stop(), not by context cancellation.
	// This tests that stop() works correctly even after context is cancelled.
	d.stop()
	if d.active {
		t.Fatal("expected inactive after stop")
	}
}

func TestEmitRawChannelNotification(t *testing.T) {
	nw, buf := testNotificationWriter()

	emitRawChannelNotification(nw, map[string]string{
		"event":    "dnd_reminder",
		"agent_id": "agent-x",
	}, "You have 3 queued message(s)")

	if buf.Len() == 0 {
		t.Fatal("expected output from emitRawChannelNotification")
	}

	_, cp := parseChannelNotification(t, buf.Bytes())

	if cp.Channel != "bifrost" {
		t.Errorf("channel: got %q, want %q", cp.Channel, "bifrost")
	}
	if cp.Message != "You have 3 queued message(s)" {
		t.Errorf("message: got %q", cp.Message)
	}
	if cp.Meta["event"] != "dnd_reminder" {
		t.Errorf("meta.event: got %q", cp.Meta["event"])
	}
	if cp.Meta["agent_id"] != "agent-x" {
		t.Errorf("meta.agent_id: got %q", cp.Meta["agent_id"])
	}
}
