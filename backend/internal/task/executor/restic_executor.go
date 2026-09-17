package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"golang.org/x/crypto/ssh"
)

// ResticConfig 是 restic 执行器的配置（存储在 Task.ExecutorConfig JSON 中）。
type ResticConfig struct {
	RepositoryPassword string   `json:"repository_password,omitempty"`
	ExcludePatterns    []string `json:"exclude_patterns,omitempty"`
	RepositoryVersion  *int     `json:"repository_version,omitempty"`
}

// ResticExecutor 通过 SSH 在远程节点上执行 restic 备份/恢复操作。
// restic 必须在目标节点上预先安装（agentless 原则）。
// 配置字段语义：
//   - task.RsyncSource = 备份源路径（节点本地路径，如 /data/app）
//   - task.RsyncTarget = restic 仓库路径（如 /backup/repo 或 sftp:user@host:/backup）
//   - task.ExecutorConfig = JSON，含 repository_password 和 exclude_patterns
type ResticExecutor struct {
	binary   string // restic 二进制名称，默认 "restic"
	strategy provider.PublicationStrategy
}

type resticEvidenceConfig struct {
	ExcludePatterns []string `json:"exclude_patterns"`
}

// RunWithPublication uses the Provider-owned Restic strategy lane. It must
// never initialize repositories, construct password files, or read legacy
// access secrets from a Task configuration.
func (e *ResticExecutor) RunWithPublication(ctx context.Context, request PublicationExecutionRequest, logf LogFunc, progressf ProgressFunc) (PublicationExecutionResult, error) {
	attempt, err := request.Attempt.ResticAttempt()
	if e == nil || e.strategy == nil || e.strategy.Kind() != backupasset.ProviderRestic || err != nil || request.TaskRunID == 0 || request.Task.ID == 0 ||
		request.Task.ID != attempt.TaskID || request.TaskRunID != attempt.TaskRunID ||
		strings.ToLower(strings.TrimSpace(request.Task.ExecutorType)) != "restic" {
		return PublicationExecutionResult{}, fmt.Errorf("%w: Restic publication executor unavailable", backupasset.ErrInvalidState)
	}
	config, err := parseResticEvidenceConfig(request.Task.ExecutorConfig)
	if err != nil {
		return PublicationExecutionResult{}, fmt.Errorf("parse Restic publication config: %w", err)
	}
	input := provider.ResticBackupInput{Source: strings.TrimSpace(request.Task.RsyncSource), Excludes: append([]string(nil), config.ExcludePatterns...)}
	prepared, err := e.strategy.Prepare(ctx, provider.PublicationPrepareRequest{Attempt: request.Attempt, ResticInput: &input})
	if err != nil {
		return PublicationExecutionResult{}, err
	}
	if logf != nil {
		logf("info", "开始受管 restic 证据备份")
	}
	result, runErr := e.strategy.Execute(ctx, prepared, provider.PublicationProgress{OnResticProgress: func(progress provider.ResticBackupProgress) {
		if progressf != nil {
			progressf(ProgressSample{ObservedAt: progress.ObservedAt, Percent: progress.Percent, ThroughputMbps: progress.ThroughputMbps})
		}
	}})
	if logf != nil {
		if runErr == nil {
			logf("info", "受管 restic 证据备份已结束")
		} else {
			logf("warn", "受管 restic 证据备份未确认")
		}
	}
	output := PublicationExecutionResult{ExitCode: result.ExitCode, Completion: result.Completion, EvidenceCode: result.EvidenceCode}
	if runErr != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 || result.EvidenceCode != "" || result.ProviderCommit == nil {
		return output, runErr
	}
	commit, err := e.strategy.RecordCommit(ctx, prepared, result)
	if err != nil {
		return output, err
	}
	output.ProviderCommit = &commit
	return output, nil
}

