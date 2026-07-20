# Child 11 Current-Main Evidence

## 0. Evidence boundary

This file records read-only Phase 1 research for
`.trellis/tasks/07-20-backup-assets-worker-capabilities`. It supports the
focused PRD, design and implementation plan; it is not implementation, test,
deployment or release evidence.

```text
evidence date:            2026-07-20 Asia/Shanghai
worktree:                 /home/murray/.codex/worktrees/c893/xirang
branch:                   codex/backup-assets-worker-capabilities
required baseline:        be6eebbe50dfd78e071c6d73e9c81493487fb4d5
HEAD:                     be6eebbe50dfd78e071c6d73e9c81493487fb4d5
main:                     be6eebbe50dfd78e071c6d73e9c81493487fb4d5
origin/main:              be6eebbe50dfd78e071c6d73e9c81493487fb4d5
task status:              planning
parent status:            planning
delivered program state:  10/15
planning package review:  approved by controller
Phase 2 implementation:   authorized by user
workflow transition:      pending controller release
task.py start:             not_executed
```

No product Go/TypeScript test, PostgreSQL integration, Compose start, image
build/scan, CI, release, deployment or remote mutation was run. Source
inspection below does not claim the future Child 11 behavior exists or passes.

## 1. Workflow, skills and project guidance loaded

The focused planning session loaded the complete Trellis session/start and
brainstorm instructions before planning, including:

```text
.agents/skills/trellis-start/SKILL.md
.agents/skills/trellis-brainstorm/SKILL.md
.trellis/workflow.md
```

The planning workflow requires a complex task to retain status `planning`
until PRD/design/implementation-plan review and a separate `task.py start`.
The user has explicitly authorized Phase 2 and the controller has independently
reviewed and approved the technical planning package. The current controller
directive keeps this amendment in planning until fresh evidence is returned;
the workflow transition, rather than implementation authority, is pending.

Relevant project specs were read before writing this package:

```text
.trellis/spec/backend/index.md
.trellis/spec/backend/directory-structure.md
.trellis/spec/backend/database-guidelines.md
.trellis/spec/backend/error-handling.md
.trellis/spec/backend/logging-guidelines.md
.trellis/spec/backend/quality-guidelines.md
.trellis/spec/backend/deployment-runtime.md
.trellis/spec/frontend/index.md
.trellis/spec/frontend/directory-structure.md
.trellis/spec/frontend/component-guidelines.md
.trellis/spec/frontend/hook-guidelines.md
.trellis/spec/frontend/state-management.md
.trellis/spec/frontend/type-safety.md
.trellis/spec/frontend/a11y-guidelines.md
.trellis/spec/frontend/quality-guidelines.md
.trellis/spec/guides/index.md
.trellis/spec/guides/branch-workflow-guidelines.md
.trellis/spec/guides/code-reuse-thinking-guide.md
.trellis/spec/guides/cross-layer-thinking-guide.md
.trellis/spec/guides/documentation-truth-guide.md
backend/internal/api/handlers/AGENTS.md
web/src/pages/AGENTS.md
```

The resulting plan uses response helpers, paired-migration ownership, structured
errors/logging, typed frontend API mappers, central request, lazy boundaries,
i18n/a11y and exact branch/PR gates. This Phase 1 changes no spec.

## 2. Git baseline, branch and task registration

### 2.1 Baseline proof

Read-only checks after branch creation report:

```text
git branch --show-current:
  codex/backup-assets-worker-capabilities
git rev-parse HEAD:
  be6eebbe50dfd78e071c6d73e9c81493487fb4d5
git rev-parse main:
  be6eebbe50dfd78e071c6d73e9c81493487fb4d5
git rev-parse origin/main:
  be6eebbe50dfd78e071c6d73e9c81493487fb4d5
```

The baseline commit is `feat: add backup asset worker protocol (#394)`, the
merged Child 10 delivery. The preceding commit is Child 9 / PR #393. No local
commit diverges from main and no remote mutation followed.

### 2.2 Child registration proof

The controller-authorized command ran exactly once:

```bash
python3 ./.trellis/scripts/task.py create "备份资产 Worker 能力" \
  --slug backup-assets-worker-capabilities \
  --parent 07-12-backup-data-explorer-design
```

Evidence:

- Child path is `.trellis/tasks/07-20-backup-assets-worker-capabilities`.
- Child `task.json` has `status=planning`, branch
  `codex/backup-assets-worker-capabilities`, base `main`, and parent
  `07-12-backup-data-explorer-design`.
- Parent `task.json` remains `planning` and adds exactly this Child after
  delivered Child 10.
- Eleven instantiated children do not mean eleven delivered children. Real
  delivery remains 10/15 and the parent cannot be archived.
- `task.py start` was not run.

## 3. Parent design and program boundary

The complete parent PRD/design/implementation plan were reviewed. Focused
source anchors are:

