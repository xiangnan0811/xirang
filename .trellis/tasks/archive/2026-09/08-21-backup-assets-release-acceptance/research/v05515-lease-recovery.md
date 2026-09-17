# v0.55.15 real-data acceptance and index lease recovery

> Historical diagnosis and implementation record. [v0.55.16 production acceptance](v05516-real-data-acceptance.md) records the successful PR/release CI, user-performed upgrade, declined additional backup and current UI outcomes. Its disposition supersedes the pending CI and pre-deployment prerequisites below; consolidated acceptance remains open.

## Authority and scope

The user requested continuation of the existing three tasks on 2026-09-16, with production acceptance first, independent work branches and implement/check/PR/CI for code changes. Reuse this acceptance task; do not create another child. This record supersedes historical v0.50.x execution instructions for the current repair only. The ten delivered children remain archived. Node-log P1 is separately accepted; preserve current collector settings.

## Reported production evidence

- NAS image v0.55.15, image ID `sha256:d33ee12301e1a4ee3acb31dcf804ff5e0f8027317d49eb9512079aa87a090e55`; running/healthy, restart count 0, started 2026-09-16T09:52:57.850051648Z. Health probe succeeded at 11:40:53Z. Persistent destinations /data, /backup and /logs present.
- LAN HTTP. At 11:55:30Z Admin settings/readiness GETs were 200; enabled and private-network HTTP true from DB. Existing installation acknowledged, inventory complete, digest matched, export root and key domains ready. Counts: 10 candidates, 1 conflict, 0 unsupported, 2 capability gaps. Task 1/4 capability-gap group remains to assess; initial null reason was a diagnostic script filter excluding dotted codes, not absent server evidence.
- At 11:58:47Z all 10 listed Rsync repositories were online/access-active with active task links; no next page. Task 3 repository `0d7d7b3098bdad32426a0807b2a8ee42` selected from current evidence.
- At 12:02Z point `e35fca267e10c228ee6858dcadb787ad` was observed mutable_head, physically online, producing Task 3. Active Catalog 378 complete, 60515 entries; latest 388 failed `catalog_build_abandoned`; stale `mutable_source_changed`; content available/list permitted. This is NOT current-source acceptance.
- At 12:03:59Z generation 378 dated 2026-09-09T17:28:08Z to 17:36:19Z; generation 388 dated 2026-09-09T23:37:50Z to 2026-09-10T00:08:13Z. Repository reconcile interval 15m; build timeout 30m. Task 3 disabled/canceled, last run 2026-08-25T08:21Z. Do not enable it for diagnosis.
- Subsequent current-process metrics: FeatureRequested=FeatureLive=1; Catalog scans success=12, builds complete=5/failed=108, active=0, abandoned reconciled=0. Counters are global, not Task-3-specific.
- Read-only SQLite query found one active Catalog lease: heartbeat 2026-09-09T23:37:49Z, expiry 23:42:49Z, absolute deadline 2026-09-16T23:37:49Z. One active Search lease: heartbeat 2026-09-14T03:49:48Z, expiry 03:54:48Z, absolute deadline 2026-09-21T03:49:48Z. Both lease deadlines elapsed; neither absolute deadline elapsed. No SQL writes performed.

## Diagnosis and requirements

AcquireTx counts active owner slots regardless of heartbeat expiry. ReconcileExpired only expires absolute deadlines. Catalog/Search call Acquire with a stable owner and cannot restart after a process loses a lease until the seven-day absolute deadline. Candidate selection does not exclude disabled tasks; catalog_build_abandoned is retryable. Reproduce this shared index-recovery gap before fixing it. Do not claim all 108 failures share this cause without evidence.

Required behavior: abandoned Catalog/Search indexing can acquire a fresh fenced attempt after the old lease expires, without waiting for its absolute deadline. Live leases remain exclusive; old fences cannot renew, release the new lease, or commit results. Preserve unrelated holder takeover/recovery semantics, explicit publication deadlines, and transaction rollback. No schema or default-duration change; no production SQL repair.

## Design and implementation plan

1. Inspect LeaseService, Catalog/Search callers and existing takeover tests. Add deterministic RED regression(s) using fixed clocks and a restarted service: old heartbeat expires while absolute deadline remains in the future.
2. Implement the smallest transactional recovery boundary for Catalog/Search owner slots. Prefer exact point/holder/owner reclamation with expiry predicate and fresh fence over widening the global sweeper to every holder. Validate admission before mutation; retain DB uniqueness and concurrency handling. If inspection shows a safer equivalent, document the choice here.
3. Test both indexing holders, live-lease refusal, both expiry boundaries, stale-fence rejection, rollback, and unaffected non-index holder behavior. Add real Catalog/Search restart regression coverage where needed to verify generation advancement rather than mirroring SQL.
4. Run focused tests and applicable backend lint/build; independent trellis-check covers final diff and tests. Reuse repository PostgreSQL parity facilities where feasible; record platform gaps explicitly.
5. Commit/push PR, monitor required CI, merge only green, monitor post-merge/release automation. Code repair may warrant a patch release; task-record-only commits do not warrant one.
6. Production acceptance remains OPEN pending released fix, read-only upgrade preflight/backup evidence before any deployment change, and fresh Catalog/Search/transport/UI/health acceptance. Parent and P3 ordering remains unchanged.

## Verification

Local RED reproduced both holders returning ErrLeaseHeld with expired heartbeat and future absolute deadline, including real restarted Catalog/Search indexers. The PostgreSQL restarted-Catalog regression also exposed its existing `COUNT(*) ... FOR UPDATE` rejection (SQLSTATE 0A000) in ReconcileAbandoned; repairing this exact existence/row-lock query is necessary for the same supported restart path. No broader database rewrite is authorized.

Implementation changes only the exact index owner-slot acquisition and the PostgreSQL Catalog lease-existence query. No migration, setting-default change, API change, production mutation or acceptance pass is claimed.

Implement verification passed with Go 1.26.6: complete backupasset/Catalog/Search package tests; new recovery regressions under race detector repeated ten times; PostgreSQL 18 real restarted-indexer subtests; backupasset lint (zero issues); backend build. The new base lease PostgreSQL contract is wired into required CI, and actual indexer regressions run under existing Catalog/Search PostgreSQL behavior suites.

Independent trellis-check passed with no unresolved production-code findings. It added admission-before-mutation and real PostgreSQL renewal-versus-reclamation overlap coverage (`pg_blocking_pids` proves the UPDATE waited on the renewal before rechecking the predicate). Both index holders passed `-race -count=5`; complete PostgreSQL Catalog/Search behavior suites, the exact required PostgreSQL CI runner, subtree lint and whole-backend build passed.

Full backend verification hit local environment failures: long temporary paths exceeded Unix socket path length and the /var/tmp filesystem reported zero free inodes to the provider fixture. Go 1.26 testing.TempDir prefers GOTMPDIR; setting TMPDIR alone does not isolate runtime paths. Unsetting GOTMPDIR fixed targeted processing/provider runs, but full compilation in /tmp hit disk quota. Separating compilation from runtime using `-exec 'env -u GOTMPDIR TMPDIR=/tmp'` resolved compilation; the unrelated 320 MiB updater test then failed in /tmp. Its complete package passed uncached with `-exec 'env -u GOTMPDIR TMPDIR=/var/tmp'` (short path, disk-backed storage). No product/test behavior was changed for these fixture conditions. Do not claim a single green local full-backend invocation; require the complete PR CI gate before merging. CI outcomes pending.
