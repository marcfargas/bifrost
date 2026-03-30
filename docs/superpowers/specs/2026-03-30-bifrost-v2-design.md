# Bifrost v2 — A2A-Inspired Communication Hub over MCP

## What It Is

A hosted Python service for cross-machine agent communication. Agents connect via MCP Streamable HTTP, register an Agent Card (A2A-inspired schema), and gain access to task lifecycle, free-form messaging, broadcast channels, and a central agent directory — all through MCP tools.

One service, one source of truth. No shim, no binary to install, no federation protocol.

```
Claude Code (project A) ──MCP/HTTP──┐
                                     │
Claude Code (project B) ──MCP/HTTP──├──> bifrost.example.com
                                     │
Any MCP client ─────────MCP/HTTP──┘
```

## When to Use Bifrost (and When Not To)

Bifrost is a **communication layer**, not an orchestration platform. It does not start, stop, or manage agent processes.

**Use bifrost when:**
- Agents run on different machines (nexus, marcwin, CI runners)
- You need visibility into what agents across machines are doing (dashboard)
- You need persistent message history and task tracking across sessions
- You want a central agent directory with capability discovery

**Use native Agent Teams / file-based primitives when:**
- All agents are on one machine in one Claude Code session
- Agent Teams' built-in task delegation and auto-unblock are sufficient
- You don't need cross-session persistence or a web dashboard

**Agent lifecycle is external.** Bifrost does not start or stop agents. That's tmux, systemd, Docker, or whatever manages your processes. Bifrost tracks liveness (online/offline/last_seen) based on MCP connections, but does not control it.

## Design Principles

- **A2A-inspired data model, MCP transport.** Adopt A2A schemas (Agent Cards, task states, artifacts, parts) as the data model. Deliver via MCP tools. Not A2A-compatible on the wire — no `.well-known`, no JSON-RPC A2A endpoints. That's a future layer.
- **Hub, not peer-to-peer.** Bifrost is the central registry and router.
- **Bifrost stores data, agents decide semantics.** No workflow enforcement. No dependency graphs. No topological sorting. Agents coordinate however they want.
- **OAuth or nothing.** Production uses OAuth 2.0. Local dev uses `--insecure` (no auth). No middle ground.

## Pre-Implementation Spikes

These must be validated before any implementation work begins:

### Spike 1: MCP SDK + FastAPI + Channel Push

**Question:** Can the Python MCP SDK serve as a Streamable HTTP server with SSE push, coexisting with FastAPI routes (dashboard, OAuth)?

**Test:** Build a minimal FastAPI app that:
1. Serves one MCP tool (`echo`) via Streamable HTTP at `/mcp`
2. Pushes a `notifications/claude/channel` event via SSE
3. Serves one Jinja2 page at `/dashboard`
4. All in one uvicorn process

**Pass criteria:** Claude Code connects via `type: url`, calls the tool, receives the push notification as a `<channel>` tag.

**Fail plan:** If the MCP SDK can't coexist with FastAPI, evaluate: (a) separate processes behind a reverse proxy, (b) MCP SDK as the sole HTTP server with dashboard routes mounted on it, (c) different framework.

### Spike 2: OAuth for CLI MCP Clients

**Question:** Does Claude Code's `type: url` MCP client handle OAuth redirect flows from a terminal? Can an agent on a headless server (nexus via SSH) complete an OAuth flow?

**Test:** Configure the spike from Spike 1 with OAuth protection. Connect from:
1. Claude Code on a desktop (has browser access)
2. Claude Code on nexus via SSH (headless)

**Pass criteria:** Both can authenticate and call the MCP tool.

**Fail plan:** If headless OAuth doesn't work: (a) device code flow (OAuth 2.0 Device Authorization Grant — user authorizes on a different machine), (b) one-time token provisioning via the dashboard, (c) accept that `--insecure` is the only headless option for now.

## What Comes From A2A (Schema Only)

The data model borrows from A2A v0.3.0. This is **not** A2A wire compatibility — there are no A2A JSON-RPC endpoints. Bifrost may add A2A HTTP endpoints in the future, at which point it would become a proper A2A-to-MCP bridge.

