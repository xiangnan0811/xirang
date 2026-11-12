package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/processing/updater"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func write(path string, data []byte) { must(os.WriteFile(path, data, 0o600)) }

func jsonBytes(value any) []byte {
	data, err := json.Marshal(value)
	must(err)
	return data
}

func createProfileFixture(root string) {
	profileDir := filepath.Join(root, "profile")
	must(os.Mkdir(profileDir, 0o700))
	observed := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	closures := map[string][]byte{
		"linux/amd64": []byte("{\"schema_version\":1,\"platform\":\"linux/amd64\",\"files\":[\"fixture\"]}\n"),
		"linux/arm64": []byte("{\"schema_version\":1,\"platform\":\"linux/arm64\",\"files\":[\"fixture\"]}\n"),
	}
	digests := map[string]string{}
	for _, arch := range []string{"amd64", "arm64"} {
		data := closures["linux/"+arch]
		digests["linux/"+arch] = updater.SHA256Hex(data)
		write(filepath.Join(profileDir, "runtime-closure-"+arch+".v1.json"), data)
		write(filepath.Join(root, "native-"+arch+".json"), data)
	}
	attestations := jsonBytes(map[string]any{
		"schema_version": 1,
		"attestations": []map[string]string{
			{"platform": "linux/amd64", "runtime_manifest_sha256": digests["linux/amd64"]},
			{"platform": "linux/arm64", "runtime_manifest_sha256": digests["linux/arm64"]},
		},
	})
	profiles := map[string][]string{}
	schemas := map[string]string{}
	for _, profile := range capabilityspec.WorkerProfiles() {
		profiles[profile.Capability] = append(profiles[profile.Capability], profile.OutputProfile)
		schemas[profile.Capability] = profile.CapabilitySchema
	}
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	capabilities := make([]updater.ManifestCapability, 0, len(names))
	for _, name := range names {
		sort.Strings(profiles[name])
		capabilities = append(capabilities, updater.ManifestCapability{
			Capability: name, Schema: schemas[name], Profiles: profiles[name],
			ToolRevision: "fixture-toolchain", ModelRevision: "fixture-model", DataRevision: "none",
		})
	}
	privateKey := ed25519.NewKeyFromSeed([]byte("ci-evidence-fixture-key-seed-001"))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	bundle, files, err := updater.BuildCanonicalTar([]updater.BundleFilePayload{
		{Path: "models/model.dat", Mode: 0o444, Content: []byte("fixture-model\n")},
		{Path: "toolchain/attestations.v1.json", Mode: 0o444, Content: attestations},
	})
	must(err)
	manifest := updater.Manifest{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "fixture", Version: "1.0.0",
		CreatedAt: observed.Add(-time.Minute), ExpiresAt: observed.Add(time.Hour),
		Capabilities: capabilities, Files: files, BundleSHA256: updater.SHA256Hex(bundle),
		SigningKeyID: "fixture-key", SignatureAlgorithm: "ed25519",
	}
	must(updater.SignManifest(&manifest, privateKey))
	manifestJSON := jsonBytes(manifest)
	trust := updater.TrustStore{Keys: []updater.TrustedKey{{
		ID: "fixture-key", PublicKey: publicKey, ActiveFrom: observed.Add(-time.Hour), RetireAfter: observed.Add(time.Hour),
	}}}
	verified, err := updater.VerifyPackage(manifestJSON, bundle, trust, observed)
	must(err)
	bundles := make(processing.CapabilityBundleFingerprints)
	for _, profile := range capabilityspec.WorkerProfiles() {
		bundles[profile.Capability] = []string{verified.BundleFingerprint}
	}
	set, err := processing.NewProductionWorkerCapabilitySetWithBundles(bundles)
	must(err)
	pipeline := ""
	for _, item := range set.Advertisements() {
		if item.Capability == capabilityspec.CapabilityImageOCR && item.OutputProfile == capabilityspec.ProfileTesseractTextV1 {
			pipeline = item.PipelineFingerprint
		}
	}
	if pipeline == "" {
		panic("missing pipeline")
	}
	trustJSON := jsonBytes(map[string]any{"schema_version": 1, "keys": []map[string]string{{
		"id": "fixture-key", "public_key": base64.StdEncoding.EncodeToString(publicKey),
		"active_from":  observed.Add(-time.Hour).Format(time.RFC3339),
		"retire_after": observed.Add(time.Hour).Format(time.RFC3339),
	}}})
	receipt := jsonBytes(map[string]any{
		"schema_version": 1, "kind": "worker-profile-smoke", "repository": "fixture/example",
		"run_id": 123, "run_attempt": 1, "checkout_sha": strings.Repeat("a", 40),
		"observed_at": observed.Format(time.RFC3339), "closures": digests,
		"bundle_fingerprint": verified.BundleFingerprint, "pipeline_fingerprint": pipeline,
		"post_restart_bundle_fingerprint": verified.BundleFingerprint, "post_restart_pipeline_fingerprint": pipeline,
	})
	write(filepath.Join(profileDir, "manifest.json"), manifestJSON)
	write(filepath.Join(profileDir, "bundle.tar"), bundle)
	write(filepath.Join(profileDir, "trust.json"), trustJSON)
	write(filepath.Join(profileDir, "attestations.v1.json"), attestations)
	write(filepath.Join(profileDir, "receipt.json"), receipt)
}

