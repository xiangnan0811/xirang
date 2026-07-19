# Backup Asset Worker Protocol And Derived Store Implementation Plan

> **Execution mode:** inline only. Do not create implementation or check
> sub-agents. Load `trellis-before-dev`, `superpowers:test-driven-development`
> and `superpowers:executing-plans` only after the complete focused planning
> package is explicitly approved and product implementation is separately
> authorized. Before any completion claim, load `trellis-check` and
> `superpowers:verification-before-completion`; finish with
> `trellis-finish-work`.

**Goal:** Add an optional persistent Worker control plane, attempt-bound
Content/Sink grants, atomic derived publication and an independently encrypted
Derived Store without exposing Provider/Repository credentials or weakening
Core availability when no Worker is deployed.

**Architecture:** One shared `backupasset/runtime.Runtime` composes a database
pull queue, dual Worker/RecoveryPoint leases, the Child 8 source boundary, the
Child 7 projection port, one-use grants, a fenced multi-artifact Sink and a
Derived Store with per-blob DEKs. A dedicated local/mTLS listener derives Worker
identity from transport; the public API receives only one sanitized Admin health
GET. The separate `asset-worker` is a protocol client with a test-injected
fake/no-op capability, not a tool image.

**Tech stack:** Go 1.26, Gin, GORM, SQLite, PostgreSQL 18, standard-library TLS,
Unix peer credentials, SHA-256, CSPRNG, AES-256-GCM,
authenticated chunked ciphertext, Prometheus, Swagger and Trellis.

## 1. Execution and approval gate

### 1.1 Current Phase 1 state

```text
task:                       .trellis/tasks/07-19-backup-assets-worker-protocol
status:                     planning
parent:                     07-12-backup-data-explorer-design (planning tracker)
branch:                     codex/backup-assets-worker-protocol
base / HEAD:                2ce71339b7f10fe759c0009ff01a100e589a700c
main / origin/main:         2ce71339b7f10fe759c0009ff01a100e589a700c
delivered program state:    9/15
focused planning review:    approved by user 2026-07-19
implementation approval:   approved by user 2026-07-19
workflow execution gate:   pending; active state requires planning
task.py start:              not_executed
product/migration/tests:    not_executed
delivery/CI/merge/release:  not_executed
```

This plan is descriptive, not self-authorizing. The user has now approved both
the complete planning package and the separate `task.py start`/implementation
gate. The remaining prerequisite is an explicit Phase 2 transition from the
active workflow state; until that transition arrives, Sections 1.2-16 remain
future instructions and no product path may be edited.

### 1.2 Future approved-start preflight

After the workflow state transitions to Phase 2 (both user approvals are
recorded above), reload the required skills/specs and execute these checks
before any product edit:

```bash
cd /home/murray/.codex/worktrees/8f0f/xirang
git fetch origin --prune
test "$(git branch --show-current)" = "codex/backup-assets-worker-protocol"
test "$(git rev-parse HEAD)" = "2ce71339b7f10fe759c0009ff01a100e589a700c"
test "$(git rev-parse main)" = "2ce71339b7f10fe759c0009ff01a100e589a700c"
test "$(git rev-parse origin/main)" = "2ce71339b7f10fe759c0009ff01a100e589a700c"
test "$(git merge-base HEAD origin/main)" = "2ce71339b7f10fe759c0009ff01a100e589a700c"
test "$(python3 -c 'import json; print(json.load(open(".trellis/tasks/07-19-backup-assets-worker-protocol/task.json"))["status"])')" = planning
git status --short
```

Expected tracked changes before start are exactly the parent child registration
and the active Child 10 Phase 1 manifest in §2.1. Then prove the migration slot:

```bash
for engine in sqlite postgres; do
  test ! -e "backend/internal/database/migrations/$engine/000067_backup_asset_processing.up.sql"
  test ! -e "backend/internal/database/migrations/$engine/000067_backup_asset_processing.down.sql"
  for version in 000068 000069 000070 000071; do
    test -z "$(find "backend/internal/database/migrations/$engine" -maxdepth 1 -name "${version}_*" -print -quit)"
  done
done
```

Also re-run the symbol checks in `research/current-main-evidence.md`: current
migration head, `LeaseHolderProcessingJob`, `ValidateFenceTx`, Child 7 ingest,
Child 8 SourceResolver/Broker, key domains, settings snapshot, runtime accessors,
router and PostgreSQL CI selector.

If `origin/main` moved, 067 became occupied, a current-main contract changed, or
an unrelated dirty path overlaps §2, stop. Rebuild/rebase from the latest merged
main under repository workflow, amend all three planning documents and obtain
review again. Do not copy from an unmerged sibling, silently renumber, or alter
068-071.

Only after both recorded approval gates, the Phase 2 workflow transition and a
clean preflight may this run once:

```bash
python3 ./.trellis/scripts/task.py start .trellis/tasks/07-19-backup-assets-worker-protocol
```

Record the resulting `in_progress` status before TDD. Do not infer start
authorization from planning approval.

## 2. Exact file manifest

Any tracked path outside this manifest requires a focused written amendment and
approval before edit. Do not use directory-wide or wildcard staging.

### 2.1 Current Phase 1 planning manifest

```text
.trellis/tasks/07-12-backup-data-explorer-design/task.json
.trellis/tasks/07-19-backup-assets-worker-protocol/check.jsonl
.trellis/tasks/07-19-backup-assets-worker-protocol/design.md
.trellis/tasks/07-19-backup-assets-worker-protocol/implement.jsonl
.trellis/tasks/07-19-backup-assets-worker-protocol/implement.md
.trellis/tasks/07-19-backup-assets-worker-protocol/prd.md
.trellis/tasks/07-19-backup-assets-worker-protocol/research/current-main-evidence.md
.trellis/tasks/07-19-backup-assets-worker-protocol/task.json
```

There are seven Child files plus the parent child-registration edit. No product,
migration, test, CI, frontend, deploy or public documentation path belongs to
Phase 1.

### 2.2 Future implementation create manifest

