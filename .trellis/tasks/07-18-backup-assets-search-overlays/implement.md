# Backup Asset Search And User Overlays Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add portable, permission-first backup-asset metadata search and owner-scoped saved-search/favorite/tag/recent overlays without Provider access or content production.

**Architecture:** A new database-only `backupasset/search` projection binds the exact active Child 6 Catalog generation and uses a separate wrapped Search Token key, portable Go normalization, HMAC postings, three-valued secret evaluation, and signed opaque cursors. A separate `backupasset/overlay` service owns encrypted user control-plane state, transactional quotas/idempotency, and broken/tombstone/delete lifecycle; runtime composes both and frontend work stops at strict DTO/domain/API boundaries.

**Tech Stack:** Go 1.26, GORM, Gin, SQLite, PostgreSQL 18, `golang.org/x/text`, HMAC-SHA256, React 18 TypeScript 5.8 API modules, Vitest, Swagger, Trellis.

---

## 1. Execution gate and working contract

The user approved `prd.md`, `design.md`, and this plan on 2026-07-18, then
separately authorized implementation/start. The inline session loaded
`trellis-before-dev`, `superpowers:test-driven-development`, and
`superpowers:executing-plans`, verified the following gate, and ran:

```bash
cd /home/murray/code/xirang
git fetch --prune origin
test "$(git branch --show-current)" = "codex/backup-assets-search-overlays"
test "$(git merge-base HEAD origin/main)" = "8cd6e5184e7dd05f702c3a5762b013c67901a399"
test "$(git rev-parse origin/main)" = "8cd6e5184e7dd05f702c3a5762b013c67901a399"
test ! -e backend/internal/database/migrations/sqlite/000065_backup_asset_search.up.sql
test ! -e backend/internal/database/migrations/postgres/000065_backup_asset_search.up.sql
python3 ./.trellis/scripts/task.py start .trellis/tasks/07-18-backup-assets-search-overlays
```

Expected after the final command: task status `in_progress`. If `origin/main`
advanced, `000065` is occupied, a required current-main contract changed, or
the branch/base checks differ, stop and refresh the three planning artifacts.
Do not rebase onto or copy an unmerged sibling and do not choose another
migration number.

Implementation/check remain inline in this task. No implement/check sub-agent
is created.

## 2. Exact file manifest

Any file outside this manifest requires an explicit plan amendment before it
is edited.

### 2.1 Create

```text
backend/internal/model/backup_asset_search.go
backend/internal/model/backup_asset_overlay.go
backend/internal/database/migrations/sqlite/000065_backup_asset_search.up.sql
backend/internal/database/migrations/sqlite/000065_backup_asset_search.down.sql
backend/internal/database/migrations/postgres/000065_backup_asset_search.up.sql
backend/internal/database/migrations/postgres/000065_backup_asset_search.down.sql

backend/internal/backupasset/search/contracts.go
backend/internal/backupasset/search/contracts_test.go
backend/internal/backupasset/search/normalizer.go
backend/internal/backupasset/search/normalizer_test.go
backend/internal/backupasset/search/token.go
backend/internal/backupasset/search/token_test.go
backend/internal/backupasset/search/ast.go
backend/internal/backupasset/search/ast_test.go
backend/internal/backupasset/search/scope.go
backend/internal/backupasset/search/scope_test.go
backend/internal/backupasset/search/cursor.go
backend/internal/backupasset/search/cursor_test.go
backend/internal/backupasset/search/indexer.go
backend/internal/backupasset/search/indexer_test.go
backend/internal/backupasset/search/service.go
backend/internal/backupasset/search/service_test.go
backend/internal/backupasset/search/ingest.go
backend/internal/backupasset/search/ingest_test.go
backend/internal/backupasset/search/metrics.go
backend/internal/backupasset/search/metrics_test.go
backend/internal/backupasset/search/behavior_integration_test.go

backend/internal/backupasset/overlay/contracts.go
backend/internal/backupasset/overlay/contracts_test.go
backend/internal/backupasset/overlay/service.go
backend/internal/backupasset/overlay/service_test.go
backend/internal/backupasset/overlay/idempotency.go
backend/internal/backupasset/overlay/idempotency_test.go
backend/internal/backupasset/overlay/lifecycle.go
backend/internal/backupasset/overlay/lifecycle_test.go

backend/internal/backupasset/runtime/search_worker.go
backend/internal/backupasset/runtime/search_worker_test.go
backend/internal/api/handlers/backup_asset_search_handler.go
backend/internal/api/handlers/backup_asset_search_handler_test.go
backend/internal/api/handlers/backup_asset_overlay_handler.go
backend/internal/api/handlers/backup_asset_overlay_handler_test.go

web/src/lib/api/backup-assets-boundary.ts
web/src/lib/api/backup-assets-boundary.test.ts
web/src/lib/api/backup-asset-search-api.ts
web/src/lib/api/backup-asset-search-api.test.ts
web/src/lib/api/backup-asset-overlays-api.ts
web/src/lib/api/backup-asset-overlays-api.test.ts
```

### 2.2 Modify

```text
.github/workflows/ci.yml
backend/internal/database/backup_asset_migrations_integration_test.go
backend/internal/backupasset/keyring.go
backend/internal/backupasset/keyring_test.go
backend/internal/backupasset/lease.go
backend/internal/backupasset/lease_test.go
backend/internal/backupasset/audit_action.go
backend/internal/backupasset/audit_action_test.go
backend/internal/backupasset/service.go
backend/internal/backupasset/service_test.go
backend/internal/backupasset/repository/testutil_test.go
backend/internal/settings/service.go
backend/internal/settings/service_test.go
backend/internal/bootstrap/bootstrap.go
backend/internal/bootstrap/bootstrap_test.go
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go
backend/internal/api/handlers/step_up.go
backend/internal/api/handlers/step_up_test.go
backend/internal/api/router.go
backend/internal/api/router_test.go
backend/internal/api/backup_asset_rbac_test.go
backend/internal/api/docs/docs.go
web/src/lib/api/core.ts
web/src/lib/api/client.test.ts
web/src/lib/api/backup-assets-api.ts
web/src/lib/api/backup-assets-api.test.ts
web/src/types/domain.ts
.trellis/spec/backend/database-guidelines.md
.trellis/spec/frontend/type-safety.md
```

