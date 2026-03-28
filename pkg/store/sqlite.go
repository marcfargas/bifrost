package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

const driverName = "sqlite"

// SQLiteStore is a Store backed by a local SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLite opens (or creates) a SQLite database at dbPath, applies the schema,
// and returns a ready-to-use SQLiteStore.
func NewSQLite(dbPath string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("store: create dirs: %w", err)
	}

	// Use a single SQLite connection. SQLite allows one writer at a time;
	// using a single connection serialises all statements through one OS handle
	// so PRAGMA settings (journal_mode, busy_timeout, foreign_keys) remain in
	// effect for the lifetime of the pool.
	db, err := sql.Open(driverName, dbPath)
	if err != nil {
		return nil, fmt.Errorf("store: open db: %w", err)
	}
	db.SetMaxOpenConns(1)

	// WAL mode + busy timeout
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: pragma %q: %w", p, err)
		}
	}

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	// Phase 1: drop old tables that are replaced by the new schema.
	// v0.1.x has no production data requiring preservation.
	oldTables := []string{"messages", "tasks", "message_queue", "peer_message_queue", "attachments"}
	for _, tbl := range oldTables {
		var name string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&name)
		if err == nil {
			// Table exists — drop it.
			if _, err := s.db.Exec(`DROP TABLE IF EXISTS ` + tbl); err != nil {
				return fmt.Errorf("drop old table %s: %w", tbl, err)
			}
		}
	}

	// Phase 2: create current schema.
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS agents (
    agent_id         TEXT PRIMARY KEY,
    aliases          TEXT NOT NULL DEFAULT '[]',
    username         TEXT NOT NULL DEFAULT '',
    hostname         TEXT NOT NULL DEFAULT '',
    local_path       TEXT NOT NULL DEFAULT '',
    project_name     TEXT NOT NULL DEFAULT '',
    capabilities     TEXT NOT NULL DEFAULT '[]',
    display_name     TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'offline',
    dnd_reason       TEXT NOT NULL DEFAULT '',
    connected_at     TEXT NOT NULL,
    last_seen        TEXT NOT NULL,
    protocol_version TEXT NOT NULL DEFAULT '',
    peer_hub         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS conversations (
    id            TEXT PRIMARY KEY,
    participants  TEXT NOT NULL DEFAULT '[]',
    is_task       INTEGER NOT NULL DEFAULT 0,
    title         TEXT NOT NULL DEFAULT '',
    assignee      TEXT NOT NULL DEFAULT '',
    requester     TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    closed        INTEGER NOT NULL DEFAULT 0,
    closed_reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_conversations_closed    ON conversations(closed);
CREATE INDEX IF NOT EXISTS idx_conversations_is_task   ON conversations(is_task);
CREATE INDEX IF NOT EXISTS idx_conversations_assignee  ON conversations(assignee);
CREATE INDEX IF NOT EXISTS idx_conversations_requester ON conversations(requester);

CREATE TABLE IF NOT EXISTS events (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    type            TEXT NOT NULL,
    from_agent      TEXT NOT NULL,
    data            TEXT NOT NULL,
    timestamp       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_conversation ON events(conversation_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_events_type         ON events(conversation_id, type);

CREATE TABLE IF NOT EXISTS delivery_state (
    target_type     TEXT NOT NULL,
    target_id       TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    last_event_id   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (target_type, target_id, conversation_id)
);
CREATE INDEX IF NOT EXISTS idx_delivery_target ON delivery_state(target_type, target_id);

CREATE TABLE IF NOT EXISTS subscriptions (
    agent_id TEXT NOT NULL,
    target   TEXT NOT NULL,
    PRIMARY KEY (agent_id, target)
);
CREATE INDEX IF NOT EXISTS idx_subscriptions_target ON subscriptions(target);

CREATE TABLE IF NOT EXISTS peers (
    peer_id         TEXT PRIMARY KEY,
    display_name    TEXT NOT NULL DEFAULT '',
    transport       TEXT NOT NULL DEFAULT '',
    address         TEXT NOT NULL DEFAULT '',
    token           TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'disconnected',
    last_seen       TEXT NOT NULL,
    connected_at    TEXT NOT NULL,
    fail_count      INTEGER NOT NULL DEFAULT 0,
    proto_version   TEXT NOT NULL DEFAULT ''
);
`)
	return err
}

// Close releases the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ---- helpers ----------------------------------------------------------------

func toJSON(v any) (string, error) {
	if v == nil {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func fromJSON[T any](raw string, dest *[]T) error {
	if raw == "" || raw == "null" {
		*dest = nil
		return nil
	}
	return json.Unmarshal([]byte(raw), dest)
}

func fmtTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// fallback to second precision
		t, err = time.Parse(time.RFC3339, s)
	}
	return t, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- Agents -----------------------------------------------------------------

func (s *SQLiteStore) UpsertAgent(ctx context.Context, agent *protocol.Agent) error {
	aliases, err := toJSON(agent.Aliases)
	if err != nil {
		return err
	}
	caps, err := toJSON(agent.Capabilities)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO agents
    (agent_id, aliases, username, hostname, local_path, project_name,
     capabilities, display_name, status, dnd_reason, connected_at, last_seen,
     protocol_version, peer_hub)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(agent_id) DO UPDATE SET
    aliases          = excluded.aliases,
    username         = excluded.username,
    hostname         = excluded.hostname,
    local_path       = excluded.local_path,
    project_name     = excluded.project_name,
    capabilities     = excluded.capabilities,
    display_name     = excluded.display_name,
    status           = excluded.status,
    dnd_reason       = excluded.dnd_reason,
    connected_at     = excluded.connected_at,
    last_seen        = excluded.last_seen,
    protocol_version = excluded.protocol_version,
    peer_hub         = excluded.peer_hub
`,
		agent.AgentID, aliases, agent.Username, agent.Hostname, agent.LocalPath,
		agent.ProjectName, caps, agent.DisplayName, string(agent.Status),
		agent.DNDReason, fmtTime(agent.ConnectedAt), fmtTime(agent.LastSeen),
		agent.ProtocolVersion, agent.PeerHub,
	)
	return err
}

func (s *SQLiteStore) GetAgent(ctx context.Context, agentID string) (*protocol.Agent, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT agent_id, aliases, username, hostname, local_path, project_name,
       capabilities, display_name, status, dnd_reason, connected_at, last_seen,
       protocol_version, peer_hub
FROM agents WHERE agent_id = ?`, agentID)
	return scanAgent(row)
}

func (s *SQLiteStore) ListAgents(ctx context.Context, filter AgentFilter) ([]*protocol.Agent, error) {
	q := `
SELECT agent_id, aliases, username, hostname, local_path, project_name,
       capabilities, display_name, status, dnd_reason, connected_at, last_seen,
       protocol_version, peer_hub
FROM agents WHERE 1=1`
	args := []any{}
	if filter.Status != "" {
		q += " AND status = ?"
		args = append(args, string(filter.Status))
	}
	if filter.PeerHub != "" {
		q += " AND peer_hub = ?"
		args = append(args, filter.PeerHub)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func (s *SQLiteStore) DeleteAgent(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE agent_id = ?`, agentID)
	return err
}

func (s *SQLiteStore) UpdateAgentStatus(ctx context.Context, agentID string, status protocol.AgentStatus, dndReason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status = ?, dnd_reason = ? WHERE agent_id = ?`,
		string(status), dndReason, agentID)
	return err
}

func (s *SQLiteStore) TouchAgent(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET last_seen = ? WHERE agent_id = ?`,
		fmtTime(time.Now()), agentID)
	return err
}

