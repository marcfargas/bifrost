# Bifrost v2 — Core Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the bifrost v2 MCP server with agent registry, task lifecycle, messaging, channels, delivery tracking, and OAuth authentication.

**Architecture:** FastAPI app mounting low-level MCP Server at `/mcp`. Business logic in `hub/`, persistence in `store/`, MCP tool handlers in `mcp/`. SQLite for storage. OAuth 2.0 via authlib (or `--insecure` for local dev).

**Tech Stack:** Python 3.12+, `mcp` SDK (low-level Server), FastAPI, SQLite, authlib, uvicorn

**Spec:** `docs/superpowers/specs/2026-03-30-bifrost-v2-design.md`

**Prerequisite:** Spikes must pass. See `docs/superpowers/plans/2026-03-30-bifrost-v2-spikes.md`.

---

### Task 1: Delete Go v1, Restructure Project

**Files:**
- Delete: `cmd/`, `pkg/`, `internal/`, `plugin/`, `test/`, `go.mod`, `go.sum`, `DESIGN.md`, `ROADMAP.md`
- Keep: `docs/`, `LICENSE`, `README.md`, `pyproject.toml`, `src/`, `tests/`
- Create: `src/bifrost/hub/__init__.py`
- Create: `src/bifrost/store/__init__.py`
- Create: `src/bifrost/mcp/__init__.py`
- Create: `src/bifrost/auth/__init__.py`
- Create: `src/bifrost/dashboard/__init__.py`
- Create: `src/bifrost/a2a/__init__.py`

- [ ] **Step 1: Delete v1 Go code**

```bash
cd /home/marc/dev/bifrost
rm -rf cmd/ pkg/ internal/ plugin/ test/ go.mod go.sum DESIGN.md ROADMAP.md
```

- [ ] **Step 2: Remove spike files**

```bash
rm -f src/bifrost/spike_mcp.py src/bifrost/spike_oauth.py tests/test_spike_mcp.py tests/test_spike_oauth.py
```

- [ ] **Step 3: Create package directories**

```bash
mkdir -p src/bifrost/{hub,store,mcp,auth,dashboard,a2a}
touch src/bifrost/{hub,store,mcp,auth,dashboard,a2a}/__init__.py
mkdir -p src/bifrost/dashboard/{templates,static}
mkdir -p tests
touch tests/__init__.py tests/conftest.py
```

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "chore: delete Go v1, restructure for Python v2"
```

---

### Task 2: Data Models

**Files:**
- Create: `src/bifrost/store/models.py`
- Create: `tests/test_models.py`

- [ ] **Step 1: Write tests for data models**

```python
# tests/test_models.py
"""Tests for A2A-inspired data models."""
from bifrost.store.models import (
    Agent,
    AgentCard,
    AgentSkill,
    AgentStatus,
    Artifact,
    Conversation,
    Event,
    EventType,
    Task,
    TaskStatus,
    TextPart,
)


def test_agent_creation():
    agent = Agent(id="a1", name="api-backend")
    assert agent.id == "a1"
    assert agent.name == "api-backend"
    assert agent.status == AgentStatus.OFFLINE


def test_agent_card_with_skills():
    skill = AgentSkill(
        id="code-review",
        name="Code Review",
        description="Reviews code for quality",
        tags=["review", "quality"],
    )
    card = AgentCard(
        agent_id="a1",
        description="Backend API agent",
        skills=[skill],
    )
    assert len(card.skills) == 1
    assert card.skills[0].id == "code-review"


def test_task_status_values():
    """A2A task states."""
    assert TaskStatus.QUEUED == "queued"
    assert TaskStatus.RUNNING == "running"
    assert TaskStatus.INPUT_REQUIRED == "input-required"
    assert TaskStatus.AUTH_REQUIRED == "auth-required"
    assert TaskStatus.COMPLETED == "completed"
    assert TaskStatus.FAILED == "failed"
    assert TaskStatus.CANCELED == "canceled"
    assert TaskStatus.REJECTED == "rejected"


def test_task_creation():
    task = Task(
        id="t1",
        requester="a1",
        assignee="a2",
        status=TaskStatus.QUEUED,
    )
    assert task.status == TaskStatus.QUEUED
    assert task.artifacts == []


def test_conversation_with_channel():
    conv = Conversation(id="deploys:abc123", channel="deploys", title="Deploy v2.1")
    assert conv.channel == "deploys"
    assert not conv.closed


def test_conversation_without_channel():
    conv = Conversation(id="abc123", title="Quick question")
    assert conv.channel is None


def test_event_with_text_part():
    event = Event(
        id="e1",
        conversation_id="abc123",
        type=EventType.MESSAGE,
        from_agent="a1",
        data={"parts": [{"type": "text", "text": "Hello"}]},
    )
    assert event.type == EventType.MESSAGE


def test_artifact_structure():
    artifact = Artifact(
        id="art1",
        parts=[TextPart(type="text", text="Generated code")],
    )
    assert artifact.parts[0].text == "Generated code"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_models.py -v`
Expected: ImportError — models module doesn't exist.

- [ ] **Step 3: Implement data models**

```python
# src/bifrost/store/models.py
"""A2A-inspired data models for bifrost v2."""
from __future__ import annotations

import enum
from dataclasses import dataclass, field
from datetime import datetime, timezone


class AgentStatus(str, enum.Enum):
    ONLINE = "online"
    IDLE = "idle"
    OFFLINE = "offline"
    DND = "dnd"


class TaskStatus(str, enum.Enum):
    QUEUED = "queued"
    RUNNING = "running"
    INPUT_REQUIRED = "input-required"
    AUTH_REQUIRED = "auth-required"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELED = "canceled"
    REJECTED = "rejected"


class EventType(str, enum.Enum):
    MESSAGE = "message"
    METADATA = "metadata"
    FILE = "file"
    PARTICIPANT_ADDED = "participant.added"
    PARTICIPANT_REMOVED = "participant.removed"


def _now() -> datetime:
    return datetime.now(timezone.utc)


# --- A2A Part types ---


@dataclass
class TextPart:
    text: str
    type: str = "text"


@dataclass
class FilePart:
    mime_type: str
    uri: str | None = None
    data: str | None = None  # base64
    type: str = "file"


@dataclass
class DataPart:
    data: dict
    type: str = "data"


# --- A2A Artifact ---


@dataclass
class Artifact:
    id: str
    parts: list[TextPart | FilePart | DataPart] = field(default_factory=list)
    metadata: dict = field(default_factory=dict)


# --- A2A AgentSkill ---


@dataclass
class AgentSkill:
    id: str
    name: str
    description: str
    tags: list[str] = field(default_factory=list)
    examples: list[str] = field(default_factory=list)
    input_modes: list[str] = field(default_factory=list)
    output_modes: list[str] = field(default_factory=list)


# --- Agent Card (A2A-inspired) ---


@dataclass
class AgentCard:
    agent_id: str
    description: str = ""
    version: str = ""
    icon_url: str = ""
    provider: dict = field(default_factory=dict)
    capabilities: dict = field(default_factory=dict)
    skills: list[AgentSkill] = field(default_factory=list)
    default_input_modes: list[str] = field(default_factory=lambda: ["text"])
    default_output_modes: list[str] = field(default_factory=lambda: ["text"])
    metadata: dict = field(default_factory=dict)
    updated_at: datetime = field(default_factory=_now)


# --- Agent ---


@dataclass
class Agent:
    id: str
    name: str
    status: AgentStatus = AgentStatus.OFFLINE
    dnd_reason: str = ""
    connected_at: datetime | None = None
    last_seen: datetime | None = None
    oauth_subject: str = ""
    card: AgentCard | None = None


# --- Task (A2A states) ---


@dataclass
class Task:
    id: str
    requester: str
    assignee: str
    status: TaskStatus = TaskStatus.QUEUED
    context_id: str = ""
    status_message: dict | None = None  # A2A Message (role + parts)
    artifacts: list[Artifact] = field(default_factory=list)
    metadata: dict = field(default_factory=dict)
    created_at: datetime = field(default_factory=_now)
    updated_at: datetime = field(default_factory=_now)


# --- Conversation ---


@dataclass
class Conversation:
    id: str
    title: str = ""
    channel: str | None = None
    participants: list[str] = field(default_factory=list)
    closed: bool = False
    closed_reason: str = ""
    created_at: datetime = field(default_factory=_now)


# --- Event ---


@dataclass
class Event:
    id: str
    conversation_id: str
    type: EventType
    from_agent: str
    data: dict = field(default_factory=dict)
    timestamp: datetime = field(default_factory=_now)
```

- [ ] **Step 4: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_models.py -v`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add src/bifrost/store/models.py tests/test_models.py
git commit -m "feat: A2A-inspired data models"
```

---

### Task 3: SQLite Store

**Files:**
- Create: `src/bifrost/store/db.py`
- Create: `tests/test_db.py`

- [ ] **Step 1: Write tests for the store**

```python
# tests/test_db.py
"""Tests for SQLite store."""
import pytest

from bifrost.store.db import Store
from bifrost.store.models import (
    Agent,
    AgentCard,
    AgentSkill,
    AgentStatus,
    Conversation,
    Event,
    EventType,
    Task,
    TaskStatus,
)


@pytest.fixture
def store(tmp_path):
    db_path = tmp_path / "test.db"
    s = Store(str(db_path))
    s.initialize()
    return s


# --- Agents ---


def test_upsert_and_get_agent(store: Store):
    agent = Agent(id="a1", name="api-backend", status=AgentStatus.ONLINE)
    store.upsert_agent(agent)
    result = store.get_agent("a1")
    assert result is not None
    assert result.name == "api-backend"
    assert result.status == AgentStatus.ONLINE


def test_get_agent_not_found(store: Store):
    assert store.get_agent("nonexistent") is None


def test_get_agent_by_name(store: Store):
    agent = Agent(id="a1", name="api-backend")
    store.upsert_agent(agent)
    result = store.get_agent_by_name("api-backend")
    assert result is not None
    assert result.id == "a1"


def test_list_agents(store: Store):
    store.upsert_agent(Agent(id="a1", name="agent-a", status=AgentStatus.ONLINE))
    store.upsert_agent(Agent(id="a2", name="agent-b", status=AgentStatus.OFFLINE))
    agents = store.list_agents()
    assert len(agents) == 2


def test_list_agents_by_status(store: Store):
    store.upsert_agent(Agent(id="a1", name="agent-a", status=AgentStatus.ONLINE))
    store.upsert_agent(Agent(id="a2", name="agent-b", status=AgentStatus.OFFLINE))
    online = store.list_agents(status=AgentStatus.ONLINE)
    assert len(online) == 1
    assert online[0].name == "agent-a"


