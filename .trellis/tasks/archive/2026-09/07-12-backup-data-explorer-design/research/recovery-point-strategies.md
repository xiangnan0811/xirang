# Recovery-Point Strategies For Rsync, Restic, And Rclone

- **Date verified:** 2026-07-12
- **Scope:** Recovery-point semantics, publication guarantees, storage constraints, retention, and migration for Xirang's three file backup executors.
- **Method:** Current repository behavior was compared with upstream Rsync/Rclone documentation, Linux filesystem semantics, and object-storage versioning documentation. This is design research only; no implementation is proposed for the current phase.

## Executive Conclusion

The user's choice of a unified `RecoveryPoint` is technically sound, but the entity must describe a **provable version boundary**, not merely rename every successful `TaskRun` as a snapshot.

The recommended provider mapping is:

| Provider | Recommended default recovery-point mode | Why |
|---|---|---|
| Restic | Map repository identity + full native snapshot ID captured from the producing run | Restic already owns snapshot identity and repository semantics, but a snapshot ID alone is not globally unique and current shared-repository attribution is unsafe. |
| Rsync | New empty staging tree + `--link-dest` to the previous committed tree + same-filesystem atomic publish | Produces independently browsable point-in-time directory trees while reusing unchanged file data. |
| Rclone | A unique, never-reused object prefix per recovery point, published by a commit manifest after remote verification | Works across substantially more remotes than backend-native object versioning and avoids reconstructing a point from a chain of deltas. |
| Existing Rsync/Rclone tasks | Keep a `mutable_head` compatibility view until explicitly migrated | Current data has no historical identity and must not be relabeled as an immutable past version. |

Rclone `--backup-dir` and backend-native object versioning are useful secondary mechanisms, but neither is a safe universal definition of `RecoveryPoint`:

- `--backup-dir` stores the **old objects displaced by a sync**, not a self-contained post-run tree. It does not identify objects first added by later runs, so reverse overlays alone cannot reconstruct arbitrary states; a complete per-run manifest/change set and an unbroken delta chain would still be required.
- Native object versioning is provider-specific. It exposes object-level versions and delete markers, not automatically a transactionally committed version of a whole directory tree. It is suitable only when a provider adapter can record stable object version IDs, validate retention, and reconstruct the exact run boundary.

The durable invariant should therefore be:

> A recovery point becomes browsable/restorable only after its provider artifact is complete, a manifest/index has been produced and minimally verified, and a stable provider locator plus manifest digest have been committed. Failed or interrupted attempts remain invisible staging artifacts.

## Current Xirang Baseline

### Rsync is a mutable destination today

- `RsyncExecutor.Run` invokes `rsync -avz --info=progress2 ... source target` (`backend/internal/task/executor/executor.go:131-143`, `:235-242`).
- It does not create a per-run directory, use `--link-dest`, or publish a version marker.
- It also does **not** pass `--delete`. Consequently the current target is a mutable accumulated directory, and source-side deletions can remain in the target. Calling it an exact mirror would be inaccurate.
- The normal target is a local path prepared by the Xirang process, which makes local filesystem snapshots practical (`backend/internal/task/executor/executor.go:136-141`).
- `-a` preserves `-rlptgoD`, but upstream Rsync explicitly states that it does not include ACLs, extended attributes, access/create times, or source hard-link topology. Versioning must not silently imply higher backup fidelity than the configured transfer flags provide.

### Rclone is a destructive current mirror today

- Rclone executes on the managed node through SSH to preserve the agentless model (`backend/internal/task/executor/rclone_executor.go:26-31`, `:47-75`).
- The generated command is `rclone sync source remote`, with only bandwidth and transfer concurrency configuration (`backend/internal/task/executor/rclone_executor.go:20-24`, `:203-218`).
- Upstream Rclone defines `sync` as making destination equal to source, including deletion when necessary. The current remote is therefore a mutable head, not a historical version.
- The UI currently exposes only source, remote path, bandwidth limit, and transfers (`web/src/components/task-create-dialog.advanced.tsx:82-142`). No version/retention semantics are configured.

