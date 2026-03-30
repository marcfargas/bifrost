# Bifrost v2 — Pre-Implementation Spikes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Validate that the Python MCP SDK can serve Streamable HTTP with channel push alongside FastAPI, and that OAuth works for CLI MCP clients (including headless).

**Architecture:** Two isolated proof-of-concepts. Each produces a minimal runnable server that proves one critical assumption. Results inform the core server plan.

**Tech Stack:** Python 3.12+, `mcp` SDK, FastAPI, uvicorn, authlib, Jinja2

**Spec:** `docs/superpowers/specs/2026-03-30-bifrost-v2-design.md`

---

### Task 1: Project Bootstrap

**Files:**
- Create: `pyproject.toml`
- Create: `src/bifrost/__init__.py`

- [ ] **Step 1: Create pyproject.toml**

```toml
[project]
name = "bifrost"
version = "0.1.0"
description = "A2A-inspired communication hub over MCP"
requires-python = ">=3.12"
license = "LGPL-3.0-or-later"
dependencies = [
    "mcp>=1.9.0",
    "fastapi>=0.115.0",
    "uvicorn[standard]>=0.34.0",
    "jinja2>=3.1.0",
    "authlib>=1.4.0",
    "httpx>=0.28.0",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.0",
    "pytest-asyncio>=0.25.0",
    "httpx>=0.28.0",
]

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.hatch.build.targets.wheel]
packages = ["src/bifrost"]

[tool.pytest.ini_options]
asyncio_mode = "auto"
testpaths = ["tests"]
```

- [ ] **Step 2: Create package init**

```python
# src/bifrost/__init__.py
```

Empty file.

- [ ] **Step 3: Install in dev mode**

Run: `cd /home/marc/dev/bifrost && pip install -e ".[dev]"`
Expected: Installs successfully, `python -c "import bifrost"` works.

- [ ] **Step 4: Commit**

```bash
git add pyproject.toml src/bifrost/__init__.py
git commit -m "chore: bootstrap Python project for bifrost v2"
```

---

### Task 2: Spike 1 — MCP SDK + FastAPI + Channel Push

**Files:**
- Create: `src/bifrost/spike_mcp.py`
- Create: `tests/test_spike_mcp.py`

This spike answers: Can the Python MCP SDK serve Streamable HTTP with SSE push, coexisting with FastAPI routes, in one uvicorn process?

- [ ] **Step 1: Write the spike server**

```python
# src/bifrost/spike_mcp.py
"""
Spike: MCP Streamable HTTP + FastAPI coexistence + channel push.

Run: uvicorn bifrost.spike_mcp:app --host 0.0.0.0 --port 8000
Test: Connect Claude Code with type:url pointing to http://localhost:8000/mcp
"""
import contextlib
import json

from fastapi import FastAPI
from fastapi.responses import HTMLResponse
from mcp.server import Server
from mcp.types import (
    CallToolRequestParams,
    CallToolResult,
    ListToolsResult,
    PaginatedRequestParams,
    TextContent,
    Tool,
)
from starlette.routing import Mount


# --- MCP Server (low-level for experimental capabilities) ---

mcp_server = Server("bifrost-spike")


@mcp_server.list_tools()
async def list_tools() -> list[Tool]:
    return [
        Tool(
            name="echo",
            description="Echo a message back",
            inputSchema={
                "type": "object",
                "required": ["message"],
                "properties": {
                    "message": {"type": "string", "description": "Message to echo"},
                },
            },
        ),
        Tool(
            name="ping_channel",
            description="Send a test channel notification to yourself",
            inputSchema={
                "type": "object",
                "properties": {},
            },
        ),
    ]


@mcp_server.call_tool()
async def call_tool(name: str, arguments: dict) -> list[TextContent]:
    if name == "echo":
        return [TextContent(type="text", text=f"Echo: {arguments['message']}")]

    if name == "ping_channel":
        # Get the current session from the request context
        ctx = mcp_server.request_context
        session = ctx.session

        # Send a channel notification via SSE
        await session.send_notification(
            method="notifications/claude/channel",
            params={
                "content": "Channel push works!",
                "meta": {"from": "bifrost-spike", "type": "STATUS"},
            },
        )
        return [TextContent(type="text", text="Channel notification sent.")]

    return [TextContent(type="text", text=f"Unknown tool: {name}", is_error=True)]


# --- FastAPI app ---

@contextlib.asynccontextmanager
async def lifespan(app: FastAPI):
    async with contextlib.AsyncExitStack() as stack:
        await stack.enter_async_context(mcp_server.session_manager.run())
        yield


app = FastAPI(lifespan=lifespan)


@app.get("/dashboard", response_class=HTMLResponse)
async def dashboard():
    return "<html><body><h1>Bifrost Spike Dashboard</h1><p>It works.</p></body></html>"


# Mount MCP at /mcp
app.mount(
    "/mcp",
    mcp_server.streamable_http_app(
        streamable_http_path="/",
    ),
)
```

