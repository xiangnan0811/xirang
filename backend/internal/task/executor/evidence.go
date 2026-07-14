package executor

import (
	"context"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

const (
	maxResticEvidenceConfigBytes  = 1 << 20
	maxResticEvidenceExcludes     = 256
	maxResticEvidenceExcludeBytes = 4096
)

// EvidenceExecutionRequest carries the coordinator-owned exact publication
// attempt into the Restic-only evidence lane. Repository access and secrets
// are intentionally absent: they belong to the validated Provider binding.
type EvidenceExecutionRequest struct {
	Task      model.Task
	TaskRunID uint
	Attempt   provider.PublicationAttempt
}

type EvidenceExecutionResult struct {
	ExitCode       int
	Completion     backupasset.ProviderCompletionClass
	ProviderCommit *provider.ProviderCommitEvidence
	EvidenceCode   backupasset.PublicationFailureCode
}

// EvidenceExecutor is deliberately separate from Executor so non-Restic
// executors retain their legacy compatibility contracts.
type EvidenceExecutor interface {
	RunWithEvidence(context.Context, EvidenceExecutionRequest, LogFunc, ProgressFunc) (EvidenceExecutionResult, error)
}
