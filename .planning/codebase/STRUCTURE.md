# Project Structure

**Analysis Date:** 2026-03-30

## Directory Layout

```
bifrost/
├── src/bifrost/               # Main Python package
│   ├── __init__.py            # Empty
│   ├── __main__.py            # CLI entry point (argparse)
│   ├── app.py                 # FastMCP app factory, tool registration, session management
│   ├── config.py              # Config dataclass (CLI args / env vars)
│   ├── a2a/                   # Placeholder package (empty __init__.py)
│   │   └── __init__.py
│   ├── auth/                  # OAuth Authorization Server
│   │   ├── __init__.py
│   │   └── oauth.py           # DexOAuthProvider — OIDC proxy, token management
│   ├── dashboard/             # Placeholder package (empty __init__.py)
│   │   └── __init__.py
│   ├── hub/                   # Business logic layer
│   │   ├── __init__.py
│   │   ├── agents.py          # AgentHub — registration, status, cards
│   │   ├── conversations.py   # ConversationHub — messaging, channels, subscriptions
│   │   ├── delivery.py        # DeliveryHub — polling-based event delivery
│   │   └── tasks.py           # TaskHub — A2A task lifecycle, state machine
│   ├── mcp/                   # MCP tool interface
│   │   ├── __init__.py
│   │   └── tools.py           # ToolHandlers — one method per MCP tool
│   └── store/                 # Persistence layer
│       ├── __init__.py
│       ├── db.py              # Store — SQLite CRUD, schema DDL, JSON serialization
│       └── models.py          # Dataclasses, enums, content types (Agent, Task, etc.)
├── tests/                     # pytest test suite
│   ├── __init__.py
│   ├── conftest.py            # Empty (fixtures inline in test files)
│   ├── test_app.py            # App factory and tool registration tests
│   ├── test_db.py             # Store/SQLite layer tests
│   ├── test_hub_agents.py     # AgentHub unit tests
│   ├── test_hub_conversations.py  # ConversationHub unit tests
│   ├── test_hub_delivery.py   # DeliveryHub unit tests
│   ├── test_hub_tasks.py      # TaskHub unit tests (incl. state machine)
│   ├── test_integration.py    # End-to-end tool handler tests
│   ├── test_models.py         # Model dataclass tests
│   └── test_tools.py          # ToolHandlers unit tests
├── docs/                      # Historical design specs (not runtime)
│   ├── research/              # Landscape analysis docs
│   ├── spike-results.md       # Spike outcomes
│   ├── superpowers/plans/     # Implementation plans
│   ├── superpowers/specs/     # Design specifications
│   └── v2-remote-mcp-server.md
├── data/                      # Runtime SQLite database (gitignored)
├── .planning/                 # GSD planning documents
├── pyproject.toml             # Project metadata, dependencies, build config
├── Dockerfile                 # Production container (Python 3.14-slim + uv)
├── docker-compose.yml         # Production compose
├── docker-compose.dev.yml     # Dev compose
├── CLAUDE.md                  # AI assistant project context
├── README.md                  # Project readme
├── ROADMAP.md                 # Feature roadmap
└── LICENSE                    # LGPL-3.0-or-later
```

## Module Organization

**Package: `bifrost`**

The codebase follows a layered package structure with clear dependency direction:

```
app.py (wiring)
  └── mcp/tools.py (tool handlers)
        └── hub/*.py (business logic)
              └── store/db.py (persistence)
                    └── store/models.py (data structures)
```

- `store/models.py` is the leaf module — imported by everything, imports nothing from bifrost
- `store/db.py` imports only from `store/models.py`
- `hub/*.py` imports from `store/db.py` and `store/models.py`
- `mcp/tools.py` imports from `hub/*.py` and `store/models.py`
- `app.py` imports from all layers and wires them together
- `auth/oauth.py` imports from `store/db.py` and MCP SDK auth types

**Placeholder packages:**
- `src/bifrost/a2a/` — empty, reserved for future A2A protocol types
- `src/bifrost/dashboard/` — empty, reserved for future web dashboard

## Entry Points

**CLI Entry Point:**
- `src/bifrost/__main__.py` — `main()` function
- Invoked via `python -m bifrost` or `bifrost` console script (defined in `pyproject.toml` `[project.scripts]`)
- Parses CLI args with `argparse`, builds `Config`, calls `create_app()`, runs with `app.run(transport="streamable-http")`

**App Factory:**
- `src/bifrost/app.py` — `create_app(config: Config) -> FastMCP`
- Creates `Store`, instantiates all four hubs, creates `ToolHandlers`, builds `FastMCP` instance
- Registers 11 MCP tools as async functions with `@mcp.tool()`
- Adds custom routes: `GET /health`, `GET /callback` (OAuth only)
- Monkey-patches `_tool_manager.list_tools` for auto-registration on first MCP call

**HTTP Endpoints:**
- `/mcp` — Streamable HTTP MCP endpoint (handled by FastMCP)
- `/health` — Health check (`{"status": "ok"}`)
- `/callback` — OAuth OIDC callback (only when auth is configured)

