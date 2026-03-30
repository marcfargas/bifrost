# Architecture Research

**Domain:** AI agent communication hub — Jinja2+htmx dashboard with SSE push on FastAPI/FastMCP
**Researched:** 2026-03-30
**Confidence:** HIGH (patterns verified against FastAPI official docs + community implementations)

## Standard Architecture

### System Overview

The milestone adds a dashboard layer alongside the existing MCP layer. Both layers share the same FastMCP/Starlette application, the same SQLite store, and the same OAuth. The new components are a router, templates, static files, an SSE broadcaster, and a human operator agent.

```
┌─────────────────────────────────────────────────────────────────┐
│                        FastMCP / Starlette App                  │
│  ┌──────────────────────────┐  ┌───────────────────────────┐    │
│  │   MCP Layer (existing)   │  │   Dashboard Layer (new)   │    │
│  │   /mcp  (Streamable HTTP)│  │   /  /agents /tasks …     │    │
│  │   11 tools, OAuth        │  │   /events  (SSE stream)   │    │
│  └──────────────┬───────────┘  └──────────────┬────────────┘    │
│                 │                             │                  │
│  ┌──────────────▼─────────────────────────────▼────────────┐    │
│  │                   Hub Layer (shared)                     │    │
│  │  AgentHub  TaskHub  ConversationHub  DeliveryHub         │    │
│  └──────────────────────────────┬────────────────────────── ┘   │
│                                 │                                │
│  ┌──────────────────────────────▼────────────────────────────┐  │
│  │                    Store / SQLite                          │  │
│  └────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘

New components in Dashboard Layer:
  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌────────────────┐
  │ APIRouter    │  │ Jinja2       │  │ Static files │  │ SSE Broadcaster│
  │ (routes.py)  │  │ Templates    │  │ /static/     │  │ (broadcaster.py│
  └──────────────┘  └──────────────┘  └──────────────┘  └────────────────┘

Human operator agent (special row in agents table, is_human=True flag):
  ┌──────────────────────────────────────────────────────────────┐
  │ HumanOperator: a regular Agent with is_human=True            │
  │ Created once on first dashboard login, reused across sessions│
  │ Can send messages and create tasks via dashboard POST forms   │
  └──────────────────────────────────────────────────────────────┘
```

### Component Responsibilities

| Component | Responsibility | Location |
|-----------|----------------|----------|
| `dashboard/routes.py` | All HTTP GET/POST handlers for dashboard pages and partials | `src/bifrost/dashboard/routes.py` |
| `dashboard/templates/` | Jinja2 base + page + partials hierarchy | `src/bifrost/dashboard/templates/` |
| `dashboard/static/` | CSS (no JS frameworks, htmx via CDN) | `src/bifrost/dashboard/static/` |
| `dashboard/broadcaster.py` | In-process SSE event fan-out — asyncio queue per connected client | `src/bifrost/dashboard/broadcaster.py` |
| Human operator agent | A flagged Agent record (`is_human=True`) that represents the dashboard user in the agent ecosystem | Added to `store/models.py` + `hub/agents.py` |
| `app.py` wiring | `include_router()` + `mount()` for static files, broadcaster injection | `src/bifrost/app.py` |

## Recommended Project Structure

The dashboard package already has a placeholder at `src/bifrost/dashboard/`. Expand it as follows:

```
src/bifrost/dashboard/
├── __init__.py
├── routes.py               # APIRouter factory function
├── broadcaster.py          # SSEBroadcaster: asyncio queue fan-out
├── templates/
│   ├── base.html           # Nav, htmx CDN, SSE extension
│   ├── agents.html         # Agent directory page
│   ├── tasks.html          # Task board page
│   ├── conversations.html  # Conversation list + detail panel
│   ├── activity.html       # Activity feed page (SSE-driven)
│   └── partials/
│       ├── agent_list.html        # htmx swap target
│       ├── task_list.html         # htmx swap target
│       ├── conversation_list.html # htmx swap target
│       ├── conversation_detail.html
│       └── activity_event.html    # SSE-pushed fragment
└── static/
    └── style.css           # Minimal CSS, no build toolchain
```

### Structure Rationale

