package sshutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	ErrUnsafeCommandSpec  = errors.New("unsafe command specification")
	ErrCommandOutputLimit = errors.New("command output limit exceeded")
	ErrCommandTimeout     = errors.New("command timed out")
	ErrCommandFailed      = errors.New("command failed")
)

const (
	defaultCommandTimeout     = 2 * time.Minute
	defaultCommandStderrLimit = int64(64 << 10)
	// MaximumSecretStdinBytes is the shared hard ceiling for in-memory secret
	// stdin payloads. Settings that bound provider configs must never exceed it.
	MaximumSecretStdinBytes = 64 << 10
	defaultTerminationGrace = 100 * time.Millisecond
)

var safeBinaryPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

type SecretStdin struct {
	Value         []byte `json:"-"`
	AppendNewline bool   `json:"-"`
}

type CommandSpec struct {
	Binary         string
	Args           []string
	Timeout        time.Duration
	MaxStdoutBytes int64
	MaxStderrBytes int64
	MaxRecordBytes int
	SecretStdin    *SecretStdin `json:"-"`
}

type CommandResult struct {
	Stdout []byte
	Stderr []byte
}

// CommandCompletion records the joined remote-command outcome. Stderr is
// intentionally excluded from JSON because callers must keep provider output
// out of normal logs and serialized state.
type CommandCompletion struct {
	ExitCode        int
	ExitCodeKnown   bool
	Stderr          []byte `json:"-"`
	StderrTruncated bool
}

type CommandReadHandle interface {
	io.Reader
	Close() error
}

// CommandExecutionStream exposes the raw stdout stream until natural EOF, then
// joins the full SSH lifecycle to report a trustworthy exit status. It is for
// protocol parsers that must distinguish a valid non-zero exit from an
// indeterminate transport lifecycle.
type CommandExecutionStream interface {
	io.Reader
	Join() (CommandCompletion, error)
	Cancel() error
}

type CommandSession interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	StderrPipe() (io.Reader, error)
	Start(string) error
	Wait() error
	Signal(ssh.Signal) error
	Close() error
}

type CommandSessionFactory func(context.Context) (CommandSession, error)

type CommandRunner struct {
	factory          CommandSessionFactory
	semaphore        chan struct{}
	terminationGrace time.Duration
}

func NewCommandRunner(factory CommandSessionFactory, maxConcurrency int) *CommandRunner {
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	return &CommandRunner{factory: factory, semaphore: make(chan struct{}, maxConcurrency), terminationGrace: defaultTerminationGrace}
}

func NewSSHCommandRunner(client *ssh.Client, maxConcurrency int) *CommandRunner {
	return NewCommandRunner(func(context.Context) (CommandSession, error) {
		if client == nil {
			return nil, fmt.Errorf("SSH client unavailable")
		}
		session, err := client.NewSession()
		if err != nil {
			return nil, fmt.Errorf("create SSH command session")
		}
		return sshCommandSession{Session: session}, nil
	}, maxConcurrency)
}

