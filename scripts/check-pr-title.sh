#!/usr/bin/env bash

set -euo pipefail

title="${1:-${PR_TITLE:-}}"
pattern='^(build|chore|ci|docs|feat|fix|perf|refactor|revert|security|style|test)(\([^)]+\))?(!)?: .*[^[:space:]]$'

if [[ -z "${title}" ]]; then
  echo "pull request title is empty" >&2
  exit 1
fi

if [[ "${title}" =~ ${pattern} ]]; then
  echo "PR title is valid: ${title}"
  exit 0
fi

cat >&2 <<EOF
pull request title must follow Conventional Commits:

  <type>(<scope>): <description>
  <type>: <description>
  <type>(<scope>)!: <description>

Examples:
  feat(web): add release badge
  fix(backend): correct version check parsing
  docs: document Docker Hub upgrade path

Received:
  ${title}
EOF

exit 1
