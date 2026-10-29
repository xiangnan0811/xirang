package sshutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// lifecycleSSH starts a real SSH transport; each handler controls the server's
// session, exit messages, signals, and disconnect independently of the runner.
func lifecycleSSH(t *testing.T, handler func(*ssh.ServerConn, <-chan ssh.NewChannel) error) *ssh.Client {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(nodeDialerTestPrivateKey(t)))
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = server.Close() }()
		go ssh.DiscardRequests(requests)
		if err := handler(server, channels); err != nil {
			serverDone <- err
			return
		}
		_ = server.Wait()
		serverDone <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialSSH(ctx, listener.Addr().String(), "FAKE_USER_FOR_TEST_ONLY", nil, ssh.FixedHostKey(signer.PublicKey()))
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = listener.Close()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("SSH server: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("SSH server did not stop")
		}
	})
	return client
}

func lifecycleAcceptExec(channels <-chan ssh.NewChannel) (ssh.Channel, <-chan *ssh.Request, error) {
	incoming, ok := <-channels
	if !ok || incoming.ChannelType() != "session" {
		return nil, nil, errors.New("missing SSH session")
	}
	channel, requests, err := incoming.Accept()
	if err != nil {
		return nil, nil, err
	}
	request, ok := <-requests
	if !ok || request.Type != "exec" {
		_ = channel.Close()
		return nil, nil, errors.New("missing SSH exec")
	}
	if err := request.Reply(true, nil); err != nil {
		_ = channel.Close()
		return nil, nil, err
	}
	return channel, requests, nil
}

func lifecycleStatus(channel ssh.Channel, status uint32) error {
	_, err := channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
	if err != nil {
		return err
	}
	return channel.Close()
}

func lifecycleSignalExit(channel ssh.Channel) error {
	return lifecycleSignalExitAs(channel, ssh.SIGTERM)
}

func lifecycleSignalExitAs(channel ssh.Channel, signal ssh.Signal) error {
	message := struct {
		Signal     string
		CoreDumped bool
		Error      string
		Language   string
	}{Signal: string(signal)}
	_, err := channel.SendRequest("exit-signal", false, ssh.Marshal(message))
	if err != nil {
		return err
	}
	return channel.Close()
}

func lifecycleWaitSignal(requests <-chan *ssh.Request, received chan<- string) error {
	for request := range requests {
		if request.Type != "signal" {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
			continue
		}
		var signal struct{ Signal string }
		if err := ssh.Unmarshal(request.Payload, &signal); err != nil {
			return err
		}
		received <- signal.Signal
		return nil
	}
	return errors.New("SSH session closed before signal")
}

func lifecycleOpen(t *testing.T, runner *CommandRunner, ctx context.Context, maxOutput int64) CommandExecutionStream {
	t.Helper()
	stream, err := runner.OpenExecution(ctx, CommandSpec{
		Binary: "rclone", Args: []string{"cat", "REMOTE_FOR_TEST_ONLY"}, MaxStdoutBytes: maxOutput,
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	return stream
}

func lifecycleReadPrefix(t *testing.T, stream CommandExecutionStream) {
	t.Helper()
	value := make([]byte, len("prefix"))
	if _, err := io.ReadFull(stream, value); err != nil || string(value) != "prefix" {
		t.Fatalf("prefix=%q read=%v", value, err)
	}
}

func lifecycleReceive(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case signal := <-ch:
		if signal != string(ssh.SIGTERM) {
			t.Fatalf("signal=%q, want TERM", signal)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TERM was not received")
	}
}

func lifecycleWait(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestGracefulJoinedTransportRealSSHRemoteExitEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		exit       string
		code       int
		remoteExit bool
	}{
		{name: "exit_zero", exit: "zero", code: 0, remoteExit: true},
		{name: "exit_nonzero", exit: "nonzero", code: 7, remoteExit: true},
		{name: "exit_signal", exit: "signal", code: 143, remoteExit: true},
		{name: "missing_exit_status", exit: "missing"},
		{name: "transport_disconnect", exit: "disconnect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := lifecycleSSH(t, func(server *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
				channel, _, err := lifecycleAcceptExec(channels)
				if err != nil {
					return err
				}
				if _, err := io.WriteString(channel, "prefix"); err != nil {
					return err
				}
				switch tc.exit {
				case "zero":
					return lifecycleStatus(channel, 0)
				case "nonzero":
					return lifecycleStatus(channel, 7)
				case "signal":
					return lifecycleSignalExit(channel)
				case "missing":
					return channel.Close()
				default:
					return server.Close()
				}
			})
			runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
			stream := lifecycleOpen(t, runner, context.Background(), 32)
			_, _ = io.ReadAll(stream)
			completion, err := stream.Join()
			if completion.RemoteExited != tc.remoteExit {
				t.Fatalf("completion=%+v err=%v, want RemoteExited=%t", completion, err, tc.remoteExit)
			}
			if tc.remoteExit {
				if err != nil || !completion.ExitCodeKnown || completion.ExitCode != tc.code {
					t.Fatalf("completion=%+v err=%v, want exit %d", completion, err, tc.code)
				}
			} else if !errors.Is(err, ErrCommandFailed) || completion.ExitCodeKnown {
				t.Fatalf("uncertain exit completion=%+v err=%v", completion, err)
			}
			encoded, marshalErr := json.Marshal(completion)
			if marshalErr != nil || strings.Contains(string(encoded), "RemoteExited") {
				t.Fatalf("serialized completion=%s err=%v", encoded, marshalErr)
			}
		})
	}
}

