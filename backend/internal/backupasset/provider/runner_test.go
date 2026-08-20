package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
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
	if err := handle.Close(); err == nil {
		t.Fatal("overflow close unexpectedly succeeded")
	} else {
		var capabilityErr *CapabilityError
		if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityProviderResourceLimit {
			t.Fatalf("overflow close error=%v, want provider resource limit", err)
		}
	}
}

func TestBoundedReadHandleReportsProbeInclusiveProviderBytes(t *testing.T) {
	t.Run("overflow proof", func(t *testing.T) {
		underlying := &trackingProviderReadHandle{Reader: strings.NewReader("12345")}
		handle := newBoundedReadHandle(underlying, 4)
		reporter, ok := handle.(ProviderByteReporter)
		if !ok {
			t.Fatal("bounded handle does not expose ProviderByteReporter")
		}
		buffer := make([]byte, 4)
		if _, err := io.ReadFull(handle, buffer); err != nil {
			t.Fatal(err)
		}
		if got := reporter.ProviderBytes(); got != 5 {
			t.Fatalf("provider bytes after inline proof=%d, want 5", got)
		}
		if err := handle.Close(); err == nil {
			t.Fatal("overflow close unexpectedly succeeded")
		}
		if got := reporter.ProviderBytes(); got != 5 {
			t.Fatalf("provider bytes after close=%d, want inline proof retained", got)
		}
	})

	t.Run("exact EOF", func(t *testing.T) {
		handle := newBoundedReadHandle(&trackingProviderReadHandle{Reader: strings.NewReader("1234")}, 4)
		reporter, ok := handle.(ProviderByteReporter)
		if !ok {
			t.Fatal("bounded handle does not expose ProviderByteReporter")
		}
		buffer := make([]byte, 4)
		if _, err := io.ReadFull(handle, buffer); err != nil || string(buffer) != "1234" {
			t.Fatalf("direct exact read value=%q err=%v", buffer, err)
		}
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
		if got := reporter.ProviderBytes(); got != 4 {
			t.Fatalf("exact EOF provider bytes=%d, want 4", got)
		}
	})
}

