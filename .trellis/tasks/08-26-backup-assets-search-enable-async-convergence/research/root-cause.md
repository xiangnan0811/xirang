# Root cause — bounded enablement synchronously owns large Search projection

## Confirmed production signature

- Image: `linnea7171/xirang:v0.50.8`
- Revision: `d9db1d98fa5e33c54a83bad72d69217c98a7784d`
- Container: running/healthy, restart count zero, health HTTP 200
- Database: SQLite integrity `ok`, migration `72|0`
- Feature after compensation: `backup_assets.enabled=false`
- GA: existing/acknowledged, current digest present and acknowledged
- Task 3: canceled/disabled; global active TaskRuns zero
- Catalog: mutable_head/observed, generation 50 complete/active, 60,515
  written and physical entries
- Search generation 11: failed/inactive, `search_build_timeout`, expected 60,515,
  written 8,000, physical documents 8,000, duration 24,979 ms
- Search: no active generation, no active lease, one active Search token key
- Prior generations 1-10: failed/inactive `search_invalid_security_state`
- Logs: exact enable request observed once, one context-deadline match, no lock,
  compensation-fence, panic, fatal, or new invalid-security-state signature
- Automatic rollback: authenticated Settings API restored false; container and
  health remained available; no Provider read/write or asset diagnostic output

## Causal chain

1. Formal settings mutation holds the Foundation mutation gate and starts the
   25-second feature-transition operation context.
2. GA, content, admission, persistence, Search key, and readiness preparation
   succeed.
3. `startupSearchWithConfig` calls the full Search startup pass synchronously.
4. The pass selects the valid active Catalog and starts generation 11.
5. Projection commits eight 1,000-row batches with exact written/physical count.
6. The operation context reaches its 25-second deadline; Indexer records
   `search_build_timeout`, releases the Search lease, and leaves the generation
   inactive.
7. Runtime compensates the enabled setting back to false and the handler returns
   HTTP 500. Feature-gated APIs correctly remain 503.

This rules out the v0.50.8 sealed-state mapper, Catalog integrity, missing key,
lease leakage, SQLite locking, Provider access, GA readiness, and Task activity.

## Safety boundary

The fix must remove candidate Build ownership from bounded enablement/startup,
not lengthen the deadline or weaken Search. Synchronous readiness keeps exact
infrastructure checks. Only a fully committed hot enable emits a coalesced wake
to the existing owned background worker; cold startup relies on its normal
initial pass. The worker re-reads committed config and retains full candidate
lifecycle, metrics, failure evidence, cancellation, and atomic activation.
