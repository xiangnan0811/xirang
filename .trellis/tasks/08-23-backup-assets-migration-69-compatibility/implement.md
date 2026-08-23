# Implement — 迁移 69 孤儿运行兼容与脏状态失效保护

Do not run `task.py start` until the user approves this planning set in a later message.
No product-code edits belong in the approval-waiting turn.

## Phase 0 — task start and RED baseline

- [ ] After approval, start this task on a dedicated `codex/` work branch from current
  `origin/main`; do not reuse the planning branch as an implementation shortcut.
- [ ] Re-read backend database/deployment/quality specs and this task's root-cause research.
- [ ] Record exact baseline SHA and current latest migration version.
- [ ] Add failing paired integration fixtures first:
  - terminal orphan survives 69 with snapshot 0;
  - active/unknown orphan still fails atomically;
  - clean 71 missing 69 schema fails before migration 72 writes;
  - dirty + `ALLOW_DIRTY_STARTUP=true` still fails.
- [ ] Run focused tests and save the genuine RED output in task research/journal.

## Phase 1 — TaskRun compatibility contract

- [ ] Introduce one package-owned closed helper/constant set for active, terminal and
  legacy-unknown snapshot semantics; avoid copied status lists where executable code needs them.
- [ ] Audit all TaskRun creators and snapshot consumers; add explicit zero rejection at authority
  boundaries without exposing the internal field.
- [ ] Add single/batch node-delete tests proving runs are retained.
- [ ] Add post-69 deletion test proving a positive snapshot remains frozen.
- [ ] Run model/task/repository focused tests and race where concurrency is involved.

## Phase 2 — repair 000069 for installations below 69

- [ ] Change SQLite 69 backfill/guard for terminal orphan → 0 and all unsafe cases → rollback.
- [ ] Change PostgreSQL 69 with the same closed product inside its existing transaction.
- [ ] Preserve row counts and every non-snapshot field in integration snapshots.
- [ ] Keep raw INSERT requiring positive matching snapshots on both engines.
- [ ] Verify a representative high-count orphan fixture without copying production data.

## Phase 3 — add 000072 convergence migration

- [ ] Add paired SQLite/PostgreSQL up/down files.
- [ ] Normalize PostgreSQL CHECK/function/trigger definitions from both upgrade paths.
- [ ] Normalize SQLite triggers without rebuilding the shared TaskRun table.
- [ ] Add legacy-unknown status immutability.
- [ ] Add used-down metadata admission and body guard; sentinel data must keep version 72 clean.
- [ ] Update version constants, paired-file assertions, PostgreSQL selectors and migration checker.
- [ ] Compare schema definitions reached from revised-69 and original-clean-71 fixtures.

## Phase 4 — remove dirty auto-force and detect clean drift

- [ ] Delete `allowDirtyStartup` behavior and every startup call to `migrate.Force`.
- [ ] Make dirty checking unconditional and position it before any unsafe mutation.
- [ ] Add `ErrMigrationSchemaDrift` (or equivalent typed sentinel) with sanitized operator guidance.
- [ ] Implement minimum 69 schema pre/post validation for SQLite and PostgreSQL search_path.
- [ ] Prove a false clean 71 fixture stays byte/logically unchanged after rejection.
- [ ] Update dirty tests: env unset/false/true/1 all reject; source/spy assertion prevents Force regressions.

## Phase 5 — docs and executable specs

- [ ] Remove the escape hatch from `.env` examples and current deployment/env-var recovery docs.
- [ ] Document backup-first dirty/schema-drift response without publishing host-specific commands,
  data paths or production fingerprints.
- [ ] Update `.trellis/spec/backend/database-guidelines.md` with migration 72, legacy_unknown,
  paired parity and clean-schema validation contracts.
- [ ] Update deployment/quality spec only where runtime behavior changed.
- [ ] Keep `BACKUP_ASSETS_ENABLED=false` and Worker publication contracts unchanged.

## Phase 6 — verification

- [ ] SQLite migration69/72 focused tests, including apply/down/dirty/schema-drift.
- [ ] Required real PostgreSQL migration69/72 suite with `REQUIRE_POSTGRES_MIGRATION_TEST=1`.
- [ ] Node repository and TaskRun focused tests.
- [ ] `scripts/check-backup-asset-migration.sh` and its self-tests after extending coverage.
- [ ] `cd backend && go test ./internal/database ./internal/model ./internal/task ./internal/repository/gorm -count=1`.
- [ ] Repetition/race for new transition and migration helpers.
- [ ] `cd backend && go test ./...`, `go build ./...`, backend lint/vet.
- [ ] doc freshness, privacy/source scans, JSON/JSONL validation, `git diff --check`.
- [ ] Independent review against this PRD before commit.

## Phase 7 — delivery and post-merge evidence

- [ ] Commit only after gates pass; open PR with the production No-Go link but no private evidence.
- [ ] Monitor every required CI job; repair failures on the same branch.
- [ ] Squash merge only after required checks are green, then sync local main.
- [ ] Monitor Release Please, stable GitHub Release, Docker multi-arch publish and expected
  Docker Hub description job.
- [ ] Record the new tag and immutable image digest in this task.
- [ ] Do not archive Child 18; hand the new release back for a fresh production acceptance run.

## Risky boundaries

- Editing an already published 000069 must be paired with 000072 convergence.
- PostgreSQL required tests cannot be replaced by SQLite or SQL text inspection.
- Down guards must protect `schema_migrations` before golang-migrate sets dirty/target version.
- Startup validation must remain read-only and cannot become a hidden auto-repair path.
- No production names, IPs, paths, hashes, logs, commands or database rows enter the repository.

## Rollback

Code rollback uses a normal PR/revert. Database rollback is not a blind `Steps(-1)` when
legacy_unknown rows exist; the 000072 guard intentionally refuses. Production rollback remains
stop → preserve evidence → restore the verified pre-upgrade database → run the prior image.
