package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestResticSnapshotPresenceProbesExactID(t *testing.T) {
	snapshotID := strings.Repeat("c", 64)
	transport := newResticDeletionTransport(t, resticDeletionScript{
		listed:   [][]string{{snapshotID}, {}},
		forgetOK: true,
	})
	deleter := newResticPointDeleterForTest(t, transport)
	result, err := deleter.DeletePoint(context.Background(), validResticDeletePointRequest(t))
	if err != nil {
		t.Fatalf("DeletePoint exact probe: %v", err)
	}
	if result.Outcome != DeletePointDeleted {
		t.Fatalf("exact probe result=%+v, want deleted", result)
	}
	if len(transport.snapshotArgs) == 0 {
		t.Fatal("snapshot presence probe was not invoked")
	}
	for index, args := range transport.snapshotArgs {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, snapshotID) || !strings.Contains(joined, "--") {
			t.Fatalf("snapshot probe %d args=%q, want exact snapshot ID", index, joined)
		}
		if strings.Count(joined, snapshotID) < 1 {
			t.Fatalf("snapshot probe %d omitted the snapshot ID: %q", index, joined)
		}
	}
	if err := (CommandInvocation{
		Tool: ToolRestic, Operation: OperationResticSnapshots, Purpose: CommandPurposeList,
		SecretStdin: []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"),
		Args:        []string{"--password-file", "/dev/stdin", "snapshots", "--json", "--", snapshotID},
	}).Validate(); err != nil {
		t.Fatalf("exact snapshot probe allowlist rejected: %v", err)
	}
}

func TestResticExactPointDeletionForgetsFullIDAndPrunes(t *testing.T) {
	snapshotID := strings.Repeat("c", 64)
	transport := newResticDeletionTransport(t, resticDeletionScript{
		listed:   [][]string{{snapshotID}, {}},
		forgetOK: true,
	})
	deleter := newResticPointDeleterForTest(t, transport)
	result, err := deleter.DeletePoint(context.Background(), validResticDeletePointRequest(t))
	if err != nil {
		t.Fatalf("DeletePoint exact ID: %v", err)
	}
	if result.Outcome != DeletePointDeleted || !lowerHex(result.ReceiptDigest, 64) {
		t.Fatalf("exact forget result=%+v", result)
	}
	if result.ReceiptDigest == snapshotID {
		t.Fatal("receipt digest reused the raw snapshot ID")
	}
	if transport.forgetCalls != 1 || transport.listCalls != 2 {
		t.Fatalf("forget/list calls=%d/%d, want 1/2", transport.forgetCalls, transport.listCalls)
	}
	if !transport.forgotExact || transport.forgotLatest || transport.forgotPrefix || transport.forgotTag || transport.forgotKeep {
		t.Fatalf("forget invocation was not the exact full-ID prune: %+v", transport)
	}
	if err := (CommandInvocation{
		Tool: ToolRestic, Operation: OperationResticForgetExact, Purpose: CommandPurposeDelete,
		SecretStdin: []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"),
		Args:        []string{"--password-file", "/dev/stdin", "forget", "--prune", "--", snapshotID},
	}).Validate(); err != nil {
		t.Fatalf("exact forget allowlist rejected: %v", err)
	}
}

