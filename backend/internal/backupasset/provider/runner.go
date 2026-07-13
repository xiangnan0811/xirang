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
	CommandPurposeProbe CommandPurpose = "probe"
	CommandPurposeList  CommandPurpose = "list"
	CommandPurposeRead  CommandPurpose = "read"
)

const (
	ToolRestic     CommandTool = "restic"
	ToolRclone     CommandTool = "rclone"
	ToolRemoteFind CommandTool = "remote_find"

	OperationResticVersion   CommandOperation = "restic_version"
	OperationResticConfig    CommandOperation = "restic_config"
	OperationResticSnapshots CommandOperation = "restic_snapshots"
	OperationResticList      CommandOperation = "restic_list"
	OperationResticDump      CommandOperation = "restic_dump"
	OperationRcloneVersion   CommandOperation = "rclone_version"
	OperationRcloneFeatures  CommandOperation = "rclone_features"
	OperationRcloneList      CommandOperation = "rclone_list"
	OperationRcloneStat      CommandOperation = "rclone_stat"
	OperationRcloneCat       CommandOperation = "rclone_cat"
	OperationRemoteEnumerate CommandOperation = "remote_enumerate"
)

type CommandInvocation struct {
	Tool           CommandTool          `json:"tool"`
	Operation      CommandOperation     `json:"operation"`
	Purpose        CommandPurpose       `json:"-"`
	Args           []string             `json:"-"`
	SecretStdin    []byte               `json:"-"`
	PrivateLocator string               `json:"-"`
	Runtime        *RemoteCommandAccess `json:"-"`
}

type RemoteCommandAccess struct {
	Node  model.Node               `json:"-"`
	Audit sshutil.DialAuditContext `json:"-"`
}

func (invocation CommandInvocation) Validate() error {
	allowed := map[CommandTool]map[CommandOperation]bool{
		ToolRestic: {
			OperationResticVersion: true, OperationResticConfig: true, OperationResticSnapshots: true,
			OperationResticList: true, OperationResticDump: true,
		},
		ToolRclone: {
			OperationRcloneVersion: true, OperationRcloneFeatures: true, OperationRcloneList: true,
			OperationRcloneStat: true, OperationRcloneCat: true,
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
	if !validReadOnlyInvocation(invocation) {
		return fmt.Errorf("%w: command operands do not match the registered read-only operation", ErrUnsafeInvocation)
	}
	return nil
}

func validReadOnlyInvocation(invocation CommandInvocation) bool {
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
	specification, access, purpose, err := transport.commandSpec(invocation, limits, limits.MaxMetadataBytes)
	if err != nil {
		return CommandOutput{}, err
	}
	if ctx == nil {
		ctx = context.Background()
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
	specification, access, purpose, err := transport.commandSpec(invocation, limits, maxBytes)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := transport.acquire(ctx); err != nil {
		return nil, err
	}
	runner, closer, err := transport.factory(ctx, access, purpose)
	if err != nil {
		transport.release()
		return nil, err
	}
	if runner == nil || closer == nil {
		if closer != nil {
			_ = closer.Close()
		}
		transport.release()
		return nil, fmt.Errorf("%w: remote command runner unavailable", backupasset.ErrProviderUnavailable)
	}
	handle, err := runner.Open(ctx, specification)
	if err != nil {
		_ = closer.Close()
		transport.release()
		return nil, err
	}
	if handle == nil {
		_ = closer.Close()
		transport.release()
		return nil, fmt.Errorf("%w: nil remote command stream", backupasset.ErrProviderUnavailable)
	}
	return &transportReadHandle{underlying: handle, closer: closer, release: transport.release}, nil
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
		if invocation.Operation == OperationRcloneVersion {
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

var _ CommandTransport = (*SSHCommandTransport)(nil)

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
