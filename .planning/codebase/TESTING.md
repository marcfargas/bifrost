# Testing Patterns

**Analysis Date:** 2026-03-30

## Test Framework

**Runner:**
- pytest >= 8.0
- Config: `[tool.pytest.ini_options]` in `pyproject.toml`

**Async Support:**
- pytest-asyncio >= 0.25.0
- `asyncio_mode = "auto"` — async test functions run automatically without `@pytest.mark.asyncio`

**HTTP Testing:**
- httpx >= 0.28.0 — `AsyncClient` with `ASGITransport` for ASGI app testing
- asgi-lifespan >= 2.1.0 — `LifespanManager` for startup/shutdown lifecycle

**Run Commands:**
```bash
pytest tests/              # Run all tests
pytest tests/ -q           # Quiet output
pytest tests/ -x           # Stop on first failure
pytest tests/test_db.py    # Run specific test file
```

## Test File Organization

**Location:**
- All tests in top-level `tests/` directory (separate from source)
- `tests/__init__.py` — empty, makes tests importable
- `tests/conftest.py` — empty (fixtures defined per-file)

**Naming:**
- Files: `test_{module}.py` — maps to source module
- Test files map to source layers:

| Test File | Source Under Test |
|---|---|
| `tests/test_models.py` | `src/bifrost/store/models.py` |
| `tests/test_db.py` | `src/bifrost/store/db.py` |
| `tests/test_hub_agents.py` | `src/bifrost/hub/agents.py` |
| `tests/test_hub_tasks.py` | `src/bifrost/hub/tasks.py` |
| `tests/test_hub_conversations.py` | `src/bifrost/hub/conversations.py` |
| `tests/test_hub_delivery.py` | `src/bifrost/hub/delivery.py` |
| `tests/test_tools.py` | `src/bifrost/mcp/tools.py` |
| `tests/test_app.py` | `src/bifrost/app.py` + `src/bifrost/config.py` |
| `tests/test_integration.py` | Full stack through `ToolHandlers` |

## Test Structure

**Suite Organization:**
- Group related tests in classes: `class TestRegister:`, `class TestDisconnect:`, `class TestUpdateStatus:`
- Class names: `Test` + feature/method being tested
- Method names: `test_` + behavior description: `test_new_agent`, `test_reconnect`, `test_missing_agent`
- No inheritance between test classes

**Pattern — Hub Unit Test:**
```python
class TestCreate:
    def test_basic(self, hub):
        task = hub.create(requester="a1", assignee="a2")
        assert task.requester == "a1"
        assert task.assignee == "a2"
        assert task.status == TaskStatus.QUEUED

    def test_with_metadata(self, hub):
        task = hub.create(requester="a1", assignee="a2", metadata={"priority": "high"})
        assert task.metadata == {"priority": "high"}
```

**Pattern — Error Case Testing:**
```python
def test_disconnect_missing(self, hub):
    with pytest.raises(KeyError):
        hub.disconnect("nope")

def test_queued_to_completed_invalid(self, hub):
    t = hub.create(requester="a1", assignee="a2")
    with pytest.raises(InvalidTransition):
        hub.update_status(t.id, TaskStatus.COMPLETED)
```

**Pattern — Async HTTP Test (test_app.py):**
```python
class TestHealth:
    async def test_health_returns_200(self, client):
        resp = await client.get("/health")
        assert resp.status_code == 200
        assert resp.json() == {"status": "ok"}
```

**Pattern — Integration Test (standalone functions):**
```python
def test_two_agents_message_exchange(handlers):
    alice = handlers._agents.register("alice")
    bob = handlers._agents.register("bob")

    handlers.handle_send(alice.id, to="bob", body="Hey Bob, how are you?")
    result = handlers.handle_check(bob.id)
    assert "Hey Bob, how are you?" in result

    result2 = handlers.handle_check(bob.id)
    assert "No pending" in result2
```

## Fixtures

**Database Fixture (most common):**
```python
@pytest.fixture
def store(tmp_path):
    s = Store(tmp_path / "test.db")
    yield s
    s.close()
```
- Uses `tmp_path` (pytest builtin) for isolated SQLite databases
- `yield` pattern for cleanup (close DB connection)
- Every test gets a fresh database

**Hub Fixture:**
```python
@pytest.fixture
def hub(tmp_path):
    store = Store(tmp_path / "test.db")
    yield AgentHub(store)
    store.close()
```
- Creates Store + Hub together, yields the hub
- Store cleanup via `store.close()` in teardown

**Composite Fixture (test_tools.py):**
```python
@pytest.fixture()
def hubs(store):
    return {
        "agents": AgentHub(store),
        "tasks": TaskHub(store),
        "conversations": ConversationHub(store),
        "delivery": DeliveryHub(store),
    }

@pytest.fixture()
def handlers(hubs):
    return ToolHandlers(**hubs)

@pytest.fixture()
def alice(hubs):
    return hubs["agents"].register("alice")

@pytest.fixture()
def bob(hubs):
    return hubs["agents"].register("bob")
```
- Layered fixtures: `store` -> `hubs` -> `handlers` + `alice`/`bob`
- Named agent fixtures for readability

**Async ASGI Fixture (test_app.py):**
```python
@pytest.fixture()
async def client(mcp_app):
    starlette_app = mcp_app.streamable_http_app()
    async with LifespanManager(starlette_app) as manager:
        transport = ASGITransport(app=manager.app)
        async with AsyncClient(transport=transport, base_url="http://test") as c:
            yield c
```
- Uses `LifespanManager` to manage app startup/shutdown
- `ASGITransport` for in-process HTTP testing (no real server)
- Base URL `http://test` — arbitrary, never hits network

