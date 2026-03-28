package e2e

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/protocol"
)

// startTestHub creates a hub.Server with a temp-dir socket and starts it.
// Returns the server and socket path. The server is stopped on test cleanup.
func startTestHub(t *testing.T) (*hub.Server, string) {
	t.Helper()

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")

	// Unix sockets have a max path length of ~104 chars on macOS.
	// t.TempDir() paths can exceed this, so use a short path for the socket.
	socketDir, err := os.MkdirTemp("", "bf")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "h.sock")

	cfg := config.Defaults()
	cfg.Hub.Local.SocketPath = socketPath
	cfg.Storage.DataDir = dataDir
	cfg.Federation.Libp2p.Enabled = false
	cfg.Federation.MDNS.Enabled = false
	cfg.Federation.Direct.Enabled = false

	srv, err := hub.NewServer(&cfg, nil)
	if err != nil {
		t.Fatalf("create hub server: %v", err)
	}

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	// Wait for socket to appear.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", socketPath)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	return srv, socketPath
}

// dialHub connects to the hub socket and returns a transport.Conn.
func dialHub(t *testing.T, socketPath string) *transport.Conn {
	t.Helper()
	raw, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	conn := transport.NewConn(raw)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// registerResult holds the RPC response and any notifications received
// before the response arrived (e.g. the ping notification sent inline
// during registration).
type registerResult struct {
	resp          hub.RPCResponse
	notifications []json.RawMessage
}

// rpcRegister sends a hub.register RPC and returns the response along with
// any notifications that arrived before the response. The hub sends a ping
// notification inline before returning the register response, so the ping
// arrives on the wire before the response.
func rpcRegister(t *testing.T, conn *transport.Conn, agent protocol.Agent) registerResult {
	t.Helper()

	agentJSON, err := json.Marshal(agent)
	if err != nil {
		t.Fatalf("marshal agent: %v", err)
	}

	req := hub.RPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "hub.register",
		Params:  agentJSON,
	}
	if err := conn.Send(req); err != nil {
		t.Fatalf("send register: %v", err)
	}

	var result registerResult
	deadline := time.After(5 * time.Second)
	for {
		type readResult struct {
			raw json.RawMessage
			err error
		}
		ch := make(chan readResult, 1)
		go func() {
			var raw json.RawMessage
			err := conn.Receive(&raw)
			ch <- readResult{raw, err}
		}()

		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("receive register: %v", r.err)
			}
			var peek struct {
				ID *json.RawMessage `json:"id"`
			}
			json.Unmarshal(r.raw, &peek)
			if peek.ID != nil {
				json.Unmarshal(r.raw, &result.resp)
				return result
			}
			// It's a notification — save it.
			result.notifications = append(result.notifications, r.raw)
		case <-deadline:
			t.Fatal("timeout waiting for register response")
			return result
		}
	}
}

// drainUntilType reads notifications until one matches the given envelope type,
// or times out.
func drainUntilType(t *testing.T, conn *transport.Conn, wantType string, timeout time.Duration) hub.RPCNotification {
	t.Helper()

	deadline := time.After(timeout)
	for {
		type result struct {
			raw json.RawMessage
			err error
		}
		ch := make(chan result, 1)
		go func() {
			var raw json.RawMessage
			err := conn.Receive(&raw)
			ch <- result{raw, err}
		}()

		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("drain read: %v", r.err)
			}

			// Check if this is a notification (no "id" field) or a response.
			var peek struct {
				ID     *json.RawMessage `json:"id"`
				Method string           `json:"method"`
				Params json.RawMessage  `json:"params"`
			}
			json.Unmarshal(r.raw, &peek)

			// Skip RPC responses (have ID).
			if peek.ID != nil {
				continue
			}

			if peek.Method == "bifrost.notification" && peek.Params != nil {
				var envelope struct {
					Type string `json:"type"`
				}
				json.Unmarshal(peek.Params, &envelope)
				if envelope.Type == wantType {
					var notif hub.RPCNotification
					json.Unmarshal(r.raw, &notif)
					return notif
				}
			}

		case <-deadline:
			t.Fatalf("timeout draining for type %q", wantType)
			return hub.RPCNotification{}
		}
	}
}

// findNotifByType scans captured raw notifications for one whose
// bifrost.notification envelope has the given type.
func findNotifByType(t *testing.T, raws []json.RawMessage, wantType string) json.RawMessage {
	t.Helper()
	for _, raw := range raws {
		var peek struct {
			Method string `json:"method"`
			Params struct {
				Type string `json:"type"`
			} `json:"params"`
		}
		json.Unmarshal(raw, &peek)
		if peek.Method == "bifrost.notification" && peek.Params.Type == wantType {
			return raw
		}
	}
	return nil
}

