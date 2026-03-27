package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

	db, err := sql.Open(driverName, dbPath)
	if err != nil {
		return nil, fmt.Errorf("store: open db: %w", err)
	}

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

CREATE TABLE IF NOT EXISTS messages (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL DEFAULT '',
    from_agent      TEXT NOT NULL DEFAULT '',
    to_agent        TEXT NOT NULL DEFAULT '',
    type            TEXT NOT NULL DEFAULT '',
    subject         TEXT NOT NULL DEFAULT '',
    body            TEXT NOT NULL DEFAULT '',
    in_reply_to     TEXT NOT NULL DEFAULT '',
    priority        TEXT NOT NULL DEFAULT 'normal',
    timestamp       TEXT NOT NULL,
    acknowledged    INTEGER NOT NULL DEFAULT 0,
    no_reply        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_to           ON messages(to_agent);
CREATE INDEX IF NOT EXISTS idx_messages_timestamp    ON messages(timestamp);

CREATE TABLE IF NOT EXISTS conversations (
    conversation_id TEXT PRIMARY KEY,
    participants    TEXT NOT NULL DEFAULT '[]',
    task_id         TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    last_activity   TEXT NOT NULL,
    closed          INTEGER NOT NULL DEFAULT 0,
    closed_reason   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_conversations_closed         ON conversations(closed);
CREATE INDEX IF NOT EXISTS idx_conversations_last_activity  ON conversations(last_activity);

CREATE TABLE IF NOT EXISTS tasks (
    task_id         TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL DEFAULT '',
    requester       TEXT NOT NULL DEFAULT '',
    assignee        TEXT NOT NULL DEFAULT '',
    title           TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'requested',
    reason          TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    attachments     TEXT NOT NULL DEFAULT '[]',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_conversation ON tasks(conversation_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status       ON tasks(status);

CREATE TABLE IF NOT EXISTS attachments (
    attachment_id TEXT PRIMARY KEY,
    task_id       TEXT NOT NULL DEFAULT '',
    filename      TEXT NOT NULL DEFAULT '',
    content_type  TEXT NOT NULL DEFAULT '',
    size          INTEGER NOT NULL DEFAULT 0,
    uploaded_by   TEXT NOT NULL DEFAULT '',
    uploaded_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_attachments_task       ON attachments(task_id);
CREATE INDEX IF NOT EXISTS idx_attachments_uploaded_at ON attachments(uploaded_at);

CREATE TABLE IF NOT EXISTS subscriptions (
    agent_id TEXT NOT NULL,
    target   TEXT NOT NULL,
    PRIMARY KEY (agent_id, target)
);
CREATE INDEX IF NOT EXISTS idx_subscriptions_target ON subscriptions(target);

CREATE TABLE IF NOT EXISTS message_queue (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id   TEXT NOT NULL,
    message_id TEXT NOT NULL,
    payload    TEXT NOT NULL,
    enqueued_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_message_queue_agent ON message_queue(agent_id, id);
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

// ---- Messages ---------------------------------------------------------------

func (s *SQLiteStore) SaveMessage(ctx context.Context, msg *protocol.Message) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO messages
    (id, conversation_id, from_agent, to_agent, type, subject, body,
     in_reply_to, priority, timestamp, acknowledged, no_reply)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		msg.ID, msg.ConversationID, msg.From, msg.To, string(msg.Type),
		msg.Subject, msg.Body, msg.InReplyTo, string(msg.Priority),
		fmtTime(msg.Timestamp), boolInt(msg.Acknowledged), boolInt(msg.NoReply),
	)
	return err
}

func (s *SQLiteStore) GetMessage(ctx context.Context, messageID string) (*protocol.Message, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, conversation_id, from_agent, to_agent, type, subject, body,
       in_reply_to, priority, timestamp, acknowledged, no_reply
FROM messages WHERE id = ?`, messageID)
	return scanMessage(row)
}

func (s *SQLiteStore) ListMessages(ctx context.Context, filter MessageFilter) ([]*protocol.Message, error) {
	q := `
SELECT id, conversation_id, from_agent, to_agent, type, subject, body,
       in_reply_to, priority, timestamp, acknowledged, no_reply
FROM messages WHERE 1=1`
	args := []any{}
	if filter.ConversationID != "" {
		q += " AND conversation_id = ?"
		args = append(args, filter.ConversationID)
	}
	if filter.To != "" {
		q += " AND to_agent = ?"
		args = append(args, filter.To)
	}
	if filter.From != "" {
		q += " AND from_agent = ?"
		args = append(args, filter.From)
	}
	if filter.Unread {
		q += " AND acknowledged = 0"
	}
	q += " ORDER BY timestamp ASC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *SQLiteStore) AckMessage(ctx context.Context, messageID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE messages SET acknowledged = 1 WHERE id = ?`, messageID)
	return err
}

func (s *SQLiteStore) DeleteMessagesBefore(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM messages WHERE timestamp < ?`, fmtTime(before))
	return err
}

func scanMessage(row *sql.Row) (*protocol.Message, error) {
	var m protocol.Message
	var ts string
	var acked, noReply int
	err := row.Scan(
		&m.ID, &m.ConversationID, &m.From, &m.To, &m.Type,
		&m.Subject, &m.Body, &m.InReplyTo, &m.Priority,
		&ts, &acked, &noReply,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.Acknowledged = acked == 1
	m.NoReply = noReply == 1
	if m.Timestamp, err = parseTime(ts); err != nil {
		return nil, err
	}
	return &m, nil
}

func scanMessages(rows *sql.Rows) ([]*protocol.Message, error) {
	var msgs []*protocol.Message
	for rows.Next() {
		var m protocol.Message
		var ts string
		var acked, noReply int
		if err := rows.Scan(
			&m.ID, &m.ConversationID, &m.From, &m.To, &m.Type,
			&m.Subject, &m.Body, &m.InReplyTo, &m.Priority,
			&ts, &acked, &noReply,
		); err != nil {
			return nil, err
		}
		m.Acknowledged = acked == 1
		m.NoReply = noReply == 1
		var err error
		if m.Timestamp, err = parseTime(ts); err != nil {
			return nil, err
		}
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// ---- Conversations ----------------------------------------------------------

func (s *SQLiteStore) SaveConversation(ctx context.Context, conv *protocol.Conversation) error {
	parts, err := toJSON(conv.Participants)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO conversations
    (conversation_id, participants, task_id, created_at, last_activity, closed, closed_reason)
VALUES (?,?,?,?,?,?,?)`,
		conv.ConversationID, parts, conv.TaskID,
		fmtTime(conv.CreatedAt), fmtTime(conv.LastActivity),
		boolInt(conv.Closed), string(conv.ClosedReason),
	)
	return err
}

func (s *SQLiteStore) GetConversation(ctx context.Context, conversationID string) (*protocol.Conversation, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT conversation_id, participants, task_id, created_at, last_activity, closed, closed_reason
FROM conversations WHERE conversation_id = ?`, conversationID)
	return scanConversation(row)
}

func (s *SQLiteStore) ListConversations(ctx context.Context, filter ConversationFilter) ([]*protocol.Conversation, error) {
	q := `
SELECT conversation_id, participants, task_id, created_at, last_activity, closed, closed_reason
FROM conversations WHERE 1=1`
	args := []any{}
	if filter.TaskID != "" {
		q += " AND task_id = ?"
		args = append(args, filter.TaskID)
	}
	if filter.Closed != nil {
		q += " AND closed = ?"
		args = append(args, boolInt(*filter.Closed))
	}
	// participant filter handled post-scan (JSON array)
	q += " ORDER BY last_activity DESC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	convs, err := scanConversations(rows)
	if err != nil {
		return nil, err
	}
	if filter.Participant == "" {
		return convs, nil
	}
	// filter by participant in-memory
	var result []*protocol.Conversation
	for _, c := range convs {
		if slices.Contains(c.Participants, filter.Participant) {
			result = append(result, c)
		}
	}
	return result, nil
}

func (s *SQLiteStore) CloseConversation(ctx context.Context, conversationID string, reason protocol.ConversationCloseReason) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET closed = 1, closed_reason = ? WHERE conversation_id = ?`,
		string(reason), conversationID)
	return err
}

func (s *SQLiteStore) TouchConversation(ctx context.Context, conversationID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET last_activity = ? WHERE conversation_id = ?`,
		fmtTime(time.Now()), conversationID)
	return err
}

func (s *SQLiteStore) ListStaleConversations(ctx context.Context, before time.Time) ([]*protocol.Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT conversation_id, participants, task_id, created_at, last_activity, closed, closed_reason
FROM conversations
WHERE closed = 0 AND last_activity < ?
ORDER BY last_activity ASC`, fmtTime(before))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConversations(rows)
}