### Task runs have no artifact identity

`TaskRun` records timing, status, verification, throughput, and errors, but no repository locator, snapshot ID, manifest, or recovery-point relation (`backend/internal/model/task.go:66-84`). A successful row alone cannot prove that the bytes still exist or identify which bytes were produced.

### Existing Restic attribution and append-only labels are insufficient

- `ResticExecutor.ListSnapshots` lists the whole repository without a task-specific tag/path filter, while the current `snapshot.BuildIndex` stamps every returned snapshot with the task that initiated indexing. Because Xirang does not require one repository per task, a shared repository can cause another task's snapshots to be indexed and exposed under the wrong owner. The generalized Catalog must not migrate these rows as authoritative lineage.
- The current Restic runner discards the final `backup --json` summary and does not persist the full snapshot ID. Inferring “latest snapshot” after completion is race-prone when another task or external writer shares the repository. Each run needs a unique Xirang task/run tag and must capture its own full snapshot ID from command evidence.
- The current `append_only` option only selects/checks Restic repository format version 2; it does not prove append-only backend credentials or storage policy. The UI must not translate this flag into an immutability or WORM claim.

## Unified Recovery-Point Contract

The final design should distinguish the domain record from its provider-specific representation.

### Required identity and lineage

Each committed point needs at least:

- stable opaque `RecoveryPoint` ID independent of timestamps and paths;
- task and producing `TaskRun` relationship, except an explicitly labeled imported legacy baseline;
- provider kind and non-secret provider locator;
- version semantics such as `native_snapshot`, `hardlink_tree`, `versioned_prefix`, `native_object_versions`, `imported_baseline`, or `mutable_head`;
- capture start/end time, committed time, and source consistency statement;
- state (`preparing`, `verifying`, `committed`, `degraded`, `expiring`, `expired`, `failed`);
- mutability and enforcement level (`xirang_managed`, `backend_versioned`, `storage_worm`), rather than one misleading immutable boolean;
- manifest/index state, object/file count, logical bytes, manifest digest, and verification evidence;
- retention deadline/hold state and physical availability;
- capability snapshot for list, search, sequential read, range read, download, restore, and diff.
- effective comparison/fidelity profile, including whether size/mtime quick checks, content hashes, ACLs, xattrs, hard links, symlinks, empty directories, and special files were preserved or verified.

`TaskRun → RecoveryPoint → manifest/index → verification/drill evidence → preview/download/restore audit` becomes the traceable chain. A task run may succeed operationally while point publication fails; that must be visible as “backup transfer succeeded, asset catalog unavailable/uncommitted,” not silently promoted to a trusted point.

### Publication boundary

The system should never use “command exited with 0” as the sole publication event. Provider adapters should follow a common lifecycle:

1. Allocate an opaque point ID and persist `preparing` before transfer.
2. Write only to a provider-specific staging location that can never be browsed as committed data.
3. Complete transfer, enumerate the resulting tree, create a manifest/index, and run the configured minimum verification.
4. Publish provider evidence: atomic rename for a local tree, or an immutable commit-manifest object for object storage.
5. Mark the database record `committed` and expose it to browse/restore queries.
6. Reconcile crashes between provider publication and database commit by the stable point ID; never infer completion from directory/object age.

The point represents a capture interval unless the source itself was quiesced or snapshotted. Xirang should show `capture_started_at` and `capture_finished_at` and must not claim that an active multi-file source was transactionally frozen.

## Rsync Recovery Points

### Strategy comparison

