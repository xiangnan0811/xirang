package alerting

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

func durableTestAlert() model.Alert {
	return model.Alert{
		NodeID:           0,
		NodeName:         "durable-node",
		Severity:         "critical",
		Status:           "open",
		ErrorCode:        "XR-DURABLE-1",
		Message:          "durable delivery test",
		TriggeredAt:      time.Now(),
		DeliveryDecision: model.AlertDeliveryDecisionPending,
	}
}

func durableTestIntegration(name string) model.Integration {
	return model.Integration{
		Name:            name,
		Type:            "webhook",
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 0,
		Endpoint:        "",
	}
}

func setupDurableTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	SetSharedGroupingForTest(NewGrouping(5 * time.Minute))
	return setupTestDB(t)
}
func durableAlertsTotalValue(t *testing.T, severity string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather alert metric: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "xirang_alerts_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "severity" && label.GetValue() == severity {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestAlertsTotalIncrementsOnlyOnCreateAndNotReplay(t *testing.T) {
	db := setupDurableTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("migrate task/run tables: %v", err)
	}
	task := model.Task{Name: "metric-task", NodeID: 1, Status: "failed", Enabled: true}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	now := time.Now()
	run := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID,
		TriggerType: "manual", Status: model.TaskRunStatusFailed, FinishedAt: &now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create failed run: %v", err)
	}
	dispatcher := NewDispatcher(db, nil, nil)
	before := durableAlertsTotalValue(t, "critical")
	if err := dispatcher.RaiseTaskFailureForRun(task, run.ID, "causal metric failure"); err != nil {
		t.Fatalf("create causal failure alert: %v", err)
	}
	afterCreate := durableAlertsTotalValue(t, "critical")
	if afterCreate-before != 1 {
		t.Fatalf("alertsTotal delta after causal create=%v, want 1", afterCreate-before)
	}
	if err := dispatcher.RaiseTaskFailureForRun(task, run.ID, "causal metric replay"); err != nil {
		t.Fatalf("replay causal failure alert: %v", err)
	}
	afterReplay := durableAlertsTotalValue(t, "critical")
	if afterReplay != afterCreate {
		t.Fatalf("alertsTotal changed on causal replay: before=%v after=%v", afterCreate, afterReplay)
	}
	var count int64
	if err := db.Model(&model.Alert{}).Where("task_id = ? AND task_run_id = ?", task.ID, run.ID).Count(&count).Error; err != nil {
		t.Fatalf("count causal alerts: %v", err)
	}
	if count != 1 {
		t.Fatalf("causal replay created %d alerts, want one", count)
	}
}

func TestDurableRoutingDecisionsPersistGroupingAndThreshold(t *testing.T) {
	t.Run("grouping", func(t *testing.T) {
		db := setupDurableTestDB(t)
		integration := durableTestIntegration("decision-grouping")
		if err := db.Create(&integration).Error; err != nil {
			t.Fatalf("create integration: %v", err)
		}
		dispatcher := NewDispatcher(db, nil, nil)
		first := durableTestAlert()
		if err := db.Create(&first).Error; err != nil {
			t.Fatalf("create first alert: %v", err)
		}
		sendFn := func(model.Integration, model.Alert) error { return nil }
		if err := dispatcher.dispatchCreatedAlertWithSender(&first, sendFn); err != nil {
			t.Fatalf("dispatch first grouped alert: %v", err)
		}
		second := durableTestAlert()
		if err := db.Create(&second).Error; err != nil {
			t.Fatalf("create second alert: %v", err)
		}
		if err := dispatcher.dispatchCreatedAlertWithSender(&second, sendFn); err != nil {
			t.Fatalf("dispatch grouped alert: %v", err)
		}
		var persisted model.Alert
		if err := db.First(&persisted, second.ID).Error; err != nil {
			t.Fatalf("reload grouped alert: %v", err)
		}
		if persisted.DeliveryDecision != model.AlertDeliveryDecisionSuppressed ||
			persisted.DeliveryReason != model.AlertDeliveryReasonGrouping {
			t.Fatalf("grouped decision=%q reason=%q, want suppressed/grouping", persisted.DeliveryDecision, persisted.DeliveryReason)
		}
		var count int64
		db.Model(&model.AlertDelivery{}).Where("alert_id = ?", second.ID).Count(&count)
		if count != 0 {
			t.Fatalf("grouped alert created %d intents", count)
		}
	})

	t.Run("threshold", func(t *testing.T) {
		db := setupDurableTestDB(t)
		integration := durableTestIntegration("decision-threshold")
		integration.FailThreshold = 2
		if err := db.Create(&integration).Error; err != nil {
			t.Fatalf("create threshold integration: %v", err)
		}
		dispatcher := NewDispatcher(db, nil, nil)
		alert := durableTestAlert()
		if err := db.Create(&alert).Error; err != nil {
			t.Fatalf("create threshold alert: %v", err)
		}
		if err := dispatcher.dispatchCreatedAlertWithSender(&alert, func(model.Integration, model.Alert) error { return nil }); err != nil {
			t.Fatalf("dispatch threshold alert: %v", err)
		}
		var persisted model.Alert
		if err := db.First(&persisted, alert.ID).Error; err != nil {
			t.Fatalf("reload threshold alert: %v", err)
		}
		if persisted.DeliveryDecision != model.AlertDeliveryDecisionSuppressed ||
			persisted.DeliveryReason != model.AlertDeliveryReasonThresholdOrCooldown {
			t.Fatalf("threshold decision=%q reason=%q, want suppressed/threshold_or_cooldown", persisted.DeliveryDecision, persisted.DeliveryReason)
		}
	})
}

