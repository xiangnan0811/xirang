# Existing P3 task implementation

1. Implement backend envelope consistency and focused regressions. Independently implement panel RAF cleanup and frontend consumer/regression coverage in disjoint files.
2. In the backend slice, bound Hub overflow logs without blocking Publish or losing counters; preserve protocol and redaction. Record other legacy logging as evaluated/out of current scope, not fixed.
3. Reproduce failures before fixes. Use Go 1.26.6; separate compilation temp storage (/var/tmp) from runtime if /tmp quota requires it. Do not change product/tests for local environment limits. Frontend full gate: npm run check. Required remote CI remains mandatory.
4. Independent trellis-check over final changes; main updates executable specs and task evidence. No new task or unrelated refactor.
5. Commit on fix/p3-quality-debt, record completed scope/non-goals, archive via Trellis and deliver through PR with required CI. Sync main and monitor post-merge automation; code merge alone is not a NAS upgrade or formal release.
