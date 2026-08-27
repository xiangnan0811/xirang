# Implementation evidence

Date: 2026-08-27

Branch/worktree: `codex/backup-assets-preview-authorization-ui` in
`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui`.

## Outcome

The local implementation covers the dynamic Foundation setting and rollback,
the shared exact scheme/client policy at all five ticket surfaces plus Serve,
the closed transport error product, exact/fallback Nginx sanitization,
method-independent application-log privacy, the Admin Backups control, all
delivery-surface guidance, i18n, and deployment documentation. Existing RBAC,
step-up, ticket/grant, origin, request/byte budget, Catalog, Provider, schema,
migration, and node-log boundaries were preserved. No production, NAS, release,
or collector action was performed.

## Genuine RED evidence

The first behavioral product action was the hermetic official-template Nginx
POST-cookie-GET-HEAD test:

```text
$ ./scripts/check-asset-content-nginx-runtime.sh
asset-content nginx runtime check: FAIL
 - get.headers X-Probe-Proto expected 'http', got 'https'
 - get.headers X-Probe-Xff expected '198.51.100.77, 127.0.0.1', got '198.51.100.77'
 - head.headers X-Probe-Proto expected 'http', got 'https'
 - head.headers X-Probe-Xff expected '198.51.100.77, 127.0.0.1', got '198.51.100.77'
```

The next production-shaped handler RED was:

```text
$ cd backend && go test ./internal/api/handlers -run '^TestBackupContentIssueAllowsPrivateNetworkHTTPThroughTrustedProxy$' -count=1
expected HTTP ticket issuance; got 503
body={"code":503,"message":"需要安全传输","data":null}
service calls=0
```

The final PRD R5 audit added a missing exact negative before changing the shared
policy:

```text
$ cd backend && go test ./internal/api/handlers -run 'TestBackupContentSchemePolicyRequiresTrustedProxyForForwardedHTTPS|TestBackupContentSchemePolicyPrivateNetworkMatrix' -count=1
--- FAIL: TestBackupContentSchemePolicyRequiresTrustedProxyForForwardedHTTPS (0.00s)
    --- FAIL: TestBackupContentSchemePolicyRequiresTrustedProxyForForwardedHTTPS/forwarded_HTTPS_without_XFF (0.00s)
        backup_content_handler_test.go:540: unsafe scheme accepted: secure=true remote="127.0.0.1:43210" headers=map[X-Forwarded-Proto:[https]]
FAIL xirang/backend/internal/api/handlers 0.067s
```

After the minimal fail-closed correction, the same selector passed in `0.062s`.
Any XFP presented over an underlying HTTP hop now requires one valid XFF chain
with a provable client; direct TLS remains compatible.

## Focused and repeated verification

The three planned normal selectors passed three consecutive complete rounds:

```text
round 1: handlers 0.069s; handlers/settings/backupasset/runtime 0.089s/0.013s/0.007s/0.062s; api 0.062s
round 2: handlers 0.068s; handlers/settings/backupasset/runtime 0.091s/0.013s/0.006s/0.064s; api 0.062s
round 3: handlers 0.070s; handlers/settings/backupasset/runtime 0.087s/0.014s/0.007s/0.063s; api 0.061s
```

These are the exact selectors from `implement.md` lines 122-124. After the R5
audit correction, the full handler selector passed again in `0.070s` and the API
selector passed in `0.060s`.

The focused race selector from `implement.md` line 125 passed after the final
policy correction:

```text
handlers 1.734s
middleware 1.541s
settings 1.069s
backupasset 1.042s
backupasset/runtime 1.652s
```

The Foundation transition/rollback selector passed:

```text
$ cd backend && go test ./internal/settings ./internal/api/handlers ./internal/backupasset/runtime -run 'PrivateNetworkContentTransport|BackupAssetOverrideSnapshot|FoundationTransition' -count=1
settings 0.020s; handlers 0.080s; runtime 0.058s
```

It covers default/env/DB projection, prospective transition, exact override and
runtime restoration, induced apply/restoration errors, and production-composed
runtime visibility. Audit assertions remain value-free.

The focused Web selector used all 12 introduced/affected test files (Admin panel,
guidance, typed setting API, bounded error parser, preview, Export panel, Archive
panel, Recovery wizard, three delivery hooks, and Backups page): 12 files and 233
tests passed in `2.79s`.

## Full gates

- `GOTOOLCHAIN=go1.26.6 make check`: PASS on the final tree. GolangCI reported
  `0 issues`; all backend packages passed; Web reported 184 files and 1,554 tests
  passed with 71.64% statement, 65.20% branch, 68.32% function, and 74.36% line
  coverage; backend and production Web builds passed (Vite `4.85s`).
- `cd backend && go build ./...`: PASS (`4.09s`) in the earlier explicit build gate.
- `./scripts/check-asset-content-nginx.sh`: PASS.
- `./scripts/check-asset-content-nginx.test.sh`: PASS, including mutation cases.
- `./scripts/check-asset-content-nginx-runtime.sh`: PASS for rendered official
  template POST-cookie-GET-HEAD, exact headers, Range/If-Range, and canary-free log.
- `bash scripts/check-doc-freshness.sh`: PASS.
- `git diff --check`: PASS.

