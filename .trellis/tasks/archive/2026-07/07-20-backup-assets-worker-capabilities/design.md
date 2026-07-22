# Child 11 Backup Asset Worker Capabilities And Enhanced Preview Design

## 0. Planning authority and execution gate

```text
task:                    .trellis/tasks/07-20-backup-assets-worker-capabilities
status:                  in_progress
parent:                  07-12-backup-data-explorer-design (planning tracker)
branch:                  codex/backup-assets-worker-capabilities
base / inspected SHA:    main / be6eebbe50dfd78e071c6d73e9c81493487fb4d5
main / origin/main:      be6eebbe50dfd78e071c6d73e9c81493487fb4d5
delivered program state: 10/15
planning package review: approved by controller
Phase 2 implementation: authorized by user
workflow transition:     completed by controller
task.py start:           completed
product implementation: in_progress
```

This document narrows parent design sections 7-10, 17.2, 18, 20-21 and
parent implement section 12 against merged Child 10. The parent remains a
planning/program tracker and must stay `planning`; the eleven instantiated
Trellis children do not change the delivered program state from 10/15.

The controller has completed technical review, approved this planning package,
and completed the workflow transition; the user has explicitly authorized
Phase 2 and the Child is now `in_progress`.

The focused corrections to the parent plan are:

- Child 11 uses the paired `000067_backup_asset_processing` schema exactly as
  merged. It creates no migration and does not change model storage contracts.
- Production Worker registries are currently empty and materialization is
  deliberately disabled. Child 11 fills those runtime seams; it does not
  replace the Child 10 protocol or grant model.
- Current Derived publication commits an artifact set before calling a Search
  port which opens another transaction. Text/OCR/classification publication
  must instead use one outer GORM transaction and one fence.
- Existing artifact roles and stable database error codes are closed. Child 11
  reuses them and keeps finer tool reasons in bounded typed response DTOs.
- Existing native preview remains the baseline. Enhanced processing API and UI
  are separate lazy boundaries because the startup budget has only about
  1.91 KiB JavaScript and 0.79 KiB CSS of current headroom.
- Worker packaging is an optional, unpublished profile. The official all-in-one
  image, port 10761, image selector, release source of truth, and Docker Hub
  publication contract do not change.
- The four public documentation paths assigned by parent implement section 12
  are restored, and `backend/README_backend.md` is added for the current router
  documentation-freshness contract. They document default-off local operation,
  security, rollback and no-Worker degradation only; they do not create a GA,
  public-image or publication contract.

Current-main source evidence and exact line references are recorded in
`research/current-main-evidence.md`.

## 1. Goals and non-negotiable invariants

### 1.1 Goals

1. Turn the protocol-only `asset-worker` into a real, closed-capability Worker
   for image, text/OCR, document, malware, media and archive processing.
2. Keep every parser and external tool behind a common bounded runner and a
   container sandbox with no network, credentials, Provider access or durable
   plaintext staging.
3. Provide signed, atomic, generic bundle updates through a separately
   identified updater without giving parser jobs egress.
4. Make bundle and policy changes invalidate only affected derivatives and
   converge through a quota-controlled, starvation-free backfill.
5. Publish Derived and Child 7 Search state atomically under the Child 10
   attempt/RecoveryPoint fence.
6. Surface honest preview, coverage, scan and updater state without exposing
   private locators, tool output, credentials or internal identities.
7. Preserve all Core browsing and recovery workflows when the Worker profile is
   absent, disabled, incompatible, unhealthy or rolled back.

### 1.2 Invariants

- Worker input comes only from an attempt-bound Child 10 Input grant and output
  returns only through its matching Sink grant. No capability opens a Provider,
  Repository, database, host path or arbitrary URL.
- Provider bytes are immutable. A capability receives a stream or a private
  tmpfs copy and can never write back through the Input session.
- Requests select only a capability and a versioned closed profile. They cannot
  supply an executable, argv, codec, font, model path, environment variable,
  file path, URL, archive member path or tool configuration.
- Every source and output limit is enforced at three levels: the Core descriptor
  and grants, the Worker capability/profile, and the tool/container resource
  boundary. The smallest applicable ceiling wins.
- Parser processes have no egress or DNS. Updater credentials and networking
  are never inherited, mounted or proxied into a Worker job.
- Diagnostics are stable category/code plus bounded sanitized parameters. Raw
  stdout/stderr, tool command lines, temp paths, source names and content are
  never persisted or returned.
- A malware positive is a successful `malware.scan` result, not a processing
  failure. `not_scanned`, `no_finding`, `finding` and `stale` remain distinct.
- `unknown` and `secret` sensitivity fail closed. UI state and Worker output
  cannot weaken RBAC, ownership, step-up, malware or secret enforcement.
- Only the current job, current attempt and current RecoveryPoint fence may
  publish. A stale attempt writes neither Derived references nor Search state.
- `backup_assets.enabled`, local Worker, remote Worker and updater remain false
  by default through Child 14. Command Provider remains typed unsupported.
- A missing Worker does not enqueue doomed background work, change backup
  health, create alerts, or disable Catalog/Search/Content/workspace/native
  preview/download/recovery.

## 2. Component topology and dependency direction

```text
                           public /api/v1
                                  |
             +--------------------+----------------------+
             |                                           |
   exact AssetRef preview API                  Admin processing API
   RBAC + ownership + audit                    Admin + feature gate
             |                                           |
             +--------------------+----------------------+
                                  |
                    Processing Capability Service
                eligibility / interests / coverage / policy
                       |                         |
                       v                         v
              Child 10 Coordinator       Bundle Activation Service
                       |                         ^
       attempt-bound Input/Sink grants          | bounded receipts
                       |                         |
                       v                         |
              parser Worker process        updater process
             no network, read-only          separate UID/socket
                       |                         |
              closed capability tools       fixed inbox/egress
                       |                         |
                       +-----------+-------------+
                                   |
                        immutable bundle store
                         updater rw / Worker ro

      prepared output -> one outer DB transaction + same fence
                                   |
                 +-----------------+------------------+
                 |                                    |
          Derived set/reference              Child 7 Search postings,
          and job completion                 excerpt and field coverage
                 |
        Derived representation resolver
                 |
      existing Content Broker ticket/cookie
                 |
       /api/v1/asset-content/:deliveryId
```

Dependency direction remains acyclic:

```text
processing/capabilityspec -> no parent-package imports
processing/capabilities  -> capabilityspec only
processing/updater       -> processing metadata contracts only
processing              -> capabilityspec + content attempt/derived delivery ports
content                 -> backupasset/model contracts, never processing
search                  -> backupasset/model/lease contracts, never processing
runtime                 -> processing/content/search/updater composition
api                     -> narrow runtime service interfaces
frontend                -> typed API mapper/domain boundary
```

The updater has no database handle. Core records only a validated activation
receipt through the existing `BackupAssetUpdaterMetadata` shape. The Worker has
no updater control socket. `backupasset/runtime.Runtime` remains the only Core
composition root; neither `cmd/server` nor handlers construct duplicate queues,
stores, keyrings or Provider registries.

The shared `processing/capabilityspec` package contains only closed
profile identities, typed input/output envelopes and validation helpers. It
imports no parent `processing` package, so the real implementations
in `processing/capabilities` cannot create a Go import cycle. The
Worker command assembles those implementations and passes them to the existing
validated WorkerCapabilitySet factory; Core uses the same spec package to build
the advertisement registry.

## 3. Closed capability and profile contract

### 3.1 Production capability set

The production registry advertises only the following identities. Schema,
pipeline and profile strings are constants compiled into Core and Worker. A
bundle fingerprint is composed into the pipeline fingerprint; the handshake is
rejected if Core and Worker do not advertise the same tuple.

