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
PROFILE_SMOKE=${ASSET_WORKER_PROFILE_SMOKE:-0}
PROFILE_NETWORK_MODE=${ASSET_WORKER_PROFILE_NETWORK_MODE:-bridge}
PROFILE_CORE_TAG=${ASSET_WORKER_CORE_IMAGE_TAG:-}
PROFILE_PROJECT=${ASSET_WORKER_PROFILE_PROJECT:-xirang-asset-worker-smoke-$$}
AMD64_RUNTIME_CLOSURE=${ASSET_WORKER_AMD64_RUNTIME_CLOSURE:-}
ARM64_RUNTIME_CLOSURE=${ASSET_WORKER_ARM64_RUNTIME_CLOSURE:-}
VALIDATE_RUNTIME_CLOSURE_ONLY=${ASSET_WORKER_VALIDATE_RUNTIME_CLOSURE:-}
PROFILE_COMPOSE_FILES=()

fail() {
  echo "asset Worker smoke: $*" >&2
  exit 1
}

profile_compose() {
  IMAGE_TAG="$PROFILE_CORE_TAG" ASSET_WORKER_IMAGE_TAG="${IMAGE#xirang-asset-worker:}" \
    "$DOCKER" compose -p "$PROFILE_PROJECT" "${PROFILE_COMPOSE_FILES[@]}" --profile asset-worker "$@"
}

validate_runtime_closure() {
  local manifest=$1
  local platform=$2
  [[ -f "$manifest" && ! -L "$manifest" ]] || fail "runtime closure artifact is missing: $platform"
  jq -e --arg platform "$platform" '
    (keys == ["files", "platform", "schema_version"]) and
    .schema_version == 1 and .platform == $platform and
    (.files | type == "array" and length > 0 and length <= 100000) and
    ([.files[].path] == ([.files[].path] | sort)) and
    ([.files[].path] | length == (unique | length)) and
    all(.files[];
      (keys == ["kind", "mode", "path", "sha256", "size"]) and
      (.kind == "regular" or .kind == "symlink") and
      (.path | type == "string" and startswith("/") and . != "/" and (test("[\u0000\r\n]") | not)) and
      (.mode | type == "number" and . >= 0 and . <= 511) and
      (if .kind == "symlink" then
        .mode == 511
      else
        (((.mode / 16 | floor) % 2) == 0 and ((.mode / 2 | floor) % 2) == 0)
      end) and
      (.size | type == "number" and . >= 0) and
      (.sha256 | type == "string" and test("^[0-9a-f]{64}$")))
  ' "$manifest" >/dev/null || fail "runtime closure artifact is invalid: $platform"
}

for path in "$DOCKERFILE" "$ENTRYPOINT" "$SECCOMP" "$WORKFLOW"; do
  [[ -f "$path" ]] || fail "required contract file not found: $path"
done
command -v jq >/dev/null 2>&1 || fail "jq is required"
bash -n "$ENTRYPOINT" || fail "entrypoint syntax is invalid"

if [[ -n "$VALIDATE_RUNTIME_CLOSURE_ONLY" ]]; then
  [[ "$EXPECTED_ARCH" == "amd64" || "$EXPECTED_ARCH" == "arm64" ]] ||
    fail "EXPECTED_WORKER_ARCH must be amd64 or arm64"
  validate_runtime_closure "$VALIDATE_RUNTIME_CLOSURE_ONLY" "linux/$EXPECTED_ARCH"
  echo "asset Worker runtime closure artifact: PASS"
  exit 0
fi

grep -Eq '^FROM golang:1\.26\.6-alpine@sha256:[0-9a-f]{64} AS worker-builder$' "$DOCKERFILE" || fail "builder base is not digest pinned"
grep -Eq '^FROM alpine:3\.23@sha256:[0-9a-f]{64}$' "$DOCKERFILE" || fail "runtime base is not digest pinned"
grep -Fqx -- 'USER 10000:10000' "$DOCKERFILE" || fail "image metadata user is not fixed non-root"
if grep -Eq 'addgroup[[:space:]]+asset-worker[[:space:]]+asset-updater' "$DOCKERFILE"; then
  fail "parser image identity includes updater supplementary group"
fi
grep -Fq -- 'go build -trimpath' "$DOCKERFILE" || fail "Worker binaries are not built in-image"
for binary in asset-worker asset-tool-sandbox asset-worker-updater; do
  grep -Fq -- "-o /out/$binary" "$DOCKERFILE" || fail "missing binary build: $binary"
done
for contract in \
	  'mkdir -p /usr/local/share/xirang' \
	  'chmod 0755 /etc/network/if-up.d/dad' \
	  "sed -i -E 's/^(clamav|asset-worker|asset-updater):([^:]*):[^:]*/\\1:\\2:/' /etc/shadow" \
	  'test "$(stat -c '\''%a:%u:%g'\'' /etc/shadow)" = "640:0:42"' \
	  'rm -f /etc/crontabs/root /etc/shadow- /lib/apk/db/lock' \
	  'rm -f /var/cache/fontconfig/*.cache-* /var/log/apk.log' \
	  '! find / -xdev -type f -perm /022 -print -quit | grep -q .' \
	  '/usr/local/bin/asset-worker write-runtime-closure-manifest' \
  'runtime-closure.v1.json' \
  "444:0:0"; do
  grep -Fq -- "$contract" "$DOCKERFILE" || fail "runtime closure build contract missing: $contract"
done
for package in \
  'gcc=15.2.0-r5' \
  'musl-dev=1.2.6-r2' \
  'bash=5.3.3-r1' \
  'clamav=1.4.4-r0' \
  'ffmpeg=8.0.1-r1' \
  'font-noto=2025.12.01-r0' \
  'font-noto-cjk=0_git20220127-r1' \
  'libcrypto3=3.5.8-r0' \
  'libreoffice=25.8.1.1-r5' \
  'libssl3=3.5.8-r0' \
  'poppler-utils=25.12.0-r0' \
  'tesseract-ocr=5.5.1-r0' \
  'tesseract-ocr-data-chi_sim=5.5.1-r0' \
  'tesseract-ocr-data-eng=5.5.1-r0' \
  'vips-tools=8.17.3-r1' \
  'gzip=1.14-r3' \
  'xz=5.8.3-r0' \
  'zstd=1.5.7-r2'; do
  grep -Eq "^[[:space:]]*$package([[:space:]\\]|]|$)" "$DOCKERFILE" || fail "missing pinned closed toolchain package: $package"
done
if grep -Fq -- 'apk upgrade' "$DOCKERFILE"; then
  fail "Worker image performs nondeterministic apk upgrade"
fi
if grep -Eq '^[[:space:]]*(EXPOSE|HEALTHCHECK)[[:space:]]' "$DOCKERFILE"; then
  fail "Worker image declared a public/runtime endpoint"
fi

for contract in \
  'WORKER_UID=10000' \
  'UPDATER_UID=10002' \
  'group_ids=$(id -G) || fail' \
  '[ "$group_ids" = "$expected_gid" ] || fail' \
  'require_exact_groups "$WORKER_GID"' \
  'require_exact_groups "$UPDATER_GID"' \
	  'require_mount_option "$WORKSPACE_ROOT" noexec' \
	  'require_mount_option "$WORKSPACE_ROOT" nosuid' \
	  'require_mount_option "$WORKSPACE_ROOT" nodev' \
	  'require_private_tmpfs "$WORKSPACE_ROOT" "$WORKER_UID" "$WORKER_GID"' \
	  'require_mount_option "$BUNDLE_ROOT" ro' \
	  'require_directory "$target" 700 "$uid" "$gid"' \
	  'exact_mounts == 1 && filesystem == "tmpfs" && rw && noexec && nosuid && nodev' \
	  'require_private_tmpfs /tmp "$WORKER_UID" "$WORKER_GID"' \
	  'require_socket "$WORKER_SOCKET" 600' \
  'require_socket "$UPDATER_SOCKET" 660' \
  'require_directory "$RUNTIME_ROOT" 2770' \
	  'require_directory "$DERIVED_ROOT" 700' \
	  'require_directory "$EXPORT_ROOT" 700' \
	  'DERIVED_ROOT=/var/lib/xirang-asset-runtime/derived' \
	  'EXPORT_ROOT=/var/lib/xirang-asset-runtime/export' \
	  'chmod 0700 "$DERIVED_ROOT"' \
	  'chmod 0700 "$EXPORT_ROOT"' \
  'chmod 2770 "$RUNTIME_ROOT"'; do
  grep -Fq -- "$contract" "$ENTRYPOINT" || fail "entrypoint contract missing: $contract"
done

jq -e '
  .defaultAction == "SCMP_ACT_ALLOW" and
  ([.syscalls[] | select(.action == "SCMP_ACT_ERRNO") | .names[]] as $denied |
    ["mount", "umount2", "ptrace", "keyctl", "bpf", "perf_event_open", "init_module", "setns", "unshare"] |
    all(. as $name | $denied | index($name) != null)) and
  ([.syscalls[] | select(.action == "SCMP_ACT_ERRNO" and (.names | index("socket") != null))] | length) == 2
' "$SECCOMP" >/dev/null || fail "seccomp denied-syscall contract is incomplete"

CLOSURE_JOB_FILE=$(mktemp)
JOB_FILE=$(mktemp)
cleanup_contract_files() {
  rm -f -- "$CLOSURE_JOB_FILE" "$JOB_FILE"
}
trap cleanup_contract_files EXIT
extract_workflow_job() {
  local job=$1
  awk -v job="$job" '
    $0 == "  " job ":" { inside = 1 }
    inside && /^  [A-Za-z0-9_-]+:/ && $1 != job ":" { exit }
    inside { print }
  ' "$WORKFLOW"
}
extract_workflow_job asset-worker-closure >"$CLOSURE_JOB_FILE"
extract_workflow_job asset-worker >"$JOB_FILE"
[[ -s "$CLOSURE_JOB_FILE" ]] || fail "CI asset-worker-closure job is missing"
[[ -s "$JOB_FILE" ]] || fail "CI asset-worker job is missing"
for contract in \
  '# asset-worker-closure-no-publish' \
  'arch: amd64' \
  'arch: arm64' \
  'docker build --platform "linux/${{ matrix.arch }}"' \
  'ASSET_WORKER_VALIDATE_RUNTIME_CLOSURE' \
  'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1' \
  'name: asset-worker-runtime-closure-${{ matrix.arch }}'; do
  grep -Fq -- "$contract" "$CLOSURE_JOB_FILE" || fail "CI closure producer contract missing: $contract"
done
for contract in \
  '# asset-worker-no-publish' \
  'needs: asset-worker-closure' \
  'arch: amd64' \
  'arch: arm64' \
  'actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1' \
  'name: asset-worker-runtime-closure-amd64' \
  'name: asset-worker-runtime-closure-arm64' \
  'ASSET_WORKER_AMD64_RUNTIME_CLOSURE: ${{ runner.temp }}/asset-worker-runtime-closure-amd64/runtime-closure.v1.json' \
  'ASSET_WORKER_ARM64_RUNTIME_CLOSURE: ${{ runner.temp }}/asset-worker-runtime-closure-arm64/runtime-closure.v1.json' \
  'docker build --platform "linux/${{ matrix.arch }}"' \
  'bash scripts/check-compose-config.test.sh' \
  'bash scripts/test-asset-worker.sh' \
  'aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25'; do
  grep -Fq -- "$contract" "$JOB_FILE" || fail "CI Worker contract missing: $contract"
