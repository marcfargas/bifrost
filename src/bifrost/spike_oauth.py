"""
Spike 3: OAuth for CLI MCP Clients
====================================

GOAL: Test whether we can protect an MCP endpoint with OAuth (via Dex OIDC)
and have Claude Code authenticate against it.

ARCHITECTURE:
    Two auth surfaces:
    1. Dashboard (/dashboard, /agents, etc.) — protected by Traefik forward-auth
       middleware (auth-all@docker). Zero app code needed.
    2. MCP endpoint (/mcp/) — protected by bearer token validation. The MCP SDK
       provides middleware for this.

    For the MCP endpoint, we operate as an OAuth Resource Server (RS):
    - Dex is the Authorization Server (AS) — it issues tokens
    - Bifrost validates tokens by checking Dex's userinfo endpoint
    - The MCP SDK's auth system provides:
      * `TokenVerifier` protocol — we implement this to validate tokens against Dex
      * `BearerAuthBackend` — Starlette AuthenticationMiddleware that extracts Bearer tokens
      * `RequireAuthMiddleware` — ASGI middleware that rejects unauthenticated requests
      * `AuthContextMiddleware` — stores the authenticated user in a contextvar
      * `get_access_token()` — retrieves the token from contextvar in tool handlers
      * Protected Resource Metadata (RFC 9728) — tells clients where the AS lives

    The MCP SDK also supports a full OAuth AS mode (OAuthAuthorizationServerProvider)
    where the MCP server implements /authorize, /token, /register endpoints. This is
    used when the MCP server proxies OAuth or IS the OAuth server. We do NOT use this
    mode — Dex is the AS, and we just validate its tokens.

FINDINGS (updated after implementation):

1. MCP SDK Auth Integration: The SDK's FastMCP.streamable_http_app() method shows
   the wiring pattern clearly:
   - Starlette AuthenticationMiddleware with BearerAuthBackend (extracts Bearer token)
   - AuthContextMiddleware (stores authenticated user in contextvar)
   - RequireAuthMiddleware wraps the StreamableHTTP ASGI app (rejects if no auth)
   - Protected Resource Metadata endpoint at /.well-known/oauth-protected-resource
     tells clients which AS to use (RFC 9728)

2. Token Verification against Dex: We implement TokenVerifier by calling Dex's
   userinfo endpoint with the bearer token. If Dex returns user info, the token is
   valid. We map the response to an AccessToken with client_id=sub, scopes from the
   token's scope claim. This avoids needing to validate JWTs locally (no JWKS fetching,
   no signature verification). Trade-off: one extra HTTP call per request, but simpler
   and works regardless of Dex's token format.

3. Claude Code OAuth Support: CRITICAL QUESTION. As of March 2026, Claude Code's
   MCP client supports `type: url` servers. The MCP spec defines:
   - Server publishes /.well-known/oauth-protected-resource (RFC 9728)
   - Client discovers the AS from that metadata
   - Client does OAuth with the AS directly (PKCE flow)
   - Client sends bearer token to the MCP server

   For this to work with Dex:
   a) We publish protected resource metadata pointing to Dex as the AS
   b) Claude Code must support RFC 9728 discovery + external AS flows
   c) Dex must support dynamic client registration OR we pre-register Claude Code
   d) Claude Code must handle the browser-based OAuth redirect

   NEEDS MANUAL TESTING: Deploy this spike and configure Claude Code with:
   ```json
   {
     "mcpServers": {
       "bifrost-spike": {
         "type": "url",
         "url": "https://bifrost-spike.blegal.dev/mcp/"
       }
     }
   }
   ```

4. Insecure Fallback: When OIDC_ISSUER is not set, the server runs without auth.
   This makes local development easy and lets us test the MCP tools without OAuth.

5. Auth Context in Tools: The MCP SDK provides get_access_token() to retrieve the
   authenticated user's token from a contextvar. This works inside tool handlers
   because AuthContextMiddleware sets the contextvar before the request reaches
   the MCP transport.
"""

from __future__ import annotations

import contextlib
import logging
import os
from collections.abc import AsyncIterator
from typing import Any

import httpx
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from pydantic import AnyHttpUrl
from starlette.middleware import Middleware
from starlette.middleware.authentication import AuthenticationMiddleware
from starlette.routing import Mount, Route
from starlette.types import Receive, Scope, Send

