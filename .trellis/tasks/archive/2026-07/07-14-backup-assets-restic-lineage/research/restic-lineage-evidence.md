# Restic Exact Lineage Evidence

## Scope And Baseline

- Active child: `07-14-backup-assets-restic-lineage`.
- Baseline: `main@e1a8f24c3c8b8b71581cedc148c5f32482c8ac0b`.
- Completed dependencies: Child 1 domain foundation and Child 2 provider readers/repository access.
- This document records design evidence only. It does not authorize implementation or Provider mutation.

## Conclusions

1. The recommended architecture remains a provider-neutral publication coordinator around an evidence-aware executor. The Manager owns TaskRun orchestration; the publication service alone owns RecoveryPoint, manifest, lease, audit, and reconciliation transactions; the Restic execution path returns exact Provider evidence without writing asset tables.
2. `TaskRun.ID` exists before executor invocation, so Xirang can allocate a RecoveryPoint and unique tags before `restic backup` starts. No `latest`, time window, repository diff, or post-run guess is needed.
3. A successful Restic command and valid publication evidence are separate results. Exit zero with missing, malformed, duplicate, truncated, or non-final summary means transfer succeeded but publication evidence is invalid. It must not enter the existing transfer retry/failure path.
4. The Child 2 command boundary should be extended for a narrowly allowlisted Restic backup stream and exact manifest stream. Reusing the legacy shell-string executor would retain merged stdout/stderr, remote password files, unchecked scanner errors, and unsafe binary composition.
5. `000062` has two hard enum omissions and two missing uniqueness defenses. Child 3 cannot meet its exact-once/fencing contract honestly while claiming zero migrations.
6. Legacy risk is broader than `SnapshotFileIndex`: list/files/search/diff/restore, anomaly `--latest 2`, task-level `restore latest`, and untagged `forget --prune` also need a feature-gated lineage guard or fail-closed behavior.
7. Prewritten attempt tags prove ownership, not command success. Exit 3 can save the same tagged snapshot, so restart may auto-recover a preparing point only when `known_exit_zero` was already persisted; otherwise it must quarantine for reviewed import.

## Local Code Evidence

### TaskRun And Executor Boundary

- `backend/internal/task/runner.go:160-179` creates and persists the TaskRun before starting `runTask`.
- `backend/internal/task/runner.go:302-317` marks the exact run `running` before executor invocation.
- `backend/internal/task/runner.go:395-404` drops the run identity because `Executor.Run` accepts only `model.Task`.
- `backend/internal/task/executor/executor.go:69-80` defines the compatibility executor/factory contract.
- Automatic retry creates a new TaskRun while retaining the chain ID. Therefore each retry gets a new point/tag; reconciliation of one TaskRun reuses its original point/tag.

### Current Restic Evidence Loss

- `backend/internal/task/executor/restic_executor.go:120-135` constructs a shell command with `--json 2>&1` and returns only `(exitCode, error)`.
- `backend/internal/task/executor/restic_executor.go:190-248` reads the merged stream with the default 64 KiB `bufio.Scanner`, never checks `scanner.Err()`, and can return success after truncated scanning.
- `backend/internal/task/executor/restic_executor.go:250-289` parses only `status`; summary becomes a sanitized log line and is discarded. `current_files` can reach the logging path, and `percent_done` is not copied to `ProgressSample.Percent`.
- `backend/internal/task/executor/restic_executor_test.go` has no real backup-stream, summary, scanner-limit, exit-code, or evidence tests.

### Child 2 Reusable Boundary

- `backend/internal/backupasset/provider/runner.go:49-111` provides typed tool/operation/purpose validation and separate operands.
- `backend/internal/backupasset/provider/runner.go:325-451` provides bounded stdout/stderr, timeout, concurrency, private locator injection, and secret stdin.
- `backend/internal/sshutil/command_runner.go:504-543` validates binaries, denies shell binaries, and quotes each operand independently.
- `backend/internal/backupasset/provider/restic.go:119-166` requires a full 64-character lowercase snapshot ID for reads.
- `backend/internal/backupasset/provider/restic.go:313-386` is strict about malformed records, duplicate paths, native snapshot header mismatch, and scanner errors, but it buffers the full command output upstream and is a directory reader, not a complete manifest builder.
- `backend/internal/backupasset/provider/contracts.go:222-234` reserves empty publication/manifest types; `registry.go:11-18` does not yet expose a manifest builder.
- `backend/internal/sshutil/command_runner.go:430-479` currently maps stream completion to generic failure and does not expose the Restic exit status. The publication stream contract must return bounded stderr plus exact exit code only after natural stdout EOF and joined wait; early close is cancellation and cannot establish a known exit.

