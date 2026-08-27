# Independent check evidence

Date: 2026-08-27

Worktree: `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui`

Branch: `codex/backup-assets-preview-authorization-ui`

## Findings

One Important finding was found and fixed during the post-spec pass: the exact
Nginx content route passed inbound X-Real-IP, and the shaped fallback passed
inbound XFF/X-Real-IP because absent `proxy_set_header` directives do not clear
ordinary request headers. The reviewer added test-first runtime/static/mutation
coverage, explicit empty header directives, and matching injectable specs. No
Critical, Important, or Minor finding remains unresolved.

The AC2 rollback question was resolved as evidence-sufficient, not a finding.
The production runtime captures/restores the exact prior override, delegates
runtime restoration through the managed transition chain, and request-time
Issue/Serve configuration reloads the effective Foundation snapshot.
`TestBackupAssetSettingsPrivateNetworkFailureRestoresExactFoundationConfig`
proves restored persistence/effective Content configuration and one runtime
restore call; the Serve test separately proves the per-request recheck. The
handler-only fake intentionally represents the persistence boundary and its
retained prospective bundle is not the production runtime state.

## Contract audit

- The new Foundation setting is boolean, default-false, and uses the existing
  DB-over-environment-over-default registry. PUT, reset-to-environment/default,
  transition failure, exact prior-override restoration, and value-free audit
  behavior are covered without new schema or DTO fields.
- The control is mounted only for authenticated Admin users in Backups Overview.
  It loads source/effective state, requires confirmation only when enabling,
  disables directly, prevents duplicate mutation, exposes a stable hash/focus
  target and live-region result, and does not expose unrelated backup settings.
- The shared backend policy classifies only RFC1918, ULA, and loopback clients as
  private; preserves direct loopback compatibility; rejects `Forwarded`; bounds
  and validates exact single XFP/XFF values; requires XFF with forwarded HTTP;
  appends the socket peer and peels trusted hops right-to-left; and rejects
  malformed, untrusted, public, link-local, multicast, unspecified, and
  documentation-only addresses.
- Issue and Serve both recheck the live setting. Preview/original, Export,
  Archive, and Recovery result delivery use the shared policy and preserve their
  existing RBAC, step-up, ticket/grant, origin, request-budget, and byte-budget
  boundaries.
- The API emits only the exact parameter-free safe reason for the policy denial.
  The Web parser accepts only the exact 503/content-ticket/two-key shape; Admin,
  Operator, and Viewer guidance is role-aware and non-leaking, while malformed
  or generic errors remain generic.
- The official Nginx template overwrites XFP from `$scheme`, appends exact-route
  XFF with `$proxy_add_x_forwarded_for`, explicitly clears exact-route X-Real-IP,
  explicitly clears shaped-fallback XFF/X-Real-IP, and keeps an all-method
  content-shaped privacy gate. Static, mutation, and official-image runtime
  probes verify headers, methods, range behavior, spoof resistance, and log
  privacy.
- Application content-shaped logs omit client identity and request metadata for
  every method and malformed shape while retaining only approved safe fields.
  No migration/model, Catalog/RBAC, ticket/grant schema or budgets, Provider,
  node-log collector, or Swagger surface was expanded.
- Deployment/environment docs and backend deployment/logging specs describe the
  secure default, private-address boundary, cleartext risk, Admin control,
  environment fallback, trusted-proxy caveat, and setting-first rollback.

## Verification

All commands were run independently from the worktree above.

### Focused backend selectors, three consecutive rounds

Each of the following commands passed in rounds 1, 2, and 3:

```text
cd backend && go test ./internal/api/handlers -run 'BackupContent.*(Transport|Scheme|Private|Cookie|Serve)|BackupAssetExport.*(Transport|Private)|BackupArchive.*(Transport|Private|DeliveryTicket)' -count=1
rounds: handlers 0.072s, 0.076s, 0.077s

cd backend && go test ./internal/api/handlers ./internal/settings ./internal/backupasset ./internal/backupasset/runtime -run 'PrivateNetwork|Settings.*Content|Content.*Private|FoundationTransition|RuntimeConfigAwareContent|Registry.*Backup' -count=1
rounds: handlers 0.098s/0.095s/0.096s; settings 0.015s/0.016s/0.016s;
backupasset 0.008s each; runtime 0.069s/0.070s/0.070s

cd backend && go test ./internal/api -run 'BackupContent|Export.*RBAC|Recovery.*Result' -count=1
rounds: api 0.063s, 0.066s, 0.066s
```

### Focused Web and race

