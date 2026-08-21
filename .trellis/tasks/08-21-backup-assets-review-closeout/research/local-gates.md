# Child 17 local gates

Date: 2026-08-21
Branch: `feat/backup-assets-review-closeout`
Runner: remaining local gates (not a product-design review)
Constraint: evidence recorded from this run; no invented production walkthrough.

Status: complete (this session). No commit made.

## Summary

| # | Gate | Result | Exit |
|---|------|--------|------|
| 1 | `go test ./internal/backupasset/... ./internal/api/... -count=1` | PASS | 0 |
| 2 | `go test -race ./internal/backupasset/... ./internal/api/handlers/ -count=1` | PASS (attempt 3) | 0 |
| 3 | `bash scripts/check-backupasset-coverage.sh` | PASS (after env retries) | 0 |
| 4 | `web`: `env -u NODE_ENV npm run check` | PASS | 0 |
| 5 | `bash scripts/check-npm-audit.sh` | PASS | 0 |
| 6 | Playwright `npm run e2e` | FAIL (webkit host libs) | 1 |
| 7 | `govulncheck` (`go run ...@latest ./...`) | PASS | 0 |

## Environment notes

- `govulncheck` binary: not on PATH at start (`which govulncheck` empty). Will try `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` from `backend/`.
- Playwright cache at `~/.cache/ms-playwright` already has older Chromium/Firefox/WebKit builds; install may still be required if versions do not match this repo's Playwright.

## 1. Backend unit tests

Command:

```bash
cd /home/murray/code/xirang/backend && go test ./internal/backupasset/... ./internal/api/... -count=1
```

- Started: 2026-08-21 (this session)
- Exit: **0**
- Duration: ~53s

Excerpt:

```
ok  	xirang/backend/internal/backupasset	1.948s
ok  	xirang/backend/internal/backupasset/catalog	9.839s
ok  	xirang/backend/internal/backupasset/content	2.878s
ok  	xirang/backend/internal/backupasset/export	22.575s
ok  	xirang/backend/internal/backupasset/ga	0.114s
ok  	xirang/backend/internal/backupasset/overlay	1.209s
ok  	xirang/backend/internal/backupasset/processing	7.378s
ok  	xirang/backend/internal/backupasset/processing/capabilities	1.929s
ok  	xirang/backend/internal/backupasset/processing/capabilityspec	0.024s
ok  	xirang/backend/internal/backupasset/processing/updater	2.866s
ok  	xirang/backend/internal/backupasset/provider	1.407s
ok  	xirang/backend/internal/backupasset/publication	0.010s
ok  	xirang/backend/internal/backupasset/recovery	47.670s
ok  	xirang/backend/internal/backupasset/repository	10.179s
ok  	xirang/backend/internal/backupasset/retention	3.558s
ok  	xirang/backend/internal/backupasset/runtime	9.005s
ok  	xirang/backend/internal/backupasset/search	2.094s
ok  	xirang/backend/internal/api	0.713s
?   	xirang/backend/internal/api/docs	[no test files]
ok  	xirang/backend/internal/api/handlers	6.102s
EXIT:0
```

## 5. npm audit high+

Command:

```bash
cd /home/murray/code/xirang && bash scripts/check-npm-audit.sh
```

- Exit: **0**
- Duration: ~3s

Excerpt:

```
npm warn Unknown env config "devdir". This will stop working in the next major version of npm. See `npm help npmrc` for supported config options.
npm audit high+: clean or allowlisted
EXIT:0
```

Note: npm printed a local `devdir` config warning; it did not change the gate result.

## 7. govulncheck

`govulncheck` was not on PATH. Ran:

```bash
cd /home/murray/code/xirang/backend && go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

- Exit: **0**
- Duration: ~28s (includes module download of `golang.org/x/vuln` v1.7.0)

Excerpt:

```
=== Symbol Results ===

No vulnerabilities found.

Your code is affected by 0 vulnerabilities.
This scan also found 0 vulnerabilities in packages you import and 1
vulnerability in modules you require, but your code doesn't appear to call these
vulnerabilities.
Use '-show verbose' for more details.
EXIT:0
```

Verbose follow-up not required for the gate; symbol result is clean.

## 2. Race tests

Command:

```bash
cd /home/murray/code/xirang/backend && go test -race ./internal/backupasset/... ./internal/api/handlers/ -count=1
```

### Attempt 1 — FAIL (environment, not a product assertion)

- Exit: **1**
- Duration: ~65s
- Host: `/tmp` is tmpfs (14G, ~3.6G free at inspection). Parallel `-race` link + large updater fixtures exhausted tmpfs.
- Error class: `disk quota exceeded` / `设备上没有空间` / `disk I/O error` during compile/link and sqlite/temp writes. Not a data-race report.

Excerpt (link/compile):

```
# xirang/backend/internal/backupasset/search.test
/usr/lib/go/pkg/tool/linux_amd64/link: running gcc failed: exit status 1
...
/usr/bin/ld: final link failed: 设备上没有空间