func (runner *CommandRunner) Run(ctx context.Context, specification CommandSpec) (CommandResult, error) {
	specification, command, err := normalizeCommandSpec(specification)
	if err != nil {
		return CommandResult{}, err
	}
	if runner == nil || runner.factory == nil || runner.semaphore == nil {
		return CommandResult{}, fmt.Errorf("%w: runner unavailable", ErrCommandFailed)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case runner.semaphore <- struct{}{}:
		defer func() { <-runner.semaphore }()
	case <-ctx.Done():
		return CommandResult{}, fmt.Errorf("command canceled: %w", ctx.Err())
	}

	runContext, cancel := context.WithTimeout(ctx, specification.Timeout)
	defer cancel()
	session, err := runner.factory(runContext)
	if err != nil {
		return CommandResult{}, fmt.Errorf("%w: create session", ErrCommandFailed)
	}
	if session == nil {
		return CommandResult{}, fmt.Errorf("%w: nil session", ErrCommandFailed)
	}
	defer session.Close() //nolint:errcheck

	stdout, err := session.StdoutPipe()
	if err != nil {
		return CommandResult{}, fmt.Errorf("%w: stdout pipe", ErrCommandFailed)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return CommandResult{}, fmt.Errorf("%w: stderr pipe", ErrCommandFailed)
	}
	var stdin io.WriteCloser
	if specification.SecretStdin != nil {
		stdin, err = session.StdinPipe()
		if err != nil {
			return CommandResult{}, fmt.Errorf("%w: secret stdin unavailable", ErrCommandFailed)
		}
	}
	if err := session.Start(command); err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		return CommandResult{}, fmt.Errorf("%w: start", ErrCommandFailed)
	}

	type readResult struct {
		value []byte
		err   error
	}
	stdoutResult := make(chan readResult, 1)
	stderrResult := make(chan readResult, 1)
	go func() {
		value, readErr := readCommandOutput(stdout, specification.MaxStdoutBytes, specification.MaxRecordBytes)
		stdoutResult <- readResult{value: value, err: readErr}
	}()
	go func() {
		value, readErr := readCommandOutput(stderr, specification.MaxStderrBytes, specification.MaxRecordBytes)
		stderrResult <- readResult{value: value, err: readErr}
	}()
	stdinResult := make(chan error, 1)
	if stdin != nil {
		go func() { stdinResult <- writeSecretStdin(stdin, specification.SecretStdin) }()
	} else {
		stdinResult <- nil
	}

	waitResult := make(chan error, 1)
	go func() { waitResult <- session.Wait() }()
	terminated := make(chan struct{})
	go func() {
		select {
		case <-runContext.Done():
			_ = session.Signal(ssh.SIGTERM)
			timer := time.NewTimer(runner.terminationGrace)
			select {
			case <-terminated:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				_ = session.Close()
			}
		case <-terminated:
		}
	}()

	var (
		stdoutValue readResult
		stderrValue readResult
		stdoutDone  bool
		stderrDone  bool
		stdinDone   bool
		waitDone    bool
		stdinErr    error
		waitErr     error
	)
	for !stdoutDone || !stderrDone || !stdinDone || !waitDone {
		select {
		case result := <-stdoutResult:
			if !stdoutDone {
				stdoutValue = result
				stdoutDone = true
				if result.err != nil {
					cancel()
				}
			}
		case result := <-stderrResult:
			if !stderrDone {
				stderrValue = result
				stderrDone = true
				if result.err != nil {
					cancel()
				}
			}
		case result := <-stdinResult:
			if !stdinDone {
				stdinErr = result
				stdinDone = true
				if result != nil {
					cancel()
				}
			}
		case result := <-waitResult:
			if !waitDone {
				waitErr = result
				waitDone = true
			}
		}
	}
	close(terminated)
	if ctx.Err() != nil {
		return CommandResult{}, fmt.Errorf("command canceled: %w", ctx.Err())
	}
	if runContext.Err() == context.DeadlineExceeded {
		return CommandResult{}, fmt.Errorf("%w", ErrCommandTimeout)
	}
	if errors.Is(stdoutValue.err, ErrCommandOutputLimit) || errors.Is(stderrValue.err, ErrCommandOutputLimit) {
		return CommandResult{}, fmt.Errorf("%w", ErrCommandOutputLimit)
	}
	if stdoutValue.err != nil || stderrValue.err != nil || stdinErr != nil || waitErr != nil {
		return CommandResult{}, fmt.Errorf("%w", ErrCommandFailed)
	}
	return CommandResult{Stdout: stdoutValue.value, Stderr: stderrValue.value}, nil
}

