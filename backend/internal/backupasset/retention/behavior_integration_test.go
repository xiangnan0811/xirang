package retention

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestRetentionWorkerBehaviorSelectsExpiresAndRecordsAggregates(t *testing.T) {
	runRetentionWorkerBehaviorSelectsExpiresAndRecordsAggregates(t, nil)
}

func TestRetentionWorkerBehaviorDisabledMaintenanceRetainsClaimedLease(t *testing.T) {
	runRetentionWorkerBehaviorDisabledMaintenanceRetainsClaimedLease(t, nil)
}

func TestRetentionBehaviorPostgres(t *testing.T) {
	requireRetentionBehaviorPostgresDSN(t)
	t.Run("SelectsExpiresAndRecordsAggregates", func(t *testing.T) {
		runRetentionWorkerBehaviorSelectsExpiresAndRecordsAggregates(t, newLifecycleCoordinatorPostgresTestDB(t))
	})
	t.Run("DisabledMaintenanceRetainsClaimedLease", func(t *testing.T) {
		runRetentionWorkerBehaviorDisabledMaintenanceRetainsClaimedLease(t, newLifecycleCoordinatorPostgresTestDB(t))
	})
}

func requireRetentionBehaviorPostgresDSN(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")) != "" {
		return
	}
	if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_MIGRATION_TEST")) == "1" {
		t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1")
	}
	t.Skip("TEST_POSTGRES_DSN is not configured")
}

func runRetentionWorkerBehaviorSelectsExpiresAndRecordsAggregates(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 10, eligiblePoints: 2, keepLatest: 1, db: db,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatalf("StartupPass: %v", err)
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&attempt).Error; err != nil {
		t.Fatalf("load selected attempt: %v", err)
	}
	if attempt.Operation != string(backupasset.LifecycleRetentionExpire) || attempt.Phase == "" {
		t.Fatalf("behavior attempt=%+v", attempt)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointExpiring) &&
		point.State != string(backupasset.RecoveryPointExpired) &&
		point.State != string(backupasset.RecoveryPointPurgeBlocked) {
		t.Fatalf("selected point state=%s, want a lifecycle state", point.State)
	}
	if fixture.metrics.count(MetricSelected) < 1 {
		t.Fatal("behavior pass did not record selected aggregates")
	}
}

func runRetentionWorkerBehaviorDisabledMaintenanceRetainsClaimedLease(t *testing.T, db *gorm.DB) {
	t.Helper()
	fixture := newRetentionWorkerFixture(t, retentionWorkerFixtureOptions{
		enabled: true, interval: 30 * time.Second, batchSize: 1, eligiblePoints: 1, db: db,
	})
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	var lease model.RecoveryPointLease
	if err := fixture.db.First(&lease).Error; err != nil {
		t.Fatal(err)
	}
	fixture.settings["backup_assets.enabled"] = "false"
	if err := fixture.worker.StartupPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&lease, "id = ?", lease.ID).Error; err != nil {
		t.Fatal(err)
	}
	if lease.HolderType != string(backupasset.LeaseHolderRetentionWorker) {
		t.Fatalf("disabled maintenance dropped retention_worker lease holder=%s", lease.HolderType)
	}
	if strings.TrimSpace(lease.OwnerID) != "retention-worker" {
		t.Fatalf("disabled maintenance owner=%s", lease.OwnerID)
	}
}