## Mocking

**Approach:** No mocking framework used. Tests use real implementations with in-memory SQLite databases.

**What gets tested directly (no mocks):**
- Store layer: real SQLite via `tmp_path`
- Hub layer: real Store + real Hub classes
- Tool handlers: real Hubs wired together
- App factory: real FastMCP app via ASGI transport

**What is NOT mocked:**
- Database operations — always test against real SQLite
- Hub business logic — always real
- Inter-hub interactions — always real

**Implication:** All tests below the app layer are effectively integration tests against real SQLite. There are no mock objects or `unittest.mock` usage anywhere in the test suite.

## Test Types

**Model Tests (`tests/test_models.py`):**
- Verify dataclass defaults, field types, enum values
- No database or hub interaction
- Pure unit tests

**Store Tests (`tests/test_db.py`):**
- CRUD operations against real SQLite
- Serialization roundtrips (JSON fields: skills, artifacts, participants)
- Query filtering (by status, requester, assignee)
- Edge cases: missing records return `None`, invalid field updates raise `ValueError`

**Hub Tests (`tests/test_hub_*.py`):**
- Business logic: registration, status transitions, conversation creation
- State machine validation (task transitions)
- Error cases: missing entities, invalid operations
- Each hub tested in isolation with its own Store

**Tool Handler Tests (`tests/test_tools.py`):**
- All 11 MCP tool handlers tested
- String output assertions: `assert "alice" in result`, `assert "queued" in result`
- Error propagation: `pytest.raises(KeyError)`, `pytest.raises(InvalidTransition)`
- Tests use the handler return strings, not the underlying data

**App Tests (`tests/test_app.py`):**
- HTTP endpoint testing via `httpx.AsyncClient`
- Tool registration verification (all 11 tools present)
- Config dataclass defaults
- App factory smoke test

**Integration Tests (`tests/test_integration.py`):**
- 9 end-to-end scenarios through `ToolHandlers`
- Full lifecycle flows: message exchange, task lifecycle, channel broadcast
- Multi-agent interactions: DM, channels, subscriptions
- Conversation reuse and delivery state tracking
- Tests use standalone functions (not test classes)

## Assertion Patterns

**Direct equality:**
```python
assert agent.name == "bot-1"
assert agent.status == AgentStatus.ONLINE
```

**String containment (for MCP tool output):**
```python
assert "alice" in result
assert "queued" in result
assert "No pending" in result
```

**Collection checks:**
```python
assert len(agents) == 2
assert len(got.artifacts) == 1
assert senders == {"a1", "a3"}
```

**None/identity checks:**
```python
assert got.card is None
assert agent.connected_at is not None
assert t1.artifacts is not t2.artifacts
```

**Exception assertions:**
```python
with pytest.raises(KeyError):
    hub.get("nope")

with pytest.raises(InvalidTransition):
    hub.update_status(t.id, TaskStatus.COMPLETED)

with pytest.raises(ValueError, match="Cannot update field"):
    store.update_task("t1", requester="a3")
```

## Coverage

**Requirements:** No coverage enforcement configured (no `--cov` in pytest config, no coverage config file).

**Estimated coverage by module:**

| Module | Coverage | Notes |
|---|---|---|
| `src/bifrost/store/models.py` | High | All dataclasses and enums tested |
| `src/bifrost/store/db.py` | High (core), Low (OAuth) | CRUD well tested; OAuth persistence methods untested |
| `src/bifrost/hub/agents.py` | High | All public methods tested |
| `src/bifrost/hub/tasks.py` | High | All transitions + artifacts tested |
| `src/bifrost/hub/conversations.py` | High | Send, list, subscribe tested |
| `src/bifrost/hub/delivery.py` | High | Check, dedup, channel delivery tested |
| `src/bifrost/mcp/tools.py` | High | All 11 handlers tested |
| `src/bifrost/app.py` | Medium | Health + tool registration tested; OAuth flow untested |
| `src/bifrost/auth/oauth.py` | None | No tests for OAuth provider |
| `src/bifrost/__main__.py` | None | CLI entry point untested |
| `src/bifrost/config.py` | High | Defaults + custom values tested |

## Test File Sizes

| File | Lines |
|---|---|
| `tests/test_tools.py` | 300 |
| `tests/test_integration.py` | 270 |
| `tests/test_db.py` | 242 |
| `tests/test_hub_tasks.py` | 175 |
| `tests/test_models.py` | 170 |
| `tests/test_hub_agents.py` | 150 |
| `tests/test_app.py` | 126 |
| `tests/test_hub_delivery.py` | 96 |
| `tests/test_hub_conversations.py` | 91 |

**Total test code:** ~1,620 lines across 9 test files.

## Adding New Tests

**For a new hub module:**
1. Create `tests/test_hub_{name}.py`
2. Add a fixture creating `Store` + Hub with `tmp_path`
3. Group tests in classes by method/feature
4. Test happy path, edge cases, and error raises

**For a new MCP tool:**
1. Add handler tests to `tests/test_tools.py` in a new `TestHandle{ToolName}` class
2. Use `handlers`, `alice`, `bob` fixtures
3. Assert on string output content
4. Add an integration test in `tests/test_integration.py` for the full flow

**For a new store method:**
1. Add tests to `tests/test_db.py` in the appropriate `Test{Entity}` class
2. Use the `store` fixture
3. Test create, read, update, list, and missing-record cases

---

*Testing analysis: 2026-03-30*
