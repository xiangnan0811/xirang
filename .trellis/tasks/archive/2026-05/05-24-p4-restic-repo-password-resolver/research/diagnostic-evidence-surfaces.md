# Research: diagnostic evidence surfaces

- **Query**: Research the P4 diagnostic evidence sanitizer slice for Xirang; confirm current Node Doctor and node migration preflight response/audit surfaces that can expose diagnostic evidence, hostnames, paths, command output, raw SSH/probe/dial errors, endpoint/proxy values, and host-sensitive strings.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/handlers/node_doctor_handler.go` | Node Doctor response DTO, diagnostic runner, allowlisted remote command execution, response evidence sanitization, credential audit writer. |
| `backend/internal/api/handlers/node_migrate_preflight_handler.go` | Node migration preflight response DTO, SSH/probe/tool/path/disk checks, credential audit writer. |
| `backend/internal/api/handlers/node_doctor_handler_test.go` | Doctor handler tests for rejected input, audit safety, outcome classification, allowlist, and evidence sanitization. |
| `backend/internal/api/handlers/node_handler_test.go` | Migration preflight tests for internal errors, audit safety, and blocked outcome classification. |
| `backend/internal/api/router.go` | Route registration and RBAC/ownership middleware for Doctor and migration preflight endpoints. |
| `web/src/lib/api/nodes-api.ts` | Frontend API client and DTO mapping/types for Doctor and migration preflight. |
| `web/src/components/node-doctor-dialog.tsx` | UI rendering of Doctor evidence/suggestion. |
| `web/src/components/node-migrate-wizard.tsx` | UI storage/rendering of preflight checks, source/target names, policies, and data-size summary. |
| `.trellis/spec/backend/error-handling.md` | Backend contracts for API error secrecy and SSH Fleet Doctor diagnostics. |
| `.trellis/spec/backend/database-guidelines.md` | Credential audit durability contract and forbidden persisted values. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging contract forbidding Doctor evidence and migration preflight command output in logs. |
| `.trellis/spec/backend/quality-guidelines.md` | Credential-use audit event contract and forbidden metadata/error contents. |
| `.trellis/spec/frontend/type-safety.md` | Frontend Doctor mapping/rendering contract. |

### Code Patterns

#### Node Doctor response and UI surfaces

- `doctorCheckResult` includes free-form `Evidence` and `Suggestion` returned in JSON as `evidence` / `suggestion` at `backend/internal/api/handlers/node_doctor_handler.go:38-43`. `doctorResponse` returns `node_id`, `node_name`, `generated_at`, and `checks` at `backend/internal/api/handlers/node_doctor_handler.go:45-50`.
- `RunDoctor` rejects any non-empty request body before running diagnostics, so callers cannot submit arbitrary commands/check definitions (`backend/internal/api/handlers/node_doctor_handler.go:65-74`, helper at `106-118`). Tests cover normal and chunked custom bodies at `backend/internal/api/handlers/node_doctor_handler_test.go:20-69`.
- Doctor check evidence is added only through `r.add`, which calls `sanitizeDoctorEvidence` for both evidence and suggestion (`backend/internal/api/handlers/node_doctor_handler.go:250-257`).
- `sanitizeDoctorEvidence` trims, truncates to 500 runes, hides messages containing secret markers, then delegates to `util.SanitizeMessage` (`backend/internal/api/handlers/node_doctor_handler.go:694-712`). Existing marker coverage includes private keys, password/passwd, token, secret, authorization, bearer, `proxy_url`, and `DATA_ENCRYPTION_KEY`; it does not generically suppress all hostnames or all local/remote path strings.
- `util.SanitizeMessage` redacts URL credentials/query/path to `scheme://***`, common token/key/password patterns, and selected host error patterns for `lookup ...:` and `dial tcp|udp ...:` (`backend/internal/util/sanitize.go:28-45`, `57-87`). It is not a complete host/path classifier.
- Doctor raw SSH dial errors are classified into generic categories by `classifyDoctorSSHEvidence`, e.g. known_hosts, auth, network/port, handshake, or generic connection failure (`backend/internal/api/handlers/node_doctor_handler.go:303-331`). Suggestions are similarly categorical (`334-348`).
- Raw sudo command output can become Doctor evidence: `checkSudo` runs `sudo -n true 2>&1`, takes `strings.TrimSpace(output)` when the command fails, and passes it to `r.add` (`backend/internal/api/handlers/node_doctor_handler.go:358-370`). Because `r.add` sanitizes but does not categorically replace all command output, this is a current diagnostic-output surface.
- Backup directory checks collect configured paths from `Task.RsyncTarget` and `Policy.TargetPath`/`node.BackupDir` (`backend/internal/api/handlers/node_doctor_handler.go:436-518`). Failure evidence includes path values via `不存在：%s` and `不可写：%s` before sanitization (`463-471`), so local backup/target paths are a current response surface.
- Disk/probe evidence returns coarse numbers/status/ages but no host/path strings (`backend/internal/api/handlers/node_doctor_handler.go:520-575`).
- Remote commands are server-side allowlisted in `doctorCommandAllowed`; allowed commands include `sudo -n true 2>&1`, `df -BG / | awk ...`, `command -v <allowed-tool>`, and `test -d/test -w <safe absolute path>` (`backend/internal/api/handlers/node_doctor_handler.go:577-631`). Tests cover allowed/rejected commands at `backend/internal/api/handlers/node_doctor_handler_test.go:256-282`.
- Frontend `mapNodeDoctorResult` maps backend evidence/suggestion to strings without additional sanitization (`web/src/lib/api/nodes-api.ts:121-134`). `NodeDoctorDialog` renders `check.evidence` and `check.suggestion` as-is in the dialog (`web/src/components/node-doctor-dialog.tsx:37-60`, `135-140`). `nodes-page.state.ts` stores the result in React state (`web/src/pages/nodes-page.state.ts:93-96`, `295-310`).