func parseResticEvidenceConfig(raw string) (resticEvidenceConfig, error) {
	if len(raw) > maxResticEvidenceConfigBytes {
		return resticEvidenceConfig{}, fmt.Errorf("%w: Restic evidence config exceeds byte limit", backupasset.ErrInvalidState)
	}
	if strings.TrimSpace(raw) == "" {
		return resticEvidenceConfig{}, nil
	}
	decoder := json.NewDecoder(io.LimitReader(strings.NewReader(raw), maxResticEvidenceConfigBytes+1))
	var config resticEvidenceConfig
	if err := decoder.Decode(&config); err != nil {
		return resticEvidenceConfig{}, fmt.Errorf("%w: invalid Restic evidence config", backupasset.ErrInvalidState)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return resticEvidenceConfig{}, fmt.Errorf("%w: Restic evidence config has trailing data", backupasset.ErrInvalidState)
	}
	if len(config.ExcludePatterns) > maxResticEvidenceExcludes {
		return resticEvidenceConfig{}, fmt.Errorf("%w: too many Restic evidence excludes", backupasset.ErrInvalidState)
	}
	for _, pattern := range config.ExcludePatterns {
		if !utf8.ValidString(pattern) || len(pattern) > maxResticEvidenceExcludeBytes || strings.ContainsRune(pattern, '\x00') {
			return resticEvidenceConfig{}, fmt.Errorf("%w: invalid Restic evidence exclude", backupasset.ErrInvalidState)
		}
	}
	return config, nil
}

func (e *ResticExecutor) resticBinary() string {
	if e.binary != "" {
		return e.binary
	}
	return util.GetEnvOrDefault("RESTIC_BINARY", "restic")
}

