package escalation

import (
	"xirang/backend/internal/model"
)

// SilenceCheckerFn returns the matching silence (nil = not silenced).
// Injected from main.go to avoid import cycle between escalation and alerting.
type SilenceCheckerFn func(alert model.Alert) *model.Silence

// SenderFn dispatches the alert to the given integration IDs (post-commit, async OK).
// Injected from main.go; typically wraps alerting.DispatchToIntegrations.
type SenderFn func(alert model.Alert, integrationIDs []uint)
