"""Tests for bifrost data models."""

from bifrost.store.models import (
    Agent,
    AgentCard,
    AgentSkill,
    AgentStatus,
    Artifact,
    Conversation,
    DataPart,
    Event,
    EventType,
    FilePart,
    Task,
    TaskStatus,
    TextPart,
)


# ---------------------------------------------------------------------------
# Enums
# ---------------------------------------------------------------------------

class TestAgentStatus:
    def test_values(self):
        assert AgentStatus.ONLINE == "online"
        assert AgentStatus.IDLE == "idle"
        assert AgentStatus.OFFLINE == "offline"
        assert AgentStatus.DND == "dnd"

    def test_is_str(self):
        assert isinstance(AgentStatus.ONLINE, str)


class TestTaskStatus:
    def test_values(self):
        assert TaskStatus.QUEUED == "queued"
        assert TaskStatus.RUNNING == "running"
        assert TaskStatus.INPUT_REQUIRED == "input-required"
        assert TaskStatus.AUTH_REQUIRED == "auth-required"
        assert TaskStatus.COMPLETED == "completed"
        assert TaskStatus.FAILED == "failed"
        assert TaskStatus.CANCELED == "canceled"
        assert TaskStatus.REJECTED == "rejected"

    def test_is_str(self):
        assert isinstance(TaskStatus.QUEUED, str)


class TestEventType:
    def test_values(self):
        assert EventType.MESSAGE == "message"
        assert EventType.METADATA == "metadata"
        assert EventType.FILE == "file"
        assert EventType.PARTICIPANT_ADDED == "participant.added"
        assert EventType.PARTICIPANT_REMOVED == "participant.removed"


# ---------------------------------------------------------------------------
# Part types
# ---------------------------------------------------------------------------

class TestTextPart:
    def test_creation(self):
        p = TextPart(text="hello")
        assert p.text == "hello"
        assert p.type == "text"


class TestFilePart:
    def test_defaults(self):
        p = FilePart(mime_type="image/png")
        assert p.mime_type == "image/png"
        assert p.uri is None
        assert p.data is None
        assert p.type == "file"


class TestDataPart:
    def test_defaults(self):
        p = DataPart()
        assert p.data == {}
        assert p.type == "data"


# ---------------------------------------------------------------------------
# A2A types
# ---------------------------------------------------------------------------

class TestArtifact:
    def test_defaults(self):
        a = Artifact()
        assert len(a.id) == 12
        assert a.parts == []
        assert a.metadata == {}

    def test_with_parts(self):
        a = Artifact(parts=[TextPart("hi")])
        assert len(a.parts) == 1


class TestAgentSkill:
    def test_defaults(self):
        s = AgentSkill()
        assert s.tags == []
        assert s.examples == []


class TestAgentCard:
    def test_defaults(self):
        c = AgentCard(agent_id="a1", description="test")
        assert c.agent_id == "a1"
        assert c.skills == []
        assert c.updated_at  # non-empty


# ---------------------------------------------------------------------------
# Core entities
# ---------------------------------------------------------------------------

class TestAgent:
    def test_defaults(self):
        a = Agent()
        assert len(a.id) == 12
        assert a.status == AgentStatus.OFFLINE
        assert a.card is None
        assert a.dnd_reason is None

    def test_custom(self):
        a = Agent(name="bot", status=AgentStatus.ONLINE)
        assert a.name == "bot"
        assert a.status == "online"


class TestTask:
    def test_defaults(self):
        t = Task()
        assert t.status == TaskStatus.QUEUED
        assert t.artifacts == []
        assert t.metadata == {}
        assert t.created_at
        assert t.updated_at

    def test_no_shared_defaults(self):
        t1 = Task()
        t2 = Task()
        assert t1.artifacts is not t2.artifacts
        assert t1.metadata is not t2.metadata


class TestConversation:
    def test_defaults(self):
        c = Conversation()
        assert len(c.id) == 12
        assert c.closed is False
        assert c.participants == []

    def test_no_shared_defaults(self):
        c1 = Conversation()
        c2 = Conversation()
        assert c1.participants is not c2.participants


class TestEvent:
    def test_defaults(self):
        e = Event()
        assert len(e.id) == 12
        assert e.type == EventType.MESSAGE
        assert e.data == {}
        assert e.timestamp
