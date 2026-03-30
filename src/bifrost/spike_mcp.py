"""
Spike 1: MCP SDK + FastAPI + Channel Push
==========================================

FINDINGS (updated after implementation):

1. FastAPI + MCP coexistence: WORKS.
   - StreamableHTTPSessionManager is the key class. It wraps the low-level Server
     and manages sessions/transports for Streamable HTTP.
   - Mount pattern: Use a Starlette Mount("/mcp", app=<ASGI wrapper>) on the
     FastAPI router. The Mount strips the prefix so the handler sees "/".
   - CAVEAT: Starlette Mount redirects /mcp -> /mcp/ (307). MCP clients must
     use /mcp/ or follow redirects. In production, we may want to handle this
     with middleware or configure the client. For the spike, tests use /mcp/.
   - The session manager MUST be started via `async with session_manager.run()`
     in the app lifespan. Without this, requests fail with "Task group not initialized".
   - For testing with httpx ASGITransport, the lifespan is NOT invoked automatically.
     Tests must start the session manager manually.

2. Experimental capabilities: WORKS.
   - Server.create_initialization_options(experimental_capabilities={"claude/channel": {}})
     correctly injects into the InitializeResult's capabilities.experimental field.
   - The session manager calls server.create_initialization_options() internally with
     no args when creating sessions. The default experimental_capabilities is {}.
   - SOLUTION: Monkey-patch create_initialization_options on the server instance to
     always inject our experimental capabilities.

3. Channel push (notifications/claude/channel): WORKS with workaround.
   - ServerNotification is a typed union of predefined notification types. It does NOT
     support arbitrary methods like "notifications/claude/channel".
   - WORKAROUND: Use session.send_message() to send a raw JSONRPCNotification with
     any method string. This bypasses the typed notification system but writes directly
     to the transport's write stream.
   - IMPORTANT: Notifications sent via send_message() during a tool call go to the
     GET SSE stream, NOT the POST response. The POST response only contains the tool
     result. This means the client MUST have an active GET /mcp/ SSE connection open
     to receive push notifications. This is the expected MCP Streamable HTTP behavior.
   - During a tool call, we access the session via server.request_context.session.

4. Request context access: WORKS.
   - Inside tool handlers, `server.request_context` returns a RequestContext with
     .session (ServerSession), .request_id, .meta, and .lifespan_context.

5. Template rendering: WORKS.
   - Jinja2Templates from Starlette/FastAPI works alongside the MCP mount.

6. Testing pattern: Use create_app() factory + manual session_manager.run().
   - StreamableHTTPSessionManager.run() can only be called once per instance.
   - Each test needs a fresh app with a fresh session manager.
   - httpx ASGITransport does not invoke ASGI lifespan events, so tests must
     start the session manager explicitly.

ARCHITECTURE:
    FastAPI app
    ├── GET /dashboard          (Jinja2 HTML page)
    └── Mount /mcp/
        └── StreamableHTTPSessionManager.handle_request  (MCP Streamable HTTP)
            ├── POST /mcp/      (JSON-RPC requests: initialize, tools/list, tools/call)
            ├── GET  /mcp/      (SSE stream for server-initiated messages)
            └── DELETE /mcp/    (session termination)
"""

from __future__ import annotations

import contextlib
import logging
from collections.abc import AsyncIterator
from pathlib import Path
from typing import Any

from fastapi import FastAPI, Request
from fastapi.templating import Jinja2Templates
from starlette.types import Receive, Scope, Send

import mcp.types as types
from mcp.server.lowlevel.server import NotificationOptions, Server
from mcp.server.models import InitializationOptions
from mcp.server.streamable_http_manager import StreamableHTTPSessionManager
from mcp.shared.message import SessionMessage
from mcp.types import JSONRPCMessage, JSONRPCNotification

logger = logging.getLogger(__name__)

templates_dir = Path(__file__).parent / "templates"
templates = Jinja2Templates(directory=str(templates_dir))


# ---------------------------------------------------------------------------
# MCP Server setup (low-level)
# ---------------------------------------------------------------------------


def _create_mcp_server() -> Server:
    """Create and configure the low-level MCP server with tools and capabilities."""
    server = Server(name="bifrost-spike", version="0.1.0")

    # Monkey-patch create_initialization_options to always inject claude/channel.
    # The session manager calls this with no args, so defaults would yield empty
    # experimental capabilities. We ensure ours are always present.
    _original = server.create_initialization_options

    def _patched(
        notification_options: NotificationOptions | None = None,
        experimental_capabilities: dict[str, dict[str, Any]] | None = None,
    ) -> InitializationOptions:
        merged = experimental_capabilities or {}
        merged.setdefault("claude/channel", {})
        return _original(
            notification_options=notification_options,
            experimental_capabilities=merged,
        )

    server.create_initialization_options = _patched  # type: ignore[assignment]

    # -- Tool handlers --------------------------------------------------------

    @server.list_tools()
    async def list_tools() -> list[types.Tool]:
        return [
            types.Tool(
                name="echo",
                description="Returns the input message unchanged.",
                inputSchema={
                    "type": "object",
                    "properties": {
                        "message": {"type": "string", "description": "Message to echo back"},
                    },
                    "required": ["message"],
                },
            ),
            types.Tool(
                name="ping_channel",
                description="Sends a notifications/claude/channel push via SSE.",
                inputSchema={
                    "type": "object",
                    "properties": {},
                },
            ),
        ]

    @server.call_tool()
    async def call_tool(
        name: str, arguments: dict[str, Any] | None
    ) -> list[types.TextContent]:
        match name:
            case "echo":
                msg = (arguments or {}).get("message", "")
                return [types.TextContent(type="text", text=msg)]

            case "ping_channel":
                ctx = server.request_context
                session = ctx.session

                # Send a raw JSON-RPC notification with arbitrary method.
                # ServerNotification is a typed union that doesn't include
                # custom methods, so we bypass it with send_message().
                notification = JSONRPCNotification(
                    jsonrpc="2.0",
                    method="notifications/claude/channel",
                    params={"channel": "bifrost", "data": {"ping": True}},
                )
                await session.send_message(
                    SessionMessage(message=JSONRPCMessage(notification))
                )

                return [types.TextContent(type="text", text="channel notification sent")]

            case _:
                return [types.TextContent(type="text", text=f"unknown tool: {name}")]

    return server


# ---------------------------------------------------------------------------
# App factory
# ---------------------------------------------------------------------------


def create_app() -> tuple[FastAPI, StreamableHTTPSessionManager]:
    """Create a fresh FastAPI app with MCP server. Returns (app, session_manager).

    The caller must ensure session_manager.run() is active before handling
    MCP requests (either via the app lifespan or manually in tests).
    """
    mcp_server = _create_mcp_server()

    sm = StreamableHTTPSessionManager(
        app=mcp_server,
        json_response=False,
        stateless=False,
    )

    @contextlib.asynccontextmanager
    async def lifespan(_app: FastAPI) -> AsyncIterator[None]:
        async with sm.run():
            yield

    fastapi_app = FastAPI(title="Bifrost Spike", lifespan=lifespan)

    @fastapi_app.get("/dashboard")
    async def dashboard(request: Request):
        return templates.TemplateResponse(request, "dashboard.html")

    # ASGI wrapper for the session manager
    class _MCPTransport:
        async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
            await sm.handle_request(scope, receive, send)

    fastapi_app.mount("/mcp", app=_MCPTransport())

    return fastapi_app, sm


# Module-level app for `uvicorn bifrost.spike_mcp:app`
app, session_manager = create_app()
