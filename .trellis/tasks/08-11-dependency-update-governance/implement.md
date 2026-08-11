# Dependency Update Governance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace noisy weekly single-dependency PRs with monthly grouped minor/patch updates, keep security updates immediate, and prevent duplicate CI runs for pull-request commits.

**Architecture:** Project execution preference is persisted through the canonical Trellis Codex dispatch setting; repository automation changes are limited to `.github/dependabot.yml` and the CI event block in `.github/workflows/ci.yml`. After the configuration PR is green and merged, the migration syncs `main` in the primary worktree, closes only the pre-captured version-update PRs, uses GitHub's supported Web UI to run and verify exactly three version-update jobs, then enables security settings through GitHub APIs before verifying post-merge automation and completing Trellis archival.

**Tech Stack:** Trellis Codex dispatch configuration, Dependabot v2 configuration, GitHub Actions YAML, Ruby/Psych structural assertions, actionlint, GitHub CLI.

---

## File Map

- Modify `.github/dependabot.yml`: monthly schedules, version-update allow rules, four groups, and 1/2/1 version-PR limits.
- Modify `.github/workflows/ci.yml`: limit the `push` trigger to `main` while preserving the existing `pull_request` trigger and all jobs.
- Modify `.trellis/config.yaml`: make the existing Codex sub-agent dispatch behavior an explicit persistent project preference.
- Modify `.trellis/spec/guides/branch-workflow-guidelines.md` during Phase 3.3: preserve the durable dependency-automation contract for future maintainers.
- Update `.trellis/tasks/08-11-dependency-update-governance/*`: record validation evidence, acceptance results, PR metadata, and completion state.
- Do not modify dependency manifests, lock files, workflow action pins, Release Please configuration, or application code.

## Execution Setup: Create The Project-Local Worktree

**Files:**

- Verify unchanged: `.gitignore`
- Create ignored directory: `.worktrees/dependency-update-governance`

- [ ] **Step 1: Verify the project worktree directory is ignored**

```bash
git check-ignore -v .worktrees/
```

Expected: `.gitignore:37:.worktrees/` is reported. Stop before creating a worktree if the rule is missing.

- [ ] **Step 2: Free the governance branch in the primary worktree**

```bash
git status --short --branch
git switch main
```

Expected: the governance branch is clean before switching; the primary worktree moves to `main` without altering files.

- [ ] **Step 3: Create the isolated worktree from the existing branch**

```bash
git worktree add .worktrees/dependency-update-governance codex/chore-dependency-governance
```

Expected: `/home/murray/code/xirang/.worktrees/dependency-update-governance` checks out `codex/chore-dependency-governance`.

- [ ] **Step 4: Verify the isolated baseline**

Run from `.worktrees/dependency-update-governance`:

```bash
git status --short --branch
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-11-dependency-update-governance
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/*.yml
make check
```

Expected: clean governance branch, valid 5-entry implement/check manifests, actionlint success, and the repository baseline check passes. If the baseline fails before implementation, stop and report the exact pre-existing failure.

## Task 0: Persist The Project Sub-Agent Default

**Files:**

- Modify: `.trellis/config.yaml:129`
- Verify unchanged: `.codex/agents/trellis-implement.toml`
- Verify unchanged: `.codex/agents/trellis-check.toml`
- Verify unchanged: `.codex/agents/trellis-research.toml`
- Verify unchanged: `.codex/hooks.json`

- [ ] **Step 1: Confirm the explicit-preference assertion fails against `auto`**

```bash
ruby -ryaml -e '
config = YAML.safe_load(File.read(".trellis/config.yaml"))
abort "Codex dispatch preference must be explicit" unless config.dig("codex", "dispatch_mode") == "sub-agent"
puts "Codex sub-agent dispatch preference OK"
'
```

Expected: exit 1 with `Codex dispatch preference must be explicit`.

- [ ] **Step 2: Change only the project dispatch value**

Apply this narrow change and preserve every other config line:

```yaml
codex:
  dispatch_mode: sub-agent
```

- [ ] **Step 3: Re-run the explicit-preference assertion**

Run the exact Ruby command from Step 1.

Expected: exit 0 and `Codex sub-agent dispatch preference OK`.

- [ ] **Step 4: Verify generated Codex integration files are unchanged**

```bash
git diff --exit-code origin/main -- .codex/agents .codex/hooks.json .codex/config.toml
```

Expected: exit 0 with no output.

- [ ] **Step 5: Commit the persistent project preference**

```bash
git add .trellis/config.yaml
git commit -m "chore(trellis): prefer sub-agent dispatch"
```

## Task 1: Configure Monthly Grouped Version Updates

**Files:**

- Modify: `.github/dependabot.yml`
- Verify unchanged: `backend/go.mod`
- Verify unchanged: `backend/go.sum`
- Verify unchanged: `web/package.json`
- Verify unchanged: `web/package-lock.json`

- [ ] **Step 1: Run the governance assertion and confirm the old config fails**

Run:

```bash
ruby -ryaml -e '
config = YAML.safe_load(File.read(".github/dependabot.yml"))
updates = config.fetch("updates").to_h { |entry| [entry.fetch("package-ecosystem"), entry] }
limits = {"gomod" => 1, "npm" => 2, "github-actions" => 1}
expected_groups = {
  "gomod" => {"go-minor-patch" => nil},
  "npm" => {
    "npm-production-minor-patch" => "production",
    "npm-development-minor-patch" => "development"
  },
  "github-actions" => {"actions-minor-patch" => nil}
}
limits.each do |ecosystem, limit|
  entry = updates.fetch(ecosystem)
  schedule = entry.fetch("schedule")
  abort "#{ecosystem}: schedule" unless schedule == {
    "interval" => "monthly",
    "time" => "03:00",
    "timezone" => "Asia/Shanghai"
  }
  abort "#{ecosystem}: limit" unless entry.fetch("open-pull-requests-limit") == limit
  allow_types = entry.fetch("allow").fetch(0).fetch("update-types")
  abort "#{ecosystem}: allow" unless allow_types.sort == [
    "version-update:semver-minor",
    "version-update:semver-patch"
  ]
  groups = entry.fetch("groups")
  expected_groups.fetch(ecosystem).each do |name, dependency_type|
    group = groups.fetch(name)
    abort "#{ecosystem}: #{name} scope" unless group.fetch("applies-to") == "version-updates"
    abort "#{ecosystem}: #{name} patterns" unless group.fetch("patterns") == ["*"]
    abort "#{ecosystem}: #{name} types" unless group.fetch("update-types").sort == ["minor", "patch"]
    abort "#{ecosystem}: #{name} dependency type" unless dependency_type.nil? || group.fetch("dependency-type") == dependency_type
  end
end
puts "Dependabot governance config OK"
'
```

Expected: exit 1 with `gomod: schedule` against the weekly ungrouped configuration.

- [ ] **Step 2: Replace `.github/dependabot.yml` with the approved configuration**

Use this complete content:

```yaml
version: 2
updates:
  - package-ecosystem: "gomod"
    directory: "/backend"
    schedule:
      interval: "monthly"
      time: "03:00"
      timezone: "Asia/Shanghai"
    open-pull-requests-limit: 1
    allow:
      - dependency-name: "*"
        update-types:
          - "version-update:semver-minor"
          - "version-update:semver-patch"
    groups:
      go-minor-patch:
        applies-to: "version-updates"
        patterns:
          - "*"
        update-types:
          - "minor"
          - "patch"
    labels:
      - "dependencies"
      - "go"

  - package-ecosystem: "npm"
    directory: "/web"
    schedule:
      interval: "monthly"
      time: "03:00"
      timezone: "Asia/Shanghai"
    open-pull-requests-limit: 2
    allow:
      - dependency-name: "*"
        update-types:
          - "version-update:semver-minor"
          - "version-update:semver-patch"
    groups:
      npm-production-minor-patch:
        applies-to: "version-updates"
        dependency-type: "production"
        patterns:
          - "*"
        update-types:
          - "minor"
          - "patch"
      npm-development-minor-patch:
        applies-to: "version-updates"
        dependency-type: "development"
        patterns:
          - "*"
        update-types:
          - "minor"
          - "patch"
    labels:
      - "dependencies"
      - "javascript"

  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "monthly"
      time: "03:00"
      timezone: "Asia/Shanghai"
    open-pull-requests-limit: 1
    allow:
      - dependency-name: "*"
        update-types:
          - "version-update:semver-minor"
          - "version-update:semver-patch"
    groups:
      actions-minor-patch:
        applies-to: "version-updates"
        patterns:
          - "*"
        update-types:
          - "minor"
          - "patch"
    labels:
      - "dependencies"
      - "ci"
```

- [ ] **Step 3: Re-run the governance assertion**

Run the exact Ruby command from Step 1.

Expected: exit 0 and `Dependabot governance config OK`.

- [ ] **Step 4: Confirm dependency files did not change**

Run:

```bash
git diff --exit-code origin/main -- backend/go.mod backend/go.sum web/package.json web/package-lock.json
```

Expected: exit 0 with no output.

- [ ] **Step 5: Commit the Dependabot policy**

```bash
git add .github/dependabot.yml
git commit -m "chore(deps): group routine dependency updates"
```

Expected: one commit containing only `.github/dependabot.yml`.

## Task 2: Remove Duplicate Pull-Request CI Runs

**Files:**

- Modify: `.github/workflows/ci.yml:3`
- Verify unchanged: all `.github/workflows/ci.yml` jobs and action pins below the event block.

- [ ] **Step 1: Run the trigger assertion and confirm the old config fails**

Run:

```bash
ruby -ryaml -e '
workflow = YAML.safe_load(File.read(".github/workflows/ci.yml"))
events = workflow.fetch(true)
abort "push must target only main" unless events.fetch("push") == {"branches" => ["main"]}
abort "pull_request trigger missing" unless events.key?("pull_request")
puts "CI trigger config OK"
'
```

Expected: exit 1 with `push must target only main`.

- [ ] **Step 2: Restrict only the push event**

Replace the existing event block with:

```yaml
on:
  push:
    branches:
      - main
  pull_request:
```

Do not modify `concurrency`, `permissions`, `jobs`, action pins, commands, or job conditions.

- [ ] **Step 3: Re-run the trigger assertion**

Run the exact Ruby command from Step 1.

Expected: exit 0 and `CI trigger config OK`.

- [ ] **Step 4: Validate the complete workflow with actionlint**

Run:

```bash
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/ci.yml
```

Expected: exit 0 with no lint findings.

- [ ] **Step 5: Confirm the diff is event-only and commit**

Run:

```bash
git diff -- .github/workflows/ci.yml
```

Expected: only the `push.branches: [main]` restriction is added.

```bash
git add .github/workflows/ci.yml
git commit -m "ci: avoid duplicate pull request runs"
```

## Task 3: Run The Local Quality Gate And Update Durable Guidance

**Files:**

- Modify: `.trellis/spec/guides/branch-workflow-guidelines.md`
- Update: `.trellis/tasks/08-11-dependency-update-governance/*`

- [ ] **Step 1: Run all structural assertions**

Run the exact Ruby assertions from Task 0 Step 1, Task 1 Step 1, and Task 2 Step 1.

Expected: all three exit 0 with their `OK` messages.

- [ ] **Step 2: Run workflow lint and repository hygiene checks**

```bash
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/*.yml
git diff --check origin/main...HEAD
bash scripts/check-pr-title.sh "chore(deps): govern dependency update automation"
```

Expected: all commands exit 0. Actionlint reports no errors; diff check is silent; PR title is valid.

- [ ] **Step 3: Reconfirm protected files are unchanged**

```bash
git diff --exit-code origin/main -- backend/go.mod backend/go.sum web/package.json web/package-lock.json .github/workflows/release-please.yml .codex/agents .codex/hooks.json .codex/config.toml
```

Expected: exit 0 with no output.

- [ ] **Step 4: Capture the durable contract through `trellis-update-spec`**

Add this section to `.trellis/spec/guides/branch-workflow-guidelines.md`:

```markdown
## Dependency Automation Maintenance

- Keep routine dependency version updates monthly and grouped by ecosystem; do not return to one weekly PR per dependency without an explicit maintainer decision.
- Treat ordinary major-version upgrades as dedicated tasks with compatibility research and full validation.
- Keep Dependabot vulnerability alerts and security fixes independent from the routine version-update schedule and groups.
- Before replacing old bot PRs, capture their exact numbers and head branches. Close only that allowlist; never use a dynamic close-all query that could include new security PRs.
- Run pull-request CI through `pull_request`, and limit push-triggered CI to `main` so a PR commit does not run the same workflow twice.
- For Codex Trellis work, default to project-configured sub-agent dispatch for research, implementation, and checks. Use inline execution only when the user explicitly requests it for the current task.
- Use the repository-local ignored `.worktrees/<task-slug>` path for isolated implementation worktrees; preserve `.worktrees/` in `.gitignore` and revalidate it before creation.
```

- [ ] **Step 5: Update task evidence and commit the guidance**

Create `.trellis/tasks/08-11-dependency-update-governance/research/validation-evidence.md` with the exact commands, exit codes, and relevant output from Steps 1-3, followed by a mapping from R1-R14 and AC1-AC10 to the implementing task step. Then run:

```bash
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-11-dependency-update-governance
git add .trellis/spec/guides/branch-workflow-guidelines.md .trellis/tasks/08-11-dependency-update-governance
git commit -m "docs(process): record dependency automation policy"
```

Expected: Trellis validation passes and the commit contains only the guide and task evidence.

## Task 4: Push, Open The Governance PR, And Monitor CI

**Files:** None beyond task metadata updates required by Trellis.

- [ ] **Step 1: Verify branch state before push**

```bash
git status --short --branch
git log --oneline origin/main..HEAD
```

Expected: clean worktree on `codex/chore-dependency-governance`; commits are limited to planning, Dependabot policy, CI event restriction, and durable guidance.

- [ ] **Step 2: Push and create the PR**

```bash
git push -u origin codex/chore-dependency-governance
gh pr create \
  --base main \
  --head codex/chore-dependency-governance \
  --title "chore(deps): govern dependency update automation" \
  --body "$(printf '%s\n' \
    '## Summary' \
    '- persist project-level Codex sub-agent dispatch as the default' \
    '- group routine minor/patch dependency updates into monthly ecosystem PRs' \
    '- keep security updates outside the monthly version-update groups' \
    '- run PR CI once by limiting push-triggered CI to main' \
    '' \
    '## Validation' \
    '- Dependabot structural assertions' \
    '- actionlint for all GitHub Actions workflows' \
    '- git diff hygiene and protected dependency-file checks' \
    '' \
    '## Post-merge' \
    '- close only the 13 captured legacy Dependabot PRs' \
    '- save pre-click baseline job IDs, run exactly three supported Web UI version checks, require all three to succeed, and verify grouped PRs' \
    '- enable and verify vulnerability alerts and automated security fixes' \
    '- preserve Release Please PR #386')"
```

