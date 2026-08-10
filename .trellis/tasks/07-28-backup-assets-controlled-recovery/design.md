# Child 13 controlled recovery design

## 1. Status and authority

This design refines the approved parent contracts at
`.trellis/tasks/07-12-backup-data-explorer-design/{prd,design,implement}.md`
against `main@51771654a85967656fe1ca69686590b734ff9214`.

- Task: `.trellis/tasks/07-28-backup-assets-controlled-recovery`
- Branch: `codex/backup-assets-controlled-recovery`
- Status: `in_progress`
- Parent: `planning`, delivered program state `12/15`
- Migration ownership: paired `000069_backup_asset_recovery`; `000070+`
  remain reserved
- Original planning review: `complete_approved`; two independent read-only
  rereviews on 2026-07-28 found no open Critical/Important issue. The focused
  fourteenth correction is now B3 `PROVED_COMPLETE` at correction scope after
  independent specification receipt
  `019fc0c2-cfda-74e3-b218-246f3a425545` returned `APPROVED`; this is not whole
  Task 6 approval.
- `task.py start`: `executed_once` on 2026-07-28; Tasks 1
  (`000069`/model/contracts), 2 (selection/source/plan), and 3
  (target/preflight, purpose-exact SSH, and durable node-write coordination) are
  `complete_approved`. Task 3 closed its fresh Critical target-authority and
  Important cancellation/expired-lease findings with observed RED-to-GREEN
  corrections, an independent specification rereview, and a controller-inline
  quality recheck. Its original Task 3A/3B no-RED qualification remains in the
  implementation evidence. Task 4 B1 is `complete_approved`: the Provider
  missing-strength RED and real Repository immutable Rsync Catalog point changed
  RED were observed, a detached synthetic factory/fingerprint rewrite was
  rejected, production uses authenticated `request.SourceFingerprint`, and the
  real `Service.OpenCatalogRead` plus `catalog.Indexer` path passed focused
  normal/race selectors. Its independent receipts are `SPEC APPROVED` and
  `QUALITY APPROVED`; the inherited-GREEN follow-up is not a new RED. This is
  focused Task 4 evidence only: broad Provider remains blocked by host
  `IFree=0`. Task 5 is now `complete_approved` at its focused
  authorization-receipt scope after independent `SPEC APPROVED` and `QUALITY
  APPROVED: READY` receipts. Task 6 is `in_progress`; Tasks 7--10 remain
  `not_executed`.
- Latest independent Task 1 specification review: four technical Important
  gaps (Content CHECK NULL/proof/down parity, consumed-grant binding
  immutability, exact PostgreSQL F6 selector, and rejected-down mutation-arm
  trigger/function snapshot) were closed by focused observed RED-to-GREEN
  correction. The recorded focused suite passed full SQLite 000069, paired
  files, database package, model/recovery regressions, `go vet`, and the exact
  real PostgreSQL 18 `TestBackupAssetMigration069Postgres` and
  `TestRecoveryReviewF6UseLatchPostgres` selectors; the disposable service was
  removed. The final independent specification re-review and fresh
  live-worktree quality re-review both returned `APPROVED` on 2026-07-29. The
  quality re-review confirmed that the older armed-attempt finding was stale
  against the current migrations and cross-engine matrix. This is not
  Child/full-gate completion.
- Task 2 disposition: all eight focused findings are closed. Findings 1--7 have
  observed RED-to-GREEN evidence; Finding 8 was an immediate-GREEN coverage gap
  and caused no invented production change. Controller-inline specification and
  quality passes found no open finding, and the prescribed focused/full/race/
  vet/lint/static checks passed. This does not close Child/full gates.
- Historical process disposition: the original Task 1 model/state/contracts and
  original Task 3A/3B product implementations did not observe or preserve an
  executed RED before GREEN. They are controller-accepted historical deviations
  under standing user authorization, not passed TDD gates and not retroactive
  RED. Task 3C and both current product remediations have genuine RED-to-GREEN;
  the permanent PostgreSQL behavior test was immediate-GREEN coverage only.
  Every later fix and Tasks 4--10 require observed RED-to-GREEN.
- Tasks 1--3 are complete_approved at task scope. The second Task 3
  specification review's durable-exclusion races, the later deterministic
  deadline-seam correction, the fresh Critical target-authority/path-safety
  finding, and the final legacy early-cancel terminal-overwrite finding are all
  recorded with their observed RED-to-GREEN evidence. Child/full gates remain
  open. Task 4 is `complete_approved` at focused task scope with `SPEC
  APPROVED` and `QUALITY APPROVED` receipts; it makes no Child/full,
  PostgreSQL, frontend, CI, PR, or merge claim. Task 5 is `complete_approved`
  at focused authorization-receipt scope after both independent reviews. Task 6
  is `in_progress`; Tasks 7--10, staging, commit, push, PR, CI, and merge remain
  `not_executed`.
- Historical Task 5 planning record: before implementation, two independent
  read-only reviews of
  the current Task 5 contract found that generic step-up validation validates a
  JTI without durable consumption, security override/write/delete/execute lack
  a shared durable idempotency product, the ordinary audit projection is
  post-commit and details-purgeable, and encrypted reasons are not part of a
  unique full-intent contract. The controller-approved 2026-07-31 amendment
  below closes the design gap with a row kind in the existing evidence table;
  it is planning only, not RED, GREEN, implementation, test, migration, or
  implementation-evidence. A follow-up read-only review found one Critical
  receipt-lifetime gap and six Important execution-plan gaps; the present
  correction freezes proof/replay/session ordering, singleton execute lease,
  stateless runtime reaping, complete selectors/settings REDs, Web Crypto secret
  lifecycle and real-PostgreSQL rollback coverage. At that dated point this
  correction was planning only and Tasks 5--10 remained `not_executed`.
- Current Task 5 disposition: `complete_approved` at focused
  authorization-receipt scope. The receipt arm and paired SQL, four atomic
  receipt/effect transactions, proof/session/grant lifetime ordering, exact
  operation snapshot projection, settings, stateless reaper and standalone
  runtime owner are implemented within the frozen manifest. Fresh focused
  SQLite, race and required real PostgreSQL gates pass. The surviving evidence
  distinguishes genuine historical REDs, immediate-GREEN coverage additions
  and a fixture-only clock failure; no missing RED output is reconstructed.
  Independent receipts `019fb71a-75df-7770-a17d-9b3d8647d99d` and
  `019fb73d-03b6-7111-baf3-83e1ae2e3f8b` returned `SPEC APPROVED` and
  `QUALITY APPROVED: READY`. Task 6 is `in_progress`; Tasks 7--10 remain
  `not_executed`.
- Current Task 6 disposition: B1 covers Corrections 1--3, 5 and 7--13; B1-E1
  (Corrections 1--3 and 5), B1-E2 (Corrections 7--10) and B1-E3 (Corrections
  11--13) are `complete_checked`, while the B1 aggregate remains partial. B2-E1
  and B2-E2 are `complete_checked` for Corrections 4 and 6 plus the delete row;
  the B2 aggregate remains partial pending combined/whole evidence.
  No open batch receives retroactive RED/review credit. B3 covers the
  fourteenth unresolved-remote-outcome correction and is `PROVED_COMPLETE` at
  focused scope only. F6 live-mutation-permit is `complete_approved` at focused
  scope only and gives no credit to F3, B1/B2, F4, whole Task 6, Child, delivery
  or full gates. F3 and Task-6-owned F4 are also `complete_checked` at their
  focused scopes. Unchecked execution items, whole gates and whole reviews
  remain open. The
  original review is F1--F8; there is no Finding 9.
- Exact planning scope: 9 current paths
- Exact future manifest: 55 create + 81 modify
- Exact current union: 145 unique, disjoint paths
- Dirty accounting at this amendment: 82 actual paths = 80 manifest paths plus
  unrelated `go.mod` and `recovery/testdata/rsync_local_to_remote.json`. The
  similarly named future-create path under `backend/internal/backupasset/recovery`
  was absent; no path was moved, deleted or admitted to scope.

The user's standing instruction authorizes the controller to resolve routine
technical choices and continue the approved program without repeatedly asking
for permission. Planning review and the exact-manifest preflight closed before
the single successful `task.py start`; this document remains a contract rather
than product implementation evidence.

## 2. Design principles

1. **Freeze before authority.** Recovery never executes a mutable query,
   `latest`, inferred locator or client path. Plan, preflight, grant and job all
   bind the same exact source and target revisions.
2. **Default to isolation.** A new owned directory under a probed remote safe
   root is the ordinary destination. In-place recovery is a separate high-risk
   phase with explicit impact and grants.
3. **Separate outcomes from cleanup.** Plan authorization, job execution and
   plaintext result lifecycle are independent state machines.
4. **Fence every writer.** Source leases protect Provider facts; node-write and
   result-cleanup leases protect remote mutation. Stale attempts may observe but
   cannot mutate.
5. **Provider owns portable contracts; Repository owns managed-Rsync
   resolution; Recovery owns target safety.** Provider exposes closed
   request/result/ref/error contracts without Repository/Recovery imports.
   Repository revalidates and pins the managed Rsync source; Recovery supplies a
   bounded target writer and never branches on executor strings.
6. **Reuse closed foundations.** Existing lease, RBAC, step-up, audit, keyring,
   Content delivery and runtime transition contracts are extended through their
   frozen seams rather than duplicated.
7. **Separate every mutation authority.** Closed target mode, operation/delete
   digests, security decision and one-use write/delete authorities are durable
   products, not UI warnings or executor flags.
8. **Publish only terminal verified plaintext.** Execution-owned workspaces are
   unavailable to Content until an isolated job reaches its publication barrier;
   cleanup always reacquires node-wide writer exclusion.
9. **Make authorization a durable receipt, not a best-effort audit.** Each
   high-risk endpoint has one immutable receipt that atomically binds a fresh
   proof consumption, presenting login session, idempotency intent and exact
   durable effect. Ordinary audit projection is useful telemetry, not the
   authority or replay ledger.

## 3. Layer boundaries

```text
React recovery wizard
  -> typed backup-recovery API boundary
  -> thin Gin recovery handler + Auth/RBAC/ownership/step-up
  -> recovery Service (plan/preflight/authorization receipt+effect/job intent)
  -> recovery Worker (claim/fence/execute/verify/reconcile)
       -> Repository/Catalog source-revision validator + managed-Rsync RestorePort
          -> Provider closed RestorePort/ref/error contracts
          -> fileaccess pinned strict no-follow source tree
       -> remote TargetPort (probe/write/verify/cleanup)
       -> LeaseService + recovery node-write lease
  -> RecoveryResultSet lifecycle
       -> Content recovery-result adapter
       -> remote result reader / cleanup TargetPort
```

Package rules:

- `backupasset/recovery` imports root backupasset domain, provider ports and
  model/GORM dependencies, but never Gin, React or Repository contracts.
- `backupasset/provider` owns closed portable request/result/ref/error contracts
  and the kind-checked registry. It does not import recovery, runtime,
  Repository, Task or API packages.
- `backupasset/repository` imports Provider contracts but not Recovery. It owns
  the concrete managed-Rsync descriptor resolver and `RestorePort`.
- `backupasset/runtime` is the only composition root beneath `cmd/server`; it
  injects Repository's resolver/port and Recovery's fenced target writer.
- handlers parse/map/respond only; all state transitions, ownership revalidation
  and transactions live in the recovery service.
- frontend wire types remain private to `backup-recovery-api.ts`; components
  consume closed camelCase domain unions.

## 4. Domain contracts

### 4.1 Source revision

```go
type SourceRevisionKind string

const (
    SourceRevisionImmutable  SourceRevisionKind = "immutable"
    SourceRevisionObservation SourceRevisionKind = "observation"
)

type ObservationRevision struct {
    SourceFingerprint   string
    CatalogGenerationID string
    ObservedAt          time.Time
}

type ImmutableSourceRevision struct {
    LocatorDigest  string
    ManifestDigest string
}

type SourceRevision struct {
    Kind               SourceRevisionKind
    Immutable          *ImmutableSourceRevision
    MutableObservation *ObservationRevision
}
```

Exactly one union arm is populated. Immutable points bind the existing opaque
RecoveryPoint reference plus
`SHA-256("xirang/recovery/source-locator/v1" || length-framed repository ID ||
provider kind || recovery point ID || exact locator)` and the manifest digest.
`mutable_head` requires the full observation tuple, and every selected Catalog
row must belong to that generation. Authority products copy only this closed
revision and its domain-separated digest.

If an exact locator copy is needed for execution immutability, the model field is
encrypted at rest and tagged `json:"-"`; handlers, audits and frontend DTOs never
receive it. For Rsync, Recovery carries only a scalar `RsyncRestoreSourceRef`
containing the plan binding digest, repository/point/catalog IDs, selection,
source-revision and manifest digests. Only Repository decrypts the current/copy
locator, recomputes `ImmutableLocatorDigest` after RecoveryPoint revalidation,
derives the producing task from the point, and resolves a pinned fileaccess
descriptor. The ref and every Provider public contract exclude root, locator,
task ID, marker, ciphertext and identity facts. A ciphertext/row/RecoveryPoint
substitution therefore fails before Provider I/O.

The Repository validator exposes both ordinary and caller-transaction methods.
Authority transactions call the Tx method while holding the plan/job row lock,
so a Catalog/source change cannot commit between validation and transition.

### 4.2 Target binding and eligibility

```go
type TargetMode string

const (
    TargetModeIsolated TargetMode = "isolated"
    TargetModeInPlace  TargetMode = "in_place"
)

type TargetBinding struct {
    Mode                    TargetMode
    NodeID                  uint
    RootID                  string
    EncryptedRelativePath   string `json:"-"`
    RootLocatorDigest       string
    PathDigest              string
    BaseNodeRevision        string
    CredentialScopeRevision string
    RootRevision            string
    FilesystemRevision      string
}
```

Eligibility policy is intentionally broad enough for disaster recovery: any
non-archived registered node may be selected after exact preflight; the producing
node is only a UI default when it also passes. The service does not persist raw
node credentials. Configured root locators are sensitive settings encrypted by
`settings.Service`; `RootID` resolves them only inside the target/session
boundary. The root locator and relative path use separate
`xirang/recovery/target-root/v1` and `xirang/recovery/target-path/v1`
domain-separated digests. Responses expose safe labels and opaque IDs only.

`RootID` resolves a Foundation-configured safe root. Preflight establishes:

- node ownership/visibility, online status and exact credential purposes;
- required source/target tools and Provider capability revision;
- canonical root path, device/mount identity, owner/mode and non-symlink state;
- an existing non-world-writable parent controlled by the selected SSH user;
- target absence or exact conflict facts, free bytes/inodes and reserved margin;
- no overlap with source repository, Xirang data/log/cache/export roots or
  another active node-write lease;
- security/policy findings and the complete bounded impact counts.

The design assumes the selected node credential is trusted to mutate its target.
Agentless SFTP/SSH cannot defend against that same trusted principal maliciously
swapping its own directory between checks. Safety therefore requires the root
to be non-world-writable, job directories to be atomically created mode `0700`,
and every write/delete boundary to revalidate root identity, ownership marker,
component `lstat` and fence before and after mutation.

### 4.3 Plan binding

```go
type ConflictPolicy string

const (
    ConflictFailOnConflict    ConflictPolicy = "fail_on_conflict"
    ConflictSkipExisting      ConflictPolicy = "skip_existing"
    ConflictOverwriteSelected ConflictPolicy = "overwrite_selected"
    ConflictExactMirror       ConflictPolicy = "exact_mirror"
)

type AuthorityCategory string

const (
    AuthorityWrite             AuthorityCategory = "write"
    AuthorityExactMirrorDelete AuthorityCategory = "exact_mirror_delete"
)

type SecurityDecisionKind string

const (
    SecurityDecisionAllowClean    SecurityDecisionKind = "allow_clean"
    SecurityDecisionBlock         SecurityDecisionKind = "block"
    SecurityDecisionAdminOverride SecurityDecisionKind = "admin_override"
)

type SecurityDecision struct {
    Kind                  SecurityDecisionKind
    FindingSetDigest      string
    PolicyRevision        string
    OverrideBindingDigest string
}

type PlanBinding struct {
    SchemaVersion      int
    PlanDigest         string
    SelectionDigest    string
    RepositoryID       string
    RecoveryPointID    string
    SourceRevision     SourceRevision
    Target             TargetBinding
    ConflictPolicy     ConflictPolicy
    OperationSetDigest string
    DeleteSetDigest    string
    CapabilityRevision string
    SecurityDecision   SecurityDecision
    PreflightRevision  string
}
```

The preflight produces a bounded ordered operation product. Each row has a
closed operation kind (`create|overwrite|skip|delete`), the exact source
AssetRef when applicable, an exact canonical `target_relative_locator`, a
`SemanticTargetDigest`, expected pre-mutation target identity and bounded safe
display class. In-place locators are target-root-relative; isolated locators are
deterministic workspace-relative suffixes. The semantic digest binds mode, the
exact root product and that canonical item locator; it is not the later final
object digest. Rows are sorted by canonical byte order, length-framed and hashed under
`xirang/recovery/operation-set/v1`; delete rows are independently hashed under
`xirang/recovery/delete-set/v1`. Duplicate target locators, unbounded expansion
or a delete row outside `in_place + exact_mirror` rejects the whole preflight.
The delete digest is the canonical empty-set digest when deletion is forbidden.
Every operation, including delete, carries its own locator; neither a singular
plan locator nor a plan-item field can fill a missing row locator.

The exact rows are not returned through the public API and are never entrusted
to browser persistence or a client echo. The existing preflight row owns one
versioned, encrypted canonical operation snapshot. Before a preflight commit,
the service rebuilds both operation/delete digests and impact totals from that
snapshot; after every load, authorization repeats the same rebuild before any
grant consumption, lease acquisition or job insert. Missing, malformed,
noncanonical or digest-mismatched ciphertext is a closed preflight conflict.
This adds no table and no later migration number.

At execute commit, the frozen rows are projected structurally into existing
`backup_asset_recovery_job_items`. Each item persists operation kind,
`SemanticTargetDigest`, the distinct final-object `TargetObjectDigest`, exact
row-bound encrypted locator and explicit key/cipher versions, expected-prior/
post presence/digest/byte facts, display class and estimated bytes. A non-delete
row must reference the exact plan item and source row selected by its AssetRef;
a delete row has no source and therefore must have a null plan-item/source-row
reference. Paired CHECK/FK/trigger/index contracts enforce those mutually
exclusive shapes and unique semantic/final target digests per job. The worker
consumes only this durable projection; it never guesses `create`, reconstructs
an operation from aggregate digests or substitutes a plan-level locator.

`SecurityDecisionBlock` is the default for any finding. `allow_clean` requires
an exact known-clean finding/policy product. `admin_override` is accepted only
for a known policy category explicitly marked overridable and binds the exact
finding-set/policy revisions, Admin/session, a separately encrypted nonempty
override reason, reason digest and a newly validated `asset.recover` proof.
Unknown findings and non-overridable categories have no transition out of
`block`. The write authority binds the complete security-decision digest; a
finding or policy change invalidates both decision and authority.

Plan creation uses a bounded opaque idempotency key stored as a
requester/endpoint/domain-separated digest. Same key + same intent returns the
same plan; same key + different intent returns a stable conflict. Intent includes
target mode, operation/delete digests, security decision and authority category.
Directory selection expands to explicit items before the plan becomes
preflight-ready.

### 4.3.1 Authorization-receipt contract

Plan-create idempotency remains the Task 2 plan-table product. It is not proof
consumption and is not reused for the four Task 5 mutations. Those mutations
have this closed operation product:

```go
type AuthorizationReceiptOperation string

const (
	AuthorizationReceiptSecurityOverride AuthorizationReceiptOperation = "security_override"
	AuthorizationReceiptWriteAuthorize   AuthorizationReceiptOperation = "write_authorize"
	AuthorizationReceiptDeleteAuthorize  AuthorizationReceiptOperation = "exact_mirror_delete_authorize"
	AuthorizationReceiptExecute          AuthorizationReceiptOperation = "execute"
)

// ReceiptCategory is deliberately separate from a grant's authority category:
// execute consumes the write grant, while security override has no grant at all.
// The persisted receipt category is always one of these four closed values.
type AuthorizationReceiptCategory string

const (
	AuthorizationReceiptCategorySecurityOverride AuthorizationReceiptCategory = "security_override"
	AuthorizationReceiptCategoryWrite             AuthorizationReceiptCategory = "write"
	AuthorizationReceiptCategoryExactMirrorDelete AuthorizationReceiptCategory = "exact_mirror_delete"
	AuthorizationReceiptCategoryExecute           AuthorizationReceiptCategory = "execute"
)
```

An `authorization_receipt` is a private evidence-row arm, never an API DTO. It
has `plan_id` as its subject root, optional subject job/checkpoint linkage where
the operation requires it, requester ID, the exact closed operation and
authority category, canonical endpoint template, idempotency-key digest, full
intent digest, domain-separated one-way step-up-JTI digest, proof expiry, and
the independently captured presenting login-session binding. The latter is the
`middleware.CurrentSessionBinding` JTI transformed under a separate one-way
domain, together with user ID, role, token version and login-session expiry.
It is deliberately not an assertion that the generic step-up JWT was minted in
that login session: the current generic proof has its own JTI and only proves
its action/user/role/token-version/expiry.

The receipt also records `replay_expires_at` and exact private effect
references. For a security override these are plan ID plus expected and resulting
transition revision. For write and delete issuance they are plan/job/checkpoint
as applicable plus exactly one new grant ID and its binding digest. For execute
they are consumed write-grant ID, unique job ID, initial attempt ID, exactly one
non-null `recovery_job` source-lease ID plus its singleton binding digest, target
node-lease ID/fence, and resulting plan transition revision. The singleton lease
must bind the plan's sole RecoveryPoint, job and initial attempt; an omission,
additional lease or cross-owner substitution is invalid. A receipt whose
operation, category, subject linkage, or effect arm does not match its
CHECK/FK/trigger contract is invalid rather than a substitute for an effect.

All receipt digest inputs are length-framed and use distinct domains:

```text
xirang/recovery/authorization/idempotency-key/v1
xirang/recovery/authorization/intent/v1
xirang/recovery/authorization/step-up-jti/v1
xirang/recovery/authorization/presenting-session/v1
xirang/recovery/authorization/reason/v1
xirang/recovery/authorization/grant-secret/v1
```

The full intent includes operation/category/endpoint, subject and expected
revision, canonical plan/job/checkpoint/fence/revision bindings, a one-way
reason digest, and the appropriate grant-secret hash. It does not contain raw
reason text, raw proof, either raw JTI, or raw bearer secret. A `write` or
`exact_mirror_delete` secret is a client-generated 32-byte CSPRNG value encoded
as exactly 43 unpadded base64url characters; the server decodes and re-encodes
to enforce canonical shape, then persists only the category+subject-bound
`grant_hash`. A changed secret changes intent. Security override has no secret.

The service first computes requester/endpoint/key/intent from normalized input
and performs a bounded receipt lookup. A same-key, same-intent receipt may be
replayed only by the same current presenting session and only before
`replay_expires_at`; the already durable effect is read back, not performed
again. This lookup occurs before new-proof validation so a client can recover a
lost response even after the original proof expires. A same key with a different
intent returns a stable idempotency conflict. Only a no-receipt path validates a
fresh exact `asset.recover` proof, derives its JTI digest, and attempts the
transaction. A globally occupied proof digest that is not that same receipt
returns the safe `proof_used` conflict, including cross-plan, cross-category and
cross-endpoint use. No proof is consumed by a failed/rolled-back transaction.

`replay_expires_at` is derived from a bounded Recovery setting and must satisfy
`proof_expires_at <= replay_expires_at <= presenting_session_expires_at`.
Write/delete issuance additionally requires the new grant expiry to be no later
than the receipt replay expiry. The service rejects the no-receipt request before
effect creation or proof consumption when the current session remainder or
atomic settings snapshot cannot satisfy that ordering; it never shortens the
receipt beneath a still-valid proof or grant. The replay window may therefore
outlive proof expiry for lost-response recovery, but never the presenting login
session. Retention runs only after every applicable proof/replay/grant deadline.
It never scans reason plaintext and never deletes by plan or job cascade. Before
expiry every receipt update and delete is rejected; after expiry deletion must
still satisfy the receipt-specific linkage guard. Thus a receipt is durable
authorization/audit evidence for its replay window without reopening a proof or
converting ordinary expired recovery data into indefinite personal-data
retention.

### 4.4 State machines

Plan:

```text
draft -> preflight_ready -> authorized -> executed
  |           |                |          |
  +-> canceled/superseded/expired <-+     +-> superseded (guarded pre-mutation only)
```

Only `draft` may accept a new preflight snapshot. Any revision drift supersedes
the old plan; it is never silently refreshed under an existing grant. The sole
`executed -> superseded` edge requires one durable job, the current attempt
fence, zero mutation-arm/checkpoint evidence and exact proof that the target is
still at its base revision. It atomically terminates the job as
`failed/pre_write_drift`, revokes unused authorities and releases source/node
leases. Once mutation is armed or remote state is ambiguous, this edge is
illegal and the job verifies or becomes `needs_attention`.

Job outcome:

```text
queued -> running -> verifying -> succeeded|degraded
   |         |            |      -> needs_attention|failed
   +-> cancel_requested -> canceled|needs_attention
```

`needs_attention` is terminal for automated writes. It records partial/unknown
target state and requires operator review; it is not retried as a new source
version. Cleanup never changes these outcomes.

Result set lifecycle:

```text
ready -> revoking -> cleaned
           |
           v
     cleanup_failed -> revoking

revoking -> revoking (expired-owner takeover with a fresh attempt/fence)
```

`cleaned` is terminal. A current-owner cleanup failure moves
`revoking -> cleanup_failed`; `cleanup_failed -> revoking` is the only retry
edge and always allocates a fresh cleanup fence. An expired owner may CAS
`revoking -> revoking` without changing the public state, but must allocate a
fresh cleanup attempt/fence and resume from durable phase evidence.

The public union does not gain WIP states. Before publication, the job owns an
internal workspace phase:

```text
none -> reserved -> marker_created -> writing -> sealed
  -> published | cleanup_due -> workspace_cleaned
```

The phase product separates immutable identity from remote reservation.
Execute inserts an isolated job at `none` with its already
preallocated generic-encrypted `jobs/<opaque>` workspace locator and immutable
`WorkspaceBindingDigest`; marker, owner, fence and deadline are still empty.
`PrepareFirstWrite` locks that exact row and changes `none -> reserved` by
filling marker binding, owner/fence and immutable absolute plaintext deadline
before the first restored byte, without rewriting the locator or binding digest.
An in-place job at `none` keeps every workspace field empty. Temporary names
are never durable result rows. Only `isolated` jobs in the non-mutating
terminal outcomes `succeeded|degraded` may transition `sealed -> published` by
atomically inserting one `ready` ResultSet and its verified regular-file/report
rows. `failed|canceled|needs_attention` goes to `cleanup_due`; partial files are
never published. `in_place` never creates a ResultSet or RecoveryResultRef.

Cleanup attempts persist a closed phase
`claimed|revoked|drained|validated|delete_started|deleted|tombstoned` plus owner,
lease expiry, node-lease ID/fence and cleanup fence. Every phase transition is a
CAS under both current fences, so a crash before `cleanup_failed` is written is
recoverable after lease expiry.

### 4.5 Durable schema

Paired `000069` introduces twelve tables:

| Table | Responsibility |
|---|---|
| `backup_asset_recovery_plans` | requester/idempotency, target mode, binding/operation/delete/security digests, encrypted target/reasons, plan state/revision |
| `backup_asset_recovery_plan_items` | canonical ordered AssetRefs, source entry facts, intended relative result name |
| `backup_asset_recovery_preflights` | immutable expiring target/source/capability snapshot and safe impact summary |
| `backup_asset_recovery_grants` | hash-only one-use write/delete authority, actor/session/category/revisions/reason/expiry/consumed state |
| `backup_asset_recovery_jobs` | preallocated immutable job/workspace identity, one-job-per-plan, outcome, workspace binding/deadline/phase, transition revision |
| `backup_asset_recovery_job_items` | preallocated item identity, separate semantic/final target digests, recovery-local locator ciphertext/versions, operation facts/outcome, verification and safe failure category |
| `backup_asset_recovery_attempts` | worker owner/attempt/fence/lease/heartbeat/takeover and mutation-arm state |
| `backup_asset_recovery_checkpoints` | ordered operation/delete-authority evidence and mutation-aware target revision chain |
| `backup_asset_recovery_evidence` | final sanitized verification/difference facts, immutable authorization-receipt arms, plus the distinguished permanent `schema_use_latch` row |
| `backup_asset_recovery_result_sets` | published target marker binding, TTL/retain and cleanup owner/lease/phase/fences |
| `backup_asset_recovery_results` | opaque result ID, owned regular-file/report metadata and encrypted relative locator |
| `backup_asset_recovery_node_leases` | durable target-node writer exclusion, owner/attempt/fence/expiry/release |

Existing Content delivery tables are altered only to activate the already
reserved mutually exclusive RecoveryResult FK/check arm. Tickets/grants remain
owned by the Content plane; no thirteenth recovery delivery ledger is created.

The evidence table gains no new table or migration number. Its closed row kinds
become `verification|difference|failure|authorization_receipt|schema_use_latch`.
The receipt arm has a private plan root; optional job/checkpoint/grant/attempt/
single-source-lease/node-lease effect columns; requester, operation/category, endpoint,
idempotency/intent/proof/session digests; proof/session/replay deadlines; and
the exact plan-transition/effect references from §4.3.1. Normal evidence retains
its job-bound outcome fields; the fixed latch remains the only latch arm. CHECKs
make all three shapes mutually exclusive, require a nonempty proof digest only
for a receipt, bind every operation/category to its allowed subject/effect arm,
and prohibit arbitrary JSON/list/blob substitutes for source-lease references.

Paired SQLite/PostgreSQL partial unique indexes enforce exactly one
`(requester_id, endpoint, idempotency_key_digest)` receipt and exactly one
nonempty receipt `step_up_jti_digest` globally. Indexed bounded reads use the
first key plus `intent_digest,replay_expires_at`, the proof digest, and a
`kind,replay_expires_at` retention index; no reason ciphertext or free text is
indexed. Foreign keys and receipt-specific insert guards require the referenced
plan/job/checkpoint/grant/attempt/lease rows to be the same effect. The execute
arm requires exactly one source lease whose holder is `recovery_job` and whose
RecoveryPoint/job/initial-attempt ownership matches the receipt; all other arms
require that source-lease column and singleton digest to be null. Paired
owner-first partial maintenance indexes on the existing lease table cover
`(holder_type,owner_id,attempt_id,recovery_point_id,id)` for this exact
`recovery_job` linkage without changing the lease contract. Paired
`proof_expires_at <= replay_expires_at <= presenting_session_expires_at` checks
and write/delete grant-expiry linkage guards prevent early proof/grant reopening,
while immutable UPDATE and pre-expiry DELETE triggers also stop parent cascades
and direct SQL from severing linkage. A receipt reaper can delete only its own
expired and fully unlink-safe row with the database's UTC clock and bounded
`(replay_expires_at,id)` keyset predicate; it cannot touch normal evidence or
`schema_use_latch`.

Cross-field SQL checks enforce known schema/state/mode/policy/security/authority
values, canonical ID and digest lengths, exactly one source revision arm,
item/job/plan identity parity, authority-category bindings, one job per plan,
deadline order, nonnegative counts/bytes, consumed-grant and receipt immutability,
active lease uniqueness, terminal/publication invariants and result lifecycle
consistency. Maintenance indexes cover claim/reconcile/expiry, plan
requester+idempotency, receipt lookup/proof/reaping, node lease, job item,
result expiry/cleanup and Content recovery-result lookups.

For Task 6, paired SQL additionally freezes job and item IDs; requires every
item to carry `SemanticTargetDigest`, `TargetObjectDigest`, locator ciphertext
and positive explicit key/cipher versions; enforces the closed operation
presence/digest/byte matrix and per-job uniqueness of both digest families; and
permits delete rows only for `in_place + exact_mirror`. An isolated
`workspace_phase=none` row requires nonempty workspace ciphertext and
`WorkspaceBindingDigest` with empty marker/owner/fence/deadline. Every
`reserved+` phase retains that same identity and requires the phase-appropriate
marker/owner/fence/deadline product. An in-place `none` row requires every
workspace field to be empty. SQL cannot authenticate ciphertext content, so
every service/worker load reconstructs and validates the full item set before
target I/O.

The twelve-table contract remains frozen. The use latch is the fixed-ID
`schema_use_latch` arm of `backup_asset_recovery_evidence`: its job FK is null,
all ordinary evidence columns are in their latch-only form, and every normal
evidence row must have a job FK. Paired SQLite/PostgreSQL CHECKs plus
BEFORE UPDATE/DELETE triggers make the distinguished row immutable; application
cleanup always excludes it. The worker commits an idempotent insert/read of this
row under current job/attempt/node fences before it marks mutation armed or calls
any mutating TargetPort method. A failed first mutation may leave the latch;
remote mutation may never precede it.

`authorization_receipt` is retention-bounded durable authorization evidence;
it is not the use latch and cannot be mistaken for normal verification evidence.
The existing `BackupAssetAuditEvent` chain remains a registered sanitized
projection whose detail segments may be purged by its established policy. The
receipt's fixed private digest/effect fields, rather than that projection, are
the non-purgeable-before-replay-expiry proof that an authorization effect
committed. Recovery does not alter the generic audit, credential-grant, or
step-up schemas/handlers to obtain this property.

Down succeeds only when all twelve recovery tables are empty, no Recovery
Content reference/lease remains and the use latch is absent. Its first statement
guards the whole schema before dropping any constraint, trigger, index or table.
Once the latch exists, even purge-to-empty and a crash immediately after latch
commit permanently refuse down on both engines. Before receipt expiry, its
evidence row also makes the existing first-statement down guard reject atomically;
after a lawful expiry reaper removes it, down still requires every Recovery
table/Content/lease condition to be pristine. Down never deletes recovery history,
a live receipt, or the latch.

The same paired `000069` migration also extends the pre-existing `task_runs`
table with an immutable `node_id_snapshot`; it does not create a thirteenth
Recovery table or consume `000070`. Existing rows are backfilled from their
current Task node during migration. New writing runs freeze the selected node
inside the shared node-boundary reservation transaction. Paired constraints or
triggers reject a missing/changed snapshot after insert, and down removes only
this additive column and its guards after the ordinary pristine Recovery down
guard admits the downgrade.

## 5. Transactions, fences and lock order

The global order is:

```text
plan/job -> grant -> source RecoveryPoint -> source lease
-> target node lease -> attempt/checkpoint -> result set/result -> Content grant
```

Create/authority/execute transactions never hold a database transaction while
performing SSH, Provider reads, audit projection sink calls, or client-secret
handoff. A new-effect transaction writes all concrete effect rows first, then
the immutable receipt with their exact references, and commits them together;
any receipt unique/linkage failure rolls the whole effect back. The receipt is
not a lock-order participant because it is never updated: a bounded preflight
lookup chooses replay/conflict before a new-effect transaction, and the paired
unique indexes arbitrate a no-row or cross-plan proof race at insert time.

### 5.0 Receipt preflight, proof/session composition and races

The recovery handler—not frozen generic middleware—obtains the authenticated
`middleware.CurrentSessionBinding`, normalizes the body and derives an endpoint
template/key/intent. It first calls the receipt replay service. A matching
receipt requires the same requester and presenting-session digest/token version/
role, is checked against the current authenticated session expiry, and returns
only its stored durable result. The route may therefore replay after the original
proof expired; it neither validates nor consumes a new proof on that branch.

