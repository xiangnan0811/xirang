# P1: Security & CI Quality Gates — Implementation Plan

> **For agentic workers:** Use the current repo-approved task execution workflow to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden SSH executor defaults, deduplicate SSH auth code, and establish CI quality gates (lint, vulnerability scanning, coverage, container scanning).

**Architecture:** Two independent PRs. PR1 moves the existing `DialSSHForNode` helper to a shared location, adds a warning log for empty-username fallback, and makes 3 inline-auth call sites use it. PR2 adds golangci-lint, ESLint, govulncheck, npm audit, Codecov coverage, and Trivy to CI workflows.

**Tech Stack:** Go 1.24 (zerolog, GORM, golang.org/x/crypto/ssh), React 18 + TypeScript + Vite 7, GitHub Actions, ESLint 9, golangci-lint, govulncheck, Codecov, Trivy

---

## PR 1: Security Hardening (SSH Auth Dedup + Root Warning)

### Task 1: Move `DialSSHForNode` to shared file and add root-default warning

Currently `DialSSHForNode` and `RunSSHCommandOutput` live in `restic_executor.go` but are used by rclone and restic executors. They belong in a shared file.

**Files:**
- Create: `backend/internal/task/executor/ssh_connect.go`
- Modify: `backend/internal/task/executor/restic_executor.go` (remove lines 404-480)
- Test: `backend/internal/task/executor/ssh_connect_test.go`

- [ ] **Step 1: Create `ssh_connect.go` with `DialSSHForNode` + root warning**

```go
// backend/internal/task/executor/ssh_connect.go
package executor

import (
	"context"
	"fmt"
	"strings"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
)

// DialSSHForNode 为节点建立 SSH 连接（节点的 SSHKey 应已通过 Preload 加载）。
func DialSSHForNode(ctx context.Context, node model.Node) (*ssh.Client, error) {
	port := node.Port
	if port == 0 {
		port = 22
	}
	user := strings.TrimSpace(node.Username)
	if user == "" {
		user = "root"
		logger.Module("executor").Warn().Str("node", node.Name).
			Msg("节点未配置 SSH 用户名，默认使用 root")
	}

	authMethods, err := resolveSSHAuthMethods(node)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("主机密钥配置异常: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", node.Host, port)
	return sshutil.DialSSH(ctx, addr, user, authMethods, hostKeyCallback)
}

// resolveSSHAuthMethods 根据节点认证类型解析 SSH 认证方法。
func resolveSSHAuthMethods(node model.Node) ([]ssh.AuthMethod, error) {
	authType := strings.ToLower(strings.TrimSpace(node.AuthType))
	var authMethods []ssh.AuthMethod

	switch authType {
	case "key":
		keyContent, _, err := resolveNodePrivateKey(node)
		if err != nil {
			return nil, err
		}
		if keyContent == "" {
			return nil, fmt.Errorf("密钥认证未配置")
		}
		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			return nil, fmt.Errorf("私钥校验失败")
		}
		signer, err := ssh.ParsePrivateKey([]byte(normalizedKey))
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	case "password":
		if node.Password == "" {
			return nil, fmt.Errorf("密码认证未配置密码")
		}
		authMethods = append(authMethods, ssh.Password(node.Password))
	default:
		return nil, fmt.Errorf("不支持的认证方式: %s", authType)
	}
	return authMethods, nil
}

// ResolveSSHUser 返回节点的 SSH 用户名，空值时回退到 "root" 并记录警告。
// 用于不走 DialSSHForNode 的场景（如本地 rsync -e ssh）。
func ResolveSSHUser(node model.Node) string {
	user := strings.TrimSpace(node.Username)
	if user == "" {
		user = "root"
		logger.Module("executor").Warn().Str("node", node.Name).
			Msg("节点未配置 SSH 用户名，默认使用 root")
	}
	return user
}

// RunSSHCommandOutput 通过 SSH 执行命令并返回合并的 stdout+stderr 输出。
func RunSSHCommandOutput(ctx context.Context, client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-done:
		}
	}()

	out, err := session.CombinedOutput(cmd)
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}
```

- [ ] **Step 2: Remove `DialSSHForNode` and `RunSSHCommandOutput` from `restic_executor.go`**

Delete lines 404-480 from `backend/internal/task/executor/restic_executor.go` (the `DialSSHForNode` and `RunSSHCommandOutput` functions). They now live in `ssh_connect.go`. Since the functions keep the same name and stay in the same package, all existing callers (restic, rclone) continue to compile without changes.

