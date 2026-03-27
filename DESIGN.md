# Bifrost — Cross-Agent Communication Hub

> **This is an early draft. The authoritative design spec is at [`docs/superpowers/specs/2026-03-27-bifrost-design.md`](docs/superpowers/specs/2026-03-27-bifrost-design.md).**

A lightweight Go daemon + MCP server enabling Claude Code agents (and other MCP clients) running in separate projects to discover each other, exchange messages, and delegate tasks.

## Principles

- **Zero friction locally** — first agent auto-starts the daemon, no setup needed
- **Remote-capable** — daemon can run on a server, agents connect via URL + auth
- **Lightweight** — single Go binary, ~10MB RSS per process
- **Tested thoroughly** — integration tests using `claude -p` for real MCP validation
- **Platform-agnostic** — Windows, Linux, macOS

## Three Modes of Operation

### Mode 1: Embedded daemon (zero friction, local only)

The first shim to connect detects no running daemon and auto-starts one.
Subsequent shims discover the running daemon and connect to it.

```
Claude Code ──stdio──> bifrost shim ──> (no daemon?) ──> auto-start daemon
                                    ──> connect via named pipe / unix socket
```

**Discovery mechanism:**
- Unix: unix domain socket at `$XDG_RUNTIME_DIR/bifrost/hub.sock` (fallback: `/tmp/bifrost-$UID/hub.sock`)
- Windows: named pipe `\\.\pipe\bifrost-hub`
- Lock file with PID at `~/.bifrost/hub.pid` for stale detection

**Lifecycle:**
- First shim starts daemon as a background process
- Daemon stays alive while any shim is connected (+ grace period, e.g., 30s)
- Daemon auto-exits when all shims disconnect and grace period expires
- `bifrost hub stop` for manual shutdown

### Mode 2: Standalone daemon (local or remote)

User explicitly starts the daemon. Shims connect via TCP.

```bash
# Local
bifrost hub start --listen localhost:7432

# Remote (with auth)
bifrost hub start --listen 0.0.0.0:7432 --auth-mode token
```

Shims connect via:
```bash
bifrost shim --hub localhost:7432
bifrost shim --hub https://remote.host:7432 --token <token>
```

### Mode 3: Daemon as MCP server directly (no shim)

The daemon itself speaks MCP over Streamable HTTP. Claude Code connects directly — no shim process needed.

```json
{
  "mcpServers": {
    "bifrost": {
      "type": "url",
      "url": "http://localhost:7432/mcp"
    }
  }
}
```

This is the simplest mode for remote setups: just point Claude Code at the daemon URL.

**Trade-off:** In this mode the daemon must handle MCP session management per connected agent. The shim mode offloads that to each shim process.

## Agent Identity

Each agent registers with:

| Field | Source | Example |
|-------|--------|---------|
| `project_name` | Directory name or `package.json`/`go.mod` name | `api-backend` |
| `username` | OS current user | `marc` |
| `hostname` | OS hostname | `marcbook` |
| `local_path` | Working directory | `/home/marc/dev/api` |
| `capabilities` | Auto-detected from project structure | `["typescript", "react", "vite"]` |
| `agent_id` | Hash of hostname + local_path (stable across sessions) | `a1b2c3d4` |
| `display_name` | Optional human-friendly name | `frontend` |

Agents are uniquely identified by `agent_id`. If an agent reconnects with the same `agent_id`, it resumes its previous identity (pending messages preserved).

## Authentication

### Local (Mode 1 & 2 on localhost)

No auth required. Connection via unix socket / named pipe or localhost TCP implies trust (same user context).

### Remote (Mode 2 & 3 over network)

**Pairing flow** (inspired by Bluetooth pairing):

1. Daemon generates a one-time pairing code: `bifrost hub pair` → `ABCD-1234`
2. Shim presents the code: `bifrost shim --hub remote:7432 --pair ABCD-1234`
3. Daemon validates, issues a persistent token stored in `~/.bifrost/tokens.json`
4. Subsequent connections use the token automatically

**Token storage:**
- Daemon side: `~/.bifrost/authorized_agents.json` — list of paired agent tokens
- Client side: `~/.bifrost/tokens.json` — `{ "remote:7432": "token-value" }`

## Protocol

### Transport layers

| Mode | Transport | Format |
|------|-----------|--------|
| Embedded | Unix socket / named pipe | JSON-RPC 2.0 |
| Standalone | TCP / TLS | JSON-RPC 2.0 |
| Direct MCP | HTTP + SSE | MCP over Streamable HTTP |

All modes use JSON-RPC 2.0 internally between shim and hub. Mode 3 wraps this in MCP's Streamable HTTP transport.

### Hub API (JSON-RPC methods)

```
hub.register        — register/update agent identity
hub.deregister      — clean disconnect
hub.heartbeat       — keep-alive with status update
hub.list_agents     — list connected agents

msg.send            — send typed message to agent(s)
msg.list            — fetch messages (with filters)
msg.ack             — acknowledge message receipt

task.create         — create task, optionally assign to agent
task.accept         — accept assigned task
task.update         — update task status/progress
task.list           — list tasks (with filters)

channel.subscribe   — join broadcast channel
channel.publish     — publish to channel
channel.list        — list available channels
```

### Push notifications

Hub pushes events to connected shims via:
- **Unix socket / named pipe:** JSON-RPC notifications on the persistent connection
- **TCP:** same, persistent connection with notifications
- **HTTP (Mode 3):** SSE stream at `/mcp` (per MCP Streamable HTTP spec)

