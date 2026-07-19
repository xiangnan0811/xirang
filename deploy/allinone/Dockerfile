FROM node:20-alpine AS web-builder

WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

FROM golang:1.26.5-alpine AS backend-builder

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

# 自行使用与后端一致的 Go 工具链编译 supercronic，避免官方预编译二进制带入过期 stdlib 的 HIGH CVE，
# 并支持 linux/amd64 与 linux/arm64 交叉编译。
FROM golang:1.26.5-alpine AS supercronic-builder
ARG TARGETOS
ARG TARGETARCH
ARG SUPERCRONIC_VERSION=v0.2.44
RUN apk add --no-cache git
RUN set -eux; \
    git clone --depth 1 --branch "${SUPERCRONIC_VERSION}" https://github.com/aptible/supercronic.git /src; \
    cd /src; \
    CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-amd64}" \
        go build -trimpath -ldflags='-s -w' -o /out/supercronic

FROM nginx:1.29-alpine

# 升级基础镜像已安装包到最新补丁版本（解 libxml2 / musl / zlib 等 HIGH CVE）
# 再安装运行期依赖：rsync/openssh-client（任务执行）、sqlite（备份 WAL 一致性）、bash（脚本）、curl（健康检查）、tzdata（时区数据）
RUN apk upgrade --no-cache && \
    apk add --no-cache curl rsync openssh-client sqlite bash tzdata

COPY --from=supercronic-builder /out/supercronic /usr/local/bin/supercronic
RUN chmod +x /usr/local/bin/supercronic

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
COPY deploy/allinone/entrypoint.sh /entrypoint.sh
COPY scripts/backup-db.sh /usr/local/bin/backup-db.sh
COPY deploy/allinone/xirang-backup.cron /etc/supercronic/xirang-backup

RUN chmod +x /usr/local/bin/backup-db.sh /entrypoint.sh && \
    mkdir -p /data /backup/db /logs /var/cache/xirang/asset-content /etc/supercronic && \
    chown -R xirang:xirang /data /backup /logs /var/cache/xirang /usr/share/nginx/html /etc/supercronic /etc/nginx/templates

# 后端监听 3000（容器内部），nginx 监听 10761 对外提供服务
ENV SERVER_ADDR=:3000
ENV DB_TYPE=sqlite
ENV SQLITE_PATH=/data/xirang.db
# known_hosts 默认存放在持久化卷，容器重启不丢失
ENV SSH_KNOWN_HOSTS_PATH=/data/.ssh/known_hosts
ENV LOG_FILE=/logs/xirang.log
# 默认时区：避免容器落入 UTC 导致备份/日志时间戳与运维偏差；运行时可通过 -e TZ= 覆盖
ENV TZ=Asia/Shanghai

VOLUME ["/data", "/backup", "/logs"]
EXPOSE 10761

# 镜像层健康检查兜底（compose 已配 healthcheck，此处保证裸 docker run 也能监控）
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl -fsS http://127.0.0.1:10761/healthz || exit 1

# 不设置 USER，由 entrypoint 在修复 bind mount 权限后切换用户
ENTRYPOINT ["/entrypoint.sh"]
