# P4 file browser process residual security hardening

## Goal

Reduce remaining local file-browser and process-log residual evidence without changing authorized file browser behavior, node-log semantics, API schemas, or deployment behavior.

## Requirements

- Preserve authorized file browser responses: directory entries still include names/paths/metadata, and file preview still returns bounded content to authorized callers.
- Preserve file browser route protection, least-privilege SSH purpose (`file_browser`), SFTP path validation, 1 MiB preview cap, directory entry cap, and credential audit behavior.
- Harden frontend file preview state so file content is aborted and cleared on close/unmount, with no browser-storage persistence.
- Harden backend file-handler process logs so SFTP/path/read failures are logged with safe structured fields instead of raw error values that may contain host/path/output evidence.
- Keep API response messages and credential audit rows unchanged unless a current leak is proven.
- Do not change node-log storage/query/response semantics; existing write/read sanitizers already cover node-log evidence.

## Acceptance Criteria

- [ ] `FilePreviewDialog` accepts and propagates an `AbortSignal` to the file-content fetch path.
- [ ] Closing or unmounting the file preview aborts any in-flight content request and clears `content`, `size`, `truncated`, and `error` state.
- [ ] Late file-content promises after close/unmount cannot repopulate preview state.
- [ ] File browser directory loading behavior remains unchanged.
- [ ] File handler process logs for SFTP dial, path validation, directory read, file open, file read, and local backup path/directory errors avoid raw `Err(err)` and include safe fields such as `node_id`, `stage`, and `path_hash` where applicable.
- [ ] File browser API responses and credential audit metadata/error behavior remain compatible and covered by existing tests.
- [ ] Node-log and AppCredential/policy hook behavior are not changed.
- [ ] Focused backend and frontend tests pass, plus full relevant gates before PR.

## Definition of Done

- Trellis research, PRD, implement/check context, implementation, and check artifacts are complete.
- Required local verification commands pass with recorded output.
- Changes are committed on the work branch.
- Trellis task is archived and session journal recorded after the work commit.
- PR is created, CI is green, PR is merged, release automation completes, Docker publishing is verified if triggered, and local `main` is synced cleanly.

## Technical Approach

- Change `FilePreviewDialog` fetch prop to accept an optional `AbortSignal`, create an `AbortController` per open/fetch cycle, and abort/clear state from the effect cleanup and close path.
- Thread the signal through `FileBrowser` to the existing typed file API, which already supports `AbortSignal`.
- Add/update a component test for close/unmount cleanup and late fetch suppression.
- Replace file-handler `logger.Log.*().Err(err)` calls in file browser/local backup file surfaces with safe structured logs that record stage, `node_id`, and `path_hash` for requested paths. Keep response and audit error handling unchanged.

## Decision (ADR-lite)

**Context**: File browser and node-log write/read boundaries are already mostly sanitized, but file-preview content can remain in component memory after close and file handler process logs still record raw error values in several SFTP/path failure paths.

**Decision**: Implement a narrow behavior-compatible hardening slice: frontend preview abort/clear plus backend file-handler safe process logging. Do not redact authorized file browser responses or redesign node-log storage/query behavior.

**Consequences**: Local residual file content state and process-log evidence risk are reduced without changing product behavior. File paths/content still appear where they are the authorized feature output.

## Out of Scope

- External Vault/KMS/SSH CA/session recording/command approval/WebAuthn/passkeys/device trust.
- Broad AppCredential rendered-hook storage/API redesign.
- Redacting authorized file browser list/content responses.
- Changing node-log parser/storage/query/response behavior.
- Config export/import redesign.
- Rclone/restic/task executor residual changes.
- Deployment, Docker, or CI workflow changes.

## Research References

- [`research/file-process-residual.md`](research/file-process-residual.md) — Internal review recommending file preview state cleanup and file-handler process-log hardening as the smallest compatible slice.

## Technical Notes

- Backend error-handling spec forbids exposing SFTP/file content, diagnostic evidence, raw command output, and stack-like details to clients.
- Backend logging spec forbids SFTP file contents and risky raw remote evidence in process logs.
- Frontend state-management spec prefers local transient dialog state and forbids sensitive prompt-like state in browser storage.
- Frontend quality spec requires typed API wrappers and tests for async stale/refresh behavior.