Focused exact-manifest amendment approved by program control on 2026-07-19:
the fenced Sink/manifest implementation is split into dedicated
`derived_manifest*` files. This is a file-ownership correction only and does
not expand the frozen behavior.

```text
.trellis/tasks/07-19-backup-assets-worker-protocol/research/implementation-evidence.md

backend/internal/model/backup_asset_processing.go

backend/internal/database/migrations/sqlite/000067_backup_asset_processing.up.sql
backend/internal/database/migrations/sqlite/000067_backup_asset_processing.down.sql
backend/internal/database/migrations/postgres/000067_backup_asset_processing.up.sql
backend/internal/database/migrations/postgres/000067_backup_asset_processing.down.sql

backend/internal/backupasset/content/attempt_broker.go
backend/internal/backupasset/content/attempt_broker_test.go

backend/internal/backupasset/repository/private_runtime_root.go
backend/internal/backupasset/repository/private_runtime_root_test.go

backend/internal/backupasset/processing/contracts.go
backend/internal/backupasset/processing/contracts_test.go
backend/internal/backupasset/processing/work_key.go
backend/internal/backupasset/processing/work_key_test.go
backend/internal/backupasset/processing/state.go
backend/internal/backupasset/processing/state_test.go
backend/internal/backupasset/processing/coordinator.go
backend/internal/backupasset/processing/coordinator_test.go
backend/internal/backupasset/processing/scheduler.go
backend/internal/backupasset/processing/scheduler_test.go
backend/internal/backupasset/processing/grants.go
backend/internal/backupasset/processing/grants_test.go
backend/internal/backupasset/processing/protocol.go
backend/internal/backupasset/processing/protocol_test.go
backend/internal/backupasset/processing/worker_client.go
backend/internal/backupasset/processing/worker_client_test.go
backend/internal/backupasset/processing/transport.go
backend/internal/backupasset/processing/transport_test.go
backend/internal/backupasset/processing/transport_local_linux.go
backend/internal/backupasset/processing/transport_local_linux_test.go
backend/internal/backupasset/processing/transport_local_other.go
backend/internal/backupasset/processing/derived_crypto.go
backend/internal/backupasset/processing/derived_crypto_test.go
backend/internal/backupasset/processing/derived_store.go
backend/internal/backupasset/processing/derived_store_test.go
backend/internal/backupasset/processing/derived_lifecycle.go
backend/internal/backupasset/processing/derived_lifecycle_test.go
backend/internal/backupasset/processing/derived_manifest.go
backend/internal/backupasset/processing/derived_manifest_test.go
backend/internal/backupasset/processing/reconciler.go
backend/internal/backupasset/processing/reconciler_test.go
backend/internal/backupasset/processing/metrics.go
backend/internal/backupasset/processing/metrics_test.go
backend/internal/backupasset/processing/behavior_integration_test.go
backend/internal/backupasset/processing/testutil_test.go

backend/internal/backupasset/runtime/processing_runtime.go
backend/internal/backupasset/runtime/processing_runtime_test.go

backend/internal/api/worker_router.go
backend/internal/api/worker_router_test.go
backend/internal/api/handlers/backup_worker_handler.go
backend/internal/api/handlers/backup_worker_handler_test.go

backend/cmd/asset-worker/main.go
backend/cmd/asset-worker/main_test.go
```

File ownership is deliberate:

- `content/attempt_broker*` seals Child 8 source resolution behind an
  attempt-bound internal port.
- `repository/private_runtime_root*` generalizes the existing cache-root proof
  for distinct private roots without exposing stored locators.
- `processing` owns persistent work, grants, protocol, crypto/store, lifecycle,
  reconciliation and the reusable Worker client. It never imports
  `repository` or a Provider package.
- `runtime/processing_runtime*` owns lifecycle composition; it is not a second
  service graph.
- `api/worker_router*` is a dedicated non-CORS listener/router. The public
  handler file contains only the sanitized Admin GET adapter.
- `cmd/asset-worker` contains no real capability, tool runner or updater.

If implementation proves a listed conceptual split unnecessary, amend and
re-review the exact manifest before omitting/merging it; do not create empty or
pass-through files merely to satisfy this list.

### 2.3 Future implementation modify manifest

```text
.github/workflows/ci.yml

backend/internal/database/backup_asset_migrations_integration_test.go

backend/internal/backupasset/keyring.go
backend/internal/backupasset/keyring_test.go
backend/internal/backupasset/lease.go
backend/internal/backupasset/service.go
backend/internal/backupasset/service_test.go
backend/internal/backupasset/repository/content_read.go
backend/internal/backupasset/repository/testutil_test.go
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go
backend/internal/backupasset/runtime/admission_controller_test.go

backend/internal/settings/service.go
backend/internal/settings/service_test.go

backend/internal/api/router.go
backend/internal/api/router_test.go
backend/internal/api/backup_asset_rbac_test.go
backend/internal/api/docs/docs.go

backend/cmd/server/main.go
backend/cmd/server/main_test.go

.trellis/spec/backend/database-guidelines.md
```

The CI change is limited to adding migration 067 and
`TestProcessingBehaviorPostgres` to the existing PostgreSQL 18 job. It must not
build/publish a Worker image. The backend spec change updates the paired
migration head/real-PostgreSQL selector after implementation; it is not public
release/deployment documentation.

### 2.4 Explicitly unchanged without an approved amendment

