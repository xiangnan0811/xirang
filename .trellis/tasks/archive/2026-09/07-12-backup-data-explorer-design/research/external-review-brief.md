# External review brief — backup asset explorer

> Internal brief for a reviewer of **v0.50.0**. Not a public doc. Not parent
> final acceptance. Alan confirmed 2026-08-21 that external review has not
> started, so the parent stays `planning`.

## What this is

A gated, Core-only backup-asset explorer shipped in
[v0.50.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.50.0)
(`1750a238`). Official image: `linnea7171/xirang:0.50.0`, port **10761**.

Review the **released implementation + enablement gate**, not parent
`implement.md` §16. Child 15 superseded that section.

## What this is not

- Not “default-on GA”. `backup_assets.enabled` CodeDefault is `"false"`.
- Not a published Worker product. No `xirang-asset-worker` on Docker Hub or
  GitHub Release. `publish-images.yml` does not mention Worker.
- Not parent close-out. Fifteen children are archived; the parent is still
  `planning` until Alan accepts after external review.

## Operator context (this installation)

One Admin user. Production has run the **pre-explorer** backup loop for more
than three months (tasks, credibility, drills). After an upgrade to v0.50.0
the install classifies as **`existing`**: file-backup Tasks and/or
repositories already exist.

Effective setting order: **DB > env `BACKUP_ASSETS_ENABLED` > CodeDefault**.
CodeDefault stays false so this upgrade does **not** request enablement.
Even a requested `true` (env or settings PUT) is fail-closed until dual
engine migrations, key domains, export root, dry-run inventory, and Admin
ack of the current inventory digest all pass. Core still boots if the gate
blocks; admission stays disabled.

Do not flip CodeDefault to `"true"` for this operator. That knob is for
**future fresh installs** that have no backup Tasks yet. This production
box is the opposite case.

## Worker publish (why it is out of scope)

Core all-in-one already lists, previews (browser-native), downloads, exports,
and recovers without a Worker. Worker only adds derived processing
(thumbnails, OCR, document conversion, malware scan, media derivatives).

“Publish Worker” would mean a second official image and a Docker Hub /
Release contract. The repo can build Worker locally via Compose profile
`asset-worker` (`xirang-asset-worker:local`). That is optional and not GA.
Missing Worker is an info gap, not an enablement hard fail.

This single-user production can stay Core-only.

## Review against this contract

| Topic | Contract |
|---|---|
| Feature gate | `backup_assets.enabled`, CodeDefault `"false"` |
| Fresh vs existing | `fresh` only if no Restic/Rsync/Rclone Task, no `BackupRepository`, no managed-history latch/tombstone |
| Inventory | Dry-run only; never mutates Provider bytes |
| Command tasks | Typed unsupported |
| Legacy snapshot UI | Tasks no longer mounts SnapshotBrowser/Search/Restore as primary entry; legacy HTTP routes remain until a later documented removal |
| Export volume | Compose named volume `asset-worker-export-store` → Core `/var/lib/xirang-asset-runtime/export` |
| Search / public GA JSON | Counts, closed kinds, opaque IDs/digests; no locators, proofs, tickets, identity keys, or `SnapshotFileIndex` |
| Parent §16 leftovers | Not in scope: CodeDefault flip, Worker publish, million-entry CI, `internal/alerting` product, rewriting Worker Dockerfile/seccomp |

## Disclose these residuals

1. Child 13 accepted a historical same-selector RED provenance exception;
   it was not reconstructed.
2. Child 5 AWS live-suite is `not_executed`, not certified.
3. `AdmissionController.Initialize` alone still treats env `true` as
   managed; production `StartupPass` authorizes first.
4. Ack audit log hard-codes `conflicts=0` (counts stay off the public
   payload).
5. `ListRetentionPolicies` parses `cursor` and does not set `NextCursor`.
6. Content leftover proof is fail-open when `db` is nil or the delivery-grant
   table is missing.
7. `scripts/check-backup-asset-migration.sh` and
   `scripts/test-backup-asset-load.sh` exist at small scale and are **not**
   required CI jobs.
8. Host `/tmp` quota can fail local `make check`; CI is authoritative.

## Suggested review questions

1. Does requested enablement stay fail-closed for an existing install
   without ack?
2. Does inventory leave Provider bytes unchanged?
3. Do Viewer/Operator see enablement controls or conflict internals?
4. Do public docs still say default false and Worker unpublished?
5. Are leftover snapshot HTTP routes unused by the new Tasks entry?

## Evidence pointers

- Child 15 archive: `.trellis/tasks/archive/2026-08/08-20-backup-assets-ga-hardening/`
- Pre-review UX/RBAC record: `research/pre-review-ux-security-walkthrough.md`
- Feature PR: https://github.com/xiangnan0811/xirang/pull/440
- Release: https://github.com/xiangnan0811/xirang/releases/tag/v0.50.0
