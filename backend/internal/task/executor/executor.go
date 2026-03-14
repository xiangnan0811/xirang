package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"golang.org/x/crypto/ssh"
)

type LogFunc func(level, message string)

type ProgressSample struct {
	ObservedAt     time.Time
	ThroughputMbps float64
}

type ProgressFunc func(sample ProgressSample)

type Executor interface {
	Run(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error)
}

// RestoreExecutor 支持恢复操作的执行器。恢复模式下，source 和 target 都在远程节点上。
type RestoreExecutor interface {
	RunRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error)
}

type Factory interface {
	Resolve(executorType string) Executor
}

type factory struct {
	rsync   Executor
	command Executor
	restic  Executor
	rclone  Executor
}

func NewFactory(rsyncBinary string) Factory {
	return &factory{
		rsync:   &RsyncExecutor{binary: rsyncBinary},
		command: &CommandExecutor{},
		restic:  &ResticExecutor{},
		rclone:  &RcloneExecutor{},
	}
}

func (f *factory) Resolve(executorType string) Executor {
	normalized := strings.ToLower(strings.TrimSpace(executorType))
	switch normalized {
	case "rsync":
		return f.rsync
	case "command":
		return f.command
	case "restic":
		return f.restic
	case "rclone":
		return f.rclone
	default:
		return &DisabledExecutor{executorType: normalized}
	}
}

type DisabledExecutor struct {
	executorType string
}

func (e *DisabledExecutor) Run(_ context.Context, _ model.Task, _ LogFunc, _ ProgressFunc) (int, error) {
	if e.executorType == "" || e.executorType == "local" {
		return -1, fmt.Errorf("本地执行器已禁用")
	}
	return -1, fmt.Errorf("不支持的执行器类型")
}

var progressLinePattern = regexp.MustCompile(`(?i)^\s*[0-9][0-9,]*(?:\.[0-9]+)?\s+[0-9]+%\s+([0-9][0-9,]*(?:\.[0-9]+)?)([kmgt]?)(?:i?b|b)/s\s+`)

type RsyncExecutor struct {
	binary string
}

func (e *RsyncExecutor) Run(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	if strings.TrimSpace(task.RsyncSource) == "" || strings.TrimSpace(task.RsyncTarget) == "" {
		return -1, fmt.Errorf("同步任务缺少源路径或目标路径")
	}

	// 备份模式：标准 rsync 执行（本地 -> 远程）
	if !util.IsRemotePathSpec(task.RsyncTarget) {
		if err := EnsureLocalTargetReady(task.RsyncTarget); err != nil {
			return -1, err
		}
	}

	args := []string{"-avz", "--info=progress2"}
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
			return -1, fmt.Errorf("不支持的认证方式")
		}

		keyContent, keySource, keyResolveErr := resolveNodePrivateKey(task.Node)
		if keyResolveErr != nil {
			return -1, keyResolveErr
		}
		if keyContent == "" {
			return -1, fmt.Errorf("密钥认证未配置，请为节点设置密钥")
		}

		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			if strings.TrimSpace(keySource) == "" {
				keySource = "unknown"
			}
			return -1, fmt.Errorf("私钥校验失败，请检查密钥内容是否正确")
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
				return -1, fmt.Errorf("SSH 主机密钥配置异常，请联系管理员")
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
				return -1, fmt.Errorf("准备密钥文件失败，请稍候重试")
			}
			if _, err = keyFile.WriteString(normalizedKey); err != nil {
				_ = keyFile.Close()
				_ = os.Remove(keyFile.Name())
				return -1, fmt.Errorf("准备密钥文件失败，请稍候重试")
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
			message := scanner.Text()
			if strings.TrimSpace(message) == "" {
				continue
			}
			logf(level, message)
			if level == "info" && progressf != nil {
				if sample, ok := parseProgressSample(message); ok {
					progressf(sample)
				}
			}
		}
	}

	wg.Add(2)
	go stream(newProgressScanner(stdout), "info")
	go stream(newProgressScanner(stderr), "error")

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

func newProgressScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(splitProgressTokens)
	return scanner
}

// RunRestore 实现 RestoreExecutor 接口，在远程节点上执行 rsync 恢复操作。
func (e *RsyncExecutor) RunRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
	return e.runRemoteRestore(ctx, task, logf, progressf)
}

