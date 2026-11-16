package sshutil

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// These regressions replay a delayed setup watchdog or execution watcher at
// the exact decision entry it would reach after observing runContext.Done.
// The replay happens after natural completion and the internal cleanup
// cancel, which is the schedule a delayed goroutine can legally produce.

type gateExitStatusError struct{ code int }

func (gateExitStatusError) Error() string       { return "FAKE_EXIT_STATUS_FOR_TEST_ONLY" }
func (err gateExitStatusError) ExitStatus() int { return err.code }

// gateSession behaves like an SSH session: once closed, Signal reports EOF.
type gateSession struct {
	stdout     string
	waitErr    error
	blockWait  bool
	closed     chan struct{}
	closeOnce  sync.Once
	signals    atomic.Int32
	signaled   chan struct{}
	signalOnce sync.Once
}

func newGateSession(stdout string, waitErr error) *gateSession {
	return &gateSession{stdout: stdout, waitErr: waitErr, closed: make(chan struct{}), signaled: make(chan struct{})}
}

func (*gateSession) StdinPipe() (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
func (session *gateSession) StdoutPipe() (io.Reader, error) {
	return strings.NewReader(session.stdout), nil
}
func (*gateSession) StderrPipe() (io.Reader, error) { return strings.NewReader(""), nil }
func (*gateSession) Start(string) error             { return nil }
func (session *gateSession) Wait() error {
	if session.blockWait {
		<-session.closed
	}
	return session.waitErr
}
func (session *gateSession) Signal(ssh.Signal) error {
	session.signals.Add(1)
	session.signalOnce.Do(func() { close(session.signaled) })
	select {
	case <-session.closed:
		return io.EOF
	default:
		return nil
	}
}
func (session *gateSession) Close() error {
	session.closeOnce.Do(func() { close(session.closed) })
	return nil
}

func openGateCommand(t *testing.T, runner *CommandRunner, ctx context.Context) CommandExecutionStream {
	t.Helper()
	stream, err := runner.OpenExecution(ctx, CommandSpec{Binary: "docker", Args: []string{"volume", "ls"}, MaxStdoutBytes: 1024})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	return stream
}

func TestCompatibilityLateCancellationAfterCompletionKeepsResultAndTransport(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(map[int]string{0: "exit_zero", 7: "exit_nonzero"}[code], func(t *testing.T) {
			var waitErr error
			if code != 0 {
				waitErr = gateExitStatusError{code: code}
			}
			sessions := []*gateSession{newGateSession("first\n", waitErr), newGateSession("second\n", nil)}
			var opened atomic.Int32
			runner := NewCommandRunner(func(context.Context) (CommandSession, error) {
				return sessions[opened.Add(1)-1], nil
			}, 1)
			var transportCloses atomic.Int32
			runner.transportClose = func() { transportCloses.Add(1) }

			first := openGateCommand(t, runner, context.Background())
			if output, err := io.ReadAll(first); err != nil || string(output) != "first\n" {
				t.Fatalf("first output=%q err=%v", output, err)
			}
			completion, err := first.Join()
			if err != nil || !completion.ExitCodeKnown || completion.ExitCode != code {
				t.Fatalf("first completion=%+v err=%v, want known exit %d", completion, err, code)
			}
			state := first.(*commandExecution)
			lifecycleWait(t, state.watchDone, "execution watcher exit")

			// Delayed watcher and setup watchdog act only after completion and
			// the internal cleanup cancel; both must be rejected without effect.
			state.terminate()
			state.setup.cancelTransport()
			if signals := sessions[0].signals.Load(); signals != 0 {
				t.Fatalf("completed session received %d termination signals", signals)
			}
			if closes := transportCloses.Load(); closes != 0 {
				t.Fatalf("reused transport closed %d times after natural completion", closes)
			}
			again, againErr := first.Join()
			if againErr != err || again.ExitCode != completion.ExitCode || again.ExitCodeKnown != completion.ExitCodeKnown {
				t.Fatalf("repeated join=%+v err=%v, want %+v err=%v", again, againErr, completion, err)
			}

			second := openGateCommand(t, runner, context.Background())
			if output, err := io.ReadAll(second); err != nil || string(output) != "second\n" {
				t.Fatalf("second output=%q err=%v", output, err)
			}
			if completion, err := second.Join(); err != nil || !completion.ExitCodeKnown || completion.ExitCode != 0 {
				t.Fatalf("second completion=%+v err=%v", completion, err)
			}
			if closes := transportCloses.Load(); closes != 0 {
				t.Fatalf("transport closed %d times across sequential commands", closes)
			}
		})
	}
}

