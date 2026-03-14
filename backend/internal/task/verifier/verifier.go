package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// Result 备份完整性校验结果
type Result struct {
	Status          string // "passed" | "warning" | "failed"
	Message         string
	FileCountSrc    int
	FileCountDst    int
	TotalSizeSrc    int64
	TotalSizeDst    int64
	SampledFiles    int
	MismatchedFiles int
}

// Verify 执行备份完整性校验。
// 源端通过 SSH 在远程节点上执行命令，目标端为本地路径。
// SSH 连接在校验开始时建立，整个过程中复用。
func Verify(ctx context.Context, task model.Task, sampleRate int, db *gorm.DB, logf func(level, msg string), isRestore bool) Result {
	if task.RsyncSource == "" || task.RsyncTarget == "" {
		return Result{Status: "passed", Message: "无需校验：未配置同步路径"}
	}

	// restic/rclone 使用内建校验命令
	switch task.ExecutorType {
	case "restic":
		return VerifyRestic(ctx, task, db, logf)
	case "rclone":
		return VerifyRclone(ctx, task, db, logf)
	}

	if isRestore {
		return VerifyRemoteToRemote(ctx, task, sampleRate, db, logf)
	}

	// 备份模式：源端在远程，目标端在本地
	// 建立 SSH 连接，整个校验过程复用
	sshClient, err := dialSSHForTask(ctx, task, db)
	if err != nil {
		logf("warn", fmt.Sprintf("校验阶段建立 SSH 连接失败: %v", err))
		return Result{Status: "warning", Message: fmt.Sprintf("校验阶段建立 SSH 连接失败: %v", err)}
	}
	defer sshClient.Close()

	result := Result{Status: "passed"}

	// 源端文件计数（远程）
	srcCount, srcErr := remoteFileCount(ctx, sshClient, task.RsyncSource)
	if srcErr != nil {
		logf("warn", fmt.Sprintf("源端文件计数失败: %v", srcErr))
		result.Status = "warning"
		result.Message = fmt.Sprintf("源端文件计数失败: %v", srcErr)
		return result
	}
	result.FileCountSrc = srcCount

	// 目标端文件计数（本地）
	dstCount, dstErr := localFileCount(ctx, task.RsyncTarget)
	if dstErr != nil {
		logf("warn", fmt.Sprintf("目标端文件计数失败: %v", dstErr))
		result.Status = "warning"
		result.Message = fmt.Sprintf("目标端文件计数失败: %v", dstErr)
		return result
	}
	result.FileCountDst = dstCount

	// 比较文件数量
	if srcCount > 0 {
		diff := abs(srcCount - dstCount)
		ratio := float64(diff) / float64(srcCount)
		if ratio > 0.05 {
			result.Status = "warning"
			result.Message = fmt.Sprintf("文件数差异过大：源端 %d，目标端 %d（差异 %.1f%%）", srcCount, dstCount, ratio*100)
			logf("warn", result.Message)
			return result
		}
	}

	// 源端总大小（远程）
	srcSize, srcSizeErr := remoteDirectorySize(ctx, sshClient, task.RsyncSource)
	if srcSizeErr == nil {
		result.TotalSizeSrc = srcSize
	}

	// 目标端总大小（本地）
	dstSize, dstSizeErr := localDirectorySize(ctx, task.RsyncTarget)
	if dstSizeErr == nil {
		result.TotalSizeDst = dstSize
	}

	// 比较总大小
	if srcSizeErr == nil && dstSizeErr == nil && srcSize > 0 {
		diff := absInt64(srcSize - dstSize)
		ratio := float64(diff) / float64(srcSize)
		if ratio > 0.05 {
			result.Status = "warning"
			result.Message = fmt.Sprintf("总大小差异过大：源端 %d 字节，目标端 %d 字节（差异 %.1f%%）", srcSize, dstSize, ratio*100)
			logf("warn", result.Message)
			return result
		}
	}

	// 抽样校验文件内容
	if sampleRate > 0 && srcCount > 0 {
		sampled, mismatched, sampleErr := sampleChecksum(ctx, sshClient, task.RsyncSource, task.RsyncTarget, sampleRate, srcCount, logf)
		result.SampledFiles = sampled
		result.MismatchedFiles = mismatched
		if sampleErr != nil {
			result.Status = "warning"
			result.Message = fmt.Sprintf("抽样校验失败: %v", sampleErr)
			return result
		}
		if mismatched > 0 {
			result.Status = "warning"
			result.Message = fmt.Sprintf("抽样校验发现 %d/%d 个文件不一致", mismatched, sampled)
			return result
		}
	}

	if result.Status == "passed" {
		result.Message = fmt.Sprintf("校验通过：源端 %d 文件，目标端 %d 文件", srcCount, dstCount)
	}
	return result
}

