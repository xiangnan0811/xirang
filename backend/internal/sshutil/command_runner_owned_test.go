package sshutil

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type ownedOutputSession struct {
	*fakeCommandSession
	output io.Reader
}

func (s *ownedOutputSession) StdoutPipe() (io.Reader, error) { return s.output, nil }

type ownedFailureReader struct{}

func (ownedFailureReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestJoinedOwnedTransportJoinsOutputFailures(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "limit"}[limited], func(t *testing.T) {
			output := io.Reader(io.MultiReader(strings.NewReader("12"), ownedFailureReader{}))
			if limited {
				output = strings.NewReader("12345")
			}
			session := &ownedOutputSession{fakeCommandSession: newFakeCommandSession(), output: output}
			session.blockWait = true
			runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
			runner.joinOwnedTransport = true
			transportClosed := make(chan struct{})
			runner.transportClose = func() { close(transportClosed); _ = session.Close() }
			stream, err := runner.OpenRawExecution(context.Background(), RawCommandSpec{Command: "FAKE_COMMAND_FOR_TEST_ONLY", MaxStdoutBytes: 4})
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.ReadAll(stream)
			_, joinErr := stream.Join()
			if readErr == nil || joinErr == nil {
				t.Fatalf("read=%v join=%v", readErr, joinErr)
			}
			if limited && (!errors.Is(readErr, ErrCommandOutputLimit) || !errors.Is(joinErr, ErrCommandOutputLimit)) {
				t.Fatalf("read=%v join=%v", readErr, joinErr)
			}
			state := stream.(*commandExecution)
			for _, done := range []<-chan struct{}{transportClosed, session.closed, state.allDone, state.watchDone} {
				select {
				case <-done:
				default:
					t.Fatal("failure returned with unfinished owner")
				}
			}
		})
	}
}

func TestJoinedOwnedTransportCancelsAndJoinsBlockedRead(t *testing.T) {
	session := newOwnedBlockingCommandSession()
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	runner.joinOwnedTransport = true
	runner.transportClose = func() {
		session.once.Do(func() {
			close(session.closed)
			_ = session.stdout.Close()
			close(session.closeRelease)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := runner.OpenRawExecution(ctx, RawCommandSpec{Command: "FAKE_COMMAND_FOR_TEST_ONLY", MaxStdoutBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _ = io.ReadAll(stream); _, err := stream.Join(); done <- err }()
	<-session.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read/wait did not join")
	}
	select {
	case <-session.closeEntered:
	default:
		t.Fatal("session close not joined")
	}
	select {
	case <-session.signalEntered:
		t.Fatal("strict ownership must close transport before any blocking signal")
	default:
	}
	state := stream.(*commandExecution)
	for _, joined := range []<-chan struct{}{state.waitDone, state.stderrDone, state.stdinDone, state.allDone, state.watchDone} {
		select {
		case <-joined:
		default:
			t.Fatal("unfinished owner after Join")
		}
	}
}

func TestJoinedOwnedTransportUnblocksSessionCreationAndStart(t *testing.T) {
	for _, stage := range []string{"session", "start"} {
		t.Run(stage, func(t *testing.T) {
			entered, transportDone := make(chan struct{}), make(chan struct{})
			session := &blockedStartCommandSession{fakeCommandSession: newFakeCommandSession(), startEntered: entered, transportDone: transportDone, startErr: io.EOF}
			runner := NewCommandRunner(func(context.Context) (CommandSession, error) {
				if stage == "session" {
					close(entered)
					<-transportDone
					return nil, io.EOF
				}
				return session, nil
			}, 1)
			runner.joinOwnedTransport = true
			runner.transportClose = func() { close(transportDone) }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := runner.OpenRawExecution(ctx, RawCommandSpec{Command: "FAKE_COMMAND_FOR_TEST_ONLY"})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("operation did not enter")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected failure")
				}
			case <-time.After(time.Second):
				t.Fatal("operation not joined")
			}
			if stage == "start" {
				select {
				case <-session.closed:
				default:
					t.Fatal("session close not joined")
				}
			}
		})
	}
}

func TestJoinedOwnedTransportDeadlineUnblocksRemoteWait(t *testing.T) {
	session := newFakeCommandSession()
	session.blockWait = true
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	runner.joinOwnedTransport = true
	runner.transportClose = func() { _ = session.Close() }
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := controlledDeadlineContext{parent}
	stream, err := runner.OpenRawExecution(ctx, RawCommandSpec{Command: "FAKE_COMMAND_FOR_TEST_ONLY"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatal(err)
	}
	// Expire only after stdout completed, so CPU scheduling cannot move the
	// deadline into session setup and change which boundary this test covers.
	cancel()
	_, err = stream.Join()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	select {
	case <-stream.(*commandExecution).waitDone:
	default:
		t.Fatal("wait not joined")
	}
}

type controlledDeadlineContext struct{ context.Context }

func (ctx controlledDeadlineContext) Err() error {
	if ctx.Context.Err() != nil {
		return context.DeadlineExceeded
	}
	return nil
}