#### Node Doctor audit surfaces

- Doctor writes a credential audit event with action `node.doctor.run`, purpose `node_test`, safe credential labels, node ID, outcome, and metadata counts/stage only (`backend/internal/api/handlers/node_doctor_handler.go:176-206`). It does not copy check evidence into metadata.
- If `runNodeDoctor` returns a hard error, `event.ErrorMessage` is set via `credentialAuditSafeError(stage, err)` (`backend/internal/api/handlers/node_doctor_handler.go:86-90`, `203-205`). The shared credential audit writer then applies `sanitizeErrorMessage`, which runs `util.SanitizeMessage` and redacts text after `输出:`, `output:`, `stdout:`, or `stderr:` (`backend/internal/credentialaudit/audit.go:291-310`).
- Existing Doctor audit test asserts no diagnostic evidence, host, node name, private-key/password markers, or fake secret strings are copied into metadata/error for a blocked diagnostic (`backend/internal/api/handlers/node_doctor_handler_test.go:127-181`).

#### Migration preflight response and UI surfaces

- `PreflightCheckItem.Message` is free-form response text (`backend/internal/api/handlers/node_migrate_preflight_handler.go:29-34`). `PreflightNodeInfo` returns `name` and `host` for both source and target (`36-44`). `PreflightPolicy` returns policy `name`, `sourcePath`, and `executorType` (`46-52`). `MigratePreflightResponse` returns source/target nodes, policies, task count, checks, `canProceed`, `dataMigratable`, and `dataSizeMb` (`54-64`). These are current host/path/name response surfaces.
- The response is populated with raw source and target node names/hosts (`backend/internal/api/handlers/node_migrate_preflight_handler.go:193-201`) and policy source paths (`180-190`).
- SSH probe failure response includes raw `probeErr.Error()` in `Message` (`backend/internal/api/handlers/node_migrate_preflight_handler.go:212-220`). SSH session dial failure for tool checks includes raw `dialErr.Error()` (`241-254`). These are current raw SSH/probe/dial error response surfaces.
- Path checks use each policy `SourcePath` and return `目标节点路径不存在: %s` or `路径存在: %s` with the raw path (`backend/internal/api/handlers/node_migrate_preflight_handler.go:274-296`). This is a current include/source path response surface.
- Tool checks run `command -v <tool> >/dev/null 2>&1` and ignore output (`backend/internal/api/handlers/node_migrate_preflight_handler.go:259-271`). Path checks also ignore output (`285-296`), so command output is not directly returned on those paths.
- Local data migration detection checks each task `RsyncTarget` via `os.Stat` and `du -sm` but returns only count/estimated MB (`backend/internal/api/handlers/node_migrate_preflight_handler.go:341-374`, `473-491`), not the local path.
- Frontend `MigratePreflightResult` keeps `sourceNode.host`, `targetNode.host`, `policies[].sourcePath`, and `checks[].message` in API state/types (`web/src/lib/api/nodes-api.ts:275-310`). `NodeMigrateWizard` stores preflight in React state (`web/src/components/node-migrate-wizard.tsx:50-59`, `80-92`) and renders check messages (`211-220`), source/target names (`238-246`), policy names/executor types (`248-255`), and data size (`260-275`). It does not currently render preflight hosts or `policies[].sourcePath`, though they are present in state.
- Route registration uses authenticated routes with RBAC/ownership: Doctor is `POST /api/v1/nodes/:id/doctor` with `nodes:test` and `OwnershipNodeCheck` (`backend/internal/api/router.go:151-158`); migration preflight is `POST /api/v1/nodes/:id/migrate/preflight` with `nodes:write` and `OwnershipNodeCheck` (`backend/internal/api/router.go:368-369`). Swagger annotations in the preflight handler still document `/nodes/{id}/migrate-preflight` (`backend/internal/api/handlers/node_migrate_preflight_handler.go:66-80`), which differs from the registered frontend route.

