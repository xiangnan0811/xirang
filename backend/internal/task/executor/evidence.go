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

// PublicationExecutionRequest carries the coordinator-owned, tagged exact
// publication attempt into a provider executor. Repository access and secrets
// are intentionally absent: they belong to the validated Provider binding.
type PublicationExecutionRequest struct {
	Task           model.Task
	TaskRunID      uint
	Attempt        provider.TaggedPublicationAttempt
	RsyncTreeInput *provider.RsyncTreePublicationInput
	RcloneInput    *provider.RclonePublicationInput
}

// PublicationExecutionResult deliberately preserves the transfer outcome
// independently from the optional Provider commit fact. The task runner owns
// the resulting database publication transition.
type PublicationExecutionResult struct {
	ExitCode       int
	Completion     backupasset.ProviderCompletionClass
	ProviderCommit *provider.ProviderCommit
	EvidenceCode   backupasset.PublicationFailureCode
}

// PublicationExecutor is deliberately separate from Executor so legacy
// mutable executors retain their compatibility contracts. Providers receive a
// closed tagged attempt and must never accept an untyped payload here.
type PublicationExecutor interface {
	RunWithPublication(context.Context, PublicationExecutionRequest, LogFunc, ProgressFunc) (PublicationExecutionResult, error)
}
