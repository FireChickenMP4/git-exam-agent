#!/usr/bin/env bash
# 初始化考场 Gitea：创建站点管理员，并生成 examd 用的管理员令牌。
set -euo pipefail

COMPOSE_FILE=${COMPOSE_FILE:-deploy/docker-compose.example.yml}
GITEA_URL=${GITEA_URL:-http://127.0.0.1:3000}
ADMIN_USER=${GITEA_ADMIN_USER:-exam-admin}
ADMIN_PASS=${GITEA_ADMIN_PASSWORD:?请先设置 GITEA_ADMIN_PASSWORD}

docker compose -f "$COMPOSE_FILE" exec -T gitea gitea admin user create \
  --admin \
  --username "$ADMIN_USER" \
  --password "$ADMIN_PASS" \
  --email exam-admin@exam.local \
  --must-change-password=false || true

echo "管理员账号已就绪：$ADMIN_USER"

token=$(curl -fsS -u "$ADMIN_USER:$ADMIN_PASS" \
  -H 'Content-Type: application/json' \
  -X POST "$GITEA_URL/api/v1/users/$ADMIN_USER/tokens" \
  -d '{"name":"examd","scopes":["all"]}' 2>/dev/null |
  sed -n 's/.*"sha1":"\([^"]*\)".*/\1/p' || true)

if [ -n "$token" ]; then
  echo
  echo "把下面这行写入 deploy/.env："
  echo "GITEA_ADMIN_TOKEN=$token"
else
  echo "未能自动创建令牌，请在 Gitea 网页 设置 -> 应用 -> 生成令牌 后填入 GITEA_ADMIN_TOKEN"
fi
