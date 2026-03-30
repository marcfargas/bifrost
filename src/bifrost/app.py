"""Bifrost FastMCP application factory."""

from __future__ import annotations

from typing import Any

from starlette.requests import Request
from starlette.responses import JSONResponse, RedirectResponse, Response

from mcp.server.fastmcp import FastMCP

from bifrost.config import Config
from bifrost.hub.agents import AgentHub
from bifrost.hub.conversations import ConversationHub
from bifrost.hub.delivery import DeliveryHub
from bifrost.hub.tasks import TaskHub
from bifrost.mcp.tools import ToolHandlers
from bifrost.store.db import Store


MCP_INSTRUCTIONS = """Bifrost connects you to other agents.

MESSAGES: Use bifrost_send to message agents or channels.
Call bifrost_check regularly to receive messages.

TASKS: Use bifrost_request_task to delegate work.
States: queued -> running -> completed/failed/canceled/rejected.
Use input-required when you need more info.

DISCOVERY: Use bifrost_list_agents to find agents and their capabilities.
Use bifrost_introduce to describe yourself.

CHANNELS: Use bifrost_subscribe to follow topics.
STATUS: Use bifrost_whoami to check/update your status.
"""


def _resolve_agent_id(
    access_token: Any,
    agents: AgentHub,
    *,
    insecure: bool = False,
    agent_name: str | None = None,
    _session_agents: dict[str, str] | None = None,
) -> str:
    """Resolve the calling agent's ID.

    In OAuth mode: uses access_token.client_id to find/create agent.
    In insecure mode: uses agent_name param or auto-generates.
    """
    if not insecure and access_token is not None:
        # OAuth mode: client_id is the agent identity
        client_id = access_token.client_id
        try:
            agent = agents.resolve(client_id)
            return agent.id
        except KeyError:
            agent = agents.register(client_id, oauth_subject=client_id)
            return agent.id

    # Insecure mode
    if agent_name:
        try:
            agent = agents.resolve(agent_name)
            return agent.id
        except KeyError:
            agent = agents.register(agent_name)
            return agent.id

    raise ValueError(
        "Agent identity required. In insecure mode, provide _agent_name parameter."
    )


