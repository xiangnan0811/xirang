package verifier

import (
	"context"
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
	"strings"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"

	"golang.org/x/crypto/ssh"
)

// TestRsyncRestoreVerifierRealExecutorRoundTripVirtualNodePaths proves that
// Core never relies on being able to read the node's absolute paths.  The
// sshd ForceCommand maps a virtual node-only prefix to a separate physical
// fixture subtree and executes the actual SSH_ORIGINAL_COMMAND, including
// rsync --server and source hash commands.
func TestRsyncRestoreVerifierRealExecutorRoundTripVirtualNodePaths(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")

	physicalRoot := t.TempDir()
	virtualRoot := filepath.Join("/tmp", "xirang-node-virtual-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if _, err := os.Stat(virtualRoot); !os.IsNotExist(err) {
		t.Fatalf("virtual node prefix unexpectedly exists locally: %q err=%v", virtualRoot, err)
	}
	node := startMappedRsyncVerifierSSHServer(t, sshdBinary, virtualRoot, physicalRoot)
	fixture := newVirtualRsyncRestoreFixture(t, node, rsyncBinary, virtualRoot, physicalRoot)
	if _, err := os.Stat(fixture.backupTask.RsyncSource); !os.IsNotExist(err) {
		t.Fatalf("Core unexpectedly sees virtual node source %q: err=%v", fixture.backupTask.RsyncSource, err)
	}
	if _, err := os.Stat(fixture.restoreTask.RsyncTarget); !os.IsNotExist(err) {
		t.Fatalf("Core unexpectedly sees virtual node target %q: err=%v", fixture.restoreTask.RsyncTarget, err)
	}
	assertRsyncFixtureManifest(t, fixture)
	assertRsyncCoreLayout(t, fixture)
	assertRsyncRemoteLayout(t, fixture)
	assertRsyncRootMode(t, fixture.source, fixture.target)
	assertRsyncVerifyPassed(t, fixture)
}

func newVirtualRsyncRestoreFixture(t *testing.T, node model.Node, rsyncBinary, virtualRoot, physicalRoot string) rsyncRestoreFixture {
	t.Helper()
	coreRoot := t.TempDir()
	physicalSource := filepath.Join(physicalRoot, "app")
	fileData, entryKinds := createRsyncFixtureTree(t, physicalSource, "directory")
	if err := os.Chmod(physicalSource, 0o700); err != nil {
		t.Fatalf("set virtual source root mode: %v", err)
	}
	largePayload := strings.Repeat("v", 2048)
	if err := os.WriteFile(filepath.Join(physicalSource, "keep.txt"), []byte(largePayload), 0o644); err != nil {
		t.Fatalf("write large virtual-node payload: %v", err)
	}
	fileData["keep.txt"] = largePayload
	virtualSource := filepath.Join(virtualRoot, "app")
	physicalTarget := filepath.Join(physicalRoot, "restore", "destination")
	virtualTarget := filepath.Join(virtualRoot, "restore", "destination")
	if err := os.MkdirAll(filepath.Dir(physicalTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	coreBackup := filepath.Join(coreRoot, "backup")
	backupTask := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  virtualSource,
		RsyncTarget:  coreBackup,
		Node:         node,
	}
	ctx := context.Background()
	raw, err := executor.CaptureRsyncManifest(ctx, backupTask)
	if err != nil {
		t.Fatalf("capture virtual node source manifest: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode virtual node source manifest: %v", err)
	}
	runner := executor.NewFactory(rsyncBinary).Resolve("rsync")
	if exitCode, runErr := runner.Run(ctx, backupTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("virtual node backup exit=%d err=%v", exitCode, runErr)
	}
	if err := executor.VerifyRsyncCaptureManifestTarget(ctx, backupTask, raw); err != nil {
		t.Fatalf("virtual node Core target evidence: %v", err)
	}
	restorer, ok := runner.(executor.RestoreExecutor)
	if !ok {
		t.Fatal("rsync factory result does not implement RestoreExecutor")
	}
	restoreTask := backupTask
	restoreTask.RsyncSource = coreBackup
	restoreTask.RsyncTarget = virtualTarget
	restoreTask.RsyncCaptureLayout = manifest.Layout
	restoreTask.RsyncCaptureRoot = manifest.Root
	restoreTask.RsyncCaptureManifest = raw
	if exitCode, runErr := restorer.RunRestore(ctx, restoreTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("virtual node restore exit=%d err=%v", exitCode, runErr)
	}
	return rsyncRestoreFixture{
		source:      physicalSource,
		coreBackup:  coreBackup,
		target:      physicalTarget,
		backupTask:  backupTask,
		restoreTask: restoreTask,
		manifest:    manifest,
		rawManifest: raw,
		runner:      runner,
		restorer:    restorer,
		fileData:    fileData,
		entryKinds:  entryKinds,
	}
}

func startMappedRsyncVerifierSSHServer(t *testing.T, sshdBinary, virtualRoot, physicalRoot string) model.Node {
	t.Helper()
	currentUser, err := user.Current()
	if err != nil || strings.TrimSpace(currentUser.Username) == "" {
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
	wrapperPath := filepath.Join(workDir, "map-node-path.sh")
	pattern := strings.ReplaceAll(strings.ReplaceAll(virtualRoot, `\`, `\\`), `|`, `\|`)
	replacement := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(physicalRoot, `\`, `\\`), `|`, `\|`), `&`, `\&`)
	reversePattern := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(physicalRoot, `\`, `\\`), `|`, `\|`), `.`, `\.`)
	reverseReplacement := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(virtualRoot, `\`, `\\`), `|`, `\|`), `&`, `\&`)
	script := fmt.Sprintf("#!/bin/sh\nset -eu\ncommand=${SSH_ORIGINAL_COMMAND:-}\nif [ -z \"$command\" ]; then exit 1; fi\nmapped=$(printf '%%s' \"$command\" | sed 's|%s|%s|g')\ncase \"$mapped\" in\n  *'rsync --server'*) exec /bin/sh -c \"$mapped\" ;;\nesac\ntmp=$(mktemp)\nstatus=0\n/bin/sh -c \"$mapped\" >\"$tmp\" || status=$?\nsed -z 's|%s|%s|g' \"$tmp\"\nrm -f \"$tmp\"\nexit \"$status\"\n", pattern, replacement, reversePattern, reverseReplacement)
	if err := os.WriteFile(wrapperPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write SSH path-map wrapper: %v", err)
	}
	permitRoot := "no"
	if currentUser.Username == "root" {
		permitRoot = "yes"
	}
	config := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nAuthorizedKeysFile %s\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nChallengeResponseAuthentication no\nUsePAM no\nPermitRootLogin %s\nPubkeyAuthentication yes\nStrictModes no\nUseDNS no\nPrintMotd no\nPidFile %s\nLogLevel ERROR\nAllowUsers %s\nForceCommand %s\n", port, hostPrivatePath, authorizedKeysPath, permitRoot, pidPath, currentUser.Username, wrapperPath)
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