For a no-receipt branch the handler calls the unchanged exact-action generic
proof validator and passes its claims plus the separately captured session
binding to Recovery. It must not call `RequireStepUp`/`EnforceStepUp` for these
four mutations because those helpers emit an independent pre-effect audit and
cannot make receipt replay conditional. This is a recovery-handler composition
rule, not a change to generic step-up behavior. The handler verifies that both
facts refer to the authenticated requester and current role/token version, but
does not compare the proof JTI with the login-session JTI or assert any issuance
relationship between them.

Before opening the effect transaction, the service computes the candidate
replay deadline from one atomic settings snapshot and rejects unless the actual
proof expiry, applicable write/delete grant expiry, and current presenting-
session expiry satisfy §4.3.1. This failure consumes no proof and creates no
effect or receipt.

On receipt insert unique failure, the service rolls back and rereads by
requester/endpoint/key and proof digest. Same key+same intent+same session is a
replay; same key+different intent is `idempotency_conflict`; a different receipt
holding the proof digest is `proof_used`. A lost race never exposes the other
plan/job/category, reuses a proof, or leaves a partial effect. An expired
receipt returns a stable replay-expired conflict until its guarded reaper
removes it; callers must then choose a new idempotency key and fresh proof.

After a successful commit, Recovery may invoke the existing registered
sanitized `AuditWriter` projection with only opaque effect IDs and closed
operation/outcome. That write is deliberately outside the effect transaction
and may be details-purged later. If it fails after commit, the response remains
the committed durable effect/receipt result (with no raw error); no rollback,
second authorization, grant creation, grant consumption, job creation, or lease
creation is allowed. The receipt is the authoritative audit/replay record for
this operation.

### 5.1 Security decision and write authority

Clean plans carry `allow_clean` directly from the exact preflight. A blocked
finding cannot reach write authorization. For an overridable known category,
`POST /recovery-plans/:id/security-overrides` performs this transaction:

1. After §5.0 no-receipt proof/session validation, lock the plan and latest
   preflight and validate Admin/ownership, expected revision and a separate
   nonempty override reason.
2. Revalidate the exact finding-set and policy revision and require the category
   to be explicitly overridable; unknown/non-overridable products return a
   policy denial without mutation.
3. Encrypt the reason in its owning plan field, derive its one-way intent input,
   persist the complete allowed `admin_override` binding, and CAS the plan
   revision.
4. Insert the `security_override` receipt with plan ID plus exact old/new
   revision effect references, proof/session digests and replay expiry, then
   commit. The post-commit projection contains only
   `security_decision=admin_override`, safe category and opaque IDs.

`POST /recovery-plans/:id/write-authorizations` is a separate operation with a
different fresh `asset.recover` proof and write reason:

1. Require a transient canonical client-generated 256-bit `grant_secret`;
   validate its unpadded base64url encoding and derive the category/plan-bound
   one-way hash before full-intent calculation. No raw value enters a model,
   audit input, log, response, or retry record.
2. After §5.0 no-receipt proof/session validation, lock plan, latest preflight
   and current security decision.
3. Validate actor/ownership, state, expiry, expected revision, presenting session and
   exact proof; revalidate source, target, capability, policy, security,
   operation-set and delete-set bindings through narrow Tx validators.
4. Insert a hash-only one-use `write` grant bound to every digest/revision,
   presenting-session digest and encrypted reason; persist only the derived
   hash, never a server-generated/recoverable bearer value. Its expiry must be
   no later than the receipt replay expiry.
5. Transition plan to `authorized` with CAS revision; insert the
   `write_authorize` receipt with the exact grant/plan-revision effect
   references; commit; then perform the non-authoritative redacted audit
   projection. Initial and replay responses return only durable grant metadata,
   so the client retains its own secret for execute.

### 5.2 Job creation

1. `POST /recovery-plans/:id/execute` receives the durable write-grant ID and
   client-retained secret, validates canonical shape, derives the expected hash
   and performs the bounded receipt replay lookup before preparing a new effect.
   Same-key+same-intent replay returns the existing job; a valid-shape
   nonmatching hash remains a generic authorization denial rather than a grant
   oracle.
2. Still outside the effect transaction, decode the canonical encrypted
   preflight snapshot, validate the complete operation/delete product and exact
   associations, then preallocate the opaque job ID, every job-item ID and, for
   isolated mode, the immutable workspace identity `jobs/<opaque>`. Allocation
   creates no DB row and reserves nothing remotely.
3. Select one explicit Active Recovery Cleanup Ownership key/version, calculate
   every `SemanticTargetDigest`, expected `TargetObjectDigest` and
   `WorkspaceBindingDigest`, and materialize all generic/recovery-local AEAD
   ciphertext in an immutable in-memory prepared aggregate. The expected final
   digest is binding evidence only; no `TargetObjectRef`, mutation permit or
   remote authority exists yet.
4. Open one effect transaction and recheck replay/proof/session. Lock, in order,
   the plan, preflight and plan items, grant, exact cleanup-key row, source/
   Catalog facts and the existing source/node/attempt resources. Recompute the
   prepared aggregate from those locked facts and require byte-for-byte equality.
   `LockActiveTx` must return the exact key/version selected during preparation;
   key drift fails before any effect mutation.
5. CAS-consume the grant as the first effect mutation. Only after that CAS
   succeeds, insert the complete job and items, exactly one source
   `recovery_job` lease, target-node lease, initial attempt, plan transition and
   execute receipt, then commit once. An isolated job is inserted at
   `workspace_phase=none` with its nonempty generic-encrypted workspace locator
   and immutable binding digest while marker/owner/fence/deadline are empty; an
   in-place job has all workspace fields empty. Paired guards prove the sole
   source lease belongs to the plan RecoveryPoint/job/initial attempt.
6. No encryption, Provider/SSH/target I/O, audit projection or remote reservation
   runs inside this transaction. Preparation failure leaves no state. Any
   transaction/commit failure rolls back both grant consumption and every
   aggregate row; a crash after commit leaves one complete-but-unreserved
   aggregate. Retry before reservation commit reuses the preallocated identity,
   and later retry/adoption never renames or reallocates it.
7. The one-job-per-plan constraint remains defense in depth, not the replay
   contract. Same-key execute replay reads the receipt and returns the unique
   durable job even after terminal state; it never consumes again or duplicates
   items, attempts or leases.

### 5.3 Worker claim and mutation

Claim uses revision/fence CAS and bounded keyset scheduling. A worker renews both
source and target leases before expiry; loss cancels its context and prevents
further checkpoints. Every target mutation writes only after checking current
job/attempt/node fence and exact source revision. Checkpoint commit repeats the
fence/source/target validation; stale work becomes zero-mutation.

After claim and before mutation arm, the worker runs one legal first-write
barrier transaction. It locks plan/job, source lease, target-node lease and
attempt in the declared order and revalidates every binding. Drift while target
still equals the base revision atomically performs the guarded
`executed -> superseded`, terminal `failed/pre_write_drift`, authority revoke,
attempt close and source/node lease release. Transaction rollback or a losing
second worker changes nothing; restart repeats the barrier. Once clean, the
worker commits the permanent `schema_use_latch` and `mutation_armed` under the
current fences. For isolated work, `PrepareFirstWrite` locks the exact persisted
job/workspace and item set, decrypts and validates the preallocated workspace
identity, and performs `none -> reserved` by adding marker binding, owner/fence
and deadline without rewriting that identity. An unexpected existing remote
directory fails closed; it is never renamed around or replaced with a new
workspace. These durable transitions complete before the first mutating
TargetPort call.

Target observations form a mutation-aware chain. Each operation checkpoint
binds the prior chain revision, operation-row digest, attempt/node fence,
verified post-mutation identity and next chain revision under
`xirang/recovery/target-chain/v1`. The worker accepts a changed target only when
it exactly verifies the intended next operation. A crash after remote mutation
but before checkpoint is reconciled by exact verify-and-adopt under a fresh
fence or becomes `needs_attention`; it is neither blindly replayed nor mistaken
for harmless external drift.

The worker never changes the source binding during retry/takeover. New source
content requires a new plan and grant.

Ordinary Task and legacy restore execution use the same durable boundary at
executor entry. In one caller-owned transaction the coordinator locks the
snapshot node, rejects an active Recovery node lease, and CASes exactly the
reserved `TaskRun` from `pending` to `running`. The CAS also requires the
immutable `node_id_snapshot` to match. Zero affected rows (including a prior
cancel), a lease conflict or any snapshot mismatch is terminal/no-executor;
neither runner may rewrite the row back to running. Recovery admission queries
`task_runs.node_id_snapshot` directly for `pending|running` writers and never
joins mutable `tasks.node_id`, so moving a Task cannot hide a live writer on its
original node.

### 5.4 Exact-mirror delete authority

An exact-mirror worker completes and checkpoints all non-delete operations,
renews its leases, observes the target again and persists a
`delete_authority_required` checkpoint bound to the canonical delete-set digest,
current target-chain revision, node/root revisions, attempt/node fences and an
immutable authorization deadline. The job stays `running` but performs no
delete while paused.

`POST /recovery-jobs/:id/exact-mirror-delete-authorizations` requires Admin,
ownership, a new exact `asset.recover` proof and a new nonempty encrypted delete
reason plus a transient canonical client-generated 256-bit delete-grant secret.
After §5.0 receipt preflight, it locks the job/checkpoint, requires the current
paused attempt/fence, validates the secret shape and persists only its
category/job/checkpoint-bound hash. It inserts the one-use
`exact_mirror_delete` grant and its `exact_mirror_delete_authorize` receipt in
one transaction and requires `delete_grant_expires_at <= replay_expires_at <=
presenting_session_expires_at`; the receipt references the exact checkpoint/
grant/fence/plan effect. Initial and replay responses expose durable grant metadata only. The
client retains the secret for the later fenced transient worker handoff in Task
6; no raw delete bearer is stored, returned, or put in an audit. Write/security
proofs, reasons and grants cannot satisfy it.

After wake, Task 6's worker receives only a transient hash-matching permit from
the client-presented retained secret; restart/lost handoff requires a same-intent
receipt replay to re-present it, never database recovery of a bearer. It then
performs another fresh remote node/root/target observation. In one transaction
it locks job, delete grant, source/node leases, attempt and checkpoint;
revalidates source, node, root, fences, target-chain and delete-set; consumes
the grant once; and appends the consumed second checkpoint. Only then may typed
delete operations begin. Drift, stale fence, wrong category, reused/mismatched
secret or expiry performs zero deletion. Missing/expired authority terminates as
`failed/delete_authority_not_obtained` when no mutation was armed/committed, or
`needs_attention` when earlier operations may have changed the target, then
releases both leases.

## 6. Closed restore contracts and managed-Rsync port

`provider/restore.go` defines a closed union, conceptually:

```go
type RestorePort interface {
    ProviderKind() backupasset.ProviderKind
    Preflight(context.Context, RestorePreflightRequest) (RestorePreflightEvidence, error)
    Execute(context.Context, RestoreRequest, RestoreProgress) (RestoreResult, error)
    Verify(context.Context, RestoreVerifyRequest) (RestoreVerifyResult, error)
    Reconcile(context.Context, RestoreReconcileRequest) (RestoreReconcileResult, error)
}
```

The registry accepts a port only when `ProviderKind()` equals the requested
registration kind. Requests contain frozen entries, bounded limits, typed target
session, conflict policy and current fence/checkpoint. They contain no Gin
actor, arbitrary shell fragment, raw credential or `latest` arm.

`RsyncRestoreSourceRef` and `RsyncRestoreSourceResolver` are portable, closed
Provider contracts. The ref has exactly the following public scalar facts: plan
ID, plan-binding digest, repository ID, RecoveryPoint ID, catalog-generation ID,
selection digest, source-revision digest and manifest digest. It has no raw
locator/root, task ID, marker, ciphertext, managed-root identity, remote path or
transport. The resolver accepts only that ref and returns an opaque bounded
declared-entry source capability, never a path or locator. The ref is serialized
only as those scalar fields and cannot mint a usable source by itself.

Before Repository can resolve that ref, the immutable managed-Rsync point must
have a real active complete Catalog. The generic Provider
`catalogReadSession.acceptEntry` owns the projection of ordinary `Entry` values
into `CatalogRecord`. Because the generic entry contract carries no proved
content fingerprint, that projection must emit an empty fingerprint together
with the closed strength `none`; it must never emit an empty strength. The
Repository sealing wrapper changes only `ProviderLocator` into
`SealedProviderLocator`, and the Catalog indexer continues to parse and reject
unknown or empty strengths. Defaulting in Repository or Catalog would hide a
Provider contract defect and is forbidden.

For Rsync, Repository implements the Provider resolver contract and owns the
concrete `RestorePort`. Before each preflight, execute, verify and reconcile
runner call it locks/revalidates the
durable plan and plan items, active complete catalog generation, exact selected
entries, point semantics, source-revision digest, manifest digest and current
`ImmutableLocatorDigest`; it then derives the producing task only from the
RecoveryPoint, decodes the existing managed committed-point request, validates
the managed root/marker/point identity, and opens a pinned strict no-follow tree.
`mutable_head`, legacy Rsync bindings, caller task IDs, caller roots and any
binding/ciphertext/point/catalog/selection/revision/manifest mismatch fail
closed. The resolver reuses private Repository parsing helpers rather than
copying `binding.go` or `rsync_publication_execution.go`.

After every runner return, Repository revalidates the pinned root, marker, point
identity and current manifest before accepting phase evidence. A post-phase
source change is not silently absorbed; after a target write it follows the
Task 6 partial-write path.

`fileaccess` supplies the pinned tree capability. On Linux it holds the opened
directory descriptor through the runner call and performs its final-tree and
regular-entry opens through `openat2` with `RESOLVE_BENEATH`,
`RESOLVE_NO_MAGICLINKS`, `RESOLVE_NO_SYMLINKS` and `RESOLVE_NO_XDEV`; unsupported
platforms return `ErrStrictUnavailable`. It never gives a caller a reconstructed
source/root path. The only source material a runner may observe is a bounded
stream of declared regular entries from that descriptor. Root, final-tree and
link swaps between validation and use are source drift, not a new source.

Provider owns a narrow typed `RsyncTargetWriter` contract, implemented by
Recovery's fenced `TargetPort` adapter and injected by Runtime. It receives no
raw remote path. The existing `RsyncBoundRemoteTarget` is not a transport and
cannot become one. A fake runner exercises data flow but cannot issue source or
target authority.

Malformed ref/union or source identity drift returns a Provider typed source-
drift error that also satisfies `errors.Is(err, ErrInvalidRestoreRequest)`.
Arbitrary resolver/runner errors map inside the port to a separate typed
unavailable error; only context cancellation/deadline preserve their original
identity. Runner stderr, raw errors, locators, paths and marker material never
cross into Recovery, audit, logging or API products. Source drift found after a
target write remains a non-success partial-write condition for Task 6, which maps
it to `ErrRecoverySourceChanged` rather than treating it as success.

- **Rsync:** explicit managed local source to fenced target writer through a
  pinned declared-regular-entry stream. It never reverses the existing backup
  direction implicitly, reconstructs a raw path or follows undeclared links.
- **Restic:** full exact snapshot ID is mandatory. Frozen includes are encoded
  without glob reinterpretation; entries are restored/dumped through the target
  session. `latest`, short ambiguous IDs and default target are rejected.
- **Rclone:** reads only the committed version prefix/manifest entries and writes
  selected targets. It never invokes destructive sync against an undeclared
  tree.

The target port offers `ProbeRoot`, `CreateOwnedJobDir`, `Lstat`,
`CreateDirectory`, `WriteAtomic`, `Verify`, `RemoveOwnedJobDir` and
`OpenOwnedResult`. Read methods take a closed observation permit. Every mutating
method takes a `TargetMutationPermit` containing the permanent use-latch ID,
job/attempt/node fences, expected target-chain revision and purpose-scoped
credential session; it rejects a missing/stale permit before remote I/O and
rechecks after bounded mutation. Shared SSH mechanics use `sshutil.NodeDialer`
so Recovery and ordinary Task writes do not duplicate scope/host-key/session
behavior. No generic command or arbitrary path method is exposed.

SSH purposes are separate for recovery preflight, write, verify, result read and
cleanup. Credential audit records purpose/stage/outcome with safe node/job IDs;
command output and raw paths are excluded.

## 7. Isolated execution and in-place phase

### 7.1 Default isolated recovery

Execute preparation has already allocated the immutable unpredictable
`jobs/<opaque>` child identity outside the effect transaction. The complete job
commit persists that generic-encrypted locator and `WorkspaceBindingDigest` at
`workspace_phase=none`; it does not create a remote directory, marker,
owner/fence or plaintext deadline. Under the current node fence,
`PrepareFirstWrite` locks and decrypts that exact persisted identity, verifies
the full job/item product, reuses it for `none -> reserved`, and fills marker
binding, workspace owner/fence and immutable absolute plaintext deadline. It
cannot generate, rename or substitute another locator. Only after that commit
and the permanent use-latch commit may `CreateOwnedJobDir` create mode `0700`
and atomically write the authenticated marker containing schema version,
installation ID, job ID, root revision and random ownership nonce. An unexpected
remote directory fails closed. The marker is HMACed by the existing Recovery
Cleanup Ownership key; no client-supplied path is accepted.

Every file is written to a same-directory temporary name with no-follow checks,
verified, then atomically renamed. Directories are explicit. Symlink/hardlink/
special entries are skipped with stable reasons until the metadata contract can
prove a safe target; no link target is invented.

Temporary names remain execution-owned and are never registered. For the Task 6
slice, verification records only exact lowercase SHA-256 content identity plus
byte count. After all mutating
attempts are fenced/terminal, an `isolated` job may publish only when its outcome
is `succeeded|degraded`: one transaction revalidates the terminal job,
workspace/marker, target-chain and absolute deadline, inserts verified regular
files/reports, creates `RecoveryResultSet{state=ready}` and marks the workspace
published. Content cannot issue before this barrier. For
  `failed|canceled|needs_attention`, all partial files are cleanup-only and the
  sanitized verification evidence stays on the job API; no ResultSet or result ref
  is created. Directory download delegates to the existing persistent ExportJob
  using the frozen source selection; the remote plaintext tree is never
  dynamically archived for delivery.

Task 6 owns only the preallocated workspace identity, `none -> reserved`
reservation, immutable deadline and cleanup-only classification in this flow.
Task 7 owns publication, Content revalidation and every externally usable result
projection; Task 6 evidence cannot close those Task 7 behaviors.

### 7.2 In-place recovery

In-place mode creates a new plan/preflight/write authority; it is not a toggle
on an already executed isolated job. Impact lists every
create/overwrite/delete/skip operation through opaque/sanitized summaries and
binds the canonical operation/delete digests. `exact_mirror` uses the separate
one-use delete authority and consumed second checkpoint from §5.4. No in-place
target path, verification file or report may be exposed through
`RecoveryResultRef`; its safe verification summary is job evidence only.

The denial contract is designed here but its implementation and proof ownership
is Task 7, together with the publication/Content boundary.

Cancellation or failure after any write preserves completed checkpoints and
enters `needs_attention` when the target cannot be proven fully reconciled.
There is no generic automatic rollback claim.

## 8. Recovery result delivery and cleanup

### 8.1 Content adapter

The Content plane changes from an asset-only request to a tagged resource arm.
Existing backup-asset validation/product behavior remains byte-for-byte closed.
For `recovery_result`:

- the recovery adapter authorizes Admin recover permission, exact job ownership,
  `target_mode=isolated`, terminal `succeeded|degraded` job, committed
  publication barrier, ready ResultSet, current result/cleanup fence and owned
  regular-file/report;
- issuance and reauthorization require an exact
  `recovery.result_download` proof bounded by session/result expiry;
- ticket issue, reauthorization and open each revalidate the terminal job,
  publication/workspace revision and current cleanup fence; the remote reader
  uses the recovery-result SSH purpose and revalidates marker, file identity,
  result size and fence before/after open;
- Range, request/byte/in-flight budgets, trusted-proxy policy, cookies, logout
  revoke, drain and redacted delivery audit reuse the Content plane;
- missing, dual-arm, symlink, special, stale-fence, revoked, tampered or
  over-budget resources collapse to safe closed products without existence leak.

Logout composition always attempts Content, Export and Recovery revocation and
returns one sanitized aggregate. Restart reconciliation denies stale recovery
result grants before re-emitting any retryable redacted audit summary.

### 8.2 Retain and cleanup

Retain locks the ready ResultSet, verifies Admin/ownership/fresh
`recovery.result_retain` proof, expected revision and a deadline no later than
the immutable hard cap. It can only extend exposure and cannot revive cleanup.
The job `plaintext_deadline` remains the immutable initial workspace anchor
persisted before the first byte; the ready ResultSet `plaintext_deadline` is the
current effective delivery deadline. Retain changes only the latter. Resolver,
reauthorization and open accept it only while it is no earlier than the initial
anchor, no later than the hard cap and still in the future; the changed deadline
is part of the publication fingerprint, so pre-retain delivery authority cannot
silently survive an extension.

Every published or unpublished remote cleanup attempt uses this order:

1. Read a bounded candidate snapshot without locking it. In one transaction,
   lock the owning job, acquire a fresh node-wide `recovery_cleanup` writer
   lease/fence, then lock the ResultSet or workspace row. CAS
   `ready|cleanup_failed -> revoking`, or take over an expired
   `revoking -> revoking`, increment cleanup attempt/fence and persist the node
   lease/fence plus phase `claimed`. A lost candidate race releases the newly
   acquired node lease in the same transaction.
2. Revoke all recovery result grants/tickets and reject new issue/retain; CAS
   phase `revoked` under both cleanup and node fences.
3. Drain/close old-fence reads within a bounded deadline and CAS `drained`.
4. Renew the node lease, resolve a fresh purpose-scoped target credential and
   revalidate root identity, non-symlink job directory, marker HMAC,
   job/result/tombstone parity, use latch and both fences; CAS `validated`.
5. Require enough lease lifetime for the bounded operation, CAS
   `delete_started`, then delete only the exact owned job directory without
   following links. Recheck both fences and persist `deleted`; lease loss during
   delete permits no further old-owner remote or DB mutation and a new owner
   verifies the exact directory state.
6. Persist `cleaned` plus phase `tombstoned`, then release the node lease on the
   success path. A current owner failure retains the marker, records
   `cleanup_failed` and releases the lease; a crash leaves `revoking` and the
   expired cleanup/node leases are reclaimed by the same sequence. Release
   errors remain durable reconciliation work and are never relabeled clean.

Node-lease acquisition is short and nonblocking. Busy nodes persist bounded
backoff/`next_attempt_at`; keyset/high-water selection advances past them so
repeated writer contention cannot starve cleanup on other nodes. Node writers
admitted after the candidate read race against the same unique lease: exactly
one side wins. An old cleanup or node fence performs zero new remote/DB mutation.

Startup reconciliation keyset-scans due ResultSets, unpublished
`cleanup_due` workspaces and orphans. It resumes from each durable phase,
including every crash before/after revoke, drain, validation, delete and
tombstone. It never deletes unknown, invalid-HMAC or DB-unmatched directories;
those are quarantined and audited for manual review.

Task 7 owns `revoking` takeover and the fresh cleanup node-lease claim/renew/
release behavior. Task 6 owns only the bounded job-operation adoption and
reconciliation semantics that classify a workspace for later cleanup.

## 9. Runtime and scheduling

Runtime constructs one managed recovery graph containing service, Provider
restore registry, target port, worker, result lifecycle and delivery adapter.
Publication into Router/Content happens only after startup metadata
reconciliation; long-running restore execution belongs to `Run`, not synchronous
server startup.

Task 8 owns this startup/listener publication ordering and managed lifecycle.
Task 6 restart work may define bounded adoption/reconciliation semantics but
cannot claim this runtime wiring or lifecycle proof.

`StopAccepting` is sticky across concurrent graph startup. Shutdown order is:

```text
unpublish plan/ticket facades -> stop new claims -> cancel/join attempts
-> fence active node/source ownership -> revoke/drain delivery -> stop lifecycle
```

New job creation wakes the worker. Retry/takeover uses its own bounded deadline
scheduler, not the long GC cadence. Reconciliation uses durable keyset/high-water
scheduling so persistent failures cannot monopolize every bounded pass.

The same graph owns authorization-receipt retention even while Recovery
admission is disabled. Each maintenance tick calls a bounded service operation
whose stateless `(replay_expires_at,id)` keyset query selects only receipts that
are past proof/replay/applicable-grant deadlines and satisfy the exact deletion
linkage predicate in that transaction. Protected or invalid old receipts,
normal evidence and the permanent latch are excluded before `LIMIT`, so they
cannot fill every pass after restart; successful rows disappear and the next
pass advances without a mutable timestamp cursor. A database-wide failure fails
the bounded pass and retries later. Shutdown and schema drain cancel and join
this owner before down admission.

Feature disable and binary downgrade are different runtime operations. A normal
settings/config transition to disabled stops plan, security/write/delete
authority, job and remote-write admission and fences active attempts, while the
same binary keeps RecoveryResult revoke/drain/cleanup/orphan reconciliation
running. Retained plaintext may remain within its hard cap; that does not make a
binary downgrade ready.

The current binary exposes one Admin downgrade-readiness operation through the
existing settings transition owner. It requires a fresh `asset.recover` proof,
a bounded nonempty reason and an idempotency key, is accepted only after feature
disable, takes the process-wide transition mutex, installs a sticky downgrade admission
fence, joins mutation workers and snapshots both runtime and DB under one
generation. It reports `pristine_downgrade_allowed` only when the permanent use
latch is absent and there are zero queued/running jobs, unconsumed authorities,
active/releasable source or node leases, live attempts, non-`cleaned` ResultSets,
Recovery Content grants/tickets/streams and cleanup/orphan/reconciliation
backlog. The receipt is bound to the admission generation and is invalidated by
any re-enable. The operator may then stop this binary and apply paired pristine
down before starting a pre-Child binary.

If the use latch exists, the operation always returns
`forward_fix_only` regardless of row cleanup; there is no Child 13 mechanism
that authorizes a pre-Child binary to understand 000069. Startup/restart tests
exercise this gate in the current binary rather than relying on old code to
detect forward schema.

## 10. API contract

Proposed secured routes:

```text
POST   /recovery-plans
GET    /recovery-plans/:id
POST   /recovery-plans/:id/preflights
POST   /recovery-plans/:id/security-overrides
POST   /recovery-plans/:id/write-authorizations
POST   /recovery-plans/:id/execute
POST   /recovery-plans/:id/cancel
GET    /recovery-jobs/:id
POST   /recovery-jobs/:id/cancel
POST   /recovery-jobs/:id/exact-mirror-delete-authorizations
POST   /recovery-jobs/:id/results/:resultId/download-ticket
POST   /recovery-jobs/:id/results/retain
POST   /recovery-jobs/:id/results/cleanup
POST   /settings/backup-assets/recovery/downgrade-readiness
```

All routes are under the existing authenticated `/api/v1` group, require
`backup_assets:recover` and Admin, and perform exact ownership checks (the
settings route uses the same permission plus the existing Admin settings
boundary). Create keeps Task 2 plan idempotency. Security override, write
authorize, exact-mirror-delete authorize and execute require an endpoint-
requester-scoped bounded `Idempotency-Key`; all CAS mutations carry opaque
decimal `expected_revision` strings. The first effect requires the exact
`X-Xirang-Step-Up` proof and current middleware login-session binding. A
same-key+same-intent same-session receipt replay returns the stored result before
proof validation, so it may be retried after proof expiry without consuming a
second proof. A different current session never replays the original effect.

The write and delete authorization bodies additionally carry `grant_secret` as
exactly 43 unpadded base64url characters decoding to 32 bytes. Execute carries
the durable `grant_id` and the client-retained write secret. Delete secret use at
the later fenced handoff remains private to Task 6; no new public path is added.
Security override has no secret field. Initial and replay authority responses
contain only stable receipt/effect IDs and durable grant metadata (ID, category,
binding/expiry and safe status), never `grant_hash`, raw bearer material, proof,
JTI, reason or ciphertext. API responses otherwise use schema version 1, closed
target/security/authority/state enums, safe labels, public plan/selection/
operation summary digests and UTC timestamps. Source-locator, root-locator and
both semantic/final target digests stay internal with their encrypted values. No raw
target/source/root path, locator, credential, reason, proof, marker or command
output is returned.

Handlers map sentinel errors through standard response helpers:

| Domain condition | HTTP/product result |
|---|---|
| malformed/unknown union or revision | 400 closed validation error |
| missing/expired/wrong-purpose proof | 403 step-up required |
| role/ownership/policy denied | 403 generic forbidden |
| hidden/missing object | 404 generic not found |
| same idempotency key with different intent; receipt replay expired; stale revision, active lease, drift | 409 typed conflict |
| step-up proof already consumed by another receipt | 409 safe `proof_used` conflict without plan/category disclosure |
| same-key receipt but presenting login session differs | 403 generic forbidden |
| malformed client grant-secret encoding/decoded length | 400 closed validation error |
| valid-shape grant secret does not match durable grant hash | 403 generic forbidden |
| capacity/rate/selection bound | 413/429 closed limit product |
| dependency unavailable/offline | 503 sanitized unavailable |
| unexpected DB/SSH/Provider error | generic 500; detail only in structured sanitized logs/audit |
| ordinary audit projection fails after receipt/effect commit | committed success/replay response; closed internal metric/log only |

Swagger annotations and generated docs must reflect required fields, closed
enums, 401/403/404/409/413/429/500/503 cases and Admin security.

## 11. Frontend design

The recovery page becomes the plan/job shell. The asset workspace and bulk
actions navigate with an explicit selection handoff; route state carries only
opaque recovery point/plan/job IDs, not the entire selection or secrets.

Feature modules:

- `backup-recovery-api.ts`: raw validation, snake_case serialization, closed
  DTO mapping and opaque revision/idempotency handling.
- `use-backup-recovery.ts`: reducer/effects for create, preflight, closed
  security decision/override, write authority, execute, exact-mirror delete
  checkpoint/authority, polling, cancel, result retain/cleanup and ambiguity
  replay.
- `recovery-plan-wizard.tsx`: accessible stepper and focus/validation owner.
- `recovery-impact-panel.tsx`: bounded create/overwrite/delete/skip summaries.
- `recovery-job-panel.tsx`: outcome, item paging, verification and result-set
  lifecycle without conflating the two.

State is component/route scoped. Write, security-override and delete reasons/
proofs are separate drafts. The API boundary generates each write/delete grant
secret only with Web Crypto `crypto.getRandomValues(new Uint8Array(32))`, encodes
it to canonical 43-character unpadded base64url, and fails closed without a
CSPRNG; `Math.random` and server/replay regeneration are forbidden. Sensitive
proof and grant material stays in the smallest owning component. A network/5xx
ambiguity retry reuses the exact same endpoint key and secret. The write secret
is handed only to execute and cleared after definitive consumption; the delete
secret is handed only to the fenced delete checkpoint and cleared after
definitive consumption. Both clear on plan/job/session/context replacement and
never enter URL, browser storage, logs or serialized state. Source/target
locators, operation rows and tickets likewise never enter URL or storage.
Polling pauses while hidden, reconciles on visibility/reload and respects quiet
terminal/TTL states.

Accessibility requirements include native/Radix controls, labelled fields,
DialogTitle, visible focus, status live regions without noisy countdowns, step
focus transfer, a distinct malware-override confirmation and a second
exact-mirror delete confirmation, no hover-only information, reduced
motion, axe coverage, 200% zoom and narrow-width bounded layout. Both locale
files carry complete closed-state/failure copy.

## 12. Settings

Foundation adds typed, atomically validated keys for:

- recovery enabled (still subordinate to global `backup_assets.enabled`);
- encrypted sensitive allowed target-root locators and a non-sensitive default
  root ID/safe label;
- preflight TTL, plan/grant/idempotency limits and bounded authorization-receipt
  replay/retention TTL plus receipt maintenance cadence/batch (proof expiry and
  write/delete grant expiry never beyond replay expiry, replay never beyond the
  presenting session expiry);
- max selection items/logical bytes and impact rows;
- worker/global/user/node concurrency, claim/heartbeat/lease/retry limits;
- execution/verification absolute caps;
- result default TTL, retain hard cap, Range/read/drain limits;
- cleanup cadence/batch/lease/retry/orphan quarantine limits.

Cross-field validation requires heartbeat/renew margins below lease durations,
all TTLs below hard caps, drain below cleanup lease, selection and result byte
ceilings within global foundation limits, and every default root present in the
allowlist. It also requires the configured receipt replay window to cover every
configured write/delete grant lifetime and validates positive bounded receipt-
reaper cadence/batch; per-request service validation still rejects a near-expiry
login session that cannot cover the actual proof/effect deadline. Runtime
transitions use the existing validate -> drain -> persist ->
install/rollback protocol. Repository frozen defaults and all snapshot fixtures
must contain every new key. Settings BatchUpdate, delete/reset and config import
all pass through the same transition owner; none may persist a recovery setting
before validation/drain succeeds. Target-root locator definitions use
`Sensitive: true`, model/service encryption and redacted settings responses.

## 13. Security and privacy

- Source locator copies, configured target-root locators, target relative paths,
  write/delete/override/downgrade reasons are encrypted at rest and tagged
  `json:"-"` in models. Separate domain-separated digests support authority
  binding; locator/root/path digests remain internal rather than becoming a
  frontend correlation oracle.
- Credentials, proofs and grants remain hash-only or encrypted and purpose-bound.
- Authorization receipts persist only domain-separated proof-JTI and
  presenting-session-JTI digests, never either JTI or JWT. Their full intent
  includes a one-way reason digest and grant-secret hash; reason plaintext stays
  only in its existing encrypted owning plan/grant field and raw client bearer
  material never reaches storage. Receipt rows and their effect links are
  `json:"-"`/private service data, not an audit/API convenience DTO.
- Shell arguments are generated from provider-owned typed requests; no user
  command fragment is accepted. Remote paths are passed through the target port,
  never concatenated into an ad hoc shell command.
- Opaque IDs use fixed canonical lowercase encodings. HTML/SVG/script/archive
  content is restored as bytes but never executed or rendered by the recovery
  UI.
- Metrics use closed provider/state/outcome labels only; no repository, job,
  node, user, path, reason or error string label.
- Audit summaries contain stable opaque IDs, counts/bytes, action, stage and
  closed authority/security category only. Raw source locator, target root/path,
  their ciphertext, reasons, proofs and marker material are excluded. Generic
  server errors never expose raw DB/SSH/Provider details. The ordinary audit
  projection must not populate its raw step-up-proof/JTI fields for recovery
  receipt actions; receipt ID/effect IDs and closed outcome/category are enough.
- Model, service, handler, Swagger, structured-log, metric, audit, failure-
  evidence and frontend tests seed recognizable fake locator/root strings and
  assert they never appear, while digest substitution still fails before I/O.

## 14. Cross-engine and delivery validation

Required PostgreSQL helpers fail closed without `TEST_POSTGRES_DSN` when
`REQUIRE_POSTGRES_RECOVERY_TEST=1`. CI extends migration parity through 000069
and runs Recovery behavior with non-skipped real PostgreSQL evidence. SQLite and
PostgreSQL must agree on constraints, CAS, lock conflict products, UTC scan
locations, use-latch triggers and used/pristine down behavior. Required crash
barriers cover latch-before-mutation, job-commit/claim/first-write drift,
publication, every cleanup phase and node-lease loss on both engines where the
state transaction differs.

