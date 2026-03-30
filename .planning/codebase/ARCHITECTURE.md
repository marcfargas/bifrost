# Architecture

**Analysis Date:** 2026-03-30

## Pattern Overview

**Overall:** Layered service architecture — MCP transport -> Tool handlers -> Hub business logic -> SQLite persistence

**Key Characteristics:**
- Single-process Python server exposing 11 MCP tools over Streamable HTTP
- A2A-inspired domain model (agents, tasks, conversations, events)
- Synchronous SQLite store (WAL mode) accessed by synchronous hub classes, wrapped in async MCP tool handlers
- Session-to-agent binding via in-memory dict (not persisted across restarts)
- Optional OAuth AS that proxies to an external OIDC provider (e.g., Dex)

## Layers

**Transport Layer (FastMCP / Starlette):**
- Purpose: HTTP transport, MCP protocol handling, OAuth middleware
- Location: `src/bifrost/app.py` (app factory), FastMCP framework
- Contains: Tool registration, session management, custom routes (`/health`, `/callback`)
- Depends on: `mcp` SDK (`FastMCP`), Starlette
- Used by: External MCP clients (Claude Code, other agents)

**Tool Handlers Layer:**
- Purpose: Thin wrappers that translate MCP tool calls into hub method calls
- Location: `src/bifrost/mcp/tools.py`
- Contains: `ToolHandlers` class with one method per MCP tool (11 methods)
- Depends on: All four hub classes
- Used by: `src/bifrost/app.py` tool registrations

**Hub Layer (Business Logic):**
- Purpose: Domain logic, validation, state transitions
- Location: `src/bifrost/hub/`
- Contains: Four hub classes, each owning a domain area
- Depends on: `Store`, model dataclasses
- Used by: `ToolHandlers`

**Persistence Layer:**
- Purpose: SQLite CRUD operations, schema management, JSON serialization
- Location: `src/bifrost/store/db.py`
- Contains: `Store` class with methods per entity type
- Depends on: `sqlite3`, model dataclasses
- Used by: All hub classes and `DexOAuthProvider`

**Model Layer:**
- Purpose: Domain data structures (pure dataclasses, no behavior)
- Location: `src/bifrost/store/models.py`
- Contains: Dataclasses (`Agent`, `Task`, `Conversation`, `Event`, `AgentCard`, etc.), enums (`TaskStatus`, `AgentStatus`, `EventType`), content types (`TextPart`, `FilePart`, `DataPart`, `Artifact`)
- Depends on: Nothing (leaf module)
- Used by: Every other layer

**Auth Layer:**
- Purpose: OAuth Authorization Server proxying to external OIDC
- Location: `src/bifrost/auth/oauth.py`
- Contains: `DexOAuthProvider` implementing the MCP SDK's auth provider protocol
- Depends on: `Store` (for token/client persistence), `httpx` (for OIDC token exchange)
- Used by: `src/bifrost/app.py` (conditionally, only when `--insecure` is not set)

## Data Flow

**Agent Connection and Introduction:**

1. MCP client connects to `/mcp` (Streamable HTTP)
2. If OAuth enabled: `list_tools` hook auto-registers unnamed placeholder agent via `AgentHub.register()`
3. Client calls `bifrost_introduce` with name and description
4. `_introduce_agent()` in `app.py` calls `AgentHub.register()` which upserts agent in SQLite
5. Session key (from access token or "insecure") is mapped to `agent_id` in `session_agents` dict
6. `ToolHandlers.handle_introduce()` updates the agent card via `AgentHub.update_card()`

**Message Sending (bifrost_send):**

1. `app.py` resolves caller's `agent_id` from session via `_get_session_agent()`
2. `ToolHandlers.handle_send()` resolves target agent name to ID via `AgentHub.resolve()`
3. `ConversationHub.send()` either reuses existing conversation or creates new one (DM or channel)
4. An `Event` (type=message) is appended to the conversation in SQLite
5. Returns conversation ID and event ID

**Message Delivery (bifrost_check — polling):**

1. `DeliveryHub.check()` iterates all conversations where agent is a participant
2. Also checks channel subscriptions (target format: `channel:<name>`)
3. For each conversation, fetches events after `last_event_id` from `delivery_state` table
4. Filters out agent's own messages
5. Advances `delivery_state` to the latest delivered event per conversation
6. Returns sorted list of undelivered events

**Task Lifecycle:**

