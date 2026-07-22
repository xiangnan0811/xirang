# Backup Asset Worker Capabilities And Enhanced Preview Implementation Plan

> **Execution authority:** the controller has approved this planning package,
> the user has explicitly authorized Phase 2, and the workflow transition has
> completed. This file is the approved future execution checklist. The plan
> remains inline and does not dispatch implementation/check sub-sessions.

## 1. Execution authority, baseline and ledger

```text
task:                         .trellis/tasks/07-20-backup-assets-worker-capabilities
status:                       in_progress
parent:                       07-12-backup-data-explorer-design (planning)
branch:                       codex/backup-assets-worker-capabilities
base branch:                  main
required baseline:            be6eebbe50dfd78e071c6d73e9c81493487fb4d5
HEAD/main/origin/main:        be6eebbe50dfd78e071c6d73e9c81493487fb4d5
delivered program state:      10/15
planning package review:      approved by controller
Phase 2 implementation:       authorized by user
workflow transition:          completed by controller
task.py start:                completed
product implementation:      in_progress
product tests:                in_progress
closed-profile parity review: approved; no manifest delta
migrations:                   not_executed (none permitted)
stage/commit/archive/journal: not_executed
push/PR/CI/merge:             not_executed
release/deploy/post-merge:    not_executed
```

The parent task remains `planning`; its Child 11 entry is a program tracker
registration rather than delivery evidence. The Trellis planning-artifact
manifest remains exactly the eight paths below; product paths are governed
separately by the exact future implementation manifest in Section 2:

```text
.trellis/tasks/07-12-backup-data-explorer-design/task.json
.trellis/tasks/07-20-backup-assets-worker-capabilities/task.json
.trellis/tasks/07-20-backup-assets-worker-capabilities/prd.md
.trellis/tasks/07-20-backup-assets-worker-capabilities/design.md
.trellis/tasks/07-20-backup-assets-worker-capabilities/implement.md
.trellis/tasks/07-20-backup-assets-worker-capabilities/implement.jsonl
.trellis/tasks/07-20-backup-assets-worker-capabilities/check.jsonl
.trellis/tasks/07-20-backup-assets-worker-capabilities/research/current-main-evidence.md
```

The last path was created during planning. Phase 2 product changes must remain
inside Section 2 and do not alter this eight-path planning-artifact count.

## 2. Exact future implementation manifest

This approved future manifest contains exactly 165 unique paths. Any future
tracked path outside it requires a written focused amendment and review of that
scope change. Directory-wide staging is forbidden; the eventual commit must
stage exact paths from this list.

The controller-approved 2026-07-20 focused amendment adds only
`backend/internal/backupasset/processing/reconciler.go` and
`backend/internal/backupasset/processing/reconciler_test.go`. This is the
**atomic Derived/Search reconciliation correction**: missing or unreadable
Derived blob repair must revoke its Search projection through the caller's
managed processing-job fence and transaction before Derived state/reference/
blob updates. It corrects the transaction boundary and is not a product scope
expansion.

The controller-approved follow-up amendment adds only
`backend/internal/backupasset/repository/testutil_test.go`. This is the
**repository foundation settings fixture synchronization**: the repository
package's explicit atomic-snapshot fixture must include the 12 frozen
processing/backfill/updater defaults added by Child 11. It does not change
`repositorySettings.BackupAssetSettingsSnapshot`, production Foundation
validation, or product scope, and it raises the exact manifest from 163 to 164
unique paths while preserving the earlier 161-to-163 amendment history.

The controller-approved **closed-profile advertisement/preflight/executable
parity correction** adds no path and keeps this manifest at 164/164 unique. It
retains archive gzip/xz/zstd execution, all advertised image MIME paths and the
optional secret profile, while requiring executable parity and bounded Office/
ODF preflight. It preserves both earlier manifest-amendment records and does not
authorize `go.mod`/`go.sum`, migration/model, Provider, release or Child 12+
changes.

The controller-approved **bootstrap null parser correction** adds only
`backend/internal/api/handlers/backup_content_handler.go` and raises the exact
manifest from 164 to 165 unique paths. The shared token walker must accept a
legal JSON null used by the closed first-activation request while retaining
duplicate-key, unknown-field, depth and trailing-token rejection. The focused
handler regression was observed RED at HTTP 400 after the path was reverted and
GREEN after the minimal correction. This is not an API, dependency, migration
or product-scope expansion.

