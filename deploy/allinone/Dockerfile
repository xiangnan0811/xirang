# Retained bases and complete APK inputs are pinned by deploy/build-inputs/lock.json.
FROM docker.io/linnea7171/xirang@sha256:19dd108ef4110db4d82c2ed6c811cae976c8ee4769615f05792a658c1fabc996 AS cgo-builder-inputs
FROM docker.io/linnea7171/xirang@sha256:9ad69af2963b57f42e2ce371e243739d3f4d90d64458ce4a77bd35123d7d4de4 AS supercronic-builder-inputs
FROM docker.io/linnea7171/xirang@sha256:117ea1705452f9626d349bff0e392134502afa2c14cb03673091643ab668c099 AS core-runtime-inputs

FROM docker.io/linnea7171/xirang@sha256:3e4c9ad8b3fd601ce93e5a8f7a77a639efe8d7d36846846c5179f89175f89d86 AS web-builder

WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

FROM docker.io/linnea7171/xirang@sha256:cf0703df5335d4c540833cee2f8a9c42afa97a9abacf01b715fe30fe837980a5 AS backend-builder

WORKDIR /app/backend
ARG TARGETOS
ARG TARGETARCH

COPY backend/go.mod backend/go.sum ./
RUN --network=none --mount=from=cgo-builder-inputs,source=/inputs,target=/build-inputs \
    sh /build-inputs/install.sh cgo-builder "$TARGETARCH" -- \
      gcc=15.2.0-r5 \
      musl-dev=1.2.6-r3

RUN go mod download

COPY backend/ ./
RUN set -eux; \
    target_os="${TARGETOS:-$(go env GOOS)}"; \
    target_arch="${TARGETARCH:-$(go env GOARCH)}"; \
    CGO_ENABLED=1 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags='-s -w' -o /out/xirang ./cmd/server; \
    CGO_ENABLED=1 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags='-s -w' -o /out/xirang-recover-admin ./cmd/recover-admin; \
    CGO_ENABLED=1 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags='-s -w' -o /out/xirang-recover-account ./cmd/recover-account; \
    CGO_ENABLED=1 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags='-s -w' -o /out/xirang-rsync-confined ./cmd/rsync-confined; \
    CGO_ENABLED=1 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags='-s -w' -o /out/xirang-cron-db-backup ./cmd/cron-db-backup

# 自行使用与后端一致的 Go 工具链编译 supercronic，避免官方预编译二进制带入过期 stdlib 的 HIGH CVE，
# 并支持 linux/amd64 与 linux/arm64 交叉编译。
FROM docker.io/linnea7171/xirang@sha256:cf0703df5335d4c540833cee2f8a9c42afa97a9abacf01b715fe30fe837980a5 AS supercronic-builder
ARG TARGETOS
ARG TARGETARCH
ARG SUPERCRONIC_COMMIT=c14d35dd8bb0d34e984097d5613cef2eea4a675f
RUN --network=none --mount=from=supercronic-builder-inputs,source=/inputs,target=/build-inputs \
    sh /build-inputs/install.sh supercronic-builder "$TARGETARCH" -- \
      git=2.54.0-r0
RUN set -eux; \
    git init /src; \
    git -C /src remote add origin https://github.com/aptible/supercronic.git; \
    git -C /src fetch --depth 1 origin "${SUPERCRONIC_COMMIT}"; \
    git -C /src checkout --detach FETCH_HEAD; \
    CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-amd64}" \
        go -C /src build -trimpath -ldflags='-s -w' -o /out/supercronic

FROM docker.io/linnea7171/xirang@sha256:c8faeaa02d9794e17ce55d4fd646b89614c71cc1eafb4e187dc3d3ce8dd759e9
ARG TARGETARCH

# Replay the complete verified APK transaction from retained build inputs.
# The explicit roots must match deploy/build-inputs/lock.json exactly.
# Security-fixed transitive libraries remain direct pins.
# PostgreSQL client utilities provide pg_dump/pg_restore for DB_TYPE=postgres.
RUN --network=none --mount=from=core-runtime-inputs,source=/inputs,target=/build-inputs \
    sh /build-inputs/install.sh core-runtime "$TARGETARCH" -- \
      bash=5.3.3-r1 \
      brotli-libs=1.2.1-r0 \
      c-ares=1.34.9-r0 \
      curl=8.23.0-r0 \
      libcrypto3=3.5.9-r0 \
      libexpat=2.9.0-r0 \
      libssl3=3.5.9-r0 \
      libuuid=2.41.6-r1 \
      libxml2=2.13.9-r1 \
      mount=2.41.6-r1 \
      nghttp2-libs=1.69.0-r0 \
      openssh-client-default=10.2_p1-r0 \
      pcre2=10.49-r0 \
      postgresql18-client=18.6-r0 \
      rsync=3.5.0-r0 \
      sqlite=3.53.4-r0 \
      tiff=4.7.2-r0 \
      tzdata=2026e-r0 \
      util-linux-misc=2.41.6-r1

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
COPY --from=backend-builder /out/xirang-recover-admin /usr/local/bin/xirang-recover-admin
COPY --from=backend-builder /out/xirang-recover-account /usr/local/bin/xirang-recover-account
COPY --from=backend-builder /out/xirang-rsync-confined /usr/local/bin/xirang-rsync-confined
COPY --from=backend-builder /out/xirang-cron-db-backup /usr/local/bin/xirang-cron-db-backup
COPY deploy/nginx/templates/default.conf.template /etc/nginx/templates/default.conf.template
COPY deploy/allinone/entrypoint.sh /entrypoint.sh
COPY scripts/backup-db.sh /usr/local/bin/backup-db.sh
COPY deploy/allinone/xirang-backup.cron /etc/supercronic/xirang-backup

RUN chmod +x /usr/local/bin/backup-db.sh /entrypoint.sh /usr/local/bin/xirang-rsync-confined /usr/local/bin/xirang-cron-db-backup && \
    mkdir -p /data /backup/db /logs /var/cache/xirang/asset-content /etc/supercronic && \
    chown -R xirang:xirang /data /backup /logs /var/cache/xirang /usr/share/nginx/html /etc/supercronic /etc/nginx/templates

# 后端监听 3000（容器内部），nginx 监听 10761 对外提供服务
ENV SERVER_ADDR=:3000
ENV DB_TYPE=sqlite
ENV SQLITE_PATH=/data/xirang.db
ENV CRON_DB_BACKUP_DIR=/backup/db
# cron 作业状态记录持久化在备份卷中，与产物清理范围分离
ENV CRON_DB_BACKUP_STATE_DIR=/backup/.cron-db-state
# known_hosts 默认存放在持久化卷，容器重启不丢失
ENV SSH_KNOWN_HOSTS_PATH=/data/.ssh/known_hosts
ENV LOG_FILE=/logs/xirang.log
# 默认时区：避免容器落入 UTC 导致备份/日志时间戳与运维偏差；运行时可通过 -e TZ= 覆盖
ENV TZ=Asia/Shanghai

VOLUME ["/data", "/backup", "/logs"]
EXPOSE 10761

# 镜像层健康检查兜底（compose 已配 readiness healthcheck，此处保证裸 docker run 也能监控）
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl -fsS http://127.0.0.1:10761/readyz || exit 1

# 不设置 USER，由 entrypoint 在修复 bind mount 权限后切换用户
ENTRYPOINT ["/entrypoint.sh"]
