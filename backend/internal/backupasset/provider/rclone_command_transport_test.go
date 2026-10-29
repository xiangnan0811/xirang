package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

const configTransportCanary = "FAKE_RCLONE_CONFIG_SECRET_FOR_TEST_ONLY"
const configTransportOutput = "remote content"

// These doubles replace only the SSH command boundary. Configuration is staged
// and cleaned through the real SFTP fixture, and the command opens the staged
// file before returning any output.
type configTransportRunner struct {
	openExecution func(context.Context, sshutil.CommandSpec) (sshutil.CommandExecutionStream, error)
	run           func(context.Context, sshutil.CommandSpec) (sshutil.CommandResult, error)
}

func (runner *configTransportRunner) Run(ctx context.Context, spec sshutil.CommandSpec) (sshutil.CommandResult, error) {
	if runner.run == nil {
		return sshutil.CommandResult{}, errors.New("unexpected Run at command boundary")
	}
	return runner.run(ctx, spec)
}

func (*configTransportRunner) Open(context.Context, sshutil.CommandSpec) (sshutil.CommandReadHandle, error) {
	return nil, errors.New("unexpected Open at command boundary")
}

func (runner *configTransportRunner) OpenExecution(ctx context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
	if runner.openExecution == nil {
		return nil, errors.New("unexpected OpenExecution at command boundary")
	}
	return runner.openExecution(ctx, spec)
}

type configTransportExecution struct {
	reader      *strings.Reader
	completion  sshutil.CommandCompletion
	joinErr     error
	prefixErr   error
	cancelErr   error
	onFinish    func()
	once        sync.Once
	resultErr   error
	joinCalls   atomic.Int32
	prefixCalls atomic.Int32
	cancelCalls atomic.Int32
}

func newConfigTransportExecution() *configTransportExecution {
	return &configTransportExecution{
		reader:     strings.NewReader(configTransportOutput),
		completion: sshutil.CommandCompletion{ExitCodeKnown: true, RemoteExited: true},
		cancelErr:  sshutil.ErrCommandFailed,
	}
}

func (execution *configTransportExecution) Read(p []byte) (int, error) {
	return execution.reader.Read(p)
}

func (execution *configTransportExecution) finish(err error) error {
	execution.once.Do(func() {
		if execution.onFinish != nil {
			execution.onFinish()
		}
		execution.resultErr = err
	})
	return execution.resultErr
}

func (execution *configTransportExecution) Join() (sshutil.CommandCompletion, error) {
	execution.joinCalls.Add(1)
	return execution.completion, execution.finish(execution.joinErr)
}

func (execution *configTransportExecution) JoinPrefix() (sshutil.CommandCompletion, error) {
	execution.prefixCalls.Add(1)
	return execution.completion, execution.finish(execution.prefixErr)
}

func (execution *configTransportExecution) Cancel() error {
	execution.cancelCalls.Add(1)
	return execution.finish(execution.cancelErr)
}

func configTransportHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func configTransportInvocation(managed bool, config []byte) CommandInvocation {
	invocation := CommandInvocation{
		Tool: ToolRclone, Operation: OperationRcloneCat, Purpose: CommandPurposeRead,
		Args: []string{"cat", "--"}, SecretStdin: config,
		PrivateLocator: "remote:root/file", Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}},
	}
	if managed {
		locator, err := NewRclonePrivateLocator("remote:managed/file")
		if err != nil {
			panic(err)
		}
		invocation.Operation = OperationRcloneManagedCat
		invocation.Args = nil
		invocation.PrivateLocator = ""
		invocation.RcloneSource = &locator
		invocation.RcloneLowLevelRetries = 3
		invocation.AbsoluteDeadline = time.Now().UTC().Add(time.Minute)
	}
	return invocation
}

func configTransportForTest(t *testing.T, home string, runner remoteCommandRunner, factory rcloneCommandConfigSessionFactory) *SSHCommandTransport {
	t.Helper()
	if factory == nil {
		factory = configStoreForTest(t, home, nil).factory
	}
	transport, err := newSSHCommandTransportWithConfig(
		func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error) {
			return runner, io.NopCloser(strings.NewReader("")), nil
		}, factory, func() (int, error) { return 1, nil }, ToolBinaries{Rclone: "rclone", Restic: "restic"},
	)
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