### Repository Runtime And Ownership

- `backend/internal/api/router.go:466-535` currently constructs Foundation, Keyring, cursor codec, transport, Provider adapters, Registry, audit, and Repository service privately inside API wiring.
- `backend/cmd/server/main.go:173-176` separately constructs Task executors and Manager before the API runtime exists.
- Child 3 needs one shared backup-asset runtime constructed in process bootstrap and injected into both API and Task Manager. Creating a second transport would split concurrency, identity, limits, and test seams.
- `backend/internal/backupasset/repository/binding.go` is the only allowed Task-executor-to-Provider mapping boundary and should resolve the active publication binding.

### Frozen Domain And Schema Facts

- `backend/internal/backupasset/domain.go:119-125` and both `000062` migrations define manifest completeness as `complete | partial | unavailable`. Failure belongs to RecoveryPoint state, not a `failed` manifest value.
- `backend/internal/model/backup_asset.go:78-155` already stores Task/TaskRun lineage, encrypted Provider locator, source fingerprint, manifest digest/counts/fidelity, and encrypted commit evidence.
- `RecoveryPointLineageSummary` must copy immutable run facts because TaskRun retention later deletes old rows and `ON DELETE SET NULL` clears the FK.
- `backend/internal/backupasset/domain.go:340-370` maps Restic to native snapshot semantics regardless of TaskPublicationMode, masking a persistence mismatch.
- `backend/internal/backupasset/repository/connect.go:354-360` currently persists Restic Task links as `native_object_versions`, a Rclone-specific meaning.
- `backend/internal/database/migrations/{sqlite,postgres}/000062_backup_asset_foundation.up.sql` does not allow `native_snapshot` in `task_repository_links.publication_mode`.
- The same migration and `backend/internal/backupasset/domain.go:148-157` do not allow a publication lease holder.
- The only immutable RecoveryPoint indexes are non-unique Task/TaskRun lookup indexes. Nothing prevents two points for one TaskRun or two TaskRuns claiming one native snapshot.

### Existing Lease Capability

- `backend/internal/backupasset/lease.go:121-225` renews/releases/validates a fence using lease, point, holder, owner, attempt, token, short expiry, and absolute deadline.
- `backend/internal/backupasset/lease.go:242-306` takeover rotates attempt and token, invalidating the old fence.
- A fresh `LeaseService.Acquire` currently calculates a new absolute deadline from the acquire time. Publication crosses an execution stage and an asynchronous manifest stage, so blindly releasing and re-acquiring would extend one point forever. Child 3 must pin one point-wide deadline in immutable lineage and pass it to every fresh publication lease; takeover preserves the current row deadline.
- Because a lease references an existing RecoveryPoint, `Prepare` must first create/load the deterministic `preparing` point, then acquire the `point_publication` lease.
- A standalone `ValidateFence` followed by a different publication transaction has a TOCTOU gap. Manifest activation and `verifying -> committed` must lock/validate the lease in the same transaction or use one conditional update whose predicate includes the live fence.
- `RecoveryPoint` has an encrypted Provider locator but no encrypted commit-evidence column; `RecoveryPointManifest` owns `EncryptedCommitEvidence`. A verifying point must persist a versioned encrypted exact locator plus safe summary facts (capture interval and checked counts), identity/tag/capability digests, and a canonical evidence digest in versioned `ConsistencyJSON`. After restart the worker reconstructs and digest-verifies the original envelope, then the complete-manifest transaction persists it with observed-tag attestation. No extra point column is required.

### Legacy Shared-Repository Hazards

