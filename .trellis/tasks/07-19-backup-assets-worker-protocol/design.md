# Child 10 Backup Asset Worker Protocol And Derived Store Design

## 0. Planning authority and gate

```text
task:                    .trellis/tasks/07-19-backup-assets-worker-protocol
status:                  planning
parent:                  07-12-backup-data-explorer-design (planning tracker)
branch:                  codex/backup-assets-worker-protocol
base / inspected SHA:    main / 2ce71339b7f10fe759c0009ff01a100e589a700c
delivered program state: 9/15
planning review:         approved by user 2026-07-19
implementation auth:    approved by user 2026-07-19; workflow transition pending
task.py start:           not_executed (active workflow requires planning)
product implementation: not_executed
```

This document narrows parent design §§7, 16, 18 and 21 plus parent implement
§11 against merged current main. The parent remains authoritative where this
document is silent. This design does not authorize implementation.

The focused correction set is explicit:

- Child 10 owns paired `000067_backup_asset_processing`; `000068...000071`
  remain untouched.
- Parent implement §11's `BackupAssetMigration066` test selector is a historical
  typo. Every Child 10 command uses `BackupAssetMigration067`.
- `fetching` and `materializing` are separate states, not a slash-joined state.
- Current main requires keyring, Content Broker internal facet, repository root
  validation, runtime composition, generated Swagger and PostgreSQL CI files
  that the parent's coarse manifest omitted. The exact focused manifest is in
  `implement.md`.
- Full container sandbox, real updater/capabilities, Worker image/Compose/CI
  publish and enhanced UI remain Child 11.

## 1. Goals and non-negotiable invariants

### 1.1 Goals

1. Persist a small-deployment-friendly queue without Redis/Kafka.
2. Make at-least-once execution publish effectively once through fences.
3. Coalesce only byte-for-byte equivalent, schema-valid output work.
4. Keep every Provider credential/read primitive in the current Repository →
   Content boundary.
5. Make Worker trust explicit, local-first and remote-disabled by default.
6. Encrypt every durable derivative independently from Search/Export/cache.
7. Make projection revocation precede cryptographic/file destruction under
   normal execution, crash recovery, key loss and rollback.
8. Keep Core useful and quiet when no Worker is installed.

### 1.2 Invariants

- A Worker identity comes only from an authenticated connection. No header or
  JSON `worker_id` can create or upgrade trust.
- A Worker never receives a database handle/DSN, Repository/Task binding,
  Provider kind/locator, SSH/Restic/Rclone credential, native path, host path,
  user query, original filename, updater credential or arbitrary outbound URL.
- A job always carries composite `AssetRef{recovery_point_id, entry_id}` and an
  exact Catalog/source binding. A bare entry ID is never sufficient.
- State, transition revision, processing error code, attempt, Worker lease,
  RecoveryPoint lease/fence, retry schedule, cancellation, supersession and
  expiry are independent columns/products.
- Every Input/Sink activation is one-use and bound to exactly one
  job/attempt/Worker/fence. An activated session remains authorized only by the
  same authenticated connection identity plus persisted binding; activation
  material is never a reusable bearer.
- Every artifact set is invisible until all members and the manifest publish
  in one fenced DB transaction. File-system staging before that transaction can
  create only invisible ciphertext orphans, never partially visible products.
- Search projection removal commits before any derived key/reference/blob is
  made unavailable. A crash may leave an unreferenced readable derivative, but
  never a searchable derivative whose key/blob was already destroyed.
- `backup_assets.enabled=false`, transport disabled, Worker missing, or Derived
  key unavailable never changes RecoveryPoint credibility or core Catalog/
  Search/Content availability.

## 2. Component topology and ownership

```text
Public /api/v1 admin GET
          │ JWT + Admin + feature gate + standard rate/body limits
          ▼
BackupWorkerHandler ───────────────► sanitized ProcessingAdminSummary

local UDS peer credentials ─┐
                            ├─► dedicated internal router ─► ProtocolService
remote TLS 1.3 mTLS ────────┘          fixed-route logs          │
                                                               │ pull/heartbeat
                    ┌──────────────────────────────────────────┤
                    ▼                                          ▼
            Persistent Coordinator ◄──── interest/work key ─ Worker Client
                    │
          ┌─────────┴───────────┐
          ▼                     ▼
processing_job RP lease    one-use Input/Sink grants
          │                     │
          │          ┌──────────┴──────────┐
          │          ▼                     ▼
          │  content.AttemptBroker      Artifact Sink
          │          │                     │
          │  repository.SourceResolver     ▼
          │          │               encrypted staging
          │          ▼                     │
          │   bounded Provider bytes       ▼
          └──────────────────────────► fenced manifest commit
                                            │
                         ┌──────────────────┴─────────────────┐
                         ▼                                    ▼
                 Derived Store                         Child 7 ingest port
              blob/ref/key metadata              content/OCR projection only
```

`backupasset/runtime.Runtime` remains the only service composition root. It
constructs one shared DB, FoundationService, Keyring, LeaseService,
RepositoryService, Search ingest, Content services and Processing services.
`cmd/server` only wraps the runtime's narrow protocol/admin ports in listeners;
it cannot construct another coordinator, keyring, lease service or Provider
registry.

Package dependencies flow one way:

```text
processing -> backupasset + content contracts + search port + model
content    -> backupasset + model (never processing)
repository -> content contracts (implements sealed source resolution)
runtime    -> processing/content/search/repository (composition only)
api        -> processing service interfaces (adapters only)
```

The `content.AttemptBroker` types live in `content` so processing may consume
the narrow port without creating a content↔processing import cycle.

## 3. Work identity and canonicalization

### 3.1 Versioned descriptor

```go
type WorkDescriptorV1 struct {
    SchemaVersion          int
    Source                 backupasset.AssetRef
    CatalogGenerationID    string
    SourceFingerprint      string
    EntryFingerprint       string
    ProviderCapabilityRev  int
    Capability             string
    CapabilitySchema       string
    PipelineFingerprint    string
    OutputProfile          string
    SecurityPolicyRevision string
    Parameters             CanonicalParameters
    OutputLimits           OutputAffectingLimits
}
```

`work_key` is SHA-256 over a domain-separated, length-prefixed canonical binary
encoding of every field above:

```text
xirang.backup_asset.work.v1 || canonical(WorkDescriptorV1)
```

`CanonicalParameters` is not caller-provided arbitrary JSON. A capability
schema validator must reject unknown/duplicate members, invalid UTF-8, nulls
where not explicitly allowed, non-finite/non-canonical numbers, default-value
ambiguity and values outside hard limits, then re-encode a typed value in one
canonical form. The hash includes the parameter schema version and all explicit
defaults.

The following are always output-affecting and therefore included even if a
future capability calls them a “limit”: dimensions, crop/orientation, codec,
container, page/member/time/frame range, quality, language/model/font/profile,
maximum pages/pixels/duration/expanded bytes, truncation/coverage policy,
maximum output bytes/count and any deadline that may produce partial output.
Queue priority, retry count, lease heartbeat and requester identity do not
change output and are excluded.

Difference/property tests form a required matrix: mutating any one included
field changes the key; map insertion order and semantically identical canonical
values do not. SHA input is never logged or returned by the admin API.

### 3.2 Current job and reuse

`backup_asset_processing_jobs` has a partial unique current slot on `work_key`.
One current row may be queued/running/retryable or point at a valid succeeded
artifact set. A terminal row whose result is not reusable clears `is_current`
in the same transaction; history remains, and a later request may create a new
job. A succeeded row is current only while its artifact set is active and its
source/pipeline/policy binding remains valid.

Request algorithm, inside a retryable transaction:

1. Validate and canonicalize the complete descriptor before DB access.
2. Revalidate source eligibility and compatible trusted capability availability.
   If no Worker has ever been configured/registered for the product, return
   informational `not_deployed` without inserting a job.
3. Reuse an exact active artifact set if present.
4. Otherwise lock current work-key row, add/idempotently reactivate an interest,
   and upgrade effective priority if needed.
5. If no current row exists, insert one queued job plus the first interest.
   The partial unique index resolves concurrent creators on SQLite/PostgreSQL.

## 4. Closed processing state machine

### 4.1 States

```go
const (
    ProcessingQueued          ProcessingState = "queued"
    ProcessingLeased         ProcessingState = "leased"
    ProcessingFetching       ProcessingState = "fetching"
    ProcessingMaterializing  ProcessingState = "materializing"
    ProcessingProcessing     ProcessingState = "processing"
    ProcessingUploading      ProcessingState = "uploading"
    ProcessingValidating     ProcessingState = "validating"
    ProcessingRetryWait      ProcessingState = "retry_wait"
    ProcessingCancelRequested ProcessingState = "cancel_requested"
    ProcessingCanceled       ProcessingState = "canceled"
    ProcessingSucceeded      ProcessingState = "succeeded"
    ProcessingFailed         ProcessingState = "failed"
    ProcessingSuperseded     ProcessingState = "superseded"
    ProcessingExpired        ProcessingState = "expired"
)
```

`fetching` means the attempt activated Input and is obtaining bounded bytes.
`materializing` means a future path-requiring capability is creating its
job-private tmpfs view. They are independent:

- stream/Range path: `leased → fetching → processing`;
- future path path: `leased → fetching → materializing → processing`.

Child 10 exercises the first path with a no-op fixture and only validates the
second transition contract. It does not implement tmpfs or tools.

### 4.2 Transition matrix

Every transition uses `WHERE id=? AND state=? AND transition_revision=?`,
increments revision exactly once and validates the current attempt/fence when
the source state is attempt-owned.

