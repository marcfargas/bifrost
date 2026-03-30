# Stack Research

**Domain:** Interactive web dashboard + SSE push for existing FastAPI/FastMCP server
**Researched:** 2026-03-30
**Confidence:** HIGH (core choices verified against official FastAPI docs and htmx docs)

## Context

This is an additive milestone on an existing working system. The server already runs
FastAPI >= 0.115.0, Jinja2 >= 3.1.0, and Starlette. The research question is: what
exactly do we need to add, at what versions, and how?

Existing dependencies that are already present and require no changes:

- `fastapi >= 0.115.0` — already in pyproject.toml
- `jinja2 >= 3.1.0` — already in pyproject.toml
- Starlette — transitive via fastapi, provides StaticFiles and SessionMiddleware

---

## Recommended Stack

### Core Technologies

| Technology | Required Version | Purpose | Why Recommended |
|------------|-----------------|---------|-----------------|
| FastAPI | >= 0.135.2 | Native SSE via `EventSourceResponse` | 0.135.0 (2026-03-01) added `fastapi.sse.EventSourceResponse` — no third-party SSE library needed. Pin to 0.135.2 (latest stable). |
| Jinja2 | >= 3.1.0 (already present) | HTML template rendering | Standard FastAPI templating; `Jinja2Templates` from `fastapi.templating` wraps Starlette's implementation. Already in the project. |
| Starlette StaticFiles | (transitive) | Serve JS/CSS assets | `app.mount("/static", StaticFiles(directory="static"), name="static")` — zero extra install, ships with Starlette. |
| Starlette SessionMiddleware | (transitive) | Dashboard OAuth session cookie | Ships with Starlette; stores signed cookie via `itsdangerous`. Required for OIDC authorization-code redirect flow. |
| htmx | 2.0.8 (CDN or vendored) | Hypermedia-driven UI interactions | The standard choice for server-rendered interactive UIs without a JS build step. Pairs with Jinja2 HTML fragments over API JSON. |
| htmx-ext-sse | 2.2.4 (CDN or vendored) | SSE event consumption from htmx | The official htmx SSE extension for htmx 2.x (replaces the old built-in `hx-sse`). |

### Supporting Libraries

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `itsdangerous` | >= 2.0 (transitive via Starlette SessionMiddleware) | Session cookie signing | Required automatically when `SessionMiddleware` is added. No explicit install needed — Starlette will raise at startup if missing. |
| `authlib` | >= 1.3.0 | OIDC authorization-code flow for the dashboard | Use if you want a clean `authorize_redirect` / `authorize_access_token` wrapper on top of the existing OIDC provider. Alternative: implement the redirect loop manually with `httpx` (same pattern as the existing `DexOAuthProvider`). |
| `asyncio.Queue` (stdlib) | stdlib | SSE broadcast bus — fanout events to all SSE clients | Use one queue per connected SSE client; a central dispatcher puts events in every queue on hub operations. No extra install. |

### Development Tools

| Tool | Purpose | Notes |
|------|---------|-------|
| `pytest-asyncio` (already present) | Testing async SSE generators and template routes | Already configured with `asyncio_mode = "auto"`. No change needed. |
| `httpx` (already present) | Test dashboard HTTP routes with cookie sessions | Use `httpx.AsyncClient` with `follow_redirects=True` for OAuth redirect testing. |

---

## Installation

The only hard dependency bump needed:

```bash
# Bump FastAPI to get EventSourceResponse (0.135.0+)
# Edit pyproject.toml: fastapi >= 0.135.2

# If using Authlib for dashboard OIDC (optional — see notes below)
pip install "authlib>=1.3.0"

# itsdangerous is already a transitive dep of Starlette/FastAPI
# No explicit install needed for SessionMiddleware
```

htmx and htmx-ext-sse are loaded from CDN or vendored as static files — no pip install.

