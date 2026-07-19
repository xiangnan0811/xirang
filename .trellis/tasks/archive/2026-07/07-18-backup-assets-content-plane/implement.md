# Backup Asset Content Plane Implementation Plan

> **Execution mode:** inline only. Do not create implement/check sub-agents.
> Load `trellis-before-dev`, `superpowers:test-driven-development`,
> `superpowers:executing-plans` and, before completion claims,
> `superpowers:verification-before-completion` only after the focused planning
> package is explicitly approved and implementation is separately authorized.

**Goal:** Add a backup-asset-only, cookie-scoped Content Broker with exact
session/resource authorization, RecoveryPoint leases, portable Range/accounting,
per-process authenticated cache, fail-closed core renderers, safe audit/logging,
Nginx streaming policy and a frontend ticket API boundary.

**Architecture:** Secured JSON issuance creates a hash-only server grant and
path-scoped HttpOnly cookie. A separate cookie-only gateway reauthorizes every
request/heartbeat, atomically reserves budgets, and reads an exact composite
AssetRef through `repository.Service`. Provider Range is used only when proven;
otherwise bounded sequential or fully authenticated cache materialization is
used. RecoveryResult, Worker/Derived Store and workspace UI remain absent.

**Tech stack:** Go 1.26, GORM, Gin 1.11, SQLite, PostgreSQL 18, AES-256-GCM,
HTTP Range/ResponseController, Nginx 1.29, React 18/TypeScript 5.8 API modules,
Vitest, Swagger, Docker, Trellis.

Task 1 and Task 2 have recorded GREEN evidence. Task 3 has intentional RED
source-contract tests plus a partial Repository source adapter/admission
implementation. Focused Amendment A was explicitly approved on 2026-07-18.
Task 3 review then found a superseded lease deadline contract, late admission
and concurrent manifest-external edits. The Amendment B design proposal was
approved on 2026-07-18, followed by explicit approval of this complete written
package. Remaining product implementation may resume under the corrected plan.

## 1. Execution gate

### 1.1 Current execution state

```text
task:      .trellis/tasks/07-18-backup-assets-content-plane
status:    in_progress
branch:    codex/backup-assets-content-plane
PR base:   main
worktree:  /home/murray/code/xirang
baseline:  a3c309a922d9a4f48cb82031031c0975c251f5f4
```

The user explicitly approved `prd.md + design.md + implement.md` on 2026-07-18
and separately authorized `task.py start` plus product implementation. Commands
in Sections 1.2-15 may now run in order, but none is passing without recorded
execution evidence. Amendment A and Amendment B's design/written package are
approved; the corrected Task 3 RED-GREEN work may now continue.

### 1.2 Original approved-start preflight (executed history)

- [x] Fetch and prove the worktree/branch/base were valid before DDL. A fresh
  fetch on 2026-07-18 still proves the same branch/base:

```bash
cd /home/murray/code/xirang
git fetch --prune origin
test "$(git branch --show-current)" = "codex/backup-assets-content-plane"
test "$(git rev-parse origin/main)" = "a3c309a922d9a4f48cb82031031c0975c251f5f4"
test "$(git merge-base HEAD origin/main)" = "a3c309a922d9a4f48cb82031031c0975c251f5f4"
test -z "$(git status --porcelain -- . ':!.trellis/tasks/07-12-backup-data-explorer-design/task.json' ':!.trellis/tasks/07-18-backup-assets-content-plane')"
```

- [x] Prove migration reservation before any DDL edit:

```bash
for engine in sqlite postgres; do
  test ! -e "backend/internal/database/migrations/$engine/000066_backup_asset_content.up.sql"
  test ! -e "backend/internal/database/migrations/$engine/000066_backup_asset_content.down.sql"
  for version in 000067 000068 000069 000070 000071; do
    test -z "$(find "backend/internal/database/migrations/$engine" -maxdepth 1 -name "${version}_*" -print -quit)"
  done
done
```

- [x] Reload relevant Trellis specs/context and re-check the current-main
  symbols listed in `research/current-main-evidence.md`.
- [x] Only after both approvals and all checks, run exactly:

```bash
python3 ./.trellis/scripts/task.py start .trellis/tasks/07-18-backup-assets-content-plane
```

Recorded result: status became `in_progress`. If `origin/main` moves,
`000066` is occupied, a dependency contract changed, or unrelated worktree
changes overlap the manifest, stop and amend/re-review all three planning
artifacts. Do not silently rebase/copy a sibling, self-renumber, or run start.

## 2. Exact file manifest

Any product/spec file outside this manifest requires a focused plan amendment
and approval before editing.

### 2.1 Create

```text
backend/internal/model/backup_asset_content.go
backend/internal/database/migrations/sqlite/000066_backup_asset_content.up.sql
backend/internal/database/migrations/sqlite/000066_backup_asset_content.down.sql
backend/internal/database/migrations/postgres/000066_backup_asset_content.up.sql
backend/internal/database/migrations/postgres/000066_backup_asset_content.down.sql

backend/internal/backupasset/content/contracts.go
backend/internal/backupasset/content/contracts_test.go
backend/internal/backupasset/content/ticket.go
backend/internal/backupasset/content/ticket_test.go
backend/internal/backupasset/content/source_contracts.go
backend/internal/backupasset/content/source_contracts_test.go
backend/internal/backupasset/content/lease.go
backend/internal/backupasset/content/lease_test.go
backend/internal/backupasset/content/broker.go
backend/internal/backupasset/content/broker_test.go
backend/internal/backupasset/content/range.go
backend/internal/backupasset/content/range_test.go
backend/internal/backupasset/content/budget.go
backend/internal/backupasset/content/budget_test.go
backend/internal/backupasset/content/cache.go
backend/internal/backupasset/content/cache_test.go
backend/internal/backupasset/content/classifier.go
backend/internal/backupasset/content/classifier_test.go
backend/internal/backupasset/content/renderer.go
backend/internal/backupasset/content/renderer_test.go
backend/internal/backupasset/content/audit.go
backend/internal/backupasset/content/audit_test.go
backend/internal/backupasset/content/metrics.go
backend/internal/backupasset/content/metrics_test.go
backend/internal/backupasset/content/reconciler.go
backend/internal/backupasset/content/reconciler_test.go
backend/internal/backupasset/content/behavior_integration_test.go

backend/internal/backupasset/repository/content_read.go
backend/internal/backupasset/repository/content_read_test.go

backend/internal/api/handlers/backup_content_handler.go
backend/internal/api/handlers/backup_content_handler_test.go
backend/internal/middleware/content_safe_recovery.go
backend/internal/middleware/content_safe_recovery_test.go
backend/internal/middleware/structured_logger_test.go

web/src/lib/api/backup-content-api.ts
web/src/lib/api/backup-content-api.test.ts

scripts/check-asset-content-nginx.sh
scripts/check-asset-content-nginx.test.sh
```