- `backend/internal/task/executor/restic_executor.go:309-350` lists every snapshot in a repository.
- `backend/internal/snapshot/indexer.go:133-161,216-228` enumerates all of them and stamps every entry with the requesting Task ID.
- `SnapshotFileIndex` is keyed by `(task_id, snapshot_id, path)`, so two Tasks can each claim the same shared-repository snapshot without conflict.
- Old snapshot list/files/search/diff/restore handlers accept repository-wide results or short IDs without proving Task lineage.
- `backend/internal/anomaly/snapshot_diff.go:332-400` runs repository-level `snapshots --latest 2` and writes the result as the current Task/TaskRun history.
- `backend/internal/task/executor/restic_executor.go:175-186` implements task-level `restore latest`.
- `backend/internal/task/retention.go:119-170` runs repository-level `forget ... --prune` without Task tag filtering. Once a repository is shared, this can delete another Task's point.

## Upstream Restic Evidence

Evidence was verified against the latest stable release available on 2026-07-14, Restic v0.19.1, published 2026-07-05:

- Release: <https://github.com/restic/restic/releases/tag/v0.19.1>
- Scripting/JSON contract: <https://github.com/restic/restic/blob/v0.19.1/doc/075_scripting.rst>
- Backup JSON implementation: <https://github.com/restic/restic/blob/v0.19.1/internal/ui/backup/json.go>
- Persisted snapshot/summary shape: <https://github.com/restic/restic/blob/v0.19.1/internal/data/snapshot.go>
- Backup tags: <https://github.com/restic/restic/blob/v0.19.1/doc/040_backup.rst>
- Snapshot tag filtering: <https://github.com/restic/restic/blob/v0.19.1/doc/man/restic-snapshots.1>
- Tag matching implementation: <https://github.com/restic/restic/blob/v0.19.1/internal/data/snapshot.go>
- Tag rewrite semantics: <https://github.com/restic/restic/blob/v0.19.1/cmd/restic/cmd_tag.go>
- `ls --json` shape: <https://github.com/restic/restic/blob/v0.19.1/cmd/restic/cmd_ls.go>
- Tree/walker ordering: <https://github.com/restic/restic/blob/v0.19.1/internal/data/tree.go> and <https://github.com/restic/restic/blob/v0.19.1/internal/walker/walker.go>

Confirmed upstream behavior:

- `backup --json` uses JSON Lines. A successful summary is the final stdout record and includes full `snapshot_id`, `backup_start`, `backup_end`, file counts, and byte counts.
- Restic also persists `Snapshot.Summary` inside the saved snapshot object. In v0.19.1 `internal/data/snapshot.go`, that typed summary contains `backup_start`, `backup_end`, the same file/directory counters, and `total_bytes_processed`; `snapshots --json` serializes the snapshot object plus its full ID. Therefore a durable Xirang `known_exit_zero` marker plus one exact-tag snapshot can reconstruct canonical commit evidence from Provider-stored metadata even when the stdout summary was missing or malformed. A nil/invalid stored summary still fails closed.
- The successful summary does not echo the snapshot tag set. The execution attempt proves which allowlisted tag operands were requested, while the exact `ls` snapshot header or reconciliation lookup must independently observe the exact two-marker multiset and absent `original` before commit.
- JSON is backward-compatible, but new fields and message types may be added. Known records should tolerate unknown fields; unknown backup progress message types before summary may be ignored safely.
- Exit code zero is success. Exit code 3 means some source data could not be read and must not publish a trusted point, even if a summary was emitted. Unknown future non-zero codes are failures.
- Fatal JSON errors are written to stderr. Stdout and stderr must remain separate; stderr/path payloads never become lineage evidence.
- Backup accepts multiple `--tag` operands. Snapshot filters use one comma-separated tag list for AND; repeated tag filters are OR. Generated tag values must therefore be fixed ASCII and must reject commas.
- `restic tag` rewrites snapshot metadata under a new native ID, retains the original `Snapshot.Summary`, records the first ID in `Snapshot.Original`, and removes the previous snapshot object. Because the asset backup supplies exactly two generated tags and accepts no user tags, automatic attribution must require the raw tag multiset to equal those two values and `original` to be absent; accepting tag supersets could claim a rewritten ID the backup command never returned.
- `ls --json` emits one snapshot header followed by node records. Node JSON exposes path, name, type, uid, gid, optional file size/mode, mtime/atime/ctime, and inode; it does not expose all Restic tree metadata such as link target, xattrs, or content IDs. The manifest fidelity profile must state this honestly.
- Restic stores each directory's nodes in strictly increasing sibling-name order and `ls` walks them depth-first. That traversal is deterministic but is **not** globally lexicographic by full path (for example, descendants of `/a` may precede sibling `/a-`). A correct streaming validator tracks directory frames and sibling order in O(depth) bounded memory instead of requiring every full path to increase globally.