// runRemoteRestore 在远程节点上执行 rsync 恢复操作。
// 与标准备份不同，恢复需要在远程节点上执行 rsync source target（两个路径都是节点本地路径）。
func (e *RsyncExecutor) runRemoteRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error) {
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
		return -1, fmt.Errorf("恢复操作暂不支持密码认证，请为节点配置 SSH key")
	}
	if authType != "key" {
		return -1, fmt.Errorf("不支持的认证方式")
	}

	keyContent, _, keyResolveErr := resolveNodePrivateKey(task.Node)
	if keyResolveErr != nil {
		return -1, keyResolveErr
	}
	if keyContent == "" {
		return -1, fmt.Errorf("密钥认证未配置，请为节点设置密钥")
	}

	normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
	if err != nil {
		return -1, fmt.Errorf("私钥校验失败，请检查密钥内容是否正确")
	}

	// 建立 SSH 连接
	signer, err := ssh.ParsePrivateKey([]byte(normalizedKey))
	if err != nil {
		return -1, fmt.Errorf("解析私钥失败: %w", err)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		Timeout: 30 * time.Second,
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return -1, fmt.Errorf("配置 SSH 主机密钥校验失败: %w", err)
	}
	config.HostKeyCallback = hostKeyCallback

	addr := fmt.Sprintf("%s:%d", task.Node.Host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return -1, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return -1, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	// 构造在远程节点上执行的 rsync 命令
	// 注意：source 和 target 都是节点本地路径
	rsyncCmd := fmt.Sprintf("rsync -avz --info=progress2 -- %s %s",
		shellEscape(task.RsyncSource),
		shellEscape(task.RsyncTarget))

	logf("info", fmt.Sprintf("在远程节点执行: %s", rsyncCmd))

	stdout, err := session.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("获取 stdout 失败: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return -1, fmt.Errorf("获取 stderr 失败: %w", err)
	}

	if err := session.Start(rsyncCmd); err != nil {
		return -1, fmt.Errorf("启动远程命令失败: %w", err)
	}

	// 处理输出
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Split(splitProgressTokens)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" {
				logf("info", line)
				if progressf != nil {
					if sample, ok := parseProgressSample(line); ok {
						progressf(sample)
					}
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" {
				logf("warn", line)
			}
		}
	}()

	// 等待命令完成
	errChan := make(chan error, 1)
	go func() {
		errChan <- session.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		time.Sleep(2 * time.Second)
		_ = session.Signal(ssh.SIGKILL)
		wg.Wait()
		return -1, fmt.Errorf("恢复操作被取消")
	case err := <-errChan:
		wg.Wait()
		if err != nil {
			if exitErr, ok := err.(*ssh.ExitError); ok {
				return exitErr.ExitStatus(), fmt.Errorf("rsync 执行失败: %w", err)
			}
			return -1, fmt.Errorf("远程命令执行失败: %w", err)
		}
		return 0, nil
	}
}

// shellEscape 对 shell 参数进行转义，防止命令注入。
func shellEscape(s string) string {
	// 使用单引号包裹，并转义内部的单引号
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func splitProgressTokens(data []byte, atEOF bool) (advance int, token []byte, err error) {
	start := 0
	for start < len(data) && (data[start] == '\r' || data[start] == '\n') {
		start += 1
	}
	if start > 0 {
		if start == len(data) && !atEOF {
			return start, nil, nil
		}
		data = data[start:]
		advance += start
	}
	for index, value := range data {
		if value == '\r' || value == '\n' {
			if index == 0 {
				return advance + 1, nil, nil
			}
			return advance + index + 1, data[:index], nil
		}
	}
	if atEOF && len(data) > 0 {
		return advance + len(data), data, nil
	}
	return advance, nil, nil
}

func parseProgressSample(message string) (ProgressSample, bool) {
	normalized := strings.TrimSpace(message)
	matches := progressLinePattern.FindStringSubmatch(normalized)
	if len(matches) != 3 {
		return ProgressSample{}, false
	}
	throughputMbps, ok := parseThroughputMbps(matches[1], matches[2])
	if !ok {
		return ProgressSample{}, false
	}
	return ProgressSample{
		ObservedAt:     time.Now().UTC(),
		ThroughputMbps: throughputMbps,
	}, true
}

func parseThroughputMbps(valueField string, unitField string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(valueField), ",", ""), 64)
	if err != nil {
		return 0, false
	}
	multiplier := 1.0
	switch strings.ToLower(strings.TrimSpace(unitField)) {
	case "k":
		multiplier = 1_000
	case "m":
		multiplier = 1_000_000
	case "g":
		multiplier = 1_000_000_000
	case "t":
		multiplier = 1_000_000_000_000
	}
	bytesPerSecond := value * multiplier
	return bytesPerSecond * 8 / 1_000_000, true
}

