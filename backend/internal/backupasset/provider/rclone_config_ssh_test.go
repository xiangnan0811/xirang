package provider

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

const (
	rcloneSSHConfigCanary      = "FAKE_RCLONE_BOUND_CANARY_FOR_TEST_ONLY"
	rcloneSSHResticCanary      = "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"
	rcloneSSHBoundConfig       = "[fixture]\ntype = local\n# " + rcloneSSHConfigCanary
	rcloneSSHControlledModeEnv = "XIRANG_RCLONE_CONFIG_SSH_TEST_MODE"
)

// The child process is the test binary itself, launched as a controlled remote
// tool. Its only output is boolean observations; neither config nor password is
// passed in the command or environment, or printed by the child.
func init() {
	if mode := os.Getenv(rcloneSSHControlledModeEnv); mode != "" {
		observation := observeRcloneSSHControlledTool(mode)
		if err := json.NewEncoder(os.Stdout).Encode(observation); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

type rcloneSSHControlledObservation struct {
	ConfigPresent   bool `json:"config_present"`
	ExactConfig     bool `json:"exact_config"`
	PrivateConfig   bool `json:"private_config"`
	ExpectedStdin   bool `json:"expected_stdin"`
	SafeArguments   bool `json:"safe_arguments"`
	SafeEnvironment bool `json:"safe_environment"`
}

func observeRcloneSSHControlledTool(mode string) rcloneSSHControlledObservation {
	observation := rcloneSSHControlledObservation{SafeArguments: true, SafeEnvironment: true}
	var configPath string
	configCount := 0
	for index, argument := range os.Args {
		if containsRcloneSSHCanary(argument) {
			observation.SafeArguments = false
		}
		if argument == "--config" {
			configCount++
			if index+1 < len(os.Args) {
				configPath = os.Args[index+1]
			}
		} else if value, ok := strings.CutPrefix(argument, "--config="); ok {
			configCount++
			configPath = value
		}
	}
	for _, variable := range os.Environ() {
		if containsRcloneSSHCanary(variable) {
			observation.SafeEnvironment = false
		}
	}
	observation.ConfigPresent = configCount != 0
	input, inputErr := io.ReadAll(io.LimitReader(os.Stdin, 64<<10+2))
	if strings.HasPrefix(mode, "bound-") {
		want := []byte(rcloneSSHBoundConfig)
		if mode == "bound-newline" {
			want = append(want, '\n')
		}
		observation.PrivateConfig = configCount == 1 && rcloneSSHConfigIsPrivate(configPath)
		if observation.PrivateConfig {
			config, err := os.ReadFile(configPath)
			observation.ExactConfig = err == nil && bytes.Equal(config, want)
		}
		observation.ExpectedStdin = inputErr == nil && len(input) == 0
	} else {
		observation.ExactConfig = configCount == 0
		observation.PrivateConfig = configCount == 0
		want := []byte(nil)
		if strings.HasPrefix(mode, "restic-") {
			want = []byte(rcloneSSHResticCanary + "\n")
			if mode == "restic-newline" {
				want = append(want, '\n')
			}
		}
		observation.ExpectedStdin = inputErr == nil && bytes.Equal(input, want)
	}
	return observation
}

func containsRcloneSSHCanary(value string) bool {
	return strings.Contains(value, rcloneSSHConfigCanary) || strings.Contains(value, rcloneSSHResticCanary)
}

func rcloneSSHConfigIsPrivate(configPath string) bool {
	home := os.Getenv("HOME")
	root := filepath.Join(home, rcloneCommandConfigRoot)
	relative, err := filepath.Rel(root, configPath)
	if err != nil {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 2 || !lowerHex(parts[0], 32) || parts[1] != "rclone.conf" {
		return false
	}
	for _, directory := range []string{filepath.Join(home, ".xirang"), root, filepath.Dir(configPath)} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return false
		}
	}
	info, err := os.Lstat(configPath)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o600
}

// rcloneConfigLoopbackSSH owns the listener, authenticated clients, SFTP
// subsystems and command processes. Every factory obtains a fresh connection.
// The pkg/sftp server uses the private fixture home as its working directory;
// it is a test server for trusted local commands, not a filesystem sandbox.
type rcloneConfigLoopbackSSH struct {
	listener net.Listener
	home     string
	mode     string
	hostKey  ssh.PublicKey
	userKey  ssh.Signer
	config   *ssh.ServerConfig

	mu               sync.Mutex
	connections      map[net.Conn]struct{}
	workers          sync.WaitGroup
	acceptDone       chan struct{}
	dials            atomic.Int64
	commands         atomic.Int64
	subsystems       atomic.Int64
	commandHasCanary atomic.Bool
}

func newRcloneConfigLoopbackSSH(t *testing.T, mode string) *rcloneConfigLoopbackSSH {
	t.Helper()
	home := t.TempDir()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for fixture SSH: %v", err)
	}
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("generate SSH fixture host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("create SSH fixture host signer: %v", err)
	}
	_, userPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("generate SSH fixture user key: %v", err)
	}
	userSigner, err := ssh.NewSignerFromKey(userPrivate)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("create SSH fixture user signer: %v", err)
	}
	server := &rcloneConfigLoopbackSSH{
		listener: listener, home: home, mode: mode, hostKey: hostSigner.PublicKey(), userKey: userSigner,
		connections: make(map[net.Conn]struct{}), acceptDone: make(chan struct{}),
	}
	server.config = &ssh.ServerConfig{PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if metadata.User() != "rclone-config-fixture" || !bytes.Equal(key.Marshal(), userSigner.PublicKey().Marshal()) {
			return nil, errors.New("SSH fixture authentication rejected")
		}
		return nil, nil
	}}
	server.config.AddHostKey(hostSigner)
	go server.accept()
	t.Cleanup(func() { server.close(t) })
	return server
}

