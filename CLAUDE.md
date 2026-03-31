# Bifrost

A2A-inspired communication hub for AI agents, served as a remote MCP server (Python).

## Running

```bash
# Install dependencies
pip install -e ".[dev]"

# Run tests
pytest tests/

# Run the server (local dev, no auth)
python -m bifrost --insecure

# Run the server (with OAuth)
python -m bifrost --oidc-issuer <url> --oidc-client-id <id> --oidc-client-secret <secret> --server-url <url>
```

## Project structure

```
src/bifrost/
  __main__.py          CLI entry point (argparse)
  app.py               FastMCP app factory, tool registration
  config.py            Config dataclass
  auth/oauth.py        OAuth AS proxying to external OIDC
  hub/
    agents.py          Agent registry, card management
    tasks.py           Task lifecycle with A2A states
    conversations.py   Messaging, channels
    delivery.py        Delivery tracking (bifrost_check)
  mcp/tools.py         11 MCP tool handlers
  store/
    models.py          A2A-inspired dataclasses (Agent, Task, Conversation, Event, etc.)
    db.py              SQLite persistence
tests/                 pytest tests (asyncio_mode = auto)
docs/                  Historical design specs and plans
```

## Conventions

- Python 3.12+, dataclasses (no Pydantic)
- pytest with pytest-asyncio (auto mode)
- Conventional commits: feat, fix, docs, test, refactor, chore
- Default branch: `develop` (main is release-only)
- License: LGPL-3.0-or-later

## Key concepts

- 11 MCP tools exposed via FastMCP (Streamable HTTP transport)
- Agents connect, introduce themselves, then communicate via messages and tasks
- Tasks follow A2A-inspired lifecycle: queued -> running -> completed/failed/canceled/rejected
- SQLite persistence at `data/bifrost.db`
- `--insecure` mode for local dev (no OAuth), OIDC proxy for production

<!-- GSD:project-start source:PROJECT.md -->
## Project

**Bifrost**

A communication hub for AI agents, served as a remote MCP server. Agents connect via MCP (Streamable HTTP), introduce themselves, then communicate through messages, tasks, and channels. Built in Python with FastMCP, SQLite persistence, and OAuth via external OIDC. Deployed at bifrost.blegal.dev.

**Core Value:** Agents can find each other and exchange messages and tasks through a single shared hub — without needing to know about each other's transport or implementation.

### Constraints

- **Tech stack**: Python 3.12+, FastMCP, Jinja2 + htmx, SQLite — no frontend build toolchain
- **Deployment**: Same FastAPI app, same Docker container, same OAuth
- **Auth**: Reuse existing OIDC proxy for dashboard access
- **Dependencies**: Minimal — no JS frameworks, no additional databases
<!-- GSD:project-end -->

<!-- GSD:stack-start source:codebase/STACK.md -->
## Technology Stack

