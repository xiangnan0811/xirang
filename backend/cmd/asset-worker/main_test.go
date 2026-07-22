package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing/capabilities"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

func TestRunAssetWorkerRuntimeClosureCommandIsExactAndIsolated(t *testing.T) {
	sentinel := errors.New("writer failed")
	calls := 0
	writer := func() error {
		calls++
		return sentinel
	}
	if err := runAssetWorkerWithRuntimeClosureWriter(
		context.Background(), []string{assetWorkerRuntimeClosureCommand}, writer,
	); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("runtime closure command err=%v calls=%d", err, calls)
	}
	if err := runAssetWorkerWithRuntimeClosureWriter(
		context.Background(), []string{assetWorkerRuntimeClosureCommand, "extra"}, writer,
	); !errors.Is(err, errInvalidAssetWorkerConfig) || calls != 1 {
		t.Fatalf("mixed runtime closure command err=%v calls=%d", err, calls)
	}
}

func TestParseAssetWorkerOptionsRequiresExactlyOneConfiguredTransport(t *testing.T) {
	options, err := parseAssetWorkerOptions([]string{
		"--local-socket", "/run/xirang/asset-worker.sock",
		"--interactive-slots", "2", "--background-slots", "3",
		"--heartbeat-interval", "5s", "--grace-period", "20s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.client.LocalSocketPath != "/run/xirang/asset-worker.sock" || options.client.RemoteEndpoint != "" ||
		options.runner.InteractiveSlots != 2 || options.runner.BackgroundSlots != 3 ||
		options.runner.HeartbeatInterval != 5*time.Second || options.runner.GracePeriod != 20*time.Second ||
		options.tools.WorkspaceRoot != "/run/xirang/asset-jobs" ||
		options.bundleRoot != "/var/lib/xirang/asset-worker-bundles" {
		t.Fatalf("unexpected parsed options: %+v", options)
	}
	invalid := [][]string{
		{},
		{"--local-socket", "/run/xirang/worker.sock", "--remote-endpoint", "https://127.0.0.1:9443"},
		{"--local-socket", "relative.sock"},
		{"--local-socket", "/run/xirang/worker.sock", "--workspace-root", "relative"},
		{"--local-socket", "/run/xirang/worker.sock", "positional"},
	}
	for index, args := range invalid {
		if _, err := parseAssetWorkerOptions(args); err == nil {
			t.Fatalf("invalid option set %d accepted: %v", index, args)
		}
	}
}

func TestLoadActiveBundleFingerprintAcceptsOnlyContainedImmutableTarget(t *testing.T) {
	root := t.TempDir()
	makeAssetWorkerTestTreeRemovable(t, root)
	bundles := filepath.Join(root, "bundles")
	model := []byte("model-v1")
	receiptPayload, fingerprint := makeAssetWorkerBundleReceipt(t, map[string][]byte{"model.dat": model})
	bundle := filepath.Join(bundles, fingerprint)
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "model.dat"), model, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundle, 0o555); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(root, "active")
	if err := os.Symlink(filepath.Join("bundles", fingerprint), active); err != nil {
		t.Fatal(err)
	}
	if _, err := loadActiveBundleFingerprint(root); err == nil {
		t.Fatal("active bundle without a verified stored-tree receipt accepted")
	}
	writeAssetWorkerBundleReceipt(t, bundle, receiptPayload)
	got, err := loadActiveBundleFingerprint(root)
	if err != nil || got != fingerprint {
		t.Fatalf("fingerprint=%q err=%v", got, err)
	}
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bundle, active); err != nil {
		t.Fatal(err)
	}
	if _, err := loadActiveBundleFingerprint(root); err == nil {
		t.Fatal("absolute active target accepted")
	}
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("bundles", "missing"), active); err != nil {
		t.Fatal(err)
	}
	if _, err := loadActiveBundleFingerprint(root); err == nil {
		t.Fatal("missing active bundle accepted")
	}
}

