# Production final-acceptance protocol (Child 18)

Fill this from a real walkthrough on Alan's Core-only production host. Do not invent values. Parent archive stays No-Go while `production_walkthrough` is `not_executed`.

Child 17's protocol remains historical and `not_executed`. This file is the authority for parent-archive gating.

## Binding

| Field | Value |
|---|---|
| Child | `08-21-backup-assets-release-acceptance` |
| Git SHA | `9f059b0b3283825b41462c76ea42259a2d9ab9dc` |
| GitHub Release | [v0.50.2](https://github.com/xiangnan0811/xirang/releases/tag/v0.50.2) |
| Image | `docker.io/linnea7171/xirang:v0.50.2` |
| Image digest | _pending (copy from the running host / Hub; do not invent)_ |
| DB engine | _pending_ |
| Provider mode | `core-only` |
| Restic | `declared_supported_not_executed` |
| Rsync | `declared_supported_not_executed` |
| Rclone Portable | `declared_supported_not_executed` |
| Rclone Native AWS | `excluded_this_ga` |
| Browser | _pending_ |
| Acceptor | Alan |
| Recorder | weibo |
| `production_walkthrough` | `not_executed` |
| `slo_rules_installed` | _pending_ |
| `backup_cycle` | `not_elapsed` |
| `docs_exclusion` | `verified` — `docs/admin/backup-recovery.md:62`; `docs/admin/backup-assets-load.md:23` |

## Native AWS exclusion (this GA)

Rclone Native AWS is **not** part of the v0.50.2 enablement promise. Do not run Native binding during this drill. Do not treat mock S3 as a substitute. Public docs must continue to keep Native out of the support matrix.

## Must-pass checks

- [ ] Official CodeDefault and `.env.deploy` are still false in the repository / release image
- [ ] Dry-run inventory recorded; Admin ack recorded; process restart recorded if still required
- [ ] After enable: FeatureLive true; Catalog / Search / Content agree on this Core-only host
- [ ] Viewer leftover snapshot reads are 410; Viewer cannot restore
- [ ] Operator leftover snapshot reads are 410; Operator cannot restore
- [ ] Admin leftover snapshot reads are 410
- [ ] Secret reveal is Admin-only; Operator UI has no retry
- [ ] Preview renew does not re-prompt TOTP in the same session
- [ ] In-place recovery confirm works; disable returns FeatureLive false
- [ ] Worker remains unpublished; Core-only path works without Worker
- [ ] Native AWS binding was not opened
- [ ] Failures / waivers listed below with owner

## Failures / waivers

| ID | Result | Waiver? | Owner |
|---|---|---|---|
| _none yet_ | | | |

## Rollback drill

- [ ] Disable `backup_assets.enabled` on this instance (not CodeDefault)
- [ ] Confirm workspace closed and leftover reads still 410
- [ ] Confirm no Worker image was pulled from Docker Hub as official
