//go:build linux

package updater

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalUpdaterListenerAuthenticatesExactPeerCredentials(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "updater.sock")
	listener, err := ListenLocalUpdater(LocalUpdaterTransportConfig{
		SocketPath: socket, ExpectedPeerUID: uint32(os.Geteuid()), ExpectedPeerGID: uint32(os.Getegid()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o660 {
		t.Fatalf("socket info=%v err=%v", info, err)
	}

	accepted := make(chan struct {
		connection net.Conn
		identity   UpdaterTransportIdentity
		err        error
	}, 1)
	go func() {
		connection, identity, err := listener.AcceptIdentity(context.Background())
		accepted <- struct {
			connection net.Conn
			identity   UpdaterTransportIdentity
			err        error
		}{connection, identity, err}
	}()
	client, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	result := <-accepted
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer func() { _ = result.connection.Close() }()
	if result.identity.PeerUID != uint32(os.Geteuid()) || result.identity.PeerGID != uint32(os.Getegid()) ||
		result.identity.PeerPID <= 0 || len(result.identity.Fingerprint) != 64 {
		t.Fatalf("unexpected peer identity: %+v", result.identity)
	}
}

func TestLocalUpdaterListenerRejectsWrongPeerAndUnsafeSocketBeforeDecode(t *testing.T) {
	t.Run("wrong UID", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(root, "updater.sock")
		listener, err := ListenLocalUpdater(LocalUpdaterTransportConfig{
			SocketPath: socket, ExpectedPeerUID: uint32(os.Geteuid() + 1), ExpectedPeerGID: uint32(os.Getegid()),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		accepted := make(chan error, 1)
		go func() {
			connection, _, err := listener.AcceptIdentity(context.Background())
			if connection != nil {
				_ = connection.Close()
			}
			accepted <- err
		}()
		connection, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.Close()
		if err := <-accepted; !errors.Is(err, ErrUpdaterUnauthenticated) {
			t.Fatalf("wrong UID error=%v", err)
		}
	})

	t.Run("unsafe mode", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(root, "updater.sock")
		listener, err := ListenLocalUpdater(LocalUpdaterTransportConfig{
			SocketPath: socket, ExpectedPeerUID: uint32(os.Geteuid()), ExpectedPeerGID: uint32(os.Getegid()),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		if err := os.Chmod(socket, 0o666); err != nil {
			t.Fatal(err)
		}
		if connection, _, err := listener.AcceptIdentity(context.Background()); connection != nil || !errors.Is(err, ErrTransportUnsafe) {
			t.Fatalf("unsafe socket connection=%v error=%v", connection, err)
		}
	})
}

func TestUpdaterIdentityCannotBeForgedByPlainOrWorkerStyleConnection(t *testing.T) {
	left, right := net.Pipe()
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	if _, ok := UpdaterIdentityFromConn(left); ok {
		t.Fatal("plain connection received updater identity")
	}
	worker := &workerStyleIdentityConn{Conn: left}
	if _, ok := UpdaterIdentityFromConn(worker); ok {
		t.Fatal("Worker-style identity received updater identity")
	}
}

type workerStyleIdentityConn struct{ net.Conn }

func (*workerStyleIdentityConn) WorkerIdentity() string { return "worker" }
