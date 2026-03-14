package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"golang.org/x/crypto/ssh"
)

// RcloneConfig 是 rclone 执行器的配置（存储在 Task.ExecutorConfig JSON 中）。
type RcloneConfig struct {
	BandwidthLimit string `json:"bandwidth_limit,omitempty"` // 如 "10M"
	Transfers      int    `json:"transfers,omitempty"`       // 并发传输数，默认 4
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
	source := strings.TrimSpace(task.RsyncSource)
	remote := strings.TrimSpace(task.RsyncTarget)
	if source == "" || remote == "" {
		return -1, fmt.Errorf("rclone 同步任务缺少源路径或目标 remote")
	}

	cfg, err := parseRcloneConfig(task.ExecutorConfig)
	if err != nil {
		return -1, fmt.Errorf("解析 rclone 配置失败: %w", err)
	}

	client, err := dialSSHForNode(ctx, task.Node)
	if err != nil {
		return -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	bin := e.rcloneBinary()

	// 检查 rclone 是否安装
	if _, err := runSSHCommandOutput(ctx, client, "which "+bin+" 2>/dev/null || command -v "+bin+" 2>/dev/null"); err != nil {
		return -1, fmt.Errorf("目标节点未安装 rclone，请先在节点上安装")
	}

	syncCmd := buildRcloneSyncCmd(bin, source, remote, cfg, false)
	logf("info", fmt.Sprintf("开始 rclone 同步: %s → %s", source, remote))

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
	remote := strings.TrimSpace(task.RsyncSource)
	targetPath := strings.TrimSpace(task.RsyncTarget)
	if remote == "" || targetPath == "" {
		return -1, fmt.Errorf("rclone 恢复任务缺少 remote 或目标路径")
	}

	cfg, err := parseRcloneConfig(task.ExecutorConfig)
	if err != nil {
		return -1, fmt.Errorf("解析 rclone 配置失败: %w", err)
	}

	client, err := dialSSHForNode(ctx, task.Node)
	if err != nil {
		return -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	bin := e.rcloneBinary()
	syncCmd := buildRcloneSyncCmd(bin, remote, targetPath, cfg, true)
	logf("info", fmt.Sprintf("开始 rclone 恢复: %s → %s", remote, targetPath))

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

// streamSSHCommand 通过 SSH 流式执行 rclone 命令，解析进度。
func (e *RcloneExecutor) streamSSHCommand(ctx context.Context, client *ssh.Client, cmd string, logf LogFunc, progressf ProgressFunc) (int, error) {
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

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		logf("info", line)
		if progressf != nil {
			if sample, ok := parseRcloneProgressLine(line); ok {
				progressf(sample)
			}
		}
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

func buildRcloneSyncCmd(bin, source, dest string, cfg RcloneConfig, isRestore bool) string {
	args := []string{bin, "sync", shellEscape(source), shellEscape(dest),
		"--stats", "1s", "--stats-one-line", "-v"}
	if cfg.BandwidthLimit != "" {
		args = append(args, "--bwlimit", shellEscape(cfg.BandwidthLimit))
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
