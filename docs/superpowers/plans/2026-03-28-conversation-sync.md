# Plan: Conversation Sync Refactoring

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the separate messages/tasks/queues model with a single conversation-event stream. Everything is a conversation. Tasks are conversations with `is_task=true`. A unified sync engine delivers events to both local agents and federated peers using identical mechanics. One delivery mechanism, no special cases.

**Spec:** `docs/superpowers/specs/2026-03-28-conversation-sync-design.md`

**Builds on:** Plans 1–3 (foundation, tasks/channels/DND, federation). All existing agent, peer, subscription, and DND infrastructure is preserved.

**Key invariants:**
- No separate task table — task status derived from latest `status` event in conversation
- `delivery_state` replaces both `message_queue` AND `peer_message_queue`
- Max file size: 256KB (inline base64 in event data)
- `bifrost_create_task` → `bifrost_request_task` (tool rename)
- DND checked on the receiving hub, not the sending hub
- One sync engine path for local agents and federation peers

---

## Phase 1: New Store Layer

### Task 1: New Protocol Types

**Files:**
- Modify: `pkg/protocol/types.go`
- Create: `pkg/protocol/events_test.go`

- [ ] **Step 1: Add Event and ConversationSync types to pkg/protocol/types.go**

Add after the existing `PeerHeartbeatPayload` type:

```go
// EventType classifies what happened in a conversation.
type EventType string

const (
    EventTypeMessage           EventType = "message"
    EventTypeStatus            EventType = "status"
    EventTypeMetadata          EventType = "metadata"
    EventTypeFile              EventType = "file"
    EventTypeParticipantAdded  EventType = "participant.added"
    EventTypeParticipantRemoved EventType = "participant.removed"
)

// MaxFileSizeBytes is the maximum inline file size for file events.
const MaxFileSizeBytes = 256 * 1024 // 256KB

// Event is a single occurrence in a conversation stream.
type Event struct {
    ID             string    `json:"id"`
    ConversationID string    `json:"conversation_id"`
    Type           EventType `json:"type"`
    FromAgent      string    `json:"from_agent"`
    Data           EventData `json:"data"`
    Timestamp      time.Time `json:"timestamp"`
}

// EventData is the polymorphic payload of an Event. Fields are populated
// based on the event type; unused fields are zero-valued.
type EventData struct {
    // message event
    Body        string      `json:"body,omitempty"`
    MessageType MessageType `json:"message_type,omitempty"`
    Priority    Priority    `json:"priority,omitempty"`
    InReplyTo   string      `json:"in_reply_to,omitempty"`

    // status event
    NewStatus TaskStatus `json:"new_status,omitempty"`
    Summary   string     `json:"summary,omitempty"`
    Reason    string     `json:"reason,omitempty"`

    // metadata event
    Field string `json:"field,omitempty"`
    Value string `json:"value,omitempty"`

    // file event
    Filename    string `json:"filename,omitempty"`
    Size        int64  `json:"size,omitempty"`
    ContentType string `json:"content_type,omitempty"`
    Content     []byte `json:"content,omitempty"` // base64 via json.Marshal, max 256KB

    // participant.added / participant.removed
    AgentID string `json:"agent_id,omitempty"`
}

// ConvMeta carries conversation metadata for federation sync.
type ConvMeta struct {
    ID           string    `json:"id"`
    Participants []string  `json:"participants"`
    IsTask       bool      `json:"is_task"`
    Title        string    `json:"title,omitempty"`
    Assignee     string    `json:"assignee,omitempty"`
    Requester    string    `json:"requester,omitempty"`
    CreatedAt    time.Time `json:"created_at"`
}

// PeerConversationSyncPayload is sent via peer.conversation_sync.
type PeerConversationSyncPayload struct {
    Conversation ConvMeta `json:"conversation"`
    Events       []*Event `json:"events"`
}

// PeerConversationSyncAck acknowledges a peer.conversation_sync.
type PeerConversationSyncAck struct {
    ConversationID string `json:"conversation_id"`
    LastEventID    string `json:"last_event_id"`
}
```

Also replace the existing `Conversation` struct — the new schema removes `TaskID` and `LastActivity`, adds `IsTask`, `Title`, `Assignee`, `Requester`, and `Closed`/`ClosedReason`:

```go
// Conversation groups events between participants.
// Tasks are conversations with IsTask=true.
type Conversation struct {
    ConversationID string                  `json:"conversation_id"`
    Participants   []string                `json:"participants"`
    IsTask         bool                    `json:"is_task"`
    Title          string                  `json:"title,omitempty"`
    Assignee       string                  `json:"assignee,omitempty"`
    Requester      string                  `json:"requester,omitempty"`
    CreatedAt      time.Time               `json:"created_at"`
    Closed         bool                    `json:"closed"`
    ClosedReason   ConversationCloseReason `json:"closed_reason,omitempty"`
}

// TaskView is a read projection of a task conversation derived from events.
// It is never persisted — it is computed on demand by the sync engine.
type TaskView struct {
    ConversationID string     `json:"conversation_id"`
    Title          string     `json:"title"`
    Assignee       string     `json:"assignee"`
    Requester      string     `json:"requester"`
    Status         TaskStatus `json:"status"`
    Summary        string     `json:"summary,omitempty"`
    Reason         string     `json:"reason,omitempty"`
    CreatedAt      time.Time  `json:"created_at"`
    UpdatedAt      time.Time  `json:"updated_at"`
}
```

- [ ] **Step 2: Write pkg/protocol/events_test.go**

```go
package protocol_test

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/marcfargas/bifrost/pkg/protocol"
)

func TestEventDataRoundtrip(t *testing.T) {
    now := time.Now().UTC().Truncate(time.Second)
    ev := &protocol.Event{
        ID:             "ev001",
        ConversationID: "conv001",
        Type:           protocol.EventTypeMessage,
        FromAgent:      "agent-a",
        Data: protocol.EventData{
            Body:        "hello",
            MessageType: protocol.MessageTypeAnswer,
            Priority:    protocol.PriorityNormal,
        },
        Timestamp: now,
    }

    b, err := json.Marshal(ev)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    var got protocol.Event
    if err := json.Unmarshal(b, &got); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if got.ID != ev.ID {
        t.Errorf("ID: want %q got %q", ev.ID, got.ID)
    }
    if got.Data.Body != "hello" {
        t.Errorf("Body: want hello got %q", got.Data.Body)
    }
}

func TestFileEventSizeConstant(t *testing.T) {
    if protocol.MaxFileSizeBytes != 256*1024 {
        t.Errorf("MaxFileSizeBytes: want 262144 got %d", protocol.MaxFileSizeBytes)
    }
}
```

- [ ] **Step 3: Verify**

```
cd C:\dev\bifrost && go test ./pkg/protocol/... -run TestEvent -v
```

Commit: `feat(protocol): add Event, EventData, ConvMeta, ConversationSync types`

---

### Task 2: Store Interface — New Methods

**Files:**
- Modify: `pkg/store/store.go`

- [ ] **Step 1: Replace old interface sections with new ones in pkg/store/store.go**

Remove the `--- Messages ---`, `--- Tasks ---`, `--- Attachments ---`, `--- Offline message queue ---`, and `--- Federation message queue ---` sections entirely.

Replace them with:

```go
// --- Conversations (new schema) ---

// SaveConversation persists a new conversation.
SaveConversation(ctx context.Context, conv *protocol.Conversation) error

// GetConversation retrieves a conversation by ID. Returns nil, nil when not found.
GetConversation(ctx context.Context, conversationID string) (*protocol.Conversation, error)

// ListConversations returns conversations matching the filter.
ListConversations(ctx context.Context, filter ConversationFilter) ([]*protocol.Conversation, error)

// CloseConversation marks a conversation as closed.
CloseConversation(ctx context.Context, conversationID string, reason protocol.ConversationCloseReason) error

// --- Events ---

// AppendEvent persists a new event to a conversation's stream.
AppendEvent(ctx context.Context, ev *protocol.Event) error

// GetEvent retrieves an event by ID. Returns nil, nil when not found.
GetEvent(ctx context.Context, eventID string) (*protocol.Event, error)

// ListEventsSince returns events in a conversation after (exclusive) afterEventID,
// ordered by timestamp ascending. Pass "" to get all events from the start.
ListEventsSince(ctx context.Context, conversationID, afterEventID string) ([]*protocol.Event, error)

// LatestStatusEvent returns the most recent status event for a conversation,
// or nil if none exists.
LatestStatusEvent(ctx context.Context, conversationID string) (*protocol.Event, error)

// --- Delivery State ---

// GetDeliveryMark returns the last delivered event ID for a target in a conversation.
// Returns "", nil when no mark exists (meaning start from the beginning).
GetDeliveryMark(ctx context.Context, targetType, targetID, conversationID string) (string, error)

// SetDeliveryMark upserts the last delivered event ID for a target in a conversation.
SetDeliveryMark(ctx context.Context, targetType, targetID, conversationID, lastEventID string) error

// ListPendingDelivery returns all (conversationID, lastEventID) pairs where the
// target has undelivered events (i.e. delivery_state rows for this target).
ListPendingDelivery(ctx context.Context, targetType, targetID string) ([]DeliveryMark, error)

// InitDeliveryTargets ensures delivery_state rows exist for all participants
// of the conversation, with lastEventID="" for any that don't yet have a row.
InitDeliveryTargets(ctx context.Context, conv *protocol.Conversation) error
```

Also add the new filter and helper types:

```go
// DeliveryMark holds one row from delivery_state.
type DeliveryMark struct {
    ConversationID string
    LastEventID    string
}

// ConversationFilter contains optional filters for listing conversations.
type ConversationFilter struct {
    Participant string // agent ID that must appear in participants
    IsTask      *bool  // nil = no filter; true = tasks only; false = messages only
    Closed      *bool  // nil = no filter
    Assignee    string
    Requester   string
}
```

Keep the existing `--- Agents ---`, `--- Subscriptions ---`, `--- Peers ---` sections unchanged.

Remove `QueueEntry`, `MessageFilter`, `TaskFilter` types entirely (replaced by `ConversationFilter` and `DeliveryMark`).

Also remove `QueueStats` and `PeerQueueStats` from the interface.

- [ ] **Step 2: Verify interface compiles (implementation will be added next)**

```
cd C:\dev\bifrost && go build ./pkg/store/... 2>&1 | head -20
```

Note: SQLiteStore will not satisfy the interface yet — that is expected. This step just checks the interface itself parses.

Commit: `refactor(store): new store interface for events, delivery_state, conversation schema`

---

### Task 3: SQLite Implementation — Migration + New Tables

**Files:**
- Modify: `pkg/store/sqlite.go`
- Create: `pkg/store/sqlite_sync_test.go`

- [ ] **Step 1: Replace migrate() in pkg/store/sqlite.go**

Replace the entire `migrate()` function body with a two-phase migration:

```go
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
```

