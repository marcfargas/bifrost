# Project Research Summary

**Project:** Bifrost Dashboard + SSE Push
**Domain:** Interactive web dashboard with real-time push on an existing FastMCP/FastAPI Python server
**Researched:** 2026-03-30
**Confidence:** HIGH (STACK and PITFALLS verified against official sources; ARCHITECTURE derived from existing codebase)

## Executive Summary

Bifrost already has a working MCP server (FastAPI + SQLite + OIDC). The dashboard milestone is purely additive — it layers a browser UI on top of the existing hub and store layers without replacing or refactoring any core logic. The recommended approach is htmx + Jinja2 HTML fragments served by an `APIRouter` inside the same FastMCP/Starlette process, with real-time push via FastAPI's native `EventSourceResponse` (added in 0.135.0). No frontend build toolchain, no new database, no separate process — the entire dashboard is a new `src/bifrost/dashboard/` package that wires into `app.py` at startup.

The single non-trivial technical investment is the SSE broadcaster: an in-process `asyncio.Queue`-per-client fanout that hooks into the hub write paths (`ConversationHub.send`, `TaskHub.update_status`). This must be designed per-agent from day one — one multiplexed SSE endpoint per dashboard page delivering named events — because retrofitting it later requires coordinated changes across routes, templates, and hub internals. The human operator identity (a flagged `Agent` record with `is_human=True`) enables the dashboard user to appear in the agent ecosystem without a parallel identity system.

The biggest risks are all async-correctness and browser-behavior issues: synchronous SQLite calls blocking the event loop inside SSE generators, connection leaks when browser tabs close, login redirects rendering inside htmx partial targets, and the browser's 6-connection-per-origin HTTP/1.1 limit exhausted by multiple SSE streams. All five critical pitfalls have well-understood mitigations that must be baked in from the start of the SSE implementation phase, not added later.

## Key Findings

### Recommended Stack

The project needs only one dependency bump: FastAPI from the current version to >= 0.135.2, which added native `EventSourceResponse` and `ServerSentEvent` (2026-03-01). All other stack components — Jinja2, Starlette StaticFiles, Starlette SessionMiddleware, httpx, pytest-asyncio — are already present. htmx 2.0.8 and htmx-ext-sse 2.2.4 are loaded from CDN (or vendored) as `<script>` tags; no npm or build step is involved.

**Core technologies:**
- `fastapi >= 0.135.2`: version bump required for native SSE — eliminates the `sse-starlette` third-party dependency
- `jinja2 >= 3.1.0`: already present; Jinja2Templates + partial templates for htmx fragment responses
- `starlette` (transitive): StaticFiles mount for CSS/JS assets; SessionMiddleware for dashboard OIDC session cookie
- `htmx 2.0.8` (CDN/vendored): hypermedia UI — no JS build toolchain, pairs directly with Jinja2 fragment responses
- `htmx-ext-sse 2.2.4` (CDN/vendored): official htmx 2.x SSE extension for `hx-ext="sse"` / `sse-connect` / `sse-swap`
- `asyncio.Queue` (stdlib): per-client SSE fanout bus — no Redis or external pub/sub needed for single-process deployment

Explicitly rejected: `sse-starlette` (redundant since 0.135.0), WebSockets (SSE is unidirectional — right tool for the job), any JS framework (React/Vue/Alpine require a build step), ORM (dashboard queries are read-heavy and simple; existing `Store` class is sufficient).

### Expected Features

FEATURES.md was not produced by the parallel research agents (file missing from `.planning/research/`). Feature scope has been inferred from the architecture and pitfalls research, the existing codebase (`docs/superpowers/plans/2026-03-30-bifrost-v2-dashboard.md`), and the STACK research context.