func TestGroupingCommitFailureReplaysSameFirstAlert(t *testing.T) {
	db := setupDurableTestDB(t)
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	integration := durableTestIntegration("grouping-commit-replay")
	integration.Endpoint = server.URL
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	dispatcher := NewDispatcher(db, nil, nil)
	SetDispatcher(dispatcher)
	t.Cleanup(func() { SetDispatcher(nil) })

	var failed atomic.Bool
	const callbackName = "test:fail-grouping-decision-once"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "alerts" {
			return
		}
		values, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok {
			return
		}
		decision, ok := values["delivery_decision"].(string)
		if ok && decision == model.AlertDeliveryDecisionDirect && failed.CompareAndSwap(false, true) {
			_ = tx.AddError(errors.New("injected grouping decision persistence failure"))
		}
	}); err != nil {
		t.Fatalf("register decision fault: %v", err)
	}
	if err := dispatcher.dispatchCreatedAlert(&alert); err == nil {
		t.Fatal("expected injected decision commit failure")
	}

	_ = db.Callback().Update().Remove(callbackName)
	if !failed.Load() {
		t.Fatal("decision fault callback did not run")
	}

	var pending model.Alert
	if err := db.First(&pending, alert.ID).Error; err != nil {
		t.Fatalf("reload pending alert: %v", err)
	}
	if pending.DeliveryDecision != model.AlertDeliveryDecisionPending {
		t.Fatalf("decision=%q, want pending after rollback", pending.DeliveryDecision)
	}
	var before int64
	if err := db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&before).Error; err != nil {
		t.Fatalf("count rolled-back intents: %v", err)
	}
	if before != 0 {
		t.Fatalf("rolled-back decision left %d intents", before)
	}

	worker := NewRetryWorker(db)
	worker.tick(context.Background(), time.Now())
	if got := sends.Load(); got != 1 {
		t.Fatalf("same-alert replay sends=%d, want one", got)
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ? AND integration_id = ?", alert.ID, integration.ID).First(&intent).Error; err != nil {
		t.Fatalf("load replay intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSent || intent.AttemptCount != 1 || intent.SentAt == nil {
		t.Fatalf("replay intent=%+v, want sent once with success timestamp", intent)
	}
	if got := GetSharedGrouping().Count(GroupKey(alert.ErrorCode, alert.NodeID, nil)); got != 1 {
		t.Fatalf("same-alert replay changed grouping count=%d, want 1", got)
	}
}

func TestDurableFailedSendLeavesRetryableIntent(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("failed-send-intent")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	dispatcher := NewDispatcher(db, nil, nil)
	go func() {
		finished <- dispatcher.dispatchCreatedAlertWithSender(&alert, func(model.Integration, model.Alert) error {
			close(started)
			<-release
			return errors.New("transport failed after intent commit")
		})
	}()
	<-started
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load sending intent before fault: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSending || intent.DeliveryKey == "" {
		t.Fatalf("intent=%+v, want sending with key before send fault", intent)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("dispatch should persist send failure, got %v", err)
	}
	if err := db.First(&intent, intent.ID).Error; err != nil {
		t.Fatalf("reload failed intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusRetrying || intent.AttemptCount != 1 || intent.LastError == "" {
		t.Fatalf("failed intent=%+v, want retrying attempt 1 with error", intent)
	}
}

func TestLegacyBlankDeliveryWithEscalationHistoryRemainsUnknown(t *testing.T) {
	db := setupDurableTestDB(t)
	if err := db.AutoMigrate(&model.AlertEscalationEvent{}); err != nil {
		t.Fatalf("migrate escalation event table: %v", err)
	}
	integration := durableTestIntegration("legacy-escalation-unknown")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	alert.DeliveryDecision = ""
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	now := time.Now()
	if err := db.Create(&model.AlertEscalationEvent{
		AlertID:        alert.ID,
		LevelIndex:     0,
		IntegrationIDs: "[]",
		SeverityBefore: alert.Severity,
		SeverityAfter:  alert.Severity,
		FiredAt:        now,
	}).Error; err != nil {
		t.Fatalf("create escalation event: %v", err)
	}
	past := now.Add(-time.Minute)
	intent := model.AlertDelivery{
		AlertID:       alert.ID,
		IntegrationID: integration.ID,
		Status:        model.AlertDeliveryStatusRetrying,
		Decision:      "deliver",
		NextRetryAt:   &past,
		AttemptCount:  1,
	}
	if err := db.Create(&intent).Error; err != nil {
		t.Fatalf("create legacy delivery: %v", err)
	}
	var sends atomic.Int64
	err := runDeliveryAttempt(context.Background(), db, intent, func(model.Integration, model.Alert) error {
		sends.Add(1)
		return nil
	}, false)
	if err != nil {
		t.Fatalf("unknown legacy delivery attempt: %v", err)
	}
	if sends.Load() != 0 {
		t.Fatalf("unknown legacy delivery was sent %d times", sends.Load())
	}
	if err := db.First(&intent, intent.ID).Error; err != nil {
		t.Fatalf("reload legacy delivery: %v", err)
	}
	if intent.DeliveryKey != "" || intent.Decision != model.AlertDeliveryDecisionUnknown ||
		intent.Status != model.AlertDeliveryStatusRetrying || intent.AttemptCount != 1 {
		t.Fatalf("legacy escalation delivery was rewritten: %+v", intent)
	}
}

func TestDurableCommittedIntentMissingIntegrationFails(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("deleted-before-send")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	dispatcher := NewDispatcher(db, nil, nil)
	if decision, err := dispatcher.prepareDeliveryDecision(&alert); err != nil || decision != model.AlertDeliveryDecisionDirect {
		t.Fatalf("prepare decision=%q err=%v, want direct", decision, err)
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load committed intent: %v", err)
	}
	if err := db.Delete(&integration).Error; err != nil {
		t.Fatalf("delete integration: %v", err)
	}
	worker := NewRetryWorker(db)
	worker.tick(context.Background(), time.Now())
	if err := db.First(&intent, intent.ID).Error; err != nil {
		t.Fatalf("reload deleted-integration intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusFailed || intent.LastError != "integration deleted" {
		t.Fatalf("deleted-integration intent=%+v, want terminal load failure", intent)
	}
}
func TestDurableIntegrationLookupFailureReplaysPendingAlert(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("transient-integration-query")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	var failed atomic.Bool
	const callbackName = "test:fail-integration-query-once"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		table := tx.Statement.Table
		if table == "integrations" && failed.CompareAndSwap(false, true) {
			_ = tx.AddError(errors.New("transient integration lookup failure"))
		}
	}); err != nil {
		t.Fatalf("register integration query fault: %v", err)
	}

	dispatcher := NewDispatcher(db, nil, nil)
	if err := dispatcher.raiseAndDispatch(&alert); err == nil {
		t.Fatal("expected transient integration lookup failure")
	}
	_ = db.Callback().Query().Remove(callbackName)
	if !failed.Load() {
		t.Fatal("integration query fault callback did not run")
	}
	var persisted model.Alert
	if err := db.First(&persisted, alert.ID).Error; err != nil {
		t.Fatalf("load committed pending alert: %v", err)
	}
	if persisted.DeliveryDecision != model.AlertDeliveryDecisionPending {
		t.Fatalf("decision=%q, want pending after transient lookup failure", persisted.DeliveryDecision)
	}
	var before int64
	db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&before)
	if before != 0 {
		t.Fatalf("transient lookup failure created %d intents before routing completed", before)
	}

	var sends int64
	worker := NewRetryWorker(db)
	worker.sendFn = func(model.Integration, model.Alert) error {
		atomic.AddInt64(&sends, 1)
		return nil
	}
	worker.tick(context.Background(), time.Now())
	if got := atomic.LoadInt64(&sends); got != 1 {
		t.Fatalf("replay sends=%d, want one", got)
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load replay intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSent || intent.AttemptCount != 1 {
		t.Fatalf("replay intent=%+v, want sent once", intent)
	}
}

