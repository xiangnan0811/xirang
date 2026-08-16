# Research: T8-B2 source namespace authority

- Query: Can the current durable Repository/provider products prove authenticated current source-node identity, independently observed current source namespace, component-boundary source/target overlap after exact node equality, and a provider-aware Restic/Rclone fail-closed boundary without schema or migration changes?
- Scope: internal
- Date: 2026-08-14

## Findings

### Verdict

**BLOCKED.** The current graph can prove exact managed-Rsync recovery-point access and can fail Restic/Rclone closed, but it cannot prove the required current source-node/namespace authority. The proposed namespace product is derived from the current `Task.RsyncSource` string and caller-supplied target digest, then compared for digest equality. That is durable correlation, not an independently authenticated namespace observation, and it misses ancestor/descendant overlap.

No schema or migration change is inherently required. The missing owning product is a purpose-exact, current source-node namespace observer/session boundary (Node/SSH/provider runtime ownership) that returns a private sealed observation bound to the current durable node and credential revisions and to a remotely canonicalized, component-inspected source root. Repository can compose that observation with its existing pinned recovery-point source, but Repository cannot manufacture it from Task, link, point, locator, or digest fields.

### What the existing products prove

1. **Exact managed-Rsync recovery-point access is reusable.** `provider.RsyncRestoreSourceResolver` accepts only a scalar ref and exposes an opaque declared-entry capability, not a root or locator (`backend/internal/backupasset/provider/contracts.go:252-267`). Repository loads and locks the exact plan/point/catalog/manifest/selection tuple (`backend/internal/backupasset/repository/query.go:967-1017`), opens the committed managed tree and every declared file (`query.go:852-881`), then performs a second durable snapshot comparison and pinned-tree revalidation (`query.go:776-792`, `query.go:918-945`). The pinned Linux tree retains descriptors and verifies that the current root/tree identities still equal the opened identities (`backend/internal/fileaccess/local_linux.go:65-103`, `local_linux.go:143-183`). This proves current access to the exact repository-resident recovery source; it does **not** prove the original source-node namespace.

2. **The durable managed binding provides relational provenance, not current node authentication.** `managedRsyncBindingDocumentV2` durably binds Task ID, Node ID, repository/link IDs, managed repository root identity, marker and preflight (`backend/internal/backupasset/repository/binding.go:45-66`). Loading a runtime requires the active Task/link/repository/binding association and recomputed repository identity (`backend/internal/backupasset/repository/rsync_publication_execution.go:357-422`). A committed point is also tied to its producing Task and lineage (`backend/internal/backupasset/repository/query.go:1485-1533`). These checks make substitution detectable. They do not open/authenticate the current source node, bind a current source-node revision, or observe its current namespace.

3. **`RsyncTreePreflightEvidence.SourceIdentityDigest` is real filesystem evidence but cannot satisfy T8-B2.** Preflight opens a local source directory and derives the digest from observed device/inode/mount ID (`backend/internal/backupasset/provider/rsync_tree_linux.go:708-740`); this is stronger than a path digest. However, the evidence is stored only in a process-local, expiring activation record (`backend/internal/backupasset/repository/rsync_versioning.go:54-69`) and `managedRsyncBindingDocumentV2` does not retain it (`binding.go:48-66`). It is generated from a local filesystem path while managed publication may use an authenticated remote source when `Task.Node.Host` is set (`backend/internal/task/executor/rsync_publication_executor.go:95-107`). It also carries physical identity, not the private canonical path components needed for source/target ancestor comparison. Therefore it may remain an activation safety check but cannot be promoted into current source namespace authority.

4. **`ManagedRootIdentityDigest` and source fingerprints concern the repository root, not the original source namespace.** The managed-root identity is derived from repository marker plus managed-root device/inode/mount (`backend/internal/backupasset/provider/rsync_preflight.go:111-125`, `rsync_preflight.go:206-213`) and is persisted in the managed binding (`binding.go:57-60`). `managedRsyncSourceFingerprint` is an HMAC over repository ID, managed-root identity and point ID (`backend/internal/backupasset/repository/rsync_publication_execution.go:1391-1396`). Both are valuable recovery-point access evidence; neither identifies or authorizes `Task.RsyncSource` on the producing node.

5. **The proposed `RecoverySourceNamespaceIdentity` is a path-derived digest and cannot prove authority.** It cleans a supplied absolute path and hashes `(nodeID, clean path)` (`backend/internal/backupasset/provider/contracts.go:295-312`). Repository populates it directly from `runtime.task.NodeID` and `runtime.task.RsyncSource` (`backend/internal/backupasset/repository/query.go:893-915`). `SourceNodeID` is therefore a current row value, not a node authenticated by an external session, and `SourceNamespaceIdentity` is a durable path claim, not observed filesystem evidence.

