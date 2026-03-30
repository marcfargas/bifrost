"""Tests for the SQLite store."""

import pytest

from bifrost.store.db import Store
from bifrost.store.models import (
    Agent,
    AgentCard,
    AgentSkill,
    AgentStatus,
    Artifact,
    Conversation,
    Event,
    EventType,
    Task,
    TaskStatus,
    TextPart,
    _new_id,
    _now,
)


@pytest.fixture
def store(tmp_path):
    s = Store(tmp_path / "test.db")
    yield s
    s.close()


# ------------------------------------------------------------------
# Agents
# ------------------------------------------------------------------

class TestAgents:
    def test_upsert_and_get(self, store):
        a = Agent(id="a1", name="bot", status=AgentStatus.ONLINE)
        store.upsert_agent(a)
        got = store.get_agent("a1")
        assert got is not None
        assert got.name == "bot"
        assert got.status == AgentStatus.ONLINE

    def test_get_missing(self, store):
        assert store.get_agent("nope") is None

    def test_get_by_name(self, store):
        a = Agent(id="a1", name="mybot")
        store.upsert_agent(a)
        got = store.get_agent_by_name("mybot")
        assert got is not None
        assert got.id == "a1"

    def test_get_by_name_missing(self, store):
        assert store.get_agent_by_name("nope") is None

    def test_list_agents(self, store):
        store.upsert_agent(Agent(id="a1", name="b1", status=AgentStatus.ONLINE))
        store.upsert_agent(Agent(id="a2", name="b2", status=AgentStatus.OFFLINE))
        assert len(store.list_agents()) == 2
        assert len(store.list_agents(status=AgentStatus.ONLINE)) == 1

    def test_upsert_updates(self, store):
        store.upsert_agent(Agent(id="a1", name="bot", status=AgentStatus.OFFLINE))
        store.upsert_agent(Agent(id="a1", name="bot", status=AgentStatus.ONLINE))
        got = store.get_agent("a1")
        assert got.status == AgentStatus.ONLINE


# ------------------------------------------------------------------
# Agent Cards
# ------------------------------------------------------------------

class TestAgentCards:
    def test_upsert_and_get(self, store):
        store.upsert_agent(Agent(id="a1", name="bot"))
        card = AgentCard(agent_id="a1", description="A helpful bot", version="1.0")
        store.upsert_card(card)
        got = store.get_card("a1")
        assert got is not None
        assert got.description == "A helpful bot"
        assert got.version == "1.0"

    def test_get_missing(self, store):
        assert store.get_card("nope") is None

    def test_skills_roundtrip(self, store):
        store.upsert_agent(Agent(id="a1", name="bot"))
        skill = AgentSkill(id="s1", name="search", description="Search things", tags=["web"])
        card = AgentCard(agent_id="a1", description="bot", skills=[skill])
        store.upsert_card(card)
        got = store.get_card("a1")
        assert len(got.skills) == 1
        assert got.skills[0].name == "search"
        assert got.skills[0].tags == ["web"]


# ------------------------------------------------------------------
# Tasks
# ------------------------------------------------------------------

class TestTasks:
    def test_create_and_get(self, store):
        t = Task(id="t1", requester="a1", assignee="a2")
        store.create_task(t)
        got = store.get_task("t1")
        assert got is not None
        assert got.requester == "a1"
        assert got.status == TaskStatus.QUEUED

    def test_get_missing(self, store):
        assert store.get_task("nope") is None

    def test_update_task(self, store):
        store.create_task(Task(id="t1", requester="a1", assignee="a2"))
        store.update_task("t1", status="running", status_message="working on it")
        got = store.get_task("t1")
        assert got.status == TaskStatus.RUNNING
        assert got.status_message == "working on it"

    def test_update_invalid_field(self, store):
        store.create_task(Task(id="t1", requester="a1", assignee="a2"))
        with pytest.raises(ValueError, match="Cannot update field"):
            store.update_task("t1", requester="a3")

    def test_update_artifacts(self, store):
        store.create_task(Task(id="t1", requester="a1", assignee="a2"))
        art = Artifact(id="art1", parts=[TextPart("result")])
        store.update_task("t1", artifacts=[art])
        got = store.get_task("t1")
        assert len(got.artifacts) == 1
        assert got.artifacts[0].parts[0].text == "result"

    def test_list_tasks(self, store):
        store.create_task(Task(id="t1", requester="a1", assignee="a2", status=TaskStatus.QUEUED))
        store.create_task(Task(id="t2", requester="a1", assignee="a3", status=TaskStatus.RUNNING))
        store.create_task(Task(id="t3", requester="a2", assignee="a3", status=TaskStatus.QUEUED))
        assert len(store.list_tasks()) == 3
        assert len(store.list_tasks(status=TaskStatus.QUEUED)) == 2
        assert len(store.list_tasks(requester="a1")) == 2
        assert len(store.list_tasks(assignee="a3")) == 2
        assert len(store.list_tasks(status=TaskStatus.QUEUED, assignee="a3")) == 1


