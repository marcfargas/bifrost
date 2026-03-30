# Pitfalls Research

**Domain:** Python MCP server + FastAPI + Jinja2/htmx dashboard + SSE push
**Researched:** 2026-03-30
**Confidence:** HIGH (most pitfalls directly observable from existing codebase + verified with FastAPI/htmx ecosystem sources)

## Critical Pitfalls

### Pitfall 1: Synchronous SQLite Blocking the Event Loop Inside SSE Generators

**What goes wrong:**
An SSE generator runs inside the async event loop. If it calls `Store` methods directly (which use synchronous `sqlite3`), every `SELECT` to check for new events freezes the entire server — all other HTTP requests queue behind it. With a polling interval of even 1 second and multiple connected SSE clients, the event loop is blocked continuously.

**Why it happens:**
The existing `Store` class is fully synchronous (`sqlite3`, `check_same_thread=False`). This was acceptable for MCP tool calls because FastMCP wraps handlers in a thread via `run_in_executor` implicitly. SSE generators are `async def` — they run directly on the event loop. Developers copy the sync DB call pattern from existing hub code without realising the execution context changed.

**How to avoid:**
Wrap every `Store` call inside the SSE generator in `asyncio.to_thread()`:
```python
events = await asyncio.to_thread(store.list_events, conversation_id, after=last_id)
```
Do NOT call hub methods directly from the generator without this wrapper. Add a `threading.Lock` (already recommended in CONCERNS.md) before introducing `to_thread` — the existing `check_same_thread=False` without a lock becomes a real race condition the moment two threads access the connection simultaneously.

**Warning signs:**
- Dashboard page loads slow down when an SSE stream is connected
- `uvicorn` worker appears "stuck" under load
- `asyncio` debug mode logs: `Coroutine X took X.Xs` for DB calls

**Phase to address:**
SSE implementation phase — before writing any SSE generator, wrap the Store. Do not defer.

---

### Pitfall 2: SSE Generator Keeps Running After Client Disconnects (Connection Leak)

**What goes wrong:**
When a browser tab is closed or refreshed, the `EventSource` disconnects. FastAPI/Starlette does not automatically cancel the async generator. The generator keeps polling SQLite, accumulating in memory, until the server process restarts or the generator happens to exit on its own. With a dashboard that auto-connects SSE on page load, every tab open/close cycle adds a leaked generator.

**Why it happens:**
`StreamingResponse` with an async generator does not propagate client disconnect into the generator as a cancellation. The generator is a coroutine that has no visibility into the transport layer unless explicitly checked.

