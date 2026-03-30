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
