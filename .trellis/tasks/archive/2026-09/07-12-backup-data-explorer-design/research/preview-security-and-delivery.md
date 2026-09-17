# Technical Research: Safe Preview And Content Delivery

- **Date:** 2026-07-12
- **Scope:** Threat model, browser delivery constraints, provider feasibility, file-type policy, caching, and operational limits for read-only access to backup contents.
- **Status:** Evidence and design inputs, updated after the user selected a complete end-state with optional processing Workers rather than an MVP-only renderer set.

## Core Security Principle

A successful backup may contain the most sensitive data in the fleet: private keys, database dumps, environment files, source code, credentials, customer documents, images, and executables. “Read-only” protects the repository from mutation, but it does **not** make content disclosure or browser rendering low-risk.

The content plane must therefore be treated as a distinct high-risk subsystem, not as another `tasks:read` JSON endpoint.

## Threat Model

| Threat | Example | Required control direction |
|---|---|---|
| Unauthorized disclosure | A viewer who can see task health opens `.env`, SSH keys, or database dumps | Dedicated asset list/preview/download permissions; ownership; server-side enforcement on every content request |
| Identifier/path tampering | Change task/snapshot/path parameters to reach another repository or `../` outside a root | Opaque asset/version IDs; normalized provider paths; containment after symlink resolution; never trust client-reported roots |
| Active-content execution | Backed-up HTML/SVG/XML/Office content executes script or external resources in the Xirang origin | `nosniff`; strict renderer allowlist; active formats displayed as escaped text/attachment; optional conversion Workers emit passive derivatives and never execute macros; never inject raw content into DOM |
| MIME confusion | `.jpg` contains HTML/JavaScript | Combine extension, provider metadata, and server-side signature/sniffing; renderer chosen from the safest classification; explicit `Content-Type` + `X-Content-Type-Options: nosniff` |
| Malicious/corrupt parser input | Crafted PDF/image targets a browser or converter vulnerability | Prefer maintained renderers; isolate and resource-bound optional conversion/scanning Workers; pin/update parser versions; preserve a metadata/download/restore fallback |
| Resource exhaustion | Multi-GB video, enormous directory, archive bomb, repeated thumbnail requests | Per-file byte limits, paging, timeouts, concurrency quotas, cancellation, cache quotas; Worker jobs have separate queues, expanded-size/duration limits, and failure isolation |
| Repository/node exhaustion | Many previews each open SSH and run Restic/Rclone | Per-user/task/node/provider concurrency; metadata cache; request coalescing; circuit-break/unavailable states |
| Secret leakage in logs | Raw file path, ticket, content, stderr, or command appears in process/audit logs | Path hashes and safe metadata only; query strings already excluded from current Nginx/backend access logs; sanitize upstream output |
| Ticket/token leakage | Session JWT placed in `<video src>` or a reusable URL | Never put JWT/SSH/repository credentials in URLs; use short-lived random resource-bound preview tickets; no-store/no-referrer |
| Plaintext cache residue | Decrypted Restic file remains after preview or crash | Opaque cache filenames, `0700` directory, strict TTL/quota, startup cleanup, atomic creation, no backups of cache, optional disable policy |
| Time-of-check/time-of-use swap | Local mirror path or object changes between stat and stream | Bind to a stable recovery point/provider version where possible; capture size/mtime/ETag; use `If-Range`; disclose mutable-mirror semantics |
| Cross-user cache bleed | Cached item for one owner is returned to another | Cache key includes provider/task/version/path identity; authorization always rechecked; cache stores bytes, not authority |
| Content exfiltration by “download” | Preview permission silently grants unrestricted original download | Separate preview and download capabilities; UI/API actions and audit events remain distinct |

The OWASP file-handling guidance is written for uploads, but its main rules still apply because backed-up content is untrusted input: do not trust `Content-Type`, allowlist business-critical types, set size limits, keep content outside the webroot, map opaque IDs to files, use authorization, and consider malware scanning/CDR where appropriate.