func TestBoundedReadHandleCloseDoesNotStartIdleProbe(t *testing.T) {
	underlying := newBlockingExactLimitProbeProviderReadHandle()
	handle := newBoundedReadHandle(underlying, 1)
	reporter := handle.(ProviderByteReporter)

	closeDone := make(chan error, 1)
	closeObserved := false
	t.Cleanup(func() {
		underlying.allowCloseReturn()
		underlying.allowProbeReturn()
		if !closeObserved {
			select {
			case <-closeDone:
			case <-time.After(time.Second):
				t.Error("bounded close did not finish during cleanup")
			}
		}
	})
	go func() { closeDone <- handle.Close() }()

	select {
	case <-underlying.closeCalled:
	case <-underlying.probeEntered:
		t.Fatal("idle bounded close entered an exact-limit probe")
	case <-time.After(time.Second):
		t.Fatal("bounded close did not reach underlying Close")
	}
	select {
	case err := <-closeDone:
		closeObserved = true
		t.Fatalf("bounded close returned before underlying Close drained: %v", err)
	default:
	}

	underlying.allowCloseReturn()
	select {
	case err := <-closeDone:
		closeObserved = true
		if err != nil {
			t.Fatalf("idle close error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded close did not finish after underlying Close drained")
	}
	if got := reporter.ProviderBytes(); got != 0 {
		t.Fatalf("provider bytes=%d, want no read", got)
	}
	if got := underlying.reads.Load(); got != 0 {
		t.Fatalf("underlying reads=%d, want no idle hidden probe", got)
	}
}

func TestBoundedReadHandleCloseDrainsBlockedExactLimitProbe(t *testing.T) {
	underlying := newBlockingExactLimitProbeProviderReadHandle()
	defer underlying.allowCloseReturn()
	defer underlying.allowProbeReturn()
	handle := newBoundedReadHandle(underlying, 1)
	reporter := handle.(ProviderByteReporter)
	type readResult struct {
		count int
		err   error
	}
	readDone := make(chan readResult, 1)
	go func() {
		count, err := handle.Read(make([]byte, 1))
		readDone <- readResult{count: count, err: err}
	}()
	select {
	case <-underlying.probeEntered:
	case <-time.After(time.Second):
		t.Fatal("final permitted read did not reach its inline exact-limit probe")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- handle.Close() }()
	select {
	case <-underlying.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("bounded close did not reach the blocked exact-limit probe")
	}
	select {
	case err := <-closeDone:
		t.Fatalf("bounded close returned before exact-limit probe drained: %v", err)
	default:
	}

	underlying.allowCloseReturn()
	select {
	case err := <-closeDone:
		t.Fatalf("bounded close returned before exact-limit probe drained: %v", err)
	default:
	}
	underlying.allowProbeReturn()
	select {
	case result := <-readDone:
		if result.count != 1 || result.err != nil {
			t.Fatalf("final permitted read count=%d err=%v, want one byte with inline EOF proof", result.count, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("final permitted read did not finish after inline probe drained")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("bounded close error=%v, want EOF-success close", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded close did not finish after exact-limit probe drained")
	}
	if got := reporter.ProviderBytes(); got != 1 {
		t.Fatalf("provider bytes=%d, want permitted byte only", got)
	}
	if got := underlying.reads.Load(); got != 2 {
		t.Fatalf("underlying reads=%d, want final permitted read plus inline exact-limit probe", got)
	}
}

func TestBoundedReadHandleCloseInterruptsBlockedRead(t *testing.T) {
	underlying := newBlockingProviderReadHandle()
	t.Cleanup(func() {
		if err := underlying.Close(); err != nil {
			t.Errorf("close blocking provider read handle: %v", err)
		}
	})
	defer underlying.allowReadReturn()
	handle := newBoundedReadHandle(underlying, 1)
	readDone := make(chan error, 1)
	go func() {
		_, err := handle.Read(make([]byte, 1))
		readDone <- err
	}()
	select {
	case <-underlying.readEntered:
	case <-time.After(time.Second):
		t.Fatal("bounded read did not reach underlying reader")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- handle.Close() }()
	select {
	case <-underlying.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("bounded close did not reach underlying reader")
	}
	select {
	case <-underlying.readReleased:
	case <-time.After(time.Second):
		t.Fatal("blocked read did not observe underlying close")
	}
	select {
	case err := <-closeDone:
		t.Fatalf("bounded close returned before blocked read drained: %v", err)
	default:
	}
	underlying.allowReadReturn()
	select {
	case err := <-readDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("blocked read error=%v, want closed pipe", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked read did not finish after close")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("non-final interrupted read close error=%v, want ordinary close behavior", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded close did not finish")
	}
	if _, err := handle.Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("post-close read error=%v, want closed pipe", err)
	}
}

func TestBoundedReadHandleCloseIgnoresQueuedPostCloseRead(t *testing.T) {
	underlying := newBlockingProviderReadHandle()
	handle := newBoundedReadHandle(underlying, 1).(*boundedReadHandle)

	handle.readMu.Lock()
	locked := true
	defer func() {
		if locked {
			handle.readMu.Unlock()
		}
	}()
	queuedStarted := make(chan struct{})
	queuedDone := make(chan error, 1)
	go func() {
		close(queuedStarted)
		_, err := handle.Read(make([]byte, 1))
		queuedDone <- err
	}()
	<-queuedStarted

	closeDone := make(chan error, 1)
	go func() { closeDone <- handle.Close() }()

	select {
	case <-underlying.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("bounded close did not reach underlying reader")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close error=%v, want clean close without an underlying read", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded close waited for a queued post-close read")
	}
	handle.readMu.Unlock()
	locked = false
	select {
	case err := <-queuedDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("queued post-close read error=%v, want closed pipe", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued post-close read did not return")
	}
	select {
	case <-underlying.readEntered:
		t.Fatal("queued post-close read reached the underlying reader")
	default:
	}
}

func TestBoundedReadHandleCloseWaitsOnlyForInFlightRead(t *testing.T) {
	underlying := newBlockingCloseProviderReadHandle()
	handle := newBoundedReadHandle(underlying, 1).(*boundedReadHandle)

	// Model the active Read after it has claimed the serialization lock and
	// marked itself in flight, before it calls the underlying reader.
	handle.readMu.Lock()
	readMuLocked := true
	handle.mu.Lock()
	handle.readInFlight = true
	handle.mu.Unlock()

	closeDone := make(chan error, 1)
	closeObserved := false
	go func() { closeDone <- handle.Close() }()
	select {
	case <-underlying.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("bounded close did not reach underlying reader")
	}

	// This is the state transition an active Read makes before it releases
	// readMu. A post-close reader can then acquire readMu but be paused before
	// it observes the closed state.
	handle.finishRead()
	queuedOwnsReadMu := make(chan struct{})
	releaseQueuedRead := make(chan struct{})
	go func() {
		handle.readMu.Lock()
		close(queuedOwnsReadMu)
		<-releaseQueuedRead
		handle.readMu.Unlock()
	}()
	handle.readMu.Unlock()
	readMuLocked = false
	select {
	case <-queuedOwnsReadMu:
	case <-time.After(time.Second):
		t.Fatal("queued post-close read did not acquire read mutex")
	}

	t.Cleanup(func() {
		close(releaseQueuedRead)
		underlying.allowCloseReturn()
		if !closeObserved {
			select {
			case <-closeDone:
			case <-time.After(time.Second):
				t.Error("bounded close did not finish during cleanup")
			}
		}
	})
	defer func() {
		if readMuLocked {
			handle.readMu.Unlock()
		}
	}()

	underlying.allowCloseReturn()
	select {
	case err := <-closeDone:
		closeObserved = true
		if err != nil {
			t.Fatalf("close error=%v, want clean close after active read drained", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded close waited for a queued post-close read")
	}
}

func TestBoundedReadHandleCloseRacingFinalPermittedReadFailsClosed(t *testing.T) {
	underlying := newFinalByteThenProbeProviderReadHandle(nil, io.ErrClosedPipe, false)
	t.Cleanup(func() {
		if err := underlying.Close(); err != nil {
			t.Errorf("close final-byte probe provider read handle: %v", err)
		}
	})
	handle := newBoundedReadHandle(underlying, 1)
	type readResult struct {
		count int
		err   error
	}
	readDone := make(chan readResult, 1)
	go func() {
		count, err := handle.Read(make([]byte, 1))
		readDone <- readResult{count: count, err: err}
	}()
	select {
	case <-underlying.readEntered:
	case <-time.After(time.Second):
		t.Fatal("bounded read did not reach final-byte reader")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- handle.Close() }()
	select {
	case <-underlying.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("bounded close did not reach final-byte reader")
	}
	select {
	case result := <-readDone:
		if result.count != 1 || !errors.Is(result.err, backupasset.ErrCapabilityUnavailable) {
			t.Fatalf("interrupted final proof count=%d err=%v, want provider unavailable", result.count, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("final permitted read did not finish")
	}
	select {
	case err := <-closeDone:
		var capabilityErr *CapabilityError
		if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityProviderUnavailable {
			t.Fatalf("close error=%v, want provider unavailable after interruption", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded close did not finish")
	}
	reporter := handle.(ProviderByteReporter)
	if got := reporter.ProviderBytes(); got != 1 {
		t.Fatalf("provider bytes=%d, want final permitted byte only", got)
	}
	if got := underlying.reads.Load(); got != 2 {
		t.Fatalf("underlying reads=%d, want final permitted read plus interrupted inline proof", got)
	}
}

func TestBoundedReadHandleCloseRacingFinalPermittedEOFRemainsSuccessful(t *testing.T) {
	underlying := newFinalByteThenProbeProviderReadHandle(io.EOF, nil, false)
	t.Cleanup(func() {
		if err := underlying.Close(); err != nil {
			t.Errorf("close final-byte probe provider read handle: %v", err)
		}
	})
	handle := newBoundedReadHandle(underlying, 1)
	type readResult struct {
		count int
		err   error
	}
	readDone := make(chan readResult, 1)
	go func() {
		count, err := handle.Read(make([]byte, 1))
		readDone <- readResult{count: count, err: err}
	}()
	select {
	case <-underlying.readEntered:
	case <-time.After(time.Second):
		t.Fatal("bounded read did not reach final-byte reader")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- handle.Close() }()
	select {
	case <-underlying.closeCalled:
	case <-time.After(time.Second):
		t.Fatal("bounded close did not reach final-byte reader")
	}
	select {
	case result := <-readDone:
		if result.count != 1 || !errors.Is(result.err, io.EOF) {
			t.Fatalf("final permitted EOF read count=%d err=%v", result.count, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("final permitted EOF read did not finish")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("bounded close error=%v, want EOF-success close", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded close did not finish")
	}
	reporter := handle.(ProviderByteReporter)
	if got := reporter.ProviderBytes(); got != 1 {
		t.Fatalf("provider bytes=%d, want final permitted byte only", got)
	}
	if got := underlying.reads.Load(); got != 1 {
		t.Fatalf("underlying reads=%d, want no post-close overflow probe", got)
	}
}

func TestRunnerValidatesReadOnlyToolOperationPairs(t *testing.T) {
	snapshotID := strings.Repeat("a", 64)
	resticSecret := []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY")
	valid := []CommandInvocation{
		{Tool: ToolRestic, Operation: OperationResticVersion, Args: []string{"version"}},
		{Tool: ToolRestic, Operation: OperationResticConfig, Args: []string{"--password-file", "/dev/stdin", "cat", "config"}, SecretStdin: resticSecret},
		{Tool: ToolRestic, Operation: OperationResticSnapshots, Args: []string{"--password-file", "/dev/stdin", "snapshots", "--json"}, SecretStdin: resticSecret},
		{Tool: ToolRestic, Operation: OperationResticSnapshots, Args: []string{"--password-file", "/dev/stdin", "snapshots", "--json", "--", snapshotID}, SecretStdin: resticSecret},
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
	specs             []sshutil.CommandSpec
	openMaxBytes      int64
	executionMaxBytes int64
	result            sshutil.CommandResult
	handle            sshutil.CommandReadHandle
	execution         sshutil.CommandExecutionStream
	executionErr      error
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

func (runner *fakeRemoteCommandRunner) OpenExecution(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
	runner.specs = append(runner.specs, spec)
	runner.executionMaxBytes = spec.MaxStdoutBytes
	return runner.execution, runner.executionErr
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

func TestRunnerPublicationResticOperationsAreStructurallyAllowlisted(t *testing.T) {
	linkTag := "xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pointTag := "xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	snapshotID := strings.Repeat("c", 64)
	secret := []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY")
	valid := []CommandInvocation{
		{
			Tool: ToolRestic, Operation: OperationResticBackup, Purpose: CommandPurposePublish, SecretStdin: secret,
			Args: []string{"--password-file", "/dev/stdin", "backup", "--json", "--tag", linkTag, "--tag", pointTag, "--exclude", "/var/cache", "--", "/private/source"},
		},
		{
			Tool: ToolRestic, Operation: OperationResticSnapshotsByTags, Purpose: CommandPurposeManifest, SecretStdin: secret,
			Args: []string{"--password-file", "/dev/stdin", "snapshots", "--json", "--tag", linkTag + "," + pointTag},
		},
		{
			Tool: ToolRestic, Operation: OperationResticManifest, Purpose: CommandPurposeManifest, SecretStdin: secret,
			Args: []string{"--password-file", "/dev/stdin", "ls", "--json", "--recursive", "--", snapshotID, "/"},
		},
	}
	for _, invocation := range valid {
		if err := invocation.Validate(); err != nil {
			t.Fatalf("valid publication invocation %s rejected: %v", invocation.Operation, err)
		}
	}
	for _, invocation := range []CommandInvocation{
		{Tool: ToolRestic, Operation: OperationResticBackup, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "backup", "--json", "--tag", pointTag, "--tag", linkTag, "--", "/private/source"}},
		{Tool: ToolRestic, Operation: OperationResticBackup, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "backup", "--json", "--tag", linkTag + ",x", "--tag", pointTag, "--", "/private/source"}},
		{Tool: ToolRestic, Operation: OperationResticBackup, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "backup", "--json", "--tag", linkTag, "--tag", pointTag, "--", "relative/source"}},
		{Tool: ToolRestic, Operation: OperationResticBackup, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "backup", "--json", "--tag", linkTag, "--tag", pointTag, "--", "/private/source", "--dry-run"}},
		{Tool: ToolRestic, Operation: OperationResticSnapshotsByTags, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "snapshots", "--json", "--tag", linkTag, "--tag", pointTag}},
		{Tool: ToolRestic, Operation: OperationResticManifest, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "ls", "--json", "--recursive", "--", snapshotID[:8], "/"}},
		{Tool: ToolRestic, Operation: CommandOperation("restic_forget"), SecretStdin: secret, Args: []string{"forget", "--prune"}},
		{Tool: ToolRestic, Operation: CommandOperation("restic_restore"), SecretStdin: secret, Args: []string{"restore", "latest"}},
		{Tool: ToolRestic, Operation: CommandOperation("restic_init"), SecretStdin: secret, Args: []string{"init"}},
	} {
		if err := invocation.Validate(); !errors.Is(err, ErrUnsafeInvocation) {
			t.Fatalf("unsafe publication invocation %s accepted: %v", invocation.Operation, err)
		}
	}
}

func TestRunnerPublicationPurposeMapsToSSHScopes(t *testing.T) {
	for purpose, want := range map[CommandPurpose]string{
		CommandPurposePublish:  sshutil.PurposeTaskBackup,
		CommandPurposeManifest: sshutil.PurposeRepositoryList,
		CommandPurposeDelete:   sshutil.PurposeTaskBackup,
	} {
		got, ok := sshPurpose(purpose)
		if !ok || got != want {
			t.Fatalf("purpose %q mapped to %q ok=%v, want %q", purpose, got, ok, want)
		}
	}
}

func TestRcloneManagedCommandAllowlistBuildsOnlyFixedArguments(t *testing.T) {
	transport, err := newSSHCommandTransport(func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error) {
		return &fakeRemoteCommandRunner{}, &trackingCloser{}, nil
	}, 1, ToolBinaries{Rclone: "rclone"})
	if err != nil {
		t.Fatal(err)
	}
	source := mustRclonePrivateLocatorForTest(t, "/srv/source")
	destination := mustRclonePrivateLocatorForTest(t, "s3:bucket/managed/prefix")
	staged := stagedPayloadRefForCommandTest("/home/operator/.xirang/rclone-publication/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/commit.json")
	copyDest := mustRclonePrivateLocatorForTest(t, "s3:bucket/managed/parent/data")
	base := []string{"--config", "/dev/stdin", "--retries", "1", "--low-level-retries", "3", "--links"}
	tests := []struct {
		name        string
		operation   CommandOperation
		source      *RclonePrivateLocator
		destination *RclonePrivateLocator
		stagedFrom  *StagedPayloadRef
		stagedTo    *StagedPayloadRef
		copyDest    *RclonePrivateLocator
		want        []string
	}{
		{name: "version", operation: OperationRcloneManagedVersion, want: []string{"version"}},
		{name: "features", operation: OperationRcloneManagedFeatures, source: &destination, want: []string{"backend", "features", "--", destination.value}},
		{name: "recursive list", operation: OperationRcloneManagedRecursiveList, source: &destination, want: []string{"lsjson", "--recursive", "--hash", "--metadata", "--", destination.value}},
		{name: "copy", operation: OperationRcloneManagedCopy, source: &source, destination: &destination, want: []string{"copy", "--create-empty-src-dirs", "--", source.value, destination.value}},
		{name: "copy with exact parent", operation: OperationRcloneManagedCopy, source: &source, destination: &destination, copyDest: &copyDest, want: []string{"copy", "--create-empty-src-dirs", "--copy-dest", copyDest.value, "--", source.value, destination.value}},
		{name: "native sync", operation: OperationRcloneManagedNativeSync, source: &source, destination: &destination, want: []string{"sync", "--create-empty-src-dirs", "--", source.value, destination.value}},
		{name: "check download", operation: OperationRcloneManagedCheckDownload, source: &source, destination: &destination, want: []string{"check", "--download", "--one-way", "--", source.value, destination.value}},
		{name: "copyto staged upload", operation: OperationRcloneManagedCopyTo, destination: &destination, stagedFrom: &staged, want: []string{"copyto", "--", staged.path, destination.value}},
		{name: "copyto staged download", operation: OperationRcloneManagedCopyTo, source: &destination, stagedTo: &staged, want: []string{"copyto", "--", destination.value, staged.path}},
		{name: "cat", operation: OperationRcloneManagedCat, source: &destination, want: []string{"cat", "--", destination.value}},
		{name: "exact stat", operation: OperationRcloneManagedExactStat, source: &destination, want: []string{"lsjson", "--stat", "--hash", "--metadata", "--", destination.value}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := CommandInvocation{
				Tool: ToolRclone, Operation: test.operation, Purpose: CommandPurposePublish,
				SecretStdin: []byte("FAKE_BOUND_RCLONE_CONFIG_FOR_TEST_ONLY"), Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}},
				RcloneSource: test.source, RcloneDestination: test.destination,
				RcloneStagedSource: test.stagedFrom, RcloneStagedDestination: test.stagedTo,
				RcloneCopyDest: test.copyDest, RcloneLowLevelRetries: 3, AbsoluteDeadline: time.Now().UTC().Add(time.Hour),
			}
			specification, _, _, err := transport.commandSpec(invocation, testOperationLimits(), 1024)
			if err != nil {
				t.Fatalf("commandSpec: %v", err)
			}
			want := append(append([]string(nil), base...), test.want...)
			if strings.Join(specification.Args, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("managed args=%q want=%q", specification.Args, want)
			}
			if specification.SecretStdin == nil || string(specification.SecretStdin.Value) != "FAKE_BOUND_RCLONE_CONFIG_FOR_TEST_ONLY" {
				t.Fatalf("managed config did not use SecretStdin: %+v", specification.SecretStdin)
			}
		})
	}
}

func TestRcloneManagedInvocationRejectsCallerFlagsRawPathsAndUnsafeVariants(t *testing.T) {
	source := mustRclonePrivateLocatorForTest(t, "/srv/source")
	destination := mustRclonePrivateLocatorForTest(t, "s3:bucket/managed/prefix")
	valid := CommandInvocation{
		Tool: ToolRclone, Operation: OperationRcloneManagedCopy, Purpose: CommandPurposePublish,
		SecretStdin: []byte("FAKE_BOUND_RCLONE_CONFIG_FOR_TEST_ONLY"), RcloneSource: &source, RcloneDestination: &destination,
		RcloneLowLevelRetries: 3, AbsoluteDeadline: time.Now().UTC().Add(time.Hour),
	}
	mutations := []func(*CommandInvocation){
		func(value *CommandInvocation) { value.Args = []string{"--backup-dir", "attacker:root"} },
		func(value *CommandInvocation) { value.Args = []string{"--dest-after", "/tmp/leak"} },
		func(value *CommandInvocation) { value.Args = []string{"--copy-links"} },
		func(value *CommandInvocation) { value.Args = []string{"--skip-links"} },
		func(value *CommandInvocation) { value.Args = []string{"delete", "--", destination.value} },
		func(value *CommandInvocation) { value.Args = []string{"/home/operator/.xirang/rclone-publication/raw"} },
		func(value *CommandInvocation) { value.SecretStdin = nil },
		func(value *CommandInvocation) { value.RcloneLowLevelRetries = 0 },
		func(value *CommandInvocation) { value.RcloneLowLevelRetries = 11 },
		func(value *CommandInvocation) { value.AbsoluteDeadline = time.Time{} },
		func(value *CommandInvocation) { value.RcloneDestination = nil },
		func(value *CommandInvocation) { value.Operation = CommandOperation("rclone_managed_purge") },
	}
	for index, mutate := range mutations {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); !errors.Is(err, ErrUnsafeInvocation) {
			t.Fatalf("unsafe managed invocation case %d accepted: %v", index, err)
		}
	}
	if _, err := NewRclonePrivateLocator("--config=/tmp/attacker"); err == nil {
		t.Fatal("flag-shaped private locator accepted")
	}
}

func TestRcloneManagedVersionIsPinnedExactly(t *testing.T) {
	if err := ValidateManagedRcloneVersion([]byte("rclone v1.74.4\n- os/version: test\n")); err != nil {
		t.Fatalf("pinned Rclone version rejected: %v", err)
	}
	for _, output := range []string{"rclone v1.74.3\n", "rclone v1.75.0\n", "rclone v1.74.4-beta.1\n", "v1.74.4\n"} {
		if err := ValidateManagedRcloneVersion([]byte(output)); err == nil {
			t.Fatalf("unpinned managed Rclone output accepted: %q", output)
		}
	}
}

func TestRcloneManagedTransportPropagatesAbsoluteContextDeadline(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Hour)
	var observed time.Time
	transport, err := newSSHCommandTransport(func(ctx context.Context, _ RemoteCommandAccess, _ string) (remoteCommandRunner, io.Closer, error) {
		observed, _ = ctx.Deadline()
		return &fakeRemoteCommandRunner{}, &trackingCloser{}, nil
	}, 1, ToolBinaries{Rclone: "rclone"})
	if err != nil {
		t.Fatal(err)
	}
	invocation := CommandInvocation{
		Tool: ToolRclone, Operation: OperationRcloneManagedVersion, Purpose: CommandPurposeProbe,
		SecretStdin: []byte("FAKE_BOUND_RCLONE_CONFIG_FOR_TEST_ONLY"), Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}},
		RcloneLowLevelRetries: 3, AbsoluteDeadline: deadline,
	}
	if _, err := transport.Run(context.Background(), invocation, testOperationLimits()); err != nil {
		t.Fatalf("run managed version: %v", err)
	}
	if observed.IsZero() || !observed.Equal(deadline) {
		t.Fatalf("managed command context deadline=%v, want %v", observed, deadline)
	}
}

func mustRclonePrivateLocatorForTest(t *testing.T, value string) RclonePrivateLocator {
	t.Helper()
	locator, err := NewRclonePrivateLocator(value)
	if err != nil {
		t.Fatal(err)
	}
	return locator
}

func stagedPayloadRefForCommandTest(path string) StagedPayloadRef {
	return StagedPayloadRef{
		attemptID: strings.Repeat("a", 32), name: filepath.Base(path), path: path,
		size: 1, digest: strings.Repeat("b", 64), ownerMarkerPath: filepath.Join(filepath.Dir(path), stagedPayloadOwnerMarkerName),
		ownerDigest: strings.Repeat("c", 64), lease: &stagedPayloadLease{},
	}
}

func TestSSHCommandTransportOpenExecutionCopiesCompletionAndRetainsConnection(t *testing.T) {
	taskID := uint(41)
	taskRunID := uint(42)
	underlying := &fakeSSHExecution{
		Reader:     strings.NewReader("provider stdout"),
		completion: sshutil.CommandCompletion{ExitCode: 17, ExitCodeKnown: true, Stderr: []byte("provider stderr"), StderrTruncated: true},
	}
	runner := &fakeRemoteCommandRunner{execution: underlying}
	closer := &trackingCloser{}
	var observedAudit sshutil.DialAuditContext
	var observedPurpose string
	transport, err := newSSHCommandTransport(func(_ context.Context, access RemoteCommandAccess, purpose string) (remoteCommandRunner, io.Closer, error) {
		observedAudit = access.Audit
		observedPurpose = purpose
		return runner, closer, nil
	}, 1, ToolBinaries{Restic: "restic"})
	if err != nil {
		t.Fatal(err)
	}
	linkTag := "xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pointTag := "xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	invocation := CommandInvocation{
		Tool: ToolRestic, Operation: OperationResticBackup, Purpose: CommandPurposePublish,
		Args:        []string{"--password-file", "/dev/stdin", "backup", "--json", "--tag", linkTag, "--tag", pointTag, "--", "/private/source"},
		SecretStdin: []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"), PrivateLocator: "FAKE_REPOSITORY_LOCATOR_FOR_TEST_ONLY",
		Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}, Audit: sshutil.DialAuditContext{
			UserID: 7, Username: "operator", Role: "operator", CorrelationID: "corr.publication-42", TaskID: &taskID, TaskRunID: &taskRunID,
		}},
	}
	execution, err := transport.OpenExecution(context.Background(), invocation, testOperationLimits(), 8192)
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	if observedPurpose != sshutil.PurposeTaskBackup || observedAudit.TaskID != &taskID || observedAudit.TaskRunID != &taskRunID || closer.closed {
		t.Fatalf("purpose=%q audit=%+v close=%v", observedPurpose, observedAudit, closer.closed)
	}
	stdout, readErr := io.ReadAll(execution)
	if readErr != nil || string(stdout) != "provider stdout" || closer.closed {
		t.Fatalf("stdout=%q read=%v close=%v", stdout, readErr, closer.closed)
	}
	completion, joinErr := execution.Join()
	if joinErr != nil || completion.ExitCode != 17 || !completion.ExitCodeKnown || string(completion.Stderr) != "provider stderr" || !completion.StderrTruncated || !closer.closed || runner.executionMaxBytes != 8192 {
		t.Fatalf("completion=%+v join=%v close=%v max=%d", completion, joinErr, closer.closed, runner.executionMaxBytes)
	}
}

