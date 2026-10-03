package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	taskservice "xirang/backend/internal/task"

	"gorm.io/gorm"
)

func TestRecoveryPointProducerRetentionConsumersSQLite(t *testing.T) {
	runRecoveryPointProducerRetentionConsumers(t, "sqlite")
}

func TestRecoveryPointProducerRetentionConsumersPostgres(t *testing.T) {
	runRecoveryPointProducerRetentionConsumers(t, "postgres")
}

func runRecoveryPointProducerRetentionConsumers(t *testing.T, engine string) {
	t.Helper()
	db := openProducerRetentionIntegrationDB(t, engine)
	now := time.Now().UTC()
	fixture := newPublicationFixtureWithDB(t, db, true, publication.AdmissionManaged, now)
	fixture, lifecycle, point := seedResticLifecycleDeleteFixture(t, fixture)

	historyAt := now.Add(-72 * time.Hour)
	producerID := fixture.taskRun.ID
	if err := db.Model(&model.TaskRun{}).Where("id = ?", producerID).Updates(map[string]any{
		"status":      model.TaskRunStatusSuccess,
		"started_at":  historyAt.Add(-time.Minute),
		"finished_at": historyAt,
		"created_at":  historyAt,
		"updated_at":  historyAt,
	}).Error; err != nil {
		t.Fatalf("age Restic producer TaskRun: %v", err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Updates(map[string]any{
		"captured_at":     historyAt,
		"committed_at":    historyAt,
		"created_at":      historyAt,
		"updated_at":      historyAt,
		"retention_until": now.Add(24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("age Restic recovery point: %v", err)
	}
	if err := db.First(&point, "id = ?", point.ID).Error; err != nil {
		t.Fatalf("reload historical Restic recovery point: %v", err)
	}

	control := model.TaskRun{
		TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusFailed, StartedAt: timePointer(historyAt), FinishedAt: timePointer(historyAt.Add(time.Minute)),
		CreatedAt: historyAt.Add(2 * time.Minute), UpdatedAt: historyAt.Add(2 * time.Minute),
	}
	if err := db.Create(&control).Error; err != nil {
		t.Fatalf("seed unreferenced control TaskRun: %v", err)
	}

	authorizer := &producerRetentionPointAuthorizer{allowed: map[string]struct{}{point.ID: struct{}{}}}
	resolver, err := search.NewScopeResolver(db, authorizer, search.ScopeResolverLimits{MaxCandidates: 16})
	if err != nil {
		t.Fatalf("NewScopeResolver: %v", err)
	}
	before := resolveProducerRetentionConsumers(t, resolver, lifecycle, point, fixture.repository, strings.Repeat("d", 32))
	producerLineage := point.LineageJSON
	producerLocator := point.EncryptedProviderLocator
	producerSourceFingerprint := point.SourceFingerprint
	producerRepositoryID := point.RepositoryID

	manager := taskservice.NewManager(db, nil, nil, nil, nil, nil, 0, 1)
	var shutdownOnce sync.Once
	var shutdownErr error
	shutdown := func() error {
		shutdownOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shutdownErr = manager.Shutdown(ctx)
		})
		return shutdownErr
	}
	t.Cleanup(func() {
		if err := shutdown(); err != nil {
			t.Errorf("shutdown producer retention manager: %v", err)
		}
	})

	waitForTaskRunRemoval(t, db, control.ID)
	if err := shutdown(); err != nil {
		t.Fatalf("shutdown producer retention manager: %v", err)
	}

	var retainedRun model.TaskRun
	if err := db.First(&retainedRun, "id = ?", producerID).Error; err != nil {
		t.Fatalf("load retained producer TaskRun: %v", err)
	}
	if retainedRun.Status != model.TaskRunStatusSuccess || retainedRun.NodeIDSnapshot != fixture.task.NodeID {
		t.Fatalf("retained producer TaskRun=%+v, want terminal success with node snapshot %d", retainedRun, fixture.task.NodeID)
	}
	var retainedPoint model.RecoveryPoint
	if err := db.First(&retainedPoint, "id = ?", point.ID).Error; err != nil {
		t.Fatalf("load retained recovery point: %v", err)
	}
	var retainedRepository model.BackupRepository
	if err := db.First(&retainedRepository, "id = ?", fixture.repository.ID).Error; err != nil {
		t.Fatalf("load retained repository: %v", err)
	}
	if retainedPoint.ProducingTaskRunID == nil || *retainedPoint.ProducingTaskRunID != producerID {
		t.Fatalf("retained point producer TaskRun=%v, want %d", retainedPoint.ProducingTaskRunID, producerID)
	}
	if retainedPoint.RepositoryID != producerRepositoryID || retainedPoint.LineageJSON != producerLineage ||
		retainedPoint.EncryptedProviderLocator != producerLocator || retainedPoint.SourceFingerprint != producerSourceFingerprint {
		t.Fatalf("retained point changed after cleanup: before repository=%q lineage=%q locator=%q source=%q after=%+v",
			producerRepositoryID, producerLineage, producerLocator, producerSourceFingerprint, retainedPoint)
	}
	var controlCount int64
	if err := db.Model(&model.TaskRun{}).Where("id = ?", control.ID).Count(&controlCount).Error; err != nil {
		t.Fatalf("count cleaned control TaskRun: %v", err)
	}
	if controlCount != 0 {
		t.Fatalf("unreferenced control TaskRun count=%d, want 0", controlCount)
	}

	after := resolveProducerRetentionConsumers(t, resolver, lifecycle, retainedPoint, retainedRepository, strings.Repeat("e", 32))
	if after != before {
		t.Fatalf("consumer projection changed after cleanup: before=%+v after=%+v", before, after)
	}

	badPoint := retainedPoint
	badPoint.ID = strings.Repeat("f", 32)
	badNative := strings.Repeat("d", 64)
	badLocator, err := json.Marshal(resticPointLocatorV1{
		Version: 1, Provider: string(backupasset.ProviderRestic), FullSnapshotID: badNative,
	})
	if err != nil {
		t.Fatalf("encode NULL-producer Restic locator: %v", err)
	}
	badPoint.ProducingTaskRunID = nil
	badPoint.EncryptedProviderLocator = string(badLocator)
	badPoint.SourceFingerprint = resticSourceFingerprint(*fixture.repository.RepositoryIdentity, badNative)
	badPoint.CapturedAt = timePointer(historyAt.Add(time.Minute))
	badPoint.CommittedAt = timePointer(historyAt.Add(time.Minute))
	badPoint.CreatedAt = historyAt.Add(3 * time.Minute)
	badPoint.UpdatedAt = historyAt.Add(3 * time.Minute)
	badPoint.RetentionUntil = nil
	if err := db.Create(&badPoint).Error; err != nil {
		t.Fatalf("seed NULL-producer recovery point: %v", err)
	}
	if err := db.First(&badPoint, "id = ?", badPoint.ID).Error; err != nil {
		t.Fatalf("reload NULL-producer recovery point: %v", err)
	}
	authorizer.allowed[badPoint.ID] = struct{}{}
	selection, err := resolver.Resolve(context.Background(), catalog.AuthorizationScope{Role: "admin", UserID: 1}, search.SearchScope{
		Mode: search.SearchScopeExactPoints, RecoveryPointIDs: []string{badPoint.ID},
	})
	if !errors.Is(err, search.ErrScopeStale) {
		t.Fatalf("NULL-producer exact search selection=%+v error=%v, want ErrScopeStale", selection, err)
	}
	if _, err := lifecycle.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("a", 32), badPoint, retainedRepository); err == nil {
		t.Fatal("NULL-producer lifecycle resolution unexpectedly succeeded")
	} else {
		var capabilityErr *provider.CapabilityError
		if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityDeletionUnavailable {
			t.Fatalf("NULL-producer lifecycle error=%v, want deletion_unavailable", err)
		}
	}
	var persistedBad model.RecoveryPoint
	if err := db.First(&persistedBad, "id = ?", badPoint.ID).Error; err != nil {
		t.Fatalf("reload NULL-producer point after consumer checks: %v", err)
	}
	if persistedBad.ProducingTaskRunID != nil {
		t.Fatalf("NULL-producer point was repaired to TaskRun %v", persistedBad.ProducingTaskRunID)
	}
}

type producerRetentionConsumerProjection struct {
	TaskID             uint
	NodeID             uint
	RepositoryID       string
	NativePointID      string
	NativeRepositoryID string
}

func resolveProducerRetentionConsumers(
	t *testing.T,
	resolver *search.ScopeResolver,
	lifecycle *Service,
	point model.RecoveryPoint,
	repository model.BackupRepository,
	operationID string,
) producerRetentionConsumerProjection {
	t.Helper()
	selection, err := resolver.Resolve(context.Background(), catalog.AuthorizationScope{Role: "admin", UserID: 1}, search.SearchScope{
		Mode: search.SearchScopeExactPoints, RecoveryPointIDs: []string{point.ID},
	})
	if err != nil {
		t.Fatalf("resolve exact retained point %s: %v", point.ID, err)
	}
	if len(selection.Points) != 1 || selection.Points[0].ID != point.ID {
		t.Fatalf("exact retained point selection=%+v, want point %s", selection, point.ID)
	}
	request, err := lifecycle.ResolveLifecycleDeletePoint(context.Background(), operationID, point, repository)
	if err != nil {
		t.Fatalf("resolve lifecycle deletion for retained point %s: %v", point.ID, err)
	}
	runtimeAccess, ok := request.Snapshot.Access.AdapterData.(provider.ResticRuntimeAccess)
	if !ok || runtimeAccess.Command == nil {
		t.Fatalf("Restic lifecycle runtime access=%T %+v", request.Snapshot.Access.AdapterData, request.Snapshot.Access.AdapterData)
	}
	if request.Point.Native == "" {
		t.Fatalf("Restic lifecycle native point ID is empty: %+v", request.Point)
	}
	return producerRetentionConsumerProjection{
		TaskID:             request.Snapshot.Access.TaskID,
		NodeID:             request.Snapshot.Access.NodeID,
		RepositoryID:       request.Snapshot.Access.RepositoryID,
		NativePointID:      request.Point.Native,
		NativeRepositoryID: runtimeAccess.NativeRepositoryID,
	}
}

func waitForTaskRunRemoval(t *testing.T, db *gorm.DB, runID uint) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	check := func() bool {
		var count int64
		if err := db.Model(&model.TaskRun{}).Where("id = ?", runID).Count(&count).Error; err != nil {
			t.Fatalf("poll control TaskRun %d: %v", runID, err)
		}
		return count == 0
	}
	if check() {
		return
	}
	for {
		select {
		case <-ticker.C:
			if check() {
				return
			}
		case <-deadline.C:
			t.Fatalf("control TaskRun %d was not removed within 10s", runID)
		}
	}
}