| Capability | Profile | Accepted input | Output role / media | Hard profile ceiling |
|---|---|---|---|---|
| `image.thumbnail` | `raster_thumbnail_v1` | JPEG, PNG, WebP, GIF, TIFF, BMP | `thumbnail` / PNG or WebP; metadata JSON | 256 MiB input, 50 MP decoded total, 8 frames inspected, one 4096x4096 image, 8 MiB output, 90 s |
| `text.extract` | `bounded_text_v1` | UTF-8/UTF-16/UTF-32 text and allowlisted plain-text MIME | `content` / UTF-8 text; metadata JSON | 64 MiB input, 4 MiB or 1,000,000 runes output, 100,000 lines, 60 s |
| `image.ocr` | `tesseract_text_v1` | safe raster pages only | `ocr` / UTF-8 text; metadata JSON | 128 MiB input, 8 pages, 20 MP/page, 8 MiB output, 5 min |
| `document.convert` | `static_pages_v1` | PDF, DOCX/XLSX/PPTX, ODT/ODS/ODP | up to 30 `thumbnail` PNG pages, one `content` PDF-or-text artifact and one metadata artifact | 256 MiB input, 100 pages inspected, 30 pages rendered, 50 MP/page, 64 MiB total output, 10 min |
| `malware.scan` | `signature_scan_v1` | any regular file within policy | `metadata` / canonical finding JSON | 1 GiB input, one result, 64 KiB output, 10 min |
| `media.probe` | `media_probe_v1` | allowlisted audio/video containers | `metadata` / canonical probe JSON | 512 MiB input, 32 streams, 2 h declared duration, 1 MiB output, 2 min |
| `media.transcode` | `browser_preview_v1` | allowlisted audio/video after successful probe | `content` / MP4-WebM-or-audio allowlist; thumbnail poster; metadata | 2 GiB input, first 30 min only, 3840x2160 input, 1080p output, 512 MiB output, 30 min |
| `archive.inspect` | `archive_index_v1` | ZIP, TAR, tar+gzip/xz/zstd | `metadata` / bounded opaque index JSON | 2 GiB input, 16 path components, 100,000 entries, 8 GiB declared/streamed expansion, 100:1 ratio, 16 MiB index, 10 min |
| `archive.extract_entry` | `archive_member_v1` | an already accepted archive plus opaque member ordinal | `content` / sniffed closed MIME or octet-stream; metadata | one regular member, 256 MiB output, same archive limits, 10 min |
| `secret.classify` | `bounded_secret_v1` | complete authorized text/OCR only | `metadata` / canonical classification JSON | disabled by default; 16 MiB input, 256 KiB output, 60 s |

These are ceilings, not defaults a caller can increase. Core may lower them by
settings or policy. `ProtocolCapabilityLimits` continues to carry the common
ceilings. Capability-specific closed limits such as archive entry count, ratio,
stream count and decoded frames live in a versioned canonical profile contract
validated by both sides and included in `PipelineFingerprint` and the work key;
the existing `limits_canonical` column stores the canonical advertisement, so
no schema change is required.

The current Sink default permits 32 artifacts. `document.convert` therefore
renders at most 30 page products, plus exactly one canonical metadata artifact
and at most one content artifact, even when it inspects a 100-page input. PDF
input may use that content slot for bounded extracted text; Office/ODF input
may use it for the sanitized PDF representation. Longer documents publish
`partial` coverage with exact page counts. Increasing a deployment setting
cannot make this profile exceed 32 artifacts; that requires a new profile/
fingerprint.

### 3.2 Artifact mapping without schema expansion

Child 11 reuses the existing roles:

| Meaning | Existing role | Validation rule |
|---|---|---|
| Extracted text | `content` | UTF-8 `text/plain`, bounded canonical text metadata |
| OCR text | `ocr` | UTF-8 `text/plain`, bounded page coverage |
| Static image/poster/page | `thumbnail` | PNG/JPEG/WebP only, Core decodes dimensions before commit |
| Sanitized document/media/member representation | `content` | closed PDF/MP4/WebM/audio/octet-stream MIME allowlist selected by capability/profile |
| Probe, scan, archive index, classification | `metadata` | capability-specific strict canonical JSON schema |
| Test/no-output protocol path | `noop` | unchanged; never advertised as a user capability |

`validArtifactMedia` becomes capability/profile-aware instead of globally
accepting arbitrary binary `content`. It still rejects HTML, SVG, executable
MIME, unknown role/profile combinations and unexpected artifact counts. The
coverage envelope stays `schema_version=1,kind=all` at the Child 10 storage
boundary; detailed bytes/pages/time/member coverage is inside the validated
capability metadata DTO and cannot change the 000067 CHECK contract.

### 3.3 Pinned tool families

The Worker image carries a reviewed, multi-architecture tool set rather than a
general plugin host:

| Profile family | Tool family | Isolation rule |
|---|---|---|
| raster thumbnail | libvips loader/thumbnail profile | only raster MIME; no SVG loader, no external resource or profile write |
| bounded text | Go streaming decoder | no external executable or locale/model lookup |
| OCR | Tesseract with an allowlisted read-only language bundle | fixed language/model IDs; no model download |
| PDF/document | Poppler `pdftocairo`/`pdftotext`; LibreOffice headless for Office/ODF | private profile, macro/link/extension disablement, no network |
| malware | ClamAV `clamscan` with a verified signature bundle | metadata-only result; no quarantine path or sample output |
| media | ffprobe/ffmpeg with file/pipe protocol and codec allowlists | no network/device/concat protocols; fixed output profile |
| archive | bounded Go ZIP/TAR readers plus fixed gzip/xz/zstd decompressor profiles | no recursive extraction, path/type checks before member output |

Executable paths, package versions, policy files, license/SBOM evidence and
toolchain digest are fixed in the image build and included in the pipeline
fingerprint. A missing binary or bundle removes the capability from the
advertisement. The plan intentionally does not add a 7z parser or a generic
plugin loader; unsupported formats use the native fallback.

### 3.4 Stable outcome mapping

No processing error is added. Tool outcomes map to the existing closed codes:

| Tool result | Persisted processing code | Bounded API reason examples |
|---|---|---|
| MIME/profile unsupported | `unsupported_format` | `mime_not_allowlisted`, `active_content_rejected` |
| encrypted archive | `encrypted_archive` | `archive_encrypted` |
| input/page/pixel/duration/entry/ratio ceiling | `input_too_large` | `page_limit`, `pixel_limit`, `archive_ratio_limit` |
| secure tmpfs unavailable | `materialization_disabled` | `secure_workspace_unavailable` |
| tool deadline | `timeout` | `tool_deadline` |
| cgroup/tmpfs/concurrency pressure | `quota_busy` | `worker_memory_busy`, `tmpfs_busy` |
| process exits or is killed unexpectedly | `worker_crash` | `tool_exit`, `process_tree_killed` |
| MIME/digest/schema/output mismatch | `invalid_output` or `digest_mismatch` | `output_schema_invalid`, `output_mime_mismatch` |
| seccomp/path/process policy breach | `sandbox_violation` | `forbidden_syscall`, `workspace_escape` |
| observed socket/DNS/URL access | `network_violation` | `network_attempt` |

Reason values are closed enums with at most four non-sensitive scalar
parameters and a 256-byte encoded ceiling. They never contain filenames,
paths, argv, signatures containing source content, process IDs or raw messages.
Contract/security outcomes quarantine the Worker under existing Child 10 rules;
permanent input outcomes do not. A malware finding uses a successful metadata
artifact and no processing error.

## 4. Shared runner and job workspace

### 4.1 Typed command construction

Each capability implements a typed profile which returns an internal
`ToolInvocation`; there is no generic command API:

```go
type ToolInvocation struct {
    ExecutableID ToolExecutableID
    ArgProfile   ToolArgProfile
    InputMode    ToolInputMode
    OutputSpec   ClosedOutputSpec
    Limits       ToolLimits
}
```

