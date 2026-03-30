"""Bifrost server configuration."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Config:
    """Server configuration, populated from CLI args or environment."""

    host: str = "0.0.0.0"
    port: int = 8000
    db_path: str = "bifrost.db"
    insecure: bool = False

    # OIDC settings (required unless --insecure)
    oidc_issuer: str = ""
    oidc_client_id: str = ""
    oidc_client_secret: str = ""
    server_url: str = ""
