package sshutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type fakeCommandSession struct {
	stdout    []byte
	stderr    []byte
	command   string
	stdin     *trackingWriteCloser
	wait      chan struct{}
	closed    chan struct{}
	started   chan struct{}
	blockWait bool
	closeErr  error
	once      sync.Once
}

func newFakeCommandSession() *fakeCommandSession {
	return &fakeCommandSession{stdin: &trackingWriteCloser{}, wait: make(chan struct{}), closed: make(chan struct{}), started: make(chan struct{})}
}

func (session *fakeCommandSession) StdinPipe() (io.WriteCloser, error) { return session.stdin, nil }
func (session *fakeCommandSession) StdoutPipe() (io.Reader, error) {
	return bytes.NewReader(session.stdout), nil
}
func (session *fakeCommandSession) StderrPipe() (io.Reader, error) {
	return bytes.NewReader(session.stderr), nil
}
func (session *fakeCommandSession) Start(command string) error {
	session.command = command
	close(session.started)
	return nil
}
func (session *fakeCommandSession) Wait() error {
	if session.blockWait {
		<-session.closed
	}
	return nil
}
func (*fakeCommandSession) Signal(ssh.Signal) error { return nil }
func (session *fakeCommandSession) Close() error {
	session.once.Do(func() { close(session.closed) })
	return session.closeErr
}

type trackingWriteCloser struct {
	bytes.Buffer
	writes int
	closed bool
}

func (writer *trackingWriteCloser) Write(value []byte) (int, error) {
	writer.writes++
	return writer.Buffer.Write(value)
}
func (writer *trackingWriteCloser) Close() error { writer.closed = true; return nil }

type naturallyCompletingCommandSession struct {
	*fakeCommandSession
	delay time.Duration
}

type interruptedPrefixCommandSession struct {
	*fakeCommandSession
}

type naturallyFailedPrefixCommandSession struct {
	*fakeCommandSession
	waited chan struct{}
}

func (session *interruptedPrefixCommandSession) Wait() error {
	<-session.closed
	return errors.New("FAKE_EXPECTED_PREFIX_ABORT_FOR_TEST_ONLY")
}

func (session *naturallyFailedPrefixCommandSession) Wait() error {
	close(session.waited)
	return errors.New("FAKE_NATURAL_PREFIX_COMMAND_FAILURE_FOR_TEST_ONLY")
}

func (session *naturallyCompletingCommandSession) Wait() error {
	timer := time.NewTimer(session.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-session.closed:
		return errors.New("session closed before natural completion")
	}
}

func TestCommandRunnerRejectsRawShellAndLimitsOutput(t *testing.T) {
	session := newFakeCommandSession()
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	_, err := runner.Run(context.Background(), CommandSpec{Binary: "sh -c", Args: []string{"whoami"}, MaxStdoutBytes: 1024})
	if !errors.Is(err, ErrUnsafeCommandSpec) {
		t.Fatalf("raw shell accepted: %v", err)
	}
	session.stdout = bytes.Repeat([]byte("x"), 1025)
	_, err = runner.Run(context.Background(), CommandSpec{Binary: "find", Args: []string{"/safe"}, MaxStdoutBytes: 1024})
	if !errors.Is(err, ErrCommandOutputLimit) {
		t.Fatalf("oversize output=%v", err)
	}
}

