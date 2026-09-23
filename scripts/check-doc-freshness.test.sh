#!/usr/bin/env bash
# 真实 Git fixture 对比 CI 提醒与 hook 阻断，确保二者调用同一规则。
set -euo pipefail
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf -- "$WORK"' EXIT
mkdir -p "$WORK/scripts" "$WORK/.githooks" "$WORK/backend/internal/database/migrations/"{sqlite,postgres} "$WORK/docs"
for script in check-doc-freshness.sh doc-freshness-rules.sh check-doc-structure.py check-migration-version.sh check-migration-utc-safety.sh check-pre-commit-fast.sh; do
  cp "$ROOT_DIR/scripts/$script" "$WORK/scripts/"
done
cp "$ROOT_DIR/.githooks/pre-commit" "$WORK/.githooks/"
printf '# 文档\n' >"$WORK/docs/README.md"
printf '当前迁移版本：`000001_initial`。\n' >"$WORK/backend/README.md"
for dialect in sqlite postgres; do
  printf '%s\n' '-- up' >"$WORK/backend/internal/database/migrations/$dialect/000001_initial.up.sql"
  printf '%s\n' '-- down' >"$WORK/backend/internal/database/migrations/$dialect/000001_initial.down.sql"
done
git -C "$WORK" init -q
git -C "$WORK" config user.name fixture
git -C "$WORK" config user.email fixture@example.invalid
git -C "$WORK" add .
git -C "$WORK" -c core.hooksPath=/dev/null commit -qm initial
git -C "$WORK" -c core.hooksPath=/dev/null commit --allow-empty -qm baseline
git -C "$WORK" update-ref refs/remotes/origin/fixture-base HEAD

