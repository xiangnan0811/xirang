# Child 14 Technical Design

## 1. Design boundary

Child 14 adds one lifecycle authority around the existing backup-asset graph.
It does not replace Repository read/publication, RecoveryPoint leases, Catalog,
Search, Content, Processing, Export, Recovery, Overlay, or Audit owners. It
coordinates them through narrow point-scoped ports and adds only the durable
facts that do not exist on current main.

The runtime remains behind `backup_assets.enabled`; its code default stays
`false`. The design deliberately excludes GA, deployment metadata, Worker
packaging, dependency upgrades, and legacy UI removal.

## 2. Core invariants

1. **Selection and effect are separate.** A policy or purge plan selects opaque
   immutable RecoveryPoint IDs. Provider code never receives age/count/GFS rules.
2. **Revoke before delete.** A point becomes unavailable to new work before any
   dependent cleanup or Provider deletion begins.
3. **Owner cleanup, not cross-package table deletion.** Content, Catalog,
   Search, Processing, Export, Recovery, and Overlay each implement a narrow
   point lifecycle operation.
4. **Physical truth is monotonic.** `expired` means exact deletion was confirmed.
   Unknown, WORM, hold, live lease, or owner failure means `purge_blocked`.
5. **Mutable retirement is non-destructive.** It clears readable/content-bearing
   projections and retires the same stable head ID while preserving bytes and
   the protected rollback locator.
6. **Tombstones never reopen legacy fallback.** Terminal cleanup leaves a
   permanent per-point managed-history tombstone in addition to the existing
   installation/repository latch.
7. **Reconnect reuses current authority.** The existing probe-first `Connect`
   operation remains the only reconnect path; no parallel state machine is added.
8. **Recovery results remain independently owned.** Source-point cancellation
   cannot advance RecoveryResult/workspace cleanup phases.
9. **No private material crosses a public boundary.** Raw Provider identity,
   locator, credential, encrypted reason, proof, grant/ticket, fence token,
   command/output, and file content are private.

## 3. Durable schema (`000070_backup_asset_lifecycle`)

`backend/internal/model/backup_asset_lifecycle.go` defines the GORM parity
models; paired SQL is authoritative.

### 3.1 `backup_retention_policies`

| Field | Contract |
|---|---|
| `id` | 32-character opaque primary key |
| `scope_kind` | `repository` or `task_link` |
| `scope_id` | opaque repository/link ID; never a raw path |
| `revision` | positive monotonic integer; unique with policy ID |
| `rules_json` | canonical versioned rules; only immutable-point age/count/calendar selectors |
| `status` | `active` or `deleted` |
| `created_by`, `updated_by` | actor user IDs |
| timestamps | UTC create/update/delete facts |

At most one active policy exists for an exact scope. Updating a policy is CAS on
the current revision. Historical lifecycle attempts retain selected policy ID,
revision, and a canonical rule digest.

### 3.2 `recovery_point_holds`

| Field | Contract |
|---|---|
| `id`, `recovery_point_id` | opaque IDs; immutable binding |
| `hold_type` | `operational` or `legal` |
| `state` | `active` or `released` |
| `encrypted_reason` | encrypted model-hook field, never JSON |
| `created_by`, `created_at`, `expires_at` | exact create authority; legal expiry may be null |
| `released_by`, `released_at`, `encrypted_release_reason` | immutable release authority |

A partial unique index permits only one active hold of each type on a point.
`RecoveryPoint.hold_state/hold_until` remains the safe aggregate projection and
is updated in the same transaction. Release is one-way.

### 3.3 `recovery_point_lifecycle_attempts`

| Field | Contract |
|---|---|
| `id`, `recovery_point_id` | opaque attempt and exact point |
| `operation` | `retention_expire`, `explicit_purge`, or `mutable_retire` |
| `phase` | `selected`, `revoking`, `draining`, `cleaning`, `provider_delete`, `tombstoning`, `blocked`, `complete` |
| policy facts | nullable policy ID/revision/rule digest and frozen evaluation time |
| purge facts | nullable purge plan ID/revision and actor |
| lease facts | RecoveryPointLease ID/attempt ID plus fence-token hash, never raw token |
| `blocked_reason` | closed code only |
| timestamps | claim/heartbeat/retry/complete UTC facts |

