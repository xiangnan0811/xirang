#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SMOKE="$ROOT_DIR/scripts/test-asset-worker.sh"
BASE_DOCKERFILE="$ROOT_DIR/deploy/worker/Dockerfile"
BASE_ENTRYPOINT="$ROOT_DIR/deploy/worker/entrypoint.sh"
BASE_SECCOMP="$ROOT_DIR/deploy/worker/seccomp.json"
BASE_CI="$ROOT_DIR/.github/workflows/ci.yml"
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

fail() {
  echo "asset Worker smoke self-test: $*" >&2
  exit 1
}

for path in "$SMOKE" "$BASE_DOCKERFILE" "$BASE_ENTRYPOINT" "$BASE_SECCOMP"; do
  if [[ ! -f "$path" ]]; then
    fail "required fixture not found: $path"
  fi
done

for contract in \
  '--executable-id=gzip --arg-profile=gzip_decompress_v1' \
  '--executable-id=xz --arg-profile=xz_decompress_v1' \
  '--executable-id=zstd --arg-profile=zstd_decompress_v1'; do
  grep -Fq -- "$contract" "$SMOKE" || fail "runtime decompressor smoke missing: $contract"
done

run_static() {
  local dockerfile=$1
  local entrypoint=$2
  local seccomp=$3
  local workflow=$4
  ASSET_WORKER_STATIC_ONLY=1 \
  WORKER_DOCKERFILE_PATH="$dockerfile" \
  WORKER_ENTRYPOINT_PATH="$entrypoint" \
  WORKER_SECCOMP_PATH="$seccomp" \
  CI_WORKFLOW_PATH="$workflow" \
  bash "$SMOKE"
}

expect_mutation_failure() {
  local name=$1
  local kind=$2
  local expression=$3
  local dockerfile="$BASE_DOCKERFILE"
  local entrypoint="$BASE_ENTRYPOINT"
  local seccomp="$BASE_SECCOMP"
  local workflow="$BASE_CI"
  local candidate="$TMP_DIR/$name"
  local output="$TMP_DIR/$name.log"

  case "$kind" in
    dockerfile) dockerfile="$candidate" ;;
    entrypoint) entrypoint="$candidate" ;;
    seccomp) seccomp="$candidate" ;;
    workflow) workflow="$candidate" ;;
    *) fail "unknown mutation kind: $kind" ;;
  esac
  sed "$expression" "${kind/dockerfile/$BASE_DOCKERFILE}" >"$candidate" 2>/dev/null || {
    case "$kind" in
      entrypoint) sed "$expression" "$BASE_ENTRYPOINT" >"$candidate" ;;
      seccomp) sed "$expression" "$BASE_SECCOMP" >"$candidate" ;;
      workflow) sed "$expression" "$BASE_CI" >"$candidate" ;;
    esac
  }
  local original
  case "$kind" in
    dockerfile) original=$BASE_DOCKERFILE ;;
    entrypoint) original=$BASE_ENTRYPOINT ;;
    seccomp) original=$BASE_SECCOMP ;;
    workflow) original=$BASE_CI ;;
  esac
  if cmp -s "$original" "$candidate"; then
    fail "mutation did not change fixture: $name"
  fi
  if run_static "$dockerfile" "$entrypoint" "$seccomp" "$workflow" >"$output" 2>&1; then
    fail "smoke accepted unsafe mutation: $name"
  fi
}

run_static "$BASE_DOCKERFILE" "$BASE_ENTRYPOINT" "$BASE_SECCOMP" "$BASE_CI"

expect_mutation_failure unpinned-runtime dockerfile 's/@sha256:[0-9a-f]\{64\}//'
expect_mutation_failure root-user dockerfile 's/USER 10000:10000/USER 0:0/'
expect_mutation_failure public-port dockerfile '/USER 10000:10000/a\EXPOSE 9443'
expect_mutation_failure missing-worker-uid-check entrypoint '/WORKER_UID=10000/d'
expect_mutation_failure writable-bundle-check entrypoint 's/require_mount_option "$BUNDLE_ROOT" ro/require_mount_option "$BUNDLE_ROOT" rw/'
expect_mutation_failure permissive-seccomp seccomp 's/"SCMP_ACT_ERRNO"/"SCMP_ACT_ALLOW"/'
expect_mutation_failure missing-mount-denial seccomp 's/"mount", //'
expect_mutation_failure worker-publish workflow '/# asset-worker-no-publish/a\      - run: docker push example.invalid/xirang-asset-worker:test'

echo "asset Worker smoke self-test: PASS"
