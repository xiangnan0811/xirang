package executor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"xirang/backend/internal/sshutil"
)

const (
	resticRepositoryAccessKind   = "restic_repository_access"
	resticRepositoryAccessSource = "task_executor_settings"
)

var ErrInvalidResticRepositoryAccess = errors.New("invalid restic repository access settings")

type ResticRepositoryAccess struct {
	password string
	Provider string
	Kind     string
	Source   string
}

func ResolveResticRepositoryAccess(raw string) (ResticRepositoryAccess, error) {
	cfg, err := parseResticConfig(raw)
	if err != nil {
		return ResticRepositoryAccess{}, ErrInvalidResticRepositoryAccess
	}
	return NewResticRepositoryAccess(cfg.RepositoryPassword), nil
}

func ResolveResticRepositoryAccessOrEmpty(raw string) ResticRepositoryAccess {
	access, err := ResolveResticRepositoryAccess(raw)
	if err != nil {
		return NewResticRepositoryAccess("")
	}
	return access
}

func NewResticRepositoryAccess(password string) ResticRepositoryAccess {
	return ResticRepositoryAccess{
		password: password,
		Provider: sshutil.CredentialProviderLocal,
		Kind:     resticRepositoryAccessKind,
		Source:   resticRepositoryAccessSource,
	}
}

func (access ResticRepositoryAccess) SafeMetadata() map[string]string {
	return map[string]string{
		"provider": access.Provider,
		"kind":     access.Kind,
		"source":   access.Source,
	}
}

func (access ResticRepositoryAccess) Password() string {
	return access.password
}

// BuildResticPasswordFilePath 生成一个唯一的 restic 密码临时文件路径。
// 使用 crypto/rand 生成随机后缀，避免可预测的路径名。
func BuildResticPasswordFilePath() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("/tmp/xirang_restic_pw_%s", hex.EncodeToString(b))
}

// BuildResticPasswordFileArg 返回 restic 命令使用的 --password-file 参数。
func BuildResticPasswordFileArg(passwordFilePath string) string {
	return "--password-file " + ShellEscape(passwordFilePath)
}

// BuildCreateResticPasswordFileCmd 返回在远程节点上创建 restic 密码文件的命令。
// 密码写入临时文件并设置 chmod 600，确保只有文件所有者可读。
func BuildCreateResticPasswordFileCmd(passwordFilePath string, access ResticRepositoryAccess) string {
	pw := access.Password()
	pwEscaped := ShellEscape(pw)
	pathEscaped := ShellEscape(passwordFilePath)
	return fmt.Sprintf("printf '%%s' %s > %s && chmod 600 %s", pwEscaped, pathEscaped, pathEscaped)
}

// BuildCleanupResticPasswordFileCmd 返回删除远程节点上 restic 密码临时文件的命令。
func BuildCleanupResticPasswordFileCmd(passwordFilePath string) string {
	return "rm -f " + ShellEscape(passwordFilePath)
}

func parseResticConfigWithRepositoryAccess(raw string) (ResticConfig, ResticRepositoryAccess, error) {
	cfg, access, err := resolveResticConfigWithRepositoryAccess(raw)
	if err != nil {
		return ResticConfig{}, ResticRepositoryAccess{}, err
	}
	return cfg, access, nil
}

func resolveResticConfigWithRepositoryAccess(raw string) (ResticConfig, ResticRepositoryAccess, error) {
	cfg, err := parseResticConfig(raw)
	if err != nil {
		return ResticConfig{}, ResticRepositoryAccess{}, ErrInvalidResticRepositoryAccess
	}
	return cfg, NewResticRepositoryAccess(cfg.RepositoryPassword), nil
}