- [ ] **Step 3: Verify compilation**

Run: `cd backend && go build ./...`
Expected: Clean build, no errors.

- [ ] **Step 4: Write tests for `ResolveSSHUser`**

```go
// backend/internal/task/executor/ssh_connect_test.go
package executor

import (
	"testing"

	"xirang/backend/internal/model"
)

func TestResolveSSHUserReturnsConfiguredUsername(t *testing.T) {
	node := model.Node{Name: "test-node", Username: "deploy"}
	user := ResolveSSHUser(node)
	if user != "deploy" {
		t.Fatalf("期望用户名=deploy，实际=%s", user)
	}
}

func TestResolveSSHUserDefaultsToRootWhenEmpty(t *testing.T) {
	node := model.Node{Name: "test-node", Username: ""}
	user := ResolveSSHUser(node)
	if user != "root" {
		t.Fatalf("期望空用户名回退到 root，实际=%s", user)
	}
}

func TestResolveSSHUserTrimsWhitespace(t *testing.T) {
	node := model.Node{Name: "test-node", Username: "  admin  "}
	user := ResolveSSHUser(node)
	if user != "admin" {
		t.Fatalf("期望去除空白后=admin，实际=%s", user)
	}
}

func TestResolveSSHAuthMethodsRejectsEmptyAuthType(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: ""}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望空认证类型报错")
	}
}

func TestResolveSSHAuthMethodsRejectsPasswordWithoutPassword(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: "password", Password: ""}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望无密码时报错")
	}
}

func TestResolveSSHAuthMethodsRejectsKeyWithoutKey(t *testing.T) {
	node := model.Node{Name: "test-node", AuthType: "key"}
	_, err := resolveSSHAuthMethods(node)
	if err == nil {
		t.Fatal("期望无密钥时报错")
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test ./internal/task/executor/... -v -run "TestResolveSSH"`
Expected: All 6 tests PASS.

- [ ] **Step 6: Commit**

```bash
cd backend && git add internal/task/executor/ssh_connect.go internal/task/executor/ssh_connect_test.go internal/task/executor/restic_executor.go
git commit -m "$(cat <<'EOF'
refactor(backend): extract DialSSHForNode to shared ssh_connect.go

Move DialSSHForNode and RunSSHCommandOutput from restic_executor.go
to a dedicated ssh_connect.go file. Add ResolveSSHUser helper that
logs a warning when username is empty and defaults to root. Extract
resolveSSHAuthMethods for reuse across executors.
EOF
)"
```

---

### Task 2: Refactor `CommandExecutor` to use `DialSSHForNode`

**Files:**
- Modify: `backend/internal/task/executor/command_executor.go:28-78`

- [ ] **Step 1: Replace inline SSH auth in `CommandExecutor.Run`**

Replace lines 28-82 of `command_executor.go` (from `port := node.Port` through `defer client.Close()`) with:

```go
	// 使用共享 SSH 连接逻辑
	client, err := DialSSHForNode(ctx, task.Node)
	if err != nil {
		return -1, err
	}
	defer client.Close()

	user := ResolveSSHUser(task.Node)
	addr := fmt.Sprintf("%s:%d", task.Node.Host, task.Node.Port)
	if task.Node.Port == 0 {
		addr = fmt.Sprintf("%s:22", task.Node.Host)
	}
	logf("info", fmt.Sprintf("连接节点 %s@%s", user, addr))
```

Also remove the now-unused import from command_executor.go: `"xirang/backend/internal/sshutil"`. Keep `"golang.org/x/crypto/ssh"` — it's still used for `ssh.ExitError` at line 158.

- [ ] **Step 2: Verify compilation**

Run: `cd backend && go build ./...`
Expected: Clean build.

- [ ] **Step 3: Run existing tests**

Run: `cd backend && go test ./internal/task/executor/... -v`
Expected: All existing tests pass (CommandExecutor tests connect to real SSH, so they might not exist — the key thing is no regressions in rsync/restic/rclone tests).

- [ ] **Step 4: Commit**

```bash
git add backend/internal/task/executor/command_executor.go
git commit -m "$(cat <<'EOF'
refactor(backend): CommandExecutor uses shared DialSSHForNode

Replace 50 lines of inline SSH auth logic with a single
DialSSHForNode call. Inherits root-default warning.
EOF
)"
```

---

### Task 3: Refactor `EnsureRemoteTargetReady` to use `DialSSHForNode`

**Files:**
- Modify: `backend/internal/task/executor/executor.go:601-656`

