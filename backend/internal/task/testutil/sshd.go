package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"golang.org/x/crypto/ssh"
)

// StartRsyncSSHServer starts an isolated local sshd suitable for real Rsync
// integration tests. The returned node carries the generated private key and
// all files/processes are cleaned up through the test lifecycle.
func StartRsyncSSHServer(t testing.TB, sshdBinary string) model.Node {
	t.Helper()
	currentUser, err := user.Current()
	if err != nil || currentUser == nil || currentUser.Username == "" {
		t.Fatalf("resolve current SSH test user: %v", err)
	}
	workDir := t.TempDir()
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate SSH test port: %v", err)
	}
	port := portListener.Addr().(*net.TCPAddr).Port
	if err := portListener.Close(); err != nil {
		t.Fatal(err)
	}

	userKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate SSH user key: %v", err)
	}
	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate SSH host key: %v", err)
	}
	userPrivatePath := filepath.Join(workDir, "user-key.pem")
	hostPrivatePath := filepath.Join(workDir, "host-key.pem")
	authorizedKeysPath := filepath.Join(workDir, "authorized_keys")
	configPath := filepath.Join(workDir, "sshd_config")
	pidPath := filepath.Join(workDir, "sshd.pid")
	writePEM := func(path string, key *rsa.PrivateKey) {
		t.Helper()
		data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write SSH key %q: %v", path, err)
		}
	}
	writePEM(userPrivatePath, userKey)
	writePEM(hostPrivatePath, hostKey)
	publicKey, err := ssh.NewPublicKey(&userKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal SSH public key: %v", err)
	}
	if err := os.WriteFile(authorizedKeysPath, ssh.MarshalAuthorizedKey(publicKey), 0o600); err != nil {
		t.Fatalf("write SSH authorized keys: %v", err)
	}
	permitRoot := "no"
	if currentUser.Username == "root" {
		permitRoot = "yes"
	}
	config := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nAuthorizedKeysFile %s\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nChallengeResponseAuthentication no\nUsePAM no\nPermitRootLogin %s\nPubkeyAuthentication yes\nStrictModes no\nUseDNS no\nPrintMotd no\nPidFile %s\nLogLevel ERROR\nAllowUsers %s\n", port, hostPrivatePath, authorizedKeysPath, permitRoot, pidPath, currentUser.Username)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write sshd config: %v", err)
	}

	cmd := exec.Command(sshdBinary, "-D", "-e", "-f", configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logPath := filepath.Join(workDir, "sshd.log")
	output, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open sshd log: %v", err)
	}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		_ = output.Close()
		t.Fatalf("start sshd: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process == nil {
			_ = output.Close()
			return
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = output.Close()
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Logf("sshd did not exit after process-group kill")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return model.Node{
				Host:       "127.0.0.1",
				Port:       port,
				Username:   currentUser.Username,
				AuthType:   "key",
				PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(userKey)})),
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = output.Close()
	logContents, _ := os.ReadFile(logPath)
	t.Fatalf("sshd did not become ready: %s", string(logContents))
	return model.Node{}
}
