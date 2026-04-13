# P1: Security Hardening & CI Quality Gates

Phase 1 of the Xirang improvement plan. Focuses on executor security and establishing CI quality gates.

## Scope

Two pull requests:

- **PR 1 — Security hardening**: Default root username warning + SSH auth code deduplication
- **PR 2 — CI quality gates**: golangci-lint, ESLint, dependency scanning, test coverage, container scanning

## PR 1: Security Hardening

### 1.1 Default `root` Username Warning

**Problem**: 5 locations across executors fall back to `user = "root"` when `node.Username` is empty. A misconfigured node silently connects as root with no indication.

**Affected files** (all in `backend/internal/task/executor/`):
- `executor.go` — lines 121, 273, 612
- `restic_executor.go` — line 412
- `command_executor.go` — line 34

**Fix**: Keep backward compatibility but add a warning log:

```go
user := strings.TrimSpace(node.Username)
if user == "" {
    user = "root"
    logger.Module("executor").Warn().Str("node", node.Name).
        Msg("节点未配置 SSH 用户名，默认使用 root")
}
```

This warning will be consolidated into the shared `ResolveSSHConfig` function (section 1.2), so it appears in one place rather than five.

### 1.2 SSH Auth Code Deduplication

**Problem**: The same ~15-line block (username resolution, port defaulting, auth type parsing, SSH key/password credential setup) is copy-pasted across all 5 executor entry points. This duplication increases the risk of inconsistent handling.

**Fix**: Extract a shared `ResolveSSHConfig` function in `executor.go`:

```go
// SSHConnectParams holds validated SSH connection parameters.
type SSHConnectParams struct {
    Host     string
    Port     int
    User     string
    AuthMethods []ssh.AuthMethod
}

// ResolveSSHConfig validates node SSH configuration and resolves credentials.
// Logs a warning if username is empty (defaults to root for backward compat).
func ResolveSSHConfig(node model.Node, db *gorm.DB) (SSHConnectParams, error) {
    user := strings.TrimSpace(node.Username)
    if user == "" {
        user = "root"
        logger.Module("executor").Warn().Str("node", node.Name).
            Msg("节点未配置 SSH 用户名，默认使用 root")
    }

    port := node.Port
    if port == 0 {
        port = 22
    }

    // resolveAuthMethods encapsulates the existing auth resolution logic:
    // password auth, inline private key, or SSHKey reference (loaded via db).
    authMethods, err := resolveAuthMethods(node, db)
    if err != nil {
        return SSHConnectParams{}, fmt.Errorf("节点 %s SSH 认证配置错误: %w", node.Name, err)
    }

    return SSHConnectParams{
        Host:        node.Host,
        Port:        port,
        User:        user,
        AuthMethods: authMethods,
    }, nil
}
```

All 5 call sites reduce to:

```go
params, err := ResolveSSHConfig(task.Node, e.db)
if err != nil {
    return -1, err
}
```

**Tests**: Add unit tests for `ResolveSSHConfig` covering:
- Normal case (username + key auth)
- Empty username (warns, defaults to root)
- Missing port (defaults to 22)
- Invalid auth type (returns error)
- Password auth fallback

### 1.3 Verification

- `go test ./internal/task/executor/...` — all existing + new tests pass
- `go build ./...` — no compilation errors
- Manual check: grep for remaining `user = "root"` patterns to confirm all consolidated

## PR 2: CI Quality Gates

All new checks are **required** (block merge on failure).

### 2.1 golangci-lint (Backend Lint)

Add a lint step to the existing `backend` job in `.github/workflows/ci.yml`:

```yaml
- name: Lint backend
  uses: golangci/golangci-lint-action@v7
  with:
    version: latest
    working-directory: backend
```

Create `backend/.golangci.yml` with a conservative starter config:

```yaml
linters:
  enable:
    - errcheck
    - govet
    - staticcheck
    - unused
    - ineffassign
    - gosimple

issues:
  max-issues-per-linter: 50
  max-same-issues: 3

run:
  timeout: 5m
```

**First-run strategy**: Run locally, fix violations before merging. The conservative linter set minimizes false positives.

### 2.2 ESLint (Frontend Lint)

**New dependencies** (devDependencies in `web/package.json`):
- `eslint` (v9)
- `@eslint/js`
- `typescript-eslint`
- `eslint-plugin-react-hooks`
- `eslint-plugin-react-refresh`

**New file**: `web/eslint.config.js` (flat config format):

```js
import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";

export default tseslint.config(
  { ignores: ["dist/", "coverage/"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": ["warn", { allowConstantExport: true }],
      // No stylistic rules — keep it focused on correctness
    },
  }
);
```