### 2.1 Backend capability and runner paths

Create:

```text
backend/internal/backupasset/processing/capabilityspec/contracts.go
backend/internal/backupasset/processing/capabilityspec/contracts_test.go
backend/internal/backupasset/processing/capabilityspec/profiles.go
backend/internal/backupasset/processing/capabilityspec/profiles_test.go
backend/internal/backupasset/processing/capabilities/contracts.go
backend/internal/backupasset/processing/capabilities/profiles.go
backend/internal/backupasset/processing/capabilities/runner.go
backend/internal/backupasset/processing/capabilities/runner_test.go
backend/internal/backupasset/processing/capabilities/sandbox.go
backend/internal/backupasset/processing/capabilities/sandbox_linux.go
backend/internal/backupasset/processing/capabilities/sandbox_linux_test.go
backend/internal/backupasset/processing/capabilities/sandbox_other.go
backend/internal/backupasset/processing/capabilities/image.go
backend/internal/backupasset/processing/capabilities/image_test.go
backend/internal/backupasset/processing/capabilities/text.go
backend/internal/backupasset/processing/capabilities/text_test.go
backend/internal/backupasset/processing/capabilities/document.go
backend/internal/backupasset/processing/capabilities/document_test.go
backend/internal/backupasset/processing/capabilities/malware.go
backend/internal/backupasset/processing/capabilities/malware_test.go
backend/internal/backupasset/processing/capabilities/media.go
backend/internal/backupasset/processing/capabilities/media_test.go
backend/internal/backupasset/processing/capabilities/archive.go
backend/internal/backupasset/processing/capabilities/archive_test.go
backend/internal/backupasset/processing/capabilities/secret.go
backend/internal/backupasset/processing/capabilities/secret_test.go
backend/internal/backupasset/processing/capabilities/testdata/malformed-image-truncated.png
backend/internal/backupasset/processing/capabilities/testdata/malformed-document-truncated.pdf
backend/internal/backupasset/processing/capabilities/testdata/active-content.svg
backend/internal/backupasset/processing/capabilities/testdata/active-content.html
backend/internal/backupasset/processing/capabilities/testdata/office-macro.docm
backend/internal/backupasset/processing/capabilities/testdata/office-external-link.docx
backend/internal/backupasset/processing/capabilities/testdata/malformed-media.mp4
backend/internal/backupasset/processing/capabilities/testdata/archive-traversal.zip
backend/internal/backupasset/processing/capabilities/testdata/archive-symlink.tar
backend/internal/backupasset/processing/capabilities/testdata/archive-device.tar
backend/internal/backupasset/processing/capabilities/testdata/archive-bomb.zip
backend/internal/backupasset/processing/capabilities/testdata/archive-encrypted.zip
backend/internal/backupasset/processing/capabilities/testdata/malware-positive.txt
```

The fixtures contain no executable malware. `malware-positive.txt` contains an
obviously fake marker and the test generates a matching test-only ClamAV
signature database in tmpfs; it proves a positive finding is a successful scan
result without checking an EICAR payload into the repository.

Modify:

```text
backend/internal/backupasset/processing/protocol.go
backend/internal/backupasset/processing/protocol_test.go
backend/internal/backupasset/processing/worker_client.go
backend/internal/backupasset/processing/worker_client_test.go
backend/internal/backupasset/processing/contracts.go
backend/internal/backupasset/processing/contracts_test.go
backend/internal/backupasset/processing/derived_manifest.go
backend/internal/backupasset/processing/derived_manifest_test.go
backend/internal/backupasset/processing/derived_lifecycle.go
backend/internal/backupasset/processing/derived_lifecycle_test.go
backend/internal/backupasset/processing/reconciler.go
backend/internal/backupasset/processing/reconciler_test.go
backend/internal/backupasset/processing/coordinator.go
backend/internal/backupasset/processing/coordinator_test.go
backend/internal/backupasset/processing/scheduler.go
backend/internal/backupasset/processing/scheduler_test.go
backend/internal/backupasset/processing/behavior_integration_test.go
backend/internal/backupasset/processing/metrics.go
backend/internal/backupasset/processing/metrics_test.go
backend/internal/backupasset/service.go
backend/internal/backupasset/service_test.go
backend/internal/backupasset/repository/testutil_test.go
backend/internal/backupasset/runtime/processing_runtime.go
backend/internal/backupasset/runtime/processing_runtime_test.go
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go
backend/internal/backupasset/search/ingest.go
backend/internal/backupasset/search/ingest_test.go
backend/internal/backupasset/search/service.go
backend/internal/backupasset/search/service_test.go
backend/internal/backupasset/search/behavior_integration_test.go
backend/internal/backupasset/content/broker.go
backend/internal/backupasset/content/broker_test.go
backend/internal/backupasset/content/ticket.go
backend/internal/backupasset/content/ticket_test.go
backend/internal/backupasset/content/renderer.go
backend/internal/backupasset/content/renderer_test.go
```

