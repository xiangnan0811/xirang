package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

type fakeCommandTransport struct {
	runResult CommandOutput
	runErr    error
	outputs   map[CommandOperation]CommandOutput
	errors    map[CommandOperation]error
	requests  []CommandInvocation
	open      ReadHandle
	openErr   error
	runFunc   func(CommandInvocation) (CommandOutput, error)
	openFunc  func(CommandInvocation, int64) (ReadHandle, error)
}

func (transport *fakeCommandTransport) Run(_ context.Context, invocation CommandInvocation, _ OperationLimits) (CommandOutput, error) {
	transport.requests = append(transport.requests, invocation)
	if transport.runFunc != nil {
		return transport.runFunc(invocation)
	}
	if transport.errors != nil && transport.errors[invocation.Operation] != nil {
		return CommandOutput{}, transport.errors[invocation.Operation]
	}
	if transport.outputs != nil {
		if output, ok := transport.outputs[invocation.Operation]; ok {
			return output, nil
		}
	}
	return transport.runResult, transport.runErr
}

func (transport *fakeCommandTransport) Open(_ context.Context, invocation CommandInvocation, _ OperationLimits, maxBytes int64) (ReadHandle, error) {
	transport.requests = append(transport.requests, invocation)
	if transport.openFunc != nil {
		return transport.openFunc(invocation, maxBytes)
	}
	return transport.open, transport.openErr
}

func TestFakeCommandTransportPreservesOpenByteLimit(t *testing.T) {
	var received int64
	transport := &fakeCommandTransport{openFunc: func(_ CommandInvocation, maxBytes int64) (ReadHandle, error) {
		received = maxBytes
		return nil, nil
	}}
	if _, err := transport.Open(context.Background(), CommandInvocation{}, OperationLimits{}, 17); err != nil {
		t.Fatal(err)
	}
	if received != 17 {
		t.Fatalf("Open maxBytes=%d want=17", received)
	}
}

func TestBoundedReadHandleCloseDetectsUnreadOverflowAtExactLimit(t *testing.T) {
	underlying := &trackingProviderReadHandle{Reader: strings.NewReader("12345")}
	handle := newBoundedReadHandle(underlying, 4)
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(handle, buffer); err != nil || string(buffer) != "1234" {
		t.Fatalf("read value=%q err=%v", buffer, err)
	}
	if err := handle.Close(); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("overflow close error=%v", err)
	}
}

func TestRunnerValidatesReadOnlyToolOperationPairs(t *testing.T) {
	snapshotID := strings.Repeat("a", 64)
	resticSecret := []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY")
	valid := []CommandInvocation{
		{Tool: ToolRestic, Operation: OperationResticVersion, Args: []string{"version"}},
		{Tool: ToolRestic, Operation: OperationResticConfig, Args: []string{"--password-file", "/dev/stdin", "cat", "config"}, SecretStdin: resticSecret},
		{Tool: ToolRestic, Operation: OperationResticSnapshots, Args: []string{"--password-file", "/dev/stdin", "snapshots", "--json"}, SecretStdin: resticSecret},
		{Tool: ToolRestic, Operation: OperationResticList, Args: []string{"--password-file", "/dev/stdin", "ls", "--json", "--", snapshotID, "/"}, SecretStdin: resticSecret},
		{Tool: ToolRestic, Operation: OperationResticDump, Args: []string{"--password-file", "/dev/stdin", "dump", "--", snapshotID, "/file"}, SecretStdin: resticSecret},
		{Tool: ToolRclone, Operation: OperationRcloneVersion, Args: []string{"version"}},
		{Tool: ToolRclone, Operation: OperationRcloneFeatures, Args: []string{"backend", "features", "--"}},
		{Tool: ToolRclone, Operation: OperationRcloneList, Args: []string{"lsjson", "--max-depth", "1", "--"}},
		{Tool: ToolRclone, Operation: OperationRcloneStat, Args: []string{"lsjson", "--stat", "--"}},
		{Tool: ToolRclone, Operation: OperationRcloneCat, Args: []string{"cat", "--"}},
		{Tool: ToolRclone, Operation: OperationRcloneCat, Args: []string{"cat", "--count", "16", "--"}},
		{Tool: ToolRclone, Operation: OperationRcloneCat, Args: []string{"cat", "--offset", "0", "--count", "16", "--"}},
		{Tool: ToolRemoteFind, Operation: OperationRemoteEnumerate, Args: []string{"-mindepth", "1", "-maxdepth", "1", "-print0"}},
	}
	for _, invocation := range valid {
		if err := invocation.Validate(); err != nil {
			t.Fatalf("valid invocation %+v rejected: %v", invocation, err)
		}
	}
	invalid := []CommandInvocation{
		{},
		{Tool: ToolRestic, Operation: OperationRcloneList},
		{Tool: CommandTool("shell"), Operation: OperationResticList},
		{Tool: ToolRestic, Operation: CommandOperation("backup")},
		{Tool: ToolRclone, Operation: CommandOperation("sync")},
		{Tool: ToolRestic, Operation: OperationResticList, Args: []string{"nul\x00arg"}},
	}
	for _, invocation := range invalid {
		if err := invocation.Validate(); !errors.Is(err, ErrUnsafeInvocation) {
			t.Fatalf("invalid invocation %+v error=%v", invocation, err)
		}
	}
}

