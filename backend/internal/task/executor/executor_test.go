package executor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"golang.org/x/crypto/ssh"
)

func TestFactoryRejectsLocalExecutor(t *testing.T) {
	factory := NewFactory(createArgEchoScript(t))
	exec := factory.Resolve("local")

	task := model.Task{
		ExecutorType: "local",
		Command:      "echo hello",
	}
	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {}, nil)
	if err == nil {
		t.Fatalf("期望 local 执行器被拒绝")
	}
	if exitCode != -1 {
		t.Fatalf("期望退出码=-1，实际=%d", exitCode)
	}
	if !strings.Contains(err.Error(), "已禁用") {
		t.Fatalf("期望返回禁用错误，实际: %v", err)
	}
}

func TestFactoryRejectsUnknownExecutor(t *testing.T) {
	factory := NewFactory(createArgEchoScript(t))
	exec := factory.Resolve("custom")

	task := model.Task{
		ExecutorType: "custom",
	}
	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {}, nil)
	if err == nil {
		t.Fatalf("期望未知执行器被拒绝")
	}
	if exitCode != -1 {
		t.Fatalf("期望退出码=-1，实际=%d", exitCode)
	}
	if !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("期望返回不支持错误，实际: %v", err)
	}
}

func createArgEchoScript(t *testing.T) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "fake-rsync.sh")
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n  printf \"%s\\n\" \"$arg\"\ndone\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假 rsync 脚本失败: %v", err)
	}
	return scriptPath
}

func TestRsyncExecutorUsesSSHKeyRelationWhenNodePrivateKeyEmpty(t *testing.T) {
	exec := &RsyncExecutor{binary: createArgEchoScript(t)}
	target := t.TempDir()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})
	if len(privateKey) == 0 {
		t.Fatalf("编码测试私钥失败")
	}

	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  "/var/data",
		RsyncTarget:  target,
		Node: model.Node{
			Host:     "1.2.3.4",
			Port:     22,
			Username: "root",
			AuthType: "key",
			SSHKey: &model.SSHKey{
				PrivateKey: string(privateKey),
			},
		},
	}

	var lines []string
	exitCode, runErr := exec.Run(context.Background(), task, func(_ string, message string) {
		lines = append(lines, message)
	}, nil)
	if runErr != nil {
		t.Fatalf("期望执行成功，实际失败: %v", runErr)
	}
	if exitCode != 0 {
		t.Fatalf("期望退出码=0，实际=%d", exitCode)
	}

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "root@1.2.3.4:/var/data") {
		t.Fatalf("期望 source 使用远端地址，实际日志: %s", joined)
	}
	if !strings.Contains(joined, "\n--\n") {
		t.Fatalf("期望 rsync 参数包含 -- 以阻断选项注入，实际日志: %s", joined)
	}
	if !strings.Contains(joined, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("期望默认携带 StrictHostKeyChecking=accept-new，实际日志: %s", joined)
	}
	if !strings.Contains(joined, "-i ") || !strings.Contains(joined, "xirang-key-") {
		t.Fatalf("期望携带 -i 临时密钥参数，实际日志: %s", joined)
	}
}

func TestRsyncExecutorUsesStrictHostKeyCheckingWhenAutoAcceptDisabled(t *testing.T) {
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	exec := &RsyncExecutor{binary: createArgEchoScript(t)}
	target := t.TempDir()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})

	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  "/var/data",
		RsyncTarget:  target,
		Node: model.Node{
			Host:     "1.2.3.4",
			Port:     22,
			Username: "root",
			AuthType: "key",
			SSHKey: &model.SSHKey{
				PrivateKey: string(privateKey),
			},
		},
	}

	var lines []string
	exitCode, runErr := exec.Run(context.Background(), task, func(_ string, message string) {
		lines = append(lines, message)
	}, nil)
	if runErr != nil {
		t.Fatalf("期望执行成功，实际失败: %v", runErr)
	}
	if exitCode != 0 {
		t.Fatalf("期望退出码=0，实际=%d", exitCode)
	}

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "StrictHostKeyChecking=yes") {
		t.Fatalf("期望显式禁用时携带 StrictHostKeyChecking=yes，实际日志: %s", joined)
	}
}

func TestRsyncExecutorRejectsStaleNodePrivateKeyWhenSSHKeyIDPresent(t *testing.T) {
	exec := &RsyncExecutor{binary: createArgEchoScript(t)}
	keyID := uint(42)

	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  "/var/data",
		RsyncTarget:  t.TempDir(),
		Node: model.Node{
			Host:       "1.2.3.4",
			Port:       22,
			Username:   "root",
			AuthType:   "key",
			SSHKeyID:   &keyID,
			PrivateKey: "not-a-private-key",
		},
	}

	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {}, nil)
	if err == nil {
		t.Fatalf("期望在 SSHKey 关联缺失时失败")
	}
	if !strings.Contains(err.Error(), "节点绑定的密钥不存在") {
		t.Fatalf("期望错误信息提示密钥不存在，实际: %v", err)
	}
	if strings.Contains(err.Error(), "私钥格式无效") {
		t.Fatalf("不应回退使用 node.private_key 触发私钥格式错误，实际: %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("期望退出码=-1，实际=%d", exitCode)
	}
}