func (runner *CommandRunner) Open(ctx context.Context, specification CommandSpec) (CommandReadHandle, error) {
	specification, command, err := normalizeCommandSpec(specification)
	if err != nil {
		return nil, err
	}
	if runner == nil || runner.factory == nil || runner.semaphore == nil {
		return nil, fmt.Errorf("%w: runner unavailable", ErrCommandFailed)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case runner.semaphore <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("command canceled: %w", ctx.Err())
	}
	release := func() { <-runner.semaphore }
	runContext, cancel := context.WithTimeout(ctx, specification.Timeout)
	session, err := runner.factory(runContext)
	if err != nil {
		cancel()
		release()
		return nil, fmt.Errorf("%w: create session", ErrCommandFailed)
	}
	if session == nil {
		cancel()
		release()
		return nil, fmt.Errorf("%w: nil session", ErrCommandFailed)
	}
	fail := func(err error) (CommandReadHandle, error) {
		cancel()
		_ = session.Close()
		release()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fail(fmt.Errorf("%w: stdout pipe", ErrCommandFailed))
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return fail(fmt.Errorf("%w: stderr pipe", ErrCommandFailed))
	}
	var stdin io.WriteCloser
	if specification.SecretStdin != nil {
		stdin, err = session.StdinPipe()
		if err != nil {
			return fail(fmt.Errorf("%w: secret stdin unavailable", ErrCommandFailed))
		}
	}
	if err := session.Start(command); err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		return fail(fmt.Errorf("%w: start", ErrCommandFailed))
	}

	handle := &commandStream{
		session: session, stdout: stdout, parentContext: ctx, runContext: runContext, cancel: cancel,
		remaining: specification.MaxStdoutBytes, release: release, terminationGrace: runner.terminationGrace,
		stderrDone: make(chan struct{}), stdinDone: make(chan struct{}), waitDone: make(chan struct{}), lifecycleDone: make(chan struct{}),
	}
	go func() {
		_, readErr := readCommandOutput(stderr, specification.MaxStderrBytes, specification.MaxRecordBytes)
		handle.setBackgroundError(readErr)
		close(handle.stderrDone)
	}()
	go func() {
		var writeErr error
		if stdin != nil {
			writeErr = writeSecretStdin(stdin, specification.SecretStdin)
		}
		handle.setBackgroundError(writeErr)
		close(handle.stdinDone)
	}()
	go func() {
		handle.setWaitError(session.Wait())
		close(handle.waitDone)
	}()
	go func() {
		select {
		case <-runContext.Done():
			_ = session.Signal(ssh.SIGTERM)
			timer := time.NewTimer(runner.terminationGrace)
			select {
			case <-handle.lifecycleDone:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				_ = session.Close()
			}
		case <-handle.lifecycleDone:
		}
	}()
	return handle, nil
}

// OpenExecution opens a command stream whose exit status is available only
// after stdout reaches natural EOF and the complete SSH lifecycle is joined.
// Existing Run and Open callers retain their established contracts.
func (runner *CommandRunner) OpenExecution(ctx context.Context, specification CommandSpec) (CommandExecutionStream, error) {
	specification, command, err := normalizeCommandSpec(specification)
	if err != nil {
		return nil, err
	}
	if runner == nil || runner.factory == nil || runner.semaphore == nil {
		return nil, fmt.Errorf("%w: runner unavailable", ErrCommandFailed)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case runner.semaphore <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("command canceled: %w", ctx.Err())
	}
	release := func() { <-runner.semaphore }
	runContext, cancel := context.WithTimeout(ctx, specification.Timeout)
	session, err := runner.factory(runContext)
	if err != nil {
		cancel()
		release()
		return nil, fmt.Errorf("%w: create session", ErrCommandFailed)
	}
	if session == nil {
		cancel()
		release()
		return nil, fmt.Errorf("%w: nil session", ErrCommandFailed)
	}
	fail := func(cause error) (CommandExecutionStream, error) {
		cancel()
		_ = session.Close()
		release()
		return nil, cause
	}
	stdout, err := session.StdoutPipe()
	if err != nil || stdout == nil {
		return fail(fmt.Errorf("%w: stdout pipe", ErrCommandFailed))
	}
	stderr, err := session.StderrPipe()
	if err != nil || stderr == nil {
		return fail(fmt.Errorf("%w: stderr pipe", ErrCommandFailed))
	}
	var stdin io.WriteCloser
	if specification.SecretStdin != nil {
		stdin, err = session.StdinPipe()
		if err != nil || stdin == nil {
			return fail(fmt.Errorf("%w: secret stdin unavailable", ErrCommandFailed))
		}
	}
	if err := session.Start(command); err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		return fail(fmt.Errorf("%w: start", ErrCommandFailed))
	}

	stream := &commandExecution{
		session:          session,
		stdout:           stdout,
		stderr:           stderr,
		stdin:            stdin,
		secretStdin:      specification.SecretStdin,
		parentContext:    ctx,
		runContext:       runContext,
		cancel:           cancel,
		remaining:        specification.MaxStdoutBytes,
		maxRecordBytes:   specification.MaxRecordBytes,
		maxStderrBytes:   specification.MaxStderrBytes,
		release:          release,
		terminationGrace: runner.terminationGrace,
		stdinDone:        make(chan struct{}),
		stderrDone:       make(chan struct{}),
		waitDone:         make(chan struct{}),
		allDone:          make(chan struct{}),
		finished:         make(chan struct{}),
	}
	if closer, ok := stdout.(io.Closer); ok {
		stream.stdoutCloser = closer
	}
	if closer, ok := stderr.(io.Closer); ok {
		stream.stderrCloser = closer
	}
	stream.start()
	return stream, nil
}

