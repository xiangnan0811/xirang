# Child 16 Implementation Plan

> Planning only. Do not run `task.py start` until Alan approves the latest
> planning summary. Parent `implement.md` §16 stays superseded by Child 15
> and is not this file list.

**Goal:** Close the v0.50.0 review punch list (R1–R5), then keep this child
open for later re-review waves.

**Architecture:** Requested setting stays `FeatureEnabled()`. Product planes
use a live readiness predicate. Secret reveal and version history reuse
existing backend contracts. UI follows download step-up and opaque deep
links.

**Stack:** Go 1.26 backend (Gin/GORM), React 18 + Vitest frontend, existing
step-up + Catalog/Search/Content packages.

---

## Before `task.py start`

- [ ] Alan approved this planning summary in a later message.
- [ ] Branch is `feat/backup-assets-closeout` from up-to-date `main` (or a
      fresh Wave branch from `main` if this one is stale).
- [ ] Read `.trellis/spec/backend/index.md` and
      `.trellis/spec/frontend/index.md` via `trellis-before-dev`.
- [ ] Do not flip CodeDefault. Do not edit `publish-images.yml`.
- [ ] Do not write review notes into the parent task.

## Risky files

| Area | Files |
|---|---|
| Live gate | `backend/internal/backupasset/runtime/runtime.go`, `controller.go`, `ga_runtime_test.go` |
| Wiring | `runtime.go` Catalog/Search/Overlay/Content `FeatureEnabled` injection (~510, ~648, ~669, ~881) |
| Catalog | `backend/internal/backupasset/catalog/service.go`, catalog handler |
| Search | `backend/internal/backupasset/search/service.go` |
| Secret UI | `web/src/features/backup-assets/use-backup-assets-state.ts` |
| Versions UI | `web/src/features/backup-assets/asset-versions.tsx` |
| Recovery | `web/src/features/backup-assets/recovery-plan-wizard.tsx` |
| Ack audit | `backend/internal/backupasset/ga/inventory.go` |
| Docs | `docs/env-vars.md`, `docs/admin/backup-recovery.md` |

Rollback: revert the Wave PR. No expected Wave 1 migration; if an index is
added, pair SQLite + PostgreSQL and revert both.

---

## Wave 1A — Live enablement gate (R1, R5 Initialize)

**Files:** `runtime.go`, `controller.go`, tests under `runtime/` and
`handlers/` GA; docs listed above.

- [ ] Add `FeatureLive` (or equivalent) on `Runtime`. Test: requested true +
      existing/unacked snapshot → false; requested true + ready/ack → true;
      requested false → false.
- [ ] Inject that predicate into Catalog, Search, Overlay, and Content
      instead of `foundation.FeatureEnabled`.
- [ ] Extend `StartupPass` / GA runtime tests: env true without ack boots;
      catalog/search/content calls return feature-disabled; admission is not
      managed.
- [ ] Change `AdmissionController.Initialize` / `initialMode` so requested
      true alone cannot become `AdmissionManaged`. Keep
      `InitializeDisabled` for the blocked startup path.
- [ ] Keep settings PUT 409 tests green (`backup_ga_handler_test.go`).
- [ ] Update `docs/env-vars.md` and `docs/admin/backup-recovery.md`: env
      true without readiness leaves the **effective** feature closed.

**Validate:**

```text
cd backend && go test ./internal/backupasset/runtime/ ./internal/backupasset/ga/ ./internal/backupasset/catalog/ ./internal/backupasset/search/ ./internal/api/handlers/ -count=1
```

## Wave 1B — Secret reveal UI (R2)

**Files:** `use-backup-assets-state.ts`, search execute path, presenters,
i18n `zh.ts`/`en.ts`, existing step-up tests.

- [ ] Mirror `prepareDownload`: Admin preview of secret/unknown requests
      `STEP_UP_ACTIONS.assetSecretReveal` (`persist: false`,
      `reuseCached: false`) and retries the ticket with the proof.
- [ ] Pass `secretRevealProof` on search when that proof exists.
- [ ] Operator/Viewer never call secret step-up; blocked state unchanged.
- [ ] Tests: Admin success, cancel, operator denied, no proof still
      fail-closed. Do not put proofs in the URL.

**Validate:**

```text
cd web && npm run test -- src/features/backup-assets/use-backup-assets-state.test.tsx src/features/backup-assets/backup-assets-workspace.test.tsx
cd backend && go test ./internal/backupasset/content/ ./internal/api/handlers/ -count=1 -run 'Secret|StepUp|Preview'
```

## Wave 1C — Versions (R3)

**Files:** catalog service + handler + API client; `search/service.go`;
`asset-versions.tsx`; route helper; mappers.

- [ ] Search grouping: keep latest hit; set `retained_version_count`.
- [ ] Catalog bounded history by lineage + `normalized_path` (active
      generation only). Public DTO: opaque IDs, `captured_at`, size, type.
      No path/locator.
