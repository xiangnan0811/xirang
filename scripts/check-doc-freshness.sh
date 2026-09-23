#!/usr/bin/env bash
# 主题同步为 CI 提醒、hook 阻断；结构和迁移始终阻断。
set -euo pipefail
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
source "$ROOT_DIR/scripts/doc-freshness-rules.sh"

if [[ "${1:-}" == "--staged" ]]; then
  CHANGED="$(git -C "$ROOT_DIR" diff --cached --name-only)"
  UPDATED="$(git -C "$ROOT_DIR" diff --cached --name-only --diff-filter=ACMR)"
else
  bash "$ROOT_DIR/scripts/check-migration-version.sh"
  python3 "$ROOT_DIR/scripts/check-doc-structure.py"
  if [[ -n "${GITHUB_BASE_REF:-}" ]]; then
    CHANGED="$(git -C "$ROOT_DIR" diff --name-only "origin/${GITHUB_BASE_REF}...HEAD")"
    UPDATED="$(git -C "$ROOT_DIR" diff --name-only --diff-filter=ACMR "origin/${GITHUB_BASE_REF}...HEAD")"
  elif git -C "$ROOT_DIR" rev-parse HEAD~1 >/dev/null 2>&1; then
    CHANGED="$(git -C "$ROOT_DIR" diff --name-only HEAD~1)"
    UPDATED="$(git -C "$ROOT_DIR" diff --name-only --diff-filter=ACMR HEAD~1)"
  else
    CHANGED=""
    UPDATED=""
  fi
fi

doc_freshness_check "$CHANGED" "$UPDATED"
if (( DOC_FRESHNESS_WARNINGS > 0 )); then
  echo "文档同步提醒：${DOC_FRESHNESS_WARNINGS} 项，请核对对应主题。"
  [[ "${1:-}" != "--staged" ]] || exit 1
else
  echo "✅ 文档新鲜度检查通过"
fi
