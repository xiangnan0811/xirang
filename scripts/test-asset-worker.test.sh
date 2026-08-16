#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SELF="$ROOT_DIR/scripts/test-asset-worker.test.sh"
SMOKE="$ROOT_DIR/scripts/test-asset-worker.sh"
BASE_DOCKERFILE="$ROOT_DIR/deploy/worker/Dockerfile"
BASE_ENTRYPOINT="$ROOT_DIR/deploy/worker/entrypoint.sh"
BASE_SECCOMP="$ROOT_DIR/deploy/worker/seccomp.json"
BASE_CI="$ROOT_DIR/.github/workflows/ci.yml"
TMP_DIR=$(mktemp -d)
trap 'rm -rf -- "$TMP_DIR"' EXIT

AMD64_CLOSURE="$TMP_DIR/runtime-closure-amd64.v1.json"
ARM64_CLOSURE="$TMP_DIR/runtime-closure-arm64.v1.json"
TAMPERED_AMD64_CLOSURE="$TMP_DIR/runtime-closure-amd64-tampered.v1.json"
WRONG_ARCH_CLOSURE="$TMP_DIR/runtime-closure-wrong-arch.v1.json"
WRITABLE_AMD64_CLOSURE="$TMP_DIR/runtime-closure-amd64-writable.v1.json"
FAKE_DOCKER="$TMP_DIR/docker"

fail() {
  echo "asset Worker smoke self-test: $*" >&2
  exit 1
}

smoke_runtime_closure_contract() {
  local smoke=$1
  local contract
  for contract in \
    'validate_runtime_closure "$AMD64_RUNTIME_CLOSURE" linux/amd64' \
    'validate_runtime_closure "$ARM64_RUNTIME_CLOSURE" linux/arm64' \
    '"$DOCKER" cp "$container_id:/usr/local/share/xirang/runtime-closure.v1.json" "$IMAGE_RUNTIME_CLOSURE"' \
    'cmp -s "$IMAGE_RUNTIME_CLOSURE" "$expected_runtime_closure"' \
    'sha256sum "$AMD64_RUNTIME_CLOSURE"' \
    'sha256sum "$ARM64_RUNTIME_CLOSURE"' \
    'generate_profile_fixture "$PROFILE_DIR" "$amd64_runtime_closure_sha256" "$arm64_runtime_closure_sha256"' \
    'go run "./$helper_package" "$profile_root"' \
    '"$amd64_runtime_closure_sha256" "$arm64_runtime_closure_sha256"'; do
    grep -Fq -- "$contract" "$smoke" || return 1
  done
  ! grep -Fq $'\t"strings"' "$smoke"
}

smoke_private_tmpfs_contract() {
  local smoke=$1
  grep -Fq -- '--tmpfs /tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000' "$smoke" &&
    grep -Fq -- '--tmpfs /var/lib/xirang/asset-worker-bundles/active/clamav:rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000' "$smoke" &&
    grep -Fq -- '(.[0].HostConfig.Tmpfs | keys | sort) == ["/run/xirang/asset-jobs", "/tmp", "/var/lib/xirang/asset-worker-bundles/active/clamav"]' "$smoke" &&
    grep -Fq -- '.[0].HostConfig.Tmpfs["/tmp"] == "rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000"' "$smoke" &&
    grep -Fq -- '.[0].HostConfig.Tmpfs["/var/lib/xirang/asset-worker-bundles/active/clamav"] == "rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000"' "$smoke"
}

smoke_worker_group_isolation_contract() {
  local smoke=$1
  [[ "$(grep -Fc -- 'test "$(id -u):$(id -g)" = "10000:10000"' "$smoke" || true)" == "2" ]] &&
    [[ "$(grep -Fc -- '*" 10002 "*) exit 1 ;;' "$smoke" || true)" == "2" ]] &&
    [[ "$(grep -Fc -- '.[0].HostConfig.GroupAdd == null' "$smoke" || true)" == "2" ]] &&
    grep -Fq -- 'parser image identity includes updater supplementary group' "$smoke" &&
    grep -Fq -- '+ require_exact_groups 10000' "$smoke" &&
    grep -Fq -- '--group-add "$supplementary_gid"' "$smoke" &&
    grep -Fq -- 'for supplementary_gid in 10002 10003' "$smoke" &&
    grep -Fq -- 'supplementary parser group' "$smoke"
}

entrypoint_rejects_arbitrary_supplementary_group() {
  local entrypoint=$1
  local role=$2
  local uid=$3
  local gid=$4
  local probe_root="$TMP_DIR/group-probe-${role}"
  local fake_bin="$probe_root/bin"
  local mount_log="$probe_root/mount.log"
  mkdir -p "$fake_bin"
  : >"$mount_log"
  cat >"$fake_bin/id" <<EOF
#!/usr/bin/env bash
set -euo pipefail
case "\${1-}" in
  -u) printf '%s\\n' '$uid' ;;
  -g) printf '%s\\n' '$gid' ;;
  -G) printf '%s\\n' '$gid 42' ;;
  *) exit 1 ;;
esac
EOF
  cat >"$fake_bin/stat" <<EOF
#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' stat >>'$mount_log'
printf '700:%s:%s\\n' '$uid' '$gid'
EOF
  cat >"$fake_bin/awk" <<EOF
#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' awk >>'$mount_log'
exit 0
EOF
  chmod 0755 "$fake_bin/id" "$fake_bin/stat" "$fake_bin/awk"
  if PATH="$fake_bin:$PATH" "$entrypoint" "$role" >"$probe_root/output" 2>&1; then
    fail "$role entrypoint accepted arbitrary supplementary group"
  fi
  [[ ! -s "$mount_log" ]] || fail "$role entrypoint reached mount validation before rejecting supplementary group"
}