func TestRunnerRejectsMutationArgumentsBehindReadOnlyOperationLabels(t *testing.T) {
	tests := []CommandInvocation{
		{Tool: ToolRestic, Operation: OperationResticList, Args: []string{"forget"}},
		{Tool: ToolRestic, Operation: OperationResticDump, Args: []string{"prune"}},
		{Tool: ToolRclone, Operation: OperationRcloneList, Args: []string{"sync"}},
		{Tool: ToolRclone, Operation: OperationRcloneFeatures, Args: []string{"backend", "cleanup"}},
		{Tool: ToolRemoteFind, Operation: OperationRemoteEnumerate, Args: []string{"-delete"}},
	}
	for _, invocation := range tests {
		if err := invocation.Validate(); !errors.Is(err, ErrUnsafeInvocation) {
			t.Fatalf("mutation-shaped invocation %+v error=%v", invocation, err)
		}
	}
}

func TestRunnerInvocationJSONExcludesSecretsAndLocators(t *testing.T) {
	invocation := CommandInvocation{
		Tool: ToolRestic, Operation: OperationResticConfig,
		Args:           []string{"cat", "config"},
		SecretStdin:    []byte("FAKE_PASSWORD_FOR_TEST_ONLY"),
		PrivateLocator: "FAKE_REPOSITORY_LOCATOR_FOR_TEST_ONLY",
	}
	payload, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"FAKE_PASSWORD_FOR_TEST_ONLY", "FAKE_REPOSITORY_LOCATOR_FOR_TEST_ONLY"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("invocation JSON leaked %q: %s", forbidden, payload)
		}
	}
}

type fakeRemoteCommandRunner struct {
	specs        []sshutil.CommandSpec
	openMaxBytes int64
	result       sshutil.CommandResult
	handle       sshutil.CommandReadHandle
}

func (runner *fakeRemoteCommandRunner) Run(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandResult, error) {
	runner.specs = append(runner.specs, spec)
	return runner.result, nil
}

func (runner *fakeRemoteCommandRunner) Open(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandReadHandle, error) {
	runner.specs = append(runner.specs, spec)
	runner.openMaxBytes = spec.MaxStdoutBytes
	return runner.handle, nil
}

type trackingCloser struct{ closed bool }

func (closer *trackingCloser) Close() error { closer.closed = true; return nil }

func TestSSHCommandTransportMapsToolsPurposesAndPrivateOperands(t *testing.T) {
	runner := &fakeRemoteCommandRunner{result: sshutil.CommandResult{Stdout: []byte("ok")}}
	closer := &trackingCloser{}
	var gotPurpose string
	transport, err := newSSHCommandTransport(func(_ context.Context, access RemoteCommandAccess, purpose string) (remoteCommandRunner, io.Closer, error) {
		if access.Node.ID != 9 {
			t.Fatalf("runtime node=%d", access.Node.ID)
		}
		gotPurpose = purpose
		return runner, closer, nil
	}, 2, ToolBinaries{Restic: "restic", Rclone: "rclone", RemoteFind: "find"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := CommandInvocation{
		Tool: ToolRestic, Operation: OperationResticConfig, Purpose: CommandPurposeProbe,
		Args: []string{"--password-file", "/dev/stdin", "cat", "config"}, SecretStdin: []byte("FAKE_PASSWORD_FOR_TEST_ONLY"),
		PrivateLocator: "FAKE_REPOSITORY_LOCATOR_FOR_TEST_ONLY", Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}},
	}
	output, err := transport.Run(context.Background(), invocation, testOperationLimits())
	if err != nil || string(output.Stdout) != "ok" || gotPurpose != sshutil.PurposeRepositoryProbe || !closer.closed {
		t.Fatalf("output=%+v purpose=%q closed=%v err=%v", output, gotPurpose, closer.closed, err)
	}
	if len(runner.specs) != 1 {
		t.Fatalf("specs=%+v", runner.specs)
	}
	spec := runner.specs[0]
	joined := strings.Join(spec.Args, " ")
	if spec.Binary != "restic" || !strings.Contains(joined, "-r FAKE_REPOSITORY_LOCATOR_FOR_TEST_ONLY") || !strings.Contains(joined, "cat config") || spec.SecretStdin == nil || string(spec.SecretStdin.Value) != "FAKE_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("Restic spec=%+v", spec)
	}
	if strings.Contains(spec.Binary, "FAKE_") {
		t.Fatalf("private locator entered binary: %+v", spec)
	}
}

