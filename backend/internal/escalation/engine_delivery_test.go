package escalation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func migrateEscalationDeliveryDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "dGVzdC1rZXktZGF0YS1lbmNyeXB0aW9uLWtleS0zMmIh")
	secure.ResetForTesting()
	if err := db.AutoMigrate(
		&model.EscalationPolicy{}, &model.Alert{}, &model.Task{}, &model.Policy{},
		&model.SLODefinition{}, &model.Node{}, &model.AlertEscalationEvent{},
		&model.AlertDelivery{}, &model.Integration{},
	); err != nil {
		t.Fatalf("migrate escalation delivery tables: %v", err)
	}
}

func openEscalationDeliverySQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(8)
	} else {
		t.Fatalf("get SQLite handle: %v", err)
	}
	migrateEscalationDeliveryDB(t, db)
	return db
}

func openEscalationDeliveryPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL base connection: %v", err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL base connection: %v", err)
	}
	schema := fmt.Sprintf("xirang_escalation_delivery_%d", time.Now().UTC().UnixNano())
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = baseSQL.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
		}
		_ = baseSQL.Close()
	})
	scoped := *parsed
	query := scoped.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	scoped.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(scoped.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL connection: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(8)
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		t.Fatalf("get isolated PostgreSQL handle: %v", err)
	}
	migrateEscalationDeliveryDB(t, db)
	return db
}

func seedEscalationDeliveryAlert(t *testing.T, db *gorm.DB, policyID, nodeID uint, triggered time.Time) *model.Alert {
	t.Helper()
	if err := db.Create(&model.Node{ID: nodeID, Name: "delivery-node", Host: "host", Username: "user", BackupDir: "/backup", EscalationPolicyID: &policyID}).Error; err != nil {
		t.Fatalf("create escalation node: %v", err)
	}
	alert := &model.Alert{
		NodeID: nodeID, NodeName: "delivery-node", Severity: "warning", Status: "open",
		ErrorCode: "XR-ESC-DELIVERY", Message: "escalation delivery", TriggeredAt: triggered,
		Tags: "[]", LastLevelFired: -1, DeliveryDecision: model.AlertDeliveryDecisionEscalated,
	}
	if err := db.Create(alert).Error; err != nil {
		t.Fatalf("create escalation alert: %v", err)
	}
	return alert
}

func newLoopbackWebhook(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server, &sends
}

func waitForWebhookSends(t *testing.T, sends *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for sends.Load() < want {
		select {
		case <-deadline.C:
			t.Fatalf("webhook sends=%d, want at least %d", sends.Load(), want)
		case <-ticker.C:
		}
	}
}

