package escalation

import (
	"context"
	"database/sql"
	"encoding/json"
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

	"xirang/backend/internal/alerting"
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

type escalationMigrationRuntimeFixture struct {
	engine   string
	migrator *migrate.Migrate
	sqlDB    *sql.DB
	db       *gorm.DB
}

func newEscalationMigrationRuntimeFixture(t *testing.T, engine string) *escalationMigrationRuntimeFixture {
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

	fixture := &escalationMigrationRuntimeFixture{engine: engine}
	switch engine {
	case "sqlite":
		path := filepath.Join(t.TempDir(), "escalation-migration-runtime.db")
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
				t.Fatal("TEST_POSTGRES_DSN is required for migration escalation verification")
			}
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		fixture.sqlDB = openEscalationMigrationPostgres(t, dsn)
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

func openEscalationMigrationPostgres(t *testing.T, dsn string) *sql.DB {
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
	schema := fmt.Sprintf("xirang_escalation_migration_%d", time.Now().UTC().UnixNano())
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

func (f *escalationMigrationRuntimeFixture) migrateToRetirement(t *testing.T) {
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
func (f *escalationMigrationRuntimeFixture) migrateToServiceMonitorRetirement(t *testing.T) {
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

type escalationMigrationSeed struct {
	integrationID     uint
	retiredAlertID    uint
	retiredDeliveryID uint
	retiredEventID    uint
	retainedAlertID   uint
}

func seedEscalationMigrationState(t *testing.T, db *gorm.DB, endpoint string) escalationMigrationSeed {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "dGVzdC1rZXktZGF0YS1lbmNyeXB0aW9uLWtleS0zMmIh")
	secure.ResetForTesting()
	now := time.Now().UTC().Add(-time.Hour)
	integration := model.Integration{
		Name: "escalation-migration-loopback", Type: "webhook", Endpoint: endpoint,
		Enabled: true, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("create escalation migration integration: %v", err)
	}
	levels, err := json.Marshal([]model.EscalationLevel{{
		DelaySeconds: 0, IntegrationIDs: []uint{integration.ID}, SeverityOverride: "", Tags: []string{"migration"},
	}, {DelaySeconds: 0, IntegrationIDs: []uint{integration.ID}}})
	if err != nil {
		t.Fatalf("marshal escalation levels: %v", err)
	}
	policy := &model.EscalationPolicy{
		Name: "migration-runtime-policy", Description: "migration runtime", MinSeverity: "warning",
		Enabled: true, Levels: string(levels),
	}
	if err := db.Create(policy).Error; err != nil {
		t.Fatalf("create escalation policy: %v", err)
	}
	node := &model.Node{
		Name: "migration-runtime-escalation-node", Host: "127.0.0.1", Port: 22,
		Username: "runtime", AuthType: "key", BackupDir: "/migration-runtime",
		Tags: "[]", EscalationPolicyID: &policy.ID,
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("create escalation node: %v", err)
	}
	retired := &model.Alert{
		NodeID: node.ID, NodeName: node.Name, Severity: "critical", Status: "open",
		ErrorCode: "XR-NODE-8800", Message: "retired escalation source", Retryable: true,
		TriggeredAt: now, Tags: "[]", LastLevelFired: 0, DeliveryDecision: model.AlertDeliveryDecisionEscalated,
	}
	if err := db.Create(retired).Error; err != nil {
		t.Fatalf("create retired escalation alert: %v", err)
	}
	event := &model.AlertEscalationEvent{
		AlertID: retired.ID, EscalationPolicyID: &policy.ID, LevelIndex: 0,
		IntegrationIDs: fmt.Sprintf("[%d]", integration.ID), SeverityBefore: "critical", SeverityAfter: "critical",
		TagsAdded: "[\"migration\"]", FiredAt: now,
	}
	if err := db.Create(event).Error; err != nil {
		t.Fatalf("create retired escalation event: %v", err)
	}
	delivery := &model.AlertDelivery{
		AlertID: retired.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusPending,
		Decision: "deliver", DeliveryKey: fmt.Sprintf("%d:%d:%d", retired.ID, event.ID, integration.ID),
	}
	if err := db.Create(delivery).Error; err != nil {
		t.Fatalf("create retired escalation delivery: %v", err)
	}
	retained := &model.Alert{
		NodeID: node.ID, NodeName: node.Name, Severity: "warning", Status: "open",
		ErrorCode: "XR-TASK-RETAINED", Message: "retained escalation source", Retryable: true,
		TriggeredAt: now, Tags: "[]", LastLevelFired: -1, DeliveryDecision: model.AlertDeliveryDecisionPending,
	}
	if err := db.Create(retained).Error; err != nil {
		t.Fatalf("create retained escalation alert: %v", err)
	}
	return escalationMigrationSeed{
		integrationID: integration.ID, retiredAlertID: retired.ID, retiredDeliveryID: delivery.ID,
		retiredEventID: event.ID, retainedAlertID: retained.ID,
	}
}

func TestMigrationEscalationDeliveryFenceSQLite(t *testing.T) {
	testMigrationEscalationDeliveryFence(t, newEscalationMigrationRuntimeFixture(t, "sqlite"))
}

func TestMigrationEscalationDeliveryFencePostgres(t *testing.T) {
	testMigrationEscalationDeliveryFence(t, newEscalationMigrationRuntimeFixture(t, "postgres"))
}

func testMigrationEscalationDeliveryFence(t *testing.T, fixture *escalationMigrationRuntimeFixture) {
	t.Helper()
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	seed := seedEscalationMigrationState(t, fixture.db, server.URL)
	fixture.migrateToRetirement(t)

	var retired model.Alert
	if err := fixture.db.First(&retired, seed.retiredAlertID).Error; err != nil {
		t.Fatalf("load retired escalation alert: %v", err)
	}
	if retired.Status != "resolved" || retired.Retryable || retired.DeliveryDecision != model.AlertDeliveryDecisionUnknown || retired.DeliveryReason != "feature_retired" {
		t.Fatalf("retired escalation parent was not fenced: %+v", retired)
	}
	var retiredDelivery model.AlertDelivery
	if err := fixture.db.First(&retiredDelivery, seed.retiredDeliveryID).Error; err != nil {
		t.Fatalf("load retired escalation delivery: %v", err)
	}
	if retiredDelivery.Status != model.AlertDeliveryStatusFailed || retiredDelivery.Decision != model.AlertDeliveryDecisionUnknown {
		t.Fatalf("retired escalation delivery was not fenced: %+v", retiredDelivery)
	}
	var retiredEvent model.AlertEscalationEvent
	if err := fixture.db.First(&retiredEvent, seed.retiredEventID).Error; err != nil {
		t.Fatalf("load retired escalation event: %v", err)
	}

	dispatcher := alerting.NewDispatcher(fixture.db, nil, nil)
	if _, err := dispatcher.RetryDeliveryByID(context.Background(), retiredDelivery.ID); err != nil {
		t.Fatalf("manual retry of retired escalation delivery: %v", err)
	}
	if err := dispatcher.DispatchEscalationDeliveries(context.Background(), retired, retiredEvent.ID, []uint{retiredDelivery.ID}); err != nil {
		t.Fatalf("automatic escalation replay of retired delivery: %v", err)
	}
	if got := sends.Load(); got != 0 {
		t.Fatalf("retired escalation intent was sent after migration: sends=%d", got)
	}

	var beforeRetiredEvents int64
	if err := fixture.db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", retired.ID).Count(&beforeRetiredEvents).Error; err != nil {
		t.Fatalf("count retired escalation events before tick: %v", err)
	}
	engine := NewEngine(fixture.db, NewService(fixture.db), nil, dispatcher)
	engine.SetNowFn(func() time.Time { return time.Now().UTC() })
	engine.Tick(context.Background())
	var afterRetiredEvents int64
	if err := fixture.db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", retired.ID).Count(&afterRetiredEvents).Error; err != nil {
		t.Fatalf("count retired escalation events after tick: %v", err)
	}
	if afterRetiredEvents != beforeRetiredEvents {
		t.Fatalf("resolved retired parent created a new escalation event: before=%d after=%d", beforeRetiredEvents, afterRetiredEvents)
	}

	var retained model.Alert
	if err := fixture.db.First(&retained, seed.retainedAlertID).Error; err != nil {
		t.Fatalf("load retained escalation alert: %v", err)
	}
	if retained.Status != "open" || retained.DeliveryDecision != model.AlertDeliveryDecisionPending {
		t.Fatalf("retained escalation source was unexpectedly changed: %+v", retained)
	}
	var retainedEvents int64
	if err := fixture.db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", retained.ID).Count(&retainedEvents).Error; err != nil {
		t.Fatalf("count retained escalation events: %v", err)
	}
	if retainedEvents != 1 {
		t.Fatalf("retained source events=%d, want one fired level", retainedEvents)
	}
	if got := sends.Load(); got != 1 {
		t.Fatalf("retained escalation sends=%d, want one", got)
	}
	var retainedDelivery model.AlertDelivery
	if err := fixture.db.Where("alert_id = ? AND integration_id = ?", retained.ID, seed.integrationID).First(&retainedDelivery).Error; err != nil {
		t.Fatalf("load retained escalation delivery: %v", err)
	}
	if retainedDelivery.Status != model.AlertDeliveryStatusSent || retainedDelivery.SentAt == nil || retainedDelivery.Decision != "deliver" {
		t.Fatalf("retained escalation did not successfully deliver: %+v", retainedDelivery)
	}
	testMigrationServiceMonitorEscalationFence(t, fixture.engine)
}

func testMigrationServiceMonitorEscalationFence(t *testing.T, engine string) {
	t.Helper()
	var serviceSends, retainedSends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ErrorCode string `json:"error_code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode escalation migration webhook payload: %v", err)
		}
		switch payload.ErrorCode {
		case "XR-TASK-RETAINED":
			retainedSends.Add(1)
		case "XR-SERVICE-DOWN-42":
			serviceSends.Add(1)
		default:
			t.Errorf("unexpected escalation migration webhook error code %q", payload.ErrorCode)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	fixture := newEscalationMigrationRuntimeFixture(t, engine)
	seed := seedEscalationMigrationState(t, fixture.db, server.URL)
	var node model.Node
	if err := fixture.db.First(&node).Error; err != nil {
		t.Fatalf("load service-monitor escalation node: %v", err)
	}
	var policy model.EscalationPolicy
	if err := fixture.db.First(&policy).Error; err != nil {
		t.Fatalf("load service-monitor escalation policy: %v", err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	serviceAlert := &model.Alert{
		NodeID: node.ID, NodeName: node.Name, Severity: "critical", Status: "open",
		ErrorCode: "XR-SERVICE-DOWN-42", Message: "retired service escalation source", Retryable: true,
		TriggeredAt: now, Tags: "[]", LastLevelFired: 0, DeliveryDecision: model.AlertDeliveryDecisionEscalated,
	}
	if err := fixture.db.Create(serviceAlert).Error; err != nil {
		t.Fatalf("create service-monitor escalation alert: %v", err)
	}
	event := &model.AlertEscalationEvent{
		AlertID: serviceAlert.ID, EscalationPolicyID: &policy.ID, LevelIndex: 0,
		IntegrationIDs: fmt.Sprintf("[%d]", seed.integrationID), SeverityBefore: "critical", SeverityAfter: "critical",
		TagsAdded: "[\"migration\"]", FiredAt: now,
	}
	if err := fixture.db.Create(event).Error; err != nil {
		t.Fatalf("create service-monitor escalation event: %v", err)
	}
	var serviceEventBeforeMigration model.AlertEscalationEvent
	if err := fixture.db.First(&serviceEventBeforeMigration, event.ID).Error; err != nil {
		t.Fatalf("load service-monitor escalation event before migration: %v", err)
	}
	assertServiceEventUnchanged := func(stage string) {
		var persisted model.AlertEscalationEvent
		if err := fixture.db.First(&persisted, event.ID).Error; err != nil {
			t.Fatalf("load service-monitor escalation event after %s: %v", stage, err)
		}
		policyMatches := persisted.EscalationPolicyID == nil && serviceEventBeforeMigration.EscalationPolicyID == nil
		if persisted.EscalationPolicyID != nil && serviceEventBeforeMigration.EscalationPolicyID != nil {
			policyMatches = *persisted.EscalationPolicyID == *serviceEventBeforeMigration.EscalationPolicyID
		}
		if !policyMatches ||
			persisted.ID != serviceEventBeforeMigration.ID ||
			persisted.AlertID != serviceEventBeforeMigration.AlertID ||
			persisted.LevelIndex != serviceEventBeforeMigration.LevelIndex ||
			persisted.IntegrationIDs != serviceEventBeforeMigration.IntegrationIDs ||
			persisted.SeverityBefore != serviceEventBeforeMigration.SeverityBefore ||
			persisted.SeverityAfter != serviceEventBeforeMigration.SeverityAfter ||
			persisted.TagsAdded != serviceEventBeforeMigration.TagsAdded ||
			!persisted.FiredAt.Equal(serviceEventBeforeMigration.FiredAt) {
			t.Fatalf("service-monitor escalation event changed after %s: before=%+v after=%+v", stage, serviceEventBeforeMigration, persisted)
		}
	}
	delivery := &model.AlertDelivery{
		AlertID: serviceAlert.ID, IntegrationID: seed.integrationID,
		Status: model.AlertDeliveryStatusPending, Decision: "deliver",
		DeliveryKey: fmt.Sprintf("%d:%d:%d", serviceAlert.ID, event.ID, seed.integrationID),
		AttemptID:   "service-monitor-escalation-attempt",
	}
	if err := fixture.db.Create(delivery).Error; err != nil {
		t.Fatalf("create service-monitor escalation delivery: %v", err)
	}
	fixture.migrateToServiceMonitorRetirement(t)
	assertServiceEventUnchanged("SQL migration")

	var retired model.Alert
	if err := fixture.db.First(&retired, serviceAlert.ID).Error; err != nil {
		t.Fatalf("load retired service-monitor escalation alert: %v", err)
	}
	if retired.Status != "resolved" || retired.Retryable || retired.DeliveryDecision != model.AlertDeliveryDecisionUnknown || retired.DeliveryReason != "feature_retired" {
		t.Fatalf("retired service-monitor escalation alert was not fenced: %+v", retired)
	}
	var retiredDelivery model.AlertDelivery
	if err := fixture.db.First(&retiredDelivery, delivery.ID).Error; err != nil {
		t.Fatalf("load retired service-monitor escalation delivery: %v", err)
	}
	if retiredDelivery.Status != model.AlertDeliveryStatusFailed || retiredDelivery.Decision != model.AlertDeliveryDecisionUnknown ||
		retiredDelivery.LeaseExpiresAt != nil || retiredDelivery.NextRetryAt != nil {
		t.Fatalf("retired service-monitor escalation delivery was not fenced: %+v", retiredDelivery)
	}

	dispatcher := alerting.NewDispatcher(fixture.db, nil, nil)
	if _, err := dispatcher.RetryDeliveryByID(context.Background(), retiredDelivery.ID); err != nil {
		t.Fatalf("manual retry of retired service-monitor escalation delivery: %v", err)
	}
	if err := dispatcher.DispatchEscalationDeliveries(context.Background(), retired, event.ID, []uint{retiredDelivery.ID}); err != nil {
		t.Fatalf("automatic replay of retired service-monitor escalation delivery: %v", err)
	}
	if got := serviceSends.Load(); got != 0 {
		t.Fatalf("retired service-monitor escalation intent was sent: sends=%d", got)
	}

	var beforeRetiredEvents int64
	if err := fixture.db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", retired.ID).Count(&beforeRetiredEvents).Error; err != nil {
		t.Fatalf("count retired service-monitor escalation events before tick: %v", err)
	}
	engineService := NewEngine(fixture.db, NewService(fixture.db), nil, dispatcher)
	engineService.SetNowFn(func() time.Time { return time.Now().UTC() })
	engineService.Tick(context.Background())
	var afterRetiredEvents int64
	if err := fixture.db.Model(&model.AlertEscalationEvent{}).Where("alert_id = ?", retired.ID).Count(&afterRetiredEvents).Error; err != nil {
		t.Fatalf("count retired service-monitor escalation events after tick: %v", err)
	}
	if afterRetiredEvents != beforeRetiredEvents || serviceSends.Load() != 0 {
		t.Fatalf("retired service-monitor escalation changed after tick: before=%d after=%d sends=%d", beforeRetiredEvents, afterRetiredEvents, serviceSends.Load())
	}
	assertServiceEventUnchanged("replay and tick")
	if got := retainedSends.Load(); got != 1 {
		t.Fatalf("retained escalation source sends=%d, want one", got)
	}
}
