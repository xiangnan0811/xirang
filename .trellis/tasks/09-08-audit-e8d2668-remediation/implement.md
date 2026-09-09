# Implementation plan

Approved by user after complete audit adjudication. Dedicated branch: fix/audit-e8d2668-remediation. Source report remains untouched.

1. Dispatch four disjoint implementation slices: identity XR-02/03/06/07; runtime XR-04/05; monitoring XR-01/08/09 including its UI; client XR-10/12/13.
2. Main implements XR-11/14/15 and O-01/02/03, coordinates shared startup/router/bootstrap and migration metadata changes.
3. Integrate every exported callsite, frontend contract, generated API documentation and relevant existing docs. Avoid new abstractions outside these requirements.
4. Run defect-focused behavioral regressions and throwaway smoke scenarios. Run SQLite/Postgres logical-concurrency and migration gates, real browser evidence, IPv6 SSH and sandbox task interruption.
5. Freeze diff and run parallel integration-reviewer/GPT-5.6-Sol, grok-reviewer/Grok-4.6, gemini-reviewer/Gemini 3.8 Flash discovery with identical baseline/contracts. Validate model badges; adjudicate ledger. At most two compatible repair/verification cycles; no new broad audit after each repair.
6. Run complete backend and frontend checks plus workflow/migration helper checks against final changes. Update docs/changelog after behavior proof; remove throwaway files. Commit/push PR and monitor required CI if authentication permits. Do not merge/release or claim readiness without verified gates; report concrete external blockers.

Relevant commands: cd backend && go test ./... && go build ./...; cd web && npm run check; scripts/run-required-postgres-tests.sh for required PostgreSQL cases; scripts/check-migration-version.sh and repository CI parity scripts. New targeted tests must fail on plausible original defects, not pin source text/plumbing.
