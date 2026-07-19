# Child 10 Current-Main Evidence

## 0. Evidence boundary

This file records read-only Phase 1 research for
`.trellis/tasks/07-19-backup-assets-worker-protocol`. It is evidence for the
focused `prd.md`, `design.md`, and `implement.md`; it is not implementation or
test evidence.

```text
evidence date:            2026-07-19 Asia/Shanghai
worktree:                 /home/murray/.codex/worktrees/8f0f/xirang
branch:                   codex/backup-assets-worker-protocol
required baseline:        2ce71339b7f10fe759c0009ff01a100e589a700c
HEAD:                     2ce71339b7f10fe759c0009ff01a100e589a700c
main:                     2ce71339b7f10fe759c0009ff01a100e589a700c
origin/main:              2ce71339b7f10fe759c0009ff01a100e589a700c
task status:              planning
parent status:            planning
delivered program state:  9/15
```

No live Worker, PostgreSQL integration, product test, CI, release or deployment
command was run for this evidence. A source inspection result below is not a
claim that the future Child 10 behavior passes.

## 1. Baseline, identity, branch and task creation

### 1.1 Git baseline

The worktree started clean at the required merged Child 9 baseline. The
following read-only synchronization/check sequence was executed before tracked
file changes:

```bash
git status --short --branch
git rev-parse HEAD
git rev-parse main
git rev-parse origin/main
git fetch origin --prune
git rev-parse HEAD
git rev-parse main
git rev-parse origin/main
```

`git fetch origin --prune` succeeded. All three revisions remained
`2ce71339b7f10fe759c0009ff01a100e589a700c`; there was no network or `.git`
permission blocker to report. The dedicated branch was then created/switched:

```bash
git switch -c codex/backup-assets-worker-protocol
```

No commit, push, PR or other remote mutation followed.

### 1.2 Local Trellis developer identity

The shared developer is `weibo`. A Codex worktree does not inherit the ignored
`.trellis/.developer`, so the controller-authorized local restoration ran once:

```bash
python3 ./.trellis/scripts/init_developer.py weibo
```

Current proof:

```text
.trellis/.developer:
  name=weibo
.trellis/.gitignore match:
  .developer
shared journal:
  .trellis/workspace/weibo/journal-1.md
```

The identity file is ignored and absent from the tracked diff. This was local
session restoration, not a Trellis/product scope change.

### 1.3 Child registration

The authorized child creation command ran exactly once:

```bash
python3 ./.trellis/scripts/task.py create "备份资产 Worker 协议" \
  --slug backup-assets-worker-protocol \
  --parent 07-12-backup-data-explorer-design
```

Evidence in tracked metadata:

- `.trellis/tasks/07-19-backup-assets-worker-protocol/task.json` has
  `status=planning`, `parent=07-12-backup-data-explorer-design`,
  `branch=codex/backup-assets-worker-protocol`, and `base_branch=main`.
- `.trellis/tasks/07-12-backup-data-explorer-design/task.json` remains
  `status=planning` and adds exactly
  `07-19-backup-assets-worker-protocol` as its tenth instantiated child.
- The parent's ten children are a Trellis instantiation count. Only Children
  1-9 are delivered, so real program delivery remains 9/15.
- `task.py start` was not run.

## 2. Parent and delivered-child contract evidence

### 2.1 Parent tracker

Read in full:

```text
.trellis/tasks/07-12-backup-data-explorer-design/prd.md
.trellis/tasks/07-12-backup-data-explorer-design/design.md
.trellis/tasks/07-12-backup-data-explorer-design/implement.md
```

Focused review covered parent design §§7, 16, 18 and 21 and implement §11.
The parent reserves Child 10 for Worker protocol/Derived Store, but it is a
planning program tracker rather than an implementation target.

Two focused corrections/expansions are required by merged main:

1. Parent implement §11 line 977 selects `BackupAssetMigration066`; that is a
   historical typo because Child 10 owns migration 067. The focused plan uses
   `BackupAssetMigration067` everywhere.