`ExecutableID` resolves to a compile-time absolute path in the image. Args are
built from closed enums and bounded numeric fields; user data is never placed
in an option position. The runner calls `exec.CommandContext` directly, never a
shell, `PATH` lookup, response file or plugin loader. Environment is an exact
allowlist: UTC, fixed locale, private HOME/cache/config directories and the
minimum tool-specific variables. Proxy variables, inherited environment and
dynamic library/plugin paths are cleared.

The supervisor never execs a parser tool directly. It invokes the fixed
`asset-tool-sandbox` helper, which closes every non-stdio file
descriptor, disables dumpability, applies no-new-privileges, rlimits, a
tool-profile seccomp filter
that denies all socket/connect and privileged syscalls, and a Landlock
filesystem ruleset exposing only the selected read-only input/binary/library/
bundle roots plus the job's writable output directory. The Core UDS path is
neither visible nor reachable to the child. The helper then execs exactly the
typed tool path; its restrictions persist through descendants. If the Linux
kernel cannot enforce the required Landlock/seccomp profile, production
capabilities are not advertised. Non-Linux builds keep a fail-closed test stub.

The runtime image pins tool packages/image digest. At startup it hashes the
binary/version, static policy files, codecs/fonts/models and active bundles to
build the advertised pipeline fingerprint. A missing or mismatched component
removes that capability from advertisement; it does not advertise degraded
output under an old fingerprint.

### 4.2 Input and materialization

1. Core validates exact AssetRef, active Catalog/source binding, policy,
   capability eligibility and grant limits.
2. Worker activates the one-use Input grant and reads a bounded prefix for
   media sniffing. Declared and sniffed types must match a profile rule.
3. Streaming profiles feed a pipe with backpressure and cumulative byte
   accounting. Tools receive only `pipe:0`/stdin where supported.
4. Path-only profiles transition to `materializing`, create a job-exclusive
   tmpfs directory, copy through the Input session while hashing/counting, then
   open the file read-only. No normal-disk fallback exists.
5. The workspace contains generated opaque names only. Original/display names,
   Provider paths and locators never enter the Worker filesystem or argv.
6. Before upload, Worker re-sniffs every output, verifies count/size and hashes
   it. Core repeats those checks and schema validation before publication.

The tmpfs mount must be `noexec,nosuid,nodev`, owned by the Worker UID, mode
0700, and exclusive to one job. Its byte/inode ceiling is no larger than the
attempt and container limits. Lack of a verified mount returns
`materialization_disabled`. Swap must be disabled or encrypted as an operator
prerequisite; the Worker cannot silently claim swap safety.

### 4.3 Cancellation, timeouts and diagnostics

Every tool starts in a new process group. Context cancellation, heartbeat
failure, grant revocation, fence loss, timeout or shutdown sends TERM to the
group, waits a short fixed grace period, then sends KILL and waits for all
children. A subreaper/reaper collects descendants; a job cannot complete while
its process tree remains alive. Sink activation/upload is skipped after fence
loss even if a tool exits successfully.

Stdout and stderr use independent fixed-size ring buffers with a small prefix
and suffix allowance; the default retained budget is 16 KiB each in memory.
Tool adapters parse only allowlisted machine-readable fields, discard the raw
buffers, and emit a stable result code. Logs contain capability, profile,
closed outcome category, duration bucket and byte/count buckets only. Workspace
cleanup runs on success, failure, cancellation and startup orphan scan.

## 5. Capability-specific safety contracts

### 5.1 Image and active content

- Decode orientation and frames under the pixel/frame ceilings; never trust
  dimensions from headers alone. Strip EXIF/XMP/ICC/comments unless a bounded
  allowlisted metadata field is explicitly returned.
- Every advertised JPEG/PNG/WebP/GIF/TIFF/BMP thumbnail MIME enters the same
  closed libvips runner, and WebP/TIFF OCR is first normalized through that
  bounded raster path before Tesseract. Declared MIME, decoded format,
  pixels/frames and tool output are revalidated at both boundaries; no
  advertised pair may deterministically fall through to `unsupported`.
- Output is re-encoded static raster. Animated output, embedded profiles,
  attachments, scripts and metadata chunks are rejected.
- `text/html`, `application/xhtml+xml` and `image/svg+xml` are not accepted by
  `raster_thumbnail_v1`. They remain escaped-text/download/recovery fallbacks;
  they are never active inline on the main origin.
- Truncated and malformed image fixtures must fail closed before Sink commit.

### 5.2 Text and OCR

- Text decoding supports only explicit BOM/allowlisted encodings. Invalid
  sequences are either rejected or replaced under the profile's fixed policy;
  the result reports `complete` or `partial` plus byte/rune/line coverage.
- NUL-heavy or binary-sniffed input is not treated as text. Lines and runes are
  counted during streaming, so a single unbounded line cannot bypass limits.
- OCR consumes safe raster output or a profile-approved image, never an active
  document. Tesseract languages/models are an allowlisted read-only bundle;
  jobs cannot request downloads or arbitrary language files.
- Core performs canonical Unicode normalization/tokenization and secret policy
  checks before preparing a Search projection. Worker text is untrusted data.

### 5.3 PDF and Office/ODF

- PDF pages are rasterized in the sandbox with JavaScript, launch actions,
  attachments and external resources disabled. Browser delivery receives only
  validated derived raster/PDF, never an unchecked conversion intermediate.
- Office/ODF conversion runs headless with a fresh private user profile, macro
  execution disabled, no extensions, no template lookup, no printer access and
  no network namespace. A bounded ZIP/XML preflight requires the canonical
  package/mimetype contract and rejects encryption, duplicate/traversal/non-
  regular members, bombs, macro/script metadata and external relationships.
  Macro-, script- or external-link-bearing OOXML/ODF returns a closed
  unsupported/invalid result before any LibreOffice plan exists; target values,
  paths and original XML are never returned even as warnings.
- Page count and decoded pixels are rechecked after conversion. More than 30
  rendered pages yields explicit partial coverage; more than 100 inspected
  pages is rejected by this profile.
- A malformed PDF, macro-enabled document or external-link document can never
  trigger execution or egress. Failure preserves metadata/download/recovery.

### 5.4 Malware and secret findings

`malware.scan` returns canonical metadata:

```text
schema_version, engine_family, signature_bundle_fingerprint,
result(not_scanned|no_finding|finding|stale), finding_category,
scanned_bytes, completeness, scanned_at
```

It never returns raw engine output, source path, full signature text, sample
bytes or remediation commands. A positive test signature produces
`result=finding`, job `succeeded`, and a server-enforced gate. Scan engine error
or timeout is not `no_finding`.

Core path/name/MIME/bounded-content classification remains authoritative.
Optional `secret.classify` may only strengthen a result for complete authorized
text/OCR under the same source and policy revision. It is disabled unless an
administrator explicitly enables its closed policy. `unknown` and `secret`
remain excluded from content/OCR matches, counts, suggestions and excerpts
without the required proof.

Worker physical support and Core product-policy enablement are distinct. The
closed Worker registry contains the optional profile and executes only complete
authorized plaintext up to 16 MiB, while API inventory, admission, continuation
and Search publication remain gated by `processing_secret_classify=false` by
default. Classification continuation reuses the exact AssetRef/source,
pipeline/security-policy identity; stale attempts or fences cannot update the
Derived evidence or Search classification transaction.

### 5.5 Media

- ffprobe/ffmpeg accept only local pipe/file protocols; concat, crypto, HTTP,
  RTP, device, subtitle-script and dynamic protocol access are disabled.
- Probe JSON is parsed into a new canonical DTO containing only allowlisted
  container, codec, dimensions, duration, bitrate and stream-count fields.
  Tags, chapters, filenames and tool diagnostics are omitted or bounded.
