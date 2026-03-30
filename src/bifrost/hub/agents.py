"""Hub layer for agent lifecycle management."""

from __future__ import annotations

from bifrost.store.db import Store
from bifrost.store.models import (
    Agent,
    AgentCard,
    AgentSkill,
    AgentStatus,
    _new_id,
    _now,
)


class AgentHub:
    """Manages agent registration, status, and cards."""

    def __init__(self, store: Store) -> None:
        self._store = store

    def register(self, name: str, oauth_subject: str | None = None) -> Agent:
        """Register or reconnect an agent. Sets status to ONLINE."""
        existing = self._store.get_agent_by_name(name)
        now = _now()
        if existing:
            existing.status = AgentStatus.ONLINE
            existing.connected_at = now
            existing.last_seen = now
            if oauth_subject is not None:
                existing.oauth_subject = oauth_subject
            self._store.upsert_agent(existing)
            existing.card = self._store.get_card(existing.id)
            return existing
        agent = Agent(
            id=_new_id(),
            name=name,
            status=AgentStatus.ONLINE,
            connected_at=now,
            last_seen=now,
            oauth_subject=oauth_subject,
        )
        self._store.upsert_agent(agent)
        return agent

    def rename(self, agent_id: str, new_name: str) -> Agent:
        """Rename an agent. Raises ValueError if name is taken by another agent."""
        existing = self._store.get_agent_by_name(new_name)
        if existing and existing.id != agent_id:
            raise ValueError(f"Name '{new_name}' is already taken")
        agent = self._store.get_agent(agent_id)
        if agent is None:
            raise KeyError(f"Agent not found: {agent_id}")
        agent.name = new_name
        self._store.upsert_agent(agent)
        return agent

    def disconnect(self, agent_id: str) -> None:
        """Mark an agent as OFFLINE."""
        agent = self._store.get_agent(agent_id)
        if agent is None:
            raise KeyError(f"Agent not found: {agent_id}")
        agent.status = AgentStatus.OFFLINE
        agent.last_seen = _now()
        self._store.upsert_agent(agent)

    def update_status(
        self, agent_id: str, status: AgentStatus, reason: str | None = None
    ) -> None:
        """Update agent status (and optional DND reason)."""
        agent = self._store.get_agent(agent_id)
        if agent is None:
            raise KeyError(f"Agent not found: {agent_id}")
        agent.status = status
        agent.dnd_reason = reason if status == AgentStatus.DND else None
        agent.last_seen = _now()
        self._store.upsert_agent(agent)

    def update_card(
        self,
        agent_id: str,
        *,
        introduction: str | None = None,
        description: str | None = None,
        version: str | None = None,
        icon_url: str | None = None,
        provider: str | None = None,
        capabilities: dict | None = None,
        skills: list[AgentSkill] | None = None,
        default_input_modes: list[str] | None = None,
        default_output_modes: list[str] | None = None,
        metadata: dict | None = None,
    ) -> AgentCard:
        """Create or update an agent's card.

        If *introduction* is provided but *description* is not, introduction
        text is stored as the description (freeform text shortcut).
        Explicit *description* always wins over *introduction*.
        """
        agent = self._store.get_agent(agent_id)
        if agent is None:
            raise KeyError(f"Agent not found: {agent_id}")

        existing = self._store.get_card(agent_id)
        card = existing or AgentCard(agent_id=agent_id)

        # Freeform text → description if no explicit description given
        if description is not None:
            card.description = description
        elif introduction is not None:
            card.description = introduction

        if version is not None:
            card.version = version
        if icon_url is not None:
            card.icon_url = icon_url
        if provider is not None:
            card.provider = provider
        if capabilities is not None:
            card.capabilities = capabilities
        if skills is not None:
            card.skills = skills
        if default_input_modes is not None:
            card.default_input_modes = default_input_modes
        if default_output_modes is not None:
            card.default_output_modes = default_output_modes
        if metadata is not None:
            card.metadata = metadata

        card.updated_at = _now()
        self._store.upsert_card(card)
        return card

    def get(self, agent_id: str) -> Agent:
        """Get an agent with its card attached."""
        agent = self._store.get_agent(agent_id)
        if agent is None:
            raise KeyError(f"Agent not found: {agent_id}")
        agent.card = self._store.get_card(agent_id)
        return agent

    def resolve(self, name: str) -> Agent:
        """Resolve an agent by name."""
        agent = self._store.get_agent_by_name(name)
        if agent is None:
            raise KeyError(f"Agent not found: {name}")
        agent.card = self._store.get_card(agent.id)
        return agent

    def list_all(self, status: AgentStatus | None = None) -> list[Agent]:
        """List all agents, optionally filtered by status, with cards attached."""
        agents = self._store.list_agents(status=status)
        for a in agents:
            a.card = self._store.get_card(a.id)
        return agents