- [ ] **Step 2: Check channel capability declaration**

The low-level `Server` class needs to declare `claude/channel` in experimental capabilities. Check if `Server.create_initialization_options()` or the server constructor accepts `experimental_capabilities`. If not, we need to find the right hook.

Search the MCP SDK source:
```bash
python -c "import mcp.server; print(mcp.server.__file__)"
```
Then grep for `experimental` in that directory.

If the SDK supports it, add to `spike_mcp.py`:
```python
# After creating mcp_server, configure experimental capabilities
# The exact API depends on what we find in the SDK source
```

If the SDK does NOT support it, document the gap and note that channel push may require patching or a PR.

- [ ] **Step 3: Start the spike server**

Run: `cd /home/marc/dev/bifrost && uvicorn bifrost.spike_mcp:app --host 0.0.0.0 --port 8000`

Expected: Server starts, logs show FastAPI + MCP routes.

- [ ] **Step 4: Test dashboard route**

Run: `curl http://localhost:8000/dashboard`
Expected: HTML response with "Bifrost Spike Dashboard".

- [ ] **Step 5: Test MCP connection from Claude Code**

Add to Claude Code MCP config (or use `claude mcp add`):
```json
{
  "mcpServers": {
    "bifrost-spike": {
      "type": "url",
      "url": "http://localhost:8000/mcp"
    }
  }
}
```

Start a Claude Code session. Call the `echo` tool. Verify it returns "Echo: <message>".

- [ ] **Step 6: Test channel push**

In the same Claude Code session, call the `ping_channel` tool. Check if a `<channel>` notification appears.

**If it works:** Channel push is viable. Document the exact API calls that worked.
**If it doesn't:** Document why (missing capability declaration, SSE stream not persistent, etc.). Note that `bifrost_check` (polling) is the primary delivery mechanism anyway — channel push is an optimization.

- [ ] **Step 7: Write test for MCP tool registration**

```python
# tests/test_spike_mcp.py
"""Tests for MCP spike — verifies tool registration and FastAPI coexistence."""
import pytest
from httpx import ASGITransport, AsyncClient

from bifrost.spike_mcp import app


@pytest.fixture
async def client():
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as c:
        yield c


async def test_dashboard_returns_html(client: AsyncClient):
    response = await client.get("/dashboard")
    assert response.status_code == 200
    assert "Bifrost Spike Dashboard" in response.text


async def test_mcp_endpoint_exists(client: AsyncClient):
    # MCP Streamable HTTP expects POST to /mcp
    # Without proper MCP handshake this should return an error, not 404
    response = await client.post("/mcp", json={"jsonrpc": "2.0", "method": "initialize", "id": 1, "params": {}})
    assert response.status_code != 404
```

- [ ] **Step 8: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_spike_mcp.py -v`
Expected: Both tests pass.

- [ ] **Step 9: Commit**

```bash
git add src/bifrost/spike_mcp.py tests/test_spike_mcp.py
git commit -m "spike: MCP SDK + FastAPI + channel push proof-of-concept"
```

- [ ] **Step 10: Document results**

Create a brief note at the top of `spike_mcp.py` documenting:
- What worked
- What didn't work
- Exact SDK API used for channel push (or why it failed)
- Any SDK version constraints discovered

Commit the update.

---

### Task 3: Spike 2 — OAuth for CLI MCP Clients

**Files:**
- Create: `src/bifrost/spike_oauth.py`
- Create: `tests/test_spike_oauth.py`

This spike answers: Can Claude Code's `type: url` MCP client handle OAuth redirect flows from a terminal? What about headless (SSH)?

- [ ] **Step 1: Read deployment infrastructure reference**

Run: `cat /opt/nexus/CLAUDE-DEPLOY.md` (on nexus) or check `docs/` for OAuth/Traefik setup details.

We need to understand the existing Google OAuth + Traefik + Cloudflare tunnel setup to wire the spike into it.

- [ ] **Step 2: Write the OAuth spike server**

```python
# src/bifrost/spike_oauth.py
"""
Spike: OAuth 2.0 for MCP clients.

Tests whether Claude Code can complete an OAuth flow when connecting to a
protected MCP server.

Run: uvicorn bifrost.spike_oauth:app --host 0.0.0.0 --port 8001
"""
import contextlib
import os

from fastapi import FastAPI
from mcp.server import Server
from mcp.types import TextContent, Tool
from starlette.routing import Mount

# --- Configuration ---
# These come from environment variables or .env
OAUTH_ISSUER_URL = os.environ.get("OAUTH_ISSUER_URL", "")
OAUTH_CLIENT_ID = os.environ.get("OAUTH_CLIENT_ID", "")
OAUTH_CLIENT_SECRET = os.environ.get("OAUTH_CLIENT_SECRET", "")


# --- MCP Server with auth ---

mcp_server = Server("bifrost-oauth-spike")


