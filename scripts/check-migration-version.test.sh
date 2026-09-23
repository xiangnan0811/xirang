#!/usr/bin/env bash
# 验证真实默认 README 路径及双引擎迁移配对。
set -euo pipefail
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/check-migration-version.sh"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
SQLITE_DIR="$WORK/backend/internal/database/migrations/sqlite"
POSTGRES_DIR="$WORK/backend/internal/database/migrations/postgres"
README="$WORK/backend/README.md"
fixture() {
  rm -rf "$WORK/backend"
  mkdir -p "$SQLITE_DIR" "$POSTGRES_DIR"
  for dialect_dir in "$SQLITE_DIR" "$POSTGRES_DIR"; do
    printf '%s\n' '-- up' >"$dialect_dir/000077_lifecycle_effect_claim_audit_slot.up.sql"
    printf '%s\n' '-- down' >"$dialect_dir/000077_lifecycle_effect_claim_audit_slot.down.sql"
  done
  printf '%s\n' '当前迁移版本：`000077_lifecycle_effect_claim_audit_slot`。' >"$README"
}
run_checker() {
  env -u MIGRATION_FRESHNESS_README MIGRATION_FRESHNESS_ROOT="$WORK" bash "$SCRIPT"
}
reject() {
  if run_checker >/dev/null 2>&1; then echo "FAIL: $1" >&2; exit 1; fi
  echo "OK: $1"
}
fixture
run_checker >/dev/null
echo 'OK: 默认新 README 路径'
rm "$README"
reject '缺少新 README'
printf '%s\n' '当前迁移版本：`000077_lifecycle_effect_claim_audit_slot`。' >"$WORK/backend/README_backend.md"
reject '仅旧 README 存在'
fixture
printf '%s\n' '当前迁移版本：`000076_provider_native_version_reference_reason`。' >"$README"
reject '文档版本不符'
fixture
printf '%s\n' '-- up' >"$POSTGRES_DIR/000078_next.up.sql"
printf '%s\n' '-- down' >"$POSTGRES_DIR/000078_next.down.sql"
reject '双引擎版本不一致'
fixture
rm "$POSTGRES_DIR/000077_lifecycle_effect_claim_audit_slot.down.sql"
reject '缺少 down'
fixture
: >"$SQLITE_DIR/000077_lifecycle_effect_claim_audit_slot.down.sql"
reject '空 down'
fixture
for dialect_dir in "$SQLITE_DIR" "$POSTGRES_DIR"; do
  for suffix in a b; do
    printf '%s\n' '-- up' >"$dialect_dir/000078_duplicate_${suffix}.up.sql"
    printf '%s\n' '-- down' >"$dialect_dir/000078_duplicate_${suffix}.down.sql"
  done
done
reject '同版本不同名称'
echo 'migration freshness checker self-test: PASS'