import mcp.types as types
from mcp.server.auth.middleware.auth_context import (
    AuthContextMiddleware,
    get_access_token,
)
from mcp.server.auth.middleware.bearer_auth import (
    BearerAuthBackend,
    RequireAuthMiddleware,
)
from mcp.server.auth.provider import AccessToken, TokenVerifier
from mcp.server.auth.routes import (
    build_resource_metadata_url,
    create_protected_resource_routes,
)
from mcp.server.auth.settings import AuthSettings
from mcp.server.lowlevel.server import NotificationOptions, Server
from mcp.server.models import InitializationOptions
from mcp.server.streamable_http_manager import StreamableHTTPSessionManager

logger = logging.getLogger(__name__)


# ---------------------------------------------------------------------------
# Configuration from environment
# ---------------------------------------------------------------------------


class OIDCConfig:
    """OIDC configuration from environment variables."""

    def __init__(self) -> None:
        self.issuer = os.environ.get("OIDC_ISSUER", "").strip()
        self.client_id = os.environ.get("OIDC_CLIENT_ID", "").strip()
        self.client_secret = os.environ.get("OIDC_CLIENT_SECRET", "").strip()
        self.server_url = os.environ.get("SERVER_URL", "").strip()

    @property
    def is_configured(self) -> bool:
        return bool(self.issuer and self.client_id)


# ---------------------------------------------------------------------------
# Dex Token Verifier
# ---------------------------------------------------------------------------


class DexTokenVerifier:
    """Verify bearer tokens by calling Dex's userinfo endpoint.

    Instead of validating JWTs locally (which requires JWKS fetching and
    signature verification), we delegate to Dex's userinfo endpoint. If the
    token is valid, Dex returns user info; if not, it returns 401.

    This is simpler and works regardless of Dex's token format (opaque or JWT).
    The trade-off is one extra HTTP call per MCP request, which is acceptable
    for a spike. In production, we'd cache tokens or validate JWTs locally.
    """

    def __init__(self, issuer_url: str) -> None:
        self._userinfo_url = issuer_url.rstrip("/") + "/userinfo"
        self._client: httpx.AsyncClient | None = None

    async def _get_client(self) -> httpx.AsyncClient:
        if self._client is None or self._client.is_closed:
            self._client = httpx.AsyncClient(timeout=10.0)
        return self._client

    async def verify_token(self, token: str) -> AccessToken | None:
        """Verify a bearer token against Dex's userinfo endpoint."""
        try:
            client = await self._get_client()
            resp = await client.get(
                self._userinfo_url,
                headers={"Authorization": f"Bearer {token}"},
            )
            if resp.status_code != 200:
                logger.debug("Token verification failed: %s", resp.status_code)
                return None

            user_info = resp.json()
            # Dex userinfo returns: sub, name, email, email_verified, groups, etc.
            return AccessToken(
                token=token,
                client_id=user_info.get("sub", "unknown"),
                scopes=user_info.get("groups", []),
            )
        except Exception:
            logger.exception("Token verification error")
            return None

    async def close(self) -> None:
        if self._client and not self._client.is_closed:
            await self._client.aclose()


# ---------------------------------------------------------------------------
# MCP Server setup
# ---------------------------------------------------------------------------


def _create_mcp_server() -> Server:
    """Create and configure the low-level MCP server with tools."""
    server = Server(name="bifrost-spike-oauth", version="0.1.0")

    # Monkey-patch create_initialization_options (same pattern as spike_mcp.py)
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
                name="whoami",
                description="Returns the authenticated user's identity.",
                inputSchema={
                    "type": "object",
                    "properties": {},
                },
            ),
            types.Tool(
                name="echo",
                description="Returns the input message unchanged.",
                inputSchema={
                    "type": "object",
                    "properties": {
                        "message": {
                            "type": "string",
                            "description": "Message to echo back",
                        },
                    },
                    "required": ["message"],
                },
            ),
        ]

    @server.call_tool()
    async def call_tool(
        name: str, arguments: dict[str, Any] | None
    ) -> list[types.TextContent]:
        match name:
            case "whoami":
                access_token = get_access_token()
                if access_token:
                    return [
                        types.TextContent(
                            type="text",
                            text=(
                                f"Authenticated as: {access_token.client_id}\n"
                                f"Scopes: {', '.join(access_token.scopes) or 'none'}"
                            ),
                        )
                    ]
                return [
                    types.TextContent(
                        type="text",
                        text="Not authenticated (auth not configured or no token)",
                    )
                ]

            case "echo":
                msg = (arguments or {}).get("message", "")
                return [types.TextContent(type="text", text=msg)]

            case _:
                return [types.TextContent(type="text", text=f"unknown tool: {name}")]

    return server