Task 5 additionally requires direct-SQL and service-level parity for receipt
partial uniques, operation/category/effect linkage, immutable update, parent
cascade/pre-expiry delete denial, UTC replay-expiry retention, receipt-present
down refusal and post-reap pristine down. SQLite race tests and a required real
PostgreSQL race must prove one winner for each of the four endpoint operations;
same-key/same-intent callers replay its effect, different intent conflicts, and
a proof that races across plans/categories yields one safe proof-used loser. A
fault after every effect stage but before commit must leave no receipt/effect;
the identical four-operation rollback matrix is mandatory against real
PostgreSQL, not only SQLite;
an injected ordinary audit-projection failure after commit must leave exactly
one receipt/effect and make retry a read-only replay.

The final gate includes focused normal/race suites, recovery package aggregate,
API route/RBAC/audit, runtime restart/shutdown, Content recovery delivery,
Repository/Provider regression, frontend full check and bundle budget,
`env -u NODE_ENV make check`, Docker/Worker closure and exact manifest parity.
Make-generated `backend/xirang-server` is removed before parity checks.

## 15. Rollback and compatibility

Same-binary feature disable rejects new plans, security overrides, authorities,
jobs and writes; it does not drop 000069 or stop the cleanup owner. Existing
attempts are fenced/canceled, result tickets are revoked, and isolated plaintext
remains under bounded cleanup until cleaned or explicitly retained within the
hard cap. In-place writes are never generically rolled back; evidence remains
for operator repair. Retained/non-cleaned plaintext is valid disabled state but
always a binary-downgrade blocker.

Binary downgrade uses the readiness operation in §9. A pristine, never-used
installation may prove every blocker zero, stop the current binary, apply paired
000069 down and then start the pre-Child binary. After `schema_use_latch` exists,
pre-Child binary downgrade is unsupported and forward-fix-only even if ordinary
rows are later empty. No old process is trusted to recognize 000069 or run its
cleanup owner.

Mixed-version behavior is closed:

| Combination | Required behavior |
|---|---|
| new backend, recovery default-disabled, old frontend | old UI exposes no plan flow; backend reconciliation remains active |
| new frontend, old backend | missing plan APIs map to disabled/unavailable; no legacy restore fallback |
| same-binary feature disable with retained/non-cleaned result | new authority/writes stop; revoke/cleanup continues; downgrade remains unready |
| pristine readiness receipt followed by paired down | pre-Child binary may start only after schema returns to 000068 |
| use latch present or any readiness blocker | readiness is `forward_fix_only`/blocked; old binary must not start |

Legacy restore routes stay gated and are not redirected to the new service until
Child 15 proves GA parity. No Child 14 reconnect/retention/purge behavior and no
deploy/release/default-enable claim belongs here.

## 16. Security/state review dispositions

The immutable review remains unchanged evidence of the original findings. This
design disposes of its eight Important findings as follows; implementation ownership and exact
RED-to-GREEN selectors are fixed in `implement.md` §3.1.

| Finding | Design disposition |
|---|---|
| 1. destructive authority | closed target mode; canonical operation/delete digests; one immutable evidence-table authorization receipt per security/write/delete/execute effect with proof-use/idempotency/session/effect binding; separate hash-only client-secret write/delete authorities; second fresh checkpoint; mutation-aware target chain; in-place result refs forbidden |
| 2. malware/security | default block; only known overridable category accepts Admin override with separate proof/encrypted reason/binding/audit; unknown/non-overridable remains blocked |
| 3. pre-write drift | guarded `executed -> superseded` transaction before mutation arm; terminal `pre_write_drift`, lease release, one-job replay and dual-engine crash/two-worker semantics |
| 4. plaintext/publication | durable unpublished workspace/deadline before content bytes; terminal publish barrier; partial cleanup-only; Content revalidation; phase/fence revoking takeover |
| 5. cleanup exclusion | every remote cleanup claims/renews/releases a fresh node-wide writer lease under job→node→result order; stale fences are inert and busy-node fairness is durable |
| 6. permanent use latch | distinguished immutable `schema_use_latch` evidence row preserves twelve tables, commits before every remote mutation and permanently blocks used/purge-to-empty down |
| 7. downgrade | same-binary disable keeps cleanup alive; current-binary readiness gate permits only pristine down; first use is forward-fix-only; mixed-version products fail closed |
| 8. locators | RecoveryPoint + domain-separated locator digest in authority; encrypted locator only in Repository, scalar Rsync ref in Recovery/Provider contracts, Repository-owned descriptor resolver, pinned no-follow source tree and full substitution/no-leak contracts |

Both independent Phase 1 rereviews approved these dispositions and the exact
then-current plan/manifest on 2026-07-28 with no open Critical/Important issue.
The later Task 3 snapshot correction and Task 4 B1 boundary correction moved the
prior exact union to 9 current + 55 create + 80 modify = 144 paths. The focused
Catalog completion amendment adds only tracked `provider/catalog.go`, because
the real committed managed-Rsync RED and cross-layer completion test use the
already manifested `provider/rsync_test.go` and `repository/query_test.go`.
The current union is 9 current + 55 create + 81 modify = 145 paths. At the
dated amendment, that ledger entry was not RED, GREEN, implementation evidence
or approval; `task.py start` remained a separate recorded workflow action and
Task 4 was still pending. The later focused closure is `complete_approved` with
`SPEC APPROVED` and `QUALITY APPROVED` receipts. Task 5 is now
`complete_approved` at focused authorization-receipt scope after its independent
`SPEC APPROVED` and `QUALITY APPROVED: READY` receipts. Task 6 is now
`in_progress`; Tasks 7--10 remain unexecuted.

## 17. Rejected alternatives

- **Producing node only:** rejected because it defeats disaster recovery when
  that node is unavailable.
- **Repository-scoped node allowlist:** rejected for this Child because it adds
  a new policy surface without a current product requirement; exact node/root
  preflight already provides the safety boundary.
- **Provider generic `restore latest`:** rejected because it breaks frozen
  source authority.
- **Provider-local managed-Rsync issuer or a revalidated string path:** rejected
  because arbitrary callers can forge its locator and validation cannot protect a
  root/final-tree swap before runner use. Repository-owned resolution plus a
  fileaccess-pinned descriptor is required.
- **Accept empty Catalog fingerprint strength or default it after Provider:**
  rejected because it hides an invalid Provider projection and makes the
  Repository/indexer boundary silently reinterpret evidence. Generic Provider
  entries without a proved fingerprint explicitly project `none`; Repository
  only seals the locator and Catalog keeps its closed parser unchanged.
- **Single job state including cleanup:** rejected because plaintext cleanup
  failure would corrupt the authoritative execution outcome.
- **One generic recovery grant:** rejected because write, security override and
  exact-mirror deletion have different evidence and consumption boundaries.
- **A thirteenth receipt/idempotency/audit table or `000070`:** rejected because
  the existing evidence table already has closed row-kind mechanics, paired
  `000069` is owned by this Child, and an immutable receipt arm can preserve the
  required unique/linkage/retention contract without changing the frozen
  twelve-table aggregate.
- **Generic step-up audit or post-commit audit event as proof consumption:**
  rejected because generic validation does not bind the current login session or
  durably consume a proof and ordinary audit details are retention-purgeable.
  The receipt is committed with its effect; generic handlers remain frozen.
- **Public WIP ResultSet state or partial-result publication:** rejected;
  execution owns unpublished plaintext, and unsafe terminal partials are
  cleanup-only.
- **Thirteenth use-latch table:** rejected because a SQL-distinguished immutable
  evidence row supplies the permanent invariant without changing the frozen
  twelve-table aggregate.
- **Treat feature disable as downgrade-ready:** rejected because retained or
  cleanup-owned plaintext still requires the Child 13 binary, and first use
  permanently requires a forward fix rather than pre-Child execution.
- **Reuse `backup_assets:download` for results:** rejected because restored
  plaintext has distinct ownership and step-up semantics.
- **Dynamic archive of remote result directories:** rejected; use the existing
  persistent ExportJob from the frozen source selection.
- **Auto-delete unrecognized orphan directories:** rejected because marker/key/
  DB loss must fail closed rather than delete user data.

## 18. Current Task 5 independent review closure

This later current-status ledger entry preserves the dated pending implementation
entries above. Task 5 is now `complete_approved` at focused authorization-receipt
scope only. Specification receipt `019fb71a-75df-7770-a17d-9b3d8647d99d`
returned `SPEC APPROVED` after exact Steps 7/11/12, SQLite/PostgreSQL races, the
full PostgreSQL `000069` matrix, and manifest/static/Trellis/index checks; it
confirmed the full Task 8 graph is intentionally deferred. Quality receipt
`019fb73d-03b6-7111-baf3-83e1ae2e3f8b` returned `QUALITY APPROVED: READY` with
no Critical, Important, or Minor finding after the focused/eight-package,
SQLite-race-x10, runtime-owner-x50, PostgreSQL-winner-x10/direct-SQL/rollback,
vet/format/diff/Trellis/manifest/index review.

The approval leaves the exact 9 + 55 + 81 = 145 manifest unchanged and preserves
the two unrelated exclusions, `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`. At that Task 5 closure snapshot,
Tasks 6--10 and the full Task 8 graph were `not_executed`; frontend/Child/full/
CI/delivery gates remain open. Task 6 is now `in_progress`; the Child remains
`in_progress` and the parent remains `planning`.

## 19. Task 6 restart-adoption persistence amendment

### 19.1 Status, precedence and review chronology

This 2026-07-31 amendment is controller-approved and independently `SPEC
APPROVED`. It is the later controlling Task 6 design wherever earlier worker,
checkpoint, locator or reconcile wording is less precise or conflicts with this
section. The 2026-08-01 evidence-backed clarification returned by the independent
fidelity researcher is approved by controller direction as a focused planning
amendment. It resolves the fidelity ambiguity without adding a counted product
correction. A subsequent independent read-only design pass returned `DESIGN READY`;
controller direction approved its coherent preallocation, dual-digest,
workspace, encryption, transaction and adoption clarification inside the
then-existing first thirteen corrections. The later §20 correction is the
  focused fourteenth Task 6 product correction and has since closed as B3 at
  focused scope. Task 6 remains `in_progress`: B1-E1/E2/E3 and B2-E1 are `complete_checked`,
  B1 and B2 aggregates are partial, B3 is
  `PROVED_COMPLETE_FOCUSED_ONLY`, F6 is `complete_approved`, and F3 is
  `complete_checked` at their exact focused scopes. No whole-task or delivery
  completion is claimed. Tasks 1--5 retain their approvals; B2-E2 and
  Task-6-owned F4 are now separately `complete_checked` at focused scope, while
  unchecked execution items, whole gates/reviews and Tasks 7--10 remain open.
  The original review has F1--F8 only.

The review chronology is preserved exactly:

1. The initial independent review returned 3 Critical + 2 Important.
2. The first revision was rejected because it invented a delete absence digest.
3. The corrected controlling revision removed that digest. Its next independent
   review found 2 Important: skip/source identity conflation and insert-before-
   grant ordering.
4. Both corrections were adopted. The final independent result was `SPEC
   APPROVED`.
5. The final nonblocking clarification records that skip's separately frozen
   prior-target bytes are immutable, and that the exact key/version lock remains
   transaction-scoped through grant CAS and complete aggregate insert.

The exact manifest remains 9 current + 55 create + 81 modify = 145. This design
amends the existing unshipped paired `000069` only. It adds no path, migration,
table, backfill or `000070+` allocation.

### 19.2 First thirteen controlling product corrections

1. **Expected-post persistence.** Every versioned operation snapshot row and
   every projected job item freezes one operation-specific digest/byte product.
   `create` has prior absent, a nonempty exact lowercase SHA-256 post digest,
   post bytes `>= 0`, and prior bytes `-1`. `overwrite` has prior present and an
   exact lowercase SHA-256 post digest with post bytes `>= 0`. `skip` has prior
   present, `ExpectedPostIdentityDigest = ExpectedPriorDigest`, post bytes `-1`,
   and separately frozen immutable prior-target bytes `>= 0`; those bytes are
   never source bytes. `delete` has prior present, empty expected-post digest,
   and both byte fields `-1`. The delete empty field remains length-framed in
   every canonical digest. There is no delete absence digest.
2. **Create/overwrite revalidation.** Immediately before execute or adoption, a
   freshly revalidated frozen `RestoreEntry` must equal the persisted expected-
   post digest and bytes. Any source change is source drift and performs no
   target mutation or terminal success projection.
3. **Skip source/target separation.** Skip independently revalidates the frozen
   source so the plan cannot silently change, but verifies only that the exact
   target remains unchanged against frozen prior-target digest and bytes. A
   valid skip projects `skipped`, never `succeeded`, and does not claim that the
   source identity is the target identity.
4. **Delete absence semantics.** Delete requires durable exact-mirror delete
   authority and an explicit exact `AbsentObservation`. Permission denial,
   timeout, unsupported stat, transport failure, ambiguous missing or any other
   non-proof of absence cannot satisfy it.
5. **Closed Verify product.** `TargetPort.Verify` accepts a closed
   `present|absent` expectation union and returns a matching closed observation
   union plus `ObservedRevision`. Exactly one arm is populated. Each present arm
   carries exact lowercase SHA-256 content identity and bytes `>= 0`, and the
   observation must exactly equal the expectation. Each absent arm carries
   closed exact absence evidence, never a synthetic object digest.
   `ObservedRevision` is bounded, nonempty, opaque, strong and target-derived;
   it is not SHA-256-shaped and must not be validated as one.
6. **Absence chain binding.** The target chain uses a dedicated, separately
   domain-bound encoding for the absent observation and its strong observed
   revision. The operation's expected-post field remains the length-framed empty
   semantic field; chain binding must never copy an absence value into it.
7. **Canonical semantic locator mapping.** Every canonical schema-v2 operation
   row, including delete, persists its own exact `target_relative_locator` and
   `SemanticTargetDigest`. In-place uses a target-root-relative locator;
   isolated uses a deterministic preflight-frozen workspace-relative suffix.
   The semantic digest length-frames mode, exact root product and canonical
   item locator. Aliases, normalization changes, duplicates, collisions,
   cross-item mapping or runtime rename are invalid. A plan or plan-item locator
   is never a fallback for a missing operation-row locator.
8. **Prepared workspace and distinct full-object binding.** Execute preparation
   allocates the opaque job ID, every item ID and immutable isolated workspace
   identity `jobs/<opaque>` outside the transaction in an in-memory prepared
   aggregate; this creates no row or remote reservation. It calculates a
   distinct `TargetObjectDigest` over the final root-relative object—exact
   in-place locator, or `jobs/<opaque>/<suffix>` for isolated. This expected
   digest is binding evidence only. The committed isolated job starts at
   `workspace_phase=none` with generic-encrypted workspace locator plus immutable
   `WorkspaceBindingDigest` and empty marker/owner/fence/deadline; in-place none
   has no workspace fields. `PrepareFirstWrite` must reuse that identity for
   `none -> reserved`. Only after locking the exact persisted workspace/item,
   decrypting the locator, strict-joining, and recomputing/equality-checking the
   digest may a worker construct `TargetObjectRef` and a permit.
   `TargetObjectRef.TargetPathDigest` carries only `TargetObjectDigest`, never
   `SemanticTargetDigest`.
9. **Row-bound recovery-local locator ciphertext.** Each item stores separate
   semantic/final digests, private locator ciphertext, positive
   `TargetLocatorKeyVersion`, positive local cipher version and the entire
   operation presence/digest/byte product. Recovery derives AES-256-GCM material
   with HKDF-SHA256 from `KeyDomainRecoveryCleanupOwnership` using
   `xirang/recovery/job-item-target-locator/aead/v1`. The locator column does not
   use generic model `BeforeSave`/`AfterFind` encryption hooks; only Recovery
   opens it by its persisted versions. `TargetLocatorEnvelopeBinding`
   length-frames codec/cipher versions; job/item/plan/nullable plan-item/source-
   row identities; mode/node/root/root digest; workspace identity and
   `WorkspaceBindingDigest`; separate semantic/full-object digests; operation,
   presence, digest and byte facts; and explicit key/cipher versions. Plaintext
   contains the exact canonical item locator and workspace locator. Cross-row,
   job, root, item, workspace, key, cipher or operation-product substitution
   fails before target I/O.
10. **Versioned aggregate envelope.** The existing generic `enc:v2` value is the
    encrypted preflight `EncryptedOperationRows` snapshot, not the explicitly
    versioned recovery-local item AEAD. Generic model encryption remains for
    that snapshot and the job workspace locator; item ciphertext is immutable
    and never rekeyed. Canonical schema-v2 bindings retain every locator,
    semantic digest and operation-specific digest/byte fact, including delete-
    empty framing, without a parallel fidelity field. Every load authenticates,
    decrypts, parses, canonicalizes and recomputes the whole approved operation/
    delete/item product. Snapshot decode rejects duplicate-source, policy-invalid
    and self-consistent/canonical-but-operation-invalid payloads. No caller uses
    a cached echo, partial comparison or implicit current key version, and
    `keyring.go`/`secure/crypto.go` remain unchanged.
11. **Internal-only three-boundary restart adoption.** The only API is
    `AdoptInterruptedOperation(ctx, claim, jobItemID)` and it accepts no caller
    locator, identity, revision, operation or product facts. A short DB phase
    loads/locks and decrypts the exact durable item/job/plan/attempt/workspace/
    authority product; target Verify then runs with no DB transaction held; a
    final DB phase re-locks and revalidates every fence/revision before one
    fenced CAS projects success or skipped, optionally appends sequence 1,
    advances the chain and closes the attempt. Every fact is derived from durable
    state. A validation failure before any authorized durable checkpoint yields
    zero false success, skip, checkpoint or chain advance; a stale owner or
    takeover loser cannot append or project anything new. A post-arm invalid or
    contradictory remote outcome instead follows the later controlling §20
    terminal projection.
12. **Fatal cleanup-key reconciliation and prepared grant-first creation.**
    Before runtime returns fatal `ErrKeyLost|ErrKeyUnavailable`, a bounded
    idempotent DB-only reconciliation changes only current post-arm work to
    sanitized `needs_attention|cleanup_due` and closes its attempt; pre-arm,
    terminal and stale work remains unchanged, with no target I/O/decrypt/
    checkpoint/success/skip/chain advance. Execute preparation outside the
    effect transaction performs replay lookup, snapshot decode, whole-product
    validation, association resolution, all ID/workspace allocation, explicit
    Active key selection, both digest calculations and all AEAD encryption.
    Inside the transaction it rechecks replay/proof; locks plan, preflight/items,
    grant, exact cleanup-key row, source/Catalog and existing source/node/attempt
    resources; recomputes the prepared aggregate byte-for-byte; and requires
    `LockActiveTx` to match the selected key. Grant CAS is the first effect
    mutation, followed by one complete job/items/lease/node/attempt/plan/receipt
    insert and commit. No encryption, Provider/SSH/target I/O, audit or remote
    reservation occurs inside. Preparation failure leaves no state; transaction
    failure rolls back grant plus aggregate; post-commit crash leaves a complete
    unreserved aggregate whose preallocated identity is reused. Unexpected
    remote directories fail closed without rename/reallocation.
13. **Paired immutable enforcement.** The existing paired `000069` gains all
    required CHECKs plus insert, immutable, one-way projection and checkpoint
    triggers. Isolated none requires workspace ciphertext/binding digest with
    empty marker/owner/fence/deadline; reserved+ keeps the same identity and
    requires the phase product; in-place none keeps all workspace fields empty.
    The paired contract freezes job identity, both item digests, locator
    ciphertext/versions and every operation presence/digest/byte sentinel;
    enforces unique semantic and final digests per job; binds insert facts;
    permits delete only for in-place exact-mirror; and freezes terminal product.
    It adds no independent fidelity field, permits only the documented
    `pending -> terminal` projection plus `updated_at`, and rejects terminal
    rewrite. Because SQL cannot authenticate ciphertext, service/worker loads
    reconstruct the full item set before I/O. Down runs the existing guard first,
    then drops every added trigger/function. SQLite and PostgreSQL expose the
    same insert, update, direct-SQL, pristine/down/reapply and race behavior.

### 19.2a Focused fidelity clarification

This controller-approved clarification narrows Task 6 fidelity to exact content
identity only: lowercase SHA-256 digest plus byte count. It does not claim mtime,
mode, owner, MIME or other metadata fidelity, define an independent fidelity
digest, or synthesize an absence digest. Broader metadata fidelity requires a
future source-freeze and target-observation contract amendment.

| Operation | Prior | Persisted post digest | Prior byte field | Post byte field | Accepted verification/projection |
|---|---|---|---:|---:|---|
| `create` | absent | exact lowercase SHA-256 | `-1` | `>= 0` | exact present digest/bytes |
| `overwrite` | present | exact lowercase SHA-256 | `>= 0` present fact | `>= 0` | exact present digest/bytes |
| `skip` | present | equals frozen prior digest | separately frozen prior-target bytes `>= 0` | `-1` | exact unchanged present target; `skipped` only |
| `delete` | present | empty, length-framed | `-1` | `-1` | closed exact absence evidence |

The locator clarification is likewise controlling but does not broaden
fidelity. Schema-v2 stores a canonical `target_relative_locator` and
`SemanticTargetDigest` on every row, including delete. Execute preparation
preallocates the isolated `jobs/<opaque>` identity and calculates the distinct
expected `TargetObjectDigest`; the item envelope binds both digests, the
workspace identity/`WorkspaceBindingDigest`, exact row identities and all
operation facts. Only a worker that later locks the persisted workspace/item,
decrypts, strict-joins and recomputes the expected final digest may construct a
`TargetObjectRef`, whose `TargetPathDigest` is the final-object digest only.
Decode validates the full product in its approved operation/policy context, so
an individually canonical locator/envelope substitute remains invalid.

### 19.3 Closed Verify and adoption flow

Conceptually, Verify owns this closed product; the final Go names may follow
the package's existing naming style, but the arms and invariants may not widen:

```go
type TargetVerifyExpectation struct {
    Kind    TargetPresenceKind // present | absent
    Present *PresentExpectation
    Absent  *AbsentExpectation
}

type TargetVerifyObservation struct {
    Kind             TargetPresenceKind // present | absent
    Present          *PresentObservation
    Absent           *AbsentObservation
    ObservedRevision string // bounded, nonempty, opaque, strong, target-derived
}
```

`present` requires exactly one present arm with lowercase SHA-256 content
identity and bytes `>= 0`; expectation and observation must match exactly and
there is no separate fidelity fact. `absent` requires exactly one absent arm and
explicit exact absence evidence. Unknown, permission-denied, timeout and
ambiguous-missing products satisfy neither arm. `ObservedRevision` is a bounded,
nonempty, opaque strong target-derived value, not a SHA-256-shaped digest. Delete
checkpoint material binds the absent observation under the target-chain absence
domain while leaving expected-post empty.

Restart adoption executes in this order:

1. In one short DB transaction, validate `claim`; lock the exact current
   job/attempt/item ownership rows plus immutable plan, snapshot, workspace and
   applicable delete-authority state under the declared order; select the
   persisted positive key/cipher versions; decrypt and validate the row-bound
   locator/envelope; reconstruct the full item set; and derive a bounded
   immutable adoption handoff. Isolated resolution locks the exact persisted
   workspace identity before strict-join and recomputes the expected
   `TargetObjectDigest`. Commit/close this transaction before target I/O.
2. Without any DB transaction held, revalidate the frozen source and call
   target Verify through the durable handoff. For create/overwrite, the fresh
   `RestoreEntry` post digest/bytes must equal persistence. Skip keeps source
   revalidation separate from unchanged-target prior facts. Delete requires the
   durable delete-authority facts loaded above. Accept only a strong exact
   present observation or explicit exact absent observation; no caller fact can
   replace persistence.
3. Open one final short transaction, re-lock the exact job/item/attempt/
   workspace/authority rows, and revalidate every digest, fence and revision
   against the handoff and observation. Its fenced CAS atomically performs only
   the operation-appropriate projection, optional sequence-1 append, chain
   advance and attempt close. A zero-row CAS, stale fence, takeover, pre-arm
   drift or key/version/auth failure leaves all success/checkpoint/chain fields
   unchanged. A post-arm invalid or contradictory write/observation outcome,
   including a valid observation mismatch, instead follows §20 and commits
   exactly one terminal `operation_unresolved`; source drift/failure may coexist
   with that remote-outcome category.

### 19.4 Creation, key loss and migration order

Job creation cannot use a DB insert or remote directory as a reservation.
Outside the effect transaction it performs receipt replay lookup, canonical
snapshot decode, whole-product validation and association resolution; allocates
job/item IDs plus isolated `jobs/<opaque>` identity; selects an explicit Active
key/version; calculates semantic/final/workspace digests; and completes every
generic/recovery-local AEAD encryption into one immutable prepared aggregate.
Inside the transaction it rechecks replay/proof, locks the plan/preflight/items/
grant/exact key/source/Catalog/existing resource product in order, recomputes the
aggregate byte-for-byte and requires `LockActiveTx` to match the selected key.
The one-use grant CAS is the first effect mutation, followed by the complete
job/item/source-lease/node-lease/attempt/plan/receipt insert and one commit.
Receipt and one-job constraints remain defense in depth. No encryption,
Provider/SSH/target I/O, audit or remote reservation occurs inside. Any
preparation failure leaves no state; any transaction failure rolls back grant
and aggregate; a post-commit crash leaves the complete unreserved identity for
`PrepareFirstWrite` to reuse.

Permanent cleanup-key loss is handled before the runtime crosses its fatal
startup boundary. The bounded DB-only pass may close only current post-arm
attempts and mark their jobs/workspaces `needs_attention|cleanup_due` with a
sanitized stable category. It cannot decrypt locators, contact a target, append
sequence 1, project success/skipped, or advance a target chain. Its idempotent
retry changes no pre-arm, terminal or stale/taken-over row. The original
`ErrKeyLost|ErrKeyUnavailable` remains the returned startup error.

The first-thirteen paired migration work stays inside the four existing
`000069` files. Up installs identical cross-engine shape and transition
enforcement. Down first executes the existing complete data guard and only then
removes every new trigger/function before the existing tables are dropped.
There is no backfill: the migration is unshipped and job creation must write
complete rows from the first admitted aggregate. The later fourteenth
correction is narrower: §20.5 permits edits only to the two up files and keeps
both down files unchanged.

### 19.5 Mandatory pre-production RED-GREEN matrix

Before the corresponding production, model or migration behavior is credited,
the exact failing Task 6 selectors must be preserved for:

- lowercase SHA-256 and bytes exact matching, closed present/absent union arms,
  and bounded opaque `ObservedRevision` cases including empty, oversize and code
  that incorrectly assumes a SHA-256-shaped revision;
- create/overwrite/skip/delete prior-presence, digest and byte-sentinel mutations,
  including skip post bytes other than `-1`, nonidentical post/prior digest, and
  delete nonempty post digest or either byte field other than `-1`;
- schema-v2 whole-product rejection of duplicate-source, policy-invalid and
  individually canonical/self-consistent but operation-invalid payloads;
- snapshot/envelope/ciphertext/cross-row/root/item tamper, including changes to
  isolated workspace identity/binding, semantic/final-object digests and
  operation digest/byte facts, plus
  non-echo fakes;
- wrong root/item, cross-adoption, canonicalization, collision, duplicate,
  cross-item mapping and runtime rename;
- compile/contract removal of every caller-forged locator/identity/revision/
  operation adoption input;
- key loss, unavailable/wrong key version, wrong local cipher version and AEAD
  authentication failure;
- source drift, create/overwrite post mismatch, skip unchanged-target semantics,
  explicit delete absence and ambiguous/permission/timeout missing;
- SQLite plus required real PostgreSQL direct-SQL insert/update/terminal rewrite,
  pristine down, used-down refusal, reapply and paired definition parity; and
- deterministic adoption/takeover races with exactly one fenced winner.

Every semantically pre-arm or zero-mutation negative case asserts zero
sequence-1 checkpoint, item success/skipped projection, job success,
target-chain advance, target I/O where forbidden, and plaintext locator leak.
The later §20 unresolved matrix instead forbids a current-item success/adoption
checkpoint and requires exactly one terminal `operation_unresolved`, which may
itself be sequence 1 or follow applicable earlier valid history. The same
captured RED selectors gate the corresponding GREEN. B3 has proved that focused
matrix, while B1-E1/E2/E3 have proved only the B1 arms of the first-thirteen
matrix. B1 aggregate remains partial; B2 and whole Task 6 evidence remain open,
so Task 6 stays `in_progress`.

## 20. Task 6 unresolved remote outcome correction

### 20.1 Status, precedence and chronology

This later controlling section is the fourteenth Task 6 product correction. It
supersedes any earlier worker/checkpoint wording that would turn an invalid or
contradictory post-arm write/verification result into success, blind replay, or
target-chain advancement. B3 is now `PROVED_COMPLETE` at this focused correction
scope only. Task 6 remains `in_progress`; Tasks 7--10 remain `not_executed` and
staged paths remain zero.

The B3 chronology did not credit inherited GREEN. The five selectors were
frozen after independent specification approval, only the fourteenth behavior
was taken to the authorized pre-feature baseline, genuine RED was observed, and
the final behavior was restored/fixed before the unchanged selectors proved
GREEN. The bounded final writer changed only `recovery/executor_test.go` and
`database/backup_asset_migrations_integration_test.go`; the first thirteen
Task 6 corrections were not granted RED, review or completion credit.

### 20.2 Closed checkpoint and failure products

The checkpoint phase is exactly `operation_unresolved`. It is a terminal
post-mutation-arm product. The current job and item use exactly the stable
failure category `remote_outcome_unresolved`. The unresolved category is a
closed union:

```text
revision_disagreement
verification_mismatch
write_result_invalid
observation_invalid
```

The checkpoint carries exactly these additional private facts:

| Fact | Contract |
|---|---|
| `job_item_id` | exact current item for the armed operation |
| `unresolved_category` | exactly one closed category above |
| `write_result_digest` | empty when no write result exists; otherwise a sanitized length-framed binding |
| `write_target_revision` | bounded trusted revision only for a valid returned write result; otherwise empty |
| `observation_digest` | empty when no observation exists; otherwise a sanitized length-framed binding, including invalid observations |
| `observed_target_revision` | bounded trusted revision only for a valid observation; otherwise empty |
| `observed_presence` | exactly empty, `present`, or `absent`; nonempty only for a valid observation |
| `source_revalidation_outcome` | exactly `matched`, `drifted`, or `failed` |

Every pre-existing checkpoint phase requires all eight facts to retain their
neutral empty values. An `operation_unresolved` row binds the current job,
`job_item_id`, canonical operation digest, current prior target revision,
current attempt ID/fence, node lease/fence, source fence, preflight fence,
applicable write/delete-authority fence, and the sanitized length-framed remote
facts. Its `next_target_revision` is empty. It is legal as sequence 1 for the
first unresolved operation, or after `workspace_reserved`, earlier operation,
or `delete_authority_consumed` history when that history applies. No operation,
verification, delete-authority, second unresolved, or other checkpoint for the
job may follow it; in particular the current item receives no success, skipped,
or adoption checkpoint. The job's target-chain revision remains the prior
revision, so the unresolved observation can never authorize the next target
operation.

Category validation is fail closed and field legality is exact:

| Category | Legal checkpoint facts |
|---|---|
| `revision_disagreement` | valid sanitized write and observation digests, valid write and observed revisions, and the two revisions are unequal |
| `verification_mismatch` | a valid observation; `skip` keeps every write field neutral, while `create|overwrite` carries a valid write digest and revision; the existing B2 delete contract is unchanged |
| `write_result_invalid` | only a sanitized length-framed digest of the invalid raw write result; write revision and every observation fact are neutral |
| `observation_invalid` | valid write facts when applicable plus only a sanitized length-framed digest of the invalid observation; structured observed revision and presence are neutral |

`source_revalidation_outcome=drifted|failed` may coexist with any legal remote-
outcome category. It records a separate source result and never replaces
`remote_outcome_unresolved` or its `unresolved_category`. Any other combination
is invalid rather than a fifth category. This correction does not alter the
already approved B2 multi-delete authority or evidence contract.

### 20.3 Sanitized evidence bindings

`WriteResultDigest` and `ObservationDigest` are private, domain-separated,
length-framed evidence bindings. The write binding covers only the bounded
write-result tuple; the observation binding covers only the bounded closed
observation tuple. Their purpose is to bind what the worker received without
persisting raw output.

They are not content-fidelity digests, source identity digests, target-chain
revisions, synthetic absence digests, or proof that an invalid product is true.
They never contain plaintext, raw Provider/SSH/target errors, stdout/stderr, a
locator, or a serialized raw result. Exact absence remains the prior closed
`AbsentObservation` contract; this correction does not invent another absence
identity.

### 20.4 Atomic terminal disposition

After mutation arm, the current fence owner performs one short database
transaction. It re-locks and validates the exact job/item/operation, attempt,
source lease/fence, preflight fence, applicable authority fence, node lease/
fence and prior target revision, then atomically:

1. appends the terminal `operation_unresolved` checkpoint;
2. changes only the still-neutral current item to
   `failed/remote_outcome_unresolved`;
3. changes the running job to
   `needs_attention/remote_outcome_unresolved`;
4. inserts sanitized failure evidence bound to the new checkpoint and current
   job/item/attempt/node-fence facts;
5. closes the attempt as failed; and
6. releases the source and node leases.

Any failed guard or write rolls back the complete disposition. The transaction
does not append a current-item success, skipped, or adoption checkpoint, does
not project the current item or job as success, does not change any prior item's
succeeded/skipped facts, does not rewrite success/skipped fields, and does not
advance the target chain. Exactly one `operation_unresolved` checkpoint is
committed. Existing isolated-workspace cleanup-only handling remains governed
by the first thirteen corrections; this amendment does not authorize another
remote mutation, continued write, or automatic replay.

### 20.5 Paired migration and exact RED-GREEN gate

Only the two existing unshipped SQLite/PostgreSQL
`000069_backup_asset_recovery.up.sql` files may be amended for this correction;
both down migrations remain unchanged. Their checks/triggers must enforce the
closed phase/category/value sets, neutral old-phase arms, exact current
row/fence/revision binding, legal predecessor history, empty next revision,
terminality, failure projection and no-chain evidence relationship on both
engines. The remaining implementation is confined to the already manifested
model/state/executor/worker and test paths. There is no new path, table,
migration, backfill, `000070`, target/contracts interface, keyring domain, or
cryptographic primitive.

The exact frozen selectors are:

```text
TestStateOperationUnresolvedProductsAreClosedAndTerminal
TestBackupAssetRecoveryCheckpointCarriesPrivateUnresolvedOutcomeProduct
TestRecoveryExecuteClaimProjectsUnresolvedRemoteOutcomeMatrix
TestBackupAssetMigration069UnresolvedOperationOutcomeSQLite
TestBackupAssetMigration069UnresolvedOperationOutcomePostgres
```

The first four form the SQLite/local RED gate. The PostgreSQL selector is a
required real-PostgreSQL gate and must fail closed rather than skip when required
mode lacks a usable DSN. The local gate must explicitly include the existing
`TestBackupAssetMigration069SQLite` legality helper and
`TestBackupAssetMigration069PairedFiles` parity helper. The required PostgreSQL
gate must explicitly include the existing `TestBackupAssetMigration069Postgres`
helper alongside `TestBackupAssetMigration069UnresolvedOperationOutcomePostgres`;
these exact names follow the repository migration convention. At the authorized
pre-feature baseline, the unchanged selectors failed for the missing fourteenth
behavior; the same selector bodies then gated GREEN. Required real PostgreSQL
`000069` plus the six-case behavior matrix passed without skip, along with the
bounded cancellation set, focused race, affected exact-mirror regressions, vet,
owned gofmt, diff, manifest and staged-zero checks. Resources were cleaned.
Independent specification receipt `019fc0c2-cfda-74e3-b218-246f3a425545`
returned `APPROVED` and closed both prior Important evidence findings;
controller-inline quality review found no issue. A local reviewer rerun could
not link because of host disk quota and is not classified as pass or fail.

