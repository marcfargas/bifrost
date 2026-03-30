"""
Spike 3 v2: Bifrost as OAuth Authorization Server (proxying to Dex)
=====================================================================

ARCHITECTURE:
    Bifrost is BOTH the OAuth Authorization Server (AS) and Resource Server (RS)
    from Claude Code's perspective. Under the hood, bifrost delegates user
    authentication to Dex via OIDC.

    Flow:
    1. Claude Code → POST /mcp → 401 with resource_metadata URL
    2. Claude Code → GET /.well-known/oauth-protected-resource → {authorization_servers: [bifrost]}
    3. Claude Code → GET /.well-known/oauth-authorization-server → {authorize, token, register}
    4. Claude Code → POST /register (Dynamic Client Registration RFC 7591)
    5. Claude Code → GET /authorize?... → bifrost redirects to Dex
    6. Dex callback → bifrost exchanges Dex code for Dex token, issues its own auth code
    7. Claude Code → POST /token (exchanges auth code for bifrost-issued access token)
    8. Claude Code → Bearer token to /mcp → bifrost validates its own token

    Why this approach:
    The previous spike pointed authorization_servers to Dex directly, but Claude
    Code's MCP client expects RFC 8414 metadata at the AS URL, which Dex doesn't
    support. By acting as our own AS, bifrost publishes proper metadata and proxies
    authentication to Dex.

    The MCP Python SDK provides OAuthAuthorizationServerProvider — a protocol that
    handles dynamic client registration (RFC 7591), authorization, token exchange,
    and token verification. The SDK's create_auth_routes() wires this into Starlette
    routes at /authorize, /token, /register, and /.well-known/oauth-authorization-server.

FINDINGS:
    1. SDK Auth Routes: The SDK creates routes at root-level paths (/authorize, /token,
       /register), NOT under /oauth/. The metadata is at /.well-known/oauth-authorization-server.

    2. Provider.authorize(): Returns a URL to redirect to. We redirect to Dex's auth
       endpoint, storing the original AuthorizationParams (state, code_challenge,
       redirect_uri) keyed by a random nonce so we can recover them in the callback.

    3. Callback route: The SDK does NOT provide a callback route — we must add one via
       FastMCP.custom_route(). The callback at /callback handles:
       a) Exchange Dex auth code for Dex ID token (validates user identity)
       b) Generate our own authorization code
       c) Redirect back to Claude Code's redirect_uri with our code + state

    4. Token verification: We store issued access tokens in memory and verify them
       via load_access_token(). No external calls needed — we issued the tokens.

    5. Dex redirect_uri: Must be https://bifrost.blegal.dev/callback (already registered).
"""

from __future__ import annotations

import logging
import os
import secrets
import time
from typing import Any

import httpx
from mcp.server.auth.middleware.auth_context import get_access_token
from mcp.server.auth.provider import (
    AccessToken,
    AuthorizationCode,
    AuthorizationParams,
    OAuthAuthorizationServerProvider,
    RefreshToken,
    construct_redirect_uri,
)
from mcp.server.auth.settings import AuthSettings, ClientRegistrationOptions, RevocationOptions
from mcp.server.fastmcp import FastMCP
from mcp.shared.auth import OAuthClientInformationFull, OAuthToken
from pydantic import AnyHttpUrl
from starlette.requests import Request
from starlette.responses import JSONResponse, RedirectResponse, Response

logger = logging.getLogger(__name__)


# ---------------------------------------------------------------------------
# Configuration
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
        return bool(self.issuer and self.client_id and self.client_secret)


# ---------------------------------------------------------------------------
# Dex-backed OAuth Authorization Server Provider
# ---------------------------------------------------------------------------