type producerRetentionPointAuthorizer struct {
	allowed map[string]struct{}
}

func (authorizer *producerRetentionPointAuthorizer) AuthorizedPointIDs(_ context.Context, _ catalog.AuthorizationScope, candidateIDs []string) ([]string, error) {
	visible := make([]string, 0, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		if _, ok := authorizer.allowed[candidateID]; ok {
			visible = append(visible, candidateID)
		}
	}
	return visible, nil
}

func openProducerRetentionIntegrationDB(t *testing.T, engine string) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	switch engine {
	case "sqlite":
		db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "retention.db")})
		if err != nil {
			t.Fatalf("open producer retention SQLite database: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("get producer retention SQLite connection: %v", err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		if err := database.RunMigrations(db, "sqlite"); err != nil {
			t.Fatalf("run producer retention SQLite migrations: %v", err)
		}
		return db
	case "postgres":
		dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
		if dsn == "" {
			if os.Getenv("REQUIRE_POSTGRES_REPOSITORY_TEST") == "1" || os.Getenv("REQUIRE_POSTGRES_PRODUCER_RETENTION_TEST") == "1" {
				t.Fatal("TEST_POSTGRES_DSN is required for producer retention PostgreSQL tests")
			}
			t.Skip("TEST_POSTGRES_DSN is required for producer retention PostgreSQL tests")
		}
		parsed, err := url.Parse(dsn)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
		}
		base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
		if err != nil {
			t.Fatalf("open producer retention PostgreSQL base connection: %v", err)
		}
		baseSQL, err := base.DB()
		if err != nil {
			t.Fatalf("get producer retention PostgreSQL base connection: %v", err)
		}
		schema := fmt.Sprintf("xirang_producer_retention_%d", time.Now().UTC().UnixNano())
		schemaCreated := false
		t.Cleanup(func() {
			if schemaCreated {
				_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
			}
			_ = baseSQL.Close()
		})
		if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatalf("create producer retention PostgreSQL schema: %v", err)
		}
		schemaCreated = true
		scoped := *parsed
		query := scoped.Query()
		query.Set("search_path", schema)
		query.Set("timezone", "UTC")
		scoped.RawQuery = query.Encode()
		db, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: scoped.String()})
		if err != nil {
			t.Fatalf("open producer retention isolated PostgreSQL connection: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("get producer retention isolated PostgreSQL connection: %v", err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		if err := database.RunMigrations(db, "postgres"); err != nil {
			t.Fatalf("run producer retention PostgreSQL migrations: %v", err)
		}
		return db
	default:
		t.Fatalf("unsupported producer retention integration database engine %q", engine)
		return nil
	}
}

