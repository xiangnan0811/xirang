# v0.55.16 real-data acceptance — 2026-09-17

## Current disposition and evidence boundary

The existing acceptance task remains `in_progress` pending final evidence review
and archival by the coordinating session. Task 13 is the user's named
positive production sample for search and preview. Previously indexing tasks now
preview successfully after indexing completes; the user reports no task currently
unable to preview. There is no remaining reproducible preview failure in that
report. The user subsequently confirmed all requested UI/security cases normal.
The current consolidated functional acceptance is ready for archival; evidence
limitations are explicitly stated below and historical unexecuted checks stay
unchecked.

This record supersedes current-status and execution instructions in the older
v0.50.x plans and [v0.55.15 lease diagnosis](v05515-lease-recovery.md), preserving
their historical evidence. Do not recreate the ten archived repairs. The parent
remains open; P3 quality-debt work follows acceptance and parent review. Node-log
P1 was separately accepted: preserve its existing collector configuration rather
than applying the historical collectors-must-be-zero gate.

The user explicitly declined an additional pre-upgrade backup (`无需备份`) and
reported performing the upgrade themselves. Do not request that backup again or
claim a recoverable backup exists. NAS execution and UI observations below are
user-reported; release/CI/registry verification is repository delivery evidence,
not a substitute for final host acceptance.

## Delivered repair and release

