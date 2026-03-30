"""Tests for MCP tool handlers."""

from __future__ import annotations

import pytest

from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.hub.tasks import TaskHub, InvalidTransition
from bifrost.mcp.tools import ToolHandlers
from bifrost.store.db import Store


@pytest.fixture()
def store(tmp_path):
    s = Store(tmp_path / "test.db")
    yield s
    s.close()


@pytest.fixture()
def hubs(store):
    return {
        "agents": AgentHub(store),
        "tasks": TaskHub(store),
        "conversations": ConversationHub(store),
        "delivery": DeliveryHub(store),
    }


@pytest.fixture()
def handlers(hubs):
    return ToolHandlers(**hubs)


@pytest.fixture()
def alice(hubs):
    return hubs["agents"].register("alice")


@pytest.fixture()
def bob(hubs):
    return hubs["agents"].register("bob")


# ------------------------------------------------------------------
# 1. handle_introduce
# ------------------------------------------------------------------

class TestHandleIntroduce:
    def test_with_introduction(self, handlers, alice):
        result = handlers.handle_introduce(alice.id, introduction="I help with code")
        assert "alice" in result
        assert "I help with code" in result

    def test_with_description(self, handlers, alice):
        result = handlers.handle_introduce(alice.id, description="A coding assistant")
        assert "A coding assistant" in result

    def test_with_skills(self, handlers, alice):
        skills = [{"name": "python", "description": "Python expertise"}]
        result = handlers.handle_introduce(alice.id, skills=skills)
        assert "alice" in result

    def test_with_limitations(self, handlers, alice):
        result = handlers.handle_introduce(alice.id, introduction="hi", limitations="no web")
        assert "alice" in result


# ------------------------------------------------------------------
# 2. handle_whoami
# ------------------------------------------------------------------

class TestHandleWhoami:
    def test_identity(self, handlers, alice):
        result = handlers.handle_whoami(alice.id)
        assert "alice" in result
        assert alice.id in result
        assert "online" in result

    def test_update_status(self, handlers, alice):
        result = handlers.handle_whoami(alice.id, status="idle")
        assert "idle" in result

    def test_dnd_with_reason(self, handlers, alice):
        result = handlers.handle_whoami(alice.id, status="dnd", dnd_reason="busy coding")
        assert "dnd" in result
        assert "busy coding" in result


# ------------------------------------------------------------------
# 3. handle_list_agents
# ------------------------------------------------------------------

class TestHandleListAgents:
    def test_empty(self, handlers):
        result = handlers.handle_list_agents()
        assert "No agents" in result

    def test_lists_agents(self, handlers, alice, bob):
        result = handlers.handle_list_agents()
        assert "alice" in result
        assert "bob" in result

    def test_filter_by_status(self, handlers, alice, bob, hubs):
        hubs["agents"].update_status(bob.id, status=hubs["agents"]._store.get_agent(bob.id).status)
        hubs["agents"].disconnect(bob.id)
        result = handlers.handle_list_agents(status="online")
        assert "alice" in result
        assert "bob" not in result


# ------------------------------------------------------------------
# 4. handle_request_task
# ------------------------------------------------------------------

class TestHandleRequestTask:
    def test_create_task(self, handlers, alice, bob):
        result = handlers.handle_request_task(
            requester_id=alice.id,
            assignee_name="bob",
        )
        assert "Task" in result
        assert "alice" in result
        assert "bob" in result
        assert "queued" in result

    def test_with_metadata(self, handlers, alice, bob):
        result = handlers.handle_request_task(
            requester_id=alice.id,
            assignee_name="bob",
            metadata={"priority": "high"},
        )
        assert "Task" in result

    def test_unknown_assignee(self, handlers, alice):
        with pytest.raises(KeyError):
            handlers.handle_request_task(
                requester_id=alice.id,
                assignee_name="nobody",
            )


# ------------------------------------------------------------------
# 5. handle_update_task
# ------------------------------------------------------------------

class TestHandleUpdateTask:
    def test_update_status(self, handlers, alice, bob, hubs):
        task = hubs["tasks"].create(requester=alice.id, assignee=bob.id)
        result = handlers.handle_update_task(task.id, status="running")
        assert "running" in result

    def test_update_artifacts(self, handlers, alice, bob, hubs):
        task = hubs["tasks"].create(requester=alice.id, assignee=bob.id)
        result = handlers.handle_update_task(
            task.id,
            artifacts=[{"text": "some output"}],
        )
        assert "updated" in result.lower()

    def test_invalid_transition(self, handlers, alice, bob, hubs):
        task = hubs["tasks"].create(requester=alice.id, assignee=bob.id)
        with pytest.raises(InvalidTransition):
            handlers.handle_update_task(task.id, status="completed")


