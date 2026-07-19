# Child 10 Implementation Evidence

## 0. Evidence boundary

This file records actual Phase 2 implementation and verification evidence for
`.trellis/tasks/07-19-backup-assets-worker-protocol`. Planning claims do not
turn future gates green; every command remains `not_executed` until its output
is observed in this worktree.

```text
evidence date:            2026-07-19 through 2026-07-20 Asia/Shanghai
worktree:                 /home/murray/.codex/worktrees/8f0f/xirang
branch:                   codex/backup-assets-worker-protocol
required baseline:        2ce71339b7f10fe759c0009ff01a100e589a700c
task status:              in_progress
parent status:            planning
dispatch mode:            inline
staging/commit/push/PR:   not_executed
```

## 1. Phase 2 entry

Program control independently reviewed the focused planning package and the
user separately approved both planning and implementation. The workflow entry
command executed once:

```bash
python3 ./.trellis/scripts/task.py start \
  .trellis/tasks/07-19-backup-assets-worker-protocol
```

The resulting child status is `in_progress`; the parent remains the
`planning` program tracker. The preflight confirmed the dedicated branch and
the three baseline refs at `2ce71339b7f10fe759c0009ff01a100e589a700c`.
Migrations `000068` through `000071` remain reserved and untouched.

## 2. Approved exact-manifest amendment

Program control approved the following focused file-ownership amendment on
2026-07-19. It does not change behavior or product scope.

Added to the implementation manifest:

```text
backend/internal/backupasset/lease.go
backend/internal/backupasset/repository/testutil_test.go
backend/internal/backupasset/runtime/admission_controller_test.go
backend/internal/backupasset/processing/derived_manifest.go
backend/internal/backupasset/processing/derived_manifest_test.go
```

Removed from the implementation manifest because the implementation did not
modify them:

```text
backend/internal/backupasset/content/source_contracts.go
backend/internal/backupasset/content/source_contracts_test.go
backend/internal/backupasset/repository/content_read_test.go
```

`backend/internal/backupasset/repository/content_read.go` remains in the
approved modify manifest. `implement.md` Sections 2, 15 and 16 carry the same
amendment. The final pre-stage equality check must compare every actual tracked
path to the exact Section 15 manifest and reject both missing and extra paths.

## 3. Implementation evidence

The implementation remains inside the frozen Child 10 boundary:

- paired SQLite/PostgreSQL migration `000067_backup_asset_processing` only;
- persistent closed processing queue, work identity, interests, leases/fences,
  grants, protocol transport/client, Derived Store and reconciliation;
- `backup_assets.enabled=false` by default and Command Provider remains typed
  unsupported;
- no frontend, real capability/updater, Worker image/Compose/container sandbox,
  Provider byte mutation, release or deploy implementation.

The final implementation review also closed these concrete defects without
changing the approved behavior boundary:

- heartbeat renews the Worker lease, RecoveryPoint lease and all live
  `issued|active` grants atomically; expired grants are not resurrected and
  in-memory Input/Sink deadlines follow the persistent authority;
- Sink validation uses the existing SQLite conflict retry path rather than
  misclassifying transient locks as permanent grant failures;
- server and Worker-client TLS material rejects symlinks, non-regular files and
  group/world-readable private keys;
- Processing SQLite test fixtures use a per-open sequence in their shared-memory
  DSNs, so `go test -count=N` cannot inherit Worker/Derived rows across runs;
- the cross-engine migration fixture binds projection booleans as Go `bool`
  values, allowing PostgreSQL to reach the intended CHECK-constraint test.

## 4. Verification evidence

### 4.1 Backend, race and engine parity

Fresh passing commands after the last source/test edits:

```text
pass  go test ./... -count=1
pass  go test -race ./internal/backupasset/processing ./internal/backupasset/content \
        ./internal/backupasset/repository ./internal/backupasset/runtime \
        ./internal/api/... ./cmd/asset-worker ./cmd/server -count=1
pass  go test -race ./internal/backupasset/processing -count=3
pass  golangci-lint run ./...                     # 0 issues
pass  go build ./...
pass  go test ./internal/database \
        -run '^TestBackupAssetMigration067(SQLite|PairedFiles)$' -count=1
pass  go test ./internal/backupasset/processing \
        -run '^TestProcessingBehaviorSQLite$' -count=1
```

The first `go test -race ./internal/backupasset/processing -count=3` run was a
required RED reproduction: fixed `t.Name()` SQLite shared-memory DSNs retained
rows across the second/third package iteration. After adding the per-open test
sequence, the identical command passed in 7.073 seconds.

A real PostgreSQL 18 container was then started locally. The first Docker
bridge attempt failed before startup with the raw environment error
`failed to add the host ... pair interfaces: operation not supported`.
The reviewed fallback used host networking with PostgreSQL bound only to
`127.0.0.1:5432`; the container reported healthy. The first migration run
correctly exposed the integer-to-boolean fixture defect described above. After
the focused correction, both required selectors passed:

```text
pass  REQUIRE_POSTGRES_MIGRATION_TEST=1 go test ./internal/database \
        -run 'Test(BackupAssetMigration0(62|63|64|65|66|67)Postgres|PostgresTimestamptzScanUsesConfiguredUTC)' \
        -count=1
pass  REQUIRE_POSTGRES_PROCESSING_TEST=1 go test \
        ./internal/backupasset/processing \
        -run '^TestProcessingBehaviorPostgres$' -count=1
```

The temporary container used `--rm`, was stopped after the tests and no
Child-10 PostgreSQL container remains.

### 4.2 Swagger, repository and CI-equivalent scripts

```text
pass  make check
      backend lint/tests/build; frontend 157 files and 879 tests; production build
pass  node web/scripts/check-bundle-budget.mjs
      main JS 498.09/500.00 KiB; main CSS 104.21/105.00 KiB
pass  npm audit --audit-level=moderate             # 0 vulnerabilities
pass  go run golang.org/x/vuln/cmd/govulncheck@latest ./...
      0 reachable vulnerabilities
pass  bash scripts/check-doc-freshness.sh          # exit 0, two reviewed warnings
pass  bash scripts/check-doc-freshness.test.sh
pass  bash scripts/check-migration-utc-safety.sh   # 68 migration files
pass  bash scripts/check-migration-utc-safety.test.sh
pass  bash scripts/check-asset-content-nginx.sh
pass  bash scripts/check-asset-content-nginx.test.sh
```

The doc-freshness script is non-blocking by design. Outside GitHub it compares
`HEAD~1` to the dirty worktree, so it also reported the merged Child 9
`web/src/router.tsx` change. It reported the current backend router because
Child 10 intentionally keeps README/public documentation out of scope; the
paired migration documentation is updated in the backend database spec.

The workspace did not have a `swag` binary, so generation used the same pinned
tool through `go run github.com/swaggo/swag/cmd/swag@v1.16.6`. Two consecutive
generations produced the same tracked artifact, and the pre/first/second
`docs.go` SHA-256 was consistently:

```text
60f68cebdb0ba25e0dd4bae2682e0a8a9515f5b13345edaf7ebccbb77748dd1b
```

Generation emitted the known third-party `pkg/sftp` octal-constant warnings
but exited zero. Ignored Swagger JSON/YAML and build/coverage outputs are
removed before the final scope proof.

## 5. Current gate ledger

| Gate/action | Status |
|---|---|
| focused planning review and implementation authorization | executed 2026-07-19 |
| `task.py start` / child `in_progress` | executed 2026-07-19 |
| exact-manifest amendment | approved and recorded 2026-07-19 |
| product/model/migration/test implementation | implemented; final scope review pending |
| SQLite 067 apply/down and processing behavior | pass |
| real PostgreSQL 062-067/UTC and processing behavior | pass against local PostgreSQL 18 |
| focused race/lease/fence/cancel/quarantine/crypto tests | pass, including Processing `-count=3` |
| Swagger generation/freshness | pass; deterministic tracked hash recorded |
| backend lint/build/full `go test ./...` | pass |
| full repository `make check` / bundle budget | pass |
| audit/govuln/doc/UTC/nginx scripts | pass with reviewed non-blocking doc warnings |
| Trellis validate/manifest equality/scope/contradiction/diff scans | pass: validate 0 errors; exact 82/82; placeholder/current-approval contradictions 0; forbidden product paths 0; `git diff --check` pass |
| stage/commit/push/single PR/required CI | `not_executed` |
| merge/post-merge automation/main sync | `not_executed` |
| Release Please PR #386 | `not_executed`; forbidden/out of scope |
| release/deploy/Worker image/Compose/UI/feature enablement | `not_executed`; out of scope |

## 6. Final scope and handoff

The final pre-stage scan observed:

```text
branch=codex/backup-assets-worker-protocol
HEAD=main=origin/main=2ce71339b7f10fe759c0009ff01a100e589a700c
child_status=in_progress; parent_status=planning
manifest_count=82; actual_count=82; equality=pass
placeholder_scan=0
approval_current_contradictions=0
forbidden_product_paths=0
staged_paths=0
generated_artifacts=0
git_diff_check=pass
```

The scan intentionally treats `not_executed` rows in the planning documents as
historical Phase 1 ledger text; the current evidence records `task.py start`
and implementation as executed while keeping delivery mutations explicitly
pending. `implement.md` no longer contains the stale
“After the remaining separate implementation authorization” wording.

The branch is now at the pre-stage handoff gate. Exact-path staging, the work
commit, `trellis-finish-work` archive/journal commit, push, one PR, required CI
monitoring, squash merge, post-merge automation and main synchronization have
not been executed in this session.
