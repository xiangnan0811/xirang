package executor

import (
	"errors"

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

func BuildResticEnvPrefix(access ResticRepositoryAccess) string {
	if access.password == "" {
		return "RESTIC_PASSWORD=''"
	}
	return "RESTIC_PASSWORD=" + ShellEscape(access.password)
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