func TestCommandRunnerQuotesEachOperandAndClosesSecretStdin(t *testing.T) {
	session := newFakeCommandSession()
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	result, err := runner.Run(context.Background(), CommandSpec{
		Binary:         "restic",
		Args:           []string{"dump", "--leading", "space value", "line\nbreak", "it's"},
		MaxStdoutBytes: 1024,
		SecretStdin:    &SecretStdin{Value: []byte("FAKE_PASSWORD_FOR_TEST_ONLY"), AppendNewline: true},
	})
	if err != nil || len(result.Stdout) != 0 {
		t.Fatalf("Run result=%+v err=%v", result, err)
	}
	for _, operand := range []string{"'restic'", "'--leading'", "'space value'", "'line\nbreak'", `'it'"'"'s'`} {
		if !strings.Contains(session.command, operand) {
			t.Fatalf("serialized command %q missing %q", session.command, operand)
		}
	}
	if session.stdin.writes != 1 || !session.stdin.closed || session.stdin.String() != "FAKE_PASSWORD_FOR_TEST_ONLY\n" {
		t.Fatalf("secret stdin lifecycle writes=%d closed=%v value=%q", session.stdin.writes, session.stdin.closed, session.stdin.String())
	}
	if strings.Contains(session.command, "FAKE_PASSWORD_FOR_TEST_ONLY") {
		t.Fatalf("secret leaked into command: %q", session.command)
	}
}

func TestCommandRunnerCancellationClosesSession(t *testing.T) {
	session := newFakeCommandSession()
	session.blockWait = true
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, CommandSpec{Binary: "find", MaxStdoutBytes: 1024, Timeout: time.Second})
		result <- err
	}()
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("command did not start")
	}
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("session was not closed")
	}
}

func TestCommandRunnerOutputLimitTerminatesBlockedSession(t *testing.T) {
	session := newFakeCommandSession()
	session.stdout = bytes.Repeat([]byte("x"), 1025)
	session.blockWait = true
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	result := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), CommandSpec{Binary: "find", MaxStdoutBytes: 1024, Timeout: time.Second})
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCommandOutputLimit) {
			t.Fatalf("output limit error=%v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("output limit did not terminate blocked session")
	}
}

