package capabilities

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type RunnerConfig struct {
	WorkspaceRoot string
	StdoutLimit   int
	StderrLimit   int
	GracePeriod   time.Duration
}

type Runner struct {
	config     RunnerConfig
	resolver   executableResolver
	testDirect bool
}

func NewRunner(config RunnerConfig) (*Runner, error) {
	if err := validateRunnerConfig(config); err != nil {
		return nil, err
	}
	info, err := inspectWorkspaceMount(config.WorkspaceRoot)
	if err != nil || validateWorkspaceMount(info) != nil {
		return nil, ErrSecureWorkspaceUnavailable
	}
	if err := cleanupOrphanWorkspaces(config.WorkspaceRoot); err != nil {
		return nil, ErrSecureWorkspaceUnavailable
	}
	return &Runner{config: config, resolver: productionExecutableResolver}, nil
}

func newRunnerForTest(config RunnerConfig, resolver executableResolver) *Runner {
	return &Runner{config: config, resolver: resolver, testDirect: true}
}

func validateRunnerConfig(config RunnerConfig) error {
	if !cleanWorkspaceRoot(config.WorkspaceRoot) || config.StdoutLimit <= 0 || config.StdoutLimit > 64<<10 ||
		config.StderrLimit <= 0 || config.StderrLimit > 64<<10 || config.GracePeriod <= 0 || config.GracePeriod > 5*time.Second {
		return ErrInvalidInvocation
	}
	info, err := os.Stat(config.WorkspaceRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return ErrSecureWorkspaceUnavailable
	}
	return nil
}

func cleanupOrphanWorkspaces(root string) error {
	if !cleanWorkspaceRoot(root) {
		return ErrSecureWorkspaceUnavailable
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "job-") || len(name) <= len("job-") || strings.ContainsAny(name, "/\\\x00\r\n") {
			continue
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			return ErrSecureWorkspaceUnavailable
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return ErrSecureWorkspaceUnavailable
		}
		if err := os.RemoveAll(path); err != nil {
			return ErrSecureWorkspaceUnavailable
		}
	}
	return nil
}

func (runner *Runner) Run(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	return runner.RunInput(ctx, invocation, bytes.NewReader(nil))
}

