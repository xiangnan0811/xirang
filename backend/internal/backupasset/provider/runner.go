package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

var ErrUnsafeInvocation = errors.New("unsafe provider command invocation")

type CommandTool string
type CommandOperation string
type CommandPurpose string

const (
	CommandPurposeProbe    CommandPurpose = "probe"
	CommandPurposeList     CommandPurpose = "list"
	CommandPurposeRead     CommandPurpose = "read"
	CommandPurposePublish  CommandPurpose = "publish"
	CommandPurposeManifest CommandPurpose = "manifest"
)

const (
	ToolRestic     CommandTool = "restic"
	ToolRclone     CommandTool = "rclone"
	ToolRemoteFind CommandTool = "remote_find"

	OperationResticVersion              CommandOperation = "restic_version"
	OperationResticConfig               CommandOperation = "restic_config"
	OperationResticSnapshots            CommandOperation = "restic_snapshots"
	OperationResticList                 CommandOperation = "restic_list"
	OperationResticDump                 CommandOperation = "restic_dump"
	OperationResticBackup               CommandOperation = "restic_backup"
	OperationResticSnapshotsByTags      CommandOperation = "restic_snapshots_by_tags"
	OperationResticManifest             CommandOperation = "restic_manifest"
	OperationRcloneVersion              CommandOperation = "rclone_version"
	OperationRcloneFeatures             CommandOperation = "rclone_features"
	OperationRcloneList                 CommandOperation = "rclone_list"
	OperationRcloneStat                 CommandOperation = "rclone_stat"
	OperationRcloneCat                  CommandOperation = "rclone_cat"
	OperationRcloneManagedVersion       CommandOperation = "rclone_managed_version"
	OperationRcloneManagedFeatures      CommandOperation = "rclone_managed_features"
	OperationRcloneManagedRecursiveList CommandOperation = "rclone_managed_recursive_list"
	OperationRcloneManagedCopy          CommandOperation = "rclone_managed_copy"
	OperationRcloneManagedNativeSync    CommandOperation = "rclone_managed_native_sync"
	OperationRcloneManagedCheckDownload CommandOperation = "rclone_managed_check_download"
	OperationRcloneManagedCopyTo        CommandOperation = "rclone_managed_copyto"
	OperationRcloneManagedCat           CommandOperation = "rclone_managed_cat"
	OperationRcloneManagedExactStat     CommandOperation = "rclone_managed_exact_stat"
	OperationRemoteEnumerate            CommandOperation = "remote_enumerate"
)

type RclonePrivateLocator struct {
	value string
}

func NewRclonePrivateLocator(value string) (RclonePrivateLocator, error) {
	if value == "" || len(value) > 16<<10 || strings.TrimSpace(value) != value || strings.HasPrefix(value, "-") ||
		strings.ContainsRune(value, '\x00') || strings.ContainsAny(value, "\r\n`") || strings.Contains(value, "$(") {
		return RclonePrivateLocator{}, fmt.Errorf("%w: invalid private Rclone locator", ErrUnsafeInvocation)
	}
	return RclonePrivateLocator{value: value}, nil
}

func (value RclonePrivateLocator) valid() bool {
	validated, err := NewRclonePrivateLocator(value.value)
	return err == nil && validated.value == value.value
}

type CommandInvocation struct {
	Tool                    CommandTool           `json:"tool"`
	Operation               CommandOperation      `json:"operation"`
	Purpose                 CommandPurpose        `json:"-"`
	Args                    []string              `json:"-"`
	SecretStdin             []byte                `json:"-"`
	PrivateLocator          string                `json:"-"`
	Runtime                 *RemoteCommandAccess  `json:"-"`
	RcloneSource            *RclonePrivateLocator `json:"-"`
	RcloneDestination       *RclonePrivateLocator `json:"-"`
	RcloneStagedSource      *StagedPayloadRef     `json:"-"`
	RcloneStagedDestination *StagedPayloadRef     `json:"-"`
	RcloneCopyDest          *RclonePrivateLocator `json:"-"`
	RcloneLowLevelRetries   int                   `json:"-"`
	AbsoluteDeadline        time.Time             `json:"-"`
}

type RemoteCommandAccess struct {
	Node  model.Node               `json:"-"`
	Audit sshutil.DialAuditContext `json:"-"`
}

