package alerting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/golang-migrate/migrate/v4"

	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// migrationRuntimeFixture deliberately uses the real versioned migration files
// from the database source tree rather than AutoMigrate. The database package
// owns schema-contract tests; this package verifies post-retirement runtime delivery.
type migrationRuntimeFixture struct {
	engine   string
	migrator *migrate.Migrate
	sqlDB    *sql.DB
	db       *gorm.DB
}

func newAlertingMigrationRuntimeFixture(t *testing.T, engine string) *migrationRuntimeFixture {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed while locating migrations")
	}
	databaseRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "database"))
	source, err := iofs.New(os.DirFS(databaseRoot), "migrations/"+engine)
	if err != nil {
		t.Fatalf("open %s migration source: %v", engine, err)
	}

	fixture := &migrationRuntimeFixture{engine: engine}
	switch engine {
	case "sqlite":
		path := filepath.Join(t.TempDir(), "alerting-migration-runtime.db")
		fixture.sqlDB, err = sql.Open("sqlite3", fmt.Sprintf(
			"file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_txlock=immediate&_loc=UTC",
			path,
		))
		if err != nil {
			t.Fatalf("open SQLite migration database: %v", err)
		}
		fixture.sqlDB.SetMaxOpenConns(1)
		driver, driverErr := sqlitemigrate.WithInstance(fixture.sqlDB, &sqlitemigrate.Config{})
		if driverErr != nil {
			t.Fatalf("create SQLite migration driver: %v", driverErr)
		}
		fixture.migrator, err = migrate.NewWithInstance("iofs", source, "sqlite3", driver)
		if err != nil {
			t.Fatalf("create SQLite migrator: %v", err)
		}
		fixture.db, err = gorm.Open(sqlite.New(sqlite.Config{Conn: fixture.sqlDB}), &gorm.Config{})
	case "postgres":
		dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
		if dsn == "" {
			if os.Getenv("REQUIRE_POSTGRES_MIGRATION_TEST") == "1" {
				t.Fatal("TEST_POSTGRES_DSN is required for migration runtime verification")
			}
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		fixture.sqlDB = openAlertingMigrationPostgres(t, dsn)
		driver, driverErr := pgxmigrate.WithInstance(fixture.sqlDB, &pgxmigrate.Config{})
		if driverErr != nil {
			t.Fatalf("create PostgreSQL migration driver: %v", driverErr)
		}
		fixture.migrator, err = migrate.NewWithInstance("iofs", source, "pgx5", driver)
		if err != nil {
			t.Fatalf("create PostgreSQL migrator: %v", err)
		}
		fixture.db, err = gorm.Open(postgres.New(postgres.Config{Conn: fixture.sqlDB}), &gorm.Config{})
	default:
		t.Fatalf("unsupported migration engine %q", engine)
	}
	if err != nil {
		t.Fatalf("open %s GORM database: %v", engine, err)
	}
	t.Cleanup(func() {
		_, _ = fixture.migrator.Close()
		_ = fixture.sqlDB.Close()
	})
	if err := fixture.migrator.Migrate(89); err != nil {
		t.Fatalf("migrate %s fixture to version 89: %v", engine, err)
	}
	return fixture
}

func openAlertingMigrationPostgres(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	baseConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	if baseConfig.RuntimeParams["timezone"] == "" {
		baseConfig.RuntimeParams["timezone"] = "UTC"
	}
	base := stdlib.OpenDB(*baseConfig)
	if err := base.Ping(); err != nil {
		_ = base.Close()
		t.Fatalf("ping PostgreSQL base database: %v", err)
	}
	schema := fmt.Sprintf("xirang_alerting_migration_%d", time.Now().UTC().UnixNano())
	if _, err := base.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = base.Close()
		t.Fatalf("create PostgreSQL migration schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop PostgreSQL migration schema: %v", err)
		}
		_ = base.Close()
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	scopedConfig, err := pgx.ParseConfig(parsed.String())
	if err != nil {
		t.Fatalf("parse scoped PostgreSQL DSN: %v", err)
	}
	scopedConfig.RuntimeParams["search_path"] = schema
	scopedConfig.RuntimeParams["timezone"] = "UTC"
	return stdlib.OpenDB(*scopedConfig)
}

