# Root cause — sanitized node-log collector evidence

## Classification

This is a pre-existing node-log reliability defect, not a v0.50.2 migration regression. Source
inspection of the prior production image and current main shows the same scheduler/SSH runner
shape.

## Confirmed observations

- Multiple configured journal-only collectors had stale cursors and no newly ingested rows.
- The worker pool has 10 workers, the queue has capacity 50, scheduling repeats every 30 seconds,
  fetch timeout is nominally 15 seconds and output cap is 10 MiB.
- During the observed incident window, queue-full warnings accumulated continuously while
  fetch/cursor/insert/save failure counts stayed at zero.
- This pattern means jobs entered workers but did not return through the instrumented error paths.
- Temporarily disabling all collectors and restarting produced a healthy Core with zero remaining
  collectors; after the new process started, queue-full/fetch-failure/critical counts remained zero.

## Source-confirmed causes

1. Scheduler enqueues every eligible Node on every tick with no queued/in-flight de-duplication.
2. `context.WithTimeout` reaches TCP dial and SSH handshake through `DialSSH`.
3. After session creation, stdout uses blocking `ReadAll(LimitReader(maxBytes))` and then blocking
   `session.Wait()` with no context-driven close.
4. Reaching the local LimitReader cap can stop reading while the remote command still writes,
   making remote completion and Wait mutually blocked.
5. Scheduler `done` closes when its tick loop returns, not when its workers join.

## Why the mitigation worked

Disabling sources stopped new jobs; restarting discarded the stuck process and its in-memory queue.
It did not repair the cancellation, output-limit or duplicate-enqueue defects, so collection must
remain disabled until a fixed image is verified.

## Privacy boundary

No production node names, hosts, usernames, paths, cursor values, journal content, IPs or raw SSH
errors are recorded here. Tests must use synthetic FAKE fixtures only.