func (invocation CommandInvocation) Validate() error {
	allowed := map[CommandTool]map[CommandOperation]bool{
		ToolRestic: {
			OperationResticVersion: true, OperationResticConfig: true, OperationResticSnapshots: true,
			OperationResticList: true, OperationResticDump: true, OperationResticBackup: true,
			OperationResticSnapshotsByTags: true, OperationResticManifest: true,
		},
		ToolRclone: {
			OperationRcloneVersion: true, OperationRcloneFeatures: true, OperationRcloneList: true,
			OperationRcloneStat: true, OperationRcloneCat: true,
			OperationRcloneManagedVersion: true, OperationRcloneManagedFeatures: true,
			OperationRcloneManagedRecursiveList: true, OperationRcloneManagedCopy: true,
			OperationRcloneManagedNativeSync: true, OperationRcloneManagedCheckDownload: true,
			OperationRcloneManagedCopyTo: true, OperationRcloneManagedCat: true, OperationRcloneManagedExactStat: true,
		},
		ToolRemoteFind: {OperationRemoteEnumerate: true},
	}
	operations, ok := allowed[invocation.Tool]
	if !ok || !operations[invocation.Operation] {
		return fmt.Errorf("%w: unregistered tool operation", ErrUnsafeInvocation)
	}
	if len(invocation.Args) > 128 || len(invocation.SecretStdin) > 64<<10 || len(invocation.PrivateLocator) > 16<<10 || strings.ContainsRune(invocation.PrivateLocator, '\x00') {
		return fmt.Errorf("%w: invocation exceeds structural limits", ErrUnsafeInvocation)
	}
	for _, argument := range invocation.Args {
		if strings.ContainsRune(argument, '\x00') || len(argument) > 16<<10 {
			return fmt.Errorf("%w: invalid command operand", ErrUnsafeInvocation)
		}
	}
	if !validCommandInvocation(invocation) {
		return fmt.Errorf("%w: command operands do not match the registered operation", ErrUnsafeInvocation)
	}
	return nil
}

func validCommandInvocation(invocation CommandInvocation) bool {
	if isManagedRcloneOperation(invocation.Operation) {
		return validManagedRcloneInvocation(invocation)
	}
	arguments := invocation.Args
	switch invocation.Tool {
	case ToolRestic:
		switch invocation.Operation {
		case OperationResticVersion:
			return len(invocation.SecretStdin) == 0 && equalArguments(arguments, "version")
		case OperationResticConfig:
			return len(invocation.SecretStdin) > 0 && equalArguments(arguments, "--password-file", "/dev/stdin", "cat", "config")
		case OperationResticSnapshots:
			return len(invocation.SecretStdin) > 0 && equalArguments(arguments, "--password-file", "/dev/stdin", "snapshots", "--json")
		case OperationResticList:
			return len(invocation.SecretStdin) > 0 && len(arguments) == 7 && equalArguments(arguments[:5], "--password-file", "/dev/stdin", "ls", "--json", "--") &&
				lowerHex(arguments[5], 64) && arguments[6] != ""
		case OperationResticDump:
			return len(invocation.SecretStdin) > 0 && len(arguments) == 6 && equalArguments(arguments[:4], "--password-file", "/dev/stdin", "dump", "--") &&
				lowerHex(arguments[4], 64) && arguments[5] != ""
		case OperationResticBackup:
			return invocation.Purpose == CommandPurposePublish && len(invocation.SecretStdin) > 0 && validResticBackupArguments(arguments)
		case OperationResticSnapshotsByTags:
			return invocation.Purpose == CommandPurposeManifest && len(invocation.SecretStdin) > 0 && validResticSnapshotsByTagsArguments(arguments)
		case OperationResticManifest:
			return invocation.Purpose == CommandPurposeManifest && len(invocation.SecretStdin) > 0 && validResticManifestArguments(arguments)
		}
	case ToolRclone:
		if invocation.Operation == OperationRcloneVersion {
			return len(invocation.SecretStdin) == 0 && equalArguments(arguments, "version")
		}
		boundConfig := len(arguments) >= 2 && arguments[0] == "--config" && arguments[1] == "/dev/stdin"
		if boundConfig {
			arguments = arguments[2:]
		}
		if boundConfig != (len(invocation.SecretStdin) > 0) {
			return false
		}
		switch invocation.Operation {
		case OperationRcloneVersion:
			return false
		case OperationRcloneFeatures:
			return equalArguments(arguments, "backend", "features", "--")
		case OperationRcloneList:
			return equalArguments(arguments, "lsjson", "--max-depth", "1", "--")
		case OperationRcloneStat:
			return equalArguments(arguments, "lsjson", "--stat", "--")
		case OperationRcloneCat:
			return validRcloneCatArguments(arguments)
		}
	case ToolRemoteFind:
		return len(invocation.SecretStdin) == 0 && invocation.Operation == OperationRemoteEnumerate && equalArguments(arguments, "-mindepth", "1", "-maxdepth", "1", "-print0")
	}
	return false
}

