#!/usr/bin/env bash
# Focused contract test for scripts/run-required-postgres-tests.sh.

set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
RUNNER="$ROOT_DIR/scripts/run-required-postgres-tests.sh"
WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT

cd "$ROOT_DIR/backend"

list_output=$(bash "$RUNNER" --list-only \
  "runner list contract" \
  ./internal/database \
  '^TestDrillRecoveryMigrationPair$')
grep -Fxq 'selected tests: 1' <<<"$list_output" || {
  echo "FAIL: list-only mode did not report one selected test" >&2
  exit 1
}
grep -Fxq '  TestDrillRecoveryMigrationPair' <<<"$list_output" || {
  echo "FAIL: list-only mode did not print the selected test name" >&2
  exit 1
}

if bash "$RUNNER" --list-only \
  "empty selector contract" \
  ./internal/database \
  '^TestRequiredPostgresRunnerNoSuchTest$' >"$WORK/empty.out" 2>&1; then
  echo "FAIL: an empty required PostgreSQL selector was accepted" >&2
  exit 1
fi
grep -Fq 'selected zero tests' "$WORK/empty.out" || {
  echo "FAIL: empty selector failure was not explicit" >&2
  exit 1
}

if env -u TEST_POSTGRES_DSN bash "$RUNNER" \
  "missing DSN contract" \
  ./internal/database \
  '^TestDrillRecoveryMigrationPair$' >"$WORK/missing-dsn.out" 2>&1; then
  echo "FAIL: required PostgreSQL execution without TEST_POSTGRES_DSN was accepted" >&2
  exit 1
fi
grep -Fq 'TEST_POSTGRES_DSN is required' "$WORK/missing-dsn.out" || {
  echo "FAIL: missing TEST_POSTGRES_DSN failure was not explicit" >&2
  exit 1
}

TEST_POSTGRES_DSN='postgres://contract.invalid/test' bash "$RUNNER" \
  "runner execution contract" \
  ./internal/database \
  '^TestDrillRecoveryMigrationPair$' >"$WORK/execution.out"
grep -Fq 'ok' "$WORK/execution.out" || {
  echo "FAIL: required PostgreSQL runner did not execute the selected Go test" >&2
  exit 1
}

mkdir -p "$WORK/fake-bin"
cat >"$WORK/fake-bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ " $* " == *" -list "* ]]; then
  printf 'TestFakeRequiredPostgres\n'
  exit 0
fi

printf '%s\n' "$*" >"${FAKE_GO_ARGS_FILE:?}"
printf 'ok\tfake/required-postgres\n'
EOF
chmod +x "$WORK/fake-bin/go"

FAKE_GO_ARGS_FILE="$WORK/fake-go.args" \
  TEST_POSTGRES_DSN='postgres://contract.invalid/test' \
  PATH="$WORK/fake-bin:$PATH" \
  bash "$RUNNER" \
    "runner timeout contract" \
    ./internal/database \
    '^TestFakeRequiredPostgres$' >"$WORK/timeout.out"
grep -Fq -- '-timeout=20m' "$WORK/fake-go.args" || {
  echo "FAIL: required PostgreSQL runner did not provide the 20-minute suite timeout" >&2
  exit 1
}

printf 'required PostgreSQL test runner self-test: PASS\n'