#### Migration preflight audit surfaces

- Preflight writes action `node_migration.preflight`, purpose `node_migration`, credential labels, target node ID, outcome, and metadata counts/booleans/IDs (`backend/internal/api/handlers/node_migrate_preflight_handler.go:377-385`, `397-423`). It does not persist check messages.
- Audit outcome is `success` when no failed checks, `blocked` when the `ssh` check fails, otherwise `failure` (`backend/internal/api/handlers/node_migrate_preflight_handler.go:425-435`). Tests cover SSH failure as blocked even when the check message contains a fake secret (`backend/internal/api/handlers/node_handler_test.go:321-329`).
- Existing preflight audit test asserts metadata/error do not contain diagnostic host/path/evidence strings such as node hosts, node names, policy path, fake secrets, or the SSH failure text (`backend/internal/api/handlers/node_handler_test.go:259-319`).

#### Credential audit and settings summary surfaces

- `credentialaudit.Write` sanitizes/bounds actor/action/purpose/credential fields, JSON metadata, error message, client IP, and user agent before inserting `model.CredentialAuditEvent` (`backend/internal/credentialaudit/audit.go:144-176`).
- Metadata sanitizer drops keys containing `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, or `payload`, and drops string values containing those plus `bearer` or `authorization:` (`backend/internal/credentialaudit/audit.go:208-280`).
- Settings security risk summary treats `node.doctor.run` and `node_migration.preflight` as high-risk credential audit actions (`backend/internal/api/handlers/settings_handler.go:468-492`) and renders only action labels/counts (`495-514`), not raw metadata/error messages.

### External References

No external references were used; this is local-only code/spec research.

### Related Specs

- `.trellis/spec/backend/error-handling.md` — API errors must not expose raw SQL, SSH private keys, tokens, command output, SFTP/file content, Docker output, diagnostic evidence, exported config payloads, or stack-like details (`71-74`). The SSH Fleet Doctor contract requires read-only, server-side allowlisted diagnostics and sanitized concise evidence that does not return passwords, private keys, tokens, proxy endpoints, raw SQL/encryption details, or full command output (`78-133`).
- `.trellis/spec/backend/database-guidelines.md` — `credential_audit_events` must store only safe identifiers, sanitized errors, and sanitized metadata; it must not store raw credentials, decrypted executor config, terminal streams, command output, or file contents (`85-92`).
- `.trellis/spec/backend/logging-guidelines.md` — logs must not contain passwords, private keys, TOTP/JWT/recovery values, raw notification endpoints, decrypted values, full command output that may contain credentials, Docker output/volume names, node Doctor evidence, migration preflight command output, executor config, or credential audit metadata with raw remote evidence (`68-81`).
- `.trellis/spec/backend/quality-guidelines.md` — credential audit events must not store raw passwords, private keys, TOTP/JWT/recovery codes, decrypted executor config, terminal input/output, SFTP payloads, file contents, raw command output, Docker output/volume names, diagnostic evidence, exported config payloads, or full command text; metadata is limited to small sanitized counts/stages/IDs/path hashes/booleans and drops forbidden keys/values (`380-405`).
- `.trellis/spec/frontend/type-safety.md` — frontend Doctor mapper preserves backend `evidence`/`suggestion` as-is and components must not enrich them with connection secrets or raw node credentials (`74-123`).

## Implementation Boundaries

- Keep the slice local-only: sanitizer/helpers, response DTO shaping, audit/log sanitization, and tests around the existing Node Doctor and migration preflight handlers/API/UI surfaces.
- Preserve the existing routes, method shapes, auth/RBAC/ownership behavior, check status vocabulary, and migration/Doctor workflows unless a response field must be minimized to meet the sanitizer contract.
- Do not introduce external Vault/KMS/SSH CA/session recording/command approval, and do not expand remote command capability beyond current server-side allowlists.
- Treat API responses, credential audit rows, process logs, frontend React state, and rendered UI text as in-scope storage/display surfaces for diagnostic evidence and host-sensitive strings.
- Keep audit metadata count/ID/stage-oriented; do not persist check messages, raw errors, raw command text/output, hostnames, endpoint/proxy values, or raw paths.

## Caveats / Not Found

- No dedicated migration-preflight Trellis scenario spec was found; relevant constraints are spread across backend error-handling, database, logging, quality, and frontend Doctor mapping specs.
- Existing Doctor sanitization is marker/URL/pattern-based and truncating, not a blanket hostname/path/host-sensitive-string suppressor.
- Existing migration preflight audit is already count/ID-oriented, but the migration preflight API response still includes raw node hostnames, policy source paths, path check messages, and raw SSH/probe/dial error text.
- The frontend preflight wizard stores raw preflight response fields in component state; it renders check messages and node/policy names, but not currently hosts or policy source paths.
