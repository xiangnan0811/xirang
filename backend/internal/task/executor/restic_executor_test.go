package executor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/util"
)

// =============================================================================
// ResticConfig — append_only 解析测试
// =============================================================================

func TestParseResticConfigAppendOnlyDefaults(t *testing.T) {
	cfg, err := parseResticConfig("")
	if err != nil {
		t.Fatalf("parseResticConfig 失败: %v", err)
	}
	if cfg.AppendOnly {
		t.Fatalf("期望 AppendOnly 默认 = false，实际 = true")
	}
}

func TestParseResticConfigAppendOnlyTrue(t *testing.T) {
	cfg, err := parseResticConfig(`{"append_only":true}`)
	if err != nil {
		t.Fatalf("parseResticConfig 失败: %v", err)
	}
	if !cfg.AppendOnly {
		t.Fatalf("期望 AppendOnly = true，实际 = false")
	}
}

func TestParseResticConfigAppendOnlyFalse(t *testing.T) {
	cfg, err := parseResticConfig(`{"append_only":false}`)
	if err != nil {
		t.Fatalf("parseResticConfig 失败: %v", err)
	}
	if cfg.AppendOnly {
		t.Fatalf("期望 AppendOnly = false，实际 = true")
	}
}

func TestParseResticConfigRoundtrip(t *testing.T) {
	original := ResticConfig{
		RepositoryPassword: "FAKE_PASSWORD_FOR_TEST_ONLY",
		ExcludePatterns:    []string{"*.log", "/tmp"},
		AppendOnly:         true,
	}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	parsed, err := parseResticConfig(string(b))
	if err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if parsed.RepositoryPassword != original.RepositoryPassword {
		t.Fatalf("密码不匹配")
	}
	if len(parsed.ExcludePatterns) != 2 {
		t.Fatalf("排除规则数量不匹配")
	}
	if !parsed.AppendOnly {
		t.Fatalf("期望 AppendOnly = true，实际 = false")
	}
}

// =============================================================================
// init 命令构建测试（间接验证 cmdPrefix + initFlag）
// =============================================================================

func TestInitCommandWithoutAppendOnly(t *testing.T) {
	exec := &ResticExecutor{}
	cfg := ResticConfig{AppendOnly: false}
	node := model.Node{Host: "127.0.0.1", Port: 22, Username: "FAKE_USER_FOR_TEST_ONLY", AuthType: "key"}

	pwFilePath := BuildResticPasswordFilePath()
	cmdPrefix := exec.buildCommandPrefix(node, pwFilePath)
	initFlags := ""
	if cfg.AppendOnly {
		initFlags = " --repository-version 2"
	}
	initCmd := fmt.Sprintf("%s init%s -r %s 2>&1", cmdPrefix, initFlags, ShellEscape("/backup/repo"))

	if strings.Contains(initCmd, "--repository-version") {
		t.Fatalf("期望 AppendOnly=false 时不含 --repository-version，实际: %s", initCmd)
	}
	if !strings.Contains(initCmd, "init") {
		t.Fatalf("期望命令包含 init 子命令，实际: %s", initCmd)
	}
}

func TestInitCommandWithAppendOnly(t *testing.T) {
	exec := &ResticExecutor{}
	node := model.Node{Host: "127.0.0.1", Port: 22, Username: "FAKE_USER_FOR_TEST_ONLY", AuthType: "key"}

	pwFilePath := BuildResticPasswordFilePath()
	cmdPrefix := exec.buildCommandPrefix(node, pwFilePath)
	initFlags := " --repository-version 2"
	initCmd := fmt.Sprintf("%s init%s -r %s 2>&1", cmdPrefix, initFlags, ShellEscape("/backup/repo"))

	if !strings.Contains(initCmd, "--repository-version 2") {
		t.Fatalf("期望 AppendOnly=true 时含 --repository-version 2，实际: %s", initCmd)
	}
	if !strings.Contains(initCmd, "init") {
		t.Fatalf("期望命令包含 init 子命令，实际: %s", initCmd)
	}
}

// =============================================================================
// 仓库版本解析测试
// =============================================================================