```text
backend/go.mod
backend/go.sum
backend/internal/backupasset/domain.go
backend/internal/backupasset/domain_test.go
backend/internal/backupasset/lease_test.go
backend/internal/backupasset/search/**
backend/internal/backupasset/provider/**
backend/internal/database/migrator.go
backend/internal/database/migrations/sqlite/000062_*
backend/internal/database/migrations/sqlite/000063_*
backend/internal/database/migrations/sqlite/000064_*
backend/internal/database/migrations/sqlite/000065_*
backend/internal/database/migrations/sqlite/000066_*
backend/internal/database/migrations/sqlite/000068_*
backend/internal/database/migrations/sqlite/000069_*
backend/internal/database/migrations/sqlite/000070_*
backend/internal/database/migrations/sqlite/000071_*
backend/internal/database/migrations/postgres/000062_*
backend/internal/database/migrations/postgres/000063_*
backend/internal/database/migrations/postgres/000064_*
backend/internal/database/migrations/postgres/000065_*
backend/internal/database/migrations/postgres/000066_*
backend/internal/database/migrations/postgres/000068_*
backend/internal/database/migrations/postgres/000069_*
backend/internal/database/migrations/postgres/000070_*
backend/internal/database/migrations/postgres/000071_*

web/**
deploy/**
docker-compose*.yml
docs/**
README.md
CHANGELOG.md
package.json
package-lock.json
scripts/**
```

No new Go dependency is planned. Provider byte mutation, direct Worker Provider
access, Command Provider support, public preview-job mutation, frontend bundle,
real updater/capability/tool, sandbox container, Worker image/Compose, release
and deploy changes are forbidden. If standard-library primitives cannot satisfy
the reviewed cryptographic/transport contract, stop for security review before
changing `go.mod`.

### 2.5 Workflow-owned completion paths

After implementation and verification, `trellis-finish-work` may move the active
task to the deterministic archive and update the shared journal/index. Those
workflow outputs are not to be guessed or staged with wildcards. Inspect the
actual output and stage only exact paths such as:

```text
.trellis/tasks/archive/2026-07/07-19-backup-assets-worker-protocol/<actual files>
.trellis/workspace/weibo/index.md                          (only if changed)
.trellis/workspace/weibo/journal-1.md                     (only if changed)
```

All work remains on this one `codex/` branch and goes through one PR. The parent
is never archived by this child.

## 3. Frozen behavior and test matrix

Every cell below requires executable evidence on SQLite and real PostgreSQL
where marked. An in-memory fake may supplement but cannot replace engine
behavior.

| Contract | Required proof |
|---|---|
| Migration/model | paired 067 apply/down, table/index/CHECK/FK/UTC/model parity, 066 preservation, blocked unsafe down |
| State | all 15 `ProcessingState` values; `fetching` and `materializing` independent; legal transition/revision/error products only |
| Work key | typed canonical round trip and one-field difference matrix including all size/codec/page/quality/limit fields |
| Queue | persistent same-key coalescing, concurrent unique current slot, stable order, interactive reserve/background isolation |
| Interest | idempotent add/remove, priority recompute, one interest cancel does not stop work, final interest revokes grants then cancels |
| Leases/fences | dual heartbeat, crash/takeover new fence, old attempt/late output rejection, absolute deadline, cancel/supersede/expiry |
| Trust | same-UID UDS, non-Linux unsupported, remote disabled default, TLS 1.3 mTLS URI SAN, identity revision, drain/quarantine |
| Input | one-use job/attempt/Worker/fence activation, bounded stat/sequential/multi-Range, atomic request/byte/in-flight accounting |
| Sink | encrypted staging, bounded count/item/total, MIME/role/size/digest/completeness/policy checks, one manifest commit |
| Derived | independent KEK, random per-blob DEK, authenticated chunks/AAD, tamper/wrong-key detection, rewrap without ciphertext rewrite |
| References | cross-RP physical sharing, source-local authorization, checked refcount, last-reference cryptographic destruction |
| Publication/revoke | only current fence publishes; source/policy drift rejects; Search-first revoke at every injected crash point |
| Recovery | abandoned attempt/grant/staging/orphan/refcount/rewrap/projection reconciliation moves forward idempotently |
| Degradation | disabled/no Worker leaves Core ready and quiet; no failed job/alert noise; sanitized informational admin result |
| Protocol client | fake/no-op test lifecycle proves handshake/pull/heartbeat/fetch/upload/manifest/cancel/drain/graceful shutdown |
| API | internal strict decode/body/rate/auth; public Admin-only feature gate, sanitized DTO, Swagger/RBAC coverage |

## 4. Task 1 — Paired migration 067 and Go model parity

**Files:** model file, four 067 SQL files, migration integration test, keyring
files, database spec, PostgreSQL CI selector.

- [ ] Write `TestBackupAssetMigration067SQLite` and
  `TestBackupAssetMigration067Postgres` before DDL. Tests must enumerate every
  067-owned table/column/index/FK/CHECK and assert UTC/no DB timestamp defaults
  plus `model.BackupAsset*` shape parity.
- [ ] Add negative fixtures for every closed state/error/trust/health/grant/
  completeness/Derived/updater product, invalid terminal timestamps, budget
  overflow and dangling composite source/attempt/blob references.
- [ ] Prove 067 apply preserves all 062-066 data and indexes. Prove 067 down
  preserves 066 and blocks before mutation while any processing row,
  `processing_job` lease, Derived reference/blob/key or unrevoked projection
  evidence remains.
- [ ] Implement paired `000067_backup_asset_processing` SQL and the model file.
  SQLite and PostgreSQL may use engine-appropriate DDL but must expose the same
  behavior. 067 safely expands `wrapped_domain_keys.domain` to include
  `derived_store`; it does not edit migration 062/065 or add a lease holder.
- [ ] Extend keyring validation with optional `KeyDomainDerivedStore`. It is not
  appended to unconditional `RequiredKeyDomains`; feature-disabled startup
  must not create it. Cover ensure/active/version/rotate/verify-only/lost/master
  rewrap behavior and projection-safe invalidation callback requirements.
- [ ] Extend `.github/workflows/ci.yml` from 062-066 to 062-067 and add the real
  PostgreSQL Processing behavior selector only. Update the backend database
  spec's current head and selector after GREEN.

Focused commands use the corrected selector, never the parent's historical
`BackupAssetMigration066` typo:

```bash
cd backend
go test ./internal/database -run '^TestBackupAssetMigration067SQLite$' -count=1
test -n "${TEST_POSTGRES_DSN:-}"
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
  go test ./internal/database -run '^TestBackupAssetMigration067Postgres$' -count=1
cd ..
```

