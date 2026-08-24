# Design — Catalog SQLite safe persistence batching

## Diagnosis boundary

Production supplies a repeatable black-box signature but intentionally does not expose raw errors.
The code-level hypothesis is arithmetic and testable: `catalog_batch_size=2000` feeds one
`db.Create(&entries)` call; a `CatalogEntry` insert binds approximately 17 columns, while bundled
`go-sqlite3` defaults `SQLITE_MAX_VARIABLE_NUMBER` to 32766. The first full batch can therefore
attempt approximately 34000 variables and fail before generation activation.

The hypothesis becomes root cause only after the pre-change SQLite RED fails at that boundary.
If it does not, stop this design and continue systematic diagnosis without implementing it.

## Proposed change

Keep the existing logical Catalog batch and introduce a smaller internal persistence chunk inside
`Indexer.insertBatch`. Use GORM's supported batched create path so hooks, dialect placeholders and
transaction behavior remain framework-owned. The physical chunk is a private constant chosen with
clear headroom below the bundled SQLite variable ceiling for the current `CatalogEntry` column
count; it is not a user setting and does not change Provider pagination.

```text
Provider page / logical batch (up to configured batch size)
  -> canonical validation + identity/proof accumulator
  -> database-safe persistence chunks
  -> remaining proof/source/fence checks
  -> active generation CAS
```

## Correctness constraints

- Do not split or reorder canonical record processing; only split the final persistence statement.
- Do not activate a generation until every physical chunk and existing proof/source/fence check
  succeeds.
- Preserve GORM model hooks so encrypted provider locators never become plaintext at rest.
- Do not couple the safe chunk to SQLite-only application branches; one conservative path works for
  SQLite and PostgreSQL.
- Do not change error responses, stable public enums, migrations, settings, or worker retry policy.

## Tests

1. Pre-change real SQLite `Indexer.Build` with the default logical batch and at least one full batch:
   expected desired result is complete; current implementation must fail due the SQL variable limit.
2. Post-change same test: complete generation, exact count/digest/proof and encrypted stored locator.
3. Existing small-batch, partial, proof mismatch, source drift, lease and lifecycle tests remain green.
4. Repeat focused test to rule out ordering/timing flakes; run package and backend gates with both
   `GOTMPDIR` and `TMPDIR` on a dedicated `/var/tmp` directory when the local `/tmp` quota requires it.

## Rollback

The change has no schema or data migration. Roll back to the previous stable image if Catalog or
Core health regresses; failed generations remain durable evidence and the existing connected point
can be retried by the old worker. Backup files are never deleted or modified by this change.