type commandExecution struct {
	session       CommandSession
	stdout        io.Reader
	stderr        io.Reader
	stdin         io.WriteCloser
	secretStdin   *SecretStdin
	stdoutCloser  io.Closer
	stderrCloser  io.Closer
	parentContext context.Context
	runContext    context.Context
	cancel        context.CancelFunc
	release       func()

	remaining        int64
	maxRecordBytes   int
	maxStderrBytes   int64
	terminationGrace time.Duration

	stdinDone  chan struct{}
	stderrDone chan struct{}
	waitDone   chan struct{}
	allDone    chan struct{}
	finished   chan struct{}

	terminateOnce       sync.Once
	connectionCloseOnce sync.Once
	finalizeOnce        sync.Once
	releaseOnce         sync.Once

	mu sync.Mutex

	stdoutLineBytes int
	stdoutEOF       bool
	abandoned       bool
	remainingLimit  error
	stdoutReadErr   error
	stderrReadErr   error
	stdinWriteErr   error
	stdinCloseErr   error
	stdoutCloseErr  error
	stderrCloseErr  error
	connectionErr   error
	signalErr       error
	cleanupErr      error
	waitErr         error
	stderrBytes     []byte
	stderrTruncated bool

	completion CommandCompletion
	resultErr  error
}

func (stream *commandExecution) start() {
	go func() {
		stream.drainStderr()
		close(stream.stderrDone)
	}()
	go func() {
		stream.writeStdin()
		close(stream.stdinDone)
	}()
	go func() {
		stream.setWaitError(stream.session.Wait())
		close(stream.waitDone)
	}()
	go func() {
		<-stream.stdinDone
		<-stream.stderrDone
		<-stream.waitDone
		close(stream.allDone)
	}()
	go func() {
		select {
		case <-stream.runContext.Done():
			stream.terminate()
		case <-stream.finished:
		}
	}()
}

