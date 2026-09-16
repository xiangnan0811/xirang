# Root cause — Catalog renewal-loss lifecycle test flakes

## Evidence status

- Confirmed CI RED 1: GitHub Actions run `32811380329`, Backend race step, `TestCatalogIndexerRenewalLossCancelsBuild`, `indexer_test.go:738`, “lease renewal loss did not cancel Catalog enumeration”.
- Confirmed CI RED 2: GitHub Actions run `32814904444`, Backend race step, `TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown`, `source_lifecycle_test.go:190`, “Catalog Provider session did not enter enumeration”.
- Corresponding failed Backend jobs are `97691289454` and `97701179482`.
- Both runs executed `go test -race ./internal/backupasset/... ./internal/api/handlers/ -count=1`.

## Production order

`Indexer.Build` starts its lease heartbeat before database generation setup and before `OpenCatalogRead` / `ListCanonical`. This is intentional: the lease must remain renewable while setup work is in progress. The heartbeat cancels the shared build context when renewal fails.

## Pre-change fixture race

Both affected tests set a 5ms heartbeat. Their fake renewers return `ErrLeaseFenceLost` at the first renewal. Under ordinary fast local execution, Provider enumeration usually starts before the first tick. Under race instrumentation or loaded CI, generation/database setup can exceed 5ms, so the first tick cancels the build before enumeration begins.

The source-lifecycle test then waits for an entry signal that cannot occur. The indexer test waits for a cancellation signal produced only inside the enumeration session, which likewise cannot occur if the session never opened. Increasing timeouts cannot resolve this impossible-event branch.

## Selected fix

The lease-loss injection is a test concern, so it must be synchronized to the phase the tests claim to exercise. The indexer test reuses the existing context-aware blocking-renewer wrapper and releases its failing inner renewer only after both heartbeat renewal and Provider enumeration have entered. The unified-teardown fake lease waits for the Provider session's test-owned `entered` channel (or its own context cancellation) before returning lease loss. Production code remains unchanged.

This preserves the meaningful proof: an already-entered enumeration observes renewal loss through the production context path and participates in the existing unified teardown.

Waiting for Provider teardown/join instead of Provider entry is forbidden because teardown itself depends on renewal failure and would deadlock. A context cancellation arm is mandatory for every helper wait.

## Verification target

- exact selectors ordinary high repetition;
- exact selectors under `-race` high repetition;
- complete Catalog package;
- exact CI backup-asset race command;
- no production-code diff, no timeout inflation, no sleep, no schema/API/settings/privacy drift.
