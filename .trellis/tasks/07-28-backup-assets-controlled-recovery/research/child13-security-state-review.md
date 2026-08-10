# Research: Child 13 security and state planning review

- Query: Independently review the Child 13 planning package for authorization, source/target binding, state-machine, plaintext lifecycle, migration/rollback, and RED-to-GREEN contract completeness.
- Scope: internal
- Date: 2026-07-28

## Findings

Disposition: **NOT APPROVED**. No Critical finding was identified, but the eight Important findings below must be resolved before `task.py start`.

### Important 1 — The in-place/destructive authority product is asserted but not executable

Evidence:

- `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:163` defines `PlanBinding` without a closed isolated/in-place target mode, an exact operation/delete-set digest, or an authority/grant stage.
- `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:268` through `:286` creates and consumes one undifferentiated grant when creating the job.
- `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:356` through `:362` separately promises a new in-place plan and a distinct high-risk `exact_mirror` grant/checkpoint, but `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:432` through `:446` exposes only one generic authorize route and no issue/consume contract for the delete grant.
- `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md:450` through `:470` owns grant schema/service behavior in Task 5, while the first explicit distinct-delete-grant requirement appears in Task 6 at `:491` through `:497`, whose declared files at `:472` through `:475` are executor/worker files only.

Correction: add a closed `target_mode = isolated | in_place`, a canonical bounded operation/delete-set digest, and distinct one-use `write` versus `exact_mirror_delete` authority categories to plan, preflight, grant, job, checkpoint, idempotency, audit, and API products. Specify exactly how each grant is issued with a fresh `asset.recover` proof, nonempty encrypted reason and actor session, and how the worker refuses/pauses before deletion unless the delete grant is consumed at the second checkpoint after fresh node/root/fence/target revalidation. Define the mutation-aware target revision chain so authorized writes do not look like external drift. State explicitly that in-place paths can never become `RecoveryResultRef` sources. Move genuine RED schema/service/API tests for this product into Tasks 1/5 before the Task 6 executor GREEN.

### Important 2 — The parent’s malware/security override contract disappeared

Evidence:

- The parent requires malware findings to block by default while allowing a separately reasoned and audited Admin override at `/home/murray/code/xirang/.trellis/tasks/07-12-backup-data-explorer-design/design.md:661`.
- Child 13 records findings/revisions in preflight and plan binding at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:133` through `:142` and `:163` through `:175`, but defines no closed override decision, override reason, grant binding, audit product, or denial matrix.
- The implementation plan tests findings and security drift at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md:403` through `:413` and `:455` through `:466`, but has no override RED case.

Correction: either explicitly amend the parent contract to permanent fail-closed blocking, or add a closed `security_decision` product whose default is block and whose permitted Admin override binds the exact finding/policy revision, separate encrypted reason, fresh proof, plan/grant/job digest and registered sanitized audit. Unknown findings and non-overridable policy categories must remain blocked. Add service/API/UI/audit RED tests before implementation.

### Important 3 — Pre-write drift conflicts with the Plan/Job state product

Evidence:

- The Plan diagram at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:183` through `:194` does not give `executed` a drift/supersede edge.
- Job creation marks the plan `executed` atomically before the queued worker can perform first-write revalidation at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:277` through `:286`.
- Task 6 nevertheless requires drift before any write to supersede the plan at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md:476` through `:483`.
- The parent contract also requires pre-first-write drift to mark the plan superseded at `/home/murray/code/xirang/.trellis/tasks/07-12-backup-data-explorer-design/design.md:640`.

Correction: define one legal cross-state transaction for drift after durable job creation but before the first committed target mutation. Either allow a narrowly guarded `executed -> superseded` edge when the job has zero committed mutation checkpoints, or delay/redefine `executed`; in either case specify the job terminal outcome/failure category, source/node lease release, one-job-per-plan uniqueness, and same-key execute replay. Add two-worker and crash-barrier RED tests at job-commit, claim, and first-write boundaries on both engines.

### Important 4 — Work-in-progress plaintext has no publication owner, and crashed `revoking` cleanup has no takeover edge

Evidence:

- ResultSet has only `ready | revoking | cleaned | cleanup_failed` and only `cleanup_failed -> revoking` as a retry edge at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:208` through `:217`.
- The worker creates the plaintext directory and temporary files before result registration at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:337` through `:354`, but the design never states when the ResultSet becomes `ready`, who owns unregistered temp/partial files, or how TTL is anchored.
- Content authorization checks a `ready` ResultSet but not a non-mutating terminal job/publication barrier at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:369` through `:384`.
- Cleanup begins with `ready|cleanup_failed -> revoking` at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:390` through `:409`; a process crash after that CAS leaves `revoking`, which neither the stated CAS nor state-machine retry edge can reclaim. The parent explicitly requires restart reconciliation to resume `revoking` as well as `cleanup_failed` at `/home/murray/code/xirang/.trellis/tasks/07-12-backup-data-explorer-design/implement.md:1222`.

Correction: define an unpublished execution-owned workspace contract without expanding the public four-state ResultSet union: persist marker/workspace ownership and the absolute plaintext deadline before the first target byte, never register temp names, fence all writers, and publish `ready` plus its verified regular-file/report rows only after the allowed terminal/non-mutating job boundary. Explicitly decide whether failed/canceled/`needs_attention` partial verified files are published or cleanup-only. Content issuance must revalidate that barrier. Add cleanup attempt phase/lease fields so an expired owner can CAS `revoking -> revoking` with a fresh fence and resume from durable phase evidence; cover crashes before/after every revoke, drain, validate, delete and tombstone boundary, including the crash before `cleanup_failed` can be written.

### Important 5 — Result cleanup can delete remotely without reacquiring node-wide write exclusion

Evidence:

- The global lock order places target-node lease before ResultSet at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:254` through `:261`.
- The cleanup sequence at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:396` through `:405` first CASes the ResultSet and later checks a node fence, but never acquires/renews/releases the durable node-wide writer lease. Holding the job’s original node lease for the 24-hour plaintext lifetime would be unbounded and is not specified.
- Task 3 promises common recovery/ordinary-write exclusion at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md:417` through `:419`, while Task 7 cleanup tests at `:524` through `:531` omit cleanup-versus-recovery/ordinary-writer races.

