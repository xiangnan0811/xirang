# Child 8 Current-Main Evidence

## 1. Evidence boundary

This note records the source inspection used to scope Child 8. It is not an
implementation result or a validation report.

- Repository: `/home/murray/code/xirang`
- Work branch: `codex/backup-assets-content-plane`
- PR base: `main`
- Inspected baseline: `a3c309a922d9a4f48cb82031031c0975c251f5f4`
- `main` and `origin/main` both resolved to that commit after
  `git fetch --prune origin`; no baseline drift was observed.
- Child 7 PR #391 is present in that baseline. No detached Child 7 worktree,
  unmerged sibling, or post-baseline implementation was used.
- The only planning-tree changes before these artifacts were the new Child 8
  task, its parent-child registration, and corrected Trellis branch/base
  metadata.
- At evidence-capture time, `task.py start`, product code, migration DDL, tests,
  commit, push, PR, CI, merge, and release operations had not executed. This is
  a historical research snapshot; current execution status is recorded in
  `prd.md` and `implement.md`. Later Phase 2 gaps, including the read-only
  Provider byte reporter amendment, are recorded separately in
  `implementation-amendment-a-evidence.md`.

## 2. Parent contract inspected

The following parent sections remain authoritative:

- `.trellis/tasks/07-12-backup-data-explorer-design/implement.md`
  Sections 0, 0.4, 0.6, 9, 18-20 and 22.
- `.trellis/tasks/07-12-backup-data-explorer-design/design.md`
  Sections 6, 8, 9, 17-21 and 23.
- `.trellis/tasks/07-12-backup-data-explorer-design/prd.md`
  confirmed product decisions, safety constraints and acceptance criteria.

The parent assigns Child 8 paired migration `000066`, Content Broker,
delivery tickets/cookies, Range, authenticated chunk cache, core
classification/renderer policy, content-route/Nginx policy, and the frontend
ticket API boundary. It assigns full workspace UI to Child 9, Worker/Derived
Store and enhanced previews to Children 10-11, exports to Child 12, and the
`RecoveryResult` source adapter to Child 13.

## 3. Child 2 Provider and repository evidence

### 3.1 Existing bounded read ports

`backend/internal/backupasset/provider/contracts.go` already defines:

```go
type ReadSnapshot struct { /* exact repository/capability/source binding */ }
type ReadRequest struct { MaxBytes int64 }
type ByteRange struct { Offset, Length int64 }
type ReadHandle interface { io.Reader; Close() error }
type SequentialReader interface { OpenSequential(...) }
type RangeReader interface { OpenRange(...) }
```

Both request types reject non-positive/unbounded reads. `ContentStat` returns
size, modification time, source revision and optional media type. These are
the only legal byte-read primitives; Child 8 does not add a raw path, command,
SSH, or credential port.

Provider registrations in
`backend/internal/backupasset/runtime/runtime.go` prove the current capability
matrix:

| Provider | Sequential | Range | Current implementation evidence |
|---|---:|---:|---|
| Restic | yes | no | `provider/restic.go: OpenSequential`; bounded existing `restic dump` |
| Rsync | yes | yes when proven | mutable and committed adapters in `provider/rsync.go` |
| Rclone | yes | yes when proven | `provider/rclone.go`; existing bounded `rclone cat` offset/count |
| Command | no | no | not registered; stable typed unsupported |

Rsync and Rclone wrap readers with invariant checks that run on `Close`.
Rclone re-stats the object/root; Rsync revalidates its tree/root revision.
`provider/runner.go` owns process cancellation, stream closure, and command
join. Child 8 must always propagate the request context and close the returned
handle so those post-read checks and joins cannot be bypassed.

### 3.2 Repository is the credential boundary

`backend/internal/backupasset/repository/service.go` is the one process-wide
repository service with DB, Provider registry, keyring, publication admission,
and exact runtime binding dependencies.

`repository/catalog_read.go: OpenCatalogRead` demonstrates the required shape:

- callers provide opaque repository/recovery-point IDs only;
- the service loads the exact point and repository;
- Command fails with `task_artifact_contract_missing`;
- mutable heads require `observed` and immutable points require
  `committed|degraded`;
- the service resolves encrypted point/access evidence internally;
- publication admission and Provider sessions are closed together.

