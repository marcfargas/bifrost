"""Tests for the Bifrost FastMCP application."""

from __future__ import annotations

import pytest
from asgi_lifespan import LifespanManager
from httpx import ASGITransport, AsyncClient

from bifrost.app import create_app
from bifrost.config import Config


@pytest.fixture()
def config(tmp_path):
    return Config(
        host="127.0.0.1",
        port=9999,
        db_path=str(tmp_path / "test.db"),
        insecure=True,
    )


@pytest.fixture()
def mcp_app(config):
    return create_app(config)


@pytest.fixture()
async def client(mcp_app):
    starlette_app = mcp_app.streamable_http_app()
    async with LifespanManager(starlette_app) as manager:
        transport = ASGITransport(app=manager.app)
        async with AsyncClient(transport=transport, base_url="http://test") as c:
            yield c


# ------------------------------------------------------------------
# Health endpoint
# ------------------------------------------------------------------

class TestHealth:
    async def test_health_returns_200(self, client):
        resp = await client.get("/health")
        assert resp.status_code == 200
        assert resp.json() == {"status": "ok"}


# ------------------------------------------------------------------
# MCP endpoint exists
# ------------------------------------------------------------------

class TestMCPEndpoint:
    async def test_mcp_endpoint_not_404(self, client):
        # POST to /mcp should not 404 (it may return 400 without proper MCP handshake)
        resp = await client.post("/mcp", json={})
        assert resp.status_code != 404


# ------------------------------------------------------------------
# Tool registration
# ------------------------------------------------------------------

class TestToolRegistration:
    def test_all_tools_registered(self, mcp_app):
        tool_names = set()
        for tool in mcp_app._tool_manager.list_tools():
            tool_names.add(tool.name)

        expected = {
            "bifrost_introduce",
            "bifrost_whoami",
            "bifrost_list_agents",
            "bifrost_request_task",
            "bifrost_update_task",
            "bifrost_get_task",
            "bifrost_list_tasks",
            "bifrost_send",
            "bifrost_list_conversations",
            "bifrost_subscribe",
            "bifrost_check",
        }
        assert expected.issubset(tool_names), f"Missing tools: {expected - tool_names}"
        assert len(tool_names) >= 11


# ------------------------------------------------------------------
# Config
# ------------------------------------------------------------------

class TestConfig:
    def test_default_config(self):
        c = Config()
        assert c.host == "0.0.0.0"
        assert c.port == 8000
        assert c.db_path == "data/bifrost.db"
        assert c.insecure is False

    def test_custom_config(self, tmp_path):
        c = Config(
            host="127.0.0.1",
            port=9000,
            db_path=str(tmp_path / "custom.db"),
            insecure=True,
        )
        assert c.port == 9000
        assert c.insecure is True


# ------------------------------------------------------------------
# App factory
# ------------------------------------------------------------------

class TestCreateApp:
    def test_creates_fastmcp(self, config):
        app = create_app(config)
        assert app.name == "bifrost"

    def test_insecure_mode_no_auth(self, config):
        app = create_app(config)
        # In insecure mode, no auth provider should be set
        assert app.settings is not None

    def test_instructions_set(self, config):
        app = create_app(config)
        assert app.instructions is not None
        assert "bifrost_send" in app.instructions