## Recommended Contracts

### Tag Codec

Use opaque, fixed-grammar tags with no user-controlled text:

```text
xirang.link.v1.<32-lower-hex-task-repository-link-id>
xirang.point.v1.<32-lower-hex-recovery-point-id>
```

- Both are separate `backup --tag` operands.
- Reconciliation queries one AND list: `--tag <link-tag>,<point-tag>`.
- Tags contain no Task name, node name, path, repository locator/identity, credential, or raw auto-increment TaskRun ID.
- RecoveryPoint ID is deterministically derived as the first 16 bytes of SHA-256 over a versioned domain separator, installation-stable TaskRepositoryLink ID, and decimal TaskRun ID. Concurrent prepare calls therefore converge on the same primary key and tags.

### Evidence-Aware Execution

Keep `Executor.Run` unchanged for non-asset and pristine feature-disabled compatibility; a Repository with managed publication history must be blocked before legacy fallback. Add an optional typed extension conceptually equivalent to:

```go
type EvidenceExecutionRequest struct {
    Task      model.Task
    TaskRunID uint
    Attempt   provider.PublicationAttempt
}

type EvidenceExecutionResult struct {
    ExitCode       int
    Completion     backupasset.ProviderCompletionClass
    ProviderCommit *provider.ProviderCommitEvidence
    EvidenceCode   backupasset.PublicationFailureCode
}
```

- The outer error channel is only transfer/transport timeout, cancellation, non-zero exit, or runner failure. Typed completion is exactly `known_exit_zero | known_nonzero | outcome_unknown`; timeout/cancel/hard limit/read-close-wait uncertainty uses the last class and suppresses automatic transfer retry until reconciliation/manual action.
- Exit zero with invalid evidence returns no outer error, an empty commit, and a stable evidence failure code.
- Manager preserves the existing transfer result and asks the coordinator to defer with typed `{completion, code}` or fail separately. A valid full commit is always retried/read back through the same idempotent `RecordProviderCommit`; it is never downgraded to a different outcome after an unconfirmed DB response.

### Backup Parser

- Parse stdout as bounded, newline-terminated JSON records; keep stderr separately bounded and sanitized.
- Known `status` records validate numeric types, emit only safe percent/throughput, and discard `current_files`.
- Known `verbose_status` and `error` payloads contribute only safe counters; never log item/path/message.
- Ignore unknown message types before summary for forward compatibility.
- Require exactly one `summary`, `dry_run=false`, full lowercase 64-hex `snapshot_id`, non-zero/ordered RFC3339/RFC3339Nano capture timestamps with any legal offset, UTC normalization, and summary as the final nonblank record.
- A semantic parser defect must be remembered while stdout continues bounded draining to natural EOF; explicit early `Close` would cancel the command and cannot prove transfer success. Only joined exit zero returns an evidence code. Non-zero (including 3), timeout/cancel, read/close/wait failure, or a hard total-output limit overrides parser state as a transfer/lifecycle error.

### Exact Manifest