done
if grep -Eiq 'docker[[:space:]]+(login|push|tag)|docker/login-action|docker/build-push-action|push:[[:space:]]*true|create-release|publish-release' \
  "$CLOSURE_JOB_FILE" "$JOB_FILE"; then
  fail "CI Worker job contains a publication action"
fi

if [[ "${ASSET_WORKER_STATIC_ONLY:-0}" == "1" ]]; then
  echo "asset Worker static contract: PASS"
  exit 0
fi
[[ -n "$IMAGE" ]] || fail "ASSET_WORKER_IMAGE is required for runtime smoke"
[[ "$EXPECTED_ARCH" == "amd64" || "$EXPECTED_ARCH" == "arm64" ]] || fail "EXPECTED_WORKER_ARCH must be amd64 or arm64"
case "$EXPECTED_ARCH" in
  amd64)
    validate_runtime_closure "$AMD64_RUNTIME_CLOSURE" linux/amd64
    expected_runtime_closure=$AMD64_RUNTIME_CLOSURE
    ;;
  arm64)
    validate_runtime_closure "$ARM64_RUNTIME_CLOSURE" linux/arm64
    expected_runtime_closure=$ARM64_RUNTIME_CLOSURE
    ;;
esac
if ! "$DOCKER" info >/dev/null 2>&1; then
  fail "Docker daemon is required"
fi

IMAGE_JSON=$(mktemp)
CONTAINER_JSON=$(mktemp)
IMAGE_RUNTIME_CLOSURE=$(mktemp)
container_id=
PROFILE_DIR=
PROFILE_OWNED=0
PROFILE_HELPER_DIR=
PROFILE_FIXTURE_JSON=
PROFILE_API_TOKEN=
PROFILE_API_BODY=
cleanup_profile_dir() {
  [[ -n "$PROFILE_DIR" ]] || return 0
  "$DOCKER" run --rm \
    --network none \
    --user 0:0 \
    --read-only \
    --cap-drop ALL \
    --cap-add DAC_OVERRIDE \
    --security-opt no-new-privileges=true \
    --entrypoint /bin/sh \
    -v "$PROFILE_DIR:/profile" \
    "$IMAGE" -eu -c 'rm -rf /profile/* /profile/.[!.]* /profile/..?*' \
    >/dev/null 2>&1 || true
  rm -rf -- "$PROFILE_DIR" >/dev/null 2>&1 || true
}
cleanup() {
  if [[ -n "$container_id" ]]; then
    "$DOCKER" rm -f "$container_id" >/dev/null 2>&1 || true
  fi
  if [[ -n "$PROFILE_HELPER_DIR" ]]; then
    rm -rf -- "$PROFILE_HELPER_DIR" >/dev/null 2>&1 || true
  fi
  if [[ "$PROFILE_OWNED" == "1" && -n "$PROFILE_DIR" ]]; then
    profile_compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  cleanup_profile_dir
  rm -f -- "$IMAGE_JSON" "$CONTAINER_JSON" "$IMAGE_RUNTIME_CLOSURE" "$CLOSURE_JOB_FILE" "$JOB_FILE"
}
trap cleanup EXIT

generate_profile_fixture() {
  local profile_root=$1
  local amd64_runtime_closure_sha256=$2
  local arm64_runtime_closure_sha256=$3
  PROFILE_HELPER_DIR=$(mktemp -d "$ROOT_DIR/backend/profile_smoke_fixture_XXXXXX")
  cat >"$PROFILE_HELPER_DIR/main.go" <<'GO'
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/processing/updater"
)

type trustKeyDocument struct {
	ID          string `json:"id"`
	PublicKey   string `json:"public_key"`
	ActiveFrom  string `json:"active_from"`
	RetireAfter string `json:"retire_after"`
}

type trustDocument struct {
	SchemaVersion int                `json:"schema_version"`
	Keys          []trustKeyDocument `json:"keys"`
}

type fixtureResult struct {
	BundleFingerprint   string `json:"bundle_fingerprint"`
	PipelineFingerprint string `json:"pipeline_fingerprint"`
}

type runtimeClosureAttestations struct {
	SchemaVersion int                         `json:"schema_version"`
	Attestations  []runtimeClosureAttestation `json:"attestations"`
}