Phase 3 plan amendment (2026-07-18): `trellis-update-spec` classifies the new
schema/API/cross-layer boundary as a mandatory code-spec trigger. The two spec
files above capture the already-implemented Child 7 contract; this adds no
product behavior or migration scope.

Trellis task files, the parent `task.json`, and the concrete workspace journal
are changed only by the planning/finish flow described in Task 13.

### 2.3 Explicitly unchanged

```text
backend/go.mod
backend/go.sum
backend/cmd/server/main.go
backend/internal/model/backup_asset_catalog.go
backend/internal/backupasset/catalog/**
backend/internal/backupasset/provider/**
backend/internal/backupasset/repository production implementation files
backend/internal/api/handlers/snapshot_search_handler.go
backend/internal/database/migrations/{sqlite,postgres}/000066_*
backend/internal/database/migrations/{sqlite,postgres}/000067_*
backend/internal/database/migrations/{sqlite,postgres}/000068_*
backend/internal/database/migrations/{sqlite,postgres}/000069_*
backend/internal/database/migrations/{sqlite,postgres}/000070_*
backend/internal/database/migrations/{sqlite,postgres}/000071_*
web/src/pages/**
web/src/components/**
web/src/features/**
web/src/hooks/**
docs/**
CHANGELOG.md
deploy/**
```

`golang.org/x/text v0.38.0` is already a direct dependency, so module files do
not change. Swagger's tracked `docs.go` is regenerated; ignored JSON/YAML are
not staged.

### 2.4 Workflow-owned Trellis files

```text
.trellis/tasks/07-12-backup-data-explorer-design/task.json
.trellis/tasks/07-18-backup-assets-search-overlays/**
.trellis/tasks/archive/2026-07/07-18-backup-assets-search-overlays/**
.trellis/workspace/weibo/index.md
.trellis/workspace/weibo/journal-1.md
```

The first two are included in the work commit. `trellis-finish-work` owns the
active-to-archive move and archive auto-commit. The concrete journal step owns
the workspace journal/index commit; if Trellis rotates at the 2,000-line cap,
the newly selected journal path replaces `journal-1.md` in that commit. No
sibling task or parent archive is allowed.

## 3. Task 1: Paired 000065 schema and model parity

**Files:** migration/model files from section 2.1;
`backend/internal/database/backup_asset_migrations_integration_test.go`.

- [x] **Step 1: Write red migration fixtures.** Add exact test entry points:

```go
func TestBackupAssetMigration065SQLite(t *testing.T)
func TestBackupAssetMigration065Postgres(t *testing.T)
```

Both call one `runBackupAssetMigration065Contract`. The fixture starts at
000064 with representative Catalog/key/lease/publication data and asserts:

```text
legacy apply preservation
12 Child 7 tables + indexes/checks/FKs
search_token and search_index closed-set acceptance
all prior closed values still accepted
invalid enum/negative count/bad FK/duplicate natural key rejected
cross-RecoveryPoint Catalog/Search generation/document FK mismatches rejected
UTC scans and GORM model/table/column parity
pristine down reaches exactly 000064
used down blocked independently by a search key, any search lease,
each projection table family, and each overlay table family
blocked down leaves migration version/schema/data identical
```

The PostgreSQL function uses `newRequiredPostgresMigrationFixture`; missing DSN
under `REQUIRE_POSTGRES_MIGRATION_TEST=1` is fatal.

- [x] **Step 2: Run red tests.** After test declarations but before DDL/models:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration065SQLite$' -count=1
```

Expected: FAIL because migration 000065/tables are absent. Do not weaken the
fixture or replace it with SQL string matching.

- [x] **Step 3: Add paired DDL and models.** Implement exactly the 12 tables and
two closed-set rebuilds in `design.md` section 5. SQLite copies existing key and
lease rows with every 000064 column/index/FK. PostgreSQL changes named CHECK
constraints. Both apply files create equivalent natural/composite uniques,
owner/FK/cleanup indexes, closed checks, and UTC columns.

Down begins with a transaction-local guard over all Search Token/search lease/
projection/overlay rows, then drops new tables in child-before-parent order and
restores the exact 000064 checks. It never deletes data to make the guard pass.

Model hooks encrypt only:

```go
SavedSearch.EncryptedAST
Favorite.EncryptedLabel
TagDefinition.EncryptedName
OverlayIdempotency.EncryptedRequestFingerprint
```

Raw models use `json:"-"` on all encrypted/private/HMAC/sort/fence fields and
are never response DTOs.

- [x] **Step 4: Run SQLite and real PostgreSQL parity.** Use an isolated test
database; never point the fixture at a development database.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration065SQLite$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database -run '^TestBackupAssetMigration065Postgres$' -count=1
```

Expected: PASS on both engines, including apply/down/rejected-down atomicity.

**Rollback point:** Only the pristine down fixture may remove 000065. Once any
guarded row exists, retain additive schema and use a forward fix.

## 4. Task 2: Search Token key, lease holder, and portable normalization

**Files:** `keyring*`, `lease*`, `search/normalizer*`, `search/token*`.

- [x] **Step 1: Write key/normalizer red tests.** Name and cover:

