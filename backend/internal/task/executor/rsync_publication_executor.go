package executor

import (
	"context"
	"fmt"
	"os"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"
)

// RsyncPublicationExecutor owns only the managed provider lane. Its ordinary
// methods remain exact delegates to RsyncExecutor so legacy_mutable tasks keep
// their established behavior and never receive a managed-root locator.
type RsyncPublicationExecutor struct {
	legacy   *RsyncExecutor
	strategy provider.PublicationStrategy
}

func (executor *RsyncPublicationExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	if executor == nil || executor.legacy == nil {
		return -1, fmt.Errorf("%w: legacy Rsync executor unavailable", backupasset.ErrInvalidState)
	}
	return executor.legacy.Run(ctx, task, logf, progressf)
}

func (executor *RsyncPublicationExecutor) RunRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	if executor == nil || executor.legacy == nil {
		return -1, fmt.Errorf("%w: legacy Rsync restore executor unavailable", backupasset.ErrInvalidState)
	}
	return executor.legacy.RunRestore(ctx, task, logf, progressf)
}

func (executor *RsyncPublicationExecutor) RunWithPublication(ctx context.Context, request PublicationExecutionRequest, logf LogFunc, _ ProgressFunc) (PublicationExecutionResult, error) {
	attempt, err := request.Attempt.RsyncTreeAttempt()
	if executor == nil || executor.strategy == nil || executor.strategy.Kind() != backupasset.ProviderRsync || executor.legacy == nil || err != nil ||
		request.TaskRunID == 0 || request.Task.ID == 0 || request.RsyncTreeInput == nil || request.Task.ID != attempt.TaskID ||
		request.TaskRunID != attempt.TaskRunID || strings.ToLower(strings.TrimSpace(request.Task.ExecutorType)) != "rsync" {
		return PublicationExecutionResult{}, fmt.Errorf("%w: managed Rsync publication executor unavailable", backupasset.ErrInvalidState)
	}
	input := cloneRsyncTreePublicationInput(*request.RsyncTreeInput)
	if input.Source.LocalPath != "" || input.Source.Remote != nil {
		return PublicationExecutionResult{}, fmt.Errorf("%w: managed Rsync source must be executor-derived", backupasset.ErrInvalidState)
	}
	source, cleanup, err := managedRsyncSourceForTask(ctx, request.Task)
	if err != nil {
		return PublicationExecutionResult{}, err
	}
	defer cleanup()
	input.Source = source
	prepared, err := executor.strategy.Prepare(ctx, provider.PublicationPrepareRequest{Attempt: request.Attempt, RsyncTreeInput: &input})
	if err != nil {
		return PublicationExecutionResult{}, err
	}
	if logf != nil {
		logf("info", "开始受管 rsync 恢复点发布")
	}
	result, runErr := executor.strategy.Execute(ctx, prepared, provider.PublicationProgress{})
	if logf != nil {
		if runErr == nil && result.Completion == backupasset.CompletionKnownExitZero {
			logf("info", "受管 rsync 传输已结束")
		} else {
			logf("warn", "受管 rsync 传输未确认")
		}
	}
	output := PublicationExecutionResult{ExitCode: result.ExitCode, Completion: result.Completion, EvidenceCode: result.EvidenceCode}
	if result.Completion == backupasset.CompletionKnownNonzero && runErr == nil {
		runErr = fmt.Errorf("managed Rsync transfer exited nonzero")
	}
	if runErr != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 || result.EvidenceCode != "" || result.ProviderCommit == nil {
		return output, runErr
	}
	commit, err := executor.strategy.RecordCommit(ctx, prepared, result)
	if err != nil {
		return output, err
	}
	output.ProviderCommit = &commit
	return output, nil
}

func cloneRsyncTreePublicationInput(input provider.RsyncTreePublicationInput) provider.RsyncTreePublicationInput {
	input.MarkerKey = append([]byte(nil), input.MarkerKey...)
	if input.Source.Remote != nil {
		remote := *input.Source.Remote
		input.Source.Remote = &remote
	}
	return input
}

func managedRsyncSourceForTask(ctx context.Context, task model.Task) (provider.RsyncTreeCommandSource, func(), error) {
	source := strings.TrimSpace(task.RsyncSource)
	if source == "" || strings.ContainsRune(source, '\x00') {
		return provider.RsyncTreeCommandSource{}, func() {}, fmt.Errorf("%w: managed Rsync source is unavailable", backupasset.ErrInvalidState)
	}
	if strings.TrimSpace(task.Node.Host) == "" {
		return provider.RsyncTreeCommandSource{LocalPath: source}, func() {}, nil
	}
	remote, cleanup, err := managedRsyncRemoteSource(ctx, task.Node, source)
	if err != nil {
		return provider.RsyncTreeCommandSource{}, func() {}, err
	}
	return provider.RsyncTreeCommandSource{Remote: &remote}, cleanup, nil
}