func (e *ResticExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (exitCode int, returnErr error) {
	source := strings.TrimSpace(task.RsyncSource)
	repo := strings.TrimSpace(task.RsyncTarget)
	if source == "" || repo == "" {
		return -1, fmt.Errorf("restic 备份任务缺少源路径或仓库路径")
	}

	cfg, access, err := parseResticConfigWithRepositoryAccess(task.ExecutorConfig)
	if err != nil {
		return -1, fmt.Errorf("解析 restic 配置失败: %w", err)
	}

	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeTaskBackup)
	if err != nil {
		return -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	// 生成唯一的密码临时文件路径，并在远程节点上创建
	pwFilePath := BuildResticPasswordFilePath()
	// Arm cleanup before creation so cancellation or a partial create cannot
	// leave a private directory behind. The helper is collision-safe.
	defer func() {
		if cleanupErr := CleanupResticPasswordFile(task.Node, pwFilePath, sshutil.PurposeTaskBackup); cleanupErr != nil {
			if logf != nil {
				logf("warn", sanitizeExecutorRuntimeEvidence(fmt.Sprintf("restic 密码临时文件清理失败（备份业务结果保持原状态）: %v", cleanupErr)))
			}
			if returnErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()
	if err := CreateResticPasswordFile(ctx, client, pwFilePath, access); err != nil {
		return -1, fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}
	// The cleanup defer remains active for the full Restic operation.

	bin := e.resticBinary()

	// 检查 restic 是否安装
	if _, err := RunSSHCommandOutput(ctx, client, "which "+bin+" 2>/dev/null || command -v "+bin+" 2>/dev/null"); err != nil {
		return -1, fmt.Errorf("目标节点未安装 restic，请先在节点上安装")
	}

	repoArg := ShellEscape(repo)
	cmdPrefix := e.buildCommandPrefix(task.Node, pwFilePath)

	// 初始化仓库（若不存在）
	checkCmd := fmt.Sprintf("%s snapshots -r %s --json 2>&1", cmdPrefix, repoArg)
	checkOut, _ := RunSSHCommandOutput(ctx, client, checkCmd)
	if strings.Contains(checkOut, "Is there a repository at the following location") ||
		strings.Contains(checkOut, "repository does not exist") ||
		strings.Contains(checkOut, "no such file or directory") {
		logf("info", "初始化 restic 仓库")
		initFlags := ""
		if cfg.RepositoryVersion != nil {
			initFlags = fmt.Sprintf(" --repository-version %d", *cfg.RepositoryVersion)
		}
		initCmd := fmt.Sprintf("%s init%s -r %s 2>&1", cmdPrefix, initFlags, repoArg)
		initOut, initErr := RunSSHCommandOutput(ctx, client, initCmd)
		if initErr != nil {
			return -1, fmt.Errorf("初始化 restic 仓库失败: %s", sanitizeExecutorRuntimeEvidence(initOut))
		}
		logf("info", "restic 仓库初始化成功")
	}

	// 构造 backup 命令
	excludeArgs := buildResticExcludeArgs(cfg.ExcludePatterns)
	backupCmd := fmt.Sprintf("%s backup -r %s %s %s --json 2>&1",
		cmdPrefix, repoArg, ShellEscape(source), excludeArgs)

	logf("info", "开始 restic 备份")

	exitCode, runErr := e.streamSSHCommand(ctx, client, backupCmd, logf, progressf)
	if runErr != nil {
		return exitCode, fmt.Errorf("restic 备份执行失败: %w", runErr)
	}
	if exitCode != 0 {
		return exitCode, fmt.Errorf("restic 备份退出码: %d", exitCode)
	}
	logf("info", "restic 备份完成")
	return 0, nil
}

// RunRestore 在远程节点上执行 restic 恢复操作。
// restoreTask.RsyncSource = restic 仓库路径（原任务的 RsyncTarget）
// restoreTask.RsyncTarget = 恢复目标路径
func (e *ResticExecutor) RunRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (exitCode int, returnErr error) {
	repo := strings.TrimSpace(task.RsyncSource)
	targetPath := strings.TrimSpace(task.RsyncTarget)
	if repo == "" || targetPath == "" {
		return -1, fmt.Errorf("restic 恢复任务缺少仓库路径或目标路径")
	}

	_, access, err := parseResticConfigWithRepositoryAccess(task.ExecutorConfig)
	if err != nil {
		return -1, fmt.Errorf("解析 restic 配置失败: %w", err)
	}

	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeTaskRestore)
	if err != nil {
		return -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	// 生成唯一的密码临时文件路径，并在远程节点上创建
	pwFilePath := BuildResticPasswordFilePath()
	defer func() {
		if cleanupErr := CleanupResticPasswordFile(task.Node, pwFilePath, sshutil.PurposeTaskRestore); cleanupErr != nil {
			if logf != nil {
				logf("warn", sanitizeExecutorRuntimeEvidence(fmt.Sprintf("restic 密码临时文件清理失败（恢复业务结果保持原状态）: %v", cleanupErr)))
			}
			if returnErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()
	if err := CreateResticPasswordFile(ctx, client, pwFilePath, access); err != nil {
		return -1, fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}

	repoArg := ShellEscape(repo)
	cmdPrefix := e.buildCommandPrefix(task.Node, pwFilePath)

	restoreCmd := fmt.Sprintf("%s restore latest -r %s --target %s --json 2>&1",
		cmdPrefix, repoArg, ShellEscape(targetPath))

	logf("info", "开始 restic 恢复")
	exitCode, runErr := e.streamSSHCommand(ctx, client, restoreCmd, logf, progressf)
	if runErr != nil {
		return exitCode, fmt.Errorf("restic 恢复执行失败: %w", runErr)
	}
	if exitCode != 0 {
		return exitCode, fmt.Errorf("restic 恢复退出码: %d", exitCode)
	}
	logf("info", "restic 恢复完成")
	return 0, nil
}

// streamSSHCommand 通过 SSH 流式执行命令，解析 restic JSON 进度行。
func (e *ResticExecutor) streamSSHCommand(ctx context.Context, client *ssh.Client, cmd string, logf LogFunc, progressf ProgressFunc) (int, error) {
	runner := sshutil.NewSSHCommandRunnerWithTransportClose(client, 1)
	stream, err := runner.OpenRawExecution(ctx, sshutil.RawCommandSpec{
		Command: cmd,
		// The caller controls the backup/restore lifetime.
		MaxStdoutBytes: 0,
		MaxStderrBytes: 64 << 10,
		MaxRecordBytes: 1 << 20,
	})
	if err != nil {
		return -1, fmt.Errorf("启动远程命令失败: %w", err)
	}

	var lastBytesDone int64
	var lastObservedAt time.Time
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// 尝试解析 restic JSON 进度
		if strings.HasPrefix(line, "{") {
			if sample, ok := parseResticProgressLine(line, &lastBytesDone, &lastObservedAt); ok && progressf != nil {
				progressf(sample)
				continue
			}
		}
		if logf != nil {
			logf("info", sanitizeExecutorRuntimeEvidence(line))
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		cancelErr := stream.Cancel()
		if cancelErr != nil && !errors.Is(cancelErr, sshutil.ErrCommandFailed) {
			scanErr = errors.Join(scanErr, cancelErr)
		}
		return -1, scanErr
	}

	completion, err := stream.Join()
	if err != nil {
		return -1, err
	}
	if !completion.ExitCodeKnown {
		return -1, fmt.Errorf("远程命令完成但未返回退出状态")
	}
	return completion.ExitCode, nil
}

// resticStatusMsg 表示 restic --json 输出中的 status 类型消息。
type resticStatusMsg struct {
	MessageType  string   `json:"message_type"`
	PercentDone  float64  `json:"percent_done"`
	TotalBytes   int64    `json:"total_bytes"`
	BytesDone    int64    `json:"bytes_done"`
	CurrentFiles []string `json:"current_files"`
}

func parseResticProgressLine(line string, lastBytesDone *int64, lastObservedAt *time.Time) (ProgressSample, bool) {
	var msg resticStatusMsg
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return ProgressSample{}, false
	}
	if msg.MessageType != "status" || msg.TotalBytes == 0 {
		return ProgressSample{}, false
	}

	now := time.Now().UTC()
	deltaDone := msg.BytesDone - *lastBytesDone
	if deltaDone <= 0 || lastObservedAt.IsZero() {
		*lastBytesDone = msg.BytesDone
		*lastObservedAt = now
		return ProgressSample{}, false
	}

	elapsed := now.Sub(*lastObservedAt).Seconds()
	if elapsed < 0.5 {
		return ProgressSample{}, false
	}

	throughputMbps := float64(deltaDone) * 8 / elapsed / 1_000_000
	*lastBytesDone = msg.BytesDone
	*lastObservedAt = now

	return ProgressSample{
		ObservedAt:     now,
		ThroughputMbps: throughputMbps,
	}, true
}

// ResticSnapshot 表示一个 restic 快照。
type ResticSnapshot struct {
	ID       string   `json:"id"`
	ShortID  string   `json:"short_id"`
	Time     string   `json:"time"`
	Hostname string   `json:"hostname"`
	Paths    []string `json:"paths"`
}

// ResticEntry 表示 restic 快照中的一个文件/目录。
type ResticEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Path  string `json:"path"`
	Size  uint64 `json:"size"`
	Mtime string `json:"mtime"`
}

// ListSnapshots 列出 restic 仓库中的所有快照。
func (e *ResticExecutor) ListSnapshots(ctx context.Context, task model.Task) ([]ResticSnapshot, error) {
	return e.listSnapshots(ctx, task, "")
}

// ListSnapshotsByLinkTag lists only snapshots carrying the canonical immutable
// task-link marker. The tag is internal lineage data, never client input.
func (e *ResticExecutor) ListSnapshotsByLinkTag(ctx context.Context, task model.Task, linkTag string) ([]ResticSnapshot, error) {
	if !validResticLinkTag(linkTag) {
		return nil, fmt.Errorf("invalid Restic task-link tag")
	}
	return e.listSnapshots(ctx, task, linkTag)
}

func (e *ResticExecutor) listSnapshots(ctx context.Context, task model.Task, linkTag string) (result []ResticSnapshot, returnErr error) {
	repo := strings.TrimSpace(task.RsyncTarget)
	if repo == "" {
		return nil, fmt.Errorf("restic 仓库路径为空")
	}
	_, access, err := parseResticConfigWithRepositoryAccess(task.ExecutorConfig)
	if err != nil {
		return nil, err
	}

	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeSnapshot)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	// 生成唯一的密码临时文件路径，并在远程节点上创建
	pwFilePath := BuildResticPasswordFilePath()
	defer func() {
		if cleanupErr := CleanupResticPasswordFile(task.Node, pwFilePath, sshutil.PurposeSnapshot); cleanupErr != nil {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			} else {
				returnErr = cleanupErr
			}
		}
	}()
	if err := CreateResticPasswordFile(ctx, client, pwFilePath, access); err != nil {
		return nil, fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}

	cmdPrefix := e.buildCommandPrefix(task.Node, pwFilePath)
	cmd := fmt.Sprintf("%s snapshots -r %s --json", cmdPrefix, ShellEscape(repo))
	if linkTag != "" {
		cmd += " --tag " + ShellEscape(linkTag)
	}
	output, err := RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		return nil, fmt.Errorf("获取快照列表失败: %w, 输出: %s", err, sanitizeExecutorRuntimeOutput(output))
	}

	var snapshots []ResticSnapshot
	if err := json.Unmarshal([]byte(output), &snapshots); err != nil {
		return nil, fmt.Errorf("解析快照列表失败: %w", err)
	}
	return snapshots, nil
}