- Re-probe and bind native repository identity before enumeration.
- Stream only `restic ls --json --recursive -- <full-id> /` through the Child 2 runner.
- Require exactly one matching snapshot header whose raw tag multiset equals the two expected link/point markers, `original` is absent, and snapshot time equals normalized backup start. Extra/missing/duplicate tags or non-null `original` are `provider_snapshot_rewritten`, not compatible metadata.
- Strictly parse every node. Accept unknown JSON fields but reject malformed/unknown record or node type, duplicate/non-canonical path, negative/overflowing values, limit breach, truncated record, command failure, or locator/identity drift.
- Validate Restic's canonical depth-first pre-order with a bounded directory stack initialized at `/`: before each record, pop completed frames until top equals its parent; validate and advance that frame's sibling name; then push a directory record. A popped directory cannot be re-entered. This preserves streaming behavior without the incorrect global-path-order assumption.
- Hash a versioned length-delimited binary representation in that validated traversal order, not raw JSON. A complete manifest uses domain `xirang.restic.manifest.complete.v1`, a prelude binding provider/full ID/tag/traversal profile, per-node path/type/size-presence/size/mode/uid/gid/mtime/atime/ctime/inode, then checked-count trailer. An inactive partial uses disjoint domain `xirang.restic.manifest.partial.v1`, the same accepted prefix, and a mandatory terminator containing stable failure category plus prefix counts; it can never collide with a shorter complete manifest. Canonical slash cleanup preserves exact UTF-8 code points and performs no Unicode normalization or case folding.
- Count all nodes; sum logical bytes only for regular files with checked arithmetic.
- Store `sha256`, generator `xirang-restic-ls`, generator/schema version, digest, counts, `complete`, and an explicit `restic_ls_json_v1` fidelity profile. Raw paths and filenames remain transient.
- Use dedicated configurable streaming limits rather than the Child 2 interactive 16 MiB/100k listing defaults. Limits stay finite: a trustworthy parsed prefix may become an inactive domain-separated `partial` diagnostic, otherwise the diagnostic is inactive `unavailable` with empty digest/zero counts; neither may become active or committed.

### Publication State Machine

1. `Prepare`: every Restic command path first acquires the same generation token before its safety decision and keeps it until command/read-handle close/join. In pristine-disabled mode, backup performs only a side-effect-free managed-history check and returns no attempt; a latched or unlinked/ambiguous Restic Task returns `legacy_fallback_blocked`. If enabled for Restic, require an active exact Repository link, create/load one deterministic `preparing` point, copy immutable Task/Run/link summaries, and acquire the `point_publication` lease. Enable/disable/first-point/downgrade/down close admission and drain all backup/read/restore/anomaly/retention/publication tokens before changing generation.
2. `Capture`: execute the allowlisted backup with exact tags and return the final-summary commit only.
3. `RecordProviderCommit`: on exit-zero exact evidence, use a short non-cancelled DB cleanup context to persist the versioned encrypted exact locator, safe summary facts, canonical evidence/identity/tag digests, and source fingerprint; advance only `preparing -> verifying`; atomically release the execution-stage lease; enqueue only the opaque point ID; and return without scanning the manifest or waiting for `committed`. An unconfirmed result is retried/read back with byte-equivalent evidence as this same operation; it is never converted to `Defer` merely because the database response was transient.
4. `Publication worker`: asynchronously acquire a fresh manifest-stage fence under the same holder/owner slot and original point-wide deadline, reconstruct/digest-check the original commit envelope, build and verify the exact manifest outside the database transaction, then transactionally revalidate the fence/evidence, persist encrypted commit plus observed-tag attestation on the new manifest revision, activate only a complete revision, and advance `verifying -> committed`. TaskRun completion proceeds independently.
5. `Defer` / `Fail`: deterministic protocol/identity/tag ambiguity becomes `failed`; cancellation, timeout, or transient Provider outage remains `preparing/verifying` for bounded reconciliation. Execution `Defer` takes typed `{completion, code}`: a proven exit-zero evidence defect persists only `known_exit_zero` plus the safe defect code, while timeout/cancel/hard-limit/transport uncertainty persists `outcome_unknown`. It never blesses invalid stdout as commit evidence. Publication failure never changes an already successful transfer into a transfer failure. Fixed-deadline termination uses the separate conditional `ExpireAtDeadline` path after every lease is invalid.
6. `Reconcile`: acquire the same publication owner slot or take over an expired live lease. Zero exact-tag matches remains pending; the missing grace raises safe telemetry but does not terminally fail a possibly late remote snapshot. One exact full-ID match continues verification **only** with durable `known_exit_zero`, exact two-tag multiset, absent `original`, matching snapshot time, and a valid Provider-stored `Snapshot.Summary`; reconciliation constructs the canonical envelope from that typed snapshot metadata, not the rejected stdout. A tag-only, outcome-unknown, marker-absent, or stored-summary-invalid match is claimed/quarantined or failed closed as appropriate; any metadata rewrite is `provider_snapshot_rewritten`. More than one, identity drift, wrong tags, or native-ID conflict fails closed. Only the fixed point-wide deadline permits missing/deadline terminalization. Never run `latest`, `forget`, `prune`, `delete`, `restore`, or `init`.

