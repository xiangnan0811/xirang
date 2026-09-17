# Independent GitHub review of v0.50.2 (2026-08-21)

Source: Alan, after reviewing latest `main`, PR #446, PR #448, Trellis closeout records, related backend/frontend, and GitHub Actions raw logs.

This is **not** a production walkthrough. The reviewer did not re-clone or re-run tests locally (GitHub DNS unavailable in that environment). Code judgement is from GitHub `main` and merged PR diffs. CI claims are from Actions logs, not from a local run.

## Verdict

- Previous code-level P0 and P1 findings are substantively fixed.
- This round found no new P0 or P1 code defects.
- Default-off, controlled-range enablement / pilot: **Go**.
- Declared-support Core-only, Restic, Rsync, Rclone Portable: **conditional Go**.
- Full matrix including Rclone Native AWS: **hold**, unless Native AWS is formally excluded from this GA.
- Trellis parent archive: **wait** until real-environment final acceptance.

## Code merge quality

**Go.** No new P0/P1 that should block merge or emergency rollback. Previous issues closed: leftover snapshot read bypass, enablement half-open, search audit fail-open, proof renew, CI gates, risk-ledger honesty.

## Default-off controlled enablement

**Go**, with:

- `BACKUP_ASSETS_ENABLED=false`
- Admin-only inventory, ack, enable
- Core-only as default shape
- Worker explicitly enabled if used
- Rclone Native AWS not in the official support matrix
- PromQL alerts installed in the deploy environment before enablement
- Fast disable / rollback path kept
- Observe at least one full backup cycle after first enablement

## Wide GA

**Conditional Go.** Becomes unconditional after the real provider/deploy matrix. Native AWS must not block other providers if it is formally excluded.

## Parent archive

**Do not archive now.** Parent correctly remains `planning`. Code-closed and release-accepted are different. After this child passes, parent has archive conditions.

## P2 (non-blocking)

1. SLO rules are real PromQL but not auto-installed into Prometheus/Alertmanager/Grafana/Xirang persistent alert storage. Rule `backup_asset_search_audit_fail` actually counts all search 503s. Doc SLO window vs rule window must match. search build backlog / abandoned build signals are not all in the rule set.
2. `enablement_succeeded_at` can stamp before the full hot-enable transaction lands; rollback restores runtime live but may leave the stamp.
3. Soft CI edges: unexpected `console.warn`/`console.error`/unmatched MSW should fail over time; occasional full Worker Compose profile on arm64 self-hosted.

## Previously closed (round-2 confirmation)

1. Old snapshot read APIs: **410 Gone**. P0 closed.
2. Enablement + Search restart: atomic + `FeatureLive()`. P1 closed.
3. Search audit fail-closed on successful searches. P1 closed.
4. Preview proof reuse with exact binding. P2 closed.
5. CI hard gates. P1 closed (Codecov upload remains soft; coverage floor is hard).
6. Load script 10k catalog executed in CI. Not million-row / soak certification.
7. Risk ledger honesty. Closed.

## Still missing (runtime evidence, not code holes)

1. Real production-shaped provider/deploy/browser/fault/rollback matrix.
2. Rclone Native AWS live suite (or formal exclusion).