- Transcode uses a fixed container/codec/bitrate/resolution profile, caps the
  preview at 30 minutes, limits frames and output bytes, strips metadata and
  creates an optional safe poster. Input above 4K or two hours is unsupported
  for the profile rather than implicitly downscanned without accounting.
- Malformed headers, duration overflow and decompression/resource exhaustion
  fail closed. Native browser Range preview remains available when policy
  permits, independently of transcode success.

### 5.6 Archive index and member extraction

- Inspection never extracts an archive tree. It streams headers and produces a
  bounded tree of opaque member IDs, opaque parent IDs, sanitized display names,
  type, declared size and closed warnings. Raw archive paths are not returned.
- Absolute paths, `..`, empty/confusable components, NUL/control characters,
  symlink/hardlink, device, FIFO/socket, duplicate/colliding normalized paths,
  encrypted entries and unsupported filters make the profile fail closed.
- Nested archive files are reported as regular opaque members but are not
  recursively opened. The depth limit applies to path components; recursive
  processing requires a separately authorized future job and is out of scope.
- Entry count, declared and actual expanded bytes, per-member bytes and
  compression ratio are checked incrementally. Missing/lying sizes cannot evade
  the streamed byte ceiling.
- gzip/xz/zstd input goes only through fixed absolute pipe-only decompressor
  profiles in the sandbox. A concurrent bounded container validator proves one
  complete stream/frame with no truncation, trailing bytes or concatenated
  empty stream, while the decompressed bytes flow directly into the same TAR
  inspect/extract consumer. No expanded archive is accumulated in memory or an
  ordinary temporary file.
- Member extraction accepts only an opaque member ID resolved by Core to the
  previously committed index ordinal/digest. No client/Worker request contains
  a path. Only one regular member is streamed to Sink; nothing is written to a
  host directory.
- Child 11 may deliver that member only through preview authorization and a
  closed safe renderer after MIME/malware/secret checks. Unknown binary output
  remains an internal derivative with an unsupported preview result; it is not
  a new download/export surface. Durable member retrieval remains Child 12.
- Inspect and member operations use existing `archive_inspect` and
  `archive_member` typed audit actions with exact AssetRef and opaque member ID.

## 6. Generic signed updater contract

### 6.1 Identity and trust separation

The updater is a separate process with a separate UID, UDS peer identity,
socket and writable bundle volume. It can run from the Worker image using a
different entrypoint, but it never shares a process, identity, network
namespace or writable mount with parser Workers. Core accepts updater receipts
only from the authenticated updater socket. Worker protocol headers cannot
impersonate it.

The updater transport has its own `UpdaterTransportIdentity` and dedicated
listener; it does not reuse `WorkerTransportIdentity`, the Worker socket or a
caller-controlled header. On Linux, the Core listener validates the fixed
socket owner/mode and `SO_PEERCRED` UID against the configured updater UID
before placing that identity in request context. Other platforms fail closed.
The parser Worker does not mount this socket, and the updater does not mount
the parser Worker socket. Wrong UID, unsafe socket mode, missing peer identity
or a request on the public server is rejected before receipt decoding.

Default mode is offline-only. Optional online mode clears inherited proxy
environment and accepts only the deployment's fixed proxy socket/authority,
allows TLS only to exact configured `scheme=https,host,port` origins, resolves
and connects through the updater network policy, revalidates every redirect or
rejects redirects, and caps metadata/bundle bytes and time. Parser containers
remain `network_mode:none` with DNS absent. Any online credential is mounted as
an updater-only secret file and held only in memory; it never enters settings,
DB, API DTOs, logs, metrics, command lines or bundle metadata.

Application allowlisting is not treated as a container egress firewall.
Online mode is available only when the deployment supplies an independently
configured allowlist proxy/firewall endpoint; the updater network can reach
that endpoint but has no direct default route or general DNS. Core passes a
fixed proxy socket/authority, and the updater still validates HTTPS authority,
TLS name, resolved public address and redirect policy. Without this external
enforcement, online mode fails startup and signed offline import remains the
supported path.

Offline import is an updater-local data path, not a browser/Core upload path:

1. An operator places one signed candidate directory out of band into the fixed
   updater-only inbox mount. The API cannot choose a path, URL or filename.
2. Updater scans only the inbox root with `openat`-style no-follow operations,
   rejects symlink/hardlink/device/collision inputs, and applies the same
   1 GiB/15-minute package ceilings before canonical manifest verification.
   It holds directory/file descriptors and requires pre-open/opened/post-copy
   identity, type, size and digest agreement; an operator replacement or
   partially copied candidate fails closed instead of mixing manifest/payload.
3. A verified package is copied into the immutable content-addressed store and
   reported to Core with a bounded metadata receipt. Core creates/coalesces the
   existing 000067 updater-metadata row and returns its random 32-hex ID as the
   one-purpose candidate handle; updater records only the handle-to-digest
   mapping in its private candidate journal.
4. Admin may list the sanitized candidate and confirm activation with a small
   JSON control request. Candidate IDs never reveal or accept an inbox path,
   never enter logs/metrics, and become non-actionable after a terminal
   activation/failure state. No new candidate table or column is required.

The read-only inbox is not the active bundle store. Updater never executes or
loads parser data from it, and parser Workers cannot mount it. Cleanup of the
operator-owned inbox file is an out-of-band operator action after the verified
candidate is safely stored.

### 6.2 Canonical manifest

The generic manifest uses deterministic UTF-8 JSON with duplicate/unknown
members rejected. It contains only:

```text
schema_version=1
source_kind, source_id, version, created_at, expires_at
capabilities[]: capability, schema, profiles[], tool/model/data revisions
files[]: relative allowlisted path, mode(read-only regular), size, sha256
bundle_sha256, signing_key_id, signature_algorithm=ed25519, signature
```

Offline candidates have one closed package envelope: a single directory with
exactly `manifest.json` and `bundle.tar`. `bundle.tar` is an uncompressed,
canonical tar payload with sorted regular-file entries only; PAX/GNU extensions,
sparse files, links, devices, duplicate names, extra entries and trailing bytes
are rejected. `bundle_sha256` covers the exact `bundle.tar` bytes, not the
manifest that contains the field, so the digest contract is not self-referential.
The signed manifest covers its domain-separated canonical form without the
`signature` member and binds that payload digest plus the exact file list.
Online mode obtains the same manifest and payload from its fixed configured
source contract; the payload format does not vary by transport.

Paths must be normalized relative paths under a bundle root; no symlink,
hardlink, device, executable-by-default, duplicate or case-fold collision is
allowed. The signature covers a domain-separated canonical manifest without
the signature field. Trusted Ed25519 public keys come from an administrator
managed read-only file; key fingerprint and rotation policy are closed. Every
tar member and the exact payload tar are SHA-256 verified before storage. The
derived bundle fingerprint is the domain-separated digest of schema,
capability/profile/tool/model/data revisions, payload digest and ordered file
digests; signing-key rotation alone does not silently change parser content.

Core persists only the existing metadata fields: source kind/opaque source ID,
version, manifest digest, signing key fingerprint, bundle fingerprint, state,
closed updater failure code and UTC timestamps. It stores no URL, credential,
manifest body, bundle bytes/path, secret, raw response or diagnostic.

### 6.3 Content-addressed store and activation

Verified files are written under a digest-derived directory on the updater
volume, using `openat`-style no-follow operations, fixed modes and a private
staging directory. The updater fsyncs files and directories, then makes the
immutable digest directory visible. Worker mounts the store read-only and sees
only an `active` pointer plus immutable digest roots.

Activation is a crash-recoverable handshake:

1. Core records the updater's bounded verified-candidate receipt as `verified`
   under the existing random 32-hex updater metadata ID and sends that
   one-purpose candidate handle plus expected old/new fingerprints to
   the updater. Core has never received the bundle bytes or inbox path.
2. Updater writes/fsyncs a small activation journal, atomically renames the
   `active` pointer, and returns a bounded receipt while retaining the old
   fingerprint for rollback.
3. Core opens one bounded DB transaction, revalidates the candidate/old
   fingerprint, marks metadata active/superseded, increments the fixed
   content/OCR pipeline revisions for affected fields, and commits. This makes
   old fingerprints logically stale immediately without scanning every set.
4. Core acknowledges the committed activation. Only then may Workers advertise
   and accept jobs using the new fingerprint.
5. If Core rejects or times out before commit, updater atomically restores the
   old pointer. If either side crashes, reconciliation compares the journal,
   active pointer and DB metadata: a committed DB fingerprint is completed;
   an uncommitted swap is rolled back. It never guesses from directory mtime.

Signature/digest/policy failure maps to the existing updater codes
`invalid_signature`, `unsupported_version`, `policy_rejected` or
`activation_failed`. Failed candidates never replace the active bundle.

## 7. Work identity, invalidation and backfill

### 7.1 Pipeline fingerprint

For each capability/profile, Core and Worker compute:

```text
SHA256(
  "xirang.asset.pipeline.v1" ||
  capability || capability_schema || output_profile ||
  canonical_profile_limits || toolchain_fingerprint ||
  ordered_active_bundle_fingerprints || security_policy_revision
)
```

The result fills Child 10 `PipelineFingerprint`; the existing work key already
also includes source/entry fingerprint, capability/schema, output profile,
security policy revision and all output-affecting parameters. Changing a tool,
model, font, codec, signature database, profile, policy or relevant bundle
therefore cannot reuse an old work item accidentally.

Strong reuse requires `fingerprint_strength=strong`, an identical source/entry
digest and the complete work identity above. The physical encrypted blob may be
shared, but each RecoveryPoint receives its own authorization/lifecycle
reference and fenced Search publication. Weak/none fingerprints are reusable
only within the exact AssetRef and active Catalog/source binding; path, size and
mtime never permit cross-point reuse.

### 7.2 Targeted invalidation

The activation candidate declares the exact capabilities/profiles it changes.
An activation must not update an unbounded number of rows. The bounded
activation transaction verifies the receipt, flips updater metadata and
increments only the fixed current pipeline revision for an affected Search
field (`content` and/or `ocr`). Search service receives
those two revisions through a narrow runtime source and treats any field row
with a different `pipeline_revision` as unavailable/partial before it
reads postings, counts, suggestions or excerpts. Derived delivery likewise
compares the artifact set's job fingerprint with the active capability registry
before returning bytes. Thus old output becomes logically stale at commit even
though physical reconciliation is batched.

After commit, an idempotent controller pages artifact set -> job/work descriptor
and Catalog rows by stable ID. Each bounded batch:

1. marks only affected old-fingerprint sets `stale` while preserving
   encrypted bytes for the explicit stale fallback policy;
2. revokes content/OCR postings, excerpts and field coverage together with that
   Derived state change through the transaction-aware port;
3. supersedes old-fingerprint queued/running attempts; publication independently
   rechecks the active fingerprint so a not-yet-visited attempt still cannot
   commit;
4. creates/coalesces new descriptors and background interests using the new
   fingerprint; and
5. records low-cardinality counters without path/name/internal identity labels.

The controller derives remaining work from active updater metadata, job
descriptors, set state and current Catalog; it needs no progress table and can
resume after a crash. A failed batch rolls back and retries without reopening
stale Search output. Unaffected capabilities and valid artifacts remain active.

### 7.3 Backfill scheduling

No new priority enum/table is needed. The scheduler maps work into existing
`interactive/background` plus `effective_priority`:

| Rank | Work | Class | Ordering intent |
|---:|---|---|---|
| 0 | latest retained point per producing lineage | `background` | highest global effective score; new latest before old latest |
| 1 | active user preview/member request | `interactive` | reserved interactive slots, oldest request first within score |
| 2 | recent retained history | `background` | captured time descending within configured recent window |
| 3 | older retained history | `background` | low score, captured time descending, starvation aging |

The closed score bands are `latest=900..1000`, `interactive=700..899`,
`recent=400..699`, and `history=1..399`; values remain inside Child 10's
existing 0..1000 contract. Queue sorting compares `effective_priority` before
priority class, then enqueue time and opaque ID. Priority class continues to
control slot admission: background work cannot consume the interactive reserve,
and a pull skips a higher-ranked candidate whose slot class is currently full
instead of blocking an eligible reserved-slot candidate. History aging is
capped at 399, so it converges without crossing the recent/interactive/latest
bands. Backfill pause excludes background candidates but never blocks an
explicit interactive preview request.

The backfill controller pages Catalog metadata; it does not scan Provider bytes
to discover work. It skips ineligible MIME/size/policy, missing capabilities,
offline points and already equivalent work. Absence of a matching Worker records
`unsupported/not_deployed` coverage but creates no failed job.

Dynamic settings expose background-backfill pause, batch size, jobs/hour, bytes/hour,
Provider/capability concurrency and maximum backlog age. Interactive reserve is
never borrowed by backfill. A bounded aging term guarantees old eligible work
eventually advances when unpaused without outranking latest, interactive or
recent work. Backup, verification and recovery admission remain outside this Worker
pool and cannot be consumed by backfill.

## 8. Atomic Derived and Search publication

### 8.1 Current gap

Child 10 `publishManifestTx` creates Derived rows and leaves a projection-required
job in `validating`; it then calls `DerivedProjectionPort.Publish`, and the
runtime Search adapter currently fails closed. Child 7
`PublishContentProjection` opens its own transaction. Simply enabling both
would leave a visible Derived set if Search publication fails.

### 8.2 Transaction-aware port

Child 7 keeps its public wrapper for existing callers but adds a narrow
transaction-aware contract:

```go
type ContentIndexIngestTx interface {
    PrepareContentProjection(context.Context, ContentProjectionInput) (PreparedContentProjection, error)
    PublishContentProjectionTx(context.Context, *gorm.DB, PreparedContentProjection) error
    RevokeContentProjectionTx(context.Context, *gorm.DB, RevokeProjection) error
}
```

Preparation performs bounded normalization, tokenization/HMAC material
construction and canonical DTO checks outside the write transaction. The
prepared value is private, immutable and source/fence/revision-bound. The Tx
methods never call `Transaction` themselves and reject nil/nonmatching DB
handles. Existing `PublishContentProjection`/`RevokeContentProjection` become
thin wrappers that prepare and open one transaction for compatibility.

The processing package does not import Search. Its
`DerivedProjectionPort` returns a short-lived prepared object with a
`PublishTx(*gorm.DB)` method; `runtimeDerivedProjectionPort`
is the concrete adapter holding Search's prepared projection. This keeps the
current package direction while allowing Artifact Sink to invoke Search inside
its outer transaction.

### 8.3 Publication sequence

For a projection-bearing manifest:

1. Core validates and decrypts/streams the staged text/OCR/metadata into bounded
   prepared artifacts; it re-sniffs MIME and parses capability schemas.
2. Core prepares Search postings/classification/coverage without making state
   visible.
3. Artifact Sink opens one outer DB transaction and locks job, attempt, grant,
   RecoveryPoint lease/fence, point, Catalog/Search generations and current
   policy/source as required by existing contracts.
4. It creates/attaches Derived blob references, artifact set and artifacts.
5. It calls `PublishContentProjectionTx` with the same `tx` and exact fence to
   replace HMAC postings, sensitivity revision, excerpt reference and field
   coverage.
6. It marks projection published, attempt/job succeeded, closes the Sink grant
   and releases the lease in that transaction.
