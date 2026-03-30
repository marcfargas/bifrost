"""Tests for Spike 3 v2: Bifrost as OAuth Authorization Server."""

from __future__ import annotations

import json
import secrets
import time
from unittest.mock import AsyncMock, Mock, patch

import pytest
from asgi_lifespan import LifespanManager
from httpx import ASGITransport, AsyncClient

from bifrost.spike_oauth import (
    DexOAuthProvider,
    OIDCConfig,
    create_app,
)
from mcp.server.auth.provider import AccessToken, AuthorizationCode, AuthorizationParams
from mcp.server.fastmcp.server import TransportSecuritySettings
from mcp.shared.auth import OAuthClientInformationFull
from pydantic import AnyUrl

# Disable DNS rebinding protection in tests (httpx ASGITransport doesn't send real Host headers)
_NO_SECURITY = TransportSecuritySettings(enable_dns_rebinding_protection=False)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def insecure_config() -> OIDCConfig:
    """OIDC config with no issuer — auth disabled."""
    config = OIDCConfig.__new__(OIDCConfig)
    config.issuer = ""
    config.client_id = ""
    config.client_secret = ""
    config.server_url = ""
    return config


@pytest.fixture
def secure_config() -> OIDCConfig:
    """OIDC config with issuer set — auth enabled."""
    config = OIDCConfig.__new__(OIDCConfig)
    config.issuer = "https://auth.example.com/dex"
    config.client_id = "bifrost-test"
    config.client_secret = "test-secret"
    config.server_url = "https://bifrost.example.com"
    return config


@pytest.fixture
def provider(secure_config: OIDCConfig) -> DexOAuthProvider:
    """A DexOAuthProvider for unit tests."""
    return DexOAuthProvider(secure_config)


@pytest.fixture
async def insecure_client(insecure_config: OIDCConfig):
    """Client for app without auth."""
    mcp, _provider = create_app(oidc_config=insecure_config, transport_security=_NO_SECURITY)
    starlette_app = mcp.streamable_http_app()
    async with LifespanManager(starlette_app) as manager:
        transport = ASGITransport(app=manager.app)
        async with AsyncClient(transport=transport, base_url="http://localhost") as c:
            yield c


@pytest.fixture
async def secure_client(secure_config: OIDCConfig):
    """Client for app with auth enabled."""
    mcp, provider = create_app(oidc_config=secure_config, transport_security=_NO_SECURITY)
    starlette_app = mcp.streamable_http_app()
    async with LifespanManager(starlette_app) as manager:
        transport = ASGITransport(app=manager.app)
        async with AsyncClient(transport=transport, base_url="http://localhost") as c:
            yield c, provider
    if provider:
        await provider.close()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _parse_mcp_response(response) -> dict | None:
    """Parse MCP response which may be JSON or SSE format."""
    content_type = response.headers.get("content-type", "")

    if "application/json" in content_type:
        return response.json()

    if "text/event-stream" in content_type:
        for line in response.text.splitlines():
            if line.startswith("data:"):
                data_str = line[len("data:"):].strip()
                if not data_str:
                    continue
                try:
                    parsed = json.loads(data_str)
                    if "result" in parsed or "error" in parsed:
                        return parsed
                except json.JSONDecodeError:
                    continue
        return None

    try:
        return response.json()
    except Exception:
        return None


async def mcp_initialize(
    client: AsyncClient, extra_headers: dict[str, str] | None = None
) -> tuple[dict | None, str | None]:
    """Send initialize, return (parsed response, session_id)."""
    headers = {"Accept": "application/json, text/event-stream"}
    if extra_headers:
        headers.update(extra_headers)

    resp = await client.post(
        "/mcp",
        json={
            "jsonrpc": "2.0",
            "method": "initialize",
            "id": 1,
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "test-client", "version": "0.1.0"},
            },
        },
        headers=headers,
    )
    session_id = resp.headers.get("mcp-session-id")
    data = _parse_mcp_response(resp)
    return data, session_id