func (stream *commandExecution) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	stream.mu.Lock()
	if stream.remainingLimit != nil {
		err := stream.remainingLimit
		stream.mu.Unlock()
		return 0, err
	}
	if stream.stdoutReadErr != nil {
		stream.mu.Unlock()
		return 0, ErrCommandFailed
	}
	remaining := stream.remaining
	stream.mu.Unlock()
	if stream.parentContext.Err() != nil {
		stream.requestTermination()
		return 0, fmt.Errorf("command canceled: %w", stream.parentContext.Err())
	}
	if remaining == 0 {
		var probe [1]byte
		count, err := stream.stdout.Read(probe[:])
		if count > 0 {
			stream.setOutputLimit()
			return 0, ErrCommandOutputLimit
		}
		if errors.Is(err, io.EOF) {
			stream.setStdoutEOF()
			return 0, io.EOF
		}
		if err != nil {
			stream.setStdoutReadError(err)
			return 0, ErrCommandFailed
		}
		return 0, nil
	}
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	count, err := stream.stdout.Read(buffer)
	if count > 0 {
		if stream.advanceStdout(count, buffer[:count]) {
			stream.setOutputLimit()
			return count, ErrCommandOutputLimit
		}
	}
	if errors.Is(err, io.EOF) {
		stream.setStdoutEOF()
		return count, io.EOF
	}
	if err != nil {
		stream.setStdoutReadError(err)
		return count, ErrCommandFailed
	}
	return count, nil
}

func (stream *commandExecution) Join() (CommandCompletion, error) {
	stream.finalize(false)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return copyCommandCompletion(stream.completion), stream.resultErr
}

func (stream *commandExecution) Cancel() error {
	stream.finalize(true)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.resultErr
}

func (stream *commandExecution) finalize(cancelRequested bool) {
	stream.finalizeOnce.Do(func() {
		stream.mu.Lock()
		naturalEOF := stream.stdoutEOF
		if cancelRequested || !naturalEOF {
			stream.abandoned = true
		}
		abandoned := stream.abandoned
		stream.mu.Unlock()

		if abandoned {
			stream.terminate()
			stream.waitAfterTermination()
		} else {
			stream.waitForNaturalCompletion()
		}
		stream.closeStreams()
		stream.closeConnection()
		close(stream.finished)
		stream.cancel()
		stream.releaseOnce.Do(stream.release)
		stream.mu.Lock()
		stream.completion, stream.resultErr = stream.classifyLocked()
		stream.mu.Unlock()
	})
}

func (stream *commandExecution) waitForNaturalCompletion() {
	select {
	case <-stream.allDone:
		return
	case <-stream.runContext.Done():
		stream.terminate()
		stream.waitAfterTermination()
	}
}

func (stream *commandExecution) waitAfterTermination() {
	timer := time.NewTimer(CommandExecutionJoinTimeout)
	defer timer.Stop()
	select {
	case <-stream.allDone:
	case <-timer.C:
		stream.mu.Lock()
		if stream.cleanupErr == nil {
			stream.cleanupErr = ErrCommandFailed
		}
		stream.mu.Unlock()
	}
}

func (stream *commandExecution) terminate() {
	stream.terminateOnce.Do(func() {
		stream.cancel()
		if err := stream.session.Signal(ssh.SIGTERM); err != nil {
			stream.mu.Lock()
			stream.signalErr = err
			stream.mu.Unlock()
		}
		grace := stream.terminationGrace
		if grace <= 0 {
			grace = defaultTerminationGrace
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-stream.allDone:
		case <-timer.C:
			stream.closeConnection()
		}
	})
}

func (stream *commandExecution) requestTermination() {
	go stream.terminate()
}

func (stream *commandExecution) drainStderr() {
	buffer := make([]byte, 32<<10)
	for {
		count, err := stream.stderr.Read(buffer)
		if count > 0 {
			stream.appendStderr(buffer[:count])
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			stream.mu.Lock()
			if stream.stderrReadErr == nil {
				stream.stderrReadErr = err
			}
			stream.mu.Unlock()
			stream.requestTermination()
			return
		}
		if count == 0 {
			stream.mu.Lock()
			if stream.stderrReadErr == nil {
				stream.stderrReadErr = io.ErrNoProgress
			}
			stream.mu.Unlock()
			stream.requestTermination()
			return
		}
	}
}

func (stream *commandExecution) appendStderr(value []byte) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	remaining := stream.maxStderrBytes - int64(len(stream.stderrBytes))
	if remaining <= 0 {
		stream.stderrTruncated = true
		return
	}
	if int64(len(value)) > remaining {
		stream.stderrBytes = append(stream.stderrBytes, value[:remaining]...)
		stream.stderrTruncated = true
		return
	}
	stream.stderrBytes = append(stream.stderrBytes, value...)
}

