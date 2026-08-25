# Backup Assets Mutable Catalog Search Projection

## Goal

Allow Search to project an active complete mutable-head Catalog whose manifest expected count is unavailable, while preserving immutable count and drift fail-closed checks.

## Requirements

- Search MUST project an eligible `mutable_head` / `observed` RecoveryPoint from
  its exact active complete Catalog generation even though that generation has
  no manifest-derived expected entry count.
- For a manifest-less mutable Catalog, `WrittenEntryCount` is the bounded source
  count used to create `ExpectedDocumentCount`; the Search generation still
  activates only when the exact projected document count equals that frozen
  value.
- Manifest-backed Catalog generations MUST continue to require
  `WrittenEntryCount == ExpectedEntryCount` before Search accepts them.
- Source fingerprint, point eligibility, active Catalog identity/state,
  Search-key version, lease fence, non-negative counts, and activation-time
  count/Catalog drift checks MUST remain fail closed.
- The fix MUST NOT add a migration, mutate Provider data, expose paths/names/
  content/locators, change the Search HTTP schema, or resume production Task 3.
- The background worker MUST converge through its existing candidate/build path;
  no SQL repair, manual Search-generation creation, or special production API
  is allowed.
- Production acceptance remains gated on an active complete Search generation,
  at least one exact-point file result, UI metadata/content preview, healthy
  container evidence, zero critical errors, and node-log collectors remaining
  disabled.

## Acceptance Criteria

- [ ] A focused SQLite regression first fails because an active complete
  manifest-less mutable Catalog has `expected_entry_count=0`, a positive
  `written_entry_count`, and Search returns `ErrSearchCatalogChanged` before
  generation creation.
- [ ] After the minimal fix, the same fixture builds and activates an exact
  Search generation whose expected/written documents equal the Catalog's
  written entries.
- [ ] Table-driven negative coverage preserves immutable count mismatch,
  mutable source drift, ineligible point state, negative count, Catalog drift,
  and activation count mismatch rejection without active partial output.
- [ ] Existing zero-entry, lifecycle, fence, key, SQLite, and required real
  PostgreSQL Search behavior remains green.
- [ ] `gofmt`, focused repetition/race, Search package tests, vet, pinned lint,
  privacy/static checks, and `git diff --check` pass before PR.
- [ ] PR required CI is green, the PR is merged, and release/Docker automation
  is monitored through the stable image consumed by production.
- [ ] Production Search indexes the existing 60,515-entry Catalog and returns at
  least one opaque file AssetRef without printing filename/path/content.
- [ ] Backup -> Data shows selected metadata and a supported preview; final
  health/restart/schema/error/collector evidence is recorded before node-log P1
  starts.

## Notes

- Parent task: `08-21-backup-assets-release-acceptance`.
- Production evidence on 2026-08-25: v0.50.6 Catalog generation 37 completed
  with 60,515 indexed entries, but exact-point Search stayed `unavailable` with
  zero items and `authoritative_empty=false` beyond one reconcile interval.