Expected: PR targets `main` and has a valid Conventional Commit title.

- [ ] **Step 3: Record PR metadata in Trellis and push it**

Run:

```bash
governance_pr_url=$(gh pr view --json url --jq '.url')
python3 ./.trellis/scripts/task.py set-meta \
  .trellis/tasks/08-11-dependency-update-governance \
  pr_url \
  "${governance_pr_url}"
git add .trellis/tasks/08-11-dependency-update-governance/task.json
git commit -m "chore(task): record dependency governance PR"
git push
```

Expected: `task.json` records the created PR URL under task metadata and the commit is pushed to the same PR.

- [ ] **Step 4: Monitor every required CI job**

```bash
gh pr checks --watch --fail-fast=false
```

Expected: all required checks complete successfully. Fix repository-caused failures on this branch, rerun local validation, push, and resume monitoring. Record a genuine external blocker instead of merging around a missing or failing required check.

- [ ] **Step 5: Confirm the PR diff before merge**

```bash
gh pr diff
gh pr view --json mergeable,mergeStateStatus,statusCheckRollup,url
```

Expected: only approved config, Trellis task, and spec files; merge state is clean and required checks are successful.

- [ ] **Step 6: Squash merge without deleting the linked work branch**

```bash
gh pr merge --squash
```

Expected: merge succeeds only after required checks pass. Do not pass `--delete-branch`: `codex/chore-dependency-governance` remains checked out by `/home/murray/code/xirang/.worktrees/dependency-update-governance`, and `gh` must not attempt to switch or delete that linked branch. Defer both local and remote governance-branch cleanup until Task 7 removes the clean governance worktree.

## Task 5: Perform Exact Cleanup And Re-run Version Updates

**Files:** Use `.trellis/tasks/08-11-dependency-update-governance/research/open-dependabot-prs-2026-08-11.md` as the immutable allowlist.

- [ ] **Step 1: Transition to the primary worktree, sync `main`, and resolve the governance PR number**

```bash
primary_worktree=/home/murray/code/xirang
primary_branch=$(git -C "${primary_worktree}" branch --show-current) || {
  printf 'cannot inspect primary worktree branch: %s\n' "${primary_worktree}" >&2
  exit 1
}
if [[ "${primary_branch}" != "main" ]]; then
  printf 'primary worktree must already be on main, got %s\n' "${primary_branch}" >&2
  exit 1
fi
primary_status=$(git -C "${primary_worktree}" status --porcelain) || {
  printf 'cannot inspect primary worktree status: %s\n' "${primary_worktree}" >&2
  exit 1
}
if [[ -n "${primary_status}" ]]; then
  printf 'primary worktree is dirty; refusing post-merge pull\n%s\n' "${primary_status}" >&2
  exit 1
fi
git -C "${primary_worktree}" pull --ff-only origin main || {
  printf 'failed to fast-forward primary main from origin/main\n' >&2
  exit 1
}
primary_head=$(git -C "${primary_worktree}" rev-parse HEAD) || {
  printf 'cannot resolve primary worktree HEAD\n' >&2
  exit 1
}
origin_main_head=$(git -C "${primary_worktree}" rev-parse origin/main) || {
  printf 'cannot resolve origin/main\n' >&2
  exit 1
}
if [[ "${primary_head}" != "${origin_main_head}" ]]; then
  printf 'primary HEAD %s does not equal origin/main %s\n' \
    "${primary_head}" "${origin_main_head}" >&2
  exit 1
fi
cd "${primary_worktree}" || {
  printf 'cannot enter primary worktree: %s\n' "${primary_worktree}" >&2
  exit 1
}

governance_pr_record=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state merged \
  --head codex/chore-dependency-governance \
  --limit 2 \
  --json number,state \
  --jq 'if length == 1 then .[0] | [.number, .state] | @tsv else empty end') || {
    printf 'failed to resolve merged governance PR\n' >&2
    exit 1
  }
if [[ -z "${governance_pr_record}" ]]; then
  printf 'expected exactly one merged governance PR for the governance branch\n' >&2
  exit 1
fi
IFS=$'\t' read -r governance_pr_number governance_pr_state <<<"${governance_pr_record}"
if [[ "${governance_pr_state}" != "MERGED" ]]; then
  printf 'governance PR #%s is not merged: %s\n' \
    "${governance_pr_number}" "${governance_pr_state}" >&2
  exit 1
fi
gh pr view "${governance_pr_number}" \
  --repo xiangnan0811/xirang \
  --json number,url,state,mergedAt,mergeCommit || {
    printf 'failed to inspect merged governance PR #%s\n' \
      "${governance_pr_number}" >&2
    exit 1
  }
```

Expected: the primary worktree is clean, already on `main`, and its `HEAD` equals `origin/main`; governance PR state is `MERGED`. GitHub CLI verification can be run from either worktree because every command names `--repo`, but this explicit `cd` makes `/home/murray/code/xirang` the working directory for all remaining post-merge steps. Never run `git switch main` from the governance worktree.

- [ ] **Step 2: Revalidate every captured PR before closing anything**

Run this exact allowlist validation:

```bash
while IFS='|' read -r pr_number expected_head; do
  actual=$(gh pr view "${pr_number}" \
    --repo xiangnan0811/xirang \
    --json state,author,headRefName \
    --jq '[.state, .author.login, .headRefName] | @tsv') || {
      printf 'failed to inspect captured PR #%s; refusing cleanup\n' \
        "${pr_number}" >&2
      exit 1
    }
  expected=$(printf 'OPEN\tapp/dependabot\t%s' "${expected_head}")
  if [[ "${actual}" != "${expected}" ]]; then
    printf 'refusing cleanup for PR #%s: expected %s, got %s\n' \
      "${pr_number}" "${expected}" "${actual}" >&2
    exit 1
  fi
done <<'EOF'
409|dependabot/npm_and_yarn/web/eslint-10.8.0
408|dependabot/npm_and_yarn/web/react-i18next-17.0.11
407|dependabot/npm_and_yarn/web/radix-ui/react-label-2.1.15
406|dependabot/npm_and_yarn/web/radix-ui/react-dialog-1.1.23
405|dependabot/npm_and_yarn/web/radix-ui/react-separator-1.1.15
397|dependabot/go_modules/backend/github.com/aws/aws-sdk-go-v2/service/s3-1.105.2
396|dependabot/github_actions/actions/setup-node-7.0.0
395|dependabot/github_actions/actions/setup-go-7.0.0
380|dependabot/go_modules/backend/golang.org/x/net-0.57.0
378|dependabot/go_modules/backend/golang.org/x/crypto-0.54.0
377|dependabot/github_actions/docker/metadata-action-6.2.0
376|dependabot/go_modules/backend/github.com/pkg/sftp-1.13.11
375|dependabot/go_modules/backend/github.com/mattn/go-sqlite3-1.14.48
EOF
```

Expected: exit 0 with no output. Any mismatch aborts the cleanup; never substitute a newly created PR.

- [ ] **Step 3: Close only the validated snapshot PRs**

Run:

```bash
for pr_number in 409 408 407 406 405 397 396 395 380 378 377 376 375; do
  gh pr close "${pr_number}" \
    --repo xiangnan0811/xirang \
    --comment "Superseded by the merged monthly grouped dependency-update policy. Security updates are handled separately and will be enabled and verified in the next migration step." || {
      printf 'failed to close captured PR #%s; stopping exact cleanup\n' \
        "${pr_number}" >&2
      exit 1
    }
done
```

Expected: only the 13 captured version-update PRs become closed. PR #386 remains open.

- [ ] **Step 4: Verify PR state and clean only the captured branches**

```bash
gh pr list --repo xiangnan0811/xirang --state open --limit 200 --json number,author,headRefName,title,url

while IFS='|' read -r pr_number expected_head; do
  pr_record=$(gh pr view "${pr_number}" \
    --repo xiangnan0811/xirang \
    --json state,author,headRefName,headRefOid \
    --jq '[.state, .author.login, .headRefName, .headRefOid] | @tsv') || {
      printf 'failed to verify closed captured PR #%s; refusing branch cleanup\n' \
        "${pr_number}" >&2
      exit 1
    }
  IFS=$'\t' read -r pr_state pr_author actual_head validated_oid <<<"${pr_record}"
  if [[ "${pr_state}" != "CLOSED" ]]; then
    printf 'captured PR #%s must be CLOSED, got %s\n' \
      "${pr_number}" "${pr_state}" >&2
    exit 1
  fi
  if [[ "${pr_author}" != "app/dependabot" ]]; then
    printf 'captured PR #%s author mismatch: %s\n' \
      "${pr_number}" "${pr_author}" >&2
    exit 1
  fi
  if [[ "${actual_head}" != "${expected_head}" ]]; then
    printf 'captured PR #%s head mismatch: expected %s, got %s\n' \
      "${pr_number}" "${expected_head}" "${actual_head}" >&2
    exit 1
  fi
  if [[ ! "${validated_oid}" =~ ^[0-9a-fA-F]{40}$ ]]; then
    printf 'captured PR #%s has invalid full headRefOid: %s\n' \
      "${pr_number}" "${validated_oid}" >&2
    exit 1
  fi
  remote_lookup=$(git ls-remote --heads origin "refs/heads/${expected_head}" 2>&1)
  remote_lookup_status=$?
  if (( remote_lookup_status != 0 )); then
    printf 'external blocker checking captured branch %s (exit %s):\n%s\n' \
      "${expected_head}" "${remote_lookup_status}" "${remote_lookup}" >&2
    exit 1
  fi
  if [[ -n "${remote_lookup}" ]]; then
    IFS=$'\t' read -r remote_head_oid remote_head_ref <<<"${remote_lookup}"
    if [[ ! "${remote_head_oid}" =~ ^[0-9a-fA-F]{40}$ || \
          "${remote_head_ref}" != "refs/heads/${expected_head}" ]]; then
      printf 'unexpected remote head while checking %s: %s\n' \
        "${expected_head}" "${remote_lookup}" >&2
      exit 1
    fi
    if [[ "${remote_head_oid}" != "${validated_oid}" ]]; then
      printf 'captured branch %s moved: PR OID %s, remote OID %s\n' \
        "${expected_head}" "${validated_oid}" "${remote_head_oid}" >&2
      exit 1
    fi
    git push \
      --force-with-lease="refs/heads/${expected_head}:${validated_oid}" \
      origin --delete "${expected_head}" || {
      printf 'leased deletion failed for captured branch %s at OID %s\n' \
        "${expected_head}" "${validated_oid}" >&2
      exit 1
    }
  fi
done <<'EOF'
409|dependabot/npm_and_yarn/web/eslint-10.8.0
408|dependabot/npm_and_yarn/web/react-i18next-17.0.11
407|dependabot/npm_and_yarn/web/radix-ui/react-label-2.1.15
406|dependabot/npm_and_yarn/web/radix-ui/react-dialog-1.1.23
405|dependabot/npm_and_yarn/web/radix-ui/react-separator-1.1.15
397|dependabot/go_modules/backend/github.com/aws/aws-sdk-go-v2/service/s3-1.105.2
396|dependabot/github_actions/actions/setup-node-7.0.0
395|dependabot/github_actions/actions/setup-go-7.0.0
380|dependabot/go_modules/backend/golang.org/x/net-0.57.0
378|dependabot/go_modules/backend/golang.org/x/crypto-0.54.0
377|dependabot/github_actions/docker/metadata-action-6.2.0
376|dependabot/go_modules/backend/github.com/pkg/sftp-1.13.11
375|dependabot/go_modules/backend/github.com/mattn/go-sqlite3-1.14.48
EOF

remaining_dependabot_heads=$(git ls-remote --heads origin 'refs/heads/dependabot/*' 2>&1)
remaining_lookup_status=$?
if (( remaining_lookup_status != 0 )); then
  printf 'external blocker verifying remaining Dependabot heads (exit %s):\n%s\n' \
    "${remaining_lookup_status}" "${remaining_dependabot_heads}" >&2
  exit 1
fi
printf '%s\n' "${remaining_dependabot_heads}"
```

Expected: no captured PR remains open and no captured Dependabot head remains. The loop refuses deletion unless the exact PR is CLOSED, Dependabot-authored, has the expected head name, and supplies a full `headRefOid`; an existing remote ref must still equal that OID. The expected-OID lease makes deletion atomic and rejects a ref that moves after validation. Lookup transport errors, OID mismatches, and lease failures stop cleanup as external blockers; never substitute a dynamic close-all or delete-all query.

- [ ] **Step 5: Verify Release Please was preserved**

```bash
release_please_record=$(gh pr view 386 \
  --repo xiangnan0811/xirang \
  --json number,state,headRefName,url \
  --jq '[.number, .state, .headRefName, .url] | @tsv') || {
    printf 'failed to inspect protected Release Please PR #386\n' >&2
    exit 1
  }
IFS=$'\t' read -r release_pr_number release_pr_state release_pr_head release_pr_url \
  <<<"${release_please_record}"
if [[ "${release_pr_number}" != "386" || "${release_pr_state}" != "OPEN" || \
      "${release_pr_head}" != "release-please--branches--main" || \
      -z "${release_pr_url}" ]]; then
  printf 'protected Release Please PR mismatch: %s\n' \
    "${release_please_record}" >&2
  exit 1
fi
printf '386\tOPEN\trelease-please--branches--main\t%s\n' "${release_pr_url}"
```

Expected: query succeeds and asserts number `386`, state `OPEN`, head `release-please--branches--main`, and a nonempty URL before any manual version-update trigger.

- [ ] **Step 6: Trigger exactly three supported version-update checks in the GitHub Web UI**

At execution time, load and use the `browser` skill against an authenticated GitHub browser session. Navigate to the repository Dependabot page:

```text
https://github.com/xiangnan0811/xirang/network/updates
```

Follow the documented path `Repository > Insights > Dependency graph > Dependabot > Recent update jobs`. Before each click, identify the exact ecosystem/directory row and save its current recent-job IDs as the pre-click baseline so the new job can be distinguished. Click `Check for updates` exactly once for each of these entries, in this order:

1. `gomod` at `/backend`
2. `npm` at `/web`
3. `github-actions` at `/`

npm is one ecosystem/directory trigger even though its configuration has production and development groups. Do not click either npm group separately and do not trigger more than these three jobs.

Immediately record this table in the task evidence, using ISO 8601 timestamps with timezone and the new job's logs link:

| ecosystem | directory | pre-click baseline job IDs | click time | job ID | job timestamp | type | current status | logs URL |
|---|---|---|---|---|---|---|---|---|
| `gomod` | `/backend` | | | | | | | |
| `npm` | `/web` | | | | | | | |
| `github-actions` | `/` | | | | | | | |

If a click times out, the page reloads, or the outcome is otherwise ambiguous, do not click again. Reload Recent update jobs and compare the current IDs for that exact ecosystem/directory with its saved baseline until exactly one new job is identified. If zero or multiple new IDs remain ambiguous, record a blocker and stop; a second click could create a duplicate trigger.

Expected: exactly three new version-update jobs are identified from three clicks, with a saved baseline for each. This Web UI action is the only documented supported rerun mechanism: <https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/manage-your-dependency-security/re-run-dependabot-jobs>. GitHub documents no public REST, GraphQL, or first-party `gh` trigger for it; do not call undocumented internal endpoints.

- [ ] **Step 7: Monitor all three jobs asynchronously to a terminal result**

Use the `browser` automation to revisit each captured logs URL without clicking `Check for updates` again. Record every status transition and the final timestamp/type/status. Do not treat `queued` as complete: it remains pending and must be checked again asynchronously. `success` is terminal and valid even when the logs report no eligible update. For any `failure`, inspect and record the relevant log error and investigate repository-caused failures, but do not submit a fourth trigger.

Expected: all three captured jobs have terminal `success` evidence. Any queued job keeps the task pending; any failure, including a documented external blocker, leaves R10/AC1/AC2/AC6 incomplete and blocks Task 6. A failure cannot be converted into acceptance evidence and must not cause a fourth click.

- [ ] **Step 8: Capture and verify the new grouped version-update PRs**

From the three successful job pages/logs, capture the exact PR numbers and URLs associated with those runs. If the jobs created PRs, populate `version_pr_numbers` with only those exact numbers; otherwise leave it empty. Run this read-only shape check from the primary worktree:

```bash
version_pr_numbers=() # replace with the exact associated PR numbers, for example: (410 411)
if (( ${#version_pr_numbers[@]} > 4 )); then
  printf 'too many grouped version-update PRs: %s\n' "${#version_pr_numbers[@]}" >&2
  exit 1
fi

declare -A seen_groups=()
for pr_number in "${version_pr_numbers[@]}"; do
  actual=$(gh pr view "${pr_number}" \
    --repo xiangnan0811/xirang \
    --json state,author,headRefName,url \
    --jq '[.state, .author.login, .headRefName, .url] | @tsv') || {
      printf 'failed to inspect associated version-update PR #%s\n' "${pr_number}" >&2
      exit 1
    }
  IFS=$'\t' read -r state author head url <<<"${actual}"
  if [[ "${state}" != "OPEN" ]]; then
    printf 'associated version-update PR #%s must be OPEN, got %s\n' \
      "${pr_number}" "${state}" >&2
    exit 1
  fi
  if [[ "${author}" != "app/dependabot" ]]; then
    printf 'associated version-update PR #%s must be authored by app/dependabot, got %s\n' \
      "${pr_number}" "${author}" >&2
    exit 1
  fi
  case "${head}" in
    dependabot/go_modules/backend/go-minor-patch|dependabot/go_modules/backend/go-minor-patch-*) group=go-minor-patch ;;
    dependabot/npm_and_yarn/web/npm-production-minor-patch|dependabot/npm_and_yarn/web/npm-production-minor-patch-*) group=npm-production-minor-patch ;;
    dependabot/npm_and_yarn/web/npm-development-minor-patch|dependabot/npm_and_yarn/web/npm-development-minor-patch-*) group=npm-development-minor-patch ;;
    dependabot/github_actions/actions-minor-patch|dependabot/github_actions/actions-minor-patch-*) group=actions-minor-patch ;;
    *) printf 'unapproved grouped PR head for #%s: %s\n' "${pr_number}" "${head}" >&2; exit 1 ;;
  esac
  if [[ -n "${seen_groups[${group}]+x}" ]]; then
    printf 'duplicate grouped PR identity: %s\n' "${group}" >&2
    exit 1
  fi
  seen_groups[${group}]=1
  printf '%s\t%s\t%s\t%s\n' "${pr_number}" "${group}" "${head}" "${url}"
done
```

Expected: zero to four associated PRs, each open, Dependabot-authored, and mapped to one of the four approved unique group identities. Record the job-to-PR mapping and command output in task evidence. Do not close these new grouped PRs. Only after Steps 6-8 prove all three jobs succeeded and the grouped-PR evidence is complete may Task 6 enable security updates.

## Task 6: Enable And Verify Security Updates

**Files:** No repository files; GitHub repository settings only.

**Entry gate:** Task 5 exact cleanup is verified; all three manual version-update jobs are terminal `success`; all associated version-update PRs have passed the approved-identity, unique-identity, and maximum-four checks. Any queued/failure job blocks this task even when recorded as an external blocker. This ordering is mandatory so newly created security PRs cannot be confused with version-update PRs.

- [ ] **Step 1: Enable vulnerability alerts**

```bash
gh api --method PUT repos/xiangnan0811/xirang/vulnerability-alerts >/dev/null || {
  printf 'failed to enable Dependabot vulnerability alerts\n' >&2
  exit 1
}
```

Expected: HTTP 204 success.

- [ ] **Step 2: Enable automated security fixes**

```bash
gh api --method PUT repos/xiangnan0811/xirang/automated-security-fixes >/dev/null || {
  printf 'failed to enable Dependabot automated security fixes\n' >&2
  exit 1
}
```

Expected: HTTP 204 success.

- [ ] **Step 3: Verify both settings through read-only APIs**

```bash
vulnerability_alert_response=$(gh api --include \
  repos/xiangnan0811/xirang/vulnerability-alerts) || {
    printf 'vulnerability-alert verification request failed\n' >&2
    exit 1
  }
printf '%s\n' "${vulnerability_alert_response}"

security_fix_record=$(gh api \
  repos/xiangnan0811/xirang/automated-security-fixes \
  --jq '[.enabled, .paused] | @tsv') || {
    printf 'automated-security-fixes verification request failed\n' >&2
    exit 1
  }
IFS=$'\t' read -r security_fixes_enabled security_fixes_paused \
  <<<"${security_fix_record}"
if [[ "${security_fixes_enabled}" != "true" || \
      "${security_fixes_paused}" != "false" ]]; then
  printf 'unexpected automated-security-fixes state: enabled=%s paused=%s\n' \
    "${security_fixes_enabled}" "${security_fixes_paused}" >&2
  exit 1
fi
printf 'automated security fixes: enabled=true paused=false\n'
```

Expected: the vulnerability-alert read request succeeds; automated security fixes is asserted as exactly `enabled=true` and `paused=false`. Query failures, `enabled=false`, or `paused=true` abort verification.

- [ ] **Step 4: Inspect newly opened bot PRs without closing them**

```bash
new_bot_prs=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state open \
  --author 'app/dependabot' \
  --limit 200 \
  --json number,title,headRefName,createdAt,url) || {
    printf 'failed to inspect newly opened Dependabot PRs\n' >&2
    exit 1
  }
printf '%s\n' "${new_bot_prs}"
```

Expected: any newly created security PR remains open. The routine version PR shape was already captured and verified in Task 5 before security enablement; do not close either class of new PR.

- [ ] **Step 5: Inspect open security alerts for manual-major follow-up**