## Languages
- Python 3.12+ - All application code (`src/bifrost/`)
- SQL (SQLite dialect) - Schema and queries embedded in `src/bifrost/store/db.py`
## Runtime
- CPython 3.12+ (required by `pyproject.toml`: `requires-python = ">=3.12"`)
- Docker image uses `python:3.14-slim` (`Dockerfile`)
- pip (standard) for local development: `pip install -e ".[dev]"`
- uv (Astral) for Docker builds: copied from `ghcr.io/astral-sh/uv:latest` in `Dockerfile`
- No lockfile (no `requirements.txt`, no `uv.lock`) - dependencies resolved at install time
## Frameworks
- FastMCP (from `mcp` package) >= 1.9.0 - MCP server framework, provides tool registration, Streamable HTTP transport, and OAuth middleware
- FastAPI >= 0.115.0 - Underlying ASGI framework (used by FastMCP internally; custom routes via `mcp.custom_route()`)
- Starlette - Request/Response types used directly (`starlette.requests.Request`, `starlette.responses.JSONResponse`)
- pytest >= 8.0 - Test runner
- pytest-asyncio >= 0.25.0 - Async test support (configured with `asyncio_mode = "auto"`)
- Hatchling - PEP 517 build backend (`pyproject.toml` `[build-system]`)
## Key Dependencies
- `mcp` >= 1.9.0 - Core MCP protocol implementation (FastMCP server, auth provider types, OAuth middleware). This is THE framework dependency. Provides `mcp.server.fastmcp.FastMCP`, `mcp.server.auth.provider.*`, `mcp.server.auth.settings.*`, `mcp.server.auth.middleware.auth_context.*`, `mcp.shared.auth.*`.
- `fastapi` >= 0.115.0 - ASGI web framework (FastMCP builds on top of it)
- `uvicorn[standard]` >= 0.34.0 - ASGI server for HTTP transport
- `httpx` >= 0.28.0 - Async HTTP client for OIDC token exchange (`src/bifrost/auth/oauth.py`)
- `jinja2` >= 3.1.0 - Template engine (likely for FastMCP internals or future dashboard)
- `sqlite3` (stdlib) - Persistence layer, no external DB driver needed
- `pydantic` (transitive via `mcp`) - Used in OAuth provider for `OAuthClientInformationFull.model_validate_json()` / `model_dump_json()`
- `asgi-lifespan` >= 2.1.0 - Test helper for ASGI app lifecycle in tests
## Configuration
- CLI args with env var fallbacks for OIDC settings (`src/bifrost/__main__.py`)
- Key env vars: `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `SERVER_URL`
- `.env.example` file present for reference
- `--insecure` flag bypasses all auth for local dev
- `pyproject.toml` - Single source of truth for project metadata, dependencies, build config, and pytest settings
- No separate `setup.py`, `setup.cfg`, or `requirements.txt`
- `Dockerfile` - Single-stage build, `python:3.14-slim` base, uv for fast installs
- `docker-compose.yml` - Production config with volume for `data/`
- `docker-compose.dev.yml` - Overlay for Traefik reverse proxy deployment on `bifrost.blegal.dev`
## Platform Requirements
- Python 3.12+
- `pip install -e ".[dev]"` for editable install with test dependencies
- SQLite3 (ships with Python stdlib)
- No external services required in `--insecure` mode
- Docker (preferred) or Python 3.12+ with pip/uv
- External OIDC provider (e.g., Dex) for authentication
- Persistent volume for SQLite database at `data/bifrost.db`
- Reverse proxy (Traefik) for TLS termination in deployed environments
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->
## Conventions

## Naming Patterns
- Use `snake_case.py` for all Python modules
- Hub modules named by domain: `agents.py`, `tasks.py`, `conversations.py`, `delivery.py`
- Test files prefixed with `test_`: `test_hub_agents.py`, `test_db.py`
- Single-purpose modules: one class per hub file, one config dataclass per `config.py`
- Use `snake_case` for all functions and methods
- Public hub methods: verb-based — `register()`, `disconnect()`, `update_status()`, `create()`, `get()`, `list()`
- Private helpers prefixed with underscore: `_get_session_key()`, `_introduce_agent()`, `_get_session_agent()`, `_get_undelivered()`
- Store methods follow CRUD naming: `upsert_agent()`, `get_agent()`, `list_agents()`, `create_task()`, `update_task()`
- Row conversion methods: `_row_to_agent()`, `_row_to_task()`, `_row_to_conversation()`, `_row_to_event()`
- Tool handler methods: `handle_` prefix — `handle_introduce()`, `handle_whoami()`, `handle_send()`
- Use `snake_case` for all variables
- Hub instances: descriptive nouns — `agents`, `tasks`, `conversations`, `delivery`, `handlers`
- Internal state stored with underscore prefix: `self._store`, `self._agents`
- SQL parameter lists: `clauses`, `params`, `sets`, `vals`
- `PascalCase` for all classes
- Hub classes suffixed with `Hub`: `AgentHub`, `TaskHub`, `ConversationHub`, `DeliveryHub`
- Model classes are bare nouns: `Agent`, `Task`, `Conversation`, `Event`
- Enums use `StrEnum` with UPPERCASE members: `AgentStatus.ONLINE`, `TaskStatus.QUEUED`
- Custom exceptions: descriptive `PascalCase` — `InvalidTransition`
- Module-level constants: `UPPERCASE` — `MCP_INSTRUCTIONS`, `_SCHEMA`
- Private module constants: underscore prefix — `_TRANSITIONS`
## Code Style
- No formatter configured (no ruff, black, flake8, or pylint config detected)
- Consistent 4-space indentation throughout
- Line lengths generally under 100, occasional longer lines for SQL and string formatting
- Trailing commas used in multi-line function calls and data structures
- All modules start with `from __future__ import annotations` for PEP 604 union syntax
- Import order:
- Blank line separates each import group
- Prefer `from X import Y` over bare `import X`
- Some lazy imports inside functions for optional/conditional dependencies (e.g., `from mcp.server.auth.middleware.auth_context import get_access_token` inside `_get_session_agent()`)
- Full type annotations on all function signatures (parameters and return types)
- Use modern union syntax: `str | None` instead of `Optional[str]`
- Use `list[str]`, `dict[str, Any]`, `set[TaskStatus]` (lowercase generics)
- `Any` used sparingly, primarily for JSON-like data: `dict[str, Any]`
- Return type always specified: `-> None`, `-> str`, `-> Agent`, `-> list[Task]`
- `@staticmethod` used for pure conversion functions in `Store` class
- All models use `@dataclass` (no Pydantic)
- Default factory for mutable fields: `field(default_factory=list)`, `field(default_factory=dict)`
- ID generation via `field(default_factory=_new_id)` — 12-char hex from `uuid4`
- Timestamps via `field(default_factory=_now)` — UTC ISO format strings
## Error Handling
- Hub methods raise `KeyError` for "not found" cases: `raise KeyError(f"Agent not found: {agent_id}")`
- Hub methods raise `ValueError` for invalid input: `raise ValueError(f"Name '{new_name}' is already taken")`
- Custom exceptions for domain-specific errors: `InvalidTransition` in `src/bifrost/hub/tasks.py`
- Tool handlers in `src/bifrost/app.py` catch `(ValueError, KeyError)` and return error strings: `return f"Error: {e}"`
- Store methods raise `ValueError` for disallowed field updates: `raise ValueError(f"Cannot update field: {k}")`
- No bare `except:` clauses — all catches are specific exception types
- OAuth callback in `src/bifrost/app.py` catches `ValueError` and generic `Exception` separately, returning appropriate HTTP status codes
- MCP tool functions return error strings (not exceptions) to the LLM caller
- Internal hub/store methods raise exceptions for callers to handle
- Two-layer pattern: Hub raises -> App catches -> returns string to MCP client
## Documentation
- Every module has a one-line docstring: `"""Bifrost FastMCP application factory."""`
- Format: `"""Description."""` (triple-quoted, single line, period-terminated)
- Short single-line docstrings: `"""Manages agent registration, status, and cards."""`
- Not all classes have docstrings (e.g., model dataclasses lack them)
- Hub public methods have one-line docstrings: `"""Register or reconnect an agent. Sets status to ONLINE."""`
- Multi-line docstrings used for complex methods with detailed behavior explanation (see `ConversationHub.send()` and `AgentHub.update_card()`)
- MCP tool functions use docstrings as tool descriptions (consumed by LLM), include `Args:` sections
- Section separators use `# ---` comment blocks: `# ------------------------------------------------------------------`
- Inline comments explain "why" not "what": `# Ensure sender is a participant`, `# Terminal states`
- Numbered sections in `src/bifrost/mcp/tools.py`: `# 1. bifrost_introduce`, `# 2. bifrost_whoami`
## Commit & Branch Conventions
- Default working branch: `develop`
- `main` is the release branch, updated only via merge from `develop`
- Never push directly to main/master
- Conventional commits: `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`
- Recent examples: `chore: remove pycache from tracking`, `docs: add roadmap`, `docs: update README`
## Module Design
- No barrel files (`__init__.py` files are empty)
- Import directly from the module: `from bifrost.hub.agents import AgentHub`
- Models imported individually: `from bifrost.store.models import Agent, Task, TaskStatus`
- Store layer (`src/bifrost/store/`) — raw SQLite CRUD, no business logic
- Hub layer (`src/bifrost/hub/`) — business logic, validation, state transitions
- Tool layer (`src/bifrost/mcp/tools.py`) — string formatting for MCP responses
- App layer (`src/bifrost/app.py`) — FastMCP wiring, session management, route registration
- All hub classes take a `Store` instance via constructor: `def __init__(self, store: Store) -> None`
- `ToolHandlers` takes all four hubs: `agents`, `tasks`, `conversations`, `delivery`
- No dependency injection framework — manual wiring in `create_app()`
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->
## Architecture

