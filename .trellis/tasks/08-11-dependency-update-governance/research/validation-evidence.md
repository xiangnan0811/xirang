# Dependency Update Governance Validation Evidence

Captured on 2026-08-11 from
`/home/murray/code/xirang/.worktrees/dependency-update-governance` on branch
`codex/chore-dependency-governance`.

This file records local validation only. Remote CI, GitHub repository settings,
legacy PR cleanup, and post-merge automation remain assigned to the later tasks
identified in the requirement mapping.

## Commands And Results

### 1. Trellis dispatch structural assertion

```bash
ruby -ryaml -e 'config = YAML.safe_load(File.read(".trellis/config.yaml")); abort "Codex dispatch preference must be explicit" unless config.dig("codex", "dispatch_mode") == "sub-agent"; puts "Codex sub-agent dispatch preference OK"'
```

- Exit code: `0`
- Output: `Codex sub-agent dispatch preference OK`

### 2. Dependabot governance structural assertion

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

- Exit code: `0`
- Output: `Dependabot governance config OK`
- Contract covered: monthly `03:00` `Asia/Shanghai` schedules, 1/2/1
  limits, minor/patch allow rules, four version-update groups, wildcard
  patterns, and production/development npm dependency types.

### 3. Strict supplemental Dependabot assertion

```bash
ruby -ryaml -e '
config = YAML.safe_load(File.read(".github/dependabot.yml"))
updates = config.fetch("updates")
abort "expected exactly three ecosystem entries" unless updates.length == 3
ecosystems = updates.map { |entry| entry.fetch("package-ecosystem") }
abort "duplicate ecosystem entries" unless ecosystems.uniq.length == ecosystems.length
expected = {
  "gomod" => {
    "directory" => "/backend",
    "labels" => ["dependencies", "go"],
    "groups" => ["go-minor-patch"]
  },
  "npm" => {
    "directory" => "/web",
    "labels" => ["dependencies", "javascript"],
    "groups" => ["npm-development-minor-patch", "npm-production-minor-patch"]
  },
  "github-actions" => {
    "directory" => "/",
    "labels" => ["dependencies", "ci"],
    "groups" => ["actions-minor-patch"]
  }
}
expected_allow = {
  "dependency-name" => "*",
  "update-types" => [
    "version-update:semver-minor",
    "version-update:semver-patch"
  ]
}
abort "unexpected ecosystem set" unless ecosystems.sort == expected.keys.sort
updates.each do |entry|
  ecosystem = entry.fetch("package-ecosystem")
  contract = expected.fetch(ecosystem)
  abort "#{ecosystem}: directory" unless entry.fetch("directory") == contract.fetch("directory")
  abort "#{ecosystem}: labels" unless entry.fetch("labels") == contract.fetch("labels")
  abort "#{ecosystem}: group keys" unless entry.fetch("groups").keys.sort == contract.fetch("groups")
  allow = entry.fetch("allow")
  abort "#{ecosystem}: allow rule count" unless allow.length == 1
  rule = allow.fetch(0)
  normalized_rule = rule.merge("update-types" => rule.fetch("update-types").sort)
  abort "#{ecosystem}: exact allow rule" unless normalized_rule == expected_allow
end
puts "Strict Dependabot governance config OK"
'
```

- Exit code: `0`
- Output: `Strict Dependabot governance config OK`
- Contract covered: exactly three unique ecosystem entries; exact `/backend`,
  `/web`, and `/` directories; preserved labels; exact group key names and
  cardinality; and exactly one complete allow rule per ecosystem containing
  only `dependency-name: "*"` and minor/patch version update types (type order
  is normalized before the complete hash comparison).

### 4. CI trigger assertion

```bash
ruby -ryaml -e 'workflow = YAML.safe_load(File.read(".github/workflows/ci.yml")); events = workflow.fetch(true); abort "push must target only main" unless events.fetch("push") == {"branches" => ["main"]}; abort "pull_request trigger missing" unless events.key?("pull_request"); puts "CI trigger config OK"'
```

- Exit code: `0`
- Output: `CI trigger config OK`

### 5. Workflow lint

