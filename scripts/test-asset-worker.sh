#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DOCKERFILE=${WORKER_DOCKERFILE_PATH:-"$ROOT_DIR/deploy/worker/Dockerfile"}
ENTRYPOINT=${WORKER_ENTRYPOINT_PATH:-"$ROOT_DIR/deploy/worker/entrypoint.sh"}
SECCOMP=${WORKER_SECCOMP_PATH:-"$ROOT_DIR/deploy/worker/seccomp.json"}
WORKFLOW=${CI_WORKFLOW_PATH:-"$ROOT_DIR/.github/workflows/ci.yml"}
DOCKER=${ASSET_WORKER_DOCKER:-docker}
IMAGE=${ASSET_WORKER_IMAGE:-}
EXPECTED_ARCH=${EXPECTED_WORKER_ARCH:-}

fail() {
  echo "asset Worker smoke: $*" >&2
  exit 1
}

for path in "$DOCKERFILE" "$ENTRYPOINT" "$SECCOMP" "$WORKFLOW"; do
  [[ -f "$path" ]] || fail "required contract file not found: $path"
done
command -v jq >/dev/null 2>&1 || fail "jq is required"
bash -n "$ENTRYPOINT" || fail "entrypoint syntax is invalid"

grep -Eq '^FROM alpine:3\.23@sha256:[0-9a-f]{64}$' "$DOCKERFILE" || fail "runtime base is not digest pinned"
grep -Fqx -- 'USER 10000:10000' "$DOCKERFILE" || fail "image metadata user is not fixed non-root"
grep -Fq -- 'go build -trimpath' "$DOCKERFILE" || fail "Worker binaries are not built in-image"
for binary in asset-worker asset-tool-sandbox asset-worker-updater; do
  grep -Fq -- "-o /out/$binary" "$DOCKERFILE" || fail "missing binary build: $binary"
done
for tool in vips-tools tesseract-ocr poppler-utils libreoffice clamav ffmpeg; do
  grep -Eq "^[[:space:]]*$tool([[:space:]\\]|]|$)" "$DOCKERFILE" || fail "missing closed toolchain package: $tool"
done
if grep -Eq '^[[:space:]]*(EXPOSE|HEALTHCHECK)[[:space:]]' "$DOCKERFILE"; then
  fail "Worker image declared a public/runtime endpoint"
fi

for contract in \
  'WORKER_UID=10000' \
  'UPDATER_UID=10002' \
  'require_mount_option "$WORKSPACE_ROOT" noexec' \
  'require_mount_option "$WORKSPACE_ROOT" nosuid' \
  'require_mount_option "$WORKSPACE_ROOT" nodev' \
  'require_mount_option "$BUNDLE_ROOT" ro' \
  'require_socket "$WORKER_SOCKET" 600' \
  'require_socket "$UPDATER_SOCKET" 660'; do
  grep -Fq -- "$contract" "$ENTRYPOINT" || fail "entrypoint contract missing: $contract"
done

jq -e '
  .defaultAction == "SCMP_ACT_ALLOW" and
  ([.syscalls[] | select(.action == "SCMP_ACT_ERRNO") | .names[]] as $denied |
    ["mount", "umount2", "ptrace", "keyctl", "bpf", "perf_event_open", "init_module", "setns", "unshare"] |
    all(. as $name | $denied | index($name) != null)) and
  ([.syscalls[] | select(.action == "SCMP_ACT_ERRNO" and (.names | index("socket") != null))] | length) == 2
' "$SECCOMP" >/dev/null || fail "seccomp denied-syscall contract is incomplete"

JOB_FILE=$(mktemp)
trap 'rm -f -- "$JOB_FILE"' RETURN
awk '
  /^  asset-worker:/ { inside = 1 }
  inside && /^  [A-Za-z0-9_-]+:/ && $1 != "asset-worker:" { exit }
  inside { print }
' "$WORKFLOW" >"$JOB_FILE"
[[ -s "$JOB_FILE" ]] || fail "CI asset-worker job is missing"
for contract in \
  '# asset-worker-no-publish' \
  'arch: amd64' \
  'arch: arm64' \
  'docker build --platform "linux/${{ matrix.arch }}"' \
  'bash scripts/check-compose-config.test.sh' \
  'bash scripts/test-asset-worker.sh' \
  'aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25'; do
  grep -Fq -- "$contract" "$JOB_FILE" || fail "CI Worker contract missing: $contract"
done
if grep -Eiq 'docker[[:space:]]+(login|push|tag)|docker/login-action|docker/build-push-action|push:[[:space:]]*true|create-release|publish-release' "$JOB_FILE"; then
  fail "CI Worker job contains a publication action"
fi

if [[ "${ASSET_WORKER_STATIC_ONLY:-0}" == "1" ]]; then
  echo "asset Worker static contract: PASS"
  exit 0
