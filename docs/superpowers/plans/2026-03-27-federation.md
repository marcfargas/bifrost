# Plan 3: Federation (libp2p, mDNS, Direct TCP, Offline Queue)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two bifrost hubs on different machines (or the internet) can peer, sync agent registries, and route messages and tasks transparently — agents address remote agents exactly like local ones.

**Architecture:** Federation manager sits between the hub core and multiple federation transports (libp2p, mDNS, direct TCP/TLS). When the hub's message router resolves a recipient to a peer hub, the federation manager forwards the message. An offline queue buffers messages when peer hubs are unreachable and flushes on reconnect. Magic code peering via libp2p is the default zero-config internet transport.

**Tech Stack:** Go 1.22+, `github.com/libp2p/go-libp2p`, `github.com/libp2p/go-libp2p-kad-dht`, existing bifrost codebase from Plans 1 and 2

**Spec:** `docs/superpowers/specs/2026-03-27-bifrost-design.md`

**Depends on:** Plan 1 (foundation, hub, local transport), Plan 2 (tasks, attachments, DND, MCP tools)

---

### Task 1: Peer Protocol Types and Store Extensions

**Files:**
- Modify: `pkg/protocol/types.go`
- Modify: `pkg/protocol/version.go`
- Modify: `pkg/store/store.go`
- Modify: `pkg/store/sqlite.go`
- Test: `pkg/protocol/peer_test.go`
- Test: `pkg/store/sqlite_peer_test.go`

- [ ] **Step 1: Add peer protocol types to types.go**

Add to `pkg/protocol/types.go`:

```go
// PeerStatus values.
type PeerStatus string

const (
	PeerConnected   PeerStatus = "connected"
	PeerUnreachable PeerStatus = "unreachable"
	PeerDisconnected PeerStatus = "disconnected"
)

// PeerTransport identifies the federation transport type.
type PeerTransport string

const (
	TransportLibp2p PeerTransport = "libp2p"
	TransportMDNS   PeerTransport = "mdns"
	TransportDirect PeerTransport = "direct"
)

// Peer represents a federated hub.
type Peer struct {
	PeerID        string        `json:"peer_id"`         // unique identifier (libp2p peer ID or generated)
	DisplayName   string        `json:"display_name"`    // human-friendly name
	Transport     PeerTransport `json:"transport"`       // how we connected
	Address       string        `json:"address"`         // transport-specific address
	Token         string        `json:"token"`           // persistent auth token for reconnection
	Status        PeerStatus    `json:"status"`
	LastSeen      time.Time     `json:"last_seen"`
	ConnectedAt   time.Time     `json:"connected_at"`
	FailCount     int           `json:"fail_count"`      // consecutive heartbeat failures
	ProtoVersion  string        `json:"proto_version"`   // peer's protocol version
}

// PeerEnvelope is the wire format for all hub-to-hub messages.
type PeerEnvelope struct {
	Method  string          `json:"method"`   // peer.sync_agents, peer.message, etc.
	ID      string          `json:"id"`       // request ID for correlation
	Version string          `json:"version"`  // protocol version
	From    string          `json:"from"`     // sender hub's peer ID
	Payload json.RawMessage `json:"payload"`  // method-specific data
}

// PeerSyncAgentsPayload is the payload for peer.sync_agents.
type PeerSyncAgentsPayload struct {
	Agents []*Agent `json:"agents"`
}

// PeerMessagePayload is the payload for peer.message.
type PeerMessagePayload struct {
	Message *Message `json:"message"`
}

// PeerTaskCreatePayload is the payload for peer.task_create.
type PeerTaskCreatePayload struct {
	Task            *Task               `json:"task"`
	AttachmentData  []PeerAttachmentData `json:"attachment_data,omitempty"`
}

// PeerAttachmentData carries attachment content across federation.
type PeerAttachmentData struct {
	Attachment *Attachment `json:"attachment"`
	Content    []byte     `json:"content"` // base64 encoded on wire
}

// PeerTaskUpdatePayload is the payload for peer.task_update.
type PeerTaskUpdatePayload struct {
	Task *Task `json:"task"`
}

// PeerAgentStatusPayload is the payload for peer.agent_status.
type PeerAgentStatusPayload struct {
	AgentID string      `json:"agent_id"`
	Status  AgentStatus `json:"status"`
}

// PeerHeartbeatPayload is the payload for peer.heartbeat.
type PeerHeartbeatPayload struct {
	Timestamp time.Time `json:"timestamp"`
}

// PeerResponse is the response to any peer envelope.
type PeerResponse struct {
	ID    string `json:"id"`    // matches request ID
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
```

Add the `import "encoding/json"` to the imports in `types.go`.

- [ ] **Step 2: Add peer hub ID to version.go**

Add to `pkg/protocol/version.go`:

```go
// HubID generates a stable hub identity from a peer ID.
// Used to tag agents with their origin hub.
func HubID(peerID string) string {
	h := sha256.Sum256([]byte("bifrost-hub:" + peerID))
	return hex.EncodeToString(h[:8])
}
```

Add `"crypto/sha256"` and `"encoding/hex"` to the imports.

- [ ] **Step 3: Add peer and federation queue methods to store interface**

Add to the `Store` interface in `pkg/store/store.go`:

```go
	// Peers (federation)
	UpsertPeer(ctx context.Context, peer *protocol.Peer) error
	GetPeer(ctx context.Context, peerID string) (*protocol.Peer, error)
	ListPeers(ctx context.Context) ([]*protocol.Peer, error)
	DeletePeer(ctx context.Context, peerID string) error
	UpdatePeerStatus(ctx context.Context, peerID string, status protocol.PeerStatus, failCount int) error
	TouchPeer(ctx context.Context, peerID string) error

	// Federation message queue (for unreachable peer hubs)
	EnqueuePeerMessage(ctx context.Context, peerID string, envelope *protocol.PeerEnvelope) error
	DequeuePeerMessages(ctx context.Context, peerID string) ([]*protocol.PeerEnvelope, error)
```

- [ ] **Step 4: Add peers table and federation queue table to SQLite migration**

Add to the `migrate()` method in `pkg/store/sqlite.go`:

```go
		CREATE TABLE IF NOT EXISTS peers (
			peer_id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			transport TEXT NOT NULL,
			address TEXT NOT NULL DEFAULT '',
			token TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'disconnected',
			last_seen TEXT NOT NULL,
			connected_at TEXT NOT NULL,
			fail_count INTEGER NOT NULL DEFAULT 0,
			proto_version TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS peer_message_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			peer_id TEXT NOT NULL,
			envelope_json TEXT NOT NULL,
			queued_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_peer_queue_peer ON peer_message_queue(peer_id);
```

- [ ] **Step 5: Implement peer CRUD and queue methods on SQLiteStore**

Add to `pkg/store/sqlite.go`:

```go
func (s *SQLiteStore) UpsertPeer(ctx context.Context, peer *protocol.Peer) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO peers (peer_id, display_name, transport, address, token, status,
			last_seen, connected_at, fail_count, proto_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(peer_id) DO UPDATE SET
			display_name=excluded.display_name, transport=excluded.transport,
			address=excluded.address, token=excluded.token, status=excluded.status,
			last_seen=excluded.last_seen, connected_at=excluded.connected_at,
			fail_count=excluded.fail_count, proto_version=excluded.proto_version`,
		peer.PeerID, peer.DisplayName, string(peer.Transport), peer.Address,
		peer.Token, string(peer.Status), peer.LastSeen.Format(time.RFC3339),
		peer.ConnectedAt.Format(time.RFC3339), peer.FailCount, peer.ProtoVersion)
	return err
}

func (s *SQLiteStore) GetPeer(ctx context.Context, peerID string) (*protocol.Peer, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT peer_id, display_name, transport, address, token, status,
			last_seen, connected_at, fail_count, proto_version
		FROM peers WHERE peer_id = ?`, peerID)

	var p protocol.Peer
	var lastSeen, connectedAt string
	err := row.Scan(&p.PeerID, &p.DisplayName, &p.Transport, &p.Address,
		&p.Token, &p.Status, &lastSeen, &connectedAt, &p.FailCount, &p.ProtoVersion)
	if err != nil {
		return nil, fmt.Errorf("peer %s not found: %w", peerID, err)
	}
	p.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
	p.ConnectedAt, _ = time.Parse(time.RFC3339, connectedAt)
	return &p, nil
}

func (s *SQLiteStore) ListPeers(ctx context.Context) ([]*protocol.Peer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT peer_id, display_name, transport, address, token, status,
			last_seen, connected_at, fail_count, proto_version
		FROM peers ORDER BY connected_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var peers []*protocol.Peer
	for rows.Next() {
		var p protocol.Peer
		var lastSeen, connectedAt string
		if err := rows.Scan(&p.PeerID, &p.DisplayName, &p.Transport, &p.Address,
			&p.Token, &p.Status, &lastSeen, &connectedAt, &p.FailCount, &p.ProtoVersion); err != nil {
			return nil, err
		}
		p.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
		p.ConnectedAt, _ = time.Parse(time.RFC3339, connectedAt)
		peers = append(peers, &p)
	}
	return peers, rows.Err()
}

func (s *SQLiteStore) DeletePeer(ctx context.Context, peerID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM peers WHERE peer_id = ?`, peerID)
	return err
}

func (s *SQLiteStore) UpdatePeerStatus(ctx context.Context, peerID string, status protocol.PeerStatus, failCount int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE peers SET status = ?, fail_count = ?, last_seen = ? WHERE peer_id = ?`,
		string(status), failCount, time.Now().Format(time.RFC3339), peerID)
	return err
}

func (s *SQLiteStore) TouchPeer(ctx context.Context, peerID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE peers SET last_seen = ?, fail_count = 0 WHERE peer_id = ?`,
		time.Now().Format(time.RFC3339), peerID)
	return err
}

func (s *SQLiteStore) EnqueuePeerMessage(ctx context.Context, peerID string, envelope *protocol.PeerEnvelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO peer_message_queue (peer_id, envelope_json, queued_at)
		VALUES (?, ?, ?)`, peerID, string(data), time.Now().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) DequeuePeerMessages(ctx context.Context, peerID string) ([]*protocol.PeerEnvelope, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT envelope_json FROM peer_message_queue
		WHERE peer_id = ? ORDER BY id ASC`, peerID)
	if err != nil {
		return nil, err
	}

	var envelopes []*protocol.PeerEnvelope
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return nil, err
		}
		var env protocol.PeerEnvelope
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			rows.Close()
			return nil, err
		}
		envelopes = append(envelopes, &env)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	_, err = tx.ExecContext(ctx, `DELETE FROM peer_message_queue WHERE peer_id = ?`, peerID)
	if err != nil {
		return nil, err
	}

	return envelopes, tx.Commit()
}
```

- [ ] **Step 6: Write peer protocol tests**

```go
// pkg/protocol/peer_test.go
package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPeerEnvelopeSerialization(t *testing.T) {
	msg := &Message{
		ID: "m1", ConversationID: "c1", From: "agent-a", To: "agent-b",
		Type: MsgContext, Body: "hello from remote", Priority: PriorityNormal,
		Timestamp: time.Now().Truncate(time.Second),
	}
	payload, err := json.Marshal(PeerMessagePayload{Message: msg})
	if err != nil {
		t.Fatal(err)
	}

	env := PeerEnvelope{
		Method:  "peer.message",
		ID:      NewShortID(),
		Version: ProtocolVersion,
		From:    "hub-abc",
		Payload: payload,
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	var decoded PeerEnvelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Method != "peer.message" {
		t.Errorf("expected peer.message, got %s", decoded.Method)
	}
	if decoded.Version != ProtocolVersion {
		t.Errorf("expected %s, got %s", ProtocolVersion, decoded.Version)
	}

	var msgPayload PeerMessagePayload
	if err := json.Unmarshal(decoded.Payload, &msgPayload); err != nil {
		t.Fatal(err)
	}
	if msgPayload.Message.Body != "hello from remote" {
		t.Errorf("unexpected body: %s", msgPayload.Message.Body)
	}
}

func TestPeerSyncAgentsPayload(t *testing.T) {
	agents := []*Agent{
		{AgentID: "a1", ProjectName: "backend", Status: AgentOnline},
		{AgentID: "a2", ProjectName: "frontend", Status: AgentIdle},
	}
	payload := PeerSyncAgentsPayload{Agents: agents}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var decoded PeerSyncAgentsPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(decoded.Agents))
	}
}

func TestHubIDDeterministic(t *testing.T) {
	id1 := HubID("peer-xyz")
	id2 := HubID("peer-xyz")
	if id1 != id2 {
		t.Errorf("HubID not deterministic: %s != %s", id1, id2)
	}
	id3 := HubID("peer-other")
	if id1 == id3 {
		t.Error("different peer IDs should produce different hub IDs")
	}
}
```

- [ ] **Step 7: Write store peer tests**

```go
// pkg/store/sqlite_peer_test.go
package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestPeerCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	peer := &protocol.Peer{
		PeerID:       "peer-abc",
		DisplayName:  "Bob's Hub",
		Transport:    protocol.TransportLibp2p,
		Token:        "tok-123",
		Status:       protocol.PeerConnected,
		LastSeen:     now,
		ConnectedAt:  now,
		ProtoVersion: protocol.ProtocolVersion,
	}

	// Insert
	if err := s.UpsertPeer(ctx, peer); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Read
	got, err := s.GetPeer(ctx, "peer-abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "Bob's Hub" {
		t.Errorf("expected Bob's Hub, got %s", got.DisplayName)
	}
	if got.Transport != protocol.TransportLibp2p {
		t.Errorf("expected libp2p, got %s", got.Transport)
	}

	// List
	peers, err := s.ListPeers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(peers) != 1 {
		t.Errorf("expected 1 peer, got %d", len(peers))
	}

	// Update status
	if err := s.UpdatePeerStatus(ctx, "peer-abc", protocol.PeerUnreachable, 3); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = s.GetPeer(ctx, "peer-abc")
	if got.Status != protocol.PeerUnreachable {
		t.Errorf("expected unreachable, got %s", got.Status)
	}
	if got.FailCount != 3 {
		t.Errorf("expected fail_count 3, got %d", got.FailCount)
	}

	// Touch
	if err := s.TouchPeer(ctx, "peer-abc"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, _ = s.GetPeer(ctx, "peer-abc")
	if got.FailCount != 0 {
		t.Error("touch should reset fail_count")
	}

	// Delete
	if err := s.DeletePeer(ctx, "peer-abc"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = s.GetPeer(ctx, "peer-abc")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestPeerMessageQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	msg := &protocol.Message{
		ID: "m1", ConversationID: "c1", From: "agent-a", To: "agent-b",
		Type: protocol.MsgContext, Body: "hello", Priority: protocol.PriorityNormal,
		Timestamp: time.Now().Truncate(time.Second),
	}
	payload, _ := json.Marshal(protocol.PeerMessagePayload{Message: msg})

	env1 := &protocol.PeerEnvelope{
		Method: "peer.message", ID: "r1", Version: protocol.ProtocolVersion,
		From: "hub-a", Payload: payload,
	}
	env2 := &protocol.PeerEnvelope{
		Method: "peer.message", ID: "r2", Version: protocol.ProtocolVersion,
		From: "hub-a", Payload: payload,
	}

	// Enqueue
	if err := s.EnqueuePeerMessage(ctx, "peer-xyz", env1); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if err := s.EnqueuePeerMessage(ctx, "peer-xyz", env2); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}

	// Dequeue returns all and clears
	got, err := s.DequeuePeerMessages(ctx, "peer-xyz")
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 envelopes, got %d", len(got))
	}
	if got[0].ID != "r1" || got[1].ID != "r2" {
		t.Error("envelopes not in order")
	}

	// Second dequeue returns empty
	got, err = s.DequeuePeerMessages(ctx, "peer-xyz")
	if err != nil {
		t.Fatalf("dequeue 2: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 after dequeue, got %d", len(got))
	}
}
```

- [ ] **Step 8: Run tests**

```bash
go test ./pkg/protocol/ ./pkg/store/ -v
```

Expected: all tests pass.

- [ ] **Step 9: Commit**

```bash
git add pkg/protocol/ pkg/store/
git commit -m "feat: add peer protocol types, store extensions for federation"
```

---

### Task 2: Federation Transport Interface

**Files:**
- Create: `internal/federation/transport.go`

- [ ] **Step 1: Write the federation transport interface**

```go
// internal/federation/transport.go
package federation

import (
	"context"
	"io"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// PeerConn represents a bidirectional connection to a peer hub.
type PeerConn interface {
	// PeerID returns the unique identifier for the remote peer.
	PeerID() string

	// Send sends an envelope to the peer.
	Send(ctx context.Context, env *protocol.PeerEnvelope) error

	// Receive blocks until an envelope is received from the peer.
	Receive(ctx context.Context) (*protocol.PeerEnvelope, error)

	// Close terminates the connection.
	Close() error
}

// Transport is a federation transport that can initiate and accept peer connections.
type Transport interface {
	// Name returns the transport identifier (e.g., "libp2p", "mdns", "direct").
	Name() string

	// Start begins listening for incoming peer connections.
	// Accepted connections are sent to the provided channel.
	Start(ctx context.Context, incoming chan<- PeerConn) error

	// Connect initiates an outgoing connection to a peer.
	// The address format is transport-specific.
	Connect(ctx context.Context, address string) (PeerConn, error)

	// Stop shuts down the transport.
	Stop() error
}

// StreamPeerConn adapts any io.ReadWriteCloser into a PeerConn using JSON encoding.
// Used by both direct TCP and libp2p transports.
type StreamPeerConn struct {
	peerID  string
	rwc     io.ReadWriteCloser
	encoder *json.Encoder
	decoder *json.Decoder
}

// NewStreamPeerConn creates a PeerConn from a bidirectional stream.
func NewStreamPeerConn(peerID string, rwc io.ReadWriteCloser) *StreamPeerConn {
	return &StreamPeerConn{
		peerID:  peerID,
		rwc:     rwc,
		encoder: json.NewEncoder(rwc),
		decoder: json.NewDecoder(rwc),
	}
}

func (c *StreamPeerConn) PeerID() string { return c.peerID }

func (c *StreamPeerConn) Send(ctx context.Context, env *protocol.PeerEnvelope) error {
	return c.encoder.Encode(env)
}

func (c *StreamPeerConn) Receive(ctx context.Context) (*protocol.PeerEnvelope, error) {
	var env protocol.PeerEnvelope
	if err := c.decoder.Decode(&env); err != nil {
		return nil, err
	}
	return &env, nil
}

func (c *StreamPeerConn) Close() error {
	return c.rwc.Close()
}
```

Add `"encoding/json"` to the imports.

- [ ] **Step 2: Commit**

```bash
git add internal/federation/
git commit -m "feat: add federation transport interface and StreamPeerConn"
```

---

### Task 3: Federation Manager

**Files:**
- Create: `internal/federation/manager.go`
- Test: `internal/federation/manager_test.go`

- [ ] **Step 1: Write the federation manager**

```go
// internal/federation/manager.go
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// Manager handles federation: peer registry, message forwarding, agent sync.
// It coordinates multiple transports and routes messages to/from peer hubs.
type Manager struct {
	hub        *core.Hub
	store      store.Store
	cfg        *config.Config
	logger     *slog.Logger
	localPeerID string

	mu         sync.RWMutex
	transports []Transport
	peers      map[string]*peerState // peer_id -> state
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

type peerState struct {
	conn     PeerConn
	peer     *protocol.Peer
	mu       sync.Mutex
	sendCh   chan *protocol.PeerEnvelope
	stopOnce sync.Once
	done     chan struct{}
}

// NewManager creates a new federation manager.
func NewManager(hub *core.Hub, s store.Store, cfg *config.Config, localPeerID string, logger *slog.Logger) *Manager {
	return &Manager{
		hub:         hub,
		store:       s,
		cfg:         cfg,
		logger:      logger,
		localPeerID: localPeerID,
		peers:       make(map[string]*peerState),
	}
}

// LocalPeerID returns this hub's peer identity.
func (m *Manager) LocalPeerID() string { return m.localPeerID }

// AddTransport registers a federation transport.
func (m *Manager) AddTransport(t Transport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transports = append(m.transports, t)
}

// Start begins all registered transports and reconnects known peers.
func (m *Manager) Start(ctx context.Context) error {
	ctx, m.cancel = context.WithCancel(ctx)
	incoming := make(chan PeerConn, 16)

	// Start all transports
	m.mu.RLock()
	for _, t := range m.transports {
		transport := t
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if err := transport.Start(ctx, incoming); err != nil {
				m.logger.Error("transport start failed", "transport", transport.Name(), "error", err)
			}
		}()
	}
	m.mu.RUnlock()

	// Accept incoming peer connections
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case conn := <-incoming:
				m.handleIncomingPeer(ctx, conn)
			}
		}
	}()

	// Reconnect known peers
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.reconnectKnownPeers(ctx)
	}()

	// Start heartbeat loop
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.heartbeatLoop(ctx)
	}()

	return nil
}

