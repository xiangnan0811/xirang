# Backup Asset Export And Archive Retrieval Implementation Plan

> **Execution mode after approval:** load `trellis-before-dev`,
> `superpowers:test-driven-development` and the phase detail before product
> edits. Use focused review for crypto, lease, delivery and migration gates.
> Before any completion claim, load `trellis-check` and
> `superpowers:verification-before-completion`; finish with
> `trellis-finish-work`. The 2026-07-22 user clarification authorizes these
> implementation and delivery actions inside the exact approved scope.

**Goal:** implement the frozen, encrypted and fenced export aggregate plus
single-hop restricted archive-member orchestration described in `prd.md` and
`design.md`, without changing Provider bytes, deployment/release contracts or
the default-disabled feature posture.

**Architecture:** one managed Export runtime freezes selection through
Catalog/Search/Overlay adapters, reads exact assets through Content
`AttemptBroker`, atomically holds existing `export_job` RecoveryPoint leases
from create through access terminal, writes regular items to encrypted spools
before ZIP/TAR emission, publishes only under the current fence, and owns a
000068 exact temporary-artifact delivery ledger behind the existing content
route. Archive-member requests reuse the Child 11 Processing/Derived path,
bind exact output artifacts and reject chains longer than one.

**Tech stack:** Go 1.26, Gin, GORM, SQLite/PostgreSQL 18, standard-library
archive/zip/archive/tar/gzip, AES-256-GCM, SHA-256, CSPRNG, React 18,
TypeScript 5.8 strict, Vite 7, Vitest, Testing Library, i18next and Trellis.

## 1. Execution And Approval Gate

### 1.1 Current Phase 2 State

```text
task:                       .trellis/tasks/07-22-backup-assets-export-archive
status:                     in_progress
parent:                     07-12-backup-data-explorer-design (planning tracker)
branch:                     codex/backup-assets-export-archive
planning baseline:          9ad2893c714c82781461f452030c25e0766eedd4
feature head:                2e9e119ec3267748a9562f29450be0a18d9725f3
squash merge:                bd9572f9f69dde721db9976c25816ea72b4ae664
PR:                          #399 merged 2026-07-28T09:03:00Z
delivered program state:    11/15
focused planning approval:  complete_approved (2026-07-22 controller user reply "批准")
workflow transition:        complete_approved (same approval + user clarification)
implementation approval:   complete_approved (same approval + user clarification)
task.py start approval:     complete_approved (same approval + user clarification)
task.py start:              executed once on 2026-07-22
product/migration/tests:    executed within exact 131 manifest; runner stdout ownership RED/GREEN complete
PostgreSQL lock tranche:    closed (lock order/context/idempotency only; controller fresh required `TestExportBehaviorPostgres` PASS `11.527s`)
focused crypto review:      closed (spec ✅; quality APPROVED; zero findings)
archive-profile sub-boundary: closed (independent current-code review: complete AssetRef + nonnegative root
                              ordinal validation before allocation; collision allocator/limits final-member
                              validation after scope prefixing and every retry; focused selectors passed;
                              remaining P3 instrumentation coverage limitation is not a product failure)
final implementation review: closed in focused implementation/review tranches
Step 10 runnable gates:     passed_current_follow_up_amended; exact approved manifest 131;
                            Config rollback and Processing dual-boundary retry gates passed fresh
Step 10 status:             passed_with_explicit_dependency_risk_acceptance
dependency audit gate:      risk_accepted_for_child_delivery; unchanged 1 moderate + 3 high;
                            package files unchanged; audit did not pass; no --force or unsafe override
historical local gates:     checkpoints only; current completion evidence is the fresh runnable-gate record
Step 11 / delivery:         merged_post_merge_verified_archive_pending
commit/push/PR:             executed; feature head 2e9e119, PR #399 squash merge bd9572f
required/post-merge CI:     passed; main CI 30344924877, Release Please 30344927402
release publication:        no tag/release; image/description publish not triggered or expected
archive/journal:            not_executed; next Child-only bookkeeping action
release/deploy:             not_applicable in Child 12 product scope
```

This file does not self-authorize work. The 2026-07-22
controller-thread user reply “批准” marked the complete `prd.md`, `design.md`,
this plan, current-main evidence, the original manifest and thirteen
focused corrections in PRD §7 as `complete_approved`. The user's 2026-07-22
clarification confirms that same approval also authorized the planning workflow
transition, `task.py start`, and Phase 2 implementation inside the exact manifest.
When the controller presents multiple approval requests together, approval covers
all listed requests unless explicitly excluded; this does not authorize genuinely
new scope, irreversible high-risk actions, or manifest deviations.

### 1.2 Completed Approved-Start Preflight

Planning, workflow-transition, start and exact-manifest Phase 2 authorizations are
complete. Phase 2 reloaded workflow/spec/skills and passed this fresh preflight
before running the start command. The following is the exact historical
pre-start block and must not be rerun after the task is `in_progress`:

```bash
cd /home/murray/.codex/worktrees/7f06/xirang
git fetch origin --prune
test "$(git branch --show-current)" = "codex/backup-assets-export-archive"
test "$(git rev-parse HEAD)" = "9ad2893c714c82781461f452030c25e0766eedd4"
test "$(git rev-parse main)" = "9ad2893c714c82781461f452030c25e0766eedd4"
test "$(git rev-parse origin/main)" = "9ad2893c714c82781461f452030c25e0766eedd4"
test "$(git merge-base HEAD origin/main)" = "9ad2893c714c82781461f452030c25e0766eedd4"
test "$(python3 -c 'import json; print(json.load(open(".trellis/tasks/07-22-backup-assets-export-archive/task.json"))["status"])')" = planning
test "$(python3 -c 'import json; print(json.load(open(".trellis/tasks/07-12-backup-data-explorer-design/task.json"))["status"])')" = planning
git status --short
```

Expected dirty paths are exactly §2.1. Then reserve the migration slot:

```bash
for engine in sqlite postgres; do
  test -e "backend/internal/database/migrations/$engine/000067_backup_asset_processing.up.sql"
  test -e "backend/internal/database/migrations/$engine/000067_backup_asset_processing.down.sql"
  test -z "$(find "backend/internal/database/migrations/$engine" -maxdepth 1 -name '000068_*' -print -quit)"
  for version in 000069 000070 000071; do
    test -z "$(find "backend/internal/database/migrations/$engine" -maxdepth 1 -name "${version}_*" -print -quit)"
  done
done
```

Re-run the current-main symbol checks in research: `LeaseHolderExportJob`,
`KeyDomainDerivedStore`, Content ticket/Range/AttemptBroker, 000066 resource
CHECKs, archive profiles, Derived input restriction, step-up/audit registries,
frontend selection/lazy hooks and CI PostgreSQL selector.

If origin moved, another branch occupied 068, any reserved version exists,
the planning diff is not exact, or a current-main contract changed, stop. Rebase
from latest clean merged main, refresh all planning/evidence/manifests, and
obtain both approvals again. Do not silently renumber or copy an unmerged
sibling.

Only then may this command run once:

```bash
python3 ./.trellis/scripts/task.py start .trellis/tasks/07-22-backup-assets-export-archive
```

Record `in_progress` before the first red test. Planning approval alone is not
start authorization.

## 2. Exact File Manifest

Any tracked path outside this manifest requires a focused written amendment and
approval before edit. Do not create empty files to satisfy the list and do not
stage directories or wildcards.

### 2.1 Current Phase 1 Planning Manifest

```text
.trellis/tasks/07-12-backup-data-explorer-design/task.json
.trellis/tasks/07-22-backup-assets-export-archive/check.jsonl
.trellis/tasks/07-22-backup-assets-export-archive/design.md
.trellis/tasks/07-22-backup-assets-export-archive/implement.jsonl
.trellis/tasks/07-22-backup-assets-export-archive/implement.md
.trellis/tasks/07-22-backup-assets-export-archive/prd.md
.trellis/tasks/07-22-backup-assets-export-archive/research/current-main-evidence.md
.trellis/tasks/07-22-backup-assets-export-archive/task.json
```

There are seven Child files plus the parent child-registration edit. No
`backend/`, `web/`, migration, test, CI, deploy, public docs or release path is
part of Phase 1.

### 2.2 Future Implementation Create Manifest

