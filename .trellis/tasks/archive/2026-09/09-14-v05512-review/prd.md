> **Current disposition (2026-09-16):** see [reconciliation.md](reconciliation.md). Older status, delivery gates and deferred-work ownership below are historical where that record supersedes them.

# v0.55.12 external review repairs

Baseline: 180bc2cc1c69f05317c9e224d373ab57aa3a5d98. Source: root external review report dated 2026-09-13. All eight findings accepted after scoped source review.

## Requirements
- R8-01 reject invalid cron before commit; isolate bad historical task configurations without suppressing infrastructure failures or reverting the prior valid-commit behavior.
- R8-02 ordinary APIs honor durable single-session logout across running cores; uncertainty fails closed; unrelated sessions remain usable.
- R8-03 never equate Restic format v2 with backend deletion protection. Explicitly distinguish unverified protection; do not invent storage guarantees.
- R8-04 safe whitelist projection, explicit task edit preservation/clearing semantics, no secret echo, and stale edit protection.
- R8-05 explicit empty cron means manual even with policy; omission preserves edit state; schedule cursor changes atomically; creation defaults remain.
- R8-06 full filesystem execution confinement, including remote filesystem and check/use races. User explicitly selected full isolation and permits remote helper deployment. Missing confinement capability fails closed for configured allowlists. Empty allowlists remain unrestricted. Do not reject all legitimate internal links as a substitute for containment.
- R8-07 remove secret from all password creation command argv; preserve exact input bytes, private exclusive creation, collision protection, and independent cleanup.
- R8-08 request cancellation, total deadline, output and volume budgets cover Docker discovery; incomplete results are never represented as complete empty success.

## Acceptance
Exercise real task HTTP editing, invalid cron persistence invariants and isolated Core startup; two Core logout; browser rename/config/manual editing; disposable SSH secret/cancellation/volume limits; disposable local/remote rsync boundary, link, replacement and capability failure scenarios. Run relevant existing tests and required PostgreSQL concurrency gates. Independent GPT/Grok discovery followed by bounded verification; every confirmed issue closed before readiness.

## Constraints
No production access, real-backup deletion, auto release, broad rollback, secret responses, security-looking fallback, or unrelated changes. Preserve the untracked user review report. Do not change version manifests. Report exercised evidence honestly.
