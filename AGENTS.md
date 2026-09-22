# Xirang development instructions

This file is the canonical project entrypoint for contributors and coding agents.
The project owns its contracts in [spec/](spec/index.md), verification commands,
and delivery requirements. A personal harness is optional: a clean checkout is
sufficient to find and follow these requirements.

## Work directly within the authorized scope

- Read only the relevant layer index and contracts before editing. Resolve paths
  relative to the checkout root (`git rev-parse --show-toplevel`), including when
  entering from `backend/`, `web/`, or a linked worktree. For a multi-repository
  assignment, identify each repository's own authority and verification directory.
- Clear, authorized requests proceed directly. Ask only about unresolved material
  requirements, risks, or authorization. Small changes do not require a task,
  PRD, design document, phase approval, routine journal, or migration receipt.
- Read-only requests stay read-only, including private recovery/index writes.
  Trellis and Superpowers lifecycle instructions are retired for this project;
  do not recreate their artifacts or load historical tasks as live instructions.
- Keep generic personal guidance in the personal harness and recovery/index data
  outside Git. Indexes contain pointers, not copied specifications or transcripts.
  Historical sessions and old memory are leads; current project contracts and
  current repository evidence govern. Completed/cancelled work is not active work.
- Authorized implementation may use native implementation agents with explicit
  file ownership. Read-only investigator/reviewer restrictions apply to those
  roles, not to separately authorized implementers or main-agent repairs.
- Preserve native model, reasoning and Advisor configuration. Use native role
  dispatch; never import another platform's runtime configuration. For parallel
  work, independent GPT + Grok review, repair verification, and candidate evidence,
  follow [collaboration and verification](spec/guides/agent-collaboration.md).
- Synchronize affected contracts and regression evidence when behavior changes.
  At completion name the spec and checks, or explain why no spec change applies.
  Keep current evidence while valid; the same HEAD alone does not prove unchanged
  staged, unstaged, untracked content or verification conditions.

## Repository Workflow

- Do not commit directly on `main`. Treat `main` as an integration branch that should track `origin/main`.
- Before any file-changing work, create or switch to a dedicated work branch from an up-to-date `main`. This applies to feature work, bug fixes, docs/config changes, specifications, and process changes.
- Allowed on `main`: read-only inspection, fetch/pull synchronization, branch creation, and post-merge sync. If `main` has local-only commits, stop and resolve the branch state before starting new work.
- Complete changes through a pull request with CI checks. After squash merge, sync local `main` to `origin/main` before starting the next branch.
- After creating a pull request, the responsible agent or maintainer must monitor all required CI jobs, fix failures on the same work branch, push the fix, and keep monitoring until the required checks pass or a real external blocker is recorded. Do not merge while required checks are failing, pending, or missing.
- After a PR merges, monitor post-merge automation before declaring the task complete: `Release Please`, any auto release, `Publish Docker Images`, and `Sync Docker Hub Description` when README/release docs are involved. If the merge does not trigger a formal release, explicitly record that no GitHub Release or Docker Hub publish was expected.
- Keep the release contract accurate in process docs and PRs: GitHub Release is the public version source of truth, Docker Hub is the only official public image source, and public releases use stable semver tags only.

## Project Overview

**xirang (息壤)** is a lightweight, agentless server operations management platform. It provides backup credibility verification, recovery drill automation, node diagnostics, monitoring/alerting, web terminal, and audit logging through SSH-based multi-server management — all in a single ops loop.

- **Backend**: Go 1.26, Gin, GORM (SQLite + PostgreSQL), zerolog, gorilla/websocket, robfig/cron/v3
- **Frontend**: React 18, TypeScript 5.8 (strict), Vite 7, Tailwind CSS 4, Radix UI (shadcn/ui), i18next (zh default)
- **Deploy**: Single all-in-one Docker image (frontend + backend + nginx), multi-arch (amd64 + arm64)
- **License**: MIT

## Repository Structure

```
xirang/
├── backend/              # Go backend (single binary, 40+ internal packages)
│   ├── cmd/server/       # entry point — main.go wires all packages
│   ├── internal/api/     # Gin router, Swagger docs, REST handlers
│   ├── internal/model/   # GORM models and model hooks
│   ├── internal/middleware/  # auth, RBAC, audit, metrics, rate limiting
│   ├── internal/database/   # DB open, GORM logger, paired migrations
│   ├── internal/task/    # scheduler, manager, runners, executors
│   ├── internal/alerting/   # alert dispatch, escalation, silence, retry
│   └── internal/...      # 30+ domain packages (node, policy, metrics, etc.)
├── web/                  # React frontend (Vite + TypeScript)
│   └── src/
│       ├── pages/        # route-level screens and page fragments
│       ├── components/   # reusable components + ui/ (shadcn primitives)
│       ├── context/      # React context providers (4-file pattern)
│       ├── hooks/        # custom hooks (use-*.ts)
│       ├── features/     # focused feature modules (e.g. nodes-detail)
│       ├── lib/          # API clients, utilities, themes, ws helpers
│       ├── types/        # shared domain types (domain.ts)
│       └── i18n/         # i18next setup and locale files
├── deploy/               # Docker, nginx, docker-compose
├── scripts/              # CI/ops helper scripts
├── docs/                 # user documentation
├── spec/                 # authoritative development contracts and regression requirements
└── Makefile              # all build/test/lint/deploy commands
```

