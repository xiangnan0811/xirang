FROM node:20-alpine AS web-builder

WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

FROM golang:1.24 AS backend-builder

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

RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*

COPY --from=web-builder /app/web/dist /usr/share/nginx/html
COPY --from=backend-builder /out/xirang /usr/local/bin/xirang
COPY deploy/nginx/templates/default.conf.template /etc/nginx/templates/default.conf.template
COPY deploy/allinone/entrypoint.sh /entrypoint.sh

ENV BACKEND_UPSTREAM=http://127.0.0.1:8080
ENV SERVER_ADDR=:8080
ENV DB_TYPE=sqlite
ENV SQLITE_PATH=/data/xirang.db

VOLUME ["/data", "/backup"]
EXPOSE 80 443

ENTRYPOINT ["/entrypoint.sh"]
