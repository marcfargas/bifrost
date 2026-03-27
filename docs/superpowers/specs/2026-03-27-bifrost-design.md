# Bifrost — Cross-Agent Communication Hub

**Date:** 2026-03-27
**Status:** Design approved
**License:** LGPL-3.0

## Overview

Bifrost is a lightweight Go daemon + MCP server enabling Claude Code agents (and other MCP clients) running in separate projects to discover each other, exchange messages, delegate tasks, and collaborate — locally or across the internet.

## Principles

- **Zero friction locally** — first agent auto-starts the daemon, no setup needed
- **Federation by default** — hubs peer over the internet via libp2p with NAT hole punching
- **Lightweight** — single Go binary, ~10MB RSS per process
- **Push-based** — uses Claude Code's `claude/channel` notification system for real-time message delivery
- **Tested thoroughly** — unit, integration, and E2E tests including real Claude Code instances via `claude -p`
- **Platform-agnostic** — Windows (named pipes), Linux/macOS (unix sockets)

## Three Modes of Operation

### Mode 1: Embedded daemon (zero friction, local only)

The first shim to connect detects no running daemon and auto-starts one. Subsequent shims discover the running daemon and connect to it.

```
Claude Code --stdio--> bifrost shim --> (no daemon?) --> auto-start daemon
                                    --> connect via named pipe / unix socket
```

**Discovery mechanism:**
- Unix: unix domain socket at `$XDG_RUNTIME_DIR/bifrost/hub.sock` (fallback: `/tmp/bifrost-$UID/hub.sock`)
- Windows: named pipe `\\.\pipe\bifrost-hub`
- Lock file with PID at `~/.bifrost/hub.pid` for stale detection

**Lifecycle:**
- First shim starts daemon as a background process
- Daemon stays alive while any shim is connected (+ configurable grace period, default 30s)
- Daemon auto-exits when all shims disconnect and grace period expires
- `bifrost hub stop` for manual shutdown

### Mode 2: Standalone daemon (local or remote)

User explicitly starts the daemon.

```bash
bifrost hub start                           # foreground
bifrost hub start -d                        # background
bifrost hub start --hub.tcp.enabled=true    # enable remote shim connections
```

### Mode 3: Daemon as MCP server directly (no shim)

The daemon itself speaks MCP over Streamable HTTP. Claude Code connects directly via URL — no shim process needed.

```json
{
  "mcpServers": {
    "bifrost": {
      "type": "url",
      "url": "http://localhost:7433/mcp"
    }
  }
}
```

In this mode the daemon manages MCP session state per connected agent.

## Agent Identity

Each agent registers with:

| Field | Source | Example |
|-------|--------|---------|
| `agent_id` | Hash of hostname + local_path (stable across sessions) | `a1b2c3d4` |
| `project_name` | Directory name or `package.json`/`go.mod` name | `api-backend` |
| `username` | OS current user | `marc` |
| `hostname` | OS hostname | `marcbook` |
| `local_path` | Working directory | `/home/marc/dev/api` |
| `capabilities` | Auto-detected from project structure | `["typescript", "react", "vite"]` |
| `display_name` | Optional, user-set via `--display-name` | `frontend` |
| `status` | Managed by hub | `online`, `idle`, `offline`, `unreachable`, `dnd` |

### Alias System

- Aliases are human-friendly names that resolve to an `agent_id`
- Auto-generated from `project_name` and `display_name` (if set)
- Aliases are hub-global (spanning federation): if two agents would share an alias, **neither gets it**
- Agents can be addressed by alias or `agent_id`
- Ambiguous or unknown address: tool returns error with full list of connected agents, prompting the agent to clarify

## Core Domain Model

### Conversation

```
conversation_id  string     — short random ID (8 chars)
participants     []agent_id
task_id          string?    — linked task, if any
created_at       timestamp
last_activity    timestamp
closed           bool
closed_reason    string?    — "inactivity" | "explicit"
```

**Auto-close rules:**
- Conversations auto-close after a configurable inactivity timeout (default 10min)
- Inactivity timer resets on any message, including task status updates
- No special close trigger for task completion — completion is just another message that resets the timer
- Agents do not need to explicitly close conversations; they close themselves

