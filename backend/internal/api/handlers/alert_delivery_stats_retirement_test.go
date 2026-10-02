package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	alertStatsFeatureRetiredReason = "feature_retired"

	alertStatsOwnedNodeID     = uint(501)
	alertStatsUnownedNodeID   = uint(502)
	alertStatsOperatorID      = uint(701)
	alertStatsNoOwnerUserID   = uint(702)
	alertStatsOwnedAlertID    = uint(1001)
	alertStatsUnownedAlertID  = uint(1002)
	alertStatsRetiredAlertID  = uint(1003)
	alertStatsUnknownAlertID  = uint(1004)
	alertStatsNullReasonID    = uint(1005)
	alertStatsIntegrationAID  = uint(2001)
	alertStatsIntegrationBID  = uint(2002)
	alertStatsIntegrationCID  = uint(2003)
	alertStatsRetiredOnlyID   = uint(2004)
	alertStatsUnknownID       = uint(2005)
	alertStatsPendingOnlyID   = uint(2006)
	alertStatsMissingParentID = uint(9999)
	alertStatsFailureDelivery = uint(3001)
)

func TestAlertDeliveryStatsRetirement(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	seedAlertDeliveryStatsFixture(t, db)
	exerciseAlertDeliveryStatsFixture(t, db)
}

func TestAlertDeliveryStatsPostgres(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := openR306DB(t, "postgres", &model.User{}, &model.Alert{}, &model.AlertDelivery{}, &model.Integration{}, &model.NodeOwner{})
	seedAlertDeliveryStatsFixture(t, db)
	exerciseAlertDeliveryStatsFixture(t, db)
}

func seedAlertDeliveryStatsFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.User{}, &model.Alert{}, &model.AlertDelivery{}, &model.Integration{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("migrate alert delivery stats fixture: %v", err)
	}
	users := []model.User{
		{ID: alertStatsOperatorID, Username: "stats-operator", PasswordHash: "fixture-password-hash", Role: "operator"},
		{ID: alertStatsNoOwnerUserID, Username: "stats-no-owner", PasswordHash: "fixture-password-hash", Role: "operator"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("seed alert delivery stats users: %v", err)
	}

	now := time.Now().UTC()
	alerts := []model.Alert{
		{
			ID:             alertStatsOwnedAlertID,
			NodeID:         alertStatsOwnedNodeID,
			NodeName:       "owned-node",
			Severity:       "warning",
			Status:         "open",
			ErrorCode:      "XR-STATS-OWNED",
			Message:        "owned alert",
			TriggeredAt:    now,
			DeliveryReason: "ordinary",
		},
		{
			ID:             alertStatsUnownedAlertID,
			NodeID:         alertStatsUnownedNodeID,
			NodeName:       "unowned-node",
			Severity:       "warning",
			Status:         "open",
			ErrorCode:      "XR-STATS-UNOWNED",
			Message:        "unowned alert",
			TriggeredAt:    now,
			DeliveryReason: "ordinary",
		},
		{
			ID:               alertStatsRetiredAlertID,
			NodeID:           alertStatsOwnedNodeID,
			NodeName:         "owned-node",
			Severity:         "warning",
			Status:           "resolved",
			ErrorCode:        "XR-STATS-RETIRED",
			Message:          "retired alert",
			TriggeredAt:      now,
			DeliveryDecision: model.AlertDeliveryDecisionUnknown,
			DeliveryReason:   alertStatsFeatureRetiredReason,
		},
		{
			ID:               alertStatsUnknownAlertID,
			NodeID:           alertStatsOwnedNodeID,
			NodeName:         "owned-node",
			Severity:         "warning",
			Status:           "open",
			ErrorCode:        "XR-STATS-UNKNOWN",
			Message:          "legacy unknown alert",
			TriggeredAt:      now,
			DeliveryDecision: model.AlertDeliveryDecisionUnknown,
			DeliveryReason:   "legacy_unknown",
		},
		{
			ID:             alertStatsNullReasonID,
			NodeID:         alertStatsOwnedNodeID,
			NodeName:       "owned-node",
			Severity:       "warning",
			Status:         "open",
			ErrorCode:      "XR-STATS-NULL-REASON",
			Message:        "null reason alert",
			TriggeredAt:    now,
			DeliveryReason: "temporary",
		},
	}
	if err := db.Create(&alerts).Error; err != nil {
		t.Fatalf("seed alert delivery stats alerts: %v", err)
	}
	if err := db.Exec("UPDATE alerts SET delivery_reason = NULL WHERE id = ?", alertStatsNullReasonID).Error; err != nil {
		t.Fatalf("set null delivery reason: %v", err)
	}

	integrations := []model.Integration{
		{ID: alertStatsIntegrationAID, Type: "webhook", Name: "stats-a", Endpoint: "https://example.test/stats-a", Enabled: true, FailThreshold: 1, CooldownMinutes: 1},
		{ID: alertStatsIntegrationBID, Type: "slack", Name: "stats-b", Endpoint: "https://example.test/stats-b", Enabled: true, FailThreshold: 1, CooldownMinutes: 1},
		{ID: alertStatsIntegrationCID, Type: "email", Name: "stats-retired-sent", Endpoint: "https://example.test/stats-c", Enabled: true, FailThreshold: 1, CooldownMinutes: 1},
		{ID: alertStatsRetiredOnlyID, Type: "webhook", Name: "stats-retired-only", Endpoint: "https://example.test/stats-retired-only", Enabled: true, FailThreshold: 1, CooldownMinutes: 1},
		{ID: alertStatsUnknownID, Type: "webhook", Name: "stats-unknown", Endpoint: "https://example.test/stats-unknown", Enabled: true, FailThreshold: 1, CooldownMinutes: 1},
		{ID: alertStatsPendingOnlyID, Type: "webhook", Name: "stats-pending", Endpoint: "https://example.test/stats-pending", Enabled: true, FailThreshold: 1, CooldownMinutes: 1},
	}
	if err := db.Create(&integrations).Error; err != nil {
		t.Fatalf("seed alert delivery stats integrations: %v", err)
	}

	deliveries := []model.AlertDelivery{
		{ID: 1, AlertID: alertStatsOwnedAlertID, IntegrationID: alertStatsIntegrationAID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-1 * time.Hour)},
		{ID: 2, AlertID: alertStatsOwnedAlertID, IntegrationID: alertStatsIntegrationAID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: 3, AlertID: alertStatsOwnedAlertID, IntegrationID: alertStatsIntegrationBID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-3 * time.Hour)},
		{ID: 4, AlertID: alertStatsUnownedAlertID, IntegrationID: alertStatsIntegrationAID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-4 * time.Hour)},
		{ID: 5, AlertID: alertStatsUnownedAlertID, IntegrationID: alertStatsIntegrationBID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-5 * time.Hour)},
		{ID: 6, AlertID: alertStatsRetiredAlertID, IntegrationID: alertStatsIntegrationCID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-6 * time.Hour)},
		{ID: 7, AlertID: alertStatsUnknownAlertID, IntegrationID: alertStatsIntegrationBID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-7 * time.Hour)},
		{ID: 8, AlertID: alertStatsUnknownAlertID, IntegrationID: alertStatsIntegrationBID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-8 * time.Hour)},
		{ID: 9, AlertID: alertStatsNullReasonID, IntegrationID: alertStatsIntegrationAID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-9 * time.Hour)},
		{ID: 10, AlertID: alertStatsMissingParentID, IntegrationID: 9099, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-10 * time.Hour)},
		{ID: 11, AlertID: alertStatsRetiredAlertID, IntegrationID: alertStatsRetiredOnlyID, Status: model.AlertDeliveryStatusFailed, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-11 * time.Hour)},
		{ID: 12, AlertID: alertStatsUnknownAlertID, IntegrationID: alertStatsUnknownID, Status: "unknown", Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-12 * time.Hour)},
		{ID: 13, AlertID: alertStatsUnknownAlertID, IntegrationID: alertStatsPendingOnlyID, Status: model.AlertDeliveryStatusPending, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-13 * time.Hour)},
		{ID: 14, AlertID: alertStatsOwnedAlertID, IntegrationID: alertStatsIntegrationAID, Status: model.AlertDeliveryStatusSent, Decision: "deliver", AttemptCount: 1, CreatedAt: now.Add(-25 * time.Hour)},
	}
	if err := db.Create(&deliveries).Error; err != nil {
		t.Fatalf("seed alert delivery stats deliveries: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: alertStatsOwnedNodeID, UserID: alertStatsOperatorID}).Error; err != nil {
		t.Fatalf("seed alert delivery stats ownership: %v", err)
	}
}