6. **The proposed overlap check is caller echo plus equality, not component-boundary overlap.** The request carries caller-created `TargetNodeID` and `TargetNamespaceIdentity` (`backend/internal/backupasset/provider/contracts.go:269-277`). `ObserveRecoverySource` compares the target digest only when the caller's node ID equals the Task-derived source node, and only exact digest equality becomes `overlap`; a same-node ancestor, descendant, or merely different path remains `not_compared` (`backend/internal/backupasset/repository/query.go:601-650`). The test itself creates the target identity with the same public helper from fixture values (`backend/internal/backupasset/repository/query_test.go:334-376`), so it proves equality plumbing, not independent observation.

7. **Component-boundary primitives are reusable once both private canonical paths are independently observed.** `fileaccess.Contains` uses equality or `root + separator`, avoiding prefix confusion such as `/backup` versus `/backup-evil` (`backend/internal/fileaccess/path.go:85-92`; test at `path_test.go:50-60`). There is currently no `fileaccess.PathRootsOverlap`; the repository-private equivalent performs the required bidirectional component-boundary relation (`backend/internal/backupasset/repository/private_runtime_root.go:105-116`). The truthful shared operation is `Contains(source,target) || Contains(target,source)`, but it must run only after exact authenticated source/target node IDs match and only over private canonical paths returned/bound by the two observers.

8. **A target-side authenticated session seam exists, but its public facts do not currently carry namespace material.** Recovery resolves and locks the current non-archived node plus current node/credential revisions (`backend/internal/backupasset/recovery/target.go:337-398`), rechecks them when opening a purpose-exact preflight session (`target.go:1348-1377`), and probes every root prefix with `Lstat` and `RealPath`, marking the root canonical only when the remote result exactly matches (`target.go:5473-5513`). `TargetRootProbeFacts` exposes only closed booleans/revisions/capacity (`target.go:4218-4236`), not the private canonical namespace. The eligibility owner may reuse the session's already-private root locator only when `RootCanonical` is true and the same observation/binding is sealed; a locator digest alone remains insufficient.

9. **The current Node revision product is reusable for a source observer, but it is target-purpose scoped.** Runtime locks the current Node and SSH key, validates exact key material/scope, and derives node/credential revisions from their current security-bearing fields (`backend/internal/backupasset/runtime/recovery_runtime.go:63-120`). The target session binds these revisions to the remote SSH/SFTP access. T8-B2 needs an equivalent source-purpose seam; re-labeling a target preflight session or trusting `Task.NodeID` is not equivalent. Repository visibility joins through `node_owners` (`backend/internal/backupasset/repository/query.go:369-396`, `query.go:424-443`) prove operator authorization to view lineage, not machine identity or source namespace authority.

10. **The Restic/Rclone fail-closed arm is reusable.** `ObserveRecoverySource` rejects every provider other than Rsync with `backupasset.ErrCapabilityUnavailable` before resolving or opening source access (`backend/internal/backupasset/repository/query.go:601-613`). The dedicated test covers Restic and Rclone and checks empty products and redacted errors (`query_test.go:409-424`). This boundary should remain capability-exact: no generic RestorePort, Catalog, legacy locator or Rsync fallback may satisfy unsupported providers. Runtime currently constructs only a managed Rsync restore port (`backend/internal/backupasset/runtime/recovery_runtime.go:316-343`), which is consistent with fail-closed coverage.

### Smallest truthful no-schema-change contract

The minimum viable contract is transient and two-phase; it does not add durable columns:

1. Repository keeps `RsyncRestoreSourceRef` resolution, pinned declared tree ownership, exact durable tuple revalidation, and the returned opaque `RsyncRestoreSource`.
2. A new owning source-node namespace port captures current durable Task/binding/point provenance plus current Node/credential revisions in a short transaction, opens a purpose-exact authenticated source-node session outside the transaction, and independently observes the source root using remote canonicalization and component-by-component no-symlink/directory checks. Its private sealed result binds exact `NodeID`, node revision, credential revision, canonical source namespace, observation revision/time, and source binding; no raw path crosses JSON/log/audit/public DTO boundaries.
3. A second short transaction re-locks and matches Task/binding/point, Node, credential and source observation revisions before the observation is accepted. Repository pinned-tree `Revalidate` still runs before and after use as already required.
4. Target observation supplies its exact authenticated `NodeID` and a private canonical target namespace from the same purpose-exact session/binding. Compare no paths when node IDs differ. When node IDs are exactly equal, compute overlap with bidirectional component-boundary containment. Missing canonical namespace, node/credential drift, symlink ambiguity, or observer failure is unavailable; it is never `disjoint` by default.
5. The provider switch occurs before any source or target access. Only managed Rsync is admitted. Restic/Rclone return the stable capability-unavailable sentinel and cause zero target mutation.

