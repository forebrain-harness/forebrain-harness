#!/usr/bin/env bash
# 清 Docker 未使用资源，然后让宿主 Docker.raw 真正缩小。
#
# 会删除未使用的镜像/容器/卷，以及（可选）本仓 scripts/test-db.sh 起的
# fb-mysql / fb-pg 临时库。执行前会打印现存容器并要求确认。
#
# 用法：
#   ./scripts/docker-trim.sh
#   SCRATCH_DBS=1 ./scripts/docker-trim.sh     # 连 fb-mysql/fb-pg 及其匿名卷一起删
#
# 镜像站前缀按本机网络设置；docker.io 直连不通时用镜像站（见 SKILL.md 故障 4）。
set -euo pipefail

TRIM_IMAGE=${TRIM_IMAGE:-alpine:3}
DATA_VOLUME=/System/Volumes/Data
RAW="$HOME/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw"

probe() { curl -sS --max-time 10 --unix-socket "$HOME/.docker/run/docker.sock" http://localhost/_ping; }

echo "== 探活"
if ! probe 2>/dev/null | grep -q OK; then
  echo "守护进程无响应：日志看 ~/Library/Containers/com.docker.docker/Data/log/host/supervisor.log"
  echo "恢复：kill -9 <com.docker.backend PID> && open -a Docker，然后重跑本脚本"
  exit 1
fi

echo
echo "== 清理前"
df -h "$DATA_VOLUME" | tail -1
du -h "$RAW" 2>/dev/null || true
docker system df

echo
echo "== 现存容器"
docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}'
echo "== 匿名卷（64 位十六进制名）"
docker volume ls --format '{{.Name}}\t{{.Driver}}'
echo
read -r -p '继续删除未使用的容器/镜像/卷？[y/N] ' ans
[[ ${ans:-} == y ]] || { echo "取消"; exit 1; }

if [[ ${SCRATCH_DBS:-0} == 1 ]]; then
  # scripts/test-db.sh down 只删容器不删卷，匿名卷会一直累积。
  docker rm -f fb-mysql fb-pg >/dev/null 2>&1 || true
fi

docker system prune -af

echo
echo "== VM 内 fstrim（删除后宿主 Docker.raw 不会自己缩小）"
docker run --rm --privileged --pid=host "$TRIM_IMAGE" nsenter -t 1 -m -- fstrim -av || {
  echo "trim 镜像拉不到时先补：docker pull <mirror>/library/alpine:3"; exit 1; }
docker rmi "$TRIM_IMAGE" >/dev/null 2>&1 || true

echo
echo "== 清理后"
df -h "$DATA_VOLUME" | tail -1
du -h "$RAW" 2>/dev/null || true
docker system df
