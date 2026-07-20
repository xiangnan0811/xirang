FROM golang:1.26.5-alpine AS worker-builder

WORKDIR /src/backend
ARG TARGETOS
ARG TARGETARCH

COPY backend/go.mod backend/go.sum ./
RUN apk add --no-cache gcc musl-dev
RUN go mod download

COPY backend/ ./
RUN set -eux; \
    target_os="${TARGETOS:-$(go env GOOS)}"; \
    target_arch="${TARGETARCH:-$(go env GOARCH)}"; \
    CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -ldflags='-s -w' -o /out/asset-worker ./cmd/asset-worker; \
    CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -ldflags='-s -w' -o /out/asset-tool-sandbox ./cmd/asset-tool-sandbox; \
    CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -ldflags='-s -w' -o /out/asset-worker-updater ./cmd/asset-worker-updater

# alpine:3.23 multi-architecture manifest, resolved from Docker Hub on 2026-07-12.
FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40

RUN apk upgrade --no-cache && \
    apk add --no-cache \
      bash \
      clamav \
      ffmpeg \
      font-noto \
      font-noto-cjk \
      libreoffice \
      poppler-utils \
      tesseract-ocr \
      tesseract-ocr-data-chi_sim \
      tesseract-ocr-data-eng \
      vips-tools \
      gzip \
      xz \
      zstd && \
    addgroup -S -g 10000 asset-worker && \
    adduser -S -D -H -u 10000 -G asset-worker -s /sbin/nologin asset-worker && \
    addgroup -S -g 10002 asset-updater && \
    adduser -S -D -H -u 10002 -G asset-updater -s /sbin/nologin asset-updater && \
    addgroup asset-worker asset-updater && \
    mkdir -p /run/xirang/asset-jobs /var/lib/xirang/asset-worker-bundles /var/lib/xirang/asset-worker-inbox && \
    touch /etc/ld.so.cache && \
    chown 10000:10000 /run/xirang/asset-jobs && \
    chown 10002:10002 /var/lib/xirang/asset-worker-bundles && \
    chmod 0700 /run/xirang/asset-jobs && \
    chmod 0750 /var/lib/xirang/asset-worker-bundles && \
    chmod 0555 /var/lib/xirang/asset-worker-inbox && \
    chmod 0444 /etc/ld.so.cache

COPY --from=worker-builder /out/asset-worker /usr/local/bin/asset-worker
COPY --from=worker-builder /out/asset-tool-sandbox /usr/local/bin/asset-tool-sandbox
COPY --from=worker-builder /out/asset-worker-updater /usr/local/bin/asset-worker-updater
COPY deploy/worker/entrypoint.sh /usr/local/bin/asset-worker-entrypoint

RUN chmod 0555 \
      /usr/local/bin/asset-worker \
      /usr/local/bin/asset-tool-sandbox \
      /usr/local/bin/asset-worker-updater \
      /usr/local/bin/asset-worker-entrypoint

ENV HOME=/nonexistent
ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8
ENV TZ=UTC

USER 10000:10000
ENTRYPOINT ["/usr/local/bin/asset-worker-entrypoint"]
CMD ["worker"]
