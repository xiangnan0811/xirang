package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func retentionCaptureManifest(t *testing.T) string {
	t.Helper()
	raw, err := model.EncodeRsyncCaptureManifest(model.RsyncCaptureManifest{
		Version: 1,
		Layout:  model.TaskRunCaptureLayoutDirectoryContents,
		Entries: []model.RsyncCaptureManifestEntry{{Path: "", Kind: "directory"}},
	})
	if err != nil {
		t.Fatalf("encode retention capture manifest: %v", err)
	}
	return raw
}

func seedRetentionGeneration(
	t *testing.T,
	db *gorm.DB,
	taskEntity model.Task,
	createdAt time.Time,
	status string,
	generationState string,
) model.TaskRun {
	t.Helper()
	var loaded model.Task
	if err := db.Preload("Node").Preload("Policy").First(&loaded, taskEntity.ID).Error; err != nil {
		t.Fatalf("load retention task: %v", err)
	}
	finishedAt := createdAt.Add(time.Minute)
	captureManifest := retentionCaptureManifest(t)
	capture, err := model.DecodeRsyncCaptureManifest(captureManifest)
	if err != nil {
		t.Fatalf("decode retention capture manifest: %v", err)
	}
	captureRoot, err := model.EncodeRsyncCaptureRootSidecar(capture.Root)
	if err != nil {
		t.Fatalf("encode retention capture root sidecar: %v", err)
	}
	run := model.TaskRun{
		TaskID:                  loaded.ID,
		NodeIDSnapshot:          loaded.NodeID,
		TriggerType:             "manual",
		Status:                  status,
		BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(loaded),
		BackupCaptureLayout:     model.TaskRunCaptureLayoutDirectoryContents,
		BackupCaptureRoot:       captureRoot,
		BackupCaptureManifest:   captureManifest,
		BackupGenerationState:   generationState,
		CreatedAt:               createdAt,
		UpdatedAt:               createdAt,
		FinishedAt:              &finishedAt,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create retention generation: %v", err)
	}
	return run
}

func seedExpiredHistoryRun(t *testing.T, db *gorm.DB, taskEntity model.Task, createdAt time.Time) model.TaskRun {
	t.Helper()
	run := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: taskEntity.NodeID,
		TriggerType:    "manual",
		Status:         model.TaskRunStatusFailed,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create expired history run: %v", err)
	}
	return run
}

func retentionManager(db *gorm.DB) *Manager {
	return &Manager{db: db, taskRunRetentionDays: 1}
}

func TestTaskRunCaptureSidecarPersistencePreservesRawBytes(t *testing.T) {
	runTaskRunCaptureSidecarPersistencePreservesRawBytes(t, openManagerTestDB(t))
}

