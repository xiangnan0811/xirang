#!/usr/bin/env bash
# Self-test for scripts/check-doc-freshness.sh.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/check-doc-freshness.sh"

run_case() {
  local label="$1"
  local changed="$2"
  local expect="$3"

  local output
  output="$(DOC_FRESHNESS_CHANGED_FILES="$changed" bash "$SCRIPT")"
  if [[ "$expect" == "warn" ]]; then
    if ! grep -q "⚠️" <<<"$output"; then
      echo "FAIL[$label]: expected warning"
      echo "$output"
      exit 1
    fi
  else
    if grep -q "⚠️" <<<"$output"; then
      echo "FAIL[$label]: expected clean output"
      echo "$output"
      exit 1
    fi
  fi
  echo "OK[$label]"
}

run_case "config-without-env-doc" $'backend/internal/config/config.go' warn
run_case "config-with-env-doc" $'backend/internal/config/config.go\ndocs/env-vars.md' clean
run_case "model-without-model-doc" $'backend/internal/model/models.go' warn
run_case "model-with-backend-doc" $'backend/internal/model/models.go\nbackend/README_backend.md' clean
run_case "release-without-doc" $'.github/workflows/publish-images.yml' warn
run_case "release-with-doc" $'.github/workflows/publish-images.yml\ndocs/maintainers/release.md' clean

echo ""
echo "OK: doc freshness self-test passed"