func TestRepoVersionParseVersion1(t *testing.T) {
	catOut := `{"version":1}`
	var repoConfig struct {
		Version uint `json:"version"`
	}
	if err := json.Unmarshal([]byte(catOut), &repoConfig); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if repoConfig.Version != 1 {
		t.Fatalf("期望版本 = 1，实际 = %d", repoConfig.Version)
	}
}

func TestRepoVersionParseVersion2(t *testing.T) {
	catOut := `{"version":2}`
	var repoConfig struct {
		Version uint `json:"version"`
	}
	if err := json.Unmarshal([]byte(catOut), &repoConfig); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if repoConfig.Version != 2 {
		t.Fatalf("期望版本 = 2，实际 = %d", repoConfig.Version)
	}
}

// =============================================================================
// ShellEscape 辅助测试
// =============================================================================

func TestShellEscapeDoesNotMutateSimplePath(t *testing.T) {
	escaped := ShellEscape("/backup/repo")
	if !strings.HasPrefix(escaped, "'") && !strings.Contains(escaped, "backup") {
		t.Fatalf("期望 ShellEscape 将路径包装或保留，实际: %s", escaped)
	}
}

// =============================================================================
// buildCommandPrefix 中的环境变量不影响 init flag
// =============================================================================

func TestBuildCommandPrefixWithAppendOnly(t *testing.T) {
	exec := &ResticExecutor{binary: "restic"}
	node := model.Node{
		Host:     "10.0.0.1",
		Port:     22,
		Username: "FAKE_USER_FOR_TEST_ONLY",
		AuthType: "key",
	}

	pwFilePath := BuildResticPasswordFilePath()
	prefix := exec.buildCommandPrefix(node, pwFilePath)

	if !strings.Contains(prefix, "--password-file") {
		t.Fatalf("期望命令前缀包含 --password-file，实际: %s", prefix)
	}
	if !strings.Contains(prefix, "restic") {
		t.Fatalf("期望命令前缀包含 restic 二进制名称，实际: %s", prefix)
	}
	// AppendOnly 不应影响 buildCommandPrefix
	if strings.Contains(prefix, "--repository-version") {
		t.Fatalf("buildCommandPrefix 不应包含 --repository-version，实际: %s", prefix)
	}
}

func TestBuildCommandPrefixWithoutPassword(t *testing.T) {
	exec := &ResticExecutor{binary: "restic"}
	node := model.Node{Host: "10.0.0.1", Port: 22, Username: "FAKE_USER_FOR_TEST_ONLY", AuthType: "key"}

	pwFilePath := BuildResticPasswordFilePath()
	prefix := exec.buildCommandPrefix(node, pwFilePath)

	if !strings.Contains(prefix, "--password-file") {
		t.Fatalf("期望前缀包含 --password-file，实际: %s", prefix)
	}
	if !strings.Contains(prefix, pwFilePath) {
		t.Fatalf("期望前缀包含密码文件路径 %s，实际: %s", pwFilePath, prefix)
	}
}

func TestBuildCommandPrefixWithSudoPreservesEnvWrapping(t *testing.T) {
	exec := &ResticExecutor{binary: "/usr/local/bin/restic"}
	node := model.Node{
		Host:     "10.0.0.1",
		Port:     22,
		Username: "FAKE_USER_FOR_TEST_ONLY",
		AuthType: "key",
		UseSudo:  true,
	}

	pwFilePath := BuildResticPasswordFilePath()
	prefix := exec.buildCommandPrefix(node, pwFilePath)
	expected := fmt.Sprintf("sudo /usr/local/bin/restic --password-file %s", ShellEscape(pwFilePath))
	if prefix != expected {
		t.Fatalf("sudo 前缀不等价，期望 %q，实际 %q", expected, prefix)
	}
}

func TestBuildResticPasswordFileArgContainsPasswordFileFlag(t *testing.T) {
	access := NewResticRepositoryAccess("FAKE_PASSWORD_WITH_QUOTE_'_FOR_TEST_ONLY")
	pwFilePath := BuildResticPasswordFilePath()
	pwFileArg := BuildResticPasswordFileArg(pwFilePath)

	if !strings.HasPrefix(pwFileArg, "--password-file ") {
		t.Fatalf("期望 --password-file 前缀，实际: %q", pwFileArg)
	}

	// 验证密码文件创建命令包含正确的密码
	createCmd := BuildCreateResticPasswordFileCmd(pwFilePath, access)
	if !strings.Contains(createCmd, pwFilePath) {
		t.Fatalf("期望创建命令包含密码文件路径 %s，实际: %s", pwFilePath, createCmd)
	}
	if !strings.Contains(createCmd, "chmod 600") {
		t.Fatalf("期望创建命令包含 chmod 600，实际: %s", createCmd)
	}

	// 验证清理命令
	cleanupCmd := BuildCleanupResticPasswordFileCmd(pwFilePath)
	if !strings.HasPrefix(cleanupCmd, "rm -f ") {
		t.Fatalf("期望清理命令以 rm -f 开头，实际: %s", cleanupCmd)
	}
}

