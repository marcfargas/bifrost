"""SQLite persistence layer for Bifrost v2."""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone
from pathlib import Path

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

_SCHEMA = """
CREATE TABLE IF NOT EXISTS agents (
    id          TEXT PRIMARY KEY,
    name        TEXT UNIQUE NOT NULL,
    status      TEXT NOT NULL DEFAULT 'offline',
    dnd_reason  TEXT,
    connected_at TEXT,
    last_seen   TEXT,
    oauth_subject TEXT
);

CREATE TABLE IF NOT EXISTS agent_cards (
    agent_id            TEXT PRIMARY KEY REFERENCES agents(id),
    description         TEXT NOT NULL DEFAULT '',
    version             TEXT NOT NULL DEFAULT '',
    icon_url            TEXT NOT NULL DEFAULT '',
    provider            TEXT NOT NULL DEFAULT '',
    capabilities        TEXT NOT NULL DEFAULT '{}',
    skills              TEXT NOT NULL DEFAULT '[]',
    default_input_modes TEXT NOT NULL DEFAULT '[]',
    default_output_modes TEXT NOT NULL DEFAULT '[]',
    metadata            TEXT NOT NULL DEFAULT '{}',
    updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id              TEXT PRIMARY KEY,
    requester       TEXT NOT NULL,
    assignee        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued',
    context_id      TEXT,
    status_message  TEXT,
    artifacts       TEXT NOT NULL DEFAULT '[]',
    metadata        TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS conversations (
    id            TEXT PRIMARY KEY,
    title         TEXT NOT NULL DEFAULT '',
    channel       TEXT,
    participants  TEXT NOT NULL DEFAULT '[]',
    closed        INTEGER NOT NULL DEFAULT 0,
    closed_reason TEXT,
    created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    type            TEXT NOT NULL DEFAULT 'message',
    from_agent      TEXT NOT NULL,
    data            TEXT NOT NULL DEFAULT '{}',
    timestamp       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_conv ON events(conversation_id, timestamp);

CREATE TABLE IF NOT EXISTS delivery_state (
    agent_id        TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    last_event_id   TEXT NOT NULL,
    PRIMARY KEY (agent_id, conversation_id)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    agent_id TEXT NOT NULL,
    target   TEXT NOT NULL,
    PRIMARY KEY (agent_id, target)
);

-- OAuth persistence (survives restarts)
CREATE TABLE IF NOT EXISTS oauth_clients (
    client_id   TEXT PRIMARY KEY,
    client_info TEXT NOT NULL,  -- JSON: OAuthClientInformationFull
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_access_tokens (
    token       TEXT PRIMARY KEY,
    client_id   TEXT NOT NULL,
    scopes      TEXT NOT NULL DEFAULT '[]',  -- JSON array
    expires_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
    token       TEXT PRIMARY KEY,
    client_id   TEXT NOT NULL,
    scopes      TEXT NOT NULL DEFAULT '[]',  -- JSON array
    expires_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_auth_codes (
    code        TEXT PRIMARY KEY,
    client_id   TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    redirect_uri_provided_explicitly INTEGER NOT NULL DEFAULT 1,
    code_challenge TEXT NOT NULL,
    scopes      TEXT NOT NULL DEFAULT '[]',  -- JSON array
    expires_at  REAL NOT NULL
);
"""