| From | Allowed to | Required reason/side effect |
|---|---|---|
| `queued` | `leased` | trusted compatible pull; create attempt and RP lease |
| `queued` | `cancel_requested` | final interest gone; grants cannot exist |
| `queued` | `superseded` | source/pipeline/policy identity replaced |
| `queued` | `expired` | source/RP/deadline no longer eligible |
| `leased` | `fetching` | Input grant activated by exact Worker |
| `fetching` | `materializing` | descriptor explicitly requires safe path |
| `fetching` | `processing` | streaming/Range input contract satisfied |
| `materializing` | `processing` | future sandbox reports bounded materialization complete |
| `processing` | `uploading` | processing output contract completed |
| `uploading` | `validating` | all declared members uploaded; Sink closed to writes |
| `validating` | `succeeded` | one current-fence manifest commit and required projection publish |
| `leased`…`validating` | `retry_wait` | closed transient error; revoke grants/release or expire attempt |
| `retry_wait` | `queued` | backoff due, interest/source/deadline still valid; no old fence reuse |
| any nonterminal except `cancel_requested` | `cancel_requested` | final interest/cancel; revoke Input/Sink before state visible |
| `cancel_requested` | `canceled` | Worker acknowledged or lease expired; staging destroyed |
| any nonterminal | `superseded` | source fingerprint changed; reject/destroy output |
| any nonterminal | `expired` | source/RP/absolute deadline expired; revoke/release/cleanup |
| attempt-owned active state | `failed` | permanent or contract/security error, or retries exhausted |
| terminal | none | rebuild/re-request creates a new current job row |

No shortcut `queued → canceled`, no `failed → queued`, and no
`succeeded → queued`. Reconciliation may execute two legal transitions in
sequence but persists/revisions each separately.

### 4.3 Stable processing errors

Processing outcome codes are closed and separate from sanitized HTTP transport
codes:

| Category | Codes | State policy |
|---|---|---|
| permanent input/policy | `unsupported_format`, `encrypted_archive`, `input_too_large`, `materialization_disabled`, `source_changed`, `source_expired` | `failed`, `superseded`, or `expired`; never automatic retry |
| transient capacity/infrastructure | `worker_unavailable`, `provider_unavailable`, `quota_busy`, `timeout`, `worker_crash`, `lease_lost` | bounded `retry_wait`; new attempt/fence only |
| contract/security | `protocol_incompatible`, `invalid_output`, `digest_mismatch`, `sandbox_violation`, `network_violation` | Worker quarantined; current job terminal failed; no blind retry |

An absent code is valid only for normal states/transitions. Localized/raw error
text is not persisted. Successful partial output uses
`completeness=partial` plus closed warnings, never `failed`.

## 5. Persistent schema owned by migration 000067

The Go model file and paired migrations define the same shapes and closed
constraints. Names are frozen for the focused plan:

| Table | Lifecycle owner and purpose |
|---|---|
| `backup_asset_processing_jobs` | Coordinator; work descriptor digest/canonical bytes, exact source binding, current slot, state/revision, error/retry/cancel/supersede/expiry dimensions |
| `backup_asset_processing_interests` | Coordinator; idempotent requester reference, priority class, active/removed lifecycle; no raw UI/query/path |
| `backup_asset_processing_attempts` | Coordinator; attempt number, authenticated worker, Worker pull lease/heartbeat/deadline, exact RecoveryPoint lease/attempt/fence binding, closed outcome |
| `backup_asset_processing_grants` | Grant service; input/sink kind, activation-secret hash, job/attempt/worker/fence binding, one-use state/TTL and aggregate budgets |
| `backup_asset_processing_grant_requests` | Grant service; input Range/sink upload reservation, in-flight/final byte accounting and crash reconciliation |
| `backup_asset_processing_uploads` | Artifact Sink; per-member role/MIME/declared+actual size/digest/completeness and opaque encrypted staging ID/state |
| `backup_asset_worker_identities` | Protocol service; transport-derived stable ID, local peer or cert fingerprint, trust/quarantine state, compatible protocol, health/last-seen; no private key/subject/raw diagnostic |
| `backup_asset_worker_capabilities` | Protocol service; strict advertisement columns/canonical digest for capability/schema/pipeline/profile/modes/limits/health |
| `backup_asset_derived_artifact_sets` | Derived service; atomic manifest/source/work/fence/policy binding and closed Derived state |
| `backup_asset_derived_artifacts` | Derived service; member role/MIME/size/digest/completeness/coverage and physical blob reference |
| `backup_asset_derived_blobs` | Derived store; physical digest/size/chunk format, opaque relative locator, Wrapped DEK envelope + Derived KEK version, state/refcount reconciliation facts |
| `backup_asset_derived_blob_references` | Derived lifecycle; exact source/artifact authorization and lifecycle reference for cross-RP sharing |
| `backup_asset_updater_metadata` | Future Child 11 seam; signed source/version/fingerprint/verification/activation/failure metadata only |

Critical SQL constraints include:

- exact 32/64-hex identity/digest lengths; nonnegative bounded counters;
- closed state/error/grant/transport/health/completeness products;
- composite FK from job/artifact source to current Catalog tuple where stable,
  plus RecoveryPoint and user-independent opaque ownership constraints;
- unique `(job_id, attempt_number)`, one current attempt per job, one current
  work-key slot, one active interest key, one grant kind per attempt, one upload
  ordinal per Sink, one manifest commit per set, one reference per
  source/artifact/blob;
- `is_current` only for reusable states, grant counters never over maxima,
  activated/revoked/expired timestamps consistent with state, and terminal
  processing timestamps/error products consistent with the transition matrix;
- SQLite integers/booleans and PostgreSQL booleans express equivalent partial
  unique indexes and CHECK semantics; all timestamps are explicit UTC with no
  database `CURRENT_TIMESTAMP` defaults.

`000067` also expands `wrapped_domain_keys.domain` with `derived_store`. It does
not change the RecoveryPoint holder CHECK because `processing_job` already
exists on current main.

## 6. Pull scheduling, interests and fencing

