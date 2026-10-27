# Retained bases and complete APK inputs are pinned by deploy/build-inputs/lock.json.
FROM docker.io/linnea7171/xirang@sha256:9d1c2af88e090059ff4f673562458e8688c22295642385f683ca48e6ce004883 AS cgo-builder-inputs
FROM docker.io/linnea7171/xirang@sha256:20abdfea5e6bfba595df4177d2a93438d4fd9bc3fac565ad0a13c26976778d8c AS worker-runtime-inputs

# Retained golang:1.27.3-alpine3.24 platform images; upstream identities are in the lock.
FROM docker.io/linnea7171/xirang@sha256:cf0703df5335d4c540833cee2f8a9c42afa97a9abacf01b715fe30fe837980a5 AS worker-builder

WORKDIR /src/backend
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
    CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -ldflags='-s -w' -o /out/asset-worker ./cmd/asset-worker; \
    CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -ldflags='-s -w' -o /out/asset-tool-sandbox ./cmd/asset-tool-sandbox; \
    CGO_ENABLED=1 GOOS="$target_os" GOARCH="$target_arch" \
      go build -trimpath -ldflags='-s -w' -o /out/asset-worker-updater ./cmd/asset-worker-updater

# Retained alpine:3.23 platform images; upstream identities are in the lock.
FROM docker.io/linnea7171/xirang@sha256:f918df6f5dc296d250d2f59bfb8a40ec16933df94c6894a91085f4ad7added8d
ARG TARGETARCH

# Keep the complete APK transaction outside the final filesystem. Its retained
# manifest and explicit roots are checked before an offline installation.
RUN --network=none --mount=from=worker-runtime-inputs,source=/inputs,target=/build-inputs \
    sh /build-inputs/install.sh worker-runtime "$TARGETARCH" -- \
      bash=5.3.3-r1 \
      clamav=1.4.4-r0 \
      ffmpeg=8.0.1-r1 \
      font-noto-cjk=0_git20220127-r1 \
      font-noto=2025.12.01-r0 \
      gzip=1.14-r3 \
      libcrypto3=3.5.9-r0 \
      libreoffice=25.8.1.1-r5 \
      libssl3=3.5.9-r0 \
      poppler-utils=25.12.0-r0 \
      tesseract-ocr-data-chi_sim=5.5.1-r0 \
      tesseract-ocr-data-eng=5.5.1-r0 \
      tesseract-ocr=5.5.1-r0 \
      vips-tools=8.17.3-r1 \
      xz=5.8.4-r0 \
      zstd=1.5.7-r2

RUN addgroup -S -g 10000 asset-worker && \
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
