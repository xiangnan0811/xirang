package sshutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type hostKeyTestServer struct {
	address string
	// hostKey is the Ed25519 key; ecdsaKey is set only for multi-key servers,
	// where Go's default negotiation would pick ECDSA.
	hostKey      ssh.PublicKey
	ecdsaKey     ssh.PublicKey
	authAttempts atomic.Int32
}

func startHostKeyTestServer(t *testing.T) *hostKeyTestServer {
	return startHostKeyTestServerWithKeys(t, false)
}

func startHostKeyTestServerWithKeys(t *testing.T, withECDSA bool) *hostKeyTestServer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &hostKeyTestServer{address: listener.Addr().String(), hostKey: signer.PublicKey()}
	var ecdsaSigner ssh.Signer
	if withECDSA {
		ecdsaPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if ecdsaSigner, err = ssh.NewSignerFromKey(ecdsaPrivateKey); err != nil {
			t.Fatal(err)
		}
		server.ecdsaKey = ecdsaSigner.PublicKey()
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			server.authAttempts.Add(1)
			if string(password) == "secret" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		KeyboardInteractiveCallback: func(ssh.ConnMetadata, ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			server.authAttempts.Add(1)
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			server.authAttempts.Add(1)
			return nil, errors.New("denied")
		},
	}
	config.AddHostKey(signer)
	if ecdsaSigner != nil {
		config.AddHostKey(ecdsaSigner)
	}
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close() //nolint:errcheck
				sshConn, channels, requests, handshakeErr := ssh.NewServerConn(conn, config)
				if handshakeErr != nil {
					return
				}
				defer sshConn.Close() //nolint:errcheck
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(ssh.Prohibited, "test server")
				}
			}()
		}
	}()
	return server
}

func setupStrictKnownHosts(t *testing.T) string {
	t.Helper()
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	path := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", path)
	return path
}

func dialHostKeyTestServer(t *testing.T, server *hostKeyTestServer) error {
	t.Helper()
	callback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	client, err := DialSSH(context.Background(), server.address, "tester", []ssh.AuthMethod{ssh.Password("secret")}, callback)
	if err == nil {
		_ = client.Close()
	}
	return err
}

func TestTrustNewHostKeyAppendsConfirmedKeyWithoutAuthenticating(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	server := startHostKeyTestServer(t)

	dialErr := dialHostKeyTestServer(t, server)
	var hostKeyErr *HostKeyError
	if !errors.As(dialErr, &hostKeyErr) || hostKeyErr.Kind != HostKeyUnknown {
		t.Fatalf("dial before trust must fail with unknown HostKeyError, got %v", dialErr)
	}
	fingerprint := hostKeyErr.Fingerprint()
	if fingerprint != ssh.FingerprintSHA256(server.hostKey) {
		t.Fatalf("fingerprint=%q want %q", fingerprint, ssh.FingerprintSHA256(server.hostKey))
	}

	result, err := TrustNewHostKey(context.Background(), server.address, "  "+fingerprint+"\n")
	if err != nil {
		t.Fatalf("trust: %v", err)
	}
	if result.AlreadyTrusted || result.Fingerprint != fingerprint || result.Algorithm != server.hostKey.Type() {
		t.Fatalf("unexpected result %+v", result)
	}
	if attempts := server.authAttempts.Load(); attempts != 0 {
		t.Fatalf("trust probe must not attempt authentication, got %d attempts", attempts)
	}

	if err := dialHostKeyTestServer(t, server); err != nil {
		t.Fatalf("dial after trust: %v", err)
	}

	result, err = TrustNewHostKey(context.Background(), server.address, fingerprint)
	if err != nil || !result.AlreadyTrusted {
		t.Fatalf("repeat trust must report already trusted: result=%+v err=%v", result, err)
	}
	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(content)), "\n") + 1; lines != 1 {
		t.Fatalf("known_hosts must contain exactly one entry, got %d: %s", lines, content)
	}
}