async def mcp_send_initialized(
    client: AsyncClient,
    session_id: str | None,
    extra_headers: dict[str, str] | None = None,
) -> None:
    """Send the initialized notification."""
    headers = {"Accept": "application/json, text/event-stream"}
    if session_id:
        headers["mcp-session-id"] = session_id
    if extra_headers:
        headers.update(extra_headers)
    await client.post(
        "/mcp",
        json={"jsonrpc": "2.0", "method": "notifications/initialized"},
        headers=headers,
    )


def mcp_headers(
    session_id: str | None, extra_headers: dict[str, str] | None = None
) -> dict[str, str]:
    headers = {"Accept": "application/json, text/event-stream"}
    if session_id:
        headers["mcp-session-id"] = session_id
    if extra_headers:
        headers.update(extra_headers)
    return headers


# ---------------------------------------------------------------------------
# Tests: Health endpoint
# ---------------------------------------------------------------------------


async def test_health_no_auth(insecure_client: AsyncClient):
    resp = await insecure_client.get("/health")
    assert resp.status_code == 200
    data = resp.json()
    assert data["status"] == "ok"
    assert data["auth_enabled"] is False


async def test_health_with_auth(secure_client):
    client, _provider = secure_client
    resp = await client.get("/health")
    assert resp.status_code == 200
    data = resp.json()
    assert data["status"] == "ok"
    assert data["auth_enabled"] is True


# ---------------------------------------------------------------------------
# Tests: Insecure mode (no auth)
# ---------------------------------------------------------------------------


async def test_mcp_endpoint_exists_insecure(insecure_client: AsyncClient):
    """MCP endpoint should accept POST and not return 404."""
    response = await insecure_client.post(
        "/mcp",
        json={"jsonrpc": "2.0", "method": "initialize", "id": 1, "params": {}},
    )
    assert response.status_code != 404


async def test_mcp_initialize_insecure(insecure_client: AsyncClient):
    """Initialize should work without auth when OIDC is not configured."""
    data, session_id = await mcp_initialize(insecure_client)
    assert data is not None
    assert "result" in data
    assert "capabilities" in data["result"]


async def test_whoami_no_auth(insecure_client: AsyncClient):
    """whoami tool should report unauthenticated when auth is disabled."""
    data, session_id = await mcp_initialize(insecure_client)
    await mcp_send_initialized(insecure_client, session_id)

    resp = await insecure_client.post(
        "/mcp",
        json={
            "jsonrpc": "2.0",
            "method": "tools/call",
            "id": 3,
            "params": {"name": "whoami", "arguments": {}},
        },
        headers=mcp_headers(session_id),
    )
    data = _parse_mcp_response(resp)
    assert data is not None
    assert "result" in data
    content = data["result"]["content"]
    assert any("Not authenticated" in c.get("text", "") for c in content)


# ---------------------------------------------------------------------------
# Tests: OAuth AS metadata endpoints
# ---------------------------------------------------------------------------


async def test_oauth_metadata_endpoint(secure_client):
    """The /.well-known/oauth-authorization-server should return proper metadata."""
    client, _provider = secure_client
    resp = await client.get("/.well-known/oauth-authorization-server")
    assert resp.status_code == 200
    data = resp.json()

    # Verify all required fields are present
    assert "issuer" in data
    assert "authorization_endpoint" in data
    assert "token_endpoint" in data
    assert "registration_endpoint" in data
    assert "code_challenge_methods_supported" in data
    assert "S256" in data["code_challenge_methods_supported"]

    # Verify endpoints point to bifrost (not Dex)
    assert "bifrost.example.com" in data["authorization_endpoint"]
    assert "bifrost.example.com" in data["token_endpoint"]


async def test_protected_resource_metadata(secure_client):
    """The /.well-known/oauth-protected-resource endpoint should exist."""
    client, _provider = secure_client
    # RFC 9728: metadata at /.well-known/oauth-protected-resource/mcp
    resp = await client.get("/.well-known/oauth-protected-resource/mcp")
    assert resp.status_code == 200
    data = resp.json()
    assert "resource" in data
    assert "authorization_servers" in data
    # Authorization server should be bifrost itself
    assert any("bifrost.example.com" in str(s) for s in data["authorization_servers"])


