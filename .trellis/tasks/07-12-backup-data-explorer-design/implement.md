# Backup Data Explorer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task. In the current Codex inline workflow, implementation and checks must use `superpowers:executing-plans`; do not dispatch implement/check sub-agents. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** Complete planning package approved by the user on 2026-07-13; this parent remains a planning/integration tracker, and this file alone authorizes no implementation or child-task creation.

**Goal:** Build the approved end-state read-only backup asset explorer: repository-independent recovery points, complete Catalog/search, secure native preview, optional derived-processing Workers, durable exports, and controlled infrastructure recovery.

**Architecture:** `BackupRepository` and `RecoveryPoint` form the backup truth boundary. Rebuildable Catalog and Derived planes are separated from a re-authorized Content plane; Provider adapters preserve Rsync/Restic/Rclone semantics, and optional Workers consume only Broker grants. The UI is a deep-linkable three-view backup workspace backed by dedicated asset permissions and persisted asynchronous jobs.

**Tech Stack:** Go 1.26, Gin, GORM, SQLite/PostgreSQL paired migrations, zerolog, SSH/rsync/restic/rclone; React 18, TypeScript 5.8 strict, Vite 7, Tailwind 3.4, Radix/shadcn, i18next, Vitest/Testing Library; Docker Compose, Nginx, optional sandboxed Worker image.

---

## 0. Execution Contract

This is a program-level master plan. The approved design contains independently verifiable subsystems, so the current task becomes a **planning parent** and must not be started as the implementation target.

After the user later requests implementation, create the next required child with `task.py create --parent 07-12-backup-data-explorer-design`; do not create all 15 merely because the planning package was approved. Start only the child that owns the next deliverable after its focused PRD/design/plan review. Each child receives the relevant sections of `prd.md`/`design.md` and the corresponding task section below.

### 0.1 Child task map and dependencies

| Order | Proposed child slug | Deliverable | Depends on |
|---:|---|---|---|
| 1 | `backup-assets-domain-foundation` | schema, domain states, feature gates, task/archive safety | none |
| 2 | `backup-assets-provider-readers` | repository identity, access binding, provider read adapters/reconnect probe | 1 |
| 3 | `backup-assets-restic-lineage` | exact Restic run attribution and native RecoveryPoint publication | 1–2 |
| 4 | `backup-assets-rsync-versioning` | hard-link/full-copy versioned Rsync publication and migration preflight | 1–3 |
| 5 | `backup-assets-rclone-versioning` | unique-prefix/native-object-version Rclone publication and migration preflight | 1–3 |
| 6 | `backup-assets-catalog` | atomic Catalog generations, listing APIs, provider reconciliation | 2–5 |
| 7 | `backup-assets-search-overlays` | portable search, saved searches, favorites/tags/recent, coverage | 6 |
| 8 | `backup-assets-content-plane` | delivery tickets, Range, secure cache, core renderers/download | 2, 6; 7 for migration order |
| 9 | `backup-assets-workspace-ui` | `/app/backups` workspace and core three-column explorer | 6–8 |
| 10 | `backup-assets-worker-protocol` | persistent processing queue, grants, derived encryption, Worker protocol | 6–8 |
| 11 | `backup-assets-worker-capabilities` | thumbnail/OCR/document/scan/media/archive Workers and enhanced UI | 9–10 |
| 12 | `backup-assets-export-archive` | durable encrypted batch export and restricted archive member retrieval | 7–11 |
| 13 | `backup-assets-controlled-recovery` | RecoveryPlan/Job, preflight, isolated/default and in-place restore | 2–12 (12 is migration-order only) |
| 14 | `backup-assets-lifecycle-reconnect` | retention owner, reconnect/import, purge, GC, task archival migration | 3–13 |
| 15 | `backup-assets-ga-hardening` | migration cutover, docs, observability, load/security tests, legacy UI removal | all |

Task 3 first merges the generic `EvidenceExecutor`/runner publication seam. Tasks 4 and 5 may then develop in parallel, but both must rebase after either changes shared Task validation; Task 6 does not expose a complete Catalog until all three Provider contract suites pass. Tasks 7 and 8 may develop in parallel after Task 6, but Child 8 must rebase onto merged Child 7 and rerun every gate before the `000064` PR can merge after `000063`. No public feature is enabled by default before Task 15.

Task 7 owns the metadata/search projection contract and nullable encrypted-excerpt references; it does not create excerpt ciphertext. Task 10 depends on Task 7 and owns the encrypted Derived Store. Task 11 atomically publishes text/OCR/classification derivatives and their search postings/field coverage through those two stable ports, so neither side has a reverse or undeclared dependency.

### 0.2 Future child creation commands

These are reference commands. Run each one only when the user later requests that delivery wave; planning-package approval alone authorizes none of them:

```bash
python3 ./.trellis/scripts/task.py create "备份资产领域基础" --slug backup-assets-domain-foundation --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产 Provider 读取适配" --slug backup-assets-provider-readers --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "Restic 精确血缘与恢复点发布" --slug backup-assets-restic-lineage --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "Rsync 版本化恢复点" --slug backup-assets-rsync-versioning --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "Rclone 版本化恢复点" --slug backup-assets-rclone-versioning --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产 Catalog" --slug backup-assets-catalog --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产搜索与用户覆盖" --slug backup-assets-search-overlays --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产内容平面" --slug backup-assets-content-plane --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产工作区 UI" --slug backup-assets-workspace-ui --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产 Worker 协议" --slug backup-assets-worker-protocol --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产 Worker 能力" --slug backup-assets-worker-capabilities --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产导出与归档" --slug backup-assets-export-archive --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产受控恢复" --slug backup-assets-controlled-recovery --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产生命周期与重连" --slug backup-assets-lifecycle-reconnect --parent 07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py create "备份资产 GA 加固" --slug backup-assets-ga-hardening --parent 07-12-backup-data-explorer-design
```

### 0.3 Branch and feature-gate rules

- Every child starts from an up-to-date `main` on `codex/<child-slug>` and merges by PR with required CI green.
- Additive migrations land before code that writes the new schema; old binaries must tolerate the added tables/columns.
- `backup_assets.enabled` defaults `false` through Tasks 1–14. Internal tests set it explicitly. Task 15 enables it for new installations only after migration readiness; existing installations see an admin migration/preflight state.
- No child may write, move, rename, version, or delete existing Provider bytes unless its PRD contains a dry-run, explicit enablement, rollback locator, and Provider contract tests.
- Keep official all-in-one deployment on port 10761 and the existing image namespace. Worker packaging must not change the core image contract.

### 0.4 Migration reservation

This plan reserves migration numbers `000062` onward from the current `000061` baseline. Migration-bearing PRs merge and deploy strictly in numeric order (`000062 → … → 000069`); parallel development never permits `000064` to merge before `000063`. Before each child writes a migration, verify the entire remaining reservation is still free in both engines. If another merged change consumed any number, renumber that migration and every later reserved migration, all four paired files, commands and references together; never create a skipped `golang-migrate` version or divergent SQLite/PostgreSQL numbers.

Child 1 creates a real SQLite/PostgreSQL apply/down integration harness and adds a PostgreSQL CI service. Every later migration child extends the same fixtures and must pass both engines before merge; pure Go tests or SQL text inspection do not count as PostgreSQL parity.

### 0.5 Planning-parent merge gate

The parent planning package must exist on `main` before any child branches from it:

1. obtain final user approval of `prd.md`, `design.md`, and this file;
2. commit the parent planning package, open a planning-only PR to `main`, monitor required CI, and squash-merge;
3. monitor the applicable post-merge automation and record explicitly that no product release/image publish is expected for planning-only docs;
4. fast-forward local `main` to `origin/main`; create no child until the user later asks to begin implementation.

For each future child: switch to the current `main`, pull `--ff-only`, create `codex/<child-slug>`, set Trellis branch/base metadata, complete and review that child's focused planning artifacts, and only then run `task.py start <child-slug>` after the user requests implementation.

### 0.6 Per-child PR and staging contract

Every child, not only Child 15, must push its dedicated branch, open a PR, monitor/fix all required CI on that branch, squash-merge only when green, monitor applicable post-merge release/image/docs automation, and fast-forward local `main` before a dependent child branches. Migration and API dependencies are satisfied only by merged `main`, not an unreviewed sibling branch.

Each child starts from a clean worktree. Before commit, run `git status --short`, stage only exact files in that child's reviewed file manifest, run `git diff --cached --name-only` and `git diff --check`, and stop if unrelated user changes appear. Directory-wide `git add backend`, `git add web/src`, or wildcard staging in this master plan must be expanded to exact changed paths in the child plan before execution.

## 1. File/Package Map

### Backend domain packages

```text
backend/internal/backupasset/
├── domain.go
├── errors.go
├── audit.go
├── service.go
├── provider/
│   ├── provider.go
│   ├── identity.go
│   ├── rsync.go
│   ├── restic.go
│   └── rclone.go
├── catalog/
│   ├── service.go
│   ├── indexer.go
│   ├── search.go
│   ├── normalize.go
│   └── ownership.go
├── content/
│   ├── broker.go
│   ├── ticket.go
│   ├── range.go
│   ├── cache.go
│   └── renderer.go
├── processing/
│   ├── coordinator.go
│   ├── protocol.go
│   ├── grants.go
│   └── derived_store.go
├── export/
│   ├── service.go
│   ├── archive.go
│   ├── crypto.go
│   └── worker.go
├── recovery/
│   ├── service.go
│   ├── preflight.go
│   ├── executor.go
│   └── worker.go
└── retention/
    ├── worker.go
    └── reconcile.go
```

Handlers remain thin in resource files such as `backup_repository_handler.go`, `backup_asset_handler.go`, `backup_content_handler.go`, `backup_export_handler.go`, and `backup_recovery_handler.go`. Persistent structs live in focused `backend/internal/model/backup_asset*.go` files.

### Frontend feature files

```text
web/src/lib/api/
├── backup-repositories-api.ts
├── recovery-points-api.ts
├── backup-assets-api.ts
├── backup-content-api.ts
├── backup-exports-api.ts
└── backup-recovery-api.ts

web/src/features/backup-assets/
├── backup-assets-workspace.tsx
├── use-backup-assets-state.ts
├── asset-context-panel.tsx
├── asset-list.tsx
├── asset-grid.tsx
├── asset-inspector.tsx
├── asset-preview.tsx
├── asset-search.tsx
├── asset-bulk-bar.tsx
├── export-job-panel.tsx
├── recovery-plan-wizard.tsx
└── *.test.tsx

web/src/pages/
├── backups-page.tsx
├── backups-page.overview.tsx
├── backups-page.data.tsx
├── backups-page.recovery.tsx
└── backups-page.test.tsx
```

Cross-module product types remain in `web/src/types/domain.ts`; raw snake_case types remain private to API modules.

## 2. Child 1 — Domain Foundation

**Files:**

- Create: `backend/internal/model/backup_asset.go`
- Create: `backend/internal/model/backup_asset_catalog.go`
- Create: `backend/internal/model/backup_asset_audit.go`
- Create: `backend/internal/model/backup_asset_lease.go`
- Create: `backend/internal/backupasset/domain.go`
- Create: `backend/internal/backupasset/errors.go`
- Create: `backend/internal/backupasset/audit.go`
- Create: `backend/internal/backupasset/audit_action.go`
- Create: `backend/internal/backupasset/authorization.go`
- Create: `backend/internal/backupasset/lease.go`
- Create: `backend/internal/backupasset/service.go`
- Create: `backend/internal/backupasset/{domain,audit,audit_action,authorization,lease,service}_test.go`
- Create: `backend/internal/secure/keyring.go`, `backend/internal/secure/keyring_test.go`
- Create: `backend/internal/database/backup_asset_migrations_integration_test.go`
- Modify: `.github/workflows/ci.yml` to provide the PostgreSQL integration service/job
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000062_backup_asset_foundation.{up,down}.sql`
- Modify: `backend/internal/model/task.go`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `backend/internal/middleware/rbac.go`, `backend/internal/middleware/rbac_test.go`
- Modify: `backend/internal/auth/jwt.go`, `backend/internal/auth/jwt_test.go`
- Modify: `backend/internal/api/handlers/auth_handler.go`, `backend/internal/api/handlers/auth_handler_test.go`
- Modify: `backend/internal/api/handlers/step_up.go`, `backend/internal/api/handlers/step_up_test.go`
- Modify: `backend/internal/api/handlers/credential_access_grant.go`, `backend/internal/api/handlers/credential_access_grant_test.go`
- Modify: `backend/internal/api/handlers/batch_handler.go`, `backend/internal/api/handlers/batch_handler_test.go`
- Modify: `backend/internal/api/handlers/task_handler.go`, `backend/internal/api/handlers/task_handler_test.go`
- Modify: `backend/internal/api/handlers/terminal_handler.go`, `backend/internal/api/handlers/terminal_handler_test.go`
- Modify: `backend/internal/api/router.go`, `backend/internal/api/router_test.go`
- Modify: `web/src/lib/api/totp-api.ts`; create `web/src/lib/api/totp-api.test.ts`
- Modify: `web/src/lib/api/core.ts`, `web/src/lib/api/client.test.ts`
- Modify: `web/src/lib/step-up-storage.ts`, `web/src/lib/step-up-storage.test.ts`
- Modify: `web/src/hooks/use-step-up-action.ts`, `web/src/hooks/use-step-up-action.test.tsx`
- Modify: `web/src/hooks/use-console-task-operations.ts`, `web/src/hooks/use-console-data.test.tsx`
- Modify: `web/src/context/auth-context-provider.tsx`, `web/src/context/auth-context.shared.ts`, `web/src/context/auth-context.test.tsx`
- Modify: `web/src/components/batch-command-dialog.tsx`, `web/src/components/batch-command-dialog.test.tsx`
- Modify: `web/src/components/config-export-import.tsx`, `web/src/components/config-export-import.test.tsx`
- Modify: `web/src/components/restore-confirm-dialog.tsx`, `web/src/components/restore-confirm-dialog.test.tsx`
- Modify: `web/src/components/snapshot-browser.tsx`, `web/src/components/snapshot-browser.test.tsx`
- Modify: `web/src/components/ssh-key-export-dialog.tsx`, `web/src/components/web-terminal.tsx`, `web/src/components/web-terminal.test.tsx`
- Modify: `web/src/pages/tasks-page.tsx`, `web/src/pages/tasks-page.test.tsx`, `web/src/pages/notifications/alert-center.tsx`, `web/src/pages/notifications-page.test.tsx`

### Required contract

```go
package backupasset

type RepositoryStatus string
type RecoveryPointState string
type VersionMode string
type TaskPublicationMode string
type PointVersionSemantics string
type LeaseHolderType string
type ImmutabilityLevel string
type PhysicalAvailability string
type HoldState string
type CapabilityReasonCode string

type CapabilityReason struct {
    Code   CapabilityReasonCode `json:"code"`
    Params map[string]string    `json:"params,omitempty"`
}

type AssetRef struct {
    RecoveryPointID string `json:"recovery_point_id"`
    EntryID         string `json:"entry_id"`
}

