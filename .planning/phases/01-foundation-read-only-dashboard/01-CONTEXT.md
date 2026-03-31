# Phase 1: Foundation + Read-Only Dashboard - Context

**Gathered:** 2026-03-31
**Status:** Ready for planning

<domain>
## Phase Boundary

Deliver a read-only web dashboard for observing the Bifrost agent ecosystem. Users can view agent directory, task board, conversation message history, and activity feed — all updating via htmx partials without full page reload. Also adds the human operator agent identity (`is_human` flag) with visual distinction, but no interactive actions yet (that's Phase 2).

</domain>

<decisions>
## Implementation Decisions

### Dashboard Layout & Navigation
- **D-01:** Multi-page layout with sidebar navigation. Each view (agents, tasks, conversations, activity) is its own page. Sidebar provides navigation between views.

### Visual Style & CSS
- **D-02:** Pico CSS via CDN for styling. Classless/minimal-class framework, semantic HTML approach.
- **D-03:** Auto dark/light mode following system preference (Pico's default behavior, no explicit `data-theme` needed).

### Agent Directory (DASH-01)
- **D-04:** Card grid layout. One card per agent showing name, status, last seen, and skills at a glance.

### Task Board (DASH-02)
- **D-05:** List with colored status badges. Filter buttons across the top by status (All / Queued / Running / Completed / Failed etc.). Swap list via htmx on filter click.

### Conversation Viewer (DASH-03)
- **D-06:** Two-panel layout within the conversation page. Conversation list on the left, message history pane on the right. Click a conversation to load its messages via htmx.

### Activity Feed (DASH-04)
- **D-07:** Chronological event stream. Timestamped list of events (messages sent, tasks created/updated, agents connected) across all conversations. Simple log-style view.

### Human Operator Identity (HUMAN-01, HUMAN-02)
- **D-08:** Human operator agent created lazily on first dashboard visit, not on server startup.
- **D-09:** Human operator displayed in a separate section above the agent card grid in the directory. Clear visual separation from regular agents.

### Claude's Discretion
- htmx polling intervals for auto-refresh (reasonable defaults)
- Template file organization within `src/bifrost/dashboard/templates/`
- Route mounting strategy (verify `include_router` or fall back to `@mcp.custom_route()`)
- Exact Pico CSS version and SRI hash

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Earlier Dashboard Design
- `docs/superpowers/plans/2026-03-30-bifrost-v2-dashboard.md` — Earlier implementation plan with skeleton code, test patterns, and template structure. Some decisions superseded by this context (e.g., sidebar nav instead of top nav).
- `docs/superpowers/specs/2026-03-30-bifrost-v2-design.md` — Full v2 design spec including data model, A2A schemas, and dashboard section.

### Project Planning
- `.planning/REQUIREMENTS.md` — Phase 1 requirements: DASH-01 through DASH-05, HUMAN-01, HUMAN-02
- `.planning/ROADMAP.md` — Phase details and success criteria

### Codebase Analysis
- `.planning/codebase/STRUCTURE.md` — File layout, where to add new code
- `.planning/codebase/STACK.md` — Technology stack, dependencies
- `.planning/codebase/INTEGRATIONS.md` — External integrations, auth flow, HTTP endpoints

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `src/bifrost/dashboard/` — Empty placeholder package, ready for dashboard code
- `jinja2 >= 3.1.0` — Already a dependency in `pyproject.toml`
- `@mcp.custom_route()` — Available for mounting non-MCP HTTP routes
- Store layer (`src/bifrost/store/db.py`) — All CRUD methods for agents, tasks, conversations, events already exist

### Established Patterns
- Layered architecture: Store -> Hub -> Tool handlers -> App wiring
- All hub classes take `Store` via constructor injection
- Custom routes use Starlette `Request`/`Response` types directly
- Synchronous SQLite store, async route handlers

### Integration Points
- `src/bifrost/app.py` `create_app()` — Dashboard routes need to be mounted here
- `src/bifrost/store/models.py` — `Agent` dataclass needs `is_human` field addition
- `src/bifrost/store/db.py` — Schema migration for `is_human` column, `_row_to_agent()` update
- `/health` route already exists as pattern for custom route registration

</code_context>

<specifics>
## Specific Ideas

No specific external references or "I want it like X" moments — open to standard approaches within the decisions above.

</specifics>

<deferred>
## Deferred Ideas

None — discussion stayed within phase scope.

</deferred>

---

*Phase: 01-foundation-read-only-dashboard*
*Context gathered: 2026-03-31*
