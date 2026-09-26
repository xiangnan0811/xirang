#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf -- "$fixture"' EXIT
mkdir -p "$fixture/repo/scripts" "$fixture/repo/backend" "$fixture/bin"
cp "$ROOT_DIR/scripts/lint-backend.sh" "$fixture/repo/scripts/"

cat > "$fixture/bin/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s|%s|%s\n' "$PWD" "${GOTOOLCHAIN:-auto}" "$*" >> "$LINT_TEST_TRACE"
if [[ "$1" == version ]]; then
  echo 'go version go1.27.1 linux/amd64'
  exit 0
fi
[[ "$*" == 'run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...' ]]
exit "${LINT_TEST_EXIT:-0}"
SH
cat > "$fixture/bin/golangci-lint" <<'SH'
#!/usr/bin/env bash
echo 'old Go 1.26 linter must not run' >&2
exit 99
SH
chmod +x "$fixture/bin/go" "$fixture/bin/golangci-lint"
export PATH="$fixture/bin:$PATH"
export LINT_TEST_TRACE="$fixture/trace"
export GOTOOLCHAIN=go1.27.1

# Invoke from outside the checkout and keep the caller's selected Go toolchain.
(cd "$fixture" && bash "$fixture/repo/scripts/lint-backend.sh")
expected="$fixture/repo/backend|go1.27.1|run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./..."
grep -Fxq "$expected" "$LINT_TEST_TRACE"

# A lint failure must still block the caller (and therefore the push gate).
status=0
LINT_TEST_EXIT=17 bash "$fixture/repo/scripts/lint-backend.sh" || status=$?
[[ "$status" == 17 ]]

# The local entrypoints must share the wrapper and match the CI release.
grep -Fq 'bash scripts/lint-backend.sh' "$ROOT_DIR/Makefile"
grep -Fq 'run_in_dir "$BACKEND_DIR" bash "$ROOT_DIR/scripts/lint-backend.sh"' "$ROOT_DIR/scripts/local-ci-parity.sh"
grep -Eq '^ +version: v2\.14\.0$' "$ROOT_DIR/.github/workflows/ci.yml"
echo 'backend lint toolchain regression: PASS'