This contract can use existing encrypted fields and current rows, so it needs no migration. It does require a real external source namespace owner. Adding another locator/path digest, persisting the activation `SourceIdentityDigest`, or hashing `Task.RsyncSource` differently would not close the authority gap.

### Files found

- `backend/internal/backupasset/provider/contracts.go` - restore source capability and proposed source-authority request/namespace digest.
- `backend/internal/backupasset/repository/query.go` - exact pinned Rsync resolver and proposed authority observation.
- `backend/internal/backupasset/repository/binding.go` - encrypted managed-Rsync durable binding.
- `backend/internal/backupasset/provider/rsync_preflight.go` and `rsync_tree_linux.go` - local activation-time filesystem identity evidence.
- `backend/internal/fileaccess/path.go` and `local_linux.go` - component-boundary containment and pinned descriptor revalidation.
- `backend/internal/backupasset/recovery/target.go` - authenticated target node/session and remote canonical-root observation.
- `backend/internal/backupasset/runtime/recovery_runtime.go` - current node/credential revision owner and managed Rsync-only restore composition.
- `backend/internal/sshutil/node_dialer.go` - purpose-scoped credential and host-key checked SSH session boundary.
- `backend/internal/model/backup_asset.go` and `backup_asset_recovery.go` - existing durable provenance/binding fields; no schema addition is needed for a transient observation.

### Related specs

- `.trellis/tasks/07-28-backup-assets-controlled-recovery/prd.md:2057-2088` requires current exact source authority, forbids locator/Catalog/request-echo fallback, and makes overlap part of the current eligibility product.
- `.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:6228-6263` requires one eligibility owner, complete products, managed-Rsync-only enablement, and explicit Restic/Rclone unavailability.
- `.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md:2538-2585` defines T8-B2/B4 expectations: pinned resolver, exact source namespace evidence, node-gated overlap, closure on every path, and no locator return.
- `.trellis/spec/backend/database-guidelines.md:113-117` requires paired migrations for schema changes; this finding avoids schema changes by keeping observation transient and sealing it into the existing eligibility flow.
- `.trellis/spec/backend/quality-guidelines.md:102-105` requires explicit denial tests for SSH/path security and full backend verification.

### External references

None. This audit is based on the checked-out Go contracts and task specifications; no external API or version choice is needed.

## Caveats / Not Found

- No `fileaccess.PathRootsOverlap` symbol exists in the inspected worktree. Only `fileaccess.Contains` and a repository-private bidirectional helper exist.
- No production `TargetRootRegistrationProbe.ObserveRecoveryTargetRoot` implementation was found; only the interface/service consumer and test fake exist. This does not change the source-side verdict, but a complete eligibility product must also ensure the target canonical namespace comes from a real production observer.
- Focused verification is currently red: `go test ./internal/backupasset/provider ./internal/backupasset/repository -run 'RecoveryRsyncSourceAuthority|RecoverySourceAuthorityRequest|PathRootOverlap' -count=1` fails because `RecoverySourceAuthorityRequest` gained target fields while its closed-shape test still expects two fields, and `query_test.go` references undefined `observeRecoveryRsyncSourceWithResolver`. Concurrent B2 work is therefore incomplete in addition to the authority design blocker.
- `sshutil.ResolveSSHHostKeyCallback` permits configuration that disables strict checking and defaults unknown-host auto-accept (`backend/internal/sshutil/ssh_auth.go:83-135`). Any product described as authenticated current node identity must define and enforce the accepted production host-key posture rather than assume every successful SSH connection proves a pre-registered machine identity.

### Non-negotiable constraints

- Never treat `Task.NodeID`, `ProducingNodeIDSnapshot`, `NodeIDSnapshot`, locator/path digests, `SourceFingerprint`, `ManagedRootIdentityDigest`, or caller-supplied namespace identity as current source-node authority.
- Never label same-node unequal digest values `disjoint`; ancestor/descendant comparison requires independently observed private canonical paths and component-boundary logic.
- Never compare namespaces before exact authenticated node identity equality.
- Never let Restic/Rclone fall through to Rsync, generic RestorePort/Catalog access, a legacy locator, or a synthetic clear result.
- Every opened pinned tree, SSH/SFTP session and source capability must close on every error path; all durable revisions must be revalidated after external observation and before authority is consumed.