func TestTrustNewHostKeyRejectsChangedFingerprintAndMismatch(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	server := startHostKeyTestServer(t)

	if _, err := TrustNewHostKey(context.Background(), server.address, "SHA256:AAAA"); !errors.Is(err, ErrHostKeyFingerprintChanged) {
		t.Fatalf("wrong fingerprint must be rejected, got %v", err)
	}
	if content, _ := os.ReadFile(knownHostsPath); len(content) != 0 {
		t.Fatalf("known_hosts must stay unchanged, got %q", content)
	}

	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := ssh.NewPublicKey(otherPublic)
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendKnownHost(knownHostsPath, server.address, otherKey); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}

	dialErr := dialHostKeyTestServer(t, server)
	var hostKeyErr *HostKeyError
	if !errors.As(dialErr, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch {
		t.Fatalf("dial with conflicting known_hosts must fail with mismatch, got %v", dialErr)
	}
	_, err = TrustNewHostKey(context.Background(), server.address, ssh.FingerprintSHA256(server.hostKey))
	if !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch {
		t.Fatalf("trust with conflicting known_hosts must fail with mismatch, got %v", err)
	}
	after, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("known_hosts changed on mismatch:\nbefore=%q\nafter=%q", before, after)
	}
	if attempts := server.authAttempts.Load(); attempts != 0 {
		t.Fatalf("no authentication may reach an untrusted server, got %d", attempts)
	}
}

func TestTrustNewHostKeyRequiresStrictCheckingAndReachableHost(t *testing.T) {
	setupStrictKnownHosts(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddress := listener.Addr().String()
	_ = listener.Close()
	if _, err := TrustNewHostKey(context.Background(), closedAddress, "SHA256:AAAA"); !errors.Is(err, ErrHostKeyUnreachable) {
		t.Fatalf("closed port must be unreachable, got %v", err)
	}

	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	if _, err := TrustNewHostKey(context.Background(), closedAddress, "SHA256:AAAA"); !errors.Is(err, ErrHostKeyCheckingDisabled) {
		t.Fatalf("disabled strict checking must be reported, got %v", err)
	}
}

func TestAutoAcceptCallbackWithStaleSnapshotRejectsConflictingKey(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")
	// Both callbacks load known_hosts while it is still empty.
	first, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.30"), Port: 22}
	if err := first("race.example.test:22", remote, newTestPublicKey(t)); err != nil {
		t.Fatalf("first auto-accept: %v", err)
	}
	err = stale("race.example.test:22", remote, newTestPublicKey(t))
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch {
		t.Fatalf("stale snapshot must not record a second key for the host, got %v", err)
	}
	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(content)), "\n") + 1; lines != 1 {
		t.Fatalf("known_hosts must keep exactly one key for the host, got %d:\n%s", lines, content)
	}
}

func TestRecordNewKnownHostConcurrentConflictingKeysRecordOnlyOne(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	if err := ensureKnownHostsFile(knownHostsPath); err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.31"), Port: 22}
	const writers = 8
	keys := make([]ssh.PublicKey, writers)
	for i := range keys {
		keys[i] = newTestPublicKey(t)
	}
	errs := make(chan error, writers)
	start := make(chan struct{})
	for i := range writers {
		go func(key ssh.PublicKey) {
			<-start
			_, err := recordNewKnownHost(knownHostsPath, "concurrent.example.test:22", remote, key)
			errs <- err
		}(keys[i])
	}
	close(start)
	recorded, mismatched := 0, 0
	for range writers {
		var hostKeyErr *HostKeyError
		switch err := <-errs; {
		case err == nil:
			recorded++
		case errors.As(err, &hostKeyErr) && hostKeyErr.Kind == HostKeyMismatch:
			mismatched++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if recorded != 1 || mismatched != writers-1 {
		t.Fatalf("recorded=%d mismatched=%d, want 1 and %d", recorded, mismatched, writers-1)
	}
	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(content)), "\n") + 1; lines != 1 {
		t.Fatalf("known_hosts must hold one entry, got %d:\n%s", lines, content)
	}
}