### 2.2 Modify

```text
.github/workflows/ci.yml

backend/internal/database/backup_asset_migrations_integration_test.go
backend/internal/backupasset/domain.go
backend/internal/backupasset/domain_test.go
backend/internal/backupasset/publication/contracts.go
backend/internal/backupasset/publication/contracts_test.go
backend/internal/backupasset/provider/contracts.go
backend/internal/backupasset/provider/restic.go
backend/internal/backupasset/provider/runner_test.go
backend/internal/backupasset/provider/rclone.go
backend/internal/backupasset/provider/rclone_test.go
backend/internal/backupasset/repository/query.go
backend/internal/backupasset/repository/query_test.go
backend/internal/backupasset/repository/testutil_test.go
backend/internal/backupasset/service.go
backend/internal/backupasset/service_test.go
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go

backend/internal/auth/jwt.go
backend/internal/auth/jwt_test.go
backend/internal/middleware/auth.go
backend/internal/middleware/auth_test.go
backend/internal/middleware/structured_logger.go

backend/internal/api/handlers/auth_handler.go
backend/internal/api/handlers/auth_handler_test.go
backend/internal/api/router.go
backend/internal/api/router_test.go
backend/internal/api/backup_asset_rbac_test.go
backend/internal/api/docs/docs.go
backend/cmd/server/main.go
backend/cmd/server/main_test.go

backend/internal/settings/service.go
backend/internal/settings/service_test.go

deploy/allinone/Dockerfile
deploy/nginx/templates/default.conf.template
deploy/nginx/README.md

web/src/types/domain.ts

.trellis/spec/backend/database-guidelines.md
.trellis/spec/backend/deployment-runtime.md
.trellis/spec/backend/logging-guidelines.md
.trellis/spec/frontend/type-safety.md
```

The four spec files are implementation-phase executable contract updates, not
public feature/release documentation. If `trellis-update-spec` finds no durable
new convention for one file, remove that path from the manifest before staging
instead of creating churn.

### 2.3 Explicitly unchanged

```text
backend/go.mod
backend/go.sum
backend/internal/backupasset/keyring.go
backend/internal/backupasset/audit_action.go
backend/internal/backupasset/authorization.go
backend/internal/backupasset/search/**
backend/internal/backupasset/overlay/**
backend/internal/backupasset/provider/** except the five exact files in Section 2.2
backend/internal/api/handlers/step_up.go
backend/internal/api/handlers/credential_access_grant.go
backend/internal/database/migrations/{sqlite,postgres}/000065_*
backend/internal/database/migrations/{sqlite,postgres}/000067_*
backend/internal/database/migrations/{sqlite,postgres}/000068_*
backend/internal/database/migrations/{sqlite,postgres}/000069_*
backend/internal/database/migrations/{sqlite,postgres}/000070_*
backend/internal/database/migrations/{sqlite,postgres}/000071_*
web/src/pages/**
web/src/components/**
web/src/features/**
web/src/hooks/**
web/src/router.tsx
web/src/router-pages.tsx
web/src/lib/step-up-storage.ts
docs/**
README.md
CHANGELOG.md
docker-compose*.yml
```

No new module dependency is expected: cryptography, HTTP, filesystem and
process-key handling use the Go standard library. Exact step-up actions/audit
fields already exist. Any discovered need to touch an explicitly unchanged
boundary pauses implementation for plan amendment.

### 2.4 Workflow-owned Trellis files

```text
.trellis/tasks/07-12-backup-data-explorer-design/task.json
.trellis/tasks/07-18-backup-assets-content-plane/**
.trellis/tasks/archive/2026-07/07-18-backup-assets-content-plane/**
.trellis/workspace/weibo/index.md
.trellis/workspace/weibo/journal-*.md
```

The parent registration and active Child task are included in the work commit.
`trellis-finish-work` owns the active-to-archive move and its auto-commit. A
concrete, non-template journal entry is a separate final commit; if the journal
rotates, stage only the actual new/current journal and index. Never archive the
parent or backfill Child 7's pre-PR `commit/pr_url` snapshot.

### 2.5 Focused Amendment A manifest gate

Implementation evidence is frozen in
`research/implementation-amendment-a-evidence.md`. The amendment adds two
focused Content source-contract files and eight existing Provider/Repository
files to the exact manifest. `source_contracts_test.go`, `query_test.go` and
`testutil_test.go` already contain Task 3 RED/fixture edits made before the
manifest mismatch was noticed. `source_contracts.go` and the Provider meter are
still absent; no further Amendment A product edit occurred after the gap was
identified.

The exact new boundary is a read-only Provider byte reporter that includes a
bounded reader's hidden overflow probe and is forwarded by the Rclone invariant
and managed Rsync Repository wrappers. It adds no Provider command, mutation,
credential/locator exposure or public DTO. All other Amendment A changes stay
inside files already listed in Sections 2.1-2.2.

The user explicitly approved this focused amendment on 2026-07-18. That opens
the exact expanded manifest, `source_contracts.go` and Task 3 GREEN, but it does
not relax any out-of-scope, validation or delivery gate.

### 2.6 Focused Amendment B lease/admission gate

Evidence is frozen in
`research/implementation-amendment-b-evidence.md`. The Amendment B design was
approved on 2026-07-18, followed by explicit approval of the complete written
package. It adds only the two focused Content lease files shown in Section 2.1.

After written approval, manually reconcile the concurrent research-agent edits
with `apply_patch`:

```text
restore to origin/main behavior:
  backend/internal/backupasset/lease.go
  backend/internal/backupasset/lease_test.go
  backend/internal/backupasset/repository/service.go
  backend/internal/backupasset/repository/rclone_publication_execution_test.go

rename within approved manifest:
  backend/internal/backupasset/content/source.go
    -> backend/internal/backupasset/content/source_contracts.go
```