| Strategy | Browse/restore simplicity | Storage efficiency | Portability | Trust limitations | Recommendation |
|---|---:|---:|---:|---|---|
| Continue updating one directory | High for current data | High | High | Mutable; no history; stale deleted files with current flags | Compatibility only (`mutable_head`) |
| Full new copy for every point | High | Low | High | Capacity and transfer cost scale with full logical size | Fallback when linking is unsupported |
| `--link-dest` hard-link tree | High | High for unchanged files | Local hard-link filesystems | Application-managed immutability; same-mount and link-count constraints | **Default for local Rsync targets** |
| Btrfs/ZFS/LVM/native snapshots | High | High | Low | Requires host/filesystem-specific lifecycle and privileges | Optional future provider capability, not baseline |

### Why `--link-dest` fits Xirang

Rsync documents that `--link-dest=DIR` hard-links unchanged regular files from the reference tree into a new destination. The files must match in all preserved attributes; otherwise Rsync copies them. Rsync also recommends an empty destination hierarchy because changing attributes of existing destination files can affect alternate trees through hard links.

This maps cleanly to a layout conceptually equivalent to:

```text
<task repository>/
  staging/<attempt-id>/data/          # never visible
  recovery-points/<point-id>/data/    # committed, never updated
  recovery-points/<point-id>/manifest
```

A run writes a brand-new staging tree and references one pinned prior committed `data/` tree through `--link-dest`. A source file absent in the new run is simply absent from the new empty tree, so a destructive `--delete` against a prior committed point is neither required nor allowed.

### Correctness rules

- **Same mounted filesystem:** Linux hard links fail with `EXDEV` across mounted filesystems, even when the same filesystem appears at multiple mount points. Preflight must perform an actual hard-link probe between the selected prior point and staging path, not only compare path strings.
- **Atomic local publication:** staging and final directories must share a mount. A same-filesystem rename provides atomic name replacement/publication; cross-mount rename fails with `EXDEV`. The manifest and parent directory still need deliberate durability handling and crash reconciliation.
- **Never use `--inplace`:** upstream Rsync warns that in-place updates do not break hard links, so changed bytes become visible through every linked recovery point. The provider must reject incompatible flags such as `--inplace`, `--append`, and device-writing modes for versioned trees.
- **Always target an empty attempt tree:** resuming by mutating an existing hard-linked tree can alter shared metadata. The safest retry creates a new attempt ID; interrupted staging trees are garbage-collected after no active lease references them.
- **Committed trees are write-once by Xirang:** the executor and content gateway must never open committed file paths for write. Directory ownership/ACLs and a separate read-only access path should enforce this operationally. Recursively changing modes after commit is unsafe because chmod affects a shared inode through every hard link.
- **Immutability claim must be qualified:** an external process with write access can still modify a multiply-linked inode in place. `hardlink_tree` therefore means “Xirang-managed immutable,” not storage-enforced WORM. Stronger claims require filesystem snapshots/read-only mounts or WORM storage.
- **Capacity includes metadata/inodes:** unchanged data blocks are shared, but every point still adds directory entries and directories. Link-count ceilings, inode exhaustion, and changed-file growth need preflight/monitoring. Linux reports `EMLINK` when a file reaches its filesystem link maximum.
- **Retention is reference-safe but serialized:** removing an old tree unlinks names; shared file data remains while any point references the inode. Retention must not delete a point pinned as the current run's `--link-dest`, and restore/preview readers need leases during expiration.
- **Backup fidelity is separately declared:** the existing `-a` omits `-HAX`. A point should record the effective transfer profile and warn when ACLs/xattrs/hard-link topology were not preserved. Recovery-point versioning cannot fix missing source metadata after the fact.

### Rsync commit and recovery sequence

1. Serialize runs for the same task and pin the previous committed point.
2. Preflight mount identity, hard-link support, permissions, free bytes/inodes, Rsync version, source/target non-overlap, and incompatible flags.
3. Create a unique staging tree and run Rsync into it with `--link-dest=<previous>/data` when supported; otherwise use the declared full-copy fallback.
4. Enumerate the staged result and write the manifest under the staging point. Run minimum count/size/list verification plus the configured checksum sampling or stronger check.
5. Sync the small commit metadata as required, then rename the complete point directory into its final same-filesystem location. Do not expose raw staging paths through the API.
6. Commit database lineage and make the point browsable. Startup/periodic reconciliation handles an atomically published tree whose database transaction was interrupted.
7. Release the previous-point lease and clean stale attempts with age **and** lease checks.