| Evidence | Verified result |
| --- | --- |
| Fix PR | [#538](https://github.com/xiangnan0811/xirang/pull/538), squash `952494f68db36a21fb80b7e2e3d8d822db4adbe2` |
| Fix PR / post-merge CI | [35096963730](https://github.com/xiangnan0811/xirang/actions/runs/35096963730) / [35100262172](https://github.com/xiangnan0811/xirang/actions/runs/35100262172): success |
| Release PR | [#539](https://github.com/xiangnan0811/xirang/pull/539), head `4721418863069462054c89891428e5190de6de23`, squash `1862dde234490c48896254c3e1b3fcc37c573272` |
| Release PR / exact main CI | [35100305299](https://github.com/xiangnan0811/xirang/actions/runs/35100305299) / [35103908247](https://github.com/xiangnan0811/xirang/actions/runs/35103908247): success |
| Public release | [v0.55.16](https://github.com/xiangnan0811/xirang/releases/tag/v0.55.16), published `2026-09-16T13:45:04Z` |
| Release Please / Docker | [35103908117](https://github.com/xiangnan0811/xirang/actions/runs/35103908117) / [35103932136](https://github.com/xiangnan0811/xirang/actions/runs/35103932136): success |
| Official image | `docker.io/linnea7171/xirang:v0.55.16` |
| Manifest digest | `sha256:fd9eaeba69e5871684a520f2cce1e7305e3faef9ec1e26f14d21bff966097ef2` |
| amd64 digest | `sha256:c90aa65fb986282f78aec891c69c060e1984cb39d7efc18dc2e057ab084190d9` |
| arm64 digest | `sha256:65a72df0926d1eac2d0b340c21102f2e8bdc96077d992ed8fa960226d5ea899b` |

Both registry architectures reported version `0.55.16` and revision
`1862dde234490c48896254c3e1b3fcc37c573272`. Required CI passed after the local
fixture limitations recorded in the earlier diagnosis; no claim is made that a
single local full-backend invocation passed. The repair reclaims only expired
Catalog/Search owner slots transactionally and preserves fencing. Production
leases were not repaired with SQL.

## Production observations

The pre-upgrade Admin readiness, acknowledged digest, FeatureLive, online
repositories and LAN HTTP policy remain dated evidence in the v0.55.15 record.
Do not relabel those observations as fresh post-upgrade health checks.

1. On `2026-09-16T14:25:34Z`, Task 3 search returned HTTP 200 against old Catalog
   378, generation `602d04c03ef108a0a76f1e4236c967c2`, with failed index coverage,
   no items, `total_relation=unavailable` and `authoritative_empty=false`.
   Preview-source requests at `14:26:59Z` and `14:27:04Z` returned HTTP 503; the
   user supplied the generic body `备份内容服务暂不可用`. This was not an
   authoritative empty search result or a diagnosed permission denial.
2. A later catalog-status GET returned HTTP 200 for Task 3 point
   `e35fca267e10c228ee6858dcadb787ad`. Active generation 390,
   `22252d939e81d39ed311d15fe76d3d73`, completed from
   `2026-09-16T14:29:47.189421116Z` to `2026-09-16T14:37:42.121495604Z`, with
   complete coverage and 60,515 indexed entries. This proves a completed
   post-upgrade Catalog build. It is a sampled historical generation, not an
   assertion of the permanently latest generation.
3. In that same sample, latest build 402 was building from
   `2026-09-17T01:58:37.664283705Z`; content was available and list permitted.
   `stale / mutable_source_changed` alone does not prove a physical file change;
   the observation can age out. Catalog's `preview=false / download=false`
   fields are its list-only projection, not content authorization denial evidence.
4. On 2026-09-17 the user confirmed Task 13 search and preview work normally.
   Its repository from the earlier live inventory is
   `4d5f1d4e4ae72187885bface9e3382a8` (Rsync, node 10, online, access active,
   active Task 13 link, `legacy_mutable`). No exact Task 13 recovery-point/file
   identifier, request status or timestamp was supplied for this successful UI
   observation; none is inferred from Task 3.
5. The user also confirmed that the previously indexing tasks had completed
   indexing and could preview, and that no task remained unable to preview.
   Directory browsing had already been reported working. This closes the
   observed positive UI-path failure, without proving every security or health
   acceptance item.
6. A subsequent user-supplied NAS `docker inspect` reports image
   `linnea7171/xirang:v0.55.16`, `running`, `healthy`, restart count `0`, and
   started-at `2026-09-17T01:43:04.996732851Z`. This verifies the current image
   tag and container health/count. The earlier September 16 observations belong
   to an earlier container lifetime; do not attribute them to this start or infer
   uninterrupted uptime. Healthy status alone does not prove zero errors or a
   separately executed final `/healthz` probe.
7. The user subsequently confirmed all requested UI cases normal: Up navigation,
   Retry, readable preview frame, same-login fixed 45-minute secret-reveal reuse
   and logout invalidation, and retained-data visibility for stopped/abnormal
   tasks. These are user-reported production passes; no precise execution times,
   request IDs or credentials were supplied or inferred.

The private HAR files contain no response text and are not committed or copied
into this evidence. Only sanitized request outcomes and separately supplied
response fields are recorded. No headers, cookies, tokens, proof, asset names,
paths, locators or file content belong in the task record.

## Remaining consolidated acceptance

- [x] Record the delivered fix, formal release and required CI separately from
  live production evidence.
- [x] Record a completed post-upgrade Catalog generation and positive directory,
  search and preview UI observations, with Task 13 as the named passing sample.
- [x] Record the user's report that no preview failure remains after indexing.
- [x] Record the current post-upgrade image tag, running/healthy state and zero
  restart count from the user's NAS inspection, with its distinct start time.
- [x] Record the user's functional observation window: previously indexing tasks
  now preview and no current preview failure remains. Raw server logs were not
  re-audited, and no separate final `/healthz` result was supplied. This is not
  evidence of zero server-log errors. Preserve accepted node-log settings.
- [x] Confirm authorized retained-data visibility, Up navigation, Retry and the
  representative readable preview frame through the intended LAN HTTP route.
- [x] Confirm exact 45-minute non-sliding secret-reveal reuse in the same login,
  plus expiry/logout invalidation; a normal preview pass alone does not prove
  this. Keep credentials and proof in the user's browser.
- [x] Review the consolidated requirements in [reconciliation.md](../reconciliation.md)
  and resolve or explicitly disposition any remaining item before archival.

The coordinating session accepts the current same-day functional observation
(no remaining failures after indexing) plus running/healthy status and zero
restarts as the consolidated bounded health/error observation. A standalone
`/healthz` probe and raw server-log audit were not repeated; historical R6 rows
for those exact checks remain unchecked. This explicitly scoped acceptance does
not assert zero server-log errors or a particular deployed healthcheck command.
Do not convert those limitations into claims of execution or require
production fault injection. This documentation-only continuation requires a docs
PR and necessary CI; no new GitHub Release or Docker Hub publish is expected.