// ListFiles 列出 restic 快照中指定路径下的文件。
func (e *ResticExecutor) ListFiles(ctx context.Context, task model.Task, snapshotID string, path string) (result []ResticEntry, returnErr error) {
	repo := strings.TrimSpace(task.RsyncTarget)
	_, access, err := parseResticConfigWithRepositoryAccess(task.ExecutorConfig)
	if err != nil {
		return nil, err
	}

	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeSnapshot)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	// 生成唯一的密码临时文件路径，并在远程节点上创建
	pwFilePath := BuildResticPasswordFilePath()
	defer func() {
		if cleanupErr := CleanupResticPasswordFile(task.Node, pwFilePath, sshutil.PurposeSnapshot); cleanupErr != nil {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			} else {
				returnErr = cleanupErr
			}
		}
	}()
	if err := CreateResticPasswordFile(ctx, client, pwFilePath, access); err != nil {
		return nil, fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}

	cmdPrefix := e.buildCommandPrefix(task.Node, pwFilePath)
	lsPath := "/"
	if path != "" {
		lsPath = path
	}
	cmd := fmt.Sprintf("%s ls %s %s -r %s --json", cmdPrefix, ShellEscape(snapshotID), ShellEscape(lsPath), ShellEscape(repo))
	output, err := RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		return nil, fmt.Errorf("获取文件列表失败: %w, 输出: %s", err, sanitizeExecutorRuntimeOutput(output))
	}

	// restic ls 输出 NDJSON（每行一个 JSON 对象）
	var entries []ResticEntry
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry ResticEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // 跳过无法解析的行（如快照头信息）
		}
		if entry.Name != "" {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// RestoreFiles 从 restic 快照恢复指定文件到目标路径。
func (e *ResticExecutor) RestoreFiles(ctx context.Context, task model.Task, snapshotID string, includes []string, targetPath string) (returnErr error) {
	repo := strings.TrimSpace(task.RsyncTarget)
	_, access, err := parseResticConfigWithRepositoryAccess(task.ExecutorConfig)
	if err != nil {
		return err
	}

	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeSnapshot)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	// 生成唯一的密码临时文件路径，并在远程节点上创建
	pwFilePath := BuildResticPasswordFilePath()
	defer func() {
		if cleanupErr := CleanupResticPasswordFile(task.Node, pwFilePath, sshutil.PurposeSnapshot); cleanupErr != nil {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			} else {
				returnErr = cleanupErr
			}
		}
	}()
	if err := CreateResticPasswordFile(ctx, client, pwFilePath, access); err != nil {
		return fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}

	cmdPrefix := e.buildCommandPrefix(task.Node, pwFilePath)
	includeArgs := ""
	for _, inc := range includes {
		includeArgs += " --include " + ShellEscape(inc)
	}
	cmd := fmt.Sprintf("%s restore %s -r %s --target %s%s", cmdPrefix, ShellEscape(snapshotID), ShellEscape(repo), ShellEscape(targetPath), includeArgs)
	output, err := RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		return fmt.Errorf("恢复失败: %w, 输出: %s", err, sanitizeExecutorRuntimeOutput(output))
	}
	return nil
}