Note: The old `conversations` table used `conversation_id` as the primary key column name; the new schema uses `id`. The migration drops old tables and recreates. The `IF NOT EXISTS` guards are safe for fresh databases.

- [ ] **Step 2: Implement conversation methods**

Replace the old `SaveConversation`, `GetConversation`, `ListConversations`, `UpdateConversation`, `CloseConversation`, `TouchConversation`, `ListStaleConversations`, `DeleteConversationsBefore` implementations with these new ones (the old `UpdateConversation`/`TouchConversation`/`ListStaleConversations`/`DeleteConversationsBefore` are removed entirely):

```go
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

func (s *SQLiteStore) ListConversations(ctx context.Context, filter store.ConversationFilter) ([]*protocol.Conversation, error) {
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

func scanConversation(row interface {
    Scan(...any) error
}) (*protocol.Conversation, error) {
    var (
        id, partsJSON, title, assignee, requester, createdAtStr, closedReason string
        isTask, closed                                                          int
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
```

- [ ] **Step 3: Implement event methods**

```go
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

func scanEvent(row interface {
    Scan(...any) error
}) (*protocol.Event, error) {
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
```

- [ ] **Step 4: Implement delivery_state methods**

```go
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

func (s *SQLiteStore) ListPendingDelivery(ctx context.Context, targetType, targetID string) ([]store.DeliveryMark, error) {
    rows, err := s.db.QueryContext(ctx,
        `SELECT conversation_id, last_event_id FROM delivery_state
         WHERE target_type = ? AND target_id = ?`,
        targetType, targetID,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var marks []store.DeliveryMark
    for rows.Next() {
        var m store.DeliveryMark
        if err := rows.Scan(&m.ConversationID, &m.LastEventID); err != nil {
            return nil, err
        }
        marks = append(marks, m)
    }
    return marks, rows.Err()
}

func (s *SQLiteStore) InitDeliveryTargets(ctx context.Context, conv *protocol.Conversation) error {
    for _, agentID := range conv.Participants {
        _, err := s.db.ExecContext(ctx, `
INSERT INTO delivery_state (target_type, target_id, conversation_id, last_event_id)
VALUES ('agent', ?, ?, '')
ON CONFLICT DO NOTHING`,
            agentID, conv.ConversationID,
        )
        if err != nil {
            return err
        }
    }
    return nil
}
```

Also remove the old `SaveMessage`, `GetMessage`, `ListMessages`, `AckMessage`, `DeleteMessagesBefore`, `SaveTask`, `GetTask`, `UpdateTask`, `ListTasks`, `DeleteCompletedTasksBefore`, `SaveAttachment`, `GetAttachment`, `ListAttachments`, `DeleteAttachmentsBefore`, `EnqueueMessage`, `DequeueMessages`, `QueuedMessageCount`, `EnqueuePeerMessage`, `DequeuePeerMessages`, `QueueStats`, `PeerQueueStats` implementations from `sqlite.go`.

- [ ] **Step 5: Write pkg/store/sqlite_sync_test.go**

```go
package store_test

import (
    "context"
    "testing"
    "time"

    "github.com/marcfargas/bifrost/pkg/protocol"
    "github.com/marcfargas/bifrost/pkg/store"
)

func TestConversationNewSchema(t *testing.T) {
    ctx := context.Background()
    s := newTestStore(t)

    now := time.Now().UTC().Truncate(time.Second)
    conv := &protocol.Conversation{
        ConversationID: "conv-001",
        Participants:   []string{"agent-a", "agent-b"},
        IsTask:         false,
        CreatedAt:      now,
    }
    if err := s.SaveConversation(ctx, conv); err != nil {
        t.Fatalf("SaveConversation: %v", err)
    }
    got, err := s.GetConversation(ctx, "conv-001")
    if err != nil {
        t.Fatalf("GetConversation: %v", err)
    }
    if got == nil {
        t.Fatal("expected conversation, got nil")
    }
    if got.IsTask {
        t.Error("IsTask should be false")
    }
    if len(got.Participants) != 2 {
        t.Errorf("Participants: want 2 got %d", len(got.Participants))
    }
}

func TestTaskConversation(t *testing.T) {
    ctx := context.Background()
    s := newTestStore(t)

    now := time.Now().UTC().Truncate(time.Second)
    conv := &protocol.Conversation{
        ConversationID: "task-conv-001",
        Participants:   []string{"requester-a", "assignee-b"},
        IsTask:         true,
        Title:          "Fix the thing",
        Assignee:       "assignee-b",
        Requester:      "requester-a",
        CreatedAt:      now,
    }
    if err := s.SaveConversation(ctx, conv); err != nil {
        t.Fatalf("SaveConversation: %v", err)
    }

    isTask := true
    convs, err := s.ListConversations(ctx, store.ConversationFilter{IsTask: &isTask})
    if err != nil {
        t.Fatalf("ListConversations: %v", err)
    }
    if len(convs) != 1 {
        t.Fatalf("want 1 task conversation, got %d", len(convs))
    }
    if convs[0].Title != "Fix the thing" {
        t.Errorf("Title: want %q got %q", "Fix the thing", convs[0].Title)
    }
}

func TestAppendAndListEvents(t *testing.T) {
    ctx := context.Background()
    s := newTestStore(t)

    now := time.Now().UTC().Truncate(time.Millisecond)
    conv := &protocol.Conversation{
        ConversationID: "ev-conv-001",
        Participants:   []string{"agent-a", "agent-b"},
        CreatedAt:      now,
    }
    if err := s.SaveConversation(ctx, conv); err != nil {
        t.Fatalf("SaveConversation: %v", err)
    }

    ev1 := &protocol.Event{
        ID: "ev-001", ConversationID: "ev-conv-001",
        Type: protocol.EventTypeMessage, FromAgent: "agent-a",
        Data:      protocol.EventData{Body: "hello", Priority: protocol.PriorityNormal},
        Timestamp: now,
    }
    ev2 := &protocol.Event{
        ID: "ev-002", ConversationID: "ev-conv-001",
        Type: protocol.EventTypeMessage, FromAgent: "agent-b",
        Data:      protocol.EventData{Body: "world", Priority: protocol.PriorityNormal},
        Timestamp: now.Add(time.Second),
    }

    if err := s.AppendEvent(ctx, ev1); err != nil {
        t.Fatalf("AppendEvent ev1: %v", err)
    }
    if err := s.AppendEvent(ctx, ev2); err != nil {
        t.Fatalf("AppendEvent ev2: %v", err)
    }

    all, err := s.ListEventsSince(ctx, "ev-conv-001", "")
    if err != nil {
        t.Fatalf("ListEventsSince all: %v", err)
    }
    if len(all) != 2 {
        t.Fatalf("want 2 events, got %d", len(all))
    }

    since, err := s.ListEventsSince(ctx, "ev-conv-001", "ev-001")
    if err != nil {
        t.Fatalf("ListEventsSince since ev-001: %v", err)
    }
    if len(since) != 1 || since[0].ID != "ev-002" {
        t.Errorf("want [ev-002], got %v", since)
    }
}

func TestLatestStatusEvent(t *testing.T) {
    ctx := context.Background()
    s := newTestStore(t)

    now := time.Now().UTC().Truncate(time.Millisecond)
    conv := &protocol.Conversation{
        ConversationID: "status-conv", Participants: []string{"a", "b"}, IsTask: true, CreatedAt: now,
    }
    _ = s.SaveConversation(ctx, conv)

    _ = s.AppendEvent(ctx, &protocol.Event{
        ID: "s1", ConversationID: "status-conv", Type: protocol.EventTypeStatus,
        FromAgent: "b", Data: protocol.EventData{NewStatus: protocol.TaskStatusAccepted},
        Timestamp: now,
    })
    _ = s.AppendEvent(ctx, &protocol.Event{
        ID: "s2", ConversationID: "status-conv", Type: protocol.EventTypeStatus,
        FromAgent: "b", Data: protocol.EventData{NewStatus: protocol.TaskStatusInProgress},
        Timestamp: now.Add(time.Second),
    })

    ev, err := s.LatestStatusEvent(ctx, "status-conv")
    if err != nil {
        t.Fatalf("LatestStatusEvent: %v", err)
    }
    if ev == nil {
        t.Fatal("expected status event, got nil")
    }
    if ev.Data.NewStatus != protocol.TaskStatusInProgress {
        t.Errorf("NewStatus: want in_progress got %q", ev.Data.NewStatus)
    }
}

func TestDeliveryState(t *testing.T) {
    ctx := context.Background()
    s := newTestStore(t)

    now := time.Now().UTC().Truncate(time.Second)
    conv := &protocol.Conversation{
        ConversationID: "ds-conv", Participants: []string{"agent-a", "agent-b"}, CreatedAt: now,
    }
    _ = s.SaveConversation(ctx, conv)

    if err := s.InitDeliveryTargets(ctx, conv); err != nil {
        t.Fatalf("InitDeliveryTargets: %v", err)
    }

    mark, err := s.GetDeliveryMark(ctx, "agent", "agent-a", "ds-conv")
    if err != nil {
        t.Fatalf("GetDeliveryMark: %v", err)
    }
    if mark != "" {
        t.Errorf("initial mark should be empty, got %q", mark)
    }

    if err := s.SetDeliveryMark(ctx, "agent", "agent-a", "ds-conv", "ev-007"); err != nil {
        t.Fatalf("SetDeliveryMark: %v", err)
    }
    mark, err = s.GetDeliveryMark(ctx, "agent", "agent-a", "ds-conv")
    if err != nil {
        t.Fatalf("GetDeliveryMark after set: %v", err)
    }
    if mark != "ev-007" {
        t.Errorf("mark: want ev-007 got %q", mark)
    }

    pending, err := s.ListPendingDelivery(ctx, "agent", "agent-a")
    if err != nil {
        t.Fatalf("ListPendingDelivery: %v", err)
    }
    if len(pending) != 1 {
        t.Fatalf("want 1 pending, got %d", len(pending))
    }
    if pending[0].ConversationID != "ds-conv" {
        t.Errorf("ConversationID: want ds-conv got %q", pending[0].ConversationID)
    }
}
```

- [ ] **Step 6: Verify**

```
cd C:\dev\bifrost && go test ./pkg/store/... -v -run "TestConversation|TestAppend|TestLatestStatus|TestDelivery|TestTask"
```

Commit: `feat(store): SQLite implementation for conversations, events, delivery_state`

---

## Phase 2: Sync Engine

### Task 4: pkg/core/sync.go

**Files:**
- Create: `pkg/core/sync.go`
- Create: `pkg/core/sync_test.go`

- [ ] **Step 1: Write pkg/core/sync.go**