```text
cd web && env -u NODE_ENV npx vitest run src/lib/api/backup-assets-error.test.ts src/lib/api/backup-content-transport-api.test.ts src/features/backup-assets/content-transport-guidance.test.tsx src/features/backup-assets/private-network-content-transport-panel.test.tsx src/features/backup-assets/asset-preview.test.tsx src/features/backup-assets/export-job-panel.test.tsx src/features/backup-assets/archive-member-panel.test.tsx src/features/backup-assets/recovery-plan-wizard.test.tsx src/features/backup-assets/use-backup-archive.test.tsx src/features/backup-assets/use-backup-asset-export.test.tsx src/features/backup-assets/use-backup-recovery.test.tsx src/pages/backups-page.test.tsx
PASS: 12 files, 233 tests, duration 2.54s

cd backend && go test -race ./internal/api/handlers ./internal/middleware ./internal/settings ./internal/backupasset ./internal/backupasset/runtime -run 'PrivateNetwork|Backup(Content|AssetExport|Archive).*(Transport|Private|Serve|DeliveryTicket)|StructuredLogger.*Content|Settings.*Content|FoundationTransition|RuntimeConfigAwareContent|Content.*Private' -count=1
PASS: handlers 1.740s; middleware 1.546s; settings 1.071s;
backupasset 1.046s; runtime 1.650s
```

The 12-file selector contained the setting API and panel, exact error parser,
guidance, preview, Export panel, Archive panel, Recovery wizard, three delivery
hooks, and Backups page tests.

### Nginx, backend, Web, and repository gates

```text
./scripts/check-asset-content-nginx.sh
PASS: asset-content nginx check

./scripts/check-asset-content-nginx.test.sh
PASS: asset-content nginx checker self-test

./scripts/check-asset-content-nginx-runtime.sh
PASS: asset-content nginx runtime check

cd backend && go test ./...
PASS: all packages

cd backend && go build ./...
PASS

cd web && env -u NODE_ENV npm run check
PASS: typecheck, lint, 184 files / 1,554 tests, coverage, production build
coverage: 71.63% statements, 65.19% branches, 68.32% functions, 74.35% lines
Vite build: 5.14s

bash scripts/check-doc-freshness.sh
PASS

GOTOOLCHAIN=go1.26.6 make check
PASS: golangci-lint 0 issues; Web lint; all backend packages; Web 184 files /
1,554 tests with coverage; backend build; Web production build (4.98s)

git diff --check
PASS: no output
```

The explicit logger selector also passed both exact-path compatibility and
all-method/all-shape privacy tests. Modified Web production source has no added
direct `fetch`, `localStorage`, `sessionStorage`, `any`, `unknown as`, or
history/router persistence. The only new location access is the approved exact
hash focus target. Protected-path and diff scans found no schema/migration,
Catalog/RBAC, grant/ticket schema, Provider, node-log, collector, Swagger, or
OpenAPI expansion; a textual `Provider` match was formatting-only.

The initial tracked diff snapshot was 46 files, 1,143 insertions, and 162
deletions, plus the task-scoped untracked task artifacts, focused tests, Nginx
runtime checker, guidance/panel, and typed setting API files. Every tracked and
untracked intended path was inspected. No commit, push, PR, merge, release, NAS,
production, P1, or collector action was performed. Collectors remain `0`.

## Post-spec finding and TDD closure

The mandatory seven-section spec review found that the Nginx prose described
address-header omission, while the template and checker only omitted directives.
Nginx passes ordinary inbound headers by default, so the approved R6/AC7 privacy
contract required explicit empty forwarding directives.

The official-template runtime probe was extended first. It failed with exit 1:

```text
get.headers X-Probe-Real-Ip expected '', got '192.0.2.88'
head.headers X-Probe-Real-Ip expected '', got '192.0.2.88'
shaped.headers X-Probe-Xff expected '', got '198.51.100.77'
shaped.headers X-Probe-Real-Ip expected '', got '192.0.2.88'
```

The tightened static checker independently failed with exit 1 because the exact
route lacked an explicit X-Real-IP removal. The minimal fix added
`proxy_set_header X-Real-IP ""` to exact content and empty XFF/X-Real-IP headers
to the shaped fallback; exact XFF remains `$proxy_add_x_forwarded_for`.

Post-fix verification:

```text
./scripts/check-asset-content-nginx.sh              PASS
./scripts/check-asset-content-nginx.test.sh         PASS
./scripts/check-asset-content-nginx-runtime.sh      PASS
focused handlers                                   PASS (0.090s)
StructuredLogger exact/all-shape privacy           PASS (0.054s)
bash scripts/check-doc-freshness.sh                 PASS
python3 .trellis/scripts/task.py validate <task>    PASS (14 entries each)
git diff --check                                    PASS
GOTOOLCHAIN=go1.26.6 make check                     PASS
```

The backend/frontend seven-section scenarios now live in dedicated indexed
`backup-content-transport.md` files and are explicitly listed in both task
manifests. This avoids duplicating the generic quality/type specs and ensures the
new contracts are injected below the 32 KiB per-file cap. Existing generic-file
size warnings remain, but they no longer hide the task-specific scenarios.
Reset-audit wording was corrected to match actual fields, and the new specs
contain no production identifiers, secrets, or content evidence.

The final tracked diff snapshot was 48 files, 1,159 insertions, and 160
deletions, plus the task-scoped untracked specs, task artifacts, focused tests,
runtime checker, guidance/panel, and typed setting API files. No commit, push,
PR, merge, release, NAS, production, P1, or collector action was performed.
Collectors remain `0`.

QUALITY_OK