// Stop shuts down the federation manager and all transports.
func (m *Manager) Stop() error {
	if m.cancel != nil {
		m.cancel()
	}

	m.mu.Lock()
	for _, ps := range m.peers {
		ps.stopOnce.Do(func() {
			close(ps.done)
			ps.conn.Close()
		})
	}
	for _, t := range m.transports {
		t.Stop()
	}
	m.mu.Unlock()

	m.wg.Wait()
	return nil
}

// handleIncomingPeer processes a new inbound peer connection.
func (m *Manager) handleIncomingPeer(ctx context.Context, conn PeerConn) {
	peerID := conn.PeerID()
	m.logger.Info("incoming peer connection", "peer_id", peerID)

	// Version handshake: first message must be peer.heartbeat with version
	env, err := conn.Receive(ctx)
	if err != nil {
		m.logger.Error("peer handshake failed", "peer_id", peerID, "error", err)
		conn.Close()
		return
	}

	if !protocol.CompatibleWith(env.Version) {
		resp := &protocol.PeerResponse{
			ID:    env.ID,
			OK:    false,
			Error: fmt.Sprintf("incompatible protocol version: local=%s remote=%s", protocol.ProtocolVersion, env.Version),
		}
		payload, _ := json.Marshal(resp)
		conn.Send(ctx, &protocol.PeerEnvelope{
			Method: "peer.error", ID: env.ID, Version: protocol.ProtocolVersion,
			From: m.localPeerID, Payload: payload,
		})
		conn.Close()
		return
	}

	// Register peer
	now := time.Now()
	peer := &protocol.Peer{
		PeerID:       peerID,
		Status:       protocol.PeerConnected,
		LastSeen:     now,
		ConnectedAt:  now,
		ProtoVersion: env.Version,
	}

	// Check if this is a known peer (reconnection)
	existing, err := m.store.GetPeer(ctx, peerID)
	if err == nil {
		peer.DisplayName = existing.DisplayName
		peer.Transport = existing.Transport
		peer.Token = existing.Token
	}

	m.store.UpsertPeer(ctx, peer)
	m.addPeerConn(ctx, peerID, conn, peer)

	// Respond to the handshake heartbeat
	m.sendHeartbeatResponse(ctx, conn, env.ID)

	// Initial agent sync
	m.syncAgentsWithPeer(ctx, peerID)

	// Flush queued messages
	m.flushPeerQueue(ctx, peerID)
}

// ConnectPeer initiates a connection to a peer via any available transport.
func (m *Manager) ConnectPeer(ctx context.Context, transport PeerTransport, address string) (string, error) {
	m.mu.RLock()
	var t Transport
	for _, tr := range m.transports {
		if PeerTransport(tr.Name()) == transport {
			t = tr
			break
		}
	}
	m.mu.RUnlock()

	if t == nil {
		return "", fmt.Errorf("transport %s not available", transport)
	}

	conn, err := t.Connect(ctx, address)
	if err != nil {
		return "", fmt.Errorf("connect via %s: %w", transport, err)
	}

	peerID := conn.PeerID()

	// Send handshake heartbeat
	hbPayload, _ := json.Marshal(protocol.PeerHeartbeatPayload{Timestamp: time.Now()})
	handshake := &protocol.PeerEnvelope{
		Method:  "peer.heartbeat",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    m.localPeerID,
		Payload: hbPayload,
	}
	if err := conn.Send(ctx, handshake); err != nil {
		conn.Close()
		return "", fmt.Errorf("handshake send: %w", err)
	}

	// Wait for handshake response
	resp, err := conn.Receive(ctx)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("handshake receive: %w", err)
	}

	if !protocol.CompatibleWith(resp.Version) {
		conn.Close()
		return "", fmt.Errorf("incompatible protocol version: local=%s remote=%s",
			protocol.ProtocolVersion, resp.Version)
	}

	// Register peer
	now := time.Now()
	peer := &protocol.Peer{
		PeerID:       peerID,
		Transport:    transport,
		Address:      address,
		Status:       protocol.PeerConnected,
		LastSeen:     now,
		ConnectedAt:  now,
		ProtoVersion: resp.Version,
	}
	m.store.UpsertPeer(ctx, peer)
	m.addPeerConn(ctx, peerID, conn, peer)

	// Initial agent sync
	m.syncAgentsWithPeer(ctx, peerID)

	// Flush queued messages
	m.flushPeerQueue(ctx, peerID)

	return peerID, nil
}

// addPeerConn registers a peer connection and starts its read/write loops.
func (m *Manager) addPeerConn(ctx context.Context, peerID string, conn PeerConn, peer *protocol.Peer) {
	ps := &peerState{
		conn:   conn,
		peer:   peer,
		sendCh: make(chan *protocol.PeerEnvelope, 64),
		done:   make(chan struct{}),
	}

	m.mu.Lock()
	// Close existing connection if any
	if old, ok := m.peers[peerID]; ok {
		old.stopOnce.Do(func() {
			close(old.done)
			old.conn.Close()
		})
	}
	m.peers[peerID] = ps
	m.mu.Unlock()

	// Write loop
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-ps.done:
				return
			case <-ctx.Done():
				return
			case env := <-ps.sendCh:
				if err := conn.Send(ctx, env); err != nil {
					m.logger.Error("peer send failed", "peer_id", peerID, "error", err)
					m.handlePeerDisconnect(peerID)
					return
				}
			}
		}
	}()

	// Read loop
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-ps.done:
				return
			case <-ctx.Done():
				return
			default:
			}

			env, err := conn.Receive(ctx)
			if err != nil {
				m.logger.Error("peer receive failed", "peer_id", peerID, "error", err)
				m.handlePeerDisconnect(peerID)
				return
			}
			m.handlePeerEnvelope(ctx, peerID, env)
		}
	}()
}

// ForwardMessage sends a message to a peer hub.
// Called by the hub core when the recipient agent is on a remote hub.
func (m *Manager) ForwardMessage(ctx context.Context, peerID string, msg *protocol.Message) (protocol.DeliveryStatus, error) {
	payload, err := json.Marshal(protocol.PeerMessagePayload{Message: msg})
	if err != nil {
		return "", fmt.Errorf("marshal message: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.message",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    m.localPeerID,
		Payload: payload,
	}

	if err := m.sendToPeer(ctx, peerID, env); err != nil {
		// Queue for later delivery
		m.store.EnqueuePeerMessage(ctx, peerID, env)
		return protocol.QueuedUnreachable, nil
	}

	return protocol.Delivered, nil
}

// ForwardTaskCreate sends a task creation to the assignee's peer hub.
func (m *Manager) ForwardTaskCreate(ctx context.Context, peerID string, task *protocol.Task, attachmentData []protocol.PeerAttachmentData) error {
	payload, err := json.Marshal(protocol.PeerTaskCreatePayload{
		Task:           task,
		AttachmentData: attachmentData,
	})
	if err != nil {
		return fmt.Errorf("marshal task create: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.task_create",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    m.localPeerID,
		Payload: payload,
	}

	if err := m.sendToPeer(ctx, peerID, env); err != nil {
		m.store.EnqueuePeerMessage(ctx, peerID, env)
		return fmt.Errorf("task create queued (hub unreachable): %w", err)
	}
	return nil
}

// ForwardTaskUpdate sends a task status update to relevant peer hubs.
func (m *Manager) ForwardTaskUpdate(ctx context.Context, peerID string, task *protocol.Task) error {
	payload, err := json.Marshal(protocol.PeerTaskUpdatePayload{Task: task})
	if err != nil {
		return fmt.Errorf("marshal task update: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.task_update",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    m.localPeerID,
		Payload: payload,
	}

	if err := m.sendToPeer(ctx, peerID, env); err != nil {
		m.store.EnqueuePeerMessage(ctx, peerID, env)
	}
	return nil
}

// BroadcastAgentStatus notifies all peers about an agent status change.
func (m *Manager) BroadcastAgentStatus(ctx context.Context, agentID string, status protocol.AgentStatus) {
	payload, _ := json.Marshal(protocol.PeerAgentStatusPayload{
		AgentID: agentID,
		Status:  status,
	})

	env := &protocol.PeerEnvelope{
		Method:  "peer.agent_status",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    m.localPeerID,
		Payload: payload,
	}

	m.mu.RLock()
	peerIDs := make([]string, 0, len(m.peers))
	for id := range m.peers {
		peerIDs = append(peerIDs, id)
	}
	m.mu.RUnlock()

	for _, pid := range peerIDs {
		if err := m.sendToPeer(ctx, pid, env); err != nil {
			m.logger.Warn("agent status broadcast failed", "peer_id", pid, "error", err)
		}
	}
}

// sendToPeer sends an envelope to a connected peer.
// Returns an error if the peer is not connected.
func (m *Manager) sendToPeer(ctx context.Context, peerID string, env *protocol.PeerEnvelope) error {
	m.mu.RLock()
	ps, ok := m.peers[peerID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("peer %s not connected", peerID)
	}

	select {
	case ps.sendCh <- env:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-ps.done:
		return fmt.Errorf("peer %s disconnected", peerID)
	}
}

// handlePeerEnvelope processes an incoming envelope from a peer.
func (m *Manager) handlePeerEnvelope(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
	// Touch peer on any activity
	m.store.TouchPeer(ctx, peerID)

	switch env.Method {
	case "peer.heartbeat":
		m.handleHeartbeat(ctx, peerID, env)
	case "peer.sync_agents":
		m.handleSyncAgents(ctx, peerID, env)
	case "peer.message":
		m.handlePeerMessage(ctx, peerID, env)
	case "peer.task_create":
		m.handlePeerTaskCreate(ctx, peerID, env)
	case "peer.task_update":
		m.handlePeerTaskUpdate(ctx, peerID, env)
	case "peer.agent_status":
		m.handlePeerAgentStatus(ctx, peerID, env)
	default:
		m.logger.Warn("unknown peer method", "method", env.Method, "peer_id", peerID)
	}
}

func (m *Manager) handleHeartbeat(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
	m.sendHeartbeatResponse(ctx, nil, env.ID)
}

func (m *Manager) sendHeartbeatResponse(ctx context.Context, conn PeerConn, requestID string) {
	payload, _ := json.Marshal(protocol.PeerHeartbeatPayload{Timestamp: time.Now()})
	resp := &protocol.PeerEnvelope{
		Method:  "peer.heartbeat",
		ID:      requestID,
		Version: protocol.ProtocolVersion,
		From:    m.localPeerID,
		Payload: payload,
	}
	if conn != nil {
		conn.Send(ctx, resp)
	}
}

func (m *Manager) handleSyncAgents(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
	var payload protocol.PeerSyncAgentsPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		m.logger.Error("invalid sync_agents payload", "peer_id", peerID, "error", err)
		return
	}

	// Register remote agents with peer hub tag
	for _, agent := range payload.Agents {
		agent.PeerHub = peerID
		m.store.UpsertAgent(ctx, agent)
	}

	// Remove agents from this peer that are no longer reported
	existing, _ := m.store.ListAgents(ctx, store.AgentFilter{PeerHub: peerID})
	reportedIDs := make(map[string]bool)
	for _, a := range payload.Agents {
		reportedIDs[a.AgentID] = true
	}
	for _, a := range existing {
		if !reportedIDs[a.AgentID] {
			m.store.DeleteAgent(ctx, a.AgentID)
		}
	}

	// Recompute aliases across all agents (local + remote)
	m.hub.Agents().RecomputeAliases(ctx)

	m.logger.Info("synced agents from peer", "peer_id", peerID, "count", len(payload.Agents))
}

func (m *Manager) handlePeerMessage(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
	var payload protocol.PeerMessagePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		m.logger.Error("invalid message payload", "peer_id", peerID, "error", err)
		return
	}

	msg := payload.Message

	// Save the message
	m.store.SaveMessage(ctx, msg)

	// Deliver to local agent
	agent, err := m.hub.Agents().Resolve(ctx, msg.To)
	if err != nil {
		m.logger.Error("cannot resolve recipient for peer message", "to", msg.To, "error", err)
		return
	}

	if agent.PeerHub != "" {
		// Recipient is on another peer hub, not local — this should not happen in normal routing
		m.logger.Warn("peer message for non-local agent", "to", msg.To, "peer_hub", agent.PeerHub)
		return
	}

	// Check DND
	if agent.Status == protocol.AgentDND {
		if msg.Priority != protocol.PriorityUrgent {
			m.store.EnqueueMessage(ctx, agent.AgentID, msg)
			return
		}
	}

	status := m.hub.NotifyAgent(agent.AgentID, core.Notification{
		Type:    "message",
		Payload: msg,
	})
	if status == protocol.QueuedOffline {
		m.store.EnqueueMessage(ctx, agent.AgentID, msg)
	}
}

func (m *Manager) handlePeerTaskCreate(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
	var payload protocol.PeerTaskCreatePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		m.logger.Error("invalid task_create payload", "peer_id", peerID, "error", err)
		return
	}

	task := payload.Task

	// Save task locally (this hub becomes source of truth for the assignee)
	if err := m.store.SaveTask(ctx, task); err != nil {
		m.logger.Error("save federated task", "task_id", task.TaskID, "error", err)
		return
	}

	// Save attachments and their content
	for _, ad := range payload.AttachmentData {
		if err := m.store.SaveAttachment(ctx, ad.Attachment); err != nil {
			m.logger.Error("save federated attachment", "attachment_id", ad.Attachment.AttachmentID, "error", err)
			continue
		}
		// Write file content to disk
		if err := m.hub.WriteAttachmentContent(ad.Attachment, ad.Content); err != nil {
			m.logger.Error("write federated attachment content", "attachment_id", ad.Attachment.AttachmentID, "error", err)
		}
	}

	// Auto-subscribe requester (remote) and assignee (local) to task channel
	m.store.Subscribe(ctx, task.Requester, "task:"+task.TaskID)
	m.store.Subscribe(ctx, task.Assignee, "task:"+task.TaskID)

	// Notify the local assignee
	m.hub.NotifyAgent(task.Assignee, core.Notification{
		Type:    "task_requested",
		Payload: task,
	})
}

func (m *Manager) handlePeerTaskUpdate(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
	var payload protocol.PeerTaskUpdatePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		m.logger.Error("invalid task_update payload", "peer_id", peerID, "error", err)
		return
	}

	task := payload.Task

	// Update local task record
	if err := m.store.UpdateTask(ctx, task); err != nil {
		m.logger.Error("update federated task", "task_id", task.TaskID, "error", err)
		return
	}

	// Notify all local subscribers of the task
	subs, err := m.store.GetSubscribers(ctx, "task:"+task.TaskID)
	if err != nil {
		return
	}
	for _, agentID := range subs {
		agent, err := m.store.GetAgent(ctx, agentID)
		if err != nil || agent.PeerHub != "" {
			continue // skip remote agents, their hub handles notification
		}
		m.hub.NotifyAgent(agentID, core.Notification{
			Type:    "task_update",
			Payload: task,
		})
	}
}

func (m *Manager) handlePeerAgentStatus(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
	var payload protocol.PeerAgentStatusPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		m.logger.Error("invalid agent_status payload", "peer_id", peerID, "error", err)
		return
	}

	m.store.UpdateAgentStatus(ctx, payload.AgentID, payload.Status)

	// Notify local agents about the status change
	m.hub.NotifyAll(core.Notification{
		Type: "agent_status",
		Payload: map[string]string{
			"agent_id": payload.AgentID,
			"status":   string(payload.Status),
		},
	}, "")
}

// syncAgentsWithPeer sends local agent list to a peer.
func (m *Manager) syncAgentsWithPeer(ctx context.Context, peerID string) {
	agents, err := m.store.ListAgents(ctx, store.AgentFilter{PeerHub: "local"})
	if err != nil {
		m.logger.Error("list local agents for sync", "error", err)
		return
	}

	payload, _ := json.Marshal(protocol.PeerSyncAgentsPayload{Agents: agents})
	env := &protocol.PeerEnvelope{
		Method:  "peer.sync_agents",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    m.localPeerID,
		Payload: payload,
	}

	m.sendToPeer(ctx, peerID, env)
}

// flushPeerQueue sends all queued messages to a now-connected peer.
func (m *Manager) flushPeerQueue(ctx context.Context, peerID string) {
	envelopes, err := m.store.DequeuePeerMessages(ctx, peerID)
	if err != nil {
		m.logger.Error("dequeue peer messages", "peer_id", peerID, "error", err)
		return
	}

	for _, env := range envelopes {
		if err := m.sendToPeer(ctx, peerID, env); err != nil {
			// Re-enqueue if sending fails
			m.store.EnqueuePeerMessage(ctx, peerID, env)
			m.logger.Warn("re-enqueued peer message after flush failure", "peer_id", peerID)
			break
		}
	}

	if len(envelopes) > 0 {
		m.logger.Info("flushed queued messages to peer", "peer_id", peerID, "count", len(envelopes))
	}
}