func (stream *commandExecution) writeStdin() {
	if stream.stdin == nil {
		return
	}
	writeErr, closeErr := writeExecutionSecretStdin(stream.stdin, stream.secretStdin)
	stream.mu.Lock()
	stream.stdinWriteErr = writeErr
	stream.stdinCloseErr = closeErr
	stream.mu.Unlock()
	if writeErr != nil || closeErr != nil {
		stream.requestTermination()
	}
}

func (stream *commandExecution) closeStreams() {
	if stream.stdoutCloser != nil {
		if err := stream.stdoutCloser.Close(); err != nil {
			stream.mu.Lock()
			stream.stdoutCloseErr = err
			stream.mu.Unlock()
		}
	}
	if stream.stderrCloser != nil {
		if err := stream.stderrCloser.Close(); err != nil {
			stream.mu.Lock()
			stream.stderrCloseErr = err
			stream.mu.Unlock()
		}
	}
}

func (stream *commandExecution) closeConnection() {
	stream.connectionCloseOnce.Do(func() {
		if err := stream.session.Close(); err != nil {
			stream.mu.Lock()
			stream.connectionErr = err
			stream.mu.Unlock()
		}
	})
}

func (stream *commandExecution) setWaitError(err error) {
	stream.mu.Lock()
	stream.waitErr = err
	stream.mu.Unlock()
}

func (stream *commandExecution) setOutputLimit() {
	stream.mu.Lock()
	if stream.remainingLimit == nil {
		stream.remainingLimit = ErrCommandOutputLimit
	}
	stream.mu.Unlock()
	stream.requestTermination()
}

func (stream *commandExecution) setStdoutReadError(err error) {
	stream.mu.Lock()
	if stream.stdoutReadErr == nil {
		stream.stdoutReadErr = err
	}
	stream.mu.Unlock()
	stream.requestTermination()
}

func (stream *commandExecution) setStdoutEOF() {
	stream.mu.Lock()
	stream.stdoutEOF = true
	stream.mu.Unlock()
}

func (stream *commandExecution) advanceStdout(count int, value []byte) bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	stream.remaining -= int64(count)
	if stream.maxRecordBytes <= 0 {
		return false
	}
	for _, current := range value {
		if current == '\n' {
			stream.stdoutLineBytes = 0
			continue
		}
		stream.stdoutLineBytes++
		if stream.stdoutLineBytes > stream.maxRecordBytes {
			return true
		}
	}
	return false
}

func (stream *commandExecution) classifyLocked() (CommandCompletion, error) {
	switch {
	case stream.parentContext.Err() != nil:
		return CommandCompletion{}, fmt.Errorf("command canceled: %w", stream.parentContext.Err())
	case stream.runContext.Err() == context.DeadlineExceeded:
		return CommandCompletion{}, ErrCommandTimeout
	case stream.remainingLimit != nil:
		return CommandCompletion{}, ErrCommandOutputLimit
	case stream.abandoned || stream.cleanupErr != nil || stream.stdoutReadErr != nil || stream.stderrReadErr != nil ||
		stream.stdinWriteErr != nil || stream.stdinCloseErr != nil || stream.stdoutCloseErr != nil ||
		stream.stderrCloseErr != nil || stream.connectionErr != nil || stream.signalErr != nil:
		return CommandCompletion{}, ErrCommandFailed
	}
	completion := CommandCompletion{Stderr: append([]byte(nil), stream.stderrBytes...), StderrTruncated: stream.stderrTruncated}
	if stream.waitErr == nil {
		completion.ExitCodeKnown = true
		return completion, nil
	}
	var exitStatusError interface{ ExitStatus() int }
	if errors.As(stream.waitErr, &exitStatusError) {
		completion.ExitCode = exitStatusError.ExitStatus()
		completion.ExitCodeKnown = true
		return completion, nil
	}
	return CommandCompletion{}, ErrCommandFailed
}