Create the focused orchestration/adapter seams:

```text
backend/internal/backupasset/processing/capability_service.go
backend/internal/backupasset/processing/capability_service_test.go
backend/internal/backupasset/processing/backfill.go
backend/internal/backupasset/processing/backfill_test.go
backend/internal/backupasset/processing/invalidation.go
backend/internal/backupasset/processing/invalidation_test.go
backend/internal/backupasset/processing/coverage.go
backend/internal/backupasset/processing/coverage_test.go
backend/internal/backupasset/processing/updater/service.go
backend/internal/backupasset/processing/updater/service_test.go
backend/internal/backupasset/processing/updater/protocol.go
backend/internal/backupasset/processing/updater/protocol_test.go
backend/internal/backupasset/processing/updater/client.go
backend/internal/backupasset/processing/updater/client_test.go
backend/internal/backupasset/processing/updater/transport.go
backend/internal/backupasset/processing/updater/transport_linux.go
backend/internal/backupasset/processing/updater/transport_linux_test.go
backend/internal/backupasset/processing/updater/transport_other.go
backend/internal/backupasset/processing/updater/manifest.go
backend/internal/backupasset/processing/updater/manifest_test.go
backend/internal/backupasset/processing/updater/inbox.go
backend/internal/backupasset/processing/updater/inbox_test.go
backend/internal/backupasset/processing/updater/store.go
backend/internal/backupasset/processing/updater/store_test.go
backend/internal/backupasset/processing/updater/activation.go
backend/internal/backupasset/processing/updater/activation_test.go
backend/internal/backupasset/content/derived_resolver.go
backend/internal/backupasset/content/derived_resolver_test.go
```

### 2.2 Commands, API, settings and generated contract paths

Create/modify exactly:

```text
backend/cmd/asset-worker/main.go
backend/cmd/asset-worker/main_test.go
backend/cmd/asset-tool-sandbox/main.go
backend/cmd/asset-tool-sandbox/main_test.go
backend/cmd/asset-worker-updater/main.go
backend/cmd/asset-worker-updater/main_test.go
backend/cmd/server/main.go
backend/cmd/server/main_test.go
backend/internal/api/worker_updater_router.go
backend/internal/api/worker_updater_router_test.go
backend/internal/api/handlers/backup_processing_handler.go
backend/internal/api/handlers/backup_processing_handler_test.go
backend/internal/api/handlers/backup_content_handler.go
backend/internal/api/handlers/backup_worker_handler.go
backend/internal/api/handlers/backup_worker_handler_test.go
backend/internal/api/handlers/config_handler.go
backend/internal/api/handlers/config_handler_test.go
backend/internal/api/router.go
backend/internal/api/router_test.go
backend/internal/api/backup_asset_rbac_test.go
backend/internal/api/docs/docs.go
backend/internal/settings/service.go
backend/internal/settings/service_test.go
```

The API handler remains thin and uses response helpers. `worker_updater_router`
is a private authenticated socket adapter, not a public HTTP route. Public
preview-job and Admin routes are listed in `design.md` and are strictly
feature-gated, ownership-checked, rate/body limited and sanitized.

### 2.3 Runtime, image, Compose and CI paths

Create/modify exactly:

```text
deploy/worker/Dockerfile
deploy/worker/entrypoint.sh
deploy/worker/seccomp.json
docker-compose.yml
.github/workflows/ci.yml
scripts/check-compose-config.sh
scripts/check-compose-config.test.sh
scripts/test-core-compose.sh
scripts/test-core-compose.test.sh
scripts/test-asset-worker.sh
scripts/test-asset-worker.test.sh
```

