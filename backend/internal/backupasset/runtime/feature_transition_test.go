package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"
)

func TestFeatureTransitionContextUsesCentralCeilingAndEarlierCallerDeadline(t *testing.T) {
	const serverWriteTimeout = 30 * time.Second
	if featureTransitionCeiling >= serverWriteTimeout {
		t.Fatalf("feature transition ceiling=%s, must stay below server write timeout=%s", featureTransitionCeiling, serverWriteTimeout)
	}

	opCtx, cancel := newFeatureTransitionContext(context.Background())
	defer cancel()
	deadline, ok := opCtx.Deadline()
	if !ok {
		t.Fatal("feature transition context has no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > featureTransitionCeiling {
		t.Fatalf("feature transition deadline remaining=%s, want within (0,%s]", remaining, featureTransitionCeiling)
	}

	callerDeadline := time.Now().Add(time.Second)
	callerCtx, cancelCaller := context.WithDeadline(context.Background(), callerDeadline)
	defer cancelCaller()
	callerBound, cancelCallerBound := newFeatureTransitionContext(callerCtx)
	defer cancelCallerBound()
	gotCallerDeadline, ok := callerBound.Deadline()
	if !ok || !gotCallerDeadline.Equal(callerDeadline) {
		t.Fatalf("composed deadline=%s ok=%t, want earlier caller deadline=%s", gotCallerDeadline, ok, callerDeadline)
	}
}

func TestFeatureTransitionCleanupContextIsLiveAfterCancellationAndKeepsTotalBelowWriteTimeout(t *testing.T) {
	const serverWriteTimeout = 30 * time.Second
	if featureTransitionCeiling+featureTransitionCleanupReserve >= serverWriteTimeout {
		t.Fatalf("operation + cleanup ceiling=%s, must stay below server write timeout=%s", featureTransitionCeiling+featureTransitionCleanupReserve, serverWriteTimeout)
	}
	opCtx, cancelOperation := context.WithCancel(context.Background())
	cancelOperation()
	cleanupCtx, cancelCleanup := newFeatureTransitionCleanupContext(opCtx)
	defer cancelCleanup()
	if err := cleanupCtx.Err(); err != nil {
		t.Fatalf("cleanup context inherited operation cancellation: %v", err)
	}
	deadline, ok := cleanupCtx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > featureTransitionCleanupReserve {
		t.Fatalf("cleanup deadline=%s ok=%t, want live bounded reserve <=%s", deadline, ok, featureTransitionCleanupReserve)
	}
}

func TestFeatureTransitionSearchFailureRestoresExactPriorSettingAndEmptyStamp(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_PHASE3_SEARCH_ROLLBACK_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	now := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{},
		&model.BackupAssetInstallation{},
		&model.BackupAssetInventoryRun{},
		&model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetInstallation{
		ID: strings.Repeat("d", 32), Slot: 1, Class: string(ga.InstallationFresh),
		Readiness: string(ga.ReadinessReady), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed installation: %v", err)
	}

	settingsService := settings.NewService(db)
	backend := newSearchWorkerBackendFake()
	searchErr := errors.New("FAKE_PHASE3_SEARCH_FAILURE_FOR_TEST_ONLY")
	backend.overlayErr = searchErr
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	admission := newAdmissionControllerFixture(t, false, nil)
	admission.initialize(t)
	runtime := EnablementRuntime(readyGAEnablement(), admission.controller)
	runtime.admission = admission.controller
	runtime.foundation = backupasset.NewFoundationService(settingsService)
	runtime.settings = settingsService
	runtime.keyring = backupasset.NewKeyring(db, func() time.Time { return now })
	runtime.searchWorker = worker
	runtime.contentManager = &runtimeContentManagerFake{events: &events}
	runtime.inventory = ga.NewInventoryService(ga.InventoryDependencies{DB: db, Now: func() time.Time { return now }})

	err = runtime.TransitionFeature(context.Background(), true, func() error {
		return settingsService.Update("backup_assets.enabled", "true")
	})
	if !errors.Is(err, searchErr) {
		t.Fatalf("transition error=%v, want Search failure", err)
	}
	if got := settingsService.GetEffective("backup_assets.enabled"); got != "false" {
		t.Fatalf("restored setting=%q, want exact prior false", got)
	}
	var installation model.BackupAssetInstallation
	if err := db.Where("slot = ?", 1).Take(&installation).Error; err != nil {
		t.Fatal(err)
	}
	if installation.EnablementSucceededAt != nil {
		t.Fatalf("failed full enable retained false success stamp=%s", installation.EnablementSucceededAt)
	}
	if got := len(worker.wake); got != 0 {
		t.Fatalf("Search preparation failure queued wakes=%d, want zero", got)
	}
}

type featureTransitionContextContentProbe struct {
	contexts []context.Context
}

func (*featureTransitionContextContentProbe) Startup(context.Context) error { return nil }
func (probe *featureTransitionContextContentProbe) PrepareEnable(ctx context.Context, _ backupasset.ContentConfig) error {
	probe.contexts = append(probe.contexts, ctx)
	return nil
}
func (probe *featureTransitionContextContentProbe) RestoreEnable(ctx context.Context, _ backupasset.ContentConfig) error {
	probe.contexts = append(probe.contexts, ctx)
	return nil
}
func (probe *featureTransitionContextContentProbe) PrepareDisable(ctx context.Context) error {
	probe.contexts = append(probe.contexts, ctx)
	return nil
}
func (*featureTransitionContextContentProbe) SetReady(bool)                  {}
func (*featureTransitionContextContentProbe) StopAccepting()                 {}
func (*featureTransitionContextContentProbe) Run(context.Context)            {}
func (*featureTransitionContextContentProbe) Shutdown(context.Context) error { return nil }
func (*featureTransitionContextContentProbe) PrepareSchemaDown(_ context.Context, callback func() error) error {
	return callback()
}

type featureTransitionContextAdmissionProbe struct {
	ctx context.Context
}

func (probe *featureTransitionContextAdmissionProbe) TransitionFeature(
	ctx context.Context,
	_ bool,
	persist func() error,
) error {
	probe.ctx = ctx
	return persist()
}

func (*featureTransitionContextAdmissionProbe) PrepareApplicationDowngrade(_ context.Context, callback func() error) error {
	return callback()
}
func (*featureTransitionContextAdmissionProbe) PrepareSchemaDown(_ context.Context, callback func() error) error {
	return callback()
}

func TestFeatureTransitionPropagatesOneBoundedContextThroughContentAdmissionAndPersist(t *testing.T) {
	content := &featureTransitionContextContentProbe{}
	admission := &featureTransitionContextAdmissionProbe{}
	runtime := &Runtime{contentManager: content, transitioner: admission, enablement: readyGAEnablement()}
	configs := testFoundationTransitionConfigs(t, false, true)
	persistCalled := false
	if err := runtime.transitionFeatureWithConfigs(context.Background(), configs, func() error {
		persistCalled = true
		if admission.ctx == nil || admission.ctx.Err() != nil {
			t.Fatalf("persist did not execute under the live operation context: %v", admission.ctx)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !persistCalled || len(content.contexts) != 1 {
		t.Fatalf("persist=%t Content context calls=%d", persistCalled, len(content.contexts))
	}
	wantDeadline, ok := admission.ctx.Deadline()
	if !ok {
		t.Fatal("Admission received an unbounded transition context")
	}
	gotDeadline, ok := content.contexts[0].Deadline()
	if !ok || !gotDeadline.Equal(wantDeadline) {
		t.Fatalf("Content deadline=%s ok=%t, want shared Admission deadline=%s", gotDeadline, ok, wantDeadline)
	}
}

type featureTransitionContextExportProbe struct {
	ctx context.Context
}

type featureTransitionContextAwareExportProbe struct {
	featureTransitionContextExportProbe
	legacyCalled bool
}

type featureTransitionRestoreExportProbe struct {
	featureTransitionContextExportProbe
	failure error
}

func (probe *featureTransitionContextAwareExportProbe) TransitionSettings(
	context.Context,
	bool,
	backupasset.ExportConfig,
	func() error,
) error {
	probe.legacyCalled = true
	return errors.New("FAKE_LEGACY_EXPORT_PERSISTENCE_SEAM_FOR_TEST_ONLY")
}

func (probe *featureTransitionContextAwareExportProbe) TransitionSettingsContextWithRestore(
	ctx context.Context,
	_ bool,
	_ backupasset.ExportConfig,
	persist func(context.Context) error,
	_ func(context.Context) error,
) error {
	probe.ctx = ctx
	return persist(ctx)
}

func (probe *featureTransitionRestoreExportProbe) TransitionSettingsContextWithRestore(
	ctx context.Context,
	_ bool,
	_ backupasset.ExportConfig,
	persist func(context.Context) error,
	restore func(context.Context) error,
) error {
	if err := persist(ctx); err != nil {
		return err
	}
	if probe.failure == nil {
		return nil
	}
	cleanupCtx, cancelCleanup := newFeatureTransitionCleanupContext(ctx)
	defer cancelCleanup()
	return errors.Join(probe.failure, restore(cleanupCtx))
}

func (*featureTransitionContextExportProbe) Startup(context.Context) error { return nil }
func (*featureTransitionContextExportProbe) Ready() bool                   { return false }
func (probe *featureTransitionContextExportProbe) TransitionSettings(
	ctx context.Context,
	_ bool,
	_ backupasset.ExportConfig,
	persist func() error,
) error {
	probe.ctx = ctx
	return persist()
}
func (*featureTransitionContextExportProbe) Service() *managedExportServiceFacade   { return nil }
func (*featureTransitionContextExportProbe) Delivery() *managedExportDeliveryFacade { return nil }
func (*featureTransitionContextExportProbe) StopAccepting()                         {}
func (*featureTransitionContextExportProbe) Run(context.Context)                    {}
func (*featureTransitionContextExportProbe) Shutdown(context.Context) error         { return nil }
func (*featureTransitionContextExportProbe) PrepareSchemaDown(_ context.Context, callback func() error) error {
	return callback()
}

type featureTransitionContextRecoveryProbe struct {
	ctx context.Context
}

type featureTransitionContextAwareRecoveryProbe struct {
	featureTransitionContextRecoveryProbe
	legacyCalled bool
}

func (probe *featureTransitionContextAwareRecoveryProbe) TransitionSettingsWithRestore(
	context.Context,
	backupasset.RecoveryConfig,
	func() error,
	func() error,
) error {
	probe.legacyCalled = true
	return errors.New("FAKE_LEGACY_RECOVERY_PERSISTENCE_SEAM_FOR_TEST_ONLY")
}

func (probe *featureTransitionContextAwareRecoveryProbe) TransitionSettingsContextWithRestore(
	ctx context.Context,
	_ backupasset.RecoveryConfig,
	persist func(context.Context) error,
	_ func(context.Context) error,
) error {
	probe.ctx = ctx
	return persist(ctx)
}

type featureTransitionBlockingStage struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	joined   atomic.Bool
	once     sync.Once
}

func newFeatureTransitionBlockingStage() *featureTransitionBlockingStage {
	return &featureTransitionBlockingStage{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
}

func (stage *featureTransitionBlockingStage) await(ctx context.Context) error {
	stage.once.Do(func() { close(stage.started) })
	<-ctx.Done()
	close(stage.canceled)
	<-stage.release
	stage.joined.Store(true)
	return ctx.Err()
}

type featureTransitionBlockingContent struct {
	featureTransitionContextContentProbe
	stage *featureTransitionBlockingStage
}

func (probe *featureTransitionBlockingContent) PrepareEnable(ctx context.Context, _ backupasset.ContentConfig) error {
	return probe.stage.await(ctx)
}

type featureTransitionBlockingAdmission struct {
	featureTransitionContextAdmissionProbe
	stage *featureTransitionBlockingStage
	calls atomic.Int32
}

func (probe *featureTransitionBlockingAdmission) TransitionFeature(
	ctx context.Context, _ bool, persist func() error,
) error {
	probe.ctx = ctx
	if probe.calls.Add(1) > 1 {
		return persist()
	}
	return probe.stage.await(ctx)
}

type featureTransitionBlockingExport struct {
	featureTransitionContextExportProbe
	stage *featureTransitionBlockingStage
}

func (probe *featureTransitionBlockingExport) TransitionSettings(
	ctx context.Context, _ bool, _ backupasset.ExportConfig, _ func() error,
) error {
	probe.ctx = ctx
	return probe.stage.await(ctx)
}

type featureTransitionBlockingRecovery struct {
	featureTransitionContextRecoveryProbe
	stage *featureTransitionBlockingStage
}

func (probe *featureTransitionBlockingRecovery) TransitionSettingsWithRestore(
	ctx context.Context, _ backupasset.RecoveryConfig, _ func() error, _ func() error,
) error {
	probe.ctx = ctx
	return probe.stage.await(ctx)
}

func assertFeatureTransitionBlockingStageJoinsBeforeReturn(
	t *testing.T,
	stage *featureTransitionBlockingStage,
	invoke func(context.Context) error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- invoke(ctx) }()
	select {
	case <-stage.started:
	case <-time.After(time.Second):
		t.Fatal("blocking transition stage did not start")
	}
	select {
	case <-stage.canceled:
	case <-time.After(time.Second):
		t.Fatal("blocking transition stage did not observe its caller deadline")
	}
	select {
	case err := <-done:
		t.Fatalf("transition returned before blocking stage joined: %v", err)
	default:
	}
	close(stage.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("transition error=%v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transition did not return after blocking stage joined")
	}
	if !stage.joined.Load() {
		t.Fatal("transition returned before blocking stage recorded its join")
	}
}

func TestFeatureTransitionBlockingStagesTimeoutDrainAndJoinBeforeReturn(t *testing.T) {
	t.Run("Content", func(t *testing.T) {
		stage := newFeatureTransitionBlockingStage()
		runtime := &Runtime{
			contentManager: &featureTransitionBlockingContent{stage: stage},
			transitioner:   &featureTransitionContextAdmissionProbe{},
			enablement:     readyGAEnablement(),
		}
		assertFeatureTransitionBlockingStageJoinsBeforeReturn(t, stage, func(ctx context.Context) error {
			return runtime.transitionFeatureWithConfigs(ctx, testFoundationTransitionConfigs(t, false, true), func() error { return nil })
		})
	})
	t.Run("Admission", func(t *testing.T) {
		stage := newFeatureTransitionBlockingStage()
		runtime := &Runtime{
			contentManager: &featureTransitionContextContentProbe{},
			transitioner:   &featureTransitionBlockingAdmission{stage: stage},
			enablement:     readyGAEnablement(),
		}
		assertFeatureTransitionBlockingStageJoinsBeforeReturn(t, stage, func(ctx context.Context) error {
			return runtime.transitionFeatureWithConfigs(ctx, testFoundationTransitionConfigs(t, false, true), func() error { return nil })
		})
	})
	t.Run("persist", func(t *testing.T) {
		stage := newFeatureTransitionBlockingStage()
		admission := &featureTransitionContextAdmissionProbe{}
		runtime := &Runtime{
			contentManager: &featureTransitionContextContentProbe{}, transitioner: admission, enablement: readyGAEnablement(),
		}
		assertFeatureTransitionBlockingStageJoinsBeforeReturn(t, stage, func(ctx context.Context) error {
			return runtime.transitionFeatureWithConfigs(ctx, testFoundationTransitionConfigs(t, false, true), func() error {
				return stage.await(admission.ctx)
			})
		})
	})
	for _, stageName := range []string{"Export", "Recovery"} {
		t.Run(stageName, func(t *testing.T) {
			stage := newFeatureTransitionBlockingStage()
			settingsService := settings.NewService(openRuntimeTestDB(t))
			current, err := settingsService.BackupAssetSettingsSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			effective := make(map[string]string, len(current))
			for key, value := range current {
				effective[key] = value
			}
			effective["backup_assets.export.worker_concurrency"] = "3"
			config, err := backupasset.ExportConfigFromValues(effective)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &Runtime{
				transitioner: &featureTransitionContextAdmissionProbe{},
				settings:     settingsService,
			}
			if stageName == "Export" {
				runtime.exportManager = &featureTransitionBlockingExport{stage: stage}
			} else {
				runtime.exportManager = &featureTransitionContextExportProbe{}
				runtime.recoveryManager = &featureTransitionBlockingRecovery{stage: stage}
			}
			assertFeatureTransitionBlockingStageJoinsBeforeReturn(t, stage, func(ctx context.Context) error {
				return runtime.TransitionBackupAssetSettings(
					ctx, current, map[string]string{"backup_assets.export.worker_concurrency": "3"}, effective, config,
					func() error { return nil },
				)
			})
		})
	}
}

func (*featureTransitionContextRecoveryProbe) StartupWithConfig(context.Context, backupasset.RecoveryConfig) error {
	return nil
}
func (probe *featureTransitionContextRecoveryProbe) TransitionSettingsWithRestore(
	ctx context.Context,
	_ backupasset.RecoveryConfig,
	persist func() error,
	_ func() error,
) error {
	probe.ctx = ctx
	return persist()
}
func (*featureTransitionContextRecoveryProbe) DowngradeReadiness(context.Context) (RecoveryDowngradeReadiness, error) {
	return RecoveryDowngradeReadiness{}, nil
}
func (*featureTransitionContextRecoveryProbe) StopAccepting()                 {}
func (*featureTransitionContextRecoveryProbe) Run(context.Context)            {}
func (*featureTransitionContextRecoveryProbe) Shutdown(context.Context) error { return nil }
func (*featureTransitionContextRecoveryProbe) PrepareSchemaDown(_ context.Context, callback func() error) error {
	return callback()
}

func TestBackupAssetSettingsTransitionPropagatesOneBoundedContextThroughAllRuntimeStages(t *testing.T) {
	content := &featureTransitionContextContentProbe{}
	admission := &featureTransitionContextAdmissionProbe{}
	export := &featureTransitionContextExportProbe{}
	recovery := &featureTransitionContextRecoveryProbe{}
	settingsService := settings.NewService(openRuntimeTestDB(t))
	runtime := &Runtime{
		contentManager:  content,
		transitioner:    admission,
		exportManager:   export,
		recoveryManager: recovery,
		enablement:      readyGAEnablement(),
		settings:        settingsService,
	}
	current := runtimeFoundationSettings(false)
	effective := runtimeFoundationSettings(true)
	if err := runtime.TransitionBackupAssetSettings(
		context.Background(), current, map[string]string{"backup_assets.enabled": "true"}, effective,
		backupasset.ExportConfig{}, func() error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	wantDeadline, ok := admission.ctx.Deadline()
	if !ok {
		t.Fatal("Admission received an unbounded transition context")
	}
	for stage, stageCtx := range map[string]context.Context{
		"Content":  content.contexts[0],
		"Export":   export.ctx,
		"Recovery": recovery.ctx,
	} {
		gotDeadline, ok := stageCtx.Deadline()
		if !ok || !gotDeadline.Equal(wantDeadline) {
			t.Errorf("%s deadline=%s ok=%t, want shared operation deadline=%s", stage, gotDeadline, ok, wantDeadline)
		}
	}
}

func TestBackupAssetSettingsExternalRestoreUsesSharedCleanupDeadlineAndFencesFailure(t *testing.T) {
	primaryErr := errors.New("FAKE_EXTERNAL_RESTORE_PRIMARY_FAILURE_FOR_TEST_ONLY")
	restoreErr := errors.New("FAKE_EXTERNAL_RESTORE_COMPENSATION_FAILURE_FOR_TEST_ONLY")
	events := []string{}
	runtime := &Runtime{
		contentManager: &runtimeContentManagerFake{events: &events},
		transitioner:   &runtimeFeatureTransitionerFake{events: &events},
		exportManager:  &featureTransitionRestoreExportProbe{failure: primaryErr},
		enablement:     readyGAEnablement(),
	}
	current := runtimeFoundationSettings(false)
	effective := runtimeFoundationSettings(true)
	restoreDeadlines := make([]time.Time, 0, 2)
	err := runtime.TransitionBackupAssetSettingsContextWithRestore(
		context.Background(), current, map[string]string{"backup_assets.enabled": "true"}, effective,
		backupasset.ExportConfig{}, func(context.Context) error { return nil }, func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("external import restore received an unbounded context")
			}
			restoreDeadlines = append(restoreDeadlines, deadline)
			if len(restoreDeadlines) == 1 {
				return restoreErr
			}
			return nil
		},
	)
	if !errors.Is(err, primaryErr) || !errors.Is(err, restoreErr) || !errors.Is(err, ErrFeatureTransitionCompensation) {
		t.Fatalf("transition error=%v, want primary, restore and typed compensation failures", err)
	}
	if len(restoreDeadlines) < 2 {
		t.Fatalf("external restore deadlines=%v, want one shared absolute deadline", restoreDeadlines)
	}
	for _, deadline := range restoreDeadlines[1:] {
		if !deadline.Equal(restoreDeadlines[0]) {
			t.Fatalf("external restore deadlines=%v, want one shared absolute deadline", restoreDeadlines)
		}
	}
	if !runtime.featureTransitionFenced.Load() || runtime.featureTransitionReady() {
		t.Fatal("external import restore failure did not fence the runtime fail closed")
	}
}

func TestBackupAssetSettingsSearchFailureRestoresExactPriorAdmissionModeAfterImportedHistory(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_IMPORTED_HISTORY_SEARCH_FAILURE_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	fixture := newAdmissionControllerFixture(t, false, nil)
	fixture.initialize(t)
	priorMode, err := fixture.controller.CurrentMode()
	if err != nil || priorMode != publication.AdmissionPristineLegacy {
		t.Fatalf("prior admission mode=%q err=%v", priorMode, err)
	}
	events := []string{}
	if err := fixture.db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	searchErr := errors.New("FAKE_IMPORTED_HISTORY_SEARCH_FAILURE_FOR_TEST_ONLY")
	backend := newSearchWorkerBackendFake()
	backend.overlayErr = searchErr
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		},
		Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 17, 0, 0, 0, time.UTC)
	runtime := &Runtime{
		admission:      fixture.controller,
		transitioner:   fixture.controller,
		contentManager: &runtimeContentManagerFake{events: &events},
		exportManager:  &featureTransitionContextAwareExportProbe{},
		enablement:     readyGAEnablement(),
		keyring:        backupasset.NewKeyring(fixture.db, func() time.Time { return now }),
		searchWorker:   worker,
	}
	current := runtimeFoundationSettings(false)
	effective := runtimeFoundationSettings(true)
	const latchID = "installation:imported-history"
	err = runtime.TransitionBackupAssetSettingsContextWithRestore(
		context.Background(), current, map[string]string{"backup_assets.enabled": "true"}, effective,
		backupasset.ExportConfig{}, func(ctx context.Context) error {
			return fixture.db.WithContext(ctx).Create(&model.BackupAssetManagedHistoryLatch{
				ID: latchID, Scope: "installation", FirstSemantics: string(backupasset.PointNativeSnapshot),
				FirstOrigin: "config_import", FirstSeenAt: now, CreatedAt: now, UpdatedAt: now,
			}).Error
		}, func(ctx context.Context) error {
			return fixture.db.WithContext(ctx).Where("id = ?", latchID).Delete(&model.BackupAssetManagedHistoryLatch{}).Error
		},
	)
	if !errors.Is(err, searchErr) {
		t.Fatalf("transition error=%v, want Search failure", err)
	}
	mode, modeErr := fixture.controller.CurrentMode()
	if modeErr != nil || mode != priorMode {
		t.Fatalf("Admission restored mode=%q err=%v, want exact prior %q", mode, modeErr, priorMode)
	}
	var latchCount int64
	if countErr := fixture.db.Model(&model.BackupAssetManagedHistoryLatch{}).Where("id = ?", latchID).Count(&latchCount).Error; countErr != nil || latchCount != 0 {
		t.Fatalf("imported history compensation count=%d err=%v", latchCount, countErr)
	}
}

