package alerting

import (
	"context"

	"xirang/backend/internal/model"
	"xirang/backend/internal/slo"

	"gorm.io/gorm"
)

// Raiser is alerting's small inbound interface for upstream subsystems
// that need to surface alerts but would otherwise create an import cycle
// (slo and anomaly each refer to types alerting can't see). The interface is
// intentionally narrow — only the two raise verbs that today need inversion.
// Escalation delivery uses the separate transaction-aware Dispatcher bridge.
type Raiser interface {
	RaiseSLOBreach(def *model.SLODefinition, c *slo.Compliance) error
	RaiseAnomalyAlert(input AnomalyAlertInput) (alertID uint, raisedNew bool, err error)
}

// DefaultRaiser is the production implementation backing every Raiser
// receiver. It also provides the transaction-aware escalation delivery
// bridge used by escalation.Engine.
type DefaultRaiser struct {
	DB *gorm.DB
}

func (r DefaultRaiser) RaiseSLOBreach(def *model.SLODefinition, c *slo.Compliance) error {
	return RaiseSLOBreach(r.DB, def, c)
}

func (r DefaultRaiser) RaiseAnomalyAlert(input AnomalyAlertInput) (uint, bool, error) {
	return RaiseAnomalyAlert(r.DB, input)
}

// EnqueueEscalationDeliveriesTx delegates the transaction-scoped intent
// materialization to the configured dispatcher without performing network I/O.
func (r DefaultRaiser) EnqueueEscalationDeliveriesTx(
	tx *gorm.DB,
	alert model.Alert,
	event model.AlertEscalationEvent,
	integrationIDs []uint,
) ([]uint, error) {
	return ensureDispatcher(r.DB).EnqueueEscalationDeliveriesTx(tx, alert, event, integrationIDs)
}

// DispatchEscalationDeliveries runs the post-commit delivery claim/CAS path.
func (r DefaultRaiser) DispatchEscalationDeliveries(
	ctx context.Context,
	alert model.Alert,
	eventID uint,
	intentIDs []uint,
) error {
	return ensureDispatcher(r.DB).DispatchEscalationDeliveries(ctx, alert, eventID, intentIDs)
}