func (f *migrationRuntimeFixture) migrateToRetirement(t *testing.T) {
	t.Helper()
	if err := f.migrator.Steps(1); err != nil {
		t.Fatalf("apply 000090 retirement migration on %s: %v", f.engine, err)
	}
	version, dirty, err := f.migrator.Version()
	if err != nil {
		t.Fatalf("read %s migration version: %v", f.engine, err)
	}
	if version != 90 || dirty {
		t.Fatalf("%s migration version=%d dirty=%v, want 90 clean", f.engine, version, dirty)
	}
}
func (f *migrationRuntimeFixture) migrateToServiceMonitorRetirement(t *testing.T) {
	t.Helper()
	if err := f.migrator.Steps(2); err != nil {
		t.Fatalf("apply 000090 and 000091 retirement migrations on %s: %v", f.engine, err)
	}
	version, dirty, err := f.migrator.Version()
	if err != nil {
		t.Fatalf("read %s migration version: %v", f.engine, err)
	}
	if version != 91 || dirty {
		t.Fatalf("%s migration version=%d dirty=%v, want 91 clean", f.engine, version, dirty)
	}
}

type migrationDeliverySeed struct {
	integrationID           uint
	retiredDeliveryIDs      []uint
	retiredAlertIDs         []uint
	retiredNoIntentAlertID  uint
	retiredPendingAlertID   uint
	retiredSentAlertID      uint
	retiredSentDeliveryID   uint
	retiredSentEventID      uint
	retiredSentDeliveryKey  string
	retiredSentAt           time.Time
	retiredSentEventFiredAt time.Time
	resolvedPendingAlertID  uint
	retainedAlertID         uint
	retainedDeliveryID      uint
}

