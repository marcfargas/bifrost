# Bifrost v2 — Spike Results

## Spike 1: MCP SDK + FastAPI + Channel Push

**Result: PASS**

The Python MCP SDK (v1.26.0) can serve Streamable HTTP alongside FastAPI in one uvicorn process.

### Key findings

- **`StreamableHTTPSessionManager`** wraps the low-level `Server` and manages HTTP sessions
- **Mount pattern:** ASGI wrapper class delegating to `session_manager.handle_request`, mounted on FastAPI via `app.mount("/mcp", ...)`
- **Experimental capabilities (`claude/channel`):** Monkey-patch `Server.create_initialization_options()` to inject `{"claude/channel": {}}` into the InitializeResult
- **Channel push:** Use `session.send_message(SessionMessage(JSONRPCMessage(JSONRPCNotification(...))))` to send custom `notifications/claude/channel` events. The typed `ServerNotification` union doesn't support arbitrary methods — bypass with raw JSONRPCNotification.
- **SSE delivery:** Push notifications go to the GET SSE stream, not the POST response. Client must have an active GET connection.
- **Testing:** `StreamableHTTPSessionManager.run()` is single-use per instance — use a factory per test. `httpx.ASGITransport` doesn't invoke ASGI lifespan — start session manager manually.

### When using FastMCP (high-level)

Spike 2 switched to `FastMCP` for OAuth support. With `FastMCP`:
- `mcp.streamable_http_app()` returns a full Starlette ASGI app with all routes
- `streamable_http_path="/mcp"` sets the MCP endpoint path
- `mcp.custom_route()` adds additional routes (health, callbacks)
- Auth routes (`/authorize`, `/token`, `/register`, `/.well-known/*`) are auto-generated at root

## Spike 2: OAuth for CLI MCP Clients

**Result: PASS**

Claude Code successfully authenticates against bifrost via OAuth, with Dex as the OIDC backend.

### Architecture

Bifrost acts as **both** the OAuth Authorization Server and Resource Server. Claude Code never talks to Dex directly — bifrost proxies the auth flow.

```
Claude Code
  → POST /mcp → 401 + resource_metadata URL
  → GET /.well-known/oauth-protected-resource/mcp → {authorization_servers: [bifrost]}
  → GET /.well-known/oauth-authorization-server → {authorize, token, register endpoints}
  → POST /register (Dynamic Client Registration RFC 7591) → 201
  → GET /authorize → bifrost 302 → Dex login page
  → User logs in at Dex
  → Dex → GET /callback?code=...&state=... → bifrost exchanges code, issues own auth code
  → 302 → Claude Code's localhost callback
  → POST /token (exchanges auth code for bifrost access token) → 200
  → POST /mcp (with Bearer token) → authenticated MCP session
```

### Key findings

1. **Claude Code expects RFC 8414** (`/.well-known/oauth-authorization-server`) at the AS. Dex only has OIDC discovery (`/.well-known/openid-configuration`). Bifrost must be its own AS.

2. **MCP SDK's `OAuthAuthorizationServerProvider`** is the right interface. Implement it to proxy auth to Dex. The SDK auto-generates all OAuth routes.

3. **Dynamic Client Registration (RFC 7591)** works out of the box. Claude Code registers a public client (PKCE, no secret).

4. **Dex callback:** The SDK does NOT provide a callback route. Add via `FastMCP.custom_route("/callback")`. This handles Dex's redirect, exchanges the Dex code for a Dex token, and issues bifrost's own auth code.

5. **Trailing slash matters:** Claude Code sends `POST /mcp/` if the configured URL has a trailing slash. Starlette 307-redirects to `/mcp`, dropping the auth header. **Configure without trailing slash:** `https://bifrost.blegal.dev/mcp`

6. **Headless SSH:** Not tested. Claude Code opens a browser for OAuth consent. Headless agents would need `--insecure` mode or a PAT mechanism.

7. **Traefik routing:** OAuth routes (`/authorize`, `/token`, `/register`, `/.well-known/*`, `/callback`) must NOT go through Traefik's forward-auth middleware. MCP handles its own auth.

### Dex configuration

- Registered as staticClient: `client_id=bifrost`
- Redirect URI: `https://bifrost.blegal.dev/callback`
- Discovery: `https://auth.blegal.dev/dex/.well-known/openid-configuration`

## Go/No-Go Decision

**GO.** Both spikes pass. Proceed with core server implementation.

### Design spec updates needed

- OAuth section: bifrost is its own AS (proxying to Dex), not just a resource server pointing to an external AS
- MCP URL convention: no trailing slash (`/mcp` not `/mcp/`)
- Consider using `FastMCP` instead of low-level `Server` for the production app — it handles OAuth wiring automatically