func (s *SQLiteStore) DeleteConversationsBefore(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM conversations WHERE closed = 1 AND created_at < ?`, fmtTime(before))
	return err
}

func scanConversation(row *sql.Row) (*protocol.Conversation, error) {
	var c protocol.Conversation
	var partsRaw, createdAt, lastActivity string
	var closed int
	err := row.Scan(
		&c.ConversationID, &partsRaw, &c.TaskID,
		&createdAt, &lastActivity, &closed, &c.ClosedReason,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Closed = closed == 1
	if err := fromJSON(partsRaw, &c.Participants); err != nil {
		return nil, err
	}
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if c.LastActivity, err = parseTime(lastActivity); err != nil {
		return nil, err
	}
	return &c, nil
}

func scanConversations(rows *sql.Rows) ([]*protocol.Conversation, error) {
	var convs []*protocol.Conversation
	for rows.Next() {
		var c protocol.Conversation
		var partsRaw, createdAt, lastActivity string
		var closed int
		if err := rows.Scan(
			&c.ConversationID, &partsRaw, &c.TaskID,
			&createdAt, &lastActivity, &closed, &c.ClosedReason,
		); err != nil {
			return nil, err
		}
		c.Closed = closed == 1
		if err := fromJSON(partsRaw, &c.Participants); err != nil {
			return nil, err
		}
		var err error
		if c.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if c.LastActivity, err = parseTime(lastActivity); err != nil {
			return nil, err
		}
		convs = append(convs, &c)
	}
	return convs, rows.Err()
}

// ---- Tasks ------------------------------------------------------------------

func (s *SQLiteStore) SaveTask(ctx context.Context, task *protocol.Task) error {
	atts, err := toJSON(task.Attachments)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO tasks
    (task_id, conversation_id, requester, assignee, title, description,
     status, reason, summary, attachments, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.TaskID, task.ConversationID, task.Requester, task.Assignee,
		task.Title, task.Description, string(task.Status), task.Reason,
		task.Summary, atts, fmtTime(task.CreatedAt), fmtTime(task.UpdatedAt),
	)
	return err
}

func (s *SQLiteStore) GetTask(ctx context.Context, taskID string) (*protocol.Task, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT task_id, conversation_id, requester, assignee, title, description,
       status, reason, summary, attachments, created_at, updated_at
FROM tasks WHERE task_id = ?`, taskID)
	return scanTask(row)
}

