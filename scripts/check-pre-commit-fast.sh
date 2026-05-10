#!/usr/bin/env bash
# Fast staged checks used by .githooks/pre-commit.

set -euo pipefail

if ! ROOT_DIR="$(git rev-parse --show-toplevel 2>/dev/null)"; then
  echo "[FAIL] 无法定位 Git 仓库根目录" >&2
  exit 2
fi

echo "[INFO] pre-commit 快检: git diff --check --cached"
if ! git -C "$ROOT_DIR" diff --check --cached; then
  echo "" >&2
  echo "[FAIL] staged diff 存在空白错误。请修复后重新 git add。" >&2
  exit 1
fi

go_files=()
while IFS= read -r -d '' file; do
  go_files+=("$file")
done < <(git -C "$ROOT_DIR" diff --cached --name-only -z --diff-filter=ACMR -- '*.go')

if (( ${#go_files[@]} == 0 )); then
  echo "[INFO] pre-commit 快检通过"
  exit 0
fi

if ! command -v gofmt >/dev/null 2>&1; then
  echo "[FAIL] 检测到 staged Go 文件，但 gofmt 不可用。请安装 Go 工具链。" >&2
  exit 2
fi

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

unformatted=()
syntax_failed=()
idx=0

for file in "${go_files[@]}"; do
  idx=$((idx + 1))
  staged_file="${tmp_dir}/staged-${idx}.go"
  formatted_file="${tmp_dir}/formatted-${idx}.go"

  if ! git -C "$ROOT_DIR" show ":$file" > "$staged_file"; then
    echo "[FAIL] 无法读取 staged Go 文件: $file" >&2
    exit 1
  fi

  if ! gofmt "$staged_file" > "$formatted_file"; then
    syntax_failed+=("$file")
    continue
  fi

  if ! cmp -s "$staged_file" "$formatted_file"; then
    unformatted+=("$file")
  fi
done

if (( ${#syntax_failed[@]} > 0 )); then
  echo "" >&2
  echo "[FAIL] 以下 staged Go 文件无法通过 gofmt 解析:" >&2
  for file in "${syntax_failed[@]}"; do
    echo "  - $file" >&2
  done
  exit 1
fi

if (( ${#unformatted[@]} > 0 )); then
  echo "" >&2
  echo "[FAIL] 以下 staged Go 文件未通过 gofmt 检查:" >&2
  for file in "${unformatted[@]}"; do
    echo "  - $file" >&2
  done
  echo "请运行 gofmt 并重新 git add 后再提交。" >&2
  exit 1
fi

echo "[INFO] pre-commit 快检通过"