func TestRecoveryPointProducerRetentionPublicationSQLite(t *testing.T) {
	runRecoveryPointProducerRetentionPublication(t, "sqlite")
}

func TestRecoveryPointProducerRetentionPublicationPostgres(t *testing.T) {
	runRecoveryPointProducerRetentionPublication(t, "postgres")
}

func runRecoveryPointProducerRetentionPublication(t *testing.T, engine string) {
	t.Helper()
	t.Run("publication first", func(t *testing.T) {
		runProducerRetentionPublicationFirst(t, engine)
	})
	t.Run("cleaner first", func(t *testing.T) {
		runProducerRetentionCleanerFirst(t, engine)
	})
}

type producerRetentionPrepareResult struct {
	execution publication.Execution
	err       error
}

func runProducerRetentionPublicationFirst(t *testing.T, engine string) {
	t.Helper()
	db := openProducerRetentionIntegrationDB(t, engine)
	now := time.Now().UTC()
	fixture := newPublicationFixtureWithDB(t, db, true, publication.AdmissionManaged, now)
	fixture.connectExactResticBinding(t)

	historyAt := now.Add(-72 * time.Hour)
	ageProducerRetentionTaskRun(t, db, &fixture.taskRun, historyAt, model.TaskRunStatusRunning)
	control := model.TaskRun{
		TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusRunning, StartedAt: timePointer(historyAt.Add(time.Minute)),
		CreatedAt: historyAt.Add(2 * time.Minute), UpdatedAt: historyAt.Add(2 * time.Minute),
	}
	if err := db.Create(&control).Error; err != nil {
		t.Fatalf("seed publication-first control TaskRun: %v", err)
	}
	postTerminalControl := model.TaskRun{
		TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusRunning, StartedAt: timePointer(historyAt.Add(3 * time.Minute)),
		CreatedAt: historyAt.Add(4 * time.Minute), UpdatedAt: historyAt.Add(4 * time.Minute),
	}
	if err := db.Create(&postTerminalControl).Error; err != nil {
		t.Fatalf("seed publication-first post-terminal control TaskRun: %v", err)
	}
	runInput := fixture.run()
	targetPointID := fixture.expectedPointID(t)

	release := make(chan struct{})
	var releaseOnce sync.Once
	entered := make(chan struct{})
	var pauseOnce sync.Once
	callbackName := "test:producer_retention_publication_pause_" + strings.ReplaceAll(t.Name(), "/", "_")
	callbackRegistered := false
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx == nil || tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != (model.RecoveryPoint{}).TableName() {
			return
		}
		point, ok := tx.Statement.Dest.(*model.RecoveryPoint)
		if !ok || point.ID != targetPointID || point.ProducingTaskRunID == nil ||
			*point.ProducingTaskRunID != fixture.taskRun.ID ||
			point.State != string(backupasset.RecoveryPointPreparing) {
			return
		}
		pauseOnce.Do(func() {
			// This callback runs after preparePoint has entered its transaction
			// and locked the producer TaskRun, but before the point INSERT.
			close(entered)
			callbackCtx := tx.Statement.Context
			if callbackCtx == nil {
				callbackCtx = context.Background()
			}
			select {
			case <-release:
			case <-callbackCtx.Done():
				_ = tx.AddError(callbackCtx.Err())
			}
		})
	}); err != nil {
		t.Fatalf("register publication point create gate: %v", err)
	}
	callbackRegistered = true

	var (
		prepareCancel   context.CancelFunc
		prepareDone     chan producerRetentionPrepareResult
		prepareResult   producerRetentionPrepareResult
		prepareFinished bool
		abandonPrepare  func() error

		overlapManager      *taskservice.Manager
		overlapShutdownOnce sync.Once
		overlapShutdownErr  error
		freshManager        *taskservice.Manager
		freshShutdownOnce   sync.Once
		freshShutdownErr    error
	)
	releasePrepare := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	awaitPrepare := func(timeout time.Duration) bool {
		if prepareFinished || prepareDone == nil {
			return prepareFinished
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case prepareResult = <-prepareDone:
			prepareFinished = true
			if prepareResult.execution != nil && abandonPrepare == nil {
				abandonPrepare = registerProducerRetentionExecutionCleanup(t, prepareResult.execution, "publication-first")
			}
			return true
		case <-timer.C:
			return false
		}
	}
	shutdownOverlapManager := func() error {
		overlapShutdownOnce.Do(func() {
			if overlapManager == nil {
				return
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			overlapShutdownErr = overlapManager.Shutdown(shutdownCtx)
		})
		return overlapShutdownErr
	}
	shutdownFreshManager := func() error {
		freshShutdownOnce.Do(func() {
			if freshManager == nil {
				return
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			freshShutdownErr = freshManager.Shutdown(shutdownCtx)
		})
		return freshShutdownErr
	}
	defer func() {
		if !prepareFinished {
			// Release the callback before cancellation so the gated transaction
			// cannot remain blocked while this bounded join is in progress.
			releasePrepare()
			if prepareCancel != nil {
				prepareCancel()
			}
			if !awaitPrepare(5 * time.Second) {
				failStopProducerRetentionTest("publication-first Prepare worker did not join after bounded cancellation and gate release; refusing shared-resource teardown")
				return
			}
		}
		if abandonPrepare != nil {
			if err := abandonPrepare(); err != nil {
				failStopProducerRetentionTest(fmt.Sprintf("abandon publication-first execution during cleanup failed: %v", err))
				return
			}
		}
		if prepareCancel != nil {
			prepareCancel()
		}
		freshErr := shutdownFreshManager()
		overlapErr := shutdownOverlapManager()
		if freshErr != nil || overlapErr != nil {
			failStopProducerRetentionTest(fmt.Sprintf("publication-first manager join failed (fresh=%v overlap=%v); refusing callback and database teardown", freshErr, overlapErr))
			return
		}
		if callbackRegistered {
			if err := db.Callback().Create().Remove(callbackName); err != nil {
				t.Errorf("remove publication point create gate: %v", err)
			}
		}
	}()

	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), 10*time.Second)
	prepareCancel = cancelPrepare
	prepareDone = make(chan producerRetentionPrepareResult, 1)
	go func() {
		execution, err := fixture.service.Prepare(prepareCtx, runInput)
		prepareDone <- producerRetentionPrepareResult{execution: execution, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("publication point create gate was not entered")
	}

	// Start the real maintenance worker while Prepare still owns its gated
	// transaction. The worker is deliberately joined before terminalization so
	// this overlap cannot consume the only successful-cleanup throttle window.
	overlapManager = taskservice.NewManager(db, nil, nil, nil, nil, nil, 0, 1)
	releasePrepare()
	if !awaitPrepare(10 * time.Second) {
		t.Fatal("publication-first Prepare did not complete within 10s")
	}
	if prepareResult.err != nil {
		if abandonPrepare != nil {
			if err := abandonPrepare(); err != nil {
				t.Errorf("abandon publication-first unexpected execution: %v", err)
			}
		}
		t.Fatalf("publication-first Prepare: %v", prepareResult.err)
	}
	if prepareResult.execution == nil {
		t.Fatal("publication-first Prepare returned nil execution")
	}

	if err := shutdownOverlapManager(); err != nil {
		t.Fatalf("shutdown publication-first overlap manager: %v", err)
	}
	terminalizeProducerRetentionTaskRun(t, db, &fixture.taskRun, model.TaskRunStatusSuccess, time.Now().UTC())
	ageProducerRetentionTaskRun(t, db, &control, historyAt.Add(2*time.Minute), model.TaskRunStatusFailed)
	ageProducerRetentionTaskRun(t, db, &postTerminalControl, historyAt.Add(4*time.Minute), model.TaskRunStatusFailed)
	requireProducerRetentionTaskRunPresent(t, db, control.ID, "publication-first control")
	requireProducerRetentionTaskRunPresent(t, db, postTerminalControl.ID, "publication-first post-terminal control")

	// A fresh Manager has a clean successful-cleanup throttle state and sees
	// both controls only after they have become terminal and expired.
	freshManager = taskservice.NewManager(db, nil, nil, nil, nil, nil, 0, 1)
	waitForTaskRunRemoval(t, db, control.ID)
	waitForTaskRunRemoval(t, db, postTerminalControl.ID)
	if err := shutdownFreshManager(); err != nil {
		t.Fatalf("shutdown publication-first fresh manager: %v", err)
	}

	var retainedProducer model.TaskRun
	if err := db.First(&retainedProducer, "id = ?", fixture.taskRun.ID).Error; err != nil {
		t.Fatalf("load publication-first producer TaskRun: %v", err)
	}
	if retainedProducer.Status != model.TaskRunStatusSuccess {
		t.Fatalf("publication-first producer status=%q, want %q", retainedProducer.Status, model.TaskRunStatusSuccess)
	}
	var retainedPoint model.RecoveryPoint
	if err := db.First(&retainedPoint, "id = ?", targetPointID).Error; err != nil {
		t.Fatalf("load publication-first preparing point: %v", err)
	}
	if retainedPoint.State != string(backupasset.RecoveryPointPreparing) {
		t.Fatalf("publication-first point state=%q, want %q", retainedPoint.State, backupasset.RecoveryPointPreparing)
	}
	if retainedPoint.ProducingTaskRunID == nil || *retainedPoint.ProducingTaskRunID != fixture.taskRun.ID {
		t.Fatalf("publication-first point producer=%v, want %d", retainedPoint.ProducingTaskRunID, fixture.taskRun.ID)
	}
	var activeLeaseCount int64
	if err := db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND status = ?", targetPointID, backupasset.LeaseActive).
		Count(&activeLeaseCount).Error; err != nil {
		t.Fatalf("count publication-first active lease: %v", err)
	}
	if activeLeaseCount != 1 {
		t.Fatalf("publication-first active leases=%d, want 1", activeLeaseCount)
	}
}

