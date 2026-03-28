# Conversation Sync — Design Spec

**Date:** 2026-03-28
**Status:** Approved

## Overview

Conversations are the unit of replication. Everything is a conversation. Tasks are conversations with metadata. Messages are events in conversations. A single sync engine delivers events to both local agents and federated peers using identical mechanics.

## Data Model

### conversations

```sql
CREATE TABLE conversations (
    id            TEXT PRIMARY KEY,
    participants  TEXT NOT NULL DEFAULT '[]',   -- JSON []string of agent_ids
    is_task       INTEGER NOT NULL DEFAULT 0,
    title         TEXT NOT NULL DEFAULT '',     -- task only
    assignee      TEXT NOT NULL DEFAULT '',     -- task only
    requester     TEXT NOT NULL DEFAULT '',     -- task only
    created_at    TEXT NOT NULL,
    closed        INTEGER NOT NULL DEFAULT 0,
    closed_reason TEXT NOT NULL DEFAULT ''
);
```

### events

```sql
CREATE TABLE events (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    type            TEXT NOT NULL,     -- message, status, metadata, file, participant.added, participant.removed
    from_agent      TEXT NOT NULL,
    data            TEXT NOT NULL,     -- JSON: body, priority, filename, content (base64), status, etc.
    timestamp       TEXT NOT NULL
);
CREATE INDEX idx_events_conversation ON events(conversation_id, timestamp);
```

### delivery_state

```sql
CREATE TABLE delivery_state (
    target_type     TEXT NOT NULL,    -- 'peer' or 'agent'
    target_id       TEXT NOT NULL,    -- peer_id or agent_id
    conversation_id TEXT NOT NULL,
    last_event_id   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (target_type, target_id, conversation_id)
);
```

### Dropped tables

`messages`, `tasks`, `message_queue`, `peer_message_queue`, `attachments` — all replaced by `events` + `delivery_state`.

### Kept tables

`agents`, `peers`, `subscriptions` — unchanged.

## Event Types

| Type | Data fields | Who can emit |
|------|------------|--------------|
| `message` | body, message_type, priority, in_reply_to? | Any participant |
| `status` | new_status, summary?, reason? | Assignee (task conversations only) |
| `metadata` | field, value | Assignee or requester (task conversations only) |
| `file` | filename, size, content_type, content (base64) | Any participant |
| `participant.added` | agent_id | Any current participant |
| `participant.removed` | agent_id | Self |

Max file size: 256KB (inline as base64 in event data).

## Task Status

Derived from the latest `status` event in the conversation. No separate task table.

Valid status values: `requested`, `accepted`, `in_progress`, `completed`, `failed`, `rejected`.

A task conversation is created with `is_task=true`, `title`, `assignee`, `requester`. The initial status is `requested` (implied — no status event means requested).

## Sync Engine

The sync engine is the single delivery mechanism for all events, to both local agents and federated peers.

### Flow

1. Agent calls `bifrost_send` or `bifrost_request_task`
2. Hub resolves or creates the conversation
3. Hub appends event to the `events` table
4. Hub nudges the sync engine: "new event in conversation X"
5. Sync engine queries `delivery_state` for all targets of this conversation
6. For each target where `last_event_id < new_event_id`:
   - **Peer target:** push event(s) via federation (`peer.conversation_sync`)
   - **Agent target:** push via `NotifyAgent` (channel notification)
   - **DND agent:** skip notification, don't advance the mark
7. On successful delivery: advance `last_event_id` in `delivery_state`

### Conversation replication trigger

When a conversation has participants on different hubs:
- The hub that creates the conversation checks if any participant is on a peer hub
- If so, it creates a `delivery_state` entry for that peer with `last_event_id = ""`
- The sync engine sees the empty mark → sends the full conversation (metadata + all events)
- The peer hub creates its local replica (conversation + events + delivery_state for its local agents)

Explicit subscription (`bifrost_subscribe` to a conversation or task) also triggers replication:
- The subscriber's agent_id is added to `delivery_state` for that conversation
- If the subscriber is on a peer hub, the peer hub gets a replica too

### On peer reconnect

No separate queue. Events are in the stream. On reconnect:
1. Hub queries `delivery_state` for all conversations where `target_type='peer' AND target_id=<reconnected_peer>`
2. For each, queries events after `last_event_id`
3. Sends batch via `peer.conversation_sync`
4. Advances marks on acknowledgment

### DND handling

DND is checked on the receiving hub, not the sending hub. The event syncs to all replicas regardless. The local hub decides whether to notify the agent:
- DND off: notify and advance mark
- DND on + not urgent: don't notify, don't advance mark
- DND on + urgent: notify and advance mark
- DND disabled: flush all undelivered events (advance marks)

## Federation Protocol

Replace `peer.message`, `peer.task_create`, `peer.task_update`, `peer.agent_status` with:

```
peer.conversation_sync {
    conversation: {         -- full metadata, included on first sync or when changed
        id, participants, is_task, title, assignee, requester, created_at
    }
    events: [               -- events since last sync point
        { id, type, from_agent, data, timestamp }
    ]
}
```

Response:
```
peer.conversation_sync_ack {
    conversation_id: string
    last_event_id: string    -- highest event ID the peer has now
}
```

Keep `peer.sync_agents` and `peer.heartbeat` — agent discovery and liveness are separate from conversation sync.

## MCP Tool Changes

| Current | New | Change |
|---------|-----|--------|
| `bifrost_send` | `bifrost_send` | Same interface, internally appends message event |
| `bifrost_create_task` | `bifrost_request_task` | Renamed. Creates task conversation + first event |
| `bifrost_update_task` | `bifrost_update_task` | Appends status event to conversation |
| `bifrost_get_task` | `bifrost_get_task` | Derives task state from events |
| `bifrost_list_tasks` | `bifrost_list_tasks` | Queries conversations where is_task=true |
| `bifrost_list_conversations` | `bifrost_list_conversations` | Same |
| `bifrost_subscribe` | `bifrost_subscribe` | Triggers replication if needed |

## Channel Notification Format

Unchanged. The sync engine calls `NotifyAgent` which emits `notifications/claude/channel` with `{content, meta}`. The shim's `listenHubNotifications` formats the `<channel>` tag.

## Migration

Clean break. New schema, drop old tables. Run migration in store constructor (check if old tables exist → drop, create new). v0.1.x has no production data requiring preservation.

## What This Fixes

- Tasks created on hub A for agents on hub B are replicated automatically
- Task status updates sync bidirectionally
- Offline delivery works for everything (events are in the stream, not a separate queue)
- One sync mechanism instead of three ad-hoc forwarding paths
- DND is consistent (receiving hub decides)
