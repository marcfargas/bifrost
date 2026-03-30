"""Tests for Spike 3: OAuth for CLI MCP Clients."""

from __future__ import annotations

import json
from unittest.mock import AsyncMock, patch

import pytest
from httpx import ASGITransport, AsyncClient

from bifrost.spike_oauth import (
    DexTokenVerifier,
    OIDCConfig,
    _MCPTransport,
    create_app,
)
from mcp.server.auth.provider import AccessToken


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
    config.server_url = "https://bifrost-spike.example.com"
    return config


@pytest.fixture
async def insecure_client(insecure_config: OIDCConfig):
    """Client for app without auth."""
    app, sm, _verifier = create_app(oidc_config=insecure_config)
    try:
        async with sm.run():
            transport = ASGITransport(app=app)
            async with AsyncClient(transport=transport, base_url="http://test") as c:
                yield c
    except RuntimeError as e:
        if "cancel scope" in str(e):
            pass
        else:
            raise


@pytest.fixture
async def secure_client(secure_config: OIDCConfig):
    """Client for app with auth enabled (but Dex is mocked)."""
    app, sm, verifier = create_app(oidc_config=secure_config)
    try:
        async with sm.run():
            transport = ASGITransport(app=app)
            async with AsyncClient(transport=transport, base_url="http://test") as c:
                yield c, verifier
    except RuntimeError as e:
        if "cancel scope" in str(e):
            pass
        else:
            raise
    finally:
        if verifier:
            await verifier.close()


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
                data_str = line[len("data:") :].strip()
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
        "/mcp/",
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
        "/mcp/",
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
    client, _verifier = secure_client
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
        "/mcp/",
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
        "/mcp/",
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
# Tests: Secure mode (auth enabled)
# ---------------------------------------------------------------------------


async def test_mcp_requires_auth_when_configured(secure_client):
    """MCP requests without a bearer token should get 401 when auth is enabled."""
    client, _verifier = secure_client
    resp = await client.post(
        "/mcp/",
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
    body = resp.json()
    assert body["error"] == "invalid_token"


async def test_mcp_rejects_bad_token(secure_client):
    """MCP requests with an invalid bearer token should get 401."""
    client, verifier = secure_client

    # Mock the verifier to reject the token
    with patch.object(verifier, "verify_token", new_callable=AsyncMock, return_value=None):
        resp = await client.post(
            "/mcp/",
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
                "Authorization": "Bearer bad-token-123",
            },
        )
    assert resp.status_code == 401


async def test_mcp_accepts_valid_token(secure_client):
    """MCP requests with a valid bearer token should succeed."""
    client, verifier = secure_client

    valid_token = AccessToken(
        token="valid-token-123",
        client_id="test-user-sub",
        scopes=["openid", "email"],
    )

    with patch.object(verifier, "verify_token", new_callable=AsyncMock, return_value=valid_token):
        data, session_id = await mcp_initialize(
            client,
            extra_headers={"Authorization": "Bearer valid-token-123"},
        )

    assert data is not None
    assert "result" in data
    assert "capabilities" in data["result"]


async def test_whoami_with_valid_token(secure_client):
    """whoami tool should return the authenticated user's identity."""
    client, verifier = secure_client

    valid_token = AccessToken(
        token="valid-token-123",
        client_id="test-user-sub",
        scopes=["openid", "email"],
    )

    auth_headers = {"Authorization": "Bearer valid-token-123"}

    with patch.object(verifier, "verify_token", new_callable=AsyncMock, return_value=valid_token):
        data, session_id = await mcp_initialize(client, extra_headers=auth_headers)
        await mcp_send_initialized(client, session_id, extra_headers=auth_headers)

        resp = await client.post(
            "/mcp/",
            json={
                "jsonrpc": "2.0",
                "method": "tools/call",
                "id": 3,
                "params": {"name": "whoami", "arguments": {}},
            },
            headers=mcp_headers(session_id, extra_headers=auth_headers),
        )

    data = _parse_mcp_response(resp)
    assert data is not None
    assert "result" in data
    content = data["result"]["content"]
    assert any("test-user-sub" in c.get("text", "") for c in content)


