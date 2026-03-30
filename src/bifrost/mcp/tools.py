"""MCP tool handlers — thin wrappers around hub logic."""

from __future__ import annotations

from typing import Any

from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.hub.tasks import TaskHub
from bifrost.store.models import AgentStatus, Artifact, TaskStatus, TextPart


class ToolHandlers:
    """One method per MCP tool. Each takes parsed arguments + caller agent_id."""

    def __init__(
        self,
        agents: AgentHub,
        tasks: TaskHub,
        conversations: ConversationHub,
        delivery: DeliveryHub,
    ) -> None:
        self._agents = agents
        self._tasks = tasks
        self._conversations = conversations
        self._delivery = delivery

    # ------------------------------------------------------------------
    # 1. bifrost_introduce
    # ------------------------------------------------------------------

    def handle_introduce(
        self,
        agent_id: str,
        introduction: str | None = None,
        description: str | None = None,
        skills: list[dict[str, Any]] | None = None,
        limitations: str | None = None,
    ) -> str:
        """Update the caller's agent card."""
        from bifrost.store.models import AgentSkill

        skill_objs = None
        if skills is not None:
            skill_objs = [AgentSkill(**s) for s in skills]

        metadata: dict[str, Any] | None = None
        if limitations is not None:
            metadata = {"limitations": limitations}

        card = self._agents.update_card(
            agent_id,
            introduction=introduction,
            description=description,
            skills=skill_objs,
            metadata=metadata,
        )

        agent = self._agents.get(agent_id)
        return f"Updated card for {agent.name}. Description: {card.description}"

    # ------------------------------------------------------------------
    # 2. bifrost_whoami
    # ------------------------------------------------------------------

    def handle_whoami(
        self,
        agent_id: str,
        status: str | None = None,
        dnd_reason: str | None = None,
    ) -> str:
        """Return identity or update status."""
        agent = self._agents.get(agent_id)

        if status is not None:
            agent_status = AgentStatus(status)
            self._agents.update_status(agent_id, agent_status, reason=dnd_reason)
            agent = self._agents.get(agent_id)

        lines = [
            f"Name: {agent.name}",
            f"ID: {agent.id}",
            f"Status: {agent.status}",
        ]
        if agent.dnd_reason:
            lines.append(f"DND reason: {agent.dnd_reason}")
        if agent.card and agent.card.description:
            lines.append(f"Description: {agent.card.description}")
        return "\n".join(lines)

    # ------------------------------------------------------------------
    # 3. bifrost_list_agents
    # ------------------------------------------------------------------

    def handle_list_agents(self, status: str | None = None) -> str:
        """List agents, optionally filtered by status."""
        agent_status = AgentStatus(status) if status else None
        agents = self._agents.list_all(status=agent_status)

        if not agents:
            return "No agents found."

        lines: list[str] = []
        for a in agents:
            desc = ""
            if a.card and a.card.description:
                desc = f" — {a.card.description}"
            lines.append(f"- {a.name} [{a.status}]{desc}")
        return "\n".join(lines)

    # ------------------------------------------------------------------
    # 4. bifrost_request_task
    # ------------------------------------------------------------------

    def handle_request_task(
        self,
        requester_id: str,
        assignee_name: str,
        context_id: str | None = None,
        metadata: dict[str, Any] | None = None,
    ) -> str:
        """Create a new task assigned to another agent."""
        assignee = self._agents.resolve(assignee_name)
        task = self._tasks.create(
            requester=requester_id,
            assignee=assignee.id,
            context_id=context_id,
            metadata=metadata,
        )
        requester = self._agents.get(requester_id)
        return (
            f"Task {task.id} created.\n"
            f"Requester: {requester.name}\n"
            f"Assignee: {assignee.name}\n"
            f"Status: {task.status}"
        )

    # ------------------------------------------------------------------
    # 5. bifrost_update_task
    # ------------------------------------------------------------------

    def handle_update_task(
        self,
        task_id: str,
        status: str | None = None,
        artifacts: list[dict[str, Any]] | None = None,
    ) -> str:
        """Update a task's status and/or artifacts."""
        task = self._tasks.get(task_id)

        if artifacts is not None:
            artifact_objs = [
                Artifact(
                    parts=[TextPart(text=a["text"])] if "text" in a else [],
                    metadata=a.get("metadata", {}),
                )
                for a in artifacts
            ]
            task = self._tasks.update_artifacts(task_id, artifact_objs)

        if status is not None:
            task_status = TaskStatus(status)
            task = self._tasks.update_status(task_id, task_status)

        return f"Task {task.id} updated. Status: {task.status}"

    # ------------------------------------------------------------------
    # 6. bifrost_get_task
    # ------------------------------------------------------------------

    def handle_get_task(self, task_id: str) -> str:
        """Return task details."""
        task = self._tasks.get(task_id)
        requester = self._agents.get(task.requester)
        assignee = self._agents.get(task.assignee)
        lines = [
            f"Task: {task.id}",
            f"Status: {task.status}",
            f"Requester: {requester.name}",
            f"Assignee: {assignee.name}",
            f"Created: {task.created_at}",
            f"Updated: {task.updated_at}",
        ]
        if task.context_id:
            lines.append(f"Context: {task.context_id}")
        if task.artifacts:
            lines.append(f"Artifacts: {len(task.artifacts)}")
        if task.metadata:
            lines.append(f"Metadata: {task.metadata}")
        return "\n".join(lines)

    # ------------------------------------------------------------------
    # 7. bifrost_list_tasks
    # ------------------------------------------------------------------

    def handle_list_tasks(
        self,
        status: str | None = None,
        requester: str | None = None,
        assignee: str | None = None,
    ) -> str:
        """List tasks with optional filters."""
        task_status = TaskStatus(status) if status else None
        tasks = self._tasks.list(
            status=task_status,
            requester=requester,
            assignee=assignee,
        )

        if not tasks:
            return "No tasks found."

        lines: list[str] = []
        for t in tasks:
            lines.append(f"- {t.id} [{t.status}] {t.requester} -> {t.assignee}")
        return "\n".join(lines)

    # ------------------------------------------------------------------
    # 8. bifrost_send
    # ------------------------------------------------------------------

    def handle_send(
        self,
        from_agent_id: str,
        to: str | None = None,
        channel: str | None = None,
        conversation_id: str | None = None,
        body: str | None = None,
    ) -> str:
        """Send a message."""
        # Resolve to_agent name -> id if provided
        to_agent_id: str | None = None
        if to:
            target = self._agents.resolve(to)
            to_agent_id = target.id

        conv, event = self._conversations.send(
            from_agent_id,
            to_agent=to_agent_id,
            channel=channel,
            conversation_id=conversation_id,
            text=body,
        )
        return (
            f"Message sent.\n"
            f"Conversation: {conv.id}\n"
            f"Event: {event.id}"
        )

    # ------------------------------------------------------------------
    # 9. bifrost_list_conversations
    # ------------------------------------------------------------------

    def handle_list_conversations(self, channel: str | None = None) -> str:
        """List conversations, optionally filtered by channel."""
        convs = self._conversations.list(channel=channel, active_only=True)

        if not convs:
            return "No conversations found."

        lines: list[str] = []
        for c in convs:
            ch = f" [#{c.channel}]" if c.channel else ""
            parts = ", ".join(c.participants)
            lines.append(f"- {c.id}{ch} ({parts})")
        return "\n".join(lines)

    # ------------------------------------------------------------------
    # 10. bifrost_subscribe
    # ------------------------------------------------------------------

    def handle_subscribe(self, agent_id: str, target: str) -> str:
        """Subscribe the agent to a channel or task target."""
        self._conversations.subscribe(agent_id, target)
        return f"Subscribed to {target}."

    # ------------------------------------------------------------------
    # 11. bifrost_check
    # ------------------------------------------------------------------

    def handle_check(self, agent_id: str) -> str:
        """Return pending (undelivered) events for the agent."""
        events = self._delivery.check(agent_id)

        if not events:
            return "No pending events."

        lines: list[str] = []
        for e in events:
            text = e.data.get("text", "")
            preview = text[:80] if text else f"[{e.type}]"
            lines.append(f"[{e.conversation_id}] {e.from_agent}: {preview}")
        return "\n".join(lines)