### 6.1 Scheduling

Workers advertise protocol version, capability/schema/pipeline, MIME/profile,
input modes, hard limits, health and slot counts. Advertisement is strict:
unknown fields/products or limits beyond server policy reject the whole
handshake. A Worker cannot advertise an actual Child 11 capability as usable
until Core has a registered matching schema/policy; Child 10 registers only a
test-injected no-op schema.

The server owns queue truth and matches a pull transactionally. Background work
never consumes `interactive_reserved_slots`. Interactive work first uses the
reserve and may borrow an idle background slot; borrowed work is not preempted,
so the reserve remains available. Within a class, stable priority + queued time
+ job ID ordering prevents starvation and makes SQLite/PostgreSQL behavior
testable.

Adding an interactive interest to background work upgrades the current job;
removing it recomputes priority from remaining interests. It never creates a
second job or changes `work_key`.

### 6.2 Dual lease binding

The Worker pull lease and source-retention lease are distinct records:

- `backup_asset_processing_attempts` owns the short Worker pull lease used for
  heartbeat/crash detection.
- existing `recovery_point_leases` owns a `processing_job` lease covering the
  complete source-dependent job lifecycle.

On pull, the Coordinator acquires or takes over the RP lease with owner
`processing:<job-id>` and binds the returned LeaseFence to the new attempt.
The Worker envelope receives the attempt number, lease deadline and fence but
never the lease row's Provider/source internals. Heartbeat renews both leases;
the effective deadline is their minimum. Failure of either revokes grants and
prevents publication.

Takeover is allowed only after the prior Worker lease/fence is stale. It uses
`LeaseService.Takeover`, obtains a new root attempt/fence, creates a new
processing attempt, invalidates old grants and moves through `retry_wait →
queued → leased`. The existing Child 7 ingest port validates this same root
fence in its transaction.

## 7. Worker identity and transport

### 7.1 Local trust

- Local transport is disabled unless `worker_local_enabled=true`.
- The listener is a Unix socket under a clean absolute startup path. Its parent
  must be owned by the Core effective UID, non-symlink, mode 0700; stale sockets
  are removed only after type/owner checks; the new socket is mode 0600.
- Linux `SO_PEERCRED` provides PID/UID/GID. The peer UID must equal Core's
  effective UID. The trusted identity is derived from transport kind, UID and
  a validated random Worker instance ID; the instance ID alone is not trusted.
- Non-Linux builds return typed local-transport unsupported and never fall back
  to loopback TCP or unauthenticated headers.

### 7.2 Remote trust

- `worker_remote_enabled=false` is the default and means no TCP bind.
- Enabling requires a non-wildcard reviewed listen address, server certificate
  and private-key files, client CA file and exact trust domain. Missing/unsafe
  values fail startup before listening.
- TLS minimum/maximum is 1.3, `ClientAuth=RequireAndVerifyClientCert`, a private
  CA pool is used, renegotiation is disabled, and certificate expiry/EKU are
  verified by Go's normal chain validator.
- Worker identity is an exact canonical URI SAN
  `spiffe://<configured-trust-domain>/asset-worker/<opaque-id>` from the verified
  leaf. CN/subject/header fallback, wildcard trust domains and multiple
  conflicting Worker SANs are rejected.
- Only certificate/identity fingerprints are persisted. Private key, raw
  certificate, subject and SAN are not returned/logged. Certificate replacement
  creates an explicit identity revision; it cannot inherit an active fence.

### 7.3 Identity lifecycle

Trust states are `active | quarantined | revoked`; capability health is
orthogonal `ready | degraded | draining`. A draining Worker finishes or cancels
current work but receives no new lease. Protocol incompatibility or
invalid-output/digest/sandbox/network contract failure atomically quarantines
the identity, revokes its active grants and denies pulls. Child 10 has no public
unquarantine mutation; remediation/re-enrollment requires later reviewed
administrative policy.

## 8. Internal protocol surface

The dedicated router is not mounted below public `/api/v1`, does not apply
browser CORS, never trusts X-Forwarded-* identity, and uses safe fixed route
labels. IDs in paths are opaque and non-authorizing; activation secrets occur
only in strict JSON bodies. Limits are enforced before decode/read and again by
domain budgets.

| Route | Purpose | Max request | Per-identity ceiling |
|---|---|---:|---:|
| `POST /internal/v1/asset-worker/handshake` | version + strict capability advertisement | 64 KiB | 12/min |
| `POST /internal/v1/asset-worker/leases/pull` | bounded long-poll (≤20s), returns one envelope | 16 KiB | 120/min |
| `POST /internal/v1/asset-worker/jobs/:jobId/heartbeat` | renew + receive cancel/drain state | 16 KiB | 360/min |
| `POST /internal/v1/asset-worker/jobs/:jobId/transitions` | advance one legal state/revision | 16 KiB | 360/min |
| `POST /internal/v1/asset-worker/jobs/:jobId/input/activate` | consume one Input activation | 8 KiB | 60/min |
| `POST /internal/v1/asset-worker/input-sessions/:sessionId/ranges` | one sequential/Range request; binary response | 8 KiB | 600/min plus byte budgets |
| `POST /internal/v1/asset-worker/jobs/:jobId/sink/activate` | consume one Sink activation | 8 KiB | 60/min |
| `POST /internal/v1/asset-worker/sink-sessions/:sessionId/artifacts` | one declared member; streaming body | configured per-artifact hard max | 120/min plus byte budgets |
| `POST /internal/v1/asset-worker/sink-sessions/:sessionId/manifest` | one atomic manifest submit | 64 KiB | 60/min |
| `POST /internal/v1/asset-worker/drain` | stop pulls and graceful shutdown | 8 KiB | 12/min |

