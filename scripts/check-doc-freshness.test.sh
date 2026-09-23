#!/usr/bin/env bash
# 真实 Git fixture 对比 CI 提醒与 hook 阻断，确保二者调用同一规则。
set -euo pipefail
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GITHUB_BASE_REF \
  MIGRATION_FRESHNESS_ROOT MIGRATION_FRESHNESS_README MIGRATION_LINT_ROOT
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
WORK="$(mktemp -d)"
STATE="$(mktemp -d)"
trap 'rm -rf -- "$WORK" "$STATE"' EXIT
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

FAILURES=0

fail_test() {
  echo "FAIL[$1] $2"
  FAILURES=$((FAILURES + 1))
}

run_case() {
  local label="$1" changed="$2" expected="$3" path out staged hook_output status hook_ok=1
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
  case "$expected" in
    warn)
      if ! grep -q '⚠️' <<<"$out" || [[ "$status" != 1 ]]; then
        fail_test "$label" "expected blocking hook warning, got status=$status; $out $hook_output"
        hook_ok=0
      fi
      ;;
    clean)
      if grep -q '⚠️' <<<"$out" || [[ "$status" != 0 ]]; then
        fail_test "$label" "expected no blocking warning, got status=$status; $out $hook_output"
        hook_ok=0
      fi
      ;;
    info)
      if grep -q '⚠️' <<<"$out" || [[ "$status" != 0 ]] || ! grep -q '\[INFO\]' <<<"$out" || ! grep -q '\[INFO\]' <<<"$hook_output"; then
        fail_test "$label" "expected nonblocking navigation info in rule and hook, got status=$status; $out $hook_output"
        hook_ok=0
      fi
      ;;
    *)
      fail_test "$label" "unknown expected result: $expected"
      hook_ok=0
      ;;
  esac
  # 对可组成完整文档树的候选实际跑 CI 入口，并比较相同主题提醒。
  if [[ "$label" != *old* ]]; then
    git -C "$WORK" -c core.hooksPath=/dev/null commit -qm "$label"
    local ci_output ci_base ci_status
    # 显式覆盖宿主 PR 环境；分别验证本地回退和 fixture 自己的远端基线。
    for ci_base in '' fixture-base; do
      ci_status=0
      ci_output="$(cd "$WORK" && GITHUB_BASE_REF="$ci_base" bash scripts/check-doc-freshness.sh 2>&1)" || ci_status=$?
      if [[ "$ci_status" != 0 ]]; then
        fail_test "$label/$ci_base" "CI entrypoint exited $ci_status: $ci_output"
      elif [[ "$expected" == warn ]] && ! grep -q '⚠️' <<<"$ci_output"; then
        fail_test "$label/$ci_base" "CI blocking-theme reminder missing: $ci_output"
      elif [[ "$expected" != warn ]] && grep -q '⚠️' <<<"$ci_output"; then
        fail_test "$label/$ci_base" "unexpected blocking-theme reminder: $ci_output"
      elif [[ "$expected" == info ]] && ! grep -q '\[INFO\]' <<<"$ci_output"; then
        fail_test "$label/$ci_base" "CI navigation reminder missing: $ci_output"
      fi
    done
  fi
  if (( hook_ok )); then
    echo "OK[$label]"
  fi
}