func TestSSHCommandTransportKeepsReadConnectionUntilHandleClose(t *testing.T) {
	underlying := &trackingProviderReadHandle{Reader: strings.NewReader("data")}
	runner := &fakeRemoteCommandRunner{handle: underlying}
	closer := &trackingCloser{}
	var gotPurpose string
	transport, err := newSSHCommandTransport(func(_ context.Context, _ RemoteCommandAccess, purpose string) (remoteCommandRunner, io.Closer, error) {
		gotPurpose = purpose
		return runner, closer, nil
	}, 1, ToolBinaries{Restic: "restic", Rclone: "rclone", RemoteFind: "find"})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := transport.Open(context.Background(), CommandInvocation{
		Tool: ToolRclone, Operation: OperationRcloneCat, Purpose: CommandPurposeRead,
		Args: []string{"cat", "--"}, PrivateLocator: "remote-name:root/file",
		Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}},
	}, testOperationLimits(), 4)
	if err != nil || gotPurpose != sshutil.PurposeRepositoryRead || runner.openMaxBytes != 4 || closer.closed {
		t.Fatalf("purpose=%q max=%d closed=%v err=%v", gotPurpose, runner.openMaxBytes, closer.closed, err)
	}
	value, readErr := io.ReadAll(handle)
	if closeErr := handle.Close(); readErr != nil || closeErr != nil || string(value) != "data" || !underlying.closed || !closer.closed {
		t.Fatalf("value=%q read=%v close=%v underlying=%v connection=%v", value, readErr, closeErr, underlying.closed, closer.closed)
	}
}

func TestSSHCommandTransportRefreshesDynamicConcurrencyLimit(t *testing.T) {
	var limit atomic.Int64
	limit.Store(1)
	transport, err := newSSHCommandTransportWithConcurrencySource(func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error) {
		return &fakeRemoteCommandRunner{handle: &trackingProviderReadHandle{Reader: strings.NewReader("data")}}, &trackingCloser{}, nil
	}, func() (int, error) {
		return int(limit.Load()), nil
	}, ToolBinaries{Rclone: "rclone"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := CommandInvocation{
		Tool: ToolRclone, Operation: OperationRcloneCat, Purpose: CommandPurposeRead,
		Args: []string{"cat", "--"}, PrivateLocator: "remote-name:root/file", Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}},
	}
	first, err := transport.Open(context.Background(), invocation, testOperationLimits(), 4)
	if err != nil {
		t.Fatal(err)
	}
	type openResult struct {
		handle ReadHandle
		err    error
	}
	secondResult := make(chan openResult, 1)
	go func() {
		handle, openErr := transport.Open(context.Background(), invocation, testOperationLimits(), 4)
		secondResult <- openResult{handle: handle, err: openErr}
	}()
	select {
	case result := <-secondResult:
		if result.handle != nil {
			_ = result.handle.Close()
		}
		t.Fatalf("second command bypassed concurrency limit: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}
	limit.Store(2)
	var second ReadHandle
	select {
	case result := <-secondResult:
		if result.err != nil {
			t.Fatalf("dynamic concurrency increase failed: %v", result.err)
		}
		second = result.handle
	case <-time.After(time.Second):
		t.Fatal("dynamic concurrency increase did not wake a waiting command")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHCommandTransportRejectsMissingRuntimeAndPurpose(t *testing.T) {
	transport, err := newSSHCommandTransport(func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error) {
		return &fakeRemoteCommandRunner{}, &trackingCloser{}, nil
	}, 1, ToolBinaries{Restic: "restic", Rclone: "rclone", RemoteFind: "find"})
	if err != nil {
		t.Fatal(err)
	}
	limits := OperationLimits{Timeout: time.Second, MaxMetadataBytes: 1024, MaxStderrBytes: 1024, MaxRecordBytes: 512, MaxItems: 10}
	for _, invocation := range []CommandInvocation{
		{Tool: ToolRestic, Operation: OperationResticVersion, Purpose: CommandPurposeProbe},
		{Tool: ToolRestic, Operation: OperationResticVersion, Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}}},
	} {
		if _, err := transport.Run(context.Background(), invocation, limits); !errors.Is(err, ErrUnsafeInvocation) {
			t.Fatalf("unsafe invocation=%+v error=%v", invocation, err)
		}
	}
}

func TestSSHCommandTransportPlacesRemoteFindRootBeforeFixedExpression(t *testing.T) {
	transport, err := newSSHCommandTransport(func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error) {
		return &fakeRemoteCommandRunner{}, &trackingCloser{}, nil
	}, 1, ToolBinaries{RemoteFind: "find"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := CommandInvocation{
		Tool: ToolRemoteFind, Operation: OperationRemoteEnumerate, Purpose: CommandPurposeList,
		Args: []string{"-mindepth", "1", "-maxdepth", "1", "-print0"}, PrivateLocator: "/srv/backups",
		Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}},
	}
	specification, _, _, err := transport.commandSpec(invocation, testOperationLimits(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/srv/backups", "-mindepth", "1", "-maxdepth", "1", "-print0"}
	if strings.Join(specification.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("remote find args=%q want=%q", specification.Args, want)
	}
}

type trackingProviderReadHandle struct {
	io.Reader
	closed bool
}

func (handle *trackingProviderReadHandle) Close() error { handle.closed = true; return nil }