**Lifecycle:**
- Auto-created on first message between participants
- Referenced by short `conversation_id` in follow-up messages
- `in_reply_to` on messages also maintains threading within a conversation

### Message

```
id               string     — unique ID
conversation_id  string
from             agent_id
to               string     — agent alias/id, "channel:name", "task:id", or "*" for broadcast
type             enum       — QUESTION | ANSWER | CONTEXT | STATUS | ERROR
subject          string?
body             string
in_reply_to      string?    — message ID
priority         enum       — low | normal | urgent
timestamp        timestamp
acknowledged     bool
noreply          bool       — true for CLI-originated messages
```

### Task

```
task_id          string
conversation_id  string     — conversation where it was requested
requester        agent_id
assignee         agent_id
title            string
description      string     — long-form context/spec
status           enum       — requested | accepted | in_progress | completed | failed | rejected
reason           string?    — for rejected/failed
summary          string?    — for completed
attachments      []attachment
created_at       timestamp
updated_at       timestamp
```

Tasks are subscribed to via the unified `bifrost_subscribe` tool (tasks are channels: `task:{task_id}`). Subscribers receive status change notifications.

**Task lifecycle:**
```
requested --> accepted --> in_progress --> completed
                |                          |
                v                          v
             rejected                    failed
```

Clarification happens via messages in the task's conversation before accepting.

**Task ownership:** Tasks live on the assignee's hub. When a task is created for a remote agent, the full task (description + attachments) is pushed to the assignee's hub. That hub becomes the source of truth. The requester's hub keeps a reference (task_id, status, subscription).

**Task request notification:** When a task is assigned, the assignee receives the full task details inline in the channel notification (without attachment contents). `bifrost_get_task` is for later reference or downloading attachments.

### Attachment

```
attachment_id    string
task_id          string
filename         string
content_type     string
size             int64
uploaded_by      agent_id
uploaded_at      timestamp
```

File content stored on disk at `{data_dir}/attachments/{attachment_id}/{filename}`.

Attachments are always part of a task operation (create or update). Max file size is configurable (default 10MB).

## Channel Notification Format

Bifrost uses Claude Code's `claude/channel` capability for push-based message delivery. The MCP server declares:

```json
{
  "capabilities": {
    "tools": {},
    "experimental": {
      "claude/channel": {}
    }
  }
}
```

### Notification formats

**Regular message:**
```xml
<channel source="bifrost" from="api-backend" type="question"
  conversation="abc123" hostname="marcbook" username="marc"
  project_path="/home/marc/dev/api" ts="2026-03-27T14:00:00Z">
What fields does the /auth/login endpoint return?
</channel>
```

**Task requested:**
```xml
<channel source="bifrost" from="web-frontend" type="task_requested"
  task_id="t42" conversation="abc123" ts="2026-03-27T14:05:00Z">
Title: Implement GET /users/:id endpoint
Description: Should return {id, name, email, created_at}.
  Validate that :id is a valid UUID. Return 404 if not found.
Attachments: 1 file (user-schema.json, 2.1KB)
</channel>
```

**Task status update:**
```xml
<channel source="bifrost" from="api-backend" type="task_update"
  task_id="t42" status="completed" ts="2026-03-27T15:00:00Z">
Endpoint implemented and tested. Returns 200 with user object or 404.
</channel>
```

**DND reminder (periodic, from shim):**
```xml
<channel source="bifrost" type="dnd_reminder">
DND is active. 3 messages queued. Use bifrost_dnd to disable when ready.
</channel>
```

**Agent join/leave:**
```xml
<channel source="bifrost" type="agent_joined" agent="mobile-app"
  hostname="bob-laptop" username="bob">
</channel>
```

**Noreply message (from CLI):**
```xml
<channel source="bifrost" from="cli" type="context" noreply="true"
  ts="2026-03-27T14:00:00Z">
Deploy is starting, hold off on API changes.
</channel>
```

### MCP instructions