Every JSON decoder disallows unknown/trailing/duplicate members and validates
closed products. Binary input/output paths use `http.MaxBytesReader` plus domain
reservation; no generic JSON error includes raw decoder/tool/output text.
Sanitized protocol codes are only `invalid_request`, `unauthenticated`,
`forbidden`, `feature_disabled`, `rate_limited`, `body_too_large`, `not_found`,
`conflict`, `fence_lost`, and `temporarily_unavailable`. They are not persisted
as processing outcome codes.

## 9. One-use Input session through Child 8

### 9.1 Activation and port shape

The Coordinator creates independent Input and Sink activation material at
attempt lease time. It returns plaintext secrets once in the JobEnvelope and
persists only SHA-256 hashes. Activation atomically matches:

```text
grant kind + state=issued + unexpired
+ job ID + attempt number/ID + transport-derived Worker ID
+ current Worker lease + processing_job RP lease/fence
+ constant-time secret hash
```

On success the secret hash is cleared/replaced with a non-authorizing consumed
marker; `activated_at` is set and replay always fails.

`content.AttemptBroker` accepts only an internal binding:

```go
type AttemptSourceBinding struct {
    Ref                 backupasset.AssetRef
    CatalogGenerationID string
    SourceFingerprint   string
    EntryFingerprint    string
    AllowedModes        []SourceMode
    Limits              AttemptReadLimits
    AbsoluteExpiresAt   time.Time
}
```

It owns the existing `content.SourceResolver`; processing receives an
`AttemptInputSession` interface only. Neither processing nor protocol handler
can type-assert/extract Repository service, Provider registry, access binding,
locator or source handle.

### 9.2 Reads and accounting

- Activation returns safe size/MIME/fingerprint strength/read-mode facts, never
  a path/name/locator.
- Each stat/sequential/Range call locks/reserves request count, cumulative
  Provider bytes and in-flight slots before `OpenContentSource`.
- A Range is one normalized `{offset,length}`; the session may make many calls
  within `max_requests`, `max_in_flight`, per-call and cumulative limits.
- ProviderBytes includes overflow probes and is charged conservatively exactly
  as the current Content plane does. Unknown/crash outcomes charge reservation.
- SourceResolver revalidates exact Catalog/source/entry before open and after
  close. Mutable source change revokes both grants and supersedes the job.
- Context cancellation closes/join the Provider reader. No bytes are cached to
  plaintext disk; future path materialization is not an Input Broker fallback.

The Broker also exposes `RevalidateAttemptSource` for the Sink's final
publication check. It opens only a sealed stat/revalidation path and returns no
source internals.

## 10. Artifact Sink and atomic manifest

### 10.1 Upload

Sink activation uses the same one-use rules but a separate secret/hash/session.
For each member the Worker declares a closed role, MIME, expected plaintext
size/digest, completeness/coverage and manifest ordinal. The service reserves
member count, per-member and aggregate bytes before reading the body.

Core streams body bytes through:

```text
size limiter → SHA-256 digest → chunk encryptor → safe staging object
```

There is no plaintext file, shared writable directory or Worker-visible root.
Raw output/stdout/stderr is never logged. A member with wrong length/digest,
unknown MIME/role, duplicate ordinal, excessive count/bytes or trailing data is
rejected and its ciphertext staging destroyed/orphaned for reconciliation.

### 10.2 Manifest commit

The manifest includes exact job/work/attempt/fence, source and policy revision,
expected output profile, member count/order/role/MIME/size/digest/completeness,
warnings and total bytes. Commit is allowed exactly once:

1. Close Sink to new uploads and move job `uploading → validating`.
2. Validate every member against the registered capability schema/output
   profile and hard limits; recompute manifest digest.
3. Revalidate source through AttemptBroker and current security-policy revision.
4. Durably finalize ciphertext files under opaque content-addressed names. A
   crash here leaves only invisible orphan ciphertext.
5. In one DB transaction lock job/attempt/grant/staging rows, call
   `LeaseService.ValidateFenceTx`, recheck revision/current slot/source/policy,
   upsert/reuse blobs, insert every artifact/ref/set, and mark the manifest
   committed. The whole set becomes visible together.
6. For content/OCR products, publish the required Search projection through
   Child 7's port only after the derivative is readable. The job becomes
   `succeeded` only after required projection publication. A crash-safe
   reconciliation may complete this forward step.

If step 3/5/6 finds old fence, cancel, expiry or source change, the set never
becomes a successful result; staged/new unreferenced ciphertext is destroyed.
Late output cannot replace a current artifact merely because digest/work key
matches.

## 11. Derived Store cryptography and references

### 11.1 Key separation and format

`KeyDomainDerivedStore = "derived_store"` is optional at feature-disabled
startup and created only when Processing/Derived startup is enabled. It is not
in the unconditional Foundation required domains.