type fakeSSHExecution struct {
	io.Reader
	completion sshutil.CommandCompletion
	joinErr    error
	cancelErr  error
}

func (execution *fakeSSHExecution) Join() (sshutil.CommandCompletion, error) {
	return execution.completion, execution.joinErr
}

func (execution *fakeSSHExecution) Cancel() error { return execution.cancelErr }

type trackingProviderReadHandle struct {
	io.Reader
	closed bool
}

func (handle *trackingProviderReadHandle) Close() error { handle.closed = true; return nil }

type blockingCloseProviderReadHandle struct {
	closeCalled chan struct{}
	allowClose  chan struct{}
	closeOnce   sync.Once
	allowOnce   sync.Once
}

func newBlockingCloseProviderReadHandle() *blockingCloseProviderReadHandle {
	return &blockingCloseProviderReadHandle{
		closeCalled: make(chan struct{}),
		allowClose:  make(chan struct{}),
	}
}

func (handle *blockingCloseProviderReadHandle) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (handle *blockingCloseProviderReadHandle) Close() error {
	handle.closeOnce.Do(func() {
		close(handle.closeCalled)
		<-handle.allowClose
	})
	return nil
}

func (handle *blockingCloseProviderReadHandle) allowCloseReturn() {
	handle.allowOnce.Do(func() { close(handle.allowClose) })
}

