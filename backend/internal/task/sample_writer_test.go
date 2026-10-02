package task

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func forceSampleCleanup(writer *SampleWriter) {
	writer.lastSampleCleanupAt = time.Now().UTC().Add(-defaultSampleCleanupInterval - time.Second)
}

func seedRetentionSample(t *testing.T, db *gorm.DB, taskEntity model.Task, sampledAt time.Time) model.TaskTrafficSample {
	t.Helper()
	sample := model.TaskTrafficSample{
		TaskID:         taskEntity.ID,
		NodeID:         taskEntity.NodeID,
		RunStartedAt:   sampledAt.Add(-time.Minute),
		SampledAt:      sampledAt,
		ThroughputMbps: 1,
	}
	if err := db.Create(&sample).Error; err != nil {
		t.Fatalf("create retention sample: %v", err)
	}
	return sample
}

func retentionSampleCount(t *testing.T, db *gorm.DB, sampleID uint) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.TaskTrafficSample{}).Where("id = ?", sampleID).Count(&count).Error; err != nil {
		t.Fatalf("count retention sample %d: %v", sampleID, err)
	}
	return count
}

func TestSampleWriterRetentionUsesLiveSettings(t *testing.T) {
	runSampleWriterRetentionUsesLiveSettings(t, openManagerTestDB(t))
}

func TestSampleWriterRetentionUsesLiveSettingsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runSampleWriterRetentionUsesLiveSettings(t, openTaskRetentionPostgresDB(t, dsn))
}

func runSampleWriterRetentionUsesLiveSettings(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("TASK_TRAFFIC_RETENTION_DAYS", "")
	settingsSvc := openRetentionSettingsService(t, db)
	if err := settingsSvc.Update("retention.task_traffic_days", "30"); err != nil {
		t.Fatalf("set traffic retention override: %v", err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, settingsSvc, nil, 0, 90)
	shutdownManagerOnCleanup(t, manager)
	stopRetentionWorkers(t, manager)

	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-10 * 24 * time.Hour)
	sample := seedRetentionSample(t, db, taskEntity, old)
	manager.sampleWriter.cleanupExpired()
	if got := retentionSampleCount(t, db, sample.ID); got != 1 {
		t.Fatalf("DB retention=30 deleted 10-day sample, count=%d", got)
	}

	if err := settingsSvc.Update("retention.task_traffic_days", "8"); err != nil {
		t.Fatalf("shrink traffic retention override: %v", err)
	}
	forceSampleCleanup(manager.sampleWriter)
	manager.sampleWriter.cleanupExpired()
	if got := retentionSampleCount(t, db, sample.ID); got != 0 {
		t.Fatalf("DB retention=8 kept 10-day sample, count=%d", got)
	}

	expanded := seedRetentionSample(t, db, taskEntity, old)
	if err := settingsSvc.Update("retention.task_traffic_days", "30"); err != nil {
		t.Fatalf("expand traffic retention override: %v", err)
	}
	forceSampleCleanup(manager.sampleWriter)
	manager.sampleWriter.cleanupExpired()
	if got := retentionSampleCount(t, db, expanded.ID); got != 1 {
		t.Fatalf("expanded DB retention=30 deleted new 10-day sample, count=%d", got)
	}

	if err := settingsSvc.Delete("retention.task_traffic_days"); err != nil {
		t.Fatalf("delete traffic retention override: %v", err)
	}
	defaultSample := seedRetentionSample(t, db, taskEntity, old)
	forceSampleCleanup(manager.sampleWriter)
	manager.sampleWriter.cleanupExpired()
	if got := retentionSampleCount(t, db, defaultSample.ID); got != 0 {
		t.Fatalf("registered default retention=8 kept 10-day sample, count=%d", got)
	}

	t.Setenv("TASK_TRAFFIC_RETENTION_DAYS", "0")
	envDisabled := seedRetentionSample(t, db, taskEntity, old)
	forceSampleCleanup(manager.sampleWriter)
	manager.sampleWriter.cleanupExpired()
	if got := retentionSampleCount(t, db, envDisabled.ID); got != 1 {
		t.Fatalf("deleted DB override with env retention=0, count=%d", got)
	}
}

func TestSampleWriterRetentionFixesCutoffPerRound(t *testing.T) {
	runSampleWriterRetentionFixesCutoffPerRound(t, openManagerTestDB(t))
}

func TestSampleWriterRetentionFixesCutoffPerRoundPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runSampleWriterRetentionFixesCutoffPerRound(t, openTaskRetentionPostgresDB(t, dsn))
}

