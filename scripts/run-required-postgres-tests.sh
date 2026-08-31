#!/usr/bin/env bash
# List and run a required PostgreSQL Go test slice without allowing an empty
# selector or a missing database to masquerade as coverage.

set -euo pipefail

usage() {
  echo "usage: $0 [--list-only] <suite-label> <go-package> <test-selector>" >&2
  exit 2
}

list_only=0
if [[ "${1:-}" == "--list-only" ]]; then
  list_only=1
  shift
fi

[[ "$#" -eq 3 ]] || usage

suite_label=$1
package_path=$2
selector=$3

[[ -n "$suite_label" && -n "$package_path" && -n "$selector" ]] || usage

if ! command -v go >/dev/null 2>&1; then
  echo "required PostgreSQL test runner: go is required" >&2
  exit 2
fi

if ! list_output=$(go test "$package_path" -list "$selector" -count=1); then
  echo "required PostgreSQL test runner: failed to list suite '$suite_label'" >&2
  exit 1
fi

selected_tests=$(printf '%s\n' "$list_output" | awk '/^Test[A-Za-z0-9_]+$/')
if [[ -z "$selected_tests" ]]; then
  echo "required PostgreSQL test runner: selector for '$suite_label' selected zero tests" >&2
  exit 1
fi
selected_count=$(printf '%s\n' "$selected_tests" | awk 'END { print NR }')

printf 'required PostgreSQL suite: %s\n' "$suite_label"
printf 'selected tests: %s\n' "$selected_count"
while IFS= read -r selected_test; do
  printf '  %s\n' "$selected_test"
done <<<"$selected_tests"

if [[ "$list_only" -eq 1 ]]; then
  exit 0
fi

if [[ -z "${TEST_POSTGRES_DSN:-}" ]]; then
  echo "required PostgreSQL test runner: TEST_POSTGRES_DSN is required for '$suite_label'" >&2
  exit 1
fi

go test "$package_path" -run "$selector" -count=1
