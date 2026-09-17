# Production acceptance — 2026-09-16

Status: accepted by the user. Scope: the one journal-only node currently requiring
collection. The user explicitly confirmed that other nodes do not currently need
collection and that this task can be considered passed. Restoring other collectors
is therefore not an outstanding acceptance requirement.

## Evidence supplied by the user

After upgrading to v0.55.15, two observations approximately four minutes apart show:

| Observation | First | Second |
|---|---:|---:|
| Completed fetches | 8 | 17 |
| Ingested journal entries | 344 | 506 |
| Successful stale-poll recovery resets | 1 | 1 |
| Queue depth | 0 | 0 |
| In flight | 1 | 0 |
| Cursor update time (UTC) | 09:59:07 | 10:03:07 |
| Latest stored event (UTC) | 09:59:07 | 10:03:05 |

Neither observation exposed fetch-error or queue-rejection series. The cursor
remained nonempty and its update time advanced. The first SQL row count (362)
followed the metrics snapshot (344) while a fetch was in flight; these are separate
observations, not an atomic count comparison. The second count was 506 in both.

This establishes recent ingestion, successful one-time stale cursor recovery,
continued polling and queue/worker recovery for the accepted production scope.
It does not claim that all nodes were enabled or that a production fault-injection
test was performed. Failure/timeout paths were covered by repository tests.

No raw cursor, log body, host identifier or credential is recorded here. No
production configuration or database mutation was performed by this archive work.

## Delivery evidence

- Repair PRs #533 and #535, release PRs #534 and #536 are merged.
- Final release: v0.55.15, commit `4a0c0aed105200fae74eeff5cd4c30cf1c3153b7`.
- Product CI `35071234286`, product-main CI `35074127942`, release PR CI
  `35074344070`, release-main CI `35077542331`, and Release Please `35077542445`
  succeeded. Local repeated/race tests and independent reviews also passed.
- Docker publication `35077559241` succeeded on attempt 2 after the first attempt
  exhausted its wait for still-running main CI. No release protection was bypassed.
- Registry-verified linux/amd64 and linux/arm64 index:
  `sha256:15e82678aa5d7d4f2fa68685ab9f5fb095895df6ff13b8415637535bff59188d`.

## Closure

Archive the existing P1 task as completed. This follow-up changes Trellis records
only; no new GitHub Release or Docker Hub image publication is expected. Remaining
backup acceptance and quality-debt tasks retain their separate scope and status.