func scanAgent(row *sql.Row) (*protocol.Agent, error) {
	var a protocol.Agent
	var aliasesRaw, capsRaw string
	var connectedAt, lastSeen string
	err := row.Scan(
		&a.AgentID, &aliasesRaw, &a.Username, &a.Hostname, &a.LocalPath,
		&a.ProjectName, &capsRaw, &a.DisplayName, &a.Status, &a.DNDReason,
		&connectedAt, &lastSeen, &a.ProtocolVersion, &a.PeerHub,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := fromJSON(aliasesRaw, &a.Aliases); err != nil {
		return nil, err
	}
	if err := fromJSON(capsRaw, &a.Capabilities); err != nil {
		return nil, err
	}
	if a.ConnectedAt, err = parseTime(connectedAt); err != nil {
		return nil, err
	}
	if a.LastSeen, err = parseTime(lastSeen); err != nil {
		return nil, err
	}
	return &a, nil
}

func scanAgents(rows *sql.Rows) ([]*protocol.Agent, error) {
	var agents []*protocol.Agent
	for rows.Next() {
		var a protocol.Agent
		var aliasesRaw, capsRaw string
		var connectedAt, lastSeen string
		if err := rows.Scan(
			&a.AgentID, &aliasesRaw, &a.Username, &a.Hostname, &a.LocalPath,
			&a.ProjectName, &capsRaw, &a.DisplayName, &a.Status, &a.DNDReason,
			&connectedAt, &lastSeen, &a.ProtocolVersion, &a.PeerHub,
		); err != nil {
			return nil, err
		}
		if err := fromJSON(aliasesRaw, &a.Aliases); err != nil {
			return nil, err
		}
		if err := fromJSON(capsRaw, &a.Capabilities); err != nil {
			return nil, err
		}
		var err error
		if a.ConnectedAt, err = parseTime(connectedAt); err != nil {
			return nil, err
		}
		if a.LastSeen, err = parseTime(lastSeen); err != nil {
			return nil, err
		}
		agents = append(agents, &a)
	}
	return agents, rows.Err()
}

// ---- Conversations ----------------------------------------------------------

func (s *SQLiteStore) SaveConversation(ctx context.Context, conv *protocol.Conversation) error {
	parts, err := toJSON(conv.Participants)
	if err != nil {
		return err
	}
	isTask := 0
	if conv.IsTask {
		isTask = 1
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO conversations (id, participants, is_task, title, assignee, requester, created_at, closed, closed_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, '')`,
		conv.ConversationID, parts, isTask,
		conv.Title, conv.Assignee, conv.Requester,
		fmtTime(conv.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) GetConversation(ctx context.Context, conversationID string) (*protocol.Conversation, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, participants, is_task, title, assignee, requester, created_at, closed, closed_reason
FROM conversations WHERE id = ?`, conversationID)
	return scanConversation(row)
}

func (s *SQLiteStore) ListConversations(ctx context.Context, filter ConversationFilter) ([]*protocol.Conversation, error) {
	q := `SELECT id, participants, is_task, title, assignee, requester, created_at, closed, closed_reason
FROM conversations WHERE 1=1`
	var args []any
	if filter.Closed != nil {
		closed := 0
		if *filter.Closed {
			closed = 1
		}
		q += ` AND closed = ?`
		args = append(args, closed)
	}
	if filter.IsTask != nil {
		isTask := 0
		if *filter.IsTask {
			isTask = 1
		}
		q += ` AND is_task = ?`
		args = append(args, isTask)
	}
	if filter.Assignee != "" {
		q += ` AND assignee = ?`
		args = append(args, filter.Assignee)
	}
	if filter.Requester != "" {
		q += ` AND requester = ?`
		args = append(args, filter.Requester)
	}
	if filter.Participant != "" {
		q += ` AND participants LIKE ?`
		args = append(args, "%"+filter.Participant+"%")
	}
	q += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var convs []*protocol.Conversation
	for rows.Next() {
		conv, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		convs = append(convs, conv)
	}
	return convs, rows.Err()
}

func (s *SQLiteStore) CloseConversation(ctx context.Context, conversationID string, reason protocol.ConversationCloseReason) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET closed = 1, closed_reason = ? WHERE id = ?`,
		string(reason), conversationID,
	)
	return err
}

// scanConversationRow is a shared interface satisfied by both *sql.Row and *sql.Rows.
type scanConversationRow interface {
	Scan(...any) error
}

func scanConversation(row scanConversationRow) (*protocol.Conversation, error) {
	var (
		id, partsJSON, title, assignee, requester, createdAtStr, closedReason string
		isTask, closed                                                        int
	)
	err := row.Scan(&id, &partsJSON, &isTask, &title, &assignee, &requester,
		&createdAtStr, &closed, &closedReason)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	createdAt, _ := parseTime(createdAtStr)
	var parts []string
	_ = fromJSON(partsJSON, &parts)
	return &protocol.Conversation{
		ConversationID: id,
		Participants:   parts,
		IsTask:         isTask == 1,
		Title:          title,
		Assignee:       assignee,
		Requester:      requester,
		CreatedAt:      createdAt,
		Closed:         closed == 1,
		ClosedReason:   protocol.ConversationCloseReason(closedReason),
	}, nil
}

// ---- Events -----------------------------------------------------------------

func (s *SQLiteStore) AppendEvent(ctx context.Context, ev *protocol.Event) error {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO events (id, conversation_id, type, from_agent, data, timestamp)
VALUES (?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.ConversationID, string(ev.Type), ev.FromAgent,
		string(data), fmtTime(ev.Timestamp),
	)
	return err
}

func (s *SQLiteStore) GetEvent(ctx context.Context, eventID string) (*protocol.Event, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, type, from_agent, data, timestamp FROM events WHERE id = ?`,
		eventID)
	return scanEvent(row)
}