func TestGracefulJoinedTransportRealSSHFailuresRetainExitEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{name: "caller_cancel", want: context.Canceled},
		{name: "caller_deadline", want: context.DeadlineExceeded},
		{name: "operation_timeout", want: ErrCommandTimeout},
		{name: "output_limit", want: ErrCommandOutputLimit},
		{name: "explicit_cancel", want: ErrCommandFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan string, 1)
			client := lifecycleSSH(t, func(_ *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
				channel, requests, err := lifecycleAcceptExec(channels)
				if err != nil {
					return err
				}
				if _, err := io.WriteString(channel, "prefix-and-more"); err != nil {
					return err
				}
				if err := lifecycleWaitSignal(requests, received); err != nil {
					return err
				}
				return lifecycleSignalExit(channel)
			})
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := context.Context(parent)
			if tc.name == "caller_deadline" {
				ctx = controlledDeadlineContext{parent}
			}
			limit := int64(64)
			if tc.name == "output_limit" {
				limit = 4
			}
			runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
			var stream CommandExecutionStream
			if tc.name == "operation_timeout" {
				var err error
				stream, err = runner.OpenExecution(ctx, CommandSpec{
					Binary: "rclone", Args: []string{"cat", "REMOTE_FOR_TEST_ONLY"}, MaxStdoutBytes: limit, Timeout: 4 * time.Second,
				})
				if err != nil {
					t.Fatalf("open execution: %v", err)
				}
			} else {
				stream = lifecycleOpen(t, runner, ctx, limit)
			}
			if tc.name == "output_limit" {
				if _, err := io.ReadAll(stream); !errors.Is(err, ErrCommandOutputLimit) {
					t.Fatalf("read limit error=%v", err)
				}
			} else {
				lifecycleReadPrefix(t, stream)
				switch tc.name {
				case "explicit_cancel":
					if err := stream.Cancel(); !errors.Is(err, ErrCommandFailed) {
						t.Fatalf("cancel error=%v", err)
					}
				case "operation_timeout":
					select {
					case <-stream.(*commandExecution).runContext.Done():
					case <-time.After(8 * time.Second):
						t.Fatal("operation deadline did not expire")
					}
				default:
					cancel()
				}
			}
			completion, err := stream.Join()
			lifecycleReceive(t, received)
			if !errors.Is(err, tc.want) || !completion.RemoteExited || completion.ExitCodeKnown || len(completion.Stderr) != 0 {
				t.Fatalf("completion=%+v err=%v, want %v with remote exit evidence", completion, err, tc.want)
			}
		})
	}
}