func (server *rcloneConfigLoopbackSSH) accept() {
	defer close(server.acceptDone)
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.mu.Lock()
		server.connections[connection] = struct{}{}
		server.mu.Unlock()
		server.workers.Add(1)
		go func() {
			defer server.workers.Done()
			defer func() {
				server.mu.Lock()
				delete(server.connections, connection)
				server.mu.Unlock()
				_ = connection.Close()
			}()
			server.serveConnection(connection)
		}()
	}
}

func (server *rcloneConfigLoopbackSSH) serveConnection(connection net.Conn) {
	sshConnection, channels, requests, err := ssh.NewServerConn(connection, server.config)
	if err != nil {
		return
	}
	requestsDone := make(chan struct{})
	go func() {
		defer close(requestsDone)
		ssh.DiscardRequests(requests)
	}()
	var sessions sync.WaitGroup
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(ssh.UnknownChannelType, "fixture accepts only sessions")
			continue
		}
		channel, sessionRequests, err := incoming.Accept()
		if err != nil {
			continue
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			server.serveSession(channel, sessionRequests)
		}()
	}
	_ = sshConnection.Close()
	sessions.Wait()
	<-requestsDone
}

func (server *rcloneConfigLoopbackSSH) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close() //nolint:errcheck
	for request := range requests {
		switch request.Type {
		case "subsystem":
			var payload struct{ Name string }
			if ssh.Unmarshal(request.Payload, &payload) != nil || payload.Name != "sftp" {
				_ = request.Reply(false, nil)
				continue
			}
			subsystem, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(server.home))
			if err != nil {
				_ = request.Reply(false, nil)
				return
			}
			server.subsystems.Add(1)
			if err := request.Reply(true, nil); err != nil {
				_ = subsystem.Close()
				return
			}
			_ = subsystem.Serve()
			_ = subsystem.Close()
			return
		case "exec":
			var payload struct{ Command string }
			if ssh.Unmarshal(request.Payload, &payload) != nil {
				_ = request.Reply(false, nil)
				return
			}
			server.commands.Add(1)
			if containsRcloneSSHCanary(payload.Command) {
				server.commandHasCanary.Store(true)
			}
			server.runCommand(channel, requests, request, payload.Command)
			return
		default:
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
		}
	}
}