```go
// Package core — sync.go
// SyncEngine is the single delivery mechanism for all events.
// It delivers to local agents via NotifyAgent and to peer hubs via
// ConversationSyncer. DND is checked on this (receiving) hub.
package core

import (
    "context"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/google/uuid"

    "github.com/marcfargas/bifrost/pkg/protocol"
    "github.com/marcfargas/bifrost/pkg/store"
)

// ConversationSyncer is implemented by the federation manager.
// It is called by the sync engine when events must be pushed to a peer hub.
type ConversationSyncer interface {
    SyncConversation(ctx context.Context, peerID string, conv *protocol.Conversation, events []*protocol.Event) error
}

// SyncEngine delivers events from the event store to all targets registered
// in delivery_state. It is the only delivery path for both local and federated
// delivery.
type SyncEngine struct {
    store  store.Store
    hub    *Hub
    syncer ConversationSyncer // nil if federation disabled
    logger *slog.Logger

    mu     sync.Mutex
    nudges map[string]struct{} // conversation IDs pending a sync pass
    wakeup chan struct{}
}

// NewSyncEngine creates a sync engine. Call Start to begin processing.
func NewSyncEngine(s store.Store, h *Hub, logger *slog.Logger) *SyncEngine {
    if logger == nil {
        logger = slog.Default()
    }
    return &SyncEngine{
        store:  s,
        hub:    h,
        logger: logger,
        nudges: make(map[string]struct{}),
        wakeup: make(chan struct{}, 1),
    }
}

// SetSyncer sets the federation syncer. Safe to call before Start.
func (e *SyncEngine) SetSyncer(s ConversationSyncer) {
    e.mu.Lock()
    e.syncer = s
    e.mu.Unlock()
}

// Start runs the sync engine until ctx is cancelled.
func (e *SyncEngine) Start(ctx context.Context) {
    go e.loop(ctx)
}

// Nudge schedules a sync pass for the given conversation.
// Non-blocking: if a pass is already scheduled, the call is a no-op.
func (e *SyncEngine) Nudge(conversationID string) {
    e.mu.Lock()
    e.nudges[conversationID] = struct{}{}
    e.mu.Unlock()
    select {
    case e.wakeup <- struct{}{}:
    default:
    }
}

func (e *SyncEngine) loop(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case <-e.wakeup:
            e.mu.Lock()
            pending := make([]string, 0, len(e.nudges))
            for id := range e.nudges {
                pending = append(pending, id)
            }
            e.nudges = make(map[string]struct{})
            e.mu.Unlock()

            for _, convID := range pending {
                if err := e.syncConversation(ctx, convID); err != nil {
                    e.logger.Error("sync conversation failed", "conversation_id", convID, "error", err)
                }
            }
        }
    }
}

// syncConversation delivers any new events in conversationID to all registered
// targets in delivery_state.
func (e *SyncEngine) syncConversation(ctx context.Context, convID string) error {
    conv, err := e.store.GetConversation(ctx, convID)
    if err != nil {
        return fmt.Errorf("sync: get conversation: %w", err)
    }
    if conv == nil {
        return nil // conversation may have been deleted
    }

    // Collect all agent targets.
    agentMarks, err := e.store.ListPendingDelivery(ctx, "agent", "")
    if err != nil {
        return fmt.Errorf("sync: list agent delivery: %w", err)
    }
    // Filter to this conversation only.
    for _, mark := range agentMarks {
        if mark.ConversationID != convID {
            continue
        }
        if err := e.deliverToAgent(ctx, conv, mark); err != nil {
            e.logger.Warn("deliver to agent failed",
                "conversation_id", convID, "error", err)
        }
    }

    // Collect all peer targets.
    peerMarks, err := e.store.ListPendingDelivery(ctx, "peer", "")
    if err != nil {
        return fmt.Errorf("sync: list peer delivery: %w", err)
    }
    for _, mark := range peerMarks {
        if mark.ConversationID != convID {
            continue
        }
        if err := e.deliverToPeer(ctx, conv, mark); err != nil {
            e.logger.Warn("deliver to peer failed",
                "conversation_id", convID, "error", err)
        }
    }

    return nil
}

// deliverToAgent pushes new events in a conversation to a single local agent.
func (e *SyncEngine) deliverToAgent(ctx context.Context, conv *protocol.Conversation, mark store.DeliveryMark) error {
    // We need to know the target agent ID from the mark. ListPendingDelivery
    // must be redesigned to return target_id. See note below.
    // The actual implementation uses the extended DeliveryMark (see store change).
    agentID := mark.TargetID

    agent, err := e.store.GetAgent(ctx, agentID)
    if err != nil || agent == nil {
        return nil // agent gone; leave mark in place for reconnect
    }

    events, err := e.store.ListEventsSince(ctx, conv.ConversationID, mark.LastEventID)
    if err != nil {
        return fmt.Errorf("list events: %w", err)
    }
    if len(events) == 0 {
        return nil
    }

    for _, ev := range events {
        // DND check: receiving hub decides.
        if agent.Status == protocol.AgentStatusDND {
            if ev.Data.Priority != protocol.PriorityUrgent {
                continue // skip; mark not advanced
            }
        }

        status := e.hub.NotifyAgent(agentID, Notification{
            Type:    "event.new",
            Payload: ev,
        })

        if status == protocol.DeliveryStatusDelivered {
            if err := e.store.SetDeliveryMark(ctx, "agent", agentID, conv.ConversationID, ev.ID); err != nil {
                e.logger.Warn("set delivery mark failed", "agent_id", agentID, "event_id", ev.ID)
            }
        }
        // If offline: leave mark where it is; event stays in stream for reconnect.
    }
    return nil
}

// deliverToPeer pushes new events in a conversation to a peer hub via
// ConversationSyncer.
func (e *SyncEngine) deliverToPeer(ctx context.Context, conv *protocol.Conversation, mark store.DeliveryMark) error {
    if e.syncer == nil {
        return nil
    }
    peerID := mark.TargetID

    events, err := e.store.ListEventsSince(ctx, conv.ConversationID, mark.LastEventID)
    if err != nil {
        return fmt.Errorf("list events for peer: %w", err)
    }
    if len(events) == 0 {
        return nil
    }

    if err := e.syncer.SyncConversation(ctx, peerID, conv, events); err != nil {
        // Peer unreachable — leave mark; retry on reconnect.
        e.logger.Info("peer sync failed, will retry on reconnect",
            "peer_id", peerID, "conversation_id", conv.ConversationID, "error", err)
        return nil
    }

    // Advance mark to last successfully sent event.
    last := events[len(events)-1]
    return e.store.SetDeliveryMark(ctx, "peer", peerID, conv.ConversationID, last.ID)
}

// AppendEvent is the single write path: persist an event and nudge the engine.
func (e *SyncEngine) AppendEvent(ctx context.Context, ev *protocol.Event) error {
    if ev.ID == "" {
        ev.ID = uuid.New().String()
    }
    if ev.Timestamp.IsZero() {
        ev.Timestamp = time.Now()
    }
    if err := e.store.AppendEvent(ctx, ev); err != nil {
        return fmt.Errorf("sync: append event: %w", err)
    }
    e.Nudge(ev.ConversationID)
    return nil
}

// BatchSyncForPeer sends all pending conversations to a peer that just
// reconnected. Called by the federation manager on peer connect.
func (e *SyncEngine) BatchSyncForPeer(ctx context.Context, peerID string) error {
    marks, err := e.store.ListPendingDelivery(ctx, "peer", peerID)
    if err != nil {
        return fmt.Errorf("batch sync: list pending: %w", err)
    }
    for _, mark := range marks {
        conv, err := e.store.GetConversation(ctx, mark.ConversationID)
        if err != nil || conv == nil {
            continue
        }
        if err := e.deliverToPeer(ctx, conv, mark); err != nil {
            e.logger.Warn("batch sync delivery failed",
                "peer_id", peerID, "conversation_id", mark.ConversationID, "error", err)
        }
    }
    return nil
}

// FlushForAgent delivers all pending events to an agent that just came online
// or disabled DND.
func (e *SyncEngine) FlushForAgent(ctx context.Context, agentID string) error {
    marks, err := e.store.ListPendingDelivery(ctx, "agent", agentID)
    if err != nil {
        return fmt.Errorf("flush: list pending: %w", err)
    }
    for _, mark := range marks {
        conv, err := e.store.GetConversation(ctx, mark.ConversationID)
        if err != nil || conv == nil {
            continue
        }
        if err := e.deliverToAgent(ctx, conv, mark); err != nil {
            e.logger.Warn("flush delivery failed",
                "agent_id", agentID, "conversation_id", mark.ConversationID, "error", err)
        }
    }
    return nil
}
```

Note: `DeliveryMark` in the store must include `TargetID`. Update `store.DeliveryMark` to:

```go
type DeliveryMark struct {
    TargetID       string
    ConversationID string
    LastEventID    string
}
```

And update `ListPendingDelivery` signature: when `targetID == ""`, it returns all marks for `targetType`. When non-empty, scoped to that specific target. Update the SQLite implementation accordingly:

```go
func (s *SQLiteStore) ListPendingDelivery(ctx context.Context, targetType, targetID string) ([]store.DeliveryMark, error) {
    var (
        rows *sql.Rows
        err  error
    )
    if targetID == "" {
        rows, err = s.db.QueryContext(ctx,
            `SELECT target_id, conversation_id, last_event_id FROM delivery_state WHERE target_type = ?`,
            targetType)
    } else {
        rows, err = s.db.QueryContext(ctx,
            `SELECT target_id, conversation_id, last_event_id FROM delivery_state WHERE target_type = ? AND target_id = ?`,
            targetType, targetID)
    }
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var marks []store.DeliveryMark
    for rows.Next() {
        var m store.DeliveryMark
        if err := rows.Scan(&m.TargetID, &m.ConversationID, &m.LastEventID); err != nil {
            return nil, err
        }
        marks = append(marks, m)
    }
    return marks, rows.Err()
}
```

- [ ] **Step 2: Write pkg/core/sync_test.go**