smoke_post_restart_capability_contract() {
  local smoke=$1
  [[ "$(grep -Fc -- 'profile_assert_worker_capabilities "$core_id"' "$smoke" || true)" == "2" ]] &&
    [[ "$(grep -Ec '^[[:space:]]*profile_assert_core_capabilities$' "$smoke" || true)" == "3" ]]
}

smoke_raster_output_contract() {
  local smoke=$1
  local contract
  for contract in \
    'find "$vips_job/output" -mindepth 1 -maxdepth 1 -type f | wc -l' \
    'find "$vips_job/output" -mindepth 1 -maxdepth 1 ! -type f -print -quit' \
    'vips_thumbnail_codec=$(ffprobe' \
    'test "$vips_thumbnail_codec" = png' \
    'test "$vips_thumbnail_width" -eq 16' \
    'test "$vips_thumbnail_height" -eq 16' \
    'find "$ocr_job/output" -mindepth 1 -maxdepth 1 -type f | wc -l' \
    'find "$ocr_job/output" -mindepth 1 -maxdepth 1 ! -type f -print -quit'; do
    grep -Fq -- "$contract" "$smoke" || return 1
  done
}

fake_runtime_repository_override_contract() {
  local section
  local contract
  section=$(sed -n '/^run_fake_runtime()/,/^expect_runtime_failure()/p' "$SELF")
  for contract in \
    'WORKER_DOCKERFILE_PATH="$BASE_DOCKERFILE"' \
    'WORKER_ENTRYPOINT_PATH="$BASE_ENTRYPOINT"' \
    'WORKER_SECCOMP_PATH="$BASE_SECCOMP"' \
    'CI_WORKFLOW_PATH="$BASE_CI"'; do
    grep -Fq -- "$contract" <<<"$section" || return 1
  done
}

smoke_resource_profile_contract() {
  local smoke=$1
  (cd "$ROOT_DIR/backend" &&
    ASSET_WORKER_SMOKE_PATH="$smoke" \
      go test ./internal/backupasset/processing/capabilities \
        -run '^TestAssetWorkerSmokeInvocationsMatchBuildInvocationResources$' -count=1)
}

smoke_media_probe_contract() {
  local smoke=$1
  local contract
  for contract in \
    'media_probe_stdout="$job/media-probe.stdout"' \
    'media_probe_stderr="$job/media-probe.stderr"' \
    'test ! -s "$media_probe_stderr"' \
    'keys == [\"format\", \"programs\", \"stream_groups\", \"streams\"]' \
    '(.programs | type == \"array\" and length == 0)' \
    '(.stream_groups | type == \"array\" and length == 0)'; do
    grep -Fq -- "$contract" "$smoke" || return 1
  done
}

smoke_media_probe_host_boundary_contract() {
  local smoke=$1
  awk '
    /^container_id=\$\("\$DOCKER" create/ { native_container = 1 }
    native_container && /"\$IMAGE" -eu -c/ {
      inside = 1
      native_container = 0
      next
    }
    inside {
      line = $0
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
      if (line == "\047)") {
        ended = 1
        inside = 0
        next
      }
      if ($0 ~ /(^|[[:space:]])jq[[:space:]]+-e/) worker_jq++
      if (index($0, "cat \"$media_probe_stdout\"") != 0) emitted++
    }
    END { exit ended == 1 && worker_jq == 0 && emitted == 1 ? 0 : 1 }
  ' "$smoke" &&
    grep -Fq -- 'MEDIA_PROBE_JSON=$("$DOCKER" start -a "$container_id")' "$smoke" &&
    grep -Fq -- '<<<"$MEDIA_PROBE_JSON"' "$smoke"
}

mutation_source() {
  case "$1" in
    smoke) printf '%s\n' "$SMOKE" ;;
    dockerfile) printf '%s\n' "$BASE_DOCKERFILE" ;;
    entrypoint) printf '%s\n' "$BASE_ENTRYPOINT" ;;
    seccomp) printf '%s\n' "$BASE_SECCOMP" ;;
    workflow) printf '%s\n' "$BASE_CI" ;;
    *) return 1 ;;
  esac
}

assert_private_tmpfs_create() {
  local create_log=$1
  awk -v expected='/tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000' '
    /^\/tmp:/ {
      targets++
      if (previous == "--tmpfs" && $0 == expected) {
        exact++
      }
    }
    { previous = $0 }
    END { exit targets == 1 && exact == 1 ? 0 : 1 }
  ' "$create_log"
}

assert_clamav_tmpfs_create() {
  local create_log=$1
  awk -v expected='/var/lib/xirang/asset-worker-bundles/active/clamav:rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000' '
    /^\/var\/lib\/xirang\/asset-worker-bundles\/active\/clamav:/ {
      targets++
      if (previous == "--tmpfs" && $0 == expected) {
        exact++
      }
    }
    { previous = $0 }
    END { exit targets == 1 && exact == 1 ? 0 : 1 }
  ' "$create_log"
}

write_runtime_closure() {
  local path=$1
  local platform=$2
  local digest_character=$3
  local digest
  digest=$(printf '%64s' '' | tr ' ' "$digest_character")
  printf '{"schema_version":1,"platform":"%s","files":[{"kind":"regular","path":"/usr/local/bin/asset-worker","mode":365,"size":1,"sha256":"%s"}]}' \
    "$platform" "$digest" >"$path"
}