run_case config-missing $'backend/internal/config/config.go' warn
run_case config-env $'backend/internal/config/config.go\ndocs/env-vars.md' clean
run_case config-unrelated $'backend/internal/config/config.go\ndocs/admin/README.md' warn
run_case model-navigation $'backend/internal/model/models.go' info
run_case model-test-only $'backend/internal/model/models_test.go' clean
run_case other-tests-only $'backend/internal/model/node_test.go\nbackend/internal/api/health_test.go' clean
run_case node-credentials $'backend/internal/model/node.go\ndocs/spec/domains/credentials-access.md' clean
run_case node-backup-catalog $'backend/internal/model/node.go\ndocs/spec/domains/backup-catalog.md' warn
run_case node-database-guidelines $'backend/internal/model/node.go\ndocs/spec/backend/database-guidelines.md' warn
run_case user-credentials $'backend/internal/model/user.go\ndocs/spec/domains/credentials-access.md' clean
run_case user-alerting $'backend/internal/model/user.go\ndocs/spec/domains/alerting-health.md' warn
run_case task-execution $'backend/internal/model/task.go\ndocs/spec/domains/task-execution-recovery.md' clean
run_case database-contract $'backend/internal/database/database.go\ndocs/spec/backend/database-guidelines.md' clean
run_case database-unrelated-domain $'backend/internal/database/database.go\ndocs/spec/domains/alerting-health.md' warn
run_case router-navigation $'backend/internal/api/router.go' info
run_case router-navigation-with-guide $'backend/internal/api/router.go\ndocs/spec/backend/directory-structure.md' info
run_case model-domain-index $'backend/internal/model/models.go\ndocs/spec/domains/README.md' info
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

reset_fixture() {
  git -C "$WORK" reset --hard -q origin/fixture-base
  git -C "$WORK" clean -fdq
}

create_migration_candidate() {
  local dialect
  printf '当前迁移版本：`000051_feature`。\n' >"$WORK/backend/README.md"
  for dialect in sqlite postgres; do
    printf '%s\n' '-- up' >"$WORK/backend/internal/database/migrations/$dialect/000051_feature.up.sql"
    printf '%s\n' '-- down' >"$WORK/backend/internal/database/migrations/$dialect/000051_feature.down.sql"
  done
}

stage_migration_candidate() {
  git -C "$WORK" add -- backend/README.md \
    backend/internal/database/migrations/sqlite/000051_feature.up.sql \
    backend/internal/database/migrations/sqlite/000051_feature.down.sql \
    backend/internal/database/migrations/postgres/000051_feature.up.sql \
    backend/internal/database/migrations/postgres/000051_feature.down.sql
}

assert_hook() {
  local label="$1" expected="$2" fragment="${3:-}" status=0 output
  output="$(cd "$WORK" && bash .githooks/pre-commit 2>&1)" || status=$?
  if [[ "$expected" == pass ]]; then
    if [[ "$status" != 0 ]]; then
      fail_test "$label" "expected hook pass, got status=$status: $output"
      return
    fi
  elif [[ "$status" == 0 ]]; then
    fail_test "$label" "expected hook rejection, got success: $output"
    return
  fi
  if [[ -n "$fragment" ]] && ! grep -Fq -- "$fragment" <<<"$output"; then
    fail_test "$label" "expected rejection detail '$fragment': $output"
    return
  fi
  echo "OK[$label]"
}

assert_script_rejects() {
  local label="$1" script="$2" fragment="${3:-}" status=0 output
  output="$(cd "$WORK" && GITHUB_BASE_REF='' bash "$script" 2>&1)" || status=$?
  if [[ "$status" == 0 ]]; then
    fail_test "$label" "expected workspace checker rejection, got success: $output"
  elif [[ -n "$fragment" ]] && ! grep -Fq -- "$fragment" <<<"$output"; then
    fail_test "$label" "expected workspace rejection detail '$fragment': $output"
  else
    echo "OK[$label]"
  fi
}

capture_worktree() {
  local snapshot="$1"
  find "$WORK" -path "$WORK/.git" -prune -o -type f -print0 |
    sort -z | xargs -0 sha256sum >"$snapshot"
}

reset_fixture
create_migration_candidate
git -C "$WORK" add -- backend/README.md \
  backend/internal/database/migrations/sqlite/000051_feature.up.sql \
  backend/internal/database/migrations/sqlite/000051_feature.down.sql
# PostgreSQL's matching migrations exist only in the worktree, not the index.
assert_hook r1-staged-sqlite-with-worktree-postgres-rejected reject 'differs from PostgreSQL'