1. Requester calls `bifrost_request_task` with assignee name
2. `TaskHub.create()` creates task in `QUEUED` status
3. Assignee discovers task via `bifrost_check` or `bifrost_list_tasks`
4. Assignee calls `bifrost_update_task` to transition status
5. `TaskHub.update_status()` validates transition against A2A state machine (`_TRANSITIONS` dict)
6. Terminal states: `completed`, `failed`, `canceled`, `rejected`

**State Management:**
- All persistent state lives in SQLite at `data/bifrost.db` (configurable via `--db`)
- Session-to-agent mapping (`session_agents`) is in-memory only — lost on restart
- OAuth pending auth state (`_pending_auths`) is in-memory — ephemeral by design (seconds)
- OAuth clients, tokens, and auth codes are persisted in SQLite

## Key Abstractions

**Hub Classes:**
- Purpose: Each hub owns one domain area and encapsulates its business rules
- Examples: `src/bifrost/hub/agents.py` (`AgentHub`), `src/bifrost/hub/tasks.py` (`TaskHub`), `src/bifrost/hub/conversations.py` (`ConversationHub`), `src/bifrost/hub/delivery.py` (`DeliveryHub`)
- Pattern: Constructor injection of `Store`; methods operate on domain models and delegate persistence

**ToolHandlers:**
- Purpose: Single class mapping MCP tool names to hub method calls
- Examples: `src/bifrost/mcp/tools.py`
- Pattern: Aggregates all four hubs; each handler method is a thin orchestrator (resolve agent, call hub, format response string)

**A2A Task State Machine:**
- Purpose: Enforce valid task status transitions
- Examples: `src/bifrost/hub/tasks.py` (`_TRANSITIONS` dict, `InvalidTransition` exception)
- Pattern: Explicit transition map checked before any status update

**Store (Repository Pattern):**
- Purpose: Single class wrapping all SQLite operations
- Examples: `src/bifrost/store/db.py`
- Pattern: One `Store` instance shared across all hubs; methods like `upsert_agent()`, `create_task()`, `append_event()`; handles JSON serialization of complex fields

## Entry Points

**CLI (`python -m bifrost`):**
- Location: `src/bifrost/__main__.py`
- Triggers: Command-line invocation or `bifrost` console script
- Responsibilities: Parse args, build `Config`, call `create_app()`, run via `mcp.run(transport="streamable-http")`

**App Factory (`create_app`):**
- Location: `src/bifrost/app.py`
- Triggers: Called by CLI
- Responsibilities: Wire up Store -> Hubs -> ToolHandlers -> FastMCP; register all 11 tools; configure OAuth if enabled; add `/health` and `/callback` routes

**MCP Endpoint (`/mcp`):**
- Location: Handled by FastMCP framework (configured in `app.py` via `streamable_http_path="/mcp"`)
- Triggers: MCP client connections (Streamable HTTP)
- Responsibilities: MCP protocol handling, tool dispatch

## Error Handling

**Strategy:** Exceptions bubble up to tool handlers in `app.py`, caught and returned as error strings

**Patterns:**
- Hub classes raise `KeyError` for not-found entities (e.g., `Agent not found: {id}`)
- Hub classes raise `ValueError` for invalid operations (e.g., duplicate agent names)
- `TaskHub` raises `InvalidTransition` (custom exception) for illegal state changes
- Tool wrappers in `app.py` catch `(ValueError, KeyError)` and return `f"Error: {e}"`
- All errors are returned as plain text strings to the MCP client — no structured error codes

## Cross-Cutting Concerns

**Logging:** Minimal — one `logging.getLogger("bifrost").warning()` call in auto-registration fallback. No structured logging framework.

**Validation:** Enum-based validation for `AgentStatus` and `TaskStatus` (invalid values raise `ValueError`). Task state machine validates transitions. Agent name uniqueness enforced by `AgentHub.rename()`.

**Authentication:** Two modes controlled by `--insecure` flag:
- Insecure: No auth, all sessions share a single key (`"insecure"`), only one agent active at a time
- OAuth: `DexOAuthProvider` in `src/bifrost/auth/oauth.py` acts as Authorization Server, proxying to external OIDC. Tokens persisted in SQLite. Session key derived from `access_token.token`.

**ID Generation:** 12-character hex strings from `uuid4` (`_new_id()` in `src/bifrost/store/models.py`).

**Timestamps:** UTC ISO 8601 strings via `_now()` in `src/bifrost/store/models.py`.

---

*Architecture analysis: 2026-03-30*