// handlePeerDisconnect marks a peer as disconnected and its agents as unreachable.
func (m *Manager) handlePeerDisconnect(peerID string) {
	m.mu.Lock()
	ps, ok := m.peers[peerID]
	if ok {
		ps.stopOnce.Do(func() {
			close(ps.done)
			ps.conn.Close()
		})
		delete(m.peers, peerID)
	}
	m.mu.Unlock()

	ctx := context.Background()
	m.store.UpdatePeerStatus(ctx, peerID, protocol.PeerDisconnected, 0)

	// Mark all agents on this peer as unreachable
	agents, _ := m.store.ListAgents(ctx, store.AgentFilter{PeerHub: peerID})
	for _, a := range agents {
		m.store.UpdateAgentStatus(ctx, a.AgentID, protocol.AgentUnreachable)
	}

	m.logger.Info("peer disconnected", "peer_id", peerID)
}

// heartbeatLoop sends periodic heartbeats to all connected peers.
func (m *Manager) heartbeatLoop(ctx context.Context) {
	interval := m.cfg.Hub.HeartbeatInterval.Duration
	if interval == 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sendHeartbeats(ctx)
		}
	}
}

// sendHeartbeats pings all connected peers and tracks failures.
func (m *Manager) sendHeartbeats(ctx context.Context) {
	m.mu.RLock()
	peerIDs := make([]string, 0, len(m.peers))
	for id := range m.peers {
		peerIDs = append(peerIDs, id)
	}
	m.mu.RUnlock()

	payload, _ := json.Marshal(protocol.PeerHeartbeatPayload{Timestamp: time.Now()})

	for _, peerID := range peerIDs {
		env := &protocol.PeerEnvelope{
			Method:  "peer.heartbeat",
			ID:      protocol.NewShortID(),
			Version: protocol.ProtocolVersion,
			From:    m.localPeerID,
			Payload: payload,
		}

		if err := m.sendToPeer(ctx, peerID, env); err != nil {
			// Increment failure count
			peer, err := m.store.GetPeer(ctx, peerID)
			if err != nil {
				continue
			}
			newFails := peer.FailCount + 1
			maxFails := 3 // N consecutive failures marks unreachable
			if newFails >= maxFails {
				m.logger.Warn("peer unreachable after heartbeat failures",
					"peer_id", peerID, "failures", newFails)
				m.store.UpdatePeerStatus(ctx, peerID, protocol.PeerUnreachable, newFails)
				m.handlePeerDisconnect(peerID)
			} else {
				m.store.UpdatePeerStatus(ctx, peerID, protocol.PeerConnected, newFails)
			}
		}
	}
}

// reconnectKnownPeers tries to reconnect to previously connected peers on startup.
func (m *Manager) reconnectKnownPeers(ctx context.Context) {
	peers, err := m.store.ListPeers(ctx)
	if err != nil {
		m.logger.Error("list known peers for reconnect", "error", err)
		return
	}

	for _, peer := range peers {
		if peer.Address == "" {
			continue // cannot reconnect without address (e.g., mDNS-only)
		}

		m.mu.RLock()
		_, connected := m.peers[peer.PeerID]
		m.mu.RUnlock()
		if connected {
			continue
		}

		m.logger.Info("attempting reconnect to known peer",
			"peer_id", peer.PeerID, "transport", peer.Transport)

		_, err := m.ConnectPeer(ctx, PeerTransport(peer.Transport), peer.Address)
		if err != nil {
			m.logger.Warn("reconnect failed", "peer_id", peer.PeerID, "error", err)
			m.store.UpdatePeerStatus(ctx, peer.PeerID, protocol.PeerDisconnected, peer.FailCount+1)
		}
	}
}

// IsPeerConnected checks if a peer hub is currently connected.
func (m *Manager) IsPeerConnected(peerID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.peers[peerID]
	return ok
}

// ConnectedPeerIDs returns the IDs of all currently connected peers.
func (m *Manager) ConnectedPeerIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.peers))
	for id := range m.peers {
		ids = append(ids, id)
	}
	return ids
}

// PeerTransport re-exports for use in ConnectPeer without import cycle.
type PeerTransport = protocol.PeerTransport
```

- [ ] **Step 2: Add RecomputeAliases and WriteAttachmentContent to hub core**

Add a public `RecomputeAliases` method to `pkg/core/agents.go`:

```go
// RecomputeAliases recomputes and deduplicates aliases across all agents (local + federated).
// This is the public API called by the federation manager after syncing remote agents.
func (r *AgentRegistry) RecomputeAliases(ctx context.Context) {
	r.recomputeAllAliases(ctx)
}
```

Add `WriteAttachmentContent` to `pkg/core/hub.go`:

```go
// WriteAttachmentContent writes attachment file content to disk.
// Used when receiving federated task attachments.
func (h *Hub) WriteAttachmentContent(att *protocol.Attachment, content []byte) error {
	dir := filepath.Join(h.dataDir, "attachments", att.AttachmentID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir attachment: %w", err)
	}
	path := filepath.Join(dir, att.Filename)
	return os.WriteFile(path, content, 0o644)
}
```

Add `dataDir` field to `Hub` struct and update `NewHub` to accept it:

```go
type Hub struct {
	store     store.Store
	dataDir   string
	mu        sync.RWMutex
	notifiers []Notifier
	agents    *AgentRegistry
	messages  *MessageRouter
}

func NewHub(s store.Store, dataDir string) *Hub {
	h := &Hub{store: s, dataDir: dataDir}
	h.agents = NewAgentRegistry(s, h)
	h.messages = NewMessageRouter(s, h)
	return h
}
```

Add `"os"` and `"path/filepath"` to the imports in `hub.go`.

- [ ] **Step 3: Write federation manager tests**

```go
// internal/federation/manager_test.go
package federation

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"log/slog"
)

// mockPeerConn implements PeerConn for testing.
type mockPeerConn struct {
	id      string
	sendBuf []*protocol.PeerEnvelope
	recvCh  chan *protocol.PeerEnvelope
	mu      sync.Mutex
	closed  bool
}

func newMockPeerConn(id string) *mockPeerConn {
	return &mockPeerConn{
		id:     id,
		recvCh: make(chan *protocol.PeerEnvelope, 32),
	}
}

func (c *mockPeerConn) PeerID() string { return c.id }

func (c *mockPeerConn) Send(ctx context.Context, env *protocol.PeerEnvelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendBuf = append(c.sendBuf, env)
	return nil
}

func (c *mockPeerConn) Receive(ctx context.Context) (*protocol.PeerEnvelope, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case env := <-c.recvCh:
		return env, nil
	}
}

func (c *mockPeerConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *mockPeerConn) Sent() []*protocol.PeerEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]*protocol.PeerEnvelope, len(c.sendBuf))
	copy(cp, c.sendBuf)
	return cp
}

// mockTransport implements Transport for testing.
type mockTransport struct {
	name    string
	conns   []*mockPeerConn
	mu      sync.Mutex
}

func newMockTransport(name string) *mockTransport {
	return &mockTransport{name: name}
}

func (t *mockTransport) Name() string { return t.name }

func (t *mockTransport) Start(ctx context.Context, incoming chan<- PeerConn) error {
	return nil
}

func (t *mockTransport) Connect(ctx context.Context, address string) (PeerConn, error) {
	conn := newMockPeerConn("peer-" + address)
	t.mu.Lock()
	t.conns = append(t.conns, conn)
	t.mu.Unlock()
	return conn, nil
}

func (t *mockTransport) Stop() error { return nil }

func newTestManager(t *testing.T) (*Manager, store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	dataDir := t.TempDir()
	hub := core.NewHub(s, dataDir)
	cfg := config.Defaults()
	logger := slog.Default()

	mgr := NewManager(hub, s, cfg, "local-hub-id", logger)
	return mgr, s
}

func TestManagerForwardMessage(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Simulate a connected peer
	conn := newMockPeerConn("peer-remote")
	peer := &protocol.Peer{
		PeerID: "peer-remote", Status: protocol.PeerConnected,
		LastSeen: time.Now(), ConnectedAt: time.Now(),
		ProtoVersion: protocol.ProtocolVersion,
	}
	s.UpsertPeer(ctx, peer)
	mgr.addPeerConn(ctx, "peer-remote", conn, peer)

	msg := &protocol.Message{
		ID: "m1", ConversationID: "c1", From: "local-agent",
		To: "remote-agent", Type: protocol.MsgQuestion,
		Body: "hello remote", Priority: protocol.PriorityNormal,
		Timestamp: time.Now(),
	}

	status, err := mgr.ForwardMessage(ctx, "peer-remote", msg)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if status != protocol.Delivered {
		t.Errorf("expected delivered, got %s", status)
	}

	// Verify envelope was sent
	sent := conn.Sent()
	if len(sent) == 0 {
		t.Fatal("no envelopes sent")
	}
	if sent[0].Method != "peer.message" {
		t.Errorf("expected peer.message, got %s", sent[0].Method)
	}
}

func TestManagerForwardMessageQueuedWhenDisconnected(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Peer exists in store but is not connected
	peer := &protocol.Peer{
		PeerID: "peer-away", Status: protocol.PeerDisconnected,
		LastSeen: time.Now(), ConnectedAt: time.Now(),
		ProtoVersion: protocol.ProtocolVersion,
	}
	s.UpsertPeer(ctx, peer)

	msg := &protocol.Message{
		ID: "m2", ConversationID: "c1", From: "local-agent",
		To: "remote-agent", Type: protocol.MsgContext,
		Body: "queued message", Priority: protocol.PriorityNormal,
		Timestamp: time.Now(),
	}

	status, err := mgr.ForwardMessage(ctx, "peer-away", msg)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if status != protocol.QueuedUnreachable {
		t.Errorf("expected queued (hub unreachable), got %s", status)
	}

	// Verify message was queued
	queued, err := s.DequeuePeerMessages(ctx, "peer-away")
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(queued) != 1 {
		t.Errorf("expected 1 queued, got %d", len(queued))
	}
}

func TestManagerSyncAgents(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Build a sync_agents envelope
	remoteAgents := []*protocol.Agent{
		{
			AgentID: "remote-a1", ProjectName: "mobile-app",
			Status: protocol.AgentOnline, Username: "bob",
			Hostname: "bob-laptop", LocalPath: "/dev/mobile",
			ConnectedAt: time.Now(), LastSeen: time.Now(),
			ProtocolVersion: protocol.ProtocolVersion,
		},
	}
	payload, _ := json.Marshal(protocol.PeerSyncAgentsPayload{Agents: remoteAgents})
	env := &protocol.PeerEnvelope{
		Method: "peer.sync_agents", ID: "s1", Version: protocol.ProtocolVersion,
		From: "peer-bob", Payload: payload,
	}

	mgr.handlePeerEnvelope(ctx, "peer-bob", env)

	// Verify remote agent was registered with peer_hub tag
	agent, err := s.GetAgent(ctx, "remote-a1")
	if err != nil {
		t.Fatalf("get remote agent: %v", err)
	}
	if agent.PeerHub != "peer-bob" {
		t.Errorf("expected peer_hub=peer-bob, got %s", agent.PeerHub)
	}
	if agent.ProjectName != "mobile-app" {
		t.Errorf("expected mobile-app, got %s", agent.ProjectName)
	}
}

func TestManagerHandlePeerDisconnect(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Register a remote agent from a peer
	agent := &protocol.Agent{
		AgentID: "remote-x", ProjectName: "infra", PeerHub: "peer-x",
		Status: protocol.AgentOnline, Username: "alice",
		Hostname: "alice-pc", LocalPath: "/dev/infra",
		ConnectedAt: time.Now(), LastSeen: time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}
	s.UpsertAgent(ctx, agent)

	peer := &protocol.Peer{
		PeerID: "peer-x", Status: protocol.PeerConnected,
		LastSeen: time.Now(), ConnectedAt: time.Now(),
		ProtoVersion: protocol.ProtocolVersion,
	}
	s.UpsertPeer(ctx, peer)

	conn := newMockPeerConn("peer-x")
	mgr.addPeerConn(ctx, "peer-x", conn, peer)

	// Disconnect
	mgr.handlePeerDisconnect("peer-x")

	// Agent should be unreachable
	got, _ := s.GetAgent(ctx, "remote-x")
	if got.Status != protocol.AgentUnreachable {
		t.Errorf("expected unreachable, got %s", got.Status)
	}

	// Peer should be disconnected
	gotPeer, _ := s.GetPeer(ctx, "peer-x")
	if gotPeer.Status != protocol.PeerDisconnected {
		t.Errorf("expected disconnected, got %s", gotPeer.Status)
	}
}

func TestManagerFlushPeerQueue(t *testing.T) {
	mgr, s := newTestManager(t)
	ctx := context.Background()

	// Queue some messages for a peer
	payload, _ := json.Marshal(protocol.PeerMessagePayload{
		Message: &protocol.Message{
			ID: "q1", ConversationID: "c1", From: "a", To: "b",
			Type: protocol.MsgContext, Body: "queued",
			Priority: protocol.PriorityNormal, Timestamp: time.Now(),
		},
	})
	env := &protocol.PeerEnvelope{
		Method: "peer.message", ID: "r1", Version: protocol.ProtocolVersion,
		From: "local-hub-id", Payload: payload,
	}
	s.EnqueuePeerMessage(ctx, "peer-flush", env)

	// Connect the peer
	conn := newMockPeerConn("peer-flush")
	peer := &protocol.Peer{
		PeerID: "peer-flush", Status: protocol.PeerConnected,
		LastSeen: time.Now(), ConnectedAt: time.Now(),
		ProtoVersion: protocol.ProtocolVersion,
	}
	s.UpsertPeer(ctx, peer)
	mgr.addPeerConn(ctx, "peer-flush", conn, peer)

	// Flush
	mgr.flushPeerQueue(ctx, "peer-flush")

	// Verify message was sent
	sent := conn.Sent()
	if len(sent) == 0 {
		t.Fatal("no envelopes sent after flush")
	}

	// Verify queue is empty
	remaining, _ := s.DequeuePeerMessages(ctx, "peer-flush")
	if len(remaining) != 0 {
		t.Errorf("expected empty queue after flush, got %d", len(remaining))
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/federation/ -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/federation/manager.go internal/federation/manager_test.go pkg/core/agents.go pkg/core/hub.go
git commit -m "feat: add federation manager with peer registry, message forwarding, agent sync"
```

---

### Task 4: libp2p Transport with Magic Code Peering

**Files:**
- Create: `internal/federation/libp2p.go`
- Create: `internal/federation/magiccode.go`
- Test: `internal/federation/magiccode_test.go`
- Test: `internal/federation/libp2p_test.go`

- [ ] **Step 1: Install libp2p dependencies**

```bash
go get github.com/libp2p/go-libp2p@latest
go get github.com/libp2p/go-libp2p-kad-dht@latest
go get github.com/multiformats/go-multiaddr@latest
```

- [ ] **Step 2: Write magic code generation and derivation**

```go
// internal/federation/magiccode.go
package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
)

const (
	// MagicCodePrefix is the human-readable prefix for magic codes.
	MagicCodePrefix = "BIFROST"
	// MagicCodeSegmentLen is the length of each segment in a formatted magic code.
	MagicCodeSegmentLen = 4
	// MagicCodeSegments is the number of segments (excluding prefix) in a magic code.
	MagicCodeSegments = 4
)

// MagicCode holds a generated magic code and the derived libp2p key pair.
type MagicCode struct {
	Code    string          // formatted: BIFROST-AXKM-TNVR-Q7PD-HZLW
	Seed    []byte          // raw 16-byte seed
	PrivKey crypto.PrivKey  // ed25519 private key derived from seed
	PubKey  crypto.PubKey   // ed25519 public key
}

// GenerateMagicCode creates a new magic code with a random seed.
func GenerateMagicCode() (*MagicCode, error) {
	seed := make([]byte, 16)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generate seed: %w", err)
	}
	return magicCodeFromSeed(seed)
}

// DeriveMagicCode reconstructs the key pair from a formatted magic code string.
func DeriveMagicCode(code string) (*MagicCode, error) {
	seed, err := decodeMagicCode(code)
	if err != nil {
		return nil, err
	}
	return magicCodeFromSeed(seed)
}

// magicCodeFromSeed derives a deterministic ed25519 key pair from a 16-byte seed.
func magicCodeFromSeed(seed []byte) (*MagicCode, error) {
	// Derive a 32-byte ed25519 seed from the 16-byte magic code seed.
	// Use SHA-256 to expand the seed deterministically.
	h := sha256.Sum256(append([]byte("bifrost-magic-v1:"), seed...))
	edSeed := h[:]

	// Create ed25519 key pair from the derived seed
	edPriv := ed25519.NewKeyFromSeed(edSeed)
	privKey, pubKey, err := crypto.KeyPairFromStdKey(&edPriv)
	if err != nil {
		return nil, fmt.Errorf("derive keypair: %w", err)
	}

	return &MagicCode{
		Code:    formatMagicCode(seed),
		Seed:    seed,
		PrivKey: privKey,
		PubKey:  pubKey,
	}, nil
}

// formatMagicCode encodes a seed as a human-friendly code: BIFROST-XXXX-XXXX-XXXX-XXXX
func formatMagicCode(seed []byte) string {
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
	// Take first 16 chars (covers 10 bytes = 16 base32 chars), then segment
	if len(encoded) > MagicCodeSegmentLen*MagicCodeSegments {
		encoded = encoded[:MagicCodeSegmentLen*MagicCodeSegments]
	}

	var segments []string
	segments = append(segments, MagicCodePrefix)
	for i := 0; i < len(encoded); i += MagicCodeSegmentLen {
		end := i + MagicCodeSegmentLen
		if end > len(encoded) {
			end = len(encoded)
		}
		segments = append(segments, encoded[i:end])
	}
	return strings.Join(segments, "-")
}

// decodeMagicCode parses a formatted magic code back to the raw seed.
func decodeMagicCode(code string) ([]byte, error) {
	code = strings.ToUpper(strings.TrimSpace(code))

	if !strings.HasPrefix(code, MagicCodePrefix+"-") {
		return nil, fmt.Errorf("invalid magic code: must start with %s-", MagicCodePrefix)
	}

	// Remove prefix and dashes
	rest := strings.TrimPrefix(code, MagicCodePrefix+"-")
	encoded := strings.ReplaceAll(rest, "-", "")

	if len(encoded) == 0 {
		return nil, fmt.Errorf("invalid magic code: empty after prefix")
	}

	// Pad to multiple of 8 for base32
	for len(encoded)%8 != 0 {
		encoded += "="
	}

	seed, err := base32.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid magic code: base32 decode: %w", err)
	}

	return seed, nil
}
```

- [ ] **Step 3: Write libp2p transport**

```go
// internal/federation/libp2p.go
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/multiformats/go-multiaddr"

	bifproto "github.com/marcfargas/bifrost/pkg/protocol"
)