func TestResolveResticRepositoryAccessReturnsSafeLocalMetadata(t *testing.T) {
	access, err := ResolveResticRepositoryAccess(`{"repository_password":"FAKE_PASSWORD_FOR_TEST_ONLY"}`)
	if err != nil {
		t.Fatalf("解析 restic repository access 失败: %v", err)
	}
	if access.Password() != "FAKE_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("期望解析仓库访问口令")
	}
	metadata := access.SafeMetadata()
	if metadata["provider"] != "local" || metadata["kind"] != "restic_repository_access" || metadata["source"] != "task_executor_settings" {
		t.Fatalf("metadata 不符合预期: %#v", metadata)
	}
	serialized := fmt.Sprintf("%#v", metadata)
	if strings.Contains(serialized, access.Password()) || strings.Contains(strings.ToLower(serialized), "password") || strings.Contains(strings.ToLower(serialized), "config") || strings.Contains(strings.ToLower(serialized), "credential") {
		t.Fatalf("metadata 不应包含敏感字段或敏感词: %s", serialized)
	}
}

func TestResticRepositoryAccessJSONDoesNotExposePassword(t *testing.T) {
	access := NewResticRepositoryAccess("FAKE_PASSWORD_FOR_TEST_ONLY")
	b, err := json.Marshal(access)
	if err != nil {
		t.Fatalf("序列化 access 失败: %v", err)
	}
	serialized := string(b)
	if strings.Contains(serialized, "FAKE_PASSWORD_FOR_TEST_ONLY") || strings.Contains(strings.ToLower(serialized), "password") {
		t.Fatalf("access JSON 不应包含口令字段或口令值: %s", serialized)
	}
}

func TestResolveResticRepositoryAccessInvalidJSONDoesNotExposeRawConfig(t *testing.T) {
	raw := `{"repository_password":"FAKE_PASSWORD_FOR_TEST_ONLY"`
	_, err := ResolveResticRepositoryAccess(raw)
	if err == nil {
		t.Fatal("期望非法 JSON 返回错误")
	}
	if strings.Contains(err.Error(), "FAKE_PASSWORD_FOR_TEST_ONLY") || strings.Contains(err.Error(), raw) {
		t.Fatalf("错误不应包含原始配置或口令: %v", err)
	}
}

func TestResolveResticRepositoryAccessInvalidTypeDoesNotExposeRawPassword(t *testing.T) {
	raw := `{"repository_password":123,"other":"FAKE_PASSWORD_FOR_TEST_ONLY"}`
	_, err := ResolveResticRepositoryAccess(raw)
	if err == nil {
		t.Fatal("期望非法字段类型返回错误")
	}
	if strings.Contains(err.Error(), "FAKE_PASSWORD_FOR_TEST_ONLY") || strings.Contains(err.Error(), raw) {
		t.Fatalf("错误不应包含原始配置或口令: %v", err)
	}
}

func TestResticBinaryDefault(t *testing.T) {
	t.Setenv("RESTIC_BINARY", "")
	exec := &ResticExecutor{}
	bin := exec.resticBinary()
	expected := util.GetEnvOrDefault("RESTIC_BINARY", "restic")
	if bin != expected {
		t.Fatalf("期望 restic 二进制 = %s，实际 = %s", expected, bin)
	}
}

func TestResticBinaryCustom(t *testing.T) {
	t.Setenv("RESTIC_BINARY", "/usr/local/bin/restic")
	exec := &ResticExecutor{}
	bin := exec.resticBinary()
	if bin != "/usr/local/bin/restic" {
		t.Fatalf("期望 restic 二进制 = /usr/local/bin/restic，实际 = %s", bin)
	}
}
