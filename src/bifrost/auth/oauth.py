"""OAuth Authorization Server provider — proxies to an external OIDC provider (e.g. Dex).

Persists clients, tokens, and auth codes in SQLite so they survive restarts.
Only pending_auths (mid-redirect state) is in-memory — ephemeral by nature.
"""

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
    RefreshToken,
    construct_redirect_uri,
)
from mcp.shared.auth import OAuthClientInformationFull, OAuthToken

from bifrost.store.db import Store


class DexOAuthProvider:
    """Bifrost acts as its own Authorization Server, proxying to Dex/OIDC.

    Clients, access tokens, refresh tokens, and auth codes are persisted
    in SQLite. Only the mid-redirect pending auth state is in-memory.
    """

    def __init__(
        self,
        *,
        store: Store,
        issuer: str,
        client_id: str,
        client_secret: str,
        server_url: str,
    ) -> None:
        self._store = store
        self._issuer = issuer.rstrip("/")
        self._client_id = client_id
        self._client_secret = client_secret
        self._server_url = server_url.rstrip("/")

        # Ephemeral: only lives during the browser redirect (seconds)
        self._pending_auths: dict[str, dict] = {}

    # ------------------------------------------------------------------
    # OAuthAuthorizationServerProvider protocol
    # ------------------------------------------------------------------

    async def get_client(self, client_id: str) -> OAuthClientInformationFull | None:
        raw = self._store.get_oauth_client(client_id)
        if raw is None:
            return None
        return OAuthClientInformationFull.model_validate_json(raw)

    async def register_client(self, client_info: OAuthClientInformationFull) -> None:
        self._store.save_oauth_client(
            client_info.client_id,
            client_info.model_dump_json(),
        )

    async def authorize(
        self, client: OAuthClientInformationFull, params: AuthorizationParams
    ) -> str:
        """Redirect the user to the external OIDC provider for authentication."""
        internal_state = secrets.token_urlsafe(32)
        self._pending_auths[internal_state] = {
            "client_id": client.client_id,
            "redirect_uri": str(params.redirect_uri),
            "redirect_uri_provided_explicitly": params.redirect_uri_provided_explicitly,
            "code_challenge": params.code_challenge,
            "state": params.state,
            "scopes": params.scopes or [],
        }

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
        """Handle the OIDC provider callback. Returns redirect URL for the MCP client."""
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

        # Generate our own authorization code and persist it
        local_code = secrets.token_urlsafe(32)
        expires_at = time.time() + 300
        self._store.save_oauth_auth_code(
            code=local_code,
            client_id=pending["client_id"],
            redirect_uri=pending["redirect_uri"],
            redirect_uri_provided_explicitly=pending["redirect_uri_provided_explicitly"],
            code_challenge=pending["code_challenge"],
            scopes=pending["scopes"],
            expires_at=expires_at,
        )

        return construct_redirect_uri(
            pending["redirect_uri"],
            code=local_code,
            state=pending["state"],
        )

    async def load_authorization_code(
        self, client: OAuthClientInformationFull, authorization_code: str
    ) -> AuthorizationCode | None:
        row = self._store.get_oauth_auth_code(authorization_code)
        if row is None or row["client_id"] != client.client_id:
            return None
        if row["expires_at"] < time.time():
            self._store.delete_oauth_auth_code(authorization_code)
            return None
        return AuthorizationCode(
            code=row["code"],
            client_id=row["client_id"],
            redirect_uri=AnyUrl(row["redirect_uri"]),
            redirect_uri_provided_explicitly=row["redirect_uri_provided_explicitly"],
            code_challenge=row["code_challenge"],
            scopes=row["scopes"],
            expires_at=row["expires_at"],
        )

    async def exchange_authorization_code(
        self, client: OAuthClientInformationFull, authorization_code: AuthorizationCode
    ) -> OAuthToken:
        self._store.delete_oauth_auth_code(authorization_code.code)

        access_token_str = secrets.token_urlsafe(32)
        refresh_token_str = secrets.token_urlsafe(32)
        expires_in = 3600
        now = int(time.time())

        self._store.save_oauth_access_token(
            token=access_token_str,
            client_id=client.client_id,
            scopes=authorization_code.scopes,
            expires_at=now + expires_in,
        )
        self._store.save_oauth_refresh_token(
            token=refresh_token_str,
            client_id=client.client_id,
            scopes=authorization_code.scopes,
            expires_at=now + 86400,
        )

        return OAuthToken(
            access_token=access_token_str,
            token_type="Bearer",
            expires_in=expires_in,
            refresh_token=refresh_token_str,
            scope=" ".join(authorization_code.scopes) if authorization_code.scopes else None,
        )

    async def load_access_token(self, token: str) -> AccessToken | None:
        row = self._store.get_oauth_access_token(token)
        if row is None:
            return None
        if row["expires_at"] < int(time.time()):
            self._store.delete_oauth_access_token(token)
            return None
        return AccessToken(
            token=row["token"],
            client_id=row["client_id"],
            scopes=row["scopes"],
            expires_at=row["expires_at"],
        )

    async def load_refresh_token(
        self, client: OAuthClientInformationFull, refresh_token: str
    ) -> RefreshToken | None:
        row = self._store.get_oauth_refresh_token(refresh_token)
        if row is None or row["client_id"] != client.client_id:
            return None
        if row["expires_at"] < int(time.time()):
            self._store.delete_oauth_refresh_token(refresh_token)
            return None
        return RefreshToken(
            token=row["token"],
            client_id=row["client_id"],
            scopes=row["scopes"],
            expires_at=row["expires_at"],
        )

    async def exchange_refresh_token(
        self,
        client: OAuthClientInformationFull,
        refresh_token: RefreshToken,
        scopes: list[str],
    ) -> OAuthToken:
        self._store.delete_oauth_refresh_token(refresh_token.token)

        access_token_str = secrets.token_urlsafe(32)
        new_refresh_str = secrets.token_urlsafe(32)
        expires_in = 3600
        now = int(time.time())
        effective_scopes = scopes or refresh_token.scopes

        self._store.save_oauth_access_token(
            token=access_token_str,
            client_id=client.client_id,
            scopes=effective_scopes,
            expires_at=now + expires_in,
        )
        self._store.save_oauth_refresh_token(
            token=new_refresh_str,
            client_id=client.client_id,
            scopes=effective_scopes,
            expires_at=now + 86400,
        )

        return OAuthToken(
            access_token=access_token_str,
            token_type="Bearer",
            expires_in=expires_in,
            refresh_token=new_refresh_str,
            scope=" ".join(effective_scopes) if effective_scopes else None,
        )

    async def revoke_token(self, token: AccessToken | RefreshToken) -> None:
        if isinstance(token, AccessToken):
            self._store.delete_oauth_access_token(token.token)
        elif isinstance(token, RefreshToken):
            self._store.delete_oauth_refresh_token(token.token)