### Existing Rsync migration

The current target cannot be retroactively split into historical points. It should initially appear as one live `mutable_head` with explicit warnings:

- historical versions: unavailable;
- source deletion fidelity: not guaranteed, because the current command does not use `--delete`;
- producing run: unknown/not provable;
- immutability: false.

Migration should be an explicit wizard with dry-run/preflight, task pause, capacity/inode estimate, rollback location, and post-copy verification. Two safe shapes are:

1. create a new versioned repository beside the legacy target, import its current bytes as an `imported_baseline`, verify, then switch future runs; or
2. leave the legacy directory untouched and make the first newly captured source state the first historical recovery point.

The first option preserves the current target but costs time/capacity and still cannot prove which past run produced it. The second is cheaper and more truthful when the user does not need to preserve the legacy head as a named baseline. Neither option fabricates past history. Rollback switches the task back to the untouched legacy target until the versioned repository is accepted.

## Rclone Recovery Points

### Strategy comparison

| Strategy | What is physically retained | Can one point be browsed independently? | Backend assumptions | Recommendation |
|---|---|---:|---|---|
| Current `rclone sync` prefix | Latest state only | Yes, but mutable | Any writable remote | Compatibility only (`mutable_head`) |
| `sync --backup-dir=<dated path>` | Objects overwritten/deleted by that run | **No**; it is a reverse delta | Same remote plus server-side move/copy; non-overlapping paths | Optional safety net, not primary point model |
| Unique prefix per run | Complete logical tree for the run | **Yes** | Any remote that can reliably list/write/read | **Portable default** |
| Current prefix + backend object versions | Changed object generations/delete markers | Only with a provider-specific version manifest/view | Version-enabled backend, stable version APIs, protected lifecycle | Optimized capability mode only |

### Why `--backup-dir` is not a complete point

Rclone documents that `--backup-dir` moves files that *would have been overwritten or deleted* into a parallel hierarchy. It requires server-side move/copy support, the same remote as the destination, and a non-overlapping path. Reusing the same backup path/suffix can overwrite an earlier displaced object.

After run `N`, the live prefix contains state `N`, while `backup-dir/N` contains portions of state `N-1` displaced during the transition. After later runs, reconstructing state `N` requires starting from the latest head and reverse-applying every subsequent transition in the correct order. A displaced-object directory alone does not say which objects were newly added by a later run and therefore must be removed during reversal; exact reconstruction would also require complete per-run manifests/change sets. Deleting any intermediate delta can invalidate several later logical recovery points. This conflicts with independent retention, simple browsing, and provable manifests.

`--backup-dir` remains useful as a short-lived operational guard on legacy mutable mirrors, but Xirang should label it “changed/deleted object archive,” not expose each directory as a self-contained `RecoveryPoint`.

### Portable default: unique versioned prefixes

Each new Rclone point should write to a prefix that has never been committed or reused, for example:

```text
remote:configured-root/
  _xirang/staging/<attempt-id>/data/...
  _xirang/recovery-points/<point-id>/data/...
  _xirang/recovery-points/<point-id>/commit.json
```

The exact reserved namespace must be validated against source filters and hidden from the asset tree.

Object stores generally do not offer an atomic directory rename. Therefore the **commit manifest**, not prefix visibility, is the publication boundary:

1. sync source into a unique uncommitted attempt prefix;
2. list the resulting prefix and create a manifest with stable object identity/hashes where available;
3. run provider-appropriate `rclone check`/list verification;
4. publish the final point/commit marker and database lineage only after the adapter has proven reliable read-after-write/list behavior for the remote;
5. expose only points with a valid commit marker and matching manifest digest.