func seedAlertingMigrationDeliveryState(t *testing.T, db *gorm.DB, endpoint string) migrationDeliverySeed {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "dGVzdC1rZXktZGF0YS1lbmNyeXB0aW9uLWtleS0zMmIh")
	secure.ResetForTesting()
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	integration := model.Integration{
		Name: "migration-runtime-loopback", Type: "webhook", Endpoint: endpoint,
		Enabled: true, FailThreshold: 1, CooldownMinutes: 0,
	}
	node := &model.Node{
		Name: "migration-runtime-node", Host: "127.0.0.1", Port: 22,
		Username: "runtime", AuthType: "key", BackupDir: "/migration-runtime-alerting",
		Tags: "[]",
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("create migration runtime node: %v", err)
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create migration runtime integration: %v", err)
	}
	retainedIntegration := model.Integration{
		Name: "migration-runtime-retained-loopback", Type: "webhook",
		Endpoint: strings.TrimRight(endpoint, "/") + "/retained",
		Enabled:  true, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&retainedIntegration).Error; err != nil {
		t.Fatalf("create retained migration runtime integration: %v", err)
	}
	seed := migrationDeliverySeed{integrationID: integration.ID}

	newAlert := func(code, decision string, status string) *model.Alert {
		alert := &model.Alert{
			NodeID: node.ID, NodeName: "migration-runtime-node", Severity: "critical", Status: status,
			ErrorCode: code, Message: "migration runtime delivery", Retryable: true,
			TriggeredAt: now, Tags: "[]", LastLevelFired: -1, DeliveryDecision: decision,
		}
		if err := db.Create(alert).Error; err != nil {
			t.Fatalf("create alert %s: %v", code, err)
		}
		return alert
	}
	newDelivery := func(alertID uint, status string, decision string, key string, attempt int, lastError string, sentAt, nextRetry, lease *time.Time) model.AlertDelivery {
		delivery := model.AlertDelivery{
			AlertID: alertID, IntegrationID: integration.ID, Status: status, Decision: decision,
			DeliveryKey: key, AttemptCount: attempt, LastError: lastError,
			SentAt: sentAt, NextRetryAt: nextRetry, LeaseExpiresAt: lease,
			AttemptID: "migration-attempt",
		}
		if err := db.Create(&delivery).Error; err != nil {
			t.Fatalf("create delivery for alert %d: %v", alertID, err)
		}
		return delivery
	}

	statuses := []string{
		model.AlertDeliveryStatusPending,
		model.AlertDeliveryStatusRetrying,
		model.AlertDeliveryStatusSending,
		model.AlertDeliveryStatusFailed,
	}
	for i, status := range statuses {
		alert := newAlert(fmt.Sprintf("XR-NODE-%d", 700+i), model.AlertDeliveryDecisionDirect, "open")
		past := now.Add(-time.Minute)
		future := now.Add(time.Hour)
		var nextRetry, lease *time.Time
		if status == model.AlertDeliveryStatusRetrying {
			nextRetry = &past
		}
		if status == model.AlertDeliveryStatusSending {
			lease = &future
		}
		delivery := newDelivery(alert.ID, status, "deliver", deliveryIntentKey(alert.ID, integration.ID), i+1, "historical failure", nil, nextRetry, lease)
		seed.retiredDeliveryIDs = append(seed.retiredDeliveryIDs, delivery.ID)
		seed.retiredAlertIDs = append(seed.retiredAlertIDs, alert.ID)
	}
	for i, status := range statuses {
		alert := newAlert(fmt.Sprintf("XR-NODE-%d", 800+i), model.AlertDeliveryDecisionEscalated, "open")
		event := model.AlertEscalationEvent{
			AlertID: alert.ID, LevelIndex: 0, IntegrationIDs: fmt.Sprintf("[%d]", integration.ID),
			SeverityBefore: "critical", SeverityAfter: "critical", TagsAdded: "[]", FiredAt: now,
		}
		if err := db.Create(&event).Error; err != nil {
			t.Fatalf("create escalation event for alert %d: %v", alert.ID, err)
		}
		past := now.Add(-time.Minute)
		future := now.Add(time.Hour)
		var nextRetry, lease *time.Time
		if status == model.AlertDeliveryStatusRetrying {
			nextRetry = &past
		}
		if status == model.AlertDeliveryStatusSending {
			lease = &future
		}
		delivery := newDelivery(alert.ID, status, "deliver", escalationDeliveryIntentKey(alert.ID, event.ID, integration.ID), i+1, "historical escalation failure", nil, nextRetry, lease)
		seed.retiredDeliveryIDs = append(seed.retiredDeliveryIDs, delivery.ID)
		seed.retiredAlertIDs = append(seed.retiredAlertIDs, alert.ID)
	}

	// This row was already resolved before the upgrade. It must still be fenced.
	resolved := newAlert("XR-NODE-DISK-FULL", model.AlertDeliveryDecisionDirect, "resolved")
	resolvedDelivery := newDelivery(resolved.ID, model.AlertDeliveryStatusPending, "deliver", deliveryIntentKey(resolved.ID, integration.ID), 0, "", nil, nil, nil)
	seed.resolvedPendingAlertID = resolved.ID
	seed.retiredAlertIDs = append(seed.retiredAlertIDs, resolved.ID)
	seed.retiredDeliveryIDs = append(seed.retiredDeliveryIDs, resolvedDelivery.ID)

	// A pending decision with no intent must not be replayed or manually turned
	// into a new deliverable row after migration.
	pending := newAlert("XR-NODE-9998", model.AlertDeliveryDecisionPending, "open")
	seed.retiredPendingAlertID = pending.ID
	seed.retiredAlertIDs = append(seed.retiredAlertIDs, pending.ID)

	noIntent := newAlert("XR-NODE-9999", model.AlertDeliveryDecisionDirect, "open")
	seed.retiredNoIntentAlertID = noIntent.ID
	seed.retiredAlertIDs = append(seed.retiredAlertIDs, noIntent.ID)

	// Keep provider-success evidence and escalation history intact. The parent
	// and only unsent rows are fenced, while this sent fact remains deliver/done.
	sent := newAlert("XR-NODE-7010", model.AlertDeliveryDecisionEscalated, "open")
	sent.Retryable = true
	if err := db.Save(sent).Error; err != nil {
		t.Fatalf("mark sent alert retryable: %v", err)
	}
	event := model.AlertEscalationEvent{
		AlertID: sent.ID, LevelIndex: 0, IntegrationIDs: fmt.Sprintf("[%d]", integration.ID),
		SeverityBefore: "critical", SeverityAfter: "critical", TagsAdded: "[]", FiredAt: now,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create sent escalation history: %v", err)
	}
	whenSent := now.Add(-30 * time.Minute)
	sentDelivery := newDelivery(sent.ID, model.AlertDeliveryStatusSent, "deliver", escalationDeliveryIntentKey(sent.ID, event.ID, integration.ID), 4, "", &whenSent, nil, nil)
	seed.retiredSentAlertID = sent.ID
	seed.retiredSentDeliveryID = sentDelivery.ID
	seed.retiredSentEventID = event.ID
	seed.retiredSentDeliveryKey = sentDelivery.DeliveryKey
	seed.retiredSentAt = whenSent
	seed.retiredSentEventFiredAt = event.FiredAt
	seed.retiredAlertIDs = append(seed.retiredAlertIDs, sent.ID)

	// This source is intentionally outside every retired predicate.
	retained := newAlert("XR-TASK-RETAINED", model.AlertDeliveryDecisionDirect, "open")
	retained.Retryable = true
	if err := db.Save(retained).Error; err != nil {
		t.Fatalf("mark retained alert retryable: %v", err)
	}
	retainedDelivery := newDelivery(retained.ID, model.AlertDeliveryStatusPending, "deliver", deliveryIntentKey(retained.ID, integration.ID), 0, "", nil, nil, nil)
	retainedDelivery.IntegrationID = retainedIntegration.ID
	retainedDelivery.DeliveryKey = deliveryIntentKey(retained.ID, retainedIntegration.ID)
	if err := db.Model(&model.AlertDelivery{}).Where("id = ?", retainedDelivery.ID).Updates(map[string]any{
		"integration_id": retainedIntegration.ID,
		"delivery_key":   retainedDelivery.DeliveryKey,
	}).Error; err != nil {
		t.Fatalf("move retained delivery to retained sink: %v", err)
	}
	seed.retainedAlertID = retained.ID
	seed.retainedDeliveryID = retainedDelivery.ID
	return seed
}