func (server *rcloneConfigLoopbackSSH) runCommand(channel ssh.Channel, requests <-chan *ssh.Request, request *ssh.Request, command string) {
	// The real SSH runner supplies shell-quoted argv, just as for an ordinary
	// SSH server. exec makes the tool itself receive signals and exit evidence.
	process := exec.Command("/bin/sh", "-c", "exec "+command)
	process.Dir = server.home
	process.Env = []string{"HOME=" + server.home, "PATH=/usr/bin:/bin", "LC_ALL=C", "TZ=UTC"}
	if server.mode != "" {
		process.Env = append(process.Env, rcloneSSHControlledModeEnv+"="+server.mode)
	}
	process.Stdin, process.Stdout, process.Stderr = channel, channel, channel.Stderr()
	if err := process.Start(); err != nil {
		_ = request.Reply(false, nil)
		return
	}
	if err := request.Reply(true, nil); err != nil {
		_ = process.Process.Kill()
		_ = process.Wait()
		return
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	for {
		select {
		case <-done:
			if status, ok := process.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				signal := struct {
					Signal     string
					CoreDumped bool
					Error      string
					Language   string
				}{Signal: strings.TrimPrefix(status.Signal().String(), "SIG")}
				if status.Signal() == syscall.SIGTERM {
					signal.Signal = "TERM"
				}
				_, _ = channel.SendRequest("exit-signal", false, ssh.Marshal(signal))
			} else {
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: uint32(process.ProcessState.ExitCode())}))
			}
			return
		case next, ok := <-requests:
			if !ok {
				_ = process.Process.Kill()
				<-done
				return
			}
			if next.Type == "signal" {
				var payload struct{ Signal string }
				if ssh.Unmarshal(next.Payload, &payload) == nil && payload.Signal == "TERM" {
					_ = process.Process.Signal(syscall.SIGTERM)
				}
			}
			if next.WantReply {
				_ = next.Reply(false, nil)
			}
		}
	}
}

func (server *rcloneConfigLoopbackSSH) close(t *testing.T) {
	t.Helper()
	_ = server.listener.Close()
	<-server.acceptDone
	server.mu.Lock()
	for connection := range server.connections {
		_ = connection.Close()
	}
	server.mu.Unlock()
	done := make(chan struct{})
	go func() { server.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("fixture SSH connections or processes did not stop")
	}
}

func (server *rcloneConfigLoopbackSSH) dial(ctx context.Context) (*ssh.Client, error) {
	server.dials.Add(1)
	return sshutil.DialSSH(ctx, server.listener.Addr().String(), "rclone-config-fixture",
		[]ssh.AuthMethod{ssh.PublicKeys(server.userKey)}, ssh.FixedHostKey(server.hostKey))
}

func (server *rcloneConfigLoopbackSSH) transport(t *testing.T, binaries ToolBinaries) *SSHCommandTransport {
	t.Helper()
	runnerFactory := func(ctx context.Context, _ RemoteCommandAccess, _ string) (remoteCommandRunner, io.Closer, error) {
		client, err := server.dial(ctx)
		if err != nil {
			return nil, nil, err
		}
		return sshutil.NewSSHCommandRunnerWithGracefulJoinedTransportClose(client, 1), client, nil
	}
	configFactory := func(ctx context.Context, _ RemoteCommandAccess, _ string) (stagedPayloadSession, error) {
		client, err := server.dial(ctx)
		if err != nil {
			return nil, err
		}
		return newRcloneConfigSession(ctx, client)
	}
	transport, err := newSSHCommandTransportWithConfig(runnerFactory, configFactory, func() (int, error) { return 2, nil }, binaries)
	if err != nil {
		t.Fatalf("create fixture command transport: %v", err)
	}
	return transport
}

func (server *rcloneConfigLoopbackSSH) assertConfigClean(t *testing.T, created bool) {
	t.Helper()
	root := filepath.Join(server.home, rcloneCommandConfigRoot)
	entries, err := os.ReadDir(root)
	if !created {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected config root after unconfigured operation (exists=%t)", err == nil)
		}
		return
	}
	if err != nil || len(entries) != 0 {
		t.Fatalf("config cleanup incomplete (read_ok=%t empty=%t)", err == nil, len(entries) == 0)
	}
}

