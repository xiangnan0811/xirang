package verifier

import (
	"context"
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
func Verify(ctx context.Context, task model.Task, sampleRate int, db *gorm.DB, logf func(level, msg string)) Result {
	if task.RsyncSource == "" || task.RsyncTarget == "" {
		return Result{Status: "passed", Message: "无需校验：未配置同步路径"}
	}

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
func runRemoteCommand(sshClient *ssh.Client, command string) (string, error) {
	session, err := sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	output, err := session.Output(command)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func remoteFileCount(ctx context.Context, sshClient *ssh.Client, path string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	output, err := runRemoteCommand(sshClient, fmt.Sprintf("find %s -type f 2>/dev/null | wc -l", shellQuote(path)))
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
	output, err := runRemoteCommand(sshClient, fmt.Sprintf("du -sb %s 2>/dev/null | awk '{print $1}'", shellQuote(path)))
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
	output, err := runRemoteCommand(sshClient, fmt.Sprintf("find %s -type f 2>/dev/null", shellQuote(srcPath)))
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
		srcChecksum, srcErr := runRemoteCommand(sshClient, fmt.Sprintf("sha256sum %s 2>/dev/null | awk '{print $1}'", shellQuote(file)))
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