```text
TestSearchTokenDomainIsIndependentAndRandom
TestSearchTokenOrdinaryRotationIsProhibited
TestSearchTokenKEKRewrapPreservesKeyAndVersion
TestSearchTokenLossDoesNotRegenerate
TestSearchTokenReplacementInvalidatesBeforeActivation
TestSearchTokenReplacementRekeysTagsBeforeTagAvailability
TestSearchIndexLeaseTakeoverRejectsOldFence
TestNormalizerV1CanonicalEquivalence
TestNormalizerV1HanBigramsLatinExtensionAndUTCDate
TestNormalizerV1RejectsTraversalControlsAndLimits
TestTokenHMACSeparatesFieldKindNormalizerAndKeyVersion
TestPortableSortKeyUsesASCIIHexByteOrder
TestPathGroupTokenPreservesCanonicalCaseAndLineage
```

Use `testing/quick`/seeded deterministic generators for valid Unicode
equivalence properties and table fixtures for known case-fold edge cases. Do
not rely on database collation.

- [x] **Step 2: Verify red state.** Run:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset ./internal/backupasset/search \
  -run 'SearchToken|SearchIndexLease|NormalizerV1|TokenHMAC|PortableSort' -count=1
```

Expected: FAIL because the domain, lease holder, and package do not exist.

- [x] **Step 3: Implement closed contracts.** Add:

```go
const KeyDomainSearchToken KeyDomain = "search_token"
const LeaseHolderSearchIndex LeaseHolderType = "search_index"
const NormalizerVersion = 1
```

Add Search Token to the valid domain set but not an unconditional boot-time
required list; only enabled Search startup ensures it. Explicitly prohibit
normal `Rotate`, and expose one rebuildable-key transition that accepts a
transaction invalidation callback. The callback invalidates Search generations and tag
lookup before activation; bounded reconciliation re-HMACs encrypted tag names
under the new key, and tag search/mutation stays unavailable until all rows use
that version. Callback failure keeps the prior active key. `MarkLost` for this
domain must use the same coordinated invalidation path. All key bytes come from
`crypto/rand` through existing secure wrapping.

Implement NFKC -> full case fold -> safe slash/segment -> bounded per-field
tokenization with `x/text`; emit domain-separated HMACs and ASCII-hex sort/group
keys. No token/string is logged.

- [x] **Step 4: Run focused tests.** Same command as Step 2. Expected: PASS.

## 5. Task 3: Closed AST, scope resolver, and opaque cursor

**Files:** `search/contracts*`, `search/ast*`, `search/scope*`,
`search/cursor*`.

- [x] **Step 1: Write red contract tests.** Cover every valid discriminator and
pairwise invalid product:

```text
schema 1 only; and/or >=2; not ==1; leaf has no children
term/type/modified_time required and forbidden properties
closed fields/types/sorts; RFC3339 UTC and from<=to
body/depth/node/value/rune/byte/page/candidate/time limits
recursive and/or child + ID/value dedupe/sort; empty/mixed exact rejection
current newest committed/degraded or stable mutable head
all_retained eligibility; exact all-or-nothing; Admin imported rules
shared-repository Operator producing-lineage isolation
more than 2,000 candidates use stable batches through the same ownership port
cursor has no query/path/name/tag/token/sort text
cursor binds user/role/scope/query/key/point/generation/revision/proof/expiry
```

Use frozen clocks and opaque fixtures. Parse the cursor payload in tests and
scan it for every sensitive source string before checking the signature.

- [x] **Step 2: Run red tests.** Expected: compile/behavior FAIL.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/search \
  -run 'AST|Canonical|Scope|Current|AllRetained|ExactPoints|Cursor' -count=1
```

- [x] **Step 3: Implement contracts.** Define closed string types and one
strict `ValidateAndCanonicalize` used by inline and saved queries. Scope uses
`catalog.Ownership.AuthorizedPointIDs` as the ownership source, selects points
before checking Search coverage, and returns a frozen selection revision.

Cursor uses `KeyDomainCursorSigning`, a 15-minute maximum, canonical JSON, and
only the opaque/digest fields in `design.md` section 9. Resume reloads the
anchor's private sort tuple; it never embeds the tuple's path/name components.

- [x] **Step 4: Run focused tests.** Same command as Step 2. Expected: PASS.

## 6. Task 4: Atomic metadata projection and runtime worker

**Files:** `search/indexer*`, `search/metrics*`, `runtime/search_worker*`.

- [x] **Step 1: Write red indexer/worker tests.** Add exact scenarios:

```text
building rows invisible; zero-document complete activation
exact active Catalog generation and source frozen
unknown non-empty security state fails; empty maps unknown
batch counts/HMAC postings/field coverage/sort/group data correct
old active preserved on build failure
old Search rejected after Catalog generation changes
lease takeover/late fence/source drift/key replacement reject activation
abandoned build reconciliation and restart retry
bounded candidates/concurrency/per-repository fairness/dynamic disable
feature false touches no Search key/table and invokes no Provider
shutdown cancels and joins every build
metrics labels remain low cardinality
```

Provider dependencies are intentionally impossible in the indexer constructor.

- [x] **Step 2: Run red tests.** Expected: FAIL for missing indexer/worker.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/search ./internal/backupasset/runtime \
  -run 'Indexer|Projection|Activation|Fence|SearchWorker|SearchMetrics' -count=1
```

- [x] **Step 3: Implement stage/build/activate.** Keyset-read only the frozen
Catalog generation. Acquire/renew/release `search_index`; batch insert staging
documents/postings/fields; validate count/closed sensitivity; activate in one
transaction after point/source/Catalog/key/lease/fence checks. No fallback to a
different Catalog or point is permitted.

Worker mirrors the established Catalog worker's scheduling/cancellation shape
but its backend exposes only DB Search candidate/build/reconcile methods. It
re-reads typed settings each pass and starts no work when disabled.

- [x] **Step 4: Run focused tests and repetition.** Expected: PASS.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/search ./internal/backupasset/runtime \
  -run 'Indexer|Projection|Activation|Fence|SearchWorker|SearchMetrics' -count=3
go test -race ./internal/backupasset/search ./internal/backupasset/runtime \
  -run 'Activation|Fence|SearchWorker' -count=3
```