## Where to Find Conventions

**Before writing code in any layer, read the relevant spec.** These specs are the authoritative source for coding conventions — this file is a navigation hub, not a replacement. Dependency versions come from `backend/go.mod` and `web/package.json`/lockfile.

| Layer | Spec Location | Key Topics |
|-------|---------------|------------|
| Backend | `spec/backend/` | directory structure, database/migrations, error handling, quality, logging, deployment runtime |
| Frontend | `spec/frontend/` | directory structure, components, hooks, state management, quality, type safety, a11y |
| Cross-cutting | `spec/guides/` | branch workflow, code reuse, cross-layer thinking, documentation truth |

Quick links: [Backend index](spec/backend/index.md) · [Frontend index](spec/frontend/index.md) · [Guides index](spec/guides/index.md)

Subdirectory guides: [backend/internal/api/handlers/](backend/internal/api/handlers/AGENTS.md) · [web/src/pages/](web/src/pages/AGENTS.md)

## Build and Test Commands

### Backend (from repo root)
- `make backend-run` — run the Go server
- `make backend-test` — `cd backend && go test ./...`
- `make backend-build` — build binary with version ldflags
- `make swag-init` — regenerate OpenAPI/Swagger docs

### Frontend (from `web/`)
- `npm run dev` — Vite dev server
- `npm run typecheck` — `tsc -b --noEmit`
- `npm run lint` / `npm run lint:fix` — ESLint
- `npm run test` — vitest with coverage
- `npm run build` — `tsc -b && vite build`
- **`npm run check`** — THE full gate: typecheck + lint + test + build

### Full project
- Run the following commands from the checkout root, not from `backend/` or `web/`.
- `make check` — lint (golangci-lint + eslint) + test (backend + frontend) + build
- `make lint` — golangci-lint + eslint only
- `make coverage` — coverage report
- `make docker-build` / `make docker-buildx` — Docker image (single or multi-arch)
- `make setup-hooks` — install git pre-commit/pre-push hooks
- `bash scripts/check-doc-freshness.sh` and `bash scripts/check-doc-freshness.test.sh` — documentation checks
- `bash scripts/local-ci-parity.sh` — existing pre-push gate; includes frontend full checks, bundle budget, backend vulnerability checks, and documentation/migration checks

Git hooks and required CI remain mandatory; do not bypass them. CI also owns
PostgreSQL parity, selected race tests, browser acceptance, coverage, and Docker
runtime checks. Local success does not replace those results or live acceptance.

## CI/CD

- **CI** (`.github/workflows/ci.yml`): PR title check, backend lint+test+coverage+build+govulncheck, frontend npm ci+audit+check+bundle-budget+coverage, docker-build, doc-freshness, migration-utc-safety
- **Release**: release-please (conventional commits, CHANGELOG.md, semver tags)
- **Docker**: `deploy/allinone/Dockerfile` — single all-in-one image published to `docker.io/linnea7171/xirang`
- **Post-merge**: monitor Release Please, Publish Docker Images, Sync Docker Hub Description

## Key Conventions (quick reference — see specs for full detail)

**Backend**:
- Use `response.go` helpers (`respondOK`, `respondCreated`, `respondBadRequest`, etc.) — never ad hoc `c.JSON`
- Every `/api/v1` route needs `AuthMiddleware` + `RBAC` + ownership checks
- Sensitive fields must go through `model.Sanitized()` — never return raw secrets
- Schema changes require paired SQLite + PostgreSQL migrations
- Dynamic config goes through `settings.Service` registry (DB > env > default)
- Logging via `logger.Module("name")` — structured, never `fmt.Printf` or `log.Printf`
- Sentinel errors in domain packages + `errors.Is` + `%w` wrapping

**Frontend**:
- Use typed API wrappers in `web/src/lib/api/` — never `fetch` directly in components
- Map `snake_case` → `camelCase` at API boundary via `map*` helpers
- Use `web/src/components/ui/` primitives (shadcn/ui) — never create ad hoc UI primitives
- `import type` for type-only imports; no `any` (use raw types + mappers)
- i18n via `setLanguage()` helper — never `i18n.changeLanguage()` directly
- `npm run check` is the full quality gate — must pass before PR

## Key Anti-Patterns (see specs for exhaustive list)

- **Backend**: ad hoc JSON responses, unsanitized sensitive fields, routes without auth/RBAC/ownership, SQLite-only or PostgreSQL-only migrations, settings outside `settings.Service`, raw `err.Error()` for 500s, treating missing auth as admin
- **Frontend**: direct `fetch` in components, ad hoc UI primitives, raw `snake_case` in components, `any` for API responses/props, `unknown as T` casts, bypassing the central request wrapper, negative/viewport-scaled text hacks