func TestMigrationRetiredDeliveryFenceSQLite(t *testing.T) {
	testMigrationRetiredDeliveryFence(t, newAlertingMigrationRuntimeFixture(t, "sqlite"))
}

func TestMigrationRetiredDeliveryFencePostgres(t *testing.T) {
	testMigrationRetiredDeliveryFence(t, newAlertingMigrationRuntimeFixture(t, "postgres"))
}

func testMigrationRetiredDeliveryFence(t *testing.T, fixture *migrationRuntimeFixture) {
	t.Helper()
	var retiredSends, retainedSends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/retained" {
			retainedSends.Add(1)
		} else {
			retiredSends.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	seed := seedAlertingMigrationDeliveryState(t, fixture.db, server.URL)
	fixture.migrateToRetirement(t)

	var sent model.AlertDelivery
	if err := fixture.db.First(&sent, seed.retiredSentDeliveryID).Error; err != nil {
		t.Fatalf("load preserved sent delivery: %v", err)
	}
	if sent.Status != model.AlertDeliveryStatusSent || sent.Decision != "deliver" || sent.AttemptCount != 4 ||
		sent.DeliveryKey != seed.retiredSentDeliveryKey || sent.SentAt == nil ||
		!sent.SentAt.UTC().Equal(seed.retiredSentAt.UTC()) {
		t.Fatalf("sent delivery changed by retirement: %+v", sent)
	}
	var history model.AlertEscalationEvent
	if err := fixture.db.First(&history, seed.retiredSentEventID).Error; err != nil {
		t.Fatalf("load preserved escalation history: %v", err)
	}
	if history.AlertID != seed.retiredSentAlertID || history.LevelIndex != 0 ||
		!history.FiredAt.UTC().Equal(seed.retiredSentEventFiredAt.UTC()) {
		t.Fatalf("escalation history changed by retirement: %+v", history)
	}

	for _, alertID := range seed.retiredAlertIDs {
		var alert model.Alert
		if err := fixture.db.First(&alert, alertID).Error; err != nil {
			t.Fatalf("load retired alert %d: %v", alertID, err)
		}
		if alert.Status != "resolved" || alert.Retryable || alert.DeliveryDecision != model.AlertDeliveryDecisionUnknown || alert.DeliveryReason != "feature_retired" {
			t.Fatalf("retired alert %d was not fenced: %+v", alertID, alert)
		}
	}
	for _, deliveryID := range seed.retiredDeliveryIDs {
		var delivery model.AlertDelivery
		if err := fixture.db.First(&delivery, deliveryID).Error; err != nil {
			t.Fatalf("load retired delivery %d: %v", deliveryID, err)
		}
		if delivery.Status != model.AlertDeliveryStatusFailed || delivery.Decision != model.AlertDeliveryDecisionUnknown || delivery.LeaseExpiresAt != nil || delivery.NextRetryAt != nil {
			t.Fatalf("retired delivery %d was not fenced: %+v", deliveryID, delivery)
		}
	}

	dispatcher := NewDispatcher(fixture.db, nil, nil)
	worker := NewRetryWorker(fixture.db)
	worker.dispatcher = dispatcher
	worker.sendFn = dispatcher.send
	worker.tick(context.Background(), time.Now().UTC())
	if got := retiredSends.Load(); got != 0 {
		t.Fatalf("automatic retry sent retired delivery: retired_sends=%d", got)
	}
	if got := retainedSends.Load(); got != 1 {
		t.Fatalf("automatic retry did not deliver retained source: retained_sends=%d", got)
	}
	for _, deliveryID := range seed.retiredDeliveryIDs {
		var before model.AlertDelivery
		if err := fixture.db.First(&before, deliveryID).Error; err != nil {
			t.Fatalf("load retired delivery %d before manual retry: %v", deliveryID, err)
		}
		if err := worker.ManualRetry(deliveryID); err != nil {
			t.Fatalf("manual retry for retired delivery %d: %v", deliveryID, err)
		}
		if _, err := dispatcher.RetryDeliveryByID(context.Background(), deliveryID); err != nil {
			t.Fatalf("dispatcher manual retry for retired delivery %d: %v", deliveryID, err)
		}
		if _, err := dispatcher.RetryDelivery(context.Background(), before.AlertID, before.IntegrationID); err != nil {
			t.Fatalf("dispatcher alert/channel retry for retired delivery %d: %v", deliveryID, err)
		}
		var after model.AlertDelivery
		if err := fixture.db.First(&after, deliveryID).Error; err != nil {
			t.Fatalf("reload retired delivery %d: %v", deliveryID, err)
		}
		if after.Status != before.Status || after.Decision != before.Decision || after.AttemptCount != before.AttemptCount ||
			after.LeaseExpiresAt != nil || after.NextRetryAt != nil {
			t.Fatalf("manual retry changed retired delivery %d: before=%+v after=%+v", deliveryID, before, after)
		}
	}
	if _, err := dispatcher.RetryDelivery(context.Background(), seed.retiredNoIntentAlertID, seed.integrationID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("manual retry created an intent for retired no-intent alert: err=%v", err)
	}
	var noIntentCount int64
	if err := fixture.db.Model(&model.AlertDelivery{}).Where("alert_id = ?", seed.retiredNoIntentAlertID).Count(&noIntentCount).Error; err != nil {
		t.Fatalf("count retired no-intent rows: %v", err)
	}
	if noIntentCount != 0 {
		t.Fatalf("retired no-intent alert acquired %d delivery rows", noIntentCount)
	}
	var pending model.Alert
	if err := fixture.db.First(&pending, seed.retiredPendingAlertID).Error; err != nil {
		t.Fatalf("load retired pending-decision alert: %v", err)
	}
	if err := dispatcher.dispatchCreatedAlertWithSender(&pending, dispatcher.send); err != nil {
		t.Fatalf("replay retired pending-decision alert: %v", err)
	}
	var pendingCount int64
	if err := fixture.db.Model(&model.AlertDelivery{}).Where("alert_id = ?", seed.retiredPendingAlertID).Count(&pendingCount).Error; err != nil {
		t.Fatalf("count pending-decision replay rows: %v", err)
	}
	if pendingCount != 0 || retiredSends.Load() != 0 {
		t.Fatalf("retired pending-decision replay produced delivery: rows=%d retired_sends=%d", pendingCount, retiredSends.Load())
	}

	var retained model.Alert
	if err := fixture.db.First(&retained, seed.retainedAlertID).Error; err != nil {
		t.Fatalf("load retained alert: %v", err)
	}
	if retained.Status != "open" || retained.DeliveryDecision != model.AlertDeliveryDecisionDirect {
		t.Fatalf("retained source was unexpectedly fenced: %+v", retained)
	}
	if err := dispatcher.dispatchCreatedAlert(&retained); err != nil {
		t.Fatalf("dispatch retained source: %v", err)
	}
	if got := retainedSends.Load(); got != 1 {
		t.Fatalf("dispatcher replay duplicated or lost retained source: retained_sends=%d", got)
	}
	var retainedDelivery model.AlertDelivery
	if err := fixture.db.First(&retainedDelivery, seed.retainedDeliveryID).Error; err != nil {
		t.Fatalf("load retained delivery: %v", err)
	}
	if retainedDelivery.Status != model.AlertDeliveryStatusSent || retainedDelivery.SentAt == nil {
		t.Fatalf("retained source did not successfully deliver: %+v", retainedDelivery)
	}
	worker.tick(context.Background(), time.Now().UTC())
	if retiredSends.Load() != 0 || retainedSends.Load() != 1 {
		t.Fatalf("retry replay changed sink sends: retired_sends=%d retained_sends=%d", retiredSends.Load(), retainedSends.Load())
	}
	testMigrationServiceMonitorDeliveryFence(t, fixture.engine)
}

