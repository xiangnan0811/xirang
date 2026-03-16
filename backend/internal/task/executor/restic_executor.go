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

	cfg, err := parseResticConfig(task.ExecutorConfig)
	if err != nil {
		return -1, fmt.Errorf("解析 restic 配置失败: %w", err)
	}

	client, err := DialSSHForNode(ctx, task.Node)
	if err != nil {
		return -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	bin := e.resticBinary()

	// 检查 restic 是否安装
	if _, err := RunSSHCommandOutput(ctx, client, "which "+bin+" 2>/dev/null || command -v "+bin+" 2>/dev/null"); err != nil {
		return -1, fmt.Errorf("目标节点未安装 restic，请先在节点上安装")
	}

	envPrefix := buildResticEnvPrefix(cfg.RepositoryPassword)
	repoArg := shellEscape(repo)

	// 初始化仓库（若不存在）
	checkCmd := fmt.Sprintf("%s %s snapshots -r %s --json 2>&1", envPrefix, bin, repoArg)
	checkOut, _ := RunSSHCommandOutput(ctx, client, checkCmd)
	if strings.Contains(checkOut, "Is there a repository at the following location") ||
		strings.Contains(checkOut, "repository does not exist") ||
		strings.Contains(checkOut, "no such file or directory") {
		logf("info", fmt.Sprintf("初始化 restic 仓库: %s", repo))
		initCmd := fmt.Sprintf("%s %s init -r %s 2>&1", envPrefix, bin, repoArg)
		initOut, initErr := RunSSHCommandOutput(ctx, client, initCmd)
		if initErr != nil {
			return -1, fmt.Errorf("初始化 restic 仓库失败: %s", initOut)
		}
		logf("info", "restic 仓库初始化成功")
	}

	// 构造 backup 命令
	excludeArgs := buildResticExcludeArgs(cfg.ExcludePatterns)
	backupCmd := fmt.Sprintf("%s %s backup -r %s %s %s --json 2>&1",
		envPrefix, bin, repoArg, shellEscape(source), excludeArgs)

	logf("info", fmt.Sprintf("开始 restic 备份: %s → %s", source, repo))

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

	cfg, err := parseResticConfig(task.ExecutorConfig)
	if err != nil {
		return -1, fmt.Errorf("解析 restic 配置失败: %w", err)
	}

	client, err := DialSSHForNode(ctx, task.Node)
	if err != nil {
		return -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	bin := e.resticBinary()
	envPrefix := buildResticEnvPrefix(cfg.RepositoryPassword)
	repoArg := shellEscape(repo)

	restoreCmd := fmt.Sprintf("%s %s restore latest -r %s --target %s --json 2>&1",
		envPrefix, bin, repoArg, shellEscape(targetPath))

	logf("info", fmt.Sprintf("开始 restic 恢复: %s → %s", repo, targetPath))
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
	defer session.Close()

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
		logf("info", line)
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
	MessageType  string  `json:"message_type"`
	PercentDone  float64 `json:"percent_done"`
	TotalBytes   int64   `json:"total_bytes"`
	BytesDone    int64   `json:"bytes_done"`
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
	cfg, err := parseResticConfig(task.ExecutorConfig)
	if err != nil {
		return nil, err
	}

	client, err := DialSSHForNode(ctx, task.Node)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	envPrefix := buildResticEnvPrefix(cfg.RepositoryPassword)
	cmd := fmt.Sprintf("%s %s snapshots -r %s --json", envPrefix, e.resticBinary(), shellEscape(repo))
	output, err := RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		return nil, fmt.Errorf("获取快照列表失败: %w, 输出: %s", err, output)
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
	cfg, err := parseResticConfig(task.ExecutorConfig)
	if err != nil {
		return nil, err
	}

	client, err := DialSSHForNode(ctx, task.Node)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	envPrefix := buildResticEnvPrefix(cfg.RepositoryPassword)
	lsPath := "/"
	if path != "" {
		lsPath = path
	}
	cmd := fmt.Sprintf("%s %s ls %s %s -r %s --json", envPrefix, e.resticBinary(), shellEscape(snapshotID), shellEscape(lsPath), shellEscape(repo))
	output, err := RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		return nil, fmt.Errorf("获取文件列表失败: %w, 输出: %s", err, output)
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
	cfg, err := parseResticConfig(task.ExecutorConfig)
	if err != nil {
		return err
	}

	client, err := DialSSHForNode(ctx, task.Node)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	envPrefix := buildResticEnvPrefix(cfg.RepositoryPassword)
	includeArgs := ""
	for _, inc := range includes {
		includeArgs += " --include " + shellEscape(inc)
	}
	cmd := fmt.Sprintf("%s %s restore %s -r %s --target %s%s", envPrefix, e.resticBinary(), shellEscape(snapshotID), shellEscape(repo), shellEscape(targetPath), includeArgs)
	output, err := RunSSHCommandOutput(ctx, client, cmd)
	if err != nil {
		return fmt.Errorf("恢复失败: %w, 输出: %s", err, output)
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

func buildResticEnvPrefix(password string) string {
	if password == "" {
		return "RESTIC_PASSWORD=''"
	}
	return "RESTIC_PASSWORD=" + shellEscape(password)
}

func buildResticExcludeArgs(patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, "--exclude "+shellEscape(p))
		}
	}
	return strings.Join(parts, " ")
}

// DialSSHForNode 为节点建立 SSH 连接（节点的 SSHKey 应已通过 Preload 加载）。
func DialSSHForNode(ctx context.Context, node model.Node) (*ssh.Client, error) {
	port := node.Port
	if port == 0 {
		port = 22
	}
	user := strings.TrimSpace(node.Username)
	if user == "" {
		user = "root"
	}

	authType := strings.ToLower(strings.TrimSpace(node.AuthType))
	var authMethods []ssh.AuthMethod

	switch authType {
	case "key":
		keyContent, _, err := resolveNodePrivateKey(node)
		if err != nil {
			return nil, err
		}
		if keyContent == "" {
			return nil, fmt.Errorf("密钥认证未配置")
		}
		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			return nil, fmt.Errorf("私钥校验失败")
		}
		signer, err := ssh.ParsePrivateKey([]byte(normalizedKey))
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	case "password":
		if node.Password == "" {
			return nil, fmt.Errorf("密码认证未配置密码")
		}
		authMethods = append(authMethods, ssh.Password(node.Password))
	default:
		return nil, fmt.Errorf("不支持的认证方式: %s", authType)
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("主机密钥配置异常: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", node.Host, port)
	return sshutil.DialSSH(ctx, addr, user, authMethods, hostKeyCallback)
}

// RunSSHCommandOutput 通过 SSH 执行命令并返回合并的 stdout+stderr 输出。
func RunSSHCommandOutput(ctx context.Context, client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-done:
		}
	}()

	out, err := session.CombinedOutput(cmd)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return string(out), err
}