class Store:
    """Synchronous SQLite store for Bifrost data."""

    def __init__(self, path: str | Path) -> None:
        self._path = str(path)
        Path(self._path).parent.mkdir(parents=True, exist_ok=True)
        self._conn = sqlite3.connect(self._path, check_same_thread=False)
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute("PRAGMA foreign_keys=ON")
        self._conn.row_factory = sqlite3.Row
        self._conn.executescript(_SCHEMA)

    def close(self) -> None:
        self._conn.close()

    # ------------------------------------------------------------------
    # Agents
    # ------------------------------------------------------------------

    def upsert_agent(self, agent: Agent) -> None:
        self._conn.execute(
            """INSERT INTO agents (id, name, status, dnd_reason, connected_at, last_seen, oauth_subject)
               VALUES (?, ?, ?, ?, ?, ?, ?)
               ON CONFLICT(id) DO UPDATE SET
                 name=excluded.name, status=excluded.status, dnd_reason=excluded.dnd_reason,
                 connected_at=excluded.connected_at, last_seen=excluded.last_seen,
                 oauth_subject=excluded.oauth_subject""",
            (agent.id, agent.name, agent.status, agent.dnd_reason,
             agent.connected_at, agent.last_seen, agent.oauth_subject),
        )
        self._conn.commit()

    def get_agent(self, agent_id: str) -> Agent | None:
        row = self._conn.execute("SELECT * FROM agents WHERE id=?", (agent_id,)).fetchone()
        return self._row_to_agent(row) if row else None

    def get_agent_by_name(self, name: str) -> Agent | None:
        row = self._conn.execute("SELECT * FROM agents WHERE name=?", (name,)).fetchone()
        return self._row_to_agent(row) if row else None

    def list_agents(self, status: AgentStatus | None = None) -> list[Agent]:
        if status:
            rows = self._conn.execute("SELECT * FROM agents WHERE status=?", (status,)).fetchall()
        else:
            rows = self._conn.execute("SELECT * FROM agents").fetchall()
        return [self._row_to_agent(r) for r in rows]

    @staticmethod
    def _row_to_agent(row: sqlite3.Row) -> Agent:
        return Agent(
            id=row["id"], name=row["name"],
            status=AgentStatus(row["status"]),
            dnd_reason=row["dnd_reason"],
            connected_at=row["connected_at"],
            last_seen=row["last_seen"],
            oauth_subject=row["oauth_subject"],
        )

    # ------------------------------------------------------------------
    # Agent Cards
    # ------------------------------------------------------------------

    def upsert_card(self, card: AgentCard) -> None:
        skills_json = json.dumps([_skill_to_dict(s) for s in card.skills])
        self._conn.execute(
            """INSERT INTO agent_cards
                 (agent_id, description, version, icon_url, provider,
                  capabilities, skills, default_input_modes, default_output_modes,
                  metadata, updated_at)
               VALUES (?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(agent_id) DO UPDATE SET
                 description=excluded.description, version=excluded.version,
                 icon_url=excluded.icon_url, provider=excluded.provider,
                 capabilities=excluded.capabilities, skills=excluded.skills,
                 default_input_modes=excluded.default_input_modes,
                 default_output_modes=excluded.default_output_modes,
                 metadata=excluded.metadata, updated_at=excluded.updated_at""",
            (card.agent_id, card.description, card.version, card.icon_url,
             card.provider, json.dumps(card.capabilities), skills_json,
             json.dumps(card.default_input_modes), json.dumps(card.default_output_modes),
             json.dumps(card.metadata), card.updated_at),
        )
        self._conn.commit()

    def get_card(self, agent_id: str) -> AgentCard | None:
        row = self._conn.execute("SELECT * FROM agent_cards WHERE agent_id=?", (agent_id,)).fetchone()
        return self._row_to_card(row) if row else None

    @staticmethod
    def _row_to_card(row: sqlite3.Row) -> AgentCard:
        skills_data = json.loads(row["skills"])
        skills = [AgentSkill(**s) for s in skills_data]
        return AgentCard(
            agent_id=row["agent_id"],
            description=row["description"],
            version=row["version"],
            icon_url=row["icon_url"],
            provider=row["provider"],
            capabilities=json.loads(row["capabilities"]),
            skills=skills,
            default_input_modes=json.loads(row["default_input_modes"]),
            default_output_modes=json.loads(row["default_output_modes"]),
            metadata=json.loads(row["metadata"]),
            updated_at=row["updated_at"],
        )

    # ------------------------------------------------------------------
    # Tasks
    # ------------------------------------------------------------------

    def create_task(self, task: Task) -> None:
        self._conn.execute(
            """INSERT INTO tasks (id, requester, assignee, status, context_id,
                 status_message, artifacts, metadata, created_at, updated_at)
               VALUES (?,?,?,?,?,?,?,?,?,?)""",
            (task.id, task.requester, task.assignee, task.status,
             task.context_id, task.status_message,
             json.dumps([_artifact_to_dict(a) for a in task.artifacts]),
             json.dumps(task.metadata), task.created_at, task.updated_at),
        )
        self._conn.commit()

    def get_task(self, task_id: str) -> Task | None:
        row = self._conn.execute("SELECT * FROM tasks WHERE id=?", (task_id,)).fetchone()
        return self._row_to_task(row) if row else None

    def update_task(self, task_id: str, **kwargs: object) -> None:
        allowed = {"status", "status_message", "artifacts", "metadata", "updated_at", "context_id"}
        sets: list[str] = []
        vals: list[object] = []
        for k, v in kwargs.items():
            if k not in allowed:
                raise ValueError(f"Cannot update field: {k}")
            if k == "artifacts":
                v = json.dumps([_artifact_to_dict(a) for a in v])  # type: ignore[union-attr]
            elif k == "metadata":
                v = json.dumps(v)
            sets.append(f"{k}=?")
            vals.append(v)
        if not sets:
            return
        vals.append(task_id)
        self._conn.execute(f"UPDATE tasks SET {', '.join(sets)} WHERE id=?", vals)
        self._conn.commit()

    def list_tasks(
        self,
        status: TaskStatus | None = None,
        requester: str | None = None,
        assignee: str | None = None,
    ) -> list[Task]:
        clauses: list[str] = []
        params: list[str] = []
        if status:
            clauses.append("status=?")
            params.append(status)
        if requester:
            clauses.append("requester=?")
            params.append(requester)
        if assignee:
            clauses.append("assignee=?")
            params.append(assignee)
        where = " WHERE " + " AND ".join(clauses) if clauses else ""
        rows = self._conn.execute(f"SELECT * FROM tasks{where}", params).fetchall()
        return [self._row_to_task(r) for r in rows]

    @staticmethod
    def _row_to_task(row: sqlite3.Row) -> Task:
        artifacts_data = json.loads(row["artifacts"])
        artifacts = [_dict_to_artifact(a) for a in artifacts_data]
        return Task(
            id=row["id"], requester=row["requester"], assignee=row["assignee"],
            status=TaskStatus(row["status"]),
            context_id=row["context_id"], status_message=row["status_message"],
            artifacts=artifacts, metadata=json.loads(row["metadata"]),
            created_at=row["created_at"], updated_at=row["updated_at"],
        )

    # ------------------------------------------------------------------
    # Conversations
    # ------------------------------------------------------------------

    def create_conversation(self, conv: Conversation) -> None:
        self._conn.execute(
            """INSERT INTO conversations (id, title, channel, participants, closed, closed_reason, created_at)
               VALUES (?,?,?,?,?,?,?)""",
            (conv.id, conv.title, conv.channel, json.dumps(conv.participants),
             int(conv.closed), conv.closed_reason, conv.created_at),
        )
        self._conn.commit()

    def get_conversation(self, conv_id: str) -> Conversation | None:
        row = self._conn.execute("SELECT * FROM conversations WHERE id=?", (conv_id,)).fetchone()
        return self._row_to_conversation(row) if row else None

    def update_conversation(self, conv_id: str, **kwargs: object) -> None:
        allowed = {"title", "participants", "closed", "closed_reason"}
        sets: list[str] = []
        vals: list[object] = []
        for k, v in kwargs.items():
            if k not in allowed:
                raise ValueError(f"Cannot update field: {k}")
            if k == "participants":
                v = json.dumps(v)
            elif k == "closed":
                v = int(v)  # type: ignore[arg-type]
            sets.append(f"{k}=?")
            vals.append(v)
        if not sets:
            return
        vals.append(conv_id)
        self._conn.execute(f"UPDATE conversations SET {', '.join(sets)} WHERE id=?", vals)
        self._conn.commit()

    def list_conversations(
        self,
        channel: str | None = None,
        active_only: bool = False,
    ) -> list[Conversation]:
        clauses: list[str] = []
        params: list[object] = []
        if channel:
            clauses.append("channel=?")
            params.append(channel)
        if active_only:
            clauses.append("closed=0")
        where = " WHERE " + " AND ".join(clauses) if clauses else ""
        rows = self._conn.execute(f"SELECT * FROM conversations{where}", params).fetchall()
        return [self._row_to_conversation(r) for r in rows]

    @staticmethod
    def _row_to_conversation(row: sqlite3.Row) -> Conversation:
        return Conversation(
            id=row["id"], title=row["title"], channel=row["channel"],
            participants=json.loads(row["participants"]),
            closed=bool(row["closed"]), closed_reason=row["closed_reason"],
            created_at=row["created_at"],
        )

    # ------------------------------------------------------------------
    # Events
    # ------------------------------------------------------------------

    def append_event(self, event: Event) -> None:
        self._conn.execute(
            """INSERT INTO events (id, conversation_id, type, from_agent, data, timestamp)
               VALUES (?,?,?,?,?,?)""",
            (event.id, event.conversation_id, event.type, event.from_agent,
             json.dumps(event.data), event.timestamp),
        )
        self._conn.commit()

    def list_events(self, conversation_id: str, after_event_id: str | None = None) -> list[Event]:
        if after_event_id:
            # Get timestamp of the reference event, then return events after it
            ref = self._conn.execute(
                "SELECT timestamp FROM events WHERE id=?", (after_event_id,)
            ).fetchone()
            if not ref:
                return []
            rows = self._conn.execute(
                """SELECT * FROM events
                   WHERE conversation_id=? AND (timestamp > ? OR (timestamp = ? AND id > ?))
                   ORDER BY timestamp, id""",
                (conversation_id, ref["timestamp"], ref["timestamp"], after_event_id),
            ).fetchall()
        else:
            rows = self._conn.execute(
                "SELECT * FROM events WHERE conversation_id=? ORDER BY timestamp, id",
                (conversation_id,),
            ).fetchall()
        return [self._row_to_event(r) for r in rows]

    @staticmethod
    def _row_to_event(row: sqlite3.Row) -> Event:
        return Event(
            id=row["id"], conversation_id=row["conversation_id"],
            type=EventType(row["type"]), from_agent=row["from_agent"],
            data=json.loads(row["data"]), timestamp=row["timestamp"],
        )

    # ------------------------------------------------------------------
    # Delivery state
    # ------------------------------------------------------------------

    def get_last_delivered_event(self, agent_id: str, conversation_id: str) -> str | None:
        row = self._conn.execute(
            "SELECT last_event_id FROM delivery_state WHERE agent_id=? AND conversation_id=?",
            (agent_id, conversation_id),
        ).fetchone()
        return row["last_event_id"] if row else None

    def set_last_delivered_event(self, agent_id: str, conversation_id: str, event_id: str) -> None:
        self._conn.execute(
            """INSERT INTO delivery_state (agent_id, conversation_id, last_event_id)
               VALUES (?,?,?)
               ON CONFLICT(agent_id, conversation_id) DO UPDATE SET last_event_id=excluded.last_event_id""",
            (agent_id, conversation_id, event_id),
        )
        self._conn.commit()

    # ------------------------------------------------------------------
    # Subscriptions
    # ------------------------------------------------------------------

    def subscribe(self, agent_id: str, target: str) -> None:
        self._conn.execute(
            "INSERT OR IGNORE INTO subscriptions (agent_id, target) VALUES (?,?)",
            (agent_id, target),
        )
        self._conn.commit()

    def unsubscribe(self, agent_id: str, target: str) -> None:
        self._conn.execute(
            "DELETE FROM subscriptions WHERE agent_id=? AND target=?",
            (agent_id, target),
        )
        self._conn.commit()

    def list_subscriptions(self, agent_id: str) -> list[str]:
        rows = self._conn.execute(
            "SELECT target FROM subscriptions WHERE agent_id=?", (agent_id,),
        ).fetchall()
        return [r["target"] for r in rows]

    def list_subscribers(self, target: str) -> list[str]:
        rows = self._conn.execute(
            "SELECT agent_id FROM subscriptions WHERE target=?", (target,),
        ).fetchall()
        return [r["agent_id"] for r in rows]

    # ------------------------------------------------------------------
    # OAuth persistence
    # ------------------------------------------------------------------

    def save_oauth_client(self, client_id: str, client_info_json: str) -> None:
        self._conn.execute(
            "INSERT OR REPLACE INTO oauth_clients (client_id, client_info, created_at) VALUES (?, ?, ?)",
            (client_id, client_info_json, datetime.now(timezone.utc).isoformat()),
        )
        self._conn.commit()

    def get_oauth_client(self, client_id: str) -> str | None:
        row = self._conn.execute(
            "SELECT client_info FROM oauth_clients WHERE client_id=?", (client_id,),
        ).fetchone()
        return row["client_info"] if row else None

    def save_oauth_access_token(
        self, token: str, client_id: str, scopes: list[str], expires_at: int,
    ) -> None:
        self._conn.execute(
            "INSERT OR REPLACE INTO oauth_access_tokens (token, client_id, scopes, expires_at) VALUES (?, ?, ?, ?)",
            (token, client_id, json.dumps(scopes), expires_at),
        )
        self._conn.commit()

    def get_oauth_access_token(self, token: str) -> dict | None:
        row = self._conn.execute(
            "SELECT * FROM oauth_access_tokens WHERE token=?", (token,),
        ).fetchone()
        if not row:
            return None
        return {"token": row["token"], "client_id": row["client_id"],
                "scopes": json.loads(row["scopes"]), "expires_at": row["expires_at"]}

    def delete_oauth_access_token(self, token: str) -> None:
        self._conn.execute("DELETE FROM oauth_access_tokens WHERE token=?", (token,))
        self._conn.commit()

    def save_oauth_refresh_token(
        self, token: str, client_id: str, scopes: list[str], expires_at: int,
    ) -> None:
        self._conn.execute(
            "INSERT OR REPLACE INTO oauth_refresh_tokens (token, client_id, scopes, expires_at) VALUES (?, ?, ?, ?)",
            (token, client_id, json.dumps(scopes), expires_at),
        )
        self._conn.commit()

    def get_oauth_refresh_token(self, token: str) -> dict | None:
        row = self._conn.execute(
            "SELECT * FROM oauth_refresh_tokens WHERE token=?", (token,),
        ).fetchone()
        if not row:
            return None
        return {"token": row["token"], "client_id": row["client_id"],
                "scopes": json.loads(row["scopes"]), "expires_at": row["expires_at"]}

    def delete_oauth_refresh_token(self, token: str) -> None:
        self._conn.execute("DELETE FROM oauth_refresh_tokens WHERE token=?", (token,))
        self._conn.commit()

    def save_oauth_auth_code(
        self, code: str, client_id: str, redirect_uri: str,
        redirect_uri_provided_explicitly: bool, code_challenge: str,
        scopes: list[str], expires_at: float,
    ) -> None:
        self._conn.execute(
            """INSERT OR REPLACE INTO oauth_auth_codes
               (code, client_id, redirect_uri, redirect_uri_provided_explicitly, code_challenge, scopes, expires_at)
               VALUES (?, ?, ?, ?, ?, ?, ?)""",
            (code, client_id, redirect_uri, int(redirect_uri_provided_explicitly),
             code_challenge, json.dumps(scopes), expires_at),
        )
        self._conn.commit()

    def get_oauth_auth_code(self, code: str) -> dict | None:
        row = self._conn.execute(
            "SELECT * FROM oauth_auth_codes WHERE code=?", (code,),
        ).fetchone()
        if not row:
            return None
        return {
            "code": row["code"], "client_id": row["client_id"],
            "redirect_uri": row["redirect_uri"],
            "redirect_uri_provided_explicitly": bool(row["redirect_uri_provided_explicitly"]),
            "code_challenge": row["code_challenge"],
            "scopes": json.loads(row["scopes"]), "expires_at": row["expires_at"],
        }

    def delete_oauth_auth_code(self, code: str) -> None:
        self._conn.execute("DELETE FROM oauth_auth_codes WHERE code=?", (code,))
        self._conn.commit()


