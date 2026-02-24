package executor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"
)

type LogFunc func(level, message string)

type Executor interface {
	Run(ctx context.Context, task model.Task, logf LogFunc) (int, error)
}

type Factory interface {
	Resolve(executorType string) Executor
}

type factory struct {
	rsync Executor
}

func NewFactory(rsyncBinary string) Factory {
	return &factory{
		rsync: &RsyncExecutor{binary: rsyncBinary},
	}
}

func (f *factory) Resolve(executorType string) Executor {
	normalized := strings.ToLower(strings.TrimSpace(executorType))
	switch normalized {
	case "rsync":
		return f.rsync
	default:
		return &DisabledExecutor{executorType: normalized}
	}
}

type DisabledExecutor struct {
	executorType string
}

func (e *DisabledExecutor) Run(_ context.Context, _ model.Task, _ LogFunc) (int, error) {
	if e.executorType == "" || e.executorType == "local" {
		return -1, fmt.Errorf("local 执行器已禁用")
	}
	return -1, fmt.Errorf("不支持的 executor_type: %s", e.executorType)
}

type RsyncExecutor struct {
	binary string
}

func (e *RsyncExecutor) Run(ctx context.Context, task model.Task, logf LogFunc) (int, error) {
	if strings.TrimSpace(task.RsyncSource) == "" || strings.TrimSpace(task.RsyncTarget) == "" {
		return -1, fmt.Errorf("rsync 执行需要 rsync_source 与 rsync_target")
	}

	if !util.IsRemotePathSpec(task.RsyncTarget) {
		if err := ensureLocalTargetReady(task.RsyncTarget); err != nil {
			return -1, err
		}
	}

	args := []string{"-avz"}
	source := task.RsyncSource

	cleanup := func() {}
	if strings.TrimSpace(task.Node.Host) != "" {
		port := task.Node.Port
		if port == 0 {
			port = 22
		}
		user := strings.TrimSpace(task.Node.Username)
		if user == "" {
			user = "root"
		}

		authType := strings.ToLower(strings.TrimSpace(task.Node.AuthType))
		if authType == "password" {
			return -1, fmt.Errorf("rsync 远程执行暂不支持密码认证，请为节点配置 SSH key")
		}
		if authType != "key" {
			return -1, fmt.Errorf("不支持的认证模式: %s", authType)
		}

		keyContent, keySource, keyResolveErr := resolveNodePrivateKey(task.Node)
		if keyResolveErr != nil {
			return -1, keyResolveErr
		}
		if keyContent == "" {
			return -1, fmt.Errorf("密钥认证未配置 private_key 或 ssh_key_id")
		}

		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			if strings.TrimSpace(keySource) == "" {
				keySource = "unknown"
			}
			return -1, fmt.Errorf("%s（来源: %s）", err.Error(), keySource)
		}

		sshParts := []string{"ssh", "-p", fmt.Sprintf("%d", port)}
		strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
		if err != nil {
			return -1, err
		}
		if strictHostCheck {
			knownHosts := util.GetEnvOrDefault("SSH_KNOWN_HOSTS_PATH", "~/.ssh/known_hosts")
			expandedKnownHosts, err := util.ExpandHomePath(knownHosts)
			if err != nil {
				return -1, fmt.Errorf("展开 SSH_KNOWN_HOSTS_PATH 失败: %w", err)
			}
			sshParts = append(sshParts,
				"-o", "StrictHostKeyChecking=yes",
				"-o", fmt.Sprintf("UserKnownHostsFile=%s", expandedKnownHosts),
			)
		} else {
			sshParts = append(sshParts, "-o", "StrictHostKeyChecking=no")
		}

		if normalizedKey != "" {
			keyFile, err := os.CreateTemp("", "xirang-key-*.pem")
			if err != nil {
				return -1, fmt.Errorf("创建临时密钥文件失败: %w", err)
			}
			if _, err = keyFile.WriteString(normalizedKey); err != nil {
				_ = keyFile.Close()
				_ = os.Remove(keyFile.Name())
				return -1, fmt.Errorf("写入临时密钥文件失败: %w", err)
			}
			_ = keyFile.Close()
			_ = os.Chmod(keyFile.Name(), 0o600)
			sshParts = append(sshParts, "-i", keyFile.Name())
			cleanup = func() {
				_ = os.Remove(keyFile.Name())
			}
		}

		args = append(args, "-e", strings.Join(sshParts, " "))
		source = fmt.Sprintf("%s@%s:%s", user, task.Node.Host, task.RsyncSource)
	}
	defer cleanup()

	// 使用 `--` 终止参数解析，防止路径内容被解释为 rsync 选项。
	args = append(args, "--", source, task.RsyncTarget)
	cmd := exec.CommandContext(ctx, e.binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}

	if err := cmd.Start(); err != nil {
		return -1, err
	}

	var wg sync.WaitGroup
	stream := func(scanner *bufio.Scanner, level string) {
		defer wg.Done()
		for scanner.Scan() {
			logf(level, scanner.Text())
		}
	}

	wg.Add(2)
	go stream(bufio.NewScanner(stdout), "info")
	go stream(bufio.NewScanner(stderr), "error")

	waitErr := cmd.Wait()
	wg.Wait()
	if waitErr == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if ok := AsExitError(waitErr, &exitErr); ok {
		return exitErr.ExitCode(), waitErr
	}
	return -1, waitErr
}

