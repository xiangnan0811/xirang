.PHONY: backend-run backend-test backend-build web-dev web-test web-build install-web dev prod-pull prod-up prod-down e2e-alert-demo e2e-check

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X xirang/backend/internal/version.Version=$(VERSION) \
           -X xirang/backend/internal/version.BuildTime=$(BUILD_TIME) \
           -X xirang/backend/internal/version.GitCommit=$(GIT_COMMIT)

backend-run:
	cd backend && go run ./cmd/server

backend-test:
	cd backend && go test ./...

backend-build:
	cd backend && go build -ldflags "$(LDFLAGS)" -o xirang-server ./cmd/server

install-web:
	cd web && npm install

web-dev:
	cd web && npm run dev

web-test:
	cd web && npm test

web-build:
	cd web && npm run build

dev:
	@echo "请开两个终端执行：make backend-run 和 make web-dev"

prod-pull:
	docker compose -f docker-compose.prod.yml pull

prod-up:
	docker compose -f docker-compose.prod.yml up -d

prod-down:
	docker compose -f docker-compose.prod.yml down


e2e-alert-demo:
	bash scripts/e2e-alert-demo.sh


e2e-check:
	bash scripts/smoke-e2e.sh
