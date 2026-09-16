# Implementation and verification — 2026-09-16

User authorized the existing P1 task after reconciliation. Baseline is main
`bb27865ab3f334bbbc8b104ae6590a3fa6fe886c`; branch is
`codex/node-logs-collector-stall`. No production configuration was inspected or changed.

## Implemented behavior

- Reused shared raw SSH execution with a narrow, opt-in dedicated-transport
  close-and-join mode. Existing shared-client compatibility behavior is unchanged.
- Validated bounds before credentials, covered session creation/Start/read/Wait
  with cancellation, rejected over-limit/partial output, and preserved complete
  output on explicit remote nonzero exit. Errors/audits remain payload-free.
- Added queued/in-flight node claims and unconditional worker release, owned
  cancellation, joined/idempotent shutdown and context-aware worker DB operations.
- Added bounded scheduler metrics, aggregate queue warnings and distinct fetch
  failure reasons. Failed fetches preserve both journal/file cursors and zero rows.
- Added nodelogs and sshutil to the existing CI race package list so ownership
  regressions remain covered on future pull requests.
- Updated operator docs and the executable backend collection contract. Fixed
  existing test DB cleanup so repeated package runs are isolated.

## Failure-first evidence

- Original duplicate enqueue queued two jobs for one node; shutdown did not cancel
  a blocked runner and shutdown-before-Run returned the caller cancellation.
- Invalid SSH bounds and already-canceled parents incorrectly attempted auth.
- Canceled/output-limit error metric increments were zero before classification.
- Repeated DB-backed tests retained rows across runs until connection cleanup.

## Completed local evidence

- `GOTOOLCHAIN=go1.26.6 go test ./internal/nodelogs -count=50` passed.
- `go test -race ./internal/nodelogs -count=10` passed.
- Related sshutil, credentialaudit and lifecycle race checks passed.
- Shared strict execution focused normal/race tests passed 50 repetitions.
- Real loopback SSH exact-limit, over-limit and nonzero-exit tests passed 20 times;
  independent review added these and passed three focused race repetitions.
- Full backend build, vet and golangci-lint passed (0 issues).
- Task context validation, document freshness and diff whitespace checks passed.
- Independent Trellis review found no remaining functional findings.

All backend packages passed across the full run and environment-specific reruns.
The initial `go test -p 1 ./...` with disk-backed GOTMPDIR failed only content,
processing, processing/updater, provider and runtime filesystem/socket fixtures.
Content, processing, provider and runtime then passed with ordinary `/tmp`;
updater passed with its 320 MiB test on disk and all remaining tests on `/tmp`.
Full govulncheck found no reachable vulnerabilities (one dependency-module finding
was not imported/called). PR #533 contains code commit `664555e6`; CI/post-merge
evidence remains pending at this checkpoint. The push hook was bypassed because
its fixed `/tmp` cannot satisfy the host's large-stream fixture quota; all relevant
local backend gates were run explicitly, and full remote CI remains required.

Use Go 1.26.6; default host Go 1.27 is incompatible
with the installed linter. Disk-backed GOTMPDIR avoids the host tmpfs quota, but
mount-verifier and Unix-socket tests require the ordinary short `/tmp` filesystem;
the updater's 320 MiB streaming fixture needs disk-backed temporary storage.

## Production acceptance remains open

The August disabled-collector observation is historical. A fixed image is not
evidence of deployment or restored collection. Before a separately authorized
rollout, verify current version/digest and collector configuration; preserve
logs/cursors and rollback readiness. Enable one low-risk node and observe at least
two cycles (cursor progress, sanitized rows, in-flight recovery and no unexpected
queue rejection), then restore further nodes in small batches. Do not publish
credentials, hosts, paths, cursor values or raw log bodies in the acceptance record.
