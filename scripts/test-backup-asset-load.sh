#!/usr/bin/env bash
# scripts/test-backup-asset-load.sh
#
# Bounded backup-asset load/security gate. CI scale is small and explicit.
# The script reuses existing Child 6/8/12/13 owners instead of generating
# Catalog, Range, export, or recovery datasets.
#
# Local-only hooks (never enabled in CI; this script refuses to generate them):
#   BACKUP_ASSET_LOAD_LOCAL=million-catalog
#   BACKUP_ASSET_LOAD_LOCAL=archive-bomb
#   BACKUP_ASSET_LOAD_LOCAL=process-restart
#
# Usage:
#   bash scripts/test-backup-asset-load.sh
# Exit: 0 OK; 1 contract failure; 2 missing toolchain/layout.

set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BACKEND_DIR="$ROOT_DIR/backend"

# Explicit CI scale. Do not raise these without a dedicated local hook.
CI_PAGE_SIZE=8
CI_CONCURRENT_PREVIEW_N=2
CI_CATALOG_ENTRY_CAP=16
CI_CONCURRENT_LEASE_CALLERS_MAX=16

fail() {
  echo "backup-asset load/security: $*" >&2
  exit 1
}

require_text() {
  local path=$1
  local needle=$2
  [[ -f "$path" ]] || fail "owner file is missing: $path"
  grep -Fq -- "$needle" "$path" || fail "$path is missing ${needle}"
}

if [[ ! -d "$BACKEND_DIR" ]]; then
  echo "backup-asset load/security: backend directory is missing" >&2
  exit 2
fi
if ! command -v go >/dev/null 2>&1; then
  echo "backup-asset load/security: go is required" >&2
  exit 2
fi

if [[ -n "${BACKUP_ASSET_LOAD_LOCAL:-}" ]]; then
  fail "unbounded local soak '${BACKUP_ASSET_LOAD_LOCAL}' is a comment-only hook; do not generate datasets here"
fi

(( CI_PAGE_SIZE <= 8 )) || fail "CI_PAGE_SIZE must stay <= 8"
(( CI_CONCURRENT_PREVIEW_N <= 2 )) || fail "CI_CONCURRENT_PREVIEW_N must stay <= 2"
(( CI_CATALOG_ENTRY_CAP <= 16 )) || fail "CI_CATALOG_ENTRY_CAP must stay <= 16"

CATALOG_TEST="$BACKEND_DIR/internal/backupasset/catalog/service_test.go"
RANGE_TEST="$BACKEND_DIR/internal/backupasset/content/range_test.go"
LEASE_TEST="$BACKEND_DIR/internal/backupasset/content/lease_test.go"
BROKER_TEST="$BACKEND_DIR/internal/backupasset/content/broker_test.go"
TICKET_TEST="$BACKEND_DIR/internal/backupasset/content/ticket_test.go"
BUDGET_TEST="$BACKEND_DIR/internal/backupasset/content/budget_test.go"
EXPORT_DELIVERY_TEST="$BACKEND_DIR/internal/backupasset/export/delivery_test.go"
EXPORT_SERVICE_TEST="$BACKEND_DIR/internal/backupasset/export/service_test.go"
RECOVERY_STATE_TEST="$BACKEND_DIR/internal/backupasset/recovery/state_test.go"
AUDIT_TEST="$BACKEND_DIR/internal/backupasset/audit_action_test.go"