func TestBackupAssetSettingsTransitionUsesContextAwareExportPersistence(t *testing.T) {
	settingsService := settings.NewService(openRuntimeTestDB(t))
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	const key = "backup_assets.export.worker_concurrency"
	effective := make(map[string]string, len(current))
	for settingKey, value := range current {
		effective[settingKey] = value
	}
	effective[key] = "3"
	export := &featureTransitionContextAwareExportProbe{}
	runtime := &Runtime{
		transitioner:  &featureTransitionContextAdmissionProbe{},
		exportManager: export,
		settings:      settingsService,
	}
	persistCalled := false
	if err := runtime.TransitionBackupAssetSettingsContext(
		context.Background(), current, map[string]string{key: "3"}, effective,
		backupasset.ExportConfig{}, func(persistCtx context.Context) error {
			persistCalled = true
			if persistCtx != export.ctx {
				t.Fatal("Export did not pass its transition context to persistence")
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if export.legacyCalled || !persistCalled {
		t.Fatalf("Export persistence seam legacy=%t context-aware persist=%t", export.legacyCalled, persistCalled)
	}
}

func TestBackupAssetSettingsTransitionUsesContextAwareRecoveryPersistence(t *testing.T) {
	settingsService := settings.NewService(openRuntimeTestDB(t))
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	const key = "backup_assets.recovery.enabled"
	effective := make(map[string]string, len(current))
	for settingKey, value := range current {
		effective[settingKey] = value
	}
	effective[key] = "true"
	recovery := &featureTransitionContextAwareRecoveryProbe{}
	runtime := &Runtime{
		transitioner:    &featureTransitionContextAdmissionProbe{},
		exportManager:   &featureTransitionContextExportProbe{},
		recoveryManager: recovery,
		settings:        settingsService,
	}
	persistCalled := false
	if err := runtime.TransitionBackupAssetSettingsContext(
		context.Background(), current, map[string]string{key: "true"}, effective,
		backupasset.ExportConfig{}, func(persistCtx context.Context) error {
			persistCalled = true
			if persistCtx != recovery.ctx {
				t.Fatal("Recovery did not pass its transition context to persistence")
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if recovery.legacyCalled || !persistCalled {
		t.Fatalf("Recovery persistence seam legacy=%t context-aware persist=%t", recovery.legacyCalled, persistCalled)
	}
}

func TestBackupAssetSettingsPersistenceReceivesCanceledOperationContext(t *testing.T) {
	db := openRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	const key = "backup_assets.export.worker_concurrency"
	effective := make(map[string]string, len(current))
	for settingKey, value := range current {
		effective[settingKey] = value
	}
	effective[key] = "3"

	runtime := &Runtime{
		transitioner:  &featureTransitionContextAdmissionProbe{},
		exportManager: &featureTransitionContextExportProbe{},
		settings:      settingsService,
	}
	transitioner, ok := any(runtime).(interface {
		TransitionBackupAssetSettingsContext(
			context.Context,
			map[string]string,
			map[string]string,
			map[string]string,
			backupasset.ExportConfig,
			func(context.Context) error,
		) error
	})
	if !ok {
		t.Fatal("runtime settings persistence callback is not context-aware")
	}

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	started := make(chan struct{})
	callbackErr := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- transitioner.TransitionBackupAssetSettingsContext(
			callerCtx,
			current,
			map[string]string{key: "3"},
			effective,
			backupasset.ExportConfig{},
			func(persistCtx context.Context) error {
				close(started)
				<-persistCtx.Done()
				callbackErr <- persistCtx.Err()
				return settingsService.UpdateManyContext(persistCtx, map[string]string{key: "3"})
			},
		)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runtime settings persistence callback did not start")
	}
	cancelCaller()
	select {
	case err := <-callbackErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("persistence callback context error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("persistence callback did not observe cancellation")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("settings transition error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("settings transition did not return after persistence cancellation")
	}
	var count int64
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("canceled operation persisted %s row count=%d", key, count)
	}
}

func TestFeatureTransitionCompensationFailureJoinsPrimaryAndStaysFailClosed(t *testing.T) {
	primaryErr := errors.New("FAKE_PHASE3_PRIMARY_PERSIST_FAILURE_FOR_TEST_ONLY")
	compensationErr := errors.New("FAKE_PHASE3_CONTENT_COMPENSATION_FAILURE_FOR_TEST_ONLY")
	events := []string{}
	content := &runtimeContentManagerFake{events: &events, prepareDisableErr: compensationErr}
	runtime := &Runtime{
		contentManager: content,
		transitioner:   &runtimeFeatureTransitionerFake{events: &events},
		enablement:     readyGAEnablement(),
	}
	err := runtime.transitionFeatureWithConfigs(
		context.Background(), testFoundationTransitionConfigs(t, false, true),
		func() error {
			events = append(events, "persist-enabled")
			return primaryErr
		},
	)
	if !errors.Is(err, primaryErr) || !errors.Is(err, compensationErr) || !errors.Is(err, ErrFeatureTransitionCompensation) {
		t.Fatalf("joined transition error=%v, want primary + compensation + typed compensation sentinel", err)
	}
	want := []string{
		"content-prepare-enable", "admission-transition-true", "persist-enabled",
		"content-ready-false", "admission-transition-false", "content-prepare-disable",
	}
	if got := strings.Join(events, ","); got != strings.Join(want, ",") {
		t.Fatalf("compensation order=%v, want reverse order=%v", events, want)
	}
	if runtime.featureTransitionReady() {
		t.Fatal("compensation failure left the feature transition ready")
	}
}

func TestFeatureTransitionCompensationFenceClosesFeatureLive(t *testing.T) {
	runtime := EnablementRuntime(readyGAEnablement(), nil)
	runtime.foundation = backupasset.NewFoundationService(runtimeFoundationSettings(true))
	runtime.featureTransitionFenced.Store(true)
	live, err := runtime.FeatureLive()
	if err != nil {
		t.Fatal(err)
	}
	if live {
		t.Fatal("compensation-fenced runtime reported FeatureLive")
	}
}

func TestFeatureTransitionCompensationFenceRejectsFurtherMutation(t *testing.T) {
	events := []string{}
	runtime := &Runtime{
		contentManager: &runtimeContentManagerFake{events: &events},
		transitioner:   &runtimeFeatureTransitionerFake{events: &events},
		enablement:     readyGAEnablement(),
	}
	runtime.featureTransitionFenced.Store(true)
	persistCalled := false
	err := runtime.transitionFeatureWithConfigs(
		context.Background(), testFoundationTransitionConfigs(t, false, true),
		func() error { persistCalled = true; return nil },
	)
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("fenced transition error=%v, want ErrInvalidState", err)
	}
	if persistCalled || len(events) != 0 {
		t.Fatalf("fenced transition mutated state: persist=%t events=%v", persistCalled, events)
	}
}

func TestFeatureTransitionCompensationFenceRejectsNonEnabledFoundationMutation(t *testing.T) {
	const key = "backup_assets.export.worker_concurrency"
	current := runtimeFoundationSettings(false)
	effective := runtimeFoundationSettings(false)
	effective[key] = "3"
	config, err := backupasset.ExportConfigFromValues(effective)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	runtime := &Runtime{
		exportManager: &runtimeExportSettingsManagerFake{events: &events},
		transitioner:  &runtimeFeatureTransitionerFake{events: &events},
		settings:      settings.NewService(openRuntimeTestDB(t)),
	}
	runtime.featureTransitionFenced.Store(true)
	persistCalled := false
	err = runtime.TransitionBackupAssetSettings(
		context.Background(), current, map[string]string{key: "3"}, effective, config,
		func() error { persistCalled = true; return nil },
	)
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("fenced non-enabled Foundation transition error=%v, want ErrInvalidState", err)
	}
	if persistCalled || len(events) != 0 {
		t.Fatalf("fenced non-enabled Foundation transition mutated state: persist=%t events=%v", persistCalled, events)
	}
}

func TestFeatureDisableFailureRestoresPriorSearchReadiness(t *testing.T) {
	primaryErr := errors.New("FAKE_PHASE3_DISABLE_PERSIST_FAILURE_FOR_TEST_ONLY")
	events := []string{}
	searchReady := &atomic.Bool{}
	searchReady.Store(true)
	runtime := &Runtime{
		contentManager: &runtimeContentManagerFake{events: &events},
		transitioner:   &runtimeFeatureTransitionerFake{events: &events, enabled: true},
		searchReady:    searchReady,
	}
	err := runtime.transitionFeatureWithConfigs(
		context.Background(), testFoundationTransitionConfigs(t, true, false),
		func() error { return primaryErr },
	)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("disable transition error=%v, want persist failure", err)
	}
	if !searchReady.Load() {
		t.Fatal("failed disable did not restore prior Search readiness")
	}
}

func TestFeatureDisableContentFailureRestoresPriorContentAndSearch(t *testing.T) {
	primaryErr := errors.New("FAKE_PHASE3_CONTENT_DISABLE_FAILURE_FOR_TEST_ONLY")
	events := []string{}
	searchReady := &atomic.Bool{}
	searchReady.Store(true)
	runtime := &Runtime{
		contentManager: &runtimeContentManagerFake{events: &events, prepareDisableErr: primaryErr},
		transitioner:   &runtimeFeatureTransitionerFake{events: &events, enabled: true},
		searchReady:    searchReady,
	}
	err := runtime.transitionFeatureWithConfigs(
		context.Background(), testFoundationTransitionConfigs(t, true, false), func() error { return nil },
	)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("disable transition error=%v, want Content failure", err)
	}
	if !searchReady.Load() {
		t.Fatal("Content disable failure did not restore prior Search readiness")
	}
	want := []string{"content-ready-false", "content-prepare-disable", "content-prepare-enable", "content-ready-true"}
	if got := strings.Join(events, ","); got != strings.Join(want, ",") {
		t.Fatalf("Content compensation order=%v, want %v", events, want)
	}
}

func TestFeatureTransitionSettingCompensationRestoresExactPriorEnabledValue(t *testing.T) {
	db := openRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{
		settings:     settingsService,
		transitioner: &runtimeFeatureTransitionerFake{events: &[]string{}},
	}
	if err := runtime.revertEnabledAfterSearchFailure(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := settingsService.GetEffective("backup_assets.enabled"); got != "true" {
		t.Fatalf("setting compensation=%q, want exact prior true", got)
	}
}

func TestBackupAssetSettingsExportFailureRestoresExactPriorOverlay(t *testing.T) {
	db := openRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	const key = "backup_assets.export.worker_concurrency"
	if err := settingsService.Update(key, "2"); err != nil {
		t.Fatal(err)
	}
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	effective := make(map[string]string, len(current))
	for settingKey, value := range current {
		effective[settingKey] = value
	}
	effective[key] = "3"
	config, err := backupasset.ExportConfigFromValues(effective)
	if err != nil {
		t.Fatal(err)
	}
	primaryErr := errors.New("FAKE_PHASE3_EXPORT_AFTER_PERSIST_FAILURE_FOR_TEST_ONLY")
	events := []string{}
	export := &runtimeExportSettingsManagerFake{events: &events, failAfterPersist: primaryErr}
	runtime := &Runtime{
		exportManager: export,
		transitioner:  &runtimeFeatureTransitionerFake{events: &events},
		settings:      settingsService,
	}
	err = runtime.TransitionBackupAssetSettings(
		context.Background(), current, map[string]string{key: "3"}, effective, config,
		func() error { return settingsService.UpdateMany(map[string]string{key: "3"}) },
	)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("transition error=%v, want Export failure", err)
	}
	if got := settingsService.GetEffective(key); got != "2" {
		t.Fatalf("failed Export transition retained %s=%q, want exact prior 2", key, got)
	}
	if export.restoreCalls != 1 {
		t.Fatalf("Export restore calls=%d, want 1", export.restoreCalls)
	}
}

func TestBackupAssetSettingsExportFailureRestoresPriorOverrideAbsence(t *testing.T) {
	db := openRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	const key = "backup_assets.export.worker_concurrency"
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	effective := make(map[string]string, len(current))
	for settingKey, value := range current {
		effective[settingKey] = value
	}
	effective[key] = "3"
	config, err := backupasset.ExportConfigFromValues(effective)
	if err != nil {
		t.Fatal(err)
	}
	primaryErr := errors.New("FAKE_PHASE3_EXPORT_ABSENT_OVERRIDE_FAILURE_FOR_TEST_ONLY")
	events := []string{}
	runtime := &Runtime{
		exportManager: &runtimeExportSettingsManagerFake{events: &events, failAfterPersist: primaryErr},
		transitioner:  &runtimeFeatureTransitionerFake{events: &events},
		settings:      settingsService,
	}
	err = runtime.TransitionBackupAssetSettings(
		context.Background(), current, map[string]string{key: "3"}, effective, config,
		func() error { return settingsService.UpdateMany(map[string]string{key: "3"}) },
	)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("transition error=%v, want Export failure", err)
	}
	var row model.SystemSetting
	if err := db.Where("key = ?", key).Take(&row).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("rollback override row error=%v row=%+v, want exact prior absence", err, row)
	}
}

