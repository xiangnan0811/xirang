# Task 8 production-authorities and runtime audit

- Scope: audit the current Task 8 graph against the PRD, design, and implementation contracts for production authority, disabled reconciliation, lifecycle ownership, receipt retention, and downgrade readiness.
- Date: 2026-08-13
- Evidence rule: a value is authoritative only when production code obtains and validates the current owning product at the decision boundary. Frozen plan fields, timestamps, digests of a different binding, non-nil adapters that always return unavailable, and clean/zero defaults are not current authority.

## Executive conclusion

The lifecycle shell is substantially implemented, but the production Recovery graph is not ready for enabled publication or Task 8 completion. Production composition installs explicit unavailable adapters for live preflight evidence, full authority revalidation, and reconciliation revisions, yet `buildManagedRecoveryGraph` accepts those non-nil adapters and returns `admissionEnabled: true`. The graph is then published after only permanent-cleanup-key reconciliation. Enabled publication must fail closed at graph build/startup until the real authorities exist; deferring failure until preflight, claim, or reconciliation use advertises an enabled product whose required production evidence cannot succeed.

Default-disabled startup is mutation-closed but also incomplete: it publishes a maintenance graph whose metadata reconciliation is a no-op and whose only long-running graph owner is cleanup. It therefore violates the explicit default-disabled requirement that logical Recovery reconciliation remain active.

## Severity-ordered blockers

### 1. Critical: enabled publication accepts known-unavailable production authorities

**Observed:** production composition supplies `managedRecoveryUnavailablePreflightEvidenceAuthority`, `managedRecoveryUnavailableAuthorityRevalidator`, and `managedRecoveryUnavailableReconciliationRevisionSource` (`backend/internal/backupasset/runtime/runtime.go:649-665`). Each is intentionally non-authoritative and always returns a sanitized unavailable/changed error (`recovery_runtime.go:185-226`). The graph builder checks only interface non-nilness (`recovery_runtime.go:801-809`), constructs all enabled services, sets `admissionEnabled: true`, and publishes them (`recovery_runtime.go:811-914`, `1164-1201`). Startup reconciliation only calls `ReconcilePermanentCleanupKeyLoss`; it does not exercise or certify the three authorities (`recovery_runtime.go:894-897`).

**Why this is not authority:** an adapter's presence proves type wiring, not the availability or freshness of its owning evidence. The comments accurately say admission should remain closed, but the graph state does the opposite.

**Required disposition:** enabled publication **must fail closed**. Graph build/startup must reject the known-unavailable adapters, or composition must omit enabled publication until real implementations can establish all required current evidence. Per-operation failure remains necessary for dependency outages, but is not an adequate substitute for an honestly publishable production graph.

### 2. Critical: default-disabled runtime drops logical reconciliation