- [ ] **Step 1: Replace inline SSH auth in `EnsureRemoteTargetReady`**

Replace lines 606-655 of `executor.go` (from `port := node.Port` through the `sshutil.DialSSH` call and its error handling) with:

```go
	client, err := DialSSHForNode(ctx, node)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()
```

This replaces ~50 lines of inline auth code. The rest of the function (target path check, disk space check) remains unchanged.

- [ ] **Step 2: Verify compilation**

Run: `cd backend && go build ./...`
Expected: Clean build.

- [ ] **Step 3: Run all executor tests**

Run: `cd backend && go test ./internal/task/executor/... -v`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/task/executor/executor.go
git commit -m "$(cat <<'EOF'
refactor(backend): EnsureRemoteTargetReady uses shared DialSSHForNode

Replace inline SSH auth with DialSSHForNode call.
Inherits root-default warning and consistent auth handling.
EOF
)"
```

---

### Task 4: Refactor `runRemoteRestore` to use `DialSSHForNode`

**Files:**
- Modify: `backend/internal/task/executor/executor.go:260-322`

- [ ] **Step 1: Replace inline SSH auth in `runRemoteRestore`**

Replace lines 267-322 of `executor.go` (from `port := task.Node.Port` through `defer client.Close()`) with:

```go
	client, err := DialSSHForNode(ctx, task.Node)
	if err != nil {
		return -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()
```

Note: `runRemoteRestore` previously only supported key auth. By using `DialSSHForNode`, it now also supports password auth — a minor improvement. The `user` variable is no longer needed here since the remote rsync command runs entirely on the node (no `user@host:` prefix). Remove any references to `user` in the remaining code of this function.

- [ ] **Step 2: Add root-default warning to `RsyncExecutor.Run` backup path**

In `executor.go` line 119-121 (the rsync backup path that builds `-e ssh` args), replace:

```go
		user := strings.TrimSpace(task.Node.Username)
		if user == "" {
			user = "root"
		}
```

with:

```go
		user := ResolveSSHUser(task.Node)
```

- [ ] **Step 3: Verify compilation**

Run: `cd backend && go build ./...`
Expected: Clean build.

- [ ] **Step 4: Run full test suite**

Run: `cd backend && go test ./... -count=1`
Expected: All tests pass.

- [ ] **Step 5: Verify no remaining inline `user = "root"` patterns**

Run: `grep -rn 'user = "root"' backend/internal/task/executor/`
Expected: Zero matches (all consolidated into `ResolveSSHUser` and `DialSSHForNode`).

- [ ] **Step 6: Commit**

```bash
git add backend/internal/task/executor/executor.go
git commit -m "$(cat <<'EOF'
refactor(backend): consolidate all SSH user defaults via ResolveSSHUser

runRemoteRestore and RsyncExecutor.Run now use shared helpers.
All 5 user="root" fallbacks are consolidated. grep confirms zero
remaining inline defaults.
EOF
)"
```

---

## PR 2: CI Quality Gates

### Task 5: Add golangci-lint to backend CI

**Files:**
- Create: `backend/.golangci.yml`
- Modify: `.github/workflows/ci.yml` (backend job)

- [ ] **Step 1: Create golangci-lint config**

```yaml
# backend/.golangci.yml
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

- [ ] **Step 2: Run golangci-lint locally to find violations**

Run: `cd backend && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest && golangci-lint run ./...`
Expected: Note any violations. Fix them before proceeding.

- [ ] **Step 3: Fix all lint violations found in Step 2**

Fix each violation in the relevant source files. Common fixes:
- `errcheck`: add `_ =` prefix or handle the error
- `unused`: remove dead code
- `ineffassign`: remove or use the variable

- [ ] **Step 4: Verify lint passes cleanly**

Run: `cd backend && golangci-lint run ./...`
Expected: Zero violations.

- [ ] **Step 5: Add lint step to CI**

In `.github/workflows/ci.yml`, add this step to the `backend` job, after "Setup Go" and before "Download modules":

```yaml
      - name: Lint backend
        uses: golangci/golangci-lint-action@v7
        with:
          version: latest
          working-directory: backend
```

- [ ] **Step 6: Commit**

```bash
git add backend/.golangci.yml .github/workflows/ci.yml
git add -u backend/  # any lint fixes
git commit -m "$(cat <<'EOF'
ci(backend): add golangci-lint with conservative linter set

Enable errcheck, govet, staticcheck, unused, ineffassign, gosimple.
Fix all existing violations. Blocks PR merge on failure.
EOF
)"
```

