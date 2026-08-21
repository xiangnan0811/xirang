# Production final-acceptance protocol

Fill this from a real walkthrough. Do not invent values. Parent archive stays No-Go while `production_walkthrough` is `not_executed`.

## Binding

| Field | Value |
|---|---|
| Child | `08-21-backup-assets-review-closeout` |
| Git SHA | `9f059b0b3283825b41462c76ea42259a2d9ab9dc` (published review target; not walkthrough evidence) |
| GitHub Release | [v0.50.2](https://github.com/xiangnan0811/xirang/releases/tag/v0.50.2) |
| Image | `docker.io/linnea7171/xirang:v0.50.2` |
| Image digest | _pending (publish run 32478196001 succeeded; digest not copied from logs)_ |
| DB engine | _pending (expect existing production SQLite or recorded engine)_ |
| Provider mode | Core-only / Portable rclone / Native AWS (Native AWS is unsupported until live suite) |
| Browser | _pending_ |
| Acceptor | Alan |
| Recorder | weibo |
| `production_walkthrough` | `not_executed` |

## Must-pass checks

- [ ] `backup_assets.enabled` CodeDefault and official deploy env are still false before the drill
- [ ] Dry-run inventory recorded; Admin ack recorded; process restart recorded if still required
- [ ] After enable: FeatureLive true; Catalog / Search / Content agree
- [ ] Viewer cannot call leftover snapshot reads (410) or restore
- [ ] Operator cannot call leftover snapshot reads (410) or restore
- [ ] Admin leftover snapshot reads are 410
- [ ] Secret reveal is Admin-only; Operator UI has no retry
- [ ] Preview renew does not re-prompt TOTP in the same session
- [ ] In-place recovery confirm works; rollback / disable returns FeatureLive false
- [ ] Worker remains unpublished; Core-only path works without Worker
- [ ] Failures / waivers listed below with owner

## Failures / waivers

| ID | Result | Waiver? | Owner |
|---|---|---|---|
| _none yet_ | | | |

## Rollback drill

- [ ] Disable `backup_assets.enabled`
- [ ] Confirm workspace closed and leftover reads still 410
- [ ] Confirm no Worker image was pulled from Docker Hub as official