func TestTaskRunCaptureSidecarPersistencePreservesRawBytesPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunCaptureSidecarPersistencePreservesRawBytes(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunCaptureSidecarPersistencePreservesRawBytes(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedRetentionTask(t, db)
	var loadedTask model.Task
	if err := db.Preload("Node").Preload("Policy").First(&loadedTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("load sidecar task: %v", err)
	}
	root := string([]byte{'r', 'o', 'o', 't', '-', 0x80, 0xff})
	path := string([]byte{'e', 'n', 't', 'r', 'y', '-', 0x81, 0xfe})
	linkTarget := string([]byte{'/', 't', 'a', 'r', 'g', 'e', 't', '-', 0x82, 0xfd})
	manifest, err := model.EncodeRsyncCaptureManifest(model.RsyncCaptureManifest{
		Version: 2,
		Layout:  model.TaskRunCaptureLayoutDirectoryRoot,
		Root:    root,
		Entries: []model.RsyncCaptureManifestEntry{
			{Path: "", Kind: "directory"},
			{Path: path, Kind: "symlink", LinkTarget: linkTarget},
		},
	})
	if err != nil {
		t.Fatalf("encode v2 sidecar manifest: %v", err)
	}
	sidecar, err := model.EncodeRsyncCaptureRootSidecar(root)
	if err != nil {
		t.Fatalf("encode v2 root sidecar: %v", err)
	}
	now := time.Now().UTC()
	run := model.TaskRun{
		TaskID:                  taskEntity.ID,
		NodeIDSnapshot:          taskEntity.NodeID,
		TriggerType:             "manual",
		Status:                  model.TaskRunStatusSuccess,
		BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(loadedTask),
		BackupCaptureLayout:     model.TaskRunCaptureLayoutDirectoryRoot,
		BackupCaptureRoot:       sidecar,
		BackupCaptureManifest:   manifest,
		BackupGenerationState:   model.TaskRunGenerationStateVerified,
		CreatedAt:               now,
		UpdatedAt:               now,
		FinishedAt:              &now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("persist v2 sidecar run: %v", err)
	}
	loaded, err := retentionManager(db).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("load v2 sidecar restore provenance: %v", err)
	}
	if loaded.RsyncCaptureRoot != root {
		t.Fatalf("decoded v2 root bytes=%v, want %v", []byte(loaded.RsyncCaptureRoot), []byte(root))
	}
	if loaded.RsyncCaptureGenerationID != run.ID {
		t.Fatalf("decoded v2 generation=%d, want %d", loaded.RsyncCaptureGenerationID, run.ID)
	}
	decoded, err := model.DecodeRsyncCaptureManifest(loaded.RsyncCaptureManifest)
	if err != nil {
		t.Fatalf("decode persisted v2 manifest: %v", err)
	}
	if decoded.Root != root || decoded.Entries[1].Path != path || decoded.Entries[1].LinkTarget != linkTarget {
		t.Fatalf("decoded v2 evidence root=%v path=%v link=%v", []byte(decoded.Root), []byte(decoded.Entries[1].Path), []byte(decoded.Entries[1].LinkTarget))
	}

	legacyTask := seedRetentionTask(t, db)
	var loadedLegacyTask model.Task
	if err := db.Preload("Node").Preload("Policy").First(&loadedLegacyTask, legacyTask.ID).Error; err != nil {
		t.Fatalf("load legacy sidecar task: %v", err)
	}
	legacyManifest := `{"version":1,"layout":"directory_root","root":"legacy-root","entries":[{"path":"","kind":"directory"}]}`
	legacyNow := now.Add(time.Second)
	legacyRun := model.TaskRun{
		TaskID:                  legacyTask.ID,
		NodeIDSnapshot:          legacyTask.NodeID,
		TriggerType:             "manual",
		Status:                  model.TaskRunStatusSuccess,
		BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(loadedLegacyTask),
		BackupCaptureLayout:     model.TaskRunCaptureLayoutDirectoryRoot,
		BackupCaptureRoot:       "legacy-root",
		BackupCaptureManifest:   legacyManifest,
		BackupGenerationState:   model.TaskRunGenerationStateVerified,
		CreatedAt:               legacyNow,
		UpdatedAt:               legacyNow,
		FinishedAt:              &legacyNow,
	}
	if err := db.Create(&legacyRun).Error; err != nil {
		t.Fatalf("persist v1 raw sidecar run: %v", err)
	}
	legacyLoaded, err := retentionManager(db).loadRestoreTaskWithProvenance(context.Background(), legacyTask.ID)
	if err != nil {
		t.Fatalf("load v1 raw sidecar restore provenance: %v", err)
	}
	if legacyLoaded.RsyncCaptureRoot != "legacy-root" {
		t.Fatalf("decoded v1 root=%q, want legacy-root", legacyLoaded.RsyncCaptureRoot)
	}
}

func TestTaskRunRetentionKeepsLastValidGenerationAcrossRetention(t *testing.T) {
	runTaskRunRetentionKeepsLastValidGenerationAcrossRetention(t, openManagerTestDB(t))
}

func TestTaskRunRetentionKeepsLastValidGenerationAcrossRetentionPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsLastValidGenerationAcrossRetention(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsLastValidGenerationAcrossRetention(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-72 * time.Hour)
	source := seedRetentionGeneration(t, db, taskEntity, old, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified)
	history := seedExpiredHistoryRun(t, db, taskEntity, old.Add(time.Minute))
	manager := retentionManager(db)
	manager.cleanupExpiredTaskRuns()
	var kept model.TaskRun
	if err := db.First(&kept, source.ID).Error; err != nil {
		t.Fatalf("last valid capture generation was deleted: %v", err)
	}
	if kept.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("kept generation state=%q, want verified", kept.BackupGenerationState)
	}
	var historyCount int64
	if err := db.Model(&model.TaskRun{}).Where("id = ?", history.ID).Count(&historyCount).Error; err != nil {
		t.Fatalf("count expired history: %v", err)
	}
	if historyCount != 0 {
		t.Fatalf("ordinary history row survived cleanup")
	}
	loaded, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("last valid capture was not recoverable: %v", err)
	}
	if loaded.RsyncCaptureGenerationID != source.ID {
		t.Fatalf("restore selected generation %d, want %d", loaded.RsyncCaptureGenerationID, source.ID)
	}
}

func TestTaskRunRetentionKeepsCurrentDirtyGenerationAndBlocksFallback(t *testing.T) {
	db := openManagerTestDB(t)
	runTaskRunRetentionKeepsCurrentDirtyGenerationAndBlocksFallback(t, db)
}

func TestTaskRunRetentionKeepsCurrentDirtyGenerationAndBlocksFallbackPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsCurrentDirtyGenerationAndBlocksFallback(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsCurrentDirtyGenerationAndBlocksFallback(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-72 * time.Hour)
	verified := seedRetentionGeneration(t, db, taskEntity, old, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified)
	dirty := seedRetentionGeneration(t, db, taskEntity, old.Add(time.Minute), model.TaskRunStatusFailed, model.TaskRunGenerationStateDirty)
	manager := retentionManager(db)
	manager.cleanupExpiredTaskRuns()
	var verifiedCount int64
	if err := db.Model(&model.TaskRun{}).Where("id = ?", verified.ID).Count(&verifiedCount).Error; err != nil {
		t.Fatalf("count old verified generation: %v", err)
	}
	if verifiedCount != 0 {
		t.Fatalf("old verified generation survived a newer dirty generation")
	}
	var current model.TaskRun
	if err := db.First(&current, dirty.ID).Error; err != nil {
		t.Fatalf("current dirty generation was deleted: %v", err)
	}
	if current.BackupGenerationState != model.TaskRunGenerationStateDirty {
		t.Fatalf("current generation state=%q, want dirty", current.BackupGenerationState)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("restore error=%v, want ErrRestoreRequiresNewBackup", err)
	}
}