# xirang/backend/internal/backupasset/retention.test
/usr/lib/go/pkg/tool/linux_amd64/link: mapping output file failed: disk quota exceeded
FAIL	xirang/backend/internal/backupasset/content [build failed]
FAIL	xirang/backend/internal/backupasset/export [build failed]
```

Excerpt (tests that then failed because tmpfs was already full):

```
--- FAIL: TestCatalogServiceFeatureGateFailsBeforeResourceLookup (0.14s)
    service_test.go:179: 执行迁移失败: transaction commit failed in line 0:  (details: disk I/O error: disk quota exceeded)
--- FAIL: TestServiceStreams320MiBCanonicalBundleBelowHeapBudget (0.02s)
    service_test.go:597: write large canonical member bytes=10759680 err=write /tmp/TestServiceStreams320MiBCanonicalBundleBelowHeapBudget.../bundle.tar: disk quota exceeded
FAIL	xirang/backend/internal/backupasset/catalog	4.457s
FAIL	xirang/backend/internal/backupasset/processing/updater	0.555s
```

No `-race` DATA RACE lines observed in this attempt.

### Attempt 2 — FAIL (workaround artifact)

- Exit: **1**
- Duration: ~213s
- Built and ran, but `TMPDIR`/`GOTMPDIR` were redirected to on-disk `.tmp/local-gates`. That is not a dedicated temp filesystem, and unix socket paths exceeded `sun_path`.
- Failures (caused by the workaround, not treated as product bugs):
  - `backupasset/content` `TestDefaultCacheMountVerifierAcceptsDedicatedTempFilesystem` — `unsafe content cache root` under `.tmp/local-gates`
  - `backupasset/processing` `TestLocalTransportDerivesSameUIDPeerAndCreatesPrivateSocket` / `TestLocalWorkerListenerImplementsAuthenticatedNetListener` — `listen unix ... bind: invalid argument`
  - `backupasset/processing/updater` `TestLocalUpdaterListenerAuthenticatesExactPeerCredentials` / `TestLocalUpdaterListenerRejectsWrongPeerAndUnsafeSocketBeforeDecode` — same unix bind error
  - `backupasset/provider` `TestRsyncTreePreflightBuildsBoundEvidenceFromTrustedRoot` — `preflight capability evidence is incomplete` (`FreeInodes:0`, `QuotaSignal:unknown`)
  - `backupasset/runtime` `TestRuntimeAuthenticatedCacheUsesSharedContentMetrics` — `cache_root_unverified`
- Packages that passed on this attempt include `recovery` (150.9s), `handlers` (52.9s), `search`, `export`, `catalog`.

### Attempt 3 — PASS

Command actually run (same packages/count; `-p 2` and `TMPDIR=/tmp` to keep tmpfs from overflowing while still using a dedicated temp filesystem):

```bash
cd /home/murray/code/xirang/backend && env -u GOTMPDIR TMPDIR=/tmp go test -race -p 2 ./internal/backupasset/... ./internal/api/handlers/ -count=1
```

- Exit: **0**
- Duration: ~213s
- `/tmp` before start: tmpfs 14G, 4.0G free

Excerpt:

```
ok  	xirang/backend/internal/backupasset	2.891s
ok  	xirang/backend/internal/backupasset/catalog	8.794s
ok  	xirang/backend/internal/backupasset/content	4.838s
ok  	xirang/backend/internal/backupasset/export	58.463s
ok  	xirang/backend/internal/backupasset/ga	1.186s
ok  	xirang/backend/internal/backupasset/overlay	2.297s
ok  	xirang/backend/internal/backupasset/processing	10.645s
ok  	xirang/backend/internal/backupasset/processing/capabilities	11.279s
ok  	xirang/backend/internal/backupasset/processing/capabilityspec	1.015s
ok  	xirang/backend/internal/backupasset/processing/updater	4.267s
ok  	xirang/backend/internal/backupasset/provider	2.600s
ok  	xirang/backend/internal/backupasset/publication	1.033s
ok  	xirang/backend/internal/backupasset/recovery	161.927s
ok  	xirang/backend/internal/backupasset/repository	26.626s
ok  	xirang/backend/internal/backupasset/retention	7.124s
ok  	xirang/backend/internal/backupasset/runtime	18.350s
ok  	xirang/backend/internal/backupasset/search	4.597s
ok  	xirang/backend/internal/api/handlers	48.582s
EXIT:0
```

No DATA RACE lines. Gate recorded as PASS.

## 4. Frontend `npm run check`

Command:

```bash
cd /home/murray/code/xirang/web && env -u NODE_ENV npm run check
```

- Exit: **0**
- Duration: ~114s

Excerpt:

```
> xirang-web@0.1.0 check
> npm run typecheck && npm run lint && npm run test && npm run build

 Test Files  181 passed (181)
      Tests  1518 passed (1518)
   Duration  34.36s
