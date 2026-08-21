# Child 16 Technical Design

## 1. Boundary

Child 16 does not add a new product plane. It closes the live split between
“setting requested” and “feature usable”, wires already-built secret and
version contracts into the UI, and keeps this task as the vehicle for later
re-review fixes.

Owners stay where they are: Foundation/settings, GA inventory, Admission,
Catalog, Search, Content, Recovery UI, Overlay. No second inventory, no
Worker publish, no CodeDefault flip.

## 2. Invariants

1. **Requested ≠ live.** `FoundationService.FeatureEnabled()` remains the
   requested setting (DB > env > CodeDefault). Product read/write planes
   consult a **live** predicate that is true only after `ga.EvaluateEnablement`
   would pass on a current readiness snapshot.
2. **Settings UI stays fail-closed.** Existing-install PUT/import without ack
   remains HTTP 409. That path already does not persist `true`.
3. **Secrets stay fail-closed without proof.** Wiring the UI must not weaken
   Operator/Viewer rules or skip step-up.
4. **Version history is catalog facts, not Provider walks.** Responses carry
   opaque IDs, timestamps, size, type. No locators, paths, proofs, or
   `SnapshotFileIndex`.
5. **Worker remains optional.** Copy tells the truth; Core does not grow
   OCR/Office/ZIP browse.
6. **This child stays open** until Alan archives it after re-review. New
   findings amend these artifacts, then a new wave. Process notes stay under
   this task’s `research/`.

## 3. Live feature predicate

### 3.1 Why not change `FeatureEnabled()`

Settings, GA inventory, and docs need the requested value. Collapsing it
into “usable” would hide `BACKUP_ASSETS_ENABLED=true` from Admin while the
gate is still blocked.

### 3.2 `FeatureLive`

Add a single helper on `Runtime` (name may be `FeatureLive` or
`FeatureUsable`) used as the injected `FeatureEnabled` callback for Catalog,
Search, Overlay, and Content:

```
requested, err := foundation.FeatureEnabled()
if err != nil || !requested {
    return false, err
}
snapshot, err := readiness.Current(...)  // same source StartupPass uses
if err != nil {
    return false, err
}
return ga.EvaluateEnablement(snapshot) == nil, nil
```

`StartupPass` already DryRuns inventory then `authorizeEnablement`. When
that fails with `ErrEnablementBlocked` / `ErrEnablementAckRequired`, it
`InitializeDisabled` and returns nil. After this change, Catalog/Search
callbacks must use `FeatureLive`, not raw `FeatureEnabled`.

Content `Startup` stays skipped on that path. Ticket issue must also use
`FeatureLive` so a later settings flip cannot be bypassed.

### 3.3 `AdmissionController.Initialize`

`initialMode` today treats `FeatureEnabled()==true` as `AdmissionManaged`
(`controller.go` ~152–166). Production `StartupPass` authorizes first, but
any new caller of `Initialize()` would skip the gate.

Change: `Initialize()` / `initialMode` must not select `AdmissionManaged`
from the requested setting alone. Options (pick one, keep tests local):

- Inject the same `FeatureLive` / readiness evaluator into the controller, or
- Make `Initialize()` only apply a caller-supplied mode, and keep
  `InitializeDisabled` + authorized `initializeTo(AdmissionManaged)` as the
  only production path.

Do not add a second settings key.

## 4. Secret reveal

Backend already accepts `asset.secret_reveal` on content tickets and search.
Frontend `prepareDownload` is the pattern: `ensureStepUpProof(action, { persist: false, reuseCached: false })`, then retry the request with the proof.

Preview:

1. Classify the selected asset / first ticket error as secret-gated
   (existing content/search capability codes; do not invent a second
   taxonomy).
2. If **role is Admin** and `ensureStepUpProof` exists, request
   `STEP_UP_ACTIONS.assetSecretReveal`. Presence of the callback alone
   is not enough (`backups-page.data.tsx` passes it for every role).