| Parent contract | Source lines | Child 11 consequence |
|---|---:|---|
| Optional Worker/topology/updater/capabilities/jobs/Derived/sandbox | `design.md:365` onward | Worker gets only Broker/Sink grants; updater identity/network is separate |
| File-type preview matrix | `design.md:451` onward | passive raster/text/document/media/archive output with explicit fallback |
| Authorization, step-up and audit | `design.md:467` onward | exact preview permission/ownership; malware/secret server gates; typed audits |
| Frontend information architecture/state | `design.md:516` onward | enhance the existing inspector/workspace, not a new top-level product |
| Asset/Search/Preview API boundary | `design.md:740` onward | strict DTOs, exact AssetRef, same Content Broker delivery surface |
| Deployment/config/observability | `design.md:793` onward | optional Worker, default-off, bounded labels and independent updater |
| Failure close/degradation | `design.md:863` onward | no Worker leaves Core useful and quiet |
| Verification strategy | `design.md:883` onward | malicious files, DB parity, sandbox, UI and rollback evidence |
| Child 11 coarse execution section | `implement.md:990` onward | capabilities, updater, image, Compose, atomic Search, enhanced UI |

Parent implement section 12 is deliberately coarse. Merged main requires these
focused additions:

1. production capability registry and Worker runner/materializer seams from
   Child 10;
2. a transaction-aware Child 7 Search port because current methods start their
   own transaction;
3. a Derived representation resolver into Child 8 Content Broker;
4. runtime/router/generated Swagger paths plus the existing server listener
   composition root for a separate updater UDS;
5. lazy processing API/controller plus the Backups data-page auth boundary to
   preserve Child 9 bundle headroom; and
6. exact script self-tests and no-publication CI checks.

The focused plan also removes parent section 12's public deployment-document
edits from Child 11. This Child builds/tests an optional unpublished image but
does not establish a public release/deployment contract before Child 15.

## 4. Child 10 schema and closed contracts

### 4.1 Paired migration evidence

Both engines contain the same migration version:

```text
backend/internal/database/migrations/sqlite/000067_backup_asset_processing.up.sql
backend/internal/database/migrations/sqlite/000067_backup_asset_processing.down.sql
backend/internal/database/migrations/postgres/000067_backup_asset_processing.up.sql
backend/internal/database/migrations/postgres/000067_backup_asset_processing.down.sql
```

No `000068`, `000069`, `000070` or `000071` migration exists. Those
versions remain reserved for Children 12-15. Child 11 has no DDL requirement
because 000067 already stores jobs/interests/attempts/grants/uploads, Worker
identities/capabilities, Derived blobs/sets/artifacts/references and generic
updater metadata.

### 4.2 Model evidence

`backend/internal/model/backup_asset_processing.go` proves:

- lines 7-43: job stores work identity, exact point/catalog/entry, source and
  entry fingerprints, capability/schema/pipeline/profile/policy, priority,
  state/revision/error, current attempt/set and deadlines;
- lines 47-161: interests, attempts, one-use grants, grant requests and staged
  uploads are persisted without public JSON serialization;
- lines 163-199: transport-derived Worker identity and canonical capability
  advertisements are persisted;
- lines 201-284: encrypted Derived blob, artifact set, artifact and per-point
  authorization reference exist;
- lines 286-302: updater metadata contains only source/version/digests/signing
  fingerprint/state/failure/timestamps.

Models use `json:"-"`; handlers must return sanitized domain DTOs. Child 11
does not modify this model file or any migration.

### 4.3 State/error/updater contract evidence

`backend/internal/backupasset/processing/state.go:8-108` defines the closed
processing states and transitions. Lines 110-153 define only these stable error
families:

```text
permanent:
  unsupported_format, encrypted_archive, input_too_large,
  materialization_disabled, source_changed, source_expired
transient:
  worker_unavailable, provider_unavailable, quota_busy, timeout,
  worker_crash, lease_lost
contract/security:
  protocol_incompatible, invalid_output, digest_mismatch,
  sandbox_violation, network_violation
```

`backend/internal/backupasset/processing/contracts.go:29-154` defines updater
source kinds, registered/verified/active/superseded/failed states and only four
failure codes: `invalid_signature`, `unsupported_version`,
`policy_rejected`, and `activation_failed`. Its metadata DTO explicitly has
no URL, credential, key, manifest body, bundle bytes or raw output.

Lines 160-254 define `CanonicalParametersV1` and `WorkDescriptorV1`.
Source/entry fingerprint, capability/schema, pipeline fingerprint, output
profile, security policy revision and all output-affecting parameters are
already part of work identity. Child 11 does not need a priority or work-key
schema change.

`processing/scheduler.go:20-38` already reserves interactive/background slots,
but lines 48-62 currently sort every interactive candidate before background
priority. The frozen Child 11 order is latest/interactive/recent/history, so the
focused scheduler change must compare existing effective-priority bands first
while retaining slot admission and skipping candidates whose class is full.
`coordinator.go:27-68` plus the 000067 interest model provide a random private
interest ID and workspace owner tuple; that existing ID can be the public
user-scoped preview-job handle after ownership/AssetRef revalidation, avoiding
raw job IDs, a new key domain or schema.

## 5. Protocol, Worker and Derived publication evidence

### 5.1 Protocol registry and limits

`backend/internal/backupasset/processing/protocol.go:43-103` defines common
capability ceilings and advertisement fields: input/output bytes/count,
pages/pixels/duration/expanded bytes, capability/schema, pipeline fingerprint,
profile and input modes.