**Must have (table stakes):**
- Agent directory — list all registered agents with status and card data
- Task board — view all tasks with status, assignee, and lifecycle controls (cancel, reject)
- Conversation view — list conversations and read message threads
- Activity feed — real-time push of hub events (new messages, task transitions) via SSE
- Human operator identity — dashboard user appears as a named `Agent` in the ecosystem, can send messages and create tasks via POST forms
- Dashboard OIDC auth — session cookie gate in production; `--insecure` bypass for local dev

**Should have (competitive):**
- Interactive actions — send message to agent, create/cancel task from the dashboard UI
- SSE-based delivery push — replaces `bifrost_check` polling for MCP agents receiving events
- Out-of-band htmx swaps — update multiple page components from a single SSE event without full-page reload

**Defer (v2+):**
- Redis pub/sub fanout (required only for multi-process deployment)
- Pagination cursor API (add limit/offset first; cursor-based pagination is a later optimization)
- Agent-to-agent orchestration UI (complex workflow visualization)
- Mobile-responsive layout (operator dashboard, not a consumer product)

### Architecture Approach

The dashboard is a self-contained `src/bifrost/dashboard/` package with a `create_dashboard_router(store, broadcaster)` factory that returns an `APIRouter`. This router is included into the underlying Starlette app in `app.py` alongside a static files mount. The `SSEBroadcaster` singleton (created in `create_app`) is injected into both the router (for SSE endpoints) and the hub write paths (for event publishing). All dashboard routes call hub methods directly — same pattern as `mcp/tools.py` — with no abstraction layer between routes and hubs.

**Major components:**
1. `dashboard/routes.py` — all GET (full-page + partial) and POST (action) handlers; factory returns `APIRouter`
2. `dashboard/broadcaster.py` — `SSEBroadcaster`: dict of `agent_id -> list[asyncio.Queue]`, `subscribe/unsubscribe/publish/broadcast` methods
3. `dashboard/templates/` — Jinja2 base layout + per-page templates + `partials/` subdirectory for htmx swap targets
4. `dashboard/static/` — minimal CSS; no JS frameworks
5. Human operator agent — `Agent` dataclass gains `is_human: bool = False`; `AgentHub.get_or_create_human_operator()` is an idempotent upsert

**Key patterns:**
- Page + partial route pairs: every dynamic view has a full-page route (direct navigation) and a fragment route (htmx `hx-get` poll)
- PRG (Post-Redirect-Get): POST action routes redirect to GET on success; no double-submit
- SSE generator reads only from `asyncio.Queue`; all SQLite access happens outside the generator (on the write path or in separate request handlers)
- Build order is strictly: schema migration → dashboard skeleton → read-only pages → human operator → SSE broadcaster → SSE activity feed → agent SSE delivery → dashboard OAuth

### Critical Pitfalls

1. **Synchronous SQLite blocks the event loop inside SSE generators** — wrap every `Store` call inside SSE generators in `asyncio.to_thread()`; add a `threading.Lock` before introducing concurrent threads. Do this before writing the first SSE generator, not after observing slowdowns.

2. **SSE generator keeps running after client disconnects (connection leak)** — use `try/finally` in the async generator to call `broadcaster.unsubscribe()`; poll `await request.is_disconnected()` inside the generator loop. Every browser tab close without this leaks a coroutine and a queue.

3. **htmx auth redirect renders login page inside a partial target** — detect `HX-Request: true` header on unauthenticated requests and return `HTTP 200` with `HX-Redirect` header instead of a `302`. Never return 3xx to an htmx partial request.

4. **Multiple SSE streams exhaust the browser's 6 HTTP/1.1 connections per origin** — use a single multiplexed SSE endpoint per page delivering all event types as named SSE events (`event: task_update`, `event: agent_update`); use htmx SSE extension's named event subscriptions to route them to the right DOM targets. Design this upfront, not as a retrofit.

5. **`DeliveryHub.check()` O(conversations) scan at SSE polling frequency hammers SQLite** — add the `agent_conversations` junction table index before wiring `DeliveryHub` to the SSE path; benchmark with 50 conversations; never use the full-scan pattern at SSE frequency.

