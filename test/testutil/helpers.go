package testutil

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// TestHub creates a Hub backed by a SQLite database in t's temp directory.
// The database (and temp directory) are cleaned up automatically when the test ends.
func TestHub(t *testing.T) *core.Hub {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "bifrost_test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("testutil.TestHub: open sqlite: %v", err)
	}
	hub := core.NewHub(s)
	t.Cleanup(func() {
		if err := hub.Store().Close(); err != nil {
			t.Logf("testutil.TestHub: close store: %v", err)
		}
	})
	return hub
}

// TestAgent creates a protocol.Agent with deterministic fields for the given
// project name. The AgentID is derived from the project name for predictability.
func TestAgent(name string) *protocol.Agent {
	return &protocol.Agent{
		AgentID:         "agent-" + name,
		ProjectName:     name,
		DisplayName:     name + " agent",
		Username:        "testuser",
		Hostname:        "testhost",
		LocalPath:       "/tmp/" + name,
		ProtocolVersion: "1.0",
	}
}

// CollectingNotifier implements core.Notifier and captures every notification
// delivered to it, keyed by agent ID.
type CollectingNotifier struct {
	mu            sync.Mutex
	notifications map[string][]core.Notification
}

// NewCollectingNotifier returns an initialised CollectingNotifier.
func NewCollectingNotifier() *CollectingNotifier {
	return &CollectingNotifier{
		notifications: make(map[string][]core.Notification),
	}
}

// Notify always returns true (agent reachable) and stores the notification.
func (c *CollectingNotifier) Notify(agentID string, notif core.Notification) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications[agentID] = append(c.notifications[agentID], notif)
	return true
}

// CountFor returns the total number of notifications received for agentID.
func (c *CollectingNotifier) CountFor(agentID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.notifications[agentID])
}

// MessagesFor returns all Message payloads from notifications of type
// "message.new" delivered to agentID.
func (c *CollectingNotifier) MessagesFor(agentID string) []*protocol.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	var msgs []*protocol.Message
	for _, notif := range c.notifications[agentID] {
		if notif.Type != "message.new" {
			continue
		}
		if msg, ok := notif.Payload.(*protocol.Message); ok {
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

// NotificationsOfType returns all notifications of the given type for agentID.
func (c *CollectingNotifier) NotificationsOfType(agentID, notifType string) []core.Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []core.Notification
	for _, n := range c.notifications[agentID] {
		if n.Type == notifType {
			out = append(out, n)
		}
	}
	return out
}

// Clear resets all captured notifications, useful between logical test phases.
func (c *CollectingNotifier) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications = make(map[string][]core.Notification)
}
