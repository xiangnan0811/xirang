# P4 AppCredential hook residual hardening

## Goal

Continue the P4 residual security hardening program by selecting the next smallest local-only, behavior-compatible slice from the remaining AppCredential/rendered-hook risk area and delivering it end-to-end.

## What I already know

* The prior P4 residual review completed task/task-run HTTP read-boundary sanitization for legacy runtime evidence.
* AppCredential configs are encrypted at rest and sanitized in AppCredential API responses.
* Built-in AppCredential profiles intentionally render decrypted config values into policy hook command strings because those commands must execute on target nodes.
* Current tests and docs treat visible rendered policy hooks as existing product behavior, so redacting or removing `Policy.PreHook` / `Policy.PostHook` from policy APIs would be behavior-changing.
* Task runtime logs are sanitized on write, HTTP task-log reads are sanitized again, and live WebSocket log events originate from sanitized writes.
* WebSocket task-log backfill can replay stored legacy `TaskLog.Message` rows directly without the HTTP read-boundary sanitizer.

## Requirements

* Implement exactly one local-only hardening slice in this task.
* Preserve existing API schemas, WebSocket event schemas, deployment behavior, and normal UI behavior.
* Do not change AppCredential profile rendering semantics, policy hook storage format, policy hook response fields, or hook execution behavior.
* Sanitize WebSocket task-log backfill messages at read boundary using the existing task runtime evidence sanitizer.
* Ensure legacy/raw `TaskLog.Message` rows replayed through WebSocket backfill cannot expose command text/output, endpoint/proxy values, hostnames, raw paths, remote paths, tokens, passwords, private values, or host-sensitive strings.
* Preserve WebSocket backfill event IDs, task IDs, levels, statuses, timestamps, ordering, and filtering semantics.
* Do not mutate stored legacy task-log rows while sanitizing backfill responses.
* Keep file browser/process logs, snapshot indexer/restic find raw-output handling, and Docker/Nginx query logging out of this slice.

## Acceptance Criteria

* [ ] WebSocket log backfill applies response-time sanitization to `TaskLog.Message` before constructing `LogEvent` values.
* [ ] A regression test inserts a raw legacy `TaskLog.Message`, reads it through the WebSocket backfill path, and proves sensitive fragments are absent from the emitted event.
* [ ] The regression test proves the stored DB row remains unchanged.
* [ ] The regression test proves ordering/filtering metadata such as `log_id`, `task_id`, level/status, and timestamp semantics remain compatible.
* [ ] Focused backend tests covering the WebSocket backfill sanitizer pass.
* [ ] Full backend tests pass.
* [ ] Backend build/lint and relevant repository checks pass.

## Definition of Done

* Trellis task is planned, activated, implemented, checked, archived, and journaled.
* Code is committed on a non-main branch.
* PR is created, CI is green, and the PR is merged.
* Release automation is completed, release/Docker publish are verified when triggered, and local `main` is synchronized.

## Technical Approach

Use the existing `task.SanitizeRuntimeEvidenceForRead` helper in the WebSocket backfill loader before assigning `TaskLog.Message` to `LogEvent.Message`. Add focused backend regression coverage around the backfill path rather than changing policy hook rendering or storage.

## Decision (ADR-lite)

**Context**: AppCredential rendered hooks can contain decrypted credential values in durable policy hook fields, but existing tests/docs/UI rely on visible rendered hooks for authorized users. Changing that contract would be broader than a minimal behavior-compatible P4 slice.

**Decision**: Defer policy hook storage/response contract changes and harden the confirmed legacy-read gap in WebSocket task-log backfill for this slice.

**Consequences**: The task closes a real response/read-boundary leak without altering user-facing policy behavior. The larger rendered-hook persistence model remains a documented future product/security design decision, not a local-only compatibility patch.

## Out of Scope

* External Vault/KMS, SSH CA, terminal/session recording, command approval, WebAuthn/passkeys, and device trust.
* Redacting or removing `Policy.PreHook` / `Policy.PostHook` from policy or task APIs.
* Changing AppCredential profile templates, hook execution, or policy hook persistence semantics.
* Backfilling or mutating existing database rows.
* File browser/process log hardening, snapshot indexer/restic find error-output hardening, and Docker/Nginx query-string logging changes.

## Research References

* [`research/appcredential-rendered-hooks.md`](research/appcredential-rendered-hooks.md) — AppCredential APIs are sanitized, policy hooks are intentionally visible, and WS backfill is the smallest compatible residual read surface.
* [`research/residual-alternatives.md`](research/residual-alternatives.md) — file/process logs, snapshot indexer output, and Docker/Nginx query logging are adjacent but separate slices.

## Technical Notes

* Relevant code paths include `backend/internal/ws/hub.go`, `backend/internal/task/runtime_sanitize.go`, HTTP task/task-run log handlers, and task log writer sanitization.
* Security constraint: no raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text, file contents, Docker output, diagnostic output, exported/imported secret material, raw SQL, endpoint/proxy values, hostnames, include-path contents, target-path contents, or host-sensitive strings in responses/logs/audit/docs/UI storage.