## Pattern Overview
- Single-process Python server exposing 11 MCP tools over Streamable HTTP
- A2A-inspired domain model (agents, tasks, conversations, events)
- Synchronous SQLite store (WAL mode) accessed by synchronous hub classes, wrapped in async MCP tool handlers
- Session-to-agent binding via in-memory dict (not persisted across restarts)
- Optional OAuth AS that proxies to an external OIDC provider (e.g., Dex)
## Layers
- Purpose: HTTP transport, MCP protocol handling, OAuth middleware
- Location: `src/bifrost/app.py` (app factory), FastMCP framework
- Contains: Tool registration, session management, custom routes (`/health`, `/callback`)
- Depends on: `mcp` SDK (`FastMCP`), Starlette
- Used by: External MCP clients (Claude Code, other agents)
- Purpose: Thin wrappers that translate MCP tool calls into hub method calls
- Location: `src/bifrost/mcp/tools.py`
- Contains: `ToolHandlers` class with one method per MCP tool (11 methods)
- Depends on: All four hub classes
- Used by: `src/bifrost/app.py` tool registrations
- Purpose: Domain logic, validation, state transitions
- Location: `src/bifrost/hub/`
- Contains: Four hub classes, each owning a domain area
- Depends on: `Store`, model dataclasses
- Used by: `ToolHandlers`
- Purpose: SQLite CRUD operations, schema management, JSON serialization
- Location: `src/bifrost/store/db.py`
- Contains: `Store` class with methods per entity type
- Depends on: `sqlite3`, model dataclasses
- Used by: All hub classes and `DexOAuthProvider`
- Purpose: Domain data structures (pure dataclasses, no behavior)
- Location: `src/bifrost/store/models.py`
- Contains: Dataclasses (`Agent`, `Task`, `Conversation`, `Event`, `AgentCard`, etc.), enums (`TaskStatus`, `AgentStatus`, `EventType`), content types (`TextPart`, `FilePart`, `DataPart`, `Artifact`)
- Depends on: Nothing (leaf module)
- Used by: Every other layer
- Purpose: OAuth Authorization Server proxying to external OIDC
- Location: `src/bifrost/auth/oauth.py`
- Contains: `DexOAuthProvider` implementing the MCP SDK's auth provider protocol
- Depends on: `Store` (for token/client persistence), `httpx` (for OIDC token exchange)
- Used by: `src/bifrost/app.py` (conditionally, only when `--insecure` is not set)
## Data Flow
- All persistent state lives in SQLite at `data/bifrost.db` (configurable via `--db`)
- Session-to-agent mapping (`session_agents`) is in-memory only — lost on restart
- OAuth pending auth state (`_pending_auths`) is in-memory — ephemeral by design (seconds)
- OAuth clients, tokens, and auth codes are persisted in SQLite
## Key Abstractions
- Purpose: Each hub owns one domain area and encapsulates its business rules
- Examples: `src/bifrost/hub/agents.py` (`AgentHub`), `src/bifrost/hub/tasks.py` (`TaskHub`), `src/bifrost/hub/conversations.py` (`ConversationHub`), `src/bifrost/hub/delivery.py` (`DeliveryHub`)
- Pattern: Constructor injection of `Store`; methods operate on domain models and delegate persistence
- Purpose: Single class mapping MCP tool names to hub method calls
- Examples: `src/bifrost/mcp/tools.py`
- Pattern: Aggregates all four hubs; each handler method is a thin orchestrator (resolve agent, call hub, format response string)
- Purpose: Enforce valid task status transitions
- Examples: `src/bifrost/hub/tasks.py` (`_TRANSITIONS` dict, `InvalidTransition` exception)
- Pattern: Explicit transition map checked before any status update
- Purpose: Single class wrapping all SQLite operations
- Examples: `src/bifrost/store/db.py`
- Pattern: One `Store` instance shared across all hubs; methods like `upsert_agent()`, `create_task()`, `append_event()`; handles JSON serialization of complex fields
## Entry Points
- Location: `src/bifrost/__main__.py`
- Triggers: Command-line invocation or `bifrost` console script
- Responsibilities: Parse args, build `Config`, call `create_app()`, run via `mcp.run(transport="streamable-http")`
- Location: `src/bifrost/app.py`
- Triggers: Called by CLI
- Responsibilities: Wire up Store -> Hubs -> ToolHandlers -> FastMCP; register all 11 tools; configure OAuth if enabled; add `/health` and `/callback` routes
- Location: Handled by FastMCP framework (configured in `app.py` via `streamable_http_path="/mcp"`)
- Triggers: MCP client connections (Streamable HTTP)
- Responsibilities: MCP protocol handling, tool dispatch
## Error Handling
- Hub classes raise `KeyError` for not-found entities (e.g., `Agent not found: {id}`)
- Hub classes raise `ValueError` for invalid operations (e.g., duplicate agent names)
- `TaskHub` raises `InvalidTransition` (custom exception) for illegal state changes
- Tool wrappers in `app.py` catch `(ValueError, KeyError)` and return `f"Error: {e}"`
- All errors are returned as plain text strings to the MCP client — no structured error codes
## Cross-Cutting Concerns
- Insecure: No auth, all sessions share a single key (`"insecure"`), only one agent active at a time
- OAuth: `DexOAuthProvider` in `src/bifrost/auth/oauth.py` acts as Authorization Server, proxying to external OIDC. Tokens persisted in SQLite. Session key derived from `access_token.token`.
<!-- GSD:architecture-end -->

<!-- GSD:workflow-start source:GSD defaults -->
## GSD Workflow Enforcement

Before using Edit, Write, or other file-changing tools, start work through a GSD command so planning artifacts and execution context stay in sync.

Use these entry points:
- `/gsd:quick` for small fixes, doc updates, and ad-hoc tasks
- `/gsd:debug` for investigation and bug fixing
- `/gsd:execute-phase` for planned phase work

Do not make direct repo edits outside a GSD workflow unless the user explicitly asks to bypass it.
<!-- GSD:workflow-end -->

<!-- GSD:profile-start -->
## Developer Profile

> Profile not yet configured. Run `/gsd:profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
