package escalation

import (
	"context"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// Dispatcher is the explicit bridge between escalation persistence and
// notification delivery. EnqueueEscalationDeliveriesTx is called inside the
// Engine.fire transaction and MUST perform database work only; network I/O
// belongs to DispatchEscalationDeliveries, which runs after that transaction
// commits and uses the notification delivery claim/CAS state machine.
type Dispatcher interface {
	EnqueueEscalationDeliveriesTx(tx *gorm.DB, alert model.Alert, event model.AlertEscalationEvent, integrationIDs []uint) ([]uint, error)
	DispatchEscalationDeliveries(ctx context.Context, alert model.Alert, eventID uint, intentIDs []uint) error
}

// SilenceCheckerFn returns the matching silence (nil = not silenced).
// Injected from main.go to avoid import cycle between escalation and alerting.
type SilenceCheckerFn func(alert model.Alert) *model.Silence