- **`routes.py` as factory:** `create_dashboard_router(store, broadcaster)` returns an `APIRouter`. Clean separation from `app.py` wiring. Matches the existing `create_app` factory pattern.
- **`broadcaster.py` as separate module:** SSE state (client registry) is independent of request handling. Injected into routes via closure, same as how `Store` is injected into hubs.
- **`templates/partials/`:** htmx partial responses are distinct from full pages. A dedicated directory prevents confusion between full-page responses and fragment responses.
- **Static in-package:** `src/bifrost/dashboard/static/` rather than a top-level `static/` keeps the dashboard self-contained and avoids conflicts with any future static roots.

## Architectural Patterns

### Pattern 1: Page + Partial Route Pairs

**What:** Each view has two routes — one that renders the full page (for direct navigation) and one that renders only the updated fragment (for htmx swaps).

**When to use:** Every dynamic table or list in the dashboard.

**Trade-offs:** Doubles route count; keeps JS-free architecture and enables browser-native navigation.

**Example:**
```python
@router.get("/agents", response_class=HTMLResponse)
async def agents_page(request: Request):
    return templates.TemplateResponse("agents.html", {"request": request, "active": "agents"})

@router.get("/agents/list", response_class=HTMLResponse)
async def agents_list(request: Request):
    agents = agent_hub.list_all()
    return templates.TemplateResponse("partials/agent_list.html", {"request": request, "agents": agents})
```

Template (`agents.html`):
```html
<div id="agent-list"
     hx-get="/agents/list"
     hx-trigger="load, every 10s"
     hx-swap="innerHTML">
    Loading...
</div>
```

### Pattern 2: Per-Client SSE Queue (In-Process Broadcaster)

**What:** An `SSEBroadcaster` singleton maintains a dict of `agent_id -> asyncio.Queue`. When an event occurs (message sent, task updated), the relevant queue receives a payload. SSE endpoints consume their queue with an async generator.

**When to use:** Real-time push to the activity feed and agent message inbox. This milestone uses it for channel push (replacing `bifrost_check` polling).

**Trade-offs:**
- Pro: No extra dependencies (pure asyncio), works within single-process deployment.
- Con: State is in-process; does not survive restarts; does not work with multi-process deployments. Acceptable for this single-container deployment.

**Example:**
```python
# dashboard/broadcaster.py
import asyncio
from collections import defaultdict

class SSEBroadcaster:
    def __init__(self):
        self._queues: dict[str, list[asyncio.Queue]] = defaultdict(list)

    def subscribe(self, agent_id: str) -> asyncio.Queue:
        q: asyncio.Queue = asyncio.Queue()
        self._queues[agent_id].append(q)
        return q

    def unsubscribe(self, agent_id: str, q: asyncio.Queue) -> None:
        self._queues[agent_id].discard(q)

    async def publish(self, agent_id: str, event: dict) -> None:
        for q in list(self._queues[agent_id]):
            await q.put(event)

    async def broadcast(self, event: dict) -> None:
        """Send to all connected dashboard clients."""
        for queues in self._queues.values():
            for q in queues:
                await q.put(event)
```

SSE endpoint:
```python
from fastapi.sse import EventSourceResponse
import json

@router.get("/events")
async def sse_stream(request: Request, agent_id: str):
    q = broadcaster.subscribe(agent_id)
    async def generator():
        try:
            while True:
                if await request.is_disconnected():
                    break
                event = await asyncio.wait_for(q.get(), timeout=15)
                yield {"data": json.dumps(event), "event": event.get("type", "update")}
        finally:
            broadcaster.unsubscribe(agent_id, q)
    return EventSourceResponse(generator())
```

htmx client (in `activity.html`):
```html
<script src="https://unpkg.com/htmx-ext-sse@2.2.2/sse.js"></script>
<div hx-ext="sse" sse-connect="/events?agent_id={{ operator_id }}"
     sse-swap="activity_event" hx-swap="afterbegin">
</div>
```

### Pattern 3: Human Operator Agent

**What:** A special `Agent` record with `is_human=True` flag stored in SQLite. Created once when the dashboard first authenticates, reused on subsequent logins. The dashboard routes act as its MCP tools — POST forms submit messages and task requests on its behalf, using the same hub methods that MCP tool handlers use.

