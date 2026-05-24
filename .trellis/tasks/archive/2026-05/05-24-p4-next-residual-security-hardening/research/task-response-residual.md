# Research: Task response residual

- **Query**: Confirm whether task list/detail responses still expose legacy runtime evidence or duplicate policy hook command text, and identify the smallest local-only hardening slice.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/handlers/task_handler.go` | Task list/detail handlers preload `Node` and `Policy`, sanitize nodes, then return `model.Task` directly. |
| `backend/internal/model/models.go` | `Task.LastError` is JSON-visible; `Task.Policy *Policy` is JSON-visible; `Policy.PreHook` and `Policy.PostHook` are JSON-visible plaintext fields. |
| `backend/internal/task/runtime_sanitize.go` | Existing read-boundary sanitizer exposed as `SanitizeRuntimeEvidenceForRead`. |
| `backend/internal/api/handlers/task_run_handler.go` | Existing pattern for response-copy sanitization of task-run/log legacy runtime evidence. |
| `.trellis/tasks/05-24-p4-next-residual-security-hardening/research/appcredential-hook-residual.md` | Identifies nested task policy hook output as the smallest compatible AppCredential hook sub-surface. |

### Code Patterns

`TaskHandler.List` queries `model.Task` with `Preload("Node").Preload("Policy")`, sanitizes only `Node`, hydrates progress, then returns the task slice through `respondPaginated`. `TaskHandler.Get` follows the same pattern and returns a single `model.Task` through `respondOK`.

`model.Task.LastError` is serialized as `json:"last_error"`, so legacy rows can be returned as-is from task list/detail even though newer runtime write paths sanitize many task-run errors. The task-run handler already demonstrates the desired response-boundary pattern by copying values and applying `task.SanitizeRuntimeEvidenceForRead`.

The nested `Policy` object can carry `pre_hook` and `post_hook`. Those fields may contain rendered AppCredential-derived hook command strings. Policy endpoints still return hook fields by current product contract, but task endpoints do not need to duplicate them.

### Smallest Local-Only, Behavior-Compatible Slice

Harden only the task list/detail response boundary:

1. Copy each `model.Task` before serialization.
2. Sanitize `LastError` with `task.SanitizeRuntimeEvidenceForRead`.
3. Copy the nested `Policy` pointer and clear `PreHook` / `PostHook` before returning it.
4. Do not mutate DB rows and do not alter policy endpoints or hook execution.

This shares the same response-copy pattern already used by task-run read-boundary sanitization and avoids a larger policy hook redesign.

## Related Specs

- `.trellis/spec/backend/error-handling.md` — API responses must not expose command output/text, endpoints, hostnames, raw paths, or diagnostic evidence.
- `.trellis/spec/backend/database-guidelines.md` — Response sanitizers are required before returning raw model values containing sensitive evidence.