Source: [OWASP File Upload Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/File_Upload_Cheat_Sheet.html)

## Browser Authentication Constraint

### Current state

- Xirang's frontend sends the session JWT in `Authorization: Bearer ...` through the central fetch wrapper (`web/src/lib/api/core.ts:51-69`).
- Native `<img>`, `<audio>`, `<video>`, browser PDF navigation, and download links cannot attach that custom header.
- Fetch-to-Blob can attach the header, but it downloads the object into browser memory and is unsuitable for large seekable media.

### Rejected shortcut

Do not append the session JWT to a preview/download URL. A session credential in a URL can leak through browser tooling/history, copied URLs, referrers, screenshots, error reports, and any log format that later includes query strings.

### Recommended delivery primitive: preview ticket

1. An authenticated JSON request asks for a ticket for one exact action and object identity.
2. The server rechecks RBAC, ownership, provider capability, version/path validity, size/type policy, and resource quota.
3. It returns a same-origin URL containing a cryptographically random opaque ticket—not the JWT, repository password, path, or credentials.
4. The native element uses that URL and can issue byte-range requests.
5. The content endpoint validates ticket hash, action, resource, expiry, and allowed request behavior before opening the provider.

Suggested properties:

- at least 256 bits of randomness;
- short TTL (for example, 60–300 seconds, configurable);
- bound to `preview` or `download`, never both implicitly;
- bound to task/provider/version/entry and the requesting user in server state;
- allows multiple byte ranges during TTL for media playback, with request/byte ceilings;
- stored hashed if persisted; an in-memory store is preferable for the first single-process deployment shape;
- never included in process or audit logs;
- response headers include `Cache-Control: private, no-store`, `Referrer-Policy: no-referrer`, and a restrictive content disposition.

This is analogous to S3's time-limited presigned-object access, but remains a Xirang-local authorization handle rather than exposing storage-provider credentials.

Source: [Amazon S3 presigned URLs](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ShareObjectPreSignedURL.html)

## HTTP Range And Media Playback

Range requests allow media players to seek and download managers to resume. A correct single-range implementation needs:

- `Accept-Ranges: bytes` when the provider truly supports stable ranges;
- `206 Partial Content` and correct `Content-Range`/`Content-Length`;
- `416 Requested Range Not Satisfiable` for invalid ranges;
- stable ETag/version identity and `If-Range` handling where applicable;
- cancellation propagated to SSH/process/provider reads;
- one-range-at-a-time support is sufficient for the core provider contract; transcoded derivatives may expose a separate segmented-stream contract.

Sources:

- [MDN: HTTP range requests](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/Range_requests)
- [Amazon S3 GetObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html)

### Provider consequences

| Provider | Native read shape | Range strategy |
|---|---|---|
| Local Rsync target | Local regular file; seekable | Use a stable open file/`ReadSeeker` and standard HTTP range semantics after path containment checks |
| Restic snapshot | `restic dump` is sequential stdout; current browse commands run over SSH | Small bounded preview can stream sequentially; large/seekable media requires bounded materialization into an ephemeral local cache before serving ranges |
| Rclone remote | `rclone cat` streams; `--offset` and `--count` read a section | Map one HTTP range to offset/count, subject to object stability and backend behavior; use `lsjson --stat` identity/size metadata |
| Command task | No defined artifact provider | Unsupported until a future explicit artifact manifest exists |

Restic FUSE mount is intentionally not recommended as a dependency of the official core container: it adds kernel/FUSE privileges, mount lifecycle, cleanup, and deadlock/availability concerns that conflict with the lightweight agentless deployment.

Sources:

- [Restic restore, mount, and dump](https://restic.readthedocs.io/en/stable/050_restore.html)
- [rclone cat](https://rclone.org/commands/rclone_cat/)
- [rclone lsjson](https://rclone.org/commands/rclone_lsjson/)

## File-Type Rendering Policy

The renderer should use a server-produced `PreviewDescriptor`, not infer solely from the filename in React. Candidate fields:

- safe display name and opaque entry ID;
- logical path for authorized display;
- size, mtime, stable version/ETag if available;
- declared MIME, detected MIME, and confidence;
- renderer kind;
- preview limit and truncation state;
- delivery mode (`inline-json`, `ticket-stream`, `materialize-then-ticket`, `none`);
- range support;
- fallback reason;
- sensitivity warning/step-up requirement;
- provider capability and repository availability.

### End-state policy with an independent core fallback

| Content class | Core all-in-one behavior | Optional Worker augmentation | Important safeguards |
|---|---|---|---|
| Plain text/config/log/source | Escaped UTF-8 text preview, line-oriented and bounded (for example 1 MiB/20k lines), with truncation and original download | Full-text extraction/indexing, language/encoding detection, safe snippets | Never `innerHTML`; binary/NUL detection; explicit reveal warning for likely secret paths; index access follows content authorization |
| Common raster images | Inline original preview with fit/zoom/rotate and previous/next navigation | Version-keyed thumbnails, OCR, metadata extraction, malware scan | JPEG/PNG/GIF/WebP allowlist; byte/dimension/pixel limits; same-origin ticket; no unbounded decode |
| SVG/HTML/XML | Display as escaped source or force download; never render as an active same-origin document | Text extraction/indexing only; a future sanitizer must be separately threat-modeled | Correct `text/plain`/attachment and `nosniff`; block scripts and external resources |
| PDF | Maintained PDF.js-style canvas viewer using ticketed bytes, with ticketed new-tab/download fallback | Page thumbnails, OCR/text extraction, malware scan, and a passive normalized derivative when needed | No generic `<object>` under current `object-src 'none'`; parser isolation/limits; disclose derivative/partial preview |
| Audio/video | Native player for allowlisted codecs/containers when stable Range is available | Probe metadata/poster frames and transcode unsupported media into a bounded, allowlisted playback derivative | Size/duration/bitrate limits; job and byte quotas; clear source-versus-derivative label; download/restore fallback |
| Archive | Metadata plus download/restore | Optional malware scan and a bounded entry manifest if separately enabled | Never automatically execute or expand into the webroot; cap entry count, nesting, expanded size, and compression ratio |
| Office documents | Metadata plus original download/restore | Isolated read-only conversion to PDF/images/text for preview/search, plus malware scan | Never run macros, links, embedded executables, or an editing/collaboration service; label conversion fidelity limits |
| Database dumps | Bounded text preview only when confidently textual; otherwise download/restore | Full-text extraction for supported textual dumps | Secret warning; never execute, import, or connect automatically |
| Executable/binary/unknown | Metadata, existing checksum, and download/restore subject to permission | Malware scanning and safe classification only | Force attachment; never inline-execute; suspicious/quarantined result is visible and audited |

Google Drive's warning that previews are scaled representations and may differ from complete files is a useful product truth pattern. OneDrive's explicit codec/size limits support returning granular fallback reasons instead of pretending universal support.

## Response Header Policy

Every content response should set an exact safe MIME and retain `X-Content-Type-Options: nosniff`. MDN notes that `nosniff` prevents browsers from treating a `text/plain` response as HTML during navigation, reducing MIME-confusion XSS risk.

Source: [MDN: X-Content-Type-Options](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/X-Content-Type-Options)

Recommended response modes:

- `inline` only for an allowlisted passive renderer/MIME;
- `attachment; filename*=UTF-8''...` for unknown/active/executable content;
- sanitize the display filename and never use it as a server filesystem path;
- no cache for tickets/content by default; derived thumbnails may use a private version-keyed cache if later approved;
- no cross-origin resource access;
- preserve current restrictive CSP; add only the minimum source directive needed by the selected renderer path.

## Metadata Plane Versus Content Plane

The strongest backup-product precedent is to keep browse metadata cheap and content reads explicit:

```text
provider repository
      │
      ├── catalog/index sync ──> local metadata plane ──> browse/search UI
      │
      └── on-demand bytes ─────> content gateway/cache ─> preview/download/restore
```

Benefits:

- Directory navigation/search does not repeatedly decrypt or download file bytes.
- The UI can remain useful when a node/repository is temporarily offline, while clearly marking content unavailable.
- Content concurrency and byte costs can be governed independently.
- Index/cache loss is recoverable by rebuilding from the repository, following Duplicati and Proxmox patterns.

The local catalog is an optimization, not the source of truth. Repository/version identity remains authoritative.

Sources:

- [Duplicati restoring files](https://docs.duplicati.com/getting-started/restoring-files)
- [Proxmox interactive restores](https://pbs.proxmox.com/docs/backup-client.html#interactive-restores)
- [Kopia caching and restore methods](https://kopia.io/docs/features/)

## Ephemeral Materialization Cache

Restic video/PDF/large-file random access may require temporary plaintext materialization. If enabled, the design should include:

- a dedicated non-webroot directory owned `0700` by the Xirang runtime user;
- opaque random filenames unrelated to original paths;
- cache keys based on provider/task/version/entry identity, not user input;
- per-object and total-byte caps;
- short idle/absolute TTLs;
- per-task/node/user concurrency limits and request coalescing;
- atomic temp-file creation and publish-after-complete;
- startup plus periodic cleanup, including crash residue;
- no inclusion in Xirang's own database backup or managed backup roots;
- content authorization rechecked even on a cache hit;
- telemetry containing counts/bytes/durations only, never paths/content;
- an admin setting to disable plaintext materialization entirely.

The official image currently has persistent `/data`, `/backup`, and `/logs` mounts and runs as a non-root user. A cache location/lifecycle must be deliberate; silently placing decrypted data under `/data` would make previews durable, while relying on unlimited `/tmp` risks disk exhaustion.

### Feasibility findings from the current deployment

- The default Compose file bind-mounts only `/data`, `/backup`, and `/logs`; it does not define a dedicated cache path or `tmpfs`. `/data` and `/backup` are explicitly persistent/backup data and must never be implicit preview workspaces.
- `/tmp` belongs to the container writable layer unless an operator adds a `tmpfs`. It is neither quota-controlled by Xirang nor guaranteed to disappear on a backend-process restart.
- Docker documents that a `tmpfs` is removed when the container stops, supports `noexec`/`nosuid`/`nodev`, size and mode limits, but may still be written to host swap. “tmpfs only” therefore requires encrypted/disabled swap if the product claims that plaintext never reaches storage media.
- Xirang's current `secure` package encrypts small database strings by applying AES-GCM to the whole value and Base64-encoding it. It is not a seekable/chunked large-file container and should not be reused unchanged for media cache files.
- The current SSH-key temporary directory is `0700` and removed on graceful shutdown, but a crash can leave the per-process directory behind; the code does not scan all old process directories at startup. The content cache needs its own lease-aware startup and periodic reconciler rather than treating graceful cleanup as sufficient.
- Production already requires a strong `DATA_ENCRYPTION_KEY`, but using that durable key for a disposable cache would make crash residue decryptable across restarts and create rotation coupling. A per-process/per-boot random cache key makes residual ciphertext cryptographically disposable after restart, at the cost of rebuilding every cached derivative/materialization.

### Selected materialization policy and rejected alternatives

The user selected **tiered secure materialization** on 2026-07-13. The other rows remain recorded to preserve the trade-off analysis; they are not active design choices.

| Policy | Shape | Capability impact | Residual risk |
|---|---|---|---|
| **Tiered secure materialization (recommended)** | Small/sequential reads stay in bounded memory; large seekable content is stored as independently authenticated encrypted chunks under a dedicated quota-controlled cache using an in-memory per-boot key; unavoidable Worker file workspaces use a tightly capped dedicated `tmpfs` only | Supports Restic Range playback, scanning, conversion, and transcoding while keeping the core stream path independent | Plaintext still exists in process/Worker memory; `tmpfs` can swap unless the host controls swap; implementation and recovery logic are more complex |
| Permission-protected plaintext cache | Materialize original/derived bytes under a `0700` non-webroot directory with leases, quotas, TTL, startup/periodic cleanup | Simplest and fastest; broad tool compatibility | Crash residue, host snapshots/backups, filesystem forensics, and privileged-host reads can expose plaintext |
| Memory/stream only | Never create a plaintext or encrypted disk materialization | Small text/images and sequential download remain possible | Large Restic seek, Office conversion, malware scanning, and media transcoding become unavailable or unreliable; memory pressure grows |

For the recommended policy, external tools should consume a pipe or loopback resource-bound stream whenever they can. If a tool strictly requires a seekable pathname, the job uses the capped `tmpfs` workspace or reports a precise unavailable reason; it must not silently fall back to an ordinary plaintext disk file. Derived thumbnails, OCR text, converted documents, and transcoded media are as sensitive as their originals and use the same authorization, version binding, encryption, quota, and retention rules.

Sources:

- [Docker tmpfs mounts](https://docs.docker.com/engine/storage/tmpfs/)
- [OWASP Cryptographic Storage Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html)

## Authorization Model Inputs

Current `tasks:read` is too broad for content. Candidate permissions should separate:

- `backup_assets:list` — repository/version/tree metadata;
- `backup_assets:preview` — bounded inline content;
- `backup_assets:download` — original bytes;
- existing restore permission/grant path — infrastructure write operation.

Current-code consequences are concrete:

- `viewer` has `tasks:read`, and `OwnershipTaskCheck` explicitly bypasses ownership for both admin and viewer. Reusing `tasks:read` would therefore expose filenames or content across every task visible to a viewer.
- Live-node file list/content uses the narrower `nodes:files` permission, which admin/operator have and viewer lacks. This is a closer precedent for sensitive bytes than snapshot metadata.
- The unused local Rsync backup-file list is currently admin-only, while Restic snapshot paths use `tasks:read`; the new feature must remove this inconsistency rather than preserve it.
- Existing step-up proofs require enabled TOTP, are bound to user, role, token version and a dedicated purpose, and expire after five minutes.
- Existing restore routes require admin role, a valid step-up proof, and a resource/action-bound self-grant with reason and TTL. This remains appropriate because restore writes into infrastructure, unlike browse/preview.

Recommended baseline role shape for user review:

- **Admin:** all owned/platform assets, preview/download; restore still step-up/JIT.
- **Operator:** metadata and preview only for owned nodes/tasks; download may be separately granted.
- **Viewer:** backup health/status only by default, no filenames/content.

Whether sensitive file previews/downloads require step-up on each short session is a product-risk decision. Path/extension classification can assist warnings, but must not be treated as a reliable secret detector.

The user selected **practical least privilege** on 2026-07-13. The alternatives remain below as rejected trade-offs:

| Policy | Role mapping | Step-up behavior | Trade-off |
|---|---|---|---|
| **Practical least privilege (recommended)** | Admin lists/previews/downloads all; operator lists/previews owned-task assets but original download is separately restricted; viewer sees backup health only, not filenames/content | Ordinary allowed preview needs no repeated TOTP; likely-secret reveal and every original download require a reusable five-minute step-up proof; restore keeps step-up + reasoned task grant | Good single-admin usability with defensible future role boundaries; secret classification needs cautious false-positive/false-negative UX |
| Admin-only content | Only admin can list filenames, preview, search, and download | Download/restore step-up; preview normally does not | Simplest for today's deployment, but makes existing operator ownership model artificially useless and encourages later permission redesign |
| Task-read convenience | `tasks:read` implicitly grants list/preview; download restricted only loosely | Restore alone requires step-up | Least friction, but viewer's current global bypass makes it an unacceptable disclosure boundary for configurations, keys, dumps, images, and media |

Under the recommended policy, a sensitivity classifier can only increase friction or display warnings; it can never grant access. A “not sensitive” classification does not override RBAC, ownership, ticket binding, audit, or download step-up. Search snippets, OCR text, thumbnails, and converted documents require preview permission because they disclose content even when the original byte stream is not opened.

## Audit And Privacy

Use distinct actions and record only safe evidence:

- `backup_asset.list`
- `backup_asset.preview`
- `backup_asset.download`
- `backup_asset.ticket_issue`
- `backup_asset.ticket_reject`

Safe fields include actor/resource IDs, provider kind, outcome, detected class, bytes served, range/full, truncated, cache hit, duration, and a keyed or stable path hash. Do not store raw path, filename, query ticket, repository target, content, thumbnail, command, stdout/stderr, or credentials.

The current credential-audit sanitizer and `safePathHash` pattern are strong reusable foundations.

## Operational Guardrails

- Paginate directories; do not preserve the current hard `first 500 then truncate` behavior as the only contract.
- Cap search results and support cursors.
- Limit concurrent upstream SSH sessions separately from generic 200 requests/minute API limiting.
- Enforce per-user and global bytes-in-flight/materialization quotas.
- Cancel upstream commands when browser navigation closes the request.
- Use bounded command timeouts that differ for metadata, text preview, materialization, and download.
- Expose `429`/`503` with retry guidance for capacity/repository unavailability.
- Do not let preview failure affect backup scheduling, retention, verification, or restore workers.
- Track preview/cache health separately from backup confidence; a preview outage must not imply the backup is corrupt.

## Compatibility Findings From Current Deployment

- Nginx already uses `$uri` rather than query strings in its access log and has `proxy_read_timeout 3600s` (`deploy/nginx/templates/default.conf.template:6-12`, `:39-50`). This reduces ticket/path-query log risk but must remain an explicit contract.
- The Go server's global `WriteTimeout` is 30 seconds (`backend/cmd/server/main.go:284-291`), so long content streams need a deliberate serving strategy rather than relying on the generic handler unchanged.
- CSP currently allows same-origin images and same-origin default media, but blocks object embedding and Blob sources unless changed (`deploy/nginx/templates/default.conf.template:18-25`; `backend/internal/api/router.go:94-102`). A same-origin ticket stream avoids a broad Blob allowance.
- Current `X-Frame-Options: DENY` makes an inline browser PDF iframe unsuitable without a narrowly reviewed header/renderer change. A canvas-based maintained viewer or ticketed new-tab fallback is safer than relaxing framing globally.
- The official image deliberately does not install Restic/Rclone; provider access must continue through the managed node over SSH to preserve current deployment assumptions.

## Technical Decisions To Carry Into Design Review

1. Use a provider-capability abstraction; do not expose executor-specific commands to handlers/UI.
2. Keep metadata catalog and content delivery separate.
3. Use short-lived resource-bound preview tickets for native media/download requests.
4. Keep browser-native preview/download/restore functional in the core all-in-one; add thumbnails, OCR, indexing, Office/PDF conversion, malware scanning, and media transcoding through optional capability-discovered Workers.
5. Treat active formats as text/attachment, never executable inline content.
6. Make Restic materialization cache bounded, optional, and observable.
7. Introduce dedicated asset permissions; do not reuse generic `tasks:read` unchanged.
8. Keep preview failures isolated from backup/verification/restore health.
9. Isolate Worker queues, resources, versions, failures, and derived-artifact retention from both the content gateway and the backup scheduler.
