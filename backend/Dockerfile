FROM golang:1.24-alpine AS builder

WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata
ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN set -eux; \
    target_os="${TARGETOS:-$(go env GOOS)}"; \
    target_arch="${TARGETARCH:-$(go env GOARCH)}"; \
    CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags='-s -w' -o /out/xirang ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder /out/xirang /app/xirang
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENV SERVER_ADDR=:8080
ENV DB_TYPE=sqlite
ENV SQLITE_PATH=/data/xirang.db

VOLUME ["/data", "/backup"]
EXPOSE 8080

ENTRYPOINT ["/app/xirang"]