Do not use checkout/reset or discard any other Child 8 change. The root lease
service retains its zero-deadline independent-holder behavior. The shared
Rclone publication fake is not a Child 8 source test seam; factor an exact
native-reader helper/fake into `content_read.go`/`content_read_test.go` instead.

The written Amendment B package is explicitly approved. A test that passed with
the superseded publication-deadline rule remains `not_accepted`, not GREEN.

## 3. Task 1: Paired `000066` migration and model parity

**Files:** content model, four migration files, database integration test.

- [x] **Step 1: Write red SQLite/PostgreSQL entry points.** Added exact tests:

```go
func TestBackupAssetMigration066SQLite(t *testing.T)
func TestBackupAssetMigration066Postgres(t *testing.T)
```

Both call one engine-neutral contract that starts at real `000065` and covers:

```text
prior Catalog/Search/key/lease row preservation
three Child 8 tables + audit unique index
valid backup_asset grant/request/usage rows
RecoveryResult/dual/unknown/null resource rejection
cross-point/cross-generation Catalog FK rejection
all action/renderer/profile/classification/proof illegal products
secret hash/ID length, TTL ordering, counters and negative/overflow rejection
request Range coupling and scope-kind/scope-id coupling
UTC/model/table/column/index/FK parity
pristine down exactly to 000065
grant+request preservation together, plus independent usage and content-lease guards
raw down SQL guard leaves schema/data/indexes identical; do not claim a failed
golang-migrate step preserves migration metadata
explicit safe-drain fixture permits down only after proven terminal cleanup
000065 used-down still rejects Child 7 data after stepping to 000065
```

- [x] **Step 2: Run the red SQLite test before DDL/models.** The test failed
  for the expected absent-`000066` category before DDL/models were added.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration066SQLite$' -count=1
```

Expected future failure: migration `000066`/content tables are absent. Record
that failure category in `implement.jsonl`; do not count a compile typo or
weaken the test.

- [x] **Step 3: Implement the original exact paired DDL/models from design
  Section 4.** Used
  the existing `000065` composite Catalog parents, no key/lease table rebuild,
  named PostgreSQL constraints, SQLite checks, child-before-parent down order,
  atomic guard and audit partial unique index. Models expose no raw secret.

- [x] **Step 4: Run SQLite and mandatory isolated PostgreSQL parity.** Both
  engines passed the original Task 1 contract against PostgreSQL 18; the
  SQLite test was freshly rerun on 2026-07-18 and passed.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration066SQLite$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database -run '^TestBackupAssetMigration066Postgres$' -count=1
```

Missing PostgreSQL DSN is not a skip/pass. Start an isolated PostgreSQL 18 test
container or stop with the gate pending.

- [x] **Step 5: Amendment A representation RED.** Extended the shared SQLite/
  PostgreSQL contract first for `representation_source_bytes`,
  `representation_size` and `representation_truncated`; prove raw and text/hex
  illegal products fail and the current DDL fails for missing columns.
- [x] **Step 6: Amendment A representation GREEN.** Added paired fields/checks and
  model tags, then rerun both real engines. Down ordering/guard and versions
  remain unchanged.

## 4. Task 2: Closed contracts, ticket secrets and session facts

**Files:** content contracts/ticket; domain; JWT/auth middleware tests/files.

- [x] **Step 1: Write red closed-product tests.** Covered exact one-resource,
  RecoveryResult stable unsupported, renderer/profile/action/range/proof matrix,
  state transitions, UTC/TTL bounds, random internal/public/secret separation,
  hash-only persistence and no JSON exposure.
- [x] **Step 2: Write red session tests.** `AuthMiddleware` exposes only safe
  JTI/user/role/version/expiry; `JWTManager.IsSessionRevoked` honors memory and
  persisted revocations; invalid/empty JTI fails closed; raw JWT never enters a
  grant/model/log.
- [x] **Step 3: Run red focused tests.** Expected failures were missing content
  package/session APIs.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/content ./internal/auth ./internal/middleware \
  -run 'Delivery|Ticket|SessionBinding|SessionRevoked' -count=1
```

- [x] **Step 4: Implement only closed contracts/session seams.** Used CSPRNG,
  SHA-256 secret storage and constant-time comparison. Do not implement HTTP or
  source reads yet.

## 4A. Focused Amendment A: representation and exact byte metering

**Files:** paired `000066`/model/integration test; content source contracts;
exact Provider/Repository files added in Section 2.5.

Frozen minimum signatures/products:

```go
// provider package; optional, ReadHandle itself is unchanged.
type ProviderByteReporter interface { ProviderBytes() int64 }

// content package; Repository always returns this sealed wrapper.
type SourceReader interface {
    io.ReadCloser
    ProviderBytes() int64
}
```

```text
representation_source_bytes: 0 <= value <= source_size
representation_size: value >= 0
representation_truncated: closed false|true
raw renderer: source_bytes = representation_size = source_size, truncated=false
text/hex: truncated = (source_bytes < source_size), output size frozen
request reservation: overflow-safe max(response bytes, Provider bound + possible probe)
```

- [x] **Step 1: Obtain explicit focused approval.** Approved 2026-07-18 and
  recorded across PRD/design/implementation artifacts before continued GREEN.
- [x] **Step 2: Write provider-meter RED tests.** Proved an exact-limit EOF
  reports N Provider bytes, an overflow probe reports N+1 while only N bytes
  are caller-visible, a close-time probe is counted, and Rclone invariant plus
  managed Rsync Repository wrappers preserve the report. Missing/invalid meter
  evidence must produce conservative full-reservation accounting. Prove
  `content_read` admission occurs before any locator/access model hook or
  Provider port, and post-close validation uses a strict cleanup deadline
  instead of unbounded background context.
- [x] **Step 3: Write representation RED tests.** Extended the shared real-engine
  migration fixture for source bytes/representation size/truncation and add
  content contract tests for raw equality, text/hex deterministic size,
  overflow-safe `max(response, provider+probe)` reservation and zero-size
  products.
- [x] **Step 4: Run and record the expected RED commands.** Missing reporter,
  source contracts and representation columns/checks failed for the intended
  categories before their implementations were added.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/provider ./internal/backupasset/repository \
  -run 'ProviderBytes|BoundedRead.*Accounting|ManagedRsync.*Accounting|Rclone.*Accounting' -count=1
go test ./internal/backupasset/content ./internal/database \
  -run 'SourceRequest|Representation|Migration066SQLite' -count=1
```

