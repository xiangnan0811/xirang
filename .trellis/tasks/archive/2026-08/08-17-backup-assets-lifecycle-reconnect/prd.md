# 备份资产生命周期与重连

## Goal

Make backup-asset retention, mutable-head retirement, reconnect/import/rebuild,
holds, and explicit purge operate through one fail-closed RecoveryPoint
lifecycle owner. Managed data must be selected by exact opaque RecoveryPoint ID,
all dependent interests must be revoked and drained before metadata or Provider
effects, and no operation may infer deletion authority from a Task path, age,
favorite, tag, or legacy executor setting.

This child delivers lifecycle governance behind the existing
`backup_assets.enabled` gate. It does not make the feature generally available.

## User outcomes

### Administrator

- Archive a linked Task without destroying repository, RecoveryPoint, Catalog,
  run, or audit history.
- Disconnect and later reconnect a repository through the existing identity-
  matching connect contract, with explicit conflict if identity changed.
- Discover attributable Provider points, review unknown imports, accept a
  verified baseline, and rebuild Catalog/eligible derived data with a truthful
  complete/partial/failure report.
- Define versioned repository/task-link retention policies and preview the exact
  immutable RecoveryPoint IDs selected by a policy.
- Place an operational or legal hold on an exact immutable point and release it
  only with an Admin reason and fresh purpose-exact proof.
- Preview and execute an explicit point/repository purge with exact scope,
  lease/hold/WORM impact, and retryable `purge_blocked` state.
- Export/import configuration v2 without relying on source database numeric
  IDs, while keeping v1 import compatibility and explicit reconnect after
  import.

### Operator and viewer

- Continue to browse authorized online/offline Catalog facts and see truthful
  lifecycle status.
- Never see Admin-only unknown import candidates, sensitive access bindings,
  reasons, Provider locators, proofs, grants, tickets, or internal cleanup data.
- Never count an unreviewed import as a trusted RecoveryPoint.

## Functional requirements

### R1. Exact managed retention authority

1. Managed retention selects deterministic immutable RecoveryPoint IDs from a
   versioned policy and a frozen evaluation time.
2. Mutable heads (`observed` or `retired`) are excluded from ordinary age/count
   retention.
3. The legacy Task retention loop may remain only for a lineage-guard-proven
   pristine legacy installation. A managed Task delegates to the new owner and
   never reaches directory mtime/`RemoveAll`, broad Restic GFS/age selection, or
   Rclone `--min-age` selection.
4. Every Provider effect is bound to a private exact point locator. `latest`,
   prefixes used as snapshot identity, arbitrary paths/remotes, globs, and
   repository-wide user-controlled selectors are rejected.

### R2. Versioned policy and hold lifecycle

1. A retention policy has an opaque ID, exact scope (repository or Task link),
   monotonically increasing revision, deterministic rule document, status, and
   actor/timestamps.
2. Policy create/update/delete is Admin-only, produces an impact preview, and
   uses the existing retention-policy audit actions.
3. Holds bind one immutable RecoveryPoint. They never arise implicitly from a
   favorite, tag, saved search, or recent access.
4. Hold create requires an Admin reason. Hold release additionally requires a
   distinct fresh step-up proof and release reason.
5. An active hold blocks ordinary expiry and Provider deletion. An explicitly
   authorized purge may revoke access and report impact, but remains
   `purge_blocked` until the hold is independently released.

### R3. RecoveryPoint state and physical truth

1. Ordinary expiry moves an eligible immutable point from a readable state to
   `expiring`, rejects new work, revokes existing interests, drains leases, and
   reaches `expired` only after exact Provider deletion is confirmed.
2. Provider/WORM failure, live hold, unproven drain, identity conflict, or
   deletion uncertainty produces/retries `purge_blocked`; it never claims bytes
   are gone.
3. Exact deletion is idempotent. A retry proves the same point identity and may
   resume only from the durable lifecycle attempt/fence.
4. A mutable head retires non-destructively through
   `observed -> retired`, storing a typed cutover/withdraw reason and time,
   removing readable/content-bearing projections, and preserving Provider bytes
   plus the protected rollback locator.
5. A retired head never reactivates. Later explicit purge uses the same revoke /
   drain ordering and changes the point to `expired` only after deletion proof.

### R4. Dependent owner coordination

Before retirement or purge terminalizes, the coordinator must, in durable and
retryable order:

1. stop new point publication/content/processing/export/recovery admission;
2. revoke content tickets/grants and drain reads;
3. cancel or source-expire processing, export, and recovery jobs and release or
   take over their RecoveryPoint leases;