func TestTaskRunRetentionKeepsOlderUnresolvedGeneration(t *testing.T) {
	runTaskRunRetentionKeepsOlderUnresolvedGeneration(t, openManagerTestDB(t))
}

func TestTaskRunRetentionKeepsOlderUnresolvedGenerationPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsOlderUnresolvedGeneration(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsOlderUnresolvedGeneration(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-72 * time.Hour)
	unresolved := seedRetentionGeneration(t, db, taskEntity, old, model.TaskRunStatusFailed, model.TaskRunGenerationStateUnknown)
	latest := seedRetentionGeneration(t, db, taskEntity, old.Add(time.Minute), model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified)
	history := seedExpiredHistoryRun(t, db, taskEntity, old.Add(2*time.Minute))
	retentionManager(db).cleanupExpiredTaskRuns()

	var count int64
	if err := db.Model(&model.TaskRun{}).Where("id = ?", unresolved.ID).Count(&count).Error; err != nil {
		t.Fatalf("count older unresolved generation: %v", err)
	}
	if count != 1 {
		t.Fatal("older unresolved generation was deleted")
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", latest.ID).Count(&count).Error; err != nil {
		t.Fatalf("count latest verified generation: %v", err)
	}
	if count != 1 {
		t.Fatal("latest verified generation was deleted")
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", history.ID).Count(&count).Error; err != nil {
		t.Fatalf("count ordinary expired history: %v", err)
	}
	if count != 0 {
		t.Fatal("ordinary expired history survived cleanup")
	}
}

func TestTaskRunRetentionKeepsRcloneVerifiedReferenceAndDirtyHead(t *testing.T) {
	runTaskRunRetentionKeepsRcloneVerifiedReferenceAndDirtyHead(t, openManagerTestDB(t))
}

func TestTaskRunRetentionKeepsRcloneVerifiedReferenceAndDirtyHeadPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsRcloneVerifiedReferenceAndDirtyHead(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsRcloneVerifiedReferenceAndDirtyHead(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	old := time.Now().UTC().Add(-72 * time.Hour)
	source := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", old)
	restore := model.TaskRun{
		TaskID:            taskEntity.ID,
		NodeIDSnapshot:    taskEntity.NodeID,
		TriggerType:       "restore",
		Status:            model.TaskRunStatusPending,
		BackupSourceRunID: source.ID,
		CreatedAt:         old.Add(time.Minute),
		UpdatedAt:         old.Add(time.Minute),
	}
	if err := db.Create(&restore).Error; err != nil {
		t.Fatalf("create Rclone source reference: %v", err)
	}
	dirty := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusFailed, model.TaskRunGenerationStateDirty, "known partial write", old.Add(2*time.Minute))
	retentionManager(db).cleanupExpiredTaskRuns()

	var count int64
	if err := db.Model(&model.TaskRun{}).Where("id IN ?", []uint{source.ID, dirty.ID}).Count(&count).Error; err != nil {
		t.Fatalf("count Rclone verified/dirty generations: %v", err)
	}
	if count != 2 {
		t.Fatalf("Rclone verified source or dirty head was deleted, count=%d", count)
	}
	if _, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("Rclone dirty head restore error=%v, want ErrRestoreRequiresNewBackup", err)
	}
}

func TestTaskRunRetentionKeepsLoneRcloneVerifiedHead(t *testing.T) {
	runTaskRunRetentionKeepsLoneRcloneVerifiedHead(t, openManagerTestDB(t))
}

