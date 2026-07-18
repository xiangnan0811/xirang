# Child 7 Current Main Evidence

## 1. Research boundary

- Read-only audit target: `/home/murray/code/xirang` at
  `8cd6e5184e7dd05f702c3a5762b013c67901a399`.
- Audit date: 2026-07-18 (Asia/Shanghai).
- Branch used for planning artifacts: `codex/backup-assets-search-overlays`;
  PR base: `main`.
- Merged dependency: Child 6 PR #390, squash commit `8cd6e51`.
- This research did not run `task.py start`, change product code, execute product
  tests, create commits, push, create a PR, or change Provider bytes.

## 2. Baseline and migration reservation

- After `git fetch --prune origin`, `HEAD`, `main`, and `origin/main` all resolve
  to `8cd6e5184e7dd05f702c3a5762b013c67901a399`.
- The worktree was clean before task creation.
- Both migration directories stop at paired
  `000064_backup_asset_rsync_publication_contract`.
- `000065` is free in SQLite and PostgreSQL. The parent reservation remains:
  Child 7 = `000065`; Child 8 through Child 15 = `000066...000071`.
- `backend/internal/database/backup_asset_migrations_integration_test.go`
  currently has version constants 62/63/64 and real SQLite/PostgreSQL fixtures.
  Its PostgreSQL fixture fails rather than skips when
  `REQUIRE_POSTGRES_MIGRATION_TEST=1` and no DSN is present.
- `.github/workflows/ci.yml` has a PostgreSQL 18 service. Its migration regex
  currently covers only 062/063/064, and the behavior job runs only
  `TestCatalogBehaviorPostgres`. Child 7 must extend both; a SQL text test is
  not parity evidence.

## 3. Child 6 Catalog facts on current main

The implementation, rather than the archived plan, establishes these reusable
contracts:

| Evidence | Current contract | Child 7 consequence |
|---|---|---|
| `backupasset/catalog/ownership.go` | `AuthorizedPointIDs` filters Admin/Operator producing lineage before names, counts, evidence, or pagination; Viewer is invalid | Search scope resolution must reuse/extend this predicate and may not create a parallel ownership rule |
| `backupasset/catalog/service.go` | Active Catalog requires a complete generation; coverage, staleness, and content availability are separate | Search coverage must be a separate projection and must bind the exact active Catalog generation |
| `backupasset/catalog/cursor.go` | Signed cursor uses the independent `cursor_signing` domain, binds user/role/resource/generation/sort, expires within 15 minutes, and re-loads opaque anchors | Search needs a separate closed cursor envelope using the same cursor-key domain, but it must contain no query/path/name text |
| `backupasset/catalog/contracts.go` | Closed enums are validated; unknown internal values return a safe contract error | Search/overlay DTOs and frontend mappers must block the whole coupled projection on unknown or impossible states |
| `backupasset/catalog/indexer.go` | Catalog stages a generation and atomically activates only after proof, source, and lease-fence validation | Metadata search projection needs its own staging generation and `search_index` fence; it may never treat Catalog rows as search-complete without an activated projection |
| `backupasset/runtime/catalog_worker.go` | Runtime owns async scheduling, dynamic config reads, bounded concurrency, cancellation, and shutdown | Search backfill/reconciliation belongs in runtime, not handlers; it must remain DB-only and never invoke a Provider |
| `api/handlers/backup_asset_handler.go` | Thin strict handlers, 64 KiB body and 8 KiB cursor caps, standard response helpers, safe error mapping, and asset audit sink | Create focused search/overlay handlers rather than adding business logic to the existing 628-line Catalog handler |
| `web/src/lib/api/backup-assets-api.ts` | Raw unknown input is mapped to camelCase composite `AssetRef`; unknown enums become a blocked projection | Child 7 follows the same boundary and adds whole-response closed-product validation; no page/component/router work |

Current Catalog behavior tests exercise SQLite and real PostgreSQL ordering,
cursor, ownership, activation rollback, and concurrency. Search parity can reuse
their database setup helpers/pattern, but must have independent search behavior
fixtures and a mandatory CI environment flag.

## 4. Current schema and model gaps

Paired `000062` already owns:

- `catalog_generations` and `catalog_entries`, including private normalized
  path/name and encrypted Provider locator;
- composite Catalog row identity `(generation_id, entry_id)` and active
  generation uniqueness per RecoveryPoint;
- `wrapped_domain_keys` with only `entry_identity`, `cursor_signing`,
  `audit_fingerprint`, and `recovery_cleanup_ownership` domains;
- `recovery_point_leases` with no `search_index` holder type;
- append-only `backup_asset_audit_events`, including keyed query fingerprint,
  step-up action/proof, and safe field columns.

There is no search generation, document, posting, per-field coverage,
classification revision, encrypted saved-search AST, favorite, tag, recent,
quota counter, or overlay idempotency table. There is also no stable
`(recovery_point_id, entry_id)` Catalog FK because the same mutable-head entry
identity can occur in multiple Catalog generations. Overlay persistence must
therefore bind the composite opaque values, FK the RecoveryPoint/user where
appropriate, and rely on service/lifecycle validation for the active entry.

`backend/internal/model/backup_asset_catalog.go` has a free-form legacy
`security_state` column on Catalog entries. Existing Child 6 rows use empty
state. Child 7 must map only empty -> `unknown` as an explicit legacy rule;
unknown non-empty future values fail the search projection rather than being
guessed field-by-field.

## 5. Keyring, KEK, and bootstrap findings

- `backupasset/keyring.go` generates random 32-byte domain keys, wraps them with
  the application KEK, supports version lookup, explicit lost state, and
  `RewrapAll` without changing plaintext key/version.
