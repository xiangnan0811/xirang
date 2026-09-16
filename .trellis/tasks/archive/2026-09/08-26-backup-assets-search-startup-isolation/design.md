# Design — Search Startup Isolation and Sealed-State Interoperability

## Root cause

`SearchWorker.buildCandidates` currently joins candidate builds and returns the
first ordinary build error. During startup that candidate-local error propagates
through runtime reconciliation to `main`, which calls `Fatal` before creating
the router. The periodic worker already treats the same pass failure as
retryable, so restart incorrectly turns point-local Search unavailability into
global service unavailability.

The released mutable-count fix correctly reaches projection. Production then
exposes the next exact contract mismatch: Catalog persists every enumerated
entry with `security_state=sealed`, while Search treats the same column as a
closed sensitivity and rejects `sealed`. Every attempt therefore fails before
the first Search document with `search_invalid_security_state`.

## Selected changes

### Candidate isolation

Mirror the existing Catalog worker boundary. Candidate goroutines continue to
classify build metrics and the Indexer continues to persist failed staging
evidence. After all candidates join, `buildCandidates` returns only caller
context failure. Pass-level configuration, reconciliation, key, and list errors
still return normally and keep startup fail closed.

Do not catch errors in `main` or `Runtime.StartupPass`; the isolation belongs at
the fan-out boundary that knows the errors are candidate-local.

### Known sealed-state compatibility

Map the exact Catalog-produced value `sealed` to sensitivity `unknown` during
Search projection. This is conservative: unknown content remains subject to
the existing three-valued filtering and step-up rules. Keep every other unknown
non-empty value invalid. Do not rewrite Catalog rows and do not relax the
Indexer lifecycle or activation proof.

The existing active Catalog can therefore converge through the normal Search
worker after formal feature re-enable; no Catalog rebuild or SQL mutation is
required.

## Verification

TDD covers both independent incident causes:

1. worker/runtime startup RED for an ordinary candidate build failure;
2. Indexer RED for a real `sealed` Catalog entry;
3. table-driven infrastructure, cancellation, fence, future-security-state,
   and inactive-output negative controls;
4. focused repetition/race, affected packages, vet/lint/format/privacy;
5. independent Trellis check, PR CI, release and production acceptance.

Production stays fail-closed with backup assets disabled until the released
image is verified. Re-enable uses the authenticated Settings API, not SQL.