func TestRsyncExecutorFailsWhenKeyAuthHasNoKey(t *testing.T) {
	exec := &RsyncExecutor{binary: createArgEchoScript(t)}

	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  "/var/data",
		RsyncTarget:  t.TempDir(),
		Node: model.Node{
			Host:     "1.2.3.4",
			Port:     22,
			Username: "root",
			AuthType: "key",
		},
	}

	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {}, nil)
	if err == nil {
		t.Fatalf("期望密钥缺失时报错")
	}
	if !strings.Contains(err.Error(), "密钥认证未配置") {
		t.Fatalf("期望返回密钥缺失错误，实际: %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("期望退出码=-1，实际=%d", exitCode)
	}
}

func TestRsyncExecutorRejectsPasswordAuthForRemoteNode(t *testing.T) {
	exec := &RsyncExecutor{binary: createArgEchoScript(t)}

	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  "/var/data",
		RsyncTarget:  t.TempDir(),
		Node: model.Node{
			Host:     "1.2.3.4",
			Port:     22,
			Username: "root",
			AuthType: "password",
			Password: "secret",
		},
	}

	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {}, nil)
	if err == nil {
		t.Fatalf("期望密码认证被拒绝")
	}
	if !strings.Contains(err.Error(), "rsync 远程执行暂不支持密码认证") {
		t.Fatalf("期望返回密码认证限制错误，实际: %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("期望退出码=-1，实际=%d", exitCode)
	}
}

func TestPreparePrivateKeyForSSHNormalizesEscapedNewlines(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})
	if len(pemBytes) == 0 {
		t.Fatalf("编码 PEM 失败")
	}

	escaped := strings.ReplaceAll(string(pemBytes), "\n", "\\n")
	prepared, _, err := sshutil.ValidateAndPreparePrivateKey(escaped, sshutil.SSHKeyTypeAuto)
	if err != nil {
		t.Fatalf("期望可修复并通过校验，实际失败: %v", err)
	}

	if strings.Contains(prepared, "\\n") {
		t.Fatalf("期望转换为真实换行，实际仍包含转义换行")
	}
	if !strings.Contains(prepared, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("期望输出 PEM 私钥，实际: %s", prepared)
	}
	if !strings.HasSuffix(prepared, "\n") {
		t.Fatalf("期望私钥以换行结尾")
	}
	if _, err := ssh.ParsePrivateKey([]byte(prepared)); err != nil {
		t.Fatalf("期望输出私钥可解析，实际失败: %v", err)
	}
}

func TestPreparePrivateKeyForSSHRejectsInvalidContent(t *testing.T) {
	_, _, err := sshutil.ValidateAndPreparePrivateKey("not-a-private-key", sshutil.SSHKeyTypeAuto)
	if err == nil {
		t.Fatalf("期望非法私钥报错")
	}
	if !strings.Contains(err.Error(), "私钥格式无效") {
		t.Fatalf("期望返回私钥格式错误提示，实际: %v", err)
	}
}

func TestParseProgressSampleParsesRsyncProgress2Line(t *testing.T) {
	sample, ok := parseProgressSample("1,258,291,200  64%   12.50MB/s    0:00:12 (xfr#3, to-chk=1/5)")
	if !ok {
		t.Fatalf("期望解析 progress2 行成功")
	}
	if sample.ThroughputMbps != 100 {
		t.Fatalf("期望吞吐为 100 Mbps，实际: %v", sample.ThroughputMbps)
	}
}

func TestParseProgressSampleRejectsNonCanonicalLine(t *testing.T) {
	if _, ok := parseProgressSample("report file-999MB/s.txt uploaded"); ok {
		t.Fatalf("期望非 canonical progress2 行不被解析")
	}
}

