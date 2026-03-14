#!/bin/sh
set -eu

# 导出备份相关环境变量供 cron 使用（cron 不继承容器运行时环境变量）
printenv | grep -E '^(DB_TYPE|SQLITE_PATH|DB_DSN)=' | sed 's/^/export /' > /etc/backup-env || true
chmod 600 /etc/backup-env

# 确保备份目录存在
mkdir -p /backup/db

# 启动 cron 守护进程
cron

/usr/local/bin/xirang &
XIRANG_PID=$!

trap 'kill -TERM $XIRANG_PID 2>/dev/null; wait $XIRANG_PID 2>/dev/null' TERM INT

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

/docker-entrypoint.sh nginx -g 'daemon off;' &
NGINX_PID=$!

# Update trap to cover both processes
trap 'kill -TERM $NGINX_PID $XIRANG_PID 2>/dev/null; wait $NGINX_PID $XIRANG_PID 2>/dev/null' TERM INT

# Wait for nginx; if it exits, clean up backend
wait $NGINX_PID
kill -TERM $XIRANG_PID 2>/dev/null
wait $XIRANG_PID 2>/dev/null