## 7. Task 5: Search evaluator, coverage, ranking, and parity

**Files:** `search/service*`, `search/behavior_integration_test.go`.

- [x] **Step 1: Write red service and dual-engine behavior tests.** One shared
fixture runs as `TestSearchBehaviorSQLite` and
`TestSearchBehaviorPostgres`. It covers:

```text
Admin/Operator/Viewer and shared-repository ownership-before-candidate
current newest-unindexed no fallback; all-history lineage+path grouping
exact membership drift and saved broken signal
Kleene and/or/not for secret/unknown content
all registered wrong-purpose proofs and proof expiry between pages
authorized metadata OR hidden content exposes only metadata facts
fixed integer score including path proximity, total tie-break order, page concatenation parity
query/index generation, AssetRef, hit fields, capability/permissions
complete exact total/authoritative empty
partial/building/failed/unavailable lower-bound/null total and non-authoritative zero
suggestion/count/snippet/existence/error redaction
stale cursor on every bound revision
candidate/time/page hard-limit errors without truncation
```

PostgreSQL test calls `t.Fatal` when
`REQUIRE_POSTGRES_SEARCH_TEST=1` and `TEST_POSTGRES_DSN` is absent.

- [x] **Step 2: Run SQLite red behavior test.** Expected: FAIL because Service
does not exist.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/search -run '^TestSearchBehaviorSQLite$' -count=1
```

- [x] **Step 3: Implement bounded query pipeline.** Follow the exact pipeline
in `design.md` section 7. SQL only returns a bounded candidate set. Go applies
closed AST/Kleene truth, grouping, the frozen integer ranking formula, total
order, coverage aggregation, count, suggestions, and DTO construction.

`any` is OR over authorized metadata/tag/content fields. Hidden content facts
remain unknown and cannot contribute score/hits/count/suggestion/snippet. A
content posting without an installed excerpt resolver cannot return a match or
complete coverage.

The final candidate plan compiles posting/type/time predicates and unions them
with owner-tag composite refs resolved only inside the already-authorized point
scope. It bounds and deduplicates candidate rows before private hydration;
`any` never forces a whole-projection scan when its available positive branches
are selective. Ranking includes the frozen path-leaf segment proximity term.
Suggestions are deterministic, bounded metadata facts after grouping; content/
OCR and hidden facts never become suggestions. Cursor binding includes the
owner-tag revision digest so assignment/name changes stale pagination.

- [x] **Step 4: Run SQLite and real PostgreSQL parity.** Expected: PASS with
identical normalized fixture results, scores, ordering, grouping, cursors, and
coverage semantics.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/search -run '^TestSearchBehaviorSQLite$' -count=1
REQUIRE_POSTGRES_SEARCH_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/search -run '^TestSearchBehaviorPostgres$' -count=1
```

## 8. Task 6: Safe future content-ingest port

**Files:** `search/ingest*` and relevant Search service fixtures.

- [x] **Step 1: Write red ingest tests.** Cover field/term/ref limits, exact
source, active Catalog/Search generation, Search key version, active
`processing_job` owner/attempt/fence/deadline, sensitivity/classification CAS,
monotonic classification/pipeline/index revisions, atomic replace/revoke, stale worker rollback,
metadata-field rejection, no excerpt resolver, and concurrent calls.

After each test, query raw posting/field/audit rows and assert plaintext terms,
content, OCR, query, and excerpt ciphertext markers are absent.

- [x] **Step 2: Run red tests.** Expected: FAIL for missing port.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/search -run 'ContentIndexIngest|ContentProjection|ProjectionRevoke' -count=1
```

- [x] **Step 3: Implement the transaction.** Export the exact interface/input
from `design.md` section 10. Resolve key inside Core, HMAC bounded terms in
memory, and transactionally replace only the requested content/OCR field while
CASing the closed sensitivity/revision and advancing projection revision.
When classification changes, delete both content/OCR posting families, move
the sibling field to the new classification revision as unavailable, and clear
its excerpt ref before allowing publication at that revision.
Revoke deletes postings/ref and marks coverage unavailable before returning.
Do not add a producer, Worker call, Derived FK, excerpt read, or ciphertext
storage.

- [x] **Step 4: Run focused/race tests.** Expected: PASS.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/search -run 'ContentIndexIngest|ContentProjection|ProjectionRevoke' -count=3
go test -race ./internal/backupasset/search -run 'ContentIndexIngest|ProjectionRevoke' -count=3
```

## 9. Task 7: Owner overlays, quota, idempotency, and lifecycle

**Files:** all `backupasset/overlay` files.

- [x] **Step 1: Write red overlay contract/service tests.** Cover:

```text
owner-only missing/other-owner indistinguishable
active AssetRef requires current Catalog ownership
saved AST uses the Search validator and encrypts at rest
saved exact source broken; future schema blocked; no scope widening
favorite/tag natural duplicate add and absent remove idempotency
tag normalized owner-local uniqueness and cross-owner isolation
Search Token replacement gates tag search/mutation until all definitions rekey
bulk validate/dedupe/new-count then whole rollback
PostgreSQL row-lock and SQLite immediate quota races
same Idempotency-Key same request replay; different request conflict
receipt TTL cleanup without plaintext request/response
favorite/tag opaque tombstone; recent immediate delete/no tombstone
recent merge/count/30-day TTL/10k quota/persisted 120-per-minute frozen-clock limits
cleanup retry/restart/concurrent user delete
no hold/retention/Provider/Catalog mutation
```

