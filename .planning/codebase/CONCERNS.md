# Codebase Concerns

**Analysis Date:** 2026-03-30

## Tech Debt

**Synchronous SQLite in async server:**
- Issue: `Store` uses synchronous `sqlite3` calls from within an async FastMCP/Starlette server. Every DB call blocks the event loop.
- Files: `src/bifrost/store/db.py` (entire class, line 126-549)
- Impact: Under concurrent load, one slow query blocks all other requests. Single-writer SQLite + WAL mitigates reads but writes still serialize and block.
- Fix approach: Wrap Store calls in `asyncio.to_thread()` at the hub layer, or migrate to `aiosqlite`. The hub classes (`src/bifrost/hub/agents.py`, `src/bifrost/hub/tasks.py`, `src/bifrost/hub/conversations.py`, `src/bifrost/hub/delivery.py`) would need async method signatures.

**`check_same_thread=False` without locking:**
- Issue: SQLite connection created with `check_same_thread=False` (line 132 of `src/bifrost/store/db.py`) but no mutex or connection pool protects concurrent access. WAL mode helps for read concurrency but concurrent writes from multiple threads can corrupt.
- Files: `src/bifrost/store/db.py:132`
- Impact: Race conditions under concurrent MCP sessions writing simultaneously. Low risk currently (single-process, event-loop-serialized), but becomes dangerous if `asyncio.to_thread()` is added without a lock.
- Fix approach: Add a `threading.Lock` around write operations, or use a connection-per-thread pool.

**Per-operation commits:**
- Issue: Every write method calls `self._conn.commit()` individually (e.g., `upsert_agent`, `create_task`, `append_event`). No transaction batching.
- Files: `src/bifrost/store/db.py` (lines 156, 208, 246, 269, 315, 338, 375, 425, 443, 466, 480, 500, 530, 547)
- Impact: Multi-step operations (e.g., creating a conversation + appending its first event) are not atomic. A crash between steps leaves inconsistent state. Also hurts write throughput.
- Fix approach: Add context-manager transaction support (`with store.transaction():`) that defers commit until the block completes.

**Monkey-patching `_tool_manager.list_tools`:**
- Issue: `create_app()` patches an internal `_tool_manager.list_tools` method on the FastMCP instance to inject auto-registration logic.
- Files: `src/bifrost/app.py:115-134`
- Impact: Fragile coupling to FastMCP internals. Any FastMCP upgrade that changes `_tool_manager` breaks silently.
- Fix approach: Check if FastMCP provides lifecycle hooks or middleware for session initialization. If not, consider contributing upstream or using Starlette middleware instead.

**`session_agents` dict is in-memory only:**
- Issue: The `session_agents: dict[str, str]` mapping (session key to agent ID) lives only in process memory. Server restart loses all session bindings.
- Files: `src/bifrost/app.py:70`
- Impact: After restart, agents must re-introduce themselves. Access tokens in SQLite survive restart, but the session-to-agent binding does not. Not critical for MVP but surprising behavior.
- Fix approach: Persist session-agent bindings in SQLite, or reconstruct from OAuth subject on reconnect.

**`_new_id()` uses truncated UUIDs:**
- Issue: `_new_id()` returns `uuid.uuid4().hex[:12]` -- only 12 hex chars (48 bits of randomness).
- Files: `src/bifrost/store/models.py:17`
- Impact: Collision probability is non-trivial at scale (~1% at ~17M IDs via birthday paradox). For a communication hub with many events per conversation, event IDs could collide over time.
- Fix approach: Use full UUID hex (32 chars) or at minimum 16 chars (64 bits). The short IDs are nice for display but dangerous as primary keys.

**Artifact serialization uses `assert`:**
- Issue: `_artifact_to_dict()` uses `assert isinstance(a, Artifact)` for type checking, which is stripped in optimized mode (`python -O`).
- Files: `src/bifrost/store/db.py:565`
- Impact: Running with `-O` flag would silently pass invalid data through serialization.
- Fix approach: Replace with a proper `isinstance` check that raises `TypeError`.

## Known Issues

**Insecure mode allows only one agent:**
- Issue: In `--insecure` mode, all sessions share the key `"insecure"`, so `session_agents["insecure"]` can only hold one agent ID.
- Files: `src/bifrost/app.py:41-52`
- Impact: Only one agent can be active at a time in dev mode. The second `bifrost_introduce` call overwrites the first agent's session binding.
- Fix approach: Use per-connection state or generate a unique key per MCP session even in insecure mode.

**No cleanup of stale agents/data:**
- Issue: Agents that disconnect are never cleaned up. No TTL on conversations, events, or OAuth tokens. The database grows unboundedly.
- Files: `src/bifrost/store/db.py` (no cleanup methods exist), `src/bifrost/hub/agents.py`
- Impact: Long-running server accumulates dead agents, closed conversations, and expired tokens forever.
- Fix approach: Add periodic cleanup (expired OAuth tokens, old closed conversations, offline agents past TTL). Could be a background task or on-demand during `bifrost_check`.

