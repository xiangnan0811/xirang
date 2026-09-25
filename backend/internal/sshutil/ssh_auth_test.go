package sshutil

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestDialSSHHandshakeCancellationClosesConnection(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, _, err := newClientConnWithContext(ctx, client, "example.invalid:22", &ssh.ClientConfig{
			User: "reader", HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handshake error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH handshake ignored cancellation")
	}
}

func newTestPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试 RSA 私钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("生成测试 SSH signer 失败: %v", err)
	}
	return signer.PublicKey()
}

func TestResolveSSHHostKeyCallbackAcceptsUnknownKeyOnceAndRejectsMismatch(t *testing.T) {
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")
	knownHostsPath := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHostsPath)

	callback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatalf("初始化 SSH host key callback 失败: %v", err)
	}

	hostname := "example.com:22"
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 22}
	trustedKey := newTestPublicKey(t)
	changedKey := newTestPublicKey(t)

	if err := callback(hostname, remote, trustedKey); err != nil {
		t.Fatalf("首次未知主机密钥应被接受并写入 known_hosts，实际错误: %v", err)
	}

	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("读取 known_hosts 失败: %v", err)
	}
	if !strings.Contains(string(content), "example.com") {
		t.Fatalf("known_hosts 未写入目标主机，实际内容: %s", string(content))
	}

	if err := callback(hostname, remote, trustedKey); err != nil {
		t.Fatalf("已记录的主机密钥再次校验应通过，实际错误: %v", err)
	}

	err = callback(hostname, remote, changedKey)
	if err == nil {
		t.Fatalf("主机密钥变化时应继续拒绝连接")
	}
	if strings.Contains(err.Error(), "example.com") || strings.Contains(err.Error(), "203.0.113.10") || strings.Contains(err.Error(), knownHostsPath) {
		t.Fatalf("主机密钥冲突错误不应暴露主机标识或 known_hosts 路径: %v", err)
	}
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyMismatch || hostKeyErr.Fingerprint() != ssh.FingerprintSHA256(changedKey) {
		t.Fatalf("主机密钥冲突应返回 mismatch HostKeyError 并携带当前指纹，实际: %#v", err)
	}
}

func TestResolveSSHHostKeyCallbackFollowsDynamicAutoAcceptSource(t *testing.T) {
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	knownHostsPath := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHostsPath)
	effective := "true"
	SetAutoAcceptNewHostsSource(func() string { return effective })
	t.Cleanup(func() { SetAutoAcceptNewHostsSource(nil) })

	callback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 22}
	if err := callback("accepted.example.com:22", remote, newTestPublicKey(t)); err != nil {
		t.Fatalf("动态设置开启时应接受未知主机密钥（覆盖 env=false），实际: %v", err)
	}

	// The same callback instance must observe the change without restart.
	effective = "false"
	if err := callback("rejected.example.com:22", remote, newTestPublicKey(t)); err == nil {
		t.Fatal("动态设置关闭后未知主机密钥应被拒绝")
	}
	effective = "not-a-bool"
	var hostKeyErr *HostKeyError
	if err := callback("invalid.example.com:22", remote, newTestPublicKey(t)); !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyUnknown {
		t.Fatalf("无效设置值应按拒绝处理，实际: %v", err)
	}
	if _, err := AutoAcceptNewHosts(); err == nil || err.Error() != "SSH 自动接受未知主机密钥配置值无效" {
		t.Fatalf("无效设置值应返回固定错误，实际: %v", err)
	}
	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "accepted.example.com") || strings.Contains(string(content), "rejected.example.com") || strings.Contains(string(content), "invalid.example.com") {
		t.Fatalf("known_hosts 应只记录动态开启时的主机，实际: %s", content)
	}
}

