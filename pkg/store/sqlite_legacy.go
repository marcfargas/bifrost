// sqlite_legacy.go — implementations of legacy Store methods that are removed
// in Task 5. These methods reference tables that no longer exist after the
// migration (messages, tasks, attachments, message_queue, peer_message_queue).
// They will return errors at runtime. They are kept here only for transitional
// compilation until Task 5 rewrites pkg/core.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// ---- Legacy: Messages -------------------------------------------------------

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

// ---- Legacy: Tasks ----------------------------------------------------------

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

// ---- Legacy: Attachments ----------------------------------------------------

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

// ---- Legacy: Conversation mutations -----------------------------------------

func (s *SQLiteStore) UpdateConversation(ctx context.Context, conv *protocol.Conversation) error {
	parts, err := toJSON(conv.Participants)
	if err != nil {
		return err
	}
	isTask := 0
	if conv.IsTask {
		isTask = 1
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE conversations
SET participants = ?, is_task = ?, closed = ?, closed_reason = ?
WHERE id = ?`,
		parts, isTask,
		boolInt(conv.Closed), string(conv.ClosedReason),
		conv.ConversationID,
	)
	return err
}

func (s *SQLiteStore) TouchConversation(ctx context.Context, conversationID string) error {
	// last_activity column no longer exists; this is a no-op shim.
	return nil
}

func (s *SQLiteStore) ListStaleConversations(ctx context.Context, before time.Time) ([]*protocol.Conversation, error) {
	// last_activity column no longer exists; returns empty list.
	return nil, nil
}

func (s *SQLiteStore) DeleteConversationsBefore(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM conversations WHERE closed = 1 AND created_at < ?`, fmtTime(before))
	return err
}

// ---- Legacy: Offline message queue ------------------------------------------

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

func (s *SQLiteStore) QueueStats(ctx context.Context) ([]QueueEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent_id, COUNT(*) AS cnt, MIN(enqueued_at) AS oldest
FROM message_queue
GROUP BY agent_id
ORDER BY agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQueueEntries(rows)
}

// ---- Legacy: Federation message queue ---------------------------------------

func (s *SQLiteStore) EnqueuePeerMessage(ctx context.Context, peerID string, env *protocol.PeerEnvelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("store: marshal peer envelope: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO peer_message_queue (peer_id, payload, enqueued_at)
VALUES (?,?,?)`,
		peerID, string(payload), fmtTime(time.Now()))
	return err
}

func (s *SQLiteStore) DequeuePeerMessages(ctx context.Context, peerID string) ([]*protocol.PeerEnvelope, error) {
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
SELECT id, payload FROM peer_message_queue
WHERE peer_id = ?
ORDER BY id ASC`, peerID)
	if err != nil {
		return nil, err
	}

	type qrow struct {
		id      int64
		payload string
	}
	var fetched []qrow
	for rows.Next() {
		var r qrow
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

	var envs []*protocol.PeerEnvelope
	for _, r := range fetched {
		var env protocol.PeerEnvelope
		if err = json.Unmarshal([]byte(r.payload), &env); err != nil {
			return nil, fmt.Errorf("store: unmarshal peer envelope: %w", err)
		}
		envs = append(envs, &env)
	}

	if len(fetched) > 0 {
		if _, err = tx.ExecContext(ctx,
			`DELETE FROM peer_message_queue WHERE peer_id = ?`, peerID); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return envs, nil
}

func (s *SQLiteStore) PeerQueueStats(ctx context.Context) ([]QueueEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT peer_id, COUNT(*) AS cnt, MIN(enqueued_at) AS oldest
FROM peer_message_queue
GROUP BY peer_id
ORDER BY peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQueueEntries(rows)
}

func scanQueueEntries(rows *sql.Rows) ([]QueueEntry, error) {
	var entries []QueueEntry
	for rows.Next() {
		var e QueueEntry
		var oldest string
		if err := rows.Scan(&e.Target, &e.Count, &oldest); err != nil {
			return nil, err
		}
		var err error
		if e.Oldest, err = parseTime(oldest); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
