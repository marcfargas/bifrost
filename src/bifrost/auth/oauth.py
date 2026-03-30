"""OAuth Authorization Server provider — proxies to an external OIDC provider (e.g. Dex)."""

from __future__ import annotations

import secrets
import time
from urllib.parse import urlencode

import httpx
from pydantic import AnyUrl

from mcp.server.auth.provider import (
    AccessToken,
    AuthorizationCode,
    AuthorizationParams,
    OAuthAuthorizationServerProvider,
    RefreshToken,
    construct_redirect_uri,
)
from mcp.shared.auth import OAuthClientInformationFull, OAuthToken


class DexOAuthProvider:
    """Bifrost acts as its own Authorization Server, proxying to Dex/OIDC.

    Implements the ``OAuthAuthorizationServerProvider`` protocol with
    in-memory stores for clients, authorization codes, and tokens.
    """

    def __init__(
        self,
        *,
        issuer: str,
        client_id: str,
        client_secret: str,
        server_url: str,
    ) -> None:
        self._issuer = issuer.rstrip("/")
        self._client_id = client_id
        self._client_secret = client_secret
        self._server_url = server_url.rstrip("/")

        # In-memory stores
        self._clients: dict[str, OAuthClientInformationFull] = {}
        self._auth_codes: dict[str, AuthorizationCode] = {}
        self._access_tokens: dict[str, AccessToken] = {}
        self._refresh_tokens: dict[str, RefreshToken] = {}

        # Map: auth_code -> (code_verifier_from_dex_flow, state, redirect_uri, client_id)
        self._pending_auths: dict[str, dict] = {}

    # ------------------------------------------------------------------
    # OAuthAuthorizationServerProvider protocol
    # ------------------------------------------------------------------

    async def get_client(self, client_id: str) -> OAuthClientInformationFull | None:
        return self._clients.get(client_id)

    async def register_client(self, client_info: OAuthClientInformationFull) -> None:
        self._clients[client_info.client_id] = client_info

    async def authorize(
        self, client: OAuthClientInformationFull, params: AuthorizationParams
    ) -> str:
        """Redirect the user to the external OIDC provider for authentication."""
        # Generate an internal state that maps back to the original request
        internal_state = secrets.token_urlsafe(32)
        self._pending_auths[internal_state] = {
            "client_id": client.client_id,
            "redirect_uri": str(params.redirect_uri),
            "redirect_uri_provided_explicitly": params.redirect_uri_provided_explicitly,
            "code_challenge": params.code_challenge,
            "state": params.state,
            "scopes": params.scopes or [],
        }

        # Build the redirect URL to the external OIDC provider
        callback_url = f"{self._server_url}/callback"
        oidc_params = {
            "client_id": self._client_id,
            "response_type": "code",
            "redirect_uri": callback_url,
            "scope": "openid email profile",
            "state": internal_state,
        }
        return f"{self._issuer}/auth?{urlencode(oidc_params)}"

    async def handle_callback(self, code: str, state: str) -> str:
        """Handle the OIDC provider callback.

        Exchanges the code with the OIDC provider, generates a local auth code,
        and redirects back to the MCP client.

        Returns the redirect URL to send the user back to.
        """
        pending = self._pending_auths.pop(state, None)
        if pending is None:
            raise ValueError("Unknown or expired authorization state")

        # Exchange code with external OIDC provider
        callback_url = f"{self._server_url}/callback"
        async with httpx.AsyncClient() as http:
            token_resp = await http.post(
                f"{self._issuer}/token",
                data={
                    "grant_type": "authorization_code",
                    "code": code,
                    "redirect_uri": callback_url,
                    "client_id": self._client_id,
                    "client_secret": self._client_secret,
                },
            )
            token_resp.raise_for_status()
            _oidc_tokens = token_resp.json()

        # Generate our own authorization code
        local_code = secrets.token_urlsafe(32)
        auth_code = AuthorizationCode(
            code=local_code,
            client_id=pending["client_id"],
            redirect_uri=AnyUrl(pending["redirect_uri"]),
            redirect_uri_provided_explicitly=pending["redirect_uri_provided_explicitly"],
            code_challenge=pending["code_challenge"],
            scopes=pending["scopes"],
            expires_at=time.time() + 300,
        )
        self._auth_codes[local_code] = auth_code

        # Redirect back to the MCP client
        return construct_redirect_uri(
            pending["redirect_uri"],
            code=local_code,
            state=pending["state"],
        )

    async def load_authorization_code(
        self, client: OAuthClientInformationFull, authorization_code: str
    ) -> AuthorizationCode | None:
        code_obj = self._auth_codes.get(authorization_code)
        if code_obj and code_obj.client_id == client.client_id:
            return code_obj
        return None

    async def exchange_authorization_code(
        self, client: OAuthClientInformationFull, authorization_code: AuthorizationCode
    ) -> OAuthToken:
        # Remove the used code
        self._auth_codes.pop(authorization_code.code, None)

        # Generate access and refresh tokens
        access_token_str = secrets.token_urlsafe(32)
        refresh_token_str = secrets.token_urlsafe(32)
        expires_in = 3600

        access_token = AccessToken(
            token=access_token_str,
            client_id=client.client_id,
            scopes=authorization_code.scopes,
            expires_at=int(time.time()) + expires_in,
        )
        self._access_tokens[access_token_str] = access_token

        refresh_token = RefreshToken(
            token=refresh_token_str,
            client_id=client.client_id,
            scopes=authorization_code.scopes,
            expires_at=int(time.time()) + 86400,
        )
        self._refresh_tokens[refresh_token_str] = refresh_token

        return OAuthToken(
            access_token=access_token_str,
            token_type="Bearer",
            expires_in=expires_in,
            refresh_token=refresh_token_str,
            scope=" ".join(authorization_code.scopes) if authorization_code.scopes else None,
        )

    async def load_access_token(self, token: str) -> AccessToken | None:
        access_token = self._access_tokens.get(token)
        if access_token is None:
            return None
        if access_token.expires_at and access_token.expires_at < int(time.time()):
            del self._access_tokens[token]
            return None
        return access_token

    async def load_refresh_token(
        self, client: OAuthClientInformationFull, refresh_token: str
    ) -> RefreshToken | None:
        token_obj = self._refresh_tokens.get(refresh_token)
        if token_obj and token_obj.client_id == client.client_id:
            return token_obj
        return None

    async def exchange_refresh_token(
        self,
        client: OAuthClientInformationFull,
        refresh_token: RefreshToken,
        scopes: list[str],
    ) -> OAuthToken:
        # Revoke old tokens
        self._refresh_tokens.pop(refresh_token.token, None)

        # Generate new tokens
        access_token_str = secrets.token_urlsafe(32)
        new_refresh_str = secrets.token_urlsafe(32)
        expires_in = 3600

        access_token = AccessToken(
            token=access_token_str,
            client_id=client.client_id,
            scopes=scopes or refresh_token.scopes,
            expires_at=int(time.time()) + expires_in,
        )
        self._access_tokens[access_token_str] = access_token

        new_refresh = RefreshToken(
            token=new_refresh_str,
            client_id=client.client_id,
            scopes=scopes or refresh_token.scopes,
            expires_at=int(time.time()) + 86400,
        )
        self._refresh_tokens[new_refresh_str] = new_refresh

        return OAuthToken(
            access_token=access_token_str,
            token_type="Bearer",
            expires_in=expires_in,
            refresh_token=new_refresh_str,
            scope=" ".join(scopes) if scopes else None,
        )

    async def revoke_token(
        self, token: AccessToken | RefreshToken
    ) -> None:
        if isinstance(token, AccessToken):
            self._access_tokens.pop(token.token, None)
        elif isinstance(token, RefreshToken):
            self._refresh_tokens.pop(token.token, None)
