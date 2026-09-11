package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
)

type providerRunResult struct {
	ExitCode int
	Err      error
	// ExecutorNotInvoked is authoritative only for exits before the selected
	// executor's Run/RunWithPublication method is called.
	ExecutorNotInvoked bool
	SuppressRetry      bool
	Managed            bool
	WarningCode        backupasset.PublicationFailureCode
}

type publicationFinalization struct {
	result    providerRunResult
	finalized bool
}

func shouldRunLegacyVerification(result providerRunResult, policy *model.Policy) bool {
	return !result.Managed && policy != nil && policy.VerifyEnabled
}
func newPublicationCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), sshutil.CommandExecutionJoinTimeout)
}

// executeProvider keeps TaskRun transfer truth separate from asynchronous
// recovery-point publication. A successful evidence transfer returns as soon
// as its exact commit fact is durable; manifest work remains with the worker.
func (m *Manager) executeProvider(ctx context.Context, taskEntity model.Task, runID uint, reason string, chainRunID string, logf executor.LogFunc, progressf executor.ProgressFunc) (providerResult providerRunResult) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil || m.executorFactory == nil {
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: task executor factory unavailable", backupasset.ErrInvalidState), ExecutorNotInvoked: true}
	}
	exec := m.executorFactory.Resolve(taskEntity.ExecutorType)
	if exec == nil {
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: task executor unavailable", backupasset.ErrInvalidState), ExecutorNotInvoked: true}
	}
	providerKind := strings.ToLower(strings.TrimSpace(taskEntity.ExecutorType))
	if m.publicationCoordinator == nil || (providerKind != "restic" && providerKind != "rsync" && providerKind != "rclone") {
		exitCode, err := exec.Run(ctx, taskEntity, logf, progressf)
		return providerRunResult{ExitCode: exitCode, Err: err}
	}

	audit, err := taskPublicationAuditContext(runID)
	if err != nil {
		return providerRunResult{ExitCode: -1, Err: err, Managed: true, ExecutorNotInvoked: true}
	}
	session, err := m.publicationCoordinator.Prepare(ctx, publication.Run{
		Task: taskEntity, TaskRunID: runID, Trigger: reason, ChainRunID: chainRunID, Audit: audit,
	})
	if err != nil {
		return providerRunResult{ExitCode: -1, Err: err, Managed: true, ExecutorNotInvoked: true}
	}
	if session == nil {
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: nil publication session", backupasset.ErrInvalidState), Managed: true, ExecutorNotInvoked: true}
	}

	// Finalization is complete only after the persistence handoff succeeds and
	// the execution implementation has released its admission token. A provider
	// return, or an attempted fallback, is not enough to mark this fact.
	publicationFinalized := false
	defer func() {
		if publicationFinalized {
			return
		}
		if abandonErr := session.Abandon(backupasset.ErrPublicationSessionAbandoned); abandonErr != nil {
			providerResult.WarningCode = backupasset.FailurePublicationSessionAbandoned
			if providerResult.Err == nil {
				providerResult.Err = abandonErr
			} else {
				providerResult.Err = errors.Join(providerResult.Err, abandonErr)
			}
		}
	}()

	rejectPrecondition := func(result providerRunResult) providerRunResult {
		result.ExecutorNotInvoked = true
		cleanupCtx, cleanupCancel := newPublicationCleanupContext(ctx)
		finalization := rejectPublicationPrecondition(cleanupCtx, session, result)
		cleanupCancel()
		publicationFinalized = finalization.finalized
		return finalization.result
	}

	commandCtx := session.Context()
	if commandCtx == nil {
		return rejectPrecondition(providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: publication execution context unavailable", backupasset.ErrInvalidState), Managed: true})
	}
	if session.Mode() == publication.ModeCompatibility {
		exitCode, runErr := exec.Run(commandCtx, taskEntity, logf, progressf)
		cleanupCtx, cleanupCancel := newPublicationCleanupContext(ctx)
		completeErr := session.CompleteCompatibility(cleanupCtx)
		cleanupCancel()
		publicationFinalized = completeErr == nil
		if runErr != nil {
			if completeErr != nil {
				runErr = errors.Join(runErr, completeErr)
			}
			return providerRunResult{ExitCode: exitCode, Err: runErr}
		}
		return providerRunResult{ExitCode: exitCode, Err: completeErr}
	}
	if session.Mode() != publication.ModeEvidence || session.Attempt() == nil {
		return rejectPrecondition(providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: invalid publication evidence session", backupasset.ErrInvalidState), Managed: true})
	}
	publicationExecutor, ok := exec.(executor.PublicationExecutor)
	if !ok {
		return rejectPrecondition(providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: provider executor has no publication lane", backupasset.ErrInvalidState), Managed: true})
	}
	attempt := session.Attempt()
	if attempt == nil {
		return rejectPrecondition(providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: missing tagged publication attempt", backupasset.ErrInvalidState), Managed: true})
	}
	request := executor.PublicationExecutionRequest{Task: taskEntity, TaskRunID: runID, Attempt: *attempt}
	recoveryPointID := ""
	switch providerKind {
	case "restic":
		resticAttempt, attemptErr := attempt.ResticAttempt()
		if attemptErr != nil {
			return rejectPrecondition(providerRunResult{ExitCode: -1, Err: attemptErr, Managed: true})
		}
		recoveryPointID = resticAttempt.RecoveryPointID
	case "rsync":
		rsyncAttempt, attemptErr := attempt.RsyncTreeAttempt()
		if attemptErr != nil {
			return rejectPrecondition(providerRunResult{ExitCode: -1, Err: attemptErr, Managed: true})
		}
		inputProvider, ok := session.(interface {
			RsyncTreePublicationInput() (provider.RsyncTreePublicationInput, error)
		})
		if !ok {
			return rejectPrecondition(providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: managed Rsync publication input is unavailable", backupasset.ErrInvalidState), Managed: true})
		}
		input, inputErr := inputProvider.RsyncTreePublicationInput()
		if inputErr != nil {
			return rejectPrecondition(providerRunResult{ExitCode: -1, Err: inputErr, Managed: true})
		}
		request.RsyncTreeInput = &input
		recoveryPointID = rsyncAttempt.RecoveryPointID
	case "rclone":
		rcloneAttempt, attemptErr := attempt.RcloneAttempt()
		if attemptErr != nil {
			return rejectPrecondition(providerRunResult{ExitCode: -1, Err: attemptErr, Managed: true})
		}
		inputProvider, ok := session.(interface {
			RclonePublicationInput() (provider.RclonePublicationInput, error)
		})
		if !ok {
			return rejectPrecondition(providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: managed Rclone publication input is unavailable", backupasset.ErrInvalidState), Managed: true})
		}
		input, inputErr := inputProvider.RclonePublicationInput()
		if inputErr != nil {
			return rejectPrecondition(providerRunResult{ExitCode: -1, Err: inputErr, Managed: true})
		}
		request.RcloneInput = &input
		recoveryPointID = rcloneAttempt.RecoveryPointID
	default:
		return rejectPrecondition(providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: unsupported managed publication provider", backupasset.ErrInvalidState), Managed: true})
	}
	result, runErr := publicationExecutor.RunWithPublication(commandCtx, request, logf, progressf)

	// Do not spend the bounded cleanup budget while the Provider is running.
	// WithoutCancel preserves the audit/dependency values while allowing the
	// post-provider transaction to receive its own complete join window.
	cleanupCtx, cleanupCancel := newPublicationCleanupContext(ctx)
	finalization := m.finishPublicationExecutionState(cleanupCtx, session, result, runErr)
	cleanupCancel()
	publicationFinalized = finalization.finalized
	providerResult = finalization.result
	if providerResult.WarningCode != "" && logf != nil {
		logf("warn", fmt.Sprintf("恢复点发布未提交: point_id=%s code=%s", recoveryPointID, providerResult.WarningCode))
	}
	return providerResult
}

func rejectPublicationPrecondition(ctx context.Context, session publication.Execution, result providerRunResult) publicationFinalization {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing); err != nil {
		if result.Err == nil {
			result.Err = err
		} else {
			result.Err = errors.Join(result.Err, err)
		}
		return publicationFinalization{result: result}
	}
	return publicationFinalization{result: result, finalized: true}
}

func (m *Manager) finishPublicationExecutionState(ctx context.Context, session publication.Execution, result executor.PublicationExecutionResult, runErr error) publicationFinalization {
	if session == nil {
		return publicationFinalization{result: providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: publication execution unavailable", backupasset.ErrInvalidState), Managed: true}}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	managed := true
	switch result.Completion {
	case backupasset.CompletionKnownExitZero:
		if result.ExitCode != 0 {
			return rejectPublicationPrecondition(ctx, session, providerRunResult{ExitCode: result.ExitCode, Err: fmt.Errorf("%w: known exit-zero result has nonzero exit", backupasset.ErrInvalidState), Managed: managed})
		}
		if runErr != nil {
			return rejectPublicationPrecondition(ctx, session, providerRunResult{ExitCode: result.ExitCode, Err: runErr, Managed: managed})
		}
		if result.ProviderCommit != nil && result.EvidenceCode == "" {
			if err := recordPublicationCommit(ctx, session, *result.ProviderCommit); err != nil {
				return publicationFinalization{result: providerRunResult{ExitCode: 0, Managed: managed, WarningCode: backupasset.FailurePublicationSessionAbandoned}}
			}
			return publicationFinalization{result: providerRunResult{ExitCode: 0, Managed: managed}, finalized: true}
		}
		if result.ProviderCommit != nil || result.EvidenceCode == "" {
			return rejectPublicationPrecondition(ctx, session, providerRunResult{ExitCode: 0, Err: fmt.Errorf("%w: inconsistent known exit-zero evidence", backupasset.ErrInvalidState), Managed: managed})
		}
		if err := session.Defer(ctx, publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: result.EvidenceCode}); err != nil {
			return publicationFinalization{result: providerRunResult{ExitCode: 0, Err: err, Managed: managed, WarningCode: backupasset.FailurePublicationSessionAbandoned}}
		}
		return publicationFinalization{result: providerRunResult{ExitCode: 0, Managed: managed, WarningCode: result.EvidenceCode}, finalized: true}
	case backupasset.CompletionKnownNonzero:
		if result.ExitCode <= 0 || runErr == nil {
			return rejectPublicationPrecondition(ctx, session, providerRunResult{ExitCode: result.ExitCode, Err: fmt.Errorf("%w: inconsistent known nonzero evidence", backupasset.ErrInvalidState), Managed: managed})
		}
		if err := session.Fail(ctx, backupasset.FailureProviderNonzeroExit); err != nil {
			return publicationFinalization{result: providerRunResult{ExitCode: result.ExitCode, Err: errors.Join(runErr, err), Managed: managed}}
		}
		return publicationFinalization{result: providerRunResult{ExitCode: result.ExitCode, Err: runErr, Managed: managed}, finalized: true}
	case backupasset.CompletionOutcomeUnknown:
		if result.ExitCode != provider.UnknownProviderExitCode {
			return rejectPublicationPrecondition(ctx, session, providerRunResult{ExitCode: result.ExitCode, Err: fmt.Errorf("%w: inconsistent unknown-outcome evidence", backupasset.ErrInvalidState), Managed: managed, SuppressRetry: true})
		}
		code := publicationUnknownOutcomeCode(runErr)
		if err := session.Defer(ctx, publication.Deferral{Completion: backupasset.CompletionOutcomeUnknown, Code: code}); err != nil {
			return publicationFinalization{result: providerRunResult{ExitCode: result.ExitCode, Err: err, Managed: managed, SuppressRetry: true, WarningCode: backupasset.FailurePublicationSessionAbandoned}}
		}
		if runErr == nil {
			runErr = fmt.Errorf("provider command outcome is unknown")
		}
		return publicationFinalization{result: providerRunResult{ExitCode: result.ExitCode, Err: runErr, Managed: managed, SuppressRetry: true, WarningCode: code}, finalized: true}
	default:
		return rejectPublicationPrecondition(ctx, session, providerRunResult{ExitCode: result.ExitCode, Err: fmt.Errorf("%w: invalid evidence completion", backupasset.ErrInvalidState), Managed: managed})
	}
}

func recordPublicationCommit(ctx context.Context, session publication.Execution, evidence provider.ProviderCommit) error {
	for {
		_, err := session.RecordProviderCommit(ctx, evidence)
		if !errors.Is(err, backupasset.ErrPublicationUnconfirmed) {
			return err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return backupasset.ErrPublicationUnconfirmed
		case <-timer.C:
		}
	}
}

func publicationUnknownOutcomeCode(err error) backupasset.PublicationFailureCode {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, sshutil.ErrCommandTimeout):
		return backupasset.FailureProviderTimeout
	case errors.Is(err, context.Canceled):
		return backupasset.FailureProviderCanceled
	case errors.Is(err, sshutil.ErrCommandOutputLimit):
		return backupasset.FailureProviderResourceLimit
	default:
		return backupasset.FailureProviderOutcomeUnknown
	}
}

func taskPublicationAuditContext(runID uint) (backupasset.PublicationAuditContext, error) {
	if runID == 0 {
		return backupasset.PublicationAuditContext{}, fmt.Errorf("%w: TaskRun ID is required", backupasset.ErrInvalidState)
	}
	sum := sha256.Sum256([]byte("xirang.publication.correlation.v1\x00" + strconv.FormatUint(uint64(runID), 10)))
	return backupasset.NewSystemPublicationAuditContext("pub-" + hex.EncodeToString(sum[:16]))
}