## Configuration Files

**`pyproject.toml`:**
- Project metadata (name, version, license)
- Dependencies: `mcp>=1.9.0`, `fastapi>=0.115.0`, `uvicorn[standard]>=0.34.0`, `jinja2>=3.1.0`, `httpx>=0.28.0`
- Dev dependencies: `pytest>=8.0`, `pytest-asyncio>=0.25.0`, `httpx>=0.28.0`, `asgi-lifespan>=2.1.0`
- Build system: Hatchling
- pytest config: `asyncio_mode = "auto"`, `testpaths = ["tests"]`

**`src/bifrost/config.py`:**
- `Config` dataclass with fields: `host`, `port`, `db_path`, `insecure`, `oidc_issuer`, `oidc_client_id`, `oidc_client_secret`, `server_url`
- Populated from CLI args in `__main__.py`; OIDC fields fall back to env vars (`OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `SERVER_URL`)

**`.env.example`:**
- Exists — documents required environment variables (not read, existence noted only)

**`Dockerfile`:**
- Python 3.14-slim base, installs via `uv pip install --system .`, exposes port 8000

**`docker-compose.yml` / `docker-compose.dev.yml`:**
- Production and development compose configurations

## Key File Locations

**Entry Points:**
- `src/bifrost/__main__.py`: CLI entry, arg parsing, app startup
- `src/bifrost/app.py`: App factory, tool registration, session wiring

**Configuration:**
- `src/bifrost/config.py`: Server config dataclass
- `pyproject.toml`: Dependencies, build config, pytest config

**Core Logic:**
- `src/bifrost/hub/agents.py`: Agent registration, status, card management
- `src/bifrost/hub/tasks.py`: Task creation, A2A state machine transitions
- `src/bifrost/hub/conversations.py`: Message sending, conversation creation
- `src/bifrost/hub/delivery.py`: Polling-based event delivery to agents

**Data Layer:**
- `src/bifrost/store/models.py`: All dataclasses and enums
- `src/bifrost/store/db.py`: SQLite schema DDL, CRUD operations, JSON serialization helpers

**Auth:**
- `src/bifrost/auth/oauth.py`: OAuth AS proxying to external OIDC

**Testing:**
- `tests/test_hub_*.py`: Unit tests per hub class
- `tests/test_db.py`: Store layer tests
- `tests/test_tools.py`: ToolHandlers unit tests
- `tests/test_integration.py`: End-to-end tests
- `tests/test_models.py`: Model dataclass tests
- `tests/test_app.py`: App factory tests

## Naming Conventions

**Files:**
- Snake_case for all Python modules: `agents.py`, `tools.py`, `db.py`
- Test files prefixed with `test_`: `test_hub_agents.py`
- Test files mirror source structure: `hub/agents.py` -> `test_hub_agents.py`

**Directories:**
- Lowercase single words: `hub/`, `store/`, `mcp/`, `auth/`, `tests/`
- Each directory is a Python package with `__init__.py`

## Where to Add New Code

**New Hub Domain (e.g., federation, notifications):**
- Create `src/bifrost/hub/<domain>.py` with a `<Domain>Hub` class
- Inject `Store` in constructor
- Add persistence methods to `src/bifrost/store/db.py`
- Add dataclasses/enums to `src/bifrost/store/models.py`
- Wire into `create_app()` in `src/bifrost/app.py`

**New MCP Tool:**
- Add handler method to `ToolHandlers` class in `src/bifrost/mcp/tools.py`
- Register as `@mcp.tool()` async function in `src/bifrost/app.py`
- Add tests in `tests/test_tools.py` and `tests/test_integration.py`

**New SQLite Table:**
- Add DDL to `_SCHEMA` string in `src/bifrost/store/db.py`
- Add CRUD methods to the `Store` class in the same file
- Add corresponding dataclass to `src/bifrost/store/models.py`

**New HTTP Endpoint (non-MCP):**
- Use `@mcp.custom_route()` in `src/bifrost/app.py` (Starlette-style handler)

**Dashboard / Web UI:**
- `src/bifrost/dashboard/` package is reserved for this (currently empty)

**Tests:**
- Unit tests: `tests/test_<module>.py` or `tests/test_hub_<domain>.py`
- Integration tests: `tests/test_integration.py`
- Use pytest-asyncio auto mode (no `@pytest.mark.asyncio` needed)

## Special Directories

**`data/`:**
- Purpose: Runtime SQLite database (`bifrost.db`)
- Generated: Yes (created automatically by `Store.__init__`)
- Committed: No (gitignored)

**`docs/`:**
- Purpose: Historical design specs, plans, and research
- Generated: No (hand-written)
- Committed: Yes

**`.planning/`:**
- Purpose: GSD planning and codebase analysis documents
- Generated: Yes (by GSD tooling)
- Committed: Yes

---

*Structure analysis: 2026-03-30*
