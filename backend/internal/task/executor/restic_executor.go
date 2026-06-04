package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"golang.org/x/crypto/ssh"
)

// ResticConfig 是 restic 执行器的配置（存储在 Task.ExecutorConfig JSON 中）。
type ResticConfig struct {
	RepositoryPassword string   `json:"repository_password,omitempty"`
	ExcludePatterns    []string `json:"exclude_patterns,omitempty"`
	AppendOnly         bool     `json:"append_only,omitempty"`
}

// ResticExecutor 通过 SSH 在远程节点上执行 restic 备份/恢复操作。
// restic 必须在目标节点上预先安装（agentless 原则）。
// 配置字段语义：
//   - task.RsyncSource = 备份源路径（节点本地路径，如 /data/app）
//   - task.RsyncTarget = restic 仓库路径（如 /backup/repo 或 sftp:user@host:/backup）
//   - task.ExecutorConfig = JSON，含 repository_password 和 exclude_patterns
type ResticExecutor struct {
	binary string // restic 二进制名称，默认 "restic"
}

func (e *ResticExecutor) resticBinary() string {
	if e.binary != "" {
		return e.binary
	}
	return util.GetEnvOrDefault("RESTIC_BINARY", "restic")
}

func (e *ResticExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
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
	createPwCmd := BuildCreateResticPasswordFileCmd(pwFilePath, access)
	if _, err := RunSSHCommandOutput(ctx, client, createPwCmd); err != nil {
		return -1, fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}
	// 确保函数退出时清理远程节点上的密码临时文件
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupCmd := BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = RunSSHCommandOutput(cleanupCtx, client, cleanupCmd)
	}()

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
		if cfg.AppendOnly {
			initFlags = " --repository-version 2"
		}
		initCmd := fmt.Sprintf("%s init%s -r %s 2>&1", cmdPrefix, initFlags, repoArg)
		initOut, initErr := RunSSHCommandOutput(ctx, client, initCmd)
		if initErr != nil {
			return -1, fmt.Errorf("初始化 restic 仓库失败: %s", sanitizeExecutorRuntimeEvidence(initOut))
		}
		logf("info", "restic 仓库初始化成功")
	}

	// 若配置了 append_only，检查仓库版本是否符合要求
	if cfg.AppendOnly {
		catCmd := fmt.Sprintf("%s cat config -r %s 2>&1", cmdPrefix, repoArg)
		catOut, catErr := RunSSHCommandOutput(ctx, client, catCmd)
		if catErr == nil {
			var repoConfig struct {
				Version uint `json:"version"`
			}
			if err := json.Unmarshal([]byte(catOut), &repoConfig); err == nil && repoConfig.Version < 2 {
				logf("warn", fmt.Sprintf(
					"append_only=true 但仓库版本为 %d（需要版本 2），备份继续但不受 append-only 保护。请重建仓库以启用不可变保护。",
					repoConfig.Version,
				))
			}
		}
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
func (e *ResticExecutor) RunRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
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
	createPwCmd := BuildCreateResticPasswordFileCmd(pwFilePath, access)
	if _, err := RunSSHCommandOutput(ctx, client, createPwCmd); err != nil {
		return -1, fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupCmd := BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = RunSSHCommandOutput(cleanupCtx, client, cleanupCmd)
	}()

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
	session, err := client.NewSession()
	if err != nil {
		return -1, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	stdout, err := session.StdoutPipe()
	if err != nil {
		return -1, err
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGTERM)
		case <-done:
		}
	}()

	if err := session.Start(cmd); err != nil {
		return -1, fmt.Errorf("启动远程命令失败: %w", err)
	}

	var lastBytesDone int64
	var lastObservedAt time.Time

	scanner := bufio.NewScanner(stdout)
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
		logf("info", sanitizeExecutorRuntimeEvidence(line))
	}

	waitErr := session.Wait()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*ssh.ExitError); ok {
			return exitErr.ExitStatus(), waitErr
		}
		return -1, waitErr
	}
	return 0, nil
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
	createPwCmd := BuildCreateResticPasswordFileCmd(pwFilePath, access)
	if _, err := RunSSHCommandOutput(ctx, client, createPwCmd); err != nil {
		return nil, fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupCmd := BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = RunSSHCommandOutput(cleanupCtx, client, cleanupCmd)
	}()

	cmdPrefix := e.buildCommandPrefix(task.Node, pwFilePath)
	cmd := fmt.Sprintf("%s snapshots -r %s --json", cmdPrefix, ShellEscape(repo))
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
func (e *ResticExecutor) ListFiles(ctx context.Context, task model.Task, snapshotID string, path string) ([]ResticEntry, error) {
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
	createPwCmd := BuildCreateResticPasswordFileCmd(pwFilePath, access)
	if _, err := RunSSHCommandOutput(ctx, client, createPwCmd); err != nil {
		return nil, fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupCmd := BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = RunSSHCommandOutput(cleanupCtx, client, cleanupCmd)
	}()

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
func (e *ResticExecutor) RestoreFiles(ctx context.Context, task model.Task, snapshotID string, includes []string, targetPath string) error {
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
	createPwCmd := BuildCreateResticPasswordFileCmd(pwFilePath, access)
	if _, err := RunSSHCommandOutput(ctx, client, createPwCmd); err != nil {
		return fmt.Errorf("创建 restic 密码临时文件失败: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupCmd := BuildCleanupResticPasswordFileCmd(pwFilePath)
		_, _ = RunSSHCommandOutput(cleanupCtx, client, cleanupCmd)
	}()

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

func parseResticConfig(raw string) (ResticConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return ResticConfig{}, nil
	}
	var cfg ResticConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return ResticConfig{}, err
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