@mcp_server.list_tools()
async def list_tools() -> list[Tool]:
    return [
        Tool(
            name="whoami",
            description="Returns the authenticated user identity",
            inputSchema={"type": "object", "properties": {}},
        ),
    ]


@mcp_server.call_tool()
async def call_tool(name: str, arguments: dict) -> list[TextContent]:
    if name == "whoami":
        # Access auth context — exact API depends on SDK's auth middleware
        ctx = mcp_server.request_context
        # Try to extract user identity from the auth context
        # This will depend on how the SDK exposes auth info
        return [TextContent(type="text", text="Auth context: TODO extract user")]
    return [TextContent(type="text", text=f"Unknown tool: {name}")]


# --- App ---

@contextlib.asynccontextmanager
async def lifespan(app: FastAPI):
    async with contextlib.AsyncExitStack() as stack:
        await stack.enter_async_context(mcp_server.session_manager.run())
        yield


app = FastAPI(lifespan=lifespan)


@app.get("/health")
async def health():
    return {"status": "ok"}


# Mount MCP with OAuth protection
# The exact wiring depends on what we learned in Spike 1 about the SDK's auth API.
# See the MCP SDK's auth.settings.AuthSettings and auth.provider.TokenVerifier.
#
# Skeleton:
#   from mcp.server.auth.settings import AuthSettings
#   from mcp.server.auth.provider import TokenVerifier, AccessToken
#
#   class GoogleTokenVerifier(TokenVerifier):
#       async def verify_token(self, token: str) -> AccessToken | None:
#           # Validate token against Google's userinfo endpoint
#           ...
#
#   mcp_app = mcp_server.streamable_http_app(
#       streamable_http_path="/",
#       auth=AuthSettings(issuer_url=..., resource_server_url=..., required_scopes=[...]),
#       token_verifier=GoogleTokenVerifier(),
#   )

app.mount(
    "/mcp",
    mcp_server.streamable_http_app(streamable_http_path="/"),
)
```

- [ ] **Step 3: Research Claude Code's OAuth handling**

Before deploying, check how Claude Code handles OAuth for `type: url` MCP servers:
```bash
claude mcp --help
```

Search Claude Code docs for OAuth, `type: url`, and authentication flow. Key questions:
- Does Claude Code open a browser for OAuth consent?
- Does it support device code flow for headless?
- How does it store/refresh tokens?

- [ ] **Step 4: Test on desktop (browser available)**

Deploy the spike (or run locally), configure Claude Code to connect:
```json
{
  "mcpServers": {
    "bifrost-oauth-spike": {
      "type": "url",
      "url": "https://bifrost-spike.blegal.dev/mcp"
    }
  }
}
```

Attempt to connect. Document what happens:
- Does a browser open?
- Can you complete the OAuth flow?
- Does the `whoami` tool return user identity?

- [ ] **Step 5: Test on headless (SSH to nexus)**

SSH into nexus, start a Claude Code session, attempt to connect to the same OAuth-protected spike.

Document what happens:
- Can the OAuth flow complete without a browser?
- If not, what error does Claude Code show?

- [ ] **Step 6: Document results and fallback assessment**

Update the top of `spike_oauth.py` with findings:
- Desktop OAuth: works / doesn't work
- Headless OAuth: works / doesn't work
- If headless fails, which fallback is viable:
  - Device code flow (OAuth 2.0 Device Authorization Grant)
  - One-time token via dashboard
  - `--insecure` as only headless option

- [ ] **Step 7: Write basic test**

```python
# tests/test_spike_oauth.py
"""Tests for OAuth spike."""
import pytest
from httpx import ASGITransport, AsyncClient

from bifrost.spike_oauth import app


@pytest.fixture
async def client():
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as c:
        yield c


async def test_health_endpoint(client: AsyncClient):
    response = await client.get("/health")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


async def test_mcp_without_auth_returns_unauthorized(client: AsyncClient):
    # Once OAuth is wired, unauthenticated requests should be rejected
    # For now, just verify the endpoint exists
    response = await client.post("/mcp", json={"jsonrpc": "2.0", "method": "initialize", "id": 1, "params": {}})
    assert response.status_code != 404
```

- [ ] **Step 8: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_spike_oauth.py -v`
Expected: Tests pass.

- [ ] **Step 9: Commit**

```bash
git add src/bifrost/spike_oauth.py tests/test_spike_oauth.py
git commit -m "spike: OAuth 2.0 for CLI MCP clients proof-of-concept"
```

---

### Task 4: Spike Cleanup

After both spikes are complete and results documented:

- [ ] **Step 1: Write spike summary**

Create `docs/spike-results.md` with:
- Spike 1 result: pass/fail, SDK API details, workarounds needed
- Spike 2 result: pass/fail, OAuth flow details, headless status
- Go/no-go decision for the core server plan
- Any changes needed to the design spec based on findings

- [ ] **Step 2: Commit**

```bash
git add docs/spike-results.md
git commit -m "docs: spike results — MCP SDK and OAuth validation"
```
