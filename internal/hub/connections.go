package hub

import (
	"sync"

	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/core"
)

// ConnManager maps agent IDs to active transport connections.
// It implements core.Notifier.
type ConnManager struct {
	mu    sync.RWMutex
	conns map[string]*transport.Conn
}

// NewConnManager creates an empty ConnManager.
func NewConnManager() *ConnManager {
	return &ConnManager{
		conns: make(map[string]*transport.Conn),
	}
}

// Add registers conn under agentID, replacing any previous entry.
func (m *ConnManager) Add(agentID string, conn *transport.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[agentID] = conn
}

// Remove removes the connection for agentID if present.
func (m *ConnManager) Remove(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns, agentID)
}

// Get returns the connection for agentID and whether it was found.
func (m *ConnManager) Get(agentID string) (*transport.Conn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conns[agentID]
	return c, ok
}

// Count returns the number of currently registered connections.
func (m *ConnManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.conns)
}

// Notify implements core.Notifier. It sends a JSON-RPC 2.0 notification with
// method "bifrost.notification" to the agent identified by agentID. On send
// error it removes the connection and returns false. Returns false if the agent
// is not connected.
func (m *ConnManager) Notify(agentID string, notification core.Notification) bool {
	conn, ok := m.Get(agentID)
	if !ok {
		return false
	}

	msg := RPCNotification{
		JSONRPC: "2.0",
		Method:  "bifrost.notification",
		Params:  notification,
	}

	if err := conn.Send(msg); err != nil {
		m.Remove(agentID)
		return false
	}
	return true
}
