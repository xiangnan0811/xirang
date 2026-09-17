# P3 scoped completion and verification — 2026-09-17

## Disposition

Reuse the existing P3 task after backup acceptance/parent archival #540. Implemented
the three necessary bounded changes; no new task, migration, protocol rewrite,
NAS operation or blanket legacy-log conversion. The earlier child
`07-28-snapshot-indexer-test-isolation` is archived/completed (#403).

| Item | Current result |
| --- | --- |
| Ordinary API middleware errors | Shared middleware envelope with code/message/null data; auth, RBAC, ownership and audit preserve status, safe messages and abort behavior. Existing 429 retry data/header retained. |
| Panel RAF | Effect-local two-frame ownership; no setter mutation/cast, zero IDs cancelled, close/reopen/unmount and StrictMode covered. |
| Runtime log bounds | Hub counts every overflow; atomic per-Hub 30-second warning gate with aggregate count only. Handshake diagnostics use Debug and omit raw client-controlled errors. |

Metrics authentication, CORS, content, streams and WebSocket first-frame protocols
retain their existing contracts. Frontend API client production code already
supports the desired envelope; only consumer regressions were needed.

Ordinary terminal/reporting/SSH/migrator/auth logging was evaluated as outside the
bounded overflow fix; it is not claimed converted or eliminated. Pre-init config
logging remains intentional. The previously documented generic WebSocket rewrite
and KDF salt migration remain explicit non-goals, not silently completed work.
The RAF change does not redesign create-mode metric selection on reopening.

## Implementation and independent verification

- Backend RED: seven real middleware cases exposed legacy error bodies, and the
  overflow regression exposed per-drop logging. Frontend RED: second RAF ID zero
  survived close/unmount in two behavioral regressions.
- Backend GREEN: complete middleware/ws tests and `-race -count=1`; new regressions
  repeated under race detector twenty times; focused lint zero issues; whole
  backend build. Main independently ran whole-backend lint with zero issues.
- Independent check added real WebSocket close-frame tests proving redaction and
  Info/Debug filtering; concurrent overflow tests use controlled timestamps.
  Both relevant packages passed single-run race and changed tests passed twenty
  race repetitions. No unresolved product correctness finding.
- Frontend GREEN: 43 focused tests, then complete `npm run check`: typecheck,
  lint, 214 test files / 1,987 tests, production build. Audit reported zero
  vulnerabilities. Independent check also passed frontend lint/typecheck.
- Specs updated for API envelopes, bounded Hub logs and effect RAF ownership;
  corrected the API-error property documentation to `detail`.

Go checks used 1.26.6 and disk-backed compilation temp space. Repeated whole-package
race runs (`-count=3/5`) exposed existing shared SQLite fixture accumulation in
middleware auth / WS backfill; these were not data-race reports. Do not claim those
repeated whole-package runs passed. Single-run complete packages and repeated new
regressions pass; unrelated fixtures are unchanged. Required full remote CI remains
mandatory before merge. This record does not claim a single local all-backend test
invocation passed.

## Delivery gate

Commit and archive on the dedicated branch, then merge only after all required PR
checks pass. Monitor exact post-merge CI and Release Please. A code merge may create
a release PR; it is not itself a formal version publication or NAS deployment.