func exerciseAlertDeliveryStatsFixture(t *testing.T, db *gorm.DB) {
	t.Helper()

	initial := requestAlertDeliveryStats(t, db, "admin", 0, 24)
	assertAlertDeliveryStatsTotals(t, initial, 10, 0, 100)
	if len(initial.ByIntegration) != 6 {
		t.Fatalf("initial integration row count = %d, want 6", len(initial.ByIntegration))
	}
	assertAlertDeliveryIntegration(t, initial, alertStatsIntegrationAID, "stats-a", "webhook", 4, 0)
	assertAlertDeliveryIntegration(t, initial, alertStatsIntegrationBID, "stats-b", "slack", 4, 0)
	assertAlertDeliveryIntegration(t, initial, alertStatsIntegrationCID, "stats-retired-sent", "email", 1, 0)
	assertAlertDeliveryIntegration(t, initial, alertStatsUnknownID, "stats-unknown", "webhook", 0, 0)
	assertAlertDeliveryIntegration(t, initial, alertStatsPendingOnlyID, "stats-pending", "webhook", 0, 0)
	assertAlertDeliveryIntegration(t, initial, 9099, "integration-9099", "", 1, 0)
	assertAlertDeliveryIntegrationAbsent(t, initial, alertStatsRetiredOnlyID)

	if err := db.Create(&model.AlertDelivery{
		ID:            alertStatsFailureDelivery,
		AlertID:       alertStatsOwnedAlertID,
		IntegrationID: alertStatsIntegrationBID,
		Status:        model.AlertDeliveryStatusFailed,
		Decision:      "deliver",
		AttemptCount:  1,
		CreatedAt:     time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed non-retired delivery failure: %v", err)
	}

	admin := requestAlertDeliveryStats(t, db, "admin", 0, 24)
	assertAlertDeliveryStatsTotals(t, admin, 10, 1, 90.9)
	assertAlertDeliveryIntegration(t, admin, alertStatsIntegrationBID, "stats-b", "slack", 4, 1)
	assertAlertDeliveryIntegrationAbsent(t, admin, alertStatsRetiredOnlyID)

	viewer := requestAlertDeliveryStats(t, db, "viewer", 999, 24)
	assertAlertDeliveryStatsTotals(t, viewer, 10, 1, 90.9)
	assertAlertDeliveryIntegration(t, viewer, alertStatsIntegrationAID, "stats-a", "webhook", 4, 0)
	assertAlertDeliveryIntegration(t, viewer, alertStatsIntegrationBID, "stats-b", "slack", 4, 1)
	assertAlertDeliveryIntegration(t, viewer, 9099, "integration-9099", "", 1, 0)
	assertAlertDeliveryIntegrationAbsent(t, viewer, alertStatsRetiredOnlyID)

	operator := requestAlertDeliveryStats(t, db, "operator", alertStatsOperatorID, 24)
	assertAlertDeliveryStatsTotals(t, operator, 7, 1, 87.5)
	if len(operator.ByIntegration) != 5 {
		t.Fatalf("owned operator integration row count = %d, want 5", len(operator.ByIntegration))
	}
	assertAlertDeliveryIntegration(t, operator, alertStatsIntegrationAID, "stats-a", "webhook", 3, 0)
	assertAlertDeliveryIntegration(t, operator, alertStatsIntegrationBID, "stats-b", "slack", 3, 1)
	assertAlertDeliveryIntegration(t, operator, alertStatsIntegrationCID, "stats-retired-sent", "email", 1, 0)
	assertAlertDeliveryIntegration(t, operator, alertStatsUnknownID, "stats-unknown", "webhook", 0, 0)
	assertAlertDeliveryIntegration(t, operator, alertStatsPendingOnlyID, "stats-pending", "webhook", 0, 0)
	assertAlertDeliveryIntegrationAbsent(t, operator, alertStatsRetiredOnlyID)
	assertAlertDeliveryIntegrationAbsent(t, operator, 9099)

	noOwnedNodes := requestAlertDeliveryStats(t, db, "operator", alertStatsNoOwnerUserID, 24)
	assertAlertDeliveryStatsTotals(t, noOwnedNodes, 0, 0, 0)
	if len(noOwnedNodes.ByIntegration) != 0 {
		t.Fatalf("operator without owned nodes returned %d integration rows", len(noOwnedNodes.ByIntegration))
	}

	window := requestAlertDeliveryStats(t, db, "admin", 0, 72)
	assertAlertDeliveryStatsTotals(t, window, 11, 1, 91.7)
	assertAlertDeliveryIntegration(t, window, alertStatsIntegrationAID, "stats-a", "webhook", 5, 0)

	// Exercise the non-sent branch: sent rows alone cannot prove that NULL,
	// empty reasons and missing parents survive the retirement predicate.
	const emptyReasonAlertID = uint(1006)
	if err := db.Create(&model.Alert{
		ID: emptyReasonAlertID, NodeID: alertStatsOwnedNodeID, NodeName: "owned-node",
		Severity: "warning", Status: "open", ErrorCode: "XR-STATS-EMPTY-REASON",
		Message: "empty reason alert", TriggeredAt: time.Now().UTC(), DeliveryReason: "",
	}).Error; err != nil {
		t.Fatalf("seed empty-reason parent: %v", err)
	}
	failures := []model.AlertDelivery{
		{ID: 3002, AlertID: alertStatsNullReasonID, IntegrationID: alertStatsIntegrationAID},
		{ID: 3003, AlertID: emptyReasonAlertID, IntegrationID: alertStatsIntegrationAID},
		{ID: 3004, AlertID: alertStatsMissingParentID, IntegrationID: 9099},
		{ID: 3005, AlertID: alertStatsUnownedAlertID, IntegrationID: alertStatsIntegrationBID},
	}
	for i := range failures {
		failures[i].Status = model.AlertDeliveryStatusFailed
		failures[i].Decision = "unknown"
		failures[i].AttemptCount = 1
		failures[i].CreatedAt = time.Now().UTC()
	}
	if err := db.Create(&failures).Error; err != nil {
		t.Fatalf("seed non-retired boundary failures: %v", err)
	}
	for _, role := range []string{"admin", "viewer"} {
		result := requestAlertDeliveryStats(t, db, role, 0, 24)
		assertAlertDeliveryStatsTotals(t, result, 10, 5, 66.7)
		assertAlertDeliveryIntegration(t, result, alertStatsIntegrationAID, "stats-a", "webhook", 4, 2)
		assertAlertDeliveryIntegration(t, result, alertStatsIntegrationBID, "stats-b", "slack", 4, 2)
		assertAlertDeliveryIntegration(t, result, 9099, "integration-9099", "", 1, 1)
		assertAlertDeliveryIntegrationAbsent(t, result, alertStatsRetiredOnlyID)
	}
	ownedFailures := requestAlertDeliveryStats(t, db, "operator", alertStatsOperatorID, 24)
	assertAlertDeliveryStatsTotals(t, ownedFailures, 7, 3, 70)
	assertAlertDeliveryIntegration(t, ownedFailures, alertStatsIntegrationAID, "stats-a", "webhook", 3, 2)
	assertAlertDeliveryIntegration(t, ownedFailures, alertStatsIntegrationBID, "stats-b", "slack", 3, 1)
	assertAlertDeliveryIntegrationAbsent(t, ownedFailures, 9099)
	assertAlertDeliveryIntegrationAbsent(t, ownedFailures, alertStatsRetiredOnlyID)
	noOwnedFailures := requestAlertDeliveryStats(t, db, "operator", alertStatsNoOwnerUserID, 24)
	assertAlertDeliveryStatsTotals(t, noOwnedFailures, 0, 0, 0)
	if len(noOwnedFailures.ByIntegration) != 0 {
		t.Fatalf("operator without owned nodes returned failure integration rows: %+v", noOwnedFailures.ByIntegration)
	}
}

func requestAlertDeliveryStats(t *testing.T, db *gorm.DB, role string, userID uint, hours int) deliveryStatsResponse {
	t.Helper()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		if userID != 0 {
			c.Set(middleware.CtxUserID, userID)
		}
		c.Next()
	})
	r.GET("/alerts/delivery-stats", NewAlertHandler(db).DeliveryStats)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/alerts/delivery-stats?hours=%d", hours), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("delivery stats role=%s hours=%d status=%d body=%s", role, hours, resp.Code, resp.Body.String())
	}
	var result struct {
		Data deliveryStatsResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode delivery stats role=%s hours=%d: %v", role, hours, err)
	}
	return result.Data
}