type runtimeClosureAttestation struct {
	Platform              string `json:"platform"`
	RuntimeManifestSHA256 string `json:"runtime_manifest_sha256"`
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func writeFile(path string, payload []byte, mode os.FileMode) {
	check(os.WriteFile(path, payload, mode))
	check(os.Chmod(path, mode))
}

func lowerHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func main() {
	if len(os.Args) != 4 || !lowerHex(os.Args[2]) || !lowerHex(os.Args[3]) {
		panic("fixture output root and exact amd64/arm64 runtime closure digests are required")
	}
	root := os.Args[1]
	attestationPayload, err := json.Marshal(runtimeClosureAttestations{
		SchemaVersion: 1,
		Attestations: []runtimeClosureAttestation{
			{Platform: "linux/amd64", RuntimeManifestSHA256: os.Args[2]},
			{Platform: "linux/arm64", RuntimeManifestSHA256: os.Args[3]},
		},
	})
	check(err)
	profilesByCapability := make(map[string][]string)
	schemas := make(map[string]string)
	for _, profile := range capabilityspec.WorkerProfiles() {
		profilesByCapability[profile.Capability] = append(profilesByCapability[profile.Capability], profile.OutputProfile)
		schemas[profile.Capability] = profile.CapabilitySchema
	}
	capabilityNames := make([]string, 0, len(profilesByCapability))
	for capability := range profilesByCapability {
		capabilityNames = append(capabilityNames, capability)
	}
	sort.Strings(capabilityNames)
	manifestCapabilities := make([]updater.ManifestCapability, 0, len(capabilityNames))
	for _, capability := range capabilityNames {
		profiles := profilesByCapability[capability]
		sort.Strings(profiles)
		manifestCapabilities = append(manifestCapabilities, updater.ManifestCapability{
			Capability: capability, Schema: schemas[capability], Profiles: profiles,
			ToolRevision: "profile-smoke-toolchain-v2", ModelRevision: "profile-smoke-model-v1", DataRevision: "none",
		})
	}
	now := time.Now().UTC().Truncate(time.Second)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	check(err)
	bundle, files, err := updater.BuildCanonicalTar([]updater.BundleFilePayload{
		{Path: "models/model.dat", Mode: 0o444, Content: []byte("profile-smoke-model\n")},
		{Path: "toolchain/attestations.v1.json", Mode: 0o444, Content: attestationPayload},
	})
	check(err)
	manifest := updater.Manifest{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "profile-smoke", Version: "1.0.0",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Capabilities: manifestCapabilities,
		Files: files, BundleSHA256: updater.SHA256Hex(bundle), SigningKeyID: "profile-smoke-key",
		SignatureAlgorithm: "ed25519",
	}
	check(updater.SignManifest(&manifest, privateKey))
	manifestPayload, err := json.Marshal(manifest)
	check(err)
	trust := updater.TrustStore{Keys: []updater.TrustedKey{{
		ID: "profile-smoke-key", PublicKey: publicKey,
		ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}
	verified, err := updater.VerifyPackage(manifestPayload, bundle, trust, now)
	check(err)

	bundles := make(processing.CapabilityBundleFingerprints)
	for _, profile := range capabilityspec.WorkerProfiles() {
		bundles[profile.Capability] = []string{verified.BundleFingerprint}
	}
	capabilities, err := processing.NewProductionWorkerCapabilitySetWithBundles(bundles)
	check(err)
	pipelineFingerprint := ""
	for _, advertisement := range capabilities.Advertisements() {
		if advertisement.Capability == capabilityspec.CapabilityImageOCR &&
			advertisement.OutputProfile == capabilityspec.ProfileTesseractTextV1 {
			pipelineFingerprint = advertisement.PipelineFingerprint
		}
	}
	if pipelineFingerprint == "" {
		panic("OCR pipeline fingerprint is unavailable")
	}

	trustPayload, err := json.Marshal(trustDocument{SchemaVersion: 1, Keys: []trustKeyDocument{{
		ID: "profile-smoke-key", PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		ActiveFrom: now.Add(-time.Hour).Format(time.RFC3339),
		RetireAfter: now.Add(time.Hour).Format(time.RFC3339),
	}}})
	check(err)
	writeFile(filepath.Join(root, "asset-worker-updater-trust.json"), trustPayload, 0o440)
	candidate := filepath.Join(root, "profile-smoke-candidate")
	check(os.Mkdir(candidate, 0o755))
	writeFile(filepath.Join(candidate, "bundle.tar"), bundle, 0o444)
	writeFile(filepath.Join(candidate, "manifest.json"), manifestPayload, 0o444)
	check(os.Chmod(candidate, 0o555))
	check(json.NewEncoder(os.Stdout).Encode(fixtureResult{
		BundleFingerprint: verified.BundleFingerprint, PipelineFingerprint: pipelineFingerprint,
	}))
}
GO

  local helper_package fixture_json
  helper_package=${PROFILE_HELPER_DIR#"$ROOT_DIR/backend/"}
  fixture_json=$(cd "$ROOT_DIR/backend" && go run "./$helper_package" "$profile_root" \
    "$amd64_runtime_closure_sha256" "$arm64_runtime_closure_sha256")
  rm -rf -- "$PROFILE_HELPER_DIR"
  PROFILE_HELPER_DIR=
  jq -e '
    (.bundle_fingerprint | type == "string" and test("^[0-9a-f]{64}$")) and
    (.pipeline_fingerprint | type == "string" and test("^[0-9a-f]{64}$"))
  ' <<<"$fixture_json" >/dev/null || fail "generated profile fixture metadata is invalid"
  PROFILE_FIXTURE_JSON=$fixture_json
}

profile_api_request() {
  local expected_status=$1
  local method=$2
  local path=$3
  local body=${4-}
  local response status
  local -a arguments=(-sS --max-time 20 -X "$method" "http://127.0.0.1:10761$path" -w $'\n%{http_code}')
  if [[ -n "$PROFILE_API_TOKEN" ]]; then
    arguments+=(-H "Authorization: Bearer $PROFILE_API_TOKEN")
  fi
  if [[ -n "$body" ]]; then
    arguments+=(-H 'Content-Type: application/json' --data "$body")
  fi
  response=$(curl "${arguments[@]}") || fail "profile API request failed: $method $path"
  status=${response##*$'\n'}
  PROFILE_API_BODY=${response%$'\n'*}
	[[ "$status" == "$expected_status" ]] || fail "profile API returned HTTP $status for $method $path"
}

profile_assert_worker_capabilities() {
  local core_id=$1 actual expected
  actual=$("$DOCKER" exec "$core_id" sqlite3 -readonly -cmd '.timeout 5000' /data/xirang.db '
    SELECT capabilities.capability || '\''/'\'' || capabilities.output_profile
    FROM backup_asset_worker_capabilities AS capabilities
    JOIN backup_asset_worker_identities AS workers ON workers.id = capabilities.worker_id
    WHERE workers.trust_state = '\''active'\'' AND workers.health_state = '\''ready'\''
      AND capabilities.health_state = '\''ready'\''
    ORDER BY capabilities.capability, capabilities.output_profile;
  ') || fail "physical Worker capability query failed"
  expected=$(printf '%s\n' \
    'archive.extract_entry/archive_member_v1' \
    'archive.inspect/archive_index_v1' \
    'document.convert/static_pages_v1' \
    'image.ocr/tesseract_text_v1' \
    'image.thumbnail/raster_thumbnail_v1' \
    'malware.scan/signature_scan_v1' \
    'media.probe/media_probe_v1' \
    'media.transcode/browser_preview_v1' \
    'secret.classify/bounded_secret_v1' \
    'text.extract/bounded_text_v1')
  [[ "$actual" == "$expected" ]] || fail "worker capability/profile set did not match the closed 10-profile contract"
}

profile_assert_core_capabilities() {
  jq -e '
    .data.schema_version == 1 and
    ([.data.items[] | (.capability + "/" + .profile)] | sort) == [
      "archive.extract_entry/archive_member_v1",
      "archive.inspect/archive_index_v1",
      "document.convert/static_pages_v1",
      "image.ocr/tesseract_text_v1",
      "image.thumbnail/raster_thumbnail_v1",
      "malware.scan/signature_scan_v1",
      "media.probe/media_probe_v1",
      "media.transcode/browser_preview_v1",
      "text.extract/bounded_text_v1"
    ]
  ' <<<"$PROFILE_API_BODY" >/dev/null || fail "Core capability projection did not match the closed 9-profile policy"
}

"$DOCKER" image inspect "$IMAGE" >"$IMAGE_JSON"
jq -e --arg arch "$EXPECTED_ARCH" '
  length == 1 and .[0].Architecture == $arch and .[0].Config.User == "10000:10000" and
  .[0].Config.Entrypoint == ["/usr/local/bin/asset-worker-entrypoint"] and
  .[0].Config.ExposedPorts == null
' "$IMAGE_JSON" >/dev/null || fail "Worker image metadata is unsafe"

for supplementary_gid in 10002 10003; do
  if forbidden_group_output=$("$DOCKER" run --rm \
      --user 10000:10000 \
      --group-add "$supplementary_gid" \
      --network none \
      --read-only \
      --cap-drop ALL \
      --security-opt no-new-privileges=true \
      --security-opt "seccomp=$SECCOMP" \
      --entrypoint /bin/sh \
      "$IMAGE" -x /usr/local/bin/asset-worker-entrypoint worker 2>&1); then
    fail "entrypoint accepted parser supplementary group $supplementary_gid"
  fi
  grep -Fq -- '+ require_exact_groups 10000' <<<"$forbidden_group_output" ||
    fail "entrypoint did not reject parser supplementary group $supplementary_gid at the identity boundary"
  if grep -Fq -- '+ require_private_tmpfs' <<<"$forbidden_group_output"; then
    fail "supplementary parser group $supplementary_gid reached parser mount validation"
  fi
done

container_id=$("$DOCKER" create \
  --user 10000:10000 \
  --network none \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges=true \
  --security-opt "seccomp=$SECCOMP" \
	  --pids-limit 256 \
	  --memory 1g \
	  --memory-swap 1g \
	  --cpus 2 \
  --tmpfs /run/xirang/asset-jobs:rw,noexec,nosuid,nodev,size=64m,nr_inodes=8192,mode=0700,uid=10000,gid=10000 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000 \
  --tmpfs /var/lib/xirang/asset-worker-bundles/active/clamav:rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000 \
  --entrypoint /bin/sh \
  "$IMAGE" -eu -c '
    test "$(id -u):$(id -g)" = "10000:10000"
    worker_groups=" $(id -G) "
    case "$worker_groups" in
      *" 10000 "*) ;;
      *) exit 1 ;;
    esac
    case "$worker_groups" in
      *" 10002 "*) exit 1 ;;
    esac
    valid_utf8_text_file() {
      LC_ALL=C od -An -v -tu1 "$1" | awk "
        BEGIN { remaining = 0; minimum = 128; maximum = 191; invalid = 0 }
        {
          for (position = 1; position <= NF && !invalid; position++) {
            byte = \$position + 0
            if (remaining > 0) {
              if (byte < minimum || byte > maximum) { invalid = 1; break }
              remaining--
              minimum = 128
              maximum = 191
              continue
            }
            if (byte == 0) { invalid = 1; break }
            if (byte <= 127) { continue }
            if (byte >= 194 && byte <= 223) { remaining = 1; continue }
            if (byte == 224) { remaining = 2; minimum = 160; continue }
            if (byte >= 225 && byte <= 236) { remaining = 2; continue }
            if (byte == 237) { remaining = 2; maximum = 159; continue }
            if (byte >= 238 && byte <= 239) { remaining = 2; continue }
            if (byte == 240) { remaining = 3; minimum = 144; continue }
            if (byte >= 241 && byte <= 243) { remaining = 3; continue }
            if (byte == 244) { remaining = 3; maximum = 143; continue }
            invalid = 1
          }
        }
        END { exit invalid || remaining != 0 ? 1 : 0 }
      "
    }
	    validate_office_output() {
      office_output_dir=$1
      office_output=$2
      office_home=$3
      office_buildid="$office_home/user/extensions/buildid"
      test -f "$office_output" && test ! -L "$office_output" && test -s "$office_output"
      test "$(find "$office_output_dir" -mindepth 1 -maxdepth 1 -type f | wc -l)" -eq 1
      test -z "$(find "$office_output_dir" -mindepth 1 -maxdepth 1 ! -type f -print -quit)"
      test "$(wc -c <"$office_output")" -le 1048576
      test "$(head -c 5 "$office_output")" = "%PDF-"
      pdfinfo "$office_output" 2>/dev/null | grep -Eq "^Pages:[[:space:]]+1[[:space:]]*$"
      test -f "$office_buildid" && test ! -L "$office_buildid" && test -s "$office_buildid"
      test "$(stat -c "%a:%u:%g" "$office_buildid")" = "600:10000:10000"
      test "$(wc -c <"$office_buildid")" -le 65
	      valid_utf8_text_file "$office_buildid"
	      grep -Eq "^[A-Za-z0-9().:+_-]{1,64}$" "$office_buildid"
	    }
	    run_asset_tool() {
	      (
	        run_executable=$1
	        run_profile=$2
	        run_cpu_seconds=$3
	        run_memory_bytes=$4
	        run_file_bytes=$5
	        run_processes=$6
	        run_input_mode=$7
	        run_input=$8
	        run_home=$9
	        run_output=${10}
	        shift 10
	        export HOME="$run_home"
	        export XIRANG_OUTPUT_DIR="$run_output"
	        export XIRANG_INPUT_MODE="$run_input_mode"
	        export XIRANG_RLIMIT_CPU_SECONDS="$run_cpu_seconds"
	        export XIRANG_RLIMIT_MEMORY_BYTES="$run_memory_bytes"
	        export XIRANG_RLIMIT_FSIZE_BYTES="$run_file_bytes"
	        export XIRANG_RLIMIT_PROCESSES="$run_processes"
	        export LANG=C.UTF-8
	        export LC_ALL=C.UTF-8
	        export TZ=UTC
	        case "$run_input_mode" in
	          path)
	            test "$run_input" != -
	            export XIRANG_INPUT_PATH="$run_input"
	            ;;
	          pipe)
	            test "$run_input" = -
	            unset XIRANG_INPUT_PATH
	            ;;
	          *) exit 2 ;;
	        esac
	        exec /usr/local/bin/asset-tool-sandbox --executable-id="$run_executable" --arg-profile="$run_profile" "$@"
	      )
	    }
	    for package in \
      bash=5.3.3-r1 \
      clamav=1.4.4-r0 \
      ffmpeg=8.0.1-r1 \
      font-noto=2025.12.01-r0 \
      font-noto-cjk=0_git20220127-r1 \
      libreoffice=25.8.1.1-r5 \
      poppler-utils=25.12.0-r0 \
      tesseract-ocr=5.5.1-r0 \
      tesseract-ocr-data-chi_sim=5.5.1-r0 \
      tesseract-ocr-data-eng=5.5.1-r0 \
      vips-tools=8.17.3-r1 \
      gzip=1.14-r3 \
      xz=5.8.3-r0 \
      zstd=1.5.7-r2; do
      apk info -e "$package" >/dev/null
    done
    for executable in \
      /usr/local/bin/asset-tool-sandbox \
      /usr/bin/vips \
      /usr/bin/tesseract \
      /usr/bin/pdftocairo \
      /usr/bin/pdftotext \
      /usr/bin/pdfinfo \
      /usr/lib/libreoffice/program/soffice.bin \
      /usr/bin/clamscan \
      /usr/bin/ffprobe \
      /usr/bin/ffmpeg \
      /bin/gzip \
      /usr/bin/xz \
      /usr/bin/zstd; do
      test -f "$executable" && test ! -L "$executable" && test -x "$executable" && test ! -w "$executable"
    done
    for data in \
      /usr/share/tessdata/eng.traineddata \
      /usr/share/tessdata/chi_sim.traineddata \
      /usr/share/fonts/noto/NotoSans-Regular.ttf \
      /usr/share/fonts/noto/NotoSansCJK-Regular.ttc; do
      test -f "$data" && test ! -L "$data" && test ! -w "$data"
    done
    languages=$(tesseract --list-langs 2>/dev/null)
    for language in eng chi_sim; do
      grep -Fxq "$language" <<EOF
$languages
EOF
    done
    codecs=$(ffmpeg -hide_banner -codecs 2>&1)
    for codec in h264 vp8 vp9 aac mp3 vorbis pcm_s16le png; do
      grep -Eq "[[:space:]]${codec}[[:space:]]" <<EOF
$codecs
EOF
    done
    encoders=$(ffmpeg -hide_banner -encoders 2>&1)
    for encoder in libx264 aac png; do
      grep -Eq "[[:space:]]${encoder}[[:space:]]" <<EOF