func TestCommandRunnerOpenStreamsAndReleasesPermitOnClose(t *testing.T) {
	first := newFakeCommandSession()
	first.stdout = []byte("data")
	first.blockWait = true
	second := newFakeCommandSession()
	sessions := []*fakeCommandSession{first, second}
	var index int
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) {
		session := sessions[index]
		index++
		return session, nil
	}, 1)
	handle, err := runner.Open(context.Background(), CommandSpec{
		Binary: "restic", Args: []string{"dump"}, MaxStdoutBytes: 4,
		SecretStdin: &SecretStdin{Value: []byte("FAKE_PASSWORD_FOR_TEST_ONLY"), AppendNewline: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, readErr := io.ReadAll(handle)
	if readErr != nil || string(value) != "data" {
		t.Fatalf("stream value=%q err=%v", value, readErr)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if !first.stdin.closed || first.stdin.writes != 1 || first.stdin.String() != "FAKE_PASSWORD_FOR_TEST_ONLY\n" {
		t.Fatalf("secret stdin lifecycle=%+v", first.stdin)
	}
	secondHandle, err := runner.Open(context.Background(), CommandSpec{Binary: "rclone", Args: []string{"cat"}, MaxStdoutBytes: 4})
	if err != nil {
		t.Fatalf("permit not released: %v", err)
	}
	if err := secondHandle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandRunnerOpenJoinsNaturallyCompletedStreamBeforeClosingSession(t *testing.T) {
	base := newFakeCommandSession()
	base.stdout = []byte("data")
	session := &naturallyCompletingCommandSession{fakeCommandSession: base, delay: 20 * time.Millisecond}
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	handle, err := runner.Open(context.Background(), CommandSpec{Binary: "rclone", Args: []string{"cat"}, MaxStdoutBytes: 16, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	value, err := io.ReadAll(handle)
	if err != nil || string(value) != "data" {
		t.Fatalf("stream value=%q err=%v", value, err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("natural stream close killed the command before Wait completed: %v", err)
	}
}

func TestCommandRunnerOpenSupportsIntentionalBoundedPrefixClose(t *testing.T) {
	base := newFakeCommandSession()
	base.stdout = []byte("prefix-and-more")
	session := &interruptedPrefixCommandSession{fakeCommandSession: base}
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	handle, err := runner.Open(context.Background(), CommandSpec{
		Binary: "restic", Args: []string{"dump"}, MaxStdoutBytes: 8, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 7)
	if _, err := io.ReadFull(handle, prefix); err != nil || string(prefix) != "prefix-" {
		t.Fatalf("prefix=%q err=%v", prefix, err)
	}
	prefixCloser, ok := handle.(interface{ ClosePrefix() error })
	if !ok {
		t.Fatal("command stream does not expose intentional prefix close")
	}
	if err := prefixCloser.ClosePrefix(); err != nil {
		t.Fatalf("intentional prefix close=%v", err)
	}
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("prefix close did not terminate the command session")
	}
}

func TestCommandRunnerOpenOrdinaryEarlyClosePreservesAbortedWaitFailure(t *testing.T) {
	base := newFakeCommandSession()
	base.stdout = []byte("prefix-and-more")
	session := &interruptedPrefixCommandSession{fakeCommandSession: base}
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	handle, err := runner.Open(context.Background(), CommandSpec{
		Binary: "restic", Args: []string{"dump"}, MaxStdoutBytes: 8, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 7)
	if _, err := io.ReadFull(handle, prefix); err != nil || string(prefix) != "prefix-" {
		t.Fatalf("prefix=%q err=%v", prefix, err)
	}
	if err := handle.Close(); !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("ordinary early close suppressed aborted wait failure: %v", err)
	}
}

func TestCommandRunnerOpenPrefixClosePreservesCompletedCommandFailure(t *testing.T) {
	base := newFakeCommandSession()
	base.stdout = []byte("prefix-and-more")
	session := &naturallyFailedPrefixCommandSession{fakeCommandSession: base, waited: make(chan struct{})}
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	handle, err := runner.Open(context.Background(), CommandSpec{
		Binary: "restic", Args: []string{"dump"}, MaxStdoutBytes: 8, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-session.waited
	prefix := make([]byte, 7)
	if _, err := io.ReadFull(handle, prefix); err != nil || string(prefix) != "prefix-" {
		t.Fatalf("prefix=%q err=%v", prefix, err)
	}
	prefixCloser, ok := handle.(interface{ ClosePrefix() error })
	if !ok {
		t.Fatal("command stream does not expose intentional prefix close")
	}
	if err := prefixCloser.ClosePrefix(); !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("completed command failure was suppressed: %v", err)
	}
}

func TestCommandRunnerOpenPrefixClosePreservesSessionCleanupFailure(t *testing.T) {
	base := newFakeCommandSession()
	base.stdout = []byte("prefix-and-more")
	base.closeErr = errors.New("FAKE_PRIVATE_SESSION_CLEANUP_FAILURE_FOR_TEST_ONLY")
	session := &interruptedPrefixCommandSession{fakeCommandSession: base}
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	handle, err := runner.Open(context.Background(), CommandSpec{
		Binary: "restic", Args: []string{"dump"}, MaxStdoutBytes: 8, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 7)
	if _, err := io.ReadFull(handle, prefix); err != nil || string(prefix) != "prefix-" {
		t.Fatalf("prefix=%q err=%v", prefix, err)
	}
	prefixCloser := handle.(interface{ ClosePrefix() error })
	if err := prefixCloser.ClosePrefix(); !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("session cleanup failure was suppressed: %v", err)
	} else if strings.Contains(err.Error(), "FAKE_PRIVATE") {
		t.Fatalf("session cleanup failure leaked private evidence: %v", err)
	}
}

func TestCommandRunnerOpenEnforcesStreamingLimit(t *testing.T) {
	session := newFakeCommandSession()
	session.stdout = []byte("12345")
	session.blockWait = true
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	handle, err := runner.Open(context.Background(), CommandSpec{Binary: "rclone", Args: []string{"cat"}, MaxStdoutBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	value, readErr := io.ReadAll(handle)
	if string(value) != "1234" || !errors.Is(readErr, ErrCommandOutputLimit) {
		t.Fatalf("stream value=%q err=%v", value, readErr)
	}
	if closeErr := handle.Close(); !errors.Is(closeErr, ErrCommandOutputLimit) {
		t.Fatalf("close error=%v", closeErr)
	}
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("limited stream did not close session")
	}
}

func TestCommandRunnerOpenCancellationClosesSession(t *testing.T) {
	session := newFakeCommandSession()
	session.blockWait = true
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	ctx, cancel := context.WithCancel(context.Background())
	handle, err := runner.Open(ctx, CommandSpec{Binary: "rclone", Args: []string{"cat"}, MaxStdoutBytes: 4, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("canceled stream did not close session")
	}
	if closeErr := handle.Close(); !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("close error=%v", closeErr)
	}
}

func TestCommandRunnerExecutionJoinsExitZeroAfterNaturalEOF(t *testing.T) {
	session := newExecutionFakeCommandSession([]byte("stdout"), []byte("stderr"))
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	stream, err := runner.OpenExecution(context.Background(), CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32, MaxStderrBytes: 32})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	stdout, err := io.ReadAll(stream)
	if err != nil || string(stdout) != "stdout" {
		t.Fatalf("read stdout=%q err=%v", stdout, err)
	}
	completion, err := stream.Join()
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if !completion.ExitCodeKnown || completion.ExitCode != 0 || string(completion.Stderr) != "stderr" || completion.StderrTruncated {
		t.Fatalf("completion=%+v", completion)
	}
	select {
	case <-session.closed:
	default:
		t.Fatal("joined execution did not close the session")
	}
}

func TestCommandRunnerExecutionReturnsExactNonzeroExit(t *testing.T) {
	session := newExecutionFakeCommandSession(nil, []byte("ordinary provider diagnostic"))
	session.waitErr = fakeExitStatusError(3)
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	stream, err := runner.OpenExecution(context.Background(), CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	if stdout, readErr := io.ReadAll(stream); readErr != nil || len(stdout) != 0 {
		t.Fatalf("read stdout=%q err=%v", stdout, readErr)
	}
	completion, err := stream.Join()
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if !completion.ExitCodeKnown || completion.ExitCode != 3 || string(completion.Stderr) != "ordinary provider diagnostic" {
		t.Fatalf("completion=%+v", completion)
	}
}

func TestCommandRunnerExecutionRejectsJoinBeforeEOF(t *testing.T) {
	session := newExecutionFakeCommandSession([]byte("unread output"), nil)
	session.waitUntilClose = true
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	stream, err := runner.OpenExecution(context.Background(), CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	completion, err := stream.Join()
	if !errors.Is(err, ErrCommandFailed) || completion.ExitCodeKnown {
		t.Fatalf("early join completion=%+v err=%v", completion, err)
	}
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("early join did not cancel the session")
	}
}

func TestCommandRunnerExecutionClassifiesTimeoutCancelOutputAndWaitUncertainty(t *testing.T) {
	tests := []struct {
		name            string
		setup           func(*executionFakeCommandSession) CommandSpec
		cancelAfterOpen bool
		want            error
	}{
		{
			name: "timeout",
			setup: func(session *executionFakeCommandSession) CommandSpec {
				session.waitUntilClose = true
				return CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32, Timeout: 10 * time.Millisecond}
			},
			want: ErrCommandTimeout,
		},
		{
			name: "parent cancellation",
			setup: func(session *executionFakeCommandSession) CommandSpec {
				session.waitUntilClose = true
				return CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32}
			},
			cancelAfterOpen: true,
			want:            context.Canceled,
		},
		{
			name: "hard stdout limit",
			setup: func(session *executionFakeCommandSession) CommandSpec {
				session.stdout.payload = []byte("12")
				session.waitUntilClose = true
				return CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 1}
			},
			want: ErrCommandOutputLimit,
		},
		{
			name: "ordinary wait error",
			setup: func(session *executionFakeCommandSession) CommandSpec {
				session.waitErr = errors.New("FAKE_WAIT_UNCERTAINTY")
				return CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32}
			},
			want: ErrCommandFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newExecutionFakeCommandSession(nil, []byte("stderr payload"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
			specification := test.setup(session)
			stream, err := runner.OpenExecution(ctx, specification)
			if err != nil {
				t.Fatalf("open execution: %v", err)
			}
			if test.cancelAfterOpen {
				cancel()
			}
			_, _ = io.ReadAll(stream)
			completion, err := stream.Join()
			if !errors.Is(err, test.want) || completion.ExitCodeKnown || strings.Contains(fmtError(err), "stderr payload") || strings.Contains(fmtError(err), "FAKE_WAIT_UNCERTAINTY") {
				t.Fatalf("completion=%+v err=%v want=%v", completion, err, test.want)
			}
		})
	}
}

func TestCommandRunnerExecutionClassifiesStdoutStderrReadStdinWriteAndCloseUncertainty(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*executionFakeCommandSession)
	}{
		{"stdout read", func(session *executionFakeCommandSession) {
			session.stdout.terminalErr = errors.New("FAKE_STDOUT_READ_SECRET")
		}},
		{"stderr read", func(session *executionFakeCommandSession) {
			session.stderr.terminalErr = errors.New("FAKE_STDERR_READ_SECRET")
		}},
		{"stdin write", func(session *executionFakeCommandSession) {
			session.stdin.writeErr = errors.New("FAKE_STDIN_WRITE_SECRET")
		}},
		{"stdin close", func(session *executionFakeCommandSession) {
			session.stdin.closeErr = errors.New("FAKE_STDIN_CLOSE_SECRET")
		}},
		{"stdout close", func(session *executionFakeCommandSession) {
			session.stdout.closeErr = errors.New("FAKE_STDOUT_CLOSE_SECRET")
		}},
		{"stderr close", func(session *executionFakeCommandSession) {
			session.stderr.closeErr = errors.New("FAKE_STDERR_CLOSE_SECRET")
		}},
		{"connection close", func(session *executionFakeCommandSession) {
			session.closeErr = errors.New("FAKE_CONNECTION_CLOSE_SECRET")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newExecutionFakeCommandSession(nil, []byte("stderr payload"))
			test.setup(session)
			runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
			stream, err := runner.OpenExecution(context.Background(), CommandSpec{
				Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32,
				SecretStdin: &SecretStdin{Value: []byte("FAKE_STDIN_SECRET")},
			})
			if err != nil {
				t.Fatalf("open execution: %v", err)
			}
			_, _ = io.ReadAll(stream)
			completion, joinErr := stream.Join()
			if !errors.Is(joinErr, ErrCommandFailed) || completion.ExitCodeKnown || strings.Contains(fmtError(joinErr), "FAKE_") || strings.Contains(fmtError(joinErr), "stderr payload") {
				t.Fatalf("completion=%+v err=%v", completion, joinErr)
			}
		})
	}
}

func TestCommandRunnerExecutionKeepsStdoutAndStderrSeparateAndBounded(t *testing.T) {
	session := newExecutionFakeCommandSession([]byte("stdout"), []byte("0123456789"))
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	stream, err := runner.OpenExecution(context.Background(), CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32, MaxStderrBytes: 4})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	stdout, readErr := io.ReadAll(stream)
	if readErr != nil || string(stdout) != "stdout" || strings.Contains(string(stdout), "0123") {
		t.Fatalf("stdout=%q err=%v", stdout, readErr)
	}
	completion, joinErr := stream.Join()
	if joinErr != nil || string(completion.Stderr) != "0123" || !completion.StderrTruncated || session.stderr.BytesRead() != 10 {
		t.Fatalf("completion=%+v join=%v stderrRead=%d", completion, joinErr, session.stderr.BytesRead())
	}
}

func TestCommandRunnerExecutionCancelAndJoinAreIdempotent(t *testing.T) {
	first := newExecutionFakeCommandSession([]byte("unread"), nil)
	first.waitUntilClose = true
	second := newExecutionFakeCommandSession(nil, nil)
	sessions := []*executionFakeCommandSession{first, second}
	var index int
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) {
		session := sessions[index]
		index++
		return session, nil
	}, 1)
	stream, err := runner.OpenExecution(context.Background(), CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32})
	if err != nil {
		t.Fatalf("open first execution: %v", err)
	}
	blockedContext, blockedCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer blockedCancel()
	if _, err := runner.OpenExecution(blockedContext, CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("semaphore released before cancel/join: %v", err)
	}
	firstCancelErr := stream.Cancel()
	secondCancelErr := stream.Cancel()
	if !errors.Is(firstCancelErr, ErrCommandFailed) || !errors.Is(secondCancelErr, ErrCommandFailed) {
		t.Fatalf("cancel errors first=%v second=%v", firstCancelErr, secondCancelErr)
	}
	completion, joinErr := stream.Join()
	if !errors.Is(joinErr, ErrCommandFailed) || completion.ExitCodeKnown {
		t.Fatalf("join after cancel completion=%+v err=%v", completion, joinErr)
	}
	secondStream, err := runner.OpenExecution(context.Background(), CommandSpec{Binary: "restic", Args: []string{"backup"}, MaxStdoutBytes: 32})
	if err != nil {
		t.Fatalf("semaphore was not released after cancel: %v", err)
	}
	if _, readErr := io.ReadAll(secondStream); readErr != nil {
		t.Fatalf("read second execution: %v", readErr)
	}
	if _, joinErr := secondStream.Join(); joinErr != nil {
		t.Fatalf("join second execution: %v", joinErr)
	}
}

