"""Hub layer for event delivery tracking."""

from __future__ import annotations

from bifrost.store.db import Store
from bifrost.store.models import Event


class DeliveryHub:
    """Tracks and delivers unread events to agents."""

    def __init__(self, store: Store) -> None:
        self._store = store

    def check(self, agent_id: str) -> list[Event]:
        """Return undelivered events for an agent, advancing delivery state.

        Checks:
        1. Conversations where agent is a participant
        2. Channel conversations where agent is subscribed

        Excludes the agent's own messages. Sorts by timestamp.
        """
        seen_conv_ids: set[str] = set()
        all_events: list[Event] = []

        # 1. Direct conversations where agent is a participant
        conversations = self._store.list_conversations()
        for conv in conversations:
            if agent_id in conv.participants:
                seen_conv_ids.add(conv.id)
                events = self._get_undelivered(agent_id, conv.id)
                all_events.extend(events)

        # 2. Channel subscriptions
        subscriptions = self._store.list_subscriptions(agent_id)
        for target in subscriptions:
            # target format: "channel:name"
            if target.startswith("channel:"):
                channel_name = target[len("channel:"):]
                channel_convs = self._store.list_conversations(channel=channel_name)
                for conv in channel_convs:
                    if conv.id not in seen_conv_ids:
                        seen_conv_ids.add(conv.id)
                        events = self._get_undelivered(agent_id, conv.id)
                        all_events.extend(events)

        # Exclude own messages
        all_events = [e for e in all_events if e.from_agent != agent_id]

        # Sort by timestamp
        all_events.sort(key=lambda e: e.timestamp)

        # Advance delivery state for each conversation
        conv_last: dict[str, Event] = {}
        for e in all_events:
            conv_last[e.conversation_id] = e
        for conv_id, last_event in conv_last.items():
            self._store.set_last_delivered_event(agent_id, conv_id, last_event.id)

        return all_events

    def _get_undelivered(self, agent_id: str, conversation_id: str) -> list[Event]:
        """Get events after the last delivered event for this agent+conversation."""
        last_id = self._store.get_last_delivered_event(agent_id, conversation_id)
        return self._store.list_events(conversation_id, after_event_id=last_id)