func TestResticExactPointDeletionRejectsLatestPrefixTagGFSAndMulti(t *testing.T) {
	snapshotID := strings.Repeat("c", 64)
	secret := []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY")
	for _, invocation := range []CommandInvocation{
		{Tool: ToolRestic, Operation: OperationResticForgetExact, Purpose: CommandPurposeDelete, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "forget", "--prune", "--", "latest"}},
		{Tool: ToolRestic, Operation: OperationResticForgetExact, Purpose: CommandPurposeDelete, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "forget", "--prune", "--", snapshotID[:8]}},
		{Tool: ToolRestic, Operation: OperationResticForgetExact, Purpose: CommandPurposeDelete, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "forget", "--prune", "--tag", "prod", "--", snapshotID}},
		{Tool: ToolRestic, Operation: OperationResticForgetExact, Purpose: CommandPurposeDelete, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "forget", "--prune", "--keep-daily", "7", "--", snapshotID}},
		{Tool: ToolRestic, Operation: OperationResticForgetExact, Purpose: CommandPurposeDelete, SecretStdin: secret, Args: []string{"--password-file", "/dev/stdin", "forget", "--prune", "--", snapshotID, strings.Repeat("d", 64)}},
		{Tool: ToolRestic, Operation: CommandOperation("restic_forget"), Purpose: CommandPurposeDelete, SecretStdin: secret, Args: []string{"forget", "--prune"}},
	} {
		if err := invocation.Validate(); !errors.Is(err, ErrUnsafeInvocation) {
			t.Fatalf("unsafe forget invocation %+v accepted: %v", invocation, err)
		}
	}

	transport := newResticDeletionTransport(t, resticDeletionScript{listed: [][]string{{snapshotID}}})
	deleter := newResticPointDeleterForTest(t, transport)
	for name, mutate := range map[string]func(*DeletePointRequest){
		"latest": func(request *DeletePointRequest) { request.Point.Native = "latest" },
		"prefix": func(request *DeletePointRequest) { request.Point.Native = snapshotID[:8] },
		"empty":  func(request *DeletePointRequest) { request.Point.Native = "" },
		"multi":  func(request *DeletePointRequest) { request.Point.Native = snapshotID + "," + strings.Repeat("d", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			request := validResticDeletePointRequest(t)
			mutate(&request)
			if _, err := deleter.DeletePoint(context.Background(), request); !errors.Is(err, ErrInvalidDeletePointRequest) {
				t.Fatalf("DeletePoint %s error=%v, want invalid delete request", name, err)
			}
			if transport.forgetCalls != 0 {
				t.Fatalf("%s reached forget %d time(s)", name, transport.forgetCalls)
			}
		})
	}
}

func TestResticExactPointDeletionAlreadyAbsentIsIdempotent(t *testing.T) {
	transport := newResticDeletionTransport(t, resticDeletionScript{listed: [][]string{{}}})
	deleter := newResticPointDeleterForTest(t, transport)
	request := validResticDeletePointRequest(t)
	first, err := deleter.DeletePoint(context.Background(), request)
	if err != nil || first.Outcome != DeletePointAlreadyAbsent || !lowerHex(first.ReceiptDigest, 64) {
		t.Fatalf("first already-absent result=%+v err=%v", first, err)
	}
	second, err := deleter.DeletePoint(context.Background(), request)
	if err != nil || second.Outcome != DeletePointAlreadyAbsent || second.ReceiptDigest != first.ReceiptDigest {
		t.Fatalf("repeat already-absent result=%+v err=%v", second, err)
	}
	if transport.forgetCalls != 0 || transport.listCalls != 2 {
		t.Fatalf("already-absent forget/list calls=%d/%d, want 0/2", transport.forgetCalls, transport.listCalls)
	}
}

func TestResticExactPointDeletionCancelsAndJoins(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	transport := newResticDeletionTransport(t, resticDeletionScript{
		listed: [][]string{{strings.Repeat("c", 64)}},
		blockForget: func() {
			close(started)
			<-release
		},
	})
	deleter := newResticPointDeleterForTest(t, transport)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := deleter.DeletePoint(ctx, validResticDeletePointRequest(t))
		errCh <- err
	}()
	<-started
	cancel()
	close(release)
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled DeletePoint error=%v, want context.Canceled", err)
	}
	if !transport.canceled || !transport.joined {
		t.Fatalf("canceled forget cancel=%t join=%t, want both true", transport.canceled, transport.joined)
	}
}

func TestResticExactPointDeletionBoundsOutputAndHidesRawSecrets(t *testing.T) {
	raw := bytes.Repeat([]byte("FAKE_RESTIC_FORGET_STDOUT_FOR_TEST_ONLY"), 64)
	transport := newResticDeletionTransport(t, resticDeletionScript{
		listed:        [][]string{{strings.Repeat("c", 64)}},
		forgetStdout:  raw,
		forgetBounded: true,
	})
	deleter := newResticPointDeleterForTest(t, transport)
	_, err := deleter.DeletePoint(context.Background(), validResticDeletePointRequest(t))
	if err == nil {
		t.Fatal("oversized forget output unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), "FAKE_RESTIC_FORGET_STDOUT_FOR_TEST_ONLY") {
		t.Fatalf("raw forget output leaked in error: %v", err)
	}
	if transport.maxBytes <= 0 || int64(len(raw)) <= transport.maxBytes {
		t.Fatalf("forget was not bounded: maxBytes=%d raw=%d", transport.maxBytes, len(raw))
	}
	if !transport.canceled || !transport.joined {
		t.Fatalf("bounded forget cancel=%t join=%t, want both true", transport.canceled, transport.joined)
	}

	request := validResticDeletePointRequest(t)
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"FAKE_RESTIC_REPOSITORY_FOR_TEST_ONLY",
		"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY",
		strings.Repeat("c", 64),
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("private restic deletion value %q leaked in %s", forbidden, payload)
		}
	}
}