# ---------------------------------------------------------------------------
# Serialization helpers
# ---------------------------------------------------------------------------

def _skill_to_dict(s: AgentSkill) -> dict:
    return {
        "id": s.id, "name": s.name, "description": s.description,
        "tags": s.tags, "examples": s.examples,
        "input_modes": s.input_modes, "output_modes": s.output_modes,
    }


def _artifact_to_dict(a: object) -> dict:
    from bifrost.store.models import Artifact, TextPart, FilePart, DataPart
    assert isinstance(a, Artifact)
    parts = []
    for p in a.parts:
        if isinstance(p, TextPart):
            parts.append({"type": "text", "text": p.text})
        elif isinstance(p, FilePart):
            parts.append({"type": "file", "mime_type": p.mime_type, "uri": p.uri, "data": p.data})
        elif isinstance(p, DataPart):
            parts.append({"type": "data", "data": p.data})
    return {"id": a.id, "parts": parts, "metadata": a.metadata}


def _dict_to_artifact(d: dict) -> object:
    from bifrost.store.models import Artifact, TextPart, FilePart, DataPart
    parts = []
    for p in d.get("parts", []):
        if p["type"] == "text":
            parts.append(TextPart(text=p["text"]))
        elif p["type"] == "file":
            parts.append(FilePart(mime_type=p["mime_type"], uri=p.get("uri"), data=p.get("data")))
        elif p["type"] == "data":
            parts.append(DataPart(data=p.get("data", {})))
    return Artifact(id=d["id"], parts=parts, metadata=d.get("metadata", {}))