```bash
gh api --method GET --paginate repos/xiangnan0811/xirang/dependabot/alerts \
  --field state=open \
  --jq '.[] | [.number, .dependency.package.ecosystem, .dependency.package.name, .security_advisory.severity, (.security_vulnerability.first_patched_version.identifier // "no-automatic-patch")] | @tsv' || {
    printf 'failed to inspect open Dependabot security alerts\n' >&2
    exit 1
  }
```

Expected: the command succeeds. For any open alert without an automatically created fix PR, record the package, severity, and first patched version in task evidence; create a separate high-priority Trellis upgrade task when the fix requires a major-version compatibility change.

## Task 7: Verify Post-Merge Automation And Finish Trellis

**Files:** Trellis task archive and developer journal as directed by `trellis-finish-work`.

- [ ] **Step 1: Verify the main push CI run**

```bash
governance_pr_record=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state merged \
  --head codex/chore-dependency-governance \
  --limit 2 \
  --json number,state,mergeCommit,url \
  --jq 'if length == 1 then .[0] | [(.number | tostring), .state, (.mergeCommit.oid // ""), .url] | @tsv else empty end') || {
    printf 'failed to resolve the merged governance PR\n' >&2
    exit 1
  }
if [[ -z "${governance_pr_record}" ]]; then
  printf 'expected exactly one merged governance PR\n' >&2
  exit 1
fi
IFS=$'\t' read -r governance_pr_number governance_pr_state \
  governance_merge_sha governance_pr_url <<<"${governance_pr_record}"
if [[ "${governance_pr_state}" != "MERGED" || \
      ! "${governance_merge_sha}" =~ ^[0-9a-fA-F]{40}$ || \
      -z "${governance_pr_url}" ]]; then
  printf 'invalid merged governance PR record: %s\n' \
    "${governance_pr_record}" >&2
  exit 1
fi

ci_run_record=$(gh run list \
  --repo xiangnan0811/xirang \
  --workflow ci.yml \
  --branch main \
  --commit "${governance_merge_sha}" \
  --event push \
  --limit 2 \
  --json databaseId,event,headSha,status,conclusion,url,createdAt \
  --jq 'if length == 1 then .[0] | [(.databaseId | tostring), .event, .headSha, .status, (.conclusion // "NONE"), .url, .createdAt] | @tsv else empty end') || {
    printf 'failed to query CI runs for governance merge %s\n' \
      "${governance_merge_sha}" >&2
    exit 1
  }
if [[ -z "${ci_run_record}" ]]; then
  printf 'expected exactly one CI push run for governance merge %s\n' \
    "${governance_merge_sha}" >&2
  exit 1
fi
IFS=$'\t' read -r ci_run_id ci_event ci_head_sha ci_status ci_conclusion \
  ci_run_url ci_created_at <<<"${ci_run_record}"
if [[ ! "${ci_run_id}" =~ ^[0-9]+$ || "${ci_event}" != "push" || \
      "${ci_head_sha}" != "${governance_merge_sha}" || \
      "${ci_status}" != "completed" || "${ci_conclusion}" != "success" || \
      -z "${ci_run_url}" ]]; then
  printf 'governance CI run failed assertion: %s\n' "${ci_run_record}" >&2
  exit 1
fi
printf 'governance_pr=%s\tmerge_sha=%s\tci_run=%s\tstatus=completed\tconclusion=success\turl=%s\n' \
  "${governance_pr_url}" "${governance_merge_sha}" "${ci_run_id}" "${ci_run_url}"
```

Expected: exactly one merged governance PR supplies a full `mergeCommit.oid`; exactly one CI run matches that SHA and `push` event, with matching `headSha`, terminal `completed` status, `success` conclusion, and recorded run ID/URL.

- [ ] **Step 2: Inspect Release Please and publishing workflows**

```bash
governance_pr_record=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state merged \
  --head codex/chore-dependency-governance \
  --limit 2 \
  --json number,state,mergeCommit,url \
  --jq 'if length == 1 then .[0] | [(.number | tostring), .state, (.mergeCommit.oid // ""), .url] | @tsv else empty end') || {
    printf 'failed to resolve the merged governance PR for post-merge checks\n' >&2
    exit 1
  }
if [[ -z "${governance_pr_record}" ]]; then
  printf 'expected exactly one merged governance PR for post-merge checks\n' >&2
  exit 1
fi
IFS=$'\t' read -r governance_pr_number governance_pr_state \
  governance_merge_sha governance_pr_url <<<"${governance_pr_record}"
if [[ "${governance_pr_state}" != "MERGED" || \
      ! "${governance_merge_sha}" =~ ^[0-9a-fA-F]{40}$ || \
      -z "${governance_pr_url}" ]]; then
  printf 'invalid merged governance PR record: %s\n' \
    "${governance_pr_record}" >&2
  exit 1
fi

release_run_record=$(gh run list \
  --repo xiangnan0811/xirang \
  --workflow release-please.yml \
  --branch main \
  --commit "${governance_merge_sha}" \
  --event push \
  --limit 2 \
  --json databaseId,event,headSha,status,conclusion,url,createdAt \
  --jq 'if length == 1 then .[0] | [(.databaseId | tostring), .event, .headSha, .status, (.conclusion // "NONE"), .url, .createdAt] | @tsv else empty end') || {
    printf 'failed to query Release Please runs for governance merge %s\n' \
      "${governance_merge_sha}" >&2
    exit 1
  }
if [[ -z "${release_run_record}" ]]; then
  printf 'expected exactly one Release Please push run for governance merge %s\n' \
    "${governance_merge_sha}" >&2
  exit 1
fi
IFS=$'\t' read -r release_run_id release_event release_head_sha release_status \
  release_conclusion release_run_url release_created_at <<<"${release_run_record}"
if [[ ! "${release_run_id}" =~ ^[0-9]+$ || "${release_event}" != "push" || \
      "${release_head_sha}" != "${governance_merge_sha}" || \
      "${release_status}" != "completed" || \
      "${release_conclusion}" != "success" || -z "${release_run_url}" ]]; then
  printf 'Release Please run failed assertion: %s\n' \
    "${release_run_record}" >&2
  exit 1
fi

publish_run_count=$(gh run list \
  --repo xiangnan0811/xirang \
  --workflow publish-images.yml \
  --commit "${governance_merge_sha}" \
  --limit 2 \
  --json databaseId \
  --jq 'length') || {
    printf 'failed to query Publish Docker Images runs for governance merge %s\n' \
      "${governance_merge_sha}" >&2
    exit 1
  }
if [[ "${publish_run_count}" != "0" ]]; then
  printf 'unexpected Publish Docker Images runs for governance merge %s: %s\n' \
    "${governance_merge_sha}" "${publish_run_count}" >&2
  exit 1
fi

description_run_count=$(gh run list \
  --repo xiangnan0811/xirang \
  --workflow dockerhub-description.yml \
  --commit "${governance_merge_sha}" \
  --limit 2 \
  --json databaseId \
  --jq 'length') || {
    printf 'failed to query Sync Docker Hub Description runs for governance merge %s\n' \
      "${governance_merge_sha}" >&2
    exit 1
  }
if [[ "${description_run_count}" != "0" ]]; then
  printf 'unexpected Sync Docker Hub Description runs for governance merge %s: %s\n' \
    "${governance_merge_sha}" "${description_run_count}" >&2
  exit 1
fi

release_please_record=$(gh pr view 386 \
  --repo xiangnan0811/xirang \
  --json number,state,headRefName,url \
  --jq '[.number, .state, .headRefName, .url] | @tsv') || {
    printf 'failed to revalidate Release Please PR #386 post-merge\n' >&2
    exit 1
  }
IFS=$'\t' read -r release_pr_number release_pr_state release_pr_head release_pr_url \
  <<<"${release_please_record}"
if [[ "${release_pr_number}" != "386" || "${release_pr_state}" != "OPEN" || \
      "${release_pr_head}" != "release-please--branches--main" || \
      -z "${release_pr_url}" ]]; then
  printf 'post-merge Release Please PR mismatch: %s\n' \
    "${release_please_record}" >&2
  exit 1
fi

printf 'merge_sha=%s\trelease_run=%s\tstatus=completed\tconclusion=success\turl=%s\n' \
  "${governance_merge_sha}" "${release_run_id}" "${release_run_url}"
printf 'publish_images_runs=0\tdockerhub_description_runs=0\trelease_pr=%s\n' \
  "${release_pr_url}"
```