$encoders
EOF
    done
    job=/run/xirang/asset-jobs/job-smoke
    mkdir -m 0700 "$job" "$job/home" "$job/output"
    ffmpeg -nostdin -loglevel error -f lavfi -i color=c=black:s=16x16:d=0.1 -an \
      -c:v libx264 -pix_fmt yuv420p -f mp4 -y "$job/input.bin"
    chmod 0400 "$job/input.bin"
	    media_probe_stdout="$job/media-probe.stdout"
	    media_probe_stderr="$job/media-probe.stderr"
	    # ASSET_TOOL_SMOKE_INVOCATIONS_BEGIN
	    run_asset_tool ffprobe media_probe_v1 120 1073741824 1048576 16 path "$job/input.bin" "$job/home" "$job/output" >"$media_probe_stdout" 2>"$media_probe_stderr"
    chmod 0400 "$media_probe_stdout" "$media_probe_stderr"
    test ! -s "$media_probe_stderr"
    vips_job="$job/job-vips"
    mkdir -m 0700 "$vips_job" "$vips_job/home" "$vips_job/output"
    ffmpeg -nostdin -loglevel error -f lavfi -i color=c=white:s=32x32 -frames:v 1 -f image2 -y "$vips_job/input.bin"
    chmod 0400 "$vips_job/input.bin"
	    run_asset_tool vips vips_thumbnail_v1 90 1073741824 8388608 16 path "$vips_job/input.bin" "$vips_job/home" "$vips_job/output" --width=16 --height=16 --quality=80 >/dev/null 2>&1
    vips_thumbnail="$vips_job/output/thumbnail.png"
    test -f "$vips_thumbnail" && test ! -L "$vips_thumbnail" && test -s "$vips_thumbnail"
    test "$(find "$vips_job/output" -mindepth 1 -maxdepth 1 -type f | wc -l)" -eq 1
    test -z "$(find "$vips_job/output" -mindepth 1 -maxdepth 1 ! -type f -print -quit)"
    test "$(wc -c <"$vips_thumbnail")" -le 1048576
    vips_thumbnail_codec=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name \
      -of default=noprint_wrappers=1:nokey=1 "$vips_thumbnail" 2>/dev/null)
    vips_thumbnail_width=$(ffprobe -v error -select_streams v:0 -show_entries stream=width \
      -of default=noprint_wrappers=1:nokey=1 "$vips_thumbnail" 2>/dev/null)
    vips_thumbnail_height=$(ffprobe -v error -select_streams v:0 -show_entries stream=height \
      -of default=noprint_wrappers=1:nokey=1 "$vips_thumbnail" 2>/dev/null)
    test "$vips_thumbnail_codec" = png
    test "$vips_thumbnail_width" -eq 16
    test "$vips_thumbnail_height" -eq 16
    ocr_job="$job/job-ocr"
    mkdir -m 0700 "$ocr_job" "$ocr_job/home" "$ocr_job/output"
    ffmpeg -nostdin -loglevel error -f lavfi -i color=c=white:s=640x160 \
      -vf "drawtext=fontfile=/usr/share/fonts/noto/NotoSans-Regular.ttf:text=XIRANGOCR:fontcolor=black:fontsize=72:x=20:y=40" \
      -frames:v 1 -f image2 -y "$ocr_job/input.bin"
    chmod 0400 "$ocr_job/input.bin"
	    run_asset_tool tesseract tesseract_ocr_v1 300 2147483648 8388608 16 path "$ocr_job/input.bin" "$ocr_job/home" "$ocr_job/output" --language=eng >/dev/null 2>&1
    test -f "$ocr_job/output/ocr.txt" && test ! -L "$ocr_job/output/ocr.txt" && test -s "$ocr_job/output/ocr.txt"
    test "$(find "$ocr_job/output" -mindepth 1 -maxdepth 1 -type f | wc -l)" -eq 1
    test -z "$(find "$ocr_job/output" -mindepth 1 -maxdepth 1 ! -type f -print -quit)"
    test "$(wc -c <"$ocr_job/output/ocr.txt")" -le 1048576
    valid_utf8_text_file "$ocr_job/output/ocr.txt"
    grep -Fxq -- XIRANGOCR "$ocr_job/output/ocr.txt"
    pdf_source="$job/single-page.pdf"
    pdf_content="BT /F1 24 Tf 30 40 Td (XIRANG PDF) Tj ET"
    pdf_stream_length=$((${#pdf_content} + 1))
    printf "%%PDF-1.4\n" >"$pdf_source"
    pdf_offset_1=$(wc -c <"$pdf_source")
    printf "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" >>"$pdf_source"
    pdf_offset_2=$(wc -c <"$pdf_source")
    printf "2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" >>"$pdf_source"
    pdf_offset_3=$(wc -c <"$pdf_source")
    printf "3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 100] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>\nendobj\n" >>"$pdf_source"
    pdf_offset_4=$(wc -c <"$pdf_source")
    printf "4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n" >>"$pdf_source"
    pdf_offset_5=$(wc -c <"$pdf_source")
    printf "5 0 obj\n<< /Length %s >>\nstream\n%s\nendstream\nendobj\n" "$pdf_stream_length" "$pdf_content" >>"$pdf_source"
    pdf_xref=$(wc -c <"$pdf_source")
    {
      printf "xref\n0 6\n0000000000 65535 f \n"
      printf "%010d 00000 n \n" "$pdf_offset_1" "$pdf_offset_2" "$pdf_offset_3" "$pdf_offset_4" "$pdf_offset_5"
      printf "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%s\n%%%%EOF\n" "$pdf_xref"
    } >>"$pdf_source"
    chmod 0400 "$pdf_source"
    pdf_pages_job="$job/job-pdf-pages"
    mkdir -m 0700 "$pdf_pages_job" "$pdf_pages_job/home" "$pdf_pages_job/output"
    cp "$pdf_source" "$pdf_pages_job/input.bin"
    chmod 0400 "$pdf_pages_job/input.bin"
	    run_asset_tool pdftocairo pdf_pages_v1 600 2147483648 67108864 32 path "$pdf_pages_job/input.bin" "$pdf_pages_job/home" "$pdf_pages_job/output" >/dev/null 2>&1
    pdf_page="$pdf_pages_job/output/page-1.png"
    test -f "$pdf_page" && test ! -L "$pdf_page" && test -s "$pdf_page"
    test "$(find "$pdf_pages_job/output" -mindepth 1 -maxdepth 1 -type f | wc -l)" -eq 1
    test -z "$(find "$pdf_pages_job/output" -mindepth 1 -maxdepth 1 ! -type f -print -quit)"
    test "$(wc -c <"$pdf_page")" -le 1048576
    page_codec=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of default=noprint_wrappers=1:nokey=1 "$pdf_page" 2>/dev/null)
    page_width=$(ffprobe -v error -select_streams v:0 -show_entries stream=width -of default=noprint_wrappers=1:nokey=1 "$pdf_page" 2>/dev/null)
    page_height=$(ffprobe -v error -select_streams v:0 -show_entries stream=height -of default=noprint_wrappers=1:nokey=1 "$pdf_page" 2>/dev/null)
    test "$page_codec" = png
    test "$page_width" -gt 0 && test "$page_width" -le 4096
    test "$page_height" -gt 0 && test "$page_height" -le 4096
    pdf_text_job="$job/job-pdf-text"
    mkdir -m 0700 "$pdf_text_job" "$pdf_text_job/home" "$pdf_text_job/output"
    cp "$pdf_source" "$pdf_text_job/input.bin"
    chmod 0400 "$pdf_text_job/input.bin"
	    run_asset_tool pdftotext pdf_text_v1 600 2147483648 67108864 32 path "$pdf_text_job/input.bin" "$pdf_text_job/home" "$pdf_text_job/output" >/dev/null 2>&1
    pdf_text="$pdf_text_job/output/content.txt"
    test -f "$pdf_text" && test ! -L "$pdf_text" && test -s "$pdf_text"
    test "$(find "$pdf_text_job/output" -mindepth 1 -maxdepth 1 -type f | wc -l)" -eq 1
    test -z "$(find "$pdf_text_job/output" -mindepth 1 -maxdepth 1 ! -type f -print -quit)"
    test "$(wc -c <"$pdf_text")" -le 1048576
    valid_utf8_text_file "$pdf_text"
    grep -Fq -- "XIRANG PDF" "$pdf_text"
    odt_job="$job/job-office-odt"
    mkdir -m 0700 "$odt_job" "$odt_job/home" "$odt_job/output"
    printf "%s" "UEsDBAoAAAAAAAIL9lxexjIMJwAAACcAAAAIAAAAbWltZXR5cGVhcHBsaWNhdGlvbi92bmQub2FzaXMub3BlbmRvY3VtZW50LnRleHRQSwMECgAAAAAAAgv2XAAAAAAAAAAAAAAAAAkAAABNRVRBLUlORi9QSwMEFAAAAAgAAgv2XFmQkIWyAAAAaQEAABUAAABNRVRBLUlORi9tYW5pZmVzdC54bWyNkNEKwjAMRX9l5H2r4ouUdXvzC/QDSpdpoU3Lmon7e7uBcyKCbwnJvecmdfvwrrjjkGwgBftqBwWSCZ2lq4LL+VQeoW1qr8n2mFi+iiLLKK2tgnEgGXSySZL2mCQbGSJSF8zokVh+7ssFtHYb/gE2tN46LLN6mN67/ehcGTXfFIiNhcfO6pKniAp0jM4azdlS3KmrllzVNk7F+GAQ/6NMIJ51+Ywf0NlRzOPsKr7+1TwBUEsDBBQAAAAIAAIL9lznAALLogAAADsBAAALAAAAY29udGVudC54bWyNj08LwjAMxb9K6X1W8SKh6xBE8aIgE7zOrpOCS8bayfz27p9jOwieQpL3e3mRUZ0/2cuUzhKGfLVYcmZQU2rxEfJrvA82PFKSssxqAynpKjfoA03om8oaGB3025BXJQIlzjrAJDcOvAYqDH4pmKqhO9VPvKn9v3Sr7dnBZ5J9zcekd0rfY9MySnZkoW7Hy/Z0YOddLMUwkmImFDMP8eN39QFQSwECHgMKAAAAAAACC/ZcXsYyDCcAAAAnAAAACAAAAAAAAAAAAAAApIEAAAAAbWltZXR5cGVQSwECHgMKAAAAAAACC/ZcAAAAAAAAAAAAAAAACQAAAAAAAAAAABAA7UFNAAAATUVUQS1JTkYvUEsBAh4DFAAAAAgAAgv2XFmQkIWyAAAAaQEAABUAAAAAAAAAAQAAAKSBdAAAAE1FVEEtSU5GL21hbmlmZXN0LnhtbFBLAQIeAxQAAAAIAAIL9lznAALLogAAADsBAAALAAAAAAAAAAEAAACkgVkBAABjb250ZW50LnhtbFBLBQYAAAAABAAEAOkAAAAkAgAAAAA=" | base64 -d >"$odt_job/input.bin"
    chmod 0400 "$odt_job/input.bin"
    test -f "$odt_job/input.bin" && test ! -L "$odt_job/input.bin" && test -s "$odt_job/input.bin"
	    run_asset_tool libreoffice office_pdf_v1 600 2147483648 67108864 32 path "$odt_job/input.bin" "$odt_job/home" "$odt_job/output" >/dev/null 2>&1
    validate_office_output "$odt_job/output" "$odt_job/output/input.pdf" "$odt_job/home"
    ooxml_job="$job/job-office-ooxml"
    mkdir -m 0700 "$ooxml_job" "$ooxml_job/home" "$ooxml_job/output"
    printf "%s" "UEsDBBQAAAAIAAIL9lx5bjPX6AAAAK0BAAATAAAAW0NvbnRlbnRfVHlwZXNdLnhtbH1QyU7DMBD9FWuuKHHggBCK0wPLETiUDxjZk8SqN3nc0v49Tlt6QIXjzFv1+tXeO7GjzDYGBbdtB4KCjsaGScHn+rV5AMEFg0EXAyk4EMNq6NeHRCyqNrCCuZT0KCXrmTxyGxOFiowxeyz1zJNMqDc4kbzrunupYygUSlMWDxj6Zxpx64p42df3qUcmxyCeTsQlSwGm5KzGUnG5C+ZXSnNOaKvyyOHZJr6pBJBXExbk74Cz7r0Ok60h8YG5vKGvLPkVs5Em6q2vyvZ/mys94zhaTRf94pZy1MRcF/euvSAebfjpL49zD99QSwMECgAAAAAAAgv2XAAAAAAAAAAAAAAAAAYAAABfcmVscy9QSwMEFAAAAAgAAgv2XJv9N+qtAAAAKQEAAAsAAABfcmVscy8ucmVsc43POw7CMAwG4KtE3mlaBoRQ0y4IqSsqB7ASN61oHkrCo7cnAwNFDIy2f3+W6/ZpZnanECdnBVRFCYysdGqyWsClP232wGJCq3B2lgQsFKFt6jPNmPJKHCcfWTZsFDCm5A+cRzmSwVg4TzZPBhcMplwGzT3KK2ri27Lc8fBpwNpknRIQOlUB6xdP/9huGCZJRydvhmz6ceIrkWUMmpKAhwuKq3e7yCzwpuarF5sXUEsDBAoAAAAAAAIL9lwAAAAAAAAAAAAAAAAFAAAAd29yZC9QSwMEFAAAAAgAAgv2XIrB8lTiAAAATwEAABEAAAB3b3JkL2RvY3VtZW50LnhtbEVQXWvDMAz8K8bvq9PQjRKSlL5sDNZ17AP66tpqEogtY2vzul8/OyXk5aw7HSfJ9e7XjOwHfBjQNny9KjgDq1APtmv41+fj3ZazQNJqOaKFhl8h8F1bx0qj+jZgiaUAG6rY8J7IVUIE1YORYYUObOpd0BtJifpORPTaeVQQQso3oyiL4kEYOVieI8+or/l1GXwGak/P7/vXJ3Y8ng4vtchKRj/h5Aug6G0yu+7jj8W8ybosN+mQWPWpvt+mWtwMB+mTSuiSvrlZ/ND1tNAzEqFZ+AiXuSumofM8Me8rlr9o/wFQSwECHgMUAAAACAACC/ZceW4z1+gAAACtAQAAEwAAAAAAAAABAAAApIEAAAAAW0NvbnRlbnRfVHlwZXNdLnhtbFBLAQIeAwoAAAAAAAIL9lwAAAAAAAAAAAAAAAAGAAAAAAAAAAAAEADtQRkBAABfcmVscy9QSwECHgMUAAAACAACC/Zcm/036q0AAAApAQAACwAAAAAAAAABAAAApIE9AQAAX3JlbHMvLnJlbHNQSwECHgMKAAAAAAACC/ZcAAAAAAAAAAAAAAAABQAAAAAAAAAAABAA7UETAgAAd29yZC9QSwECHgMUAAAACAACC/ZcisHyVOIAAABPAQAAEQAAAAAAAAABAAAApIE2AgAAd29yZC9kb2N1bWVudC54bWxQSwUGAAAAAAUABQAgAQAARwMAAAAA" | base64 -d >"$ooxml_job/input.bin"
    chmod 0400 "$ooxml_job/input.bin"
    test -f "$ooxml_job/input.bin" && test ! -L "$ooxml_job/input.bin" && test -s "$ooxml_job/input.bin"
	    run_asset_tool libreoffice office_pdf_v1 600 2147483648 67108864 32 path "$ooxml_job/input.bin" "$ooxml_job/home" "$ooxml_job/output" >/dev/null 2>&1
	    validate_office_output "$ooxml_job/output" "$ooxml_job/output/input.pdf" "$ooxml_job/home"
	    clamav_database=/var/lib/xirang/asset-worker-bundles/active/clamav
    printf "%s\n" "XirangSmoke.Marker:0:*:584952414e475f434c414d5f534d4f4b455f4d41524b45525f3230323630373232" \
      >"$clamav_database/xirang-smoke.ndb"
    chmod 0400 "$clamav_database/xirang-smoke.ndb"
    test -f "$clamav_database/xirang-smoke.ndb" && test ! -L "$clamav_database/xirang-smoke.ndb"
	    clamav_clean_job="$job/job-clamav-clean"
	    mkdir -m 0700 "$clamav_clean_job" "$clamav_clean_job/home" "$clamav_clean_job/output"
	    printf "%s\n" "XIRANG_CLAM_CLEAN_FIXTURE_20260722" >"$clamav_clean_job/input.bin"
	    chmod 0400 "$clamav_clean_job/input.bin"
	    if ! (
	      cd "$clamav_clean_job"
	      run_asset_tool clamscan clam_scan_v1 600 2147483648 1073741824 16 path "$clamav_clean_job/input.bin" "$clamav_clean_job/home" "$clamav_clean_job/output"
	    ) >"$clamav_clean_job/scan.stdout" 2>"$clamav_clean_job/scan.stderr"; then
	      fail "sandboxed ClamAV clean fixture failed"
	    fi
	    chmod 0400 "$clamav_clean_job/scan.stdout" "$clamav_clean_job/scan.stderr"
	    test ! -s "$clamav_clean_job/scan.stdout"
	    test ! -s "$clamav_clean_job/scan.stderr"
	    test -z "$(find "$clamav_clean_job/output" -mindepth 1 -print -quit)"
	    clamav_clean_classification=no_finding
	    test "$clamav_clean_classification" = no_finding
	    clamav_finding_job="$job/job-clamav-finding"
	    mkdir -m 0700 "$clamav_finding_job" "$clamav_finding_job/home" "$clamav_finding_job/output"
	    printf "%s\n" "XIRANG_CLAM_SMOKE_MARKER_20260722" >"$clamav_finding_job/input.bin"
	    chmod 0400 "$clamav_finding_job/input.bin"
	    clamav_finding_status=0
	    if (
	      cd "$clamav_finding_job"
	      run_asset_tool clamscan clam_scan_v1 600 2147483648 1073741824 16 path "$clamav_finding_job/input.bin" "$clamav_finding_job/home" "$clamav_finding_job/output"
	    ) >"$clamav_finding_job/scan.stdout" 2>"$clamav_finding_job/scan.stderr"; then
	      clamav_finding_status=0
	    else
	      clamav_finding_status=$?
	    fi
	    test "$clamav_finding_status" -eq 1 ||
	      fail "sandboxed ClamAV finding fixture did not return the closed finding exit code"
	    chmod 0400 "$clamav_finding_job/scan.stdout" "$clamav_finding_job/scan.stderr"
	    test ! -s "$clamav_finding_job/scan.stderr"
	    test "$(wc -l <"$clamav_finding_job/scan.stdout")" -eq 1
	    grep -Fxq -- "$clamav_finding_job/input.bin: XirangSmoke.Marker.UNOFFICIAL FOUND" "$clamav_finding_job/scan.stdout"
	    test -z "$(find "$clamav_finding_job/output" -mindepth 1 -print -quit)"
	    clamav_finding_classification=finding
	    test "$clamav_finding_classification" = finding
	    clamav_limit_job="$job/job-clamav-limit"
	    mkdir -m 0700 "$clamav_limit_job" "$clamav_limit_job/home" "$clamav_limit_job/output"
	    printf "%s\n" "nested limit payload" >"$clamav_limit_job/input.bin"
	    clamav_limit_depth=1
	    while test "$clamav_limit_depth" -le 20; do
	      mkdir -m 0700 "$clamav_limit_job/layer"
	      mv "$clamav_limit_job/input.bin" "$clamav_limit_job/layer/payload.bin"
	      tar -C "$clamav_limit_job/layer" -cf "$clamav_limit_job/layer.tar" payload.bin
	      gzip -n -c "$clamav_limit_job/layer.tar" >"$clamav_limit_job/input.bin"
	      rm -rf "$clamav_limit_job/layer" "$clamav_limit_job/layer.tar"
	      clamav_limit_depth=$((clamav_limit_depth + 1))
	    done
	    chmod 0400 "$clamav_limit_job/input.bin"
	    clamav_limit_status=0
	    if (
	      cd "$clamav_limit_job"
	      run_asset_tool clamscan clam_scan_v1 600 2147483648 1073741824 16 path "$clamav_limit_job/input.bin" "$clamav_limit_job/home" "$clamav_limit_job/output"
	    ) >"$clamav_limit_job/scan.stdout" 2>"$clamav_limit_job/scan.stderr"; then
	      clamav_limit_status=0
	    else
	      clamav_limit_status=$?
	    fi
	    test "$clamav_limit_status" -eq 1 ||
	      fail "sandboxed ClamAV limit fixture did not return the closed limit exit code"
	    chmod 0400 "$clamav_limit_job/scan.stdout" "$clamav_limit_job/scan.stderr"
	    test ! -s "$clamav_limit_job/scan.stderr"
	    test "$(wc -l <"$clamav_limit_job/scan.stdout")" -eq 1
	    clamav_limit_line=$(cat "$clamav_limit_job/scan.stdout")
	    case "$clamav_limit_line" in
	      "$clamav_limit_job/input.bin: Heuristics.Limits.Exceeded."*" FOUND") ;;
	      *) fail "sandboxed ClamAV limit fixture returned an unsafe result" ;;
	    esac
	    test -z "$(find "$clamav_limit_job/output" -mindepth 1 -print -quit)"
	    media_preview_job="$job/job-media-preview"
    mkdir -m 0700 "$media_preview_job" "$media_preview_job/home" "$media_preview_job/output"
    cp "$job/input.bin" "$media_preview_job/input.bin"
    chmod 0400 "$media_preview_job/input.bin"
	    run_asset_tool ffmpeg media_preview_v1 1800 4294967296 536870912 32 path "$media_preview_job/input.bin" "$media_preview_job/home" "$media_preview_job/output" >/dev/null 2>&1
    media_preview="$media_preview_job/output/preview.mp4"
    test -f "$media_preview" && test ! -L "$media_preview" && test -s "$media_preview"
    test "$(find "$media_preview_job/output" -mindepth 1 -maxdepth 1 -type f | wc -l)" -eq 1
    test -z "$(find "$media_preview_job/output" -mindepth 1 -maxdepth 1 ! -type f -print -quit)"
    test "$(wc -c <"$media_preview")" -le 1048576
    media_preview_format=$(ffprobe -v error -show_entries format=format_name \
      -of default=noprint_wrappers=1:nokey=1 "$media_preview" 2>/dev/null)
    media_preview_codec=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name \
      -of default=noprint_wrappers=1:nokey=1 "$media_preview" 2>/dev/null)
    media_preview_pixel_format=$(ffprobe -v error -select_streams v:0 -show_entries stream=pix_fmt \
      -of default=noprint_wrappers=1:nokey=1 "$media_preview" 2>/dev/null)
    media_preview_frames=$(ffprobe -v error -count_frames -select_streams v:0 -show_entries stream=nb_read_frames \
      -of default=noprint_wrappers=1:nokey=1 "$media_preview" 2>/dev/null)
    media_preview_duration=$(ffprobe -v error -show_entries format=duration \
      -of default=noprint_wrappers=1:nokey=1 "$media_preview" 2>/dev/null)
    case ",$media_preview_format," in
      *,mp4,*) ;;
      *) fail "sandboxed media preview container was not closed" ;;
    esac
    test "$media_preview_codec" = h264
    test "$media_preview_pixel_format" = yuv420p
    case "$media_preview_frames" in
      ""|*[!0-9]*) fail "sandboxed media preview frame count was not closed" ;;
    esac
    test "$media_preview_frames" -gt 0 && test "$media_preview_frames" -le 300
    awk -v duration="$media_preview_duration" \
      "BEGIN { exit duration ~ /^[0-9]+([.][0-9]+)?$/ && duration > 0 && duration <= 5 ? 0 : 1 }"
    printf "%s\n" "closed archive member" >"$job/member.txt"
    tar -C "$job" -cf "$job/archive.tar" member.txt
    gzip -n -c "$job/archive.tar" >"$job/archive.tar.gz"
	    run_asset_tool gzip gzip_decompress_v1 600 1073741824 16777216 4 pipe - "$job/home" "$job/output" <"$job/archive.tar.gz" >"$job/gzip.tar"
    cmp "$job/archive.tar" "$job/gzip.tar"
    xz -c "$job/archive.tar" >"$job/archive.tar.xz"
	    run_asset_tool xz xz_decompress_v1 600 1073741824 16777216 4 pipe - "$job/home" "$job/output" <"$job/archive.tar.xz" >"$job/xz.tar"
    cmp "$job/archive.tar" "$job/xz.tar"
	    zstd -q -c "$job/archive.tar" >"$job/archive.tar.zst"
	    run_asset_tool zstd zstd_decompress_v1 600 1073741824 16777216 4 pipe - "$job/home" "$job/output" <"$job/archive.tar.zst" >"$job/zstd.tar"
	    # ASSET_TOOL_SMOKE_INVOCATIONS_END
	    cmp "$job/archive.tar" "$job/zstd.tar"
	  cat "$media_probe_stdout"
	  ')

