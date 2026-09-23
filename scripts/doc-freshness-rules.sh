#!/usr/bin/env bash
# hook 与 CI 共用的主题规则。触发包含删除；文档同步只认 ACMR。

doc_freshness_check() {
  local changed="$1" updated="${2-$1}"
  DOC_FRESHNESS_WARNINGS=0
  doc_rule() {
    local trigger="$1" document="$2" message="$3"
    if grep -qE "$trigger" <<<"$changed" && ! grep -qE "$document" <<<"$updated"; then
      echo "⚠️  $message"
      DOC_FRESHNESS_WARNINGS=$((DOC_FRESHNESS_WARNINGS + 1))
    fi
  }
  doc_rule '^backend/internal/model/[^/]+\.go$' \
    '^docs/spec/backend/database-guidelines\.md$' \
    '集中模型已修改，请同步数据库合同及其领域导航'
  doc_rule '^backend/internal/api/router\.go$' \
    '^docs/spec/backend/directory-structure\.md$' \
    '集中 API 路由已修改，请同步后端路由组织及其领域导航'
  doc_rule '^web/src/router(-pages)?\.tsx$' \
    '^(README\.md|docs/admin/README\.md|docs/spec/frontend/directory-structure\.md)$' \
    '前端路由已修改，请同步公开入口或前端目录合同'
  doc_rule '^backend/internal/database/migrations/.*\.sql$' '^backend/README\.md$' \
    '数据库迁移已修改，请同步 backend/README.md 的迁移版本'
  doc_rule '^(backend/internal/(config/config|settings/service)\.go|backend/\.env[^/]*|\.env\.deploy|deploy/allinone/(entrypoint\.sh|Dockerfile)|web/src/lib/env\.ts|web/vite\.config\.ts)$' \
    '^docs/env-vars\.md$' '配置入口已修改，请同步环境变量参考'
  doc_rule '^(\.github/workflows/(release-please|publish-images|dockerhub-description)\.yml|scripts/verify-release-ci\.mjs|backend/internal/api/handlers/version_handler\.go|release-please-config\.json|\.release-please-manifest\.json|CHANGELOG\.md)$' \
    '^docs/maintainers/release\.md$' '发布或镜像流程已修改，请同步维护者发布手册'
  doc_rule '^(docker-compose[^/]*\.yml|\.github/workflows/deploy\.yml|deploy/(allinone|nginx)/.*)$' \
    '^(docs/deployment\.md|docs/spec/backend/deployment-runtime\.md)$' \
    '部署入口已修改，请同步部署指南或运行时合同'
  doc_rule '^(\.github/(dependabot\.yml|workflows/ci\.yml)|web/package(-lock)?\.json|backend/go\.(mod|sum))$' \
    '^docs/maintainers/automation\.md$' '依赖或 CI 已修改，请同步仓库自动化说明'
  doc_rule '^(AGENTS\.md|CLAUDE\.md|\.omp/AGENTS\.md|\.codex/.*|backend/internal/api/handlers/AGENTS\.md|web/src/pages/AGENTS\.md)$' \
    '^docs/spec/guides/agent-collaboration\.md$' '代理入口已修改，请同步代理协作与验证合同'
  doc_rule '^(scripts/(check-doc-[^/]+|doc-freshness-rules\.sh)|\.githooks/pre-commit)$' \
    '^docs/spec/guides/documentation-truth-guide\.md$' '文档门禁已修改，请同步文档维护合同'
}
