#!/usr/bin/env python3
"""首次启动准备可写配置；后续启动仅使用持久目录中的配置。"""
import os
from pathlib import Path
import secrets
import sys
import tempfile
import yaml

args = sys.argv[1:]
if not args:
    args = ["serve", "--config", str(Path.home() / ".hexclaw/hexclaw.yaml")]
if args[0] == "serve":
    config_path = Path.home() / ".hexclaw/hexclaw.yaml"
    for index, arg in enumerate(args):
        if arg == "--config" and index + 1 < len(args):
            config_path = Path(args[index + 1])
        elif arg.startswith("--config="):
            config_path = Path(arg.split("=", 1)[1])
    if not config_path.exists():
        seed_path = Path(os.environ.get("HEXCLAW_INIT_CONFIG_FILE", "/run/hexclaw-init/hexclaw.yaml"))
        if seed_path.is_file():
            body = seed_path.read_bytes()
            cfg = yaml.safe_load(body)
            if not isinstance(cfg, dict) or not cfg.get("server", {}).get("api_token"):
                raise SystemExit("Initial configuration requires server.api_token")
        else:
            token_path = os.environ.get("HEXCLAW_INIT_TOKEN_FILE")
            token = Path(token_path).read_text().strip() if token_path else secrets.token_urlsafe(48)
            if not token:
                raise SystemExit("Initial API token must not be empty")
            cfg = {"server": {"host": "0.0.0.0", "port": 16060, "mode": "production", "api_token": token},
                   "llm": {"default": "", "providers": {}, "routing": {"enabled": False}},
                   "storage": {"driver": "sqlite", "sqlite": {"path": str(Path.home() / ".hexclaw/data.db")}}}
            body = yaml.safe_dump(cfg, allow_unicode=True, sort_keys=False).encode()
        config_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd, temp_name = tempfile.mkstemp(prefix=".hexclaw-init-", dir=config_path.parent)
        try:
            with os.fdopen(fd, "wb") as output:
                output.write(body)
                output.flush()
                os.fsync(output.fileno())
            try:
                os.link(temp_name, config_path)
                print("Initialized persistent HexClaw configuration", flush=True)
            except FileExistsError:
                pass
        finally:
            os.unlink(temp_name)
os.execvp("hexclaw", ["hexclaw", *args])