One non-terminal attempt may exist per point. Every transition is CAS on phase,
attempt ID, and current RecoveryPointLease fence. Restart resumes the durable
phase; it does not restart from a guessed side effect.

For immutable expiry/purge, claiming moves an eligible point to `expiring` in
the same transaction. For mutable retirement, the active attempt is an
admission fence while the point remains `observed`; it becomes `retired` only
after dependent cleanup. Explicit purge of an observed/retired mutable head
moves it to `expiring`.

### 3.4 `recovery_point_lifecycle_tombstones`

Each immutable event row is keyed by `(recovery_point_id, terminal_operation)`
and stores only point/repository opaque IDs, original semantics, terminal
operation/state, managed-history latch=true, keyed deletion-receipt digest when
deletion was confirmed, retirement/purge timestamps, and safe closed result
codes. A mutable point may therefore retain one `mutable_retire` event and one
later `explicit_purge` event without rewriting or deleting its retirement
history. Duplicate terminal operations for the same point remain impossible.
Rows contain no locator, name, path, credential, reason, or command output.
Update/delete triggers make every event and the managed-history latch permanent.

The existence of any event for a point implements the existing
`repository.ManagedHistoryTombstoneSource`, so legacy fallback stays closed even
after content-bearing metadata is removed.

### 3.5 `backup_repository_import_candidates`

The queue stores repository ID, candidate opaque ID, closed candidate kind,
keyed source fingerprint, encrypted private Provider locator/evidence, review
state (`pending`, `accepted`, `rejected`), reviewer/timestamps, and optional
accepted RecoveryPoint ID. Pending/rejected rows never join normal Repository or
RecoveryPoint list queries.

Uniqueness on repository + keyed source fingerprint makes discovery/review
idempotent. One failed/quarantined source record may map to at most one accepted
`imported_baseline`.

### 3.6 `backup_asset_purge_plans` and items

A short-lived plan freezes repository ID, requester, exact impact revision,
expiry, hold/lease/WORM counts, and status. Child items contain exact
RecoveryPoint IDs and expected point/capability revisions. Execute requires the
same actor, an unexpired plan, exact revisions, reason, and fresh
`repository.purge` proof. Any drift invalidates the entire plan before effects.

### 3.7 `backup_asset_config_import_refs`

This table maps a source document ID + stable source reference + entity kind to
one local opaque entity ID. It preserves shared-repository remapping and repeat
idempotency without reusing source numeric DB IDs. Conflicting remaps fail the
transaction.

### 3.8 Existing schema changes

- Add `RecoveryPoint.point_revision` as a positive monotonic lifecycle CAS token,
  distinct from `capability_revision`. Paired `000070` triggers advance it on
  every RecoveryPoint row mutation and reject skipped or decreasing revisions;
  existing rows enter `000070` at revision 1. Selection and purge plans carry
  both revisions so state/hold/availability drift cannot hide behind an
  unchanged capability revision.
- Extend the RecoveryPointLease holder check with `retention_worker`; reuse the
  current lease service for acquire/renew/release/takeover/fencing.
- Do not recreate RecoveryPoint states, hold projection fields, retention time,
  retirement fields, audit actions, or managed-history latch tables.
- Paired metadata-admission triggers reject a used `000070` downgrade before
  `golang-migrate` can leave the version dirty. Pristine down removes only
  Child 14 schema and restores the exact prior lease-holder definition.
- Used-down guards include every policy, hold, attempt, tombstone, import,
  purge-plan, and config-ref fact. A tombstone/latch is permanently used even if
  operational rows have later been compacted.

## 4. Lifecycle services

### 4.1 Policy selector

`retention.PolicyService` validates/canonicalizes rules and returns a
`Selection` containing policy ID/revision, evaluation time, rule digest, and
sorted exact point IDs with separate point/capability revisions. It locks the
selected RecoveryPoint rows in the caller transaction, queries only immutable semantics, and never
selects `mutable_head`, `observed`, `retired`, already terminal, held, or
unavailable rows for ordinary retention.