type blockingProviderReadHandle struct {
	readEntered      chan struct{}
	closeCalled      chan struct{}
	releaseRead      chan struct{}
	readReleased     chan struct{}
	allowReadRelease chan struct{}
	closeOnce        sync.Once
	releaseOnce      sync.Once
}

func newBlockingProviderReadHandle() *blockingProviderReadHandle {
	return &blockingProviderReadHandle{
		readEntered:      make(chan struct{}),
		closeCalled:      make(chan struct{}),
		releaseRead:      make(chan struct{}),
		readReleased:     make(chan struct{}),
		allowReadRelease: make(chan struct{}),
	}
}

func (handle *blockingProviderReadHandle) Read([]byte) (int, error) {
	select {
	case <-handle.readEntered:
	default:
		close(handle.readEntered)
	}
	<-handle.releaseRead
	close(handle.readReleased)
	<-handle.allowReadRelease
	return 0, io.ErrClosedPipe
}

func (handle *blockingProviderReadHandle) Close() error {
	handle.closeOnce.Do(func() {
		close(handle.closeCalled)
		close(handle.releaseRead)
	})
	return nil
}

func (handle *blockingProviderReadHandle) allowReadReturn() {
	handle.releaseOnce.Do(func() { close(handle.allowReadRelease) })
}

