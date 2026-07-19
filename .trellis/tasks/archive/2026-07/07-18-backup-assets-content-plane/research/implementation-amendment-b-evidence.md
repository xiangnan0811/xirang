# Child 8 Focused Implementation Amendment B Evidence

## 1. Purpose and review state

This note records a lease/admission design defect discovered while reviewing
concurrent Task 3 edits. It is planning evidence, not implementation or test
acceptance evidence.

- Branch/base remain `codex/backup-assets-content-plane` at
  `a3c309a922d9a4f48cb82031031c0975c251f5f4`.
- The Trellis task remains `in_progress`, but product edits are paused while the
  focused amendment is reviewed.
- The user approved the Amendment B design proposal and then explicitly
  approved the complete written PRD/design/implementation revision on
  2026-07-18. Product work may resume under the corrected manifest and TDD plan.
- A focused lease/source test command passed before this review. That result is
  not accepted as GREEN because the tests encoded the incorrect deadline
  relationship described below.

No commit, archive, journal, push, PR, CI, merge, or post-merge action occurred.

## 2. Parent lease contract and current-main behavior

The parent design Section 16 says that a new fence in a **multi-stage
publication** inherits the same point deadline. It does not make that historical
publication deadline the deadline of every later lease holder. Catalog, Search,
Content, Worker, export, and recovery are independent lease producers.

Current `LeaseService.resolveAcquireDeadlineTx` implements that distinction:

- a zero `AcquireLeaseRequest.AbsoluteDeadline` receives a fresh bounded
  `now + LeaseConfig.AbsoluteDeadline` deadline;
- an explicit deadline participates in the immutable multi-stage contract and
  is compared with prior point lease evidence.

Catalog and Search pass zero because their stage deadlines are independent.
Publication passes an explicit `PublicationLineage.PointDeadlineAt` so a
release/reacquire cannot extend the publication attempt.

The approved Child 8 design incorrectly generalized publication behavior to a
`content_session`: it required every delivery grant to reuse the historical
publication deadline. A committed RecoveryPoint may be browsed long after that
deadline has elapsed. Reusing it would reject valid historical assets. It can
also conflict with a newer zero-deadline Catalog/Search lease selected by the
generic prior-row query.

## 3. Frozen lease correction

Every active delivery grant still owns one independent `content_session`
RecoveryPoint lease:

```text
RecoveryPointID  = exact AssetRef.RecoveryPointID
HolderType       = content_session
OwnerID          = internal grant_id
AbsoluteDeadline = zero in AcquireLeaseRequest
```

`LeaseService` supplies its existing bounded holder deadline (default cap seven
days). The grant remains much shorter lived:

```text
grant absolute expiry = min(profile TTL, login session expiry,
                            exact proof expiry when required,
                            returned content lease deadline)
```

The Broker renews the short lease only while the grant is active and releases
the exact fence on normal close/revoke/expiry. Crash reconciliation may take
over only an expired short lease for cleanup; it never resumes delivery. No
root `LeaseService` behavior or paired migration changes are needed.

Two focused Content files are added to the exact manifest because lease
ownership is a distinct, testable unit rather than Broker transport logic:

```text
backend/internal/backupasset/content/lease.go
backend/internal/backupasset/content/lease_test.go
```

## 4. Admission-before-decryption and one-token lifecycle

`repository.Service.OpenContentSource` must acquire exactly one
`publication.OperationContentRead` token after feature/runtime/request-shape
validation and before any GORM model hook can decrypt Catalog locator/access
material or any Provider port is resolved.

The token is transferred, not reacquired, through the selected source path:

- portable Restic/Rclone and mutable readers keep it on the sealed source
  session;
- managed Rsync accepts the already-acquired token in its internal exact-point
  constructor instead of consuming a second admission slot;
- native Rclone keeps the same token while using only the exact version reader.

Reader close/join, post-read source revalidation, Provider/session release, and
admission release execute in that order. All cleanup stages run even when an
earlier stage fails. Source/fence drift remains the authoritative safe failure
when it is observed alongside a generic limit/close error.

Post-read revalidation uses `context.WithoutCancel` and always receives a
deadline five seconds after cleanup starts. If the request context had a hard
deadline, the earlier deadline wins. If that deadline has elapsed, revalidation
performs no unbounded I/O and returns source-unknown; accounting charges the
full reservation and the grant is revoked. A context without a caller deadline
still receives the five-second cleanup ceiling.

Tests must prove a rejected admission token causes zero Catalog/access queries,
zero model-hook decryption, and zero Provider calls; managed Rsync consumes one
token; cancellation still closes/joins exactly once; and close-time revalidation
always observes a finite deadline.

## 5. Concurrent-edit reconciliation

A research agent continued writing the shared worktree during context handoff.
It introduced several edits outside the approved manifest. They are preserved
until this written amendment is approved, then reconciled manually with
`apply_patch` (never checkout/reset):

```text
backend/internal/backupasset/lease.go
backend/internal/backupasset/lease_test.go
backend/internal/backupasset/repository/service.go
backend/internal/backupasset/repository/rclone_publication_execution_test.go
```

The root lease edits and their incorrect point-deadline test are removed. The
Repository service test-only function field is removed in favor of a narrow
helper owned by `content_read.go`. The shared Rclone publication fake is restored;
native exact-read behavior is tested through a local narrow helper/fake in
`content_read_test.go`. The existing `content/source.go` is renamed to the
approved `content/source_contracts.go` path.

No unrelated concurrent edit is discarded. The approved Provider meter,
representation migration, ticket/session, SourceResolver, and Trellis changes
remain and are reviewed independently.

## 6. Alternatives rejected

1. **Reuse the publication deadline.** Rejected because historical committed
   points can outlive publication execution by months or years.
2. **Add per-holder explicit-deadline groups to root LeaseService.** Safe in
   principle but unnecessary: existing zero-deadline acquisition already gives
   Content an independent bounded holder deadline and avoids a shared-domain
   change.
3. **Hide lease code inside Broker.** Rejected because acquire/renew/fence hash,
   cleanup-only takeover, and release idempotency form an independent unit with
   focused tests.

## 7. Scope conclusion

Amendment B adds only the two Content lease files to the product manifest and
corrects existing Child task documents. It does not change `000066`, root lease
semantics, Provider commands, Search ingest, Worker/Derived/UI scope,
RecoveryResult support, feature defaults, ports, TLS, or release behavior.

Design and written-package approvals are recorded. Product implementation may
resume under the corrected manifest; approval is not implementation or test
evidence.

## 8. Final inline lease-cleanup review evidence

The pre-staging review found three direct `context.Background()` cleanup paths
inside the two Amendment B files: invalid acquired-lease rollback, invalid
cleanup-takeover rollback, and `ContentLeaseSession.Close`. Each could wait
without a deadline even though ticket/read cleanup elsewhere was capped.

`TestContentLeaseDetachedCleanupHasFiniteDeadline` first failed all three
subcases because the controller observed no deadline. The minimum fix derives a
`context.WithTimeout(context.WithoutCancel(parent), 5s)` cleanup context for all
three paths. `TestContentLeaseInvalidCleanupReturnsReleaseFailure` then failed
before rollback errors were joined and passed after both the invalid-lease and
release errors remained discoverable with `errors.Is`. The two focused tests
and the complete `backend/internal/backupasset/content` package exited 0 after
the fix. Broader final gates remain evidence-driven in `implement.md` and are
not implied by this focused result.
