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
	return nil
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
