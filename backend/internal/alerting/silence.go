package alerting

import (
	"strings"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// MatchSilence 返回第一个在 now 时刻匹配 (alert, node) 的静默规则。
// 若无匹配则返回 nil。字段为 NULL / 空时视为通配。
func MatchSilence(alert model.Alert, node model.Node, silences []model.Silence, now time.Time) *model.Silence {
	for i := range silences {
		s := &silences[i]
		if !isActive(s, now) {
			continue
		}
		if s.MatchNodeID != nil && *s.MatchNodeID != alert.NodeID {
			continue
		}
		if s.MatchCategory != "" {
			// match_category is a type prefix (e.g. "XR-NODE") that matches any ErrorCode
			// of that category (e.g. "XR-NODE-5", "XR-NODE-42"). Exact equality is a subset.
			// To avoid "XR-NODE" incorrectly matching sibling prefix "XR-NODE-EXPIRY-5",
			// the tail after the "-" separator must be purely numeric (instance ID only).
			if alert.ErrorCode != s.MatchCategory {
				tail, ok := strings.CutPrefix(alert.ErrorCode, s.MatchCategory+"-")
				if !ok || !isAllDigits(tail) {
					continue
				}
			}
		}
		if tags := s.DecodedMatchTags(); len(tags) > 0 {
			if !anyTagMatches(tags, splitNodeTags(node.Tags)) {
				continue
			}
		}
		return s
	}
	return nil
}

// isAllDigits reports whether s is non-empty and consists entirely of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isActive(s *model.Silence, now time.Time) bool {
	return !now.Before(s.StartsAt) && now.Before(s.EndsAt)
}

func splitNodeTags(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func anyTagMatches(silenceTags, nodeTags []string) bool {
	idx := make(map[string]struct{}, len(nodeTags))
	for _, t := range nodeTags {
		idx[t] = struct{}{}
	}
	for _, t := range silenceTags {
		if _, ok := idx[t]; ok {
			return true
		}
	}
	return false
}

// ActiveSilences loads silences whose window covers `now`. Intended for dispatcher hot path.
// Current implementation is a linear scan over all active rows; expected working set is <100
// silences at this deployment scale (1-50 nodes). Upgrade to an indexed lookup if the alert
// dispatch rate ever dominates this path.
func ActiveSilences(db *gorm.DB, now time.Time) ([]model.Silence, error) {
	var out []model.Silence
	err := db.Where("starts_at <= ? AND ends_at > ?", now, now).Find(&out).Error
	return out, err
}