Repository and Task-link scopes are resolved through live immutable link facts.
Selection and attempt creation occur in one transaction; a changed policy,
point, hold, link, or capability revision yields conflict rather than a partial
selection.

The Task retention integration is a dedicated facade in this package. It maps
the Task 1 safe request into `PolicyService` selection, revalidates the exact
Task-to-Repository link and caller handoff scope, and returns only sorted opaque
RecoveryPoint IDs plus the revisions needed by lifecycle claim. It never
returns Provider locators, credentials, paths, executor configuration, rule
operands, or repository-wide selectors to `internal/task`.

### 4.2 Hold service

`retention.HoldService` owns create/release and the safe RecoveryPoint aggregate
projection. It returns metadata without reason plaintext. Expired operational
holds are released by the same service in a bounded worker pass; legal holds
never auto-release.

The purpose-exact step-up action is `retention.hold_release`. It is added to both
backend and frontend registries with pairwise cross-purpose tests against
`repository.purge` and all existing actions.

Registry and Foundation-setting additions also update every exhaustive backend
consumer fixture. In particular, handler step-up cardinality matrices and
snapshot-indexer Foundation settings remain exact, and the full backend test
gate must reject stale fixed snapshots before a task can be checked complete.

### 4.3 Coordinator phases

For each claimed point, `retention.Coordinator` performs:

```text
selected
  -> revoke new admissions/interests
  -> drain/take over ordinary point leases
  -> owner-specific content/derived/catalog/search/overlay cleanup
  -> exact Provider deletion (expiry/purge only)
  -> clear private locator only after confirmed deletion
  -> persist permanent tombstone
  -> terminal RecoveryPoint state
```

Every owner call is idempotent and may be retried. The coordinator advances a
phase only after all effects in that phase are proven. A bounded detached
cleanup context is allowed only where the owning package already requires it;
it always has a deadline.

Blocked classification is closed and conservative:

- `active_hold`
- `lease_live`
- `lease_drain_unproven`
- `owner_cleanup_unproven`
- `provider_worm`
- `provider_unavailable`
- `provider_identity_conflict`
- `provider_delete_unproven`
- `deletion_unavailable`
- `fence_lost`

All map to `purge_blocked` for destructive operations. Mutable retirement does
not call Provider deletion; if cleanup is unproven the attempt stays blocked and
the point never reaches `retired`.

### 4.4 Dependent lifecycle ports

The coordinator depends on point-scoped interfaces, composed in
`backupasset/runtime`. Each method accepts the shared closed
`backupasset.SourceLifecycleRequest`, not a bare point ID:

- `ContentSourceLifecycle.RevokeAndDrainRecoveryPoint`
- `CatalogSourceLifecycle.RetireRecoveryPoint`
- `SearchSourceLifecycle.RevokeRecoveryPoint`
- `ProcessingSourceLifecycle.RevokeRecoveryPoint`
- `ExportSourceLifecycle.ExpireRecoveryPoint`
- `RecoverySourceLifecycle.CancelRecoveryPointInterests`
- existing `overlay.Service.ReconcileSource`

`SourceLifecycleRequest` contains only the opaque RecoveryPoint ID, lifecycle
attempt ID, closed lifecycle operation, and a closed `prepare|cleanup` owner
stage. It contains no authority/fence token, locator, key, credential, result
ID, or workspace-cleanup state. The pure aggregate constructs `prepare` only
from its coordinator `RevokeRecoveryPoint` entry and `cleanup` only from its
coordinator `CleanupRecoveryPoint` entry; callers cannot select a stronger
stage through an owner API. Every owner verifies the exact active lifecycle
attempt and that its durable coordinator phase matches the requested owner
stage before mutation.

Each owner may scan its own rows by RecoveryPoint ID and call its current lower-
level lifecycle methods. The retention package never deletes owner tables or
key material directly.