func rcloneSSHControlledBinary(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate controlled test executable: %v", err)
	}
	return binary
}

func rcloneSSHInvocationForTest() CommandInvocation {
	return CommandInvocation{Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}}}
}

func assertRcloneSSHObservation(t *testing.T, output []byte, configPresent bool) {
	t.Helper()
	var observation rcloneSSHControlledObservation
	if err := json.Unmarshal(output, &observation); err != nil {
		t.Fatal("controlled tool did not return boolean observations")
	}
	if observation.ConfigPresent != configPresent || !observation.ExactConfig || !observation.PrivateConfig ||
		!observation.ExpectedStdin || !observation.SafeArguments || !observation.SafeEnvironment {
		t.Fatalf("controlled tool conclusions: %+v", observation)
	}
}

func TestRcloneConfigSSHRealBoundDelivery(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, newline := range []bool{false, true} {
			for _, method := range []string{"run", "open", "execution"} {
				name := "normal"
				if managed {
					name = "managed-version"
				}
				if newline {
					name += "/newline"
				} else {
					name += "/no-newline"
				}
				t.Run(name+"/"+method, func(t *testing.T) {
					mode := "bound-no-newline"
					secret := []byte(rcloneSSHBoundConfig)
					if newline {
						mode = "bound-newline"
						secret = append(secret, '\n')
					}
					server := newRcloneConfigLoopbackSSH(t, mode)
					transport := server.transport(t, ToolBinaries{Rclone: rcloneSSHControlledBinary(t)})
					invocation := rcloneSSHInvocationForTest()
					invocation.Tool, invocation.Purpose, invocation.SecretStdin = ToolRclone, CommandPurposeProbe, secret
					if managed {
						invocation.Operation = OperationRcloneManagedVersion
						invocation.RcloneLowLevelRetries = 1
						invocation.AbsoluteDeadline = time.Now().UTC().Add(5 * time.Minute)
					} else {
						invocation.Operation = OperationRcloneFeatures
						invocation.Args = []string{"backend", "features", "--"}
						invocation.PrivateLocator = "fixture:"
					}
					ctx := context.Background()
					limits := testOperationLimits()
					var output []byte
					var err error
					switch method {
					case "run":
						result, runErr := transport.Run(ctx, invocation, limits)
						output, err = result.Stdout, runErr
					case "open":
						handle, openErr := transport.Open(ctx, invocation, limits, limits.MaxMetadataBytes)
						if openErr != nil {
							t.Fatalf("open configured command: %v", openErr)
						}
						output, err = io.ReadAll(handle)
						err = errors.Join(err, handle.Close())
					case "execution":
						execution, openErr := transport.OpenExecution(ctx, invocation, limits, limits.MaxMetadataBytes)
						if openErr != nil {
							t.Fatalf("open configured execution: %v", openErr)
						}
						output, err = io.ReadAll(execution)
						completion, joinErr := execution.Join()
						err = errors.Join(err, joinErr)
						if err == nil && (!completion.ExitCodeKnown || completion.ExitCode != 0) {
							t.Fatal("execution did not report successful exit")
						}
					}
					if err != nil {
						t.Fatalf("configured command failed: %v", err)
					}
					assertRcloneSSHObservation(t, output, true)
					if server.commands.Load() != 1 || server.dials.Load() < 3 || server.subsystems.Load() < 3 || server.commandHasCanary.Load() {
						t.Fatalf("SSH conclusions: command_once=%t fresh_connections=%t used_sftp=%t command_safe=%t", server.commands.Load() == 1, server.dials.Load() >= 3, server.subsystems.Load() >= 3, !server.commandHasCanary.Load())
					}
					server.assertConfigClean(t, true)
				})
			}
		}
	}
}

