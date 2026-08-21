#!/usr/bin/env bash
# scripts/test-backup-asset-load.sh
#
# Bounded backup-asset load/security gate.
# CI runs a real 10k catalog pagination owner, zip-bomb rejection, and a
# controlled SIGKILL-then-reconcile owner. A literal million-row catalog is
# not generated in-tree.
#
# Modes (BACKUP_ASSET_LOAD_LOCAL):
#   unset / ci-bounded   — CI contract (default)
#   million-catalog      — same 10k catalog owner; not a literal million
#   archive-bomb         — zip/ratio bomb rejection owners
#   process-restart      — restart/reconcile + SIGKILL owners
#   million-catalog-full — reserved; fails unless BACKUP_ASSET_LOAD_ALLOW_MILLION=1
#
# Operator notes: docs/admin/backup-assets-load.md
#
# Usage:
#   bash scripts/test-backup-asset-load.sh
# Exit: 0 OK; 1 contract failure; 2 missing toolchain/layout.

set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BACKEND_DIR="$ROOT_DIR/backend"

CI_PAGE_SIZE=8
CI_CONCURRENT_PREVIEW_N=2
CI_CATALOG_ENTRY_CAP=10000
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

LOAD_MODE=${BACKUP_ASSET_LOAD_LOCAL:-ci-bounded}
case "$LOAD_MODE" in
  ci-bounded|"") ;;
  million-catalog|archive-bomb|process-restart) ;;
  million-catalog-full)
    if [[ "${BACKUP_ASSET_LOAD_ALLOW_MILLION:-}" != "1" ]]; then
      fail "million-catalog-full requires BACKUP_ASSET_LOAD_ALLOW_MILLION=1 and an operator host; CI must not set this"
    fi
    ;;
  *)
    fail "unknown BACKUP_ASSET_LOAD_LOCAL='$LOAD_MODE'"
    ;;
esac

(( CI_PAGE_SIZE <= 8 )) || fail "CI_PAGE_SIZE must stay <= 8"
(( CI_CONCURRENT_PREVIEW_N <= 2 )) || fail "CI_CONCURRENT_PREVIEW_N must stay <= 2"
(( CI_CATALOG_ENTRY_CAP >= 10000 )) || fail "CI_CATALOG_ENTRY_CAP must stay >= 10000"

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
LOAD_DOC="$ROOT_DIR/docs/admin/backup-assets-load.md"

require_text "$CATALOG_TEST" "func TestCatalogPaginatesTenThousandCommittedEntries"
require_text "$CATALOG_TEST" "const total = 10000"
require_text "$RANGE_TEST" "func TestRangePlansFullNormalOpenAndSuffixRepresentations"
require_text "$RANGE_TEST" "func TestRangeRejectsMalformedDuplicateMultipartOverflowAndUnsatisfiedInputs"
require_text "$LEASE_TEST" "func TestContentLeaseHeartbeatCoalescesConcurrentRenewals"
require_text "$BROKER_TEST" "func TestBrokerSecretPreviewRequiresExactProofAndBindsProofExpiry"
require_text "$TICKET_TEST" "func TestParseDeliveryCookieRejectsDuplicatesAndMalformedValues"
require_text "$BUDGET_TEST" "func TestBudgetReplayDoesNotReserveTwice"
require_text "$EXPORT_DELIVERY_TEST" "func TestDeliveryGatewayRejectsTerminalRequestIDReplay"
require_text "$EXPORT_DELIVERY_TEST" "func TestDeliveryGatewayRestartReconcilesPendingBudgetAndPartialRevocation"
require_text "$EXPORT_DELIVERY_TEST" "func TestControlledProcessSIGKILLThenRestartReconciles"
require_text "$EXPORT_SERVICE_TEST" "func TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays"
require_text "$RECOVERY_STATE_TEST" "func TestStateResultSetRetryAndTakeoverRequireFreshFence"
require_text "$AUDIT_TEST" "func TestAuditSanitizerDropsForbiddenKeysAndValues"
require_text "$LOAD_DOC" "10k catalog pagination"

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

run_go ./internal/backupasset/catalog \
  '^TestCatalogPaginatesTenThousandCommittedEntries$'

run_go ./internal/backupasset/content \
  '^(TestRangePlansFullNormalOpenAndSuffixRepresentations|TestRangeRejectsMalformedDuplicateMultipartOverflowAndUnsatisfiedInputs|TestContentLeaseHeartbeatCoalescesConcurrentRenewals|TestBrokerSecretPreviewRequiresExactProofAndBindsProofExpiry|TestParseDeliveryCookieRejectsDuplicatesAndMalformedValues|TestBudgetReplayDoesNotReserveTwice)$'

run_go ./internal/backupasset/export \
  '^(TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays|TestDeliveryGatewayRejectsTerminalRequestIDReplay|TestDeliveryGatewayRestartReconcilesPendingBudgetAndPartialRevocation|TestControlledProcessSIGKILLThenRestartReconciles)$'

run_go ./internal/backupasset/recovery \
  '^TestStateResultSetRetryAndTakeoverRequireFreshFence$'

run_go ./internal/backupasset \
  '^TestAuditSanitizerDropsForbiddenKeysAndValues$'

if [[ "$LOAD_MODE" == "archive-bomb" || "$LOAD_MODE" == "ci-bounded" ]]; then
  run_go ./internal/backupasset/processing/capabilities \
    '^TestArchiveInspectRejectsTraversalLinksDevicesBombsAndEncryption$'
  run_go ./internal/backupasset/processing \
    '^TestArchiveMemberRatioBombPersistsGenericClosedLimitProduct$'
fi

if [[ "$LOAD_MODE" == "process-restart" || "$LOAD_MODE" == "ci-bounded" ]]; then
  run_go ./internal/backupasset/export \
    '^TestControlledProcessSIGKILLThenRestartReconciles$'
  run_go ./internal/backupasset/search \
    '^TestRecoveryPointSourceLifecycleSearchRemovesSupersededGenerationPayloadOnRestart$'
fi

if [[ "$LOAD_MODE" == "million-catalog" ]]; then
  echo "backup-asset load/security: million-catalog mode reuses the 10k catalog owner; literal million is not-executed"
  run_go ./internal/backupasset/catalog \
    '^TestCatalogPaginatesTenThousandCommittedEntries$'
fi

if [[ "$LOAD_MODE" == "million-catalog-full" ]]; then
  fail "literal million-catalog generation is not checked in; run a dedicated operator harness outside this script"
fi

echo "backup-asset load/security: PASS mode=${LOAD_MODE}"
echo "backup-asset load/security: CI scale page=${CI_PAGE_SIZE} preview_n=${CI_CONCURRENT_PREVIEW_N} catalog_cap=${CI_CATALOG_ENTRY_CAP}"