```bash
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/*.yml
```

- Exit code: `0`
- Output: none (no findings).

### 6. Diff hygiene against the branch range

```bash
git diff --check origin/main...HEAD
```

- Exit code: `0`
- Output: none.

### 7. Diff hygiene for uncommitted changes

```bash
git diff --check
```

- Exit code: `0`
- Output: none.

### 8. PR title validation

```bash
bash scripts/check-pr-title.sh "chore(deps): govern dependency update automation"
```

- Exit code: `0`
- Output: `PR title is valid: chore(deps): govern dependency update automation`

### 9. Protected-path validation

```bash
git diff --exit-code origin/main -- backend/go.mod backend/go.sum web/package.json web/package-lock.json .github/workflows/release-please.yml .codex/agents .codex/hooks.json .codex/config.toml
```

- Exit code: `0`
- Output: none; dependency manifests, lock files, Release Please workflow,
  generated Codex agents, hooks, and Codex config are unchanged.

### 10. Worktree convention

```bash
git check-ignore -v .worktrees/
```

- Exit code: `0`
- Output: `.gitignore:37:.worktrees/\t.worktrees/`

### 11. Trellis context validation before evidence creation

```bash
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-11-dependency-update-governance
```

- Exit code: `0`
- Relevant output:

```text
implement.jsonl: ✓ (5 entries)
check.jsonl: ✓ (5 entries)
✓ All validations passed
```

### 12. Full repository quality gate

```bash
make check
```

- Exit code: `0`
- Relevant output:
  - Backend lint: `0 issues.`
  - Frontend lint: `1 problem (0 errors, 1 warning)`; warning recorded below.
  - Backend `go test ./...`: all tested packages passed; packages without tests
    reported `[no test files]`.
  - Frontend Vitest: `168 passed` test files and `1388 passed` tests.
  - Frontend coverage: statements `70.02%`, branches `62.07%`, functions
    `65.84%`, lines `72.58%`.
  - Backend build completed successfully.
  - Frontend production build transformed 3214 modules and completed
    successfully (`built in 4.71s` on the required run).

The required unwrapped command was followed by this timed rerun of the same
gate:

```bash
TIMEFMT='real %E'; time make check
```

- Exit code: `0`
- Measured duration: `69.57s`
- The timed rerun reproduced the same pass counts and warning.
- Both full-gate runs generated the expected untracked
  `backend/xirang-server`; it was removed after each run.

### 13. Trellis context validation after evidence creation

```bash
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-11-dependency-update-governance
```

- Exit code: `0`
- Relevant output:

```text
implement.jsonl: ✓ (5 entries)
check.jsonl: ✓ (5 entries)
✓ All validations passed
```

### 14. Diff hygiene including the untracked evidence file

```bash
git diff --cached --name-status
```

- Exit code: `0`
- Output: none; the index was clean before the temporary intent entry.

```bash
git add --intent-to-add .trellis/tasks/08-11-dependency-update-governance/research/validation-evidence.md
```

- Exit code: `0`
- Output: none.

```bash
git diff --check
```

- Exit code: `0`
- Output: none; intent-to-add made the untracked evidence file part of the
  whitespace check.

```bash
git reset -- .trellis/tasks/08-11-dependency-update-governance/research/validation-evidence.md
```

- Exit code: `0`
- Output:

```text
Unstaged changes after reset:
M	.trellis/spec/guides/branch-workflow-guidelines.md
```

```bash
git diff --cached --name-status
```

- Exit code: `0`
- Output: none; no pre-existing or temporary staged state remained.

```bash
git status --short .trellis/tasks/08-11-dependency-update-governance/research/validation-evidence.md
```

- Exit code: `0`
- Output:

```text
?? .trellis/tasks/08-11-dependency-update-governance/research/validation-evidence.md
```

### 15. Final worktree status inspection

```bash
git status --short
```

- Exit code: `0`
- Output at inspection time:

```text
 M .trellis/spec/guides/branch-workflow-guidelines.md
?? .trellis/tasks/08-11-dependency-update-governance/research/validation-evidence.md
```

## Existing Non-Failing Warnings

