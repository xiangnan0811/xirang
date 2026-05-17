# Research: Backup Confidence Center code foundations

- **Query**: Research the existing code foundations and implementation constraints for `.trellis/tasks/05-17-backup-confidence-center` (Backup Confidence Center MVP), focused on backup health, TaskRun/Policy evidence, drill/verifier/integrity/reporting sources, frontend health panel, tests, and concrete gaps for confidence scoring/read model.
- **Scope**: internal
- **Date**: 2026-05-17

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/handlers/overview_backup_health_handler.go` | Existing `/overview/backup-health` backend aggregation for stale nodes, degraded policies, and 7-day task-run success trend. |
| `backend/internal/api/handlers/overview_backup_health_handler_test.go` | Backend tests for stale node detection, degraded policy detection, trend aggregation, empty trend, and summary stats. |
| `backend/internal/model/models.go` | Core evidence models: `Node`, `Policy`, `Task`, `TaskRun`, `Alert`, `Report`; also sensitive-field sanitization/encryption hooks. |
| `backend/internal/task/runner.go` | Main backup/restore execution path; creates and updates `TaskRun`, verification status, last backup timestamp, and task/verification alerts. |
| `backend/internal/task/drill.go` | Restore drill execution path; creates `TaskRun` records with `trigger_type = "drill"`, updates final status, emits drill logs/alerts. |
| `backend/internal/task/drill_test.go` | Tests drill validation and `TriggerDrill` creation of drill task runs. |
| `backend/internal/task/verifier/verifier.go` | Verification dispatcher for rsync/restic/rclone backup and restore checks. |
| `backend/internal/task/verifier/verifier_test.go` | Tests verification routing for normal backup versus restore. |
| `backend/internal/task/integrity_checker.go` | Periodic restic/rclone integrity checker; persists failures as alerts. |
| `backend/internal/task/retention_worker.go` | Periodic worker that schedules retention/storage/expiry/integrity checks. |
| `backend/internal/alerting/dispatcher.go` | Alert creation helpers for task failure, verification failure, integrity failure, and drill failure. |
| `backend/internal/reporting/generator.go` | SLA report generation and unexported RPO/RTO calculation over `TaskRun` evidence. |
| `backend/internal/api/handlers/task_run_handler.go` | Existing TaskRun read APIs for task runs and logs. |
| `backend/internal/api/handlers/policy_handler.go` | Policy CRUD and `/policies/:id/drill-trigger`; policy response includes drill/RPO/RTO fields. |
| `backend/internal/api/handlers/task_handler.go` | Task CRUD and restore trigger endpoint; list/get paths already include live run state enrichment. |
| `backend/internal/api/handlers/alert_handler.go` | Alert list/read APIs and filter surface. |
| `backend/internal/api/router.go` | Route registration and RBAC/ownership middleware for backup health, drill trigger, task runs, tasks, alerts, and reports. |
| `backend/cmd/server/main.go` | Server startup wiring for task manager and drill loop. |
| `backend/internal/database/migrations/sqlite/000006_task_runs.up.sql` | Creates `task_runs` with `trigger_type` and `verify_status` evidence columns. |
| `backend/internal/database/migrations/postgres/000006_task_runs.up.sql` | PostgreSQL version of task run migration. |
| `backend/internal/database/migrations/sqlite/000052_drill_config.up.sql` | Adds recovery drill config fields to policies. |
| `backend/internal/database/migrations/sqlite/000055_rpo_rto_gfs.up.sql` | Adds policy/report RPO/RTO and retention/GFS fields. |
| `web/src/components/backup-health-panel.tsx` | Existing frontend panel for backup health summary, trend, stale nodes, and degraded policies. |
| `web/src/pages/backups-page.tsx` | Backups page entry that renders `BackupHealthPanel`, storage panel, and guide card. |
| `web/src/lib/api/overview-api.ts` | Frontend API client and mapper for `/overview/backup-health`. |
| `web/src/lib/api/task-runs-api.ts` | Frontend TaskRun API mapper; currently does not preserve all declared trigger types. |
| `web/src/lib/api/reports-api.ts` | Frontend report API types include actual/compliant RPO/RTO fields. |
| `web/src/types/domain.ts` | Frontend domain types for policies, task runs, alerts, backup health, reports/SLOs. |
| `web/src/pages/backups-page.test.tsx` | Frontend tests around Backups page and backup health panel rendering. |
| `web/src/lib/api/overview-api.test.ts` | Overview API tests; currently focused on summary/traffic mapping. |
| `.trellis/spec/frontend/type-safety.md` | Related spec note found via search: API modules own raw-to-domain mapping helpers such as `mapBackupHealth`. |

### Code Patterns

#### Current backup health backend aggregation

- `backend/internal/api/handlers/overview_backup_health_handler.go:35-41` sets the stale backup threshold to 48 hours by default and allows `BACKUP_STALE_THRESHOLD_HOURS` override.
- `backend/internal/api/handlers/overview_backup_health_handler.go:52` selects stale nodes where `last_backup_at IS NULL OR last_backup_at < ?`.
- `backend/internal/api/handlers/overview_backup_health_handler.go:77-98` builds degraded policies by checking enabled policies whose latest three task-run statuses are all `failed`.
- `backend/internal/api/handlers/overview_backup_health_handler.go:110-156` builds a zero-filled 7-day trend from `task_runs`, grouped by date and status.
- `backend/internal/api/handlers/overview_backup_health_handler.go:169-178` normalizes nil degraded policies and returns `degraded_policies`, `degraded_count`, and `trend`.

This endpoint is a useful existing foundation for health evidence, but it currently produces aggregate health data rather than per policy/node confidence status, reasons, evidence list, and next steps.

#### Backup health backend tests

- `backend/internal/api/handlers/overview_backup_health_handler_test.go:32` uses in-memory migration for `Node`, `Policy`, `Task`, and `TaskRun`.
- `backend/internal/api/handlers/overview_backup_health_handler_test.go:193`, `:229`, `:262`, `:298`, `:333-343`, `:542`, and `:550` create `TaskRun` fixtures for degraded-policy and trend scenarios.
- Covered behavior includes never-backed-up nodes, stale nodes, disabled policy exclusion, latest-three-failure degradation, non-degradation with a success, 7-day trend aggregation, zero-filled trend, and summary stats.

These tests provide the nearest existing backend test style for confidence scoring/reason combinations.

#### Core evidence models

- `backend/internal/model/models.go:41-42` defines `Node.Sanitized()` for removing sensitive node fields before API responses.
- `backend/internal/model/models.go:89-131` defines `Policy` evidence/config fields including `rpo_minutes`, `rto_minutes`, `verify_enabled`, `verify_sample_rate`, `drill_enabled`, drill cron/target/restore path, and drill scripts.
- `backend/internal/model/models.go:291-308` defines `Task` evidence fields including `policy_id`, `node_id`, `status`, `last_run_at`, `next_run_at`, `last_error`, `retry_count`, `executor_type`, encrypted `executor_config`, and `verify_status`.
- `backend/internal/model/models.go:322-341` encrypts/decrypts `Task.ExecutorConfig` via GORM hooks.
- `backend/internal/model/models.go:345-357` defines `TaskRun` evidence fields including `task_id`, `trigger_type`, `status`, `started_at`, `finished_at`, `duration_ms`, `verify_status`, `throughput_mbps`, `progress`, and `last_error`.
- `backend/internal/model/models.go:259` starts the `Alert` model; alert records carry task/policy/node/error/severity/status context for failures.
- `backend/internal/model/models.go:488-501` defines `Report` fields including `actual_rpo_minutes`, `actual_rto_minutes`, `rpo_compliant`, and `rto_compliant`.

Security-relevant constraint: confidence responses must not expose full models that can contain sensitive or internal fields. In particular, `Node` has sensitive fields that require `Sanitized()`, and `Task.ExecutorConfig` is decrypted by hooks in memory. Policy responses currently expose drill script fields, so confidence evidence should use explicit safe DTO fields if implemented.

#### TaskRun evidence from backup/restore runner

- `backend/internal/task/runner.go:159-161` creates a `TaskRun` with `TriggerType` set from the trigger reason.
- `backend/internal/task/runner.go:414` invokes `verifier.Verify(...)` for normal backup verification when the policy enables verification.
- `backend/internal/task/runner.go:426-450` updates run status and `verify_status` after verification outcomes.
- `backend/internal/task/runner.go:456` raises verification-failure alerts tied to the task run.
- `backend/internal/task/runner.go:467-484` stores final success status, timing, progress, and verification status.
- `backend/internal/task/runner.go:493` updates `nodes.last_backup_at` after successful backup.
- `backend/internal/task/runner.go:601-602` raises task-failure alerts with `task_run_id` context.
- `backend/internal/task/runner.go:786` forces restore verification after restore execution.
- `backend/internal/task/runner.go:793-829` updates restore run status and verification status, and raises verification failure for restore verification failures.
- `backend/internal/task/runner.go:897-899` creates dependent chain `TaskRun` records with `TriggerType: "chain"`.

`TaskRun` is the strongest existing read source for recent backup outcome, restore outcome, drill outcome, verification status, duration, and last error.

#### Restore drill evidence

- `backend/internal/task/drill.go:57-59` documents `TriggerDrill(policyID uint) (uint, error)` as creating a drill `TaskRun` and returning its ID.
- `backend/internal/task/drill.go:96-98` creates `TaskRun{TriggerType: "drill"}`.
- `backend/internal/task/drill.go:165-171` updates the drill run to running.
- `backend/internal/task/drill.go:187-198`, `:215-226`, and `:287-305` set final drill status and raise drill-failure alerts for sandbox unreachable, restore failed, and verification failed.
- `backend/internal/task/drill.go:480-527` runs the drill loop, scans policies where `drill_enabled = true`, matches cron, and triggers drills.
- `backend/cmd/server/main.go` wires `taskManager.StartDrillLoop()` during production startup.

There is no dedicated drill evidence model; drill evidence is represented by `TaskRun(trigger_type="drill")`, `TaskLog`, and drill alerts.

#### Verifier evidence

- `backend/internal/task/verifier/verifier.go:35` exposes `Verify(ctx, task, sampleRate, db, logf, isRestore) Result`.
- The `Result` type includes `Status` (`passed`, `warning`, `failed`), `Message`, source/destination counts/sizes, sampled files, and mismatch count.
- Normal backups dispatch by executor type: restic uses repository check, rclone uses rclone check, rsync/command use remote/local comparison paths.
- Restore verification is explicit through `isRestore`; restic/rclone restore verification can return passed with a skip-style message because the executor is treated as having built-in error detection.
- Runner persists only status/message-level evidence to `TaskRun.verify_status` and `TaskRun.last_error`; detailed verifier counts are not stored as a structured model.

#### Integrity checker evidence

- `backend/internal/task/integrity_checker.go` scans enabled policies and checks restic/rclone task integrity.
- `backend/internal/task/integrity_checker.go:90` and `:126` persist restic/rclone integrity failures via `alerting.RaiseIntegrityCheckFailure(...)`.
- `backend/internal/task/retention_worker.go` runs integrity checks as part of periodic maintenance; default periodicity is tied to retention worker ticks and `INTEGRITY_CHECK_MULTIPLIER`.

Integrity failure evidence exists as alerts. Successful periodic integrity checks are logged but not persisted as dedicated structured evidence.

#### Alert evidence

- `backend/internal/alerting/dispatcher.go:102` defines `RaiseTaskFailure(...)`.
- `backend/internal/alerting/dispatcher.go:124` defines `RaiseVerificationFailure(...)`.
- `backend/internal/alerting/dispatcher.go:225` defines `RaiseIntegrityCheckFailure(...)`.
- `backend/internal/alerting/dispatcher.go:241-246` documents drill failure error classes: `drill_sandbox_unreachable`, `drill_verify_failed`, and `drill_restore_failed`.
- `backend/internal/alerting/dispatcher.go:246-248` defines `RaiseDrillFailure(...)`; sandbox unreachable is warning severity and other drill failures are critical.

Correlation detail: task failure and verification failure alerts include `TaskRunID`; integrity alerts include policy ID in the error code; drill failure alerts include policy/node context but are not persisted with the drill `TaskRunID` in the helper signature.

#### Reporting / RPO / RTO evidence

- `backend/internal/reporting/generator.go:67` calls `computeRPOAndRTO(...)` while generating reports.
- `backend/internal/reporting/generator.go:224-234` documents RPO/RTO calculation semantics.
- `backend/internal/reporting/generator.go:226-229` states RPO uses successful backup `TaskRun` records excluding restore/drill, and RTO uses the most recent successful restore `TaskRun` duration.
- `backend/internal/reporting/generator.go:271` computes policy RPO for policies with targets.
- `backend/internal/reporting/generator.go:293` computes policy RTO for policies with targets.
- `backend/internal/reporting/generator.go:323-328` starts `computePolicyRPO`, querying recent successful task runs where `trigger_type NOT IN (restore, drill)`.
- `backend/internal/reporting/generator.go:352-356` starts `computePolicyRTO`, querying the most recent successful `trigger_type = restore` task run.

RPO/RTO logic exists but is report-scoped and unexported. Persisted report rows expose `actual_rpo_minutes`, `actual_rto_minutes`, `rpo_compliant`, and `rto_compliant` after report generation.

#### Existing API surface and middleware

- `backend/internal/api/router.go:136` registers `GET /overview/backup-health` with `tasks:read` RBAC.
- `backend/internal/api/router.go:262` registers `POST /policies/:id/drill-trigger` with `tasks:trigger` RBAC.
- `backend/internal/api/router.go:270` registers `GET /tasks/:id/runs` with `tasks:read` and `OwnershipTaskCheck`.
- `backend/internal/api/router.go:280-281` registers `GET /task-runs/:id` and `GET /task-runs/:id/logs` with `tasks:read`.
- `backend/internal/api/handlers/task_run_handler.go:40-64` lists task runs for a task with pagination/status filtering.
- `backend/internal/api/handlers/task_run_handler.go:85-100` fetches a task run and handles orphaned task-run read permissions.
- `backend/internal/api/handlers/task_run_handler.go:134-160` fetches task-run logs and handles orphaned task-run read permissions.
- `backend/internal/api/handlers/policy_handler.go:875` marks the policy drill trigger route handler.

Existing read APIs can expose raw task-run/log evidence for one task/run. There is no existing confidence-specific aggregate endpoint or confidence DTO.

#### Frontend backup health foundations

- `web/src/types/domain.ts:129-137` defines policy drill fields in the frontend `PolicyRecord`.
- `web/src/types/domain.ts:225` declares `TaskRunTriggerType = "manual" | "cron" | "retry" | "restore" | "chain" | "drill"`.
- `web/src/types/domain.ts:451` starts `BackupHealthData`, the current frontend health shape for stale nodes, degraded policies, trend, and summary.
- `web/src/lib/api/overview-api.ts:73` starts `BackupHealthRaw` for `/overview/backup-health`.
- `web/src/lib/api/overview-api.ts:85` starts `mapBackupHealth(...)`.
- `web/src/lib/api/overview-api.ts:175-177` fetches `/overview/backup-health` and maps it to `BackupHealthData`.
- `web/src/components/backup-health-panel.tsx:22-39` fetches backup health with loading/error state.
- `web/src/pages/backups-page.tsx:17-31` fetches backup health for the page subtitle while the panel also fetches its own health data.
- `web/src/pages/backups-page.tsx:62` renders `<BackupHealthPanel />` as the current clear frontend entry under Backups.
- `web/src/pages/backups-page.test.tsx:73` verifies the current page renders “Never backed up” / “从未备份”.

Current UI shows backup health summary/trend/problems. It does not have confidence status, reason codes, evidence list, restore-drill signal, RPO/RTO signal, alert signal, or next-step actions.

#### Frontend TaskRun mapper gap relevant to drill evidence

- `web/src/types/domain.ts:225` includes `"chain"` and `"drill"` in `TaskRunTriggerType`.
- `web/src/lib/api/task-runs-api.ts:51` starts `mapTriggerType(raw: string)`.
- The mapper preserves `manual`, `cron`, `retry`, and `restore`, but unknown values fall back to `manual`; therefore backend `chain` and `drill` trigger types are not preserved by this mapper.

This is a concrete frontend evidence display gap for drill/chain task runs.

### External References

None. This research was internal-only.

### Related Specs

- `.trellis/spec/frontend/type-safety.md` — related type-safety guidance discovered by search; API modules own raw-to-domain mapping helpers such as `mapBackupHealth`.
- `.trellis/spec/frontend/component-guidelines.md` — relevant if frontend confidence UI is added under existing component patterns.
- `.trellis/spec/frontend/a11y-guidelines.md` — relevant if confidence UI adds status badges, lists, or actions.
- `.trellis/spec/backend/error-handling.md` — relevant if a confidence endpoint returns new error shapes.
- `.trellis/spec/backend/quality-guidelines.md` — relevant to backend scoring/reason tests.

## Caveats / Not Found

- No existing `confidence` backend API, service, model, DTO, or frontend type was found.
- No dedicated restore-drill evidence/read model was found; drill evidence is currently spread across `TaskRun`, logs, and alerts.
- No persisted structured success record for periodic integrity checks was found; integrity failures are persisted as alerts.
- No reason-code or next-step contract for backup confidence was found.
- No frontend confidence UI or confidence API client was found.
- No backend tests for confidence scoring/reason combinations exist yet.
- No frontend tests for confidence UI exist yet.
- RPO/RTO calculation exists in reporting but is unexported and tied to generated report rows.
- Existing `/overview/backup-health` is aggregate health, not per-object explainable confidence.
- Alert list filters support status/node/task/severity/keyword, but no direct policy-id filter was identified from the researched alert handler context.
- Confidence API work must respect the acceptance criterion: “Confidence API 不暴露敏感字段。” Relevant existing constraints include `Node.Sanitized()` for password/private key removal and `Task.ExecutorConfig` GORM decryption hooks.