- [x] **Step 2: Run red tests.** Expected: FAIL for missing package.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/overlay \
  -run 'SavedSearch|Favorite|Tag|Recent|Quota|Idempotency|Lifecycle' -count=1
```

- [x] **Step 3: Implement closed services.** Saved search stores encrypted
canonical AST/scope and explicit exact membership only. Favorite/tag/recent use
composite AssetRef. Transactions lock usage, validate the full mutation, enforce
quota, mutate natural rows, and commit a typed receipt. No overlay method writes
RecoveryPoint hold/retention or imports Provider/command packages.

Favorite add/bulk add, tag assignment, and recent record pass the active
mutation `*gorm.DB` into the Catalog authorizer before idempotency replay or any
write, closing the prior authorization-to-transaction window. Owner tag search
uses a bounded `CandidateRefs` port scoped to the selected authorized points.

Lifecycle implements the exact matrix in `design.md` section 11. It accepts a
bounded coordinator call for future Child 14 and is also called by the Search
worker's DB reconciliation pass.

- [x] **Step 4: Run focused/race tests.** Expected: PASS.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/overlay \
  -run 'SavedSearch|Favorite|Tag|Recent|Quota|Idempotency|Lifecycle' -count=3
go test -race ./internal/backupasset/overlay \
  -run 'Quota|Idempotency|Lifecycle' -count=3
```

## 10. Task 8: Settings, bootstrap, optional step-up, audit, and runtime wiring

**Files:** settings/foundation/bootstrap/keyring/audit/step-up/runtime files in
section 2.2, including the Repository test-only complete-settings fixture.

- [x] **Step 1: Write red integration tests.** Add exact registry/config tests
for these keys and safe bounds:

```text
backup_assets.search_reconcile_interval=1m
backup_assets.search_build_timeout=30m
backup_assets.search_batch_size=500
backup_assets.search_max_concurrency=2
backup_assets.search_ast_max_depth=8
backup_assets.search_ast_max_nodes=64
backup_assets.search_values_per_node=32
backup_assets.search_body_max_bytes=65536
backup_assets.search_value_max_bytes=1024
backup_assets.search_candidate_limit=10000
backup_assets.search_query_timeout=5s
backup_assets.search_page_size_max=200
backup_assets.search_suggestion_limit=20
backup_assets.saved_search_quota=100
backup_assets.favorite_quota=5000
backup_assets.tag_definition_quota=100
backup_assets.tag_assignment_quota=10000
backup_assets.overlay_bulk_max_items=200
backup_assets.overlay_label_max_bytes=256
backup_assets.recent_quota=10000
backup_assets.recent_retention=720h
backup_assets.recent_writes_per_minute=120
backup_assets.idempotency_ttl=24h
backup_assets.idempotency_key_max_bytes=128
```

Register these inclusive validation ranges:

```text
reconcile_interval 10s..1h; build_timeout 1m..24h
batch_size 50..5000; max_concurrency 1..16
ast_depth 1..16; ast_nodes 2..256; values_per_node 1..64
body_bytes 1024..65536; value_bytes 1..4096
candidate_limit 100..100000; query_timeout 100ms..30s
page_size 1..500; suggestion_limit 0..50
saved quota 1..1000; favorite quota 1..100000
tag-definition quota 1..1000; tag-assignment quota 1..200000
bulk items 1..1000; label bytes 1..4096
recent quota 1..100000; retention 24h..8760h; writes/minute 1..10000
idempotency TTL 1h..168h; key max bytes 32..256
```

Cross-validation requires nodes >= depth, body bytes >= value bytes,
candidate limit >= page size, page size >= suggestion limit, tag-assignment
quota >= bulk items, build timeout <= the existing Search lease absolute
deadline, and query timeout <= the server's 30-second write deadline.
`Idempotency-Key` additionally has a non-configurable 16-character minimum and
the ASCII alphabet `[A-Za-z0-9._~-]`.

Tests also cover cross-setting constraints, dynamic reads, feature disabled
before Search key/DB/worker work, enabled KEK rewrap/ensure, intentional lost
key typed unavailable, unexpected unwrap fatal, startup reconciliation, and
shutdown ordering.

Export the existing mutex-protected complete backup-asset snapshot from
`settings.Service` through a narrow `BackupAssetSettingsSnapshotReader` port.
Production `FoundationService` must use that port for one-map parsing; tests use
an explicit snapshot fake. Do not widen the existing `SettingsReader` interface
used by unrelated package fakes; the new combined Search/Overlay config getter
requires/asserts the snapshot port. A concurrent mutation test proves it cannot
observe half of a multi-key transition.

Add optional proof tests for absent, malformed, expired, wrong user/role/token
version/TOTP/action, all other registered purposes, exact valid
`asset.secret_reveal`, and injected infrastructure error. Non-exact proofs
produce no reveal capability and no distinguishing public reason.

Refactor the private verifier to typed `invalid` and `verifier_unavailable`
errors: user not found/claim mismatch is invalid, while nil dependencies and
non-not-found DB errors are unavailable. Mandatory middleware keeps its current
403/audit behavior; optional Search swallows only invalid proof and propagates
unavailable infrastructure as a safe request failure.

Add typed audit registry tests for:

```text
saved_search_use
saved_search_broken
favorite_tombstone
tag_assignment_tombstone
recent_record
overlay_cleanup
```

These values are action constants, never handler-local strings.

- [x] **Step 2: Run red tests.** Expected: FAIL for missing keys/config/actions.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/settings ./internal/bootstrap ./internal/backupasset \
  ./internal/backupasset/runtime ./internal/api/handlers \
  -run 'SearchConfig|OverlayConfig|SearchBootstrap|OptionalStepUp|AuditAction|RuntimeSearch' -count=1
