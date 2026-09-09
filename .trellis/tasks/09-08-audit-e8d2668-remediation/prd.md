# Verified external audit remediation

## Goal
Implement the user-approved complete remediation of XR-01 through XR-15 and O-01/O-02/O-03 at baseline e8d2668266b1a5586b5ff7b50937deab31dd0331. Approval to implement was explicitly given after the review and phased plan.

## Requirements
- XR-01: encrypted write-only monitor headers, explicit preserve/replace/clear, coordinated UI, legacy plaintext backfill.
- XR-02/03/06/07: pending version/MFA binding and atomic single-use completion; atomic recovery consumption with no stale security writes; encrypted expiring enrollment; concurrent last-admin invariant and audited break-glass recovery.
- XR-04/05: atomic legal Task/TaskRun terminal pairs; durable bounded post-commit effects and crash recovery; unified policy controls and authoritative queued-executor entry check.
- XR-08/09: per-monitor due scheduling, bounded parallelism, no overlapping target, cancellation and stale-result suppression.
- XR-10/12/13: retries do not resolve alerts; old responses cannot invalidate a new session; production never implicitly transfers credentials to another origin.
- XR-11/14: all IPv6 SSH paths work; atomic batch creation with durable dispatch results and endpoint-scoped idempotency.
- XR-15: all architectures and promotion use one frozen SHA, verified complete matching CI, scanned digests and provenance.
- O-01: coverage upload failures visible and correct authorization configuration; no fabricated external credentials. O-02: explicit backend/go.sum cache dependency. O-03: consistent fail-closed first-host trust default.

## Acceptance
- Behavioral regression coverage for all confirmed defects, including fault injection and SQLite/PostgreSQL concurrency.
- Browser evidence for changed monitor/session/alert paths; real IPv6 SSH smoke; sandbox interrupted task/recovery verification.
- Existing full backend/frontend gates and migration/CI checks pass on final revision.
- Three-model independent discovery followed by bounded repair verification resolves all confirmed findings.
- No production DB modifications, credential disclosure, unrelated changes or unverified readiness claims.