// Inspect the file available to the command, not a copy of the invocation's
// configuration. Error messages intentionally avoid including private data.
func inspectTransportConfig(home string, expected []byte, spec sshutil.CommandSpec) (string, error) {
	if spec.Binary != "rclone" || spec.SecretStdin != nil {
		return "", errors.New("bound command did not use the Rclone config file contract")
	}
	var configPath string
	for index, arg := range spec.Args {
		if strings.Contains(arg, configTransportCanary) {
			return "", errors.New("config secret appeared in command arguments")
		}
		if arg == "--config" {
			if configPath != "" || index+1 >= len(spec.Args) {
				return "", errors.New("missing or repeated config argument")
			}
			configPath = spec.Args[index+1]
		}
	}
	if configPath == "" || filepath.Base(configPath) != "rclone.conf" ||
		filepath.Dir(filepath.Dir(configPath)) != filepath.Join(home, ".xirang", "rclone-command-config") {
		return "", errors.New("config path was not owned by this command")
	}
	file, err := os.Lstat(configPath)
	if err != nil || !file.Mode().IsRegular() || file.Mode().Perm() != 0o600 {
		return "", errors.New("config is not a private regular file")
	}
	dir, err := os.Lstat(filepath.Dir(configPath))
	if err != nil || !dir.IsDir() || dir.Mode().Perm() != 0o700 {
		return "", errors.New("config directory is not private")
	}
	actual, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(actual, expected) {
		return "", errors.New("command did not receive the exact config bytes")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(configPath), ".owner-v1")); err != nil {
		return "", errors.New("config ownership marker is missing")
	}
	serialized, err := json.Marshal(spec)
	if err != nil || bytes.Contains(serialized, []byte(configTransportCanary)) {
		return "", errors.New("command specification disclosed config")
	}
	return configPath, nil
}

func expectConfigRemoved(t *testing.T, configPath string) {
	t.Helper()
	if configPath == "" {
		t.Fatal("command did not observe a config path")
	}
	if _, err := os.Lstat(filepath.Dir(configPath)); !os.IsNotExist(err) {
		t.Fatalf("command config directory was not removed: %v", err)
	}
}

func expectConfigRetained(t *testing.T, configPath string, expected []byte) {
	t.Helper()
	if configPath == "" {
		t.Fatal("command did not observe a config path")
	}
	actual, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("config was not retained intact: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(configPath), ".owner-v1")); err != nil {
		t.Fatalf("retained config lost its owner marker: %v", err)
	}
}

func invokeConfiguredForTest(transport *SSHCommandTransport, mode string, ctx context.Context, invocation CommandInvocation, limits OperationLimits) ([]byte, error) {
	switch mode {
	case "Run":
		output, err := transport.Run(ctx, invocation, limits)
		return output.Stdout, err
	case "Open":
		handle, err := transport.Open(ctx, invocation, limits, 4096)
		if err != nil {
			return nil, err
		}
		output, readErr := io.ReadAll(handle)
		return output, errors.Join(readErr, handle.Close())
	case "OpenExecution":
		execution, err := transport.OpenExecution(ctx, invocation, limits, 4096)
		if err != nil {
			return nil, err
		}
		output, readErr := io.ReadAll(execution)
		completion, joinErr := execution.Join()
		if joinErr == nil && (!completion.ExitCodeKnown || completion.ExitCode != 0) {
			joinErr = sshutil.ErrCommandFailed
		}
		return output, errors.Join(readErr, joinErr)
	default:
		panic("unknown transport mode")
	}
}

func TestRcloneCommandTransportBoundDeliveryAndCleanup(t *testing.T) {
	for _, mode := range []string{"Run", "Open", "OpenExecution"} {
		for _, managed := range []bool{false, true} {
			for _, newline := range []bool{false, true} {
				name := fmt.Sprintf("%s/managed=%t/newline=%t", mode, managed, newline)
				t.Run(name, func(t *testing.T) {
					home := configTransportHome(t)
					config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
					if newline {
						config = append(config, '\n')
					}
					var configPath string
					runner := &configTransportRunner{openExecution: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
						var err error
						configPath, err = inspectTransportConfig(home, config, spec)
						if err != nil {
							return nil, err
						}
						return newConfigTransportExecution(), nil
					}}
					transport := configTransportForTest(t, home, runner, nil)
					output, err := invokeConfiguredForTest(transport, mode, context.Background(), configTransportInvocation(managed, config), testOperationLimits())
					if err != nil || string(output) != configTransportOutput {
						t.Fatalf("consumer output=%q error=%v", output, err)
					}
					expectConfigRemoved(t, configPath)
				})
			}
		}
	}
}