Correction: make every remote cleanup attempt acquire a fresh node-wide writer lease/fence under the declared lock order, renew it through delete/tombstone, and release it on every outcome. Define a claim/acquire/CAS sequence that does not lock ResultSet before node lease. Add RED races against active and newly admitted recovery/ordinary node writes, lease loss during delete, and fairness when a busy node repeatedly rejects cleanup; the old cleanup fence must perform zero remote or DB mutation.

### Important 6 — The permanent 000069 use latch has no durable location and is ordered after the irreversible side effect

Evidence:

- The schema is fixed to exactly twelve named tables at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:219` through `:247`, none of which is identified as the recovery-use latch.
- The same design requires a permanent first-use latch at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:249` through `:252`.
- Task 1 again requires exactly twelve tables, then says the latch is created in the “same first successful recovery write transaction” at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md:340` through `:365`; a remote SSH/Provider write cannot be atomic with that database transaction, so a crash can otherwise leave target bytes without the down latch.
- The existing Recovery Cleanup Ownership key cannot stand in for Child 13 use: it is already a required current-main domain at `/home/murray/code/xirang/backend/internal/backupasset/keyring.go:20` through `:37` and is ensured/used by current runtime at `/home/murray/code/xirang/backend/internal/backupasset/runtime/runtime.go:280` through `:289`.

Correction: name the exact recovery-specific durable latch row/table and its paired constraints. Commit it conservatively **before** the first possible remote mutation under the current job/node fence, require every TargetPort mutation to observe it, and never delete it; a failed first write may leave the latch, but a successful write may never precede it. If this requires a thirteenth table, amend the twelve-table contract and manifest explicitly; otherwise specify the distinguished permanent row and prevent normal cleanup from deleting it. Add SQLite/real-PostgreSQL crash tests plus pristine/used/purge-to-empty down guards that fail before any schema/constraint change.

### Important 7 — Binary rollback/mixed-version safety is declarative but not enforceable

Evidence:

- Application rollback keeps the new cleanup owner alive at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:551` through `:557` and `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md:813` through `:825`.
- Binary rollback says an older binary “must not run” while recovery authority or cleanup exists at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md:827` through `:838`, but defines no downgrade-readiness gate or operator/runtime mechanism. A retained but non-cleaned ResultSet still requires the new cleanup owner, so “cleaned or retained” is sufficient for feature disable but not for a pre-Child binary downgrade.
- Current startup only runs `m.Up()` and handles dirty state; it has no explicit forward-schema/active-recovery rejection at `/home/murray/code/xirang/backend/internal/database/migrator.go:107` through `:142`.

Correction: distinguish same-binary feature disable from binary downgrade. After first use, declare pre-Child binary downgrade unsupported/forward-fix-only unless a new, tested downgrade-readiness operation proves zero queued/running jobs, unconsumed grants, leases/attempts, non-`cleaned` ResultSets, recovery Content grants/tickets/streams and reconciliation backlog. Retained plaintext must keep downgrade unready. Define where this gate is invoked and record the mixed-version matrix: default-disabled new backend, old frontend, new frontend/old backend, feature disable, and forbidden downgrade. Add restart/rollback RED tests; do not rely on the old binary to understand 000069.

### Important 8 — Exact source locators would be copied into durable authority objects without an at-rest/no-leak contract

Evidence:

- `SourceRevision.ImmutableLocator` is a plaintext string and the serialized revision is copied into selection/plan/grant/job/checkpoint digests at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:78` through `:106`.
- The durable schema stores those bindings at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:219` through `:247`, while the privacy section encrypts target paths and reasons but does not cover Provider/source locators at `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md:520` through `:535`.
- Current-main source and entry locators are deliberately encrypted and `json:"-"` at `/home/murray/code/xirang/backend/internal/model/backup_asset.go:78` through `:127` and `/home/murray/code/xirang/backend/internal/model/backup_asset_catalog.go:35` through `:58`.

Correction: bind immutable source authority with an existing RecoveryPoint reference plus a domain-separated locator digest, and store any locator copy only in encrypted `json:"-"` fields; decrypt it only inside the Repository/Provider boundary after revalidation. Apply the same sensitive-setting treatment to configured target-root locators. Add model/service tests proving ciphertext at rest and no raw locator/root in API, Swagger, audit, log, metric, failure evidence or frontend DTO, while the digest still detects substitution.

### Activation assessment

The planning package is **not safe to activate now**. It will be safe to activate after all eight findings are resolved in `prd.md`, `design.md` and `implement.md`, each correction has an owning genuine RED-to-GREEN batch and selector, the exact manifest/context files are amended if needed, and both independent Phase 1.4 reviews are rerun with no open Critical/Important findings.

## Files Found

- `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/prd.md` — Child 13 requirements and acceptance contract.
- `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md` — Child 13 domain, state, authority, runtime and rollback design.
- `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md` — exact manifest and ordered RED-to-GREEN execution plan.
- `/home/murray/code/xirang/.trellis/tasks/07-28-backup-assets-controlled-recovery/research/current-main-evidence.md` — current-main seams and gaps.
- `/home/murray/code/xirang/.trellis/tasks/07-12-backup-data-explorer-design/prd.md` — parent product decisions, including controlled recovery.
- `/home/murray/code/xirang/.trellis/tasks/07-12-backup-data-explorer-design/design.md` — parent Child 13 recovery, security and ResultSet contracts.
- `/home/murray/code/xirang/.trellis/tasks/07-12-backup-data-explorer-design/implement.md` — parent Child 13 state/test/execution contract.
- `/home/murray/code/xirang/backend/internal/model/backup_asset.go` and `backup_asset_catalog.go` — current encrypted source-locator patterns.
- `/home/murray/code/xirang/backend/internal/backupasset/runtime/runtime.go` and `backend/internal/backupasset/keyring.go` — current Recovery Cleanup Ownership key creation/use.
- `/home/murray/code/xirang/backend/internal/database/migrator.go` — current startup migration behavior.
- `/home/murray/code/xirang/.trellis/spec/backend/database-guidelines.md` — paired migration, real-PostgreSQL and down-safety contracts.
- `/home/murray/code/xirang/.trellis/spec/backend/error-handling.md` and `quality-guidelines.md` — fail-closed RBAC/ownership, generic-error, SSH-purpose and audit/grant contracts.
- `/home/murray/code/xirang/.trellis/spec/frontend/type-safety.md`, `state-management.md`, `a11y-guidelines.md` and `quality-guidelines.md` — closed API mapping, opaque revision, async state, accessibility and full frontend gates.
- `/home/murray/code/xirang/.trellis/spec/guides/cross-layer-thinking-guide.md` — cross-layer contract checklist.

## Code Patterns

- Current provider locators use encrypted model fields and model hooks, not raw API-visible strings: `/home/murray/code/xirang/backend/internal/model/backup_asset.go:87`, `:116` and `:123`.
- Recovery Cleanup Ownership is pre-existing foundation state, so Child 13 needs a separate use latch: `/home/murray/code/xirang/backend/internal/backupasset/keyring.go:26` and `/home/murray/code/xirang/backend/internal/backupasset/runtime/runtime.go:282`.
- Required `/api/v1` authorization is Auth + RBAC + ownership and fail-closed denial: `/home/murray/code/xirang/.trellis/spec/backend/quality-guidelines.md:17` through `:29` and `:183` through `:233`.
- Real PostgreSQL parity must be non-skipped and every new migration must enter the selector: `/home/murray/code/xirang/.trellis/spec/backend/database-guidelines.md:119` through `:189`.

## External References

None. This was an internal planning/current-main/spec review; no external version or documentation claim was needed.

## Related Specs

- `/home/murray/code/xirang/.trellis/spec/backend/database-guidelines.md`
- `/home/murray/code/xirang/.trellis/spec/backend/error-handling.md`
- `/home/murray/code/xirang/.trellis/spec/backend/quality-guidelines.md`
- `/home/murray/code/xirang/.trellis/spec/frontend/type-safety.md`
- `/home/murray/code/xirang/.trellis/spec/frontend/state-management.md`
- `/home/murray/code/xirang/.trellis/spec/frontend/a11y-guidelines.md`
- `/home/murray/code/xirang/.trellis/spec/frontend/quality-guidelines.md`
- `/home/murray/code/xirang/.trellis/spec/guides/cross-layer-thinking-guide.md`

## Caveats / Not Found

- This is a contract-completeness and internal-consistency review, not a style review and not implementation evidence.
- No product, test, migration, task-status, staging, commit, push or PR action was performed.
- The proposed 000069 files do not exist on the reviewed baseline, so DDL behavior can only be assessed from the planning contract until implementation RED tests exist.