3. Re-issue the preview ticket with `stepUpProof`.
4. On cancel or non-Admin, keep the current blocked empty state.
   Operator/Viewer must not call secret step-up even if the helper is
   passed.

Search: when Admin has a current secret-reveal proof (same action), pass
`secretRevealProof` on **every** `apiClient.search` call that uses that
session ref: first page, `loadMore`, and saved-search refresh. Do not
put proofs in the URL.

Backend fail-closed for AC3: content ticket issue and search verification
must reject `asset.secret_reveal` unless the actor role is `admin`.
`POST /auth/step-up` has no action-role allowlist; do not treat a valid
TOTP proof as authorization for Operator reveal.

## 5. Versions and all-retained search

### 5.1 Search

Keep `groupAllRetainedHits` collapsing to the latest hit per
`lineageToken + pathGroupToken`. Add `retained_version_count` (int, ≥ 1)
on the surviving `SearchHit`. Count is derived while grouping; it is not a
raw path. Public JSON stays opaque IDs + count.

The frontend result row must keep `retainedVersionCount` through
`commitSearchProjection` / saved-search mapping and **render** it on
all-retained search hits (list and grid) when count > 1. The versions
inspector alone does not satisfy R3.

Do not return every historical hit in the all-retained list (unbounded).

### 5.2 Catalog history

Add a bounded Catalog query, e.g.
`GET /api/v1/backup-assets/recovery-points/:id/entries/:entryId/versions`
(Auth + existing asset read permission).

Implementation sketch:

1. Load the source entry in the active generation (`catalog_entries`).
2. Resolve its RecoveryPoint → lineage (existing Catalog/RecoveryPoint
   relation; same identity Search already hashed into `LineageToken`).
3. List other active-generation entries with the same `normalized_path`
   in that lineage, newest first, limit ≤ catalog page cap (100/200).
4. Map to `{ recovery_point_id, entry_id, captured_at, size, entry_type }`.
   Never emit `normalized_path` or locators.

If an existing Catalog list can express this without a new route, reuse it.
Do not walk Provider bytes and do not use `SnapshotFileIndex`.

### 5.3 UI

`AssetVersions` loads that list for the selected recovery point + entry.
Rows navigate with the existing opaque deep-link helper. Empty / single
version is an honest one-row list, not “expansion unavailable”.

## 6. Core-only UX

- **In-place confirm:** `AlertDialog` (existing shadcn primitive) when the
  Admin selects `in_place` or before `submitTarget` if mode is `in_place`.
  Cancel leaves `isolated`. No extra backend field.
- **Preview errors:** `PreviewBody` uses the same capability → i18n map
  already used in the context panel (`backup-assets-presenters` /
  `backup-assets-error`).
- **Worker gap:** reuse processing/capability empty states; add copy that
  names optional Worker / Core-only. No Worker image work.

## 7. Residuals

- Ack audit: log `document.Counts.Conflicts` (pattern already at
  `inventory.go` ~725). Still no conflict row payloads in the public GA
  JSON.
- Retention cursor: fix Swagger/comment to “unsigned policy ID”. Do not
  change the cursor bytes. Run `make swag-init` only if annotations change.
- Docs: `docs/env-vars.md` and `docs/admin/backup-recovery.md` state that
  env `true` without readiness leaves the **effective** feature closed.

## 8. Compatibility and rollback

- No schema migration expected for Wave 1. If Catalog history needs an
  index, add paired SQLite + PostgreSQL migrations.
- Rollback: revert the Wave PR. Requested setting behavior is unchanged;
  only the live predicate and UI wiring regress.
- Leftover snapshot HTTP stays on `tasks:read`.

## 9. Later re-review waves

1. Record findings in this child’s `research/` (not the parent).
2. Amend `prd.md` / this file / `implement.md` with new requirement IDs.
3. Implement on a dedicated branch from up-to-date `main`.
4. Do not create Child 17 unless Alan asks.

Archive of this child is a separate Alan instruction after re-review.
Parent archive is out of scope.
