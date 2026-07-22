package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing/updater"
)

func TestParseAssetWorkerUpdaterOptionsUsesOnlyFixedPrivatePaths(t *testing.T) {
	options, err := parseAssetWorkerUpdaterOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.coreSocket != "/run/xirang/asset-worker-updater.sock" ||
		options.inboxRoot != "/var/lib/xirang/asset-worker-inbox" ||
		options.storeRoot != "/var/lib/xirang/asset-worker-bundles" ||
		options.trustFile != "/run/secrets/asset-worker-updater-trust.json" {
		t.Fatalf("unexpected fixed updater options: %+v", options)
	}
	if _, err := parseAssetWorkerUpdaterOptions([]string{"--inbox", "/caller/selected"}); err == nil {
		t.Fatal("updater accepted caller-selected path")
	}
}

func TestLoadAssetWorkerUpdaterTrustFileIsStableStrictAndPrivate(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	payload := fmt.Sprintf(
		`{"schema_version":1,"keys":[{"id":"key-2026","public_key":"%s","active_from":"%s","retire_after":"%s"}]}`,
		base64.StdEncoding.EncodeToString(publicKey), now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339),
	)
	path := filepath.Join(t.TempDir(), "trust.json")
	if err := os.WriteFile(path, []byte(payload), 0o400); err != nil {
		t.Fatal(err)
	}
	trust, err := loadAssetWorkerUpdaterTrustFile(path)
	if err != nil || len(trust.Keys) != 1 || !publicKey.Equal(trust.Keys[0].PublicKey) {
		t.Fatalf("trust=%+v err=%v", trust, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssetWorkerUpdaterTrustFile(path); err == nil {
		t.Fatal("world-readable trust file accepted")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(filepath.Dir(path), "trust-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssetWorkerUpdaterTrustFile(symlink); err == nil {
		t.Fatal("trust symlink accepted")
	}
	duplicate := strings.Replace(payload, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(duplicate), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssetWorkerUpdaterTrustFile(path); err == nil {
		t.Fatal("duplicate trust member accepted")
	}
}

func TestAssetWorkerUpdaterLoopRetriesTransportAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &assetWorkerUpdaterServiceFake{errors: []error{updater.ErrTemporarilyUnavailable, nil}}
	waits := 0
	err := runAssetWorkerUpdaterLoop(
		ctx,
		service,
		updater.TrustStore{},
		func() time.Time { return time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC) },
		func(context.Context, time.Duration) error {
			waits++
			if waits == 2 {
				cancel()
				return context.Canceled
			}
			return nil
		},
	)
	if err != nil || service.calls != 2 || waits != 2 {
		t.Fatalf("calls=%d waits=%d err=%v", service.calls, waits, err)
	}
}

func TestAssetWorkerUpdaterMainUsesOnlyUpdaterPrivateComponents(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"updater.NewUpdaterClient(", "updater.NewInbox(", "updater.NewStore(", "updater.NewActivator(",
		"updater.NewService(", "PollScanAndActivate(", "signal.NotifyContext(",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("asset-worker-updater main missing %q", required)
		}
	}
	for _, forbidden := range []string{"backupasset/provider", "backupasset/repository", "gin.", "multipart", "formdata"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("asset-worker-updater crossed private boundary with %q", forbidden)
		}
	}
}

type assetWorkerUpdaterServiceFake struct {
	calls  int
	errors []error
}

func (service *assetWorkerUpdaterServiceFake) PollScanAndActivate(
	context.Context,
	updater.TrustStore,
	time.Time,
) (int, error) {
	service.calls++
	if len(service.errors) == 0 {
		return 5, errors.New("unexpected poll")
	}
	err := service.errors[0]
	service.errors = service.errors[1:]
	return 5, err
}