```text
.trellis/tasks/07-22-backup-assets-export-archive/research/implementation-evidence.md

backend/internal/model/backup_asset_export.go

backend/internal/database/migrations/sqlite/000068_backup_asset_export.up.sql
backend/internal/database/migrations/sqlite/000068_backup_asset_export.down.sql
backend/internal/database/migrations/postgres/000068_backup_asset_export.up.sql
backend/internal/database/migrations/postgres/000068_backup_asset_export.down.sql

backend/internal/backupasset/export/contracts.go
backend/internal/backupasset/export/contracts_test.go
backend/internal/backupasset/export/selection.go
backend/internal/backupasset/export/selection_test.go
backend/internal/backupasset/export/state.go
backend/internal/backupasset/export/state_test.go
backend/internal/backupasset/export/idempotency.go
backend/internal/backupasset/export/idempotency_test.go
backend/internal/backupasset/export/archive.go
backend/internal/backupasset/export/archive_test.go
backend/internal/backupasset/export/crypto.go
backend/internal/backupasset/export/crypto_test.go
backend/internal/backupasset/export/store.go
backend/internal/backupasset/export/store_test.go
backend/internal/backupasset/export/service.go
backend/internal/backupasset/export/service_test.go
backend/internal/backupasset/export/worker.go
backend/internal/backupasset/export/worker_test.go
backend/internal/backupasset/export/lifecycle.go
backend/internal/backupasset/export/lifecycle_test.go
backend/internal/backupasset/export/delivery.go
backend/internal/backupasset/export/delivery_test.go
backend/internal/backupasset/export/audit.go
backend/internal/backupasset/export/audit_test.go
backend/internal/backupasset/export/metrics.go
backend/internal/backupasset/export/metrics_test.go
backend/internal/backupasset/export/quota.go
backend/internal/backupasset/export/quota_test.go
backend/internal/backupasset/export/behavior_integration_test.go
backend/internal/backupasset/export/testutil_test.go

backend/internal/backupasset/processing/archive_member.go
backend/internal/backupasset/processing/archive_member_test.go

backend/internal/backupasset/runtime/export_runtime.go
backend/internal/backupasset/runtime/export_runtime_test.go

backend/internal/api/handlers/backup_asset_export_handler.go
backend/internal/api/handlers/backup_asset_export_handler_test.go
backend/internal/api/handlers/backup_archive_handler.go
backend/internal/api/handlers/backup_archive_handler_test.go

web/src/lib/api/backup-exports-api.ts
web/src/lib/api/backup-exports-api.test.ts
web/src/lib/api/backup-archive-api.ts
web/src/lib/api/backup-archive-api.test.ts

web/src/features/backup-assets/use-backup-asset-export.ts
web/src/features/backup-assets/use-backup-asset-export.test.tsx
web/src/features/backup-assets/export-job-panel.tsx
web/src/features/backup-assets/export-job-panel.test.tsx
web/src/features/backup-assets/use-backup-archive.ts
web/src/features/backup-assets/use-backup-archive.test.tsx
web/src/features/backup-assets/archive-member-panel.tsx
web/src/features/backup-assets/archive-member-panel.test.tsx
```

File ownership is deliberate:

- `export/contracts/state/selection/idempotency` define closed identities and
  persistence transitions; `service` is the use-case boundary.
- `archive` only writes client ZIP/TAR and sanitizes output paths. It is not the
  Child 11 archive parser.
- `crypto/store` own Export key wrapping, chunk format and private root;
  `worker/lifecycle` own claims, leases, reconciliation and GC.
- `quota` owns durable global/user bucket CAS and crash-reconciled reservations.
- `delivery` owns 000068 `export_archive|archive_member` grants/requests while
  importing only reviewed Content ticket/Range primitives; it does not extend
  the ambiguous Content Broker IssueRequest or 000066 rows.
- `processing/archive_member` adds typed one-hop orchestration around existing
  capability/coordinator/Derived ports; it adds no parser/tool profile.
- `runtime/export_runtime` joins the one shared service graph and worker loop.
- frontend API/controller/panels are lazy boundaries, not eager client growth.

If implementation proves a listed split unnecessary or another exact file is
required for a reviewed contract, amend all three planning documents and the
manifest, obtain approval, then continue. Do not silently merge/omit/add paths.

### 2.3 Future Implementation Modify Manifest

```text
.github/workflows/ci.yml

backend/internal/database/backup_asset_migrations_integration_test.go
backend/internal/database/migrator.go
backend/internal/database/database.go
backend/internal/database/database_test.go

backend/internal/backupasset/keyring.go
backend/internal/backupasset/keyring_test.go
backend/internal/backupasset/service.go
backend/internal/backupasset/service_test.go
backend/internal/backupasset/repository/testutil_test.go
backend/internal/backupasset/repository/rsync_publication_execution_test.go
backend/internal/backupasset/repository/rsync_versioning_test.go
backend/internal/backupasset/provider/rsync_preflight_test.go
backend/internal/backupasset/provider/restic.go
backend/internal/backupasset/provider/runner_test.go
backend/internal/backupasset/overlay/idempotency.go
backend/internal/backupasset/overlay/service.go
backend/internal/backupasset/overlay/service_test.go
backend/internal/backupasset/content/derived_resolver.go
backend/internal/backupasset/content/derived_resolver_test.go
backend/internal/backupasset/content/attempt_broker.go
backend/internal/backupasset/processing/capability_service.go
backend/internal/backupasset/processing/capability_service_test.go
backend/internal/backupasset/processing/capabilities/runner.go
backend/internal/backupasset/processing/capabilities/runner_test.go
backend/internal/backupasset/processing/derived_manifest.go
backend/internal/backupasset/processing/derived_manifest_test.go
backend/internal/backupasset/runtime/processing_runtime.go
backend/internal/backupasset/runtime/processing_runtime_test.go
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go

backend/internal/settings/service.go
backend/internal/settings/service_test.go

backend/internal/api/router.go
backend/internal/api/router_test.go
backend/internal/api/backup_asset_rbac_test.go
backend/internal/api/docs/docs.go
backend/internal/api/handlers/backup_content_handler.go
backend/internal/api/handlers/backup_content_handler_test.go
backend/internal/api/handlers/settings_handler.go
backend/internal/api/handlers/settings_transition_test.go
backend/internal/api/handlers/config_handler.go
backend/internal/api/handlers/config_handler_test.go

backend/cmd/server/main.go
backend/cmd/server/main_test.go

web/src/types/domain.ts
web/src/lib/api/core.ts
web/src/index.css
web/src/index-css.test.ts
web/src/features/backup-assets/backup-assets-route-state.ts
web/src/features/backup-assets/backup-assets-route-state.test.ts
web/src/features/backup-assets/asset-bulk-bar.tsx
web/src/features/backup-assets/asset-bulk-bar.test.tsx
web/src/features/backup-assets/asset-browser.tsx
web/src/features/backup-assets/asset-browser.test.tsx
web/src/features/backup-assets/backup-assets-workspace.tsx
web/src/features/backup-assets/backup-assets-workspace.test.tsx
web/src/features/backup-assets/asset-preview.tsx
web/src/features/backup-assets/asset-preview.test.tsx
web/src/features/backup-assets/backup-asset-processing-panel.tsx
web/src/features/backup-assets/backup-asset-processing-panel.test.tsx
web/src/pages/__tests__/backups-page.a11y.test.tsx
web/src/pages/backups-page.data.tsx
web/src/pages/backups-page.test.tsx
web/src/i18n/locales/zh.ts
web/src/i18n/locales/en.ts

.trellis/spec/backend/database-guidelines.md
```

The frozen proposed future implementation manifest is exactly 56 create + 67 modify.
All paths are unique; the two sets do not overlap. The 2026-07-22 controller user
reply “批准”, as clarified by the user, marked this exact manifest
`complete_approved` for planning and Phase 2 implementation; the controller's
2026-07-23 focused amendments add only the three listed existing Rsync test
fixtures under the already approved no-aging-calendar contract. The 2026-07-24
controller runtime-settings amendment adds the four relevant Settings/Config
handler paths above plus the existing Overlay idempotency helper that reads the
same live TTL/key settings under its service lock, so non-restart Export settings, especially
`backup_assets.export.enabled`, use the existing atomic mutation boundary instead
of leaving the managed graph on a stale startup snapshot. The 2026-07-25
controller amendment adds only `migrator.go`: the PostgreSQL dirty-state
existence probe must follow the exact search path used by its read, and the
already-listed migration integration regression proves sibling isolation while
retaining genuine search-path-visible dirty fail-closed behavior. The same-day
SQLite amendment adds only `database.go` and `database_test.go`: enforced DSN
keys replace caller-supplied `_txlock`/`_busy_timeout` values so duplicate query
parameters cannot downgrade immediate writer serialization. A final scope-minimization
audit removes unrelated `settings_handler_test.go`; dedicated live-settings
coverage remains in `settings_transition_test.go` and `config_handler_test.go`.
The 2026-07-26 controller-directed, `complete_approved` amendment adds only the
two existing `processing/derived_manifest` paths above. It makes
`archive.extract_entry/archive_member_v1` output non-projectable so a text/OCR
member cannot publish a generic Search/preview projection before its request is
ready. A running request's terminal authority/source invalidation removes its
interest, while generic Search has no request-state join and unfenced ready
`RevokeSet` fails for a published projection; prevention
at manifest publication is the smallest containment. This plan records future
work and focused test coverage only, not code/test execution or broad durable
cleanup of historical projection output.

The source-reader drain amendment adds only the existing
`content/attempt_broker.go`. Its locked read path must allow cancellation to close
the underlying reader while a source `Read` is blocked, so the existing Export
runtime regression can prove reader-drain acknowledgement before key destruction
or source-lease release. It does not change a Content public contract, Broker,
BudgetService, Auth handler, ledger/model/migration, Provider, endpoint, deploy,
release, or product correction.

The shared Provider bounded-reader cancellation amendment adds only existing
`provider/restic.go` and `provider/runner_test.go`. `boundedReadHandle.Read`
currently holds its internal mutex through `underlying.Read`, while `Close`
needs that mutex before invoking `underlying.Close`; a genuinely blocked read
can therefore prevent the cancellation required before Export key destruction
or source-lease release. Write a real-wrapper RED, then narrow only this lock
scope and prove `Close` reaches the underlying reader without changing byte
accounting, overflow/EOF behavior, a Provider public contract, Provider bytes,
locators, credentials, Content, API, schema, deployment, release, or a product
correction.

The final runner stdout ownership amendment adds only existing
`processing/capabilities/runner.go` and `runner_test.go`. A final full gate
observed `read |0: file already closed`: `exec.Cmd.Wait` may close a
`StdoutPipe` before a delayed streaming consumer observes EOF. Add one
deterministic delayed-consumer RED, then replace only the pipe ownership with a
parent-owned `os.Pipe`; keep `Wait` concurrent so leader exit plus inherited
process-group stdout cannot block cleanup. This changes no capability/profile,
Worker protocol, dependency, API, migration, deploy, release or product
correction.