func TestEngineProductionDispatcherPersistsAndSendsEscalationLevelsSQLite(t *testing.T) {
	db := openEscalationDeliverySQLiteDB(t)
	server, sends := newLoopbackWebhook(t)
	integration := model.Integration{
		Name: "escalation-loopback-sqlite", Type: "webhook", Enabled: true,
		Endpoint: server.URL, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	svc := NewService(db)
	triggered := time.Now().UTC()
	policy := seedPolicy(t, svc, "escalation-delivery-sqlite", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{integration.ID}},
		{DelaySeconds: 1, IntegrationIDs: []uint{integration.ID}},
	}, "warning")
	alert := seedEscalationDeliveryAlert(t, db, policy.ID, 101, triggered)
	dispatcher := alerting.NewDispatcher(db, nil, nil)
	alerting.SetDispatcher(dispatcher)
	t.Cleanup(func() { alerting.SetDispatcher(nil) })
	engine := NewEngine(db, svc, nil, alerting.DefaultRaiser{DB: db})
	engine.SetNowFn(func() time.Time { return triggered })
	engine.Tick(context.Background())
	waitForWebhookSends(t, sends, 1)

	var events []model.AlertEscalationEvent
	if err := db.Where("alert_id = ?", alert.ID).Order("level_index ASC").Find(&events).Error; err != nil {
		t.Fatalf("load first escalation event: %v", err)
	}
	if len(events) != 1 || events[0].LevelIndex != 0 {
		t.Fatalf("events after first level=%+v, want one level 0 event", events)
	}
	var first model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&first).Error; err != nil {
		t.Fatalf("load first escalation intent: %v", err)
	}
	if first.Status != model.AlertDeliveryStatusSent || first.AttemptCount != 1 {
		t.Fatalf("first escalation intent=%+v, want sent once", first)
	}
	if first.DeliveryKey != fmt.Sprintf("%d:%d:%d", alert.ID, events[0].ID, integration.ID) {
		t.Fatalf("first delivery key=%q, want event-scoped key", first.DeliveryKey)
	}
	var persisted model.Alert
	if err := db.First(&persisted, alert.ID).Error; err != nil {
		t.Fatalf("reload alert: %v", err)
	}
	if persisted.DeliveryDecision != model.AlertDeliveryDecisionEscalated || persisted.LastLevelFired != 0 {
		t.Fatalf("alert after first level=%+v, want escalated ownership and level 0", persisted)
	}

	engine.SetNowFn(func() time.Time { return triggered.Add(2 * time.Second) })
	engine.Tick(context.Background())
	waitForWebhookSends(t, sends, 2)
	if err := db.Where("alert_id = ?", alert.ID).Order("level_index ASC").Find(&events).Error; err != nil {
		t.Fatalf("load both escalation events: %v", err)
	}
	if len(events) != 2 || events[1].LevelIndex != 1 {
		t.Fatalf("events after second level=%+v, want levels 0 and 1", events)
	}
	var deliveries []model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).Order("id ASC").Find(&deliveries).Error; err != nil {
		t.Fatalf("load escalation intents: %v", err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("escalation intents=%d, want one per level despite integration reuse", len(deliveries))
	}
	if deliveries[0].DeliveryKey == deliveries[1].DeliveryKey || deliveries[1].Status != model.AlertDeliveryStatusSent || deliveries[1].AttemptCount != 1 {
		t.Fatalf("reused integration intents=%+v, want distinct sent rows", deliveries)
	}
}

type failingEnqueueDispatcher struct {
	err   error
	calls atomic.Int64
}

func (d *failingEnqueueDispatcher) EnqueueEscalationDeliveriesTx(
	_ *gorm.DB, _ model.Alert, _ model.AlertEscalationEvent, _ []uint,
) ([]uint, error) {
	return nil, d.err
}

func (d *failingEnqueueDispatcher) DispatchEscalationDeliveries(
	_ context.Context, _ model.Alert, _ uint, _ []uint,
) error {
	d.calls.Add(1)
	return nil
}

func TestEngineEscalationIntentFailureRollsBackEventAndLevel(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "escalation-enqueue-failure", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
	}, "warning")
	triggered := time.Now().UTC()
	alert := seedAlertOnNodeWithPolicy(t, db, 102, policy.ID, triggered, "warning")
	dispatcher := &failingEnqueueDispatcher{err: errors.New("injected enqueue failure")}
	engine := NewEngine(db, svc, nil, dispatcher)
	engine.SetNowFn(func() time.Time { return triggered })
	engine.Tick(context.Background())

	var persisted model.Alert
	if err := db.First(&persisted, alert.ID).Error; err != nil {
		t.Fatalf("reload alert after failed fire: %v", err)
	}
	if persisted.LastLevelFired != -1 {
		t.Fatalf("last_level_fired=%d, want rollback to -1", persisted.LastLevelFired)
	}
	var eventCount int64
	if err := db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", alert.ID).Count(&eventCount).Error; err != nil {
		t.Fatalf("count rolled-back events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("rolled-back fire left %d events", eventCount)
	}
	if dispatcher.calls.Load() != 0 {
		t.Fatalf("post-commit dispatch calls=%d, want zero", dispatcher.calls.Load())
	}
}

