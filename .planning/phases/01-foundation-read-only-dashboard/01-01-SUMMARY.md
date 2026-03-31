---
phase: 01-foundation-read-only-dashboard
plan: 01
subsystem: ui
tags: [jinja2, htmx, pico-css, dashboard, sqlite-migration]

# Dependency graph
requires: []
provides:
  - Agent.is_human boolean field with SQLite schema migration
  - Dashboard route infrastructure (setup_dashboard_routes pattern)
  - Base template with Pico CSS 2.1.1 + htmx 2.0.8 CDN, sidebar nav
  - Human operator lazy creation on first dashboard visit
  - Page routes for /agents, /tasks, /conversations, /activity
  - Partial routes for htmx-loaded content fragments
affects: [01-02, 01-03, 02-interactive-dashboard]

# Tech tracking
tech-stack:
  added: [pico-css-2.1.1-cdn, htmx-2.0.8-cdn, starlette-jinja2-templates]
  patterns: [setup_dashboard_routes(mcp, store), mcp.custom_route for pages, htmx partial loading]

key-files:
  created:
    - src/bifrost/dashboard/routes.py
    - src/bifrost/dashboard/templates/base.html
    - src/bifrost/dashboard/templates/agents.html
    - src/bifrost/dashboard/templates/tasks.html
    - src/bifrost/dashboard/templates/conversations.html
    - src/bifrost/dashboard/templates/activity.html
    - src/bifrost/dashboard/templates/partials/agent_list.html
    - src/bifrost/dashboard/templates/partials/task_list.html
    - src/bifrost/dashboard/templates/partials/conversation_list.html
    - src/bifrost/dashboard/templates/partials/conversation_detail.html
    - src/bifrost/dashboard/templates/partials/activity_feed.html
    - tests/test_dashboard.py
  modified:
    - src/bifrost/store/models.py
    - src/bifrost/store/db.py
    - src/bifrost/app.py

key-decisions:
  - "TestClient (sync) for dashboard tests instead of async httpx -- simpler for HTML route testing"
  - "fmt_event filter signature (data, event_type) to match Jinja2 pipe convention"

patterns-established:
  - "Route registration: setup_dashboard_routes(mcp, store) called in create_app()"
  - "Template pattern: page templates extend base.html, partials loaded via htmx"
  - "Human operator: lazy-created on first /agents page visit via _ensure_human_operator()"

requirements-completed: [HUMAN-01, DASH-05]

# Metrics
duration: 4min
completed: 2026-03-31
---

# Phase 01 Plan 01: Foundation Dashboard Infrastructure Summary

**Agent is_human field with SQLite migration, Jinja2+htmx dashboard skeleton with Pico CSS, sidebar nav, and 8 route smoke tests**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-31T12:16:14Z
- **Completed:** 2026-03-31T12:20:01Z
- **Tasks:** 2
- **Files modified:** 15

## Accomplishments
- Added is_human boolean field to Agent model with ALTER TABLE migration for existing databases
- Created full dashboard route infrastructure with 10 routes (4 pages, 5 partials, 1 redirect)
- Base template with Pico CSS classless + htmx CDN, sidebar nav with 4 links and active state
- Human operator agent created lazily on first /agents visit
- 8 dashboard smoke tests all passing, 170 total tests passing

## Task Commits

Each task was committed atomically:

1. **Task 1: Add is_human to Agent model and Store persistence** - `87adf6d` (feat)
2. **Task 2: Create dashboard routes, base template, and wire into create_app** - `2850079` (feat)

## Files Created/Modified
- `src/bifrost/store/models.py` - Added is_human: bool = False to Agent dataclass
- `src/bifrost/store/db.py` - Schema migration, upsert/read for is_human column
- `src/bifrost/dashboard/routes.py` - setup_dashboard_routes() with all page and partial routes
- `src/bifrost/dashboard/templates/base.html` - Page shell with Pico CSS, htmx, sidebar nav
- `src/bifrost/dashboard/templates/agents.html` - Agent Directory page with htmx auto-refresh
- `src/bifrost/dashboard/templates/tasks.html` - Task Board page
- `src/bifrost/dashboard/templates/conversations.html` - Conversations page with split layout
- `src/bifrost/dashboard/templates/activity.html` - Activity Feed page
- `src/bifrost/dashboard/templates/partials/*.html` - 5 partial templates for htmx content
- `src/bifrost/app.py` - Wired setup_dashboard_routes into create_app()
- `tests/test_dashboard.py` - 8 smoke tests for all routes

## Decisions Made
- Used sync TestClient for dashboard tests instead of async httpx pattern used in MCP tests -- simpler for plain HTML route testing
- Fixed fmt_event filter signature to (data, event_type) to match Jinja2 pipe calling convention where the piped value is the first argument

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed _fmt_event parameter order for Jinja2 filter convention**
- **Found during:** Task 2 (template creation)
- **Issue:** Plan had _fmt_event(event_type, data) but Jinja2 filter pipes data as first arg
- **Fix:** Swapped parameter order to _fmt_event(data, event_type) to match `e.data|fmt_event(e.type)` usage
- **Files modified:** src/bifrost/dashboard/routes.py
- **Verification:** Templates render correctly, tests pass
- **Committed in:** 2850079 (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 bug fix)
**Impact on plan:** Minor parameter reordering for correctness. No scope creep.

## Issues Encountered
None.

## Known Stubs
- `src/bifrost/dashboard/templates/partials/task_list.html` - Functional but will be enhanced in plan 02
- `src/bifrost/dashboard/templates/partials/conversation_list.html` - Functional but will be enhanced in plan 03
- `src/bifrost/dashboard/templates/partials/conversation_detail.html` - Functional but will be enhanced in plan 03
- `src/bifrost/dashboard/templates/partials/activity_feed.html` - Functional but will be enhanced in plan 03

These are intentional per the plan -- plans 02 and 03 will fully implement the partial templates with real data rendering.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Dashboard infrastructure is ready for plans 02 (agent cards, task board) and 03 (conversations, activity)
- Route registration pattern established -- future routes follow same setup_dashboard_routes pattern
- Base template and sidebar nav ready for all dashboard pages

## Self-Check: PASSED

All 15 created/modified files verified present. Both commit hashes (87adf6d, 2850079) confirmed in git log. 170 tests passing.

---
*Phase: 01-foundation-read-only-dashboard*
*Completed: 2026-03-31*