async def test_oauth_metadata_not_present_insecure(insecure_client: AsyncClient):
    """OAuth metadata should not exist when auth is disabled."""
    resp = await insecure_client.get("/.well-known/oauth-authorization-server")
    assert resp.status_code in (404, 405)


# ---------------------------------------------------------------------------
# Tests: Dynamic Client Registration
# ---------------------------------------------------------------------------


async def test_dynamic_client_registration(secure_client):
    """POST /register should register a new client."""
    client, _provider = secure_client
    resp = await client.post(
        "/register",
        json={
            "redirect_uris": ["http://localhost:3000/callback"],
            "client_name": "test-claude-code",
            "grant_types": ["authorization_code", "refresh_token"],
            "response_types": ["code"],
            "token_endpoint_auth_method": "client_secret_post",
        },
    )
    assert resp.status_code == 201
    data = resp.json()
    assert "client_id" in data
    assert "client_secret" in data
    # SDK generates UUIDs for client_id
    assert len(data["client_id"]) > 0


# ---------------------------------------------------------------------------
# Tests: MCP requires auth when configured
# ---------------------------------------------------------------------------


async def test_mcp_requires_auth_when_configured(secure_client):
    """MCP requests without a bearer token should get 401."""
    client, _provider = secure_client
    resp = await client.post(
        "/mcp",
        json={
            "jsonrpc": "2.0",
            "method": "initialize",
            "id": 1,
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "test-client", "version": "0.1.0"},
            },
        },
        headers={"Accept": "application/json, text/event-stream"},
    )
    assert resp.status_code == 401


async def test_mcp_accepts_valid_token(secure_client):
    """MCP requests with a valid bearer token (in our store) should succeed."""
    client, provider = secure_client

    # Directly store a token in the provider's memory
    token_str = secrets.token_urlsafe(32)
    provider._access_tokens[token_str] = AccessToken(
        token=token_str,
        client_id="test-user",
        scopes=["openid"],
        expires_at=int(time.time()) + 3600,
    )

    data, session_id = await mcp_initialize(
        client,
        extra_headers={"Authorization": f"Bearer {token_str}"},
    )
    assert data is not None
    assert "result" in data
    assert "capabilities" in data["result"]


async def test_mcp_rejects_expired_token(secure_client):
    """MCP requests with an expired token should get 401."""
    client, provider = secure_client

    token_str = secrets.token_urlsafe(32)
    provider._access_tokens[token_str] = AccessToken(
        token=token_str,
        client_id="test-user",
        scopes=["openid"],
        expires_at=int(time.time()) - 100,  # Already expired
    )

    resp = await client.post(
        "/mcp",
        json={
            "jsonrpc": "2.0",
            "method": "initialize",
            "id": 1,
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "test-client", "version": "0.1.0"},
            },
        },
        headers={
            "Accept": "application/json, text/event-stream",
            "Authorization": f"Bearer {token_str}",
        },
    )
    assert resp.status_code == 401


# ---------------------------------------------------------------------------
# Tests: DexOAuthProvider unit tests
# ---------------------------------------------------------------------------


async def test_provider_register_client(provider: DexOAuthProvider):
    """register_client should store client info (SDK generates IDs before calling)."""
    client_info = OAuthClientInformationFull(
        client_id="sdk-generated-uuid",
        client_secret="sdk-generated-secret",
        redirect_uris=[AnyUrl("http://localhost:3000/callback")],
        grant_types=["authorization_code", "refresh_token"],
        response_types=["code"],
    )
    await provider.register_client(client_info)

    # Should be retrievable
    loaded = await provider.get_client("sdk-generated-uuid")
    assert loaded is not None
    assert loaded.client_id == "sdk-generated-uuid"


