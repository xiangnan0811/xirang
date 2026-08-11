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

Create `.trellis/tasks/08-11-dependency-update-governance/research/validation-evidence.md` with the exact commands, exit codes, and relevant output from Steps 1-3, followed by a mapping from R1-R15 and AC1-AC11 to the implementing task step. Then run:

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
    '- save pre-click baseline job IDs, run exactly three supported Web UI version checks, require all three to succeed, and reconcile job-associated PRs with the complete live Dependabot PR set' \
    '- enable and verify vulnerability alerts and automated security fixes' \
    '- preserve Release Please PR #386' \
    '- deliver tracked post-merge evidence and Trellis closeout through the dedicated evidence branch and follow-up PR')"
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

**Files:** Use `.trellis/tasks/08-11-dependency-update-governance/research/open-dependabot-prs-2026-08-11.md` as the immutable allowlist. Record all live results only in `.trellis/tasks/08-11-dependency-update-governance/research/post-merge-evidence.md` on `codex/chore-dependency-governance-evidence`, initialize `.trellis/tasks/08-11-dependency-update-governance/research/r7-follow-up-task-paths.txt` as the machine-readable R7 disposition manifest, and preserve the verified branch base in `.trellis/tasks/08-11-dependency-update-governance/research/evidence-branch-base-oid.txt`.

- [ ] **Step 1: Sync primary `main`, create the exact evidence branch, and authorize tracked writes**

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
post_merge_evidence=.trellis/tasks/08-11-dependency-update-governance/research/post-merge-evidence.md
r7_follow_up_manifest=.trellis/tasks/08-11-dependency-update-governance/research/r7-follow-up-task-paths.txt
evidence_base_oid_file=.trellis/tasks/08-11-dependency-update-governance/research/evidence-branch-base-oid.txt
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

local_evidence_lookup=$(git -C "${primary_worktree}" show-ref --verify \
  "refs/heads/${evidence_branch}" 2>&1)
local_evidence_status=$?
case "${local_evidence_status}" in
  0)
    printf 'local evidence branch already exists; refusing reuse: %s\n%s\n' \
      "${evidence_branch}" "${local_evidence_lookup}" >&2
    exit 1
    ;;
  1)
    ;;
  *)
    printf 'cannot inspect local evidence branch (exit %s): %s\n' \
      "${local_evidence_status}" "${evidence_branch}" >&2
    exit 1
    ;;
esac

remote_evidence_lookup=$(git -C "${primary_worktree}" ls-remote --exit-code \
  --heads origin "refs/heads/${evidence_branch}" 2>&1)
remote_evidence_status=$?
case "${remote_evidence_status}" in
  0)
    printf 'remote evidence branch already exists; refusing reuse: %s\n%s\n' \
      "${evidence_branch}" "${remote_evidence_lookup}" >&2
    exit 1
    ;;
  2)
    ;;
  *)
    printf 'cannot inspect remote evidence branch (exit %s):\n%s\n' \
      "${remote_evidence_status}" "${remote_evidence_lookup}" >&2
    exit 1
    ;;
esac

git -C "${primary_worktree}" switch -c "${evidence_branch}" \
  "${origin_main_head}" || {
  printf 'failed to create evidence branch %s from synchronized main %s\n' \
    "${evidence_branch}" "${origin_main_head}" >&2
  exit 1
}
cd "${primary_worktree}" || {
  printf 'cannot enter primary worktree: %s\n' "${primary_worktree}" >&2
  exit 1
}
actual_worktree=$(pwd -P) || {
  printf 'cannot resolve current worktree path\n' >&2
  exit 1
}
actual_branch=$(git branch --show-current) || {
  printf 'cannot inspect evidence branch after creation\n' >&2
  exit 1
}
actual_branch_head=$(git rev-parse HEAD) || {
  printf 'cannot inspect evidence branch HEAD after creation\n' >&2
  exit 1
}
if [[ "${actual_worktree}" != "${primary_worktree}" || \
      "${actual_branch}" != "${evidence_branch}" || \
      "${actual_branch_head}" != "${origin_main_head}" || \
      ! "${origin_main_head}" =~ ^[0-9a-fA-F]{40}$ ]]; then
  printf 'tracked evidence write requires %s on %s at base %s; got %s on %s at %s\n' \
    "${primary_worktree}" "${evidence_branch}" \
    "${origin_main_head}" "${actual_worktree}" "${actual_branch}" \
    "${actual_branch_head}" >&2
  exit 1
fi
if [[ -e "${post_merge_evidence}" ]]; then
  printf 'post-merge evidence file unexpectedly exists on fresh branch: %s\n' \
    "${post_merge_evidence}" >&2
  exit 1
fi
if [[ -e "${r7_follow_up_manifest}" ]]; then
  printf 'R7 follow-up manifest unexpectedly exists on fresh branch: %s\n' \
    "${r7_follow_up_manifest}" >&2
  exit 1
fi
if [[ -e "${evidence_base_oid_file}" ]]; then
  printf 'evidence base OID file unexpectedly exists on fresh branch: %s\n' \
    "${evidence_base_oid_file}" >&2
  exit 1
fi
printf '# Dependency Update Governance Post-Merge Evidence\n\n' \
  >"${post_merge_evidence}" || {
  printf 'failed to initialize tracked post-merge evidence: %s\n' \
    "${post_merge_evidence}" >&2
  exit 1
}
printf 'PENDING\n' >"${r7_follow_up_manifest}" || {
  printf 'failed to initialize R7 follow-up manifest: %s\n' \
    "${r7_follow_up_manifest}" >&2
  exit 1
}
printf '%s\n' "${origin_main_head}" >"${evidence_base_oid_file}" || {
  printf 'failed to persist evidence branch base OID: %s\n' \
    "${evidence_base_oid_file}" >&2
  exit 1
}
```

Expected: the primary worktree begins clean on `main`, fast-forwards to `origin/main`, and rejects a pre-existing local or remote evidence branch as well as any lookup error. It then creates `codex/chore-dependency-governance-evidence` from that exact synchronized full OID, proves the new branch HEAD still equals that OID, asserts the primary path and branch, and only then performs the first tracked writes by initializing the exact evidence file, the R7 manifest to the single line `PENDING`, and the base-OID file to exactly that one full OID. The persisted base is the closeout ancestry source of truth. This branch creation is the authorization boundary for every Task 5-7 live evidence, acceptance, R7 follow-up task, archive, and journal write; never write them on `main` or in the old governance worktree. The governance PR is uniquely `MERGED`.

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

Immediately record this table in the exact `post-merge-evidence.md`, using ISO 8601 timestamps with timezone and the new job's logs link:

| ecosystem | directory | pre-click baseline job IDs | click time | job ID | job timestamp | type | current status | logs URL |
|---|---|---|---|---|---|---|---|---|
| `gomod` | `/backend` | | | | | | | |
| `npm` | `/web` | | | | | | | |
| `github-actions` | `/` | | | | | | | |

Before the first baseline/result append, reassert the write context:

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
post_merge_evidence=.trellis/tasks/08-11-dependency-update-governance/research/post-merge-evidence.md
actual_worktree=$(pwd -P) || exit 1
actual_branch=$(git branch --show-current) || exit 1
if [[ "${actual_worktree}" != "${primary_worktree}" || \
      "${actual_branch}" != "${evidence_branch}" || \
      ! -f "${post_merge_evidence}" ]]; then
  printf 'refusing Task 5 evidence write outside exact evidence context\n' >&2
  exit 1
fi
```

If a click times out, the page reloads, or the outcome is otherwise ambiguous, do not click again. Reload Recent update jobs and compare the current IDs for that exact ecosystem/directory with its saved baseline until exactly one new job is identified. If zero or multiple new IDs remain ambiguous, record a blocker and stop; a second click could create a duplicate trigger.

Expected: exactly three new version-update jobs are identified from three clicks, with a saved baseline for each. This Web UI action is the only documented supported rerun mechanism: <https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/manage-your-dependency-security/re-run-dependabot-jobs>. GitHub documents no public REST, GraphQL, or first-party `gh` trigger for it; do not call undocumented internal endpoints.

- [ ] **Step 7: Monitor all three jobs asynchronously to a terminal result**

Use the `browser` automation to revisit each captured logs URL without clicking `Check for updates` again. Record every status transition and the final timestamp/type/status. Do not treat `queued` as complete: it remains pending and must be checked again asynchronously. `success` is terminal and valid even when the logs report no eligible update. For any `failure`, inspect and record the relevant log error and investigate repository-caused failures, but do not submit a fourth trigger.

Expected: all three captured jobs have terminal `success` evidence. Any queued job keeps the task pending; any failure, including a documented external blocker, leaves R10/AC1/AC2/AC6 incomplete and blocks Task 6. A failure cannot be converted into acceptance evidence and must not cause a fourth click.

- [ ] **Step 8: Reconcile job-associated PRs with the complete live Dependabot set**

From the three successful job logs, independently capture every exact associated PR number. Populate `job_associated_pr_numbers` only from those logs, never from the live PR query. Then enumerate the complete current open `app/dependabot` set and run this gate from the primary evidence branch:

```bash
job_associated_pr_numbers=() # replace only from the three successful job logs
declare -A seen_job_pr_numbers=()
normalized_job_pr_numbers=()
for pr_number in "${job_associated_pr_numbers[@]}"; do
  if [[ ! "${pr_number}" =~ ^[0-9]+$ ]]; then
    printf 'job-associated PR number is not numeric: %s\n' "${pr_number}" >&2
    exit 1
  fi
  if [[ -n "${seen_job_pr_numbers[${pr_number}]+x}" ]]; then
    printf 'duplicate job-associated PR number: %s\n' "${pr_number}" >&2
    exit 1
  fi
  seen_job_pr_numbers[${pr_number}]=1
  normalized_job_pr_numbers+=("${pr_number}")
done
if (( ${#normalized_job_pr_numbers[@]} > 0 )); then
  mapfile -t normalized_job_pr_numbers < <(
    printf '%s\n' "${normalized_job_pr_numbers[@]}" | LC_ALL=C sort -n
  )
fi

live_dependabot_pr_rows=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state open \
  --author app/dependabot \
  --limit 200 \
  --json number,state,author,headRefName,url \
  --jq '.[] | [(.number | tostring), .state, .author.login, .headRefName, .url] | @tsv') || {
    printf 'failed to enumerate the complete live Dependabot PR set\n' >&2
    exit 1
  }

declare -A seen_live_pr_numbers=()
declare -A seen_groups=()
live_pr_numbers=()
if [[ -n "${live_dependabot_pr_rows}" ]]; then
  while IFS=$'\t' read -r pr_number state author head url; do
    if [[ ! "${pr_number}" =~ ^[0-9]+$ ]]; then
      printf 'live Dependabot PR number is not numeric: %s\n' "${pr_number}" >&2
      exit 1
    fi
    if [[ -n "${seen_live_pr_numbers[${pr_number}]+x}" ]]; then
      printf 'duplicate live Dependabot PR number: %s\n' "${pr_number}" >&2
      exit 1
    fi
    seen_live_pr_numbers[${pr_number}]=1
    if [[ "${state}" != "OPEN" ]]; then
      printf 'live Dependabot PR #%s must be OPEN, got %s\n' \
        "${pr_number}" "${state}" >&2
      exit 1
    fi
    if [[ "${author}" != "app/dependabot" ]]; then
      printf 'live Dependabot PR #%s author mismatch: %s\n' \
        "${pr_number}" "${author}" >&2
      exit 1
    fi
    case "${head}" in
      dependabot/go_modules/backend/go-minor-patch|dependabot/go_modules/backend/go-minor-patch-*) group=go-minor-patch ;;
      dependabot/npm_and_yarn/web/npm-production-minor-patch|dependabot/npm_and_yarn/web/npm-production-minor-patch-*) group=npm-production-minor-patch ;;
      dependabot/npm_and_yarn/web/npm-development-minor-patch|dependabot/npm_and_yarn/web/npm-development-minor-patch-*) group=npm-development-minor-patch ;;
      dependabot/github_actions/actions-minor-patch|dependabot/github_actions/actions-minor-patch-*) group=actions-minor-patch ;;
      *) printf 'unapproved live grouped PR head for #%s: %s\n' \
           "${pr_number}" "${head}" >&2; exit 1 ;;
    esac
    if [[ -n "${seen_groups[${group}]+x}" ]]; then
      printf 'duplicate live grouped PR identity: %s\n' "${group}" >&2
      exit 1
    fi
    if [[ -z "${url}" ]]; then
      printf 'live Dependabot PR #%s has no URL\n' "${pr_number}" >&2
      exit 1
    fi
    seen_groups[${group}]=1
    live_pr_numbers+=("${pr_number}")
    printf '%s\t%s\t%s\t%s\n' "${pr_number}" "${group}" "${head}" "${url}"
  done <<<"${live_dependabot_pr_rows}"
fi
if (( ${#live_pr_numbers[@]} > 4 )); then
  printf 'too many live grouped version-update PRs: %s\n' \
    "${#live_pr_numbers[@]}" >&2
  exit 1
fi
if (( ${#live_pr_numbers[@]} > 0 )); then
  mapfile -t live_pr_numbers < <(
    printf '%s\n' "${live_pr_numbers[@]}" | LC_ALL=C sort -n
  )
fi

if (( ${#normalized_job_pr_numbers[@]} != ${#live_pr_numbers[@]} )); then
  printf 'job/live Dependabot PR set size mismatch: job=%s live=%s\n' \
    "${normalized_job_pr_numbers[*]:-}" "${live_pr_numbers[*]:-}" >&2
  exit 1
fi
for ((index = 0; index < ${#live_pr_numbers[@]}; index++)); do
  if [[ "${normalized_job_pr_numbers[index]}" != "${live_pr_numbers[index]}" ]]; then
    printf 'job/live Dependabot PR set mismatch: job=%s live=%s\n' \
      "${normalized_job_pr_numbers[*]:-}" "${live_pr_numbers[*]:-}" >&2
    exit 1
  fi
done
printf 'reconciled grouped version PRs: %s\n' "${live_pr_numbers[*]:-none}"
```

Expected: both sources are independently normalized as numeric, duplicate-free, sorted PR-number sets and are exactly equal. An empty job-associated array fails whenever the live set is nonempty; an extra live PR or job-only number fails. Every actual live PR is OPEN, authored by `app/dependabot`, mapped from an approved head to one unique group, has a URL, and the live total is 0-4; duplicate numbers/groups and wrong state/author/head fail. Because all 13 captured legacy PRs are closed and security settings are still disabled at this gate, every live Dependabot PR must be one of these ordinary grouped version PRs; any violation blocks Task 6. Record the job/log mapping, complete live query, normalized sets, and comparison output in the exact evidence file. Leave valid new PRs open. Zero is valid only when all three jobs succeeded with no updates and both sets are empty.

## Task 6: Enable And Verify Security Updates

**Files:** GitHub repository settings, result appends to `.trellis/tasks/08-11-dependency-update-governance/research/post-merge-evidence.md`, the exact R7 disposition manifest, and any required high-priority R7 child task directories, all on the evidence branch.

**Entry gate:** Task 5 exact cleanup is verified; all three manual version-update jobs are terminal `success`; the independently derived job-associated and complete live Dependabot PR sets are numeric, unique, sorted, exactly equal, and every live PR passed the OPEN/author/head/unique-group/0-4 checks. Any queued/failure job or reconciliation failure blocks this task even when recorded as an external blocker. This ordering is mandatory so newly created security PRs cannot be confused with version-update PRs. Before appending any Task 6 result, re-run the exact primary-worktree/evidence-branch/file assertion from Task 5 Step 6.

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

Expected: the command succeeds. For every open alert, record the package, severity, first patched version, automatic-fix-PR status, and manual-major disposition in the exact post-merge evidence file after the evidence-context assertion. Create a separate high-priority Trellis child upgrade task for each compatibility scope that requires a manual major-version fix; use `--parent .trellis/tasks/08-11-dependency-update-governance` and `--no-start` so the current evidence task remains active.

- [ ] **Step 6: Resolve and record the exact R7 follow-up task paths**

After the Step 5 review and any required task creation, populate `r7_follow_up_task_paths` with the exact created task directories. Leave it empty only when the recorded alert-by-alert review proves no R7 follow-up is required. Then run:

```bash
active_task_root=.trellis/tasks/08-11-dependency-update-governance
post_merge_evidence="${active_task_root}/research/post-merge-evidence.md"
r7_follow_up_manifest="${active_task_root}/research/r7-follow-up-task-paths.txt"
r7_follow_up_task_paths=() # replace with exact created paths when R7 follow-up is required

if [[ ! -f "${post_merge_evidence}" || ! -f "${r7_follow_up_manifest}" ]]; then
  printf 'R7 resolution requires the exact evidence and manifest files\n' >&2
  exit 1
fi
if (( ${#r7_follow_up_task_paths[@]} == 0 )); then
  manifest_entries=(NONE)
else
  manifest_entries=("${r7_follow_up_task_paths[@]}")
fi

declare -A seen_r7_paths=()
for follow_up_path in "${manifest_entries[@]}"; do
  if [[ "${follow_up_path}" == "NONE" ]]; then
    if (( ${#manifest_entries[@]} != 1 )); then
      printf 'NONE cannot be combined with R7 follow-up paths\n' >&2
      exit 1
    fi
    continue
  fi
  if [[ ! "${follow_up_path}" =~ ^\.trellis/tasks/[A-Za-z0-9._-]+$ || \
        "${follow_up_path}" == "${active_task_root}" || \
        "${follow_up_path}" == ".trellis/tasks/archive" ]]; then
    printf 'invalid R7 direct-child task path: %s\n' "${follow_up_path}" >&2
    exit 1
  fi
  if [[ -n "${seen_r7_paths[${follow_up_path}]+x}" ]]; then
    printf 'duplicate R7 follow-up task path: %s\n' "${follow_up_path}" >&2
    exit 1
  fi
  if [[ ! -d "${follow_up_path}" || ! -f "${follow_up_path}/task.json" ]]; then
    printf 'R7 follow-up task path is missing created task content: %s\n' \
      "${follow_up_path}" >&2
    exit 1
  fi
  follow_up_parent=$(ruby -rjson -e \
    'data = JSON.parse(File.read(ARGV.fetch(0))); print(data["parent"].to_s)' \
    "${follow_up_path}/task.json") || exit 1
  if [[ "${follow_up_parent}" != "${active_task_root##*/}" ]]; then
    printf 'R7 follow-up is not a direct child of the current task: %s\n' \
      "${follow_up_path}" >&2
    exit 1
  fi
  seen_r7_paths[${follow_up_path}]=1
done

printf '%s\n' "${manifest_entries[@]}" >"${r7_follow_up_manifest}" || exit 1
{
  printf '## R7 Follow-Up Task Paths\n\n```text\n'
  printf '%s\n' "${manifest_entries[@]}"
  printf '```\n\n'
} >>"${post_merge_evidence}" || exit 1
```

Expected: `PENDING` is replaced by exactly `NONE` when no reviewed alert requires manual-major work, or by one or more unique exact `.trellis/tasks/<exact-task-name>` directories containing a created `task.json` whose parent is the current task. The command rejects the current task, `archive`, nested paths, duplicates, missing task content, and non-child tasks. The manifest and post-merge evidence receive the same canonical ordered entries from one array. Task 7 performs the final dirty-content proof and rejects a manifest path whose directory contributes no created changes.

## Task 7: Verify Automation, Deliver The Evidence PR, And Finish Trellis

**Files:** The exact post-merge evidence, R7 manifest, and evidence-base-OID files, any manifest-listed follow-up child task directories, Trellis task acceptance/archive files, and developer journal/index files directed by `trellis-finish-work`, all on `codex/chore-dependency-governance-evidence` in the primary worktree.

**Entry gate:** The primary worktree remains on the evidence branch created in Task 5 at the exact full OID persisted during branch creation, all Task 5-6 live results have been appended to the exact evidence file, and the R7 manifest has been resolved from `PENDING` to `NONE` or exact created direct-child task paths. Neither `main` nor the old governance worktree may receive tracked evidence writes.

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

- [ ] **Step 3: Assert the evidence context and complete acceptance evidence**

Before the first Task 7 tracked write, run:

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
post_merge_evidence=.trellis/tasks/08-11-dependency-update-governance/research/post-merge-evidence.md
r7_follow_up_manifest=.trellis/tasks/08-11-dependency-update-governance/research/r7-follow-up-task-paths.txt
evidence_base_oid_file=.trellis/tasks/08-11-dependency-update-governance/research/evidence-branch-base-oid.txt
cd "${primary_worktree}" || {
  printf 'cannot enter primary worktree: %s\n' "${primary_worktree}" >&2
  exit 1
}
actual_worktree=$(pwd -P) || exit 1
actual_branch=$(git branch --show-current) || exit 1
if [[ "${actual_worktree}" != "${primary_worktree}" || \
      "${actual_branch}" != "${evidence_branch}" || \
      ! -f "${post_merge_evidence}" || \
      ! -f "${r7_follow_up_manifest}" || \
      ! -f "${evidence_base_oid_file}" ]]; then
  printf 'Task 7 tracked writes require %s on %s with %s, %s, and %s\n' \
    "${primary_worktree}" "${evidence_branch}" \
    "${post_merge_evidence}" "${r7_follow_up_manifest}" \
    "${evidence_base_oid_file}" >&2
  exit 1
fi
mapfile -t evidence_base_oid_entries <"${evidence_base_oid_file}" || exit 1
if (( ${#evidence_base_oid_entries[@]} != 1 )) || \
   [[ ! "${evidence_base_oid_entries[0]}" =~ ^[0-9a-fA-F]{40}$ || \
      "$(git rev-parse HEAD)" != "${evidence_base_oid_entries[0]}" ]]; then
  printf 'Task 7 requires HEAD to equal the single persisted evidence base OID\n' >&2
  exit 1
fi
```

Complete the exact evidence file and task acceptance state with:

- config assertions and actionlint results;
- governance PR URL, exact merge SHA, and successful CI run ID/URL;
- exact old-PR closure and leased branch-deletion results;
- exactly three manual jobs with baseline IDs, click/job timestamps, terminal `success`, type and logs URLs;
- the independently captured job-associated PR numbers, complete live `app/dependabot` query, per-row shape/group results, both normalized sets, and exact equality result;
- vulnerability-alert query success and exact automated-security-fixes enabled/paused values;
- the alert-by-alert manual-major disposition and exact R7 manifest entries, including every created follow-up task path or the single `NONE` result;
- governance Release Please run, PR #386 state/head/URL, and exact no-publish/no-description results.

Do not yet record the future evidence-PR merge automation; that observation occurs after this evidence is committed and merged.

- [ ] **Step 4: Audit Phase 3.4 paths and commit the completed work**

Finish all post-merge evidence, acceptance-state, manifest, and R7 follow-up task content before this step. Repeat the Step 3 worktree/branch/file assertion, then run this fail-closed Phase 3.4 audit. The current active task root is always allowed; every other allowed task root must be an exact resolved manifest entry that contributes created dirty content. Workspace, archive, arbitrary sibling tasks, and any pre-staged path are rejected before staging:

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
active_task_root=.trellis/tasks/08-11-dependency-update-governance
post_merge_evidence="${active_task_root}/research/post-merge-evidence.md"
r7_follow_up_manifest="${active_task_root}/research/r7-follow-up-task-paths.txt"
cd "${primary_worktree}" || exit 1
actual_branch=$(git branch --show-current) || exit 1
if [[ "$(pwd -P)" != "${primary_worktree}" || \
      "${actual_branch}" != "${evidence_branch}" ]]; then
  printf 'Phase 3.4 work commit requires the exact primary evidence branch\n' >&2
  exit 1
fi
preexisting_staged=$(git diff --cached --name-only) || {
  printf 'cannot inspect staged paths before Phase 3.4 audit\n' >&2
  exit 1
}
if [[ -n "${preexisting_staged}" ]]; then
  printf 'unexpected staged paths before Phase 3.4 audit:\n%s\n' \
    "${preexisting_staged}" >&2
  exit 1
fi

if [[ ! -f "${post_merge_evidence}" || ! -f "${r7_follow_up_manifest}" || \
      ! -f "${evidence_base_oid_file}" ]]; then
  printf 'Phase 3.4 requires the exact evidence, R7 manifest, and base-OID files\n' >&2
  exit 1
