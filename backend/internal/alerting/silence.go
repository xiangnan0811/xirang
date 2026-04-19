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
		if s.MatchCategory != "" && s.MatchCategory != alert.ErrorCode {
			continue
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

// ActiveSilences 加载 now 时刻处于活跃窗口的静默规则，供告警分发热路径使用。
func ActiveSilences(db *gorm.DB, now time.Time) ([]model.Silence, error) {
	var out []model.Silence
	err := db.Where("starts_at <= ? AND ends_at > ?", now, now).Find(&out).Error
	return out, err
}
