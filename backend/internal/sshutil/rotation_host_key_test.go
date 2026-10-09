package sshutil

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func prepareRotationKnownHosts(t *testing.T) string {
	t.Helper()
	path := setupStrictKnownHosts(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create known_hosts directory: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create known_hosts file: %v", err)
	}
	return path
}

func writeRotationKnownHost(t *testing.T, path, address string, key ssh.PublicKey) []byte {
	t.Helper()
	content := []byte(knownhosts.Line([]string{knownhosts.Normalize(address)}, key) + "\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write known_hosts fixture: %v", err)
	}
	return content
}

func rotationHostRemote(t *testing.T, address string) net.Addr {
	t.Helper()
	remote, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		t.Fatalf("resolve host test remote address: %v", err)
	}
	return remote
}

func TestResolveSSHRotationHostKeyCallbackIsStrictAndReadOnly(t *testing.T) {
	server := startHostKeyTestServer(t)
	path := prepareRotationKnownHosts(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read empty known_hosts fixture: %v", err)
	}

	for _, tc := range []struct {
		name   string
		strict string
	}{
		{name: "strict", strict: "true"},
		{name: "permissive global setting", strict: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", tc.strict)
			t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")

			callback, err := ResolveSSHRotationHostKeyCallback()
			if err != nil {
				t.Fatalf("resolve rotation callback: %v", err)
			}
			err = callback(server.address, rotationHostRemote(t, server.address), server.hostKey)
			var hostKeyErr *HostKeyError
			if !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyUnknown {
				t.Fatalf("unknown host key must be rejected even with permissive globals, got %v", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read known_hosts after callback: %v", readErr)
			}
			if string(after) != string(before) {
				t.Fatalf("rotation callback must not modify known_hosts: before=%q after=%q", before, after)
			}
		})
	}
	if attempts := server.authAttempts.Load(); attempts != 0 {
		t.Fatalf("callback-only validation must not contact SSH authentication, got %d attempts", attempts)
	}
}

func TestResolveSSHRotationHostKeyCallbackRejectsMismatchWithoutWriting(t *testing.T) {
	server := startHostKeyTestServer(t)
	path := prepareRotationKnownHosts(t)
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate conflicting host key: %v", err)
	}
	otherKey, err := ssh.NewPublicKey(otherPublic)
	if err != nil {
		t.Fatalf("build conflicting host key: %v", err)
	}
	before := writeRotationKnownHost(t, path, server.address, otherKey)
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")

	callback, err := ResolveSSHRotationHostKeyCallback()
	if err != nil {
		t.Fatalf("resolve rotation callback: %v", err)
	}
	err = callback(server.address, rotationHostRemote(t, server.address), server.hostKey)
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch {
		t.Fatalf("changed host key must be a mismatch, got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts after mismatch: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("mismatch callback must not modify known_hosts: before=%q after=%q", before, after)
	}
}

func TestResolveSSHRotationHostKeyCallbackAcceptsOnlyExistingEntry(t *testing.T) {
	server := startHostKeyTestServer(t)
	path := prepareRotationKnownHosts(t)
	before := writeRotationKnownHost(t, path, server.address, server.hostKey)
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")

	callback, err := ResolveSSHRotationHostKeyCallback()
	if err != nil {
		t.Fatalf("resolve rotation callback: %v", err)
	}
	if err := callback(server.address, rotationHostRemote(t, server.address), server.hostKey); err != nil {
		t.Fatalf("existing trusted host key should pass: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts after trusted callback: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("trusted callback must not rewrite known_hosts")
	}
}

func TestDialSSHForRotationPrefersRecordedHostKeyWhenGlobalStrictDisabled(t *testing.T) {
	server := startHostKeyTestServerWithKeys(t, true)
	path := prepareRotationKnownHosts(t)
	before := writeRotationKnownHost(t, path, server.address, server.hostKey)
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")

	callback, err := ResolveSSHRotationHostKeyCallback()
	if err != nil {
		t.Fatalf("resolve rotation callback: %v", err)
	}
	client, err := DialSSHForRotation(
		context.Background(),
		server.address,
		"tester",
		[]ssh.AuthMethod{ssh.Password("secret")},
		callback,
	)
	if err != nil {
		t.Fatalf("rotation dial should negotiate the recorded Ed25519 key: %v", err)
	}
	_ = client.Close()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts after rotation dial: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("rotation dial must not modify known_hosts: before=%q after=%q", before, after)
	}
	if attempts := server.authAttempts.Load(); attempts == 0 {
		t.Fatal("rotation dial should reach user authentication after trusted host verification")
	}
}

func TestDialSSHForRotationRejectsChangedRecordedHostKeyBeforeAuthentication(t *testing.T) {
	server := startHostKeyTestServerWithKeys(t, true)
	path := prepareRotationKnownHosts(t)
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate stale host key: %v", err)
	}
	staleKey, err := ssh.NewPublicKey(otherPublic)
	if err != nil {
		t.Fatalf("build stale host key: %v", err)
	}
	before := writeRotationKnownHost(t, path, server.address, staleKey)
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")

	callback, err := ResolveSSHRotationHostKeyCallback()
	if err != nil {
		t.Fatalf("resolve rotation callback: %v", err)
	}
	_, err = DialSSHForRotation(
		context.Background(),
		server.address,
		"tester",
		[]ssh.AuthMethod{ssh.Password("secret")},
		callback,
	)
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch {
		t.Fatalf("changed recorded host key must be rejected as mismatch, got %v", err)
	}
	if attempts := server.authAttempts.Load(); attempts != 0 {
		t.Fatalf("host key mismatch must be rejected before authentication, got %d attempts", attempts)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts after mismatch: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("mismatch must not modify known_hosts: before=%q after=%q", before, after)
	}
}

func TestResolveSSHRotationHostKeyCallbackDoesNotCreateMissingFile(t *testing.T) {
	t.Setenv("SSH_KNOWN_HOSTS_PATH", filepath.Join(t.TempDir(), "missing", "known_hosts"))
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")

	if _, err := ResolveSSHRotationHostKeyCallback(); err == nil {
		t.Fatal("missing known_hosts must make rotation trust unavailable")
	}
	if _, err := os.Stat(os.Getenv("SSH_KNOWN_HOSTS_PATH")); !os.IsNotExist(err) {
		t.Fatalf("rotation callback must not create known_hosts, stat error=%v", err)
	}
}
