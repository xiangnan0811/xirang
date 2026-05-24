# P4 next residual security hardening

## Goal

Reduce the remaining P4 residual exposure in task read APIs with a local-only, behavior-compatible response-boundary hardening slice. The task list/detail endpoints currently serialize `model.Task` directly, including legacy `last_error` runtime evidence and the preloaded nested `Policy` object. Policy hooks may contain rendered AppCredential-derived command text, and task `last_error` can contain legacy command/runtime evidence from older rows.

## Requirements

- Sanitize `Task.LastError` when returning `/tasks` and `/tasks/:id`, using the existing runtime-evidence read sanitizer.
- Hide nested `policy.pre_hook` and `policy.post_hook` in `/tasks` and `/tasks/:id` responses so task APIs do not duplicate the rendered policy-hook surface.
- Preserve stored database rows; this is a response-boundary sanitizer, not a migration/backfill.
- Preserve task execution, policy create/update, policy list/detail, AppCredential CRUD, config import/export, deployment, and frontend behavior.
- Keep the implementation local to backend response shaping and regression tests unless code inspection proves broader need.

## Acceptance Criteria

- [ ] `/tasks` responses do not contain raw legacy `last_error` runtime evidence, command text/output, endpoints, hostnames, or paths.
- [ ] `/tasks/:id` responses do not contain raw legacy `last_error` runtime evidence, command text/output, endpoints, hostnames, or paths.
- [ ] `/tasks` responses do not expose raw nested `policy.pre_hook` or `policy.post_hook` command text.
- [ ] `/tasks/:id` responses do not expose raw nested `policy.pre_hook` or `policy.post_hook` command text.
- [ ] Sanitization does not mutate stored `tasks.last_error` or `policies.pre_hook` / `policies.post_hook` rows.
- [ ] Existing task list/detail filtering, pagination, node sanitization, policy association, and progress behavior remain intact.
- [ ] Backend tests pass for the touched package and full backend suite.

## Definition of Done

- Trellis research/context is recorded for implement/check phases.
- Backend implementation and regression tests are complete.
- `go test` / build verification passes using repo-local temp/cache if needed.
- Work is committed on the feature branch, task archived, journal recorded, PR created, CI green, merged, release automation handled, Docker publish verified if triggered, and local `main` synced.

## Technical Approach

Add task-response sanitization helpers in `backend/internal/api/handlers/task_handler.go`. The helpers should copy `model.Task` values before response serialization, sanitize `LastError` through `task.SanitizeRuntimeEvidenceForRead`, and copy the nested `Policy` pointer with `PreHook` and `PostHook` cleared. Apply the helper to both list and detail responses after existing node sanitization and progress hydration.

Regression tests should live in `backend/internal/api/handlers/task_handler_test.go` and assert both list and detail responses hide legacy runtime evidence and policy hook text without mutating DB rows.

## Decision (ADR-lite)

**Context**: Full policy-hook storage/API redesign would change the current product contract because policy endpoints intentionally return hook fields and docs/tests treat rendered hook visibility as expected. Task APIs are a smaller duplicate response surface: the frontend task mapper only needs nested policy id/name.

**Decision**: Harden the task read boundary only: sanitize task `last_error` and hide nested policy hook fields in task list/detail responses.

**Consequences**: This removes avoidable duplicate exposure from task APIs while leaving the larger policy hook contract unchanged for a future explicit redesign.

## Out of Scope

- Reworking policy hook rendering, persistence, or execution.
- Masking policy list/detail hook fields.
- Changing AppCredential CRUD behavior.
- Adding migrations or backfills.
- External Vault/KMS/SSH CA/session recording/command approval/WebAuthn/passkeys/device trust.
- Docker/nginx logging changes, file-browser content/path authorization changes, and snapshot indexer changes already covered by prior work.

## Technical Notes

- `backend/internal/api/handlers/task_handler.go` currently preloads `Node` and `Policy`, sanitizes nodes, and returns `model.Task` directly from `List` and `Get`.
- `backend/internal/model/models.go` defines `Task.LastError` as JSON-visible and `Task.Policy *Policy` as JSON-visible.
- `backend/internal/model/models.go` defines `Policy.PreHook` and `Policy.PostHook` as plaintext JSON-visible fields.
- `backend/internal/task/runtime_sanitize.go` exposes `SanitizeRuntimeEvidenceForRead` for API read-boundary sanitization.
- `backend/internal/api/handlers/task_run_handler.go` already applies the same read-boundary sanitizer pattern for task-run and log responses.
- `research/appcredential-hook-residual.md` identifies nested task policy hooks as the smallest behavior-compatible remaining AppCredential hook sub-surface.
