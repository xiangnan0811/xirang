#!/bin/sh
set -eu

# 修复 bind mount 目录权限（宿主机目录可能是 root 所有）
chown -R xirang:xirang /data /backup 2>/dev/null || true
mkdir -p /backup/db
chown xirang:xirang /backup/db 2>/dev/null || true

# 确保 known_hosts 目录存在于持久化卷中
mkdir -p /data/.ssh
chown xirang:xirang /data/.ssh

# 自动检测 TLS 证书，选择 HTTP 或 HTTPS 模式
if [ -f /etc/nginx/certs/fullchain.pem ] && [ -f /etc/nginx/certs/privkey.pem ]; then
  echo "==> TLS 证书已检测到，启用 HTTPS 模式"
else
  echo "==> 未检测到 TLS 证书，使用 HTTP 模式（端口 8080）"
  cp /etc/xirang/nginx-http.conf.template /etc/nginx/templates/default.conf.template
fi

# 以 xirang 用户启动 supercronic
su -s /bin/sh xirang -c 'supercronic /etc/supercronic/xirang-backup' &
CRON_PID=$!

# 以 xirang 用户启动后端
su -s /bin/sh xirang -c '/usr/local/bin/xirang' &
XIRANG_PID=$!

trap 'kill -TERM $NGINX_PID $XIRANG_PID $CRON_PID 2>/dev/null; wait $NGINX_PID $XIRANG_PID $CRON_PID 2>/dev/null' TERM INT

# 等待后端就绪（后端监听在容器内部端口 3000）
attempts=0
max_attempts=30
until curl -fsS http://127.0.0.1:3000/healthz >/dev/null 2>&1; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge "$max_attempts" ]; then
    echo "==> 后端未在 ${max_attempts}s 内就绪，中止启动" >&2
    exit 1
  fi
  sleep 1
done

echo "==> 后端已就绪，启动 nginx"

# 通过 nginx 官方 entrypoint 处理模板（envsubst）后启动
/docker-entrypoint.sh nginx -g 'daemon off;' &
NGINX_PID=$!

# 更新 trap 覆盖所有进程
trap 'kill -TERM $NGINX_PID $XIRANG_PID $CRON_PID 2>/dev/null; wait $NGINX_PID $XIRANG_PID $CRON_PID 2>/dev/null' TERM INT

# 等待 nginx；如果退出则清理
wait $NGINX_PID
kill -TERM $XIRANG_PID $CRON_PID 2>/dev/null
wait $XIRANG_PID $CRON_PID 2>/dev/null