fi
[[ -n "$IMAGE" ]] || fail "ASSET_WORKER_IMAGE is required for runtime smoke"
[[ "$EXPECTED_ARCH" == "amd64" || "$EXPECTED_ARCH" == "arm64" ]] || fail "EXPECTED_WORKER_ARCH must be amd64 or arm64"
if ! "$DOCKER" info >/dev/null 2>&1; then
  fail "Docker daemon is required"
fi

IMAGE_JSON=$(mktemp)
CONTAINER_JSON=$(mktemp)
container_id=
cleanup() {
  if [[ -n "$container_id" ]]; then
    "$DOCKER" rm -f "$container_id" >/dev/null 2>&1 || true
  fi
  rm -f -- "$IMAGE_JSON" "$CONTAINER_JSON" "$JOB_FILE"
}
trap cleanup EXIT

"$DOCKER" image inspect "$IMAGE" >"$IMAGE_JSON"
jq -e --arg arch "$EXPECTED_ARCH" '
  length == 1 and .[0].Architecture == $arch and .[0].Config.User == "10000:10000" and
  .[0].Config.Entrypoint == ["/usr/local/bin/asset-worker-entrypoint"] and
  .[0].Config.ExposedPorts == null
' "$IMAGE_JSON" >/dev/null || fail "Worker image metadata is unsafe"

container_id=$("$DOCKER" create \
  --user 10000:10000 \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges=true \
  --security-opt "seccomp=$SECCOMP" \
  --pids-limit 256 \
  --memory 1g \
  --cpus 2 \
  --tmpfs /run/xirang/asset-jobs:rw,noexec,nosuid,nodev,size=64m,nr_inodes=8192,mode=0700,uid=10000,gid=10000 \
  --entrypoint /bin/sh \
  "$IMAGE" -eu -c '
    job=/run/xirang/asset-jobs/job-smoke
    mkdir -m 0700 "$job" "$job/home" "$job/output"
    ffmpeg -nostdin -loglevel error -f lavfi -i color=c=black:s=16x16:d=0.1 -an -c:v mpeg4 -f mp4 -y "$job/input.bin"
    chmod 0400 "$job/input.bin"
    HOME="$job/home" XIRANG_OUTPUT_DIR="$job/output" XIRANG_INPUT_MODE=path XIRANG_INPUT_PATH="$job/input.bin" \
      /usr/local/bin/asset-tool-sandbox --executable-id=ffprobe --arg-profile=media_probe_v1 >/dev/null
    printf "%s\n" "closed archive member" >"$job/member.txt"
    tar -C "$job" -cf "$job/archive.tar" member.txt
    gzip -n -c "$job/archive.tar" >"$job/archive.tar.gz"
    HOME="$job/home" XIRANG_OUTPUT_DIR="$job/output" XIRANG_INPUT_MODE=pipe \
      /usr/local/bin/asset-tool-sandbox --executable-id=gzip --arg-profile=gzip_decompress_v1 \
      <"$job/archive.tar.gz" >"$job/gzip.tar"
    cmp "$job/archive.tar" "$job/gzip.tar"
    xz -c "$job/archive.tar" >"$job/archive.tar.xz"
    HOME="$job/home" XIRANG_OUTPUT_DIR="$job/output" XIRANG_INPUT_MODE=pipe \
      /usr/local/bin/asset-tool-sandbox --executable-id=xz --arg-profile=xz_decompress_v1 \
      <"$job/archive.tar.xz" >"$job/xz.tar"
    cmp "$job/archive.tar" "$job/xz.tar"
    zstd -q -c "$job/archive.tar" >"$job/archive.tar.zst"
    HOME="$job/home" XIRANG_OUTPUT_DIR="$job/output" XIRANG_INPUT_MODE=pipe \
      /usr/local/bin/asset-tool-sandbox --executable-id=zstd --arg-profile=zstd_decompress_v1 \
      <"$job/archive.tar.zst" >"$job/zstd.tar"
    cmp "$job/archive.tar" "$job/zstd.tar"
  ')

"$DOCKER" inspect "$container_id" >"$CONTAINER_JSON"
jq -e '
  .[0].HostConfig.ReadonlyRootfs == true and
  .[0].HostConfig.NetworkMode == "none" and
  .[0].HostConfig.CapDrop == ["ALL"] and
  (.[0].HostConfig.SecurityOpt | index("no-new-privileges=true")) != null and
  (.[0].HostConfig.SecurityOpt | map(startswith("seccomp=")) | any) and
  .[0].HostConfig.PidsLimit == 256 and
  .[0].HostConfig.Memory == 1073741824 and
  .[0].HostConfig.NanoCpus == 2000000000 and
  (.[0].HostConfig.Tmpfs["/run/xirang/asset-jobs"] | contains("noexec")) and
  (.[0].HostConfig.Tmpfs["/run/xirang/asset-jobs"] | contains("nosuid")) and
  (.[0].HostConfig.Tmpfs["/run/xirang/asset-jobs"] | contains("nodev"))
' "$CONTAINER_JSON" >/dev/null || fail "Worker runtime isolation flags are unsafe"

"$DOCKER" start -a "$container_id"
echo "asset Worker runtime smoke: PASS"