The 2026-07-28 accessibility amendment adds existing `web/src/index.css` and
`web/src/index-css.test.ts` only for the approved global reduced-motion/
power-save behavior and its regression. The export-list inset focus ring remains
owned by the already-listed export panel files. The separate narrow frontend
route-cleanup amendment adds existing `web/src/lib/api/core.ts`: removal of all
decoded `exportJobId` query fields must preserve every unrelated raw query byte,
order, duplicate, bare flag, empty separator and hash fragment. These amendments
add no endpoint, dependency, migration, deployment, release or product
correction.

Modification boundaries:

- CI only extends the PostgreSQL 18 migration/behavior selector from the 067
  baseline to 068 Export parity, keeps `REQUIRE_POSTGRES_MIGRATION_TEST=1` and
  `REQUIRE_POSTGRES_PROCESSING_TEST=1`, and adds
  `REQUIRE_POSTGRES_EXPORT_TEST=1`. It adds no image/login/tag/publish/release job.
- PostgreSQL dirty-state checking stays fail-closed. The narrow migrator change
  aligns its existence probe with the following unqualified read; it does not
  set `ALLOW_DIRTY_STARTUP` in CI, force-clean a test schema, or modify any SQL.
- SQLite DSN construction retains the existing WAL/UTC/pool policy but replaces
  protected query keys rather than appending duplicates. It adds no configurable
  lock bypass, migration, API, Provider, deploy or release behavior.
- keyring adds optional `export_store`; it does not make Export boot-required
  or change other domain rotation/loss semantics.
- Repository changes only its test foundation-default fixture and replaces the
  two approved Rsync test fixtures' aging calendar instants with one captured
  test-start-relative instant per fixture. Overlay changes
  only add the narrow caller-transaction saved-search owner/state/version/query
  validator and its tests; Export never reads the raw Overlay model.
- Provider production code and behavior remain unchanged except the internal
  `boundedReadHandle` lock-scope correction in `restic.go`; the only Provider
  test exceptions are `rsync_preflight_test.go`, where the expiring preflight
  fixture captures one test-start-relative instant without weakening the expiry
  check, and `runner_test.go`, which proves Close reaches a blocked underlying
  reader. Neither exception changes a public Provider contract or bytes,
  locators, credentials, tools, or commands.
- Content changes expose exact request/job/artifact-bound archive-member Derived
  resolution and add a typed delivery/revocation mux branch. The composite
  `RevokeSession` best-effort fans out both ledgers without changing Auth handler.
  The old Broker IssueRequest/contracts/model/000066 behavior and Content
  `BudgetService` are unchanged; member issue and budgeting use 000068.
- Processing changes construct/validate only the existing closed
  `archive.extract_entry` profile and member ordinal, and mark its
  `archive_member_v1` output non-projectable before generic projection creation.
- Router/Swagger/RBAC add only reviewed export/archive endpoints, inject the
  existing validated trusted-proxy set into the shared scheme resolver, and
  reuse the existing asset-content route; no public port/CORS/nginx path changes.
- frontend route state adds only one validated opaque `exportJobId`; page wiring
  owns push-on-open and replace-on-dismiss/repair. It never serializes selection,
  reason, path, ticket or proof.
- the backend database spec advances the documented migration head/parity
  selector after 068 is real; it is not public release documentation.

### 2.4 Explicitly Unchanged Without An Approved Amendment

```text
backend/go.mod
backend/go.sum
backend/internal/backupasset/domain.go
backend/internal/backupasset/lease.go
backend/internal/backupasset/lease_test.go
backend/internal/backupasset/provider/** (except approved rsync_preflight_test.go,
backend/internal/backupasset/provider/restic.go, and
backend/internal/backupasset/provider/runner_test.go)
backend/internal/backupasset/content/contracts.go
backend/internal/backupasset/content/broker.go
backend/internal/backupasset/content/broker_test.go
backend/internal/backupasset/content/budget.go
backend/internal/backupasset/content/budget_test.go
backend/internal/backupasset/search/**
backend/internal/backupasset/processing/capabilities/** (except approved
backend/internal/backupasset/processing/capabilities/runner.go and
backend/internal/backupasset/processing/capabilities/runner_test.go)
backend/internal/backupasset/processing/capabilityspec/**
backend/internal/backupasset/processing/worker_client.go
backend/internal/backupasset/processing/worker_client_test.go
backend/internal/model/backup_asset_content.go
backend/internal/model/backup_asset_processing.go
backend/internal/database/migrations/sqlite/000062_*
backend/internal/database/migrations/sqlite/000063_*
backend/internal/database/migrations/sqlite/000064_*
backend/internal/database/migrations/sqlite/000065_*
backend/internal/database/migrations/sqlite/000066_*
backend/internal/database/migrations/sqlite/000067_*
backend/internal/database/migrations/sqlite/000069_*
backend/internal/database/migrations/sqlite/000070_*
backend/internal/database/migrations/sqlite/000071_*
backend/internal/database/migrations/postgres/000062_*
backend/internal/database/migrations/postgres/000063_*
backend/internal/database/migrations/postgres/000064_*
backend/internal/database/migrations/postgres/000065_*
backend/internal/database/migrations/postgres/000066_*
backend/internal/database/migrations/postgres/000067_*
backend/internal/database/migrations/postgres/000069_*
backend/internal/database/migrations/postgres/000070_*
backend/internal/database/migrations/postgres/000071_*
backend/internal/api/handlers/step_up.go
backend/internal/api/handlers/step_up_test.go
backend/internal/api/handlers/auth_handler.go
backend/internal/api/handlers/auth_handler_test.go
backend/internal/api/handlers/credential_access_grant.go
backend/internal/api/handlers/credential_access_grant_test.go

web/src/lib/api/client.ts
web/src/context/auth-context-provider.tsx
web/src/context/auth-context.shared.ts
web/src/hooks/use-step-up-action.ts
web/src/features/backup-assets/backup-assets-state.ts
web/src/features/backup-assets/backup-assets-state.test.ts

deploy/**
docker-compose*.yml
docs/**
README.md
CHANGELOG.md
package.json
package-lock.json
scripts/**
```

There is no new dependency, Provider/credential/Command reader, parser profile,
state-reducer persistence, eager API registration, deployment volume/nginx,
official image, public docs, release or publication change. Existing typed
step-up purposes and audit actions are wired, not recreated. If a standard
library primitive cannot satisfy the reviewed crypto/archive contract, stop
for security/design approval before changing module manifests.

Repository production files remain unchanged except for the exact test fixture
listed in §2.3. Overlay remains unchanged except for the exact service caller-Tx
seam and test listed there. Content budget and Auth handler files, Processing
capabilityspec/Worker mapping, 000067 schema/model and all other paths stay
outside the manifest.

### 2.5 Workflow-Owned Completion Paths

Only after PR readiness/merge and post-merge monitoring may
`trellis-finish-work` move the Child 12 task to its deterministic archive and
update the shared journal/index. Inspect actual output and record/stage only
exact generated paths, for example:

```text
.trellis/tasks/archive/2026-07/07-22-backup-assets-export-archive/
.trellis/workspace/weibo/index.md                         (only if changed)
.trellis/workspace/weibo/journal-1.md                    (only if changed)
```

The parent remains planning and is never archived by Child 12. Product delivery
used dedicated branch `codex/backup-assets-export-archive` and PR #399. The
historical eleven-path follow-up was committed as feature head `2e9e119`, passed
required checks, and squash-merged as `bd9572f`. This section now governs only
the remaining Child archive/journal bookkeeping.

## 3. Ordered TDD And Implementation Steps

### Step 0: Authorized Workflow Transition (Completed)

Run §1.2, record exact baseline/diff/slot evidence, execute `task.py start` once,
and confirm only task status changed. Re-read relevant specs. Do not begin if
the migration or manifest is ambiguous.

### Step 1: Migration, Models And Key Domain

Write red tests before SQL/implementation for:

- all thirteen 000068 model/table/index/FK/CHECK contracts and UTC lifecycle
  order, including immutable typed job-limit columns, orthogonal execution/
  cleanup states, job-key, immutable item-attempt, quota and strict delivery
  union rows;
- pristine SQLite/PostgreSQL apply/down, exact 067 preservation and key-domain
  CHECK restoration; the first successful Export or archive-member 000068 write
  transaction ensures the global quota-bucket singleton use latch, which is never
  GC/deleted, so used and purge-to-empty down both fail permanently; required PostgreSQL 068 parity
  also proves the current connector scans both `timestamp` and `timestamptz`
  through their registered `ScanLocation`; direct SQL down proves only DB/lease/
  key pristine and does not claim filesystem inspection;
- `export_job` already accepted by 000062, with no 062 rewrite;
- `KeyDomainExportStore` optional ensure/active/by-version/rotate/rewrap/loss,
  no addition to boot-required domains and no cross-domain unwrap;
- no source-lifecycle FK hold/cascade, internal-only raw attempt fence fields,
  no raw/reversibly encrypted archive-member ID, forbidden plaintext path/
  member/ticket/key fields and non-paired/reserved migration scans;
- a create-commit job key can unwrap selection metadata before any attempt or
  artifact exists, while an artifact has no duplicate wrapped DEK owner.

Run the red tests and capture expected missing model/table/domain/selector
failures. Then add paired SQL/model/keyring, format Go and run focused tests.