func (s *SQLiteStore) UpdateTask(ctx context.Context, task *protocol.Task) error {
	atts, err := toJSON(task.Attachments)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE tasks SET
    conversation_id = ?, requester = ?, assignee = ?, title = ?, description = ?,
    status = ?, reason = ?, summary = ?, attachments = ?, created_at = ?, updated_at = ?
WHERE task_id = ?`,
		task.ConversationID, task.Requester, task.Assignee, task.Title, task.Description,
		string(task.Status), task.Reason, task.Summary, atts,
		fmtTime(task.CreatedAt), fmtTime(task.UpdatedAt), task.TaskID,
	)
	return err
}

func (s *SQLiteStore) ListTasks(ctx context.Context, filter TaskFilter) ([]*protocol.Task, error) {
	q := `
SELECT task_id, conversation_id, requester, assignee, title, description,
       status, reason, summary, attachments, created_at, updated_at
FROM tasks WHERE 1=1`
	args := []any{}
	if filter.ConversationID != "" {
		q += " AND conversation_id = ?"
		args = append(args, filter.ConversationID)
	}
	if filter.Requester != "" {
		q += " AND requester = ?"
		args = append(args, filter.Requester)
	}
	if filter.Assignee != "" {
		q += " AND assignee = ?"
		args = append(args, filter.Assignee)
	}
	if filter.Status != "" {
		q += " AND status = ?"
		args = append(args, string(filter.Status))
	}
	q += " ORDER BY created_at ASC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *SQLiteStore) DeleteCompletedTasksBefore(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM tasks
WHERE status IN ('completed','failed','rejected') AND updated_at < ?`,
		fmtTime(before))
	return err
}

