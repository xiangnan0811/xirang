# Child 6 Current Main Evidence

## 1. Research boundary

- Read-only audit target: `main` / `origin/main` / Child base `2edd795581f9368dbaacb27ad2d9f389848060fe`.
- Audit date: 2026-07-17 (Asia/Shanghai).
- Scope: merged Child 1–5 interfaces needed by `backup-assets-catalog`.
- No product code, migration, Provider data, release, PR, or task execution was changed by this research.

## 2. Merged dependency evidence

| Dependency | Main evidence | Catalog implication |
|---|---|---|
| Child 1 foundation | `backend/internal/model/backup_asset.go`, `backup_asset_catalog.go`, `backup_asset_lease.go`; paired `000062_backup_asset_foundation` | Repository/RP/manifest/generation/entry/lease tables and stable key domains already exist. |
| Child 2 readers | `backend/internal/backupasset/provider/contracts.go`, `cursor.go`, `restic.go`, `rsync.go`, `rclone.go`, registry tests | Narrow read-only Provider ports and signed Provider cursors exist; public API still needs a Catalog-owned cursor and Repository-owned exact session. |
| Child 3 Restic lineage | `backend/internal/backupasset/publication/contracts.go`; Restic publication/manifest and repository publication files; paired `000063` | Exact Restic lineage, active manifest, `point_publication`, and immutable point publication facts exist. |
| Child 4 Rsync | `backend/internal/backupasset/repository/query.go:379+` and Rsync publication files; paired `000064` | `BeginManagedRsyncPointRead` safely reconstructs one exact committed point without accepting roots/paths, but it is Rsync-specific and task-ID-shaped. |
| Child 5 Rclone | Rclone portable/native publication, manifest, health and repository integration files | Managed point reader primitives exist inside the adapter, but no Repository-owned unified exact Rclone Catalog session exists. |
| Runtime composition | `backend/internal/backupasset/runtime/runtime.go`, `controller.go`, publication and Rclone health workers; `backend/cmd/server/main.go`; `backend/internal/api/router.go` | Runtime is already the single graph/lifecycle owner. Catalog must extend it, not instantiate services in handlers. |

Git history inspected on current main confirms Child 1–5 are merged. Release Please PR #386 remains an unmerged v0.46.0 candidate; latest formal release/tag is v0.45.0. Neither is part of Child 6 planning.

## 3. Existing schema facts

Paired SQLite/PostgreSQL `000062` creates:

- `catalog_generations(id, recovery_point_id, manifest_id, generation, state, is_active, source_fingerprint, expected_entry_count, written_entry_count, expected_digest, written_digest, error_code, correlation_id, started_at, finished_at, created_at, updated_at)`;
- unique `(recovery_point_id, generation)` and partial unique active-generation index per recovery point;
- `catalog_entries` keyed by `(generation_id, entry_id)`, with `recovery_point_id`, `parent_entry_id`, private normalized path/name/provider locator and safe metadata columns;
- listing index `(recovery_point_id, generation_id, parent_entry_id, name, entry_id)`;
- installation-stable `entry_identity` key domain and rotatable `cursor_signing` key domain;
- `recovery_point_leases` with an active unique owner slot `(recovery_point_id, holder_type, owner_id)`.

Paired `000063` extends lease holder types with `point_publication`; `catalog_build` was already present in `000062`.

### Gap conclusion

- No migration is needed for Catalog generations, entries, exact asset identity, cursor signing, or durable point fencing.
- The schema has no repository-scoped lease key/table. A stable owner on different RecoveryPoint rows does not serialize different points in one Repository. Reusing a sentinel point, unrelated setting row, or Provider path as a lock is rejected.
- `000065…000071` are reserved for Search–GA. Child 6 cannot consume or renumber them without a parent-level migration decision.

## 4. Existing identity and safety contracts

- `backupasset.AssetRef` validates the composite RecoveryPoint + Entry identity; opaque point IDs are 32 lowercase hex and entry IDs are 64 lowercase hex.
- `KeyDomainEntryIdentity` is installation-stable and rotation-prohibited; `KeyDomainCursorSigning` supports bounded dual-key verification.
- `RecoveryPointLease` supports acquire/renew/release/takeover and `LeaseFence` validation. Its uniqueness includes `owner_id`, so a generation-specific owner would permit parallel builders; Child 6 must use a deterministic point-scoped owner such as `catalog:<recoveryPointID>`.
- `catalog_entries.normalized_path`, `name`, and `encrypted_provider_locator` are not public DTO fields. Provider locator is encrypted by model hooks.
- Typed permissions already exist. Admin has all asset permissions; Operator has list/preview; Viewer has no backup asset permission and therefore must be rejected by `RBAC(backup_assets:list)` before handler access.
- Typed audit actions already include repository list, recovery-point list/detail/evidence/diff, and asset list; no raw string action is needed for Child 6 read routes.

## 5. Reader and manifest findings

### 5.1 Common reader contract

