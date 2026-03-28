package e2e

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/federation"
	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// federatedE2E holds two hub servers connected via direct federation.
type federatedE2E struct {
	srvA, srvB *hub.Server
	socketA    string
	socketB    string
	mgrA, mgrB *federation.Manager
	dtA, dtB   *federation.DirectTransport
}

// setupFederatedE2E creates two complete hub servers (with socket listeners,
// ConnManagers, stores) and connects them via direct TCP federation.
func setupFederatedE2E(t *testing.T) *federatedE2E {
	t.Helper()
	logger := slog.Default()

	// --- Hub A ---
	dirA := t.TempDir()
	dataA := filepath.Join(dirA, "data")
	// Short socket path for macOS (104 char limit on unix socket paths).
	sockDirA, _ := os.MkdirTemp("", "bfA")
	t.Cleanup(func() { os.RemoveAll(sockDirA) })
	socketA := filepath.Join(sockDirA, "h.sock")

	cfgA := config.Defaults()
	cfgA.Hub.Local.SocketPath = socketA
	cfgA.Storage.DataDir = dataA
	cfgA.Federation.Libp2p.Enabled = false
	cfgA.Federation.MDNS.Enabled = false
	cfgA.Federation.Direct.Enabled = true
	cfgA.Federation.Direct.Listen = "127.0.0.1:0"

	srvA, err := hub.NewServer(&cfgA, logger)
	if err != nil {
		t.Fatalf("create server A: %v", err)
	}

	// Use the hub's own store for federation so agents are shared.
	stA := srvA.Hub().Store()

	mgrA := federation.NewManager(srvA.Hub(), stA, &cfgA, "hub-a", logger)
	dtA := federation.NewDirectTransport(federation.DirectTransportConfig{
		HubID: "hub-a", Listen: "127.0.0.1:0", Logger: logger,
	})
	mgrA.AddTransport(dtA)
	srvA.Hub().SetFederation(mgrA)

	// --- Hub B ---
	dirB := t.TempDir()
	dataB := filepath.Join(dirB, "data")
	sockDirB, _ := os.MkdirTemp("", "bfB")
	t.Cleanup(func() { os.RemoveAll(sockDirB) })
	socketB := filepath.Join(sockDirB, "h.sock")

	cfgB := config.Defaults()
	cfgB.Hub.Local.SocketPath = socketB
	cfgB.Storage.DataDir = dataB
	cfgB.Federation.Libp2p.Enabled = false
	cfgB.Federation.MDNS.Enabled = false
	cfgB.Federation.Direct.Enabled = true
	cfgB.Federation.Direct.Listen = "127.0.0.1:0"

	srvB, err := hub.NewServer(&cfgB, logger)
	if err != nil {
		t.Fatalf("create server B: %v", err)
	}

	stB := srvB.Hub().Store()

	mgrB := federation.NewManager(srvB.Hub(), stB, &cfgB, "hub-b", logger)
	dtB := federation.NewDirectTransport(federation.DirectTransportConfig{
		HubID: "hub-b", Listen: "127.0.0.1:0", Logger: logger,
	})
	mgrB.AddTransport(dtB)
	srvB.Hub().SetFederation(mgrB)

	// Start hub servers.
	ctx := context.Background()
	if err := srvA.Start(ctx); err != nil {
		t.Fatalf("start server A: %v", err)
	}
	if err := srvB.Start(ctx); err != nil {
		t.Fatalf("start server B: %v", err)
	}

	// Wait for sockets.
	for _, sp := range []string{socketA, socketB} {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if c, err := net.Dial("unix", sp); err == nil {
				c.Close()
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Start federation managers.
	if err := mgrA.Start(ctx); err != nil {
		t.Fatalf("start federation A: %v", err)
	}
	if err := mgrB.Start(ctx); err != nil {
		t.Fatalf("start federation B: %v", err)
	}

	t.Cleanup(func() {
		mgrA.Stop()
		mgrB.Stop()
		srvA.Stop()
		srvB.Stop()
		// Stores are owned by the servers — closed by srv.Stop().
	})

	return &federatedE2E{
		srvA: srvA, srvB: srvB,
		socketA: socketA, socketB: socketB,
		mgrA: mgrA, mgrB: mgrB,
		dtA: dtA, dtB: dtB,
	}
}

// connectPeers connects hub B to hub A via direct transport.
func (fe *federatedE2E) connectPeers(t *testing.T) {
	t.Helper()

	// Poll until dtA has a real listener address.
	var addrA string
	for range 50 {
		addrA = fe.dtA.Addr()
		if addrA != "" && addrA != "127.0.0.1:0" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if addrA == "" || addrA == "127.0.0.1:0" {
		t.Fatal("transport A did not start listening")
	}

	ctx := context.Background()
	if _, err := fe.mgrB.ConnectPeer(ctx, protocol.PeerTransportDirect, addrA); err != nil {
		t.Fatalf("connect B->A: %v", err)
	}

	// Wait for bidirectional sync to complete.
	time.Sleep(500 * time.Millisecond)
}

func TestFederatedE2E_CrossHubMessage(t *testing.T) {
	fe := setupFederatedE2E(t)

	now := time.Now()

	// Register agent on Hub A via socket.
	connA := dialHub(t, fe.socketA)
	agentA := protocol.Agent{
		AgentID:         "fed-agent-a",
		Username:        "marc",
		Hostname:        "pc-a",
		LocalPath:       "/a",
		ProjectName:     "frontend",
		DisplayName:     "marc@frontend",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}
	resultA := rpcRegister(t, connA, agentA)
	if resultA.resp.Error != nil {
		t.Fatalf("register A: %s", resultA.resp.Error.Message)
	}

	// Register agent on Hub B via socket.
	connB := dialHub(t, fe.socketB)
	agentB := protocol.Agent{
		AgentID:         "fed-agent-b",
		Username:        "bob",
		Hostname:        "pc-b",
		LocalPath:       "/b",
		ProjectName:     "backend",
		DisplayName:     "bob@backend",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}
	resultB := rpcRegister(t, connB, agentB)
	if resultB.resp.Error != nil {
		t.Fatalf("register B: %s", resultB.resp.Error.Message)
	}

	// Connect the federation peers (after registration so agents sync).
	fe.connectPeers(t)

	// Verify Hub A knows about agent-b via federation.
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	var remoteAgent *protocol.Agent
	for time.Now().Before(deadline) {
		remoteAgent, _ = fe.srvA.Hub().Store().GetAgent(ctx, "fed-agent-b")
		if remoteAgent != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if remoteAgent == nil {
		t.Fatal("hub A does not know about fed-agent-b after peering")
	}

	// Send message from A to B (cross-hub).
	msgPayload, _ := json.Marshal(protocol.Message{
		From:     "fed-agent-a",
		To:       "fed-agent-b",
		Type:     protocol.MessageTypeQuestion,
		Body:     "Cross-hub hello!",
		Priority: protocol.PriorityNormal,
	})
	sendReq := hub.RPCRequest{
		JSONRPC: "2.0",
		ID:      float64(20),
		Method:  "msg.send",
		Params:  msgPayload,
	}
	if err := connA.Send(sendReq); err != nil {
		t.Fatalf("send msg: %v", err)
	}

	// Read the msg.send response from A to confirm the RPC succeeded.
	type rpcResult struct {
		raw json.RawMessage
		err error
	}
	respCh := make(chan rpcResult, 1)
	go func() {
		var raw json.RawMessage
		err := connA.Receive(&raw)
		respCh <- rpcResult{raw, err}
	}()

	select {
	case r := <-respCh:
		if r.err != nil {
			t.Fatalf("receive send response: %v", r.err)
		}
		var resp hub.RPCResponse
		json.Unmarshal(r.raw, &resp)
		if resp.Error != nil {
			t.Fatalf("msg.send error: %s", resp.Error.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for msg.send response")
	}

	// The message was forwarded via federation. On Hub B, the agent should
	// receive a notification. Check the event arrived in B's conversation store.
	deadline = time.Now().Add(5 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		open := false
		convs, err := fe.srvB.Hub().Store().ListConversations(ctx, store.ConversationFilter{
			Participant: "fed-agent-b",
			Closed:      &open,
		})
		if err == nil {
			for _, conv := range convs {
				events, err2 := fe.srvB.Hub().Store().ListEventsSince(ctx, conv.ConversationID, "")
				if err2 != nil {
					continue
				}
				for _, ev := range events {
					if ev.Data.Body == "Cross-hub hello!" && ev.FromAgent == "fed-agent-a" {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Error("message from fed-agent-a did not arrive at Hub B's store")
	}

	t.Log("Federated cross-hub message exchange passed")
}

func TestFederatedE2E_AgentSync(t *testing.T) {
	fe := setupFederatedE2E(t)

	now := time.Now()
	ctx := context.Background()

	// Register agents on both hubs via the core API (simpler for sync test).
	fe.srvA.Hub().Agents().Register(ctx, &protocol.Agent{
		AgentID: "sync-a1", ProjectName: "svc-a",
		Username: "u", Hostname: "h", LocalPath: "/",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	})
	fe.srvA.Hub().Agents().Register(ctx, &protocol.Agent{
		AgentID: "sync-a2", ProjectName: "svc-a2",
		Username: "u", Hostname: "h", LocalPath: "/",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	})
	fe.srvB.Hub().Agents().Register(ctx, &protocol.Agent{
		AgentID: "sync-b1", ProjectName: "svc-b",
		Username: "u", Hostname: "h", LocalPath: "/",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	})

	fe.connectPeers(t)

	// Hub B should know about sync-a1 and sync-a2.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a1, _ := fe.srvB.Hub().Store().GetAgent(ctx, "sync-a1")
		a2, _ := fe.srvB.Hub().Store().GetAgent(ctx, "sync-a2")
		if a1 != nil && a2 != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	a1, _ := fe.srvB.Hub().Store().GetAgent(ctx, "sync-a1")
	a2, _ := fe.srvB.Hub().Store().GetAgent(ctx, "sync-a2")
	if a1 == nil || a2 == nil {
		t.Error("Hub B did not receive synced agents from Hub A")
	}

	// Hub A should know about sync-b1.
	b1, _ := fe.srvA.Hub().Store().GetAgent(ctx, "sync-b1")
	if b1 == nil {
		t.Error("Hub A did not receive synced agent from Hub B")
	}

	t.Log("Federated agent sync passed")
}

func TestFederatedE2E_NotificationDeliveryViaSocket(t *testing.T) {
	_, socketPath := startTestHub(t)

	now := time.Now()
	conn := dialHub(t, socketPath)

	agent := protocol.Agent{
		AgentID:         "notif-test",
		Username:        "u",
		Hostname:        "h",
		LocalPath:       "/",
		ProjectName:     "p",
		DisplayName:     "u@p",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}

	result := rpcRegister(t, conn, agent)
	if result.resp.Error != nil {
		t.Fatalf("register: %s", result.resp.Error.Message)
	}
	// Ping was captured during register — no need to drain.

	// Now manually push a custom notification via the hub's NotifyAgent.
	// This goes: Hub -> ConnManager -> transport.Conn -> socket -> client.
	// We can't call Hub() from here since we don't have the server ref.
	// Instead, we test via sending a message to ourselves which triggers a
	// notification on the same agent.
	// Actually, we already tested this in the two-client test above.
	// This test just verifies the full notification path for a single agent.

	// Send a message to this agent from itself (allowed by the protocol).
	msgPayload, _ := json.Marshal(protocol.Message{
		From:     "notif-test",
		To:       "notif-test",
		Type:     protocol.MessageTypeStatus,
		Body:     "Self-test notification",
		Priority: protocol.PriorityNormal,
	})
	sendReq := hub.RPCRequest{
		JSONRPC: "2.0",
		ID:      float64(99),
		Method:  "msg.send",
		Params:  msgPayload,
	}
	if err := conn.Send(sendReq); err != nil {
		t.Fatalf("send: %v", err)
	}

	// We should receive the event.new notification (SyncEngine delivers event.new).
	notif := drainUntilType(t, conn, "event.new", 5*time.Second)

	paramsJSON, _ := json.Marshal(notif.Params)
	var envelope struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	json.Unmarshal(paramsJSON, &envelope)

	var ev protocol.Event
	json.Unmarshal(envelope.Payload, &ev)

	if ev.Data.Body != "Self-test notification" {
		t.Errorf("body: got %q, want %q", ev.Data.Body, "Self-test notification")
	}

	t.Log("Notification delivery via socket passed")
}