func isManagedRcloneOperation(operation CommandOperation) bool {
	switch operation {
	case OperationRcloneManagedVersion, OperationRcloneManagedFeatures, OperationRcloneManagedRecursiveList,
		OperationRcloneManagedCopy, OperationRcloneManagedNativeSync, OperationRcloneManagedCheckDownload,
		OperationRcloneManagedCopyTo, OperationRcloneManagedCat, OperationRcloneManagedExactStat:
		return true
	default:
		return false
	}
}

func validManagedRcloneInvocation(invocation CommandInvocation) bool {
	if invocation.Tool != ToolRclone || len(invocation.Args) != 0 || len(invocation.SecretStdin) == 0 || invocation.PrivateLocator != "" ||
		invocation.RcloneLowLevelRetries < 1 || invocation.RcloneLowLevelRetries > 10 || !validTaggedPublicationTime(invocation.AbsoluteDeadline) {
		return false
	}
	privateSource := invocation.RcloneSource != nil
	privateDestination := invocation.RcloneDestination != nil
	stagedSource := invocation.RcloneStagedSource != nil
	stagedDestination := invocation.RcloneStagedDestination != nil
	if (privateSource && !invocation.RcloneSource.valid()) || (privateDestination && !invocation.RcloneDestination.valid()) ||
		(stagedSource && invocation.RcloneStagedSource.validate() != nil) || (stagedDestination && invocation.RcloneStagedDestination.validate() != nil) ||
		(privateSource && stagedSource) || (privateDestination && stagedDestination) {
		return false
	}
	if invocation.RcloneCopyDest != nil {
		if invocation.Operation != OperationRcloneManagedCopy || !invocation.RcloneCopyDest.valid() || !privateDestination ||
			invocation.RcloneCopyDest.value == invocation.RcloneDestination.value ||
			strings.HasPrefix(invocation.RcloneCopyDest.value, invocation.RcloneDestination.value+"/") ||
			strings.HasPrefix(invocation.RcloneDestination.value, invocation.RcloneCopyDest.value+"/") {
			return false
		}
	}
	switch invocation.Operation {
	case OperationRcloneManagedVersion:
		return !privateSource && !privateDestination && !stagedSource && !stagedDestination
	case OperationRcloneManagedFeatures, OperationRcloneManagedRecursiveList, OperationRcloneManagedCat, OperationRcloneManagedExactStat:
		return privateSource && !privateDestination && !stagedSource && !stagedDestination
	case OperationRcloneManagedCopy, OperationRcloneManagedNativeSync, OperationRcloneManagedCheckDownload:
		return privateSource && privateDestination && !stagedSource && !stagedDestination
	case OperationRcloneManagedCopyTo:
		return (privateSource || stagedSource) && (privateDestination || stagedDestination)
	default:
		return false
	}
}

func managedRcloneArguments(invocation CommandInvocation) ([]string, error) {
	if !validManagedRcloneInvocation(invocation) {
		return nil, fmt.Errorf("%w: invalid managed Rclone invocation", ErrUnsafeInvocation)
	}
	arguments := []string{
		"--config", "/dev/stdin", "--retries", "1", "--low-level-retries", strconv.Itoa(invocation.RcloneLowLevelRetries), "--links",
	}
	source := func() string {
		if invocation.RcloneSource != nil {
			return invocation.RcloneSource.value
		}
		return invocation.RcloneStagedSource.path
	}
	destination := func() string {
		if invocation.RcloneDestination != nil {
			return invocation.RcloneDestination.value
		}
		return invocation.RcloneStagedDestination.path
	}
	switch invocation.Operation {
	case OperationRcloneManagedVersion:
		arguments = append(arguments, "version")
	case OperationRcloneManagedFeatures:
		arguments = append(arguments, "backend", "features", "--", source())
	case OperationRcloneManagedRecursiveList:
		arguments = append(arguments, "lsjson", "--recursive", "--hash", "--metadata", "--", source())
	case OperationRcloneManagedCopy:
		arguments = append(arguments, "copy", "--create-empty-src-dirs")
		if invocation.RcloneCopyDest != nil {
			arguments = append(arguments, "--copy-dest", invocation.RcloneCopyDest.value)
		}
		arguments = append(arguments, "--", source(), destination())
	case OperationRcloneManagedNativeSync:
		arguments = append(arguments, "sync", "--create-empty-src-dirs", "--", source(), destination())
	case OperationRcloneManagedCheckDownload:
		arguments = append(arguments, "check", "--download", "--one-way", "--", source(), destination())
	case OperationRcloneManagedCopyTo:
		arguments = append(arguments, "copyto", "--", source(), destination())
	case OperationRcloneManagedCat:
		arguments = append(arguments, "cat", "--", source())
	case OperationRcloneManagedExactStat:
		arguments = append(arguments, "lsjson", "--stat", "--hash", "--metadata", "--", source())
	default:
		return nil, fmt.Errorf("%w: unsupported managed Rclone operation", ErrUnsafeInvocation)
	}
	return arguments, nil
}

