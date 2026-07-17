package executor

import (
	"context"
	"fmt"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

// RclonePublicationExecutor owns only the managed provider lane. Legacy Run
// and RunRestore remain exact delegates so pristine node-default Rclone tasks
// keep their compatibility behavior.
type RclonePublicationExecutor struct {
	legacy   *RcloneExecutor
	strategy provider.PublicationStrategy
}

func (executor *RclonePublicationExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	if executor == nil || executor.legacy == nil {
		return -1, fmt.Errorf("%w: legacy Rclone executor unavailable", backupasset.ErrInvalidState)
	}
	return executor.legacy.Run(ctx, task, logf, progressf)
}

func (executor *RclonePublicationExecutor) RunRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	if executor == nil || executor.legacy == nil {
		return -1, fmt.Errorf("%w: legacy Rclone restore executor unavailable", backupasset.ErrInvalidState)
	}
	return executor.legacy.RunRestore(ctx, task, logf, progressf)
}

func (executor *RclonePublicationExecutor) RunWithPublication(
	ctx context.Context,
	request PublicationExecutionRequest,
	logf LogFunc,
	_ ProgressFunc,
) (PublicationExecutionResult, error) {
	attempt, err := request.Attempt.RcloneAttempt()
	if executor == nil || executor.legacy == nil || executor.strategy == nil || executor.strategy.Kind() != backupasset.ProviderRclone ||
		err != nil || request.Task.ID == 0 || request.TaskRunID == 0 || request.RcloneInput == nil ||
		request.Task.ID != attempt.TaskID || request.TaskRunID != attempt.TaskRunID ||
		strings.ToLower(strings.TrimSpace(request.Task.ExecutorType)) != string(backupasset.ProviderRclone) {
		return PublicationExecutionResult{}, fmt.Errorf("%w: managed Rclone publication executor unavailable", backupasset.ErrInvalidState)
	}
	input := cloneRclonePublicationInput(*request.RcloneInput)
	source, err := provider.NewRclonePrivateLocator(strings.TrimSpace(request.Task.RsyncSource))
	if err != nil {
		return PublicationExecutionResult{}, fmt.Errorf("%w: managed Rclone source is unavailable", backupasset.ErrInvalidState)
	}
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if input.PortableRequest == nil || input.NativeRequest != nil || input.PortableRequest.Source != (provider.RclonePrivateLocator{}) {
			return PublicationExecutionResult{}, fmt.Errorf("%w: managed Rclone portable source must be executor-derived", backupasset.ErrInvalidState)
		}
		input.PortableRequest.Source = source
	case backupasset.PublicationNativeObjectVersions:
		if input.NativeRequest == nil || input.PortableRequest != nil || input.NativeRequest.Source != (provider.RclonePrivateLocator{}) {
			return PublicationExecutionResult{}, fmt.Errorf("%w: managed Rclone native source must be executor-derived", backupasset.ErrInvalidState)
		}
		input.NativeRequest.Source = source
	default:
		return PublicationExecutionResult{}, fmt.Errorf("%w: unsupported managed Rclone publication mode", backupasset.ErrInvalidState)
	}
	prepared, err := executor.strategy.Prepare(ctx, provider.PublicationPrepareRequest{Attempt: request.Attempt, RcloneInput: &input})
	if err != nil {
		return PublicationExecutionResult{}, err
	}
	if logf != nil {
		logf("info", "开始受管 Rclone 恢复点发布")
	}
	result, runErr := executor.strategy.Execute(ctx, prepared, provider.PublicationProgress{})
	if logf != nil {
		if runErr == nil && result.Completion == backupasset.CompletionKnownExitZero {
			logf("info", "受管 Rclone 传输与证据提交已结束")
		} else {
			logf("warn", "受管 Rclone 传输或证据提交未确认")
		}
	}
	output := PublicationExecutionResult{
		ExitCode: result.ExitCode, Completion: result.Completion, EvidenceCode: result.EvidenceCode,
	}
	if result.Completion == backupasset.CompletionKnownNonzero && runErr == nil {
		runErr = fmt.Errorf("managed Rclone transfer exited nonzero")
	}
	if runErr != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 ||
		result.EvidenceCode != "" || result.ProviderCommit == nil {
		return output, runErr
	}
	commit, err := executor.strategy.RecordCommit(ctx, prepared, result)
	if err != nil {
		return output, err
	}
	output.ProviderCommit = &commit
	return output, nil
}

func cloneRclonePublicationInput(input provider.RclonePublicationInput) provider.RclonePublicationInput {
	if input.PortableRequest != nil {
		portable := *input.PortableRequest
		portable.MarkerKey = append([]byte(nil), portable.MarkerKey...)
		portable.Manifest.IndexEncoded = append([]byte(nil), portable.Manifest.IndexEncoded...)
		portable.Manifest.Chunks = append([]provider.RcloneManifestChunk(nil), portable.Manifest.Chunks...)
		for index := range portable.Manifest.Chunks {
			portable.Manifest.Chunks[index].Encoded = append([]byte(nil), portable.Manifest.Chunks[index].Encoded...)
		}
		if portable.CopyDest != nil {
			copyDest := *portable.CopyDest
			portable.CopyDest = &copyDest
		}
		input.PortableRequest = &portable
	}
	if input.NativeRequest != nil {
		native := *input.NativeRequest
		native.RcloneConfig = append([]byte(nil), native.RcloneConfig...)
		native.MarkerKey = append([]byte(nil), native.MarkerKey...)
		native.KMSKeyBindings = append([]provider.RcloneNativeKMSKeyDigestBinding(nil), native.KMSKeyBindings...)
		native.Encryption.RetainedReadKeyARNs = append([]string(nil), native.Encryption.RetainedReadKeyARNs...)
		native.Manifest.IndexEncoded = append([]byte(nil), native.Manifest.IndexEncoded...)
		native.Manifest.Chunks = append([]provider.RcloneManifestChunk(nil), native.Manifest.Chunks...)
		for index := range native.Manifest.Chunks {
			native.Manifest.Chunks[index].Encoded = append([]byte(nil), native.Manifest.Chunks[index].Encoded...)
		}
		input.NativeRequest = &native
	}
	return input
}

var _ Executor = (*RclonePublicationExecutor)(nil)
var _ RestoreExecutor = (*RclonePublicationExecutor)(nil)
var _ PublicationExecutor = (*RclonePublicationExecutor)(nil)
