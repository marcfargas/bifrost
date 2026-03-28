---
name: setup
description: Guided setup and status check for the Bifrost cross-agent communication hub.
---

# /bifrost:setup

You are helping the user set up and verify their Bifrost installation. Follow
these steps in order, stopping if any step fails and explaining how to fix it.

## Step 1: Check binary

Run this command to check if the `bifrost` binary is installed and get its version:

```bash
bifrost version
```

**If the command fails** (not found):
- Tell the user: "The `bifrost` binary is not on your PATH."
- Offer two options:
  1. Install from source: `go install github.com/marcfargas/bifrost/cmd/bifrost@latest`
  2. Download from GitHub Releases: `https://github.com/marcfargas/bifrost/releases`
- After installation, re-run `bifrost version` to verify.

**If it succeeds**, show the version and protocol version. Continue to Step 2.

## Step 2: Check hub status

Run this command to check if a hub is already running:

```bash
bifrost hub status
```

**If the hub is running:**
- Show the status output (connected agents, uptime, listeners).
- Continue to Step 3.

**If the hub is NOT running:**
- Ask the user: "No hub is running. Would you like to:"
  1. **Start one now** (foreground): `bifrost hub start`
  2. **Start in background**: `bifrost hub start -d`
  3. **Skip** — the shim will auto-start a hub when needed.
- If the user wants to start with MCP HTTP enabled, use:
  `bifrost hub start -d --hub.mcp.enabled=true`
- After starting, re-run `bifrost hub status` to confirm.

## Step 3: Show connected agents

Run:

```bash
bifrost agents
```

- Display the list of connected agents with their project names, aliases, and status.
- If no agents are connected, explain: "No agents connected yet. When you start Claude Code sessions with the bifrost plugin or MCP server configured, they will appear here automatically."

## Step 4: Check MCP configuration

Read the user's Claude Code settings to see if bifrost is configured:

```bash
cat ~/.claude/settings.json 2>/dev/null || echo "No settings.json found"
```

On Windows, check `%APPDATA%/claude/settings.json` instead.

**If bifrost is already configured** in `mcpServers`:
- Show the current configuration.
- Confirm it looks correct.

**If bifrost is NOT configured:**
- Ask the user which mode they prefer:
  1. **Shim mode** (recommended): Add to settings.json:
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
  2. **Direct MCP mode**: Add to settings.json:
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
     Remind the user that direct mode requires the hub to be running with
     `--hub.mcp.enabled=true`.

## Step 5: Federation (optional)

Ask the user: "Would you like to set up federation with another machine?"

**If yes:**
- Ask: "Are you creating a NEW federation or JOINING an existing one?"
  - **New**: Run `bifrost peer new` and show the generated magic code.
    Tell the user: "Share this code with the other machine. They should run:
    `bifrost peer join <CODE>`"
  - **Join**: Ask for the magic code, then run `bifrost peer join <CODE>`.
    Show the connection result and the list of remote agents.

**If no:**
- Skip. Tell the user: "You can set up federation later with `bifrost peer new` or the `bifrost_peer` tool."

## Step 6: Summary

Display a summary of the setup:

```
Bifrost Setup Summary
=====================
Binary:      bifrost vX.Y.Z (protocol vX.Y.Z)
Hub:         running / not running (auto-start via shim)
Agents:      N connected
MCP Config:  shim mode / direct mode / not configured
Federation:  N peers / not configured
```

Tell the user: "Setup complete. Your Claude Code agents can now communicate through Bifrost. Start a new Claude Code session to see it in action."