```go
package core_test

import (
    "context"
    "path/filepath"
    "sync"
    "testing"
    "time"

    "github.com/marcfargas/bifrost/pkg/core"
    "github.com/marcfargas/bifrost/pkg/protocol"
    "github.com/marcfargas/bifrost/pkg/store"
)

// fakeNotifier records notifications delivered to agents.
type fakeNotifier struct {
    mu    sync.Mutex
    calls []core.Notification
}

func (f *fakeNotifier) Notify(agentID string, n core.Notification) bool {
    f.mu.Lock()
    f.calls = append(f.calls, n)
    f.mu.Unlock()
    return true
}

func newTestHub(t *testing.T) (*core.Hub, *store.SQLiteStore) {
    t.Helper()
    dbPath := filepath.Join(t.TempDir(), "test.db")
    s, err := store.NewSQLite(dbPath)
    if err != nil {
        t.Fatalf("NewSQLite: %v", err)
    }
    t.Cleanup(func() { _ = s.Close() })
    h := core.NewHub(s)
    return h, s
}

func TestSyncEngineDeliverToAgent(t *testing.T) {
    ctx := context.Background()
    h, s := newTestHub(t)
    notifier := &fakeNotifier{}
    h.AddNotifier(notifier)

    eng := core.NewSyncEngine(s, h, nil)
    eng.Start(ctx)

    now := time.Now().UTC().Truncate(time.Millisecond)

    // Register agent
    agent := &protocol.Agent{
        AgentID: "agent-recv", Status: protocol.AgentStatusOnline,
        ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0",
    }
    if err := s.UpsertAgent(ctx, agent); err != nil {
        t.Fatalf("UpsertAgent: %v", err)
    }

    // Create conversation with delivery_state
    conv := &protocol.Conversation{
        ConversationID: "sync-test-conv",
        Participants:   []string{"agent-send", "agent-recv"},
        CreatedAt:      now,
    }
    if err := s.SaveConversation(ctx, conv); err != nil {
        t.Fatalf("SaveConversation: %v", err)
    }
    if err := s.InitDeliveryTargets(ctx, conv); err != nil {
        t.Fatalf("InitDeliveryTargets: %v", err)
    }

    // Append an event
    ev := &protocol.Event{
        ID:             "ev-sync-001",
        ConversationID: "sync-test-conv",
        Type:           protocol.EventTypeMessage,
        FromAgent:      "agent-send",
        Data:           protocol.EventData{Body: "ping", Priority: protocol.PriorityNormal},
        Timestamp:      now,
    }
    if err := eng.AppendEvent(ctx, ev); err != nil {
        t.Fatalf("AppendEvent: %v", err)
    }

    // Give the sync engine a moment to deliver.
    time.Sleep(50 * time.Millisecond)

    notifier.mu.Lock()
    count := len(notifier.calls)
    notifier.mu.Unlock()

    if count == 0 {
        t.Error("expected at least one notification to agent-recv, got none")
    }
}
```

- [ ] **Step 3: Verify**

```
cd C:\dev\bifrost && go test ./pkg/core/... -run TestSyncEngine -v
```

Commit: `feat(core): SyncEngine — append event, nudge, deliver to agents and peers`

---

## Phase 3: Core Rewrite

### Task 5: Replace MessageRouter and TaskManager

**Files:**
- Modify: `pkg/core/messages.go` → rewrite as thin wrapper over SyncEngine
- Modify: `pkg/core/tasks.go` → rewrite as conversation-based, no task table
- Modify: `pkg/core/conversations.go` → simplify (no more TouchConversation/LastActivity)
- Modify: `pkg/core/hub.go` → wire SyncEngine, remove old managers
- Create: `pkg/core/tasks_test.go` (replace old)

- [ ] **Step 1: Rewrite pkg/core/messages.go**

```go
package core

import (
    "context"
    "fmt"
    "slices"
    "strings"

    "github.com/marcfargas/bifrost/pkg/protocol"
    "github.com/marcfargas/bifrost/pkg/store"
)

// MessageRouter is now a thin facade over the SyncEngine.
// It resolves or creates the conversation, then appends a message event.
type MessageRouter struct {
    store store.Store
    hub   *Hub
}

func newMessageRouter(s store.Store, h *Hub) *MessageRouter {
    return &MessageRouter{store: s, hub: h}
}

// Send resolves or creates a conversation, appends a message event, and
// triggers the sync engine. Channel and task routing is handled by
// subscription lookups as before.
func (r *MessageRouter) Send(ctx context.Context, msg *protocol.Message) error {
    if msg.ID == "" {
        msg.ID = protocol.NewID()
    }

    switch {
    case strings.HasPrefix(msg.To, "channel:"):
        return r.routeToChannel(ctx, msg)
    case msg.To == "*":
        return r.routeBroadcast(ctx, msg)
    default:
        return r.routeToAgent(ctx, msg)
    }
}

func (r *MessageRouter) routeToChannel(ctx context.Context, msg *protocol.Message) error {
    subscribers, err := r.store.GetSubscribers(ctx, msg.To)
    if err != nil {
        return fmt.Errorf("messages: channel subscribers: %w", err)
    }

    conv, err := r.getOrCreateConversation(ctx, msg.From, msg.To, nil)
    if err != nil {
        return err
    }

    ev := messageToEvent(msg, conv.ConversationID)
    if err := r.hub.Sync().AppendEvent(ctx, ev); err != nil {
        return err
    }

    // Ensure all channel subscribers have a delivery_state row.
    for _, agentID := range subscribers {
        _ = r.store.SetDeliveryMark(ctx, "agent", agentID, conv.ConversationID, "")
    }
    return nil
}

func (r *MessageRouter) routeBroadcast(ctx context.Context, msg *protocol.Message) error {
    agents, err := r.store.ListAgents(ctx, store.AgentFilter{})
    if err != nil {
        return fmt.Errorf("messages: list agents for broadcast: %w", err)
    }
    // Broadcast creates individual conversations or reuses open ones.
    for _, a := range agents {
        if a.AgentID == msg.From || a.PeerHub != "" {
            continue
        }
        toMsg := *msg
        toMsg.To = "agent:" + a.AgentID
        if err := r.routeToAgent(ctx, &toMsg); err != nil {
            r.hub.logger().Warn("broadcast delivery failed", "to", a.AgentID, "error", err)
        }
    }
    return nil
}

func (r *MessageRouter) routeToAgent(ctx context.Context, msg *protocol.Message) error {
    agent, err := r.hub.Agents().Resolve(ctx, msg.To)
    if err != nil {
        return err
    }

    conv, err := r.getOrCreateConversation(ctx, msg.From, agent.AgentID, nil)
    if err != nil {
        return err
    }

    ev := messageToEvent(msg, conv.ConversationID)

    // Ensure both parties have delivery_state rows.
    if initErr := r.store.InitDeliveryTargets(ctx, conv); initErr != nil {
        return fmt.Errorf("messages: init delivery targets: %w", initErr)
    }

    // If agent is on a peer hub, ensure peer has a delivery_state row.
    if agent.PeerHub != "" {
        _ = r.store.SetDeliveryMark(ctx, "peer", agent.PeerHub, conv.ConversationID, "")
    }

    return r.hub.Sync().AppendEvent(ctx, ev)
}

// getOrCreateConversation finds or creates an open conversation between two parties.
func (r *MessageRouter) getOrCreateConversation(ctx context.Context, from, to string, taskMeta *conversationTaskMeta) (*protocol.Conversation, error) {
    open := false
    convs, err := r.store.ListConversations(ctx, store.ConversationFilter{
        Participant: from,
        Closed:      &open,
    })
    if err != nil {
        return nil, err
    }
    for _, c := range convs {
        if slices.Contains(c.Participants, to) && !c.IsTask {
            return c, nil
        }
    }

    conv := &protocol.Conversation{
        ConversationID: protocol.NewShortID(),
        Participants:   []string{from, to},
        CreatedAt:      timeNow(),
    }
    if taskMeta != nil {
        conv.IsTask = true
        conv.Title = taskMeta.title
        conv.Assignee = taskMeta.assignee
        conv.Requester = taskMeta.requester
    }
    if err := r.store.SaveConversation(ctx, conv); err != nil {
        return nil, err
    }
    return conv, nil
}

type conversationTaskMeta struct {
    title, assignee, requester string
}

// messageToEvent converts a protocol.Message to a message event.
func messageToEvent(msg *protocol.Message, convID string) *protocol.Event {
    return &protocol.Event{
        ID:             msg.ID,
        ConversationID: convID,
        Type:           protocol.EventTypeMessage,
        FromAgent:      msg.From,
        Data: protocol.EventData{
            Body:        msg.Body,
            MessageType: msg.Type,
            Priority:    msg.Priority,
            InReplyTo:   msg.InReplyTo,
        },
    }
}
```

- [ ] **Step 2: Rewrite pkg/core/tasks.go**

