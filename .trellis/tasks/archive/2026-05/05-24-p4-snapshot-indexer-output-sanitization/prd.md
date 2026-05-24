# P4 snapshot indexer output sanitization

## Goal

Harden the next P4 residual security surface by preventing snapshot indexer `restic find` failures from carrying raw remote command output into returned errors, server logs, future API responses, or other evidence sinks. Keep the slice local-only, behavior-compatible, and limited to the snapshot indexer error path identified by research.

## Requirements

- Sanitize/hide raw `restic find` command output before it is included in snapshot indexer errors.
- Preserve existing snapshot search/index behavior: async first-search indexing, fixed indexing status response, snapshot indexing semantics, search result fields, and API/deployment/UI behavior.
- Keep parsed successful `restic find --json --long` output behavior unchanged; only failure evidence is affected.
- Do not mutate stored `snapshot_file_indices` rows or change snapshot path/search result contracts.
- Do not introduce external secret managers, SSH CA, session recording, command approval, or broader AppCredential/file-browser/Docker/Nginx redesign.
- Do not expose raw private keys, passwords, bearer tokens, executor config, command text/output, endpoints, hostnames, raw paths, include/target paths, diagnostic output, Docker output, raw SQL, or host-sensitive strings in errors/logs/responses/storage.

## Acceptance Criteria

- [ ] `backend/internal/snapshot/indexer.go` no longer embeds trimmed raw `restic find` output in returned errors.
- [ ] Non-empty failed command output is represented by a stable placeholder such as `[输出已隐藏]`.
- [ ] Empty failed command output does not add misleading raw-output text.
- [ ] Tests prove command output containing fake tokens, hosts, and raw paths is absent from the snapshot indexer error string.
- [ ] Tests prove existing `parseResticFindOutput` and `EscapeLikePattern` behavior remain intact.
- [ ] Backend test/build/lint gates relevant to this backend-only change pass.

## Definition of Done

- Trellis task has research artifacts and curated implement/check context.
- Implementation is reviewed by `trellis-check` and any findings are fixed.
- Backend tests and build/lint checks pass with actual command output.
- Spec is updated if this task adds or clarifies a backend error/logging convention.
- Work is committed on a non-main branch, PR is created, CI is green, PR is merged, release automation completes, Docker publish is verified if triggered, and local `main` is synced.

## Technical Approach

Use the established restic executor precedent: hide non-empty command output rather than attempting partial redaction of arbitrary remote output. Keep the helper package-local unless a check shows an existing exported shared helper is more appropriate. The current smallest target is `indexSnapshot` in `backend/internal/snapshot/indexer.go`, where `executor.RunSSHCommandOutput` returns raw combined stdout/stderr and the failure branch currently formats `strings.TrimSpace(output)` into the error.

## Decision (ADR-lite)

**Context**: Prior P4 tasks sanitized runtime evidence at task-log HTTP and WebSocket read boundaries. Research found one remaining local residual: snapshot indexing builds `restic find` output with `2>&1` and includes raw output in a returned error.

**Decision**: Limit this slice to hiding failed `restic find` output in snapshot indexer errors, preserving all successful indexing and snapshot search behavior.

**Consequences**: Operators lose raw remote output in this error string, but this matches the project security boundary and existing restic executor behavior. More invasive surfaces—AppCredential rendered hooks, file browser/process logs, Docker/Nginx query logging—remain out of scope for separate future slices.

## Out of Scope

- AppCredential profile rendering or policy `pre_hook`/`post_hook` storage/response redesign.
- File browser content/path behavior and process-log redesign.
- Snapshot search result path redaction or `snapshot_file_indices` schema changes.
- Docker/Nginx query logging hardening.
- External Vault/KMS, SSH CA, WebAuthn/passkeys, device trust, session recording, or command approval.

## Research References

- [`research/snapshot-indexer-restic-output.md`](research/snapshot-indexer-restic-output.md) — identified `indexer.go` failure branch as the specific raw-output residual and compared restic executor sanitization precedent.
- [`research/snapshot-adjacent-surfaces.md`](research/snapshot-adjacent-surfaces.md) — confirmed search/status API responses do not expose raw output and adjacent surfaces should stay out of this slice.

## Technical Notes

- Key code target: `backend/internal/snapshot/indexer.go` `indexSnapshot` failure branch after `executor.RunSSHCommandOutput`.
- Relevant specs: backend error handling, logging, quality, and database guidelines.
- The snapshot search route already returns a fixed indexing message for async builds and generic internal errors via handler helpers; this task prevents raw output from entering the error object/log path in the first place.