2. Parent §11's coarse file/staging list omits the merged Content Broker seam,
   keyring domain, shared runtime, private-root validation, generated Swagger,
   router/RBAC tests and PostgreSQL CI selector. `implement.md` replaces the
   broad directory adds with an exact path manifest.

The parent's state constants already list `fetching` and `materializing`
separately. The focused transition matrix preserves them as independent closed
states and does not collapse them into one stage.

### 2.2 Child 7 — Search projection port

Reviewed archived planning/delivery evidence under:

```text
.trellis/tasks/archive/2026-07/07-18-backup-assets-search-overlays/
```

Merged source proof:

- `backend/internal/backupasset/search/ingest.go` defines the narrow
  `ContentIndexIngest` interface with only `PublishContentProjection` and
  `RevokeContentProjection`.
- Both requests carry composite `AssetRef`, source fingerprint, exact Catalog
  and Search generation IDs, a `processing_job` RecoveryPoint lease ID,
  attempt ID, fence token and monotonic projection revisions.
- `RevokeContentProjection` runs inside one GORM transaction, validates the
  current lease/fence, deletes the selected content/OCR postings, clears
  `excerpt_ref`, changes field coverage to `unavailable`, advances coverage,
  pipeline and index revisions, and advances the generation projection
  revision.
- `lockAndValidate` requires the lease holder type to equal
  `LeaseHolderProcessingJob`; it also rechecks source, active Catalog/Search
  generations, classification CAS and active Search key version.

Therefore Child 10 must call this port before Derived reference/key/blob
destruction. It must not directly mutate Search tables or weaken the existing
CAS/fence contract.

### 2.3 Child 8 — Content Broker, source seam, leases and keyring

Reviewed archived planning/delivery evidence under:

```text
.trellis/tasks/archive/2026-07/07-18-backup-assets-content-plane/
```

Merged source proof:

- `backend/internal/backupasset/content/source_contracts.go` defines closed
  `stat`, `sequential` and `range` modes, exact `AssetRef` plus Catalog/source/
  entry fingerprint binding, bounded requests, `SourceSession.Revalidate`, and
  a `SourceReader.ProviderBytes` meter.
- `backend/internal/backupasset/content/broker.go` is the current authorized
  delivery plane. It reserves request/byte budgets before opening sources,
  supports Range/fallback behavior, revalidates mutable sources, closes
  readers and reconciles conservative accounting.
- The public Broker shape is intentionally user-delivery-specific: ticket,
  cookie, subject/session and HTTP response behavior are not a Worker grant.
  Child 10 therefore needs a narrow attempt-bound facet inside `content`, not a
  reuse of the public ticket cookie and not direct Repository access.
- `backend/internal/backupasset/repository/content_read.go` implements the
  source resolver. Provider locator, binding/config, admission token and native
  reader stay sealed in Repository. Command Provider fails with typed
  `task_artifact_contract_missing`; Child 10 does not alter that result.
- The same Repository file currently validates `content_cache_root` against
  `/data`, `/backup`, `/logs`, Task rsync roots and stored Repository bindings.
  Child 10 must generalize that proof for a distinct Derived private root
  instead of copying a weaker path check into Processing.
- `backend/internal/backupasset/domain.go` already includes
  `LeaseHolderProcessingJob` in the closed lease-holder set.
- `backend/internal/backupasset/lease.go` already implements transactional
  acquire, renew, release, stale takeover with a new attempt/fence, absolute
  deadlines, expiry reconciliation and `ValidateFenceTx`. No 062 holder CHECK
  edit is needed.
- `backend/internal/backupasset/keyring.go` currently knows entry identity,
  cursor signing, audit fingerprint, recovery cleanup ownership and Search
  token domains. It has no `derived_store` domain. Search token is rebuildable
  but intentionally not boot-required; Derived must gain its own similarly
  optional lifecycle without being added to unconditional
  `RequiredKeyDomains`.