The production gap is explicit:

- `protocol.go:1183-1185` returns an empty
  `NewProductionCapabilityRegistry`.
- `worker_client.go:494-496` returns an empty
  `NewProductionWorkerCapabilitySet`.
- `protocol.go:1232-1328` already rejects incompatible protocol,
  unsupported/duplicate capability tuples, invalid modes and out-of-range
  common limits.
- `worker_client.go:664-714` runs heartbeat and one-use Input activation but
  lines 694-700 currently fail every materializing job with
  `materialization_disabled`.

Therefore Child 11 should fill the existing production registry/set and secure
materialization path, not invent another transport or grant protocol.

### 5.2 Artifact roles and current two-step publication

`backend/internal/backupasset/processing/derived_manifest.go:32-39` limits
roles to `noop`, `content`, `ocr`, `thumbnail`, and `metadata`.
Lines 951-993 validate role/MIME, completeness and the existing canonical
coverage envelope. Child 11 reuses the roles and extends only the closed
capability/profile MIME validation in Go; no persisted enum is added.

The atomicity gap is visible in the same file:

- lines 580-715 lock/revalidate the current job/attempt/grant/fence, create the
  Derived set/artifacts/references and leave projection-required work in
  validating;
- lines 718-791 later open another transaction to mark projection published,
  complete the job/attempt and release the lease.

This is safe while production projection is disabled, but it cannot satisfy
Child 11's required single Derived/Search publication transaction.

### 5.3 Runtime production gaps

`backend/internal/backupasset/runtime/processing_runtime.go:220-250` composes
the Child 10 Sink/reconcilers and currently constructs the empty production
capability registry. Lines 757-776 define
`runtimeDerivedProjectionPort`; both Publish and Revoke return the closed
projection-disabled error.

`backend/internal/backupasset/runtime/runtime.go:469` already injects that
port with the Child 7 Search ingest instance. This is the correct composition
point for a transaction-aware adapter; handlers or Worker commands must not
construct a second runtime.

## 6. Child 7 Search and Child 8 Content evidence

### 6.1 Search ingest port

`backend/internal/backupasset/search/ingest.go:63-66` exposes the stable
`ContentIndexIngest` Publish/Revoke port. Current
`PublishContentProjection` at lines 114-205 starts its own GORM transaction,
replaces content/OCR postings, updates sensitivity/excerpt/coverage and advances
the projection revision. Revoke also starts its own transaction.

Lines 254-315 prove `lockAndValidate` requires:

- the `processing_job` RecoveryPoint lease holder;
- exact attempt/fence;
- current source fingerprint and eligible point;
- active matching Catalog and Search generations;
- active Search token key version; and
- current document/field classification revisions.

The focused design preserves every check but exposes an internal method that
accepts the outer processing transaction. Search remains unaware of Derived
storage and processing remains the transaction coordinator.

`backend/internal/model/backup_asset_search.go:15` already stores a
generation projection revision and line 77 stores each field's integer
`pipeline_revision`. Current Search evaluation at
`search/service.go:900-927` checks sensitivity and coverage state but
does not compare the field with an active content/OCR pipeline revision. Child
11 adds that runtime revision source so a bundle activation can make old
postings logically unavailable in one bounded transaction, then physically
revoke/rebuild them in resumable batches without new DDL.

### 6.2 Content Broker and delivery surface

Child 8 already provides:

- `content/attempt_broker.go`: attempt-bound stat/sequential/range reads for
  Worker Input grants;
- `content/broker.go`: authorized browser delivery, budgets, mutable-source
  revalidation, Range and cleanup;
- `content/ticket.go`: short-lived ticket/cookie binding; and
- `/api/v1/asset-content/:deliveryId`: same-origin GET/HEAD byte route.

`backend/internal/api/router.go:348-361` registers the content route with the
content-safe recovery middleware. Lines 392 onward issue exact
RecoveryPoint/entry delivery tickets under `backup_assets:preview`.

Child 11 can add a narrow Derived representation resolver behind the existing
Broker and ticket. A new public blob URL would bypass current authorization,
audit, Range and lifecycle semantics and is therefore rejected.

## 7. API, settings, audit and Provider boundary evidence

### 7.1 Existing Worker API

`backend/internal/api/worker_router.go:19-30` defines the private protocol
interface. Lines 58-80 register only
`/internal/v1/asset-worker/{handshake,leases,jobs,input,sink,drain}` routes.
Every route is bound to transport identity and per-identity rate labels; JSON
and artifact body limits are configured separately.

`backend/internal/api/handlers/backup_worker_handler.go:15-66` exposes one
Admin-only sanitized summary service. It rejects query/body input, checks
processing feature configuration, uses response helpers and never returns raw
errors. `router.go:382` applies Admin role, 30/min rate and one-byte body
limit.

This is the base for bounded Admin capability/coverage/updater surfaces. Asset
preview jobs require their own exact AssetRef handler and normal preview RBAC,
not Admin.

`backend/cmd/server/main.go:227,433-500` starts authenticated Worker listeners
before the public server and owns their shutdown. A separately authenticated
updater router cannot become live merely by creating a handler; the focused
manifest therefore includes `cmd/server/main.go` and `main_test.go` so its
dedicated UDS listener participates in the same startup/shutdown ordering
without sharing the Worker or public listener.