const (
    RepositoryConnecting   RepositoryStatus = "connecting"
    RepositoryOnline       RepositoryStatus = "online"
    RepositoryDegraded     RepositoryStatus = "degraded"
    RepositoryOffline      RepositoryStatus = "offline"
    RepositoryDisconnected RepositoryStatus = "disconnected"
    RepositoryPurging      RepositoryStatus = "purging"
    RepositoryPurgeBlocked RepositoryStatus = "purge_blocked"

    VersionNativeSnapshot       VersionMode = "native_snapshot"
    VersionHardlinkTree         VersionMode = "hardlink_tree"
    VersionFullCopyTree         VersionMode = "full_copy_tree"
    VersionVersionedPrefix      VersionMode = "versioned_prefix"
    VersionNativeObjectVersions VersionMode = "native_object_versions"
    VersionMutableHead          VersionMode = "mutable_head"

    TaskLegacyMutable       TaskPublicationMode = "legacy_mutable"
    TaskVersionedHardlink   TaskPublicationMode = "versioned_hardlink"
    TaskVersionedFullCopy   TaskPublicationMode = "versioned_full_copy"
    TaskVersionedPrefix     TaskPublicationMode = "versioned_prefix"
    TaskNativeObjectVersion TaskPublicationMode = "native_object_versions"

    SemanticsNativeSnapshot  PointVersionSemantics = "native_snapshot"
    SemanticsXirangManifest  PointVersionSemantics = "xirang_manifest"
    SemanticsImportedBaseline PointVersionSemantics = "imported_baseline"
    SemanticsMutableHead      PointVersionSemantics = "mutable_head"

    PointObserved     RecoveryPointState = "observed"
    PointRetired      RecoveryPointState = "retired"
    PointPreparing    RecoveryPointState = "preparing"
    PointVerifying    RecoveryPointState = "verifying"
    PointCommitted    RecoveryPointState = "committed"
    PointDegraded     RecoveryPointState = "degraded"
    PointExpiring     RecoveryPointState = "expiring"
    PointExpired      RecoveryPointState = "expired"
    PointFailed       RecoveryPointState = "failed"
    PointPurgeBlocked RecoveryPointState = "purge_blocked"

    LeaseRsyncParent     LeaseHolderType = "rsync_parent"
    LeaseCatalogBuild    LeaseHolderType = "catalog_build"
    LeaseContentSession  LeaseHolderType = "content_session"
    LeaseProcessingJob   LeaseHolderType = "processing_job"
    LeaseExportJob       LeaseHolderType = "export_job"
    LeaseRecoveryJob     LeaseHolderType = "recovery_job"

    ImmutabilityMutable          ImmutabilityLevel = "mutable"
    ImmutabilityXirangManaged    ImmutabilityLevel = "xirang_managed"
    ImmutabilityBackendVersioned ImmutabilityLevel = "backend_versioned"
    ImmutabilityStorageWORM      ImmutabilityLevel = "storage_worm"

    AvailabilityOnline  PhysicalAvailability = "online"
    AvailabilityOffline PhysicalAvailability = "offline"
    AvailabilityMissing PhysicalAvailability = "missing"
    AvailabilityUnknown PhysicalAvailability = "unknown"

    HoldNone     HoldState = "none"
    HoldActive   HoldState = "active"
    HoldReleased HoldState = "released"
)

type CapabilitySet struct {
    List, SearchPath, OpenSequential, OpenRange bool
    Download, Restore, Diff, NativeHistory     bool
    UnavailableReasons                         []CapabilityReason
}
```

- [ ] **Step 1: Write state-transition, three-enum mapping, composite-asset-identity and repository-identity tests.** Cover every Repository/RecoveryPoint state; reserve `observed` and non-destructive `retired` exclusively for one stable mutable-head row per repository; prove observed refresh preserves ID, disconnect stays observed/offline, cutover/withdraw uses observed→retired with typed reason and preserved rollback locator, and retired cannot reconcile/reactivate/read/publish. Explicit physical purge uses observed|retired→expiring→expired and failure/retry uses expiring→purge_blocked→expiring; only confirmed Provider deletion reaches expired. Reject committed→preparing, immutable→observed and mutable-head→committed/imported-baseline mutation. Prove `TaskLegacyMutable → VersionMutableHead + SemanticsMutableHead + PointObserved`, prove an entry ID without its recovery point cannot resolve, prove Command Tasks remain capability-unsupported without an explicit artifact contract, and prove public identity/locator DTOs contain no repository password, SSH material, raw Rclone config or untyped/raw capability reason.
- [ ] **Step 2: Run the focused tests and confirm they fail because the package does not exist.**

```bash
cd backend && go test ./internal/backupasset -run 'Test(RecoveryPointTransition|RepositoryIdentity)' -count=1
```

Expected: FAIL with missing package symbols.

- [ ] **Step 3: Add the domain enums, capability type, sentinel errors, and pure transition validator.** Required sentinels: `ErrNotFound`, `ErrForbidden`, `ErrConflict`, `ErrInvalidState`, `ErrProviderUnavailable`, and `ErrCapabilityUnavailable`; wrap causes with `%w`.
- [ ] **Step 4: Add paired schema migrations.** `000062` creates repository/access/link/recovery-point/manifest/catalog-generation/catalog-entry, versioned wrapped-domain-key, recovery-point-lease, and segmented `backup_asset_audit_events`/checkpoint tables; adds nullable `tasks.archived_at`; and uses `ON DELETE SET NULL` for optional Task/TaskRun lineage. Down migrations drop only new tables/indexes/column using engine-safe syntax.
- [ ] **Step 5: Add model structs matching every SQL column and JSON-hide secret/access locator fields.** Repository access config uses model hooks or service encryption; API responses use explicit sanitized DTOs, never raw models.
- [ ] **Step 6: Implement the versioned domain keyring, `RecoveryPointLease` service and purpose-bound step-up foundation.** Entry Identity and Recovery Cleanup Ownership are installation-stable independent domains; Cursor Signing supports bounded dual-key verification; Audit Fingerprint uses its own domain. Lease acquire/renew/release/takeover binds holder type, owner, attempt/fence, short expiry and absolute deadline; an old fence cannot publish after takeover. Keep JWT `purpose=step_up` as token class and add required allowlisted `step_up_action`; change request DTO, signer and every verifier API—including terminal WebSocket authentication—to require an exact typed purpose. Register all current high-risk purposes including `terminal.open`, plus `asset.secret_reveal`, `asset.download`, `asset.export_create`, `asset.export_download`, `asset.recover`, `recovery.result_download`, `recovery.result_retain` and `repository.purge`; migrate existing backend/frontend callers and purpose-key the frontend proof cache. Tests cover missing/unknown/generic legacy proof and the complete pairwise cross-purpose rejection matrix; terminal accepts only `terminal.open` and rejects every other purpose, with no compatibility path that accepts a purpose-less proof.
- [ ] **Step 7: Implement the complete typed asset-action registry, sanitization, segmented hash-chain tests and writer.** Freeze the Design §9.3 repository/RP, asset/overlay, content/processing, export/recovery and lifecycle actions in one enum; a router coverage test fails for an unregistered action or handler string. Drop raw path/name/query/snippet/content/ticket/cookie/credential/Provider fields; path/query correlation uses the Audit Fingerprint Key and keyed digests, never bare hashes. Store counts/stable IDs, verify entry and cross-segment checkpoint continuity, retain safe anchors when closed segment details expire, and aggregate high-volume Range reads into bounded session summaries. Credential-bearing Provider operations also use existing credential audit with a shared correlation ID.
- [ ] **Step 8: Add foundational settings with fixed safe defaults.** Register `backup_assets.enabled=false`, Catalog batch size `2000` and build timeout `30m`, repository reconcile interval `15m`, audit segment `10,000 events/24h`, audit detail retention `180d`, checkpoint retention `2555d`, lease duration `5m`, heartbeat `60s`, and absolute holder deadline cap `7d`. Child-specific settings are added by their owning migrations/steps. Path/key settings are `RequiresRestart`; secret-like values are `Sensitive`.
- [ ] **Step 9: Add dedicated permissions to all role maps.** Exact permissions are `backup_assets:list`, `backup_assets:preview`, `backup_assets:download`, `backup_assets:export`, `backup_assets:recover`, `backup_repositories:manage`, and `backup_repositories:purge`. Admin gets all seven; Operator gets list/preview only; Viewer gets none. Freeze recovery-result delivery as an action governed by `backup_assets:recover` plus exact job ownership and its own step-up purpose: `backup_assets:download` alone is insufficient and is not an additional requirement. Tests assert Viewer cannot acquire any asset/repository permission and that purge is not implied by manage.
- [ ] **Step 10: Add the dual-engine migration harness/CI service and freeze the purpose-migration caller manifest.** Apply the complete migration sequence through `000062`, verify schema/FKs/indexes/UTC behavior, apply the down migration on disposable SQLite and PostgreSQL databases, and fail rather than skip when `TEST_POSTGRES_DSN` is absent in the CI job. Before editing, run `rg -l 'RequireStepUp|EnforceStepUp|enforceStepUpForContext|validateStepUpProof|GenerateStepUpToken|requestStepUpProof|ensureStepUpProof|useStepUpAction|readStepUpProof|saveStepUpProof|clearStepUpProof' backend web/src` and reconcile every production/test caller with the Child 1 file list. The check fails if a caller is unowned; this includes direct terminal WebSocket validation and router-level task/batch enforcement even when another leaf handler has no direct call.
- [ ] **Step 11: Run focused tests.**

```bash
cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/backupasset ./internal/model ./internal/secure ./internal/settings ./internal/middleware ./internal/database ./internal/auth ./internal/api/... -run 'BackupAsset|DomainKeyring|RecoveryPointLease|StepUp|Purpose' -count=1
cd ../web && npm run check
cd ..
```

Expected: backend focused tests and the complete frontend gate PASS; missing, legacy-generic and cross-purpose proofs fail in tests.

- [ ] **Step 12: Run the full gate, stage every foundation/step-up caller, and commit.**

```bash
make check
git add backend/internal/backupasset backend/internal/model backend/internal/secure backend/internal/settings backend/internal/middleware backend/internal/database/migrations backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/auth backend/internal/api/handlers/auth_handler.go backend/internal/api/handlers/auth_handler_test.go backend/internal/api/handlers/step_up.go backend/internal/api/handlers/step_up_test.go backend/internal/api/handlers/credential_access_grant.go backend/internal/api/handlers/credential_access_grant_test.go backend/internal/api/handlers/batch_handler.go backend/internal/api/handlers/batch_handler_test.go backend/internal/api/handlers/task_handler.go backend/internal/api/handlers/task_handler_test.go backend/internal/api/handlers/terminal_handler.go backend/internal/api/handlers/terminal_handler_test.go backend/internal/api/router.go backend/internal/api/router_test.go
git add web/src/lib/api/totp-api.ts web/src/lib/api/totp-api.test.ts web/src/lib/api/core.ts web/src/lib/api/client.test.ts web/src/lib/step-up-storage.ts web/src/lib/step-up-storage.test.ts web/src/hooks/use-step-up-action.ts web/src/hooks/use-step-up-action.test.tsx web/src/hooks/use-console-task-operations.ts web/src/hooks/use-console-data.test.tsx web/src/context/auth-context-provider.tsx web/src/context/auth-context.shared.ts web/src/context/auth-context.test.tsx web/src/components/batch-command-dialog.tsx web/src/components/batch-command-dialog.test.tsx web/src/components/config-export-import.tsx web/src/components/config-export-import.test.tsx web/src/components/restore-confirm-dialog.tsx web/src/components/restore-confirm-dialog.test.tsx web/src/components/snapshot-browser.tsx web/src/components/snapshot-browser.test.tsx web/src/components/ssh-key-export-dialog.tsx web/src/components/web-terminal.tsx web/src/components/web-terminal.test.tsx web/src/pages/tasks-page.tsx web/src/pages/tasks-page.test.tsx web/src/pages/notifications/alert-center.tsx web/src/pages/notifications-page.test.tsx .github/workflows/ci.yml
git commit -m "feat: add backup asset domain foundation"
```

Expected: full gate PASS; commit contains the complete purpose-bound signing/verifying/frontend caller migration but no backup-asset public routes or Provider mutations.

**Rollback:** Set `backup_assets.enabled=false`; old application ignores additive tables. Run down migration only before any child writes repository/recovery-point rows.

## 3. Child 2 — Provider Readers And Repository Access

**Files:**

- Create: `backend/internal/backupasset/provider/{provider,identity,registry,rsync,restic,rclone}.go`
- Create: `backend/internal/backupasset/provider/{provider,identity,registry,rsync,restic,rclone}_test.go`, `backend/internal/backupasset/provider/testdata/repository_observations.json`
- Create: `backend/internal/api/handlers/backup_repository_handler.go`, `backend/internal/api/handlers/backup_repository_handler_test.go`
- Create: `backend/internal/api/backup_asset_rbac_test.go`
- Create: `backend/internal/fileaccess/safe_path.go`, `backend/internal/fileaccess/safe_path_test.go`
- Modify: `backend/internal/api/handlers/file_handler.go`, `backend/internal/api/handlers/file_handler_validate_test.go`
- Modify: `backend/internal/api/router.go`
- Modify: handler Swagger annotations and tracked generated `backend/internal/api/docs/docs.go`; regenerate ignored `swagger.json`/`swagger.yaml` for local validation only
- Modify: `backend/internal/sshutil/scope.go`, `backend/internal/sshutil/scope_test.go`

### Required provider interfaces

```go
type RepositoryProber interface {
    Probe(context.Context, AccessBinding) (RepositoryObservation, error)
}

type PointReader interface {
    ListRecoveryPoints(context.Context, Repository) ([]NativePoint, error)
    ListEntries(context.Context, Repository, PointLocator, EntryLocator, PageRequest) (EntryPage, error)
    StatEntry(context.Context, Repository, PointLocator, EntryLocator) (Entry, error)
    OpenSequential(context.Context, Repository, PointLocator, EntryLocator) (io.ReadCloser, ContentStat, error)
}

type PointRangeReader interface {
    OpenRange(context.Context, Repository, PointLocator, EntryLocator, ByteRange) (io.ReadCloser, ContentStat, error)
}

type PointPublisher interface {
    Publish(context.Context, PublicationAttempt) (ProviderCommitEvidence, error)
}
type ManifestBuilder interface {
    BuildManifest(context.Context, Repository, PointLocator) (ManifestStream, error)
}
type RepositoryReconciler interface {
    Reconcile(context.Context, Repository) (RepositoryObservation, error)
}
type PointDeleter interface {
    DeletePoint(context.Context, Repository, PointLocator, FencingToken) error
}
```

- [ ] **Step 1: Write registry tests.** Unknown Provider returns `ErrCapabilityUnavailable`; a registered fake adapter probes identity, lists entries with cursor, and never exposes decrypted credentials in public DTOs.
- [ ] **Step 2: Run tests and confirm missing-interface failures.**

```bash
cd backend && go test ./internal/backupasset/provider -count=1
```

- [ ] **Step 3: Implement the narrow interface/registry set and repository identity sanitizer.** Consumer packages request only RepositoryProber, PointReader, PointRangeReader, PointPublisher, ManifestBuilder, RepositoryReconciler, PointDeleter or later optional restore/diff capabilities; they do not switch on executor strings. This child freezes mutation contracts but does not execute them.
- [ ] **Step 4: Extract and implement read-only adapters.** Move containment/realpath/symlink/root-allowlist rules from the handler into `fileaccess`, keep the current file handler on that shared lower-layer helper, and forbid Provider packages from importing handlers or copying rules. Restic uses exact repository identity/snapshot locator and `dump`; Rclone uses `lsjson`/`cat --offset --count` only when capability probes prove stable reads. This child adds no publication/delete commands.
- [ ] **Step 5: Add SSH credential purposes for repository probe/list/read.** Reuse `DialSSHForNodePurpose`; do not create raw SSH auth paths.
- [ ] **Step 6: Write handler and audit tests for connect probe/list/detail/disconnect.** Handlers parse/bind/call/respond only; the service owns identity conflicts and encrypted access bindings. Assert Admin access, Operator ownership filtering, Viewer 403, sanitized errors, and registered `repository_connect`/list/disconnect audit actions. A shared repository with mixed-owner Tasks exposes only the authorized producing lineage, never other recovery points, counts or evidence. For mutable repositories, first successful probe creates one stable observed row, later reconcile refreshes that ID, failure only changes availability/staleness, and disconnect preserves the row and offline Catalog without creating history.
- [ ] **Step 7: Wire feature-gated routes with Auth + RBAC + ownership.** Disabled mode returns a stable not-enabled response; no route is public.
- [ ] **Step 8: Regenerate Swagger and run focused/full tests.**

```bash
make swag-init
cd backend && go test ./internal/backupasset/provider ./internal/api/... -run 'Backup(Repository|Asset)' -count=1
cd .. && make backend-test
```

- [ ] **Step 9: Commit the read-only seam.**

```bash
git add backend/internal/backupasset/provider backend/internal/api backend/internal/sshutil backend/internal/fileaccess
git commit -m "feat: add backup repository read adapters"
```

**Rollback:** Disable the gate and revoke access bindings. No Provider bytes were changed, so rollback is database-only.

## 4. Child 3 — Restic Exact Lineage

**Files:**

- Modify: `backend/internal/task/executor/restic_executor.go`, `backend/internal/task/executor/restic_executor_test.go`
- Modify: `backend/internal/snapshot/indexer.go`, `backend/internal/snapshot/indexer_test.go`
- Create: `backend/internal/backupasset/provider/restic_publication.go`, `backend/internal/backupasset/provider/restic_publication_test.go`
- Modify: `backend/internal/task/runner.go`, `backend/internal/task/runner_timeout_test.go`, `backend/internal/task/manager.go`, `backend/internal/task/manager_test.go`
- Create: `backend/internal/backupasset/provider/testdata/{restic_backup_progress,restic_backup_summary,restic_backup_missing_summary,restic_backup_malformed}.ndjson`, `backend/internal/backupasset/provider/testdata/restic_shared_repository_snapshots.json`

### Required evidence shape

```go
type PublicationEvidence struct {
    RepositoryIdentity string
    ProviderKind       string
    PointLocator       string
    NativeID           string
    Manifest           ManifestEvidence
    CaptureStartedAt   time.Time
    CaptureFinishedAt  time.Time
    FidelityProfile    map[string]string
}

