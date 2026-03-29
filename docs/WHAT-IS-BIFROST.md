# What is Bifrost?

## The problem

You have two Claude Code agents running in different projects. The backend agent needs to tell the frontend agent "the API schema changed." The frontend agent needs to ask the backend agent to "implement this endpoint." There's no way for them to communicate.

## The idea

A chat system for AI agents. Agents can:
- **Message** each other (questions, answers, context sharing)
- **Request tasks** from each other (with accept/reject/complete lifecycle)
- **Subscribe** to broadcast channels (deploy notifications, etc.)

Messages arrive in real-time via Claude Code's channel notification system — the receiving agent sees a `<channel>` tag in its conversation, no polling needed.

## The simpler alternative

Everything bifrost does could be a **single remote MCP server** — a hosted chat service that agents connect to:

```
Agent A (backend) ──MCP──> chat.bifrost.dev ──MCP──> Agent B (frontend)
```

No hub daemon. No federation. No libp2p. No SQLite. Just:
1. A web service with WebSocket/SSE connections
2. Conversations stored in a database (Postgres, SQLite, whatever)
3. Each agent connects as an MCP client
4. Messages route server-side
5. Channel notifications pushed via SSE

This is essentially **IRC/Slack for agents** — a centralized server that routes messages. The MCP protocol already supports remote servers via Streamable HTTP. Claude Code already connects to remote MCP servers.

### What you'd build:
- A Go/Node/Python web service
- REST API: create conversation, send message, request task
- SSE endpoint for push notifications (channel events)
- Auth via API keys or tokens
- Deploy to any VPS or cloud

### What you'd skip:
- Local hub daemon and auto-start
- Unix socket / named pipe transport
- libp2p, DHT, NAT hole punching, magic codes
- Federation protocol (peer.conversation_sync)
- Shim process per agent
- delivery_state tracking (the server IS the single source of truth)

### Trade-offs:

| | Current (P2P) | Remote MCP server |
|---|---|---|
| **Setup** | `go install` + `claude mcp add` | Just `claude mcp add --url` |
| **Latency** | Local socket (~0ms) | Network round-trip |
| **Offline** | Works offline (local hub) | Needs internet |
| **Privacy** | All data local | Data on server |
| **Federation** | Hub-to-hub, no central point | Central server |
| **Complexity** | High (hub, shim, federation, sync) | Low (one web service) |
| **Reliability** | Complex (reconnect, queue, sync) | Simple (server handles it) |

## What actually matters

The value isn't in the transport — it's in the **conversation model**:
- Conversations as the unit of work
- Tasks as conversations with metadata
- Event-sourced state (append-only, replayable)
- Push notifications to agents (channel system)

Whether that runs on a local hub with P2P federation or a remote MCP server is an implementation detail. The agent experience is the same: `bifrost_send`, `bifrost_request_task`, `<channel>` notifications.

## Both can coexist

The local hub is great for:
- Solo developers on one machine (zero config)
- Privacy-sensitive work (everything local)
- Unreliable internet

The remote server is great for:
- Teams (shared server)
- CI agents (no local state)
- Simplicity (one URL, done)

Bifrost could support both — the shim connects to either a local hub or a remote server, same MCP tools either way.
