# Roadmap: Bifrost Dashboard + SSE Push

## Overview

The Bifrost core MCP server is complete and deployed. This milestone layers an interactive web dashboard and real-time SSE push on top of the existing hub and store — without replacing any core logic. The journey: read-only observability first, then human participation, then real-time push, then production auth gate.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [ ] **Phase 1: Foundation + Read-Only Dashboard** - Schema migration, dashboard skeleton, all read-only views with htmx polling
- [ ] **Phase 2: Human Operator + Interactive Actions** - Human operator agent identity complete, POST forms for messages and tasks
- [ ] **Phase 3: Real-Time Push (SSE)** - SSE broadcaster infrastructure, live activity feed, agent push delivery
- [ ] **Phase 4: Dashboard Auth** - OIDC session gate protecting all dashboard routes in production

## Phase Details

### Phase 1: Foundation + Read-Only Dashboard
**Goal**: Users can observe the agent ecosystem through a working dashboard with live-updating views
**Depends on**: Nothing (first phase)
**Requirements**: DASH-01, DASH-02, DASH-03, DASH-04, DASH-05, HUMAN-01, HUMAN-02
**Success Criteria** (what must be TRUE):
  1. User can navigate to /agents and see agent directory with name, status, last seen, and skills
  2. User can view task board showing all tasks with status, assignee, requester, and filter by status
  3. User can view a conversation and read its message history
  4. User can view activity feed showing recent events across all conversations
  5. All dashboard views refresh their data via htmx partials without a full page reload
**Plans:** 3 plans
Plans:
- [ ] 01-01-PLAN.md — Model + schema migration, dashboard route infrastructure, base template
- [ ] 01-02-PLAN.md — Agent directory (card grid) and task board (filtered list with badges)
- [ ] 01-03-PLAN.md — Conversation viewer (two-panel) and activity feed
**UI hint**: yes

### Phase 2: Human Operator + Interactive Actions
**Goal**: Users can participate in the agent ecosystem by sending messages and managing tasks from the dashboard
**Depends on**: Phase 1
**Requirements**: INTX-01, INTX-02, INTX-03, INTX-04, HUMAN-03
**Success Criteria** (what must be TRUE):
  1. User can send a message from the dashboard and it appears attributed to the human operator agent
  2. User can update task status (e.g., cancel, reject) from the task board without leaving the page
  3. Message compose and task actions submit via htmx inline partials with no full page reload
  4. Human operator agent appears in agent listings and its messages are visually distinguishable
**Plans**: TBD
**UI hint**: yes

### Phase 3: Real-Time Push (SSE)
**Goal**: Dashboard activity feed updates in real time and MCP agents receive events via push instead of polling
**Depends on**: Phase 2
**Requirements**: SSE-01, SSE-02, SSE-03, SSE-04
**Success Criteria** (what must be TRUE):
  1. Activity feed in the dashboard updates live when new messages or task transitions occur, without polling
  2. A single multiplexed SSE endpoint delivers all event types to the dashboard page
  3. When a browser tab closes, the SSE connection is cleaned up with no coroutine or queue leak
  4. MCP agents can subscribe to an SSE delivery channel and receive events without calling bifrost_check
**Plans**: TBD

### Phase 4: Dashboard Auth
**Goal**: Dashboard routes are protected by OIDC in production and safely bypassable for local development
**Depends on**: Phase 3
**Requirements**: AUTH-01, AUTH-02, AUTH-03
**Success Criteria** (what must be TRUE):
  1. Visiting any dashboard route without a valid session redirects to the OIDC login flow
  2. Running with --insecure bypasses dashboard auth entirely for local development
  3. An expired session on an htmx partial request receives HX-Redirect instead of injecting the login page into the partial target
**Plans**: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 1 → 2 → 3 → 4

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundation + Read-Only Dashboard | 0/3 | Planning complete | - |
| 2. Human Operator + Interactive Actions | 0/? | Not started | - |
| 3. Real-Time Push (SSE) | 0/? | Not started | - |
| 4. Dashboard Auth | 0/? | Not started | - |