```

- [x] **Step 3: Implement typed integration.** `FoundationService` returns
`SearchConfig` and `OverlayConfig` snapshots with validation. Runtime constructs
one Search/Overlay graph, exposes services to Router, rewraps/ensures Search key
only in the enabled startup pass, starts/joins Search worker, and keeps Catalog
available on intentional Search-key loss.

Extend bootstrap v1 enumeration/migration for the four encrypted overlay
columns. Add optional exact-purpose verification alongside the existing
mandatory verifier; do not weaken mandatory callers. Register typed audit
actions/fields and use the existing Audit Fingerprint key for canonical query
fingerprints.

- [x] **Step 4: Run focused tests.** Same command as Step 2. Expected: PASS.

## 11. Task 9: Strict handlers, router, RBAC, audit, and Swagger

**Files:** new handlers plus router/RBAC/docs files in section 2.

- [x] **Step 1: Write red handler/router tests.** Cover every route/method in
`design.md` section 12 with Admin, Operator, and Viewer. Assert Viewer is denied
before a spy service call; other-owner/missing overlays share a response;
search uses POST body; unknown/trailing/64-KiB body/8-KiB cursor/128-byte
idempotency failures are stable; `Idempotency-Key` and `X-Xirang-Step-Up` are
CORS-allowed; response helpers/envelopes are used.

Audit spies assert action/outcome/opaque IDs/count/proof ID and query
fingerprint input, then scan event/error/log/cursor bodies to prove no query,
path, name, tag, snippet, SQL, crypto, fence, or Provider locator. Add a source
boundary test that fails if search/overlay/new handlers import Provider,
runner, executor, SSH, or direct `fetch` equivalents.

- [x] **Step 2: Run red tests.** Expected: FAIL for missing handlers/routes.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/api/... \
  -run 'AssetSearch|SavedSearch|Favorite|AssetTag|Recent|BackupAssetRBAC|SearchSourceBoundary' -count=1
```

- [x] **Step 3: Implement transport adapters.** Add feature-disabled service
stubs in Router, inject runtime Search/Overlay services when present, register
Auth + `backup_assets:list`, parse strict body/header/path values, map sentinel
errors through response helpers, and write typed sanitized audit events. No
handler performs a DB query or returns a model.

- [x] **Step 4: Regenerate Swagger and verify only tracked output.** Run:

```bash
cd /home/murray/code/xirang
make swag-init
git status --short backend/internal/api/docs
```

Expected: only tracked `backend/internal/api/docs/docs.go` is staged later;
ignored `swagger.json`/`swagger.yaml` are not added.

- [x] **Step 5: Run focused tests.** Same Go command as Step 2. Expected: PASS.

## 12. Task 10: Frontend closed DTO/domain/API boundary

**Files:** frontend files in sections 2.1 and 2.2.

- [x] **Step 1: Write red API/mapper tests.** Cover valid raw snake_case to
camelCase mappings and whole-product blocking for every unknown enum/schema/op/
field, invalid composite ref, unsafe count/coverage/authoritative-empty product,
missing generation, content hit/snippet/suggestion without server content
capability, and illegal overlay
state/version.

Request spies assert:

```text
search uses POST body, never URL query
saved search use sends opaque ID only
mutations send Idempotency-Key through central request
only exact stepUpProof uses X-Xirang-Step-Up
no module reads/writes localStorage, sessionStorage, history, location, or router
no raw query/path/selection/result/saved AST is persisted or URL-encoded
```

- [x] **Step 2: Run red tests with clean Node environment.** Expected: FAIL for
missing modules/options/types.

```bash
cd /home/murray/code/xirang/web
env -u NODE_ENV npm run test -- \
  src/lib/api/backup-assets-boundary.test.ts \
  src/lib/api/backup-asset-search-api.test.ts \
  src/lib/api/backup-asset-overlays-api.test.ts \
  src/lib/api/client.test.ts
```

- [x] **Step 3: Implement boundary.** Add `idempotencyKey?: string` to central
`RequestOptions` and set the header only when supplied. Move shared opaque ID,
AssetRef, finite count/time, and blocked-projection parsing into the focused
boundary module; migrate existing Catalog API tests without changing behavior.

Add closed domain types and separate search/overlay API factories. Raw DTOs are
private; mapping returns one blocked projection on any coupled inconsistency.
No component/page/route/hook/i18n/storage code changes.

- [x] **Step 4: Run focused and full frontend gates.** Expected: PASS.

```bash
cd /home/murray/code/xirang/web
env -u NODE_ENV npm run test -- \
  src/lib/api/backup-assets-boundary.test.ts \
  src/lib/api/backup-asset-search-api.test.ts \
  src/lib/api/backup-asset-overlays-api.test.ts \
  src/lib/api/backup-assets-api.test.ts \
  src/lib/api/client.test.ts
env -u NODE_ENV npm run check
```

## 13. Task 11: CI parity and complete verification

**Files:** `.github/workflows/ci.yml` plus all test files above.

- [x] **Step 1: Extend mandatory PostgreSQL CI.** Add
`REQUIRE_POSTGRES_SEARCH_TEST: "1"` and
`REQUIRE_POSTGRES_OVERLAY_TEST: "1"`; extend the migration regex to
`62|63|64|65`; add exact Search and Overlay behavior steps. Keep PostgreSQL 18
and the existing Catalog job.

- [x] **Step 2: Run fresh focused gates.** Start with a clean isolated
PostgreSQL test database and run:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/search ./internal/backupasset/overlay \
  ./internal/backupasset/runtime ./internal/api/... ./internal/settings \
  ./internal/bootstrap -count=1
