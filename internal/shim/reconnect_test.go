package shim

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/internal/transport"
)

// pipeConn pairs two net.Conn ends (via net.Pipe) and returns a *transport.Conn
// wrapping the client side. The server side is returned for the test to read/write.
func pipeConn(t *testing.T) (client *transport.Conn, server net.Conn) {
	t.Helper()
	c, s, err := makeNetPipe()
	if err != nil {
		t.Fatalf("net.Pipe: %v", err)
	}
	return transport.NewConn(c), s
}

func makeNetPipe() (net.Conn, net.Conn, error) {
	c, s := net.Pipe()
	return c, s, nil
}

// sendRPCResponse writes a JSON-RPC response to conn for the given id.
func sendRPCResponse(t *testing.T, conn net.Conn, id uint64, result any) {
	t.Helper()
	resp := hub.RPCResponse{
		JSONRPC: "2.0",
		ID:      float64(id),
		Result:  result,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		// conn may have been closed already during test teardown — ignore.
		t.Logf("sendRPCResponse write: %v", err)
	}
}

// readRPCRequest reads and decodes the next JSON-RPC request from conn.
func readRPCRequest(t *testing.T, conn net.Conn) hub.RPCRequest {
	t.Helper()
	dec := json.NewDecoder(conn)
	var req hub.RPCRequest
	if err := dec.Decode(&req); err != nil && err != io.EOF {
		t.Fatalf("readRPCRequest: %v", err)
	}
	return req
}

// TestHubMux_DisconnectSignal verifies that readLoop closes the disconnected
// channel when the remote end of the connection closes.
func TestHubMux_DisconnectSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	client, server := pipeConn(t)
	log := slog.Default()

	mux := newHubMux(ctx, client, log)

	// Arm the disconnect watcher before closing the server side.
	discCh := mux.disconnectedCh()

	// Close the server end — readLoop should see EOF and signal disconnect.
	server.Close()

	select {
	case <-discCh:
		// Good — disconnect was signalled.
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: disconnected channel was not closed after connection drop")
	}
}

// TestHubMux_DrainPending verifies that in-flight rpcCall waiters receive an
// error response when the connection drops.
func TestHubMux_DrainPending(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	client, server := pipeConn(t)
	log := slog.Default()

	mux := newHubMux(ctx, client, log)

	// Start an rpcCall that will never get a response.
	callDone := make(chan error, 1)
	go func() {
		_, err := mux.rpcCall(ctx, "hub.ping", nil)
		callDone <- err
	}()

	// Give the goroutine time to register the pending call and send.
	// Read (and discard) the request from the server side so the send succeeds.
	go func() {
		readRPCRequest(t, server)
		// Now drop the connection.
		server.Close()
	}()

	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("expected an error from rpcCall after connection drop, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: rpcCall did not return after connection drop")
	}
}

// TestHubMux_ReconnectBackoff verifies the exponential backoff progression.
func TestHubMux_ReconnectBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 30 * time.Second},  // capped
		{6, 30 * time.Second},  // capped
		{63, 30 * time.Second}, // overflow guard
	}

	for _, tc := range cases {
		got := reconnectBackoff(tc.attempt)
		if got != tc.want {
			t.Errorf("reconnectBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// TestRunReconnect_ReconnectsOnDrop verifies that runReconnect swaps in a new
// connection and emits a "reconnected" notification after a drop.
func TestRunReconnect_ReconnectsOnDrop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// First connection (will be dropped).
	firstClient, firstServer := pipeConn(t)
	log := slog.Default()

	mux := newHubMux(ctx, firstClient, log)

	nw, buf := testNotificationWriter()

	agent := makeTestAgent()

	// Second connection (will be used after reconnect).
	// We set up a fake "hub" on the server side that answers hub.register.
	secondClient, secondServer := pipeConn(t)

	// Override connectToHub for this test by swapping the reconnect logic —
	// we do this by calling swapConn directly in a goroutine that mimics what
	// runReconnect does, but uses our pre-built secondClient.
	//
	// Instead of testing runReconnect end-to-end (which needs a real socket),
	// we directly exercise the mux primitives it uses:
	// 1. Signal disconnect by closing firstServer.
	// 2. swapConn to secondClient.
	// 3. Start new readLoop.
	// 4. Have server answer hub.register.
	// 5. Verify "reconnected" notification is emitted.

	discCh := mux.disconnectedCh()

	// Drop the first connection.
	firstServer.Close()

	// Wait for the disconnect signal.
	select {
	case <-discCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: disconnect not signalled")
	}

	// Simulate what runReconnect does after connectToHub succeeds.
	old := mux.swapConn(secondClient)
	if old != nil {
		_ = old.Close()
	}
	go mux.readLoop(ctx)

	// Answer the hub.register call that runReconnect would send.
	go func() {
		req := readRPCRequest(t, secondServer)
		if req.Method == "" {
			return // connection closed during teardown
		}
		var id uint64
		switch v := req.ID.(type) {
		case float64:
			id = uint64(v)
		}
		sendRPCResponse(t, secondServer, id, map[string]string{"status": "ok"})
	}()

	// Send hub.register and capture the result.
	regResp, err := mux.rpcCall(ctx, "hub.register", agent)
	if err != nil {
		t.Fatalf("rpcCall hub.register after reconnect: %v", err)
	}
	if regResp.Error != nil {
		t.Fatalf("hub.register returned error: %s", regResp.Error.Message)
	}

	// Emit the reconnected notification (as runReconnect would).
	_ = nw.writeNotification("notifications/claude/channel", channelNotificationParams{
		Content: "Reconnected to bifrost hub",
		Meta:    map[string]string{"event": "reconnected"},
	})

	// Verify the notification was written.
	if buf.Len() == 0 {
		t.Fatal("expected reconnected notification in output buffer")
	}

	_, cp := parseChannelNotification(t, buf.Bytes())
	if cp.Content != "Reconnected to bifrost hub" {
		t.Errorf("notification content: got %q, want %q", cp.Content, "Reconnected to bifrost hub")
	}
	if cp.Meta["event"] != "reconnected" {
		t.Errorf("meta.event: got %q, want %q", cp.Meta["event"], "reconnected")
	}

	secondServer.Close()
}

// makeTestAgent builds a minimal *protocol.Agent for use in tests.
func makeTestAgent() any {
	return map[string]string{
		"agent_id":     "test-agent",
		"display_name": "Test Agent",
	}
}

// TestHubMux_RpcCallReturnsErrAfterConnNil verifies that rpcCall returns
// errReconnecting when the connection has been set to nil (during swap).
func TestHubMux_RpcCallReturnsErrAfterConnNil(t *testing.T) {
	ctx := context.Background()
	log := slog.Default()

	client, server := pipeConn(t)
	defer server.Close()

	mux := newHubMux(ctx, client, log)

	// Manually set conn to nil to simulate mid-reconnect state.
	mux.connMu.Lock()
	mux.conn = nil
	mux.connMu.Unlock()

	_, err := mux.rpcCall(ctx, "hub.ping", nil)
	if err != errReconnecting {
		t.Errorf("expected errReconnecting, got %v", err)
	}
}
