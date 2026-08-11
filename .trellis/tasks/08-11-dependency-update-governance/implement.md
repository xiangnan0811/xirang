# Dependency Update Governance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace noisy weekly single-dependency PRs with monthly grouped minor/patch updates, keep security updates immediate, and prevent duplicate CI runs for pull-request commits.

**Architecture:** Project execution preference is persisted through the canonical Trellis Codex dispatch setting; repository automation changes are limited to `.github/dependabot.yml` and the CI event block in `.github/workflows/ci.yml`. After the configuration PR is green and merged, the migration closes only the pre-captured version-update PRs, enables security settings through GitHub APIs, and verifies post-merge automation before Trellis archival.

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

- [ ] **Step 6: Squash merge and delete the work branch**

```bash
gh pr merge --squash --delete-branch
```

Expected: merge succeeds only after required checks pass.

## Task 5: Perform The Exact Post-Merge PR Cleanup

**Files:** Use `.trellis/tasks/08-11-dependency-update-governance/research/open-dependabot-prs-2026-08-11.md` as the immutable allowlist.

- [ ] **Step 1: Sync local main and resolve the governance PR number**

```bash
git switch main
git pull --ff-only origin main
governance_pr_number=$(gh pr list \
  --repo xiangnan0811/xirang \
  --state merged \
  --head codex/chore-dependency-governance \
  --limit 1 \
  --json number \
  --jq '.[0].number')
gh pr view "${governance_pr_number}" \
  --repo xiangnan0811/xirang \
  --json number,url,state,mergedAt,mergeCommit
```

Expected: local `main` equals `origin/main`; governance PR state is `MERGED`.

- [ ] **Step 2: Revalidate every captured PR before closing anything**

Run this exact allowlist validation:

```bash
while IFS='|' read -r pr_number expected_head; do
  actual=$(gh pr view "${pr_number}" \
    --repo xiangnan0811/xirang \
    --json state,author,headRefName \
    --jq '[.state, .author.login, .headRefName] | @tsv')
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
    --comment "Superseded by the merged monthly grouped dependency-update policy. Security updates are handled separately and will be enabled and verified in the next migration step."
done
```

Expected: only the 13 captured version-update PRs become closed. PR #386 remains open.

- [ ] **Step 4: Verify PR and branch cleanup**

```bash
gh pr list --repo xiangnan0811/xirang --state open --limit 200 --json number,author,headRefName,title,url
git ls-remote --heads origin 'refs/heads/dependabot/*'
```

Expected: no captured PR remains open; no captured Dependabot head remains unless GitHub reports an external cleanup delay. Any remaining branch must be matched by exact name to a closed captured PR before considering explicit deletion.

- [ ] **Step 5: Verify Release Please was preserved**

```bash
gh pr view 386 --repo xiangnan0811/xirang --json number,state,headRefName,title,url
```

Expected: state `OPEN`, head `release-please--branches--main`.

## Task 6: Enable And Verify Security Updates

**Files:** No repository files; GitHub repository settings only.

- [ ] **Step 1: Enable vulnerability alerts**

```bash
gh api --method PUT repos/xiangnan0811/xirang/vulnerability-alerts
```

Expected: HTTP 204 success.

- [ ] **Step 2: Enable automated security fixes**

```bash
gh api --method PUT repos/xiangnan0811/xirang/automated-security-fixes
```

Expected: HTTP 204 success.

- [ ] **Step 3: Verify both settings through read-only APIs**

```bash
gh api --include repos/xiangnan0811/xirang/vulnerability-alerts
gh api repos/xiangnan0811/xirang/automated-security-fixes
```

Expected: vulnerability-alert request returns HTTP 204 and automated security fixes returns `{"enabled":true,"paused":false}`.

- [ ] **Step 4: Inspect newly opened bot PRs without closing them**

```bash
gh pr list --repo xiangnan0811/xirang --state open --author 'app/dependabot' --limit 200 --json number,title,headRefName,createdAt,url
```