For every new physical blob:

- generate a random 32-byte DEK using `crypto/rand`;
- split plaintext into configured fixed-size chunks;
- use standard-library AES-256-GCM with an 8-byte per-blob random nonce prefix
  plus a 4-byte big-endian chunk index; reject index overflow before writing;
- bind AAD to `format_version, blob_id, plaintext_digest, plaintext_size,
  chunk_size, chunk_index`;
- wrap the DEK under the active Derived KEK with AES-256-GCM, a fresh random
  12-byte nonce and AAD binding
  `blob_id, derived_kek_version, cipher_format`;
- zero in-memory DEK/chunk buffers on completion/error where practical.

The DB stores no plaintext, only digest/size/format, opaque relative locator,
wrapped DEK envelope and KEK version. Search holds only HMAC postings and an
opaque artifact/excerpt reference. Export and process cache have no access to
this key domain.

### 11.2 Root and filesystem safety

Repository owns a generalized private-runtime-root validator. Content cache's
existing check and Derived Store both call it with separate purpose/root:

- absolute, clean, non-root path;
- not equal/ancestor/descendant of `/data`, `/backup`, `/logs` or any decoded
  local backup source/binding;
- no symlink, bind/traversal/special-file escape; open beneath a trusted root;
- per-object staging/final names are server-generated opaque IDs;
- atomic rename, file+directory durability and bounded per-root operations;
- no recursive unscoped `RemoveAll`.

### 11.3 Cross-RP references and GC

Deduplication is physical only. Each artifact creates an exact source reference
containing RecoveryPoint/entry/source fingerprint and lifecycle state. A source
expiry/revoke removes only its reference. `ref_count` is a checked cache of live
reference rows and is repaired transactionally/reconciled, never trusted alone.

On last reference:

1. prove Search projections for every affected artifact are already revoked;
2. atomically set blob/key state to non-readable and erase the wrapped DEK
   envelope (cryptographic deletion);
3. delete ciphertext by exact opaque locator;
4. remove tombstone metadata only after reconciliation proof. A delete failure
   stays observable/retryable but ciphertext is already unreadable.

Startup and periodic batched reconciliation handles staging orphans, DB row
without file, file without DB row, incorrect refcount, interrupted key rewrap
and pending projection revoke. It never turns an unknown orphan into an active
artifact.

### 11.4 KEK rotation and loss

- Master application KEK rotation uses existing `Keyring.RewrapAll`: Derived
  domain key plaintext/version remains unchanged.
- Derived KEK rotation creates a new active version and leaves the old version
  verify-only. The store unwraps each small DEK with its recorded old version,
  rewraps it under the new version in bounded batches, and updates envelope +
  version atomically. Ciphertext is not decrypted/re-written.
- Crash resumes from per-blob version. Old KEK retires only after a locked query
  proves no readable blob references it.
- Active Derived KEK loss first invokes projection-safe invalidation for every
  affected artifact, marks sets unavailable, then marks the key lost and
  schedules rebuildable work. It never silently generates a replacement while
  old projections remain.

## 12. Derived/Search publication and revoke ordering

`DerivedArtifactState` is closed:

```text
active | stale | unavailable | superseded | revoked | purging | purge_failed
```

It is orthogonal to `complete|partial`, scan finding and job state. Pipeline or
policy changes mark old sets stale/superseded and use a different work key/job.

Revocation choreography is intentionally asymmetric:

```text
1. hold/validate processing_job fence or a dedicated trusted cleanup context
2. Child 7 RevokeContentProjection transaction:
   - delete content/OCR postings
   - clear excerpt_ref
   - mark field coverage unavailable
   - advance coverage/pipeline/index + projection revisions
3. persist derived reference/set revoked|unavailable
4. if last reference, destroy wrapped DEK
5. delete ciphertext; reconcile failures without restoring projection
```

The same order is used for explicit revoke, source expiry callback, Derived key
loss, application rollback and `000067` schema-down preparation. If step 2
fails, steps 3-5 do not run. If a later step fails, queries are already safe and
reconciliation only moves forward. Publication runs in the reverse availability
direction (readable derivative first, Search projection second), so no path can
create a ghost hit.

## 13. Generic signed updater metadata boundary

`backup_asset_updater_metadata` is a schema/contract seam only. It stores:

- closed source kind and opaque source ID/fingerprint;
- semantic version, bundle/manifest digest and signing-key fingerprint;
- verification and activation timestamps;
- closed `registered | verified | active | superseded | failed` state;
- stable bounded failure code, never raw verifier/downloader/tool output.

There is no URL credential, bearer, private signing key, raw manifest/output or
bundle bytes. Child 10 has no updater service/binary/download/outbound network.
Future Child 11 must use a separate updater identity and signed content-addressed
store; a processing Worker cannot switch identity or request updater egress.

## 14. Protocol-only asset-worker

The binary imports only the Worker protocol client and standard crypto/TLS/
HTTP/runtime packages. It:

1. derives/generates a non-secret instance ID;
2. connects to one configured local UDS or remote mTLS Core endpoint;
3. handshakes, pulls, heartbeats and observes cancel/drain;
4. dispatches only a registered client-side capability interface;
5. activates bounded Input/Sink sessions and submits one manifest;
6. on SIGTERM reports draining, stops pulls, cancels/join current fixtures and
   exits within a bounded grace period.