async def test_provider_register_client_generates_id_if_missing(provider: DexOAuthProvider):
    """register_client should generate client_id if None (fallback)."""
    client_info = OAuthClientInformationFull(
        redirect_uris=[AnyUrl("http://localhost:3000/callback")],
    )
    await provider.register_client(client_info)
    assert client_info.client_id is not None
    assert len(client_info.client_id) > 0


async def test_provider_authorize_redirects_to_dex(provider: DexOAuthProvider):
    """authorize() should return a URL pointing to Dex."""
    client_info = OAuthClientInformationFull(
        client_id="test-client-1",
        client_secret="secret",
        redirect_uris=[AnyUrl("http://localhost:3000/callback")],
    )

    params = AuthorizationParams(
        state="original-state",
        scopes=["openid"],
        code_challenge="test-challenge",
        redirect_uri=AnyUrl("http://localhost:3000/callback"),
        redirect_uri_provided_explicitly=True,
    )

    url = await provider.authorize(client_info, params)

    assert "auth.example.com/dex/auth" in url
    assert "client_id=bifrost-test" in url
    assert "redirect_uri=" in url
    assert "response_type=code" in url

    # Should have stored a pending auth
    assert len(provider._pending_auth) == 1


async def test_provider_dex_callback_invalid_state(provider: DexOAuthProvider):
    """handle_dex_callback should reject unknown state."""
    with pytest.raises(ValueError, match="Invalid or expired"):
        await provider.handle_dex_callback("some-code", "unknown-state")


async def test_provider_dex_callback_success(provider: DexOAuthProvider):
    """handle_dex_callback should exchange Dex code and issue our auth code."""
    # Set up a pending auth
    nonce = "test-nonce-123"
    provider._pending_auth[nonce] = {
        "client_id": "test-client-1",
        "params": AuthorizationParams(
            state="original-state",
            scopes=["openid"],
            code_challenge="test-challenge",
            redirect_uri=AnyUrl("http://localhost:3000/callback"),
            redirect_uri_provided_explicitly=True,
        ),
        "created_at": time.time(),
    }

    # Mock the HTTP client for Dex token exchange
    mock_response = Mock()
    mock_response.status_code = 200
    mock_response.json = Mock(return_value={
        "access_token": "dex-access-token",
        "id_token": "dex-id-token",
        "token_type": "Bearer",
    })

    mock_http = AsyncMock()
    mock_http.post = AsyncMock(return_value=mock_response)
    mock_http.is_closed = False
    provider._http_client = mock_http

    redirect_uri, our_code, original_state = await provider.handle_dex_callback(
        "dex-auth-code", nonce
    )

    assert redirect_uri == "http://localhost:3000/callback"
    assert original_state == "original-state"
    assert our_code in provider._auth_codes

    # Verify the auth code was stored correctly
    auth_code = provider._auth_codes[our_code]
    assert auth_code.client_id == "test-client-1"
    assert auth_code.code_challenge == "test-challenge"


async def test_provider_exchange_authorization_code(provider: DexOAuthProvider):
    """exchange_authorization_code should issue access and refresh tokens."""
    # Store a client and auth code
    client_info = OAuthClientInformationFull(
        client_id="test-client-1",
        client_secret="secret",
        redirect_uris=[AnyUrl("http://localhost:3000/callback")],
    )

    auth_code = AuthorizationCode(
        code="test-code",
        scopes=["openid"],
        expires_at=time.time() + 300,
        client_id="test-client-1",
        code_challenge="test-challenge",
        redirect_uri=AnyUrl("http://localhost:3000/callback"),
        redirect_uri_provided_explicitly=True,
    )
    provider._auth_codes["test-code"] = auth_code

    token = await provider.exchange_authorization_code(client_info, auth_code)

    assert token.access_token is not None
    assert token.refresh_token is not None
    assert token.token_type == "Bearer"
    assert token.expires_in == 3600

    # Auth code should be consumed (removed)
    assert "test-code" not in provider._auth_codes

    # Access token should be stored
    assert token.access_token in provider._access_tokens


