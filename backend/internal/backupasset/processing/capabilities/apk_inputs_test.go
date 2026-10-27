package capabilities

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

func apkInputTestDigest(value string) string {
	digest := sha256.Sum256([]byte("FAKE_" + value + "_FOR_TEST_ONLY"))
	return hex.EncodeToString(digest[:])
}

func testToolchainAPKInputs() []toolchainAPKInput {
	return []toolchainAPKInput{
		{Role: "cgo-builder", Platform: "linux/amd64", ManifestSHA256: apkInputTestDigest("CGO_AMD64")},
		{Role: "cgo-builder", Platform: "linux/arm64", ManifestSHA256: apkInputTestDigest("CGO_ARM64")},
		{Role: "worker-runtime", Platform: "linux/amd64", ManifestSHA256: apkInputTestDigest("RUNTIME_AMD64")},
		{Role: "worker-runtime", Platform: "linux/arm64", ManifestSHA256: apkInputTestDigest("RUNTIME_ARM64")},
	}
}

func marshalAPKInputDocument(t *testing.T, inputs []toolchainAPKInput) []byte {
	t.Helper()
	payload, err := json.Marshal(productionAPKInputsDocument{SchemaVersion: 1, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestAPKInputDocumentRejectsIncompleteOrAmbiguousIdentities(t *testing.T) {
	valid := marshalAPKInputDocument(t, testToolchainAPKInputs())
	inputs, err := decodeProductionAPKInputs(append(append([]byte(nil), valid...), '\n'))
	if err != nil || !reflect.DeepEqual(inputs, testToolchainAPKInputs()) {
		t.Fatalf("valid input document=%v err=%v", inputs, err)
	}
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty"},
		{name: "unknown-schema", payload: bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1)},
		{name: "missing-schema", payload: bytes.Replace(valid, []byte(`"schema_version":1,`), nil, 1)},
		{name: "duplicate-schema", payload: bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)},
		{name: "unknown-field", payload: bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"source_url":"https://example.invalid"`), 1)},
		{name: "trailing-json", payload: append(append([]byte(nil), valid...), []byte(`{}`)...)},
		{name: "missing-platform", payload: marshalAPKInputDocument(t, testToolchainAPKInputs()[:3])},
		{name: "null-inputs", payload: []byte(`{"schema_version":1,"inputs":null}`)},
		{name: "oversized-whitespace", payload: append(bytes.Repeat([]byte(" "), maximumProductionAPKInputsBytes), valid...)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := decodeProductionAPKInputs(testCase.payload); !errors.Is(err, ErrInvalidInvocation) {
				t.Fatalf("invalid document accepted: err=%v", err)
			}
		})
	}
	mutations := []struct {
		name   string
		mutate func([]toolchainAPKInput) []toolchainAPKInput
	}{
		{name: "duplicate-platform", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput { inputs[1] = inputs[0]; return inputs }},
		{name: "core-only-input", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput { inputs[0].Role = "core-runtime"; return inputs }},
		{name: "supercronic-input", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput {
			inputs[0].Role = "supercronic-builder"
			return inputs
		}},
		{name: "extra-input", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput { return append(inputs, inputs[0]) }},
		{name: "unknown-platform", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput { inputs[0].Platform = "linux/386"; return inputs }},
		{name: "empty-digest", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput { inputs[0].ManifestSHA256 = ""; return inputs }},
		{name: "upper-digest", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput {
			inputs[0].ManifestSHA256 = strings.ToUpper(inputs[0].ManifestSHA256)
			return inputs
		}},
		{name: "zero-digest", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput {
			inputs[0].ManifestSHA256 = strings.Repeat("0", 64)
			return inputs
		}},
		{name: "prefixed-digest", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput {
			inputs[0].ManifestSHA256 = "sha256:" + inputs[0].ManifestSHA256
			return inputs
		}},
		{name: "unsorted-document", mutate: func(inputs []toolchainAPKInput) []toolchainAPKInput {
			inputs[0], inputs[3] = inputs[3], inputs[0]
			return inputs
		}},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			payload := marshalAPKInputDocument(t, testCase.mutate(testToolchainAPKInputs()))
			if _, err := decodeProductionAPKInputs(payload); !errors.Is(err, ErrInvalidInvocation) {
				t.Fatalf("invalid input identity accepted: err=%v", err)
			}
		})
	}
}

func TestToolchainFingerprintBindsEveryWorkerAPKInput(t *testing.T) {
	inventory := productionToolchainInventory()
	inventory.APKInputs = testToolchainAPKInputs()
	fingerprint, err := toolchainFingerprint(inventory)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalToolchainInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(append([]byte("xirang.asset.toolchain.v3\x00"), payload...))
	if fingerprint != hex.EncodeToString(wantDigest[:]) {
		t.Fatal("complete APK input identity did not use the v3 domain separator")
	}
	profile := capabilityspec.WorkerProfiles()[0]
	pipeline, err := profile.PipelineFingerprint(fingerprint, nil, "input-lock-test-v1")
	if err != nil {
		t.Fatal(err)
	}
	for index, input := range inventory.APKInputs {
		t.Run(input.Role+"/"+input.Platform, func(t *testing.T) {
			changed := cloneToolchainInventory(inventory)
			changed.APKInputs[index].ManifestSHA256 = apkInputTestDigest("CHANGED_" + input.Role + input.Platform)
			got, err := toolchainFingerprint(changed)
			if err != nil || got == fingerprint {
				t.Fatalf("changed complete APK input did not advance toolchain identity: fingerprint=%q err=%v", got, err)
			}
			changedPipeline, err := profile.PipelineFingerprint(got, nil, "input-lock-test-v1")
			if err != nil || changedPipeline == pipeline {
				t.Fatalf("changed complete APK input did not advance pipeline identity: fingerprint=%q err=%v", changedPipeline, err)
			}
			if inventory.APKInputs[index] != input {
				t.Fatal("changing a cloned inventory modified the original input lock")
			}
		})
	}
	reordered := cloneToolchainInventory(inventory)
	for left, right := 0, len(reordered.APKInputs)-1; left < right; left, right = left+1, right-1 {
		reordered.APKInputs[left], reordered.APKInputs[right] = reordered.APKInputs[right], reordered.APKInputs[left]
	}
	before := append([]toolchainAPKInput(nil), reordered.APKInputs...)
	got, err := toolchainFingerprint(reordered)
	if err != nil || got != fingerprint || !reflect.DeepEqual(reordered.APKInputs, before) {
		t.Fatalf("canonicalization changed identity or mutated its caller: fingerprint=%q err=%v", got, err)
	}
	legacy := cloneToolchainInventory(inventory)
	legacy.SchemaVersion = "xirang.asset.toolchain.v2"
	if _, err := toolchainFingerprint(legacy); !errors.Is(err, ErrInvalidInvocation) {
		t.Fatalf("legacy schema accepted the new APK identity contract: err=%v", err)
	}
}

func TestInvalidAPKInputsCloseToolchainPreflight(t *testing.T) {
	inventory := productionToolchainInventory()
	inventory.APKInputs = nil
	result := preflightProductionToolchain(context.Background(), inventory, "/invalid-unused-bundle", "invalid-unused-attestation")
	if result.Fingerprint != "" || result.RuntimeClosureReady || result.UngatedAvailableCount != 0 ||
		result.RuntimeClosureFailureCategory != RuntimeClosureFailureEvidence {
		t.Fatalf("invalid input lock produced a ready preflight: %+v", result)
	}
	for capability, available := range result.AvailableCapabilities {
		if available {
			t.Fatalf("invalid input lock advertised %s", capability)
		}
	}
}

type testAPKInputLock struct {
	SchemaVersion int `json:"schema_version"`
	Bases         map[string]struct {
		Source string `json:"source"`
		Ref    string `json:"ref"`
	} `json:"bases"`
	Profiles map[string]struct {
		Base      string   `json:"base"`
		Roots     []string `json:"roots"`
		Platforms map[string]struct {
			ManifestSHA256 string `json:"manifest_sha256"`
		} `json:"platforms"`
	} `json:"profiles"`
}

func readProductionAPKInputLock(t *testing.T) testAPKInputLock {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..", "..")
	payload, err := os.ReadFile(filepath.Join(root, "deploy", "build-inputs", "lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock testAPKInputLock
	if err := json.Unmarshal(payload, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.SchemaVersion != 1 {
		t.Fatalf("unknown build input lock schema %d", lock.SchemaVersion)
	}
	return lock
}

func TestProductionAPKInputsMatchRetainedLock(t *testing.T) {
	lock := readProductionAPKInputLock(t)
	inputs, err := decodeProductionAPKInputs(productionAPKInputDocument)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range inputs {
		locked := lock.Profiles[input.Role].Platforms[input.Platform]
		if input.ManifestSHA256 != locked.ManifestSHA256 {
			t.Errorf("embedded %s/%s input differs from the retained input lock", input.Role, input.Platform)
		}
	}
	inventory := productionToolchainInventory()
	if lock.Profiles["cgo-builder"].Base != "go" || inventory.BuilderBase != lock.Bases["go"].Source ||
		lock.Profiles["worker-runtime"].Base != "alpine" || inventory.RuntimeBase != lock.Bases["alpine"].Source {
		t.Fatal("Worker inventory base identity differs from the APK input lock")
	}
}
