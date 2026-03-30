"""End-to-end integration tests exercising the full stack through ToolHandlers."""

from __future__ import annotations

import pytest

from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.hub.tasks import TaskHub
from bifrost.mcp.tools import ToolHandlers
from bifrost.store.db import Store


@pytest.fixture
def store(tmp_path):
    s = Store(tmp_path / "test.db")
    yield s
    s.close()


@pytest.fixture
def handlers(store):
    return ToolHandlers(
        agents=AgentHub(store),
        tasks=TaskHub(store),
        conversations=ConversationHub(store),
        delivery=DeliveryHub(store),
    )


# ---------------------------------------------------------------------------
# 1. Two agents exchange a message
# ---------------------------------------------------------------------------

def test_two_agents_message_exchange(handlers):
    alice = handlers._agents.register("alice")
    bob = handlers._agents.register("bob")

    # Alice sends to Bob
    handlers.handle_send(alice.id, to="bob", body="Hey Bob, how are you?")

    # Bob checks and sees the message
    result = handlers.handle_check(bob.id)
    assert "Hey Bob, how are you?" in result

    # Bob checks again — nothing new
    result2 = handlers.handle_check(bob.id)
    assert "No pending" in result2


# ---------------------------------------------------------------------------
# 2. Full task lifecycle: queued → running → completed
# ---------------------------------------------------------------------------

def test_task_lifecycle(handlers):
    lead = handlers._agents.register("lead")
    worker = handlers._agents.register("worker")

    # Lead creates a task for worker
    create_result = handlers.handle_request_task(
        requester_id=lead.id,
        assignee_name="worker",
    )
    assert "queued" in create_result
    task_id = create_result.split("Task ")[1].split(" created")[0]

    # Verify status is queued
    get_result = handlers.handle_get_task(task_id)
    assert "queued" in get_result

    # Worker accepts (running)
    running_result = handlers.handle_update_task(task_id, status="running")
    assert "running" in running_result

    get_result = handlers.handle_get_task(task_id)
    assert "running" in get_result

    # Worker completes
    completed_result = handlers.handle_update_task(task_id, status="completed")
    assert "completed" in completed_result

    get_result = handlers.handle_get_task(task_id)
    assert "completed" in get_result


# ---------------------------------------------------------------------------
# 3. Task rejection — terminal state
# ---------------------------------------------------------------------------

def test_task_rejection(handlers):
    requester = handlers._agents.register("requester")
    rejector = handlers._agents.register("rejector")

    create_result = handlers.handle_request_task(
        requester_id=requester.id,
        assignee_name="rejector",
    )
    task_id = create_result.split("Task ")[1].split(" created")[0]

    # Worker rejects directly from queued state
    reject_result = handlers.handle_update_task(task_id, status="rejected")
    assert "rejected" in reject_result

    # Verify terminal: cannot transition further
    from bifrost.hub.tasks import InvalidTransition
    with pytest.raises(InvalidTransition):
        handlers.handle_update_task(task_id, status="running")


# ---------------------------------------------------------------------------
# 4. Input-required loop: running → input-required → running → completed
# ---------------------------------------------------------------------------

def test_input_required_loop(handlers):
    lead = handlers._agents.register("lead_loop")
    worker = handlers._agents.register("worker_loop")

    create_result = handlers.handle_request_task(
        requester_id=lead.id,
        assignee_name="worker_loop",
    )
    task_id = create_result.split("Task ")[1].split(" created")[0]

    # running
    handlers.handle_update_task(task_id, status="running")

    # needs input
    ir_result = handlers.handle_update_task(task_id, status="input-required")
    assert "input-required" in ir_result

    # back to running
    handlers.handle_update_task(task_id, status="running")

    # complete
    done_result = handlers.handle_update_task(task_id, status="completed")
    assert "completed" in done_result


# ---------------------------------------------------------------------------
# 5. Channel broadcast — subscribed sees it, unsubscribed doesn't
# ---------------------------------------------------------------------------

