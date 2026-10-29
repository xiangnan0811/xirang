package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/sshutil"
)

func hasRcloneCommandConfig(invocation CommandInvocation) bool {
	return invocation.Tool == ToolRclone && len(invocation.SecretStdin) > 0
}

// Test injection keeps config paths entirely inside the transport owner.
func newSSHCommandTransportWithConfig(factory remoteCommandRunnerFactory, configFactory rcloneCommandConfigSessionFactory, source ConcurrencyLimitSource, binaries ToolBinaries) (*SSHCommandTransport, error) {
	transport, err := newSSHCommandTransportWithConcurrencySource(factory, source, binaries)
	if err != nil {
		return nil, err
	}
	if configFactory == nil {
		return nil, backupasset.ErrInvalidState
	}
	transport.configStore = &rcloneCommandConfigStore{factory: configFactory}
	transport.configRunnerFactory = factory
	return transport, nil
}

func (transport *SSHCommandTransport) openConfiguredExecution(ctx context.Context, invocation CommandInvocation, limits OperationLimits, maxBytes int64) (*rcloneConfigExecution, error) {
	// Validate every caller-controlled operand before acquiring or staging anything.
	specification, access, purpose, err := transport.commandSpec(invocation, limits, maxBytes)
	if err != nil {
		return nil, err
	}
	if transport.configStore == nil || transport.configRunnerFactory == nil {
		return nil, configSafeError(backupasset.ErrProviderUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(limits.Timeout)
	if !invocation.AbsoluteDeadline.IsZero() && invocation.AbsoluteDeadline.Before(deadline) {
		deadline = invocation.AbsoluteDeadline
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	if err := transport.acquire(ctx); err != nil {
		cancel()
		return nil, configSafeError(err)
	}
	releaseStaged, err := pinRcloneStagedPayloads(invocation)
	if err != nil {
		transport.release()
		cancel()
		return nil, configSafeError(err)
	}
	owner := &rcloneConfigExecution{ctx: ctx, invocation: invocation, store: transport.configStore, release: func() { releaseStaged(); transport.release(); cancel() }}
	fail := func(err error) (*rcloneConfigExecution, error) {
		owner.setupErr = err
		owner.finish(false, false)
		return nil, owner.err
	}
	owner.ref, err = transport.configStore.prepare(ctx, invocation)
	if err != nil {
		return fail(err)
	}
	specification.Args, err = owner.ref.arguments(invocation, specification.Args)
	if err != nil {
		return fail(err)
	}
	specification.SecretStdin = nil
	// The entry context is the sole wall-clock budget; never restart it after SFTP.
	specification.Timeout = time.Until(deadline)
	if specification.Timeout <= 0 || ctx.Err() != nil {
		return fail(context.DeadlineExceeded)
	}
	runner, closer, err := transport.configRunnerFactory(ctx, access, purpose)
	owner.closer = closer
	if err != nil {
		return fail(err)
	}
	executionRunner, ok := runner.(remoteCommandExecutionRunner)
	if !ok || closer == nil {
		return fail(backupasset.ErrProviderUnavailable)
	}
	execution, err := executionRunner.OpenExecution(ctx, specification)
	if err != nil {
		owner.startUncertain = errors.Is(err, sshutil.ErrCommandStart)
		return fail(err)
	}
	// A nil stream after a successful OpenExecution has no trustworthy start state.
	owner.startUncertain = true
	if execution == nil {
		return fail(backupasset.ErrProviderUnavailable)
	}
	owner.underlying = execution
	if _, ok := execution.(sshutil.CommandPrefixExecutionStream); !ok {
		owner.setupErr = backupasset.ErrProviderUnavailable
		owner.finish(true, false)
		return nil, owner.err
	}
	return owner, nil
}

type rcloneConfigExecution struct {
	underlying     sshutil.CommandExecutionStream
	closer         io.Closer
	ctx            context.Context
	invocation     CommandInvocation
	store          *rcloneCommandConfigStore
	ref            *rcloneCommandConfigRef
	release        func()
	startUncertain bool
	setupErr       error
	once           sync.Once
	completion     sshutil.CommandCompletion
	err            error
}

func (execution *rcloneConfigExecution) Read(buffer []byte) (int, error) {
	n, err := execution.underlying.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		err = configSafeError(err)
	}
	return n, err
}
func (execution *rcloneConfigExecution) Join() (CommandCompletion, error) {
	execution.finish(false, false)
	return CommandCompletion{ExitCode: execution.completion.ExitCode, ExitCodeKnown: execution.completion.ExitCodeKnown, Stderr: append([]byte(nil), execution.completion.Stderr...), StderrTruncated: execution.completion.StderrTruncated}, execution.err
}
func (execution *rcloneConfigExecution) Cancel() error {
	execution.finish(true, false)
	return execution.err
}
func (execution *rcloneConfigExecution) finish(cancel, prefix bool) {
	execution.once.Do(func() {
		defer execution.release()
		execution.err = execution.setupErr
		if execution.underlying != nil {
			var joinErr error
			if cancel {
				cancelErr := execution.underlying.Cancel()
				execution.completion, joinErr = execution.underlying.Join()
				joinErr = errors.Join(cancelErr, joinErr)
			} else if prefix {
				execution.completion, joinErr = execution.underlying.(sshutil.CommandPrefixExecutionStream).JoinPrefix()
			} else {
				execution.completion, joinErr = execution.underlying.Join()
			}
			execution.err = errors.Join(execution.err, joinErr)
		}
		if execution.closer != nil {
			closeErr := execution.closer.Close()
			// The graceful joined runner already closes its dedicated SSH client.
			if !errors.Is(closeErr, net.ErrClosed) && !errors.Is(closeErr, io.EOF) {
				execution.err = errors.Join(execution.err, closeErr)
			}
		}
		if execution.ref != nil {
			if !execution.startUncertain || execution.completion.RemoteExited {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(execution.ctx), sshutil.CommandExecutionJoinTimeout)
				cleanupErr := execution.store.cleanup(cleanupCtx, execution.invocation, execution.ref)
				cancel()
				execution.err = errors.Join(execution.err, cleanupErr)
			} else {
				execution.err = errors.Join(execution.err, backupasset.ErrProviderUnavailable)
			}
		}
		execution.err = configSafeError(errors.Join(execution.err, execution.ctx.Err()))
	})
}

type rcloneConfigReadHandle struct{ execution *rcloneConfigExecution }

func (handle *rcloneConfigReadHandle) Read(buffer []byte) (int, error) {
	return handle.execution.Read(buffer)
}
func (handle *rcloneConfigReadHandle) Close() error {
	completion, err := handle.execution.Join()
	if err == nil && (!completion.ExitCodeKnown || completion.ExitCode != 0) {
		return configSafeError(sshutil.ErrCommandFailed)
	}
	return err
}
func (handle *rcloneConfigReadHandle) ClosePrefix() error {
	handle.execution.finish(false, true)
	return handle.execution.err
}

func (transport *SSHCommandTransport) runConfigured(ctx context.Context, invocation CommandInvocation, limits OperationLimits) (CommandOutput, error) {
	execution, err := transport.openConfiguredExecution(ctx, invocation, limits, limits.MaxMetadataBytes)
	if err != nil {
		return CommandOutput{}, err
	}
	output, readErr := io.ReadAll(execution)
	completion, joinErr := execution.Join()
	err = errors.Join(readErr, joinErr)
	if err == nil && (!completion.ExitCodeKnown || completion.ExitCode != 0) {
		err = sshutil.ErrCommandFailed
	}
	if err != nil {
		return CommandOutput{}, configSafeError(err)
	}
	return CommandOutput{Stdout: output, Stderr: completion.Stderr}, nil
}