No `deploy/allinone/*`, `publish-images.yml`, Docker Hub metadata, public
port, release workflow, or stable Worker image tag is in this child manifest.

### 2.4 Frontend paths

Create:

```text
web/src/lib/api/backup-asset-processing-api.ts
web/src/lib/api/backup-asset-processing-api.test.ts
web/src/features/backup-assets/backup-assets-processing-state.ts
web/src/features/backup-assets/backup-assets-processing-state.test.ts
web/src/features/backup-assets/use-backup-asset-processing.ts
web/src/features/backup-assets/use-backup-asset-processing.test.tsx
web/src/features/backup-assets/backup-asset-processing-panel.tsx
web/src/features/backup-assets/backup-asset-processing-panel.test.tsx
web/src/features/backup-assets/processing-coverage-panel.tsx
web/src/features/backup-assets/processing-coverage-panel.test.tsx
```

Modify:

```text
web/src/features/backup-assets/asset-preview.tsx
web/src/features/backup-assets/asset-preview.test.tsx
web/src/features/backup-assets/asset-inspector.tsx
web/src/features/backup-assets/asset-inspector.test.tsx
web/src/features/backup-assets/backup-assets-workspace.tsx
web/src/features/backup-assets/backup-assets-workspace.test.tsx
web/src/types/domain.ts
web/src/i18n/locales/zh.ts
web/src/i18n/locales/en.ts
web/src/pages/__tests__/backups-page.a11y.test.tsx
web/src/pages/backups-page.data.tsx
web/src/pages/backups-page.test.tsx
```

The processing controller dynamically imports its API factory. The data page
keeps ownership of auth context and passes token/role/step-up as narrow props;
the feature does not create another auth source. Eager `client.ts` and
`backup-assets-api.ts` are not changed or expanded. No tool binary, model,
bundle or Worker runtime asset is included in `web`.

### 2.5 Documentation-truth paths

Modify exactly:

```text
backend/README_backend.md
docs/deployment.md
docs/env-vars.md
docs/admin/backup-recovery.md
docs/admin/security.md
```

The four `docs/` paths restore the documentation obligations assigned by parent
implement Section 12. `backend/README_backend.md` satisfies the current-main
router freshness rule because this manifest changes `backend/internal/api/router.go`.
These documents describe only default-off capabilities, optional local Worker
build/Compose profile operation, settings and secret boundaries, security,
rollback, and no-Worker degradation. They must not claim GA, a stable public
Worker image, Docker Hub publication, or an already released capability.

## 3. Ordered TDD and implementation steps

### Step 0: Authorized workflow-transition preflight

- [x] Phase 2 and the technical plan are approved and the controller released
  the stay-in-planning workflow state. Reload Trellis before-development,
  relevant backend/frontend/guides specs, quality-check and verification
  instructions before any product edit.
- [ ] Run git fetch origin --prune, verify branch and all four revisions equal
  be6eebbe50dfd78e071c6d73e9c81493487fb4d5, verify parent/Child statuses, and
  verify a clean worktree except the reviewed planning manifest.
- [ ] Prove both migration engines contain 000067 and that 000068 through 000071
  are absent. Stop on any occupied/reserved-slot mismatch.
- [x] Only after those checks and controller workflow release, run exactly
  `python3 ./.trellis/scripts/task.py start .trellis/tasks/07-20-backup-assets-worker-capabilities`;
  record the resulting `in_progress` state before a product edit.

### Step 1: Contracts and redaction tests first

- [ ] Add table/property tests for all capability/profile identities, canonical
  parameters, MIME/input/output/page/pixel/duration/archive ceilings, duplicate
  fields, unknown fields and malformed UTF-8.
- [ ] Add tests proving all public/internal DTOs reject paths, credentials,
  Worker/grant/attempt/fence IDs, raw diagnostics and executable settings.
- [ ] Add tests for stable processing/updater error mapping and the fact that
  positive malware finding is succeeded.
- [ ] Add API ownership/RBAC/feature-disabled/rate/body and typed-audit tests
  before handler behavior is implemented.

### Step 2: Shared runner and malicious fixtures