type ManifestEvidence struct {
    Algorithm, Generator, SchemaVersion string
    Digest                              string
    EntryCount, LogicalBytes            int64
    Completeness                        ManifestCompleteness
}

type ManifestCompleteness string
const (
    ManifestComplete ManifestCompleteness = "complete"
    ManifestPartial  ManifestCompleteness = "partial"
    ManifestFailed   ManifestCompleteness = "failed"
)

type EvidenceExecutor interface {
    RunWithEvidence(context.Context, model.Task, LogFunc, ProgressFunc) (int, *PublicationEvidence, error)
}
```

- [ ] **Step 1: Add failing parser tests using real-shaped Restic NDJSON.** Cover progress lines, final summary with full snapshot ID, missing summary, malformed output, and output redaction.
- [ ] **Step 2: Add a shared-repository regression test.** Two tasks share one repository; only the snapshot tagged with the producing Xirang task/run may become that Task's RecoveryPoint.
- [ ] **Step 3: Run focused tests and confirm current `Run` discards the snapshot ID.**

```bash
cd backend && go test ./internal/task/executor ./internal/backupasset/provider -run 'Restic.*(Summary|Lineage|SharedRepository)' -count=1
```

- [ ] **Step 4: Add unique task/run tags, parse the final summary, and build the exact manifest.** Do not infer `latest`; return no publication evidence if exact native ID is absent, even when command exit is zero. Stream `restic ls --json <full-snapshot-id>` into a canonical algorithm/generator/schema/count/logical-bytes/digest/completeness/fidelity result and reject locator drift or incomplete enumeration.
- [ ] **Step 5: Add the generic EvidenceExecutor runner seam and persist preparing→verifying→committed around it.** Transfer success and point publication failure remain distinct task evidence. A point stays `verifying` until the manifest and minimum verification are stored and match the exact Provider commit; Catalog is later and is not a substitute. Runtime audit contains IDs/stage only.
- [ ] **Step 6: Quarantine the legacy snapshot index.** Stop stamping all repository snapshots with the requesting Task. Legacy search may remain behind old UI until Catalog rebuild, but cannot enter new asset APIs.
- [ ] **Step 7: Test crash windows.** Provider snapshot exists/DB commit absent reconciles by unique tags; DB preparing row/no snapshot becomes failed after bounded reconciliation.
- [ ] **Step 8: Run Restic/task/full backend suites and commit.**

```bash
cd backend && go test ./internal/task/... ./internal/snapshot ./internal/backupasset/provider -count=1
cd .. && make backend-test
git add backend/internal/task backend/internal/snapshot backend/internal/backupasset/provider
git commit -m "feat: publish exact restic recovery points"
```

**Rollback:** New tags/rows are additive. Disable new publication and keep native Restic snapshots; do not forget/prune points during rollback.

## 5. Child 4 — Rsync Versioned Recovery Points

**Files:**

- Create: `backend/internal/backupasset/provider/rsync_publication.go`, `backend/internal/backupasset/provider/rsync_publication_test.go`
- Create: `backend/internal/backupasset/provider/rsync_preflight.go`, `backend/internal/backupasset/provider/rsync_preflight_test.go`
- Modify: `backend/internal/task/executor/executor.go`, `backend/internal/task/executor/executor_test.go`
- Modify: `backend/internal/task/service.go`; create `backend/internal/task/service_test.go`
- Modify: `backend/internal/api/handlers/task_handler.go`, `backend/internal/api/handlers/task_handler_test.go`
- Modify: `backend/internal/task/runner.go`, `backend/internal/task/manager.go`, `backend/internal/task/manager_test.go`
- Modify: `web/src/components/task-create-dialog.advanced.tsx`, `web/src/components/task-create-dialog.test.tsx`
- Modify: `web/src/lib/api/tasks-api.ts`, `web/src/lib/api/tasks-api.test.ts`
- Modify: `web/src/types/domain.ts`

- [ ] **Step 1: Write temp-filesystem preflight tests.** Cover same-mount probe, successful hard link, `EXDEV`, no-link support, inode/space warning, path overlap, non-empty staging, and rejected `--inplace/--append` flags.
- [ ] **Step 2: Write publication/lease/crash-window tests.** A changed file is copied, unchanged file shares inode, source deletion is absent from the new empty tree, committed prior tree is never mutated, and rename publishes only a complete manifest. The previous point is pinned by an `rsync_parent` lease until publication/cleanup; a Provider commit without DB commit reconciles by point marker, while DB preparing without a committed tree fails safely; an expired old fence cannot publish.
- [ ] **Step 3: Run tests and confirm the current mutable command cannot satisfy them.**

```bash
cd backend && go test ./internal/backupasset/provider -run 'Rsync.*(Preflight|Publication)' -count=1
```

- [ ] **Step 4: Implement Task modes `versioned_hardlink` and `versioned_full_copy`.** Map them explicitly to Repository `hardlink_tree`/`full_copy_tree` and point `xirang_manifest`. Attempt directories are unique and fenced; retries never reuse mutable staging. Manifest records flags/fidelity and digest before rename, then returns the generic PublicationEvidence through the merged runner seam.
- [ ] **Step 5: Extend Task validation, executor schema and API mapping.** Existing tasks default `legacy_mutable`; new UI recommends versioned mode only after successful preflight. Persist a publication schema/minimum-runtime marker, make unknown modes fail closed, and never silently convert a target. Downgrade requires pausing/fencing and relinking the preserved legacy locator before the old runtime starts.
- [ ] **Step 6: Add migration wizard tests.** Dry-run returns capacity/inode estimate and two choices: imported baseline or first-new-point. Cutover keeps the old target as rollback locator.
- [ ] **Step 7: Add frontend form/mapping tests.** Reuse existing primitives/i18n and explain Xirang-managed immutability versus WORM; components receive camelCase only.
- [ ] **Step 8: Run gates and commit.**

```bash
make backend-test
cd web && npm run check
cd ..
git add backend/internal/backupasset/provider backend/internal/task backend/internal/api/handlers/task_handler.go backend/internal/api/handlers/task_handler_test.go web/src
git commit -m "feat: add versioned rsync recovery points"
```

**Rollback:** Switch the TaskRepositoryLink back to the untouched legacy target. Never mutate/delete already committed hard-link trees; retain or expire them through the new lifecycle later.

## 6. Child 5 — Rclone Versioned Recovery Points

**Files:**

- Create: `backend/internal/backupasset/provider/rclone_publication.go`, `backend/internal/backupasset/provider/rclone_publication_test.go`
- Create: `backend/internal/backupasset/provider/rclone_preflight.go`, `backend/internal/backupasset/provider/rclone_preflight_test.go`
- Create: `backend/internal/backupasset/provider/rclone_native_versions.go`, `backend/internal/backupasset/provider/rclone_native_versions_test.go`
- Modify: `backend/internal/task/executor/rclone_executor.go`; create `backend/internal/task/executor/rclone_executor_test.go`
- Modify: `backend/internal/task/service.go`; create or extend `backend/internal/task/service_test.go`
- Modify: `backend/internal/api/handlers/task_handler.go`, `backend/internal/api/handlers/task_handler_test.go`
- Modify: `backend/internal/task/runner.go`, `backend/internal/task/manager.go`, `backend/internal/task/manager_test.go`
- Modify: `web/src/components/task-create-dialog.advanced.tsx`, `web/src/components/task-create-dialog.test.tsx`
- Modify: `web/src/lib/api/tasks-api.ts`, `web/src/lib/api/tasks-api.test.ts`
- Modify: `web/src/types/domain.ts`, `web/src/i18n/locales/zh.ts`, `web/src/i18n/locales/en.ts`
- Create: `backend/internal/backupasset/provider/testdata/{rclone_unique_prefix,rclone_inconsistent_listing,rclone_native_versions,rclone_weak_hash}.jsonl`

- [ ] **Step 1: Write command/fixture tests.** Each run writes a unique prefix, validates list/read-after-write, writes `commit.json` last, and never selects `--backup-dir` as the recovery-point representation.
- [ ] **Step 2: Write weak-remote, native-version and crash-window tests.** No stable hash/ID records weak fidelity; inconsistent listing delays commit; unsupported server-side copy falls back to full upload without changing point identity. Native mode records every object version ID/delete marker, proves retention/lifecycle and exact read/reconstruction/delete; unsupported remotes reject the mode. Provider commit without DB commit reconciles by marker, DB preparing without marker fails safely, and old fences cannot publish.
- [ ] **Step 3: Run tests and confirm the existing mutable sync fails the publication contract.**

```bash
cd backend && go test ./internal/task/executor ./internal/backupasset/provider -run 'Rclone' -count=1
```

- [ ] **Step 4: Implement unique-prefix and capability-gated native-object-version publication.** Both return the generic PublicationEvidence through the merged runner seam. Prefix commit contains point ID, source capture interval, normalized entry digest, capability/fidelity snapshot, and schema version; native mode contains per-object version/delete state and lifecycle proof. Both exclude credentials and remote config.
- [ ] **Step 5: Implement migration/runtime fencing.** Pause the legacy Task, detect external-writer drift, copy/import baseline only after stable observations, preserve the old prefix until explicit cleanup, persist a publication schema/minimum-runtime marker, and make unknown modes fail closed. Downgrade relinks the legacy prefix before an old scheduler starts.
- [ ] **Step 6: Add task UI/config mapping and preflight tests.** Display estimated API/storage cost, consistency/hash strength, and legacy mutable warnings.
- [ ] **Step 7: Run Provider/task/frontend/full gates and commit.**

```bash
cd backend && go test ./internal/task/executor ./internal/backupasset/provider -run 'Rclone' -count=1
cd ../web && npm run check
cd .. && make backend-test
git add backend/internal/backupasset/provider backend/internal/task backend/internal/api/handlers/task_handler.go backend/internal/api/handlers/task_handler_test.go web/src
git commit -m "feat: publish versioned rclone recovery points"
```

**Rollback:** Fence future versioned writes and return the Task to the preserved legacy prefix. Committed unique prefixes remain read-only and are not deleted by rollback.

## 7. Child 6 — Atomic Catalog Plane

**Files:**

- Create: `backend/internal/backupasset/catalog/{service,indexer,ownership,evidence,diff}.go`
- Create: `backend/internal/backupasset/catalog/{service,indexer,ownership,evidence,diff}_test.go`
- Create: `backend/internal/api/handlers/backup_asset_handler.go`, `backend/internal/api/handlers/backup_asset_handler_test.go`
- Modify: `backend/internal/api/router.go`, tracked generated `backend/internal/api/docs/docs.go`; regenerate ignored `swagger.json`/`swagger.yaml` for local validation only
- Modify: `backend/cmd/server/main.go`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Create: `web/src/lib/api/{backup-repositories-api,recovery-points-api,backup-assets-api}.ts` and `web/src/lib/api/{backup-repositories-api,recovery-points-api,backup-assets-api}.test.ts`
- Modify: `web/src/lib/api/client.ts`, `web/src/types/domain.ts`

### Required generation contract

```go
type CatalogGenerationState string
type CoverageStatus string
type Staleness string

const (
    GenerationBuilding   CatalogGenerationState = "building"
    GenerationComplete   CatalogGenerationState = "complete"
    GenerationPartial    CatalogGenerationState = "partial"
    GenerationFailed     CatalogGenerationState = "failed"
    GenerationSuperseded CatalogGenerationState = "superseded"

    CoverageBuilding    CoverageStatus = "building"
    CoverageComplete    CoverageStatus = "complete"
    CoveragePartial     CoverageStatus = "partial"
    CoverageFailed      CoverageStatus = "failed"
    CoverageUnavailable CoverageStatus = "unavailable"

    StalenessFresh   Staleness = "fresh"
    StalenessStale   Staleness = "stale"
    StalenessUnknown Staleness = "unknown"
)