func runProducerRetentionCleanerFirst(t *testing.T, engine string) {
	t.Helper()
	db := openProducerRetentionIntegrationDB(t, engine)
	now := time.Now().UTC()
	fixture := newPublicationFixtureWithDB(t, db, true, publication.AdmissionManaged, now)
	fixture.connectExactResticBinding(t)

	historyAt := now.Add(-72 * time.Hour)
	ageProducerRetentionTaskRun(t, db, &fixture.taskRun, historyAt, model.TaskRunStatusSuccess)
	originalRun := fixture.run()
	activeRun := model.TaskRun{
		TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusRunning, StartedAt: timePointer(now.Add(-time.Minute)),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&activeRun).Error; err != nil {
		t.Fatalf("seed cleaner-first active TaskRun: %v", err)
	}

	var manager *taskservice.Manager
	var shutdownOnce sync.Once
	var shutdownErr error
	shutdownManager := func() error {
		shutdownOnce.Do(func() {
			if manager == nil {
				return
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shutdownErr = manager.Shutdown(shutdownCtx)
		})
		return shutdownErr
	}
	defer func() {
		if err := shutdownManager(); err != nil {
			t.Errorf("shutdown cleaner-first manager: %v", err)
		}
	}()

	manager = taskservice.NewManager(db, nil, nil, nil, nil, nil, 0, 1)
	waitForTaskRunRemoval(t, db, fixture.taskRun.ID)
	if err := shutdownManager(); err != nil {
		t.Fatalf("shutdown cleaner-first manager: %v", err)
	}

	var retainedActive model.TaskRun
	if err := db.First(&retainedActive, "id = ?", activeRun.ID).Error; err != nil {
		t.Fatalf("load cleaner-first active TaskRun: %v", err)
	}
	if retainedActive.Status != model.TaskRunStatusRunning {
		t.Fatalf("cleaner-first active TaskRun status=%q, want %q", retainedActive.Status, model.TaskRunStatusRunning)
	}

	rejectedCtx, cancelRejected := context.WithTimeout(context.Background(), 5*time.Second)
	rejectedExecution, rejectedErr := fixture.service.Prepare(rejectedCtx, originalRun)
	cancelRejected()
	abandonRejected := registerProducerRetentionExecutionCleanup(t, rejectedExecution, "cleaner-first rejected")
	if abandonRejected != nil {
		if err := abandonRejected(); err != nil {
			t.Fatalf("abandon cleaner-first rejected execution: %v", err)
		}
	}
	if rejectedErr == nil {
		t.Fatal("cleaner-first Prepare unexpectedly succeeded for removed TaskRun")
	}
	if !errors.Is(rejectedErr, backupasset.ErrNotFound) && !errors.Is(rejectedErr, backupasset.ErrConflict) {
		t.Fatalf("cleaner-first removed TaskRun Prepare error=%v, want not found or conflict", rejectedErr)
	}
	requireProducerRetentionPointAbsent(t, db, fixture.expectedPointID(t))

	activeCtx, cancelActive := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelActive()
	activeExecution, activeErr := fixture.service.Prepare(activeCtx, publicationRunForProducerRetention(fixture, activeRun, "publication-cleaner-first-active"))
	abandonActive := registerProducerRetentionExecutionCleanup(t, activeExecution, "cleaner-first active")
	if activeErr != nil {
		if abandonActive != nil {
			if err := abandonActive(); err != nil {
				t.Errorf("abandon cleaner-first active execution after Prepare error: %v", err)
			}
		}
		t.Fatalf("cleaner-first active TaskRun Prepare: %v", activeErr)
	}
	if activeExecution == nil {
		t.Fatal("cleaner-first active Prepare returned nil execution")
	}
	activeAttempt := activeExecution.Attempt()
	if activeAttempt == nil || activeAttempt.Restic == nil || activeAttempt.Restic.TaskRunID != activeRun.ID {
		t.Fatalf("cleaner-first active Prepare attempt=%+v, want TaskRun %d", activeAttempt, activeRun.ID)
	}
	if abandonActive == nil {
		t.Fatal("cleaner-first active Prepare did not register execution cleanup")
	}
	if err := abandonActive(); err != nil {
		t.Fatalf("abandon cleaner-first active execution: %v", err)
	}
}

func failStopProducerRetentionTest(message string) {
	fmt.Fprintf(os.Stderr, "producer retention integration test fail-stop: %s\n", message)
	go func() {
		panic(message)
	}()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	<-timer.C
	os.Exit(1)
}

func registerProducerRetentionExecutionCleanup(t *testing.T, execution publication.Execution, label string) func() error {
	t.Helper()
	if execution == nil {
		return nil
	}
	var abandonOnce sync.Once
	var abandonErr error
	abandon := func() error {
		abandonOnce.Do(func() {
			abandonErr = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		})
		return abandonErr
	}
	t.Cleanup(func() {
		if err := abandon(); err != nil {
			failStopProducerRetentionTest(fmt.Sprintf("abandon %s execution during cleanup failed: %v", label, err))
		}
	})
	return abandon
}

func requireProducerRetentionTaskRunPresent(t *testing.T, db *gorm.DB, runID uint, label string) {
	t.Helper()
	var run model.TaskRun
	if err := db.First(&run, "id = ?", runID).Error; err != nil {
		t.Fatalf("%s TaskRun %d was not present after terminalization: %v", label, runID, err)
	}
	if !model.IsTerminalTaskRunStatus(run.Status) {
		t.Fatalf("%s TaskRun %d status=%q, want terminal before fresh cleaner", label, runID, run.Status)
	}
}

func terminalizeProducerRetentionTaskRun(t *testing.T, db *gorm.DB, run *model.TaskRun, status string, finishedAt time.Time) {
	t.Helper()
	if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": status, "finished_at": finishedAt, "updated_at": finishedAt,
	}).Error; err != nil {
		t.Fatalf("terminalize TaskRun %d as %q: %v", run.ID, status, err)
	}
	run.Status = status
	run.FinishedAt = timePointer(finishedAt)
	run.UpdatedAt = finishedAt
}