func validRcloneCatArguments(arguments []string) bool {
	if equalArguments(arguments, "cat", "--") {
		return true
	}
	if len(arguments) == 4 && equalArguments(arguments[:2], "cat", "--count") && arguments[3] == "--" {
		return canonicalInteger(arguments[2], false)
	}
	return len(arguments) == 6 && equalArguments(arguments[:2], "cat", "--offset") &&
		canonicalInteger(arguments[2], true) && arguments[3] == "--count" &&
		canonicalInteger(arguments[4], false) && arguments[5] == "--"
}

func validResticBackupArguments(arguments []string) bool {
	if len(arguments) < 10 || !equalArguments(arguments[:4], "--password-file", "/dev/stdin", "backup", "--json") {
		return false
	}
	index := 4
	for tag := 0; tag < 2; tag++ {
		if index+1 >= len(arguments) || arguments[index] != "--tag" || !validGeneratedResticTag(arguments[index+1], tag) {
			return false
		}
		index += 2
	}
	for index+2 < len(arguments) && arguments[index] == "--exclude" {
		if !validResticExclude(arguments[index+1]) {
			return false
		}
		index += 2
	}
	return index+2 == len(arguments) && arguments[index] == "--" && validResticAbsoluteSource(arguments[index+1])
}

func validResticSnapshotsByTagsArguments(arguments []string) bool {
	if len(arguments) != 6 || !equalArguments(arguments[:5], "--password-file", "/dev/stdin", "snapshots", "--json", "--tag") {
		return false
	}
	tags := strings.Split(arguments[5], ",")
	return len(tags) == 2 && validGeneratedResticTag(tags[0], 0) && validGeneratedResticTag(tags[1], 1)
}

func validResticManifestArguments(arguments []string) bool {
	return len(arguments) == 8 && equalArguments(arguments[:6], "--password-file", "/dev/stdin", "ls", "--json", "--recursive", "--") &&
		lowerHex(arguments[6], 64) && arguments[7] == "/"
}

func validGeneratedResticTag(value string, index int) bool {
	prefix := "xirang.link.v1."
	if index == 1 {
		prefix = "xirang.point.v1."
	}
	return !strings.ContainsRune(value, ',') && len(value) == len(prefix)+32 && strings.HasPrefix(value, prefix) &&
		lowerHex(strings.TrimPrefix(value, prefix), 32)
}

func validResticExclude(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 4096 && !strings.ContainsRune(value, '\x00')
}

func validResticAbsoluteSource(value string) bool {
	return value != "" && path.IsAbs(value) && path.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func canonicalInteger(value string, allowZero bool) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || (!allowZero && parsed == 0) {
		return false
	}
	return strconv.FormatInt(parsed, 10) == value
}

func equalArguments(actual []string, expected ...string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

type CommandOutput struct {
	Stdout []byte
	Stderr []byte
}

type CommandTransport interface {
	Run(context.Context, CommandInvocation, OperationLimits) (CommandOutput, error)
	Open(context.Context, CommandInvocation, OperationLimits, int64) (ReadHandle, error)
}

type ToolBinaries struct {
	Restic     string
	Rclone     string
	RemoteFind string
}

type remoteCommandRunner interface {
	Run(context.Context, sshutil.CommandSpec) (sshutil.CommandResult, error)
	Open(context.Context, sshutil.CommandSpec) (sshutil.CommandReadHandle, error)
}

type remoteCommandExecutionRunner interface {
	OpenExecution(context.Context, sshutil.CommandSpec) (sshutil.CommandExecutionStream, error)
}

type remoteCommandRunnerFactory func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error)