**Channel conversations create new conversation each time:**
- Issue: `ConversationHub.send()` with a `channel` parameter always creates a new conversation (line 47-55 of `src/bifrost/hub/conversations.py`). There is no reuse of existing channel conversations.
- Files: `src/bifrost/hub/conversations.py:46-55`
- Impact: Each channel message creates a separate conversation. Subscribers see them individually rather than as a threaded channel. This may be intentional but diverges from typical channel semantics.
- Fix approach: Consider reusing a single conversation per channel name, or document this as intentional "broadcast" semantics.

## Security Considerations

**No authorization on task operations:**
- Risk: Any agent can update any task's status, not just the assignee or requester. `bifrost_update_task` only checks that the caller is introduced, not that they own the task.
- Files: `src/bifrost/app.py:284-304`, `src/bifrost/mcp/tools.py:162-185`
- Current mitigation: None. The `_get_session_agent()` call on line 294 ensures the caller is registered but does not check ownership.
- Recommendations: Validate that the caller is either the task's `requester` or `assignee` before allowing status updates. The requester should be able to cancel; the assignee should be able to accept/reject/complete/fail.

**No authorization on conversation access:**
- Risk: Any agent can send messages to any conversation by ID, and `bifrost_send` auto-adds the sender as a participant.
- Files: `src/bifrost/hub/conversations.py:38-45`
- Current mitigation: None. Line 43-45 silently adds uninvited agents to conversations.
- Recommendations: Validate that the sender is already a participant (for existing conversations) or is explicitly invited.

**No input validation on agent names:**
- Risk: Agent names are not validated for length, characters, or format. An agent could register with an extremely long name, empty-ish name, or name containing control characters.
- Files: `src/bifrost/hub/agents.py:22-44`, `src/bifrost/app.py:225`
- Current mitigation: Only the `name` emptiness check in `bifrost_introduce` (line 225-226 of `src/bifrost/app.py`).
- Recommendations: Validate name length (e.g., 1-64 chars), allowed characters (alphanumeric, hyphens, underscores), and reject names starting with "unnamed".

**No input size limits on message bodies:**
- Risk: `bifrost_send` accepts arbitrarily large `body` strings that are stored in SQLite as JSON blobs.
- Files: `src/bifrost/app.py:329-345`, `src/bifrost/store/db.py:369-376`
- Current mitigation: None.
- Recommendations: Enforce a maximum body size (e.g., 64KB) at the tool handler layer.

**OAuth tokens stored as plaintext:**
- Risk: Access tokens and refresh tokens are stored as plaintext in SQLite.
- Files: `src/bifrost/store/db.py:474-516` (oauth_access_tokens, oauth_refresh_tokens tables)
- Current mitigation: Database file permissions. Tokens are random `secrets.token_urlsafe(32)` values.
- Recommendations: Consider hashing tokens (store hash, compare hash on lookup). For MVP this is acceptable since the DB is local.

**No OIDC discovery:**
- Risk: The OAuth provider hardcodes OIDC endpoint paths (e.g., `{issuer}/auth`, `{issuer}/token`) rather than using OIDC discovery (`/.well-known/openid-configuration`).
- Files: `src/bifrost/auth/oauth.py:91`, `src/bifrost/auth/oauth.py:106`
- Current mitigation: Works with Dex which uses these paths. Would break with providers that use different endpoint paths.
- Recommendations: Fetch and cache the OIDC discovery document at startup.

## Performance Concerns

**`DeliveryHub.check()` scans all conversations:**
- Problem: `check()` loads ALL conversations to find ones where the agent is a participant, then loads ALL subscriptions, then loads ALL matching channel conversations.
- Files: `src/bifrost/hub/delivery.py:15-61`
- Cause: No index on conversation participants (stored as JSON array in a TEXT column). Every `check()` call is O(total_conversations).
- Improvement path: Add an `agent_conversations` junction table mapping agent_id to conversation_id. Query directly instead of scanning all conversations.

**N+1 query pattern in `AgentHub.list_all()`:**
- Problem: Lists all agents, then issues one `get_card()` query per agent.
- Files: `src/bifrost/hub/agents.py:150-155`
- Cause: No JOIN query available in the Store layer.
- Improvement path: Add a `list_agents_with_cards()` Store method that JOINs agents and agent_cards in a single query.

**No pagination on list operations:**
- Problem: `list_agents()`, `list_tasks()`, `list_conversations()`, `list_events()` all return full result sets with no LIMIT/OFFSET.
- Files: `src/bifrost/store/db.py:166-171`, `src/bifrost/store/db.py:271-290`, `src/bifrost/store/db.py:340-354`, `src/bifrost/store/db.py:378-397`
- Cause: Not implemented yet.
- Improvement path: Add `limit` and `offset` parameters to all list methods. The MCP tool handlers should default to reasonable limits (e.g., 50 items).