func TestTrustNewHostKeyRejectsStaleFingerprintEvenWhenCurrentKeyIsTrusted(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	server := startHostKeyTestServer(t)
	if err := AppendKnownHost(knownHostsPath, server.address, server.hostKey); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	staleFingerprint := ssh.FingerprintSHA256(newTestPublicKey(t))

	result, err := TrustNewHostKey(context.Background(), server.address, staleFingerprint)
	if !errors.Is(err, ErrHostKeyFingerprintChanged) || result.AlreadyTrusted {
		t.Fatalf("a confirmation for another fingerprint must be rejected, got result=%+v err=%v", result, err)
	}
	after, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("known_hosts changed: before=%q after=%q", before, after)
	}
}

func requireOpenSSHClient(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client not available")
	}
	return path
}

func TestRegisterNewHostKeyWithOpenSSHFollowsConfiguredRouteWithoutCredentials(t *testing.T) {
	sshBinary := requireOpenSSHClient(t)
	knownHostsPath := setupStrictKnownHosts(t)
	server := startHostKeyTestServer(t)
	_, port, err := net.SplitHostPort(server.address)
	if err != nil {
		t.Fatal(err)
	}
	// The alias only resolves through ssh config and the connection only runs
	// through ProxyCommand, like a ProxyJump-only host: a direct Go dial of the
	// alias cannot reach it, the probe must take OpenSSH's route.
	configPath := filepath.Join(t.TempDir(), "ssh_config")
	config := "Host xirang-probe-alias\n" +
		"  HostName 127.0.0.1\n" +
		"  Port " + port + "\n" +
		// stderr is detached so the lingering relay cannot hold the caller's pipe open.
		"  ProxyCommand bash -c 'exec 2>/dev/null 3<>/dev/tcp/%h/%p && { cat <&3 & cat >&3; }'\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{sshBinary, "-F", configPath}

	for range 2 {
		if err := RegisterNewHostKeyWithOpenSSH(context.Background(), base, "tester", "xirang-probe-alias"); err != nil {
			t.Fatalf("register through configured route: %v", err)
		}
	}
	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(content)), "\n") + 1; lines != 1 || !strings.Contains(string(content), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(server.hostKey)))) {
		t.Fatalf("known_hosts must hold exactly the server key once, got:\n%s", content)
	}
	if attempts := server.authAttempts.Load(); attempts != 0 {
		t.Fatalf("registration probe must not offer any credential, got %d attempts", attempts)
	}
	// Strict OpenSSH over the same route now verifies the recorded key and only
	// then reaches authentication (which the test server refuses).
	checkCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	check := exec.CommandContext(checkCtx, sshBinary, "-F", configPath, "-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "UserKnownHostsFile="+knownHostsPath,
		"-o", "BatchMode=yes", "-o", "PreferredAuthentications=none", "-l", "tester", "xirang-probe-alias", "true")
	check.WaitDelay = time.Second
	output, _ := check.CombinedOutput()
	if !strings.Contains(string(output), "Permission denied") {
		t.Fatalf("strict OpenSSH must accept the registered key and reach authentication, output: %s", output)
	}
}

func TestMergeDiscoveredKnownHostsRespectsConcurrentRecords(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	recorded, discovered := newTestPublicKey(t), newTestPublicKey(t)
	if err := AppendKnownHost(knownHostsPath, "race.example.test:2222", recorded); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Another writer recorded a different key after the probe copied the file.
	err = mergeDiscoveredKnownHosts(knownHostsPath, []byte(knownhosts.Line([]string{"race.example.test:2222"}, discovered)+"\n"))
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch {
		t.Fatalf("conflicting discovery must be a mismatch, got %v", err)
	}
	// The same key recorded concurrently is simply already trusted.
	if err := mergeDiscoveredKnownHosts(knownHostsPath, []byte(knownhosts.Line([]string{"race.example.test:2222"}, recorded)+"\n")); err != nil {
		t.Fatalf("identical discovery must be a no-op, got %v", err)
	}
	after, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("known_hosts changed: before=%q after=%q", before, after)
	}

	// A new host is appended in the form OpenSSH wrote it.
	fresh := newTestPublicKey(t)
	if err := mergeDiscoveredKnownHosts(knownHostsPath, []byte("fresh.example.test "+strings.TrimSpace(string(ssh.MarshalAuthorizedKey(fresh)))+"\n")); err != nil {
		t.Fatal(err)
	}
	verify, err := knownhosts.New(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify("fresh.example.test:22", &net.TCPAddr{IP: net.IPv4zero}, fresh); err != nil {
		t.Fatalf("discovered host must be trusted after merge: %v", err)
	}
}

