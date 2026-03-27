// Package federation implements hub-to-hub communication for cross-network
// agent collaboration.
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
	hub         *core.Hub
	store       store.Store
	cfg         *config.Config
	logger      *slog.Logger
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

// PeerTransport re-exports for use in ConnectPeer without import cycle.
type PeerTransport = protocol.PeerTransport

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

// Transports returns the registered transports (for inspection by the hub server).
func (m *Manager) Transports() []Transport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]Transport, len(m.transports))
	copy(cp, m.transports)
	return cp
}

// Start begins all registered transports and reconnects known peers.
func (m *Manager) Start(ctx context.Context) error {
	ctx, m.cancel = context.WithCancel(ctx)
	incoming := make(chan PeerConn, 16)

	// Start all transports
	m.mu.RLock()
	for _, t := range m.transports {
		transport := t
		m.wg.Go(func() {
			if err := transport.Start(ctx, incoming); err != nil {
				m.logger.Error("transport start failed", "transport", transport.Name(), "error", err)
			}
		})
	}
	m.mu.RUnlock()

	// Accept incoming peer connections
	m.wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case conn := <-incoming:
				m.handleIncomingPeer(ctx, conn)
			}
		}
	})

	// Reconnect known peers
	m.wg.Go(func() {
		m.reconnectKnownPeers(ctx)
	})

	// Start heartbeat loop
	m.wg.Go(func() {
		m.heartbeatLoop(ctx)
	})

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
		Status:       protocol.PeerStatusConnected,
		LastSeen:     now,
		ConnectedAt:  now,
		ProtoVersion: env.Version,
	}

	// Check if this is a known peer (reconnection)
	existing, err := m.store.GetPeer(ctx, peerID)
	if err == nil && existing != nil {
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
		Status:       protocol.PeerStatusConnected,
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
	m.wg.Go(func() {
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
	})

	// Read loop
	m.wg.Go(func() {
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
	})
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
		return protocol.DeliveryStatusQueuedUnreachable, nil
	}

	return protocol.DeliveryStatusDelivered, nil
}

// ForwardTaskCreate sends a task creation to the assignee's peer hub.
func (m *Manager) ForwardTaskCreate(ctx context.Context, peerID string, task *protocol.Task, attachments []*protocol.PeerAttachmentData) error {
	payload, err := json.Marshal(protocol.PeerTaskCreatePayload{
		Task:        task,
		Attachments: attachments,
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

func (m *Manager) handleHeartbeat(ctx context.Context, _ string, env *protocol.PeerEnvelope) {
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
	if agent.Status == protocol.AgentStatusDND {
		if msg.Priority != protocol.PriorityUrgent {
			m.store.EnqueueMessage(ctx, agent.AgentID, msg)
			return
		}
	}

	status := m.hub.NotifyAgent(agent.AgentID, core.Notification{
		Type:    "message",
		Payload: msg,
	})
	if status == protocol.DeliveryStatusQueuedOffline {
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
		if err != nil || agent == nil || agent.PeerHub != "" {
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

	m.store.UpdateAgentStatus(ctx, payload.AgentID, payload.Status, payload.DNDReason)

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
	m.store.UpdatePeerStatus(ctx, peerID, protocol.PeerStatusDisconnected, 0)

	// Mark all agents on this peer as unreachable
	agents, _ := m.store.ListAgents(ctx, store.AgentFilter{PeerHub: peerID})
	for _, a := range agents {
		m.store.UpdateAgentStatus(ctx, a.AgentID, protocol.AgentStatusUnreachable, "")
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
			peer, getErr := m.store.GetPeer(ctx, peerID)
			if getErr != nil || peer == nil {
				continue
			}
			newFails := peer.FailCount + 1
			maxFails := 3 // N consecutive failures marks unreachable
			if newFails >= maxFails {
				m.logger.Warn("peer unreachable after heartbeat failures",
					"peer_id", peerID, "failures", newFails)
				m.store.UpdatePeerStatus(ctx, peerID, protocol.PeerStatusUnreachable, newFails)
				m.handlePeerDisconnect(peerID)
			} else {
				m.store.UpdatePeerStatus(ctx, peerID, protocol.PeerStatusConnected, newFails)
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
			m.store.UpdatePeerStatus(ctx, peer.PeerID, protocol.PeerStatusDisconnected, peer.FailCount+1)
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