rm -f -- "$IMAGE_RUNTIME_CLOSURE"
"$DOCKER" cp "$container_id:/usr/local/share/xirang/runtime-closure.v1.json" "$IMAGE_RUNTIME_CLOSURE"
validate_runtime_closure "$IMAGE_RUNTIME_CLOSURE" "linux/$EXPECTED_ARCH"
cmp -s "$IMAGE_RUNTIME_CLOSURE" "$expected_runtime_closure" ||
  fail "native Worker image runtime closure differs from the downloaded artifact"

"$DOCKER" inspect "$container_id" >"$CONTAINER_JSON"
jq -e '
  .[0].HostConfig.ReadonlyRootfs == true and
  .[0].HostConfig.NetworkMode == "none" and
  .[0].HostConfig.CapDrop == ["ALL"] and
  (.[0].HostConfig.SecurityOpt | index("no-new-privileges=true")) != null and
  (.[0].HostConfig.SecurityOpt | map(startswith("seccomp=")) | any) and
	  .[0].HostConfig.PidsLimit == 256 and
	  .[0].HostConfig.Memory == 1073741824 and
	  .[0].HostConfig.MemorySwap == 1073741824 and
	  .[0].HostConfig.NanoCpus == 2000000000 and
  .[0].HostConfig.GroupAdd == null and
  (.[0].HostConfig.Tmpfs | keys | sort) == ["/run/xirang/asset-jobs", "/tmp", "/var/lib/xirang/asset-worker-bundles/active/clamav"] and
	  .[0].HostConfig.Tmpfs["/tmp"] == "rw,noexec,nosuid,nodev,size=67108864,nr_inodes=8192,mode=0700,uid=10000,gid=10000" and
  .[0].HostConfig.Tmpfs["/var/lib/xirang/asset-worker-bundles/active/clamav"] == "rw,noexec,nosuid,nodev,size=1048576,nr_inodes=64,mode=0700,uid=10000,gid=10000" and
  (.[0].HostConfig.Tmpfs["/run/xirang/asset-jobs"] | contains("noexec")) and
  (.[0].HostConfig.Tmpfs["/run/xirang/asset-jobs"] | contains("nosuid")) and
  (.[0].HostConfig.Tmpfs["/run/xirang/asset-jobs"] | contains("nodev"))
