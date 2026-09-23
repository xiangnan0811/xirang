#!/usr/bin/env bash
# hook 与 CI 共用的主题规则。触发包含删除；文档同步只认 ACMR。

doc_freshness_check() {
  local changed="$1" updated="${2-$1}" path model_navigation=0 router_navigation=0
  DOC_FRESHNESS_WARNINGS=0

  # Test-only changes do not identify production-domain contract edits.
  changed="$(printf '%s\n' "$changed" | grep -Ev '(^|/)[^/]*_test\.go$' || true)"

  doc_rule() {
    local trigger="$1" document="$2" message="$3"
    if grep -qE "$trigger" <<<"$changed" && ! grep -qE "$document" <<<"$updated"; then
      echo "⚠️  $message"
      DOC_FRESHNESS_WARNINGS=$((DOC_FRESHNESS_WARNINGS + 1))
    fi
  }

  doc_rule '^backend/internal/model/(user|audit|token_revocation)\.go$' \
    '^docs/spec/domains/credentials-access\.md$' \
    '凭据模型已修改，请同步凭据与访问领域合同'
  doc_rule '^backend/internal/model/node\.go$' \
    '^docs/spec/domains/(credentials-access|node-log-collection|alerting-health)\.md$' \
    'Node 模型已修改，请同步凭据、日志或健康领域中对应的合同'
  doc_rule '^backend/internal/model/(task|task_occurrence|task_resource|task_terminal_effect|backup_completion)\.go$' \
    '^docs/spec/domains/task-execution-recovery\.md$' \
    '任务执行模型已修改，请同步任务执行与恢复领域合同'
  doc_rule '^backend/internal/model/alert\.go$' \
    '^docs/spec/domains/alerting-health\.md$' \
    '告警模型已修改，请同步告警与健康领域合同'
  doc_rule '^backend/internal/model/monitor\.go$' \
    '^docs/spec/domains/(alerting-health|credentials-access)\.md$' \
    '监控模型已修改，请同步健康或凭据领域中对应的合同'
  doc_rule '^backend/internal/model/integration\.go$' \
    '^docs/spec/domains/(credentials-access|alerting-health)\.md$' \
    '集成模型已修改，请同步凭据或健康领域中对应的合同'

  doc_rule '^backend/internal/database/(database|migrator)\.go$' \
    '^docs/spec/backend/database-guidelines\.md$' \
    '通用数据库实现已修改，请核对数据库合同并同步 database-guidelines'
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

  while IFS= read -r path; do
    case "$path" in
      backend/internal/model/*.go)
        case "${path##*/}" in
          user.go|audit.go|token_revocation.go|node.go|task.go|task_occurrence.go|task_resource.go|task_terminal_effect.go|backup_completion.go|alert.go|monitor.go|integration.go)
            ;;
          *)
            model_navigation=1
            ;;
        esac
        ;;
      backend/internal/api/router.go)
        router_navigation=1
        ;;
    esac
  done <<<"$changed"

  if (( model_navigation )); then
    echo "[INFO] 模型路径不足以判断领域语义；请从 docs/spec/domains/README.md 按实际变更核对主文。"
  fi
  if (( router_navigation )); then
    echo "[INFO] API 路由变更需按实际路由合同核对领域主文；仅路由组织或认证边界通用规则改变时同步 docs/spec/backend/directory-structure.md。"
  fi
}