An implementation may use a final prefix directly and keep it invisible until `commit.json`, avoiding a potentially expensive remote rename/copy. The point ID makes a database/provider crash reconcilable. Every retry should use a fresh attempt prefix, or perform an exact `sync` only while the prefix is uncommitted; a committed prefix is never a destination again.

The portable capability gate is stricter than “Rclone can list/write/read this remote.” The adapter must verify that it can write and subsequently read a stable commit marker, enumerate the committed prefix reliably, and bind entries through hashes, object IDs, or an explicitly weaker comparison profile. Eventually consistent or metadata-weak remotes may require delayed reconciliation and must not claim a strong committed point until the evidence converges.

Rclone `--copy-dest=<previous point>` can server-side-copy unchanged files into a new destination when the remote supports it. This reduces network transfer, but it still creates a new logical object and cannot be assumed to deduplicate storage or cost. Failure or absence of server-side copy must degrade to normal upload, not change recovery-point semantics.

### Backend-native versioning

S3 and several other object stores can retain old object versions. AWS S3 assigns version IDs and creates delete markers rather than permanently deleting the prior object when versioning is enabled. Rclone's S3 adapter can list old versions (`--s3-versions`) or expose a read-only view at a time (`--s3-version-at`). These are valuable building blocks, but not a portable default:

- versioning is enabled/suspended at backend or bucket scope, outside Xirang's current task contract;
- object versions and delete markers are per-object, while a backup run changes many objects over an interval;
- lifecycle rules or `cleanup-hidden` can delete generations independently of Xirang retention;
- Rclone presents S3 versions through timestamp-injected virtual filenames and documents name-collision caveats;
- provider quirks exist: Rclone documents version-list paging incompatibility for GCS through the S3 interoperability API and flags for backends that return versions in a nonstandard order;
- old versions incur storage charges, and ordinary versioning is not WORM. S3 Object Lock/retention is a separate capability.

Consequently a `native_object_versions` adapter must preflight and continuously attest:

- versioning state is enabled and has not been suspended;
- a stable version/generation identifier can be captured for every manifest entry and delete state;
- exact run-boundary reconstruction is supported;
- lifecycle/Object Lock configuration is compatible with the Xirang retention deadline;
- browse, read, Range, restore, and expiry operations can address those stable versions;
- external writes are either excluded or represented honestly in lineage.

Only then may native versions back a committed point. Time-based views alone are convenient but weaker than an explicit version-ID manifest for evidence and long-term reconstruction.

### Rclone migration

An existing remote prefix remains a `mutable_head`. Migration preflight should inspect remote features, object count/logical bytes, estimated copy/API cost, case sensitivity, hash/modtime availability, versioning/lifecycle state, and available target namespace. Cutover must pause/fence the legacy sync and account for external writers; otherwise a server-side baseline copy and its manifest can describe different moments.

The user should be offered two truthful migrations:

1. server-side copy/sync the current remote into a unique imported baseline, verify it, then switch subsequent runs to versioned prefixes; or
2. preserve the current prefix only as a compatibility view and let the next successful run create the first historical point.

The old prefix is retained until verification and explicit cutover completion. It is never assigned to an old task run. Rollback returns the task to its former mutable-prefix configuration; newly committed points remain read-only and can be retained or expired independently.

## Restic Mapping

Restic remains the simplest adapter after its run attribution is repaired:

- the stable provider locator is a non-secret repository identity plus the full native snapshot ID, never the snapshot ID alone;
- every Xirang run adds a unique task/run tag and captures the snapshot ID from that run's final JSON summary;
- the existing Restic file index becomes one implementation of the generalized manifest/catalog contract;
- snapshot existence, index completeness, verification/drill evidence, retention, and repository availability are separate states;
- deleting/forgetting a Restic snapshot updates physical availability without erasing historical audit lineage.