### 7.2 Feature settings

`backend/internal/settings/service.go:173` keeps
`backup_assets.enabled=false`. Lines 280-309 contain Child 10 queue/grant/Sink
limits and show:

- `worker_local_enabled=false` at line 296;
- `worker_remote_enabled=false` at line 298;
- socket/certificate/trust and Derived root settings require restart; and
- settings already bound input, Sink and Derived bytes/counts.

`backend/internal/backupasset/service.go:169-226,526-612` owns the typed
`ProcessingConfig` and resolves one complete atomic snapshot; its tests at
`service_test.go:484-574` reject missing or inconsistent processing keys. The
future manifest therefore includes both production/service fixture files when
adding updater/backfill/secret-policy registry fields; runtime and handlers
must not read those keys independently. The repository package maintains a
separate explicit Foundation snapshot fixture, whose later synchronization is
recorded in Section 15.

Child 11 adds only settings-registry entries for updater enablement/exact online
origins and dynamic backfill/secret policy. Bundle/inbox/trust-file paths are
fixed runtime mounts, no credential becomes a setting, and all feature defaults
stay false.

`backend/internal/api/handlers/config_handler.go:66-98` already validates
imported settings through the public registry, so reserved non-registry pipeline
keys are rejected. Lines 293-308 currently export every `system_settings` row,
including unknown rows, and therefore require an explicit unconditional filter
for Child 11's two internal revision keys. The focused manifest includes that
handler/test change so internal publication state cannot enter config export or
round-trip through operator configuration.

### 7.3 Audit and Provider boundaries

`backend/internal/backupasset/audit_action.go:65-73` already registers
`preview_job`, `preview_ticket`, `preview_read`,
`processing_policy_update`, `archive_inspect` and `archive_member`.
No new audit enum/migration is required.

`backend/internal/backupasset/domain.go:200` defines
`task_artifact_contract_missing`; lines 343 and 379 onward keep Command
Provider unavailable without a typed artifact contract. Child 11 does not add
one. No Provider, SSH, Restic, Rclone or Command byte/locator/credential path is
in the focused implementation manifest.

## 8. Frontend and bundle evidence

### 8.1 Native preview baseline

Current frontend behavior is already safe and useful:

- `web/src/features/backup-assets/asset-preview-model.ts:13-21` chooses native
  metadata/hex, same-origin PDF, safe raster, audio, video or escaped text by a
  closed MIME map.
- `asset-preview.tsx:180-250` renders raster, native media or an empty-sandbox
  iframe and detaches/renews tickets. Active HTML/SVG is not a renderer.
- `backup-content-api.ts:23-61` maps closed renderer/profile/content-type
  tuples and uses only the central API boundary.
- `asset-inspector.tsx:11-18` already has preview/metadata/versions/security/
  evidence/diff tabs with keyboard/focus semantics.

Enhanced UI must preserve this native path while processing loads, is absent or
fails.

### 8.2 Typed and lazy API pattern

`web/src/lib/api/backup-assets-api.ts:1-27` imports domain types, central
`request` and shared boundary mappers. Raw response objects are checked before
mapping snake_case to camelCase.

`web/src/lib/api/client.ts:40-71` uses dynamic imports for Search, Overlays and
Content; lines 73-134 expose stable lazy method adapters. Child 11 follows this
boundary principle with a separate processing API imported by the lazy
controller. It leaves the eager client and catalog API unchanged to protect the
remaining startup budget.

`web/src/lib/api/core.ts:18-26,52-70` proves the normal request boundary accepts
an `unknown` body but always serializes it as JSON and sets
`Content-Type: application/json`; it has no streaming, `FormData` or multipart
contract. Therefore a browser-to-Core 1 GiB offline bundle upload would both
contradict the central API boundary and require eager/request infrastructure
expansion. The focused design instead uses an updater-only fixed inbox and lets
the lazy Admin UI send only bounded JSON candidate scan/activation controls.

`web/src/pages/backups-page.data.tsx:40-76` is the current auth/context boundary:
it reads token and `ensureStepUpProof`, passes them into the backup-assets state
controller, and renders `BackupAssetsWorkspace`. The focused manifest includes
that page and its test so token/role/step-up can be passed as narrow props to
the independently lazy preview and Admin processing panels; the panels do not
create another auth source.

### 8.3 Bundle headroom

Fresh Child 9 delivery evidence at
`.trellis/tasks/archive/2026-07/07-19-backup-assets-workspace-ui/research/implementation-evidence.md:113`
records:

```text
main JavaScript: 498.09 / 500.00 KiB
main CSS:        104.21 / 105.00 KiB
```

That leaves about 1.91 KiB JS and 0.79 KiB CSS. Current
`web/scripts/check-bundle-budget.mjs:15-16` retains the 500/105 KiB limits.
The processing controller/panel and API must be separate lazy chunks; the
budget cannot be raised for Child 11.

## 9. Runtime, Compose and CI evidence

`docker-compose.yml:1-30` currently defines only the official `xirang`
service:

- image `linnea7171/xirang:${IMAGE_TAG:-latest}`;
- public port `10761:10761`;
- current healthcheck and data/backup/log mounts.