func TestBackupAssetSettingsPrivateNetworkFailureRestoresExactFoundationConfig(t *testing.T) {
	db := openRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	const key = "backup_assets.content_allow_insecure_private_network"
	if err := settingsService.Update(key, "false"); err != nil {
		t.Fatal(err)
	}
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	effective := make(map[string]string, len(current))
	for settingKey, value := range current {
		effective[settingKey] = value
	}
	effective[key] = "true"
	config, err := backupasset.ExportConfigFromValues(effective)
	if err != nil {
		t.Fatal(err)
	}
	primaryErr := errors.New("FAKE_PRIVATE_NETWORK_CONTENT_AFTER_PERSIST_FAILURE_FOR_TEST_ONLY")
	events := []string{}
	export := &runtimeExportSettingsManagerFake{events: &events, failAfterPersist: primaryErr}
	runtime := &Runtime{
		exportManager: export,
		transitioner:  &runtimeFeatureTransitionerFake{events: &events},
		settings:      settingsService,
	}
	err = runtime.TransitionBackupAssetSettings(
		context.Background(), current, map[string]string{key: "true"}, effective, config,
		func() error { return settingsService.UpdateMany(map[string]string{key: "true"}) },
	)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("transition error=%v, want Export failure", err)
	}
	var row model.SystemSetting
	if err := db.Where("key = ?", key).Take(&row).Error; err != nil || row.Value != "false" {
		t.Fatalf("restored override=%+v err=%v", row, err)
	}
	contentConfig, err := backupasset.NewFoundationService(settingsService).ContentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if contentConfig.AllowInsecurePrivateNetwork || export.restoreCalls != 1 {
		t.Fatalf("restored Content config private_network=%t restore_calls=%d", contentConfig.AllowInsecurePrivateNetwork, export.restoreCalls)
	}
}