The existing `SnapshotFileIndex` is only a legacy search seed: it lacks entry type, hash, manifest digest, completeness state, and `TaskRun` lineage, and a single row currently makes an entire task appear indexed. It must be rebuilt or migrated only after authoritative `RecoveryPoint` attribution; it cannot itself serve as a manifest or trust artifact.

The adapter still needs to prove which snapshot the run created. Querying “latest snapshot” later is race-prone if other writers use the same repository. Capturing the run's JSON summary and unique tags is the primary rule; before/after reconciliation is only an explicit recovery path and must remain scoped by repository identity, host/path, and tags.

## Cross-Provider Retention And Failure Rules

- Retention acts on committed recovery-point IDs, never guessed paths or timestamps.
- A point in active preview, download, restore, verification, indexing, or worker processing holds a short renewable lease; expiry first transitions to `expiring` and rejects new work.
- Physical deletion and catalog deletion are separate. Audit lineage and safe aggregate evidence may remain after bytes expire, while the UI clearly marks content unavailable.
- Provider deletion is idempotent and resumable. Partial expiry becomes `degraded/expiring`, not falsely `expired`.
- Manifest/index loss is rebuildable from committed provider data. Provider data loss is not rebuildable from the catalog.
- A failed preview/index/worker job never changes backup correctness, recovery-point commitment, or retention clocks.
- Repository credentials and full remote specifications are not stored in public locators or audit events; provider adapters resolve them from the task/credential boundary.
- WORM/object-lock claims are capability evidence, not inferred from “versioning enabled” or a read-only UI.

## Design Decisions Carried Forward

1. Keep one `RecoveryPoint` domain and provider-specific locators/capabilities; do not create separate user concepts called Rsync snapshot, Rclone folder, and Restic snapshot.
2. Model legacy mutable content explicitly as `mutable_head`, not as a fake historical run.
3. Use hard-link trees as the default Rsync versioned mode after a real same-mount/link preflight; fall back to full-copy trees when needed.
4. Use independently committed version prefixes as the portable Rclone default. Treat `--backup-dir` as a delta archive and native object versions as an advanced adapter capability.
5. Record effective backup-fidelity and immutability-enforcement levels so the UI never overstates trust.
6. Separate provider publication from database publication and add deterministic reconciliation for the dual-write window.
7. Require a manifest/index and minimum verification before a point becomes browsable/restorable.
8. Make migration opt-in, dry-runnable, reversible, and honest about the absence of historical lineage.
9. Rebuild current Restic index attribution under authoritative recovery-point identities; never trust task IDs stamped by the legacy whole-repository indexer when repositories may be shared.
10. Treat commit/publication evidence separately from transfer fidelity: default Rsync/Rclone quick comparisons may miss same-size/same-mtime changes, so the point must record exactly what was and was not verified.

## Sources

### Upstream and platform documentation

- [Rsync manual: `--link-dest`, `--inplace`, archive/metadata, and deletion semantics](https://download.samba.org/pub/rsync/rsync.1.html)
- [Rclone global options: `--backup-dir`, `--compare-dest`, `--copy-dest`, `--immutable`, and suffixes](https://rclone.org/docs/)
- [Rclone sync](https://rclone.org/commands/rclone_sync/)
- [Rclone copy](https://rclone.org/commands/rclone_copy/)
- [Rclone S3 backend: versions, point-in-time view, cleanup, and provider caveats](https://rclone.org/s3/)
- [Amazon S3 Versioning](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Versioning.html)
- [Amazon S3 Object Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)
- [Google Cloud Storage Object Versioning](https://cloud.google.com/storage/docs/object-versioning)
- [Linux `link(2)`](https://man7.org/linux/man-pages/man2/link.2.html)
- [Linux `rename(2)`](https://man7.org/linux/man-pages/man2/rename.2.html)

### Current repository evidence

- `backend/internal/task/executor/executor.go`
- `backend/internal/task/executor/rclone_executor.go`
- `backend/internal/model/task.go`
- `web/src/components/task-create-dialog.advanced.tsx`
- `deploy/allinone/Dockerfile`
