FROM golang:1.23-alpine AS builder

WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/xirang ./cmd/server

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