Expected: for the exact governance merge SHA, one Release Please `push` run is `completed`/`success`; no Publish Docker Images run exists because that workflow only handles `release.published` or manual dispatch; no Sync Docker Hub Description run exists because the governance commit changes neither README nor that workflow and no associated manual run is expected. Each query fails independently on API errors. PR #386 is revalidated as `OPEN` with its exact head and URL.

- [ ] **Step 3: Complete acceptance evidence**

Update the task PRD/check evidence with:

- config assertions and actionlint results;
- governance PR URL and merge commit;
- governance PR URL, exact merge SHA, CI run ID/URL and asserted `completed`/`success` result;
- exact old-PR closure results and remaining remote heads;
- exactly three manual version-update job records, pre-click baseline job IDs, terminal `success` statuses, logs URLs, and associated unique grouped-PR evidence;
- vulnerability-alert query success and exact automated-security-fixes enabled/paused values;
- Release Please run ID/URL/result, post-merge PR #386 URL/state/head, and exact merge-SHA no-release/no-publish query results.

- [ ] **Step 4: Run `trellis-finish-work`**

Use the project finish workflow to verify the quality gate, archive the completed task, update the developer journal, and route any archive commit through a dedicated follow-up branch and PR if required by the repository workflow.

- [ ] **Step 5: Sync `main` in the primary worktree after all merged follow-up work**

```bash
primary_worktree=/home/murray/code/xirang
primary_branch=$(git -C "${primary_worktree}" branch --show-current) || {
  printf 'cannot inspect primary worktree branch: %s\n' "${primary_worktree}" >&2
  exit 1
}
if [[ "${primary_branch}" != "main" ]]; then
  printf 'primary worktree must already be on main, got %s\n' "${primary_branch}" >&2
  exit 1
fi
primary_status=$(git -C "${primary_worktree}" status --porcelain) || {
  printf 'cannot inspect primary worktree status: %s\n' "${primary_worktree}" >&2
  exit 1
}
if [[ -n "${primary_status}" ]]; then
  printf 'primary worktree is dirty; refusing final pull\n%s\n' "${primary_status}" >&2
  exit 1
fi
git -C "${primary_worktree}" pull --ff-only origin main || {
  printf 'failed to fast-forward primary main from origin/main\n' >&2
  exit 1
}
primary_head=$(git -C "${primary_worktree}" rev-parse HEAD) || {
  printf 'cannot resolve primary worktree HEAD\n' >&2
  exit 1
}
origin_main_head=$(git -C "${primary_worktree}" rev-parse origin/main) || {
  printf 'cannot resolve origin/main\n' >&2
  exit 1
}
if [[ "${primary_head}" != "${origin_main_head}" ]]; then
  printf 'primary HEAD %s does not equal origin/main %s\n' \
    "${primary_head}" "${origin_main_head}" >&2
  exit 1
fi
git -C "${primary_worktree}" status --short --branch || {
  printf 'cannot report final primary worktree status\n' >&2
  exit 1
}
```

Expected: clean `main` tracking `origin/main` with no local-only commits.

- [ ] **Step 6: Remove the governance worktree before branch cleanup**

Run only after all governance task writes and merged follow-up work are complete:

```bash
primary_worktree=/home/murray/code/xirang
governance_worktree=/home/murray/code/xirang/.worktrees/dependency-update-governance
governance_branch=codex/chore-dependency-governance

if [[ ! -d "${governance_worktree}" ]]; then
  printf 'governance worktree does not exist: %s\n' "${governance_worktree}" >&2
  exit 1
fi
primary_branch=$(git -C "${primary_worktree}" branch --show-current) || {
  printf 'cannot inspect primary worktree branch: %s\n' "${primary_worktree}" >&2
  exit 1
}
if [[ "${primary_branch}" != "main" ]]; then
  printf 'primary worktree must be on main before cleanup, got %s\n' \
    "${primary_branch}" >&2
  exit 1
fi
primary_status=$(git -C "${primary_worktree}" status --porcelain) || {
  printf 'cannot inspect primary worktree status: %s\n' "${primary_worktree}" >&2
  exit 1
}
if [[ -n "${primary_status}" ]]; then
  printf 'primary worktree is dirty; refusing governance cleanup\n%s\n' \
    "${primary_status}" >&2
  exit 1
fi
governance_branch_actual=$(git -C "${governance_worktree}" branch --show-current) || {
  printf 'cannot inspect governance worktree branch\n' >&2
  exit 1
}
if [[ "${governance_branch_actual}" != "${governance_branch}" ]]; then
  printf 'governance worktree branch mismatch: expected %s, got %s\n' \
    "${governance_branch}" "${governance_branch_actual}" >&2
  exit 1
fi
governance_status=$(git -C "${governance_worktree}" status --porcelain) || {
  printf 'cannot inspect governance worktree status\n' >&2
  exit 1
}
if [[ -n "${governance_status}" ]]; then
  printf 'governance worktree is dirty; refusing removal\n%s\n' \
    "${governance_status}" >&2
  exit 1
fi
governance_pr_record=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state all \
  --head "${governance_branch}" \
  --limit 2 \
  --json number,state,headRefName,headRefOid \
  --jq 'if length == 1 then .[0] | [.number, .state, .headRefName, .headRefOid] | @tsv else empty end') || {
    printf 'failed to inspect governance PR\n' >&2
    exit 1
  }
if [[ -z "${governance_pr_record}" ]]; then
  printf 'no governance PR found for head %s\n' "${governance_branch}" >&2
  exit 1
fi
IFS=$'\t' read -r governance_pr_number governance_pr_state \
  governance_pr_head governance_pr_head_oid <<<"${governance_pr_record}"
if [[ "${governance_pr_state}" != "MERGED" ]]; then
  printf 'governance PR #%s is not merged: %s\n' \
    "${governance_pr_number}" "${governance_pr_state}" >&2
  exit 1
fi
if [[ "${governance_pr_head}" != "${governance_branch}" ]]; then
  printf 'governance PR head mismatch: expected %s, got %s\n' \
    "${governance_branch}" "${governance_pr_head}" >&2
  exit 1
fi
if [[ ! "${governance_pr_head_oid}" =~ ^[0-9a-fA-F]{40}$ ]]; then
  printf 'governance PR #%s has invalid full headRefOid: %s\n' \
    "${governance_pr_number}" "${governance_pr_head_oid}" >&2
  exit 1
fi
local_governance_head=$(git -C "${governance_worktree}" rev-parse HEAD) || {
  printf 'cannot resolve governance worktree HEAD\n' >&2
  exit 1
}
if [[ ! "${local_governance_head}" =~ ^[0-9a-fA-F]{40}$ ]]; then
  printf 'governance worktree has invalid full HEAD OID: %s\n' \
    "${local_governance_head}" >&2
  exit 1
fi
if [[ "${local_governance_head}" != "${governance_pr_head_oid}" ]]; then
  printf 'governance worktree HEAD %s does not equal PR headRefOid %s\n' \
    "${local_governance_head}" "${governance_pr_head_oid}" >&2
  exit 1
fi
remote_governance_lookup=$(git -C "${primary_worktree}" ls-remote --heads origin \
  "refs/heads/${governance_branch}" 2>&1)
remote_governance_status=$?
if (( remote_governance_status != 0 )); then
  printf 'external blocker checking governance branch (exit %s):\n%s\n' \
    "${remote_governance_status}" "${remote_governance_lookup}" >&2
  exit 1
fi
remote_governance_head=
if [[ -n "${remote_governance_lookup}" ]]; then
  IFS=$'\t' read -r remote_governance_head remote_governance_ref \
    <<<"${remote_governance_lookup}"
  if [[ ! "${remote_governance_head}" =~ ^[0-9a-fA-F]{40}$ || \
        "${remote_governance_ref}" != "refs/heads/${governance_branch}" ]]; then
    printf 'unexpected remote governance head: %s\n' \
      "${remote_governance_lookup}" >&2
    exit 1
  fi
  if [[ "${remote_governance_head}" != "${governance_pr_head_oid}" ]]; then
    printf 'remote governance head %s does not equal PR headRefOid %s\n' \
      "${remote_governance_head}" "${governance_pr_head_oid}" >&2
    exit 1
  fi
fi

local_governance_ref="refs/heads/${governance_branch}"
local_governance_symbolic_target=$(git -C "${primary_worktree}" symbolic-ref -q \
  "${local_governance_ref}" 2>&1)
local_governance_symbolic_status=$?
case "${local_governance_symbolic_status}" in
  0)
    printf 'local governance ref is symbolic; refusing cleanup: %s -> %s\n' \
      "${local_governance_ref}" "${local_governance_symbolic_target}" >&2
    exit 1
    ;;
  1)
    ;;
  *)
    printf 'cannot inspect whether local governance ref is symbolic (exit %s): %s\n' \
      "${local_governance_symbolic_status}" "${local_governance_ref}" >&2
    exit 1
    ;;
esac

git -C "${primary_worktree}" worktree remove "${governance_worktree}" || {
  printf 'failed to remove exact governance worktree: %s\n' \
    "${governance_worktree}" >&2
  exit 1
}
git -C "${primary_worktree}" update-ref --no-deref -d \
  "refs/heads/${governance_branch}" "${governance_pr_head_oid}" || {
  printf 'conditional local ref deletion failed for %s at OID %s\n' \
    "${governance_branch}" "${governance_pr_head_oid}" >&2
  exit 1
}
branch_config_section="branch.${governance_branch}"
branch_config_entries=$(git -C "${primary_worktree}" config --local --get-regexp \
  "^branch\\.${governance_branch}\\." 2>&1)
branch_config_status=$?
case "${branch_config_status}" in
  0)
    branch_config_present=true
    ;;
  1)
    branch_config_present=false
    ;;
  *)
    printf 'cannot inspect exact local branch config section %s (exit %s):\n%s\n' \
      "${branch_config_section}" "${branch_config_status}" \
      "${branch_config_entries}" >&2
    exit 1
    ;;
esac
if [[ "${branch_config_present}" == true ]]; then
  git -C "${primary_worktree}" config --local --remove-section \
    "${branch_config_section}" || {
    printf 'failed to remove exact local branch config section: %s\n' \
      "${branch_config_section}" >&2
    exit 1
  }
fi
if [[ -n "${remote_governance_head}" ]]; then
  git -C "${primary_worktree}" push \
    --force-with-lease="refs/heads/${governance_branch}:${governance_pr_head_oid}" \
    origin --delete "${governance_branch}" || {
    printf 'leased remote ref deletion failed for %s at OID %s\n' \
      "${governance_branch}" "${governance_pr_head_oid}" >&2
    exit 1
  }
fi
git -C "${primary_worktree}" fetch origin --prune || {
  printf 'failed to prune origin after governance cleanup\n' >&2
  exit 1
}
git -C "${primary_worktree}" worktree list || {
  printf 'failed to list worktrees after governance cleanup\n' >&2
  exit 1
}
```

Expected: every prerequisite aborts with a diagnostic before mutation when false, including zero or multiple governance PR records for the exact head branch and any empty, abbreviated, or mismatched OID. The governance worktree exists, is clean, and checks out the exact governance branch; its full local HEAD equals the unique merged PR's full `headRefOid`, and any present remote branch also equals that OID. Before mutation, the exact local branch ref must be non-symbolic (`symbolic-ref -q` status 1); symbolic refs and inspection errors abort. Only then is the clean linked worktree removed. The exact local ref is deleted with `update-ref --no-deref` conditioned on the old OID. After that succeeds, the exact `branch.${governance_branch}` config section is classified as present or absent with other inspection errors rejected, and only that exact section is removed when present, including residues such as `vscode-merge-base`; unrelated branch config remains untouched. The remote ref is deleted with an expected-OID lease. Both local and remote ref operations reject a ref moved by a concurrent writer after validation. This is the only branch-cleanup step; Task 4 intentionally leaves both refs intact so `gh` cannot switch or delete a checked-out linked branch.

## Rollback Points

- Before governance PR merge: close the governance PR; remove the clean linked governance worktree before deleting only `codex/chore-dependency-governance`. No repository settings or old PRs have changed.
- After merge but before cleanup: revert the merge through a new PR; old Dependabot PRs remain available.
- After old PR cleanup and manual reruns: reopen only exact captured PR numbers if rollback is necessary. The three submitted jobs cannot be canceled as a rollback; keep any new grouped PRs open and use their captured evidence while a configuration correction goes through a new PR.
- After security enablement: keep vulnerability alerts enabled; automated security fixes may be paused or disabled separately if an external incident requires it.

## Requirement Coverage

| Requirements | Implementation |
|---|---|
| R1-R5, AC1-AC2 | Task 1 config contract, failing/passing structural assertion, protected-file check; Task 5 three successful update jobs and live unique grouped-PR shape verification |
| R6-R7, AC3 | Task 6 independently guarded settings enablement, asserted read-only API state, bot PR inspection, and open-alert inspection |
| R8-R9, AC4 | Task 2 trigger-only change, failing/passing assertion, actionlint; Task 7 exact merge-SHA CI run cardinality/status/conclusion assertion |
| R10, AC6 | Task 5 immutable allowlist validation, OID-conditioned exact PR/branch cleanup, exactly three supported Web UI reruns, baseline IDs, three successful job/log records, and unique grouped-PR verification before Task 6 |
| R11, AC7 | Task 5 pre-trigger and Task 7 post-merge asserted PR #386 preservation checks with URL evidence |
| R12, AC5 | Tasks 1 and 3 protected-file checks, actionlint, diff hygiene, remote required CI |
| AC8 | Task 7 exact merge-SHA Release Please success assertion and zero associated publish/description run assertions |
| R13, AC9 | Task 0 explicit Trellis config preference and generated Codex integration immutability checks; Task 3 durable project guidance |
| R14, AC10 | Execution Setup ignore verification and project-local worktree creation; Task 3 durable project guidance |
