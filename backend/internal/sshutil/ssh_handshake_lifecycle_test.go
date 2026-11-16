package sshutil

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// signalPipeWrite proves that the real SSH handshake entered its blocking
// transport write before the test cancels the context.
type signalPipeWrite struct {
	net.Conn
	entered   chan struct{}
	closed    chan struct{}
	writeOnce sync.Once
	closeOnce sync.Once
}

func (conn *signalPipeWrite) Write(value []byte) (int, error) {
	conn.writeOnce.Do(func() { close(conn.entered) })
	return conn.Conn.Write(value)
}

func (conn *signalPipeWrite) Close() error {
	conn.closeOnce.Do(func() { close(conn.closed) })
	return conn.Conn.Close()
}

func TestHandshakeCancellationAfterEnteringClosesTransport(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	wrapped := &signalPipeWrite{Conn: client, entered: make(chan struct{}), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		conn     ssh.Conn
		channels <-chan ssh.NewChannel
		requests <-chan *ssh.Request
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		conn, channels, requests, err := newClientConnWithContext(ctx, wrapped, "example.invalid:22", &ssh.ClientConfig{
			User: "FAKE_USER_FOR_TEST_ONLY", HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
		result <- outcome{conn, channels, requests, err}
	}()
	lifecycleWait(t, wrapped.entered, "SSH handshake transport write")
	cancel()
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) || got.conn != nil || got.channels != nil || got.requests != nil {
			t.Fatalf("canceled handshake result=%+v, want nil connection/channels and context.Canceled", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not unblock SSH handshake")
	}
	lifecycleWait(t, wrapped.closed, "canceled handshake transport close")
}

// heldDoneContext delays only the watchdog's Done call. The handshake itself
// proceeds so cancellation can be observed by the handoff check first.
type heldDoneContext struct {
	context.Context
	entered  chan struct{}
	release  <-chan struct{}
	returned chan struct{}
}

func (ctx *heldDoneContext) Done() <-chan struct{} {
	close(ctx.entered)
	<-ctx.release
	close(ctx.returned)
	return ctx.Context.Done()
}

func TestHandshakeCanceledBeforeHandoffCheckClosesUnpublishedConnection(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, channels, requests, err := ssh.NewServerConn(server, serverConfig)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		go func() {
			for channel := range channels {
				_ = channel.Reject(ssh.Prohibited, "no sessions")
			}
		}()
		_ = conn.Wait()
	}()
	callbackEntered, callbackRelease := make(chan struct{}), make(chan struct{})
	doneEntered, doneRelease, doneReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var callbackOnce, doneOnce sync.Once
	unblockCallback := func() { callbackOnce.Do(func() { close(callbackRelease) }) }
	unblockDone := func() { doneOnce.Do(func() { close(doneRelease) }) }
	t.Cleanup(func() {
		unblockCallback()
		unblockDone()
		_ = server.Close()
		lifecycleWait(t, serverDone, "SSH server cleanup")
	})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &heldDoneContext{Context: parent, entered: doneEntered, release: doneRelease, returned: doneReturned}
	type outcome struct {
		conn     ssh.Conn
		channels <-chan ssh.NewChannel
		requests <-chan *ssh.Request
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		conn, channels, requests, err := newClientConnWithContext(ctx, client, "example.invalid:22", &ssh.ClientConfig{
			User:            "FAKE_USER_FOR_TEST_ONLY",
			HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { close(callbackEntered); <-callbackRelease; return nil },
		})
		result <- outcome{conn, channels, requests, err}
	}()
	lifecycleWait(t, doneEntered, "handshake watchdog before cancellation decision")
	lifecycleWait(t, callbackEntered, "host key callback")
	cancel()
	unblockCallback()
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) || got.conn != nil || got.channels != nil || got.requests != nil {
			t.Fatalf("canceled handoff result=%+v, want nil outputs and canceled", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled handoff did not return")
	}
	lifecycleWait(t, serverDone, "unpublished connection close")
	unblockDone()
	lifecycleWait(t, doneReturned, "delayed watchdog context")
}

// heldCloseConn allows cancellation to claim the connection while the actual
// Close is blocked. The underlying handshake can still finish successfully.
type heldCloseConn struct {
	net.Conn
	entered    chan struct{}
	release    <-chan struct{}
	returned   chan struct{}
	once       sync.Once
	returnOnce sync.Once
}

func (conn *heldCloseConn) Close() error {
	conn.once.Do(func() { close(conn.entered) })
	<-conn.release
	err := conn.Conn.Close()
	conn.returnOnce.Do(func() { close(conn.returned) })
	return err
}

func TestHandshakeCancellationClaimedBeforeHandoffDoesNotWaitForCloser(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer func() { _ = server.Close() }()
		conn, channels, requests, err := ssh.NewServerConn(server, serverConfig)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		go func() {
			for channel := range channels {
				_ = channel.Reject(ssh.Prohibited, "no sessions")
			}
		}()
		_ = conn.Wait()
	}()

	closeEntered, closeRelease, closeReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	callbackEntered, callbackRelease := make(chan struct{}), make(chan struct{})
	var releaseClose, releaseCallback sync.Once
	unblockClose := func() { releaseClose.Do(func() { close(closeRelease) }) }
	unblockCallback := func() { releaseCallback.Do(func() { close(callbackRelease) }) }
	t.Cleanup(func() {
		unblockCallback()
		unblockClose()
		_ = server.Close()
		lifecycleWait(t, serverDone, "SSH server cleanup")
	})
	wrapped := &heldCloseConn{Conn: client, entered: closeEntered, release: closeRelease, returned: closeReturned}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		conn     ssh.Conn
		channels <-chan ssh.NewChannel
		requests <-chan *ssh.Request
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		conn, channels, requests, err := newClientConnWithContext(ctx, wrapped, "example.invalid:22", &ssh.ClientConfig{
			User:            "FAKE_USER_FOR_TEST_ONLY",
			HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { close(callbackEntered); <-callbackRelease; return nil },
		})
		result <- outcome{conn, channels, requests, err}
	}()
	lifecycleWait(t, callbackEntered, "host key callback")
	cancel()
	lifecycleWait(t, closeEntered, "claimed cancellation close")
	unblockCallback()
	select {
	case got := <-result:
		if !errors.Is(got.err, context.Canceled) || got.conn != nil || got.channels != nil || got.requests != nil {
			t.Fatalf("handoff after claimed cancellation=%+v, want nil result and canceled", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handoff blocked on cancellation-owned Close")
	}
	select {
	case <-closeReturned:
		t.Fatal("Close returned before release")
	default:
	}
	unblockClose()
	lifecycleWait(t, closeReturned, "claimed cancellation close return")
}

func TestHandshakeGateLateCancellationDoesNotCloseCompletedClient(t *testing.T) {
	for _, rotation := range []bool{false, true} {
		name := "normal"
		if rotation {
			name = "rotation"
		}
		t.Run(name, func(t *testing.T) {
			_, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := ssh.NewSignerFromKey(privateKey)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			serverDone := make(chan error, 1)
			go func() {
				raw, err := listener.Accept()
				if err != nil {
					serverDone <- err
					return
				}
				defer func() { _ = raw.Close() }()
				config := &ssh.ServerConfig{NoClientAuth: true}
				config.AddHostKey(signer)
				server, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					serverDone <- err
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				for i := range 2 {
					channel, _, err := lifecycleAcceptExec(channels)
					if err != nil {
						serverDone <- err
						return
					}
					if _, err = io.WriteString(channel, fmt.Sprintf("result-%d\n", i)); err != nil {
						serverDone <- err
						return
					}
					if err = lifecycleStatus(channel, uint32(i*7)); err != nil {
						serverDone <- err
						return
					}
				}
				_ = server.Wait()
				serverDone <- nil
			}()
			var client *ssh.Client
			t.Cleanup(func() {
				if client != nil {
					_ = client.Close()
				}
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
			path := prepareRotationKnownHosts(t)
			before := writeRotationKnownHost(t, path, listener.Addr().String(), signer.PublicKey())
			var callback ssh.HostKeyCallback
			if rotation {
				callback, err = ResolveSSHRotationHostKeyCallback()
			} else {
				callback, err = ResolveSSHHostKeyCallback()
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if rotation {
				client, err = DialSSHForRotation(ctx, listener.Addr().String(), "FAKE_USER_FOR_TEST_ONLY", nil, callback)
			} else {
				client, err = DialSSH(ctx, listener.Addr().String(), "FAKE_USER_FOR_TEST_ONLY", nil, callback)
			}
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			first, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			output, err := first.Output("first")
			_ = first.Close()
			if err != nil || string(output) != "result-0\n" {
				t.Fatalf("first output=%q err=%v", output, err)
			}
			gate := &setupCancellation{transportClose: func() { _ = client.Close() }}
			if !gate.disarm() {
				t.Fatal("handoff did not win")
			}
			cancel()
			gate.cancelTransport() // replay a delayed cancellation at the production decision entry
			second, err := client.NewSession()
			if err != nil {
				t.Fatalf("second session after dial cancel: %v", err)
			}
			output, err = second.Output("second")
			_ = second.Close()
			var exit *ssh.ExitError
			if !errors.As(err, &exit) || exit.ExitStatus() != 7 || string(output) != "result-1\n" {
				t.Fatalf("second output=%q err=%v, want exit 7", output, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("known_hosts changed during dial")
			}
		})
	}
}
