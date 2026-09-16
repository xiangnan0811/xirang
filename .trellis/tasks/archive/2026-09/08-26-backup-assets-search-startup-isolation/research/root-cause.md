# Root Cause — Search Candidate Failure Startup Crash

## Production incident

The v0.50.7 production upgrade reached the Search worker after the mutable
Catalog count fix. Startup then exited deterministically before the router with
`backup asset search catalog changed`. Automatic rollback restored v0.50.6
configuration, but v0.50.6 also crashed because its older Search count proof
rejected the already-complete mutable Catalog.

The restart loop reached 659 restarts. A guarded emergency procedure stopped the
container, created and verified a fresh SQLite backup, and changed exactly one
row: `backup_assets.enabled=true` to `false`. It changed no Catalog, Search,
asset, locator, manifest, Task, or Provider row. v0.50.6 then became healthy with
restart count zero and no new critical or node-log collector errors.

## Preserved evidence

- Schema/integrity: `72|0`, quick check `ok`.
- Task 3: canceled and disabled; zero active runs.
- Node-log collectors: zero.
- Active Catalog: `mutable_head`, `observed`, complete/active, no manifest,
  expected entry count zero, written and physical rows both 60,515.
- Search: ten failed/inactive generations, zero documents and zero active leases.
- Latest Search: expected documents 60,515, written zero, stable error
  `search_invalid_security_state`.
- Emergency backup:
  `/backup/db/emergency-search-startup-crash-20260826T010124Z`.

No path, filename, locator, token, credential, or content was captured.

## Code path

`SearchWorker.buildCandidates` returns an ordinary candidate Build error after
joining all candidate goroutines. Startup propagates it through the runtime and
calls `Fatal` before API/router creation. The periodic worker treats pass errors
as retryable, proving the global startup failure is at the wrong ownership
boundary.

The stable error is produced before the first document because Catalog Indexer
persists the known locator-seal state `sealed`, while Search's sensitivity
mapper rejects every non-empty value outside
`non_secret|secret|unknown`. The Catalog value proves the Provider locator is
authenticated and encrypted; it does not classify the entry's content. Search
must therefore project it as conservative `unknown`, not as proof of
non-secret content. Arbitrary future values remain invalid.

## Locked fix boundary

1. Isolate candidate-local Build outcomes inside Search worker fan-out; only
   caller context failure escapes after all builds join.
2. Keep configuration, key, abandoned/overlay reconciliation, and candidate-list
   errors fatal to startup.
3. Map exact known Catalog value `sealed` to Search sensitivity `unknown`.
4. Preserve arbitrary future-state rejection and every Indexer integrity check.
5. Do not mutate production Catalog/Search rows or Provider data.
