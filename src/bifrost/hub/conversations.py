"""Hub layer for conversation management."""

from __future__ import annotations

from bifrost.store.db import Store
from bifrost.store.models import (
    Conversation,
    Event,
    EventType,
    _new_id,
    _now,
)


class ConversationHub:
    """Manages conversations, events, and subscriptions."""

    def __init__(self, store: Store) -> None:
        self._store = store

    def send(
        self,
        from_agent: str,
        *,
        to_agent: str | None = None,
        channel: str | None = None,
        conversation_id: str | None = None,
        text: str | None = None,
    ) -> tuple[Conversation, Event]:
        """Send a message, creating a conversation if needed.

        - If *conversation_id* is given, reuses that conversation.
        - If *channel* is given (no conversation_id), creates a channel
          conversation with id like "channel:abc123".
        - If *to_agent* is given (no channel, no conversation_id), creates
          a DM conversation between from_agent and to_agent.
        """
        if conversation_id:
            conv = self._store.get_conversation(conversation_id)
            if conv is None:
                raise KeyError(f"Conversation not found: {conversation_id}")
            # Ensure sender is a participant
            if from_agent not in conv.participants:
                conv.participants.append(from_agent)
                self._store.update_conversation(conv.id, participants=conv.participants)
        elif channel:
            conv_id = f"{channel}:{_new_id()}"
            participants = [from_agent]
            conv = Conversation(
                id=conv_id,
                title=channel,
                channel=channel,
                participants=participants,
            )
            self._store.create_conversation(conv)
        elif to_agent:
            participants = [from_agent, to_agent]
            conv = Conversation(
                id=_new_id(),
                title="",
                participants=participants,
            )
            self._store.create_conversation(conv)
        else:
            raise ValueError("Must provide to_agent, channel, or conversation_id")

        event = Event(
            id=_new_id(),
            conversation_id=conv.id,
            type=EventType.MESSAGE,
            from_agent=from_agent,
            data={"text": text} if text else {},
            timestamp=_now(),
        )
        self._store.append_event(event)
        return conv, event

    def get_events(
        self, conversation_id: str, after_event_id: str | None = None
    ) -> list[Event]:
        """Get events for a conversation, optionally after a given event."""
        return self._store.list_events(conversation_id, after_event_id=after_event_id)

    def list(
        self, channel: str | None = None, active_only: bool = False
    ) -> list[Conversation]:
        """List conversations with optional filters."""
        return self._store.list_conversations(channel=channel, active_only=active_only)

    def subscribe(self, agent_id: str, target: str) -> None:
        """Subscribe an agent to a channel or task target."""
        self._store.subscribe(agent_id, target)

    def unsubscribe(self, agent_id: str, target: str) -> None:
        """Unsubscribe an agent from a target."""
        self._store.unsubscribe(agent_id, target)