```bash
cd backend
go test ./internal/backupasset -run 'Keyring.*Export|Export.*Key' -count=1
go test ./internal/database -run 'BackupAssetMigration068' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
  TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database -run '^(TestBackupAssetMigration068.*Postgres|TestRunMigrationsPostgresDirtyCheckUsesSearchPath)$' -count=1
```

The real PostgreSQL row must fail, not skip, when its required DSN is absent.

### Step 2: Selection Freeze And Idempotency

Write table-driven and integration tests for explicit refs, directory
expansion, nested dedupe, stable sorting, symlink leaf behavior, all-history
saved searches, complete pagination, stale cursor/version/broken scope,
mixed-owner no-leak, count/byte caps and golden canonical selection digests.

Make `FrozenSelection` carry an optional saved-search commit binding with ID,
expected version, canonical query digest and the frozen Search-generation digest.
Add the narrow Overlay service caller-Tx validator in only `overlay/service.go`:
it locks by ID/owner and validates active state, expected version and canonical
query binding. `SelectionResolver.RevalidateFrozenTx` composes that typed seam
with Search-generation/source locks in the same create transaction before any
`AcquireTx` or 000068 insert; Export must not query the raw Overlay
model. A barrier test updates the saved search after the last result page and
proves commit fails atomically with zero job/lease/key/reservation. The explicit
selection arm passes the same barrier without invoking the saved-search seam.

Add call-order probes proving domain-separated SHA-256 idempotency lookup precedes
Overlay/Search, replay never invokes Search, same-key/different-intent conflicts,
Export key rotation/loss does not change lookup, concurrent duplicates
elect one row/job, and a crash before atomic insert leaves no partial rows.
One-field differences cover RP/entry/Catalog/source/entry fingerprint, type,
size/deadline/path root and profile. Add an explicit repository query scan/test
that rejects entry-only resolution.

Test create transaction fault points before/after receipt, source revalidation,
saved-search final Tx validation, each zero-`AbsoluteDeadline` `AcquireTx`, exact returned deadline persistence,
quota reservation, job-key/item insert and commit. A queued
job always has all leases + wrapped key + reservation; concurrent source cleanup
either loses to the lease or causes atomic `source_expired`/no job. Prove frozen
rows have no RP/Catalog/Foundation FK retention/cascade. Assert create persists
the exact typed per-job profile/chunk/item/source/byte/reader/duration/attempt/
retry/lease/ready-TTL snapshot and a restart cannot reinterpret it from changed
settings.

Implement only narrow `SelectionResolver` adapters in Runtime; Export must not
import Provider or handler DTOs. Store path metadata AEAD-encrypted, never add or
infer a link target, and assert raw sentinel names are absent from DB/log/audit
fixtures.

```bash
cd backend
go test ./internal/backupasset/overlay ./internal/backupasset/export \
  -run 'Selection|SavedSearch|Idempotency|CompositeIdentity|ExportCommit' -count=1
```

### Step 3: Closed State, Source Leases And Fencing

Write red tests for every legal/illegal job/item/attempt transition, monotonic
revision, claim expiry, absolute deadline, retry cap and cancellation phase.
Use an injected/frozen UTC clock, or capture one test-start instant and derive
all future/past inputs relative to it. No TTL/lease/expiry test may use a fixed
calendar date that can age into expiry.
Cover create-time acquire-all source leases with transactional rollback,
queued/running/ready renewal, access-terminal release, expired source-lease
heartbeat-owner takeover, active `running/retry_wait/sealing` job-attempt
takeover/fresh durable raw fence, source-ref hash mismatch, feature disable and
purge override.

Add current-main compatibility regressions in the new Export tests, while
keeping `backend/internal/backupasset/lease.go` and `lease_test.go` unchanged:

- seed the same RecoveryPoint with an active different-holder lease and with a
  latest released historical lease, then prove Export passes zero
  `AbsoluteDeadline` and acquires its holder/owner slot successfully;
- capture every returned `Lease.AbsoluteDeadline`, persist it byte-for-byte in
  the source-ref row, and prove job execution, ready access and artifact expiry
  are each capped by the relevant minimum of returned deadlines, non-null
  `RetentionUntil`, frozen max duration and ready TTL;
- reject create/attempt when a returned cap is reached or below the frozen safe-
  start window; renew/takeover must preserve the stored deadline and no explicit
  deadline rewrite/reacquire path may exist.

Use deterministic two-worker barriers and `go test -race` to prove:

- old attempt cannot update checkpoint/item counters after takeover;
- every byte-zero rebuild resets all current item projections, owning-attempt
  fields and aggregate/checkpoint counters, including old packed/skipped/failed,
  while immutable item-attempt observations remain; old item/final spools are
  never reused;
- ready source-lease takeover only verifies/retains or revokes the sealed
  artifact and cannot create an attempt, return to running or reset projections/
  result/counters;
- read-before/read-after source drift marks only that item and never substitutes;
- canceled/expired/late attempt cannot seal or publish;
- only a transaction validating job + attempt + every RP fence commits ready;
- returned lease deadline cannot be extended by renew/takeover/restart/retry or
  explicit reacquisition;
- ready retains/renews every source lease; cancel/fail/expiry revokes + destroys
  key before releasing leases, and crash reconciliation does this idempotently;
- barriers at revoke/drain, wrapped-DEK/selection-reference destruction, source-
  lease/non-store release and unlink/fsync/inventory prove there is no state in
  which a source lease is released while the artifact key remains readable;
- failed/source_expired/canceled/expired execution outcomes remain stable while
  orthogonal cleanup progresses through revoking/purging/purged/purge_failed.

```bash
cd backend
go test -race ./internal/backupasset/export -run 'State|Lease|Fence|Takeover|Deadline|Cancel' -count=1
```

### Step 4: Archive Writer, Crypto And Store

Write archive-path fixtures for absolute/UNC/drive/`..`/NUL/control/default
ignorable/invalid UTF-8, NFKC/case collisions, Windows devices, trailing dots,
long components/depth, cross roots and deterministic suffixes. Cover empty
dirs, regular files, every symlink/hardlink as
`skipped/link_metadata_unavailable`, devices/FIFO/socket/unknown, ZIP/TAR
ordering and complete/partial report truth. Across restic/rsync/rclone fixtures,
assert no link target exists in the input contract, no path/name/locator/
fingerprint inference occurs, no Provider reader opens and no active/inert link
or target-text member is emitted. Zero packed items must fail.

Write streaming/backpressure tests with short readers/writers, injected cancel,
Provider byte meters and hard logical/provider/ciphertext/item/concurrency/time
limits. Regular files alone call `AttemptBroker`; directories may emit empty
members, links/special/unknown call exact Catalog/source revalidation and never
touch Provider. Provider bytes stream to
an authenticated item spool, read-after drift/tamper discards it before an
archive header, while any injected error after header aborts the whole attempt.
Assert no plaintext temp path and no source mutation.

Crypto tests use deterministic randomness only in tests and cover chunk-boundary
sizes, unique prefixes/counters, AAD one-field differences, header/trailer,
bit flip, wrong job/digest/profile/fence/key, reorder/duplicate/delete/truncate,
cross-export copy, Range chunk selection and zero plaintext on auth failure.
Cover key rotation/rewrap/loss and deletion order.

Store/quota tests cover no-follow, process lock, restrictive modes, opaque
locators, root/cache/Derived/source overlap, crash before/after spool/final
fsync/rename, orphan inventory and purge failure. Two-worker barriers prove
global/user bucket CAS never exceeds job/worker/reader/store limits; crash
expiry reconciles reservation counters against encrypted files conservatively.
Every first successful Export/member 000068 write path must ensure the reserved
global bucket in its own transaction, and GC/purge/summary expiry must never
delete it. Prove the latch is idempotent under concurrent first writers and
survives a full purge-to-empty cycle.
DEK deletion may release source/non-store reservations, but unlink failure must
retain `purge_pending` store-byte accounting until unlink + parent fsync + locked
inventory prove the object absent.

```bash
cd backend
go test -race ./internal/backupasset/export -run 'Archive|Path|Stream|Crypto|Chunk|Store|Root|Key|Purge' -count=1
```

Pause for the mandatory chunk/key/path review before proceeding.

### Step 5: Worker, Checkpoint, Restart And GC

Implement the persistent worker around `content.AttemptBroker`; do not call a
Provider implementation. Tests inject exact `SourceSession` and prove stat/
sequential/ProviderBytes/revalidate/close behavior, backpressure and context
cancellation. A blocked source read must receive a close that does not wait on
the read mutex, and cleanup must observe its drain acknowledgement before it can
destroy the job key or release a source lease.

Fault injection covers crash immediately after create commit/before attempt,
after job claim, source heartbeat, source open, each item-spool chunk, post-read
revalidation, archive header/body/close, each final encrypted chunk, fsync,
rename, seal transaction, ready publication, ticket revoke, key destroy, lease/
non-store quota release, unlink/fsync and store-byte release.
For every point, restart reconciliation produces exactly one of safe retry,
ready verified artifact or cryptographically revoked cleanup. It never appends
to old staging or trusts filename/mtime, and never releases a source lease before
the wrapped DEK/selection references are irreversibly destroyed.

GC tests inject/freeze time around 24h/returned-lease/RecoveryPoint-retention
caps or derive boundaries from one captured test-start instant; they never use
an aging fixed-calendar TTL/lease/expiry fixture. Ensure download does not slide expiry,
revoke all grants before DEK deletion, delete metadata/ciphertext idempotently,
retain only safe summary and retry orthogonal cleanup/artifact `purge_failed`
without restoring access or releasing still-occupied store bytes. Cover unlink
failure for pre-publication cancel/fail/source-expired and ready expiry; the
execution outcome never changes to a cleanup outcome.

