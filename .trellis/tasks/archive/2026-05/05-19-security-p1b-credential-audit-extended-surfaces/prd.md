# P1b Extend Credential Audit Coverage

## Goal

Extend the P1 credential-use audit model beyond the core Approach B surfaces to the remaining high-risk SSH and secret-bearing paths, while preserving secret-safe bounded event metadata and avoiding full session/file recording.

## What I already know

- P1 shipped `credential_audit_events`, SSH key least-privilege scope metadata, purpose constants, handler/runtime audit helpers, Settings risk aggregation, and frontend risk mapping.
- Generic HTTP audit intentionally skips read-style GET operations, so sensitive GET surfaces need explicit domain audit events.
- P1b is a coverage-extension task, not a new audit UI/export or step-up-auth task; those remain P1c/P1d.
- Existing P1 purpose constants already cover most P1b SSH surfaces: `file_browser`, `docker_volumes`, `node_logs`, `probe`, `node_migration`, `snapshot`, `snapshot_diff`, `integrity_check`, and `retention`.
- Config export with `include_secrets=true` is admin-only and currently writes a regular audit logger warning, but not a domain credential-use event.

## Requirements

- Add explicit credential-use audit events for SFTP/file list and file preview/read paths.
- Add explicit credential-use audit events for Docker volume discovery over SSH.
- Add explicit credential-use audit events for config export, especially `include_secrets=true`, without storing exported payloads or secret values.
- Add explicit credential-use audit events for node doctor diagnostics and migration preflight SSH diagnostics.
- Add bounded system-actor credential audit for background SSH surfaces where useful and not excessively noisy; final cadence is the remaining product decision.
- Extend Settings security risk summary aggregation to count newly high-risk credential actions without exposing raw sensitive data.
- Keep all event metadata bounded and sanitized; never store private keys, passwords, tokens, command output, terminal/file streams, executor config, exported secret material, raw file contents, or raw diagnostic evidence.

## Acceptance Criteria

- [ ] New events include actor/system context, node/key/task identifiers where available, purpose/action, credential source/key ID where resolved, and outcome.
- [ ] Sensitive GET-style operations that are skipped by the generic HTTP audit middleware have explicit credential-use events.
- [ ] SFTP file preview audit proves file content and SFTP read payloads are not persisted.
- [ ] Docker volume audit proves remote command output and unsafe volume names are not persisted.
- [ ] Config export audit proves default export omits secrets, `include_secrets=true` is admin-only, and no exported payload/secret material is persisted in audit metadata.
- [ ] Node doctor and migration preflight audit tests cover representative success/failure/blocked paths and no raw diagnostic evidence/host-sensitive strings are copied into credential audit metadata.
- [ ] Settings/security-health signals aggregate new high-risk credential operations without exposing raw metadata/error content.
- [ ] Backend tests cover representative success/failure/blocked paths for the new audited surfaces.

## Definition of Done

- Backend tests added/updated for security-sensitive behavior.
- `cd backend && go test ./... -count=1` passes.
- Frontend checks run only if frontend risk mapper/UI changes are included.
- `bash scripts/check-doc-freshness.sh` and migration safety checks pass if relevant hooks require them.
- Code-spec updates capture any durable audit action/metadata contract changes.
- End-to-end PR workflow continues after implementation.

## Out of Scope

- Rich credential audit listing/search/export UI (P1c).
- Step-up authentication for high-risk operations (P1d).
- Full terminal/file/session recording or replay.
- Storing raw file contents, Docker command output, diagnostic command output, exported config payloads, or decrypted secret values.
- Replacing the existing `credential_audit_events` model/table.
- Adding approval/JIT/PAM/bastion workflows.

## Technical Approach Options

**Approach A: Handler-driven explicit events plus bounded background audit (Recommended)**

- Add direct `credentialaudit.Write` calls in file, Docker, config export, node doctor, and migration preflight handlers.
- For background SSH workers, emit system-actor events at credential-use boundaries only for meaningful failures/blocked outcomes and sampled/coalesced successes where appropriate.
- Pros: strong evidence for sensitive user-triggered operations, avoids high-volume probe spam, keeps P1b focused.
- Cons: background successful SSH use will not be exhaustively recorded.

**Approach B: Exhaustive per-SSH-use audit everywhere**

- Emit credential-use events for every probe, node-log fetch, retention, integrity, snapshot, snapshot-diff, and background SSH dial regardless of outcome.
- Pros: maximum forensic coverage.
- Cons: can flood `credential_audit_events`, distort Settings risk counts, and increase storage/noise for frequent background probes.

**Approach C: User-triggered surfaces only**

- Audit file, Docker, config export, node doctor, and migration preflight only; leave all background worker audit for later.
- Pros: smallest implementation and least noise.
- Cons: misses compromised-system or stale-worker credential usage evidence for background SSH paths.

## Decision (ADR-lite)

**Context**: P1b extends audit evidence to SSH/read/export/diagnostic surfaces that P1 intentionally deferred, but credential audit must remain bounded and useful rather than becoming a high-volume telemetry stream.

**Decision**: Use Approach A. User-triggered file, Docker, config export, node doctor, and migration preflight surfaces get explicit credential-use audit events. Background SSH workers use bounded system-actor audit: record blocked/failure outcomes and limited success summaries where useful, rather than logging every probe/dial.

**Consequences**: This provides strong coverage for sensitive user-triggered operations and meaningful system failures while avoiding audit-table noise from frequent background probes. Exhaustive per-dial background telemetry remains out of scope unless future product requirements need it.

## Research References

- [`research/code-surfaces.md`](research/code-surfaces.md) — Current P1 credentialaudit implementation, missing P1b code surfaces, and recommended boundaries/tests.

## Technical Notes

- Likely backend files: `backend/internal/api/handlers/file_handler.go`, `docker_handler.go`, `config_handler.go`, `node_doctor_handler.go`, `node_migrate_preflight_handler.go`, `settings_handler.go`, plus tests.
- Likely background files if Approach A/B includes them: `backend/internal/probe/prober.go`, `backend/internal/nodelogs/ssh_runner.go`, `backend/internal/task/retention.go`, `backend/internal/task/integrity_checker.go`, `backend/internal/snapshot/indexer.go`, `backend/internal/anomaly/snapshot_diff.go`, `backend/internal/task/verifier/verifier.go`.
- Existing helper `writeCredentialAuditFromGin` is best-effort and logs audit write failures; direct handler events should follow that pattern.
- Existing metadata sanitizer drops keys containing private/password/token/secret/credential/config/output/stream/command, so persisted metadata should use safe names such as `stage`, `kind`, `count`, `truncated`, `scope`, `latency_ms`, `path_hash`, and booleans.
- Settings risk summary currently aggregates high-risk action labels in `highRiskCredentialAuditActions()` and maps labels via `credentialActionLabel()`.