- [x] **Step 5: Implement the minimum GREEN.** Added an optional internal
  Provider byte reporter, probe-inclusive bounded counter, wrapper forwarding,
  closed Content source contracts, admission-first/bounded-cleanup source
  lifecycle and paired representation fields/checks. Do not change
  `ReadHandle`, add commands or expose the counter in DTO/log/audit.
- [x] **Step 6: Run provider/repository/content plus mandatory SQLite and real
  PostgreSQL parity.** Focused packages, SQLite and PostgreSQL 18 passed with
  representation fields and probe-inclusive accounting. Missing DSN was not
  accepted as a pass.

## 5. Task 3: Exact repository source adapter and leases

**Files:** repository content adapter/test; publication contract/test; Content
lease files/tests; Broker lease integration tests; runtime fakes as needed
within manifest.

Current evidence: `source_contracts_test.go`, Repository source tests and a
partial `repository/content_read.go` followed their original RED. Concurrent
work then made the focused packages pass while acquiring admission too late and
binding Content to a historical publication deadline. That pass is
`not_accepted`. Amendment B returns Task 3 to RED before any corrected GREEN.

- [x] **Step 1: Reconcile the approved manifest before RED.** Restored the four
  manifest-external files listed in Section 2.6 with `apply_patch`, rename
  `source.go` to `source_contracts.go`, and prove `git diff --name-only` contains
  no remaining product path outside Sections 2.1-2.2. Do not change behavior in
  this step.
- [x] **Step 2: Write red source/admission tests** for exact active composite lookup,
  immutable/mutable allowed states, old Catalog/cross-point/entry-only failure,
  Command unsupported, sequential/Range capability, hidden locator/access,
  admission rejection before any Catalog/access query/model-hook decryption or
  Provider port, exactly one transferred token for portable/managed Rsync/native
  Rclone, cancel propagation, Close validation/join, finite cleanup deadline and
  mutable before/after drift.
- [x] **Step 3: Write red lease tests** proving `AcquireContentLease` passes zero
  explicit deadline, accepts the bounded deadline returned by LeaseService,
  keeps the grant expiry separately shorter, heartbeats the exact fence,
  releases idempotently on revoke/expiry, rejects stale fences and permits
  takeover only for cleanup after short-lease expiry. A historical expired
  publication lease must not block a new content lease.
- [x] **Step 4: Run corrected RED tests.** The corrected suite failed for the
  intended missing admission-first, pre-acquired Rsync token, native exact
  helper, lease closed shape and cleanup deadline categories.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/repository ./internal/backupasset/content \
  -run 'ContentSource|ContentRead|ContentAdmission|ContentLease|MutableSource|CleanupDeadline' -count=1
```

- [x] **Step 5: Implement the corrected Content lease and SourceResolver.** Kept
  root LeaseService unchanged. Acquire one `OperationContentRead` token before
  decrypted state, transfer it into every source session, and let managed Rsync
  accept that pre-acquired token without nested admission. Portable locators use
  registry readers; native Rclone calls only the existing exact-version reader.
- [x] **Step 6: Implement bounded cleanup and error precedence.** Close/join the
  reader, revalidate with a cancellation-detached context capped by both the
  captured request deadline and a five-second cleanup ceiling, release owned
  Provider/session resources, then close admission. Run all stages; source/fence
  drift takes safe precedence over a generic limit/close error.
- [x] **Step 7: Prove reader lifecycle and corrected GREEN.** Deterministic
  cancel/close tests, the focused command, focused race command and complete
  backupasset/provider/repository/content package tests passed on 2026-07-18.

## 6. Task 4: Atomic budgets and HTTP Range

**Files:** content range/budget/broker tests/files; dual-engine behavior test.

- [x] **Step 1: Write Range parser/representation red tests.** Freeze full,
  normal/open/suffix, overflow/multipart/duplicate/malformed/out-of-bounds,
  200/206/412/413/416, Content-Range/Length, strong/weak ETag, If-Range,
  Last-Modified, Accept-Ranges, zero-size files, oversized suffix and HEAD zero
  body.
- [x] **Step 2: Write accounting red tests.** Cover grant/request/scope lock
  order, request/per-request/cumulative/in-flight/window bounds, invalid Range
  request charge, idle vs absolute, exact finalize, cancel/write failure,
  duplicate finalize/replay, user/Provider/global request-window maxima,
  probe-inclusive Provider bytes and conservative crash/unknown-meter charge.
- [x] **Step 3: Add same behavior fixture for SQLite/PostgreSQL** using barriers
  that release N goroutines together. Assert exact count/bytes of successful
  reservations and non-negative final counters.
- [x] **Step 4: Run red tests before implementation.** The first focused run
  failed for the intended missing Range representation and Budget service
  symbols before either production file existed.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/content -run 'Range|IfRange|Budget|Reservation' -count=1
```

- [x] **Step 5: Implement transaction/reservation and HTTP helpers.** No source
  opens until reservation commits; no in-memory-only security counter.
- [x] **Step 6: Run SQLite plus mandatory PostgreSQL behavior.** The same
  barrier-driven contract passed on SQLite and the isolated PostgreSQL 18
  database. Twelve simultaneous 10-byte reservations under a 50-byte/five-
  request ceiling admitted exactly five on each engine; finalize/replay/cancel/
  crash accounting stayed non-negative. Focused, package and race runs also
  passed on 2026-07-18.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/content -run '^TestContentBehaviorSQLite$' -count=1
REQUIRE_POSTGRES_CONTENT_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/content -run '^TestContentBehaviorPostgres$' -count=1
```

## 7. Task 5: Per-process authenticated cache

**Files:** content cache/metrics tests/files; repository root-check adapter.

- [x] **Step 1: Write red crypto tests** for random key/nonces, canonical AAD,
  tamper, swapped/wrong resource/generation/chunk/key, truncation, duplicate
  nonce detection, opaque filenames and zero plaintext leakage.
- [x] **Step 2: Write red filesystem tests** using temporary trees, symlink/
  parent traversal/special files/source overlap/forbidden roots, simulated bind
  ambiguity, `os.Root` containment, partial publish, ENOSPC and delete failure.
- [x] **Step 3: Write quota/lifecycle tests** for memory and disk object/user/
  Provider/global bytes/files, active cache leases, idle/absolute TTL, startup
  orphan/key-loss deletion, bounded periodic reconciliation and shutdown.
  Prove different users cannot share one cache object/AAD while the same user
  can reuse only an exact resource/source/representation generation.
- [x] **Step 4: Run red tests.** The first crypto run failed for missing cipher/
  binding symbols; the first cache-service run failed for missing object/config/
  service symbols. A focused ENOSPC-after-partial-write test subsequently
  failed by finding the residual `.partial` ciphertext before cleanup was fixed.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/content -run 'Cache|AEAD|Materialization|CacheRoot' -count=1
```