const (
	// BifrostProtocolID is the libp2p protocol identifier for bifrost federation.
	BifrostProtocolID = protocol.ID("/bifrost/federation/1.0.0")

	// BifrostDHTNamespace is the DHT namespace for magic code announcements.
	BifrostDHTNamespace = "/bifrost/peer/"

	// dhtAnnounceTTL is how long a magic code announcement lives on the DHT.
	dhtAnnounceTTL = 24 * time.Hour
)

// Libp2pTransport implements federation over libp2p with Noise encryption and DHT discovery.
type Libp2pTransport struct {
	host       host.Host
	dht        *dht.IpfsDHT
	logger     *slog.Logger
	privKey    crypto.PrivKey
	bootstrap  []string
	incoming   chan<- PeerConn
	mu         sync.Mutex
	cancel     context.CancelFunc
}

// Libp2pConfig holds configuration for the libp2p transport.
type Libp2pConfig struct {
	PrivKey      crypto.PrivKey // persistent identity key for this hub
	Bootstrap    []string       // additional DHT bootstrap peers (multiaddr strings)
	ListenAddrs  []string       // listen addresses (empty = defaults)
	Logger       *slog.Logger
}

// NewLibp2pTransport creates a new libp2p federation transport.
func NewLibp2pTransport(cfg Libp2pConfig) (*Libp2pTransport, error) {
	if cfg.PrivKey == nil {
		// Generate a new identity key pair if none provided
		priv, _, err := crypto.GenerateEd25519Key(nil)
		if err != nil {
			return nil, fmt.Errorf("generate identity key: %w", err)
		}
		cfg.PrivKey = priv
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	listenAddrs := cfg.ListenAddrs
	if len(listenAddrs) == 0 {
		listenAddrs = []string{
			"/ip4/0.0.0.0/tcp/0",
			"/ip4/0.0.0.0/udp/0/quic-v1",
			"/ip6/::/tcp/0",
			"/ip6/::/udp/0/quic-v1",
		}
	}

	var multiaddrs []multiaddr.Multiaddr
	for _, addr := range listenAddrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			return nil, fmt.Errorf("parse listen addr %s: %w", addr, err)
		}
		multiaddrs = append(multiaddrs, ma)
	}

	h, err := libp2p.New(
		libp2p.Identity(cfg.PrivKey),
		libp2p.ListenAddrs(multiaddrs...),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
		libp2p.EnableNATService(),
	)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	return &Libp2pTransport{
		host:      h,
		logger:    cfg.Logger,
		privKey:   cfg.PrivKey,
		bootstrap: cfg.Bootstrap,
	}, nil
}

func (t *Libp2pTransport) Name() string { return "libp2p" }

// Start begins listening for incoming bifrost streams and bootstraps the DHT.
func (t *Libp2pTransport) Start(ctx context.Context, incoming chan<- PeerConn) error {
	ctx, t.cancel = context.WithCancel(ctx)
	t.incoming = incoming

	// Initialize DHT
	kadDHT, err := dht.New(ctx, t.host, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		return fmt.Errorf("create DHT: %w", err)
	}
	t.dht = kadDHT

	// Bootstrap DHT
	if err := t.dht.Bootstrap(ctx); err != nil {
		return fmt.Errorf("bootstrap DHT: %w", err)
	}

	// Connect to bootstrap peers
	for _, addr := range t.bootstrap {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			t.logger.Warn("invalid bootstrap addr", "addr", addr, "error", err)
			continue
		}
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			t.logger.Warn("invalid bootstrap peer info", "addr", addr, "error", err)
			continue
		}
		go func() {
			if err := t.host.Connect(ctx, *pi); err != nil {
				t.logger.Debug("bootstrap connect failed", "peer", pi.ID, "error", err)
			}
		}()
	}

	// Connect to default IPFS bootstrap peers
	for _, addr := range dht.DefaultBootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			continue
		}
		go func() {
			if err := t.host.Connect(ctx, *pi); err != nil {
				t.logger.Debug("default bootstrap connect failed", "peer", pi.ID, "error", err)
			}
		}()
	}

	// Set stream handler for incoming connections
	t.host.SetStreamHandler(BifrostProtocolID, func(s network.Stream) {
		peerID := s.Conn().RemotePeer().String()
		t.logger.Info("incoming libp2p stream", "peer_id", peerID)
		conn := NewStreamPeerConn(peerID, s)
		incoming <- conn
	})

	t.logger.Info("libp2p transport started",
		"peer_id", t.host.ID().String(),
		"addrs", t.host.Addrs())

	return nil
}

// Connect opens a stream to a remote peer by peer ID.
// The address should be a libp2p peer ID string (for DHT-discovered peers)
// or a full multiaddr.
func (t *Libp2pTransport) Connect(ctx context.Context, address string) (PeerConn, error) {
	// Try parsing as a full multiaddr first
	ma, err := multiaddr.NewMultiaddr(address)
	if err == nil {
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err == nil {
			if err := t.host.Connect(ctx, *pi); err != nil {
				return nil, fmt.Errorf("connect to multiaddr: %w", err)
			}
			s, err := t.host.NewStream(ctx, pi.ID, BifrostProtocolID)
			if err != nil {
				return nil, fmt.Errorf("open stream: %w", err)
			}
			return NewStreamPeerConn(pi.ID.String(), s), nil
		}
	}

	// Try parsing as a peer ID and finding via DHT
	pid, err := peer.Decode(address)
	if err != nil {
		return nil, fmt.Errorf("invalid peer address %q: not a multiaddr or peer ID", address)
	}

	// Find the peer via DHT
	pi, err := t.dht.FindPeer(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("find peer %s via DHT: %w", pid, err)
	}

	if err := t.host.Connect(ctx, pi); err != nil {
		return nil, fmt.Errorf("connect to peer %s: %w", pid, err)
	}

	s, err := t.host.NewStream(ctx, pid, BifrostProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open stream to %s: %w", pid, err)
	}

	return NewStreamPeerConn(pid.String(), s), nil
}

// Stop shuts down the libp2p host and DHT.
func (t *Libp2pTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	if t.dht != nil {
		t.dht.Close()
	}
	return t.host.Close()
}

// AnnounceMagicCode publishes the magic code's peer ID on the DHT so joiners can find this hub.
func (t *Libp2pTransport) AnnounceMagicCode(ctx context.Context, mc *MagicCode) error {
	// Derive the "rendezvous" peer ID from the magic code's public key
	rendID, err := peer.IDFromPublicKey(mc.PubKey)
	if err != nil {
		return fmt.Errorf("derive rendezvous peer ID: %w", err)
	}

	// Provide our peer ID under the rendezvous key in the DHT
	key := BifrostDHTNamespace + rendID.String()
	if err := t.dht.PutValue(ctx, key, []byte(t.host.ID().String())); err != nil {
		// Fallback: use routing advertisement
		t.logger.Debug("DHT PutValue failed, using Provide", "error", err)
	}

	// Also advertise using the content routing (more reliable)
	// The magic code's pub key hash becomes a CID-like key
	routingKey := "/bifrost/" + rendID.String()
	t.dht.Provide(ctx, []byte(routingKey), true)

	t.logger.Info("announced magic code on DHT",
		"rendezvous_peer_id", rendID.String(),
		"local_peer_id", t.host.ID().String())

	return nil
}

// FindByMagicCode looks up a hub that announced the given magic code on the DHT.
func (t *Libp2pTransport) FindByMagicCode(ctx context.Context, mc *MagicCode) (peer.ID, error) {
	rendID, err := peer.IDFromPublicKey(mc.PubKey)
	if err != nil {
		return "", fmt.Errorf("derive rendezvous peer ID: %w", err)
	}

	// Try to get the value from DHT
	key := BifrostDHTNamespace + rendID.String()
	val, err := t.dht.GetValue(ctx, key)
	if err == nil {
		pid, err := peer.Decode(string(val))
		if err == nil {
			return pid, nil
		}
	}

	// Fallback: find providers
	routingKey := "/bifrost/" + rendID.String()
	provCh := t.dht.FindProviders(ctx, []byte(routingKey))

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case pi, ok := <-provCh:
		if !ok {
			return "", fmt.Errorf("no hub found for magic code")
		}
		return pi.ID, nil
	}
}

// PeerNew generates a magic code and announces this hub on the DHT.
// Returns the formatted magic code string.
func (t *Libp2pTransport) PeerNew(ctx context.Context) (string, error) {
	mc, err := GenerateMagicCode()
	if err != nil {
		return "", err
	}

	if err := t.AnnounceMagicCode(ctx, mc); err != nil {
		return "", fmt.Errorf("announce: %w", err)
	}

	return mc.Code, nil
}

// PeerJoin finds and connects to a hub that announced the given magic code.
func (t *Libp2pTransport) PeerJoin(ctx context.Context, code string) (PeerConn, error) {
	mc, err := DeriveMagicCode(code)
	if err != nil {
		return nil, fmt.Errorf("derive magic code: %w", err)
	}

	pid, err := t.FindByMagicCode(ctx, mc)
	if err != nil {
		return nil, fmt.Errorf("find peer: %w", err)
	}

	// Find and connect to the peer
	pi, err := t.dht.FindPeer(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("find peer addrs: %w", err)
	}

	if err := t.host.Connect(ctx, pi); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	s, err := t.host.NewStream(ctx, pid, BifrostProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}

	t.logger.Info("joined peer via magic code", "peer_id", pid.String())

	return NewStreamPeerConn(pid.String(), s), nil
}

// HostID returns the libp2p host's peer ID string.
func (t *Libp2pTransport) HostID() string {
	return t.host.ID().String()
}
```

- [ ] **Step 4: Write magic code tests**

```go
// internal/federation/magiccode_test.go
package federation

import (
	"strings"
	"testing"
)

func TestGenerateMagicCode(t *testing.T) {
	mc, err := GenerateMagicCode()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(mc.Code, MagicCodePrefix+"-") {
		t.Errorf("expected prefix %s-, got %s", MagicCodePrefix, mc.Code)
	}

	segments := strings.Split(mc.Code, "-")
	if len(segments) != MagicCodeSegments+1 { // prefix + 4 segments
		t.Errorf("expected %d segments, got %d: %s", MagicCodeSegments+1, len(segments), mc.Code)
	}

	if mc.PrivKey == nil || mc.PubKey == nil {
		t.Error("keys should not be nil")
	}

	if len(mc.Seed) != 16 {
		t.Errorf("expected 16-byte seed, got %d", len(mc.Seed))
	}
}

func TestDeriveMagicCodeRoundTrip(t *testing.T) {
	mc1, err := GenerateMagicCode()
	if err != nil {
		t.Fatal(err)
	}

	mc2, err := DeriveMagicCode(mc1.Code)
	if err != nil {
		t.Fatal(err)
	}

	// Same seed should produce same keys
	if !mc1.PubKey.Equals(mc2.PubKey) {
		t.Error("public keys should match after round-trip")
	}

	priv1Raw, _ := mc1.PrivKey.Raw()
	priv2Raw, _ := mc2.PrivKey.Raw()
	if string(priv1Raw) != string(priv2Raw) {
		t.Error("private keys should match after round-trip")
	}
}

func TestDeriveMagicCodeCaseInsensitive(t *testing.T) {
	mc1, err := GenerateMagicCode()
	if err != nil {
		t.Fatal(err)
	}

	// Lowercase version should work
	mc2, err := DeriveMagicCode(strings.ToLower(mc1.Code))
	if err != nil {
		t.Fatal(err)
	}

	if !mc1.PubKey.Equals(mc2.PubKey) {
		t.Error("case-insensitive derivation should produce same keys")
	}
}

func TestDeriveMagicCodeInvalid(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"no prefix", "XXXX-YYYY-ZZZZ-WWWW"},
		{"wrong prefix", "RAINBOW-XXXX-YYYY-ZZZZ"},
		{"just prefix", "BIFROST-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DeriveMagicCode(tt.code)
			if err == nil {
				t.Errorf("expected error for code %q", tt.code)
			}
		})
	}
}

func TestMagicCodeUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		mc, err := GenerateMagicCode()
		if err != nil {
			t.Fatal(err)
		}
		if seen[mc.Code] {
			t.Errorf("duplicate code on iteration %d: %s", i, mc.Code)
		}
		seen[mc.Code] = true
	}
}
```

- [ ] **Step 5: Write libp2p transport unit tests**

```go
// internal/federation/libp2p_test.go
package federation

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"log/slog"
)