go test ./internal/database -run '^TestBackupAssetMigration065SQLite$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database -run '^TestBackupAssetMigration065Postgres$' -count=1
REQUIRE_POSTGRES_SEARCH_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/search -run '^TestSearchBehaviorPostgres$' -count=1
REQUIRE_POSTGRES_OVERLAY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/overlay -run '^TestOverlayBehaviorPostgres$' -count=1
go test -race ./internal/backupasset/search ./internal/backupasset/overlay \
  ./internal/backupasset/runtime -count=3
```

Expected: PASS with no skips in required PostgreSQL tests.

- [x] **Step 3: Run repository gates.** Run fresh commands, not cached claims:

```bash
cd /home/murray/code/xirang
make swag-init
bash scripts/check-migration-utc-safety.sh
GOFLAGS=-count=1 make backend-test
cd web && env -u NODE_ENV npm run check
cd .. && env -u NODE_ENV make check
git diff --check
```

Expected: every command exits 0. Inspect generated Swagger, migration order,
coverage output, and bundle budget rather than reporting only the final command.

- [x] **Step 4: Run security/source/manual diff review.** Run:

```bash
cd /home/murray/code/xirang
rg -n 'fmt\.Print|log\.Print|query=|localStorage|sessionStorage|SnapshotFileIndex|LIKE' \
  backend/internal/backupasset/search backend/internal/backupasset/overlay \
  backend/internal/api/handlers/backup_asset_search_handler.go \
  backend/internal/api/handlers/backup_asset_overlay_handler.go \
  web/src/lib/api/backup-asset-search-api.ts \
  web/src/lib/api/backup-asset-overlays-api.ts
