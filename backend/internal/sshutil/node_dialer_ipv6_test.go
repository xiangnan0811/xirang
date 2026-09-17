package sshutil

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"golang.org/x/crypto/ssh"
)

// Exercise real TCP6 and SSH authentication/host verification, not just string formatting.
func TestNodeDialerIPv6SSHWithScopedCredentialAndPinnedHost(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer func() { _ = listener.Close() }()
	db := setupNodeDialerDB(t)
	privateKey := nodeDialerTestPrivateKey(t)
	clientSigner, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.ParsePrivateKey([]byte(nodeDialerTestPrivateKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	key := model.SSHKey{Name: "ipv6-reader", PrivateKey: privateKey, AllowedPurposes: PurposeRepositoryRead, AllowedNodeIDs: "7"}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHosts)
	if err := AppendKnownHost(knownHosts, listener.Addr().String(), hostSigner.PublicKey()); err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, publicKey ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "reader" || !bytes.Equal(publicKey.Marshal(), clientSigner.PublicKey().Marshal()) {
			return nil, fmt.Errorf("unexpected SSH credential")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(hostSigner)
	serverResult := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		server, channels, requests, handshakeErr := ssh.NewServerConn(conn, serverConfig)
		if handshakeErr != nil {
			serverResult <- handshakeErr
			return
		}
		defer func() { _ = server.Close() }()
		go ssh.DiscardRequests(requests)
		channelRequest, ok := <-channels
		if !ok {
			serverResult <- fmt.Errorf("session channel missing")
			return
		}
		channel, sessionRequests, acceptErr := channelRequest.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer func() { _ = channel.Close() }()
		request, ok := <-sessionRequests
		if !ok || request.Type != "exec" {
			serverResult <- fmt.Errorf("exec request missing")
			return
		}
		if err := request.Reply(true, nil); err != nil {
			serverResult <- err
			return
		}
		if _, err := channel.Write([]byte("ipv6-ssh-ok")); err != nil {
			serverResult <- err
			return
		}
		_, err := channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		serverResult <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	node := model.Node{ID: 7, Host: "::1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "reader", AuthType: "key", SSHKeyID: &key.ID}
	client, err := NewNodeDialer(db).Dial(ctx, node, PurposeRepositoryRead, DialAuditContext{Action: "repository.read"})
	if err != nil {
		t.Fatalf("real IPv6 SSH dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CombinedOutput("probe")
	if err != nil || string(result) != "ipv6-ssh-ok" {
		t.Fatalf("SSH session result=%q error=%v", result, err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	var event model.CredentialAuditEvent
	if err := db.Where("node_id = ? AND purpose = ? AND outcome = ?", 7, PurposeRepositoryRead, "success").First(&event).Error; err != nil {
		t.Fatalf("successful scoped SSH use must be audited: %v", err)
	}
}