Task 6 adds a pure runtime aggregate adapter but does not install it into
`Runtime` startup/run/shutdown; Task 8 remains the production-composition owner.
The adapter implements both coordinator phases and requires every owner at
construction. Revoking invokes Content, Catalog, Search, Processing, Export,
and Recovery in that order before the coordinator drains ordinary point
leases. Catalog and Search are included because their active builders also own
ordinary RecoveryPoint leases; leaving either until Cleaning would strand the
attempt behind the generic live-lease guard. Revoking fences admission,
cancels/joins active work, terminalizes only owner work-authority rows needed to
make that cancellation durable, and releases exact-point owner leases. It does
not supersede active completed projections or delete projection payloads, keys,
references, or ciphertext. Cleaning replays the same idempotent owner order to
prove every lease and owner output is settled, performs those durable cleanup
transitions, lets Processing destroy derived references/keys/ciphertext only
after Search revocation is proven, and finally reconciles Overlay state. A
missing owner, context expiry, bounded batch exhaustion, live owner fence, or
incomplete proof returns an error so the coordinator durably records
`owner_cleanup_unproven`; it never terminalizes a point from partial progress.

Aggregate tests freeze this boundary: after `prepare`, all six owners have no
active work or live owner lease while completed projection rows, wrapped keys,
references, and ciphertext remain unchanged; only `cleanup` may supersede or
remove those payloads. They also prove the exact Cleaning order Content ->
Catalog -> Search -> Processing -> Export -> Recovery -> Overlay and that no
Processing destruction occurs before the Search cleanup proof.

`LifecyclePointRequest` carries the closed lifecycle operation in addition to
opaque point/attempt IDs. The adapter maps `mutable_retire` to
`overlay.SourceRetired` and expiry/purge to `overlay.SourceExpiring`; it exposes
no locator, key, credential, result-cleanup phase, or fence material to an
owner. Overlay maintenance loops bounded batches until a zero-result pass proves
completion. It may run while the feature is disabled only after verifying the
exact active lifecycle attempt, so already-claimed maintenance cannot be
stranded and the maintenance path cannot become a general feature-gate bypass.

Every point-bearing write that can precede ordinary lease acquisition must be
transaction-bound to the lifecycle fence. Search generation creation and
content/OCR publication and Overlay saved-search, favorite, tag, and recent
writes lock the exact RecoveryPoint first and reject any non-complete lifecycle
attempt or non-readable lifecycle state. Processing uses one canonical order:
RecoveryPoint, lifecycle attempt, then exact job. `RequestWork` completes that
admission before it locks/reuses a current job or inserts a new job and interest.
`Pull` discovers candidates without a row lock, then completes the same point
and lifecycle-attempt admission before it locks and revalidates the exact queued
job. The real PostgreSQL barrier regression requires zero transaction retry and
rejects any `40P01`; preserving this order avoids the proven point/job deadlock.
Existing derived-
manifest publication continues to revalidate its exact job/attempt/grant/lease
fence transactionally; source-lifecycle tests exercise the real entry point to
prove a fenced job cannot publish late output. Search state-changing paths use
point-before-lease ordering. Together these checks close the window in which
output is prepared after lifecycle claim but before an owner scan.

Owner cleanup is idempotent and evidence-preserving. Content revokes grants,
joins reads, releases/takes over only provable leases, and evicts exact-point
cache while preserving request/audit history and all RecoveryResult resources.
Catalog preserves generation/entry history while removing active projection.
Search preserves generation evidence and the shared Search key while removing
point-owned active projections/documents. Processing preserves job/attempt
history, revokes interests/grants, releases point leases, proves Search
revocation, and then removes only owner-controlled derived references, wrapped
keys, and ciphertext. Export reuses its existing ordered lifecycle owner for
every exact-point job. Recovery cancels only source jobs/interests and releases
source leases.

The Recovery source port cancels/fences active source jobs and releases source
leases. It intentionally has no method that accepts a RecoveryResult ID or a
RecoveryResult cleanup phase. Result/workspace cleanup continues under the
existing Child 13 owner and schedule.

### 4.5 Worker and runtime ownership

`retention.Worker` embeds the shared retention loop contract but adds a durable
per-point RecoveryPointLease claim. It runs one bounded startup reconciliation,
then periodic policy selection, hold expiry, lifecycle retry, import/rebuild
reconciliation, and audit-detail pruning. It emits aggregate low-cardinality
metrics for selected/retired/expired/blocked/retried outcomes.

