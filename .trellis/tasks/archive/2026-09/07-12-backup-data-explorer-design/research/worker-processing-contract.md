# Optional Content-Processing Worker Contract

- **Date verified:** 2026-07-13
- **Scope:** Thumbnail generation, text extraction, OCR, Office/PDF conversion, malware scanning, media probing/transcoding, and archive inspection/extraction.
- **Design constraint:** Core all-in-one browsing, native preview, download, and restore remain useful without a Worker. Workers never enter backup commit, verification, or retention truth paths.

## Executive Conclusion

Xirang should own the control plane and treat Workers as untrusted-by-default, stateless capability executors:

```text
Provider Adapter → Content Broker → Worker Sandbox → Artifact Sink → Catalog/Derived Store
```

Workers must not receive repository specifications, SSH/Restic/Rclone credentials, raw provider paths, database access, or a shared writable host directory. Xirang resolves an exact asset version, brokers bounded bytes, validates the result, and alone publishes Catalog/derived state.

## Control And Capability Discovery

The core contains a persistent database-backed job coordinator; a single-user deployment does not justify mandatory Redis/Kafka infrastructure. Workers pull short leases through an authenticated channel. Remote Workers require mTLS; an in-process/local transport may provide equivalent identity for a same-host deployment.

Capabilities are independently versioned, for example:

- `image.thumbnail`
- `text.extract`
- `image.ocr`
- `secret.classify`
- `document.convert`
- `malware.scan`
- `media.probe`
- `media.transcode`
- `archive.inspect`
- `archive.extract_entry`

Versioning has three layers:

1. **Worker protocol version:** handshake, lease, grant, upload, cancellation, and health compatibility.
2. **Capability schema version:** semantic input/output contract for one capability.
3. **Pipeline fingerprint:** exact tool/model/codec/font/virus-database/configuration identity that changes derived output or interpretation.

A capability advertisement includes accepted MIME families, output profiles, streaming/Range needs, maximum input bytes/pages/pixels/duration/archive expansion, CPU/GPU class, concurrency slots, and `ready/degraded/draining` health. Xirang schedules only an explicitly allowed and compatible profile.

## Job Identity, Deduplication, And Publication

A job envelope contains at least:

- `job_id`, attempt number, lease deadline, and fencing token;
- capability/schema/output profile and pipeline fingerprint;
- `RecoveryPoint`, opaque entry ID, source fingerprint, and provider capability snapshot;
- priority, deadline, hard input/output/resource limits, and source-retention lease.

The canonical `work_key` hashes:

```text
source version fingerprint
+ capability/schema
+ pipeline fingerprint
+ security-policy revision
+ every parameter and limit that changes output
```

- Identical queued/running work is coalesced; an already valid derived artifact is reused.
- Execution is “at least once, publish effectively once.” The Artifact Sink accepts output only from the current fencing token and publishes through a unique key/atomic state transition.
- Late results from expired attempts are rejected and destroyed.
- `mutable_head` is revalidated before input and before publication. A changed fingerprint supersedes the job; results are never bound to stale mutable content.
- Multi-step flows such as Office → pages → OCR or media probe → transcode are DAGs coordinated by Xirang. Workers do not call one another.

## Input Grant And Artifact Sink

### Input

The Worker receives a single-use activation secret bound to job, attempt, Worker identity, exact source object, allowed read mode, request/concurrency/cumulative-byte budgets, and TTL. Activation opens a bounded read session; it is not a reusable bearer outside that attempt.

- Sequential inputs stream through the Content Broker.
- Random-access inputs may make multiple bounded Range reads inside that session, backed when necessary by the approved authenticated chunk-encrypted cache.
- If an external tool requires a path, the Worker runtime materializes only that job into a dedicated limited `tmpfs`; the mount is `noexec,nosuid,nodev`, and the host must disable or encrypt swap.
- There is no fallback to ordinary plaintext disk. If safe materialization is unavailable, the capability returns `materialization_disabled` and the UI degrades explicitly.

### Output

The Worker activates an attempt-bound Artifact Sink session once; it may upload a bounded multi-artifact set and then submit one artifact manifest. It cannot write Catalog rows directly. Xirang validates expected output count, MIME, length, digest, completeness, security-policy revision, and fencing token before atomic publication.

Every `DerivedArtifact` binds:

- source `RecoveryPoint` and entry identity/fingerprint;
- capability/schema, output profile, pipeline fingerprint, and tool versions;
- parent derivative/root asset for chains;
- digest, size, completeness/truncation/coverage, sensitivity and findings;
- creation/last-use/expiry state and encryption metadata.

Authorization, sensitivity, encryption, retention, and purge inherit from the source. A derivative is never visible after the source expires.

## Capability-Specific Safety Rules

- **Text/OCR:** return bounded text segments and coordinates. Xirang performs indexing, permission filtering, secret classification, and snippet generation. Workers do not receive user search queries.
- **Office/PDF:** macros, JavaScript, external links, embedded remote resources, and fonts from the network are disabled. Output is a passive render profile, never an editable office session.
- **Malware:** a positive finding is a successful scan result, not a Worker failure. Store engine/signature versions and scan time. “Not scanned” is distinct from “no finding,” and neither changes recovery-point trust evidence.
- **Media:** probe before transcode; enforce dimensions, duration, bitrate, codec, frame, and output-byte limits. Transcoding failure falls back to native playback, download, or restore where permitted.
- **Archives:** inspection produces a bounded normalized directory index only. Member extraction is a separate capability addressed by opaque member ID. Reject absolute paths, traversal, links escaping the archive root, devices/FIFOs/sockets, excessive nesting, entry count, expanded bytes, compression ratio, and encrypted archives unless an explicit future secret-input flow is designed.

## States, Errors, And Cancellation

Normal lifecycle:

```text
queued → leased → fetching/materializing → processing → uploading → validating → succeeded
```

Side states: `retry_wait`, `cancel_requested`, `canceled`, `failed`, `superseded`, `expired`.

Error categories are stable protocol codes, not localized strings:

- permanent input/policy: `unsupported_format`, `encrypted_archive`, `input_too_large`, `materialization_disabled`, `source_changed`, `source_expired`;
- transient capacity/infrastructure: `worker_unavailable`, `provider_unavailable`, `quota_busy`, `timeout`, `worker_crash`, `lease_lost`;
- contract/security: `protocol_incompatible`, `invalid_output`, `digest_mismatch`, `sandbox_violation`, `network_violation`.

Contract/security errors quarantine the Worker and do not blind-retry. A successful but incomplete result uses `completeness=partial` plus warnings, not a fake success or generic failure.

Cancellation revokes input/output grants first, then asks the Worker to terminate the whole process tree and clear `tmpfs`. Coalesced work removes one requester interest; the underlying job is canceled only after the last interest disappears.

## Quotas, Isolation, And Scheduling

- Enforce limits across user, task, provider, capability, Worker, and global scopes: queue length, concurrency, bytes, CPU, memory, PIDs, wall time, `tmpfs`, pages/pixels/media duration, archive depth/entries/expanded bytes/ratio.
- Reserve interactive preview slots so background OCR/indexing cannot starve a user opening a file.
- Run each job non-root with a read-only root filesystem, dropped capabilities, `no-new-privileges`, cgroup/PID limits, and seccomp/AppArmor-equivalent confinement.
- Processing jobs have no Internet or DNS access and can reach only the Xirang Broker/Sink. Controlled model/signature updates run through a separate updater identity.
- Logs contain only bounded, sanitized diagnostics; no original path, content, repository details, credentials, grants, or raw tool output.

## Graceful Degradation

- No compatible Worker: show “enhanced preview not deployed”; do not generate repeated failure jobs.
- Thumbnail/OCR/conversion/transcode failure: basic directory browsing, browser-native preview, download, and restore remain available according to permissions.
- Provider lacks Range or safe materialization: show the exact limitation and fall back to metadata/download/restore rather than pretending playback exists.
- Scan missing or stale: show `not_scanned`/`scan_stale`, never “safe.”
- Worker failure never changes `RecoveryPoint.committed`, backup health, verification/drill evidence, or retention deadline.

## Decisions Carried Forward

1. Workers are optional pull-based capability executors behind a Xirang-owned Content Broker and Artifact Sink.
2. Workers never receive provider credentials or write Catalog state directly.
3. Work is content/version/pipeline keyed, lease-fenced, idempotent, resource-bounded, and sandboxed without network access.
4. Derived outputs inherit source authorization, sensitivity, encryption, lifecycle, and purge.
5. Worker availability and failures degrade individual preview capabilities only; they do not enter the backup credibility chain.
6. No additional user decision is required for this protocol. Remote processing remains disabled unless an administrator explicitly configures a trusted mTLS Worker domain.