There is no Worker profile, Worker image or updater service. The all-in-one
image is `deploy/allinone/Dockerfile` and is intentionally outside Child 11's
future manifest.

`.github/workflows/ci.yml` currently runs:

- backend lint/test/coverage/build/vulnerability checks;
- real PostgreSQL migration/Catalog/Search/Overlay/Content/Processing parity;
- frontend check and bundle budget;
- official all-in-one Docker build; and
- documentation and migration UTC safety.

Lines 196-210 build only the all-in-one image. Child 11 may add an amd64/arm64
Worker build/scan job, but no registry login, push, tag or release action.
`.github/workflows/publish-images.yml` remains outside scope.

## 10. Frozen design implications and deviation record

The source evidence leads to the following decisions:

1. use only 000067 and existing role/error/state storage contracts;
2. place closed profile types in an import-cycle-free `capabilityspec`
   package, then fill the existing registry/materialization seams rather than
   create a parallel Worker protocol;
3. keep tool-specific reasons in bounded typed DTOs mapped to existing codes;
4. use current interactive/background plus effective priority for backfill;
5. compose bundle fingerprints into the existing pipeline/work identity and
   advance internal content/OCR revisions for immediate logical invalidation;
6. add a transaction-aware Search port so Derived/Search publish/revoke under
   one outer transaction and fence;
7. deliver derived bytes through the existing Content Broker ticket/cookie;
8. use an independent updater identity, signed offline-first bundle contract,
   updater-only fixed inbox/optional allowlisted egress, JSON-only Admin
   candidate control using the existing opaque updater-metadata row ID,
   canonical non-self-referential manifest/tar payload, content-addressed store
   and crash-recoverable atomic activation;
9. put processing API/state/UI behind dynamic imports; and
10. add only an optional unpublished Worker profile, leaving Core and public
    release contracts unchanged.

Scope deviations from the parent coarse Section 12 are documented in
`prd.md` Section 7 and `implement.md` Section 2. The focused amendment restores
the four public documentation files assigned by the parent and adds
`backend/README_backend.md` for the current router freshness rule; this is a
documentation-truth correction, not product expansion. There are no unresolved
product decisions, the planning package is approved, and Phase 2 is authorized.
Any later change to these decisions or the exact future manifest requires a new
focused scope review.

## 11. Phase 1 worktree and execution status

Current `git status --short --branch`:

```text
## codex/backup-assets-worker-capabilities
 M .trellis/tasks/07-12-backup-data-explorer-design/task.json
?? .trellis/tasks/07-20-backup-assets-worker-capabilities/
```

The diff consists only of the parent child registration and Child 11 planning
package. Final planning validation and scope-scan results are recorded after
the documents and JSONL manifests are complete.

```text
planning package review:              approved by controller
Phase 2 implementation authorization: approved by user
workflow transition:                  pending controller release
task.py start:                       not_executed
product code/test/migration edits:   not_executed
stage/commit/archive/journal:        not_executed
push/PR/CI/merge/post-merge:         not_executed
Docker build/publish/deploy/release: not_executed
remote mutation:                    not_executed
```

## 12. Pre-amendment Phase 1 planning validation

These are planning-package checks only. They do not claim that any future Go,
TypeScript, database, container or CI behavior has been implemented or tested.

The required Trellis validator ran after the focused documents and JSONL were
complete:

```bash
python3 ./.trellis/scripts/task.py validate \
  .trellis/tasks/07-20-backup-assets-worker-capabilities
```

Result: exit 0; `implement.jsonl` has 23 valid entries, `check.jsonl` has 24
valid entries, and the validator reported `All validations passed`. A separate
`jq -e` parse succeeded for both JSONL streams; their 26 unique referenced files
all exist.

Baseline/task checks used:

```bash
git branch --show-current
git rev-parse HEAD
git rev-parse main
git rev-parse origin/main
git merge-base HEAD main
python3 ./.trellis/scripts/task.py current
jq -r .status .trellis/tasks/07-20-backup-assets-worker-capabilities/task.json
jq -r .status .trellis/tasks/07-12-backup-data-explorer-design/task.json
git status --short --branch
```

Result: branch is `codex/backup-assets-worker-capabilities`; HEAD, main,
origin/main and merge-base are all
`be6eebbe50dfd78e071c6d73e9c81493487fb4d5`; current task resolves to this
Child; Child and parent are both `planning`; the parent has 11 instantiated
children while delivered program state remains 10/15. Status contains only the
parent registration plus the untracked Child planning directory.

The current-path scope scan combined tracked and untracked names:

```bash
{ git diff --name-only; git ls-files --others --exclude-standard; } | sort -u
```

Result: exactly the eight paths listed in `implement.md` Section 1; zero paths
under `backend/`, `web/`, `deploy/`, `scripts/`, `docs/` or `.github/`.

Migration and future-manifest scans used `find` for `000068` through `000071`,
`git diff --name-only` plus untracked-name checks for migration/model paths,
and extracted every path from `implement.md` Section 2 before sorting and
checking duplicates/forbidden patterns. Result:

```text
000068-000071:                 absent
current migration/model edit: none
future exact paths:            156
future duplicate paths:        0
future migration paths:        0
future deploy/allinone paths:  0
future publish/release paths:  0
docker-compose.yml entries:    1
```

Text scans covered `prd.md`, `design.md`, `implement.md` and this evidence file.
Case-insensitive template/incomplete-marker patterns, the temporary backtick
substitution sentinel, approval/status contradictions and odd whole-file
backtick parity all returned zero. The broad offline-import scan
returned 15 mentions; each was inspected and is either an explicit rejection
of browser/Core bundle upload, multipart/FormData/file input/URL/server-path
input, the current central JSON-boundary evidence, or the future forbidden-path
scan. No mention authorizes such a transport.

Whitespace checks ran:

```bash
git diff --check
git ls-files --others --exclude-standard -- \
  .trellis/tasks/07-20-backup-assets-worker-capabilities
# For every name above, capture and require empty output from:
git diff --no-index --check /dev/null "$file"
```

Result: tracked diff check exit 0 and no `--check` output for any untracked
Child file.

These results predate the controller's focused authorization/documentation
amendment. The historical 156-path result is retained as provenance, but it is
superseded for current authorization and manifest count by the fresh amendment
evidence recorded in Section 13.

## 13. Focused authorization and documentation amendment evidence

The user explicitly authorized Phase 2, and the controller completed technical
review and approved the planning package without changing its product, schema,
API, trust, transaction, runtime or publication decisions. The controller's
current workflow directive still requires this Child to stay in planning until
this evidence is returned:

```text
planning package review:              approved
Phase 2 implementation authorization: authorized
Child status:                         planning
parent status:                        planning
workflow transition:                  pending controller release
task.py start:                        not_executed
product implementation/tests:         not_executed
migration/stage/commit/push/PR/CI:     not_executed
```

The amendment restores the four documentation paths assigned by parent
implement Section 12 and adds the backend router documentation required by the
current freshness rule:

```text
backend/README_backend.md
docs/deployment.md
docs/env-vars.md
docs/admin/backup-recovery.md
docs/admin/security.md
```

This documentation-truth correction historically raised the exact future
manifest from 156 to 161 paths. The later atomic Derived/Search reconciliation
correction in Section 14 supersedes 161 as the current authorized count. The
documentation amendment did not add a migration/model path, root README,
all-in-one image path, release/publish workflow or public image contract. The
documents are limited to default-off settings, optional local build/Compose
profile, security, rollback and no-Worker degradation.

The fresh manifest boundary used the controller-reviewed extraction:

```bash
awk '/^## 2\. Exact future implementation manifest/{f=1;next} /^## 3\./{f=0} f && /^(backend\/|deploy\/|docker-compose\.yml$|\.github\/|scripts\/|web\/|docs\/)/ {print}' implement.md
```

Result:

```text
future exact paths:                         161
future unique paths:                        161
future duplicate paths:                     0
future documentation-truth paths:           5
future migration/000068-000071/model paths: 0
future deploy/allinone paths:               0
future release/publish/root-README paths:   0
docker-compose.yml entries:                 1
```

Fresh planning-package validation before this final evidence write reported:

- `task.py validate`: exit 0, `implement.jsonl` 23 entries,
  `check.jsonl` 24 entries, all validations passed.
- JSONL independent parse/count: 23/24, both exit 0.
- `git diff --check`: exit 0.
- `git diff --no-index --check /dev/null PATH`: seven untracked Child files
  checked, zero output/failures.
- current working-tree scope: exactly the eight planning paths in
  `implement.md` Section 1, zero product paths.
- paired `000067_backup_asset_processing`: four SQLite/PostgreSQL up/down
  files; `000068` through `000071`: zero; current migration/model edits: zero.
- stale negative authorization claims, false current execution-state claims,
  template/incomplete-marker/sentinel patterns and odd whole-file backtick
  parity: zero.
- branch is `codex/backup-assets-worker-capabilities`; HEAD, main, origin/main
  and merge-base are all `be6eebbe50dfd78e071c6d73e9c81493487fb4d5`;
  current task resolves to this Child and both Child/parent remain `planning`.

After this last planning-document edit, the same validator, whitespace,
scope/manifest, migration, JSONL, authorization/template-marker and baseline checks
must receive one immutable rerun with no intervening edit. Its results belong
in the controller handoff; writing them back here would itself invalidate that
immutable evidence.

## 14. Phase 2 atomic reconciliation manifest amendment

The controller independently reviewed the only product-path delta from the
161-path authorization and approved exactly these additions:

```text
backend/internal/backupasset/processing/reconciler.go
backend/internal/backupasset/processing/reconciler_test.go
```

This is the **atomic Derived/Search reconciliation correction**, not a product
scope expansion. Missing or unreadable Derived blob repair now uses the same
managed `processing_job` fence and caller transaction as Derived lifecycle
revocation, so Search projection revocation succeeds before Derived state,
reference, key or blob mutation. A Search revoke failure leaves Derived state
unchanged and immediately retryable.

```text
planning package review:              approved
focused manifest amendment:           approved
Child status:                         in_progress
parent status:                        planning
workflow transition:                  completed
task.py start:                        completed
product implementation/tests:         in_progress
migration/stage/commit/push/PR/CI:     migration forbidden; remaining actions not_executed
future exact paths:                    163
future unique paths:                   163
future duplicate paths:               0
```