An absent/unreachable PostgreSQL DSN is a required-gate failure/blocker, not a
skip or pass.

## 5. Task 2 — Closed state machine and canonical work identity

**Files:** `contracts*`, `state*`, `work_key*`.

- [ ] Write table-driven transition tests for the exact state set:

```text
queued, leased, fetching, materializing, processing, uploading, validating,
retry_wait, cancel_requested, canceled, succeeded, failed, superseded, expired
```

- [ ] Prove `fetching -> processing` and
  `fetching -> materializing -> processing` as distinct legal paths. Reject a
  slash-joined state, `queued -> canceled`, terminal exits, retry with the same
  attempt/fence, and transition revision skips/replays.
- [ ] Test state, revision, stable processing error, attempt/Worker lease,
  RecoveryPoint lease/fence, retry schedule, cancel reason, supersede reason and
  expiry reason independently. Permanent errors never retry; transient errors
  use bounded retry; contract/security errors quarantine and do not blind retry.
- [ ] Write typed descriptor tests that reject unknown/duplicate members,
  invalid UTF-8, non-canonical/non-finite numbers, ambiguous defaults,
  unsupported schema and values outside hard bounds before hashing.
- [ ] Build a property/difference matrix. Changing any source fingerprint,
  entry fingerprint, Catalog generation, Provider capability revision,
  capability/schema, pipeline fingerprint, output profile, security-policy
  revision, dimension, codec, page/member/frame/time range, quality, language/
  model/profile, max pages/pixels/duration/expanded bytes, truncation policy,
  output bytes/count or other output-affecting parameter must change `work_key`.
  Canonically identical typed values and map insertion order must not.
- [ ] Keep priority, requester identity, retry count and heartbeat out of the
  output identity. Never log/return the canonical hash input.

```bash
cd backend
go test ./internal/backupasset/processing \
  -run '^(TestProcessingState|TestWorkKey|TestCanonical)' -count=1
cd ..
```

## 6. Task 3 — Persistent coordinator, scheduling, interests and dual leases

**Files:** `coordinator*`, `scheduler*`, state/model plus Processing behavior
fixture.

- [ ] Write concurrent SQLite and PostgreSQL tests proving one current job for
  simultaneous identical descriptors, exact active-artifact reuse and new job
  creation after a terminal/unusable current result. The database unique
  constraint, not an in-memory mutex alone, resolves creators.
- [ ] Test queue maximum, stable priority/queued-time/job-ID ordering and
  starvation resistance. Background cannot consume interactive reserve;
  interactive may borrow idle background capacity without unsafe preemption.
- [ ] Test interest owner/type/idempotency, effective priority upgrade/recompute
  and source-local lifecycle. Removing one interest preserves the shared job;
  the last removal revokes both grants before `cancel_requested` becomes
  visible, then waits for acknowledge/lease expiry before `canceled`.
- [ ] Test no-compatible-Worker `not_deployed` before job persistence. A known
  compatible Worker that is temporarily offline may leave bounded queued work,
  but absence must not create failed rows or alerts.
- [ ] On pull, atomically bind the Worker attempt lease and an existing
  `processing_job` RecoveryPoint lease. Heartbeat renews both and uses the
  earlier effective deadline. Loss of either removes publication authority.
- [ ] Test crash/timeout takeover: new attempt and `LeaseService.Takeover` fence,
  old grants revoked, old staging hidden/destroyed, legal retry transition and
  permanent rejection of old heartbeat/output even after reconnect.
- [ ] Cover final source revalidation, source fingerprint change ->
  `superseded`, source/RP/deadline expiry -> `expired`, cancellation and retry
  exhaustion. None collapse into a generic failed backup.

```bash
cd backend
go test -race ./internal/backupasset/processing \
  -run '^(TestCoordinator|TestScheduler|TestInterest|TestLease|TestProcessingBehaviorSQLite)' \
  -count=1
cd ..
```

## 7. Task 4 — AttemptBroker and one-use Input/Sink grants

**Files:** Content attempt broker/source contracts, grants, Repository content
read/private root files.

- [ ] Write Content tests for an internal `AttemptBroker` that accepts only
  composite AssetRef, exact Catalog/source/entry fingerprints, closed modes,
  per-call/cumulative/in-flight/request budgets and an absolute expiry. It owns
  `SourceResolver`; Processing receives only an attempt session interface and
  cannot type-assert a Repository/Provider handle.
- [ ] Prove bounded stat, sequential and repeated single-Range reads. Reserve
  budgets atomically before source open; charge overflow probes and unknown
  crash outcome conservatively; cancel/join/close every reader; revalidate
  mutable source before/after reads and before publication.
- [ ] Keep Command Provider's exact typed
  `task_artifact_contract_missing`; do not add a fallback command or Provider
  byte mutation.
- [ ] Write grant activation race/replay tests. Input and Sink use separate
  CSPRNG 256-bit secrets, persist only hashes, bind job/attempt/transport Worker/
  fence/TTL, and atomically consume once. Wrong binding, replay, revoke, expiry
  and lease loss fail closed without revealing which field differed.
- [ ] Persist atomic request/byte/in-flight reservations and reconcile abandoned
  rows. Cancellation revokes grants before a state transition; no secret or
  fence appears in logs/metrics/admin DTO.
- [ ] Generalize private-runtime-root validation so Content cache and Derived
  roots both reject non-absolute/unclean/root paths, `/data|/backup|/logs`
  ancestry, source ancestry, symlinks/traversal/special files and unprovable
  local Rclone overlap. Errors remain closed and do not expose locators.

```bash
cd backend
go test -race ./internal/backupasset/content ./internal/backupasset/repository \
  -run 'Attempt|Grant|PrivateRuntimeRoot|ContentCacheRoot|ProviderBytes|Command' \
  -count=1
cd ..
```

## 8. Task 5 — Derived Store cryptography, atomic sets and references

**Files:** `derived_crypto*`, `derived_store*`, `derived_lifecycle*`, model,
keyring and private root.