func TestVerifyCIProfileEvidence(t *testing.T) {
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base, err := filepath.Abs(filepath.Join(working, "..", "..", "..", ".tmp", "agent", "round29-remediation-execution", "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("CI_PROFILE_FIXTURE_OUTPUT")
	if root == "" {
		root, err = os.MkdirTemp(base, "profile-verifier-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	} else {
		root, err = filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(base, root)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			t.Fatal("fixture outside owned scratch")
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatal("fixture output must be an empty directory")
		}
	}
	createProfileFixture(root)
	profile := filepath.Join(root, "profile")
	args := []string{profile, filepath.Join(root, "native-amd64.json"), filepath.Join(root, "native-arm64.json"), "fixture/example", "123", "1", strings.Repeat("a", 40)}
	if err := verify(args); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	mutate := func(t *testing.T, path string, replacement func([]byte) []byte) {
		t.Helper()
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, replacement(original), 0o600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Error(err)
			}
		}()
		if err := verify(args); err == nil {
			t.Fatal("tampered evidence accepted")
		}
	}
	t.Run("native closure", func(t *testing.T) { mutate(t, args[2], func(b []byte) []byte { return append(b, ' ') }) })
	t.Run("signed manifest", func(t *testing.T) {
		mutate(t, filepath.Join(profile, "manifest.json"), func(b []byte) []byte {
			var object map[string]any
			if err := json.Unmarshal(b, &object); err != nil {
				t.Fatal(err)
			}
			object["version"] = "1.0.1"
			return jsonBytes(object)
		})
	})
	t.Run("attestation", func(t *testing.T) {
		mutate(t, filepath.Join(profile, "attestations.v1.json"), func(b []byte) []byte { return append(b, ' ') })
	})
	t.Run("receipt fingerprint", func(t *testing.T) {
		mutate(t, filepath.Join(profile, "receipt.json"), func(b []byte) []byte {
			var object map[string]any
			if err := json.Unmarshal(b, &object); err != nil {
				t.Fatal(err)
			}
			object["post_restart_pipeline_fingerprint"] = strings.Repeat("0", 64)
			return jsonBytes(object)
		})
	})
	t.Run("wrong run", func(t *testing.T) {
		other := append([]string(nil), args...)
		other[4] = "124"
		if err := verify(other); err == nil {
			t.Fatal("wrong run accepted")
		}
	})
}
