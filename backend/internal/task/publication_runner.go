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
	ExitCode      int
	Err           error
	SuppressRetry bool
	Managed       bool
	WarningCode   backupasset.PublicationFailureCode
}

func shouldRunLegacyVerification(result providerRunResult, policy *model.Policy) bool {
	return !result.Managed && policy != nil && policy.VerifyEnabled
}

// executeProvider keeps TaskRun transfer truth separate from asynchronous
// recovery-point publication. A successful evidence transfer returns as soon
// as its exact commit fact is durable; manifest work remains with the worker.
func (m *Manager) executeProvider(ctx context.Context, taskEntity model.Task, runID uint, reason string, chainRunID string, logf executor.LogFunc, progressf executor.ProgressFunc) providerRunResult {
	if m == nil || m.executorFactory == nil {
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: task executor factory unavailable", backupasset.ErrInvalidState)}
	}
	exec := m.executorFactory.Resolve(taskEntity.ExecutorType)
	if exec == nil {
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: task executor unavailable", backupasset.ErrInvalidState)}
	}
	providerKind := strings.ToLower(strings.TrimSpace(taskEntity.ExecutorType))
	if m.publicationCoordinator == nil || (providerKind != "restic" && providerKind != "rsync") {
		exitCode, err := exec.Run(ctx, taskEntity, logf, progressf)
		return providerRunResult{ExitCode: exitCode, Err: err}
	}

	audit, err := taskPublicationAuditContext(runID)
	if err != nil {
		return providerRunResult{ExitCode: -1, Err: err, Managed: true}
	}
	session, err := m.publicationCoordinator.Prepare(ctx, publication.Run{
		Task: taskEntity, TaskRunID: runID, Trigger: reason, ChainRunID: chainRunID, Audit: audit,
	})
	if err != nil {
		return providerRunResult{ExitCode: -1, Err: err, Managed: true}
	}
	if session == nil {
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: nil publication session", backupasset.ErrInvalidState), Managed: true}
	}

	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), sshutil.CommandExecutionJoinTimeout)
	defer cleanupCancel()
	resolved := false
	defer func() {
		if !resolved && session.Mode() == publication.ModeEvidence {
			_ = session.Abandon(backupasset.ErrPublicationSessionAbandoned)
		}
	}()

	commandCtx := session.Context()
	if commandCtx == nil {
		_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
		resolved = true
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: publication execution context unavailable", backupasset.ErrInvalidState), Managed: true}
	}
	if session.Mode() == publication.ModeCompatibility {
		exitCode, runErr := exec.Run(commandCtx, taskEntity, logf, progressf)
		completeErr := session.CompleteCompatibility(cleanupCtx)
		resolved = true
		if runErr != nil {
			return providerRunResult{ExitCode: exitCode, Err: runErr}
		}
		return providerRunResult{ExitCode: exitCode, Err: completeErr}
	}
	if session.Mode() != publication.ModeEvidence || session.Attempt() == nil {
		_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
		resolved = true
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: invalid publication evidence session", backupasset.ErrInvalidState), Managed: true}
	}
	publicationExecutor, ok := exec.(executor.PublicationExecutor)
	if !ok {
		_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
		resolved = true
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: Restic executor has no publication lane", backupasset.ErrInvalidState), Managed: true}
	}
	attempt := session.Attempt()
	if attempt == nil {
		_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
		resolved = true
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: missing tagged publication attempt", backupasset.ErrInvalidState), Managed: true}
	}
	request := executor.PublicationExecutionRequest{Task: taskEntity, TaskRunID: runID, Attempt: *attempt}
	recoveryPointID := ""
	switch providerKind {
	case "restic":
		resticAttempt, attemptErr := attempt.ResticAttempt()
		if attemptErr != nil {
			_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
			resolved = true
			return providerRunResult{ExitCode: -1, Err: attemptErr, Managed: true}
		}
		recoveryPointID = resticAttempt.RecoveryPointID
	case "rsync":
		rsyncAttempt, attemptErr := attempt.RsyncTreeAttempt()
		if attemptErr != nil {
			_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
			resolved = true
			return providerRunResult{ExitCode: -1, Err: attemptErr, Managed: true}
		}
		inputProvider, ok := session.(interface {
			RsyncTreePublicationInput() (provider.RsyncTreePublicationInput, error)
		})
		if !ok {
			_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
			resolved = true
			return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: managed Rsync publication input is unavailable", backupasset.ErrInvalidState), Managed: true}
		}
		input, inputErr := inputProvider.RsyncTreePublicationInput()
		if inputErr != nil {
			_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
			resolved = true
			return providerRunResult{ExitCode: -1, Err: inputErr, Managed: true}
		}
		request.RsyncTreeInput = &input
		recoveryPointID = rsyncAttempt.RecoveryPointID
	default:
		_ = session.Reject(cleanupCtx, backupasset.FailurePublicationPreconditionMissing)
		resolved = true
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: unsupported managed publication provider", backupasset.ErrInvalidState), Managed: true}
	}
	result, runErr := publicationExecutor.RunWithPublication(commandCtx, request, logf, progressf)
	providerResult := m.finishPublicationExecution(cleanupCtx, session, result, runErr)
	if providerResult.WarningCode != "" && logf != nil {
		logf("warn", fmt.Sprintf("恢复点发布未提交: point_id=%s code=%s", recoveryPointID, providerResult.WarningCode))
	}
	resolved = true
	return providerResult
}