## Implications for Roadmap

Based on research, the architecture's own build order (derived from component dependencies) directly maps to the phase structure. No phase should be reordered without understanding the dependency chain.

### Phase 1: Schema Migration + Dashboard Skeleton

**Rationale:** The `is_human` flag on `Agent` is a prerequisite for human operator functionality in every subsequent phase. The dashboard skeleton (router factory, base template, static mount, app wiring) must exist before any routes can be added. These two tasks are small and can be combined into one low-risk phase.
**Delivers:** Working `/dashboard` route serving a placeholder page; `is_human` column in SQLite; confirmed `include_router` wiring strategy validated against the installed FastMCP version.
**Addresses:** Human operator identity (table stakes feature)
**Avoids:** FastMCP internal API ambiguity — verify the `starlette_app.include_router()` approach against the actual FastMCP version before building more routes on top of it.

### Phase 2: Read-Only Dashboard Pages

**Rationale:** Read-only pages use only existing hub methods and the Jinja2 + htmx polling pattern. No SSE, no auth, no POST forms. This phase validates the full render pipeline end-to-end with zero async-correctness risk.
**Delivers:** Agent directory, task board, conversation list — all with 10-second htmx polling partials.
**Uses:** Jinja2Templates, htmx `hx-get` polling, page + partial route pair pattern
**Implements:** `dashboard/routes.py` (GET routes), `dashboard/templates/` full structure
**Avoids:** No SSE yet — keeps async complexity out of scope; enforces pagination with LIMIT from the start (pitfall prevention: unbounded list queries)

### Phase 3: Human Operator Interactive Actions

**Rationale:** POST forms (send message, create task, cancel task) use the PRG pattern and call existing hub methods — no new infrastructure. Completes the human operator identity story before SSE is introduced. Validates that hub write paths work correctly from the dashboard context.
**Delivers:** Dashboard user can send messages and manage tasks; human operator agent appears in agent listings.
**Implements:** `AgentHub.get_or_create_human_operator()`, POST action routes, PRG redirect pattern
**Avoids:** Human operator duplicate on restart (upsert with fixed name, not insert); CSRF protection on POST actions required here.

### Phase 4: SSE Broadcaster Foundation

**Rationale:** The broadcaster is infrastructure that all real-time features depend on. Build and test it in isolation before wiring it to any UI or hub write path. This phase is where the critical async-correctness pitfalls must be addressed — not deferred.
**Delivers:** `SSEBroadcaster` class with subscribe/unsubscribe/publish/broadcast; `/dashboard/events` SSE endpoint returning named events; disconnect cleanup verified; no-blocking-SQLite pattern established.
**Uses:** `fastapi.sse.EventSourceResponse` (requires FastAPI >= 0.135.2 bump), `asyncio.Queue` per client
**Avoids:** Sync SQLite in SSE generator (asyncio.to_thread pattern); SSE connection leaks (try/finally cleanup); multiple streams per page (single multiplexed endpoint from the start); wrong-agent event delivery (per-agent queue design)

### Phase 5: SSE Activity Feed (Dashboard Real-Time UI)

**Rationale:** Wire the broadcaster to the hub write paths and connect the htmx SSE extension in the activity feed template. Depends on Phase 4 broadcaster being tested in isolation.
**Delivers:** Live activity feed in the dashboard that updates without polling; named SSE events driving OOB swaps across page components.
**Uses:** htmx-ext-sse, `sse-swap` / `sse-swap-oob` attributes, broadcaster injection into `ConversationHub.send()` and `TaskHub.update_status()`
**Avoids:** Full-page Jinja2 render per SSE event (use targeted `hx-swap-oob` fragments); stale data on SSE reconnect (track `Last-Event-ID`)

### Phase 6: Agent SSE Delivery (Replaces bifrost_check Polling)