**How to avoid:**
Use `sse-starlette` (`EventSourceResponse`) instead of raw `StreamingResponse`. It monitors `request.is_disconnected()` via a background task and raises `asyncio.CancelledError` into the generator. Use a `try/finally` in the generator to clean up (remove the client's queue from the registry, close DB cursors):
```python
async def event_generator(request: Request):
    try:
        while True:
            if await request.is_disconnected():
                break
            # yield events
    finally:
        # remove from active_connections dict
```
Re-raise `asyncio.CancelledError` — never swallow it.

**Warning signs:**
- Memory usage grows steadily over time without leveling off
- `ps aux` shows increasing open file descriptors
- SQLite WAL checkpoint logs show unexpectedly high read counts

**Phase to address:**
SSE implementation phase — first SSE endpoint, not after the fact.

---

### Pitfall 3: HTMX Auth Redirect Renders Login Page Inside a Partial Target

**What goes wrong:**
The dashboard has OAuth-protected routes. When a session expires (or the dashboard OAuth cookie is missing), the server returns a `302` redirect to the OIDC login flow. htmx intercepts the 302, follows it, gets back the login HTML, and swaps it into whatever `hx-target` was specified — a `<div>` in the middle of the page. The user sees a login form crammed into a table cell, not a full redirect.

**Why it happens:**
htmx does not distinguish between "intended partial response" and "this is a redirect to a full-page login". HTTP 302 redirects are followed by the browser transparently before htmx sees the response. The resulting HTML (the login page) is then treated as a partial and inserted.

**How to avoid:**
Add middleware (or a dependency) that detects the `HX-Request: true` header on unauthenticated requests and returns `HTTP 200` with an `HX-Redirect` response header pointing to the login URL, instead of a `302`. htmx will then perform a full-page redirect:
```python
if request.headers.get("HX-Request") and not authenticated:
    return Response(
        status_code=200,
        headers={"HX-Redirect": "/login"}
    )
```
Never return a 3xx to an htmx partial request.

**Warning signs:**
- Logging out or session timeout causes garbled UI instead of redirect to login
- Login form appears inside a widget/table
- Browser console shows unexpected 200s to `/login`

**Phase to address:**
Dashboard auth phase — apply before any authenticated partial endpoint goes live.

---

### Pitfall 4: SSE Endpoint Shares the Same `/sse` Path Across All Clients With No Per-Agent Fan-Out

**What goes wrong:**
A single SSE endpoint that broadcasts all events to all connected clients is built first because it is simpler. Later, per-agent filtering is added by reading `agent_id` from session and filtering in Python — but by then, every SSE connection is polling the full event table. With N connected clients, the DB is polled N times per interval, each time loading all events.

**Why it happens:**
The "broadcast one queue to all" pattern is the standard demo pattern in FastAPI SSE tutorials. It works for simple notifications but does not fit Bifrost's model where each agent has a private delivery inbox (`delivery_state` per agent).

**How to avoid:**
Design the SSE architecture per-agent from the start:
- Each SSE connection authenticates and identifies the agent (from session cookie or token)
- The SSE generator polls only `delivery_state` for that specific agent
- Use an in-process `asyncio.Queue` per connected agent to decouple the DB poll from the HTTP stream — one background task polls the DB and puts events into the queue; the SSE generator reads from the queue

This maps naturally to Bifrost's existing `DeliveryHub.check()` — the SSE generator is the async counterpart to `bifrost_check`.

**Warning signs:**
- Dashboard shows events from other agents
- CPU usage scales linearly with number of connected tabs

**Phase to address:**
SSE design phase — architecture decision before writing the first endpoint.

---

### Pitfall 5: Browser HTTP/1.1 Connection Limit Blocks Multiple Dashboard Tabs

**What goes wrong:**
Browsers enforce a maximum of 6 concurrent HTTP/1.1 connections per domain. Each SSE stream holds one connection open. Opening the dashboard in a second tab (or having both the agent activity feed and the task board as separate SSE streams) exhausts the connection pool. Subsequent API calls from htmx hang or fail silently.

**Why it happens:**
SSE uses a persistent TCP connection. On HTTP/1.1, each SSE endpoint counts against the 6-connection limit per origin. This is a well-documented SSE production issue that tutorial code never hits (single tab, single stream).

**How to avoid:**
- Use a single multiplexed SSE endpoint per dashboard page that pushes all event types (agent updates, task updates, messages) as named SSE events (`event: task_update`, `event: agent_update`)
- Use htmx's SSE extension to subscribe only to specific named events per component
- Ensure the deployment uses HTTP/2 (the Traefik setup at `bifrost.blegal.dev` should be capable — verify TLS termination enables HTTP/2)

**Warning signs:**
- Second browser tab causes first tab's htmx requests to hang
- Chrome DevTools shows "stalled" requests
- Dashboard feels fine in one tab, broken in two

**Phase to address:**
SSE design phase — one multiplexed endpoint, not multiple specialized streams.

---

## Technical Debt Patterns

Shortcuts that seem reasonable but create long-term problems.

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Calling `Store` sync methods directly in SSE generator | Avoids `asyncio.to_thread` boilerplate | Blocks event loop; all requests stall under load | Never |
| Global `asyncio.Queue` for SSE broadcast | Simple implementation | Leaks memory when clients disconnect; delivers wrong agent's events to wrong agent | Never — Bifrost is per-agent |
| Polling SQLite on every SSE heartbeat without exponential backoff | Always fresh data | Hammers DB when nothing changes; O(conversations) cost per tick (existing `DeliveryHub.check` concern) | Only if poll interval is >= 5s and load is known-low |
| Reusing the existing `delivery_state` polling as-is for SSE | Avoids new code | `DeliveryHub.check()` scans all conversations (known O(total) issue from CONCERNS.md); at SSE intervals this is destructive | Never without adding the junction table index first |
| Rendering full Jinja2 pages for htmx partial requests | Avoids separate partial templates | Sends 10-100x more HTML than needed; wastes bandwidth, increases swap jitter | Never — always check `HX-Request` header |
| Using the `--insecure` single-session mode for dashboard dev | No auth setup needed | Only one agent can exist at a time; cannot test multi-agent dashboard scenarios | Acceptable for very early UI skeleton only |

---

## Integration Gotchas

Common mistakes when connecting the new dashboard layer to existing Bifrost components.

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| `DeliveryHub.check()` from SSE generator | Calling it directly (sync, O(conversations) scan) | Wrap in `asyncio.to_thread`; add junction table before using at SSE frequency |
| Session-to-agent binding (`session_agents` dict) | Assuming dashboard sessions map to agent IDs the same way MCP sessions do | Dashboard uses browser cookies/sessions, not MCP access tokens; need a separate session mechanism (e.g., `itsdangerous` signed cookie containing agent_id) |
| The `--insecure` mode one-agent limitation | Developing dashboard against `--insecure` and discovering only one agent shows up | Run with OAuth or create a dev fixture that bypasses the single-key limitation |
| OAuth callback (`/callback` route) | Adding new `/callback` or auth-related routes that conflict with the existing `/callback` in `app.py` | Namespace dashboard routes under `/dashboard/...`; keep `/callback` exclusively for OIDC |
| `FastMCP`'s `_tool_manager` monkey-patch | Assuming `create_app()` returns a plain `FastAPI` instance that is easy to add routes to | It does return a Starlette/FastAPI-compatible app — but test that `.route()` and `mount()` additions survive without conflicting with the MCP transport at `/mcp` |
| Static files mount | Mounting `/static` after the MCP endpoint registration | Route order matters in Starlette; mount static files before dynamic catch-all routes; test that `/static` does not shadow any MCP or dashboard path |
| Human operator agent | Creating the human agent at `create_app()` time | If the DB is fresh, the agent is created. After server restart the agent already exists (upsert). After wipe it is gone. Ensure idempotent creation with a known fixed ID or name. |

---

## Performance Traps

Patterns that work at small scale but fail as usage grows.

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| `DeliveryHub.check()` per SSE tick (scans all conversations) | Dashboard slows as conversation count grows; SQLite CPU spikes | Add `agent_conversations` junction table first; never use full scan at SSE frequency | ~100 conversations or 3+ simultaneous SSE clients |
| N+1 in `AgentHub.list_all()` per agent directory refresh | Agent list page slow to render with many agents | Add `list_agents_with_cards()` JOIN query before wiring to dashboard | ~20 agents |
| No pagination on dashboard list endpoints | Agent/task/conversation pages load full table | Add `limit`/`offset` parameters to all Store list methods; dashboard defaults to 50 | ~500 rows |
| SSE polling interval too short (< 1s) with sync SQLite | Continuous event loop starvation; all MCP tool calls lag | Set SSE poll interval to minimum 2s; use async-safe DB access | Immediately with > 1 connected client |
| Rendering full Jinja2 page per SSE event | High CPU on server, lag in browser | Use targeted OOB swaps (`hx-swap-oob`) for small updates; only re-render changed components | Any realistic activity level |

---

## Security Mistakes

Domain-specific security issues beyond general web security.

| Mistake | Risk | Prevention |
|---------|------|------------|
| Dashboard SSE endpoint not checking agent identity | Any authenticated user sees all agents' events | SSE endpoint must read agent_id from session and filter delivery state to that agent only |
| HTMX POST actions (send message, cancel task) without CSRF protection | CSRF attack triggers agent actions via forged request from another origin | Add `fastapi-csrf-protect` or double-submit cookie pattern; include CSRF token in `hx-headers` on `<body>` |
| Dashboard OAuth bypass in `--insecure` mode | Deploying with `--insecure` and accidentally exposing dashboard to network | `--insecure` must only bind to `127.0.0.1` or have explicit network-level protection; dashboard routes should gate on `config.insecure` check |
| Human operator agent has no ownership check | Any authenticated dashboard user can act as the human agent | Tie human operator agent to the OIDC `sub` claim of the logged-in user; one human agent per OIDC identity |
| Task/message actions from dashboard not rate-limited | Automated browser or script can spam messages through the dashboard | Apply same input size limits from CONCERNS.md (64KB body limit) to dashboard form actions |
| Session fixation via cookie that maps to agent_id | Cookie stolen = full agent impersonation | Use signed, short-lived session cookies (e.g., `itsdangerous.TimestampSigner`); regenerate on OIDC callback |

---

## UX Pitfalls

Common user experience mistakes in this domain.

| Pitfall | User Impact | Better Approach |
|---------|-------------|-----------------|
| SSE reconnect shows stale data while reconnecting | User sees outdated agent list / task board during reconnect gap | Track `Last-Event-ID`; on reconnect, replay missed events or full-refresh the component |
| No loading indicator on htmx partial refreshes | User clicks "send message", nothing visibly happens for 200ms | Use `htmx:beforeRequest` / `htmx:afterRequest` to show/hide spinners via CSS classes |
| Full page flash on dashboard navigation | Using standard `<a>` links instead of htmx for navigation causes full reload | Use `hx-boost` on the `<body>` or htmx-driven navigation for all internal links |
| Dashboard shows "online" for all agents forever | Agents that disconnected hours ago appear active | Surface `AgentStatus` from DB; the "no disconnect detection" concern (CONCERNS.md) directly affects this — mark as known limitation in dashboard UI or add visual staleness indicator based on last-seen timestamp |
| Human operator actions silently fail if hub error | User sends message, nothing happens, no feedback | Dashboard action endpoints must return `HX-Trigger` or inline error HTML on failure, not just HTTP 500 |

---

## "Looks Done But Isn't" Checklist

Things that appear complete but are missing critical pieces.

- [ ] **SSE endpoint:** Check that client disconnect actually stops the generator — connect in browser, close tab, verify server-side generator exits via log line.
- [ ] **Dashboard auth:** Check that navigating directly to `/dashboard` without a session redirects to OIDC login, not a 500 or blank page.
- [ ] **HTMX partials:** Check that direct browser navigation to a partial URL (without `HX-Request` header) returns a full page, not a fragment inserted into nothing.
- [ ] **Human operator agent:** Check that server restart does not create a duplicate human agent — upsert, not insert.
- [ ] **SSE + htmx OOB:** Check that out-of-band swaps arrive on all named event types — the htmx SSE extension has historically had gaps in OOB swap support (verify with htmx 2.x SSE extension docs).
- [ ] **CSRF on POST actions:** Check that form submissions from the dashboard include a CSRF token — test that forged POST from another origin is rejected.
- [ ] **Static assets:** Check that CSS/JS assets load correctly behind the Traefik reverse proxy — verify `url_for` generates correct absolute URLs and proxy headers (`X-Forwarded-Proto`) are respected by Jinja2's `request.url_for()`.
- [ ] **Pagination:** Check that task/agent/conversation list pages do not fetch unbounded rows — verify LIMIT is applied before any list endpoint is wired to the dashboard.

---

## Recovery Strategies

When pitfalls occur despite prevention, how to recover.

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Event loop blocked by sync SQLite in SSE | MEDIUM | Add `asyncio.to_thread` wrapper to all Store calls used by SSE generators; no schema change required |
| SSE generator leak (connections accumulate) | LOW | Add disconnect check (`request.is_disconnected()`) + `try/finally` cleanup; deploy; leaks stop on next restart |
| Login page injected into partial target | LOW | Add htmx-aware auth middleware (20 lines); no template changes required |
| Multiple SSE streams hitting connection limit | MEDIUM | Merge streams into one multiplexed endpoint; update client-side htmx SSE extension config to filter by event name |
| Human operator duplicate on restart | LOW | Change `create_agent` to `upsert_agent` with fixed known ID; run migration if duplicates already exist |
| `DeliveryHub.check()` O(conversations) at SSE frequency causes DB overload | HIGH | Requires schema migration to add junction table; rollback path is increasing SSE poll interval as emergency measure |

---

## Pitfall-to-Phase Mapping

How roadmap phases should address these pitfalls.

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| Sync SQLite blocks event loop in SSE | SSE foundation (first phase) | Load test: 3 SSE clients + simultaneous MCP tool call; response time < 200ms |
| SSE generator connection leak | SSE foundation (first phase) | Connect, close tab, verify generator exits via log; repeat 10 times, check memory stable |
| Auth redirect into partial target | Dashboard auth phase | Log out mid-session, click an htmx-powered button; verify full-page redirect to login |
| Wrong agent's events in SSE | SSE design (before first endpoint) | Connect two agents; verify each SSE stream only receives its own delivery state |
| Browser HTTP/1.1 6-connection limit | SSE design (before first endpoint) | Open dashboard in 2 tabs; verify htmx requests in both tabs succeed |
| `DeliveryHub.check()` at SSE frequency | SSE foundation | Benchmark with 50 conversations before wiring; add index if p99 > 10ms |
| CSRF on POST actions | Dashboard interactive actions phase | Use browser devtools to forge POST without CSRF token; verify 403 |
| Human operator duplicate | Dashboard human agent phase | Restart server 3 times; verify only one human agent exists in DB each time |
| Pagination missing from list endpoints | Dashboard list views phase | Seed 200 tasks; verify list endpoint returns <= 50 with cursor/offset |

---

## Sources

- FastAPI SSE official docs: https://fastapi.tiangolo.com/tutorial/server-sent-events/
- `sse-starlette` PyPI + disconnect detection: https://pypi.org/project/sse-starlette/
- Client disconnect in FastAPI (Marcelo Tryle): https://marcelotryle.com/blog/2024/06/06/understanding-client-disconnection-in-fastapi/
- FastAPI async pitfalls and scaling: https://www.mindfulchase.com/explore/troubleshooting-tips/back-end-frameworks/troubleshooting-fastapi-async-pitfalls,-performance,-and-scaling-strategies.html
- asyncio queues for SSE: https://medium.com/@Rachita_B/lookout-for-these-cryptids-while-working-with-server-sent-events-43afabb3a868
- SSE production-readiness issues: https://dev.to/miketalbot/server-sent-events-are-still-not-production-ready-after-a-decade-a-lesson-for-me-a-warning-for-you-2gie
- Browser 6-connection SSE limit (MDN): https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events
- htmx auth redirect handling (Django, applies to FastAPI): https://saurabh-kumar.com/articles/2025/05/handling-django-authentication-redirects-in-htmx-applications/
- htmx hx-boost OAuth redirect pitfall: https://github.com/bigskysoftware/htmx/issues/2442
- HTMX + FastAPI patterns (TestDriven.io): https://testdriven.io/blog/fastapi-htmx/
- HTMX SSE extension OOB swaps: https://medium.com/@adam.giacom/pushing-the-limits-of-htmx-out-of-band-updates-via-sse-f7ca48ce711a
- htmx web security basics: https://htmx.org/essays/web-security-basics-with-htmx/
- Starlette route order (swallowed route): https://siliconandsoul.substack.com/p/the-case-of-the-swallowed-route-in
- Synchronous SQLAlchemy in async FastAPI: https://medium.com/@rndm5345/best-practice-for-synchronous-sqlalchemy-sessions-in-async-fastapi-4b6131247cce
- Codebase concerns (internal): `.planning/codebase/CONCERNS.md` (2026-03-30)

---
*Pitfalls research for: Bifrost dashboard + SSE push on existing Python MCP server*
*Researched: 2026-03-30*