The exact manifest remains 9 current + 55 create + 81 modify = 145. Paired
`000069` remains the only migration, Task 6 remains `in_progress`, Tasks 7--10
remain `not_executed`, and no delivery action is authorized by this section.

## 21. Post-B3 batch ledger and F6 live-mutation permit amendment

### 21.1 Stable batch and remaining-work ledger

| Batch | Exact scope | Status and credit |
|---|---|---|
| B1 | ordinary/foundation implementation for Corrections 1--3, 5 and 7--13 | B1-E1 (Corrections 1--3, 5), B1-E2 (Corrections 7--10) and B1-E3 (Corrections 11--13) `complete_checked`; aggregate remains partial |
| B2 | exact-mirror/multi-delete implementation for Corrections 4 and 6 plus its delete row | B2-E1 (Correction 4 plus delete row) and B2-E2 (Correction 6) `complete_checked`; aggregate remains partial pending combined/whole evidence |
| B3 | Correction 14 unresolved remote outcome | `PROVED_COMPLETE` at focused correction scope only |

The remaining ledger uses the original review's F1--F8 numbering. F6 is
`complete_approved`, while F3, B1-E1/E2/E3, B2-E1/E2 and Task-6-owned F4 are
`complete_checked` at their exact focused scopes only. Task 6 still has
unchecked execution items, whole specification/quality reviews and whole gates.
Design Corrections 5--9 are
already part of the approved first thirteen and are not review-finding numbers;
no Finding 9 exists.

The remaining execution order is fixed: whole Task 6 specification review,
whole Task 6 quality review, then every final gate before Task 7.

### 21.2 Cross-task ownership

| Contract | Owner |
|---|---|
| preallocated workspace identity, reservation and cleanup-only classification | Task 6 |
| publication, Content revalidation, `revoking` takeover, cleanup node-lease behavior and `RecoveryResultRef` denial | Task 7 |
| bounded restart adoption/reconciliation semantics | Task 6 |
| startup/listener ordering and managed lifecycle | Task 8 |

Tasks 7--10 remain `not_executed`; these boundaries prevent Task 6 evidence from
claiming their behavior.

### 21.3 F6 permit contract and authorized RED baseline

`TargetMutationPermit` remains a closed structural product, while its private
live validator rechecks the permanent latch plus current job, attempt, node
lease and source fence at the instant each target mutation is admitted. A
structurally valid permit must become unusable after any of those durable facts
is lost. Revoked authority produces zero fake mutation calls before
`CreateOwnedJobDir`, `CreateDirectory`, `WriteAtomic` and `Delete`; committed
latch plus current authority admits the corresponding call. `RemoveOwnedJobDir`
is excluded because Task 7 owns cleanup.

The only authorized temporary baseline edit is in
`backend/internal/backupasset/recovery/target.go`: bypass the private live proof
callback in `TargetMutationPermit.ValidateAt` while preserving every shape,
purpose, object-binding, type and `TargetPort` interface contract. This narrow
baseline must make frozen selector
`TestRecoveryReviewF6LatchBeforeTargetMutation` prove that a structurally valid
permit survives latch or job/attempt/node/source-fence loss and causes a
mutating fake call. No unrelated behavior or interface may be removed. The
temporary RED state is never staged, and `target.go` must be restored/fixed to
its final intended live-validation behavior before handoff.

The permanent batch path is
`backend/internal/backupasset/recovery/worker_test.go`; the temporary controlled-
baseline path is `backend/internal/backupasset/recovery/target.go`. The selector
is frozen before the baseline edit, inherited GREEN receives no credit, and an
independent focused planning/spec approval is required before the F6 writer
starts. Exact commands and regressions are owned by `implement.md`.

## 22. F6 focused completion ledger (2026-08-02)

F6 is `complete_approved` at focused live-mutation-permit scope only. The sole
permanent delta is the recording fake near `worker_test.go:34` and
`TestRecoveryReviewF6LatchBeforeTargetMutation` near line 669. The file moved
from SHA-256
`a2452e6d5f01c4afb9fb5255ecc188b8790b695f0121430ac078a58cce373534` to
`352c31b6e5ced3f9f4a033a096ee90c5cd196be3bc4da65ab426bca18254ab3d`.
`target.go` was modified only for the authorized controlled RED and restored
byte-for-byte to
`8a0efaafc5bb08d3981790cc0fa27760936b80a58862f1910fd3e96dd5c64822`.

The controlled RED bypassed only the private `TargetMutationPermit` live-proof
callback. Every revoked permanent latch, current job, attempt fence, node-lease
fence and source fence reached `CreateOwnedJobDir` and reported
`revoked authority CreateOwnedJobDir error=<nil>, want ErrInvalidTargetPermit`.
Compilation and quota failures are not RED evidence. With final validation
restored, current authority admits `CreateOwnedJobDir`, `CreateDirectory`,
`WriteAtomic` and `Delete`; each permanent/current-authority loss rejects before
the recording fake mutates. `RemoveOwnedJobDir` remains Task 7 ownership.

The writer's combined SQLite/model/recovery selector, focused race selector at
`-count=10`, four frozen recovery regressions, `gofmt`, `go vet`, diff,
exact-manifest and staged-zero checks passed. Independent specification thread
`019fc136-feca-7fb0-82bc-3c33739aef12` returned `SPEC APPROVED`; independent
quality thread `019fc13c-0710-7343-b261-dd866382a8c0` returned `QUALITY APPROVED`
and confirmed deterministic isolated fixtures, reliable admission recording,
the frozen hashes, the 145-path manifest and staged paths zero.

Required PostgreSQL thread `019fc13d-ea0e-7f93-b1c6-32aebcb7368e` returned
`POSTGRES GATE PASSED` for the exact required command. It exited 0 with
`ok xirang/backend/internal/database 1.709s` in 31.032s against PostgreSQL 18.4
from isolated `postgres:18-alpine` at loopback database `xirang_f6_gate`. The
first two compile attempts exhausted `/tmp` quota before tests and are neither
RED nor test evidence; the passing run used `/dev/shm` for Go/cgo temporary
work. Created container and scratch resources were removed without touching
pre-existing resources.

This focused closure gives no credit to F3, B1/B2, F4, whole Task 6, Child,
delivery or full gates. Task 6 and the Child remain `in_progress`; the parent
remains `planning`. The exact manifest remains 9 Phase-1 + 55 create + 81 modify
= 145 unique/disjoint paths, staged paths remain zero, and at that F6 checkpoint the next order was F3,
B1-E1/E2/E3, B2-E1/E2, Task-6-owned F4, whole Task 6 specification review,
whole quality review, all final gates, then Task 7.

## 23. R0 bounded execution and observability design

This later section controls execution topology after the old controller was
stopped on 2026-08-02. It does not change the Recovery product architecture or
any earlier technical decision.

The unit of execution is now one evidence-bearing milestone, not the whole
remaining program. Each milestone declares an entry baseline, exact owned
files/decisions, required selectors or review evidence, a delivery boundary,
and a stop/report condition. Ordinary sessions are the default for R0 and F3
adjudication. Goals, controller/child tasks, user-visible tasks and subagents are
permitted when they improve isolation or independent review, but each is scoped
to one milestone and cannot inherit open-ended authority over later work.

A 15-minute heartbeat is prohibited for this task. The paused historical
heartbeat is retained only as audit evidence. This is the sole absolute
orchestration rule introduced by R0. One feature branch, one working
implementation worktree and one product PR are current concurrency defaults,
not invariants: a later milestone may justify a different topology after
checking shared state, dependency order and coordination cost.

Progress reporting is a five-part product:

```text
program delivery + child task/batch state + evidence depth
+ Git delivery state + unresolved scope/risk
```

Elapsed time, token use, diff size, dirty paths and checkboxes are diagnostic
inputs only. They are never sufficient progress evidence. In particular,
parent `12/15` means twelve merged/archived deliverables, while mechanical
Trellis `12/13` counts only instantiated children; neither describes the
fraction of Child 13's unfinished Task 6--12 work.

### 23.1 Approved F3 Plan A+ decision (2026-08-02)

The bounded inline adjudication is complete and user-approved at design-decision
scope. F3 adopts persistent **Plan A+**. This approval gives no implementation,
test, review, Task-6, Child or delivery credit; no product file changed during
the adjudication.

The controlling contract includes all three candidate classes:

1. candidates excluded by the SQL eligibility predicate before `LIMIT`;
2. expected claim races that remain eligible only transiently and can be skipped
   in the same invocation; and
3. candidates that stay SQL-eligible and fail after selection across repeated
   invocations and process restarts.

Class 3 is real. Current claim and takeover selection restart from the oldest
ordered key on every invocation. A post-selection conflict or fence loss does
not necessarily change the candidate's SQL eligibility or ordered key, and no
existing durable row records traversal progress. The existing restart tests
exercise class 1 by making old rows ineligible before `LIMIT`; they do not prove
progress past a class-3 prefix at least as large as the scan bound. Therefore a
same-call stateless skip is useful but insufficient for the approved durable
keyset/high-water contract.

#### Durable scheduler product

The paired `000069` up migrations add a closed `scheduler_state` arm to the
existing `backup_asset_recovery_evidence` table and seed exactly two
distinguished rows:

| Fixed ID | Scope | Ordered key |
|---|---|---|
| `0000000000000000000000000000006a` | `claim` | `(recovery_job.updated_at,recovery_job.id)` |
| `0000000000000000000000000000006b` | `takeover` | `(attempt_row.lease_expires_at,attempt_row.id)` |

The arm adds explicit `scheduler_scope`, `scheduler_cursor_at`,
`scheduler_cursor_id`, `scheduler_high_water_at`, `scheduler_high_water_id` and
`scheduler_revision` fields. Cursor and high-water timestamp/ID pairs are each
both neutral or both populated; a populated cursor cannot exceed its high-water
inside one sweep. Revision is positive and monotonic. A closed row-shape check
binds each fixed ID to its one scope and requires every evidence, receipt,
authority and lease field to remain neutral. Scheduler updates may change only
cursor/high-water, revision and `updated_at`; identity, scope, `created_at` and
all unrelated columns remain immutable, and each successful update increments
revision exactly once.

These are scheduler metadata rows, not user evidence and not independent proof
that Recovery has been used. SQLite and PostgreSQL down guards may ignore only
these exact two fixed, shape-checked rows. They must still refuse atomically for
the permanent schema-use latch, any normal/receipt evidence, every real Recovery
aggregate row, Recovery Content state or attributed lease state. The twelve-table
limit, permanent latch contract and `000069` ownership remain unchanged.

#### Selection, concurrency and failure semantics

For each scope, a short scheduler transaction locks the distinguished row or
uses its revision CAS, captures a high-water when starting a sweep, selects the
next currently eligible key strictly after the cursor and not after the
high-water, durably advances cursor/revision to that key, and commits. Reaching
the high-water starts at most one new sweep in that reservation attempt. Legal
new claim/takeover arrivals sort after the captured high-water and wait for the
next sweep; rows that move behind the cursor also wait for that sweep.

Only after scheduler commit does the worker enter the existing candidate-local
claim/takeover transaction. One public invocation reserves and tries at most the
configured scan bound and returns the first successful claim. Expected claim
conflict or fence loss consumes only the reserved scheduler position and
continues; it performs no Plan, Job, Attempt, checkpoint, source/node lease or
target mutation. A database-wide query/transaction failure fails the pass and
is never relabeled candidate success. A crash after pre-advance but before the
candidate transaction delays that candidate until sweep wrap while allowing
later work to proceed. Scheduler-row serialization plus existing domain CAS and
fences preserve one-winner behavior for concurrent workers; stale workers have
zero domain or remote mutation authority.

The stateless Plan B is rejected because it proves only same-invocation progress
and loses its position on restart. Per-job or per-attempt `next_attempt_at`
rotation is rejected because a candidate that has lost its fence cannot be
authorized to mutate a domain row merely to repair scheduler fairness.

#### Drift terminalization and execute replay

Plan A+ also retains the two non-scheduler F3 corrections found by the stateless
analysis. Immediately before mutation arm, the current exact fence owner must
revalidate the frozen authority/source/target product. If authority-only drift
is found while `mutation_armed=0`, no checkpoint exists and no target outcome is
ambiguous, one guarded transaction records `job=failed` with
`failure_category=pre_write_drift`, applies the sole legal
`plan: executed -> superseded` edge with a monotonic revision, revokes unused
authority, closes the attempt, and releases the source and node leases. A losing
or stale worker changes nothing. Mutation arm, any checkpoint or an ambiguous
target observation forbids this supersede path and remains under the existing
adoption/needs-attention contract.

Same-key execute replay loads the original immutable authorization receipt and
its frozen execute intent/result linkage before revalidating mutable current
facts. It returns the same durable failed job after legitimate drift and never
creates a second effect or converts a lost response into an idempotency conflict
by recomputing intent from current source state.

#### Ownership and stop point

The later bounded F3 TDD writer owns exactly these already-manifested product
paths:

```text
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/backupasset/recovery/service.go
backend/internal/backupasset/recovery/service_test.go
backend/internal/model/backup_asset_recovery.go
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.down.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.down.sql
backend/internal/database/backup_asset_migrations_integration_test.go
```

Its frozen selectors are
`TestRecoveryReviewF3PreWriteDriftTransactionSQLite`,
`TestRecoveryReviewF3ExecuteReplayAfterDrift`,
`TestRecoveryReviewF3TwoWorkerAndCrashBarriers` and
`TestRecoveryReviewF3PreWriteDriftTransactionPostgres`. The writer must observe
controlled-baseline RED before GREEN, run the required real PostgreSQL companion
without skip, then run focused race/regression/static, paired-migration,
manifest, staged-zero and evidence checks. It stops and reports after that
focused F3 checkpoint. It cannot start B1/B2, claim whole Task 6 completion, add
a table or `000070`, or edit outside the exact ownership list without a new
written amendment.

## 23. Task 6 B1-E1 focused closure

B1-E1 closes Corrections 1--3 and 5 at `complete_checked` focused scope. Its
permanent owner set is limited to `recovery/executor.go`,
`recovery/contracts_test.go` and `recovery/executor_test.go`; the controlled RED
temporarily changed `contracts.go`, which was restored byte-for-byte before
GREEN. No schema, migration, interface or manifest path changed.

The frozen `TestRecoveryVerifyOperationProductMatrix` responsibility now binds
the ordinary create/overwrite/skip identity and byte sentinels, persisted
snapshot/job-item parity, fresh and per-operation source revalidation, skip
source/target separation, and the closed present Verify product. Create and
overwrite bind materialized source digest/size to the persisted post product.
Skip binds source size to `EstimatedBytes`, accepts an independently valid
source digest, verifies only the frozen prior-target digest/bytes, and projects
only `skipped`. Closed present observation still requires exact digest/byte
equality, while revisions remain bounded opaque target-derived values.

The controlled baselines exposed the missing ordinary product validation,
source checks occurring after mutation boundaries, and the real skip
source/target identity conflation. The same frozen selector, affected package
regressions, race repetition and paired SQLite/required-PostgreSQL ordinary-row
matrices are GREEN. This gives no credit to B1-E2/E3, B2, F4, whole Task 6,
Child or delivery gates; the next independent stop point is B1-E2.

## 24. Task 6 B1-E2 focused closure

B1-E2 closes Corrections 7--10 at `complete_checked` focused scope. Its permanent
product delta is limited to the frozen test contracts in
`recovery/contracts_test.go` and `recovery/worker_test.go`; controlled RED
temporarily changed `contracts.go` and `service.go`, both restored byte-for-byte
before GREEN. No model, schema, migration, interface, crypto primitive or
manifest path changed.

The five stable selectors bind: exact canonical schema-v2 item locators and
semantic digests for every operation row; canonical whole-product decode and
rebuild; generic encrypted aggregate persistence distinct from the item cipher;
the preallocated `jobs/<jobID>` none-state workspace and exact
`jobs/<jobID>/<suffix>` final-object digest; every row/job/source/root/workspace/
digest/operation/key/cipher field in the recovery-local AEAD; hook exclusion;
and no raw locator in DB-external product/error surfaces.

The controlled baseline removed the snapshot locator fields, one row-identity
AAD field and the isolated workspace prefix from final-object derivation. The
unchanged selectors failed at those exact behavior assertions, then passed after
the intended production behavior was restored. Focused normal and race x10,
full recovery, five affected packages, vet, SQLite and required real PostgreSQL
companions, format/diff and scope checks are GREEN. The migration companions are
supporting regression only and do not close Correction 13.

This closure gives no credit to B1-E3, B2, F4, whole Task 6, Child or delivery
gates. B1 remains partial; the next independent stop point is B1-E3.

## 25. Task 6 B1-E3 focused closure

B1-E3 closes Corrections 11--13 at `complete_checked` focused scope. Its
permanent delta is limited to `recovery/service_test.go`,
`recovery/worker_test.go` and
`database/backup_asset_migrations_integration_test.go`; controlled RED changed
only `recovery/service.go`, `recovery/worker.go` and the paired `000069` up
migrations, all restored byte-for-byte before final verification.

The stable product is three-part. Execute builds the complete identity and
encrypted item aggregate outside the effect transaction, revalidates the exact
active key and locked facts, consumes the grant before aggregate effects and
rolls grant plus aggregate back together. Restart adoption accepts only
`(ctx, claim, jobItemID)`, derives the handoff from durable rows, performs Verify
without a DB transaction, then re-locks every durable fence before one atomic
projection. Permanent cleanup-key failure reconciles only current post-arm work
through a bounded DB-only pass before returning the original fatal startup
error. Paired SQLite/PostgreSQL enforcement freezes the workspace and complete
item locator/operation product, permits only the declared one-way projection,
rejects terminal rewrite, constrains delete legality and enforces per-job
semantic/final digest uniqueness.

Five controlled behavior removals made the unchanged selectors fail at their
intended assertions: false adoption after durable digest drift, omission of the
current post-arm cleanup candidate, aggregate rejection when grant consumption
was not persisted first, acceptance of a mismatched transaction-scoped key, and
paired acceptance of an immutable semantic-digest rewrite combined with an
otherwise legal terminal projection. Restoring the protected product produced
unchanged-selector GREEN on SQLite and required real PostgreSQL.

Focused normal and race x10, full recovery, runtime startup, affected packages,
vet, paired migration companions, format/diff and scope gates are GREEN. Three
B1-E3-owned lint findings were corrected; seven earlier recovery-package lint
findings remain visible, so this closure does not claim a whole-package lint
pass. No shared code-spec update is needed because this unshipped Recovery
contract is already executable in §19 and the frozen task matrix.

At the B1-E3 checkpoint this closure gave no credit to B2, F4, whole Task 6,
Child or delivery gates. B1 remained partial and the next independent stop point
was B2-E1.

## 26. Task 6 B2-E1 focused closure

B2-E1 closes Correction 4 plus its delete row at `complete_checked` focused
scope. Its permanent delta is limited to
`recovery/contracts_test.go`; controlled RED temporarily changed
`recovery/contracts.go`, `recovery/executor.go` and the paired `000069` up
migrations, all restored byte-for-byte before final verification. No top-level
selector, interface, model, table, migration number, crypto domain or manifest
path changed.

The frozen selector responsibility now binds the complete B2-E1 product:
delete rows belong only to durable `in_place + exact_mirror`, carry prior
`present` with a lowercase SHA-256 digest, empty expected-post digest, both byte
sentinels `-1`, no plan item/source, and no synthetic absence digest. Runtime
delete pauses before mutation without durable exact-mirror authority. Success
requires an explicit `AbsentObservation{Evidence: exact}`; permission denial,
timeout, unsupported stat, transport failure and ambiguous missing cannot
satisfy the absent arm.

Four controlled behavior removals made unchanged selectors fail at the exact
contract assertions: accepting arbitrary nonempty absence evidence, bypassing
the empty-grant pause, accepting a synthetic delete post digest, and admitting
an isolated delete row in both SQLite and PostgreSQL. Restoring the protected
product produced final GREEN on focused normal/race, full recovery, six affected
packages, vet and paired required-real-PostgreSQL evidence. The seven earlier
recovery-package lint findings remain outside the B2-E1-owned delta, so this
closure does not claim a whole-package lint pass.

The existing successful-delete regression also observes the absence-chain
projection, but that inherited assertion is support-only here. It gives no
Correction 6 or B2-E2 credit. B2 aggregate remains partial; the next independent
stop point is B2-E2.

## 27. Task 6 B2-E2 focused closure

B2-E2 closes Correction 6 at `complete_checked` focused scope. It adds no
top-level selector, product interface, model, schema, migration, crypto domain
or manifest path. `TestRecoveryVerifyOperationProductMatrix` now owns the
absence-chain and multi-delete submatrix by invoking the existing successful
delete, same-execution two-delete, restart two-delete and consumed-authority
reconciliation regressions. Production-`000069` SQLite and PostgreSQL execute
the same multi-delete helper.

For delete item `i`, the committed revision is the length-framed digest under
literal domain `xirang/recovery/target-absence-chain/v1` of the prior revision,
immutable item operation digest and item ID, job source revision, attempt/node
fences, exact absence evidence and observed target revision. The ordered
contract requires `next[i] == prior[i+1]`, one operation checkpoint per delete,
and `job.target_chain_revision == next[last]`. Delete checkpoints keep the B3
unresolved product neutral.

One durable exact-mirror delete-set grant is consumed once. Its bound
`delete_authority_required` plus `delete_authority_consumed` checkpoint pair is
the restart authority for later items in the same set. The worker may reconcile
an already absent target after consumption, but cannot request another bearer,
consume again, repeat a completed delete or accept a non-exact absence.

Two controlled behavior removals proved attribution. Replacing the absence
domain with the ordinary present-target domain broke exact chain assertions.
Ignoring the consumed checkpoint pair broke the same-execution second delete,
restart continuation and consumed-absence reconciliation. Both product files
were restored byte-for-byte before GREEN; the permanent delta is test-only.

The PostgreSQL migration fixture must model causality across two clock sources:
`000069` seeds the permanent scheduler row using subsecond database time, while
the shared deterministic service fixture uses a whole-second application time.
The fixture therefore moves its clock just after the persisted scheduler floor
before claiming work. This is test infrastructure alignment, not a relaxation
of the monotonic scheduler trigger or a new runtime clock rule.

Focused normal/race, full Recovery, affected-package, vet, production SQLite,
required real PostgreSQL, format and scope gates are GREEN. Seven earlier lint
findings remain outside the owned lines, so whole-package lint is not claimed.
B2 aggregate remains partial; F4, combined/whole Task 6 evidence, reviews and
gates remain open. The next independent stop point is Task-6-owned F4.

## 28. Task 6 F4 focused closure

F4 is `complete_checked` at the Task-6-owned preallocated-workspace,
reservation, deadline and cleanup-only boundary. The permanent delta is limited
to `recovery/executor_test.go`; it adds exactly
`TestRecoveryReviewF4WorkspaceDeadlineAndPublication` and
`TestRecoveryReviewF4PartialWorkspaceCleanupOnly`. The existing seventeen
first-thirteen Task 6 selectors remain unchanged. No product interface, model,
table, migration, crypto domain or manifest path was added.

The first selector binds the complete ordered workspace product. Execute commits
one encrypted-at-rest preallocated `jobs/<opaque>` locator and immutable
`WorkspaceBindingDigest` at `workspace_phase=none`, with empty marker, owner,
fence and deadline. Under the claimed attempt, `PrepareFirstWrite` reuses that
identity and commits `reserved`, marker binding, owner/fence, the absolute
24-hour deadline, sequence-zero reservation checkpoint and permanent latch
before `CreateOwnedJobDir` or any content write. Successful execution seals the
workspace but creates no ResultSet or result row. An unexpected existing remote
directory fails closed; retry retains the exact locator, bindings, deadline and
single checkpoint without rename or reallocation.

The second selector binds terminal partial disposition. Pre-arm failure and
queued cancellation retain `workspace_phase=none` and no reservation facts.
Armed cancellation and a post-arm unresolved remote outcome preserve the
workspace identity/deadline and move to `needs_attention|cleanup_due`; neither
path publishes a result. This is classification for later cleanup only. Task 7
still exclusively owns publication, Content revalidation, `revoking` takeover,
cleanup node-lease behavior and `RecoveryResultRef` denial.

Two controlled behavior removals established attribution. A temporary
`24h - 1s` deadline made the unchanged workspace selector fail. Temporarily
removing the armed-cancellation `cleanup_due` projection made the unchanged
partial selector observe illegal `reserved` state. Final product hashes match
the entry baseline for `worker.go` and `executor.go`; the only permanent change
is the F4 test coverage.

Fresh focused normal and race x10, full Recovery, the five other affected
backend packages, three SQLite migration companions, three required real
PostgreSQL companions, same-scope vet, owned formatting, diff, Trellis,
manifest and staged-zero gates pass. Recovery-package lint still reports the
same seven earlier findings outside the new F4 test range, so this closure does
not claim whole-package lint success.

This focused closure gives no combined B1/B2, unchecked-row, whole Task 6,
Child, delivery or Task 7 credit. Task 6 and the Child remain `in_progress`, the
parent remains `planning`, and the next stop is before whole Task 6
specification review.

## 29. Whole Task 6 specification-review corrections 15--17

### 29.1 Status, precedence and scope

The 2026-08-03 whole Task 6 specification review found four blocking behaviors:
post-arm target-call errors had no terminal disposition; a valid remote outcome
was erased when the following source revalidation failed; restart adoption did
not perform Provider source revalidation and returned observation ambiguity as
fence loss; and the implemented workspace path skipped the already-designed
`marker_created` phase without paired SQL enforcement.

The user approved Plan A. This section is later controlling over §§19--20 only
where it explicitly widens an existing product arm. It adds Corrections 15--17
without changing the four Correction 14 unresolved categories, twelve-table
aggregate, paired `000069` ownership, migration number, cryptographic domains,
public API, or exact 145-path manifest. Existing focused credit remains intact;
the whole specification review remains open until these corrections pass their
frozen RED/GREEN and dual-engine gates.

### 29.2 Correction 15: no-product post-arm disposition

`ordinaryOperationResult` must distinguish three in-memory conditions rather
than infer them from zero values: a valid returned product, an invalid returned
product, and a call error with no trustworthy product. The distinction is not a
new persisted status. Persistence uses the existing category and digest arms:

| Boundary | Returned product | Persisted category/facts |
|---|---|---|
| `WriteAtomic`/`Delete` error | none | `write_result_invalid`; write digest/revision and all observation facts empty |
| invalid write result | bounded invalid product | `write_result_invalid`; sanitized write digest present, trusted write revision empty |
| `Verify` error | none | `observation_invalid`; observation digest/revision/presence empty; retain valid write facts when available |
| invalid observation | bounded invalid product | `observation_invalid`; sanitized observation digest present, structured observation facts empty |
| valid mismatching adoption observation | no original write result survives crash | `verification_mismatch`; observation facts present and write facts empty |

The ordinary create/overwrite/delete path still requires its valid write result
for revision disagreement or verification mismatch. Only the durable restart-
adoption path may project a create/overwrite mismatch without write facts.
`skip` always has neutral write facts. Paired SQL cannot authenticate an
in-process origin flag, so it admits the closed no-write mismatch shape while
the two Recovery call sites enforce their distinct preconditions.

Every call-error arm enters the existing five-second bounded
`projectPendingOperationUnresolved` transaction. It appends exactly one
terminal `operation_unresolved`, fails only the current neutral item, changes
the job to `needs_attention/remote_outcome_unresolved`, writes sanitized
evidence, closes the attempt, releases both leases, preserves the prior chain,
and stops all later target calls. Empty digest means no product existed; it is
not a digest of an error string. Raw errors, serialized zero values, output and
locators never cross the process boundary.

### 29.3 Correction 16: Provider source and completed-operation disposition

`WorkerCoordinatorDependencies` gains the existing portable interface:

```go
type WorkerCoordinatorDependencies struct {
    // existing dependencies omitted
    SourceResolver provider.RsyncRestoreSourceResolver
}
```

Construction requires a resolver. Runtime will inject the Repository service
when Task 8 wires the managed lifecycle; focused Recovery tests inject a closed
fake. Adoption never accepts a locator. After its first short durable load, it
calls `NewRsyncRestoreSourceRef(initial.plan)`, resolves that scalar ref, calls
`Revalidate`, and closes the source without a DB transaction. Resolve,
revalidate, and close errors collapse to the closed source outcome
`drifted|failed`; no private error is persisted or returned. DB-only
`SourceValidator.RevalidatePlanTx` remains an independent durable-fact check.

Target Verify is read-only and still runs after an untrusted source result so
the worker can classify the target without authorizing another mutation. The
cross-product is:

| Provider source | Target observation | Disposition |
|---|---|---|
| matched | exact expected | normal adoption projection |
| drifted/failed | exact expected | completed operation plus source-failure disposition |
| any closed source outcome | call error/invalid/mismatch | Correction 15 unresolved disposition carrying that source outcome |

Every valid verified operation now produces an `operation` checkpoint bound to
the exact existing `job_item_id`. Create, overwrite and delete derive their
existing next target revision. Skip records
`next_target_revision == prior_target_revision`; this proves an unchanged target
without claiming a write. All unresolved fields on a normal checkpoint stay
neutral. In-place history validation accepts equality only for a skip item and
requires inequality for mutating items. The same item binding is emitted by
ordinary and adoption projection and is enforced by both migrations.

For a valid target outcome followed by source drift/failure, one short fenced
transaction performs the normal item/checkpoint/chain projection first, then
creates a `failure/needs_attention` evidence row bound to that exact normal
checkpoint, closes the attempt, releases source and node leases, and updates the
job to `needs_attention` with stable failure category
`source_revalidation_failed`. The item's `succeeded|skipped` outcome and
verification facts remain intact. Isolated work moves its workspace to
`cleanup_due`; in-place remains `none`. A source failure before a later item
uses the last item-bound operation checkpoint to perform the same terminal
closure without changing any earlier item or chain fact. No fifth unresolved
category is introduced because the remote outcome is already trusted.

The evidence insert guard therefore has two exact failure arms: the existing
`operation_unresolved` arm with a failed current item, and the new normal
`operation` arm with its exact succeeded/skipped item and the job's chain equal
to the checkpoint next revision. Both require the current running attempt,
active source/node leases, current fences, write grant, plan and job bindings
before the application closes them in the same transaction.

### 29.4 Correction 17: durable marker creation

The isolated first-write sequence is now fully ordered:

```text
transaction A: none -> reserved + marker binding/deadline/checkpoint/latch
remote call:   CreateOwnedJobDir(same immutable workspace capability)
transaction B: reserved -> marker_created under current job/attempt/source/node/latch fences
remote call:   first item mutation, allowed only from marker_created
transaction C: item checkpoint + marker_created -> writing
```

Transaction B re-locks the job, attempt, source lease and node lease, checks the
same owner/fences, transition revision, workspace locator/binding/marker digest,
deadline and permanent latch, and performs a one-row CAS. If the process crashes
after the remote marker exists but before B commits, the database remains
`reserved`. A retry may call the idempotent `CreateOwnedJobDir` against the same
capability and then complete the CAS; it cannot mint an item permit, rename, or
allocate a second workspace.

The live mutation proof permits the workspace object at `reserved` or
`marker_created` only for idempotent owned-directory creation/validation. An
item object is legal only at `marker_created|writing`. Ordinary handoff loading
and restart adoption require `marker_created|writing`; `reserved` cannot be
evidence that an item mutation legally occurred. The first normal operation
transitions `marker_created -> writing`; later operations retain `writing`.

SQLite extends the existing
`trg_backup_asset_recovery_jobs_publication_integrity` trigger and PostgreSQL
extends the existing
`backup_asset_recovery_job_publication_integrity_guard()` function used by its
existing publication-integrity trigger. The workspace-phase portion permits
only these changing edges:

```text
none -> reserved
reserved -> marker_created | cleanup_due
marker_created -> writing | cleanup_due
writing -> sealed | cleanup_due
sealed -> published | cleanup_due
cleanup_due -> workspace_cleaned
```

Same-value updates remain legal. In-place jobs remain permanently `none`.
Reverse edges, `reserved -> writing`, skips, rewrites from published/cleaned,
and any other edge fail identically on both engines. The unchanged down
migrations already drop those existing publication-integrity trigger/function
objects through their paired cleanup sections; this correction creates no new
schema object that would require another down statement, and no used schema
becomes downgrade-safe.

### 29.5 Frozen implementation and validation boundary

The permanent product owner set is limited to already-manifested paths:

```text
backend/internal/backupasset/recovery/executor.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/state.go
backend/internal/backupasset/recovery/executor_test.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/backupasset/recovery/state_test.go
backend/internal/model/backup_asset_recovery.go
backend/internal/model/backup_asset_recovery_test.go
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.down.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.down.sql
backend/internal/database/backup_asset_migrations_integration_test.go
```

The model path is expected to need no field addition; it remains in the owner
set for contract assertions. The down migrations, Provider interfaces,
Repository implementation, runtime wiring, API/frontend and Task 7/8 product
remain unchanged in this bounded repair.

The frozen top-level selectors are exactly:

```text
TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved
TestRecoveryExecuteClaimRevalidatesPinnedSourcePerOperation
TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition
TestRecoveryPrepareFirstWritePersistsMarkerCreatedBeforeContent
TestBackupAssetMigration069WholeTask6ClosureSQLite
TestBackupAssetMigration069WholeTask6ClosurePostgres
```

`TestBackupAssetMigration069PairedFiles`, the existing Correction 14 selectors,
the first-thirteen frozen matrix, F3/F4/F6 regressions and required real
PostgreSQL full-`000069` helpers remain mandatory companions. Corrections
15--17 receive no implementation credit until unchanged selectors show genuine
current-baseline RED and later GREEN. Only then may whole Task 6 specification
and quality review restart.

## 30. Correction 18: isolated adoption continuation

### 30.1 Status, precedence and invariant split

This controller-approved amendment is later-controlling for isolated restart
adoption and continuation. It corrects two coupled defects discovered by a real
three-item execution: adoption previously rejected a valid prior operation
checkpoint, and a successful adoption closed the takeover attempt even when
pending items remained. The latter leaves a `running` job with no claimable or
takeover-eligible attempt.

Workspace marker provenance and current mutation authority are separate:

```text
marker provenance: workspace_owner + workspace_fence + immutable marker HMAC
current authority: current attempt + source lease + node lease + use latch
```

The marker provenance never changes after reservation. A takeover validates the
same marker but does not rewrite it. Current item permits validate the current
attempt/source/node fences; at `writing`, they do not require the marker creator
to equal the current worker.

### 30.2 Closed history and continuation admission

The isolated history is valid only when sequence zero is the exact immutable
workspace reservation and every later row is a contiguous normal `operation`
checkpoint. Later rows map one-to-one by `job_item_id` to completed durable
items, match the item operation digest and immutable job authority product, have
neutral unresolved/delete fields, and form one target chain from the preflight
revision to the current job revision. A skip row keeps the revision equal;
create/overwrite changes it. The selected item is pending and absent from the
history. Checkpoint count equals one reservation plus completed item count.

Ordinary execution has two disjoint admissions:

```text
fresh:
  exactly sequence-zero reservation, created by current attempt

continuation:
  workspace_phase=writing
  complete isolated history validates
  at least one pending item remains
  latest operation checkpoint belongs to current attempt/fence/node fence
```

The continuation condition means a fresh takeover with only old-attempt
checkpoints cannot replay the ambiguous item. `AdoptInterruptedOperation` must
first append the current-fence checkpoint. Only then may `ExecuteClaim`
materialize the remaining frozen pending declarations, reuse the existing latch
and workspace permit, and execute them. No continuation calls
`CreateOwnedJobDir` or `markWorkspaceMarkerCreated`.

### 30.3 Projection and terminal behavior

After exact adoption and matched source revalidation, the projection counts
pending items inside the same transaction:

- pending nonzero: preserve `running|writing`, current running attempt, active
  source lease and active node lease;
- pending zero: close the attempt, release both leases, transition
  `running -> verifying -> succeeded`, and transition isolated workspace
  `writing -> sealed` exactly as ordinary final projection does;
- source drift/failure: retain the completed item/checkpoint/chain and use the
  existing `needs_attention/source_revalidation_failed` terminal transaction.

Both adoption and later ordinary projections leave workspace owner/fence and
marker binding unchanged. The full validated checkpoint history participates in
the initial/final adoption durable digest so any row substitution between target
Verify and projection loses the fence.

### 30.4 Exact implementation boundary

Product/test changes remain within already-manifested paths:

```text
backend/internal/backupasset/recovery/executor.go
backend/internal/backupasset/recovery/executor_test.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/worker_test.go
```

No model, migration, Provider, Repository, runtime, API, frontend or Task 7 path
changes in this correction. Existing paired SQL immutability/checkpoint guards
remain mandatory regression evidence but require no DDL amendment.

## 31. Correction 19: durable marker-validation takeover

### 31.1 Root cause and product split

This controller-approved amendment closes the crash window where reservation
and remote marker creation exist but the `reserved -> marker_created` CAS did
not commit before the original attempt expired. `workspace_owner` and
`workspace_fence` remain immutable marker-creation provenance. They cannot
also prove which attempt successfully validated that marker and committed the
durable phase transition.

The job therefore gains one private, closed validation product:

```text
workspace_marker_validation_attempt_id
workspace_marker_validation_attempt_fence
workspace_marker_validation_node_fence
```

The tuple identifies the exact attempt and node fence that committed marker
validation. Current source authority is still checked from the active source
lease in the same transaction and at live permit use; no source token is copied
onto the job. The marker HMAC remains derived from immutable creator provenance,
not from the validation tuple.

### 31.2 Phase and crash matrix

The paired shape is exact:

```text
none | reserved:
  validation tuple = empty / 0 / 0

marker_created | writing | sealed | published:
  validation tuple = opaque attempt id / positive attempt fence / positive node fence

cleanup_due | workspace_cleaned:
  validation tuple = either wholly empty or wholly populated

in_place (always none):
  validation tuple = empty / 0 / 0
```

Only the `reserved -> marker_created` transaction may assign the tuple. Once
populated it is immutable, including across takeover, adoption, ordinary
projection, sealing, publication and cleanup. A `reserved -> cleanup_due` edge
retains the empty tuple; cleanup after marker validation retains the full tuple.
Partial tuples and same-phase injection are invalid on both engines.

The runtime crash matrix is:

```text
reserved + empty tuple:
  old claim never obtained an item permit
  a fresh claim may validate/create the same owned marker with a workspace-only permit
  successful returned product atomically commits marker_created + fresh tuple

marker_created + tuple matching current claim:
  current claim may mint the first item permit
  idempotent same-claim marker validation remains legal

marker_created + tuple belonging to an older claim:
  the first item is ambiguous
  ordinary execution fails before any target mutation
  restart must adopt/terminalize the pending first operation

writing:
  marker tuple is historical validation provenance only
  current continuation still requires the latest current-fence operation checkpoint
```

This prevents the unsafe alternative of rewriting marker creator provenance or
blindly replaying an item after a crash between `marker_created` and its first
operation checkpoint.

### 31.3 Application and SQL enforcement

`PrepareFirstWrite` may load an old-attempt sequence-zero reservation only while
the job remains `reserved` with an empty validation tuple. The live workspace
permit still requires the current attempt, source lease, node lease, permanent
latch, immutable job bindings and plaintext deadline. `markWorkspaceMarkerCreated`
re-locks that product and assigns the phase plus tuple in one CAS while leaving
owner, creator fence, marker binding, locator and deadline unchanged.

At `marker_created`, the item-permit validator requires all three tuple fields
to equal the current claim. A different takeover cannot refresh or replace the
tuple. At `writing`, Correction 18's current operation-checkpoint admission is
the only continuation proof. The tuple is included in durable handoff digests
so an initial/final load cannot observe different marker-validation provenance.

The existing paired `000069` job CHECK and publication-integrity trigger/function
gain the columns, closed tuple shape, one assignment edge and post-assignment
immutability. The existing down files need no new statement because they already
drop the owning table and trigger/function. Model fields remain private with
explicit non-null/default tags.

### 31.4 Frozen implementation boundary

Permanent product and test changes remain inside already-manifested paths:

```text
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/backupasset/recovery/executor_test.go
backend/internal/model/backup_asset_recovery.go
backend/internal/model/backup_asset_recovery_test.go
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
backend/internal/database/backup_asset_migrations_integration_test.go
```

No down migration, Provider, Repository, runtime, API, frontend, Task 7 path,
new table, migration number, crypto domain or manifest path is added.

### 31.5 Separate open exact-mirror finding

Whole-review inspection also found that consumed exact-mirror delete authority
is still validated as if its immutable `delete_authority_required/consumed`
checkpoint and grant provenance must belong to the current takeover claim. That
is separate from marker validation: it concerns durable delete-set authority
after a later in-place takeover and requires its own controlling amendment and
RED/GREEN. Correction 19 neither relaxes nor claims closure of that contract.

## 32. Correction 20: consumed exact-mirror authority takeover

### 32.1 Status, root cause and product split

This user-approved amendment is later-controlling for a fresh in-place takeover
after exact-mirror delete authority has already been consumed. The durable
`delete_authority_required -> delete_authority_consumed` pair and its one-use
grant are immutable historical delete-set authority. They are not current
worker authority and cannot be required to carry a later takeover's attempt or
fences.

The existing implementation incorrectly conflates two products:

```text
historical delete-set authority:
  required/consumed checkpoint attempt + attempt fence + node fence
  grant delete-attempt id + attempt fence + node fence

current mutation authority:
  current attempt + source lease + node lease + use latch
  latest current-fence operation checkpoint after adoption
```

### 32.2 Closed historical validation

The required and consumed checkpoints must remain contiguous and must carry the
same historical attempt id, attempt fence and node fence. The grant's
`delete_attempt_id`, `delete_attempt_fence` and `delete_node_fence` must match
that historical tuple. The existing job, plan, checkpoint, delete-set,
target-revision, binding-digest, expiry and consumed-time checks remain exact.

No takeover rewrites either checkpoint, rewrites or re-consumes the grant, or
requires a new bearer secret. Historical validation is independent of the
current claim, while the current claim is still validated separately against
the live attempt, source lease, node lease and job transition revision.

### 32.3 Fresh takeover and adoption boundary

For a fresh takeover whose latest checkpoint belongs to the expired attempt:

```text
ExecuteClaim before adoption:
  fail closed before WriteAtomic or Delete

AdoptInterruptedOperation with exact absence:
  verify only, append a current-claim operation checkpoint, advance the
  absence chain and complete the ambiguous delete item

AdoptInterruptedOperation with present/mismatch/invalid/error observation:
  enter the existing operation_unresolved terminal contract

ExecuteClaim after successful adoption with later delete items pending:
  admit continuation through the latest current-claim checkpoint and reuse the
  already-consumed historical delete-set authority without a bearer secret
```

The latest-current-checkpoint condition in `currentFirstWritePermitPathTx`
remains unchanged. It is the guard that prevents ordinary execution from
bypassing adoption. The correction only removes the invalid current-claim
comparison from historical grant validation and replaces it with exact
checkpoint-to-grant provenance checks.

### 32.4 Frozen implementation boundary

Permanent product and test changes remain inside already-manifested paths:

```text
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/executor.go
backend/internal/backupasset/recovery/executor_test.go
```

No model, migration, Provider, Repository, runtime, API, frontend, Task 7 path,
new table, migration number, backfill, crypto domain or manifest path is added.

## 33. Task 7 unpublished-workspace cleanup admission amendment

### 33.1 Status, scope and provenance split

This user-approved Plan A amendment is later-controlling for Task 7's
unpublished `cleanup_due` workspace claim. It extends the existing
`backup_asset_recovery_jobs` aggregate inside paired `000069`; it does not add a
workspace table, a thirteenth recovery table, a new migration number or a
backfill.

The existing execution tuple remains immutable historical marker provenance:

```text
workspace_owner
workspace_fence
workspace_marker_validation_attempt_id
workspace_marker_validation_attempt_fence
workspace_marker_validation_node_fence
```

Cleanup must never rewrite or reuse that tuple. The job gains this separate,
private cleanup tuple:

```text
WorkspaceCleanupPhase          / workspace_cleanup_phase
WorkspaceCleanupOwner          / workspace_cleanup_owner
WorkspaceCleanupLeaseExpiresAt / workspace_cleanup_lease_expires_at
WorkspaceCleanupFence          / workspace_cleanup_fence
WorkspaceCleanupNodeLeaseID    / workspace_cleanup_node_lease_id
WorkspaceCleanupNodeFence      / workspace_cleanup_node_fence
WorkspaceCleanupAttempt        / workspace_cleanup_attempt
```

`workspace_cleanup_phase` uses the existing closed cleanup phase union
`claimed | revoked | drained | validated | delete_started | deleted |
tombstoned`. Every field is private (`json:"-"`); the node-lease id is nullable
and is constrained with the owning job to one `recovery_cleanup` node lease.

### 33.2 Closed row shapes and transitions

Paired SQL and the model enforce these shapes:

```text
non-cleanup phase, or cleanup_due before its first claim:
  cleanup_phase=claimed; owner/lease/node binding empty; fences/attempt=0

active cleanup_due owner:
  cleanup_phase is non-tombstoned; owner/lease/node lease present;
  cleanup fence, node fence and attempt are positive

retryable cleanup_due failure:
  cleanup_phase is non-tombstoned; owner/lease/node binding cleared;
  cleanup fence and attempt remain positive; node fence returns to zero

workspace_cleaned tombstone:
  cleanup_phase=tombstoned; owner/lease/node binding cleared;
  cleanup fence and attempt remain positive; node fence is zero
```

An initial claim or retryable-failure claim enters active `claimed` and
increments cleanup fence/attempt. An expired active owner is replaced only
after expiry, preserves its durable non-tombstoned phase and increments cleanup
fence/attempt. Later phase advances keep the exact owner, cleanup fence/attempt
and node lease/fence. A current-owner retryable failure preserves its durable
phase and cleanup fence/attempt while clearing ownership and node binding. Only
an active `deleted` owner may atomically write
`workspace_cleaned + tombstoned` and clear ownership. In every shape,
`workspace_owner`, `workspace_fence` and marker-validation provenance remain
unchanged.

### 33.3 Claim ordering and lost-race disposition

The unpublished claim uses the same shared node boundary as published cleanup,
but its workspace row is physically the job row:

1. Read one exact `cleanup_due` job candidate without a locking clause.
2. Begin a transaction and lock that same job row. This single row lock is both
   the required job lock and workspace-cleanup-row lock; there is no second
   physical workspace row to lock.
3. Revalidate isolated terminal/unpublished state, run shared node admission,
   allocate a fresh monotonic node fence and insert a fresh active
   `recovery_cleanup` node lease.
4. CAS the seven workspace-cleanup fields against the complete candidate
   snapshot. Initial/retryable claims enter `claimed`; expired takeover keeps
   the prior phase.
5. If the snapshot or CAS loses, commit the newly created node lease as
   `released` in the same transaction and return the closed cleanup conflict.

Active owners, non-`cleanup_due` jobs, in-place jobs, nonterminal jobs, jobs with
a published ResultSet and busy nodes are rejected before a workspace claim can
be returned. Node admission may expire an old cleanup node lease, but an old
cleanup or node fence receives no new remote or database mutation authority.

### 33.4 Frozen focused boundary

This amendment owns only schema/model support and unpublished-workspace claim
admission in these already-manifested paths:

```text
backend/internal/model/backup_asset_recovery.go
backend/internal/model/backup_asset_recovery_test.go
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
backend/internal/database/backup_asset_migrations_integration_test.go
backend/internal/backupasset/recovery/result_lifecycle.go
backend/internal/backupasset/recovery/result_lifecycle_test.go
```

It stops before Content revoke/drain, cleanup renewal, target credential/root/
marker validation, remote delete, failure projection, tombstone execution,
orphan scanning, runtime scheduling or whole Task 7 closure. The paired down
files add only explicit teardown for the new named guards before dropping the
owning aggregate; used-down admission and all data-safety behavior remain
unchanged.

## 34. Task 7 resource-scoped revoke and drain amendment

### 34.1 Selected boundary

This later-controlling Task 7 amendment selects the bounded middle option for
the next cleanup batch:

```text
published/unpublished claim
  -> renew current cleanup + node lease
  -> revoke RecoveryResult delivery authority
  -> persist revoked
  -> bounded cancel/drain and exact Content lease release
  -> persist drained
```

A Broker-only revoke/drain method is not a closed product because it would not
advance the durable cleanup phase. Full target validation, remote deletion and
tombstoning are also excluded from this batch: cleanup-purpose permit issuance,
fresh target credential/root/marker/latch validation and the production Target
adapter must be designed and proved together before `RemoveOwnedJobDir` can be
called. This boundary therefore closes the delivery side through `drained`
without borrowing Task 8 runtime/scheduler credit.

### 34.2 Recovery-specific Content boundary

Content gains three narrow Recovery-only operations; none changes global Broker
admission or the backup-asset delivery arm:

1. `RevokeRecoveryResultGrantsTx` runs inside the caller-owned transaction. It
   accepts one exact Recovery job ID, the closed reason `recovery_cleanup` and a
   UTC timestamp, and moves only matching `recovery_result`
   `issued|active|draining` grants to `revoked`. Other Recovery jobs and every
   backup-asset grant remain unchanged.
2. `CancelRecoveryResultReads` runs only after the revocation transaction
   commits. Under the Broker mutex it marks every matching in-memory grant as
   revoked for read registration and cancels already registered reads. It does
   not wait and performs no database mutation.
3. `DrainRecoveryResult` repeats the scoped cancellation, waits only through the
   caller's bounded context, releases the exact retained Content leases after
   their reads have closed, removes only matching in-memory bindings and fails
   unless all matching durable grants are terminal with zero in-flight work.

The in-memory revoked-grant set closes the authorization-to-registration race:
a request authorized just before the durable revoke cannot register a new read
after commit. A crash discards all in-memory reads; existing Content startup
reconciliation remains the owner of stale request/accounting and lease repair.
This amendment does not claim the later crash/restart matrix complete.

### 34.3 Durable lifecycle ordering and renewal

`ResultLifecycleService` depends on a narrow interface implemented by the
Content Broker. Published cleanup uses this exact order:

1. For `claimed -> revoked`, transactionally lock the job, current cleanup node
   lease and ResultSet; validate the complete claim tuple and unexpired owner;
   extend the cleanup and node lease to the same later expiry; invoke
   `RevokeRecoveryResultGrantsTx`; and CAS phase `revoked`. Only after commit may
   the Broker cancel in-memory reads.
2. For `revoked -> drained`, renew and revalidate the same tuple before waiting;
   call the bounded Content drain outside any database transaction; then relock
   job, node lease and ResultSet, revalidate the fresh tuple, renew again and CAS
   phase `drained`.
3. A revoke transaction failure leaves grants and phase unchanged. A drain
   timeout/release/finalization failure leaves the durable phase `revoked` for
   retry. If ownership is lost after the external drain, the old owner performs
   no phase mutation and the fresh owner repeats the idempotent drain.

Unpublished workspace cleanup follows the same lease renewal and sequential
`claimed -> revoked -> drained` phase rules on the job row, but invokes no
Content operation because no ResultSet or RecoveryResult grant may exist.
Every method returns the renewed claim snapshot so a later phase must use the
latest lease expiry. An expired/replaced cleanup or node fence is rejected
before Content or database mutation.

### 34.4 Frozen focused boundary

Permanent product and tests stay inside already-manifested paths:

```text
backend/internal/backupasset/content/broker.go
backend/internal/backupasset/content/broker_test.go
backend/internal/backupasset/recovery/result_lifecycle.go
backend/internal/backupasset/recovery/result_lifecycle_test.go
```

No model, migration, API, runtime, settings, Provider, Repository, frontend or
manifest path changes. This batch stops at durable `drained`; cleanup-purpose
target validation, `validated|delete_started|deleted|tombstoned`, retryable
failure projection, release-on-terminal-outcome, orphan/quarantine scanning,
scheduling and whole Task 7 review remain open.

## 35. Task 7 cleanup target-validation amendment

### 35.1 Selected boundary and current production gap

This user-approved amendment is later-controlling over cleanup validation in
§8.2 and over §§33--34 where their retry/failure wording differs. The next
bounded batch advances both published and unpublished cleanup from durable
`drained` to durable `validated`, then stops:

```text
current drained owner
  -> renew and freeze cleanup validation authority
  -> observe the exact owned job directory outside the transaction
  -> re-lock and revalidate the complete durable authority
  -> CAS drained -> validated
  -> STOP before delete_started
```

The current repository cannot truthfully claim production target cleanup. Four
gaps are explicit inputs to the boundary rather than hidden implementation
assumptions:

1. `TargetCleanupPermit` currently wraps execution's
   `TargetMutationPermit`, whose `AttemptID`, `AttemptFence` and expected target
   revision describe a live recovery attempt rather than a cleanup owner.
2. `TargetPort` exposes destructive `RemoveOwnedJobDir` but no closed,
   observation-only `ValidateOwnedJobDir` operation, and no production
   `TargetPort` implementation exists.
3. `BackupAssetRecoveryPlan.EncryptedTargetRootLocator` exists, but
   `recoveryPlanRow` does not populate it. Settings have no production target
   root registry from which `RootID` can be freshly resolved.
4. The present marker binding is an HMAC over key version, job/root/root
   revision, workspace locator and creator/fence. There is no production marker
   document/codec with installation identity and random nonce, and the current
   creation path is proved only through fakes.

This batch may therefore freeze and prove the Recovery contract with recording
target fakes, but it must not wire runtime cleanup, claim fresh production SSH
credentials, claim marker-creation interoperability, or claim an end-to-end
remote cleanup path. Root registry/persistence, production SSH/SFTP adapter and
marker creation/validation interoperability remain separately approved work
before deletion can be considered.

### 35.2 Cleanup-specific permit and closed observation

Cleanup no longer borrows execution-attempt authority. It gains a private-proof
permit whose public shape is bound to one exact cleanup resource and operation:

```go
type CleanupResourceKind string

const (
    CleanupResourceResultSet CleanupResourceKind = "result_set"
    CleanupResourceWorkspace CleanupResourceKind = "workspace"
)

type TargetCleanupOperation string

const (
    TargetCleanupValidateOwnedJobDir TargetCleanupOperation = "validate_owned_job_dir"
    TargetCleanupRemoveOwnedJobDir   TargetCleanupOperation = "remove_owned_job_dir"
)

type TargetCleanupPermit struct {
    SchemaVersion       int
    Purpose             TargetPurpose
    Operation           TargetCleanupOperation
    ResourceKind        CleanupResourceKind
    ResourceID          string
    JobID               string
    CleanupOwner        string
    CleanupFence        uint64
    CleanupAttempt      uint64
    NodeID              uint
    NodeLeaseID         string
    NodeFence           uint64
    RootID              string
    RootLocatorDigest   string `json:"-"`
    TargetPathDigest    string `json:"-"`
    RootRevision        string
    MarkerBindingDigest string `json:"-"`
    UseLatchID          string
    ExpiresAt           time.Time
    proof               *targetCleanupPermitProof
}
```

For a published cleanup, `ResourceKind=result_set` and `ResourceID` is the exact
ResultSet ID. For an unpublished workspace, `ResourceKind=workspace` and
`ResourceID` is the job ID. In both arms, `Operation` is
`validate_owned_job_dir`, `Purpose` is `recovery_cleanup`, `UseLatchID` is the
permanent `schema_use_latch`, and expiry is no later than the renewed cleanup
and node lease. The permit binds the immutable marker digest, which in turn
binds historical workspace creator/fence provenance; it never rewrites or
pretends to be that execution provenance.

`AttemptID`, execution `AttemptFence`, source fence and
`ExpectedTargetRevision` are forbidden in this permit. A private constructor is
the only issuance path. Cross-purpose, cross-operation, cross-resource,
cross-object, expired, zero-fence, wrong-use-latch or structurally incomplete
permits fail before target I/O.

The private proof attests that the first transaction issued this exact local
product; its target-side validation is limited to shape, purpose, operation,
object and expiry. It must not open a hidden third database transaction. A
durable latch or fence can change after issuance, but the only resulting remote
work is a read-only observation and the second transaction rejects the stale
result. Future destructive cleanup authority requires a separate live
revalidation design.

The target boundary adds exactly one observation method and a closed success
product:

```go
type ValidateOwnedJobDirRequest struct {
    Object              TargetObjectRef
    MarkerBindingDigest string
}

type OwnedJobDirValidation struct {
    Object              TargetObjectRef
    MarkerBindingDigest string
    RootRevision        string
    TargetRevision      string
}

ValidateOwnedJobDir(
    context.Context,
    TargetCleanupPermit,
    ValidateOwnedJobDirRequest,
) (OwnedJobDirValidation, error)
```

Success means the adapter freshly resolved the cleanup-scoped target session,
matched the exact root identity/revision, checked every path component without
following symlinks, observed an owned directory rather than a symlink/special
entry, and validated the exact marker binding. The returned object, marker and
root revision must equal the request/permit and the target revision must be a
bounded nonempty observation. Missing directories, partial observations,
unknown marker formats, marker/root drift, permission denial, timeout and
transport ambiguity are errors, never successful absence.

`ValidateOwnedJobDir` is strictly observational. It must not create, repair,
rename, chmod, touch or delete a directory or marker. The validation operation
permit is not accepted by `RemoveOwnedJobDir`; the later delete batch must issue
a distinct `remove_owned_job_dir` permit only after durable `delete_started`.
This amendment never invokes `RemoveOwnedJobDir`.

### 35.3 Two transactions around one external observation

Published and unpublished validation use the same three-boundary protocol. No
database transaction remains open during target I/O.

1. In the first short transaction, lock the job, then the exact active
   `recovery_cleanup` node lease, then the resource row. The published resource
   row is the ResultSet; the unpublished workspace cleanup tuple is already on
   the locked job row and introduces no second physical workspace lock.
2. Revalidate `drained`, the complete cleanup owner/fence/attempt and
   lease-expiry tuple, the node owner/fence/expiry, isolated terminal job shape,
   published ResultSet or unpublished no-ResultSet parity, immutable workspace
   locator/binding/marker provenance, exact plan/job root and object binding,
   and the permanent use latch. Renew cleanup and node leases to the same later
   expiry. Only then construct the exact object/request and private cleanup
   validation permit; commit and return the renewed claim.
3. Outside all transactions, call `ValidateOwnedJobDir` once with a bounded
   context whose deadline leaves time for the closing transaction. This call
   observes only; it cannot gain delete authority and cannot advance durable
   state.
4. In the second short transaction, repeat the same job -> node lease ->
   resource lock order. Revalidate the renewed claim, all job/resource/root/
   marker/latch facts used to issue the permit, permit expiry and the complete
   returned observation. Renew both leases again, then CAS only the same
   current owner/fences from `drained` to `validated`.

If ownership or either fence changes while the target observation is in flight,
the old owner performs no phase, failure or lease mutation after the call. The
new owner may repeat the idempotent observation. A result from an earlier
cleanup fence, a different ResultSet/workspace, a different object, or an
expired permit can never be reused. A successful CAS retains the current owner
and node lease for the separately approved delete batch; it does not enter
`delete_started` and does not release deletion authority prematurely. Durable
`validated` records completion of this observation step only; it is not a
reusable deletion proof. The later delete batch must freshly revalidate the
then-current target and durable authority before issuing any destructive
permit.

### 35.4 Retryable validation failure and lease release

A closed validation failure does not erase the already durable Content drain.
If the same cleanup owner/fence and node lease/fence are still current, one
short transaction records the retryable product and releases that exact node
lease atomically:

```text
published:
  ResultSet state = cleanup_failed
  cleanup_phase = drained
  cleanup owner/lease/node binding cleared
  cleanup fence/attempt retained

unpublished:
  workspace_phase = cleanup_due
  workspace_cleanup_phase = drained
  cleanup owner/lease/node binding cleared
  cleanup fence/attempt retained
```

This later-controlling retry rule preserves `drained` rather than resetting the
resource to `claimed`. A later claim increments cleanup fence/attempt, acquires
a fresh node lease/fence, re-enters the active published `revoking` state where
applicable, and resumes at `drained`. It does not repeat Content revoke/drain or
rewrite execution/marker provenance.

The existing paired `000069` workspace-cleanup transition guard predates this
rule and forces every cleared-owner retry to `claimed`. Preserving `drained`
therefore requires one narrow SQLite/PostgreSQL guard correction: an ownerless
row with positive prior cleanup fence/attempt may preserve its exact
non-tombstoned phase when a fresh claim increments both values and binds a new
active cleanup node lease/fence. Initial neutral claims still enter `claimed`;
no retry may advance or rewind its phase; expired active-owner takeover remains
unchanged. This modifies no column, table, model field or migration number and
must be proved by the same paired migration selector before it is used by Go.

The retryable arm covers target errors, incomplete or mismatching observations,
missing/invalid root or marker facts, and permanent-latch validation failure.
It returns only sanitized categories; raw locators, marker material, SSH output
and remote error text remain private. If the failure transaction cannot prove
the exact current tuple, it changes nothing. If a database/system failure
prevents the atomic release, the code reports the error and relies on lease
expiry/reconciliation rather than falsely claiming release or validation.

### 35.5 Frozen TDD and stop matrix

The later implementation plan must freeze selectors for at least this matrix:

1. Exact cleanup permit shape and private issuance; wrong purpose, operation,
   resource, owner/fence/attempt, node lease/fence, root/object/marker/latch or
   expiry is rejected before the recording target observes a call.
2. Published `drained -> validated` and unpublished `drained -> validated`
   success, with monotonic equal cleanup/node lease renewal, one external
   observation and unchanged execution marker provenance.
3. Published ResultSet parity and unpublished no-ResultSet parity, including
   cross-job/resource substitution and immutable workspace/root drift.
4. Missing directory, symlink/special entry, marker mismatch, root-revision
   drift, partial result, timeout/cancellation and transport ambiguity all avoid
   `validated` and use the retryable `drained` failure product when still
   current.
5. Takeover between issuance and observation, and between observation and final
   CAS, proves the old owner performs no durable mutation and never releases the
   fresh owner's node lease.
6. Retry after validation failure gets fresh cleanup/node fences, resumes from
   `drained`, and can later validate without repeating Content work.
7. Permanent latch loss before issuance causes zero target calls; loss or any
   durable drift before the final CAS prevents `validated`.
8. The recording target observes zero `RemoveOwnedJobDir` calls in every arm;
   `delete_started|deleted|tombstoned`, cleaned projection and terminal node
   lease release remain untouched.

Focused implementation stays within Recovery target/lifecycle contracts, their
tests, and the existing paired `000069` workspace-cleanup transition guard plus
its migration integration selector. It adds no model field, table, migration
number, settings key, API, runtime, Provider, Repository or frontend path. The
guard correction is required parity evidence for the approved retry product,
not new schema credit. Production root registration and plan persistence,
SSH/SFTP target adapter, marker codec/creation interoperability, deletion/
tombstone execution, orphan/quarantine handling, scheduling and whole Task 7
closure all require separate approval after this batch reaches durable
`validated`.

## 36. Task 7 production target-root registry and plan-snapshot amendment

### 36.1 Approved decision, precedence and stop point

On 2026-08-03 the user approved R1: a private typed registry stored as one
encrypted `system_settings` row per node/root pair. This section controls the
next Task 7 batch and narrows the earlier generic statement in section 12 that
target roots are ordinary `Sensitive: true` settings. They remain owned and
encrypted by `settings.Service`, but they are deliberately **not** public
`SettingDef` entries because the current public settings and config-export
surfaces can reveal or transport sensitive values.

The alternatives remain rejected:

- one encrypted JSON row for the complete fleet registry makes every lookup,
  edit and corruption event fleet-wide and creates unnecessary whole-document
  contention;
- an ordinary sensitive JSON setting would require changing `GetAll`, config
  export/import and frontend settings behavior before the root could be made
  private by construction;
- a new table violates the frozen twelve-table/`000069` boundary, while
  `Node.BackupDir` is a public backup namespace label rather than a recovery
  destination allowlist and cannot represent multiple safe roots.

This batch ends after the private registry can persist/resolve exact definitions
and `PlanService` stores an encrypted locator snapshot. It does not publish a
management route, compose a runtime, open SSH/SFTP, probe a remote root, create
or validate a marker, implement a target adapter, enter `delete_started`, or
invoke `RemoveOwnedJobDir`. Task 8 retains settings-transition and managed
lifecycle ownership; Task 9 retains API/RBAC/public safe-summary ownership.

### 36.2 Private row and key contract

The private key namespace is:

```text
backup_assets.internal.recovery_target_root.v1.<canonical-node-id>.<root-id>
```

`canonical-node-id` is the base-10 positive `uint` form with no sign or leading
zero. `root-id` is 1--32 lowercase ASCII characters, begins and ends with an
ASCII alphanumeric character, and otherwise accepts only lowercase
alphanumeric, `-` and `_`. The stricter registry grammar is an allowed subset
of the existing Recovery opaque-root contract and makes the dynamic key
unambiguous and safely bounded under the existing 128-byte settings key limit.
The full internal prefix is recognized by `IsInternalSettingKey` even when a
suffix is malformed, so no damaged or attacker-inserted private row can become
config-export material.

Each row value is one current `enc:v2:` ciphertext produced by
`secure.EncryptString` over this strict document:

```json
{
  "schema_version": 1,
  "node_id": 42,
  "root_id": "recovery-a",
  "safe_label": "Recovery volume A",
  "canonical_locator": "/srv/xirang-recovery",
  "locator_digest": "<64 lowercase hex>"
}
```

The plaintext document is bounded before encryption, decoded with exactly one
JSON object, rejects unknown and duplicate fields, and requires every declared
field exactly once. The key-derived node/root identity must equal the document
identity. `locator_digest` is recomputed rather than trusted. A plaintext row,
legacy `enc:v1:` row, empty value, corrupt ciphertext, unsupported schema,
trailing JSON, substituted ciphertext or mismatched key/payload/digest is an
unavailable private record. This unreleased registry has no legitimate v1
population, so accepting v1 and amending the bootstrap migration allowlist
would add compatibility and manifest scope without a user to serve.

The strict target-root digest is lowercase SHA-256 over the existing
length-framed encoding:

```text
xirang/recovery/target-root/v1
canonical decimal node ID
root ID
canonical locator
```

Including node and root identity prevents the same remote path string from
becoming a portable cross-node binding. `TargetPathDigest` continues to bind
the resulting root digest and a separate canonical relative locator under its
existing domain.

The registry record is not a GORM model with decryption hooks. Private
`settings.Service` methods explicitly decrypt only the exact row being resolved
or the bounded rows being listed, validate the complete record, and return a
typed private product whose locator and digest have `json:"-"`. The persisted
`model.SystemSetting` remains ciphertext. No generic settings cache contains a
locator.

### 36.3 Registration and canonical locator rules

The typed boundary is conceptually:

```go
type RecoveryTargetRootDefinition struct {
    NodeID    uint
    RootID    string
    SafeLabel string
    Locator   string `json:"-"`
}

type RecoveryTargetRootSummary struct {
    NodeID    uint
    RootID    string
    SafeLabel string
}

type RecoveryTargetRootResolution struct {
    NodeID        uint
    RootID        string
    SafeLabel     string
    Locator       string `json:"-"`
    LocatorDigest string `json:"-"`
}
```

`RegisterRecoveryTargetRootTx` and `DeleteRecoveryTargetRootTx` require the
caller's existing transaction. Registration uses a narrow `nodes.id,archived`
projection and accepts only one existing non-archived node; it never loads or
decrypts node credentials. Re-registering an identical definition is an
idempotent no-op. Re-registering the same node/root with a new locator is an
explicit rotation: it atomically replaces that one encrypted row and produces
a new digest while leaving sibling roots and every existing plan untouched.
Delete targets exactly one constructed key and cannot use a broad unescaped
prefix delete.

`ListRecoveryTargetRoots` returns only complete safe summaries for one active
node, sorted by `root_id`. It is bounded at 64 roots and fails the entire list
on any malformed candidate; it never returns a partial list that could hide a
damaged allowlist entry. This is a typed service product for later Task 9 use,
not approval to add a route in this batch. Default-root selection is also a
later Task 8/9 safe-reference concern and is not encoded by choosing a locator
implicitly.

The lexical locator contract is POSIX and remote-target specific:

- valid UTF-8, 2--4096 bytes, absolute, and byte-for-byte equal to its canonical
  `path.Clean` form;
- not `/`, no NUL, backslash, ASCII control character, trailing slash, empty
  component, `.` or `..` component;
- no silent whitespace trimming or normalization; what was registered is what
  the digest binds.

This is only lexical canonicalization. Root existence, ownership/mode,
non-symlink components, mount/device identity, free-space facts and live
revision remain mandatory products of the later SSH/SFTP target probe. A valid
registry entry is an allowlisted locator, not proof that the remote filesystem
is currently safe.

Generic boundaries remain closed by construction:

- the dynamic keys are absent from `registry`/`registryMap`, so `Registry` and
  `GetAll` never enumerate or decrypt them;
- `GetEffective`/`resolveValue` return the internal-setting closed product;
- `Validate`, `Update`, `UpdateMany`, `Delete`, BatchUpdate/reset and config
  import reject the unknown private key;
- config export skips every `IsInternalSettingKey` row before applying
  `include_secrets`, so neither ciphertext nor key is exported in either mode;
- registry methods do not log the definition, ciphertext, locator, label or
  underlying decryption/JSON error.

Task 8 must later wrap registration/rotation/deletion in the established
validate -> drain -> persist -> install/rollback transition. The low-level Tx
methods intentionally do not claim that orchestration credit.

### 36.4 Plan resolver and immutable snapshot flow

`PlanServiceDependencies` gains one required narrow resolver. The Recovery
package depends only on the method contract; production composition can supply
`settings.Service` and tests can supply a recording fake:

```go
type RecoveryTargetRootResolver interface {
    ResolveRecoveryTargetRootTx(
        context.Context,
        *gorm.DB,
        uint,
        string,
    ) (settings.RecoveryTargetRootResolution, error)
}
```

The constructor rejects a nil resolver. No permissive production fallback may
copy a locator from `CreatePlanRequest`, `Node.BasePath`, `Node.BackupDir`, an
environment variable or a default. The request continues to contain only the
server-derived target binding and its private digest; it gains no raw locator.

New-plan creation retains its existing transaction and ordering:

1. normalize and validate the request, compute idempotency/intent digests, and
   check for an existing idempotent plan;