func TestEngineProductionDispatcherIntentFailureRollsBackSQLite(t *testing.T) {
	db := openEscalationDeliverySQLiteDB(t)
	server, sends := newLoopbackWebhook(t)
	integration := model.Integration{
		Name: "escalation-rollback-loopback", Type: "webhook", Enabled: true,
		Endpoint: server.URL, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	svc := NewService(db)
	triggered := time.Now().UTC()
	policy := seedPolicy(t, svc, "escalation-production-rollback", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{integration.ID}},
	}, "warning")
	alert := seedEscalationDeliveryAlert(t, db, policy.ID, 106, triggered)

	var failed atomic.Bool
	const callbackName = "test:fail-escalation-intent-create-once"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "alert_deliveries" && failed.CompareAndSwap(false, true) {
			_ = tx.AddError(errors.New("injected escalation intent create failure"))
		}
	}); err != nil {
		t.Fatalf("register intent fault: %v", err)
	}
	engine := NewEngine(db, svc, nil, alerting.NewDispatcher(db, nil, nil))
	engine.SetNowFn(func() time.Time { return triggered })
	engine.Tick(context.Background())
	_ = db.Callback().Create().Remove(callbackName)
	if !failed.Load() {
		t.Fatal("intent create fault callback did not run")
	}
	if sends.Load() != 0 {
		t.Fatalf("rollback path sent %d notifications", sends.Load())
	}

	var persisted model.Alert
	if err := db.First(&persisted, alert.ID).Error; err != nil {
		t.Fatalf("reload alert after production rollback: %v", err)
	}
	if persisted.LastLevelFired != -1 {
		t.Fatalf("last_level_fired=%d, want rollback to -1", persisted.LastLevelFired)
	}
	var eventCount int64
	if err := db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", alert.ID).Count(&eventCount).Error; err != nil {
		t.Fatalf("count rolled-back production events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("production rollback left %d events", eventCount)
	}
	var intentCount int64
	if err := db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&intentCount).Error; err != nil {
		t.Fatalf("count rolled-back production intents: %v", err)
	}
	if intentCount != 0 {
		t.Fatalf("production rollback left %d intents", intentCount)
	}
}

func TestEngineNilDispatcherDoesNotAdvanceDurableLevel(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policy := seedPolicy(t, svc, "escalation-nil-dispatcher", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{1}},
	}, "warning")
	triggered := time.Now().UTC()
	alert := seedAlertOnNodeWithPolicy(t, db, 103, policy.ID, triggered, "warning")
	engine := NewEngine(db, svc, nil, nil)
	engine.SetNowFn(func() time.Time { return triggered })
	engine.Tick(context.Background())

	var persisted model.Alert
	if err := db.First(&persisted, alert.ID).Error; err != nil {
		t.Fatalf("reload nil-dispatcher alert: %v", err)
	}
	if persisted.LastLevelFired != -1 {
		t.Fatalf("nil dispatcher advanced last_level_fired=%d", persisted.LastLevelFired)
	}
	var eventCount int64
	db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", alert.ID).Count(&eventCount)
	if eventCount != 0 {
		t.Fatalf("nil dispatcher left %d events", eventCount)
	}
}

type suppressingPostCommitDispatcher struct {
	inner *alerting.Dispatcher
}

func (d *suppressingPostCommitDispatcher) EnqueueEscalationDeliveriesTx(
	tx *gorm.DB, alert model.Alert, event model.AlertEscalationEvent, ids []uint,
) ([]uint, error) {
	return d.inner.EnqueueEscalationDeliveriesTx(tx, alert, event, ids)
}

func (d *suppressingPostCommitDispatcher) DispatchEscalationDeliveries(
	context.Context, model.Alert, uint, []uint,
) error {
	// Simulate a process crash after the fire transaction commits and before
	// the post-commit fan-out starts. RetryWorker owns the pending intent.
	return nil
}