- [ ] Write deterministic-format tests with injected entropy only in tests.
  Production creates a fresh random 32-byte DEK per new physical blob. Each
  AES-256-GCM data nonce is an 8-byte per-blob random prefix plus a 4-byte
  big-endian chunk index; envelope wrapping uses a separate fresh random
  12-byte nonce. AAD binds format, blob ID, plaintext digest/size, chunk
  size/index and Derived KEK version where applicable.
- [ ] Cover empty/single/multi-chunk blobs, truncated/reordered/duplicated/wrong
  chunk, bit tamper, wrong AAD/digest/size/key/version, chunk-index overflow and
  partial write. No plaintext file or ordinary-temp fallback may appear.
- [ ] Wrap every DEK under `derived_store`, store only the envelope and opaque
  locator, and verify master-key rewrap does not change Derived key plaintext/
  version. Derived KEK rotation rewraps only small DEK envelopes in bounded
  resumable batches; ciphertext digest/mtime/content stays unchanged.
- [ ] Stream Sink data through size limiter -> SHA-256 -> chunk encryptor ->
  safe staging. Enforce closed role/MIME/profile, ordinal, member count,
  per-member/total size, digest, completeness/coverage, source and
  security-policy revision. Recompute every digest in Core.
- [ ] Test exact source authorization on every reference. Multiple RPs may
  share one physical blob but have distinct reference lifecycle; removing one
  keeps the other readable. Reconcile `ref_count` from live rows. Last reference
  marks the key non-readable/erases wrapped DEK before deleting exact
  ciphertext; deletion failure stays retryable but unreadable.
- [ ] Prove quota admission/physical-vs-logical accounting, safe opaque rename,
  file and directory durability, no unscoped `RemoveAll`, and Worker inability
  to see root/locator.

```bash
cd backend
go test -race ./internal/backupasset/processing \
  -run '^(TestDerived|TestSink|TestArtifact|TestBlob|TestReference|TestKEK|TestQuota)' \
  -count=1
cd ..
```

## 9. Task 6 — Fenced manifest publication, Search-first revoke and recovery

**Files:** coordinator/grants/Derived lifecycle/reconciler and Processing
behavior fixture. Existing Search files remain unchanged.

- [ ] Write a one-commit manifest race test: close Sink, validate complete
  member set/schema/profile/limits, final source/policy check, durable ciphertext
  finalize, then lock job/attempt/grant/staging and call
  `LeaseService.ValidateFenceTx` inside the publication transaction. Exactly one
  current fence can publish the entire set.
- [ ] Reject/destroy invisible staging for duplicate manifest, old attempt,
  late upload, cancel, expired lease, superseded source, changed policy,
  invalid MIME/count/size/digest/completeness or Worker quarantine. Digest or
  equal work key never resurrects an old fence.
- [ ] Publish content/OCR only through `ContentIndexIngest`; a job is succeeded
  only after required projection publication. A crash between readable
  derivative and projection is forward-reconciled without exposing a partial
  set.
- [ ] For revoke, expiry, active Derived key loss and rollback, inject a failure
  before/after every step and assert this invariant:

```text
Child 7 projection revoke committed
  -> Derived reference/set unavailable or revoked
  -> wrapped DEK made unavailable/destroyed when last reference
  -> exact ciphertext cleanup
```

- [ ] If Search revoke fails, retain the reference/key/blob. If a later step
  fails, Search is already safe and reconciliation only advances. Queries must
  never hit a posting whose excerpt/artifact is destroyed (`ghost projection`).
- [ ] Startup/periodic reconciliation covers abandoned attempt/Worker/RP lease,
  grants, reservation, upload/staging orphan, DB-without-file,
  file-without-DB, wrong refcount, interrupted rewrap, pending publication and
  pending projection revoke in bounded idempotent batches.
- [ ] Generic updater metadata tests accept only closed source/version/digest/
  signing-fingerprint/verification/activation/failure facts. Reject URL secret,
  credential, raw manifest/output/bundle bytes. No updater service/network path
  is implemented.

```bash
cd backend
go test -race ./internal/backupasset/processing \
  -run 'Manifest|Fence|Late|Revoke|Ghost|KeyLoss|Rollback|Reconcile|UpdaterMetadata' \
  -count=1
cd ..
```

## 10. Task 7 — Trusted protocol transport and protocol-only Worker

**Files:** protocol/client/transport files, dedicated API Worker router and
`cmd/asset-worker`.

- [ ] Write local transport tests before listener code. Socket parent must be
  Core-owned/non-symlink 0700; socket 0600; stale removal requires exact type/
  owner proof. Linux derives PID/UID/GID through `SO_PEERCRED` and requires the
  Core effective UID. Non-Linux returns typed unsupported and never falls back
  to TCP/header/client self-report.
- [ ] Write remote tests with generated test certificates. Default disabled
  means no bind. Enabled mode requires complete material, reviewed non-wildcard
  listen address, TLS 1.3 only, `RequireAndVerifyClientCert`, private CA and one
  exact verified URI SAN
  `spiffe://<trust-domain>/asset-worker/<id>`. Reject CN/subject/header fallback,
  wildcard/multiple/conflicting SANs, wrong EKU/domain/CA, expiry and identity
  inheritance across certificate revision.
- [ ] Strictly decode handshake/capabilities and every internal request. Reject
  unknown/duplicate/trailing fields, incompatible protocol/schema/limits and
  unregistered real capability. Enforce fixed route labels, pre-read body
  ceilings and per-identity rate/byte budgets.
- [ ] Match only active trusted ready/degraded capabilities. Draining receives
  no new work. Contract/security failures atomically quarantine identity,
  revoke active grants and stop pulls; they do not blind retry. Remote trust has
  no bearer/API-key alternative and is off by default.
- [ ] Assert protocol DTO/source scans contain no DB DSN, Provider kind/locator,
  Repository/Task binding, SSH/Restic/Rclone credential, host/native path,
  original filename, user query, updater credential, arbitrary URL, raw output,
  activation secret persistence or logging.