2. for a genuine new plan, load and revalidate the frozen source and selected
   entries first, so an invalid source cannot cause unnecessary private-root
   decryption;
3. resolve the exact active `NodeID + RootID` through the same `tx`, recompute
   the canonical locator digest, and require exact equality with the request's
   `RootLocatorDigest` and returned identity;
4. pass the private resolution separately to `recoveryPlanRow`; assign
   `EncryptedTargetRootLocator` in memory and let the existing plan
   `BeforeSave` hook encrypt it together with the other private plan fields;
5. insert the plan and items atomically. Any resolver, canonicalization,
   digest, encryption or persistence failure rolls back every plan/item write.

The locator is intentionally absent from `planIntentDigest`: the existing
private `RootLocatorDigest` now has a single server-derived formula that binds
the exact locator. Hashing the plaintext again in a second intent domain would
not add authority and would enlarge the number of places that handle it.

An idempotent replay does not consult the current registry. Before returning
the replay acknowledgement, it validates the decrypted persisted snapshot as
a canonical locator, recomputes its digest with the row's node/root identity,
and requires exact equality with `RootLocatorDigest` plus the existing binding
digest. This produces the required behavior across registry rotation/removal:

```text
old plan + same idempotency intent -> validate and replay old snapshot
new plan + old root digest         -> target changed, zero writes
new plan + new root digest         -> snapshot new locator
malformed old plan snapshot        -> fail closed, never rehydrate from registry
```

Existing execution, delivery and cleanup read the plan snapshot through the
model hook. They never resolve a current registry locator in place of that
snapshot. Later live target checks compare the immutable plan root identity and
remote root revision; registry removal cannot strand cleanup of plaintext that
was created under an already-authorized plan.

### 36.5 Closed errors, privacy and failure semantics

The settings boundary distinguishes a safe missing/ineligible-root sentinel
from an unavailable private-state sentinel. It preserves
`context.Canceled`/`context.DeadlineExceeded` but never wraps a locator,
ciphertext, document, key suffix or raw crypto/DB/JSON message into a returned
domain error.

`PlanService` maps missing/archived identity and expected-digest mismatch to a
new stable target-changed conflict. Private-state/DB/crypto failure maps to the
existing generic plan-unavailable category. Context cancellation remains
context cancellation. No error text may include the safe label either; labels
are presentation data, not diagnostic correlation.

Recognizable fake values must prove absence from:

- `CreatePlanResult` JSON and any model/handler DTO JSON;
- generic settings responses and both config-export modes;
- returned errors and captured structured logs;
- plan intent/binding diagnostics, audit products and metric labels.

The raw database assertion is made through a hook-free scalar query. It must
show an `enc:v2:` registry document and an encrypted plan snapshot while the
normal typed reload returns the original canonical locator. No test may mistake
the model's post-load plaintext field name for evidence of unencrypted storage.

### 36.6 Frozen TDD, race and stop matrix

The implementation plan must preserve genuine RED before product edits and
freeze at least this matrix:

1. registration/resolve/list/rotation/delete for multiple roots on one node and
   the same root ID on different nodes; exact identical registration is a
   no-op and one rotation cannot alter a sibling row;
2. missing/archived node, malformed root ID/label, root `/`, relative,
   noncanonical, control/NUL/backslash, trailing-slash and overlong locators all
   fail before persistence;
3. plaintext, v1, corrupt, unknown-field, duplicate-field, trailing-document,
   key/payload/digest-swapped and oversized records fail without a partial safe
   list or resolution;
4. generic Registry/GetAll/GetEffective/Validate/Update/Delete and config
   export/import cannot read, accept, reset or transport the private key/value,
   including `include_secrets=true`;
5. new plan resolves once inside its transaction, stores encrypted locator,
   and reloads the exact canonical snapshot; root/digest/identity substitution,
   missing resolver and resolver/crypto error make zero plan/item writes;
6. replay validates and uses the persisted snapshot without a resolver call;
   registry rotation/removal cannot rewrite it, and a malformed snapshot never
   falls back to the current registry;
7. concurrent rotation/new-plan tests accept only a fully old or fully new
   exact tuple: success stores the requested tuple or the request receives the
   target-changed product, never a cross-bound locator/digest;
8. existing source idempotency, rollback, plan creation, authorization,
   cleanup validation and full Recovery tests remain green; no target method,
   SSH/SFTP call, marker method or `RemoveOwnedJobDir` call occurs.

The exact product/test paths are existing manifest paths only:

```text
backend/internal/settings/service.go
backend/internal/settings/service_test.go
backend/internal/backupasset/recovery/service.go
backend/internal/backupasset/recovery/service_test.go
backend/internal/api/handlers/config_handler_test.go
```

No model, migration, bootstrap, settings handler, runtime, target, Provider,
Repository, frontend, `go.mod` or root-level `recovery/` path is in this batch.
The exact manifest remains 9 Phase-1 + 55 create + 81 modify = 145 unique paths.

## 37. Task 7 recovery workspace marker codec amendment

### 37.1 Approved boundary and precedence

On 2026-08-04 the user approved the bounded marker design: implement one
strict authenticated marker codec and bind the existing `CreateOwnedJobDir`
and `ValidateOwnedJobDir` contracts to immutable marker creator provenance.
This section is later-controlling over the generic marker wording in sections
7.1 and 35.1. It does not widen the batch into a concrete SSH/SFTP target,
remote filesystem mutation, deletion, tombstone or runtime composition.

Three boundaries were considered. A pure codec was rejected because it would
not prove that creation and cleanup validation consume the same authority. A
codec plus concrete SSH/SFTP was rejected because credential resolution,
component `lstat`, fixed-name selection, atomic write and remote race handling
form a separate security batch. The selected middle boundary closes the
document and current TargetPort interoperability while retaining recording
targets for I/O.

The batch ends when codec-backed tests can create one document from a valid
write permit/request and validate those exact bytes from a separately issued
cleanup permit/request. It does not claim that production has written or read
the document remotely.

### 37.2 Exact private document

The marker is one bounded JSON object with exactly these fields:

```json
{
  "schema_version": 1,
  "key_version": 1,
  "installation_id": "<64 lowercase hex>",
  "job_id": "<32 lowercase hex>",
  "root_id": "recovery-a",
  "root_revision": "<bounded opaque revision>",
  "ownership_nonce": "<43 canonical unpadded base64url characters>",
  "marker_binding_digest": "<64 lowercase hex>",
  "authentication_tag": "<64 lowercase hex>"
}
```

The encoded document is at most 2048 bytes. The nonce is exactly 32 bytes from
`crypto/rand.Reader` in production and is injectable only as an `io.Reader`
dependency for deterministic tests and entropy-failure proof. The decoder
accepts exactly one object, requires every declared field exactly once and
rejects unknown or duplicate members, missing fields, trailing JSON and a
noncanonical nonce. It validates typed values before any comparison or result
is returned. Object-member order and insignificant JSON whitespace are not an
authority product: authentication is over the canonical typed body rebuilt by
the codec after strict decoding.

The document deliberately omits raw root/workspace locators, marker creator
identity/fence, current attempt/cleanup owner, node lease, credential, proof or
source facts. Creator provenance is supplied separately by the private target
request/permit and is authenticated indirectly through the existing marker
binding.

### 37.3 Installation identity and authentication

No durable installation-ID facility exists outside the already required,
installation-stable Recovery Cleanup Ownership key. Adding a table or ordinary
setting would enlarge both schema and disclosure surfaces without increasing
ownership strength. The codec therefore derives a pseudonymous installation
identity as lowercase hex HMAC-SHA256 under the exact domain:

```text
xirang/recovery/workspace-marker-installation/v1
```

The domain-key version is not part of that derivation, so a wrapping-envelope
rewrap leaves the installation identity unchanged. The ownership domain is
non-rotatable; if its material is lost or replaced, the new identity cannot
validate an old marker, which is the required fail-closed behavior.

The authentication tag uses the same key and a separate exact domain:

```text
xirang/recovery/workspace-marker-document/v1
```

It authenticates the canonical JSON body containing every wire field except
`authentication_tag`. Verification decodes both 64-hex values and uses
`hmac.Equal`; ordinary string equality is not the authentication boundary.
The existing DB `xirang/recovery/workspace-marker/v1` HMAC remains unchanged.

Before encode, the codec loads the active ownership key, validates the write
permit/object, then recomputes the DB marker binding from key version, job,
root, root revision, private workspace locator and immutable creator/fence.
The request digest must match exactly before entropy is consumed. Before
decode authentication, it reads the document key version, loads that exact key
with `ByVersion`, validates the cleanup permit/object/creator tuple, recomputes
the same DB marker binding and requires exact document/request/permit parity.

### 37.4 TargetPort contract binding

`CreateOwnedJobDirRequest` and `ValidateOwnedJobDirRequest` gain private
`MarkerCreatorID` and `MarkerCreatorFence` fields. Marker binding, creator facts
and every marker-bearing result field are explicitly `json:"-"`. A new
`TargetWritePermit.ValidateOwnedJobDirRequestAt` checks the write purpose,
object and closed marker-request shape before codec work.

`TargetCleanupPermit` gains the same private creator fields. They participate
in its local proof digest, shape validation and exact validation-request check.
`ResultLifecycleService` obtains them only from the locked job's immutable
`workspace_owner/workspace_fence`; it carries them through both transactions
as part of `recoveryCleanupTargetBinding`. A changed creator tuple therefore
invalidates both the issued permit and the closing binding comparison.

`WorkerCoordinator` fills creation creator facts from the locked job. On the
initial reservation these are the just-persisted creator values; on a reserved
takeover they remain the original creator rather than the current attempt.
`markWorkspaceMarkerCreated` requires request creator parity before assigning
the current validation-attempt tuple. This preserves the existing split:

```text
marker creator provenance: immutable workspace_owner/workspace_fence
marker validation provenance: current validation attempt/fences
current mutation authority: current attempt/source/node/latch permit
current cleanup authority: current cleanup/node/latch permit
```

The codec itself remains below TargetPort and returns no public DTO. A future
concrete adapter in the Recovery package may own it and supply its bytes to a
fixed remote marker file. This batch does not choose that filename or create a
generic marker read/write port.

### 37.5 Closed errors and privacy

Permit or request mismatch returns `ErrInvalidTargetPermit`. Malformed,
ambiguous, unauthenticated or substituted marker bytes return one stable
`ErrInvalidRecoveryWorkspaceMarker`. Key lookup/material and entropy failures
return one stable `ErrRecoveryWorkspaceMarkerUnavailable`. Context cancellation
and deadline preserve their original identity. No error wraps or includes key
source, random-reader, crypto, JSON, marker, nonce, creator or locator text.

The codec emits no logs. The decoded nonce never leaves codec-local state, and
successful validation returns no document or nonce. JSON reflection tests cover
all request, permit and result products. Recognizable test values must remain
absent from serialized products and returned errors.

### 37.6 Frozen TDD and stop matrix

The implementation plan must preserve genuine RED before product edits and
freeze at least:

1. deterministic create plus cleanup validation across independently issued
   write/cleanup permits, exact required wire fields and canonical nonce;
2. different generated nonces for distinct creates, while validation of an
   existing document consumes no entropy and returns no nonce;
3. empty/oversized, unknown/duplicate/missing/trailing document cases and every
   schema/key/install/job/root/revision/nonce/binding/tag substitution;
4. wrong key material/version, lost/unavailable key, short/failing entropy and
   context cancellation with stable sanitized errors;
5. wrong object/private locator, creator ID/fence, marker digest, purpose,
   resource or expired permit rejection before successful codec work;
6. Worker initial creation and reserved takeover use immutable creator facts;
   cleanup issuance binds the same facts and closing drift fails closed;
7. request/permit/result JSON contains no marker binding, creator, locator,
   nonce, key material or raw document;
8. all existing marker phase, takeover, cleanup validation and full Recovery
   tests remain green, with zero remote SSH/SFTP and zero
   `RemoveOwnedJobDir` calls.

Product ownership is limited to existing manifest paths:

```text
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/result_lifecycle.go
```

Focused tests stay in their existing colocated paths. No model, DDL, migration,
settings, secure/keyring, runtime, API, Provider, Repository, frontend or new
file path is in this batch. The global manifest remains 9 + 55 + 81 = 145.

## 38. Task 7 A1 exact-plan SFTP workspace-marker amendment

### 38.1 Approved split, precedence and stop point

On 2026-08-04 the user approved Scheme A for the remaining concrete target
work. It is deliberately split into three separately reviewed batches:

```text
A1 exact plan-snapshot session capability + real SFTP workspace marker
A2 remaining SFTP probe/write/verify/result-read operations
A3 destructive remote cleanup + durable terminal cleanup transitions
```

This section controls A1 and is later-controlling over the generic concrete-
target wording in sections 6.3, 7.1, 8.2, 35.2 and 37.4. A1 implements only
`CreateOwnedJobDir` and observational `ValidateOwnedJobDir` on the concrete
target. It does not implement or claim `ProbeRoot`, `Lstat`, `CreateDirectory`,
`WriteAtomic`, `Delete`, `Verify`, `OpenOwnedResult` or
`RemoveOwnedJobDir`; those methods remain closed-unavailable without opening a
session until their own approved batch.

Three boundaries were considered. Combining every SFTP method and deletion was
rejected because it would mix session authority, ordinary file semantics and
irreversible cleanup in one review. A marker-only adapter that resolves the
current target-root registry was rejected because registry rotation could
redirect an old plan. A generic SFTP/path helper was rejected because it would
reopen arbitrary-path authority below the closed `TargetPort`. The selected A1
boundary supplies the missing exact-plan capability and proves one complete
marker create/read protocol before any ordinary payload or destructive method
is enabled.

A1 ends after both concrete marker methods pass focused checks. It performs no
runtime/main composition, settings transition, API/frontend work, payload
write, remote workspace removal, cleanup `delete_started|deleted|tombstoned`,
terminal lease release, orphan adoption/quarantine scan or Git delivery action.
There is no polling heartbeat, including no fifteen-minute heartbeat.

### 38.2 Private exact-plan session binding

The current permits contain safe IDs and digests but no usable remote root.
They must not grow a public locator field. Instead their package-private proofs
carry one immutable `recoveryTargetSessionBinding` conceptually containing:

```go
type recoveryTargetSessionBinding struct {
    PlanID                 string
    PlanBindingDigest      string
    NodeID                 uint
    NodeRevision           string
    CredentialRevision     string
    RootID                 string
    RootLocator            string // private, never JSON/log/audit
    RootLocatorDigest      string
    RootRevision           string
    bindingDigest          string // private length-framed digest of all above
}
```

`NodeRevision` is exactly `plan.TargetBaseRevision` and
`CredentialRevision` is exactly `plan.CredentialScopeRevision`. The private
binding digest is lowercase SHA-256 over the existing length-framed encoding,
in the displayed field order excluding `bindingDigest`, under the exact domain:

```text
xirang/recovery/target-session-binding/v1
```

The binding is constructed only while the exact plan row is locked. GORM's
`AfterFind` hook has already decrypted `EncryptedTargetRootLocator` into its
in-memory field; the constructor neither decrypts that value again nor performs
a scalar/hook-free load. It requires the executed plan's complete identity and
recomputes, without trimming or normalization:

```go
settings.RecoveryTargetRootLocatorDigest(
    plan.TargetNodeID,
    plan.TargetRootID,
    plan.EncryptedTargetRootLocator,
)
```

The recomputed value must equal `plan.RootLocatorDigest`. An `enc:v2:` value,
malformed/noncanonical plaintext, plan/root/node substitution or digest mismatch
fails before session resolution or remote I/O. Most importantly, neither the
binding constructor nor the concrete target calls
`ResolveRecoveryTargetRootTx`, `ListRecoveryTargetRoots` or any generic setting
reader. Rotation or removal of the registry changes new plans only; it cannot
redirect or rehydrate an old plan.

`targetMutationPermitProof` carries the binding for write authority.
`targetCleanupPermitProof` carries it and includes its private binding digest in
the proof seal. Permit validation requires exact equality between the public
node/root/revision fields and the private binding before a target method may
open a session. Copying a binding from another plan, changing its plaintext
locator or replacing only its digest invalidates the permit.

`PrepareFirstWrite` derives the binding from the same locked plan used to arm
the mutation and carries it out of the transaction only in the private proof.
The cleanup first transaction similarly adds the binding to
`recoveryCleanupTargetBinding`; the closing transaction rebuilds it from the
relocked plan and exact-compares it with the issued binding. No database
transaction remains open during SSH/SFTP I/O.

The binding's node and credential revisions are the existing immutable plan
products. A narrow injected node-session resolver must return exactly one
non-archived node capability whose node ID, node revision and credential-scope
revision match them. It returns the private `model.Node` only to the session
factory. A1 does not invent a second node-revision formula or accept silent
revision drift; Task 8 owns production construction of that resolver and its
managed runtime wiring.

The private seam is conceptually exact:

```go
type recoveryTargetNodeSession struct {
    Node                 model.Node
    NodeRevision         string
    CredentialRevision   string
}

type recoveryTargetNodeSessionResolver interface {
    ResolveRecoveryTargetNodeSession(
        context.Context,
        uint,
        TargetPurpose,
    ) (recoveryTargetNodeSession, error)
}
```

The resolver receives only the binding's node ID and the target-selected exact
purpose. It has no root ID, root locator or registry access. The factory rejects
the returned capability unless all three identity/revision facts equal the
private plan binding before it calls NodeDialer.

### 38.3 Purpose-exact SSH/SFTP session factory

The concrete adapter owns a private session factory, not an `*sftp.Client`
supplied by a handler or caller. For A1 it has exactly two entry paths:

```text
CreateOwnedJobDir   -> recovery_write   -> sshutil.PurposeRecoveryWrite
ValidateOwnedJobDir -> recovery_cleanup -> sshutil.PurposeRecoveryCleanup
```

The target method selects the purpose; no request field can choose or downgrade
it. The factory resolves the exact node capability described in section 38.2,
calls `sshutil.NodeDialer.Dial` with that exact purpose, lets NodeDialer derive
the matching credential-audit action, and then calls `sftp.NewClient`. The job
ID may be used only as a safe correlation ID. Raw host, username, credential,
locator, marker and remote error text are never audit metadata.

For testability without changing sshutil, the factory depends on the private
one-method shape already implemented by `*sshutil.NodeDialer`:

```go
type recoveryTargetNodeDialer interface {
    Dial(
        context.Context,
        model.Node,
        string,
        sshutil.DialAuditContext,
    ) (*ssh.Client, error)
}
```

No exported constructor accepts an alternate raw SSH client or arbitrary
purpose. An internal test constructor may replace the node resolver, dialer and
SFTP-client opener independently, but production construction accepts the real
resolver plus `*sshutil.NodeDialer`.

If SFTP construction fails, the SSH client is closed. A successful factory
returns one package-private session whose `Close` closes SFTP and SSH exactly
once and preserves the first close error. The A1 session surface is limited to
the exact operations needed by the protocol: `RealPath`, `Lstat`, `Stat`,
`Mkdir`, `Chmod`, `Open`, exclusive `OpenFile`, standard `Rename`, exact-temp
`Remove`, and `Close`. Its file surface is `Read`, `Write`, `Stat`, `Sync` and
`Close`. It exposes no command execution, recursive remove, directory remove,
glob, generic upload or POSIX-overwrite rename.

After a session opens, a cancellation watcher closes it to unblock pkg/sftp
operations, which do not otherwise accept a context. The watcher is stopped and
joined on every ordinary return, and session close is idempotent. If cancellation
or deadline wins, the original `context.Canceled` or
`context.DeadlineExceeded` identity wins over the resulting transport error.
If an otherwise successful call cannot close the session cleanly, it returns a
sanitized unavailable result; retry is safe through exact marker idempotency.

### 38.4 Fixed namespace, modes and canonical component checks

The only final marker filename is:

```text
.xirang-recovery-owner-v1
```

An in-progress marker uses the same-directory reserved form:

```text
.xirang-recovery-owner-v1.tmp-<64 lowercase hex SHA-256 of new marker bytes>
```

The final name and the complete temporary prefix are private adapter constants.
A2 must reject a restored top-level item that collides with either reserved
name; A1 does not alter already frozen operation rows or write payload data.
Only a temporary path proved to have been exclusively created by the current
call may be best-effort unlinked in A1. That bounded temp cleanup is part of the
write protocol; it is not workspace deletion and never calls
`RemoveOwnedJobDir`.

The root-relative object accepted by both marker methods is exactly
`jobs/<permit.JobID>`. It must equal the request's private locator byte for byte
and recompute the request/permit target-path digest. Aliases, additional
components, a different job ID, absolute paths, `.`/`..`, backslashes or
normalization are rejected before remote mutation.

Remote path handling uses POSIX `path`, never host `filepath`. Before use, the
adapter walks every absolute prefix of the plan-snapshot root with `Lstat` and
`RealPath`. Every prefix must be a real directory, not a symlink or special
entry, and `RealPath(prefix)` must equal the exact canonical prefix. The final
root `RealPath` must equal the exact plan locator; a different canonical alias
is target drift, even when it reaches the same current directory.

The same `Lstat`/`RealPath` rule applies to the shared `jobs` directory, the
owned job directory and the marker path before and after the operation. A1 may
create a missing `jobs` directory and the missing exact job directory, one
component at a time. A directory created by A1 is immediately changed to and
verified as mode `0700`. An existing `jobs` directory must already be a real
canonical `0700` directory. An existing job directory is never repaired or
adopted: it is accepted only when it already contains an exact authenticated
marker for this request. The final and temporary marker files must be regular,
canonical and exactly mode `0600`; the adapter never chmods an unexpected
existing entry.

The registered root itself is observed, not chmodded. A1 rechecks its canonical
directory shape but does not claim to regenerate the preflight root/filesystem
revision; A2's `ProbeRoot` owns that live observation product. In A1,
`RootRevision` is accepted only when the locked plan, permit/session binding and
authenticated marker all agree. Standard SFTP v3 exposes no portable inode,
device or hardlink-count proof, so A1 makes no such claim.

### 38.5 No-overwrite marker creation protocol

`CreateOwnedJobDir` follows this exact order:

1. Normalize the context, validate the write permit/request and exact private
   session binding, and reject any mismatch before resolving a node or opening
   SSH.
2. Open a `recovery_write` session. Validate the complete root and existing
   components without mutation. If the exact job directory and final marker
   already exist, bounded-read the marker and authenticate it against the write
   permit/request using a new codec `ValidateForCreate` path. Exact parity is an
   idempotent success and consumes no entropy; any other existing directory or
   marker fails closed.
3. When the exact job directory is absent, encode the marker before the first
   remote mutation. Key or entropy failure therefore creates no directory. Then
   ensure/create the shared `jobs` directory and exclusively create the exact
   job directory. A lost create race is accepted only if the winner's final
   marker authenticates against this exact request; a markerless or mismatched
   directory is never adopted.
4. Derive the reserved temp name from the encoded bytes. Create it with
   `O_WRONLY|O_CREATE|O_EXCL`, set and verify `0600`, write every byte with
   explicit short-write handling, call `File.Sync`, close it, reopen it, and
   bounded-read/compare the exact bytes. `Sync` support is mandatory; an SFTP
   server without `fsync@openssh.com` is closed-unavailable for A1.
5. Revalidate root, directory and temp components. Rename temp to the fixed
   final name using standard SFTP v3 `Rename`, whose destination must not exist.
   `PosixRename` is forbidden because pkg/sftp documents that it replaces the
   destination. A rename conflict or ambiguous error never authorizes overwrite.
6. Bounded-read the final marker, run `ValidateForCreate`, verify final mode and
   size, and repeat all canonical component checks. Only then close the session
   and return `OwnedJobDir`.

The adapter never uses `Create`, `WriteFile`, truncating open or an overwrite
rename for the final marker. On failure before rename it attempts to remove only
the exact temp file created by this invocation, while preserving the primary
error. It never removes the shared/job directory. On an ambiguous rename or
close outcome, a later retry may accept the final file only by fully
authenticating it; a leftover temp or markerless directory remains fail-closed
for the later orphan/quarantine design.

Both successful create and idempotent replay return a 64-lowercase-hex target
observation revision derived with a separate length-framed domain from node/root
identity, root revision, exact workspace locator, directory/marker modes, marker
byte count and SHA-256 of the authenticated marker bytes. It is an observation
token, not a filesystem inode or a replacement for the target-chain revision.

The exact observation domain and field order are:

```text
xirang/recovery/sftp-owned-workspace-observation/v1
canonical decimal node ID
root ID
root locator digest
root revision
exact workspace-relative locator
directory mode "0700"
marker mode "0600"
canonical decimal marker byte count
lowercase SHA-256 of marker bytes
```

### 38.6 Bounded observational validation

`ValidateOwnedJobDir` validates the cleanup permit/request/private session
binding before opening a session and then opens only a `recovery_cleanup`
session. It performs no `Mkdir`, `Chmod`, `OpenFile`, `Rename` or `Remove` call.

It requires the exact canonical root, `jobs` directory and owned job directory,
the exact `0700` directory modes, and one regular canonical `0600` final marker.
The marker's declared stat size must be in `1..2048`. It opens that one file and
reads through a `2049`-byte bound, requiring EOF at or before 2048 bytes; it
never uses an unbounded `ReadFile`. It compares pre-open, opened and post-read
size/mode/modification facts, re-runs `Lstat`/`RealPath` for every component,
and rejects any replacement or drift visible through SFTP.

Only after those checks does it call the existing strict codec
`ValidateForCleanup`. Success returns the exact request object and marker
binding, the immutable plan root revision and the same observation-revision
formula used by create. It returns no marker bytes, nonce, locator, node
capability or session product. Missing paths, short/oversized reads, a changed
file, symlink/special entry, wrong mode, canonical alias, codec mismatch or
partial observation are errors, never successful absence.

Cancellation closes the session. If cancellation races a rename or read, A1
does not infer the remote outcome from the transport error. Create relies on a
later authenticated idempotent read; cleanup remains at durable `drained` and
retries under a fresh lifecycle claim. A1 adds no periodic keepalive or
heartbeat.

### 38.7 Closed errors and privacy

The concrete boundary uses this exact sanitized classification:

| Condition | Returned identity |
|---|---|
| invalid/mismatched permit, request or private session proof | `ErrInvalidTargetPermit` |
| canonical root/object/mode/type drift or unexpected existing directory | `ErrRecoveryTargetChanged` |
| malformed, substituted or unauthenticated marker | `ErrInvalidRecoveryWorkspaceMarker` |
| marker key/codec dependency unavailable | `ErrRecoveryWorkspaceMarkerUnavailable` |
| node resolution, SSH/SFTP, sync, I/O, close or ambiguous remote failure | one new stable `ErrRecoveryTargetUnavailable` |
| caller cancellation/deadline | original context identity |

None of these errors wraps or includes a raw node, host, username, credential,
plan locator, remote path, marker/temp name, marker bytes, nonce, SFTP status,
SSH output or dependency error. The adapter itself emits no logs. NodeDialer
retains its existing safe credential audit, with purpose/stage/outcome and safe
IDs only.

Every new session field, resolver product, proof field, locator and marker byte
field is unexported or explicitly `json:"-"`. Reflection/serialization and
recognizable-secret tests cover permits, requests, results, errors and captured
logs/audit products. The session binding exists only for the short permit
lifetime and is never persisted as a new row or setting.

### 38.8 Frozen A1 TDD and verification matrix

The implementation plan must preserve genuine RED before product edits and
freeze at least:

1. a locked, hook-decrypted plan creates one exact private session binding;
   raw ciphertext, locator/root/node/revision substitution and a mismatched
   recomputed settings digest fail before resolver/factory calls;
2. registry rotation and deletion after plan creation do not change write or
   cleanup root bytes, while a new plan uses the new registry value; no A1 call
   invokes a target-root registry method;
3. write and cleanup resolve exact node/credential revisions and call
   NodeDialer/SFTP with only their exact purposes; wrong purpose, unavailable
   node, dial/client failure and close ordering are sanitized and leak-free;
4. absolute root and every root/object component cover missing, alias,
   noncanonical, symlink, file/special, wrong-mode and replacement cases through
   pre/post `Lstat` plus `RealPath` checks;
5. initial create produces `jobs/<job>` mode `0700`, fixed marker mode `0600`
   and an authenticated document; exact existing marker is idempotent, while an
   unexpected/mismatched/markerless existing directory is never adopted;
6. the exact temp protocol proves exclusive open, complete write, mandatory
   sync, close, bounded byte verification and standard no-overwrite rename in
   order; every injected failure leaves the final marker unmodified and removes
   at most the current exact temp;
7. observational cleanup reads at most 2049 bytes, rejects all path/mode/size/
   replacement/codec drift and makes zero mutation calls; create and cleanup
   return the same stable observation revision for unchanged marker bytes;
8. cancellation at resolve, dial, SFTP construction, write, sync, rename, read
   and close preserves context identity, closes each resource at most once and
   never infers an uncertain remote outcome;
9. all existing marker codec, first-write, takeover, cleanup-validation and full
   Recovery tests remain green. Every A2 method and `RemoveOwnedJobDir` opens
   zero sessions and remains closed-unavailable.

Permanent product ownership stays within already manifested paths:

```text
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/result_lifecycle.go
```

Focused tests remain in their existing colocated test paths. Existing
`github.com/pkg/sftp v1.13.10` is reused; `go.mod` is protected and untouched.
No model, DDL, migration, settings implementation, sshutil implementation,
runtime, main, API, Provider, Repository, frontend or new file path belongs to
A1. The global manifest remains 9 Phase-1 + 55 create + 81 modify = 145.

## 39. Task 7 A2a exact-plan regular-file Verify amendment

### 39.1 Approved trust-boundary split and stop point

On 2026-08-04 the user approved a further trust-boundary split inside A2. The
alternatives were a broad read/write split and one monolithic A2. Both were
rejected because preflight, execution verification and published-result delivery
derive authority from different durable aggregates, while ordinary create and
overwrite still lack a frozen expected-prior atomicity contract.

The approved sequence is:

```text
A2a exact executed-plan observation authority + regular-file present Verify
A2b directory creation + no-overwrite regular-file create
A2c concrete preflight probe under a separate draft observation capability
A2d published result read under resolver-bound delivery authority
A2e overwrite plus delete-oriented Lstat/absence observation contracts
A3  destructive Delete/RemoveOwnedJobDir + durable terminal cleanup/orphans
```

This amendment controls A2a only and is later-controlling over sections 6.3,
19.3 and 38 where they would otherwise imply that all observation methods open
together. A2a implements one concrete arm: `Verify` with a valid present
regular-file expectation. A valid absent expectation and concrete `ProbeRoot`,
`Lstat`, `CreateDirectory`, `WriteAtomic`, `Delete`, `OpenOwnedResult` and
`RemoveOwnedJobDir` remain closed-unavailable before session creation.

A2a ends after focused verification. It performs no remote mutation, marker
change, target-root registry lookup, schema/model/settings/sshutil change,
runtime or main composition, API/frontend work, cleanup state transition,
orphan/quarantine work, Git delivery action, polling loop or heartbeat.

### 39.2 Purpose-exact sealed verify authority

`TargetObservationPermit` remains the common safe-field carrier because the
preflight and result-delivery services already use its public shape with closed
fakes. A2a does not pretend that those two authorities are solved. Instead only
`TargetVerifyPermit` becomes sealed by a package-private proof conceptually
shaped as:

```go
type targetVerifyPermitProof struct {
    sessionBinding recoveryTargetSessionBinding
    JobID           string
    TargetMode      TargetMode
    bindingDigest   string
}
```

The proof is held by an unexported `json:"-"` pointer on the base observation
permit. `NewTargetVerifyPermit` and every later `TargetVerifyPermit.Validate*`
call require a non-nil, internally consistent proof. `NewTargetPreflightPermit`
and `NewTargetResultReadPermit` continue their current structural validation in
A2a, but their concrete methods remain unavailable and open zero sessions until
their own capability designs are approved.

The private proof digest uses the existing length-framed SHA-256 helper under:

```text
xirang/recovery/target-verify-permit-proof/v1
```

Its ordered fields are the observation schema version, canonical node ID,
purpose, root ID, root locator digest, target path digest, root revision, UTC
expiry, job ID, target mode and the complete private session-binding digest.
Mutation of any public or private field invalidates the proof. The only issuer
accepts `TargetPurposeVerify`, an exact valid executed-plan session binding, a
valid job ID and `isolated|in_place`; it returns no locator or session product.

The immutable `interruptedOperationHandoff` gains the private session binding.
It is constructed by `newRecoveryTargetSessionBinding` while the executed plan
row is held inside the existing locked durable load, after the model hook has
opened the plan locator and after the complete plan/job/item/envelope aggregate
has validated. The handoff digest binds the already-approved plan/job/object
facts; the new private field is additionally checked against those same facts
before permit issuance. No target I/O occurs while a DB transaction is open.

All four current issuance paths use one package-private helper rather than
reconstructing the proof independently: ordinary post-operation Verify, delete
pause Lstat, consumed-delete observation Lstat, and restart-adoption Verify.
The two Lstat consumers receive sealed authority for fake/contract continuity,
but concrete `Lstat` still returns unavailable before opening a session. This
does not activate delete behavior.

The existing session factory expands its allowlist by exactly
`TargetPurposeVerify`; write and cleanup behavior remain unchanged. It resolves
the exact node, compares node and credential revisions with the private plan
snapshot, and dials only `recovery_verify` with the safe job ID as correlation.
No caller may select another purpose or supply an arbitrary correlation value.

### 39.3 Present Verify data flow

The concrete call order is fixed:

```text
locked durable aggregate
  -> immutable handoff + exact-plan session binding
  -> sealed short-lived TargetVerifyPermit
  -> permit/object/present-expectation/namespace validation
  -> recovery_verify SSH/SFTP session
  -> pre-read canonical root/parents/final-file observations
  -> exact bounded streaming SHA-256 read
  -> opened/post-read stat equality + canonical rewalk
  -> exact digest/byte comparison
  -> stable opaque observation revision
  -> SFTP/SSH close
  -> closed TargetVerifyObservation
```

Before the session opens, the adapter validates context, target dependencies,
proof integrity, expiry, purpose, exact object digest, session-to-permit
node/root/root-revision parity, job ID and target mode. A present expectation is
required. A valid absent expectation is deliberately unavailable in A2a and
opens no session.

For isolated mode, the exact private locator must have
`jobs/<proof.JobID>/` as a component boundary prefix and must contain at least
one item component after the workspace. Only that first item component may not
equal the reserved final marker name or begin with the reserved temp prefix; a
deeper ordinary filename is not conflated with the workspace marker namespace.
The shared `jobs` and exact job parent remain canonical real directories with
mode `0700`. In-place mode accepts only the exact object from its mode-bound
durable handoff and does not reinterpret a literal `jobs/` prefix as an isolated
workspace or impose private mode on ordinary target parents. Both modes reject
absolute, empty, dot, dot-dot, backslash, normalized-alias or digest-mismatched
locators through the existing canonical `TargetObjectRef` contract.

### 39.4 Canonical bounded read and content-only fidelity

A2a reuses A1's root-prefix and canonical path machinery. The regular-file
helper is factored so marker reads retain their exact `0600` requirement while
payload verification can require only a real canonical regular file. This is a
targeted reuse change, not a new generic filesystem API.

The adapter validates every absolute root prefix, then walks each relative
parent with `Lstat` and `RealPath`. Every parent must be a real directory and
`RealPath(parent)` must equal the exact joined POSIX path. The final object must
be regular under `Lstat`, never a symlink or special entry, and its `RealPath`
must equal the exact final path. Missing or ambiguous parent/final observations
are never converted to absence.