// RunSnapshotDiff executes the legacy Restic diff command behind a narrow
// port. Callers in managed mode must pass full IDs resolved by LineageSession.
func (e *ResticExecutor) RunSnapshotDiff(ctx context.Context, task model.Task, leftSnapshotID, rightSnapshotID string) (result string, returnErr error) {
	if !validResticSnapshotReference(leftSnapshotID) || !validResticSnapshotReference(rightSnapshotID) {
		return "", fmt.Errorf("invalid Restic snapshot reference")
	}
	repo := strings.TrimSpace(task.RsyncTarget)
	if repo == "" {
		return "", fmt.Errorf("restic 仓库路径为空")
	}
	_, access, err := parseResticConfigWithRepositoryAccess(task.ExecutorConfig)
	if err != nil {
		return "", fmt.Errorf("解析 restic 配置失败: %w", err)
	}
	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeSnapshotDiff)
	if err != nil {
		return "", fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck // best-effort remote close
	pwFilePath := BuildResticPasswordFilePath()
	defer func() {
		if cleanupErr := CleanupResticPasswordFile(task.Node, pwFilePath, sshutil.PurposeSnapshotDiff); cleanupErr != nil {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			} else {
				returnErr = cleanupErr
			}
		}
	}()
	if err := CreateResticPasswordFile(ctx, client, pwFilePath, access); err != nil {
		return "", fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}
	command := fmt.Sprintf("%s diff %s %s -r %s 2>&1", e.buildCommandPrefix(task.Node, pwFilePath), ShellEscape(leftSnapshotID), ShellEscape(rightSnapshotID), ShellEscape(repo))
	output, err := RunSSHCommandOutput(ctx, client, command)
	if err != nil {
		return "", fmt.Errorf("执行 restic diff 失败: %w", err)
	}
	return output, nil
}