func (m *Manager) finishPublicationExecution(ctx context.Context, session publication.Execution, result executor.PublicationExecutionResult, runErr error) providerRunResult {
	if session == nil {
		return providerRunResult{ExitCode: -1, Err: fmt.Errorf("%w: publication execution unavailable", backupasset.ErrInvalidState), Managed: true}
	}
	managed := true
	switch result.Completion {
	case backupasset.CompletionKnownExitZero:
		if result.ExitCode != 0 {
			_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
			return providerRunResult{ExitCode: result.ExitCode, Err: fmt.Errorf("%w: known exit-zero result has nonzero exit", backupasset.ErrInvalidState), Managed: managed}
		}
		if runErr != nil {
			_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
			return providerRunResult{ExitCode: result.ExitCode, Err: runErr, Managed: managed}
		}
		if result.ProviderCommit != nil && result.EvidenceCode == "" {
			if err := recordPublicationCommit(ctx, session, *result.ProviderCommit); err != nil {
				cause := backupasset.ErrPublicationSessionAbandoned
				if errors.Is(err, backupasset.ErrPublicationUnconfirmed) {
					cause = backupasset.ErrPublicationUnconfirmed
				}
				_ = session.Abandon(cause)
				return providerRunResult{ExitCode: 0, Managed: managed, WarningCode: backupasset.FailurePublicationSessionAbandoned}
			}
			return providerRunResult{ExitCode: 0, Managed: managed}
		}
		if result.ProviderCommit != nil || result.EvidenceCode == "" {
			_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
			return providerRunResult{ExitCode: 0, Err: fmt.Errorf("%w: inconsistent known exit-zero evidence", backupasset.ErrInvalidState), Managed: managed}
		}
		if err := session.Defer(ctx, publication.Deferral{Completion: backupasset.CompletionKnownExitZero, Code: result.EvidenceCode}); err != nil {
			_ = session.Abandon(backupasset.ErrPublicationSessionAbandoned)
			return providerRunResult{ExitCode: 0, Managed: managed, WarningCode: backupasset.FailurePublicationSessionAbandoned}
		}
		return providerRunResult{ExitCode: 0, Managed: managed, WarningCode: result.EvidenceCode}
	case backupasset.CompletionKnownNonzero:
		if result.ExitCode <= 0 || runErr == nil {
			_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
			return providerRunResult{ExitCode: result.ExitCode, Err: fmt.Errorf("%w: inconsistent known nonzero evidence", backupasset.ErrInvalidState), Managed: managed}
		}
		if err := session.Fail(ctx, backupasset.FailureProviderNonzeroExit); err != nil {
			return providerRunResult{ExitCode: result.ExitCode, Err: err, Managed: managed}
		}
		return providerRunResult{ExitCode: result.ExitCode, Err: runErr, Managed: managed}
	case backupasset.CompletionOutcomeUnknown:
		if result.ExitCode != provider.UnknownProviderExitCode {
			_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
			return providerRunResult{ExitCode: result.ExitCode, Err: fmt.Errorf("%w: inconsistent unknown-outcome evidence", backupasset.ErrInvalidState), Managed: managed, SuppressRetry: true}
		}
		code := publicationUnknownOutcomeCode(runErr)
		if err := session.Defer(ctx, publication.Deferral{Completion: backupasset.CompletionOutcomeUnknown, Code: code}); err != nil {
			return providerRunResult{ExitCode: result.ExitCode, Err: err, Managed: managed, SuppressRetry: true}
		}
		if runErr == nil {
			runErr = fmt.Errorf("provider command outcome is unknown")
		}
		return providerRunResult{ExitCode: result.ExitCode, Err: runErr, Managed: managed, SuppressRetry: true, WarningCode: code}
	default:
		_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		return providerRunResult{ExitCode: result.ExitCode, Err: fmt.Errorf("%w: invalid evidence completion", backupasset.ErrInvalidState), Managed: managed}
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
