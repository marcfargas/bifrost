"""Tests for the DeliveryHub."""

import pytest

from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.store.db import Store


@pytest.fixture
def store(tmp_path):
    s = Store(tmp_path / "test.db")
    yield s
    s.close()


@pytest.fixture
def conv_hub(store):
    return ConversationHub(store)


@pytest.fixture
def delivery(store):
    return DeliveryHub(store)


class TestCheck:
    def test_delivers_unread_messages(self, conv_hub, delivery):
        conv, _ = conv_hub.send("a1", to_agent="a2", text="hello")
        conv_hub.send("a1", conversation_id=conv.id, text="ping")
        events = delivery.check("a2")
        assert len(events) == 2
        assert events[0].data == {"text": "hello"}
        assert events[1].data == {"text": "ping"}

    def test_excludes_own_messages(self, conv_hub, delivery):
        conv, _ = conv_hub.send("a1", to_agent="a2", text="hello")
        conv_hub.send("a2", conversation_id=conv.id, text="hi back")
        events = delivery.check("a2")
        # a2 should only see a1's message, not their own
        assert len(events) == 1
        assert events[0].data == {"text": "hello"}

    def test_advances_delivery_state(self, conv_hub, delivery):
        conv, _ = conv_hub.send("a1", to_agent="a2", text="msg1")
        events1 = delivery.check("a2")
        assert len(events1) == 1

        # Second check — nothing new
        events2 = delivery.check("a2")
        assert len(events2) == 0

        # New message arrives
        conv_hub.send("a1", conversation_id=conv.id, text="msg2")
        events3 = delivery.check("a2")
        assert len(events3) == 1
        assert events3[0].data == {"text": "msg2"}

    def test_channel_subscription_delivery(self, conv_hub, delivery, store):
        # a2 subscribes to channel:deploy
        store.subscribe("a2", "channel:deploy")
        # a1 sends to the deploy channel
        conv, _ = conv_hub.send("a1", channel="deploy", text="deployed v3")
        events = delivery.check("a2")
        assert len(events) == 1
        assert events[0].data == {"text": "deployed v3"}

    def test_no_duplicates_participant_and_subscriber(self, conv_hub, delivery, store):
        # a2 subscribes to channel:deploy AND is a participant
        store.subscribe("a2", "channel:deploy")
        conv, _ = conv_hub.send("a1", channel="deploy", text="v3")
        # Make a2 a participant too
        conv_hub.send("a2", conversation_id=conv.id, text="ack")
        events = delivery.check("a2")
        # Should get a1's message once (not duplicated), own message excluded
        assert len(events) == 1
        assert events[0].data == {"text": "v3"}

    def test_empty_when_no_conversations(self, delivery):
        events = delivery.check("a1")
        assert events == []

    def test_sorted_by_timestamp(self, conv_hub, delivery):
        conv1, _ = conv_hub.send("a1", to_agent="a2", text="first")
        conv2, _ = conv_hub.send("a3", to_agent="a2", text="second")
        events = delivery.check("a2")
        assert len(events) == 2
        assert events[0].timestamp <= events[1].timestamp

    def test_multiple_conversations(self, conv_hub, delivery):
        conv1, _ = conv_hub.send("a1", to_agent="a2", text="from a1")
        conv2, _ = conv_hub.send("a3", to_agent="a2", text="from a3")
        events = delivery.check("a2")
        assert len(events) == 2
        senders = {e.from_agent for e in events}
        assert senders == {"a1", "a3"}