type fakeExitStatusError int

func (err fakeExitStatusError) Error() string   { return "remote command exited" }
func (err fakeExitStatusError) ExitStatus() int { return int(err) }

type executionFakeCommandSession struct {
	stdout         *executionReader
	stderr         *executionReader
	stdin          *executionWriteCloser
	waitErr        error
	waitUntilClose bool
	closeErr       error
	started        chan struct{}
	closed         chan struct{}
	closeOnce      sync.Once
}

func newExecutionFakeCommandSession(stdout, stderr []byte) *executionFakeCommandSession {
	return &executionFakeCommandSession{
		stdout:  &executionReader{payload: append([]byte(nil), stdout...)},
		stderr:  &executionReader{payload: append([]byte(nil), stderr...)},
		stdin:   &executionWriteCloser{},
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (session *executionFakeCommandSession) StdinPipe() (io.WriteCloser, error) {
	return session.stdin, nil
}
func (session *executionFakeCommandSession) StdoutPipe() (io.Reader, error) {
	return session.stdout, nil
}
func (session *executionFakeCommandSession) StderrPipe() (io.Reader, error) {
	return session.stderr, nil
}
func (session *executionFakeCommandSession) Start(string) error {
	close(session.started)
	return nil
}
func (session *executionFakeCommandSession) Wait() error {
	if session.waitUntilClose {
		<-session.closed
	}
	return session.waitErr
}
func (*executionFakeCommandSession) Signal(ssh.Signal) error { return nil }
func (session *executionFakeCommandSession) Close() error {
	session.closeOnce.Do(func() { close(session.closed) })
	return session.closeErr
}

type executionReader struct {
	mu          sync.Mutex
	payload     []byte
	terminalErr error
	closeErr    error
	bytesRead   int
}

func (reader *executionReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if len(reader.payload) > 0 {
		count := copy(buffer, reader.payload)
		reader.payload = reader.payload[count:]
		reader.bytesRead += count
		return count, nil
	}
	if reader.terminalErr != nil {
		err := reader.terminalErr
		reader.terminalErr = nil
		return 0, err
	}
	return 0, io.EOF
}

func (reader *executionReader) Close() error { return reader.closeErr }

func (reader *executionReader) BytesRead() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.bytesRead
}

type executionWriteCloser struct {
	mu       sync.Mutex
	value    bytes.Buffer
	writeErr error
	closeErr error
}

func (writer *executionWriteCloser) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.writeErr != nil {
		return 0, writer.writeErr
	}
	return writer.value.Write(value)
}

func (writer *executionWriteCloser) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.closeErr
}

func fmtError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
