#!/usr/bin/env bash
# macOS / Linux 安装 exam-agent（用户级，不需要 root）。
# 用法：install.sh <工作区目录> [exam-agent 二进制路径]
set -euo pipefail

WORKDIR=${1:?用法: install.sh <工作区> [二进制]}
BIN=${2:-$(cd "$(dirname "$0")/../.." && pwd)/bin/exam-agent}
HERE=$(cd "$(dirname "$0")" && pwd)

if [ ! -f "$WORKDIR/.exam-agent.json" ]; then
  echo "工作区缺少 .exam-agent.json：$WORKDIR" >&2
  exit 1
fi
WORKDIR=$(cd "$WORKDIR" && pwd)

install -m 0755 "$BIN" /usr/local/bin/exam-agent

case "$(uname -s)" in
  Darwin)
    PLIST="$HOME/Library/LaunchAgents/exam.agent.plist"
    mkdir -p "$(dirname "$PLIST")"
    sed "s#WORKDIR#$WORKDIR#g" "$HERE/com.exam.agent.plist" > "$PLIST"
    launchctl unload "$PLIST" 2>/dev/null || true
    launchctl load "$PLIST"
    echo "LaunchAgent 已加载：$PLIST"
    ;;
  Linux)
    UNIT_DIR="$HOME/.config/systemd/user"
    mkdir -p "$UNIT_DIR"
    sed "s#%h/exam-2027#$WORKDIR#g" "$HERE/exam-agent.service" > "$UNIT_DIR/exam-agent.service"
    systemctl --user daemon-reload
    systemctl --user enable --now exam-agent
    echo "systemd user unit 已启动：$UNIT_DIR/exam-agent.service"
    ;;
  *)
    echo "不支持的系统：$(uname -s)" >&2
    exit 1
    ;;
esac