func assertAlertDeliveryStatsTotals(t *testing.T, got deliveryStatsResponse, sent, failed int64, successRate float64) {
	t.Helper()
	if got.TotalSent != sent || got.TotalFailed != failed || got.SuccessRate != successRate {
		t.Fatalf("delivery stats totals = sent:%d failed:%d rate:%.1f, want sent:%d failed:%d rate:%.1f", got.TotalSent, got.TotalFailed, got.SuccessRate, sent, failed, successRate)
	}
}

func assertAlertDeliveryIntegration(t *testing.T, got deliveryStatsResponse, integrationID uint, name, integrationType string, sent, failed int64) {
	t.Helper()
	for _, row := range got.ByIntegration {
		if row.IntegrationID != integrationID {
			continue
		}
		if row.Name != name || row.Type != integrationType || row.Sent != sent || row.Failed != failed {
			t.Fatalf("integration %d = name:%q type:%q sent:%d failed:%d, want name:%q type:%q sent:%d failed:%d", integrationID, row.Name, row.Type, row.Sent, row.Failed, name, integrationType, sent, failed)
		}
		return
	}
	t.Fatalf("integration %d missing from delivery stats", integrationID)
}

func assertAlertDeliveryIntegrationAbsent(t *testing.T, got deliveryStatsResponse, integrationID uint) {
	t.Helper()
	for _, row := range got.ByIntegration {
		if row.IntegrationID == integrationID {
			t.Fatalf("integration %d unexpectedly present with sent:%d failed:%d", integrationID, row.Sent, row.Failed)
		}
	}
}