func TestGracefulJoinedTransportRealSSHIgnoredTERMIsUncertain(t *testing.T) {
	received := make(chan string, 1)
	client := lifecycleSSH(t, func(_ *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
		channel, requests, err := lifecycleAcceptExec(channels)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(channel, "prefix-and-more"); err != nil {
			return err
		}
		if err := lifecycleWaitSignal(requests, received); err != nil {
			return err
		}
		for range requests {
		}
		return nil
	})
	runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
	stream := lifecycleOpen(t, runner, context.Background(), 64)
	lifecycleReadPrefix(t, stream)
	if err := stream.Cancel(); !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("cancel error=%v", err)
	}
	completion, err := stream.Join()
	lifecycleReceive(t, received)
	if !errors.Is(err, ErrCommandFailed) || completion.RemoteExited || completion.ExitCodeKnown {
		t.Fatalf("ignored TERM completion=%+v err=%v", completion, err)
	}
}

type lifecycleHeldSignalSession struct {
	CommandSession
	entered  chan struct{}
	release  <-chan struct{}
	returned chan struct{}
}

func (session *lifecycleHeldSignalSession) Signal(signal ssh.Signal) error {
	close(session.entered)
	<-session.release
	defer close(session.returned)
	return session.CommandSession.Signal(signal)
}

func TestGracefulJoinedTransportRealSSHJoinsBlockedSignalAfterForceClose(t *testing.T) {
	remoteClosed := make(chan struct{})
	client := lifecycleSSH(t, func(_ *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
		channel, requests, err := lifecycleAcceptExec(channels)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(channel, "prefix-and-more"); err != nil {
			return err
		}
		for range requests {
		}
		close(remoteClosed)
		return nil
	})
	runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
	factory := runner.factory
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	runner.factory = func(ctx context.Context) (CommandSession, error) {
		session, err := factory(ctx)
		if err != nil {
			return nil, err
		}
		return &lifecycleHeldSignalSession{CommandSession: session, entered: entered, release: release, returned: returned}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := lifecycleOpen(t, runner, ctx, 64)
	lifecycleReadPrefix(t, stream)
	result := make(chan error, 1)
	go func() {
		_, err := stream.Join()
		result <- err
	}()
	cancel()
	lifecycleWait(t, entered, "signal call")
	lifecycleWait(t, remoteClosed, "forced transport close")
	select {
	case err := <-result:
		t.Fatalf("join returned before the signal owner finished: %v", err)
	default:
	}
	unblock()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("join error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("join did not finish after signal returned")
	}
	lifecycleWait(t, returned, "signal owner exit")
	completion, err := stream.Join()
	if !errors.Is(err, context.Canceled) || completion.RemoteExited {
		t.Fatalf("forced close completion=%+v err=%v", completion, err)
	}
}

func TestGracefulJoinedTransportRealSSHSetupCancellationKeepsStartAmbiguous(t *testing.T) {
	for _, stage := range []string{"session", "start"} {
		t.Run(stage, func(t *testing.T) {
			entered := make(chan struct{})
			client := lifecycleSSH(t, func(server *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
				incoming, ok := <-channels
				if !ok {
					return errors.New("missing SSH session")
				}
				if stage == "session" {
					close(entered)
					_ = server.Wait()
					return nil
				}
				channel, requests, err := incoming.Accept()
				if err != nil {
					return err
				}
				defer func() { _ = channel.Close() }()
				request, ok := <-requests
				if !ok || request.Type != "exec" {
					return errors.New("missing SSH exec")
				}
				close(entered)
				_ = server.Wait()
				return nil
			})
			runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := runner.OpenExecution(ctx, CommandSpec{Binary: "rclone", MaxStdoutBytes: 64})
				result <- err
			}()
			lifecycleWait(t, entered, stage)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, ErrCommandFailed) || errors.Is(err, ErrCommandStart) != (stage == "start") {
					t.Fatalf("%s error=%v", stage, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("%s cancellation did not close and join setup", stage)
			}
		})
	}
}

type lifecycleHeldStartSession struct {
	CommandSession
	started chan struct{}
	release <-chan struct{}
}

func (session *lifecycleHeldStartSession) Start(command string) error {
	err := session.CommandSession.Start(command)
	if err == nil {
		close(session.started)
		<-session.release
	}
	return err
}