func managedRsyncRemoteSource(ctx context.Context, node model.Node, source string) (provider.RsyncTreeRemoteSource, func(), error) {
	if strings.ToLower(strings.TrimSpace(node.AuthType)) != "key" {
		err := fmt.Errorf("managed Rsync remote source requires SSH key authentication")
		writeRsyncCredentialAudit(ctx, node, sshutil.ResolvedCredential{}, credentialaudit.OutcomeBlocked, "managed_rsync_key_resolve", err)
		return provider.RsyncTreeRemoteSource{}, func() {}, err
	}
	keyContent, _, credential, err := resolveNodePrivateKeyForPurpose(node, sshutil.PurposeTaskBackup)
	if err != nil {
		writeRsyncCredentialAudit(ctx, node, credential, credentialaudit.OutcomeBlocked, "managed_rsync_key_resolve", err)
		return provider.RsyncTreeRemoteSource{}, func() {}, err
	}
	normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
	if err != nil || normalizedKey == "" {
		if err == nil {
			err = fmt.Errorf("managed Rsync SSH key is unavailable")
		}
		writeRsyncCredentialAudit(ctx, node, credential, credentialaudit.OutcomeFailure, "managed_rsync_key_prepare", err)
		return provider.RsyncTreeRemoteSource{}, func() {}, err
	}
	if err := ensureTempKeyDir(); err != nil {
		return provider.RsyncTreeRemoteSource{}, func() {}, fmt.Errorf("prepare managed Rsync SSH key directory: %w", err)
	}
	keyFile, err := os.CreateTemp(tempKeyDir, "xirang-managed-rsync-key-*.pem")
	if err != nil {
		return provider.RsyncTreeRemoteSource{}, func() {}, fmt.Errorf("prepare managed Rsync SSH key file: %w", err)
	}
	cleanup := func() { _ = os.Remove(keyFile.Name()) }
	if _, err := keyFile.WriteString(normalizedKey); err != nil {
		_ = keyFile.Close()
		cleanup()
		return provider.RsyncTreeRemoteSource{}, func() {}, fmt.Errorf("write managed Rsync SSH key file: %w", err)
	}
	if err := keyFile.Close(); err != nil {
		cleanup()
		return provider.RsyncTreeRemoteSource{}, func() {}, fmt.Errorf("close managed Rsync SSH key file: %w", err)
	}
	if err := os.Chmod(keyFile.Name(), 0o600); err != nil {
		cleanup()
		return provider.RsyncTreeRemoteSource{}, func() {}, fmt.Errorf("protect managed Rsync SSH key file: %w", err)
	}
	strictHostChecking, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil || !strictHostChecking {
		cleanup()
		if err == nil {
			err = fmt.Errorf("managed Rsync requires strict SSH host key checking")
		}
		return provider.RsyncTreeRemoteSource{}, func() {}, err
	}
	knownHosts, err := util.ExpandHomePath(util.GetEnvOrDefault("SSH_KNOWN_HOSTS_PATH", "~/.ssh/known_hosts"))
	if err != nil {
		cleanup()
		return provider.RsyncTreeRemoteSource{}, func() {}, fmt.Errorf("resolve managed Rsync known-hosts path: %w", err)
	}
	autoAccept, err := util.ReadBoolEnv("SSH_AUTO_ACCEPT_NEW_HOSTS", true)
	if err != nil {
		cleanup()
		return provider.RsyncTreeRemoteSource{}, func() {}, err
	}
	hostKeyMode := provider.RsyncTreeHostKeyStrict
	if autoAccept {
		hostKeyMode = provider.RsyncTreeHostKeyAcceptNew
	}
	user := strings.TrimSpace(node.Username)
	if user == "" {
		cleanup()
		return provider.RsyncTreeRemoteSource{}, func() {}, fmt.Errorf("%w: managed Rsync SSH user is unavailable", backupasset.ErrInvalidState)
	}
	if node.Port < 0 || node.Port > 65535 {
		cleanup()
		return provider.RsyncTreeRemoteSource{}, func() {}, fmt.Errorf("%w: managed Rsync SSH port is invalid", backupasset.ErrInvalidState)
	}
	writeRsyncCredentialAudit(ctx, node, credential, credentialaudit.OutcomeSuccess, "managed_rsync_key_prepare", nil)
	return provider.RsyncTreeRemoteSource{
		User: user, Host: strings.TrimSpace(node.Host), Path: source, UseSudoRsync: NeedsSudo(node),
		Transport: provider.RsyncTreeSSHTransport{Port: uint16(node.Port), HostKeyMode: hostKeyMode, KnownHostsFile: knownHosts, IdentityFile: keyFile.Name()},
	}, cleanup, nil
}

var _ Executor = (*RsyncPublicationExecutor)(nil)
var _ RestoreExecutor = (*RsyncPublicationExecutor)(nil)
var _ PublicationExecutor = (*RsyncPublicationExecutor)(nil)