4. supersede/purge active Catalog and Search projections and prevent late output
   resurrection;
5. destroy derived/export key material and purge owned ciphertext through those
   packages' lifecycle owners;
6. reconcile overlays so saved searches become broken, favorites/tags retain
   opaque tombstones, and recents are removed;
7. retain a safe RecoveryPoint lifecycle tombstone and managed-history latch;
8. write only closed, opaque IDs/counts/outcomes to the asset audit chain.

The existing RecoveryResult/workspace cleanup state machine remains the sole
owner of managed recovery-result cleanup. Child 14 may cancel source interests;
it must not rewrite or terminalize that state machine.

### R5. Audit retention

1. The worker may close/select audit detail segments according to the existing
   dynamic retention settings and call the existing segment-detail purge owner.
2. Checkpoint hashes, anchors, entry counts, and cross-segment links remain
   verifiable after detail purge.
3. A legal/asset purge cannot erase independent audit evidence outside the
   explicit audit-detail policy.

### R6. Task archive and unlink

1. HTTP Task delete becomes a transactionally safe archive operation.
2. The Task is disabled and timestamped, its active repository link is unlinked
   while immutable Task/node snapshots remain, and schedule removal happens
   after the database commit.
3. Repository, RecoveryPoint, TaskRun, TaskLog, and audit history are preserved.
4. Archive never invokes Provider deletion. Metadata hard-delete is eligible
   only after conservative proof that no repository/run/audit/dependency record
   still needs the Task; uncertainty keeps the archived row.

### R7. Reconnect, import, review, and rebuild

1. Reuse the existing `Connect` probe-first identity contract for reconnect.
   Identity match rebinds; mismatch refuses overwrite. A non-retired mutable
   head keeps its stable ID; a retired head cannot reactivate.
2. Failed reconcile preserves the prior good generation/identity and records
   only safe offline/stale facts.
3. Restic import accepts only full attributable native snapshot identities.
   Unknown points enter an Admin-only review queue and do not enter Operator
   counts.
4. Xirang-managed Rsync/Rclone import requires valid marker plus every required
   commit digest. Arbitrary trees may become only an explicitly reviewed mutable
   head or `imported_baseline`.
5. A completion-unproven/rewritten publication may yield at most one separately
   reviewed `imported_baseline`; review cannot relabel its terminal failed point
   or create a second trusted native claimant.
6. Accepted manifests rebuild RecoveryPoints/Catalog and eligible low-priority
   derived backfill, returning complete/partial/failure counts. No rebuild
   invents user overlays or audit history.

### R8. Admin APIs and UI

1. Existing connect/reconcile/disconnect endpoints remain thin and typed;
   frontend wrappers add their missing mutations.
2. Add typed Admin endpoints for import scan/candidate review/rebuild,
   retention-policy CRUD, hold create/release, and dry-run/execute purge.
3. Purge requires `backup_repositories:purge`, Admin role, the existing
   `repository.purge` fresh proof, reason, exact reviewed impact revision, and
   typed hold/lease/WORM status.
4. Hold release requires a new purpose-exact step-up action; a repository purge
   proof cannot authorize it, and vice versa.
5. All routes use the standard response envelope, safe typed error mapping,
   feature gate, RBAC/ownership, rate/body limits, and opaque identifiers.
6. The Chinese-primary UI uses existing primitives, mapped camelCase DTOs,
   explicit loading/empty/error/blocked states, accessible dialogs/controls, and
   Admin-only mutation surfaces. English translations remain complete.

### R9. Config export/import v2

1. Default export version becomes `2.0` and includes non-secret repository,
   link, and policy metadata through stable export references.
   `recovery_point_holds` is always exported as an empty array. Holds are
   database-plus-key disaster-recovery facts and are not config-restorable.
   Import rejects any non-empty hold list (`errConfigAssetGraphInvalid` /
   HTTP 400).
2. Ordinary export omits access secrets, private locators, encrypted reasons,
   proofs, grants, tickets, lifecycle fences, and internal cleanup state.
3. Sensitive export may include reconnectable access bindings only through the
   existing Admin + config-export step-up + credential-grant path.
4. Import never reuses source numeric database IDs. It remaps stable references,
   preserves shared-repository relationships, is idempotent on repeat, and fails
   closed on identity conflicts.
5. Imported repositories/access bindings are disconnected by default. Import
   performs no Provider mutation or probe; an Admin must explicitly reconnect.
6. Version `1.0` remains accepted with current behavior.

### R10. Disaster recovery truth

1. Behavioral tests prove a new database can rebuild Provider-derived
   repository/RecoveryPoint/Catalog facts only after valid reconnect/import.
