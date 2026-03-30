# Coding Conventions

**Analysis Date:** 2026-03-30

## Naming Patterns

**Files:**
- Use `snake_case.py` for all Python modules
- Hub modules named by domain: `agents.py`, `tasks.py`, `conversations.py`, `delivery.py`
- Test files prefixed with `test_`: `test_hub_agents.py`, `test_db.py`
- Single-purpose modules: one class per hub file, one config dataclass per `config.py`

**Functions:**
- Use `snake_case` for all functions and methods
- Public hub methods: verb-based — `register()`, `disconnect()`, `update_status()`, `create()`, `get()`, `list()`
- Private helpers prefixed with underscore: `_get_session_key()`, `_introduce_agent()`, `_get_session_agent()`, `_get_undelivered()`
- Store methods follow CRUD naming: `upsert_agent()`, `get_agent()`, `list_agents()`, `create_task()`, `update_task()`
- Row conversion methods: `_row_to_agent()`, `_row_to_task()`, `_row_to_conversation()`, `_row_to_event()`
- Tool handler methods: `handle_` prefix — `handle_introduce()`, `handle_whoami()`, `handle_send()`

**Variables:**
- Use `snake_case` for all variables
- Hub instances: descriptive nouns — `agents`, `tasks`, `conversations`, `delivery`, `handlers`
- Internal state stored with underscore prefix: `self._store`, `self._agents`
- SQL parameter lists: `clauses`, `params`, `sets`, `vals`

**Classes:**
- `PascalCase` for all classes
- Hub classes suffixed with `Hub`: `AgentHub`, `TaskHub`, `ConversationHub`, `DeliveryHub`
- Model classes are bare nouns: `Agent`, `Task`, `Conversation`, `Event`
- Enums use `StrEnum` with UPPERCASE members: `AgentStatus.ONLINE`, `TaskStatus.QUEUED`
- Custom exceptions: descriptive `PascalCase` — `InvalidTransition`

**Constants:**
- Module-level constants: `UPPERCASE` — `MCP_INSTRUCTIONS`, `_SCHEMA`
- Private module constants: underscore prefix — `_TRANSITIONS`

## Code Style

**Formatting:**
- No formatter configured (no ruff, black, flake8, or pylint config detected)
- Consistent 4-space indentation throughout
- Line lengths generally under 100, occasional longer lines for SQL and string formatting
- Trailing commas used in multi-line function calls and data structures

**Imports:**
- All modules start with `from __future__ import annotations` for PEP 604 union syntax
- Import order:
  1. `__future__` imports
  2. Standard library (`json`, `sqlite3`, `uuid`, `datetime`, `argparse`, `os`, `secrets`)
  3. Third-party (`starlette`, `mcp`, `httpx`, `asgi_lifespan`)
  4. Internal (`bifrost.store.models`, `bifrost.hub.agents`, etc.)
- Blank line separates each import group
- Prefer `from X import Y` over bare `import X`
- Some lazy imports inside functions for optional/conditional dependencies (e.g., `from mcp.server.auth.middleware.auth_context import get_access_token` inside `_get_session_agent()`)

**Type Hints:**
- Full type annotations on all function signatures (parameters and return types)
- Use modern union syntax: `str | None` instead of `Optional[str]`
- Use `list[str]`, `dict[str, Any]`, `set[TaskStatus]` (lowercase generics)
- `Any` used sparingly, primarily for JSON-like data: `dict[str, Any]`
- Return type always specified: `-> None`, `-> str`, `-> Agent`, `-> list[Task]`
- `@staticmethod` used for pure conversion functions in `Store` class

**Dataclasses:**
- All models use `@dataclass` (no Pydantic)
- Default factory for mutable fields: `field(default_factory=list)`, `field(default_factory=dict)`
- ID generation via `field(default_factory=_new_id)` — 12-char hex from `uuid4`
- Timestamps via `field(default_factory=_now)` — UTC ISO format strings

## Error Handling

**Patterns:**
- Hub methods raise `KeyError` for "not found" cases: `raise KeyError(f"Agent not found: {agent_id}")`
- Hub methods raise `ValueError` for invalid input: `raise ValueError(f"Name '{new_name}' is already taken")`
- Custom exceptions for domain-specific errors: `InvalidTransition` in `src/bifrost/hub/tasks.py`
- Tool handlers in `src/bifrost/app.py` catch `(ValueError, KeyError)` and return error strings: `return f"Error: {e}"`
- Store methods raise `ValueError` for disallowed field updates: `raise ValueError(f"Cannot update field: {k}")`
- No bare `except:` clauses — all catches are specific exception types
- OAuth callback in `src/bifrost/app.py` catches `ValueError` and generic `Exception` separately, returning appropriate HTTP status codes

**Error Return Convention:**
- MCP tool functions return error strings (not exceptions) to the LLM caller
- Internal hub/store methods raise exceptions for callers to handle
- Two-layer pattern: Hub raises -> App catches -> returns string to MCP client

## Documentation

**Module Docstrings:**
- Every module has a one-line docstring: `"""Bifrost FastMCP application factory."""`
- Format: `"""Description."""` (triple-quoted, single line, period-terminated)

**Class Docstrings:**
- Short single-line docstrings: `"""Manages agent registration, status, and cards."""`
- Not all classes have docstrings (e.g., model dataclasses lack them)

**Method Docstrings:**
- Hub public methods have one-line docstrings: `"""Register or reconnect an agent. Sets status to ONLINE."""`
- Multi-line docstrings used for complex methods with detailed behavior explanation (see `ConversationHub.send()` and `AgentHub.update_card()`)
- MCP tool functions use docstrings as tool descriptions (consumed by LLM), include `Args:` sections

**Inline Comments:**
- Section separators use `# ---` comment blocks: `# ------------------------------------------------------------------`
- Inline comments explain "why" not "what": `# Ensure sender is a participant`, `# Terminal states`
- Numbered sections in `src/bifrost/mcp/tools.py`: `# 1. bifrost_introduce`, `# 2. bifrost_whoami`

**No JSDoc/TSDoc** — Python project, no TypeScript.

## Commit & Branch Conventions

**Git Workflow:**
- Default working branch: `develop`
- `main` is the release branch, updated only via merge from `develop`
- Never push directly to main/master

**Commit Messages:**
- Conventional commits: `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`
- Recent examples: `chore: remove pycache from tracking`, `docs: add roadmap`, `docs: update README`

## Module Design

**Exports:**
- No barrel files (`__init__.py` files are empty)
- Import directly from the module: `from bifrost.hub.agents import AgentHub`
- Models imported individually: `from bifrost.store.models import Agent, Task, TaskStatus`

**Layer Separation:**
- Store layer (`src/bifrost/store/`) — raw SQLite CRUD, no business logic
- Hub layer (`src/bifrost/hub/`) — business logic, validation, state transitions
- Tool layer (`src/bifrost/mcp/tools.py`) — string formatting for MCP responses
- App layer (`src/bifrost/app.py`) — FastMCP wiring, session management, route registration

**Constructor Pattern:**
- All hub classes take a `Store` instance via constructor: `def __init__(self, store: Store) -> None`
- `ToolHandlers` takes all four hubs: `agents`, `tasks`, `conversations`, `delivery`
- No dependency injection framework — manual wiring in `create_app()`

---

*Convention analysis: 2026-03-30*
