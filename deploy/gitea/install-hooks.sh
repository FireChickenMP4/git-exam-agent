#!/usr/bin/env bash
# 把 exam-hook 安装到 Gitea 所有仓库的 pre-receive.d。
# 用法：deploy/gitea/install-hooks.sh [linux 版 exam-hook 二进制路径]
set -euo pipefail

COMPOSE_FILE=${COMPOSE_FILE:-deploy/docker-compose.example.yml}
BIN=${1:-bin/exam-hook-linux-amd64}

if [ ! -f "$BIN" ]; then
  echo "找不到 $BIN，先执行：make dist" >&2
  exit 1
fi

cp "$BIN" deploy/gitea/custom/hooks/exam-hook
chmod +x deploy/gitea/custom/hooks/exam-hook deploy/gitea/custom/hooks/pre-receive.d/exam-hook

docker compose -f "$COMPOSE_FILE" exec -T gitea sh -c '
set -e
chmod +x /data/gitea/custom/hooks/exam-hook /data/gitea/custom/hooks/pre-receive.d/exam-hook
for repo in /data/git/repositories/*/*.git; do
  [ -d "$repo" ] || continue
  mkdir -p "$repo/hooks/pre-receive.d"
  cp /data/gitea/custom/hooks/pre-receive.d/exam-hook "$repo/hooks/pre-receive.d/exam-hook"
  chmod +x "$repo/hooks/pre-receive.d/exam-hook"
done
'

echo "pre-receive 钩子已安装到所有仓库"
