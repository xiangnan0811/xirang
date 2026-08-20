package retention

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAuditRetentionPrunesEligibleClosedSegmentDetailsAndKeepsCheckpointContinuity(t *testing.T) {
	writer, clock, db := newRetentionAuditHarness(t, backupasset.AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("write first segment: %v", err)
	}
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("write second segment: %v", err)
	}
	clock.now = clock.now.Add(200 * 24 * time.Hour)

	service, err := NewAuditRetention(AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (AuditRetentionConfig, error) {
			return AuditRetentionConfig{DetailRetentionDays: 180, CheckpointRetentionDays: 2555}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewAuditRetention: %v", err)
	}
	purged, err := service.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 1 {
		t.Fatalf("PurgeEligibleDetails=%d err=%v, want 1 eligible closed segment", purged, err)
	}

	var events int64
	if err := db.Model(&model.BackupAssetAuditEvent{}).Where("segment_no = ?", 1).Count(&events).Error; err != nil {
		t.Fatalf("count purged segment events: %v", err)
	}
	var checkpoints []model.BackupAssetAuditCheckpoint
	if err := db.Order("segment_no ASC").Find(&checkpoints).Error; err != nil {
		t.Fatalf("load checkpoints: %v", err)
	}
	if events != 0 || len(checkpoints) != 2 || checkpoints[0].Status != string(backupasset.AuditSegmentDetailsPurged) ||
		checkpoints[0].DetailsPurgedAt == nil || checkpoints[0].CheckpointHash == "" ||
		checkpoints[1].PreviousCheckpointHash != checkpoints[0].CheckpointHash ||
		checkpoints[1].EntryCount == 0 {
		t.Fatalf("checkpoint continuity after purge: events=%d checkpoints=%+v", events, checkpoints)
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify after eligible detail purge: %v", err)
	}
}

func TestAuditRetentionSkipsOpenRecentAndAlreadyPurgedSegments(t *testing.T) {
	writer, clock, db := newRetentionAuditHarness(t, backupasset.AuditConfig{SegmentMaxEvents: 100, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("write open segment: %v", err)
	}
	service, err := NewAuditRetention(AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (AuditRetentionConfig, error) {
			return AuditRetentionConfig{DetailRetentionDays: 180, CheckpointRetentionDays: 2555}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	purged, err := service.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 0 {
		t.Fatalf("open segment purged=%d err=%v, want 0", purged, err)
	}
	var openEvents int64
	if err := db.Model(&model.BackupAssetAuditEvent{}).Count(&openEvents).Error; err != nil || openEvents != 1 {
		t.Fatalf("open segment events=%d err=%v, want retained 1", openEvents, err)
	}
}

func TestAuditRetentionUsesDynamicDetailRetentionDays(t *testing.T) {
	writer, clock, db := newRetentionAuditHarness(t, backupasset.AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("write closed segment: %v", err)
	}
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatalf("open next segment: %v", err)
	}
	clock.now = clock.now.Add(10 * 24 * time.Hour)
	days := 30
	service, err := NewAuditRetention(AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (AuditRetentionConfig, error) {
			return AuditRetentionConfig{DetailRetentionDays: days, CheckpointRetentionDays: 2555}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	purged, err := service.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 0 {
		t.Fatalf("10-day-old segment with 30-day policy purged=%d err=%v, want 0", purged, err)
	}
	days = 5
	purged, err = service.PurgeEligibleDetails(context.Background(), 10)
	if err != nil || purged != 1 {
		t.Fatalf("dynamic 5-day policy purged=%d err=%v, want 1", purged, err)
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify after dynamic purge: %v", err)
	}
}

func TestAuditRetentionNeverDeletesCheckpointsOrIndependentEvidence(t *testing.T) {
	writer, clock, db := newRetentionAuditHarness(t, backupasset.AuditConfig{SegmentMaxEvents: 1, SegmentMaxAge: 24 * time.Hour})
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(context.Background(), retentionAuditEventInput()); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(4000 * 24 * time.Hour)
	service, err := NewAuditRetention(AuditRetentionDependencies{
		DB: db, Writer: writer, Now: clock.Now,
		Config: func() (AuditRetentionConfig, error) {
			return AuditRetentionConfig{DetailRetentionDays: 1, CheckpointRetentionDays: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PurgeEligibleDetails(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var checkpoints int64
	if err := db.Model(&model.BackupAssetAuditCheckpoint{}).Count(&checkpoints).Error; err != nil || checkpoints != 2 {
		t.Fatalf("checkpoints=%d err=%v, want both retained", checkpoints, err)
	}
	if err := writer.Verify(context.Background()); err != nil {
		t.Fatalf("Verify after checkpoint-retention setting: %v", err)
	}
}

type retentionAuditClock struct{ now time.Time }

func (clock *retentionAuditClock) Now() time.Time { return clock.now }

func newRetentionAuditHarness(t *testing.T, config backupasset.AuditConfig) (*backupasset.AuditWriter, *retentionAuditClock, *gorm.DB) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_AUDIT_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	clock := &retentionAuditClock{now: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)}
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)), &gorm.Config{NowFunc: clock.Now, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open audit retention database: %v", err)
	}
	if err := db.AutoMigrate(&model.WrappedDomainKey{}, &model.BackupAssetAuditCheckpoint{}, &model.BackupAssetAuditEvent{}); err != nil {
		t.Fatalf("migrate audit retention tables: %v", err)
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wrapped_domain_keys_domain_version ON wrapped_domain_keys(domain, version)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wrapped_domain_keys_active ON wrapped_domain_keys(domain) WHERE state = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_asset_audit_events_segment_sequence ON backup_asset_audit_events(segment_no, segment_sequence)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("create audit retention index: %v", err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	ring := backupasset.NewKeyring(db, clock.Now)
	writer, err := backupasset.NewAuditWriter(db, ring, clock.Now, config)
	if err != nil {
		t.Fatalf("NewAuditWriter: %v", err)
	}
	return writer, clock, db
}

func retentionAuditEventInput() backupasset.AuditEventInput {
	return backupasset.AuditEventInput{
		Actor:           backupasset.AuditActor{UserID: 41, Username: "admin", Role: "admin"},
		Action:          backupasset.AuditActionAssetList,
		Outcome:         backupasset.AuditOutcomeSuccess,
		RepositoryID:    strings.Repeat("a", 32),
		RecoveryPointID: strings.Repeat("b", 32),
		EntryID:         strings.Repeat("c", 64),
		ItemCount:       1,
		ByteCount:       128,
		Range:           backupasset.NewRangeSummary(1, 128),
		Fields:          map[backupasset.AuditField]any{backupasset.AuditFieldCorrelationID: "corr_retention"},
	}
}