The full gate reported one ESLint warning and no lint errors:

```text
web/src/features/backup-assets/export-job-panel.tsx
  195:17  warning  `tabIndex` should only be declared on interactive elements  jsx-a11y/no-noninteractive-tabindex
```

This warning did not fail `make check`. No other warning was reported by the
local gate.

## Requirement Mapping

| Requirement | Implementation / evidence | Status |
|---|---|---|
| R1 | Task 1 monthly `03:00` `Asia/Shanghai` schedules; assertions 2-3 | Local contract passed |
| R2 | Task 1 four ecosystem/dependency-type groups; assertions 2-3 | Local contract passed |
| R3 | Task 1 minor/patch-only routine allow rules plus a durable guide requirement that ordinary major upgrades use dedicated tasks with compatibility research, full validation, and upstream release-note review | Local contract passed |
| R4 | Task 1 1/2/1 PR limits aligned to the four groups; assertion 2 | Local contract passed |
| R5 | Task 1 contains no auto-merge policy; Task 2 preserves PR CI review flow | Local contract passed |
| R6 | Task 6 enables and verifies vulnerability alerts and automated security fixes | **Pending Task 6** |
| R7 | Task 6 inspects security PRs and alerts and creates manual high-priority major-upgrade follow-up where needed | **Pending Task 6** |
| R8 | Task 2 keeps `pull_request` and limits `push` to `main`; assertion 4 and actionlint pass | Local trigger contract passed; remote behavior is covered by AC4 below |
| R9 | Task 2 changes only the CI event restriction and adds no path filters or job reductions; actionlint passes | Local contract passed |
| R10 | Task 5 revalidates and closes only the captured 13-PR number/head allowlist | **Pending Task 5** |
| R11 | Protected-path check preserves the Release Please workflow; Task 5 must verify PR #386 remains open and unchanged | **Pending Task 5 live verification** |
| R12 | Protected-path command exits 0 for all dependency manifests, lock files, Release Please workflow, and action/Codex protected paths | Local contract passed |
| R13 | Task 0 sets `codex.dispatch_mode: sub-agent`; assertion 1 and protected Codex-path check pass; durable guide records the default | Local contract passed |
| R14 | Work is in `.worktrees/dependency-update-governance`; assertion 10 verifies the ignore rule; durable guide records `.worktrees/<task-slug>` | Local contract passed |

## Acceptance-Criteria Mapping

| Acceptance criterion | Implementation / evidence | Status |
|---|---|---|
| AC1 | Task 1 Dependabot config; assertions 2-3 validate schedule, timezone, groups, major boundary, and limits | Local contract passed |
| AC2 | Task 1 exact group keys and 1/2/1 limits; assertions 2-3 cap routine maintenance at four groups | Local contract passed |
| AC3 | Task 6 GitHub API enablement and read-only verification for vulnerability alerts and automated security fixes | **Pending Task 6** |
| AC4 | Assertion 4 proves the local event contract. Task 7 must still observe the post-merge `main` push and confirm one push CI run | **Local trigger contract passed; post-merge observation pending Task 7** |
| AC5 | YAML structural assertions, actionlint, diff hygiene, title validation, protected paths, Trellis validation, and `make check` all pass locally. Task 4 must still obtain all required remote CI | **Local checks passed; remote required CI pending Task 4** |
| AC6 | Task 5 exact allowlist validation, closure of only the 13 captured legacy Dependabot PRs, and remote-head verification | **Pending Task 5** |
| AC7 | Protected-path check shows no Release Please workflow change. Task 5 must still verify PR #386 remains open with head `release-please--branches--main` | **Pending Task 5 live verification** |
| AC8 | Task 7 inspects Release Please and publishing workflows and records the expected no-release/no-publish result | **Pending Task 7 post-merge observation** |
| AC9 | Assertion 1 verifies explicit sub-agent dispatch; command 9 verifies `.codex/agents`, hooks, and Codex config are unchanged | Local contract passed |
| AC10 | Command 10 verifies `.worktrees/` remains ignored; the durable guide records the local path; this task ran in the required isolated worktree | Local contract passed |
