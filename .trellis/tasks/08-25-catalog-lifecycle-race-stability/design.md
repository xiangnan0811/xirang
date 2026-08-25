# Design — Catalog renewal-loss lifecycle test synchronization

## Confirmed failure

`Indexer.Build` acquires a lease, registers an active build, starts the heartbeat, begins the database generation, opens the Provider session, and only then enters canonical enumeration. The two failing tests configure a 5ms heartbeat and a renewer that returns `ErrLeaseFenceLost` immediately. Under race/CI load, the first heartbeat can therefore cancel the build during generation setup, before the Provider session reaches enumeration.

The tests subsequently wait for an “enumeration entered” or “enumeration canceled” signal. Those signals are impossible when cancellation legally happened earlier, so the timeout reports a lifecycle failure even though production cancellation may be correct. The same assumption produced two independent CI failures:

- run `32811380329`: `TestCatalogIndexerRenewalLossCancelsBuild` timed out waiting for Catalog enumeration cancellation;
- run `32814904444`: `TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown` timed out waiting for Provider enumeration entry.

## Selected boundary

Retain production order and add a test-only release gate to each failing lease renewer:

```text
Build starts heartbeat
  -> generation setup/open Provider
  -> Provider ListCanonical closes entered
  -> gated test renewer observes entered
  -> renewer returns ErrLeaseFenceLost
  -> heartbeat cancels build context
  -> blocked Provider observes ctx.Done and returns
  -> Build performs existing unified teardown
```

The gate waits on either the Provider-entered channel or the renewal context. If context ends first, it returns the context error and does not manufacture a renewal-loss event. This keeps cleanup bounded and prevents a blocked test helper from leaking a goroutine.

## Test contract

- `TestCatalogIndexerRenewalLossCancelsBuild` reuses the existing context-aware `catalogBlockingLeaseRenewer`, with `catalogFailingLeaseRenewer` as its inner renewer. The test waits for both the heartbeat to enter `Renew` and the Provider to enter enumeration before releasing the failing renewer.
- `catalogUnifiedTeardownLease` receives the Provider-entry channel; its `Renew` method waits for that channel or `ctx.Done()`, then emits the existing renewal observation and lease-loss result only after enumeration entry.
- Existing Provider sessions remain the source of `entered` and `canceled` evidence. No sleep or database callback is added.
- Existing assertions continue to prove the returned build error, Provider cancellation, active-build teardown, lease cleanup, generation state, and source-lifecycle deletion ordering.
- CI failures are the pre-change RED. The implementation worker first captures the exact existing selectors and then adds a deterministic fixture-order regression or equivalent explicit assertion if it can fail before the helper change without altering production code. If no additional local RED is honest, the task records the two immutable CI REDs rather than inventing a false production failure.

## Rejected alternatives

- Increasing the 5ms interval or the 3s/1s test timeout only changes failure probability.
- Retrying failed CI would hide recurring nondeterminism.
- Starting the production heartbeat after Provider entry would leave generation/open work without renewal and changes the lease safety boundary.
- Making Provider entry optional would weaken the tests: the intended contract is cancellation and teardown of an already-running enumeration.

## Compatibility and rollback

The intended diff is test-only. It introduces no runtime, database, API, configuration, logging, metrics, security, or privacy change. Reverting the two helper gates restores the old tests; production binaries are byte-for-byte unaffected by the patch except for normal build metadata.
