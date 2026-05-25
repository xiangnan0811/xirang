# Research: backup/restore posture signal

- **Query**: Research a low-burden, report-only backup/restore safety posture signal for Xirang P5 small-team security roadmap. Focus on repo-derived patterns and comparable self-hosted backup posture conventions: stale backups, failed backups, missing restore drills, retention/recoverability confidence. Map recommendations to existing Xirang models/APIs and explicitly avoid enterprise policy/device trust/approval/session recording/full Vault/KMS/SSH CA.
- **Scope**: mixed
- **Date**: 2026-05-25

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/handlers/backup_confidence_handler.go` | Existing policy-scoped backup confidence endpoint and scoring/reason/evidence model for recoverability posture. |
| `backend/internal/api/handlers/overview_backup_health_handler.go` | Existing stale backup/degraded policy/7-day success trend endpoint. |
| `backend/internal/api/handlers/health_incident_timeline_handler.go` | Read-only incident timeline that surfaces backup stale and degraded signals without creating incident rows or remediation. |
| `backend/internal/api/router.go` | Routes/RBAC for backup health, backup confidence, health incident timeline, reports, and protected restore endpoints. |
| `backend/internal/model/models.go` | Core models and fields for Node last backup time, Policy RPO/RTO/retention/drill settings, Task/TaskRun status, RestoreDrillEvidence, Report RPO/RTO compliance. |
| `backend/internal/reporting/generator.go` | SLA report generator that persists run success/failure, top failures, disk trend, actual RPO/RTO, and compliance. |
| `backend/internal/api/handlers/report_handler.go` | Report config/report CRUD and generate endpoints, including scope validation and operator ownership filtering. |
| `backend/internal/task/runner.go` | Task run lifecycle: successful normal backup updates TaskRun fields and `Node.last_backup_at`; restore runs remain separate run evidence. |
| `backend/internal/task/drill.go` | Structured restore drill workflow and evidence creation; cross-node transfer path is intentionally blocked for credential safety. |
| `backend/internal/api/handlers/task_run_handler.go` | TaskRun detail and logs endpoints; detail can include sanitized `RestoreDrillEvidence`. |
| `backend/internal/task/integrity_checker.go` | Periodic restic/rclone integrity checks that raise alert evidence on failure. |
| `backend/internal/task/retention.go` | Retention enforcement for rsync/restic/rclone; failures raise retention alert evidence. |
| `backend/internal/task/retention_worker.go` | Background worker cadence for retention, storage, expiry, and periodic integrity checks. |
| `backend/internal/alerting/dispatcher.go` | Alert raisers and code families for task, verification, retention, integrity, and drill failures. |
| `web/src/components/backup-confidence-panel.tsx` | UI panel for backup confidence summary, item cards, reasons, evidence, and next steps. |
| `web/src/components/backup-health-panel.tsx` | UI panel for stale/never-backed-up nodes, degraded policies, and 7-day success trend. |
| `web/src/lib/api/overview-api.ts` | Frontend raw-to-domain mapping for backup health and backup confidence payloads. |
| `web/src/pages/backups-page.tsx` | Current UI home for backup posture panels. |
| `web/src/lib/api/reports-api.ts` | Frontend report types expose actual RPO/RTO and compliance fields. |
| `web/src/pages/reports-page.tsx` | Reports UI lists/generated reports and existing SLA/failure history presentation. |
| `.trellis/spec/backend/error-handling.md` | Backend contract for backup confidence endpoint, restore drill evidence, and read-only health incident timeline. |
| `.trellis/spec/frontend/type-safety.md` | Frontend type/mapping contract for backup confidence and drill evidence payloads. |

### Code Patterns

#### Existing report-only posture primitives

- Backup posture already has two read-only overview endpoints:
  - `GET /api/v1/overview/backup-health` protected by `middleware.RBAC("tasks:read")` in `backend/internal/api/router.go`.
  - `GET /api/v1/overview/backup-confidence` protected by `middleware.RBAC("tasks:read")` in `backend/internal/api/router.go`.
- Reports are already the persistent, low-burden reporting surface:
  - Report config/list/get/generate routes are protected by `reports:read` / `reports:write` in `backend/internal/api/router.go`.
  - `backend/internal/reporting/generator.go` persists generated `Report` rows with run totals, success/failure counts, top failures, disk trend, actual RPO/RTO, and RPO/RTO compliance.

#### Stale backup signal

- `backend/internal/api/handlers/overview_backup_health_handler.go` uses `Node.last_backup_at` as the primary stale/never-backed-up signal.
- Default stale threshold is 48 hours, configurable by `BACKUP_STALE_THRESHOLD_HOURS`.
- The stale query shape is:

```go
Where("last_backup_at IS NULL OR last_backup_at < ?", staleThreshold)
```

- On normal backup success, `backend/internal/task/runner.go` updates `Node.last_backup_at`, making successful backup completion the source of truth for freshness.

#### Failed backup and degraded policy signal

- `backend/internal/api/handlers/overview_backup_health_handler.go` treats a policy as degraded when recent task runs indicate repeated failure, using the latest three task runs as evidence.
- `backend/internal/reporting/generator.go` also persists period-level run totals, failed run counts, success rate, and top failure summaries in generated `Report` rows.
- `backend/internal/alerting/dispatcher.go` raises task failure alerts with the `XR-EXEC-*` family, which `backup_confidence_handler.go` can use as supporting evidence.

#### Restore drill / recoverability confidence signal

- `backend/internal/model/models.go` defines `RestoreDrillEvidence` with structured phase statuses, errors, duration, source snapshot reference, sandbox node/path, and `ConfidenceEligible`.
- `backend/internal/api/handlers/task_run_handler.go` returns optional `drill_evidence` for task-run details and sanitizes runtime evidence before response.
- `backend/internal/api/handlers/backup_confidence_handler.go` already models missing/failed/non-confidence-eligible drills as posture reasons:
  - `drill_missing`
  - `drill_failed`
  - `drill_not_confident`
  - `drill_alert`
- A key caveat is in `backend/internal/task/drill.go`: the old cross-node restore-drill transfer path is intentionally blocked before remote writes because it would spread source SSH credentials to the sandbox node. Therefore, missing/blocked drill evidence is a valid report-only confidence signal, not a prompt to bypass credential-safety controls.

#### Retention and integrity confidence signal

- `backend/internal/task/retention.go` implements retention for:
  - rsync local directory cleanup with dangerous-root protections;
  - restic `forget --prune`, including GFS keep args when `RetentionMode == "gfs"`;
  - rclone `delete --min-age`.
- Retention failures raise `XR-RETN-*` alerts via `alerting.RaiseRetentionFailure`.
- `backend/internal/task/integrity_checker.go` implements periodic restic/rclone integrity checks and raises `XR-INTG-*` alerts on failure.
- `backend/internal/api/handlers/backup_confidence_handler.go` includes retention/recoverability-adjacent reason/evidence patterns through verification and integrity alert signals.

#### Existing confidence scoring and status pattern

- `backend/internal/api/handlers/backup_confidence_handler.go` returns:
  - generated time;
  - summary counts;
  - per-policy/per-node confidence items;
  - status;
  - score;
  - reasons;
  - evidence;
  - next steps;
  - targets.
- Existing reason codes cover the requested conventions:
  - stale/missing backup evidence: `no_successful_backup`, `backup_not_completed`, `rpo_unknown`, `rpo_exceeded`;
  - failed backups: `recent_backup_failed`, `recent_run_failed`, plus task failure alerts;
  - missing restore drills: `drill_missing`;
  - failed or non-confident restore drills: `drill_failed`, `drill_not_confident`, `drill_alert`;
  - verification/integrity: `verify_failed`, `verify_warning`, `verify_missing`, `integrity_alert`, `verification_alert`.
- Status mapping is low-burden and user-facing rather than enterprise compliance-oriented:
  - `healthy`
  - `warning`
  - `at_risk`
  - `insufficient`

#### Sanitization / safety pattern

- `backend/internal/api/handlers/task_run_handler.go` sanitizes `TaskRun.LastError`, `TaskLog.Message`, and restore drill evidence errors with runtime evidence sanitizers.
- `backend/internal/api/handlers/backup_confidence_handler.go` uses `util.SanitizeMessage` and avoids leaking raw system paths, credentials, tokens, passwords, and executor config.
- `.trellis/spec/backend/error-handling.md` explicitly requires backup confidence to avoid leaking host, raw paths, executor config, private keys, passwords, or tokens.

#### UI placement pattern

- `web/src/pages/backups-page.tsx` already places `BackupConfidencePanel`, `BackupHealthPanel`, `StorageUsagePanel`, and `StorageGuideCard` together. This page is the current natural home for backup posture.
- `web/src/pages/reports-page.tsx` is the persistent report/history surface. Existing report frontend types in `web/src/lib/api/reports-api.ts` already expose actual RPO/RTO and compliance values.

### Mapping to Existing Xirang Models / APIs

| Posture convention | Existing model fields | Existing API/UI surface | Notes |
|---|---|---|---|
| Stale backups / never backed up | `Node.LastBackupAt`; normal backup `TaskRun.Status=success` | `GET /overview/backup-health`; `BackupHealthPanel`; health incident timeline `backup_stale` | Uses 48h default threshold with env override. |
| Failed backups / degraded policy | `TaskRun.Status`, `Task.Status`, `Task.LastError`; Alert `XR-EXEC-*` | `GET /overview/backup-health`; `GET /overview/backup-confidence`; generated `Report` success/failure/top failures | Health uses recent run failures; reports persist period aggregates. |
| RPO breach / freshness confidence | `Policy.RPOMinutes`; `TaskRun.StartedAt`; `Report.ActualRPOMinutes`, `Report.RPOCompliant` | `GET /overview/backup-confidence`; Reports API/UI | Reporting generator already computes actual RPO from successful non-restore/drill runs. |
| RTO / restore duration confidence | `Policy.RTOMinutes`; restore `TaskRun.DurationMs`; `Report.ActualRTOMinutes`, `Report.RTOCompliant` | Reports API/UI; task-run details | Reporting generator already computes RTO from latest successful restore run duration. |
| Missing restore drill | `Policy.DrillEnabled`, `RestoreDrillEvidence`, drill `TaskRun.TriggerType` | `GET /overview/backup-confidence`; task-run detail `drill_evidence` | Missing drill evidence should be represented as insufficient confidence. |
| Failed / not confidence-eligible drill | `RestoreDrillEvidence.Status`, `ConfidenceEligible`, phase statuses/errors | `GET /overview/backup-confidence`; `GET /task-runs/:id` | Errors are sanitized before API response. |
| Retention confidence | `Policy.RetentionDays`, `RetentionMode`, `KeepDaily/Weekly/Monthly/Yearly`; Alert `XR-RETN-*` | Retention worker and alerts; confidence can consume alert evidence | Existing retention worker enforces cleanup and raises failure alerts. |
| Integrity / recoverability confidence | verification fields, integrity alerts `XR-INTG-*`, verification alerts `XR-VRFY-*` | `GET /overview/backup-confidence`; alerting surfaces | Confidence endpoint already includes verification/integrity/drill alert evidence patterns. |

### Comparable Self-hosted Backup Posture Conventions

Self-hosted backup tools and guidance commonly describe backup safety in terms of evidence rather than enterprise governance controls:

- Restic documentation emphasizes repository checks (`restic check`) and forget/prune retention workflows. This maps to Xirang's restic integrity checker and retention worker.
- BorgBackup documentation emphasizes checking repository/archives (`borg check`) and pruning retention (`borg prune`). This supports the same posture dimensions: recoverability checks plus retention enforcement.
- rclone documentation supports one-way comparison/check workflows (`rclone check`) and age-based deletion. This maps to Xirang's rclone integrity and retention paths.
- 3-2-1 backup guidance from public cybersecurity sources emphasizes multiple copies, separate media/location, and restore confidence. For this small-team slice, the repo-aligned interpretation is report-only evidence of backup freshness, failures, restore tests, retention, and integrity—not device trust, approval workflows, session recording, Vault/KMS, SSH CA, or enterprise policy engines.

### External References

- [restic documentation](https://restic.readthedocs.io/) — relevant for `restic check`, `forget`, and `prune` concepts already mirrored by Xirang integrity and retention workers.
- [BorgBackup documentation](https://borgbackup.readthedocs.io/) — comparable self-hosted backup convention around archive checks and retention pruning.
- [rclone documentation](https://rclone.org/docs/) — relevant for `rclone check` and deletion/age-based retention behavior.
- [CISA ransomware guidance and 3-2-1 backup materials](https://www.cisa.gov/) — general public-sector guidance that backup posture should include backup availability and restore confidence; use only as high-level context for small-team reporting.
- [NIST Cybersecurity Framework 2.0](https://www.nist.gov/cyberframework) — high-level recover/recovery-planning context; avoid translating it into enterprise compliance workflows for this slice.

### Related Specs

- `.trellis/spec/backend/error-handling.md` — Defines backup confidence endpoint contract, restore drill evidence contract, health incident timeline contract, read-only/no-remediation constraints, RBAC, and sanitization requirements.
- `.trellis/spec/frontend/type-safety.md` — Defines frontend mapping expectations for backup confidence, drill evidence, and `TaskRunTriggerType` including `"drill"`.

## Caveats / Not Found

- The requested low-burden, report-only posture slice should stay within existing read/reporting surfaces and explicitly avoid enterprise policy, device trust, approval flows, session recording, full Vault/KMS, SSH CA, or similar infrastructure-heavy features.
- Cross-node restore drill transfer is currently blocked by design in `backend/internal/task/drill.go` to avoid spreading source SSH credentials to sandbox nodes. This means missing or blocked restore-drill evidence can appear in posture reporting and should be treated as confidence evidence, not as a request to bypass the safety block.
- Existing `Report` rows persist RPO/RTO and run/failure statistics, but the research did not find a persisted report field that directly embeds the full `backup-confidence` reason/evidence breakdown. The current detailed confidence breakdown is exposed by the read-only overview confidence endpoint and frontend panel.
- External references were used for convention mapping only. Exact external wording should be rechecked before quoting in user-facing documentation.