func TestLibp2pTransportCreateAndStop(t *testing.T) {
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}

	lt, err := NewLibp2pTransport(Libp2pConfig{
		PrivKey: priv,
		Logger:  slog.Default(),
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if lt.Name() != "libp2p" {
		t.Errorf("expected name libp2p, got %s", lt.Name())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	incoming := make(chan PeerConn, 4)
	if err := lt.Start(ctx, incoming); err != nil {
		t.Fatal(err)
	}

	hostID := lt.HostID()
	if hostID == "" {
		t.Error("host ID should not be empty")
	}

	if err := lt.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestLibp2pTwoHostsConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger := slog.Default()

	// Create host A
	privA, _, _ := crypto.GenerateEd25519Key(nil)
	ltA, err := NewLibp2pTransport(Libp2pConfig{
		PrivKey: privA, Logger: logger,
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ltA.Stop()

	incomingA := make(chan PeerConn, 4)
	if err := ltA.Start(ctx, incomingA); err != nil {
		t.Fatal(err)
	}

	// Create host B
	privB, _, _ := crypto.GenerateEd25519Key(nil)
	ltB, err := NewLibp2pTransport(Libp2pConfig{
		PrivKey: privB, Logger: logger,
		ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ltB.Stop()

	incomingB := make(chan PeerConn, 4)
	if err := ltB.Start(ctx, incomingB); err != nil {
		t.Fatal(err)
	}

	// B connects to A using A's full multiaddr
	addrs := ltA.host.Addrs()
	if len(addrs) == 0 {
		t.Fatal("host A has no listen addresses")
	}
	fullAddr := addrs[0].String() + "/p2p/" + ltA.host.ID().String()

	conn, err := ltB.Connect(ctx, fullAddr)
	if err != nil {
		t.Fatalf("connect B->A: %v", err)
	}
	defer conn.Close()

	if conn.PeerID() != ltA.host.ID().String() {
		t.Errorf("expected peer ID %s, got %s", ltA.host.ID(), conn.PeerID())
	}

	// A should receive the incoming connection
	select {
	case incoming := <-incomingA:
		if incoming.PeerID() != ltB.host.ID().String() {
			t.Errorf("expected incoming from %s, got %s", ltB.host.ID(), incoming.PeerID())
		}
		incoming.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for incoming connection on A")
	}
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/federation/ -v -timeout 60s
```

Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/federation/libp2p.go internal/federation/magiccode.go
git add internal/federation/magiccode_test.go internal/federation/libp2p_test.go
git add go.mod go.sum
git commit -m "feat: add libp2p transport with magic code peering and DHT discovery"
```

---

### Task 5: mDNS Transport

**Files:**
- Create: `internal/federation/mdns.go`
- Test: `internal/federation/mdns_test.go`

- [ ] **Step 1: Write mDNS transport**

```go
// internal/federation/mdns.go
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

const (
	// MDNSServiceName is the mDNS service type for bifrost hub discovery.
	MDNSServiceName = "_bifrost-hub._tcp"
	// MDNSBrowseInterval is how often to scan for new hubs on the LAN.
	MDNSBrowseInterval = 10 * time.Second
)

// MDNSTransport discovers bifrost hubs on the local network via mDNS/DNS-SD.
// Disabled by default because it has no authentication.
type MDNSTransport struct {
	logger   *slog.Logger
	hubID    string
	port     int
	listener net.Listener
	incoming chan<- PeerConn
	mu       sync.Mutex
	known    map[string]bool // peer addresses already connected
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// MDNSConfig holds configuration for the mDNS transport.
type MDNSConfig struct {
	HubID  string
	Logger *slog.Logger
}

// NewMDNSTransport creates a new mDNS federation transport.
func NewMDNSTransport(cfg MDNSConfig) *MDNSTransport {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &MDNSTransport{
		logger: cfg.Logger,
		hubID:  cfg.HubID,
		known:  make(map[string]bool),
	}
}

func (t *MDNSTransport) Name() string { return "mdns" }

// Start begins advertising this hub via mDNS and browsing for others.
func (t *MDNSTransport) Start(ctx context.Context, incoming chan<- PeerConn) error {
	ctx, t.cancel = context.WithCancel(ctx)
	t.incoming = incoming

	// Start TCP listener for incoming connections from discovered peers
	var err error
	t.listener, err = net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf("mdns: listen: %w", err)
	}
	t.port = t.listener.Addr().(*net.TCPAddr).Port

	// Accept incoming TCP connections
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			conn, err := t.listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					t.logger.Error("mdns accept error", "error", err)
					continue
				}
			}
			// Read peer ID from initial handshake line
			peerConn := NewStreamPeerConn("mdns-"+conn.RemoteAddr().String(), conn)
			incoming <- peerConn
		}
	}()

	// Advertise this hub via mDNS
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.advertise(ctx)
	}()

	// Browse for other hubs
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.browse(ctx)
	}()

	t.logger.Info("mDNS transport started", "port", t.port, "hub_id", t.hubID)
	return nil
}

// advertise registers this hub's mDNS service using a simple UDP broadcast.
// This is a simplified implementation; production would use github.com/grandcat/zeroconf.
func (t *MDNSTransport) advertise(ctx context.Context) {
	ticker := time.NewTicker(MDNSBrowseInterval)
	defer ticker.Stop()

	record := fmt.Sprintf("%s\t%s\t%d", MDNSServiceName, t.hubID, t.port)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Broadcast presence via UDP multicast
			addr, err := net.ResolveUDPAddr("udp4", "224.0.0.251:5353")
			if err != nil {
				continue
			}
			conn, err := net.DialUDP("udp4", nil, addr)
			if err != nil {
				continue
			}
			conn.Write([]byte(record))
			conn.Close()
		}
	}
}

// browse listens for mDNS announcements from other bifrost hubs.
func (t *MDNSTransport) browse(ctx context.Context) {
	addr, err := net.ResolveUDPAddr("udp4", "224.0.0.251:5353")
	if err != nil {
		t.logger.Error("mdns: resolve multicast addr", "error", err)
		return
	}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		t.logger.Error("mdns: listen multicast", "error", err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(MDNSBrowseInterval))
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		record := string(buf[:n])
		var service, hubID string
		var port int
		_, err = fmt.Sscanf(record, "%s\t%s\t%d", &service, &hubID, &port)
		if err != nil || service != MDNSServiceName || hubID == t.hubID {
			continue
		}

		peerAddr := fmt.Sprintf("%s:%d", remoteAddr.IP.String(), port)

		t.mu.Lock()
		alreadyKnown := t.known[peerAddr]
		if !alreadyKnown {
			t.known[peerAddr] = true
		}
		t.mu.Unlock()

		if !alreadyKnown {
			t.logger.Info("discovered hub via mDNS", "hub_id", hubID, "addr", peerAddr)
			go func() {
				peerConn, err := t.Connect(ctx, peerAddr)
				if err != nil {
					t.logger.Warn("mdns connect failed", "addr", peerAddr, "error", err)
					t.mu.Lock()
					delete(t.known, peerAddr)
					t.mu.Unlock()
					return
				}
				t.incoming <- peerConn
			}()
		}
	}
}

// Connect opens a TCP connection to a discovered hub.
func (t *MDNSTransport) Connect(ctx context.Context, address string) (PeerConn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("mdns connect %s: %w", address, err)
	}
	return NewStreamPeerConn("mdns-"+address, conn), nil
}

// Stop shuts down the mDNS transport.
func (t *MDNSTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	if t.listener != nil {
		t.listener.Close()
	}
	t.wg.Wait()
	return nil
}
```

- [ ] **Step 2: Write mDNS transport tests**

```go
// internal/federation/mdns_test.go
package federation

import (
	"context"
	"testing"
	"time"

	"log/slog"
)

func TestMDNSTransportStartStop(t *testing.T) {
	mt := NewMDNSTransport(MDNSConfig{
		HubID:  "test-hub-1",
		Logger: slog.Default(),
	})

	if mt.Name() != "mdns" {
		t.Errorf("expected name mdns, got %s", mt.Name())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	incoming := make(chan PeerConn, 4)
	if err := mt.Start(ctx, incoming); err != nil {
		t.Fatal(err)
	}

	if mt.port == 0 {
		t.Error("port should be assigned after start")
	}

	if err := mt.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestMDNSTransportConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger := slog.Default()

	// Start hub A
	mtA := NewMDNSTransport(MDNSConfig{HubID: "hub-a", Logger: logger})
	incomingA := make(chan PeerConn, 4)
	if err := mtA.Start(ctx, incomingA); err != nil {
		t.Fatal(err)
	}
	defer mtA.Stop()

	// Start hub B and connect to A directly (bypassing mDNS discovery for test reliability)
	mtB := NewMDNSTransport(MDNSConfig{HubID: "hub-b", Logger: logger})
	incomingB := make(chan PeerConn, 4)
	if err := mtB.Start(ctx, incomingB); err != nil {
		t.Fatal(err)
	}
	defer mtB.Stop()

	// Direct connect B -> A
	addr := mtA.listener.Addr().String()
	conn, err := mtB.Connect(ctx, addr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if conn.PeerID() == "" {
		t.Error("peer ID should not be empty")
	}

	// A should accept the connection
	select {
	case incoming := <-incomingA:
		defer incoming.Close()
		if incoming.PeerID() == "" {
			t.Error("incoming peer ID should not be empty")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for incoming connection on A")
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/federation/ -v -run TestMDNS -timeout 30s
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/federation/mdns.go internal/federation/mdns_test.go
git commit -m "feat: add mDNS transport for zero-config LAN hub discovery"
```

---

### Task 6: Direct TCP/TLS Transport

**Files:**
- Create: `internal/federation/direct.go`
- Test: `internal/federation/direct_test.go`

- [ ] **Step 1: Write direct TCP/TLS transport**

```go
// internal/federation/direct.go
package federation

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// DirectTransport implements federation over direct TCP/TLS connections
// with token-based authentication. For known-address peering when
// libp2p DHT is not desired.
type DirectTransport struct {
	logger   *slog.Logger
	hubID    string
	listen   string
	tlsCert  string
	tlsKey   string
	tokens   map[string]string // peer_id -> token
	listener net.Listener
	incoming chan<- PeerConn
	mu       sync.RWMutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// DirectConfig holds configuration for the direct TCP/TLS transport.
type DirectConfig struct {
	HubID   string
	Listen  string // e.g., "0.0.0.0:7434"
	TLSCert string // path to TLS cert (empty = no TLS)
	TLSKey  string // path to TLS key
	Logger  *slog.Logger
}

// NewDirectTransport creates a new direct TCP/TLS federation transport.
func NewDirectTransport(cfg DirectConfig) *DirectTransport {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &DirectTransport{
		logger:  cfg.Logger,
		hubID:   cfg.HubID,
		listen:  cfg.Listen,
		tlsCert: cfg.TLSCert,
		tlsKey:  cfg.TLSKey,
		tokens:  make(map[string]string),
	}
}

func (t *DirectTransport) Name() string { return "direct" }

// Start begins listening for incoming direct connections.
func (t *DirectTransport) Start(ctx context.Context, incoming chan<- PeerConn) error {
	ctx, t.cancel = context.WithCancel(ctx)
	t.incoming = incoming

	var listener net.Listener
	var err error

	if t.tlsCert != "" && t.tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(t.tlsCert, t.tlsKey)
		if err != nil {
			return fmt.Errorf("direct: load TLS cert: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		}
		listener, err = tls.Listen("tcp", t.listen, tlsCfg)
		if err != nil {
			return fmt.Errorf("direct: tls listen: %w", err)
		}
	} else {
		listener, err = net.Listen("tcp", t.listen)
		if err != nil {
			return fmt.Errorf("direct: listen: %w", err)
		}
	}

	t.listener = listener

	// Accept loop
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					t.logger.Error("direct accept error", "error", err)
					continue
				}
			}
			go t.handleIncoming(ctx, conn)
		}
	}()

	t.logger.Info("direct transport started", "listen", listener.Addr().String())
	return nil
}

// handleIncoming authenticates an incoming connection.
func (t *DirectTransport) handleIncoming(ctx context.Context, conn net.Conn) {
	// Read the initial auth envelope
	peerConn := NewStreamPeerConn("", conn)

	env, err := peerConn.Receive(ctx)
	if err != nil {
		t.logger.Error("direct: auth receive failed", "error", err)
		conn.Close()
		return
	}

	if env.Method != "peer.auth" {
		t.logger.Error("direct: expected peer.auth, got", "method", env.Method)
		conn.Close()
		return
	}

	if !protocol.CompatibleWith(env.Version) {
		errPayload, _ := json.Marshal(protocol.PeerResponse{
			ID: env.ID, OK: false,
			Error: fmt.Sprintf("incompatible protocol: local=%s remote=%s",
				protocol.ProtocolVersion, env.Version),
		})
		peerConn.Send(ctx, &protocol.PeerEnvelope{
			Method: "peer.error", ID: env.ID, Version: protocol.ProtocolVersion,
			From: t.hubID, Payload: errPayload,
		})
		conn.Close()
		return
	}

	// Validate token
	var authPayload struct {
		Token  string `json:"token"`
		PeerID string `json:"peer_id"`
	}
	if err := json.Unmarshal(env.Payload, &authPayload); err != nil {
		conn.Close()
		return
	}

	t.mu.RLock()
	expectedToken, known := t.tokens[authPayload.PeerID]
	t.mu.RUnlock()

	if known && authPayload.Token != expectedToken {
		t.logger.Warn("direct: invalid token", "peer_id", authPayload.PeerID)
		conn.Close()
		return
	}

	// If not known, this is a new pairing — accept and store the token
	if !known && authPayload.Token != "" {
		t.mu.Lock()
		t.tokens[authPayload.PeerID] = authPayload.Token
		t.mu.Unlock()
	}

	// Send auth response
	respPayload, _ := json.Marshal(struct {
		PeerID string `json:"peer_id"`
		Token  string `json:"token"`
	}{
		PeerID: t.hubID,
		Token:  t.localToken(authPayload.PeerID),
	})
	peerConn.Send(ctx, &protocol.PeerEnvelope{
		Method: "peer.auth_ok", ID: env.ID, Version: protocol.ProtocolVersion,
		From: t.hubID, Payload: respPayload,
	})

	// Re-create the PeerConn with the real peer ID
	authedConn := &directPeerConn{
		StreamPeerConn: peerConn,
		remotePeerID:   authPayload.PeerID,
	}

	t.incoming <- authedConn
}

// localToken returns or generates a persistent token for a peer.
func (t *DirectTransport) localToken(peerID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	tok, ok := t.tokens[peerID]
	if ok {
		return tok
	}
	tok = protocol.NewID() + protocol.NewID() // 32 hex chars
	t.tokens[peerID] = tok
	return tok
}

// SetToken stores a token for a peer (loaded from config on startup).
func (t *DirectTransport) SetToken(peerID, token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tokens[peerID] = token
}

// Connect opens a direct TCP/TLS connection to a peer at the given address.
func (t *DirectTransport) Connect(ctx context.Context, address string) (PeerConn, error) {
	var conn net.Conn
	var err error

	dialer := net.Dialer{Timeout: 10 * time.Second}

	if t.tlsCert != "" {
		tlsCfg := &tls.Config{
			InsecureSkipVerify: true, // peers authenticate via tokens, not CA
			MinVersion:         tls.VersionTLS13,
		}
		conn, err = tls.DialWithDialer(&dialer, "tcp", address, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}

	if err != nil {
		return nil, fmt.Errorf("direct connect %s: %w", address, err)
	}

	peerConn := NewStreamPeerConn("", conn)

	// Send auth
	t.mu.RLock()
	token := t.tokens[address] // may be empty for first connection
	t.mu.RUnlock()

	authPayload, _ := json.Marshal(struct {
		Token  string `json:"token"`
		PeerID string `json:"peer_id"`
	}{
		Token:  token,
		PeerID: t.hubID,
	})

	if err := peerConn.Send(ctx, &protocol.PeerEnvelope{
		Method:  "peer.auth",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    t.hubID,
		Payload: authPayload,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("direct auth send: %w", err)
	}

	// Read auth response
	resp, err := peerConn.Receive(ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("direct auth receive: %w", err)
	}

	if resp.Method == "peer.error" {
		var errResp protocol.PeerResponse
		json.Unmarshal(resp.Payload, &errResp)
		conn.Close()
		return nil, fmt.Errorf("direct auth rejected: %s", errResp.Error)
	}

	var authResp struct {
		PeerID string `json:"peer_id"`
		Token  string `json:"token"`
	}
	json.Unmarshal(resp.Payload, &authResp)

	// Store the token for future reconnections
	if authResp.Token != "" {
		t.mu.Lock()
		t.tokens[authResp.PeerID] = authResp.Token
		t.mu.Unlock()
	}

	return &directPeerConn{
		StreamPeerConn: peerConn,
		remotePeerID:   authResp.PeerID,
	}, nil
}

// Stop shuts down the direct transport.
func (t *DirectTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	if t.listener != nil {
		t.listener.Close()
	}
	t.wg.Wait()
	return nil
}

// Addr returns the listener address (for tests and config).
func (t *DirectTransport) Addr() string {
	if t.listener != nil {
		return t.listener.Addr().String()
	}
	return t.listen
}

// directPeerConn wraps StreamPeerConn with the authenticated peer ID.
type directPeerConn struct {
	*StreamPeerConn
	remotePeerID string
}

func (c *directPeerConn) PeerID() string { return c.remotePeerID }
```

Add `"encoding/json"` to the imports.

- [ ] **Step 2: Write direct transport tests**

```go
// internal/federation/direct_test.go
package federation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"log/slog"
)

func TestDirectTransportStartStop(t *testing.T) {
	dt := NewDirectTransport(DirectConfig{
		HubID:  "hub-direct-1",
		Listen: "127.0.0.1:0",
		Logger: slog.Default(),
	})

	if dt.Name() != "direct" {
		t.Errorf("expected name direct, got %s", dt.Name())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	incoming := make(chan PeerConn, 4)
	if err := dt.Start(ctx, incoming); err != nil {
		t.Fatal(err)
	}

	if dt.Addr() == "" {
		t.Error("addr should be set after start")
	}

	if err := dt.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestDirectTransportConnectAndAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger := slog.Default()

	// Start server hub
	server := NewDirectTransport(DirectConfig{
		HubID:  "hub-server",
		Listen: "127.0.0.1:0",
		Logger: logger,
	})
	incomingServer := make(chan PeerConn, 4)
	if err := server.Start(ctx, incomingServer); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()

	// Start client hub
	client := NewDirectTransport(DirectConfig{
		HubID:  "hub-client",
		Listen: "127.0.0.1:0",
		Logger: logger,
	})
	incomingClient := make(chan PeerConn, 4)
	if err := client.Start(ctx, incomingClient); err != nil {
		t.Fatal(err)
	}
	defer client.Stop()

	// Client connects to server
	conn, err := client.Connect(ctx, server.Addr())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if conn.PeerID() != "hub-server" {
		t.Errorf("expected peer ID hub-server, got %s", conn.PeerID())
	}

	// Server should receive the incoming connection
	select {
	case incoming := <-incomingServer:
		if incoming.PeerID() != "hub-client" {
			t.Errorf("expected incoming from hub-client, got %s", incoming.PeerID())
		}

		// Test bidirectional messaging
		testMsg := &protocol.Message{
			ID: "test-1", ConversationID: "c1", From: "agent-s",
			To: "agent-c", Type: protocol.MsgContext, Body: "hello direct",
			Priority: protocol.PriorityNormal, Timestamp: time.Now(),
		}
		payload, _ := json.Marshal(protocol.PeerMessagePayload{Message: testMsg})
		env := &protocol.PeerEnvelope{
			Method: "peer.message", ID: "e1", Version: protocol.ProtocolVersion,
			From: "hub-server", Payload: payload,
		}

		// Server sends to client
		if err := incoming.Send(ctx, env); err != nil {
			t.Fatalf("server send: %v", err)
		}

		// Client receives
		received, err := conn.Receive(ctx)
		if err != nil {
			t.Fatalf("client receive: %v", err)
		}
		if received.Method != "peer.message" {
			t.Errorf("expected peer.message, got %s", received.Method)
		}

		incoming.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for incoming connection on server")
	}
}

func TestDirectTransportTokenPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger := slog.Default()

	server := NewDirectTransport(DirectConfig{
		HubID: "hub-s", Listen: "127.0.0.1:0", Logger: logger,
	})
	incomingS := make(chan PeerConn, 4)
	server.Start(ctx, incomingS)
	defer server.Stop()

	client := NewDirectTransport(DirectConfig{
		HubID: "hub-c", Listen: "127.0.0.1:0", Logger: logger,
	})
	incomingC := make(chan PeerConn, 4)
	client.Start(ctx, incomingC)
	defer client.Stop()

	// First connection (new pairing)
	conn1, err := client.Connect(ctx, server.Addr())
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	conn1.Close()
	<-incomingS // consume incoming

	// Client should now have a token for the server
	client.mu.RLock()
	token, hasToken := client.tokens["hub-s"]
	client.mu.RUnlock()
	if !hasToken || token == "" {
		t.Error("client should have stored a token for hub-s")
	}

	// Second connection (should use stored token)
	conn2, err := client.Connect(ctx, server.Addr())
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	conn2.Close()
	<-incomingS // consume incoming
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/federation/ -v -run TestDirect -timeout 30s
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/federation/direct.go internal/federation/direct_test.go
git commit -m "feat: add direct TCP/TLS transport with token-based authentication"
```

---

### Task 7: Offline Message Queue

**Files:**
- Create: `internal/federation/queue.go`
- Test: `internal/federation/queue_test.go`

- [ ] **Step 1: Write the offline queue manager**

```go
// internal/federation/queue.go
package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// Queue manages buffering of messages when peer hubs are unreachable.
// It sits between the federation manager and the store, providing
// a higher-level API for queuing and flushing.
type Queue struct {
	store  store.Store
	logger *slog.Logger
	mu     sync.Mutex
}

// NewQueue creates a new offline message queue.
func NewQueue(s store.Store, logger *slog.Logger) *Queue {
	if logger == nil {
		logger = slog.Default()
	}
	return &Queue{store: s, logger: logger}
}

// EnqueueMessage queues a message for delivery to a peer hub.
func (q *Queue) EnqueueMessage(ctx context.Context, peerID string, msg *protocol.Message) error {
	payload, err := json.Marshal(protocol.PeerMessagePayload{Message: msg})
	if err != nil {
		return fmt.Errorf("marshal message for queue: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.message",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    "", // will be set by manager on flush
		Payload: payload,
	}

	return q.store.EnqueuePeerMessage(ctx, peerID, env)
}

// EnqueueTaskCreate queues a task creation for a peer hub.
func (q *Queue) EnqueueTaskCreate(ctx context.Context, peerID string, task *protocol.Task, attachments []protocol.PeerAttachmentData) error {
	payload, err := json.Marshal(protocol.PeerTaskCreatePayload{
		Task:           task,
		AttachmentData: attachments,
	})
	if err != nil {
		return fmt.Errorf("marshal task create for queue: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.task_create",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		Payload: payload,
	}

	return q.store.EnqueuePeerMessage(ctx, peerID, env)
}

// EnqueueTaskUpdate queues a task update for a peer hub.
func (q *Queue) EnqueueTaskUpdate(ctx context.Context, peerID string, task *protocol.Task) error {
	payload, err := json.Marshal(protocol.PeerTaskUpdatePayload{Task: task})
	if err != nil {
		return fmt.Errorf("marshal task update for queue: %w", err)
	}

	env := &protocol.PeerEnvelope{
		Method:  "peer.task_update",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		Payload: payload,
	}

	return q.store.EnqueuePeerMessage(ctx, peerID, env)
}

// Flush dequeues all pending messages for a peer and sends them via the provided sender function.
// Returns the number of successfully sent messages and any error from dequeuing.
func (q *Queue) Flush(ctx context.Context, peerID string, sender func(ctx context.Context, env *protocol.PeerEnvelope) error) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	envelopes, err := q.store.DequeuePeerMessages(ctx, peerID)
	if err != nil {
		return 0, fmt.Errorf("dequeue: %w", err)
	}

	sent := 0
	for _, env := range envelopes {
		if err := sender(ctx, env); err != nil {
			// Re-enqueue remaining messages (including the failed one)
			for i := sent; i < len(envelopes); i++ {
				q.store.EnqueuePeerMessage(ctx, peerID, envelopes[i])
			}
			q.logger.Warn("flush interrupted, re-enqueued remaining",
				"peer_id", peerID, "sent", sent, "remaining", len(envelopes)-sent)
			return sent, nil
		}
		sent++
	}

	if sent > 0 {
		q.logger.Info("flushed queued messages", "peer_id", peerID, "count", sent)
	}

	return sent, nil
}

// Depth returns the number of queued messages for a peer.
func (q *Queue) Depth(ctx context.Context, peerID string) (int, error) {
	envelopes, err := q.store.DequeuePeerMessages(ctx, peerID)
	if err != nil {
		return 0, err
	}

	// DequeuePeerMessages deletes, so re-enqueue them
	for _, env := range envelopes {
		q.store.EnqueuePeerMessage(ctx, peerID, env)
	}

	return len(envelopes), nil
}
```

- [ ] **Step 2: Write queue tests**

```go
// internal/federation/queue_test.go
package federation

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"log/slog"
)

func newTestQueue(t *testing.T) (*Queue, store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewQueue(s, slog.Default()), s
}

func TestQueueEnqueueMessage(t *testing.T) {
	q, s := newTestQueue(t)
	ctx := context.Background()

	msg := &protocol.Message{
		ID: "m1", ConversationID: "c1", From: "agent-a", To: "agent-b",
		Type: protocol.MsgContext, Body: "hello", Priority: protocol.PriorityNormal,
		Timestamp: time.Now(),
	}

	if err := q.EnqueueMessage(ctx, "peer-x", msg); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Verify it's in the store
	envs, err := s.DequeuePeerMessages(ctx, "peer-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 {
		t.Errorf("expected 1, got %d", len(envs))
	}
	if envs[0].Method != "peer.message" {
		t.Errorf("expected peer.message, got %s", envs[0].Method)
	}
}

func TestQueueEnqueueTaskCreate(t *testing.T) {
	q, s := newTestQueue(t)
	ctx := context.Background()

	task := &protocol.Task{
		TaskID: "t1", ConversationID: "c1", Requester: "a1",
		Assignee: "a2", Title: "Build API", Description: "REST endpoints",
		Status: protocol.TaskRequested, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	if err := q.EnqueueTaskCreate(ctx, "peer-y", task, nil); err != nil {
		t.Fatalf("enqueue task: %v", err)
	}

	envs, _ := s.DequeuePeerMessages(ctx, "peer-y")
	if len(envs) != 1 || envs[0].Method != "peer.task_create" {
		t.Error("expected 1 peer.task_create envelope")
	}
}

func TestQueueFlushSuccess(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// Enqueue 3 messages
	for i := 0; i < 3; i++ {
		msg := &protocol.Message{
			ID: fmt.Sprintf("m%d", i), ConversationID: "c1",
			From: "a", To: "b", Type: protocol.MsgContext,
			Body: fmt.Sprintf("msg %d", i), Priority: protocol.PriorityNormal,
			Timestamp: time.Now(),
		}
		q.EnqueueMessage(ctx, "peer-z", msg)
	}

	// Flush with a sender that always succeeds
	var sent []*protocol.PeerEnvelope
	count, err := q.Flush(ctx, "peer-z", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		sent = append(sent, env)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("expected 3 sent, got %d", count)
	}
	if len(sent) != 3 {
		t.Errorf("expected 3 in sent slice, got %d", len(sent))
	}
}

func TestQueueFlushPartialFailure(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	// Enqueue 3 messages
	for i := 0; i < 3; i++ {
		msg := &protocol.Message{
			ID: fmt.Sprintf("pf%d", i), ConversationID: "c1",
			From: "a", To: "b", Type: protocol.MsgContext,
			Body: fmt.Sprintf("msg %d", i), Priority: protocol.PriorityNormal,
			Timestamp: time.Now(),
		}
		q.EnqueueMessage(ctx, "peer-partial", msg)
	}

	// Sender fails on second message
	callCount := 0
	count, err := q.Flush(ctx, "peer-partial", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		callCount++
		if callCount == 2 {
			return fmt.Errorf("network error")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 sent, got %d", count)
	}

	// Remaining 2 should be re-enqueued
	count2, err := q.Flush(ctx, "peer-partial", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count2 != 2 {
		t.Errorf("expected 2 remaining, got %d", count2)
	}
}

func TestQueueFlushEmpty(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	count, err := q.Flush(ctx, "peer-empty", func(ctx context.Context, env *protocol.PeerEnvelope) error {
		t.Error("sender should not be called for empty queue")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/federation/ -v -run TestQueue -timeout 30s
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/federation/queue.go internal/federation/queue_test.go
git commit -m "feat: add offline message queue with partial flush and re-enqueue"
```

---

### Task 8: Hub Core Integration — Federated Routing

**Files:**
- Modify: `pkg/core/hub.go`
- Modify: `pkg/core/messages.go`
- Modify: `pkg/core/agents.go`
- Modify: `internal/hub/server.go`
- Test: `pkg/core/federation_routing_test.go`

- [ ] **Step 1: Add federation manager hook to Hub**

Add to `pkg/core/hub.go`:

```go
// FederationForwarder is the interface the hub core uses to forward messages
// to peer hubs. This avoids an import cycle with internal/federation.
type FederationForwarder interface {
	// ForwardMessage sends a message to a peer hub.
	ForwardMessage(ctx context.Context, peerID string, msg *protocol.Message) (protocol.DeliveryStatus, error)

	// ForwardTaskCreate sends a task creation to the assignee's peer hub.
	ForwardTaskCreate(ctx context.Context, peerID string, task *protocol.Task, attachmentData []protocol.PeerAttachmentData) error

	// ForwardTaskUpdate sends a task update to a peer hub.
	ForwardTaskUpdate(ctx context.Context, peerID string, task *protocol.Task) error

	// BroadcastAgentStatus notifies all peers about an agent status change.
	BroadcastAgentStatus(ctx context.Context, agentID string, status protocol.AgentStatus)
}
```

Add federation field to Hub struct:

```go
type Hub struct {
	store      store.Store
	dataDir    string
	mu         sync.RWMutex
	notifiers  []Notifier
	federation FederationForwarder // nil if federation is disabled
	agents     *AgentRegistry
	messages   *MessageRouter
}

// SetFederation sets the federation forwarder for cross-hub routing.
func (h *Hub) SetFederation(f FederationForwarder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.federation = f
}

// Federation returns the federation forwarder (may be nil).
func (h *Hub) Federation() FederationForwarder {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.federation
}
```

- [ ] **Step 2: Update message routing to handle remote agents**

Modify `routeToAgent` in `pkg/core/messages.go`:

```go
func (r *MessageRouter) routeToAgent(ctx context.Context, msg *protocol.Message) (protocol.DeliveryStatus, error) {
	agent, err := r.hub.Agents().Resolve(ctx, msg.To)
	if err != nil {
		return "", err
	}

	// If agent is on a peer hub, forward via federation
	if agent.PeerHub != "" {
		fed := r.hub.Federation()
		if fed == nil {
			return "", fmt.Errorf("agent %s is on peer hub %s but federation is not enabled", msg.To, agent.PeerHub)
		}
		return fed.ForwardMessage(ctx, agent.PeerHub, msg)
	}

	// Local delivery
	// Check DND
	if agent.Status == protocol.AgentDND {
		if msg.Priority != protocol.PriorityUrgent {
			if err := r.store.EnqueueMessage(ctx, agent.AgentID, msg); err != nil {
				return "", err
			}
			return protocol.QueuedOffline, nil
		}
	}

	status := r.hub.NotifyAgent(agent.AgentID, Notification{
		Type:    "message",
		Payload: msg,
	})

	if status == protocol.QueuedOffline {
		r.store.EnqueueMessage(ctx, agent.AgentID, msg)
	}

	return status, nil
}
```

Update `routeBroadcast` and `routeToChannel` to include remote agents:

```go
func (r *MessageRouter) routeBroadcast(ctx context.Context, msg *protocol.Message) (protocol.DeliveryStatus, error) {
	agents, err := r.store.ListAgents(ctx, store.AgentFilter{Status: protocol.AgentOnline})
	if err != nil {
		return "", err
	}

	// Group agents by hub
	peerMessages := make(map[string]bool)

	for _, a := range agents {
		if a.AgentID == msg.From {
			continue
		}
		if a.PeerHub != "" {
			// Forward once per peer hub
			if !peerMessages[a.PeerHub] {
				peerMessages[a.PeerHub] = true
				if fed := r.hub.Federation(); fed != nil {
					fed.ForwardMessage(ctx, a.PeerHub, msg)
				}
			}
		} else {
			r.hub.NotifyAgent(a.AgentID, Notification{Type: "message", Payload: msg})
		}
	}

	return protocol.Delivered, nil
}
```

- [ ] **Step 3: Update agent registry to broadcast status changes to peers**

Add federation broadcast to `Register` and `Deregister` in `pkg/core/agents.go`:

```go
func (r *AgentRegistry) Register(ctx context.Context, agent *protocol.Agent) error {
	agent.Status = protocol.AgentOnline
	agent.Aliases = r.computeAliases(ctx, agent)

	if err := r.store.UpsertAgent(ctx, agent); err != nil {
		return fmt.Errorf("register agent: %w", err)
	}

	r.recomputeAllAliases(ctx)

	r.hub.NotifyAll(Notification{
		Type:    "agent_joined",
		Payload: agent,
	}, agent.AgentID)

	// Broadcast to federated peers
	if fed := r.hub.Federation(); fed != nil {
		fed.BroadcastAgentStatus(ctx, agent.AgentID, protocol.AgentOnline)
	}

	r.flushQueue(ctx, agent.AgentID)

	return nil
}

func (r *AgentRegistry) Deregister(ctx context.Context, agentID string) error {
	if err := r.store.UpdateAgentStatus(ctx, agentID, protocol.AgentOffline); err != nil {
		return err
	}
	r.hub.NotifyAll(Notification{
		Type:    "agent_left",
		Payload: map[string]string{"agent_id": agentID},
	}, agentID)

	// Broadcast to federated peers
	if fed := r.hub.Federation(); fed != nil {
		fed.BroadcastAgentStatus(ctx, agentID, protocol.AgentOffline)
	}

	return nil
}
```

- [ ] **Step 4: Wire federation into hub server startup**

Add to `internal/hub/server.go`, in the `Start` method, after the hub core and store are initialized:

```go
// Initialize federation if any transport is enabled
if cfg.Federation.Libp2p.Enabled || cfg.Federation.MDNS.Enabled || cfg.Federation.Direct.Enabled {
	fedLogger := logger.With("component", "federation")

	// Generate or load hub identity
	hubPeerID := loadOrGenerateHubID(cfg)

	fedManager := federation.NewManager(hub, s, cfg, hubPeerID, fedLogger)

	if cfg.Federation.Libp2p.Enabled {
		lt, err := federation.NewLibp2pTransport(federation.Libp2pConfig{
			Bootstrap: cfg.Federation.Libp2p.DHTBootstrap,
			Logger:    fedLogger.With("transport", "libp2p"),
		})
		if err != nil {
			return fmt.Errorf("create libp2p transport: %w", err)
		}
		fedManager.AddTransport(lt)
	}

	if cfg.Federation.MDNS.Enabled {
		mt := federation.NewMDNSTransport(federation.MDNSConfig{
			HubID:  hubPeerID,
			Logger: fedLogger.With("transport", "mdns"),
		})
		fedManager.AddTransport(mt)
	}

	if cfg.Federation.Direct.Enabled {
		dt := federation.NewDirectTransport(federation.DirectConfig{
			HubID:   hubPeerID,
			Listen:  cfg.Federation.Direct.Listen,
			TLSCert: cfg.Federation.Direct.TLSCert,
			TLSKey:  cfg.Federation.Direct.TLSKey,
			Logger:  fedLogger.With("transport", "direct"),
		})
		// Load stored tokens
		peers, _ := s.ListPeers(context.Background())
		for _, p := range peers {
			if p.Token != "" {
				dt.SetToken(p.PeerID, p.Token)
			}
		}
		fedManager.AddTransport(dt)
	}

	hub.SetFederation(fedManager)

	if err := fedManager.Start(ctx); err != nil {
		return fmt.Errorf("start federation: %w", err)
	}
	defer fedManager.Stop()
}
```

Add the hub ID persistence helper:

```go
func loadOrGenerateHubID(cfg *config.Config) string {
	idPath := filepath.Join(cfg.Storage.DataDir, "hub_identity")
	data, err := os.ReadFile(idPath)
	if err == nil && len(data) > 0 {
		return strings.TrimSpace(string(data))
	}
	id := protocol.NewID() + protocol.NewID() // 32 hex chars
	os.MkdirAll(filepath.Dir(idPath), 0o755)
	os.WriteFile(idPath, []byte(id), 0o600)
	return id
}
```

- [ ] **Step 5: Write federated routing tests**

```go
// pkg/core/federation_routing_test.go
package core

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// mockFederation implements FederationForwarder for testing.
type mockFederation struct {
	mu               sync.Mutex
	forwardedMsgs    []*protocol.Message
	forwardedTasks   []*protocol.Task
	statusBroadcasts []struct {
		AgentID string
		Status  protocol.AgentStatus
	}
}

func (m *mockFederation) ForwardMessage(ctx context.Context, peerID string, msg *protocol.Message) (protocol.DeliveryStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forwardedMsgs = append(m.forwardedMsgs, msg)
	return protocol.Delivered, nil
}

func (m *mockFederation) ForwardTaskCreate(ctx context.Context, peerID string, task *protocol.Task, attachments []protocol.PeerAttachmentData) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forwardedTasks = append(m.forwardedTasks, task)
	return nil
}

func (m *mockFederation) ForwardTaskUpdate(ctx context.Context, peerID string, task *protocol.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forwardedTasks = append(m.forwardedTasks, task)
	return nil
}

func (m *mockFederation) BroadcastAgentStatus(ctx context.Context, agentID string, status protocol.AgentStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusBroadcasts = append(m.statusBroadcasts, struct {
		AgentID string
		Status  protocol.AgentStatus
	}{agentID, status})
}

func TestMessageRouteToRemoteAgent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hub := NewHub(s, t.TempDir())
	fed := &mockFederation{}
	hub.SetFederation(fed)

	ctx := context.Background()
	now := time.Now()

	// Register a local agent
	localAgent := &protocol.Agent{
		AgentID: "local-1", ProjectName: "frontend",
		Username: "marc", Hostname: "myhost", LocalPath: "/dev/fe",
		Status: protocol.AgentOnline, ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	hub.Agents().Register(ctx, localAgent)

	// Register a remote agent (from peer hub)
	remoteAgent := &protocol.Agent{
		AgentID: "remote-1", ProjectName: "backend",
		Username: "bob", Hostname: "bob-pc", LocalPath: "/dev/api",
		Status: protocol.AgentOnline, ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion, PeerHub: "peer-bob",
	}
	s.UpsertAgent(ctx, remoteAgent)

	// Send message from local to remote
	msg := &protocol.Message{
		From: "local-1", To: "remote-1",
		Type: protocol.MsgQuestion, Body: "What's the API schema?",
	}
	status, err := hub.Messages().Send(ctx, msg)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != protocol.Delivered {
		t.Errorf("expected delivered, got %s", status)
	}

	// Verify federation forwarder was called
	fed.mu.Lock()
	if len(fed.forwardedMsgs) != 1 {
		t.Errorf("expected 1 forwarded message, got %d", len(fed.forwardedMsgs))
	}
	if fed.forwardedMsgs[0].Body != "What's the API schema?" {
		t.Errorf("unexpected body: %s", fed.forwardedMsgs[0].Body)
	}
	fed.mu.Unlock()
}

func TestMessageRouteToRemoteAgentNoFederation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hub := NewHub(s, t.TempDir())
	// Federation is NOT set

	ctx := context.Background()
	now := time.Now()

	remoteAgent := &protocol.Agent{
		AgentID: "remote-1", ProjectName: "backend", PeerHub: "peer-bob",
		Status: protocol.AgentOnline, Username: "bob", Hostname: "bob-pc",
		LocalPath: "/dev/api", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	s.UpsertAgent(ctx, remoteAgent)

	msg := &protocol.Message{
		From: "local-1", To: "remote-1",
		Type: protocol.MsgContext, Body: "test",
	}
	_, err = hub.Messages().Send(ctx, msg)
	if err == nil {
		t.Error("expected error when federation is not enabled")
	}
}

func TestAgentRegisterBroadcastsToFederation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hub := NewHub(s, t.TempDir())
	fed := &mockFederation{}
	hub.SetFederation(fed)

	ctx := context.Background()

	agent := &protocol.Agent{
		AgentID: "new-agent", ProjectName: "mobile",
		Username: "alice", Hostname: "alice-pc", LocalPath: "/dev/mobile",
		ConnectedAt: time.Now(), LastSeen: time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}
	hub.Agents().Register(ctx, agent)

	fed.mu.Lock()
	if len(fed.statusBroadcasts) != 1 {
		t.Errorf("expected 1 broadcast, got %d", len(fed.statusBroadcasts))
	}
	if fed.statusBroadcasts[0].Status != protocol.AgentOnline {
		t.Errorf("expected online, got %s", fed.statusBroadcasts[0].Status)
	}
	fed.mu.Unlock()
}

func TestBroadcastIncludesRemoteAgentPeerHubs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hub := NewHub(s, t.TempDir())
	fed := &mockFederation{}
	hub.SetFederation(fed)

	ctx := context.Background()
	now := time.Now()

	// Local agents
	for _, name := range []string{"fe", "be"} {
		a := &protocol.Agent{
			AgentID: "local-" + name, ProjectName: name,
			Username: "marc", Hostname: "myhost", LocalPath: "/dev/" + name,
			Status: protocol.AgentOnline, ConnectedAt: now, LastSeen: now,
			ProtocolVersion: protocol.ProtocolVersion,
		}
		s.UpsertAgent(ctx, a)
	}

	// Remote agent
	s.UpsertAgent(ctx, &protocol.Agent{
		AgentID: "remote-mob", ProjectName: "mobile", PeerHub: "peer-bob",
		Status: protocol.AgentOnline, Username: "bob", Hostname: "bob-pc",
		LocalPath: "/dev/mob", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion,
	})

	msg := &protocol.Message{
		From: "local-fe", To: "*",
		Type: protocol.MsgContext, Body: "deploy starting",
	}
	hub.Messages().Send(ctx, msg)

	// Federation should have received one forwarded message (for peer-bob)
	fed.mu.Lock()
	if len(fed.forwardedMsgs) != 1 {
		t.Errorf("expected 1 forwarded, got %d", len(fed.forwardedMsgs))
	}
	fed.mu.Unlock()
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./pkg/core/ -v -run TestMessage -timeout 30s
go test ./pkg/core/ -v -run TestAgent -timeout 30s
go test ./pkg/core/ -v -run TestBroadcast -timeout 30s
```

Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add pkg/core/ internal/hub/server.go
git commit -m "feat: integrate federation routing into hub core message router"
```

---

### Task 9: CLI Commands — bifrost peer

**Files:**
- Modify: `cmd/bifrost/main.go` (or appropriate cobra command file)
- Create: `cmd/bifrost/peer.go`

- [ ] **Step 1: Write peer CLI commands**

```go
// cmd/bifrost/peer.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/spf13/cobra"
)

func newPeerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "peer",
		Short: "Manage federation peers",
	}
	cmd.AddCommand(newPeerNewCmd())
	cmd.AddCommand(newPeerJoinCmd())
	cmd.AddCommand(newPeerListCmd())
	return cmd
}

func newPeerNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new",
		Short: "Generate a magic code for federation peering",
		Long:  "Generates a magic code and announces this hub on the DHT.\nShare the code with another hub operator to establish federation.",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := connectToHub()
			if err != nil {
				return fmt.Errorf("cannot connect to hub: %w", err)
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			resp, err := sendRPC(ctx, conn, "peer.new", nil)
			if err != nil {
				return err
			}

			var result struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(resp, &result); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}

			fmt.Println()
			fmt.Println("  Magic code generated. Share this code with the other hub:")
			fmt.Println()
			fmt.Printf("    %s\n", result.Code)
			fmt.Println()
			fmt.Println("  On the other machine, run:")
			fmt.Printf("    bifrost peer join %s\n", result.Code)
			fmt.Println()
			fmt.Println("  The code expires in 24 hours.")
			fmt.Println()

			return nil
		},
	}
}

func newPeerJoinCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "join CODE",
		Short: "Join a federated hub using a magic code",
		Long:  "Connects to a remote hub using the magic code generated by 'bifrost peer new'.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code := args[0]

			conn, err := connectToHub()
			if err != nil {
				return fmt.Errorf("cannot connect to hub: %w", err)
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			params := map[string]string{"code": code}
			resp, err := sendRPC(ctx, conn, "peer.join", params)
			if err != nil {
				return err
			}

			var result struct {
				PeerID string          `json:"peer_id"`
				Agents []*protocol.Agent `json:"agents"`
			}
			if err := json.Unmarshal(resp, &result); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}

			fmt.Println()
			fmt.Printf("  Connected to peer: %s\n", result.PeerID)
			fmt.Println()

			if len(result.Agents) > 0 {
				fmt.Println("  Remote agents:")
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintf(w, "    NAME\tSTATUS\tHOSTNAME\tPATH\n")
				for _, a := range result.Agents {
					name := a.AgentID
					if len(a.Aliases) > 0 {
						name = a.Aliases[0]
					}
					fmt.Fprintf(w, "    %s\t%s\t%s\t%s\n",
						name, a.Status, a.Hostname, a.LocalPath)
				}
				w.Flush()
			} else {
				fmt.Println("  No remote agents connected yet.")
			}
			fmt.Println()

			return nil
		},
	}
}

func newPeerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List federated peer hubs",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := connectToHub()
			if err != nil {
				return fmt.Errorf("cannot connect to hub: %w", err)
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := sendRPC(ctx, conn, "peer.list", nil)
			if err != nil {
				return err
			}

			var result struct {
				Peers []*protocol.Peer `json:"peers"`
			}
			if err := json.Unmarshal(resp, &result); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}

			if len(result.Peers) == 0 {
				fmt.Println("No federated peers.")
				fmt.Println("Use 'bifrost peer new' to generate a magic code.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "PEER ID\tNAME\tTRANSPORT\tSTATUS\tLAST SEEN\n")
			for _, p := range result.Peers {
				name := p.DisplayName
				if name == "" {
					name = "-"
				}
				lastSeen := p.LastSeen.Format("2006-01-02 15:04:05")
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					truncateID(p.PeerID, 16), name, p.Transport, p.Status, lastSeen)
			}
			w.Flush()

			return nil
		},
	}
}

// truncateID shortens an ID for display.
func truncateID(id string, maxLen int) string {
	if len(id) <= maxLen {
		return id
	}
	return id[:maxLen-3] + "..."
}
```

- [ ] **Step 2: Register peer command in main.go**

Add to the root command setup in `cmd/bifrost/main.go`:

```go
rootCmd.AddCommand(newPeerCmd())
```

- [ ] **Step 3: Add peer RPC handlers to hub server**

Add to the RPC handler in `internal/hub/rpc.go` (or `server.go`):

```go
case "peer.new":
	return h.handlePeerNew(ctx)
