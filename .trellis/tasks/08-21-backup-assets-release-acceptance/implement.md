# Implement — 备份资产发布验收

Planning-only until the user explicitly approves this plan and says to start. Do not `task.py start` in the same turn as planning review.

## Wave 0 — protocol template and parent breadcrumb

- Replace this child's `research/acceptance-protocol.md` with the v0.50.2 / Core-only template (`production_walkthrough: not_executed`).
- Keep Child 17 protocol untouched as history.
- Parent notes already point at Child 18; after start, do not claim walkthrough executed.

## Wave 1 — exclusion verification (repo, no product rewrite)

- Read `docs/admin/backup-recovery.md` and `docs/admin/backup-assets-load.md`.
- If they still say AWS Native is out of the support matrix, record `docs_exclusion: verified` in the protocol. Do not edit.
- If they claim Native is supported, open a **docs-only** fix on this branch. Do not remove Native UI.

## Wave 2 — Alan production drill (human)

Agent supplies the checklist. Alan executes on the Core-only host running `docker.io/linnea7171/xirang:v0.50.2`.

1. Record running image digest (`docker image inspect` / Hub) into the protocol.
2. Confirm CodeDefault / official `.env.deploy` were not flipped; this host may set an instance override only after inventory.
3. Dry-run inventory; Admin ack; restart if still required.
4. Optional: install PromQL from `docs/admin/backup-assets-slo.md`, or file a waiver.
5. Enable; confirm FeatureLive; Catalog / Search / Content agree.
6. Viewer / Operator / Admin leftover snapshot reads → 410; restore stays Admin + live + step-up.
7. Admin-only secret reveal; preview renew without second TOTP.
8. In-place recovery confirm; then disable; FeatureLive false; leftover reads still 410.
9. Confirm no official Worker image pull from Docker Hub.
10. Do not open Rclone Native binding. Do not require Restic / Rsync / Portable repos.

## Wave 3 — record, do not invent

- Fill protocol fields only from Alan's report.
- Set `production_walkthrough` to `executed` only when AC1–AC7 that were in-scope are checked or waived with owner.
- Leave Restic / Rsync / Portable as `declared_supported_not_executed`.
- Leave Native AWS as `excluded_this_ga`.
- Backup cycle: `observed` or `not_elapsed`.
- Update this child's `task.json` notes. Do not archive the parent.

## Wave 4 — parent archive request (separate instruction)

- Only after Wave 3 `executed`, **ask** Alan whether to archive the parent.
- Do not archive on Child 18 completion alone.

## Validation

- `python3 -c` JSON load on parent and child `task.json`.
- Diff must not include `backend/`, `web/` product files unless Wave 1 found a docs lie.
- Forbidden: `.env.deploy` CodeDefault flip, `publish-images.yml`, invented checkmarks.

## Rollback of the child itself

If the drill fails, keep `production_walkthrough: not_executed` (or `failed` with owner). Parent stays `planning`. Do not revert v0.50.2 on GitHub.

## P2 parking lot (not a gate)

- Rename `backup_asset_search_audit_fail` / unify 15m vs 10m windows / add build-backlog rules.
- Stamp `enablement_succeeded_at` only after FeatureLive.
- Fail unexpected `console.*` / unmatched MSW; occasional arm64 full Worker Compose.