**Rationale:** Expose the broadcaster to MCP agents so they receive events via push rather than polling `bifrost_check`. This is a quality-of-life improvement for agent authors that also removes the O(conversations) polling concern from the hot path.
**Delivers:** `/agent-events` SSE endpoint for MCP agents; `bifrost_check` demoted to fallback.
**Avoids:** `DeliveryHub.check()` at SSE frequency without the junction table index (must be added or benchmarked before this phase).

### Phase 7: Dashboard OAuth (Production Gate)

**Rationale:** Auth is last because it gates all previous work. Adding it earlier would slow development iteration. In `--insecure` mode all previous phases are fully testable. The htmx auth redirect pitfall must be addressed here.
**Delivers:** `SessionMiddleware` + OIDC login redirect; session tied to OIDC `sub` claim; human operator agent per OIDC identity; `--insecure` mode restricted to `127.0.0.1`.
**Avoids:** htmx partial target receiving login page HTML (HX-Redirect response header pattern); session fixation (signed, short-lived cookies, regenerated on OIDC callback).

### Phase Ordering Rationale

- Schema migration must precede all hub changes (everything depends on the model).
- Read-only pages before interactive actions (validate render pipeline before adding write complexity).
- Interactive actions before SSE (validate hub write paths in the synchronous PRG context before introducing async generators on top of them).
- SSE broadcaster in isolation before wiring to UI or hub write paths (async correctness is easier to verify in a standalone test than after it is already threaded through the template layer).
- Agent SSE delivery after the dashboard activity feed (reuses the same broadcaster; adds a new consumer endpoint).
- OAuth last (avoid blocking all other phases behind auth setup).

### Research Flags

Phases likely needing deeper research during planning:
- **Phase 1 (skeleton wiring):** The exact API for accessing FastMCP's underlying Starlette app to call `include_router()` is LOW confidence — must be verified against the installed FastMCP version. If `include_router` is unavailable, fallback is `@mcp.custom_route()` for each route (verbose but safe).
- **Phase 5 (htmx SSE OOB swaps):** The htmx SSE extension's OOB swap support (`hx-swap-oob` delivered via SSE) has historically had gaps. Verify behavior against htmx-ext-sse 2.2.4 docs before building the activity feed on this pattern.
- **Phase 6 (agent SSE delivery):** The `DeliveryHub.check()` O(conversations) concern requires a benchmark or schema change before this phase. Needs research into the exact SQL query plan and whether a junction table index is sufficient.

Phases with standard patterns (skip research-phase):
- **Phase 2 (read-only pages):** Jinja2 + htmx polling is thoroughly documented; page + partial route pair pattern is textbook.
- **Phase 3 (human operator actions):** PRG pattern + hub method calls; no new infrastructure.
- **Phase 7 (dashboard OAuth):** Starlette SessionMiddleware + OIDC redirect loop is well-documented; existing `DexOAuthProvider` is prior art in the codebase.

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | FastAPI 0.135.0 SSE addition verified against official release notes; htmx versions confirmed via jsDelivr (SRI hashes should be re-verified at implementation). Authlib is an optional alternative; the existing manual OIDC approach is the preferred path. |
| Features | MEDIUM | FEATURES.md was not produced (file missing). Feature scope inferred from architecture plan (`docs/superpowers/plans/2026-03-30-bifrost-v2-dashboard.md`), STACK and PITFALLS context, and existing codebase. Feature list should be validated against the planning document before roadmap creation. |
| Architecture | HIGH | Component boundaries derived from the existing codebase (direct inspection of `app.py`, hub classes, store). FastMCP `include_router` integration point is LOW confidence — one specific API call needs verification at implementation time, but the overall pattern is sound. |
| Pitfalls | HIGH | Five critical pitfalls verified across multiple independent sources (FastAPI official docs, MDN, community implementations). Mitigations are concrete and testable. |