func TestLocalE2E_PingOnRegister(t *testing.T) {
	_, socketPath := startTestHub(t)

	conn := dialHub(t, socketPath)

	now := time.Now()
	agent := protocol.Agent{
		AgentID:         "e2e-ping-test",
		Username:        "tester",
		Hostname:        "e2e-host",
		LocalPath:       "/test",
		ProjectName:     "ping-project",
		DisplayName:     "Tester@ping-project",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}

	result := rpcRegister(t, conn, agent)
	if result.resp.Error != nil {
		t.Fatalf("register error: %s", result.resp.Error.Message)
	}

	// The ping notification is captured during registration (sent inline
	// before the response).
	pingRaw := findNotifByType(t, result.notifications, "ping")
	if pingRaw == nil {
		t.Fatal("no ping notification received during registration")
	}

	var notif struct {
		Method string `json:"method"`
		Params struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"params"`
	}
	json.Unmarshal(pingRaw, &notif)

	if notif.Method != "bifrost.notification" {
		t.Fatalf("expected bifrost.notification, got %s", notif.Method)
	}
	if notif.Params.Type != "ping" {
		t.Fatalf("expected ping type, got %s", notif.Params.Type)
	}

	var info map[string]string
	json.Unmarshal(notif.Params.Payload, &info)

	if info["message"] == "" {
		t.Error("ping message is empty")
	}
	t.Logf("Ping: %s", info["message"])
}

func TestLocalE2E_TwoClientsMessageExchange(t *testing.T) {
	_, socketPath := startTestHub(t)

	now := time.Now()

	// --- Client A ---
	connA := dialHub(t, socketPath)
	agentA := protocol.Agent{
		AgentID:         "e2e-agent-a",
		Username:        "marc",
		Hostname:        "test",
		LocalPath:       "/a",
		ProjectName:     "project-a",
		DisplayName:     "marc@project-a",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}
	resultA := rpcRegister(t, connA, agentA)
	if resultA.resp.Error != nil {
		t.Fatalf("register A: %s", resultA.resp.Error.Message)
	}
	// Ping was captured during register — no need to drain.

	// --- Client B ---
	connB := dialHub(t, socketPath)
	agentB := protocol.Agent{
		AgentID:         "e2e-agent-b",
		Username:        "bob",
		Hostname:        "test",
		LocalPath:       "/b",
		ProjectName:     "project-b",
		DisplayName:     "bob@project-b",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}
	resultB := rpcRegister(t, connB, agentB)
	if resultB.resp.Error != nil {
		t.Fatalf("register B: %s", resultB.resp.Error.Message)
	}
	// Ping was captured during register — no need to drain.

	// --- Send message from A to B ---
	msgPayload, _ := json.Marshal(protocol.Message{
		From:     "e2e-agent-a",
		To:       "e2e-agent-b",
		Type:     protocol.MessageTypeQuestion,
		Body:     "Hello from A!",
		Priority: protocol.PriorityNormal,
	})
	sendReq := hub.RPCRequest{
		JSONRPC: "2.0",
		ID:      float64(10),
		Method:  "msg.send",
		Params:  msgPayload,
	}
	if err := connA.Send(sendReq); err != nil {
		t.Fatalf("send msg.send: %v", err)
	}

	// Read send response on A (may arrive among notifications).
	// We don't need to parse the response beyond checking no error.

	// --- Read event.new on B (SyncEngine delivers event.new, not message.new) ---
	notifB := drainUntilType(t, connB, "event.new", 5*time.Second)

	paramsJSON, _ := json.Marshal(notifB.Params)
	var envelope struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	json.Unmarshal(paramsJSON, &envelope)

	if envelope.Type != "event.new" {
		t.Fatalf("expected event.new, got %s", envelope.Type)
	}

	var received protocol.Event
	if err := json.Unmarshal(envelope.Payload, &received); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}

	if received.Data.Body != "Hello from A!" {
		t.Errorf("body: got %q, want %q", received.Data.Body, "Hello from A!")
	}
	if received.FromAgent != "e2e-agent-a" {
		t.Errorf("from: got %q, want %q", received.FromAgent, "e2e-agent-a")
	}

	t.Log("Two-client message exchange passed")
}

func TestLocalE2E_BothReceivePing(t *testing.T) {
	_, socketPath := startTestHub(t)

	now := time.Now()

	connA := dialHub(t, socketPath)
	agentA := protocol.Agent{
		AgentID:         "ping-a",
		Username:        "u",
		Hostname:        "h",
		LocalPath:       "/",
		ProjectName:     "p-a",
		DisplayName:     "u@p-a",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}
	resultA := rpcRegister(t, connA, agentA)
	if resultA.resp.Error != nil {
		t.Fatalf("register A: %s", resultA.resp.Error.Message)
	}
	if findNotifByType(t, resultA.notifications, "ping") == nil {
		t.Fatal("A: no ping notification received during registration")
	}

	connB := dialHub(t, socketPath)
	agentB := protocol.Agent{
		AgentID:         "ping-b",
		Username:        "u",
		Hostname:        "h",
		LocalPath:       "/",
		ProjectName:     "p-b",
		DisplayName:     "u@p-b",
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}
	resultB := rpcRegister(t, connB, agentB)
	if resultB.resp.Error != nil {
		t.Fatalf("register B: %s", resultB.resp.Error.Message)
	}
	if findNotifByType(t, resultB.notifications, "ping") == nil {
		t.Fatal("B: no ping notification received during registration")
	}

	t.Log("Both agents received ping on connect")
}
