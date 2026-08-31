#!/usr/bin/env bash
# Run local checks that mirror the repository's GitHub CI where practical.
# Intended for manual use and .githooks/pre-push.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_DIR="${ROOT_DIR}/backend"
WEB_DIR="${ROOT_DIR}/web"
LOCAL_TMP_DIR="${ROOT_DIR}/.trellis/.runtime/tmp"
mkdir -p "$LOCAL_TMP_DIR"

section() {
  echo ""
  echo "==> $1"
}

require_cmd() {
  local cmd="$1"
  local hint="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "[FAIL] 缺少依赖: $cmd" >&2
    echo "       $hint" >&2
    exit 2
  fi
}

run_in_dir() {
  local dir="$1"
  shift
  echo "+ (cd ${dir#$ROOT_DIR/} && $*)"
  (cd "$dir" && TMPDIR="$LOCAL_TMP_DIR" "$@")
}

run_script() {
  local script="$1"
  echo "+ bash ${script#$ROOT_DIR/}"
  bash "$script"
}

require_cmd go "请安装 Go，版本以 backend/go.mod 为准。"
require_cmd golangci-lint "请安装 golangci-lint（CI 使用 v2.11.4），或用 git push --no-verify 紧急跳过。"
require_cmd npm "请安装 Node.js/npm，CI 使用 Node.js 20。"
require_cmd node "请安装 Node.js，CI 使用 Node.js 20。"

section "Backend: golangci-lint run ./..."
run_in_dir "$BACKEND_DIR" golangci-lint run ./...

section "Backend: go mod download"
run_in_dir "$BACKEND_DIR" go mod download

section "Backend: go test ./..."
run_in_dir "$BACKEND_DIR" go test ./...

section "Backend: go build ./..."
run_in_dir "$BACKEND_DIR" go build ./...

section "Backend: govulncheck ./..."
run_in_dir "$BACKEND_DIR" go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

section "Frontend: npm audit --audit-level=moderate"
run_in_dir "$WEB_DIR" npm audit --audit-level=moderate

section "Frontend: npm run check"
run_in_dir "$WEB_DIR" npm run check

section "Frontend: bundle budget"
run_in_dir "$WEB_DIR" node scripts/check-bundle-budget.mjs

section "Docs: freshness check"
run_script "$ROOT_DIR/scripts/check-doc-freshness.sh"

section "Docs: freshness self-test"
run_script "$ROOT_DIR/scripts/check-doc-freshness.test.sh"

section "Docs: migration version freshness self-test"
run_script "$ROOT_DIR/scripts/check-migration-version.test.sh"

section "Migrations: UTC safety check"
run_script "$ROOT_DIR/scripts/check-migration-utc-safety.sh"

section "Migrations: UTC safety self-test"
run_script "$ROOT_DIR/scripts/check-migration-utc-safety.test.sh"

section "Local CI parity passed"