type Coverage struct {
    Status         CoverageStatus `json:"status"`
    Staleness      Staleness      `json:"staleness"`
    IndexedEntries int64            `json:"indexed_entries"`
    ExpectedEntries *int64          `json:"expected_entries,omitempty"`
    ManifestDigest string           `json:"manifest_digest"`
    ObservedAt     time.Time        `json:"observed_at"`
}
```

- [ ] **Step 1: Write Catalog generation, evidence and diff tests.** A zero-entry point can complete; one inserted row never implies completeness; build failures leave the prior complete generation active; manifest count/digest mismatch produces partial/failed; generation state never leaks as coverage; unavailable/stale are represented independently. Mutable-head refresh atomically supersedes its prior generation on the same observed ID, a source-fingerprint race cannot publish, and retired heads have no active generation/search projection. Evidence aggregates exact TaskRun/manifest/verification/RestoreDrillEvidence without upgrading trust. Cross-version diff compares two explicit AssetRef scopes with stable pagination and separates Catalog metadata differences from Provider-native evidence.
- [ ] **Step 2: Run tests and confirm no Catalog service exists.**

```bash
cd backend && go test ./internal/backupasset/catalog -count=1
```

- [ ] **Step 3: Implement deterministic entry IDs and batched generation writes.** Normalize Provider paths once, derive keyed entry IDs, write with context and transactions, then atomically switch `active_generation_id` only after manifest validation.
- [ ] **Step 4: Implement the startup/periodic index worker.** Embed `retention.Loop`-style Run/Shutdown semantics, bounded concurrency, a `catalog_build` RecoveryPoint lease plus per-repository scheduler lease, backoff, and structured aggregate logs. Provider offline is a retryable availability state, not corruption; acquire/renew/release and stale-fence behavior are tested.
- [ ] **Step 5: Add list repository/recovery-point/entry, evidence and diff APIs.** Asset routes always carry `rpId + entryId`. Use cursor pagination, stable sort tuples, registered list/evidence/diff audit actions, dedicated permission/ownership filtering, capability reasons, and Catalog coverage in every response. Shared repositories project top-level metadata and all counts after producing-lineage filtering; unattributed/imported points are Admin-only. Handlers contain no Provider commands.
- [ ] **Step 6: Add raw DTO mappers and domain types on the frontend.** Every asset uses composite `{ recoveryPointId, entryId }`; API modules own snake_case, dates, nullable values, capability fallback, separate generation/coverage/staleness unions, evidence and diff DTOs; `apiClient` composes the new factories.
- [ ] **Step 7: Add handler/ownership tests.** Operator sees only owned producing lineage even in a shared repository; Viewer receives 403 before names/counts/evidence; unattributed points are Admin-only; repository offline still returns committed Catalog with content availability false. Every public GET/list/evidence/diff route asserts its domain audit action and sanitizer.
- [ ] **Step 8: Wire the worker lifecycle into `main.go` and test shutdown.** Startup initial reconciliation must not block the HTTP server indefinitely; Shutdown obeys the common worker contract.
- [ ] **Step 9: Regenerate Swagger, run gates, and commit.**

```bash
make swag-init
cd backend && go test ./internal/backupasset/catalog ./internal/api/... -run 'BackupAsset|Catalog' -count=1
cd ../web && npm run typecheck && npm run test -- backup-assets-api
cd .. && make backend-test
git add backend/internal/backupasset/catalog backend/internal/api backend/internal/settings/service.go backend/internal/settings/service_test.go backend/cmd/server web/src/lib/api web/src/types
git commit -m "feat: add atomic backup asset catalog"
```

**Rollback:** Disable Catalog scheduling and asset routes. Existing Provider bytes and recovery-point manifests remain authoritative; incomplete generations are safe to delete.

## 8. Child 7 — Portable Search And User Overlays

**Files:**

- Create: `backend/internal/model/backup_asset_search.go`
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000063_backup_asset_search.{up,down}.sql`
- Create: `backend/internal/backupasset/catalog/{normalize,search,search_key,overlays}.go`
- Create: `backend/internal/backupasset/catalog/{normalize,search,search_key,overlays}_test.go`
- Create: `backend/internal/backupasset/catalog/search_integration_test.go` for SQLite/PostgreSQL parity fixtures
- Modify: `backend/internal/api/handlers/backup_asset_handler.go`, `backend/internal/api/handlers/backup_asset_handler_test.go`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `backend/internal/bootstrap/bootstrap.go`, `backend/internal/bootstrap/bootstrap_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`
- Modify: `web/src/lib/api/backup-assets-api.ts`, `web/src/lib/api/backup-assets-api.test.ts`
- Modify: `web/src/types/domain.ts`

### Required query contract

```go
type SearchScopeMode string
type QueryOp string

const (
    SearchCurrent     SearchScopeMode = "current"
    SearchAllRetained SearchScopeMode = "all_retained"
    SearchExactPoints SearchScopeMode = "exact_points"

    QueryAnd   QueryOp = "and"
    QueryOr    QueryOp = "or"
    QueryNot   QueryOp = "not"
    QueryTerm  QueryOp = "term"
    QueryType  QueryOp = "type"
    QueryMTime QueryOp = "modified_time"
)

type SearchScope struct {
    Mode          SearchScopeMode
    RepositoryIDs []string
    TaskIDs       []uint
    RecoveryPointIDs []string
}

type QueryNode struct {
    Op            QueryOp
    Field         string
    Text          string
    Values        []string
    ModifiedFrom, ModifiedTo *time.Time
    Children      []QueryNode
}

type SearchQuery struct {
    SchemaVersion int // initial supported version is 1
    Root          QueryNode
    Scope         SearchScope
    Cursor        string
}

type ContentIndexIngest interface {
    PublishContentProjection(ctx context.Context, source AssetRef, projection ContentProjection, fence string) error
}
```

- [ ] **Step 1: Write normalization property tests.** NFKC/case folding, slash normalization, Chinese bigrams, Latin tokens, filename extensions, and equivalent strings must produce identical tokens on both database engines.
- [ ] **Step 2: Write authorization/classification-order tests.** Viewer cannot infer result counts/suggestions; Operator cannot infer another owner's paths or shared-repository lineages; `secret` and `unknown` content produce no content match/count/snippet/**suggestion** without a proof for the dedicated secret-reveal purpose. A proof for download/recovery/purge cannot be substituted.
- [ ] **Step 3: Write AST/scope/coverage/grouping tests.** Reject unknown schema/op/field, excessive depth/nodes/bytes and empty exact scope. Default scope uses each authorized lineage's newest committed point or stable mutable-head row; all-history groups by lineage+canonical path; exact-points binds explicit IDs and a saved search becomes broken when any required point expires rather than widening scope. Partial generations never return an authoritative zero-result claim.
- [ ] **Step 4: Run tests and confirm the old `%LIKE% LIMIT 200` path cannot satisfy stable ordering/coverage.**

```bash
cd backend && go test ./internal/backupasset/catalog -run 'Search|Normalize|Secret|Ownership' -count=1
```

- [ ] **Step 5: Add paired search/overlay schema and dual-engine migration fixtures.** Include search documents/postings/generations, sensitivity state/revision, nullable encrypted-excerpt reference metadata, wrapped Search Token Key metadata, saved searches, favorites, tag definitions/assignments, and recent access with required indexes and composite AssetRef ownership FKs. Apply/down `000063` on SQLite and PostgreSQL.
- [ ] **Step 6: Implement the portable metadata postings index and content-ingest port.** Generate a random Search Token Key in its keyring domain; store HMAC tokens/grams, a nullable opaque excerpt reference, field frequencies, and deterministic sort data. Child 7 does not create excerpt ciphertext. Go computes ranking/cursor with the independent Cursor Signing Key; DB-specific FTS is not the contract. `ContentIndexIngest` atomically checks source/fence/classification revision before replacing content postings and field coverage.
- [ ] **Step 7: Implement search service, APIs and audit.** Use `POST /asset-search`; validate the versioned query AST, apply authorization/classification before grouping/count/suggestions, return composite AssetRef, hit field and coverage, sign cursor with user/generation/scope, and record sanitized `search` events without query text.
- [ ] **Step 8: Implement overlay lifecycle, quotas and audit.** Saved searches store AST only; favorites/tags bind composite AssetRef; recent defaults 30 days and clears immediately on source expiry. Register safe limits for AST depth/nodes/bytes, per-user saved searches/favorites/tag definitions/assignments, label length, bulk mutation size and recent-write rate. Create/update/delete/clear/broken-scope changes use registered domain actions and never become retention holds.
- [ ] **Step 9: Add frontend mapping tests.** Map unknown status/coverage safely, never persist query text/path/selection in browser storage, and use opaque saved-search ID in URLs.
- [ ] **Step 10: Run SQLite/PostgreSQL parity and full gates.**

```bash
cd backend && go test ./internal/backupasset/catalog ./internal/api/... -run 'Search|SavedSearch|Favorite|AssetTag|Recent' -count=1
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration063' -count=1
cd ../web && npm run check
cd .. && make backend-test
```

- [ ] **Step 11: Commit the search/overlay slice.**

```bash
git add backend/internal/model backend/internal/database/migrations backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/backupasset/catalog backend/internal/api backend/internal/settings backend/internal/bootstrap web/src
git commit -m "feat: add permission-aware backup asset search"
```

**Rollback:** Turn off search scheduling and APIs, destroy the wrapped Search Token Key, and delete rebuildable postings. Catalog entries and Provider data remain intact.

## 9. Child 8 — Content Plane, Tickets, Range, And Core Preview

**Files:**

- Create: `backend/internal/model/backup_asset_content.go`
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000064_backup_asset_content.{up,down}.sql`
- Create: `backend/internal/backupasset/content/{broker,ticket,range,cache,renderer}.go`
- Create: `backend/internal/backupasset/content/{broker,ticket,range,cache,renderer}_test.go`
- Create: `backend/internal/api/handlers/backup_content_handler.go`, `backend/internal/api/handlers/backup_content_handler_test.go`
- Modify: `backend/internal/api/handlers/step_up.go`, `backend/internal/api/handlers/step_up_test.go`, `backend/internal/api/handlers/credential_access_grant.go`, `backend/internal/api/handlers/credential_access_grant_test.go`
- Modify: `backend/internal/api/router.go`, `backend/cmd/server/main.go`
- Modify: `deploy/nginx/templates/default.conf.template`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`
- Create: `web/src/lib/api/backup-content-api.ts`, `web/src/lib/api/backup-content-api.test.ts`

### Required ticket shape

```go
type DeliveryAction string
type DeliveryResourceKind string

const (
    DeliveryPreview  DeliveryAction = "preview"
    DeliveryDownload DeliveryAction = "download"

    DeliveryBackupAsset   DeliveryResourceKind = "backup_asset"
    DeliveryRecoveryResult DeliveryResourceKind = "recovery_result"
)

type RecoveryResultRef struct {
    RecoveryJobID string `json:"recovery_job_id"`
    ResultID      string `json:"result_id"`
}

type DeliveryResource struct {
    Kind           DeliveryResourceKind
    Asset          *backupasset.AssetRef
    RecoveryResult *RecoveryResultRef
}

type DeliveryGrant struct {
    ID, SessionID             string
    Resource                  DeliveryResource
    UserID                    uint
    Action                    DeliveryAction
    AbsoluteExpiresAt         time.Time
    IdleExpiresAt             time.Time
    LastActivityAt            time.Time
    MaxBytesPerRequest        int64
    MaxCumulativeBytes        int64
    MaxRequests, MaxInFlight  int
    AllowRange                bool
}
```

- [ ] **Step 1: Write ticket, typed-resource, purpose and source-lease tests.** Issuance stores only a secret hash and exactly one typed resource reference; malformed/dual/unknown kinds fail. Child 8 enables backup AssetRef only and rejects RecoveryResult until Child 13 registers its adapter. URL contains a non-authorizing delivery ID; cookie is HttpOnly/SameSite/Path-scoped/Secure in production. Wrong session/action/RP/range, expiry, logout, permission/classification changes reject access. Secret reveal accepts only `asset.secret_reveal`; original download accepts only `asset.download`; every other registered proof cross-fails. A `content_session` RecoveryPoint lease is acquired/renewed within the absolute deadline and released/reconciled on revoke/expiry.
- [ ] **Step 2: Write HTTP Range budget tests.** Support HEAD/full GET and exactly one normal/suffix Range; reject multipart Range. Cover 206/416, `If-Range`, stable ETag, atomic cumulative bytes/request/in-flight accounting, bounded idle refresh below absolute TTL, concurrent seek, cancellation and Provider without Range.
- [ ] **Step 3: Write cache cryptography tests.** Tampering/wrong chunk/generation fails AEAD, crash leftovers cannot decrypt after key restart, quotas/TTL/leases work, and disabled/full cache never writes plaintext fallback.
- [ ] **Step 4: Run tests and confirm current generic JWT/request path cannot serve native media.**

```bash
cd backend && go test ./internal/backupasset/content -count=1
```

- [ ] **Step 5: Add delivery/session schema, settings and dual-engine fixtures.** Store typed resource kind plus mutually exclusive AssetRef/RecoveryResultRef columns, grant hash/state/revocation/TTL/activity/cumulative-budget counters and safe metrics fields only; RecoveryResult columns remain disabled/no-FK until Child 13. Register memory/cache/ticket/rate/concurrency limits and a dedicated cache root that cannot resolve under `/data`, `/backup`, or `/logs`; apply/down `000064` on SQLite and PostgreSQL.
- [ ] **Step 6: Implement Broker and Provider capability routing.** Re-check RBAC/ownership and allow only committed/degraded immutable points or `PointObserved + SemanticsMutableHead`; mutable-head source fingerprint is checked before and after reads. Resolve composite opaque IDs server-side, propagate cancellation and enforce byte/rate quotas.
- [ ] **Step 7: Implement authenticated chunk cache, fail-closed core sensitivity classification and renderer policy.** Path/name/MIME/config rules plus bounded content scanning set `secret | non_secret | unknown` before text reveal/content postings; unknown is treated as secret without proof. Core supports bounded escaped text/config/log, safe raster image, same-origin PDF policy, audio/video native delivery, metadata/hex fallback, and attachment download. HTML/XML/SVG never active-inline.
- [ ] **Step 8: Add handler and audit tests.** Preview/list/download actions write safe domain audit summaries without raw path/content/ticket/cookie. Download issuance requires step-up; Viewer is denied before ticket creation.
- [ ] **Step 9: Wire the content route and Nginx policy.** Apply dedicated streaming/Range/proxy-buffer/timeout behavior only to the composite content route. Its Nginx log format must omit `$request`, `$request_uri`, `$uri`, arguments and cookies entirely while retaining request ID/status/bytes/timing; the application logs only a delivery-ID keyed fingerprint. Preserve the all-in-one port/TLS contract and add rendered-config assertions.
- [ ] **Step 10: Add frontend ticket API tests.** JSON issuance uses the central wrapper; content URLs are treated as opaque; no JWT/query secret or browser-storage persistence.
- [ ] **Step 11: Run security/full gates and commit.**

```bash
cd backend && go test ./internal/backupasset/content ./internal/api/... -run 'BackupContent|Delivery|Range|Cache' -count=1
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration064' -count=1
cd ../web && npm run check
cd .. && make backend-test
git add backend/internal/model backend/internal/database/migrations backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/backupasset/content backend/internal/api backend/internal/settings/service.go backend/internal/settings/service_test.go backend/cmd/server deploy/nginx web/src
git commit -m "feat: add secure backup asset content plane"
```

**Rollback:** Revoke all delivery sessions, stop the content route, discard the process cache key, and clean encrypted cache files. Catalog and recovery points remain browsable as metadata.

## 10. Child 9 — Backup Workspace And Core Explorer UI

**Files:**

- Modify: `web/src/pages/backups-page.tsx`, `web/src/pages/backups-page.test.tsx`
- Create: `web/src/pages/backups-page.{overview,data,recovery}.tsx`
- Create: `web/src/features/backup-assets/backup-assets-workspace.tsx`
- Create: `web/src/features/backup-assets/use-backup-assets-state.ts`, `web/src/features/backup-assets/use-backup-assets-state.test.ts`
- Create: `web/src/features/backup-assets/{asset-context-panel,asset-list,asset-grid,asset-inspector,asset-preview,asset-search,asset-bulk-bar}.tsx`
- Create: `web/src/features/backup-assets/{asset-overlays,asset-evidence,asset-versions}.tsx`
- Create: `web/src/features/backup-assets/{backup-assets-workspace,asset-context-panel,asset-list,asset-grid,asset-inspector,asset-preview,asset-search,asset-bulk-bar,asset-overlays,asset-evidence,asset-versions}.test.tsx`, `web/src/pages/__tests__/backups-page.a11y.test.tsx`
- Modify: `web/src/router.tsx`, `web/src/router-pages.tsx`
- Modify: `web/src/components/layout/navigation.ts`, `web/src/components/layout/navigation.test.ts`
- Modify: `web/src/components/snapshot-browser.tsx`, `snapshot-browser.test.tsx`, `snapshot-search.tsx`, `task-run-detail.tsx`
- Modify: `web/src/pages/tasks-page.dialogs.tsx`, `tasks-page.test.tsx`
- Modify: `web/src/i18n/locales/zh.ts`, `web/src/i18n/locales/en.ts`

### Required route state

```ts
export type BackupAssetsRouteState = {
  view: "overview" | "data" | "recovery";
  repositoryId?: string;
  taskId?: number;
  recoveryPointId?: string;
  parentEntryId?: string;
  entryId?: string;
  savedSearchId?: number;
  scope: "current" | "all_retained";
  typeFilter?: string[];
  tagId?: number;
  favoriteOnly?: boolean;
  sort: "name" | "size" | "modified_at" | "type";
  direction: "asc" | "desc";
  layout: "list" | "grid";
  inspectorTab: "preview" | "metadata" | "versions" | "security";
};
```