write_runtime_closure "$AMD64_CLOSURE" linux/amd64 a
write_runtime_closure "$ARM64_CLOSURE" linux/arm64 b
write_runtime_closure "$TAMPERED_AMD64_CLOSURE" linux/amd64 c
write_runtime_closure "$WRONG_ARCH_CLOSURE" linux/386 d
write_runtime_closure "$WRITABLE_AMD64_CLOSURE" linux/amd64 e
jq '.files[0].mode = 509' "$WRITABLE_AMD64_CLOSURE" >"$WRITABLE_AMD64_CLOSURE.tmp"
mv "$WRITABLE_AMD64_CLOSURE.tmp" "$WRITABLE_AMD64_CLOSURE"

cat >"$FAKE_DOCKER" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

case "${1-}:${2-}" in
  info:)
    exit 0
    ;;
  image:inspect)
    printf '[{"Architecture":"%s","Config":{"User":"10000:10000","Entrypoint":["/usr/local/bin/asset-worker-entrypoint"],"ExposedPorts":null}}]\n' \
      "$FAKE_DOCKER_ARCH"
    ;;
  run:*)
    printf '%s\n' '+ require_exact_groups 10000' '+ id -G' '+ fail' >&2
    exit 1
    ;;
  create:*)
    : "${FAKE_DOCKER_CREATE_LOG:?}"
    printf '%s\n' "$@" >"$FAKE_DOCKER_CREATE_LOG"
    printf '%s\n' fake-worker-container
    ;;
  cp:*)
    cp "$FAKE_DOCKER_IMAGE_CLOSURE" "$3"
    ;;
  inspect:*)
	    printf '%s\n' '[{"HostConfig":{"ReadonlyRootfs":true,"NetworkMode":"none","CapDrop":["ALL"],"SecurityOpt":["no-new-privileges=true","seccomp=/fixture/seccomp.json"],"PidsLimit":256,"Memory":1073741824,"MemorySwap":1073741824,"NanoCpus":2000000000,"Tmpfs":{"/run/xirang/asset-jobs":"rw,noexec,nosuid,nodev","/tmp":"rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000","/var/lib/xirang/asset-worker-bundles/active/clamav":"rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000"}}}]'
    ;;
  start:*)
    printf '%s\n' '{"format":{"duration":"0.100000"},"programs":[],"stream_groups":[],"streams":[{"codec_name":"h264","codec_type":"video","duration":"0.100000","height":16,"index":0,"width":16}]}'
    ;;
  rm:*)
    exit 0
    ;;
  *)
    printf 'unexpected fake docker invocation: %q\n' "$*" >&2
    exit 1
    ;;
esac
SH
chmod 0755 "$FAKE_DOCKER"

for path in "$SMOKE" "$BASE_DOCKERFILE" "$BASE_ENTRYPOINT" "$BASE_SECCOMP"; do
  if [[ ! -f "$path" ]]; then
    fail "required fixture not found: $path"
  fi
done

SMOKE_MUTATION_PROBE="$TMP_DIR/smoke-mutation-probe.sh"
smoke_mutation_source=$(mutation_source smoke) || fail "smoke mutation source is unavailable"
sed '/--tmpfs \/tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000/d' \
  "$smoke_mutation_source" >"$SMOKE_MUTATION_PROBE"
[[ -s "$SMOKE_MUTATION_PROBE" ]] && grep -Fqx -- '#!/usr/bin/env bash' "$SMOKE_MUTATION_PROBE" ||
  fail "smoke mutation did not use the real runtime smoke fixture"
if grep -Fq -- '--tmpfs /tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000' "$SMOKE_MUTATION_PROBE"; then
	fail "smoke mutation did not hit its target"
fi

VALID_CREATE_LOG="$TMP_DIR/fake-create-valid.log"
MISSING_CREATE_LOG="$TMP_DIR/fake-create-missing.log"
DRIFT_CREATE_LOG="$TMP_DIR/fake-create-drift.log"
MISSING_CLAMAV_CREATE_LOG="$TMP_DIR/fake-create-missing-clamav.log"
DRIFT_CLAMAV_CREATE_LOG="$TMP_DIR/fake-create-drift-clamav.log"
FAKE_DOCKER_CREATE_LOG="$VALID_CREATE_LOG" "$FAKE_DOCKER" create \
  --tmpfs /run/xirang/asset-jobs:rw,noexec,nosuid,nodev \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000 \
  --tmpfs /var/lib/xirang/asset-worker-bundles/active/clamav:rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000 >/dev/null
assert_private_tmpfs_create "$VALID_CREATE_LOG" || fail "fake Docker did not capture the exact Worker /tmp create contract"
assert_clamav_tmpfs_create "$VALID_CREATE_LOG" || fail "fake Docker did not capture the exact ClamAV database tmpfs contract"
FAKE_DOCKER_CREATE_LOG="$MISSING_CREATE_LOG" "$FAKE_DOCKER" create \
  --tmpfs /run/xirang/asset-jobs:rw,noexec,nosuid,nodev \
  --tmpfs /var/lib/xirang/asset-worker-bundles/active/clamav:rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000 >/dev/null
if assert_private_tmpfs_create "$MISSING_CREATE_LOG"; then
  fail "fake Docker accepted a missing Worker /tmp create contract"
fi
FAKE_DOCKER_CREATE_LOG="$DRIFT_CREATE_LOG" "$FAKE_DOCKER" create \
  --tmpfs /run/xirang/asset-jobs:rw,noexec,nosuid,nodev \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0750,uid=10000,gid=10000 \
  --tmpfs /var/lib/xirang/asset-worker-bundles/active/clamav:rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000 >/dev/null
if assert_private_tmpfs_create "$DRIFT_CREATE_LOG"; then
  fail "fake Docker accepted a drifted Worker /tmp create contract"