| Concept | Details |
|---------|---------|
| **Agent Cards** | name, description, version, icon_url, provider, capabilities, skills[], default_input/output_modes |
| **AgentSkill** | id, name, description, tags, examples, input/output modes |
| **Task states** | queued, running, input-required, auth-required, completed, failed, canceled, rejected |
| **Task.context_id** | Groups related tasks into a session/thread |
| **Task.status_message** | A Message (role + parts) explaining current status |
| **Artifacts** | id, parts[], metadata — structured task outputs |
| **Parts** (content model) | TextPart, FilePart, DataPart — typed content fragments |
| **Auth model** | OAuth 2.0 with OpenAPI-style security scheme declarations |

## What Bifrost Adds

| Concept | Why A2A doesn't have it |
|---------|------------------------|
| **Agent registry** | A2A expects self-hosted `.well-known/agent.json`. Bifrost is the central directory — agents submit their card via MCP on connect. |
| **Agent status tracking** | online, idle, offline, dnd, last_seen, connected_at. DND is a status with optional reason. |
| **Conversations** | Free-form messaging between agents, independent of tasks |
| **Channels** | Pub/sub broadcast via conversation prefixes |
| **Delivery tracking** | Per-agent push/poll delivery state |
| **Observability dashboard** | Jinja2+htmx web UI |

## Data Model

### Agents & Agent Cards

```sql
agents
  id              TEXT PK
  name            TEXT UNIQUE       -- human-readable identifier
  status          TEXT              -- online, idle, offline, dnd
  dnd_reason      TEXT
  connected_at    TIMESTAMP
  last_seen       TIMESTAMP
  oauth_subject   TEXT              -- from OAuth token (null in --insecure mode)

agent_cards                          -- A2A-inspired AgentCard schema
  agent_id        TEXT PK FK
  description     TEXT               -- what the agent does
  version         TEXT
  icon_url        TEXT
  provider        JSON               -- {organization, url}
  capabilities    JSON               -- {streaming, pushNotifications, ...}
  skills          JSON               -- AgentSkill[] (id, name, description, tags, examples)
  default_input_modes   JSON         -- MIME types accepted
  default_output_modes  JSON         -- MIME types produced
  metadata        JSON
  updated_at      TIMESTAMP
```

On every connect, bifrost sends the agent its current card (if any) via a channel notification (or MCP instruction for non-channel clients) and asks them to confirm or update via `bifrost_introduce`. First-time agents get a prompt to introduce themselves. The agent is online immediately — the intro is advisory, not a gate. Agents will often ignore it, and that's fine — the card persists across sessions.

### Tasks

```sql
tasks
  id              TEXT PK
  context_id      TEXT              -- groups related tasks (A2A concept)
  requester       TEXT FK
  assignee        TEXT FK
  status          TEXT              -- queued, running, input-required,
                                   --   auth-required, completed, failed,
                                   --   canceled, rejected
  status_message  JSON              -- A2A Message (role + parts)
  artifacts       JSON              -- A2A Artifact[] (id, parts[], metadata)
  metadata        JSON
  created_at      TIMESTAMP
  updated_at      TIMESTAMP
```

**Crash recovery:** When an agent disconnects (MCP connection drops), bifrost marks it offline. Tasks in `running` or `input-required` assigned to that agent are **not** automatically transitioned — bifrost doesn't know if the agent will reconnect. Instead:
- The requester is notified that the assignee went offline.
- The requester can cancel the task (`canceled`) and re-request it to another agent.
- The dashboard shows stale tasks (running + assignee offline) prominently.
- A future enhancement could add configurable timeouts, but that's orchestration logic — out of scope for v2.0.

### Conversations & Events

Conversations have an optional channel prefix for pub/sub topics:
- `deploys:abc123` — conversation in the "deploys" channel
- `abc123` — direct conversation (no channel)

Agents subscribe to a prefix (all conversations in a channel) or a specific conversation ID.

```sql
conversations
  id              TEXT PK           -- full ID including optional channel prefix
  channel         TEXT              -- extracted prefix, nullable (for indexing)
  participants    JSON
  title           TEXT
  created_at      TIMESTAMP
  closed          BOOLEAN
  closed_reason   TEXT

events
  id              TEXT PK
  conversation_id TEXT FK
  type            TEXT              -- message, metadata, file, participant.added,
                                   --   participant.removed
  from_agent      TEXT FK
  data            JSON              -- uses A2A Part types for content
  timestamp       TIMESTAMP
```

### Subscriptions & Delivery

