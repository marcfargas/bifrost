# External Integrations

**Analysis Date:** 2026-03-30

## APIs & External Services

**MCP Protocol (provided):**
- Bifrost IS an MCP server exposing 11 tools via Streamable HTTP transport
- Endpoint: `/mcp` (Streamable HTTP)
- Health check: `/health` (GET, returns `{"status": "ok"}`)
- OAuth endpoints (when auth enabled): `/.well-known/*`, `/authorize`, `/token`, `/register`, `/revoke`, `/callback`
- Implementation: `src/bifrost/app.py` (app factory and tool registration)

**OIDC Provider (consumed):**
- External OpenID Connect provider (Dex in production deployment)
- Used for user authentication, proxied through Bifrost's own OAuth AS
- SDK/Client: `httpx.AsyncClient` for token exchange (`src/bifrost/auth/oauth.py`)
- Auth config: `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` env vars
- Endpoints consumed: `{issuer}/auth` (authorization), `{issuer}/token` (token exchange)
- Scopes requested: `openid email profile`

**No other external APIs are consumed.** Bifrost is a self-contained hub -- agents connect to it, not the other way around.

## Data Storage

**Database:**
- SQLite 3 (stdlib `sqlite3` module)
- Connection: configured via `--db` CLI flag, defaults to `data/bifrost.db`
- Client: raw `sqlite3` with `Row` factory, no ORM
- Implementation: `src/bifrost/store/db.py` (synchronous, `check_same_thread=False`)
- WAL mode enabled (`PRAGMA journal_mode=WAL`)
- Foreign keys enabled (`PRAGMA foreign_keys=ON`)
- Schema auto-created on first connection (inline DDL in `db.py`)

**Tables:**
- `agents` - Agent registry
- `agent_cards` - Agent capability metadata (A2A agent cards)
- `tasks` - Task lifecycle tracking
- `conversations` - Messaging conversations
- `events` - Conversation events/messages (indexed on `conversation_id, timestamp`)
- `delivery_state` - Per-agent read cursors for message delivery
- `subscriptions` - Channel/task subscriptions
- `oauth_clients` - Registered OAuth clients
- `oauth_access_tokens` - Active access tokens
- `oauth_refresh_tokens` - Active refresh tokens
- `oauth_auth_codes` - Pending authorization codes

**File Storage:**
- None. All data lives in SQLite.

**Caching:**
- None. All reads go directly to SQLite.

## Authentication & Authorization

**Auth Provider:**
- Custom OAuth 2.0 Authorization Server built into Bifrost (`src/bifrost/auth/oauth.py`)
- Proxies authentication to an external OIDC provider (e.g., Dex)
- Implements `OAuthAuthorizationServerProvider` protocol from `mcp.server.auth.provider`
- OAuth clients, tokens, and auth codes are persisted in SQLite
- Only `_pending_auths` (mid-redirect state) is ephemeral/in-memory

**Auth Flow:**
1. MCP client calls `/register` to register as OAuth client (dynamic client registration)
2. Client redirects user to Bifrost `/authorize`
3. Bifrost redirects to external OIDC provider (`{issuer}/auth`)
4. OIDC callback returns to `/callback`, Bifrost exchanges code with OIDC provider
5. Bifrost issues its own authorization code, redirects to MCP client
6. Client exchanges code for access/refresh tokens at `/token`
7. Access tokens validated on every MCP request via `mcp.server.auth.middleware`

**Insecure Mode:**
- `--insecure` flag disables all auth (`src/bifrost/__main__.py`)
- All sessions share a single "insecure" session key (`src/bifrost/app.py` `_get_session_key`)
- Only suitable for local development

**Session Management:**
- Session-to-agent mapping stored in-memory dict `session_agents` (`src/bifrost/app.py`)
- Key derived from OAuth access token (unique per session) or "insecure" constant
- Auto-registration of unnamed agent on first authenticated MCP request (hooks `list_tools`)

## Protocols & Standards

**MCP (Model Context Protocol):**
- Server implementation via FastMCP
- Transport: Streamable HTTP on `/mcp`
- Stateful sessions (`stateless_http=False`)
- 11 registered tools: `bifrost_introduce`, `bifrost_whoami`, `bifrost_list_agents`, `bifrost_request_task`, `bifrost_update_task`, `bifrost_get_task`, `bifrost_list_tasks`, `bifrost_send`, `bifrost_list_conversations`, `bifrost_subscribe`, `bifrost_check`

**A2A (Agent-to-Agent, inspiration):**
- Data models inspired by Google's A2A protocol
- Agent cards with skills, capabilities, input/output modes (`src/bifrost/store/models.py`)
- Task lifecycle: queued -> running -> completed/failed/canceled/rejected
- Content model: TextPart, FilePart, DataPart composing Artifacts

**OAuth 2.0:**
- Full OAuth 2.0 AS implementation with PKCE (code_challenge)
- Dynamic client registration (RFC 7591)
- Token revocation (RFC 7009)
- Bearer token authentication

**OIDC (OpenID Connect):**
- Consumed (not provided) -- Bifrost delegates user authentication to external OIDC
- Standard authorization code flow with external provider

## Monitoring & Observability

**Error Tracking:**
- None. Errors logged to stdout via Python `logging` module.

**Logs:**
- Standard Python `logging` (`logging.getLogger("bifrost")`)
- No structured logging framework
- Warning-level logging for auto-registration failures (`src/bifrost/app.py`)

**Health Check:**
- `GET /health` returns `{"status": "ok"}` -- suitable for Docker/Traefik health probes

## CI/CD & Deployment

**Hosting:**
- Docker on Hetzner remote dev server (`nexus.blegal.dev`)
- Reverse proxy: Traefik with Cloudflare tunnel
- Public URL: `bifrost.blegal.dev` (configured in `docker-compose.dev.yml`)

**CI Pipeline:**
- Not detected (no `.github/workflows/`, no CI config files)

## Webhooks & Callbacks

**Incoming:**
- `/callback` - OAuth callback from external OIDC provider (receives auth code + state)

**Outgoing:**
- None. Bifrost does not push to external webhooks.

---

*Integration audit: 2026-03-30*