func TestDurableReceiptUpdateFailureLeavesIntentForReclaim(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("receipt-update-failure")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	dispatcher := NewDispatcher(db, nil, nil)
	if decision, err := dispatcher.prepareDeliveryDecision(&alert); err != nil || decision != model.AlertDeliveryDecisionDirect {
		t.Fatalf("prepare decision=%q err=%v, want direct", decision, err)
	}
	var callbackFailed atomic.Bool
	const callbackName = "test:fail-delivery-receipt-once"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "alert_deliveries" {
			return
		}
		values, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok {
			return
		}
		status, ok := values["status"].(string)
		if ok && status == model.AlertDeliveryStatusSent && callbackFailed.CompareAndSwap(false, true) {
			_ = tx.AddError(errors.New("injected receipt persistence failure"))
		}
	}); err != nil {
		t.Fatalf("register receipt update fault: %v", err)
	}
	defer func() { _ = db.Callback().Update().Remove(callbackName) }()

	var sends int64
	sendFn := func(model.Integration, model.Alert) error {
		atomic.AddInt64(&sends, 1)
		return nil
	}
	if err := dispatcher.dispatchCreatedAlertWithSender(&alert, sendFn); err != nil {
		t.Fatalf("dispatch with injected receipt fault: %v", err)
	}
	_ = db.Callback().Update().Remove(callbackName)
	if !callbackFailed.Load() {
		t.Fatal("receipt update fault callback did not run")
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load leased intent after receipt fault: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSending || intent.AttemptCount != 1 {
		t.Fatalf("intent=%+v, want sending lease after uncertain receipt", intent)
	}
	if err := db.Model(&model.AlertDelivery{}).Where("id = ?", intent.ID).
		Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire uncertain lease: %v", err)
	}
	worker := NewRetryWorker(db)
	worker.sendFn = sendFn
	worker.tick(context.Background(), time.Now())
	if got := atomic.LoadInt64(&sends); got != 2 {
		t.Fatalf("reclaim sends=%d, want two possible sends under uncertain receipt", got)
	}
	if err := db.First(&intent, intent.ID).Error; err != nil {
		t.Fatalf("reload reclaimed intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSent || intent.AttemptCount != 2 {
		t.Fatalf("reclaimed intent=%+v, want sent attempt 2", intent)
	}
}