func runSampleWriterRetentionFixesCutoffPerRound(t *testing.T, db *gorm.DB) {
	t.Helper()
	settingsSvc := openRetentionSettingsService(t, db)
	if err := settingsSvc.Update("retention.task_traffic_days", "30"); err != nil {
		t.Fatalf("set initial traffic retention: %v", err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, settingsSvc, nil, 0, 90)
	shutdownManagerOnCleanup(t, manager)
	stopRetentionWorkers(t, manager)
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-10 * 24 * time.Hour)
	sample := seedRetentionSample(t, db, taskEntity, old)

	updateErr := installRetentionSettingUpdateAfterRead(t, db, settingsSvc, "retention.task_traffic_days", "8")
	forceSampleCleanup(manager.sampleWriter)
	manager.sampleWriter.cleanupExpired()
	if err := updateErr(); err != nil {
		t.Fatalf("update traffic retention during cleanup round: %v", err)
	}
	if got := retentionSampleCount(t, db, sample.ID); got != 1 {
		t.Fatalf("round read at 30 used later 8-day setting, count=%d", got)
	}

	forceSampleCleanup(manager.sampleWriter)
	manager.sampleWriter.cleanupExpired()
	if got := retentionSampleCount(t, db, sample.ID); got != 0 {
		t.Fatalf("next round did not use updated 8-day setting, count=%d", got)
	}

	if err := settingsSvc.Update("retention.task_traffic_days", "8"); err != nil {
		t.Fatalf("set reverse traffic retention: %v", err)
	}
	reverse := seedRetentionSample(t, db, taskEntity, old)
	updateErr = installRetentionSettingUpdateAfterRead(t, db, settingsSvc, "retention.task_traffic_days", "30")
	forceSampleCleanup(manager.sampleWriter)
	manager.sampleWriter.cleanupExpired()
	if err := updateErr(); err != nil {
		t.Fatalf("expand traffic retention during cleanup round: %v", err)
	}
	if got := retentionSampleCount(t, db, reverse.ID); got != 0 {
		t.Fatalf("round read at 8 used later 30-day setting, count=%d", got)
	}

	expanded := seedRetentionSample(t, db, taskEntity, old)
	forceSampleCleanup(manager.sampleWriter)
	manager.sampleWriter.cleanupExpired()
	if got := retentionSampleCount(t, db, expanded.ID); got != 1 {
		t.Fatalf("next round did not use expanded 30-day setting, count=%d", got)
	}
}

func TestSampleWriterRetentionSkipsSettingsReadFailure(t *testing.T) {
	runSampleWriterRetentionSkipsSettingsReadFailure(t, openManagerTestDB(t))
}

func TestSampleWriterRetentionSkipsSettingsReadFailurePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runSampleWriterRetentionSkipsSettingsReadFailure(t, openTaskRetentionPostgresDB(t, dsn))
}

func runSampleWriterRetentionSkipsSettingsReadFailure(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("TASK_TRAFFIC_RETENTION_DAYS", "1")
	settingsSvc := openRetentionSettingsService(t, db)
	if err := settingsSvc.Update("retention.task_traffic_days", "30"); err != nil {
		t.Fatalf("set traffic retention for read failure: %v", err)
	}
	taskEntity := seedRetentionTask(t, db)
	sample := seedRetentionSample(t, db, taskEntity, time.Now().UTC().Add(-10*24*time.Hour))
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, settingsSvc, nil, 1, 90)
	shutdownManagerOnCleanup(t, manager)
	stopRetentionWorkers(t, manager)

	callbackName := fmt.Sprintf("test:sample-settings-read-failure:%s:%d", t.Name(), time.Now().UnixNano())
	injected := errors.New("FAKE_SETTINGS_QUERY_FAILURE_FOR_TEST_ONLY")
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "system_settings" {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatalf("register settings read failure: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	manager.sampleWriter.cleanupExpired()
	if got := retentionSampleCount(t, db, sample.ID); got != 1 {
		t.Fatalf("settings read failure fell back to env retention=1, count=%d", got)
	}
	if !manager.sampleWriter.lastSampleCleanupAt.IsZero() {
		t.Fatal("settings read failure advanced cleanup throttle timestamp")
	}
}

func TestSampleWriterRetentionSkipsInvalidSettingsValues(t *testing.T) {
	runSampleWriterRetentionSkipsInvalidSettingsValues(t, openManagerTestDB(t))
}

func TestSampleWriterRetentionSkipsInvalidSettingsValuesPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runSampleWriterRetentionSkipsInvalidSettingsValues(t, openTaskRetentionPostgresDB(t, dsn))
}

func runSampleWriterRetentionSkipsInvalidSettingsValues(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Setenv("TASK_TRAFFIC_RETENTION_DAYS", "1")
	settingsSvc := openRetentionSettingsService(t, db)
	taskEntity := seedRetentionTask(t, db)
	invalidValues := []string{"0", "-1", "abc", "366", "9223372036854775808"}
	for _, value := range invalidValues {
		t.Run("value_"+strings.NewReplacer("-", "negative_", " ", "_").Replace(value), func(t *testing.T) {
			if err := settingsSvc.Update("retention.task_traffic_days", "30"); err != nil {
				t.Fatalf("seed traffic retention setting: %v", err)
			}
			if err := db.Model(&model.SystemSetting{}).
				Where("key = ?", "retention.task_traffic_days").
				Update("value", value).Error; err != nil {
				t.Fatalf("corrupt traffic retention setting with %q: %v", value, err)
			}
			sample := seedRetentionSample(t, db, taskEntity, time.Now().UTC().Add(-10*24*time.Hour))
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, settingsSvc, nil, 1, 90)
			shutdownManagerOnCleanup(t, manager)
			stopRetentionWorkers(t, manager)
			manager.sampleWriter.cleanupExpired()
			if got := retentionSampleCount(t, db, sample.ID); got != 1 {
				t.Fatalf("invalid DB retention=%q deleted sample, count=%d", value, got)
			}
		})
	}
}
