# Backup Assets Search Startup Isolation

## Goal

Keep the service and authenticated API available when one RecoveryPoint cannot
build its Search projection, while preserving durable failure evidence and
Search's fail-closed projection rules. Resolve the exact production
`search_invalid_security_state` conflict so the existing real Rsync Catalog can
be searched without SQL repair or fabricated output.

## Production evidence

- v0.50.7 startup repeatedly terminated with
  `backup asset search catalog changed` before the router became available.
- Emergency recovery took a verified SQLite backup and changed only
  `system_settings.backup_assets.enabled` from `true` to `false` by exact CAS.
- v0.50.6 is now running/healthy with restart count reset to zero, health HTTP
  200, Task 3 paused/disabled, zero active runs, and zero node-log collectors.
- The active mutable Catalog is complete with 60,515 durable entries.
- Ten Search generations are failed/inactive; the latest has
  `expected_document_count=60515`, `written_document_count=0`, and stable code
  `search_invalid_security_state`.
- Catalog Indexer writes the known state `sealed`; Search currently accepts only
  empty, `non_secret`, `secret`, or `unknown` values.

## Requirements

- A per-candidate Search `Build` failure MUST be recorded in existing durable
  and metric evidence but MUST NOT fail the whole startup pass.
- Startup MUST still fail for invalid worker configuration, key setup/rewrap,
  abandoned-generation reconciliation, overlay reconciliation, candidate
  enumeration, and caller context cancellation.
- Candidate goroutines MUST be joined and active-build metrics reset before the
  pass returns; cancellation and fence outcomes remain distinct.
- Runtime startup with only candidate-local failures MUST leave Search ready so
  the API can report unavailable/non-authoritative coverage and the background
  worker can retry.
- Search MUST interpret only the known Catalog interoperability value `sealed`
  as conservative sensitivity `unknown`; arbitrary non-empty future values MUST
  remain rejected with `search_invalid_security_state`.
- Indexer source/count/identity/normalization/key/fence/activation validation
  MUST remain fail closed. Failed or partial Search output MUST never activate.
- No migration, API/frontend schema, raw locator exposure, asset data rewrite,
  Provider mutation, synthetic Search generation, or manual Catalog repair is
  allowed.
- Production `backup_assets.enabled` remains false and Task 3/node-log
  collectors remain disabled until the hotfix image is deployed and guarded
  formal-API re-enable acceptance begins.

## Acceptance criteria

- [ ] A genuine focused RED proves a candidate-local Search build error escapes
  `StartupPassWithConfig` and makes runtime Search unready before the fix.
- [ ] Candidate error matrix is isolated while metrics and durable Indexer
  failure evidence remain intact.
- [ ] Infrastructure/config/context failures still propagate and cancellation
  joins every started candidate.
- [ ] A genuine focused RED proves Catalog `sealed` produces
  `search_invalid_security_state`; GREEN maps it only to `unknown` and activates
  a complete Search projection.
- [ ] Empty/known sensitivities retain behavior; an arbitrary future state still
  fails inactive with the stable error code.
- [ ] Focused repetition/race, runtime and Search packages, vet, pinned lint,
  formatting, privacy/static checks, and diff checks pass.
- [ ] Independent Trellis check has no unresolved Critical/Important findings.
- [ ] PR CI is fully green, merged, and v0.50.8 release/Docker automation is
  complete before production upgrade.
- [ ] Production is upgraded with verified backup/rollback; feature is re-enabled
  only through the authenticated Settings API.
- [ ] Search becomes active/complete with positive documents, exact-point file
  search returns HTTP 200 with a real opaque AssetRef, and UI metadata/content
  preview plus final health/privacy/collector acceptance passes.

## Out of scope

- Weakening Search normalizer, identity, count, key, lease, or activation checks.
- Treating unknown future security states as safe.
- Deleting the ten failed Search generations or changing Catalog rows in SQL.
- Restarting Task 3 or starting node-log P1 before real-data preview acceptance.
