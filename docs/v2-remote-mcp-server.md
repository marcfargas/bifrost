# Bifrost v2 — Remote MCP Server

## Concept

A hosted Python web service that AI agents connect to directly via MCP Streamable HTTP. No shim, no binary to install, no federation protocol. The server IS the hub.

```
Claude Code (project A) ──MCP/HTTP──┐
                                     │
Claude Code (project B) ──MCP/HTTP──├──> bifrost.example.com
                                     │
Claude Desktop ──────────MCP/HTTP──┘
```

## Why

Bifrost v1 (Go, local hub, P2P federation) works but is complex:
- Go binary distribution is painful (no npx/uv)
- Federation (libp2p, DHT, NAT hole punching) adds massive complexity for what is essentially message routing
- Local hub auto-start, reconnection, sync engine — all necessary because there's no central server
- A remote MCP server eliminates all of this: one service, one source of truth

## Architecture

**One Python web service:**
- FastAPI or Starlette
- SQLite or Postgres for persistence
- SSE for push notifications (MCP Streamable HTTP spec)
- OAuth 2.0 for authentication (required for Claude Desktop, good practice for all)

**No shim.** Clients connect directly:
```json
{
  "mcpServers": {
    "bifrost": {
      "type": "url",
      "url": "https://bifrost.example.com/mcp"
    }
  }
}
```

## Auth

OAuth 2.0 — required because:
- Claude Desktop only supports OAuth for remote MCP servers
- Identifies which user/agent is connecting
- Standard, no custom token management
- Can use existing OAuth providers (Google, GitHub) or self-hosted

Flow:
1. User adds server URL to Claude Code/Desktop
2. Browser opens for OAuth consent
3. Token issued, stored by client
4. All MCP requests carry the token
5. Server maps token → user → agent identity

## Data model

Same conversation/event model from v1:
- Conversations (with optional task metadata)
- Events (message, status, metadata, file, participant changes)
- Delivery tracking per agent (which events have been pushed)

The server is the single source of truth. No replication, no sync protocol.

## MCP tools (same as v1)

- `bifrost_send` — send message
- `bifrost_request_task` — request work from another agent
- `bifrost_update_task` — accept/reject/complete
- `bifrost_get_task` — get task status
- `bifrost_list_agents` — who's online
- `bifrost_list_tasks` — my tasks
- `bifrost_subscribe` — join a channel
- `bifrost_dnd` — do not disturb

## Push notifications

MCP Streamable HTTP supports SSE. The server pushes `notifications/claude/channel` events via the SSE stream. Same `<channel>` tags the agent sees today.

## What to carry from v1

- Conversation model (event-sourced, append-only)
- Task lifecycle (request → accept → in_progress → complete/fail/reject)
- MCP tool names and semantics
- Channel notification format (`{content, meta}`)
- DND with delivery tracking
- Agent identity (project name, hostname, capabilities auto-detection)

## What to drop

- Go binary / shim process
- Local hub daemon and auto-start
- Unix socket / named pipe transport
- libp2p, DHT, NAT hole punching, magic codes
- Federation protocol (peer.conversation_sync)
- delivery_state sync between hubs
- SQLite on every machine

## Tech stack

- **Python 3.12+**
- **FastAPI** — async, SSE support, OpenAPI docs for free
- **MCP Python SDK** — `mcp` package
- **SQLite** (single instance) or **Postgres** (multi-instance)
- **OAuth 2.0** — authlib or similar
- **uvicorn** — ASGI server
- **Deploy:** Docker on any VPS, or Cloudflare Workers, or fly.io

## Open questions

- Self-hosted vs managed service?
- OAuth provider: GitHub? Google? Self-hosted (like Authelia)?
- Multi-tenant (one server, many teams) or single-tenant?
- Can the Python MCP SDK serve as a Streamable HTTP server with SSE push?
- Rate limiting / abuse prevention for the hosted scenario
- File attachments: inline (256KB limit) or object storage?
- Can this coexist with v1? (agents connect to either local hub or remote server)

## Estimated effort

Much less than v1. No P2P networking, no binary distribution, no cross-platform socket handling. The core is:
1. MCP server with tools (1-2 days)
2. Conversation/event store (1 day)
3. OAuth integration (1 day)
4. SSE push notifications (1 day)
5. Deploy + CI (half day)

~5 days vs the ~2 days of v1 design + implementation (though v1 took longer due to debugging federation, channel push, etc.)