- [x] **Step 5: Implement memory + AES-GCM chunk cache.** Use independent
  process keys, `os.Root`, opaque partial/committed chunks and no plaintext/
  `secure.EncryptString` fallback.
- [x] **Step 6: Prove sequential Provider only becomes Range-capable after full
  post-validated materialization; cancel/partial never publishes.**

  Focused cache, complete content-package and cache race suites passed on
  2026-07-18. Tests cover owner-partitioned identity, active lease eviction,
  idle/absolute expiry, startup/periodic orphan deletion, shutdown key zero,
  symlink/special/source/mount rejection, tamper/wrong key/AAD/chunk/generation,
  quota-before-source admission and ENOSPC cleanup with no plaintext fallback.

## 8. Task 6: Classification and core renderers

**Files:** classifier/renderer/contracts tests/files.

- [x] **Step 1: Write classification red table tests** for path/name rules,
  private keys/credentials/config, MIME/extension conflicts, BOM/encoding,
  bounded/truncated/error scans, Child 7 classification elevation and unknown
  as secret.
- [x] **Step 2: Write renderer red matrix tests** for text/control escaping and
  truncation, hex, PNG/JPEG/GIF/WebP dimensions/pixel bombs, PDF/audio/video
  magic, HTML/XML/SVG never active-inline, download attachment and every
  illegal coupled product.
- [x] **Step 3: Write header/file-name injection tests** for CRLF/NUL/path/bidi,
  nosniff/CSP/sandbox/frame/object/referrer/CORP/content disposition.
- [x] **Step 4: Run red tests.** The first run failed for the intended missing
  classifier/renderer symbols before either production policy existed.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/content -run 'Classification|Renderer|MIME|Disposition|ActiveContent' -count=1
```

- [x] **Step 5: Implement bounded in-process policy only.** No external parser,
  Worker, Derived Store, Search ingest, excerpt, OCR or content posting.

  Focused classification/renderer, race and complete content-package tests
  passed on 2026-07-18. Exact Search evidence can only elevate risk; incomplete,
  invalid, active or MIME-confused content remains `unknown`, and HTML/XML/SVG
  never receives an active inline media type.

## 9. Task 7: Broker audit and reconciliation

**Files:** Broker/ticket/audit/reconciler/metrics tests/files.

- [x] **Step 1: Write issuance flow tests** proving authorization/ownership and
  scan precede active grant/cookie, audit precedes cookie, ticket audit failure
  revokes/releases, and public delivery ID is absent from audit/log fields.
- [x] **Step 2: Write aggregate read audit tests** for preview/download success,
  blocked/failure, Range count/bytes, internal grant idempotency, retry/backoff,
  crash recovery and backlog-full refusal of new tickets. When the existing
  writer returns an error after a unique conflict, only an exact persisted
  event projection is accepted as idempotent success; mismatched collisions
  fail closed.
- [x] **Step 3: Write startup/shutdown reconciliation tests** for previous
  process grants, stale reservations, conservative charges, pending audit,
  stale fences, cache orphan and errors accumulated while all cleanup stages
  still run.
- [x] **Step 4: Run red then implement Broker/reconciler/metrics.**

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/content -run 'Broker|Audit|Reconcile|Startup|Shutdown' -count=1
```

Audit failure must never leak raw error/content fields. Already authorized
streaming bytes are not aborted solely because the final aggregate writer is
temporarily unavailable.

Task 7 RED was observed for the missing Reconciler/Broker shutdown symbols and
again for the missing metrics API. GREEN on 2026-07-18:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/content -run \
  'Broker|Audit|Reconcile|Startup|Shutdown|ContentMetrics' -count=1
go test ./internal/backupasset/content -count=1
go test -race ./internal/backupasset/content -run \
  'Broker|Audit|Reconcile|Startup|Shutdown|ContentMetrics' -count=1
```

Startup now conservatively finalizes every scanned reserved/streaming request
before revoking prior-process grants, taking over cleanup-only fences and
flushing pending aggregate audit. Errors are joined across stages. Shutdown
closes issuance admission, joins in-progress issuance, then revokes and
releases every retained grant/lease. Prometheus labels are closed action,
outcome, Provider, byte, reason and cache enums; all unknown inputs map to
`unknown` and no identity/path/MIME value is accepted as a label.

## 10. Task 8: Auth/logout, handlers, router and app logs

**Files:** content handler/tests; auth handler/tests; middleware/logger tests;
router/RBAC/docs files.

- [x] **Step 1: Write red handler/router tests.** Cover strict issuance JSON,
  safe SessionBinding, Viewer pre-ticket 403, action permission/ownership,
  optional secret vs required download proof, exact cross-purpose matrix,
  Set-Cookie attributes, duplicate cookie, content GET/HEAD outside Auth,
  Authorization/query rejection and feature-disabled construction.
- [x] **Step 2: Write red browser-security tests.** Same-origin/none Fetch
  Metadata, block same-site/cross-site, exact Origin, no content CORS, CORP,
  global OPTIONS cannot short-circuit, trailing slash/unsupported methods cannot
  redirect around the content chain, generic empty errors, content-local panic
  recovery with no URI/header/cookie dump, hard ticket timeout below 30s with
  no large issuance materialization, and bounded ResponseController deadlines.
- [x] **Step 3: Write red logout tests.** JWT revocation succeeds first, only JTI
  reaches the revoker, active read cancellation occurs, and revoker failure
  cannot reauthorize the session or expose raw IDs.
- [x] **Step 4: Write red StructuredLogger tests.** Exact content path becomes a
  constant route; request URI/query/delivery/cookie are absent, while ordinary
  paths remain unchanged.
- [x] **Step 5: Run red tests.**

```bash
cd /home/murray/code/xirang/backend
go test ./internal/api/handlers ./internal/api ./internal/middleware ./internal/auth \
  -run 'BackupContent|ContentRoute|Logout.*Content|StructuredLogger' -count=1