func parseResticConfig(raw string) (ResticConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return ResticConfig{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return ResticConfig{}, err
	}
	if _, legacy := fields["append_only"]; legacy {
		return ResticConfig{}, fmt.Errorf("restic config requires repository_version migration")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ResticConfig{}, fmt.Errorf("restic config has trailing data")
		}
		return ResticConfig{}, err
	}
	var cfg ResticConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return ResticConfig{}, err
	}
	if cfg.RepositoryVersion != nil && *cfg.RepositoryVersion != 1 && *cfg.RepositoryVersion != 2 {
		return ResticConfig{}, fmt.Errorf("unsupported restic repository version")
	}
	return cfg, nil
}

// buildCommandPrefix 构造 restic 命令前缀（含 --password-file 和可选 sudo）。
func (e *ResticExecutor) buildCommandPrefix(node model.Node, passwordFilePath string) string {
	pwFileArg := BuildResticPasswordFileArg(passwordFilePath)
	bin := e.resticBinary()
	if NeedsSudo(node) {
		// sudo restic --password-file /tmp/xirang_restic_pw_XXXX ...
		return fmt.Sprintf("sudo %s %s", bin, pwFileArg)
	}
	return fmt.Sprintf("%s %s", bin, pwFileArg)
}

func buildResticExcludeArgs(patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, "--exclude "+ShellEscape(p))
		}
	}
	return strings.Join(parts, " ")
}

func validResticLinkTag(value string) bool {
	const prefix = "xirang.link.v1."
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validResticSnapshotReference(value string) bool {
	if len(value) < 4 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

var _ PublicationExecutor = (*ResticExecutor)(nil)
