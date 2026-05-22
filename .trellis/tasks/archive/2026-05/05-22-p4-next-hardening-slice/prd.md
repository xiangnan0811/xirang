# P4 executor SSH local-provider adoption

## Goal

Route executor-side SSH credential resolution through the local credential provider seam introduced in P4-1, eliminating the remaining duplicated task/runtime credential-resolution path while preserving current encrypted local storage, fail-closed behavior, SSH key scope checks, and credential audit metadata safety.

## Requirements

- Adopt the `sshutil` local credential provider seam for executor SSH auth construction and rsync remote-backup key materialization.
- Preserve existing local encrypted database / model storage as the only provider source; do not add external Vault/KMS/CA providers, migrations, deployment config, environment variables, or public API fields.
- Preserve executor fail-closed semantics when a node references a managed SSH key but the `Node.SSHKey` relation is unavailable in executor context; do not fall back to stale inline `Node.PrivateKey` in that case.
- Preserve purpose-aware managed-key scope checks for task-related SSH purposes (`task_command`, `batch_command`, `task_backup`, `task_restore`, and adjacent executor purposes already used by callers).
- Preserve rsync remote-backup behavior: password auth remains unsupported for remote rsync, key materialization still uses a `0600` temporary key file, cleanup remains best-effort, and no raw key content is logged or persisted.
- Ensure runtime credential audit evidence remains safe: provider/source/kind/key ID/status/stage metadata only; no private keys, passwords, command text/output, terminal streams, paths containing sensitive content, endpoints, hostnames, or decrypted executor config.
- Keep changes backend-only and minimal; no frontend/UI/docs/deployment behavior changes.

## Acceptance Criteria

- [ ] Executor SSH auth resolution delegates to provider-backed `sshutil` APIs or a provider-compatible wrapper instead of duplicating key/password auth construction.
- [ ] Rsync remote-backup key resolution uses provider-backed local resolution while preserving missing-managed-key fail-closed behavior and stale inline-key rejection.
- [ ] Resolved executor credential metadata includes the local provider identifier for managed key, inline private-key, and password sources where credential metadata is emitted.
- [ ] Existing task executor behavior remains compatible for command, restore, remote target checks, and rsync backup paths.
- [ ] Tests cover provider metadata, missing managed-key fail-closed behavior, stale inline-key non-fallback, password metadata, and safe errors.
- [ ] Targeted executor/sshutil tests, full backend tests, and backend build pass before commit.

## Definition of Done

- Trellis task is started before implementation and archived after the work commit.
- Code changes are committed on `security/p4-next-hardening-slice`, not `main`.
- Verification evidence is based on actual command output.
- PR is created, CI is green, branch is merged, Release Please completes, GitHub release is published, Docker publish succeeds, and local `main` is synced clean.

## Technical Approach

Use the P4-1 local provider seam as the single source for executor credential construction, but keep an executor-local guard for `SSHKeyID != nil && Node.SSHKey == nil` where current executor semantics require preloaded managed keys. This avoids turning the provider seam into an implicit fallback path from a missing managed key relation to stale inline node private-key material.

For rsync remote-backup key materialization, resolve key content through the provider-backed helper after the same preload guard, then continue existing normalization, temporary PEM file creation, SSH option construction, and cleanup. For SSH client dialing paths, resolve auth methods through the provider-backed helper after the guard and keep runtime audit writes unchanged except for safer provider-populated credential metadata.

## Decision (ADR-lite)

**Context**: P4-1 added an internal `local` credential provider seam, but executor task/runtime SSH code still contains duplicated auth/key/password resolution and emits credential metadata without provider identity.

**Decision**: Implement executor SSH local-provider adoption as the next P4 slice. Defer terminal/session recording, command approval, external providers, SSH CA, and app/repository-secret provider seams.

**Consequences**: This is a small backend-only hardening slice with no migrations or deployment changes. The main trade-off is retaining a narrow executor preload guard even though the shared provider can DB-load keys elsewhere; that guard intentionally preserves current fail-closed executor behavior and prevents stale inline-key fallback.

## Out of Scope

- External credential providers such as Vault, KMS, Boundary, Teleport, SSH CA, cloud secret managers, or provider lease/health semantics.
- Terminal/session recording, replay, transcript storage, retention policy, or UI.
- Interactive terminal command approval or command parsing.
- Task/batch reviewer approval workflows.
- Public API, frontend, deployment, docs, or migration changes.
- Restic repository-password provider seam and profile hook/app credential provider seams.

## Research References

- [`research/remaining-p4-roadmap.md`](research/remaining-p4-roadmap.md) — ranks executor SSH local-provider adoption as the smallest high-value next P4 slice after P4-1.
- [`research/current-credential-gaps.md`](research/current-credential-gaps.md) — identifies executor-side duplicated credential resolution as the main remaining direct credential-resolution gap.
- [`research/session-recording-feasibility.md`](research/session-recording-feasibility.md) — confirms terminal recording/command approval are larger, cross-cutting slices that should not be bundled here.

## Technical Notes

- Primary implementation anchors: `backend/internal/task/executor/ssh_connect.go` and `backend/internal/task/executor/executor.go`.
- Primary tests: `backend/internal/task/executor/ssh_connect_test.go`, `backend/internal/task/executor/executor_test.go`, and existing `backend/internal/sshutil/credential_provider_test.go`.
- Relevant specs: backend SSH key least-privilege scope, credential-use audit, credential access grants, and backend error-handling sensitive-data exclusions.
