#!/usr/bin/env bash
# 默认预览；显式选择文件后应用，先完整预检，不删除未选择的种子。
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec python3 "$REPO_ROOT/scripts/sync-hub-embed.py" "$@"
