package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"xirang/backend/internal/backupasset/processing"
)

var errInvalidAssetWorkerConfig = errors.New("invalid asset-worker configuration")

type assetWorkerOptions struct {
	client processing.WorkerClientConfig
	runner processing.WorkerRunnerConfig
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runAssetWorker(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "asset-worker stopped with a safe protocol error")
		os.Exit(1)
	}
}

func runAssetWorker(ctx context.Context, args []string) error {
	options, err := parseAssetWorkerOptions(args)
	if err != nil {
		return err
	}
	if options.runner.InstanceID == "" {
		options.runner.InstanceID, err = newAssetWorkerInstanceID()
		if err != nil {
			return errInvalidAssetWorkerConfig
		}
	}
	client, err := processing.NewWorkerClient(options.client)
	if err != nil {
		return err
	}
	capabilities := processing.NewProductionWorkerCapabilitySet()
	runner, err := processing.NewWorkerRunner(client, capabilities, options.runner)
	if err != nil {
		client.CloseIdleConnections()
		return err
	}
	return runner.Run(ctx)
}

func parseAssetWorkerOptions(args []string) (assetWorkerOptions, error) {
	var options assetWorkerOptions
	flags := flag.NewFlagSet("asset-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.client.LocalSocketPath, "local-socket", "", "Core Unix socket")
	flags.StringVar(&options.client.RemoteEndpoint, "remote-endpoint", "", "Core private-IP mTLS endpoint")
	flags.StringVar(&options.client.RemoteClientCertFile, "remote-client-cert", "", "Worker client certificate")
	flags.StringVar(&options.client.RemoteClientKeyFile, "remote-client-key", "", "Worker client key")
	flags.StringVar(&options.client.RemoteServerCAFile, "remote-server-ca", "", "Core server CA")
	flags.Int64Var(&options.client.JSONMaxBytes, "json-max-bytes", 64<<10, "protocol JSON ceiling")
	flags.Int64Var(&options.client.InputMaxBytes, "input-max-bytes", 16<<20, "per-read Input ceiling")
	flags.Int64Var(&options.client.ArtifactMaxBytes, "artifact-max-bytes", 64<<20, "per-artifact ceiling")
	flags.DurationVar(&options.client.RequestTimeout, "request-timeout", 30*time.Second, "protocol request timeout")
	flags.StringVar(&options.runner.InstanceID, "instance-id", "", "optional non-secret instance ID")
	flags.Int64Var(&options.runner.IdentityRevision, "identity-revision", 1, "Worker identity revision")
	flags.IntVar(&options.runner.InteractiveSlots, "interactive-slots", 1, "interactive reserved slots")
	flags.IntVar(&options.runner.BackgroundSlots, "background-slots", 1, "background slots")
	flags.DurationVar(&options.runner.HeartbeatInterval, "heartbeat-interval", 10*time.Second, "attempt heartbeat interval")
	flags.DurationVar(&options.runner.PullBackoff, "pull-backoff", time.Second, "empty pull backoff")
	flags.DurationVar(&options.runner.GracePeriod, "grace-period", 30*time.Second, "bounded shutdown grace")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return assetWorkerOptions{}, errInvalidAssetWorkerConfig
	}
	local := strings.TrimSpace(options.client.LocalSocketPath)
	remote := strings.TrimSpace(options.client.RemoteEndpoint)
	if (local == "") == (remote == "") {
		return assetWorkerOptions{}, errInvalidAssetWorkerConfig
	}
	if local != "" {
		if !filepath.IsAbs(local) || filepath.Clean(local) != local || options.client.RemoteClientCertFile != "" ||
			options.client.RemoteClientKeyFile != "" || options.client.RemoteServerCAFile != "" {
			return assetWorkerOptions{}, errInvalidAssetWorkerConfig
		}
		options.client.LocalSocketPath = local
	} else if strings.TrimSpace(options.client.RemoteClientCertFile) == "" || strings.TrimSpace(options.client.RemoteClientKeyFile) == "" ||
		strings.TrimSpace(options.client.RemoteServerCAFile) == "" {
		return assetWorkerOptions{}, errInvalidAssetWorkerConfig
	}
	return options, nil
}

func newAssetWorkerInstanceID() (string, error) {
	payload := make([]byte, 16)
	if _, err := rand.Read(payload); err != nil {
		return "", err
	}
	return hex.EncodeToString(payload), nil
}