func TestExpiredLeaseCannotRecordSuccessWithoutTakeover(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("expired-success-cas")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	dispatcher := NewDispatcher(db, nil, nil)
	if decision, err := dispatcher.prepareDeliveryDecision(&alert); err != nil || decision != model.AlertDeliveryDecisionDirect {
		t.Fatalf("prepare decision=%q err=%v, want direct", decision, err)
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load intent: %v", err)
	}
	claimed, ok, err := claimDelivery(context.Background(), db, intent.ID, time.Now(), false)
	if err != nil || !ok {
		t.Fatalf("claim intent: ok=%v err=%v", ok, err)
	}
	expired := time.Now().Add(-time.Minute)
	if err := db.Model(&model.AlertDelivery{}).Where("id = ?", claimed.ID).
		Update("lease_expires_at", expired).Error; err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if err := completeDelivery(context.Background(), db, claimed.ID, claimed.AttemptID, model.AlertDeliveryStatusSent, nil, ""); err != nil {
		t.Fatalf("expired completion returned error: %v", err)
	}
	if err := db.First(&intent, claimed.ID).Error; err != nil {
		t.Fatalf("reload expired intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSending || intent.SentAt != nil {
		t.Fatalf("expired completion changed durable state: %+v", intent)
	}
}

func TestChannelHTTP200BusinessFailureLeavesDeliveryUnsent(t *testing.T) {
	db := setupDurableTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":123}`))
	}))
	defer server.Close()

	integration := durableTestIntegration("feishu-business-failure")
	integration.Type = "feishu"
	integration.Endpoint = server.URL
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	dispatcher := NewDispatcher(db, nil, nil)
	if decision, err := dispatcher.prepareDeliveryDecision(&alert); err != nil || decision != model.AlertDeliveryDecisionDirect {
		t.Fatalf("prepare decision=%q err=%v, want direct", decision, err)
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ? AND integration_id = ?", alert.ID, integration.ID).First(&intent).Error; err != nil {
		t.Fatalf("load intent: %v", err)
	}
	if err := runDeliveryAttempt(context.Background(), db, intent, dispatcher.send, false); err != nil {
		t.Fatalf("run delivery attempt: %v", err)
	}
	if err := db.First(&intent, intent.ID).Error; err != nil {
		t.Fatalf("reload intent: %v", err)
	}
	if intent.Status == model.AlertDeliveryStatusSent || intent.SentAt != nil {
		t.Fatalf("business failure marked delivery sent: %+v", intent)
	}
	if !strings.Contains(intent.LastError, "provider") {
		t.Fatalf("delivery error=%q, want provider acknowledgement failure", intent.LastError)
	}
}

func TestAlertingDurableDeliveryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openCausalTaskAlertPostgresDB(t, dsn)
	SetSharedGroupingForTest(NewGrouping(5 * time.Minute))

	integration := durableTestIntegration("durable-postgres")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	var sends int64
	dispatcher := NewDispatcher(db, nil, nil)
	sendFn := func(model.Integration, model.Alert) error {
		atomic.AddInt64(&sends, 1)
		return nil
	}
	if err := dispatcher.dispatchCreatedAlertWithSender(&alert, sendFn); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if err := dispatcher.dispatchCreatedAlertWithSender(&alert, sendFn); err != nil {
		t.Fatalf("replay dispatch: %v", err)
	}
	if got := atomic.LoadInt64(&sends); got != 1 {
		t.Fatalf("send calls=%d, want one", got)
	}
	var persisted model.AlertDelivery
	if err := db.Where("alert_id = ? AND integration_id = ?", alert.ID, integration.ID).First(&persisted).Error; err != nil {
		t.Fatalf("load delivery: %v", err)
	}
	if persisted.Status != model.AlertDeliveryStatusSent || persisted.AttemptCount != 1 || persisted.SentAt == nil {
		t.Fatalf("delivery=%+v, want sent after one attempt with success timestamp", persisted)
	}
}
func TestAlertingDeliveryConcurrencyPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openCausalTaskAlertPostgresDB(t, dsn)
	integration := durableTestIntegration("durable-postgres-concurrency")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	past := time.Now().Add(-time.Second)
	intent := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID,
		Status: model.AlertDeliveryStatusRetrying, Decision: "deliver", NextRetryAt: &past,
	}
	if err := db.Create(&intent).Error; err != nil {
		t.Fatalf("create retry intent: %v", err)
	}
	worker := NewRetryWorker(db)
	var sends int32
	started := make(chan struct{})
	release := make(chan struct{})
	worker.sendFn = func(model.Integration, model.Alert) error {
		if atomic.AddInt32(&sends, 1) == 1 {
			close(started)
			<-release
		}
		return nil
	}
	var candidate model.AlertDelivery
	if err := db.First(&candidate, intent.ID).Error; err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); worker.attempt(context.Background(), candidate) }()
	<-started
	go func() { defer wg.Done(); worker.attempt(context.Background(), candidate) }()
	close(release)
	wg.Wait()
	if got := atomic.LoadInt32(&sends); got != 1 {
		t.Fatalf("send calls=%d, want one claim winner", got)
	}
	var persisted model.AlertDelivery
	if err := db.First(&persisted, intent.ID).Error; err != nil {
		t.Fatalf("reload intent: %v", err)
	}
	if persisted.Status != model.AlertDeliveryStatusSent || persisted.AttemptCount != 1 {
		t.Fatalf("intent=%+v, want sent and one attempt", persisted)
	}

	now := time.Now()
	expired := now.Add(-time.Minute)
	stale := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID,
		Status: model.AlertDeliveryStatusSending, Decision: "deliver",
		AttemptCount: 1, AttemptID: "stale-pg-attempt", LeaseExpiresAt: &expired,
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatalf("create stale intent: %v", err)
	}
	claimed, ok, err := claimDelivery(context.Background(), db, stale.ID, now, false)
	if err != nil || !ok {
		t.Fatalf("take over stale intent: ok=%v err=%v", ok, err)
	}
	if claimed.AttemptID == stale.AttemptID || claimed.AttemptCount != 2 {
		t.Fatalf("replacement claim=%+v, want new token and attempt 2", claimed)
	}
	if err := completeDelivery(context.Background(), db, stale.ID, stale.AttemptID, model.AlertDeliveryStatusFailed, nil, "stale failure"); err != nil {
		t.Fatalf("stale completion: %v", err)
	}
	var stalePersisted model.AlertDelivery
	if err := db.First(&stalePersisted, stale.ID).Error; err != nil {
		t.Fatalf("reload stale intent: %v", err)
	}
	if stalePersisted.Status != model.AlertDeliveryStatusSending || stalePersisted.AttemptID != claimed.AttemptID {
		t.Fatalf("stale completion overwrote replacement: %+v", stalePersisted)
	}
}
func TestAlertingDeliveryLegacyBlankAutoManualConcurrencyPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openCausalTaskAlertPostgresDB(t, dsn)
	dispatcher := NewDispatcher(db, nil, nil)

	var requests int32
	firstRequestStarted := make(chan struct{})
	releaseFirstRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			close(firstRequestStarted)
			<-releaseFirstRequest
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	integration := durableTestIntegration("legacy-blank-auto-manual-pg")
	integration.Endpoint = server.URL
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	past := time.Now().Add(-time.Second)
	direct := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID,
		DeliveryKey: deliveryIntentKey(alert.ID, integration.ID),
		Status:      model.AlertDeliveryStatusRetrying, Decision: "deliver",
		NextRetryAt: &past,
	}
	blankOne := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID,
		Status: model.AlertDeliveryStatusRetrying, Decision: "deliver",
		NextRetryAt: &past,
	}
	blankTwo := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID,
		Status: model.AlertDeliveryStatusRetrying, Decision: "deliver",
		NextRetryAt: &past,
	}
	if err := db.Create(&direct).Error; err != nil {
		t.Fatalf("create direct delivery: %v", err)
	}
	if err := db.Create(&blankOne).Error; err != nil {
		t.Fatalf("create first blank delivery: %v", err)
	}
	if err := db.Create(&blankTwo).Error; err != nil {
		t.Fatalf("create second blank delivery: %v", err)
	}
	var candidate model.AlertDelivery
	if err := db.First(&candidate, blankOne.ID).Error; err != nil {
		t.Fatalf("load blank delivery: %v", err)
	}

	worker := NewRetryWorker(db)
	var manualErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		worker.attempt(context.Background(), candidate)
	}()
	<-firstRequestStarted

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, manualErr = dispatcher.RetryDeliveryByID(context.Background(), blankOne.ID)
	}()
	close(releaseFirstRequest)
	wg.Wait()
	if manualErr != nil && !errors.Is(manualErr, errDeliveryAlreadySent) {
		t.Fatalf("manual retry: %v", manualErr)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("concurrent auto/manual sends=%d, want one", got)
	}

	var persistedDirect model.AlertDelivery
	if err := db.First(&persistedDirect, direct.ID).Error; err != nil {
		t.Fatalf("reload direct delivery: %v", err)
	}
	if persistedDirect.Status != model.AlertDeliveryStatusSent || persistedDirect.AttemptCount != 1 {
		t.Fatalf("direct delivery=%+v, want one sent attempt", persistedDirect)
	}
	var persistedBlanks []model.AlertDelivery
	if err := db.Where("id IN ?", []uint{blankOne.ID, blankTwo.ID}).Find(&persistedBlanks).Error; err != nil {
		t.Fatalf("reload blank deliveries: %v", err)
	}
	for _, row := range persistedBlanks {
		if strings.TrimSpace(row.DeliveryKey) != "" {
			t.Fatalf("blank duplicate was merged into canonical key: %+v", row)
		}
	}

	eventOne := model.AlertEscalationEvent{
		AlertID: alert.ID, LevelIndex: 0, IntegrationIDs: "[]",
		SeverityBefore: alert.Severity, SeverityAfter: alert.Severity,
		TagsAdded: "[]", FiredAt: time.Now(),
	}
	eventTwo := model.AlertEscalationEvent{
		AlertID: alert.ID, LevelIndex: 1, IntegrationIDs: "[]",
		SeverityBefore: alert.Severity, SeverityAfter: alert.Severity,
		TagsAdded: "[]", FiredAt: time.Now(),
	}
	if err := db.Create(&eventOne).Error; err != nil {
		t.Fatalf("create first escalation event: %v", err)
	}
	if err := db.Create(&eventTwo).Error; err != nil {
		t.Fatalf("create second escalation event: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := dispatcher.EnqueueEscalationDeliveriesTx(tx, alert, eventOne, []uint{integration.ID}); err != nil {
			return err
		}
		_, err := dispatcher.EnqueueEscalationDeliveriesTx(tx, alert, eventTwo, []uint{integration.ID})
		return err
	}); err != nil {
		t.Fatalf("enqueue event-scoped deliveries: %v", err)
	}
	var eventRows []model.AlertDelivery
	if err := db.Where("alert_id = ? AND integration_id = ?", alert.ID, integration.ID).Find(&eventRows).Error; err != nil {
		t.Fatalf("reload event deliveries: %v", err)
	}
	wantKeys := map[string]bool{
		escalationDeliveryIntentKey(alert.ID, eventOne.ID, integration.ID): false,
		escalationDeliveryIntentKey(alert.ID, eventTwo.ID, integration.ID): false,
	}
	for _, row := range eventRows {
		if _, ok := wantKeys[row.DeliveryKey]; ok {
			wantKeys[row.DeliveryKey] = true
		}
	}
	for key, found := range wantKeys {
		if !found {
			t.Fatalf("missing distinct event delivery key %q in %+v", key, eventRows)
		}
	}
}

