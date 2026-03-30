"""Bifrost FastMCP application factory."""

from __future__ import annotations

import secrets
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

FIRST: Call bifrost_introduce now with your name and a description of what
you can do. Your name is typically your project name. You MUST introduce
yourself before using any other bifrost tool.

MESSAGES: Use bifrost_send to message agents or channels.
Call bifrost_check regularly to receive messages.

TASKS: Use bifrost_request_task to delegate work.
States: queued -> running -> completed/failed/canceled/rejected.
Use input-required when you need more info.

DISCOVERY: Use bifrost_list_agents to find agents and their capabilities.
CHANNELS: Use bifrost_subscribe to follow topics.
STATUS: Use bifrost_whoami to check/update your status.
"""


def _get_session_key(access_token: Any, *, insecure: bool) -> str:
    """Get a unique key for the current MCP session.

    OAuth mode: uses the access_token.token (unique per session).
    Insecure mode: uses a fixed key (single-session).
    """
    if not insecure and access_token is not None:
        return access_token.token
    # In insecure mode there's no access token to derive a unique key from.
    # All insecure sessions share a single key, so only one agent can be
    # active at a time.  A future improvement could use per-connection state.
    return "insecure"


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

    # Maps session key -> agent_id. Populated by bifrost_introduce.
    # Agent is invisible until introduced.
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
            store=store,
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
    # Auto-register unnamed agents on any authenticated MCP request.
    # The list_tools handler runs on every new connection — we hook into
    # it to ensure authenticated sessions get a placeholder agent entry.
    # ------------------------------------------------------------------

    _original_list_tools = mcp._tool_manager.list_tools

    def _list_tools_with_registration():
        """Wrapper that auto-registers the agent on list_tools (first MCP call)."""
        from mcp.server.auth.middleware.auth_context import get_access_token
        try:
            access_token = get_access_token()
            if access_token is not None:
                session_key = _get_session_key(access_token, insecure=config.insecure)
                if session_key not in session_agents:
                    oauth_subject = access_token.client_id
                    placeholder = f"unnamed ({oauth_subject[:12]})"
                    agent = agents.register(placeholder, oauth_subject=oauth_subject)
                    session_agents[session_key] = agent.id
        except Exception:
            import logging
            logging.getLogger("bifrost").warning("Auto-registration failed", exc_info=True)
        return _original_list_tools()

    mcp._tool_manager.list_tools = _list_tools_with_registration

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
            try:
                redirect_url = await provider.handle_callback(code, state)
            except ValueError as e:
                return JSONResponse({"error": "authorization_failed", "error_description": str(e)}, status_code=400)
            except Exception:
                return JSONResponse({"error": "server_error", "error_description": "Failed to complete authorization"}, status_code=500)
            return RedirectResponse(redirect_url)

    # ------------------------------------------------------------------
    # Helper to get agent_id in tool handlers
    # ------------------------------------------------------------------

    def _get_session_agent() -> str:
        """Get or create the agent for the current session.

        If not yet introduced, creates an unnamed placeholder so the agent
        is visible in the registry. bifrost_introduce renames it later.
        """
        from mcp.server.auth.middleware.auth_context import get_access_token

        access_token = get_access_token()
        session_key = _get_session_key(access_token, insecure=config.insecure)

        if session_key in session_agents:
            return session_agents[session_key]

        # Auto-register unnamed placeholder
        if access_token is not None:
            oauth_subject = access_token.client_id
            placeholder = f"unnamed ({oauth_subject[:12]})"
        else:
            placeholder = f"unnamed-{secrets.token_hex(4)}"
        agent = agents.register(placeholder, oauth_subject=access_token.client_id if access_token else "")
        session_agents[session_key] = agent.id
        return agent.id

    def _introduce_agent(name: str) -> str:
        """Register or reconnect an agent by name. Binds to current session."""
        from mcp.server.auth.middleware.auth_context import get_access_token

        access_token = get_access_token()
        session_key = _get_session_key(access_token, insecure=config.insecure)
        oauth_subject = access_token.client_id if access_token else ""

        # register() handles both new and returning agents
        agent = agents.register(name, oauth_subject=oauth_subject)
        session_agents[session_key] = agent.id
        return agent.id

    # ------------------------------------------------------------------
    # Tool registration
    # ------------------------------------------------------------------

    @mcp.tool()
    async def bifrost_introduce(
        name: str = "",
        introduction: str = "",
        description: str = "",
        skills: list[dict] | None = None,
        limitations: str = "",
    ) -> str:
        """Register yourself with bifrost. MUST be called before any other tool.

        Args:
            name: Your agent name (required). How other agents will address you.
            introduction: Freeform self-description.
            description: What you do (overrides introduction if both given).
            skills: List of skill objects with name, description, tags.
            limitations: What you cannot do.
        """
        if not name:
            return "Error: name is required. Tell bifrost who you are."

        try:
            agent_id = _introduce_agent(name)
            return handlers.handle_introduce(
                agent_id=agent_id,
                name=name,
                introduction=introduction or None,
                description=description or None,
                skills=skills,
                limitations=limitations or None,
            )
        except (ValueError, KeyError) as e:
            return f"Error: {e}"

    @mcp.tool()
    async def bifrost_whoami(
        status: str = "",
        dnd_reason: str = "",
    ) -> str:
        """Check your identity or update your status (online, idle, dnd, offline)."""
        try:
            agent_id = _get_session_agent()
            return handlers.handle_whoami(
                agent_id=agent_id,
                status=status or None,
                dnd_reason=dnd_reason or None,
            )
        except (ValueError, KeyError) as e:
            return f"Error: {e}"

    @mcp.tool()
    async def bifrost_list_agents(status: str = "") -> str:
        """List all connected agents and their capabilities."""
        return handlers.handle_list_agents(status=status or None)

    @mcp.tool()
    async def bifrost_request_task(
        assignee: str,
        title: str = "",
        description: str = "",
    ) -> str:
        """Request another agent to perform a task."""
        try:
            agent_id = _get_session_agent()
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
        except (ValueError, KeyError) as e:
            return f"Error: {e}"

    @mcp.tool()
    async def bifrost_update_task(
        task_id: str,
        status: str = "",
        summary: str = "",
        reason: str = "",
    ) -> str:
        """Update a task's status (accepted, in_progress, completed, failed, rejected)."""
        from bifrost.hub.tasks import InvalidTransition
        try:
            _get_session_agent()  # Ensure introduced
            artifacts = None
            if summary:
                artifacts = [{"text": summary}]
            return handlers.handle_update_task(
                task_id=task_id,
                status=status or None,
                artifacts=artifacts,
            )
        except (ValueError, KeyError, InvalidTransition) as e:
            return f"Error: {e}"

    @mcp.tool()
    async def bifrost_get_task(task_id: str) -> str:
        """Get detailed information about a specific task."""
        try:
            return handlers.handle_get_task(task_id=task_id)
        except (ValueError, KeyError) as e:
            return f"Error: {e}"

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
    ) -> str:
        """Send a message to an agent, channel, or existing conversation."""
        try:
            agent_id = _get_session_agent()
            return handlers.handle_send(
                from_agent_id=agent_id,
                to=to or None,
                channel=channel or None,
                conversation_id=conversation_id or None,
                body=body,
            )
        except (ValueError, KeyError) as e:
            return f"Error: {e}"

    @mcp.tool()
    async def bifrost_list_conversations(channel: str = "") -> str:
        """List active conversations, optionally filtered by channel."""
        try:
            return handlers.handle_list_conversations(channel=channel or None)
        except (ValueError, KeyError) as e:
            return f"Error: {e}"

    @mcp.tool()
    async def bifrost_subscribe(target: str) -> str:
        """Subscribe to a channel or task for notifications."""
        try:
            agent_id = _get_session_agent()
            return handlers.handle_subscribe(agent_id=agent_id, target=target)
        except (ValueError, KeyError) as e:
            return f"Error: {e}"

    @mcp.tool()
    async def bifrost_check() -> str:
        """Check for pending messages and events."""
        try:
            agent_id = _get_session_agent()
            return handlers.handle_check(agent_id=agent_id)
        except (ValueError, KeyError) as e:
            return f"Error: {e}"

    return mcp