- [ ] Implement typed no-shell runner, process-group cancellation, bounded
  stdout/stderr, timeout, cgroup/tmpfs checks and startup orphan cleanup.
- [ ] Implement and test the fixed asset-tool-sandbox helper: close inherited
  descriptors, apply Landlock/rlimit/tool seccomp, hide/deny the Core UDS and
  fail capability advertisement when the kernel contract is unavailable.
- [ ] Implement image/text/OCR/document/media/archive adapters against only
  closed executable/profile IDs. Make all external resources and network
  protocols unavailable by construction.
- [ ] Prove advertisement/executable parity: all image MIME rows reach their
  closed libvips/Tesseract path; gzip/xz/zstd TAR is single-stream and bounded;
  optional physical secret support remains Core-policy default-off; active
  OOXML/ODF fails before LibreOffice.
- [ ] Run the fixture matrix in Section 4 before connecting the coordinator.
- [ ] Prove source bytes are byte-for-byte unchanged after every capability,
  including cancellation and failed output validation.

### Step 3: Updater and pipeline identity

- [ ] Test canonical manifest signing, Ed25519 trust rotation, SHA-256 per-file
  and exact canonical uncompressed-tar payload verification, path/no-follow
  policy, envelope self-reference avoidance and source/version checks.
- [ ] Test offline import and exact allowlisted HTTPS egress, redirect rejection,
  response/body/time ceilings, required external allowlist-proxy enforcement
  and credential redaction. Offline tests must use the fixed read-only inbox,
  no-follow scan and opaque candidate activation; prove browser/Core APIs reject
  bundle bytes, multipart, URL and server paths. Online mode must fail startup
  without that network contract.
- [ ] Test the separate updater UDS protocol/client and Linux peer-credential
  listener: fixed UID/socket ownership and mode succeed; wrong UID, unsafe mode,
  Worker-socket identity, missing peer identity and public-server requests fail
  before receipt decode. Non-Linux transport stays fail closed.
- [ ] Implement content-addressed store, fsync/atomic pointer, activation
  journal, crash-before/after-rename recovery and old-bundle rollback.
- [ ] Feed active bundle fingerprints into pipeline/work identity and reject
  mismatched Worker advertisements.
- [ ] Persist content/OCR pipeline revisions only through the reserved internal
  settings methods; test Settings Registry/GetAll/API omission, unconditional
  config-export filtering and config-import rejection for both exact keys.

### Step 4: Invalidation, backfill and atomic publication

- [ ] Add tests for affected-only stale marking, superseded old attempts,
  strong/weak fingerprint dedupe, latest/interactive/recent/history ordering,
  pause/quota and interactive reserve.
- [ ] Add the transaction-aware Child 7 Search port while preserving existing
  wrappers.
- [ ] Refactor Derived manifest publication and revocation to pass one outer DB
  transaction/fence through Derived and Search. Inject failures at every write,
  old fence, duplicate commit, Search failure, Core crash and bundle race.
- [ ] Run real SQLite and PostgreSQL behavior parity; no pure SQL/text test is
  sufficient for the cross-plane contract.

### Step 5: Runtime and API

- [ ] Register closed production capabilities only when the verified active
  bundle and sandbox prerequisites are present; otherwise advertise none and
  return not_deployed/materialization_disabled without noisy jobs.
- [ ] Compose capability service, updater client, backfill policy and Derived
  resolver through backupasset/runtime.Runtime only.
- [ ] Implement exact AssetRef preview-job/poll/cancel/processing routes, Admin
  aggregate/coverage/updater/policy routes, JSON-only offline candidate scan/
  list/activation and private updater receipt routes. API tests bind queued JSON
  `job_id` and `Location` to the same processing-interest ID, bound
  `poll_after_seconds`, allow one terminal response and reject cross-owner/
  cross-AssetRef/replayed handles.
- [ ] Start the dedicated updater listener from the existing server listener
  composition root before public serving, with independent shutdown/drain and
  no fallback onto either public HTTP or the parser Worker socket.
- [ ] Regenerate Swagger and test route middleware order, response helpers,
  sanitization, ownership, archive audit and malware/secret server gates.

### Step 6: Frontend lazy processing state