func TestCompatibilityCancellationClaimedBeforeCompletionStaysCanceled(t *testing.T) {
	session := newGateSession("", nil)
	session.blockWait = true
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	var transportCloses atomic.Int32
	runner.transportClose = func() {
		transportCloses.Add(1)
		_ = session.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := openGateCommand(t, runner, ctx)
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("read before cancellation: %v", err)
	}
	// stdout reached EOF but Wait cannot finish until the transport closes, so
	// cancellation claims termination before natural completion is possible.
	cancel()
	completion, err := stream.Join()
	if !errors.Is(err, context.Canceled) || completion.ExitCodeKnown {
		t.Fatalf("completion=%+v err=%v, want canceled without exit status", completion, err)
	}
	if closes := transportCloses.Load(); closes != 1 {
		t.Fatalf("transport closes=%d, want 1", closes)
	}
	if _, againErr := stream.Join(); againErr != err {
		t.Fatalf("repeated join err=%v, want %v", againErr, err)
	}
}

func TestCompatibilitySetupCancellationClaimedBeforeStartHandoffIsNotReusable(t *testing.T) {
	session := &blockedStartCommandSession{
		fakeCommandSession: newFakeCommandSession(),
		startEntered:       make(chan struct{}),
		transportDone:      make(chan struct{}),
	}
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	var transportCloses atomic.Int32
	runner.transportClose = func() {
		transportCloses.Add(1)
		close(session.transportDone)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan CommandExecutionStream, 1)
	go func() {
		stream, err := runner.OpenExecution(ctx, CommandSpec{Binary: "docker", Args: []string{"volume", "ls"}, MaxStdoutBytes: 1024})
		if err != nil {
			t.Errorf("start returned success after transport close, got open error %v", err)
		}
		opened <- stream
	}()
	lifecycleWait(t, session.startEntered, "blocked start")
	cancel()
	var stream CommandExecutionStream
	select {
	case stream = <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked start was not released by setup cancellation")
	}
	if stream == nil {
		return
	}
	_, _ = io.ReadAll(stream)
	completion, err := stream.Join()
	if !errors.Is(err, context.Canceled) || completion.ExitCodeKnown {
		t.Fatalf("completion=%+v err=%v, want canceled after claimed setup cancellation", completion, err)
	}
	if closes := transportCloses.Load(); closes != 1 {
		t.Fatalf("transport closes=%d, want 1", closes)
	}
}

func TestCompatibilityBlockedSessionCreationUnblocksByTransportClose(t *testing.T) {
	entered, transportDone := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-transportDone
			return nil, io.EOF
		}
		return newGateSession("next\n", nil), nil
	}, 1)
	var transportCloses atomic.Int32
	runner.transportClose = func() {
		if transportCloses.Add(1) == 1 {
			close(transportDone)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := runner.OpenExecution(ctx, CommandSpec{Binary: "docker", Args: []string{"volume", "ls"}, MaxStdoutBytes: 1024})
		result <- err
	}()
	lifecycleWait(t, entered, "blocked session creation")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrCommandNotStarted) || !errors.Is(err, ErrCommandFailed) {
			t.Fatalf("blocked creation error=%v, want not-started command failure", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked session creation was not released by transport close")
	}
	if closes := transportCloses.Load(); closes != 1 {
		t.Fatalf("transport closes=%d, want 1", closes)
	}
	// The permit is released: an independent caller can still run a command.
	next := openGateCommand(t, runner, context.Background())
	if output, err := io.ReadAll(next); err != nil || string(output) != "next\n" {
		t.Fatalf("next output=%q err=%v", output, err)
	}
	if _, err := next.Join(); err != nil {
		t.Fatalf("next join: %v", err)
	}
}

func TestCompatibilityRealSSHSequentialCommandsIgnoreLateCancellation(t *testing.T) {
	client := lifecycleSSH(t, func(_ *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
		for _, step := range []struct {
			output string
			status uint32
		}{{"first\n", 0}, {"second\n", 7}} {
			channel, _, err := lifecycleAcceptExec(channels)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(channel, step.output); err != nil {
				return err
			}
			if err := lifecycleStatus(channel, step.status); err != nil {
				return err
			}
		}
		return nil
	})
	runner := NewSSHCommandRunnerWithTransportClose(client, 1)

	first := openGateCommand(t, runner, context.Background())
	if output, err := io.ReadAll(first); err != nil || string(output) != "first\n" {
		t.Fatalf("first output=%q err=%v", output, err)
	}
	completion, err := first.Join()
	if err != nil || !completion.ExitCodeKnown || completion.ExitCode != 0 || !completion.RemoteExited {
		t.Fatalf("first completion=%+v err=%v", completion, err)
	}
	state := first.(*commandExecution)
	lifecycleWait(t, state.watchDone, "execution watcher exit")
	state.terminate()
	state.setup.cancelTransport()
	if again, againErr := first.Join(); againErr != nil || again.ExitCode != 0 || !again.ExitCodeKnown {
		t.Fatalf("repeated join=%+v err=%v", again, againErr)
	}

	second := openGateCommand(t, runner, context.Background())
	if output, err := io.ReadAll(second); err != nil || string(output) != "second\n" {
		t.Fatalf("second output=%q err=%v", output, err)
	}
	completion, err = second.Join()
	if err != nil || !completion.ExitCodeKnown || completion.ExitCode != 7 || !completion.RemoteExited {
		t.Fatalf("second completion=%+v err=%v, want real exit 7 on the same transport", completion, err)
	}
}
