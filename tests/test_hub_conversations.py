"""Tests for the ConversationHub."""

import pytest

from bifrost.hub.conversations import ConversationHub
from bifrost.store.db import Store
from bifrost.store.models import EventType


@pytest.fixture
def hub(tmp_path):
    store = Store(tmp_path / "test.db")
    yield ConversationHub(store)
    store.close()


class TestSend:
    def test_dm_creates_conversation(self, hub):
        conv, event = hub.send("a1", to_agent="a2", text="hello")
        assert "a1" in conv.participants
        assert "a2" in conv.participants
        assert event.type == EventType.MESSAGE
        assert event.data == {"text": "hello"}
        assert event.from_agent == "a1"

    def test_channel_creates_conversation(self, hub):
        conv, event = hub.send("a1", channel="deploy", text="deployed v2")
        assert conv.channel == "deploy"
        assert conv.id.startswith("deploy:")
        assert "a1" in conv.participants

    def test_reuse_conversation(self, hub):
        conv1, _ = hub.send("a1", to_agent="a2", text="hi")
        conv2, event2 = hub.send("a2", conversation_id=conv1.id, text="hey back")
        assert conv2.id == conv1.id
        assert event2.from_agent == "a2"

    def test_reuse_adds_participant(self, hub):
        conv1, _ = hub.send("a1", to_agent="a2", text="hi")
        hub.send("a3", conversation_id=conv1.id, text="joining")
        events = hub.get_events(conv1.id)
        assert len(events) == 2

    def test_missing_conversation(self, hub):
        with pytest.raises(KeyError):
            hub.send("a1", conversation_id="nope", text="hi")

    def test_no_target_raises(self, hub):
        with pytest.raises(ValueError):
            hub.send("a1", text="hi")

    def test_empty_text(self, hub):
        conv, event = hub.send("a1", to_agent="a2")
        assert event.data == {}


class TestGetEvents:
    def test_all_events(self, hub):
        conv, _ = hub.send("a1", to_agent="a2", text="msg1")
        hub.send("a2", conversation_id=conv.id, text="msg2")
        events = hub.get_events(conv.id)
        assert len(events) == 2

    def test_after_event(self, hub):
        conv, e1 = hub.send("a1", to_agent="a2", text="msg1")
        hub.send("a2", conversation_id=conv.id, text="msg2")
        events = hub.get_events(conv.id, after_event_id=e1.id)
        assert len(events) == 1
        assert events[0].data == {"text": "msg2"}


class TestList:
    def test_list_all(self, hub):
        hub.send("a1", to_agent="a2", text="hi")
        hub.send("a1", channel="deploy", text="v2")
        assert len(hub.list()) == 2

    def test_list_by_channel(self, hub):
        hub.send("a1", to_agent="a2", text="hi")
        hub.send("a1", channel="deploy", text="v2")
        assert len(hub.list(channel="deploy")) == 1


class TestSubscriptions:
    def test_subscribe_unsubscribe(self, hub):
        hub.subscribe("a1", "channel:deploy")
        hub.subscribe("a1", "channel:alerts")
        hub.unsubscribe("a1", "channel:deploy")
        # Verify via store (subscribe/unsubscribe are thin wrappers)
        subs = hub._store.list_subscriptions("a1")
        assert subs == ["channel:alerts"]
