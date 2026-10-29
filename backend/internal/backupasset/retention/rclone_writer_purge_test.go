package retention

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestRcloneExpiredWriterCannotBeExplicitlyPurged(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { runRcloneExpiredWriterPurgeGuard(t, newLifecycleCoordinatorTestDB(t)) })
	t.Run("postgres", func(t *testing.T) { runRcloneExpiredWriterPurgeGuard(t, newLifecycleCoordinatorPostgresTestDB(t)) })
}

func runRcloneExpiredWriterPurgeGuard(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	repositoryID := testOpaqueID(1900)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	if err := db.Model(&model.BackupRepository{}).Where("id = ?", repositoryID).Update("provider_kind", backupasset.ProviderRclone).Error; err != nil {
		t.Fatal(err)
	}
	point := newSelectionPoint(testOpaqueID(1901), repositoryID, nil, now.Add(-time.Hour), 1)
	point.Semantics = string(backupasset.PointXirangManifest)
	point.State = string(backupasset.RecoveryPointPreparing)
	point.ConsistencyJSON = `{"version":1,"provider":"rclone","completion":"outcome_unknown","code":"publication_deadline_exceeded"}`
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	leases, err := backupasset.NewLeaseService(db, clock, backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	holds := mustNewLifecycleHoldService(t, db, clock)
	coordinator, err := NewCoordinator(CoordinatorDependencies{DB: db, Leases: leases, Holds: holds, Now: clock, LeaseOwnerID: "purge-writer-guard", Admissions: &lifecycleAdmissionFake{}, RetryDelay: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	purge, err := NewPurgeService(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	actor := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	for _, state := range []backupasset.RecoveryPointState{backupasset.RecoveryPointPreparing, backupasset.RecoveryPointVerifying} {
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("state", state).Error; err != nil {
			t.Fatal(err)
		}
		_, err := purge.Preview(ctx, PreviewPurgeRequest{Actor: actor, RepositoryID: repositoryID, RecoveryPointIDs: []string{point.ID}})
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("explicit purge accepted %s unknown writer: %v", state, err)
		}
	}
	assertNoClaimedAttempts(t, db)
	var stored model.RecoveryPoint
	if err := db.First(&stored, "id = ?", point.ID).Error; err != nil || stored.ConsistencyJSON != point.ConsistencyJSON {
		t.Fatal("purge changed the writer hold")
	}
}