- [ ] Add raw DTO mappers and domain types with exhaustive closed states and
  bounded numeric/time/string validation. Mapper tests cover exact equality
  between the public processing-interest handle and JSON `job_id`, bounded
  `poll_after_seconds`, and rejection of a whole malformed product rather than
  retaining a partially trusted response.
- [ ] Add request-revision/AbortController state hook, coalesced job polling,
  cancellation and fallback selection. Successful queued responses schedule
  from mapped `pollAfterSeconds`; `429`/`503` errors use
  `ApiError.retryAfter`. Test unmount/asset-switch races.
- [ ] Lazy-load the panel/controller only on processing interaction/admin view;
  `asset-preview.tsx` dynamically imports `backup-asset-processing-panel.tsx`,
  while the Admin surface independently imports `processing-coverage-panel.tsx`.
  Integrate the existing preview/inspector without replacing native renderers.
- [ ] Keep `BackupsDataWorkspace` as the auth boundary: pass token, role and the
  existing step-up callback into the workspace/panels through bounded props;
  never read auth storage inside the lazy feature or treat the role prop as the
  server authorization decision.
- [ ] Keep offline administration JSON-only: render verified candidate metadata
  and scan/activate controls, never a file input, Blob/FormData transport, URL
  import or server-path field.
- [ ] Add zh/en strings, keyboard/focus/live-region/reduced-motion and 375,
  768, 1440 viewport tests.

### Step 7: Runtime sandbox and CI

- [ ] Build the Worker image from a pinned multi-arch base with fixed non-root
  UID, read-only root, drop-all/no-new-privileges/seccomp, no network/DNS,
  read-only bundle mount and job tmpfs/resource limits.
- [ ] Add optional Compose profile and isolated core-only/profile scripts. Test
  that core service still uses image selector
  linnea7171/xirang:${IMAGE_TAG:-latest} and port 10761.
- [ ] Add CI matrix build/scan/smoke for amd64/arm64. Add an assertion that no
  Worker login/push/tag/publish/release step exists.

### Step 8: Documentation truth

- [ ] Update `backend/README_backend.md` with the feature-gated public/Admin
  route inventory and sanitized contract; update `docs/deployment.md` and
  `docs/env-vars.md` with default-off settings, optional local build/Compose
  profile, restart/secret boundaries, rollback and core-only behavior.
- [ ] Update `docs/admin/backup-recovery.md` with honest processing/preview
  states and no-Worker fallback, and `docs/admin/security.md` with sandbox,
  updater trust, malware/secret gates and sanitized audit behavior.
- [ ] State explicitly that Child 11 is not GA, does not provide a stable public
  Worker image, does not publish to Docker Hub, and does not change the official
  all-in-one image/port. Do not add a root `README.md` or release/deploy contract.
- [ ] Regenerate Swagger, run `scripts/check-doc-freshness.sh` against the full
  implementation diff, and run prohibited positive GA/public-image/publish-claim
  scans over all five documentation paths.

### Step 9: Full gate and handoff

- [ ] Run the validation matrix in Section 5 and retain fresh command output in
  the existing `research/current-main-evidence.md` ledger. Do not create a new
  evidence path outside the approved eight planning artifacts.
- [ ] Run Trellis quality check, review exact manifest, run security/scope scans
  and inspect staged names before commit.
- [ ] Stop for user/controller review if any limit, public route, migration,
  image, feature default or manifest path changes.
- [ ] Commit/push/PR/CI/merge/post-merge are future pending actions only; do not
  infer authorization from passing tests.

## 4. Malicious fixture and security matrix

| Fixture | Expected capability result | Required assertion |
|---|---|---|
| malformed-image-truncated.png | unsupported_format or invalid_output | no thumbnail, no source mutation, bounded diagnostic |
| active-content.svg | unsupported_format | never inline/executed; escaped/download fallback |
| active-content.html | unsupported_format | never sent to image/document tool or main-origin frame |
| malformed-document-truncated.pdf | invalid_output | no page artifact, no external process after cancellation |
| office-macro.docm | unsupported_format | macro-bearing type never enters LibreOffice or executes |
| office-external-link.docx | unsupported_format/invalid_output | preflight creates no LibreOffice plan and never fetches or leaks target |
| malformed-media.mp4 | unsupported_format/invalid_output | no unbounded probe/transcode or network protocol |
| archive-traversal.zip | sandbox_violation/unsupported_format | no traversal/absolute member and no host write |
| archive-symlink.tar | sandbox_violation/unsupported_format | symlink/hardlink rejected |
| archive-device.tar | sandbox_violation/unsupported_format | device/FIFO/socket rejected |
| archive-bomb.zip | input_too_large | expansion/ratio ceiling aborts incrementally |
| archive-encrypted.zip | encrypted_archive | no password prompt or retry loop |
| malware-positive.txt + generated test signature | succeeded, finding | finding gates only where server policy says; never becomes no_finding |