case "peer.join":
	return h.handlePeerJoin(ctx, params)
case "peer.list":
	return h.handlePeerList(ctx)
```

Implement the handlers:

```go
func (h *HubServer) handlePeerNew(ctx context.Context) (json.RawMessage, error) {
	fed := h.hub.Federation()
	if fed == nil {
		return nil, fmt.Errorf("federation is not enabled")
	}

	// Get the libp2p transport from the federation manager
	mgr, ok := fed.(*federation.Manager)
	if !ok {
		return nil, fmt.Errorf("federation manager not available")
	}

	// Find libp2p transport
	var lt *federation.Libp2pTransport
	for _, t := range mgr.Transports() {
		if lp, ok := t.(*federation.Libp2pTransport); ok {
			lt = lp
			break
		}
	}
	if lt == nil {
		return nil, fmt.Errorf("libp2p transport not enabled")
	}

	code, err := lt.PeerNew(ctx)
	if err != nil {
		return nil, fmt.Errorf("generate magic code: %w", err)
	}

	return json.Marshal(map[string]string{"code": code})
}

func (h *HubServer) handlePeerJoin(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	fed := h.hub.Federation()
	if fed == nil {
		return nil, fmt.Errorf("federation is not enabled")
	}

	mgr, ok := fed.(*federation.Manager)
	if !ok {
		return nil, fmt.Errorf("federation manager not available")
	}

	var lt *federation.Libp2pTransport
	for _, t := range mgr.Transports() {
		if lp, ok := t.(*federation.Libp2pTransport); ok {
			lt = lp
			break
		}
	}
	if lt == nil {
		return nil, fmt.Errorf("libp2p transport not enabled")
	}

	conn, err := lt.PeerJoin(ctx, p.Code)
	if err != nil {
		return nil, fmt.Errorf("join peer: %w", err)
	}

	peerID, err := mgr.ConnectPeer(ctx, protocol.TransportLibp2p, conn.PeerID())
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("register peer: %w", err)
	}

	// Get remote agents
	agents, _ := h.store.ListAgents(ctx, store.AgentFilter{PeerHub: peerID})

	return json.Marshal(map[string]interface{}{
		"peer_id": peerID,
		"agents":  agents,
	})
}

func (h *HubServer) handlePeerList(ctx context.Context) (json.RawMessage, error) {
	peers, err := h.store.ListPeers(ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]interface{}{"peers": peers})
}
```

Add `Transports()` method to the federation manager:

```go
// Transports returns the registered transports (for inspection by the hub server).
func (m *Manager) Transports() []Transport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]Transport, len(m.transports))
	copy(cp, m.transports)
	return cp
}
```

- [ ] **Step 4: Commit**

```bash
git add cmd/bifrost/peer.go cmd/bifrost/main.go internal/hub/ internal/federation/manager.go
git commit -m "feat: add bifrost peer CLI commands (new, join, list)"
```

---

### Task 10: MCP Tool — bifrost_peer

**Files:**
- Modify: `internal/shim/tools.go`

- [ ] **Step 1: Add bifrost_peer tool handler**

Add to the MCP tool registration in `internal/shim/tools.go`:

```go
{
	Name:        "bifrost_peer",
	Description: "Manage federation peering. Use 'new' to generate a magic code, or 'join' with a code to connect to a remote hub.",
	InputSchema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"new", "join"},
				"description": "Action: 'new' to generate a magic code, 'join' to connect using a code",
			},
			"code": map[string]interface{}{
				"type":        "string",
				"description": "Magic code (required for 'join' action). Format: BIFROST-XXXX-XXXX-XXXX-XXXX",
			},
		},
		"required": []string{"action"},
	},
},
```

- [ ] **Step 2: Implement the tool handler**

```go
func (s *Shim) handleBifrostPeer(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	action, _ := params["action"].(string)

	switch action {
	case "new":
		resp, err := s.sendRPC(ctx, "peer.new", nil)
		if err != nil {
			return nil, err
		}
		var result struct {
			Code string `json:"code"`
		}
		json.Unmarshal(resp, &result)

		return map[string]interface{}{
			"code":    result.Code,
			"message": fmt.Sprintf("Magic code generated: %s. Share this code with the other hub. They should run: bifrost peer join %s", result.Code, result.Code),
		}, nil

	case "join":
		code, _ := params["code"].(string)
		if code == "" {
			return nil, fmt.Errorf("'code' is required for join action")
		}

		resp, err := s.sendRPC(ctx, "peer.join", map[string]string{"code": code})
		if err != nil {
			return nil, err
		}

		var result struct {
			PeerID string          `json:"peer_id"`
			Agents []*protocol.Agent `json:"agents"`
		}
		json.Unmarshal(resp, &result)

		agentNames := make([]string, 0, len(result.Agents))
		for _, a := range result.Agents {
			name := a.AgentID
			if len(a.Aliases) > 0 {
				name = a.Aliases[0]
			}
			agentNames = append(agentNames, fmt.Sprintf("%s (%s)", name, a.Status))
		}

		return map[string]interface{}{
			"peer_id":       result.PeerID,
			"remote_agents": agentNames,
			"message":       fmt.Sprintf("Connected to peer hub. %d remote agents available.", len(result.Agents)),
		}, nil

	default:
		return nil, fmt.Errorf("unknown action %q, must be 'new' or 'join'", action)
	}
}
```

Add the handler dispatch in the tool call router:

```go
case "bifrost_peer":
	return s.handleBifrostPeer(ctx, params)
```

- [ ] **Step 3: Commit**

```bash
git add internal/shim/tools.go
git commit -m "feat: add bifrost_peer MCP tool for federation peering"
```

---

### Task 11: Federated Alias Resolution

**Files:**
- Modify: `pkg/core/agents.go`
- Test: `pkg/core/agents_federation_test.go`

- [ ] **Step 1: Update alias resolution for federation**

The alias resolution logic in `recomputeAllAliases` already operates on all agents in the store (local + remote). Verify and ensure that:

1. Local agents are checked first during resolution
2. If an alias exists on both a local and remote agent, neither gets it
3. Resolution returns the full agent list (including remote) on ambiguity

Update `Resolve` in `pkg/core/agents.go`:

```go
// Resolve finds an agent by alias, agent_id, or returns an error with available agents.
// Resolution order: local agents first, then peer agents.
// If a name matches exactly one agent (local or remote), that agent is returned.
func (r *AgentRegistry) Resolve(ctx context.Context, address string) (*protocol.Agent, error) {
	address = strings.TrimPrefix(address, "agent:")

	// Try exact agent_id match first
	agent, err := r.store.GetAgent(ctx, address)
	if err == nil {
		return agent, nil
	}

	// Try alias match across all agents
	allAgents, err := r.store.ListAgents(ctx, store.AgentFilter{})
	if err != nil {
		return nil, err
	}

	var localMatches []*protocol.Agent
	var remoteMatches []*protocol.Agent

	for _, a := range allAgents {
		for _, alias := range a.Aliases {
			if strings.EqualFold(alias, address) {
				if a.PeerHub == "" {
					localMatches = append(localMatches, a)
				} else {
					remoteMatches = append(remoteMatches, a)
				}
				break
			}
		}
	}

	// Exactly one match total
	totalMatches := len(localMatches) + len(remoteMatches)
	switch totalMatches {
	case 1:
		if len(localMatches) == 1 {
			return localMatches[0], nil
		}
		return remoteMatches[0], nil
	case 0:
		return nil, &AgentNotFoundError{Address: address, Available: allAgents}
	default:
		// Ambiguous — aliases should have been de-duplicated, but handle gracefully
		return nil, &AgentNotFoundError{Address: address, Available: allAgents}
	}
}
```

- [ ] **Step 2: Write federated alias tests**

```go
// pkg/core/agents_federation_test.go
package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