### 2.4 Child 9 — UI and bundle reality

Reviewed archived planning/delivery evidence under:

```text
.trellis/tasks/archive/2026-07/07-19-backup-assets-workspace-ui/
```

The recorded fresh Child 9 bundle result is main JavaScript
`498.09/500.00 KiB` and CSS `104.21/105.00 KiB`, leaving only 1.91 KiB and
0.79 KiB. The merged workspace already renders future enhancement states as
unavailable/not deployed. Child 10 has no frontend route, API client, i18n or
component file in its manifest; a future UI boundary must be lazy, but the
default focused plan adds none.

## 3. Schema and migration evidence

### 3.1 Current migration head

Read-only enumeration found paired SQLite/PostgreSQL files through:

```text
backend/internal/database/migrations/sqlite/000066_backup_asset_content.{up,down}.sql
backend/internal/database/migrations/postgres/000066_backup_asset_content.{up,down}.sql
```

No `000067`, `000068`, `000069`, `000070` or `000071` migration file exists on
the inspected main. Child 10 therefore owns exactly:

```text
backend/internal/database/migrations/sqlite/000067_backup_asset_processing.up.sql
backend/internal/database/migrations/sqlite/000067_backup_asset_processing.down.sql
backend/internal/database/migrations/postgres/000067_backup_asset_processing.up.sql
backend/internal/database/migrations/postgres/000067_backup_asset_processing.down.sql
```

Versions 068-071 remain reserved and untouched.

### 3.2 Discovery, parity and real PostgreSQL fixture

- `backend/internal/database/migrator.go` embeds
  `migrations/sqlite/*.sql` and `migrations/postgres/*.sql`. There is no manual
  version registry to edit.
- `backend/internal/database/backup_asset_migrations_integration_test.go`
  exposes paired SQLite/PostgreSQL entry points for 062-066. Its required
  PostgreSQL fixture fails when `TEST_POSTGRES_DSN` is absent rather than
  silently marking a required test successful.
- Existing migration checks cover columns/indexes/CHECKs/FKs, UTC/no database
  timestamp defaults, Go model parity, blocked down and preservation of the
  preceding child schema. Child 10 must extend the same file for 067.
- `.github/workflows/ci.yml` uses PostgreSQL 18 and currently selects only
  `TestBackupAssetMigration0(62|63|64|65|66)Postgres` plus Catalog, Search,
  Overlay and Content PostgreSQL behavior. It must add 067 and Processing
  behavior parity; it must not add a Worker image/publish job in Child 10.

## 4. Composition, settings and API evidence

### 4.1 Unique composition root

`backend/internal/backupasset/runtime/runtime.go` constructs the shared
FoundationService, Keyring, LeaseService-backed services, Repository, Search
ingest and Content Broker graph. It exposes narrow accessors including
`ContentIndexIngest()` and `ContentBroker()`. Startup/shutdown and feature
transitions also live in this runtime.

Child 10 must extend this graph. Constructing a second DB, Keyring, LeaseService,
Repository/Provider registry or Content source resolver in `cmd/server` would
split admission/fence/key/lifecycle truth and contradict current main.

### 4.2 Settings snapshot

- `backend/internal/settings/service.go` registers
  `backup_assets.enabled` with default `false` and returns one atomically
  validated `BackupAssetSettingsSnapshot`.
- `backend/internal/backupasset/service.go` is the typed configuration boundary
  used by FoundationService; Content, Search and Overlay already parse from
  that snapshot rather than independent `GetEffective` calls.
- Child 10's queue/quota/TTL settings must extend both files and tests. Static
  Worker socket/mTLS material and Derived root/chunk format are
  `RequiresRestart`; trust/key paths are sensitive. The feature default remains
  false and enabling backup assets does not implicitly enable a Worker
  listener.

### 4.3 Public router and generated docs

- `backend/internal/api/router.go` receives the shared backup Runtime in its
  dependency graph and mounts existing backup-asset routes under authenticated
  `/api/v1` groups. `backend/internal/api/backup_asset_rbac_test.go` is the
  cross-role proof for Admin/Operator/Viewer routes.