// VerifyRemoteToRemote 执行恢复操作的完整性校验。
// 恢复模式下，source 和 target 都在远程节点上，需要在远程执行所有校验命令。
func VerifyRemoteToRemote(ctx context.Context, task model.Task, sampleRate int, db *gorm.DB, logf func(level, msg string)) Result {
	// 建立 SSH 连接
	sshClient, err := dialSSHForTask(ctx, task, db)
	if err != nil {
		logf("warn", fmt.Sprintf("恢复校验阶段建立 SSH 连接失败: %v", err))
		return Result{Status: "warning", Message: fmt.Sprintf("恢复校验阶段建立 SSH 连接失败: %v", err)}
	}
	defer sshClient.Close()

	result := Result{Status: "passed"}

	// 源端文件计数（远程）
	srcCount, srcErr := remoteFileCount(ctx, sshClient, task.RsyncSource)
	if srcErr != nil {
		logf("warn", fmt.Sprintf("源端（备份）文件计数失败: %v", srcErr))
		result.Status = "warning"
		result.Message = fmt.Sprintf("源端（备份）文件计数失败: %v", srcErr)
		return result
	}
	result.FileCountSrc = srcCount

	// 目标端文件计数（也是远程）
	dstCount, dstErr := remoteFileCount(ctx, sshClient, task.RsyncTarget)
	if dstErr != nil {
		logf("warn", fmt.Sprintf("目标端（恢复路径）文件计数失败: %v", dstErr))
		result.Status = "warning"
		result.Message = fmt.Sprintf("目标端（恢复路径）文件计数失败: %v", dstErr)
		return result
	}
	result.FileCountDst = dstCount

	// 比较文件数量
	if srcCount > 0 {
		diff := abs(srcCount - dstCount)
		ratio := float64(diff) / float64(srcCount)
		if ratio > 0.05 {
			result.Status = "warning"
			result.Message = fmt.Sprintf("文件数差异过大：备份 %d，恢复后 %d（差异 %.1f%%）", srcCount, dstCount, ratio*100)
			logf("warn", result.Message)
			return result
		}
	}

	// 源端总大小（远程）
	srcSize, srcSizeErr := remoteDirectorySize(ctx, sshClient, task.RsyncSource)
	if srcSizeErr == nil {
		result.TotalSizeSrc = srcSize
	}

	// 目标端总大小（也是远程）
	dstSize, dstSizeErr := remoteDirectorySize(ctx, sshClient, task.RsyncTarget)
	if dstSizeErr == nil {
		result.TotalSizeDst = dstSize
	}

	// 比较总大小
	if srcSizeErr == nil && dstSizeErr == nil && srcSize > 0 {
		diff := absInt64(srcSize - dstSize)
		ratio := float64(diff) / float64(srcSize)
		if ratio > 0.05 {
			result.Status = "warning"
			result.Message = fmt.Sprintf("总大小差异过大：备份 %d 字节，恢复后 %d 字节（差异 %.1f%%）", srcSize, dstSize, ratio*100)
			logf("warn", result.Message)
			return result
		}
	}

	// 抽样校验文件内容（两个路径都在远程）
	if sampleRate > 0 && srcCount > 0 {
		sampled, mismatched, sampleErr := sampleChecksumRemote(ctx, sshClient, task.RsyncSource, task.RsyncTarget, sampleRate, srcCount, logf)
		result.SampledFiles = sampled
		result.MismatchedFiles = mismatched
		if sampleErr != nil {
			result.Status = "warning"
			result.Message = fmt.Sprintf("抽样校验失败: %v", sampleErr)
			return result
		}
		if mismatched > 0 {
			result.Status = "warning"
			result.Message = fmt.Sprintf("抽样校验发现 %d/%d 个文件不一致", mismatched, sampled)
			return result
		}
	}

	if result.Status == "passed" {
		result.Message = fmt.Sprintf("恢复校验通过：备份 %d 文件，恢复后 %d 文件", srcCount, dstCount)
	}
	return result
}

// dialSSHForTask 为任务建立 SSH 连接（复用 sshutil 工具函数）。
// task.Node 和 task.Node.SSHKey 应已通过 Preload 加载。
func dialSSHForTask(ctx context.Context, task model.Task, db *gorm.DB) (*ssh.Client, error) {
	if task.Node.ID == 0 {
		return nil, fmt.Errorf("任务未关联节点")
	}

	authMethods, err := sshutil.BuildSSHAuth(task.Node, db)
	if err != nil {
		return nil, fmt.Errorf("构建 SSH 认证失败: %w", err)
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("解析主机密钥回调失败: %w", err)
	}

	address := fmt.Sprintf("%s:%d", task.Node.Host, task.Node.Port)
	return sshutil.DialSSH(ctx, address, task.Node.Username, authMethods, hostKeyCallback)
}