func (runner *Runner) RunInput(ctx context.Context, invocation ToolInvocation, input io.Reader) (ToolResult, error) {
	if runner == nil || ctx == nil || runner.resolver == nil {
		return ToolResult{}, ErrInvalidInvocation
	}
	if input == nil {
		return ToolResult{}, ErrInvalidInvocation
	}
	if err := validateRunnerConfig(runner.config); err != nil {
		return ToolResult{}, err
	}
	if err := invocation.Validate(); err != nil {
		return ToolResult{}, err
	}
	workspace, err := os.MkdirTemp(runner.config.WorkspaceRoot, "job-")
	if err != nil {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	if err := os.Chmod(workspace, 0o700); err != nil {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	home := filepath.Join(workspace, "home")
	output := filepath.Join(workspace, "output")
	if err := os.Mkdir(home, 0o700); err != nil {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	var boundedInput *boundedInputReader
	inputPath := ""
	switch invocation.InputMode {
	case ToolInputPipe:
		boundedInput = &boundedInputReader{source: input, maximum: invocation.Limits.MaxInputBytes}
	case ToolInputPath:
		inputPath = filepath.Join(workspace, "input.bin")
		if err := materializeInput(inputPath, input, invocation.Limits.MaxInputBytes); err != nil {
			return ToolResult{}, err
		}
	default:
		return ToolResult{}, ErrInvalidInvocation
	}

	executable, err := runner.resolver(invocation.ExecutableID)
	if err != nil || !filepath.IsAbs(executable) {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	args := invocation.Args
	if !runner.testDirect {
		args = append([]string{"--executable-id=" + string(invocation.ExecutableID), "--arg-profile=" + string(invocation.ArgProfile)}, args...)
	}
	command := exec.Command(executable, args...)
	command.Dir = workspace
	command.Env = runtimeEnvironment(invocation.Environment, workspace, output, invocation.InputMode, inputPath, invocation.Limits)
	if boundedInput != nil {
		command.Stdin = boundedInput
	}
	configureToolProcess(command)
	stdout := newBoundedBuffer(runner.config.StdoutLimit)
	stderr := newBoundedBuffer(runner.config.StderrLimit)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return ToolResult{}, sanitizeProcessError(err)
	}

	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	deadlineCtx, cancel := context.WithTimeout(ctx, invocation.Limits.WallTime)
	defer cancel()
	var waitErr error
	select {
	case waitErr = <-waited:
		if err := killAndWaitToolProcessGroup(command.Process, runner.config.GracePeriod); err != nil {
			return ToolResult{}, ErrToolFailed
		}
	case <-deadlineCtx.Done():
		_ = signalToolProcessGroup(command.Process, syscall.SIGTERM)
		timer := time.NewTimer(runner.config.GracePeriod)
		select {
		case waitErr = <-waited:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = signalToolProcessGroup(command.Process, syscall.SIGKILL)
			waitErr = <-waited
		}
		if err := killAndWaitToolProcessGroup(command.Process, runner.config.GracePeriod); err != nil {
			return ToolResult{}, ErrToolFailed
		}
		_ = waitErr
		if ctx.Err() != nil {
			return ToolResult{}, ctx.Err()
		}
		return ToolResult{}, ErrToolTimeout
	}
	if boundedInput != nil && boundedInput.exceeded {
		return ToolResult{}, ErrInputLimit
	}
	exitCode := 0
	if waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) {
			return ToolResult{}, ErrToolFailed
		}
		exitCode = exitError.ExitCode()
		if !allowedExitCode(invocation, exitCode) {
			return ToolResult{}, ErrToolFailed
		}
	}
	outputs, err := readClosedOutputs(output, invocation.OutputSpec, invocation.Limits.MaxOutputBytes)
	if err != nil {
		return ToolResult{}, err
	}
	result := ToolResult{
		ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String(),
		StdoutTruncated: stdout.Truncated(), StderrTruncated: stderr.Truncated(), Outputs: outputs,
	}
	if invocation.ExecutableID == ExecutableClamScan && invocation.ArgProfile == ArgsClamScan && exitCode == 1 {
		result.Stdout, err = canonicalClamFindingOutput(result, inputPath)
		if err != nil {
			return ToolResult{}, err
		}
	}
	return result, nil
}

func (runner *Runner) RunInputStream(
	ctx context.Context,
	invocation ToolInvocation,
	input io.Reader,
	consume func(io.Reader) error,
) (ToolResult, error) {
	if runner == nil || ctx == nil || runner.resolver == nil || input == nil || consume == nil ||
		invocation.InputMode != ToolInputPipe {
		return ToolResult{}, ErrInvalidInvocation
	}
	if err := validateRunnerConfig(runner.config); err != nil {
		return ToolResult{}, err
	}
	if err := invocation.Validate(); err != nil {
		return ToolResult{}, err
	}
	workspace, err := os.MkdirTemp(runner.config.WorkspaceRoot, "job-")
	if err != nil {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	if err := os.Chmod(workspace, 0o700); err != nil {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	home := filepath.Join(workspace, "home")
	output := filepath.Join(workspace, "output")
	if err := os.Mkdir(home, 0o700); err != nil {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}

	boundedInput := &boundedInputReader{source: input, maximum: invocation.Limits.MaxInputBytes}
	executable, err := runner.resolver(invocation.ExecutableID)
	if err != nil || !filepath.IsAbs(executable) {
		return ToolResult{}, ErrSecureWorkspaceUnavailable
	}
	args := invocation.Args
	if !runner.testDirect {
		args = append([]string{"--executable-id=" + string(invocation.ExecutableID), "--arg-profile=" + string(invocation.ArgProfile)}, args...)
	}
	command := exec.Command(executable, args...)
	command.Dir = workspace
	command.Env = runtimeEnvironment(invocation.Environment, workspace, output, invocation.InputMode, "", invocation.Limits)
	command.Stdin = boundedInput
	configureToolProcess(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return ToolResult{}, ErrToolFailed
	}
	stderr := newBoundedBuffer(runner.config.StderrLimit)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return ToolResult{}, sanitizeProcessError(err)
	}

	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	stream := &eofTrackingReader{source: stdout}
	consumed := make(chan error, 1)
	go func() {
		consumeErr := consume(stream)
		if consumeErr == nil && !stream.sawEOF {
			consumeErr = ErrInvalidToolOutput
		}
		consumed <- consumeErr
	}()

	deadlineCtx, cancel := context.WithTimeout(ctx, invocation.Limits.WallTime)
	defer cancel()
	var (
		waitErr     error
		consumeErr  error
		waitDone    bool
		consumeDone bool
		terminalErr error
	)
	stopAndJoin := func() error {
		_ = signalToolProcessGroup(command.Process, syscall.SIGTERM)
		if !waitDone {
			timer := time.NewTimer(runner.config.GracePeriod)
			select {
			case waitErr = <-waited:
				waitDone = true
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				_ = signalToolProcessGroup(command.Process, syscall.SIGKILL)
				waitErr = <-waited
				waitDone = true
			}
		}
		return killAndWaitToolProcessGroup(command.Process, runner.config.GracePeriod)
	}

	for !waitDone || !consumeDone {
		select {
		case waitErr = <-waited:
			waitDone = true
			if err := killAndWaitToolProcessGroup(command.Process, runner.config.GracePeriod); err != nil {
				terminalErr = ErrToolFailed
			}
		case consumeErr = <-consumed:
			consumeDone = true
			if consumeErr != nil {
				terminalErr = consumeErr
			}
		case <-deadlineCtx.Done():
			if ctx.Err() != nil {
				terminalErr = ctx.Err()
			} else {
				terminalErr = ErrToolTimeout
			}
		}
		if terminalErr != nil {
			if err := stopAndJoin(); err != nil && !errors.Is(terminalErr, context.Canceled) &&
				!errors.Is(terminalErr, context.DeadlineExceeded) {
				terminalErr = ErrToolFailed
			}
			if !consumeDone {
				<-consumed
			}
			return ToolResult{}, terminalErr
		}
	}

	if boundedInput.exceeded {
		return ToolResult{}, ErrInputLimit
	}
	exitCode := 0
	if waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) {
			return ToolResult{}, ErrToolFailed
		}
		exitCode = exitError.ExitCode()
		if !allowedExitCode(invocation, exitCode) {
			return ToolResult{}, ErrToolFailed
		}
	}
	outputs, err := readClosedOutputs(output, invocation.OutputSpec, invocation.Limits.MaxOutputBytes)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{
		ExitCode: exitCode, Stderr: stderr.String(), StderrTruncated: stderr.Truncated(), Outputs: outputs,
	}, nil
}

