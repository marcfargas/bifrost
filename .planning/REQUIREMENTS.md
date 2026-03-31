# Requirements: Bifrost

**Defined:** 2026-03-31
**Core Value:** Agents can find each other and exchange messages and tasks through a single shared hub

## v1 Requirements

Requirements for this milestone. Each maps to roadmap phases.

### Dashboard Views

- [ ] **DASH-01**: User can view agent directory with cards, status, last seen, and skills
- [ ] **DASH-02**: User can view task board with status, assignee, requester, and filtering
- [ ] **DASH-03**: User can view conversation message history
- [ ] **DASH-04**: User can view activity feed of recent events across all conversations
- [x] **DASH-05**: Dashboard pages update via htmx partials without full page reload

### Dashboard Interactivity

- [ ] **INTX-01**: User can send messages from the dashboard as the human operator agent
- [ ] **INTX-02**: User can update task status from the task board view
- [ ] **INTX-03**: User can cancel tasks from the task board view
- [ ] **INTX-04**: Message compose uses inline htmx partial (no page reload)

### Human Operator

- [x] **HUMAN-01**: A virtual "human" agent exists with an `is_human` flag on the Agent model
- [ ] **HUMAN-02**: Human operator is distinguishable from regular agents in both UI and API responses
- [ ] **HUMAN-03**: Messages sent from dashboard are attributed to the human operator agent

### Real-Time Push (SSE)

- [ ] **SSE-01**: Dashboard activity feed updates in real-time via SSE without page refresh
- [ ] **SSE-02**: Single multiplexed SSE endpoint serves multiple widget updates
- [ ] **SSE-03**: Per-client asyncio.Queue broadcaster pushes events from hub write paths
- [ ] **SSE-04**: Agent SSE delivery channel replaces bifrost_check polling for connected agents

### Dashboard Auth

- [ ] **AUTH-01**: Dashboard routes are protected by OAuth using the same OIDC provider as agents
- [ ] **AUTH-02**: `--insecure` mode bypasses dashboard auth for local development
- [ ] **AUTH-03**: Expired sessions on htmx partials return `HX-Redirect` instead of injecting login page

## v2 Requirements

Deferred to future milestones. Tracked but not in current roadmap.

### Production Hardening

- **PROD-01**: aiosqlite or connection pooling for async safety
- **PROD-02**: Pagination on all list queries
- **PROD-03**: Task ownership enforcement
- **PROD-04**: Session cleanup / TTL cache
- **PROD-05**: Rate limiting

### A2A HTTP Layer

- **A2A-01**: `.well-known/agent.json` per registered agent
- **A2A-02**: A2A JSON-RPC endpoints
- **A2A-03**: Bridge between MCP clients and A2A clients

## Out of Scope

| Feature | Reason |
|---------|--------|
| Task creation from dashboard | Tasks are created by messaging agents — keeps agent protocol as single task creation path |
| Mobile app / standalone SPA | Jinja2 + htmx is the UI approach, no JS framework |
| Real-time chat UI | Dashboard is for observability and operator participation, not a chat app |
| Agent disconnect detection | Production hardening, deferred |
| OAuth test coverage | Accepted limitation for MVP |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| DASH-01 | Phase 1 | Pending |
| DASH-02 | Phase 1 | Pending |
| DASH-03 | Phase 1 | Pending |
| DASH-04 | Phase 1 | Pending |
| DASH-05 | Phase 1 | Complete |
| HUMAN-01 | Phase 1 | Complete |
| HUMAN-02 | Phase 1 | Pending |
| INTX-01 | Phase 2 | Pending |
| INTX-02 | Phase 2 | Pending |
| INTX-03 | Phase 2 | Pending |
| INTX-04 | Phase 2 | Pending |
| HUMAN-03 | Phase 2 | Pending |
| SSE-01 | Phase 3 | Pending |
| SSE-02 | Phase 3 | Pending |
| SSE-03 | Phase 3 | Pending |
| SSE-04 | Phase 3 | Pending |
| AUTH-01 | Phase 4 | Pending |
| AUTH-02 | Phase 4 | Pending |
| AUTH-03 | Phase 4 | Pending |

**Coverage:**
- v1 requirements: 19 total
- Mapped to phases: 19
- Unmapped: 0

---
*Requirements defined: 2026-03-31*
*Last updated: 2026-03-30 after roadmap creation — all 19 requirements mapped*