The two reconciler paths are the only additions at this amendment checkpoint.
They do not alter migration, model, Provider bytes or credentials,
release/publication, feature defaults, Command Provider support, or Child 12-15
ownership. Section 15 records the later independently approved repository
fixture synchronization that supersedes 163 as the current manifest count.

## 15. Phase 2 repository Foundation fixture manifest amendment

The full backend gate exposed one stale explicit test fixture after Child 11
added 12 processing/backfill/updater keys to the Foundation settings snapshot.
The controller independently reproduced the failure and approved exactly this
additional path:

```text
backend/internal/backupasset/repository/testutil_test.go
```

`repositoryFoundationDefaults` had 137 keys while the maintained
`staticFoundationDefaults` and `BackupAssetFoundationSettingKeys()` contract had
149. Their exact difference was the 12 Child 11 keys, and every value matched
the settings registry CodeDefault. `repositorySettings.BackupAssetSettingsSnapshot`
intentionally returns only the explicit fixture map, while
`FoundationService.atomicFoundationValues` intentionally rejects incomplete
snapshots; neither production contract may be relaxed or given implicit
defaults.

This **repository foundation settings fixture synchronization** adds only the
12 frozen defaults to the explicit repository test fixture. It is not a product
scope expansion and preserves the historical 161-to-163 atomic Derived/Search
reconciliation correction recorded in Section 14.

```text
planning package review:              approved
atomic reconciliation amendment:      approved (161 -> 163)
repository fixture amendment:         approved (163 -> 164)
Child status:                         in_progress
parent status:                        planning
workflow transition:                  completed
task.py start:                        completed
product implementation/tests:         in_progress
migration/stage/commit/push/PR/CI:     migration forbidden; remaining actions not_executed
future exact paths:                    164
future unique paths:                   164
future duplicate paths:               0
```

The fixture path does not alter migrations, models, Provider bytes or
credentials, public API, root README, all-in-one image, release/publication,
feature defaults, Command Provider support, or Child 12-15 ownership. Any later
path delta from the 164-path exact manifest requires another focused amendment
and review.

## 16. Phase 2 closed-profile parity correction

The controller retained the complete closed capability advertisement and
approved a correction inside the existing 164 paths. This is the
**closed-profile advertisement/preflight/executable parity correction**, not a
manifest amendment or product-scope expansion:

- archive gzip/xz/zstd continues through fixed absolute pipe-only sandbox
  decompressor profiles, while a bounded single-stream validator and the TAR
  consumer jointly reject MIME/magic mismatch, truncation, trailing bytes,
  concatenated streams, ratio/expanded bombs and cancellation leaks;
- all advertised thumbnail/OCR raster MIME rows have a real closed libvips or
  normalized libvips-to-Tesseract execution path;
- the Worker physically advertises and executes optional `secret.classify`,
  while Core scheduling, inventory and Search publication remain gated by
  `processing_secret_classify=false` and fenced Derived/Search identity;
- bounded OOXML/ODF ZIP/XML preflight rejects macro/script/external-link
  packages before LibreOffice and never returns a target, path or raw XML.

The compressed-stream review exposed that the three external CLIs accept a
valid stream followed by an empty second stream with exit code zero. A fresh
RED test reproduced successful acceptance for gzip, xz and zstd. The minimal
GREEN keeps decompression in `ProductionToolRunner`, streams output into TAR,
adds no temporary file or module, and verifies exactly one compressed
stream/frame concurrently. Fresh focused tests then passed for all three
inspect/extract paths, concrete truncated/trailing/empty-second-stream cases,
ratio/cancel/join, capability profiles, document/image/secret adapters and the
asset-tool-sandbox fixed invocations.

```text
closed-profile parity correction: approved; no path amendment
future exact paths:              164
future unique paths:             164
future duplicate paths:          0
go.mod/go.sum changes:           0
migration/model/000068-000071:   0
product implementation/tests:    in_progress; full gate pending
stage/commit/push/PR/CI:         not_executed
```

The 161-to-163 atomic reconciliation and 163-to-164 repository fixture
amendments remain separate historical facts. Any later product-path delta still
requires a focused amendment before staging or committing.

## 17. Phase 2 implementation and pre-commit verification evidence

Child 11 implementation is complete inside the approved 164-path product
manifest. The closed-profile correction retained archive/image/secret/Office
advertisements and made each advertised path executable or fail closed before
tool execution. No additional manifest amendment was required after the two
historical amendments in Sections 14 and 15.

### 17.1 Identity, scope and frozen boundaries

Fresh pre-commit checks reported:

```text
task.py validate:                  pass (implement.jsonl 23; check.jsonl 24)
Child status:                      in_progress
parent status:                     planning
branch:                            codex/backup-assets-worker-capabilities
HEAD/main/origin/main/merge-base: be6eebbe50dfd78e071c6d73e9c81493487fb4d5
exact manifest:                    164/164 unique; duplicate 0
current product paths:             158; outside manifest 0
planning artifacts:                8/8
approved manifest paths no diff:   6
staged paths:                      0
tracked/untracked whitespace:      0/0
exact-manifest Go formatting:      113 files; gofmt diff 0
go.mod/go.sum changes:             0
forbidden product paths:           0
000067 paired files:               4
000068 through 000071 files:       0
backend/xirang-server:             absent after exact artifact cleanup
```