7. Commit makes both planes visible together. Any error rolls back all DB
   changes; staged ciphertext is invisible and later discarded as an orphan.

Revocation/invalidation performs the inverse Search removal and Derived state/
reference update inside one outer transaction before key/blob destruction.
Any projection-bearing revocation first acquires a `processing_job`
RecoveryPoint lease/fence. `DerivedLifecycle` may revoke a
projection-free binary set without one, but it fails closed for a published
Search projection unless the fenced revocation form is used. Bundle/policy
invalidation and key-loss/expiry reconciliation use that form; key destruction
cannot advance until all published projections are transactionally removed.
Lock order is fixed: processing job -> attempt/grant -> RecoveryPoint lease ->
point -> Catalog generation -> Search generation/document/field -> Derived set/
reference/blob. SQLite/PostgreSQL conflict retry is bounded and revalidates the
fence each time.

Tests inject failure before/after every write, stale attempt/fence, duplicate
commit, Core crash and bundle activation races. No state may expose an active
searchable field with unavailable Derived evidence, or an active Derived
content/OCR set with an unpublished required projection.

## 9. Derived delivery, API and server-side gates

### 9.1 Derived representation resolver

The existing Content Broker remains the only browser byte plane. A narrow
`DerivedRepresentationResolver` selects an active, policy-compatible artifact
for exact AssetRef, renderer/profile, source fingerprint and security policy.
It rechecks ownership, current Catalog/source, artifact lifecycle, scan result,
sensitivity proof and Derived key availability before ticket issue and again on
read. It returns a bounded `SourceReader` over decrypted Derived chunks; it does
not expose a blob ID, storage locator or direct file URL.

Derived tickets keep the current same-origin cookie and
`/api/v1/asset-content/:deliveryId` route. They inherit TTL, Range, concurrency,
byte budget, detach and audit behavior. No public artifact endpoint is added.
If the derived artifact is stale/unavailable/blocked, resolver returns a typed
reason and the original native renderer/download/recovery fallback remains.

### 9.2 Asset processing API

Routes are exact-resource, feature-gated and use current auth/RBAC/ownership:

```text
POST /api/v1/recovery-points/:id/entries/:entryId/preview-jobs
GET  /api/v1/recovery-points/:id/entries/:entryId/preview-jobs/:jobId
POST /api/v1/recovery-points/:id/entries/:entryId/preview-jobs/:jobId/cancel
GET  /api/v1/recovery-points/:id/entries/:entryId/processing
```

Create accepts only a closed requested representation such as `thumbnail`,
`text`, `document_pages`, `media_preview`, `archive_index` and an optional
closed profile. It returns `202` for queued/coalesced work with the public
processing-interest handle in JSON `job_id`, a bounded JSON
`poll_after_seconds`, and `Location` set to the exact poll route for that
handle. The HTTP `Retry-After` header, when also present, only mirrors the same
JSON delay; it is not the successful-response source of truth. An existing
completed representation returns `200`. Poll/cancel must match requester,
exact AssetRef, active interest and ownership. Cancel removes only that
interest; shared work stops only when the last interest is gone.

Responses use stable product states:

```text
native | derived | partial | unsupported | not_deployed | queued | failed
```

They may include capability/profile, complete/partial coverage, freshness,
scan/sensitivity status, safe reason, retryability and allowlisted fallback
actions. They do not include internal coordinator job/attempt/fence/grant/
Worker/blob IDs, pipeline hash input, raw paths, raw diagnostics or internal
failure details. The public `:jobId`/`Location` handle is the existing random
processing-interest ID, never the coordinator job ID. The handler resolves
that interest, verifies its workspace owner key/requester and exact AssetRef,
and only then reads/removes the shared job. An active interest may receive one
terminal response and is atomically marked completed with that response;
already removed interests cannot authorize another poll or cancel.
`WorkResult` may return the created/coalesced interest ID internally, but no
model is serialized and no new key domain or schema is required.

### 9.3 Admin and updater API

Admin-only routes extend the existing summary surface:

```text
GET   /api/v1/admin/backup-asset-processing
GET   /api/v1/admin/backup-asset-processing/capabilities
GET   /api/v1/admin/backup-asset-processing/coverage
GET   /api/v1/admin/backup-asset-processing/updater
GET   /api/v1/admin/backup-asset-processing/updater/offline-candidates
PATCH /api/v1/admin/backup-asset-processing/backfill-policy
POST  /api/v1/admin/backup-asset-processing/updater/offline-candidates/scan
POST  /api/v1/admin/backup-asset-processing/updater/offline-imports
```

Coverage aggregates by opaque Provider/Task reference and closed capability,
not path/name: eligible, completed, partial, queued, failed, unsupported,
not-deployed, stale, backlog age bucket and bounded ETA. Updater DTO exposes
only source kind/opaque ID, semver, digest/fingerprint, signing key fingerprint,
state, stable failure category and timestamps. Policy mutation uses settings
registry validation, optimistic revision and `processing_policy_update` audit.
The candidate list exposes only candidate ID, version, manifest/bundle/key
fingerprints, declared capability/profile changes, verified time and closed
state/reason; never inbox name/path or raw manifest. Scan has an empty body and
a one-byte body limit. Offline import accepts only a strict JSON object with
`candidate_id` (exact 32 lower-hex) and
`expected_active_fingerprint`: it is required and exact 64 lower-hex when an
active bundle exists, and must be JSON null only for bootstrap with no active
bundle. The request is bounded to 4 KiB. It confirms
one already verified inbox candidate; it does not accept bundle bytes,
multipart, URL or server path. Scan/activation are globally serialized,
Admin-only, independently rate limited, and activation is limited to one start
per hour with a 15-minute updater deadline.

Internal updater routes live on a dedicated authenticated UDS and accept only
bounded scan/activation/status receipts. Public and internal control routes use
strict JSON (or an explicitly empty body), duplicate/unknown-field rejection,
response helpers, per-route rate/body limits and sanitized errors. No HTTP
route transports bundle bytes. Swagger generation is updated. No internal
route is exposed through Nginx.

### 9.4 Enforcement and audit

- Asset routes require `backup_assets:preview` and producing-lineage ownership;
  Admin routes require Admin. Feature disabled returns the existing closed
  behavior without revealing asset existence.
- Malware and secret checks run at preview-job creation, Derived ticket issue,
  Derived read and Search result/excerpt release. Client state cannot bypass
  them.
- `preview_job`, `preview_ticket`, `preview_read`,
  `processing_policy_update`, `archive_inspect` and `archive_member` use the
  already registered actions. Audit records exact opaque AssetRef/operation and
  outcome, never raw archive path, content, signature, credential or diagnostic.
- Mutating Admin candidate scan/import actions are independently rate/body
  limited. Archive member preview is separately ownership-, rate- and
  body-limited for its normal preview role; it is not an Admin operation.
  Archive member is a typed preview action in Child 11, not Child 12 batch
  export.

## 10. Frontend state and lazy boundary

### 10.1 API and domain mapping

`backup-asset-processing-api.ts` owns private raw snake_case DTOs, exhaustive
closed-enum mappers and `createBackupAssetProcessingApi()`. It calls central
`request()` only. The lazy processing controller imports that factory
inside its own chunk; eager `client.ts` and
`backup-assets-api.ts` remain unchanged. This preserves the principle
behind the existing Search/Overlay/Content lazy adapters without adding even a
method proxy to the startup client. Shared camelCase product types live in
`types/domain.ts`; there is no `any`, unchecked cast or raw
server object in components.

The controller keys every request by `recoveryPointId:entryId:representation`
plus a monotonically increasing local request revision. Asset selection,
unmount, cancel and route change abort polling and detach tickets. Late poll or
ticket responses for a previous asset are ignored. The central `request()`
returns parsed success JSON, so queued-work polling uses the mapped
`pollAfterSeconds`; `ApiError.retryAfter` is reserved for `429`/`503` error
backoff. Both paths are bounded, pause when hidden/offline and do not turn a
missing Worker into repeated creates.

