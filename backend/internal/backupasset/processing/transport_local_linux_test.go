//go:build linux

package processing

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalTransportDerivesSameUIDPeerAndCreatesPrivateSocket(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(parent, "asset-worker.sock")
	listener, err := ListenLocalWorker(LocalTransportConfig{SocketPath: socket})
	if err != nil {
		t.Fatalf("ListenLocalWorker: %v", err)
	}
	defer func() { _ = listener.Close() }()
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode=%v err=%v", info, err)
	}
	type accepted struct {
		connection net.Conn
		identity   WorkerTransportIdentity
		err        error
	}
	result := make(chan accepted, 1)
	go func() {
		connection, identity, acceptErr := listener.AcceptIdentity(context.Background())
		result <- accepted{connection, identity, acceptErr}
	}()
	client, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	acceptedConnection := <-result
	if acceptedConnection.err != nil {
		t.Fatal(acceptedConnection.err)
	}
	defer func() { _ = acceptedConnection.connection.Close() }()
	if acceptedConnection.identity.Kind != WorkerTransportLocal || acceptedConnection.identity.PeerUID != uint32(os.Geteuid()) ||
		acceptedConnection.identity.Fingerprint == "" {
		t.Fatalf("local identity not transport-derived: %+v", acceptedConnection.identity)
	}
	payload, err := json.Marshal(acceptedConnection.identity)
	if err != nil || string(payload) != "{}" {
		t.Fatalf("private transport identity serialized: %s err=%v", payload, err)
	}
}

func TestLocalTransportRejectsUnsafeParentAndStaleNonSocket(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenLocalWorker(LocalTransportConfig{SocketPath: filepath.Join(parent, "worker.sock")}); !errors.Is(err, ErrWorkerTransportUnsafe) {
		t.Fatalf("world-readable parent got %v", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(parent, "worker.sock")
	if err := os.WriteFile(stale, []byte("do-not-remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenLocalWorker(LocalTransportConfig{SocketPath: stale}); !errors.Is(err, ErrWorkerTransportUnsafe) {
		t.Fatalf("stale regular file got %v", err)
	}
	if payload, err := os.ReadFile(stale); err != nil || string(payload) != "do-not-remove" {
		t.Fatalf("unsafe stale path was removed: payload=%q err=%v", payload, err)
	}
}

func TestLocalWorkerListenerImplementsAuthenticatedNetListener(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := ListenLocalWorker(LocalTransportConfig{SocketPath: filepath.Join(parent, "worker.sock")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	result := make(chan net.Conn, 1)
	errs := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			errs <- acceptErr
			return
		}
		result <- connection
	}()
	client, err := net.DialTimeout("unix", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	select {
	case err := <-errs:
		t.Fatal(err)
	case connection := <-result:
		defer func() { _ = connection.Close() }()
		identity, ok := WorkerIdentityFromConn(connection)
		if !ok || identity.Kind != WorkerTransportLocal || identity.PeerUID != uint32(os.Geteuid()) || identity.Fingerprint == "" {
			t.Fatalf("accepted net.Conn lost peer identity: identity=%+v ok=%v", identity, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated listener accept timed out")
	}
}