**Observed:** `config.Enabled == false` returns before reconciliation construction. It installs `reconcileMetadata: func(...) error { return nil }` and runs only the cleanup owner (`recovery_runtime.go:793-799`). The PRD requires result revoke/cleanup/**orphan reconciliation** while disabled (`prd.md:246-251`), and the mixed-version contract explicitly requires reconciliation to remain active for a default-disabled new backend (`design.md:1435-1460`).

**Consequence:** a fresh default-disabled process cannot discover or report remote `known_drift`, `db_unmatched`, `forged_or_unknown`, or `scan_incomplete` state. Cleanup is a separate mutation lifecycle and is not a stand-in for read-only logical reconciliation.

**Required disposition:** enabled publication **must fail closed: yes**, independently of this disabled-mode defect. Disabled publication may remain admission-closed, but Task 8 completion must also require a real read-only reconciliation owner in the disabled graph.

### 3. Critical: no production external-evidence authority for live preflight

**Required evidence:** the external boundary must independently prove current source accessibility, exact source/capability/policy/finding revisions and disposition, Xirang/source-root overlap, and reserved byte/inode policy, then exactly match the sealed request (`design.md:5204-5219`; `recovery/preflight.go:132-237`). Missing, unproved, substituted, or echoed request evidence must fail closed (`prd.md:1501-1507`).

**Authoritative pieces that exist:** the target probe produces current target-side root/filesystem/target observations; `SourceValidator.RevalidatePlanTx` can perform a current locked source check; repository/recovery-point rows contain capability revision fields.

**Missing ownership:** no production issuer composes those pieces, and no current security policy/finding product, overlap product, or byte/inode reserve policy source exists. Frozen plan revisions, target free counters, request echo, zero reserve, `false` overlap, clean disposition, or a current timestamp would be stand-ins, not proof. The wired adapter always returns `ErrTargetPreflightUnavailable`.

**Required disposition:** enabled publication **must fail closed** until the complete production external-evidence issuer/adapter is wired. A partial adapter must not approve eligibility.

### 4. Critical: `RecoveryAuthorityRevalidator` is incomplete in production

**Required evidence:** every authority effect must revalidate the complete `RecoveryAuthorityBinding`: target node/credential/root, source and capability, policy/finding/disposition, target observations, and preflight freshness (`recovery/service.go:1703-1742`).

**Authoritative pieces that exist:** `managedRecoveryNodeRevisionSource.ResolveRecoveryNodeRevisionsTx` locks current Node and SSHKey rows, validates exact key-only Recovery credentials and scope, and derives domain-separated node/credential revisions (`recovery_runtime.go:55-120`). Production target session opening consumes that source transactionally (`recovery/target.go:337-399`). `SourceValidator.RevalidatePlanTx` and target-root resolution can supply additional partial checks.

**Missing ownership:** there is no complete production revalidator joining those pieces with a current root revision, security policy/finding authority, external-evidence freshness, overlap, and reserve policy. The wired adapter always returns `ErrRecoveryTargetChanged`. A frozen binding matching itself, plan timestamps, or partial node/source success cannot authorize a write/delete/claim.

**Required disposition:** enabled publication **must fail closed** until a complete revalidator is wired. It must continue to fail each effect closed on any current dependency ambiguity or mismatch.

### 5. Critical: reconciliation has no current root-revision authority

**Required evidence:** logical reconciliation must resolve current node, credential, and root revisions transactionally, scan every registered root with fresh read-only authority, and report clear only after complete trusted EOF with zero findings (`prd.md:1936-1973`, `2004-2007`).

**Authoritative pieces that exist:** current node/credential revisions are implemented; the settings service validates and resolves registered roots; the reconciliation service/target and finding sink are constructed for enabled graphs.

**Missing ownership:** the settings registry has a root locator digest but no independently owned opaque root revision. A locator digest authenticates/compares locator material; it is not the revision binding required by permits and reconciliation. Production therefore wires a revision source that always returns `ErrRecoveryReconciliationUnavailable` (`recovery_runtime.go:214-226`). No timestamp, plan root revision, or locator digest may be substituted.

**Required disposition:** enabled publication **must fail closed** until a durable current root revision and complete production revision source exist. Disabled publication must retain reconciliation but report blocked/unavailable, never synthetic clear, when that authority is absent.

### 6. Important: downgrade readiness differs between fresh-disabled startup and enabled-to-disabled transition

**Observed:** a fresh disabled graph has neither `reconciliation` nor `downgradeReconciler`. `DowngradeReadiness` installs its sticky fence, snapshots DB blockers, and, when the use latch is absent, returns `ErrInvalidState` because reconciliation is unavailable (`recovery_runtime.go:1379-1439`). During enabled-to-disabled transition, `retainMaintenanceFrom` copies the previous reconciliation/downgrade reconciler into the candidate (`recovery_runtime.go:1332-1377`), so the path exists but still fails closed through the unavailable production revision source. If the permanent use latch exists, readiness correctly returns `forward_fix_only` without requiring a remote clear.

**Assessment:** the sticky fence, latch dominance, DB blocker snapshot, and error-as-blocker behavior are conservative. However, fresh default-disabled pristine readiness can never prove remote clear, and transition behavior depends on whether an earlier enabled graph existed. That violates the uniform default-disabled/mixed-version readiness contract even though it does not permit an unsafe downgrade.

**Required disposition:** enabled publication **must fail closed: yes**, due to the missing reconciliation authority. Downgrade readiness itself must remain blocked/error until every registered root receives a fresh clear under the sticky generation; unavailable reconciliation must never be converted to an empty backlog or pristine-ready result.

## Implemented runtime ownership (not blockers)

- **Startup/publication:** `StartupWithConfig` builds, runs `reconcileMetadata`, then publishes and records the graph (`recovery_runtime.go:1164-1201`). Candidate settings graphs are also reconciled before publication (`recovery_runtime.go:1442-1475`). This ordering is real, but the reconciliation callback currently has insufficient scope as described above.
- **Disable/transition:** settings transitions prepare and reconcile the candidate before unpublishing/draining the prior graph, persist only after drain, and attempt restoration on failure (`recovery_runtime.go:1288-1350`; `runtime.go:1510-1549`). Admission is closed while maintenance products may be retained.
- **Shutdown:** `StopAccepting` is sticky and unpublishes before stopping claims. Shutdown waits for publication users, stops graph run ownership, cancels/joins attempts, fences ownership, drains delivery, and shuts lifecycle in the designed order (`recovery_runtime.go:1204-1224`, `1497-1520`, `1576-1617`). Focused tests assert this order (`recovery_runtime_test.go:1480-1516`).
- **Receipt reaper:** production constructs one process-wide owner from the real authorization receipt reaper (`runtime.go:626-650`). `Run` starts it regardless of graph admission, performs an immediate bounded pass, reloads valid dynamic cadence/batch configuration, and retries later after pass failure (`recovery_runtime.go:1226-1285`, `2222-2312`). `PrepareSchemaDown` and shutdown cancel and join it before schema drain (`recovery_runtime.go:1561-1569`, `1609-1611`, `2314-2352`). Tests cover reconcile-before-reaper, disabled ownership, single owner/join, and join-before-schema callback (`recovery_runtime_test.go:1518-1625`, `2183-2245`). The reaper lifecycle is authoritative implementation, not a timestamp/default stand-in.
- **Downgrade DB inspection:** the inspector uses a serializable read-only transaction and counts the use latch and durable Recovery/Content/lease products (`recovery_runtime.go:437-538`). This is authoritative DB evidence for the rows it counts, but cannot replace fresh remote reconciliation.

## Authority-versus-stand-in matrix

| Product | Current authoritative production evidence | Stand-ins that must not approve | Status |
|---|---|---|---|
| Node and credential revisions | Locked Node + SSHKey validation and domain-separated revisions | plan revisions, row timestamps, credential fingerprint alone | Implemented |
| Registered target root | Validated encrypted registry/root resolution | caller locator, frozen plan locator | Partial: no root revision |
| Source validity | Transactional source revalidation exists | frozen plan/source snapshot | Partial/not composed into external authority |
| Capability revision | persisted repository/recovery-point fields exist | plan capability revision alone | Partial/not composed |
| Security policy/finding/disposition | none found | clean/default disposition, plan fields | Missing |
| Xirang/source-root overlap | none found | `false` default, string comparison without owning roots | Missing |
| Reserved bytes/inodes | none found | zero defaults, observed free counters | Missing |
| Preflight freshness | target observation exists | timestamp/TTL alone, request echo | Partial; external proof missing |
| Reconciliation root revision | none found | locator digest, plan root revision, timestamp | Missing |
| Downgrade row blockers/use latch | serializable DB snapshot | cached counts | Implemented |
| Downgrade remote clear | reconciliation contract exists | old scan result, empty default, no-op callback | Unavailable in production |

## Completion gate

Task 8 cannot be marked complete on the current graph. At minimum, production must supply the external-evidence authority (including policy/finding, overlap, and reserve ownership), a complete live authority revalidator, a durable root revision plus reconciliation revision source, disabled-mode managed reconciliation, and tests proving known-unavailable adapters cannot result in enabled publication. Existing lifecycle and receipt-reaper tests should remain as positive evidence, while new production-composition tests should assert startup/publication failure rather than only nil-dependency rejection.

## Caveats

- This is a static audit of the current Task 8 worktree. It does not claim that focused tests or the full project gate were run in this research pass.
- The alert-backed reconciliation finding sink is a reporting sink, not the missing current security finding/policy authority used by preflight and live revalidation.
- Cleanup and receipt retention correctly remain active while admission is disabled, but neither constitutes logical remote reconciliation.