async def test_provider_load_access_token(provider: DexOAuthProvider):
    """load_access_token should return stored tokens and reject expired ones."""
    # Store a valid token
    provider._access_tokens["valid-token"] = AccessToken(
        token="valid-token",
        client_id="test-user",
        scopes=["openid"],
        expires_at=int(time.time()) + 3600,
    )

    result = await provider.load_access_token("valid-token")
    assert result is not None
    assert result.client_id == "test-user"

    # Store an expired token
    provider._access_tokens["expired-token"] = AccessToken(
        token="expired-token",
        client_id="test-user",
        scopes=["openid"],
        expires_at=int(time.time()) - 100,
    )

    result = await provider.load_access_token("expired-token")
    assert result is None

    # Unknown token
    result = await provider.load_access_token("unknown")
    assert result is None


async def test_provider_revoke_token(provider: DexOAuthProvider):
    """revoke_token should remove the token from storage."""
    access = AccessToken(
        token="to-revoke",
        client_id="test",
        scopes=["openid"],
    )
    provider._access_tokens["to-revoke"] = access

    await provider.revoke_token(access)
    assert "to-revoke" not in provider._access_tokens


# ---------------------------------------------------------------------------
# Tests: /callback endpoint
# ---------------------------------------------------------------------------


async def test_callback_missing_params(secure_client):
    """GET /callback without code/state should return 400."""
    client, _provider = secure_client
    resp = await client.get("/callback")
    assert resp.status_code == 400
    data = resp.json()
    assert "error" in data


async def test_callback_invalid_state(secure_client):
    """GET /callback with unknown state should return 400."""
    client, _provider = secure_client
    resp = await client.get("/callback?code=test&state=unknown")
    assert resp.status_code == 400


async def test_callback_full_flow(secure_client):
    """GET /callback with valid Dex response should redirect with our auth code."""
    client, provider = secure_client

    # Set up pending auth
    nonce = "test-nonce"
    provider._pending_auth[nonce] = {
        "client_id": "test-client",
        "params": AuthorizationParams(
            state="original-state",
            scopes=["openid"],
            code_challenge="challenge",
            redirect_uri=AnyUrl("http://localhost:3000/callback"),
            redirect_uri_provided_explicitly=True,
        ),
        "created_at": time.time(),
    }

    # Mock Dex token exchange
    mock_response = Mock()
    mock_response.status_code = 200
    mock_response.json = Mock(return_value={
        "access_token": "dex-token",
        "id_token": "dex-id",
        "token_type": "Bearer",
    })

    mock_http = AsyncMock()
    mock_http.post = AsyncMock(return_value=mock_response)
    mock_http.is_closed = False
    provider._http_client = mock_http

    resp = await client.get(
        f"/callback?code=dex-code&state={nonce}",
        follow_redirects=False,
    )

    assert resp.status_code == 302
    location = resp.headers["location"]
    assert "localhost:3000/callback" in location
    assert "code=" in location
    assert "state=original-state" in location


# ---------------------------------------------------------------------------
# Tests: OIDCConfig
# ---------------------------------------------------------------------------


def test_oidc_config_not_configured():
    config = OIDCConfig.__new__(OIDCConfig)
    config.issuer = ""
    config.client_id = ""
    config.client_secret = ""
    config.server_url = ""
    assert config.is_configured is False


def test_oidc_config_configured():
    config = OIDCConfig.__new__(OIDCConfig)
    config.issuer = "https://auth.example.com/dex"
    config.client_id = "my-client"
    config.client_secret = "secret"
    config.server_url = ""
    assert config.is_configured is True


def test_oidc_config_requires_secret():
    """is_configured should be False without client_secret."""
    config = OIDCConfig.__new__(OIDCConfig)
    config.issuer = "https://auth.example.com/dex"
    config.client_id = "my-client"
    config.client_secret = ""
    config.server_url = ""
    assert config.is_configured is False
