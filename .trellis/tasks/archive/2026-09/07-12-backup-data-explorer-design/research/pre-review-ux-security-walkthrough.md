# Pre-review UX / security walkthrough

> Parent Step 10 analogue. Recorded 2026-08-21 on `main` `1750a238`
> (v0.50.0). This is **not** a click-through of Alan’s three-month
> production box. That instance still runs the pre-explorer build until he
> upgrades. This record is code + focused automated evidence so external
> review has a written trail.

## Scope

Covered: Admin / Operator / Viewer enablement visibility, disabled
workspace, leftover Tasks entry, public JSON privacy, a11y of the GA
panel.

Not covered here (needs Alan after upgrade, or a disposable fixture):

- Live inventory against his real Restic/Rsync/Rclone Tasks
- Offline repository, no-Worker, disabled cache, no Range
- Secret step-up and malware-finding UX
- Narrow-screen / keyboard-only on a running browser
- Restart during export/recovery on his host

## Role matrix (code + tests)

| Role | Overview GA panel | Enable / inventory / ack | Data/recovery when disabled |
|---|---|---|---|
| Admin | Mounted on `/app/backups` overview (`backups-page.overview.tsx`) | Dedicated routes: Auth + `backup_repositories:manage` + `RequireRole("admin")` | Sees `feature_disabled` and an Admin CTA into the panel |
| Operator | Panel returns `null` (`role !== "admin"`) | HTTP 403; service not called | Disabled empty state, no conflict payload |
| Viewer | Same as Operator | HTTP 403 | Health/status only; no filenames/paths |

Evidence:

- `web/src/features/backup-assets/ga-readiness-panel.tsx` early-returns for
  non-admin.
- `web/src/features/backup-assets/ga-readiness-panel.test.tsx` asserts
  Operator/Viewer render no enable controls and do not call the API.
- `web/src/features/backup-assets/ga-readiness-panel.a11y.test.tsx` axe +
  name/status.
- `web/src/features/backup-assets/backup-assets-workspace.test.tsx`
  `feature_disabled` Admin CTA vs non-admin.
- Backend: `backend/internal/api/handlers/backup_ga_handler.go` +
  `backup_ga_handler_test.go`.

## Disabled product (upgrade-safe)

After upgrade, CodeDefault `"false"` means this existing install does not
request enablement. Data/recovery stay on the existing
`feature_disabled` empty state. Overview still shows confidence/health
(the three-month product). GA panel is Admin-only and does not invent
assets.

Enablement path for this operator, when he chooses it:

1. Admin opens overview → readiness panel
2. Run dry-run inventory (no Provider writes)
3. Because class is `existing`, acknowledge the current digest
4. Only then settings/env `true` can pass the gate

## Leftover snapshot UX

Tasks no longer mounts `SnapshotBrowser` / `SnapshotSearch` /
`RestoreConfirmDialog` as the primary asset entry. Deep links go to
`/app/backups`. Legacy HTTP snapshot routes remain lineage-guarded until
a later documented removal. New search does not read `SnapshotFileIndex`.

Evidence: `web/src/pages/tasks-page.tsx`, `tasks-page.dialogs.tsx`,
`backup-assets-route-state.ts` and their tests.

## Privacy of public GA JSON

Inventory / readiness / ack responses are counts, closed kinds, opaque
32-hex IDs, and 64-hex digests. No locators, proofs, tickets, identity
keys, or `SnapshotFileIndex`. Ack audit still logs `conflicts=0` (known
residual; not a public leak).

## Evidence used for this record

This session did not re-run `web` vitest: the worktree has no usable
`web/node_modules`. Authoritative automated proof is already on `main`:

- Child 15 exact-squash main CI `32377898099` (required checks green,
  including Worker complete-profile smoke)
- Release v0.50.0 post-merge main CI `32430912238` (success)

Re-run in this session (backend only):

```text
cd backend && go test ./internal/api/handlers/ -count=1 -run 'TestBackupGa|TestGA|Ga'
cd backend && go test ./internal/backupasset/runtime/ -count=1 -run 'Ga|Enablement|Startup'
```

Both packages reported `ok` (2026-08-21). Frontend role/a11y files were not
re-executed here because `web/node_modules` is absent; they remain covered
by the two main CI runs above.

Role / disabled / leftover-entry claims above are pinned to the files
listed in each section, which shipped in those green runs.

## Alan follow-up after he upgrades to v0.50.0

On the production host, as Admin:

1. Confirm backups overview still shows the old confidence/health cards
   and that data/recovery say the feature is off.
2. Run inventory once; confirm Provider bytes and task configs are
   unchanged.
3. Do **not** ack or enable until he has read the conflict/gap counts.
4. Leave Worker unpublished; stay Core-only unless he later builds the
   local `asset-worker` profile on purpose.