Additional generated cases cover duplicate archive names after normalization,
100,001 entries, depth 17, a single 257 MiB member, invalid UTF-8 text, a
10,000,001-rune line, PDF JavaScript/launch action, media duration overflow,
output digest mismatch, stale fence and a tool that forks a child after cancel.
Generated cases are bounded and deleted by the test harness.

Generated compressed-TAR cases also cover declared MIME/magic mismatch and,
for gzip/xz/zstd independently, valid inspect/extract, truncation, trailing
bytes and a valid empty second stream. Ratio and expanded-byte bombs abort in
the same streamed TAR consumer, and cancellation joins the tool process group.

## 5. Validation matrix and commands

All commands below are required Phase 2/3 gates. Focused rows may already have
execution evidence, but no row is complete until its final fresh run is
captured in `research/current-main-evidence.md`; skipped/not-executed results
never count as pass.

| Area | Command/evidence | Pass condition |
|---|---|---|
| Trellis package | python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-20-backup-assets-worker-capabilities | task metadata, docs and JSONL valid |
| Go formatting/lint | gofmt on exact Go paths; cd backend && go vet ./...; make lint-backend | no format/lint/error |
| Capability unit | cd backend && go test ./internal/backupasset/processing/capabilities -count=1 | profiles, fixtures, runner cancellation/redaction pass |
| Processing unit | cd backend && go test ./internal/backupasset/processing -count=1 | state, identity, updater, invalidation, atomic manifest pass |
| Content/Search unit | cd backend && go test ./internal/backupasset/content ./internal/backupasset/search -count=1 | resolver and transaction-aware port pass |
| Runtime/API unit | cd backend && go test ./internal/backupasset/runtime ./internal/api/... -count=1 | composition, routes, gates and sanitization pass |
| Race | cd backend && go test -race ./internal/backupasset/processing/... ./internal/backupasset/content/... ./internal/backupasset/search/... | no race/leak |
| PostgreSQL | cd backend && REQUIRE_POSTGRES_PROCESSING_TEST=1 REQUIRE_POSTGRES_SEARCH_TEST=1 go test ./internal/backupasset/processing ./internal/backupasset/search -run 'Postgres|Atomic|Projection' -count=1 | real DB behavior equals SQLite |
| Fault injection | focused Go tests with failpoints at each Derived/Search write | one transaction, no ghost postings/references |
| API redaction | cd backend && go test ./internal/api/handlers -run 'Backup.*Processing|Worker.*Updater|Preview.*Job' -count=1 | no forbidden fields/raw errors |
| Frontend gate | cd web && npm run check | typecheck/lint/tests/build pass |
| Frontend bundle | cd web && node scripts/check-bundle-budget.mjs | main JS <=500 KiB, CSS <=105 KiB; processing is lazy |
| Frontend focused | cd web && npm run test -- backup-asset-processing asset-preview asset-inspector backup-assets-workspace | mapper/race/a11y/fallback pass |
| Compose config | ./scripts/check-compose-config.sh and its self-test | core-only/profile config valid; no broad cleanup |
| Core-only smoke | ./scripts/test-core-compose.sh | no Worker still serves health/Catalog/native preview/download/recovery |
| Worker sandbox | ./scripts/test-asset-worker.sh | non-root/read-only/no-network/tmpfs/resource/seccomp contract |
| Image matrix | CI asset-worker matrix for linux/amd64,linux/arm64 | build/scan only, no login/push/publish |
| Migration fence | find backend/internal/database/migrations/{sqlite,postgres} -maxdepth 1 for 000068-000071 | no new migration files |
| Scope scan | git diff --name-only against Section 2.1 and forbidden path regex | only approved implementation paths after Phase 2 |
| Sensitive scan | rg forbidden credential/secret/raw-output/path/fence fields on DTO/log/API fixtures with allowlist | no accidental disclosure |
| Offline import scan | rg FormData/Blob/file input/multipart/bundle-byte request/server-path import across processing API/UI/Core handlers | only fixed-inbox JSON candidate control exists |
| Publish scan | rg docker login/push/build-push/release/publish in CI and Worker scripts | no Worker publication action |
| Docs/generated | make swag-init; run `DOC_FRESHNESS_CHANGED_FILES` with the full implementation path set through `bash scripts/check-doc-freshness.sh`; inspect a second Swagger generation for zero change; scan the five Section 2.5 paths for prohibited positive GA/stable-public-Worker-image/Docker-Hub-publish claims | generated API matches routes; freshness emits no warning; docs remain default-off/local-only and sanitized |
| Full project | make check | existing backend/frontend/core gates remain green |

