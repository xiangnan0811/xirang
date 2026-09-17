# Research: planning review

- **Query**: Defect-first review of the lifecycle PRD/design/implementation plan against the live repository. Do not start implementation.
- **Scope**: provider-delete coordinator/registry/worker paths, audit writer/retention paths, lifecycle models/migrations, fixtures, migration checks and PostgreSQL CI.
- **Date**: 2026-09-03

## Status of this artifact

The detailed findings below are historical code-gap evidence gathered before the mandatory lifecycle revision. The earlier `revise-before-start` verdict and later `approve-with-nits` re-review are retained only as history; neither is a current approval. The current authoritative requirements are in `prd.md`, `design.md` and `implement.md`.

A mandatory revision was applied to those artifacts on 2026-09-03. No production code, test, migration, specification or task start was performed. A fresh independent review of the integrated current artifacts is still required and must report no Important finding before planning can be approved.

## Historical code-gap findings retained for implementation context

1. **One-shot provider-delete call graph.** `Coordinator.deleteAndTransition` currently reaches `RegistryPointDeletion.DeleteRecoveryPoint`, whose Tx1/provider/Tx2 boundary has no durable effect claim. The implementation must split request preparation, provider execution and receipt verification without leaving a production one-shot bypass.
2. **In-flight observation mapping.** The current consumer maps unrecognized provider errors through `providerDeletionBlockedReason`, so a future `ErrEffectClaimInFlight` would become a blocked/unproven attempt unless the Coordinator consumes explicit claim/stage truth first.
3. **Ownership boundary.** Current adapter and Coordinator responsibilities do not yet provide a Coordinator-owned claim/renewer/receipt lifecycle. The current design assigns all claim state transitions, renewer ownership and post-effect CAS to the Coordinator while keeping exact provider materialization in the Registry boundary.
4. **Provider-stage error ambiguity.** Tx1 identity errors and Tx2/provider errors currently share sentinel mapping, and context cancellation can be classified as blocked. The current plan requires explicit pre/provider/post stage and `ProviderCalled` truth while preserving `errors.Is`.
5. **No durable claim schema/model.** Lifecycle attempts currently have no effect executor, execution ID, target digest, claim state, deadline or heartbeat row. Migration 000077 and model work are therefore required.
6. **Fence/lease races.** The current shared retention lease owner and `ensureLifecycleFenceTx` path do not fence an external provider call. The required claim snapshots and matching executor/execution/fence/revision/digest predicates prevent old renew, uncertain, receipt and attempt mutations.
7. **Claim fixture gap.** `newClaimedExpiryFixture` currently uses `lifecycleDeletionFake`; it does not exercise Registry preparation or a durable claim. Claim acceptance tests must use RegistryPointDeletion with a synchronized provider fake or a DB-backed provider barrier.
8. **Target identity gap.** `sameLifecycleDeleteRequestAuthority` compares private provider authority but is not a persisted, secret-free retry binding. The current plan requires public `NewCanonicalSHA256` framing plus an `IdentitySalt`-keyed private fingerprint, provider-specific canonicalizers and drift vectors.
9. **Audit deduplication gap.** Production settled audit currently has a scan-then-`Write` shape and may use purgeable event detail rows as proof. The current plan replaces this with one locked status derivation, `WriteTx`, and an immutable `(attempt_id,status)` slot.
10. **Audit transaction boundary.** `AuditWriter` already supports `WriteTx`, and the runtime retention adapter delegates it, but the lifecycle sink contract and settled paths do not yet use it as the slot transaction boundary. Blocked facts must commit before a second slot/event transaction; receipt facts must not roll back on audit failure.
11. **Status transition gap.** Existing blocked/retry paths can derive status from an unlocked caller or event scan and can race a receipt. The required status machine allows one each of `blocked` and `identity_conflict` in either order, then at most one terminal `deleted`/`already_absent`, with stale callers re-deriving locked truth.
12. **Retention proof gap.** Existing tests and helpers do not prove that a completed settled event remains deduplicated after `AuditRetention.PurgeEligibleDetails` removes details from a closed non-latest segment. Acceptance evidence must use the production AuditWriter/adapter and actual purge operation.
13. **Migration safety gap.** The latest migration is 000076, and its SQLite implementation rebuilds lifecycle attempts. 000077 must be additive, paired and stacked on the existing admission triggers; claim-only and slot-only used-down cases need atomic preservation tests.
14. **Schema contract gap.** No claim or audit-slot table currently enforces secure ID/digest shape, positive revision, normalized attempt relation, claim no-delete/proven immutability, slot append-only behavior, `(attempt,status)` uniqueness or one terminal per attempt. Both engines need direct constraint tests.
15. **Validation/CI gap.** `backup_asset_migrations_integration_test.go` still names 000076, the backup migration checker has hard-coded older selectors, and the normal workflow does not yet provide a required retention barrier selector. The implementation plan names every freshness script, current-version document and required PostgreSQL wrapper change; missing `TEST_POSTGRES_DSN` and zero selected tests must fail closed.

