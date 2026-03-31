"""Tests for dashboard routes."""

from __future__ import annotations

import pytest
from starlette.testclient import TestClient

from bifrost.app import create_app
from bifrost.config import Config


@pytest.fixture
def client(tmp_path):
    config = Config(db_path=str(tmp_path / "test.db"), insecure=True)
    app = create_app(config)
    starlette_app = app.streamable_http_app()
    return TestClient(starlette_app)


class TestDashboardRoutes:
    def test_index_redirects_to_agents(self, client):
        resp = client.get("/", follow_redirects=False)
        assert resp.status_code == 307
        assert resp.headers["location"] == "/agents"

    def test_agents_page(self, client):
        resp = client.get("/agents")
        assert resp.status_code == 200
        assert "Agent Directory" in resp.text
        assert "Bifrost" in resp.text
        assert 'aria-current="page"' in resp.text

    def test_agents_list_partial(self, client):
        resp = client.get("/agents/list")
        assert resp.status_code == 200

    def test_tasks_page(self, client):
        resp = client.get("/tasks")
        assert resp.status_code == 200
        assert "Task Board" in resp.text

    def test_tasks_list_partial(self, client):
        resp = client.get("/tasks/list")
        assert resp.status_code == 200

    def test_conversations_page(self, client):
        resp = client.get("/conversations")
        assert resp.status_code == 200
        assert "Conversations" in resp.text

    def test_activity_page(self, client):
        resp = client.get("/activity")
        assert resp.status_code == 200
        assert "Activity Feed" in resp.text

    def test_human_operator_created_lazily(self, client):
        """Per D-08: human operator created on first dashboard visit."""
        # The agent page visit triggers lazy creation
        resp = client.get("/agents")
        assert resp.status_code == 200
        # Verify via the agents list partial
        resp = client.get("/agents/list")
        assert resp.status_code == 200
        assert "Human Operator" in resp.text

    def test_agents_list_with_data(self, client):
        """Test agent list shows human operator card with status and badge."""
        # Trigger human operator creation
        resp = client.get("/agents")
        resp = client.get("/agents/list")
        assert resp.status_code == 200
        assert "Human Operator" in resp.text
        assert "human-card" in resp.text
        assert "status-dot" in resp.text

    def test_tasks_list_with_filter(self, client):
        """Test task list filter parameter returns empty state."""
        resp = client.get("/tasks/list?status=queued")
        assert resp.status_code == 200
        assert "No tasks found." in resp.text

    def test_tasks_filter_buttons_present(self, client):
        """Test task page has filter buttons."""
        resp = client.get("/tasks")
        assert resp.status_code == 200
        assert 'hx-get="/tasks/list?status=queued"' in resp.text
        assert 'hx-get="/tasks/list?status=running"' in resp.text
        assert 'hx-get="/tasks/list?status=completed"' in resp.text
        assert 'hx-get="/tasks/list?status=failed"' in resp.text