Injected into Claude Code's context via the MCP server's `instructions` field:

```
Messages from other agents arrive as <channel source="bifrost" ...>.
Use bifrost_send to reply — set the "to" field to the agent name from
the "from" attribute. Address agents by name (e.g., "api-backend").
If the name is ambiguous, the tool will list available agents — pick
the right one and retry.

Addressing: use "agent:name" for agents, "channel:name" for broadcast
channels, "task:id" for task-scoped messages.

When you receive a task request (type="task_requested"), review it
carefully. If you need clarification, send a QUESTION message in the
task's conversation before accepting. Accept with bifrost_update_task.

Use bifrost_dnd to enable Do Not Disturb when you need focus time.
Remember to disable it when you're ready for messages again.
```

## MCP Tools

11 tools exposed to Claude Code:

### Agent discovery

**`bifrost_list_agents`**
- Parameters: `status?` (online | all)
- Returns: agent list with aliases, status, hub origin; includes peered hubs and their agents

**`bifrost_whoami`**
- Parameters: none
- Returns: this agent's identity, aliases, connected hub info, DND status

### Messaging

**`bifrost_send`**
- Parameters:
  - `to` — `agent:name`, `channel:name`, or `task:id`
  - `body` — message content
  - `type?` — QUESTION | ANSWER | CONTEXT | STATUS | ERROR (default: CONTEXT)
  - `conversation_id?` — continue existing conversation
  - `in_reply_to?` — message ID for threading
  - `priority?` — low | normal | urgent (default: normal)
  - `files?` — file paths to attach (only valid when `to` is `task:id`; error otherwise)
- Returns: delivery status ("delivered" | "queued (agent offline)" | "queued (hub unreachable)")

### Conversations

**`bifrost_list_conversations`**
- Parameters: `active_only?` (default: true)
- Returns: conversation list with participants, linked task, last activity

### Tasks

**`bifrost_create_task`**
- Parameters:
  - `assignee` — agent name or ID
  - `title` — short task title
  - `description` — long-form context, spec, requirements
  - `files?` — file paths to attach
- Returns: task ID, delivery status

**`bifrost_update_task`**
- Parameters:
  - `task_id`
  - `status?` — accepted | in_progress | completed | failed | rejected
  - `description?` — append context or notes
  - `files?` — attach additional files
  - `summary?` — completion summary
  - `reason?` — rejection/failure reason
- Returns: confirmation, notifies requester + subscribers

**`bifrost_get_task`**
- Parameters:
  - `task_id`
  - `include_attachments?` — downloads files to local temp dir, returns paths
- Returns: full task details

**`bifrost_list_tasks`**
- Parameters:
  - `status?` — filter by status
  - `role?` — requester | assignee | subscriber
- Returns: task list

### Channels & Subscriptions

**`bifrost_subscribe`**
- Parameters: `target` — `channel:name` or `task:id`
- Returns: confirmation

**`bifrost_list_channels`**
- Parameters: none
- Returns: channel list with subscriber counts

### Do Not Disturb

**`bifrost_dnd`**
- Parameters:
  - `enabled` — bool
  - `reason?` — displayed to agents who try to message this agent
- Behavior when enabled:
  - Hub queues messages (urgent priority still breaks through)
  - Shim injects periodic reminder (configurable, default 5min)
  - Reminder shows queued message count
- Behavior when disabled:
  - Queued messages flush immediately as channel notifications

### Federation

**`bifrost_peer`**
- Parameters:
  - `action` — `new` | `join`
  - `code?` — magic code (required for `join`)
- Returns:
  - `new`: generated magic code
  - `join`: connection status, remote agents list

## Hub Architecture

The hub is the central daemon managing all state and routing all communication.