```

- [x] **Step 6: Implement thin handlers/router/session revoker/log redaction.**
  Reuse `validateStepUpProof`; do not modify step-up action or credential-grant
  registries.

Task 8 RED was observed for the absent handler/config/service, content-safe
recovery, route registrations, logout revoker and StructuredLogger redaction;
duplicate JSON and local-time JWT revocation reload regressions also failed
before their focused fixes. GREEN on 2026-07-19 local time:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/api/handlers ./internal/api ./internal/middleware ./internal/auth \
  -run 'BackupContent|ContentRoute|Logout.*Content|StructuredLogger|ContentSafeRecovery' -count=1
go test ./internal/api/handlers ./internal/api ./internal/middleware ./internal/auth -count=1
go test -race ./internal/backupasset/content \
  -run 'Broker|Range|Cancel|Shutdown|Reconcile' -count=1
go test ./internal/auth -run '^TestJWTManagerRevokeTokenPersistsAndReloads$' -count=10
```

The secured issuance route now rejects duplicate/unknown/trailing JSON and
query input, adapts immutable SessionBinding plus exact optional/required
step-up proofs, enforces a <=25s ticket context, and writes exactly one checked
cookie. The cookie-only GET/HEAD route is outside Authorization middleware,
rejects Authorization/query/cross-site/wrong-Origin input, bypasses global CORS
preflight, and explicitly catches unsupported methods/trailing slashes with
content-safe headers/recovery. Broker streaming reserves before source open,
revalidates on writes, applies ResponseController deadlines and finalizes/audits
after reader close. Logout revokes JWT first, then cancels by JTI; revoker
failure cannot reauthorize or leak identifiers. JWT revocation persistence now
uses UTC DB predicates and synchronous post-write expiry cleanup, eliminating
the SQLite local-time miscomparison and async table-lock race found by the full
package run.

## 11. Task 9: Settings, runtime and server composition

**Files:** settings/foundation/runtime/main tests/files.

- [x] **Step 1: Write red settings tests** for every design Section 17 key,
  defaults/bounds/env precedence/atomic snapshot and all cross-field invalid
  combinations, including ticket timeout, scope request maxima, memory user/
  Provider and cache object/user/Provider file limits. Assert
  `backup_assets.enabled=false` remains unchanged.
- [x] **Step 2: Write red runtime tests** for one Broker/cache/session validator,
  construction dependency failures, content readiness ordering, startup pass,
  Run reconciler, feature transition admission/drain, schema-down safe drain,
  StopAccepting and shutdown cleanup order.
- [x] **Step 3: Write server composition test/evidence** that one JWT manager is
  constructed before runtime and reused by AuthService/router; global HTTP
  timeouts remain exactly 10s/30s/30s/60s.
- [x] **Step 4: Run red then implement.**

```bash
cd /home/murray/code/xirang/backend
go test ./internal/settings ./internal/backupasset ./internal/backupasset/runtime \
  -run 'ContentConfig|ContentRuntime|ContentTransition|ContentSchemaDown' -count=1
go test ./cmd/server -count=1
```

If `cmd/server` has no test package, use a focused source/config assertion plus
full backend build; do not create a brittle text-only security substitute for
runtime tests.

Task 9 RED was observed for missing Content definitions/snapshot/config,
runtime composition/readiness/lifecycle symbols and shared JWT construction.
GREEN on 2026-07-19 local time:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/settings ./internal/backupasset \
  ./internal/backupasset/repository ./internal/backupasset/runtime ./cmd/server -count=1
go build ./cmd/server
```

The settings registry now returns one complete atomic Content snapshot while
legacy core-only typed readers do not invent absent Content overrides during
validation. Runtime owns exactly one Content session validator, exact Catalog
authorizer, budget/audit/Broker/reconciler/cache lifecycle and composes content
readiness with feature transitions, safe drain and schema down. Server startup
constructs one JWT manager before Runtime and reuses it for AuthService and the
router; the existing HTTP timeout constants remain unchanged.

## 12. Task 10: Nginx and image cache root

**Files:** Nginx template/README, Dockerfile, checker/self-test, CI.

- [x] **Step 1: Write checker and red self-tests first.** Mutated fixtures must
  fail independently for raw URI/cookie log variables, missing exact route,
  buffering/cache/temp/gzip enabled, infinite/missing timeout, changed 10761 or
  missing generic API route, inherited/unredacted content error logging, Host
  port loss, or non-closed effective forwarded proto.
- [x] **Step 2: Run checker against current template before edit.**

```bash
cd /home/murray/code/xirang
bash scripts/check-asset-content-nginx.test.sh
bash scripts/check-asset-content-nginx.sh
```

Expected future second-command failure: the content-specific location/format
does not exist. The self-test may be green because it tests the checker itself.

- [x] **Step 3: Add exact content policy and dedicated cache directory.** Keep
  generic API, 10761, external TLS assumptions and image source unchanged.
- [x] **Step 4: Run rendered config and image build.**

```bash
cd /home/murray/code/xirang
bash scripts/check-asset-content-nginx.test.sh
bash scripts/check-asset-content-nginx.sh
docker build -f deploy/allinone/Dockerfile -t xirang:child8-local .
```

- [x] **Step 5: Extend CI** with migration 066, required PostgreSQL content
  behavior and Nginx checker/self-test before Docker build.

Task 10 RED was observed first because the checker script was absent, then the
checker correctly rejected the current template for its missing exact content
route. GREEN on 2026-07-19 local time:

```bash
cd /home/murray/code/xirang
bash scripts/check-asset-content-nginx.test.sh
bash scripts/check-asset-content-nginx.sh
docker build --network=host -f deploy/allinone/Dockerfile -t xirang:child8-local .
```

The first ordinary local `docker build` attempt reached the frontend build but
the host daemon could not create a bridge veth (`operation not supported`). An
otherwise identical host-network build completed all 44 stages and tagged
`xirang:child8-local`; standard-network CI remains a required future gate. The
checker uses the official nginx:1.29-alpine envsubst entrypoint plus `nginx -T`
and independently rejects unsafe log, route, buffering/cache/temp/gzip,
timeout, port, generic API, error-log, Host, proto-map, header and Range
mutations.

## 13. Task 11: Frontend mapper and Swagger boundary

**Files:** frontend domain/API/test; handler annotations/generated docs.

- [x] **Step 1: Write red frontend tests** for snake-to-camel success, exact
  request encoding, step-up forwarding, closed enums/schema/times/sizes,
  action/renderer/profile/range/classification/expiry invalid products,
  same-origin query-free opaque URL and no JWT/ticket/query/path storage.
- [x] **Step 2: Run red tests.**

```bash
cd /home/murray/code/xirang/web
env -u NODE_ENV npm run test -- backup-content-api.test.ts
```

Expected future failure: module/types are absent.

- [x] **Step 3: Implement only domain/raw mapper/API factory.** Do not change
  client aggregation, UI/page/router/component/hook/storage unless the approved
  manifest is amended.
- [x] **Step 4: Regenerate Swagger and inspect generated diff.**

```bash
cd /home/murray/code/xirang
make swag-init
git diff -- backend/internal/api/docs/docs.go
```

- [x] **Step 5: Run frontend full gate without inherited NODE_ENV.**

```bash
cd /home/murray/code/xirang/web
env -u NODE_ENV npm run check
```

Task 11 RED was observed for the absent `backup-content-api` module. GREEN on
2026-07-19 local time:

```bash
cd /home/murray/code/xirang/web
env -u NODE_ENV npm run test -- backup-content-api.test.ts
env -u NODE_ENV npm run check
cd /home/murray/code/xirang
PATH=/tmp/xirang-child8-tools:$PATH make swag-init
cd backend
go test ./internal/api/handlers ./internal/backupasset/content ./internal/backupasset \
  -run 'BackupContent|Delivery|Capability' -count=1