func TestAlertingDeliveryClaimLoadFenceSQLite(t *testing.T) {
	testAlertingDeliveryClaimLoadFence(t, setupDurableTestDB(t))
}

func TestAlertingDeliveryClaimLoadFencePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	testAlertingDeliveryClaimLoadFence(t, openCausalTaskAlertPostgresDB(t, dsn))
}

func testAlertingDeliveryClaimLoadFence(t *testing.T, db *gorm.DB) {
	t.Helper()
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	integration := durableTestIntegration("claim-load-fence")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	intent := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID,
		Status: model.AlertDeliveryStatusPending, Decision: "deliver",
	}
	if err := db.Create(&intent).Error; err != nil {
		t.Fatalf("create intent: %v", err)
	}
	now := time.Now()
	oldLease := now.Add(time.Minute)
	if err := db.Model(&model.AlertDelivery{}).Where("id = ?", intent.ID).Updates(map[string]interface{}{
		"status": model.AlertDeliveryStatusSending, "attempt_id": "old-attempt",
		"lease_expires_at": oldLease, "attempt_count": 1,
	}).Error; err != nil {
		t.Fatalf("seed old claim: %v", err)
	}
	newLease := now.Add(2 * time.Minute)
	if err := db.Model(&model.AlertDelivery{}).Where("id = ?", intent.ID).Updates(map[string]interface{}{
		"attempt_id": "replacement-attempt", "lease_expires_at": newLease,
		"attempt_count": 2,
	}).Error; err != nil {
		t.Fatalf("simulate takeover: %v", err)
	}
	loaded, ok, err := loadClaimedDelivery(context.Background(), db, intent.ID, "old-attempt", now)
	if err != nil {
		t.Fatalf("load old claim: %v", err)
	}
	if ok || loaded.ID != 0 {
		t.Fatalf("stale claim loaded after takeover: ok=%v loaded=%+v", ok, loaded)
	}
}

func TestDurableDeliveryIntentCommittedBeforeSend(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("durable-before-send")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	dispatcher := NewDispatcher(db, nil, nil)
	go func() {
		finished <- dispatcher.dispatchCreatedAlertWithSender(&alert, func(model.Integration, model.Alert) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	var intent model.AlertDelivery
	if err := db.Where("alert_id = ? AND integration_id = ?", alert.ID, integration.ID).First(&intent).Error; err != nil {
		t.Fatalf("delivery intent was not committed before send: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSending || intent.AttemptCount != 1 {
		t.Fatalf("intent=%+v, want sending with one claimed attempt", intent)
	}
	if intent.DeliveryKey != deliveryIntentKey(alert.ID, integration.ID) || intent.AttemptID == "" {
		t.Fatalf("intent=%+v, want durable key and attempt token", intent)
	}

	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := db.First(&intent, intent.ID).Error; err != nil {
		t.Fatalf("reload intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSent {
		t.Fatalf("intent status=%q, want sent", intent.Status)
	}
}

func TestRetryWorkerReplaysPendingAlertAfterRestart(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("durable-replay")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create pending alert: %v", err)
	}

	var intents int64
	worker := NewRetryWorker(db)
	worker.sendFn = func(model.Integration, model.Alert) error {
		atomic.AddInt64(&intents, 1)
		return nil
	}
	worker.tick(context.Background(), time.Now())

	if got := atomic.LoadInt64(&intents); got != 1 {
		t.Fatalf("send calls=%d, want one replay", got)
	}
	var persisted model.Alert
	if err := db.First(&persisted, alert.ID).Error; err != nil {
		t.Fatalf("reload alert: %v", err)
	}
	if persisted.DeliveryDecision != model.AlertDeliveryDecisionDirect {
		t.Fatalf("decision=%q, want direct", persisted.DeliveryDecision)
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load replay intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSent {
		t.Fatalf("replay intent status=%q, want sent", intent.Status)
	}
}

func TestHistoricalNullDecisionIsNotReplayed(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("historical-null")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	alert.DeliveryDecision = ""
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create historical alert: %v", err)
	}

	var sends int64
	worker := NewRetryWorker(db)
	worker.sendFn = func(model.Integration, model.Alert) error {
		atomic.AddInt64(&sends, 1)
		return nil
	}
	worker.tick(context.Background(), time.Now())

	if got := atomic.LoadInt64(&sends); got != 0 {
		t.Fatalf("historical NULL decision was replayed, sends=%d", got)
	}
	var count int64
	if err := db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&count).Error; err != nil {
		t.Fatalf("count historical intents: %v", err)
	}
	if count != 0 {
		t.Fatalf("historical NULL decision created %d intents", count)
	}
}

func TestDurableTerminalDecisionsSurviveReplay(t *testing.T) {
	tests := []struct {
		name        string
		integration bool
		decision    string
		reason      string
		resolver    EscalationResolverFn
	}{
		{name: "no_channel", integration: false, decision: model.AlertDeliveryDecisionNoChannel, reason: model.AlertDeliveryReasonNoEnabledChannel},
		{name: "escalated", integration: true, decision: model.AlertDeliveryDecisionEscalated, reason: model.AlertDeliveryReasonEscalation,
			resolver: func(model.Alert) (*EscalationPolicySummary, error) {
				return &EscalationPolicySummary{Enabled: true, MinSeverity: "warning"}, nil
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupDurableTestDB(t)
			if tc.integration {
				integration := durableTestIntegration("terminal-" + tc.name)
				if err := db.Create(&integration).Error; err != nil {
					t.Fatalf("create integration: %v", err)
				}
			}
			alert := durableTestAlert()
			if err := db.Create(&alert).Error; err != nil {
				t.Fatalf("create alert: %v", err)
			}
			dispatcher := NewDispatcher(db, nil, tc.resolver)
			if err := dispatcher.dispatchCreatedAlert(&alert); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			var persisted model.Alert
			if err := db.First(&persisted, alert.ID).Error; err != nil {
				t.Fatalf("reload alert: %v", err)
			}
			if persisted.DeliveryDecision != tc.decision || persisted.DeliveryReason != tc.reason {
				t.Fatalf("decision=%q reason=%q, want %q/%q", persisted.DeliveryDecision, persisted.DeliveryReason, tc.decision, tc.reason)
			}
			var before int64
			db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&before)
			worker := NewRetryWorker(db)
			worker.sendFn = func(model.Integration, model.Alert) error {
				t.Fatal("terminal decision was replayed")
				return nil
			}
			worker.tick(context.Background(), time.Now())
			var after int64
			db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&after)
			if after != before {
				t.Fatalf("terminal replay changed intent count %d -> %d", before, after)
			}
		})
	}
}