...
All files          |   71.44 |    64.95 |   68.13 |   74.17 |
...
✓ built in 5.08s
EXIT:0
```

typecheck, eslint, vitest, and vite build all completed.

## 3. backupasset coverage floor

Command:

```bash
cd /home/murray/code/xirang && bash scripts/check-backupasset-coverage.sh
```

### Attempt 1 — FAIL (stale TMPDIR from race workaround)

- Exit: **1**
- `set -e` stopped the script after `go test` failed, so it never printed `backup-asset coverage: N%`.
- This sandbox still had `TMPDIR=/home/murray/code/xirang/.tmp/local-gates` from the race retry. Failures matched attempt-2 race artifacts (unix bind / unsafe cache root), not a coverage-floor miss.
- Coverprofile landed at `.tmp/local-gates/backupasset-coverage.out` (not `/tmp`).

### Attempt 2 — FAIL (tmpfs quota writing coverprofile)

```
saving coverage profile: write /tmp/backupasset-coverage.out: copy_file_range: disk quota exceeded
EXIT:1
```

`/tmp` is 14G tmpfs; ~9G already used by `/tmp/cursor-sandbox-cache`. Not a coverage-floor miss.

### Attempt 3 — FAIL (same quota, incomplete profile)

`go test` with coverprofile on disk still used `/tmp` for `$WORK`/sqlite. Multiple packages failed with `disk quota exceeded`. A leftover profile printed `72.9%` but **is not valid evidence** (recovery/runtime build failed).

### Attempt 4 — FAIL (custom GOCACHE + leftover tmpfs)

`GOCACHE=.tmp/go-cache` then:
- `export`: `could not import os/exec (open : no such file or directory)` (corrupt/incomplete cache)
- `processing/updater` `TestServiceStreams320MiBCanonicalBundleBelowHeapBudget`: `scan_err=updater activation failed` (likely tmpfs pressure writing the 320MiB fixture)

Partial profile `72.7%` also **not valid**.

### Attempt 5 — PASS

Command actually run (official script; env only, no script edit):

```bash
cd /home/murray/code/xirang
env -u GOTMPDIR TMPDIR=/tmp GOCACHE="$HOME/.cache/go-build" GOFLAGS=-p=1 bash scripts/check-backupasset-coverage.sh
```

- Exit: **0**
- Duration: ~235s
- `/tmp` after: 4.5G free

Excerpt:

```
backup-asset coverage: 73.4% (floor 55%)
EXIT:0
```

Floor is 55%. Gate recorded as PASS.

## 6. Playwright e2e

### Browser install

`npx playwright install --with-deps chromium firefox webkit` **failed** (`INSTALL_EXIT:1`):

```
sudo: 读取密码需要一个终端
Failed to install browsers
Error: Installation process exited with code: 1
```

`--with-deps` cannot install Ubuntu 20.04 fallback packages on this Arch host without an interactive sudo password.

Retry without `--with-deps`: `npx playwright install chromium firefox webkit` — **exit 0** after ~11 min. Playwright 1.55.1 downloaded:

- Chromium 140.0.7339.186 build v1193
- Firefox 141.0 build v1490
- WebKit build v2092 (`webkit_ubuntu20.04_x64_special-2092`, frozen)

Install still warned: host missing `libicudata.so.66`, `libicui18n.so.66`, `libicuuc.so.66`, `libxml2.so.2`, `libffi.so.7`. Host actually has ICU **78** and `libffi.so.8` (no `.66` / `.7` SONAMEs).

### Full matrix

```bash
cd /home/murray/code/xirang/web && env -u NODE_ENV npm run e2e
```

- Exit: **1**
- Duration: ~5s
- 6 tests, 6 workers

Excerpt:

```
  ✓  [chromium] closed FeatureLive does not open a searchable workspace (1.5s)
  ✓  [chromium] live FeatureLive can browse, search, and preview fixtures (2.0s)
  ✓  [firefox]  closed FeatureLive does not open a searchable workspace (1.5s)
  ✓  [firefox]  live FeatureLive can browse, search, and preview fixtures (2.2s)
  ✘  [webkit]   closed FeatureLive does not open a searchable workspace
  ✘  [webkit]   live FeatureLive can browse, search, and preview fixtures
  2 failed
  4 passed (4.2s)
EXIT:1
```

WebKit failure (both specs): `browserType.launch` host missing the SONAMEs above. **Not an assertion failure in the product specs.**

### Chromium-only / WebKit diagnostic

Chromium already passed in the full run (no separate chromium-only rerun needed).

`PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS=1 npx playwright test --project=webkit` still failed (`WEBKIT_SKIP_VALIDATE_EXIT:1`): MiniBrowser cannot load `libicudata.so.66` (exit 127). WebKit is actually unloadable on this host, not just a validator false positive.

No production walkthrough was run.

## Commit blockers

- **Product / Child 17 code:** none observed. Gates 1–5 and 7 passed. Chromium + Firefox e2e specs passed.
- **Local Playwright matrix:** incomplete. WebKit cannot launch on Arch (frozen ubuntu20.04 build needs ICU 66 / libffi 7 / libxml2.so.2). `--with-deps` blocked by sudo. This is a **host environment gap**, not a failing assertion in `e2e/backup-assets-gate.spec.ts`.
- CI on Ubuntu should still be treated as the WebKit proof; do not invent a local WebKit pass.
- Did not commit. Did not edit `CodeDefault` or `publish-images.yml`.
