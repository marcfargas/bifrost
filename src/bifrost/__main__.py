"""Bifrost CLI entry point."""

from __future__ import annotations

import argparse
import os


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="bifrost",
        description="Bifrost — A2A-inspired communication hub over MCP",
    )
    parser.add_argument("--host", default="0.0.0.0", help="Bind host (default: 0.0.0.0)")
    parser.add_argument("--port", type=int, default=8000, help="Bind port (default: 8000)")
    parser.add_argument("--db", default="data/bifrost.db", help="SQLite database path (default: data/bifrost.db)")
    parser.add_argument("--insecure", action="store_true", help="Run without authentication")
    parser.add_argument(
        "--oidc-issuer",
        default=os.environ.get("OIDC_ISSUER", ""),
        help="OIDC issuer URL (env: OIDC_ISSUER)",
    )
    parser.add_argument(
        "--oidc-client-id",
        default=os.environ.get("OIDC_CLIENT_ID", ""),
        help="OIDC client ID (env: OIDC_CLIENT_ID)",
    )
    parser.add_argument(
        "--oidc-client-secret",
        default=os.environ.get("OIDC_CLIENT_SECRET", ""),
        help="OIDC client secret (env: OIDC_CLIENT_SECRET)",
    )
    parser.add_argument(
        "--server-url",
        default=os.environ.get("SERVER_URL", ""),
        help="Public server URL for callbacks (env: SERVER_URL)",
    )

    args = parser.parse_args()

    from bifrost.config import Config
    from bifrost.app import create_app

    config = Config(
        host=args.host,
        port=args.port,
        db_path=args.db,
        insecure=args.insecure,
        oidc_issuer=args.oidc_issuer,
        oidc_client_id=args.oidc_client_id,
        oidc_client_secret=args.oidc_client_secret,
        server_url=args.server_url,
    )

    if not config.insecure and not config.oidc_issuer:
        parser.error("Either --insecure or --oidc-issuer must be provided")

    app = create_app(config)
    app.run(transport="streamable-http")


if __name__ == "__main__":
    main()
