#!/usr/bin/env bash
# scripts/check-migration-utc-safety.sh
#
# 阻止后续 migration 引入回归 UTC 不变量的写法。
#
# 背景：migration 000050_utc_cutover 把所有历史 timestamp 列从 Asia/Shanghai (+8h)
# 平移到 UTC，配合 GORM NowFunc=time.Now().UTC() + SQLite DSN _loc=UTC + Postgres
# DSN timezone=UTC 形成端到端 UTC 不变量。任何后续 migration 若使用 SQLite
# datetime('now', 'localtime') / SQLite 旧式 CURRENT_TIMESTAMP DEFAULT（在
# _loc=UTC 之外的 legacy 连接上写入语义可疑）/ Postgres 裸 NOW()（受 session
# timezone 影响）/ Postgres `AT TIME ZONE 'Asia/Shanghai'` 等表达式，会立刻引入
# 新数据偏移，破坏不变量。
#
# 本脚本扫所有 migrations/{sqlite,postgres}/*.{up,down}.sql 文件，遇到禁用模式
# 报错并退出非零；CI 与 .githooks/pre-commit 接入后保护未来不会回退。
#
# Exclusions:
#  - 历史基线 (000001_*) 与 cutover 本身 (000050_*) 不扫，因为它们就是 setup /
#    transitional 文件，含上述模式属于"故意行为"。
#
# Usage:
#   bash scripts/check-migration-utc-safety.sh
# 退出码：0 OK；1 命中违规；2 配置错误（无目录等）。

set -euo pipefail

# 默认 ROOT_DIR = repo 根；可由调用方覆盖（self-test 用 mktemp 目录）。
ROOT_DIR="${MIGRATION_LINT_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
DIRS=(
  "$ROOT_DIR/backend/internal/database/migrations/sqlite"
  "$ROOT_DIR/backend/internal/database/migrations/postgres"
)

# 禁用模式（POSIX ERE）。每条对应一个具体的回归风险：
#  1. SQLite 的 datetime('now', 'localtime') —— 显式写本地时间，违反 UTC 不变量
#  2. SQLite 的 datetime('now','localtime')   —— 同上，去空格变体
#  3. SQLite 的裸 'localtime' 修饰符（任何 datetime/strftime 调用里）
#  4. SQLite 的 DEFAULT CURRENT_TIMESTAMP —— SQLite 文档说 CURRENT_TIMESTAMP 是
#     UTC（看似安全），但若 schema 由 GORM AutoMigrate 写入然后被 _loc=UTC 读取，
#     行为依赖驱动版本；000050 之后我们坚持 GORM NowFunc 写入，禁用 DB 端 DEFAULT
#  5. Postgres 的 AT TIME ZONE 'Asia/...' —— 任何写入端时区转换都偏离 UTC 不变量
#  6. Postgres 的 SET TIME ZONE / SET LOCAL TIME ZONE —— 切换 session timezone
PATTERNS=(
  "datetime\\([^)]*'localtime'"
  "'localtime'"
  "DEFAULT CURRENT_TIMESTAMP"
  "AT TIME ZONE 'Asia/"
  "AT TIME ZONE 'America/"
  "AT TIME ZONE 'Europe/"
  "SET TIME ZONE"
  "SET LOCAL TIME ZONE"
)

# 仅检查 cutover (000050) 之后新写的 migration。早于 000050 的历史 migration 已经
# 被 cutover 平移过，且 SQLite 的 DEFAULT CURRENT_TIMESTAMP 历史上是 UTC（与新不变量
# 巧合一致）；改写它们风险大于收益。新 migration 必须保持纪律。
#
# 阈值：版本号 < FORWARD_FROM_VERSION 的文件全部 skip。
FORWARD_FROM_VERSION=51

# 显式排除（即使版本 >= FORWARD_FROM_VERSION 也跳过）。当前为空；将来如有
# "故意做时区平移"的迁移，把它的文件名子串加进来。
EXCLUDE_GLOBS=()

hits=0
checked=0

for dir in "${DIRS[@]}"; do
  if [[ ! -d "$dir" ]]; then
    echo "[ERR] migration 目录不存在: $dir" >&2
    exit 2
  fi
  while IFS= read -r -d '' f; do
    base="$(basename "$f")"
    # 解析版本号（文件名前 6 位数字）。无法解析视为 0（保险跳过）。
    version_str="${base:0:6}"
    if [[ "$version_str" =~ ^[0-9]{6}$ ]]; then
      version=$((10#$version_str))
    else
      version=0
    fi
    if (( version < FORWARD_FROM_VERSION )); then
      continue
    fi
    skip=0
    # Bash 在 set -u 模式下展开空数组会报 unbound；用 ${arr[@]+...} 防御。
    for ex in "${EXCLUDE_GLOBS[@]+"${EXCLUDE_GLOBS[@]}"}"; do
      if [[ -n "$ex" && "$base" == *"$ex"* ]]; then
        skip=1
        break
      fi
    done
    if [[ "$skip" == 1 ]]; then
      continue
    fi
    checked=$((checked + 1))
    for pat in "${PATTERNS[@]}"; do
      # -E 扩展正则；-i 大小写不敏感；--with-filename 默认开启。
      # 输出格式：path:line:matched-content
      if matches=$(grep -niE "$pat" "$f" 2>/dev/null); then
        while IFS= read -r line; do
          [[ -z "$line" ]] && continue
          echo "[FAIL] ${f}: ${line}"
          echo "       违规模式: ${pat}"
          hits=$((hits + 1))
        done <<<"$matches"
      fi
    done
  done < <(find "$dir" -maxdepth 1 -type f \( -name "*.up.sql" -o -name "*.down.sql" \) -print0)
done

if [[ "$hits" -eq 0 ]]; then
  echo "[OK] 已扫描 ${checked} 个 migration 文件，无 UTC 不变量回归模式"
  exit 0
fi

echo ""
echo "[FAIL] 共发现 ${hits} 处违规。"
echo "       UTC 不变量见 docs/migration-utc-cutover.md。"
echo "       新 migration 应使用 GORM NowFunc 写入 timestamp（不用 SQL DEFAULT），"
echo "       禁止显式时区转换。如需在历史数据上做 timezone 平移，请仿 000050 形式"
echo "       并把文件名加入本脚本 EXCLUDE_GLOBS。"
exit 1