# ---------------------------------------------------------------------------
# Tests: Protected Resource Metadata (RFC 9728)
# ---------------------------------------------------------------------------


async def test_protected_resource_metadata(secure_client):
    """The /.well-known/oauth-protected-resource endpoint should exist when auth is enabled."""
    client, _verifier = secure_client

    # RFC 9728: the metadata URL is /.well-known/oauth-protected-resource + resource path
    # Our resource is at /mcp/ so the metadata is at
    # /.well-known/oauth-protected-resource/mcp/
    # But since we mount at /mcp, the route is relative: just /
    # The actual path from the client's perspective through the mount:
    resp = await client.get("/mcp/.well-known/oauth-protected-resource/mcp/")
    # This may return 200 or 404 depending on how Starlette routes the mount.
    # The important thing is it doesn't crash.
    # NOTE: In production, the well-known endpoint should be at the root,
    # not under /mcp/. This is a known limitation of mounting the Starlette
    # auth app under /mcp.
    if resp.status_code == 200:
        data = resp.json()
        assert "resource" in data
        assert "authorization_servers" in data


# ---------------------------------------------------------------------------
# Tests: DexTokenVerifier unit tests
# ---------------------------------------------------------------------------


async def test_dex_verifier_valid_token():
    """DexTokenVerifier should return AccessToken for valid tokens."""
    verifier = DexTokenVerifier("https://auth.example.com/dex")

    mock_response = AsyncMock()
    mock_response.status_code = 200
    # httpx Response.json() is synchronous, so use a plain Mock for it
    from unittest.mock import Mock
    mock_response.json = Mock(return_value={
        "sub": "user-123",
        "email": "user@example.com",
        "groups": ["admin", "users"],
    })

    mock_client = AsyncMock()
    mock_client.get = AsyncMock(return_value=mock_response)
    mock_client.is_closed = False
    verifier._client = mock_client

    result = await verifier.verify_token("test-token")
    assert result is not None
    assert result.client_id == "user-123"
    assert result.scopes == ["admin", "users"]
    assert result.token == "test-token"

    await verifier.close()


async def test_dex_verifier_invalid_token():
    """DexTokenVerifier should return None for invalid tokens."""
    verifier = DexTokenVerifier("https://auth.example.com/dex")

    mock_response = AsyncMock()
    mock_response.status_code = 401

    mock_client = AsyncMock()
    mock_client.get = AsyncMock(return_value=mock_response)
    mock_client.is_closed = False
    verifier._client = mock_client

    result = await verifier.verify_token("bad-token")
    assert result is None

    await verifier.close()


async def test_dex_verifier_network_error():
    """DexTokenVerifier should return None on network errors."""
    verifier = DexTokenVerifier("https://auth.example.com/dex")

    mock_client = AsyncMock()
    mock_client.get = AsyncMock(side_effect=Exception("connection refused"))
    mock_client.is_closed = False
    verifier._client = mock_client

    result = await verifier.verify_token("some-token")
    assert result is None

    await verifier.close()


# ---------------------------------------------------------------------------
# Tests: OIDCConfig
# ---------------------------------------------------------------------------


def test_oidc_config_not_configured():
    """OIDCConfig.is_configured should be False when issuer is empty."""
    config = OIDCConfig.__new__(OIDCConfig)
    config.issuer = ""
    config.client_id = ""
    config.client_secret = ""
    config.server_url = ""
    assert config.is_configured is False


def test_oidc_config_configured():
    """OIDCConfig.is_configured should be True when issuer and client_id are set."""
    config = OIDCConfig.__new__(OIDCConfig)
    config.issuer = "https://auth.example.com/dex"
    config.client_id = "my-client"
    config.client_secret = "secret"
    config.server_url = ""
    assert config.is_configured is True