```bash
cd backend
go test -race ./internal/backupasset/export -run 'Worker|Checkpoint|Restart|Reconcile|TTL|GC|Orphan|Fault' -count=1
```

### Step 6: Exact Temporary-Artifact Delivery And Existing Content Route Mux

Write tests before gateway/mux code for:

- ready/owner/permission/fresh `asset.export_download` issue and all non-ready,
  expired/key-lost/tampered/foreign-owner rejections;
- grant freezes exact export artifact/attempt/fence/digests/sizes/chunk+format/
  job-key version and session JTI/token+role revision/proof expiry; artifact
  replacement, a second match or KEK rewrap version change revokes it;
- cookie secret hash-only storage, exact path/action/session/subject/method/range,
  Secure/SameSite/HttpOnly and direct TLS/trusted-proxy/loopback policy;
- forged XFP from untrusted peer, zero/multiple/comma values, ambiguous
  `Forwarded`, TLS/header contradiction and invalid CIDR all fail closed; only
  a configured proxy peer's canonical HTTPS is accepted for forwarded TLS;
- collision checks across 000066/000068, zero/one/double resolver match and
  indistinguishable safe not-found;
- GET/HEAD, full/single/open/suffix Range, multi/overflow/If-Range, per-request/
  cumulative/in-flight budgets, repeated/revoked cookie and crash accounting;
- independent 000068 delivery transaction/CAS reserve/finalize/replay semantics,
  with parity fixtures against the reviewed 000066 behavior and conservative
  crash reconciliation; no call or edit to Content `BudgetService`;
- plaintext Range to encrypted chunk mapping, chunk authentication before write,
  mid-stream revoke/drain and content-disposition safety;
- logout/session/role/permission/cancel/expiry/key-loss revocation; the existing
  handler/runtime/main mux exposes one composite `RevokeSession` that always
  attempts both 000066 Content and 000068 Export ledgers, aggregates a safe
  error, denies both after logout and reconciles partial failure after restart;
  cover Content-only failure, Export-only failure and both failures;
- route template/application/audit log redaction for delivery/cookie/ID/path/
  filename/member/key/locator/raw error.

Reuse only exported Content ticket/deadline/cookie/Range/scheme helpers. Implement
000068 grant/request/bucket budgeting in `export/delivery.go`; do not import or
generalize the 000066-specific Content `BudgetService`. Inject Router's
already validated `TrustedProxies` into one shared scheme policy used by old
Content and 000068 issuance. Do not change Content contracts/Broker/model,
000066 SQL, or add a bearer/export URL. Keep the same router path and middleware
recovery behavior.

```bash
cd backend
go test -race ./internal/backupasset/export ./internal/backupasset/content ./internal/api/handlers \
  -run 'ExportDelivery|AssetContent.*Export|Range|Cookie|TLS|Redact|Revoke' -count=1
```

Pause for the ticket/cookie/content-route review.

### Step 7: One-Hop Archive Member Retrieval

Write tests proving the list endpoint reads only a current, validated Derived
archive index and returns opaque IDs/sanitized fields/index revision without
ordinal/path/artifact locator. Create accepts one opaque ID plus exact index
revision; zero/>1 chain, client path/ordinal, stale index and cross-asset/member
reuse fail closed. The durable request stores only a domain-separated member-ID/
chain digest and resolved ordinal, never the raw ID or ciphertext lacking an
owned metadata-key lifecycle; Worker output is rehashed against that binding.

Add durable HTTP idempotency tests: lookup-before-index/Processing, Export-key
rotation/loss independence, same-key/same-intent same request, different member/index
conflict, concurrent unique winner, crash after request insert before interest,
and reconciler-created request-ID-keyed interest exactly once.

Use existing `archive.extract_entry` profile/coordinator/Worker. Tests verify
outer AssetRef/Catalog/source/entry/pipeline/security binding, private ID->ordinal
resolution, idempotent interest, poll/cancel and Derived output metadata/digest.
The exact Derived resolver must bind request + outer fingerprints + Processing
job/attempt/set/artifact/blob/member digest; two member requests for one outer
asset receive distinct correct 000068 attachment-only grants. GET/HEAD uses
Range none plus fresh `asset.download`; it never uses the existing Broker
IssueRequest/renderer or falls back to the outer file. Run malicious ZIP/TAR/gzip/xz/zstd
fixtures for traversal, duplicate path, symlink/hardlink/device/FIFO/socket,
bomb/ratio/count/size/time, encrypted, malformed and unsupported cases.

Add the focused `derived_manifest_test.go` regression before changing
`derived_manifest.go`: `archive.extract_entry/archive_member_v1` output that
would otherwise be text/OCR must not satisfy `manifestNeedsProjection` or publish
a generic projection. Cover the terminal authority/source invalidation boundary
as prevention-before-publication: a generic Search projection has no request-state
join, and ready `RevokeSet` is unfenced after publication. Do not claim this
future regression supplies broad historical-projection cleanup.

For encrypted, unsupported and limit outcomes, test the closed archive DTO/reason
used by the frontend fallback. A real ratio-bomb capability fixture must return
`ErrInputLimit`, pass through the unchanged Worker mapping, and persist generic
`ProcessingErrorInputTooLarge`; the backend mapper must map every persisted
`ProcessingErrorInputTooLarge` to that same closed `limit` product. Do not add
067/schema/capabilityspec/worker-client fields to retain
`ReasonArchiveRatioLimit`, and do not make the member resolver fall back to the
outer file: original download remains a separate existing Child 8 Content action,
and controlled recovery remains absent/capability-gated until Child 13.

Add pairwise permission/sensitivity/malware/source-drift/revoke tests and prove
the output never enters `DerivedAttemptSourceResolver` as a nested input. Audit
contains only outer IDs and member/index keyed digest/category.

```bash
cd backend
go test -race ./internal/backupasset/processing ./internal/backupasset/content ./internal/api/handlers \
  -run 'Archive(Member|Index|Extract|Nested|Malicious|Audit)' -count=1
```

Pause for Worker sandbox/grant/archive malicious-fixture review.

### Step 8: Runtime, Settings, API, RBAC And Audit

Wire one managed Export runtime, root/keyring/settings snapshots, startup/disable/
shutdown/schema-down and accessors. `ExportRuntime.PrepareSchemaDown` must stop
admission, drain, hold/validate the Export-root lock, reject any owned/unknown/
unreadable entry, then invoke the DB-only 068 down callback; direct SQL never
claims to inspect files. Add strict handlers and exact routes from
design §§9-10. Use response helpers, body/rate limits, typed permission,
step-up/audit actions and safe errors; remove no generic grant or reason.

Tests cover default false, on-demand key/root unavailable, no Worker/core-only,
Command typed unsupported, restart, immutable per-job limit snapshots across
dynamic setting increases/decreases, conservative existing store reservations,
runtime-owned empty-root/orphan schema-down proof, metrics label allowlist and
all endpoints' 202/Location/idempotency/cancel/status/ticket contracts.

Extend `repository/testutil_test.go` with only the frozen defaults for every new
Export/Archive foundation key. Run the existing complete-snapshot fixture test
and a focused Repository test proving `BackupAssetFoundationSettingKeys` is
fully present; do not add fallback behavior or weaken `atomicFoundationValues`.

For every exact setting in design §11.1, assert key/env/type/default/min/max/
RequiresRestart metadata and the cross-field inequalities. Archive settings may
only tighten Child 11 hard caps. Assert there is no raw Export KEK/file setting,
and existing idempotency TTL/key-length settings are reused.

Run full Admin/Operator/Viewer, creator/foreign-owner, disabled/malformed/
missing and no-existence-leak matrices. Run complete step-up purpose pairwise
cross-rejection, route-to-action audit coverage, redaction and retention-chain
tests. Regenerate Swagger only after handler tests pass and inspect the diff.

```bash
cd backend
go test -race ./internal/backupasset/export ./internal/backupasset/repository ./internal/backupasset/runtime ./internal/settings ./internal/api/... \
  -run 'AssetExport|ArchiveMember|RBAC|StepUp|Audit|Runtime|Settings' -count=1
cd ..
make swag-init
git diff -- backend/internal/api/docs/docs.go
```

### Step 9: Frontend Closed Mapper And Lazy Product

Write API mapper tests first for every job execution/cleanup, item/artifact/
archive state, IDs, duplicate
ordinals/refs, byte/time ordering, ready/partial/expiry contradictions,
query-bearing/cross-origin URL, unknown enum/reason and atomic whole-product
rejection.

Then write interaction/controller tests for:

- Admin/Operator/Viewer trigger visibility plus server 403 handling;
- API mapper/request tests for both explicit and saved-search ID/version create
  arms, but UI trigger tests only for deep-cloned immutable explicit selection;
  saved-search overlay remains unchanged and API-only;
- toggle/clear/results/route change during step-up/create, duplicate click/
  idempotency and stale response abort; pre-create review labels root count and
  bytes as client estimate, then replaces them with authoritative 202/status
  count/bytes/digest;
- fresh non-persisted create/download proofs and cross-purpose rejection;
- queued/running/retry_wait/sealing polls, server cadence, 429/503, hidden/
  offline resume, Core restart GET reconciliation and terminal stop;
- route-only opaque `exportJobId`, push once on new open, poll without navigation,
  replace on dismiss/404/unauthorized/context reset, Back semantics, non-Admin
  direct URL and zero selection/reason/proof/ticket/path/name in local/session
  storage/history state/query/logs;