`BackupsDataWorkspace` remains the auth-context boundary. It passes the current
token, role and `ensureStepUpProof` capability through a narrow runtime prop to
`BackupAssetsWorkspace`; neither lazy panel reads local storage or creates a
second auth state. The workspace exposes the Admin processing trigger only for
the passed Admin role, and the server independently enforces Admin/RBAC. Token,
proof and raw error detail are never rendered, persisted or logged.

### 10.2 UI composition

The existing preview/inspector remains the shell. `asset-preview.tsx`
dynamically loads the processing panel only after the user opens processing
status or requests an enhanced representation. The Admin-only trigger in
`backup-assets-workspace.tsx` independently loads the coverage/updater panel;
`backups-page.data.tsx` supplies the existing auth/step-up facade. It renders:

- current native/derived/partial/queued/not-deployed/unsupported/failed state;
- coverage and freshness without claiming `no_finding` for `not_scanned`;
- scan/sensitivity gates and only server-allowed fallback commands;
- archive index with opaque member selection and typed fetch action; and
- Admin coverage, updater status, pause/quota controls and offline candidate
  scan/activation; there is no browser file-upload control.

No Worker marketing/explanation panel is added. Missing capability is a compact
operational state next to the existing preview action. Native preview stays
immediately usable while the lazy chunk or Worker is unavailable.

All labels exist in zh/en. Buttons use existing shadcn primitives and Lucide
icons. Tabs/menus/controls use correct keyboard semantics, focus returns after
dialogs, queued changes use polite live regions, errors do not steal focus,
motion respects reduced-motion, and the inspector remains usable at 375x812,
768x1024 and 1440x900. Stable preview dimensions prevent status changes from
shifting the workspace.

### 10.3 Bundle gate

Fresh production build evidence must compare startup main JS/CSS and the new
lazy chunk to the Child 9 baseline (`498.09/500 KiB`, `104.21/105 KiB`). Main
budgets may not be raised. A failure requires moving code/assets behind the
lazy boundary or reducing existing bytes; it is not waived by the feature
being disabled. Large tool assets/models live only in the Worker image, never
the web bundle or Core all-in-one image.

## 11. Runtime, Compose and CI contract

### 11.1 Worker container

The optional parser service must run with:

```text
non-root fixed UID/GID
read_only root filesystem
cap_drop: [ALL]
security_opt: [no-new-privileges:true, seccomp=deploy/worker/seccomp.json]
network_mode: none; no DNS configuration
pids_limit and CPU/memory limits
tmpfs job root with noexec,nosuid,nodev,size/inode ceiling
Worker socket mounted rw only as needed to connect
active bundle store mounted read-only
no /data, /backup, /logs, Docker socket or host source mounts
```

The seccomp profile denies mount, ptrace, keyctl, bpf, perf, module, namespace,
raw socket and other unneeded privileged syscalls while retaining the exact
toolchain calls proven by smoke tests. Read-only root plus tmpfs provides only
explicit writable directories. Entry point validates UID, mounts, bundle
read-only state and socket before advertising capabilities; failure drains/exits.

### 11.2 Updater container

Updater is an explicit separate Compose service/profile member with its own
UID, socket, tmpfs, fixed read-only inbox mount and writable bundle volume.
Only updater sees the inbox; Core and parser Workers do not mount it.
Offline-only mode has no network.
The repository Compose profile does not create unrestricted online egress.
Optional online mode requires an operator-supplied allowlist proxy/firewall
network and mounts allowlist/trust/credential secrets only into updater;
parser Worker remains networkless. The Worker and updater do not share a
writable root or credential mount.

### 11.3 Compose compatibility

`docker-compose.yml` gains an explicit optional profile (for example
`asset-worker`) and named runtime volumes/secrets only. Running plain
`docker compose up` still starts the current single `xirang` service using
`linnea7171/xirang:${IMAGE_TAG:-latest}`, port 10761 and the same healthcheck.
The official `deploy/allinone/Dockerfile` is not modified and no new public port
is mapped. Core-only and profile configuration/smoke scripts use isolated
Compose project names, ephemeral non-production secrets and targeted cleanup.

### 11.4 CI without publication

CI adds a Worker job that:

1. builds linux/amd64 and linux/arm64 images from `deploy/worker/Dockerfile`;
2. scans OS packages/image configuration with a pinned scanner;
3. runs unit/malicious-fixture tests and the native-architecture sandbox smoke;
4. validates both core-only and optional-profile Compose configuration; and
5. proves the workflow contains no registry login, push, release tag or publish
   step for the Worker.

QEMU may build the non-native image but does not count as a full behavioral
test. The matrix must at least inspect non-root/read-only entrypoint and image
metadata for both architectures. Any future Docker Hub/GitHub Release
publication requires Child 15 review; Child 11 never modifies
`publish-images.yml` or the public stable-semver contract.

## 12. Settings, metrics and privacy

Dynamic settings extend the registry, with conservative bounds and the
following default posture:

```text
backup_assets.enabled=false                         (unchanged)
backup_assets.worker_local_enabled=false            (unchanged)
backup_assets.worker_remote_enabled=false           (unchanged)
backup_assets.worker_updater_enabled=false           (new, restart)
backup_assets.worker_updater_online_enabled=false    (new, restart)
backup_assets.processing_secret_classify=false       (new, dynamic)
backup_assets.processing_backfill_paused=true        (new, dynamic initially)
```

Additional registry keys cover exact online origins and bounded backfill
batch/jobs/hour/bytes/hour/Provider/capability concurrency. Online origins are
restart-required so the Core contract cannot drift from the independently
configured allowlist proxy/firewall. Credential values are never registry
keys. Updater socket, bundle root, inbox and trust-file paths are fixed
image/Compose mount contracts, not API-visible settings; changing those mounts
requires a restart. Pause/quota changes are dynamic and audited. The global
feature and transport defaults remain false.

The active content/OCR pipeline revisions are internal monotonic publication
state, not dynamic configuration and not registry entries. Their exact reserved
keys are `backup_assets.internal.processing_content_pipeline_revision` and
`backup_assets.internal.processing_ocr_pipeline_revision`. A transaction-aware
internal method on `settings.Service` stores them in the existing
`system_settings` table while keeping them outside `Registry`, `GetAll`, env
resolution and Settings API mutation. Config export unconditionally filters
both keys even when secrets are requested, and config import rejects them as
unknown non-registry settings. Only the Core bundle-activation transaction can
initialize/advance them; Search receives a read-only revision source. This
reuses existing storage without a migration and avoids pretending
operator-editable configuration is publication state.

The revision/fingerprint source bypasses the normal 30-second settings cache
and reads the two reserved rows plus active updater metadata from the database
at each Search projection evaluation, Derived ticket/read gate and Worker
handshake. The query is bounded and validates positive integers/closed active
metadata; missing, malformed or transactionally inconsistent state returns
unavailable. Therefore the activation commit, not a later cache refresh, is the
visibility boundary for stale Search/Derived output.

Metrics labels are limited to capability, profile, priority class, closed state,
error category and coarse resource bucket. Metrics expose counts, duration,
bytes, queue age, coverage and activation outcome; never Provider/Task/Asset,
Worker ID, bundle path, signing key, source ID, file/path, tool message or error
text. Admin APIs may return opaque authorized Provider/Task IDs but logs and
Prometheus labels do not.

Security scans must prove API JSON, logs, metrics, audit and stored updater rows
contain none of: Provider locator, host/tmp path, credential, raw archive path,
raw tool output, updater response/body/secret, UID/PID, certificate material,
grant/session/attempt/fence token or activation secret.

