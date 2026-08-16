# golang:1.26.6-alpine multi-architecture manifest, resolved from Docker Hub on 2026-08-16.
FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS worker-builder

WORKDIR /src/backend
ARG TARGETOS
ARG TARGETARCH

COPY backend/go.mod backend/go.sum ./
RUN apk add --no-cache \
      gcc=15.2.0-r5 \
      musl-dev=1.2.6-r2
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

RUN apk add --no-cache \
      bash=5.3.3-r1 \
      clamav=1.4.4-r0 \
      ffmpeg=8.0.1-r1 \
      font-noto=2025.12.01-r0 \
      font-noto-cjk=0_git20220127-r1 \
      libreoffice=25.8.1.1-r5 \
      poppler-utils=25.12.0-r0 \
      tesseract-ocr=5.5.1-r0 \
      tesseract-ocr-data-chi_sim=5.5.1-r0 \
      tesseract-ocr-data-eng=5.5.1-r0 \
      vips-tools=8.17.3-r1 \
      gzip=1.14-r2 \
      xz=5.8.3-r0 \
      zstd=1.5.7-r2 && \
    addgroup -S -g 10000 asset-worker && \
	    adduser -S -D -H -u 10000 -G asset-worker -s /sbin/nologin asset-worker && \
	    addgroup -S -g 10002 asset-updater && \
	    adduser -S -D -H -u 10002 -G asset-updater -s /sbin/nologin asset-updater && \
	    sed -i -E 's/^(clamav|asset-worker|asset-updater):([^:]*):[^:]*/\1:\2:/' /etc/shadow && \
	    chown 0:42 /etc/shadow && \
	    chmod 0640 /etc/shadow && \
	    test "$(stat -c '%a:%u:%g' /etc/shadow)" = "640:0:42" && \
	    test -z "$(awk -F: '$1 == "clamav" || $1 == "asset-worker" || $1 == "asset-updater" { if ($3 != "") print $1 }' /etc/shadow)" && \
	    mkdir -p /run/xirang/worker /run/xirang/asset-jobs /var/lib/xirang/asset-worker-bundles /var/lib/xirang/asset-worker-inbox && \
    touch /etc/ld.so.cache && \
    chown 10000:10000 /run/xirang/worker /run/xirang/asset-jobs && \
    chown 10002:10000 /var/lib/xirang/asset-worker-bundles && \
	    chmod 0700 /run/xirang/worker /run/xirang/asset-jobs && \
	    chmod 02750 /var/lib/xirang/asset-worker-bundles && \
	    chmod 0555 /var/lib/xirang/asset-worker-inbox && \
	    chmod 0444 /etc/ld.so.cache && \
	    chmod 0755 /etc/network/if-up.d/dad && \
	    rm -f /etc/crontabs/root /etc/shadow- /lib/apk/db/lock && \
	    rm -f /var/cache/fontconfig/*.cache-* /var/log/apk.log

COPY --from=worker-builder /out/asset-worker /usr/local/bin/asset-worker
COPY --from=worker-builder /out/asset-tool-sandbox /usr/local/bin/asset-tool-sandbox
COPY --from=worker-builder /out/asset-worker-updater /usr/local/bin/asset-worker-updater
COPY deploy/worker/entrypoint.sh /usr/local/bin/asset-worker-entrypoint

RUN chmod 0555 \
      /usr/local/bin/asset-worker \
      /usr/local/bin/asset-tool-sandbox \
      /usr/local/bin/asset-worker-updater \
      /usr/local/bin/asset-worker-entrypoint

RUN ! find / -xdev -type f -perm /022 -print -quit | grep -q . && \
	    mkdir -p /usr/local/share/xirang && \
    /usr/local/bin/asset-worker write-runtime-closure-manifest && \
    test "$(stat -c '%a:%u:%g' /usr/local/share/xirang/runtime-closure.v1.json)" = "444:0:0"

ENV HOME=/nonexistent
ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8
ENV TZ=UTC

USER 10000:10000
ENTRYPOINT ["/usr/local/bin/asset-worker-entrypoint"]
CMD ["worker"]