```

The mapper preserves one opaque, same-origin, query-free content URL and blocks
the whole ticket for malformed schema/URL/time/size/ETag, closed-enum,
renderer/profile/range/MIME, proof/classification/action, expiry or capability/
fallback contradictions. It forwards proof only through the central request
wrapper and contains no content fetch, Blob, storage, router or delivery-ID
extraction. Pinned `swag` v1.16.6 generated deterministic GET+HEAD binary route
and secured ticket POST documentation; ignored JSON/YAML outputs remain
unstaged.

## 14. Task 12: Security/race/full verification

- [x] **Focused backend:**

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/content ./internal/backupasset/repository \
  ./internal/backupasset/runtime ./internal/auth ./internal/middleware \
  ./internal/api/handlers ./internal/api -count=1
```

- [x] **Race/cancel/cache/budget:**

```bash
cd /home/murray/code/xirang/backend
go test -race ./internal/backupasset/content ./internal/backupasset/repository \
  -run 'Concurrent|Budget|Range|Cache|Cancel|Shutdown|Reconcile' -count=1
```

- [x] **Dual-engine mandatory gates:**

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration066SQLite$' -count=1
go test ./internal/backupasset/content -run '^TestContentBehaviorSQLite$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database -run '^TestBackupAssetMigration066Postgres$' -count=1
REQUIRE_POSTGRES_CONTENT_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/content -run '^TestContentBehaviorPostgres$' -count=1
```

- [x] **Nginx/frontend/Swagger/Docker:**

```bash
cd /home/murray/code/xirang
bash scripts/check-asset-content-nginx.test.sh
bash scripts/check-asset-content-nginx.sh
make swag-init
cd web && env -u NODE_ENV npm run check
cd ..
docker build --network=host -f deploy/allinone/Dockerfile -t xirang:child8-local .
```

- [x] **Full project and migration safety:**

```bash
cd /home/murray/code/xirang
make check
bash scripts/check-migration-utc-safety.sh
bash scripts/check-migration-utc-safety.test.sh
git diff --check
```

- [x] **Leak/scope review:** inspect rather than rely on one regex. At minimum:

```bash
cd /home/murray/code/xirang
rg -n 'delivery_id|cookie_secret|session_jti|provider_locator|Authorization|request_uri|\$uri|\$args' \
  backend/internal/backupasset/content backend/internal/api/handlers/backup_content_handler.go \
  backend/internal/middleware/structured_logger.go deploy/nginx scripts web/src/lib/api/backup-content-api.ts
rg -n 'ContentIndexIngest|PublishContentProjection|OpenRange|OpenSequential|CommandInvocation|SSH|runner' \
  backend/internal/backupasset/content backend/internal/api/handlers/backup_content_handler.go
