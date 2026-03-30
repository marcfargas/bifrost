# Technology Stack

**Analysis Date:** 2026-03-30

## Languages

**Primary:**
- Python 3.12+ - All application code (`src/bifrost/`)

**Secondary:**
- SQL (SQLite dialect) - Schema and queries embedded in `src/bifrost/store/db.py`

## Runtime

**Environment:**
- CPython 3.12+ (required by `pyproject.toml`: `requires-python = ">=3.12"`)
- Docker image uses `python:3.14-slim` (`Dockerfile`)

**Package Manager:**
- pip (standard) for local development: `pip install -e ".[dev]"`
- uv (Astral) for Docker builds: copied from `ghcr.io/astral-sh/uv:latest` in `Dockerfile`
- No lockfile (no `requirements.txt`, no `uv.lock`) - dependencies resolved at install time

## Frameworks

**Core:**
- FastMCP (from `mcp` package) >= 1.9.0 - MCP server framework, provides tool registration, Streamable HTTP transport, and OAuth middleware
- FastAPI >= 0.115.0 - Underlying ASGI framework (used by FastMCP internally; custom routes via `mcp.custom_route()`)
- Starlette - Request/Response types used directly (`starlette.requests.Request`, `starlette.responses.JSONResponse`)

**Testing:**
- pytest >= 8.0 - Test runner
- pytest-asyncio >= 0.25.0 - Async test support (configured with `asyncio_mode = "auto"`)

**Build/Dev:**
- Hatchling - PEP 517 build backend (`pyproject.toml` `[build-system]`)

## Key Dependencies

**Critical:**
- `mcp` >= 1.9.0 - Core MCP protocol implementation (FastMCP server, auth provider types, OAuth middleware). This is THE framework dependency. Provides `mcp.server.fastmcp.FastMCP`, `mcp.server.auth.provider.*`, `mcp.server.auth.settings.*`, `mcp.server.auth.middleware.auth_context.*`, `mcp.shared.auth.*`.
- `fastapi` >= 0.115.0 - ASGI web framework (FastMCP builds on top of it)
- `uvicorn[standard]` >= 0.34.0 - ASGI server for HTTP transport
- `httpx` >= 0.28.0 - Async HTTP client for OIDC token exchange (`src/bifrost/auth/oauth.py`)
- `jinja2` >= 3.1.0 - Template engine (likely for FastMCP internals or future dashboard)

**Infrastructure:**
- `sqlite3` (stdlib) - Persistence layer, no external DB driver needed
- `pydantic` (transitive via `mcp`) - Used in OAuth provider for `OAuthClientInformationFull.model_validate_json()` / `model_dump_json()`

**Dev-only:**
- `asgi-lifespan` >= 2.1.0 - Test helper for ASGI app lifecycle in tests

## Configuration

**Environment:**
- CLI args with env var fallbacks for OIDC settings (`src/bifrost/__main__.py`)
- Key env vars: `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `SERVER_URL`
- `.env.example` file present for reference
- `--insecure` flag bypasses all auth for local dev

**Build:**
- `pyproject.toml` - Single source of truth for project metadata, dependencies, build config, and pytest settings
- No separate `setup.py`, `setup.cfg`, or `requirements.txt`

**Docker:**
- `Dockerfile` - Single-stage build, `python:3.14-slim` base, uv for fast installs
- `docker-compose.yml` - Production config with volume for `data/`
- `docker-compose.dev.yml` - Overlay for Traefik reverse proxy deployment on `bifrost.blegal.dev`

## Platform Requirements

**Development:**
- Python 3.12+
- `pip install -e ".[dev]"` for editable install with test dependencies
- SQLite3 (ships with Python stdlib)
- No external services required in `--insecure` mode

**Production:**
- Docker (preferred) or Python 3.12+ with pip/uv
- External OIDC provider (e.g., Dex) for authentication
- Persistent volume for SQLite database at `data/bifrost.db`
- Reverse proxy (Traefik) for TLS termination in deployed environments

---

*Stack analysis: 2026-03-30*