fi
FAKE_DOCKER_CREATE_LOG="$MISSING_CLAMAV_CREATE_LOG" "$FAKE_DOCKER" create \
  --tmpfs /run/xirang/asset-jobs:rw,noexec,nosuid,nodev \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000 >/dev/null
if assert_clamav_tmpfs_create "$MISSING_CLAMAV_CREATE_LOG"; then
  fail "fake Docker accepted a missing ClamAV database tmpfs contract"
fi
FAKE_DOCKER_CREATE_LOG="$DRIFT_CLAMAV_CREATE_LOG" "$FAKE_DOCKER" create \
  --tmpfs /run/xirang/asset-jobs:rw,noexec,nosuid,nodev \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000 \
  --tmpfs /var/lib/xirang/asset-worker-bundles/active/clamav:rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0750,uid=10000,gid=10000 >/dev/null
if assert_clamav_tmpfs_create "$DRIFT_CLAMAV_CREATE_LOG"; then
  fail "fake Docker accepted a drifted ClamAV database tmpfs contract"
fi

for contract in \
	  'run_asset_tool gzip gzip_decompress_v1' \
	  'run_asset_tool xz xz_decompress_v1' \
	  'run_asset_tool zstd zstd_decompress_v1'; do
  grep -Fq -- "$contract" "$SMOKE" || fail "runtime decompressor smoke missing: $contract"
done

smoke_runtime_closure_contract "$SMOKE" ||
  fail "runtime closure artifact or signed profile contract is incomplete"
smoke_private_tmpfs_contract "$SMOKE" ||
  fail "Worker private /tmp runtime smoke contract is incomplete"
smoke_worker_group_isolation_contract "$SMOKE" ||
  fail "parser supplementary-group isolation smoke contract is incomplete"
entrypoint_rejects_arbitrary_supplementary_group "$BASE_ENTRYPOINT" worker 10000 10000
entrypoint_rejects_arbitrary_supplementary_group "$BASE_ENTRYPOINT" updater 10002 10002
smoke_post_restart_capability_contract "$SMOKE" ||
  fail "post-Core-restart exact capability assertions are incomplete"
smoke_raster_output_contract "$SMOKE" ||
  fail "vips or Tesseract bounded output assertions are incomplete"
fake_runtime_repository_override_contract ||
  fail "fake runtime does not bind the repository contract paths"
smoke_resource_profile_contract "$SMOKE" ||
  fail "Worker smoke invocations drifted from BuildInvocation resource limits"
smoke_media_probe_contract "$SMOKE" ||
  fail "media probe runtime output does not use the closed canonical shape"
smoke_media_probe_host_boundary_contract "$SMOKE" ||
  fail "ffprobe JSON validation escaped the host harness boundary"

for contract in \
  'apk info -e "$package"' \
  '/usr/share/tessdata/eng.traineddata' \
  '/usr/share/fonts/noto/NotoSansCJK-Regular.ttc' \
	  'tesseract --list-langs' \
	  'for codec in h264 vp8 vp9 aac mp3 vorbis pcm_s16le png' \
	  'for encoder in libx264 aac png' \
	  '--memory-swap 1g' \
	  '.HostConfig.MemorySwap == 1073741824'; do
  grep -Fq -- "$contract" "$SMOKE" || fail "runtime toolchain preflight smoke missing: $contract"
done

for contract in \
  'FROM golang:1.26.6-alpine@sha256:' \
  'gcc=15.2.0-r5' \
  'musl-dev=1.2.6-r2' \
  'bash=5.3.3-r1' \
  'tesseract-ocr-data-eng=5.5.1-r0' \
	  'font-noto=2025.12.01-r0' \
	  'chmod 0755 /etc/network/if-up.d/dad' \
	  "sed -i -E 's/^(clamav|asset-worker|asset-updater):([^:]*):[^:]*/\\1:\\2:/' /etc/shadow" \
	  'test "$(stat -c '\''%a:%u:%g'\'' /etc/shadow)" = "640:0:42"' \
	  'rm -f /etc/crontabs/root /etc/shadow- /lib/apk/db/lock' \
	  'rm -f /var/cache/fontconfig/*.cache-* /var/log/apk.log' \
	  '! find / -xdev -type f -perm /022 -print -quit | grep -q .' \
	  '/usr/local/bin/asset-worker write-runtime-closure-manifest' \
  '444:0:0'; do
  grep -Fq -- "$contract" "$BASE_DOCKERFILE" || fail "pinned toolchain input missing: $contract"
done
if grep -Fq -- 'apk upgrade' "$BASE_DOCKERFILE"; then
  fail "Worker Dockerfile retained nondeterministic apk upgrade"
fi

for contract in \
	  'ASSET_WORKER_PROFILE_SMOKE' \
	  'PROFILE_NETWORK_MODE=${ASSET_WORKER_PROFILE_NETWORK_MODE:-bridge}' \
	  'network_mode: host' \
	  'ports: !reset []' \
	  'PROFILE_COMPOSE_FILES' \
	  'ADMIN_INITIAL_PASSWORD=ProfileSmokeAdmin1!' \
	  'BACKUP_ASSETS_DERIVED_STORE_ROOT=/var/lib/xirang-asset-runtime/derived' \
	  'cleanup_profile_dir()' \
  '--cap-add DAC_OVERRIDE' \
  'rm -rf /profile/* /profile/.[!.]* /profile/..?*' \
  '--profile asset-worker' \
  'up -d --no-build' \
  'asset-worker-init' \
  'asset-worker-updater' \
	  'asset-worker-worker-runtime' \
  'asset-worker-updater-runtime' \
  'asset-worker-derived-store' \
  '/usr/local/bin/asset-worker-entrypoint' \
  'down --volumes --remove-orphans' \
  'parser mounted the updater socket' \
  'updater violated its identity, mount, or network boundary'; do
  grep -Fq -- "$contract" "$SMOKE" || fail "complete profile smoke missing: $contract"