func TestTaskRunRetentionKeepsLoneRcloneVerifiedHeadPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsLoneRcloneVerifiedHead(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsLoneRcloneVerifiedHead(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	head := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", time.Now().UTC().Add(-72*time.Hour))
	retentionManager(db).cleanupExpiredTaskRuns()

	var count int64
	if err := db.Model(&model.TaskRun{}).Where("id = ?", head.ID).Count(&count).Error; err != nil {
		t.Fatalf("count lone Rclone verified head: %v", err)
	}
	if count != 1 {
		t.Fatal("lone Rclone verified head was deleted")
	}
	loaded, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil || loaded.RsyncCaptureGenerationID != head.ID {
		t.Fatalf("lone Rclone verified head restore generation=%d err=%v, want %d", loaded.RsyncCaptureGenerationID, err, head.ID)
	}
}

func TestTaskRunRetentionKeepsRcloneVerifiedHeadAfterNoStart(t *testing.T) {
	runTaskRunRetentionKeepsRcloneVerifiedHeadAfterNoStart(t, openManagerTestDB(t))
}

func TestTaskRunRetentionKeepsRcloneVerifiedHeadAfterNoStartPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsRcloneVerifiedHeadAfterNoStart(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsRcloneVerifiedHeadAfterNoStart(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	old := time.Now().UTC().Add(-72 * time.Hour)
	head := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", old)
	noStart := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusFailed, model.TaskRunGenerationStateNoStart, "", old.Add(time.Minute))
	retentionManager(db).cleanupExpiredTaskRuns()

	var count int64
	if err := db.Model(&model.TaskRun{}).Where("id IN ?", []uint{head.ID, noStart.ID}).Count(&count).Error; err != nil {
		t.Fatalf("count Rclone head/no-start rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("Rclone verified head was deleted before no-start skip, count=%d", count)
	}
	loaded, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil || loaded.RsyncCaptureGenerationID != head.ID {
		t.Fatalf("Rclone no-start restore generation=%d err=%v, want %d", loaded.RsyncCaptureGenerationID, err, head.ID)
	}
}

func TestTaskRunRetentionKeepsRcloneAmbiguousEmptyHead(t *testing.T) {
	runTaskRunRetentionKeepsRcloneAmbiguousEmptyHead(t, openManagerTestDB(t))
}

func TestTaskRunRetentionKeepsRcloneAmbiguousEmptyHeadPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsRcloneAmbiguousEmptyHead(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsRcloneAmbiguousEmptyHead(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	old := time.Now().UTC().Add(-72 * time.Hour)
	source := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", old)
	head := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusFailed, "", "ambiguous partial write", old.Add(time.Minute))
	retentionManager(db).cleanupExpiredTaskRuns()

	var count int64
	if err := db.Model(&model.TaskRun{}).Where("id IN ?", []uint{source.ID, head.ID}).Count(&count).Error; err != nil {
		t.Fatalf("count Rclone verified/ambiguous rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("Rclone ambiguous empty head or predecessor was deleted, count=%d", count)
	}
	if _, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("Rclone ambiguous empty head restore error=%v, want ErrRestoreRequiresNewBackup", err)
	}
}

func TestTaskRunRetentionPreservesPredecessorDuringActiveDirtyNoStart(t *testing.T) {
	runTaskRunRetentionPreservesPredecessorDuringActiveDirtyNoStart(t, openManagerTestDB(t))
}

func TestTaskRunRetentionPreservesPredecessorDuringActiveDirtyNoStartPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionPreservesPredecessorDuringActiveDirtyNoStart(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionPreservesPredecessorDuringActiveDirtyNoStart(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-72 * time.Hour)
	source := seedRetentionGeneration(t, db, taskEntity, old, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified)

	manager := NewManager(db, stubExecutorFactory{
		executor: &noStartCompatibilityExecutor{err: errors.New("RETENTION_NO_START_FOR_TEST_ONLY")},
	}, nil, nil, nil, nil, 8, 1)
	shutdownManagerOnCleanup(t, manager)
	manager.afterLegacyRsyncGenerationArm = func() {
		// Exercise the real runner lifecycle at the only interval where a
		// dirty successor is active but has not yet been cleared.
		manager.cleanupExpiredTaskRuns()
	}

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
	var noStartRun model.TaskRun
	if err := db.First(&noStartRun, runID).Error; err != nil {
		t.Fatalf("load no-start run: %v", err)
	}
	if noStartRun.Status != model.TaskRunStatusFailed || noStartRun.BackupGenerationState != "" {
		t.Fatalf("no-start run status=%q state=%q, want failed/empty", noStartRun.Status, noStartRun.BackupGenerationState)
	}
	var kept model.TaskRun
	if err := db.First(&kept, source.ID).Error; err != nil {
		t.Fatalf("active dirty successor caused valid predecessor deletion: %v", err)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("verified predecessor was not recoverable after no-start clear: %v", err)
	}
}

func TestTaskRunRetentionKeepsPendingRestoreSourceReference(t *testing.T) {
	db := openManagerTestDB(t)
	runTaskRunRetentionKeepsPendingRestoreSourceReference(t, db)
}

func TestTaskRunRetentionKeepsPendingRestoreSourceReferencePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsPendingRestoreSourceReference(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsPendingRestoreSourceReference(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-72 * time.Hour)
	source := seedRetentionGeneration(t, db, taskEntity, old, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified)
	restore := model.TaskRun{
		TaskID:            taskEntity.ID,
		NodeIDSnapshot:    taskEntity.NodeID,
		TriggerType:       "restore",
		Status:            model.TaskRunStatusPending,
		BackupSourceRunID: source.ID,
		CreatedAt:         old.Add(time.Minute),
		UpdatedAt:         old.Add(time.Minute),
	}
	if err := db.Create(&restore).Error; err != nil {
		t.Fatalf("create pending restore run: %v", err)
	}
	retentionManager(db).cleanupExpiredTaskRuns()
	var kept model.TaskRun
	if err := db.First(&kept, source.ID).Error; err != nil {
		t.Fatalf("pending restore source was deleted: %v", err)
	}
	if kept.ID != source.ID {
		t.Fatalf("kept source id=%d, want %d", kept.ID, source.ID)
	}
}

func TestTaskRunRetentionKeepsActiveDrillSourceReference(t *testing.T) {
	db := openManagerTestDB(t)
	runTaskRunRetentionKeepsActiveDrillSourceReference(t, db)
}

func TestTaskRunRetentionKeepsActiveDrillSourceReferencePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionKeepsActiveDrillSourceReference(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionKeepsActiveDrillSourceReference(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-72 * time.Hour)
	source := seedRetentionGeneration(t, db, taskEntity, old, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified)
	drill := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: taskEntity.NodeID,
		TriggerType:    "drill",
		Status:         model.TaskRunStatusPending,
		CreatedAt:      old.Add(time.Minute),
		UpdatedAt:      old.Add(time.Minute),
	}
	if err := db.Create(&drill).Error; err != nil {
		t.Fatalf("create pending drill run: %v", err)
	}
	evidence := model.RestoreDrillEvidence{
		TaskID:           taskEntity.ID,
		TaskRunID:        drill.ID,
		SourceTaskRunID:  &source.ID,
		SandboxNodeID:    taskEntity.NodeID,
		SandboxPath:      "/tmp/xirang-retention-drill",
		Status:           model.TaskRunStatusPending,
		RestoreStatus:    model.TaskRunStatusPending,
		VerifyStatus:     model.TaskRunStatusPending,
		PostVerifyStatus: model.TaskRunStatusSkipped,
		CleanupStatus:    model.TaskRunStatusSkipped,
		CreatedAt:        old.Add(time.Minute),
		UpdatedAt:        old.Add(time.Minute),
	}
	if err := db.Create(&evidence).Error; err != nil {
		t.Fatalf("create active drill evidence: %v", err)
	}
	retentionManager(db).cleanupExpiredTaskRuns()
	var kept model.TaskRun
	if err := db.First(&kept, source.ID).Error; err != nil {
		t.Fatalf("active drill source was deleted: %v", err)
	}
}

func TestDrillReservationRevalidatesSourceAfterRetention(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-72 * time.Hour)
	source := model.TaskRun{
		TaskID:         taskEntity.ID,
		NodeIDSnapshot: taskEntity.NodeID,
		TriggerType:    "manual",
		Status:         model.TaskRunStatusSuccess,
		CreatedAt:      old,
		UpdatedAt:      old,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create drill source: %v", err)
	}
	manager := &Manager{
		db:                 db,
		drillOwnerID:       "retention-drill-owner",
		drillRecoveryLease: time.Minute,
		nodeWriteAdmission: &nodeWriteAdmissionFake{},
	}
	policy := model.Policy{ID: 1, DrillRestorePath: "/tmp/xirang-retention-drill"}
	sandbox := model.Node{ID: taskEntity.NodeID + 1, Name: "retention-sandbox"}
	evidence, err := manager.pendingDrillEvidence(context.Background(), policy, taskEntity, sandbox)
	if err != nil {
		t.Fatalf("prepare drill evidence: %v", err)
	}
	if evidence.SourceTaskRunID == nil || *evidence.SourceTaskRunID != source.ID {
		t.Fatalf("preflight source=%v, want %d", evidence.SourceTaskRunID, source.ID)
	}

	retentionManager(db).cleanupExpiredTaskRuns()
	var sourceCount int64
	if err := db.Model(&model.TaskRun{}).Where("id = ?", source.ID).Count(&sourceCount).Error; err != nil {
		t.Fatalf("count cleaned drill source: %v", err)
	}
	if sourceCount != 0 {
		t.Fatal("source without capture evidence unexpectedly survived cleanup")
	}

	run, err := manager.reserveDrillRun(context.Background(), taskEntity, evidence)
	if err != nil {
		t.Fatalf("reserve drill after source cleanup: %v", err)
	}
	var stored model.RestoreDrillEvidence
	if err := db.Where("task_run_id = ?", run.ID).First(&stored).Error; err != nil {
		t.Fatalf("load reserved drill evidence: %v", err)
	}
	if stored.SourceTaskRunID != nil {
		t.Fatalf("reserved drill retained stale source reference %d", *stored.SourceTaskRunID)
	}
}