- summary + stable cursor pages at 100/hard 200, bounded DOM, every per-item
  result, complete/partial/zero-packed, cancel race, automatic retry wording,
  ready/expiring visual countdown with live announcements only at state and
  1h/10m/1m/expired thresholds, fresh redownload, expired/key/cleanup failure;
- archive hierarchy/index revision/one-hop create/poll/cancel/delivery and all
  encrypted/unsupported/limit/bomb/traversal/special/stale states; consume only
  the backend closed `limit` product. Step 7 owns the ratio-bomb proof from
  `ErrInputLimit` through persisted `ProcessingErrorInputTooLarge` to that
  product; frontend must not receive, depend on or guess a raw Worker diagnostic
  reason. For encrypted, unsupported and every limit outcome show a download-original
  command only when existing download permission, online state and Content
  availability allow it, delegate to the existing
  `onPrepareDownload`, and otherwise render the same no-leak closed reason;
- regression-prove the delegated original download uses the existing typed
  `download/attachment/original_v1` flow with fresh non-persisted
  `asset.download` proof/ticket, never an Export/member proof, and that no
  controlled-recovery action is rendered before Child 13;
- dialog naming/trigger focus return plus reload fallback to result heading/
  workspace root, keyboard/list+grid selection/ARIA live/progress/reduced
  motion/axe, zh/en parity and 1440/1200/390 layouts.

Keep both API modules/hooks/panels lazy and outside `apiClient`. Do not persist
selection or modify the shared selection reducer unless a separately approved
test proves unavoidable.

```bash
cd web
env -u NODE_ENV npm run typecheck
env -u NODE_ENV npm run lint
env -u NODE_ENV npm run test
env -u NODE_ENV npm run build
node scripts/check-bundle-budget.mjs
env -u NODE_ENV npm run check
```

Inspect produced chunks and confirm the eager main JS/CSS limits remain 500/105
KiB. Run browser/CDP screenshots and keyboard/axe checks only after a local
server is started for the implemented UI; record exact viewports and evidence.

### Step 10: Cross-Engine, Full Gate And Documentation Truth

**Current status:** `passed_with_explicit_dependency_risk_acceptance`. The
runner-amended record below is a historical pre-commit checkpoint; the current
Config rollback, Processing dual-boundary retry, fresh aggregate gates, and
controller dependency-risk disposition are recorded in Step 11 and §8. The
unchanged audit still fails with `1 moderate + 3 high`; its explicit temporary
acceptance is not remediation or an audit pass. The prior exact-129 runnable
record and the later runner-reopen state are historical checkpoints. That later full
`make check` observed
`TestRunnerStreamsToolStdoutToConsumerAndJoinsOnCancellation` fail with
`read |0: file already closed` in the then-unchanged
`processing/capabilities/runner.go`. Go's documented contract says
`Cmd.Wait` closes a `StdoutPipe` and must not run before all reads complete;
the old streaming runner started `Wait` and the consumer concurrently, so a
fast leader exit could close the pipe before a delayed consumer reached EOF.

The current exact scope is `8 Phase-1 + 56 create + 67 modify = 131`, comprising
`68 tracked + 63 untracked`, with zero missing, extra, overlap, duplicate or
staged paths. A deterministic delayed-consumer regression reproduced the exact
old-code `file already closed` RED. GREEN uses a parent-owned `os.Pipe`, assigns
its writer to `command.Stdout`, closes the parent's writer copy immediately
after successful `Start`, and retains concurrent `Wait`/process-group cleanup.
The exact regression, capabilities normal/race/20x repetition, `go vet`, exact
parity/static checks, corrected short-TMP full-project gate, bundle budgets and
required real PostgreSQL selectors then passed fresh. The thirteen product
corrections are unchanged.

Earlier PostgreSQL lock, crypto/AAD, archive-profile, lifecycle/runtime-stop and
other `passed_local` or completion-like rows below remain historical focused
checkpoints. They are not substituted for the fresh aggregate record above.
The P3 instrumentation coverage note remains a coverage limitation rather than
a product failure, and the focused lock/review tranches are closed.

The 2026-07-28 CI-equivalent dependency audit is a separate external gate, not
a product-scope amendment. Fresh Node 20/npm 10 and local npm 11 runs fail on
new `brace-expansion` and `react-router` advisories. A compatible lockfile probe
updates the older vulnerable resolutions but still requires an unpublished
compatible brace-expansion fix and `react-router 8.3.0`; that router release
requires Node `>=22.22` and React `>=19.2.7`, outside the repository's Node 20 /
React 18 contract. Do not use `--force`, downgrade to another vulnerable line,
or smuggle a major router/React migration into Child 12. Keep both package files
unchanged; the unrelated `core.ts` route-cleanup amendment made the prior exact
manifest 129, while the independent runner amendment makes the target 131.
The unchanged dependency tree currently reports `1 moderate + 3 high`. The
remaining brace-expansion advisory needs either a compatible upstream release
or a separately scoped lint-tool migration; the Router family needs a 7.x
backport or a separately approved Node `22.22+` / React `19.2.7+` / Router 8
migration. Before the controller's explicit temporary risk acceptance, this
audit was the sole open Step 10 gate; every runner-amended runnable boundary was
current and closed. The later disposition records the audit as
`risk_accepted_for_child_delivery` without claiming remediation or an audit
pass.

The current runner-amended record executed this focused TDD and repetition
sequence:

```bash
cd backend
go test ./internal/backupasset/processing/capabilities \
  -run '^TestRunnerStreamsToolStdoutToConsumerAndJoinsOnCancellation$' -count=1
go test ./internal/backupasset/processing/capabilities -count=1
go test -race ./internal/backupasset/processing/capabilities -count=1
go test ./internal/backupasset/processing/capabilities \
  -run '^TestRunnerStreamsToolStdoutToConsumerAndJoinsOnCancellation$' -count=20
go vet ./internal/backupasset/processing/capabilities
```

After runner GREEN, the current aggregate record reran the following command
family with the short temporary-root correction shown for the full gate:

```bash
cd backend
git diff --name-only --diff-filter=ACMR -- backend | rg '\.go$' | xargs -r gofmt -w
go test -race ./internal/backupasset/export ./internal/backupasset/repository ./internal/backupasset/overlay ./internal/backupasset/processing ./internal/backupasset/content ./internal/backupasset/runtime ./internal/api/... -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
  TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database -run '^(TestBackupAssetMigration068.*Postgres|TestRunMigrationsPostgresDirtyCheckUsesSearchPath)$' -count=1
REQUIRE_POSTGRES_EXPORT_TEST=1 \
  TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
REQUIRE_POSTGRES_PROCESSING_TEST=1 \
  TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/processing -run '^(TestProcessingBehaviorPostgres|TestArchiveMemberBehaviorPostgres)$' -count=1
cd ..
make backend-test
make backend-build
rm -f backend/xirang-server
test ! -e backend/xirang-server
TMPDIR=/tmp/xc12 GOTMPDIR=/tmp/xc12 env -u NODE_ENV make check
rm -f backend/xirang-server
test ! -e backend/xirang-server
env -u NODE_ENV npm --prefix web audit --audit-level=moderate
git diff --check
```

Every command in that runner-amended block except the audit command passed.
`backend/xirang-server` was removed and confirmed absent. A warning-only audit
result in hosted CI cannot convert the local fail-closed dependency row into a
pass.

Update the backend database spec only with verified 068 head/selector facts.
Run exact scope, privacy, Provider-mutation, migration reservation, forbidden
deploy/release/docs, manifest/staging parity and generated Swagger scans from
§§4-5. Add the fail-closed `REQUIRE_POSTGRES_EXPORT_TEST=1` helper in Export's
behavior integration test and set each required PostgreSQL env on its exact,
independent migration/Export/Processing selector in the CI job and commands
above. A required unavailable PostgreSQL/browser/Docker environment
is a blocked row with evidence, never a pass or skip. Review every Export test
date literal: TTL/lease/expiry inputs must be frozen/injected or test-start-
relative; a fixed literal is allowed only for non-expiring codec/schema round-
trip proof such as `timestamp`/`timestamptz` ScanLocation parity.

The PostgreSQL dirty-state regression is part of the required migration command.
It creates a sibling schema containing `schema_migrations`, then proves a scoped
empty schema is pristine and a search-path-visible dirty row still returns `ErrMigrationDirty`.
Do not use `ALLOW_DIRTY_STARTUP` to make this selector pass.

Request focused code review for the high-risk matrix in design §15 and use
`superpowers:requesting-code-review`. Resolve findings on the same branch and
rerun affected/final gates before claiming implementation complete.

### Step 11: Merged Delivery And Child-Only Bookkeeping

**Current status:** `merged_post_merge_verified_archive_pending`. The exact-131
implementation reached feature head `2e9e119ec3267748a9562f29450be0a18d9725f3`.
PR #399 at <https://github.com/xiangnan0811/xirang/pull/399> passed required
checks and squash-merged at 2026-07-28T09:03:00Z as
`bd9572f9f69dde721db9976c25816ea72b4ae664`. Earlier pending/draft claims in
historical evidence below are not current truth.

CI RED/GREEN 1 found that Config import swallowed Task Create errors, returned
200, and could partially commit nodes. Production now propagates the transaction
error, producing the generic 500 envelope and total rollback; the fixture also
isolates the secure global cache. Focused/sequencing tests, handlers coverage
`57.9%`, handlers race, vet/gofmt/diff passed.

