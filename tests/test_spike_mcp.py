"""Tests for Spike 1: MCP SDK + FastAPI + Channel Push."""

from __future__ import annotations

import json

import pytest
from httpx import ASGITransport, AsyncClient

from bifrost.spike_mcp import create_app


@pytest.fixture
async def client():
    """Create a fresh app + session manager per test.

    StreamableHTTPSessionManager.run() can only be called once per instance,
    so each test gets a brand-new app via the factory.

    httpx ASGITransport does not invoke ASGI lifespan, so we start the
    session manager manually here.

    The session manager uses an anyio task group internally. When MCP sessions
    are created during tests, background tasks are spawned. On teardown, the
    task group cancellation may raise RuntimeError if the cancel scope crosses
    asyncio task boundaries (a known anyio/pytest-asyncio interaction). We
    suppress this since it's only a cleanup issue in tests.
    """
    app, sm = create_app()
    try:
        async with sm.run():
            transport = ASGITransport(app=app)
            async with AsyncClient(transport=transport, base_url="http://test") as c:
                yield c
    except RuntimeError as e:
        if "cancel scope" in str(e):
            pass  # Expected teardown issue with anyio + pytest-asyncio
        else:
            raise


# ---------------------------------------------------------------------------
# Helper to send MCP requests with proper headers
# ---------------------------------------------------------------------------


async def mcp_initialize(client: AsyncClient) -> tuple[dict, str | None]:
    """Send initialize, return (parsed response, session_id)."""
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
    session_id = resp.headers.get("mcp-session-id")
    data = _parse_mcp_response(resp)
    return data, session_id


async def mcp_send_initialized(client: AsyncClient, session_id: str | None) -> None:
    """Send the initialized notification to complete the handshake."""
    headers = {"Accept": "application/json, text/event-stream"}
    if session_id:
        headers["mcp-session-id"] = session_id
    await client.post(
        "/mcp/",
        json={"jsonrpc": "2.0", "method": "notifications/initialized"},
        headers=headers,
    )


def mcp_headers(session_id: str | None) -> dict[str, str]:
    headers = {"Accept": "application/json, text/event-stream"}
    if session_id:
        headers["mcp-session-id"] = session_id
    return headers


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


async def test_dashboard_returns_html(client: AsyncClient):
    response = await client.get("/dashboard")
    assert response.status_code == 200
    assert "Bifrost Spike Dashboard" in response.text


async def test_mcp_endpoint_exists(client: AsyncClient):
    """MCP endpoint should accept POST and not return 404."""
    response = await client.post(
        "/mcp/",
        json={"jsonrpc": "2.0", "method": "initialize", "id": 1, "params": {}},
    )
    assert response.status_code != 404


async def test_mcp_initialize_returns_capabilities(client: AsyncClient):
    """Initialize should return server capabilities including experimental claude/channel."""
    data, _ = await mcp_initialize(client)
    assert data is not None, "Could not parse MCP response"
    assert "result" in data, f"Expected 'result' in response: {data}"

    result = data["result"]
    assert "capabilities" in result
    caps = result["capabilities"]

    # Verify experimental claude/channel is declared
    assert "experimental" in caps, f"No experimental capabilities in: {caps}"
    assert "claude/channel" in caps["experimental"], (
        f"claude/channel not in experimental: {caps['experimental']}"
    )


async def test_mcp_list_tools(client: AsyncClient):
    """After initialization, tools/list should return our tools."""
    _, session_id = await mcp_initialize(client)
    await mcp_send_initialized(client, session_id)

    tools_resp = await client.post(
        "/mcp/",
        json={"jsonrpc": "2.0", "method": "tools/list", "id": 2, "params": {}},
        headers=mcp_headers(session_id),
    )
    data = _parse_mcp_response(tools_resp)
    assert data is not None, f"Could not parse tools response: {tools_resp.text}"
    assert "result" in data

    tool_names = [t["name"] for t in data["result"]["tools"]]
    assert "echo" in tool_names
    assert "ping_channel" in tool_names


async def test_mcp_echo_tool(client: AsyncClient):
    """The echo tool should return the input message."""
    _, session_id = await mcp_initialize(client)
    await mcp_send_initialized(client, session_id)

    echo_resp = await client.post(
        "/mcp/",
        json={
            "jsonrpc": "2.0",
            "method": "tools/call",
            "id": 3,
            "params": {"name": "echo", "arguments": {"message": "hello bifrost"}},
        },
        headers=mcp_headers(session_id),
    )
    data = _parse_mcp_response(echo_resp)
    assert data is not None
    assert "result" in data
    content = data["result"]["content"]
    assert any("hello bifrost" in c.get("text", "") for c in content)


async def test_mcp_ping_channel_tool(client: AsyncClient):
    """The ping_channel tool should execute and return confirmation.

    FINDING: The channel notification sent via session.send_message() during a
    tool call is delivered to the GET SSE stream, NOT piggybacked on the POST
    response. The POST response only contains the tool result. This means for
    push notifications to reach the client, the client must have an active
    GET /mcp/ SSE connection open. This is the expected MCP Streamable HTTP
    behavior — notifications go to the standalone SSE stream.

    In this test we only verify the tool call itself succeeds. Verifying the
    SSE notification delivery would require a streaming GET request running
    concurrently, which is complex to test with httpx ASGITransport.
    """
    _, session_id = await mcp_initialize(client)
    await mcp_send_initialized(client, session_id)

    resp = await client.post(
        "/mcp/",
        json={
            "jsonrpc": "2.0",
            "method": "tools/call",
            "id": 4,
            "params": {"name": "ping_channel", "arguments": {}},
        },
        headers=mcp_headers(session_id),
    )
    data = _parse_mcp_response(resp)
    assert data is not None
    assert "result" in data
    content = data["result"]["content"]
    assert any("channel notification sent" in c.get("text", "") for c in content)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _parse_mcp_response(response) -> dict | None:
    """Parse MCP response which may be JSON or SSE format."""
    content_type = response.headers.get("content-type", "")

    if "application/json" in content_type:
        return response.json()

    if "text/event-stream" in content_type:
        # Parse SSE: look for "data:" lines containing JSON-RPC responses
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

    # Fallback: try JSON
    try:
        return response.json()
    except Exception:
        return None


def _parse_all_sse_events(text: str) -> list[dict]:
    """Parse all JSON-RPC messages from an SSE stream."""
    events = []
    for line in text.splitlines():
        if line.startswith("data:"):
            data_str = line[len("data:"):].strip()
            if not data_str:
                continue
            try:
                events.append(json.loads(data_str))
            except json.JSONDecodeError:
                continue
    return events