**When to use:** Any interactive action from the dashboard (send message, create task, cancel task).

**Trade-offs:**
- Pro: The human appears in agent listings; agents can address the human by name; no separate identity system needed.
- Con: Requires schema migration to add `is_human` column.

**Example model addition:**
```python
# store/models.py
@dataclass
class Agent:
    # ... existing fields ...
    is_human: bool = False   # True for dashboard human operator
```

Dashboard POST route (mirrors bifrost_send MCP tool):
```python
@router.post("/send", response_class=HTMLResponse)
async def send_message(request: Request, to: str = Form(...), body: str = Form(...)):
    operator = agent_hub.get_or_create_human_operator()
    conversation_hub.send(from_agent_id=operator.id, to=to, body=body)
    return RedirectResponse("/conversations", status_code=303)
```

### Pattern 4: FastMCP Custom Route vs. APIRouter Include

**What:** FastMCP provides `@mcp.custom_route()` for adding one-off Starlette-style handlers. For a full dashboard with many routes, use `FastAPI.include_router()` on the underlying Starlette app object instead — FastMCP exposes the underlying Starlette app for this purpose.

**When to use:** Use `custom_route` only for small additions (health, callback). For the dashboard, wire the `APIRouter` through the underlying Starlette app.

**Trade-offs:** FastMCP does not document `include_router` directly; the underlying Starlette/FastAPI app must be accessed. The existing codebase already uses `custom_route` successfully for `/health` and `/callback` — this pattern is the minimal-change approach for the skeleton and can be refactored once routing needs grow.

**Example (mounting the router):**
```python
# app.py, inside create_app()
from bifrost.dashboard.routes import create_dashboard_router
from bifrost.dashboard.broadcaster import SSEBroadcaster
from fastapi.staticfiles import StaticFiles
from pathlib import Path

broadcaster = SSEBroadcaster()
dashboard_router = create_dashboard_router(store, broadcaster)

# Access underlying Starlette app to include router
starlette_app = mcp.streamable_http_app()  # or mcp._app depending on FastMCP version
starlette_app.include_router(dashboard_router)

# Static files
static_dir = Path(__file__).parent / "dashboard" / "static"
starlette_app.mount("/static", StaticFiles(directory=str(static_dir)), name="static")
```

**Note (LOW confidence):** The exact API for accessing FastMCP's underlying Starlette app object must be verified against the installed FastMCP version at implementation time. Alternatively, all dashboard routes can be added as `@mcp.custom_route()` calls — this is verbose but avoids the internal API question entirely.

## Data Flow

### Page Request Flow (htmx partial refresh)

```
Browser (10s poll)
    │ GET /agents/list  (hx-get)
    ▼
FastMCP/Starlette router
    │ matched to dashboard APIRouter
    ▼
routes.py: agents_list()
    │ calls AgentHub.list_all()
    ▼
AgentHub → Store → SQLite
    │ returns list[Agent]
    ▼
Jinja2: render partials/agent_list.html
    │ returns HTML fragment
    ▼
htmx: swaps #agent-list innerHTML
```

### SSE Push Flow (real-time activity feed)

```
MCP agent calls bifrost_send()
    │
    ▼
ConversationHub.send() → Store → SQLite (event appended)
    │ (new: also calls broadcaster.publish())
    ▼
SSEBroadcaster.publish(agent_id, event_payload)
    │ puts payload into per-client asyncio.Queue
    ▼
SSE generator in /events yields payload
    │ chunked HTTP response to browser
    ▼
htmx SSE extension receives event
    │ swaps #activity-feed with new event fragment
    ▼
Browser DOM updated without page reload
```

### Human Operator Action Flow (POST form)

```
Dashboard operator submits "Send Message" form
    │ POST /send  (body, to=agent_name)
    ▼
routes.py: send_message()
    │ resolves human operator agent_id
    │ calls ConversationHub.send() (same as MCP tool)
    │ broadcaster.publish() notifies SSE clients
    ▼
303 redirect to /conversations
    │
    ▼
Browser follows redirect, htmx reloads partials
```

### SSE Channel Push Flow (replaces bifrost_check for agents)

