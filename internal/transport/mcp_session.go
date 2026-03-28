// Package transport provides connection management for the Bifrost hub.
//
// This file implements per-client session state for MCP HTTP connections.
package transport

import (
	"sync"
	"time"
)

// MCPSession tracks the state of a single MCP HTTP client connected to the hub.
type MCPSession struct {
	SessionID   string
	AgentID     string
	ProjectName string
	Hostname    string
	Username    string
	LocalPath   string
	CreatedAt   time.Time
	LastSeen    time.Time
	Registered  bool // true after bifrost_whoami first call
}

// MCPSessionStore manages active MCP HTTP sessions.
type MCPSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*MCPSession // session ID -> session
	byAgent  map[string]string      // agent ID -> session ID
}

// NewMCPSessionStore creates a new session store.
func NewMCPSessionStore() *MCPSessionStore {
	return &MCPSessionStore{
		sessions: make(map[string]*MCPSession),
		byAgent:  make(map[string]string),
	}
}

// Create adds a new session to the store. Returns the session.
func (s *MCPSessionStore) Create(sessionID string) *MCPSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := &MCPSession{
		SessionID: sessionID,
		CreatedAt: time.Now(),
		LastSeen:  time.Now(),
	}
	s.sessions[sessionID] = sess
	return sess
}

// Get retrieves a session by ID. Returns nil if not found.
func (s *MCPSessionStore) Get(sessionID string) *MCPSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID]
}

// GetByAgent retrieves the session ID for a given agent ID.
func (s *MCPSessionStore) GetByAgent(agentID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sid, ok := s.byAgent[agentID]
	return sid, ok
}

// Register binds an agent ID to a session. Called during the first tool
// invocation that provides project identity info.
func (s *MCPSessionStore) Register(sessionID, agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	sess.AgentID = agentID
	sess.Registered = true
	s.byAgent[agentID] = sessionID
}

// Touch updates the last-seen timestamp for a session.
func (s *MCPSessionStore) Touch(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sessionID]; ok {
		sess.LastSeen = time.Now()
	}
}

// Remove deletes a session and its agent mapping.
func (s *MCPSessionStore) Remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	if sess.AgentID != "" {
		delete(s.byAgent, sess.AgentID)
	}
	delete(s.sessions, sessionID)
}

// ActiveSessions returns the count of active sessions.
func (s *MCPSessionStore) ActiveSessions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// AllSessions returns a snapshot of all sessions.
func (s *MCPSessionStore) AllSessions() []*MCPSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*MCPSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, sess)
	}
	return result
}