CI RED/GREEN 2 found that Processing `CommitManifest` could hit SQLite locks at
the projection-evidence read and atomic-publish transaction and produce
concurrent successes=0. Both boundaries now use the existing bounded,
context-aware conflict retry and retain one durable winner. Deterministic
focused tests, count `50`, race count `20`, Processing coverage `74.0%`, and
full Processing race passed.

Fresh local gates after both fixes passed: exact `131`, child/parent validation,
`env -u NODE_ENV make check` exit `0`, backend lint `0`, frontend `168 files /
1388 tests`, lint `0` errors plus one approved a11y debt warning, and bundle JS
`498.48/500 KiB` plus CSS `104.94/105 KiB`. Required PostgreSQL 18 selectors
passed without skip against `127.0.0.1:55470`: migration/UTC/dirty-search-path
`49.561s`, Export `13.353s`, and Processing/archive-member `4.679s`.

The five-file in-manifest product/spec follow-up and six ledger files recorded
at the earlier checkpoint were reviewed and committed as feature head
`2e9e119ec3267748a9562f29450be0a18d9725f3`. Required PR checks passed. The
post-merge main CI run `30344924877` and Release Please run `30344927402` also
succeeded. Release Please only updated PR #386; it did not create a tag or
GitHub Release, so no Docker image publish or Docker Hub description sync was
triggered or expected. Local `main` was fast-forwarded to `bd9572f`.

The unchanged npm audit still reports four vulnerabilities (`1 moderate + 3
high`). The controller explicitly and temporarily accepts that pre-existing
risk for Child 12 delivery, closing the Child gate without claiming remediation
or an audit pass. Package files stay unchanged; do not use `--force`, an unsafe
override, or an incompatible Node 20/React 18 router migration. Track compatible
upstream remediation or a Node/React/Router migration in a separate Trellis
task/branch after Child 12 merge.

The delivery ordering through merge and post-merge monitoring is complete. The
remaining mandatory order is:

1. Validate these six superseding ledger updates on the dedicated bookkeeping
   branch.
2. Run `trellis-finish-work` so `task.py archive` moves only Child 12 and
   `add_session.py` records merge commit `bd9572f`.
3. Deliver the bookkeeping commits through their own PR and monitor required
   CI/post-merge automation. Do not merge Release Please PR #386.
4. Keep parent `07-12-backup-data-explorer-design` in `planning`; Child 12 must
   not archive or complete it.

No GitHub Release, Docker publish or Docker Hub sync is expected from this
non-release child unless repository automation actually creates one; record
observed truth rather than inferring it.

## 4. Validation Matrix

| Boundary | Required evidence |
|---|---|
| exact identity | explicit/directory/search freeze, canonical digest, no entry-only lookup, no new RP/substitution; final saved-search owner/state/version/query + Search-generation Tx validation before acquire/insert, with update-after-last-page race and explicit-arm control |
| idempotency | domain-separated digest lookup-before-search/index, Export-key independence, same/different intent, concurrent winner, crash boundary for export/member |
| job/item state | closed execution + orthogonal cleanup transitions, byte-zero full projection/counter reset, immutable item-attempt history, partial/complete truth, zero-packed failure, unknown rejection |
| migration/model | paired thirteen-table 068, DB-only pristine/used/purge-to-empty down with permanent global quota-bucket use latch, runtime locked-root proof, 067 preservation, no source FK hold, UTC/CHECK/FK/index/model parity, required migration/export/Processing PostgreSQL envs, both `timestamp`/`timestamptz` ScanLocation regressions and no skip evidence |
| key/chunk crypto | create-crash job key, domain-separated spool/final randomness/nonce/AAD/trailer, Range, tamper/reorder/truncate, rotation/rewrap/loss/delete |
| store/root/quota | no plaintext, no-follow/lock/fsync/rename, source/cache/Derived separation, bucket CAS, concurrent first-write permanent global use latch, store reservation retained through physical delete, orphans |
| leases/fence | zero-deadline create acquire despite different-holder/released history, exact returned deadline persistence/caps, queued/ready renew, access-terminal release, source-lease-owner vs active-attempt takeover separation, no rewrite/reacquire extension, two workers, late read/checkpoint/seal/publish; Foundation lease files unchanged |
| archive writer | encrypted pre-header spool, post-header attempt abort, ZIP/TAR/profile, deterministic sanitization/collision, directory/link/special closed outcomes, cross-Provider no-link-target/no-read/no-mutation, report/backpressure/limits |
| TTL/GC | injected/frozen or test-start-relative time with no aging calendar fixture, 24h/returned-lease/RecoveryPoint-retention caps, non-sliding, fence/revoke/drain -> DEK/selection destroy -> source-lease/non-store release -> unlink/fsync/inventory -> store release, every crash boundary idempotent, no released-source/readable-key window, pre/post-publish purge retry |
| ticket/content | exact export/member artifact, cookie/path/session/action, trusted-proxy TLS, GET/HEAD/Range, independent 000068 transaction/CAS budget parity, replay/revoke, mux collision/redaction, best-effort dual-ledger logout with one/both failure and restart convergence |
| archive member | durable idempotency, outer fingerprint/index revision/member digest + ordinal without unowned ciphertext, exact Derived artifact, one-hop, malicious fixtures, ratio-bomb `ErrInputLimit -> persisted ProcessingErrorInputTooLarge -> closed limit` mapper, encrypted/unsupported/all-limit original-download/no-leak fallback, Child 13 recovery gate |
| RBAC/step-up | Admin/Operator/Viewer/owner/no-leak, exact create/download/download purposes pairwise rejected |
| audit/privacy | action coverage, allowed fields, raw sentinel scan, segment retention and failure policy |
| settings/runtime | default false, optional key/root, complete foundation-key Repository fixture, immutable job/current grant limits, live admission controls, startup/disable/restart/shutdown/runtime-owned schema-down root proof |
| frontend mapper | atomic closed mapping, invalid ID/time/bytes/state/url/error rejection |
| frontend flow | explicit cloned selection + API-only saved search, estimate vs authoritative values, paging, polling/reload/cancel/TTL/fresh proof/no persistence, archive encrypted/unsupported/all-limit (including ratio-bomb) original-download permission/offline/unavailable matrix |
| delivery hygiene | exact cleanup + absent assertion for `backend/xirang-server` after backend/full builds; final manifest is tracked/staged/untracked union |
| frontend quality | route push/replace/Back, focus fallback, quiet live regions, keyboard/axe/reduced motion/zh-en/1440-1200-390/lazy bundle |
| regressions | Content/Catalog/Search/Overlay/Processing/Command/core-only, backend/frontend/full `env -u NODE_ENV make check` gate |

## 5. Security And Fault-Injection Matrix

| Injection/fixture | Required closed outcome |
|---|---|
| saved search changes between pages or after the final page before commit | typed Overlay/Search Tx validation rejects atomically; no job/lease/key/reservation; explicit selection unaffected |
| crash after create commit before attempt | job key unwraps; all leases/reservations reconcile; no orphan unreadable metadata |
| source cleanup races queued create | atomic lease/revalidation winner or no job; no FK hold/cascade loss |
| same RP has active different holder or latest released history | Export zero-deadline acquire succeeds in its own holder/owner slot; returned deadline persists and caps lifecycle |
| returned lease/RetentionUntil is reached or unsafe | create/attempt fails closed; renew/takeover preserves deadline; no explicit rewrite/reacquire extension |
| mutable source changes during read | affected item failed, no substituted bytes |
| item read fails/drifts before archive header | encrypted spool deleted; only item fails when partial allowed |
| local/tamper error after archive header | entire attempt aborts; no malformed ready artifact |
| RP expires between final byte and seal | no ready; source_expired/revoke cleanup |
| old worker writes after active-attempt takeover | CAS/fence rejection, every current projection/counter reset under new attempt, orphan deletion |
| ready source-lease owner expires/takes over | sealed attempt/result/projections unchanged; verify/retain or revoke only |
| crash at every filesystem/DB boundary | retry, verified ready or revoked cleanup; never plaintext/false ready |
| crash around revoke/key/lease boundary | ordered reconciliation resumes; wrapped DEK is destroyed before source-lease release, so no released-source/readable-key window exists |
| nonce collision/random failure | attempt/job rejected; no encryption under repeated nonce |
| header/chunk/trailer/DB metadata tamper | zero unauthenticated body and revoke/purge |
| key rotation/loss mid-ticket | old referenced key works until rotation policy; loss revokes immediately |
| artifact/KEK tuple changes after ticket | exact grant mismatch revoked; fresh ticket required |
| cross-ledger delivery collision | neither branch served; safe not-found + bounded alert |
| logout revocation: Content-only, Export-only or both ledger writes fail | both fan-out calls are attempted; ended session serves neither branch; safe aggregate plus restart reconciliation converges ledger state |
| two members share one outer asset | request-bound resolver/tickets return only each exact Derived artifact |
| forged/multi-value XFP | untrusted proxy evidence denied; no Secure-cookie downgrade/bypass |
| quota double claimant/crash/unlink failure | bucket ceiling preserved; occupied store bytes remain charged until locked inventory proves absence |
| Export key rotates/is lost | idempotency digest remains stable; no duplicate export/member request |
| cookie/proof/action/path/session replay | denied; no existence or secret logging |
| Range overflow/multi-range/budget race | bounded 4xx and conservative counters |
| path traversal/Unicode/device collision | deterministic sanitize/skip/fail per closed policy |
| Provider reports symlink/hardlink without target metadata | `skipped/link_metadata_unavailable`; no target inference, Provider byte read, link member or mutation |
| archive encrypted/bomb/link/device/malformed | typed failure; no mount/execution/host extraction |
| archive encrypted/unsupported/limit fallback, including ratio bomb | real ratio-bomb fixture persists generic `ProcessingErrorInputTooLarge`; backend maps every such code to closed limit; existing authorized original download requires fresh `asset.download`; denied/offline/unavailable is one no-leak closed reason; no Child 13 Recovery action |
| member ID from another index/asset | digest/ordinal mismatch, safe not-found; no processing interest or raw/reversible ID storage |
| member chain length >1 | `archive_nested_unsupported`; no Derived re-input |
| audit sink failure at publish/ticket | sensitive action fails closed; no unaudited access |
| disk unlink failure before/after publish | original execution outcome + cleanup/artifact purge_failed; cryptographically unavailable; store charge retained |
| schema down with empty DB but orphan file | ExportRuntime callback refuses before DB-only down until locked root is proven pristine |
| schema used then fully purged | permanent global quota-bucket latch remains; SQLite/PostgreSQL down refuse forever and require forward fix |

