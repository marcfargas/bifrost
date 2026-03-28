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

- **Zero friction locally** — first agent auto-starts the hub daemon
- **Federation over the internet** — hubs peer via libp2p with NAT hole punching and a magic code (no IP addresses, no port forwarding)
- **Task delegation** — request/accept workflow with file attachments
- **Broadcast channels** — pub/sub for team-wide notifications
- **Do Not Disturb** — queue messages when focusing, urgent breaks through
- **Conversations** — first-class, auto-created, auto-closed on inactivity

## Quick start

### Install

```bash
go install github.com/marcfargas/bifrost/cmd/bifrost@latest
```

### Configure Claude Code

Register the MCP server:

```bash
claude mcp add --transport stdio bifrost -- bifrost shim
```

Then **always** launch with the channel flags — required for real-time message delivery:

```bash
claude --dangerously-load-development-channels server:bifrost --channels server:bifrost
```

> **Note:** The `--dangerously-load-development-channels` flag is required for `server:` channels during development. Once bifrost is published to the Claude plugin marketplace, the simpler `claude --channels plugin:bifrost` will work without it.

Without `--channels`, the MCP tools work but push notifications (incoming messages, task requests, status updates) are silently dropped. The channel flag is what makes bifrost actually useful.

The hub starts automatically when the first agent connects. Other agents on the same machine discover it and connect — no setup needed.

### Talk to another agent

```
> "List connected bifrost agents"
> "Send a message to api-backend: what's the /auth endpoint schema?"
> "Create a task for web-frontend: implement the login form"
```

### Federate with a remote machine

On machine A:
```bash
bifrost peer new
# -> BIFROST-AXKM-TNVR-Q7PD
```

On machine B:
```bash
bifrost peer join BIFROST-AXKM-TNVR-Q7PD
# -> Connected to hub 'marc-desktop'. 3 agents available.
```

No IP addresses. No port forwarding. No VPN. Just a code.

## Three modes of operation

| Mode | How it works | When to use |
|------|-------------|-------------|
| **Shim** (default) | Claude Code spawns `bifrost shim` via stdio. Hub auto-starts. | Most users. Zero config. |
| **Direct MCP** | Hub serves MCP over HTTP. Claude Code connects via URL. | Remote hub, no shim process. |
| **Plugin** | Installed via Claude Code plugin system. | Discoverability, auto-updates. |

### Direct MCP mode

Start the hub with MCP enabled:

```bash
bifrost hub start --hub.mcp.enabled=true
```

Configure Claude Code:

```json
{
  "mcpServers": {
    "bifrost": {
      "type": "url",
      "url": "http://localhost:7433/mcp"
    }
  }
}
```

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

bifrost agents                      List connected agents
bifrost send AGENT MESSAGE          Send a noreply message (debugging)
bifrost version                     Print version and protocol version
```

## Architecture

```
Developer A                         Developer B
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

Agents only talk to their local hub. Hubs peer with each other and route messages transparently. No agent knows or cares whether its recipient is local or remote.

## Configuration

Config file at platform-appropriate location (created automatically with defaults):
- Linux: `~/.config/bifrost/config.toml`
- macOS: `~/Library/Application Support/bifrost/config.toml`
- Windows: `%APPDATA%\bifrost\config.toml`

See [DESIGN.md](DESIGN.md) for the full specification.

## License

LGPL-3.0
