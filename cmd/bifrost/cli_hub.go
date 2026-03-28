package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/protocol"
)

// tryHubConnect attempts a single connection to the hub without auto-starting.
// Uses unix domain sockets on all platforms (Linux, macOS, Windows 10+).
func tryHubConnect() (*transport.Conn, error) {
	addr := config.SocketPath()
	nc, err := net.Dial("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to hub: %w", err)
	}
	return transport.NewConn(nc), nil
}

// cliMux is a minimal RPC multiplexer for CLI commands.
type cliMux struct {
	conn *transport.Conn
	log  *slog.Logger

	mu      sync.Mutex
	pending map[uint64]chan<- hub.RPCResponse
	nextID  atomic.Uint64
}

func newCLIMux(ctx context.Context, conn *transport.Conn) *cliMux {
	m := &cliMux{
		conn:    conn,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		pending: make(map[uint64]chan<- hub.RPCResponse),
	}
	go m.readLoop(ctx)
	return m
}

func (m *cliMux) rpcCall(ctx context.Context, method string, params any) (*hub.RPCResponse, error) {
	id := m.nextID.Add(1)

	ch := make(chan hub.RPCResponse, 1)
	m.mu.Lock()
	m.pending[id] = ch
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
	}()

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	req := hub.RPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  paramsJSON,
	}

	if err := m.conn.Send(req); err != nil {
		return nil, fmt.Errorf("send rpc: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		return &resp, nil
	}
}

func (m *cliMux) readLoop(_ context.Context) {
	for {
		var raw json.RawMessage
		if err := m.conn.Receive(&raw); err != nil {
			return
		}

		var peek struct {
			ID *json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			continue
		}

		if peek.ID == nil {
			continue // notification — CLI doesn't use them
		}

		var resp hub.RPCResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}

		var id uint64
		switch v := resp.ID.(type) {
		case float64:
			id = uint64(v)
		case json.Number:
			n, _ := v.Int64()
			id = uint64(n)
		default:
			continue
		}

		m.mu.Lock()
		ch, ok := m.pending[id]
		m.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

// printAgentTable prints agents as a formatted table.
func printAgentTable(agents []protocol.Agent) {
	fmt.Printf("%-20s  %-10s  %-30s  %s\n", "NAME", "STATUS", "PROJECT", "AGENT ID")
	fmt.Printf("%-20s  %-10s  %-30s  %s\n",
		strings.Repeat("-", 20),
		strings.Repeat("-", 10),
		strings.Repeat("-", 30),
		strings.Repeat("-", 36),
	)
	for _, a := range agents {
		name := a.DisplayName
		if name == "" {
			name = a.AgentID
		}
		project := a.ProjectName
		if len(project) > 30 {
			project = project[:27] + "..."
		}
		if len(name) > 20 {
			name = name[:17] + "..."
		}
		fmt.Printf("%-20s  %-10s  %-30s  %s\n", name, string(a.Status), project, a.AgentID)
	}
}
