package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestPeerCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Second)
	peer := &protocol.Peer{
		PeerID:       "peer-hub-001",
		DisplayName:  "Remote Hub Alpha",
		Transport:    protocol.PeerTransportDirect,
		Address:      "10.0.0.1:7777",
		Token:        "secret-token",
		Status:       protocol.PeerStatusConnected,
		LastSeen:     now,
		ConnectedAt:  now,
		FailCount:    0,
		ProtoVersion: "1.0.0",
	}

	// Insert
	if err := s.UpsertPeer(ctx, peer); err != nil {
		t.Fatalf("UpsertPeer: %v", err)
	}

	// Read and verify fields
	got, err := s.GetPeer(ctx, "peer-hub-001")
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if got == nil {
		t.Fatal("expected peer, got nil")
	}
	if got.PeerID != peer.PeerID {
		t.Errorf("PeerID: want %q got %q", peer.PeerID, got.PeerID)
	}
	if got.DisplayName != peer.DisplayName {
		t.Errorf("DisplayName: want %q got %q", peer.DisplayName, got.DisplayName)
	}
	if got.Transport != peer.Transport {
		t.Errorf("Transport: want %q got %q", peer.Transport, got.Transport)
	}
	if got.Status != peer.Status {
		t.Errorf("Status: want %q got %q", peer.Status, got.Status)
	}
	if !got.ConnectedAt.Equal(now) {
		t.Errorf("ConnectedAt: want %v got %v", now, got.ConnectedAt)
	}
	if got.FailCount != 0 {
		t.Errorf("FailCount: want 0 got %d", got.FailCount)
	}

	// List
	peers, err := s.ListPeers(ctx)
	if err != nil {
		t.Fatalf("ListPeers: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}

	// Update status
	if err := s.UpdatePeerStatus(ctx, "peer-hub-001", protocol.PeerStatusUnreachable, 3); err != nil {
		t.Fatalf("UpdatePeerStatus: %v", err)
	}
	got, _ = s.GetPeer(ctx, "peer-hub-001")
	if got.Status != protocol.PeerStatusUnreachable {
		t.Errorf("status after update: want unreachable got %q", got.Status)
	}
	if got.FailCount != 3 {
		t.Errorf("fail_count after update: want 3 got %d", got.FailCount)
	}

	// Touch — resets fail_count to 0 and updates last_seen
	before := time.Now()
	if err := s.TouchPeer(ctx, "peer-hub-001"); err != nil {
		t.Fatalf("TouchPeer: %v", err)
	}
	got, _ = s.GetPeer(ctx, "peer-hub-001")
	if got.FailCount != 0 {
		t.Errorf("fail_count after touch: want 0 got %d", got.FailCount)
	}
	if got.LastSeen.Before(before.Add(-time.Second)) {
		t.Errorf("LastSeen not updated after touch: %v", got.LastSeen)
	}

	// Not found returns nil
	missing, err := s.GetPeer(ctx, "no-such-peer")
	if err != nil {
		t.Fatalf("GetPeer missing: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for missing peer")
	}

	// Delete
	if err := s.DeletePeer(ctx, "peer-hub-001"); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}
	got, err = s.GetPeer(ctx, "peer-hub-001")
	if err != nil {
		t.Fatalf("GetPeer after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}