There is no current content-oriented equivalent. The legal seam is therefore
a narrow `content.SourceResolver` implemented in a new
`repository/content_read.go`; the Broker must not receive `provider.Registry`,
`AccessBinding`, native locators, SSH dialers, commands, or repository secrets.
The existing Provider commands are sufficient. Child 8 needs a new typed
`content_read` value in the admission ledger, not a new Provider command.

## 4. Child 6 Catalog evidence

`backend/internal/backupasset/catalog/service.go` provides the current source
of metadata truth:

- `authorizedActivePoint` requires an active `complete` Catalog generation;
- `loadCatalogEntry` always queries the tuple
  `(generation_id, recovery_point_id, entry_id)`;
- public points are exactly observed mutable heads and committed/degraded
  immutable points;
- list/detail DTOs omit `NormalizedPath`, `Fingerprint`, and
  `EncryptedProviderLocator`.

`backend/internal/model/backup_asset_catalog.go` shows that a Catalog entry
contains the exact generation/point/entry tuple, size/time/type/MIME,
fingerprint strength, security state, and an encrypted Provider locator. Model
hooks decrypt that locator only inside backend persistence reads; it is never a
DTO.

`backend/internal/backupasset/catalog/ownership.go` is the current ownership
authority. `Ownership.AuthorizedPointIDs` validates the candidate IDs first,
then applies Admin/Operator producing-lineage ownership. Viewer is not a valid
asset actor. Content issuance and every subsequent content request must reuse
this authority instead of trusting the authorization decision made at ticket
creation.

The paired `000065` migration added these exact parent keys, which `000066`
can reference without rebuilding Catalog tables:

```text
catalog_generations(id, recovery_point_id)
catalog_entries(generation_id, entry_id, recovery_point_id)
```

## 5. Child 7 Search, classification, step-up, and audit evidence

### 5.1 Minimal legal Search interaction

`backend/internal/backupasset/search/ingest.go` exposes
`ContentIndexIngest`, but publication requires all of the following:

- exact composite `AssetRef`;
- exact Catalog and Search generation IDs;
- exact source fingerprint;
- a `processing_job` RecoveryPoint lease ID, attempt ID and fence token;
- expected/current classification revisions;
- monotonically advancing coverage/pipeline/index revisions.

Child 8 has no ProcessingJob and therefore must never call this port. It must
not create content/OCR postings, excerpt references or ciphertext, durable
classification revisions, Worker jobs, or Derived Store rows.

The only legal optional interaction is a read of the active Search document's
closed `secret|non_secret|unknown` classification when it binds the same active
Catalog generation and source fingerprint. A Search classification may raise
risk; it cannot replace Child 8's bounded core scan or downgrade a core
`secret|unknown` result. Ticket-local classification is not written back.

### 5.2 Exact step-up purposes already exist

`backend/internal/auth/step_up_action.go` and the corresponding frontend
registry already contain:

```text
asset.secret_reveal
asset.download
asset.export_create
asset.export_download
asset.recover
recovery.result_download
recovery.result_retain
repository.purge
```

`backend/internal/api/handlers/step_up.go` validates token class, exact action,
user, role, token version, TOTP state, expiry and revocation. Child 7 also added
the optional secret-proof adapter used by Search. Consequently Child 8 does
not need to extend the action registry or credential-grant model. It needs a
content-specific adapter over the existing exact verifier:

- non-secret preview: no proof;
- secret/unknown preview reveal: only `asset.secret_reveal`;
- original download ticket: only `asset.download`;
- every other proof purpose cross-fails;
- `RecoveryResult` remains unsupported even with a recovery proof.

### 5.3 Audit primitives already exist

`backend/internal/backupasset/audit_action.go` already registers:

```text
preview_ticket, preview_read,
asset_download_ticket, asset_download
```

The append-only audit model already has bounded byte and Range summaries,
renderer/profile/source fields, step-up proof/action IDs, an internal grant ID,
and keyed path/query fingerprints. Its sanitizer rejects raw path, name,
query, snippet, content, ticket, cookie, JWT, token, secret, credential,
command, output and Provider locator fields.

Child 8 can reuse the existing registry and writer. The public `delivery_id`
must not be written to audit or application logs. The schema/design therefore
uses a separate internal `grant_id` for idempotent audit correlation and a
public non-authorizing `delivery_id` only for route lookup.

The writer's current unique-violation path retries and ultimately returns an
error. Therefore the partial unique index is necessary but not sufficient for
idempotent success: the Child 8 adapter must query and compare the exact
persisted content audit projection after a failed write. It may accept only an
exact match, never a generic unique error.

