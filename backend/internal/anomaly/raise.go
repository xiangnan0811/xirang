package anomaly

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// AlertRaiser abstracts the alerting.RaiseAnomalyAlert call to avoid importing
// the alerting package (prevents cycle).
type AlertRaiser func(db *gorm.DB, nodeID uint, severity, errorCode, message string) (alertID uint, raisedNew bool, err error)

const alertsEnabledSettingKey = "anomaly.alerts_enabled"

// NewRaiseFn returns a RaiseFn bound to db + settings + raiser. The returned callback
// persists an AnomalyEvent row for every finding, whether or not a new Alert
// was created.
func NewRaiseFn(db *gorm.DB, settingsSvc *settings.Service, raiser AlertRaiser) RaiseFn {
	return func(ctx context.Context, f Finding) error {
		var (
			alertID   uint
			raisedNew bool
			alertErr  error
		)
		if anomalyAlertsEnabled(settingsSvc) {
			alertID, raisedNew, alertErr = raiser(db, f.NodeID, f.Severity, f.ErrorCode, f.Message)
		}
		// Always write the event even if the alert call errored; the event captures
		// the detection moment regardless of alert pipeline state.
		detailsJSON, _ := json.Marshal(f.Details)
		// json.Marshal on a nil map returns []byte("null"), never nil. Normalize
		// "null" to "{}" so downstream JSON consumers can unconditionally parse
		// as object.
		if len(detailsJSON) == 0 || string(detailsJSON) == "null" {
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

func anomalyAlertsEnabled(settingsSvc *settings.Service) bool {
	if settingsSvc == nil {
		return false
	}
	v := strings.TrimSpace(settingsSvc.GetEffective(alertsEnabledSettingKey))
	if v == "" {
		return false
	}
	enabled, err := strconv.ParseBool(v)
	return err == nil && enabled
}

// alertSinkFunc adapts a RaiseFn to the AlertSink interface so the existing
// NewRaiseFn output (which is a closure, not a method receiver) can be used
// as an AlertSink without rewriting raise.go's persistence semantics.
type alertSinkFunc func(ctx context.Context, f Finding) error

func (a alertSinkFunc) Raise(ctx context.Context, f Finding) error { return a(ctx, f) }

// NewSink builds an AlertSink that persists every finding as an
// AnomalyEvent row (via NewRaiseFn) and forwards severity/error_code/message
// to the supplied AlertRaiser only when anomaly.alerts_enabled is true.
// main.go constructs the AlertRaiser as a thin wrapper around
// alerting.DefaultRaiser.RaiseAnomalyAlert.
func NewSink(db *gorm.DB, settingsSvc *settings.Service, raiser AlertRaiser) AlertSink {
	return alertSinkFunc(NewRaiseFn(db, settingsSvc, raiser))
}