- [ ] Test the separate Worker client's full fake/no-op lifecycle: handshake,
  pull, dual heartbeat, cancel, bounded multi-Range Input, bounded Sink upload,
  one manifest, draining, SIGTERM and bounded graceful shutdown. Production
  registry remains empty and cannot advertise OCR/thumbnail/text/media/archive/
  malware capability.
- [ ] Use only the configured Core UDS or mTLS endpoint. Do not implement
  container/rootfs/cgroup/seccomp/AppArmor/tmpfs/swap/network/DNS sandbox,
  updater, image or Compose; those remain Child 11 and no production capability
  is registered before that gate.

```bash
cd backend
go test -race ./internal/backupasset/processing ./internal/api ./cmd/asset-worker \
  -run 'Worker|Protocol|Transport|PeerCred|MTLS|Noop|Graceful' -count=1
go build ./cmd/asset-worker
cd ..
```

## 11. Task 8 — Settings, shared runtime, Admin health and graceful degradation

**Files:** settings/Foundation/runtime, public handler/router/RBAC/Swagger and
server wiring.

- [ ] Add every frozen key from `design.md` §16 to `settings.Service` and the
  atomic backup-asset snapshot, with exact default/bounds/cross-setting tests.
  `backup_assets.enabled=false`, local=false and remote=false remain defaults.
  Socket, TLS material/trust domain and Derived root/chunk format require
  restart; sensitive material paths are sanitized/encrypted by registry rules.
- [ ] Add typed `ProcessingConfig` parsing to FoundationService. Prove one
  coherent snapshot under concurrent setting updates and reject partial remote
  trust, heartbeat >= half lease, aggregate limit inversions and unsafe roots
  before listener/store construction.
- [ ] Extend the existing runtime graph with one Coordinator, shared Keyring/
  LeaseService, AttemptBroker, Search ingest, Repository root validator and
  Derived store. `cmd/server` receives only narrow listener/admin lifecycle
  ports; no second DB/Provider/lease/key graph exists.
- [ ] Test startup and shutdown order from `design.md` §17, including feature
  disabled, no Worker configured, Derived key missing/lost, reconciliation
  failure, drain timeout and server shutdown. Core Catalog/Search/Content/
  workspace remains ready when Processing is unavailable.
- [ ] Add only `GET /api/v1/admin/backup-asset-processing`: JWT, Admin,
  feature-gated, standard API middleware plus focused 30/min limit, no body or
  query. Operator/Viewer fail before service access. Use response helpers and a
  sanitized aggregate DTO; never expose IDs, AssetRef, grant/fence/session,
  source/capability parameters, path/blob/cert/peer or raw error.
- [ ] Keep Worker routes exclusively in `NewWorkerRouter` on the dedicated
  authenticated listener. They do not inherit browser CORS, X-Forwarded-* or
  JWT Worker trust.
- [ ] Add safe low-cardinality metrics/logs only. No Worker configured is
  informational and never an alert/failed job; quarantine and sustained bounded
  reconciliation failures are observable without secret labels.
- [ ] Regenerate the sole Swagger artifact and cover 200/403/404/429/503 plus
  route/RBAC registration. Add no frontend file or public preview mutation.

```bash
cd backend
go test -race ./internal/settings ./internal/backupasset ./internal/backupasset/runtime \
  ./internal/api/handlers ./internal/api ./cmd/server \
  -run 'Processing|Worker|Derived|BackupAsset' -count=1
cd ..
make swag-init
```

## 12. Cross-engine behavior parity and race gate

`behavior_integration_test.go` must expose exact entry points:

```go
func TestProcessingBehaviorSQLite(t *testing.T)
func TestProcessingBehaviorPostgres(t *testing.T)
```

Both call one shared contract suite. Required parity includes concurrent
same-key creation, partial unique current slots, state/revision CAS, priority
reserve, final-interest cancel, Worker/RP lease takeover, old fence and late
output, one-use activation, atomic budgets, manifest commit, quarantine,
Derived envelope/tamper/refcount, Search-first revoke and reconciliation. SQL
syntax-specific assertions supplement but do not replace shared behavior.

```bash
cd backend
go test ./internal/backupasset/processing \
  -run '^TestProcessingBehaviorSQLite$' -count=1
test -n "${TEST_POSTGRES_DSN:-}"
REQUIRE_POSTGRES_PROCESSING_TEST=1 \
  go test ./internal/backupasset/processing \
  -run '^TestProcessingBehaviorPostgres$' -count=1
go test -race ./internal/backupasset/processing -count=3
cd ..
```

Any live PostgreSQL setup not actually executed remains `not_executed`; it
cannot be replaced by SQLite, `sqlmock`, schema text inspection or a skip.

## 13. Full validation manifest

Run from repository root after all focused tasks are GREEN. Capture command,
exit status and concise result in a new implementation evidence file only when
implementation is authorized; do not pre-mark results.

### 13.1 Formatting, focused tests and builds

```bash
gofmt -w \
  backend/internal/model/backup_asset_processing.go \
  backend/internal/backupasset/content/attempt_broker.go \
  backend/internal/backupasset/content/attempt_broker_test.go \
  backend/internal/backupasset/repository/private_runtime_root.go \
  backend/internal/backupasset/repository/private_runtime_root_test.go \
  backend/internal/backupasset/processing/*.go \
  backend/internal/backupasset/runtime/processing_runtime.go \
  backend/internal/backupasset/runtime/processing_runtime_test.go \
  backend/internal/api/worker_router.go \
  backend/internal/api/worker_router_test.go \
  backend/internal/api/handlers/backup_worker_handler.go \
  backend/internal/api/handlers/backup_worker_handler_test.go \
  backend/cmd/asset-worker/main.go \
  backend/cmd/asset-worker/main_test.go

cd backend
go test -race ./internal/backupasset/processing ./internal/backupasset/content \
  ./internal/backupasset/repository ./internal/backupasset/runtime \
  ./internal/api/... ./cmd/asset-worker ./cmd/server -count=1
go build ./cmd/asset-worker
go build ./cmd/server
cd ..
make backend-test
make backend-build
make lint-backend
```

