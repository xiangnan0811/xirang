# Phase 6 — Release And Production Acceptance

Date: 2026-08-24

## Pull Request And Release

- Implementation PR [#453](https://github.com/xiangnan0811/xirang/pull/453)
  merged at `2026-08-24T07:33:53Z` as
  `bb295c830285377af439e6021b51234e09df5127` after every required CI check
  completed successfully.
- Release PR [#454](https://github.com/xiangnan0811/xirang/pull/454)
  merged at `2026-08-24T08:16:36Z` as
  `214f5e18b47974d4e353227fa52782992cef70f7` after every required CI check
  completed successfully.
- GitHub Release
  [v0.50.4](https://github.com/xiangnan0811/xirang/releases/tag/v0.50.4)
  was published at `2026-08-24T08:16:49Z` and targets the exact release commit.
- Post-merge `CI`, `Release Please`, and `Publish Docker Images` workflows all
  completed successfully. A Docker Hub description sync was not expected because
  this release did not change the public README/description contract.

## Immutable Image Evidence

- Public image: `docker.io/linnea7171/xirang:v0.50.4`.
- Multi-architecture manifest digest:
  `sha256:9dcfddade668ee0a9b2d1940cc66004df002cd4a435be9d2c1e0f95f60a11ab9`.
- Production architecture: `linux/amd64`.
- Production image ID:
  `sha256:14f0d4d9d3ef9ce552a02cef263baa1e8acd4c13afcf8983eecb8d8111924b06`.
- OCI version/revision labels were `0.50.4` and the exact release commit.

## Pre-Upgrade Safety

- The operator verified the v0.50.3 container healthy with restart count zero,
  `/healthz` OK, SQLite integrity OK, schema `72|0`, zero active TaskRuns,
  `backup_assets.enabled` absent, no enablement-success stamp, current inventory
  acknowledgement, and zero repositories/active links/recovery points.
- The operator stopped v0.50.3 and created a logical SQLite backup named
  `xirang-pre-v0504.db`. `PRAGMA integrity_check` returned `ok`, schema remained
  `72|0`, active runs remained zero, and `sha256sum -c` returned `OK`.
- After upgrading, v0.50.4 reached healthy in about 31 seconds. The disabled
  baseline preserved all database and readiness facts and had zero critical
  logs.

## Single Enablement Acceptance

The operator armed a one-attempt evidence window and clicked **Enable Backup
Assets exactly once**.

- `PUT /api/v1/settings` returned HTTP `200` in `0.046s`, versus the original
  incident's `499` after about 692 seconds.
- `backup_assets.enabled=true` persisted.
- `enablement_succeeded_at=2026-08-24 10:04:46.972911654+00:00` persisted only
  after the transition completed.
- The container remained `running/healthy`, restart count zero; `/healthz`
  remained OK; SQLite integrity remained OK; schema remained `72|0`.
- No fatal, panic, error, deadline, compensation, fence, deadlock, Search-ready,
  or restoration failure log matched the acceptance window.

## Data Explorer And Search Acceptance

- `/app/backups/data?view=search&scope=all_retained` rendered the three-column
  data explorer and authoritative empty state without a feature-disabled or
  request-error surface.
- Repository listing returned HTTP `200` in `0.005s`.
- The UI produced two idempotent `POST /api/v1/asset-search` requests while
  entering the search view and explicitly submitting the no-match query. Both
  returned HTTP `200`, in `0.030s` and `0.022s`; neither is a retry of the
  enablement mutation.
- Runtime observation then reported
  `xirang_backup_asset_feature_requested=1` and
  `xirang_backup_asset_feature_live=1`.
- Critical log count remained zero and the container remained healthy with no
  restart.

## Outcome And Remaining Data Work

R7 and AC12–AC13 pass. The v0.50.4 deadlock repair is accepted in production,
and the Core data explorer/search entry is live. The current installation still
contains zero BackupRepository records, zero active TaskRepositoryLink records,
and zero RecoveryPoint records, so the correct current UI is an empty result.
Existing Rsync task directories and historical task runs are not automatically
promoted to trusted backup assets. Actual previewable files require a separately
authorized Repository/link/RecoveryPoint onboarding workflow; that data work is
outside this P0 repair.
