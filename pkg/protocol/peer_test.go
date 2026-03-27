package protocol_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestPeerEnvelopeSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inner := protocol.PeerHeartbeatPayload{Timestamp: now}
	rawPayload, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner payload: %v", err)
	}

	env := protocol.PeerEnvelope{
		Method:  "heartbeat",
		ID:      "env-001",
		Version: protocol.ProtocolVersion,
		From:    "hub-a",
		Payload: rawPayload,
	}

	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var got protocol.PeerEnvelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if got.Method != env.Method {
		t.Errorf("Method: want %q got %q", env.Method, got.Method)
	}
	if got.ID != env.ID {
		t.Errorf("ID: want %q got %q", env.ID, got.ID)
	}
	if got.From != env.From {
		t.Errorf("From: want %q got %q", env.From, got.From)
	}
	if string(got.Payload) != string(env.Payload) {
		t.Errorf("Payload: want %s got %s", env.Payload, got.Payload)
	}

	// Verify the payload can be decoded back into a heartbeat
	var hb protocol.PeerHeartbeatPayload
	if err := json.Unmarshal(got.Payload, &hb); err != nil {
		t.Fatalf("unmarshal heartbeat payload: %v", err)
	}
	if !hb.Timestamp.Equal(now) {
		t.Errorf("Timestamp: want %v got %v", now, hb.Timestamp)
	}
}

func TestPeerSyncAgentsPayload(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	agents := []*protocol.Agent{
		{
			AgentID:         "agent-001",
			Username:        "marc",
			Hostname:        "devbox",
			Status:          protocol.AgentStatusOnline,
			ConnectedAt:     now,
			LastSeen:        now,
			ProtocolVersion: protocol.ProtocolVersion,
		},
	}
	p := protocol.PeerSyncAgentsPayload{Agents: agents}

	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got protocol.PeerSyncAgentsPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(got.Agents))
	}
	if got.Agents[0].AgentID != "agent-001" {
		t.Errorf("AgentID: want agent-001 got %q", got.Agents[0].AgentID)
	}
}

func TestHubIDDeterministic(t *testing.T) {
	id1 := protocol.HubID("peer-abc")
	id2 := protocol.HubID("peer-abc")
	if id1 != id2 {
		t.Errorf("HubID not deterministic: %q != %q", id1, id2)
	}
	if len(id1) != 16 {
		t.Errorf("HubID length: want 16 got %d (%q)", len(id1), id1)
	}

	different := protocol.HubID("peer-xyz")
	if id1 == different {
		t.Error("different inputs produced the same HubID")
	}
}
