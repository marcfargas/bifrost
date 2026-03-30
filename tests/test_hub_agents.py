"""Tests for the AgentHub."""

import pytest

from bifrost.hub.agents import AgentHub
from bifrost.store.db import Store
from bifrost.store.models import AgentSkill, AgentStatus


@pytest.fixture
def hub(tmp_path):
    store = Store(tmp_path / "test.db")
    yield AgentHub(store)
    store.close()


class TestRegister:
    def test_new_agent(self, hub):
        agent = hub.register("bot-1")
        assert agent.name == "bot-1"
        assert agent.status == AgentStatus.ONLINE
        assert agent.connected_at is not None

    def test_reconnect(self, hub):
        a1 = hub.register("bot-1")
        hub.disconnect(a1.id)
        a2 = hub.register("bot-1")
        assert a2.id == a1.id
        assert a2.status == AgentStatus.ONLINE

    def test_register_with_oauth(self, hub):
        agent = hub.register("bot-1", oauth_subject="sub-123")
        assert agent.oauth_subject == "sub-123"


class TestDisconnect:
    def test_disconnect(self, hub):
        a = hub.register("bot-1")
        hub.disconnect(a.id)
        got = hub.get(a.id)
        assert got.status == AgentStatus.OFFLINE

    def test_disconnect_missing(self, hub):
        with pytest.raises(KeyError):
            hub.disconnect("nope")


class TestUpdateStatus:
    def test_set_dnd(self, hub):
        a = hub.register("bot-1")
        hub.update_status(a.id, AgentStatus.DND, reason="focusing")
        got = hub.get(a.id)
        assert got.status == AgentStatus.DND
        assert got.dnd_reason == "focusing"

    def test_set_idle_clears_dnd_reason(self, hub):
        a = hub.register("bot-1")
        hub.update_status(a.id, AgentStatus.DND, reason="focusing")
        hub.update_status(a.id, AgentStatus.IDLE)
        got = hub.get(a.id)
        assert got.status == AgentStatus.IDLE
        assert got.dnd_reason is None

    def test_missing_agent(self, hub):
        with pytest.raises(KeyError):
            hub.update_status("nope", AgentStatus.IDLE)


class TestUpdateCard:
    def test_create_card(self, hub):
        a = hub.register("bot-1")
        card = hub.update_card(a.id, description="A helpful bot", version="1.0")
        assert card.description == "A helpful bot"
        assert card.version == "1.0"

    def test_introduction_as_description(self, hub):
        a = hub.register("bot-1")
        card = hub.update_card(a.id, introduction="I help with code")
        assert card.description == "I help with code"

    def test_description_overrides_introduction(self, hub):
        a = hub.register("bot-1")
        card = hub.update_card(a.id, introduction="intro", description="desc")
        assert card.description == "desc"

    def test_update_skills(self, hub):
        a = hub.register("bot-1")
        skill = AgentSkill(id="s1", name="search", description="Search")
        card = hub.update_card(a.id, skills=[skill])
        assert len(card.skills) == 1
        # Verify persistence
        got = hub.get(a.id)
        assert len(got.card.skills) == 1

    def test_partial_update(self, hub):
        a = hub.register("bot-1")
        hub.update_card(a.id, description="v1", version="1.0")
        hub.update_card(a.id, version="2.0")
        got = hub.get(a.id)
        assert got.card.description == "v1"  # unchanged
        assert got.card.version == "2.0"  # updated

    def test_missing_agent(self, hub):
        with pytest.raises(KeyError):
            hub.update_card("nope", description="x")


class TestGet:
    def test_with_card(self, hub):
        a = hub.register("bot-1")
        hub.update_card(a.id, description="test")
        got = hub.get(a.id)
        assert got.card is not None
        assert got.card.description == "test"

    def test_without_card(self, hub):
        a = hub.register("bot-1")
        got = hub.get(a.id)
        assert got.card is None

    def test_missing(self, hub):
        with pytest.raises(KeyError):
            hub.get("nope")


class TestResolve:
    def test_resolve_by_name(self, hub):
        a = hub.register("bot-1")
        got = hub.resolve("bot-1")
        assert got.id == a.id

    def test_resolve_missing(self, hub):
        with pytest.raises(KeyError):
            hub.resolve("nope")


class TestListAll:
    def test_list_all(self, hub):
        hub.register("bot-1")
        hub.register("bot-2")
        agents = hub.list_all()
        assert len(agents) == 2

    def test_list_filtered(self, hub):
        a1 = hub.register("bot-1")
        hub.register("bot-2")
        hub.disconnect(a1.id)
        online = hub.list_all(status=AgentStatus.ONLINE)
        assert len(online) == 1
        assert online[0].name == "bot-2"