```
+--------------------------------------------------+
|                   Hub Process                     |
|                                                   |
|  +----------+  +----------+  +--------------+    |
|  | Socket/  |  | TCP      |  | HTTP/MCP     |    |
|  | Pipe     |  | Listener |  | Listener     |    |
|  | Listener |  | (opt-in) |  | (opt-in)     |    |
|  +----+-----+  +----+-----+  +------+-------+    |
|       |              |               |            |
|       +--------------+---------------+            |
|                      v                            |
|              +---------------+                    |
|              |  Connection   |  maps agent_id     |
|              |  Manager      |  to connection     |
|              +-------+-------+  regardless of     |
|                      |          transport          |
|                      v                            |
|  +--------------------------------------------+  |
|  |              Core (pkg/)                    |  |
|  |  +---------+ +------+ +---------------+    |  |
|  |  | Agents  | | Msgs | | Conversations |    |  |
|  |  +---------+ +------+ +---------------+    |  |
|  |  +-------+ +------------+ +----------+    |  |
|  |  | Tasks | | Attachments| | Channels |    |  |
|  |  +-------+ +------------+ +----------+    |  |
|  +---------------------+----------------------+  |
|                         v                         |
|              +-----------+----+                   |
|              |   SQLite Store |                   |
|              +----------------+                   |
|                                                   |
|  +----------------+  +---------------------+     |
|  | Housekeeping   |  | Federation Manager  |     |
|  | (periodic)     |  | (peer connections)  |     |
|  +----------------+  +---------------------+     |
+--------------------------------------------------+
```

### Connection Manager

Maps `agent_id` to connection, regardless of transport (socket, pipe, TCP, HTTP/SSE). When the core says "notify agent X", the connection manager delivers via the appropriate transport.

### Notification Dispatcher

- Message delivery: push to recipient's connection
- Task status changes: push to all subscribers
- Agent join/leave: push to all connected agents
- DND reminders: periodic push from shim to its own Claude Code session

### Housekeeping (configurable interval, default 5min)

- Close conversations past inactivity timeout
- Prune messages past retention period
- Prune completed/failed tasks past retention period
- Prune orphaned attachments
- Mark agents as `offline` if no heartbeat past threshold
- Mark federated agents as `unreachable` if peer hub is down

## Federation

Agents only talk to their local hub. Hubs peer with each other and route messages transparently. Agents don't need to know whether a recipient is local or remote.

```
Developer A's machine              Developer B's machine
+----------+ +----------+         +----------+ +----------+
| frontend | | backend  |         | mobile   | | infra    |
| agent    | | agent    |         | agent    | | agent    |
+----+-----+ +----+-----+         +----+-----+ +----+-----+
     |             |                    |             |
     +------+------+                    +------+------+
            v                                  v
     +-------------+  federation peering +-------------+
     |  Hub A      |<==================>|  Hub B      |
     |  (local)    |                     |  (local)    |
     +-------------+                     +-------------+
```

### Transports (cumulative, all can be active simultaneously)

| Transport | Purpose | Default | Auth |
|-----------|---------|---------|------|
| **Local socket/pipe** | Local shim connections | Always on | Implicit (same user) |
| **TCP/TLS** | Remote shim connections | Off | Token-based |
| **MCP HTTP** | Direct MCP mode (no shim) | Off | Per-session |
| **libp2p + DHT** | Internet federation | On | Noise protocol (magic code derived) |
| **mDNS** | LAN hub discovery | Off | No auth (explicit opt-in) |
| **Direct TCP/TLS** | Known-address federation | Off | Token-based pairing |

### libp2p Federation (default)

Magic code peering — zero configuration internet federation:

1. Hub A: `bifrost peer new` (or `bifrost_peer` MCP tool) generates keypair, derives magic code, announces on DHT
2. Hub B: `bifrost peer join BIFROST-AXKM-TNVR-Q7PD` derives same key, finds Hub A on DHT
3. Noise handshake establishes encrypted stream
4. Hubs exchange persistent identity tokens for future reconnection
5. Subsequent reconnects happen automatically via DHT (peer IDs are known)

No IP addresses. No port forwarding. No VPN. Just a code.

### What peers exchange