func TestRcloneCommandTransportGateCoversExecutionAndCleanup(t *testing.T) {
	for _, mode := range []string{"Run", "Open", "OpenExecution"} {
		t.Run(mode, func(t *testing.T) {
			home := configTransportHome(t)
			config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
			invocation := configTransportInvocation(false, config)
			base := configStoreForTest(t, home, nil).factory
			var configCalls atomic.Int32
			cleanupEntered, releaseCleanup := make(chan struct{}), make(chan struct{})
			var cleanupOnce sync.Once
			defer cleanupOnce.Do(func() { close(releaseCleanup) })
			factory := func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
				if configCalls.Add(1) == 2 {
					close(cleanupEntered)
					select {
					case <-releaseCleanup:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return base(ctx, access, purpose)
			}
			started := make(chan string, 1)
			finishEntered, releaseFinish := make(chan struct{}), make(chan struct{})
			var finishOnce sync.Once
			defer finishOnce.Do(func() { close(releaseFinish) })
			var commands atomic.Int32
			runner := &configTransportRunner{openExecution: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
				configPath, err := inspectTransportConfig(home, config, spec)
				if err != nil {
					return nil, err
				}
				execution := newConfigTransportExecution()
				if commands.Add(1) == 1 {
					started <- configPath
					execution.onFinish = func() { close(finishEntered); <-releaseFinish }
				}
				return execution, nil
			}}
			transport := configTransportForTest(t, home, runner, factory)
			result := make(chan error, 1)
			go func() {
				_, err := invokeConfiguredForTest(transport, mode, context.Background(), invocation, testOperationLimits())
				result <- err
			}()
			var configPath string
			select {
			case configPath = <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("first command did not start")
			}
			awaitStagingSignal(t, finishEntered)
			expectConfigRetained(t, configPath, config)
			assertContenderBlocked := func(wantCalls int32) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				defer cancel()
				_, err := transport.Run(ctx, invocation, testOperationLimits())
				if !errors.Is(err, context.DeadlineExceeded) || configCalls.Load() != wantCalls {
					t.Fatalf("contender entered staging before ownership ended: calls=%d error=%v", configCalls.Load(), err)
				}
			}
			assertContenderBlocked(1)
			finishOnce.Do(func() { close(releaseFinish) })
			awaitStagingSignal(t, cleanupEntered)
			expectConfigRetained(t, configPath, config)
			assertContenderBlocked(2)
			cleanupOnce.Do(func() { close(releaseCleanup) })
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("first command failed: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("first command did not finish cleanup")
			}
			expectConfigRemoved(t, configPath)
			output, err := transport.Run(context.Background(), invocation, testOperationLimits())
			if err != nil || string(output.Stdout) != configTransportOutput || commands.Load() != 2 || configCalls.Load() != 4 {
				t.Fatalf("gate was not reusable: commands=%d sessions=%d error=%v", commands.Load(), configCalls.Load(), err)
			}
		})
	}
}