type blockingExactLimitProbeProviderReadHandle struct {
	reads             atomic.Int32
	probeEntered      chan struct{}
	closeCalled       chan struct{}
	allowProbeRelease chan struct{}
	allowCloseRelease chan struct{}
	probeOnce         sync.Once
	closeOnce         sync.Once
	allowProbeOnce    sync.Once
	allowCloseOnce    sync.Once
}

func newBlockingExactLimitProbeProviderReadHandle() *blockingExactLimitProbeProviderReadHandle {
	return &blockingExactLimitProbeProviderReadHandle{
		probeEntered:      make(chan struct{}),
		closeCalled:       make(chan struct{}),
		allowProbeRelease: make(chan struct{}),
		allowCloseRelease: make(chan struct{}),
	}
}

func (handle *blockingExactLimitProbeProviderReadHandle) Read(buffer []byte) (int, error) {
	if handle.reads.Add(1) == 1 {
		buffer[0] = '1'
		return 1, nil
	}
	handle.probeOnce.Do(func() { close(handle.probeEntered) })
	<-handle.allowProbeRelease
	return 0, io.EOF
}

func (handle *blockingExactLimitProbeProviderReadHandle) Close() error {
	handle.closeOnce.Do(func() { close(handle.closeCalled) })
	<-handle.allowCloseRelease
	return nil
}

