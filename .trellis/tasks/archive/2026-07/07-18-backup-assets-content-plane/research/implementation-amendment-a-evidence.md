# Child 8 Focused Implementation Amendment A Evidence

## 1. Purpose and status

This note records implementation-time evidence discovered after the approved
Child 8 plan entered Phase 2. It is a planning amendment, not implementation or
validation evidence. Three newly listed test files had already been edited during
Task 3 exploration before the manifest mismatch was noticed; they are disclosed
below. The user explicitly approved this focused amendment on 2026-07-18,
opening the exact expanded manifest for continued implementation.

- Branch/base remain `codex/backup-assets-content-plane` at
  `a3c309a922d9a4f48cb82031031c0975c251f5f4` after a fresh
  `git fetch --prune origin` on 2026-07-18.
- The original paired `000066` and closed ticket/session contracts exist in the
  worktree. Task 3 source-contract tests started in an intentional RED state,
  followed by a partial `repository/content_read.go` implementation and
  `content_read` admission enum. The package remains compile-time RED because
  the closed Content source contract is intentionally still absent.
- This amendment does not change migration ownership, activate
  `RecoveryResult`, add Provider commands, enable the feature, or enter Child
  9-15 scope.

## 2. Provider-byte accounting gap

`backend/internal/backupasset/provider/restic.go` implements
`boundedReadHandle`. When the caller consumes exactly `MaxBytes`, `Read` or
`Close` reads one additional byte from the underlying Provider stream to prove
whether the source overflowed. That byte is deliberately hidden from the
caller while an overflow becomes `provider_resource_limit`.

`provider.ReadHandle` currently exposes only `Read` and `Close`, so a Content
Broker wrapper can count response-visible reads but cannot observe the hidden
probe. This conflicts with the frozen rule that final accounting charges the
greater of actual Provider bytes and successful response bytes. It also makes
the current `provider_bytes <= reserved_bytes` SQL contract unverifiable at a
truncation boundary.

Two wrappers can additionally hide optional accounting from the Content
adapter:

- `provider.commandInvariantHandle` wraps the bounded Rclone Range handle;
- `repository.managedRsyncPointReadHandle` wraps committed Rsync reads.

The minimal repair is an optional, read-only
`provider.ProviderByteReporter{ProviderBytes() int64}` on handles,
implemented by `boundedReadHandle` and forwarded by wrappers that can contain
it. The Content-owned source handle always counts visible reads and takes the
maximum of that counter and the Provider reporter. A path without a reporter is
legal only when no hidden Provider read exists; failures or missing final
evidence charge the full reservation. This adds no command, credential,
locator, mutation, or public API.

Exact newly mutable files:

```text
backend/internal/backupasset/content/source_contracts.go
backend/internal/backupasset/content/source_contracts_test.go
backend/internal/backupasset/provider/contracts.go
backend/internal/backupasset/provider/restic.go
backend/internal/backupasset/provider/runner_test.go
backend/internal/backupasset/provider/rclone.go
backend/internal/backupasset/provider/rclone_test.go
backend/internal/backupasset/repository/query.go
backend/internal/backupasset/repository/query_test.go
backend/internal/backupasset/repository/testutil_test.go
```

`source_contracts_test.go` already exists as an intentional compile-time RED
test and `repository/query_test.go` plus `repository/testutil_test.go` already
contain Task 3 test-fixture edits even though the original enumerated manifest
omitted those filenames. This amendment discloses and regularizes those edits
before any further implementation. `source_contracts.go` is not created. The
already planned `repository/content_read.go` and its tests remain the only
Content-facing source adapter.

The partial adapter also revealed two correctness repairs that stay inside the
already listed source files:

- it currently acquires `OperationContentRead` after exact Catalog/runtime
  loading, although those model/runtime reads can decrypt Provider locator or
  access material; acquisition must move before any such load/Provider port;
- its `Close` revalidation uses unbounded `context.Background()`; cleanup must
  use a cancellation-detached but strictly deadline-capped context and fail
  closed when post-read identity cannot be proven.

The closed Content reader contract will expose `ProviderBytes() int64`, with the
Repository adapter combining visible reads and the optional Provider reporter.
No raw Provider handle or reporter escapes the sealed source session.

## 3. Representation identity gap in `000066`

The grant currently stores `source_size`, detected MIME and representation
ETag, but not the representation byte length or whether a bounded text/hex
projection is truncated. `source_size` cannot stand in for `Content-Length`:
escaped text can expand and hex output is a different byte representation.
Recomputing limits from live settings would also let one ticket change shape
after issuance.