func testMigrationServiceMonitorDeliveryFence(t *testing.T, engine string) {
	t.Helper()
	var serviceSends, retainedSends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/retained" {
			retainedSends.Add(1)
		} else {
			serviceSends.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	fixture := newAlertingMigrationRuntimeFixture(t, engine)
	seed := seedAlertingMigrationDeliveryState(t, fixture.db, server.URL)
	var node model.Node
	if err := fixture.db.First(&node).Error; err != nil {
		t.Fatalf("load service-monitor migration node: %v", err)
	}
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	serviceAlert := &model.Alert{
		NodeID: node.ID, NodeName: node.Name, Severity: "critical", Status: "open",
		ErrorCode: "XR-SERVICE-DOWN-42", Message: "retired service source", Retryable: true,
		TriggeredAt: now, Tags: "[]", LastLevelFired: -1, DeliveryDecision: model.AlertDeliveryDecisionDirect,
	}
	if err := fixture.db.Create(serviceAlert).Error; err != nil {
		t.Fatalf("create service-monitor migration alert: %v", err)
	}
	serviceDelivery := &model.AlertDelivery{
		AlertID: serviceAlert.ID, IntegrationID: seed.integrationID,
		Status: model.AlertDeliveryStatusRetrying, Decision: "deliver",
		DeliveryKey: deliveryIntentKey(serviceAlert.ID, seed.integrationID),
		AttemptID:   "service-monitor-migration-attempt", AttemptCount: 3,
		LastError:   "service-monitor migration failure",
		NextRetryAt: &now,
	}
	if err := fixture.db.Create(serviceDelivery).Error; err != nil {
		t.Fatalf("create service-monitor migration delivery: %v", err)
	}
	fixture.migrateToServiceMonitorRetirement(t)

	var retired model.Alert
	if err := fixture.db.First(&retired, serviceAlert.ID).Error; err != nil {
		t.Fatalf("load retired service-monitor alert: %v", err)
	}
	if retired.Status != "resolved" || retired.Retryable || retired.DeliveryDecision != model.AlertDeliveryDecisionUnknown || retired.DeliveryReason != "feature_retired" {
		t.Fatalf("retired service-monitor alert was not fenced: %+v", retired)
	}
	var retiredDelivery model.AlertDelivery
	if err := fixture.db.First(&retiredDelivery, serviceDelivery.ID).Error; err != nil {
		t.Fatalf("load retired service-monitor delivery: %v", err)
	}
	if retiredDelivery.Status != model.AlertDeliveryStatusFailed || retiredDelivery.Decision != model.AlertDeliveryDecisionUnknown ||
		retiredDelivery.AttemptCount != 3 || retiredDelivery.LastError != "service-monitor migration failure" ||
		retiredDelivery.LeaseExpiresAt != nil || retiredDelivery.NextRetryAt != nil {
		t.Fatalf("retired service-monitor delivery was not fenced: %+v", retiredDelivery)
	}

	dispatcher := NewDispatcher(fixture.db, nil, nil)
	worker := NewRetryWorker(fixture.db)
	worker.dispatcher = dispatcher
	worker.sendFn = dispatcher.send
	worker.tick(context.Background(), time.Now().UTC())
	if got := serviceSends.Load(); got != 0 {
		t.Fatalf("automatic retry sent retired service-monitor delivery: sends=%d", got)
	}
	if got := retainedSends.Load(); got != 1 {
		t.Fatalf("automatic retry did not deliver retained source after service retirement: sends=%d", got)
	}

	beforeCount := int64(0)
	if err := fixture.db.Model(&model.AlertDelivery{}).Where("alert_id = ?", serviceAlert.ID).Count(&beforeCount).Error; err != nil {
		t.Fatalf("count service-monitor deliveries before replay: %v", err)
	}
	if err := worker.ManualRetry(serviceDelivery.ID); err != nil {
		t.Fatalf("manual retry of retired service-monitor delivery: %v", err)
	}
	if _, err := dispatcher.RetryDeliveryByID(context.Background(), serviceDelivery.ID); err != nil {
		t.Fatalf("dispatcher retry of retired service-monitor delivery: %v", err)
	}
	if _, err := dispatcher.RetryDelivery(context.Background(), serviceAlert.ID, seed.integrationID); err != nil {
		t.Fatalf("dispatcher alert/channel retry of retired service-monitor delivery: %v", err)
	}
	if err := dispatcher.dispatchCreatedAlertWithSender(&retired, dispatcher.send); err != nil {
		t.Fatalf("replay retired service-monitor alert: %v", err)
	}
	afterCount := int64(0)
	if err := fixture.db.Model(&model.AlertDelivery{}).Where("alert_id = ?", serviceAlert.ID).Count(&afterCount).Error; err != nil {
		t.Fatalf("count service-monitor deliveries after replay: %v", err)
	}
	if afterCount != beforeCount || serviceSends.Load() != 0 {
		t.Fatalf("retired service-monitor replay changed delivery state: before=%d after=%d sends=%d", beforeCount, afterCount, serviceSends.Load())
	}
}