func ageProducerRetentionTaskRun(t *testing.T, db *gorm.DB, run *model.TaskRun, at time.Time, status string) {
	t.Helper()
	startedAt := at.Add(-time.Minute)
	finishedAt := at
	updates := map[string]any{
		"status":      status,
		"started_at":  startedAt,
		"created_at":  at,
		"updated_at":  at,
		"finished_at": nil,
	}
	run.FinishedAt = nil
	if model.IsTerminalTaskRunStatus(status) {
		updates["finished_at"] = finishedAt
		run.FinishedAt = timePointer(finishedAt)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(updates).Error; err != nil {
		t.Fatalf("age TaskRun %d as %q: %v", run.ID, status, err)
	}
	run.Status = status
	run.StartedAt = timePointer(startedAt)
	run.CreatedAt = at
	run.UpdatedAt = at
}

func publicationRunForProducerRetention(fixture *publicationFixture, run model.TaskRun, correlationID string) publication.Run {
	startedAt := fixture.now
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	return publication.Run{
		Task: fixture.task, TaskRunID: run.ID, Trigger: run.TriggerType, StartedAt: startedAt,
		Audit: backupasset.PublicationAuditContext{
			Actor:         backupasset.AuditActor{UserID: 9, Username: "operator", Role: "operator"},
			CorrelationID: correlationID,
		},
	}
}

func requireProducerRetentionPointAbsent(t *testing.T, db *gorm.DB, pointID string) {
	t.Helper()
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", pointID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("removed TaskRun point=%+v err=%v, want no RecoveryPoint", point, err)
	}
	var leaseCount int64
	if err := db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ?", pointID).Count(&leaseCount).Error; err != nil {
		t.Fatalf("count removed TaskRun leases: %v", err)
	}
	if leaseCount != 0 {
		t.Fatalf("removed TaskRun lease count=%d, want 0", leaseCount)
	}
}
