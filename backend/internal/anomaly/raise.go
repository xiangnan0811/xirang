package anomaly

import (
	"context"
	"encoding/json"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// AlertRaiser abstracts the alerting.RaiseAnomalyAlert call to avoid importing
// the alerting package (prevents cycle).
type AlertRaiser func(db *gorm.DB, nodeID uint, severity, errorCode, message string) (alertID uint, raisedNew bool, err error)

// NewRaiseFn returns a RaiseFn bound to db + raiser. The returned callback
// persists an AnomalyEvent row for every finding, whether or not a new Alert
// was created.
func NewRaiseFn(db *gorm.DB, raiser AlertRaiser) RaiseFn {
	return func(ctx context.Context, f Finding) error {
		alertID, raisedNew, alertErr := raiser(db, f.NodeID, f.Severity, f.ErrorCode, f.Message)
		// Always write the event even if the alert call errored; the event captures
		// the detection moment regardless of alert pipeline state.
		detailsJSON, _ := json.Marshal(f.Details)
		if detailsJSON == nil {
			detailsJSON = []byte("{}")
		}
		evt := model.AnomalyEvent{
			NodeID:        f.NodeID,
			Detector:      f.Detector,
			Metric:        f.Metric,
			Severity:      f.Severity,
			ObservedValue: f.ObservedValue,
			BaselineValue: f.BaselineValue,
			Sigma:         f.Sigma,
			ForecastDays:  f.ForecastDays,
			RaisedAlert:   raisedNew,
			Details:       string(detailsJSON),
			FiredAt:       time.Now().UTC(),
		}
		if alertID > 0 {
			id := alertID
			evt.AlertID = &id
		}
		if err := db.WithContext(ctx).Create(&evt).Error; err != nil {
			return err
		}
		return alertErr
	}
}