func EnsureLocalTargetReady(target string) error {
	dir := strings.TrimSpace(target)
	if dir == "" {
		return fmt.Errorf("目标路径不能为空")
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
		return fmt.Errorf("目标目录不可用，请检查路径是否正确")
	}

	probe, err := os.CreateTemp(cleanPath, ".xirang-rsync-write-check-*")
	if err != nil {
		return fmt.Errorf("目标目录不可写，请检查权限")
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
		return fmt.Errorf("读取目标目录磁盘信息失败")
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
		return "", fmt.Sprintf("ssh_key_id=%d", keyID), fmt.Errorf("节点绑定的密钥不存在，请检查密钥配置")
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

// ensureRemoteTargetReady 通过 SSH 检查远程目标路径是否存在且有足够磁盘空间。
func EnsureRemoteTargetReady(ctx context.Context, node model.Node, targetPath string) error {
	if strings.TrimSpace(node.Host) == "" {
		return fmt.Errorf("节点地址不能为空")
	}

	port := node.Port
	if port == 0 {
		port = 22
	}
	user := strings.TrimSpace(node.Username)
	if user == "" {
		user = "root"
	}

	// 构建 SSH 认证（复用 CommandExecutor 的逻辑）
	authType := strings.ToLower(strings.TrimSpace(node.AuthType))
	var authMethods []ssh.AuthMethod

	switch authType {
	case "key":
		keyContent, _, err := resolveNodePrivateKey(node)
		if err != nil {
			return err
		}
		if keyContent == "" {
			return fmt.Errorf("密钥认证未配置")
		}
		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			return fmt.Errorf("私钥校验失败")
		}
		signer, err := ssh.ParsePrivateKey([]byte(normalizedKey))
		if err != nil {
			return fmt.Errorf("解析私钥失败: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	case "password":
		if node.Password == "" {
			return fmt.Errorf("密码认证未配置密码")
		}
		authMethods = append(authMethods, ssh.Password(node.Password))
	default:
		return fmt.Errorf("不支持的认证方式: %s", authType)
	}

	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return fmt.Errorf("主机密钥配置异常: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", node.Host, port)
	client, err := sshutil.DialSSH(ctx, addr, user, authMethods, hostKeyCallback)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close()

	// 检查目标路径是否存在，不存在则创建
	quoted := shellEscape(targetPath)
	checkCmd := fmt.Sprintf("test -d %s || mkdir -p %s", quoted, quoted)
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	if err := session.Run(checkCmd); err != nil {
		_ = session.Close()
		return fmt.Errorf("目标路径不可用: %w", err)
	}
	_ = session.Close()

	// 检查磁盘空间（获取可用空间 GB）
	minFreeGB, err := readIntEnvWithDefault("RSYNC_MIN_FREE_GB", 0)
	if err != nil {
		return err
	}
	if minFreeGB > 0 {
		spaceCmd := fmt.Sprintf("df -BG %s | tail -1 | awk '{print $4}' | sed 's/G//'", quoted)
		session2, err := client.NewSession()
		if err != nil {
			return fmt.Errorf("创建 SSH 会话失败: %w", err)
		}
		output, err := session2.Output(spaceCmd)
		_ = session2.Close()
		if err != nil {
			return fmt.Errorf("读取目标目录磁盘信息失败: %w", err)
		}
		freeGB, err := strconv.Atoi(strings.TrimSpace(string(output)))
		if err != nil {
			return fmt.Errorf("解析磁盘空间失败")
		}
		if freeGB < minFreeGB {
			return fmt.Errorf("目标目录可用空间不足，当前 %dGB，要求至少 %dGB", freeGB, minFreeGB)
		}
	}

	return nil
}
