# Research: Credential/executor/deployment residual closeout

- **Query**: Research credential resolver, executor, and deployment-adjacent residual security surfaces for the P4 closeout task.
- **Scope**: internal credential/executor/deployment audit
- **Date**: 2026-05-25

## Scope Reviewed

Reviewed current AppCredential/profile access, restic/rclone executor seams, task runtime evidence, snapshot/anomaly paths, Docker/nginx local logs, and deployment scripts. The review also cross-checked backend API/read-boundary and frontend browser-state findings from the companion closeout research files.

## Findings

### Existing hardening confirmed

1. AppCredential storage/API responses are already sanitized: encrypted JSON fields stay hidden from JSON responses, handlers use sanitized DTOs, and local provider seams expose safe metadata rather than password material.
2. Integration credential surfaces are hardened: endpoint/proxy URLs and secret-bearing delivery errors are masked/sanitized at current write paths.
3. Restic/rclone executor output paths are mostly hardened: restic repository access uses local resolver seams, runtime output is hidden/sanitized, and no secret-bearing rclone executor config fields were confirmed.
4. Task runtime/log read boundaries are already hardened: task logs, task-run `last_error`, restore-drill evidence, and WebSocket log backfill route through runtime-evidence sanitizers.
5. Snapshot/anomaly output failures are covered by prior P4 work: snapshot indexer hides restic output and anomaly snapshot JSON parse failures log only safe metadata.
6. Docker/nginx/deployment logs are mostly hardened: nginx access logs omit query strings and Docker command failures are collapsed to generic warnings.

### Candidate slices observed in companion audits

1. **SSH key batch import private-key state cleanup** — raw private-key strings are parsed into React state and can remain while the closed dialog remains mounted. High confidence and high compatibility because clearing state on close/success/reopen does not change API/schema/deployment behavior.
2. **Batch status response DTO** — batch GET currently returns full task rows even though the frontend only needs status-oriented fields. High confidence backend candidate, but it changes API response breadth for undocumented external consumers.
3. **Alert read-boundary sanitizer** — current alert/delivery responses expose stored message/last-error fields directly; this is high-confidence for legacy rows, but less directly tied to current write behavior than the SSH import private-key state residual.
4. **Node migration result message sanitization** — local backup paths can appear in migration result messages. High confidence but has more visible operator-detail behavior impact.
5. **Node probe/manual SSH test alert/log sanitization** — SSH/dial/known_hosts errors can include host/path evidence. High confidence but reduces operator diagnostic detail in alerts/logs.
6. **Persistent frontend keyword filters** — user-entered filter text can persist host/path/output fragments in `localStorage`; real but lower priority because it is user-entered state and cross-session filter persistence is existing UX.

## Exclusions

* AppCredential rendered hook redesign is a real residual area, but it changes product/API semantics around intentionally rendered hook fields and is not a closeout-sized compatible slice.
* Replacing remote `RESTIC_PASSWORD=...` command/environment materialization is broader executor/secret-delivery architecture.
* External Vault/KMS, SSH CA, session recording, command approval/inspection, WebAuthn/passkeys, device trust, and configurable policy UI remain architecture-level roadmap items.
* CI/deploy provider log output is outside this local-only P4 closeout slice.

## Recommendation

Select the SSH key batch import private-key state cleanup as the final P4 closeout implementation slice. It handles raw secret material retained in closed frontend component state, is smaller than the backend response DTO/read-boundary alternatives, and preserves API/deployment/schema/UI behavior. Record backend candidates as follow-up observations rather than expanding this closeout task into multiple unrelated fixes.
