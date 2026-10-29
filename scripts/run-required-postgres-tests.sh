#!/usr/bin/env bash
# Execute the checked-in required PostgreSQL inventory from any working directory.
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
args=()
list_seen=0
evidence_seen=0
while (($#)); do
  case "$1" in
    --list-only)
      ((list_seen == 0)) || { echo "duplicate --list-only" >&2; exit 2; }
      list_seen=1; args+=("$1"); shift ;;
    --evidence-dir)
      ((evidence_seen == 0 && $# >= 2)) && [[ -n "$2" && "$2" != --* ]] || { echo "invalid --evidence-dir" >&2; exit 2; }
      evidence_seen=1; args+=("$1" "$2"); shift 2 ;;
    *) echo "usage: $0 [--list-only] [--evidence-dir DIR]" >&2; exit 2 ;;
  esac
done
cd "$root/backend"
exec env -u GOFLAGS go run ./cmd/required-postgres-tests --inventory ../scripts/required-postgres-tests.json "${args[@]}"