Child 10's fake/no-op capability is injected only by tests. Production registry
is empty, so the binary cannot claim OCR/thumbnail/text/media/archive/malware
support. Tests may feed deterministic bytes and return a small passive artifact
to prove the wire lifecycle. No tool executable, updater, network sandbox image
or raw tool output exists in this child.

## 15. Admin API and sanitized DTO

The only public API addition is:

```text
GET /api/v1/admin/backup-asset-processing
```

It is inside existing Auth/Audit/API rate/body middleware, additionally requires
Admin, checks `backup_assets.enabled`, accepts no body/query and has a focused
30 requests/minute ceiling. Operator/Viewer fail before service access.

Response fields are bounded aggregates:

```go
type ProcessingAdminSummary struct {
    SchemaVersion int
    Configured, LocalEnabled, RemoteEnabled bool
    WorkerCounts  { Active, Draining, Degraded, Quarantined int }
    Slots         { InteractiveUsed, InteractiveTotal, BackgroundUsed, BackgroundTotal int }
    Queue         { ByState, ByPriority map[closed]string/int; OldestQueuedSeconds int64 }
    Outcomes      { ByErrorCategory map[closed]string/int64 }
    Derived       { ByState map[closed]string/int64; LogicalBytes, PhysicalBytes, OrphanBytes, QuotaBytes int64 }
    ReconciledAt  *time.Time
}
```

The handler never returns Worker IDs, job IDs, AssetRef, source/job count by RP,
capability parameters, pipeline fingerprint, grant/session/attempt/fence,
activation material, local UID/PID/socket, remote address, certificate detail,
path/blob locator or raw failure. Swagger documents this admin DTO/403/404/429/
503 behavior. No frontend client or UI is added.

## 16. Settings contract

All values join the atomic `BackupAssetSettingsSnapshot`; cross-setting checks
run for DB/env/default resolution. Defaults are conservative and feature-
disabled. Exact keys are frozen:

| Key | Default | Bounds / lifecycle |
|---|---:|---|
| `backup_assets.processing_queue_max` | `10000` | 1..100000, dynamic |
| `backup_assets.processing_interactive_slots` | `2` | 1..64, dynamic |
| `backup_assets.processing_background_slots` | `2` | 1..64, dynamic |
| `backup_assets.processing_pull_lease` | `90s` | 15s..5m, dynamic |
| `backup_assets.processing_pull_heartbeat` | `20s` | 5s..1m and < pull lease/2 |
| `backup_assets.processing_attempt_timeout` | `2h` | 1m..24h and ≤ RP absolute deadline |
| `backup_assets.processing_retry_max` | `5` | 0..20, dynamic |
| `backup_assets.processing_retry_base` | `5s` | 1s..5m, dynamic |
| `backup_assets.processing_retry_max_delay` | `15m` | base..2h, dynamic |
| `backup_assets.processing_input_request_max_bytes` | `67108864` | 64 KiB..1 GiB |
| `backup_assets.processing_input_cumulative_max_bytes` | `2147483648` | request..16 GiB |
| `backup_assets.processing_input_max_requests` | `512` | 1..4096 |
| `backup_assets.processing_input_max_in_flight` | `4` | 1..32 |
| `backup_assets.processing_sink_max_artifacts` | `32` | 1..256 |
| `backup_assets.processing_sink_artifact_max_bytes` | `536870912` | 64 KiB..4 GiB |
| `backup_assets.processing_sink_total_max_bytes` | `1073741824` | artifact..16 GiB |
| `backup_assets.processing_protocol_json_max_bytes` | `65536` | 4 KiB..1 MiB |
| `backup_assets.worker_local_enabled` | `false` | RequiresRestart |
| `backup_assets.worker_local_socket` | `/run/xirang/asset-worker.sock` | clean absolute, RequiresRestart |
| `backup_assets.worker_remote_enabled` | `false` | RequiresRestart; remote trust closed by default |
| `backup_assets.worker_remote_listen_addr` | empty | non-wildcard when enabled, RequiresRestart |
| `backup_assets.worker_remote_server_cert_file` | empty | required when enabled, RequiresRestart, Sensitive |
| `backup_assets.worker_remote_server_key_file` | empty | required when enabled, RequiresRestart, Sensitive |
| `backup_assets.worker_remote_client_ca_file` | empty | required when enabled, RequiresRestart, Sensitive |
| `backup_assets.worker_remote_trust_domain` | empty | exact canonical domain, RequiresRestart, Sensitive |
| `backup_assets.derived_store_root` | `/var/lib/xirang-asset-runtime/derived` | safe root, RequiresRestart |
| `backup_assets.derived_store_chunk_bytes` | `1048576` | 64 KiB..8 MiB, RequiresRestart/format-bound |
| `backup_assets.derived_store_blob_max_bytes` | `4294967296` | chunk..16 GiB, dynamic admission |
| `backup_assets.derived_store_global_max_bytes` | `107374182400` | blob..1 TiB, dynamic admission |
| `backup_assets.derived_store_reconcile_interval` | `15m` | 1m..24h, dynamic |
| `backup_assets.derived_store_reconcile_batch_size` | `256` | 1..10000, dynamic |

