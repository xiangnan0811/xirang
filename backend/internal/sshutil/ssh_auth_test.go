package sshutil

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

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

	if err := callback(hostname, remote, changedKey); err == nil {
		t.Fatalf("主机密钥变化时应继续拒绝连接")
	}
}

func TestResolveSSHHostKeyCallbackRejectsUnknownKeyByDefault(t *testing.T) {
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "")
	knownHostsPath := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHostsPath)

	callback, err := ResolveSSHHostKeyCallback()
	if err != nil {
		t.Fatalf("初始化 SSH host key callback 失败: %v", err)
	}

	hostname := "example.com:22"
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 22}
	if err := callback(hostname, remote, newTestPublicKey(t)); err == nil {
		t.Fatalf("默认未知主机密钥应被拒绝")
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