Expected: any newly created security PR remains open. Routine version PRs, when generated, follow the approved group names and no more than four version-update groups.

- [ ] **Step 5: Inspect open security alerts for manual-major follow-up**

```bash
gh api --method GET --paginate repos/xiangnan0811/xirang/dependabot/alerts \
  --field state=open \
  --jq '.[] | [.number, .dependency.package.ecosystem, .dependency.package.name, .security_advisory.severity, (.security_vulnerability.first_patched_version.identifier // "no-automatic-patch")] | @tsv'
```

Expected: the command succeeds. For any open alert without an automatically created fix PR, record the package, severity, and first patched version in task evidence; create a separate high-priority Trellis upgrade task when the fix requires a major-version compatibility change.

## Task 7: Verify Post-Merge Automation And Finish Trellis

**Files:** Trellis task archive and developer journal as directed by `trellis-finish-work`.

- [ ] **Step 1: Verify the main push CI run**

```bash
gh run list --workflow ci.yml --branch main --limit 5 --json databaseId,event,headSha,status,conclusion,url,createdAt
```

Expected: the governance merge commit has one `push` CI run; it completes successfully.

- [ ] **Step 2: Inspect Release Please and publishing workflows**

```bash
gh run list --workflow release-please.yml --branch main --limit 5 --json databaseId,event,status,conclusion,url,createdAt
gh run list --workflow publish-images.yml --limit 5 --json databaseId,event,status,conclusion,url,createdAt
gh run list --workflow dockerhub-description.yml --limit 5 --json databaseId,event,status,conclusion,url,createdAt
```

Expected: Release Please may update PR #386. No formal GitHub Release, Docker image publication, or Docker Hub description sync is expected because this task does not merge the release PR or publish a release.

- [ ] **Step 3: Complete acceptance evidence**

Update the task PRD/check evidence with:

- config assertions and actionlint results;
- governance PR URL and merge commit;
- CI run URL and conclusion;
- exact old-PR closure results and remaining remote heads;
- security-setting API verification;
- Release Please and no-release/no-publish conclusion.

- [ ] **Step 4: Run `trellis-finish-work`**

Use the project finish workflow to verify the quality gate, archive the completed task, update the developer journal, and route any archive commit through a dedicated follow-up branch and PR if required by the repository workflow.

- [ ] **Step 5: Sync main after all merged follow-up work**

```bash
git switch main
git pull --ff-only origin main
git status --short --branch
```

Expected: clean `main` tracking `origin/main` with no local-only commits.

## Rollback Points

- Before governance PR merge: close the governance PR and delete only `codex/chore-dependency-governance`; no repository settings or old PRs have changed.
- After merge but before cleanup: revert the merge through a new PR; old Dependabot PRs remain available.
- After old PR cleanup: reopen only exact captured PR numbers if rollback is necessary, but prefer waiting for regrouped PRs.
- After security enablement: keep vulnerability alerts enabled; automated security fixes may be paused or disabled separately if an external incident requires it.

## Requirement Coverage

| Requirements | Implementation |
|---|---|
| R1-R5, AC1-AC2 | Task 1 config contract, failing/passing structural assertion, protected-file check |
| R6-R7, AC3 | Task 6 settings enablement, API verification, bot PR inspection, open-alert inspection |
| R8-R9, AC4 | Task 2 trigger-only change, failing/passing assertion, actionlint; Task 7 main-run verification |
| R10, AC6 | Task 5 immutable allowlist validation, exact PR closure, remote-head verification |
| R11, AC7 | Task 5 explicit PR #386 preservation check |
| R12, AC5 | Tasks 1 and 3 protected-file checks, actionlint, diff hygiene, remote required CI |
| AC8 | Task 7 Release Please and publish-workflow inspection with explicit no-release conclusion |
| R13, AC9 | Task 0 explicit Trellis config preference and generated Codex integration immutability checks; Task 3 durable project guidance |
| R14, AC10 | Execution Setup ignore verification and project-local worktree creation; Task 3 durable project guidance |