func TestRsyncExecutorEmitsProgressSamplesFromCarriageReturnStream(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "fake-rsync-progress-cr.sh")
	script := "#!/bin/sh\nprintf '100  10%%   1.00MB/s    0:00:12 (xfr#1, to-chk=3/5)\r200  20%%   2.00MB/s    0:00:12 (xfr#1, to-chk=2/5)\r300  30%%   3.00MB/s    0:00:12 (xfr#1, to-chk=1/5)\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假进度脚本失败: %v", err)
	}

	exec := &RsyncExecutor{binary: scriptPath}
	task := model.Task{ExecutorType: "rsync", RsyncSource: "/tmp/src", RsyncTarget: t.TempDir()}

	var samples []ProgressSample
	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {}, func(sample ProgressSample) {
		samples = append(samples, sample)
	})
	if err != nil {
		t.Fatalf("期望执行成功，实际失败: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("期望退出码=0，实际=%d", exitCode)
	}
	if len(samples) != 3 {
		t.Fatalf("期望从 \r 刷新流中采到 3 个样本，实际: %d", len(samples))
	}
	if samples[0].ThroughputMbps != 8 || samples[1].ThroughputMbps != 16 || samples[2].ThroughputMbps != 24 {
		t.Fatalf("样本吞吐不符合预期: %+v", samples)
	}
}

// TestRsyncExecutorBackupNotMisidentifiedAsRestore 验证普通备份任务（IsRestore=false）
// 不会被误判为恢复模式。即使 target 路径的父目录在本地不存在，也应走备份路径。
// 这是回归测试：以前的 os.Stat 启发式会将此场景误判为恢复。
func TestRsyncExecutorBackupNotMisidentifiedAsRestore(t *testing.T) {
	exec := &RsyncExecutor{binary: createArgEchoScript(t)}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})

	// target 使用一个本地不存在的路径——以前的 os.Stat 启发式会误判为恢复
	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  "/var/data",
		RsyncTarget:  "/nonexistent/backup/target/that/does/not/exist",
		Node: model.Node{
			Host:     "1.2.3.4",
			Port:     22,
			Username: "root",
			AuthType: "key",
			SSHKey: &model.SSHKey{
				PrivateKey: string(privateKey),
			},
		},
	}

	var lines []string
	// 备份模式下 EnsureLocalTargetReady 会尝试创建目录，可能失败，
	// 但关键断言是不应走远程恢复路径（不应 SSH Dial）
	_, _ = exec.Run(context.Background(), task, func(_ string, message string) {
		lines = append(lines, message)
	}, nil)

	joined := strings.Join(lines, "\n")
	// 如果走了远程恢复路径，日志中会包含"在远程节点执行"
	if strings.Contains(joined, "在远程节点执行") {
		t.Fatalf("普通备份不应走远程恢复路径，日志: %s", joined)
	}
}

// TestRsyncExecutorRestoreUsesRemotePath 验证恢复任务（IsRestore=true）走远程恢复路径。
// 由于没有真实 SSH 服务器，连接会失败，但关键是确认进入了 runRemoteRestore。
func TestRsyncExecutorRestoreUsesRemotePath(t *testing.T) {
	exec := &RsyncExecutor{binary: createArgEchoScript(t)}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成测试私钥失败: %v", err)
	}
	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})

	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  "/backup/data",
		RsyncTarget:  "/var/app/data",
		Node: model.Node{
			Host:     "127.0.0.1",
			Port:     1, // 使用端口 1，连接会立即被拒绝
			Username: "root",
			AuthType: "key",
			SSHKey: &model.SSHKey{
				PrivateKey: string(privateKey),
			},
		},
	}

	_, err = exec.RunRestore(context.Background(), task, func(_ string, _ string) {}, nil)
	// 恢复模式需要真实 SSH 连接，所以必定失败。
	// 关键断言：错误来自 SSH 连接（runRemoteRestore），而非本地 rsync 执行
	if err == nil {
		t.Fatalf("恢复模式无真实 SSH 应报错")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "SSH") && !strings.Contains(errMsg, "ssh") && !strings.Contains(errMsg, "连接") && !strings.Contains(errMsg, "dial") {
		t.Fatalf("恢复模式应因 SSH 连接失败而报错，实际: %v", err)
	}
}

func TestRsyncExecutorEmitsProgressSamples(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "fake-rsync-progress.sh")
	script := "#!/bin/sh\nprintf '1,258,291,200  64%%   12.50MB/s    0:00:12 (xfr#3, to-chk=1/5)\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假进度脚本失败: %v", err)
	}

	exec := &RsyncExecutor{binary: scriptPath}
	task := model.Task{ExecutorType: "rsync", RsyncSource: "/tmp/src", RsyncTarget: t.TempDir()}

	var samples []ProgressSample
	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {}, func(sample ProgressSample) {
		samples = append(samples, sample)
	})
	if err != nil {
		t.Fatalf("期望执行成功，实际失败: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("期望退出码=0，实际=%d", exitCode)
	}
	if len(samples) != 1 {
		t.Fatalf("期望产生 1 个样本，实际: %d", len(samples))
	}
	if samples[0].ThroughputMbps != 100 {
		t.Fatalf("期望样本吞吐为 100 Mbps，实际: %v", samples[0].ThroughputMbps)
	}
}