Toolchain note: the host default is an unreleased `go1.27.0-X:nodwarf5`, while
the installed GolangCI binary was built by Go 1.26.4 and the module declares Go
1.26.6. Plain `make check` panicked in GolangCI under that host mismatch. Running
the complete repository gate with the module-pinned `GOTOOLCHAIN=go1.26.6`
succeeded as recorded above.

## Privacy, compatibility, and diff evidence

- Added Web production code contains no direct `fetch`, `localStorage`,
  `sessionStorage`, `any`, or `unknown as` bypass. No added diff line introduces
  history/location/router persistence. The only new location access is the
  approved exact static `window.location.hash` focus target in the Admin panel.
- `TestStructuredLoggerContentShapedRequestsOmitIdentityForEveryMethodAndShape`
  covers exact, malformed, trailing, query, POST, and OPTIONS content-shaped
  requests. It proves `client_ip` and raw identity/request canaries are omitted
  while the safe shaped route, method, status, latency, and request ID remain.
- The Nginx static, mutation, and runtime checks prove XFP is overwritten by
  `$scheme`, the exact route appends XFF, fallback evidence is sanitized, XRI is
  absent, and content logs remain canary-free.
- `git diff --name-only` is empty for `backend/internal/database`,
  `backend/internal/model`, `backend/internal/backupasset/catalog`,
  `backend/internal/backupasset/provider`, `backend/internal/nodelogs`, the
  generic System settings page, and the Catalog API module. There is no migration,
  schema, Provider, Catalog DTO/permission, RBAC, or collector change.
- Requested churn cleanup is narrow: repository `testutil_test.go` `+1/-0`,
  backupasset `service_test.go` `+3/-1`, settings `service_test.go` `+2/-0`.
- Final tracked diff before this evidence file: 44 files, 1,056 insertions and
  134 deletions. The new focused tests, runtime checker, Admin/guidance/API files,
  and this task directory are untracked task-owned implementation paths; the main
  session must include them deliberately at delivery time.

## Acceptance criteria audit

- AC1: compatibility matrix covers false/default, independent legacy loopback,
  private opt-in, direct TLS, and exact trusted-proxy HTTPS.
- AC2: Foundation setting precedence, live issue/Serve projection, induced
  failure, exact DB override restoration, runtime rollback, and value-free audit
  are automated.
- AC3-AC5: production-shaped issue/Serve and all five surfaces use one closed
  scheme/client policy; the full positive/negative address and proxy matrix,
  including no-XFF XFP, is automated.
- AC6: Admin-only panel load/source/warning/dialog/cancel/save/disable/rollback/
  dedup/live-region/hash-focus/keyboard/axe behavior is automated.
- AC7-AC8: runtime Nginx proxy behavior and application/Nginx privacy canaries pass;
  protected schema, authority, storage, and logging boundaries are unchanged.
- AC9: all delivery surfaces use only the exact parameter-free transport reason;
  Admin/Operator guidance and malformed/generic fallbacks are automated.
- AC10: deployment/env documentation and freshness/diff checks pass.
- AC11 local portion: three repetitions, race, full backend/Web/build, Nginx,
  documentation, privacy, and diff gates pass. The independent Trellis check
  found and fixed the inbound address-header pass-through issue described below,
  then repeated affected and full gates successfully.
- AC12-AC13: PR/CI/release/Docker, guarded NAS upgrade, explicit production enable,
  and real LAN preview acceptance are intentionally pending and outside this
  implementation agent's authority. Collectors remain `0`; node-log P1 has not
  started.

## Independent post-spec TDD closure

The final spec review caught an R6/AC7 issue that the initial checker and runtime
probe did not model: Nginx passes ordinary inbound request headers unless they
are explicitly redefined. The exact content route therefore forwarded a spoofed
X-Real-IP, and the shaped fallback forwarded spoofed XFF and X-Real-IP.

The runtime probe was extended before the template fix. The genuine RED was:

```text
asset-content nginx runtime check: FAIL
 - get.headers X-Probe-Real-Ip expected '', got '192.0.2.88'
 - head.headers X-Probe-Real-Ip expected '', got '192.0.2.88'
 - shaped.headers X-Probe-Xff expected '', got '198.51.100.77'
 - shaped.headers X-Probe-Real-Ip expected '', got '192.0.2.88'
```

The tightened static checker also failed before the template fix:

```text
asset-content nginx check: expected exactly one directive with prefix: proxy_set_header X-Real-IP
```

The minimal GREEN change explicitly clears X-Real-IP on the exact route and XFF
plus X-Real-IP on the shaped fallback, while retaining exact-route
`$proxy_add_x_forwarded_for`. Static and mutation checks now require the empty
directives, and the official-image runtime probe asserts the spoofed canaries are
absent upstream. All three Nginx gates pass.

The seven-section backend/frontend contracts were moved from the ends of the
oversized generic quality/type files into dedicated, indexed
`backup-content-transport.md` specs. Both task manifests reference those files,
so the task-specific contracts are below the context-injection cap and are not
duplicated. Reset-audit wording now matches implementation: PUT records
`source=db`; neither PUT nor reset records values.

Post-fix focused handlers and StructuredLogger privacy selectors pass. Doc
freshness, task-manifest validation (14 entries each), and `git diff --check`
pass. Final `GOTOOLCHAIN=go1.26.6 make check` passes with golangci-lint 0 issues,
all backend packages, 184 Web files / 1,554 tests with coverage, backend build,
and Web production build (`4.90s`).