The paired migration/model therefore need three frozen fields:

```text
representation_source_bytes >= 0 and <= source_size
representation_size >= 0
representation_truncated closed boolean
```

Raw raster/PDF/audio/video/original representations require source bytes and
representation size to equal `source_size` and `truncated=false`. Text/hex bind
the exact source prefix consumed and the exact deterministic output size;
`truncated` is equivalent to `representation_source_bytes < source_size`.
The response `Content-Length` comes only from `representation_size`.

Worst-case reservation is the overflow-safe maximum of response bytes and the
Provider read bound, including the one-byte overflow probe when it can occur.
Actual final charge is the maximum of reported Provider bytes and response
bytes. A request that cannot reserve probe overhead is rejected before source
open rather than reading past its budget.

These edits stay inside the already approved model/migration/integration-test
manifest and retain paired SQLite/PostgreSQL behavior and the same guarded
down migration.

## 4. Ticket deadline and materialization boundary

The process-wide HTTP server has `WriteTimeout=30s`. The approved issuance
pipeline can open and scan a Provider, but it did not freeze a handler deadline
strictly below that timeout. A slow scan could otherwise be terminated by the
outer server after work/lease acquisition without a controlled response.

Add `backup_assets.content_ticket_timeout` with default `20s`, allowed range
`1s..25s`, and require it to stay below the unchanged global 30-second write
timeout. Issuance derives a context deadline from the earliest of that timeout,
session/proof/profile/lease boundaries, performs only bounded stat/sniff/
classification and bounded in-memory text/hex preparation, and releases the
lease without issuing a cookie on timeout. Large full-object authenticated disk
materialization is never synchronous inside the ticket POST; it occurs only on
an admitted content request with its own reservation/deadline.

## 5. CORS, route canonicalization, and origin evidence

The current CORS/security middleware is inline and global in
`backend/internal/api/router.go`; it handles every `OPTIONS` request with 204
before route middleware. A future content route registered below it would
therefore expose a preflight path that bypasses content-specific origin/Fetch
Metadata checks. Gin also enables trailing-slash redirects by default; an
unregistered `/asset-content/<id>/` can redirect before the content middleware
chain runs.

The amended route contract is:

- recognize the asset-content prefix before the global CORS branch and emit no
  ACAO/credential/preflight success headers for it;
- register only canonical GET/HEAD delivery and explicit safe rejection for
  OPTIONS, unsupported methods and the trailing-slash form;
- run constant path redaction and content-local recovery for every recognized
  content-shaped request, including rejected variants;
- require an empty query and strict 32-hex delivery ID in the application;
- keep ordinary API CORS and route behavior unchanged.

The Nginx template currently sends `Host $host`, which removes an explicit
`:10761`, and overwrites `X-Forwarded-Proto` with the inner `$scheme`. The exact
content location must instead preserve the validated external Host including
port and a closed `http|https` effective-proto value so same-origin comparison
works both on direct port 10761 and behind the documented external TLS proxy.
The content location remains independently redacted and bounded; generic API
timeouts, listener, image source and TLS ownership do not change.

## 6. Scope and cache quota closure

`backup_asset_delivery_usage` stores `request_count`, but the approved settings
defined byte windows and concurrency only. Add closed per-window request
maxima for user, Provider and global scopes so the counter has an enforceable
policy limit.

The cache contract also requires bytes/files at object, user, Provider and
global scopes. Existing settings included only disk byte scopes and a global
file count, plus memory object/global bytes. Add:

```text
content_user_window_requests
content_provider_window_requests
content_global_window_requests
content_memory_user_bytes
content_memory_provider_bytes
content_cache_object_files
content_cache_user_files
content_cache_provider_files
```

Child 8 cache entries are deliberately owner-partitioned: there is no
cross-user cache sharing. The owner ID joins the opaque cache identity and AAD;
same-user grants may reuse only the exact resource/source/representation
generation. This makes per-user quota authoritative, prevents a second user
from inheriting another user's materialization, and keeps Provider/global
accounting explicit. Cross-field validation orders object <= user/provider <=
global for both bytes and files; the derived chunk count plus manifest must fit
the object file limit before any write.

## 7. Scope conclusion

The amendment expands the original exact manifest only to the ten files in
Section 2: two focused Content contract files and eight existing Provider/
Repository files. All other repairs use already approved migration, content,
settings, router, Nginx, test and Trellis files. `provider/**` remains unchanged
outside those exact files. Writing this evidence note alone did not authorize
implementation; the separate explicit approval recorded above and in
`implement.md` opened that gate.