# --- Agent Cards ---


def test_upsert_and_get_card(store: Store):
    store.upsert_agent(Agent(id="a1", name="api-backend"))
    card = AgentCard(
        agent_id="a1",
        description="Backend API",
        skills=[AgentSkill(id="tdd", name="TDD", description="Test-driven dev", tags=["testing"])],
    )
    store.upsert_card(card)
    result = store.get_card("a1")
    assert result is not None
    assert result.description == "Backend API"
    assert len(result.skills) == 1


# --- Tasks ---


def test_create_and_get_task(store: Store):
    store.upsert_agent(Agent(id="a1", name="requester"))
    store.upsert_agent(Agent(id="a2", name="assignee"))
    task = Task(id="t1", requester="a1", assignee="a2", status=TaskStatus.QUEUED)
    store.create_task(task)
    result = store.get_task("t1")
    assert result is not None
    assert result.status == TaskStatus.QUEUED


def test_update_task_status(store: Store):
    store.upsert_agent(Agent(id="a1", name="requester"))
    store.upsert_agent(Agent(id="a2", name="assignee"))
    store.create_task(Task(id="t1", requester="a1", assignee="a2"))
    store.update_task("t1", status=TaskStatus.RUNNING)
    result = store.get_task("t1")
    assert result.status == TaskStatus.RUNNING


def test_list_tasks_by_status(store: Store):
    store.upsert_agent(Agent(id="a1", name="r"))
    store.upsert_agent(Agent(id="a2", name="w"))
    store.create_task(Task(id="t1", requester="a1", assignee="a2", status=TaskStatus.QUEUED))
    store.create_task(Task(id="t2", requester="a1", assignee="a2", status=TaskStatus.COMPLETED))
    queued = store.list_tasks(status=TaskStatus.QUEUED)
    assert len(queued) == 1
    assert queued[0].id == "t1"


def test_list_tasks_by_assignee(store: Store):
    store.upsert_agent(Agent(id="a1", name="r"))
    store.upsert_agent(Agent(id="a2", name="w1"))
    store.upsert_agent(Agent(id="a3", name="w2"))
    store.create_task(Task(id="t1", requester="a1", assignee="a2"))
    store.create_task(Task(id="t2", requester="a1", assignee="a3"))
    tasks = store.list_tasks(assignee="a2")
    assert len(tasks) == 1


# --- Conversations ---


def test_create_and_get_conversation(store: Store):
    conv = Conversation(id="deploys:abc", channel="deploys", title="Deploy v2", participants=["a1", "a2"])
    store.create_conversation(conv)
    result = store.get_conversation("deploys:abc")
    assert result is not None
    assert result.channel == "deploys"
    assert result.participants == ["a1", "a2"]


def test_list_conversations_by_channel(store: Store):
    store.create_conversation(Conversation(id="deploys:c1", channel="deploys", participants=["a1"]))
    store.create_conversation(Conversation(id="general:c2", channel="general", participants=["a1"]))
    store.create_conversation(Conversation(id="c3", participants=["a1", "a2"]))
    deploys = store.list_conversations(channel="deploys")
    assert len(deploys) == 1
    assert deploys[0].id == "deploys:c1"


# --- Events ---


def test_append_and_list_events(store: Store):
    store.create_conversation(Conversation(id="c1", participants=["a1"]))
    e1 = Event(id="e1", conversation_id="c1", type=EventType.MESSAGE, from_agent="a1", data={"parts": [{"type": "text", "text": "Hello"}]})
    e2 = Event(id="e2", conversation_id="c1", type=EventType.MESSAGE, from_agent="a1", data={"parts": [{"type": "text", "text": "World"}]})
    store.append_event(e1)
    store.append_event(e2)
    events = store.list_events("c1")
    assert len(events) == 2
    assert events[0].id == "e1"  # chronological order


def test_list_events_after(store: Store):
    store.create_conversation(Conversation(id="c1", participants=["a1"]))
    store.append_event(Event(id="e1", conversation_id="c1", type=EventType.MESSAGE, from_agent="a1"))
    store.append_event(Event(id="e2", conversation_id="c1", type=EventType.MESSAGE, from_agent="a1"))
    store.append_event(Event(id="e3", conversation_id="c1", type=EventType.MESSAGE, from_agent="a1"))
    events = store.list_events("c1", after_event_id="e1")
    assert len(events) == 2
    assert events[0].id == "e2"


# --- Delivery State ---


def test_delivery_state(store: Store):
    store.create_conversation(Conversation(id="c1", participants=["a1"]))
    store.append_event(Event(id="e1", conversation_id="c1", type=EventType.MESSAGE, from_agent="a1"))
    store.append_event(Event(id="e2", conversation_id="c1", type=EventType.MESSAGE, from_agent="a1"))

    # No delivery state yet — should get all events
    last = store.get_last_delivered_event("agent-b", "c1")
    assert last is None

    # Mark delivery
    store.set_last_delivered_event("agent-b", "c1", "e1")
    last = store.get_last_delivered_event("agent-b", "c1")
    assert last == "e1"


# --- Subscriptions ---


def test_subscribe_and_list(store: Store):
    store.upsert_agent(Agent(id="a1", name="agent-a"))
    store.subscribe("a1", "deploys")
    store.subscribe("a1", "c1")
    subs = store.list_subscriptions("a1")
    assert set(subs) == {"deploys", "c1"}


def test_subscribe_idempotent(store: Store):
    store.upsert_agent(Agent(id="a1", name="agent-a"))
    store.subscribe("a1", "deploys")
    store.subscribe("a1", "deploys")
    subs = store.list_subscriptions("a1")
    assert subs == ["deploys"]
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_db.py -v`
Expected: ImportError.

- [ ] **Step 3: Implement the store**

```python
# src/bifrost/store/db.py
"""SQLite persistence layer."""
from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone

from bifrost.store.models import (
    Agent,
    AgentCard,
    AgentSkill,
    AgentStatus,
    Conversation,
    Event,
    EventType,
    Task,
    TaskStatus,
)

SCHEMA = """
CREATE TABLE IF NOT EXISTS agents (
    id              TEXT PRIMARY KEY,
    name            TEXT UNIQUE NOT NULL,
    status          TEXT NOT NULL DEFAULT 'offline',
    dnd_reason      TEXT NOT NULL DEFAULT '',
    connected_at    TEXT,
    last_seen       TEXT,
    oauth_subject   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS agent_cards (
    agent_id              TEXT PRIMARY KEY REFERENCES agents(id),
    description           TEXT NOT NULL DEFAULT '',
    version               TEXT NOT NULL DEFAULT '',
    icon_url              TEXT NOT NULL DEFAULT '',
    provider              TEXT NOT NULL DEFAULT '{}',
    capabilities          TEXT NOT NULL DEFAULT '{}',
    skills                TEXT NOT NULL DEFAULT '[]',
    default_input_modes   TEXT NOT NULL DEFAULT '["text"]',
    default_output_modes  TEXT NOT NULL DEFAULT '["text"]',
    metadata              TEXT NOT NULL DEFAULT '{}',
    updated_at            TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id              TEXT PRIMARY KEY,
    context_id      TEXT NOT NULL DEFAULT '',
    requester       TEXT NOT NULL REFERENCES agents(id),
    assignee        TEXT NOT NULL REFERENCES agents(id),
    status          TEXT NOT NULL DEFAULT 'queued',
    status_message  TEXT,
    artifacts       TEXT NOT NULL DEFAULT '[]',
    metadata        TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS conversations (
    id              TEXT PRIMARY KEY,
    channel         TEXT,
    participants    TEXT NOT NULL DEFAULT '[]',
    title           TEXT NOT NULL DEFAULT '',
    closed          INTEGER NOT NULL DEFAULT 0,
    closed_reason   TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversations_channel ON conversations(channel);

CREATE TABLE IF NOT EXISTS events (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    type            TEXT NOT NULL,
    from_agent      TEXT NOT NULL,
    data            TEXT NOT NULL DEFAULT '{}',
    timestamp       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_conversation ON events(conversation_id, timestamp);

CREATE TABLE IF NOT EXISTS delivery_state (
    agent_id        TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    last_event_id   TEXT NOT NULL,
    PRIMARY KEY (agent_id, conversation_id)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    agent_id        TEXT NOT NULL,
    target          TEXT NOT NULL,
    PRIMARY KEY (agent_id, target)
);
"""


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def _parse_dt(s: str | None) -> datetime | None:
    if not s:
        return None
    return datetime.fromisoformat(s)