reset_fixture
create_migration_candidate
stage_migration_candidate
git -C "$WORK" ls-files --stage -z >"$STATE/index.before"
capture_worktree "$STATE/worktree.before"
assert_hook r1-complete-staged-candidate-passes pass
git -C "$WORK" ls-files --stage -z >"$STATE/index.after"
capture_worktree "$STATE/worktree.after"
if ! cmp -s "$STATE/index.before" "$STATE/index.after"; then
  fail_test r1-index-unchanged 'pre-commit changed the fixture index entries or blobs'
else
  echo 'OK[r1-index-unchanged]'
fi
if ! cmp -s "$STATE/worktree.before" "$STATE/worktree.after"; then
  fail_test r1-worktree-unchanged 'pre-commit changed fixture worktree contents'
else
  echo 'OK[r1-worktree-unchanged]'
fi

reset_fixture
create_migration_candidate
printf '当前迁移版本：`000001_initial`。\n更新模块说明。\n' >"$WORK/backend/README.md"
git -C "$WORK" add -- backend/README.md
printf '当前迁移版本：`000051_feature`。\n更新模块说明。\n' >"$WORK/backend/README.md"
git -C "$WORK" add -- \
  backend/internal/database/migrations/sqlite/000051_feature.up.sql \
  backend/internal/database/migrations/sqlite/000051_feature.down.sql \
  backend/internal/database/migrations/postgres/000051_feature.up.sql \
  backend/internal/database/migrations/postgres/000051_feature.down.sql
MIGRATION_FRESHNESS_ROOT="$WORK" MIGRATION_FRESHNESS_README="$WORK/backend/README.md" \
  assert_hook r1-unstaged-readme-version-rejected reject 'documents 000001_initial, source migrations require 000051_feature'

reset_fixture
create_migration_candidate
stage_migration_candidate
printf '当前迁移版本：`999999_workspace_only`。\n' >"$WORK/backend/README.md"
assert_hook r1-worktree-readme-does-not-change-candidate pass
assert_script_rejects r1-ordinary-version-check-reads-worktree scripts/check-doc-freshness.sh 'documents 999999_workspace_only'

reset_fixture
create_migration_candidate
stage_migration_candidate
rm "$WORK/backend/README.md"
assert_hook r1-missing-worktree-readme-does-not-change-candidate pass
assert_script_rejects r1-ordinary-missing-readme-rejected scripts/check-doc-freshness.sh 'backend README is missing'

reset_fixture
create_migration_candidate
stage_migration_candidate
rm "$WORK/backend/internal/database/migrations/sqlite/000051_feature.down.sql"
assert_hook r1-missing-worktree-migration-does-not-change-candidate pass
assert_script_rejects r1-ordinary-missing-migration-rejected scripts/check-doc-freshness.sh 'paired migration file is missing'

reset_fixture
create_migration_candidate
stage_migration_candidate
printf '%s\n' "CREATE TABLE broken (created_at TEXT DEFAULT CURRENT_TIMESTAMP);" >"$WORK/backend/internal/database/migrations/sqlite/000051_feature.up.sql"
assert_hook r1-worktree-utc-violation-does-not-change-candidate pass
assert_script_rejects r1-ordinary-utc-check-reads-worktree scripts/check-migration-utc-safety.sh 'DEFAULT CURRENT_TIMESTAMP'

reset_fixture
create_migration_candidate
stage_migration_candidate
printf '%s\n' "CREATE TABLE broken (created_at TEXT DEFAULT CURRENT_TIMESTAMP);" >"$WORK/backend/internal/database/migrations/sqlite/000051_feature.up.sql"
git -C "$WORK" add -- backend/internal/database/migrations/sqlite/000051_feature.up.sql
printf '%s\n' '-- up' >"$WORK/backend/internal/database/migrations/sqlite/000051_feature.up.sql"
MIGRATION_LINT_ROOT="$WORK" assert_hook r1-index-utc-violation-rejected reject 'DEFAULT CURRENT_TIMESTAMP'

