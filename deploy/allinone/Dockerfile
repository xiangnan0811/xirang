FROM node:20-alpine AS web-builder

WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS backend-builder

WORKDIR /app/backend
ARG TARGETOS
ARG TARGETARCH

COPY backend/go.mod backend/go.sum ./
RUN apk add --no-cache gcc musl-dev
RUN go mod download

COPY backend/ ./
RUN set -eux; \
    target_os="${TARGETOS:-$(go env GOOS)}"; \
    target_arch="${TARGETARCH:-$(go env GOARCH)}"; \
    CGO_ENABLED=1 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags='-s -w' -o /out/xirang ./cmd/server

FROM nginx:1.27-alpine

# 安装 supercronic（非 root cron 替代）、sqlite3（WAL 一致性备份）和 curl（健康检查）
ARG TARGETARCH
RUN set -eux; \
    arch="${TARGETARCH:-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')}"; \
    case "${arch}" in \
      amd64) supercronic_sha256="feefa310da569c81b99e1027b86b27b51e6ee9ab647747b49099645120cfc671" ;; \
      arm64) supercronic_sha256="f1f8585c66de020fef494dd636058f99949d108f569fef00016a1c8b9eb145b3" ;; \
      *) echo "unsupported supercronic arch: ${arch}" >&2; exit 1 ;; \
    esac; \
    apk add --no-cache curl rsync openssh-client sqlite bash; \
    curl -fsSL "https://github.com/aptible/supercronic/releases/download/v0.2.33/supercronic-linux-${arch}" \
         -o /usr/local/bin/supercronic; \
    echo "${supercronic_sha256}  /usr/local/bin/supercronic" | sha256sum -c -; \
    chmod +x /usr/local/bin/supercronic

# 创建非 root 用户
RUN addgroup -S -g 10000 xirang && adduser -S -u 10000 -G xirang -h /home/xirang -s /sbin/nologin xirang

# 配置 nginx 以非 root 运行：PID 文件和临时目录
RUN sed -i -e 's|/var/run/nginx.pid|/tmp/nginx.pid|g' -e 's| /run/nginx.pid| /tmp/nginx.pid|g' /etc/nginx/nginx.conf && \
    mkdir -p /tmp/client_temp /tmp/proxy_temp /tmp/fastcgi_temp /tmp/uwsgi_temp /tmp/scgi_temp && \
    chown -R xirang:xirang /var/cache/nginx /var/log/nginx /etc/nginx/conf.d /tmp/*_temp && \
    chmod -R 755 /var/cache/nginx /var/log/nginx

COPY --from=web-builder /app/web/dist /usr/share/nginx/html
COPY --from=backend-builder /out/xirang /usr/local/bin/xirang
COPY deploy/nginx/templates/default.conf.template /etc/nginx/templates/default.conf.template
COPY deploy/nginx/templates/default-http.conf.template /etc/xirang/nginx-http.conf.template
COPY deploy/allinone/entrypoint.sh /entrypoint.sh
COPY scripts/backup-db.sh /usr/local/bin/backup-db.sh
COPY deploy/allinone/xirang-backup.cron /etc/supercronic/xirang-backup

RUN chmod +x /usr/local/bin/backup-db.sh /entrypoint.sh && \
    mkdir -p /data /backup/db /etc/supercronic /etc/xirang && \
    chown -R xirang:xirang /data /backup /usr/share/nginx/html /etc/supercronic /etc/xirang /etc/nginx/templates

# 后端监听 3000（容器内部），nginx 监听 8080/8443 对外提供服务
ENV BACKEND_UPSTREAM=http://127.0.0.1:3000
ENV SERVER_ADDR=:3000
ENV DB_TYPE=sqlite
ENV SQLITE_PATH=/data/xirang.db
# known_hosts 默认存放在持久化卷，容器重启不丢失
ENV SSH_KNOWN_HOSTS_PATH=/data/.ssh/known_hosts

VOLUME ["/data", "/backup"]
EXPOSE 8080 8443

# 不设置 USER，由 entrypoint 在修复 bind mount 权限后切换用户
ENTRYPOINT ["/entrypoint.sh"]