`backupasset/runtime.Runtime` constructs, starts, runs, and joins the worker. It
exposes only handler-facing policy/hold/purge and repository import/rebuild
facades. Feature disable stops new claims, joins current work, but retains
maintenance needed to settle already claimed attempts conservatively.

## 5. Exact Provider deletion boundary

### 5.1 Port

`provider.PointDeleter` is a separately registered optional capability:

```go
type DeletePointRequest struct {
    Snapshot ReadSnapshot
    Point    PointLocator
    ExpectedSourceRevision string
    OperationID string
}

type DeletePointResult struct {
    Outcome DeletePointOutcome // deleted, already_absent, blocked_worm
    ReceiptDigest string
}
```

The locator and binding remain `json:"-"`. Registration never makes the read
adapter mutating by default. Missing deletion capability maps to typed blocked,
not to a generic command fallback.

### 5.2 Provider implementations

- **Restic:** accept only the full stored native snapshot ID owned by the exact
  RecoveryPoint/repository. Invoke a fixed server-owned `forget` operation with
  that exact ID and prune semantics; reject `latest`, prefixes, tags, time/GFS,
  and multi-snapshot selectors. Re-list/probe proves the exact snapshot absent.
- **Rsync:** accept only a committed Xirang point component verified from the
  stored marker/manifest and managed-root binding. Use handle-relative/no-follow
  deletion inside the managed points directory, with source/marker checks before
  and after. Never delete a legacy mutable target during retirement.
- **Rclone versioned prefix:** accept only the unique committed prefix/marker
  bound to the point and delete that exact prefix through a fixed operation.
- **Rclone native object versions:** delete only the frozen exact
  physical-key/version-ID set from the accepted manifest; an unversioned/current
  object delete is forbidden. WORM/retention lock is typed blocked.

Command runners use fixed tool/operation enums, separately quoted private
operands, bounded output/time/concurrency, secret stdin, cancellation/join, and
safe error classes. Tests preserve the read-adapter mutation ban and prove only
the new deletion port can reach these exact operations.

## 6. Repository reconnect, import, and rebuild

### 6.1 Reconnect

The UI calls existing `POST /backup-repositories/connect` with Task ID and the
selected repository ID. Service behavior remains probe-first:

- exact identity and live non-archived Task lineage -> rebind, update safe
  capability revision, keep non-retired mutable-head ID;
- identity/Task/provider drift -> conflict, no binding or point mutation;
- retired head -> reconnect repository access if valid but never reactivate or
  overwrite the retired row; a fresh reviewed baseline gets a new point ID;
- probe failure -> no new binding, prior safe facts preserved.

### 6.2 Import discovery and review

Discovery is bounded and cursor-based. Provider-native facts are normalized to
keyed fingerprints and private encrypted locators before persistence.

- full attributable Restic snapshot -> directly admissible immutable candidate;
- valid Xirang marker plus complete commit digest graph -> admissible manifest;
- arbitrary or ambiguous Provider point -> pending Admin review only;
- failed/quarantined source -> at most one `imported_baseline` review path;
- mutable tree -> never fabricated into historical immutable points.

Accepting a candidate transactionally creates or binds one RecoveryPoint and
records the candidate's terminal mapping. Rejecting it is terminal. Operators
cannot list pending candidates, and ordinary counts query only accepted points.

### 6.3 Rebuild

Rebuild validates every accepted manifest, creates a fresh Catalog generation,
and schedules eligible derived backfill at low priority through existing
owners. The response reports accepted, catalog-started, derived-queued,
partial, and failed counts with safe closed reasons. It cannot reconstruct
overlays, audit events, holds, policies, or credentials.

## 7. Task archive/unlink

`task.ArchiveService` owns the transaction:

1. lock Task and dependent references;
2. reject if a live dependent Task still references it;
3. set `enabled=false` and `archived_at=now` without erasing executor/history;
4. set active `TaskRepositoryLink.task_id=NULL`, `unlinked_at=now`, preserving
   existing Task/node snapshots and encrypted protected legacy locator;
5. commit;
6. remove schedule.

If commit fails, schedule remains. If schedule removal unexpectedly cannot be
proven, the archived/disabled database state is safe and retryable; it is not
rolled back into an enabled Task. HTTP returns an archive result, not a claim
that Provider bytes were deleted.