## 13. Degradation, compatibility and rollback

### 13.1 Core-only behavior

When processing is globally disabled, transport is disabled, no Worker is
registered, the profile is absent, bundles are unavailable, Derived keys are
lost or processing fails:

- Catalog and metadata Search continue from Children 6-7;
- Content Broker and native text/image/PDF/audio/video/metadata preview continue
  from Child 8/9 where their existing policy permits;
- workspace selection, download and recovery remain available by existing RBAC;
- content/OCR Search coverage reports partial/unavailable instead of false zero;
- no background failed jobs or alerts are generated merely for not-deployed;
- the UI shows typed fallback state and never claims an asset is missing/safe.

Processing state cannot change RecoveryPoint credibility, backup verification,
immutability, retention or Provider bytes. Command Provider stays
`task_artifact_contract_missing` regardless of Worker availability.

### 13.2 Rollback order

Rollback is additive and data-preserving because no migration exists:

1. pause new preview interests/backfill through dynamic policy;
2. drain parser Workers and revoke active Input/Sink grants;
3. disable updater online mode, then updater and Worker transport;
4. atomically reactivate the last verified compatible bundle if the failure is
   bundle-specific;
5. mark incompatible new-fingerprint derivatives stale/unavailable and revoke
   their Search projections transactionally;
6. remove/disable the optional Compose profile and deploy the previous Core;
7. leave encrypted derivatives for reconciler/GC or rebuild; never touch source
   RecoveryPoints or Provider bytes.

Old Core binaries already tolerate 000067 and see no new schema. New frontend
must tolerate a pre-Child-11 Core by mapping 404/feature-disabled to native-only
state. If atomic projection compatibility cannot be guaranteed during a mixed
version rollout, processing publication stays disabled until Core and Worker
versions match; Core browsing remains available.

## 14. Rejected alternatives and tradeoffs

1. **Install tools in the all-in-one image.** Rejected because it expands the
   official attack surface/image contract and gives parsers access to Core
   mounts and network.
2. **Let each Worker download models/signatures.** Rejected because it breaks
   egress isolation, reproducible fingerprints and credential separation.
3. **Persist new fine-grained errors, roles or coverage columns.** Rejected
   because Child 11 owns no migration and 000067 already provides versioned
   canonical payloads plus stable closed storage codes.
4. **Publish Derived, then retry Search asynchronously.** Rejected because it
   exposes half-published states and lets stale attempts create ghost postings.
5. **Make Search call Derived or vice versa.** Rejected due dependency cycles;
   runtime composes two transaction-aware ports under processing ownership.
6. **Reuse by path/size/mtime or weak fingerprint across points.** Rejected due
   incorrect-content and authorization/lifecycle risks.
7. **Add another priority schema/table.** Rejected because Child 10 priority
   class/effective priority plus settings can express the required ordering.
8. **Expose Derived blob URLs.** Rejected because it bypasses Content Broker
   reauthorization, cookie, Range, audit and lifecycle gates.
9. **Put processing DTOs in eager `backup-assets-api.ts`.** Rejected because the
   startup budget is nearly exhausted and current client already has a lazy API
   pattern.
10. **Treat malware-positive as failed processing.** Rejected because a positive
    finding is valid scan evidence and must not trigger blind retries.
11. **Recursively unpack archives.** Rejected because it creates uncontrolled
    depth/ratio/state and overlaps Child 12 export behavior.
12. **Publish or document a stable Worker image now.** Rejected because public
    release/deploy contracts remain Child 15.
13. **Upload an offline bundle through the browser/Core API.** Rejected because
    central `request()` is a bounded JSON boundary, Core must not proxy a 1 GiB
    supply-chain object, and a byte-upload route would weaken updater identity,
    memory/body limits and the lazy frontend budget. The fixed inbox candidate
    flow preserves the same Admin approval without transporting bytes in Core.

## 15. Risk register and mandatory gates

| Risk | Failure mode | Gate |
|---|---|---|
| Parser RCE/escape | malicious file controls tool or host | no-network container, read-only/drop-all/seccomp, no shell, malicious fixtures, process-tree cleanup smoke |
| Resource bomb | pixels/pages/media/archive exhaust host | triple ceilings, cgroup/tmpfs/PID/time limits, streaming counters, bomb fixtures |
| Half publication | Derived visible without Search or reverse | tx-aware port, one outer transaction/fence, fault injection on SQLite and real PostgreSQL |
| Stale attempt | old Worker writes after lease/bundle/source change | lock/fence/source/policy revalidation before upload and in publication tx |
| Bundle supply-chain | forged/corrupt/mixed bundle activates | Ed25519/SHA-256, canonical manifest, trusted keys, content address, crash activation matrix |
| Credential/diagnostic leak | updater/tool secrets reach DB/API/log | distinct identity/mount, DTO allowlists, bounded diagnostic parser, forbidden-field scans |
| Offline import boundary | browser/Core receives bundle or attacker selects server path | fixed updater-only inbox, no-follow scan, JSON candidate control only, source/upload scans |
| Backfill starvation | history consumes interactive/backup/recovery | separate slots, pause/quotas, rank/aging tests, convergence/admission load test |
| False safety | not-scanned shown as clean | closed scan states and server-side gate tests at search/ticket/read |
| Archive escape | traversal/link/device/bomb/member confusion | no tree extraction, opaque ordinal, path/type checks, adversarial archive matrix |
| Frontend race/leak | previous asset result shown or raw DTO exposed | request revision/AbortController, mapper tests, three viewport/a11y tests |
| Bundle regression | large UI enters startup chunks | dynamic import assertion and unchanged 500/105 KiB budgets |
| Deployment contract drift | core image/port or Worker published | exact diff scan, core-only Compose smoke, workflow no-push scan |

No risk is accepted by silently loosening a limit. An unsupported or exhausted
profile degrades to native preview/download/recovery with a stable reason.

## 16. Scope and approval conclusion

There are no unresolved product questions in this focused plan. The selected
tool/profile ceilings, updater trust model, atomic publication port, API shape,
lazy boundary, optional runtime and rollback are the recommended closed design.

The controller has approved the current technical plan and the user has
authorized its Phase 2 implementation; the workflow transition is complete and
no further product approval is pending. The exact future manifest contains 165
unique paths. The historical 161-to-163 amendment added only
`processing/reconciler.go` and its test as the **atomic Derived/Search
reconciliation correction**: missing/unreadable Derived blob repair revokes
Search inside the same caller transaction and processing-job fence before
Derived mutation. The later 163-to-164 amendment adds only
`backupasset/repository/testutil_test.go` as the **repository foundation settings
fixture synchronization**, keeping the explicit repository snapshot fixture in
lockstep with the 12 new processing/backfill/updater defaults without weakening
production validation. The later 164-to-165 **bootstrap null parser correction**
adds only `backend/internal/api/handlers/backup_content_handler.go`: the shared
duplicate-key walker accepts a legal JSON null value while retaining duplicate,
unknown-field, depth and trailing-token rejection. It preserves the closed
`expected_active_fingerprint: null` first-activation contract proven by the
handler regression and signed-profile smoke. None of these corrections is a
product scope expansion. Any
future change to the capability list, closed profiles, toolchain, public routes,
updater trust/egress, transaction boundary, Worker image/profile, migration
ownership, feature defaults or exact 165-path future file manifest is a new
scope amendment requiring review. Child 12 export, Child 13 recovery, Child 14
lifecycle/retention and Child 15 GA remain out of scope.

The later **closed-profile advertisement/preflight/executable parity
correction** changes no manifest path or dependency: archive compressed-stream
validation, advertised image execution, optional physical secret
classification, and OOXML/ODF fail-closed preflight are implemented inside the
same approved paths. It preserves the earlier manifest-amendment records.