func TestRcloneConfigSSHNodeDefaultAndResticContracts(t *testing.T) {
	for _, scenario := range []string{"default-version", "default-features", "restic-no-newline", "restic-newline"} {
		t.Run(scenario, func(t *testing.T) {
			server := newRcloneConfigLoopbackSSH(t, scenario)
			binary := rcloneSSHControlledBinary(t)
			transport := server.transport(t, ToolBinaries{Rclone: binary, Restic: binary})
			invocation := rcloneSSHInvocationForTest()
			invocation.Purpose = CommandPurposeProbe
			switch scenario {
			case "default-version":
				invocation.Tool, invocation.Operation, invocation.Args = ToolRclone, OperationRcloneVersion, []string{"version"}
			case "default-features":
				invocation.Tool, invocation.Operation, invocation.Args = ToolRclone, OperationRcloneFeatures, []string{"backend", "features", "--"}
				invocation.PrivateLocator = "fixture:"
			default:
				invocation.Tool, invocation.Operation, invocation.Args = ToolRestic, OperationResticConfig, []string{"--password-file", "/dev/stdin", "cat", "config"}
				invocation.PrivateLocator = "/fixture/repository"
				invocation.SecretStdin = []byte(rcloneSSHResticCanary)
				if scenario == "restic-newline" {
					invocation.SecretStdin = append(invocation.SecretStdin, '\n')
				}
			}
			output, err := transport.Run(context.Background(), invocation, testOperationLimits())
			if err != nil {
				t.Fatalf("unconfigured or Restic command failed: %v", err)
			}
			assertRcloneSSHObservation(t, output.Stdout, false)
			if server.commands.Load() != 1 || server.dials.Load() != 1 || server.subsystems.Load() != 0 || server.commandHasCanary.Load() {
				t.Fatalf("SSH conclusions: command_once=%t one_connection=%t no_sftp=%t command_safe=%t", server.commands.Load() == 1, server.dials.Load() == 1, server.subsystems.Load() == 0, !server.commandHasCanary.Load())
			}
			server.assertConfigClean(t, false)
		})
	}
}

func TestRcloneConfigSSHRejectsCallerConfigBeforeSideEffects(t *testing.T) {
	cases := []struct {
		name    string
		managed bool
		bound   bool
		args    []string
	}{
		{name: "bound-stdin", bound: true, args: []string{"--config", "/dev/stdin", "backend", "features", "--"}},
		{name: "bound-path", bound: true, args: []string{"--config", "/caller-controlled", "backend", "features", "--"}},
		{name: "bound-equals-path", bound: true, args: []string{"--config=/caller-controlled", "backend", "features", "--"}},
		{name: "bound-extra-flag", bound: true, args: []string{"backend", "features", "--verbose", "--"}},
		{name: "default-path", args: []string{"--config", "/caller-controlled", "backend", "features", "--"}},
		{name: "default-equals-path", args: []string{"--config=/caller-controlled", "backend", "features", "--"}},
		{name: "managed-path", managed: true, bound: true, args: []string{"--config", "/caller-controlled"}},
		{name: "managed-extra-flag", managed: true, bound: true, args: []string{"--verbose"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := newRcloneConfigLoopbackSSH(t, "bound-no-newline")
			transport := server.transport(t, ToolBinaries{Rclone: rcloneSSHControlledBinary(t)})
			invocation := rcloneSSHInvocationForTest()
			invocation.Tool, invocation.Purpose, invocation.Args = ToolRclone, CommandPurposeProbe, test.args
			if test.bound {
				invocation.SecretStdin = []byte(rcloneSSHBoundConfig)
			}
			if test.managed {
				invocation.Operation = OperationRcloneManagedVersion
				invocation.RcloneLowLevelRetries = 1
				invocation.AbsoluteDeadline = time.Now().UTC().Add(5 * time.Minute)
			} else {
				invocation.Operation = OperationRcloneFeatures
				invocation.PrivateLocator = "fixture:"
			}
			_, err := transport.Run(context.Background(), invocation, testOperationLimits())
			if !errors.Is(err, ErrUnsafeInvocation) {
				t.Fatalf("caller arguments were not rejected as unsafe: %v", err)
			}
			if server.dials.Load() != 0 || server.commands.Load() != 0 || server.subsystems.Load() != 0 {
				t.Fatal("unsafe caller arguments caused SSH or SFTP side effects")
			}
			server.assertConfigClean(t, false)
		})
	}
}