## Missing Features

**No agent disconnect detection:**
- Problem: There is no mechanism to detect when an agent's MCP session ends and mark it offline. `AgentHub.disconnect()` exists but is never called.
- Files: `src/bifrost/hub/agents.py:58-65` (method exists), `src/bifrost/app.py` (never invoked)
- Blocks: Accurate agent status display. Currently all agents stay "online" forever once introduced.

**No structured logging:**
- Problem: Only one logging call exists in the entire codebase (a single `warning` in the auto-registration wrapper). No request logging, no audit trail.
- Files: `src/bifrost/app.py:130-131`
- Blocks: Debugging production issues, auditing agent actions, monitoring.

**Dashboard is empty:**
- Problem: `src/bifrost/dashboard/__init__.py` exists but is empty. No web UI.
- Files: `src/bifrost/dashboard/__init__.py`
- Blocks: Visual monitoring of agents, conversations, and tasks.

**No conversation close mechanism exposed:**
- Problem: The `Conversation` model has `closed` and `closed_reason` fields, and `update_conversation()` supports updating them, but no MCP tool exposes this functionality.
- Files: `src/bifrost/store/models.py:149-150`, `src/bifrost/store/db.py:321-338`
- Blocks: Agents cannot close completed conversations.

**No unsubscribe tool:**
- Problem: `ConversationHub.unsubscribe()` exists but no `bifrost_unsubscribe` MCP tool is registered.
- Files: `src/bifrost/hub/conversations.py:94-96` (method exists), `src/bifrost/app.py` (no tool)
- Blocks: Agents cannot unsubscribe from channels.

**No task notification system:**
- Problem: When a task is created or updated, the assignee/requester is not notified. They must poll via `bifrost_check` or `bifrost_list_tasks`.
- Files: `src/bifrost/hub/tasks.py`, `src/bifrost/mcp/tools.py`
- Blocks: Real-time task awareness. Agents must repeatedly poll to discover task assignments.

## Test Coverage Gaps

**No OAuth/auth tests:**
- What's not tested: The entire `src/bifrost/auth/oauth.py` (252 lines) has zero test coverage. No tests for token exchange, refresh, expiry, callback handling, or client registration.
- Files: `src/bifrost/auth/oauth.py`
- Risk: Auth bugs would only be caught in production. Token expiry edge cases, invalid state handling, and OIDC provider errors are untested.
- Priority: High

**No tests for concurrent access:**
- What's not tested: Multiple agents operating simultaneously, race conditions on shared state.
- Files: `src/bifrost/store/db.py`, `src/bifrost/app.py`
- Risk: The `check_same_thread=False` SQLite setup and in-memory `session_agents` dict are never stress-tested.
- Priority: Medium

**No tests for edge cases in delivery:**
- What's not tested: `DeliveryHub.check()` with many conversations, empty conversations, conversations where `after_event_id` references a deleted event.
- Files: `src/bifrost/hub/delivery.py`, `tests/test_hub_delivery.py` (96 lines)
- Risk: Delivery edge cases could silently drop messages.
- Priority: Medium

**No error-path tests for MCP tool layer:**
- What's not tested: The `except (ValueError, KeyError) as e: return f"Error: {e}"` handlers in `src/bifrost/app.py` are not tested. No tests verify that invalid inputs produce proper error strings rather than unhandled exceptions.
- Files: `src/bifrost/app.py:238-239`, `src/bifrost/app.py:254-255`, etc.
- Risk: Unhandled exception types would crash the MCP session instead of returning error strings.
- Priority: Low

## Dependency Risks

**`mcp>=1.9.0` with no upper bound:**
- Risk: The `mcp` package is pre-1.0-stable and the codebase patches its internals (`_tool_manager.list_tools`). A breaking change in `mcp` 2.x could break Bifrost silently.
- Files: `pyproject.toml:10`, `src/bifrost/app.py:115-134`
- Impact: Build or runtime failure on `pip install` if a breaking mcp version is released.
- Migration plan: Pin to `mcp>=1.9.0,<2.0.0` until the internal patching is replaced with a supported API.

**`fastapi>=0.115.0` and `jinja2>=3.1.0` declared but Jinja2 unused:**
- Risk: `jinja2` is listed as a dependency but never imported anywhere in the codebase. Likely intended for the dashboard.
- Files: `pyproject.toml:13`
- Impact: Unnecessary dependency. Minor supply-chain risk.
- Migration plan: Remove until dashboard is implemented, or move to optional dependencies.

**No lockfile committed:**
- Risk: No `requirements.txt` lock or `pip-compile` output is committed. `pip install -e .` resolves dependencies at install time with no reproducibility guarantee.
- Files: `pyproject.toml` (only source of dependency info)
- Impact: Different installs may get different dependency versions. CI and prod environments may diverge.
- Migration plan: Add `pip-compile` (pip-tools) or `uv lock` to generate a locked requirements file.

---

*Concerns audit: 2026-03-30*