- [ ] `AssetVersions` lists rows and navigates with existing opaque deep
      links. Remove `expansionUnavailable` as the only state.
- [ ] Tests: two retained points → count 2 + list 2; single point → count 1;
      Viewer/Operator follow existing asset-read rules.

**Validate:**

```text
cd backend && go test ./internal/backupasset/catalog/ ./internal/backupasset/search/ -count=1
cd web && npm run test -- src/features/backup-assets/asset-versions.test.tsx src/features/backup-assets/backup-assets-route-state.test.ts
```

## Wave 1D — Core-only UX (R4)

**Files:** `recovery-plan-wizard.tsx`, `asset-preview.tsx`, processing empty
states, i18n.

- [ ] Confirm before in-place plan create (or before switching to
      `in_place`). Default remains isolated.
- [ ] Preview main pane uses capability translation keys.
- [ ] Worker-missing ZIP/Office/OCR copy says optional enhancement.

**Validate:**

```text
cd web && npm run test -- src/features/backup-assets/recovery-plan-wizard.test.tsx src/features/backup-assets/asset-preview.test.tsx
```

## Wave 1E — Residuals (R5 remainder)

**Files:** `ga/inventory.go`, Swagger annotations for retention list cursor.

- [ ] Ack audit `conflicts` = inventory count (same as ~725).
- [ ] Align Swagger/comment with unsigned policy ID. Run `make swag-init`
      only if annotations change.

**Validate:**

```text
cd backend && go test ./internal/backupasset/ga/ ./internal/api/handlers/ -count=1 -run 'Ack|Retention|Cursor'
```

## Wave 1F — Re-review, then ship (not child archive)

- [x] Independent re-review of Wave 1 (`research/re-review-1.md`).
      **Not clean.** Do not commit or open a PR.
- [x] Wave 2 (F1–F3) lands on this same branch. Re-review 2
      (`research/re-review-2.md`) is clean. Commit / PR are allowed
      after `make swag-init` (F6).
- [x] `make swag-init` for versions route + unsigned retention cursor;
      confirm no CodeDefault change and no `publish-images.yml` Worker
      job; then commit and open the PR.
- [ ] Monitor required CI. Merge only when green.
- [ ] Post-merge: watch Release Please (chore/feat may or may not cut a
      release). No Docker Hub Worker publish. No parent archive.

## Wave 2 — Re-review 1 must-fix (F1–F3)

**Files:** `use-backup-assets-state.ts`, `backups-page.data.tsx`,
`backup-assets-state.ts`, `asset-list.tsx`, `asset-grid.tsx`, i18n
`zh.ts`/`en.ts`, content broker/search secret-proof role check, tests.

- [x] F1 / AC3: `loadPreview` secret retry only when `role === "admin"`
      **and** `ensureStepUpProof` exists. Operator/Viewer stay blocked
      even if the helper is passed. Pass `role` from
      `backups-page.data.tsx`. Backend: reject non-admin
      `asset.secret_reveal` on ticket issue and search verify.
      Tests: Admin retry still works; Operator with helper does not
      call step-up and does not retry the ticket; Operator proof is
      rejected server-side.
- [x] F2: attach `secretRevealProofRef` on every `apiClient.search`
      (first page, `loadMore` search, saved-search `refreshResults`).
      Extract one helper. Test: after reveal, page-two / saved reload
      sends the same proof.
- [x] F3 / R3: keep `retainedVersionCount` on `BackupAssetResultRow`
      through `commitSearchProjection` and the saved-search mapper.
      Show it on list and grid when `source === "search"` and count > 1.
      Test: search projection with count 2 is on the row and visible.

`make swag-init` is the ship step after re-review 2 (F6), not a
substitute for F1–F3. Do not flip CodeDefault.

**Validate:**

```text
cd web && npm run test -- src/features/backup-assets/use-backup-assets-state.test.tsx src/features/backup-assets/asset-list.test.tsx src/features/backup-assets/asset-grid.test.tsx
cd backend && go test ./internal/backupasset/content/ ./internal/backupasset/search/ ./internal/api/handlers/ -count=1 -run 'Secret|StepUp|Preview|Search'
```

## After Wave 2 (R6)

- [ ] Leave this child `in_progress` for the next re-review.
      Do **not** `task.py archive` until Alan says so (AC8–AC9).
- [ ] F4–F5 from `research/re-review-1.md` stay should-fix (restart
      after settings enable; handler `Enabled` vs `FeatureLive`).
- [x] F6 is `make swag-init` in the ship sequence after re-review 2.
- [ ] F7 (`research/re-review-2.md`): preview renew does not reuse the
      session reveal proof; ticket expiry re-prompts Admin TOTP.
      Fail-closed. Later wave only if Alan wants it.
- [ ] New findings: `research/re-review-N.md` in **this** child, then
      amend PRD/design/this file, then the next wave on this branch.

## Later waves

Unknown until the next external review. Do not implement a finding that is
not in the amended PRD.