type eofTrackingReader struct {
	source io.Reader
	sawEOF bool
}

func (reader *eofTrackingReader) Read(payload []byte) (int, error) {
	count, err := reader.source.Read(payload)
	if errors.Is(err, io.EOF) {
		reader.sawEOF = true
	}
	return count, err
}

func allowedExitCode(invocation ToolInvocation, code int) bool {
	if len(invocation.SuccessExitCodes) == 0 {
		return code == 0
	}
	for _, allowed := range invocation.SuccessExitCodes {
		if code == allowed {
			return true
		}
	}
	return false
}

func canonicalClamFindingOutput(result ToolResult, inputPath string) (string, error) {
	const (
		suffix                = " FOUND\n"
		maximumSignatureBytes = 128
	)
	prefix := inputPath + ": "
	if inputPath == "" || result.ExitCode != 1 || result.StdoutTruncated || result.StderrTruncated ||
		result.Stderr != "" || len(result.Outputs) != 0 || !strings.HasPrefix(result.Stdout, prefix) ||
		!strings.HasSuffix(result.Stdout, suffix) || strings.Count(result.Stdout, "\n") != 1 {
		return "", ErrInvalidToolOutput
	}
	signature := strings.TrimSuffix(strings.TrimPrefix(result.Stdout, prefix), suffix)
	if len(signature) == 0 || len(signature) > maximumSignatureBytes || strings.TrimSpace(signature) != signature {
		return "", ErrInvalidToolOutput
	}
	for _, character := range []byte(signature) {
		if character < 0x20 || character > 0x7e {
			return "", ErrInvalidToolOutput
		}
	}
	return "input.bin: " + signature + suffix, nil
}