```go
package core

import (
    "context"
    "fmt"
    "slices"

    "github.com/marcfargas/bifrost/pkg/protocol"
    "github.com/marcfargas/bifrost/pkg/store"
)

// ValidTransitions defines the allowed task status transitions.
var ValidTransitions = map[protocol.TaskStatus][]protocol.TaskStatus{
    protocol.TaskStatusRequested:  {protocol.TaskStatusAccepted, protocol.TaskStatusRejected},
    protocol.TaskStatusAccepted:   {protocol.TaskStatusInProgress},
    protocol.TaskStatusInProgress: {protocol.TaskStatusCompleted, protocol.TaskStatusFailed},
}

// TaskUpdate carries the fields that may be changed in a single UpdateTask call.
type TaskUpdate struct {
    Status  protocol.TaskStatus
    Summary string
    Reason  string
}

// TaskManager manages task conversations. Task state is derived from the
// latest status event in the conversation — no separate task table.
type TaskManager struct {
    store store.Store
    hub   *Hub
}

func newTaskManager(s store.Store, h *Hub) *TaskManager {
    return &TaskManager{store: s, hub: h}
}

func isValidTransition(from, to protocol.TaskStatus) bool {
    allowed, ok := ValidTransitions[from]
    if !ok {
        return false
    }
    return slices.Contains(allowed, to)
}

// RequestTask creates a task conversation and appends the initial message event.
// The conversation has is_task=true; no row is written to any task table.
func (m *TaskManager) RequestTask(ctx context.Context, requesterID, assigneeAddr, title, description string) (*protocol.TaskView, error) {
    assignee, err := m.hub.Agents().Resolve(ctx, assigneeAddr)
    if err != nil {
        return nil, fmt.Errorf("tasks: resolve assignee: %w", err)
    }

    now := timeNow()
    conv := &protocol.Conversation{
        ConversationID: protocol.NewShortID(),
        Participants:   []string{requesterID, assignee.AgentID},
        IsTask:         true,
        Title:          title,
        Assignee:       assignee.AgentID,
        Requester:      requesterID,
        CreatedAt:      now,
    }
    if err := m.store.SaveConversation(ctx, conv); err != nil {
        return nil, fmt.Errorf("tasks: save conversation: %w", err)
    }

    if err := m.store.InitDeliveryTargets(ctx, conv); err != nil {
        return nil, fmt.Errorf("tasks: init delivery targets: %w", err)
    }

    // If assignee is remote, add peer delivery target.
    if assignee.PeerHub != "" {
        _ = m.store.SetDeliveryMark(ctx, "peer", assignee.PeerHub, conv.ConversationID, "")
    }

    // Subscribe both parties to the conversation for channel notifications.
    _ = m.store.Subscribe(ctx, requesterID, "conv:"+conv.ConversationID)
    _ = m.store.Subscribe(ctx, assignee.AgentID, "conv:"+conv.ConversationID)

    // Append the initial message event.
    ev := &protocol.Event{
        ID:             protocol.NewID(),
        ConversationID: conv.ConversationID,
        Type:           protocol.EventTypeMessage,
        FromAgent:      requesterID,
        Data: protocol.EventData{
            Body:        "Task requested: " + title + "\n\n" + description,
            MessageType: protocol.MessageTypeContext,
            Priority:    protocol.PriorityNormal,
        },
    }
    if err := m.hub.Sync().AppendEvent(ctx, ev); err != nil {
        return nil, fmt.Errorf("tasks: append request event: %w", err)
    }

    return m.buildView(ctx, conv, nil)
}

// UpdateTask validates the caller, validates the status transition, and
// appends a status event. No task table write.
func (m *TaskManager) UpdateTask(ctx context.Context, callerAgentID, conversationID string, update TaskUpdate) (*protocol.TaskView, error) {
    conv, err := m.store.GetConversation(ctx, conversationID)
    if err != nil {
        return nil, fmt.Errorf("tasks: get conversation: %w", err)
    }
    if conv == nil || !conv.IsTask {
        return nil, fmt.Errorf("tasks: task conversation not found: %s", conversationID)
    }

    if callerAgentID != conv.Assignee && callerAgentID != conv.Requester {
        return nil, fmt.Errorf("tasks: unauthorized: caller %q is not assignee or requester", callerAgentID)
    }

    current, err := m.currentStatus(ctx, conversationID)
    if err != nil {
        return nil, err
    }

    if update.Status != "" && update.Status != current {
        if !isValidTransition(current, update.Status) {
            return nil, fmt.Errorf("tasks: invalid transition %s → %s", current, update.Status)
        }
    }

    ev := &protocol.Event{
        ID:             protocol.NewID(),
        ConversationID: conversationID,
        Type:           protocol.EventTypeStatus,
        FromAgent:      callerAgentID,
        Data: protocol.EventData{
            NewStatus: update.Status,
            Summary:   update.Summary,
            Reason:    update.Reason,
        },
    }
    if err := m.hub.Sync().AppendEvent(ctx, ev); err != nil {
        return nil, fmt.Errorf("tasks: append status event: %w", err)
    }

    return m.buildView(ctx, conv, ev)
}

// GetTask derives a TaskView by reading the conversation and its latest status event.
func (m *TaskManager) GetTask(ctx context.Context, conversationID string) (*protocol.TaskView, error) {
    conv, err := m.store.GetConversation(ctx, conversationID)
    if err != nil {
        return nil, fmt.Errorf("tasks: get conversation: %w", err)
    }
    if conv == nil || !conv.IsTask {
        return nil, nil
    }

    statusEv, err := m.store.LatestStatusEvent(ctx, conversationID)
    if err != nil {
        return nil, fmt.Errorf("tasks: latest status event: %w", err)
    }
    return m.buildView(ctx, conv, statusEv)
}

// ListTasks returns TaskView projections for all task conversations matching filter.
func (m *TaskManager) ListTasks(ctx context.Context, filter store.ConversationFilter) ([]*protocol.TaskView, error) {
    isTask := true
    filter.IsTask = &isTask
    convs, err := m.store.ListConversations(ctx, filter)
    if err != nil {
        return nil, fmt.Errorf("tasks: list: %w", err)
    }
    views := make([]*protocol.TaskView, 0, len(convs))
    for _, c := range convs {
        statusEv, _ := m.store.LatestStatusEvent(ctx, c.ConversationID)
        v, err := m.buildView(ctx, c, statusEv)
        if err != nil {
            continue
        }
        views = append(views, v)
    }
    return views, nil
}

// currentStatus derives the current task status from the latest status event.
// Returns TaskStatusRequested when no status event exists.
func (m *TaskManager) currentStatus(ctx context.Context, conversationID string) (protocol.TaskStatus, error) {
    ev, err := m.store.LatestStatusEvent(ctx, conversationID)
    if err != nil {
        return "", err
    }
    if ev == nil {
        return protocol.TaskStatusRequested, nil
    }
    return ev.Data.NewStatus, nil
}

// buildView constructs a TaskView from a conversation and its latest status event.
func (m *TaskManager) buildView(_ context.Context, conv *protocol.Conversation, statusEv *protocol.Event) (*protocol.TaskView, error) {
    v := &protocol.TaskView{
        ConversationID: conv.ConversationID,
        Title:          conv.Title,
        Assignee:       conv.Assignee,
        Requester:      conv.Requester,
        Status:         protocol.TaskStatusRequested,
        CreatedAt:      conv.CreatedAt,
        UpdatedAt:      conv.CreatedAt,
    }
    if statusEv != nil {
        v.Status = statusEv.Data.NewStatus
        v.Summary = statusEv.Data.Summary
        v.Reason = statusEv.Data.Reason
        v.UpdatedAt = statusEv.Timestamp
    }
    return v, nil
}
```

- [ ] **Step 3: Add timeNow helper and update pkg/core/hub.go**

Add `timeNow` as a package-level var for testability in `pkg/core/hub.go`:

```go
// timeNow is a testable wrapper around time.Now.
var timeNow = time.Now
```

Update `Hub` struct — add `sync` field, remove `messages` field name conflict, add `logger` accessor:

```go
type Hub struct {
    store         store.Store
    mu            sync.RWMutex
    notifiers     []Notifier
    federation    FederationForwarder
    agents        *AgentRegistry
    messages      *MessageRouter
    tasks         *TaskManager
    channels      *ChannelManager
    dnd           *DNDManager
    conversations *ConversationManager
    syncEngine    *SyncEngine
    log           *slog.Logger
}

func NewHub(s store.Store) *Hub {
    h := &Hub{
        store: s,
        log:   slog.Default(),
    }
    h.agents = newAgentRegistry(s, h)
    h.messages = newMessageRouter(s, h)
    h.tasks = newTaskManager(s, h)
    h.channels = newChannelManager(s, h)
    h.dnd = newDNDManager(s, h, true)
    h.conversations = newConversationManager(s, h)
    h.syncEngine = NewSyncEngine(s, h, h.log)
    return h
}
```

Remove `NewHubWithConfig` (attachments are no longer a separate disk-based system — files are inline events). The CLI server can construct the hub and call `hub.Sync().Start(ctx)`.

Add accessor:

```go
// Sync returns the sync engine.
func (h *Hub) Sync() *SyncEngine { return h.syncEngine }

// logger returns the hub's logger for internal use.
func (h *Hub) logger() *slog.Logger { return h.log }
```

Update `FederationForwarder` interface — remove `ForwardMessage`, `ForwardTaskCreate`, `ForwardTaskUpdate`, add `SyncConversation`:

```go
type FederationForwarder interface {
    // SyncConversation pushes events to a peer hub.
    SyncConversation(ctx context.Context, peerID string, conv *protocol.Conversation, events []*protocol.Event) error

    // BroadcastAgentStatus notifies all peers about an agent status change.
    BroadcastAgentStatus(ctx context.Context, agentID string, status protocol.AgentStatus)
}
```

Remove `AttachmentManager` from Hub entirely.

- [ ] **Step 4: Simplify pkg/core/conversations.go**

Remove `LinkToTask`, `Touch` (no more `TouchConversation`/`LastActivity`). Keep `CloseStale` but update to use `created_at` as the inactivity proxy (closed conversations are pruned by age). Remove `inactivityTimeout` — conversations are long-lived; the manager only handles close-on-request:

```go
package core

import (
    "context"
    "fmt"

    "github.com/marcfargas/bifrost/pkg/protocol"
    "github.com/marcfargas/bifrost/pkg/store"
)

// ConversationManager provides conversation retrieval and list operations.
type ConversationManager struct {
    store store.Store
    hub   *Hub
}

func newConversationManager(s store.Store, h *Hub) *ConversationManager {
    return &ConversationManager{store: s, hub: h}
}

// List returns conversations matching the given filter.
func (m *ConversationManager) List(ctx context.Context, filter store.ConversationFilter) ([]*protocol.Conversation, error) {
    convs, err := m.store.ListConversations(ctx, filter)
    if err != nil {
        return nil, fmt.Errorf("conversations: list: %w", err)
    }
    return convs, nil
}

// Get retrieves a single conversation by ID. Returns nil, nil when not found.
func (m *ConversationManager) Get(ctx context.Context, convID string) (*protocol.Conversation, error) {
    conv, err := m.store.GetConversation(ctx, convID)
    if err != nil {
        return nil, fmt.Errorf("conversations: get %s: %w", convID, err)
    }
    return conv, nil
}

// Close marks a conversation as closed.
func (m *ConversationManager) Close(ctx context.Context, convID string, reason protocol.ConversationCloseReason) error {
    return m.store.CloseConversation(ctx, convID, reason)
}
```

- [ ] **Step 5: Verify**

```
cd C:\dev\bifrost && go build ./pkg/...
```

Commit: `refactor(core): rewrite MessageRouter and TaskManager over SyncEngine; no task table`

---

## Phase 4: Hub RPC and Shim Tools

### Task 6: Update internal/hub/handler.go

**Files:**
- Modify: `internal/hub/handler.go`

- [ ] **Step 1: Update method dispatch table**

In `Handle()`, replace:

```go
case "task.create":
    return h.handleCreateTask(ctx, req)
```

with:

```go
case "task.request":
    return h.handleRequestTask(ctx, req)
```

Remove `case "task.attach":` and `case "task.retrieve_attachment":` entirely.

Remove `case "hub.queue_status":` (no more queues).

- [ ] **Step 2: Replace handleCreateTask with handleRequestTask**

```go
func (h *Handler) handleRequestTask(ctx context.Context, req *RPCRequest) *RPCResponse {
    var params struct {
        Requester   string `json:"requester"`
        Assignee    string `json:"assignee"`
        Title       string `json:"title"`
        Description string `json:"description"`
    }
    if err := json.Unmarshal(req.Params, &params); err != nil {
        return rpcError(req.ID, -32602, "invalid params: "+err.Error())
    }
    if params.Requester == "" {
        return rpcError(req.ID, -32602, "requester is required")
    }
    if params.Assignee == "" {
        return rpcError(req.ID, -32602, "assignee is required")
    }
    if params.Title == "" {
        return rpcError(req.ID, -32602, "title is required")
    }

    view, err := h.hub.Tasks().RequestTask(ctx, params.Requester, params.Assignee, params.Title, params.Description)
    if err != nil {
        if notFound, ok := errors.AsType[*core.AgentNotFoundError](err); ok {
            resp := rpcError(req.ID, -32001, err.Error())
            resp.Error.Data = notFound.Available
            return resp
        }
        return rpcError(req.ID, -32000, "request task failed: "+err.Error())
    }

    h.logger.Info("task requested", "title", view.Title, "assignee", view.Assignee)

    return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: view}
}
```

- [ ] **Step 3: Replace handleUpdateTask**

```go
func (h *Handler) handleUpdateTask(ctx context.Context, req *RPCRequest) *RPCResponse {
    var params struct {
        AgentID        string              `json:"agent_id"`
        ConversationID string              `json:"conversation_id"`
        Status         protocol.TaskStatus `json:"status"`
        Summary        string              `json:"summary"`
        Reason         string              `json:"reason"`
    }
    if err := json.Unmarshal(req.Params, &params); err != nil {
        return rpcError(req.ID, -32602, "invalid params: "+err.Error())
    }
    if params.AgentID == "" {
        return rpcError(req.ID, -32602, "agent_id is required")
    }
    if params.ConversationID == "" {
        return rpcError(req.ID, -32602, "conversation_id is required")
    }

    update := core.TaskUpdate{
        Status:  params.Status,
        Summary: params.Summary,
        Reason:  params.Reason,
    }
    view, err := h.hub.Tasks().UpdateTask(ctx, params.AgentID, params.ConversationID, update)
    if err != nil {
        return rpcError(req.ID, -32000, "update task failed: "+err.Error())
    }

    h.logger.Info("task updated", "id", view.ConversationID, "status", view.Status)
    return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: view}
}
```