```
MCP agent has SSE channel open (future: /agent-events endpoint)
    │ OR: existing bifrost_check polling (current)
    ▼
When ConversationHub.send() fires:
    broadcaster.publish(target_agent_id, delivery_event)
    │
    ▼
Target agent's asyncio.Queue receives event
    │
    ▼
SSE stream delivers event without polling
```

### Key Data Flows Summary

1. **Read-only dashboard pages:** Hub → Store → SQLite → Jinja2 partial → htmx DOM swap (10s poll)
2. **SSE activity feed:** Event write path hooks broadcaster → asyncio queue → SSE generator → htmx DOM swap (push)
3. **Interactive forms:** POST route → Hub (same as MCP tools) → broadcaster → redirect (PRG pattern)
4. **Agent SSE delivery:** Write path hooks broadcaster → per-agent queue → SSE stream (replaces polling)

## Scaling Considerations

| Scale | Architecture Adjustments |
|-------|--------------------------|
| 1-10 dashboard users | In-process SSEBroadcaster is fine. No external dependencies needed. |
| 10-100 dashboard users | In-process broadcaster still viable. Add pagination to list routes (currently return all). |
| 100+ dashboard users | SSEBroadcaster becomes memory pressure. Switch to Redis pub/sub. Out of scope for this MVP. |

### Scaling Priorities

1. **First bottleneck:** SQLite write lock contention — activity feed queries all conversations repeatedly. Mitigation: add a global events query (last N events across all conversations) to `Store` rather than querying per-conversation.
2. **Second bottleneck:** SSE connections holding asyncio tasks open per client. Mitigation: add connection timeout and queue max size. Still single-process concern.

## Anti-Patterns

### Anti-Pattern 1: Full-Page Templates for htmx Targets

**What people do:** Return a full `base.html` page from the partial endpoint (the one htmx polls).
**Why it's wrong:** htmx swaps the response into a div — returning a full `<html>` page inside a div breaks the DOM.
**Do this instead:** Partial endpoints render only the fragment template (`partials/agent_list.html`). Full-page endpoints render the page template (`agents.html`) which contains the div with `hx-get`.

### Anti-Pattern 2: Blocking SQLite Calls Inside SSE Generator

**What people do:** Call `store.list_events()` synchronously inside the `async def generator()` that drives SSE.
**Why it's wrong:** Synchronous SQLite calls inside an async generator block the event loop, stalling all SSE streams.
**Do this instead:** Use the asyncio queue pattern — the SSE generator only waits on `asyncio.Queue.get()`. All SQLite reads happen in request handlers (outside the generator) or are pushed through the queue from the write path.

### Anti-Pattern 3: Wiring Dashboard Routes as `@mcp.custom_route()` at Scale

**What people do:** Add every dashboard route as a `@mcp.custom_route()` call in `app.py`.
**Why it's wrong:** `app.py` becomes unmaintainably large; all routes are inline closures with no separation.
**Do this instead:** Use an `APIRouter` in `dashboard/routes.py`, include it at mount time in `app.py`. Keep `app.py` as a wiring layer only.

### Anti-Pattern 4: Creating a New Human Operator Agent on Every Login

**What people do:** Create a new `Agent` with `is_human=True` on every dashboard session.
**Why it's wrong:** The agent registry fills with duplicate human operators; other agents see spurious "new" agent join events.
**Do this instead:** `AgentHub.get_or_create_human_operator()` — upsert by a fixed name (e.g., `"Human Operator"`) so the record is created once and reused. The same `register()` upsert pattern already exists in the codebase.

### Anti-Pattern 5: SSE Without Connection Cleanup

**What people do:** `broadcaster.subscribe(agent_id)` on connect, never call `unsubscribe` on disconnect.
**Why it's wrong:** Queues accumulate indefinitely; memory grows with each browser tab reload.
**Do this instead:** Use `try/finally` in the SSE generator to call `broadcaster.unsubscribe()` when the generator exits (client disconnects or server shuts down).

## Integration Points

### Internal Boundaries