- [ ] **Step 1: Write route-state utility tests.** Updating one tab/filter preserves unrelated URL params; composite current AssetRef, opaque parent entry, saved search, scope and non-sensitive filter/sort deep-link correctly. Raw query text, file paths, bulk-selection IDs, ticket/proof/reason never enter URL or storage.
- [ ] **Step 2: Write page state tests.** Desktop selection preserves context and scroll across preview; narrow flow returns preview→list→context without losing filters; aborted requests cannot overwrite a newer selection.
- [ ] **Step 3: Run frontend tests and confirm the current Backups page has no asset route/state.**

```bash
cd web && npm run test -- backups-page backup-assets
```

- [ ] **Step 4: Split Backups into nested overview/data/recovery routes.** `BackupsPage` provides PageHero/tab shell and Outlet/selected subview. Preserve one sidebar entry and redirect `/app/backups` to overview.
- [ ] **Step 5: Implement the three-column shell with existing primitives.** Left context tree, virtualized/paginated middle list/grid, right inspector; use `Tree`, `DataSurface`, `SearchInput`, `ViewModeToggle`, `Badge`, `LoadingState`, and `InlineAlert` before adding primitives.
- [ ] **Step 6: Connect repository/recovery-point/entry API hooks with exact unions.** RecoveryPoint maps `observed/retired/preparing/verifying/committed/degraded/expiring/expired/failed/purge_blocked`; retired heads render an inaccessible tombstone and never reuse stale entry routes. Catalog generation, coverage and staleness remain separate; sensitivity maps `secret/non_secret/unknown` and unknown fails closed. Unknown future codes render a safe localized fallback. Use typed wrappers, AbortController cleanup, explicit loading/empty/error/partial/offline states, and server-evaluated permissions.
- [ ] **Step 7: Implement core previews.** Escaped text, raster image, PDF/audio/video ticket URLs, metadata/hex fallback, previous/next navigation, ticket renewal and explicit Range/materialization fallback reasons.
- [ ] **Step 8: Implement versions, trust evidence and complete overlay interactions.** The inspector loads manifest/verification/drill evidence and exact two-point Catalog/Provider diff without upgrading trust. Left/middle/inspector flows support saved-search create/rename/delete/broken-scope, favorite toggle, tag CRUD/assignment, recent list/clear and quota/rate errors; all use server-evaluated permissions and composite AssetRef.
- [ ] **Step 9: Add compatibility links from Tasks.** Old snapshot search/browser results deep-link to the new exact Task/RP/entry context. Do not remove the old surface until Task 15.
- [ ] **Step 10: Add i18n and accessibility tests.** Cover tablist/tab/tabpanel, keyboard tree/list/grid, icon labels, focus return, reduced motion, color-independent status, version/evidence/diff, overlay CRUD/broken scope, and axe smoke including portals.
- [ ] **Step 11: Run the full frontend gate and commit.**

```bash
cd web && npm run check
cd ..
git add web/src/pages web/src/features/backup-assets web/src/router.tsx web/src/router-pages.tsx web/src/components web/src/i18n
git commit -m "feat: add backup asset explorer workspace"
```

**Rollback:** Hide the data/recovery subroutes through the feature gate and keep overview/legacy Tasks links. No Provider/Catalog data changes are required.

## 11. Child 10 — Worker Protocol And Derived Store

**Files:**

- Create: `backend/internal/model/backup_asset_processing.go`
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000065_backup_asset_processing.{up,down}.sql`
- Create: `backend/internal/backupasset/processing/{coordinator,protocol,grants,derived_store}.go`
- Create: `backend/internal/backupasset/processing/{coordinator,protocol,grants,derived_store}_test.go`
- Create: `backend/cmd/asset-worker/main.go`
- Create: `backend/internal/api/handlers/backup_worker_handler.go`, `backend/internal/api/handlers/backup_worker_handler_test.go`
- Modify: `backend/internal/api/router.go`, `backend/cmd/server/main.go`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`

### Required job envelope

```go
type JobEnvelope struct {
    JobID, WorkKey, FencingToken string
    Attempt                      int
    Capability, SchemaVersion    string
    OutputProfile                string
    CanonicalParameters          []byte
    PipelineFingerprint          string
    Source                       backupasset.AssetRef
    SourceFingerprint            string
    SecurityPolicyRevision       string
    Priority                     int
    LeaseExpiresAt, Deadline     time.Time
    Limits                       ResourceLimits
}

type ProcessingState string
type DerivedArtifactState string

const (
    ProcessingQueued          ProcessingState = "queued"
    ProcessingLeased         ProcessingState = "leased"
    ProcessingFetching       ProcessingState = "fetching"
    ProcessingMaterializing  ProcessingState = "materializing"
    ProcessingProcessing     ProcessingState = "processing"
    ProcessingUploading      ProcessingState = "uploading"
    ProcessingValidating     ProcessingState = "validating"
    ProcessingRetryWait      ProcessingState = "retry_wait"
    ProcessingCancelRequested ProcessingState = "cancel_requested"
    ProcessingCanceled       ProcessingState = "canceled"
    ProcessingSucceeded      ProcessingState = "succeeded"
    ProcessingFailed         ProcessingState = "failed"
    ProcessingSuperseded     ProcessingState = "superseded"
    ProcessingExpired        ProcessingState = "expired"

    DerivedActive      DerivedArtifactState = "active"
    DerivedStale       DerivedArtifactState = "stale"
    DerivedUnavailable DerivedArtifactState = "unavailable"
    DerivedSuperseded  DerivedArtifactState = "superseded"
    DerivedRevoked     DerivedArtifactState = "revoked"
    DerivedPurging     DerivedArtifactState = "purging"
    DerivedPurgeFailed DerivedArtifactState = "purge_failed"
)

type CapabilityHealth string
const (
    CapabilityReady    CapabilityHealth = "ready"
    CapabilityDegraded CapabilityHealth = "degraded"
    CapabilityDraining CapabilityHealth = "draining"
)

type CapabilityAdvertisement struct {
    ID, SchemaVersion, PipelineFingerprint string
    InputModes, MIMEPatterns, OutputProfiles []string
    Limits ResourceLimits
    Health CapabilityHealth
}
```

- [ ] **Step 1: Write coordinator/state tests.** Same work key (including output profile, canonical schema-validated parameters and security-policy revision) coalesces, while any thumbnail size/codec/page/quality/limit difference yields a distinct key. Interactive priority wins reserved slots, every ProcessingState transition/terminal/retry rule is explicit, expired leases retry with a new fence, old attempts cannot publish, final-interest cancellation stops work, and source expiry cancels grants and the `processing_job` RecoveryPoint lease.
- [ ] **Step 2: Write protocol/security tests.** Incompatible versions reject and Worker identity is required. Input/Sink activation secrets are one-use and job/attempt/worker-bound; the activated input session permits bounded multi-Range reads with atomic request/cumulative-byte/in-flight limits, and the sink session permits a bounded artifact set followed by one fenced manifest commit. No message contains Provider locator/credentials/path; security errors quarantine the Worker.
- [ ] **Step 3: Write derived state/crypto/projection-revocation tests.** Per-blob DEK/wrapped key, chunk tamper, every DerivedArtifactState transition, cross-RP reference counting, last-reference key destruction, KEK rewrap, stale pipeline supersession, and unreadable-derivative rebuild behavior. Revocation first atomically marks/removes content postings, excerpt reference, classification revision and field coverage, then destroys key/blob; no query can hit a ghost projection.
- [ ] **Step 4: Run tests and confirm the queue/protocol/store are absent.**

```bash
cd backend && go test ./internal/backupasset/processing -count=1
```

- [ ] **Step 5: Add processing/derived/updater-metadata schema, dual-engine fixtures and persistent coordinator.** Persist the exact ProcessingState/DerivedArtifactState enums, transition revision, stable error codes, RecoveryPoint lease reference, and generic signed bundle source/version/fingerprint/activation/failure records for Child 11's updater; never store updater credentials or raw tool output. Apply/down `000065` on SQLite and PostgreSQL.
- [ ] **Step 6: Implement pull lease, read/upload sessions and fencing.** Worker receives exact request/byte/range budgets from Content Broker, renews a `processing_job` RecoveryPoint lease, and uploads only through Artifact Sink; Xirang validates artifact manifest/digest/MIME/count/completeness/security-policy revision before one atomic publication.
- [ ] **Step 7: Implement independent Derived Store keyring and projection-safe revoke.** Do not reuse Search/Export/cache keys; encrypted excerpt blocks are references from search postings. Revoke/expiry/key-loss/rollback calls the Child 7 projection port first and commits `unavailable`/postings removal before key destruction; startup reconciliation removes orphan ciphertext/key rows and ghost projections in the same order.
- [ ] **Step 8: Add local/mTLS Worker transport and health API.** Default remote trust is disabled. Handler routes are internal/admin, feature-gated, rate/body limited, and sanitized.
- [ ] **Step 9: Add a protocol-only worker binary with fake/no-op test capability.** It proves lease/heartbeat/cancel/upload and graceful shutdown without bundling heavy tools into the core.
- [ ] **Step 10: Wire coordinator/GC lifecycle and run gates.**

```bash
cd backend && go test ./internal/backupasset/processing ./internal/api/... -run 'Worker|Processing|Derived' -count=1
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration065' -count=1
cd .. && make backend-build && make backend-test
```

- [ ] **Step 11: Commit protocol foundation.**

```bash
git add backend/internal/model backend/internal/database/migrations backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/backupasset/processing backend/internal/api backend/internal/settings/service.go backend/internal/settings/service_test.go backend/cmd
git commit -m "feat: add backup asset worker protocol"
```

**Rollback:** Stop coordinator/Worker endpoints and revoke grants; atomically mark/remove all affected search projections and excerpt references; only then destroy derived key references and delete rebuildable jobs/artifacts. Core browse/content remains usable and search cannot retain ghost matches.

## 12. Child 11 — Worker Capabilities And Enhanced Preview

**Files:**

- Create: `backend/internal/backupasset/processing/capabilities/{image,text,document,malware,media,archive}.go`
- Create: `backend/internal/backupasset/processing/capabilities/secret.go`, `backend/internal/backupasset/processing/capabilities/secret_test.go`
- Create: `backend/internal/backupasset/processing/capabilities/{image,text,document,malware,media,archive}_test.go` and fixtures under `backend/internal/backupasset/processing/capabilities/testdata/`
- Create: `backend/internal/backupasset/processing/updater/{service,manifest,store}.go`, `backend/internal/backupasset/processing/updater/{service,manifest,store}_test.go`
- Create: `backend/cmd/asset-worker-updater/main.go`
- Create: `deploy/worker/Dockerfile`, `deploy/worker/entrypoint.sh`, `deploy/worker/seccomp.json`
- Create: `scripts/check-compose-config.sh`, `scripts/test-core-compose.sh`, `scripts/test-asset-worker.sh`
- Modify: `docker-compose.yml` with an optional profile only
- Modify: `.github/workflows/ci.yml` to build and scan the Worker on amd64/arm64 without publishing before GA
- Create: `web/src/features/backup-assets/processing-coverage-panel.tsx`, `web/src/features/backup-assets/processing-coverage-panel.test.tsx`
- Modify: `web/src/features/backup-assets/asset-preview.tsx`, `web/src/features/backup-assets/asset-preview.test.tsx`, `web/src/features/backup-assets/asset-inspector.tsx`, `web/src/features/backup-assets/asset-inspector.test.tsx`
- Modify: `web/src/lib/api/backup-assets-api.ts`, `web/src/lib/api/backup-assets-api.test.ts`
- Modify: `web/src/types/domain.ts`, `web/src/i18n/locales/zh.ts`, `web/src/i18n/locales/en.ts`
- Modify: `backend/internal/api/handlers/backup_worker_handler.go`, `backend/internal/api/handlers/backup_worker_handler_test.go`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `docs/deployment.md`, `docs/env-vars.md`, `docs/admin/backup-recovery.md`, `docs/admin/security.md`

- [ ] **Step 1: Write tool-runner and updater contract tests with fake executables/bundles.** Each capability enforces MIME/profile/input/output/page/pixel/duration/archive limits, disables network/external resources, captures bounded sanitized diagnostics, and maps tool outcomes to stable codes. The updater uses a distinct identity, allowlisted egress or signed offline import, verifies signatures/digests, atomically publishes content-addressed bundles, records versions/failures, and never grants network to parsing jobs.
- [ ] **Step 2: Add malicious fixture tests.** Include malformed image/PDF/media, active HTML/SVG, Office macro/external link metadata, archive traversal/symlink/device/bomb, encrypted archive, and malware-positive finding as a successful scan result.
- [ ] **Step 3: Run capability tests before implementing handlers.**

```bash
cd backend && go test ./internal/backupasset/processing/capabilities -count=1
```

- [ ] **Step 4: Implement capabilities.** Use passive outputs only: raster thumbnails, bounded text/OCR segments, `secret.classify` with policy revision/evidence coverage, Office/PDF render profiles, ClamAV-style finding metadata, media probe/transcode profiles, and archive index/member extraction. Worker never edits source content.
- [ ] **Step 5: Build the separate Worker image and updater command.** Worker runtime is non-root/read-only, drops capabilities, uses no-new-privileges, PID/memory/CPU limits, job tmpfs `noexec,nosuid,nodev`, no egress/DNS, and no core image tool additions. The same image may expose an updater entrypoint, but it runs with a distinct identity/network policy and writes only verified bundle storage that Workers mount read-only.
- [ ] **Step 6: Add optional Compose profile and clean-checkout validation without changing the official core port/image selector.** Worker trust credentials are explicit secrets; no insecure fallback. `check-compose-config.sh` creates a minimal ignored `.env` from non-secret `.env.deploy` defaults only when absent, records ownership, traps cleanup of only the file it created, and validates core-only plus Worker-profile config; it never overwrites/deletes a developer `.env`. `test-core-compose.sh` generates ephemeral valid JWT/data/metrics secrets, starts core **without** the Worker profile, waits for `/healthz`, captures sanitized diagnostics on failure, and always tears down only its own project/resources. `test-asset-worker.sh` separately exercises the Worker profile and sandbox contract.
- [ ] **Step 7: Implement bundle-fingerprint invalidation, final-coverage backfill and atomic search projection.** Verified updater bundle versions become part of pipeline fingerprint; atomic bundle activation marks affected artifacts stale and schedules quota-controlled reprocessing. Latest/interactive first, recent next, old history low priority, per Provider/capability pause/quotas. Dedup requires strong fingerprint + capability/schema + pipeline + output profile + security-policy revision. After validated text/OCR/classification publication, one fenced transaction updates the Derived reference, HMAC postings, sensitivity revision and field coverage; stale attempts cannot update either side. A convergence test proves all eligible retained points eventually reach a terminal coverage state without starving backup/restore slots.
- [ ] **Step 8: Add admin coverage/updater API/UI and extend asset preview/search.** Per Provider/Task/capability expose eligible/completed/partial/queued/failed/unsupported counts, backlog age, failure categories, ETA estimate and adjustable quotas/pause controls without path labels. Show active/pending bundle fingerprint, signature/source, last success/failure and offline-import action without exposing updater credentials. Asset UI shows source/derived/partial/queued/not-deployed/failed, pipeline/scan/classification freshness, coverage and exact fallback actions. Secret/malware gates remain server-enforced; archive inspect/member actions emit registered sanitized audits.
- [ ] **Step 9: Run UI a11y, Worker sandbox smoke and Docker/deployment validation.** The Worker image is built explicitly, starts non-root/read-only/network-off with tmpfs/resource limits, passes protocol health, and CI builds/scans amd64/arm64 without pushing. Public Docker Hub stable-semver publication remains disabled until Child 15 and continues to use GitHub Release as version source of truth.

