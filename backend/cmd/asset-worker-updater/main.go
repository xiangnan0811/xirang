package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"xirang/backend/internal/backupasset/processing/updater"
)

var errInvalidAssetWorkerUpdaterConfig = errors.New("invalid asset-worker-updater configuration")

const (
	assetWorkerUpdaterCoreSocket = "/run/xirang/asset-worker-updater.sock"
	assetWorkerUpdaterInboxRoot  = "/var/lib/xirang/asset-worker-inbox"
	assetWorkerUpdaterStoreRoot  = "/var/lib/xirang/asset-worker-bundles"
	assetWorkerUpdaterTrustFile  = "/run/secrets/asset-worker-updater-trust.json"
	assetWorkerUpdaterTrustMax   = int64(64 << 10)
	assetWorkerUpdaterPackageMax = int64((1 << 30) + (1 << 20))
)

type assetWorkerUpdaterOptions struct {
	coreSocket string
	inboxRoot  string
	storeRoot  string
	trustFile  string
}

type assetWorkerUpdaterService interface {
	PollScanAndActivate(context.Context, updater.TrustStore, time.Time) (int, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runAssetWorkerUpdater(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "asset-worker-updater stopped with a safe protocol error")
		os.Exit(1)
	}
}

func parseAssetWorkerUpdaterOptions(args []string) (assetWorkerUpdaterOptions, error) {
	if len(args) != 0 {
		return assetWorkerUpdaterOptions{}, errInvalidAssetWorkerUpdaterConfig
	}
	return assetWorkerUpdaterOptions{
		coreSocket: assetWorkerUpdaterCoreSocket,
		inboxRoot:  assetWorkerUpdaterInboxRoot,
		storeRoot:  assetWorkerUpdaterStoreRoot,
		trustFile:  assetWorkerUpdaterTrustFile,
	}, nil
}

func runAssetWorkerUpdater(ctx context.Context, args []string) error {
	options, err := parseAssetWorkerUpdaterOptions(args)
	if err != nil || ctx == nil {
		return errInvalidAssetWorkerUpdaterConfig
	}
	trust, err := loadAssetWorkerUpdaterTrustFile(options.trustFile)
	if err != nil {
		return errInvalidAssetWorkerUpdaterConfig
	}
	inbox, err := updater.NewInbox(options.inboxRoot, assetWorkerUpdaterPackageMax)
	if err != nil {
		return errInvalidAssetWorkerUpdaterConfig
	}
	store, err := updater.NewStore(options.storeRoot)
	if err != nil {
		return errInvalidAssetWorkerUpdaterConfig
	}
	activator, err := updater.NewActivator(options.storeRoot)
	if err != nil {
		return errInvalidAssetWorkerUpdaterConfig
	}
	client, err := updater.NewUpdaterClient(updater.UpdaterClientConfig{
		SocketPath: options.coreSocket, JSONMaxBytes: 64 << 10, RequestTimeout: 30 * time.Second,
	})
	if err != nil {
		return errInvalidAssetWorkerUpdaterConfig
	}
	defer client.CloseIdleConnections()
	service, err := updater.NewService(
		inbox, store, activator, client, filepath.Join(options.storeRoot, "candidate-journal.json"),
	)
	if err != nil {
		return errInvalidAssetWorkerUpdaterConfig
	}
	return runAssetWorkerUpdaterLoop(ctx, service, trust, func() time.Time { return time.Now().UTC() }, waitAssetWorkerUpdater)
}

func loadAssetWorkerUpdaterTrustFile(path string) (updater.TrustStore, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return updater.TrustStore{}, errInvalidAssetWorkerUpdaterConfig
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() <= 0 ||
		before.Size() > assetWorkerUpdaterTrustMax || before.Mode().Perm()&0o007 != 0 ||
		before.Mode().Perm()&0o222 != 0 || before.Mode().Perm()&0o440 == 0 {
		return updater.TrustStore{}, errInvalidAssetWorkerUpdaterConfig
	}
	handle, err := os.Open(path)
	if err != nil {
		return updater.TrustStore{}, errInvalidAssetWorkerUpdaterConfig
	}
	payload, readErr := io.ReadAll(io.LimitReader(handle, assetWorkerUpdaterTrustMax+1))
	opened, statErr := handle.Stat()
	closeErr := handle.Close()
	after, afterErr := os.Lstat(path)
	if readErr != nil || statErr != nil || closeErr != nil || afterErr != nil || int64(len(payload)) > assetWorkerUpdaterTrustMax ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) || opened.Size() != int64(len(payload)) {
		return updater.TrustStore{}, errInvalidAssetWorkerUpdaterConfig
	}
	trust, err := updater.DecodeTrustStore(payload)
	if err != nil {
		return updater.TrustStore{}, errInvalidAssetWorkerUpdaterConfig
	}
	return trust, nil
}

func runAssetWorkerUpdaterLoop(
	ctx context.Context,
	service assetWorkerUpdaterService,
	trust updater.TrustStore,
	now func() time.Time,
	wait func(context.Context, time.Duration) error,
) error {
	if ctx == nil || service == nil || now == nil || wait == nil {
		return errInvalidAssetWorkerUpdaterConfig
	}
	for {
		retryAfter, err := service.PollScanAndActivate(ctx, trust, now().UTC())
		if err != nil && !errors.Is(err, updater.ErrTemporarilyUnavailable) {
			if ctx.Err() != nil {
				return nil
			}
			return errInvalidAssetWorkerUpdaterConfig
		}
		if retryAfter < 1 || retryAfter > 60 {
			retryAfter = 5
		}
		if waitErr := wait(ctx, time.Duration(retryAfter)*time.Second); waitErr != nil {
			if ctx.Err() != nil || errors.Is(waitErr, context.Canceled) {
				return nil
			}
			return errInvalidAssetWorkerUpdaterConfig
		}
	}
}

func waitAssetWorkerUpdater(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
