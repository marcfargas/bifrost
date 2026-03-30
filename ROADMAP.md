# Bifrost Roadmap

## Done

### Core MCP Server
- Agent registry with A2A-inspired Agent Cards (name, description, skills, limitations)
- Agent introduction flow (agents must introduce themselves on connect)
- Unnamed agent placeholder for authenticated but unintroduced sessions
- Task lifecycle with A2A states (queued, running, input-required, auth-required, completed, failed, canceled, rejected)
- Free-form messaging between agents
- Channel-based pub/sub (conversation prefixes)
- Delivery tracking with `bifrost_check` polling
- 11 MCP tools via FastMCP (Streamable HTTP)
- SQLite persistence (agents, cards, tasks, conversations, events, OAuth state)
- OAuth 2.0 AS proxying to external OIDC provider (tested with Dex)
- `--insecure` mode for local development
- Docker deployment with Traefik integration
- 162 tests

### Spikes Validated
- MCP Python SDK + FastAPI coexistence (channel push via SSE)
- OAuth for CLI MCP clients (including headless via copy/paste redirect URI)

## Next

### Dashboard (Jinja2 + htmx)
Web UI for observability, served by the same FastAPI app:
- Agent directory (cards, status, last seen, skills)
- Task board (status, assignee, requester, stale task highlighting)
- Conversation viewer (message history)
- Activity feed (recent events across all conversations)
- Same OAuth for dashboard access (Traefik forward-auth)

### Known Limitations (accepted for MVP)
- No task ownership enforcement (any agent can update any task)
- Synchronous SQLite (blocks event loop under high concurrency)
- No pagination on list queries
- `session_agents` dict grows without bound (no TTL/cleanup)
- `_new_id()` uses 48-bit IDs (collision risk at scale)
- No OAuth test coverage

## Future

### A2A HTTP Layer
Expose registered agents as proper A2A agents with:
- `/.well-known/agent.json` per agent
- A2A JSON-RPC endpoints
- Bridge between MCP clients and A2A clients

### Channel Push (Claude Code)
Real-time push via `notifications/claude/channel` SSE. Currently validated in spike but not wired into the core delivery system. Requires:
- Claude Code `--dangerously-load-development-channels` or published plugin
- claude.ai login (not API key)

### Production Hardening
- `aiosqlite` or connection pooling for async safety
- Pagination on all list endpoints
- Task ownership enforcement
- Session cleanup / TTL cache
- Agent disconnect detection (mark offline when MCP session drops)
- Rate limiting
