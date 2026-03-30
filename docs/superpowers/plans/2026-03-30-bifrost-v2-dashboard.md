# Bifrost v2 — Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Jinja2+htmx observability dashboard for bifrost v2 — agent directory, task board, activity feed, conversation viewer.

**Architecture:** FastAPI routes serving Jinja2 templates with htmx for interactivity. Same app, same database, same OAuth. Read-heavy view layer.

**Tech Stack:** Jinja2, htmx, FastAPI, minimal CSS (no build toolchain)

**Spec:** `docs/superpowers/specs/2026-03-30-bifrost-v2-design.md`

**Prerequisite:** Core server must be working. See `docs/superpowers/plans/2026-03-30-bifrost-v2-core.md`.

---

### Task 1: Dashboard Skeleton

**Files:**
- Create: `src/bifrost/dashboard/routes.py`
- Create: `src/bifrost/dashboard/templates/base.html`
- Create: `src/bifrost/dashboard/templates/agents.html`
- Create: `src/bifrost/dashboard/static/style.css`
- Modify: `src/bifrost/app.py` — mount dashboard routes
- Create: `tests/test_dashboard.py`

- [ ] **Step 1: Write tests**

```python
# tests/test_dashboard.py
"""Tests for dashboard routes."""
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


async def test_dashboard_root_redirects_to_agents(client: AsyncClient):
    response = await client.get("/", follow_redirects=False)
    assert response.status_code in (301, 302, 307)
    assert "/agents" in response.headers.get("location", "")


async def test_agents_page(client: AsyncClient):
    response = await client.get("/agents")
    assert response.status_code == 200
    assert "Bifrost" in response.text
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_dashboard.py -v`
Expected: Fail — no dashboard routes.

- [ ] **Step 3: Create base template**

```html
<!-- src/bifrost/dashboard/templates/base.html -->
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{% block title %}Bifrost{% endblock %}</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="stylesheet" href="/static/style.css">
</head>
<body>
    <nav>
        <a href="/agents" class="{% if active == 'agents' %}active{% endif %}">Agents</a>
        <a href="/tasks" class="{% if active == 'tasks' %}active{% endif %}">Tasks</a>
        <a href="/conversations" class="{% if active == 'conversations' %}active{% endif %}">Conversations</a>
        <a href="/activity" class="{% if active == 'activity' %}active{% endif %}">Activity</a>
    </nav>
    <main>
        {% block content %}{% endblock %}
    </main>
</body>
</html>
```

- [ ] **Step 4: Create agents page template**

```html
<!-- src/bifrost/dashboard/templates/agents.html -->
{% extends "base.html" %}
{% block title %}Agents — Bifrost{% endblock %}
{% block content %}
<h1>Agents</h1>
<div id="agent-list" hx-get="/agents/list" hx-trigger="load, every 10s" hx-swap="innerHTML">
    Loading...
</div>
{% endblock %}
```

- [ ] **Step 5: Create agent list partial (htmx target)**

```html
<!-- src/bifrost/dashboard/templates/partials/agent_list.html -->
{% if agents %}
<table>
    <thead>
        <tr>
            <th>Name</th>
            <th>Status</th>
            <th>Description</th>
            <th>Skills</th>
            <th>Last Seen</th>
        </tr>
    </thead>
    <tbody>
        {% for agent in agents %}
        <tr class="status-{{ agent.status.value }}">
            <td>{{ agent.name }}</td>
            <td>
                <span class="status-badge {{ agent.status.value }}">{{ agent.status.value }}</span>
                {% if agent.dnd_reason %}<small>({{ agent.dnd_reason }})</small>{% endif %}
            </td>
            <td>{{ agent.card.description if agent.card else '' }}</td>
            <td>
                {% if agent.card and agent.card.skills %}
                    {% for skill in agent.card.skills %}
                    <span class="tag">{{ skill.name }}</span>
                    {% endfor %}
                {% endif %}
            </td>
            <td>{{ agent.last_seen.strftime('%H:%M:%S') if agent.last_seen else 'never' }}</td>
        </tr>
        {% endfor %}
    </tbody>
</table>
{% else %}
<p>No agents registered.</p>
{% endif %}
```