2. User overlays and audit history require database restoration.
3. Encrypted bindings, reasons, and wrapped domain keys require the matching
   `DATA_ENCRYPTION_KEY`; missing/wrong key fails closed.
4. Admin documentation states the DB + encryption-key backup/restore contract
   and distinguishes Provider rebuildable facts from non-rebuildable control
   plane/audit/user state.

## Non-functional requirements

- Paired SQLite/PostgreSQL `000070` apply, pristine down, used-down admission,
  schema-definition parity, transaction/fault, and behavior tests are required.
- Required PostgreSQL evidence is fail-closed: a missing required DSN is not a
  skipped pass.
- Every implementation slice starts with a captured genuine RED and reaches
  GREEN with the exact same selector. Child 13's historical provenance exception
  does not apply.
- Workers must be startup-safe, idempotent, bounded, cancelable, joined during
  shutdown, heartbeat/fence protected, and free of unbounded detached contexts.
- Logs, metrics, errors, API payloads, UI state, audit events, config defaults,
  and test artifacts must not leak raw locators, credentials, reasons, proofs,
  tokens, grants, ticket IDs, fence tokens, command output, or file contents.
- Existing public/package contracts are reused before adding new ones. In
  particular, do not duplicate RecoveryPoint states, leases, audit actions,
  overlay tombstones, or RecoveryResult lifecycle ownership.
- No new dependency is permitted without a separately approved scope change.

## Explicit exclusions

- Child 15 creation or implementation.
- `000071`, GA enablement, changing `backup_assets.enabled` from default false,
  legacy snapshot UI removal, or public migration/GA controls.
- Worker Dockerfile/entrypoint/seccomp, Compose profile, dual-architecture
  Worker build/scan/smoke, Docker metadata, public image publication, or release
  tag changes.
- Dependency or lockfile upgrades based only on the recorded npm-audit residual
  risk.
- Reopening Child 13 or modifying its historical research/evidence.
- Replacing the managed RecoveryResult/workspace cleanup state machine.

## Acceptance criteria

- [ ] The first implementation selector is captured as a real RED and the exact
      selector later passes GREEN; every subsequent slice has the same evidence.
- [ ] Managed Task retention delegates deterministic exact RecoveryPoint IDs and
      cannot reach legacy path/age deletion.
- [ ] Paired `000070` provides policy, hold, lifecycle attempt/tombstone, import
      review, and retention-worker fencing contracts with pristine/used down
      safety on SQLite and required PostgreSQL.
- [ ] Policy/hold selection, expiry, mutable retirement, explicit purge,
      `purge_blocked`, lease takeover/fencing, restart, and idempotency matrices
      pass on both engines where persistence semantics differ.
- [ ] All dependent content/catalog/search/processing/export/recovery/overlay
      owners revoke/drain/clean in order, reject late output, and preserve Child
      13 result-lifecycle ownership.
- [ ] Exact Restic/Rsync/Rclone deletion tests prove no broad selector or raw
      locator crosses API/audit/log boundaries and no early `expired` claim.
- [ ] Task delete archives/unlinks and preserves repository/history with correct
      schedule compensation ordering.
- [ ] Reconnect reuses the live identity-safe path; import/review/rebuild cannot
      create false trusted history or expose unknown candidates to Operators.
- [ ] Admin lifecycle APIs/UI are typed, permissioned, purpose-exact,
      localized, accessible, and cover loading/empty/error/blocked/success.
- [ ] Config v2 stable-reference round trip, repeat idempotency, v1 compatibility,
      disconnected default, shared-repository remap, and conflict failure pass.
- [ ] DR tests/docs state exactly what Provider reconnect can rebuild and why DB
      plus `DATA_ENCRYPTION_KEY` restoration is required.
- [ ] `backup_assets.enabled` remains default false; no Child 15/deploy/GA/
      dependency paths appear in the diff.
- [ ] Focused tests, required PostgreSQL selectors, backend full tests/build,
      frontend `npm run check`, Swagger regeneration/check, privacy/static scans,
      `git diff --check`, and repository full gate pass or a real external
      blocker is recorded.
- [ ] Independent lifecycle/security review has no unresolved Critical or
      Important findings.
- [ ] The dedicated branch is delivered through PR and required CI; squash merge,
      exact-main CI, Release Please, expected release/image disposition, local
      main sync, Child 14 archive, and parent progress are recorded before this
      child is called complete.

## Planning gate

Task creation authorizes only this focused planning package. Product code and
Phase 2 remain unauthorized. Do not run `task.py start` until the user reviews
`prd.md`, `design.md`, and `implement.md` and explicitly approves the plan and
start transition.
