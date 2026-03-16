#!/bin/sh
set -eu

# 确保备份目录存在
mkdir -p /backup/db

# 启动 supercronic（非 root cron 替代）
supercronic /etc/supercronic/xirang-backup &
CRON_PID=$!

/usr/local/bin/xirang &
XIRANG_PID=$!

trap 'kill -TERM $NGINX_PID $XIRANG_PID $CRON_PID 2>/dev/null; wait $NGINX_PID $XIRANG_PID $CRON_PID 2>/dev/null' TERM INT

# Wait for backend readiness before accepting traffic
attempts=0
max_attempts=30
until curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge "$max_attempts" ]; then
    echo "backend not ready after ${max_attempts}s, aborting" >&2
    exit 1
  fi
  sleep 1
done

# 通过 nginx 官方 entrypoint 处理模板（envsubst）后启动
/docker-entrypoint.sh nginx -g 'daemon off;' &
NGINX_PID=$!

# Update trap to cover all processes
trap 'kill -TERM $NGINX_PID $XIRANG_PID $CRON_PID 2>/dev/null; wait $NGINX_PID $XIRANG_PID $CRON_PID 2>/dev/null' TERM INT

# Wait for nginx; if it exits, clean up
wait $NGINX_PID
kill -TERM $XIRANG_PID $CRON_PID 2>/dev/null
wait $XIRANG_PID $CRON_PID 2>/dev/null
