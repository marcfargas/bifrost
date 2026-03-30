# Agent Orchestration Landscape Research (March 2026)

Research conducted during a blegal-dev infrastructure planning session. This captures findings relevant to bifrost's evolution — the competitive landscape, architectural patterns worth borrowing, protocol standards, and the gap bifrost should fill.

## The Problem We're Solving

5-10+ Claude Code agents running on a single server (nexus.blegal.dev), managed manually via SSH + tmux. No visibility, no coordination, no way to scale. Need:

1. **Visibility** — what agents are running, what are they doing, what did they produce
2. **Coordination** — agents messaging each other, task delegation, dependency tracking
3. **Both interactive and autonomous** workflows — sometimes you brainstorm with an agent (Mayor/CTO pattern), sometimes you fire-and-forget tasks

## Why Existing Tools Don't Fit

### Paperclip (paperclipai/paperclip)
- **What it is:** Node.js + Postgres + React orchestrator. Web dashboard, task management, agent registry, REST API, budget enforcement.
- **Adapter model:** `claude_local` process adapter spawns Claude Code as child process. Full lifecycle: skills symlinking, streaming stdout/stderr, SIGTERM/SIGKILL, session state persistence.
- **Heartbeat model:** Agents are dormant, wake on schedule/trigger, do work, go dormant. Not interactive.
- **Skills:** Portable knowledge modules (Markdown/JSON), symlinked per run. "Omega architecture" — shared across tools.
- **API:** REST at localhost:3100. CRUD for companies/agents/tasks. Cost tracking. Inbound webhooks (custom headers, incompatible with GitHub). No outbound webhooks.
- **Why rejected:** Heartbeat model is fundamentally non-interactive. Can't have a strategic conversation with an agent through Paperclip. Forces a split between "Mayor" (interactive, outside Paperclip) and "fleet" (Paperclip-managed), which is artificial. Also: Docker deployment can't easily spawn host processes (process adapter is local-only, HTTP adapter doesn't handle full lifecycle).

### Gas Town (steveyegge/gastown)
- **What it is:** Go binary, sophisticated multi-agent orchestrator. Mayor coordinates, Polecats execute, Witness monitors, Refinery merges.
- **Best ideas:**
  - **Beads** — machine-optimized task tracking in JSONL+Git. Topological sorting serves only "ready" work. Agents never see the full dependency graph.
  - **External state as source of truth** — "local state is fog, remote state is concrete." Agents are stateless, can crash and resume from beads.
  - **Structural constraints** — worktrees prevent file conflicts structurally, topological sorting prevents dependency violations structurally. Don't rely on prompts for correctness.
  - **Operational roles** (not persona theater) — each role has clear input/output contracts.
  - **Session-per-step** — agents complete one unit of work, handoff captures context for next session.
  - **Multi-model routing** — expensive models for coordination, cheap ones for execution.
- **Why rejected:** Local-only ("snow globe"), no web UI, tmux-only, immature (force pushes to recover, "murderous rampaging Deacon"), expensive ($100/hr at peak), high adoption bar.

### Codeman (Ark0N/Codeman)
- **What it is:** Per-session terminal dashboard with xterm.js. Token/cost tracking. Mobile-friendly.
- **Why rejected:** Visibility only, no coordination, no task management.

### Freshell (danshapiro/freshell)
- **What it is:** Workspace/tmux automation. Agents can split panes programmatically.
- **Why rejected:** Infrastructure layer only.

### Ruflo (ruvnet/ruflo) — 28k stars
- **What it is:** Enterprise AI orchestration platform for Claude Code. 100+ specialized agents, swarm coordination (mesh/hierarchical/ring/star), consensus protocols (Raft, BFT, Gossip, CRDT), self-learning (SONA, RL, vector memory), multi-provider (Claude/GPT/Gemini/Ollama), smart model routing (WASM for simple, Haiku for medium, Opus for complex), 310+ MCP tools, plugin system.
- **Key strengths:**
  - **Single primary session orchestrates everything** — hierarchical topology with lead session coordinating agents. Built-in Mayor pattern.
  - **Remote MCP server** — can run as MCP server that multiple Claude Code sessions connect to. WebSocket support.
  - **Multi-project** — one instance manages agents across repos via tmux session naming with directory hashes.
  - **Solo dev workflow documented** — self-coordination pattern, not just enterprise.
  - **Smart routing** — Q-Learning router picks models by task complexity, extends Claude subscription by ~250%.
  - **Session persistence** — context survives across conversations.
  - **Background daemons** — auto-runs audits, optimization, learning.
- **Web dashboard:** Not built-in. **OctoAlly** (github.com/ai-genius-automations/octoally) is a third-party web dashboard for Ruflo: live session grid, hive-mind orchestration view, streaming output, session persistence, git diffs, pop-out terminals.
- **Gaps:** Massive complexity (310 MCP tools, WASM kernels, RL algorithms). No A2A protocol. No built-in web UI. Potentially overengineered for small-scale use, but the most flexible option by far.
- **Assessment:** Most capable orchestrator evaluated. The primary-session model + remote MCP + multi-project support is exactly the architecture we need. The question is whether the complexity overhead is worth it vs. lighter alternatives.

### oh-my-claudecode (Yeachan-Heo/oh-my-claudecode) — 17k stars
- **What it is:** Claude Code plugin adding multi-agent orchestration. Zero new infrastructure.
- **Key strengths:**
  - **Team mode** — staged pipeline: plan → PRD → execute → verify → fix loop. Uses Claude Code native Agent Teams.
  - **Multi-provider workers** — spawns Claude, Codex, Gemini CLI workers as tmux panes.
  - **32 specialized agents** with smart model routing (Haiku simple, Opus complex).
  - **HUD statusline** — real-time orchestration visibility in terminal.
  - **Skills system** — auto-extracted reusable patterns, auto-injected when relevant.
  - **Notifications** — Telegram, Discord, Slack callbacks on session completion.
  - **Zero install friction** — Claude Code plugin marketplace or npx.
- **Gaps:** No web dashboard (terminal/HUD only). No A2A. No cross-machine. No persistent task board across sessions.
- **Assessment:** Closest to "just works" for adding orchestration to existing Claude Code workflow. No new services to deploy. But limited to terminal visibility and same-machine coordination.

### Conductor (Melty Labs)
- macOS only. Visual dashboard + worktree isolation for 3-8 parallel agents.

### Vibe Kanban
- Cross-platform Kanban workflow for multi-agent. Unclear remote story.

## Big Players (March 2026)

**None are building distributed/cross-network agent orchestration.** All optimizing for local, single-machine.

| Platform | Multi-agent | Cross-network | Dashboard |
|----------|------------|---------------|-----------|
| Claude Code Agent Teams | Yes (team lead + teammates, shared tasks) | No — local only (tmux/iTerm2) | Terminal only |
| Claude Code Channels | Single session interface (Telegram/Discord/iMessage) | Remote interface, not orchestration | No |
| Claude Desktop Dispatch | Background agents | Unclear | Unclear |
| Cursor | Up to 8 parallel | No | IDE |
| GitHub Copilot Cowork | Yes (Anthropic tech) | No | IDE |

## Protocol Landscape

| Protocol | Purpose | Agent-to-agent? |
|----------|---------|-----------------|
| **MCP** | Agent-to-tool. Standardizes how agents access external capabilities. Works over network (HTTP/SSE). | No — tools, not peers. But bifrost uses MCP as the transport. |
| **A2A** (Google) | Agent-to-agent. Task lifecycle, capability discovery, Agent Cards. OAuth auth. | Yes — but nascent for Claude Code. |
| **ACP** (Zed/JetBrains) | Agent-to-editor. JSON-RPC session management. | No. |

**Key insight from MCP docs:** "Using MCP where A2A is the correct abstraction produces systems where sub-agents cannot maintain their own state, authentication context, or task lifecycle." Bifrost uses MCP as transport but implements A2A-like semantics on top — this is the right approach.

## Patterns to Borrow

### From Gas Town
- **Beads / external task state** — tasks as structured data (not just prompts), with dependency tracking and topological sorting. Agents only see actionable work.
- **Structural constraints** — encode rules in the system, not in prompts. Worktree isolation, dependency enforcement, budget limits.
- **Session handoff** — when agent finishes or crashes, context is captured in the task/conversation. Next agent resumes from there.
- **Multi-model routing** — coordinate with expensive models, execute with cheap ones.

### From Paperclip
- **Web dashboard** — React UI for visibility into agents, tasks, activity.
- **Budget enforcement** — 80% warning, 100% hard stop per agent.
- **Skills/instructions** — portable knowledge modules injected at runtime.
- **Routines** — scheduled/webhook-triggered task creation.
- **REST API** — programmatic access to everything the dashboard shows.

### From the protocol ecosystem
- **A2A patterns** — Agent Cards for capability discovery, task lifecycle states, streaming updates via SSE.
- **Conversation-event model** — (bifrost already has this) append-only event streams as the unit of coordination.

## The Gap Bifrost Should Fill

No existing tool provides:
1. **MCP-native** agent communication (agents connect by adding an MCP server, nothing else)
2. **Both interactive and autonomous** support (same system for Mayor conversations and fleet tasks)
3. **Web dashboard** for visibility
4. **Cross-network** when needed (MCP over HTTP, agents on different machines)
5. **Lightweight** — no Postgres required for basic use, no heavy framework

Bifrost v2 as a remote MCP server + web dashboard layer is the right architecture. The v2 remote MCP server doc already captures most of this — this research validates the direction and adds the observability/dashboard requirement.

## Architecture Direction

```
Claude Code sessions (any machine)
    │
    │ MCP Streamable HTTP
    ↓
┌─────────────────────────┐
│  Bifrost MCP Server     │
│  (Python/FastAPI)       │
│  - Agent registry       │
│  - Conversations/events │
│  - Task lifecycle       │
│  - SSE push (channels)  │
│  - OAuth 2.0 auth       │
└────────┬────────────────┘
         │
         ↓
┌─────────────────────────┐
│  Web Dashboard          │
│  (React or similar)     │
│  agents.blegal.dev      │
│  - Agent status/list    │
│  - Task board           │
│  - Activity feed        │
│  - Conversation viewer  │
└─────────────────────────┘
```

Both the MCP server and dashboard read/write the same data store. The dashboard is a read-heavy view layer; agents interact through MCP tools.

## A2A Ecosystem (March 2026)

### Key insight: A2A + MCP, not A2A vs MCP

- **MCP** = agent-to-tool (how Claude Code accesses capabilities)
- **A2A** = agent-to-agent (how agents discover, delegate, and communicate)
- Bifrost should implement A2A as the protocol, with MCP as one transport for Claude Code agents

### Existing A2A implementations

| Project | URL | What it does |
|---------|-----|-------------|
| **a2a-python** | github.com/a2aproject/a2a-python | Google's official Python SDK. Agent Cards, task lifecycle, SSE. |
| **claude-a2a** | github.com/jcwatson11/claude-a2a | **Most relevant.** TypeScript A2A server wrapping Claude Code CLI. Exposes local `claude` as a network A2A service. Includes MCP client for Claude Code to call remote agents. |
| **A2A-MCP-Server** | github.com/GongRzhe/A2A-MCP-Server | MCP server bridging to A2A agents. Claude/MCP clients talk to A2A network. |
| **MCP_A2A** | github.com/regismesquita/MCP_A2A | Lightweight Python MCP-to-A2A bridge for Claude Desktop. |
| **a2a-mcp-with-security** | github.com/vishalmysore/a2a-mcp-with-security | Spring-based A2A+MCP with RBAC and web dashboard demo. |
| **mcp-agentic-mesh** | github.com/vishalmysore/mcp-agentic-mesh | Multi-server A2A+MCP mesh for distributed agents. |

### claude-a2a deep dive (most relevant to bifrost)

`jcwatson11/claude-a2a` does almost exactly what bifrost v2 needs:

**Architecture:**
- Express server + @a2a-js/sdk (A2A v0.3.0)
- Spawns long-lived `claude` CLI process per session (`--input-format stream-json --output-format stream-json`)
- NDJSON stdin/stdout for message passing
- Session continuity via kept-alive process + `--resume` for recovery
- Agent Card at `/.well-known/agent-card.json`

**Features:**
- Multi-agent configs (each agent = named config with model, work_dir, system prompt, permissions)
- A2A JSON-RPC + REST transports
- MCP client so interactive Claude Code sessions can call remote agents
- Auth: master key + JWT tokens with per-agent scopes
- Rate limiting (token-bucket, per-client)
- Budget tracking (daily limits, per-client, per-invocation max)
- Cost tracking per response (metadata.claude field)
- Multimodal input (images, PDFs via A2A FilePart)
- Session management (idle timeout, max lifetime, per-client limits)
- `npx claude-a2a-cli serve` — zero-install deployment

**What it's missing (bifrost opportunity):**
- **No web dashboard** — no visibility into running agents, tasks, activity
- **No observability layer** — can see agent cards and send messages, but no "what's happening across my fleet" view
- **No persistent task board** — A2A tasks exist during execution, no historical view
- **No channels/broadcast** — point-to-point only
- **No push notifications to agents** — A2A is request/response; no way to inject messages into an active Claude Code session (acknowledged in their README as a limitation)

### Implications for bifrost v2

**Option A: Build on claude-a2a**
- Fork or wrap claude-a2a, add the dashboard/observability layer
- TypeScript (matches the existing codebase's ecosystem)
- Already solves: agent spawning, A2A protocol, auth, budgets, multi-agent config
- We add: web dashboard, persistent task board, activity feed, channels

**Option B: Build bifrost v2 from scratch using a2a-python SDK**
- Python/FastAPI as originally planned in v2 concept doc
- Use Google's official SDK for A2A protocol compliance
- Build everything else ourselves
- More work but more control

**Option C: Hybrid — bifrost as the orchestration/dashboard layer, claude-a2a as the agent runtime**
- claude-a2a handles spawning and A2A protocol per agent
- Bifrost sits above as the fleet manager: dashboard, task board, cross-agent coordination
- Bifrost talks to claude-a2a instances via A2A protocol
- Clean separation of concerns

## Revised Architecture Direction

```
                    Web Dashboard (agents.blegal.dev)
                           │
                           ↓
                  ┌──────────────────┐
                  │  Bifrost Hub     │
                  │  - Agent registry│
                  │  - Task board    │
                  │  - Activity feed │
                  │  - Conversations │
                  └────────┬─────────┘
                           │ A2A protocol
                           ↓
              ┌────────────────────────┐
              │  claude-a2a server     │
              │  (per-machine or       │
              │   single instance)     │
              │  - Agent Cards         │
              │  - Claude CLI spawning │
              │  - Auth + budgets      │
              │  - Session management  │
              └────────────────────────┘
                           │
                           ↓
                    Claude Code CLI
                    (host processes)
```

OR simpler: merge both into one service.

## OctoAlly (ai-genius-automations/octoally) — 67 stars, created 2026-03-12

Web dashboard for Claude Code + Ruflo. React + Fastify + SQLite, local-first.

**Features:** Live session grid, hive-mind orchestration view, streaming output, interactive terminals (tmux-backed, pop-out/adopt-back), git source control (diffs, staging, commits), per-project agent configs, session persistence, voice dictation (local Whisper), dual CLI support (Claude + Codex).

**Architecture:** React 19 + Vite frontend, Fastify + SQLite backend, node-pty + tmux for sessions, WebSocket for streaming.

**Install:** `npx octoally@latest` — installs, starts server, launches dashboard at localhost:42010. `octoally install-service` for systemd.

**Integration with Ruflo:** OctoAlly initializes projects with Ruflo config and agent definitions. Launches hive-mind sessions via Ruflo. But maintained by different org (ai-genius-automations vs ruvnet) — integration depth unverified.

**Risk assessment:** 18 days old at time of evaluation. Prototype maturity. Unknown developer commitment. No community, no LTS, no security audit. Terminal pop-out feature is a security surface if auth is bypassed.

## Adversarial Review (against Ruflo + OctoAlly adoption)

An adversarial review was conducted against the initial proposal to adopt Ruflo + OctoAlly. Key findings:

### Critical gaps
- **Coordination not solved.** Neither Ruflo nor OctoAlly provides structured agent-to-agent messaging or task delegation with dependency tracking. Ruflo's swarm consensus (Raft, BFT) solves distributed systems coordination, not "Agent A needs types from Agent B's repo."
- **OctoAlly is a session grid, not a task board.** No persistent task lifecycle across sessions, no dependency awareness.
- **Interactive session quality unverified.** Can you have a real conversation through OctoAlly's web terminal, or is it read-only with input?

### Maturity risks
- **OctoAlly: 18 days old, 67 stars.** Not production infrastructure.
- **Ruflo: 28k stars but massive surface area.** 310 MCP tools, WASM, RL — single primary maintainer. Debugging RL reward functions is not "just works."
- **Integration assumed, not verified.** No evidence anyone runs OctoAlly + Ruflo together in production.
- **No fallback plan.** When OctoAlly ships a breaking change (inevitable for an 18-day-old project), what do you revert to?

### Complexity vs. need
- Ruflo's 310 MCP tools, consensus protocols, and self-learning system are enterprise-scale for a solo dev running 5-10 agents.
- "Just works" in the requirements doc → Ruflo is the opposite of "just works."

### Security concerns
- OctoAlly exposes a web dashboard with terminal access. Behind OAuth, but no security audit.
- Both tools run as `marc` with full access to all projects, git credentials, API keys.

### What was nearly overlooked
- **oh-my-claudecode** solves within-project orchestration with zero infrastructure but is per-project, not cross-project fleet management.
- **claude-a2a** provides a clean A2A agent runtime that could be the foundation for a lighter build.
- **A proof-of-concept was never done.** The proposal was heading to deployment without installing either tool.

## Conclusions

### The missing piece nobody has built

A **lightweight fleet dashboard** that:
1. Shows all agents across all projects (cross-project visibility)
2. Lets you launch/stop agents from one place (lifecycle management)
3. Enables agents to communicate across projects (coordination)

Every tool evaluated solves 1-2 of these but not all 3. The tools that attempt all 3 (Ruflo + OctoAlly) are either too immature or too complex.

### Realistic paths forward

**Path A: Accept the pain, use what works today.**
SSH + tmux + manual management. Possibly add oh-my-claudecode for within-project orchestration. Revisit in Q3 2026 when the landscape matures. Zero risk, zero new infrastructure.

**Path B: Build the missing dashboard layer.**
A lightweight web UI on nexus that reads tmux sessions, shows agent status, and manages launch/stop. This is a focused, small-scope project — not a full orchestration platform. Bifrost v1 already handles agent-to-agent communication. The dashboard is the only missing piece.

**Path C: Build on claude-a2a.**
Use claude-a2a as the agent runtime (A2A protocol, agent spawning, auth, budgets). Build a thin dashboard/fleet-manager on top (bifrost v2 as the hub). More work than Path B, but gives you A2A compatibility and a proper agent lifecycle.

**Path D: Adopt Ruflo + OctoAlly with a proof-of-concept first.**
Install both on nexus, verify integration, verify WebSocket proxying through Traefik, verify interactive terminal quality. Only proceed to "production" if the PoC works. High potential payoff if it works, high risk if it doesn't.

### Recommendation

**Path A now, Path C when ready to build.** Use the current setup (SSH + tmux) while the landscape matures. When time permits, build bifrost v2 as a thin layer over claude-a2a — focused on the dashboard and fleet management that nothing else provides. Keep the scope minimal: agent registry, session status, task board, web UI. Don't build consensus protocols, RL routing, or WASM kernels.

## Open Questions for Bifrost v2

1. **Build on claude-a2a or build from scratch?** — claude-a2a solves agent spawning + A2A protocol but is TypeScript/Express. Bifrost v2 concept doc said Python/FastAPI.
2. **One service or two?** — Bifrost hub + claude-a2a as separate services, or merge into one?
3. **Dashboard tech** — React? Something lighter?
4. **Push notifications** — claude-a2a acknowledges no way to inject messages into active Claude Code sessions. Bifrost v1 solved this via channels. How to bring that to v2?
5. **Coexistence with v1?** — v1 (Go, local hub) still works. Clean break or migration path?
6. **LiteLLM integration** — Pull cost data from LiteLLM? claude-a2a already tracks per-invocation cost.
7. **Scope discipline** — The biggest risk is scope creep. The missing piece is a dashboard + fleet manager, not an orchestration platform.