require_text "$CATALOG_TEST" "func TestCatalogServiceFiltersBeforePaginationAndBrowsesOfflineCommittedCatalog"
require_text "$CATALOG_TEST" "Limit: 1, Sort: RecoveryPointSortCapturedDesc"
require_text "$RANGE_TEST" "func TestRangePlansFullNormalOpenAndSuffixRepresentations"
require_text "$RANGE_TEST" "func TestRangeRejectsMalformedDuplicateMultipartOverflowAndUnsatisfiedInputs"
require_text "$LEASE_TEST" "func TestContentLeaseHeartbeatCoalescesConcurrentRenewals"
require_text "$BROKER_TEST" "func TestBrokerSecretPreviewRequiresExactProofAndBindsProofExpiry"
require_text "$TICKET_TEST" "func TestParseDeliveryCookieRejectsDuplicatesAndMalformedValues"
require_text "$BUDGET_TEST" "func TestBudgetReplayDoesNotReserveTwice"
require_text "$EXPORT_DELIVERY_TEST" "func TestDeliveryGatewayRejectsTerminalRequestIDReplay"
require_text "$EXPORT_DELIVERY_TEST" "func TestDeliveryGatewayRestartReconcilesPendingBudgetAndPartialRevocation"
require_text "$EXPORT_SERVICE_TEST" "func TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays"
require_text "$RECOVERY_STATE_TEST" "func TestStateResultSetRetryAndTakeoverRequireFreshFence"
require_text "$AUDIT_TEST" "func TestAuditSanitizerDropsForbiddenKeysAndValues"

callers=$(awk '
  /func TestContentLeaseHeartbeatCoalescesConcurrentRenewals/ { inside = 1 }
  inside && /^func Test/ && !/TestContentLeaseHeartbeatCoalescesConcurrentRenewals/ { exit }
  inside && /const callers = [0-9]+/ {
    if (match($0, /[0-9]+/)) print substr($0, RSTART, RLENGTH)
  }
' "$LEASE_TEST")
[[ -n "$callers" ]] || fail "concurrent lease owner does not declare a caller count"
(( callers <= CI_CONCURRENT_LEASE_CALLERS_MAX )) || fail "concurrent lease callers=$callers exceeds small CI scale"

run_go() {
  local pkg=$1
  local selector=$2
  echo "backup-asset load/security: go test $pkg -run $selector -count=1 (page=${CI_PAGE_SIZE} preview_n=${CI_CONCURRENT_PREVIEW_N} catalog_cap=${CI_CATALOG_ENTRY_CAP})"
  (
    cd "$BACKEND_DIR"
    go test "$pkg" -run "$selector" -count=1
  ) || fail "go test $pkg $selector failed"
}

# Pagination (Child 6). The owner pages with Limit: 1, below CI_PAGE_SIZE.
run_go ./internal/backupasset/catalog \
  '^TestCatalogServiceFiltersBeforePaginationAndBrowsesOfflineCommittedCatalog$'

# Range, malformed Range, concurrent lease (small N), preview, malformed ticket, ticket/budget replay.
run_go ./internal/backupasset/content \
  '^(TestRangePlansFullNormalOpenAndSuffixRepresentations|TestRangeRejectsMalformedDuplicateMultipartOverflowAndUnsatisfiedInputs|TestContentLeaseHeartbeatCoalescesConcurrentRenewals|TestBrokerSecretPreviewRequiresExactProofAndBindsProofExpiry|TestParseDeliveryCookieRejectsDuplicatesAndMalformedValues|TestBudgetReplayDoesNotReserveTwice)$'

# Child 12 restart-safe export + ticket replay. No export fixture is generated here.
run_go ./internal/backupasset/export \
  '^(TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays|TestDeliveryGatewayRejectsTerminalRequestIDReplay|TestDeliveryGatewayRestartReconcilesPendingBudgetAndPartialRevocation)$'

# Child 13 restart-safe recovery fence. No recovery dataset is generated here.
run_go ./internal/backupasset/recovery \
  '^TestStateResultSetRetryAndTakeoverRequireFreshFence$'

# Audit redaction: locators, tickets, cookies, and content fields stay out.
run_go ./internal/backupasset \
  '^TestAuditSanitizerDropsForbiddenKeysAndValues$'

echo "backup-asset load/security: PASS"
echo "backup-asset load/security: CI scale page=${CI_PAGE_SIZE} preview_n=${CI_CONCURRENT_PREVIEW_N} catalog_cap=${CI_CATALOG_ENTRY_CAP}; no unbounded dataset"