- `backend/internal/api/docs/docs.go` is the only generated Swagger artifact in
  this repository.
- `backend/cmd/server/main.go` constructs the single backup Runtime before the
  public router and owns process startup/shutdown. A dedicated Worker listener
  must be wired here from runtime ports but must not be mounted on the browser
  CORS surface.
- The focused public addition is only sanitized
  `GET /api/v1/admin/backup-asset-processing`. Internal protocol routes derive
  identity from UDS peer credentials or verified mTLS, use strict body/rate
  ceilings, and never accept public JWT/header identity as Worker trust.

## 5. Derived contract gaps proven by current main

The following capabilities are absent rather than partially implemented:

- no `backend/internal/backupasset/processing` package;
- no `backend/cmd/asset-worker` binary;
- no persistent processing job/interest/attempt/grant/upload tables;
- no Worker identity/capability registry or dedicated internal listener;
- no Derived artifact/blob/reference schema, encrypted store or
  `derived_store` KeyDomain;
- no generic updater metadata table;
- no Worker attempt broker facet in `content`;
- no processing admin health route;
- no migration 067 or Processing PostgreSQL CI behavior selector.

These gaps justify the Child 10 manifest. They do not authorize Child 11's real
capabilities, updater binary/bundle, sandbox implementation, Worker image,
Compose/profile, CI image build/publish or UI.

## 6. Phase 1 tracked diff and execution-status truth

At evidence capture, tracked changes are confined to:

```text
M  .trellis/tasks/07-12-backup-data-explorer-design/task.json
?? .trellis/tasks/07-19-backup-assets-worker-protocol/check.jsonl
?? .trellis/tasks/07-19-backup-assets-worker-protocol/design.md
?? .trellis/tasks/07-19-backup-assets-worker-protocol/implement.jsonl
?? .trellis/tasks/07-19-backup-assets-worker-protocol/implement.md
?? .trellis/tasks/07-19-backup-assets-worker-protocol/prd.md
?? .trellis/tasks/07-19-backup-assets-worker-protocol/research/current-main-evidence.md
?? .trellis/tasks/07-19-backup-assets-worker-protocol/task.json
```

`implement.md` did not yet exist at the initial evidence capture; the path above
is the exact intended final Phase 1 planning manifest. No `backend/`, `web/`,
`deploy/`, `docs/`, migration, test implementation or release file is changed.

| Gate/action | Phase 1 status |
|---|---|
| local identity restoration | executed; ignored/untracked by Git |
| fetch/baseline/branch/task create | executed |
| parent/Children 7-9/current-main research | executed |
| focused planning artifacts | validated; approved by user 2026-07-19 |
| implementation authorization | approved by user 2026-07-19 |
| workflow transition to Phase 2 | pending; active state requires planning |
| `task.py start` | `not_executed` |
| product code | `not_executed` |
| migration implementation/apply/down | `not_executed` |
| product unit/race/behavior/live PostgreSQL tests | `not_executed` |
| backend build/Swagger gate | `not_executed` |
| stage/commit/push/PR | `not_executed` |
| CI/merge/post-merge | `not_executed` |
| Release Please PR #386/release/deploy | `not_executed`; out of scope |

Executed Phase 1 validation evidence:

```text
task.py validate:                 pass; implement.jsonl/check.jsonl valid
git diff --check:                 pass; no output
template-token scan:             0 actionable matches
approval-contradiction review:   0 contradictions; both user approvals are
                                 recorded while workflow execution remains closed
exact tracked planning manifest: 8/8 paths
product-path scope scan:         0 paths
future manifest/staging parity:  80/80 exact paths; 0 missing; 0 extra
child / parent status:           planning / planning
parent child registration:       exactly once; 10 instantiated children
```

These results validate planning artifacts only. Any future product, live,
integration or CI gate remains `not_executed` until its own execution output
exists.