# ---------------------------------------------------------------------------
# ASGI wrapper for the session manager
# ---------------------------------------------------------------------------


class _MCPTransport:
    """ASGI app that delegates to StreamableHTTPSessionManager.handle_request."""

    def __init__(self, session_manager: StreamableHTTPSessionManager) -> None:
        self._sm = session_manager

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        await self._sm.handle_request(scope, receive, send)


# ---------------------------------------------------------------------------
# App factory
# ---------------------------------------------------------------------------


def create_app(
    oidc_config: OIDCConfig | None = None,
) -> tuple[FastAPI, StreamableHTTPSessionManager, DexTokenVerifier | None]:
    """Create a FastAPI app with optional OAuth protection on the MCP endpoint.

    Returns (app, session_manager, token_verifier).
    """
    if oidc_config is None:
        oidc_config = OIDCConfig()

    mcp_server = _create_mcp_server()

    sm = StreamableHTTPSessionManager(
        app=mcp_server,
        json_response=False,
        stateless=False,
    )

    token_verifier: DexTokenVerifier | None = None

    @contextlib.asynccontextmanager
    async def lifespan(_app: FastAPI) -> AsyncIterator[None]:
        async with sm.run():
            try:
                yield
            finally:
                if token_verifier:
                    await token_verifier.close()

    fastapi_app = FastAPI(title="Bifrost Spike (OAuth)", lifespan=lifespan)

    # -- Health endpoint (always unauthenticated) -----------------------------

    @fastapi_app.get("/health")
    async def health():
        return JSONResponse(
            {
                "status": "ok",
                "auth_enabled": oidc_config.is_configured,
            }
        )

    # -- MCP endpoint with optional OAuth protection --------------------------

    mcp_asgi = _MCPTransport(sm)

    if oidc_config.is_configured:
        logger.info(
            "OAuth enabled: issuer=%s, client_id=%s",
            oidc_config.issuer,
            oidc_config.client_id,
        )
        token_verifier = DexTokenVerifier(oidc_config.issuer)

        # Determine the resource server URL for RFC 9728 metadata
        server_url = oidc_config.server_url or "https://bifrost.blegal.dev"
        resource_url = AnyHttpUrl(f"{server_url}/mcp/")
        issuer_url = AnyHttpUrl(oidc_config.issuer)

        # Build the Starlette middleware stack:
        # 1. AuthenticationMiddleware — extracts Bearer token, runs BearerAuthBackend
        # 2. AuthContextMiddleware — stores AuthenticatedUser in contextvar
        middleware = [
            Middleware(
                AuthenticationMiddleware,
                backend=BearerAuthBackend(token_verifier),
            ),
            Middleware(AuthContextMiddleware),
        ]

        # Build the resource metadata URL for WWW-Authenticate header
        resource_metadata_url = build_resource_metadata_url(resource_url)

        # Wrap the MCP ASGI app with RequireAuthMiddleware
        auth_protected_mcp = RequireAuthMiddleware(
            app=mcp_asgi,
            required_scopes=[],  # No specific scopes required for now
            resource_metadata_url=resource_metadata_url,
        )

        # Protected Resource Metadata (RFC 9728) — tells clients where the AS lives
        resource_routes = create_protected_resource_routes(
            resource_url=resource_url,
            authorization_servers=[issuer_url],
            scopes_supported=[],
            resource_name="Bifrost MCP Server",
        )

        # Mount the protected MCP endpoint and resource metadata
        # We use a Starlette Router to apply middleware to the MCP route only
        from starlette.applications import Starlette
        from starlette.routing import Mount as StarletteMount

        mcp_starlette = Starlette(
            routes=[
                Route("/", endpoint=auth_protected_mcp),
                *resource_routes,
            ],
            middleware=middleware,
        )

        # Mount the Starlette app at /mcp on the FastAPI app
        fastapi_app.mount("/mcp", app=mcp_starlette)

    else:
        logger.info("OAuth disabled: OIDC_ISSUER not configured, running in insecure mode")
        fastapi_app.mount("/mcp", app=mcp_asgi)

    return fastapi_app, sm, token_verifier


# Module-level app for `uvicorn bifrost.spike_oauth:app`
app, session_manager, _verifier = create_app()