// runRemoteCommand 通过已建立的 SSH 连接执行远程命令
func runRemoteCommand(ctx context.Context, sshClient *ssh.Client, command string) (string, error) {
	session, err := sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	// context 取消时关闭 session，使正在执行的远程命令立即中断
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-done:
		}
	}()

	output, err := session.Output(command)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func remoteFileCount(ctx context.Context, sshClient *ssh.Client, path string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	output, err := runRemoteCommand(ctx, sshClient, fmt.Sprintf("find %s -type f 2>/dev/null | wc -l", shellQuote(path)))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(output))
}

func localFileCount(ctx context.Context, path string) (int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("find %s -type f 2>/dev/null | wc -l", shellQuote(path)))
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(output)))
}

func remoteDirectorySize(ctx context.Context, sshClient *ssh.Client, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	output, err := runRemoteCommand(ctx, sshClient, fmt.Sprintf("du -sb %s 2>/dev/null | awk '{print $1}'", shellQuote(path)))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(output), 10, 64)
}

func localDirectorySize(ctx context.Context, path string) (int64, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("du -sb %s 2>/dev/null | awk '{print $1}'", shellQuote(path)))
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
}

func sampleChecksum(ctx context.Context, sshClient *ssh.Client, srcPath, dstPath string, sampleRate, totalFiles int, logf func(level, msg string)) (int, int, error) {
	// 获取源端文件列表
	output, err := runRemoteCommand(ctx, sshClient, fmt.Sprintf("find %s -type f 2>/dev/null", shellQuote(srcPath)))
	if err != nil {
		return 0, 0, fmt.Errorf("获取源端文件列表失败: %w", err)
	}

	files := strings.Split(strings.TrimSpace(output), "\n")
	if len(files) == 0 || (len(files) == 1 && files[0] == "") {
		return 0, 0, nil
	}

	// 计算抽样数量
	sampleCount := len(files) * sampleRate / 100
	if sampleCount < 1 {
		sampleCount = 1
	}
	if sampleCount > 100 {
		sampleCount = 100
	}
	if sampleCount > len(files) {
		sampleCount = len(files)
	}

	// 随机抽样
	perm := rand.Perm(len(files))
	sampled := 0
	mismatched := 0

	for i := 0; i < sampleCount; i++ {
		// 检查 context 是否已取消
		select {
		case <-ctx.Done():
			return sampled, mismatched, ctx.Err()
		default:
		}

		file := files[perm[i]]
		relPath := strings.TrimPrefix(file, srcPath)
		relPath = strings.TrimPrefix(relPath, "/")
		if relPath == "" {
			continue
		}

		// 远程 checksum
		srcChecksum, srcErr := runRemoteCommand(ctx, sshClient, fmt.Sprintf("sha256sum %s 2>/dev/null | awk '{print $1}'", shellQuote(file)))
		if srcErr != nil {
			continue
		}

		// 本地 checksum
		dstFile := filepath.Join(dstPath, relPath)
		if !strings.HasPrefix(filepath.Clean(dstFile), filepath.Clean(dstPath)) {
			continue // skip path traversal attempts
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("sha256sum %s 2>/dev/null | awk '{print $1}'", shellQuote(dstFile)))
		dstOutput, dstErr := cmd.Output()
		if dstErr != nil {
			mismatched++
			sampled++
			continue
		}

		sampled++
		if strings.TrimSpace(srcChecksum) != strings.TrimSpace(string(dstOutput)) {
			mismatched++
			logf("warn", fmt.Sprintf("文件校验不一致: %s", relPath))
		}
	}

	return sampled, mismatched, nil
}