' "$CONTAINER_JSON" >/dev/null || fail "Worker runtime isolation flags are unsafe"

MEDIA_PROBE_JSON=$("$DOCKER" start -a "$container_id")
jq -e "
  keys == [\"format\", \"programs\", \"stream_groups\", \"streams\"] and
  (.programs | type == \"array\" and length == 0) and
  (.stream_groups | type == \"array\" and length == 0) and
  (.streams | type == \"array\" and length == 1) and
  (.streams[0] | keys == [\"codec_name\", \"codec_type\", \"duration\", \"height\", \"index\", \"width\"]) and
  .streams[0].index == 0 and .streams[0].codec_type == \"video\" and
  .streams[0].codec_name == \"h264\" and .streams[0].width == 16 and .streams[0].height == 16 and
  (.streams[0].duration | type == \"string\" and test(\"^[0-9]+[.][0-9]+$\")) and
  (.format | keys == [\"duration\"]) and
  (.format.duration | type == \"string\" and test(\"^[0-9]+[.][0-9]+$\"))
" <<<"$MEDIA_PROBE_JSON" >/dev/null || fail "media probe runtime output is not canonical"

run_profile_smoke() {
  [[ "$PROFILE_PROJECT" =~ ^[a-z0-9][a-z0-9_-]{0,62}$ ]] || fail "unsafe profile Compose project name"
  [[ "$IMAGE" =~ ^xirang-asset-worker:[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] ||
    fail "profile smoke requires ASSET_WORKER_IMAGE=xirang-asset-worker:<tag>"
  [[ "$PROFILE_CORE_TAG" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] ||
    fail "ASSET_WORKER_CORE_IMAGE_TAG must name an already built local Core image"
  [[ "$PROFILE_NETWORK_MODE" == "bridge" || "$PROFILE_NETWORK_MODE" == "host" ]] ||
    fail "ASSET_WORKER_PROFILE_NETWORK_MODE must be bridge or host"
  command -v go >/dev/null 2>&1 || fail "Go is required for the signed profile fixture"
  command -v curl >/dev/null 2>&1 || fail "curl is required for the complete profile smoke"
  validate_runtime_closure "$AMD64_RUNTIME_CLOSURE" linux/amd64
  validate_runtime_closure "$ARM64_RUNTIME_CLOSURE" linux/arm64

  local fixed_name
  for fixed_name in xirang xirang-asset-worker-init xirang-asset-worker xirang-asset-worker-updater; do
    if [[ -n "$("$DOCKER" ps -a --filter "name=^/${fixed_name}$" --format '{{.ID}}')" ]]; then
      fail "pre-existing container named ${fixed_name} is present"
    fi
  done
  if [[ -n "$("$DOCKER" ps -a --filter "label=com.docker.compose.project=$PROFILE_PROJECT" --format '{{.ID}}')" ]]; then
    fail "profile Compose project already owns containers"
  fi

  local resource
  for resource in \
    "${PROFILE_PROJECT}_asset-worker-worker-runtime" \
    "${PROFILE_PROJECT}_asset-worker-updater-runtime" \
    "${PROFILE_PROJECT}_asset-worker-bundles" \
    "${PROFILE_PROJECT}_asset-worker-derived-store" \
    "${PROFILE_PROJECT}_asset-worker-export-store"; do
    if "$DOCKER" volume inspect "$resource" >/dev/null 2>&1; then
      fail "profile Compose project volume already exists"
    fi
  done
  if "$DOCKER" network inspect "${PROFILE_PROJECT}_default" >/dev/null 2>&1; then
    fail "profile Compose project network already exists"
  fi

  PROFILE_DIR=$(mktemp -d)
  mkdir -p "$PROFILE_DIR/deploy/worker" "$PROFILE_DIR/data" "$PROFILE_DIR/backups" "$PROFILE_DIR/logs"
  cp "$ROOT_DIR/docker-compose.yml" "$PROFILE_DIR/docker-compose.yml"
  cp "$SECCOMP" "$PROFILE_DIR/deploy/worker/seccomp.json"
  PROFILE_COMPOSE_FILES=(-f "$PROFILE_DIR/docker-compose.yml")
  if [[ "$PROFILE_NETWORK_MODE" == "host" ]]; then
    cat >"$PROFILE_DIR/docker-compose.host.yml" <<'YAML'
services:
  xirang:
    network_mode: host
    ports: !reset []
YAML
    PROFILE_COMPOSE_FILES+=(-f "$PROFILE_DIR/docker-compose.host.yml")
  fi

  command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required for the signed profile fixture"
  local expected_fingerprint expected_pipeline_fingerprint
  local amd64_runtime_closure_sha256 arm64_runtime_closure_sha256
  amd64_runtime_closure_sha256=$(sha256sum "$AMD64_RUNTIME_CLOSURE" | awk '{print $1}')
  arm64_runtime_closure_sha256=$(sha256sum "$ARM64_RUNTIME_CLOSURE" | awk '{print $1}')
  generate_profile_fixture "$PROFILE_DIR" "$amd64_runtime_closure_sha256" "$arm64_runtime_closure_sha256"
  expected_fingerprint=$(jq -er '.bundle_fingerprint' <<<"$PROFILE_FIXTURE_JSON")
  expected_pipeline_fingerprint=$(jq -er '.pipeline_fingerprint' <<<"$PROFILE_FIXTURE_JSON")
  mkdir "$PROFILE_DIR/asset-worker-inbox"
  chmod 0440 "$PROFILE_DIR/asset-worker-updater-trust.json"
  chmod 0555 "$PROFILE_DIR/asset-worker-inbox"
  "$DOCKER" run --rm --network none --user 0:0 --entrypoint /bin/sh \
    -v "$PROFILE_DIR:/profile" "$IMAGE" -eu -c '
      chown 10002:10002 /profile/asset-worker-updater-trust.json /profile/asset-worker-inbox
      chmod 0440 /profile/asset-worker-updater-trust.json
      chmod 0555 /profile/asset-worker-inbox
    '

  cat >"$PROFILE_DIR/.env" <<'ENV'
APP_ENV=production
JWT_SECRET=profile-smoke-jwt-secret-at-least-32-bytes
DATA_ENCRYPTION_KEY=profile-smoke-encryption-key
METRICS_TOKEN=profile-smoke-metrics-token
ADMIN_INITIAL_PASSWORD=ProfileSmokeAdmin1!
BACKUP_ASSETS_ENABLED=true
BACKUP_ASSETS_WORKER_LOCAL_ENABLED=true
BACKUP_ASSETS_WORKER_REMOTE_ENABLED=false
BACKUP_ASSETS_WORKER_UPDATER_ENABLED=true
BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ENABLED=false
BACKUP_ASSETS_PROCESSING_SECRET_CLASSIFY=false
BACKUP_ASSETS_DERIVED_STORE_ROOT=/var/lib/xirang-asset-runtime/derived
BACKUP_ASSETS_EXPORT_ROOT=/var/lib/xirang-asset-runtime/export
ENV

  local bundle_volume
  bundle_volume="${PROFILE_PROJECT}_asset-worker-bundles"
  "$DOCKER" volume create \
    --label "com.docker.compose.project=$PROFILE_PROJECT" \
    --label com.docker.compose.volume=asset-worker-bundles \
    "$bundle_volume" >/dev/null
  PROFILE_OWNED=1
  "$DOCKER" run --rm --network none --user 0:0 --entrypoint /bin/sh \
    -v "$bundle_volume:/bundle" "$IMAGE" -eu -c '
      mkdir -p /bundle/bundles
      chown 10002:10000 /bundle /bundle/bundles
      chmod 2750 /bundle /bundle/bundles
      test ! -e /bundle/active
    '

  if ! profile_compose up -d --no-build; then
    profile_compose ps -a >&2 || true
    profile_compose logs --no-color >&2 || true
    fail "complete profile failed during Compose startup"
  fi

  local init_id core_id worker_id updater_id ready=0
  init_id=$(profile_compose ps -a -q asset-worker-init)
  core_id=$(profile_compose ps -q xirang)
  worker_id=$(profile_compose ps -q asset-worker)
  updater_id=$(profile_compose ps -q asset-worker-updater)
  [[ -n "$init_id" && -n "$core_id" && -n "$worker_id" && -n "$updater_id" ]] ||
    fail "complete profile did not create every service"

  for _ in $(seq 1 60); do
    if [[ "$("$DOCKER" inspect --format '{{.State.Status}}:{{.State.ExitCode}}' "$init_id" 2>/dev/null || true)" == "exited:0" ]] &&
      [[ "$("$DOCKER" inspect --format '{{.State.Health.Status}}' "$core_id" 2>/dev/null || true)" == "healthy" ]] &&
      [[ "$("$DOCKER" inspect --format '{{.State.Status}}' "$worker_id" 2>/dev/null || true)" == "running" ]] &&
      [[ "$("$DOCKER" inspect --format '{{.State.Status}}' "$updater_id" 2>/dev/null || true)" == "running" ]]; then
      ready=1
      break
    fi
    sleep 1
  done
  [[ "$ready" == "1" ]] || fail "complete profile services did not become ready"

  local service_id
  for service_id in "$init_id" "$worker_id" "$updater_id"; do
    [[ "$("$DOCKER" inspect --format '{{.Path}}' "$service_id")" == "/usr/local/bin/asset-worker-entrypoint" ]] ||
      fail "profile service bypassed /usr/local/bin/asset-worker-entrypoint"
  done
  [[ "$("$DOCKER" inspect --format '{{.RestartCount}}' "$worker_id")" == "0" ]] || fail "parser restarted during profile smoke"
  [[ "$("$DOCKER" inspect --format '{{.RestartCount}}' "$updater_id")" == "0" ]] || fail "updater restarted during profile smoke"

  [[ "$("$DOCKER" exec "$core_id" stat -c '%a:%u:%g' /run/xirang/worker/asset-worker.sock)" == "600:10000:10000" ]] ||
    fail "parser socket ownership or mode is unsafe"
  [[ "$("$DOCKER" exec "$core_id" stat -c '%a:%u:%g' /run/xirang/asset-worker-updater.sock)" == "660:10000:10002" ]] ||
    fail "updater socket ownership or mode is unsafe"
  [[ "$("$DOCKER" exec "$core_id" stat -c '%a:%u:%g' /var/lib/xirang-asset-runtime/derived)" == "700:10000:10000" ]] ||
    fail "Derived Store ownership or mode is unsafe"
  [[ "$("$DOCKER" exec "$core_id" stat -c '%a:%u:%g' /var/lib/xirang-asset-runtime/export)" == "700:10000:10000" ]] ||
    fail "Export Store ownership or mode is unsafe"
  if "$DOCKER" logs "$core_id" 2>&1 | grep -Fq -- '备份资产处理运行时不可用'; then
    fail "processing runtime was unavailable during fresh profile startup"
  fi
  "$DOCKER" exec --user 10000:10000 "$core_id" /bin/sh -eu -c \
    'printf "%s\n" profile-derived-persistence > /var/lib/xirang-asset-runtime/derived/.profile-smoke-persistence'
  "$DOCKER" exec --user 10000:10000 "$core_id" /bin/sh -eu -c \
    'printf "%s\n" profile-export-persistence > /var/lib/xirang-asset-runtime/export/.profile-smoke-persistence'

  "$DOCKER" exec "$worker_id" /bin/sh -eu -c '
    test "$(id -u):$(id -g)" = "10000:10000"
    worker_groups=" $(id -G) "
    case "$worker_groups" in
      *" 10000 "*) ;;
      *) exit 1 ;;
    esac
    case "$worker_groups" in
      *" 10002 "*) exit 1 ;;
    esac
    test -S /run/xirang/worker/asset-worker.sock
    test ! -e /run/xirang/asset-worker-updater.sock
    test ! -e /var/lib/xirang/asset-worker-bundles/active
    ! touch /run/xirang/worker/.profile-smoke
    ! touch /var/lib/xirang/asset-worker-bundles/.profile-smoke
    test ! -e /var/lib/xirang/asset-worker-inbox/profile-smoke-candidate
    test ! -e /run/secrets/asset-worker-updater-trust.json
    test ! -e /var/lib/xirang-asset-runtime/derived
    test ! -e /var/lib/xirang-asset-runtime/export
    ! awk '\''$2 == "00000000" { found=1 } END { exit found ? 0 : 1 }'\'' /proc/net/route
  ' || fail "parser mounted the updater socket or gained a forbidden writable/network path"
  "$DOCKER" exec "$updater_id" /bin/sh -eu -c '
    test "$(id -u):$(id -g)" = "10002:10002"
    test -S /run/xirang/asset-worker-updater.sock
    test ! -e /run/xirang/worker/asset-worker.sock
    ! touch /run/xirang/.profile-smoke
    touch /var/lib/xirang/asset-worker-bundles/.profile-smoke
    rm /var/lib/xirang/asset-worker-bundles/.profile-smoke
    ! touch /var/lib/xirang/asset-worker-inbox/.profile-smoke
    ! touch /run/secrets/asset-worker-updater-trust.json
    test ! -e /var/lib/xirang-asset-runtime/derived
    test ! -e /var/lib/xirang-asset-runtime/export
    ! awk '\''$2 == "00000000" { found=1 } END { exit found ? 0 : 1 }'\'' /proc/net/route
  ' || fail "updater violated its identity, mount, or network boundary"
  "$DOCKER" exec "$core_id" test ! -e /var/lib/xirang/asset-worker-inbox ||
    fail "Core gained visibility into the updater-only inbox"

  local worker_identity_count=0 worker_capability_count
  for _ in $(seq 1 30); do
    worker_identity_count=$("$DOCKER" exec "$core_id" sqlite3 -readonly /data/xirang.db \
      "SELECT count(*) FROM backup_asset_worker_identities WHERE trust_state='active' AND health_state='ready';")
    worker_capability_count=$("$DOCKER" exec "$core_id" sqlite3 -readonly /data/xirang.db \
      'SELECT count(*) FROM backup_asset_worker_capabilities;')
    if [[ "$worker_identity_count" == "1" && "$worker_capability_count" == "0" ]]; then
      break
    fi
    sleep 1
  done
  [[ "$worker_identity_count" == "1" && "$worker_capability_count" == "0" ]] ||
    fail "fresh Worker did not authenticate with zero capabilities"

  profile_api_request 200 POST /api/v1/auth/login \
    '{"username":"admin","password":"ProfileSmokeAdmin1!"}'
  PROFILE_API_TOKEN=$(jq -er '.data.token | select(type == "string" and length > 0)' <<<"$PROFILE_API_BODY") ||
    fail "profile admin login did not return a token"
  profile_api_request 200 GET /api/v1/admin/backup-asset-processing/updater
  jq -e '
    .data.schema_version == 1 and .data.enabled == true and .data.online_enabled == false and
    (.data | has("active") | not)
  ' <<<"$PROFILE_API_BODY" >/dev/null || fail "fresh Core unexpectedly reported an active updater bundle"
	profile_api_request 200 GET /api/v1/admin/backup-asset-processing/capabilities
	profile_assert_core_capabilities
	jq -e '
	  .data.schema_version == 1 and
	  all(.data.items[]; (.deployed | not) and .ready_workers == 0)
  ' <<<"$PROFILE_API_BODY" >/dev/null || fail "fresh Worker advertised a processing capability"

  "$DOCKER" inspect "$worker_id" "$updater_id" | jq -e \
    --arg worker_runtime "${PROFILE_PROJECT}_asset-worker-worker-runtime" \
    --arg updater_runtime "${PROFILE_PROJECT}_asset-worker-updater-runtime" \
    --arg bundle_volume "$bundle_volume" \
    --arg derived_volume "${PROFILE_PROJECT}_asset-worker-derived-store" \
    --arg export_volume "${PROFILE_PROJECT}_asset-worker-export-store" '
    length == 2 and
    all(.[]; .HostConfig.NetworkMode == "none" and .HostConfig.ReadonlyRootfs == true) and
    all(.[]; .HostConfig.CapDrop == ["ALL"] and
      (.HostConfig.SecurityOpt | map(startswith("no-new-privileges")) | any) and
      (.HostConfig.SecurityOpt | map(startswith("seccomp=")) | any)) and
    .[0].Config.User == "10000:10000" and
    .[0].HostConfig.GroupAdd == null and
    .[1].Config.User == "10002:10002" and
    ([.[0].Mounts[] | select(.Name == $worker_runtime and .Destination == "/run/xirang/worker" and .RW == false)] | length) == 1 and
    ([.[0].Mounts[] | select(.Name == $bundle_volume and .Destination == "/var/lib/xirang/asset-worker-bundles" and .RW == false)] | length) == 1 and
    ([.[0].Mounts[] | select(.Name == $updater_runtime or .Name == $derived_volume or .Name == $export_volume or .Destination == "/run/xirang" or .Destination == "/run/secrets/asset-worker-updater-trust.json" or .Destination == "/var/lib/xirang/asset-worker-inbox" or .Destination == "/var/lib/xirang-asset-runtime/derived" or .Destination == "/var/lib/xirang-asset-runtime/export")] | length) == 0 and
    ([.[1].Mounts[] | select(.Name == $updater_runtime and .Destination == "/run/xirang" and .RW == false)] | length) == 1 and
    ([.[1].Mounts[] | select(.Name == $bundle_volume and .Destination == "/var/lib/xirang/asset-worker-bundles" and .RW == true)] | length) == 1 and
    ([.[1].Mounts[] | select(.Destination == "/var/lib/xirang/asset-worker-inbox" and .RW == false)] | length) == 1 and
    ([.[1].Mounts[] | select(.Destination == "/run/secrets/asset-worker-updater-trust.json" and .RW == false)] | length) == 1 and
    ([.[1].Mounts[] | select(.Name == $worker_runtime or .Name == $derived_volume or .Name == $export_volume or .Destination == "/run/xirang/worker" or .Destination == "/var/lib/xirang-asset-runtime/derived" or .Destination == "/var/lib/xirang-asset-runtime/export")] | length) == 0 and
    all(.[].Mounts[]; .Destination != "/data" and .Destination != "/backup" and
      .Destination != "/logs" and .Destination != "/var/run/docker.sock")
  ' >/dev/null || fail "profile container isolation metadata is unsafe"

  "$DOCKER" inspect "$core_id" "$init_id" | jq -e \
    --arg derived_volume "${PROFILE_PROJECT}_asset-worker-derived-store" \
    --arg export_volume "${PROFILE_PROJECT}_asset-worker-export-store" '
    length == 2 and
    all(.[]; ([.Mounts[] | select(.Name == $derived_volume and .Destination == "/var/lib/xirang-asset-runtime/derived" and .RW == true)] | length) == 1) and
    all(.[]; ([.Mounts[] | select(.Name == $export_volume and .Destination == "/var/lib/xirang-asset-runtime/export" and .RW == true)] | length) == 1)
  ' >/dev/null || fail "Derived or Export Store volume is not confined to Core and the initializer"

  "$DOCKER" run --rm \
    --network none \
    --user 0:0 \
    --read-only \
    --cap-drop ALL \
    --cap-add CHOWN \
    --cap-add DAC_OVERRIDE \
    --cap-add FOWNER \
    --security-opt no-new-privileges=true \
    --entrypoint /bin/sh \
    -v "$PROFILE_DIR:/profile" \
    "$IMAGE" -eu -c '
      test -d /profile/profile-smoke-candidate
      test ! -e /profile/asset-worker-inbox/profile-smoke-candidate
      chmod 0755 /profile/asset-worker-inbox
      cp -R /profile/profile-smoke-candidate /profile/asset-worker-inbox/profile-smoke-candidate
      chown -R 10002:10002 /profile/asset-worker-inbox/profile-smoke-candidate
      find /profile/asset-worker-inbox/profile-smoke-candidate -type f -exec chmod 0444 {} +
      find /profile/asset-worker-inbox/profile-smoke-candidate -type d -exec chmod 0555 {} +
      chmod 0555 /profile/asset-worker-inbox
    '
  "$DOCKER" exec "$updater_id" /bin/sh -eu -c '
    test "$(stat -c "%a:%u:%g" /var/lib/xirang/asset-worker-inbox)" = "555:10002:10002"
    test "$(stat -c "%a:%u:%g" /var/lib/xirang/asset-worker-inbox/profile-smoke-candidate)" = "555:10002:10002"
    test "$(stat -c "%a:%u:%g" /var/lib/xirang/asset-worker-inbox/profile-smoke-candidate/bundle.tar)" = "444:10002:10002"
    test "$(stat -c "%a:%u:%g" /var/lib/xirang/asset-worker-inbox/profile-smoke-candidate/manifest.json)" = "444:10002:10002"
    ! touch /var/lib/xirang/asset-worker-inbox/profile-smoke-candidate/.profile-smoke
  ' || fail "signed candidate did not enter through the fixed read-only updater inbox"
  "$DOCKER" exec "$core_id" test ! -e /var/lib/xirang/asset-worker-inbox ||
    fail "Core gained visibility into the signed candidate"
  "$DOCKER" exec "$worker_id" test ! -e /var/lib/xirang/asset-worker-inbox/profile-smoke-candidate ||
    fail "parser gained visibility into the signed candidate"

  profile_api_request 202 POST \
    /api/v1/admin/backup-asset-processing/updater/offline-candidates/scan
  local candidate_id=""
  for _ in $(seq 1 30); do
    profile_api_request 200 GET \
      /api/v1/admin/backup-asset-processing/updater/offline-candidates
    candidate_id=$(jq -r --arg fingerprint "$expected_fingerprint" '
      first(.data.items[] | select(
        .bundle_fingerprint == $fingerprint and .state == "verified"
      ) | .candidate_id) // ""
    ' <<<"$PROFILE_API_BODY")
    if [[ "$candidate_id" =~ ^[0-9a-f]{32}$ ]]; then
      break
    fi
    sleep 2
  done
  [[ "$candidate_id" =~ ^[0-9a-f]{32}$ ]] || fail "signed offline candidate was not registered"
  [[ "$("$DOCKER" inspect --format '{{.RestartCount}}' "$updater_id")" == "0" ]] ||
    fail "updater restarted while scanning the signed candidate"
  [[ "$("$DOCKER" inspect --format '{{.RestartCount}}' "$worker_id")" == "0" ]] ||
    fail "parser restarted before signed activation"

  local activation_payload
  activation_payload=$(printf \
    '{"schema_version":1,"candidate_id":"%s","expected_active_fingerprint":null}' \
    "$candidate_id")
  profile_api_request 202 POST \
    /api/v1/admin/backup-asset-processing/updater/offline-imports \
    "$activation_payload"

  local api_active_fingerprint="" db_active_fingerprint="" active_target=""
  ready=0
  for _ in $(seq 1 60); do
    profile_api_request 200 GET /api/v1/admin/backup-asset-processing/updater
    api_active_fingerprint=$(jq -r '.data.active.bundle_fingerprint // ""' <<<"$PROFILE_API_BODY")
    db_active_fingerprint=$("$DOCKER" exec "$core_id" sqlite3 -readonly -cmd '.timeout 5000' /data/xirang.db \
      "SELECT bundle_fingerprint FROM backup_asset_updater_metadata WHERE state='active' ORDER BY updated_at DESC LIMIT 1;")
    active_target=$("$DOCKER" exec "$updater_id" \
      readlink /var/lib/xirang/asset-worker-bundles/active 2>/dev/null || true)
    if [[ "$api_active_fingerprint" == "$expected_fingerprint" &&
      "$db_active_fingerprint" == "$expected_fingerprint" &&
      "$active_target" == "bundles/$expected_fingerprint" ]]; then
      ready=1
      break
    fi
    sleep 1
  done
  [[ "$ready" == "1" ]] || fail "signed activation fingerprint did not converge"
  jq -e --arg fingerprint "$expected_fingerprint" '
    .data.active.bundle_fingerprint == $fingerprint and .data.active.state == "active"
  ' <<<"$PROFILE_API_BODY" >/dev/null || fail "Core updater status did not expose the committed fingerprint"
  [[ "$("$DOCKER" inspect --format '{{.RestartCount}}' "$updater_id")" == "0" ]] ||
    fail "updater restarted during signed activation"
  worker_capability_count=$("$DOCKER" exec "$core_id" sqlite3 -readonly -cmd '.timeout 5000' /data/xirang.db \
    'SELECT count(*) FROM backup_asset_worker_capabilities;')
  [[ "$worker_capability_count" == "0" ]] || fail "parser reloaded capabilities without an explicit restart"

  profile_compose restart asset-worker >/dev/null
  ready=0
  local advertised_pipeline_fingerprint=""
  for _ in $(seq 1 60); do
    if [[ "$("$DOCKER" inspect --format '{{.State.Status}}' "$worker_id" 2>/dev/null || true)" == "running" ]]; then
      advertised_pipeline_fingerprint=$("$DOCKER" exec "$core_id" sqlite3 -readonly -cmd '.timeout 5000' /data/xirang.db \
        "SELECT pipeline_fingerprint FROM backup_asset_worker_capabilities WHERE capability='image.ocr' AND output_profile='tesseract_text_v1' AND health_state='ready' ORDER BY updated_at DESC LIMIT 1;")
      if [[ "$advertised_pipeline_fingerprint" == "$expected_pipeline_fingerprint" ]]; then
        ready=1
        break
      fi
    fi
    sleep 1
  done
  if [[ "$ready" != "1" ]]; then
    printf 'expected pipeline fingerprint: %s\n' "$expected_pipeline_fingerprint" >&2
    printf 'observed pipeline fingerprint: %s\n' "$advertised_pipeline_fingerprint" >&2
    "$DOCKER" exec "$core_id" sqlite3 -readonly -cmd '.headers on' -cmd '.mode list' /data/xirang.db \
      'SELECT capability,output_profile,pipeline_fingerprint,health_state FROM backup_asset_worker_capabilities ORDER BY capability,output_profile;' >&2 || true
    "$DOCKER" inspect --format '{{.Name}} status={{.State.Status}} exit={{.State.ExitCode}} restart={{.RestartCount}}' \
      "$core_id" "$worker_id" "$updater_id" >&2 || true
    "$DOCKER" logs "$worker_id" >&2 || true
    "$DOCKER" logs "$core_id" >&2 || true
    "$DOCKER" logs "$updater_id" >&2 || true
    fail "restarted parser did not advertise the activated pipeline fingerprint"
	fi
	profile_assert_worker_capabilities "$core_id"
	profile_api_request 200 GET /api/v1/admin/backup-asset-processing/capabilities
	profile_assert_core_capabilities
	jq -e '
	  .data.schema_version == 1 and
	  all(.data.items[]; .deployed and .ready_workers >= 1)
  ' <<<"$PROFILE_API_BODY" >/dev/null || fail "restarted parser capabilities were not ready"
  "$DOCKER" exec "$worker_id" /bin/sh -eu -c '
    test -L /var/lib/xirang/asset-worker-bundles/active
    test -d "$(readlink -f /var/lib/xirang/asset-worker-bundles/active)"
    test ! -e /var/lib/xirang/asset-worker-inbox/profile-smoke-candidate
    test ! -e /run/xirang/asset-worker-updater.sock
  ' || fail "restarted parser did not preserve active-bundle and mount isolation"
  [[ "$("$DOCKER" inspect --format '{{.RestartCount}}' "$updater_id")" == "0" ]] ||
    fail "updater restarted after parser capability registration"
  if "$DOCKER" logs "$worker_id" 2>&1 | grep -Fq -- 'asset-worker stopped with a safe protocol error'; then
    fail "parser reported a protocol error during signed activation"
  fi
  if "$DOCKER" logs "$updater_id" 2>&1 | grep -Fq -- 'asset-worker-updater stopped with a safe protocol error'; then
    fail "updater reported a protocol error during signed activation"
  fi

  profile_compose restart xirang >/dev/null
  ready=0
  for _ in $(seq 1 60); do
    if [[ "$("$DOCKER" inspect --format '{{.State.Health.Status}}' "$core_id" 2>/dev/null || true)" == "healthy" ]] &&
      "$DOCKER" exec "$core_id" test -S /run/xirang/worker/asset-worker.sock >/dev/null 2>&1 &&
      "$DOCKER" exec "$core_id" test -S /run/xirang/asset-worker-updater.sock >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 1
  done
  [[ "$ready" == "1" ]] || fail "profile sockets did not recover after Core restart"
  [[ "$("$DOCKER" exec "$core_id" cat /var/lib/xirang-asset-runtime/derived/.profile-smoke-persistence)" == "profile-derived-persistence" ]] ||
    fail "Derived Store did not persist across Core restart"
  [[ "$("$DOCKER" exec "$core_id" stat -c '%a:%u:%g' /var/lib/xirang-asset-runtime/derived)" == "700:10000:10000" ]] ||
    fail "Derived Store ownership or mode changed after Core restart"
  [[ "$("$DOCKER" exec "$core_id" cat /var/lib/xirang-asset-runtime/export/.profile-smoke-persistence)" == "profile-export-persistence" ]] ||
    fail "Export Store did not persist across Core restart"
  [[ "$("$DOCKER" exec "$core_id" stat -c '%a:%u:%g' /var/lib/xirang-asset-runtime/export)" == "700:10000:10000" ]] ||
    fail "Export Store ownership or mode changed after Core restart"
  if "$DOCKER" logs "$core_id" 2>&1 | grep -Fq -- '备份资产处理运行时不可用'; then
    fail "processing runtime was unavailable after Core restart"
  fi

  local post_restart_api_fingerprint="" post_restart_db_fingerprint=""
  local post_restart_active_target="" post_restart_pipeline_fingerprint=""
  local post_restart_capabilities_ready=0
  for _ in $(seq 1 60); do
    profile_api_request 200 GET /api/v1/admin/backup-asset-processing/updater
    post_restart_api_fingerprint=$(jq -r '.data.active.bundle_fingerprint // ""' <<<"$PROFILE_API_BODY")
    post_restart_db_fingerprint=$("$DOCKER" exec "$core_id" sqlite3 -readonly -cmd '.timeout 5000' /data/xirang.db \
      "SELECT bundle_fingerprint FROM backup_asset_updater_metadata WHERE state='active' ORDER BY updated_at DESC LIMIT 1;")
    post_restart_active_target=$("$DOCKER" exec "$updater_id" \
      readlink /var/lib/xirang/asset-worker-bundles/active 2>/dev/null || true)
    post_restart_pipeline_fingerprint=$("$DOCKER" exec "$core_id" sqlite3 -readonly -cmd '.timeout 5000' /data/xirang.db \
      "SELECT pipeline_fingerprint FROM backup_asset_worker_capabilities WHERE capability='image.ocr' AND output_profile='tesseract_text_v1' AND health_state='ready' ORDER BY updated_at DESC LIMIT 1;")
    if [[ "$post_restart_api_fingerprint" == "$expected_fingerprint" &&
      "$post_restart_db_fingerprint" == "$expected_fingerprint" &&
      "$post_restart_active_target" == "bundles/$expected_fingerprint" &&
      "$post_restart_pipeline_fingerprint" == "$expected_pipeline_fingerprint" ]]; then
      profile_api_request 200 GET /api/v1/admin/backup-asset-processing/capabilities
      profile_assert_core_capabilities
      profile_assert_worker_capabilities "$core_id"
      post_restart_capabilities_ready=1
      break
    fi
    sleep 1
  done
  [[ "$post_restart_api_fingerprint" == "$expected_fingerprint" &&
    "$post_restart_db_fingerprint" == "$expected_fingerprint" &&
    "$post_restart_active_target" == "bundles/$expected_fingerprint" ]] ||
    fail "post-restart signed activation fingerprint did not persist"
  [[ "$post_restart_pipeline_fingerprint" == "$expected_pipeline_fingerprint" &&
    "$post_restart_capabilities_ready" == "1" ]] ||
    fail "post-restart parser capability fingerprint did not persist"
  [[ "$("$DOCKER" inspect --format '{{.RestartCount}}' "$updater_id")" == "0" ]] ||
    fail "updater restarted across Core restart"

  echo "asset Worker complete profile smoke: PASS"
}

if [[ "$PROFILE_SMOKE" == "1" ]]; then
  run_profile_smoke
elif [[ "$PROFILE_SMOKE" != "0" ]]; then
  fail "ASSET_WORKER_PROFILE_SMOKE must be 0 or 1"
fi
echo "asset Worker runtime smoke: PASS"
