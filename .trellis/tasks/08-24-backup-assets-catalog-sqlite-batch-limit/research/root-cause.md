# Root-cause evidence

Status: `hypothesis_pending_red`

## Production signature

- Existing v0.50.4 repository is online/access-active; mutable point is observed.
- Catalog sequences 1 through 4 automatically attempted and failed with `catalog_build_failed`.
- Sequence 1 duration was about 397 ms; sequence 4 duration was about 367 ms.
- No active generation or indexed entries exist; content/list capability remains available.
- Task 3 had no active run and no new run during the failures.

## Code arithmetic

- Registered default `backup_assets.catalog_batch_size`: 2000.
- `Indexer.insertBatch` currently calls one `db.Create(&entries)` per logical batch.
- `CatalogEntry` persists approximately 17 columns, yielding about 34000 bind variables for 2000 rows.
- Bundled `github.com/mattn/go-sqlite3@v1.14.47/sqlite3-binding.c` defaults
  `SQLITE_MAX_VARIABLE_NUMBER` to 32766.

This explains a deterministic fast failure at the first full persistence batch, but remains a
hypothesis until a pre-change real SQLite `Indexer.Build` test fails for that reason. No raw
production error, path, name, locator, content or credential is required to prove it.
