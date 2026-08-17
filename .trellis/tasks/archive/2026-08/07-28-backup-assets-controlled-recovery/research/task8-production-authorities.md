# Research: Task 8 production recovery authorities

- Query: Identify existing production code/data sources sufficient to implement each unavailable Recovery runtime authority in `backend/internal/backupasset/runtime/recovery_runtime.go`: `RecoveryPreflightExternalEvidenceAuthority`, `RecoveryAuthorityRevalidator`, and `RecoveryReconciliationRevisionSource`; determine whether default-disabled/frozen settings permit leaving them unavailable while calling Task 8 complete.
- Scope: internal
- Date: 2026-08-13

## Findings

### Runtime state

`runtime/recovery_runtime.go` now contains a runtime-owned node/credential revision source, unavailable fail-closed adapters for the three requested authorities, and graph build dependencies for all of them. The process-wide `runtime.Dependencies` still does not accept caller-controlled authority implementations; `runtime.go:637-652` wires the real node source but explicitly supplies unavailable preflight, live-revalidation, and reconciliation-revision adapters.

### 1. `RecoveryPreflightExternalEvidenceAuthority`

Required contract:

- The authority must return an observation matching all sealed request fields: plan/binding/transition, source/capability/policy/finding revisions, target root/filesystem/target revisions, and byte/inode requirements (`backend/internal/backupasset/recovery/preflight.go:132-165`, `223-237`).
- The adapter rejects any mismatch and only turns a matching result into production-proven evidence (`preflight.go:183-220`).

Existing usable sources, but only partial:

- `SourceValidator.RevalidatePlanTx` provides a real fail-closed current source check inside the caller transaction (`backend/internal/backupasset/recovery/service.go:891-948`).
- `model.BackupRepository.CapabilityRevision` and `model.RecoveryPoint.CapabilityRevision` are persisted capability fields.
- The target side independently produces `TargetRootRevision`, `TargetFilesystemRevision`, and `TargetRevision` before forming the external request.
- The runtime does not provide this authority: `managedRecoveryUnavailablePreflightEvidenceAuthority` always returns `ErrTargetPreflightUnavailable` (`backend/internal/backupasset/runtime/recovery_runtime.go:185-199`).

Fail-closed gaps:

- No production implementation exists for `ObserveRecoveryPreflightEvidence`.
- No existing recovery policy/finding product yields current `SecurityPolicyRevision`, `FindingSetDigest`, and `SecurityFindingDisposition`.
- No source establishes `OverlapsXirangRoot`, `OverlapsSourceRoot`, `ReservedBytes`, or `ReservedInodes`.
- Returning frozen plan values or default clean/access/zero-reserve values would violate the independent-evidence contract.

### 2. `RecoveryAuthorityRevalidator`

Required contract:

- `RecoveryAuthorityBinding` carries target, source, capability, policy/security, preflight and all associated revisions (`backend/internal/backupasset/recovery/service.go:1703-1742`).

Existing usable sources, but only partial:

- `SourceValidator.RevalidatePlanTx` and `settings.Service.ResolveRecoveryTargetRootTx` provide transaction-scoped source and root checks.
- `managedRecoveryNodeRevisionSource.ResolveRecoveryNodeRevisionsTx` locks the current Node and SSHKey rows, rejects archived/non-key/password/private-key configurations, validates key material/fingerprint and exact recovery purpose/node scope, and derives domain-separated node/credential revisions from those current fields (`backend/internal/backupasset/runtime/recovery_runtime.go:55-120`). `productionRecoveryTargetNodeSessionResolver` consumes that source in its own transaction before every target session (`backend/internal/backupasset/recovery/target.go:337-399`). This closes the node/credential revision-source gap for target sessions, but is not a complete `RecoveryAuthorityRevalidator`.

Fail-closed gaps:

- No current root revision exists in the settings product.
- No current recovery policy/finding evaluator or complete preflight freshness source exists.
- Repository capability data alone does not supply the remaining binding. The runtime adapter is intentionally unavailable and returns `ErrRecoveryTargetChanged` (`backend/internal/backupasset/runtime/recovery_runtime.go:201-212`); a partial revalidator must not approve authority effects.

### 3. `RecoveryReconciliationRevisionSource`

Required contract:

- It must resolve valid current node, credential, and root revisions in one transaction (`backend/internal/backupasset/recovery/service.go:3908-3988`).

Existing usable sources, but insufficient:

- `settings.Service.ResolveRecoveryTargetRootTx` and `ListAllRecoveryTargetRoots` provide a validated root registry.
- Node/credential revisions are available from the runtime adapter described above.

Fail-closed gap:

- The settings schema has no opaque root revision. Reusing the locator digest would conflate independent bindings. The runtime's `managedRecoveryUnavailableReconciliationRevisionSource` returns `ErrRecoveryReconciliationUnavailable` (`backend/internal/backupasset/runtime/recovery_runtime.go:214-226`), so reconciliation remains blocked/unavailable rather than claiming clear.

### Default-disabled and Task 8 completion

The worktree has a separate `backup_assets.recovery.enabled` setting, default `false` (`backend/internal/settings/service.go:404`; parsed by `backend/internal/backupasset/service.go:785-790`). In `buildManagedRecoveryGraph`, `config.Enabled == false` returns a graph whose metadata reconciliation is a no-op and whose `Run` starts cleanup only (`backend/internal/backupasset/runtime/recovery_runtime.go:784-790`); it does not run logical Recovery reconciliation. Thus default-disabled admission safely prevents mutation, but the current implementation cannot honestly claim the design's required disabled-mode reconciliation continuity (`.trellis/.../design.md:1196-1212`, `1452-1460`). Leaving authorities unavailable is fail-closed and incomplete, not Task 8 completion.

## Caveats / Not Found

- No non-test production implementation of the three named interfaces exists.
- No durable target-root revision, recovery security finding product, overlap source, or reserve source exists.