# ------------------------------------------------------------------
# Conversations
# ------------------------------------------------------------------

class TestConversations:
    def test_create_and_get(self, store):
        c = Conversation(id="c1", title="Hello", participants=["a1", "a2"])
        store.create_conversation(c)
        got = store.get_conversation("c1")
        assert got is not None
        assert got.title == "Hello"
        assert got.participants == ["a1", "a2"]

    def test_get_missing(self, store):
        assert store.get_conversation("nope") is None

    def test_update_conversation(self, store):
        store.create_conversation(Conversation(id="c1", title="Hello", participants=["a1"]))
        store.update_conversation("c1", participants=["a1", "a2"], closed=True, closed_reason="done")
        got = store.get_conversation("c1")
        assert got.participants == ["a1", "a2"]
        assert got.closed is True
        assert got.closed_reason == "done"

    def test_list_conversations(self, store):
        store.create_conversation(Conversation(id="c1", title="A", channel="deploy"))
        store.create_conversation(Conversation(id="c2", title="B", channel="deploy", closed=True))
        store.create_conversation(Conversation(id="c3", title="C"))
        assert len(store.list_conversations()) == 3
        assert len(store.list_conversations(channel="deploy")) == 2
        assert len(store.list_conversations(active_only=True)) == 2
        assert len(store.list_conversations(channel="deploy", active_only=True)) == 1


# ------------------------------------------------------------------
# Events
# ------------------------------------------------------------------

class TestEvents:
    def test_append_and_list(self, store):
        store.create_conversation(Conversation(id="c1", title="T"))
        e1 = Event(id="e1", conversation_id="c1", from_agent="a1",
                   data={"text": "hi"}, timestamp="2026-01-01T00:00:00Z")
        e2 = Event(id="e2", conversation_id="c1", from_agent="a2",
                   data={"text": "hello"}, timestamp="2026-01-01T00:00:01Z")
        store.append_event(e1)
        store.append_event(e2)
        events = store.list_events("c1")
        assert len(events) == 2
        assert events[0].id == "e1"
        assert events[1].id == "e2"

    def test_list_after_event(self, store):
        store.create_conversation(Conversation(id="c1", title="T"))
        for i in range(3):
            store.append_event(Event(
                id=f"e{i}", conversation_id="c1", from_agent="a1",
                timestamp=f"2026-01-01T00:00:0{i}Z",
            ))
        events = store.list_events("c1", after_event_id="e0")
        assert len(events) == 2
        assert events[0].id == "e1"

    def test_list_after_missing_event(self, store):
        store.create_conversation(Conversation(id="c1", title="T"))
        assert store.list_events("c1", after_event_id="nope") == []


# ------------------------------------------------------------------
# Delivery state
# ------------------------------------------------------------------

class TestDeliveryState:
    def test_get_set(self, store):
        assert store.get_last_delivered_event("a1", "c1") is None
        store.set_last_delivered_event("a1", "c1", "e5")
        assert store.get_last_delivered_event("a1", "c1") == "e5"
        store.set_last_delivered_event("a1", "c1", "e10")
        assert store.get_last_delivered_event("a1", "c1") == "e10"


# ------------------------------------------------------------------
# Subscriptions
# ------------------------------------------------------------------

class TestSubscriptions:
    def test_subscribe_unsubscribe(self, store):
        store.subscribe("a1", "channel:deploy")
        store.subscribe("a1", "channel:alerts")
        store.subscribe("a2", "channel:deploy")
        assert set(store.list_subscriptions("a1")) == {"channel:deploy", "channel:alerts"}
        assert store.list_subscribers("channel:deploy") == ["a1", "a2"]
        store.unsubscribe("a1", "channel:deploy")
        assert store.list_subscriptions("a1") == ["channel:alerts"]

    def test_duplicate_subscribe_ignored(self, store):
        store.subscribe("a1", "channel:deploy")
        store.subscribe("a1", "channel:deploy")
        assert store.list_subscriptions("a1") == ["channel:deploy"]
