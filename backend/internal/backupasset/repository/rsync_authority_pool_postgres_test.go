package repository

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/internal/authoritylock"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm/clause"
)

func TestRecoveryRsyncAuthorityPostgresUsesCallerConnection(t *testing.T) {
	fixture := newPostgresRsyncRestoreResolverFixture(t)

	// Capture the genuine proof before restricting the pool: the capture itself
	// owns a transaction and is not the caller-owned operation under test.
	baseline, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("capture recovery authority: %v", err)
	}
	request := provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref}
	expected := recoveryRsyncAuthorityObservation(fixture.ref, baseline)

	// Use the fixture's bound values as database overrides so environment
	// settings cannot change the result. Construct a fresh settings service
	// after capture so the revalidation exercises its cold production path.
	values := completeRepositoryFoundationSettings(true)
	rows := make([]model.SystemSetting, 0, len(values))
	for _, key := range settings.BackupAssetFoundationSettingKeys() {
		value, ok := values[key]
		if !ok {
			t.Fatalf("fixture has no foundation setting %q", key)
		}
		rows = append(rows, model.SystemSetting{Key: key, Value: value})
	}
	if err := fixture.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&rows).Error; err != nil {
		t.Fatalf("seed foundation settings: %v", err)
	}
	fixture.service.foundation = backupasset.NewFoundationService(settings.NewService(fixture.db))

	pool, err := fixture.db.DB()
	if err != nil {
		t.Fatalf("get fixture connection pool: %v", err)
	}
	previousMaxOpen := pool.Stats().MaxOpenConnections
	pool.SetMaxOpenConns(1)
	defer pool.SetMaxOpenConns(previousMaxOpen)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx := fixture.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin caller transaction: %v", tx.Error)
	}
	defer tx.Rollback()
	if err := authoritylock.LockRepository(ctx, tx, fixture.ref.RepositoryID); err != nil {
		t.Fatalf("lock repository authority: %v", err)
	}
	// An uncommitted setting proves the request was built from this transaction,
	// rather than a cached or separately checked-out settings connection.
	if err := tx.Model(&model.SystemSetting{}).Where("key = ?", "backup_assets.manifest_timeout").
		Update("value", "1h").Error; err != nil {
		t.Fatalf("override manifest timeout in caller transaction: %v", err)
	}
	if err := fixture.service.RevalidateRecoverySourceAuthorityTx(ctx, tx, request, expected); err != nil {
		t.Fatalf("revalidate recovery authority with the only connection held: %v", err)
	}
	current, err := fixture.service.loadRecoveryRsyncAuthoritySnapshotTx(ctx, tx, fixture.ref)
	if err != nil {
		t.Fatalf("load recovery authority in caller transaction: %v", err)
	}
	if got := current.durable.request.ManifestLimits.Timeout; got != time.Hour {
		t.Fatalf("manifest timeout from caller transaction = %v, want %v", got, time.Hour)
	}
}
