# P4 diagnostic evidence sanitizer

## Goal

Reduce the next highest remaining P4 evidence-exposure surface by sanitizing Node Doctor and node migration preflight diagnostic responses/audit metadata, while preserving current API shape, authorization gates, diagnostic behavior, and deployment/runtime configuration.

## What I already know

* The previous P4 node-log content/path sanitization slice is complete, merged, released as v0.43.18, Docker published, and local `main` was synced before this task branch.
* The archived restic repository password resolver seam already exists as completed P4 work, so this task must not repeat that slice.
* Prior P4 research ranked Node Doctor + migration preflight diagnostic evidence sanitizer as the next best executable slice after node-log storage/response sanitization.
* `backend/internal/api/handlers/node_doctor_handler.go` already funnels Doctor evidence/suggestions through `sanitizeDoctorEvidence`, but the current sanitizer only hides selected secret markers and does not broadly hide paths, hostnames, endpoint-like values, or command output fragments.
* `backend/internal/api/handlers/node_migrate_preflight_handler.go` returns preflight node host fields, policy source paths, raw SSH/probe/dial errors, and path check messages to clients.
* Existing credential-audit metadata for Doctor and preflight is already mostly count/stage based; this task must preserve that and avoid copying diagnostic strings into audit metadata.

## Requirements

* Add or strengthen backend-local sanitization for diagnostic/preflight evidence so responses do not expose raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text, file contents, Docker output, diagnostic output, exported/imported secret material, raw SQL, endpoint/proxy values, hostnames, include-path contents, target-path contents, or host-sensitive strings.
* Preserve Node Doctor API shape: `node_id`, `node_name`, `generated_at`, `checks[].check/status/evidence/suggestion` remain present and semantically useful.
* Preserve migration preflight API shape: `sourceNode`, `targetNode`, `policies`, `taskCount`, `checks`, `canProceed`, `dataMigratable`, and `dataSizeMb` remain present.
* Sanitize migration preflight `sourceNode.host`, `targetNode.host`, policy `sourcePath`, SSH/probe/dial error messages, and path check messages before returning to clients.
* Keep operational behavior unchanged: same checks run, same pass/warn/fail/skip decisions, same tool detection, same disk/data migratable calculations, same auth/RBAC/ownership/credential-audit ordering.
* Keep audit metadata safe and count/stage based; do not add diagnostic strings, hostnames, paths, command text/output, endpoint/proxy values, or raw errors to audit metadata.
* Keep this backend-only with no migrations, env vars, deployment changes, public route changes, frontend changes, external providers, SSH CA, session recording, or command approval.

## Acceptance Criteria

* [ ] Node Doctor evidence and suggestions are sanitized for secret markers, command output fragments, absolute/remote paths, hostnames/IPs/endpoints, endpoint/proxy-like values, and host-sensitive tokens.
* [ ] Node Doctor response still includes stable check names/statuses and useful generic evidence/suggestions for auth, SSH, known_hosts, sudo, tools, backup_dir, disk, and probe checks.
* [ ] Migration preflight response masks host fields and policy source path fields without removing those JSON fields.
* [ ] Migration preflight check messages no longer include raw probe/dial errors or raw policy source paths.
* [ ] Credential-audit metadata for Doctor/preflight remains free of raw diagnostic evidence and still includes counts/outcome context.
* [ ] Tests cover representative host/path/error/command-output leakage cases for Doctor sanitizer and migration preflight response construction.
* [ ] Targeted backend tests pass.
* [ ] Full backend tests and backend build/lint pass before commit.

## Definition of Done

* Implementation is minimal and backend-local.
* No user-visible route or deployment configuration changes are introduced.
* Tests prove the new sanitizer does not leak representative host/path/output/evidence tokens while preserving existing behavior.
* Trellis task is started, implementation/check agents complete, code is committed, task archived, journal recorded, PR created/merged, CI green, release/Docker automation monitored if triggered, and local `main` synced.

## Technical Approach

Introduce a shared diagnostic evidence sanitizer in the handlers layer or a small backend utility that can be applied to both Node Doctor and migration preflight response fields. Reuse `util.SanitizeMessage` for existing credential/secret patterns, then add diagnostic-specific masking for paths, remote path specs, URLs/endpoints, hostnames/IPs, output markers, and host-sensitive tokens. Keep numeric capacities, counts, statuses, tool names, and generic action labels intact because they are needed for troubleshooting and do not reveal host-specific evidence.

For migration preflight, preserve DTO field names but replace raw `Host` and `SourcePath` values with safe placeholders. Replace raw error interpolation with sanitized/generic classifications. Keep checks and audit outcomes based on the original runtime decisions, not on sanitized text.

## Decision (ADR-lite)

**Context**: Completed P4 slices already covered credential/provider seams, integration notification access, task runtime logs, and node logs. Remaining high-value local surfaces include diagnostic/preflight evidence and Docker/file-browser responses. File browser hardening is product-breaking, and Docker volume masking is lower blast radius than diagnostic evidence.

**Decision**: Implement Node Doctor + migration preflight diagnostic evidence sanitization as the next P4 slice.

**Consequences**: Diagnostic responses become less specific but safer by default. Operators keep check status, counts, capacity numbers, and generic remediation hints, while raw host/path/error evidence is removed from responses and audit metadata. This does not add external providers or change deployment/API shape.

## Out of Scope

* External Vault/KMS/Boundary/Teleport/SSH CA/session recording/command approval.
* New migrations, env vars, provider tables, public API routes, frontend UI, or deployment changes.
* Changing which diagnostics run, adding new diagnostic probes, or changing operational pass/warn/fail thresholds.
* File browser path/content response redesign.
* Docker volume discovery response redesign.
* Reworking existing credential provider/resolver seams already completed in prior P4 tasks.

## Technical Notes

* Relevant specs: `.trellis/spec/backend/logging-guidelines.md`, `.trellis/spec/backend/error-handling.md`, `.trellis/spec/backend/quality-guidelines.md`.
* Prior research reference: `.trellis/tasks/archive/2026-05/05-24-p4-next-security-hardening/research/next-p4-slice.md` ranked diagnostic/preflight evidence sanitizer as the next best follow-up.
* Prior completed restic PRD: `.trellis/tasks/archive/2026-05/05-23-p4-restic-credential-resolver/prd.md` confirms restic repository password resolver seam is already done.