run_case() {
  local label="$1" changed="$2" expected="$3" path out staged hook_output status
  # reset/clean 仅操作本测试创建的临时仓库。
  git -C "$WORK" reset --hard -q origin/fixture-base
  git -C "$WORK" clean -fdq
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    mkdir -p "$WORK/$(dirname "$path")"
    if [[ "$path" == *.go ]]; then
      printf 'package fixture\n' >"$WORK/$path"
    elif [[ "$path" == backend/README.md ]]; then
      printf '\n更新模块说明。\n' >>"$WORK/$path"
    elif [[ "$path" == *.md ]]; then
      printf '# 文档\n\n同步主题。\n' >"$WORK/$path"
      # 所有 docs 目录真实入口可达，结构门禁不干扰主题规则测试。
      local dir="$(dirname "$path")"
      while [[ "$dir" == docs/* ]]; do
        touch "$WORK/$dir/README.md"
        dir="$(dirname "$dir")"
      done
      if [[ "$path" == docs/* && "$path" != docs/README.md ]]; then
        printf '\n[主题](%s)\n' "${path#docs/}" >>"$WORK/docs/README.md"
      fi
    else
      printf 'fixture\n' >"$WORK/$path"
    fi
  done <<<"$changed"
  # 新的目录 README 也需有入口。
  while IFS= read -r path; do
    [[ "$path" != "$WORK/docs/README.md" ]] || continue
    printf '\n[入口](%s)\n' "${path#$WORK/docs/}" >>"$WORK/docs/README.md"
  done < <(find "$WORK/docs" -name README.md)
  git -C "$WORK" add .
  staged="$(git -C "$WORK" diff --cached --name-only --diff-filter=ACMR)"
  # 非法旧路径负例仍用共享规则和 hook 检测，CI 结构检查还会独立拒绝它们。
  out="$(bash -c 'source "$1"; doc_freshness_check "$2"' _ "$WORK/scripts/doc-freshness-rules.sh" "$staged")"
  status=0
  hook_output="$(cd "$WORK" && bash .githooks/pre-commit 2>&1)" || status=$?
  if [[ "$expected" == warn ]]; then
    grep -q '⚠️' <<<"$out" && [[ "$status" == 1 ]] || { echo "FAIL[$label] $out $hook_output"; exit 1; }
  else
    ! grep -q '⚠️' <<<"$out" && [[ "$status" == 0 ]] || { echo "FAIL[$label] $out $hook_output"; exit 1; }
  fi
  # 对可组成完整文档树的候选实际跑 CI 入口，并比较相同主题提醒。
  if [[ "$label" != *old* ]]; then
    git -C "$WORK" -c core.hooksPath=/dev/null commit -qm "$label"
    local ci_output ci_base
    # 显式覆盖宿主 PR 环境；分别验证本地回退和 fixture 自己的远端基线。
    for ci_base in '' fixture-base; do
      ci_output="$(cd "$WORK" && GITHUB_BASE_REF="$ci_base" bash scripts/check-doc-freshness.sh)"
      if [[ "$expected" == warn ]]; then
        grep -q '⚠️' <<<"$ci_output" || { echo "FAIL[$label/$ci_base] CI 提醒缺失"; exit 1; }
      else
        ! grep -q '⚠️' <<<"$ci_output" || { echo "FAIL[$label/$ci_base] CI 提醒漂移"; exit 1; }
      fi
    done
  fi
  echo "OK[$label]"
}
run_case config-missing $'backend/internal/config/config.go' warn
run_case config-env $'backend/internal/config/config.go\ndocs/env-vars.md' clean
run_case config-unrelated $'backend/internal/config/config.go\ndocs/admin/README.md' warn
run_case model-new $'backend/internal/model/models.go\ndocs/spec/backend/database-guidelines.md' clean
run_case model-old $'backend/internal/model/models.go\nspec/backend/database-guidelines.md' warn
run_case model-unrelated $'backend/internal/model/models.go\ndocs/spec/frontend/type-safety.md' warn
run_case api-new $'backend/internal/api/router.go\ndocs/spec/backend/directory-structure.md' clean
run_case api-unrelated-domain $'backend/internal/api/router.go\ndocs/spec/domains/node-log-collection.md' warn
run_case model-unrelated-domain $'backend/internal/model/models.go\ndocs/spec/domains/node-log-collection.md' warn
run_case model-domain-index $'backend/internal/model/models.go\ndocs/spec/domains/README.md' warn
run_case api-old $'backend/internal/api/router.go\nbackend/README_backend.md' warn
run_case route-new $'web/src/router.tsx\ndocs/spec/frontend/directory-structure.md' clean
run_case route-docs-child $'web/src/router.tsx\ndocs/admin/README.md' clean
run_case route-unrelated $'web/src/router.tsx\ndocs/env-vars.md' warn
run_case migration-new $'backend/internal/database/migrations/sqlite/000001_initial.up.sql\nbackend/README.md' clean
run_case migration-old $'backend/internal/database/migrations/sqlite/000001_initial.up.sql\nbackend/README_backend.md' warn
run_case release-new $'.github/workflows/publish-images.yml\ndocs/maintainers/release.md' clean
run_case release-unrelated $'.github/workflows/publish-images.yml\ndocs/env-vars.md' warn
run_case settings-missing $'backend/internal/settings/service.go' warn
run_case settings-synced $'backend/internal/settings/service.go\ndocs/env-vars.md' clean
run_case dockerhub-missing $'.github/workflows/dockerhub-description.yml' warn
run_case dockerhub-synced $'.github/workflows/dockerhub-description.yml\ndocs/maintainers/release.md' clean
run_case release-verifier-missing $'scripts/verify-release-ci.mjs' warn
run_case release-verifier-synced $'scripts/verify-release-ci.mjs\ndocs/maintainers/release.md' clean
run_case unrelated $'docs/env-vars.md' clean
git -C "$WORK" reset --hard -q origin/fixture-base
git -C "$WORK" clean -fdq
printf '# 环境变量\n' >"$WORK/docs/env-vars.md"
git -C "$WORK" add .
git -C "$WORK" -c core.hooksPath=/dev/null commit -qm 'seed config documentation'
rm "$WORK/docs/env-vars.md"
mkdir -p "$WORK/backend/internal/config"
printf 'package config\n' >"$WORK/backend/internal/config/config.go"
git -C "$WORK" add -A
deleted_status=0
(cd "$WORK" && bash .githooks/pre-commit >/dev/null 2>&1) || deleted_status=$?
[[ "$deleted_status" == 1 ]] || { echo 'FAIL: 删除对应文档不应算同步'; exit 1; }
echo 'OK[deleted-document-is-not-sync]'
echo 'doc freshness self-test: PASS'