git diff --name-only origin/main...HEAD
```

Expected review: secret/session/public IDs appear only in private validation/
persistence code and tests, not log/audit/DTO fields; Broker has only its
SourceResolver port; handler has no Provider/runner/SSH/process logic; no
Search ingest/Worker/Derived/UI/000067+ change exists.

- [x] Load `trellis-check` and `superpowers:verification-before-completion` and
  record exact command outputs/failures. No unchecked/skipped/missing-DSN gate
  may be reported as pass.

Fresh Task 12 evidence on 2026-07-19 local time:

- complete Content, seven focused backend packages, and the focused
  `-race` cancel/cache/budget suite exited 0;
- SQLite migration/behavior and real PostgreSQL 18 migration/behavior all
  exited 0, with the two destructive PostgreSQL packages run serially;
- Nginx checker/self-test, pinned `swag` v1.16.6 generation,
  `env -u NODE_ENV npm run check` (137 files / 638 tests), npm audit (0
  vulnerabilities), bundle budget, and the 44-stage host-network Docker build
  exited 0;
- `make check` exited 0 with golangci-lint reporting 0 issues, all Go packages,
  frontend tests, and both builds; migration UTC scanned 64 files and its
  mutation self-test passed; doc freshness exited 0 with two reviewed
  non-blocking heuristic reminders;
- manual scope/leak review found no Provider/Repository/SSH/runner/process
  dependency in Content or the thin handler, no public-ID/secret log or DTO
  field, no Search/Overlay/Worker/Derived/UI change, and no `000067-000071`
  file. The Task 9 server composition test was already required by the approved
  plan; Section 2 now names `backend/cmd/server/main_test.go` explicitly.

Final high-risk review also produced seven focused TDD regressions. Each test
failed for the intended old behavior before the minimum fix and passed after:

```text
TestBrokerIssueFailureBoundsDetachedLeaseRelease
TestGatewayReadStateSamplesProviderBytesAfterSourceCloseProbe
TestCacheRootRejectsReplacementBetweenValidationAndOpen
TestGatewayHeartbeatUsesPersistedIdleExpiry
TestBrokerCanceledReadBoundsDetachedFinalizationAndAudit
TestContentLeaseDetachedCleanupHasFiniteDeadline
TestContentLeaseInvalidCleanupReturnsReleaseFailure
```

The fixes bound provisional lease release after ticket failure, sample Provider
bytes after source close, compare validated/opened/current cache-root identity
before any cleanup, honor the persisted refreshed idle deadline on every
heartbeat, and give revoke/finalize/read-audit one shared five-second detached
cleanup budget. Invalid acquired/taken-over lease rollback and the no-argument
lease `Close` now also detach cancellation only under a five-second deadline;
rollback release failures remain joined to the invalid-lease error for safe
diagnosis and later reconciliation.

## 15. Task 13: Exact staging and delivery workflow

### 15.1 Work commit

- [x] Confirm only reviewed files changed:

```bash
cd /home/murray/code/xirang
git status --short
git diff --name-only
git diff --check
```

- [x] Stage files by exact path from Section 2. Never use directory-wide
  `git add backend`, `git add web/src`, wildcard migrations or `git add -A`.
- [x] Include the active Child task directory and parent `task.json`; exclude
  journals/archive until their owners run.
- [x] Inspect staged manifest and staged secret/log-sensitive diff:

```bash
git diff --cached --name-only
git diff --cached --check
git diff --cached --stat
git diff --cached -- backend/internal/backupasset/content \
  backend/internal/backupasset/repository/content_read.go \
  backend/internal/api/handlers/backup_content_handler.go \
  deploy/nginx/templates/default.conf.template
```

- [x] Create one coherent work commit only after every gate is green:

```bash
git commit -m "feat: add secure backup asset content plane"
```

### 15.2 Trellis finish/archive and journal

- [ ] Run `trellis-finish-work`. It must verify the quality gate, mark/archive
  only Child 8 and create its archive auto-commit. Parent remains planning.
- [ ] Verify archive path exactly
  `.trellis/tasks/archive/2026-07/07-18-backup-assets-content-plane` and active
  task removed; do not amend the workflow-owned archive commit.
- [ ] Write a concrete journal entry with baseline, decisions, tests, commit and
  pending PR delivery state. Remove every template placeholder, stage only the
  actual journal/index files, and commit separately.

### 15.3 Push, one PR and CI

- [ ] Re-run `git status --short`, inspect the three commits, then push the one
  dedicated branch:

```bash
git push -u origin codex/backup-assets-content-plane
```

- [ ] Open one ready PR to `main` with a conventional title, exact scope,
  migration/rollback/security evidence and validation commands. Do not include
  sibling work or merge Release Please PR #386.
- [ ] Monitor every required CI job. Fix failures on this same branch, rerun
  affected local gates, push and continue monitoring until all required checks
  are green. Pending/missing/skipped required PostgreSQL/Nginx/security jobs are
  not green.
- [ ] Request/perform the required focused high-risk review for ordered
  migration, cookie/content route, composite identity/lease, Range budgets,
  cache crypto/root, classification/renderer and log redaction.
- [ ] Squash merge only after approval and all required checks pass.

### 15.4 Post-merge and hygiene

- [ ] Monitor post-merge `Release Please` and any triggered release/image/docs
  workflows. This feature merge may update the open v0.46.0 release candidate;
  it does not authorize merging #386 or publishing a stable release.
- [ ] Unless a stable semver tag is actually created, record explicitly that no
  GitHub Release, Docker Hub image publish or Docker Hub description sync was
  expected. If any required automation fails, investigate/fix through normal
  PR workflow before declaring completion.
- [ ] Fast-forward local `main` to `origin/main`, prove the squash is present,
  remove stale local/remote Child branch when appropriate, and leave main clean
  before the next dependent Child starts.

## 16. Execution ledger

| Item | Current status |
|---|---|
| original planning research / PRD / design / implementation plan | `completed` after Phase 1 validation |
| original focused plan approval | `approved` (2026-07-18) |
| separate implementation/start authorization | `approved` (2026-07-18) |
| `task.py start` | `executed`; task status is `in_progress` |
| original Task 1 paired migration/model | `completed` for the pre-amendment contract; SQLite and real PostgreSQL 18 passed; Amendment A representation fields reopen it |
| original Task 2 closed ticket/session contract | `completed`; focused tests passed before Task 3 RED was added |
| Task 3 SourceResolver/lease | `completed`; corrected RED observed, focused/race/full package GREEN recorded; manifest-external edits restored |
| focused Amendment A planning/approval | `completed`; explicitly approved 2026-07-18; three pre-discovery test edits disclosed and exact expanded manifest opened |
| focused Amendment B design + written package | `approved` 2026-07-18; corrected product implementation gate open |
| concurrent manifest-external edits | `reconciled`; four files listed in Section 2.6 match `origin/main`, source contract filename aligned |
| latest focused rerun | SQLite/PostgreSQL 000066, auth/middleware, corrected provider/repository/content/lease, Task 4 Range/budget dual-engine behavior, focused race and complete content package tests `pass` |
| remaining PostgreSQL/race/frontend/Nginx/Docker/full gates | `completed`; fresh real PostgreSQL 18, focused/race, frontend, Nginx, Swagger, Docker, full and migration-safety gates passed after final TDD fixes |
| focused high-risk inline review | `completed`; provisional/invalid lease cleanup, lease-close bounds/error propagation, close-probe accounting, cache-root open TOCTOU, persisted idle heartbeat and bounded detached gateway cleanup RED-GREEN regressions passed |
| spec updates | `completed`; four approved code-spec files contain executable Content ledger/runtime/logging/frontend boundary contracts |
| work commit | `executed`; `5e5a13fdf527a3917d48a108d28d062b89c17a66` |
| finish-work archive auto-commit | `not_executed` |
| concrete journal commit | `not_executed` |
| push / PR / CI / review / squash merge | `not_executed` |
| post-merge automation | `not_applicable` before merge |
| local main sync / branch hygiene | `pending` after merge |

This plan contains future commands, not evidence that any command passed.
