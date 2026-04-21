package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// Service is the CRUD + lookup layer for escalation_policies.
type Service struct{ db *gorm.DB }

// NewService returns a Service bound to db.
func NewService(db *gorm.DB) *Service { return &Service{db: db} }

// PolicyInput is the payload accepted by Create/Update.
type PolicyInput struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	MinSeverity string                  `json:"min_severity"`
	Enabled     bool                    `json:"enabled"`
	Levels      []model.EscalationLevel `json:"levels"`
}

const (
	maxLevels     = 5
	maxTagsPerLvl = 10
	maxTagLen     = 32
)

// ValidatePolicyInput checks levels invariants + severity enums.
// Returns a descriptive error wrapped by ErrInvalidLevels / ErrInvalidSeverity.
func ValidatePolicyInput(in PolicyInput) error {
	n := strings.TrimSpace(in.Name)
	if n == "" || len(n) > 100 {
		return errors.New("name: 1-100 字符")
	}
	if len(in.Description) > 2000 {
		return errors.New("description: 最多 2000 字符")
	}
	switch in.MinSeverity {
	case "info", "warning", "critical":
	default:
		return fmt.Errorf("%w: min_severity 仅支持 info/warning/critical", ErrInvalidSeverity)
	}
	if len(in.Levels) == 0 || len(in.Levels) > maxLevels {
		return fmt.Errorf("%w: 级别数必须 1-%d", ErrInvalidLevels, maxLevels)
	}
	if in.Levels[0].DelaySeconds != 0 {
		return fmt.Errorf("%w: 首级 delay_seconds 必须为 0", ErrInvalidLevels)
	}
	for i, lvl := range in.Levels {
		if len(lvl.IntegrationIDs) == 0 {
			return fmt.Errorf("%w: 第 %d 级 integration_ids 不能为空", ErrInvalidLevels, i+1)
		}
		if i > 0 && lvl.DelaySeconds <= in.Levels[i-1].DelaySeconds {
			return fmt.Errorf("%w: 第 %d 级 delay_seconds 必须严格大于上一级", ErrInvalidLevels, i+1)
		}
		switch lvl.SeverityOverride {
		case "", "info", "warning", "critical":
		default:
			return fmt.Errorf("%w: 第 %d 级 severity_override 非法", ErrInvalidSeverity, i+1)
		}
		if len(lvl.Tags) > maxTagsPerLvl {
			return fmt.Errorf("%w: 第 %d 级 tags 最多 %d 个", ErrInvalidLevels, i+1, maxTagsPerLvl)
		}
		for _, t := range lvl.Tags {
			if len(t) > maxTagLen {
				return fmt.Errorf("%w: 第 %d 级 tag 长度不能超过 %d", ErrInvalidLevels, i+1, maxTagLen)
			}
		}
	}
	return nil
}

// List returns all policies (enabled and disabled), ordered by name.
func (s *Service) List(ctx context.Context) ([]model.EscalationPolicy, error) {
	var out []model.EscalationPolicy
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one policy or ErrNotFound.
func (s *Service) Get(ctx context.Context, id uint) (*model.EscalationPolicy, error) {
	var p model.EscalationPolicy
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// Create inserts a validated policy; returns ErrConflict on duplicate name.
func (s *Service) Create(ctx context.Context, in PolicyInput) (*model.EscalationPolicy, error) {
	if err := ValidatePolicyInput(in); err != nil {
		return nil, err
	}
	lvls, _ := json.Marshal(in.Levels)
	p := model.EscalationPolicy{
		Name:        strings.TrimSpace(in.Name),
		Description: in.Description,
		MinSeverity: in.MinSeverity,
		Enabled:     in.Enabled,
		Levels:      string(lvls),
	}
	if err := s.db.WithContext(ctx).Create(&p).Error; err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return &p, nil
}

// Update replaces fields on the policy; ErrNotFound/ErrConflict/Validation errors propagated.
func (s *Service) Update(ctx context.Context, id uint, in PolicyInput) (*model.EscalationPolicy, error) {
	if err := ValidatePolicyInput(in); err != nil {
		return nil, err
	}
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	lvls, _ := json.Marshal(in.Levels)
	updates := map[string]any{
		"name":         strings.TrimSpace(in.Name),
		"description":  in.Description,
		"min_severity": in.MinSeverity,
		"enabled":      in.Enabled,
		"levels":       string(lvls),
	}
	if err := s.db.WithContext(ctx).Model(p).Updates(updates).Error; err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return s.Get(ctx, id)
}

// Delete removes a policy. FK ON DELETE SET NULL clears references on task/policy/slo/node.
func (s *Service) Delete(ctx context.Context, id uint) error {
	p, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Delete(p).Error
}

// ResolvePolicyForAlert returns the first non-nil escalation_policy_id along the
// chain: Task → Policy → SLODefinition → Node, then fetches the policy.
// Returns (nil, nil) when no link exists (alert should use legacy immediate dispatch).
func (s *Service) ResolvePolicyForAlert(ctx context.Context, alert model.Alert) (*model.EscalationPolicy, error) {
	var pid *uint

	// Task first (covers RaiseTaskFailure / RaiseVerificationFailure)
	if alert.TaskID != nil && *alert.TaskID > 0 {
		var t model.Task
		if err := s.db.WithContext(ctx).Select("id, escalation_policy_id, policy_id").First(&t, *alert.TaskID).Error; err == nil {
			if t.EscalationPolicyID != nil {
				pid = t.EscalationPolicyID
			} else if t.PolicyID != nil && *t.PolicyID > 0 {
				var pol model.Policy
				if err := s.db.WithContext(ctx).Select("id, escalation_policy_id").First(&pol, *t.PolicyID).Error; err == nil {
					pid = pol.EscalationPolicyID
				}
			}
		}
	}

	// SLO (RaiseSLOBreach) — alert.PolicyName holds the SLO name; safer path is: look up by alert.TaskID==nil && alert.ErrorCode prefix "XR-SLO-"
	// We use a heuristic: ErrorCode begins with "XR-SLO-" → parse id from code; if malformed, skip.
	if pid == nil && strings.HasPrefix(alert.ErrorCode, "XR-SLO-") {
		rest := strings.TrimPrefix(alert.ErrorCode, "XR-SLO-")
		if sloIDU, err := strconv.ParseUint(rest, 10, 64); err == nil && sloIDU > 0 {
			sloID := uint(sloIDU)
			var sloDef model.SLODefinition
			if err := s.db.WithContext(ctx).Select("id, escalation_policy_id").First(&sloDef, sloID).Error; err == nil {
				pid = sloDef.EscalationPolicyID
			}
		}
	}

	// Node fallback — only when alert carries a NodeID. Platform-level alerts
	// (RaiseStorageSpaceAlert with NodeID=0) skip this branch and return nil,
	// correctly causing them to use legacy immediate-dispatch.
	if pid == nil && alert.NodeID > 0 {
		var n model.Node
		if err := s.db.WithContext(ctx).Select("id, escalation_policy_id").First(&n, alert.NodeID).Error; err == nil {
			pid = n.EscalationPolicyID
		}
	}

	if pid == nil {
		return nil, nil
	}
	return s.Get(ctx, *pid)
}

func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint") ||
		strings.Contains(s, "duplicate key") ||
		strings.Contains(s, "SQLSTATE 23505")
}