| Operation | Direction | Payload |
|-----------|-----------|---------|
| `peer.sync_agents` | Bidirectional | Agent list (id, aliases, status, identity metadata) |
| `peer.message` | Toward recipient's hub | Full message |
| `peer.task_create` | Toward assignee's hub | Full task + attachment contents |
| `peer.task_update` | Toward requester's hub + subscribers | Status change |
| `peer.agent_status` | Hub with change toward all peers | Agent online/offline/dnd |
| `peer.heartbeat` | Bidirectional | Ping/pong with timestamp |

### Alias resolution across federation

1. Local agents first
2. Peer agents second
3. Conflict (same alias on multiple agents, local or remote): alias removed for all conflicting agents
4. No match: error with full agent list including peer agents

### Disconnection handling

| Scenario | Behavior |
|----------|----------|
| Recipient agent online, local | Deliver immediately |
| Recipient agent online, on peer hub | Forward to peer, peer delivers |
| Recipient agent offline, local | Queue, deliver on reconnect |
| Recipient agent offline, on peer hub | Forward to peer, peer queues |
| Peer hub unreachable | Queue locally, forward on reconnection |

The sender always gets feedback:
- `"Message delivered to api-backend"`
- `"Message queued for api-backend (agent offline)"`
- `"Message queued for api-backend (hub unreachable)"`

**Peer heartbeat:** configurable interval (default 30s). N consecutive failures mark peer as unreachable. All agents on that peer marked `unreachable`.

**Reconnection flow:**
1. Hub A detects peer Hub B is back
2. Both hubs exchange queued messages/task operations
3. Both hubs sync agent registries

### Protocol versioning

Peers must match major protocol version. Mismatch results in a hard rejection with clear error message. No backwards compatibility layer in v1.

## Authentication

### Local (socket/pipe, localhost TCP)

No auth required. Connection via unix socket / named pipe or localhost TCP implies trust (same user context).

### Remote (federation)

**libp2p:** Noise protocol encryption with keys derived from magic code. After initial pairing, persistent identity tokens for reconnection.

**Direct TCP/TLS:** Pairing flow:
1. Hub generates pairing code: `bifrost peer new`
2. Remote hub presents code: `bifrost peer join CODE`
3. Hubs exchange persistent tokens stored in config directory
4. Subsequent connections use tokens automatically

### DND and urgent messages

When an agent enables DND, messages are queued. Messages with `priority: urgent` break through DND and are delivered immediately.

## Persistence

**SQLite** via `modernc.org/sqlite` (pure Go, no CGO — critical for cross-compilation).

**Tables:**
- `agents` — registered agents, last-seen, status
- `messages` — all messages with delivery status
- `conversations` — lifecycle state
- `tasks` — full task data with lifecycle state
- `attachments` — metadata (files on disk)
- `channels` — channel definitions
- `channel_subscriptions` — agent-channel memberships
- `peers` — federated hub connections and tokens
- `message_queue` — queued messages for offline agents/unreachable peers

**DB location:** `{data_dir}/hub.db`
**Attachment storage:** `{data_dir}/attachments/{attachment_id}/{filename}`

## Configuration

```toml
# Linux: ~/.config/bifrost/config.toml
# macOS: ~/Library/Application Support/bifrost/config.toml
# Windows: %APPDATA%/bifrost/config.toml

[hub]
grace_period = "30s"
heartbeat_interval = "30s"
housekeeping_interval = "5m"

[hub.local]
# Unix socket / named pipe — always on, not configurable

[hub.tcp]
enabled = false
listen = "0.0.0.0:7432"
tls_cert = ""
tls_key = ""

[hub.mcp]
enabled = false
port = 7433

[storage]
# data_dir defaults to XDG_DATA_HOME/bifrost (Linux),
# ~/Library/Application Support/bifrost (macOS),
# %LOCALAPPDATA%/bifrost (Windows)
data_dir = ""
max_file_size = "10MB"

[retention]
messages = "30d"
completed_tasks = "90d"
attachments = "30d"
conversations = "90d"

[conversations]
inactivity_timeout = "10m"

[dnd]
reminder_interval = "5m"
urgent_breaks_through = true

[federation.libp2p]
enabled = true
dht_bootstrap = []                  # additional bootstrap nodes

[federation.mdns]
enabled = false                     # no auth, explicit opt-in

[federation.direct]
enabled = false
listen = "0.0.0.0:7434"
tls_cert = ""
tls_key = ""

[logging]
level = "info"                      # debug | info | warn | error
file = ""                           # default: stderr
```