Events pushed:
- `notification.message` — new message received
- `notification.task` — task assigned/updated
- `notification.agent_joined` — new agent connected
- `notification.agent_left` — agent disconnected
- `notification.channel` — broadcast message on subscribed channel

## MCP Tools (exposed to Claude Code)

```
bifrost_register         — register this agent (auto-called on startup)
bifrost_list_agents      — list all connected agents
bifrost_send_message     — send message to agent by name/id
bifrost_get_messages     — get unread messages
bifrost_ack_message      — mark message as read
bifrost_create_task      — create and assign a task
bifrost_accept_task      — accept a task assigned to you
bifrost_update_task      — update task status
bifrost_list_tasks       — list tasks
bifrost_subscribe        — join a broadcast channel
bifrost_publish          — publish to a broadcast channel
```

## MCP Resources

```
bifrost://agents                 — JSON list of connected agents
bifrost://messages               — unread messages for this agent
bifrost://tasks                  — tasks assigned to this agent
bifrost://channels               — available channels
```

## Message Types

| Type | Use case |
|------|----------|
| `QUESTION` | Ask another agent for information |
| `ANSWER` | Response to a question |
| `TASK` | Delegate work |
| `CONTEXT` | Share background info proactively |
| `STATUS` | Progress update |
| `ERROR` | Report a problem |

Messages include: `id`, `from`, `to` (agent_id or `"*"` for broadcast), `type`, `subject`, `body`, `in_reply_to` (threading), `priority` (low/normal/urgent), `timestamp`, `acknowledged`.

## Persistence

**SQLite** via `modernc.org/sqlite` (pure Go, no CGO needed — critical for easy cross-compilation).

Tables:
- `agents` — registered agents and last-seen
- `messages` — all messages with delivery status
- `tasks` — tasks with lifecycle state
- `channels` — channel definitions
- `channel_subscriptions` — agent-channel memberships
- `tokens` — authorized remote agent tokens

DB location: `~/.bifrost/hub.db`

## Project Structure

```
github.com/marcfargas/bifrost/
├── cmd/
│   └── bifrost/
│       └── main.go           — CLI entry (hub/shim subcommands)
├── internal/
│   ├── hub/
│   │   ├── hub.go            — hub server orchestration
│   │   ├── agents.go         — agent registry
│   │   ├── messages.go       — message routing + push
│   │   ├── tasks.go          — task lifecycle
│   │   ├── channels.go       — pub/sub
│   │   ├── auth.go           — pairing + token validation
│   │   ├── transport.go      — listener setup (socket/pipe/tcp)
│   │   └── mcp.go            — MCP Streamable HTTP handler (mode 3)
│   ├── shim/
│   │   ├── shim.go           — MCP stdio ↔ hub bridge
│   │   ├── tools.go          — MCP tool handlers
│   │   ├── resources.go      — MCP resource handlers
│   │   ├── discovery.go      — find/start daemon
│   │   └── autostart.go      — embedded daemon lifecycle
│   ├── store/
│   │   ├── store.go          — storage interface
│   │   └── sqlite.go         — SQLite implementation
│   ├── detect/
│   │   └── project.go        — project capability detection
│   └── protocol/
│       ├── types.go          — shared types (Agent, Message, Task, etc.)
│       └── rpc.go            — JSON-RPC 2.0 helpers
├── test/
│   ├── integration/
│   │   ├── hub_test.go       — hub unit/integration tests
│   │   ├── shim_test.go      — shim ↔ hub integration
│   │   └── mcp_test.go       — full MCP flow via claude -p
│   └── testutil/
│       └── helpers.go        — test helpers, fixtures
├── go.mod
├── go.sum
├── LICENSE                    — LGPL-3.0
├── DESIGN.md                  — this file
└── README.md
```

## CLI

```
bifrost hub start [--listen addr] [--auth-mode token|none]
bifrost hub stop
bifrost hub status
bifrost hub pair                    — generate pairing code

bifrost shim [--hub addr] [--token tok] [--project-name name]
                                    — start MCP stdio server (Claude Code spawns this)

bifrost agents                      — list connected agents (CLI query)
bifrost messages [--agent id]       — list recent messages (CLI query)
bifrost send <agent> <message>      — send message from CLI (debugging)
```

## Testing Strategy

### Unit tests
- Store layer: CRUD operations, queries, concurrent access
- Message routing: delivery, broadcast, threading, ack
- Task lifecycle: state transitions, validation
- Auth: pairing flow, token validation/rejection
- Discovery: socket/pipe detection, PID staleness

### Integration tests
- Hub ↔ shim: register, send/receive messages, task delegation
- Multi-shim: two shims exchanging messages through hub
- Auto-start: first shim starts daemon, second finds it
- Reconnection: agent disconnects, messages queued, reconnects and receives them

### MCP end-to-end tests
Using `claude -p` (Claude Code in pipe/non-interactive mode):
- Spawn two Claude Code instances with bifrost configured
- Agent A asks agent B a question via bifrost
- Validate B receives and responds
- Validate A gets the response

### Platform tests
- CI matrix: Windows, Linux, macOS
- Named pipe (Windows) vs unix socket (*nix)

## Future Phases

### Phase 2: Web UI
- Simple web dashboard served by the daemon
- Lists connected agents, their status, recent messages
- Real-time updates via WebSocket
- Served at `http://localhost:7432/ui`

### Phase 3: Agent capabilities registry
- Agents declare what they can do ("I can run tests", "I can deploy")
- Other agents can query capabilities before delegating
- Enables smart routing: "send this to whoever can run Go tests"

### Phase 4: Message history + search
- Full conversation history between agents
- Search by content, type, agent, time range
- Useful for debugging multi-agent workflows
```