- [ ] **Step 4: Replace handleGetTask and handleListTasks**

```go
func (h *Handler) handleGetTask(ctx context.Context, req *RPCRequest) *RPCResponse {
    var params struct {
        ConversationID string `json:"conversation_id"`
    }
    if err := json.Unmarshal(req.Params, &params); err != nil {
        return rpcError(req.ID, -32602, "invalid params: "+err.Error())
    }
    if params.ConversationID == "" {
        return rpcError(req.ID, -32602, "conversation_id is required")
    }

    view, err := h.hub.Tasks().GetTask(ctx, params.ConversationID)
    if err != nil {
        return rpcError(req.ID, -32000, "get task failed: "+err.Error())
    }
    if view == nil {
        return rpcError(req.ID, -32001, "task not found: "+params.ConversationID)
    }
    return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: view}
}

func (h *Handler) handleListTasks(ctx context.Context, req *RPCRequest) *RPCResponse {
    var params struct {
        Status    protocol.TaskStatus `json:"status"`
        Requester string              `json:"requester"`
        Assignee  string              `json:"assignee"`
    }
    if len(req.Params) > 0 {
        _ = json.Unmarshal(req.Params, &params)
    }

    filter := store.ConversationFilter{
        Assignee:  params.Assignee,
        Requester: params.Requester,
    }

    views, err := h.hub.Tasks().ListTasks(ctx, filter)
    if err != nil {
        return rpcError(req.ID, -32000, "list tasks failed: "+err.Error())
    }
    return &RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: views}
}
```

Note: Status filtering for `ListTasks` is done in-memory after fetching all task conversations because status is derived from events, not a column. For large deployments a materialized view can be added later; v0.2 volumes don't require it.

- [ ] **Step 5: Remove handleAttachFile, handleRetrieveAttachment, handleQueueStatus**

Delete those three methods entirely.

- [ ] **Step 6: Update handleSendMessage to use new RPC method name**

The `msg.send` RPC method stays the same. `handleSendMessage` calls `h.hub.Messages().Send()` which now appends events instead of saving messages. No signature change needed.

- [ ] **Step 7: Verify**

```
cd C:\dev\bifrost && go build ./internal/hub/...
```

Commit: `refactor(hub): RPC handler — task.request, remove attachments and queue_status`

---

### Task 7: Update internal/shim/tools.go

**Files:**
- Modify: `internal/shim/tools.go`
- Modify: `internal/shim/channel.go`

- [ ] **Step 1: Rename bifrost_create_task → bifrost_request_task in tools.go**

In `registerTools`, replace:
```go
registerCreateTask(server, mux, agent)
```
with:
```go
registerRequestTask(server, mux, agent)
```

Replace the `registerCreateTask` function:

```go
func registerRequestTask(server *mcp.Server, mux *hubMux, agent *protocol.Agent) {
    type requestTaskArgs struct {
        Assignee    string `json:"assignee" jsonschema:"agent address to assign the task to"`
        Title       string `json:"title" jsonschema:"short task title"`
        Description string `json:"description,omitempty" jsonschema:"full task description"`
    }

    mcp.AddTool(server, &mcp.Tool{
        Name:        "bifrost_request_task",
        Description: "Request another agent to perform a task. Creates a task conversation and notifies the assignee.",
    }, func(ctx context.Context, req *mcp.CallToolRequest, args requestTaskArgs) (*mcp.CallToolResult, any, error) {
        if args.Assignee == "" {
            return errorResult("assignee is required"), nil, nil
        }
        if args.Title == "" {
            return errorResult("title is required"), nil, nil
        }

        params := map[string]any{
            "requester":   agent.AgentID,
            "assignee":    args.Assignee,
            "title":       args.Title,
            "description": args.Description,
        }

        resp, err := mux.rpcCall(ctx, "task.request", params)
        if err != nil {
            return nil, nil, fmt.Errorf("hub call failed: %w", err)
        }
        if resp.Error != nil {
            result := resp.Error.Message
            if resp.Error.Data != nil {
                if agents, err := formatJSON(resp.Error.Data); err == nil {
                    result += "\n\nAvailable agents:\n" + agents
                }
            }
            return errorResult(result), nil, nil
        }

        text, err := formatJSON(resp.Result)
        if err != nil {
            return errorResult("task requested but failed to format response"), nil, nil
        }
        return &mcp.CallToolResult{
            Content: []mcp.Content{&mcp.TextContent{Text: "Task requested.\n" + text}},
        }, nil, nil
    })
}
```

- [ ] **Step 2: Update registerUpdateTask to use conversation_id**

In `updateTaskArgs`, replace `TaskID string` with `ConversationID string`:

```go
type updateTaskArgs struct {
    ConversationID string `json:"conversation_id" jsonschema:"conversation ID of the task to update"`
    Status         string `json:"status,omitempty" jsonschema:"new status: accepted, in_progress, completed, failed, rejected"`
    Summary        string `json:"summary,omitempty" jsonschema:"completion summary"`
    Reason         string `json:"reason,omitempty" jsonschema:"reason for rejection or failure"`
}
```

Update the params map:

```go
params := map[string]any{
    "agent_id":        agent.AgentID,
    "conversation_id": args.ConversationID,
}
```

Remove the `files` field from both `requestTaskArgs` and `updateTaskArgs` entirely — files are sent as `file` events via `bifrost_send` with `type: "file"`.

- [ ] **Step 3: Update registerGetTask to use conversation_id**

```go
type getTaskArgs struct {
    ConversationID string `json:"conversation_id" jsonschema:"conversation ID of the task"`
}
// ...
resp, err := mux.rpcCall(ctx, "task.get", map[string]any{"conversation_id": args.ConversationID})
```

Update the human-friendly formatting to use `protocol.TaskView` fields (`ConversationID` not `TaskID`):

```go
var task protocol.TaskView
// ...
fmt.Fprintf(&sb, "Task: %s\n", task.ConversationID)
fmt.Fprintf(&sb, "Title: %s\n", task.Title)
fmt.Fprintf(&sb, "Status: %s\n", task.Status)
// ... (no Attachments field)
```

- [ ] **Step 4: Remove registerPeer attachment-related dead code**

No changes needed to `registerPeer` — it remains unchanged.

Remove `registerDND` reference to `DequeueMessages` flushed count — the DND disable response from `dnd.set` now returns `{"ok": true, "flushed": N}` where N is the count of events delivered by the sync engine. The display text stays the same.

- [ ] **Step 5: Update channel.go notification handling**

In `listenHubNotifications`, update the `event.new` case (replaces `message.new`) and update task notification cases to use `TaskView`:

```go
case "event.new":
    var ev protocol.Event
    if err := json.Unmarshal(envelope.Payload, &ev); err != nil {
        log.Warn("unmarshal event payload failed", "error", err)
        continue
    }
    switch ev.Type {
    case protocol.EventTypeMessage:
        cp.Content = ev.Data.Body
        cp.Meta["from"] = ev.FromAgent
        cp.Meta["type"] = string(ev.Data.MessageType)
        cp.Meta["conversation"] = ev.ConversationID
        cp.Meta["ts"] = ev.Timestamp.Format("2006-01-02T15:04:05Z07:00")
        if ev.Data.InReplyTo != "" {
            cp.Meta["in_reply_to"] = ev.Data.InReplyTo
        }
    case protocol.EventTypeStatus:
        cp.Content = fmt.Sprintf("Task status: %s", ev.Data.NewStatus)
        cp.Meta["event"] = "task_updated"
        cp.Meta["conversation"] = ev.ConversationID
        cp.Meta["status"] = string(ev.Data.NewStatus)
        if ev.Data.Summary != "" {
            cp.Meta["summary"] = ev.Data.Summary
        }
        if ev.Data.Reason != "" {
            cp.Meta["reason"] = ev.Data.Reason
        }
    case protocol.EventTypeFile:
        cp.Content = fmt.Sprintf("File: %s (%d bytes)", ev.Data.Filename, ev.Data.Size)
        cp.Meta["event"] = "file"
        cp.Meta["conversation"] = ev.ConversationID
        cp.Meta["filename"] = ev.Data.Filename
    default:
        cp.Content = fmt.Sprintf("Event: %s in %s", ev.Type, ev.ConversationID)
        cp.Meta["event"] = string(ev.Type)
        cp.Meta["conversation"] = ev.ConversationID
    }
```

Also keep the existing `task_requested` case for backward compatibility — the sync engine now also emits `event.new` with type `message` for task conversations, but the `task_requested` notification on first delivery is emitted by the sync engine as a special notification for the channel formatter. Update the sync engine's `deliverToAgent` to emit `task_requested` when delivering the first event of a task conversation:

In `sync.go`, in `deliverToAgent`, after the notify call for the first event in a task conversation:

```go
// For the first event in a task conversation, also send a structured
// task_requested notification so the shim can format it specially.
if conv.IsTask && mark.LastEventID == "" && ev.Type == protocol.EventTypeMessage {
    taskView := &protocol.TaskView{
        ConversationID: conv.ConversationID,
        Title:          conv.Title,
        Assignee:       conv.Assignee,
        Requester:      conv.Requester,
        Status:         protocol.TaskStatusRequested,
        CreatedAt:      conv.CreatedAt,
    }
    e.hub.NotifyAgent(agentID, Notification{
        Type:    "task_requested",
        Payload: taskView,
    })
}
```

Update `channel.go` task_requested handler to use `TaskView`:

```go
case "task_requested":
    var task protocol.TaskView
    if err := json.Unmarshal(envelope.Payload, &task); err != nil {
        log.Warn("unmarshal task_requested payload failed", "error", err)
        continue
    }
    cp.Content = fmt.Sprintf("Task requested: %s", task.Title)
    cp.Meta["event"] = "task_requested"
    cp.Meta["task_id"] = task.ConversationID
    cp.Meta["title"] = task.Title
    cp.Meta["requester"] = task.Requester
    cp.Meta["assignee"] = task.Assignee
```

- [ ] **Step 6: Verify**

```
cd C:\dev\bifrost && go build ./internal/shim/...
```

Commit: `refactor(shim): bifrost_request_task, event.new notifications, remove attachments`

---

## Phase 5: Federation Protocol

### Task 8: Replace peer.message/task_create/task_update with peer.conversation_sync

**Files:**
- Modify: `internal/federation/manager.go`

- [ ] **Step 1: Add SyncConversation method**

Add to `Manager`:

```go
// SyncConversation implements core.ConversationSyncer and core.FederationForwarder.
// It sends a peer.conversation_sync envelope to the target peer.
func (m *Manager) SyncConversation(ctx context.Context, peerID string, conv *protocol.Conversation, events []*protocol.Event) error {
    meta := protocol.ConvMeta{
        ID:           conv.ConversationID,
        Participants: conv.Participants,
        IsTask:       conv.IsTask,
        Title:        conv.Title,
        Assignee:     conv.Assignee,
        Requester:    conv.Requester,
        CreatedAt:    conv.CreatedAt,
    }
    payload, err := json.Marshal(protocol.PeerConversationSyncPayload{
        Conversation: meta,
        Events:       events,
    })
    if err != nil {
        return fmt.Errorf("marshal conversation_sync: %w", err)
    }

    env := &protocol.PeerEnvelope{
        Method:  "peer.conversation_sync",
        ID:      protocol.NewShortID(),
        Version: protocol.ProtocolVersion,
        From:    m.localPeerID,
        Payload: payload,
    }

    return m.sendToPeer(ctx, peerID, env)
}
```

- [ ] **Step 2: Add handler in handlePeerEnvelope**

In `handlePeerEnvelope`, add:

```go
case "peer.conversation_sync":
    m.handleConversationSync(ctx, peerID, env)
```

Remove:

```go
case "peer.message":
    m.handlePeerMessage(ctx, peerID, env)
case "peer.task_create":
    m.handlePeerTaskCreate(ctx, peerID, env)
case "peer.task_update":
    m.handlePeerTaskUpdate(ctx, peerID, env)
```

- [ ] **Step 3: Write handleConversationSync**

```go
func (m *Manager) handleConversationSync(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
    var payload protocol.PeerConversationSyncPayload
    if err := json.Unmarshal(env.Payload, &payload); err != nil {
        m.logger.Error("invalid conversation_sync payload", "peer_id", peerID, "error", err)
        return
    }

    // Upsert the conversation locally.
    existing, err := m.store.GetConversation(ctx, payload.Conversation.ID)
    if err != nil {
        m.logger.Error("get conversation for sync", "error", err)
        return
    }

    conv := &protocol.Conversation{
        ConversationID: payload.Conversation.ID,
        Participants:   payload.Conversation.Participants,
        IsTask:         payload.Conversation.IsTask,
        Title:          payload.Conversation.Title,
        Assignee:       payload.Conversation.Assignee,
        Requester:      payload.Conversation.Requester,
        CreatedAt:      payload.Conversation.CreatedAt,
    }

    if existing == nil {
        if err := m.store.SaveConversation(ctx, conv); err != nil {
            m.logger.Error("save synced conversation", "error", err)
            return
        }
        // Init delivery_state for all local participants.
        if err := m.store.InitDeliveryTargets(ctx, conv); err != nil {
            m.logger.Error("init delivery targets for synced conversation", "error", err)
        }
    }

    // Append new events (ignore duplicates via primary key constraint).
    var lastID string
    for _, ev := range payload.Events {
        ev.ConversationID = conv.ConversationID
        if appendErr := m.store.AppendEvent(ctx, ev); appendErr != nil {
            // Likely a duplicate — log debug and continue.
            m.logger.Debug("append synced event (may be dup)", "event_id", ev.ID, "error", appendErr)
        }
        lastID = ev.ID
    }

    // Nudge local sync engine to deliver to local agents.
    if lastID != "" {
        m.hub.Sync().Nudge(conv.ConversationID)
    }

    // Send ack.
    ackPayload, _ := json.Marshal(protocol.PeerConversationSyncAck{
        ConversationID: conv.ConversationID,
        LastEventID:    lastID,
    })
    ack := &protocol.PeerEnvelope{
        Method:  "peer.conversation_sync_ack",
        ID:      env.ID,
        Version: protocol.ProtocolVersion,
        From:    m.localPeerID,
        Payload: ackPayload,
    }
    m.sendToPeer(ctx, peerID, ack) //nolint:errcheck
}
```

Add `"peer.conversation_sync_ack"` handler (updates delivery mark on the sending side):

```go
case "peer.conversation_sync_ack":
    m.handleConversationSyncAck(ctx, peerID, env)
```

```go
func (m *Manager) handleConversationSyncAck(ctx context.Context, peerID string, env *protocol.PeerEnvelope) {
    var ack protocol.PeerConversationSyncAck
    if err := json.Unmarshal(env.Payload, &ack); err != nil {
        m.logger.Warn("invalid conversation_sync_ack", "peer_id", peerID, "error", err)
        return
    }
    if ack.LastEventID == "" {
        return
    }
    if err := m.store.SetDeliveryMark(ctx, "peer", peerID, ack.ConversationID, ack.LastEventID); err != nil {
        m.logger.Warn("set delivery mark from ack", "peer_id", peerID, "error", err)
    }
}
```

- [ ] **Step 4: Update handleIncomingPeer and ConnectPeer to call BatchSyncForPeer**

Replace `m.flushPeerQueue(ctx, peerID)` calls with:

```go
// Batch-sync all pending conversations.
if syncErr := m.hub.Sync().BatchSyncForPeer(ctx, peerID); syncErr != nil {
    m.logger.Warn("batch sync for reconnected peer failed", "peer_id", peerID, "error", syncErr)
}
```

Remove `flushPeerQueue` method entirely.

- [ ] **Step 5: Remove old forwarding methods**

Delete `ForwardMessage`, `ForwardTaskCreate`, `ForwardTaskUpdate`, `handlePeerMessage`, `handlePeerTaskCreate`, `handlePeerTaskUpdate` from manager.go.

Update `FederationForwarder` interface implementation — `Manager` now only needs to implement `SyncConversation` and `BroadcastAgentStatus`. Remove the old `ForwardMessage`/`ForwardTaskCreate`/`ForwardTaskUpdate` methods from the interface (already done in Task 5 Step 3).

- [ ] **Step 6: Verify**

```
cd C:\dev\bifrost && go build ./internal/federation/...
```

Commit: `feat(federation): peer.conversation_sync replaces peer.message/task_create/task_update`

---

## Phase 6: DND Rewrite, Cleanup, and Integration

### Task 9: Update DND to flush via SyncEngine

**Files:**
- Modify: `pkg/core/dnd.go`

- [ ] **Step 1: Rewrite Disable to use FlushForAgent**

```go
// Disable sets the agent back to online and flushes all pending events via the
// sync engine. Returns the number of conversations flushed.
func (d *DNDManager) Disable(ctx context.Context, agentID string) (int, error) {
    if err := d.store.UpdateAgentStatus(ctx, agentID, protocol.AgentStatusOnline, ""); err != nil {
        return 0, fmt.Errorf("dnd: disable: %w", err)
    }
    if err := d.hub.Sync().FlushForAgent(ctx, agentID); err != nil {
        return 0, fmt.Errorf("dnd: flush: %w", err)
    }
    marks, err := d.store.ListPendingDelivery(ctx, "agent", agentID)
    if err != nil {
        return 0, nil
    }
    return len(marks), nil
}
```

Remove `QueuedCount` (no more `message_queue`). Update `handleDNDStatus` in `handler.go` to report `delivery_state` row count instead:

```go
marks, err := h.hub.Store().ListPendingDelivery(ctx, "agent", params.AgentID)
queued := len(marks)
```

- [ ] **Step 2: Update ShouldQueue in dnd.go**

`ShouldQueue` no longer takes a `*protocol.Message` — it takes an `*protocol.Event`:

```go
// ShouldQueue reports whether the event should be skipped (not notified) for
// an agent in DND. Returns true when DND is on and event is not urgent.
func (d *DNDManager) ShouldQueue(agent *protocol.Agent, ev *protocol.Event) bool {
    if agent.Status != protocol.AgentStatusDND {
        return false
    }
    if d.urgentBreaksThrough && ev.Data.Priority == protocol.PriorityUrgent {
        return false
    }
    return true
}
```

Update the call site in `sync.go` to use the new signature:

```go
if d := e.hub.DND(); d != nil && d.ShouldQueue(agent, ev) {
    continue
}
```

- [ ] **Step 3: Verify**

```
cd C:\dev\bifrost && go build ./pkg/core/...
```

Commit: `refactor(core/dnd): flush via SyncEngine.FlushForAgent, ShouldQueue takes Event`

---

### Task 10: Integration Tests and E2E Update

**Files:**
- Create: `pkg/core/integration_test.go`
- Modify: `e2e/` tests (update task tool names)

- [ ] **Step 1: Write pkg/core/integration_test.go**