func TestGracefulJoinedTransportRealSSHStartWatchdogHandoff(t *testing.T) {
	remoteClosed := make(chan struct{})
	client := lifecycleSSH(t, func(_ *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
		_, requests, err := lifecycleAcceptExec(channels)
		if err != nil {
			return err
		}
		for range requests {
		}
		close(remoteClosed)
		return nil
	})
	runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
	factory := runner.factory
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	runner.factory = func(ctx context.Context) (CommandSession, error) {
		session, err := factory(ctx)
		if err != nil {
			return nil, err
		}
		return &lifecycleHeldStartSession{CommandSession: session, started: started, release: release}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		stream CommandExecutionStream
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		stream, err := runner.OpenExecution(ctx, CommandSpec{Binary: "rclone", MaxStdoutBytes: 64})
		result <- outcome{stream: stream, err: err}
	}()
	lifecycleWait(t, started, "acknowledged remote start")
	cancel()
	lifecycleWait(t, remoteClosed, "setup watchdog closing the transport")
	unblock()
	select {
	case got := <-result:
		if got.err != nil {
			if !errors.Is(got.err, ErrCommandStart) {
				t.Fatalf("acknowledged start reported as safely unstarted: %v", got.err)
			}
			return
		}
		if got.stream == nil {
			t.Fatal("missing execution after acknowledged start")
		}
		completion, err := got.stream.Join()
		if !errors.Is(err, context.Canceled) || completion.RemoteExited {
			t.Fatalf("watchdog handoff completion=%+v err=%v", completion, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("start handoff did not finish")
	}
}

func TestGracefulJoinedTransportRealSSHPrefixTermination(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ignoreTERM    bool
		cancel        bool
		limited       bool
		statusFailure bool
		otherSignal   bool
		want          error
		remoteExit    bool
	}{
		{name: "expected_termination", remoteExit: true},
		{name: "nonzero_after_term", statusFailure: true, want: ErrCommandFailed, remoteExit: true},
		{name: "unexpected_signal_after_term", otherSignal: true, want: ErrCommandFailed, remoteExit: true},
		{name: "ignored_term", ignoreTERM: true, want: ErrCommandFailed},
		{name: "preexisting_cancel", cancel: true, want: context.Canceled, remoteExit: true},
		{name: "preexisting_limit", limited: true, want: ErrCommandOutputLimit, remoteExit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan string, 1)
			client := lifecycleSSH(t, func(_ *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
				channel, requests, err := lifecycleAcceptExec(channels)
				if err != nil {
					return err
				}
				if _, err := io.WriteString(channel, "prefix-and-more"); err != nil {
					return err
				}
				if err := lifecycleWaitSignal(requests, received); err != nil {
					return err
				}
				if tc.ignoreTERM {
					for range requests {
					}
					return nil
				}
				if tc.statusFailure {
					return lifecycleStatus(channel, 7)
				}
				if tc.otherSignal {
					return lifecycleSignalExitAs(channel, ssh.SIGKILL)
				}
				return lifecycleSignalExit(channel)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			limit := int64(64)
			if tc.limited {
				limit = 4
			}
			runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
			stream := lifecycleOpen(t, runner, ctx, limit)
			prefix, ok := stream.(CommandPrefixExecutionStream)
			if !ok {
				t.Fatal("execution does not support JoinPrefix")
			}
			if tc.limited {
				if _, err := io.ReadAll(stream); !errors.Is(err, ErrCommandOutputLimit) {
					t.Fatalf("read limit error=%v", err)
				}
			} else {
				lifecycleReadPrefix(t, stream)
				if tc.cancel {
					cancel()
				}
			}
			completion, err := prefix.JoinPrefix()
			lifecycleReceive(t, received)
			if (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) || completion.RemoteExited != tc.remoteExit {
				t.Fatalf("prefix completion=%+v err=%v, want %v RemoteExited=%t", completion, err, tc.want, tc.remoteExit)
			}
		})
	}
}

func TestGracefulJoinedTransportRealSSHPrefixPreservesCompletedFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      uint32
		missing     bool
		naturalEOF  bool
		wantFailure bool
	}{
		{name: "natural_zero", status: 0, naturalEOF: true},
		{name: "natural_nonzero", status: 7, naturalEOF: true, wantFailure: true},
		{name: "nonzero_before_prefix", status: 7, wantFailure: true},
		{name: "missing_before_prefix", missing: true, wantFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := lifecycleSSH(t, func(_ *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
				channel, _, err := lifecycleAcceptExec(channels)
				if err != nil {
					return err
				}
				if _, err := io.WriteString(channel, "prefix-and-more"); err != nil {
					return err
				}
				if tc.missing {
					return channel.Close()
				}
				return lifecycleStatus(channel, tc.status)
			})
			runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
			stream := lifecycleOpen(t, runner, context.Background(), 64)
			prefix, ok := stream.(CommandPrefixExecutionStream)
			if !ok {
				t.Fatal("execution does not support JoinPrefix")
			}
			if tc.naturalEOF {
				if _, err := io.ReadAll(stream); err != nil {
					t.Fatalf("read to EOF: %v", err)
				}
			} else {
				lifecycleReadPrefix(t, stream)
				// Ensure Wait has observed the remote failure before prefix is requested.
				lifecycleWait(t, stream.(*commandExecution).waitDone, "remote Wait result")
			}
			completion, err := prefix.JoinPrefix()
			if tc.wantFailure && !errors.Is(err, ErrCommandFailed) {
				t.Fatalf("completed failure suppressed: completion=%+v err=%v", completion, err)
			}
			if !tc.wantFailure && (err != nil || !completion.ExitCodeKnown || completion.ExitCode != 0) {
				t.Fatalf("natural success completion=%+v err=%v", completion, err)
			}
			if completion.RemoteExited == tc.missing {
				t.Fatalf("completion=%+v err=%v, missing status=%t", completion, err, tc.missing)
			}
		})
	}
}

