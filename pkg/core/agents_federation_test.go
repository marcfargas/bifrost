package core

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestFederatedAliasResolution(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	now := time.Now()

	// Register local agent.
	local := &protocol.Agent{
		AgentID:         "local-1",
		ProjectName:     "frontend",
		Username:        "marc",
		Hostname:        "myhost",
		LocalPath:       "/dev/fe",
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := hub.Agents().Register(ctx, local); err != nil {
		t.Fatalf("register local: %v", err)
	}

	// Insert remote agent directly into the store (simulating federation sync).
	remote := &protocol.Agent{
		AgentID:         "remote-1",
		ProjectName:     "backend",
		PeerHub:         "peer-bob",
		Aliases:         []string{"backend"},
		Username:        "bob",
		Hostname:        "bob-pc",
		LocalPath:       "/dev/api",
		Status:          protocol.AgentStatusOnline,
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := hub.Store().UpsertAgent(ctx, remote); err != nil {
		t.Fatalf("upsert remote: %v", err)
	}

	// Resolve local by alias.
	got, err := hub.Agents().Resolve(ctx, "frontend")
	if err != nil {
		t.Fatalf("resolve local: %v", err)
	}
	if got.AgentID != "local-1" {
		t.Errorf("expected local-1, got %s", got.AgentID)
	}

	// Resolve remote by alias.
	got, err = hub.Agents().Resolve(ctx, "backend")
	if err != nil {
		t.Fatalf("resolve remote: %v", err)
	}
	if got.AgentID != "remote-1" {
		t.Errorf("expected remote-1, got %s", got.AgentID)
	}

	// Resolve by agent_id works for both.
	got, err = hub.Agents().Resolve(ctx, "remote-1")
	if err != nil {
		t.Fatalf("resolve by ID: %v", err)
	}
	if got.PeerHub != "peer-bob" {
		t.Errorf("expected peer_hub peer-bob, got %s", got.PeerHub)
	}
}

func TestFederatedAliasConflictRemoval(t *testing.T) {
	hub := newTestHub(t)
	ctx := context.Background()
	now := time.Now()

	// Local agent with name "api".
	local := &protocol.Agent{
		AgentID:         "local-api",
		ProjectName:     "api",
		Username:        "marc",
		Hostname:        "myhost",
		LocalPath:       "/dev/api",
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := hub.Agents().Register(ctx, local); err != nil {
		t.Fatalf("register local: %v", err)
	}

	// Remote agent also named "api".
	remote := &protocol.Agent{
		AgentID:         "remote-api",
		ProjectName:     "api",
		PeerHub:         "peer-bob",
		Aliases:         []string{"api"},
		Username:        "bob",
		Hostname:        "bob-pc",
		LocalPath:       "/dev/api",
		Status:          protocol.AgentStatusOnline,
		ConnectedAt:     now,
		LastSeen:        now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err := hub.Store().UpsertAgent(ctx, remote); err != nil {
		t.Fatalf("upsert remote: %v", err)
	}

	// Recompute aliases -- "api" should be removed from both.
	hub.Agents().RecomputeAliases(ctx)

	// Resolving "api" should fail (ambiguous, alias removed).
	_, err := hub.Agents().Resolve(ctx, "api")
	if err == nil {
		t.Error("expected error for ambiguous alias 'api'")
	}
	anf, ok := err.(*AgentNotFoundError)
	if !ok {
		t.Fatalf("expected AgentNotFoundError, got %T", err)
	}
	if len(anf.Available) < 2 {
		t.Errorf("expected at least 2 available entries, got %d", len(anf.Available))
	}

	// Resolving by agent_id still works.
	got, err := hub.Agents().Resolve(ctx, "local-api")
	if err != nil {
		t.Fatalf("resolve by ID: %v", err)
	}
	if got.AgentID != "local-api" {
		t.Errorf("expected local-api, got %s", got.AgentID)
	}

	got, err = hub.Agents().Resolve(ctx, "remote-api")
	if err != nil {
		t.Fatalf("resolve remote by ID: %v", err)
	}
	if got.PeerHub != "peer-bob" {
		t.Errorf("expected peer_hub peer-bob, got %s", got.PeerHub)
	}
}