`backup_assets.enabled` remains `false`. Enabling it does not implicitly enable
either Worker transport. Static paths/material are read once for listener/store
construction; DB updates report restart required and do not hot-swap trust.

## 17. Runtime lifecycle and graceful degradation

Startup order:

1. read one valid ProcessingConfig snapshot;
2. run global domain-key master rewrap; if Derived domain exists and cannot be
   unwrapped, set Derived unavailable and invoke projection-safe key-loss path;
3. validate Derived root if store/rows require it;
4. reconcile abandoned attempts/grants/staging/refcounts/orphans/projections;
5. only when `backup_assets.enabled` is true mark Coordinator ready;
6. start dedicated transport listeners only for explicitly enabled transports;
7. run coordinator/reconciliation loops.

Shutdown order is transport-first:

1. stop accepting handshakes/pulls and mark Workers draining;
2. wait bounded in-flight requests, revoke grants after grace and cancel
   attempts;
3. stop Coordinator admission and join heartbeats/uploads;
4. flush/finalize safe DB reservations and Derived reconciliation;
5. release/expire processing_job leases and close listeners/store handles;
6. then current Runtime continues existing Content/Search/Catalog/publication
   shutdown order.

No configured Worker means no listener, no job creation, zero-noise informational
summary and no alert. A feature-disabled Core does not create the Derived key or
root. Existing Catalog/Search/Content/workspace remain usable because their
runtime readiness is independent.

## 18. Observability and safe failures

Metrics have only closed, low-cardinality labels: priority, ProcessingState,
error category, transport kind, trust/health class, grant kind/outcome,
Derived state/reconcile result. IDs/fingerprints/path/MIME/pipeline/capability
parameters are not labels.

Structured logs use `logger.Module("backup-asset-processing")`, opaque internal
correlation IDs and fixed stage/code. They omit activation/fence, request body,
source/ref/path, cert subject/SAN, provider data, artifact plaintext/digest when
unnecessary, and raw Worker/tool output. Unknown errors become a generic safe
code and correlation ID.

Alert candidates are protocol/security quarantine, sustained queue stall after
Workers were configured, repeated lease/reconciliation failure, Derived tamper/
orphan growth and quota saturation. “No Worker configured” and isolated job
input/capability failure are not alerts and never enter backup health.

## 19. Migration down and rollback safety

Paired down migrations fail closed if any of the following remains:

- any 000067-owned table row;
- any `processing_job` RecoveryPoint lease row;
- any `derived_store` wrapped-domain-key row;
- any content/OCR Search posting or excerpt reference that the Processing
  rollback manager has not proven/revoked.

Prepared application/schema rollback must run before SQL down:

1. stop Worker listeners and new jobs;
2. revoke Input/Sink and drain attempts;
3. for every projected artifact, call Child 7 revoke and confirm its transaction;
4. mark all derived references unavailable/revoked;
5. cryptographically destroy last-reference DEKs, delete ciphertext/staging and
   reconcile orphans;
6. delete processing/updater metadata and `processing_job` lease history owned
   by this feature;
7. remove Derived key rows only after no blob envelope/reference remains;
8. invoke paired `000067` down. Provider/Catalog/original content remain intact.

If any step cannot prove safety, keep additive schema and run a forward repair;
do not force down. Disabling the feature flag alone is not rollback evidence.

## 20. Scope boundaries and rejected alternatives

| Decision | Adopted | Rejected |
|---|---|---|
| Queue | DB persistent pull queue | Redis/Kafka requirement; in-memory truth |
| Local trust | UDS + same-UID peer credentials | loopback IP/header/API-key trust |
| Remote trust | explicit TLS 1.3 mTLS URI-SAN domain, default off | public JWT route; bearer Worker token |
| Input | Child 8 internal AttemptBroker | Worker direct Provider/Repository; user ticket cookie reuse |
| Output | bounded Sink + one fenced manifest | Worker DB/Catalog writes; per-member visible commits |
| Work identity | typed schema canonical descriptor | filename/hash-only key; arbitrary map hashing |
| Durable crypto | random per-blob DEK + independent Derived KEK | Search/Export/cache key reuse; plaintext disk |
| Revoke | Search-first then key/blob | key-first deletion that leaves ghost postings |
| Child 10 Worker | protocol client + test-injected no-op | real tools, updater, image/Compose/UI |
| Public API | one sanitized Admin health GET | early preview jobs/capability UI/mutations |

No frontend, deployment, README/release, Provider mutation or migration 068+
file is part of this design.

## 21. Approved high-risk boundaries and amendment gate

The 2026-07-19 planning approval accepts these high-risk design boundaries;
implementation must still verify them at focused code-review checkpoints:

- canonical work-key schema/difference matrix;
- UDS peer credential and mTLS certificate identity extraction;
- one-use grant/budget/fence protocol;
- Derived chunk nonce/AAD/envelope design and key rotation/loss;
- Search-first revoke fault-injection ordering;
- paired 067 ordered SQLite/PostgreSQL apply/down.

Current-main research resolved the product choices needed for Phase 1; no user
question remains open. The four decisions in `prd.md` §7 are approved. A
requested change to transport, public API, capability scope, storage format or
manifest requires a focused planning amendment before `task.py start`.