func TestTaskRunRetentionCleanupAndDrillReservationSerialize(t *testing.T) {
	runTaskRunRetentionCleanupAndDrillReservationSerialize(t, openConcurrentManagerTestDB(t))
}

func TestTaskRunRetentionCleanupAndDrillReservationSerializePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionCleanupAndDrillReservationSerialize(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionCleanupAndDrillReservationSerialize(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, reserveFirst := range []bool{true, false} {
		name := "cleanup-first"
		if reserveFirst {
			name = "reserve-first"
		}
		t.Run(name, func(t *testing.T) {
			taskEntity := seedRetentionTask(t, db)
			old := time.Now().UTC().Add(-72 * time.Hour)
			source := model.TaskRun{
				TaskID:         taskEntity.ID,
				NodeIDSnapshot: taskEntity.NodeID,
				TriggerType:    "manual",
				Status:         model.TaskRunStatusSuccess,
				CreatedAt:      old,
				UpdatedAt:      old,
			}
			if err := db.Create(&source).Error; err != nil {
				t.Fatalf("create drill source: %v", err)
			}
			manager := &Manager{
				db:                 db,
				drillOwnerID:       fmt.Sprintf("retention-drill-owner-%d", taskEntity.ID),
				drillRecoveryLease: time.Minute,
				nodeWriteAdmission: &nodeWriteAdmissionFake{},
			}
			policy := model.Policy{ID: 1, DrillRestorePath: "/tmp/xirang-retention-drill"}
			sandbox := model.Node{ID: taskEntity.NodeID + 1, Name: "retention-sandbox"}
			evidence, err := manager.pendingDrillEvidence(context.Background(), policy, taskEntity, sandbox)
			if err != nil {
				t.Fatalf("prepare drill evidence: %v", err)
			}
			if evidence.SourceTaskRunID == nil || *evidence.SourceTaskRunID != source.ID {
				t.Fatalf("preflight source=%v, want %d", evidence.SourceTaskRunID, source.ID)
			}

			cleanupManager := retentionManager(db)
			var reserveEntered <-chan struct{}
			var cleanupEntered <-chan struct{}
			var release func()
			var removeGate func()
			if reserveFirst {
				reserveEntered, release, removeGate = installTaskRunCreateGate(t, db, taskEntity.ID, "drill")
			} else {
				cleanupEntered, release, removeGate = installTaskRowUpdateGate(t, db)
			}
			var wait sync.WaitGroup
			var cleanupCount int64
			var cleanupErr error
			var reserved model.TaskRun
			var reserveErr error
			cleanupDone := make(chan struct{})
			reserveDone := make(chan struct{})
			startCleanup := func() {
				wait.Add(1)
				go func() {
					defer wait.Done()
					cleanupCount, cleanupErr = cleanupManager.cleanupExpiredTaskRunBatch(time.Now().UTC().Add(-24 * time.Hour))
					close(cleanupDone)
				}()
			}
			startReserve := func() {
				wait.Add(1)
				go func() {
					defer wait.Done()
					reserved, reserveErr = manager.reserveDrillRun(context.Background(), taskEntity, evidence)
					close(reserveDone)
				}()
			}
			if reserveFirst {
				startReserve()
				select {
				case <-reserveEntered:
				case <-time.After(3 * time.Second):
					t.Fatal("drill reserve transaction did not reach task-run create gate")
				}
				startCleanup()
				select {
				case <-cleanupDone:
					t.Fatal("cleanup completed while drill reservation held task lock")
				case <-time.After(250 * time.Millisecond):
				}
			} else {
				startCleanup()
				select {
				case <-cleanupEntered:
				case <-time.After(3 * time.Second):
					t.Fatal("cleanup transaction did not reach task-row lock gate")
				}
				startReserve()
				select {
				case <-reserveDone:
					t.Fatal("drill reservation completed while cleanup held task lock")
				case <-time.After(250 * time.Millisecond):
				}
			}
			release()
			wait.Wait()
			removeGate()
			if cleanupErr != nil {
				t.Fatalf("cleanup error: %v", cleanupErr)
			}
			if reserveErr != nil {
				t.Fatalf("drill reservation error: %v", reserveErr)
			}
			if !reserveFirst && cleanupCount == 0 {
				t.Fatal("cleanup did not process the expired drill source")
			}
			var stored model.RestoreDrillEvidence
			if err := db.Where("task_run_id = ?", reserved.ID).First(&stored).Error; err != nil {
				t.Fatalf("load reserved drill evidence: %v", err)
			}
			var dangling int64
			if err := db.Model(&model.RestoreDrillEvidence{}).
				Where("source_task_run_id IS NOT NULL").
				Where("NOT EXISTS (SELECT 1 FROM task_runs AS source WHERE source.id = restore_drill_evidences.source_task_run_id)").
				Count(&dangling).Error; err != nil {
				t.Fatalf("count dangling drill references: %v", err)
			}
			if dangling != 0 {
				t.Fatalf("cleanup left %d dangling drill references", dangling)
			}
			if reserveFirst {
				if stored.SourceTaskRunID == nil || *stored.SourceTaskRunID != source.ID {
					t.Fatalf("reserve-first source=%v, want %d", stored.SourceTaskRunID, source.ID)
				}
				if err := db.First(&model.TaskRun{}, source.ID).Error; err != nil {
					t.Fatalf("reserve-first source was deleted: %v", err)
				}
			} else if stored.SourceTaskRunID != nil {
				t.Fatalf("cleanup-first retained stale source reference %d", *stored.SourceTaskRunID)
			}
		})
	}
}

func TestTaskRunRetentionCleanupAndRestoreReservationSerialize(t *testing.T) {
	runTaskRunRetentionCleanupAndRestoreReservationSerialize(t, openConcurrentManagerTestDB(t))
}

func TestTaskRunRetentionCleanupAndRestoreReservationSerializePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionCleanupAndRestoreReservationSerialize(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionCleanupAndRestoreReservationSerialize(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, reserveFirst := range []bool{true, false} {
		name := "cleanup-first"
		if reserveFirst {
			name = "reserve-first"
		}
		t.Run(name, func(t *testing.T) {
			taskEntity := seedRetentionTask(t, db)
			old := time.Now().UTC().Add(-72 * time.Hour)
			obsolete := seedRetentionGeneration(t, db, taskEntity, old, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified)
			source := seedRetentionGeneration(t, db, taskEntity, old.Add(time.Minute), model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified)
			historyLog := model.TaskLog{
				TaskID:    taskEntity.ID,
				TaskRunID: &obsolete.ID,
				Level:     "info",
				Message:   "obsolete retention generation",
				CreatedAt: old,
			}
			if err := db.Create(&historyLog).Error; err != nil {
				t.Fatalf("create obsolete generation log: %v", err)
			}

			cleanupManager := retentionManager(db)
			reserveManager := &Manager{db: db}
			requested := model.TaskRun{
				TaskID:            taskEntity.ID,
				TriggerType:       "restore",
				Status:            model.TaskRunStatusPending,
				BackupSourceRunID: source.ID,
			}
			var reserveEntered <-chan struct{}
			var cleanupEntered <-chan struct{}
			var release func()
			var removeGate func()
			if reserveFirst {
				reserveEntered, release, removeGate = installTaskRunCreateGate(t, db, taskEntity.ID, "restore")
			} else {
				cleanupEntered, release, removeGate = installTaskRowUpdateGate(t, db)
			}

			var wait sync.WaitGroup
			var cleanupCount int64
			var cleanupErr error
			var reserved model.TaskRun
			var reserveErr error
			cleanupDone := make(chan struct{})
			reserveDone := make(chan struct{})
			startCleanup := func() {
				wait.Add(1)
				go func() {
					defer wait.Done()
					cleanupCount, cleanupErr = cleanupManager.cleanupExpiredTaskRunBatch(time.Now().UTC().Add(-24 * time.Hour))
					close(cleanupDone)
				}()
			}
			startReserve := func() {
				wait.Add(1)
				go func() {
					defer wait.Done()
					reserved, reserveErr = reserveManager.reserveTaskRun(context.Background(), taskEntity.NodeID, requested)
					close(reserveDone)
				}()
			}
			if reserveFirst {
				// The reserve transaction acquires the task lock before its
				// create gate. Cleanup must wait until this transaction
				// commits, then recheck its obsolete candidate set.
				startReserve()
				select {
				case <-reserveEntered:
				case <-time.After(3 * time.Second):
					t.Fatal("reserve transaction did not reach task-run create gate")
				}
				startCleanup()
				select {
				case <-cleanupDone:
					t.Fatal("cleanup completed while reservation held task lock")
				case <-time.After(250 * time.Millisecond):
				}
			} else {
				// Cleanup locks the task before the recheck. Reservation must
				// wait until cleanup commits, so the source reference cannot
				// be created against a stale candidate snapshot.
				startCleanup()
				select {
				case <-cleanupEntered:
				case <-time.After(3 * time.Second):
					t.Fatal("cleanup transaction did not reach task-row lock gate")
				}
				startReserve()
				select {
				case <-reserveDone:
					t.Fatal("reservation completed while cleanup held task lock")
				case <-time.After(250 * time.Millisecond):
				}
			}
			release()
			wait.Wait()
			removeGate()
			if cleanupErr != nil {
				t.Fatalf("cleanup error: %v", cleanupErr)
			}
			if reserveErr != nil {
				t.Fatalf("reservation error: %v", reserveErr)
			}
			if cleanupCount == 0 {
				t.Fatal("cleanup did not delete the obsolete generation")
			}
			if reserved.BackupSourceRunID != source.ID {
				t.Fatalf("reserved source id=%d, want %d", reserved.BackupSourceRunID, source.ID)
			}
			var kept model.TaskRun
			if err := db.First(&kept, source.ID).Error; err != nil {
				t.Fatalf("successful reservation lost its source: %v", err)
			}
			var obsoleteCount int64
			if err := db.Model(&model.TaskRun{}).Where("id = ?", obsolete.ID).Count(&obsoleteCount).Error; err != nil {
				t.Fatalf("count obsolete generation: %v", err)
			}
			if obsoleteCount != 0 {
				t.Fatal("obsolete generation survived cleanup")
			}
			var logCount int64
			if err := db.Model(&model.TaskLog{}).Where("id = ?", historyLog.ID).Count(&logCount).Error; err != nil {
				t.Fatalf("count obsolete generation log: %v", err)
			}
			if logCount != 0 {
				t.Fatal("obsolete generation log survived cleanup")
			}
			var dangling int64
			if err := db.Model(&model.TaskRun{}).
				Where("backup_source_run_id <> 0").
				Where("NOT EXISTS (SELECT 1 FROM task_runs AS source WHERE source.id = task_runs.backup_source_run_id)").
				Count(&dangling).Error; err != nil {
				t.Fatalf("count dangling restore references: %v", err)
			}
			if dangling != 0 {
				t.Fatalf("cleanup left %d dangling restore references", dangling)
			}
		})
	}
}
func TestTaskRunRetentionCleanupIsAtomic(t *testing.T) {
	runTaskRunRetentionCleanupIsAtomic(t, openManagerTestDB(t))
}

func TestTaskRunRetentionCleanupIsAtomicPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTaskRunRetentionCleanupIsAtomic(t, openTaskRetentionPostgresDB(t, dsn))
}

func runTaskRunRetentionCleanupIsAtomic(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedRetentionTask(t, db)
	old := time.Now().UTC().Add(-72 * time.Hour)
	run := seedExpiredHistoryRun(t, db, taskEntity, old)
	log := model.TaskLog{
		TaskID:    taskEntity.ID,
		TaskRunID: &run.ID,
		Level:     "info",
		Message:   "retention atomicity",
		CreatedAt: old,
	}
	if err := db.Create(&log).Error; err != nil {
		t.Fatalf("create retention log: %v", err)
	}
	alert := model.Alert{
		NodeID:      taskEntity.NodeID,
		NodeName:    "retention-node",
		TaskID:      &taskEntity.ID,
		TaskRunID:   &run.ID,
		Severity:    "warning",
		Status:      "resolved",
		ErrorCode:   "RETENTION_ATOMICITY",
		Message:     "retention atomicity",
		TriggeredAt: old,
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create retention alert: %v", err)
	}

	callbackName := fmt.Sprintf("test:retention-cleanup-rollback-%d", run.ID)
	injected := false
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "alerts" && !injected {
			injected = true
			injectedErr := errors.New("RETENTION_CLEANUP_ATOMICITY_INJECTION")
			_ = tx.AddError(injectedErr)
		}
	}); err != nil {
		t.Fatalf("register retention rollback injection: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(callbackName)
	})

	retentionManager(db).cleanupExpiredTaskRuns()
	if !injected {
		t.Fatal("retention cleanup did not reach alert reference update")
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Count(&runCount).Error; err != nil {
		t.Fatalf("count rolled-back task run: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("task run count=%d after rollback, want 1", runCount)
	}
	var logCount int64
	if err := db.Model(&model.TaskLog{}).Where("id = ?", log.ID).Count(&logCount).Error; err != nil {
		t.Fatalf("count rolled-back task log: %v", err)
	}
	if logCount != 1 {
		t.Fatalf("task log count=%d after rollback, want 1", logCount)
	}
	var storedAlert model.Alert
	if err := db.First(&storedAlert, alert.ID).Error; err != nil {
		t.Fatalf("load rolled-back alert: %v", err)
	}
	if storedAlert.TaskRunID == nil || *storedAlert.TaskRunID != run.ID {
		t.Fatalf("alert task_run_id=%v after rollback, want %d", storedAlert.TaskRunID, run.ID)
	}
}

func installTaskRunCreateGate(t *testing.T, db *gorm.DB, taskID uint, triggerType string) (<-chan struct{}, func(), func()) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	callbackName := fmt.Sprintf("test:retention-reserve-create-gate-%d-%s", taskID, triggerType)
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "task_runs" {
			return
		}
		run, ok := tx.Statement.Dest.(*model.TaskRun)
		if !ok || run.TaskID != taskID || !strings.EqualFold(run.TriggerType, triggerType) {
			return
		}
		enteredOnce.Do(func() {
			close(entered)
			<-release
		})
	}); err != nil {
		t.Fatalf("register reserve create gate: %v", err)
	}
	var removeOnce sync.Once
	remove := func() {
		removeOnce.Do(func() {
			_ = db.Callback().Create().Remove(callbackName)
		})
	}
	unblock := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(func() {
		unblock()
		remove()
	})
	return entered, unblock, remove
}