- [ ] **Step 6: Create minimal CSS**

```css
/* src/bifrost/dashboard/static/style.css */
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: system-ui, sans-serif; max-width: 1200px; margin: 0 auto; padding: 1rem; }
nav { display: flex; gap: 1rem; padding: 0.5rem 0; border-bottom: 1px solid #ddd; margin-bottom: 1rem; }
nav a { text-decoration: none; color: #666; padding: 0.25rem 0.5rem; }
nav a.active { color: #000; font-weight: bold; border-bottom: 2px solid #000; }
h1 { margin-bottom: 1rem; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 0.5rem; border-bottom: 1px solid #eee; }
th { font-weight: 600; }
.status-badge { padding: 0.125rem 0.5rem; border-radius: 3px; font-size: 0.85em; }
.status-badge.online { background: #d4edda; color: #155724; }
.status-badge.idle { background: #fff3cd; color: #856404; }
.status-badge.offline { background: #e2e3e5; color: #383d41; }
.status-badge.dnd { background: #f8d7da; color: #721c24; }
.tag { background: #e9ecef; padding: 0.125rem 0.375rem; border-radius: 3px; font-size: 0.8em; margin-right: 0.25rem; }
```

- [ ] **Step 7: Implement dashboard routes**

```python
# src/bifrost/dashboard/routes.py
"""Dashboard routes — Jinja2 + htmx."""
from __future__ import annotations

from pathlib import Path

from fastapi import APIRouter, Request
from fastapi.responses import HTMLResponse, RedirectResponse
from fastapi.templating import Jinja2Templates

from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.tasks import TaskHub
from bifrost.store.db import Store

TEMPLATE_DIR = Path(__file__).parent / "templates"


def create_dashboard_router(store: Store) -> APIRouter:
    router = APIRouter()
    templates = Jinja2Templates(directory=str(TEMPLATE_DIR))
    agents_hub = AgentHub(store)
    tasks_hub = TaskHub(store)
    conversations_hub = ConversationHub(store)

    @router.get("/", response_class=RedirectResponse)
    async def index():
        return RedirectResponse(url="/agents", status_code=307)

    @router.get("/agents", response_class=HTMLResponse)
    async def agents_page(request: Request):
        return templates.TemplateResponse("agents.html", {"request": request, "active": "agents"})

    @router.get("/agents/list", response_class=HTMLResponse)
    async def agents_list(request: Request):
        all_agents = agents_hub.list_all()
        return templates.TemplateResponse(
            "partials/agent_list.html", {"request": request, "agents": all_agents}
        )

    return router
```

- [ ] **Step 8: Mount dashboard in app.py**

Add to `src/bifrost/app.py`, inside `create_app()`, after the MCP mount:

```python
from fastapi.staticfiles import StaticFiles
from bifrost.dashboard.routes import create_dashboard_router

# Dashboard
dashboard_router = create_dashboard_router(store)
app.include_router(dashboard_router)

# Static files
static_dir = Path(__file__).parent / "dashboard" / "static"
app.mount("/static", StaticFiles(directory=str(static_dir)), name="static")
```

- [ ] **Step 9: Create partials directory**

```bash
mkdir -p src/bifrost/dashboard/templates/partials
```

- [ ] **Step 10: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_dashboard.py -v`
Expected: All pass.

- [ ] **Step 11: Commit**

```bash
git add src/bifrost/dashboard/ src/bifrost/app.py tests/test_dashboard.py
git commit -m "feat: dashboard skeleton with agents page and htmx auto-refresh"
```

---

### Task 2: Task Board Page

**Files:**
- Create: `src/bifrost/dashboard/templates/tasks.html`
- Create: `src/bifrost/dashboard/templates/partials/task_list.html`
- Modify: `src/bifrost/dashboard/routes.py`
- Modify: `tests/test_dashboard.py`

- [ ] **Step 1: Add test**

```python
# Add to tests/test_dashboard.py

