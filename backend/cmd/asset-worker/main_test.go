package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	bundles := filepath.Join(root, "bundles")
	fingerprint := strings.Repeat("a", 64)
	bundle := filepath.Join(bundles, fingerprint)
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bundle, 0o555); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(root, "active")
	if err := os.Symlink(filepath.Join("bundles", fingerprint), active); err != nil {
		t.Fatal(err)
	}
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

func TestAssetWorkerMainBuildsVerifiedToolRunnerWithoutPrivilegedImports(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"processing.NewWorkerClient(",
		"capabilities.NewRunner(",
		"processing.NewProductionWorkerCapabilitySetWithOptions(",
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