func TestResticExactPointDeletionRejectsSourceRevisionDrift(t *testing.T) {
	transport := newResticDeletionTransport(t, resticDeletionScript{listed: [][]string{{strings.Repeat("c", 64)}}})
	deleter := newResticPointDeleterForTest(t, transport)
	request := validResticDeletePointRequest(t)
	request.ExpectedSourceRevision = strings.Repeat("0", 64)
	if _, err := deleter.DeletePoint(context.Background(), request); !errors.Is(err, ErrDeletePointIdentityConflict) {
		t.Fatalf("source-revision drift error=%v, want identity conflict", err)
	}
	if transport.forgetCalls != 0 {
		t.Fatalf("identity conflict reached forget %d time(s)", transport.forgetCalls)
	}
}

func TestResticExactPointDeletionRejectsNativeRepositoryIdentityDrift(t *testing.T) {
	transport := newResticDeletionTransport(t, resticDeletionScript{listed: [][]string{{strings.Repeat("c", 64)}}})
	deleter := newResticPointDeleterForTest(t, transport)
	request := validResticDeletePointRequest(t)
	request.Snapshot.RepositoryIdentity = NativeResticIdentityPrefix + strings.Repeat("b", 64)
	result, err := deleter.DeletePoint(context.Background(), request)
	if !errors.Is(err, ErrDeletePointIdentityConflict) {
		t.Fatalf("native identity drift error=%v result=%+v, want identity conflict", err, result)
	}
	if result.Outcome == DeletePointAlreadyAbsent {
		t.Fatal("native identity drift returned already-absent")
	}
	if transport.forgetCalls != 0 || transport.listCalls != 0 {
		t.Fatalf("native identity drift reached list=%d forget=%d", transport.listCalls, transport.forgetCalls)
	}
}

func TestResticExactPointDeletionLiveIdentityMustMatchBeforeAlreadyAbsent(t *testing.T) {
	transport := newResticDeletionTransport(t, resticDeletionScript{
		listed:       [][]string{{}},
		liveConfigID: strings.Repeat("d", 64),
	})
	deleter := newResticPointDeleterForTest(t, transport)
	result, err := deleter.DeletePoint(context.Background(), validResticDeletePointRequest(t))
	if !errors.Is(err, ErrDeletePointIdentityConflict) {
		t.Fatalf("live identity drift error=%v result=%+v, want identity conflict", err, result)
	}
	if result.Outcome == DeletePointAlreadyAbsent {
		t.Fatal("cached identity match with live drift returned already-absent")
	}
	if transport.forgetCalls != 0 || transport.listCalls != 0 {
		t.Fatalf("live identity drift reached list=%d forget=%d", transport.listCalls, transport.forgetCalls)
	}
}

func validResticDeletePointRequest(t *testing.T) DeletePointRequest {
	t.Helper()
	snapshot := resticReadSnapshotForTest()
	return DeletePointRequest{
		Snapshot:               snapshot,
		Point:                  PointLocator{Native: strings.Repeat("c", 64)},
		ExpectedSourceRevision: snapshot.SourceRevision,
		OperationID:            strings.Repeat("e", 32),
	}
}

func newResticPointDeleterForTest(t *testing.T, transport *resticDeletionTransport) *ResticPointDeleter {
	t.Helper()
	deleter, err := NewResticPointDeleter(transport, transport, func() (OperationLimits, error) {
		return testOperationLimits(), nil
	}, func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewResticPointDeleter: %v", err)
	}
	return deleter
}

type resticDeletionScript struct {
	listed        [][]string
	liveConfigID  string
	forgetOK      bool
	forgetStdout  []byte
	forgetBounded bool
	blockForget   func()
}

type resticDeletionTransport struct {
	script       resticDeletionScript
	configCalls  int
	listCalls    int
	forgetCalls  int
	forgotExact  bool
	forgotLatest bool
	forgotPrefix bool
	forgotTag    bool
	forgotKeep   bool
	canceled     bool
	joined       bool
	maxBytes     int64
	snapshotArgs [][]string
}

