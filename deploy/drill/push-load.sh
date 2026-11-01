#!/usr/bin/env bash
# 模拟考生并发推送（V-17 / 10 人模拟）。
#
# 用法：
#   deploy/drill/push-load.sh slips.csv [人数] [轮数]
#
# slips.csv 由 examd provision -slips 生成，列：student_no,name,repo_url,token
set -euo pipefail

SLIPS=${1:?用法: push-load.sh slips.csv [人数] [轮数]}
COUNT=${2:-10}
ROUNDS=${3:-1}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

auth_url() {
  local student=$1 url=$2 token=$3
  local scheme=${url%%://*}
  local rest=${url#*://}
  echo "${scheme}://${student}:${token}@${rest}"
}

clone_one() {
  local student=$1 url=$2 token=$3
  git clone -q "$(auth_url "$student" "$url" "$token")" "$TMP/$student"
}

push_one() {
  local student=$1 round=$2
  local dir="$TMP/$student"
  printf '\n// drill round %s\n' "$round" >> "$dir/main.go"
  git -C "$dir" -c user.name="$student" -c user.email="$student@exam.local" \
    commit -qam "drill: round $round"
  git -C "$dir" push -q origin main
}

echo "读取 $SLIPS ..."
n=0
while IFS=, read -r student name url token; do
  [ "$student" = "student_no" ] && continue
  n=$((n + 1))
  [ "$n" -gt "$COUNT" ] && break
  clone_one "$student" "$url" "$token" &
done < "$SLIPS"
wait
echo "已 clone $((n > COUNT ? COUNT : n)) 个仓库"

start=$(date +%s)
for round in $(seq 1 "$ROUNDS"); do
  echo "第 $round 轮并发推送 ..."
  n=0
  while IFS=, read -r student name url token; do
    [ "$student" = "student_no" ] && continue
    n=$((n + 1))
    [ "$n" -gt "$COUNT" ] && break
    push_one "$student" "$round" &
  done < "$SLIPS"
  wait
done
end=$(date +%s)

echo "完成：$ROUNDS 轮 x 最多 $COUNT 人，耗时 $((end - start)) 秒"