---

### Task 6: Add ESLint to frontend

**Files:**
- Create: `web/eslint.config.js`
- Modify: `web/package.json` (scripts + devDependencies)

- [ ] **Step 1: Install ESLint dependencies**

Run: `cd web && npm install -D eslint @eslint/js typescript-eslint eslint-plugin-react-hooks eslint-plugin-react-refresh`

- [ ] **Step 2: Create ESLint flat config**

```js
// web/eslint.config.js
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
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],
    },
  }
);
```

- [ ] **Step 3: Update package.json scripts**

Change `"lint"` and `"check"` scripts in `web/package.json`:

```json
{
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "typecheck": "tsc -b --noEmit",
    "check": "npm run typecheck && npm run lint && npm run test && npm run build",
    "preview": "vite preview",
    "lint": "eslint .",
    "lint:fix": "eslint . --fix",
    "test": "vitest run"
  }
}
```

- [ ] **Step 4: Run ESLint with auto-fix**

Run: `cd web && npx eslint . --fix`
Expected: Auto-fixable issues resolved.

- [ ] **Step 5: Fix remaining ESLint violations manually**

Run: `cd web && npx eslint .`
Fix each violation. For issues that are intentional patterns, add targeted `// eslint-disable-next-line` with a comment explaining why.

- [ ] **Step 6: Verify ESLint passes cleanly**

Run: `cd web && npm run lint`
Expected: Zero violations.

- [ ] **Step 7: Verify full check pipeline**

Run: `cd web && npm run check`
Expected: typecheck + lint + test + build all pass.

- [ ] **Step 8: Commit**

```bash
git add web/eslint.config.js web/package.json web/package-lock.json
git add -u web/src/  # any lint fixes
git commit -m "$(cat <<'EOF'
ci(web): add ESLint 9 with TypeScript and React hooks rules

Configure flat config with @eslint/js, typescript-eslint,
react-hooks, and react-refresh plugins. Fix all existing
violations. Integrated into npm run check pipeline.
EOF
)"
```

---

### Task 7: Add dependency vulnerability scanning to CI

**Files:**
- Modify: `.github/workflows/ci.yml` (backend + frontend jobs)

- [ ] **Step 1: Add govulncheck to backend CI job**

In `.github/workflows/ci.yml`, add these steps to the `backend` job, after "Build backend":

```yaml
      - name: Install govulncheck
        run: go install golang.org/x/vuln/cmd/govulncheck@latest

      - name: Check Go vulnerabilities
        run: govulncheck ./...
```

- [ ] **Step 2: Add npm audit to frontend CI job**

In `.github/workflows/ci.yml`, add this step to the `frontend` job, after "Install dependencies":

```yaml
      - name: Check npm vulnerabilities
        run: npm audit --audit-level=moderate
```

- [ ] **Step 3: Verify locally**

Run in parallel:
- `cd backend && go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...`
- `cd web && npm audit --audit-level=moderate`

Expected: Both pass. If npm audit fails on a transitive dependency, add an `overrides` entry in `web/package.json` to pin the fixed version.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "$(cat <<'EOF'
ci: add dependency vulnerability scanning

Backend: govulncheck (Go vulnerability database, call-graph aware).
Frontend: npm audit --audit-level=moderate.
Both block PR merge on findings.
EOF
)"
```

---

### Task 8: Add test coverage reporting with Codecov

**Files:**
- Modify: `.github/workflows/ci.yml` (backend + frontend jobs)
- Modify: `web/vite.config.ts` (add coverage config)
- Modify: `web/package.json` (add @vitest/coverage-v8)

- [ ] **Step 1: Install coverage dependency**

Run: `cd web && npm install -D @vitest/coverage-v8`

- [ ] **Step 2: Add coverage config to vite.config.ts**

In `web/vite.config.ts`, update the `test` block:

```ts
  test: {
    environment: "jsdom",
    setupFiles: "./vitest.setup.ts",
    globals: true,
    css: true,
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      reportsDirectory: "coverage",
    },
  }
```

- [ ] **Step 3: Verify coverage runs locally**

Run: `cd web && npx vitest run --coverage`
Expected: Coverage report printed to terminal, `web/coverage/lcov.info` created.

- [ ] **Step 4: Update CI backend job for coverage**

In `.github/workflows/ci.yml`, change the backend test step and add upload:

```yaml
      - name: Run backend tests
        run: go test -coverprofile=coverage.out ./...

      - name: Upload backend coverage
        if: always()
        uses: codecov/codecov-action@v5
        with:
          files: backend/coverage.out
          flags: backend
          token: ${{ secrets.CODECOV_TOKEN }}
