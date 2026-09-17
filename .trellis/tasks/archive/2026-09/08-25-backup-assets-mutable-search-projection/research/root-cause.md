# Root Cause Evidence — Mutable Catalog Search Projection

## Production facts

- v0.50.6 is running and healthy with schema `72|0`.
- Repository/access/link/mutable point are online and active; Task 3 is paused,
  disabled, and has zero active runs.
- Formal Repository Reconcile refreshed the exact mutable point observation.
- Catalog generation 37 completed with coverage complete and 60,515 indexed
  entries.
- Exact-point `type=file` Search remained HTTP 200 but reported unavailable
  coverage, zero results, null total, and `authoritative_empty=false` after more
  than one one-minute Search reconcile interval.

## Code proof

- Mutable Catalog build has no manifest, so Catalog writes an expected count
  only when `frozen.manifest != nil`
  (`backend/internal/backupasset/catalog/indexer.go:400-410`). Catalog status
  likewise exposes `expected_entries` only when `ManifestID != nil`
  (`catalog/service.go:569-575`).
- Search candidate listing admits every active complete Catalog without an exact
  active complete Search generation (`search/indexer.go:249-273`).
- `Indexer.Build` calls `loadFrozenProjection` before acquiring a lease or
  creating a Search generation (`search/indexer.go:100-119`).
- `loadFrozenProjection` rejects any
  `WrittenEntryCount != ExpectedEntryCount`, including the valid mutable shape
  `60515 != 0` (`search/indexer.go:299-328`).
- Search worker discards the periodic pass error after recording low-cardinality
  failure metrics, so the same candidate remains unavailable without durable
  generation failure evidence (`runtime/search_worker.go:83-121,181-233`).
- The existing correct post-freeze path already sets Search
  `ExpectedDocumentCount` from Catalog `WrittenEntryCount`, streams the exact
  Catalog rows, and requires physical document count == written == expected at
  activation (`search/indexer.go:331-415,546-587`).

## Contract source

The approved Search design says the worker freezes an active complete Catalog,
streams its exact rows, and activates only after generation-count and fence
revalidation. It explicitly includes legacy observed mutable heads and says a
zero-entry complete Catalog can activate an authoritative zero-document Search
generation. Manifest expected count is not required for mutable enumeration.

## Safe repair

Accept positive/zero `WrittenEntryCount` as the frozen expected Search document
count only for a manifest-less mutable head. Preserve equality for manifest-
backed/non-mutable Catalogs and retain Search's physical document-count equality
at activation. No production SQL repair is permitted.
