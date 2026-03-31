# Phase 1: Foundation + Read-Only Dashboard - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-03-31
**Phase:** 01-foundation-read-only-dashboard
**Areas discussed:** Dashboard layout & navigation, Visual style & CSS, Agent & task presentation, Human operator identity

---

## Dashboard Layout & Navigation

| Option | Description | Selected |
|--------|-------------|----------|
| Multi-page with top nav bar | Each view is its own page, nav bar across the top. Classic admin layout. Matches earlier plan. | |
| Multi-page with sidebar nav | Same pages but sidebar navigation. More room for future sections, feels more like an admin panel. | ✓ |
| Single page with htmx tab switching | One shell, swap content via htmx. Snappier but more complex templating. | |

**User's choice:** Multi-page with sidebar nav
**Notes:** None — straightforward selection.

---

## Visual Style & CSS Approach

| Option | Description | Selected |
|--------|-------------|----------|
| Pico CSS | Classless/minimal-class framework, clean defaults. CDN link, semantic HTML gets you 80% there. Fastest to ship. | ✓ |
| Simple.css | Similar classless approach, slightly more opinionated/modern look. CDN link. | |
| Hand-rolled minimal CSS | Custom stylesheet, full control, no external dependency. More work. | |
| You decide | Claude picks whatever gets clean results fastest. | |

**User's choice:** Pico CSS
**Notes:** None.

### Follow-up: Dark/Light Mode

| Option | Description | Selected |
|--------|-------------|----------|
| Dark | Easier on the eyes for monitoring/ops dashboard | |
| Light | Classic admin look | |
| Auto (system preference) | Follows OS setting, Pico does this by default | ✓ |
| You decide | | |

**User's choice:** Auto (system preference)
**Notes:** Zero config — Pico's default behavior.

---

## Agent & Task Presentation

### Agent Directory

| Option | Description | Selected |
|--------|-------------|----------|
| Card grid | One card per agent, shows name/status/skills at a glance. Visual, scannable. Earlier plan used this. | ✓ |
| Table | Rows with columns. Compact, sortable, better for many agents. | |
| List with expandable details | Compact list, click to expand skills/details. | |
| You decide | | |

**User's choice:** Card grid
**Notes:** None.

### Task Board

| Option | Description | Selected |
|--------|-------------|----------|
| Filtered table | All tasks in a table, filter buttons by status. Swap table body on filter click. | |
| Kanban columns | Columns per status, tasks as cards. Visual status overview but wider layout. | |
| List with status badges | Simple list, colored badges per status, filter dropdown. Middle ground. | ✓ |
| You decide | | |

**User's choice:** List with status badges
**Notes:** None.

### Conversation Viewer

| Option | Description | Selected |
|--------|-------------|----------|
| Two-panel: list + message pane | List of conversations on the left, click one to load messages on the right. Common chat-viewer pattern. | ✓ |
| Conversation list page, click through | Separate pages, simpler templating. | |
| You decide | | |

**User's choice:** Two-panel layout
**Notes:** Good fit with sidebar layout.

### Activity Feed

| Option | Description | Selected |
|--------|-------------|----------|
| Chronological event stream | Timestamped list of events. Like a log view with readable formatting. | ✓ |
| Grouped by conversation/agent | Events clustered by source, then chronological within each group. | |
| You decide | | |

**User's choice:** Chronological event stream
**Notes:** Simple and effective for ops view.

---

## Human Operator Identity

### Creation Timing

| Option | Description | Selected |
|--------|-------------|----------|
| Auto-created on server startup | Always exists, even if no one uses the dashboard. Simple, predictable. | |
| Created on first dashboard visit | Lazy creation, only exists when someone uses the dashboard. | ✓ |

**User's choice:** Created on first dashboard visit
**Notes:** None.

### Visual Distinction

| Option | Description | Selected |
|--------|-------------|----------|
| Different card style + label | Same card grid but with distinct accent/border, plus "Human" badge. Subtle but clear. | |
| Separate section | Human operator shown above/outside the agent card grid, like a "You" indicator. | ✓ |
| Icon/avatar distinction | Same card layout but person icon vs bot icon. Minimal visual difference. | |
| You decide | | |

**User's choice:** Separate section above agent grid
**Notes:** Clear visual separation from regular agents.

---

## Claude's Discretion

- htmx polling intervals
- Template file organization
- Route mounting strategy (include_router vs custom_route)
- Pico CSS version and SRI hash

## Deferred Ideas

None — discussion stayed within phase scope.