All values have sensible defaults. Zero config works for local embedded mode.

CLI flags override config file values for one-off runs.

## Plugin Packaging

Bifrost ships as a Claude Code plugin wrapping the Go binary.

**Binary distribution:**
- Plugin checks for `bifrost` binary on PATH with matching version
- If missing or wrong version, downloads correct binary from GitHub releases
- Version must match the plugin version (protocol compatibility)

**Plugin structure:**
```
plugin/
  .claude-plugin/
    plugin.json
  .mcp.json                — points to bifrost binary
  skills/
    setup/
      SKILL.md             — /bifrost:setup skill
  README.md
```

**`.mcp.json`:**
```json
{
  "mcpServers": {
    "bifrost": {
      "command": "bifrost",
      "args": ["shim"]
    }
  }
}
```

**Claude Code launch:**
```bash
claude --channels plugin:bifrost
```

Users who don't want the plugin can configure the Go binary directly in `.claude/settings.json`.

## CLI

```
bifrost hub start [flags]           — start daemon (foreground)
bifrost hub start -d                — start daemon (background)
bifrost hub stop                    — stop running daemon
bifrost hub status                  — daemon status, connected agents, peers

bifrost shim [flags]                — start MCP stdio server (Claude Code spawns)
  --project-name NAME               — override auto-detected name
  --display-name NAME               — set display name

bifrost peer new                    — generate magic code for federation
bifrost peer join CODE              — join federated hub
bifrost peer list                   — list peered hubs (convenience; also shown in `bifrost agents`)

bifrost agents                      — list connected agents
bifrost send AGENT MESSAGE          — send noreply message (debugging)

bifrost config                      — show effective config
bifrost version                     — version + protocol version
```

## Project Structure

```
github.com/marcfargas/bifrost/
+-- cmd/
|   +-- bifrost/
|       +-- main.go                 — CLI entry
+-- pkg/
|   +-- core/
|   |   +-- agents.go               — agent registry, alias resolution
|   |   +-- messages.go             — message routing, delivery status
|   |   +-- conversations.go        — lifecycle, inactivity tracking
|   |   +-- tasks.go                — request/accept/update lifecycle
|   |   +-- attachments.go          — file storage, size limits
|   |   +-- channels.go             — pub/sub broadcast
|   |   +-- hub.go                  — core orchestrator
|   +-- store/
|   |   +-- store.go                — storage interface
|   |   +-- sqlite.go               — SQLite implementation
|   +-- protocol/
|   |   +-- types.go                — Agent, Message, Task, Conversation, etc.
|   |   +-- rpc.go                  — JSON-RPC 2.0 methods + serialization
|   |   +-- version.go              — protocol version, compat check
|   +-- detect/
|       +-- project.go              — project name + capabilities detection
+-- internal/
|   +-- hub/
|   |   +-- server.go               — hub process orchestration
|   |   +-- connections.go          — connection manager (agent_id -> conn)
|   |   +-- notifications.go        — dispatch push notifications
|   |   +-- housekeeping.go         — retention pruning, conversation closing
|   +-- transport/
|   |   +-- socket.go               — unix socket (*nix)
|   |   +-- pipe.go                 — named pipe (Windows)
|   |   +-- tcp.go                  — TCP/TLS listener
|   |   +-- mcp_http.go             — MCP Streamable HTTP endpoint
|   +-- federation/
|   |   +-- manager.go              — peer registry, message forwarding
|   |   +-- libp2p.go               — libp2p + DHT + Noise
|   |   +-- mdns.go                 — mDNS local discovery
|   |   +-- direct.go               — TCP/TLS direct peering
|   |   +-- queue.go                — offline message queue, retry
|   +-- shim/
|   |   +-- shim.go                 — MCP stdio <-> hub bridge
|   |   +-- tools.go                — MCP tool handlers
|   |   +-- channel.go              — claude/channel notification emitter
|   |   +-- discovery.go            — find running hub (socket/pipe/PID)
|   |   +-- autostart.go            — spawn hub if missing
|   |   +-- dnd.go                  — DND state + periodic reminders
|   +-- config/
|       +-- config.go               — TOML loading, XDG/AppData paths
|       +-- defaults.go             — default values
+-- test/
|   +-- integration/
|   |   +-- hub_test.go
|   |   +-- shim_test.go
|   |   +-- federation_test.go
|   |   +-- multi_agent_test.go
|   +-- e2e/
|   |   +-- claude_test.go          — claude -p tests (build tag: e2e)
|   |   +-- federation_test.go      — cross-hub claude -p test (build tag: e2e)
|   +-- testutil/
|       +-- helpers.go              — mock MCP client, test fixtures
+-- plugin/
|   +-- .claude-plugin/
|   |   +-- plugin.json
|   +-- .mcp.json
|   +-- skills/
|   |   +-- setup/
|   |       +-- SKILL.md
|   +-- README.md
+-- go.mod
+-- go.sum
+-- LICENSE                          — LGPL-3.0
+-- DESIGN.md
```