func TestEnginePostCommitCrashLeavesIntentForProductionRetrySQLite(t *testing.T) {
	db := openEscalationDeliverySQLiteDB(t)
	server, sends := newLoopbackWebhook(t)
	integration := model.Integration{
		Name: "escalation-replay-loopback", Type: "webhook", Enabled: true,
		Endpoint: server.URL, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	svc := NewService(db)
	triggered := time.Now().UTC()
	policy := seedPolicy(t, svc, "escalation-replay", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{integration.ID}},
	}, "warning")
	alert := seedEscalationDeliveryAlert(t, db, policy.ID, 104, triggered)
	production := alerting.NewDispatcher(db, nil, nil)
	alerting.SetDispatcher(production)
	t.Cleanup(func() { alerting.SetDispatcher(nil) })
	engine := NewEngine(db, svc, nil, &suppressingPostCommitDispatcher{inner: production})
	engine.SetNowFn(func() time.Time { return triggered })
	engine.Tick(context.Background())

	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load committed pre-send intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusPending {
		t.Fatalf("pre-send intent=%+v, want pending", intent)
	}
	if sends.Load() != 0 {
		t.Fatalf("crash simulation sent %d notifications before retry", sends.Load())
	}

	worker := alerting.NewRetryWorker(db)
	if err := worker.ManualRetry(intent.ID); err != nil {
		t.Fatalf("retry committed escalation intent: %v", err)
	}
	waitForWebhookSends(t, sends, 1)
	if err := db.First(&intent, intent.ID).Error; err != nil {
		t.Fatalf("reload replayed intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSent || intent.AttemptCount != 1 {
		t.Fatalf("replayed intent=%+v, want one sent attempt", intent)
	}
}

func testEngineEscalationConcurrency(t *testing.T, db *gorm.DB) {
	t.Helper()
	server, sends := newLoopbackWebhook(t)
	integration := model.Integration{
		Name: "escalation-concurrent-loopback", Type: "webhook", Enabled: true,
		Endpoint: server.URL, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create integration: %v", err)
	}
	svc := NewService(db)
	triggered := time.Now().UTC()
	policy := seedPolicy(t, svc, "escalation-concurrent", []model.EscalationLevel{
		{DelaySeconds: 0, IntegrationIDs: []uint{integration.ID}},
	}, "warning")
	alert := seedEscalationDeliveryAlert(t, db, policy.ID, 105, triggered)
	dispatcher := alerting.NewDispatcher(db, nil, nil)
	e1 := NewEngine(db, svc, nil, dispatcher)
	e2 := NewEngine(db, svc, nil, dispatcher)
	e1.SetNowFn(func() time.Time { return triggered })
	e2.SetNowFn(func() time.Time { return triggered })
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		e1.Tick(context.Background())
	}()
	go func() {
		defer wg.Done()
		e2.Tick(context.Background())
	}()
	wg.Wait()
	waitForWebhookSends(t, sends, 1)

	var events int64
	if err := db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", alert.ID).Count(&events).Error; err != nil {
		t.Fatalf("count concurrent events: %v", err)
	}
	if events != 1 {
		t.Fatalf("concurrent event rows=%d, want one", events)
	}
	var deliveries int64
	if err := db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&deliveries).Error; err != nil {
		t.Fatalf("count concurrent intents: %v", err)
	}
	if deliveries != 1 {
		t.Fatalf("concurrent intent rows=%d, want one", deliveries)
	}
	var intent model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&intent).Error; err != nil {
		t.Fatalf("load concurrent intent: %v", err)
	}
	if intent.Status != model.AlertDeliveryStatusSent || intent.AttemptCount != 1 {
		t.Fatalf("concurrent intent=%+v, want sent once", intent)
	}
}

func TestEngineEscalationDeliveryConcurrencySQLite(t *testing.T) {
	testEngineEscalationConcurrency(t, openEscalationDeliverySQLiteDB(t))
}

func TestEngineEscalationDeliveryConcurrencyPostgres(t *testing.T) {
	testEngineEscalationConcurrency(t, openEscalationDeliveryPostgresDB(t))
}
