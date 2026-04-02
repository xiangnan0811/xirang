#!/usr/bin/env bash
# check-doc-freshness.sh — CI 文档新鲜度检查
# 当代码关键文件被修改但对应文档未同步更新时输出警告。
# 仅在 PR / push diff 中检查；非阻断（exit 0）。

set -euo pipefail

WARN=0

# 获取本次变更的文件列表
if [ -n "${GITHUB_BASE_REF:-}" ]; then
  # PR：对比 base branch
  CHANGED=$(git diff --name-only "origin/${GITHUB_BASE_REF}...HEAD" 2>/dev/null || true)
elif git rev-parse HEAD~1 >/dev/null 2>&1; then
  # push：对比上一个 commit
  CHANGED=$(git diff --name-only HEAD~1 2>/dev/null || true)
else
  echo "ℹ️  无法获取变更文件列表，跳过文档新鲜度检查"
  exit 0
fi

if [ -z "$CHANGED" ]; then
  echo "✅ 无文件变更，跳过文档新鲜度检查"
  exit 0
fi

warn() {
  echo "⚠️  $1"
  WARN=$((WARN + 1))
}

# 规则 1：模型变更 → CLAUDE.md
if echo "$CHANGED" | grep -q "backend/internal/model/models.go"; then
  if ! echo "$CHANGED" | grep -q "CLAUDE.md"; then
    warn "backend/internal/model/models.go 已修改，但 CLAUDE.md 未同步更新"
  fi
fi

# 规则 2：API 路由变更 → backend/README_backend.md
if echo "$CHANGED" | grep -q "backend/internal/api/router.go"; then
  if ! echo "$CHANGED" | grep -q "backend/README_backend.md"; then
    warn "backend/internal/api/router.go 已修改，但 backend/README_backend.md 未同步更新"
  fi
fi

# 规则 3：前端路由变更 → CLAUDE.md
if echo "$CHANGED" | grep -q "web/src/router.tsx"; then
  if ! echo "$CHANGED" | grep -q "CLAUDE.md"; then
    warn "web/src/router.tsx 已修改，但 CLAUDE.md 未同步更新"
  fi
fi

# 规则 4：新增迁移文件 → CLAUDE.md
if echo "$CHANGED" | grep -q "backend/internal/database/migrations/"; then
  if ! echo "$CHANGED" | grep -q "CLAUDE.md"; then
    warn "数据库迁移文件有变更，但 CLAUDE.md 未同步更新迁移版本"
  fi
fi

# 规则 5：配置变更 → docs/env-vars.md
if echo "$CHANGED" | grep -q "backend/internal/config/config.go"; then
  if ! echo "$CHANGED" | grep -q "docs/env-vars.md"; then
    warn "backend/internal/config/config.go 已修改，但 docs/env-vars.md 未同步更新"
  fi
fi

# 规则 6：发布/镜像/部署/版本检查变更 → 发布文档
if echo "$CHANGED" | grep -qE '^(\.github/workflows/(release-please|publish-images|deploy)\.yml|docker-compose\.prod\.yml|\.env\.deploy|backend/\.env\.production\.example|backend/internal/api/handlers/version_handler\.go|CHANGELOG\.md)$'; then
  if ! echo "$CHANGED" | grep -qE '^(README\.md|CONTRIBUTING\.md|docs/deployment\.md|docs/env-vars\.md|docs/release-maintainers\.md|AGENTS\.md|\.github/PULL_REQUEST_TEMPLATE\.md)$'; then
    warn "发布/镜像/部署/版本检查相关文件已修改，但配套文档或仓库规范未同步更新"
  fi
fi

if [ "$WARN" -gt 0 ]; then
  echo ""
  echo "📝 共 ${WARN} 条文档同步提醒。请确认是否需要更新对应文档。"
else
  echo "✅ 文档新鲜度检查通过"
fi

# 不阻断 CI
exit 0