done
for contract in \
  'mkdir -p /bundle/bundles' \
  'chown 10002:10000 /bundle /bundle/bundles' \
  'chmod 2750 /bundle /bundle/bundles' \
  'test ! -e /bundle/active'; do
  grep -Fq -- "$contract" "$SMOKE" || fail "fresh profile bundle store contract missing: $contract"
done
if grep -Fq -- 'ln -s "bundles/$fingerprint" /bundle/active' "$SMOKE"; then
  fail "fresh profile must not seed an active bundle unknown to Core"
fi
for contract in \
  'generate_profile_fixture()' \
  'PROFILE_HELPER_DIR=' \
  'mktemp -d "$ROOT_DIR/backend/profile_smoke_fixture_XXXXXX"' \
  'rm -rf -- "$PROFILE_HELPER_DIR"' \
	  'updater.BuildCanonicalTar' \
	  'updater.SignManifest' \
	  'updater.VerifyPackage' \
	  'ASSET_WORKER_AMD64_RUNTIME_CLOSURE' \
	  'ASSET_WORKER_ARM64_RUNTIME_CLOSURE' \
	  'toolchain/attestations.v1.json' \
	  'capabilityspec.WorkerProfiles()' \
  'test ! -e /var/lib/xirang/asset-worker-bundles/active' \
  '/api/v1/auth/login' \
  '.data.token | select(type == "string" and length > 0)' \
  '/api/v1/admin/backup-asset-processing/capabilities' \
  'all(.data.items[]; (.deployed | not) and .ready_workers == 0)' \
  '/api/v1/admin/backup-asset-processing/updater/offline-candidates/scan' \
  '/api/v1/admin/backup-asset-processing/updater/offline-candidates' \
  '/api/v1/admin/backup-asset-processing/updater/offline-imports' \
  '"expected_active_fingerprint":null' \
  '/api/v1/admin/backup-asset-processing/updater' \
  'readlink /var/lib/xirang/asset-worker-bundles/active' \
  'backup_asset_updater_metadata' \
  'backup_asset_worker_capabilities' \
  'pipeline_fingerprint' \
  '/profile/asset-worker-inbox/profile-smoke-candidate' \
  '"$DOCKER" exec "$worker_id" test ! -e /var/lib/xirang/asset-worker-inbox/profile-smoke-candidate' \
	  'profile_compose restart asset-worker' \
  'all(.data.items[]; .deployed and .ready_workers >= 1)' \
  'post-restart signed activation fingerprint did not persist' \
  'post-restart parser capability fingerprint did not persist' \
  'signed activation fingerprint did not converge'; do
  grep -Fq -- "$contract" "$SMOKE" || fail "signed profile activation smoke missing: $contract"
done
for contract in \
  'profile_assert_worker_capabilities()' \
  'archive.extract_entry/archive_member_v1' \
  'archive.inspect/archive_index_v1' \
  'document.convert/static_pages_v1' \
  'image.ocr/tesseract_text_v1' \
  'image.thumbnail/raster_thumbnail_v1' \
  'malware.scan/signature_scan_v1' \
  'media.probe/media_probe_v1' \
  'media.transcode/browser_preview_v1' \
  'secret.classify/bounded_secret_v1' \
  'text.extract/bounded_text_v1' \
  'profile_assert_core_capabilities()' \
  'BACKUP_ASSETS_PROCESSING_SECRET_CLASSIFY=false' \
  'worker capability/profile set did not match the closed 10-profile contract' \
  'Core capability projection did not match the closed 9-profile policy'; do
  grep -Fq -- "$contract" "$SMOKE" || fail "closed profile assertion missing: $contract"
done
for contract in \
	  'run_asset_tool vips vips_thumbnail_v1' \
	  'run_asset_tool tesseract tesseract_ocr_v1' \
	  'run_asset_tool pdftocairo pdf_pages_v1' \
	  'run_asset_tool pdftotext pdf_text_v1' \
	  'run_asset_tool libreoffice office_pdf_v1' \
	  'run_asset_tool clamscan clam_scan_v1' \
	  'run_asset_tool ffmpeg media_preview_v1' \
	  'cd "$clamav_clean_job"' \
	  'test ! -s "$clamav_clean_job/scan.stdout"' \
	  'Heuristics.Limits.Exceeded.' \
	  'sandboxed ClamAV clean fixture failed' \
	  'sandboxed ClamAV finding fixture did not return the closed finding exit code' \
	  'sandboxed ClamAV limit fixture did not return the closed limit exit code'; do
  grep -Fq -- "$contract" "$SMOKE" || fail "bounded external-tool smoke missing: $contract"
done
for contract in \
	  'DERIVED_ROOT=/var/lib/xirang-asset-runtime/derived' \
	  'require_absent "$UPDATER_SOCKET"' \
	  'require_absent "$WORKER_SOCKET"' \
	  'for (field = 1; field <= count; field++)' \
	  'require_directory "$BUNDLE_ROOT" 2750 "$UPDATER_UID" "$WORKER_GID"' \
	  'require_directory "$DERIVED_ROOT" 700 "$WORKER_UID" "$WORKER_GID"' \
	  'chmod 0700 "$DERIVED_ROOT"' \
	  'chmod 2750 "$BUNDLE_ROOT"'; do
  grep -Fq -- "$contract" "$BASE_ENTRYPOINT" || fail "entrypoint peer-mount isolation missing: $contract"