The six approved paths without a final diff are the existing Content
`renderer`/`ticket` production and test files and frontend `asset-inspector`
production and test files. They remain authorized but were not changed. There
is no migration/model, Provider byte/locator/credential, root README,
`deploy/allinone`, release/publish, or Child 12-15 product path. The settings
registry still has `backup_assets.enabled`, local/remote Worker, updater,
updater online mode and `processing_secret_classify` defaulting to `false`;
backfill remains paused by default. Command capability tests still return
`task_artifact_contract_missing`.

`backend/xirang-server` was generated only by the required backend build and
then deleted with the controller-approved exact cleanup. It was never added to
the manifest, ignore rules or staging area. This is build-artifact hygiene, not
a scope deviation or a test result.

### 17.2 Functional and security verification

Fresh successful rows:

- `make lint-backend` with writable isolated cache: `0 issues`; `go vet ./...`
  passed; the versioned backend build passed.
- The complete `capabilityspec`, `capabilities`, Search, repository, settings
  and Provider packages passed. The closed adapter matrix passed archive
  inspect/extract for gzip/xz/zstd, single-stream truncation/trailing/
  multistream and ratio/cancel checks, every advertised raster path, bounded
  OOXML/ODF preflight, optional physical secret execution, malicious fixtures,
  and no-Worker degradation.
- The exact atomic reconciler regressions passed, and the broader
  DerivedLifecycle/Search projection/classification transaction matrix passed.
  Secret evidence, Search classification and Derived state share the caller
  transaction/fence; stale or failed publication does not leave a ghost
  projection.
- Environment-independent package coverage passed with the blocked tests
  explicitly excluded: Content 123/124 top-level tests, Runtime 85/86,
  Processing 143/150 and updater 24/26. The same explicit set passed under
  `go test -race` across processing/capabilityspec/capabilities/updater,
  Content and Search.
- Child 11 handler, router, Worker/updater/server command, ownership/RBAC,
  sanitized Admin DTO, preview job/poll/cancel and default-off tests passed.
- `env -u NODE_ENV npm run check` passed typecheck, lint, all 162 test files and
  all 919 tests, then built production assets. The independent bundle gate
  measured main JS 498.14 KiB of 500 KiB and CSS 104.55 KiB of 105 KiB; the
  processing and coverage panels remain separate lazy chunks.
- Compose/static script self-tests, `bash -n`, seccomp JSON parsing and the
  concrete Compose contract checker passed. Static scans found zero Worker
  login/push/release actions; CI retains only the amd64/arm64 local build,
  runtime smoke and Trivy scan job.
- Public processing/Admin DTO scan found zero Worker/grant/attempt/fence,
  credential, path, activation-secret or raw-output fields. The offline import
  scan found zero `FormData`, multipart, file input, Blob bundle transport or
  caller-selected server/inbox path. Documentation continues to state
  default-off, non-GA, local-only Worker operation.
- Swagger was generated twice with no drift; before/first/second `docs.go`
  SHA-256 was
  `477c6ace7631dbe1002ff1a0efc42ef0eea8b60758397ea6274a748570566559`.
  Documentation freshness passed over all 166 current changed paths.

### 17.3 Required rows blocked by this execution environment

These rows are deliberately **not** recorded as pass:

- `env -u NODE_ENV GOCACHE=/tmp/codex-go-build-child11 make check` reached and
  completed lint, then exited 2 in backend tests. A minimal Python probe
  independently showed both AF_INET and AF_UNIX socket creation/bind failing
  with `EPERM`. The full Go run therefore failed existing httptest users in
  alerting, API handlers, metrics and uptime, plus the seven processing Worker
  transport/client tests and two updater peer-credential listener tests.
- `/proc/self/mountinfo` exposes two equally specific `/tmp` records for the
  same tmpfs identity, one `ro` and one `rw`. The cache mount verifier correctly
  fails closed, blocking one Content test and its one Runtime integration
  consumer with `cache_root_unverified`.
- PostgreSQL required mode was run with both required flags and no DSN. It
  failed explicitly with `TEST_POSTGRES_DSN is required` for Processing and
  Search; real PostgreSQL parity is `not_executed`, not skipped-as-pass.
- `docker info` could not access `/var/run/docker.sock`. Actual core-only and
  Worker runtime smokes also lack the required prebuilt local image variables,
  so Docker runtime, sandbox socket/setsockopt denial, amd64/arm64 image build
  and image scan remain environment/CI-blocked. Their script self-tests are not
  substitutes for runtime evidence.
- `actionlint` is not installed locally. Workflow syntax and the Worker image
  build/scan matrix therefore remain required CI evidence.

There were no product edits after the successful focused/race/frontend/
Swagger/security checks other than this concrete Trellis ledger update and the
paired correction in `implement.md`. Stage, work commit, archive commit,
journal commit, push, PR, required CI, squash merge and post-merge automation
remain pending until the immutable scope/whitespace gate is rerun.