```html
<!-- In base Jinja2 template -->
<script src="https://cdn.jsdelivr.net/npm/htmx.org@2.0.8/dist/htmx.min.js"
        integrity="sha384-/TgkGk7p307TH7EXJDuUlgG3Ce1UVolAOFopFekQkkXihi5u/6OCvVKyz1W+idaz"
        crossorigin="anonymous"></script>
<script src="https://cdn.jsdelivr.net/npm/htmx-ext-sse@2.2.4"
        integrity="sha384-A986SAtodyH8eg8x8irJnYUk7i9inVQqYigD6qZ9evobksGNIXfeFvDwLSHcp31N"
        crossorigin="anonymous"></script>
```

For air-gapped or offline-capable deployments, vendor both files into `src/bifrost/static/js/`.

---

## Alternatives Considered

| Recommended | Alternative | When to Use Alternative |
|-------------|-------------|-------------------------|
| Native `fastapi.sse.EventSourceResponse` (0.135.0+) | `sse-starlette` (third party) | Only if you need cooperative shutdown signals, thread-safe multi-loop support, or custom ping intervals. For this project (single-threaded asyncio, basic fanout), native SSE is sufficient and has zero extra dependencies. |
| `asyncio.Queue` fanout bus (stdlib) | Redis pub/sub | Only when scaling to multiple server processes. Single-container SQLite deployment has no need for external pub/sub. |
| Starlette `SessionMiddleware` (already transitive) | JWT in Authorization header | Browser dashboard requires cookies — MCP clients use Bearer tokens, but the dashboard's browser tab needs a session cookie. |
| `authlib` for dashboard OIDC | Manual `httpx` OIDC redirect loop | The existing `DexOAuthProvider` already implements OIDC with raw `httpx`. Reuse that pattern for the dashboard rather than adding `authlib` unless the manual approach becomes unwieldy. |
| htmx 2.0.8 via CDN/vendored | Alpine.js, Vue, React | Full JS frameworks require a build step and defeat the "no frontend build toolchain" constraint. htmx requires only `<script>` tags. |
| Jinja2 HTML fragment responses | JSON API + JS rendering | htmx swaps HTML fragments returned by FastAPI endpoints directly into the DOM. This avoids a JS data layer entirely and keeps the server as the single source of rendering truth. |

---

## What NOT to Use

| Avoid | Why | Use Instead |
|-------|-----|-------------|
| `sse-starlette` third-party library | Redundant since FastAPI 0.135.0 added `EventSourceResponse` natively. Adding a dependency for what ships in FastAPI is pure weight. | `from fastapi.sse import EventSourceResponse` |
| WebSockets for the dashboard activity feed | SSE is unidirectional server-to-client push — exactly the use case. WebSockets add bidirectional complexity and a connection upgrade protocol for no benefit here. | `EventSourceResponse` + normal POST routes for actions |
| `fastapi-htmx` or other HTMX integration libraries | Thin wrappers that add cognitive overhead without real value. FastAPI routes returning `TemplateResponse` with partial templates is all that's needed. | Plain `TemplateResponse` with request-aware partial detection |
| `aiofiles` for template loading | Jinja2's built-in file loader is synchronous but fast (templates are cached after first load). `aiofiles` adds complexity with no measurable benefit for template serving. | `Jinja2Templates(directory=...)` default loader |
| SQLAlchemy or any ORM | Project explicitly uses raw SQLite via stdlib `sqlite3`. Dashboard queries are read-heavy and simple — adding an ORM for a dashboard milestone is out of scope. | Direct `sqlite3` queries via existing `Store` class |

---

## Stack Patterns by Variant