done
for contract in \
  'chown 10002:10000 /var/lib/xirang/asset-worker-bundles' \
  'chmod 02750 /var/lib/xirang/asset-worker-bundles'; do
  grep -Fq -- "$contract" "$BASE_DOCKERFILE" || fail "bundle reader-group contract missing: $contract"
done
for contract in \
  'asset-worker-closure:' \
  '# asset-worker-closure-no-publish' \
  'Asset Worker Runtime Closure (${{ matrix.arch }})' \
  'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1' \
  'name: asset-worker-runtime-closure-${{ matrix.arch }}' \
  'needs: asset-worker-closure' \
  'actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1' \
  'name: asset-worker-runtime-closure-amd64' \
  'name: asset-worker-runtime-closure-arm64' \
  'ASSET_WORKER_AMD64_RUNTIME_CLOSURE: ${{ runner.temp }}/asset-worker-runtime-closure-amd64/runtime-closure.v1.json' \
  'ASSET_WORKER_ARM64_RUNTIME_CLOSURE: ${{ runner.temp }}/asset-worker-runtime-closure-arm64/runtime-closure.v1.json' \
  'Build local Core image for Worker profile smoke' \
  'Run complete Worker Compose profile smoke' \
  'ASSET_WORKER_PROFILE_SMOKE: "1"'; do
  grep -Fq -- "$contract" "$BASE_CI" || fail "CI complete profile smoke missing: $contract"
done

run_fake_runtime() {
  local expected_arch=$1
  local image_closure=$2
  local amd64_closure=$3
  local arm64_closure=$4
  local smoke=${5:-$SMOKE}
  local create_log="$TMP_DIR/fake-create-runtime-${expected_arch}-${RANDOM}.log"
  rm -f -- "$create_log"
  if ! \
  ASSET_WORKER_DOCKER="$FAKE_DOCKER" \
  FAKE_DOCKER_CREATE_LOG="$create_log" \
  FAKE_DOCKER_ARCH="$expected_arch" \
  FAKE_DOCKER_IMAGE_CLOSURE="$image_closure" \
  ASSET_WORKER_IMAGE=fake-worker:fixture \
  EXPECTED_WORKER_ARCH="$expected_arch" \
  ASSET_WORKER_AMD64_RUNTIME_CLOSURE="$amd64_closure" \
  ASSET_WORKER_ARM64_RUNTIME_CLOSURE="$arm64_closure" \
  WORKER_DOCKERFILE_PATH="$BASE_DOCKERFILE" \
  WORKER_ENTRYPOINT_PATH="$BASE_ENTRYPOINT" \
  WORKER_SECCOMP_PATH="$BASE_SECCOMP" \
  CI_WORKFLOW_PATH="$BASE_CI" \
  bash "$smoke"; then
    return 1
  fi
  if ! assert_private_tmpfs_create "$create_log"; then
    echo "asset Worker smoke self-test: runtime smoke did not capture the exact Worker /tmp create contract" >&2
    return 1
  fi
  if ! assert_clamav_tmpfs_create "$create_log"; then
    echo "asset Worker smoke self-test: runtime smoke did not capture the exact ClamAV database tmpfs contract" >&2
    return 1
  fi
}

expect_runtime_failure() {
  local name=$1
  shift
  if run_fake_runtime "$@" >"$TMP_DIR/$name.log" 2>&1; then
    fail "runtime smoke accepted invalid closure artifacts: $name"
  fi
}

expect_runtime_smoke_mutation_failure() {
  local name=$1
  local expression=$2
  local expected_reason=$3
  local candidate="$TMP_DIR/$name-smoke.sh"
  local output="$TMP_DIR/$name-smoke.log"
  sed "$expression" "$SMOKE" >"$candidate"
  if cmp -s "$SMOKE" "$candidate"; then
    fail "runtime smoke mutation did not change fixture: $name"
  fi
  if run_fake_runtime amd64 "$AMD64_CLOSURE" "$AMD64_CLOSURE" "" "$candidate" >"$output" 2>&1; then
    fail "runtime smoke accepted unsafe tmpfs mutation: $name"
  fi
  if grep -Fq -- 'required contract file not found' "$output"; then
    fail "runtime smoke mutation stopped before its closed reason: $name"
  fi
  grep -Fqx -- "asset Worker smoke self-test: $expected_reason" "$output" ||
    fail "runtime smoke mutation did not reach its closed reason: $name"
}

run_static() {
  local smoke=$1
  local dockerfile=$2
  local entrypoint=$3
  local seccomp=$4
  local workflow=$5
  ASSET_WORKER_STATIC_ONLY=1 \
  WORKER_DOCKERFILE_PATH="$dockerfile" \
  WORKER_ENTRYPOINT_PATH="$entrypoint" \
  WORKER_SECCOMP_PATH="$seccomp" \
  CI_WORKFLOW_PATH="$workflow" \
  bash "$smoke"
}