func TestLoadActiveBundleFingerprintRejectsReceiptAndTreeTampering(t *testing.T) {
	root := t.TempDir()
	makeAssetWorkerTestTreeRemovable(t, root)
	bundles := filepath.Join(root, "bundles")
	attestation := []byte(`{"schema_version":1,"attestations":[]}`)
	receiptPayload, fingerprint := makeAssetWorkerBundleReceipt(t, map[string][]byte{"toolchain/attestations.v1.json": attestation})
	bundle := filepath.Join(bundles, fingerprint)
	if err := os.MkdirAll(filepath.Join(bundle, "toolchain"), 0o755); err != nil {
		t.Fatal(err)
	}
	attestationPath := filepath.Join(bundle, "toolchain", "attestations.v1.json")
	if err := os.WriteFile(attestationPath, attestation, 0o444); err != nil {
		t.Fatal(err)
	}
	writeAssetWorkerBundleReceipt(t, bundle, receiptPayload)
	if err := os.Chmod(filepath.Join(bundle, "toolchain"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundle, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("bundles", fingerprint), filepath.Join(root, "active")); err != nil {
		t.Fatal(err)
	}
	if got, err := loadActiveBundleFingerprint(root); err != nil || got != fingerprint {
		t.Fatalf("valid stored bundle fingerprint=%q err=%v", got, err)
	}
	if err := os.Chmod(attestationPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attestationPath, []byte(`{"schema_version":1,"attestations":[{"platform":"linux/amd64"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(attestationPath, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := loadActiveBundleFingerprint(root); err == nil {
		t.Fatal("tampered active bundle tree accepted")
	}
	receiptPath := filepath.Join(bundle, capabilities.StoredBundleReceiptPath)
	receiptPayload, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := capabilities.DecodeStoredBundleReceipt(receiptPayload)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Files[0].Size = int64(len(`{"schema_version":1,"attestations":[{"platform":"linux/amd64"}]}`))
	receipt.Files[0].SHA256 = capabilities.SHA256Hex([]byte(`{"schema_version":1,"attestations":[{"platform":"linux/amd64"}]}`))
	forgedReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(receiptPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, forgedReceipt, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(receiptPath, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundle, 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := loadActiveBundleFingerprint(root); err == nil {
		t.Fatal("forged receipt retained an unchanged active fingerprint")
	}
}

func makeAssetWorkerBundleReceipt(t *testing.T, files map[string][]byte) ([]byte, string) {
	t.Helper()
	declarations := make([]capabilities.StoredBundleFile, 0, len(files))
	for path, content := range files {
		declarations = append(declarations, capabilities.StoredBundleFile{
			Path: path, Mode: 0o444, Size: int64(len(content)), SHA256: capabilities.SHA256Hex(content),
		})
	}
	sort.Slice(declarations, func(left, right int) bool { return declarations[left].Path < declarations[right].Path })
	receipt := capabilities.StoredBundleReceipt{
		SchemaVersion: 1, ManifestSchemaVersion: 1,
		Capabilities: []capabilities.StoredBundleCapability{{
			Capability: "image.ocr", Schema: "image.ocr.v1", Profiles: []string{"tesseract_text_v1"},
			ToolRevision: "tool-v1", ModelRevision: "model-v1", DataRevision: "data-v1",
		}},
		Files: declarations, BundleSHA256: strings.Repeat("e", 64),
	}
	fingerprint, err := capabilities.StoredBundleFingerprint(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.BundleFingerprint = fingerprint
	payload, err := capabilities.EncodeStoredBundleReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return payload, fingerprint
}

func writeAssetWorkerBundleReceipt(t *testing.T, root string, payload []byte) {
	t.Helper()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, capabilities.StoredBundleReceiptPath), payload, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
}

func makeAssetWorkerTestTreeRemovable(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
}

func TestNewAssetWorkerInstanceIDIsOpaqueNonSecret(t *testing.T) {
	first, err := newAssetWorkerInstanceID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newAssetWorkerInstanceID()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || len(second) != 32 || first == second {
		t.Fatalf("invalid instance IDs %q %q", first, second)
	}
	for _, value := range []string{first, second} {
		for _, character := range value {
			if character < '0' || character > '9' && character < 'a' || character > 'f' {
				t.Fatalf("instance ID is not lowercase hex: %q", value)
			}
		}
	}
}

func TestAssetWorkerBundleFingerprintsBindOptionalSecretCapability(t *testing.T) {
	fingerprint := strings.Repeat("d", 64)
	bundles := assetWorkerBundleFingerprints(fingerprint)
	if len(bundles) != len(capabilityspec.WorkerProfiles()) ||
		len(bundles[capabilityspec.CapabilitySecretClassify]) != 1 ||
		bundles[capabilityspec.CapabilitySecretClassify][0] != fingerprint {
		t.Fatalf("closed Worker bundle identities=%v", bundles)
	}
}

func TestAssetWorkerMainBuildsVerifiedToolRunnerWithoutPrivilegedImports(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"processing.NewWorkerClient(",
		"capabilities.NewRunner(",
		"capabilities.PreflightProductionToolchain(",
		"processing.NewProductionWorkerCapabilitySetWithOptions(",
		"productionOptions.AvailableCapabilities =",
		"productionOptions.BundleFingerprints =",
		"processing.NewWorkerRunner(",
		"signal.NotifyContext(",
		"syscall.SIGTERM",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("asset-worker main is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"NewWorkerCapabilitySet(", "noop", "tesseract", "ffmpeg", "libreoffice", "clamav",
		"backupasset/provider", "backupasset/repository", "updater", "docker", "compose",
	} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("asset-worker main crossed protocol-only boundary with %q", forbidden)
		}
	}
}
