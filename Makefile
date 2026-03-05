.PHONY: backend-run backend-test backend-build web-dev web-test web-build install-web dev prod-pull prod-up prod-down e2e-alert-demo e2e-check

backend-run:
	cd backend && go run ./cmd/server

backend-test:
	cd backend && go test ./...

backend-build:
	cd backend && go build ./...

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
