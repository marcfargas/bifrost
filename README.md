# Bifrost

Cross-agent communication hub for Claude Code. Enables AI agents running in separate projects to discover each other, exchange messages, delegate tasks, and collaborate — locally or across the internet.

## What it does

Two Claude Code sessions, each in a different project, can talk to each other:

```
Terminal 1 (backend project):
> "Ask the frontend agent what components need the new API"

Terminal 2 (frontend project):
> <channel source="bifrost" from="api-backend" type="question" ...>
> What components need the new API?
```

Messages are pushed in real-time via Claude Code's channel notification system — no polling.

## Features

- **Zero friction locally** — first agent auto-starts the hub daemon, auto-reconnects on hub restart
- **Federation over the internet** — hubs peer via libp2p with NAT hole punching and a magic code (no IP addresses, no port forwarding)
- **Task delegation** — request/accept workflow with file attachments and status tracking
- **Broadcast channels** — pub/sub for team-wide notifications
- **Do Not Disturb** — queue messages when focusing, urgent breaks through
- **Conversations** — first-class, auto-created, auto-closed on inactivity
- **Self-update** — `bifrost update` downloads the latest release

## Quick start

### Install

From source:
```bash
go install github.com/marcfargas/bifrost/cmd/bifrost@latest
```

Or download a prebuilt binary from [GitHub releases](https://github.com/marcfargas/bifrost/releases).

### Configure Claude Code

Register the MCP server:

```bash
claude mcp add --scope user --transport stdio bifrost -- bifrost shim
```

Launch with the channel flag for real-time message delivery:

```bash
claude --dangerously-load-development-channels server:bifrost
```

> **Note:** `--dangerously-load-development-channels` is required for custom channel servers during the Claude Code channels research preview. Once bifrost is on the plugin marketplace, `--channels plugin:bifrost` will work without it.

The hub starts automatically when the first agent connects. Other agents on the same machine discover it and connect — no setup needed.

### Talk to another agent

```
> "List connected bifrost agents"
> "Send a message to api-backend: what's the /auth endpoint schema?"
> "Create a task for web-frontend: implement the login form"
```

Messages and tasks are different:
- **Messages** (`bifrost_send`) — quick questions, context sharing, status updates
- **Tasks** (`bifrost_create_task`) — work requests with accept/reject/complete lifecycle

### Federate with a remote machine

On machine A:
```bash
bifrost peer new
# -> BIFROST-AXKM-TNVR-Q7PD
```

On machine B:
```bash
bifrost peer join BIFROST-AXKM-TNVR-Q7PD
# -> Connected to hub. Remote agents available.
```

No IP addresses. No port forwarding. No VPN. Just a code.

### Update

```bash
bifrost update
```

Downloads the latest release from GitHub and replaces the binary in-place.

### Deploy to a remote host

```bash
GOOS=linux GOARCH=amd64 go build -o /tmp/bifrost-linux \
  -ldflags "-s -w -X main.commit=$(git rev-parse --short HEAD)" \
  ./cmd/bifrost
scp /tmp/bifrost-linux user@host:~/bin/bifrost
```

Then configure Claude Code on the remote host the same way.

## MCP Tools

| Tool | Description |
|------|-------------|
| `bifrost_list_agents` | List connected agents (local + federated) |
| `bifrost_whoami` | Show this agent's identity and aliases |
| `bifrost_send` | Send message to agent, channel, or task |
| `bifrost_list_conversations` | List active conversations |
| `bifrost_create_task` | Create and assign a task with file attachments |
| `bifrost_update_task` | Accept, reject, complete, or update a task |
| `bifrost_get_task` | Get task details with optional attachment download |
| `bifrost_list_tasks` | List tasks by status or role |
| `bifrost_subscribe` | Subscribe to a channel or task updates |
| `bifrost_list_channels` | List broadcast channels |
| `bifrost_dnd` | Enable/disable Do Not Disturb |
| `bifrost_peer` | Generate or join a federation magic code |

## CLI

```
bifrost hub start [-d]              Start hub daemon
bifrost hub stop                    Stop hub daemon
bifrost hub status                  Show status and connected agents

bifrost peer new                    Generate magic code for federation
bifrost peer join CODE              Join a federated hub
bifrost peer list                   List peered hubs

bifrost agents                      List connected agents (with hostname and hub)
bifrost send AGENT MESSAGE          Send a noreply message (debugging)
bifrost update                      Self-update from GitHub releases
bifrost version                     Print version, commit, and protocol version
```

## Architecture

```
Developer A (Windows)               Developer B (Linux)
+-----------+ +-----------+        +-----------+ +-----------+
| frontend  | | backend   |        | mobile    | | infra     |
| agent     | | agent     |        | agent     | | agent     |
+-----+-----+ +-----+-----+        +-----+-----+ +-----+-----+
      |              |                    |              |
      +------+-------+                   +------+-------+
             v                                  v
      +-------------+  libp2p + DHT     +-------------+
      |  Hub A      |<================>|  Hub B      |
      |  (auto)     |  magic code       |  (auto)     |
      +-------------+                   +-------------+
```

- Agents only talk to their local hub
- Hubs peer with each other and route messages transparently
- No agent knows or cares whether its recipient is local or remote
- Unix sockets for local communication (all platforms including Windows 10+)
- SQLite for persistence (pure Go, no CGO)
- Single binary, ~13MB, no dependencies

## Configuration

Config file at platform-appropriate location (created automatically with defaults):
- Linux: `~/.config/bifrost/config.toml`
- macOS: `~/Library/Application Support/bifrost/config.toml`
- Windows: `%APPDATA%\bifrost\config.toml`

Override socket path: set `BIFROST_SOCKET_PATH` environment variable.

See the [design spec](docs/superpowers/specs/2026-03-27-bifrost-design.md) for the full specification.

## License

LGPL-3.0