expect_mutation_failure() {
  local name=$1
  local kind=$2
  local expression=$3
  local smoke="$SMOKE"
  local dockerfile="$BASE_DOCKERFILE"
  local entrypoint="$BASE_ENTRYPOINT"
  local seccomp="$BASE_SECCOMP"
  local workflow="$BASE_CI"
  local candidate="$TMP_DIR/$name"
  local output="$TMP_DIR/$name.log"

	case "$kind" in
    smoke) smoke="$candidate" ;;
    dockerfile) dockerfile="$candidate" ;;
    entrypoint) entrypoint="$candidate" ;;
    seccomp) seccomp="$candidate" ;;
    workflow) workflow="$candidate" ;;
	  *) fail "unknown mutation kind: $kind" ;;
	esac
	local original
	original=$(mutation_source "$kind") || fail "mutation source unavailable: $kind"
	sed "$expression" "$original" >"$candidate"
  if cmp -s "$original" "$candidate"; then
    fail "mutation did not change fixture: $name"
  fi
		if [[ "$kind" == "smoke" ]]; then
			if smoke_runtime_closure_contract "$candidate" && smoke_private_tmpfs_contract "$candidate" &&
			  smoke_worker_group_isolation_contract "$candidate" &&
			  smoke_post_restart_capability_contract "$candidate" && smoke_raster_output_contract "$candidate" &&
			  smoke_resource_profile_contract "$candidate" &&
			  smoke_media_probe_contract "$candidate" &&
		  smoke_media_probe_host_boundary_contract "$candidate"; then
			fail "smoke contract accepted unsafe mutation: $name"
    fi
    return 0
  fi
  if run_static "$smoke" "$dockerfile" "$entrypoint" "$seccomp" "$workflow" >"$output" 2>&1; then
    fail "smoke accepted unsafe mutation: $name"
  fi
}

run_static "$SMOKE" "$BASE_DOCKERFILE" "$BASE_ENTRYPOINT" "$BASE_SECCOMP" "$BASE_CI"

run_fake_runtime amd64 "$AMD64_CLOSURE" "$AMD64_CLOSURE" ""
run_fake_runtime arm64 "$ARM64_CLOSURE" "" "$ARM64_CLOSURE"
expect_runtime_failure missing-amd64 amd64 "$AMD64_CLOSURE" "$TMP_DIR/missing-amd64.json" "$ARM64_CLOSURE"
expect_runtime_failure swapped-architectures amd64 "$AMD64_CLOSURE" "$ARM64_CLOSURE" "$AMD64_CLOSURE"
expect_runtime_failure wrong-architecture amd64 "$AMD64_CLOSURE" "$WRONG_ARCH_CLOSURE" "$ARM64_CLOSURE"
expect_runtime_failure tampered-native amd64 "$AMD64_CLOSURE" "$TAMPERED_AMD64_CLOSURE" "$ARM64_CLOSURE"
expect_runtime_failure writable-regular-mode amd64 "$WRITABLE_AMD64_CLOSURE" "$WRITABLE_AMD64_CLOSURE" "$ARM64_CLOSURE"
expect_runtime_smoke_mutation_failure missing-worker-private-tmp-create \
  '/--tmpfs \/tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000/d' \
  'runtime smoke did not capture the exact Worker /tmp create contract'
expect_runtime_smoke_mutation_failure drifted-worker-private-tmp-create \
  '/--tmpfs \/tmp:/s/mode=0700/mode=0750/' \
  'runtime smoke did not capture the exact Worker /tmp create contract'
expect_runtime_smoke_mutation_failure missing-clamav-database-tmp-create \
  '/--tmpfs \/var\/lib\/xirang\/asset-worker-bundles\/active\/clamav:/d' \
  'runtime smoke did not capture the exact ClamAV database tmpfs contract'
expect_runtime_smoke_mutation_failure drifted-clamav-database-tmp-create \
  '/--tmpfs \/var\/lib\/xirang\/asset-worker-bundles\/active\/clamav:/s/mode=0700/mode=0750/' \
  'runtime smoke did not capture the exact ClamAV database tmpfs contract'

