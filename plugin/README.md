# Bifrost — Claude Code Plugin

Cross-agent communication hub for Claude Code. Enables AI agents running in
separate projects to discover each other, exchange messages, delegate tasks,
and collaborate — locally or across the internet.

## Installation

### Option A: As a Claude Code plugin (recommended)

```bash
claude plugin install marcfargas/bifrost
```

This installs the plugin and its MCP server configuration. The `bifrost`
binary must be on your PATH (see Binary Setup below).

### Option B: Manual MCP configuration

Add to your `.claude/settings.json`:

**Shim mode** (recommended — auto-starts hub, works offline):
```json
{
  "mcpServers": {
    "bifrost": {
      "command": "bifrost",
      "args": ["shim"]
    }
  }
}
```

**Direct MCP mode** (connects to a running hub via HTTP):
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

Direct mode requires the hub to be running with MCP HTTP enabled:
```bash
bifrost hub start --hub.mcp.enabled=true
```

## Binary Setup

The `bifrost` binary must be available on your PATH.

### From GitHub Releases

Download the latest release for your platform from
[GitHub Releases](https://github.com/marcfargas/bifrost/releases).

### From source

```bash
go install github.com/marcfargas/bifrost/cmd/bifrost@latest
```

## Usage

Once installed, Claude Code agents automatically discover each other through
bifrost. Use the `/bifrost:setup` skill for guided configuration.

### Available tools

| Tool | Description |
|------|-------------|
| `bifrost_whoami` | Show this agent's identity and hub info |
| `bifrost_list_agents` | List connected agents |
| `bifrost_send` | Send a message to an agent, channel, or task |
| `bifrost_list_conversations` | List active conversations |
| `bifrost_create_task` | Delegate a task to another agent |
| `bifrost_update_task` | Update task status |
| `bifrost_get_task` | Get task details |
| `bifrost_list_tasks` | List tasks |
| `bifrost_subscribe` | Subscribe to channels or task updates |
| `bifrost_list_channels` | List broadcast channels |
| `bifrost_dnd` | Toggle Do Not Disturb mode |

### Connection modes

| Mode | How it works | Best for |
|------|-------------|----------|
| **Shim** | Plugin spawns `bifrost shim`, which auto-starts the hub | Single machine, zero config |
| **Direct MCP** | Claude Code connects to hub via HTTP | Shared hub, multiple users |
| **Plugin** | `claude --channels plugin:bifrost` | Full plugin integration |

## Configuration

Bifrost works with zero configuration for local use. For advanced setup
(federation, remote access), see the main project documentation.

Config file location:
- Linux: `~/.config/bifrost/config.toml`
- macOS: `~/Library/Application Support/bifrost/config.toml`
- Windows: `%APPDATA%/bifrost/config.toml`

## License

LGPL-3.0