def test_channel_broadcast(handlers):
    alice = handlers._agents.register("alice_ch")
    bob = handlers._agents.register("bob_ch")
    charlie = handlers._agents.register("charlie_ch")

    # Bob subscribes to "deploys", Charlie does not
    handlers.handle_subscribe(bob.id, "channel:deploys")

    # Alice publishes to "deploys" channel
    handlers.handle_send(alice.id, channel="deploys", body="v1.2.3 deployed")

    # Bob sees the broadcast
    bob_result = handlers.handle_check(bob.id)
    assert "v1.2.3 deployed" in bob_result

    # Charlie sees nothing
    charlie_result = handlers.handle_check(charlie.id)
    assert "No pending" in charlie_result


# ---------------------------------------------------------------------------
# 6. Agent discovery — list_agents shows both with descriptions
# ---------------------------------------------------------------------------

def test_agent_discovery(handlers):
    alpha = handlers._agents.register("alpha")
    beta = handlers._agents.register("beta")

    # Each introduces themselves with different skills/descriptions
    handlers.handle_introduce(
        alpha.id,
        description="Alpha: handles data pipelines",
        skills=[{"name": "etl", "description": "ETL expert"}],
    )
    handlers.handle_introduce(
        beta.id,
        description="Beta: manages deployments",
        skills=[{"name": "deploy", "description": "Deployment specialist"}],
    )

    result = handlers.handle_list_agents()
    assert "alpha" in result
    assert "beta" in result
    assert "Alpha: handles data pipelines" in result
    assert "Beta: manages deployments" in result


# ---------------------------------------------------------------------------
# 7. DND status — agent sets DND, verify status changes
# ---------------------------------------------------------------------------

def test_dnd_status(handlers):
    agent = handlers._agents.register("dnd_agent")

    # Initially online
    whoami = handlers.handle_whoami(agent.id)
    assert "online" in whoami

    # Set DND with a reason
    dnd_result = handlers.handle_whoami(
        agent.id, status="dnd", dnd_reason="deep focus session"
    )
    assert "dnd" in dnd_result
    assert "deep focus session" in dnd_result

    # Verify persisted — read back via list_agents
    list_result = handlers.handle_list_agents(status="dnd")
    assert "dnd_agent" in list_result


# ---------------------------------------------------------------------------
# 8. Conversation reuse — multiple messages in the same conversation
# ---------------------------------------------------------------------------

def test_conversation_reuse(handlers):
    alice = handlers._agents.register("alice_reuse")
    bob = handlers._agents.register("bob_reuse")

    # First message creates a conversation
    first = handlers.handle_send(alice.id, to="bob_reuse", body="message one")
    conv_id = first.split("Conversation: ")[1].split("\n")[0]

    # Second message reuses the same conversation
    second = handlers.handle_send(alice.id, conversation_id=conv_id, body="message two")
    assert conv_id in second

    # Third message also in the same conversation
    third = handlers.handle_send(bob.id, conversation_id=conv_id, body="message three from bob")
    assert conv_id in third

    # Bob receives both of Alice's messages (not his own)
    # (already marked delivered via handle_check; reset by sending new msg)
    # Check that only Alice's messages end up delivered to Bob once
    bob_check = handlers.handle_check(bob.id)
    assert "message one" in bob_check
    assert "message two" in bob_check
    # Bob's own message is excluded
    assert "message three from bob" not in bob_check

    # Alice receives Bob's reply
    alice_check = handlers.handle_check(alice.id)
    assert "message three from bob" in alice_check


# ---------------------------------------------------------------------------
# 9. Multiple conversations — check returns all pending across conversations
# ---------------------------------------------------------------------------

def test_multiple_conversations(handlers):
    alice = handlers._agents.register("alice_multi")
    bob = handlers._agents.register("bob_multi")
    carol = handlers._agents.register("carol_multi")

    # Alice sends to Bob in one conversation
    handlers.handle_send(alice.id, to="bob_multi", body="hi from alice to bob")

    # Carol sends to Bob in a separate conversation
    handlers.handle_send(carol.id, to="bob_multi", body="hi from carol to bob")

    # Bob checks — should see messages from both conversations
    result = handlers.handle_check(bob.id)
    assert "hi from alice to bob" in result
    assert "hi from carol to bob" in result

    # Checking again yields nothing new
    result2 = handlers.handle_check(bob.id)
    assert "No pending" in result2
