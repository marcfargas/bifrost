# Phase 1: Foundation + Read-Only Dashboard - Research

**Researched:** 2026-03-31
**Domain:** Jinja2 + htmx dashboard on FastMCP/Starlette, SQLite data layer, human operator model
**Confidence:** HIGH

---

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- **D-01:** Multi-page layout with sidebar navigation. Each view (agents, tasks, conversations, activity) is its own page.
- **D-02:** Pico CSS via CDN for styling. Classless/minimal-class framework, semantic HTML approach.
- **D-03:** Auto dark/light mode following system preference (Pico's default behavior, no explicit `data-theme` needed).
- **D-04:** Card grid layout for agents. One card per agent showing name, status, last seen, and skills.
- **D-05:** List with colored status badges for tasks. Filter buttons across the top by status. Swap list via htmx on filter click.
- **D-06:** Two-panel layout for conversations. Conversation list left, message history pane right. Click conversation to load messages via htmx.
- **D-07:** Chronological event stream for activity feed. Timestamped list, simple log-style.
- **D-08:** Human operator agent created lazily on first dashboard visit, not on server startup.
- **D-09:** Human operator displayed in a separate section above the agent card grid. Clear visual separation.

### Claude's Discretion
- htmx polling intervals for auto-refresh (reasonable defaults)
- Template file organization within `src/bifrost/dashboard/templates/`
- Route mounting strategy (verify `include_router` or fall back to `@mcp.custom_route()`)
- Exact Pico CSS version and SRI hash

### Deferred Ideas (OUT OF SCOPE)
None — discussion stayed within phase scope.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| DASH-01 | User can view agent directory with cards, status, last seen, and skills | AgentHub.list_all(), Store.list_agents(), card data from Store.get_card() |
| DASH-02 | User can view task board with status, assignee, requester, and filtering | Store.list_tasks(status=...) supports filter by status param; agent name lookup via Store.get_agent() |
| DASH-03 | User can view conversation message history | Store.list_events(conv_id) returns ordered Event list; Store.get_conversation() for metadata |
| DASH-04 | User can view activity feed of recent events across all conversations | Store.list_conversations() + Store.list_events() per conv; app-level sort/truncation needed |
| DASH-05 | Dashboard pages update via htmx partials without full page reload | htmx `hx-get` + `hx-trigger="every Ns"` on container divs; separate partial routes return HTML fragments |
| HUMAN-01 | A virtual "human" agent exists with `is_human` flag on Agent model | Requires new `is_human` column in `agents` table + `Agent.is_human` field + schema migration |
| HUMAN-02 | Human operator is distinguishable from regular agents in UI and API responses | `is_human` flag drives separate section in agents page template; `list_agents` store query can filter/annotate |
</phase_requirements>

---

## Summary

This phase builds a read-only observability dashboard using the existing FastMCP/Starlette server as the host, Jinja2 templates served from the `src/bifrost/dashboard/` package, and htmx for partial page refreshes. The stack is entirely in-process — no separate service, no build toolchain.

The key technical constraint to understand before planning: **FastMCP uses Starlette, not FastAPI**. The `streamable_http_app()` method returns a `Starlette` app, and `_custom_starlette_routes` stores `Route` objects (Starlette routing primitives). There is no `include_router()` method — that is a FastAPI-only concept. Routes must be registered individually via `@mcp.custom_route()` or by directly appending to `mcp._custom_starlette_routes`. For static files, `starlette.staticfiles.StaticFiles` mounted as a `Mount` object works, but the mounting must happen through the internal list.

The second important finding: Starlette 1.0.0 is installed and its `Jinja2Templates.TemplateResponse()` signature is `(request, name, context)` — **not** the old `(name, {"request": request})` pattern seen in the earlier design doc. Any template code using the old signature will fail silently or raise a TypeError.

**Primary recommendation:** Register all dashboard routes via `@mcp.custom_route()` for each route (simpler, works with type annotations), and serve static files via CDN (Pico + htmx) to avoid the StaticFiles mounting complexity in Phase 1.

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Jinja2 | >=3.1.0 (already installed) | Server-side HTML templating | Already a project dependency; autoescape built-in |
| htmx | 2.0.8 (CDN) | Partial page updates without JS framework | Zero build toolchain; attribute-driven; fits server-rendered pattern |
| Pico CSS | 2.1.1 (CDN) | Semantic classless styling with dark/light auto-mode | Classless variant needs zero CSS class additions on HTML |
| starlette.templating.Jinja2Templates | (bundled with starlette 1.0.0) | Template rendering integration | Handles request context injection, autoescape |
| starlette.staticfiles.StaticFiles | (bundled with starlette 1.0.0) | Static file serving | Available if custom CSS needed |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| starlette.routing.Route | (starlette 1.0.0) | Register custom HTTP handlers | Each dashboard route registered via `@mcp.custom_route()` |
| starlette.routing.Mount | (starlette 1.0.0) | Mount ASGI sub-apps (e.g. StaticFiles) | If serving local CSS/JS files |
| starlette.responses.HTMLResponse | (starlette 1.0.0) | Return pre-rendered HTML | For htmx partial responses |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Pico CSS CDN | Local static CSS | CDN simpler for Phase 1 — avoids StaticFiles mount complexity |
| Per-route `@mcp.custom_route()` | FastAPI `include_router()` | `include_router` is not available — FastMCP wraps Starlette, not FastAPI |
| Separate partial routes | Full-page reload with query params | htmx partials are the specified approach (DASH-05) |

**CDN tags for base template:**
```html
<!-- htmx 2.0.8 with SRI -->
<script src="https://cdn.jsdelivr.net/npm/htmx.org@2.0.8/dist/htmx.min.js"
        integrity="sha384-/TgkGk7p307TH7EXJDuUlgG3Ce1UVolAOFopFekQkkXihi5u/6OCvVKyz1W+idaz"
        crossorigin="anonymous"></script>

<!-- Pico CSS 2.1.1 classless variant (auto dark/light, no class needed) -->
<link rel="stylesheet"
      href="https://cdn.jsdelivr.net/npm/@picocss/pico@2.1.1/css/pico.classless.min.css">
```

Note: SRI hash must be verified at implementation time against jsDelivr for the exact file version. The htmx hash above is from jsDelivr for 2.0.8 (MEDIUM confidence — from WebSearch).

## Architecture Patterns

### Recommended Project Structure
```
src/bifrost/
└── dashboard/
    ├── __init__.py          # Already exists (empty)
    ├── routes.py            # Route definitions, Jinja2Templates instance
    └── templates/
        ├── base.html        # Sidebar nav, CDN includes
        ├── agents.html      # Agent directory page (card grid)
        ├── tasks.html       # Task board page
        ├── conversations.html  # Two-panel conversation viewer
        ├── activity.html    # Activity feed page
        └── partials/
            ├── agent_list.html       # htmx target: agent card grid
            ├── task_list.html        # htmx target: filtered task list
            ├── conversation_list.html  # htmx target: left panel list
            ├── conversation_detail.html  # htmx target: right panel messages
            └── activity_feed.html    # htmx target: event stream

tests/
└── test_dashboard.py        # New test file
```

### Pattern 1: Route Registration via `@mcp.custom_route()`

**What:** Each dashboard route is registered on the `mcp` instance using `@mcp.custom_route(path, methods)`. This appends a Starlette `Route` to `mcp._custom_starlette_routes` which are added to the Starlette app at construction time.

**When to use:** All dashboard HTTP routes in Phase 1.

**How routes are wired:** Call a `setup_dashboard_routes(mcp, store)` function inside `create_app()` after the `mcp` instance is created. This function instantiates `Jinja2Templates`, creates hub instances, and decorates each handler with `@mcp.custom_route()`.

```python
# src/bifrost/dashboard/routes.py
"""Dashboard routes — Jinja2 + htmx."""
from __future__ import annotations

from pathlib import Path

from starlette.requests import Request
from starlette.responses import HTMLResponse, RedirectResponse

from mcp.server.fastmcp import FastMCP
from bifrost.store.db import Store

TEMPLATE_DIR = Path(__file__).parent / "templates"


def setup_dashboard_routes(mcp: FastMCP, store: Store) -> None:
    """Register all dashboard routes on the FastMCP instance."""
    from starlette.templating import Jinja2Templates
    from bifrost.hub.agents import AgentHub
    from bifrost.hub.tasks import TaskHub
    from bifrost.hub.conversations import ConversationHub

    templates = Jinja2Templates(directory=TEMPLATE_DIR)
    agents = AgentHub(store)
    tasks = TaskHub(store)
    conversations = ConversationHub(store)

    @mcp.custom_route("/", methods=["GET"])
    async def index(request: Request) -> RedirectResponse:
        return RedirectResponse(url="/agents", status_code=307)

    @mcp.custom_route("/agents", methods=["GET"])
    async def agents_page(request: Request) -> HTMLResponse:
        return templates.TemplateResponse(request, "agents.html", {"active": "agents"})

    @mcp.custom_route("/agents/list", methods=["GET"])
    async def agents_list(request: Request) -> HTMLResponse:
        all_agents = agents.list_all()
        # Separate human operator from regular agents
        human = next((a for a in all_agents if getattr(a, "is_human", False)), None)
        regular = [a for a in all_agents if not getattr(a, "is_human", False)]
        return templates.TemplateResponse(
            request, "partials/agent_list.html",
            {"agents": regular, "human": human}
        )
    # ... remaining routes follow same pattern
```

**CRITICAL:** The Starlette 1.0.0 `Jinja2Templates.TemplateResponse()` takes `(request, name, context)` — NOT the old `(name, {"request": request, ...})` pattern. Always use the new signature.

### Pattern 2: htmx Polling for Auto-Refresh (DASH-05)

**What:** Container `div` in the page shell has `hx-get`, `hx-trigger`, and `hx-swap` attributes. htmx polls the partial route and replaces the container content.

**Recommended polling intervals:**
- Agents list: `every 10s` (low churn)
- Task list: `every 10s`
- Activity feed: `every 5s` (higher activity expectation)
- Conversation detail: `every 5s` (user is actively reading)

```html
<!-- Page shell — triggers load + poll -->
<div id="agent-list"
     hx-get="/agents/list"
     hx-trigger="load, every 10s"
     hx-swap="innerHTML">
    Loading...
</div>
```

**Filter buttons for task board (D-05):**
```html
<!-- Filter bar — swaps list on click -->
<div role="group">
    <button hx-get="/tasks/list" hx-target="#task-list" hx-swap="innerHTML">All</button>
    <button hx-get="/tasks/list?status=queued" hx-target="#task-list" hx-swap="innerHTML">Queued</button>
    <button hx-get="/tasks/list?status=running" hx-target="#task-list" hx-swap="innerHTML">Running</button>
    <!-- etc. -->
</div>
<div id="task-list" hx-get="/tasks/list" hx-trigger="load" hx-swap="innerHTML">Loading...</div>
```

### Pattern 3: Human Operator Agent (HUMAN-01, HUMAN-02)

**What:** The `Agent` model gains a boolean `is_human` field. A corresponding `is_human` column is added to the `agents` SQLite table. The dashboard creates the human operator lazily on first visit.

**Schema change required:** Add `is_human INTEGER NOT NULL DEFAULT 0` to the `agents` table DDL in `db.py`. The store's `upsert_agent()`, `_row_to_agent()`, and `list_agents()` must handle this column. Since the schema uses `CREATE TABLE IF NOT EXISTS`, an ALTER TABLE statement is needed for existing databases.

**Two options for migration:**
1. Add `ALTER TABLE agents ADD COLUMN is_human INTEGER NOT NULL DEFAULT 0;` guarded by a try/except (preferred — simple, idempotent for existing DBs)
2. Add it to `_SCHEMA` only (works for fresh databases, breaks existing ones)

Recommended: Add the ALTER TABLE in `Store.__init__()` after `executescript(_SCHEMA)`.

**Lazy creation pattern:**
```python
HUMAN_OPERATOR_NAME = "Human Operator"

def _ensure_human_operator(store: Store) -> Agent:
    """Get or create the human operator agent. Called on first dashboard visit."""
    agent = store.get_agent_by_name(HUMAN_OPERATOR_NAME)
    if agent is None:
        from bifrost.store.models import AgentStatus
        agent = Agent(name=HUMAN_OPERATOR_NAME, status=AgentStatus.ONLINE, is_human=True)
        store.upsert_agent(agent)
    return agent
```

### Pattern 4: Sidebar Navigation with Pico CSS

**What:** Pico CSS classless variant applies styles to semantic HTML elements — `<nav>`, `<main>`, `<article>`, `<header>`. No CSS classes needed on most elements.

**Layout approach with sidebar:** Pico's classless variant does not provide a sidebar layout out-of-the-box. Use a minimal CSS override or use `<aside>` + `<main>` with a CSS grid wrapper. Since Pico does not provide grid utilities in classless mode, a small inline style or 10-line `<style>` block in base.html is needed.

```html
<!-- base.html layout wrapper -->
<body>
  <div style="display:grid; grid-template-columns:200px 1fr; min-height:100vh;">
    <nav>
      <ul>
        <li><a href="/agents" {% if active=='agents' %}aria-current="page"{% endif %}>Agents</a></li>
        <li><a href="/tasks" {% if active=='tasks' %}aria-current="page"{% endif %}>Tasks</a></li>
        <li><a href="/conversations" {% if active=='conversations' %}aria-current="page"{% endif %}>Conversations</a></li>
        <li><a href="/activity" {% if active=='activity' %}aria-current="page"{% endif %}>Activity</a></li>
      </ul>
    </nav>
    <main class="container">
      {% block content %}{% endblock %}
    </main>
  </div>
</body>
```

Pico uses `aria-current="page"` for active link styling.

### Anti-Patterns to Avoid
- **`include_router()` in `create_app()`:** FastMCP is built on Starlette, not FastAPI. There is no `include_router` method. Using it will raise AttributeError at startup.
- **Old Jinja2Templates signature `(name, {"request": request})`:** Starlette 1.0.0 requires `(request, name, context)`. The old pattern raises a TypeError.
- **Creating hub instances in `routes.py` at module load:** Hub constructors require `Store`, which is created in `create_app()`. Hub instantiation belongs inside `setup_dashboard_routes()` where `store` is available.
- **Using `str` timestamps directly in Jinja2 with `.strftime()`:** `Agent.last_seen` and similar fields are ISO strings, not `datetime` objects. Parse them before passing to templates, or use a Jinja2 filter.
- **Assuming `agent.card` is always populated:** Many agents will have no card set (never called `bifrost_introduce` with skills). Templates must guard `{% if agent.card %}`.
- **`mcp.app.include_router()` via FastAPI internals:** FastMCP does not expose a FastAPI app — `streamable_http_app()` returns Starlette.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| HTML templating | String concatenation / f-strings | Jinja2 (already installed) | XSS protection via autoescape, inheritance, filters |
| Partial page update | Manual JS fetch + innerHTML | htmx `hx-get`/`hx-swap` | Zero JS authoring for standard patterns |
| Dark/light mode | JavaScript theme switcher | Pico CSS auto-mode via `prefers-color-scheme` | Built into Pico, zero code |
| Static file serving | Custom file read route | `starlette.staticfiles.StaticFiles` mount | Handles ETags, content-type, 304 caching |
| Timestamp parsing in templates | Python strftime call in route | Jinja2 custom filter | Keeps routes clean; reusable across templates |

**Key insight:** The entire UI layer is achievable without writing a single line of JavaScript — htmx handles all interactivity through HTML attributes.

## Common Pitfalls

### Pitfall 1: Starlette `TemplateResponse` Signature Changed
**What goes wrong:** `templates.TemplateResponse("agents.html", {"request": request})` raises `TypeError` or renders the template without request context.
**Why it happens:** Starlette 1.0.0 (installed) changed the signature to `(request, name, context)`. The old `(name, dict)` pattern was removed.
**How to avoid:** Always use `templates.TemplateResponse(request, "agents.html", {"key": val})`. Request is the first positional argument.
**Warning signs:** `TypeError: TemplateResponse() got unexpected keyword argument` or missing `request` in template context.

### Pitfall 2: `is_human` Column Missing on Existing Databases
**What goes wrong:** `OperationalError: table agents has no column named is_human` when `upsert_agent()` is called on a database created before this schema change.
**Why it happens:** `CREATE TABLE IF NOT EXISTS` skips creation if the table exists — it does not alter existing tables.
**How to avoid:** Add `ALTER TABLE agents ADD COLUMN is_human INTEGER NOT NULL DEFAULT 0` in `Store.__init__()` guarded by a try/except (SQLite raises `OperationalError: duplicate column name` if column already exists, which is safe to ignore).
**Warning signs:** Tests pass on fresh DB but fail on any existing `data/bifrost.db`.

### Pitfall 3: Route Ordering — `/agents` vs `/agents/list`
**What goes wrong:** `/agents/list` is matched by a catch-all or the `/agents` route before reaching its handler.
**Why it happens:** Starlette routes are matched in order. If `/agents` is registered before `/agents/list`, it may shadow it depending on path matching behavior.
**How to avoid:** Register more specific routes (e.g., `/agents/list`) before less specific ones, or use exact path matching. With Starlette's `Route`, exact path matching is the default — `/agents` does not match `/agents/list`. This should be safe, but verify ordering.
**Warning signs:** 404 on `/agents/list` even though the route is registered.

### Pitfall 4: Timestamps as Strings in Jinja2
**What goes wrong:** `{{ agent.last_seen.strftime('%H:%M:%S') }}` raises `AttributeError: 'str' object has no attribute 'strftime'`.
**Why it happens:** All timestamp fields on `Agent`, `Task`, `Conversation`, and `Event` are ISO string fields (see `models.py` — `_now()` returns `datetime.now(...).isoformat()`), not `datetime` objects.
**How to avoid:** Either parse in the route before passing to template, or register a Jinja2 filter in the templates instance. A `datetime_format` filter is the cleanest approach.
**Warning signs:** AttributeError in template rendering.

### Pitfall 5: `event.data` Structure Varies by Event Type
**What goes wrong:** Template code `event.data.get('parts', [])` returns empty for task events or metadata events.
**Why it happens:** `Event.data` stores different shapes depending on `EventType`. A `MESSAGE` event has a `parts` key, but `PARTICIPANT_ADDED` or `METADATA` events have different shapes.
**How to avoid:** In the activity feed and conversation detail partials, check `event.type` before rendering, or render `event.data` as a formatted string fallback.
**Warning signs:** Empty message bodies in activity feed for non-message events.

### Pitfall 6: Hub Instances Created Twice
**What goes wrong:** Route handlers in `dashboard/routes.py` create new `AgentHub(store)` instances separate from those in `app.py`. Write operations from MCP tools go to `app.py` hub instances; reads in dashboard go to separate hub instances reading from same SQLite DB.
**Why it happens:** Each hub instance holds no in-memory state beyond what's in SQLite (confirmed by reading hub code), so separate instances are safe for reads. But it's wasteful and could cause confusion.
**How to avoid:** Pass hub instances from `create_app()` into `setup_dashboard_routes()`, or accept that separate hub instances are fine since all state is in SQLite. The latter is acceptable for Phase 1.
**Warning signs:** Not a runtime bug — purely a design clarity issue.

## Code Examples

### Correct `TemplateResponse` Usage (Starlette 1.0.0)
```python
# Source: starlette/templating.py (installed: starlette 1.0.0)
# CORRECT — request is first positional arg
return templates.TemplateResponse(request, "agents.html", {"active": "agents"})

# WRONG — old signature, will fail on starlette 1.0.0
return templates.TemplateResponse("agents.html", {"request": request, "active": "agents"})
```

### Route Registration in `create_app()`
```python
# src/bifrost/app.py — inside create_app(), after mcp = FastMCP(...)
from bifrost.dashboard.routes import setup_dashboard_routes
setup_dashboard_routes(mcp, store)
```

### Schema Migration for `is_human` Column
```python
# src/bifrost/store/db.py — in Store.__init__(), after executescript(_SCHEMA)
try:
    self._conn.execute(
        "ALTER TABLE agents ADD COLUMN is_human INTEGER NOT NULL DEFAULT 0"
    )
    self._conn.commit()
except Exception:
    pass  # Column already exists — safe to ignore
```

### `Agent` Dataclass Extension
```python
# src/bifrost/store/models.py — Agent dataclass
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
    is_human: bool = False  # NEW FIELD
```

### Timestamp Filter for Jinja2
```python
# In setup_dashboard_routes(), after Jinja2Templates instantiation
from datetime import datetime, timezone

def _fmt_time(ts: str | None) -> str:
    if not ts:
        return "never"
    try:
        dt = datetime.fromisoformat(ts)
        return dt.strftime("%H:%M:%S")
    except ValueError:
        return ts

templates.env.filters["fmt_time"] = _fmt_time
# Usage in template: {{ agent.last_seen | fmt_time }}
```

### htmx Two-Panel Conversation Viewer (D-06)
```html
<!-- conversations.html -->
<div style="display:grid; grid-template-columns:1fr 2fr; gap:1rem;">
  <div id="conv-list"
       hx-get="/conversations/list"
       hx-trigger="load, every 10s"
       hx-swap="innerHTML">Loading...</div>
  <div id="conv-detail">
    <p>Select a conversation to view messages.</p>
  </div>
</div>
```

```html
<!-- partials/conversation_list.html -->
{% for conv in conversations %}
<article hx-get="/conversations/{{ conv.id }}/messages"
         hx-target="#conv-detail"
         hx-swap="innerHTML"
         style="cursor:pointer">
  <strong>{{ conv.title or conv.id }}</strong>
  {% if conv.channel %}<small>{{ conv.channel }}</small>{% endif %}
</article>
{% else %}
<p>No conversations.</p>
{% endfor %}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `TemplateResponse(name, {"request": request})` | `TemplateResponse(request, name, context)` | Starlette 1.0.0 | All template rendering code must use new signature |
| FastAPI `include_router()` | Starlette `Route` via `@mcp.custom_route()` | FastMCP design | Cannot use FastAPI router patterns |
| htmx 1.x `hx-trigger="every 5s"` | htmx 2.x — same syntax, still supported | htmx 2.0 (2024) | No change for polling patterns; extensions moved to separate packages |

**Deprecated/outdated (from design doc `2026-03-30-bifrost-v2-dashboard.md`):**
- Top-nav layout: Superseded by D-01 (sidebar layout)
- Table-based agent list: Superseded by D-04 (card grid)
- Custom CSS (`style.css`): Superseded by D-02 (Pico CSS via CDN)
- Old `TemplateResponse(name, dict)` signature in the design doc code examples: Must use new Starlette 1.0.0 signature

## Open Questions

1. **FastMCP `_custom_starlette_routes` type is `list[Route]` — can `Mount` objects be appended?**
   - What we know: The type annotation says `list[Route]`, but Python doesn't enforce this at runtime. The `routes.extend(self._custom_starlette_routes)` call in `streamable_http_app()` passes them to Starlette's router which accepts `Route | Mount`.
   - What's unclear: Whether FastMCP will break or warn if a `Mount` is in the list.
   - Recommendation: For Phase 1, serve static CSS/JS from CDN to avoid this entirely. If a local `StaticFiles` mount is needed later, test by appending a `Mount` directly and verifying it works.

2. **RESOLVED: `AgentHub.list_all(status=None) -> list[Agent]` exists and attaches cards**
   - What we know: `AgentHub.list_all()` is confirmed in `src/bifrost/hub/agents.py` (line 150). It calls `store.list_agents()` and then attaches cards via `store.get_card()` for each agent. Dashboard routes should use `agents_hub.list_all()`, not `store.list_agents()` directly, to get agents with their card data already populated.
   - What's unclear: Nothing — fully resolved.
   - Recommendation: Instantiate `AgentHub(store)` in `setup_dashboard_routes()` and call `agents_hub.list_all()` in the agent list handler.

3. **`upsert_agent()` current signature does not include `is_human`**
   - What we know: `upsert_agent()` in `db.py` hardcodes the column list without `is_human`. It will need to be updated.
   - What's unclear: Whether the upsert SQL also needs updating or if `DEFAULT 0` makes it safe to omit.
   - Recommendation: Update both `upsert_agent()` and `_row_to_agent()` in the same change as the model update. The INSERT must list `is_human` explicitly to support setting it to `True`.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Python 3.12+ | All code | ✓ | (project requirement) | — |
| mcp (FastMCP) | Route registration | ✓ (in uv cache) | 1.26.0 | — |
| fastapi | (transitive) | ✓ (in uv cache) | 0.135.2 | — |
| starlette | Routing, templates | ✓ (in uv cache) | 1.0.0 | — |
| jinja2 | Template rendering | ✓ (in pyproject.toml) | >=3.1.0 | — |
| htmx 2.0.8 | Browser interactivity | CDN | 2.0.8 | Serve locally |
| Pico CSS 2.1.1 | Styling | CDN | 2.1.1 | Serve locally |
| pytest | Testing | ✓ (dev dependency) | >=8.0 | — |

**No blocking dependencies missing.**

## Sources

### Primary (HIGH confidence)
- `/home/marc/.cache/uv/archive-v0/F3SRCMMwfedMWtjtqoa9-/mcp/server/fastmcp/server.py` — FastMCP 1.26.0 source: `custom_route()`, `_custom_starlette_routes`, `streamable_http_app()`, no `include_router()`
- `/home/marc/.cache/uv/archive-v0/ljLG1IrHc8mVMWTOKPH6O/starlette/templating.py` — Starlette 1.0.0: `Jinja2Templates.TemplateResponse(request, name, context)` signature confirmed
- `/home/marc/.cache/uv/archive-v0/ljLG1IrHc8mVMWTOKPH6O/starlette/staticfiles.py` — Starlette 1.0.0: `StaticFiles` available
- `src/bifrost/store/db.py` — Confirmed: `list_agents()`, `list_tasks()`, `list_conversations()`, `list_events()`, `upsert_agent()`, `_row_to_agent()` — all need `is_human` updates
- `src/bifrost/store/models.py` — Confirmed: `Agent` lacks `is_human` field; all timestamps are ISO strings, not datetime objects
- `src/bifrost/hub/agents.py` — Confirmed: `AgentHub.list_all(status=None)` exists, attaches cards; `get(agent_id)` also attaches card

### Secondary (MEDIUM confidence)
- WebFetch of `picocss.com/docs/version-picker` — Pico CSS 2.1.1 confirmed as latest; `pico.classless.min.css` confirmed at jsDelivr
- WebSearch — htmx 2.0.8 SRI hash `sha384-/TgkGk7p307TH7EXJDuUlgG3Ce1UVolAOFopFekQkkXihi5u/6OCvVKyz1W+idaz` (verify at implementation time via jsDelivr)

### Tertiary (LOW confidence)
- Design doc `docs/superpowers/plans/2026-03-30-bifrost-v2-dashboard.md` — Contains useful skeleton code but uses outdated TemplateResponse signature and raw CSS instead of Pico; superseded decisions noted

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — verified from installed sources, CDN URLs confirmed
- Architecture: HIGH — based on actual FastMCP 1.26.0 source inspection
- Pitfalls: HIGH — TemplateResponse signature change verified from Starlette 1.0.0 source; schema pitfall from reading db.py directly
- Open questions: all flagged with clear resolution paths

**Research date:** 2026-03-31
**Valid until:** 2026-05-01 (stable dependencies; CDN SRI hashes should be verified at implementation time)