func TestRcloneCommandTransportSharesAbsoluteDeadline(t *testing.T) {
	for _, mode := range []string{"Run", "Open", "OpenExecution"} {
		for _, tc := range []struct {
			name     string
			earliest string
			managed  bool
		}{
			{name: "ordinary-caller", earliest: "caller"},
			{name: "ordinary-operation", earliest: "operation"},
			{name: "managed-caller", earliest: "caller", managed: true},
			{name: "managed-operation", earliest: "operation", managed: true},
			{name: "managed-publication", earliest: "publication", managed: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				home := configTransportHome(t)
				config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
				invocation := configTransportInvocation(tc.managed, config)
				now := time.Now()
				callerDeadline := now.Add(15 * time.Second)
				if tc.managed {
					invocation.AbsoluteDeadline = now.UTC().Add(20 * time.Second)
				}
				limits := testOperationLimits()
				limits.Timeout = 12 * time.Second
				switch tc.earliest {
				case "caller":
					callerDeadline = now.Add(5 * time.Second)
				case "operation":
					limits.Timeout = 5 * time.Second
				case "publication":
					invocation.AbsoluteDeadline = now.UTC().Add(5 * time.Second)
				}
				ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
				defer cancel()
				base := configStoreForTest(t, home, nil).factory
				var calls atomic.Int32
				var stagingDeadline, runnerDeadline, executionDeadline time.Time
				factory := func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
					if calls.Add(1) == 1 {
						stagingDeadline, _ = ctx.Deadline()
					}
					return base(ctx, access, purpose)
				}
				var configPath string
				runner := &configTransportRunner{openExecution: func(ctx context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
					executionDeadline, _ = ctx.Deadline()
					var err error
					configPath, err = inspectTransportConfig(home, config, spec)
					if err != nil {
						return nil, err
					}
					return newConfigTransportExecution(), nil
				}}
				transport, err := newSSHCommandTransportWithConfig(func(ctx context.Context, _ RemoteCommandAccess, _ string) (remoteCommandRunner, io.Closer, error) {
					runnerDeadline, _ = ctx.Deadline()
					return runner, io.NopCloser(strings.NewReader("")), nil
				}, factory, func() (int, error) { return 1, nil }, ToolBinaries{Rclone: "rclone"})
				if err != nil {
					t.Fatal(err)
				}
				before := time.Now()
				output, err := invokeConfiguredForTest(transport, mode, ctx, invocation, limits)
				after := time.Now()
				if err != nil || string(output) != configTransportOutput {
					t.Fatalf("command failed: %v", err)
				}
				if stagingDeadline.IsZero() || !stagingDeadline.Equal(runnerDeadline) || !stagingDeadline.Equal(executionDeadline) {
					t.Fatalf("deadline changed between staging, dial and execution: %v %v %v", stagingDeadline, runnerDeadline, executionDeadline)
				}
				switch tc.earliest {
				case "caller":
					if !stagingDeadline.Equal(callerDeadline) {
						t.Fatal("caller deadline was not applied")
					}
				case "publication":
					if !stagingDeadline.Equal(invocation.AbsoluteDeadline) {
						t.Fatal("publication deadline was not applied")
					}
				case "operation":
					if stagingDeadline.Before(before.Add(limits.Timeout)) || stagingDeadline.After(after.Add(limits.Timeout)) {
						t.Fatal("operation deadline was not established at entry")
					}
				}
				expectConfigRemoved(t, configPath)
			})
		}
	}
}

