# Recent journal recovery — 2026-09-16

## Delivered baseline

- Product PR #533 squash: `e34809d49f84d3cc26492b06d8395b80afc69e88`.
- Release PR #534 / v0.55.14: `3a315c61a3b79c208bc567e3423b77c87f235267`.
- Product CI `35053027727`, release PR CI `35054592960`, release-main CI
  `35056294654`, Release Please `35056294684` succeeded.
- Docker publication `35056314406` succeeded after retrying its initial exact-main
  CI wait timeout. Published linux/amd64 and linux/arm64; stable version aliases and
  latest resolved to `sha256:88c00df22e1ab0c7a70102b1223a45bc82253e23cbca188970df7952a3db5d6e`.
- README unchanged; Docker Hub description sync was not expected.

## Sanitized NAS evidence

The user reports v0.55.14 installed and one journal-only node enabled. Completed
fetch count and `output_limit` count rose together (2, 3, 6), while queue depth
returned to zero and in-flight returned from one to zero. No ingestion series
appeared. This supports resource recovery, not successful collection.

A separately measured latest-200 JSON output was 78,639 bytes. Read-only SQLite
inspection found a journal cursor with nonempty text, last updated on 2026-05-08.
Existing generation unconditionally resumes any nonempty cursor without time or
entry bounds. No inference is made about who created/enabled that cursor. The
older token explains why a latest-200 sample does not reproduce the actual fetch.

Generated commands also place framing tokens directly in shell source. Executing
them with both sh and bash yields syntax failure; the journal command can execute
before failure, masking the defect via permissive missing-delimiter parsing.

## Approved scope

User rejected months-long catch-up and approved recent bootstrap, bounded short-gap
catch-up and honest skipped-history reporting. Default policy: last one hour, at
most 200 entries per batch. Long-idle/unknown cursor age resets to recent tail;
recent cursors consume oldest-first within that window. Preserve existing stored
logs and remote journal; no NAS SQL mutation, schema change or historical-backfill UI.

Retention=0 inherits the system default and is unrelated to catch-up bounds. Empty
file whitelist is valid for journal-only collection. Neither setting caused this
failure, and neither is changed by the fix.

## Verification and delivery

Portable seek behavior was checked against official systemd
[v247 journalctl.c](https://raw.githubusercontent.com/systemd/systemd/v247/src/journal/journalctl.c)
and [current journalctl-show.c](https://raw.githubusercontent.com/systemd/systemd/main/src/journal/journalctl-show.c).
Cursor positioning precedes line-tail positioning; `--after-cursor` with a positive
line limit emits the next chronological batch. Older versions reject combining a
time window with cursor options. A metadata probe avoids that incompatible pair;
both invocations remain under one deadline and cumulative output budget.

Implementation now carries cursor update time, validates recent cursor identity
and event time, prints and checks complete shell framing, and reports resets only
after successful persistence. Actual generated POSIX shell tests reproduce the old
450-entry bootstrap selecting the wrong boundary and the masked delimiter failure.
The corrected implementation covers chronological batches, stale/unknown/vacuumed
cursors, empty reset then new entries, mixed file collection, quoting, malformed
output, and command/insert/cursor-save failure paths.

Local implementer verification passed 50 package repetitions, 10 race repetitions
and package lint. Root independently passed full backend build, vet and lint
(0 issues). Full backend tests passed across the disk-backed run and fixture-specific
reruns: five filesystem/socket packages need ordinary `/tmp`, while the updater's
320 MiB test needs short disk-backed `/home/murray/xnt`. These are the same host
constraints recorded for v0.55.14. Doc freshness and diff whitespace checks passed.

Independent full-scope review found no blocking issues and added exact cumulative
10 MiB success, exhausted-probe-budget rejection and multiple-file partial-line
offset tests. Full package tests passed three more repetitions, the added cases
passed three race repetitions, and package lint/vet passed. Related sshutil,
credentialaudit and lifecycle race tests also passed.

Remote CI/release gates remain pending. Production acceptance remains open until
the new image is installed and two or more cycles are observed.

## Single-node acceptance after image delivery

1. Confirm the deployed version/digest and existing persistent mounts/backup before
   any stop/restart. Keep the affected source disabled during the upgrade.
2. Preserve the database, existing logs, cursor rows and remote journal. No SQL
   reset is required: stale-cursor handling belongs to the product.
3. Enable only the previously affected journal source; leave the file whitelist
   empty unless file collection is actually wanted. Retention=0 remains valid.
4. Read protected metrics and cursor metadata, without exposing the token or raw
   log body. Observe at least two scheduler cycles. Queue/in-flight recovery alone
   is insufficient: successful fetch and current cursor state must be established.
5. A busy node should ingest recent entries and advance its token. A quiet node may
   legitimately ingest zero entries; a successful empty rebaseline should clear the
   stale token and persist a current update time, then consume later new entries.
6. Verify reset observability once for the old nonempty cursor and no continuing
   output-limit/command failures. Existing counters are cumulative; compare deltas,
   not whether a historical error counter has returned to zero.
7. Do not infer recovery of all nodes from one node. Restore others in small batches
   only after this evidence. If failure recurs, disable the affected source and
   preserve safe diagnostics rather than deleting history or widening hard limits.
