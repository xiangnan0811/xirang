# v0.50.4 real-data production acceptance

Status: `product_gap_child_triggered`

This is the current authority for the v0.50.4 real-data gate. The historical v0.50.2 No-Go remains unchanged in `acceptance-protocol.md`.

## Release and enablement baseline

| Evidence | Observed result |
|---|---|
| Release revision | `214f5e18b47974d4e353227fa52782992cef70f7` |
| Image | `docker.io/linnea7171/xirang:v0.50.4` |
| Container | running / healthy; restart count 0 |
| Health | `/healthz` OK |
| SQLite schema | `72|0` |
| Feature setting | `backup_assets.enabled=true` |
| Enablement timestamp | `2026-08-24 10:04:46.972911654+00:00` |
| Metrics | requested 1; live 1 |
| Existing read path | repositories GET 200; two asset-search POSTs 200 |
| Existing records | repositories 0; active task links 0; recovery points 0 |
| GA inventory | candidates 0; conflicts 1; unsupported 0; capability gaps 12 |
| Node-log collectors | 0; remain disabled until this gate passes |

## Code-truth routing decision

- `POST /api/v1/backup-repositories/connect` accepts a Task ID and optional opaque repository ID/labels only. It does not accept arbitrary paths or credentials.
- Repository Connect loads an active Task, derives an access binding, performs a read-only Provider probe, then commits the repository, active task link, encrypted binding and legacy Rsync mutable recovery point in one transaction.
- A legacy Rsync Task is eligible only when its target is a non-empty local absolute path. Remote Rsync targets cannot be made readable by inventing credentials.
- The zero-repository management UI has no initial Connect control; the authenticated Admin API is the supported first-connect path. Existing UI Connect usage is reconnect-only.
- Catalog reconciliation runs at startup and then on its configured interval (default 15 minutes); search reconciliation follows its configured interval (default 1 minute). Acceptance uses condition polling and does not restart production.

## Privacy boundary

Allowed evidence: HTTP status, request ID, role, counts, Task/node numeric IDs, safe publication status, opaque repository/link/recovery-point/entry IDs, entry type/size/MIME/timestamps, health and stable error codes.

Forbidden evidence: bearer token, password, TOTP, step-up proof, SSH/provider credentials, binding document, locator, Rsync source/target, command, raw error text, asset name/path/breadcrumb, or preview content.

## Local baseline verification

Observed at `2026-08-24T19:39:49+08:00` on branch `codex/backup-assets-v0504-data-acceptance` from `origin/main` revision `43c2771e`.

The first focused run failed before tests because the CGO linker's private `/tmp/go-link-*` directory hit the tmpfs user quota. GDB syscall evidence showed `fallocate(fd, 0, 0, 41484288) = -122 (EDQUOT)` for `/tmp/go-link-*/go.o`. Moving only `GOTMPDIR` was insufficient because the external linker uses `TMPDIR` independently. With both variables pointed at one dedicated `/var/tmp` directory, the same package set passed without clearing shared caches:

```text
ok  xirang/backend/internal/backupasset/repository
ok  xirang/backend/internal/backupasset/catalog
ok  xirang/backend/internal/backupasset/search
ok  xirang/backend/internal/backupasset/content
ok  xirang/backend/internal/api/handlers
```

This is a local verification-environment finding, not a product test failure and not production evidence.

## Preflight evidence

The first sanitized browser-console inventory was received on 2026-08-24. All five read endpoints returned HTTP 200, GA readiness was acknowledged with inventory/export-root/key-domain checks true, repository count remained 0, and 12 Rsync Tasks reported local absolute targets. The role output was `null` because the runbook read `data.role`; live code and the HTTP response contract place it at `data.user.role`. The runbook is corrected before the exact-Task preflight.

The same output confirmed that GA `no_repository` is not the Task publication reason: the safe Task projection is `legacy_mutable / legacy / legacy`. Code truth permits initial Connect for this local legacy shape. Task 3 was selected for the second read-only check because its sanitized `last_run_at` was the freshest successful enabled row in the supplied inventory.

The exact-Task preflight then returned `target_preflight_ok=true` for Task 3 / Node 3 with role `admin`, executor `rsync`, and no failed guard. By construction this proves every guarded read returned HTTP 200, the Task remained enabled and successful with a local absolute target and exact legacy publication projection, no active TaskRun existed, a recent successful TaskRun existed, the backup root contained at least one entry, and repository count remained 0. The block did not perform a write and did not print a locator, entry name, or credential.

| Check | Result |
|---|---|
| Health / API HTTP | five reads HTTP 200 |
| Admin role | `admin` via corrected `data.user.role` projection |
| GA readiness/counts | acknowledged; 0 candidates, 1 conflict, 0 unsupported, 12 capability gaps |
| Repository count before write | 0 |
| Exact Task ID | 3, Node 3; exact preflight passed |
| Local absolute Rsync target | true in exact preflight |
| No active TaskRun | confirmed, because the closed guard passed |
| Recent successful TaskRun | confirmed; sanitized inventory timestamp `2026-08-24T12:21:00.004534896Z` |
| Backup root entry count > 0 | confirmed, because the closed guard passed |