A conservative hard-delete reaper may delete only an archived row with no
Task dependencies, TaskRuns/TaskLogs, active link, audit reference, or other
known foreign owner. Any query/error/unknown dependency keeps the row. In most
real installations the preserved history means the row remains archived.

## 8. API contract

### 8.1 Repository lifecycle routes

Existing routes remain:

- `POST /backup-repositories/connect`
- `POST /backup-repositories/:id/reconcile`
- `POST /backup-repositories/:id/disconnect`

New bounded Admin routes:

- `POST /backup-repositories/:id/import-scans`
- `GET /backup-repositories/:id/import-candidates`
- `POST /backup-repositories/:id/import-candidates/:candidateId/reviews`
- `POST /backup-repositories/:id/rebuilds`

They require `backup_repositories:manage`; review/rebuild also require Admin.
Request bodies accept only opaque IDs, closed actions, limits/cursors, and
review notes that are encrypted/private. No binding/path/credential input is
accepted.

### 8.2 Policy, hold, and purge routes

- `GET|POST /backup-retention-policies`
- `PATCH|DELETE /backup-retention-policies/:id`
- `POST /backup-retention-policies/:id/impact`
- `GET|POST /recovery-points/:id/holds`
- `POST /recovery-points/:id/holds/:holdId/release`
- `POST /backup-repositories/:id/purge-plans`
- `POST /backup-repositories/:id/purges`

Policy/hold routes require Admin plus `backup_repositories:manage`. Hold release
uses `RequireStepUp(... retention.hold_release ...)`. Purge routes require Admin
plus `backup_repositories:purge`; execute also verifies a body/header proof for
`repository.purge` and consumes an exact unexpired impact plan.

Every handler uses strict bounded JSON, standard envelopes, safe sentinel error
mapping, safe 404 parity for unowned/missing resources, feature-gate-first
behavior, and typed 409/501/503 results. Swagger is regenerated from the live
routes.

### 8.3 Error matrix

| Condition | Result |
|---|---|
| feature disabled | 503 `feature_disabled`; no DB/provider/owner effect |
| malformed/unknown ID or body field | 400 generic Chinese message |
| missing/unowned repository or point | same 404 |
| identity/policy/point/plan revision drift | 409; zero effects |
| missing Provider delete port | 501 typed capability; point blocked |
| Provider timeout/offline/resource limit | 503 typed capability; point blocked |
| live hold/lease/WORM | 409 typed impact or accepted blocked operation; never expired |
| wrong/cross-purpose/expired proof | 403; zero lifecycle mutation |
| unexpected DB/crypto/SSH/provider error | generic 500; safe structured log only |

## 9. Frontend design

### 9.1 API boundary

- Extend `backup-repositories-api.ts` with typed connect/reconcile/disconnect,
  import discovery/candidate review, and rebuild methods.
- Add `backup-retention-api.ts` for policy, hold, impact plan, and purge methods.
- Raw snake_case types stay private; exported methods return mapped camelCase
  domain types. Unknown states fail closed to blocked projections, not success.
- Proofs use `ensureStepUpProof` and the central request wrapper; components do
  not call `fetch` or read session storage directly.

### 9.2 Components

- Extract the live read-only repository block into
  `RepositoryManagementPanel` and add Admin-only reconnect, reconcile,
  disconnect, import review, rebuild, and purge dialogs.
- `RetentionPolicyPanel` owns policy list/editor, exact impact preview, hold
  creation/release, and blocked-state display.
- `BackupAssetsWorkspace` composes the panels and refreshes the existing state
  controller after successful mutation. `BackupsDataPage` passes a narrow
  lifecycle runtime `{token, role, userId, ensureStepUpProof}`.
- Destructive controls display exact counts/status, require typed confirmation
  and reason, and never display Provider locators or decrypted bindings.
- Dialogs use Radix titles, labeled controls, focus return, disabled/loading
  states, keyboard operation, decorative `aria-hidden` icons, and axe smoke.
- Chinese is primary; English keys are complete. Status never relies on color
  alone.

## 10. Config v2

