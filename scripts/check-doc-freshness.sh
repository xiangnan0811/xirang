#!/usr/bin/env bash
# --staged 和 --candidate 阻断主题同步；普通模式的主题同步只提醒。
set -euo pipefail
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
source "$ROOT_DIR/scripts/doc-freshness-rules.sh"

if [[ "${1:-}" == "--candidate" ]]; then
  if [[ $# != 2 || -z "$2" ]] || ! BASE="$(git -C "$ROOT_DIR" rev-parse --verify --end-of-options "$2^{commit}" 2>/dev/null)"; then
    echo 'usage: check-doc-freshness.sh --candidate <valid commit>' >&2
    exit 2
  fi
fi

if [[ "${1:-}" == "--staged" ]]; then
  CHANGED="$(git -C "$ROOT_DIR" diff --cached --name-only)"
  UPDATED="$(git -C "$ROOT_DIR" diff --cached --name-only --diff-filter=ACMR)"
else
  bash "$ROOT_DIR/scripts/check-migration-version.sh"
  python3 "$ROOT_DIR/scripts/check-doc-structure.py"
  if [[ "${1:-}" == "--candidate" ]]; then
    # Check the two release paths against the baseline without staging files
    # or running Git clean filters. Other topic rules still run in the hook.
    export GIT_OPTIONAL_LOCKS=0
    release_doc_hash() {
      python3 -c 'import sys; sys.stdout.buffer.write(sys.stdin.buffer.read().replace(b"\r\n", b"\n"))' |
        git -C "$ROOT_DIR" hash-object --stdin
    }
    CHANGED=""
    UPDATED=""
    for PATH_ENTRY in CHANGELOG.md docs/maintainers/release.md; do
      BEFORE=""
      if git -C "$ROOT_DIR" cat-file -e "$BASE:$PATH_ENTRY" 2>/dev/null; then
        BEFORE="$(git -C "$ROOT_DIR" cat-file blob "$BASE:$PATH_ENTRY" | release_doc_hash)"
      fi
      AFTER=""
      TRACKED="$(git -C "$ROOT_DIR" ls-files -t -- "$PATH_ENTRY")"
      if [[ -z "$TRACKED" ]] && git -C "$ROOT_DIR" check-ignore -q -- "$PATH_ENTRY"; then
        : # An ignored local copy is absent from the candidate.
      elif [[ -L "$ROOT_DIR/$PATH_ENTRY" ]]; then
        # A symlink can trigger synchronization but cannot satisfy it.
        CHANGED+="$PATH_ENTRY"$'\n'
        continue
      elif [[ -f "$ROOT_DIR/$PATH_ENTRY" ]]; then
        AFTER="$(release_doc_hash < "$ROOT_DIR/$PATH_ENTRY")"
      elif [[ "$TRACKED" == 'S '* ]]; then
        # An intentionally sparse omission retains the current index blob.
        AFTER="$(git -C "$ROOT_DIR" cat-file blob ":$PATH_ENTRY" | release_doc_hash)"
      fi
      if [[ "$BEFORE" != "$AFTER" ]]; then
        CHANGED+="$PATH_ENTRY"$'\n'
        if [[ -n "$AFTER" ]]; then
          UPDATED+="$PATH_ENTRY"$'\n'
        fi
      fi
    done
  elif [[ -n "${GITHUB_BASE_REF:-}" ]]; then
    CHANGED="$(git -C "$ROOT_DIR" diff --name-only "origin/${GITHUB_BASE_REF}...HEAD")"
    UPDATED="$(git -C "$ROOT_DIR" diff --name-only --diff-filter=ACMR "origin/${GITHUB_BASE_REF}...HEAD")"
  elif git -C "$ROOT_DIR" rev-parse HEAD~1 >/dev/null 2>&1; then
    CHANGED="$(git -C "$ROOT_DIR" diff --name-only HEAD~1)"
    UPDATED="$(git -C "$ROOT_DIR" diff --name-only --diff-filter=ACMR HEAD~1)"
  else
    CHANGED=""
    UPDATED=""
  fi
fi

doc_freshness_check "$CHANGED" "$UPDATED"
if (( DOC_FRESHNESS_WARNINGS > 0 )); then
  echo "文档同步提醒：${DOC_FRESHNESS_WARNINGS} 项，请核对对应主题。"
  [[ "${1:-}" != "--staged" && "${1:-}" != "--candidate" ]] || exit 1
else
  echo "未发现缺少必需主题文档的路径信号；导航提醒仍需人工核对"
fi
