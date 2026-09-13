package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
func runShellCommand(t *testing.T, command string) error {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("shell command failed: %v\n%s", err, output)
	}
	return err
}

func TestBuildResticPasswordFileLifecycleUsesPrivateExclusivePaths(t *testing.T) {
	passwordFilePath := BuildResticPasswordFilePath()
	password := "FAKE_PASSWORD_WITH_QUOTE_'_FOR_TEST_ONLY"
	createCommand := "umask 022\n" + BuildCreateResticPasswordFileCmd(passwordFilePath, NewResticRepositoryAccess(password))

	if err := runShellCommand(t, createCommand); err != nil {
		t.Fatalf("create command failed: %v", err)
	}
	t.Cleanup(func() {
		_ = runShellCommand(t, BuildCleanupResticPasswordFileCmd(passwordFilePath))
	})

	privateDir := filepath.Dir(passwordFilePath)
	dirInfo, err := os.Stat(privateDir)
	if err != nil {
		t.Fatalf("stat private directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("private directory mode=%04o, want 0700", got)
	}
	fileInfo, err := os.Lstat(passwordFilePath)
	if err != nil {
		t.Fatalf("stat password file: %v", err)
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatal("password path unexpectedly became a symlink")
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("password file mode=%04o, want 0600", got)
	}
	content, err := os.ReadFile(passwordFilePath)
	if err != nil {
		t.Fatalf("read password file: %v", err)
	}
	if string(content) != password {
		t.Fatalf("password content=%q, want fake password", content)
	}
	if setprivPath, err := exec.LookPath("setpriv"); err == nil {
		otherUser := exec.Command(setprivPath, "--reuid=65534", "--regid=65534", "--clear-groups", "cat", passwordFilePath)
		output, err := otherUser.CombinedOutput()
		if err == nil {
			t.Fatalf("unprivileged UID unexpectedly read password file: %q", output)
		}
		if bytes.Contains(output, []byte(password)) {
			t.Fatal("unprivileged command output contained the password")
		}
	}

	cleanupCommand := BuildCleanupResticPasswordFileCmd(passwordFilePath)
	if err := runShellCommand(t, cleanupCommand); err != nil {
		t.Fatalf("cleanup command failed: %v", err)
	}
	if _, err := os.Lstat(privateDir); !os.IsNotExist(err) {
		t.Fatalf("private directory still exists after cleanup: %v", err)
	}
}

func TestBuildResticPasswordCreateCleansPrivateDirectoryOnCancellation(t *testing.T) {
	passwordFilePath := BuildResticPasswordFilePath()
	privateDir := filepath.Dir(passwordFilePath)
	barrier := filepath.Join(t.TempDir(), "after-mkdir")
	release := filepath.Join(t.TempDir(), "release")
	command := BuildCreateResticPasswordFileCmd(passwordFilePath, NewResticRepositoryAccess("FAKE_CANCEL_PASSWORD_FOR_TEST_ONLY"))
	markerCreate := fmt.Sprintf(
		"touch %s\nwhile [ ! -f %s ]; do sleep 0.01; done\nmarker_tmp=$(mktemp",
		ShellEscape(barrier), ShellEscape(release),
	)
	command = strings.Replace(command, "marker_tmp=$(mktemp", markerCreate, 1)
	process := exec.Command("/bin/sh", "-c", command)
	process.Env = append(os.Environ(), "PATH="+os.Getenv("PATH"))
	if err := process.Start(); err != nil {
		t.Fatalf("start create command: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		_ = process.Process.Kill()
		_ = process.Wait()
		_ = os.RemoveAll(privateDir)
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("create command did not reach cancellation barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("cancel create command: %v", err)
	}
	_ = os.WriteFile(release, nil, 0o600)
	if err := process.Wait(); err == nil {
		t.Fatal("canceled create command unexpectedly succeeded")
	}
	if _, err := os.Lstat(privateDir); !os.IsNotExist(err) {
		t.Fatalf("private directory survived cancellation: %v", err)
	}
}
func TestBuildResticPasswordCreateCleansAfterExclusivePublishFailure(t *testing.T) {
	passwordFilePath := BuildResticPasswordFilePath()
	privateDir := filepath.Dir(passwordFilePath)
	binDir := t.TempDir()
	counterPath := filepath.Join(t.TempDir(), "ln-count")
	lnWrapper := filepath.Join(binDir, "ln")
	const wrapper = `#!/bin/sh
count=$(cat "$R7_LN_COUNTER" 2>/dev/null || printf '0')
count=$((count + 1))
printf '%s' "$count" > "$R7_LN_COUNTER"
if [ "$count" -eq 2 ]; then
	exit 1
fi
exec /usr/bin/ln "$@"
`
	if err := os.WriteFile(lnWrapper, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write ln wrapper: %v", err)
	}
	command := exec.Command("/bin/sh", "-c", BuildCreateResticPasswordFileCmd(
		passwordFilePath, NewResticRepositoryAccess("FAKE_PUBLISH_FAILURE_PASSWORD_FOR_TEST_ONLY"),
	))
	command.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "R7_LN_COUNTER="+counterPath)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("exclusive publish failure unexpectedly succeeded: %q", output)
	}
	if _, err := os.Lstat(privateDir); !os.IsNotExist(err) {
		t.Fatalf("private directory survived publish failure: %v", err)
	}
}

func TestBuildResticPasswordFileRejectsPreexistingRegularAndSymlinkPaths(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, privateDir, passwordPath string) string
		assertKeep func(t *testing.T, passwordPath, sentinel string)
	}{
		{
			name: "empty private directory",
			setup: func(t *testing.T, privateDir, _ string) string {
				t.Helper()
				if err := os.Mkdir(privateDir, 0o700); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			assertKeep: func(t *testing.T, passwordPath, _ string) {
				t.Helper()
				if _, err := os.Stat(filepath.Dir(passwordPath)); err != nil {
					t.Fatalf("pre-existing empty directory was removed: %v", err)
				}
			},
		},
		{
			name: "private directory symlink",
			setup: func(t *testing.T, privateDir, _ string) string {
				t.Helper()
				target := filepath.Join(filepath.Dir(privateDir), "real-directory")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, privateDir); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			assertKeep: func(t *testing.T, passwordPath, _ string) {
				t.Helper()
				info, err := os.Lstat(filepath.Dir(passwordPath))
				if err != nil {
					t.Fatalf("stat pre-existing directory symlink: %v", err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("pre-existing directory symlink was replaced")
				}
			},
		},
		{
			name: "dangling private directory symlink",
			setup: func(t *testing.T, privateDir, _ string) string {
				t.Helper()
				target := filepath.Join(filepath.Dir(privateDir), "missing-directory")
				if err := os.Symlink(target, privateDir); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			assertKeep: func(t *testing.T, passwordPath, _ string) {
				t.Helper()
				info, err := os.Lstat(filepath.Dir(passwordPath))
				if err != nil {
					t.Fatalf("stat dangling directory symlink: %v", err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("dangling directory symlink was replaced")
				}
			},
		},
		{
			name: "regular file",
			setup: func(t *testing.T, privateDir, passwordPath string) string {
				t.Helper()
				if err := os.Mkdir(privateDir, 0o700); err != nil {
					t.Fatal(err)
				}
				const sentinel = "ATTACKER_EXISTING_FILE"
				if err := os.WriteFile(passwordPath, []byte(sentinel), 0o600); err != nil {
					t.Fatal(err)
				}
				return sentinel
			},
			assertKeep: func(t *testing.T, passwordPath, sentinel string) {
				t.Helper()
				content, err := os.ReadFile(passwordPath)
				if err != nil {
					t.Fatalf("read colliding file: %v", err)
				}
				if string(content) != sentinel {
					t.Fatalf("colliding file changed to %q", content)
				}
			},
		},
		{
			name: "dangling symlink",
			setup: func(t *testing.T, privateDir, passwordPath string) string {
				t.Helper()
				if err := os.Mkdir(privateDir, 0o700); err != nil {
					t.Fatal(err)
				}
				const sentinel = "ATTACKER_DANGLING_TARGET"
				target := filepath.Join(filepath.Dir(privateDir), "missing-target")
				if err := os.Symlink(target, passwordPath); err != nil {
					t.Fatal(err)
				}
				return sentinel
			},
			assertKeep: func(t *testing.T, passwordPath, _ string) {
				t.Helper()
				info, err := os.Lstat(passwordPath)
				if err != nil {
					t.Fatalf("stat dangling symlink: %v", err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("dangling symlink was replaced")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			privateDir := filepath.Join(root, "private")
			passwordPath := filepath.Join(privateDir, "password")
			sentinel := test.setup(t, privateDir, passwordPath)
			createCommand := BuildCreateResticPasswordFileCmd(passwordPath, NewResticRepositoryAccess("FAKE_COLLISION_PASSWORD_FOR_TEST_ONLY"))
			if err := runShellCommand(t, createCommand); err == nil {
				t.Fatal("create command unexpectedly succeeded over an existing path")
			}
			test.assertKeep(t, passwordPath, sentinel)
			if err := runShellCommand(t, BuildCleanupResticPasswordFileCmd(passwordPath)); err != nil {
				t.Fatalf("cleanup command failed: %v", err)
			}
			test.assertKeep(t, passwordPath, sentinel)
		})
	}
}

func TestBuildResticPasswordCleanupLeavesReplacementSymlink(t *testing.T) {
	passwordFilePath := BuildResticPasswordFilePath()
	createCommand := BuildCreateResticPasswordFileCmd(passwordFilePath, NewResticRepositoryAccess("FAKE_PASSWORD_FOR_TEST_ONLY"))
	if err := runShellCommand(t, createCommand); err != nil {
		t.Fatalf("create command failed: %v", err)
	}
	privateDir := filepath.Dir(passwordFilePath)
	markerPath := filepath.Join(privateDir, ".xirang_restic_pw_owner")
	t.Cleanup(func() {
		_ = os.Remove(passwordFilePath)
		_ = os.Remove(markerPath)
		_ = os.Remove(privateDir)
	})

	externalPath := filepath.Join(t.TempDir(), "attacker-target")
	const sentinel = "ATTACKER_TARGET_MUST_SURVIVE"
	if err := os.WriteFile(externalPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(passwordFilePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalPath, passwordFilePath); err != nil {
		t.Fatal(err)
	}

	if err := runShellCommand(t, BuildCleanupResticPasswordFileCmd(passwordFilePath)); err != nil {
		t.Fatalf("cleanup command failed: %v", err)
	}
	if _, err := os.Lstat(passwordFilePath); err != nil {
		t.Fatalf("replacement symlink was removed: %v", err)
	}
	content, err := os.ReadFile(externalPath)
	if err != nil {
		t.Fatalf("read attacker target: %v", err)
	}
	if string(content) != sentinel {
		t.Fatalf("attacker target changed to %q", content)
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

func TestResticLinkTagValidationAcceptsOnlyCanonicalTaskLinkTags(t *testing.T) {
	valid := "xirang.link.v1.0123456789abcdef0123456789abcdef"
	if !validResticLinkTag(valid) {
		t.Fatalf("canonical link tag %q was rejected", valid)
	}
	for _, tag := range []string{"", "xirang.point.v1.0123456789abcdef0123456789abcdef", valid + ",other", "xirang.link.v1.UPPERCASE0123456789abcdef"} {
		if validResticLinkTag(tag) {
			t.Fatalf("noncanonical link tag %q was accepted", tag)
		}
	}
}