func newResticDeletionTransport(t *testing.T, script resticDeletionScript) *resticDeletionTransport {
	t.Helper()
	return &resticDeletionTransport{script: script}
}

func (transport *resticDeletionTransport) Run(_ context.Context, invocation CommandInvocation, _ OperationLimits) (CommandOutput, error) {
	switch invocation.Operation {
	case OperationResticConfig:
		transport.configCalls++
		id := transport.script.liveConfigID
		if id == "" {
			id = strings.Repeat("a", 64)
		}
		return CommandOutput{Stdout: []byte(`{"version":2,"id":"` + id + `"}`)}, nil
	case OperationResticSnapshots:
		transport.listCalls++
		copied := append([]string(nil), invocation.Args...)
		transport.snapshotArgs = append(transport.snapshotArgs, copied)
		ids := []string{}
		if transport.listCalls-1 < len(transport.script.listed) {
			ids = transport.script.listed[transport.listCalls-1]
		}
		return CommandOutput{Stdout: resticSnapshotListJSON(ids)}, nil
	default:
		return CommandOutput{}, errors.New("unexpected restic deletion transport operation")
	}
}

func (transport *resticDeletionTransport) Open(context.Context, CommandInvocation, OperationLimits, int64) (ReadHandle, error) {
	return nil, errors.New("unexpected restic deletion stream open")
}

func (transport *resticDeletionTransport) OpenExecution(_ context.Context, invocation CommandInvocation, _ OperationLimits, maxBytes int64) (CommandExecution, error) {
	if invocation.Operation != OperationResticForgetExact {
		return nil, errors.New("unexpected restic deletion execution")
	}
	transport.forgetCalls++
	transport.maxBytes = maxBytes
	joined := strings.Join(invocation.Args, " ")
	transport.forgotExact = strings.Contains(joined, "forget") && strings.Contains(joined, "--prune") && strings.Contains(joined, strings.Repeat("c", 64))
	transport.forgotLatest = strings.Contains(joined, "latest")
	transport.forgotPrefix = strings.Contains(joined, strings.Repeat("c", 8)) && !strings.Contains(joined, strings.Repeat("c", 64))
	transport.forgotTag = strings.Contains(joined, "--tag")
	transport.forgotKeep = strings.Contains(joined, "--keep-")
	if transport.script.blockForget != nil {
		transport.script.blockForget()
	}
	stdout := transport.script.forgetStdout
	if transport.script.forgetBounded && maxBytes > 0 && int64(len(stdout)) > maxBytes {
		return &resticDeletionExecution{transport: transport, overflow: true, stdout: stdout[:maxBytes]}, nil
	}
	return &resticDeletionExecution{transport: transport, stdout: stdout, ok: transport.script.forgetOK}, nil
}

type resticDeletionExecution struct {
	transport *resticDeletionTransport
	stdout    []byte
	overflow  bool
	ok        bool
	offset    int
}

func (execution *resticDeletionExecution) Read(buffer []byte) (int, error) {
	if execution.offset >= len(execution.stdout) {
		if execution.overflow {
			return 0, errors.New("restic forget output exceeded bound")
		}
		return 0, io.EOF
	}
	count := copy(buffer, execution.stdout[execution.offset:])
	execution.offset += count
	return count, nil
}

func (execution *resticDeletionExecution) Join() (CommandCompletion, error) {
	execution.transport.joined = true
	if execution.overflow {
		return CommandCompletion{ExitCodeKnown: false}, newCapabilityError(backupasset.CapabilityProviderResourceLimit)
	}
	if !execution.ok {
		return CommandCompletion{ExitCode: 1, ExitCodeKnown: true}, errors.New("restic forget failed")
	}
	return CommandCompletion{ExitCode: 0, ExitCodeKnown: true}, nil
}

func (execution *resticDeletionExecution) Cancel() error {
	execution.transport.canceled = true
	return nil
}

func resticSnapshotListJSON(ids []string) []byte {
	type row struct {
		ID   string `json:"id"`
		Time string `json:"time"`
	}
	rows := make([]row, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, row{ID: id, Time: "2026-08-18T12:00:00Z"})
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return []byte("[]")
	}
	return payload
}
