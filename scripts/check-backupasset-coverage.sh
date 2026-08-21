#!/usr/bin/env bash
# Fail if backupasset package coverage is below the reviewed floor.
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BACKEND_DIR="$ROOT_DIR/backend"
FLOOR=${BACKUP_ASSET_COVERAGE_FLOOR:-55}

cd "$BACKEND_DIR"
go test ./internal/backupasset/... -coverprofile="${TMPDIR:-/tmp}/backupasset-coverage.out" -count=1 >/tmp/backupasset-coverage-test.log
total=$(go tool cover -func="${TMPDIR:-/tmp}/backupasset-coverage.out" | awk '/^total:/ {print $3}' | tr -d '%')
if [[ -z "$total" ]]; then
  echo "backup-asset coverage: could not read total" >&2
  exit 1
fi
echo "backup-asset coverage: ${total}% (floor ${FLOOR}%)"
awk -v total="$total" -v floor="$FLOOR" 'BEGIN { if (total + 0 < floor + 0) { exit 1 } }' || {
  echo "backup-asset coverage ${total}% is below floor ${FLOOR}%" >&2
  exit 1
}