## 6. Session and logout evidence

`backend/internal/auth/jwt.go` issues a random 32-hex JTI, user/role,
`token_version`, expiry and token class. `RevokeToken` persists `jti:<id>` in
`token_revocations` and keeps an in-memory revocation map.

`backend/internal/middleware/auth.go` currently revalidates `token_version` on
normal Authorization requests, but only puts user ID, username, role and the
raw token in Gin context. It does not expose the already-parsed JTI/version/
expiry as a safe session binding.

`AuthHandler.Logout` revokes the JWT but has no content-session callback. A
cookie-only content request cannot re-run `AuthMiddleware`, so Child 8 must:

1. expose safe session binding facts from the existing parsed claims;
2. store no JWT, only the JTI/session facts and random cookie-secret hash;
3. check JWT revocation plus current user role/token version on every content
   request and heartbeat;
4. add a narrow logout revoker callback to cancel/revoke matching delivery
   grants immediately. JWT revocation remains authoritative if that best-effort
   cancellation callback reports an error.

## 7. RecoveryPoint lease evidence

`LeaseHolderContentSession` has existed since the foundation migration.
`backupasset.LeaseService` already provides acquire/renew/release/takeover,
absolute deadlines, heartbeat timestamps, durable attempt/fence tokens, and
expired-lease reconciliation. Renew/release/validate use the complete fence
tuple and reject stale/expired/deadline-exceeded holders.

`000065` only added `search_index`; it preserved `content_session`. Child 8
does not need to rebuild the lease closed set. Each delivery grant must own a
`content_session` lease and use its `delivery_id`-independent internal grant ID
as the owner. A restarted process revokes prior active grants and refuses old
fences; it may only take over an expired lease for bounded cleanup, never to
resume a stolen cookie silently.

`LeaseService.resolveAcquireDeadlineTx` distinguishes zero and explicit
deadlines. A zero-deadline holder receives a fresh bounded deadline from the
foundation configuration. An explicit deadline participates in the immutable
multi-stage contract and is compared with prior point evidence. Publication
uses the explicit form; Catalog and Search use zero. Content must likewise use
zero for its independent holder deadline and keep its much shorter
session/proof/profile expiry on the grant. It must not reuse a historical
publication deadline and does not require modifying the lease service. This
clarification supersedes the original planning interpretation; implementation
evidence is recorded in `implementation-amendment-b-evidence.md`.

## 8. HTTP and logging evidence

### 8.1 Backend server

`backend/cmd/server/main.go` uses one `http.Server` with:

```text
ReadHeaderTimeout = 10s
ReadTimeout       = 30s
WriteTimeout      = 30s
IdleTimeout       = 60s
```

Those global values must remain unchanged. Gin 1.11's response writer exposes
the underlying writer through `Unwrap`, so a bounded
`http.ResponseController` write-deadline wrapper can extend active content
writes only up to the grant's absolute deadline.

The runtime is currently constructed and started before the JWT manager is
created. Child 8 needs to move creation of the same JWT manager earlier and
pass a narrow session validator into the content graph; it must not construct a
second manager or revocation cache.

### 8.2 Router and structured logs

`backend/internal/api/router.go` puts normal asset routes under
`AuthMiddleware`, audit middleware, API rate limiting and body limits. The
content route does not yet exist. It must be registered separately under
`/api/v1` because native elements cannot send Authorization, while issuance
stays in the secured group.

Global CORS currently allows configured origins with credentials. The content
route must remove CORS exposure, require same-origin Fetch Metadata when
present, set same-origin resource policy, and accept only the delivery cookie.

`backend/internal/middleware/structured_logger.go` records
`Request.URL.Path` verbatim.
Without a route-specific redaction, that would log the public delivery ID. The
logger must normalize the exact content path to a constant route label. The
content package may emit only a per-process keyed delivery fingerprint.

The router also installs Gin's generic recovery globally. A panic request dump
can contain the raw URI and Cookie header, so a content-local safe recovery
middleware must catch content panics before they reach that outer logger.

### 8.3 Nginx

`deploy/nginx/templates/default.conf.template` currently has only a generic
`/api/v1/` location. It enables global gzip, uses normal access logging,
inherits broad page CSP, leaves response buffering at the default, and has a
generic `proxy_read_timeout 3600s`.

