# Focused spec contracts — node-log collector

This routing note prevents Trellis context injection from truncating the large backend specs. It
does not replace them. Before editing code, re-read the named source specs and use this list to
jump to the task-relevant contracts.

## `.trellis/spec/backend/quality-guidelines.md`

- Never ignore SSH, DB, filesystem, encryption or migration errors.
- Background workers/goroutines must be cancelable and shutdown-aware.
- Timing/cancellation tests use test-owned synchronization when exact ordering/counts matter;
  unsynchronized wall-clock deadlines cannot require an exact retry count.
- Run focused tests repeatedly and under race for timing-sensitive fixes.
- Security-sensitive SSH paths require explicit denial/failure tests.
- Secret-shaped fixtures use `FAKE_*_FOR_TEST_ONLY`.

## `.trellis/spec/backend/logging-guidelines.md`

- Use `logger.Module("nodelogs")` and structured typed fields.
- Queue saturation is Warn; unexpected job failure is Error/Warn according to recoverability.
- Never log credentials, raw command output, decrypted settings, paths/remote evidence or
  credential-audit metadata.
- Stable IDs may correlate operations, but new metrics use bounded low-cardinality reason labels.

## `.trellis/spec/backend/database-guidelines.md`

- Check every cursor/log query and write error.
- Preserve current transaction boundaries unless the task explicitly proves a cross-row atomicity
  change is required.
- Use context-aware DB operations where the service owns a context.

## Task-specific additions

- `prd.md` owns close-and-join, output-limit, per-node single-flight and production re-enable
  requirements.
- `design.md` owns SSH owner coordination and Scheduler lifecycle state machines.
- `research/root-cause.md` owns the sanitized evidence and the non-regression attribution.
