FROM node:20-alpine AS web-builder

WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

FROM golang:1.26 AS backend-builder

WORKDIR /app/backend
ARG TARGETOS
ARG TARGETARCH

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN set -eux; \
    target_os="${TARGETOS:-$(go env GOOS)}"; \
    target_arch="${TARGETARCH:-$(go env GOARCH)}"; \
    CGO_ENABLED=1 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags='-s -w' -o /out/xirang ./cmd/server

FROM nginx:1.27

# 安装 supercronic（非 root cron 替代）和 curl（健康检查）
ARG TARGETARCH
RUN set -eux; \
    arch="${TARGETARCH:-amd64}"; \
    apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*; \
    curl -fsSL "https://github.com/aptible/supercronic/releases/download/v0.2.33/supercronic-linux-${arch}" -o /usr/local/bin/supercronic; \
    chmod +x /usr/local/bin/supercronic

# 创建非 root 用户
RUN groupadd -r -g 10000 xirang && useradd -r -u 10000 -g xirang -s /sbin/nologin xirang

# 配置 nginx 以非 root 运行：PID 文件和临时目录
RUN sed -i 's|/var/run/nginx.pid|/tmp/nginx.pid|g' /etc/nginx/nginx.conf && \
    mkdir -p /tmp/client_temp /tmp/proxy_temp /tmp/fastcgi_temp /tmp/uwsgi_temp /tmp/scgi_temp && \
    chown -R xirang:xirang /var/cache/nginx /var/log/nginx /etc/nginx/conf.d /tmp/*_temp && \
    chmod -R 755 /var/cache/nginx /var/log/nginx

COPY --from=web-builder /app/web/dist /usr/share/nginx/html
COPY --from=backend-builder /out/xirang /usr/local/bin/xirang
COPY deploy/nginx/templates/default.conf.template /etc/nginx/templates/default.conf.template
COPY deploy/allinone/entrypoint.sh /entrypoint.sh
COPY scripts/backup-db.sh /usr/local/bin/backup-db.sh
COPY deploy/allinone/xirang-backup.cron /etc/supercronic/xirang-backup

RUN chmod +x /usr/local/bin/backup-db.sh /entrypoint.sh && \
    mkdir -p /data /backup/db /etc/supercronic && \
    chown -R xirang:xirang /data /backup /usr/share/nginx/html /etc/supercronic

ENV BACKEND_UPSTREAM=http://127.0.0.1:8080
ENV SERVER_ADDR=:8080
ENV DB_TYPE=sqlite
ENV SQLITE_PATH=/data/xirang.db

VOLUME ["/data", "/backup"]
EXPOSE 8080 8443

USER xirang

ENTRYPOINT ["/entrypoint.sh"]