func ensureLocalTargetReady(target string) error {
	dir := strings.TrimSpace(target)
	if dir == "" {
		return fmt.Errorf("rsync_target 不能为空")
	}

	cleanPath := filepath.Clean(dir)
	if info, err := os.Stat(cleanPath); err == nil {
		if !info.IsDir() {
			cleanPath = filepath.Dir(cleanPath)
		}
	} else {
		cleanPath = filepath.Dir(cleanPath)
	}
	if cleanPath == "." || cleanPath == "" {
		cleanPath = "/tmp"
	}

	if err := os.MkdirAll(cleanPath, 0o755); err != nil {
		return fmt.Errorf("目标目录不可用: %w", err)
	}

	probe, err := os.CreateTemp(cleanPath, ".xirang-rsync-write-check-*")
	if err != nil {
		return fmt.Errorf("目标目录不可写: %w", err)
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)

	minFreeGB, err := readIntEnvWithDefault("RSYNC_MIN_FREE_GB", 0)
	if err != nil {
		return err
	}
	if minFreeGB <= 0 {
		return nil
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(cleanPath, &stat); err != nil {
		return fmt.Errorf("读取目标目录磁盘信息失败: %w", err)
	}
	freeBytes := uint64(stat.Bavail) * uint64(stat.Bsize)
	freeGB := int(freeBytes / (1024 * 1024 * 1024))
	if freeGB < minFreeGB {
		return fmt.Errorf("目标目录可用空间不足，当前 %dGB，要求至少 %dGB", freeGB, minFreeGB)
	}
	return nil
}

func readIntEnvWithDefault(key string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数", key)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s 不能为负数", key)
	}
	return value, nil
}

func resolveNodePrivateKey(node model.Node) (string, string, error) {
	if node.SSHKey != nil {
		if key := strings.TrimSpace(node.SSHKey.PrivateKey); key != "" {
			if node.SSHKeyID != nil {
				return key, fmt.Sprintf("ssh_key_id=%d", *node.SSHKeyID), nil
			}
			return key, "ssh_key_ref", nil
		}
	}

	if node.SSHKeyID != nil {
		keyID := *node.SSHKeyID
		return "", fmt.Sprintf("ssh_key_id=%d", keyID), fmt.Errorf("节点已配置 ssh_key_id=%d，但未加载到对应 SSH Key，请检查密钥是否存在", keyID)
	}

	if key := strings.TrimSpace(node.PrivateKey); key != "" {
		return key, "node.private_key", nil
	}
	return "", "", nil
}

func AsExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	*target = exitErr
	return true
}