// sampleChecksumRemote 对远程节点上的两个路径进行抽样校验（恢复模式）。
// 与 sampleChecksum 不同，这里 source 和 target 都在远程节点上。
func sampleChecksumRemote(ctx context.Context, sshClient *ssh.Client, srcPath, dstPath string, sampleRate, totalFiles int, logf func(level, msg string)) (int, int, error) {
	// 获取源端文件列表（远程）
	output, err := runRemoteCommand(ctx, sshClient, fmt.Sprintf("find %s -type f 2>/dev/null", shellQuote(srcPath)))
	if err != nil {
		return 0, 0, fmt.Errorf("获取源端文件列表失败: %w", err)
	}

	files := strings.Split(strings.TrimSpace(output), "\n")
	if len(files) == 0 || (len(files) == 1 && files[0] == "") {
		return 0, 0, nil
	}

	// 计算抽样数量
	sampleCount := len(files) * sampleRate / 100
	if sampleCount < 1 {
		sampleCount = 1
	}
	if sampleCount > 100 {
		sampleCount = 100
	}
	if sampleCount > len(files) {
		sampleCount = len(files)
	}

	// 随机抽样
	perm := rand.Perm(len(files))
	sampled := 0
	mismatched := 0

	for i := 0; i < sampleCount; i++ {
		// 检查 context 是否已取消
		select {
		case <-ctx.Done():
			return sampled, mismatched, ctx.Err()
		default:
		}

		file := files[perm[i]]
		relPath := strings.TrimPrefix(file, srcPath)
		relPath = strings.TrimPrefix(relPath, "/")
		if relPath == "" {
			continue
		}

		// 源端 checksum（远程）
		srcChecksum, srcErr := runRemoteCommand(ctx, sshClient, fmt.Sprintf("sha256sum %s 2>/dev/null | awk '{print $1}'", shellQuote(file)))
		if srcErr != nil {
			continue
		}

		// 目标端 checksum（也是远程）
		dstFile := filepath.Join(dstPath, relPath)
		dstChecksum, dstErr := runRemoteCommand(ctx, sshClient, fmt.Sprintf("sha256sum %s 2>/dev/null | awk '{print $1}'", shellQuote(dstFile)))
		if dstErr != nil {
			mismatched++
			sampled++
			continue
		}

		sampled++
		if strings.TrimSpace(srcChecksum) != strings.TrimSpace(dstChecksum) {
			mismatched++
			logf("warn", fmt.Sprintf("文件校验不一致: %s", relPath))
		}
	}

	return sampled, mismatched, nil
}

// VerifyRestic 通过 SSH 在远程节点上执行 restic check 校验仓库完整性。
func VerifyRestic(ctx context.Context, task model.Task, db *gorm.DB, logf func(level, msg string)) Result {
	sshClient, err := dialSSHForTask(ctx, task, db)
	if err != nil {
		logf("warn", fmt.Sprintf("restic 校验阶段建立 SSH 连接失败: %v", err))
		return Result{Status: "warning", Message: fmt.Sprintf("校验阶段建立 SSH 连接失败: %v", err)}
	}
	defer sshClient.Close()

	repo := task.RsyncTarget // 备份时 RsyncTarget = 仓库路径
	if repo == "" {
		return Result{Status: "passed", Message: "无需校验：未配置仓库路径"}
	}

	// 从 executor_config 中解析密码
	password := extractResticPassword(task.ExecutorConfig)
	envPrefix := "RESTIC_PASSWORD=" + shellQuote(password)
	checkCmd := fmt.Sprintf("%s restic check -r %s 2>&1", envPrefix, shellQuote(repo))

	logf("info", fmt.Sprintf("执行 restic check: %s", repo))
	output, err := runRemoteCommand(ctx, sshClient, checkCmd)
	if err != nil {
		msg := fmt.Sprintf("restic check 失败: %v\n%s", err, strings.TrimSpace(output))
		logf("warn", msg)
		return Result{Status: "warning", Message: msg}
	}
	logf("info", strings.TrimSpace(output))
	return Result{Status: "passed", Message: "restic check 通过"}
}

// VerifyRclone 通过 SSH 在远程节点上执行 rclone check 校验源与目标一致性。
func VerifyRclone(ctx context.Context, task model.Task, db *gorm.DB, logf func(level, msg string)) Result {
	sshClient, err := dialSSHForTask(ctx, task, db)
	if err != nil {
		logf("warn", fmt.Sprintf("rclone 校验阶段建立 SSH 连接失败: %v", err))
		return Result{Status: "warning", Message: fmt.Sprintf("校验阶段建立 SSH 连接失败: %v", err)}
	}
	defer sshClient.Close()

	source := task.RsyncSource
	remote := task.RsyncTarget
	if source == "" || remote == "" {
		return Result{Status: "passed", Message: "无需校验：未配置同步路径"}
	}

	checkCmd := fmt.Sprintf("rclone check %s %s 2>&1", shellQuote(source), shellQuote(remote))
	logf("info", fmt.Sprintf("执行 rclone check: %s <-> %s", source, remote))
	output, err := runRemoteCommand(ctx, sshClient, checkCmd)
	if err != nil {
		msg := fmt.Sprintf("rclone check 失败: %v\n%s", err, strings.TrimSpace(output))
		logf("warn", msg)
		return Result{Status: "warning", Message: msg}
	}
	logf("info", strings.TrimSpace(output))
	return Result{Status: "passed", Message: "rclone check 通过"}
}

// extractResticPassword 从 executor_config JSON 中提取 repository_password。
func extractResticPassword(executorConfig string) string {
	if strings.TrimSpace(executorConfig) == "" {
		return ""
	}
	var cfg struct {
		RepositoryPassword string `json:"repository_password"`
	}
	if err := json.Unmarshal([]byte(executorConfig), &cfg); err != nil {
		return ""
	}
	return cfg.RepositoryPassword
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func absInt64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