`provider.Entry` exposes safe metadata plus private `EntryLocator`, but it does not expose every field used by the richer publication-manifest canonical codecs. Therefore a generic loop over `Entry` cannot honestly claim that its digest is byte-for-byte equal to the committed publication manifest.

### 5.2 Restic

- Publication manifest generation has its own canonical stream, count, logical bytes, digest, completeness and fidelity facts.
- Exact Restic read is available through a lineage session keyed by Task and exact native point resolution.
- Catalog needs a Repository-owned facade keyed by RecoveryPoint ID, and a Provider-specific completion proof derived from the same canonical manifest contract; public callers must not provide a native snapshot ID or path.

### 5.3 Rsync

- `BeginManagedRsyncPointRead` loads the exact committed point, validates lineage, active publication evidence, marker/source fingerprint and manifest summary, constructs private runtime access, and returns a bounded session.
- It does not provide a common Catalog completion proof API and currently requires Task ID as an input. Catalog must obtain producing lineage internally and receive only safe canonical records/proof.

### 5.4 Rclone

- The adapter can list an exact committed managed point when given private `RcloneManagedPointAccess`; tests prove it does not fall back to mutable root.
- Repository composition currently lacks a unified `Begin...PointRead` session parallel to managed Rsync. Child 6 must add the facade and keep bound config/native locators private.

### Decision

Child 6 will not compare a newly invented Catalog digest to the publication digest. `expected_digest` freezes the immutable active publication-manifest digest; `written_digest` records the Catalog canonical digest and the two fields are not directly compared. Each Provider Catalog suite must return (a) a publication-compatible manifest proof checked against active manifest ID/revision/digest/count/completeness/source revision and (b) a Catalog canonical count/digest checked independently against the indexer's accumulator/staged rows. If any Provider cannot supply both required proofs, its generation cannot become `complete` and Child 6 cannot claim complete three-Provider exposure.

Mutable heads without an immutable publication manifest use a different contract: a Catalog canonical digest plus stable pre/post/transaction source fingerprint, and never masquerade as manifest-verified history.

## 6. Ownership findings

`repository.applyOperatorRepositoryVisibility` currently grants repository visibility through active non-archived Task links or RecoveryPoints joined back to a non-archived Task. This is insufficient for Child 6 because:

- a shared Repository can contain multiple producing lineages;
- repository visibility must not imply visibility of every point or aggregate;
- archived/deleted Task rows无法再提供 current ownership control-plane proof；RecoveryPoint 的 node/task snapshot 只能授权后展示，不能给 Operator 授权；
- imported or unattributed points must remain Admin-only;
- existing `loadLineages` can expose link/point names unless every row is filtered first.

Child 6 must introduce one reusable producing-lineage predicate：Admin 可见全部 eligible points；Operator 必须通过 current non-archived producing Task → Task.NodeID → NodeOwner，并验证 typed publication lineage 的 Task/TaskRun/TaskRepositoryLink/repository bindings 与 live rows/FKs 一致。删除/归档/畸形或冲突 lineage、无法归属/imported baseline 均 Admin-only。该 predicate 用于 repository projection、point selection、entry/evidence/diff queries 和 pagination subqueries，不能先 count 再在 Go 中过滤。

## 7. Runtime and shutdown findings

- Publication and Rclone health workers already demonstrate runtime-owned Run/Shutdown lifecycle and structured background work.
- No Catalog worker/service/package exists on current main.
- A process-local per-repository lane can serialize work inside the official single runtime without schema changes; durable per-point leases prevent duplicate activation for the same point.
- This is not a durable cross-process Repository lease. Preserving the parent wording literally requires new schema and a migration decision. Both alternatives are documented in the PRD/design approval gate.

## 8. API/frontend findings

- Current public Repository routes already use AuthMiddleware, typed RBAC, response helpers, Swagger annotations and thin handler tests.
- Existing repository list cursor is signed and role/user scoped, but Catalog point/entry/diff cursors also need active generation, sort, parent/two-point scopes and expiry binding.
- Frontend uses `request<T>` and API factory composition. No backup repository/recovery-point/asset API modules exist yet.
- Raw DTO mappers must own snake_case, dates, nullable values and closed-enum fallbacks; no component/UI work is needed in Child 6.

## 9. Planning conclusions

Decision update (2026-07-17): the user selected option A. Child 6 reuses `000062`/`000063`, adds no migration, uses a process-local per-repository lane plus durable per-point fence, and explicitly does not claim cross-process same-repository/different-point serialization. This resolves the schema/deviation question only; artifact approval and implementation/start remain separate gates.

1. Reuse `000062`/`000063`; do not create or number a Child 6 migration under the selected option A.
2. Add a Repository-owned unified exact Catalog read session and Provider-specific proof contract; do not let Catalog or handlers execute commands.
3. Centralize producing-lineage authorization before projection/count/evidence/pagination.
4. Keep generation, coverage, staleness and content availability separate.
5. Require all three Provider Catalog contract suites before claiming complete Catalog exposure.
6. Keep `backup_assets.enabled=false`, Command unsupported, frontend boundary-only, and Provider bytes untouched.