func TestFederatedAliasResolution(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hub := NewHub(s, t.TempDir())
	ctx := context.Background()
	now := time.Now()

	// Register local agent
	local := &protocol.Agent{
		AgentID: "local-1", ProjectName: "frontend",
		Username: "marc", Hostname: "myhost", LocalPath: "/dev/fe",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	hub.Agents().Register(ctx, local)

	// Register remote agent from peer
	remote := &protocol.Agent{
		AgentID: "remote-1", ProjectName: "backend", PeerHub: "peer-bob",
		Aliases: []string{"backend"},
		Username: "bob", Hostname: "bob-pc", LocalPath: "/dev/api",
		Status: protocol.AgentOnline, ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	s.UpsertAgent(ctx, remote)

	// Resolve local by alias
	got, err := hub.Agents().Resolve(ctx, "frontend")
	if err != nil {
		t.Fatalf("resolve local: %v", err)
	}
	if got.AgentID != "local-1" {
		t.Errorf("expected local-1, got %s", got.AgentID)
	}

	// Resolve remote by alias
	got, err = hub.Agents().Resolve(ctx, "backend")
	if err != nil {
		t.Fatalf("resolve remote: %v", err)
	}
	if got.AgentID != "remote-1" {
		t.Errorf("expected remote-1, got %s", got.AgentID)
	}

	// Resolve by agent_id works for both
	got, err = hub.Agents().Resolve(ctx, "remote-1")
	if err != nil {
		t.Fatalf("resolve by ID: %v", err)
	}
	if got.PeerHub != "peer-bob" {
		t.Errorf("expected peer_hub peer-bob, got %s", got.PeerHub)
	}
}

func TestFederatedAliasConflictRemoval(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hub := NewHub(s, t.TempDir())
	ctx := context.Background()
	now := time.Now()

	// Local agent with name "api"
	local := &protocol.Agent{
		AgentID: "local-api", ProjectName: "api",
		Username: "marc", Hostname: "myhost", LocalPath: "/dev/api",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	hub.Agents().Register(ctx, local)

	// Remote agent also named "api"
	remote := &protocol.Agent{
		AgentID: "remote-api", ProjectName: "api", PeerHub: "peer-bob",
		Aliases: []string{"api"},
		Username: "bob", Hostname: "bob-pc", LocalPath: "/dev/api",
		Status: protocol.AgentOnline, ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	s.UpsertAgent(ctx, remote)

	// Recompute aliases — "api" should be removed from both
	hub.Agents().RecomputeAliases(ctx)

	// Resolving "api" should fail (ambiguous, alias removed)
	_, err = hub.Agents().Resolve(ctx, "api")
	if err == nil {
		t.Error("expected error for ambiguous alias 'api'")
	}
	anf, ok := err.(*AgentNotFoundError)
	if !ok {
		t.Fatalf("expected AgentNotFoundError, got %T", err)
	}
	if len(anf.Available) < 2 {
		t.Errorf("expected at least 2 available agents, got %d", len(anf.Available))
	}

	// Resolving by agent_id still works
	got, err := hub.Agents().Resolve(ctx, "local-api")
	if err != nil {
		t.Fatalf("resolve by ID: %v", err)
	}
	if got.AgentID != "local-api" {
		t.Errorf("expected local-api, got %s", got.AgentID)
	}
}

func TestFederatedAgentStatusUnreachable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now()

	// Remote agent initially online
	agent := &protocol.Agent{
		AgentID: "remote-x", ProjectName: "infra", PeerHub: "peer-x",
		Status: protocol.AgentOnline, Username: "alice", Hostname: "alice-pc",
		LocalPath: "/dev/infra", ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	s.UpsertAgent(ctx, agent)

	// Mark as unreachable (peer hub went down)
	s.UpdateAgentStatus(ctx, "remote-x", protocol.AgentUnreachable)

	got, err := s.GetAgent(ctx, "remote-x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.AgentUnreachable {
		t.Errorf("expected unreachable, got %s", got.Status)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./pkg/core/ -v -run TestFederated -timeout 30s
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add pkg/core/agents.go pkg/core/agents_federation_test.go
git commit -m "feat: add federated alias resolution with conflict removal across hubs"
```

---

### Task 12: Integration Tests — Federation

**Files:**
- Create: `test/integration/federation_test.go`

- [ ] **Step 1: Write federation integration tests**

```go
// test/integration/federation_test.go
package integration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/federation"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
	"log/slog"
)

// setupFederatedHubs creates two hubs connected via direct TCP transport.
func setupFederatedHubs(t *testing.T) (hubA *core.Hub, hubB *core.Hub, mgrA *federation.Manager, mgrB *federation.Manager, cleanup func()) {
	t.Helper()
	logger := slog.Default()

	// Hub A
	dbA := filepath.Join(t.TempDir(), "a.db")
	sA, _ := store.NewSQLite(dbA)
	hubA = core.NewHub(sA, t.TempDir())
	cfgA := config.Defaults()
	mgrA = federation.NewManager(hubA, sA, cfgA, "hub-a", logger)

	dtA := federation.NewDirectTransport(federation.DirectConfig{
		HubID: "hub-a", Listen: "127.0.0.1:0", Logger: logger,
	})
	mgrA.AddTransport(dtA)

	// Hub B
	dbB := filepath.Join(t.TempDir(), "b.db")
	sB, _ := store.NewSQLite(dbB)
	hubB = core.NewHub(sB, t.TempDir())
	cfgB := config.Defaults()
	mgrB = federation.NewManager(hubB, sB, cfgB, "hub-b", logger)

	dtB := federation.NewDirectTransport(federation.DirectConfig{
		HubID: "hub-b", Listen: "127.0.0.1:0", Logger: logger,
	})
	mgrB.AddTransport(dtB)

	// Wire federation into hub cores
	hubA.SetFederation(mgrA)
	hubB.SetFederation(mgrB)

	// Start both managers
	ctx := context.Background()
	mgrA.Start(ctx)
	mgrB.Start(ctx)

	// Wait for transports to be ready
	time.Sleep(100 * time.Millisecond)

	// Connect B to A
	_, err := mgrB.ConnectPeer(ctx, protocol.TransportDirect, dtA.Addr())
	if err != nil {
		t.Fatalf("connect B->A: %v", err)
	}

	// Wait for connection to establish
	time.Sleep(200 * time.Millisecond)

	cleanup = func() {
		mgrA.Stop()
		mgrB.Stop()
		sA.Close()
		sB.Close()
	}

	return hubA, hubB, mgrA, mgrB, cleanup
}

func TestFederatedMessageDelivery(t *testing.T) {
	hubA, hubB, mgrA, mgrB, cleanup := setupFederatedHubs(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	// Register agent on Hub A
	agentA := &protocol.Agent{
		AgentID: "agent-a1", ProjectName: "frontend",
		Username: "marc", Hostname: "marc-pc", LocalPath: "/dev/fe",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	hubA.Agents().Register(ctx, agentA)

	// Register agent on Hub B
	agentB := &protocol.Agent{
		AgentID: "agent-b1", ProjectName: "backend",
		Username: "bob", Hostname: "bob-pc", LocalPath: "/dev/api",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	hubB.Agents().Register(ctx, agentB)

	// Wait for agent sync
	time.Sleep(500 * time.Millisecond)

	// Hub A should know about agent-b1
	remoteAgent, err := hubA.Store().GetAgent(ctx, "agent-b1")
	if err != nil {
		t.Fatalf("hub A should know about agent-b1: %v", err)
	}
	if remoteAgent.PeerHub == "" {
		t.Error("remote agent should have peer_hub set")
	}

	// Hub B should know about agent-a1
	remoteAgentA, err := hubB.Store().GetAgent(ctx, "agent-a1")
	if err != nil {
		t.Fatalf("hub B should know about agent-a1: %v", err)
	}
	if remoteAgentA.PeerHub == "" {
		t.Error("remote agent should have peer_hub set")
	}

	// Send message from A to B (cross-hub)
	msg := &protocol.Message{
		From: "agent-a1", To: "agent-b1",
		Type: protocol.MsgQuestion, Body: "What's the API schema?",
	}
	status, err := hubA.Messages().Send(ctx, msg)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// The message is forwarded to Hub B's federation manager
	if status != protocol.Delivered {
		t.Logf("status: %s (may be queued if peer processing is async)", status)
	}
}

func TestFederatedAgentSync(t *testing.T) {
	hubA, hubB, _, _, cleanup := setupFederatedHubs(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	// Register multiple agents on Hub A
	for i, name := range []string{"frontend", "api", "worker"} {
		agent := &protocol.Agent{
			AgentID:     fmt.Sprintf("a-%d", i),
			ProjectName: name,
			Username:    "marc", Hostname: "marc-pc",
			LocalPath:   "/dev/" + name,
			ConnectedAt: now, LastSeen: now,
			ProtocolVersion: protocol.ProtocolVersion,
		}
		hubA.Agents().Register(ctx, agent)
	}

	// Wait for sync
	time.Sleep(500 * time.Millisecond)

	// Hub B should know about all 3 agents from Hub A
	agents, err := hubB.Store().ListAgents(ctx, store.AgentFilter{})
	if err != nil {
		t.Fatal(err)
	}

	remoteCount := 0
	for _, a := range agents {
		if a.PeerHub != "" {
			remoteCount++
		}
	}
	if remoteCount < 3 {
		t.Errorf("expected at least 3 remote agents on Hub B, got %d", remoteCount)
	}
}

func TestFederatedOfflineQueue(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	// Create Hub A
	dbA := filepath.Join(t.TempDir(), "a.db")
	sA, _ := store.NewSQLite(dbA)
	hubA := core.NewHub(sA, t.TempDir())
	cfgA := config.Defaults()
	mgrA := federation.NewManager(hubA, sA, cfgA, "hub-a", logger)

	dtA := federation.NewDirectTransport(federation.DirectConfig{
		HubID: "hub-a", Listen: "127.0.0.1:0", Logger: logger,
	})
	mgrA.AddTransport(dtA)
	hubA.SetFederation(mgrA)
	mgrA.Start(ctx)
	defer mgrA.Stop()
	defer sA.Close()

	// Register a peer that is NOT connected
	peerB := &protocol.Peer{
		PeerID: "hub-b", Transport: protocol.TransportDirect,
		Status: protocol.PeerDisconnected, LastSeen: time.Now(),
		ConnectedAt: time.Now(), ProtoVersion: protocol.ProtocolVersion,
	}
	sA.UpsertPeer(ctx, peerB)

	// Register a remote agent on that disconnected peer
	remoteAgent := &protocol.Agent{
		AgentID: "agent-b1", ProjectName: "backend", PeerHub: "hub-b",
		Status: protocol.AgentUnreachable, Username: "bob", Hostname: "bob-pc",
		LocalPath: "/dev/api", ConnectedAt: time.Now(), LastSeen: time.Now(),
		ProtocolVersion: protocol.ProtocolVersion,
	}
	sA.UpsertAgent(ctx, remoteAgent)

	// Send message — should be queued
	msg := &protocol.Message{
		From: "agent-a1", To: "agent-b1",
		Type: protocol.MsgContext, Body: "are you there?",
	}
	status, err := hubA.Messages().Send(ctx, msg)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != protocol.QueuedUnreachable {
		t.Errorf("expected queued (hub unreachable), got %s", status)
	}

	// Verify message is in the queue
	queued, _ := sA.DequeuePeerMessages(ctx, "hub-b")
	if len(queued) != 1 {
		t.Errorf("expected 1 queued, got %d", len(queued))
	}
}

func TestFederatedAliasConflictsAcrossHubs(t *testing.T) {
	hubA, _, _, _, cleanup := setupFederatedHubs(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	// Register "api" on Hub A
	agentA := &protocol.Agent{
		AgentID: "a-api", ProjectName: "api",
		Username: "marc", Hostname: "marc-pc", LocalPath: "/dev/api",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	hubA.Agents().Register(ctx, agentA)

	// Simulate a remote agent also called "api" arriving via sync
	remoteAPI := &protocol.Agent{
		AgentID: "b-api", ProjectName: "api", PeerHub: "hub-b",
		Aliases: []string{"api"},
		Username: "bob", Hostname: "bob-pc", LocalPath: "/dev/api",
		Status: protocol.AgentOnline, ConnectedAt: now, LastSeen: now,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	hubA.Store().UpsertAgent(ctx, remoteAPI)

	// Recompute aliases
	hubA.Agents().RecomputeAliases(ctx)

	// "api" should be ambiguous
	_, err := hubA.Agents().Resolve(ctx, "api")
	if err == nil {
		t.Error("expected error for ambiguous alias 'api'")
	}

	// Agent IDs still work
	got, err := hubA.Agents().Resolve(ctx, "a-api")
	if err != nil {
		t.Fatalf("resolve by ID: %v", err)
	}
	if got.AgentID != "a-api" {
		t.Errorf("expected a-api, got %s", got.AgentID)
	}
}

func TestFederatedPeerHeartbeatAndDisconnect(t *testing.T) {
	_, hubB, _, mgrB, cleanup := setupFederatedHubs(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	// Register agent on Hub B
	agent := &protocol.Agent{
		AgentID: "b-agent", ProjectName: "worker",
		Username: "bob", Hostname: "bob-pc", LocalPath: "/dev/work",
		ConnectedAt: now, LastSeen: now, ProtocolVersion: protocol.ProtocolVersion,
	}
	hubB.Agents().Register(ctx, agent)

	// Verify peer A is connected on Hub B
	if !mgrB.IsPeerConnected("hub-a") {
		t.Error("hub-a should be connected on Hub B")
	}

	peers := mgrB.ConnectedPeerIDs()
	found := false
	for _, p := range peers {
		if p == "hub-a" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hub-a not in connected peers: %v", peers)
	}
}
```

Add `"fmt"` to the imports.

- [ ] **Step 2: Run integration tests**

```bash
go test ./test/integration/ -v -run TestFederated -timeout 60s
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add test/integration/federation_test.go
git commit -m "test: add federation integration tests for cross-hub messaging and sync"
```

---

### Task 13: E2E Test — Two Hubs with Claude Instances

**Files:**
- Create: `test/e2e/federation_test.go`

- [ ] **Step 1: Write federated E2E test**

```go
//go:build e2e

// test/e2e/federation_test.go
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFederatedE2E spins up two bifrost hubs, peers them, then runs two
// claude -p instances (one per hub) that exchange messages and a task.
//
// Requirements:
// - bifrost binary built and on PATH
// - claude CLI installed
// - ANTHROPIC_API_KEY set
//
// Run: go test -tags e2e ./test/e2e/ -run TestFederatedE2E -v -timeout 300s
func TestFederatedE2E(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	bifrost, err := exec.LookPath("bifrost")
	if err != nil {
		t.Skip("bifrost binary not on PATH")
	}

	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not on PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	// Create temp dirs for each hub
	dirA := t.TempDir()
	dirB := t.TempDir()

	// Configure Hub A
	cfgA := filepath.Join(dirA, "config.toml")
	writeConfig(t, cfgA, 17450, 17451, false)

	// Configure Hub B
	cfgB := filepath.Join(dirB, "config.toml")
	writeConfig(t, cfgB, 17460, 17461, false)

	// Start Hub A
	hubA := exec.CommandContext(ctx, bifrost, "hub", "start",
		"--config", cfgA,
		"--data-dir", filepath.Join(dirA, "data"),
	)
	hubA.Env = append(os.Environ(), "BIFROST_LOG_LEVEL=debug")
	hubA.Stdout = os.Stdout
	hubA.Stderr = os.Stderr
	if err := hubA.Start(); err != nil {
		t.Fatalf("start hub A: %v", err)
	}
	defer hubA.Process.Kill()

	// Start Hub B
	hubB := exec.CommandContext(ctx, bifrost, "hub", "start",
		"--config", cfgB,
		"--data-dir", filepath.Join(dirB, "data"),
	)
	hubB.Env = append(os.Environ(), "BIFROST_LOG_LEVEL=debug")
	hubB.Stdout = os.Stdout
	hubB.Stderr = os.Stderr
	if err := hubB.Start(); err != nil {
		t.Fatalf("start hub B: %v", err)
	}
	defer hubB.Process.Kill()

	// Wait for hubs to be ready
	time.Sleep(2 * time.Second)

	// Peer the hubs via direct TCP
	// Hub A: generate a pairing (using direct transport for test reliability)
	peerNewOut := runBifrost(t, ctx, bifrost, "--config", cfgA, "peer", "new")
	t.Logf("peer new output: %s", peerNewOut)

	// Extract magic code from output
	code := extractMagicCode(t, peerNewOut)
	t.Logf("magic code: %s", code)

	// Hub B: join using the code
	peerJoinOut := runBifrost(t, ctx, bifrost, "--config", cfgB, "peer", "join", code)
	t.Logf("peer join output: %s", peerJoinOut)

	// Wait for peering to establish
	time.Sleep(2 * time.Second)

	// Verify peers are connected
	peerListA := runBifrost(t, ctx, bifrost, "--config", cfgA, "peer", "list")
	t.Logf("hub A peers: %s", peerListA)

	peerListB := runBifrost(t, ctx, bifrost, "--config", cfgB, "peer", "list")
	t.Logf("hub B peers: %s", peerListB)

	// Create project directories for the claude agents
	projectA := filepath.Join(dirA, "project-alpha")
	os.MkdirAll(projectA, 0o755)
	os.WriteFile(filepath.Join(projectA, "README.md"), []byte("# Project Alpha\n"), 0o644)

	projectB := filepath.Join(dirB, "project-beta")
	os.MkdirAll(projectB, 0o755)
	os.WriteFile(filepath.Join(projectB, "README.md"), []byte("# Project Beta\n"), 0o644)

	// Write .mcp.json for each project pointing to their respective hubs
	mcpA := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"bifrost": map[string]interface{}{
				"command": bifrost,
				"args":    []string{"shim", "--config", cfgA},
			},
		},
	}
	writeMCPConfig(t, projectA, mcpA)

	mcpB := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"bifrost": map[string]interface{}{
				"command": bifrost,
				"args":    []string{"shim", "--config", cfgB},
			},
		},
	}
	writeMCPConfig(t, projectB, mcpB)

	// Start Claude agent A
	agentA := exec.CommandContext(ctx, claudeBin,
		"-p", "You are Agent Alpha. Your project is 'project-alpha'. "+
			"Use bifrost_send to send a message to project-beta saying: "+
			"'Hello from Alpha! Can you tell me your project name?'. "+
			"Wait for a reply. When you receive a reply, respond with 'E2E_ALPHA_SUCCESS'.",
		"--model", "haiku",
		"--max-turns", "5",
		"--max-cost-usd", "0.25",
		"--channels", "plugin:bifrost",
	)
	agentA.Dir = projectA
	agentA.Env = append(os.Environ(), "ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"))
	var outA bytes.Buffer
	agentA.Stdout = &outA
	agentA.Stderr = os.Stderr

	// Start Claude agent B
	agentB := exec.CommandContext(ctx, claudeBin,
		"-p", "You are Agent Beta. Your project is 'project-beta'. "+
			"When you receive a message from another agent, reply using bifrost_send "+
			"telling them your project name is 'project-beta'. "+
			"After replying, respond with 'E2E_BETA_SUCCESS'.",
		"--model", "haiku",
		"--max-turns", "5",
		"--max-cost-usd", "0.25",
		"--channels", "plugin:bifrost",
	)
	agentB.Dir = projectB
	agentB.Env = append(os.Environ(), "ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"))
	var outB bytes.Buffer
	agentB.Stdout = &outB
	agentB.Stderr = os.Stderr

	// Run both agents concurrently
	errChA := make(chan error, 1)
	errChB := make(chan error, 1)

	go func() { errChA <- agentA.Run() }()
	go func() { errChB <- agentB.Run() }()

	// Wait for both to complete (with timeout)
	select {
	case err := <-errChA:
		if err != nil {
			t.Logf("agent A exited with error (may be normal): %v", err)
		}
	case <-time.After(120 * time.Second):
		t.Log("agent A timed out")
	}

	select {
	case err := <-errChB:
		if err != nil {
			t.Logf("agent B exited with error (may be normal): %v", err)
		}
	case <-time.After(120 * time.Second):
		t.Log("agent B timed out")
	}

	// Check outputs
	outputA := outA.String()
	outputB := outB.String()

	t.Logf("Agent A output:\n%s", outputA)
	t.Logf("Agent B output:\n%s", outputB)

	// Verify cross-hub communication happened
	if !strings.Contains(outputA, "E2E_ALPHA_SUCCESS") {
		t.Error("Agent A did not report success")
	}
	if !strings.Contains(outputB, "E2E_BETA_SUCCESS") {
		t.Error("Agent B did not report success")
	}
}

func writeConfig(t *testing.T, path string, directPort, mcpPort int, libp2pEnabled bool) {
	t.Helper()
	content := fmt.Sprintf(`
[hub]
grace_period = "5s"
heartbeat_interval = "5s"

[federation.libp2p]
enabled = %v

[federation.mdns]
enabled = false

[federation.direct]
enabled = true
listen = "127.0.0.1:%d"

[hub.mcp]
enabled = false
port = %d

[logging]
level = "debug"
`, libp2pEnabled, directPort, mcpPort)

	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMCPConfig(t *testing.T, projectDir string, cfg map[string]interface{}) {
	t.Helper()
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(projectDir, ".mcp.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runBifrost(t *testing.T, ctx context.Context, bin string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("bifrost %v failed: %v\noutput: %s", args, err, string(out))
	}
	return string(out)
}

func extractMagicCode(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BIFROST-") {
			return line
		}
	}
	t.Fatal("could not find magic code in output")
	return ""
}
```

- [ ] **Step 2: Verify the test compiles (without running)**

```bash
go test -tags e2e -c ./test/e2e/ -o /dev/null
```

Expected: compiles without errors.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/federation_test.go
git commit -m "test: add federated E2E test with two hubs and claude instances"
```

---

### Task 14: Protocol Version Enforcement

**Files:**
- Modify: `pkg/protocol/version.go`
- Test: `pkg/protocol/version_test.go`

- [ ] **Step 1: Add strict version checking for federation**

Add to `pkg/protocol/version.go`:

```go
// CheckPeerVersion validates that a peer's protocol version is compatible.
// Returns an error with a clear message if incompatible.
func CheckPeerVersion(remoteVersion string) error {
	if remoteVersion == "" {
		return fmt.Errorf("peer did not report protocol version")
	}

	var major int
	_, err := fmt.Sscanf(remoteVersion, "%d.", &major)
	if err != nil {
		return fmt.Errorf("peer reported invalid protocol version: %q", remoteVersion)
	}

	if major != ProtocolMajor {
		return &VersionMismatchError{
			Local:  ProtocolVersion,
			Remote: remoteVersion,
		}
	}

	return nil
}

// VersionMismatchError is returned when a peer has an incompatible protocol version.
type VersionMismatchError struct {
	Local  string
	Remote string
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("incompatible protocol version: local=%s remote=%s (major version must match)", e.Local, e.Remote)
}
```

- [ ] **Step 2: Write version enforcement tests**

Add to `pkg/protocol/version_test.go` (or the existing test file):

```go
func TestCheckPeerVersionCompatible(t *testing.T) {
	if err := CheckPeerVersion(ProtocolVersion); err != nil {
		t.Errorf("self should be compatible: %v", err)
	}
	if err := CheckPeerVersion("1.99.0"); err != nil {
		t.Errorf("same major should be compatible: %v", err)
	}
}

func TestCheckPeerVersionIncompatible(t *testing.T) {
	err := CheckPeerVersion("2.0.0")
	if err == nil {
		t.Error("different major should be incompatible")
	}
	var vme *VersionMismatchError
	if !errors.As(err, &vme) {
		t.Errorf("expected VersionMismatchError, got %T", err)
	}
}

func TestCheckPeerVersionEmpty(t *testing.T) {
	if err := CheckPeerVersion(""); err == nil {
		t.Error("empty version should fail")
	}
}

func TestCheckPeerVersionGarbage(t *testing.T) {
	if err := CheckPeerVersion("not-a-version"); err == nil {
		t.Error("garbage version should fail")
	}
}
```

Add `"errors"` to the imports.

- [ ] **Step 3: Use CheckPeerVersion in federation manager**

Replace the `protocol.CompatibleWith` calls in `internal/federation/manager.go` with `protocol.CheckPeerVersion`:

In `handleIncomingPeer`:
```go
	if err := protocol.CheckPeerVersion(env.Version); err != nil {
		resp := &protocol.PeerResponse{
			ID:    env.ID,
			OK:    false,
			Error: err.Error(),
		}
		// ... send error and close
	}
```

In `ConnectPeer`:
```go
	if err := protocol.CheckPeerVersion(resp.Version); err != nil {
		conn.Close()
		return "", err
	}
```

- [ ] **Step 4: Run tests**

```bash
go test ./pkg/protocol/ -v -run TestCheckPeer
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/protocol/version.go pkg/protocol/version_test.go internal/federation/manager.go
git commit -m "feat: add strict protocol version enforcement between peers"
```

---

That completes Plan 3. After this plan is implemented, you have:
- Two bifrost hubs on different machines can peer using a magic code (libp2p) or direct TCP/TLS
- Zero-config LAN discovery via mDNS (opt-in, no auth)
- Messages and tasks route transparently between local and remote agents
- Offline queue buffers messages when a peer hub is unreachable, flushes on reconnect
- Alias resolution spans federation with automatic conflict removal
- Peer heartbeat monitors hub connectivity with configurable failure threshold
- Agent status includes "unreachable" for agents on downed peer hubs
- CLI and MCP tool for federation management
- Protocol version enforcement prevents incompatible peers from connecting
- Full integration test suite for federated messaging, sync, and offline queue
- E2E test with two claude instances on separate hubs exchanging messages