func TestResolvedPendingAlertIsDeliveredWithoutReopening(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("resolved-pending")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	alert.Status = "resolved"
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create resolved pending alert: %v", err)
	}
	worker := NewRetryWorker(db)
	worker.sendFn = func(model.Integration, model.Alert) error { return nil }
	worker.tick(context.Background(), time.Now())

	var persisted model.Alert
	if err := db.First(&persisted, alert.ID).Error; err != nil {
		t.Fatalf("reload alert: %v", err)
	}
	if persisted.Status != "resolved" {
		t.Fatalf("replay reopened alert with status=%q", persisted.Status)
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load delivery: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSent {
		t.Fatalf("delivery status=%q, want sent", intent.Status)
	}
}

func TestConfirmedSentDeliveryIsNotRepeated(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("confirmed-sent")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	var sends int64
	dispatcher := NewDispatcher(db, nil, nil)
	sendFn := func(model.Integration, model.Alert) error {
		atomic.AddInt64(&sends, 1)
		return nil
	}
	if err := dispatcher.dispatchCreatedAlertWithSender(&alert, sendFn); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if err := dispatcher.dispatchCreatedAlertWithSender(&alert, sendFn); err != nil {
		t.Fatalf("replay dispatch: %v", err)
	}
	if got := atomic.LoadInt64(&sends); got != 1 {
		t.Fatalf("send calls=%d, want one confirmed send", got)
	}
	var count int64
	db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&count)
	if count != 1 {
		t.Fatalf("intent count=%d, want one", count)
	}
}

func TestDeliveryClaimPreventsConcurrentSends(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("claim-concurrency")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	past := time.Now().Add(-time.Second)
	intent := model.AlertDelivery{AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusRetrying, Decision: "deliver", NextRetryAt: &past}
	if err := db.Create(&intent).Error; err != nil {
		t.Fatalf("create retry intent: %v", err)
	}
	worker := NewRetryWorker(db)
	started := make(chan struct{})
	release := make(chan struct{})
	var sends int32
	worker.sendFn = func(model.Integration, model.Alert) error {
		if atomic.AddInt32(&sends, 1) == 1 {
			close(started)
			<-release
		}
		return nil
	}
	var candidate model.AlertDelivery
	if err := db.First(&candidate, intent.ID).Error; err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); worker.attempt(context.Background(), candidate) }()
	<-started
	go func() { defer wg.Done(); worker.attempt(context.Background(), candidate) }()
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&sends); got != 1 {
		t.Fatalf("send calls=%d, want one claim winner", got)
	}
	var persisted model.AlertDelivery
	if err := db.First(&persisted, intent.ID).Error; err != nil {
		t.Fatalf("reload intent: %v", err)
	}
	if persisted.Status != model.AlertDeliveryStatusSent || persisted.AttemptCount != 1 {
		t.Fatalf("intent=%+v, want sent and one attempt", persisted)
	}
}

func TestStaleDeliveryCompletionCannotOverwriteNewLease(t *testing.T) {
	db := setupDurableTestDB(t)
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	integration := durableTestIntegration("stale-completion")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	now := time.Now()
	expired := now.Add(-time.Minute)
	intent := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusSending,
		Decision: "deliver", AttemptCount: 1, AttemptID: "stale-attempt", LeaseExpiresAt: &expired,
	}
	if err := db.Create(&intent).Error; err != nil {
		t.Fatalf("create expired intent: %v", err)
	}
	claimed, ok, err := claimDelivery(context.Background(), db, intent.ID, now, false)
	if err != nil || !ok {
		t.Fatalf("claim replacement: ok=%v err=%v", ok, err)
	}
	if claimed.AttemptID == intent.AttemptID || claimed.AttemptCount != 2 {
		t.Fatalf("replacement claim=%+v, want new token and attempt 2", claimed)
	}
	if err := completeDelivery(context.Background(), db, intent.ID, intent.AttemptID, model.AlertDeliveryStatusFailed, nil, "stale failure"); err != nil {
		t.Fatalf("stale completion: %v", err)
	}
	var persisted model.AlertDelivery
	if err := db.First(&persisted, intent.ID).Error; err != nil {
		t.Fatalf("reload intent: %v", err)
	}
	if persisted.Status != model.AlertDeliveryStatusSending || persisted.AttemptID != claimed.AttemptID {
		t.Fatalf("stale completion overwrote replacement: %+v", persisted)
	}
}