# ------------------------------------------------------------------
# 6. handle_get_task
# ------------------------------------------------------------------

class TestHandleGetTask:
    def test_get_task(self, handlers, alice, bob, hubs):
        task = hubs["tasks"].create(requester=alice.id, assignee=bob.id)
        result = handlers.handle_get_task(task.id)
        assert task.id in result
        assert "alice" in result
        assert "bob" in result
        assert "queued" in result

    def test_not_found(self, handlers):
        with pytest.raises(KeyError):
            handlers.handle_get_task("nonexistent")


# ------------------------------------------------------------------
# 7. handle_list_tasks
# ------------------------------------------------------------------

class TestHandleListTasks:
    def test_empty(self, handlers):
        result = handlers.handle_list_tasks()
        assert "No tasks" in result

    def test_lists_tasks(self, handlers, alice, bob, hubs):
        hubs["tasks"].create(requester=alice.id, assignee=bob.id)
        result = handlers.handle_list_tasks()
        assert alice.id in result
        assert bob.id in result

    def test_filter_by_status(self, handlers, alice, bob, hubs):
        t = hubs["tasks"].create(requester=alice.id, assignee=bob.id)
        result = handlers.handle_list_tasks(status="queued")
        assert t.id in result

        result = handlers.handle_list_tasks(status="running")
        assert "No tasks" in result


# ------------------------------------------------------------------
# 8. handle_send
# ------------------------------------------------------------------

class TestHandleSend:
    def test_send_to_agent(self, handlers, alice, bob):
        result = handlers.handle_send(alice.id, to="bob", body="hello")
        assert "Message sent" in result
        assert "Conversation" in result

    def test_send_to_channel(self, handlers, alice):
        result = handlers.handle_send(alice.id, channel="general", body="hello all")
        assert "Message sent" in result

    def test_send_to_conversation(self, handlers, alice, bob):
        # First create a conversation
        result1 = handlers.handle_send(alice.id, to="bob", body="first")
        # Extract conversation id from result
        conv_id = result1.split("Conversation: ")[1].split("\n")[0]
        result2 = handlers.handle_send(alice.id, conversation_id=conv_id, body="second")
        assert "Message sent" in result2

    def test_no_target(self, handlers, alice):
        with pytest.raises(ValueError):
            handlers.handle_send(alice.id, body="to nobody")


# ------------------------------------------------------------------
# 9. handle_list_conversations
# ------------------------------------------------------------------

class TestHandleListConversations:
    def test_empty(self, handlers):
        result = handlers.handle_list_conversations()
        assert "No conversations" in result

    def test_lists_conversations(self, handlers, alice, bob):
        handlers.handle_send(alice.id, to="bob", body="hi")
        result = handlers.handle_list_conversations()
        assert alice.id in result

    def test_filter_by_channel(self, handlers, alice):
        handlers.handle_send(alice.id, channel="general", body="hi")
        result = handlers.handle_list_conversations(channel="general")
        assert "general" in result

        result = handlers.handle_list_conversations(channel="other")
        assert "No conversations" in result


# ------------------------------------------------------------------
# 10. handle_subscribe
# ------------------------------------------------------------------

class TestHandleSubscribe:
    def test_subscribe(self, handlers, alice):
        result = handlers.handle_subscribe(alice.id, "channel:general")
        assert "Subscribed" in result

    def test_subscribe_task(self, handlers, alice):
        result = handlers.handle_subscribe(alice.id, "task:abc123")
        assert "Subscribed" in result


# ------------------------------------------------------------------
# 11. handle_check
# ------------------------------------------------------------------

class TestHandleCheck:
    def test_no_pending(self, handlers, alice):
        result = handlers.handle_check(alice.id)
        assert "No pending" in result

    def test_receives_messages(self, handlers, alice, bob):
        handlers.handle_send(alice.id, to="bob", body="hey bob")
        result = handlers.handle_check(bob.id)
        assert "hey bob" in result

    def test_does_not_return_own_messages(self, handlers, alice, bob):
        handlers.handle_send(alice.id, to="bob", body="hi")
        result = handlers.handle_check(alice.id)
        assert "No pending" in result

    def test_channel_delivery(self, handlers, alice, bob, hubs):
        # Bob subscribes to general
        hubs["conversations"].subscribe(bob.id, "channel:general")
        # Alice sends to general
        handlers.handle_send(alice.id, channel="general", body="broadcast")
        result = handlers.handle_check(bob.id)
        assert "broadcast" in result