```bash
cd web && npm run check
cd .. && make backend-test
./scripts/check-compose-config.sh
./scripts/test-core-compose.sh
./scripts/test-asset-worker.sh
docker build -f deploy/worker/Dockerfile -t xirang-worker:local .
make docker-build
bash scripts/check-doc-freshness.sh
```

- [ ] **Step 10: Commit enhanced capabilities.**

```bash
git add backend/internal/backupasset/processing backend/cmd/asset-worker-updater backend/internal/api/handlers/backup_worker_handler.go backend/internal/api/handlers/backup_worker_handler_test.go backend/internal/settings/service.go backend/internal/settings/service_test.go deploy docker-compose.yml .github/workflows/ci.yml scripts/check-compose-config.sh scripts/test-core-compose.sh scripts/test-asset-worker.sh web/src docs
git commit -m "feat: add optional backup content workers"
```

**Rollback:** Disable Worker trust/backfill and remove the optional profile. Encrypted derivatives are rebuildable; core native preview/download/recovery remains.

## 13. Child 12 — Durable Export And Restricted Archive Retrieval

**Files:**

- Create: `backend/internal/model/backup_asset_export.go`
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000066_backup_asset_export.{up,down}.sql`
- Create: `backend/internal/backupasset/export/{service,archive,crypto,worker}.go`
- Create: `backend/internal/backupasset/export/{service,archive,crypto,worker}_test.go`
- Create: `backend/internal/api/handlers/backup_export_handler.go`, `backend/internal/api/handlers/backup_export_handler_test.go`
- Modify: `backend/internal/api/router.go`, `backend/cmd/server/main.go`
- Modify: `backend/internal/api/handlers/step_up.go`, `backend/internal/api/handlers/step_up_test.go`, `backend/internal/api/handlers/credential_access_grant.go`, `backend/internal/api/handlers/credential_access_grant_test.go`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`
- Create: `web/src/lib/api/backup-exports-api.ts`, `web/src/lib/api/backup-exports-api.test.ts`
- Create: `web/src/features/backup-assets/export-job-panel.tsx`, `web/src/features/backup-assets/export-job-panel.test.tsx`
- Modify: `web/src/features/backup-assets/asset-bulk-bar.tsx`
- Modify: `web/src/types/domain.ts`, `web/src/i18n/locales/zh.ts`, `web/src/i18n/locales/en.ts`

### Required export state

```go
type ExportState string

const (
    ExportQueued        ExportState = "queued"
    ExportRunning       ExportState = "running"
    ExportRetryWait     ExportState = "retry_wait"
    ExportSealing       ExportState = "sealing"
    ExportReady         ExportState = "ready"
    ExportExpiring      ExportState = "expiring"
    ExportExpired       ExportState = "expired"
    ExportFailed        ExportState = "failed"
    ExportCancelRequested ExportState = "cancel_requested"
    ExportCanceled      ExportState = "canceled"
    ExportSourceExpired ExportState = "source_expired"
    ExportPurgeFailed   ExportState = "purge_failed"
)

type ExportLease struct {
    Attempt, FencingToken, LeaseOwner string
    LeaseExpiresAt, AbsoluteDeadline  time.Time
}
```

- [ ] **Step 1: Write selection-freeze tests.** Saved searches resolve once to exact RP/entry IDs; mutable-head fingerprint drift fails the affected item; a later new recovery point never changes the job selection.
- [ ] **Step 2: Write archive-path tests.** Absolute paths, `..`, NUL, platform-dangerous names, symlink escape and collisions normalize to deterministic safe members with a report; special files skip explicitly.
- [ ] **Step 3: Write encryption/restart/fencing tests.** Streaming chunks authenticate selection/export IDs; DEK is wrapped under versioned Export KEK; process restart or two-worker takeover allows only the current lease/fencing token to seal/publish, rejects late output from the old attempt, safely resumes/retries running work, and preserves ready artifacts. Wrong/missing key never yields plaintext.
- [ ] **Step 4: Write TTL/GC tests.** Ready TTL is absolute default 24h and capped by earliest source expiry; cancel/fail destroys key immediately; expiry revokes tickets before physical cleanup; orphan/purge failure states reconcile idempotently.
- [ ] **Step 5: Run tests and confirm the export package is absent.**

```bash
cd backend && go test ./internal/backupasset/export -count=1
```

- [ ] **Step 6: Add export/job/item schema, settings, dual-engine fixtures and persistent worker.** Persist attempt/lease owner/fencing/checkpoint, sanitized result/count/byte/error categories and a `export_job` RecoveryPoint lease per selected source; per-item paths/selections are encrypted and purged with artifact. Register Export KEK/root/quota/TTL/GC settings. The dedicated ciphertext root is outside `/data`, `/backup`, and `/logs`; durable container-volume wiring is a Child 15 readiness dependency. Apply/down `000066` on SQLite and PostgreSQL.
- [ ] **Step 7: Implement archive writer and Export keyring.** Default downloaded format is ZIP, optional TAR profile; server at-rest artifact is always encrypted; delivery decrypts only after fresh download authorization/ticket over TLS.
- [ ] **Step 8: Integrate archive inspect/member extraction.** Bind opaque member to outer composite AssetRef fingerprint/member chain; enforce Worker limits and inherit outer permissions/lifecycle. Encrypted/unsupported/bomb archives fail closed, and inspect/member retrieval emit registered sanitized `archive_member` events.
- [ ] **Step 9: Add create/status/cancel/download-ticket APIs and grants.** Admin-only export/download by default; creation and download each authorize with their exact purpose; audit selection digest/count/bytes, never raw member/path names. Cancel revokes source leases and invalidates all attempts before key deletion.
- [ ] **Step 10: Add bulk selection/export UI.** Review exact versions and estimated bytes, show per-item progress/failures, restart reconciliation, expiry countdown and fresh download step-up. Do not persist selection/reason/ticket.
- [ ] **Step 11: Run restart/security/full gates and commit.**

```bash
cd backend && go test ./internal/backupasset/export ./internal/api/... -run 'AssetExport|ArchiveMember' -count=1
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration066' -count=1
cd ../web && npm run check
cd .. && make backend-test
git add backend/internal/model backend/internal/database/migrations backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/backupasset/export backend/internal/api backend/internal/settings/service.go backend/internal/settings/service_test.go backend/cmd/server web/src
git commit -m "feat: add durable backup asset exports"
```

**Rollback:** Stop new exports, revoke tickets, destroy wrapped export DEKs, and clean ciphertext via reconciliation. Existing downloaded client archives cannot be recalled.

## 14. Child 13 — Controlled Recovery Plans And Jobs

**Files:**

- Create: `backend/internal/model/backup_asset_recovery.go`
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000067_backup_asset_recovery.{up,down}.sql`
- Create: `backend/internal/backupasset/recovery/{service,preflight,executor,worker}.go`
- Create: `backend/internal/backupasset/recovery/{service,preflight,executor,worker}_test.go`
- Create: `backend/internal/backupasset/recovery/result_lifecycle.go`, `backend/internal/backupasset/recovery/result_lifecycle_test.go`
- Create: `backend/internal/backupasset/recovery/testdata/{rsync_local_to_remote,restic_exact_snapshot,rclone_committed_prefix,target_preflight}.json`
- Modify: `backend/internal/backupasset/provider/provider.go`; create `backend/internal/backupasset/provider/restore.go`, `backend/internal/backupasset/provider/restore_test.go`
- Create: `backend/internal/api/handlers/backup_recovery_handler.go`, `backend/internal/api/handlers/backup_recovery_handler_test.go`
- Modify: `backend/internal/api/router.go`, `backend/cmd/server/main.go`
- Modify: `backend/internal/api/handlers/step_up.go`, `backend/internal/api/handlers/step_up_test.go`, `backend/internal/api/handlers/credential_access_grant.go`, `backend/internal/api/handlers/credential_access_grant_test.go`
- Modify: `backend/internal/backupasset/content/broker.go`, `backend/internal/backupasset/content/broker_test.go`, `backend/internal/backupasset/content/ticket.go`, `backend/internal/backupasset/content/ticket_test.go`
- Modify: `backend/internal/api/handlers/backup_content_handler.go`, `backend/internal/api/handlers/backup_content_handler_test.go`
- Modify: `backend/internal/sshutil/scope.go`, `backend/internal/sshutil/scope_test.go`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`
- Create: `web/src/lib/api/backup-recovery-api.ts`, `web/src/lib/api/backup-recovery-api.test.ts`
- Create: `web/src/features/backup-assets/recovery-plan-wizard.tsx`, `web/src/features/backup-assets/recovery-plan-wizard.test.tsx`
- Modify: `web/src/pages/backups-page.recovery.tsx`
- Modify: `web/src/features/backup-assets/asset-inspector.tsx`, `web/src/features/backup-assets/asset-inspector.test.tsx`
- Modify: `web/src/types/domain.ts`, `web/src/i18n/locales/zh.ts`, `web/src/i18n/locales/en.ts`

### Required plan binding

```go
type ConflictPolicy string
type SourceRevisionKind string

type ObservationRevision struct {
    SourceFingerprint  string
    CatalogGenerationID string
    ObservedAt         time.Time
}

type SourceRevision struct {
    Kind                SourceRevisionKind
    ImmutableLocator    string
    ManifestDigest      string
    MutableObservation  *ObservationRevision
}

const (
    ConflictFailOnConflict   ConflictPolicy = "fail_on_conflict"
    ConflictSkipExisting     ConflictPolicy = "skip_existing"
    ConflictOverwriteSelected ConflictPolicy = "overwrite_selected"
    ConflictExactMirror      ConflictPolicy = "exact_mirror"
)

type PlanBinding struct {
    PlanDigest, SelectionDigest string
    RepositoryID, RecoveryPointID string
    SourceRevision SourceRevision
    TargetNodeID uint
    TargetPath, TargetRevision string
    ConflictPolicy ConflictPolicy
    CapabilityRevision string
}

type RecoveryPlanState string
type RecoveryJobState string
type RecoveryResultSetState string

const (
    PlanDraft          RecoveryPlanState = "draft"
    PlanPreflightReady RecoveryPlanState = "preflight_ready"
    PlanAuthorized     RecoveryPlanState = "authorized"
    PlanSuperseded     RecoveryPlanState = "superseded"
    PlanExpired        RecoveryPlanState = "expired"
    PlanExecuted       RecoveryPlanState = "executed"
    PlanCanceled       RecoveryPlanState = "canceled"

    RecoveryQueued          RecoveryJobState = "queued"
    RecoveryRunning         RecoveryJobState = "running"
    RecoveryVerifying       RecoveryJobState = "verifying"
    RecoveryCancelRequested RecoveryJobState = "cancel_requested"
    RecoveryCanceled        RecoveryJobState = "canceled"
    RecoverySucceeded       RecoveryJobState = "succeeded"
    RecoveryDegraded        RecoveryJobState = "degraded"
    RecoveryNeedsAttention  RecoveryJobState = "needs_attention"
    RecoveryFailed          RecoveryJobState = "failed"

    ResultReady         RecoveryResultSetState = "ready"
    ResultRevoking      RecoveryResultSetState = "revoking"
    ResultCleaned       RecoveryResultSetState = "cleaned"
    ResultCleanupFailed RecoveryResultSetState = "cleanup_failed"
)
```

- [ ] **Step 1: Write plan-freeze/preflight/state/result-reference tests.** Exact RP/composite AssetRefs only; Restic `latest` forbidden; every plan/job/result-set transition, terminal/retry/restart outcome is explicit. RecoveryJob has no cleanup state: succeeded/degraded/failed remains unchanged while ResultSet alone moves ready→revoking→cleaned or cleanup_failed→revoking. Immutable binding includes exact locator + manifest digest; mutable binding requires source fingerprint + Catalog generation + observed_at, all selected entries belong to that generation, and a change before/after preflight, authorize, first write or resume supersedes/stops the plan. A race after partial target writes records the completed set and enters needs_attention. Default target is a new directory under a configured/probed **remote target-node** recovery root; dangerous/overlapping/symlink targets, insufficient space/inodes, offline source and node conflicts fail preflight. RecoveryResultRef resolves only an owned regular file or verification report inside the exact job tree and never accepts a path, symlink or special file. Ticket issuance/read requires `backup_assets:recover`, exact RecoveryJob ownership and only the `recovery.result_download` proof; `backup_assets:download` alone fails and is not required in addition to recover. Result-set tests cover cleanup failure/retry, retain hard cap and old-fence read rejection.
- [ ] **Step 2: Write conflict-policy tests.** Default conflict stops before writes; skip/overwrite affect selected entries only; exact mirror emits explicit delete plan and requires a distinct high-risk grant.
- [ ] **Step 3: Write authorization-drift tests.** Grant binds plan/selection/target/capability/source revisions/reason; for mutable heads the exact ObservationRevision is copied into the plan digest, grant and job checkpoint. Source change, target drift, policy change, malware finding or expired/wrong-purpose proof invalidates it before mutation; revalidation also runs after preflight/authorize/execute/resume boundaries so a TOCTOU race cannot hide behind the stable RecoveryPoint ID.
- [ ] **Step 4: Write Provider restore tests.** Rsync local repository uses an explicit local→remote path/stream; Restic restores exact full snapshot ID/includes; Rclone pulls exact committed prefix and never destructive-syncs an undeclared target.
- [ ] **Step 5: Run tests and confirm current direct handlers fail exact-plan semantics.**

```bash
cd backend && go test ./internal/backupasset/recovery -count=1
```

- [ ] **Step 6: Add plan/job/item/evidence/result schema, settings, dual-engine fixtures and services.** Preflight is read-only, versioned and expiring; execute requires Admin + fresh `asset.recover` proof + reason + plan-bound grant. Persist the typed immutable/observation source revision in plan, grant, job and checkpoints; persist exact state/fencing and a `recovery_job` RecoveryPoint lease for every source. Add RecoveryResultSet/Result rows with job FK, typed lifecycle, target node, opaque result IDs, created/absolute-expiry/bounded-retain deadlines, cleanup attempt/lease/fence and HMAC-owned-marker digest without raw client paths; activate Child 8 recovery-result delivery FKs/checks. Register remote safe roots, preflight validity, job concurrency, isolated-result TTL/retain cap and cleanup cadence; apply/down `000067` on SQLite and PostgreSQL.
- [ ] **Step 7: Implement default isolated recovery, result delivery and plaintext lifecycle.** Use configured `/var/tmp/xirang-recovery` only as the default remote-node root after its node probe passes; create an exact job child directory and atomically write an installation/job/root-revision/random-nonce marker authenticated by the Recovery Cleanup Ownership key before registering results. Restore, verify size/hash/fidelity, report differences, and expose regular-file/report download. Recovery-result download requires `backup_assets:recover`, exact RecoveryJob ownership, `recovery.result_download`, a typed ticket, restricted SSH purpose, existing Range/cumulative quotas/revocation and registered audit; `backup_assets:download` is neither sufficient nor additionally required. Directories fall back to ExportJob from the frozen source selection. Cleanup atomically enters revoking/increments fence, revokes grants/tickets and rejects retain/new reads, drains or closes old streams, revalidates safe root + non-symlink HMAC marker, deletes only the exact job directory, then writes a cleaned tombstone. Failure becomes cleanup_failed and retries the same idempotent sequence; unknown/invalid orphan markers are quarantined/alerted, never deleted.
- [ ] **Step 8: Implement explicit in-place phase, result retain/cleanup APIs and audit.** Display create/overwrite/delete/skip impact before grant; add a second checkpoint before exact-mirror deletes. Cancellation after writes is best effort and reports partial target state. Retain requires Admin + `backup_assets:recover` + exact job ownership + fresh `recovery.result_retain`, validates a new deadline within the hard cap, and never revives revoking/cleaned results. Manual cleanup requires the same RBAC/ownership plus explicit confirmation; automatic expiry uses a service identity. Plan/preflight/authorize/execute/cancel/verify/cleanup/retain use registered, sanitized recovery audit actions with composite source digests and no raw paths/reasons.
- [ ] **Step 9: Implement restart reconciliation.** Never blindly replay non-idempotent writes; takeover requires a new fence, renewed source lease and exact SourceRevision revalidation, resumes only from validated checkpoints, otherwise verifies and marks `needs_attention` with completed operations. Old attempts cannot mutate job state or target after takeover. Result reconciliation resumes revoking/cleanup_failed with a new cleanup fence, rejects old streams, and deletes only directories whose owned marker and job FK/tombstone reconcile; marker-key loss or mismatch fails closed for manual review.
- [ ] **Step 10: Add API/UI wizard, result-download/retain/cleanup and a11y tests.** Flow: selection→target→preflight→impact→reason/step-up→progress→verification→result download/retain/cleanup. Frontend unions keep RecoveryJob outcome (`queued/running/verifying/cancel_requested/canceled/succeeded/degraded/needs_attention/failed`) separate from RecoveryResultSet lifecycle (`ready/revoking/cleaned/cleanup_failed`), and cleanup failure never overwrites a succeeded/degraded job. Result download endpoint is `/recovery-jobs/:id/results/:resultId/download-ticket`; retain/cleanup endpoints expose typed state/deadline/conflict responses, enforce exact ownership and register audit coverage. Mutable-head source changes show a localized superseded/needs-attention path rather than silently retrying a new observation. Sensitive drafts/tickets/grants/proofs remain purpose-scoped and component-local unless the shared cache explicitly allows same-purpose reuse.
- [ ] **Step 11: Keep legacy restore routes gated until GA.** New UI uses only plan APIs. Add warnings and tests proving no accidental fallback to current `latest`/default-source behavior.
- [ ] **Step 12: Run full gates and commit.**