func (s *SQLiteStore) ListEventsSince(ctx context.Context, conversationID, afterEventID string) ([]*protocol.Event, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if afterEventID == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, conversation_id, type, from_agent, data, timestamp
             FROM events WHERE conversation_id = ? ORDER BY timestamp ASC`,
			conversationID)
	} else {
		// Find the timestamp of the last-seen event, then return everything after it.
		var afterTS string
		qErr := s.db.QueryRowContext(ctx,
			`SELECT timestamp FROM events WHERE id = ?`, afterEventID,
		).Scan(&afterTS)
		if qErr == sql.ErrNoRows {
			// afterEventID unknown — return all events.
			rows, err = s.db.QueryContext(ctx,
				`SELECT id, conversation_id, type, from_agent, data, timestamp
                 FROM events WHERE conversation_id = ? ORDER BY timestamp ASC`,
				conversationID)
		} else if qErr != nil {
			return nil, qErr
		} else {
			rows, err = s.db.QueryContext(ctx,
				`SELECT id, conversation_id, type, from_agent, data, timestamp
                 FROM events
                 WHERE conversation_id = ? AND (timestamp > ? OR (timestamp = ? AND id > ?))
                 ORDER BY timestamp ASC, id ASC`,
				conversationID, afterTS, afterTS, afterEventID)
		}
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evs []*protocol.Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		evs = append(evs, ev)
	}
	return evs, rows.Err()
}

func (s *SQLiteStore) LatestStatusEvent(ctx context.Context, conversationID string) (*protocol.Event, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, type, from_agent, data, timestamp
         FROM events
         WHERE conversation_id = ? AND type = 'status'
         ORDER BY timestamp DESC, id DESC LIMIT 1`,
		conversationID)
	return scanEvent(row)
}

