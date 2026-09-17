# Node-log collection lifecycle

Applies to `internal/nodelogs` and its owned SSH execution in `internal/sshutil`.

## Execution and data contracts

- Validate positive timeout and output bounds before credentials or SSH work.
- One operation context covers dial, session creation, command start, reads and wait.
  Cancellation closes the owned transport and joins execution owners before returning.
- Reuse shared SSH execution. Strict ownership must be opt-in when existing callers
  have different transport ownership; never close another consumer's shared client.
- Exactly the byte limit is valid; the next byte fails with a typed output-limit
  error. Failed, canceled or truncated output must never reach parsing or persistence.
- An explicit remote nonzero exit status may retain complete bounded stdout for
  compatibility. Missing exit status and transport/read errors are failures.
- Preserve `errors.Is` for cancellation/deadline/output-limit outcomes. Do not put
  command output, commands, paths or raw transport errors into logs or audit metadata.
- Fetch failure preserves every cursor and inserts zero rows. Successful ingestion
  keeps the existing sanitize, insert, then cursor-save sequence.

## Journal recovery and script framing

- Journal recovery is bounded to the last hour and at most 200 entries per batch.
  Missing/unknown/stale cursor age starts with the recent tail; a valid recent
  cursor resumes oldest-first. Persisted poll time alone does not prove that the
  cursor's journal event is inside the window. Do not replay months of history.
- Reuse the cursor table's existing timestamp; no schema change or production
  cursor deletion is needed. An empty successful reset must not restore the old
  token. A failed fetch must leave all existing stored cursors and logs unchanged.
- Deliberate discontinuity needs safe observability after successful persistence;
  never claim an exact skipped-entry count or continuous history without evidence.
- Print protocol delimiters with quoted `printf`, never as bare shell source.
  Require complete successful framing before parsing. A journal command failure
  must not look like a successful empty journal simply because stderr is hidden or
  the shared runner retains stdout on a nonzero exit.
- Verify command compatibility with older systemd. Do not assume that time-window
  and cursor options compose, or that line-limit direction is independent of seek
  mode. Test generated shell execution, not just expected command substrings.
- Retention controls stored-log deletion, not backfill. Empty file whitelist is a
  valid journal-only configuration. Keep remote journal and existing stored logs.

## Scheduling and shutdown

- Claim a node before enqueue; one queued or executing job per node. Roll back the
  claim on enqueue rejection; release it with a defer across every worker exit.
- Never hold the state lock during SSH/DB work. Queue saturation emits at most one
  aggregate warning per scheduling pass, with counts and queue capacity/depth.
- Shutdown owns cancellation independently of the parent context. Only one owner
  closes the jobs channel; completion means all workers have joined.
- Repeated shutdown and shutdown before Run are safe. A caller deadline is an error,
  not proof that workers have stopped. A stopped scheduler is not restarted.
- New scheduler metrics have no node/path/host labels. Rejection reasons are the
  closed set `full|shutdown`; fetch reasons distinguish timeout/cancel/output limit.

## Required regression evidence

Use test-owned channels for blocked Start/read/Wait, transport close and worker
completion. Cover exact limit versus limit+1, nonzero exit versus missing status,
all claim-release paths, queue recovery, concurrent/idempotent shutdown, and
unchanged cursors/zero inserts on failure. Repeat nodelogs tests and run under race;
run related sshutil, credential-audit and lifecycle tests plus full backend gates.

Recovery coverage must include initial/stale/recent and vacuumed cursors, empty
reset followed by new entries, successive batches exceeding 200 entries, rolling
window expiry despite recent poll time, mixed journal/file sources, shell quoting,
command errors and incomplete framing. Execute the generated POSIX shell against
deterministic command fixtures and temporary files.

Code/CI delivery does not establish production recovery. Verify the deployed
image and current configuration separately, then observe an authorized low-risk
node for at least two collection cycles before enabling additional nodes.
