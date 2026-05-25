# Research: export/import residual P4 hardening

- **Query**: Research remaining P4 residual security surfaces including export/import, AppCredential rendered hooks, rclone/restic executor/config residuals, and adjacent local evidence surfaces. Choose exactly one minimal, behavior-compatible, local-only implementation slice if code inspection supports it.
- **Scope**: internal
- **Date**: 2026-05-25

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/spec/backend/logging-guidelines.md` | Backend logging rules: do not log secrets, decrypted values, command output, exported config payloads, file contents, Docker output, diagnostic evidence, executor config, or raw remote evidence. |
| `.trellis/spec/backend/error-handling.md` | API error boundary rules: internal errors should be generic to clients; raw SQL/encryption/SSH/token/command/file/Docker/diagnostic/export evidence must not be exposed. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend quality/security rules for sanitizing response structs and evidence, plus tests for security-sensitive changes. |
| `.trellis/spec/frontend/state-management.md` | Frontend grant prompt state rules: do not store grant IDs/material/reasons/status in browser storage. |
| `.trellis/spec/frontend/type-safety.md` | Frontend API-boundary mapping and rule to render backend-sanitized evidence as-is. |
| `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` | P3/P4 roadmap: config import/export grants belong to near-term control-plane hardening; external Vault/KMS/SSH CA/session recording/command approval/WebAuthn/device trust excluded. |
| `.trellis/tasks/archive/2026-05/05-21-p3-config-import-grant-batch-telemetry/prd.md` | Prior config-import JIT grant slice and frontend storage-safety requirements. |
| `.trellis/tasks/archive/2026-05/05-22-p3-grant-semantics/prd.md` | Row-backed credential grant semantics for exact user/role/action/purpose/resource matching and safe audit metadata. |
| `.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md` | Prior executor SSH local-provider adoption slice; excludes external providers and session/command controls. |
| `.trellis/tasks/archive/2026-05/05-23-p4-restic-credential-resolver/prd.md` | Prior restic repository password resolver seam, including anomaly snapshot diff as a consumer; explicitly defers replacing remote `RESTIC_PASSWORD` execution behavior. |
| `.trellis/tasks/archive/2026-05/05-23-p4-next-hardening/prd.md` | Prior AppCredential profile hook local resolver seam; preserves rendered policy hook storage/response behavior. |
| `.trellis/tasks/archive/2026-05/05-24-p4-residual-security-review/prd.md` | Prior residual review selected task-run/detail/log HTTP read-boundary sanitization and deferred broader AppCredential hook redesign. |
| `.trellis/tasks/archive/2026-05/05-24-p4-next-residual-security-hardening/prd.md` | Prior task list/detail response-boundary sanitizer for `Task.LastError` and nested policy hook duplicate exposure. |
| `.trellis/tasks/archive/2026-05/05-24-p4-appcredential-hook-hardening/prd.md` | Prior WebSocket task-log backfill sanitizer; leaves policy hook storage/response contract unchanged. |
| `.trellis/tasks/archive/2026-05/05-24-p4-snapshot-indexer-output-sanitization/prd.md` | Prior snapshot indexer slice hiding failed `restic find` output. |
| `.trellis/tasks/archive/2026-05/05-24-p4-file-process-residual-hardening/prd.md` | Prior file preview state cleanup and file-handler process-log safe-field hardening. |
| `.trellis/tasks/archive/2026-05/05-24-p4-docker-nginx-residual-hardening/prd.md` | Prior Nginx access-log query minimization; Docker volume product data left unchanged. |
| `.trellis/tasks/archive/2026-05/05-06-anomaly-snapshot-diff-detection/prd.md` | Original anomaly snapshot diff feature; confirms `backend/internal/anomaly/snapshot_diff.go` owns post-backup restic snapshots/diff analysis. |
| `backend/internal/api/router.go` | Config import/export routes: sensitive export and import are admin-only, step-up-gated, and grant-gated before handlers. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import implementation and audit metadata. |
| `backend/internal/api/handlers/config_handler_test.go` | Export/import behavior tests including default secret omission, sensitive export payload behavior, safe audit metadata, and round-trip import. |
| `backend/internal/api/handlers/credential_access_grant.go` | Config import/export grant constants, request endpoints, active grant matching, reason sanitization, safe DTO/audit helpers. |
| `backend/internal/api/handlers/credential_access_grant_test.go` | Grant enforcement tests proving config import/export step-up, active-grant checks, denial-before-mutation, inactive/wrong-tuple rejection, and safe audit metadata. |
| `web/src/components/config-export-import.tsx` | Frontend config export/import UI and grant prompt flow. |
| `web/src/components/config-export-import.test.tsx` | Frontend tests for import/sensitive export grant flows, no browser-storage persistence, and invalid-file gating. |
| `web/src/lib/api/config-api.ts` | Typed config export/import API wrappers using optional step-up proof. |
| `backend/internal/model/models.go` | GORM model sensitivity boundaries: `ExecutorConfig` and `AppCredential.Config` are JSON-hidden/encrypted; policy hooks remain JSON-visible by current contract. |
| `backend/internal/profile/app_profile_access.go` | AppCredential local resolver seam with unexported config and safe metadata. |
| `backend/internal/profile/profile.go` | AppCredential profile templates and `RenderHooks`; templates render credential values into hook command strings intentionally. |
| `backend/internal/api/handlers/policy_handler.go` | Policy create/update profile rendering and `buildPolicyResponse` including `pre_hook`/`post_hook`. |
| `backend/internal/api/handlers/app_credential_handler.go` | AppCredential create/update/list/get behavior, sanitized responses, password-preservation, and cascade hook re-rendering. |
| `backend/internal/task/executor/rclone_executor.go` | rclone executor config and runtime-output sanitizer usage. |
| `backend/internal/task/executor/restic_repository_access.go` | Restic repository password resolver seam and safe metadata. |
| `backend/internal/task/executor/restic_executor.go` | Restic backup/restore/snapshot/file paths, output sanitizer usage, and authorized snapshot/file response structs. |
| `backend/internal/task/runtime_sanitize.go` | Task runtime read/write sanitizer wrapper and output placeholder helper. |
| `backend/internal/runtimeevidence/sanitize.go` | Shared sanitizer for URLs, command text, output markers, paths, hostnames/IPs, and host-sensitive fragments. |
| `backend/internal/task/log_writer.go` | Task log write boundary applies `sanitizeTaskLogMessage` before DB write/WebSocket publish. |
| `backend/internal/ws/hub.go` | WebSocket log backfill applies `runtimeevidence.SanitizeTaskRuntimeEvidence` before emitting legacy log messages. |
| `backend/internal/api/handlers/task_run_handler.go` | HTTP task-run/log response-boundary sanitizers for legacy `LastError`, `TaskLog.Message`, and drill evidence errors. |
| `backend/internal/api/handlers/task_handler.go` | Task list/detail/log read-boundary sanitizer for legacy task evidence and nested policy hook duplicate surface. |
| `backend/internal/snapshot/indexer.go` | Snapshot indexer failure path already hides non-empty `restic find` output. |
| `backend/internal/anomaly/snapshot_diff.go` | Anomaly snapshot diff implementation; contains the selected residual raw-output process-log surface. |
| `backend/internal/anomaly/snapshot_diff_test.go` | Existing anomaly snapshot diff tests; likely home for focused regression coverage. |
| `backend/internal/task/integrity_checker.go` | Restic/rclone integrity check process logs use sanitized `error` and placeholder `output` fields. |
| `backend/internal/task/retention.go` | Restic/rclone retention process logs use sanitized `error` and placeholder `output` fields. |

### Code Patterns

#### 1. Config export/import safeguards and residual risk

`backend/internal/api/router.go:328-333` wires current config routes so sensitive export and import pass through auth, admin role, step-up, and grant middleware before handler execution:

```go
secured.GET("/config/export", middleware.RequireRole("admin"), handlers.RequireStepUpIf(dep.DB, dep.JWTManager, handlers.CredentialGrantActionConfigExport, handlers.CredentialGrantPurposeConfigExport, "settings_export_sensitive", func(c *gin.Context) bool {
	return c.Query("include_secrets") == "true"
}), handlers.RequireConfigExportCredentialGrantIf(dep.DB, func(c *gin.Context) bool {
	return c.Query("include_secrets") == "true"
}), configHandler.Export)
secured.POST("/config/import", middleware.RequireRole("admin"), handlers.RequireStepUp(dep.DB, dep.JWTManager, handlers.CredentialGrantActionConfigImport, handlers.CredentialGrantPurposeConfigImport, "settings_import"), handlers.RequireConfigImportCredentialGrant(dep.DB), configHandler.Import)
```

`backend/internal/api/handlers/config_handler.go:113-255` builds default export payloads without node password/private key, SSH private key, task executor config, or sensitive-looking system settings. Sensitive export intentionally adds those values only when `include_secrets=true` after the route-level gates. Export audit metadata is count/stage based at `backend/internal/api/handlers/config_handler.go:228-243`.

`backend/internal/api/handlers/config_handler.go:271-701` imports after route-level grant enforcement. The handler itself applies a 10 MiB body cap at `backend/internal/api/handlers/config_handler.go:278`, parses wrapped/direct JSON at `backend/internal/api/handlers/config_handler.go:291`, mutates inside a transaction at `backend/internal/api/handlers/config_handler.go:299`, and writes success audit metadata with counts only at `backend/internal/api/handlers/config_handler.go:677-690`.

`backend/internal/api/handlers/credential_access_grant.go:216-258` creates system-scoped config import/export grants. `backend/internal/api/handlers/credential_access_grant.go:648-654` enforces matching grants. `backend/internal/api/handlers/credential_access_grant.go:898-910` bounds and rejects sensitive grant reasons. `backend/internal/api/handlers/credential_access_grant.go:1019-1088` writes grant request/use/blocked audits with safe scalar fields only.

`backend/internal/api/handlers/credential_access_grant_test.go:1598-1671` proves config import requires step-up plus grant before mutation and that valid grant use/import audit is safe. `backend/internal/api/handlers/credential_access_grant_test.go:1673-1887` covers sensitive export grant behavior and wrong/inactive grant tuples. `backend/internal/api/handlers/config_handler_test.go:143-347` proves default export omits secrets, sensitive export returns requested secret material to authorized admin, and audit metadata does not copy payload values.

Residual assessment: no minimal behavior-compatible config import/export code slice is supported by inspection. Non-sensitive export still returns host/path/task command-like product configuration, and sensitive export intentionally returns secret material to an authorized admin after step-up/grant. Redacting those payloads would change export/import semantics. Import is already grant-gated before body read/mutation in the real route. Frontend state is transient and tested for storage safety.

#### 2. Frontend config export/import safeguards

`web/src/components/config-export-import.tsx:18-30` returns no UI unless the user is admin. `web/src/components/config-export-import.tsx:55-60` clears reason/error/dialog state and resets the file input. `web/src/components/config-export-import.tsx:119-139` validates/parses the selected file only during grant submission, obtains a non-persistent step-up proof, requests a config-import grant, parses again, and submits import. `web/src/components/config-export-import.tsx:141-159` does the same for sensitive export and downloads the returned payload.

`web/src/components/config-export-import.test.tsx:80-143` asserts import/sensitive export request step-up and grants, then verifies `localStorage` and `sessionStorage` do not contain reason text, grant statuses/IDs, or config payload markers. `web/src/components/config-export-import.test.tsx:203-246` asserts invalid files do not request step-up/grant and import payloads are not parsed/retained until grant submission.

Residual assessment: no current browser-storage leak was found. Import/export payloads can exist in component memory, browser download blobs, and file input state as part of authorized behavior. A memory-clearing refinement would not be the highest confirmed residual and may be frontend-only process hygiene rather than a proven leak.

#### 3. AppCredential rendered hook safeguards and residual risk

`backend/internal/model/models.go:152-196` stores `AppCredential.Config` encrypted and JSON-hidden; `SanitizedConfig()` removes `password` before API responses. `backend/internal/profile/app_profile_access.go:20-89` wraps decrypted AppCredential config in a local resolver seam with unexported config and safe metadata.

`backend/internal/profile/profile.go:47-128` defines built-in hook templates; several templates intentionally interpolate `.password`, `.host`, `.container_name`, and temporary paths into generated command text. `backend/internal/profile/profile.go:145-160` renders those hooks. `backend/internal/api/handlers/policy_handler.go:247-282` resolves AppCredential config and renders missing policy hooks during policy create; update follows the same current product behavior. `backend/internal/api/handlers/policy_handler.go:1082-1133` still returns `pre_hook` and `post_hook` in policy list/detail responses.

Prior slices reduced duplicate surfaces: `.trellis/tasks/archive/2026-05/05-24-p4-next-residual-security-hardening/prd.md` sanitizes/hides nested policy hooks in task list/detail responses; `.trellis/tasks/archive/2026-05/05-24-p4-appcredential-hook-hardening/prd.md` sanitizes WebSocket log backfill. The policy API hook contract remains intentionally visible.

Residual assessment: rendered policy hooks remain a real residual design issue because generated command text can contain AppCredential-derived material. However, policy list/detail visibility and hook persistence are current product behavior and explicitly preserved by prior P4 decisions. Changing storage, response fields, generated command semantics, or hook execution would be broader and behavior-changing, not a minimal local-only compatible slice.

#### 4. rclone/restic executor and config safeguards

`backend/internal/model/models.go:296-348` hides `Task.ExecutorConfig` from JSON and encrypts/decrypts it through model hooks. `backend/internal/task/executor/rclone_executor.go:20-24` shows rclone executor config contains only `bandwidth_limit` and `transfers`, not a secret-bearing field. `backend/internal/task/executor/rclone_executor.go:72-82` and `backend/internal/task/executor/rclone_executor.go:107-119` run rclone sync/restore; streamed runtime lines are sanitized before task-log write at `backend/internal/task/executor/rclone_executor.go:149-160`.

`backend/internal/task/executor/restic_repository_access.go:16-65` centralizes restic repository password access in an unexported `password` field with safe metadata. `backend/internal/task/executor/restic_executor.go:49-52` and `backend/internal/task/executor/restic_executor.go:134-137` use that resolver for backup/restore. `backend/internal/task/executor/restic_executor.go:299-304`, `backend/internal/task/executor/restic_executor.go:332-336`, and `backend/internal/task/executor/restic_executor.go:375-379` hide command output via `sanitizeExecutorRuntimeOutput` on failures. `backend/internal/task/executor/restic_executor.go:264-280` returns snapshot IDs, hostnames, paths, and file metadata as authorized feature data.

`backend/internal/task/runtime_sanitize.go:29-34` uses a stable `[输出已隐藏]` placeholder for raw output, and `backend/internal/runtimeevidence/sanitize.go:24-49` redacts URLs, command lifecycle text, output markers, paths, IPs/hostnames, and host-sensitive fragments. `backend/internal/task/log_writer.go:60-67` sanitizes all task log writes; HTTP and WebSocket legacy reads are sanitized again in `backend/internal/api/handlers/task_run_handler.go:24-60`, `backend/internal/api/handlers/task_handler.go:70-74`, and `backend/internal/ws/hub.go:322-333`.

Residual assessment: no rclone-specific secret/config leak was confirmed. Restic repository password materialization is already centralized, and replacing remote `RESTIC_PASSWORD=...` command/environment behavior is explicitly deferred by the prior resolver PRD. Authorized snapshot/file path/hostname fields are product data in existing APIs. Successful restic diff parsing stores aggregate counts/history and emits findings with IDs/counts, not raw paths.

#### 5. Adjacent local evidence surface selected for implementation

`backend/internal/anomaly/snapshot_diff.go:312-327` runs `restic snapshots --json` and quietly skips when JSON parsing fails, but logs the raw combined stdout/stderr output as a process debug field:

```go
snapOutput, err := executor.RunSSHCommandOutput(ctx, client, snapCmd)
if err != nil {
	return nil, fmt.Errorf("获取快照列表失败: %w", err)
}