```sql
subscriptions
  agent_id        TEXT FK
  target          TEXT              -- channel prefix ("deploys") or conversation ID
  PRIMARY KEY (agent_id, target)

delivery_state
  agent_id        TEXT FK
  conversation_id TEXT FK
  last_event_id   TEXT
  PRIMARY KEY (agent_id, conversation_id)
```

## MCP Tools

| Tool | Purpose |
|------|---------|
| `bifrost_introduce` | Submit/update your Agent Card. Accepts structured fields (description, skills, capabilities, etc.) and/or a freeform `introduction` text. Freeform text is stored as-is in the card's description — no server-side parsing or LLM extraction. Structured fields always take precedence. |
| `bifrost_whoami` | No params: returns your identity, card, and status. With params: updates your status (dnd + reason, or future statuses). |
| `bifrost_list_agents` | Agent directory — all agents with cards and status. |
| `bifrost_request_task` | Create a task assigned to another agent. |
| `bifrost_update_task` | Update task status and/or artifacts. |
| `bifrost_get_task` | Get full task details. |
| `bifrost_list_tasks` | List tasks, filterable by status/requester/assignee. |
| `bifrost_send` | Send a message to an agent or channel. |
| `bifrost_list_conversations` | List conversations, filterable by channel prefix. |
| `bifrost_subscribe` | Subscribe to a channel prefix or specific conversation. |
| `bifrost_check` | Check for pending messages and task updates. Returns undelivered events since last check. Primary mechanism for non-push clients. |

11 tools total.

## Push & Polling

Two delivery mechanisms, depending on client capability:

### Push (Claude Code with channels)

For Claude Code clients that support channel push (`--dangerously-load-development-channels` or published plugin):
- Server declares `claude/channel` capability in MCP initialize response
- Pushes `notifications/claude/channel` events via SSE
- Notifications arrive as `<channel>` tags in the agent's conversation

**Limitation:** Requires Claude Code with `--dangerously-load-development-channels` flag and claude.ai login (not API key). Fleet/autonomous agents may not have this. Channel push is a nice-to-have, not the primary delivery mechanism.

### Poll (`bifrost_check`)

The reliable, universal path. Works with every MCP client:
- `bifrost_check` returns all undelivered events since the agent's last check
- Server advances `delivery_state` after each check
- MCP server instructions tell the agent to call `bifrost_check` periodically:

```
You are connected to bifrost, an agent communication hub. Other agents may
send you messages or task requests. Call bifrost_check regularly (after
completing work, or every few minutes) to see if anything is waiting for you.
```

This is the **primary delivery mechanism**. Channel push is an optimization on top.

## Authentication

Two modes:

**Production (default):** OAuth 2.0. Google OAuth provider via Traefik/Cloudflare on the deployment infrastructure. Token maps to user identity, user identity maps to agent. Spike 2 must validate that this works for headless/SSH agents before implementation.

**Local dev (`--insecure` flag):** No authentication. Any MCP client connects freely. Agent identity is self-declared. Server logs a warning on startup:
```
⚠ Running in insecure mode — no authentication. Do not expose to the network.
```

## Dashboard

Jinja2+htmx, served by the same FastAPI app at the same domain. OAuth-protected (same Google auth as MCP).

Views:
- **Agent directory** — cards, status, last seen, skills
- **Task board** — status, assignee, requester, artifacts. Stale tasks (running + assignee offline) highlighted.
- **Activity feed** — recent events across conversations
- **Conversation viewer** — message history per conversation

Read-heavy view layer. Same database as the MCP server.

## Tech Stack

- Python 3.12+
- FastAPI + uvicorn (async, SSE, serves both MCP and dashboard)
- MCP Python SDK (`mcp` package)
- SQLite (single instance) or Postgres (multi-instance)
- OAuth 2.0 via authlib
- Jinja2 + htmx (dashboard templates)
- Docker deploy

## What We Drop From v1

- Go binary / shim process
- Local hub daemon and auto-start
- Unix socket / named pipe transport
- libp2p, DHT, NAT hole punching, magic codes
- Federation protocol
- delivery_state sync between hubs
- SQLite on every client machine

## Open Questions

1. SQLite vs Postgres for initial deploy — SQLite is simpler, Postgres scales. Single-tenant likely means SQLite is fine.
2. File attachments: inline in event data (with size limit) or object storage? Defer to implementation.
3. Dashboard: same port as MCP or separate? Same FastAPI app suggests same port with path routing.