func TestRcloneCommandTransportDeadlineExpiresDuringStaging(t *testing.T) {
	home := configTransportHome(t)
	var commands atomic.Int32
	entered := make(chan struct{})
	factory := func(ctx context.Context, _ RemoteCommandAccess, _ string) (stagedPayloadSession, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	runner := &configTransportRunner{openExecution: func(context.Context, sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
		commands.Add(1)
		return newConfigTransportExecution(), nil
	}}
	transport := configTransportForTest(t, home, runner, factory)
	limits := testOperationLimits()
	limits.Timeout = 150 * time.Millisecond
	result := make(chan error, 1)
	go func() {
		_, err := transport.Run(context.Background(), configTransportInvocation(false, []byte(configTransportCanary)), limits)
		result <- err
	}()
	awaitStagingSignal(t, entered)
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) || commands.Load() != 0 {
			t.Fatalf("expired staging started a command: commands=%d error=%v", commands.Load(), err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("staging did not stop at the operation deadline")
	}
}

func TestRcloneCommandTransportExitEvidenceControlsCleanup(t *testing.T) {
	for _, scenario := range []string{"pre-start", "setup-failure", "unmarked-open-failure", "ambiguous-start", "unknown-exit", "confirmed-failed-exit"} {
		t.Run(scenario, func(t *testing.T) {
			home := configTransportHome(t)
			config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
			base := configStoreForTest(t, home, nil).factory
			var sessions atomic.Int32
			factory := func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
				sessions.Add(1)
				return base(ctx, access, purpose)
			}
			var configPath string
			cause := errors.New("fake private failure " + configTransportCanary + " " + home)
			runner := &configTransportRunner{openExecution: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
				var err error
				configPath, err = inspectTransportConfig(home, config, spec)
				if err != nil {
					return nil, err
				}
				if scenario == "ambiguous-start" {
					return nil, errors.Join(sshutil.ErrCommandStart, cause)
				}
				if scenario == "setup-failure" {
					return nil, errors.Join(sshutil.ErrCommandNotStarted, sshutil.ErrCommandFailed, cause)
				}
				if scenario == "unmarked-open-failure" {
					return nil, cause
				}
				execution := newConfigTransportExecution()
				execution.joinErr = errors.Join(sshutil.ErrCommandFailed, cause)
				execution.completion.Stderr = []byte("FAKE_REMOTE_STDERR_FOR_TEST_ONLY")
				execution.completion.RemoteExited = scenario == "confirmed-failed-exit"
				return execution, nil
			}}
			transport, err := newSSHCommandTransportWithConfig(func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error) {
				if scenario == "pre-start" {
					return nil, nil, cause
				}
				return runner, io.NopCloser(strings.NewReader("")), nil
			}, factory, func() (int, error) { return 1, nil }, ToolBinaries{Rclone: "rclone"})
			if err != nil {
				t.Fatal(err)
			}
			output, err := transport.Run(context.Background(), configTransportInvocation(false, config), testOperationLimits())
			if err == nil || !errors.Is(err, cause) || len(output.Stdout) != 0 {
				t.Fatalf("failure did not reach the consumer: error=%v", err)
			}
			if scenario == "ambiguous-start" && !errors.Is(err, sshutil.ErrCommandStart) {
				t.Fatal("ambiguous start classification was lost")
			}
			for _, private := range []string{configTransportCanary, home, "FAKE_REMOTE_STDERR_FOR_TEST_ONLY", configPath} {
				if private != "" && strings.Contains(err.Error(), private) {
					t.Fatal("error disclosed config, path or remote stderr")
				}
			}
			switch scenario {
			case "pre-start":
				entries, readErr := os.ReadDir(filepath.Join(home, ".xirang", "rclone-command-config"))
				if readErr != nil || len(entries) != 0 || sessions.Load() != 2 {
					t.Fatalf("pre-start failure did not clean owned config: sessions=%d entries=%d error=%v", sessions.Load(), len(entries), readErr)
				}
			case "confirmed-failed-exit", "setup-failure":
				expectConfigRemoved(t, configPath)
				if sessions.Load() != 2 {
					t.Fatalf("safe cleanup did not run: sessions=%d", sessions.Load())
				}
			default:
				expectConfigRetained(t, configPath, config)
				if sessions.Load() != 1 {
					t.Fatalf("uncertain execution attempted cleanup: sessions=%d", sessions.Load())
				}
			}
		})
	}
}

func TestRcloneCommandTransportCleanupFailureOverridesSuccess(t *testing.T) {
	for _, mode := range []string{"Run", "Open", "OpenExecution"} {
		for _, commandFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/commandFailure=%t", mode, commandFailure), func(t *testing.T) {
				home := configTransportHome(t)
				config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
				var configPath string
				runner := &configTransportRunner{openExecution: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
					var err error
					configPath, err = inspectTransportConfig(home, config, spec)
					if err != nil {
						return nil, err
					}
					if err := os.WriteFile(filepath.Join(filepath.Dir(configPath), "unexpected"), []byte("keep"), 0o600); err != nil {
						return nil, err
					}
					execution := newConfigTransportExecution()
					if commandFailure {
						execution.joinErr = sshutil.ErrCommandFailed
					}
					return execution, nil
				}}
				transport := configTransportForTest(t, home, runner, nil)
				output, err := invokeConfiguredForTest(transport, mode, context.Background(), configTransportInvocation(false, config), testOperationLimits())
				if err == nil || !errors.Is(err, backupasset.ErrProviderUnavailable) {
					t.Fatalf("unsafe cleanup was reported as success: %v", err)
				}
				if commandFailure && !errors.Is(err, sshutil.ErrCommandFailed) {
					t.Fatal("cleanup failure discarded command failure")
				}
				if mode == "Run" && len(output) != 0 {
					t.Fatal("Run returned successful output after cleanup failure")
				}
				if strings.Contains(err.Error(), home) || strings.Contains(err.Error(), configTransportCanary) {
					t.Fatal("cleanup error disclosed private information")
				}
				expectConfigRetained(t, configPath, config)
				if _, statErr := os.Lstat(filepath.Join(filepath.Dir(configPath), "unexpected")); statErr != nil {
					t.Fatal("cleanup deleted unknown content")
				}
			})
		}
	}
}

