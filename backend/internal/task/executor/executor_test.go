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
	})
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
	if !strings.Contains(joined, "StrictHostKeyChecking=no") {
		t.Fatalf("期望携带 StrictHostKeyChecking=no，实际日志: %s", joined)
	}
	if !strings.Contains(joined, "-i ") || !strings.Contains(joined, "xirang-key-") {
		t.Fatalf("期望携带 -i 临时密钥参数，实际日志: %s", joined)
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

	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {})
	if err == nil {
		t.Fatalf("期望在 SSHKey 关联缺失时失败")
	}
	if !strings.Contains(err.Error(), "ssh_key_id=42") {
		t.Fatalf("期望错误信息包含 ssh_key_id，实际: %v", err)
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

	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {})
	if err == nil {
		t.Fatalf("期望密钥缺失时报错")
	}
	if !strings.Contains(err.Error(), "密钥认证未配置 private_key 或 ssh_key_id") {
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

	exitCode, err := exec.Run(context.Background(), task, func(_ string, _ string) {})
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