def create_app(config: Config) -> FastMCP:
    """Create and configure the Bifrost FastMCP application."""
    store = Store(config.db_path)

    # Create hub instances
    agents = AgentHub(store)
    tasks = TaskHub(store)
    conversations = ConversationHub(store)
    delivery = DeliveryHub(store)
    handlers = ToolHandlers(
        agents=agents, tasks=tasks, conversations=conversations, delivery=delivery
    )

    # Session-level agent tracking for insecure mode
    # Maps session token -> agent_id (not used in OAuth mode)
    session_agents: dict[str, str] = {}

    # Build FastMCP kwargs
    mcp_kwargs: dict[str, Any] = {
        "name": "bifrost",
        "instructions": MCP_INSTRUCTIONS,
        "stateless_http": False,
        "streamable_http_path": "/mcp",
        "host": config.host,
        "port": config.port,
    }

    if not config.insecure and config.oidc_issuer:
        from mcp.server.auth.settings import (
            AuthSettings,
            ClientRegistrationOptions,
            RevocationOptions,
        )
        from bifrost.auth.oauth import DexOAuthProvider

        provider = DexOAuthProvider(
            issuer=config.oidc_issuer,
            client_id=config.oidc_client_id,
            client_secret=config.oidc_client_secret,
            server_url=config.server_url or f"http://{config.host}:{config.port}",
        )
        mcp_kwargs["auth_server_provider"] = provider
        mcp_kwargs["auth"] = AuthSettings(
            issuer_url=config.server_url or f"http://{config.host}:{config.port}",
            resource_server_url=config.server_url or f"http://{config.host}:{config.port}",
            client_registration_options=ClientRegistrationOptions(enabled=True),
            revocation_options=RevocationOptions(enabled=True),
        )
    else:
        provider = None

    mcp = FastMCP(**mcp_kwargs)

    # ------------------------------------------------------------------
    # Health endpoint
    # ------------------------------------------------------------------

    @mcp.custom_route("/health", methods=["GET"])
    async def health(request: Request) -> Response:
        return JSONResponse({"status": "ok"})

    # ------------------------------------------------------------------
    # OAuth callback (only if auth is configured)
    # ------------------------------------------------------------------

    if provider is not None:
        @mcp.custom_route("/callback", methods=["GET"])
        async def oauth_callback(request: Request) -> Response:
            code = request.query_params.get("code", "")
            state = request.query_params.get("state", "")
            if not code or not state:
                return JSONResponse({"error": "Missing code or state"}, status_code=400)
            redirect_url = await provider.handle_callback(code, state)
            return RedirectResponse(redirect_url)

    # ------------------------------------------------------------------
    # Helper to get agent_id in tool handlers
    # ------------------------------------------------------------------

    def _get_agent_id(agent_name: str = "") -> str:
        from mcp.server.auth.middleware.auth_context import get_access_token

        access_token = get_access_token()
        return _resolve_agent_id(
            access_token,
            agents,
            insecure=config.insecure,
            agent_name=agent_name or None,
            _session_agents=session_agents,
        )

    # ------------------------------------------------------------------
    # Tool registration
    # ------------------------------------------------------------------

    @mcp.tool()
    async def bifrost_introduce(
        introduction: str = "",
        description: str = "",
        skills: list[dict] | None = None,
        limitations: str = "",
        agent_name: str = "",
    ) -> str:
        """Update your agent card with a description, skills, and limitations.

        In insecure mode, provide agent_name to identify yourself.
        """
        agent_id = _get_agent_id(agent_name)
        return handlers.handle_introduce(
            agent_id=agent_id,
            introduction=introduction or None,
            description=description or None,
            skills=skills,
            limitations=limitations or None,
        )

    @mcp.tool()
    async def bifrost_whoami(
        status: str = "",
        dnd_reason: str = "",
        agent_name: str = "",
    ) -> str:
        """Check your identity or update your status (online, idle, dnd, offline).

        In insecure mode, provide agent_name to identify yourself.
        """
        agent_id = _get_agent_id(agent_name)
        return handlers.handle_whoami(
            agent_id=agent_id,
            status=status or None,
            dnd_reason=dnd_reason or None,
        )

    @mcp.tool()
    async def bifrost_list_agents(status: str = "") -> str:
        """List all connected agents and their capabilities."""
        return handlers.handle_list_agents(status=status or None)

    @mcp.tool()
    async def bifrost_request_task(
        assignee: str,
        title: str = "",
        description: str = "",
        agent_name: str = "",
    ) -> str:
        """Request another agent to perform a task.

        In insecure mode, provide agent_name to identify yourself.
        """
        agent_id = _get_agent_id(agent_name)
        metadata: dict[str, str] = {}
        if title:
            metadata["title"] = title
        if description:
            metadata["description"] = description
        return handlers.handle_request_task(
            requester_id=agent_id,
            assignee_name=assignee,
            metadata=metadata or None,
        )

    @mcp.tool()
    async def bifrost_update_task(
        conversation_id: str,
        status: str = "",
        summary: str = "",
        reason: str = "",
        agent_name: str = "",
    ) -> str:
        """Update a task's status (accepted, in_progress, completed, failed, rejected).

        In insecure mode, provide agent_name to identify yourself.
        """
        _get_agent_id(agent_name)  # Ensure agent is registered
        artifacts = None
        if summary:
            artifacts = [{"text": summary}]
        return handlers.handle_update_task(
            task_id=conversation_id,
            status=status or None,
            artifacts=artifacts,
        )

    @mcp.tool()
    async def bifrost_get_task(conversation_id: str) -> str:
        """Get detailed information about a specific task."""
        return handlers.handle_get_task(task_id=conversation_id)

    @mcp.tool()
    async def bifrost_list_tasks(
        status: str = "",
        requester: str = "",
        assignee: str = "",
    ) -> str:
        """List tasks, optionally filtered by status, requester, or assignee."""
        return handlers.handle_list_tasks(
            status=status or None,
            requester=requester or None,
            assignee=assignee or None,
        )

    @mcp.tool()
    async def bifrost_send(
        body: str,
        to: str = "",
        channel: str = "",
        conversation_id: str = "",
        agent_name: str = "",
    ) -> str:
        """Send a message to an agent, channel, or existing conversation.

        In insecure mode, provide agent_name to identify yourself.
        """
        agent_id = _get_agent_id(agent_name)
        return handlers.handle_send(
            from_agent_id=agent_id,
            to=to or None,
            channel=channel or None,
            conversation_id=conversation_id or None,
            body=body,
        )

    @mcp.tool()
    async def bifrost_list_conversations(channel: str = "") -> str:
        """List active conversations, optionally filtered by channel."""
        return handlers.handle_list_conversations(channel=channel or None)

    @mcp.tool()
    async def bifrost_subscribe(
        target: str,
        agent_name: str = "",
    ) -> str:
        """Subscribe to a channel or task for notifications.

        In insecure mode, provide agent_name to identify yourself.
        """
        agent_id = _get_agent_id(agent_name)
        return handlers.handle_subscribe(agent_id=agent_id, target=target)

    @mcp.tool()
    async def bifrost_check(agent_name: str = "") -> str:
        """Check for pending messages and events.

        In insecure mode, provide agent_name to identify yourself.
        """
        agent_id = _get_agent_id(agent_name)
        return handlers.handle_check(agent_id=agent_id)

    return mcp