func copyCommandCompletion(value CommandCompletion) CommandCompletion {
	value.Stderr = append([]byte(nil), value.Stderr...)
	return value
}

func writeExecutionSecretStdin(writer io.WriteCloser, secret *SecretStdin) (error, error) {
	value := append([]byte(nil), secret.Value...)
	if secret.AppendNewline {
		value = append(value, '\n')
	}
	written, writeErr := writer.Write(value)
	if writeErr == nil && written != len(value) {
		writeErr = io.ErrShortWrite
	}
	closeErr := writer.Close()
	for index := range value {
		value[index] = 0
	}
	return writeErr, closeErr
}

type commandStream struct {
	session          CommandSession
	stdout           io.Reader
	parentContext    context.Context
	runContext       context.Context
	cancel           context.CancelFunc
	remaining        int64
	release          func()
	terminationGrace time.Duration

	stderrDone    chan struct{}
	stdinDone     chan struct{}
	waitDone      chan struct{}
	lifecycleDone chan struct{}

	mu            sync.Mutex
	backgroundErr error
	waitErr       error
	limitErr      error
	reachedEOF    bool
	closeErr      error
	closeOnce     sync.Once
}

func (stream *commandStream) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	stream.mu.Lock()
	if stream.limitErr != nil {
		err := stream.limitErr
		stream.mu.Unlock()
		return 0, err
	}
	if stream.backgroundErr != nil {
		err := stream.backgroundErr
		stream.mu.Unlock()
		return 0, err
	}
	remaining := stream.remaining
	stream.mu.Unlock()
	if stream.parentContext.Err() != nil {
		stream.cancel()
		return 0, fmt.Errorf("command canceled: %w", stream.parentContext.Err())
	}
	if remaining == 0 {
		var probe [1]byte
		count, err := stream.stdout.Read(probe[:])
		if count > 0 {
			stream.mu.Lock()
			stream.limitErr = ErrCommandOutputLimit
			stream.mu.Unlock()
			stream.cancel()
			return 0, ErrCommandOutputLimit
		}
		if errors.Is(err, io.EOF) {
			stream.mu.Lock()
			stream.reachedEOF = true
			stream.mu.Unlock()
			return 0, io.EOF
		}
		if err != nil {
			stream.setBackgroundError(ErrCommandFailed)
			return 0, ErrCommandFailed
		}
		return 0, nil
	}
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	count, err := stream.stdout.Read(buffer)
	stream.mu.Lock()
	stream.remaining -= int64(count)
	if errors.Is(err, io.EOF) {
		stream.reachedEOF = true
	}
	stream.mu.Unlock()
	if err != nil && !errors.Is(err, io.EOF) {
		stream.setBackgroundError(ErrCommandFailed)
		return count, ErrCommandFailed
	}
	return count, err
}

func (stream *commandStream) Close() error {
	stream.closeOnce.Do(func() {
		stream.mu.Lock()
		joinNaturally := stream.reachedEOF && stream.limitErr == nil && stream.backgroundErr == nil
		stream.mu.Unlock()
		joined := false
		if joinNaturally {
			grace := stream.terminationGrace
			if grace <= 0 {
				grace = defaultTerminationGrace
			}
			timer := time.NewTimer(grace)
			select {
			case <-stream.waitDone:
				joined = true
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
			}
		}
		if !joined {
			stream.cancel()
			_ = stream.session.Close()
		}
		<-stream.stderrDone
		<-stream.stdinDone
		<-stream.waitDone
		stream.cancel()
		_ = stream.session.Close()
		close(stream.lifecycleDone)
		stream.mu.Lock()
		switch {
		case stream.limitErr != nil:
			stream.closeErr = stream.limitErr
		case stream.parentContext.Err() != nil:
			stream.closeErr = fmt.Errorf("command canceled: %w", stream.parentContext.Err())
		case stream.runContext.Err() == context.DeadlineExceeded:
			stream.closeErr = ErrCommandTimeout
		case stream.backgroundErr != nil:
			stream.closeErr = stream.backgroundErr
		case stream.waitErr != nil:
			stream.closeErr = ErrCommandFailed
		}
		stream.mu.Unlock()
		stream.release()
	})
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.closeErr
}

