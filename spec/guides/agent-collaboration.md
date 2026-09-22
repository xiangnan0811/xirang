# Agent collaboration and verification

## Authority and native entry

The checkout's root AGENTS.md and relevant nested AGENTS.md files govern this
repository. Codex, Grok and OMP are required supported platforms. Each uses its
native instruction, skill, role and permission mechanisms; shared harness skill
source does not authorize importing another tool's runtime settings. OMP's
project `.omp/AGENTS.md` includes the root authority. No CLAUDE.md copy is needed
for Claude versions supporting the native AGENTS.md fallback.

From a subdirectory or worktree, resolve the current Git root before reading
specs or running commands. Backend commands run in `backend/`, frontend commands
in `web/`, and Makefile or `scripts/` gates at the root. A generated private
profile's suggested commands are not a substitute for these project gates.

## Parallel work and independent review

- The main agent owns scope, decisions, integration, authorized repairs and final
  verification. Default to useful bounded native delegation for non-trivial
  work; use direct work for small changes or an explicit inline request.
- Give implementers non-overlapping file ownership, requirements and relevant
  spec pointers. They may edit their assigned files and run applicable checks.
  They must accommodate other workers and must not revert unrelated changes.
- Investigators and independent reviewers remain read-only and do not delegate.
  They return findings in the conversation; no task directory is required.
- Before delivering non-trivial code, security/compatibility changes, or changes
  to agent governance/loading, obtain independent GPT and Grok discovery reviews
  of the same integrated candidate. Neither reviewer sees the other's findings
  in the discovery pass. A self-fixing quality agent is not an independent lane.
- Use the configured native model/effort/Advisor roles. Model aliases must resolve
  to the intended family; a GPT agent named Grok does not satisfy the Grok lane.
  The personal Grok preference is grok-4.7 with xhigh unless explicitly overridden;
  keep the concrete runtime settings in that platform's user configuration.
- Supply each review's base, candidate scope, requirements and acceptance evidence.
  The main agent reconciles findings and repairs authorized issues. Repair review
  verifies finding IDs and direct repair consequences, without restarting a full
  discovery wave unless the candidate or evidence materially warrants it.
- Missing required review or native-platform evidence is a remaining requirement,
  never a pass. Report capability limits; do not replace independence with self-review.

## Recovery and reusable evidence

Recovery is optional and private. Identify repository, checkout/worktree, module
and goal; never select another repository's or workspace's newest record merely
because it exists. Completed/cancelled records are not resumed. Recheck current
contracts and working content before following a historical next action.

Bind evidence to the base revision, selected paths, actual staged blobs and any
required unstaged/untracked content, plus relevant runtime/configuration inputs.
Same HEAD does not cover dirty edits. Partial staging or candidate/condition
changes invalidate affected checks; retain unchanged, still-applicable evidence.
Use read-only index comparison during read-only assessment. Do not stage, create
temporary Git indexes, or write private state merely to assess readiness.

Before an authorized commit, verify scope, specification synchronization,
independent review, tests/builds and applicable acceptance. Repair in scope and
rerun affected checks. Recheck the final index after staging or formatting. Keep
evidence in the conversation or existing private state, not a repository journal.

## Runtime changes and isolation

After changing loading or orchestration, validate each required platform in a
fresh native session from root, backend, web and a worktree. Use ordinary short
requests: do not supply the expected files or the migration prompt to make the
test pass. Observe source labels and actual tool events; distinguish discovery,
disabled/shadowed candidates, selected sources and injected content.

A profile/worktree switch does not prove isolation. Check resolved user/config,
skill/extension, private-state and Git common-directory paths and shared services.
Helper/static tests and one platform's pass do not establish native acceptance
on the other platforms. Keep temporary probes outside the project and remove
them after acceptance; durable project requirements belong in existing specs.