These findings explain why the current task is necessary. They do not authorize implementation and do not override the current artifacts.

## Current planning state

The mandatory revision brief requires and the current artifacts now specify:

- Coordinator-owned provider-delete Tx1 claim acquisition/takeover, Coordinator-owned renewer, provider call outside transactions, receipt Tx2 and explicit stage/provider-called truth;
- process-unique executor plus fresh per-acquisition execution IDs, independent secure claim/slot IDs and no use of `CoordinatorDependencies.NewID` for them;
- non-deletable `in_flight`/`uncertain`/`proven` claims, same immutable target digest on takeover, and proof-first behavior for valid tombstones/proven claims;
- public canonical framing plus a salt-keyed private binding fingerprint, provider-specific fail-closed canonicalizers and full locator/authority Verify;
- one logical clock, required lock order, TTL/deadline/fence checks, renewer stop/join and safe deadline races;
- immutable transactional audit slots, locked status derivation, `AuditWriter.WriteTx`, rollback atomicity, status ordering and no event scan;
- exact 000077 SQLite/PostgreSQL schema, constraints, append-only/no-delete protections, stacked used-down admission and direct dual-engine tests;
- production retention purge proof, fixture cutover, migration freshness updates and a required PostgreSQL selector/runner that cannot silently skip without `TEST_POSTGRES_DSN`.

The historical review's “approve-with-nits” result is superseded. No fresh independent review was run after the mandatory rewrite, so the task remains `planning`; explicit user start approval is still required.

## Evidence read during historical review

- `backend/internal/backupasset/retention/coordinator.go`: current `DeleteRecoveryPoint`, `deleteAndTransition`, receipt/blocked/retry/audit helpers, lock order and fence checks.
- `backend/internal/backupasset/retention/worker.go`: shared `RetentionWorkerLeaseOwnerID` and heartbeat/advance flow.
- `backend/internal/backupasset/runtime/retention_lifecycle.go`: Registry adapter/runtime construction and existing `WriteTx` delegation.
- `backend/internal/model/backup_asset_lifecycle.go`: current attempt/tombstone fields and absence of claim/slot models.
- `backend/internal/backupasset/provider`: deletion request/access types, provider-specific authority and canonical/fingerprint helpers.
- `backend/internal/backupasset/audit.go`: `AuditWriter.WriteTx` and `AuditRetention.PurgeEligibleDetails`.
- `backend/internal/database/migrations`: paired 000076 migration shape and existing used-down admission objects.
- `backend/internal/database/backup_asset_migrations_integration_test.go`: current latest version 76 and schema expectations.
- `backend/README_backend.md`, `.trellis/spec/backend/database-guidelines.md`, migration freshness scripts and `.github/workflows/ci.yml`.

No tests were executed during this planning revision because implementation is unauthorized and the user requested planning-only changes.
