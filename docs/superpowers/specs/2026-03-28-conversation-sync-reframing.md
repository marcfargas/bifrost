# Conversation Sync Reframing

**Date:** 2026-03-28
**Status:** Proposal — needs brainstorming

## Problem

The current protocol treats messages, tasks, and conversations as separate entities with ad-hoc forwarding logic:
- Messages route via the message router (handles federation, DND, offline)
- Tasks have their own forwarding (ForwardTaskCreate, ForwardTaskUpdate)
- Conversations are local-only records with no sync
- A task created on hub A for an agent on hub B exists only on hub A
- The receiving agent gets a message but no task record

This leads to:
- Tasks not arriving at remote agents
- Task status updates lost across federation
- No way for the assignee to `bifrost_update_task` because the task doesn't exist on their hub
- Duplicated forwarding logic that's brittle and incomplete

## Proposed Model

### Conversations are the unit of replication

1. **Everything is a conversation.** Messages belong to conversations. Tasks are conversations with extra metadata (title, status, assignee, attachments).

2. **Conversations replicate to all interested hubs.** If agent A on hub X starts a conversation with agent B on hub Y, both hubs get a copy of the conversation and all its messages.

3. **Tasks = conversations + metadata.** Creating a task creates a conversation with task fields. The conversation (including task metadata) syncs to the assignee's hub. Both hubs can read and update the task.

4. **No separate forwarding for tasks.** When a message is added to a conversation, the conversation sync mechanism delivers it to all hubs that have a participant. Task status updates are just messages in the conversation.

5. **Broadcast conversations.** Channel messages create conversations too. Subscribers' hubs get the conversation.

### What syncs between peers

| What | When | Direction |
|------|------|-----------|
| Conversation creation | On first message to remote agent | Requester → assignee's hub |
| Messages in conversation | On send | Sender's hub → all participant hubs |
| Task metadata updates | On bifrost_update_task | Updater's hub → all participant hubs |
| Conversation close | On inactivity or explicit | Hub that closes → all participant hubs |

### Sync protocol

Instead of separate `peer.message`, `peer.task_create`, `peer.task_update`:

```
peer.conversation_sync {
  conversation: { id, participants, task_metadata?, created_at, last_activity }
  messages: [ { id, from, to, body, type, timestamp } ]  // new messages since last sync
}
```

Each hub tracks a high-water mark per conversation per peer — "last message ID I've synced with peer X for conversation Y."

### Benefits

- One sync mechanism instead of three forwarding paths
- Tasks work across federation automatically
- Offline queuing is per-conversation, not per-message
- Future: group conversations, broadcast history, conversation search

### Migration

- Existing conversations gain a `sync_peers` field (list of hubs interested)
- Tasks get a `conversation_id` (already have this)
- Remove ForwardTaskCreate, ForwardTaskUpdate, ForwardMessage
- Replace with ConversationSync in the federation manager

## Open Questions

- How to handle conflicts? (Two hubs update the same task simultaneously)
- Conversation size limits for sync? (Don't sync 10k-message conversations fully)
- Should conversations be CRDT-based for conflict-free merge?
- How does DND interact with conversation sync? (Sync the conversation but don't notify?)