snapIDs, err := parseResticSnapshotsJSON(snapOutput)
if err != nil {
	// JSON 解析失败说明仓库可能为空或输出非预期 — 静默跳过
	logger.Module("anomaly").Debug().
		Uint("task_id", task.ID).
		Err(err).
		Str("output", snapOutput).
		Msg("解析 restic snapshots JSON 失败")
	return nil, nil
}
```

Why this is a confirmed residual:

- The command is constructed with `2>&1` at `backend/internal/anomaly/snapshot_diff.go:313`, so `snapOutput` may include arbitrary restic stderr/stdout.
- On parse failure, the function intentionally returns `nil, nil`, so this is not an API/feature output; the only observable evidence sink is the local process log.
- Raw restic snapshots output can include hostnames, path-like repository/diagnostic details, endpoints, tokens from environmental failures, or other host-sensitive strings.
- Adjacent task integrity/retention process logs already use safe `error` and placeholder `output` fields at `backend/internal/task/integrity_checker.go:77-83`, `backend/internal/task/integrity_checker.go:113-118`, `backend/internal/task/retention.go:151-156`, and `backend/internal/task/retention.go:184-189`.
- The prior snapshot indexer slice already hid `restic find` failure output at `backend/internal/snapshot/indexer.go:226-231`, so anomaly snapshot diff is an analogous remaining local output surface.

Recommended smallest safe slice:

- Remove the raw `Str("output", snapOutput)` from the parse-failure debug log, or replace it with a safe boolean/length/placeholder field such as `Bool("output_present", strings.TrimSpace(snapOutput) != "")` and/or `Str("stage", "snapshots_json_parse")`.
- Keep `task_id`, `stage`, and `Err(err)` if `err` is only the local JSON parser error generated by `parseResticSnapshotsJSON`; do not add command text, output, repo path, host, snapshot path, or executor config.
- Add focused regression coverage in `backend/internal/anomaly/snapshot_diff_test.go` around a package-local helper or the final logging helper if introduced, proving raw fake token/host/path/restic output does not enter structured log fields/messages.

Behavior compatibility:

- No API response, UI, route, auth/RBAC/ownership, credential grant, schema, deployment, command execution, anomaly finding, alert, snapshot diff history, or successful parser behavior changes.
- The existing parse-failure operational behavior remains: debug log then `return nil, nil`.
- Only a local process-log field is reduced from raw remote output to safe structured metadata.

### External References

None. This research was internal code/spec/Trellis inspection only.

### Related Specs

- `.trellis/spec/backend/logging-guidelines.md` — prohibits logging command output, exported config payloads, file contents, Docker output, diagnostic evidence, executor config, and raw remote evidence.
- `.trellis/spec/backend/error-handling.md` — prohibits exposing raw command output, file/Docker/diagnostic/export evidence, raw SQL, encryption details, and stack-like details to clients.
- `.trellis/spec/backend/quality-guidelines.md` — requires shared sanitizer coverage for user-visible evidence and tests for security-sensitive changes.
- `.trellis/spec/frontend/state-management.md` — prohibits persisting grant IDs/material/reasons/status in browser storage.
- `.trellis/spec/frontend/type-safety.md` — requires typed API wrappers and safe frontend mapping/rendering of backend-provided evidence.

### Prior Trellis Context

- `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` — P3/P4 boundary and exclusions.
- `.trellis/tasks/archive/2026-05/05-21-p3-config-import-grant-batch-telemetry/prd.md` — config import grant and frontend storage-safety baseline.
- `.trellis/tasks/archive/2026-05/05-22-p3-grant-semantics/prd.md` — grant exact-match and safe metadata semantics.
- `.trellis/tasks/archive/2026-05/05-23-p4-restic-credential-resolver/prd.md` — restic repository password resolver coverage and deferred remote env exposure.
- `.trellis/tasks/archive/2026-05/05-23-p4-next-hardening/prd.md` — AppCredential profile resolver seam and explicit preservation of rendered hook persistence/response.
- `.trellis/tasks/archive/2026-05/05-24-p4-residual-security-review/prd.md` — prior residual review and task-run/log read-boundary sanitizer selection.
- `.trellis/tasks/archive/2026-05/05-24-p4-next-residual-security-hardening/prd.md` — task list/detail `LastError` and nested policy-hook duplicate surface hardening.
- `.trellis/tasks/archive/2026-05/05-24-p4-appcredential-hook-hardening/prd.md` — WebSocket task-log backfill read-boundary sanitizer.
- `.trellis/tasks/archive/2026-05/05-24-p4-snapshot-indexer-output-sanitization/prd.md` — snapshot indexer raw-output hiding precedent.
- `.trellis/tasks/archive/2026-05/05-24-p4-file-process-residual-hardening/prd.md` — file preview state and process-log safe-field precedent.
- `.trellis/tasks/archive/2026-05/05-24-p4-docker-nginx-residual-hardening/prd.md` — Nginx access-log query minimization precedent.
- `.trellis/tasks/archive/2026-05/05-06-anomaly-snapshot-diff-detection/prd.md` — original anomaly snapshot diff design and implementation anchors.

## Caveats / Not Found

- No minimal behavior-compatible config export/import payload hardening was found. Sensitive export intentionally returns secret material after step-up/grant; normal export intentionally returns product configuration such as host/path/task fields.
- No frontend config export/import browser-storage leak was found; tests cover grant/payload/reason non-persistence in `localStorage` and `sessionStorage`.
- AppCredential rendered policy hooks remain a residual design risk, but policy hook fields are intentionally stored and returned by policy APIs today. A proper fix would require a broader product/API/storage/runtime design decision.
- No rclone secret-bearing executor config field was found. rclone source/destination are execution/product data.
- Restic repository password resolver coverage is already present; replacing remote `RESTIC_PASSWORD` command/env behavior remains out of scope.
- Snapshot search/diff/list file paths and restic snapshot metadata are authorized feature outputs and were not treated as leaks for this slice.
- The selected anomaly snapshot diff log hardening should not claim to sanitize alert details or historical data; it only removes a raw process-log field on a parse-failure branch.

## Recommended Implementation Context

Likely files to touch:

| File Path | Reason |
|---|---|
| `backend/internal/anomaly/snapshot_diff.go` | Replace raw parse-failure `Str("output", snapOutput)` with safe structured fields. |
| `backend/internal/anomaly/snapshot_diff_test.go` | Add regression coverage proving raw parse-failure output fragments do not appear in the log event/helper output and existing parser behavior remains intact. |

Suggested verification commands:

```bash
cd /Users/weibo/Code/xirang/backend && go test ./internal/anomaly -count=1
cd /Users/weibo/Code/xirang/backend && go test ./... -count=1
cd /Users/weibo/Code/xirang/backend && go build ./...
git diff --check
```

Out of scope for the implementation slice:

- External Vault/KMS/secret brokers, SSH CA, session recording, command approval, WebAuthn/passkeys, and device trust.
- Config export/import API payload redesign, grant semantics redesign, or frontend grant-flow redesign.
- AppCredential profile rendering, policy hook storage, policy hook response fields, or hook execution redesign.
- rclone/restic command construction changes, remote `RESTIC_PASSWORD` execution replacement, and snapshot path/result redaction.
- Snapshot indexer, file browser, Docker/Nginx, node-log, task-log, task-run, and diagnostic/preflight changes already covered by prior slices.
- Migrations, deployment, Docker, CI workflow, docs, or frontend changes.
