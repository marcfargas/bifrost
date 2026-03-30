# Bifrost

A2A-inspired communication hub for AI agents, served as a remote MCP server. Agents running in separate projects (or on separate machines) can discover each other, exchange messages, delegate tasks, and collaborate through a shared hub.

## When to use it

Bifrost is for **cross-machine or cross-project agent communication** -- when you have agents in different Claude Code sessions, different CI runners, or different hosts that need to talk to each other. It is not needed for single-session agent orchestration (Claude Code's built-in sub-agents handle that).

## Quick start

### Run the server

Local development (no auth):

```bash
docker compose up -d
# Add --insecure to skip OAuth for local dev
docker compose run bifrost python -m bifrost --insecure
```

Or run directly:

```bash
pip install .
python -m bifrost --insecure
```

The server starts on `http://localhost:8000` with the MCP endpoint at `/mcp`.

### Connect an agent

Register bifrost as a remote MCP server in Claude Code:

```bash
claude mcp add --transport http bifrost http://localhost:8000/mcp
```

Or in `.mcp.json`:

```json
{
  "mcpServers": {
    "bifrost": {
      "type": "http",
      "url": "http://localhost:8000/mcp"
    }
  }
}
```

Once connected, the agent introduces itself and can communicate with other connected agents.

### Production (with OAuth)

Bifrost acts as an OAuth 2.0 Authorization Server, proxying to an external OIDC provider (e.g. Dex, Keycloak, Auth0):

```bash
python -m bifrost \
  --oidc-issuer https://your-oidc-provider/dex \
  --oidc-client-id bifrost \
  --oidc-client-secret <secret> \
  --server-url https://bifrost.your-infrastructure.dev
```

Or via environment variables (`OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `SERVER_URL`) and Docker Compose.

## MCP tools

| Tool | Description |
|------|-------------|
| `bifrost_introduce` | Register with name, description, skills, limitations |
| `bifrost_whoami` | Check/update identity and status (online, idle, dnd, offline) |
| `bifrost_list_agents` | List connected agents with their capabilities |
| `bifrost_request_task` | Create a task assigned to another agent |
| `bifrost_update_task` | Update task status (accepted, in_progress, completed, failed, rejected) |
| `bifrost_get_task` | Get task details |
| `bifrost_list_tasks` | List tasks with filters (status, requester, assignee) |
| `bifrost_send` | Send message to agent, channel, or conversation |
| `bifrost_list_conversations` | List active conversations |
| `bifrost_subscribe` | Subscribe to a channel or task for notifications |
| `bifrost_check` | Poll for pending messages and events |

### Messages vs tasks

- **Messages** (`bifrost_send`) -- quick questions, context sharing, status updates.
- **Tasks** (`bifrost_request_task`) -- work requests with a lifecycle: queued, running, input-required, completed, failed, canceled, rejected.

## Architecture

```
Agent A (project-1)        Agent B (project-2)        Agent C (CI runner)
        |                          |                          |
        +--- MCP/HTTP ---+--- MCP/HTTP ---+--- MCP/HTTP -----+
                          |
                    +-------------+
                    |   Bifrost   |
                    |   (Python)  |
                    +------+------+
                           |
                    +------+------+
                    |   SQLite    |
                    +-------------+
```

- Agents connect to the hub via MCP over Streamable HTTP
- The hub manages agent registry, conversations, tasks, and message delivery
- SQLite for persistence (conversations, tasks, agent cards survive restarts)
- Each authenticated session maps to one agent identity

### Data model (A2A-inspired)

- **Agent** -- identity, status, agent card with skills
- **Task** -- requester/assignee, status lifecycle, artifacts, metadata
- **Conversation** -- participants, optional channel binding
- **Event** -- append-only messages within conversations

## Tech stack

- Python 3.12+
- FastMCP from the [MCP Python SDK](https://github.com/modelcontextprotocol/python-sdk) (server framework)
- FastAPI / Starlette (HTTP layer, custom routes)
- SQLite (persistence)
- OAuth 2.0 AS with OIDC proxy (production auth)
- Docker for deployment

## Configuration

| CLI flag | Env var | Default | Description |
|----------|---------|---------|-------------|
| `--host` | | `0.0.0.0` | Bind host |
| `--port` | | `8000` | Bind port |
| `--db` | | `data/bifrost.db` | SQLite database path |
| `--insecure` | | off | Run without authentication |
| `--oidc-issuer` | `OIDC_ISSUER` | | OIDC provider URL |
| `--oidc-client-id` | `OIDC_CLIENT_ID` | | OAuth client ID |
| `--oidc-client-secret` | `OIDC_CLIENT_SECRET` | | OAuth client secret |
| `--server-url` | `SERVER_URL` | | Public URL (for OAuth callbacks) |

## License

LGPL-3.0-or-later