fi
mapfile -t evidence_base_oid_entries <"${evidence_base_oid_file}" || exit 1
precommit_head=$(git rev-parse HEAD) || exit 1
if (( ${#evidence_base_oid_entries[@]} != 1 )) || \
   [[ ! "${evidence_base_oid_entries[0]}" =~ ^[0-9a-fA-F]{40}$ || \
      "${precommit_head}" != "${evidence_base_oid_entries[0]}" ]]; then
  printf 'Phase 3.4 pre-commit HEAD does not equal the persisted evidence base OID\n' >&2
  exit 1
fi
evidence_base_oid=${evidence_base_oid_entries[0]}
mapfile -t manifest_entries <"${r7_follow_up_manifest}" || exit 1
if (( ${#manifest_entries[@]} == 0 )); then
  printf 'R7 manifest is empty\n' >&2
  exit 1
fi
follow_up_task_paths=()
declare -A seen_follow_up_paths=()
if [[ "${manifest_entries[0]}" == "NONE" ]]; then
  if (( ${#manifest_entries[@]} != 1 )); then
    printf 'R7 manifest NONE must be the only line\n' >&2
    exit 1
  fi
elif [[ "${manifest_entries[0]}" == "PENDING" ]]; then
  printf 'R7 manifest is still PENDING\n' >&2
  exit 1
else
  for follow_up_path in "${manifest_entries[@]}"; do
    if [[ ! "${follow_up_path}" =~ ^\.trellis/tasks/[A-Za-z0-9._-]+$ || \
          "${follow_up_path}" == "${active_task_root}" || \
          "${follow_up_path}" == ".trellis/tasks/archive" ]]; then
      printf 'invalid R7 direct-child task path: %s\n' "${follow_up_path}" >&2
      exit 1
    fi
    if [[ -n "${seen_follow_up_paths[${follow_up_path}]+x}" ]]; then
      printf 'duplicate R7 follow-up task path: %s\n' "${follow_up_path}" >&2
      exit 1
    fi
    if [[ ! -d "${follow_up_path}" || ! -f "${follow_up_path}/task.json" ]]; then
      printf 'R7 follow-up task content is missing: %s\n' \
        "${follow_up_path}" >&2
      exit 1
    fi
    follow_up_parent=$(ruby -rjson -e \
      'data = JSON.parse(File.read(ARGV.fetch(0))); print(data["parent"].to_s)' \
      "${follow_up_path}/task.json") || exit 1
    if [[ "${follow_up_parent}" != "${active_task_root##*/}" ]]; then
      printf 'R7 follow-up is not a direct child: %s\n' \
        "${follow_up_path}" >&2
      exit 1
    fi
    seen_follow_up_paths[${follow_up_path}]=1
    follow_up_task_paths+=("${follow_up_path}")
  done
fi

changed_paths=()
while IFS= read -r changed_path; do
  [[ -n "${changed_path}" ]] && changed_paths+=("${changed_path}")
done < <(
  {
    git diff --name-only
    git ls-files --others --exclude-standard
  } | LC_ALL=C sort -u
)
if (( ${#changed_paths[@]} == 0 )); then
  printf 'no Phase 3.4 evidence/task changes found to commit\n' >&2
  exit 1
fi
evidence_path_seen=false
manifest_path_seen=false
base_oid_path_seen=false
declare -A follow_up_path_seen=()
for follow_up_path in "${follow_up_task_paths[@]}"; do
  follow_up_path_seen[${follow_up_path}]=false
done
for changed_path in "${changed_paths[@]}"; do
  allowed_path=false
  case "${changed_path}" in
    "${active_task_root}"|"${active_task_root}"/*)
      allowed_path=true
      ;;
  esac
  if [[ "${allowed_path}" != true ]]; then
    for follow_up_path in "${follow_up_task_paths[@]}"; do
      case "${changed_path}" in
        "${follow_up_path}"|"${follow_up_path}"/*)
          allowed_path=true
          follow_up_path_seen[${follow_up_path}]=true
          break
          ;;
      esac
    done
  fi
  if [[ "${allowed_path}" != true ]]; then
    printf 'unexpected Phase 3.4 path; refusing commit: %s\n' \
      "${changed_path}" >&2
    exit 1
  fi
  case "${changed_path}" in
    "${post_merge_evidence}")
      evidence_path_seen=true
      ;;
    "${r7_follow_up_manifest}")
      manifest_path_seen=true
      ;;
    "${evidence_base_oid_file}")
      base_oid_path_seen=true
      ;;
  esac
done
if [[ "${evidence_path_seen}" != true ]]; then
  printf 'exact post-merge evidence file is absent from Phase 3.4 changes\n' >&2
  exit 1
fi
if [[ "${manifest_path_seen}" != true ]]; then
  printf 'exact R7 manifest is absent from Phase 3.4 changes\n' >&2
  exit 1
fi
if [[ "${base_oid_path_seen}" != true ]]; then
  printf 'exact evidence base-OID file is absent from Phase 3.4 changes\n' >&2
  exit 1
fi
for follow_up_path in "${follow_up_task_paths[@]}"; do
  if [[ "${follow_up_path_seen[${follow_up_path}]}" != true ]]; then
    printf 'manifest path has no created dirty content: %s\n' \
      "${follow_up_path}" >&2
    exit 1
  fi
done

git add -- "${changed_paths[@]}" || {
  printf 'failed to stage exact Phase 3.4 path set\n' >&2
  exit 1
}
git diff --cached --check || {
  printf 'staged Phase 3.4 diff failed whitespace validation\n' >&2
  exit 1
}
git diff --cached --name-status
bash scripts/check-pr-title.sh \
  "docs(task): record dependency governance evidence" || exit 1
git commit -m "docs(task): record dependency governance evidence" || {
  printf 'failed to commit Phase 3.4 evidence work\n' >&2
  exit 1
}

work_commit=$(git rev-parse HEAD) || exit 1
work_parent=$(git rev-parse "${work_commit}^") || exit 1
work_subject=$(git log -1 --format=%s "${work_commit}") || exit 1
if [[ ! "${work_commit}" =~ ^[0-9a-fA-F]{40}$ || \
      "${work_parent}" != "${evidence_base_oid}" || \
      "${work_subject}" != "docs(task): record dependency governance evidence" ]]; then
  printf 'Phase 3.4 committed work identity/base-parent mismatch\n' >&2
  exit 1
fi
mapfile -t work_committed_paths < <(
  git diff-tree --no-commit-id --name-only -r --no-renames \
    "${work_commit}" | LC_ALL=C sort -u
)
if (( ${#work_committed_paths[@]} == 0 )); then
  printf 'Phase 3.4 work commit has no committed paths\n' >&2
  exit 1
fi
committed_evidence_seen=false
committed_manifest_seen=false
committed_base_oid_seen=false
declare -A committed_follow_up_seen=()
for follow_up_path in "${follow_up_task_paths[@]}"; do
  committed_follow_up_seen[${follow_up_path}]=false
done
for committed_path in "${work_committed_paths[@]}"; do
  allowed_path=false
  case "${committed_path}" in
    "${active_task_root}"|"${active_task_root}"/*)
      allowed_path=true
      ;;
  esac
  if [[ "${allowed_path}" != true ]]; then
    for follow_up_path in "${follow_up_task_paths[@]}"; do
      case "${committed_path}" in
        "${follow_up_path}"|"${follow_up_path}"/*)
          allowed_path=true
          committed_follow_up_seen[${follow_up_path}]=true
          break
          ;;
      esac
    done
  fi
  if [[ "${allowed_path}" != true ]]; then
    printf 'unexpected path in actual Phase 3.4 work commit: %s\n' \
      "${committed_path}" >&2
    exit 1
  fi
  case "${committed_path}" in
    "${post_merge_evidence}")
      committed_evidence_seen=true
      ;;
    "${r7_follow_up_manifest}")
      committed_manifest_seen=true
      ;;
    "${evidence_base_oid_file}")
      committed_base_oid_seen=true
      ;;
  esac
done
if [[ "${committed_evidence_seen}" != true || \
      "${committed_manifest_seen}" != true || \
      "${committed_base_oid_seen}" != true ]]; then
  printf 'actual work commit lacks required evidence/manifest/base-OID paths\n' >&2
  exit 1
fi
committed_manifest=$(git show \
  "${work_commit}:${r7_follow_up_manifest}") || exit 1
expected_manifest=$(printf '%s\n' "${manifest_entries[@]}")
committed_base_oid=$(git show \
  "${work_commit}:${evidence_base_oid_file}") || exit 1
if [[ "${committed_manifest}" != "${expected_manifest}" || \
      "${committed_base_oid}" != "${evidence_base_oid}" ]]; then
  printf 'hook/concurrent change altered committed manifest or base OID content\n' >&2
  exit 1
fi
for follow_up_path in "${follow_up_task_paths[@]}"; do
  if [[ "${committed_follow_up_seen[${follow_up_path}]}" != true ]]; then
    printf 'actual work commit lacks manifest follow-up content: %s\n' \
      "${follow_up_path}" >&2
    exit 1
  fi
done
if [[ -n "$(git status --porcelain)" ]]; then
  printf 'worktree changed during Phase 3.4 commit/audit\n' >&2
  exit 1
fi
printf 'evidence_base_oid=%s\nwork_commit=%s\n' \
  "${evidence_base_oid}" "${work_commit}"
```

Expected: the work commit runs only on the exact evidence branch whose pre-commit HEAD equals the single persisted base OID and contains the completed active-task evidence/acceptance/manifest/base record plus only exact manifest-listed R7 child task directories. `PENDING`, duplicate/missing/current/archive/nested/non-child manifest entries, a listed task with no created dirty content, workspace/archive changes, arbitrary `.trellis/tasks/*`, and all other paths abort before staging. After `git commit`, the actual committed tree is independently enumerated with `diff-tree --no-renames` and passed through the same active-task/exact-manifest allowlist; exact evidence, manifest, base-OID, and every follow-up path must appear. A wrong work parent/subject, hook- or concurrent-index-added path, missing required committed path, or residual dirt aborts. This is the workflow Phase 3.4 work commit; do not archive or write the journal before every post-commit assertion succeeds.

- [ ] **Step 5: Assert the clean work boundary, capture its hash, and survey finish context**

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
active_task_root=.trellis/tasks/08-11-dependency-update-governance
evidence_base_oid_file="${active_task_root}/research/evidence-branch-base-oid.txt"
cd "${primary_worktree}" || exit 1
work_status=$(git status --porcelain) || exit 1
work_commit=$(git rev-parse HEAD) || exit 1
work_parent=$(git rev-parse "${work_commit}^") || exit 1
work_subject=$(git log -1 --format=%s "${work_commit}") || exit 1
mapfile -t evidence_base_oid_entries <"${evidence_base_oid_file}" || exit 1
if (( ${#evidence_base_oid_entries[@]} != 1 )); then
  printf 'work boundary requires one persisted evidence base OID\n' >&2
  exit 1
fi
evidence_base_oid=${evidence_base_oid_entries[0]}
mapfile -t work_range_commits < <(
  git rev-list --reverse "${evidence_base_oid}..${work_commit}"
)
if [[ "$(pwd -P)" != "${primary_worktree}" || \
      "$(git branch --show-current)" != "${evidence_branch}" || \
      -n "${work_status}" || ! "${work_commit}" =~ ^[0-9a-fA-F]{40}$ || \
      ! "${evidence_base_oid}" =~ ^[0-9a-fA-F]{40}$ || \
      "${work_parent}" != "${evidence_base_oid}" || \
      ${#work_range_commits[@]} -ne 1 || \
      "${work_range_commits[0]}" != "${work_commit}" || \
      "${work_subject}" != "docs(task): record dependency governance evidence" ]]; then
  printf 'invalid Phase 3.4 work-commit boundary\n' >&2
  exit 1
fi
printf 'evidence_base_oid=%s\nwork_commit=%s\n' \
  "${evidence_base_oid}" "${work_commit}"
python3 ./.trellis/scripts/get_context.py --mode record || exit 1
```

Expected: the exact evidence branch is clean, the persisted base file still contains one full OID, `work_commit` has that OID as its direct parent, and `base..work_commit` contains exactly that one exact-subject Phase 3.4 commit. Record mode succeeds before any Trellis-managed closeout commit. Review other tasks printed by record mode but do not archive them without separate maintainer approval.

- [ ] **Step 6: Archive the current task and audit the automatic archive commit**

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
active_task_root=.trellis/tasks/08-11-dependency-update-governance
archive_task_root=.trellis/tasks/archive/2026-08/08-11-dependency-update-governance
r7_follow_up_manifest="${active_task_root}/research/r7-follow-up-task-paths.txt"
evidence_base_oid_file="${active_task_root}/research/evidence-branch-base-oid.txt"
cd "${primary_worktree}" || exit 1
if [[ "$(pwd -P)" != "${primary_worktree}" || \
      "$(git branch --show-current)" != "${evidence_branch}" || \
      -n "$(git status --porcelain)" ]]; then
  printf 'archive requires the clean exact evidence branch\n' >&2
  exit 1
fi
work_commit=$(git rev-parse HEAD) || exit 1
work_parent=$(git rev-parse "${work_commit}^") || exit 1
work_subject=$(git log -1 --format=%s "${work_commit}") || exit 1
mapfile -t evidence_base_oid_entries <"${evidence_base_oid_file}" || exit 1
if (( ${#evidence_base_oid_entries[@]} != 1 )); then
  printf 'archive requires one persisted evidence base OID\n' >&2
  exit 1
fi
evidence_base_oid=${evidence_base_oid_entries[0]}
if [[ ! "${work_commit}" =~ ^[0-9a-fA-F]{40}$ || \
      ! "${evidence_base_oid}" =~ ^[0-9a-fA-F]{40}$ || \
      "${work_parent}" != "${evidence_base_oid}" || \
      "${work_subject}" != "docs(task): record dependency governance evidence" ]]; then
  printf 'archive parent is not the exact Phase 3.4 work commit\n' >&2
  exit 1
fi
mapfile -t manifest_entries <"${r7_follow_up_manifest}" || exit 1
follow_up_task_paths=()
declare -A seen_follow_up_paths=()
if (( ${#manifest_entries[@]} == 1 )) && [[ "${manifest_entries[0]}" == "NONE" ]]; then
  :
else
  for follow_up_path in "${manifest_entries[@]}"; do
    if [[ ! "${follow_up_path}" =~ ^\.trellis/tasks/[A-Za-z0-9._-]+$ || \
          "${follow_up_path}" == "${active_task_root}" || \
          "${follow_up_path}" == ".trellis/tasks/archive" || \
          -n "${seen_follow_up_paths[${follow_up_path}]+x}" || \
          ! -d "${follow_up_path}" ]]; then
      printf 'invalid R7 manifest entry before archive: %s\n' \
        "${follow_up_path}" >&2
      exit 1
    fi
    seen_follow_up_paths[${follow_up_path}]=1
    follow_up_task_paths+=("${follow_up_path}")
  done
fi

python3 ./.trellis/scripts/task.py archive \
  08-11-dependency-update-governance || exit 1
archive_commit=$(git rev-parse HEAD) || exit 1
archive_parent=$(git rev-parse "${archive_commit}^") || exit 1
archive_subject=$(git log -1 --format=%s "${archive_commit}") || exit 1
if [[ ! "${archive_commit}" =~ ^[0-9a-fA-F]{40}$ || \
      "${archive_parent}" != "${work_commit}" || \
      "${archive_subject}" != \
        "chore(task): archive 08-11-dependency-update-governance" ]]; then
  printf 'automatic archive commit identity/order mismatch\n' >&2
  exit 1
fi
mapfile -t pre_journal_commits < <(
  git rev-list --reverse "${evidence_base_oid}..${archive_commit}"
)
if (( ${#pre_journal_commits[@]} != 2 )) || \
   [[ "${pre_journal_commits[0]}" != "${work_commit}" || \
      "${pre_journal_commits[1]}" != "${archive_commit}" ]]; then
  printf 'base-to-archive range must contain exactly work then archive\n' >&2
  exit 1
fi

mapfile -t archive_changed_paths < <(
  git diff-tree --no-commit-id --name-only --no-renames -r \
    "${archive_commit}" | \
    LC_ALL=C sort -u
)
active_path_seen=false
archive_path_seen=false
for changed_path in "${archive_changed_paths[@]}"; do
  allowed_path=false
  case "${changed_path}" in
    "${active_task_root}"|"${active_task_root}"/*)
      allowed_path=true
      active_path_seen=true
      ;;
    "${archive_task_root}"|"${archive_task_root}"/*)
      allowed_path=true
      archive_path_seen=true
      ;;
  esac
  if [[ "${allowed_path}" != true ]]; then
    for follow_up_path in "${follow_up_task_paths[@]}"; do
      case "${changed_path}" in
        "${follow_up_path}"|"${follow_up_path}"/*)
          allowed_path=true
          break
          ;;
      esac
    done
  fi
  if [[ "${allowed_path}" != true ]]; then
    printf 'unexpected automatic archive path: %s\n' "${changed_path}" >&2
    exit 1
  fi
done
if [[ "${active_path_seen}" != true || "${archive_path_seen}" != true || \
      -n "$(git status --porcelain)" ]]; then
  printf 'archive diff/path/cleanliness assertion failed\n' >&2
  exit 1
fi
printf 'work_commit=%s\narchive_commit=%s\n' \
  "${work_commit}" "${archive_commit}"
```

Expected: the persisted base still directly parents `work_commit`; `task.py archive` creates its own exact-subject commit directly on top of that work commit; and `base..archive` contains exactly those two commits in order. Its diff contains the current task source, exact `archive/2026-08` destination, and only manifest-listed follow-up paths if Trellis clears their parent relationship. All extra commits, arbitrary task/workspace paths, a wrong parent/subject, a missing source/destination move, and residual dirt abort.

- [ ] **Step 7: Record the journal and audit the automatic journal commit**

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
archive_task_root=.trellis/tasks/archive/2026-08/08-11-dependency-update-governance
evidence_base_oid_file="${archive_task_root}/research/evidence-branch-base-oid.txt"
cd "${primary_worktree}" || exit 1
if [[ "$(pwd -P)" != "${primary_worktree}" || \
      "$(git branch --show-current)" != "${evidence_branch}" || \
      -n "$(git status --porcelain)" ]]; then
  printf 'journal requires the clean exact evidence branch\n' >&2
  exit 1
fi
archive_commit=$(git rev-parse HEAD) || exit 1
work_commit=$(git rev-parse "${archive_commit}^") || exit 1
archive_parent=$(git rev-parse "${archive_commit}^") || exit 1
work_parent=$(git rev-parse "${work_commit}^") || exit 1
mapfile -t evidence_base_oid_entries <"${evidence_base_oid_file}" || exit 1
if (( ${#evidence_base_oid_entries[@]} != 1 )); then
  printf 'journal requires one persisted evidence base OID\n' >&2
  exit 1
fi
evidence_base_oid=${evidence_base_oid_entries[0]}
if [[ "$(git log -1 --format=%s "${archive_commit}")" != \
        "chore(task): archive 08-11-dependency-update-governance" || \
      "$(git log -1 --format=%s "${work_commit}")" != \
        "docs(task): record dependency governance evidence" || \
      ! "${evidence_base_oid}" =~ ^[0-9a-fA-F]{40}$ || \
      "${work_parent}" != "${evidence_base_oid}" || \
      "${archive_parent}" != "${work_commit}" ]]; then
  printf 'journal parent chain does not end in work then archive\n' >&2
  exit 1
fi

developer_name=
while IFS='=' read -r developer_key developer_value; do
  if [[ "${developer_key}" == "name" ]]; then
    developer_name=${developer_value}
  fi
done <.trellis/.developer
if [[ ! "${developer_name}" =~ ^[A-Za-z0-9._-]+$ ]]; then
  printf 'cannot resolve a safe current developer workspace name\n' >&2
  exit 1
fi
developer_workspace=".trellis/workspace/${developer_name}"

python3 ./.trellis/scripts/add_session.py \
  --title "Complete dependency update governance evidence" \
  --commit "${work_commit}" \
  --summary "Recorded post-merge governance evidence, acceptance, and R7 disposition." \
  --branch "${evidence_branch}" || exit 1

journal_commit=$(git rev-parse HEAD) || exit 1
journal_parent=$(git rev-parse "${journal_commit}^") || exit 1
journal_subject=$(git log -1 --format=%s "${journal_commit}") || exit 1
if [[ ! "${journal_commit}" =~ ^[0-9a-fA-F]{40}$ || \
      "${journal_parent}" != "${archive_commit}" || \
      "${journal_subject}" != "chore: record journal" ]]; then
  printf 'automatic journal commit identity/order mismatch\n' >&2
  exit 1
fi

mapfile -t journal_changed_paths < <(
  git diff-tree --no-commit-id --name-only -r "${journal_commit}" | \
    LC_ALL=C sort -u
)
workspace_index_seen=false
journal_file_seen=false
for changed_path in "${journal_changed_paths[@]}"; do
  case "${changed_path}" in
    "${developer_workspace}/index.md")
      workspace_index_seen=true
      ;;
    "${developer_workspace}"/journal-[0-9]*.md)
      journal_file_seen=true
      ;;
    *)
      printf 'unexpected automatic journal path: %s\n' "${changed_path}" >&2
      exit 1
      ;;
  esac
done
git merge-base --is-ancestor "${evidence_base_oid}" \
  "${journal_commit}" || {
  printf 'persisted evidence base is not an ancestor of journal HEAD\n' >&2
  exit 1
}
mapfile -t closeout_commits < <(
  git rev-list --reverse "${evidence_base_oid}..${journal_commit}"
)
if [[ "${workspace_index_seen}" != true || "${journal_file_seen}" != true || \
      ${#closeout_commits[@]} -ne 3 || \
      "${closeout_commits[0]}" != "${work_commit}" || \
      "${closeout_commits[1]}" != "${archive_commit}" || \
      "${closeout_commits[2]}" != "${journal_commit}" || \
      "$(git rev-parse "${work_commit}^")" != "${evidence_base_oid}" || \
      "$(git rev-parse "${archive_commit}^")" != "${work_commit}" || \
      "$(git rev-parse "${journal_commit}^")" != "${archive_commit}" || \
      "$(git rev-parse HEAD)" != "${journal_commit}" || \
      -n "$(git status --porcelain)" ]]; then
  printf 'final work/archive/journal order or cleanliness assertion failed\n' >&2
  exit 1
fi
git log --reverse --format='%H%x09%s' \
  "${evidence_base_oid}..${journal_commit}"
```

Expected: `add_session.py` receives the full Phase 3.4 `work_commit` only, never the archive hash, and explicitly records the evidence branch. Its automatic commit is directly on top of the archive commit, has exact subject `chore: record journal`, and changes only the current developer's exact workspace `index.md` and one or more `journal-*.md` files. The archived base-OID record must still contain one full ancestor OID; `base..HEAD` must enumerate exactly three commits matching work, archive, and journal with direct parents base → work → archive → journal. A commit inserted before, between, or after the expected three makes the exact-count/order audit fail even when the worktree is clean.

- [ ] **Step 8: Push and open the evidence follow-up PR**

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
evidence_pr_title="docs(task): record dependency governance evidence"
archive_task_root=.trellis/tasks/archive/2026-08/08-11-dependency-update-governance
evidence_base_oid_file="${archive_task_root}/research/evidence-branch-base-oid.txt"
cd "${primary_worktree}" || exit 1
actual_branch=$(git branch --show-current) || exit 1
evidence_status=$(git status --porcelain) || exit 1
if [[ "$(pwd -P)" != "${primary_worktree}" || \
      "${actual_branch}" != "${evidence_branch}" || \
      -n "${evidence_status}" ]]; then
  printf 'evidence PR push requires a clean exact evidence branch\n' >&2
  exit 1
fi
mapfile -t evidence_base_oid_entries <"${evidence_base_oid_file}" || exit 1
if (( ${#evidence_base_oid_entries[@]} != 1 )) || \
   [[ ! "${evidence_base_oid_entries[0]}" =~ ^[0-9a-fA-F]{40}$ ]]; then
  printf 'pre-push audit requires one full persisted evidence base OID\n' >&2
  exit 1
fi
evidence_base_oid=${evidence_base_oid_entries[0]}
journal_commit=$(git rev-parse HEAD) || exit 1
git merge-base --is-ancestor "${evidence_base_oid}" \
  "${journal_commit}" || {
  printf 'pre-push evidence base is not an ancestor of HEAD\n' >&2
  exit 1
}
mapfile -t closeout_commits < <(
  git rev-list --reverse "${evidence_base_oid}..${journal_commit}"
)
if (( ${#closeout_commits[@]} != 3 )); then
  printf 'pre-push base..HEAD range must contain exactly three commits, got %s\n' \
    "${#closeout_commits[@]}" >&2
  exit 1
fi
work_commit=${closeout_commits[0]}
archive_commit=${closeout_commits[1]}
expected_journal_commit=${closeout_commits[2]}
if [[ "${expected_journal_commit}" != "${journal_commit}" || \
      "$(git rev-parse "${work_commit}^")" != "${evidence_base_oid}" || \
      "$(git rev-parse "${archive_commit}^")" != "${work_commit}" || \
      "$(git rev-parse "${journal_commit}^")" != "${archive_commit}" || \
      "$(git log -1 --format=%s "${work_commit}")" != \
        "docs(task): record dependency governance evidence" || \
      "$(git log -1 --format=%s "${archive_commit}")" != \
        "chore(task): archive 08-11-dependency-update-governance" || \
      "$(git log -1 --format=%s "${journal_commit}")" != \
        "chore: record journal" ]]; then
  printf 'pre-push exact three-commit identity/order audit failed\n' >&2
  exit 1
fi
bash scripts/check-pr-title.sh "${evidence_pr_title}" || exit 1
evidence_ref="refs/heads/${evidence_branch}"
git push \
  --force-with-lease="${evidence_ref}:" \
  origin "${journal_commit}:${evidence_ref}" || {
  printf 'failed to create absent remote evidence ref at audited journal commit\n' >&2
  exit 1
}
remote_evidence_record=$(git ls-remote --heads origin "${evidence_ref}" 2>&1) || {
  printf 'failed to verify pushed evidence ref\n' >&2
  exit 1
}
IFS=$'\t' read -r remote_evidence_oid remote_evidence_ref \
  <<<"${remote_evidence_record}"
if [[ "${remote_evidence_oid}" != "${journal_commit}" || \
      "${remote_evidence_ref}" != "${evidence_ref}" ]]; then
  printf 'remote evidence ref does not equal audited journal commit: %s\n' \
    "${remote_evidence_record}" >&2
  exit 1
fi
evidence_pr_url=$(gh pr create \
  --repo xiangnan0811/xirang \
  --base main \
  --head "${evidence_branch}" \
  --title "${evidence_pr_title}" \
  --body "$(printf '%s\n' \
    '## Summary' \
    '- record exact post-merge dependency governance evidence' \
    '- preserve exact R7 follow-up task paths from the reviewed security alerts' \
    '- deliver the separate Trellis archive and developer-journal commits' \
    '' \
    '## Validation' \
    '- exact legacy cleanup and three successful Dependabot jobs' \
    '- complete live Dependabot PR enumeration and exact job/live set reconciliation' \
    '- governance main CI, Release Please, and security-setting assertions')") || {
  printf 'failed to create evidence follow-up PR\n' >&2
  exit 1
}
if [[ -z "${evidence_pr_url}" ]]; then
  printf 'evidence PR URL is empty\n' >&2
  exit 1
fi
printf '%s\n' "${evidence_pr_url}"
```

Expected: immediately before any push, the archived base-OID file still contains one full ancestor OID and `base..HEAD` contains exactly three commits with subjects and direct parents base → work → archive → journal. Any clean commit inserted before, between, or after the expected commits aborts before network mutation. The push source is the immutable audited `journal_commit`, not the movable local branch name; the explicit empty expected value in `--force-with-lease="refs/heads/...:"` atomically requires the remote evidence ref to be absent, so a concurrently created or updated ref is never overwritten. A read-back must resolve that exact remote ref to `journal_commit` before PR creation. The dedicated evidence branch then targets `main` and has a title that passes the repository Conventional Commit validator.

- [ ] **Step 9: Monitor, inspect, and squash merge the evidence PR**

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
archive_task_root=.trellis/tasks/archive/2026-08/08-11-dependency-update-governance
evidence_base_oid_file="${archive_task_root}/research/evidence-branch-base-oid.txt"
cd "${primary_worktree}" || exit 1
if [[ "$(pwd -P)" != "${primary_worktree}" || \
      "$(git branch --show-current)" != "${evidence_branch}" || \
      -n "$(git status --porcelain)" ]]; then
  printf 'PR monitoring requires the clean exact evidence branch\n' >&2
  exit 1
fi
mapfile -t evidence_base_oid_entries <"${evidence_base_oid_file}" || exit 1
if (( ${#evidence_base_oid_entries[@]} != 1 )) || \
   [[ ! "${evidence_base_oid_entries[0]}" =~ ^[0-9a-fA-F]{40}$ ]]; then
  printf 'PR monitoring requires one full persisted evidence base OID\n' >&2
  exit 1
fi
evidence_base_oid=${evidence_base_oid_entries[0]}
journal_commit=$(git rev-parse HEAD) || exit 1
mapfile -t closeout_commits < <(
  git rev-list --reverse "${evidence_base_oid}..${journal_commit}"
)
if (( ${#closeout_commits[@]} != 3 )); then
  printf 'PR monitoring requires exactly three audited closeout commits\n' >&2
  exit 1
fi
work_commit=${closeout_commits[0]}
archive_commit=${closeout_commits[1]}
if [[ "${closeout_commits[2]}" != "${journal_commit}" || \
      "$(git rev-parse "${work_commit}^")" != "${evidence_base_oid}" || \
      "$(git rev-parse "${archive_commit}^")" != "${work_commit}" || \
      "$(git rev-parse "${journal_commit}^")" != "${archive_commit}" || \
      "$(git log -1 --format=%s "${work_commit}")" != \
        "docs(task): record dependency governance evidence" || \
      "$(git log -1 --format=%s "${archive_commit}")" != \
        "chore(task): archive 08-11-dependency-update-governance" || \
      "$(git log -1 --format=%s "${journal_commit}")" != \
        "chore: record journal" ]]; then
  printf 'PR monitoring exact closeout topology audit failed\n' >&2
  exit 1
fi

evidence_pr_record=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state open \
  --head codex/chore-dependency-governance-evidence \
  --limit 2 \
  --json number,state,headRefName,headRefOid,baseRefName,title,url \
  --jq 'if length == 1 then .[0] | [(.number | tostring), .state, .headRefName, .headRefOid, .baseRefName, .title, .url] | @tsv else empty end') || {
    printf 'failed to resolve unique open evidence PR\n' >&2
    exit 1
  }
if [[ -z "${evidence_pr_record}" ]]; then
  printf 'expected exactly one open evidence PR\n' >&2
  exit 1
fi
IFS=$'\t' read -r evidence_pr_number evidence_pr_state evidence_pr_head \
  evidence_pr_head_oid evidence_pr_base evidence_pr_title evidence_pr_url \
  <<<"${evidence_pr_record}"
if [[ "${evidence_pr_state}" != "OPEN" || \
      "${evidence_pr_head}" != "codex/chore-dependency-governance-evidence" || \
      ! "${evidence_pr_head_oid}" =~ ^[0-9a-fA-F]{40}$ || \
      "${evidence_pr_head_oid}" != "${journal_commit}" || \
      "${evidence_pr_base}" != "main" || \
      "${evidence_pr_title}" != "docs(task): record dependency governance evidence" || \
      -z "${evidence_pr_url}" ]]; then
  printf 'evidence PR metadata mismatch: %s\n' "${evidence_pr_record}" >&2
  exit 1
fi
gh pr checks "${evidence_pr_number}" \
  --repo xiangnan0811/xirang \
  --watch --fail-fast=false || {
  printf 'evidence PR required checks did not all pass\n' >&2
  exit 1
}
gh pr diff "${evidence_pr_number}" --repo xiangnan0811/xirang || exit 1
gh pr view "${evidence_pr_number}" \
  --repo xiangnan0811/xirang \
  --json mergeable,mergeStateStatus,statusCheckRollup,url || exit 1
pre_merge_pr_record=$(gh pr view "${evidence_pr_number}" \
  --repo xiangnan0811/xirang \
  --json state,headRefName,headRefOid,baseRefName,url \
  --jq '[.state, .headRefName, .headRefOid, .baseRefName, .url] | @tsv') || {
  printf 'failed to revalidate evidence PR head immediately before merge\n' >&2
  exit 1
}
IFS=$'\t' read -r pre_merge_state pre_merge_head pre_merge_head_oid \
  pre_merge_base pre_merge_url <<<"${pre_merge_pr_record}"
if [[ "${pre_merge_state}" != "OPEN" || \
      "${pre_merge_head}" != "${evidence_branch}" || \
      ! "${pre_merge_head_oid}" =~ ^[0-9a-fA-F]{40}$ || \
      "${pre_merge_head_oid}" != "${journal_commit}" || \
      "${pre_merge_base}" != "main" || -z "${pre_merge_url}" ]]; then
  printf 'evidence PR head moved before merge: expected %s, got %s\n' \
    "${journal_commit}" "${pre_merge_pr_record}" >&2
  exit 1
fi
gh pr merge "${evidence_pr_number}" \
  --repo xiangnan0811/xirang \
  --match-head-commit "${journal_commit}" \
  --squash || {
  printf 'failed to squash merge evidence PR #%s\n' \
    "${evidence_pr_number}" >&2
  exit 1
}
```

Expected: local clean topology is re-audited to recover the same `journal_commit`; the unique open PR query requests a full `headRefOid` and requires it to equal that commit before CI monitoring. Every required check reaches success and the diff contains only the audited work, archive, and journal paths. Immediately before merge, a fresh PR query again requires OPEN state, exact head/base, and `headRefOid == journal_commit`; `gh pr merge --match-head-commit` carries the same expected OID into the merge request so a later head move is rejected atomically. Do not pass `--delete-branch`: the primary worktree still checks out the evidence branch, so all cleanup is deferred to Step 11.

- [ ] **Step 10: Monitor the evidence merge without creating recursive evidence**

Wait until the evidence merge's main CI and Release Please runs are terminal, then run:

```bash
evidence_pr_record=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state merged \
  --head codex/chore-dependency-governance-evidence \
  --limit 2 \
  --json number,state,headRefName,headRefOid,mergeCommit,url \
  --jq 'if length == 1 then .[0] | [(.number | tostring), .state, .headRefName, .headRefOid, (.mergeCommit.oid // ""), .url] | @tsv else empty end') || {
    printf 'failed to resolve merged evidence PR\n' >&2
    exit 1
  }
if [[ -z "${evidence_pr_record}" ]]; then
  printf 'expected exactly one merged evidence PR\n' >&2
  exit 1
fi
IFS=$'\t' read -r evidence_pr_number evidence_pr_state evidence_pr_head \
  evidence_pr_head_oid evidence_merge_sha evidence_pr_url <<<"${evidence_pr_record}"
if [[ "${evidence_pr_state}" != "MERGED" || \
      "${evidence_pr_head}" != "codex/chore-dependency-governance-evidence" || \
      ! "${evidence_pr_head_oid}" =~ ^[0-9a-fA-F]{40}$ || \
      ! "${evidence_merge_sha}" =~ ^[0-9a-fA-F]{40}$ || \
      -z "${evidence_pr_url}" ]]; then
  printf 'invalid merged evidence PR record: %s\n' "${evidence_pr_record}" >&2
  exit 1
fi

assert_evidence_push_run() {
  local workflow=$1
  local label=$2
  local run_record
  run_record=$(gh run list \
    --repo xiangnan0811/xirang \
    --workflow "${workflow}" \
    --branch main \
    --commit "${evidence_merge_sha}" \
    --event push \
    --limit 2 \
    --json databaseId,event,headSha,status,conclusion,url \
    --jq 'if length == 1 then .[0] | [(.databaseId | tostring), .event, .headSha, .status, (.conclusion // "NONE"), .url] | @tsv else empty end') || {
      printf 'failed to query %s run for evidence merge\n' "${label}" >&2
      return 1
    }
  if [[ -z "${run_record}" ]]; then
    printf 'expected exactly one %s push run for evidence merge\n' \
      "${label}" >&2
    return 1
  fi
  local run_id run_event run_head_sha run_status run_conclusion run_url
  IFS=$'\t' read -r run_id run_event run_head_sha run_status \
    run_conclusion run_url <<<"${run_record}"
  if [[ ! "${run_id}" =~ ^[0-9]+$ || "${run_event}" != "push" || \
        "${run_head_sha}" != "${evidence_merge_sha}" || \
        "${run_status}" != "completed" || \
        "${run_conclusion}" != "success" || -z "${run_url}" ]]; then
    printf '%s evidence-merge run failed assertion: %s\n' \
      "${label}" "${run_record}" >&2
    return 1
  fi
  printf '%s\t%s\t%s\n' "${label}" "${run_id}" "${run_url}"
}
assert_evidence_push_run ci.yml CI || exit 1
assert_evidence_push_run release-please.yml 'Release Please' || exit 1

evidence_publish_count=$(gh run list \
  --repo xiangnan0811/xirang \
  --workflow publish-images.yml \
  --commit "${evidence_merge_sha}" \
  --limit 2 --json databaseId --jq 'length') || {
  printf 'failed to query image publish runs for evidence merge\n' >&2
  exit 1
}
evidence_description_count=$(gh run list \
  --repo xiangnan0811/xirang \
  --workflow dockerhub-description.yml \
  --commit "${evidence_merge_sha}" \
  --limit 2 --json databaseId --jq 'length') || {
  printf 'failed to query description runs for evidence merge\n' >&2
  exit 1
}
if [[ "${evidence_publish_count}" != "0" || \
      "${evidence_description_count}" != "0" ]]; then
  printf 'unexpected publish automation for evidence merge: images=%s description=%s\n' \
    "${evidence_publish_count}" "${evidence_description_count}" >&2
  exit 1
fi
release_please_record=$(gh pr view 386 \
  --repo xiangnan0811/xirang \
  --json number,state,headRefName,url \
  --jq '[.number, .state, .headRefName, .url] | @tsv') || exit 1
IFS=$'\t' read -r release_pr_number release_pr_state release_pr_head release_pr_url \
  <<<"${release_please_record}"
if [[ "${release_pr_number}" != "386" || "${release_pr_state}" != "OPEN" || \
      "${release_pr_head}" != "release-please--branches--main" || \
      -z "${release_pr_url}" ]]; then
  printf 'Release Please PR changed after evidence merge: %s\n' \
    "${release_please_record}" >&2
  exit 1
fi
printf 'evidence_merge=%s\tpublish_images=0\tdescription_runs=0\trelease_pr=%s\n' \
  "${evidence_merge_sha}" "${release_pr_url}"
```

Expected: the evidence merge has exactly one successful main CI push run and one successful Release Please push run, no image publish or Docker Hub description run, and PR #386 remains protected. Record this final observation only in the final task/user handoff. Do not edit the tracked evidence, archive, task, or journal after the evidence PR merges; that would create an infinite evidence-PR loop.

- [ ] **Step 11: Sync primary `main` and conditionally clean the evidence branch**

```bash
primary_worktree=/home/murray/code/xirang
evidence_branch=codex/chore-dependency-governance-evidence
evidence_ref="refs/heads/${evidence_branch}"
evidence_pr_record=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state merged \
  --head "${evidence_branch}" \
  --limit 2 \
  --json number,state,headRefName,headRefOid,url \
  --jq 'if length == 1 then .[0] | [(.number | tostring), .state, .headRefName, .headRefOid, .url] | @tsv else empty end') || {
    printf 'failed to resolve merged evidence PR for cleanup\n' >&2
    exit 1
  }
if [[ -z "${evidence_pr_record}" ]]; then
  printf 'expected exactly one merged evidence PR for cleanup\n' >&2
  exit 1
fi
IFS=$'\t' read -r evidence_pr_number evidence_pr_state evidence_pr_head \
  evidence_pr_head_oid evidence_pr_url <<<"${evidence_pr_record}"
if [[ "${evidence_pr_state}" != "MERGED" || \
      "${evidence_pr_head}" != "${evidence_branch}" || \
      ! "${evidence_pr_head_oid}" =~ ^[0-9a-fA-F]{40}$ || \
      -z "${evidence_pr_url}" ]]; then
  printf 'invalid evidence PR cleanup record: %s\n' "${evidence_pr_record}" >&2
  exit 1
fi
current_branch=$(git -C "${primary_worktree}" branch --show-current) || exit 1
current_status=$(git -C "${primary_worktree}" status --porcelain) || exit 1
if [[ "${current_branch}" != "${evidence_branch}" || \
      -n "${current_status}" ]]; then
  printf 'primary worktree must be clean on the evidence branch before final sync\n' >&2
  exit 1
fi
git -C "${primary_worktree}" switch main || {
  printf 'failed to switch primary worktree back to main\n' >&2
  exit 1
}
git -C "${primary_worktree}" pull --ff-only origin main || {
  printf 'failed to fast-forward primary main after evidence PR merge\n' >&2
  exit 1
}
primary_head=$(git -C "${primary_worktree}" rev-parse HEAD) || exit 1
origin_main_head=$(git -C "${primary_worktree}" rev-parse origin/main) || exit 1
if [[ "${primary_head}" != "${origin_main_head}" ]]; then
  printf 'primary HEAD %s does not equal origin/main %s\n' \
    "${primary_head}" "${origin_main_head}" >&2
  exit 1
fi

evidence_symbolic_target=$(git -C "${primary_worktree}" symbolic-ref -q \
  "${evidence_ref}" 2>&1)
evidence_symbolic_status=$?
case "${evidence_symbolic_status}" in
  0)
    printf 'evidence ref is symbolic; refusing cleanup: %s -> %s\n' \
      "${evidence_ref}" "${evidence_symbolic_target}" >&2
    exit 1
    ;;
  1)
    ;;
  *)
    printf 'cannot inspect evidence ref symbolic state (exit %s): %s\n' \
      "${evidence_symbolic_status}" "${evidence_ref}" >&2
    exit 1
    ;;
esac
local_evidence_record=$(git -C "${primary_worktree}" show-ref --verify \
  "${evidence_ref}" 2>&1) || {
  printf 'cannot resolve exact local evidence ref: %s\n' \
    "${local_evidence_record}" >&2
  exit 1
}
IFS=' ' read -r local_evidence_oid local_evidence_ref \
  <<<"${local_evidence_record}"
if [[ ! "${local_evidence_oid}" =~ ^[0-9a-fA-F]{40}$ || \
      "${local_evidence_ref}" != "${evidence_ref}" || \
      "${local_evidence_oid}" != "${evidence_pr_head_oid}" ]]; then
  printf 'local evidence ref does not match merged PR head OID: %s\n' \
    "${local_evidence_record}" >&2
  exit 1
fi
remote_evidence_lookup=$(git -C "${primary_worktree}" ls-remote --heads origin \
  "${evidence_ref}" 2>&1)
remote_evidence_status=$?
if (( remote_evidence_status != 0 )); then
  printf 'cannot inspect remote evidence ref (exit %s):\n%s\n' \
    "${remote_evidence_status}" "${remote_evidence_lookup}" >&2
  exit 1
fi
remote_evidence_oid=
if [[ -n "${remote_evidence_lookup}" ]]; then
  IFS=$'\t' read -r remote_evidence_oid remote_evidence_ref \
    <<<"${remote_evidence_lookup}"
  if [[ ! "${remote_evidence_oid}" =~ ^[0-9a-fA-F]{40}$ || \
        "${remote_evidence_ref}" != "${evidence_ref}" || \
        "${remote_evidence_oid}" != "${evidence_pr_head_oid}" ]]; then
    printf 'remote evidence ref does not match merged PR head OID: %s\n' \
      "${remote_evidence_lookup}" >&2
    exit 1
  fi
fi

git -C "${primary_worktree}" update-ref --no-deref -d \
  "${evidence_ref}" "${evidence_pr_head_oid}" || {
  printf 'conditional local evidence ref deletion failed at OID %s\n' \
    "${evidence_pr_head_oid}" >&2
  exit 1
}
evidence_config_section="branch.${evidence_branch}"
evidence_config_entries=$(git -C "${primary_worktree}" config --local --get-regexp \
  "^branch\\.${evidence_branch}\\." 2>&1)
evidence_config_status=$?
case "${evidence_config_status}" in
  0)
    git -C "${primary_worktree}" config --local --remove-section \
      "${evidence_config_section}" || {
      printf 'failed to remove exact evidence branch config section\n' >&2
      exit 1
    }
    ;;
  1)
    ;;
  *)
    printf 'cannot inspect exact evidence branch config section (exit %s):\n%s\n' \
      "${evidence_config_status}" "${evidence_config_entries}" >&2
    exit 1
    ;;
esac
if [[ -n "${remote_evidence_oid}" ]]; then
  git -C "${primary_worktree}" push \
    --force-with-lease="${evidence_ref}:${evidence_pr_head_oid}" \
    origin --delete "${evidence_branch}" || {
    printf 'leased remote evidence ref deletion failed at OID %s\n' \
      "${evidence_pr_head_oid}" >&2
    exit 1
  }
fi
git -C "${primary_worktree}" fetch origin --prune || exit 1
final_primary_branch=$(git -C "${primary_worktree}" branch --show-current) || exit 1
final_primary_status=$(git -C "${primary_worktree}" status --porcelain) || exit 1
if [[ "${final_primary_branch}" != "main" || \
      -n "${final_primary_status}" ]]; then
  printf 'primary worktree is not clean on main after evidence cleanup\n' >&2
  exit 1
fi
```

Expected: only after the evidence PR is merged and its final automation is observed does the primary worktree switch back to `main`, fast-forward, and prove `HEAD == origin/main`. The exact evidence ref must be non-symbolic and equal the merged PR's full `headRefOid`; local deletion uses `update-ref --no-deref` with that old OID, only the exact branch config section is removed, and any present remote ref is deleted with the same expected-OID lease. Errors and moved refs abort. No tracked write occurs after the evidence merge.

- [ ] **Step 12: Remove the governance worktree before branch cleanup**

Run only after the evidence PR is merged, its post-merge automation is recorded in the untracked final handoff, primary `main` is synchronized, and the evidence branch cleanup is complete:

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
- After evidence branch creation: if no live operation has started, return to main and conditionally delete only the unpushed exact evidence ref; otherwise retain the branch and complete the same evidence PR. After the evidence PR merges, its final automation observation belongs only in the final handoff, followed by conditional evidence-ref cleanup and then governance-worktree cleanup.

## Requirement Coverage

| Requirements | Implementation |
|---|---|
| R1-R5, AC1-AC2 | Task 1 config contract, failing/passing structural assertion, protected-file check; Task 5 three successful jobs, complete live bot enumeration, per-row shape/unique-group checks, and exact normalized job/live PR-set equality |
| R6-R7, AC3 | Task 6 independently guarded settings enablement, asserted read-only API state, bot PR/open-alert inspection, exact `NONE`-or-child-path R7 manifest resolution, and same-list evidence capture; Task 7 rejects unresolved, duplicate, non-child, missing, or content-free follow-up paths |
| R8-R9, AC4 | Task 2 trigger-only change, failing/passing assertion, actionlint; Task 7 exact merge-SHA CI run cardinality/status/conclusion assertion |
| R10, AC6 | Task 5 immutable allowlist validation, OID-conditioned exact PR/branch cleanup, exactly three Web UI jobs, baseline IDs, three successful logs, independently derived job/live PR sets, and exact equality before Task 6 |
| R11, AC7 | Task 5 pre-trigger and Task 7 post-merge asserted PR #386 preservation checks with URL evidence |
| R12, AC5 | Tasks 1 and 3 protected-file checks, actionlint, diff hygiene, remote required CI |
| AC8 | Task 7 exact merge-SHA Release Please success assertion and zero associated publish/description run assertions |
| R13, AC9 | Task 0 explicit Trellis config preference and generated Codex integration immutability checks; Task 3 durable project guidance |
| R14, AC10 | Execution Setup ignore verification and project-local worktree creation; Task 3 durable project guidance |
| R15, AC11 | Task 5 synchronized-main evidence branch authorization plus exact evidence/manifest/base-OID initialization; Task 7 pre- and post-commit Phase 3.4 allowlist audits, required committed-path/content checks, base-parent assertion, automatic archive/journal commits with exact subjects/parents/diff allowlists and work-only journal hash, exact three-commit `base..HEAD` audits, audited-journal exact refspec with absent-ref lease/read-back, pre-CI and pre-merge PR `headRefOid` equality plus expected-head merge guard, non-recursive final automation handoff, conditional evidence-ref cleanup, then governance worktree cleanup |
