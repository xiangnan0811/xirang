# Routed spec contracts

This task spans settings, backupasset Foundation/runtime and API handlers. Read the source specs before coding;
this file routes only the clauses that govern the P0.

## `.trellis/spec/backend/quality-guidelines.md`

- Dynamic settings stay in `settings.Service`; do not bypass the registry or duplicate environment resolution.
- Cross-resource/multi-row writes use transactions; every DB/context/lifecycle error is handled.
- Backend behavior changes require package tests, broad backend tests/build and lint when available.
- Timing/cancellation tests use controlled channels when exact ordering matters and run repeated/race gates.
- Backup Asset GA enablement keeps CodeDefault false, authorizes readiness before managed admission, drains on
  disable, and injects `Runtime.FeatureLive` into product planes.
- Background workers and goroutines must be cancelable/shutdown-aware.

## `.trellis/spec/backend/error-handling.md`

- Handler responses use the standard response helpers/envelope; services return typed/wrapped errors.
- Preserve `errors.Is` for context cancellation/deadline and stable domain sentinels.
- GA blocked or ack-required settings PUT/DELETE/import returns 409 `就绪检查未完成` with no persistence.
- Unexpected transition failure returns a generic 500; never return raw DB/config/runtime errors.
- Successful enable stamps `enablement_succeeded_at`; this P0 strengthens that to full-transition success with
  exact failure restoration, without weakening the existing gate.

## `.trellis/spec/backend/logging-guidelines.md`

- Use `logger.Module` and structured, low-cardinality stage/outcome fields.
- Never log decrypted settings, exported config, root paths, credentials, locators, Provider output or secrets.
- Client cancellation must reach owned work and cleanup must have a finite deadline; unbounded detached contexts
  and return-before-join are forbidden.
- Canceled requests are not automatically business failures and should not create noisy raw error logs.

## `.trellis/spec/guides/cross-layer-thinking-guide.md`

- Map API → settings coordinator → prospective parser → runtime managers → persistence/readiness as one flow.
- Define exact boundary formats and validate once at the owning entry.
- PUT, DELETE-restore, config import and startup must share the enablement predicate and error contract.
- Test round trips and every boundary error, not only a package-local spy.

## `.trellis/spec/guides/branch-workflow-guidelines.md`

- All task/spec/code changes stay on a dedicated branch and reach main by PR.
- Run relevant local checks, monitor required PR CI, then monitor post-merge release/image automation.
- Use an ignored task worktree for implementation; do not continue from an old topic baseline after squash merge.

## Task-specific executable contract to add after GREEN

The implementation must add a seven-section backend spec scenario covering:

1. scope/trigger: Foundation mutations and runtime transitions;
2. signatures: context-aware gate, prospective `FromValues` parsers and config-aware Content/Search methods;
3. contracts: no mutation-inner settings read, all-or-nothing visibility, bounded join, exact stamp rollback;
4. validation/error matrix: success, blocked, canceled waiter, each runtime/persist failure and rollback failure;
5. good/base/bad cases;
6. required genuine integration, repetition, race, privacy and handler tests;
7. wrong versus correct code showing nested current getter versus explicit prospective config.
