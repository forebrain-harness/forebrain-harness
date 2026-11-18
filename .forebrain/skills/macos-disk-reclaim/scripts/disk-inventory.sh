#!/usr/bin/env bash
# 只读盘点：不删除任何东西。在仓库根执行。
set -uo pipefail

echo "== 数据卷（真正的容量在这里）"
df -h /System/Volumes/Data | tail -1

echo
echo "== Docker 宿主占用（Docker.raw 是 sparse，必看 du 不看 ls）"
du -sh "$HOME/Library/Containers/com.docker.docker" 2>/dev/null
du -h "$HOME/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw" 2>/dev/null

echo
echo "== Docker 内部（RECLAIMABLE 0% 多半是卷被已停止容器引用）"
docker system df 2>&1 | head -6
docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}' 2>&1 | head -20
docker volume ls 2>&1 | head -20

echo
echo "== 用户缓存大户"
du -sh "$HOME/Library/Caches"/* 2>/dev/null | sort -rh | head -10

echo
echo "== 应用容器（数据，勿删）"
du -sh "$HOME/Library/Containers"/* 2>/dev/null | sort -rh | head -8

echo
echo "== 日志"
du -sh "$HOME/Library/Logs" "$HOME/.forebrain/logs" 2>/dev/null

echo
echo "== Go 缓存"
du -sh "$(go env GOCACHE)" "$(go env GOMODCACHE)" 2>/dev/null

echo
echo "== workspace"
du -sh "$HOME"/workspace/* 2>/dev/null | sort -rh | head -6

echo
echo "== 会话库（数据，勿删）"
ls -lh "$HOME"/.forebrain/state/forebrain.state.sqlite* 2>/dev/null