func (stream *commandStream) setBackgroundError(err error) {
	if err == nil {
		return
	}
	stream.mu.Lock()
	if stream.backgroundErr == nil {
		if errors.Is(err, ErrCommandOutputLimit) {
			stream.backgroundErr = ErrCommandOutputLimit
		} else {
			stream.backgroundErr = ErrCommandFailed
		}
	}
	stream.mu.Unlock()
	stream.cancel()
}

func (stream *commandStream) setWaitError(err error) {
	stream.mu.Lock()
	stream.waitErr = err
	stream.mu.Unlock()
}

func normalizeCommandSpec(specification CommandSpec) (CommandSpec, string, error) {
	binary := strings.TrimSpace(specification.Binary)
	base := filepath.Base(binary)
	if binary == "" || binary != specification.Binary || !safeBinaryPattern.MatchString(binary) || filepath.Clean(binary) != binary || base == "." || base == ".." {
		return CommandSpec{}, "", fmt.Errorf("%w: invalid binary", ErrUnsafeCommandSpec)
	}
	switch strings.ToLower(base) {
	case "sh", "bash", "zsh", "dash", "ksh", "cmd", "powershell", "pwsh":
		return CommandSpec{}, "", fmt.Errorf("%w: shell binary denied", ErrUnsafeCommandSpec)
	}
	operands := make([]string, 0, len(specification.Args)+1)
	operands = append(operands, binary)
	for _, argument := range specification.Args {
		if strings.ContainsRune(argument, '\x00') || len(argument) > 16<<10 {
			return CommandSpec{}, "", fmt.Errorf("%w: invalid operand", ErrUnsafeCommandSpec)
		}
		operands = append(operands, argument)
	}
	if specification.MaxStdoutBytes <= 0 {
		return CommandSpec{}, "", fmt.Errorf("%w: stdout limit required", ErrUnsafeCommandSpec)
	}
	if specification.MaxStderrBytes <= 0 {
		specification.MaxStderrBytes = defaultCommandStderrLimit
	}
	if specification.Timeout <= 0 {
		specification.Timeout = defaultCommandTimeout
	}
	if specification.SecretStdin != nil && (len(specification.SecretStdin.Value) == 0 || len(specification.SecretStdin.Value) > MaximumSecretStdinBytes) {
		return CommandSpec{}, "", fmt.Errorf("%w: invalid secret stdin", ErrUnsafeCommandSpec)
	}
	quoted := make([]string, len(operands))
	for index, operand := range operands {
		quoted[index] = shellQuoteOperand(operand)
	}
	return specification, strings.Join(quoted, " "), nil
}

func shellQuoteOperand(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func readCommandOutput(reader io.Reader, maximum int64, maxRecord int) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read command output")
	}
	if int64(len(value)) > maximum {
		return nil, ErrCommandOutputLimit
	}
	if maxRecord > 0 {
		for _, record := range bytes.Split(value, []byte{'\n'}) {
			if len(record) > maxRecord {
				return nil, ErrCommandOutputLimit
			}
		}
	}
	return value, nil
}

func writeSecretStdin(writer io.WriteCloser, secret *SecretStdin) error {
	value := append([]byte(nil), secret.Value...)
	if secret.AppendNewline {
		value = append(value, '\n')
	}
	written, err := writer.Write(value)
	closeErr := writer.Close()
	for index := range value {
		value[index] = 0
	}
	if err != nil || closeErr != nil || written != len(value) {
		return fmt.Errorf("write secret stdin")
	}
	return nil
}

type sshCommandSession struct{ *ssh.Session }

var _ CommandSession = sshCommandSession{}