```go
package core_test

import (
    "context"
    "testing"
    "time"

    "github.com/marcfargas/bifrost/pkg/core"
    "github.com/marcfargas/bifrost/pkg/protocol"
    "github.com/marcfargas/bifrost/pkg/store"
)

// TestRequestTaskCreatesConversation verifies the full RequestTask flow:
// conversation saved with is_task=true, first message event appended,
// delivery_state rows created for both parties.
func TestRequestTaskCreatesConversation(t *testing.T) {
    ctx := context.Background()
    h, s := newTestHub(t)

    now := time.Now().UTC().Truncate(time.Second)

    requester := &protocol.Agent{
        AgentID: "requester-01", DisplayName: "Requester",
        Status: protocol.AgentStatusOnline, ConnectedAt: now, LastSeen: now,
        ProtocolVersion: "1.0",
    }
    assignee := &protocol.Agent{
        AgentID: "assignee-01", DisplayName: "Assignee",
        Status: protocol.AgentStatusOnline, ConnectedAt: now, LastSeen: now,
        ProtocolVersion: "1.0",
    }
    _ = s.UpsertAgent(ctx, requester)
    _ = s.UpsertAgent(ctx, assignee)

    notifier := &fakeNotifier{}
    h.AddNotifier(notifier)
    h.Sync().Start(ctx)

    view, err := h.Tasks().RequestTask(ctx, requester.AgentID, "agent:assignee-01", "Fix the bug", "Description here")
    if err != nil {
        t.Fatalf("RequestTask: %v", err)
    }
    if view.Status != protocol.TaskStatusRequested {
        t.Errorf("Status: want requested got %q", view.Status)
    }
    if view.Title != "Fix the bug" {
        t.Errorf("Title: want %q got %q", "Fix the bug", view.Title)
    }

    // Verify conversation saved with is_task=true.
    conv, err := s.GetConversation(ctx, view.ConversationID)
    if err != nil || conv == nil {
        t.Fatalf("GetConversation: err=%v conv=%v", err, conv)
    }
    if !conv.IsTask {
        t.Error("conversation should have is_task=true")
    }

    // Verify at least one event was appended.
    events, err := s.ListEventsSince(ctx, view.ConversationID, "")
    if err != nil {
        t.Fatalf("ListEventsSince: %v", err)
    }
    if len(events) == 0 {
        t.Error("expected events in conversation, got none")
    }

    // Verify delivery_state rows exist.
    marks, err := s.ListPendingDelivery(ctx, "agent", "assignee-01")
    if err != nil {
        t.Fatalf("ListPendingDelivery: %v", err)
    }
    if len(marks) == 0 {
        t.Error("expected delivery_state row for assignee, got none")
    }
}

// TestUpdateTaskAppendsStatusEvent verifies that UpdateTask appends a status event
// and GetTask derives the status from it.
func TestUpdateTaskAppendsStatusEvent(t *testing.T) {
    ctx := context.Background()
    h, s := newTestHub(t)

    now := time.Now().UTC().Truncate(time.Second)
    requester := &protocol.Agent{
        AgentID: "req-02", Status: protocol.AgentStatusOnline,
        ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0",
    }
    assignee := &protocol.Agent{
        AgentID: "asgn-02", DisplayName: "asgn-02", Status: protocol.AgentStatusOnline,
        ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0",
    }
    _ = s.UpsertAgent(ctx, requester)
    _ = s.UpsertAgent(ctx, assignee)
    h.Sync().Start(ctx)

    view, err := h.Tasks().RequestTask(ctx, "req-02", "agent:asgn-02", "Task A", "")
    if err != nil {
        t.Fatalf("RequestTask: %v", err)
    }

    updated, err := h.Tasks().UpdateTask(ctx, "asgn-02", view.ConversationID, core.TaskUpdate{
        Status: protocol.TaskStatusAccepted,
    })
    if err != nil {
        t.Fatalf("UpdateTask: %v", err)
    }
    if updated.Status != protocol.TaskStatusAccepted {
        t.Errorf("Status: want accepted got %q", updated.Status)
    }

    // GetTask must derive same status.
    got, err := h.Tasks().GetTask(ctx, view.ConversationID)
    if err != nil || got == nil {
        t.Fatalf("GetTask: err=%v got=%v", err, got)
    }
    if got.Status != protocol.TaskStatusAccepted {
        t.Errorf("GetTask Status: want accepted got %q", got.Status)
    }
}

// TestInvalidTransitionRejected verifies that invalid status transitions are rejected.
func TestInvalidTransitionRejected(t *testing.T) {
    ctx := context.Background()
    h, s := newTestHub(t)

    now := time.Now().UTC().Truncate(time.Second)
    _ = s.UpsertAgent(ctx, &protocol.Agent{AgentID: "req-03", Status: protocol.AgentStatusOnline, ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0"})
    _ = s.UpsertAgent(ctx, &protocol.Agent{AgentID: "asgn-03", DisplayName: "asgn-03", Status: protocol.AgentStatusOnline, ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0"})
    h.Sync().Start(ctx)

    view, _ := h.Tasks().RequestTask(ctx, "req-03", "agent:asgn-03", "Bad transition", "")

    _, err := h.Tasks().UpdateTask(ctx, "asgn-03", view.ConversationID, core.TaskUpdate{
        Status: protocol.TaskStatusCompleted, // invalid: requested → completed
    })
    if err == nil {
        t.Error("expected error for invalid transition requested → completed")
    }
}

// TestSendMessageAppendsEvent verifies that Messages().Send creates a conversation
// and appends a message event.
func TestSendMessageAppendsEvent(t *testing.T) {
    ctx := context.Background()
    h, s := newTestHub(t)

    now := time.Now().UTC().Truncate(time.Second)
    _ = s.UpsertAgent(ctx, &protocol.Agent{AgentID: "sender", Status: protocol.AgentStatusOnline, ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0"})
    _ = s.UpsertAgent(ctx, &protocol.Agent{AgentID: "receiver", DisplayName: "receiver", Status: protocol.AgentStatusOnline, ConnectedAt: now, LastSeen: now, ProtocolVersion: "1.0"})
    h.Sync().Start(ctx)

    msg := &protocol.Message{
        From: "sender", To: "agent:receiver",
        Body:     "hello",
        Type:     protocol.MessageTypeAnswer,
        Priority: protocol.PriorityNormal,
    }
    if err := h.Messages().Send(ctx, msg); err != nil {
        t.Fatalf("Send: %v", err)
    }

    // Find the conversation.
    open := false
    convs, err := s.ListConversations(ctx, store.ConversationFilter{
        Participant: "sender",
        Closed:      &open,
    })
    if err != nil || len(convs) == 0 {
        t.Fatalf("no conversation found: err=%v convs=%d", err, len(convs))
    }

    events, err := s.ListEventsSince(ctx, convs[0].ConversationID, "")
    if err != nil || len(events) == 0 {
        t.Fatalf("no events found: err=%v events=%d", err, len(events))
    }
    if events[0].Data.Body != "hello" {
        t.Errorf("Body: want hello got %q", events[0].Data.Body)
    }
}
```

- [ ] **Step 2: Update E2E tests**

Search E2E test files for `bifrost_create_task` and replace with `bifrost_request_task`. Search for `task_id` parameters and replace with `conversation_id`. Search for `files:` parameters in task tools and remove them.

```
cd C:\dev\bifrost && grep -r "bifrost_create_task\|task_id\|task\.create" e2e/ --include="*.go" -l
```

For each file found, update the tool calls accordingly.

- [ ] **Step 3: Full test suite**

```
cd C:\dev\bifrost && go test ./... -timeout 60s
```

Commit: `test: integration tests for conversation sync, task lifecycle, event delivery`

---

### Task 11: CLI Command Updates

**Files:**
- Modify: `cmd/bifrost/` CLI commands that reference tasks, queue, or conversations

- [ ] **Step 1: Audit CLI commands**

```
cd C:\dev\bifrost && grep -r "task\|queue\|message" cmd/ --include="*.go" -l
```

- [ ] **Step 2: Update task commands**

For any `bifrost tasks list` CLI command: update to call `task.list` RPC (unchanged method name). Update response parsing to use `protocol.TaskView` fields (`ConversationID` instead of `TaskID`, no `Attachments`).

For any `bifrost tasks create` CLI command: rename to `bifrost tasks request`, update to call `task.request` RPC.

For any `bifrost queue` or `bifrost conversations queue` CLI command: remove or replace with `bifrost sync status` which queries `delivery_state` counts per target via a new `hub.sync_status` RPC method in `handler.go`:

```go
case "hub.sync_status":
    return h.handleSyncStatus(ctx, req)
```

```go
func (h *Handler) handleSyncStatus(ctx context.Context, req *RPCRequest) *RPCResponse {
    // Summarize delivery_state: count rows with last_event_id="" per target.
    // Simple approach: list all pending and group.
    agentMarks, err := h.hub.Store().ListPendingDelivery(ctx, "agent", "")
    if err != nil {
        return rpcError(req.ID, -32000, "sync status failed: "+err.Error())
    }
    peerMarks, err := h.hub.Store().ListPendingDelivery(ctx, "peer", "")
    if err != nil {
        return rpcError(req.ID, -32000, "sync status failed: "+err.Error())
    }

    // Group by target.
    agentCounts := make(map[string]int)
    for _, m := range agentMarks {
        agentCounts[m.TargetID]++
    }
    peerCounts := make(map[string]int)
    for _, m := range peerMarks {
        peerCounts[m.TargetID]++
    }

    return &RPCResponse{
        JSONRPC: "2.0",
        ID:      req.ID,
        Result: map[string]any{
            "agents": agentCounts,
            "peers":  peerCounts,
        },
    }
}
```

- [ ] **Step 3: Final build and test**

```
cd C:\dev\bifrost && go build ./... && go test ./... -timeout 120s
```

Commit: `refactor(cmd): update CLI task commands, replace queue status with sync status`

---

## Verification Checklist

Before marking this refactoring complete, verify each invariant:

- [ ] `go test ./...` passes with zero failures
- [ ] `go vet ./...` produces no output
- [ ] `grep -r "message_queue\|peer_message_queue\|SaveTask\|GetTask\|SaveMessage\|GetMessage\|EnqueueMessage\|DequeueMessages\|EnqueuePeerMessage\|DequeuePeerMessages\|ForwardMessage\|ForwardTaskCreate\|ForwardTaskUpdate\|peer\.message\|peer\.task_create\|peer\.task_update" . --include="*.go"` produces no results (outside of test files that explicitly test the migration drop)
- [ ] `grep -r "bifrost_create_task\|task_id" . --include="*.go"` produces no results
- [ ] SQLite migration: starting from an old database (with `messages`, `tasks` tables present) produces a clean new schema with no errors — test with `TestMigrationDropsOldTables` in `pkg/store/sqlite_sync_test.go`:

```go
func TestMigrationDropsOldTables(t *testing.T) {
    dbPath := filepath.Join(t.TempDir(), "legacy.db")

    // Create a bare SQLite file with old tables.
    db, err := sql.Open("sqlite", dbPath)
    if err != nil {
        t.Fatalf("open bare db: %v", err)
    }
    _, err = db.Exec(`CREATE TABLE messages (id TEXT PRIMARY KEY);
                      CREATE TABLE tasks (task_id TEXT PRIMARY KEY);
                      CREATE TABLE message_queue (id INTEGER PRIMARY KEY);`)
    if err != nil {
        db.Close()
        t.Fatalf("create old tables: %v", err)
    }
    db.Close()

    // Open via NewSQLite — migration should drop old tables and create new schema.
    s, err := store.NewSQLite(dbPath)
    if err != nil {
        t.Fatalf("NewSQLite with legacy db: %v", err)
    }
    defer s.Close()

    // Verify old tables are gone.
    ctx := context.Background()
    for _, tbl := range []string{"messages", "tasks", "message_queue"} {
        var name string
        err := s.(*store.SQLiteStore).DB().QueryRowContext(ctx,
            `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
        ).Scan(&name)
        if err == nil {
            t.Errorf("old table %q should have been dropped", tbl)
        }
    }

    // Verify new tables exist.
    for _, tbl := range []string{"conversations", "events", "delivery_state"} {
        var name string
        err := s.(*store.SQLiteStore).DB().QueryRowContext(ctx,
            `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
        ).Scan(&name)
        if err != nil || name == "" {
            t.Errorf("new table %q should exist", tbl)
        }
    }
}
```

Note: This test requires exposing `DB()` on `SQLiteStore` or making the test internal (`package store`). Use `package store` for this test file to access the unexported `db` field via a helper.

---

## Summary of Breaking Changes

| Area | Old | New |
|------|-----|-----|
| MCP tool | `bifrost_create_task` | `bifrost_request_task` |
| MCP tool param | `task_id` | `conversation_id` |
| RPC method | `task.create` | `task.request` |
| Protocol | `peer.message`, `peer.task_create`, `peer.task_update` | `peer.conversation_sync` |
| Store tables | `messages`, `tasks`, `message_queue`, `peer_message_queue`, `attachments` | dropped |
| Store tables | `conversations` (old schema) | `conversations` (new schema: `id`, `is_task`, `title`, etc.) |
| New tables | — | `events`, `delivery_state` |
| Task identity | `task_id` (UUID) | `conversation_id` |
| File delivery | Disk-based `attachments/` + `task.attach` RPC | Inline base64 `file` event (max 256KB) |
| Queue status | `hub.queue_status` RPC | `hub.sync_status` RPC |