func TestRcloneCommandTransportPrefixClosePreservesExitEvidence(t *testing.T) {
	for _, scenario := range []string{"confirmed-prefix", "remote-failure", "unknown-exit"} {
		t.Run(scenario, func(t *testing.T) {
			home := configTransportHome(t)
			config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
			var configPath string
			var execution *configTransportExecution
			runner := &configTransportRunner{openExecution: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
				var err error
				configPath, err = inspectTransportConfig(home, config, spec)
				if err != nil {
					return nil, err
				}
				execution = newConfigTransportExecution()
				if scenario != "confirmed-prefix" {
					execution.prefixErr = sshutil.ErrCommandFailed
				}
				execution.completion.RemoteExited = scenario != "unknown-exit"
				return execution, nil
			}}
			transport := configTransportForTest(t, home, runner, nil)
			handle, err := transport.Open(context.Background(), configTransportInvocation(false, config), testOperationLimits(), 4)
			if err != nil {
				t.Fatal(err)
			}
			prefix := make([]byte, 4)
			if _, err := io.ReadFull(handle, prefix); err != nil || string(prefix) != "remo" {
				t.Fatalf("read prefix=%q error=%v", prefix, err)
			}
			closer, ok := handle.(interface{ ClosePrefix() error })
			if !ok {
				t.Fatal("bound read handle has no prefix completion")
			}
			err = closer.ClosePrefix()
			if execution.prefixCalls.Load() != 1 {
				t.Fatal("prefix completion did not reach the command")
			}
			if scenario == "confirmed-prefix" {
				if err != nil {
					t.Fatalf("confirmed prefix failed: %v", err)
				}
				expectConfigRemoved(t, configPath)
			} else {
				if !errors.Is(err, sshutil.ErrCommandFailed) {
					t.Fatalf("prefix hid command failure: %v", err)
				}
				if scenario == "unknown-exit" {
					expectConfigRetained(t, configPath, config)
				} else {
					expectConfigRemoved(t, configPath)
				}
			}
		})
	}
}

func TestRcloneCommandTransportConcurrentFinalizationCleansOnce(t *testing.T) {
	for _, mode := range []string{"Close", "Cancel"} {
		t.Run(mode, func(t *testing.T) {
			home := configTransportHome(t)
			config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
			base := configStoreForTest(t, home, nil).factory
			var sessions atomic.Int32
			factory := func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
				sessions.Add(1)
				return base(ctx, access, purpose)
			}
			var configPath string
			var underlying *configTransportExecution
			runner := &configTransportRunner{openExecution: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
				var err error
				configPath, err = inspectTransportConfig(home, config, spec)
				if err != nil {
					return nil, err
				}
				underlying = newConfigTransportExecution()
				return underlying, nil
			}}
			transport := configTransportForTest(t, home, runner, factory)
			invocation := configTransportInvocation(false, config)
			var finish func() error
			var execution CommandExecution
			if mode == "Close" {
				handle, err := transport.Open(context.Background(), invocation, testOperationLimits(), 4096)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadAll(handle); err != nil {
					t.Fatal(err)
				}
				finish = handle.Close
			} else {
				var err error
				execution, err = transport.OpenExecution(context.Background(), invocation, testOperationLimits(), 4096)
				if err != nil {
					t.Fatal(err)
				}
				finish = execution.Cancel
			}
			const callers = 16
			results := make(chan error, callers)
			start := make(chan struct{})
			for range callers {
				go func() { <-start; results <- finish() }()
			}
			close(start)
			for range callers {
				select {
				case err := <-results:
					if (mode == "Close" && err != nil) || (mode == "Cancel" && !errors.Is(err, sshutil.ErrCommandFailed)) {
						t.Fatalf("inconsistent finalization error: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("concurrent finalization did not complete")
				}
			}
			if mode == "Cancel" {
				if _, err := execution.Join(); !errors.Is(err, sshutil.ErrCommandFailed) {
					t.Fatalf("Join lost the earlier cancellation: %v", err)
				}
				if underlying.cancelCalls.Load() != 1 {
					t.Fatalf("command cancelled %d times", underlying.cancelCalls.Load())
				}
			} else if underlying.joinCalls.Load() != 1 {
				t.Fatalf("command joined %d times", underlying.joinCalls.Load())
			}
			if sessions.Load() != 2 {
				t.Fatalf("config cleanup sessions=%d, want one preparation and one cleanup", sessions.Load())
			}
			expectConfigRemoved(t, configPath)
		})
	}
}

