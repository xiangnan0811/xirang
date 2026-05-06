# Comprehensive Project Review

## Goal

对 Xirang 进行一次全方位审查，找出多轮迭代后遗留的功能完整性、质量、安全、部署、文档和工程流程问题，并优先修复会影响后续开发、发布或用户信任的高价值问题。审查结果应转化为可验证的修复、明确的后续问题清单，以及可复用的检查方式。

## What I Already Know

* 用户希望这次不是单点 bug fix，而是一次全方位审查，覆盖功能不完善、潜在问题和后续开发风险。
* 当前分支已从干净且与 `origin/main` 同步的 `main` 创建：`audit/comprehensive-project-review`。
* 仓库是单仓库形态，主要分为 `backend/` Go 后端和 `web/` React/Vite 前端。
* 后端质量入口包含 `cd backend && go test ./...`、`go build ./...`、`golangci-lint run ./...`、`govulncheck ./...`。
* 前端质量入口包含 `cd web && npm run check`，其中包括 typecheck、lint、test、build；CI 还检查 bundle budget。
* 公开交付面包括 `README.md`、`docs/`、`.env.deploy`、`backend/.env.example`、`docker-compose.prod.yml`、`.github/workflows/*.yml`、PR 模板、安全/贡献/行为准则等。
* `.gitignore` 排除了 `web/node_modules/`、`web/dist/`、`web/coverage/`；这些本地目录存在不等同于仓库正在跟踪它们。
* 当前 CI 已覆盖 PR 标题、后端测试构建、前端测试构建、文档新鲜度、迁移 UTC 安全检查、Go 漏洞检查和 npm audit。

## Assumptions

* 本次先聚焦当前 `main` 的真实质量风险，而不是继续新增产品功能。
* 审查应同时覆盖代码、测试、UI/UX、API/数据流、部署/发布、文档真实性和仓库治理。
* 用户选择更完整的修复策略：本轮尽量修复所有发现的问题，不只处理 blocker/high-risk；只有过大、高风险、不明确或外部阻塞的问题才延期。
* 发现的问题需要用测试、构建、脚本或明确人工审查证据闭环，不只停留在主观判断。

## Review Scope

* Backend correctness: API handlers, auth/security, database migrations, background tasks, alerting, backup/recovery, monitoring/probe/status-page flows, error handling, logging, concurrency-sensitive code.
* Frontend correctness: route reachability, page state, forms/dialogs, API integration, empty/error/loading states, accessibility, reusable component consistency, type-safety, test coverage.
* Cross-layer contracts: API response shapes, enum/status values, validation rules, error semantics, time/zone handling, pagination/filtering, realtime/WebSocket behavior.
* Deployment and release: Docker Compose, image/tag/version-check contract, env vars, health checks, release workflows, deploy docs, rollback instructions.
* Documentation truth: README claims, feature docs, env-var reference, deployment guide, release maintainer docs, dated design docs when they still influence current public expectations.
* Repository hygiene: ignored/generated artifacts, scripts, CI parity with local commands, security-sensitive defaults, stale TODOs or dead paths, future-development guardrails.

## Requirements

* Produce a prioritized audit result with concrete findings grouped by severity and area.
* Fix discovered issues directly when the fix is reasonably scoped and confidence is high, including medium/low-severity issues.
* Add or update tests for behavior changes and regression-prone findings.
* Keep public docs aligned when fixes change behavior, deployment expectations, release contracts, or user-facing claims.
* Leave only findings that are too large, risky, ambiguous, or externally blocked in a durable task/report artifact instead of burying them in chat.
* Preserve branch workflow: no direct commits to `main`; changes go through PR-ready branch and quality checks.

## Acceptance Criteria

* [ ] Audit covers backend, frontend, cross-layer contracts, deployment/release, documentation truth, and repo hygiene.
* [ ] All findings discovered during this pass are either fixed or explicitly deferred with a clear reason.
* [ ] Fixes include targeted regression tests or documented verification when automated tests are not practical.
* [ ] Local verification commands run and results are recorded.
* [ ] Docs/config/process surfaces are updated where behavior or public claims changed.
* [ ] A final audit summary lists fixed issues, remaining follow-ups, verification commands, and residual risks.

## Definition Of Done

* Backend quality gate passes or any failure is understood and documented.
* Frontend quality gate passes or any failure is understood and documented.
* Repository-level docs and CI-related checks pass or any failure is understood and documented.
* Trellis context files are curated before implementation/check agents run.
* Spec-update judgment is performed before wrap-up.

## Out Of Scope

* Large new product features unrelated to problems found during the audit.
* Complete visual redesign or architecture rewrite unless a concrete blocker proves it necessary.
* Open-ended rewrites that are not tied to a concrete audit finding.
* Changing release/version contract without a specific defect.

## Decisions

* Scope preference: attempt to fix every discovered issue regardless of size. Defer only when a finding is too large, risky, ambiguous, or externally blocked for this pass.

## Technical Notes

* Task directory: `.trellis/tasks/05-06-comprehensive-project-review`.
* Branch: `audit/comprehensive-project-review`.
* Relevant Trellis spec indexes: `.trellis/spec/backend/index.md`, `.trellis/spec/frontend/index.md`, `.trellis/spec/guides/index.md`.
* Existing CI: `.github/workflows/ci.yml`.
* Key local commands from `Makefile`: `make backend-test`, `make web-test`, `make check`, `make build`.