async def test_tasks_page(client: AsyncClient):
    response = await client.get("/tasks")
    assert response.status_code == 200
    assert "Tasks" in response.text
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_dashboard.py::test_tasks_page -v`
Expected: 404.

- [ ] **Step 3: Create tasks page template**

```html
<!-- src/bifrost/dashboard/templates/tasks.html -->
{% extends "base.html" %}
{% block title %}Tasks — Bifrost{% endblock %}
{% block content %}
<h1>Tasks</h1>
<div id="task-list" hx-get="/tasks/list" hx-trigger="load, every 10s" hx-swap="innerHTML">
    Loading...
</div>
{% endblock %}
```

- [ ] **Step 4: Create task list partial**

```html
<!-- src/bifrost/dashboard/templates/partials/task_list.html -->
{% if tasks %}
<table>
    <thead>
        <tr>
            <th>ID</th>
            <th>Status</th>
            <th>Requester</th>
            <th>Assignee</th>
            <th>Created</th>
        </tr>
    </thead>
    <tbody>
        {% for task in tasks %}
        <tr class="{% if task.stale %}stale{% endif %}">
            <td>{{ task.id }}</td>
            <td><span class="status-badge {{ task.status.value }}">{{ task.status.value }}</span></td>
            <td>{{ task.requester_name }}</td>
            <td>
                {{ task.assignee_name }}
                {% if task.stale %}<span class="tag stale-tag">assignee offline</span>{% endif %}
            </td>
            <td>{{ task.created_at.strftime('%Y-%m-%d %H:%M') }}</td>
        </tr>
        {% endfor %}
    </tbody>
</table>
{% else %}
<p>No tasks.</p>
{% endif %}
```

- [ ] **Step 5: Add routes**

Add to `create_dashboard_router()` in `routes.py`:

```python
@router.get("/tasks", response_class=HTMLResponse)
async def tasks_page(request: Request):
    return templates.TemplateResponse("tasks.html", {"request": request, "active": "tasks"})

@router.get("/tasks/list", response_class=HTMLResponse)
async def tasks_list(request: Request):
    all_tasks = tasks_hub.list()
    # Enrich with agent names and stale detection
    enriched = []
    for t in all_tasks:
        requester = agents_hub.get(t.requester)
        assignee = agents_hub.get(t.assignee)
        t.requester_name = requester.name if requester else t.requester
        t.assignee_name = assignee.name if assignee else t.assignee
        # Stale: running but assignee offline
        from bifrost.store.models import AgentStatus, TaskStatus
        t.stale = (
            t.status in (TaskStatus.RUNNING, TaskStatus.INPUT_REQUIRED)
            and assignee is not None
            and assignee.status == AgentStatus.OFFLINE
        )
        enriched.append(t)
    return templates.TemplateResponse(
        "partials/task_list.html", {"request": request, "tasks": enriched}
    )