| Boundary | Communication | Notes |
|----------|---------------|-------|
| `dashboard/routes.py` ↔ Hub classes | Direct method calls (same process) | No abstraction needed — same pattern as `mcp/tools.py` |
| `dashboard/routes.py` ↔ `SSEBroadcaster` | Direct method calls, injected via closure | Broadcaster is a singleton created in `create_app()` |
| Write path (hubs) ↔ `SSEBroadcaster` | `broadcaster.publish()` called from `ConversationHub.send()` and `TaskHub.update_status()` | Requires hubs to accept broadcaster as optional dependency |
| `app.py` ↔ `dashboard/` | `create_dashboard_router(store, broadcaster)` + `include_router()` + static mount | Single wiring point |
| Dashboard auth ↔ existing OAuth | Starlette `SessionMiddleware` + cookie carrying the OIDC token; dashboard routes check session | Reuses the existing `DexOAuthProvider`; no new auth flow needed |

### External Services

| Service | Integration Pattern | Notes |
|---------|---------------------|-------|
| htmx (CDN) | `<script src="https://unpkg.com/htmx.org@2.0.4">` | Pin version in base.html; no npm required |
| htmx SSE extension (CDN) | `<script src="https://unpkg.com/htmx-ext-sse@2.2.2/sse.js">` | Required for `hx-ext="sse"` attribute |
| OIDC provider (Dex) | Unchanged — existing `DexOAuthProvider` proxies tokens | Dashboard session uses same tokens as MCP sessions |

## Build Order (Phase Dependencies)

The components have these dependencies — build in this order:

1. **Schema migration** (`is_human` flag on Agent, `store/models.py` + `store/db.py`) — everything else depends on the model being correct.
2. **Dashboard skeleton** — `routes.py` factory, `base.html`, static files, wiring in `app.py`. No interactivity yet, just serves pages.
3. **Read-only pages** — agents, tasks, conversations, activity via htmx polling partials. Uses existing hubs directly.
4. **Human operator agent** — `AgentHub.get_or_create_human_operator()`, POST action routes, PRG redirect pattern.
5. **SSE broadcaster** — `SSEBroadcaster` class, `/events` endpoint, broadcaster injection into hub write paths.
6. **SSE activity feed** — wire broadcaster output to `activity_event` htmx SSE swap in `activity.html`.
7. **Agent SSE delivery** — broadcaster-based push for MCP agents (replaces `bifrost_check` polling).
8. **Dashboard OAuth** — `SessionMiddleware` + login redirect for production mode (can be skipped in `--insecure` mode).

Steps 1-4 are independent of SSE. The SSE components (5-7) can only be built after the broadcaster module exists and after the hub write paths are hooked. OAuth (8) is the last step as it adds a gate in front of all previous work.

## Sources

- [FastAPI Templates (official docs)](https://fastapi.tiangolo.com/advanced/templates/) — MEDIUM confidence (official)
- [FastAPI Server-Sent Events (official docs)](https://fastapi.tiangolo.com/tutorial/server-sent-events/) — HIGH confidence (official)
- [sse-starlette on PyPI](https://pypi.org/project/sse-starlette/) — MEDIUM confidence (verified library, not needed if using FastAPI native SSE)
- [Building Real-Time Dashboards with FastAPI and HTMX](https://medium.com/codex/building-real-time-dashboards-with-fastapi-and-htmx-01ea458673cb) — MEDIUM confidence (community article, patterns verified against official docs)
- [FastAPI HTMX Patterns 2025](https://johal.in/htmx-fastapi-patterns-hypermedia-driven-single-page-applications-2025/) — LOW confidence (blog, WebSearch only)
- [fastapi-sse-htmx example](https://github.com/vlcinsky/fastapi-sse-htmx) — MEDIUM confidence (working implementation reference)
- [FastAPI Static Files (official)](https://fastapi.tiangolo.com/tutorial/static-files/) — HIGH confidence (official)
- Existing Bifrost codebase: `src/bifrost/app.py`, `src/bifrost/hub/`, `src/bifrost/store/` — HIGH confidence (ground truth)
- Existing dashboard plan: `docs/superpowers/plans/2026-03-30-bifrost-v2-dashboard.md` — HIGH confidence (prior art in same codebase)

---
*Architecture research for: Bifrost dashboard + SSE milestone*
*Researched: 2026-03-30*