The declared final size must equal `expectation.Present.Bytes` before open. The
adapter opens only that file, requires opened `Stat` to reproduce the bounded
pre-open `(size, mode, modtime)` snapshot, streams exactly the expected byte
count through SHA-256 without a size-proportional allocation, then attempts one
additional byte read. That final read must return exactly `(0, io.EOF)`; early
EOF, a returned extra byte, `(0, nil)`, a different error or any earlier read
failure is not success. After close it repeats `Lstat`, requires the same
snapshot, rewalks root/parents/final canonical facts and only then compares
lowercase SHA-256 and bytes with the expectation.

Mode and modification time participate only in same-call replacement detection;
they are not returned fidelity and are not compared with a source expectation.
A2a makes no inode, device, link-count, ownership, MIME, sparse-layout or
hardlink claim. Its product remains exactly content digest plus byte count.

Successful observation revision bytes are SHA-256 over the existing
length-framed encoding under:

```text
xirang/recovery/sftp-regular-file-observation/v1
```

The ordered fields are canonical node ID, root ID, root locator digest, root
revision, exact private relative locator, literal `regular`, lowercase content
digest and canonical decimal byte count. The public value is:

```text
sftp1:<unpadded base64url of the 32 digest bytes>
```

It is 49 bytes, bounded opaque, non-SHA-256-shaped and stable for the same
object/content. It changes across node/root/root revision/path/content/bytes.
It is not the persisted target chain, a source identity, a metadata-fidelity
digest or a proof that no intervening same-content replacement occurred. A2b
must use the same formula for a successful write result so the existing
write/verify revision-equality contract can remain exact.

### 39.5 Closed errors, cancellation and privacy

The A1 sanitized mapping remains controlling with these A2a refinements:

| Condition | Returned identity |
|---|---|
| structurally forged, expired, substituted or mismatched verify authority/object/expectation | `ErrInvalidTargetPermit` |
| missing/alias/symlink/non-regular/canonical/stat/size/content/replacement drift | `ErrRecoveryTargetChanged` |
| valid absent expectation or another deferred concrete arm | `ErrRecoveryTargetUnavailable`, before session |
| node resolution, SSH/SFTP, stat/open/read/close or ambiguous transport failure | `ErrRecoveryTargetUnavailable` |
| caller cancellation/deadline | original context identity |

The cancellation watcher closes SFTP/SSH to unblock package operations. Context
identity wins over resulting transport errors. A successful observation is not
returned when file or session close fails. Every resource closes at most once
and the watcher is joined before return.

No raw dependency error is wrapped or returned. The adapter emits no log and
returns no node, host, username, credential, root locator, object locator,
content, stat, session or SFTP status. Private proof/session/locator fields are
unexported or excluded from JSON. Recognizable-secret tests cover JSON, returned
errors and captured dial audit products.

### 39.6 Frozen A2a TDD and verification matrix

Implementation must preserve genuine RED before each product behavior and
freeze at least:

1. structural construction and every public/private proof mutation fail before
   resolver/session calls; a locked, hook-opened executed plan issues the only
   accepted binding, while ciphertext/current-registry/substituted plans fail;
2. all four verify-permit issuance paths carry the exact handoff binding, job ID
   and mode; no DB transaction remains open during target I/O;
3. only `recovery_verify` resolves/dials, with exact node/credential revisions
   and safe job correlation; wrong purpose/revision and all open/close failures
   are sanitized;
4. isolated and in-place namespace matrices reject cross-mode substitution,
   reserved marker names, invalid components and wrong private parent modes;
5. root/parent/final path matrices cover missing, alias, symlink, directory,
   special, wrong `RealPath` and pre/open/post replacement cases;
6. zero-byte and ordinary files prove exact bounded streaming, EOF, digest and
   byte behavior without size-proportional allocation or metadata claims;
7. observation token domain, field order, encoding, stability and separation
   are exact, and the closed observation validates against its expectation;
8. cancellation and every dependency failure close each resource once,
   preserve context identity and leak no recognizable sensitive value;
9. valid absent Verify and all seven deferred concrete methods open zero
   sessions and remain unavailable; the two A1 marker methods retain their
   existing behavior and no new mutation method is called;
10. existing A1, worker execution/adoption, whole Recovery and required real
    PostgreSQL behavior remain green under the focused normal/race/static gates.

Permanent product ownership remains inside already manifested files:

```text
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/executor.go
backend/internal/backupasset/recovery/worker.go
```

Tests remain colocated in their existing `*_test.go` files. A2a adds no path,
dependency, model, DDL, migration, setting, crypto key domain, sshutil change,
Provider/Repository/runtime/API/frontend change or manifest entry. `go.mod` and
`recovery/testdata/rsync_local_to_remote.json` remain protected and untouched;
the exact global manifest remains 9 + 55 + 81 = 145.

## 40. Task 7 A2b exact-plan no-overwrite regular-file create amendment

### 40.1 Approved scope and later-controlling boundary

On 2026-08-04 the user approved Scheme A and both detailed design sections.
The current selection remains regular-file-leaf-only: selecting a directory
expands to non-directory leaves, execution accepts only regular ordinary
entries, empty directories are not restored, and no directory metadata becomes
an operation or fidelity product. This section is later-controlling over the
generic directory/write wording in sections 6.3, 7.1, 19.3, 38 and 39.

A2b opens one concrete payload mutation arm: no-overwrite regular-file
`RecoveryOperationCreate`. In isolated mode only, missing parent directories
below the already owned `jobs/<jobID>` workspace may be derived from the exact
final object and created as part of that same call. In-place mode creates no
parent directory. The public `CreateDirectory` method remains closed and opens
zero sessions because no frozen directory operation exists to authorize it.
Keeping parent preparation inside `WriteAtomic` also makes every partial parent,
temp, rename and close outcome use the already durable unresolved-operation
contract rather than splitting one file create across independently ambiguous
remote calls.

Valid exact overwrite authority remains closed-unavailable before session
creation. `ProbeRoot`, `Lstat`, `CreateDirectory`, `OpenOwnedResult`, `Delete`
and `RemoveOwnedJobDir` remain unavailable. A2b does not add directory or file
metadata fidelity, empty-directory preservation, preflight, result delivery,
absence, overwrite, deletion, cleanup, runtime/main composition, orphan or
quarantine behavior, schema/model/settings changes, Git delivery or polling.

### 40.2 Exact item write authority

The existing durable first-write permit proves the permanent schema latch and
current job/attempt/source/node fences, and its workspace path is sufficient
for A1. It is not sufficient for a concrete item write: the current
`ordinaryItemWritePermit` rewrites the public target-path digest and reseals
without carrying the locked plan's private session binding or the exact item
operation facts. A2b closes that gap before opening `WriteAtomic`.

`TargetWritePermit` gains an unexported exact-item proof in addition to its
existing base mutation permit. Conceptually it contains:

```go
type targetItemWritePermitProof struct {
    sessionBinding recoveryTargetSessionBinding
    jobID           string
    targetMode      TargetMode
    object          TargetObjectRef
    operation       RecoveryOperationKind
    expectedPrior   ExpectedTargetIdentity
    expectedDigest  string
    expectedBytes   int64
    bindingDigest   string
}
```

One package-private issuer accepts the base write permit plus the complete
`interruptedOperationHandoff`. It requires the hook-opened executed plan, job,
item, operation, object and session binding to agree byte for byte; requires
`regular` for create/overwrite; binds the current public mutation permit,
including expiry and expected target-chain revision; and seals the proof under
the new length-framed domain:

```text
xirang/recovery/target-item-write-permit-proof/v1
```

The digest field order is the complete base mutation-proof digest, exact
session-binding digest, job ID, target mode, root ID, root-locator digest,
target-path digest, private relative locator, operation, expected-prior kind and
digest, expected-post digest and canonical decimal byte count. Mutation of a
public or private field invalidates the proof. The private locator and proof
remain excluded from JSON.

The ordinary loop passes the locked handoff, rather than only a caller-supplied
object, to the item issuer after the durable load transaction closes. The live
base validator still rechecks latch/job/attempt/source/node and current target
chain whenever the permit is admitted. Concrete `WriteAtomic` additionally
requires exact item proof parity with its request. A structurally valid exact
overwrite proof returns unavailable before resolver/session in A2b; a missing,
forged or substituted proof returns `ErrInvalidTargetPermit`. Workspace marker
permits and their A1 behavior remain unchanged.

### 40.3 Purpose-exact create data flow

The fixed call order is:

```text
locked durable aggregate + exact session binding
  -> sealed exact item write permit
  -> source regular stream opened by the executor
  -> request/proof/create/expected-absent validation
  -> 32-byte CSPRNG temp identity before remote mutation
  -> recovery_write SSH/SFTP session
  -> canonical root and mode-specific parent preparation
  -> final-absence observation
  -> same-directory exclusive temp create at 0600
  -> exact bounded stream + SHA-256 + one-byte EOF proof
  -> Sync + close + canonical reopen verification
  -> live permit, parents and final absence revalidation
  -> standard SFTP v3 Rename
  -> canonical final mode/content verification
  -> live permit revalidation + A2a sftp1 revision
  -> SFTP/SSH close
  -> closed TargetWriteResult
```

The target validates context, dependencies, complete proof, exact object,
literal create operation, `ExpectedTargetAbsent`, expected digest/bytes and
non-nil content before resolver use. It opens only `recovery_write`, resolves
the exact plan-snapshot node and credential revisions, and uses only the safe
job ID as correlation. Target I/O never occurs while a database transaction is
open. The source stream remains executor-owned and is closed there exactly as
in the existing ordinary-operation contract.

The adapter has a private injectable entropy reader; production uses
`crypto/rand`. It reads exactly 32 bytes before any remote mutation and encodes
them as canonical unpadded base64url. The same-directory basename is:

```text
.xirang-recovery-file-v1.tmp-<43-character base64url nonce>
```

The nonce, basename and path are never persisted or returned. If an existing
entry collides with the generated name, exclusive open fails and the adapter
does not remove it because ownership was never acquired. No global filename
reservation or source-path rejection is introduced. A generated temp equal to
the final basename is rejected before mutation.

### 40.4 Parent-directory contract

All path handling uses POSIX `path` and exact already-validated locator bytes.
The adapter never trims, cleans, reconstructs from a current registry or accepts
an absolute/cross-root path. Every absolute root prefix remains a canonical real
directory exactly as in A1/A2a.

For isolated mode, the exact object must be below
`jobs/<proof.jobID>/` and contain a final item component. The shared `jobs`
directory and exact job directory must already exist, be canonical real
directories and have mode `0700`; A1 owns their creation. Each deeper parent is
walked in order. An existing deeper parent must also be canonical, real and
`0700`, and is never chmodded. A missing deeper parent may be created only after
a fresh live-permit check, then immediately chmodded and verified as canonical
`0700`. If `Mkdir` reports an error, the adapter accepts a lost race only when
the exact path now validates as canonical real `0700`; a missing or ambiguous
path remains unavailable, while a conflicting entry is changed. A directory
created by this call is not removed on later failure.

For in-place mode every parent from the root to the final basename must already
exist as a canonical real directory. Parent mode is observed only for same-call
drift and is not fidelity; no missing parent is created and no existing parent
is chmodded or repaired. Missing, alias, symlink, file, special or canonical
drift returns `ErrRecoveryTargetChanged` before file mutation.

The complete parent chain is rewalked before temp creation, before rename and
after final verification. Visible replacement, mode drift in an isolated
parent or shape/canonical drift stops the call.

### 40.5 No-overwrite streaming and publish protocol

Before temp open and again immediately before rename, `Lstat(final)` must prove
exact absence. Any existing regular file, directory, symlink or special entry
is `ErrRecoveryTargetChanged`, including a regular file with the exact expected
digest and bytes. A2b never adopts, truncates, deletes, chmods or treats an
existing final as idempotent success. Missing is accepted only from the exact
SFTP not-exist identity; permission, transport and ambiguous errors do not
become absence.

The temp is opened with exactly `O_WRONLY|O_CREATE|O_EXCL`, chmodded to `0600`
and verified as a canonical regular file. The adapter streams exactly
`ExpectedBytes` through SHA-256 without size-proportional allocation, then
attempts one additional byte read. Success requires exactly `(0, io.EOF)` after
the expected byte count and exact lowercase `ExpectedDigest`. Short input,
extra input, `(0,nil)`, digest mismatch or any read/write failure cannot reach
rename.

After the write, `File.Sync` is mandatory. The adapter closes the handle,
reopens the temp read-only, verifies pre-open/opened/post-read bounded stat
parity, exact `0600`, bytes, digest and EOF, and rewalks the root/parents/temp.
It then revalidates the live permit and final absence. It publishes only with
pkg/sftp's standard `Rename`; `PosixRename`, overwrite rename, truncating open,
generic upload and fallback copy are forbidden. Standard SFTP v3 rename must
not replace an existing destination. Any rename error is ambiguous unavailable
and never authorizes overwrite or inferred success.

After successful rename, the adapter verifies the final as a canonical regular
`0600` file with exact bytes/digest and stable stat observations, rewalks every
component, and revalidates the live permit. The returned product is exactly:

```go
TargetWriteResult{
    BytesWritten:   request.ExpectedBytes,
    IdentityDigest: request.ExpectedDigest,
    TargetRevision: <A2a sftp1 token>,
}
```

The revision uses the already frozen
`xirang/recovery/sftp-regular-file-observation/v1` formula and exact final
object. Therefore the immediately following concrete present `Verify` must
produce byte-for-byte the same revision. No mode, mtime, owner, inode, device,
link count, MIME or sparse-layout fidelity is claimed. Newly created payload
files are deliberately `0600` because the frozen source product carries no
trusted file mode.

### 40.6 Failure ownership, cancellation and privacy

The adapter marks temp ownership only after its own exclusive open succeeds.
Before a confirmed successful rename, failure triggers best-effort removal of
only that exact temp while preserving the primary error. It never removes or
rolls back a parent or final object, and never removes an entry whose exclusive
ownership was not established. A process crash may leave parents or a private
temp; A3 owns later orphan/quarantine policy and receives no completion credit
from A2b.

The existing executor rule remains controlling: any `WriteAtomic` call error,
invalid result, final-verification failure or session-close ambiguity after the
call begins becomes the existing terminal
`operation_unresolved/write_result_invalid` disposition. The target chain does
not advance and the operation is not blindly retried. A valid result is returned
only after final verification, final live-authority validation and clean
session closure. A subsequent Verify error or mismatch uses the already frozen
observation-invalid/verification-mismatch arms.

Error identity is closed:

| Condition | Returned identity |
|---|---|
| forged/expired/substituted permit, proof, object or request | `ErrInvalidTargetPermit` |
| valid exact overwrite or another deferred method | `ErrRecoveryTargetUnavailable`, before session |
| final exists; parent/final alias, symlink, shape, mode or canonical drift | `ErrRecoveryTargetChanged` |
| entropy, resolver, SSH/SFTP, source read, write, sync, reopen, close or ambiguous rename failure | `ErrRecoveryTargetUnavailable` |
| caller cancellation/deadline | original context identity |

Context identity wins over transport errors. The existing cancellation watcher
closes SFTP/SSH to unblock operations and is joined. Each acquired file,
client and SSH resource closes at most once. The adapter emits no log and never
wraps raw dependency errors. Returned errors, JSON, audit/session products and
captured logs contain no node, host, username, credential, root/object locator,
content, temp name/nonce, SFTP status or digest input.

### 40.7 Frozen A2b TDD and verification matrix

Implementation preserves genuine RED before each product behavior and freezes
at least:

1. current item resealing without exact session/operation proof is rejected;
   only a locked hook-opened handoff issues an accepted permit, and every public
   or private substitution fails before resolver/session;
2. exact create versus valid deferred overwrite classification, purpose-exact
   node/credential resolution and absence of target I/O inside DB transactions;
3. isolated namespace and nested-parent creation order, `0700`, lost-race
   revalidation, no repair/rollback, plus in-place existing-parent-only behavior;
4. entropy-before-mutation, exact temp format, exclusive flags, `0600`, no
   collision deletion and no generated-temp/final alias;
5. zero-byte and ordinary bounded streaming, short/extra/zero-nil reads, digest,
   partial writes, Sync, close/reopen and pre/open/post stat matrices;
6. every existing-final type/content, concurrent final appearance, standard
   Rename only, ambiguous rename and final replacement/verification matrices;
7. exact `TargetWriteResult`, stable A2a revision equality with subsequent
   Verify and no metadata fidelity claims;
8. source/target/dependency/cancellation/close failure cleanup, at-most-once
   resource closure, existing unresolved projection and complete privacy scans;
9. public `CreateDirectory`, valid overwrite and all A2c--A3 methods remain
   zero-session unavailable while A1 and A2a behavior stays unchanged;
10. focused normal/race, A1+A2a+A2b combined, whole Recovery normal/race,
    required real PostgreSQL no-skip, vet, backend lint, owned format/diff,
    static/privacy, Trellis/JSON/JSONL, exact manifest, protected hashes and
    staged-zero gates all pass.

Permanent product ownership remains in already manifested files:

```text
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/executor.go
backend/internal/backupasset/recovery/worker.go
```

Tests remain in their corresponding existing `*_test.go` files. Planning and
evidence updates remain in the existing Task 7 directory. No new path,
dependency, model, DDL, migration, setting, crypto key domain, sshutil,
Provider/Repository/runtime/API/frontend change or manifest row is allowed.
`go.mod` and `recovery/testdata/rsync_local_to_remote.json` remain protected;
the exact global manifest stays 9 + 55 + 81 = 145. A2b ends at focused closure
and stops before A2c.

## 41. Task 7 A2c split concrete preflight amendment

### 41.1 Approved evidence-ownership split

The current `TargetProbeFacts` is a composite test product: it mixes facts a
target SFTP session can observe with Provider source access, capability and
policy revisions, security findings, protected-root overlap and reserve policy.
A concrete target adapter cannot honestly prove the latter fields. Echoing the
request or setting them to success would turn an internal fake contract into a
false security attestation.

The approved Scheme A therefore splits A2c in execution order:

```text
A2c1 exact draft-plan authority + target-owned probe + buildable external-evidence seam
A2c2 production source/policy evidence issuer/adapter + composite durable preflight
```

A2c1 is independently useful and reviewable: it makes the target evidence real
without claiming that the complete preflight is ready. A2c2 cannot inherit
completion credit and requires a separate implementation approval after the
A2c1 focused closure.

### 41.2 Draft-plan preflight authority

Executed-plan `recoveryTargetSessionBinding` remains unchanged. A2c1 adds a
separate `recoveryTargetPreflightSessionBinding` whose constructor accepts only
the hook-decrypted, exact `draft` plan read by `PreflightService`:

```go
type recoveryTargetPreflightSessionBinding struct {
    planID                 string
    planBindingDigest      string
    planTransitionRevision uint64
    targetMode             TargetMode
    nodeID                 uint
    nodeRevision           string
    credentialRevision     string
    rootID                 string
    rootLocator            string
    rootLocatorDigest      string
    rootRevision           string
    filesystemRevision     string
    targetPathDigest       string
    privateRelativeLocator string
    targetRevision         string
    preflightRevision      string
    bindingDigest          string
}
```

The binding digest uses
`xirang/recovery/target-preflight-session-binding/v1` and the exact field order
shown above. The locator digest and path digest are recomputed from the private
values. Empty, ciphertext-prefixed, noncanonical, expired or non-draft input is
invalid.

`TargetPreflightPermit` gains an unexported proof:

```go
type targetPreflightPermitProof struct {
    sessionBinding recoveryTargetPreflightSessionBinding
    requestDigest  string
    bindingDigest  string
}
```

The request digest binds object root/path/private locator, source/capability/
policy revisions and required bytes/inodes under
`xirang/recovery/target-preflight-request/v1`. The proof digest binds the full
public permit, session-binding digest and request digest under
`xirang/recovery/target-preflight-permit-proof/v1`. Only the package-private
`issueTargetPreflightPermit` constructs this wrapper from the observed draft
binding and exact request; the current structural `NewTargetPreflightPermit`
constructor is removed. Every `Validate*` call requires proof parity; JSON sees
neither proof nor private binding.

The caller-facing request remains structurally comparable with the plan, while
the sealed capability exists only in the service's canonical local copy:

```go
type TargetPreflightInput struct {
    // Existing caller fields, including Permit TargetObservationPermit.
    targetPermit TargetPreflightPermit
}
```

`PreflightService` canonicalizes the caller input first, reads and validates the
exact draft plan, issues `targetPermit`, and sets only this unexported field.
`TargetPreflightEvaluator.Evaluate` requires the sealed permit and public
`Permit`/`ProbeRequest` to match exactly. A caller cannot submit or overwrite
the private field across a package or serialization boundary.

`PreflightService.EvaluateAndPersist` keeps its three phases:

```text
read and validate exact draft plan
  -> derive private draft binding and seal exact target request
  -> close DB read; perform bounded target/source observations
  -> begin short transaction, lock/revalidate, insert snapshot and CAS plan
```

The service never accepts a caller-sealed proof and never opens target I/O from
an unobserved plan. Direct evaluator tests use a package-private test issuer;
production issuance stays in the service path.

### 41.3 Purpose-exact preflight session

The existing executed-session `Open` remains limited to write, verify and
cleanup. A distinct `OpenPreflight` accepts only the draft binding and literal
`recovery_preflight`, resolves the exact node and credential revisions, and
uses the opaque plan ID as correlation. It then uses the same SSH client for a
bounded fixed-command runner and SFTP.

The only commands are server-owned `id -u` and `id -G`, issued through
`sshutil.CommandRunner` with a 4 KiB stdout/stderr ceiling, 4 KiB record ceiling
and the earlier of context or permit expiry. No shell binary, generic command,
caller argv or caller path reaches the command runner. Decimal UID/GID output
is parsed strictly, bounded to a nonempty unique set, and never returned from
the target package. Any command error is sanitized; context identity wins.

### 41.4 Target-owned observation product

A2c1 changes the target port return type from the composite fake product to:

```go
type TargetRootProbeFacts struct {
    ObservedAt             time.Time
    ExpiresAt              time.Time
    RootRevision           string
    FilesystemRevision     string
    TargetRevision         string
    CredentialRevision     string
    RequiredToolsAvailable bool
    RootReal               bool
    RootCanonical          bool
    DeviceValid            bool
    MountValid             bool
    OwnerValid             bool
    ModeValid              bool
    HasSymlinkComponent    bool
    FreeBytes              int64
    FreeInodes             int64
    TargetExists           bool
}
```

It contains no source, capability, policy, security, overlap or reserve field.
A2c2 owns the composite product; A2c1 does not fill those facts with defaults.

Changing `TargetObservationPort.ProbeRoot` to this product would otherwise make
the existing evaluator uncompilable before A2c2. A2c1 therefore also freezes the
external-evidence seam, without implementing a production evidence source:

```go
type PreflightExternalEvidenceRequest struct {
    PlanID                    string
    PlanBindingDigest         string
    PlanTransitionRevision    uint64
    SourceRevisionDigest      string
    CapabilityRevision        string
    PolicyRevision            string
    FindingSetDigest          string
    TargetRootRevision        string
    TargetFilesystemRevision  string
    TargetRevision            string
    RequiredBytes             int64
    RequiredInodes            int64
}

type PreflightExternalEvidence struct {
    ObservedAt             time.Time
    ExpiresAt              time.Time
    SourceRevisionDigest   string
    CapabilityRevision     string
    PolicyRevision         string
    FindingSetDigest       string
    FindingDisposition     SecurityFindingDisposition
    SourceAccessible       bool
    OverlapsXirangRoot     bool
    OverlapsSourceRoot     bool
    ReservedBytes          int64
    ReservedInodes         int64
    proof                  *preflightExternalEvidenceProof
}

type preflightExternalEvidenceProof struct {
    requestDigest string
    bindingDigest string
}

type PreflightExternalEvidencePort interface {
    ObservePreflightEvidence(
        context.Context,
        PreflightExternalEvidenceRequest,
    ) (PreflightExternalEvidence, error)
}

func (evidence PreflightExternalEvidence) ValidateFor(
    now time.Time,
    request PreflightExternalEvidenceRequest,
) error
```

`PreflightExternalEvidenceRequest` contains only bounded scalar identities; it
contains no raw source/root/target locator, credential or command material. Its
digest domain is `xirang/recovery/preflight-external-evidence-request/v1` in the
field order above. The evidence binding domain is
`xirang/recovery/preflight-external-evidence-proof/v1`; it binds the request
digest and every returned field in declaration order. `ValidateFor` requires a
non-nil private proof, exact request digest, exact evidence digest, nonfuture
observation, live expiry, valid revisions/digests/disposition and nonnegative
reserve values.

`NewTargetPreflightEvaluator` requires both `TargetObservationPort` and
`PreflightExternalEvidencePort`. It observes target facts first, constructs the
exact scalar external request from the sealed target permit's draft binding and
the returned target revisions, validates the returned private proof, and only
then evaluates reasons/snapshot. Composite `ObservedAt` is the later of the two
observations; expiry is the earliest of snapshot TTL, permit, target facts and
external evidence. Missing ports, unsealed target input, unproved external
evidence and request/result substitution fail closed; there is no compatibility
echo or success default.

A2c1 defines no production issuer or adapter. Same-package tests define their
deterministic `issuePreflightExternalEvidenceForTest` issuer and fake in
`preflight_test.go`, using the production digest helpers
`preflightExternalEvidenceRequestDigest` and
`preflightExternalEvidenceProofDigest`. This keeps the split independently
buildable and the existing reason matrix executable. A2c2 replaces that
test-only source with a recovery-owned production adapter over independently
observed Provider/Repository evidence; Task 8 still owns runtime/main wiring.

The probe performs only:

1. strict permit/request/context validation before dependencies;
2. purpose-exact resolver, SSH and SFTP open;
3. bounded `id` observation;
4. `Lstat` plus `RealPath` for `/`, every root prefix and each existing target
   prefix, rejecting visible symlink/alias/non-directory drift;
5. `StatVFS` for root and each existing target parent, requiring one nonzero
   filesystem ID and stable filesystem identity;
6. a second complete root/target observation before returning; and
7. SFTP then SSH close, with close ambiguity blocking success.

Mode is valid only for a directory that is not world-writable. Owner validity
requires the effective UID (root is allowed) or one effective GID to receive
write and execute through POSIX mode bits; ACL-only access is not claimed and
fails closed. Available bytes are `Bavail * Frsize`, available inodes are
`Favail`, and every conversion rejects uint64 overflow above `math.MaxInt64`.

An exact not-exist result at the first missing target component means the final
target is absent. Permission, unsupported extension or transport errors never
become absence. An existing final entry is classified without following it;
regular, directory, symlink and special are all target-exists observations.

### 41.5 Stable observation revisions

Each revision is raw-url-base64 SHA-256 with a non-SHA-shaped prefix:

```text
sftpr1:<43 chars>  xirang/recovery/sftp-root-observation/v1
sftpf1:<43 chars>  xirang/recovery/sftp-filesystem-observation/v1
sftpt1:<43 chars>  xirang/recovery/sftp-target-observation/v1
```

Root fields are node ID, root ID/digest, private canonical locator, mode,
UID/GID and filesystem ID. Filesystem fields are filesystem ID, block sizes,
total blocks/files, flags and name maximum; volatile free counters are excluded.
Target fields are root revision, private relative locator and literal absent or
observed kind/size/mode/UID/GID/mtime. Length framing is mandatory. Metadata in
the target token improves drift detection but is not returned as restored
fidelity.

The second observation must reproduce all revision inputs. A mismatch is
`ErrRecoveryTargetChanged`; a dependency ambiguity is
`ErrRecoveryTargetUnavailable`.

### 41.6 A2c2 production external-evidence boundary

A2c2 implements the production issuer/adapter behind the
`PreflightExternalEvidencePort` frozen in A2c1. Its request carries only the
closed plan/source/capability/policy identities and target observation digests.
Using existing Provider/Repository authority, its result privately proves source
accessibility, exact source/capability/policy/finding revisions and disposition,
Xirang/source-root overlap, and reserved byte/inode policy. Raw source or target
locators never cross the port, and the A2c1 test-only issuer is unavailable to
production code.

The evaluator receives target and external evidence, intersects their expiry,
checks every frozen revision, applies the existing reason matrix and rebuilds
the current public snapshot. The persistence transaction remains the only
writer and repeats locked source/plan/product validation. A2c2 does not wire
runtime/main; Task 8 owns production composition.

### 41.7 Errors, privacy and stop point

Forged/substituted authority is `ErrInvalidTargetPermit`; observable root/path/
filesystem replacement is `ErrRecoveryTargetChanged`; resolver, SSH, SFTP,
StatVFS, fixed-command, parse and close ambiguity is sanitized unavailable.
Only context cancellation/deadline preserves identity. Target methods emit no
log and return no raw dependency detail.

A2c1 and A2c2 perform zero target mutation. They do not open A2d result read,
A2e overwrite/Lstat/absence, A3 Delete/RemoveOwnedJobDir, runtime/main,
orphan/quarantine or Git delivery. No new dependency, path, model, migration,
setting or manifest row is allowed; `go.mod` and the protected rsync fixture
remain unchanged.

## 42. A2d Resolver-Bound Published Result Read

This amendment controls only A2d and is later-controlling over sections 6.3,
8.1 and 38 where their generic observation wording would allow a structurally
valid result-read permit to open a target session. It opens one concrete arm:
sequential read of an exact published isolated regular-file result. `ProbeRoot`,
`Lstat`, valid overwrite, `Delete` and `RemoveOwnedJobDir` remain unavailable.

### 42.1 Durable authority and private proof

`RecoveryResultResolver.resolve` continues to load the result, ResultSet, job,
executed plan, consumed write grant and repository. After all current owner,
publication, marker, security, deadline and no-active-attempt checks, it also
constructs the existing `recoveryTargetSessionBinding` from that exact plan.
The returned `ResolvedRecoveryResult` carries an unexported comparable
`targetResultReadAuthority`; JSON and public Content products remain unchanged.

The authority contains the session binding, job/result-set/result IDs,
publication revision, cleanup fence, marker binding/creator/fence, locator
digest, exact `TargetObjectRef`, expected bytes/content digest and effective
plaintext deadline. `sameResolvedRecoveryResult` compares it, so resolver
revalidation rejects any private or public substitution. The existing opaque
publication fingerprint also hashes the private session-binding digest and
marker creator/fence, so a pre-drift Content grant cannot reauthorize while
keeping those private values out of the public product.

The delivery adapter derives a base `TargetObservationPermit` itself and calls
an unexported issuer with the exact resolved authority and
`OpenOwnedResultRequest`. The issuer stores an unexported `json:"-"` proof on
the observation permit under domain:

```text
xirang/recovery/target-result-read-permit-proof/v1
```

Its length-framed digest binds the complete base permit, session-binding
digest, every authority scalar, private object fields and exact request.
`NewTargetResultReadPermit`, `ValidateAt`, `ValidateObjectAt` and the concrete
method all require a non-nil exact proof. Verify/preflight proofs and a raw
structural result-read permit are rejected before dependency calls.

### 42.2 Purpose-exact session and workspace validation

The session factory admits `TargetPurposeResultRead` only from the validated
private authority and uses `recovery_result_read` plus the safe job ID as the
dial correlation. It reuses the existing exact node/credential revision checks
and SFTP/SSH cancellation watcher; another purpose cannot be substituted.

Before opening result bytes, the target validates root prefixes, private
`jobs` and `jobs/<jobID>` directories, the exact result parents and the final
regular file without following aliases or links. It reads the bounded marker,
checks its authenticated installation/job/root/revision/binding/creator/fence
with the existing codec, and repeats directory checks around marker access.

### 42.3 Two-pass bounded result reader

The first pass reuses the exact regular-file verifier to stream the complete
file into SHA-256 with bounded read buffers. It requires the expected byte
count, exact EOF, digest and unchanged pre/post file snapshot. It does not
retain plaintext. The target then repeats canonical parent and marker checks,
opens a second read-only handle, and requires its handle stat plus path stat to
equal the verified snapshot before returning.

The returned private reader owns that file and target session. It limits the
observable stream to the expected length, hashes delivered bytes, and on the
read that reaches the expected length checks one-byte EOF plus the digest. At
full-read completion, and for a zero-byte result on close, it also rechecks the
handle/path snapshot, canonical parents, authenticated marker and permit
expiry. Partial consumer close does not invent full-read success; it only
releases resources. File, SFTP and SSH close occur in that order under
at-most-once guards. All acquired resources are released even when validation
or close fails.

### 42.4 Durable race, errors and privacy

The adapter performs the existing resolver revalidation after target open and
before returning the Content source. Content then performs its existing source
revalidation and owns grant heartbeat, active-read cancellation and cleanup
drain. Thus a cleanup fence cannot become active before issue/open without
closing authorization, while already registered reads are canceled and drained
before remote cleanup.

Forged or substituted private authority is `ErrInvalidTargetPermit`. Observable
marker, namespace, path, type, size, content or snapshot drift is a closed
target-change result; key lookup, resolver, dial, SFTP, read/stat/open and close
ambiguity is sanitized unavailable. Caller cancellation/deadline identity wins
over dependency and close noise. No target method logs, and no error/JSON/audit
contains host, user, credential, root/object locator, marker material, content,
digest input, SFTP status or raw dependency errors.

### 42.5 Stop point

A2d adds no target mutation, public route, runtime/main composition, table,
migration, setting, dependency or manifest path. It ends after focused and
whole-package verification. A2e overwrite/Lstat/absence, A3 destructive
cleanup/delete/tombstone, orphan reconciliation and every Git delivery action
remain closed.

## 43. A2e1 Delete-Oriented Lstat And Exact Absence Observation

This amendment is later-controlling for the observation-only half of A2e. It
opens concrete `Lstat` and absent `Verify` only for the already sealed delete
handoff. It deliberately does not define or authorize overwrite.

### 43.1 Approved split and stop point

A2e1 is a zero-mutation observation slice. Its product is exact prior-entry or
exact-absence evidence suitable for the later A3 delete protocol; it does not
delete anything itself. A2e2 remains closed until a separate design resolves
the validation-to-replace race, private sidecar ownership, temporary final
absence and crash recovery. Direct replace rename is not an acceptable
fallback because an external update between validation and rename could be
silently overwritten.

The original product boundary was `target.go` with tests in `target_test.go`.
The private SFTP facade may add only `ReadLink(string) (string, error)`. V12
review then produced a genuine compile RED proving the existing verify proof
could not express operation/prior authority, followed by a behavioral RED
proving a valid substituted handoff operation/prior could be resealed. That
evidence expands the minimal product boundary to `worker.go` and the focused
test boundary to `worker_test.go` and `executor_test.go`; `executor.go` remains
unchanged. No path, public API, model, migration, setting, dependency or
manifest row is added.

### 43.2 Authority and purpose-exact session

`TargetVerifyPermit` remains the only authority. `Lstat` and absent `Verify`
must validate its private proof, exact executed-plan session binding, mode,
operation, object, prior facts, expiry and request before resolver, dial or
SFTP calls. A structural permit, copied public fields, another object's permit
or a write/preflight/result-read proof cannot open the method.

This later amendment evolves the ephemeral private proof digest to
`xirang/recovery/target-verify-permit-proof/v2`. The existing ordered v1 fields
are followed by operation kind, expected-prior kind and expected-prior digest
before the complete session-binding digest. The same proof object remains the
only authority; no second delete proof is introduced. The sole worker issuer
requires the handoff operation/prior to equal the immutable job-item fields and
requires the existing operation digest to reproduce that job item before it
seals the v2 proof. `Lstat` and absent `Verify` additionally require literal
`delete` plus `ExpectedTargetPresent`; a separately valid create, overwrite or
skip permit fails before resolver/session calls.

