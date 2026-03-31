"""Dashboard routes -- Jinja2 + htmx read-only views."""

from __future__ import annotations

from datetime import datetime
from pathlib import Path
from typing import Any

from starlette.requests import Request
from starlette.responses import HTMLResponse, RedirectResponse

from mcp.server.fastmcp import FastMCP
from bifrost.store.db import Store

TEMPLATE_DIR = Path(__file__).parent / "templates"
HUMAN_OPERATOR_NAME = "Human Operator"


def _fmt_time(ts: str | None) -> str:
    """Format ISO timestamp to HH:MM:SS. Returns 'never' for None."""
    if not ts:
        return "never"
    try:
        dt = datetime.fromisoformat(ts)
        return dt.strftime("%H:%M:%S")
    except ValueError:
        return ts


def _fmt_event(data: dict[str, Any], event_type: str = "message") -> str:
    """Format event data as a readable string based on event type."""
    if event_type == "message":
        parts = data.get("parts", [])
        texts = [p.get("text", "") for p in parts if p.get("type") == "text"]
        return " ".join(texts) if texts else "(message)"
    if event_type == "participant.added":
        return f"joined: {data.get('agent_id', '?')}"
    if event_type == "participant.removed":
        return f"left: {data.get('agent_id', '?')}"
    if event_type == "metadata":
        return str(data)
    return str(data)


def _ensure_human_operator(store: Store) -> None:
    """Create the human operator agent if it does not exist. Per D-08: lazy on first dashboard visit."""
    from bifrost.store.models import Agent, AgentStatus

    existing = store.get_agent_by_name(HUMAN_OPERATOR_NAME)
    if existing is None:
        agent = Agent(
            name=HUMAN_OPERATOR_NAME,
            status=AgentStatus.ONLINE,
            is_human=True,
        )
        store.upsert_agent(agent)


def setup_dashboard_routes(mcp: FastMCP, store: Store) -> None:
    """Register all dashboard routes on the FastMCP instance."""
    from starlette.templating import Jinja2Templates

    from bifrost.hub.agents import AgentHub
    from bifrost.hub.tasks import TaskHub
    from bifrost.hub.conversations import ConversationHub

    templates = Jinja2Templates(directory=str(TEMPLATE_DIR))
    templates.env.filters["fmt_time"] = _fmt_time
    templates.env.filters["fmt_event"] = _fmt_event

    agents_hub = AgentHub(store)
    tasks_hub = TaskHub(store)
    conversations_hub = ConversationHub(store)

    @mcp.custom_route("/", methods=["GET"])
    async def index(request: Request) -> RedirectResponse:
        return RedirectResponse(url="/agents", status_code=307)

    @mcp.custom_route("/agents", methods=["GET"])
    async def agents_page(request: Request) -> HTMLResponse:
        _ensure_human_operator(store)
        return templates.TemplateResponse(request, "agents.html", {"active": "agents"})

    @mcp.custom_route("/agents/list", methods=["GET"])
    async def agents_list_partial(request: Request) -> HTMLResponse:
        all_agents = agents_hub.list_all()
        human = next((a for a in all_agents if a.is_human), None)
        regular = [a for a in all_agents if not a.is_human]
        return templates.TemplateResponse(
            request, "partials/agent_list.html",
            {"agents": regular, "human": human},
        )

    @mcp.custom_route("/tasks", methods=["GET"])
    async def tasks_page(request: Request) -> HTMLResponse:
        return templates.TemplateResponse(request, "tasks.html", {"active": "tasks"})

    @mcp.custom_route("/tasks/list", methods=["GET"])
    async def tasks_list_partial(request: Request) -> HTMLResponse:
        status_filter = request.query_params.get("status")
        from bifrost.store.models import TaskStatus

        ts = TaskStatus(status_filter) if status_filter else None
        task_list = store.list_tasks(status=ts)
        # Resolve agent names for display
        agent_names: dict[str, str] = {}
        for t in task_list:
            for aid in (t.requester, t.assignee):
                if aid and aid not in agent_names:
                    a = store.get_agent(aid)
                    agent_names[aid] = a.name if a else aid[:8]
        return templates.TemplateResponse(
            request, "partials/task_list.html",
            {"tasks": task_list, "agent_names": agent_names, "current_status": status_filter or "all"},
        )

    @mcp.custom_route("/conversations", methods=["GET"])
    async def conversations_page(request: Request) -> HTMLResponse:
        return templates.TemplateResponse(request, "conversations.html", {"active": "conversations"})

    @mcp.custom_route("/conversations/list", methods=["GET"])
    async def conversations_list_partial(request: Request) -> HTMLResponse:
        convs = store.list_conversations()
        return templates.TemplateResponse(
            request, "partials/conversation_list.html",
            {"conversations": convs},
        )

    @mcp.custom_route("/conversations/{conv_id}/messages", methods=["GET"])
    async def conversation_detail_partial(request: Request) -> HTMLResponse:
        conv_id = request.path_params["conv_id"]
        conv = store.get_conversation(conv_id)
        if conv is None:
            return HTMLResponse("<p>Conversation not found.</p>", status_code=404)
        events = store.list_events(conv_id)
        # Resolve agent names for message senders
        agent_names: dict[str, str] = {}
        for e in events:
            if e.from_agent and e.from_agent not in agent_names:
                a = store.get_agent(e.from_agent)
                agent_names[e.from_agent] = a.name if a else e.from_agent[:8]
        return templates.TemplateResponse(
            request, "partials/conversation_detail.html",
            {"conversation": conv, "events": events, "agent_names": agent_names},
        )

    @mcp.custom_route("/activity", methods=["GET"])
    async def activity_page(request: Request) -> HTMLResponse:
        return templates.TemplateResponse(request, "activity.html", {"active": "activity"})

    @mcp.custom_route("/activity/feed", methods=["GET"])
    async def activity_feed_partial(request: Request) -> HTMLResponse:
        convs = store.list_conversations()
        all_events: list = []
        for c in convs:
            all_events.extend(store.list_events(c.id))
        all_events.sort(key=lambda e: e.timestamp, reverse=True)
        all_events = all_events[:50]  # Latest 50 events
        # Resolve agent names
        agent_names: dict[str, str] = {}
        for e in all_events:
            if e.from_agent and e.from_agent not in agent_names:
                a = store.get_agent(e.from_agent)
                agent_names[e.from_agent] = a.name if a else e.from_agent[:8]
        return templates.TemplateResponse(
            request, "partials/activity_feed.html",
            {"events": all_events, "agent_names": agent_names},
        )