Formatting may use the mechanical `gofmt` command. No wildcard is allowed in
Git staging; the wildcard above only formats the reviewed Processing package.

### 13.2 Required real PostgreSQL and migration gate

```bash
cd backend
test -n "${TEST_POSTGRES_DSN:-}"
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
  go test ./internal/database \
  -run 'Test(BackupAssetMigration0(62|63|64|65|66|67)Postgres|PostgresTimestamptzScanUsesConfiguredUTC)' \
  -count=1
REQUIRE_POSTGRES_PROCESSING_TEST=1 \
  go test ./internal/backupasset/processing \
  -run '^TestProcessingBehaviorPostgres$' -count=1
cd ..
```

This is the exact correction to parent implement §11: Child 10 selects
`BackupAssetMigration067`, not 066. Migration 068-071 is never selected or
created by this child.

### 13.3 Swagger, project and source-boundary gates

```bash
make swag-init
bash scripts/check-doc-freshness.sh
make check
git diff --check

test -z "$(git status --short -- web deploy docs README.md CHANGELOG.md 'docker-compose*.yml')"
test -z "$(find backend/internal/database/migrations -type f \
  \( -name '000068_*' -o -name '000069_*' -o -name '000070_*' -o -name '000071_*' \) \
  -newer .trellis/tasks/07-19-backup-assets-worker-protocol/task.json -print)"

rg -n 'task_artifact_contract_missing' \
  backend/internal/backupasset/domain.go \
  backend/internal/backupasset/repository/content_read.go

if rg -n '(provider_locator|repository_config|ssh_private|RESTIC_PASSWORD|RCLONE_CONFIG|database_dsn|host_path|original_filename|updater_credential|raw_output)' \
  backend/internal/backupasset/processing \
  backend/internal/api/worker_router.go \
  backend/cmd/asset-worker; then
  echo 'review every match: only explicit negative tests/comments may remain'
fi

cd backend
go list -deps ./cmd/asset-worker | \
  rg '(tesseract|libreoffice|ffmpeg|clamav|archive|updater)' && exit 1 || true
cd ..
```

The timestamp-based migration scan is a secondary guard only; the authoritative
scope proof is exact `git status --short`/`git diff --name-only` equality with
§2. A match in a negative test/comment is reviewed, not automatically declared
a leak. Record exact outputs and do not call a gate passing if it was skipped,
unavailable or only partially run.

### 13.4 Final exact scope/status proof

```bash
git status --short
git diff --name-only --diff-filter=ACMRTUXB | sort
git ls-files --others --exclude-standard | sort
git diff --check

test "$(python3 -c 'import json; print(json.load(open(".trellis/tasks/07-19-backup-assets-worker-protocol/task.json"))["parent"])')" = 07-12-backup-data-explorer-design
test "$(python3 -c 'import json; print(json.load(open(".trellis/tasks/07-12-backup-data-explorer-design/task.json"))["status"])')" = planning
```

During implementation the child should be `in_progress`; only
`trellis-finish-work` changes/archives it after all accepted gates. The parent
must remain `planning` throughout.

## 14. Rollback and schema-down choreography

### 14.1 Runtime rollback

1. Disable new Processing admission and stop both Worker listeners; keep Core
   Catalog/Search/Content/workspace live.
2. Mark Workers draining, stop pulls, revoke unactivated/active Input and Sink
   grants, wait bounded in-flight calls, then cancel/expire attempts.
3. For every published content/OCR artifact, call Child 7
   `RevokeContentProjection` and confirm its transaction. If it fails, stop and
   retain the Derived reference/key/blob.
4. Mark Derived sets/references unavailable/revoked. On last reference, make the
   wrapped DEK unavailable/destroy it, then delete exact ciphertext/staging and
   reconcile failures forward.
5. Reconcile Worker/RP leases, reservations, manifests, orphans, refcounts,
   rewraps and pending projection revokes until no 067-owned live state remains.
6. Leave `backup_assets.enabled=false`, Worker transports disabled and the
   protocol binary absent/idle. Original Provider/Catalog/Content data remains
   available.

Turning off the feature flag alone is not rollback proof.

### 14.2 Schema down

Only after the runtime preparation above:

1. prove no 067-owned processing/updater/Derived table row remains;
2. prove no `processing_job` RecoveryPoint lease remains;
3. prove no Search content/OCR posting or excerpt references a Derived artifact;
4. prove no `derived_store` wrapped-domain-key row/envelope remains;
5. invoke the paired 067 down on the target database;
6. prove migration 066 schema and data remain intact.

The down migration performs its own fail-closed checks before mutation. If any
proof fails, retain additive 067 schema and repair forward; never force-delete a
key before projection revoke and never edit migration history.

## 15. Exact future staging manifest

After implementation, accepted verification and a final §2 equality audit,
stage the work commit with exact paths only. This expands the parent's unsafe
broad `git add backend/internal/model ... backend/cmd` command:

```bash
git add \
  .github/workflows/ci.yml \
  .trellis/spec/backend/database-guidelines.md \
  .trellis/tasks/07-12-backup-data-explorer-design/task.json \
  .trellis/tasks/07-19-backup-assets-worker-protocol/check.jsonl \
  .trellis/tasks/07-19-backup-assets-worker-protocol/design.md \
  .trellis/tasks/07-19-backup-assets-worker-protocol/implement.jsonl \
  .trellis/tasks/07-19-backup-assets-worker-protocol/implement.md \
  .trellis/tasks/07-19-backup-assets-worker-protocol/prd.md \
  .trellis/tasks/07-19-backup-assets-worker-protocol/research/current-main-evidence.md \
  .trellis/tasks/07-19-backup-assets-worker-protocol/research/implementation-evidence.md \
  .trellis/tasks/07-19-backup-assets-worker-protocol/task.json \
  backend/internal/model/backup_asset_processing.go \
  backend/internal/database/migrations/sqlite/000067_backup_asset_processing.up.sql \
  backend/internal/database/migrations/sqlite/000067_backup_asset_processing.down.sql \
  backend/internal/database/migrations/postgres/000067_backup_asset_processing.up.sql \
  backend/internal/database/migrations/postgres/000067_backup_asset_processing.down.sql \
  backend/internal/database/backup_asset_migrations_integration_test.go \
  backend/internal/backupasset/keyring.go \
  backend/internal/backupasset/keyring_test.go \
  backend/internal/backupasset/lease.go \
  backend/internal/backupasset/service.go \
  backend/internal/backupasset/service_test.go \
  backend/internal/backupasset/content/attempt_broker.go \
  backend/internal/backupasset/content/attempt_broker_test.go \
  backend/internal/backupasset/repository/content_read.go \
  backend/internal/backupasset/repository/testutil_test.go \
  backend/internal/backupasset/repository/private_runtime_root.go \
  backend/internal/backupasset/repository/private_runtime_root_test.go \
  backend/internal/backupasset/processing/contracts.go \
  backend/internal/backupasset/processing/contracts_test.go \
  backend/internal/backupasset/processing/work_key.go \
  backend/internal/backupasset/processing/work_key_test.go \
  backend/internal/backupasset/processing/state.go \
  backend/internal/backupasset/processing/state_test.go \
  backend/internal/backupasset/processing/coordinator.go \
  backend/internal/backupasset/processing/coordinator_test.go \
  backend/internal/backupasset/processing/scheduler.go \
  backend/internal/backupasset/processing/scheduler_test.go \
  backend/internal/backupasset/processing/grants.go \
  backend/internal/backupasset/processing/grants_test.go \
  backend/internal/backupasset/processing/protocol.go \
  backend/internal/backupasset/processing/protocol_test.go \
  backend/internal/backupasset/processing/worker_client.go \
  backend/internal/backupasset/processing/worker_client_test.go \
  backend/internal/backupasset/processing/transport.go \
  backend/internal/backupasset/processing/transport_test.go \
  backend/internal/backupasset/processing/transport_local_linux.go \
  backend/internal/backupasset/processing/transport_local_linux_test.go \
  backend/internal/backupasset/processing/transport_local_other.go \
  backend/internal/backupasset/processing/derived_crypto.go \
  backend/internal/backupasset/processing/derived_crypto_test.go \
  backend/internal/backupasset/processing/derived_store.go \
  backend/internal/backupasset/processing/derived_store_test.go \
  backend/internal/backupasset/processing/derived_lifecycle.go \
  backend/internal/backupasset/processing/derived_lifecycle_test.go \
  backend/internal/backupasset/processing/derived_manifest.go \
  backend/internal/backupasset/processing/derived_manifest_test.go \
  backend/internal/backupasset/processing/reconciler.go \
  backend/internal/backupasset/processing/reconciler_test.go \
  backend/internal/backupasset/processing/metrics.go \
  backend/internal/backupasset/processing/metrics_test.go \
  backend/internal/backupasset/processing/behavior_integration_test.go \
  backend/internal/backupasset/processing/testutil_test.go \
  backend/internal/backupasset/runtime/runtime.go \
  backend/internal/backupasset/runtime/runtime_test.go \
  backend/internal/backupasset/runtime/admission_controller_test.go \
  backend/internal/backupasset/runtime/processing_runtime.go \
  backend/internal/backupasset/runtime/processing_runtime_test.go \
  backend/internal/settings/service.go \
  backend/internal/settings/service_test.go \
  backend/internal/api/router.go \
  backend/internal/api/router_test.go \
  backend/internal/api/backup_asset_rbac_test.go \
  backend/internal/api/worker_router.go \
  backend/internal/api/worker_router_test.go \
  backend/internal/api/handlers/backup_worker_handler.go \
  backend/internal/api/handlers/backup_worker_handler_test.go \
  backend/internal/api/docs/docs.go \
  backend/cmd/server/main.go \
  backend/cmd/server/main_test.go \
  backend/cmd/asset-worker/main.go \
  backend/cmd/asset-worker/main_test.go
```

Before commit, compare `git diff --cached --name-only | sort` exactly to this
manifest and inspect `git diff --cached --check`. Do not run this staging command
in Phase 1. Commit/push/PR remain separate later delivery actions and must occur
only after explicit implementation authorization and accepted evidence.

## 16. Delivery and gate status ledger

The implementation evidence file must update this ledger from actual outputs;
planning prose never turns a future gate green.

| Gate/action | Current status |
|---|---|
| baseline fetch/branch/task create | executed in Phase 1 |
| current-main/parent/Children 7-9 research | executed in Phase 1 |
| focused `prd.md`/`design.md`/`implement.md` | approved by user 2026-07-19 |
| focused planning validation | executed; Trellis/diff/template-token/approval/scope checks pass |
| planning approval | executed 2026-07-19 |
| separate implementation authorization | approved by user 2026-07-19 |
| focused exact-manifest amendment | approved by program control 2026-07-19 |
| workflow transition to Phase 2 | pending; current state requires planning |
| `task.py start` | `not_executed` |
| product/model/migration/test implementation | `not_executed` |
| SQLite 067 apply/down and behavior | `not_executed` |
| real PostgreSQL 067 apply/down and behavior | `not_executed` |
| race/lease/fence/late/cancel/quarantine tests | `not_executed` |
| crypto/tamper/refcount/revoke-order tests | `not_executed` |
| local UDS/mTLS/fake Worker integration | `not_executed` |
| Swagger/backend/full project gates | `not_executed` |
| exact stage/commit | `not_executed` |
| push/single PR/required CI monitoring | `not_executed` |
| merge/post-merge monitoring | `not_executed` |
| Release Please PR #386 | `not_executed`; forbidden/out of scope |
| release/deploy/Worker image/Compose/UI | `not_executed`; out of scope |

After all implementation gates genuinely pass, request review before delivery,
then follow the repository's single-PR CI-monitoring workflow. Do not merge
Release Please PR #386 and do not infer a release/deploy mutation from this
child.