## 6. Exact Phase 2 Scope/Privacy Scans

Run equivalent exact scans before staging; inspect every match rather than
treating a raw count as sufficient:

```bash
git diff --name-only --diff-filter=ACMR
git diff --cached --name-only --diff-filter=ACMR
git ls-files --others --exclude-standard
test ! -e backend/xirang-server
rg -n '[T]ODO|[F]IXME|[T]BD|[P]LACEHOLDER|unknown as|\bany\b|fetch\(' \
  backend/internal/backupasset/export backend/internal/api/handlers/backup_*export* \
  web/src/lib/api/backup-exports-api.ts web/src/lib/api/backup-archive-api.ts \
  web/src/features/backup-assets
rg -n 'path|filename|member|selection|cookie|jwt|token|locator|credential|wrapped|nonce|fence' \
  backend/internal/backupasset/export backend/internal/api/handlers/backup_asset_export_handler.go
rg -n 'Write|Remove|Delete|Rename|Mkdir|OpenFile' backend/internal/backupasset/provider backend/internal/backupasset/export
find backend/internal/database/migrations/sqlite backend/internal/database/migrations/postgres \
  -maxdepth 1 -type f | sort | rg '00006(8|9)|00007(0|1)'
git diff --name-only | rg '^(deploy/|docker-compose|docs/|README\.md$|CHANGELOG\.md$|scripts/)'
git diff --name-only | rg '^backend/internal/backupasset/provider/' | \
  rg -v '^backend/internal/backupasset/provider/(restic\.go|rsync_preflight_test\.go|runner_test\.go)$'
```

Expected migration result is exactly four 068 files and zero 069-071. Expected
forbidden-scope result is empty. Code words such as `path` or `token` are not
automatically violations; prove every occurrence is encrypted, opaque,
sanitized or test-only and record the review.

Extract future/staging manifests from this file with a structured script or
exact line parser and compare sets; do not rely on visual counting. The exact
candidate set must union tracked diff, staged diff and
`git ls-files --others --exclude-standard`, then equal the approved manifest;
assert `backend/xirang-server` absent before comparison. Generated Swagger is
permitted only at its exact manifest path.

## 7. Rollback And Mixed-Version Behavior

### 7.1 Runtime Rollback

1. Stop new export/member admission and ticket issuance.
2. Increment job fences, cancel readers/workers and revoke/drain all export and
   affected Derived deliveries.
3. Destroy wrapped Export DEKs and encrypted selection references, then release/
   expire source leases and non-storage reservations.
4. Revoke member Derived references and run idempotent item-spool/final
   ciphertext cleanup; retain store-byte quota while any object remains, then
   release it only after unlink, parent fsync and locked inventory proof.
5. Keep safe execution outcomes, cleanup summaries/audit and additive schema.
   Provider bytes, Catalog,
   Search and downloaded client archives are untouched.

Disabling the UI alone is insufficient. Revoke/GC remains active until every
protected artifact is inaccessible or a real blocker is recorded.

### 7.2 Schema And Binary Rollback

An old binary must not start while active 068 rows/leases/keys/routes exist.
Global downgrade preparation blocks admission, drains/revokes, and proves no
old binary can interpret new state. ExportRuntime holds the validated root lock,
rejects any owned/unknown/unreadable entry, and only then invokes the 068 SQL
callback that checks pristine DB/lease/key state. Once used, retain schema and
ship a forward repair. The permanent global quota-bucket singleton is the
database proof of prior use and survives summary/GC/purge-to-empty; it is never
deleted to manufacture a pristine down.

New frontend against old backend maps unavailable/404 to closed feature state;
old frontend simply has no export UI. New backend stays default-disabled and
does not change existing Content/Catalog routes except the resolver branch that
returns safe not-found for non-export IDs. Child 15 later supplies durable
container volume/GA readiness; Child 12 does not claim it.

## 8. Approval And Delivery Ledger

| Gate/action | Current status | Required transition |
|---|---|---|
| baseline/branch/child creation | executed | none |
| current-main/parent/Child 7-11 research | executed | preserve evidence |
| focused planning docs/validation | authored; final planning checks passed | planning-only evidence recorded |
| PRD review | `complete_approved` | 2026-07-22 controller user reply “批准” |
| design/security/data review | `complete_approved` | 2026-07-22 controller user reply “批准” |
| implementation plan / 56 create + 67 modify manifest / matrix review | `complete_approved` | existing amendments plus the 2026-07-28 runner stdout pipe ownership amendment limited to `processing/capabilities/runner.go` and `runner_test.go` |
| deviations/refinements: thirteen-table 068 + permanent global-bucket use latch, zero-deadline create leases + exact returned caps, parent Step 9 key-before-lease rollback order, job key + immutable limits, full projection reset, spool, all links skipped for unavailable metadata, orthogonal cleanup/store quota, exact mux/TLS + independent 000068 budgeting + dual-ledger logout, digest-only member binding, API-only saved search + final Overlay/Search Tx validation, complete Repository settings fixture, generic `ProcessingErrorInputTooLarge` archive fallback with Child 13 recovery gate, runtime root-down proof, no credential grant, one-hop, route ID | `complete_approved` | 2026-07-22 controller user reply “批准” |
| planning workflow transition | `complete_approved` | same 2026-07-22 approval + user clarification |
| `task.py start` authorization | `complete_approved` | same 2026-07-22 approval + user clarification |
| Phase 2 implementation within exact manifest | `complete_approved` | same 2026-07-22 approval + user clarification |
| `task.py start` / status `in_progress` | `executed` | 2026-07-22; one start after clean preflight |
| red product tests | `executed` | genuine RED and focused GREEN ledger recorded |
| product/migration/test implementation | `executed` | exact-131 implementation is present; deterministic runner RED/GREEN and fresh aggregate verification are recorded |
| PostgreSQL lock-order/context/idempotency tranche | `closed` | controller fresh required `TestExportBehaviorPostgres` PASS `11.527s`; canonical `Q(global,user) -> J -> A -> I -> IA` order, context cancellation, collision fallback and required real-PostgreSQL selector evidence recorded |
| focused crypto/AAD review | `closed` | spec `✅`; quality `APPROVED`; zero findings |
| archive-profile sub-boundary | `closed` | independent current-code review: complete `AssetRef` + nonnegative root ordinal validation before allocation; collision allocator/limits final-member validation after scope prefixing/every retry; focused selectors passed; remaining P3 instrumentation coverage limitation is not a product failure |
| Step 10 exact manifest/static/review | `passed_current_follow_up_amended` | exact approved manifest `131`; Config rollback and Processing dual-boundary retry RED/GREEN plus fresh parity/static/full/race/PostgreSQL reruns passed; thirteen corrections unchanged |
| Step 10 dependency audit | `risk_accepted_for_child_delivery` | unchanged package files still report `1 moderate + 3 high`; controller explicitly and temporarily accepts the pre-existing risk; vulnerabilities are not fixed and audit did not pass; separate post-merge task/branch required |
| prior SQLite/PostgreSQL/restart/frontend/backend/full gates | `historical_checkpoint` | cannot substitute for fresh runner-amended package/race/full-project evidence |
| backend/full `env -u NODE_ENV make check` gates | `passed_current_runner_amended` | corrected `/tmp/xc12` rerun exit 0 after runner GREEN; generated binary removed and confirmed absent |
| Step 11 / delivery | `merged_post_merge_verified_archive_pending` | feature head `2e9e119` passed required checks; PR #399 squash-merged as `bd9572f` |
| exact staging / coherent work commit | `executed` | initial `94a15dc`, follow-up feature head `2e9e119`, squash merge `bd9572f` |
| `trellis-finish-work` / Child archive / journal | `not_executed` | only after ready/merge and post-merge monitoring; parent stays `planning` |
| archive/journal commit | `not_executed` | only actual post-merge workflow outputs may be inspected/staged |
| push / PR / required CI | `executed` | PR #399 required checks passed and PR merged |
| squash merge / post-merge CI and Release Please observation | `executed` | main CI `30344924877` and Release Please `30344927402` succeeded; no formal release/image/description publication expected |
| local main sync / branch-worktree cleanup | `executed` | local `main` synchronized to `bd9572f`; bookkeeping continues on a dedicated branch |
| Release Please PR #386 merge | `not_applicable` | explicitly forbidden here |
| Compose/deploy/release/publication | `not_applicable` | Child 15 or separate scope |

No row after planning validation may be relabeled `pass` unless its actual
future command/action has run and fresh output exists.