```

- [ ] **Step 6: Add stale styling to CSS**

Append to `style.css`:
```css
.stale { background: #fff3cd; }
.stale-tag { background: #f8d7da; color: #721c24; }
.status-badge.running { background: #cce5ff; color: #004085; }
.status-badge.queued { background: #e2e3e5; color: #383d41; }
.status-badge.completed { background: #d4edda; color: #155724; }
.status-badge.failed { background: #f8d7da; color: #721c24; }
.status-badge.canceled { background: #e2e3e5; color: #383d41; }
.status-badge.rejected { background: #f8d7da; color: #721c24; }
.status-badge.input-required { background: #fff3cd; color: #856404; }
```

- [ ] **Step 7: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_dashboard.py -v`
Expected: All pass.

- [ ] **Step 8: Commit**

```bash
git add src/bifrost/dashboard/ tests/test_dashboard.py
git commit -m "feat: dashboard task board with stale task highlighting"
```

---

### Task 3: Conversations & Activity Pages

**Files:**
- Create: `src/bifrost/dashboard/templates/conversations.html`
- Create: `src/bifrost/dashboard/templates/partials/conversation_list.html`
- Create: `src/bifrost/dashboard/templates/partials/conversation_detail.html`
- Create: `src/bifrost/dashboard/templates/activity.html`
- Create: `src/bifrost/dashboard/templates/partials/activity_feed.html`
- Modify: `src/bifrost/dashboard/routes.py`
- Modify: `tests/test_dashboard.py`

- [ ] **Step 1: Add tests**

```python
# Add to tests/test_dashboard.py

async def test_conversations_page(client: AsyncClient):
    response = await client.get("/conversations")
    assert response.status_code == 200
    assert "Conversations" in response.text


async def test_activity_page(client: AsyncClient):
    response = await client.get("/activity")
    assert response.status_code == 200
    assert "Activity" in response.text
```

- [ ] **Step 2: Create conversations page template**

```html
<!-- src/bifrost/dashboard/templates/conversations.html -->
{% extends "base.html" %}
{% block title %}Conversations — Bifrost{% endblock %}
{% block content %}
<h1>Conversations</h1>
<div id="conversation-list" hx-get="/conversations/list" hx-trigger="load, every 10s" hx-swap="innerHTML">
    Loading...
</div>
{% endblock %}
```

- [ ] **Step 3: Create conversation list partial**

```html
<!-- src/bifrost/dashboard/templates/partials/conversation_list.html -->
{% if conversations %}
<table>
    <thead>
        <tr>
            <th>ID</th>
            <th>Channel</th>
            <th>Title</th>
            <th>Participants</th>
            <th>Created</th>
        </tr>
    </thead>
    <tbody>
        {% for conv in conversations %}
        <tr hx-get="/conversations/{{ conv.id }}/events" hx-target="#detail-panel" hx-swap="innerHTML" style="cursor: pointer">
            <td>{{ conv.id }}</td>
            <td>{% if conv.channel %}<span class="tag">{{ conv.channel }}</span>{% endif %}</td>
            <td>{{ conv.title or '—' }}</td>
            <td>{{ conv.participants | join(', ') }}</td>
            <td>{{ conv.created_at.strftime('%Y-%m-%d %H:%M') }}</td>
        </tr>
        {% endfor %}
    </tbody>
</table>
<div id="detail-panel"></div>
{% else %}
<p>No conversations.</p>
{% endif %}
```

- [ ] **Step 4: Create conversation detail partial**

```html
<!-- src/bifrost/dashboard/templates/partials/conversation_detail.html -->
<h2>{{ conversation.title or conversation.id }}</h2>
{% if events %}
<div class="event-list">
    {% for event in events %}
    <div class="event">
        <span class="event-from">{{ event.from_agent }}</span>
        <span class="event-time">{{ event.timestamp.strftime('%H:%M:%S') }}</span>
        <div class="event-body">
            {% for part in event.data.get('parts', []) %}
                {% if part.type == 'text' %}{{ part.text }}{% endif %}
            {% endfor %}
        </div>
    </div>
    {% endfor %}
</div>
{% else %}
<p>No events.</p>
{% endif %}
```

- [ ] **Step 5: Create activity page and feed**

```html
<!-- src/bifrost/dashboard/templates/activity.html -->
{% extends "base.html" %}
{% block title %}Activity — Bifrost{% endblock %}
{% block content %}
<h1>Activity</h1>
<div id="activity-feed" hx-get="/activity/feed" hx-trigger="load, every 5s" hx-swap="innerHTML">
    Loading...
</div>
{% endblock %}
```

```html
<!-- src/bifrost/dashboard/templates/partials/activity_feed.html -->
{% if events %}
<div class="event-list">
    {% for event in events %}
    <div class="event">
        <span class="event-time">{{ event.timestamp.strftime('%H:%M:%S') }}</span>
        <span class="event-from">{{ event.from_agent }}</span>
        <span class="tag">{{ event.conversation_id }}</span>
        <div class="event-body">
            {% for part in event.data.get('parts', []) %}
                {% if part.type == 'text' %}{{ part.text[:200] }}{% endif %}
            {% endfor %}
        </div>
    </div>
    {% endfor %}
</div>
{% else %}
<p>No recent activity.</p>
{% endif %}
```

- [ ] **Step 6: Add routes**

Add to `create_dashboard_router()`:

```python
@router.get("/conversations", response_class=HTMLResponse)
async def conversations_page(request: Request):
    return templates.TemplateResponse("conversations.html", {"request": request, "active": "conversations"})

@router.get("/conversations/list", response_class=HTMLResponse)
async def conversations_list(request: Request):
    convos = conversations_hub.list()
    return templates.TemplateResponse(
        "partials/conversation_list.html", {"request": request, "conversations": convos}
    )

@router.get("/conversations/{conv_id}/events", response_class=HTMLResponse)
async def conversation_events(request: Request, conv_id: str):
    conv = store.get_conversation(conv_id)
    events = store.list_events(conv_id) if conv else []
    return templates.TemplateResponse(
        "partials/conversation_detail.html",
        {"request": request, "conversation": conv, "events": events},
    )

@router.get("/activity", response_class=HTMLResponse)
async def activity_page(request: Request):
    return templates.TemplateResponse("activity.html", {"request": request, "active": "activity"})

@router.get("/activity/feed", response_class=HTMLResponse)
async def activity_feed(request: Request):
    # Get recent events across all conversations (last 50)
    all_convos = conversations_hub.list()
    all_events = []
    for c in all_convos[:20]:  # limit to 20 most recent conversations
        events = store.list_events(c.id)
        all_events.extend(events)
    all_events.sort(key=lambda e: e.timestamp, reverse=True)
    all_events = all_events[:50]  # last 50 events
    return templates.TemplateResponse(
        "partials/activity_feed.html", {"request": request, "events": all_events}
    )
```

- [ ] **Step 7: Add event styling to CSS**

Append to `style.css`:
```css
.event-list { display: flex; flex-direction: column; gap: 0.5rem; }
.event { padding: 0.5rem; border-left: 3px solid #dee2e6; }
.event-from { font-weight: 600; margin-right: 0.5rem; }
.event-time { color: #6c757d; font-size: 0.85em; }
.event-body { margin-top: 0.25rem; }
```

- [ ] **Step 8: Run tests**

Run: `cd /home/marc/dev/bifrost && pytest tests/test_dashboard.py -v`
Expected: All pass.

- [ ] **Step 9: Commit**

```bash
git add src/bifrost/dashboard/ tests/test_dashboard.py
git commit -m "feat: dashboard conversations and activity feed pages"
```

---

### Task 4: Dockerfile

**Files:**
- Create: `Dockerfile`

- [ ] **Step 1: Create Dockerfile**

```dockerfile
# Dockerfile
FROM python:3.12-slim

WORKDIR /app

COPY pyproject.toml .
COPY src/ src/

RUN pip install --no-cache-dir .

EXPOSE 8000

ENTRYPOINT ["python", "-m", "bifrost"]
CMD ["--host", "0.0.0.0", "--port", "8000"]
```

- [ ] **Step 2: Build and test**

Run: `cd /home/marc/dev/bifrost && docker build -t bifrost .`
Expected: Builds successfully.

Run: `docker run --rm -p 8000:8000 bifrost --insecure`
Expected: Server starts, accessible at http://localhost:8000/agents.

- [ ] **Step 3: Commit**

```bash
git add Dockerfile
git commit -m "chore: Dockerfile for bifrost v2"
```