type ConcurrencyLimitSource func() (int, error)

const maximumProviderConcurrency = 32

type SSHCommandTransport struct {
	factory  remoteCommandRunnerFactory
	gate     *concurrencyGate
	binaries ToolBinaries
}

type concurrencyGate struct {
	source  ConcurrencyLimitSource
	mu      sync.Mutex
	active  int
	changed chan struct{}
}

func newConcurrencyGate(source ConcurrencyLimitSource) (*concurrencyGate, error) {
	if _, err := resolveConcurrencyLimit(source); err != nil {
		return nil, err
	}
	return &concurrencyGate{source: source, changed: make(chan struct{})}, nil
}

func resolveConcurrencyLimit(source ConcurrencyLimitSource) (int, error) {
	if source == nil {
		return 0, fmt.Errorf("%w: provider concurrency source unavailable", backupasset.ErrInvalidState)
	}
	limit, err := source()
	if err != nil {
		return 0, err
	}
	if limit <= 0 || limit > maximumProviderConcurrency {
		return 0, fmt.Errorf("%w: invalid provider concurrency limit", backupasset.ErrInvalidState)
	}
	return limit, nil
}

func (gate *concurrencyGate) acquire(ctx context.Context) error {
	if gate == nil {
		return fmt.Errorf("%w: provider concurrency gate unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		limit, err := resolveConcurrencyLimit(gate.source)
		if err != nil {
			return err
		}
		gate.mu.Lock()
		if gate.active < limit {
			gate.active++
			gate.mu.Unlock()
			return nil
		}
		changed := gate.changed
		gate.mu.Unlock()

		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-changed:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func (gate *concurrencyGate) release() {
	if gate == nil {
		return
	}
	gate.mu.Lock()
	if gate.active > 0 {
		gate.active--
	}
	close(gate.changed)
	gate.changed = make(chan struct{})
	gate.mu.Unlock()
}

func NewSSHCommandTransport(dialer *sshutil.NodeDialer, maxConcurrency int, binaries ToolBinaries) (*SSHCommandTransport, error) {
	return NewSSHCommandTransportWithConcurrencySource(dialer, func() (int, error) { return maxConcurrency, nil }, binaries)
}

func NewSSHCommandTransportWithConcurrencySource(dialer *sshutil.NodeDialer, source ConcurrencyLimitSource, binaries ToolBinaries) (*SSHCommandTransport, error) {
	if dialer == nil {
		return nil, fmt.Errorf("%w: repository Node dialer unavailable", backupasset.ErrInvalidState)
	}
	return newSSHCommandTransportWithConcurrencySource(func(ctx context.Context, access RemoteCommandAccess, purpose string) (remoteCommandRunner, io.Closer, error) {
		client, err := dialer.Dial(ctx, access.Node, purpose, access.Audit)
		if err != nil {
			return nil, nil, err
		}
		return sshutil.NewSSHCommandRunner(client, 1), client, nil
	}, source, binaries)
}

func newSSHCommandTransport(factory remoteCommandRunnerFactory, maxConcurrency int, binaries ToolBinaries) (*SSHCommandTransport, error) {
	return newSSHCommandTransportWithConcurrencySource(factory, func() (int, error) { return maxConcurrency, nil }, binaries)
}

func newSSHCommandTransportWithConcurrencySource(factory remoteCommandRunnerFactory, source ConcurrencyLimitSource, binaries ToolBinaries) (*SSHCommandTransport, error) {
	if factory == nil {
		return nil, fmt.Errorf("%w: invalid SSH command transport dependencies", backupasset.ErrInvalidState)
	}
	gate, err := newConcurrencyGate(source)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(binaries.Restic) == "" {
		binaries.Restic = "restic"
	}
	if strings.TrimSpace(binaries.Rclone) == "" {
		binaries.Rclone = "rclone"
	}
	if strings.TrimSpace(binaries.RemoteFind) == "" {
		binaries.RemoteFind = "find"
	}
	return &SSHCommandTransport{factory: factory, gate: gate, binaries: binaries}, nil
}

func (transport *SSHCommandTransport) Run(ctx context.Context, invocation CommandInvocation, limits OperationLimits) (CommandOutput, error) {
	ctx, cancel, err := commandInvocationContext(ctx, invocation)
	if err != nil {
		return CommandOutput{}, err
	}
	defer cancel()
	releaseStaged, err := pinRcloneStagedPayloads(invocation)
	if err != nil {
		return CommandOutput{}, err
	}
	defer releaseStaged()
	specification, access, purpose, err := transport.commandSpec(invocation, limits, limits.MaxMetadataBytes)
	if err != nil {
		return CommandOutput{}, err
	}
	if err := transport.acquire(ctx); err != nil {
		return CommandOutput{}, err
	}
	defer transport.release()
	runner, closer, err := transport.factory(ctx, access, purpose)
	if err != nil {
		return CommandOutput{}, err
	}
	if runner == nil || closer == nil {
		if closer != nil {
			_ = closer.Close()
		}
		return CommandOutput{}, fmt.Errorf("%w: remote command runner unavailable", backupasset.ErrProviderUnavailable)
	}
	result, runErr := runner.Run(ctx, specification)
	closeErr := closer.Close()
	if runErr != nil {
		return CommandOutput{}, runErr
	}
	if closeErr != nil {
		return CommandOutput{}, fmt.Errorf("%w: close remote command connection", backupasset.ErrProviderUnavailable)
	}
	return CommandOutput{Stdout: result.Stdout, Stderr: result.Stderr}, nil
}

func (transport *SSHCommandTransport) Open(ctx context.Context, invocation CommandInvocation, limits OperationLimits, maxBytes int64) (ReadHandle, error) {
	ctx, cancel, err := commandInvocationContext(ctx, invocation)
	if err != nil {
		return nil, err
	}
	releaseStaged, err := pinRcloneStagedPayloads(invocation)
	if err != nil {
		cancel()
		return nil, err
	}
	specification, access, purpose, err := transport.commandSpec(invocation, limits, maxBytes)
	if err != nil {
		releaseStaged()
		cancel()
		return nil, err
	}
	if err := transport.acquire(ctx); err != nil {
		releaseStaged()
		cancel()
		return nil, err
	}
	runner, closer, err := transport.factory(ctx, access, purpose)
	if err != nil {
		transport.release()
		releaseStaged()
		cancel()
		return nil, err
	}
	if runner == nil || closer == nil {
		if closer != nil {
			_ = closer.Close()
		}
		transport.release()
		releaseStaged()
		cancel()
		return nil, fmt.Errorf("%w: remote command runner unavailable", backupasset.ErrProviderUnavailable)
	}
	handle, err := runner.Open(ctx, specification)
	if err != nil {
		_ = closer.Close()
		transport.release()
		releaseStaged()
		cancel()
		return nil, err
	}
	if handle == nil {
		_ = closer.Close()
		transport.release()
		releaseStaged()
		cancel()
		return nil, fmt.Errorf("%w: nil remote command stream", backupasset.ErrProviderUnavailable)
	}
	return &transportReadHandle{underlying: handle, closer: closer, release: func() { transport.release(); releaseStaged(); cancel() }}, nil
}

func (transport *SSHCommandTransport) OpenExecution(ctx context.Context, invocation CommandInvocation, limits OperationLimits, maxBytes int64) (CommandExecution, error) {
	ctx, cancel, err := commandInvocationContext(ctx, invocation)
	if err != nil {
		return nil, err
	}
	releaseStaged, err := pinRcloneStagedPayloads(invocation)
	if err != nil {
		cancel()
		return nil, err
	}
	specification, access, purpose, err := transport.commandSpec(invocation, limits, maxBytes)
	if err != nil {
		releaseStaged()
		cancel()
		return nil, err
	}
	if err := transport.acquire(ctx); err != nil {
		releaseStaged()
		cancel()
		return nil, err
	}
	runner, closer, err := transport.factory(ctx, access, purpose)
	if err != nil {
		transport.release()
		releaseStaged()
		cancel()
		return nil, err
	}
	if runner == nil || closer == nil {
		if closer != nil {
			_ = closer.Close()
		}
		transport.release()
		releaseStaged()
		cancel()
		return nil, fmt.Errorf("%w: remote command runner unavailable", backupasset.ErrProviderUnavailable)
	}
	executionRunner, ok := runner.(remoteCommandExecutionRunner)
	if !ok {
		_ = closer.Close()
		transport.release()
		releaseStaged()
		cancel()
		return nil, fmt.Errorf("%w: remote command execution runner unavailable", backupasset.ErrProviderUnavailable)
	}
	execution, err := executionRunner.OpenExecution(ctx, specification)
	if err != nil {
		_ = closer.Close()
		transport.release()
		releaseStaged()
		cancel()
		return nil, err
	}
	if execution == nil {
		_ = closer.Close()
		transport.release()
		releaseStaged()
		cancel()
		return nil, fmt.Errorf("%w: nil remote command execution", backupasset.ErrProviderUnavailable)
	}
	return &transportExecution{underlying: execution, closer: closer, release: func() { transport.release(); releaseStaged(); cancel() }}, nil
}

func commandInvocationContext(ctx context.Context, invocation CommandInvocation) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !isManagedRcloneOperation(invocation.Operation) {
		return ctx, func() {}, nil
	}
	if !validTaggedPublicationTime(invocation.AbsoluteDeadline) || time.Until(invocation.AbsoluteDeadline) <= 0 {
		return nil, nil, fmt.Errorf("%w: managed Rclone deadline expired", ErrUnsafeInvocation)
	}
	bounded, cancel := context.WithDeadline(ctx, invocation.AbsoluteDeadline)
	return bounded, cancel, nil
}

func pinRcloneStagedPayloads(invocation CommandInvocation) (func(), error) {
	refs := []*StagedPayloadRef{invocation.RcloneStagedSource, invocation.RcloneStagedDestination}
	releases := make([]func(), 0, len(refs))
	for _, ref := range refs {
		if ref == nil {
			continue
		}
		release, err := ref.acquire()
		if err != nil {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
		})
	}, nil
}

func (transport *SSHCommandTransport) commandSpec(invocation CommandInvocation, limits OperationLimits, maxStdoutBytes int64) (sshutil.CommandSpec, RemoteCommandAccess, string, error) {
	if transport == nil || transport.factory == nil || transport.gate == nil || invocation.Validate() != nil || limits.Validate() != nil || maxStdoutBytes <= 0 || invocation.Runtime == nil || invocation.Runtime.Node.ID == 0 {
		return sshutil.CommandSpec{}, RemoteCommandAccess{}, "", fmt.Errorf("%w: incomplete remote invocation", ErrUnsafeInvocation)
	}
	purpose, ok := sshPurpose(invocation.Purpose)
	if !ok {
		return sshutil.CommandSpec{}, RemoteCommandAccess{}, "", fmt.Errorf("%w: remote purpose missing", ErrUnsafeInvocation)
	}
	arguments := append([]string(nil), invocation.Args...)
	binary := ""
	secret := invocation.SecretStdin
	switch invocation.Tool {
	case ToolRestic:
		binary = transport.binaries.Restic
		if invocation.Operation == OperationResticVersion {
			secret = nil
		} else {
			if invocation.PrivateLocator == "" {
				return sshutil.CommandSpec{}, RemoteCommandAccess{}, "", fmt.Errorf("%w: Restic repository locator missing", ErrUnsafeInvocation)
			}
			insertAt := 0
			if len(arguments) >= 2 && arguments[0] == "--password-file" {
				insertAt = 2
			}
			withRepository := make([]string, 0, len(arguments)+2)
			withRepository = append(withRepository, arguments[:insertAt]...)
			withRepository = append(withRepository, "-r", invocation.PrivateLocator)
			withRepository = append(withRepository, arguments[insertAt:]...)
			arguments = withRepository
		}
	case ToolRclone:
		binary = transport.binaries.Rclone
		if isManagedRcloneOperation(invocation.Operation) {
			managedArguments, managedErr := managedRcloneArguments(invocation)
			if managedErr != nil {
				return sshutil.CommandSpec{}, RemoteCommandAccess{}, "", managedErr
			}
			arguments = managedArguments
			remaining := time.Until(invocation.AbsoluteDeadline)
			if remaining <= 0 {
				return sshutil.CommandSpec{}, RemoteCommandAccess{}, "", fmt.Errorf("%w: managed Rclone deadline expired", ErrUnsafeInvocation)
			}
			if remaining < limits.Timeout {
				limits.Timeout = remaining
			}
		} else if invocation.Operation == OperationRcloneVersion {
			secret = nil
		} else {
			if invocation.PrivateLocator == "" {
				return sshutil.CommandSpec{}, RemoteCommandAccess{}, "", fmt.Errorf("%w: Rclone locator missing", ErrUnsafeInvocation)
			}
			arguments = append(arguments, invocation.PrivateLocator)
		}
	case ToolRemoteFind:
		binary = transport.binaries.RemoteFind
		if invocation.PrivateLocator == "" || !strings.HasPrefix(invocation.PrivateLocator, "/") || path.Clean(invocation.PrivateLocator) != invocation.PrivateLocator {
			return sshutil.CommandSpec{}, RemoteCommandAccess{}, "", fmt.Errorf("%w: remote directory locator missing", ErrUnsafeInvocation)
		}
		arguments = append([]string{invocation.PrivateLocator}, arguments...)
	default:
		return sshutil.CommandSpec{}, RemoteCommandAccess{}, "", fmt.Errorf("%w: remote tool unavailable", ErrUnsafeInvocation)
	}
	specification := sshutil.CommandSpec{
		Binary: binary, Args: arguments, Timeout: limits.Timeout, MaxStdoutBytes: maxStdoutBytes,
		MaxStderrBytes: limits.MaxStderrBytes, MaxRecordBytes: limits.MaxRecordBytes,
	}
	if len(secret) > 0 {
		specification.SecretStdin = &sshutil.SecretStdin{Value: append([]byte(nil), secret...), AppendNewline: true}
	}
	return specification, *invocation.Runtime, purpose, nil
}

func sshPurpose(purpose CommandPurpose) (string, bool) {
	switch purpose {
	case CommandPurposeProbe:
		return sshutil.PurposeRepositoryProbe, true
	case CommandPurposeList:
		return sshutil.PurposeRepositoryList, true
	case CommandPurposeRead:
		return sshutil.PurposeRepositoryRead, true
	case CommandPurposePublish:
		return sshutil.PurposeTaskBackup, true
	case CommandPurposeManifest:
		return sshutil.PurposeRepositoryList, true
	default:
		return "", false
	}
}

func (transport *SSHCommandTransport) acquire(ctx context.Context) error {
	return transport.gate.acquire(ctx)
}

func (transport *SSHCommandTransport) release() { transport.gate.release() }

type transportReadHandle struct {
	underlying sshutil.CommandReadHandle
	closer     io.Closer
	release    func()
	once       sync.Once
	err        error
}

func (handle *transportReadHandle) Read(buffer []byte) (int, error) {
	return handle.underlying.Read(buffer)
}

func (handle *transportReadHandle) Close() error {
	handle.once.Do(func() {
		streamErr := handle.underlying.Close()
		closeErr := handle.closer.Close()
		if streamErr != nil {
			handle.err = streamErr
		} else if closeErr != nil {
			handle.err = fmt.Errorf("%w: close remote command connection", backupasset.ErrProviderUnavailable)
		}
		handle.release()
	})
	return handle.err
}

type transportExecution struct {
	underlying sshutil.CommandExecutionStream
	closer     io.Closer
	release    func()

	once       sync.Once
	completion CommandCompletion
	err        error
}

func (execution *transportExecution) Read(buffer []byte) (int, error) {
	return execution.underlying.Read(buffer)
}

func (execution *transportExecution) Join() (CommandCompletion, error) {
	execution.finish(false)
	return copyProviderCommandCompletion(execution.completion), execution.err
}

func (execution *transportExecution) Cancel() error {
	execution.finish(true)
	return execution.err
}

func (execution *transportExecution) finish(cancel bool) {
	execution.once.Do(func() {
		var completion sshutil.CommandCompletion
		if cancel {
			execution.err = execution.underlying.Cancel()
		} else {
			completion, execution.err = execution.underlying.Join()
			execution.completion = CommandCompletion{
				ExitCode:        completion.ExitCode,
				ExitCodeKnown:   completion.ExitCodeKnown,
				Stderr:          append([]byte(nil), completion.Stderr...),
				StderrTruncated: completion.StderrTruncated,
			}
		}
		if closeErr := execution.closer.Close(); execution.err == nil && closeErr != nil {
			execution.err = fmt.Errorf("%w: close remote command connection", backupasset.ErrProviderUnavailable)
		}
		execution.release()
	})
}

func copyProviderCommandCompletion(value CommandCompletion) CommandCompletion {
	value.Stderr = append([]byte(nil), value.Stderr...)
	return value
}

var _ CommandTransport = (*SSHCommandTransport)(nil)
var _ CommandStreamTransport = (*SSHCommandTransport)(nil)

func mapCommandTransportError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	var capabilityErr *CapabilityError
	if errors.As(err, &capabilityErr) {
		return capabilityErr
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, sshutil.ErrCommandTimeout) {
		return newCapabilityError(backupasset.CapabilityProviderOperationTimeout)
	}
	if errors.Is(err, sshutil.ErrCommandOutputLimit) {
		return newCapabilityError(backupasset.CapabilityProviderResourceLimit)
	}
	return newCapabilityError(backupasset.CapabilityProviderUnavailable)
}