### Legacy Isolation

- `backup_assets.enabled=false` with no publication-origin RecoveryPoint: preserve the current legacy backup and UI/API path exactly and create no asset rows/tags.
- Any Child-3-codec native RecoveryPoint or retained lifecycle tombstone permanently trips the managed-history latch, regardless of point state or nullable Task/TaskRun FK. Disabling the feature then stops new preparation/worker work but leaves untagged legacy Restic backup fallback, cross-Task reads, repository-wide anomaly selection, `restore latest`, and untagged retention blocked for that Repository. If installation history exists and an unlinked/stale Task cannot prove a different Repository binding, it also fails closed. A behavioral rollback cannot create untracked snapshots or reopen a destructive path merely by flipping the global setting.
- `backup_assets.enabled=true`: Restic backup must use the exact-evidence lane. Missing Repository link fails before Provider mutation rather than creating an untraceable point.
- The legacy index is never an input to RecoveryPoint, manifest, Catalog, anomaly, retention, or reconciliation.
- Old task-scoped list/files/search/diff/snapshot restore is filtered through committed RecoveryPoints for that Task. Short prefixes resolve only inside that proven set.
- Task-level `restore latest` and untagged repository-wide retention fail closed for asset-managed Restic repositories. The later controlled-recovery and lifecycle children replace these paths.

## Schema Review

Recommended paired migration: `000063_backup_asset_publication_contract`.

Required changes:

1. Add `native_snapshot` to `TaskPublicationMode` and both `task_repository_links.publication_mode` checks.
2. Convert existing Restic links from the incorrect `native_object_versions` value to `native_snapshot` using the linked Repository provider kind.
3. Add `point_publication` to `LeaseHolderType` and both lease checks.
4. Add a partial unique index for every non-null `producing_task_run_id`; the all-semantics scope is intentional because one TaskRun may name at most one point, while mutable heads normally keep this FK null.
5. Add a partial unique index for `(repository_id, source_fingerprint)` where semantics is `native_snapshot` and the fingerprint is non-empty.
6. Extend the dual-engine apply/down integration harness. Down requires an exclusive all-Restic-command drain, no active publication lease, and no any-state/FK-null Child-3 point or retained tombstone; application rollback after use keeps additive schema and Provider snapshots.

The user approved this repair on 2026-07-14. The parent reservation has therefore been renumbered atomically from `000063…000069` to `000064…000070`.

Alternatives:

- Keep zero migration and reuse `native_object_versions` plus deterministic IDs/CAS: smallest diff, but persists a false Provider meaning, lacks a truthful publication lease type, and leaves exact-once integrity to service code. Rejected.
- Postpone Child 3 until a separate foundation-repair child: clean process separation, but delays the next dependency and creates an unnecessary extra PR for a contract only Child 3 can fully test. Not recommended.
- Add the focused `000063` in Child 3: minimal honest schema repair, keeps implementation and its dual-engine behavioral tests together, and preserves all Provider data on rollback. Recommended.

## Planning Implications

- The parent Child 3 file list and evidence signature have been corrected after schema approval.
- The parent now uses `ManifestUnavailable`, moves RecoveryPoint transaction ownership out of Manager, adds shared runtime wiring, and expands legacy safety to handlers/anomaly/retention.
- No implementation should start until the focused `design.md`, `implement.md`, curated JSONL context, parent reservation update, and explicit `task.py start` review all complete.
