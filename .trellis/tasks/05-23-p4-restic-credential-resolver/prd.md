# P4 restic credential resolver seam

## Goal

Extend the P4 local-only credential provider pattern from SSH credentials to restic repository password materialization, so every current restic `repository_password` consumer resolves through one backend-internal seam while preserving existing behavior and avoiding secret exposure.

## Requirements

* Add a backend-internal local resolver seam for restic repository passwords with provider identity fixed to `local`.
* Keep existing encrypted `Task.ExecutorConfig.repository_password` as the only source of truth for this slice.
* Cover all current restic password consumers discovered in research: executor backup/restore/snapshot paths, retention, integrity check, verifier, snapshot indexer, snapshot diff handler, and anomaly snapshot diff.
* Preserve current command-prefix behavior:
  * empty password emits `RESTIC_PASSWORD=''`;
  * non-empty password remains shell-escaped;
  * `sudo env` wrapping remains unchanged;
  * restic binary selection remains unchanged.
* Keep all operation-layer gates additive: auth/RBAC/ownership/step-up/grants where they already apply, SSH key scope checks, host key resolution, and runtime credential audit must not be bypassed.
* Add only safe resolver metadata: provider/kind/source labels are allowed; raw repository password, decrypted executor config, commands, output, paths, endpoints, hostnames, and imported/exported payloads are forbidden.
* Keep the change backend-only and avoid migrations, env vars, deployment changes, public API changes, frontend changes, external providers, or config import/export semantic changes.

## Acceptance Criteria

* [ ] A shared restic repository password resolver exists and returns the resolved password plus safe metadata with `provider=local`.
* [ ] Restic executor backup, task restore, snapshot list, snapshot file list, and snapshot file restore use the shared resolver/env-prefix path.
* [ ] Retention, periodic integrity check, post-backup verifier, snapshot indexer, snapshot diff API, and anomaly snapshot diff no longer parse `repository_password` with local duplicate helpers.
* [ ] Existing command prefixes remain behaviorally equivalent for empty password, non-empty password, shell escaping, `sudo env`, and restic binary selection.
* [ ] Invalid restic executor config errors do not include the raw config or password.
* [ ] Tests prove safe metadata and that command/env helpers do not expose raw password through metadata or errors.
* [ ] Targeted backend tests and full backend tests pass before commit.

## Definition of Done

* Backend implementation is minimal and focused on the resolver seam.
* Tests are added/updated for resolver metadata, env prefix equivalence, invalid JSON safety, and adopted duplicate paths.
* `go test ./... -count=1` passes from `backend/`.
* Trellis task is started, archived after commit, journal recorded, PR created/merged, CI green, release/Docker publish completed.

## Technical Approach

Add the seam in `backend/internal/task/executor` because all identified restic password consumers already import `executor` or can safely do so without a cycle. The seam will parse `Task.ExecutorConfig` into the existing `ResticConfig`, return a small safe metadata struct, and expose helpers for `RESTIC_PASSWORD` env prefix and restic command prefix construction. Existing package-local duplicate `extractResticPassword`, index/diff config parsers, and env-prefix builders will be removed or replaced at call sites.

## Decision (ADR-lite)

**Context**: P4-1 and P4-2 established a conservative local-only provider seam for SSH credentials. Restic repository passwords remain duplicated across executor, retention, integrity, verifier, snapshot indexer, snapshot diff, and anomaly paths.

**Decision**: Implement a backend-only `local` restic repository password resolver seam using existing encrypted `Task.ExecutorConfig.repository_password`; cover all current runtime consumers in this slice.

**Consequences**: This centralizes local materialization and creates a future external-provider insertion point, but deliberately does not remove the existing remote `RESTIC_PASSWORD=...` process/environment exposure or introduce provider references.

## Out of Scope

* Vault, KMS, Boundary, Teleport, SSH CA, dynamic secrets, provider health/lease/fallback/registry semantics.
* DB migrations, provider tables, provider reference columns, new env vars, deployment changes, public API/frontend changes.
* Replacing `RESTIC_PASSWORD` remote environment-prefix execution with password files/stdin/agents.
* Config import/export provider-reference semantics.
* AppCredential/profile-hook provider seam.
* Terminal/session recording, command approval, WebAuthn/passkeys/device trust.

## Research References

* [`research/restic-password-flow.md`](research/restic-password-flow.md) — maps all current `repository_password` consumers and must-cover/deferred paths.
* [`research/p4-provider-precedent.md`](research/p4-provider-precedent.md) — captures P4-1/P4-2 local-only provider constraints and reusable acceptance criteria.

## Technical Notes

* `Task.ExecutorConfig` is encrypted/decrypted by model hooks; do not manually encrypt/decrypt in handlers.
* Normal API responses omit `executor_config`; config export with explicit secrets remains unchanged.
* Credential audit/log metadata must avoid denied keys/values such as `password`, `credential`, `config`, `command`, `output`, `content`, and `payload`.
* Existing restic command output/log paths may contain path/output data; this slice only centralizes password materialization and must not broaden exposure.