reset_fixture
create_migration_candidate
stage_migration_candidate
git -C "$WORK" rm --cached -- backend/internal/database/migrations/sqlite/000051_feature.down.sql >/dev/null
assert_hook r1-staged-delete-ignores-restored-worktree-rejected reject 'paired migration file is missing'

reset_fixture
create_migration_candidate
stage_migration_candidate
git -C "$WORK" -c core.hooksPath=/dev/null commit -qm "seed complete highest migration"
git -C "$WORK" rm -f -- \
  backend/internal/database/migrations/sqlite/000051_feature.up.sql \
  backend/internal/database/migrations/sqlite/000051_feature.down.sql \
  backend/internal/database/migrations/postgres/000051_feature.up.sql \
  backend/internal/database/migrations/postgres/000051_feature.down.sql >/dev/null
printf '当前迁移版本：`000001_initial`。\n' >"$WORK/backend/README.md"
git -C "$WORK" add -- backend/README.md
staged_deletions="$(git -C "$WORK" diff --cached --name-only --diff-filter=D)"
expected_deletions="$(printf "%s\n" backend/internal/database/migrations/{postgres,sqlite}/000051_feature.{down,up}.sql)"
[[ "$staged_deletions" == "$expected_deletions" ]] || fail_test r1-highest-removal-candidate "expected four staged migration deletions"
[[ "$(git -C "$WORK" diff --cached --name-only --diff-filter=M)" == backend/README.md ]] || fail_test r1-highest-removal-candidate "expected staged README rollback"
assert_hook r1-complete-highest-version-removal-passes pass "000001_initial"

reset_fixture
create_migration_candidate
stage_migration_candidate
git -C "$WORK" rm -f -- backend/README.md >/dev/null
assert_hook r1-staged-readme-deletion-rejected reject '请同步 backend/README.md'

reset_fixture
create_migration_candidate
stage_migration_candidate
conflict_path=backend/internal/database/migrations/sqlite/000051_feature.up.sql
git -C "$WORK" update-index --force-remove -- "$conflict_path"
for conflict_stage in 1 2 3; do
  conflict_blob="$(printf '%s\n' "-- conflict stage $conflict_stage" | git -C "$WORK" hash-object -w --stdin)"
  printf '100644 %s %s\t%s\n' "$conflict_blob" "$conflict_stage" "$conflict_path" |
    git -C "$WORK" update-index --index-info
done
assert_hook r1-unmerged-index-entry-rejected reject "$conflict_path"

reset_fixture
create_migration_candidate
stage_migration_candidate
symlink_path=backend/internal/database/migrations/sqlite/000051_feature.up.sql
rm "$WORK/$symlink_path"
ln -s 000051_feature.down.sql "$WORK/$symlink_path"
git -C "$WORK" add -- "$symlink_path"
rm "$WORK/$symlink_path"
printf '%s\n' '-- up' >"$WORK/$symlink_path"
assert_hook r1-symlink-index-entry-rejected reject "$symlink_path"

reset_fixture
printf '# 环境变量\n' >"$WORK/docs/env-vars.md"
git -C "$WORK" add .
git -C "$WORK" -c core.hooksPath=/dev/null commit -qm 'seed config documentation'
rm "$WORK/docs/env-vars.md"
mkdir -p "$WORK/backend/internal/config"
printf 'package config\n' >"$WORK/backend/internal/config/config.go"
git -C "$WORK" add -A
deleted_status=0
(cd "$WORK" && bash .githooks/pre-commit >/dev/null 2>&1) || deleted_status=$?
if [[ "$deleted_status" != 1 ]]; then
  fail_test deleted-document-is-not-sync "expected hook status 1, got $deleted_status"
else
  echo 'OK[deleted-document-is-not-sync]'
fi

if (( FAILURES > 0 )); then
  echo "doc freshness self-test: FAIL ($FAILURES failing checks)"
  exit 1
fi
echo 'doc freshness self-test: PASS'
