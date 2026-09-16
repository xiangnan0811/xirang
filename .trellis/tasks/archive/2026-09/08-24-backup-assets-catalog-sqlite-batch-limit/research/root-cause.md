# Root-cause evidence

Status: `root_cause_confirmed_red_green`

## Production signature

- Existing v0.50.4 repository is online/access-active; mutable point is observed.
- Catalog sequences 1 through 4 automatically attempted and failed with `catalog_build_failed`.
- Sequence 1 duration was about 397 ms; sequence 4 duration was about 367 ms.
- No active generation or indexed entries exist; content/list capability remains available.
- Task 3 had no active run and no new run during the failures.

## Pre-change code arithmetic

- Registered default `backup_assets.catalog_batch_size`: 2000.
- Before the fix, `Indexer.insertBatch` called one `db.Create(&entries)` per logical batch.
- `CatalogEntry` persists approximately 17 columns, yielding about 34000 bind variables for 2000 rows.
- Bundled `github.com/mattn/go-sqlite3@v1.14.47/sqlite3-binding.c` defaults
  `SQLITE_MAX_VARIABLE_NUMBER` to 32766.

## Real SQLite RED

The first normal test invocation did not reach the regression because the local `/tmp` quota
failed during Go compilation with `disk quota exceeded`. It was not counted as RED. Before any
production-code change, the same single test was rerun with one dedicated temporary directory:

```text
env GOTMPDIR=/var/tmp/xirang-go-tmp.VKOV1d TMPDIR=/var/tmp/xirang-go-tmp.VKOV1d \
  go test ./internal/backupasset/catalog \
  -run '^TestCatalogIndexerSQLiteDefaultLogicalBatchUsesDatabaseSafePersistenceChunks$' \
  -count=1 -v
```

Safe excerpt (exit 1):

```text
RED confirmed: SQLite rejected the full logical batch at its bind-variable boundary
Catalog build did not complete
FAIL xirang/backend/internal/backupasset/catalog
```

The regression internally required the returned error chain to contain the bundled driver signal
`too many SQL variables`, then loaded the durable generation and proved it was neither `complete`
nor active. It did not print the raw database error, SQL, record paths/names, content, locator,
credential, or ciphertext.

The RED confirms the root cause. The bundled driver is
`github.com/mattn/go-sqlite3@v1.14.47`; its `sqlite3-binding.c` defaults
`SQLITE_MAX_VARIABLE_NUMBER` to 32766. `CatalogEntry` binds 17 persisted columns, so one logical
batch of 2000 rows attempts about 34000 variables and crosses the ceiling.

## Minimal GREEN and verification

`Indexer.insertBatch` now calls GORM `CreateInBatches` with a package-private physical batch of
1000. That is about 17000 variables per statement, retaining roughly 48% headroom below 32766.
The Provider page/logical flush remains 2000; proof, ordering, source/fence checks, model hooks and
activation CAS are unchanged.

Exact regression GREEN (exit 0):

```text
env GOTMPDIR=/var/tmp/xirang-go-tmp.QsUe2Z TMPDIR=/var/tmp/xirang-go-tmp.QsUe2Z \
  go test ./internal/backupasset/catalog \
  -run '^TestCatalogIndexerSQLiteDefaultLogicalBatchUsesDatabaseSafePersistenceChunks$' \
  -count=1 -v
PASS
ok xirang/backend/internal/backupasset/catalog 0.218s
```

The passing test requires one `complete`, active generation, `written_entry_count=2000`, exactly
2000 durable entries, and a raw-table locator value that still passes `secure.IsEncrypted`.

Repeated focused regression and package verification (both exit 0):

```text
env GOTMPDIR=/var/tmp/xirang-go-tmp.wHjsn9 TMPDIR=/var/tmp/xirang-go-tmp.wHjsn9 \
  go test ./internal/backupasset/catalog \
  -run '^TestCatalogIndexerSQLiteDefaultLogicalBatchUsesDatabaseSafePersistenceChunks$' \
  -count=5
ok xirang/backend/internal/backupasset/catalog 0.857s

env GOTMPDIR=/var/tmp/xirang-go-tmp.wHjsn9 TMPDIR=/var/tmp/xirang-go-tmp.wHjsn9 \
  go test ./internal/backupasset/catalog -count=1
ok xirang/backend/internal/backupasset/catalog 5.566s
```

Each exact `/var/tmp/xirang-go-tmp.*` directory above was removed after its command completed.

## Spec-review default binding follow-up

The regression no longer carries a standalone logical-batch constant. It clears the
`BACKUP_ASSETS_CATALOG_BATCH_SIZE` environment override, creates a real `settings.Service` over
the migrated SQLite fixture database, and resolves `backupasset.FoundationService.CatalogConfig`.
It first asserts that the registered default resolves to `BatchSize=2000`, then derives the record
count, fixture entry count, Indexer logical batch and maximum-entry bound from that parsed value.
This makes registry drift fail the regression before Catalog construction.

Post-review verification used one exact temporary directory, removed afterward (both exit 0):

```text
env GOTMPDIR=/var/tmp/xirang-go-tmp.DoVPf0 TMPDIR=/var/tmp/xirang-go-tmp.DoVPf0 \
  go test ./internal/backupasset/catalog \
  -run '^TestCatalogIndexerSQLiteDefaultLogicalBatchUsesDatabaseSafePersistenceChunks$' \
  -count=5
ok xirang/backend/internal/backupasset/catalog 0.845s

env GOTMPDIR=/var/tmp/xirang-go-tmp.DoVPf0 TMPDIR=/var/tmp/xirang-go-tmp.DoVPf0 \
  go test ./internal/backupasset/catalog -count=1
ok xirang/backend/internal/backupasset/catalog 5.734s
```

## Independent reviews and broader local gates

- Fresh Trellis spec-compliance re-review: `SPEC_COMPLIANCE_OK`, with no unresolved Critical,
  Important or Minor findings.
- Independent Trellis quality review: `QUALITY_OK`; its only Minor observation was to record the
  already-completed reviews in `implement.md`.
- Related Catalog/Repository/Search tests, focused Catalog vet, formatting and `git diff --check`
  passed.
- With the project Go 1.26.6 toolchain, backend lint reported zero issues; full `go vet ./...`,
  `go build ./...`, and `go test -race ./internal/backupasset/catalog -count=1` passed.
- The provider package's complete test binary passed when compiled on `/var/tmp` and run from its
  package directory with a short `/dev/shm` fixture root.

A single local `go test ./...` was not recorded as green. On the root filesystem, the provider
preflight correctly rejected the host's reported `FreeInodes=0`; moving all compile and test data to
the 14 GiB `/dev/shm` mount then exhausted that mount's remaining 2.9 GiB while existing 320 MiB and
long-chain recovery fixtures ran. The observed failures were `disk quota exceeded`/`no space left on
device`, not assertion failures. A prior root-filesystem run passed the remaining packages apart from
the known short-socket/inode-sensitive packages, and those affected packages were rerun separately
with suitable short fixture roots. The pull-request CI runner remains the authoritative complete
backend test and coverage gate.