func TestBackupAssetSettingsSearchFailureRestoresEntirePriorBundleAndStamp(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_PHASE3_BUNDLE_SEARCH_ROLLBACK_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	now := time.Date(2026, 8, 24, 16, 0, 0, 0, time.UTC)
	priorStamp := now.Add(-time.Hour)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetInstallation{},
		&model.BackupAssetInventoryRun{}, &model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetInstallation{
		ID: strings.Repeat("e", 32), Slot: 1, Class: string(ga.InstallationFresh),
		Readiness: string(ga.ReadinessReady), EnablementSucceededAt: &priorStamp,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	const concurrencyKey = "backup_assets.export.worker_concurrency"
	if err := settingsService.UpdateMany(map[string]string{
		"backup_assets.enabled": "false", concurrencyKey: "2", "backup_assets.recovery.enabled": "false",
	}); err != nil {
		t.Fatal(err)
	}
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	effective := make(map[string]string, len(current))
	for key, value := range current {
		effective[key] = value
	}
	effective["backup_assets.enabled"] = "true"
	effective[concurrencyKey] = "3"
	effective["backup_assets.recovery.enabled"] = "true"
	config, err := backupasset.ExportConfigFromValues(effective)
	if err != nil {
		t.Fatal(err)
	}
	searchErr := errors.New("FAKE_PHASE3_BUNDLE_SEARCH_FAILURE_FOR_TEST_ONLY")
	backend := newSearchWorkerBackendFake()
	backend.overlayErr = searchErr
	worker, err := NewSearchWorker(SearchWorkerDependencies{
		Config: func() (SearchWorkerConfig, error) {
			return SearchWorkerConfig{Enabled: true, ReconcileInterval: time.Minute, ReconcileBatchSize: 10, WorkerConcurrency: 1}, nil
		}, Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	admission := newAdmissionControllerFixture(t, false, nil)
	admission.initialize(t)
	export := &runtimeExportSettingsManagerFake{events: &events}
	recovery := &runtimeRecoveryManagerFake{events: &events}
	runtime := EnablementRuntime(readyGAEnablement(), admission.controller)
	runtime.admission = admission.controller
	runtime.foundation = backupasset.NewFoundationService(settingsService)
	runtime.settings = settingsService
	runtime.keyring = backupasset.NewKeyring(db, func() time.Time { return now })
	runtime.searchWorker = worker
	runtime.contentManager = &runtimeContentManagerFake{events: &events}
	runtime.exportManager = export
	runtime.recoveryManager = recovery
	runtime.inventory = ga.NewInventoryService(ga.InventoryDependencies{DB: db, Now: func() time.Time { return now }})

	err = runtime.TransitionBackupAssetSettings(
		context.Background(), current,
		map[string]string{"backup_assets.enabled": "true", concurrencyKey: "3", "backup_assets.recovery.enabled": "true"},
		effective, config,
		func() error {
			return settingsService.UpdateMany(map[string]string{
				"backup_assets.enabled": "true", concurrencyKey: "3", "backup_assets.recovery.enabled": "true",
			})
		},
	)
	if !errors.Is(err, searchErr) {
		t.Fatalf("transition error=%v, want Search failure", err)
	}
	for key, want := range map[string]string{
		"backup_assets.enabled": "false", concurrencyKey: "2", "backup_assets.recovery.enabled": "false",
	} {
		if got := settingsService.GetEffective(key); got != want {
			t.Errorf("restored %s=%q, want exact prior %q", key, got, want)
		}
	}
	var installation model.BackupAssetInstallation
	if err := db.Where("slot = ?", 1).Take(&installation).Error; err != nil {
		t.Fatal(err)
	}
	if installation.EnablementSucceededAt == nil || !installation.EnablementSucceededAt.Equal(priorStamp) {
		t.Fatalf("Search failure stamp=%v, want exact prior %s", installation.EnablementSucceededAt, priorStamp)
	}
	if got := export.globalEnabled; len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("Export transition targets=%v, want prospective true then prior false", got)
	}
	if got := recovery.configs; len(got) != 2 || !got[0].Enabled || got[1].Enabled {
		t.Fatalf("Recovery transition targets=%v, want prospective true then prior false", got)
	}
	mode, err := admission.controller.CurrentMode()
	if err != nil {
		t.Fatal(err)
	}
	if mode != publication.AdmissionPristineLegacy {
		t.Fatalf("Search failure Admission mode=%q, want exact prior %q", mode, publication.AdmissionPristineLegacy)
	}
}

func TestFeatureTransitionStampFailureRestoresExactPriorSettingAndStamp(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_ASYNC_SEARCH_STAMP_FAILURE_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	now := time.Date(2026, 8, 24, 17, 0, 0, 0, time.UTC)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{},
		&model.BackupAssetInstallation{}, &model.BackupAssetInventoryRun{},
		&model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetInstallation{
		ID: strings.Repeat("f", 32), Slot: 1, Class: string(ga.InstallationFresh),
		Readiness: string(ga.ReadinessReady),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "false"); err != nil {
		t.Fatal(err)
	}
	stampErr := errors.New("FAKE_PHASE3_STAMP_FAILURE_FOR_TEST_ONLY")
	failStamp := &atomic.Bool{}
	callbackName := "phase3_fail_enablement_stamp"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if failStamp.CompareAndSwap(true, false) {
			_ = tx.AddError(stampErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })
	events := []string{}
	backend := newSearchWorkerBackendFake()
	worker := newAsyncSearchTestWorker(t, backend, func() SearchWorkerConfig {
		return validAsyncSearchWorkerConfig(true)
	})
	admission := newAdmissionControllerFixture(t, false, nil)
	admission.initialize(t)
	runtime := EnablementRuntime(readyGAEnablement(), admission.controller)
	runtime.admission = admission.controller
	runtime.foundation = backupasset.NewFoundationService(settingsService)
	runtime.settings = settingsService
	runtime.keyring = backupasset.NewKeyring(db, func() time.Time { return now })
	runtime.searchWorker = worker
	runtime.contentManager = &runtimeContentManagerFake{events: &events}
	runtime.inventory = ga.NewInventoryService(ga.InventoryDependencies{DB: db, Now: func() time.Time { return now }})
	err := runtime.TransitionFeature(context.Background(), true, func() error {
		if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
			return err
		}
		failStamp.Store(true)
		return nil
	})
	if !errors.Is(err, stampErr) {
		t.Fatalf("transition error=%v, want stamp failure", err)
	}
	if got := settingsService.GetEffective("backup_assets.enabled"); got != "false" {
		t.Fatalf("stamp failure restored enabled=%q, want false", got)
	}
	var installation model.BackupAssetInstallation
	if err := db.Where("slot = ?", 1).Take(&installation).Error; err != nil {
		t.Fatal(err)
	}
	if installation.EnablementSucceededAt != nil {
		t.Fatalf("stamp failure restored stamp=%v, want exact prior nil", installation.EnablementSucceededAt)
	}
	if got := len(worker.wake); got != 0 {
		t.Fatalf("success-stamp failure queued wakes=%d, want zero", got)
	}
}

func TestBackupAssetSettingsRecoveryFailureRestoresExactPriorSetting(t *testing.T) {
	db := openRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	const key = "backup_assets.recovery.enabled"
	if err := settingsService.Update(key, "false"); err != nil {
		t.Fatal(err)
	}
	current, err := settingsService.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	effective := make(map[string]string, len(current))
	for settingKey, value := range current {
		effective[settingKey] = value
	}
	effective[key] = "true"
	config, err := backupasset.ExportConfigFromValues(effective)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	recovery := &runtimeRecoveryManagerFake{events: &events, failAfterPersist: true}
	runtime := &Runtime{
		exportManager:   &runtimeExportSettingsManagerFake{events: &events},
		recoveryManager: recovery,
		transitioner:    &runtimeFeatureTransitionerFake{events: &events},
		settings:        settingsService,
	}
	err = runtime.TransitionBackupAssetSettings(
		context.Background(), current, map[string]string{key: "true"}, effective, config,
		func() error { return settingsService.UpdateMany(map[string]string{key: "true"}) },
	)
	if err == nil || !strings.Contains(err.Error(), "injected Recovery install failure") {
		t.Fatalf("transition error=%v, want Recovery after-persist failure", err)
	}
	if got := settingsService.GetEffective(key); got != "false" {
		t.Fatalf("failed Recovery transition retained %s=%q, want exact prior false", key, got)
	}
}