func (handle *blockingExactLimitProbeProviderReadHandle) allowCloseReturn() {
	handle.allowCloseOnce.Do(func() { close(handle.allowCloseRelease) })
}

func (handle *blockingExactLimitProbeProviderReadHandle) allowProbeReturn() {
	handle.allowProbeOnce.Do(func() { close(handle.allowProbeRelease) })
}

type finalByteThenProbeProviderReadHandle struct {
	readEntered chan struct{}
	closeCalled chan struct{}
	releaseRead chan struct{}
	closeOnce   sync.Once
	reads       atomic.Int32
	finalErr    error
	probeErr    error
	probeByte   bool
}

func newFinalByteThenProbeProviderReadHandle(finalErr, probeErr error, probeByte bool) *finalByteThenProbeProviderReadHandle {
	return &finalByteThenProbeProviderReadHandle{
		readEntered: make(chan struct{}),
		closeCalled: make(chan struct{}),
		releaseRead: make(chan struct{}),
		finalErr:    finalErr,
		probeErr:    probeErr,
		probeByte:   probeByte,
	}
}

func (handle *finalByteThenProbeProviderReadHandle) Read(buffer []byte) (int, error) {
	switch handle.reads.Add(1) {
	case 1:
		close(handle.readEntered)
		<-handle.releaseRead
		buffer[0] = '1'
		return 1, handle.finalErr
	case 2:
		if handle.probeByte {
			buffer[0] = '2'
			return 1, handle.probeErr
		}
		return 0, handle.probeErr
	default:
		return 0, io.EOF
	}
}

func (handle *finalByteThenProbeProviderReadHandle) Close() error {
	handle.closeOnce.Do(func() {
		close(handle.closeCalled)
		close(handle.releaseRead)
	})
	return nil
}
