# Research: file browser, include/target paths, config import/export residual P4 review

- **Query**: Research residual P4 risk surface: file browser/preview/content, include-path and target-path handling, plus config import/export boundaries in `/Users/weibo/Code/xirang`. Determine whether raw file contents, remote paths, hostnames, endpoints, exported/imported secret material, raw SQL, or sensitive strings are stored/returned/logged/audited/UI-persisted in a way not already intentionally part of feature behavior. Constraints: local-only hardening, no API/UI/deployment behavior changes unless necessary. Include files inspected, current protections, product-behavior boundaries, minimal compatible slice candidates, and recommendation.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/router.go` | Route-level auth/RBAC/ownership/step-up/grant gates for file browser, snapshots, restore, config import/export. |
| `backend/internal/api/handlers/file_handler.go` | Node file browser/list/content and local backup file browsing implementation. |
| `backend/internal/api/handlers/file_handler_validate_test.go` | Tests for file path validation, symlink escape prevention, dev-only bypass, and file audit metadata redaction. |
| `backend/internal/api/handlers/snapshot_handler.go` | Restic snapshot listing, file listing, restore include/target handling, restore audit metadata. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | Snapshot diff path handling and snapshot ID validation. |
| `backend/internal/api/handlers/snapshot_search_handler.go` | Snapshot search query handling and parameterized path search. |
| `backend/internal/snapshot/indexer.go` | Snapshot file index population; stores raw snapshot paths for search. |
| `backend/internal/task/executor/restic_executor.go` | Restic command construction, snapshot/file/restore executor behavior, output sanitization on most error paths. |
| `backend/internal/task/executor/executor.go` | Rsync command construction, shell escaping, runtime output sanitization calls. |
| `backend/internal/task/executor/runtime_sanitize.go` | Shared executor runtime evidence/output sanitizers for paths, hosts, URLs, and output text. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import boundary, secret inclusion rules, import size limit, audit metadata. |
| `backend/internal/api/handlers/config_handler_test.go` | Tests for default export omission, sensitive export gating, import envelope handling, audit safety. |
| `backend/internal/settings/service.go` | System setting registry and validation used during import/export. |
| `backend/internal/model/models.go` | Sensitive model hooks and response sanitization for Node, SSHKey, Task executor config, Integration. |
| `backend/internal/credentialaudit/audit.go` | Credential audit metadata/error safety filters. |
| `backend/internal/middleware/audit.go` | HTTP audit log behavior: skips GET and stores route templates instead of query/body. |
| `backend/internal/api/handlers/audit_handler.go` | Audit log filtering with escaped parameterized LIKE queries. |
| `backend/internal/api/handlers/credential_audit_handler.go` | Credential audit response/export sanitization. |
| `backend/internal/api/handlers/response.go` | Generic internal-error logging path; logs raw `Err(err)` before returning generic response. |
| `backend/internal/api/handlers/helpers.go` | Path hashing, safe credential audit error helpers, and path character/prefix validators. |
| `backend/internal/api/handlers/task_handler.go` | Task path validation and raw SQL review target for task list/progress. |
| `web/src/lib/api/files-api.ts` | Frontend file browser API calls; passes paths through query params. |
| `web/src/components/file-browser.tsx` | File browser UI state and navigation display. |
| `web/src/components/file-preview-dialog.tsx` | File content preview UI; holds content in component state and renders it. |
| `web/src/components/config-export-import.tsx` | Config export/import UI; Blob download, in-memory file parsing, non-persistent step-up. |
| `web/src/components/snapshot-browser.tsx` | Snapshot list/file/restore UI; component-local selection/target/grant state. |
| `web/src/components/snapshot-search.tsx` | Snapshot search UI; component-local query/results state. |
| `web/src/components/snapshot-diff-viewer.tsx` | Snapshot diff UI; displays raw changed paths as feature behavior. |
| `web/src/pages/nodes-page.dialogs.tsx` | Embeds node file browser and chooses initial root path. |
| `web/src/pages/tasks-page.dialogs.tsx` | Embeds snapshot browser, diff viewer, and search components. |
| `web/src/pages/settings-page.maintenance.tsx` | Embeds config export/import controls. |
| `.trellis/spec/backend/logging-guidelines.md` | Related backend spec forbidding raw command output, file contents, Docker output, diagnostics, endpoints, executor config, and similar sensitive evidence in logs. |
| `.trellis/spec/backend/quality-guidelines.md` | Related backend spec requiring response helpers, secret stripping, sanitizer use, input validation, and denial tests. |
| `.trellis/spec/frontend/state-management.md` | Related frontend spec forbidding local/session persistence for grants and sensitive one-shot materials. |

### Code Patterns

#### File browser / preview / content

- `backend/internal/api/router.go` protects node file browser routes with `nodes:read` RBAC and node ownership checks. The task backup-file browsing route is admin-only. This keeps file browsing behind existing authorization gates.
- `backend/internal/api/handlers/file_handler.go` intentionally returns raw paths and file contents for the feature surface:
  - `FileEntry.Path` is returned for navigation.
  - `FileContentResponse.Content` returns raw preview content.
  - Preview content is capped by `filePreviewMaxBytes = 1 * 1024 * 1024` and reports `Truncated` when the source exceeds the preview limit.
- Remote node path validation in `validateNodePath` uses SFTP `RealPath` and allows only paths under node `BasePath` or existing task `RsyncSource` roots, except for `FILE_BROWSER_ALLOW_ALL=true` in development only. This handles symlink resolution on remote nodes before comparing roots.
- Local backup path validation in `validateLocalPath` resolves symlinks with `filepath.EvalSymlinks` and requires the result to remain under `task.RsyncTarget`, preventing local symlink escapes.
- File credential audit metadata uses path hashes, counts, stages, and booleans instead of raw path/content/output. `file_handler_validate_test.go` has explicit assertions that audit metadata does not contain raw test path/content/SFTP output.
- Product-behavior boundary: raw file contents are returned to the authenticated user in `GetNodeFileContent` and rendered by `web/src/components/file-preview-dialog.tsx`; this is the file preview feature itself, not an unintended secondary exposure. The frontend keeps preview content in React component state and does not persist it to `localStorage` or `sessionStorage`.
- Residual log surface: file handler error paths log raw `Err(err)` for SFTP dial/read/open/stat/read and local file/directory failures. Those error values can include remote host/path or local filesystem path details. The API response stays generic/safe, but process logs can still receive raw evidence.

#### Include-path and target-path handling

- `backend/internal/api/router.go` protects snapshot restore with admin role, step-up, and snapshot restore credential grant. Snapshot list/file/diff/search routes use task read RBAC and ownership checks.
- `backend/internal/api/handlers/snapshot_handler.go` validates snapshot IDs and cleans list paths. It intentionally returns restic snapshot/file paths for browsing.
- Restore target validation in `validateRestoreTargetPath` requires an absolute path, rejects `/`, and blocks dangerous system prefixes such as `/bin`, `/sbin`, `/usr`, `/lib`, `/boot`, `/dev`, `/proc`, `/sys`, `/run`, `/etc`, and `/var/run`.
- Restore audit metadata records safe facts only: stage, include count, target-set boolean, and shortened snapshot ID. It does not store raw include paths or target path.
- `backend/internal/task/executor/restic_executor.go` shell-escapes snapshot IDs, repository paths, target paths, and include paths when constructing restic commands. Restic command output is hidden on most error paths with `sanitizeExecutorRuntimeOutput(output)`.
- `backend/internal/api/handlers/snapshot_diff_handler.go` validates snapshot IDs with a hex pattern and returns raw diff paths as the diff feature behavior.
- `backend/internal/api/handlers/snapshot_search_handler.go` uses parameterized SQL with escaped LIKE patterns for path search; no raw SQL concatenation of the search string was found in the scoped review.
- `backend/internal/snapshot/indexer.go` stores raw `SnapshotFileIndex.Path` values intentionally so cross-snapshot file search can work. This is at-rest product data, not a hidden audit/log surface.
- Residual error surface: `snapshot/indexer.go` constructs an error containing raw `strings.TrimSpace(output)` from `restic find` on failure. Current async indexing ignores that error, but if surfaced or logged by a caller later it can expose restic output/path evidence.

#### Config import/export boundaries

- `backend/internal/api/router.go` makes config export/import admin-only. Sensitive export (`include_secrets=true`) requires step-up plus config export credential grant; import requires step-up plus config import credential grant.
- `backend/internal/api/handlers/config_handler.go` default export omits high-risk secret fields while preserving operational config needed for a usable export:
  - Nodes include host/port/username/auth type/tags/base path/SSH key ID metadata but omit password/private key by default.
  - SSH keys include metadata but omit private keys by default.
  - Tasks include command/source/target/node/policy names; executor config is omitted by default.
  - System settings are exported only if non-sensitive by key/value heuristics.
- Sensitive export intentionally includes secret material only when `include_secrets=true` and the admin has the required step-up/grant. This includes node password/private key, SSH key private key, task executor config, and sensitive system settings.
- Import accepts wrapped export envelopes or direct data, limits the request body to 10 MB, and imports inside transactions. Imported model secrets are encrypted by existing hooks where applicable: `SSHKey.PrivateKey`, `Node.Password`, `Node.PrivateKey`, and `Task.ExecutorConfig`.
- Config export/import audit metadata stores counts, stages, and `with_sensitive` state, not exported payloads, imported payloads, grant reasons, endpoints, or secret values. Existing tests verify default secret omission and safe audit metadata.
- `SystemSetting.Value` is not encrypted by a model hook; this is existing settings model behavior. Export filtering reduces accidental default export of sensitive settings, while sensitive export/import intentionally carries them under admin + step-up + grant.
- `web/src/components/config-export-import.tsx` downloads exports through a Blob URL and parses imports from file text in memory. It uses non-persistent step-up proof (`persist: false`, `reuseCached: false`) and component-local grant reason state. No local/session storage of exported/imported secret material was found.

#### Raw SQL and API/audit logging

- Scoped raw SQL review found parameterized or static SQL in the relevant handlers:
  - `snapshot_search_handler.go` uses `Where("task_id = ? AND path LIKE ? ESCAPE '\\'", taskID, escapedPattern)`.
  - `audit_handler.go` escapes `%` and `_` before parameterized `LIKE` search.
  - `task_handler.go` uses a static `Raw` query with bound `taskIDs` for running progress lookup.
- `backend/internal/middleware/audit.go` skips GET/HEAD/OPTIONS requests and, for audited mutating routes, stores the route template instead of query string or request body. This avoids HTTP audit capture of file preview paths/content, config import payloads, and restore include/target bodies.
- `backend/internal/credentialaudit/audit.go` blocks risky metadata keys/values containing markers like private/password/token/secret/credential/config/output/stream/command/content/payload and redacts output markers in errors. `credential_audit_handler.go` sanitizes metadata/errors again before response/export.
- `backend/internal/api/handlers/response.go` logs raw `Err(err)` in `respondInternalError`. This is a broad residual process-log surface when callers pass SSH/restic/SFTP/filesystem errors containing hostnames, endpoints, paths, or command output fragments.

### Current Protections

- Route protections are present for the inspected surfaces: RBAC, ownership checks, admin-only routes, step-up proofs, and credential grants are applied where expected.
- File browser audit stores hashed paths and safe metadata instead of raw paths/content/output.
- File browser path validation resolves remote and local symlinks before root comparison.
- Snapshot restore audit stores safe counts/flags rather than include/target path text.
- Config default export omits private keys, passwords, task executor config, and sensitive system settings.
- Sensitive config export/import is intentionally gated by admin + step-up + grant.
- Model hooks encrypt most imported secret-bearing fields.
- HTTP audit avoids request bodies and GET query capture.
- Frontend components reviewed keep preview contents, import payloads, restore selections, target paths, grant reasons, and search/diff state in component memory rather than browser storage.
- Scoped SQL patterns use parameterized queries or static raw SQL with bound variables.

### Product-Behavior Boundaries

These raw values are intentionally stored/returned/displayed as part of existing features:

- File preview returns raw file content in `FileContentResponse.Content` and displays it in the preview dialog.
- File browser returns raw navigable file paths in `FileEntry.Path`.
- Snapshot list returns restic snapshot `Hostname` and `Paths`.
- Snapshot file list, diff, and search return raw file paths.
- Snapshot file index stores raw snapshot file paths to support search.
- Snapshot restore sends user-selected include paths and target path to restic after required gates.
- Config export returns operational host/path/task command metadata by default; sensitive secret material is returned only by explicit sensitive export with admin + step-up + grant.
- Config import accepts secret material by design so an exported configuration can be restored.

### Minimal Compatible Slice Candidates

1. **File browser process-log sanitization**
   - Evidence: `file_handler.go` returns generic API messages and safe credential audit metadata, but logs raw `Err(err)` on SFTP/local filesystem failures.
   - Risk class: unintended process-log exposure of remote hostnames, endpoints, remote paths, local paths, or sensitive filenames.
   - Compatibility: API shapes, UI behavior, auth gates, ownership checks, and operation semantics can remain unchanged. Only process log details need to be sanitized or replaced with stage/resource IDs/path hashes.
   - Security value: high for the reviewed file/content surface because it closes a secondary evidence path without changing the file preview feature.

2. **Snapshot indexer restic-output error sanitization**
   - Evidence: `snapshot/indexer.go` returns `fmt.Errorf("restic find 执行失败: %w, 输出: %s", err, strings.TrimSpace(output))` on restic find failure.
   - Risk class: future caller/log path can expose restic output containing raw snapshot paths or host-sensitive strings.
   - Compatibility: remove or replace output with an output-hidden marker while preserving failure semantics.
   - Security value: moderate; current async indexing ignores this error, so exposure appears latent rather than currently user-visible.

3. **Generic internal-error log sanitization for SSH/restic/SFTP callers**
   - Evidence: `respondInternalError` logs raw `Err(err)` globally.
   - Risk class: broad process-log exposure when lower-level errors include host/path/output details.
   - Compatibility: response shape is already generic, but changing global logging affects many handlers and may reduce operational diagnostics broadly.
   - Security value: broad, but larger blast radius than a single residual slice.

4. **Snapshot restore include validation beyond shell escaping**
   - Evidence: includes are shell-escaped and not audited/logged raw, but there is no additional include path policy beyond restic semantics.
   - Risk class: misuse or surprising restore include patterns, not an observed storage/return/log/audit leak.
   - Compatibility: stricter validation can break existing restore selections, so this is not the smallest behavior-compatible slice.

5. **Encrypt `SystemSetting.Value` for sensitive settings**
   - Evidence: imported sensitive system settings are stored as normal settings values; export filtering prevents default leak, and sensitive export/import is gated.
   - Risk class: at-rest secret exposure for settings values already known to be plaintext by model design.
   - Compatibility: would require broader storage/migration semantics and likely exceeds this local-only residual slice.

### Recommendation

Select **file browser process-log sanitization** as the smallest behavior-compatible hardening slice from this review.

Rationale:

- It directly addresses a reviewed residual surface involving file browser/preview/content without changing intended file preview/list behavior.
- It preserves response shapes, UI flows, route gates, ownership checks, step-up/grant behavior, deployment shape, and operation semantics.
- Existing file-browser API responses and credential audits are already safe; process logs are the remaining unintended evidence path for that surface.
- The implementation can be limited to replacing raw logged errors in `file_handler.go` with sanitized stage/context fields such as node/task IDs and path hashes, plus tests that prove logs/audits/responses do not include raw path/content/output evidence.

If the selected slice needs to cover one additional line with minimal cost, the next most compatible companion is sanitizing/removing the raw `restic find` output from `snapshot/indexer.go` errors. However, if strictly selecting one executable slice, prefer the file-browser process-log slice because it is an active reviewed surface rather than a latent future exposure path.

### External References

- None. This was an internal codebase review; no external library behavior or API reference was needed.

### Related Specs

- `.trellis/spec/backend/logging-guidelines.md` — explicitly forbids persisting/logging raw file contents, command output, Docker output, diagnostic output, endpoints, executor config, and other sensitive evidence.
- `.trellis/spec/backend/quality-guidelines.md` — requires response helpers, secret stripping from responses, shared sanitizers for user-visible evidence, input validation, and denial tests for security-sensitive code.
- `.trellis/spec/frontend/state-management.md` — requires one-shot operation state to stay non-persistent and forbids local/session persistence for grant material and sensitive session data.

## Caveats / Not Found

- No frontend local/session persistence of file preview content, import/export payloads, grant reasons, restore targets, include selections, snapshot paths, or search/diff results was found in the inspected components.
- No scoped raw SQL injection pattern was found in the inspected file/snapshot/config/audit/task query paths; observed dynamic inputs use parameterized queries and escaping.
- Raw file contents, paths, hostnames, snapshot paths, and config payload values do exist in API responses or storage where they are core product behavior; the recommendation intentionally avoids removing or masking those behavior surfaces.
- The generic `respondInternalError` logging path is broader than this topic and may still log raw lower-level error details outside `file_handler.go`; treating it globally should be a separate, larger hardening slice unless explicitly selected.
