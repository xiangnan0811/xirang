package escalation

import "errors"

// Sentinel errors mapped by handlers to HTTP status codes.
var (
	ErrNotFound        = errors.New("escalation policy not found")
	ErrConflict        = errors.New("escalation policy name already exists")
	ErrInvalidLevels   = errors.New("invalid levels configuration")
	ErrInvalidSeverity = errors.New("invalid severity value")
)

// Severity ordering for min_severity checks.
var severityRank = map[string]int{
	"info":     1,
	"warning":  2,
	"critical": 3,
}

// SeverityAtLeast returns true when got >= threshold in severity order.
// Unknown severities rank as 0 (lowest), so unknown → threshold=any returns false.
func SeverityAtLeast(got, threshold string) bool {
	return severityRank[got] >= severityRank[threshold]
}