## Testing Strategy

### Layer 1: Unit tests — `pkg/` core logic

- Agent registry: alias resolution, conflict removal, federation-wide disambiguation
- Messages: routing, delivery status, conversation auto-creation
- Conversations: inactivity tracking, timer reset on activity, auto-close
- Tasks: state machine transitions, validation
- Store: CRUD, concurrent access, retention pruning
- Protocol: serialization, version checks

### Layer 2: Integration tests — `internal/` wiring

- Shim to hub over socket/pipe: register, send/receive, task flow
- Two shims exchanging messages through hub
- Auto-start: first shim spawns hub, second discovers it
- DND: messages queue, urgent breaks through, reminders fire, flush on disable
- Federation: two hubs peering via libp2p, cross-hub message delivery
- Offline queue: disconnect peer, send messages, reconnect, verify delivery
- Attachments: upload with task create, download via get_task
- Channel notifications: verify `notifications/claude/channel` format
- Platform-specific: named pipe (Windows), unix socket (*nix)

### Layer 3: E2E tests — `test/e2e/` (build tag: `e2e`)

**Local E2E:**
- Two `claude -p --model haiku --max-turns 3 --max-budget-usd 0.25` instances
- Agent A sends question to agent B via bifrost
- Agent B receives channel notification, replies
- Agent A receives the reply
- Task creation and acceptance flow

**Federated E2E:**
- Two hub instances, peered via libp2p (local DHT bootstrap for test isolation)
- One `claude -p --model haiku` per hub
- Agent A on hub 1 sends message to agent B on hub 2
- Agent B receives, replies
- Agent A creates task for agent B with file attachment
- Agent B receives task (with details inline), accepts, completes
- Agent A gets completion notification

### CI matrix

- Unit + integration: every push — Windows, Linux, macOS
- E2E: manual trigger or release tags only

## Dependencies

| Dependency | Purpose |
|-----------|---------|
| `modelcontextprotocol/go-sdk` | MCP protocol (tools, resources, transports) |
| `modernc.org/sqlite` | Pure Go SQLite (no CGO) |
| `libp2p/go-libp2p` | P2P networking, NAT traversal |
| `libp2p/go-libp2p-kad-dht` | Kademlia DHT for peer discovery |
| `BurntSushi/toml` | Config file parsing |

The MCP SDK is used behind an interface to allow extension for `claude/channel` notifications (Claude Code extension, not core MCP) and potential future replacement.

## Future Phases

### Phase 2: Web UI
- Dashboard served by the daemon at `http://localhost:7433/ui`
- Connected agents, status, recent messages, task board
- Real-time updates via WebSocket

### Phase 3: Agent capabilities registry
- Agents declare capabilities ("I can run tests", "I can deploy")
- Smart routing: "send to whoever can run Go tests"

### Phase 4: ACL for federation
- Fine-grained access control on peered hubs
- Per-agent or per-hub message/task permissions