func TestCommandPrefixExecutionPreservesIOFailure(t *testing.T) {
	session := newExecutionFakeCommandSession(nil, nil)
	session.stdout.terminalErr = errors.New("FAKE_STDOUT_FAILURE_FOR_TEST_ONLY")
	runner := NewCommandRunner(func(context.Context) (CommandSession, error) { return session, nil }, 1)
	stream := lifecycleOpen(t, runner, context.Background(), 64)
	prefix, ok := stream.(CommandPrefixExecutionStream)
	if !ok {
		t.Fatal("execution does not support JoinPrefix")
	}
	if _, err := io.ReadAll(stream); !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("read error=%v", err)
	}
	completion, err := prefix.JoinPrefix()
	if !errors.Is(err, ErrCommandFailed) || !completion.RemoteExited || completion.ExitCodeKnown || strings.Contains(fmt.Sprint(err), "FAKE_STDOUT") {
		t.Fatalf("I/O failure completion=%+v err=%v", completion, err)
	}
}

type lifecycleCountingSignalSession struct {
	CommandSession
	signals chan<- struct{}
}

func (session *lifecycleCountingSignalSession) Signal(signal ssh.Signal) error {
	session.signals <- struct{}{}
	return session.CommandSession.Signal(signal)
}

func TestGracefulJoinedTransportRealSSHConcurrentCancelJoinsOnce(t *testing.T) {
	received := make(chan string, 1)
	client := lifecycleSSH(t, func(_ *ssh.ServerConn, channels <-chan ssh.NewChannel) error {
		channel, requests, err := lifecycleAcceptExec(channels)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(channel, "prefix-and-more"); err != nil {
			return err
		}
		if err := lifecycleWaitSignal(requests, received); err != nil {
			return err
		}
		return lifecycleSignalExit(channel)
	})
	runner := NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1)
	factory := runner.factory
	signals := make(chan struct{}, 8)
	runner.factory = func(ctx context.Context) (CommandSession, error) {
		session, err := factory(ctx)
		if err != nil {
			return nil, err
		}
		return &lifecycleCountingSignalSession{CommandSession: session, signals: signals}, nil
	}
	stream := lifecycleOpen(t, runner, context.Background(), 64)
	lifecycleReadPrefix(t, stream)
	start := make(chan struct{})
	results := make(chan error, 6)
	for index := range cap(results) {
		go func(cancel bool) {
			<-start
			if cancel {
				results <- stream.Cancel()
				return
			}
			_, err := stream.Join()
			results <- err
		}(index%2 == 0)
	}
	close(start)
	for range cap(results) {
		select {
		case err := <-results:
			if !errors.Is(err, ErrCommandFailed) {
				t.Fatalf("concurrent finalization error=%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent finalization did not finish")
		}
	}
	lifecycleReceive(t, received)
	completion, err := stream.Join()
	if !errors.Is(err, ErrCommandFailed) || !completion.RemoteExited || len(signals) != 1 {
		t.Fatalf("completion=%+v err=%v signal calls=%d", completion, err, len(signals))
	}
}
