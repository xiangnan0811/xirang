# 全面瘦身重构

## Goal

Reduce Xirang's runtime load, resource use, and maintenance burden through a
sequence of independently verifiable slimming refactors, while preserving the
current production behavior of the single-user deployment.

## Requirements

- Preserve normal production operation throughout the refactor. Each child task
  must be small enough to validate locally and roll back independently.
- Clean up legacy compatibility code only when repository evidence and, where
  needed, production-state evidence prove that the compatibility path is no
  longer required.
- Remove dead or redundant logic only after confirming no live backend route,
  frontend component, task runner, scheduler, or documentation contract still
  depends on it.
- Merge duplicated business logic and helper code when the same behavior appears
  in at least three places or when keeping it duplicated creates observable
  inconsistency risk.
- Prioritize performance work that reduces database payloads, repeated queries,
  background work, or frontend request churn without changing API contracts.
- Improve directory structure only when the move clarifies an existing boundary,
  reduces file size/ownership confusion, or follows an established local split
  pattern.
- Keep backend and frontend API contracts stable unless a child task explicitly
  owns a coordinated contract change.
- Use Trellis child tasks for every implementation slice. Do not treat this
  parent as the implementation target except for final cross-child review.

## Acceptance Criteria

- [ ] Every child task has its own PRD, design if complex, implementation plan,
      validation commands, and rollback notes before implementation starts.
- [ ] The first child optimizes policy `node_ids` response loading without
      changing policy list/detail response shape.
- [ ] A later child removes deprecated hook templates only after replacing the
      frontend policy editor dependency on `GET /hook-templates`.
- [ ] V1 encryption compatibility is removed only after evidence proves all
      protected fields no longer contain `enc:v1:` values.
- [ ] Frontend duplicated numeric parsing helpers are consolidated behind a
      shared utility with mapper/dialog tests updated as needed.
- [ ] Backend ad hoc `c.JSON` responses and legacy `log.Printf` call sites are
      reduced or converted in focused slices, preserving documented startup
      exceptions.
- [ ] Alerting package-global shims are removed or narrowed only after live
      callers are converted to dependency injection.
- [ ] Node detail feature token access is moved away from direct browser storage
      reads toward the auth context or explicit props.
- [ ] Final review confirms no child task introduced broad behavior drift,
      missing tests, stale docs, or unreconciled compatibility assumptions.

## Notes

- Initial read-only audit found these first candidates:
  - `backend/internal/api/handlers/policy_handler.go` preloads full `Nodes`
    while policy responses only need `node_ids`.
  - `GET /hook-templates` is deprecated but still used by
    `web/src/components/policy-editor-dialog.tsx`.
  - V1 encryption fallback is still tied to startup migration and
    `/system/encryption-status`; deletion needs production-state evidence.
  - Frontend numeric fallback helpers are duplicated across API mappers and
    dialogs.
  - Several handler and middleware paths still use direct `c.JSON`; backend
    specs require response helpers for handler responses.
  - Alerting package-level shims still have live callers in integration tests,
    alert retry, and reporting.