func TestProxyClientCacheConcurrentHitAndCleanup(t *testing.T) {
	proxyURL := "http://proxy-cache-test.invalid"
	entry := newProxyClientEntry(&http.Client{}, time.Now())
	proxyClients.Store(proxyURL, entry)
	t.Cleanup(func() { proxyClients.Delete(proxyURL) })

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if got := getHTTPClient(proxyURL); got != entry.client {
				t.Errorf("got client %p, want cached client %p", got, entry.client)
			}
		}()
		go func() {
			defer wg.Done()
			cleanupExpiredProxyClients(time.Now())
		}()
	}
	wg.Wait()
	if _, ok := proxyClients.Load(proxyURL); !ok {
		t.Fatal("active proxy cache entry was removed")
	}
	cleanupExpiredProxyClients(entry.lastAccess().Add(proxyClientTTL + time.Nanosecond))
	if _, ok := proxyClients.Load(proxyURL); ok {
		t.Fatal("expired proxy cache entry was retained")
	}
}

func TestRetryDeliveryManualFailureIsTerminal(t *testing.T) {
	db := setupDurableTestDB(t)
	integration := durableTestIntegration("manual-failure")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	intent := model.AlertDelivery{AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusRetrying, Decision: "deliver"}
	if err := db.Create(&intent).Error; err != nil {
		t.Fatalf("create intent: %v", err)
	}
	worker := NewRetryWorker(db)
	worker.sendFn = func(model.Integration, model.Alert) error { return errors.New("manual send failed") }
	if err := runDeliveryAttempt(context.Background(), db, intent, worker.sendFn, true); err != nil {
		t.Fatalf("manual attempt persistence: %v", err)
	}
	var persisted model.AlertDelivery
	if err := db.First(&persisted, intent.ID).Error; err != nil {
		t.Fatalf("reload intent: %v", err)
	}
	if persisted.Status != model.AlertDeliveryStatusFailed {
		t.Fatalf("manual failure status=%q, want failed", persisted.Status)
	}
}
func TestRetryTickSkipsUnknownPageSQLite(t *testing.T) {
	testRetryTickSkipsUnknownPage(t, setupDurableTestDB(t))
}

func TestRetryTickSkipsUnknownPagePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	testRetryTickSkipsUnknownPage(t, openCausalTaskAlertPostgresDB(t, dsn))
}

func testRetryTickSkipsUnknownPage(t *testing.T, db *gorm.DB) {
	t.Helper()
	integration := durableTestIntegration("retry-page-unknown")
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	alert := durableTestAlert()
	alert.DeliveryDecision = model.AlertDeliveryDecisionDirect
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}

	unknown := make([]model.AlertDelivery, 1000)
	for i := range unknown {
		unknown[i] = model.AlertDelivery{
			AlertID: alert.ID, IntegrationID: integration.ID,
			Status: model.AlertDeliveryStatusPending, Decision: model.AlertDeliveryDecisionUnknown,
		}
	}
	if err := db.CreateInBatches(&unknown, 200).Error; err != nil {
		t.Fatalf("create unknown delivery page: %v", err)
	}
	past := time.Now().Add(-time.Second)
	deliverable := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID,
		DeliveryKey: deliveryIntentKey(alert.ID, integration.ID),
		Status:      model.AlertDeliveryStatusPending, Decision: "deliver",
		NextRetryAt: &past,
	}
	if err := db.Create(&deliverable).Error; err != nil {
		t.Fatalf("create deliverable after unknown page: %v", err)
	}

	var sends int64
	worker := NewRetryWorker(db)
	worker.sendFn = func(model.Integration, model.Alert) error {
		atomic.AddInt64(&sends, 1)
		return nil
	}
	for range 2 {
		worker.tick(context.Background(), time.Now())
		var current model.AlertDelivery
		if err := db.First(&current, deliverable.ID).Error; err != nil {
			t.Fatalf("reload deliverable: %v", err)
		}
		if current.Status == model.AlertDeliveryStatusSent {
			break
		}
	}
	if got := atomic.LoadInt64(&sends); got != 1 {
		t.Fatalf("send calls=%d, want one later-page delivery", got)
	}
	var current model.AlertDelivery
	if err := db.First(&current, deliverable.ID).Error; err != nil {
		t.Fatalf("reload deliverable: %v", err)
	}
	if current.Status != model.AlertDeliveryStatusSent {
		t.Fatalf("deliverable status=%q, want sent", current.Status)
	}
	var unknownCount int64
	if err := db.Model(&model.AlertDelivery{}).
		Where("alert_id = ? AND decision = ?", alert.ID, model.AlertDeliveryDecisionUnknown).
		Count(&unknownCount).Error; err != nil {
		t.Fatalf("count unknown deliveries: %v", err)
	}
	if unknownCount != int64(len(unknown)) {
		t.Fatalf("unknown delivery count=%d, want %d preserved", unknownCount, len(unknown))
	}
	var changedUnknown int64
	if err := db.Model(&model.AlertDelivery{}).
		Where("alert_id = ? AND decision = ? AND status <> ?", alert.ID, model.AlertDeliveryDecisionUnknown, model.AlertDeliveryStatusPending).
		Count(&changedUnknown).Error; err != nil {
		t.Fatalf("count changed unknown deliveries: %v", err)
	}
	if changedUnknown != 0 {
		t.Fatalf("unknown deliveries changed=%d, want none", changedUnknown)
	}
}
