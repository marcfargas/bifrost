package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// newTestHub creates a Hub backed by a temporary SQLite database.
func newTestHub(t *testing.T) *Hub {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "bifrost_test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("newTestHub: open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return NewHub(s)
}

func TestRegisterAndResolve(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	agent := &protocol.Agent{
		AgentID:     "agent-001",
		ProjectName: "myproject",
		DisplayName: "My Agent",
		Username:    "user",
		Hostname:    "host",
		Status:      protocol.AgentStatusOffline, // Register will set online
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}

	if err := hub.Agents().Register(ctx, agent); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Resolve by alias derived from ProjectName.
	got, err := hub.Agents().Resolve(ctx, "myproject")
	if err != nil {
		t.Fatalf("Resolve by project alias: %v", err)
	}
	if got.AgentID != "agent-001" {
		t.Errorf("expected agent-001, got %s", got.AgentID)
	}

	// Resolve by exact ID.
	got2, err := hub.Agents().Resolve(ctx, "agent-001")
	if err != nil {
		t.Fatalf("Resolve by ID: %v", err)
	}
	if got2.AgentID != "agent-001" {
		t.Errorf("expected agent-001, got %s", got2.AgentID)
	}

	// Resolve with "agent:" prefix.
	got3, err := hub.Agents().Resolve(ctx, "agent:agent-001")
	if err != nil {
		t.Fatalf("Resolve with agent: prefix: %v", err)
	}
	if got3.AgentID != "agent-001" {
		t.Errorf("expected agent-001, got %s", got3.AgentID)
	}
}

func TestAliasConflictRemoval(t *testing.T) {
	ctx := context.Background()
	hub := newTestHub(t)

	a1 := &protocol.Agent{
		AgentID:     "agent-a",
		ProjectName: "shared-project",
		Username:    "user",
		Hostname:    "host",
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}
	a2 := &protocol.Agent{
		AgentID:     "agent-b",
		ProjectName: "shared-project",
		Username:    "user",
		Hostname:    "host",
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}

	if err := hub.Agents().Register(ctx, a1); err != nil {
		t.Fatalf("Register a1: %v", err)
	}
	if err := hub.Agents().Register(ctx, a2); err != nil {
		t.Fatalf("Register a2: %v", err)
	}

	// After both registrations, the alias "shared-project" is contested;
	// neither agent should hold it.
	_, err := hub.Agents().Resolve(ctx, "shared-project")
	if err == nil {
		t.Fatal("expected AgentNotFoundError for conflicted alias, got nil")
	}
	var notFound *AgentNotFoundError
	if nfe, ok := err.(*AgentNotFoundError); !ok {
		t.Fatalf("expected *AgentNotFoundError, got %T: %v", err, err)
	} else {
		notFound = nfe
	}
	if notFound.Address != "shared-project" {
		t.Errorf("AgentNotFoundError.Address = %q, want %q", notFound.Address, "shared-project")
	}
}