```bash
cd backend && go test ./internal/backupasset/recovery ./internal/api/... -run 'RecoveryPlan|RecoveryJob' -count=1
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration067' -count=1
cd ../web && npm run check
cd .. && make backend-test
git add backend/internal/model backend/internal/database/migrations backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/backupasset/recovery backend/internal/backupasset/provider backend/internal/backupasset/content backend/internal/sshutil backend/internal/api backend/internal/settings/service.go backend/internal/settings/service_test.go backend/cmd/server web/src
git commit -m "feat: add controlled backup asset recovery"
```

**Rollback:** Stop new plans/jobs and infrastructure writes, but keep the isolated-result TTL cleanup/orphan reconciler and ticket revocation path running until every existing plaintext result is cleaned or explicitly retained to a bounded deadline. Do not attempt generic automated rollback of in-place writes; preserve job evidence and require operator review.

## 15. Child 14 — Lifecycle, Reconnect, Retention And Purge

**Files:**

- Create: `backend/internal/model/backup_asset_lifecycle.go`
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000068_backup_asset_lifecycle.{up,down}.sql`
- Create: `backend/internal/backupasset/retention/{worker,reconcile}.go`
- Create: `backend/internal/backupasset/retention/{worker,reconcile}_test.go`
- Create: `backend/internal/backupasset/retention/{policy,hold}.go`, `backend/internal/backupasset/retention/{policy,hold}_test.go`
- Create: `backend/internal/api/handlers/backup_retention_handler.go`, `backend/internal/api/handlers/backup_retention_handler_test.go`
- Modify: `backend/internal/task/retention.go`, `backend/internal/task/retention_test.go`, `backend/internal/task/manager.go`, `backend/internal/task/manager_test.go`
- Modify: `backend/internal/api/handlers/task_handler.go`, `backend/internal/api/handlers/task_handler_test.go`
- Modify: `backend/internal/api/handlers/config_handler.go`, `backend/internal/api/handlers/config_handler_test.go`
- Modify: `backend/internal/api/handlers/backup_repository_handler.go`, `backend/internal/api/handlers/backup_repository_handler_test.go`, `backend/internal/api/handlers/backup_asset_handler.go`, `backend/internal/api/handlers/backup_asset_handler_test.go`
- Modify: `backend/internal/backupasset/provider/{provider,registry,rsync,restic,rclone}.go`, `backend/internal/backupasset/provider/{provider,registry,rsync,restic,rclone}_test.go`
- Modify: `backend/cmd/server/main.go`, `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`
- Modify: `web/src/lib/api/backup-repositories-api.ts`, `web/src/lib/api/backup-repositories-api.test.ts`
- Create: `web/src/features/backup-assets/repository-management-panel.tsx`, `web/src/features/backup-assets/repository-management-panel.test.tsx`
- Create: `web/src/features/backup-assets/retention-policy-panel.tsx`, `web/src/features/backup-assets/retention-policy-panel.test.tsx`
- Modify: `web/src/pages/backups-page.data.tsx`, `web/src/types/domain.ts`, `web/src/i18n/locales/zh.ts`, `web/src/i18n/locales/en.ts`
- Modify: `docs/admin/backup-recovery.md`, `docs/admin/security.md`, `docs/deployment.md`

- [ ] **Step 1: Write task/retention-policy/hold lifecycle tests.** Deleting a linked Task archives/stops schedule and preserves Repository/RPs; disconnect revokes access but preserves offline Catalog; no action implicitly purges Provider bytes. Versioned Repository/Task-link policies select exact immutable points deterministically. Mutable heads are excluded from time/count retention and holds: disconnect keeps the same observed/offline row; non-destructive cutover/withdraw revokes access and drains leases before observed→retired, stores typed reason/retired time, preserves the protected legacy locator and bytes, and cannot reactivate. Explicit physical purge from observed or retired uses expiring/expired; blocked deletion uses expiring→purge_blocked→expiring and never claims bytes are gone early. Operational/legal holds bind exact immutable points, require Admin reason/audit, expire/release explicitly, and never arise from favorites/tags.
- [ ] **Step 2: Write reconnect/import/rebuild tests.** Identity match rebinds; mismatch refuses overwrite. A non-retired mutable head reuses its stable ID and advances only on successful reconcile; failed reconcile preserves the prior generation with stale/availability metadata. A retired head never reactivates. Restic imports attributable snapshots, while unknowns persist in an Admin-only review queue/API/UI and never enter Operator counts. Xirang Rsync/Rclone require marker+every valid commit digest; arbitrary trees become only explicit mutable head/imported baseline. A verified baseline is a new immutable point, never a state mutation of the observed row. Accepted manifests rebuild all RecoveryPoints, start new Catalog generations and restore eligible derived backfill at low priority with complete/partial/failure reporting.
- [ ] **Step 3: Write expiry/lease/hold tests.** committed→expiring rejects new work and waits bounded ordinary leases. An active hold blocks normal expiry and Provider deletion; purge may revoke access but remains `purge_blocked` until a separately authorized hold release. Exact deletion is idempotent, and WORM/failure also yields `purge_blocked`, not expired. Prove ordinary retention never selects observed/retired rows; non-destructive mutable-head retirement rejects new tickets, drains all holder types, fences late publication and preserves Provider bytes/rollback locator; explicit purge from observed or retired repeats revoke/drain, reaches expired only after confirmed deletion, and retries purge_blocked without making the AssetRef readable.
- [ ] **Step 4: Write dependent cleanup and audit-retention tests.** Revoke tickets, cancel jobs/source leases, destroy derived/export keys, purge file-level Catalog/search/archive/recent, preserve safe RP tombstone and saved-search broken scope; favorites/tags retain opaque-only tombstones. Mutable-head retirement removes active generation/search postings and all content-bearing metadata before marking retired, retains only opaque tombstone/audit plus protected legacy rollback locator and truthful physical availability, and startup reconciliation rejects/removes late outputs. A later explicit purge removes that locator only after Provider deletion is confirmed and changes the point to expired. Close audit segments, retain chain checkpoints/anchors, prune only eligible detail segments, and prove remaining intra-/cross-segment verification. Legal purge cannot erase independent audit evidence except through its explicit policy.
- [ ] **Step 5: Run tests and confirm current path/age retention bypasses RP state.**

```bash
cd backend && go test ./internal/backupasset/retention ./internal/task -run 'Retention|Reconnect|TaskArchive|Purge' -count=1
```

- [ ] **Step 6: Add versioned retention-policy, RecoveryPointHold, lifecycle/tombstone schema, dual-engine fixtures and unified retention worker.** Reuse the Child 1 RecoveryPointLease contract rather than inventing retention-private leases. Store policy scope/rule revision and hold type/status/actor/encrypted-reason reference/timestamps; add typed mutable-head retirement reason/time and the retire coordinator with revoke/drain/fence/clear/tombstone/legacy-locator ordering, plus a distinct explicit-purge path that uses expiring/expired and purge_blocked retry semantics. Add observed/retired/reconcile metrics; use startup + periodic idempotent/batched prune, retention-worker lease/heartbeat/fencing and explicit `purge_failed/purge_blocked` telemetry; apply/down `000068` on SQLite and PostgreSQL.
- [ ] **Step 7: Refactor Task retention to select exact RecoveryPoint IDs.** Remove direct directory-mtime, broad Restic forget, and Rclone min-age decisions from the source of truth. Provider adapters execute exact point deletion after plan selection.
- [ ] **Step 8: Change Task delete to archive/unlink.** Schedule removal is immediate; metadata hard-delete waits until no repository/run/audit dependencies and copies required immutable lineage summary.
- [ ] **Step 9: Add retention-policy/hold and extend reconnect/disconnect/purge APIs/UI.** Policy CRUD and hold create/release require Admin, impact preview and typed audit actions; hold release additionally requires fresh proof + reason. Build on Child 2 access-binding endpoints with full import/rebuild/admin-review/purge workflows. Purge requires the dedicated Admin fresh proof, reason, impact counts, hold/lease/WORM status and exact scope; audit contains opaque IDs/counts only.
- [ ] **Step 10: Extend versioned config export/import tests and behavior.** Default `version: "2.0"` export includes non-secret repository identity/link metadata but omits access secrets; sensitive export includes reconnectable access bindings only behind the existing Admin + fresh config-export step-up + grant path. Stable export references never reuse source DB numeric IDs. Fresh-database round trips prove shared-repository relation remapping, repeated-import idempotency, `1.0` import compatibility and conflict failure; import creates disconnected bindings and performs no Provider mutation until explicit reconnect probe.
- [ ] **Step 11: Add DB disaster-recovery documentation/tests.** Reconnect rebuilds Provider facts/Catalog only; user overlays/audit require DB + `DATA_ENCRYPTION_KEY` restoration.
- [ ] **Step 12: Run retention/fault/full gates and commit.**

```bash
cd backend && go test ./internal/backupasset/retention ./internal/task ./internal/api/... -run 'Retention|RetentionPolicy|RecoveryPointHold|Reconnect|Purge|TaskArchive|AuditRetention|ExportGC|DerivedGC|SavedSearch|Favorite|Recent|Config(Import|Export)|DisasterRecovery' -count=1
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration068' -count=1
cd ../web && npm run check
cd .. && make backend-test
git add backend/internal/model backend/internal/database/migrations backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/backupasset/retention backend/internal/backupasset/provider backend/internal/task backend/internal/api backend/cmd/server backend/internal/settings web/src docs/admin/backup-recovery.md docs/admin/security.md docs/deployment.md
git commit -m "feat: govern backup asset lifecycle"
```

**Rollback:** Disable new purge/retention workers first. Archived Tasks can be re-enabled/relinked. Never restore deleted Provider bytes from Catalog; use backup/provider-native recovery if purge already completed.

## 16. Child 15 — GA Migration, Hardening, Docs And Legacy Removal

**Files:**

- Create: `backend/internal/model/backup_asset_migration.go`
- Create: `backend/internal/database/migrations/{sqlite,postgres}/000069_backup_asset_ga.{up,down}.sql` with persistent installation migration/readiness status and repository-conflict records
- Create: `scripts/check-backup-asset-migration.sh`
- Create: `scripts/test-backup-asset-load.sh`
- Modify: `backend/internal/settings/service.go`, `backend/internal/settings/service_test.go`
- Modify: `backend/internal/api/handlers/settings_handler.go`, `backend/internal/api/handlers/settings_handler_test.go`
- Modify: `backend/internal/api/router.go`, `backend/internal/api/handlers/snapshot_handler.go`, `backend/internal/api/handlers/snapshot_search_handler.go`, `backend/internal/api/handlers/snapshot_diff_handler.go`, `backend/internal/api/handlers/snapshot_search_handler_test.go`
- Create: `backend/internal/api/handlers/snapshot_handler_test.go`, `backend/internal/api/handlers/snapshot_diff_handler_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`
- Modify: `web/src/components/snapshot-browser.tsx`, `snapshot-browser.test.tsx`, `snapshot-search.tsx`, `restore-confirm-dialog.tsx`, `restore-confirm-dialog.test.tsx`
- Modify: `web/src/pages/tasks-page.dialogs.tsx`, `tasks-page.test.tsx`, `backups-page.tsx`, `backups-page.test.tsx`, `backups-page.overview.tsx`, `backups-page.data.tsx`, `backups-page.recovery.tsx`, `settings-page.system.tsx`, `settings-page.system.test.tsx`
- Modify: `web/src/lib/api/settings-api.ts`, `settings-api.test.ts`, `web/src/types/domain.ts`, `web/src/i18n/locales/zh.ts`, `web/src/i18n/locales/en.ts`
- Modify: `docker-compose.yml`, `.env.deploy`, `backend/.env.production.example`, `deploy/nginx/templates/default.conf.template`
- Modify: `deploy/worker/Dockerfile`, `deploy/worker/entrypoint.sh`, `deploy/worker/seccomp.json`
- Modify: `scripts/check-compose-config.sh`, `scripts/test-core-compose.sh`, `scripts/test-asset-worker.sh`
- Modify: `README.md`, `docs/deployment.md`, `docs/env-vars.md`, `docs/admin/backup-recovery.md`, `docs/admin/security.md`, `docs/maintainers/release.md`
- Modify: `.github/workflows/ci.yml`, `.github/workflows/publish-images.yml`, `.github/workflows/deploy.yml`, `.github/workflows/dockerhub-description.yml`

- [ ] **Step 1: Write migration inventory tests.** Existing Restic/Rsync/Rclone Tasks map to Repository candidates; legacy index is never trusted complete; shared Restic repositories consolidate identity without cross-task ownership; mutable mirrors remain mutable heads.
- [ ] **Step 2: Write feature-readiness tests.** Feature cannot enable unless dual-engine migrations, required key domains/paths, durable `/var/lib/xirang-asset-runtime` export/derived ciphertext volume, verified updater-bundle volume/permissions when Worker is enabled, repository migration status, retention-policy/hold enforcement, content route/log redaction, GC workers and RBAC routes pass. Worker absence is allowed and reported as optional.
- [ ] **Step 3: Write load/security test scripts.** Cover million-entry Catalog pagination/search, deep directories, Range seek, concurrent previews, background backfill isolation, restart during export/recovery, malformed content/archive bombs, ticket replay and audit redaction.
- [ ] **Step 4: Run migration dry-run and `000069` apply/down on SQLite and PostgreSQL fixtures.** The script prints counts/identity conflicts/capability gaps without changing Provider bytes; rerun is idempotent and the shared integration harness proves both engines.
- [ ] **Step 5: Enable the feature for new installs and expose existing-installation migration UI.** Existing installations remain gated until admin preflight/acknowledgment; no automatic Provider conversion.
- [ ] **Step 6: Remove legacy asset UX only after deep-link parity tests pass.** Tasks retains config/schedule/run logs and links into the new workspace. Legacy direct restore/search routes either redirect/deprecate safely or remain internal compatibility endpoints with no UI callers until the documented removal release.
- [ ] **Step 7: Complete settings/risk/observability and durable runtime wiring.** Persist the export/derived ciphertext root and verified updater bundles as separate dedicated volumes across container replacement, mount bundles read-only into Workers, keep preview cache ephemeral, and verify key/volume backup semantics. Metrics avoid path/entry labels; immutable retained/history/health counts exclude mutable heads, whose observed/retired counts, observation age and reconcile failures are separate. Alerts cover offline/degraded/backlog/updater/GC/quarantine/purge/recovery verification. “Worker not configured” remains info.
- [ ] **Step 8: Update public/maintainer docs from actual runtime.** Document Provider semantics, migration/rollback, cache/export/Worker volumes, external HTTPS, swap/tmpfs, key backup/rotation, permissions, preview matrix, archive limits and disaster reconnect. Do not publish planning artifacts.
- [ ] **Step 9: Regenerate artifacts and run all local gates.**

```bash
make swag-init
make check
./scripts/check-compose-config.sh
./scripts/test-core-compose.sh
./scripts/test-asset-worker.sh
docker build -f deploy/worker/Dockerfile -t xirang-worker:local .
make docker-build
bash scripts/check-doc-freshness.sh
bash scripts/check-backup-asset-migration.sh
cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration069' -count=1
cd ..
git diff --check
```

Expected: all commands PASS; no stale generated Swagger/docs, migrations, a11y, bundle, or Docker contract failures.

- [ ] **Step 10: Perform manual security/UX checks.** Use Admin/Operator/Viewer, offline repository, no Worker, disabled cache, no Range, secret step-up, malware finding, narrow screen, keyboard-only and restart scenarios. Record results in the child task, not public docs.
- [ ] **Step 11: Commit GA hardening.**

```bash
git add backend/internal/model/backup_asset_migration.go backend/internal/database/migrations/sqlite/000069_backup_asset_ga.up.sql backend/internal/database/migrations/sqlite/000069_backup_asset_ga.down.sql backend/internal/database/migrations/postgres/000069_backup_asset_ga.up.sql backend/internal/database/migrations/postgres/000069_backup_asset_ga.down.sql backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/settings/service.go backend/internal/settings/service_test.go backend/internal/api/router.go backend/internal/api/handlers/settings_handler.go backend/internal/api/handlers/settings_handler_test.go backend/internal/api/handlers/snapshot_handler.go backend/internal/api/handlers/snapshot_handler_test.go backend/internal/api/handlers/snapshot_search_handler.go backend/internal/api/handlers/snapshot_search_handler_test.go backend/internal/api/handlers/snapshot_diff_handler.go backend/internal/api/handlers/snapshot_diff_handler_test.go backend/internal/api/docs/docs.go
git add web/src/components/snapshot-browser.tsx web/src/components/snapshot-browser.test.tsx web/src/components/snapshot-search.tsx web/src/components/restore-confirm-dialog.tsx web/src/components/restore-confirm-dialog.test.tsx web/src/pages/tasks-page.dialogs.tsx web/src/pages/tasks-page.test.tsx web/src/pages/backups-page.tsx web/src/pages/backups-page.test.tsx web/src/pages/backups-page.overview.tsx web/src/pages/backups-page.data.tsx web/src/pages/backups-page.recovery.tsx web/src/pages/settings-page.system.tsx web/src/pages/settings-page.system.test.tsx web/src/lib/api/settings-api.ts web/src/lib/api/settings-api.test.ts web/src/types/domain.ts web/src/i18n/locales/zh.ts web/src/i18n/locales/en.ts
git add docker-compose.yml .env.deploy backend/.env.production.example deploy/nginx/templates/default.conf.template deploy/worker/Dockerfile deploy/worker/entrypoint.sh deploy/worker/seccomp.json scripts/check-backup-asset-migration.sh scripts/test-backup-asset-load.sh scripts/check-compose-config.sh scripts/test-core-compose.sh scripts/test-asset-worker.sh
git add README.md docs/deployment.md docs/env-vars.md docs/admin/backup-recovery.md docs/admin/security.md docs/maintainers/release.md .github/workflows/ci.yml .github/workflows/publish-images.yml .github/workflows/deploy.yml .github/workflows/dockerhub-description.yml
git commit -m "feat: complete backup asset explorer"
```

- [ ] **Step 12: Push, open a PR, and monitor every required CI job.** Fix failures on the same branch and continue monitoring until all required checks pass or a real external blocker is documented.
- [ ] **Step 13: After squash merge, monitor Release Please, any auto release, Publish Docker Images (including Worker platforms if added), and Sync Docker Hub Description when docs changed.** Record explicitly whether a GitHub Release/Docker publish was expected and what occurred; sync local `main` only after post-merge automation is understood.

**Rollback:** Set `backup_assets.enabled=false`; stop new Worker/backfill/content/export/recovery work, but keep revoke-first GC and isolated-result TTL/orphan cleanup running until protected artifacts/plaintext are gone or explicitly retained. Keep additive schema and committed Provider points, and restore legacy navigation only if its routes remain safe. Never mass-delete new Provider data as an application rollback.

## 17. Requirement Coverage Matrix

| PRD/design requirement | Owning child tasks | Required proof before GA |
|---|---|---|
| Independent Repository + RecoveryPoint lineage | 1–6, 14 | paired schema, Provider identity/commit fixtures, reconnect tests |
| Rsync/Restic/Rclone truthful versions | 3–5 | shared Restic attribution, hard-link immutability, Rclone commit consistency |
| Command task remains unsupported without artifact contract | 1, 6, 15 | capability/API/UI fixtures expose a stable unsupported reason and no invented assets |
| Legacy mutable compatibility/migration | 3–6, 14–15 | no fake history, dry-run/cutover/rollback fixtures |
| Mutable-head reconcile/retirement lifecycle | 1–2, 6, 9, 14–15 | stable singleton ID, offline/stale refresh, retention exclusion, non-destructive typed retirement with rollback locator, distinct confirmed physical purge, purge-blocked retry and separate metrics tests |
| Catalog browse/offline/staleness | 6 | atomic generation/empty tree/offline API tests |
| Recovery-point evidence and exact version diff | 6, 9 | manifest/verification/drill aggregation and two-point Catalog/Provider diff tests |
| Global current/all-history search | 7 | cross-engine normalization/order/cursor/coverage tests |
| Saved search/favorite/tag/recent | 7, 9, 14 | quota/audit/UI ownership and expiry/tombstone/broken-scope tests |
| Native text/image/PDF/audio/video preview | 8–9 | ticket/Range/renderer/CSP/a11y tests |
| Optional full derived coverage and search projection | 7, 10–11 | Worker lease/fencing/sandbox/dedup/backfill/atomic-ingest/admin-coverage tests |
| Signed Worker data/model updater | 11, 15 | distinct identity/allowlist/offline import/signature/atomic bundle/fingerprint-stale and deployment tests |
| Secret/malware/active-content protections | 7–11 | Core+Worker classification, unknown fail-closed, suggestion exclusion, finding, sanitizer and malformed fixture tests |
| Durable encrypted batch export | 12, 15 | restart/takeover/fencing/key/TTL/path/partial-result and persistent-volume tests |
| Restricted archive browse/member retrieval | 11–12 | traversal/bomb/encrypted/special-file tests |
| Controlled isolated/in-place recovery and result delivery | 8, 13 | exact immutable/ObservationRevision binding, source lease/race recheck, conflict/checkpoint, typed result ticket/Range/step-up/audit, result-set FK/state/fence/HMAC-owned-marker/retain/cleanup and verification tests |
| Task archive/repository reconnect/purge | 14 | non-destructive Task delete, identity match, exact purge/blocked tests |
| Versioned retention policy and explicit holds | 14–15 | deterministic point selection, hold create/release/audit, expiry/purge-blocked and GA readiness tests |
| Reconnect full rebuild and config round trip | 14–15 | all manifests/RPs/Catalog/backfill/admin review plus versioned cross-instance identity remap tests |
| Admin/Operator/Viewer and ownership | 1–15 | full-router RBAC matrix and no-existence-leak tests |
| Independent asset audit/hash chain/retention | 1–2, 6–15 | action registry, keyed fingerprints, segmented checkpoints, route action/redaction and retention verification |
| Purpose-bound step-up proof | 1, 8, 12–15 | allowlisted claim, exact signer/verifier/frontend propagation, complete pairwise cross-purpose and router coverage tests |
| Domain key separation and disaster impact | 1, 7–8, 10, 12–15 | stable identity/rotation/rewrap/loss/rebuild/export-readability/recovery-cleanup-marker tests and DR docs |
| RecoveryPoint leases across all readers/jobs | 1, 4, 6, 8, 10, 12–14 | holder acquire/renew/release/takeover/fence and expiry/purge tests |
| SQLite/PostgreSQL parity | 1, 7–8, 10, 12–15 | ordered paired migrations plus real apply/down and cross-engine behavior tests |
| All-in-one independent of Worker | 8, 10–11, 15 | core-only Compose/Docker smoke and no-Worker UI states |
| Deep-link/responsive/a11y/i18n | 9, 11–13, 15 | router/state/axe/keyboard/zh+en tests |
| Operations/docs/release truth | 11, 15 | Compose config, Docker build, doc freshness, CI/post-merge monitoring |

No requirement may be marked complete because a later child is “planned”; each child PR records its own checked acceptance evidence, and Task 15 reruns the cross-child proofs.

## 18. Global Validation Gates

### Per-child minimum

1. Run the focused red test before implementation and capture the expected failure category.
2. Implement only the child's bounded contract behind the feature gate.
3. Run focused package/component tests, then the affected layer gate.
4. Run `git diff --check` and inspect changed generated/config files.
5. Commit one coherent child deliverable and open a PR; do not stack unreviewed child branches.
6. Monitor all required CI and fix failures on the same branch.

### Cross-layer checkpoints

After Child 6:

```bash
make backend-test
make swag-init
git diff --check
```

Expected: all Provider/Catalog backend tests pass; no frontend feature is public.

After Child 9:

```bash
make backend-test
cd web && npm run check
```

Expected: core-only read/browse/preview works behind the gate with no Worker.

After Child 13:

```bash
make check
```

Expected: export/recovery cross-layer types, RBAC, step-up/grants, frontend and builds pass.

GA gate (Child 15):

```bash
make swag-init
make check
./scripts/check-compose-config.sh
./scripts/test-core-compose.sh
./scripts/test-asset-worker.sh
docker build -f deploy/worker/Dockerfile -t xirang-worker:local .
make docker-build
bash scripts/check-doc-freshness.sh
bash scripts/check-backup-asset-migration.sh
cd backend && TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration' -count=1
cd ..
git diff --check
```

Expected: all commands PASS with the feature enabled in the GA test configuration and disabled/default-compatible in migration fixtures.

## 19. High-Risk Review Gates

The responsible child must pause for a focused design/code review before merging these boundaries:

| Boundary | Required review evidence |
|---|---|
| Provider publication | exact locator, staging visibility, dual-write reconciliation, fidelity claims, no destructive rollback |
| Composite asset identity | every API/overlay/job/ticket carries RecoveryPoint ID + entry ID; no entry-only lookup or cross-point confusion |
| Domain keyring | Entry Identity stability, cursor overlap, key-domain separation, rewrap/loss behavior and DR ownership |
| RecoveryPoint leases | every holder acquires/renews/releases, takeover fencing, absolute deadline and purge override |
| Restic shared repository | run-created full snapshot ID/tag evidence and ownership isolation |
| Rsync hard links | mount/link probe, no inplace/metadata mutation, inode/link limits |
| Rclone commit | remote consistency gate, unique prefix, marker-last publication, external writer fence |
| Search crypto | normalization parity, token-key wrapping/rotation, secret exclusion before counts/snippets |
| Ticket cookie/content route | no bearer URL, session/action/path scope, Range, replay/revocation, log redaction, HTTPS behavior |
| Chunk/derived/export encryption | nonce/AD design, key separation, tamper behavior, cryptographic deletion, restart reconciliation |
| Worker sandbox | no repository credentials/network, tmpfs/swap, quotas, fencing, malicious fixture outcomes |
| Worker grant sessions | one-use activation, bounded multi-Range input, multi-output sink manifest, cumulative budgets and atomic fence |
| Recovery in-place/exact mirror | plan/target revision binding, impact list, fresh grant, checkpoint/cancel partial-state reporting |
| Purge/retention | revoke-first, exact point deletion, leases/WORM, `purge_blocked`, tombstone/overlay cleanup |
| Asset audit retention | keyed low-entropy fingerprints, route/action coverage, closed segments, checkpoint continuity and legal-retention boundaries |
| Ordered dual-engine migrations | no skipped version, SQLite/PostgreSQL apply/down evidence, clean upgrade and forward-repair strategy |

These reviews may request design amendments. Amend `design.md` and the affected child PRD/plan before implementation continues; do not hide deviations in code comments.

## 20. Program Rollback Strategy

1. **Downgrade fence:** Before starting any old binary, pause/fence every migrated Task, stop publication workers, and relink each Task to its preserved legacy locator/config. Unknown publication schema must fail closed. Merely disabling the UI gate is insufficient.
2. **Application rollback:** Disable `backup_assets.enabled` and stop new workers/routes after the downgrade fence. Additive tables and committed new points remain; only Tasks proven to use restored legacy semantics may resume on an old runtime.
3. **Read-plane rollback:** Revoke tickets/grants, release/expire source leases, discard process cache, stop Catalog/search/Worker queues. Provider data is untouched.
4. **Derived/export rollback:** First mark/remove derived search postings, excerpt references, classification revisions and field coverage; then destroy wrapped keys and delete ciphertext with idempotent GC. Export tickets are revoked before Export key destruction; source recovery points remain.
5. **Provider migration rollback:** Switch TaskRepositoryLink to the preserved legacy target/prefix. Already committed new points stay read-only; never delete them merely to revert application code.
6. **Recovery rollback:** Stop future jobs and preserve exact evidence of any partial target writes. Generic automatic infrastructure rollback is not promised; isolated plaintext cleanup continues under its owner.
7. **Schema rollback:** Down migrations are allowed only before rows are used. After data exists, roll back application behavior while retaining additive schema, then write an explicit forward repair migration.
8. **Release rollback:** If a merged release fails, publish a forward fix through the same PR/CI/release process; do not mutate stable GitHub/Docker tags in place.

## 21. Implementation-Plan Self-Review Checklist

- [x] Every `prd.md` requirement maps to at least one child in Section 17.
- [x] Every new persistent entity has paired SQLite/PostgreSQL migrations and a lifecycle owner.
- [x] Every Provider mutation has dry-run/preflight, exact locator, idempotency/fencing and rollback behavior.
- [x] Every content-bearing path states permission, ownership, step-up, audit, encryption, quota and cleanup behavior.
- [x] Frontend raw DTOs are private to API modules and all user strings are localized.
- [x] Core all-in-one remains useful with Worker missing, cache disabled, no Range or repository offline.
- [x] Legacy behavior remains reachable only until exact new-path parity is tested; no fake history or silent migration.
- [x] No child starts from `main`, bypasses PR/CI monitoring, or declares completion before required post-merge automation is understood.

## 22. Final Planning Gate

This file defines future execution but authorizes none of it. Before any child command or `task.py start`:

1. user reviews `prd.md`, `design.md`, and this `implement.md`;
2. requested changes are applied and self-review rerun;
3. user explicitly approves creation/start of the first child task;
4. the parent remains a planning/integration tracker rather than an implementation target;
5. the first child loads `trellis-before-dev`, the relevant `.trellis/spec/` files, and `superpowers:test-driven-development` before product code changes.

Planning review status: the user selected A on 2026-07-13 and approved the complete planning package, satisfying items 1–2. This approval closes the design round only. Item 3 remains an explicit future gate: no implementation child may be created or started until the user separately asks to begin implementation.
