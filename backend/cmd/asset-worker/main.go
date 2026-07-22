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
	"xirang/backend/internal/backupasset/processing/capabilities"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

var errInvalidAssetWorkerConfig = errors.New("invalid asset-worker configuration")

type assetWorkerOptions struct {
	client     processing.WorkerClientConfig
	runner     processing.WorkerRunnerConfig
	tools      capabilities.RunnerConfig
	bundleRoot string
}

const assetWorkerBundleRoot = "/var/lib/xirang/asset-worker-bundles"

const assetWorkerRuntimeClosureCommand = "write-runtime-closure-manifest"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runAssetWorker(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "asset-worker stopped with a safe protocol error")
		os.Exit(1)
	}
}

func runAssetWorker(ctx context.Context, args []string) error {
	return runAssetWorkerWithRuntimeClosureWriter(ctx, args, capabilities.WriteProductionRuntimeClosureManifest)
}

func runAssetWorkerWithRuntimeClosureWriter(ctx context.Context, args []string, writer func() error) error {
	if len(args) == 1 && args[0] == assetWorkerRuntimeClosureCommand {
		if writer == nil {
			return errInvalidAssetWorkerConfig
		}
		return writer()
	}
	return runAssetWorkerService(ctx, args)
}

func runAssetWorkerService(ctx context.Context, args []string) error {
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
	toolRunner, toolErr := capabilities.NewRunner(options.tools)
	bundleEvidence, bundleErr := loadActiveBundleEvidence(options.bundleRoot)
	preflight := capabilities.ToolchainPreflight{}
	if bundleErr == nil {
		preflight = capabilities.PreflightProductionToolchain(
			ctx, bundleEvidence.Root, bundleEvidence.ToolchainAttestationSHA256,
		)
	}
	availableCount := 0
	for _, available := range preflight.AvailableCapabilities {
		if available {
			availableCount++
		}
	}
	_, _ = fmt.Fprintf(os.Stderr, "asset-worker diagnostic stage=preflight bundle_ready=%t runner_ready=%t available=%d\n", bundleErr == nil, toolErr == nil, availableCount)
	productionOptions := processing.ProductionWorkerCapabilityOptions{}
	if toolErr == nil && bundleErr == nil && preflight.Fingerprint != "" {
		productionOptions.ToolRunner = toolRunner
		productionOptions.AvailableCapabilities = preflight.AvailableCapabilities
		productionOptions.BundleFingerprints = assetWorkerBundleFingerprints(bundleEvidence.Fingerprint)
	}
	capabilitySet, err := processing.NewProductionWorkerCapabilitySetWithOptions(productionOptions)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "asset-worker diagnostic stage=capability_set")
		client.CloseIdleConnections()
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "asset-worker diagnostic stage=capability_set_ready advertisements=%d\n", len(capabilitySet.Advertisements()))
	runner, err := processing.NewWorkerRunner(client, capabilitySet, options.runner)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "asset-worker diagnostic stage=runner_create")
		client.CloseIdleConnections()
		return err
	}
	return runner.Run(ctx)
}

func parseAssetWorkerOptions(args []string) (assetWorkerOptions, error) {
	options := assetWorkerOptions{bundleRoot: assetWorkerBundleRoot}
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
	flags.StringVar(&options.tools.WorkspaceRoot, "workspace-root", "/run/xirang/asset-jobs", "private noexec tmpfs workspace")
	flags.IntVar(&options.tools.StdoutLimit, "tool-stdout-limit", 16<<10, "bounded tool stdout")
	flags.IntVar(&options.tools.StderrLimit, "tool-stderr-limit", 16<<10, "bounded tool stderr")
	flags.DurationVar(&options.tools.GracePeriod, "tool-grace-period", time.Second, "tool process termination grace")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return assetWorkerOptions{}, errInvalidAssetWorkerConfig
	}
	local := strings.TrimSpace(options.client.LocalSocketPath)
	remote := strings.TrimSpace(options.client.RemoteEndpoint)
	if (local == "") == (remote == "") {
		return assetWorkerOptions{}, errInvalidAssetWorkerConfig
	}
	if !filepath.IsAbs(options.tools.WorkspaceRoot) || filepath.Clean(options.tools.WorkspaceRoot) != options.tools.WorkspaceRoot ||
		options.tools.WorkspaceRoot == "/" {
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

type activeBundleEvidence struct {
	Fingerprint                string
	Root                       string
	ToolchainAttestationSHA256 string
}

func loadActiveBundleFingerprint(root string) (string, error) {
	evidence, err := loadActiveBundleEvidence(root)
	return evidence.Fingerprint, err
}

func loadActiveBundleEvidence(root string) (activeBundleEvidence, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(os.PathSeparator) ||
		strings.ContainsAny(root, "\x00\r\n") {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm()&0o002 != 0 {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	bundlesRoot := filepath.Join(root, "bundles")
	bundlesInfo, err := os.Lstat(bundlesRoot)
	if err != nil || !bundlesInfo.IsDir() || bundlesInfo.Mode()&os.ModeSymlink != 0 || bundlesInfo.Mode().Perm()&0o002 != 0 {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	activePath := filepath.Join(root, "active")
	activeInfo, err := os.Lstat(activePath)
	if err != nil || activeInfo.Mode()&os.ModeSymlink == 0 {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	target, err := os.Readlink(activePath)
	prefix := "bundles" + string(os.PathSeparator)
	if err != nil || filepath.IsAbs(target) || filepath.Clean(target) != target || !strings.HasPrefix(target, prefix) {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	fingerprint := strings.TrimPrefix(target, prefix)
	if strings.Contains(fingerprint, string(os.PathSeparator)) || !lowerHexAssetWorker(fingerprint, 64) {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	bundleRoot := filepath.Join(bundlesRoot, fingerprint)
	bundleInfo, err := os.Lstat(bundleRoot)
	if err != nil || !bundleInfo.IsDir() || bundleInfo.Mode()&os.ModeSymlink != 0 || bundleInfo.Mode().Perm() != 0o555 {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	storedEvidence, err := capabilities.InspectStoredBundleTree(bundleRoot, fingerprint)
	if err != nil {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	rootAfter, rootErr := os.Lstat(root)
	bundlesAfter, bundlesErr := os.Lstat(bundlesRoot)
	activeAfter, activeErr := os.Lstat(activePath)
	targetAfter, readErr := os.Readlink(activePath)
	if rootErr != nil || bundlesErr != nil || activeErr != nil || readErr != nil || targetAfter != target ||
		!os.SameFile(rootInfo, rootAfter) || !os.SameFile(bundlesInfo, bundlesAfter) || !os.SameFile(activeInfo, activeAfter) {
		return activeBundleEvidence{}, errInvalidAssetWorkerConfig
	}
	return activeBundleEvidence{
		Fingerprint: fingerprint, Root: bundleRoot,
		ToolchainAttestationSHA256: storedEvidence.ToolchainAttestationSHA256,
	}, nil
}

func assetWorkerBundleFingerprints(fingerprint string) processing.CapabilityBundleFingerprints {
	result := make(processing.CapabilityBundleFingerprints)
	for _, profile := range capabilityspec.WorkerProfiles() {
		result[profile.Capability] = []string{fingerprint}
	}
	return result
}

func lowerHexAssetWorker(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func newAssetWorkerInstanceID() (string, error) {
	payload := make([]byte, 16)
	if _, err := rand.Read(payload); err != nil {
		return "", err
	}
	return hex.EncodeToString(payload), nil
}