The planning-only gate for this turn is narrower and must be run now:

    python3 ./.trellis/scripts/task.py validate \
      .trellis/tasks/07-20-backup-assets-worker-capabilities
    git diff --check

## 6. Rollback points and mixed-version behavior

| Point | Rollback action | Data guarantee |
|---|---|---|
| Before Worker registration | leave registries empty/disable profile | Core-only behavior unchanged |
| After capability code, before activation | set global/local/remote/updater false | no jobs/derivatives created |
| Inbox candidate verified, pointer not swapped | discard content-addressed candidate; operator later removes inbox file | old active bundle untouched |
| Pointer swapped, DB transaction fails | updater restores old pointer from journal | no new active metadata or stale marks |
| New bundle active and derivatives stale | pause backfill, reactivate old compatible bundle or keep stale fallback | source RecoveryPoints/Search core remain intact |
| Atomic publication failure | rollback outer transaction and discard unreferenced ciphertext | no half-published Search/Derived state |
| Worker image/sandbox failure | drain/revoke grants, remove optional profile | native preview/download/recovery remain |
| Frontend/API mixed version | map 404/disabled to native-only state | no raw DTO or missing-asset claim |

Never delete Provider bytes, RecoveryPoint evidence, source Catalog, or core
backup data as a rollback action. Derived ciphertext may remain as an unreferenced
rebuild candidate until the existing reconciler safely removes it.

## 7. Scope gates and handoff ledger

- [ ] No 000068-000071 file or 000067 DDL/model change is present.
- [ ] backup_assets.enabled, local/remote Worker and updater defaults remain
      false; secret classification remains opt-in; Command Provider remains
      typed unsupported.
- [ ] No Provider/SSH/Restic/Rclone/Command bytes, locator or credential contract
      changes exist.
- [ ] No Child 12 export, Child 13 recovery, Child 14 lifecycle/retention or
      Child 15 GA/publication work is included.
- [ ] No official all-in-one image, public port, Docker Hub release or stable
      Worker image contract changes exist.
- [ ] No browser/Core bundle upload, multipart updater body, URL import or
      caller-selected server/inbox path exists; Admin transport is JSON candidate
      control only.
- [x] Exact file manifest, validation evidence, risk gates and rollback plan
      were technically reviewed and approved by the controller; the user
      authorized Phase 2.
- [x] The focused 161-to-163 amendment adds only `reconciler.go` and
      `reconciler_test.go` as the atomic Derived/Search reconciliation
      correction; it is not a product scope expansion.
- [x] The focused 163-to-164 amendment adds only repository
      `testutil_test.go` as the Foundation settings fixture synchronization;
      production snapshot validation is unchanged and this is not a product
      scope expansion.
- [x] The focused 164-to-165 amendment adds only
      `backup_content_handler.go` as the bootstrap null parser correction;
      duplicate-key and strict decode boundaries remain fail closed.
- [x] task.py start: completed; Child status is `in_progress` and the parent
      remains `planning`.
- [ ] Product implementation/tests: in_progress; migrations remain forbidden.
- [ ] Commit/archive/journal/stage: not_executed / pending Phase 3.
- [ ] Push/PR/CI/merge/post-merge/release/deploy: not_executed / pending future
      delivery workflow.

The planning package and focused amendments are approved, the exact future
manifest is 165 unique paths, the workflow transition is complete, and Phase 2
is in progress. No further product decision is required unless another path or
frozen boundary changes.
