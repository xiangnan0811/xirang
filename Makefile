.PHONY: backend-run backend-test backend-build web-dev web-test web-build install-web dev prod-pull prod-up prod-down e2e-alert-demo e2e-check docker-build docker-push docker-buildx deploy-init setup-hooks lint lint-backend lint-frontend coverage coverage-backend coverage-frontend check test build clean

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

# ── Git Hooks ──
setup-hooks:
	git config core.hooksPath .githooks
	@echo "✅ Git hooks 已配置为 .githooks/ 目录"

# ── Quality & Testing ──
.PHONY: lint lint-backend lint-frontend coverage coverage-backend coverage-frontend check test build clean

lint: lint-backend lint-frontend ## Run all linters

lint-backend: ## Run golangci-lint
	cd backend && golangci-lint run ./...

lint-frontend: ## Run ESLint
	cd web && npm run lint

coverage: coverage-backend coverage-frontend ## Generate coverage reports

coverage-backend: ## Run backend tests with coverage
	cd backend && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

coverage-frontend: ## Run frontend tests with coverage
	cd web && npx vitest run --coverage

check: lint test build ## Full pre-commit quality gate

test: backend-test web-test ## Run all tests

build: backend-build web-build ## Build all

clean: ## Remove build artifacts
	rm -rf backend/xirang-server backend/coverage.out web/dist web/coverage

# ── Docker 镜像 ──
DOCKER_REGISTRY ?= docker.io
DOCKER_NAMESPACE ?= xirang
DOCKER_IMAGE ?= xirang
DOCKER_TAG ?= $(VERSION)
DOCKER_FULL_IMAGE = $(DOCKER_REGISTRY)/$(DOCKER_NAMESPACE)/$(DOCKER_IMAGE)

docker-build:
	docker build -f deploy/allinone/Dockerfile \
		-t $(DOCKER_FULL_IMAGE):$(DOCKER_TAG) \
		-t $(DOCKER_FULL_IMAGE):latest .

docker-push:
	docker push $(DOCKER_FULL_IMAGE):$(DOCKER_TAG)
	docker push $(DOCKER_FULL_IMAGE):latest

docker-buildx:
	docker buildx build --platform linux/amd64,linux/arm64 \
		-f deploy/allinone/Dockerfile \
		-t $(DOCKER_FULL_IMAGE):$(DOCKER_TAG) \
		-t $(DOCKER_FULL_IMAGE):latest --push .

# ── 部署初始化 ──
deploy-init:
	@mkdir -p deploy-kit
	@cp docker-compose.prod.yml deploy-kit/docker-compose.yml
	@cp .env.deploy deploy-kit/.env
	@echo "部署文件已生成到 deploy-kit/ 目录"
	@echo "1. 修改 deploy-kit/.env 中的密码和密钥"
	@echo "2. 将 deploy-kit/ 上传到目标服务器"
	@echo "3. 在服务器上执行: docker compose up -d"