expect_mutation_failure unpinned-runtime dockerfile 's/@sha256:[0-9a-f]\{64\}//'
expect_mutation_failure unpinned-builder dockerfile '0,/@sha256:[0-9a-f]\{64\}/s///'
expect_mutation_failure unpinned-tool-package dockerfile 's/ffmpeg=[^[:space:]\\]*/ffmpeg/'
expect_mutation_failure root-user dockerfile 's/USER 10000:10000/USER 0:0/'
expect_mutation_failure embedded-updater-group dockerfile 's/addgroup -S -g 10002 asset-updater/addgroup -S -g 10002 asset-updater \&\& addgroup asset-worker asset-updater/'
expect_mutation_failure public-port dockerfile '/USER 10000:10000/a\EXPOSE 9443'
expect_mutation_failure missing-runtime-closure dockerfile '/write-runtime-closure-manifest/d'
expect_mutation_failure mutable-runtime-closure dockerfile 's/444:0:0/644:0:0/'
expect_mutation_failure writable-runtime-package-hook dockerfile '/chmod 0755 \/etc\/network\/if-up.d\/dad/d'
expect_mutation_failure nondeterministic-shadow dockerfile '/sed -i -E.*\/etc\/shadow/d'
expect_mutation_failure retained-unreadable-runtime-files dockerfile '/rm -f \/etc\/crontabs\/root \/etc\/shadow- \/lib\/apk\/db\/lock/d'
expect_mutation_failure nondeterministic-package-output dockerfile '/rm -f \/var\/cache\/fontconfig/d'
expect_mutation_failure missing-image-closure-copy smoke '/"$DOCKER" cp "$container_id:\/usr\/local\/share\/xirang\/runtime-closure.v1.json"/d'
expect_mutation_failure missing-image-closure-compare smoke '/cmp -s "$IMAGE_RUNTIME_CLOSURE"/d'
expect_mutation_failure unsigned-profile-closure smoke '/generate_profile_fixture "$PROFILE_DIR" "$amd64_runtime_closure_sha256" "$arm64_runtime_closure_sha256"/s/ "$amd64_runtime_closure_sha256" "$arm64_runtime_closure_sha256"//'
expect_mutation_failure missing-worker-private-tmp-runtime smoke '/--tmpfs \/tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000/d'
expect_mutation_failure permissive-worker-private-tmp-runtime smoke 's#Tmpfs\["/tmp"\] == "rw,noexec,nosuid,nodev#Tmpfs["/tmp"] | contains("rw")#'
expect_mutation_failure missing-clamav-database-tmp-runtime smoke '/--tmpfs \/var\/lib\/xirang\/asset-worker-bundles\/active\/clamav:/d'
expect_mutation_failure missing-parser-supplementary-group-runtime smoke '0,/\*" 10002 "\*) exit 1 ;;/s//\*" 10003 "\*) exit 1 ;;/'
expect_mutation_failure missing-forbidden-worker-group-injection smoke '/^[[:space:]]*--group-add /d'
expect_mutation_failure worker-runtime-jq smoke '/cat "$media_probe_stdout"/i\    jq -e . "$media_probe_stdout" >/dev/null'
expect_mutation_failure missing-host-media-probe-validation smoke '/<<<"$MEDIA_PROBE_JSON"/d'
expect_mutation_failure missing-post-core-worker-capability-assertion smoke '/profile_compose restart xirang/,/post-restart parser capability fingerprint did not persist/ {/profile_assert_worker_capabilities "$core_id"/d;}'
expect_mutation_failure missing-post-core-api-capability-assertion smoke '/profile_compose restart xirang/,/post-restart parser capability fingerprint did not persist/ {/profile_assert_core_capabilities/d;}'
expect_mutation_failure drifted-vips-thumbnail-cpu-limit smoke 's/run_asset_tool vips vips_thumbnail_v1 90 /run_asset_tool vips vips_thumbnail_v1 120 /'
expect_mutation_failure drifted-vips-thumbnail-width smoke 's/--width=16/--width=17/'
expect_mutation_failure drifted-media-preview-process-limit smoke 's/run_asset_tool ffmpeg media_preview_v1 1800 4294967296 536870912 32 /run_asset_tool ffmpeg media_preview_v1 1800 4294967296 536870912 4 /'
expect_mutation_failure missing-xz-resource-block smoke '/^[[:space:]]*run_asset_tool xz xz_decompress_v1 /s/ 600 1073741824 16777216 4 / /'
expect_mutation_failure commented-sandbox-invocations smoke '/^[[:space:]]*run_asset_tool /s/^[[:space:]]*/&# /'
expect_mutation_failure copied-gzip-profile-into-xz smoke 's/run_asset_tool xz xz_decompress_v1/run_asset_tool gzip gzip_decompress_v1/'
expect_mutation_failure missing-invocation-region-begin smoke '/ASSET_TOOL_SMOKE_INVOCATIONS_BEGIN/d'
expect_mutation_failure missing-invocation-region-end smoke '/ASSET_TOOL_SMOKE_INVOCATIONS_END/d'
expect_mutation_failure empty-vips-home smoke '/run_asset_tool vips /s#"$vips_job/home"#""#'
expect_mutation_failure xz-control-suffix smoke '/run_asset_tool xz /s/$/ || true/'
expect_mutation_failure missing-worker-uid-check entrypoint '/WORKER_UID=10000/d'
expect_mutation_failure missing-worker-group-guard entrypoint '/require_exact_groups "$WORKER_GID"/d'
expect_mutation_failure missing-updater-group-guard entrypoint '/require_exact_groups "$UPDATER_GID"/d'
expect_mutation_failure missing-worker-private-tmp-check entrypoint '/require_private_tmpfs \/tmp "$WORKER_UID" "$WORKER_GID"/d'
expect_mutation_failure missing-worker-workspace-tmpfs-check entrypoint '/require_private_tmpfs "\$WORKSPACE_ROOT" "\$WORKER_UID" "\$WORKER_GID"/d'
expect_mutation_failure permissive-worker-private-tmp-count entrypoint 's/exact_mounts == 1/exact_mounts >= 1/'
expect_mutation_failure permissive-worker-private-tmp-filesystem entrypoint 's/filesystem == "tmpfs"/filesystem != ""/'
expect_mutation_failure missing-derived-root-check entrypoint '/require_directory "$DERIVED_ROOT" 700/d'
expect_mutation_failure missing-derived-root-mode entrypoint '/chmod 0700 "$DERIVED_ROOT"/d'
expect_mutation_failure writable-bundle-check entrypoint 's/require_mount_option "$BUNDLE_ROOT" ro/require_mount_option "$BUNDLE_ROOT" rw/'
expect_mutation_failure permissive-seccomp seccomp 's/"SCMP_ACT_ERRNO"/"SCMP_ACT_ALLOW"/'
expect_mutation_failure missing-mount-denial seccomp 's/"mount", //'
expect_mutation_failure worker-publish workflow '/# asset-worker-no-publish/a\      - run: docker push example.invalid/xirang-asset-worker:test'
expect_mutation_failure missing-closure-producer workflow '/^  asset-worker-closure:/,/^  asset-worker:/d'
expect_mutation_failure missing-closure-dependency workflow '/^[[:space:]]*needs: asset-worker-closure$/d'
expect_mutation_failure unpinned-closure-upload workflow 's#actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a#actions/upload-artifact@main#'
expect_mutation_failure unpinned-closure-download workflow 's#actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c#actions/download-artifact@main#'
expect_mutation_failure swapped-amd64-artifact-path workflow 's#asset-worker-runtime-closure-amd64/runtime-closure.v1.json#asset-worker-runtime-closure-arm64/runtime-closure.v1.json#'

echo "asset Worker smoke self-test: PASS"