class DexOAuthProvider:
    """OAuthAuthorizationServerProvider that delegates authentication to Dex.

    Implements the full provider protocol:
    - Dynamic client registration: stores clients in memory
    - Authorization: redirects to Dex, stores pending state
    - Token exchange: issues our own tokens after Dex callback
    - Token verification: validates our own tokens from memory
    """

    def __init__(self, oidc_config: OIDCConfig) -> None:
        self._oidc = oidc_config
        # Dex endpoints (derived from OIDC issuer)
        issuer = oidc_config.issuer.rstrip("/")
        self._dex_auth_url = f"{issuer}/auth"
        self._dex_token_url = f"{issuer}/token"
        self._dex_userinfo_url = f"{issuer}/userinfo"

        # The callback URL where Dex redirects back to us
        server_url = oidc_config.server_url.rstrip("/")
        self._callback_url = f"{server_url}/callback"

        # In-memory stores
        self._clients: dict[str, OAuthClientInformationFull] = {}
        self._auth_codes: dict[str, AuthorizationCode] = {}
        self._access_tokens: dict[str, AccessToken] = {}
        self._refresh_tokens: dict[str, RefreshToken] = {}

        # Pending authorization flows: nonce -> AuthorizationParams + client_id
        # Stored when we redirect to Dex, consumed when Dex calls back
        self._pending_auth: dict[str, dict[str, Any]] = {}

        self._http_client: httpx.AsyncClient | None = None

    async def _get_http_client(self) -> httpx.AsyncClient:
        if self._http_client is None or self._http_client.is_closed:
            self._http_client = httpx.AsyncClient(timeout=10.0)
        return self._http_client

    async def close(self) -> None:
        if self._http_client and not self._http_client.is_closed:
            await self._http_client.aclose()

    # -- Client Registration (RFC 7591) ------------------------------------

    async def get_client(self, client_id: str) -> OAuthClientInformationFull | None:
        return self._clients.get(client_id)

    async def register_client(self, client_info: OAuthClientInformationFull) -> None:
        # The SDK's RegistrationHandler generates client_id/secret before calling
        # this method, so we just store what we're given.
        if client_info.client_id is None:
            client_info.client_id = secrets.token_hex(16)
        self._clients[client_info.client_id] = client_info

    # -- Authorization -----------------------------------------------------

    async def authorize(
        self, client: OAuthClientInformationFull, params: AuthorizationParams
    ) -> str:
        """Redirect to Dex for authentication.

        We generate a nonce to link this authorization request to the Dex
        callback. The nonce is passed as Dex's `state` parameter. When Dex
        calls back, we look up the original params by nonce.
        """
        nonce = secrets.token_urlsafe(32)

        # Store the original auth params so the callback can create our auth code
        self._pending_auth[nonce] = {
            "client_id": client.client_id,
            "params": params,
            "created_at": time.time(),
        }

        # Build Dex authorization URL
        dex_params = {
            "client_id": self._oidc.client_id,
            "redirect_uri": self._callback_url,
            "response_type": "code",
            "scope": "openid email profile groups",
            "state": nonce,
        }

        dex_url = construct_redirect_uri(self._dex_auth_url, **dex_params)
        return dex_url

    # -- Authorization Code Management -------------------------------------

    async def load_authorization_code(
        self, client: OAuthClientInformationFull, authorization_code: str
    ) -> AuthorizationCode | None:
        code = self._auth_codes.get(authorization_code)
        if code and code.client_id == client.client_id:
            return code
        return None

    async def exchange_authorization_code(
        self, client: OAuthClientInformationFull, authorization_code: AuthorizationCode
    ) -> OAuthToken:
        """Exchange our authorization code for tokens we issue."""
        # Remove the auth code (single use)
        self._auth_codes.pop(authorization_code.code, None)

        # Generate access token
        access_token_str = secrets.token_urlsafe(32)
        access_token = AccessToken(
            token=access_token_str,
            client_id=authorization_code.client_id,
            scopes=authorization_code.scopes,
            expires_at=int(time.time()) + 3600,  # 1 hour
            resource=authorization_code.resource,
        )
        self._access_tokens[access_token_str] = access_token

        # Generate refresh token
        refresh_token_str = secrets.token_urlsafe(32)
        refresh_token = RefreshToken(
            token=refresh_token_str,
            client_id=authorization_code.client_id,
            scopes=authorization_code.scopes,
            expires_at=int(time.time()) + 86400 * 7,  # 7 days
        )
        self._refresh_tokens[refresh_token_str] = refresh_token

        return OAuthToken(
            access_token=access_token_str,
            token_type="Bearer",
            expires_in=3600,
            scope=" ".join(authorization_code.scopes),
            refresh_token=refresh_token_str,
        )

    # -- Refresh Token Management ------------------------------------------

    async def load_refresh_token(
        self, client: OAuthClientInformationFull, refresh_token: str
    ) -> RefreshToken | None:
        token = self._refresh_tokens.get(refresh_token)
        if token and token.client_id == client.client_id:
            return token
        return None

    async def exchange_refresh_token(
        self,
        client: OAuthClientInformationFull,
        refresh_token: RefreshToken,
        scopes: list[str],
    ) -> OAuthToken:
        """Rotate tokens on refresh."""
        # Remove old refresh token
        self._refresh_tokens.pop(refresh_token.token, None)

        # Generate new access token
        access_token_str = secrets.token_urlsafe(32)
        access_token = AccessToken(
            token=access_token_str,
            client_id=refresh_token.client_id,
            scopes=scopes,
            expires_at=int(time.time()) + 3600,
        )
        self._access_tokens[access_token_str] = access_token

        # Generate new refresh token
        new_refresh_str = secrets.token_urlsafe(32)
        new_refresh = RefreshToken(
            token=new_refresh_str,
            client_id=refresh_token.client_id,
            scopes=scopes,
            expires_at=int(time.time()) + 86400 * 7,
        )
        self._refresh_tokens[new_refresh_str] = new_refresh

        return OAuthToken(
            access_token=access_token_str,
            token_type="Bearer",
            expires_in=3600,
            scope=" ".join(scopes),
            refresh_token=new_refresh_str,
        )

    # -- Access Token Verification -----------------------------------------

    async def load_access_token(self, token: str) -> AccessToken | None:
        access_token = self._access_tokens.get(token)
        if access_token is None:
            return None
        # Check expiry
        if access_token.expires_at and access_token.expires_at < time.time():
            self._access_tokens.pop(token, None)
            return None
        return access_token

    # -- Token Revocation --------------------------------------------------

    async def revoke_token(
        self, token: AccessToken | RefreshToken
    ) -> None:
        if isinstance(token, AccessToken):
            self._access_tokens.pop(token.token, None)
        elif isinstance(token, RefreshToken):
            self._refresh_tokens.pop(token.token, None)

    # -- Dex Callback Handler (called from the /callback route) ------------

    async def handle_dex_callback(
        self, code: str, state: str
    ) -> tuple[str, str, str]:
        """Handle the Dex OIDC callback.

        Args:
            code: The authorization code from Dex
            state: The nonce we passed to Dex (links to pending auth)

        Returns:
            Tuple of (redirect_uri, our_auth_code, original_state)

        Raises:
            ValueError: If the state is invalid or expired
        """
        pending = self._pending_auth.pop(state, None)
        if pending is None:
            raise ValueError("Invalid or expired authorization state")

        # Check if the pending auth is too old (10 minutes)
        if time.time() - pending["created_at"] > 600:
            raise ValueError("Authorization request expired")

        params: AuthorizationParams = pending["params"]
        client_id: str = pending["client_id"]

        # Exchange Dex code for Dex tokens (validates the user authenticated)
        http = await self._get_http_client()
        dex_resp = await http.post(
            self._dex_token_url,
            data={
                "grant_type": "authorization_code",
                "code": code,
                "redirect_uri": self._callback_url,
                "client_id": self._oidc.client_id,
                "client_secret": self._oidc.client_secret,
            },
        )

        if dex_resp.status_code != 200:
            logger.error("Dex token exchange failed: %s %s", dex_resp.status_code, dex_resp.text)
            raise ValueError(f"Dex token exchange failed: {dex_resp.status_code}")

        # We don't need to parse the Dex tokens in detail — the fact that
        # Dex accepted the code proves the user authenticated successfully.
        # In production, we'd verify the ID token and extract user claims.

        # Generate our own authorization code
        our_code = secrets.token_urlsafe(32)
        auth_code = AuthorizationCode(
            code=our_code,
            scopes=params.scopes or [],
            expires_at=time.time() + 300,  # 5 minutes
            client_id=client_id,
            code_challenge=params.code_challenge,
            redirect_uri=params.redirect_uri,
            redirect_uri_provided_explicitly=params.redirect_uri_provided_explicitly,
            resource=params.resource,
        )
        self._auth_codes[our_code] = auth_code

        return str(params.redirect_uri), our_code, params.state or ""