Child 8 needs a higher-precedence exact content location with:

- its own log format containing only request ID, status, bytes and timings;
- no `$request`, `$request_uri`, `$uri`, `$args`, cookies, referrer or agent;
- buffering/cache/temp-file/gzip disabled for the content route;
- bounded content-only read/send timeouts;
- a location-local disabled unformattable error log, because Nginx error logs
  may include the full request URI;
- Range/If-Range forwarding and no global API timeout change;
- the same all-in-one port `10761`, TLS termination assumptions and image
  namespace.

There is no rendered-config assertion today. A dedicated script/self-test and
CI step are required in addition to `nginx -t`/Docker build.

## 9. Cache and deployment evidence

The current image creates/chowns `/data`, `/backup`, `/logs` and Nginx cache
paths. It has no dedicated content cache root. `/data`, `/backup` and `/logs`
are persistent/source-sensitive and are forbidden for content cache use.

Child 8 therefore needs a dedicated non-volume root such as
`/var/cache/xirang/asset-content`, created `0700` and owned by `xirang` in the
all-in-one image. Runtime validation must resolve the configured path, reject
symlink/bind/source overlap, and ask `repository.Service` only for a boolean
source-conflict decision so Provider locators never leave the repository
boundary. Ambiguous validation disables disk cache and returns an explicit
capability reason; it never falls back to plaintext disk.

No existing keyring domain is appropriate for the cache. The parent contract
requires a random process key, so `wrapped_domain_keys`, `secure.EncryptString`
and any persistent KEK are intentionally unchanged.

## 10. Migration reservation and `000065` down evidence

Both migration directories end at paired
`000065_backup_asset_search.{up,down}.sql`. No `000066` file exists. The parent
reservation remains:

| Version | Owner |
|---:|---|
| `000066` | Child 8 content plane |
| `000067` | Child 10 Worker protocol |
| `000068` | Child 12 export/archive |
| `000069` | Child 13 controlled recovery |
| `000070` | Child 14 lifecycle/reconnect |
| `000071` | Child 15 GA hardening |

`000065` has real SQLite and PostgreSQL apply/down fixtures. Its down migration
starts with an atomic guard over Search key/lease/projection/overlay state,
then restores the exact `000064` key and lease checks. The tests compare schema,
rows and migration state before/after a rejected used down.

`000066` can remain additive and must not alter either closed set. Its pristine
down returns to `000065`. Its own down guard must reject any delivery grant,
request, usage row, or `content_session` lease. An explicit runtime drain may
make ephemeral state pristine only after the route is closed, cookies/grants
are revoked, readers joined, old fences rejected, audit summaries reconciled,
leases released/expired and all three Child 8 tables are proven empty. The
migration itself never deletes data to make its guard pass.

## 11. Frontend boundary evidence

The current frontend already has strict AssetRef and closed-projection helpers
in `web/src/lib/api/backup-assets-boundary.ts`, and all normal JSON calls use
`request<T>()` from `web/src/lib/api/core.ts`. Search/overlay wrappers keep raw
snake_case values private and fail the whole projection on an unknown enum or
coupled invalid state.

Child 8 needs only shared delivery domain unions plus
`backup-content-api.ts` and mapper tests. It must not add a page, component,
router entry, hook, browser storage, media element, Blob implementation or
preview workspace. The issuance call uses Authorization and optional exact
step-up; the returned content URL is treated as an opaque, same-origin,
query-free value and is never reconstructed from IDs or persisted with JWT,
cookie, query or path data.

## 12. Gap-to-file conclusion

The source inspection identifies these implementation seams:

- new `backend/internal/backupasset/content/` domain package;
- new `backend/internal/backupasset/repository/content_read.go` adapter;
- paired `000066` plus content models and dual-engine behavior fixtures;
- existing Foundation settings/runtime/lease/audit/authorization composition;
- safe JWT session facts and logout callback, without storing JWT;
- separate secured issuance and cookie-only content handlers;
- route-path log redaction and content-only Nginx policy;
- dedicated image cache directory and rendered-config checks;
- strict frontend DTO/API mapper only.

No evidence requires `000067+`, a new Provider command, a keyring domain,
`ContentIndexIngest`, Derived Store, Worker, workspace UI, export/recovery,
`RecoveryResult` activation, Command Provider support, a global server timeout
change, or default-enabling `backup_assets.enabled`.
