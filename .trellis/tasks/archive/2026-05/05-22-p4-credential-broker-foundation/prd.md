# P4 credential broker foundation

## Goal

Introduce the first P4 credential broker/provider foundation by routing a small, high-value credential resolution surface through an internal `local` provider seam while preserving today’s encrypted local database storage, deployment shape, API behavior, authorization gates, and secret-safety guarantees.

## Current Assessment

P3 completed the control-plane grant model for high-risk operations. P4 should now begin reducing credential architecture risk, but the first slice must be deliberately small: establish an internal provider abstraction and prove it can delegate to existing local encrypted storage without changing user-visible behavior. External Vault/KMS/CA/session-recording work remains future architecture work.

## Requirements

- Add an internal backend credential provider/broker seam with provider identity fixed to `local` for this slice.
- Keep existing local encrypted DB storage as the only source of truth; do not add migrations, provider tables, env vars, deployment changes, or public API fields.
- Integrate the seam into the safest useful first credential-resolution path: SSH auth construction around existing purpose-aware `sshutil` helpers, preserving managed SSH key scope checks, inline credential behavior, password/key auth behavior, safe resolved metadata, and `LastUsedAt` updates.
- Preserve existing operation-layer controls: primary auth, RBAC, ownership, step-up, credential access grants, SSH key purpose/scope checks, and credential audit ordering must remain additive and fail closed.
- Ensure the provider seam never logs, audits, stores, returns, or exposes raw passwords, private keys, executor config, terminal streams, command text/output, file contents, Docker output, raw SQL, endpoint/proxy values, hostnames, target/include paths, imported/exported payloads, or step-up proof material.
- Add focused tests proving local-provider equivalence for managed SSH key, inline private key, password auth, scope denial, and safe metadata.
- Document in task PRD/research only; do not add tracked process docs or user-facing deployment docs because behavior is internal and unchanged.

## Acceptance Criteria

- [ ] A backend internal credential provider package or seam exists with a hardcoded local implementation and no external provider dependencies.
- [ ] Existing SSH auth call sites can resolve credentials through the local provider without changing public API/deployment behavior.
- [ ] Managed SSH key resolution still enforces disabled/expired/purpose/node/tag scope checks and updates `LastUsedAt` as before.
- [ ] Inline node private key and password auth behavior remains equivalent to current behavior.
- [ ] Provider result metadata is safe and compatible with current audit metadata; tests assert raw secret material is not included in metadata/errors/loggable result fields.
- [ ] No migrations, env vars, provider registry config, Vault/KMS SDKs, SSH CA behavior, terminal/session recording, or command approval UI are added.
- [ ] Targeted backend tests pass.
- [ ] Full backend verification passes before commit.
- [ ] If frontend is untouched, frontend full check is not required for the implementation commit; if frontend changes, run full frontend check.

## Out of Scope

- External Vault, KMS, Boundary/Teleport integration, provider configuration UI, provider health endpoints, leases, renewal/revoke workers, or fallback policy.
- SSH CA/external CA, host trust rollout, CA key lifecycle, principal mapping, or replacing stored SSH keys/passwords.
- Terminal/session recording, command-level approval/inspection, command transcript storage, or replay.
- Data migration from plaintext local encrypted fields to provider references.
- Config import/export provider-reference semantics beyond preserving existing behavior.
- Changing REST/WS API responses, frontend UI, Docker/Compose runtime, env var docs, or user deployment requirements.

## Technical Approach

Use the smallest local-provider seam around existing SSH credential helpers rather than a broad broker facade. The provider request carries a node, DB handle, and purpose; the local provider delegates to existing `sshutil` resolution/scope behavior and returns immediate in-memory auth material plus safe metadata. Keep the seam below call sites and above storage hooks: do not manually encrypt/decrypt in handlers, and do not bypass GORM hook behavior.

## Decision (ADR-lite)

**Context**: P4 architecture planning includes external credential brokers and Vault/KMS, but introducing an external provider first would add deployment, migration, outage, import/export, and secret-leakage risks before the repo has a stable provider seam.

**Decision**: Implement a hardcoded `local` provider foundation first, integrated with the existing purpose-aware SSH credential path. This proves the abstraction while preserving all current behavior and P1-P3 controls.

**Consequences**: The first PR is intentionally conservative and may look like internal plumbing, but it creates the extension point needed for future provider health/lease/fallback work without prematurely committing to Vault/KMS schemas or deployment dependencies.

## Research References

- [`research/current-credential-resolution.md`](research/current-credential-resolution.md) — maps current credential-bearing models, SSH/executor/import/export/control order, seam candidates, tests, and risks.
- [`research/broker-foundation-patterns.md`](research/broker-foundation-patterns.md) — compares broker/provider patterns and recommends the smallest local-provider seam around existing helpers.

## Definition of Done

- Code changes are minimal and internal to backend credential resolution/provider plumbing.
- Tests prove equivalence and fail-closed behavior for the touched credential path.
- No new secret exposure appears in responses, logs, audit metadata, test fixtures, browser storage, or docs.
- Verification commands pass with actual output.
- Changes are committed on `security/p4-credential-broker-foundation`, Trellis task is archived/journaled, PR is created, CI is green, branch is merged, and release/Docker publish automation is monitored if triggered.