**Overall confidence:** HIGH — with one specific gap (FEATURES.md missing) and one LOW-confidence integration point (FastMCP `include_router` API).

### Gaps to Address

- **Missing FEATURES.md:** The features research agent did not produce output. Before the roadmapper finalizes phase scope, validate the inferred feature list above against `docs/superpowers/plans/2026-03-30-bifrost-v2-dashboard.md` and confirm must-have vs. defer decisions.
- **FastMCP `include_router` API:** The exact method to access the underlying Starlette/FastAPI app from a `FastMCP` instance is unconfirmed. Verify at the start of Phase 1 by reading FastMCP source or docs. Fallback (`@mcp.custom_route()` per route) is safe but verbose.
- **`DeliveryHub.check()` performance baseline:** The existing O(conversations) scan concern is documented in `CONCERNS.md` but no benchmark exists. Run a benchmark against a seeded DB (50–200 conversations) before Phase 6 to decide if a schema migration is required.
- **htmx SRI hashes:** The STACK research notes that CDN integrity hashes should be re-verified at implementation time (jsDelivr may rotate them). Do this before committing the base template.

## Sources

### Primary (HIGH confidence)
- [FastAPI official docs — Templates](https://fastapi.tiangolo.com/advanced/templates/) — Jinja2Templates API, StaticFiles mount
- [FastAPI official docs — Server-Sent Events](https://fastapi.tiangolo.com/tutorial/server-sent-events/) — EventSourceResponse, 0.135.0+ native SSE
- [FastAPI official docs — Static Files](https://fastapi.tiangolo.com/tutorial/static-files/) — StaticFiles mount pattern
- [FastAPI release notes](https://fastapi.tiangolo.com/release-notes/) — 0.135.0 SSE addition confirmed
- [Starlette official docs — Middleware](https://www.starlette.io/middleware/) — SessionMiddleware, itsdangerous
- [htmx SSE extension docs](https://htmx.org/extensions/sse/) — hx-ext="sse", sse-connect, sse-swap, version 2.2.4
- [MDN — Using server-sent events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events) — browser 6-connection limit
- Existing Bifrost codebase: `src/bifrost/app.py`, `src/bifrost/hub/`, `src/bifrost/store/`, `src/bifrost/dashboard/` — ground truth for integration points
- Internal: `.planning/codebase/CONCERNS.md` — DeliveryHub O(conversations) concern, sync SQLite concern

### Secondary (MEDIUM confidence)
- [Authlib Starlette OAuth docs](https://docs.authlib.org/en/latest/client/starlette.html) — authorize_redirect pattern (alternative to manual OIDC approach)
- [fastapi-sse-htmx example](https://github.com/vlcinsky/fastapi-sse-htmx) — working SSE + htmx reference implementation
- [HTMX + FastAPI patterns (TestDriven.io)](https://testdriven.io/blog/fastapi-htmx/) — partial templates, polling patterns
- [htmx CSRF / web security basics](https://htmx.org/essays/web-security-basics-with-htmx/) — CSRF with hx-headers
- [Building Real-Time Dashboards with FastAPI and HTMX](https://medium.com/codex/building-real-time-dashboards-with-fastapi-and-htmx-01ea458673cb) — community patterns verified against official docs
- [Starlette route order issue](https://siliconandsoul.substack.com/p/the-case-of-the-swallowed-route-in) — static files mount order warning

### Tertiary (LOW confidence)
- [FastAPI HTMX Patterns 2025](https://johal.in/htmx-fastapi-patterns-hypermedia-driven-single-page-applications-2025/) — blog post, patterns cross-checked against official sources
- [SSE production-readiness issues](https://dev.to/miketalbot/server-sent-events-are-still-not-production-ready-after-a-decade-a-lesson-for-me-a-warning-for-you-2gie) — connection limit and reconnect caveats

---
*Research completed: 2026-03-30*
*Ready for roadmap: yes (with caveat: validate feature scope against planning doc before phase finalization)*