func runtimeEnvironment(
	closed []string,
	workspace, output string,
	inputMode ToolInputMode,
	inputPath string,
	limits ToolLimits,
) []string {
	result := make([]string, 0, len(closed)+7)
	for _, value := range closed {
		if value == "HOME=workspace/home" {
			result = append(result, "HOME="+filepath.Join(workspace, "home"))
			continue
		}
		result = append(result, value)
	}
	result = append(result, "XIRANG_OUTPUT_DIR="+output, "XIRANG_INPUT_MODE="+string(inputMode))
	if inputMode == ToolInputPath {
		result = append(result, "XIRANG_INPUT_PATH="+inputPath)
	}
	result = append(result,
		"XIRANG_RLIMIT_CPU_SECONDS="+strconv.FormatInt(int64(limits.CPUTime/time.Second), 10),
		"XIRANG_RLIMIT_MEMORY_BYTES="+strconv.FormatInt(limits.MaxMemoryBytes, 10),
		"XIRANG_RLIMIT_FSIZE_BYTES="+strconv.FormatInt(limits.MaxFileBytes, 10),
		"XIRANG_RLIMIT_PROCESSES="+strconv.Itoa(limits.MaxProcesses),
	)
	return result
}

func materializeInput(destination string, source io.Reader, maximum int64) error {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, maximum+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return ErrSecureWorkspaceUnavailable
	}
	if written > maximum {
		return ErrInputLimit
	}
	if err := os.Chmod(destination, 0o400); err != nil {
		return ErrSecureWorkspaceUnavailable
	}
	return nil
}

type boundedInputReader struct {
	source   io.Reader
	maximum  int64
	read     int64
	exceeded bool
}

func (reader *boundedInputReader) Read(payload []byte) (int, error) {
	if reader.exceeded {
		return 0, ErrInputLimit
	}
	remaining := reader.maximum - reader.read
	if remaining < 0 {
		reader.exceeded = true
		return 0, ErrInputLimit
	}
	limit := int64(len(payload))
	if limit > remaining+1 {
		limit = remaining + 1
	}
	count, err := reader.source.Read(payload[:limit])
	reader.read += int64(count)
	if reader.read > reader.maximum {
		reader.exceeded = true
		return count, ErrInputLimit
	}
	return count, err
}

func readClosedOutputs(root string, spec ClosedOutputSpec, maximumBytes int64) (map[string][]byte, error) {
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) > spec.MaximumFiles {
		return nil, ErrInvalidToolOutput
	}
	allowed := make(map[string]bool, len(spec.AllowedNames))
	for _, name := range spec.AllowedNames {
		allowed[name] = true
	}
	result := make(map[string][]byte, len(entries))
	var total int64
	for _, entry := range entries {
		if !allowed[entry.Name()] || entry.Type()&os.ModeSymlink != 0 {
			return nil, ErrInvalidToolOutput
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumBytes-total {
			return nil, ErrInvalidToolOutput
		}
		file, err := os.Open(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, ErrInvalidToolOutput
		}
		content, readErr := io.ReadAll(io.LimitReader(file, maximumBytes-total+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(content)) != info.Size() || int64(len(content)) > maximumBytes-total {
			return nil, ErrInvalidToolOutput
		}
		total += int64(len(content))
		result[entry.Name()] = content
	}
	return result, nil
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	maximum   int
	truncated bool
}

func newBoundedBuffer(maximum int) *boundedBuffer { return &boundedBuffer{maximum: maximum} }

func (buffer *boundedBuffer) Write(payload []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(payload)
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.truncated = buffer.truncated || original > 0
		return original, nil
	}
	if len(payload) > remaining {
		payload = payload[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(payload)
	return original, nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (buffer *boundedBuffer) Truncated() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.truncated
}
