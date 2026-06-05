# Cleanup Workspace

## Description

Clean up the development workspace after a major phase of work, ensuring the repository is ready for the next task.

## When to Use

- After merging multiple PRs to main
- After completing a Trellis task tree
- After a feature/refactoring phase
- When the user asks to "clean up" or "tidy up"
- Before starting a new phase of work

## Procedure

### Step 1: Assess current state

DO NOT blindly switch branches. First understand where we are:

```bash
git branch --show-current
git status --short
git status --short --untracked-files=normal | grep "^?" | wc -l  # untracked file count
```

Decision tree:

| Current state | Action |
|--------------|--------|
| Detached HEAD | Resolve first (`git checkout main` or the original branch) |
| On `main`, working tree clean, no untracked files | Proceed to Step 2 |
| On `main`, working tree dirty or untracked files | Stash/commit/add-to-gitignore first, then proceed |
| On a work branch, working tree clean | Check: does this branch have an open PR? Has it been merged? Decide accordingly |
| On a work branch, working tree dirty | Stash or commit changes, then re-assess |

**If there are uncommitted changes or untracked files**: ask the user what to do. Do NOT assume "stash" is always correct — the user may want to keep the work.

### Step 2: Update main

Only after confirming the working tree is clean:

```bash
git checkout main && git pull --ff-only
```

Verify the pull succeeded:
```bash
git log --oneline -1  # should match origin/main
```

If `git pull --ff-only` fails (e.g., local diverged from remote), stop and ask the user.

### Step 3: Clean stale PRs and remote tracking

```bash
# Check for open PRs with no corresponding local branch
gh pr list --state open --limit 50

# Prune remote tracking refs for deleted remote branches
git remote prune origin
```

Close any stale PRs where the code is already in main (check with `git diff` as in Step 4).

### Step 4: Analyze local branches

For each local branch (excluding `main`), determine if it can be safely deleted:

```bash
for branch in $(git branch --list | sed 's/^[* ]*//' | grep -v "^main$"); do
  # Quick check: git cherry (preferred over git rev-list for content detection)
  unchers=$(git cherry main "$branch" 2>/dev/null | grep "^+" | wc -l | tr -d ' ')
  
  if [ "$unchers" -eq 0 ]; then
    echo "SAFE: $branch (all commits already in main)"
  else
    # git cherry reports unmerged — verify with actual content diff
    has_code_diff=0
    for f in $(git diff --name-only main..."$branch" 2>/dev/null | grep -v "^.trellis/" | grep -v "^.githooks/"); do
      if [ "$(git diff main "$branch" -- "$f" 2>/dev/null | wc -l | tr -d ' ')" -gt 0 ]; then
        echo "  UNMERGED: $f"; has_code_diff=1; break
      fi
    done
    
    if [ "$has_code_diff" -eq 0 ]; then
      echo "SAFE: $branch (code already in main, only .trellis/ or process files differ)"
    else
      echo "KEEP: $branch (has unmerged code changes — needs PR)"
    fi
  fi
done
```

Rules:
1. **git cherry "-"** = safe to delete (commit already in main).
2. **git cherry "+" + no code diffs** = safe to delete (squash-merged, or only `.trellis/` files differ).
3. **git cherry "+" + code diffs exist** = keep, create PR.
4. **`.trellis/` and `.githooks/` diffs do NOT count** — per "No process docs in git", these should not be merged.

### Step 5: Delete safe branches

```bash
git branch -D <branch1> <branch2> ...
```

Verify:
```bash
git branch --list  # should show only main + any kept branches
```

### Step 6: Create PRs for unmerged branches

Only for branches with actual code diffs not in main:

1. Push: `git push -u origin <branch>`
2. Verify CI passes: `gh pr checks`
3. Fix failures on the same branch if needed
4. Create PR: `gh pr create --base main --head <branch>`
5. Monitor CI → merge per E2E PR workflow

### Step 7: Verify final state

```bash
git branch --show-current         # must be "main"
git status --short                # must be empty (no modified files)
git status --short --untracked-files=normal | grep "^?" | wc -l  # note untracked count
git branch --list                 # 1 (main) or main + actively-needed branches
git log --oneline -1              # main should match origin/main
```

Also verify:
- Trellis tasks: `python3 ./.trellis/scripts/task.py list --mine` — no stale tasks
- Claude Code tasks: complete or delete stale pending tasks
- Open PRs: review any remaining open PRs

### Step 8: Report

Summarize what was cleaned and what remains:
- Branches deleted and why
- Branches kept and why
- PRs created
- Any items needing user attention

## Important Rules

- **Step 1 always comes first** — assess before acting. Never assume the current state.
- **Never delete branches you didn't create** without confirming via content diff
- **Never trust `git rev-list` alone** — squash merges change commit SHA. Always verify with `git diff`.
- **Do NOT delete branches with actual unmerged code** — create PRs for them
- **`.trellis/` and `.githooks/` diffs do NOT count as unmerged code** — these are process files, not for main
- **Keep per "Never commit directly to main"** — even for cleanup fixes, create a feature branch