Both methods use the existing purpose-exact `recovery_verify` session and safe
job correlation. They reuse current node/credential revision validation,
context cancellation watcher and SFTP-then-SSH at-most-once closure. Opening
this arm must not broaden the session factory or make ordinary observation
permits sufficient.

### 43.3 Canonical entry observation

One complete observation validates the sealed root and every parent with the
existing no-alias, no-symlink canonical helpers, then applies non-following
`Lstat` to the final entry. Exact not-exist is the only missing result.
Present entries are classified as `regular`, `directory`, `symlink` or
`special` and retain the complete mode, size, UID/GID and Unix-second mtime.

For a regular entry, the observer opens read-only and streams exactly the
observed size through SHA-256 with bounded buffers, checks exact EOF, and
requires handle/path metadata to remain equal around the read. Zero bytes are
valid. For a symlink, it calls private `ReadLink` without following the link
and treats the returned string as exact bytes; surrounding non-following stats
must remain equal. Directory and special entries have no payload read. No
whole-file or link-target material is retained in a returned product.

Every successful call performs two complete observations in the same session
and requires equality of all canonical, metadata and payload facts. This
double observation does not claim atomicity; it makes visible drift fail
closed and supplies the exact evidence expected by the later delete protocol.

### 43.4 Exact identity and revision products

A present result has a lowercase hexadecimal SHA-256 identity digest under:

```text
xirang/recovery/sftp-delete-entry-identity/v1
```

All values are length framed in this order: root revision, private relative
locator, kind, decimal size, complete mode, decimal UID, decimal GID,
Unix-second mtime and kind-specific payload fact. The regular payload fact is
the lowercase full-content SHA-256; the symlink payload fact is the exact
`ReadLink` byte sequence; directory and special use an empty payload fact.
The returned digest exposes none of those private inputs.

`TargetRevision` is not derived from the new identity. It reuses the exact A2c
`sftpt1:` domain and field order unchanged: root revision, private relative
locator and either literal absent or present kind/size/mode/UID/GID/mtime.
Consequently, the same stable entry observed by A2c and A2e1 has the same
metadata revision, while A2e1 additionally supplies payload-bound delete
identity. A missing result has an empty identity digest and its exact absent
`sftpt1:` revision.

### 43.5 Exact absence and absent Verify

Absence requires two complete canonical observations to return exact
not-exist. A present first or second observation, or any change between them,
is target changed. Permission, unsupported operation, malformed stat,
transport loss, timeout and close ambiguity are unavailable, never absence.

`Verify` with the sealed permit and `ExpectedPresent=false` invokes the same
observer. Exact absence returns `TargetVerifyResult` with
`AbsentObservation{Evidence: exact}` and the identical `sftpt1:` revision that
`Lstat` returns for the same missing object. A present observation does not
silently satisfy or rewrite the expectation. Existing present regular-file
Verify behavior and its `sftp1:` content observation remain unchanged.

### 43.6 Errors, cancellation, privacy and verification

Invalid or substituted authority is `ErrInvalidTargetPermit`; visible root,
parent, entry, metadata or payload drift is `ErrRecoveryTargetChanged`;
resolver, SSH, SFTP, open/read/stat/readlink/parse and close ambiguity is
`ErrRecoveryTargetUnavailable`. Caller cancellation and deadline preserve
their identity over dependency and close noise. Every acquired file, SFTP and
SSH resource closes at most once in ownership order.

The target performs no mutation and emits no log. Errors, JSON, audit inputs,
formatted values and captured products must not contain host, username,
credential, root/object locator, content, link target, UID/GID, digest input,
SFTP status or raw dependency detail. Verification covers the sealed authority
matrix, all four present kinds, exact absence parity, drift/cancellation/
resource/privacy behavior, whole-package and race gates. V12 records only
A2e1 focused closure and stops before A2e2, A3, runtime/main,
orphan/quarantine and Git delivery.

## 44. A2e2 + A3a Authenticated In-Place Overwrite Sidecar Protocol

This later-controlling amendment records the approved option A. It opens
in-place regular-file overwrite only when it can capture the exact object that
occupied the final name, prove that captured object is the expected prior, and
publish the already verified post without overwriting a concurrent winner. It
also opens the minimum post-checkpoint marker reconciliation needed to make a
successful overwrite crash-recoverable. It does not open general A3 cleanup.

### 44.1 Boundary and rejected alternatives

The existing SFTP facade supplies standard `Rename`, whose destination must be
absent, but no compare-and-swap, exchange, target-side lock or versioned rename.
Another `Lstat` followed by replacement cannot close the race. OpenSSH POSIX
rename is replacement, not CAS. A remote helper or agent could supply stronger
target-side semantics but would change the agentless SSH/SFTP architecture.

The approved protocol therefore accepts temporary final-name absence while the
captured prior is verified. That interval is bounded by the immutable expected
prior byte count, the request context/deadline and transport progress, not by a
fixed wall-clock promise. The verified post is prepared before capture, so no
source streaming occurs during the absence interval. Direct replacement,
publish-before-prior-validation and cross-directory sidecars remain rejected.

The product boundary remains the already manifested `target.go`, `worker.go`
and `executor.go`, with `target_test.go`, `worker_test.go` and
`executor_test.go`. Keeping the protocol in those established files preserves
the exact 145-path Task 7 manifest. No model, DDL, checkpoint phase, setting,
route, dependency or migration-number change is required.

### 44.2 Stable private artifact binding

The locked worker handoff is extended with a private overwrite artifact
binding. It is derived from the historical recovery-cleanup ownership key whose
version is already stored on the immutable job item. Its framed HMAC input binds
the plan and operation digest, job and item, target mode, node/root/object and
root revision, the private final locator, exact prior digest/bytes, exact post
digest/bytes and key version. The resulting base64url token is the only
variable portion visible in remote artifact components; raw identifiers,
locator material and digest inputs are absent.

Four sibling components share a fixed reserved prefix and that token:

```text
.<fixed-prefix>-<token>.intent
.<fixed-prefix>-<token>.prior
.<fixed-prefix>-<token>.post
.<fixed-prefix>-<token>.published
```

All are joined to the canonical final parent inside the target; no caller can
supply an artifact path. The target rejects overlong/noncanonical components,
root or parent aliases, symlinks and a token or binding that does not reproduce
the sealed proof before opening a session. Literal overwrite, present prior and
in-place mode are required. The private item-write proof evolves to a new
domain/version and binds the item ID, operation digest and complete artifact
binding; a separately valid create or isolated proof cannot enter this arm.

`intent` and `published` are deterministic bounded authenticated documents.
Their body contains only schema version, historical key version, closed phase
and a private binding digest; the authentication tag covers the exact body
under a separate overwrite-marker domain. A fresh worker can reproduce and
validate them from durable state and historical key material. Existing marker
bytes are reusable only when exact; unknown, malformed or re-signed content is
target drift, never ownership.

### 44.3 Preparation and capture

The target first validates the final as the exact expected regular-file prior
using the existing bounded content/byte/EOF and stable snapshot mechanics. It
creates the exact intent marker and deterministic post with exclusive create,
mode 0600, bounded write, mandatory Sync, close/reopen and full post digest
verification. Exact artifacts from an interrupted invocation may be reused;
collisions or drift are not removed.

After canonical parent and live-permit revalidation, standard no-overwrite
`Rename(final, prior)` captures whichever object occupies the final name at the
mutation instant. The target then observes that captured entry while final is
absent. A regular entry must reproduce the exact expected prior digest, bytes
and stable snapshot. If another entry won between prevalidation and capture,
the target does not publish.

For a captured mismatch, automatic rollback is allowed only in the same
unambiguous session that just received successful `Rename(final, prior)` and
immediately observed the mismatched winner. It uses standard no-overwrite
`Rename(prior, final)` while final is proved absent, and requires the restored
final to reproduce that same-session captured observation. A re-entry that
starts with a mismatched prior, an ambiguous capture result, an occupied final,
ambiguous rollback or later artifact drift preserves every object and becomes
closed unresolved/needs-attention. This rule applies to regular, directory,
symlink and special captured winners: no mismatched user object is deleted.

### 44.4 Publish, acknowledgement and retained proof

An exact captured prior permits one more live-authority and parent check,
followed by standard no-overwrite `Rename(post, final)`. A concurrent final
winner makes publication fail without replacement. The target verifies final
as the exact post, then exclusively creates and verifies the authenticated
published marker. Only after that marker exists may it remove the exact owned
prior, any now-redundant exact post, and intent marker.

The successful target state is therefore:

```text
final = exact post
published = exact authenticated document
prior/post/intent = exact absent
```

`WriteAtomic` returns the exact existing A2a `sftp1:` post revision only from
that state. It deliberately retains `published`: deleting every remote proof
before the immutable operation checkpoint would reopen the remote-success /
DB-crash ambiguity. A retry that observes exact final plus exact published can
return the same write result without consuming or trusting caller content.

### 44.5 Reentrant remote state machine

The target classifies the complete final/intent/prior/post/published tuple
before mutation and after every ambiguous dependency result. The recognized
forward states are:

| State | Exact remote facts | Allowed action |
|---|---|---|
| fresh | final=prior; all artifacts absent | create intent and post |
| prepared | final=prior; intent/post exact; prior/published absent | capture final |
| captured | final absent; intent/post/prior exact; published absent | publish post |
| published-unacknowledged | final=post; intent/prior exact; post/published absent | create published marker |
| acknowledged | final=post; published exact; other exact owned artifacts optional | remove prior/post/intent and return success |
| restored | same unambiguous session restored its just-captured mismatched object; prior absent | remove only exact safe post/intent and fail closed |
| conflicted | final occupied by another object or any tuple is not uniquely explainable | preserve evidence and fail closed |

Exact not-exist is distinct from permission, unsupported, timeout and transport
ambiguity. A rename error is never interpreted from final visibility alone;
the whole authenticated tuple must select one row. State transitions are
idempotent across process death before and after every open, write, Sync,
close, read, rename, remove and marker operation. Re-entry never selects the
restored transition from a mismatched prior; only the live capture transition
has the ephemeral evidence needed to restore it automatically.

### 44.6 Durable checkpoint before marker finalization

Successful remote publication is projected in two durable stages. First, the
worker persists the existing immutable operation checkpoint, item outcome and
next target-chain revision while leaving the job running and the execution
attempt, source lease and node lease active. Existing source post-revalidation
outcome is retained in worker memory for the following same-attempt projection;
the last operation no longer terminalizes the job inside the checkpoint
transaction. A takeover after checkpoint but before that projection must run
the durable source revalidation again and must not infer `matched` from the
operation checkpoint alone.

A second locked load must reproduce the completed overwrite item, operation
checkpoint, exact next target revision, active execution fences and historical
artifact binding. It seals a fresh private overwrite-finalize proof on a new
write permit. The target opens a purpose-exact `recovery_write` session,
requires exact final post, canonical parent, prior/post/intent absence and
either the exact published marker or exact idempotent marker absence, then
removes only the published marker. The result binds the checkpoint and final
revision; copied public fields cannot authorize cleanup.

Crash before the checkpoint re-enters `WriteAtomic` using the remote marker.
Crash after the checkpoint replays finalize. Crash after marker removal but
before DB continuation sees checkpoint + exact marker absence + exact final and
returns idempotent finalize success. Before another pending item or terminal
completion, the worker reconciles every completed overwrite checkpoint from
the validated durable history, including a predecessor attempt adopted under a
fresh current attempt/node/source fence. The stable artifact token never binds
an attempt fence; the finalize proof binds both the immutable historical
checkpoint and the current takeover authority. This derives durable cleanup
work without a new column or phase.

After marker finalization, matched source evidence permits the next operation
or the existing completion transaction. A drifted/failed source uses the
existing completed-operation failure projection only after marker cleanup.
On takeover the fresh revalidation result controls that disposition. Thus no
terminal job depends on an expired execution permit for successful marker
cleanup, and the node/source leases are not released early.

### 44.7 Errors, ownership and A3a stop point

Invalid proof, operation, mode, request or substitution is
`ErrInvalidTargetPermit`. Visible final, parent, marker or artifact drift is
closed target change/unresolved. Resolver, historical-key, SSH/SFTP,
read/write/stat/sync/rename/remove and close ambiguity is sanitized unavailable.
Caller cancellation/deadline identity wins. File, SFTP and SSH resources close
at most once, and the target emits no raw diagnostic.

A3a owns only checkpoint-bound successful published-marker reconciliation
while the original execution fences remain active. It does not delete an
unrecognized artifact, mismatched captured prior or external winner. Those
states remain evidence for the later general A3 orphan/quarantine design. The
slice stops before `Delete`, `RemoveOwnedJobDir`, result/workspace tombstones,
successful terminal cleanup-lease release, general orphan scheduling,
runtime/main, whole Task 7 review and Git delivery.

## 45. Task 7 V13 focused review closure

V13 reviewed every A2e2+A3a acceptance row and sections 44.1--44.7 against the
complete R42--R47 implementation and tests. Artifact paths remain target-derived
from private sealed sibling components; only standard no-overwrite `Rename` is
reachable; mismatch restoration remains limited to the same unambiguous capture
invocation; ambiguous dependency results never infer success; the durable
operation checkpoint precedes marker finalization; and terminal continuation is
behind successful idempotent finalize. No new schema, checkpoint phase, public
path, setting, dependency or migration was introduced, and no raw overwrite
diagnostic crosses the target/worker/executor boundary.

The review found no remaining Critical or Important product issue. It found one
process-only manifest defect: three create rows were duplicated in the task
plan. Removing only those duplicate rows restored the already approved 145-path
unique/disjoint manifest; it did not change product scope.

Fresh focused normal/race, broad and whole Recovery, forced real PostgreSQL,
vet/lint, format/diff, static/privacy, Trellis, manifest, protected-hash and Git
state gates passed. This closes only the focused A2e2+A3a slice. Full A3 cleanup,
terminal cleanup-lease release, runtime/main, whole Task 7 review and Git
delivery remain outside this closure.

## 46. Full A3 controlled deletion, lifecycle cleanup and logical reconciliation

This later-controlling design records the approved full-A3 direction after the
V13 A2e2+A3a focused closure. It does not reopen the completed overwrite
protocol. It closes the remaining concrete exact-mirror `Delete`, owned
isolated-workspace removal and lifecycle terminalization, and general logical
orphan/quarantine reconciliation in three separately verifiable vertical
slices. V14 then reviews the whole Task 7 product.

The delivery order is fixed:

1. A3b: concrete exact-mirror `Delete` and execution reconciliation;
2. A3c: concrete `RemoveOwnedJobDir` and lifecycle terminalization;
3. A3d: read-only general logical reconciliation;
4. V14: whole Task 7 review.

Each slice ends at its own evidence-backed stop point. No slice counts a later
slice, Task 8 runtime composition, Task 9/10 work, or Git delivery as complete.
There is no periodic heartbeat. Temporary unavailability stops the invocation;
progress resumes only through an explicit invocation or lease-expiry takeover.

### 46.1 Capability and ownership boundaries

`TargetPort` remains the remote capability boundary. A3b extends its
execution-scoped mutation contract with a sealed exact-mirror delete request.
A3c extends its cleanup-scoped contract with an owned-job-directory removal
request. These authorities are purpose-specific and are not interchangeable:
an overwrite proof cannot authorize delete, a delete proof cannot authorize
workspace cleanup, and a job cleanup permit cannot authorize unknown-object
quarantine.

`ResultLifecycleService` remains the sole owner of durable cleanup phase,
cleanup fence, lease owner and cleanup node-lease projection. The concrete
target can mutate a proven owned workspace only while a private live-validation
callback confirms the current lifecycle and node authority before every
mutation. The target never writes lifecycle rows and the service never accepts
caller-supplied remote paths or artifact components.

A3d uses a separate read-only reconciliation service and port. Its
`TargetReconciliationPermit` authorizes only bounded observation under a
registered root. It exposes no rename or remove method, borrows no Recovery job
cleanup lease and obtains no permanent-use-latch mutation authority. General
orphan/quarantine therefore means logical classification and blocking, not a
physical move or deletion.

The existing paired `000069` model and migration already reserve the one-way
cleanup phases, published/workspace tombstone shapes, retryable owner release
and terminal immutability required by A3c. Full A3 does not add a new orphan
registry, node-level orphan mutation lease, public WIP state, route, setting or
managed scheduler. Task 8 owns cadence, startup/listener ordering and
runtime/main composition. Task 9 and Task 10 retain their existing downstream
API/UI and operational ownership.

### 46.2 A3b sealed exact-mirror delete authority

The worker consumes the existing durable per-operation delete authority before
calling the target. A private delete binding is then derived from immutable
plan, job, item and operation identity; consumed authority checkpoint; target
mode; node, registered root and private object identity; exact expected prior;
and the historical recovery key version. Its stable component token does not
bind the current attempt. A fresh sealed permit separately binds current
attempt, node, source and execution fences. This permits takeover to reconcile
the same remote state without reusing expired mutation authority.

The durable delete operation keeps `ExpectedPriorBytes == -1`; unlike create,
overwrite and skip, delete identity is the closed path-bound entry identity
already returned by the delete-oriented `Lstat`. That digest includes kind,
size, metadata and the kind-specific payload fact. Delete artifact/proof
framing binds the literal `-1` sentinel and rejects every other byte value;
captured observation recomputes the same identity facts against the original
final locator after the no-overwrite rename. It must not reinterpret the digest
as a regular-file content digest or require a nonnegative byte count.

The target derives three delete-specific siblings in the canonical final
parent; callers cannot supply their paths:

```text
.<fixed-delete-prefix>-<token>.intent
.<fixed-delete-prefix>-<token>.captured
.<fixed-delete-prefix>-<token>.verified
```

`intent` and `verified` are bounded authenticated documents under distinct
delete-marker domains. The captured sibling is the object moved from the final
name. All bindings include a schema/domain version and historical key version.
They cannot be substituted with overwrite artifacts, another operation, a
different expected object or a re-signed document. Unknown, malformed,
overlong, aliased or unauthenticated components are target drift and are never
removed.

Before session creation the target validates context, dependencies, request,
closed operation/mode/object shape and sealed authority. Inside a
purpose-exact `recovery_write` session it reproduces the binding, validates the
canonical registered root and parent, and reconciles the complete
final/intent/captured/verified tuple before mutation. Direct
`Lstat(expected) -> Remove(final)` is forbidden because an external writer can
replace the observed object before deletion.

### 46.3 A3b capture, verification and deletion state machine

From a fresh exact tuple the target creates and verifies `intent`, revalidates
the live permit and canonical parent, then uses standard no-overwrite
`Rename(final, captured)`. That rename obtains the object present at the
mutation instant without overwriting a captured sibling or external winner.
Before delete success, final-name absence is accepted only while the exact
captured tuple explains this bounded capture/delete or same-invocation
restoration interval. After verified delete success, exact final absence is the
intended durable outcome.

The captured object is verified by closed kind:

- regular files reproduce bounded bytes, digest, stable metadata and EOF;
- symlinks reproduce `Lstat` metadata and exact `ReadLink` bytes without
  following the target;
- directories reproduce exact directory identity and are proven empty;
- special entries reproduce their closed `Lstat` identity and are never
  opened or followed.

Only an exact captured object permits creation and verification of the
authenticated `verified` document. The target then revalidates live authority
and the parent immediately before the kind-appropriate leaf deletion. Directory
deletion is non-recursive and only applies to the proven empty captured
directory. A single delete operation never expands into authority over
unfrozen descendants.

If the same unambiguous invocation successfully captured an object and then
proved it mismatched the expected prior, it may restore that exact just-captured
object only while final is exactly absent, using standard no-overwrite rename,
and must verify the restored observation. Re-entry cannot infer that ephemeral
same-invocation fact. A mismatched captured object on re-entry, an ambiguous
rename/remove, an external final winner, a non-empty directory or a forged or
unknown artifact preserves all evidence and fails closed.

Recognized progression is:

| Remote tuple | Allowed action |
|---|---|
| final exact prior; delete artifacts absent | create exact intent |
| final exact prior; intent exact; captured/verified absent | no-overwrite capture |
| final absent; intent/captured exact; verified absent | verify captured and create verified |
| final absent; intent/captured/verified exact | delete exact captured object |
| final/captured absent; intent/verified exact | verify absence, remove intent, then verified |
| final/captured/intent/verified absent under consumed authority | idempotently return exact delete success |

After deletion, final and captured absence must each be established by the
closed exact-absence observer. `intent` is removed before `verified`, and
`verified` is the last artifact removed. A clean tuple plus durable consumed
authority is sufficient for idempotent adoption, so A3b does not introduce a
separate `FinalizeDelete` API.

### 46.4 A3b worker durability, takeover and error disposition

The worker order is fixed:

```text
consume durable delete authority
  -> reconcile/execute Target.Delete
  -> Verify exact absence
  -> persist operation checkpoint, item outcome and target-chain revision
  -> continue source disposition, next item or terminal projection
```

Once delete authority is consumed, neither the original attempt nor a takeover
may skip target reconciliation merely because the final name is absent. Before
any later item, takeover first replays the complete delete tuple under a fresh
attempt/node/source permit while retaining the historical artifact binding.
The immutable operation checkpoint and target-chain projection are written
only after the clean remote tuple and exact final absence are established.

Caller cancellation or deadline retains exact context identity. Resolver, key,
SSH/SFTP, read/stat/readlink/rename/remove/close ambiguity and temporary
unavailability return only the stable sanitized unavailable contract. They do
not write an operation checkpoint, advance the target chain, remove evidence,
or terminalize the job. The invocation stops without polling, heartbeat or
unbounded retry. It may be explicitly re-entered while its claim is current or
adopted after lease expiry under fresh fences.

Only a proved contradictory tuple -- including closed target change,
forged/unknown authenticated state or an external winner that the exact state
machine cannot safely adopt -- may run the bounded `context.WithoutCancel`
closing transaction. That transaction re-proves current ownership and projects
the existing operation unresolved, failed item/remote-outcome-unresolved,
job needs-attention/remote-outcome-unresolved, failure evidence, attempt closure
and source/node lease release. A stale owner updates nothing. Invalid or stale
authority alone does not manufacture remote-unresolved evidence.

A3b stops after its genuine RED/GREEN matrix, focused normal/race gates,
applicable real-PostgreSQL worker/CAS checks and evidence entry. It does not
open owned workspace cleanup or general reconciliation.

### 46.5 A3c lifecycle entry and owned-directory capture

For a published result or unpublished isolated workspace that is due for
cleanup, `ResultLifecycleService` starts a short transaction that validates the
result/workspace shape, permanent use latch, cleanup fence, current cleanup
owner and exact active cleanup node lease. It advances durable cleanup phase
from `validated` to `delete_started`, retains those authorities and issues a
destructive permit to the concrete target. Published and unpublished flows use
the same target protocol but retain their distinct durable product projections.

The permit binds immutable job, registered root, exact `jobs/<job>` workspace,
authenticated owner-marker facts, cleanup/key version and current lifecycle/
node authority. It carries a private validation callback. Immediately before
every rename or remove mutation, the target calls it to re-prove cleanup fence,
node fence, owner, unexpired lease, permanent use latch and an allowed cleanup
phase. Any mismatch stops before the next mutation. A caller cannot seal or
replace the callback and cannot supply an artifact path.

The target uses standard no-overwrite rename to capture the exact workspace to
a deterministic cleanup-specific sibling in the same canonical `jobs` parent.
It never uses replacement or cross-directory rename. Inside the captured tree
it reauthenticates the original exact owner marker. Outside that tree, in the
same `jobs` parent, it creates and verifies a bounded historical cleanup-key
authenticated `verified` marker. That marker binds job/workspace/captured
identity, owner-marker facts and key version. It survives removal of the owner
marker and proves which captured namespace may be resumed.

Ambiguous capture, an external final winner, canonical or owner-marker drift,
or unknown/forged cleanup artifacts preserves the namespace and fails closed.
The workspace namespace authority applies only to the proven exact job
directory. It never covers the shared `jobs` directory, registered root,
siblings or general unknown entries.

### 46.6 A3c bounded tree removal and terminal transaction

The captured namespace is removed depth-first, without following links and
without crossing a filesystem boundary. Regular files, symlinks and special
entries are leaves. A directory is removed only after its children have been
removed and it is observed empty. On entering and processing every directory,
the target verifies canonical containment and `StatVFS.Fsid` equality with the
validated captured root. A mount/filesystem boundary or canonical escape stops
before mutation and preserves evidence. The authenticated owner marker inside
the captured tree is an owned leaf; the external `verified` marker remains
until the captured directory is exactly absent.

`pkg/sftp` v1.13.10 has no public directory-handle paging method: its
`ReadDir` and `ReadDirContext` helpers collect the complete directory. The
cleanup target therefore uses a private bounded SFTP v3 directory reader over
one dedicated SSH subsystem channel per cleanup SFTP session. All directory
handles in the bounded depth-first stack share that channel, avoiding one SSH
session channel per depth level and the common OpenSSH `MaxSessions=10` limit.
It supports only handshake, `OPENDIR`,
`READDIR` and `CLOSE`, caps each response packet at 256 KiB, retains at most
257 non-dot entries, rejects malformed/version/ID/status/attribute products and
closes the remote handle/channel deterministically. It never shells out, adds
a dependency, changes `go.mod`, reflects into `pkg/sftp` internals or returns
wire status text. Ordinary file I/O and all other SFTP operations remain owned
by `pkg/sftp`.

One `RemoveOwnedJobDir` target call performs at most 256 remove mutations. It
returns the private result
`OwnedJobDirRemoval{Complete, RemovedEntries, ProgressDigest}`. The count is
bounded and the digest describes only the closed progress shape; neither
contains a path, name or marker. The digest is not a durable cursor and cannot
skip remote reconciliation. `Complete=false` is normal progress. The lifecycle
closing transaction renews only the exact owner and leaves phase
`delete_started`; a later explicit service call may perform another target
pass. No background heartbeat or unbounded same-turn loop is introduced.

When the target has removed the captured directory, it proves final workspace
and captured sibling absence, removes the external verified marker, proves the
complete clean tuple and returns `Complete=true`. The lifecycle service then
persists `delete_started -> deleted` without releasing cleanup or node
authority. From durable `deleted`, it calls a clean-tuple-only reconciliation;
a takeover in this phase must not capture or recursively delete again. The
reconciliation requires exact absence of final workspace, captured sibling and
external verified marker.

Only after that fresh reconciliation succeeds may one final transaction
atomically:

- advance `deleted -> tombstoned`;
- project published results to `cleaned` or unpublished workspaces to
  `workspace_cleaned`;
- clear cleanup owner, fence and node ownership fields required by the closed
  model; and
- release the exact active cleanup node lease.

Published/workspace tombstone shape and terminal immutability remain enforced.
A fence, lease, phase or owner change makes the transaction update zero rows;
partial terminalization is forbidden.

`Complete=false` is not failure. On target error or cancellation, a short
`context.WithoutCancel` closing transaction releases authority only if the
caller remains the exact current cleanup owner. A published result becomes
`cleanup_failed`; an unpublished workspace becomes ownerless `cleanup_due`;
both retain phase `delete_started` for fresh-fence retry. A stale owner updates
nothing and cannot release a successor's node lease. Failure of the closing
transaction returns sanitized lifecycle unavailable/conflict and makes no
release claim. A process crash performs no detached renewal; lease expiry is
the takeover mechanism.

A3c stops only after published/unpublished parity, bounded progression,
filesystem/crash matrices, cross-engine atomic terminalization, focused
normal/race gates and its evidence entry pass. It does not open managed orphan
cadence or runtime/main.

### 46.7 A3d expected set and read-only target scan

The reconciliation service takes a fresh bounded database snapshot for one
registered node/root and constructs the complete expected direct-child set for
non-tombstoned isolated workspaces and their currently legal A3c remote states.
If that set is incomplete, exceeds its hard bound or cannot be represented
under the closed grammar, no clean scan begins and the result is
`scan_incomplete`.

Expected remote components cross the target boundary only as private keyed
tokens. The target can compare a raw remote component with those tokens, but an
unknown remote name never leaves the target. A finding may include a safe job
ID only after the token exactly matches a DB expected component. All other
entries are represented by an audit-key-versioned HMAC fingerprint.

The service issues a node/root-scoped `TargetReconciliationPermit` binding
current node, credential and root revisions; registered root; expected-set
digest; scan and aggregate bounds; opaque cursor; expiry; and the exact
read-only operation. The target opens only a purpose-exact
`recovery_reconcile` SSH session and scans direct children of canonical
`<registered-root>/jobs`. It does not follow symlinks, recurse into unknown
directories or read user content. It may read only the fixed Recovery
marker/artifact documents required by the closed classifier.

Classification is exhaustive:

| Category | Meaning |
|---|---|
| `known_healthy` | expected component, kind, marker/artifact and durable phase agree |
| `known_drift` | expected token matches but kind, marker, artifact or phase disagrees |
| `db_unmatched` | a historical Recovery key authenticates the component but no current expected owner exists |
| `forged_or_unknown` | authentication or closed Recovery component grammar fails |
| `scan_incomplete` | an established scan is interrupted, root/prefix drifts, a bound is exhausted or EOF is not proved |

Only `known_healthy` is not a finding. Every other category leaves the remote
entry in place and blocks a clear result. A3d has no physical quarantine path.

### 46.8 A3d continuation, products and downgrade authority

Every call, chained scan, expected set, finding list and aggregate count has a
hard bound. The opaque cursor contains only an authenticated ordinal, prefix
digest and bound generation; it contains no remote name or path. Resume starts
enumeration at the directory beginning, replays the processed prefix and
requires its exact digest before continuing. Enumeration-order, content,
generation or root-revision drift returns `scan_incomplete`; it cannot skip a
prefix or turn unstable pagination into clean EOF.

Findings contain only the closed category, audit-key-versioned HMAC
fingerprint, closed entry kind, optional DB-confirmed job ID and bounded counts.
Raw names/paths, marker bytes, artifact token or HMAC input, credentials, root
locator and raw dependency status never appear in the result or cursor. Each
pass writes one bounded aggregate `recovery_reconcile` audit and calls the
required finding sink with deduplicable sanitized alert data. A finding or
`scan_incomplete` is a normal `blocked` product. Resolver/SSH/key failure or an
audit/alert sink failure returns only the stable sanitized reconciliation
unavailable error; callers treat that error as blocked.

`clear` is returned only when the DB expected set is complete, the exact
validated prefix chain reaches trustworthy EOF, the finding count is zero and
aggregate audit succeeds. There is no durable orphan owner or row, so restart
rebuilds the DB/root snapshot and replays an authenticated cursor; an old
result or cache has no clear authority. Downgrade-readiness, under its sticky
admission generation, must obtain a fresh clear for every registered root.
Cleanup backlog and the permanent-use-latch blockers remain independent.
Task 8 may later schedule this explicit product, but Task 7 does not supply its
runtime adapter or cadence.

A3d stops after purpose/permit, five-category, bounds/cursor, audit/alert,
downgrade and zero-mutation privacy evidence pass. It does not count runtime
composition or Git delivery.

### 46.9 Uniform errors, resource ownership and privacy

Across A3, error precedence is closed: exact caller cancellation/deadline
identity first; invalid or stale authority second; a proved contradictory
remote state third; sanitized dependency unavailable last. Targets, services
and workers never wrap, format or log a raw dependency error. Cancellation
continues to win when transport or close also fails.

Every acquired file, directory handle, SFTP client and SSH client closes at
most once in ownership order, including APIs that return a non-nil resource
with an error. A close ambiguity cannot upgrade an unverified operation to
success. Concrete targets emit no direct log.

All permits, proofs, artifact bindings, cursors, requests and private results
omit private JSON fields and implement redacted `String`/`GoString` behavior.
Tests scan error values, `%v`, `%+v`, `%#v`, JSON, audit, alert, metric labels
and structured logs with recognizable host, user, credential, root, path,
name, token, marker, content, digest-input, SFTP-status and raw-error canaries.
Only closed categories, bounded counts, opaque IDs and sanitized digests may
cross those boundaries. A3d finding fingerprints are stable only within an
explicit audit-key version; key rotation is not claimed to preserve them.

## 47. Full A3 verification strategy and V14 whole Task 7 gate

### 47.1 Slice-level genuine RED/GREEN evidence

Each A3 slice begins with a comprehensive selector that fails against the
pre-slice product for the intended missing contract. The evidence ledger
records the exact genuine RED output before the minimal GREEN. Tests then cover
the complete acceptance matrix rather than replacing genuine RED with an
inherited passing assertion.

A3b covers every supported object kind, the full artifact tuple, capture/
verify/delete/marker cleanup/absence/checkpoint boundaries, mismatch restore,
external conflict, unavailable versus contradictory disposition, explicit
re-entry and lease-expiry takeover. A3c covers published/unpublished parity,
live validation immediately before every mutation, exact 256-remove bounding,
multi-pass progress, filesystem boundaries, every durable/crash phase and the
atomic tombstone/projection/lease-release transaction. A3d covers the exact SSH
purpose and read-only permit, all five classifications, token privacy,
cursor-prefix replay and drift, every hard bound, audit/alert failure,
downgrade freshness and zero remote mutation.

After GREEN, each slice runs fresh focused normal and race tests. Required real
PostgreSQL normal/race no-skip tests apply wherever the slice exercises DB
transactions, CAS, lease ownership or durable projection. Pure target
filesystem behavior is not given an unrelated PostgreSQL requirement. The
slice appends its exact commands, durations, results and structural checks to
`research/implementation-evidence.md`, then stops before the next slice.

### 47.2 V14 dynamic and static verification

V14 re-reads every Task 7 acceptance and sections 1--47 against the final
implementation; prior focused evidence informs the review but does not replace
fresh whole-scope evidence. The dynamic gate includes:

- whole Recovery normal and race;
- all required real-PostgreSQL Recovery tests in normal and race mode with no
  skip, including schema/role residue checks;
- focused reruns for any defect corrected during V14; and
- any paired migration cross-engine validation still required by the complete
  Task 7 manifest.

The existing PostgreSQL fixture remains running and unchanged. Commands read
and URL-encode its secret only internally; they never print the password or
DSN and never restart, replace or remove the container.

The static and structural gate includes Go vet for Recovery, backend lint,
owned gofmt, whole `git diff --check`, direct-log and forbidden-mutation scans,
privacy/canary scans, paired migration/schema checks, child and parent Trellis
validation, task/parent JSON and JSONL parsing, exact manifest disjointness and
uniqueness, protected hashes, absence of future migrations, and exact branch,
HEAD/main/origin-main, staged-zero and outside-scope Git state.

V14 may declare Task 7/Child 13 implementation closure only when every A3
acceptance has fresh evidence and the whole review has zero Critical or
Important defect. That declaration does not complete Task 8 runtime/main,
commit, push, pull request, CI, merge, release automation or the parent
backup-data-preview program. Any failed gate leaves Task 7 in progress and is
corrected within the owning A3 slice or V14 review before reevaluation.

### 47.3 Rollback and current authorization boundary

Full A3 is additive behind already closed capabilities and explicit service
entry points. Before runtime composition, rollback is to leave those entry
points uncalled; no periodic worker or startup hook activates them. Remote
protocols preserve authenticated evidence on ambiguity, and durable phase
transitions are one-way, so rollback never means deleting unknown state or
rewinding a terminal row.

This written design is ready for specification review. It does not authorize
editing `implement.md`, implementing A3b, changing product code, staging,
committing, pushing, creating a pull request, switching branches or creating a
worktree. Those actions remain behind the next user review and implementation-
plan gates.
