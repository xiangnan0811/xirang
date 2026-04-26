package alerting

import (
	"xirang/backend/internal/model"
	"xirang/backend/internal/slo"

	"gorm.io/gorm"
)

// Raiser is alerting's small inbound interface for upstream subsystems
// that need to surface alerts but would otherwise create an import cycle
// (slo, anomaly, escalation each refer to types alerting can't see). The
// interface is intentionally narrow — only the three verbs that today need
// inversion. Other alerting raise verbs (RaiseNodeProbeFailure,
// RaiseTaskFailure, etc.) remain free functions consumed directly by probe
// and task because those packages don't introduce cycles.
type Raiser interface {
	RaiseSLOBreach(def *model.SLODefinition, c *slo.Compliance) error
	RaiseAnomalyAlert(input AnomalyAlertInput) (alertID uint, raisedNew bool, err error)
	DispatchToIntegrations(alert model.Alert, integrationIDs []uint)
}

// DefaultRaiser is the production implementation backing every Raiser
// receiver. Wraps the existing free-function dispatch verbs in alerting/.
// main.go constructs one of these at boot and passes it to slo.NewEvaluator,
// anomaly.NewEngine, and escalation.NewEngine.
type DefaultRaiser struct {
	DB *gorm.DB
}

func (r DefaultRaiser) RaiseSLOBreach(def *model.SLODefinition, c *slo.Compliance) error {
	return RaiseSLOBreach(r.DB, def, c)
}

func (r DefaultRaiser) RaiseAnomalyAlert(input AnomalyAlertInput) (uint, bool, error) {
	return RaiseAnomalyAlert(r.DB, input)
}

func (r DefaultRaiser) DispatchToIntegrations(alert model.Alert, integrationIDs []uint) {
	DispatchToIntegrations(r.DB, alert, integrationIDs)
}