func TestRcloneCommandTransportRejectsCallerFlagsBeforeSideEffects(t *testing.T) {
	for _, mode := range []string{"Run", "Open", "OpenExecution"} {
		for _, variant := range []string{"stdin config", "file config", "equals config", "extra flag", "managed flags", "missing node", "missing purpose", "invalid limits"} {
			t.Run(mode+"/"+variant, func(t *testing.T) {
				home := configTransportHome(t)
				invocation := configTransportInvocation(false, []byte(configTransportCanary))
				limits := testOperationLimits()
				switch variant {
				case "stdin config":
					invocation.Args = []string{"--config", "/dev/stdin", "cat", "--"}
				case "file config":
					invocation.Args = []string{"--config", "/tmp/attacker", "cat", "--"}
				case "equals config":
					invocation.Args = []string{"--config=/tmp/attacker", "cat", "--"}
				case "extra flag":
					invocation.Args = []string{"cat", "--log-file", "/tmp/attacker", "--"}
				case "managed flags":
					invocation = configTransportInvocation(true, []byte(configTransportCanary))
					invocation.Args = []string{"--config", "/tmp/attacker"}
				case "missing node":
					invocation.Runtime = nil
				case "missing purpose":
					invocation.Purpose = ""
				case "invalid limits":
					limits.Timeout = 0
				}
				var configCalls, commandCalls atomic.Int32
				base := configStoreForTest(t, home, nil).factory
				factory := func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
					configCalls.Add(1)
					return base(ctx, access, purpose)
				}
				runner := &configTransportRunner{openExecution: func(context.Context, sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
					return newConfigTransportExecution(), nil
				}}
				transport, err := newSSHCommandTransportWithConfig(func(context.Context, RemoteCommandAccess, string) (remoteCommandRunner, io.Closer, error) {
					commandCalls.Add(1)
					return runner, io.NopCloser(strings.NewReader("")), nil
				}, factory, func() (int, error) { return 1, nil }, ToolBinaries{Rclone: "rclone"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = invokeConfiguredForTest(transport, mode, context.Background(), invocation, limits)
				if !errors.Is(err, ErrUnsafeInvocation) || configCalls.Load() != 0 || commandCalls.Load() != 0 {
					t.Fatalf("unsafe input reached a side effect: config=%d commands=%d error=%v", configCalls.Load(), commandCalls.Load(), err)
				}
				if _, statErr := os.Lstat(filepath.Join(home, ".xirang")); !os.IsNotExist(statErr) {
					t.Fatalf("rejected input created a config root: %v", statErr)
				}
			})
		}
	}
}

func TestRcloneCommandTransportNodeDefaultAndResticContracts(t *testing.T) {
	for _, kind := range []string{"node_default", "restic"} {
		t.Run(kind, func(t *testing.T) {
			home := configTransportHome(t)
			base := configStoreForTest(t, home, nil).factory
			var configCalls atomic.Int32
			factory := func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
				configCalls.Add(1)
				return base(ctx, access, purpose)
			}
			var observed sshutil.CommandSpec
			runner := &configTransportRunner{run: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandResult, error) {
				observed = spec
				return sshutil.CommandResult{Stdout: []byte("unchanged")}, nil
			}}
			transport := configTransportForTest(t, home, runner, factory)
			invocation := configTransportInvocation(false, nil)
			if kind == "restic" {
				invocation = CommandInvocation{
					Tool: ToolRestic, Operation: OperationResticConfig, Purpose: CommandPurposeProbe,
					Args:        []string{"--password-file", "/dev/stdin", "cat", "config"},
					SecretStdin: []byte("FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"), PrivateLocator: "repository:root",
					Runtime: &RemoteCommandAccess{Node: model.Node{ID: 9}},
				}
			}
			output, err := transport.Run(context.Background(), invocation, testOperationLimits())
			if err != nil || string(output.Stdout) != "unchanged" || configCalls.Load() != 0 {
				t.Fatalf("unchanged command failed: sessions=%d error=%v", configCalls.Load(), err)
			}
			if kind == "node_default" {
				if observed.SecretStdin != nil || strings.Contains(strings.Join(observed.Args, " "), "--config") {
					t.Fatal("node_default acquired a bound config")
				}
			} else if observed.SecretStdin == nil || !observed.SecretStdin.AppendNewline || !bytes.Equal(observed.SecretStdin.Value, invocation.SecretStdin) ||
				!strings.Contains(strings.Join(observed.Args, " "), "--password-file /dev/stdin") {
				t.Fatal("Restic password stdin framing changed")
			}
			if _, statErr := os.Lstat(filepath.Join(home, ".xirang")); !os.IsNotExist(statErr) {
				t.Fatalf("unchanged command created a config root: %v", statErr)
			}
		})
	}
}