**Script changes** in `web/package.json`:
- `"lint"` → `"eslint ."`
- Add `"lint:fix"` → `"eslint . --fix"`
- Add lint step to `"check"` script: `"tsc --noEmit && eslint . && vitest run && vite build"`

**CI**: The existing `npm run check` step will now include ESLint automatically.

**First-run strategy**: Run `npx eslint . --fix` locally to auto-fix. Address remaining violations manually or with targeted `eslint-disable` comments (with TODO notes for cleanup).

### 2.3 Dependency Vulnerability Scanning

**Backend** — add to `backend` job in `ci.yml`:

```yaml
- name: Install govulncheck
  run: go install golang.org/x/vuln/cmd/govulncheck@latest

- name: Check Go vulnerabilities
  run: govulncheck ./...
```

`govulncheck` only reports vulnerabilities in actually-called code paths (more accurate than generic scanners).

**Frontend** — add to `frontend` job in `ci.yml`:

```yaml
- name: Check npm vulnerabilities
  run: npm audit --audit-level=moderate
```

Ignores low/info severity. For false positives or unfixable upstream vulnerabilities:
- npm: use `overrides` in `package.json`
- Go: `govulncheck` supports exclude annotations

### 2.4 Test Coverage Reporting

**Backend** — change test command:

```yaml
- name: Run backend tests
  run: go test -coverprofile=coverage.out ./...

- name: Upload backend coverage
  uses: codecov/codecov-action@v5
  with:
    files: backend/coverage.out
    flags: backend
    token: ${{ secrets.CODECOV_TOKEN }}
```

**Frontend** — add coverage dependency and configure:

New devDependency: `@vitest/coverage-v8`

Update vitest config to output lcov:

```ts
// in vitest.config.ts or vite.config.ts test block
test: {
  coverage: {
    provider: "v8",
    reporter: ["text", "lcov"],
    reportsDirectory: "coverage",
  },
}
```

Change test run to include coverage:

```yaml
- name: Upload frontend coverage
  uses: codecov/codecov-action@v5
  with:
    files: web/coverage/lcov.info
    flags: frontend
    token: ${{ secrets.CODECOV_TOKEN }}
```

**Threshold**: Reporting only initially (no minimum). Establish baseline first, then add "no decrease" rule via Codecov config.

**Setup**: Requires adding `CODECOV_TOKEN` to GitHub repository secrets. If the repo is public, Codecov works without a token for public repos.

### 2.5 Container Image Scanning

Add to `.github/workflows/publish-images.yml`, after image build and before push:

```yaml
- name: Scan image for vulnerabilities
  uses: aquasecurity/trivy-action@master
  with:
    image-ref: ${{ env.IMAGE_NAME }}:${{ github.sha }}
    format: 'table'
    exit-code: '1'
    severity: 'HIGH,CRITICAL'
    ignore-unfixed: true
```

Configuration:
- Only fail on HIGH/CRITICAL severity
- `ignore-unfixed: true` — don't block on upstream OS vulns with no available patch
- Runs only in the image publish workflow, not on every PR

### 2.6 Verification

After all CI changes:
- Push a test branch and verify all new CI jobs run and pass
- Intentionally introduce a lint violation to confirm blocking behavior
- Verify Codecov receives coverage data and comments on the PR
- Verify Trivy scan runs during image publish

## Out of Scope

Items deferred to later phases:
- **P2**: API response standardization, error handling consistency, backend test coverage expansion, SSH auth code beyond executor dedup
- **P3**: Frontend Context splitting, component splitting, lazy loading, bundle optimization
- **P4**: Prometheus metrics, database indexes, Makefile/DX, OpenAPI docs

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Existing lint violations block all PRs | Medium | High | Fix violations in the same PR that adds lint |
| `npm audit` fails on transitive dependency | Medium | Medium | Use `overrides` to pin fixed version |
| Codecov token setup forgotten | Low | Low | Coverage is advisory-only; CI still passes |
| Trivy false positives block image publish | Low | Medium | `ignore-unfixed: true` + severity filter |
| SSH auth refactor breaks executor | Low | High | Existing executor tests + new ResolveSSHConfig tests |

## Implementation Order

Within each PR, the order of changes:

**PR 1 (Security)**:
1. Add `ResolveSSHConfig` function + tests
2. Refactor all 5 executor call sites
3. Run `go test ./...` to verify no regressions

**PR 2 (CI)**:
1. Add `backend/.golangci.yml` + fix existing violations
2. Add ESLint config + deps + fix existing violations
3. Add govulncheck + npm audit steps
4. Add coverage tooling + Codecov integration
5. Add Trivy to publish-images workflow
6. Update CI workflow with all new steps