- Entry identity and recovery cleanup ownership are stable domains; cursor and
  audit domains have rotation behavior. There is no Search Token domain.
- No production caller currently invokes `RewrapAll`; it is test-only. A
  Search Token contract that says KEK rotation only rewraps therefore needs an
  explicit startup/bootstrap call, with unwrap failure treated as fatal.
- `bootstrap.MigrateEncryptionV1ToV2` and `CountV1EncryptedData` enumerate
  secret columns explicitly. If saved-search ASTs and idempotency request
  fingerprints use model encryption, both functions and tests must include
  those columns.
- Search Token replacement is not an ordinary KEK rotation. It must explicitly
  invalidate search projections/cursors and schedule a rebuild; loss must make
  search unavailable, not silently generate a replacement.

Conclusion: paired `000065` must extend the wrapped-key CHECK with
`search_token`; Go must add a stable/rebuildable Search Token policy and a
separate explicit replace/recover-for-reindex path. Search, Entry, Cursor,
Audit, future Derived, and cleanup keys remain distinct.

## 6. Settings and feature-gate findings

`settings.Service` currently registers the foundation, Catalog, audit, lease,
Provider, publication, manifest, and Rclone settings. `backup_assets.enabled`
defaults to `false`. `backupasset.FoundationService` materializes typed
Catalog/lease/provider configs, and runtime re-reads dynamic values.

There are no AST, query time/candidate, saved-search, favorite, tag, bulk,
recent retention/rate, quota, or idempotency limits. Child 7 must register
them, add a typed `SearchConfig`/`OverlayConfig`, validate hard cross-setting
bounds, and include them in the atomic backup-asset settings snapshot. The
feature gate remains false and must be checked before DB/keyring/worker work.

## 7. Authorization, step-up, and audit findings

- `backup_assets:list` is granted to Admin and Operator; Viewer has no asset
  permission. Search and user overlays can reuse it without introducing a
  broader permission.
- `auth.StepUpActionAssetSecretReveal` already exists in the exact backend and
  frontend allowlists. `validateStepUpProof` checks token class, exact action,
  user, role, token version, TOTP state, and five-minute expiry. Other purposes
  cannot substitute.
- Existing step-up middleware is mandatory/denying. Search needs an optional
  exact-purpose verifier: absent/invalid/wrong-purpose proof means secret and
  unknown content facts are excluded, while authorized metadata search remains
  usable. It must never downgrade an invalid proof into content access.
- `backupasset/audit_action.go` already registers `asset_search`, saved-search
  CRUD, favorite add/remove, tag CRUD/assign/unassign, and `recent_clear`.
  Lifecycle reconciliation still needs explicit typed actions (or exact
  lifecycle operation fields) for broken scopes/tombstones/recent recording;
  no handler-local action strings are allowed.
- The audit writer already HMACs query fingerprints with the independent Audit
  Fingerprint key and stores no raw query. The search handler should pass only
  the canonical query to this in-memory fingerprint input; logs, errors,
  metrics, cursors, and persisted audit fields remain text-free.

## 8. Legacy search boundary

`backend/internal/api/handlers/snapshot_search_handler.go` remains a legacy
Restic-only `GET /tasks/:id/snapshots/search?q=...` implementation backed by
`SnapshotFileIndex`, SQL `%LIKE%`, and `LIMIT 200`. It has exact-lineage and
completion-marker guards, but cannot satisfy Child 7 scope, normalization,
ranking, coverage, grouping, secrecy, or cross-engine cursor semantics.

Child 7 adds `POST /asset-search` and must not route through, migrate, or claim
truth from this legacy table. Removing the legacy route/index remains Child 15
scope.

## 9. Frontend boundary findings

- `request<T>` is the only normal JSON boundary and already supports a
  purpose-bound step-up header; it has no idempotency-key option.
- `backup-assets-api.ts` and `domain.ts` expose composite AssetRefs and closed
  Catalog products. Raw snake_case stays inside API modules.
- The existing backup-assets API file is already 372 lines and the recovery
  point API is 732 lines. Separate search and overlay API modules match the
  repository's resource-oriented pattern and the parent frontend migration
  rule better than appending all Child 7 logic to one file.
- Search query text, paths, selections, and saved ASTs are not currently in any
  backup-assets browser storage because there is no UI. Child 7 tests must keep
  it that way. Future URL state exposes only a validated opaque saved-search
  ID; Child 7 does not add a route.

## 10. Focused planning conclusions

1. Use paired `000065_backup_asset_search` and leave `000066...000071`
   untouched.
2. Add a DB-only, fenced, atomic search projection tied to the exact active
   Catalog generation; never fall back to an older indexed point when the
   newest authorized point is building or unavailable.
3. Reuse Catalog ownership, Cursor Signing, Audit Fingerprint, exact
   `asset.secret_reveal`, settings, runtime lifecycle, and PostgreSQL fixtures.
4. Add an independent wrapped Search Token key and `search_index` lease holder;
   KEK rotation rewraps, explicit token replacement/loss forces reindex and
   stale cursors.
5. Store no content/OCR/excerpt plaintext. Child 7 stores HMAC postings,
   classification/coverage metadata, and an optional opaque reference to a
   future encrypted excerpt only.
6. Implement overlays as owner-scoped control-plane data with transactional
   quota/idempotency, exact source cleanup semantics, and no retention-hold or
   Provider mutation behavior.
7. Deliver only backend contracts plus frontend raw DTO/domain/API mappers and
   tests. No page, route, component, Worker, Derived Store, content ticket,
   preview, export, recovery, purge, or Provider command belongs here.