## Mutation contract

- Exact target: one preflight-approved numeric Task ID.
- Write: one `POST /api/v1/backup-repositories/connect` using the numeric `taskId` stored by the passing preflight.
- No automatic retry.
- Immediate postconditions: repository online/access active, active task-link lineage for the Task, one observed mutable-head recovery point.
- Failure rollback: `POST /api/v1/backup-repositories/${repositoryId}/disconnect` using the exact 32-hex ID returned by Connect; this revokes product access/linkage and does not delete backup files.

The guarded production write was executed exactly once for Task 3. Connect returned HTTP/API code 200 with request ID `20e52ce8b399fe0f`, repository `0d7d7b3098bdad32426a0807b2a8ee42`, and recovery point `e35fca267e10c228ee6858dcadb787ad`. The immediate closed postcondition returned true: repository detail and recovery-point list were HTTP 200, the Rsync repository was online with active access, its active Task link used `legacy_mutable`, and the listed recovery point was `mutable_head`, `observed`, and produced by Task 3. The two supplied console transcript attachments were byte-identical (`sha256 ea8043e7d75708d02bc40dafe857cad5ba61d82b6dd20b8e72eb247ae7bfcbef`), so they are duplicate evidence of the same request rather than evidence of a second Connect. No retry occurred. Failure-only Disconnect is therefore not invoked.

## End-to-end evidence

| Check | Result |
|---|---|
| Repository exists | `0d7d7b3098bdad32426a0807b2a8ee42`; online/access active |
| Active Task link exists | confirmed for Task 3 / Node 3, `legacy_mutable`, active |
| Recovery point exists | `e35fca267e10c228ee6858dcadb787ad`; `mutable_head`, `observed`, produced by Task 3 |
| Catalog complete and indexed | generation 1 failed with `catalog_build_failed`; automatic retry outcome pending |
| Content available / Catalog list permitted | pending |
| Delivery-ticket/UI preview authorized | pending |
| Exact-point file search HTTP 200 | pending |
| At least one real AssetRef | pending |
| UI metadata visible | pending |
| UI content preview visible | pending |
| Container healthy / restart count unchanged | pending |
| Critical errors absent | pending |
| Node-log collectors still 0 | pending |

## Gate result

`pending`. Node-log P1 remains blocked by this acceptance gate, and collectors remain disabled.

## Catalog failure evidence

The first condition-poll read returned HTTP 200 with request ID `f651f572f2014004`. It reported no active complete generation and a durable latest-build state of `failed`, then stopped immediately as designed. No Catalog build was retriggered, no process/container was restarted, Search was not attempted, and Disconnect was not invoked. The browser transcript collapsed the remaining object fields, so a one-shot GET-only projection was used to recover the stable fields without exposing the raw response.

That projection returned HTTP 200 with request ID `fba69107d384c350`. Generation `855925bca305b4c9e6a6a6f41c72ef88`, sequence 1, started at `2026-08-24T12:52:31.217263439Z` and finished at `2026-08-24T12:52:31.613951559Z`: a roughly 397 ms terminal failure. Its stable code is `catalog_build_failed`, coverage is `failed`, indexed entries are 0, no active complete generation exists, and the worker correlation ID is empty. Content remains available and Catalog list permission remains true.

Code truth classifies `catalog_build_failed` as retryable. For this exact point/generation and the default 15-minute reconcile / 30-minute build settings, the deterministic first retry eligibility delay is about 277 seconds; the worker still acts only on its next scheduled scan. No manual retry is authorized. A single GET snapshot after the next scan distinguishes a transient success from a repeated deterministic product/runtime failure.

The one-shot observation at `2026-08-24T14:03:02.796Z` found sequence 4 had failed with the same stable code. It started at `2026-08-24T13:52:31.22098592Z` and finished at `2026-08-24T13:52:31.588016909Z`, again in roughly 367 ms with zero indexed entries. Repository detail remained HTTP 200, online, and access-active; content/list stayed available; Task 3 remained successful with no active run and no Task run since `2026-08-24T12:21:11.290269058Z`. This excludes a single transient generation and satisfies the conditional product-gap trigger.

The leading code-level hypothesis is a SQLite bind-variable overflow at the first full Catalog persistence batch: the registered default logical batch is 2000 entries, `CatalogEntry` has approximately 17 persisted columns, and the bundled `go-sqlite3` source defaults `SQLITE_MAX_VARIABLE_NUMBER` to 32766. A single GORM Create can therefore attempt roughly 34000 variables. The hypothesis is not accepted as root cause until a real SQLite `Indexer.Build` regression test reproduces the production failure signature before production code changes.

The connected repository, active Task link, and observed recovery point remain the exact post-release acceptance scope. Disconnect remains deferred because access is valid and the child fix needs the same durable point for verification; no further production mutation is authorized before a fixed release.
