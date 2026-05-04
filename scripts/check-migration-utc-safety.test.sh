#!/usr/bin/env bash
# Self-test for scripts/check-migration-utc-safety.sh
#
# 验证：
#  - 含禁用模式的合成 migration 触发 exit=1
#  - 干净 migration 通过 exit=0
#  - 阈值之前的历史 migration 即使含禁用模式也跳过
#
# 该脚本不依赖任何外部数据库；纯 fixture 驱动。

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LINT_SCRIPT="${ROOT_DIR}/scripts/check-migration-utc-safety.sh"

if [[ ! -x "$LINT_SCRIPT" ]]; then
  echo "FAIL: lint script 不存在或不可执行: $LINT_SCRIPT" >&2
  exit 2
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Fixture 目录布局必须匹配 lint script 的 DIRS 构造：
#   $ROOT_DIR/backend/internal/database/migrations/{sqlite,postgres}
mkdir -p "$WORK/backend/internal/database/migrations/sqlite"
mkdir -p "$WORK/backend/internal/database/migrations/postgres"

SQLITE_DIR="$WORK/backend/internal/database/migrations/sqlite"
PG_DIR="$WORK/backend/internal/database/migrations/postgres"

fail_count=0
expect_exit() {
  local label="$1"; local want="$2"; shift 2
  set +e
  MIGRATION_LINT_ROOT="$WORK" bash "$LINT_SCRIPT" >/dev/null 2>&1
  local got=$?
  set -e
  if [[ "$got" != "$want" ]]; then
    echo "FAIL[$label]: expected exit=$want, got=$got" >&2
    fail_count=$((fail_count + 1))
  else
    echo "OK[$label]: exit=$got"
  fi
}

# --- Case 1: 干净 + 高于阈值 → 通过 ---
cat > "$SQLITE_DIR/000099_clean.up.sql" <<'EOF'
CREATE TABLE foo (id INTEGER PRIMARY KEY, name TEXT);
EOF
cat > "$SQLITE_DIR/000099_clean.down.sql" <<'EOF'
DROP TABLE foo;
EOF
expect_exit "clean-above-threshold" 0

# --- Case 2: DEFAULT CURRENT_TIMESTAMP 高于阈值 → 失败 ---
cat > "$SQLITE_DIR/000098_bad_default.up.sql" <<'EOF'
CREATE TABLE bar (id INTEGER, ts DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
EOF
cat > "$SQLITE_DIR/000098_bad_default.down.sql" <<'EOF'
DROP TABLE bar;
EOF
expect_exit "default-current-timestamp-above" 1

# --- Case 3: SQLite localtime 高于阈值 → 失败 ---
cat > "$SQLITE_DIR/000097_bad_localtime.up.sql" <<'EOF'
INSERT INTO baz (created_at) VALUES (datetime('now', 'localtime'));
EOF
cat > "$SQLITE_DIR/000097_bad_localtime.down.sql" <<'EOF'
DELETE FROM baz;
EOF
expect_exit "sqlite-localtime-above" 1

# 清理 case 2/3，准备 case 4
rm "$SQLITE_DIR/000098_bad_default.up.sql" "$SQLITE_DIR/000098_bad_default.down.sql"
rm "$SQLITE_DIR/000097_bad_localtime.up.sql" "$SQLITE_DIR/000097_bad_localtime.down.sql"

# --- Case 4: 同样的违规但版本 < 阈值 → 跳过通过 ---
cat > "$SQLITE_DIR/000020_legacy.up.sql" <<'EOF'
CREATE TABLE legacy (id INTEGER, ts DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
EOF
cat > "$SQLITE_DIR/000020_legacy.down.sql" <<'EOF'
DROP TABLE legacy;
EOF
expect_exit "below-threshold-skipped" 0
rm "$SQLITE_DIR/000020_legacy.up.sql" "$SQLITE_DIR/000020_legacy.down.sql"

# --- Case 5: PostgreSQL AT TIME ZONE → 失败 ---
cat > "$PG_DIR/000099_bad_attz.up.sql" <<'EOF'
UPDATE foo SET ts = ts AT TIME ZONE 'Asia/Shanghai';
EOF
cat > "$PG_DIR/000099_bad_attz.down.sql" <<'EOF'
-- noop
EOF
expect_exit "postgres-at-time-zone" 1
rm "$PG_DIR/000099_bad_attz.up.sql" "$PG_DIR/000099_bad_attz.down.sql"

if [[ "$fail_count" -gt 0 ]]; then
  echo ""
  echo "FAIL: $fail_count 个用例失败"
  exit 1
fi

echo ""
echo "OK: 所有用例通过"
