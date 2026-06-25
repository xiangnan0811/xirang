<!-- TRELLIS:START -->
# Trellis Instructions

These instructions are for AI assistants working in this project.

This project is managed by Trellis. The working knowledge you need lives under `.trellis/`:

- `.trellis/workflow.md` — development phases, when to create tasks, skill routing
- `.trellis/spec/` — package- and layer-scoped coding guidelines (read before writing code in a given layer)
- `.trellis/workspace/` — per-developer journals and session traces
- `.trellis/tasks/` — active and archived tasks (PRDs, research, jsonl context)

If a Trellis command is available on your platform (e.g. `/trellis:finish-work`, `/trellis:continue`), prefer it over manual steps. Not every platform exposes every command.

If you're using Codex or another agent-capable tool, additional project-scoped helpers may live in:
- `.agents/skills/` — reusable Trellis skills
- `.codex/agents/` — optional custom subagents

Managed by Trellis. Edits outside this block are preserved; edits inside may be overwritten by a future `trellis update`.

<!-- TRELLIS:END -->

## OpenCode / Oh My OpenAgent Notes

- Treat `.trellis/` as the shared project memory for OpenCode, Claude Code, Codex, and other agent tools. Start OpenCode sessions by loading `trellis-start` so the agent reads the same workflow, task, spec, and workspace journal context used by Claude Code and Codex.
- Do not commit local OpenCode / Oh My OpenAgent runtime directories. `.omo/`, `.opencode/`, and `.codegraph/` are generated per machine or per session and are ignored by this repository.

## Repository Workflow

- Do not commit directly on `main`. Treat `main` as an integration branch that should track `origin/main`.
- Before any file-changing work, create or switch to a dedicated work branch from an up-to-date `main`. This applies to feature work, bug fixes, docs/config changes, Trellis task/spec updates, and process changes.
- Allowed on `main`: read-only inspection, fetch/pull synchronization, branch creation, and post-merge sync. If `main` has local-only commits, stop and resolve the branch state before starting new work.
- Complete changes through a pull request with CI checks. After squash merge, sync local `main` to `origin/main` before starting the next branch.
- After creating a pull request, the responsible agent or maintainer must monitor all required CI jobs, fix failures on the same work branch, push the fix, and keep monitoring until the required checks pass or a real external blocker is recorded. Do not merge while required checks are failing, pending, or missing.
- After a PR merges, monitor post-merge automation before declaring the task complete: `Release Please`, any auto release, `Publish Docker Images`, and `Sync Docker Hub Description` when README/release docs are involved. If the merge does not trigger a formal release, explicitly record that no GitHub Release or Docker Hub publish was expected.
- Keep the release contract accurate in process docs and PRs: GitHub Release is the public version source of truth, Docker Hub is the only official public image source, and public releases use stable semver tags only.

## Project Overview

**xirang (息壤)** is a lightweight, agentless server operations management platform. It provides backup credibility verification, recovery drill automation, node diagnostics, monitoring/alerting, web terminal, and audit logging through SSH-based multi-server management — all in a single ops loop.

- **Backend**: Go 1.26, Gin, GORM (SQLite + PostgreSQL), zerolog, gorilla/websocket, robfig/cron/v3
- **Frontend**: React 18, TypeScript 5.8 (strict), Vite 7, Tailwind CSS 3.4, Radix UI (shadcn/ui), i18next (zh default)
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
├── .trellis/             # Trellis workflow + coding specs (READ BEFORE CODING)
└── Makefile              # all build/test/lint/deploy commands
```

## Where to Find Conventions

**Before writing code in any layer, read the relevant Trellis spec.** These specs are the authoritative source for coding conventions — this file is a navigation hub, not a replacement.

| Layer | Spec Location | Key Topics |
|-------|---------------|------------|
| Backend | `.trellis/spec/backend/` | directory structure, database/migrations, error handling, quality, logging, deployment runtime |
| Frontend | `.trellis/spec/frontend/` | directory structure, components, hooks, state management, quality, type safety, a11y |
| Cross-cutting | `.trellis/spec/guides/` | branch workflow, code reuse, cross-layer thinking, documentation truth |

Quick links: [Backend index](.trellis/spec/backend/index.md) · [Frontend index](.trellis/spec/frontend/index.md) · [Guides index](.trellis/spec/guides/index.md)

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
- `make check` — lint (golangci-lint + eslint) + test (backend + frontend) + build
- `make lint` — golangci-lint + eslint only
- `make coverage` — coverage report
- `make docker-build` / `make docker-buildx` — Docker image (single or multi-arch)
- `make setup-hooks` — install git pre-commit/pre-push hooks

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
