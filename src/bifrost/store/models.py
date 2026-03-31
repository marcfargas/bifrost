"""Bifrost v2 data models — A2A-inspired dataclasses."""

from __future__ import annotations

import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import StrEnum
from typing import Any


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _new_id() -> str:
    return uuid.uuid4().hex[:12]


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


# ---------------------------------------------------------------------------
# Enums
# ---------------------------------------------------------------------------

class AgentStatus(StrEnum):
    ONLINE = "online"
    IDLE = "idle"
    OFFLINE = "offline"
    DND = "dnd"


class TaskStatus(StrEnum):
    QUEUED = "queued"
    RUNNING = "running"
    INPUT_REQUIRED = "input-required"
    AUTH_REQUIRED = "auth-required"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELED = "canceled"
    REJECTED = "rejected"


class EventType(StrEnum):
    MESSAGE = "message"
    METADATA = "metadata"
    FILE = "file"
    PARTICIPANT_ADDED = "participant.added"
    PARTICIPANT_REMOVED = "participant.removed"


# ---------------------------------------------------------------------------
# Part types (A2A content model)
# ---------------------------------------------------------------------------

@dataclass
class TextPart:
    text: str
    type: str = "text"


@dataclass
class FilePart:
    mime_type: str
    uri: str | None = None
    data: str | None = None
    type: str = "file"


@dataclass
class DataPart:
    data: dict[str, Any] = field(default_factory=dict)
    type: str = "data"


# ---------------------------------------------------------------------------
# A2A types
# ---------------------------------------------------------------------------

@dataclass
class Artifact:
    id: str = field(default_factory=_new_id)
    parts: list[TextPart | FilePart | DataPart] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)


@dataclass
class AgentSkill:
    id: str = ""
    name: str = ""
    description: str = ""
    tags: list[str] = field(default_factory=list)
    examples: list[str] = field(default_factory=list)
    input_modes: list[str] = field(default_factory=list)
    output_modes: list[str] = field(default_factory=list)


@dataclass
class AgentCard:
    agent_id: str = ""
    description: str = ""
    version: str = ""
    icon_url: str = ""
    provider: str = ""
    capabilities: dict[str, Any] = field(default_factory=dict)
    skills: list[AgentSkill] = field(default_factory=list)
    default_input_modes: list[str] = field(default_factory=list)
    default_output_modes: list[str] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)
    updated_at: str = field(default_factory=_now)


# ---------------------------------------------------------------------------
# Core entities
# ---------------------------------------------------------------------------

@dataclass
class Agent:
    id: str = field(default_factory=_new_id)
    name: str = ""
    status: AgentStatus = AgentStatus.OFFLINE
    dnd_reason: str | None = None
    connected_at: str | None = None
    last_seen: str | None = None
    oauth_subject: str | None = None
    card: AgentCard | None = None
    is_human: bool = False


@dataclass
class Task:
    id: str = field(default_factory=_new_id)
    requester: str = ""
    assignee: str = ""
    status: TaskStatus = TaskStatus.QUEUED
    context_id: str | None = None
    status_message: str | None = None
    artifacts: list[Artifact] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)
    created_at: str = field(default_factory=_now)
    updated_at: str = field(default_factory=_now)


@dataclass
class Conversation:
    id: str = field(default_factory=_new_id)
    title: str = ""
    channel: str | None = None
    participants: list[str] = field(default_factory=list)
    closed: bool = False
    closed_reason: str | None = None
    created_at: str = field(default_factory=_now)


@dataclass
class Event:
    id: str = field(default_factory=_new_id)
    conversation_id: str = ""
    type: EventType = EventType.MESSAGE
    from_agent: str = ""
    data: dict[str, Any] = field(default_factory=dict)
    timestamp: str = field(default_factory=_now)
