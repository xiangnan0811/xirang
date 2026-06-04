package policy

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"strings"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/config"
	"xirang/backend/internal/model"
	"xirang/backend/internal/repository"
)

// ErrTemplateNotFound is returned when a template policy is not found by ID.
var ErrTemplateNotFound = errors.New("模板策略不存在")

// ErrNotTemplate is returned when the requested policy is not a template.
var ErrNotTemplate = errors.New("该策略不是模板")

// PolicyService provides policy-related business logic (validation, cloning, etc.).
type PolicyService struct {
	repo   repository.PolicyRepository
	runner TaskRunner
}

// NewPolicyService creates a new PolicyService.
func NewPolicyService(repo repository.PolicyRepository, runner TaskRunner) *PolicyService {
	return &PolicyService{repo: repo, runner: runner}
}

// CloneFromTemplate clones a template policy into a new non-template policy.
// The new policy has Enabled=false, DrillEnabled=false, IsTemplate=false, and
// TargetPath set to config.BackupRoot. Node associations are copied from the template.
func (s *PolicyService) CloneFromTemplate(ctx context.Context, templateID uint) (*model.Policy, error) {
	tmpl, err := s.repo.FindByIDWithNodes(ctx, templateID)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return nil, ErrTemplateNotFound
		}
		return nil, apperr.WrapDBError(err)
	}
	if !tmpl.IsTemplate {
		return nil, ErrNotTemplate
	}

	newPolicy := model.Policy{
		Name:              tmpl.Name + " (副本)",
		Description:       tmpl.Description,
		SourcePath:        tmpl.SourcePath,
		TargetPath:        config.BackupRoot,
		CronSpec:          tmpl.CronSpec,
		ExcludeRules:      tmpl.ExcludeRules,
		BwLimit:           tmpl.BwLimit,
		BandwidthSchedule: tmpl.BandwidthSchedule,
		RetentionDays:     tmpl.RetentionDays,
		MaxConcurrent:     tmpl.MaxConcurrent,
		Enabled:           false,
		VerifyEnabled:     tmpl.VerifyEnabled,
		VerifySampleRate:  tmpl.VerifySampleRate,
		IsTemplate:        false,
		DrillEnabled:      false,
		DrillCron:         tmpl.DrillCron,
		DrillTargetNodeID: tmpl.DrillTargetNodeID,
		DrillRestorePath:  tmpl.DrillRestorePath,
		DrillPreVerify:    tmpl.DrillPreVerify,
		DrillVerify:       tmpl.DrillVerify,
		DrillPostVerify:   tmpl.DrillPostVerify,
		DrillAutoCleanup:  tmpl.DrillAutoCleanup,
		RPOMinutes:        tmpl.RPOMinutes,
		RTOMinutes:        tmpl.RTOMinutes,
		RetentionMode:     tmpl.RetentionMode,
		KeepDaily:         tmpl.KeepDaily,
		KeepWeekly:        tmpl.KeepWeekly,
		KeepMonthly:       tmpl.KeepMonthly,
		KeepYearly:        tmpl.KeepYearly,
	}

	err = s.repo.Transaction(ctx, func(txRepo repository.PolicyRepository) error {
		if err := txRepo.Create(ctx, &newPolicy); err != nil {
			return apperr.WrapDBError(err)
		}
		for _, n := range tmpl.Nodes {
			pn := model.PolicyNode{PolicyID: newPolicy.ID, NodeID: n.ID}
			if err := txRepo.CreatePolicyNode(ctx, &pn); err != nil {
				return apperr.WrapDBError(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Reload with node associations.
	result, err := s.repo.FindByIDWithNodes(ctx, newPolicy.ID)
	if err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return result, nil
}

// ValidateHookCommand validates a hook command for shell injection risks.
// It rejects shell metacharacters and known dangerous programs.
func ValidateHookCommand(cmd string) error {
	if len(cmd) > 2048 {
		return fmt.Errorf("hook 命令长度不能超过 2048 个字符")
	}
	for _, ch := range []string{";", "|", "&", "`", "$", "(", ")", "{", "}", "<", ">", "!", "\\", "\n", "\r"} {
		if strings.Contains(cmd, ch) {
			return fmt.Errorf("hook 命令包含不允许的字符: %s", ch)
		}
	}
	blocked := map[string]bool{
		"curl": true, "wget": true, "nc": true, "ncat": true,
		"python": true, "python3": true, "perl": true, "ruby": true,
		"bash": true, "sh": true, "zsh": true, "php": true, "node": true,
		"ssh": true, "scp": true, "telnet": true, "base64": true,
	}
	for _, part := range strings.Fields(strings.ToLower(cmd)) {
		base := part
		if idx := strings.LastIndex(part, "/"); idx >= 0 {
			base = part[idx+1:]
		}
		if blocked[base] {
			return fmt.Errorf("hook 命令包含不允许的程序: %s", base)
		}
	}
	return nil
}

// ValidateDrillRestorePath validates a drill restore path for safety.
// It requires an absolute path, rejects ".." and shell metacharacters,
// and blocks system-critical directories.
func ValidateDrillRestorePath(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "/") {
		return fmt.Errorf("drill_restore_path 必须是绝对路径")
	}
	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("drill_restore_path 不能包含 \"..\"")
	}
	if strings.ContainsAny(trimmed, ";|&$`\\\"'(){}[]<>!#~*?\n\r") {
		return fmt.Errorf("drill_restore_path 包含非法字符")
	}
	cleanPath := pathpkg.Clean(trimmed)
	forbidden := []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot", "/dev", "/proc", "/sys", "/run", "/var/run"}
	for _, p := range forbidden {
		if cleanPath == p || strings.HasPrefix(cleanPath, p+"/") {
			return fmt.Errorf("drill_restore_path 禁止恢复到系统目录: %s", p)
		}
	}
	return nil
}