class Store:
    def __init__(self, db_path: str) -> None:
        self._db_path = db_path
        self._conn: sqlite3.Connection | None = None

    def initialize(self) -> None:
        self._conn = sqlite3.connect(self._db_path)
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute("PRAGMA foreign_keys=ON")
        self._conn.executescript(SCHEMA)

    def close(self) -> None:
        if self._conn:
            self._conn.close()

    @property
    def conn(self) -> sqlite3.Connection:
        assert self._conn is not None, "Store not initialized"
        return self._conn

    # --- Agents ---

    def upsert_agent(self, agent: Agent) -> None:
        self.conn.execute(
            """INSERT INTO agents (id, name, status, dnd_reason, connected_at, last_seen, oauth_subject)
               VALUES (?, ?, ?, ?, ?, ?, ?)
               ON CONFLICT(id) DO UPDATE SET
                 name=excluded.name, status=excluded.status, dnd_reason=excluded.dnd_reason,
                 connected_at=excluded.connected_at, last_seen=excluded.last_seen,
                 oauth_subject=excluded.oauth_subject""",
            (agent.id, agent.name, agent.status.value, agent.dnd_reason,
             agent.connected_at.isoformat() if agent.connected_at else None,
             agent.last_seen.isoformat() if agent.last_seen else None,
             agent.oauth_subject),
        )
        self.conn.commit()

    def get_agent(self, agent_id: str) -> Agent | None:
        row = self.conn.execute("SELECT * FROM agents WHERE id=?", (agent_id,)).fetchone()
        if not row:
            return None
        return self._row_to_agent(row)

    def get_agent_by_name(self, name: str) -> Agent | None:
        row = self.conn.execute("SELECT * FROM agents WHERE name=?", (name,)).fetchone()
        if not row:
            return None
        return self._row_to_agent(row)

    def list_agents(self, status: AgentStatus | None = None) -> list[Agent]:
        if status:
            rows = self.conn.execute("SELECT * FROM agents WHERE status=?", (status.value,)).fetchall()
        else:
            rows = self.conn.execute("SELECT * FROM agents").fetchall()
        return [self._row_to_agent(r) for r in rows]

    def _row_to_agent(self, row: tuple) -> Agent:
        return Agent(
            id=row[0], name=row[1], status=AgentStatus(row[2]),
            dnd_reason=row[3], connected_at=_parse_dt(row[4]),
            last_seen=_parse_dt(row[5]), oauth_subject=row[6],
        )

    # --- Agent Cards ---

    def upsert_card(self, card: AgentCard) -> None:
        skills_json = json.dumps([
            {"id": s.id, "name": s.name, "description": s.description,
             "tags": s.tags, "examples": s.examples,
             "input_modes": s.input_modes, "output_modes": s.output_modes}
            for s in card.skills
        ])
        self.conn.execute(
            """INSERT INTO agent_cards
               (agent_id, description, version, icon_url, provider, capabilities,
                skills, default_input_modes, default_output_modes, metadata, updated_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
               ON CONFLICT(agent_id) DO UPDATE SET
                 description=excluded.description, version=excluded.version,
                 icon_url=excluded.icon_url, provider=excluded.provider,
                 capabilities=excluded.capabilities, skills=excluded.skills,
                 default_input_modes=excluded.default_input_modes,
                 default_output_modes=excluded.default_output_modes,
                 metadata=excluded.metadata, updated_at=excluded.updated_at""",
            (card.agent_id, card.description, card.version, card.icon_url,
             json.dumps(card.provider), json.dumps(card.capabilities),
             skills_json, json.dumps(card.default_input_modes),
             json.dumps(card.default_output_modes), json.dumps(card.metadata),
             card.updated_at.isoformat()),
        )
        self.conn.commit()

    def get_card(self, agent_id: str) -> AgentCard | None:
        row = self.conn.execute("SELECT * FROM agent_cards WHERE agent_id=?", (agent_id,)).fetchone()
        if not row:
            return None
        skills_data = json.loads(row[6])
        return AgentCard(
            agent_id=row[0], description=row[1], version=row[2], icon_url=row[3],
            provider=json.loads(row[4]), capabilities=json.loads(row[5]),
            skills=[AgentSkill(**s) for s in skills_data],
            default_input_modes=json.loads(row[7]),
            default_output_modes=json.loads(row[8]),
            metadata=json.loads(row[9]),
            updated_at=datetime.fromisoformat(row[10]),
        )

    # --- Tasks ---

    def create_task(self, task: Task) -> None:
        self.conn.execute(
            """INSERT INTO tasks (id, context_id, requester, assignee, status,
               status_message, artifacts, metadata, created_at, updated_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (task.id, task.context_id, task.requester, task.assignee,
             task.status.value, json.dumps(task.status_message) if task.status_message else None,
             json.dumps([]), json.dumps(task.metadata),
             task.created_at.isoformat(), task.updated_at.isoformat()),
        )
        self.conn.commit()

    def get_task(self, task_id: str) -> Task | None:
        row = self.conn.execute("SELECT * FROM tasks WHERE id=?", (task_id,)).fetchone()
        if not row:
            return None
        return self._row_to_task(row)

    def update_task(self, task_id: str, **kwargs: str | None) -> None:
        sets = []
        vals = []
        for k, v in kwargs.items():
            if k == "status":
                sets.append("status=?")
                vals.append(v if isinstance(v, str) else v.value)
            elif k == "status_message":
                sets.append("status_message=?")
                vals.append(json.dumps(v))
            elif k == "artifacts":
                sets.append("artifacts=?")
                vals.append(json.dumps(v))
            elif k == "metadata":
                sets.append("metadata=?")
                vals.append(json.dumps(v))
        sets.append("updated_at=?")
        vals.append(_now_iso())
        vals.append(task_id)
        self.conn.execute(f"UPDATE tasks SET {', '.join(sets)} WHERE id=?", vals)
        self.conn.commit()

    def list_tasks(
        self,
        status: TaskStatus | None = None,
        requester: str | None = None,
        assignee: str | None = None,
    ) -> list[Task]:
        where = []
        params: list[str] = []
        if status:
            where.append("status=?")
            params.append(status.value)
        if requester:
            where.append("requester=?")
            params.append(requester)
        if assignee:
            where.append("assignee=?")
            params.append(assignee)
        clause = f" WHERE {' AND '.join(where)}" if where else ""
        rows = self.conn.execute(f"SELECT * FROM tasks{clause} ORDER BY created_at", params).fetchall()
        return [self._row_to_task(r) for r in rows]

    def _row_to_task(self, row: tuple) -> Task:
        return Task(
            id=row[0], context_id=row[1], requester=row[2], assignee=row[3],
            status=TaskStatus(row[4]),
            status_message=json.loads(row[5]) if row[5] else None,
            artifacts=json.loads(row[6]) if row[6] else [],
            metadata=json.loads(row[7]) if row[7] else {},
            created_at=datetime.fromisoformat(row[8]),
            updated_at=datetime.fromisoformat(row[9]),
        )

    # --- Conversations ---

    def create_conversation(self, conv: Conversation) -> None:
        self.conn.execute(
            """INSERT INTO conversations (id, channel, participants, title, closed, closed_reason, created_at)
               VALUES (?, ?, ?, ?, ?, ?, ?)""",
            (conv.id, conv.channel, json.dumps(conv.participants), conv.title,
             int(conv.closed), conv.closed_reason, conv.created_at.isoformat()),
        )
        self.conn.commit()

    def get_conversation(self, conv_id: str) -> Conversation | None:
        row = self.conn.execute("SELECT * FROM conversations WHERE id=?", (conv_id,)).fetchone()
        if not row:
            return None
        return self._row_to_conversation(row)

    def list_conversations(
        self,
        channel: str | None = None,
        active_only: bool = False,
    ) -> list[Conversation]:
        where = []
        params: list = []
        if channel:
            where.append("channel=?")
            params.append(channel)
        if active_only:
            where.append("closed=0")
        clause = f" WHERE {' AND '.join(where)}" if where else ""
        rows = self.conn.execute(
            f"SELECT * FROM conversations{clause} ORDER BY created_at DESC", params
        ).fetchall()
        return [self._row_to_conversation(r) for r in rows]

    def _row_to_conversation(self, row: tuple) -> Conversation:
        return Conversation(
            id=row[0], channel=row[1], participants=json.loads(row[2]),
            title=row[3], closed=bool(row[4]), closed_reason=row[5],
            created_at=datetime.fromisoformat(row[6]),
        )

    # --- Events ---

    def append_event(self, event: Event) -> None:
        self.conn.execute(
            """INSERT INTO events (id, conversation_id, type, from_agent, data, timestamp)
               VALUES (?, ?, ?, ?, ?, ?)""",
            (event.id, event.conversation_id, event.type.value,
             event.from_agent, json.dumps(event.data), event.timestamp.isoformat()),
        )
        self.conn.commit()

    def list_events(
        self, conversation_id: str, after_event_id: str | None = None
    ) -> list[Event]:
        if after_event_id:
            # Get the timestamp of the after_event to filter
            ref = self.conn.execute(
                "SELECT timestamp FROM events WHERE id=?", (after_event_id,)
            ).fetchone()
            if not ref:
                return []
            rows = self.conn.execute(
                """SELECT * FROM events
                   WHERE conversation_id=? AND timestamp > ?
                   ORDER BY timestamp""",
                (conversation_id, ref[0]),
            ).fetchall()
        else:
            rows = self.conn.execute(
                "SELECT * FROM events WHERE conversation_id=? ORDER BY timestamp",
                (conversation_id,),
            ).fetchall()
        return [self._row_to_event(r) for r in rows]

    def _row_to_event(self, row: tuple) -> Event:
        return Event(
            id=row[0], conversation_id=row[1], type=EventType(row[2]),
            from_agent=row[3], data=json.loads(row[4]),
            timestamp=datetime.fromisoformat(row[5]),
        )

    # --- Delivery State ---

    def get_last_delivered_event(self, agent_id: str, conversation_id: str) -> str | None:
        row = self.conn.execute(
            "SELECT last_event_id FROM delivery_state WHERE agent_id=? AND conversation_id=?",
            (agent_id, conversation_id),
        ).fetchone()
        return row[0] if row else None

    def set_last_delivered_event(self, agent_id: str, conversation_id: str, event_id: str) -> None:
        self.conn.execute(
            """INSERT INTO delivery_state (agent_id, conversation_id, last_event_id)
               VALUES (?, ?, ?)
               ON CONFLICT(agent_id, conversation_id) DO UPDATE SET last_event_id=excluded.last_event_id""",
            (agent_id, conversation_id, event_id),
        )
        self.conn.commit()

    # --- Subscriptions ---

    def subscribe(self, agent_id: str, target: str) -> None:
        self.conn.execute(
            "INSERT OR IGNORE INTO subscriptions (agent_id, target) VALUES (?, ?)",
            (agent_id, target),
        )
        self.conn.commit()

    def unsubscribe(self, agent_id: str, target: str) -> None:
        self.conn.execute(
            "DELETE FROM subscriptions WHERE agent_id=? AND target=?",
            (agent_id, target),
        )
        self.conn.commit()

    def list_subscriptions(self, agent_id: str) -> list[str]:
        rows = self.conn.execute(
            "SELECT target FROM subscriptions WHERE agent_id=? ORDER BY target",
            (agent_id,),
        ).fetchall()
        return [r[0] for r in rows]

    def list_subscribers(self, target: str) -> list[str]:
        """Get agent IDs subscribed to a target (exact match or prefix match for channels)."""
        rows = self.conn.execute(
            "SELECT agent_id FROM subscriptions WHERE target=?",
            (target,),
        ).fetchall()
        return [r[0] for r in rows]
```

- [ ] **Step 4: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_db.py -v`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add src/bifrost/store/db.py tests/test_db.py
git commit -m "feat: SQLite store with full CRUD for agents, tasks, conversations, events"
```

---

### Task 4: Hub Business Logic — Agents

**Files:**
- Create: `src/bifrost/hub/agents.py`
- Create: `tests/test_hub_agents.py`

- [ ] **Step 1: Write tests**

```python
# tests/test_hub_agents.py
"""Tests for agent registry hub logic."""
import pytest

from bifrost.hub.agents import AgentHub
from bifrost.store.db import Store
from bifrost.store.models import AgentCard, AgentSkill, AgentStatus


@pytest.fixture
def store(tmp_path):
    s = Store(str(tmp_path / "test.db"))
    s.initialize()
    return s


@pytest.fixture
def hub(store):
    return AgentHub(store)


def test_register_new_agent(hub: AgentHub):
    agent = hub.register("api-backend", oauth_subject="user@example.com")
    assert agent.name == "api-backend"
    assert agent.status == AgentStatus.ONLINE
    assert agent.oauth_subject == "user@example.com"
    assert agent.connected_at is not None


def test_register_existing_agent_reconnects(hub: AgentHub):
    a1 = hub.register("api-backend")
    a2 = hub.register("api-backend")
    assert a1.id == a2.id
    assert a2.status == AgentStatus.ONLINE


def test_disconnect_marks_offline(hub: AgentHub):
    agent = hub.register("api-backend")
    hub.disconnect(agent.id)
    result = hub.get(agent.id)
    assert result.status == AgentStatus.OFFLINE


def test_update_status_dnd(hub: AgentHub):
    agent = hub.register("api-backend")
    hub.update_status(agent.id, AgentStatus.DND, reason="focusing")
    result = hub.get(agent.id)
    assert result.status == AgentStatus.DND
    assert result.dnd_reason == "focusing"


def test_update_card(hub: AgentHub):
    agent = hub.register("api-backend")
    card = hub.update_card(
        agent.id,
        description="Backend API agent",
        skills=[AgentSkill(id="tdd", name="TDD", description="Test-driven dev", tags=["testing"])],
    )
    assert card.description == "Backend API agent"
    # Retrieve via get to verify persistence
    result = hub.get(agent.id)
    assert result.card is not None
    assert result.card.description == "Backend API agent"


def test_update_card_freeform(hub: AgentHub):
    agent = hub.register("api-backend")
    card = hub.update_card(agent.id, introduction="I'm a backend agent that does Python and SQL")
    assert card.description == "I'm a backend agent that does Python and SQL"


def test_update_card_structured_overrides_freeform(hub: AgentHub):
    agent = hub.register("api-backend")
    card = hub.update_card(
        agent.id,
        introduction="I do backend stuff",
        description="Python/FastAPI backend service",
    )
    assert card.description == "Python/FastAPI backend service"


def test_list_agents_with_cards(hub: AgentHub):
    hub.register("agent-a")
    hub.register("agent-b")
    agents = hub.list_all()
    assert len(agents) == 2


def test_resolve_agent_by_name(hub: AgentHub):
    agent = hub.register("api-backend")
    resolved = hub.resolve("api-backend")
    assert resolved.id == agent.id


def test_resolve_agent_not_found(hub: AgentHub):
    assert hub.resolve("nonexistent") is None
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_hub_agents.py -v`
Expected: ImportError.

- [ ] **Step 3: Implement AgentHub**

```python
# src/bifrost/hub/agents.py
"""Agent registry and card management."""
from __future__ import annotations

import uuid
from datetime import datetime, timezone

from bifrost.store.db import Store
from bifrost.store.models import (
    Agent,
    AgentCard,
    AgentSkill,
    AgentStatus,
)


class AgentHub:
    def __init__(self, store: Store) -> None:
        self._store = store

    def register(self, name: str, oauth_subject: str = "") -> Agent:
        """Register or reconnect an agent. Returns the agent with ONLINE status."""
        existing = self._store.get_agent_by_name(name)
        now = datetime.now(timezone.utc)
        if existing:
            existing.status = AgentStatus.ONLINE
            existing.connected_at = now
            existing.last_seen = now
            if oauth_subject:
                existing.oauth_subject = oauth_subject
            self._store.upsert_agent(existing)
            existing.card = self._store.get_card(existing.id)
            return existing

        agent = Agent(
            id=uuid.uuid4().hex[:12],
            name=name,
            status=AgentStatus.ONLINE,
            connected_at=now,
            last_seen=now,
            oauth_subject=oauth_subject,
        )
        self._store.upsert_agent(agent)
        return agent

    def disconnect(self, agent_id: str) -> None:
        """Mark agent as offline."""
        agent = self._store.get_agent(agent_id)
        if agent:
            agent.status = AgentStatus.OFFLINE
            agent.last_seen = datetime.now(timezone.utc)
            self._store.upsert_agent(agent)

    def update_status(self, agent_id: str, status: AgentStatus, reason: str = "") -> None:
        agent = self._store.get_agent(agent_id)
        if not agent:
            return
        agent.status = status
        agent.dnd_reason = reason if status == AgentStatus.DND else ""
        agent.last_seen = datetime.now(timezone.utc)
        self._store.upsert_agent(agent)

    def update_card(
        self,
        agent_id: str,
        *,
        introduction: str = "",
        description: str = "",
        version: str = "",
        icon_url: str = "",
        provider: dict | None = None,
        capabilities: dict | None = None,
        skills: list[AgentSkill] | None = None,
        default_input_modes: list[str] | None = None,
        default_output_modes: list[str] | None = None,
        metadata: dict | None = None,
    ) -> AgentCard:
        """Update agent card. Structured fields override freeform introduction."""
        existing = self._store.get_card(agent_id)

        # Start from existing card or blank
        card = existing or AgentCard(agent_id=agent_id)

        # Freeform introduction goes to description if no explicit description given
        if introduction and not description:
            card.description = introduction
        if description:
            card.description = description
        if version:
            card.version = version
        if icon_url:
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
        card.updated_at = datetime.now(timezone.utc)

        self._store.upsert_card(card)
        return card

    def get(self, agent_id: str) -> Agent | None:
        agent = self._store.get_agent(agent_id)
        if agent:
            agent.card = self._store.get_card(agent_id)
        return agent

    def resolve(self, name: str) -> Agent | None:
        """Resolve agent by name."""
        agent = self._store.get_agent_by_name(name)
        if agent:
            agent.card = self._store.get_card(agent.id)
        return agent

    def list_all(self, status: AgentStatus | None = None) -> list[Agent]:
        agents = self._store.list_agents(status=status)
        for a in agents:
            a.card = self._store.get_card(a.id)
        return agents
```

- [ ] **Step 4: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_hub_agents.py -v`
Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add src/bifrost/hub/agents.py tests/test_hub_agents.py
git commit -m "feat: agent registry with card management"
```

---

### Task 5: Hub Business Logic — Tasks

**Files:**
- Create: `src/bifrost/hub/tasks.py`
- Create: `tests/test_hub_tasks.py`

- [ ] **Step 1: Write tests**

```python
# tests/test_hub_tasks.py
"""Tests for task lifecycle hub logic."""
import pytest

from bifrost.hub.agents import AgentHub
from bifrost.hub.tasks import TaskHub, InvalidTransition
from bifrost.store.db import Store
from bifrost.store.models import TaskStatus


@pytest.fixture
def store(tmp_path):
    s = Store(str(tmp_path / "test.db"))
    s.initialize()
    return s


@pytest.fixture
def agents(store):
    hub = AgentHub(store)
    hub.register("requester")
    hub.register("worker")
    return hub


@pytest.fixture
def hub(store, agents):
    return TaskHub(store)


def test_create_task(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    task = hub.create(requester=r.id, assignee=w.id)
    assert task.status == TaskStatus.QUEUED
    assert task.requester == r.id
    assert task.assignee == w.id


def test_create_task_with_context(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    t1 = hub.create(requester=r.id, assignee=w.id, context_id="ctx1")
    t2 = hub.create(requester=r.id, assignee=w.id, context_id="ctx1")
    assert t1.context_id == "ctx1"
    assert t2.context_id == "ctx1"


def test_valid_transitions(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    task = hub.create(requester=r.id, assignee=w.id)

    hub.update_status(task.id, TaskStatus.RUNNING)
    assert hub.get(task.id).status == TaskStatus.RUNNING

    hub.update_status(task.id, TaskStatus.COMPLETED)
    assert hub.get(task.id).status == TaskStatus.COMPLETED


def test_queued_to_rejected(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    task = hub.create(requester=r.id, assignee=w.id)
    hub.update_status(task.id, TaskStatus.REJECTED)
    assert hub.get(task.id).status == TaskStatus.REJECTED


def test_running_to_input_required(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    task = hub.create(requester=r.id, assignee=w.id)
    hub.update_status(task.id, TaskStatus.RUNNING)
    hub.update_status(task.id, TaskStatus.INPUT_REQUIRED)
    assert hub.get(task.id).status == TaskStatus.INPUT_REQUIRED


def test_input_required_back_to_running(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    task = hub.create(requester=r.id, assignee=w.id)
    hub.update_status(task.id, TaskStatus.RUNNING)
    hub.update_status(task.id, TaskStatus.INPUT_REQUIRED)
    hub.update_status(task.id, TaskStatus.RUNNING)
    assert hub.get(task.id).status == TaskStatus.RUNNING


def test_invalid_transition_raises(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    task = hub.create(requester=r.id, assignee=w.id)
    with pytest.raises(InvalidTransition):
        hub.update_status(task.id, TaskStatus.COMPLETED)  # can't go queued -> completed


def test_terminal_state_cannot_transition(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    task = hub.create(requester=r.id, assignee=w.id)
    hub.update_status(task.id, TaskStatus.REJECTED)
    with pytest.raises(InvalidTransition):
        hub.update_status(task.id, TaskStatus.RUNNING)


def test_list_tasks_filtered(hub: TaskHub, agents: AgentHub):
    r = agents.resolve("requester")
    w = agents.resolve("worker")
    hub.create(requester=r.id, assignee=w.id)
    hub.create(requester=r.id, assignee=w.id)
    t3 = hub.create(requester=r.id, assignee=w.id)
    hub.update_status(t3.id, TaskStatus.RUNNING)
    running = hub.list(status=TaskStatus.RUNNING)
    assert len(running) == 1
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_hub_tasks.py -v`
Expected: ImportError.

- [ ] **Step 3: Implement TaskHub**

```python
# src/bifrost/hub/tasks.py
"""Task lifecycle management."""
from __future__ import annotations

import uuid

from bifrost.store.db import Store
from bifrost.store.models import Task, TaskStatus

# A2A valid state transitions
VALID_TRANSITIONS: dict[TaskStatus, set[TaskStatus]] = {
    TaskStatus.QUEUED: {TaskStatus.RUNNING, TaskStatus.REJECTED, TaskStatus.CANCELED},
    TaskStatus.RUNNING: {
        TaskStatus.COMPLETED, TaskStatus.FAILED, TaskStatus.CANCELED,
        TaskStatus.INPUT_REQUIRED, TaskStatus.AUTH_REQUIRED,
    },
    TaskStatus.INPUT_REQUIRED: {TaskStatus.RUNNING, TaskStatus.CANCELED, TaskStatus.FAILED},
    TaskStatus.AUTH_REQUIRED: {TaskStatus.RUNNING, TaskStatus.CANCELED, TaskStatus.FAILED},
    # Terminal states — no transitions out
    TaskStatus.COMPLETED: set(),
    TaskStatus.FAILED: set(),
    TaskStatus.CANCELED: set(),
    TaskStatus.REJECTED: set(),
}


class InvalidTransition(Exception):
    def __init__(self, current: TaskStatus, target: TaskStatus) -> None:
        super().__init__(f"Cannot transition from {current.value} to {target.value}")
        self.current = current
        self.target = target


class TaskHub:
    def __init__(self, store: Store) -> None:
        self._store = store

    def create(
        self,
        requester: str,
        assignee: str,
        context_id: str = "",
        metadata: dict | None = None,
    ) -> Task:
        task = Task(
            id=uuid.uuid4().hex[:12],
            requester=requester,
            assignee=assignee,
            context_id=context_id,
            status=TaskStatus.QUEUED,
            metadata=metadata or {},
        )
        self._store.create_task(task)
        return task

    def get(self, task_id: str) -> Task | None:
        return self._store.get_task(task_id)

    def update_status(self, task_id: str, status: TaskStatus) -> Task:
        task = self._store.get_task(task_id)
        if not task:
            raise ValueError(f"Task {task_id} not found")

        allowed = VALID_TRANSITIONS.get(task.status, set())
        if status not in allowed:
            raise InvalidTransition(task.status, status)

        self._store.update_task(task_id, status=status)
        return self._store.get_task(task_id)

    def update_artifacts(self, task_id: str, artifacts: list[dict]) -> Task:
        self._store.update_task(task_id, artifacts=artifacts)
        return self._store.get_task(task_id)

    def list(
        self,
        status: TaskStatus | None = None,
        requester: str | None = None,
        assignee: str | None = None,
    ) -> list[Task]:
        return self._store.list_tasks(status=status, requester=requester, assignee=assignee)
```

- [ ] **Step 4: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_hub_tasks.py -v`
Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add src/bifrost/hub/tasks.py tests/test_hub_tasks.py
git commit -m "feat: task lifecycle with A2A state transitions"
```

---

### Task 6: Hub Business Logic — Conversations & Delivery

**Files:**
- Create: `src/bifrost/hub/conversations.py`
- Create: `src/bifrost/hub/delivery.py`
- Create: `tests/test_hub_conversations.py`
- Create: `tests/test_hub_delivery.py`

- [ ] **Step 1: Write conversation tests**

```python
# tests/test_hub_conversations.py
"""Tests for conversation and messaging hub logic."""
import pytest

from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.store.db import Store
from bifrost.store.models import EventType


@pytest.fixture
def store(tmp_path):
    s = Store(str(tmp_path / "test.db"))
    s.initialize()
    return s


@pytest.fixture
def agents(store):
    hub = AgentHub(store)
    hub.register("alice")
    hub.register("bob")
    return hub


@pytest.fixture
def hub(store):
    return ConversationHub(store)


def test_send_creates_conversation(hub: ConversationHub, agents: AgentHub):
    alice = agents.resolve("alice")
    bob = agents.resolve("bob")
    conv, event = hub.send(
        from_agent=alice.id,
        to_agent=bob.id,
        text="Hello Bob",
    )
    assert conv is not None
    assert set(conv.participants) == {alice.id, bob.id}
    assert event.type == EventType.MESSAGE


def test_send_to_channel(hub: ConversationHub, agents: AgentHub):
    alice = agents.resolve("alice")
    conv, event = hub.send(
        from_agent=alice.id,
        channel="general",
        text="Hello everyone",
    )
    assert conv.channel == "general"


def test_reply_reuses_conversation(hub: ConversationHub, agents: AgentHub):
    alice = agents.resolve("alice")
    bob = agents.resolve("bob")
    conv1, _ = hub.send(from_agent=alice.id, to_agent=bob.id, text="Hello")
    conv2, _ = hub.send(from_agent=bob.id, conversation_id=conv1.id, text="Hi back")
    assert conv1.id == conv2.id


def test_list_events_in_conversation(hub: ConversationHub, agents: AgentHub):
    alice = agents.resolve("alice")
    bob = agents.resolve("bob")
    conv, _ = hub.send(from_agent=alice.id, to_agent=bob.id, text="Hello")
    hub.send(from_agent=bob.id, conversation_id=conv.id, text="Hi")
    hub.send(from_agent=alice.id, conversation_id=conv.id, text="How are you?")
    events = hub.get_events(conv.id)
    assert len(events) == 3


def test_list_conversations_by_channel(hub: ConversationHub, agents: AgentHub):
    alice = agents.resolve("alice")
    hub.send(from_agent=alice.id, channel="deploys", text="Deploying v2")
    hub.send(from_agent=alice.id, channel="general", text="Hi all")
    deploys = hub.list(channel="deploys")
    assert len(deploys) == 1


def test_subscribe_to_channel(hub: ConversationHub, agents: AgentHub, store: Store):
    alice = agents.resolve("alice")
    hub.subscribe(alice.id, "deploys")
    subs = store.list_subscriptions(alice.id)
    assert "deploys" in subs
```

- [ ] **Step 2: Write delivery tests**

```python
# tests/test_hub_delivery.py
"""Tests for delivery tracking and bifrost_check logic."""
import pytest

from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.store.db import Store


@pytest.fixture
def store(tmp_path):
    s = Store(str(tmp_path / "test.db"))
    s.initialize()
    return s


@pytest.fixture
def agents(store):
    hub = AgentHub(store)
    hub.register("alice")
    hub.register("bob")
    return hub


@pytest.fixture
def convos(store):
    return ConversationHub(store)


@pytest.fixture
def delivery(store):
    return DeliveryHub(store)


def test_check_returns_undelivered_events(
    agents: AgentHub, convos: ConversationHub, delivery: DeliveryHub
):
    alice = agents.resolve("alice")
    bob = agents.resolve("bob")
    conv, _ = convos.send(from_agent=alice.id, to_agent=bob.id, text="Hello")
    convos.send(from_agent=alice.id, conversation_id=conv.id, text="Second msg")

    # Bob checks — should see both events
    pending = delivery.check(bob.id)
    assert len(pending) == 2


def test_check_advances_delivery_state(
    agents: AgentHub, convos: ConversationHub, delivery: DeliveryHub
):
    alice = agents.resolve("alice")
    bob = agents.resolve("bob")
    conv, _ = convos.send(from_agent=alice.id, to_agent=bob.id, text="Hello")

    # First check: 1 event
    pending = delivery.check(bob.id)
    assert len(pending) == 1

    # Second check: no new events
    pending = delivery.check(bob.id)
    assert len(pending) == 0


def test_check_after_new_message(
    agents: AgentHub, convos: ConversationHub, delivery: DeliveryHub
):
    alice = agents.resolve("alice")
    bob = agents.resolve("bob")
    conv, _ = convos.send(from_agent=alice.id, to_agent=bob.id, text="First")

    delivery.check(bob.id)  # consume first

    convos.send(from_agent=alice.id, conversation_id=conv.id, text="Second")
    pending = delivery.check(bob.id)
    assert len(pending) == 1
    assert pending[0].data["parts"][0]["text"] == "Second"


def test_check_excludes_own_messages(
    agents: AgentHub, convos: ConversationHub, delivery: DeliveryHub
):
    alice = agents.resolve("alice")
    bob = agents.resolve("bob")
    conv, _ = convos.send(from_agent=alice.id, to_agent=bob.id, text="Hello from Alice")

    # Alice checks — should not see her own message
    pending = delivery.check(alice.id)
    assert len(pending) == 0


def test_check_includes_subscribed_channels(
    agents: AgentHub, convos: ConversationHub, delivery: DeliveryHub, store: Store
):
    alice = agents.resolve("alice")
    bob = agents.resolve("bob")
    store.subscribe(bob.id, "deploys")

    convos.send(from_agent=alice.id, channel="deploys", text="Deploying v2")

    pending = delivery.check(bob.id)
    assert len(pending) == 1
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_hub_conversations.py tests/test_hub_delivery.py -v`
Expected: ImportError.

- [ ] **Step 4: Implement ConversationHub**

```python
# src/bifrost/hub/conversations.py
"""Conversation and messaging management."""
from __future__ import annotations

import uuid

from bifrost.store.db import Store
from bifrost.store.models import Conversation, Event, EventType


class ConversationHub:
    def __init__(self, store: Store) -> None:
        self._store = store

    def send(
        self,
        from_agent: str,
        *,
        to_agent: str | None = None,
        channel: str | None = None,
        conversation_id: str | None = None,
        text: str = "",
    ) -> tuple[Conversation, Event]:
        """Send a message. Creates or resumes a conversation."""
        if conversation_id:
            conv = self._store.get_conversation(conversation_id)
            if not conv:
                raise ValueError(f"Conversation {conversation_id} not found")
            # Add sender to participants if not already there
            if from_agent not in conv.participants:
                conv.participants.append(from_agent)
                # Re-create to update participants (simple approach)
        elif channel:
            conv_id = f"{channel}:{uuid.uuid4().hex[:8]}"
            conv = Conversation(
                id=conv_id,
                channel=channel,
                participants=[from_agent],
            )
            self._store.create_conversation(conv)
        elif to_agent:
            conv_id = uuid.uuid4().hex[:8]
            conv = Conversation(
                id=conv_id,
                participants=[from_agent, to_agent],
            )
            self._store.create_conversation(conv)
        else:
            raise ValueError("Must specify to_agent, channel, or conversation_id")

        event = Event(
            id=uuid.uuid4().hex[:12],
            conversation_id=conv.id,
            type=EventType.MESSAGE,
            from_agent=from_agent,
            data={"parts": [{"type": "text", "text": text}]},
        )
        self._store.append_event(event)
        return conv, event

    def get_events(self, conversation_id: str, after_event_id: str | None = None) -> list[Event]:
        return self._store.list_events(conversation_id, after_event_id=after_event_id)

    def list(
        self,
        channel: str | None = None,
        active_only: bool = False,
    ) -> list[Conversation]:
        return self._store.list_conversations(channel=channel, active_only=active_only)

    def subscribe(self, agent_id: str, target: str) -> None:
        self._store.subscribe(agent_id, target)

    def unsubscribe(self, agent_id: str, target: str) -> None:
        self._store.unsubscribe(agent_id, target)
```

- [ ] **Step 5: Implement DeliveryHub**

```python
# src/bifrost/hub/delivery.py
"""Delivery tracking — powers bifrost_check."""
from __future__ import annotations

from bifrost.store.db import Store
from bifrost.store.models import Event


class DeliveryHub:
    def __init__(self, store: Store) -> None:
        self._store = store

    def check(self, agent_id: str) -> list[Event]:
        """Return all undelivered events for this agent, advance delivery state.

        Checks:
        1. Conversations where agent is a participant
        2. Conversations in channels the agent is subscribed to
        Excludes events sent by the agent itself.
        """
        pending: list[Event] = []

        # Get conversations where agent is a participant
        all_convos = self._store.list_conversations()
        agent_convos = [c for c in all_convos if agent_id in c.participants]

        # Get channel subscriptions
        subscriptions = self._store.list_subscriptions(agent_id)

        # Add channel conversations
        for sub in subscriptions:
            channel_convos = self._store.list_conversations(channel=sub)
            for cc in channel_convos:
                if cc.id not in {c.id for c in agent_convos}:
                    agent_convos.append(cc)

        # For each conversation, get undelivered events
        for conv in agent_convos:
            last_delivered = self._store.get_last_delivered_event(agent_id, conv.id)
            events = self._store.list_events(conv.id, after_event_id=last_delivered)

            # Filter out own messages
            new_events = [e for e in events if e.from_agent != agent_id]
            pending.extend(new_events)

            # Advance delivery state to last event (including own messages)
            if events:
                self._store.set_last_delivered_event(agent_id, conv.id, events[-1].id)

        # Sort by timestamp
        pending.sort(key=lambda e: e.timestamp)
        return pending
```

- [ ] **Step 6: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_hub_conversations.py tests/test_hub_delivery.py -v`
Expected: All pass.

- [ ] **Step 7: Commit**

```bash
git add src/bifrost/hub/conversations.py src/bifrost/hub/delivery.py tests/test_hub_conversations.py tests/test_hub_delivery.py
git commit -m "feat: conversations, messaging, channels, delivery tracking"
```

---

### Task 7: MCP Server & Tool Handlers

**Files:**
- Create: `src/bifrost/mcp/server.py`
- Create: `src/bifrost/mcp/tools.py`
- Create: `tests/test_tools.py`

This task wires the hub logic to MCP tools. The exact MCP SDK API depends on spike results — the code below uses the low-level `Server` class as researched.

- [ ] **Step 1: Write tests for tool handlers**

```python
# tests/test_tools.py
"""Tests for MCP tool handlers.

Tests the tool handler functions directly (not via MCP protocol),
passing in a store and verifying state changes.
"""
import pytest

from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.hub.tasks import TaskHub
from bifrost.mcp.tools import ToolHandlers
from bifrost.store.db import Store


@pytest.fixture
def store(tmp_path):
    s = Store(str(tmp_path / "test.db"))
    s.initialize()
    return s


@pytest.fixture
def handlers(store):
    return ToolHandlers(
        agents=AgentHub(store),
        tasks=TaskHub(store),
        conversations=ConversationHub(store),
        delivery=DeliveryHub(store),
    )


def test_introduce_creates_card(handlers: ToolHandlers):
    # Simulate an agent connection
    agent = handlers.agents.register("api-backend")
    result = handlers.handle_introduce(
        agent_id=agent.id,
        description="Python backend agent",
        skills=[{"id": "tdd", "name": "TDD", "description": "Test-driven dev", "tags": ["testing"]}],
    )
    assert "Python backend agent" in result


def test_whoami_returns_identity(handlers: ToolHandlers):
    agent = handlers.agents.register("api-backend")
    handlers.handle_introduce(agent_id=agent.id, description="Backend agent")
    result = handlers.handle_whoami(agent_id=agent.id)
    assert "api-backend" in result
    assert "Backend agent" in result


def test_whoami_updates_status(handlers: ToolHandlers):
    agent = handlers.agents.register("api-backend")
    result = handlers.handle_whoami(agent_id=agent.id, status="dnd", dnd_reason="focusing")
    assert "dnd" in result


def test_list_agents(handlers: ToolHandlers):
    handlers.agents.register("agent-a")
    handlers.agents.register("agent-b")
    result = handlers.handle_list_agents()
    assert "agent-a" in result
    assert "agent-b" in result


def test_request_task(handlers: ToolHandlers):
    r = handlers.agents.register("requester")
    w = handlers.agents.register("worker")
    result = handlers.handle_request_task(
        requester_id=r.id,
        assignee_name="worker",
    )
    assert "queued" in result.lower() or "created" in result.lower()


def test_update_task(handlers: ToolHandlers):
    r = handlers.agents.register("requester")
    w = handlers.agents.register("worker")
    create_result = handlers.handle_request_task(requester_id=r.id, assignee_name="worker")
    # Extract task ID from result (handler returns it)
    task = handlers.tasks.list(assignee=w.id)[0]
    result = handlers.handle_update_task(task_id=task.id, status="running")
    assert "running" in result.lower()


def test_send_message(handlers: ToolHandlers):
    a = handlers.agents.register("alice")
    handlers.agents.register("bob")
    result = handlers.handle_send(from_agent_id=a.id, to="bob", body="Hello Bob")
    assert "sent" in result.lower() or "delivered" in result.lower()


def test_check_returns_pending(handlers: ToolHandlers):
    a = handlers.agents.register("alice")
    b = handlers.agents.register("bob")
    handlers.handle_send(from_agent_id=a.id, to="bob", body="Hello Bob")
    result = handlers.handle_check(agent_id=b.id)
    assert "Hello Bob" in result


def test_check_empty(handlers: ToolHandlers):
    a = handlers.agents.register("alice")
    result = handlers.handle_check(agent_id=a.id)
    assert "no pending" in result.lower() or result.strip() == ""
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_tools.py -v`
Expected: ImportError.

- [ ] **Step 3: Implement ToolHandlers**

```python
# src/bifrost/mcp/tools.py
"""MCP tool handler implementations.

Each method corresponds to one bifrost MCP tool. They receive parsed
arguments and the calling agent's ID, interact with hub logic, and
return a formatted text response.
"""
from __future__ import annotations

from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.hub.tasks import TaskHub, InvalidTransition
from bifrost.store.models import AgentSkill, AgentStatus, TaskStatus


class ToolHandlers:
    def __init__(
        self,
        agents: AgentHub,
        tasks: TaskHub,
        conversations: ConversationHub,
        delivery: DeliveryHub,
    ) -> None:
        self.agents = agents
        self.tasks = tasks
        self.conversations = conversations
        self.delivery = delivery

    def handle_introduce(
        self,
        agent_id: str,
        introduction: str = "",
        description: str = "",
        skills: list[dict] | None = None,
        capabilities: list[str] | None = None,
        limitations: str = "",
        **kwargs,
    ) -> str:
        parsed_skills = None
        if skills:
            parsed_skills = [
                AgentSkill(
                    id=s.get("id", s.get("name", "unknown")),
                    name=s.get("name", ""),
                    description=s.get("description", ""),
                    tags=s.get("tags", []),
                    examples=s.get("examples", []),
                )
                for s in skills
            ]

        metadata = {}
        if limitations:
            metadata["limitations"] = limitations

        card = self.agents.update_card(
            agent_id,
            introduction=introduction,
            description=description,
            skills=parsed_skills,
            metadata=metadata if metadata else None,
            **kwargs,
        )
        skill_names = ", ".join(s.name for s in card.skills) if card.skills else "none listed"
        return f"Card updated. Description: {card.description}. Skills: {skill_names}."

    def handle_whoami(
        self,
        agent_id: str,
        status: str | None = None,
        dnd_reason: str = "",
    ) -> str:
        if status:
            agent_status = AgentStatus(status)
            self.agents.update_status(agent_id, agent_status, reason=dnd_reason)

        agent = self.agents.get(agent_id)
        if not agent:
            return "Agent not found."

        lines = [f"Name: {agent.name}", f"Status: {agent.status.value}"]
        if agent.dnd_reason:
            lines.append(f"DND reason: {agent.dnd_reason}")
        if agent.card:
            if agent.card.description:
                lines.append(f"Description: {agent.card.description}")
            if agent.card.skills:
                skill_list = ", ".join(s.name for s in agent.card.skills)
                lines.append(f"Skills: {skill_list}")
        return "\n".join(lines)

    def handle_list_agents(self, status: str | None = None) -> str:
        filter_status = AgentStatus(status) if status else None
        agents = self.agents.list_all(status=filter_status)
        if not agents:
            return "No agents found."

        lines = []
        for a in agents:
            desc = ""
            if a.card and a.card.description:
                desc = f" — {a.card.description}"
            lines.append(f"  {a.name} ({a.status.value}){desc}")
        return "Agents:\n" + "\n".join(lines)

    def handle_request_task(
        self,
        requester_id: str,
        assignee_name: str,
        context_id: str = "",
        metadata: dict | None = None,
    ) -> str:
        assignee = self.agents.resolve(assignee_name)
        if not assignee:
            return f"Agent '{assignee_name}' not found."

        task = self.tasks.create(
            requester=requester_id,
            assignee=assignee.id,
            context_id=context_id,
            metadata=metadata,
        )
        return f"Task {task.id} created (status: queued, assignee: {assignee_name})."

    def handle_update_task(
        self,
        task_id: str,
        status: str | None = None,
        artifacts: list[dict] | None = None,
    ) -> str:
        try:
            if status:
                task = self.tasks.update_status(task_id, TaskStatus(status))
            if artifacts:
                task = self.tasks.update_artifacts(task_id, artifacts)
            if not status and not artifacts:
                return "Nothing to update."
            task = self.tasks.get(task_id)
            return f"Task {task_id} updated (status: {task.status.value})."
        except InvalidTransition as e:
            return f"Invalid transition: {e}"
        except ValueError as e:
            return str(e)

    def handle_get_task(self, task_id: str) -> str:
        task = self.tasks.get(task_id)
        if not task:
            return f"Task {task_id} not found."
        lines = [
            f"Task: {task.id}",
            f"Status: {task.status.value}",
            f"Requester: {task.requester}",
            f"Assignee: {task.assignee}",
        ]
        if task.context_id:
            lines.append(f"Context: {task.context_id}")
        if task.artifacts:
            lines.append(f"Artifacts: {len(task.artifacts)}")
        return "\n".join(lines)

    def handle_list_tasks(
        self,
        status: str | None = None,
        requester: str | None = None,
        assignee: str | None = None,
    ) -> str:
        filter_status = TaskStatus(status) if status else None
        tasks = self.tasks.list(status=filter_status, requester=requester, assignee=assignee)
        if not tasks:
            return "No tasks found."
        lines = []
        for t in tasks:
            lines.append(f"  {t.id} [{t.status.value}] requester={t.requester} assignee={t.assignee}")
        return "Tasks:\n" + "\n".join(lines)

    def handle_send(
        self,
        from_agent_id: str,
        to: str | None = None,
        channel: str | None = None,
        conversation_id: str | None = None,
        body: str = "",
    ) -> str:
        to_agent_id = None
        if to and not channel:
            # Resolve agent name
            target = self.agents.resolve(to)
            if not target:
                # Maybe it's a channel reference like "channel:deploys"
                if to.startswith("channel:"):
                    channel = to[len("channel:"):]
                else:
                    return f"Agent '{to}' not found."
            else:
                to_agent_id = target.id

        conv, event = self.conversations.send(
            from_agent=from_agent_id,
            to_agent=to_agent_id,
            channel=channel,
            conversation_id=conversation_id,
            text=body,
        )
        return f"Message sent (conversation: {conv.id})."

    def handle_list_conversations(self, channel: str | None = None) -> str:
        convos = self.conversations.list(channel=channel, active_only=True)
        if not convos:
            return "No conversations."
        lines = []
        for c in convos:
            prefix = f"[{c.channel}] " if c.channel else ""
            title = c.title or c.id
            parts = ", ".join(c.participants[:3])
            lines.append(f"  {prefix}{title} (participants: {parts})")
        return "Conversations:\n" + "\n".join(lines)

    def handle_subscribe(self, agent_id: str, target: str) -> str:
        self.conversations.subscribe(agent_id, target)
        return f"Subscribed to {target}."

    def handle_check(self, agent_id: str) -> str:
        events = self.delivery.check(agent_id)
        if not events:
            return "No pending messages."
        lines = []
        for e in events:
            parts = e.data.get("parts", [])
            text = parts[0].get("text", "") if parts else ""
            lines.append(f"[{e.conversation_id}] from {e.from_agent}: {text}")
        return "\n".join(lines)
```

- [ ] **Step 4: Implement MCP server wiring**

```python
# src/bifrost/mcp/server.py
"""MCP server setup — registers tools and wires to hub logic.

Uses the low-level mcp.server.Server class for experimental capability
support (claude/channel). Exact API validated in spike.
"""
from __future__ import annotations

from mcp.server import Server
from mcp.types import (
    CallToolResult,
    TextContent,
    Tool,
)

from bifrost.mcp.tools import ToolHandlers

TOOL_DEFINITIONS = [
    Tool(
        name="bifrost_introduce",
        description="Submit or update your Agent Card. Send structured fields and/or a freeform introduction.",
        inputSchema={
            "type": "object",
            "properties": {
                "introduction": {"type": "string", "description": "Freeform self-description (stored as card description)"},
                "description": {"type": "string", "description": "What you do (overrides introduction)"},
                "skills": {
                    "type": "array",
                    "description": "Your skills/capabilities",
                    "items": {
                        "type": "object",
                        "required": ["name", "description"],
                        "properties": {
                            "id": {"type": "string"},
                            "name": {"type": "string"},
                            "description": {"type": "string"},
                            "tags": {"type": "array", "items": {"type": "string"}},
                        },
                    },
                },
                "limitations": {"type": "string", "description": "What you cannot do"},
            },
        },
    ),
    Tool(
        name="bifrost_whoami",
        description="Get your identity and status. Pass status/dnd_reason to update.",
        inputSchema={
            "type": "object",
            "properties": {
                "status": {"type": "string", "enum": ["online", "idle", "dnd"], "description": "Set your status"},
                "dnd_reason": {"type": "string", "description": "Reason for DND"},
            },
        },
    ),
    Tool(
        name="bifrost_list_agents",
        description="List all agents with their cards and status.",
        inputSchema={
            "type": "object",
            "properties": {
                "status": {"type": "string", "enum": ["online", "idle", "offline", "dnd"], "description": "Filter by status"},
            },
        },
    ),
    Tool(
        name="bifrost_request_task",
        description="Create a task assigned to another agent.",
        inputSchema={
            "type": "object",
            "required": ["assignee"],
            "properties": {
                "assignee": {"type": "string", "description": "Agent name to assign the task to"},
                "context_id": {"type": "string", "description": "Group related tasks under one context"},
                "metadata": {"type": "object", "description": "Arbitrary task metadata"},
            },
        },
    ),
    Tool(
        name="bifrost_update_task",
        description="Update a task's status or artifacts.",
        inputSchema={
            "type": "object",
            "required": ["task_id"],
            "properties": {
                "task_id": {"type": "string"},
                "status": {
                    "type": "string",
                    "enum": ["running", "input-required", "auth-required", "completed", "failed", "canceled", "rejected"],
                },
                "artifacts": {"type": "array", "description": "Task output artifacts"},
            },
        },
    ),
    Tool(
        name="bifrost_get_task",
        description="Get full task details.",
        inputSchema={
            "type": "object",
            "required": ["task_id"],
            "properties": {"task_id": {"type": "string"}},
        },
    ),
    Tool(
        name="bifrost_list_tasks",
        description="List tasks, filterable by status/requester/assignee.",
        inputSchema={
            "type": "object",
            "properties": {
                "status": {"type": "string"},
                "requester": {"type": "string"},
                "assignee": {"type": "string"},
            },
        },
    ),
    Tool(
        name="bifrost_send",
        description="Send a message to an agent or channel.",
        inputSchema={
            "type": "object",
            "required": ["body"],
            "properties": {
                "to": {"type": "string", "description": "Agent name or channel:name"},
                "channel": {"type": "string", "description": "Channel name"},
                "conversation_id": {"type": "string", "description": "Continue existing conversation"},
                "body": {"type": "string", "description": "Message text"},
            },
        },
    ),
    Tool(
        name="bifrost_list_conversations",
        description="List conversations, filterable by channel prefix.",
        inputSchema={
            "type": "object",
            "properties": {
                "channel": {"type": "string", "description": "Filter by channel"},
            },
        },
    ),
    Tool(
        name="bifrost_subscribe",
        description="Subscribe to a channel prefix or specific conversation.",
        inputSchema={
            "type": "object",
            "required": ["target"],
            "properties": {
                "target": {"type": "string", "description": "Channel prefix or conversation ID"},
            },
        },
    ),
    Tool(
        name="bifrost_check",
        description="Check for pending messages and task updates. Returns undelivered events since last check.",
        inputSchema={
            "type": "object",
            "properties": {},
        },
    ),
]


def create_mcp_server(handlers: ToolHandlers) -> Server:
    """Create and configure the MCP server with all bifrost tools."""
    server = Server("bifrost")

    @server.list_tools()
    async def list_tools() -> list[Tool]:
        return TOOL_DEFINITIONS

    @server.call_tool()
    async def call_tool(name: str, arguments: dict) -> list[TextContent]:
        # TODO: Extract agent_id from MCP session/auth context.
        # The exact mechanism depends on spike results (how auth context
        # is threaded through the MCP SDK). For now, agent_id must be
        # passed or resolved from the session.
        agent_id = arguments.pop("_agent_id", "unknown")

        result = _dispatch(handlers, name, agent_id, arguments)
        return [TextContent(type="text", text=result)]

    return server


def _dispatch(handlers: ToolHandlers, name: str, agent_id: str, args: dict) -> str:
    match name:
        case "bifrost_introduce":
            return handlers.handle_introduce(agent_id=agent_id, **args)
        case "bifrost_whoami":
            return handlers.handle_whoami(agent_id=agent_id, **args)
        case "bifrost_list_agents":
            return handlers.handle_list_agents(**args)
        case "bifrost_request_task":
            return handlers.handle_request_task(requester_id=agent_id, **args)
        case "bifrost_update_task":
            return handlers.handle_update_task(**args)
        case "bifrost_get_task":
            return handlers.handle_get_task(**args)
        case "bifrost_list_tasks":
            return handlers.handle_list_tasks(**args)
        case "bifrost_send":
            return handlers.handle_send(from_agent_id=agent_id, **args)
        case "bifrost_list_conversations":
            return handlers.handle_list_conversations(**args)
        case "bifrost_subscribe":
            return handlers.handle_subscribe(agent_id=agent_id, **args)
        case "bifrost_check":
            return handlers.handle_check(agent_id=agent_id)
        case _:
            return f"Unknown tool: {name}"
```

- [ ] **Step 5: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_tools.py -v`
Expected: All pass.

- [ ] **Step 6: Commit**

```bash
git add src/bifrost/mcp/server.py src/bifrost/mcp/tools.py tests/test_tools.py
git commit -m "feat: MCP server with 11 bifrost tools"
```

---

### Task 8: FastAPI App & Config

**Files:**
- Create: `src/bifrost/config.py`
- Create: `src/bifrost/app.py`
- Create: `tests/test_app.py`

- [ ] **Step 1: Write tests**

```python
# tests/test_app.py
"""Tests for the FastAPI app."""
import pytest
from httpx import ASGITransport, AsyncClient

from bifrost.app import create_app
from bifrost.config import Config


@pytest.fixture
def config(tmp_path):
    return Config(db_path=str(tmp_path / "test.db"), insecure=True)


@pytest.fixture
async def client(config):
    app = create_app(config)
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as c:
        yield c


async def test_health_endpoint(client: AsyncClient):
    response = await client.get("/health")
    assert response.status_code == 200
    assert response.json()["status"] == "ok"


async def test_mcp_endpoint_exists(client: AsyncClient):
    response = await client.post("/mcp", json={"jsonrpc": "2.0", "method": "initialize", "id": 1, "params": {}})
    assert response.status_code != 404
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_app.py -v`
Expected: ImportError.

- [ ] **Step 3: Implement Config**

```python
# src/bifrost/config.py
"""Server configuration."""
from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Config:
    host: str = "0.0.0.0"
    port: int = 8000
    db_path: str = "bifrost.db"
    insecure: bool = False

    # OAuth settings (ignored when insecure=True)
    oauth_issuer_url: str = ""
    oauth_client_id: str = ""
    oauth_client_secret: str = ""
```

- [ ] **Step 4: Implement app factory**

```python
# src/bifrost/app.py
"""FastAPI application factory."""
from __future__ import annotations

import contextlib

from fastapi import FastAPI

from bifrost.config import Config
from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.hub.tasks import TaskHub
from bifrost.mcp.server import create_mcp_server
from bifrost.mcp.tools import ToolHandlers
from bifrost.store.db import Store


def create_app(config: Config) -> FastAPI:
    store = Store(config.db_path)

    @contextlib.asynccontextmanager
    async def lifespan(app: FastAPI):
        store.initialize()
        mcp = app.state.mcp_server
        async with contextlib.AsyncExitStack() as stack:
            await stack.enter_async_context(mcp.session_manager.run())
            yield
        store.close()

    app = FastAPI(title="Bifrost", lifespan=lifespan)

    # Hub logic
    agents = AgentHub(store)
    tasks = TaskHub(store)
    conversations = ConversationHub(store)
    delivery = DeliveryHub(store)
    handlers = ToolHandlers(agents, tasks, conversations, delivery)

    # MCP server
    mcp_server = create_mcp_server(handlers)
    app.state.mcp_server = mcp_server

    # Health check
    @app.get("/health")
    async def health():
        return {"status": "ok"}

    # Mount MCP
    app.mount("/mcp", mcp_server.streamable_http_app(streamable_http_path="/"))

    if config.insecure:
        import logging
        logging.warning("⚠ Running in insecure mode — no authentication. Do not expose to the network.")

    return app
```

- [ ] **Step 5: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_app.py -v`
Expected: All pass.

- [ ] **Step 6: Add CLI entry point**

Add to `pyproject.toml`:
```toml
[project.scripts]
bifrost = "bifrost.__main__:main"
```

Create:
```python
# src/bifrost/__main__.py
"""CLI entry point."""
import argparse

import uvicorn

from bifrost.app import create_app
from bifrost.config import Config


def main():
    parser = argparse.ArgumentParser(description="Bifrost — A2A-inspired communication hub over MCP")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8000)
    parser.add_argument("--db", default="bifrost.db", help="SQLite database path")
    parser.add_argument("--insecure", action="store_true", help="Disable authentication")
    args = parser.parse_args()

    config = Config(
        host=args.host,
        port=args.port,
        db_path=args.db,
        insecure=args.insecure,
    )
    app = create_app(config)
    uvicorn.run(app, host=config.host, port=config.port)


if __name__ == "__main__":
    main()
```

- [ ] **Step 7: Run the server manually**

Run: `cd /home/marc/dev/bifrost && python -m bifrost --insecure`
Expected: Server starts on port 8000, logs the insecure warning.

- [ ] **Step 8: Commit**

```bash
git add src/bifrost/config.py src/bifrost/app.py src/bifrost/__main__.py tests/test_app.py pyproject.toml
git commit -m "feat: FastAPI app with MCP mount and CLI entry point"
```

---

### Task 9: Auth — Insecure & OAuth

**Files:**
- Create: `src/bifrost/auth/insecure.py`
- Create: `src/bifrost/auth/oauth.py`
- Create: `tests/test_auth.py`

OAuth implementation depends on spike 2 results. This task provides the structure and the insecure path. OAuth wiring is filled in after the spike.

- [ ] **Step 1: Write tests**

```python
# tests/test_auth.py
"""Tests for auth modes."""
import pytest

from bifrost.auth.insecure import InsecureAgentIdentity
from bifrost.auth.oauth import OAuthAgentIdentity


def test_insecure_identity_from_header():
    identity = InsecureAgentIdentity()
    # In insecure mode, agent name comes from a header or tool argument
    agent_name = identity.resolve(headers={"x-bifrost-agent": "api-backend"})
    assert agent_name == "api-backend"


def test_insecure_identity_default():
    identity = InsecureAgentIdentity()
    agent_name = identity.resolve(headers={})
    assert agent_name is not None  # auto-generated


def test_oauth_identity_placeholder():
    # OAuth implementation depends on spike 2. This test ensures the class exists.
    identity = OAuthAgentIdentity(issuer_url="https://example.com", client_id="test")
    assert identity is not None
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_auth.py -v`
Expected: ImportError.

- [ ] **Step 3: Implement insecure auth**

```python
# src/bifrost/auth/insecure.py
"""Insecure mode — no authentication. Agent identity is self-declared."""
from __future__ import annotations

import uuid


class InsecureAgentIdentity:
    def resolve(self, headers: dict[str, str] | None = None) -> str:
        """Resolve agent name from request headers or generate one."""
        if headers and "x-bifrost-agent" in headers:
            return headers["x-bifrost-agent"]
        return f"agent-{uuid.uuid4().hex[:6]}"
```

- [ ] **Step 4: Implement OAuth placeholder**

```python
# src/bifrost/auth/oauth.py
"""OAuth 2.0 authentication.

Full implementation depends on spike 2 results (Claude Code OAuth flow,
MCP SDK auth middleware API). This provides the interface.
"""
from __future__ import annotations


class OAuthAgentIdentity:
    def __init__(self, issuer_url: str, client_id: str, client_secret: str = "") -> None:
        self.issuer_url = issuer_url
        self.client_id = client_id
        self.client_secret = client_secret

    def resolve(self, token: str) -> str | None:
        """Resolve agent identity from OAuth token.

        Returns the oauth_subject (e.g., email) or None if invalid.
        Implementation pending spike 2 results.
        """
        raise NotImplementedError("OAuth implementation pending spike 2 results")
```

- [ ] **Step 5: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_auth.py -v`
Expected: All pass.

- [ ] **Step 6: Commit**

```bash
git add src/bifrost/auth/insecure.py src/bifrost/auth/oauth.py tests/test_auth.py
git commit -m "feat: auth modes — insecure and OAuth placeholder"
```

---

### Task 10: MCP Server Instructions

**Files:**
- Modify: `src/bifrost/mcp/server.py`

The MCP server should include instructions that tell agents how to use bifrost, including the polling instruction for non-push clients.

- [ ] **Step 1: Add server instructions to MCP server**

Add to `src/bifrost/mcp/server.py`, inside `create_mcp_server()`:

```python
MCP_INSTRUCTIONS = """Bifrost connects you to other agents.

MESSAGES: Use bifrost_send to message agents or channels.
Messages from other agents arrive when you call bifrost_check.
Call bifrost_check regularly (after completing work, or every few minutes).

TASKS: Use bifrost_request_task to ask another agent to do work.
Use bifrost_update_task to accept/progress/complete tasks.
Task states: queued → running → completed/failed/canceled/rejected.
Use input-required when you need more info from the requester.

DISCOVERY: Use bifrost_list_agents to see who's available and what they can do.
Use bifrost_introduce to tell others about yourself.

CHANNELS: Use bifrost_subscribe to follow a topic (e.g., "deploys").
Use bifrost_send with channel parameter to broadcast.

STATUS: Use bifrost_whoami to check or update your status (including DND).
"""
```

Wire this into the server's `instructions` field (exact API depends on SDK — may be passed to `Server()` constructor or `create_initialization_options()`).

- [ ] **Step 2: Commit**

```bash
git add src/bifrost/mcp/server.py
git commit -m "feat: MCP server instructions for agent onboarding"
```

---

### Task 11: Integration Test — End-to-End

**Files:**
- Create: `tests/test_integration.py`

- [ ] **Step 1: Write integration test**

```python
# tests/test_integration.py
"""End-to-end integration test: two agents communicate via bifrost."""
import pytest

from bifrost.app import create_app
from bifrost.config import Config
from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.hub.tasks import TaskHub
from bifrost.mcp.tools import ToolHandlers
from bifrost.store.db import Store


@pytest.fixture
def store(tmp_path):
    s = Store(str(tmp_path / "test.db"))
    s.initialize()
    return s


@pytest.fixture
def handlers(store):
    return ToolHandlers(
        agents=AgentHub(store),
        tasks=TaskHub(store),
        conversations=ConversationHub(store),
        delivery=DeliveryHub(store),
    )


def test_two_agents_message_exchange(handlers: ToolHandlers):
    """Alice sends a message to Bob. Bob checks and sees it."""
    # Register agents
    alice = handlers.agents.register("alice")
    bob = handlers.agents.register("bob")

    # Alice introduces herself
    handlers.handle_introduce(alice.id, description="Frontend agent")

    # Alice sends message to Bob
    handlers.handle_send(from_agent_id=alice.id, to="bob", body="Can you review my PR?")

    # Bob checks for messages
    result = handlers.handle_check(agent_id=bob.id)
    assert "Can you review my PR?" in result

    # Bob checks again — no new messages
    result = handlers.handle_check(agent_id=bob.id)
    assert "No pending" in result


def test_task_lifecycle(handlers: ToolHandlers):
    """Full task lifecycle: create, accept, complete."""
    lead = handlers.agents.register("lead")
    worker = handlers.agents.register("worker")

    # Lead creates task
    handlers.handle_request_task(requester_id=lead.id, assignee_name="worker")
    tasks = handlers.tasks.list(assignee=worker.id)
    assert len(tasks) == 1
    task_id = tasks[0].id

    # Worker accepts (queued -> running)
    handlers.handle_update_task(task_id=task_id, status="running")

    # Worker completes
    handlers.handle_update_task(task_id=task_id, status="completed")

    result = handlers.handle_get_task(task_id=task_id)
    assert "completed" in result


def test_channel_broadcast(handlers: ToolHandlers):
    """Agent publishes to channel, subscriber receives."""
    alice = handlers.agents.register("alice")
    bob = handlers.agents.register("bob")

    # Bob subscribes to deploys channel
    handlers.handle_subscribe(agent_id=bob.id, target="deploys")

    # Alice broadcasts to deploys channel
    handlers.handle_send(from_agent_id=alice.id, channel="deploys", body="Deploying v2.1")

    # Bob checks — should see the broadcast
    result = handlers.handle_check(agent_id=bob.id)
    assert "Deploying v2.1" in result


def test_agent_discovery(handlers: ToolHandlers):
    """Agents can discover each other via cards."""
    a = handlers.agents.register("api-backend")
    handlers.handle_introduce(
        a.id,
        description="Python/FastAPI backend",
        skills=[{"name": "TDD", "description": "Test-driven development", "tags": ["testing"]}],
    )

    b = handlers.agents.register("frontend")
    handlers.handle_introduce(b.id, introduction="React/TypeScript UI developer")

    result = handlers.handle_list_agents()
    assert "api-backend" in result
    assert "Python/FastAPI backend" in result
    assert "frontend" in result
```

- [ ] **Step 2: Run integration tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_integration.py -v`
Expected: All pass.

- [ ] **Step 3: Run full test suite**

Run: `cd /home/marc/dev/bifrost && pytest -v`
Expected: All tests pass across all test files.

- [ ] **Step 4: Commit**

```bash
git add tests/test_integration.py
git commit -m "test: end-to-end integration tests for agent messaging, tasks, channels"
```