The v2 document has a top-level `document_id`, `version: "2.0"`, and a `data`
graph. Entity relationships use stable string references such as `task_ref`,
`repository_ref`, `link_ref`, and `policy_ref`; no source numeric ID is used as
an import identity.

Default export includes safe repository display/provider/status/version mode,
keyed identity reference, link snapshots, and policy rules. It always emits an
empty `recovery_point_holds` array. Holds are DB-plus-key disaster-recovery
facts and must not be restored from config. Import rejects any non-empty hold
list (`errConfigAssetGraphInvalid` / HTTP 400). The export also omits private
identity/locator/binding/reason and all ephemeral authority.

Sensitive export may add an encrypted reconnectable binding envelope only after
the existing Admin/config-export proof/grant middleware. The asset audit records
only counts and `with_sensitive`; it never records the envelope.

Import flow is parse -> version dispatch -> validate complete reference graph ->
resolve source refs -> build a transaction plan -> persist disconnected facts.
It does not probe or call a Provider. Repeated import resolves the same mapping;
conflicting local identity or changed source mapping aborts the whole graph.
Version 1.0 dispatches to the current importer unchanged.

## 11. Audit and observability

- Reuse existing audit actions. Inputs contain actor, opaque repository/point/
  policy/hold IDs, closed stage/outcome/reason codes, aggregate item/byte counts,
  and request correlation ID only.
- Retention metrics have closed labels such as operation/outcome/provider class;
  no repository/point/user/path label.
- Audit-detail pruning selects only closed eligible segments and calls
  `AuditWriter.PurgeSegmentDetails`; it never deletes checkpoints.
- Logs use `logger.Module("backup_asset_retention")` and stable opaque IDs only.
  Provider errors are classified before logging; raw command output is excluded.

## 12. Disaster recovery and rollback

### Disaster recovery

- DB + the matching `DATA_ENCRYPTION_KEY` restores repositories, access
  bindings, policies, holds, overlays, audit chain, lifecycle attempts,
  tombstones, and wrapped-key readability.
- Provider-only survival permits bounded reconnect/import and rebuild of
  verifiable Provider-derived RecoveryPoint/Catalog facts. It cannot recreate
  user overlays, audit evidence, policies/holds, task relationships, or secrets.
- Wrong/missing encryption key fails decryption and reconnect; it never silently
  replaces a binding or claims a successful rebuild.

### Rollback

1. Disable new policy selection and purge claims.
2. Keep the lifecycle worker in maintenance mode until claimed attempts are
   safely complete or durably blocked.
3. Revoke UI/API mutation exposure if necessary; preserve all `000070` rows.
4. A pristine `000070` may down. Any used fact/tombstone blocks down and requires
   an application rollback plus forward repair.
5. Never recreate deleted Provider bytes from Catalog. Use Provider-native or
   independent backup recovery.

## 13. Alternatives rejected

- **Keep Task age/path retention as managed authority:** rejected because it
  cannot prove immutable point identity or dependent lease cleanup.
- **Add delete methods to every read adapter:** rejected because it silently
  widens a deliberately read-only boundary.
- **Delete owner tables directly from retention:** rejected because it bypasses
  grants, drains, keys, quotas, reconciliation, and package invariants.
- **Reuse `repository.purge` proof for hold release:** rejected because release
  is a distinct irreversible authority and must be purpose-exact.
- **Rebuild overlays/audit from Provider:** rejected because those are not
  Provider facts.
- **Put lifecycle worker directly in `main.go`:** rejected because the asset
  runtime already owns and joins all dependent graphs.

## 14. Design review gate

Before Phase 2, review must confirm:

- schema/down-admission and state transitions are complete for both engines;
- each dependent owner port is point-scoped and idempotent;
- Provider deletion is exact and separately registered;
- Child 13 result lifecycle cannot be advanced through the new interfaces;
- purge/hold step-up purposes are pairwise isolated;
- config v2 has no numeric-ID or secret leak and import has zero Provider effect;
- runtime startup/disable/shutdown joins all work;
- the exact file/test manifest in `implement.md` matches the live tree.

No implementation starts until the user approves this design and explicitly
authorizes `task.py start`.