git diff --stat
git status --short
```

Expected: the search has no prohibited logging/storage/legacy-search use; any
match is inspected and removed or proven to be a negative test assertion.
`git status` contains only the exact manifest plus Trellis planning metadata.

- [x] **Step 5: Run `trellis-check` inline.** Re-read relevant backend,
frontend, and guide specs; check cross-layer DTO flow, error mapping, reuse,
unknown-state blocking, no Provider mutation, and parent coverage. Correct
findings on the same branch and rerun affected gates.

Fresh local evidence on 2026-07-18:

```text
focused backend/API/settings/bootstrap                         exit 0
SQLite 000065 apply/down contract                              exit 0
required real PostgreSQL 000065/Search/Overlay (no skips)      exit 0
search/overlay/runtime race -count=3                           exit 0
Swagger generation + UTC migration scan                       exit 0
GOFLAGS=-count=1 make backend-test                             exit 0
env -u NODE_ENV npm run check: 136 files / 616 tests + build   exit 0
env -u NODE_ENV make check: lint/test/backend+frontend builds  exit 0
git diff --check and prohibited-source scan                    exit 0
```

The PostgreSQL database was the isolated `xirang_child7` database in the
`xirang-child7-postgres` container. These are local gates only; required PR CI
remains `not_executed` until Task 13.

## 14. Task 12: Exact staging and single work commit

The user requires one coherent product work commit rather than per-component
commits. Before staging, compare `git status --short` with section 2 and inspect
every diff.

- [ ] **Step 1: Stage only the approved manifest.** Use explicit groups:

```bash
cd /home/murray/code/xirang
git add .github/workflows/ci.yml
git add backend/internal/model/backup_asset_search.go backend/internal/model/backup_asset_overlay.go
git add backend/internal/database/migrations/sqlite/000065_backup_asset_search.up.sql backend/internal/database/migrations/sqlite/000065_backup_asset_search.down.sql
git add backend/internal/database/migrations/postgres/000065_backup_asset_search.up.sql backend/internal/database/migrations/postgres/000065_backup_asset_search.down.sql
git add backend/internal/database/backup_asset_migrations_integration_test.go
git add backend/internal/backupasset/search backend/internal/backupasset/overlay
git add backend/internal/backupasset/keyring.go backend/internal/backupasset/keyring_test.go backend/internal/backupasset/lease.go backend/internal/backupasset/lease_test.go
git add backend/internal/backupasset/audit_action.go backend/internal/backupasset/audit_action_test.go backend/internal/backupasset/service.go backend/internal/backupasset/service_test.go
git add backend/internal/backupasset/repository/testutil_test.go
git add backend/internal/settings/service.go backend/internal/settings/service_test.go backend/internal/bootstrap/bootstrap.go backend/internal/bootstrap/bootstrap_test.go
git add backend/internal/backupasset/runtime/runtime.go backend/internal/backupasset/runtime/runtime_test.go backend/internal/backupasset/runtime/search_worker.go backend/internal/backupasset/runtime/search_worker_test.go
git add backend/internal/api/handlers/step_up.go backend/internal/api/handlers/step_up_test.go backend/internal/api/handlers/backup_asset_search_handler.go backend/internal/api/handlers/backup_asset_search_handler_test.go backend/internal/api/handlers/backup_asset_overlay_handler.go backend/internal/api/handlers/backup_asset_overlay_handler_test.go
git add backend/internal/api/router.go backend/internal/api/router_test.go backend/internal/api/backup_asset_rbac_test.go backend/internal/api/docs/docs.go
git add web/src/lib/api/core.ts web/src/lib/api/client.test.ts web/src/lib/api/backup-assets-boundary.ts web/src/lib/api/backup-assets-boundary.test.ts
git add web/src/lib/api/backup-assets-api.ts web/src/lib/api/backup-assets-api.test.ts web/src/lib/api/backup-asset-search-api.ts web/src/lib/api/backup-asset-search-api.test.ts web/src/lib/api/backup-asset-overlays-api.ts web/src/lib/api/backup-asset-overlays-api.test.ts web/src/types/domain.ts
git add .trellis/spec/backend/database-guidelines.md .trellis/spec/frontend/type-safety.md
git add .trellis/tasks/07-12-backup-data-explorer-design/task.json .trellis/tasks/07-18-backup-assets-search-overlays
git diff --cached --check
git diff --cached --name-only
```

Expected: only exact approved files; no ignored Swagger JSON/YAML, sibling
tasks, local runtime directories, 000066+, docs/pages/components, Provider, or
release metadata.

- [ ] **Step 2: Create one work commit.** Only after fresh Task 11 evidence:

```bash
git commit -m "feat: add permission-aware backup asset search"
```

Expected: one product/planning commit on the dedicated branch, never `main`.

## 15. Task 13: Trellis finish, journal, PR, CI, merge, and post-merge

- [ ] **Step 1: Finish only the Child.** Load `trellis-finish-work`, confirm
quality evidence is fresh, then archive
`.trellis/tasks/07-18-backup-assets-search-overlays`. Never archive
`.trellis/tasks/07-12-backup-data-explorer-design`; it remains `planning` and
program progress becomes 7/15. Let the Trellis archive flow make its expected
archive metadata commit.

- [ ] **Step 2: Record a concrete journal entry and commit it.** Record base,
scope, migration 000065, exact commands/results, key/security decisions,
rollback, commit SHA, and pending PR state in the active developer journal;
update its index if Trellis rotates files. Commit only those concrete workspace
files in a separate journal commit.

- [ ] **Step 3: Push and open one PR.** Push
`codex/backup-assets-search-overlays`, open one PR to `main`, use a conventional
title, include security/migration/real-PostgreSQL evidence and the fact that the
feature remains disabled. Do not merge Release Please PR #386.

- [ ] **Step 4: Monitor every required CI job.** Stay on this branch, fix each
failure here, push, and continue monitoring until all required checks are green
or a real external blocker is recorded. Pending, missing, skipped required
PostgreSQL, or failing checks are not success.

- [ ] **Step 5: Squash merge and monitor post-merge.** After approval and green
CI, squash merge the single Child PR. Monitor main CI, Release Please, any auto
release, Publish Docker Images, and Docker Hub Description where triggered.
Because this feature remains gated and a formal release may not be created,
record explicitly whether no GitHub Release/Docker publish was expected. GitHub
Release remains the public version source of truth; Docker Hub remains the only
official public image source; stable public releases use semver tags only.

- [ ] **Step 6: Restore branch hygiene.** Fetch/prune, switch local `main`,
fast-forward it to `origin/main`, confirm no local-only main commit, remove the
merged feature branch/worktree when safe, and report the squash SHA plus final
parent progress. Do not start Child 8's 000066 implementation from pre-merge
state.

## 16. Requirement-to-task coverage

| PRD acceptance | Implement task(s) |
|---|---|
| AC-1 normalization/rank/order/cursor parity | 2, 5, 11 |
| AC-2 paired 000065/apply/down/model/UTC | 1, 11 |
| AC-3 key isolation/rewrap/loss/reindex | 2, 8 |
| AC-4 staging/fencing/reconciliation | 4, 11 |
| AC-5 AST/scope/no fallback/broken exact | 3, 5, 7 |
| AC-6 ownership matrix | 3, 5, 9 |
| AC-7 secret three-valued/exact purpose/no leak | 5, 8, 9 |
| AC-8 complete response/coverage truth | 5, 10 |
| AC-9 content ingest | 6 |
| AC-10 overlays/quota/idempotency/lifecycle/no hold | 7, 9 |
| AC-11 API/RBAC/audit/Swagger/no Provider | 8, 9, 11 |
| AC-12 frontend safe boundary | 10 |
| AC-13 full fresh gates | 11 |
| AC-14 staged delivery/post-merge/hygiene | 12, 13 |

## 17. Implementation self-review checklist

- [x] Every schema entity has a model, paired DDL, lifecycle owner, parity test,
  and down behavior.
- [x] Search and overlay types/enum strings match across model, SQL, Go DTO,
  Swagger, frontend raw mapper, and domain type.
- [x] No placeholder, hidden sibling dependency, database-native search
  semantic, direct fetch, raw snake_case consumer, or field-level unknown-state
  repair remains.
- [x] Every result/count/suggestion/snippet path proves RBAC, producing
  ownership, classification, exact purpose, and coverage in the required order.
- [x] Every mutable operation proves target ownership, whole-request
  validation, quota, idempotency, optimistic version, typed audit, and cleanup.
- [x] Search/Entry/Cursor/Audit/Cleanup/future Derived keys are distinct; KEK
  rewrap and Search replacement/loss tests prove the intended transitions.
- [x] No query/path/name/tag/content/OCR/snippet/selection/plain token enters a
  posting, cursor, audit detail, log, metric, URL, or browser storage.
- [x] `000066...000071`, Provider, Command, Content Broker, UI, Worker,
  Derived/Preview/Export/Recovery/Retention/GA scope are untouched.
- [ ] Exact staging matches section 2 and all fresh validation evidence is
  recorded before any success claim.

## 18. Execution status ledger

| Item | Current status |
|---|---|
| task creation / dedicated branch / base | complete |
| focused research and three planning artifacts | approved |
| task status | in_progress |
| `task.py start` | complete |
| product files/migrations | complete_local (Tasks 1-10 plus review corrections implemented) |
| red tests/product tests/builds | pass_local (Task 11 fresh focused/dual-engine/race/full gates exit 0) |
| Search Token generation/rewrap/reindex | complete for Child 7 contract (enabled startup rewrap/ensure, loss unavailable, replacement invalidation/tag reconciliation) |
| work commit | not_executed |
| finish/archive auto-commit | not_executed |
| concrete journal commit | not_executed |
| push / PR / required CI | not_executed |
| merge / post-merge automation | not_executed |
| main sync / branch cleanup | pending |

Tasks 1-11 are complete in the current unstaged worktree. Tasks 12-13 remain
unexecuted: no staging, work commit, archive, journal commit, push, PR, required
CI, merge, post-merge automation, main sync, or branch cleanup has occurred.