func TestResolveSSHHostKeyCallbackRejectsUnknownKeyByDefault(t *testing.T) {
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "")
	knownHostsPath := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHostsPath)
	callback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 22}
	key := newTestPublicKey(t)
	err = callback("example.com:22", remote, key)
	if err == nil {
		t.Fatal("unknown host keys must be rejected without explicit trust")
	}
	var hostKeyErr *HostKeyError
	if !errors.As(err, &hostKeyErr) || hostKeyErr.Kind != HostKeyUnknown || hostKeyErr.Fingerprint() != ssh.FingerprintSHA256(key) || hostKeyErr.Algorithm() != key.Type() {
		t.Fatalf("unknown host key must be reported as HostKeyError with presented key, got %#v", err)
	}
	stored, err := os.ReadFile(knownHostsPath)
	if err != nil || len(stored) != 0 {
		t.Fatalf("rejected key must not be trusted: contents=%q err=%v", stored, err)
	}
}

func TestResolveSSHHostKeyCallbackRejectsUnknownKeyWhenExplicitlyDisabled(t *testing.T) {
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	knownHostsPath := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHostsPath)

	callback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatalf("初始化 SSH host key callback 失败: %v", err)
	}

	hostname := "example.com:22"
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 22}
	err = callback(hostname, remote, newTestPublicKey(t))
	if err == nil {
		t.Fatalf("显式禁用时未知主机密钥应被拒绝")
	}
	if strings.Contains(err.Error(), "example.com") || strings.Contains(err.Error(), "203.0.113.10") {
		t.Fatalf("未知主机密钥错误不应暴露主机标识: %v", err)
	}
}

func TestResolveSSHHostKeyCallbackSanitizesAutoAcceptWriteFailure(t *testing.T) {
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")
	tempDir := t.TempDir()
	knownHostsPath := filepath.Join(tempDir, "ssh", "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHostsPath)

	callback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatalf("初始化 SSH host key callback 失败: %v", err)
	}
	if err := os.Remove(knownHostsPath); err != nil {
		t.Fatalf("移除 known_hosts 测试文件失败: %v", err)
	}
	if err := os.Mkdir(knownHostsPath, 0o700); err != nil {
		t.Fatalf("创建 known_hosts 同名目录失败: %v", err)
	}

	hostname := "example.com:22"
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 22}
	err = callback(hostname, remote, newTestPublicKey(t))
	if err == nil {
		t.Fatalf("known_hosts 写入失败时应返回错误")
	}
	message := err.Error()
	for _, forbidden := range []string{hostname, "example.com", "203.0.113.10", knownHostsPath, tempDir} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("自动接受写入失败错误不应暴露主机或路径 %q，实际: %v", forbidden, err)
		}
	}
}

func TestAppendKnownHostSerializesConcurrentWrites(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	const total = 12

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			hostname := fmt.Sprintf("node-%02d.example.com:22", index)
			if err := AppendKnownHost(knownHostsPath, hostname, newTestPublicKey(t)); err != nil {
				t.Errorf("追加 known_hosts 失败(host=%s): %v", hostname, err)
			}
		}(i)
	}
	wg.Wait()

	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("读取 known_hosts 失败: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != total {
		t.Fatalf("期望写入 %d 条 known_hosts，实际: %d\n内容: %s", total, len(lines), string(content))
	}
	for i := 0; i < total; i++ {
		hostname := fmt.Sprintf("node-%02d.example.com", i)
		if !strings.Contains(string(content), hostname) {
			t.Fatalf("known_hosts 缺少主机 %s\n内容: %s", hostname, string(content))
		}
	}
}

func TestAppendKnownHostSkipsDuplicateHostKey(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	hostname := "node-dup.example.com:22"
	key := newTestPublicKey(t)

	if err := AppendKnownHost(knownHostsPath, hostname, key); err != nil {
		t.Fatalf("首次追加 known_hosts 失败: %v", err)
	}
	if err := AppendKnownHost(knownHostsPath, hostname, key); err != nil {
		t.Fatalf("重复追加 known_hosts 失败: %v", err)
	}

	content, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("读取 known_hosts 失败: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 1 {
		t.Fatalf("重复 host/key 不应被重复写入，实际行数: %d\n内容: %s", len(lines), string(content))
	}
}

func TestParseDiskProbeDistinctValues(t *testing.T) {
	used, total, ok := ParseDiskProbe("100G 42G")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if total != 100 || used != 42 {
		t.Fatalf("expected total=100 used=42, got total=%d used=%d", total, used)
	}
}