func TestRcloneCommandTransportCancellationUsesIndependentCleanupContext(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprintf("remoteExited=%t", confirmed), func(t *testing.T) {
			home := configTransportHome(t)
			config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
			base := configStoreForTest(t, home, nil).factory
			var sessions atomic.Int32
			var cleanupDeadline time.Time
			var cleanupContextErr error
			factory := func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
				if sessions.Add(1) == 2 {
					cleanupDeadline, _ = ctx.Deadline()
					cleanupContextErr = ctx.Err()
				}
				return base(ctx, access, purpose)
			}
			var configPath string
			var underlying *configTransportExecution
			runner := &configTransportRunner{openExecution: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
				var err error
				configPath, err = inspectTransportConfig(home, config, spec)
				if err != nil {
					return nil, err
				}
				underlying = newConfigTransportExecution()
				underlying.completion.RemoteExited = confirmed
				underlying.cancelErr = context.Canceled
				return underlying, nil
			}}
			transport := configTransportForTest(t, home, runner, factory)
			ctx, cancel := context.WithCancel(context.Background())
			execution, err := transport.OpenExecution(ctx, configTransportInvocation(false, config), testOperationLimits(), 4096)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			cancel()
			before := time.Now()
			err = execution.Cancel()
			if !errors.Is(err, context.Canceled) || underlying.cancelCalls.Load() != 1 {
				t.Fatalf("cancel outcome lost: calls=%d error=%v", underlying.cancelCalls.Load(), err)
			}
			if confirmed {
				if sessions.Load() != 2 || cleanupContextErr != nil || cleanupDeadline.Before(before) ||
					cleanupDeadline.After(before.Add(sshutil.CommandExecutionJoinTimeout+time.Second)) {
					t.Fatalf("cleanup did not use an independent bounded context: sessions=%d context=%v deadline=%v", sessions.Load(), cleanupContextErr, cleanupDeadline)
				}
				expectConfigRemoved(t, configPath)
			} else {
				if sessions.Load() != 1 || !errors.Is(err, backupasset.ErrProviderUnavailable) {
					t.Fatalf("uncertain cancellation attempted cleanup: sessions=%d error=%v", sessions.Load(), err)
				}
				expectConfigRetained(t, configPath, config)
			}
		})
	}
}

type configExecutionWithoutPrefix struct {
	underlying *configTransportExecution
}

func (execution *configExecutionWithoutPrefix) Read(p []byte) (int, error) {
	return execution.underlying.Read(p)
}

func (execution *configExecutionWithoutPrefix) Join() (sshutil.CommandCompletion, error) {
	return execution.underlying.Join()
}

func (execution *configExecutionWithoutPrefix) Cancel() error {
	return execution.underlying.Cancel()
}

func TestRcloneCommandTransportRejectsExecutionWithoutPrefixSupport(t *testing.T) {
	home := configTransportHome(t)
	config := []byte("[remote]\ntype = local\n# " + configTransportCanary)
	var configPath string
	var underlying *configTransportExecution
	runner := &configTransportRunner{openExecution: func(_ context.Context, spec sshutil.CommandSpec) (sshutil.CommandExecutionStream, error) {
		var err error
		configPath, err = inspectTransportConfig(home, config, spec)
		if err != nil {
			return nil, err
		}
		underlying = newConfigTransportExecution()
		return &configExecutionWithoutPrefix{underlying: underlying}, nil
	}}
	transport := configTransportForTest(t, home, runner, nil)
	handle, err := transport.Open(context.Background(), configTransportInvocation(false, config), testOperationLimits(), 4096)
	if handle != nil || !errors.Is(err, backupasset.ErrProviderUnavailable) {
		t.Fatalf("missing prefix support was accepted: error=%v", err)
	}
	if underlying == nil || underlying.cancelCalls.Load() != 1 {
		t.Fatal("unsupported execution was not stopped and joined")
	}
	expectConfigRemoved(t, configPath)
}
