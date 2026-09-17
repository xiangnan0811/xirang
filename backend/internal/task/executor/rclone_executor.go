package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"golang.org/x/crypto/ssh"
)

// RcloneConfig 是 rclone 执行器的配置（存储在 Task.ExecutorConfig JSON 中）。
type RcloneConfig struct {
	Version         int                             `json:"version,omitempty"`
	PublicationMode backupasset.TaskPublicationMode `json:"publication_mode,omitempty"`
	BandwidthLimit  string                          `json:"bandwidth_limit,omitempty"` // 如 "10M"
	Transfers       int                             `json:"transfers,omitempty"`       // 并发传输数，默认 4
}

// RcloneExecutor 通过 SSH 在远程节点上执行 rclone 同步/恢复操作。
// rclone 必须在目标节点上预先安装，且节点上已配置 rclone remote（agentless 原则）。
// 配置字段语义：
//   - task.RsyncSource = 备份源路径（节点本地路径，如 /data/app）
//   - task.RsyncTarget = rclone remote 目标（如 s3:mybucket/backup）
//   - task.ExecutorConfig = JSON，含 bandwidth_limit 和 transfers
type RcloneExecutor struct {
	binary string // rclone 二进制名称，默认 "rclone"
}

// rcloneStatsPattern 匹配 rclone --stats-one-line 输出中的传输速率。
// 示例: "Transferred:   1.234 GiB / 2.345 GiB, 52%, 10.234 MiB/s, ETA 1m23s"
var rcloneStatsPattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(k|m|g|t)(?:i?b)/s`)

func (e *RcloneExecutor) rcloneBinary() string {
	if e.binary != "" {
		return e.binary
	}
	return util.GetEnvOrDefault("RCLONE_BINARY", "rclone")
}

func (e *RcloneExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	cfg, err := parseRcloneConfig(task.ExecutorConfig)
	if err != nil {
		return -1, markNoProcessStart(fmt.Errorf("解析 rclone 配置失败: %w", err))
	}
	if err := rejectManagedRcloneLegacyExecution(cfg); err != nil {
		return -1, markNoProcessStart(err)
	}
	source := strings.TrimSpace(task.RsyncSource)
	remote := strings.TrimSpace(task.RsyncTarget)
	if source == "" || remote == "" {
		return -1, markNoProcessStart(fmt.Errorf("rclone 同步任务缺少源路径或目标 remote"))
	}

	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeTaskBackup)
	if err != nil {
		return -1, markNoProcessStart(fmt.Errorf("SSH 连接失败: %w", err))
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	bin := e.rcloneBinary()

	// 检查 rclone 是否安装
	if _, err := RunSSHCommandOutput(ctx, client, "which "+bin+" 2>/dev/null || command -v "+bin+" 2>/dev/null"); err != nil {
		return -1, markNoProcessStart(fmt.Errorf("目标节点未安装 rclone，请先在节点上安装"))
	}

	syncCmd := buildRcloneSyncCmd(bin, source, remote, cfg, false, NeedsSudo(task.Node))
	logf("info", "开始 rclone 同步")

	exitCode, runErr := e.streamSSHCommand(ctx, client, syncCmd, logf, progressf)
	if runErr != nil {
		return exitCode, fmt.Errorf("rclone 同步执行失败: %w", runErr)
	}
	if exitCode != 0 {
		return exitCode, fmt.Errorf("rclone 同步退出码: %d", exitCode)
	}
	logf("info", "rclone 同步完成")
	return 0, nil
}

// RunRestore 在远程节点上执行 rclone 反向同步（恢复）操作。
// restoreTask.RsyncSource = rclone remote（原任务的 RsyncTarget）
// restoreTask.RsyncTarget = 恢复目标路径
func (e *RcloneExecutor) RunRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	cfg, err := parseRcloneConfig(task.ExecutorConfig)
	if err != nil {
		return -1, markNoProcessStart(fmt.Errorf("解析 rclone 配置失败: %w", err))
	}
	if err := rejectManagedRcloneLegacyExecution(cfg); err != nil {
		return -1, markNoProcessStart(err)
	}
	remote := strings.TrimSpace(task.RsyncSource)
	targetPath := strings.TrimSpace(task.RsyncTarget)
	if remote == "" || targetPath == "" {
		return -1, markNoProcessStart(fmt.Errorf("rclone 恢复任务缺少 remote 或目标路径"))
	}

	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeTaskRestore)
	if err != nil {
		return -1, markNoProcessStart(fmt.Errorf("SSH 连接失败: %w", err))
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	bin := e.rcloneBinary()
	syncCmd := buildRcloneSyncCmd(bin, remote, targetPath, cfg, true, NeedsSudo(task.Node))
	logf("info", "开始 rclone 恢复")

	exitCode, runErr := e.streamSSHCommand(ctx, client, syncCmd, logf, progressf)
	if runErr != nil {
		return exitCode, fmt.Errorf("rclone 恢复执行失败: %w", runErr)
	}
	if exitCode != 0 {
		return exitCode, fmt.Errorf("rclone 恢复退出码: %d", exitCode)
	}
	logf("info", "rclone 恢复完成")
	return 0, nil
}

// streamSSHCommand 通过 SSH 流式执行 rclone 命令，解析进度。Raw command
// execution is retained for the compatibility shell redirection, while the
// shared SSH runner owns cancellation and session lifecycle.
func (e *RcloneExecutor) streamSSHCommand(ctx context.Context, client *ssh.Client, cmd string, logf LogFunc, progressf ProgressFunc) (int, error) {
	runner := sshutil.NewSSHCommandRunnerWithTransportClose(client, 1)
	stream, err := runner.OpenRawExecution(ctx, sshutil.RawCommandSpec{
		Command: cmd,
		// The caller controls lifetime; do not impose a shorter backup limit.
		// Stream parsing bounds each record and stderr remains capped.
		MaxStdoutBytes: 0,
		MaxStderrBytes: 64 << 10,
		MaxRecordBytes: 1 << 20,
	})
	if err != nil {
		if errors.Is(err, sshutil.ErrCommandStart) {
			return -1, markRemoteExecutionUnknown(err)
		}
		return -1, markNoProcessStart(err)
	}

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		logf("info", sanitizeExecutorRuntimeEvidence(line))
		if progressf != nil {
			if sample, ok := parseRcloneProgressLine(line); ok {
				progressf(sample)
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		cancelErr := stream.Cancel()
		if cancelErr != nil && !errors.Is(cancelErr, sshutil.ErrCommandFailed) {
			scanErr = errors.Join(scanErr, cancelErr)
		}
		return -1, markRemoteExecutionUnknown(scanErr)
	}

	completion, err := stream.Join()
	if err != nil {
		return -1, markRemoteExecutionUnknown(err)
	}
	if !completion.ExitCodeKnown {
		return -1, markRemoteExecutionUnknown(fmt.Errorf("remote command completed without an exit status"))
	}
	return completion.ExitCode, nil
}

func parseRcloneProgressLine(line string) (ProgressSample, bool) {
	matches := rcloneStatsPattern.FindStringSubmatch(line)
	if len(matches) < 3 {
		return ProgressSample{}, false
	}
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return ProgressSample{}, false
	}
	multiplier := 1.0
	switch strings.ToLower(matches[2]) {
	case "k":
		multiplier = 1_000
	case "m":
		multiplier = 1_000_000
	case "g":
		multiplier = 1_000_000_000
	case "t":
		multiplier = 1_000_000_000_000
	}
	throughputMbps := value * multiplier * 8 / 1_000_000
	return ProgressSample{
		ObservedAt:     time.Now().UTC(),
		ThroughputMbps: throughputMbps,
	}, true
}

func buildRcloneSyncCmd(bin, source, dest string, cfg RcloneConfig, isRestore bool, useSudo bool) string {
	prefix := bin
	if useSudo {
		prefix = "sudo " + bin
	}
	args := []string{prefix, "sync", ShellEscape(source), ShellEscape(dest),
		"--stats", "1s", "--stats-one-line", "-v"}
	if cfg.BandwidthLimit != "" {
		args = append(args, "--bwlimit", ShellEscape(cfg.BandwidthLimit))
	}
	if cfg.Transfers > 0 {
		args = append(args, "--transfers", strconv.Itoa(cfg.Transfers))
	}
	args = append(args, "2>&1")
	_ = isRestore // restore uses the same sync direction (source/dest already swapped by caller)
	return strings.Join(args, " ")
}

func parseRcloneConfig(raw string) (RcloneConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return RcloneConfig{}, nil
	}
	var cfg RcloneConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return RcloneConfig{}, err
	}
	return cfg, nil
}

func rejectManagedRcloneLegacyExecution(config RcloneConfig) error {
	if config.PublicationMode == "" || config.PublicationMode == backupasset.PublicationLegacyMutable {
		return nil
	}
	return fmt.Errorf("%w: managed Rclone mode cannot use the legacy executor", backupasset.ErrForbidden)
}