func TestMultiKeyHostWithRecordedEd25519IsTrustedAcrossProbeTrustAndDial(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	server := startHostKeyTestServerWithKeys(t, true)
	// Recorded the way OpenSSH or a manual connection would: Ed25519 only.
	if err := AppendKnownHost(knownHostsPath, server.address, server.hostKey); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := TrustNewHostKey(context.Background(), server.address, ssh.FingerprintSHA256(server.hostKey))
	if err != nil || !result.AlreadyTrusted || result.Algorithm != ssh.KeyAlgoED25519 {
		t.Fatalf("trust must see the recorded Ed25519 key, got result=%+v err=%v", result, err)
	}
	if err := dialHostKeyTestServer(t, server); err != nil {
		t.Fatalf("dial must negotiate the recorded key type instead of ECDSA: %v", err)
	}
	after, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("known_hosts must not change: before=%q after=%q", before, after)
	}
}

func TestMultiKeyHostWithChangedRecordedKeyStillMismatches(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	server := startHostKeyTestServerWithKeys(t, true)
	stalePublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	staleKey, err := ssh.NewPublicKey(stalePublic)
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendKnownHost(knownHostsPath, server.address, staleKey); err != nil {
		t.Fatal(err)
	}
	var hostKeyErr *HostKeyError
	if _, err := TrustNewHostKey(context.Background(), server.address, ssh.FingerprintSHA256(server.hostKey)); !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch {
		t.Fatalf("changed Ed25519 key must still be a mismatch, got %v", err)
	}
	if err := dialHostKeyTestServer(t, server); !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch || hostKeyErr.Algorithm() != ssh.KeyAlgoED25519 {
		t.Fatalf("dial must report the changed Ed25519 key as mismatch, got %v", err)
	}
}

func TestCertAuthorityBackedHostKeepsCertificateNegotiation(t *testing.T) {
	knownHostsPath := setupStrictKnownHosts(t)
	newSigner := func() ssh.Signer {
		_, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := ssh.NewSignerFromKey(privateKey)
		if err != nil {
			t.Fatal(err)
		}
		return signer
	}
	caSigner, hostSigner := newSigner(), newSigner()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().String()
	certificate := &ssh.Certificate{
		Key:             hostSigner.PublicKey(),
		CertType:        ssh.HostCert,
		ValidPrincipals: []string{"127.0.0.1"},
		ValidBefore:     ssh.CertTimeInfinity,
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	certSigner, err := ssh.NewCertSigner(certificate, hostSigner)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if string(password) == "secret" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	// Offer both the raw Ed25519 key and its certificate, as OpenSSH servers do.
	config.AddHostKey(hostSigner)
	config.AddHostKey(certSigner)
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close() //nolint:errcheck
				sshConn, channels, requests, handshakeErr := ssh.NewServerConn(conn, config)
				if handshakeErr != nil {
					return
				}
				defer sshConn.Close() //nolint:errcheck
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					_ = channel.Reject(ssh.Prohibited, "test server")
				}
			}()
		}
	}()
	caLine := "@cert-authority " + knownhosts.Normalize(address) + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(caSigner.PublicKey())))
	if err := ensureKnownHostsFile(knownHostsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHostsPath, []byte("# trusted host CA\n"+caLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}

	callback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	client, err := DialSSH(context.Background(), address, "tester", []ssh.AuthMethod{ssh.Password("secret")}, callback)
	if err != nil {
		t.Fatalf("CA-backed host must negotiate its certificate, got %v", err)
	}
	_ = client.Close()
	after, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("known_hosts must not change: before=%q after=%q", before, after)
	}
}