# ---------------------------------------------------------------------------
# App Factory
# ---------------------------------------------------------------------------


def create_app(
    oidc_config: OIDCConfig | None = None,
    **extra_kwargs: Any,
) -> tuple[FastMCP, DexOAuthProvider | None]:
    """Create a FastMCP app with OAuth AS backed by Dex.

    Args:
        oidc_config: OIDC configuration (defaults to env vars)
        **extra_kwargs: Passed through to FastMCP constructor (e.g. transport_security)

    Returns (mcp_server, oauth_provider).
    """
    if oidc_config is None:
        oidc_config = OIDCConfig()

    provider: DexOAuthProvider | None = None

    if oidc_config.is_configured:
        server_url = oidc_config.server_url or "https://bifrost.blegal.dev"

        provider = DexOAuthProvider(oidc_config)

        mcp = FastMCP(
            name="bifrost-spike-oauth",
            auth_server_provider=provider,
            auth=AuthSettings(
                # issuer_url = bifrost itself (we ARE the AS)
                issuer_url=AnyHttpUrl(server_url),
                # resource_server_url = the MCP endpoint
                resource_server_url=AnyHttpUrl(f"{server_url}/mcp"),
                client_registration_options=ClientRegistrationOptions(
                    enabled=True,
                    valid_scopes=["openid", "email", "profile"],
                    default_scopes=["openid"],
                ),
                revocation_options=RevocationOptions(enabled=True),
                required_scopes=[],
            ),
            stateless_http=False,
            streamable_http_path="/mcp",
            **extra_kwargs,
        )

        # -- Dex callback route (receives redirect from Dex after user login) --
        @mcp.custom_route("/callback", methods=["GET"])
        async def dex_callback(request: Request) -> Response:
            """Handle the Dex OIDC callback."""
            code = request.query_params.get("code")
            state = request.query_params.get("state")

            if not code or not state:
                return JSONResponse(
                    {"error": "Missing code or state parameter"},
                    status_code=400,
                )

            try:
                redirect_uri, our_code, original_state = (
                    await provider.handle_dex_callback(code, state)
                )
            except ValueError as e:
                return JSONResponse(
                    {"error": str(e)},
                    status_code=400,
                )

            # Redirect back to Claude Code's redirect_uri with our auth code
            params: dict[str, str | None] = {"code": our_code}
            if original_state:
                params["state"] = original_state
            target = construct_redirect_uri(redirect_uri, **params)
            return RedirectResponse(url=target, status_code=302)

    else:
        logger.info("OAuth disabled: OIDC not configured, running without auth")
        mcp = FastMCP(
            name="bifrost-spike-oauth",
            stateless_http=False,
            streamable_http_path="/mcp",
            **extra_kwargs,
        )

    # -- Health endpoint (always available, no auth) -----------------------
    @mcp.custom_route("/health", methods=["GET"])
    async def health(request: Request) -> Response:
        return JSONResponse({
            "status": "ok",
            "auth_enabled": oidc_config.is_configured,
        })

    # -- MCP Tools ---------------------------------------------------------

    @mcp.tool()
    async def whoami() -> str:
        """Returns the authenticated user's identity."""
        access_token = get_access_token()
        if access_token:
            return (
                f"Authenticated as: {access_token.client_id}\n"
                f"Scopes: {', '.join(access_token.scopes) or 'none'}"
            )
        return "Not authenticated (auth not configured or no token)"

    @mcp.tool()
    async def echo(message: str) -> str:
        """Returns the input message unchanged."""
        return message

    return mcp, provider


# Module-level app for `uvicorn bifrost.spike_oauth:app`
_mcp, _provider = create_app()
app = _mcp.streamable_http_app()
