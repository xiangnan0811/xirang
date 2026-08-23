# Focused spec contracts — migration repair

This routing note prevents Trellis context injection from truncating the large backend specs. It
does not replace them. Before editing code, re-read the named source specs and use this list to
jump to the task-relevant contracts.

## `.trellis/spec/backend/database-guidelines.md`

- Migration files are paired for SQLite/PostgreSQL and version numbers stay in lockstep.
- Existing installations must migrate safely; SQL is preferred, with a Go pre-fixup only when
  historical drift cannot be expressed safely in SQL.
- Backup-asset migrations and required real-PostgreSQL selectors must include every new version;
  required DSN absence is failure, not a skipped pass.
- Used-down admission must reject before golang-migrate changes the clean version. Test through
  `migrator.Steps(-1)`, not only direct SQL, and compare version/schema/data atomically.
- Durable backup-asset history is preserved; do not delete history to make down/repair succeed.

## `.trellis/spec/backend/quality-guidelines.md`

- Migration/DB errors are never ignored; schema compatibility fixes require focused tests.
- Backend changes require package tests and at least full backend test/build; broader changes add
  lint, race/repetition and explicit denial cases.
- Timing/cancellation tests use test-owned channels for exact ordering, not millisecond sleeps.
- New fixtures that resemble secrets use the `FAKE_*_FOR_TEST_ONLY` naming contract.
- Review must confirm background work is cancelable/shutdown-aware and docs match migrations/env.

## `.trellis/spec/backend/deployment-runtime.md`

- Official image/Compose/ports/mounts stay unchanged unless explicitly in scope.
- Production startup validation and env examples must remain truthful and fail closed.
- `BACKUP_ASSETS_ENABLED` stays false by default; Worker remains optional and unpublished.

## `.trellis/spec/guides/documentation-truth-guide.md`

- Current docs describe only supported runtime behavior; historical changelog facts are not
  rewritten.
- Release, image, support-matrix and operational recovery claims need executable evidence.

## Task-specific additions

- `prd.md` owns legacy_unknown/active-orphan/dirty/schema-drift requirements.
- `design.md` owns the two-path 69→72 schema convergence and down-admission design.
- `research/root-cause.md` owns the sanitized incident chain and privacy boundary.