// scanEventRow is a shared interface satisfied by both *sql.Row and *sql.Rows.
type scanEventRow interface {
	Scan(...any) error
}

func scanEvent(row scanEventRow) (*protocol.Event, error) {
	var id, convID, evType, fromAgent, dataJSON, tsStr string
	err := row.Scan(&id, &convID, &evType, &fromAgent, &dataJSON, &tsStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ts, _ := parseTime(tsStr)
	var data protocol.EventData
	_ = json.Unmarshal([]byte(dataJSON), &data)
	return &protocol.Event{
		ID:             id,
		ConversationID: convID,
		Type:           protocol.EventType(evType),
		FromAgent:      fromAgent,
		Data:           data,
		Timestamp:      ts,
	}, nil
}

// ---- Delivery State ---------------------------------------------------------

func (s *SQLiteStore) GetDeliveryMark(ctx context.Context, targetType, targetID, conversationID string) (string, error) {
	var lastEventID string
	err := s.db.QueryRowContext(ctx,
		`SELECT last_event_id FROM delivery_state
         WHERE target_type = ? AND target_id = ? AND conversation_id = ?`,
		targetType, targetID, conversationID,
	).Scan(&lastEventID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return lastEventID, err
}

func (s *SQLiteStore) SetDeliveryMark(ctx context.Context, targetType, targetID, conversationID, lastEventID string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO delivery_state (target_type, target_id, conversation_id, last_event_id)
VALUES (?, ?, ?, ?)
ON CONFLICT(target_type, target_id, conversation_id) DO UPDATE SET
    last_event_id = excluded.last_event_id`,
		targetType, targetID, conversationID, lastEventID,
	)
	return err
}

func (s *SQLiteStore) ListPendingDelivery(ctx context.Context, targetType, targetID string) ([]DeliveryMark, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if targetID == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT target_id, conversation_id, last_event_id FROM delivery_state
             WHERE target_type = ?`,
			targetType,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT target_id, conversation_id, last_event_id FROM delivery_state
             WHERE target_type = ? AND target_id = ?`,
			targetType, targetID,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var marks []DeliveryMark
	for rows.Next() {
		var m DeliveryMark
		if err := rows.Scan(&m.TargetID, &m.ConversationID, &m.LastEventID); err != nil {
			return nil, err
		}
		marks = append(marks, m)
	}
	return marks, rows.Err()
}

func (s *SQLiteStore) InitDeliveryTargets(ctx context.Context, conv *protocol.Conversation) error {
	for _, agentID := range conv.Participants {
		// Always create an agent-level delivery target.
		_, err := s.db.ExecContext(ctx, `
INSERT INTO delivery_state (target_type, target_id, conversation_id, last_event_id)
VALUES ('agent', ?, ?, '')
ON CONFLICT DO NOTHING`,
			agentID, conv.ConversationID,
		)
		if err != nil {
			return err
		}

		// If the agent is remote, also create a peer-level delivery target so the
		// SyncEngine can forward events to the peer hub.
		agent, err := s.GetAgent(ctx, agentID)
		if err == nil && agent != nil && agent.PeerHub != "" {
			_, err = s.db.ExecContext(ctx, `
INSERT INTO delivery_state (target_type, target_id, conversation_id, last_event_id)
VALUES ('peer', ?, ?, '')
ON CONFLICT DO NOTHING`,
				agent.PeerHub, conv.ConversationID,
			)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- Subscriptions ----------------------------------------------------------

func (s *SQLiteStore) Subscribe(ctx context.Context, agentID, target string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO subscriptions (agent_id, target) VALUES (?,?)`, agentID, target)
	return err
}

func (s *SQLiteStore) Unsubscribe(ctx context.Context, agentID, target string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM subscriptions WHERE agent_id = ? AND target = ?`, agentID, target)
	return err
}

func (s *SQLiteStore) GetSubscribers(ctx context.Context, target string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id FROM subscriptions WHERE target = ?`, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *SQLiteStore) ListChannels(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT substr(target, 9)
FROM subscriptions
WHERE target LIKE 'channel:%'
ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

// ---- Peers ------------------------------------------------------------------

func (s *SQLiteStore) UpsertPeer(ctx context.Context, peer *protocol.Peer) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO peers
    (peer_id, display_name, transport, address, token, status,
     last_seen, connected_at, fail_count, proto_version)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(peer_id) DO UPDATE SET
    display_name  = excluded.display_name,
    transport     = excluded.transport,
    address       = excluded.address,
    token         = excluded.token,
    status        = excluded.status,
    last_seen     = excluded.last_seen,
    connected_at  = excluded.connected_at,
    fail_count    = excluded.fail_count,
    proto_version = excluded.proto_version
`,
		peer.PeerID, peer.DisplayName, string(peer.Transport), peer.Address,
		peer.Token, string(peer.Status), fmtTime(peer.LastSeen),
		fmtTime(peer.ConnectedAt), peer.FailCount, peer.ProtoVersion,
	)
	return err
}

func (s *SQLiteStore) GetPeer(ctx context.Context, peerID string) (*protocol.Peer, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT peer_id, display_name, transport, address, token, status,
       last_seen, connected_at, fail_count, proto_version
FROM peers WHERE peer_id = ?`, peerID)
	return scanPeer(row)
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
	return scanPeers(rows)
}