**SSE endpoint (native FastAPI 0.135.0+):**
```python
from collections.abc import AsyncIterable
from fastapi.sse import EventSourceResponse, ServerSentEvent

@router.get("/events", response_class=EventSourceResponse)
async def event_stream(request: Request) -> AsyncIterable[ServerSentEvent]:
    queue: asyncio.Queue = asyncio.Queue()
    broadcast_bus.register(queue)
    try:
        while True:
            if await request.is_disconnected():
                break
            event = await asyncio.wait_for(queue.get(), timeout=30.0)
            yield ServerSentEvent(data=event, event="update")
    finally:
        broadcast_bus.unregister(queue)
```

**htmx SSE consumer (in Jinja2 template):**
```html
<div hx-ext="sse" sse-connect="/dashboard/events">
  <div id="activity-feed" sse-swap="activity_event" hx-swap="afterbegin">
    <!-- server pushes <li> fragments here -->
  </div>
</div>
```

**Dashboard route returning HTML fragment (htmx partial):**
```python
@router.get("/tasks/{task_id}/fragment", response_class=HTMLResponse)
async def task_fragment(request: Request, task_id: str):
    task = store.get_task(task_id)
    return templates.TemplateResponse(
        request=request,
        name="fragments/task_row.html",
        context={"task": task},
    )
```

**Dashboard session auth dependency:**
```python
from starlette.middleware.sessions import SessionMiddleware

app.add_middleware(SessionMiddleware, secret_key=config.session_secret)

async def require_dashboard_session(request: Request) -> dict:
    user = request.session.get("user")
    if not user:
        raise HTTPException(status_code=303, headers={"Location": "/dashboard/login"})
    return user
```

---

## Version Compatibility

| Package | Compatible With | Notes |
|---------|-----------------|-------|
| `fastapi >= 0.135.2` | `pydantic >= 2.9.0` | 0.135.2 bumped pydantic lower bound to 2.9.0. Check existing pydantic transitive version. |
| `fastapi >= 0.135.2` | `starlette` (managed by FastAPI) | FastAPI pins its own compatible Starlette version. Do not pin Starlette separately. |
| `htmx 2.0.8` | `htmx-ext-sse 2.2.4` | htmx-ext-sse 2.x is designed for htmx 2.x. Do not mix htmx 1.x with htmx-ext-sse 2.x. |
| `htmx 2.0.x` | FastAPI's `EventSourceResponse` | htmx SSE extension connects via standard `EventSource` browser API — no special server integration needed. |

---

## Sources

- [FastAPI Templates docs](https://fastapi.tiangolo.com/advanced/templates/) — Jinja2Templates API, StaticFiles mount pattern (HIGH confidence, official)
- [FastAPI SSE docs](https://fastapi.tiangolo.com/tutorial/server-sent-events/) — EventSourceResponse, ServerSentEvent, version 0.135.0+ (HIGH confidence, official)
- [FastAPI release notes](https://fastapi.tiangolo.com/release-notes/) — confirmed 0.135.0 added SSE (2026-03-01), 0.135.2 is latest stable (HIGH confidence, official)
- [htmx SSE extension docs](https://htmx.org/extensions/sse/) — hx-ext="sse", sse-connect, sse-swap attributes, version 2.2.4 (HIGH confidence, official)
- [htmx CDN version 2.0.8 with integrity hash](https://www.jsdelivr.com/package/npm/htmx.org) — MEDIUM confidence (verified against jsDelivr but hash should be re-verified at implementation time)
- [Starlette SessionMiddleware](https://www.starlette.io/middleware/) — itsdangerous cookie signing, session dict API (HIGH confidence, official)
- [Authlib Starlette OAuth docs](https://docs.authlib.org/en/latest/client/starlette.html) — authorize_redirect / authorize_access_token pattern, version 1.6.6 (MEDIUM confidence, official but alternative to existing manual approach)
- [sse-starlette GitHub](https://github.com/sysid/sse-starlette) — third-party library evaluated and rejected in favor of native FastAPI SSE (MEDIUM confidence)

---

*Stack research for: Bifrost dashboard + SSE milestone*
*Researched: 2026-03-30*
