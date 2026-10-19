package escalation

import (
	"time"

	"xirang/backend/internal/model"
)

type levelEvaluation struct {
	Reason string
	Index  int
	Level  model.EscalationLevel
}

// evaluateNextLevel preserves the runtime elapsed-duration comparison, including
// historical duration overflow behavior. Preview validates its inputs separately.
func evaluateNextLevel(alert model.Alert, policy *model.EscalationPolicy, now time.Time) levelEvaluation {
	result := levelEvaluation{Index: alert.LastLevelFired + 1}
	if policy == nil {
		result.Reason = "no_policy"
	} else if !policy.Enabled {
		result.Reason = "disabled"
	} else if !SeverityAtLeast(alert.Severity, policy.MinSeverity) {
		result.Reason = "below_minimum"
	} else {
		levels := policy.DecodedLevels()
		if result.Index < 0 || result.Index >= len(levels) {
			result.Reason = "complete"
		} else {
			result.Level = levels[result.Index]
			result.Reason = "ready"
			if now.Sub(alert.TriggeredAt) < time.Duration(result.Level.DelaySeconds)*time.Second {
				result.Reason = "waiting"
			}
		}
	}
	return result
}

type levelProjection struct {
	Severity string
	Tags     []string
}

func projectLevel(alert model.Alert, level model.EscalationLevel) levelProjection {
	severity := alert.Severity
	if level.SeverityOverride != "" {
		severity = level.SeverityOverride
	}
	return levelProjection{Severity: severity, Tags: appendUniqueTags(alert.DecodedTags(), level.Tags)}
}

// appendUniqueTags returns existing ∪ add, preserving order of first appearance.
func appendUniqueTags(existing, add []string) []string {
	seen := make(map[string]bool, len(existing))
	out := make([]string, 0, len(existing)+len(add))
	for _, t := range existing {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range add {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