```

- [ ] **Step 5: Update CI frontend job for coverage**

In `.github/workflows/ci.yml`, add coverage upload after the frontend check step:

```yaml
      - name: Upload frontend coverage
        if: always()
        uses: codecov/codecov-action@v5
        with:
          files: web/coverage/lcov.info
          flags: frontend
          token: ${{ secrets.CODECOV_TOKEN }}
```

Note: The `npm run check` script already runs `vitest run`. To also generate coverage during CI, either:
- Change `"test"` to `"vitest run --coverage"` in package.json, OR
- Add a separate `npx vitest run --coverage` step before the upload

Choose option A — update the test script:

```json
"test": "vitest run --coverage"
```

- [ ] **Step 6: Verify npm run check still works**

Run: `cd web && npm run check`
Expected: typecheck + lint + test (with coverage) + build all pass. `coverage/lcov.info` exists.

- [ ] **Step 7: Add `coverage/` to `.gitignore`**

Check if `web/.gitignore` or root `.gitignore` already ignores coverage:

Run: `grep -r "coverage" .gitignore web/.gitignore 2>/dev/null`

If not present, add `coverage/` to `web/.gitignore`.

- [ ] **Step 8: Commit**

```bash
git add .github/workflows/ci.yml web/vite.config.ts web/package.json web/package-lock.json
git commit -m "$(cat <<'EOF'
ci: add test coverage reporting via Codecov

Backend: go test -coverprofile + codecov upload.
Frontend: vitest --coverage (v8 provider) + codecov upload.
Reporting only — no minimum threshold enforced yet.

Requires CODECOV_TOKEN in repository secrets.
EOF
)"
```

---

### Task 9: Add Trivy container image scanning

**Files:**
- Modify: `.github/workflows/publish-images.yml`

- [ ] **Step 1: Add Trivy scan step**

In `.github/workflows/publish-images.yml`, add this step after "Build and push image" and before "Attest build provenance":

```yaml
      - name: Scan image for vulnerabilities
        uses: aquasecurity/trivy-action@master
        with:
          image-ref: docker.io/${{ env.IMAGE_NAMESPACE }}/xirang:v${{ steps.version.outputs.version }}
          format: 'table'
          exit-code: '1'
          severity: 'HIGH,CRITICAL'
          ignore-unfixed: true
```

Note: The scan runs on the already-pushed image. If we want to scan BEFORE push, we'd need to restructure the build step to load locally first. For simplicity, scan after push — a failing scan alerts maintainers but the image is already published. This is acceptable for an initial setup; we can move to pre-push later.

Actually, to scan before push we can split the build step. But for this first iteration, scanning after push with alerting is sufficient.

- [ ] **Step 2: Verify the workflow syntax is valid**

Run: `cd /Users/weibo/Code/xirang && python3 -c "import yaml; yaml.safe_load(open('.github/workflows/publish-images.yml'))"`
Expected: No error (valid YAML).

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/publish-images.yml
git commit -m "$(cat <<'EOF'
ci: add Trivy container image scanning on publish

Scans for HIGH/CRITICAL vulnerabilities after image push.
Ignores unfixed upstream CVEs. Fails the workflow on findings
to alert maintainers.
EOF
)"
```

---

### Task 10: Final verification and PR preparation

- [ ] **Step 1: Run full backend test suite**

Run: `cd backend && go test ./... -count=1`
Expected: All tests pass.

- [ ] **Step 2: Run full frontend check**

Run: `cd web && npm run check`
Expected: typecheck + lint + test + build all pass.

- [ ] **Step 3: Verify CI workflow YAML validity**

Run:
```bash
python3 -c "
import yaml, sys
for f in ['.github/workflows/ci.yml', '.github/workflows/publish-images.yml']:
    try:
        yaml.safe_load(open(f))
        print(f'{f}: OK')
    except Exception as e:
        print(f'{f}: FAIL - {e}')
        sys.exit(1)
"
```
Expected: Both OK.

- [ ] **Step 4: Review git log for PR 1 commits**

Run: `git log --oneline` and verify PR 1 commits (Tasks 1-4) are clean.

- [ ] **Step 5: Review git log for PR 2 commits**

Verify PR 2 commits (Tasks 5-9) are clean and self-contained.