func installTaskRowUpdateGate(t *testing.T, db *gorm.DB) (<-chan struct{}, func(), func()) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	callbackName := fmt.Sprintf("test:retention-task-lock-gate-%d", time.Now().UnixNano())
	if err := db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "tasks" {
			return
		}
		enteredOnce.Do(func() {
			close(entered)
			<-release
		})
	}); err != nil {
		t.Fatalf("register task lock gate: %v", err)
	}
	var removeOnce sync.Once
	remove := func() {
		removeOnce.Do(func() {
			_ = db.Callback().Update().Remove(callbackName)
		})
	}
	unblock := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(func() {
		unblock()
		remove()
	})
	return entered, unblock, remove
}

func seedRetentionTask(t *testing.T, db *gorm.DB) model.Task {
	t.Helper()
	node := model.Node{
		Name:      fmt.Sprintf("retention-node-%d", time.Now().UnixNano()),
		Host:      "127.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: fmt.Sprintf("/tmp/retention-backup-%d", time.Now().UnixNano()),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create retention node: %v", err)
	}
	taskTarget := fmt.Sprintf("%s/retention-target", t.TempDir())
	taskEntity := model.Task{
		Name:         fmt.Sprintf("retention-task-%d", time.Now().UnixNano()),
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		RsyncSource:  "/tmp/retention-source/",
		RsyncTarget:  taskTarget,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create retention task: %v", err)
	}
	return taskEntity
}

func openTaskRetentionPostgresDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL retention tables: %v", err)
	}
	return db
}