func (s *SQLiteStore) DeletePeer(ctx context.Context, peerID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM peers WHERE peer_id = ?`, peerID)
	return err
}

func (s *SQLiteStore) UpdatePeerStatus(ctx context.Context, peerID string, status protocol.PeerStatus, failCount int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE peers SET status = ?, fail_count = ?, last_seen = ? WHERE peer_id = ?`,
		string(status), failCount, fmtTime(time.Now()), peerID)
	return err
}

func (s *SQLiteStore) TouchPeer(ctx context.Context, peerID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE peers SET last_seen = ?, fail_count = 0 WHERE peer_id = ?`,
		fmtTime(time.Now()), peerID)
	return err
}

func scanPeer(row *sql.Row) (*protocol.Peer, error) {
	var p protocol.Peer
	var lastSeen, connectedAt string
	err := row.Scan(
		&p.PeerID, &p.DisplayName, &p.Transport, &p.Address, &p.Token,
		&p.Status, &lastSeen, &connectedAt, &p.FailCount, &p.ProtoVersion,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.LastSeen, err = parseTime(lastSeen); err != nil {
		return nil, err
	}
	if p.ConnectedAt, err = parseTime(connectedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func scanPeers(rows *sql.Rows) ([]*protocol.Peer, error) {
	var peers []*protocol.Peer
	for rows.Next() {
		var p protocol.Peer
		var lastSeen, connectedAt string
		if err := rows.Scan(
			&p.PeerID, &p.DisplayName, &p.Transport, &p.Address, &p.Token,
			&p.Status, &lastSeen, &connectedAt, &p.FailCount, &p.ProtoVersion,
		); err != nil {
			return nil, err
		}
		var err error
		if p.LastSeen, err = parseTime(lastSeen); err != nil {
			return nil, err
		}
		if p.ConnectedAt, err = parseTime(connectedAt); err != nil {
			return nil, err
		}
		peers = append(peers, &p)
	}
	return peers, rows.Err()
}