func scanTask(row *sql.Row) (*protocol.Task, error) {
	var t protocol.Task
	var attsRaw, createdAt, updatedAt string
	err := row.Scan(
		&t.TaskID, &t.ConversationID, &t.Requester, &t.Assignee, &t.Title, &t.Description,
		&t.Status, &t.Reason, &t.Summary, &attsRaw, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := fromJSON(attsRaw, &t.Attachments); err != nil {
		return nil, err
	}
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func scanTasks(rows *sql.Rows) ([]*protocol.Task, error) {
	var tasks []*protocol.Task
	for rows.Next() {
		var t protocol.Task
		var attsRaw, createdAt, updatedAt string
		if err := rows.Scan(
			&t.TaskID, &t.ConversationID, &t.Requester, &t.Assignee, &t.Title, &t.Description,
			&t.Status, &t.Reason, &t.Summary, &attsRaw, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		if err := fromJSON(attsRaw, &t.Attachments); err != nil {
			return nil, err
		}
		var err error
		if t.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, &t)
	}
	return tasks, rows.Err()
}

// ---- Attachments ------------------------------------------------------------

func (s *SQLiteStore) SaveAttachment(ctx context.Context, att *protocol.Attachment) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO attachments
    (attachment_id, task_id, filename, content_type, size, uploaded_by, uploaded_at)
VALUES (?,?,?,?,?,?,?)`,
		att.AttachmentID, att.TaskID, att.Filename, att.ContentType,
		att.Size, att.UploadedBy, fmtTime(att.UploadedAt),
	)
	return err
}

func (s *SQLiteStore) GetAttachment(ctx context.Context, attachmentID string) (*protocol.Attachment, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT attachment_id, task_id, filename, content_type, size, uploaded_by, uploaded_at
FROM attachments WHERE attachment_id = ?`, attachmentID)

	var a protocol.Attachment
	var uploadedAt string
	err := row.Scan(
		&a.AttachmentID, &a.TaskID, &a.Filename, &a.ContentType,
		&a.Size, &a.UploadedBy, &uploadedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.UploadedAt, err = parseTime(uploadedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *SQLiteStore) ListAttachments(ctx context.Context, taskID string) ([]*protocol.Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT attachment_id, task_id, filename, content_type, size, uploaded_by, uploaded_at
FROM attachments WHERE task_id = ?
ORDER BY uploaded_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var atts []*protocol.Attachment
	for rows.Next() {
		var a protocol.Attachment
		var uploadedAt string
		if err := rows.Scan(
			&a.AttachmentID, &a.TaskID, &a.Filename, &a.ContentType,
			&a.Size, &a.UploadedBy, &uploadedAt,
		); err != nil {
			return nil, err
		}
		if a.UploadedAt, err = parseTime(uploadedAt); err != nil {
			return nil, err
		}
		atts = append(atts, &a)
	}
	return atts, rows.Err()
}

func (s *SQLiteStore) DeleteAttachmentsBefore(ctx context.Context, before time.Time) ([]string, error) {
	// Select IDs first so callers can clean up files.
	rows, err := s.db.QueryContext(ctx,
		`SELECT attachment_id FROM attachments WHERE uploaded_at < ?`, fmtTime(before))
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(ids) > 0 {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM attachments WHERE uploaded_at < ?`, fmtTime(before)); err != nil {
			return nil, err
		}
	}
	return ids, nil
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

// ---- Message queue ----------------------------------------------------------

func (s *SQLiteStore) EnqueueMessage(ctx context.Context, recipientAgentID string, msg *protocol.Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("store: marshal queued message: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO message_queue (agent_id, message_id, payload, enqueued_at)
VALUES (?,?,?,?)`,
		recipientAgentID, msg.ID, string(payload), fmtTime(time.Now()))
	return err
}

func (s *SQLiteStore) DequeueMessages(ctx context.Context, recipientAgentID string) ([]*protocol.Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	rows, err := tx.QueryContext(ctx, `
SELECT id, payload FROM message_queue
WHERE agent_id = ?
ORDER BY id ASC`, recipientAgentID)
	if err != nil {
		return nil, err
	}

	type row struct {
		id      int64
		payload string
	}
	var fetched []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.payload); err != nil {
			rows.Close()
			return nil, err
		}
		fetched = append(fetched, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}

	var msgs []*protocol.Message
	for _, r := range fetched {
		var m protocol.Message
		if err = json.Unmarshal([]byte(r.payload), &m); err != nil {
			return nil, fmt.Errorf("store: unmarshal queued message: %w", err)
		}
		msgs = append(msgs, &m)
	}

	if len(fetched) > 0 {
		if _, err = tx.ExecContext(ctx,
			`DELETE FROM message_queue WHERE agent_id = ?`, recipientAgentID); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return msgs, nil
}

func (s *SQLiteStore) QueuedMessageCount(ctx context.Context, recipientAgentID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM message_queue WHERE agent_id = ?`, recipientAgentID).Scan(&count)
	return count, err
}

// ---- utilities --------------------------------------------------------------

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
