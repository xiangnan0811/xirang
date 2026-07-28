package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	stdRuntime "runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/search"
	configpkg "xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestManagedExportRuntimeIsLazyWhenFoundationOrExportIsDisabled(t *testing.T) {
	for _, test := range []struct {
		name         string
		foundationOn bool
		exportOn     bool
	}{
		{name: "foundation disabled", exportOn: true},
		{name: "export disabled", foundationOn: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, ring := exportRuntimeKeyringFixture(t)
			root := filepath.Join(t.TempDir(), "export")
			values := runtimeFoundationSettings(test.foundationOn)
			values["backup_assets.export.enabled"] = fmt.Sprintf("%t", test.exportOn)
			values["backup_assets.export.root"] = root
			var builds atomic.Int32
			manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
				DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
				ValidateRoot: func(context.Context, string) error { return nil },
				Build: func(context.Context, backupasset.ExportConfig, *assetexport.Store) (*managedExportGraph, error) {
					builds.Add(1)
					return nil, errors.New("unexpected build")
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Startup(context.Background()); err != nil {
				t.Fatal(err)
			}
			if manager.Ready() || builds.Load() != 0 {
				t.Fatalf("ready=%v builds=%d", manager.Ready(), builds.Load())
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("disabled runtime touched root: %v", err)
			}
			var keyCount int64
			if err := db.Model(&model.WrappedDomainKey{}).
				Where("domain = ?", backupasset.KeyDomainExportStore).Count(&keyCount).Error; err != nil || keyCount != 0 {
				t.Fatalf("disabled runtime key count=%d err=%v", keyCount, err)
			}
		})
	}
}

func TestManagedExportRuntimeDisabledStartupCleansRootOrphanWithoutPublishing(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, strings.Repeat("a", 32)+".xre")
	if err := os.WriteFile(orphan, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	values["backup_assets.export.root"] = root
	var builds atomic.Int32
	events := make([]string, 0, 2)
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			builds.Add(1)
			return &managedExportGraph{
				store:         store,
				stopAccepting: func() { events = append(events, "stop") },
				terminalize: func(context.Context) error {
					events = append(events, "terminalize")
					_, err := store.PurgeUnreferenced(nil)
					return err
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 1 || manager.Ready() || manager.graph != nil || manager.publication.current() != nil {
		t.Fatalf("builds=%d ready=%v graph=%p published=%p", builds.Load(), manager.Ready(), manager.graph, manager.publication.current())
	}
	if !reflect.DeepEqual(events, []string{"stop", "terminalize"}) {
		t.Fatalf("disabled maintenance events=%v", events)
	}
	if _, err := os.Lstat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled maintenance retained orphan: %v", err)
	}
	var keyCount int64
	if err := db.Model(&model.WrappedDomainKey{}).
		Where("domain = ?", backupasset.KeyDomainExportStore).Count(&keyCount).Error; err != nil || keyCount != 0 {
		t.Fatalf("disabled maintenance key count=%d err=%v", keyCount, err)
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("disabled maintenance retained Export Store lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedExportRuntimeDisabledMaintenanceCleanupAllowsOneShotFailureRetry(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, strings.Repeat("a", 32)+".xre")
	if err := os.WriteFile(orphan, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	values["backup_assets.export.root"] = root
	cleanupErr := errors.New("disabled maintenance cleanup failed once")
	var builds, terminalizations atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil }, Build: func(
			_ context.Context,
			_ backupasset.ExportConfig,
			store *assetexport.Store,
		) (*managedExportGraph, error) {
			builds.Add(1)
			return &managedExportGraph{
				store: store,
				terminalize: func(context.Context) error {
					if terminalizations.Add(1) == 1 {
						return cleanupErr
					}
					_, err := store.PurgeUnreferenced(nil)
					return err
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); !errors.Is(err, cleanupErr) {
		t.Fatalf("first disabled maintenance Startup error=%v, want one-shot cleanup error", err)
	}
	if manager.graph != nil || manager.Ready() || manager.publication.current() != nil {
		t.Fatalf("failed disabled maintenance retained graph=%p ready=%v published=%p", manager.graph, manager.Ready(), manager.publication.current())
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("failed disabled maintenance retained Export Store lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatalf("retry disabled maintenance Startup: %v", err)
	}
	if builds.Load() != 2 || terminalizations.Load() != 2 {
		t.Fatalf("disabled maintenance retry builds=%d terminalizations=%d, want two each", builds.Load(), terminalizations.Load())
	}
	if _, err := os.Lstat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled maintenance retry retained orphan: %v", err)
	}
}

func TestManagedExportRuntimeDisabledStartupDoesNotMaintainAfterShutdown(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, strings.Repeat("a", 32)+".xre")
	if err := os.WriteFile(orphan, []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	values["backup_assets.export.root"] = root
	settings := &blockingExportRuntimeSettings{
		values:          exportRuntimeSettings(values),
		snapshotEntered: make(chan struct{}),
		releaseSnapshot: make(chan struct{}),
	}
	var validations, builds, stops, terminalizations atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(settings), Keyring: ring,
		ValidateRoot: func(context.Context, string) error {
			validations.Add(1)
			return nil
		},
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			builds.Add(1)
			return &managedExportGraph{
				store:         store,
				stopAccepting: func() { stops.Add(1) },
				terminalize: func(context.Context) error {
					terminalizations.Add(1)
					_, err := store.PurgeUnreferenced(nil)
					return err
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var releaseSnapshotOnce sync.Once
	releaseSnapshot := func() {
		releaseSnapshotOnce.Do(func() { close(settings.releaseSnapshot) })
	}
	startupDone := make(chan struct{})
	var startupErr error
	go func() {
		startupErr = manager.Startup(context.Background())
		close(startupDone)
	}()
	t.Cleanup(func() {
		releaseSnapshot()
		select {
		case <-startupDone:
		case <-time.After(time.Second):
			t.Error("blocked disabled Export startup did not return")
		}
	})

	select {
	case <-settings.snapshotEntered:
	case <-time.After(time.Second):
		t.Fatal("disabled Export startup did not begin resolving its settings snapshot")
	}
	if manager.graph != nil {
		t.Fatal("disabled Export startup installed a graph before the settings snapshot completed")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown while disabled Export startup is resolving settings: %v", err)
	}

	releaseSnapshot()
	select {
	case <-startupDone:
	case <-time.After(time.Second):
		t.Fatal("disabled Export startup did not return after Shutdown")
	}
	if !errors.Is(startupErr, backupasset.ErrInvalidState) {
		t.Fatalf("Startup after Shutdown error=%v, want unavailable/stopped", startupErr)
	}
	if validations.Load() != 0 || builds.Load() != 0 || stops.Load() != 0 || terminalizations.Load() != 0 {
		t.Fatalf(
			"shutdown-won disabled maintenance validations=%d builds=%d stops=%d terminalizations=%d",
			validations.Load(), builds.Load(), stops.Load(), terminalizations.Load(),
		)
	}
	if _, err := os.Lstat(orphan); err != nil {
		t.Fatalf("shutdown-won disabled maintenance removed orphan: %v", err)
	}
	if manager.Ready() || manager.graph != nil || manager.publication.current() != nil {
		t.Fatalf("shutdown-won disabled startup ready=%v graph=%p published=%p", manager.Ready(), manager.graph, manager.publication.current())
	}
}

func TestManagedExportRuntimeDisabledStartupTerminalizesDurableWorkWithoutStartingRunner(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	jobID := strings.Repeat("b", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionFailed), CleanupState: string(assetexport.CleanupNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	values["backup_assets.export.root"] = root
	events := make([]string, 0, 3)
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			return &managedExportGraph{
				store:         store,
				startup:       func(context.Context) error { events = append(events, "startup"); return nil },
				stopAccepting: func() { events = append(events, "stop") },
				terminalize: func(context.Context) error {
					events = append(events, "terminalize")
					return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
						Update("cleanup_state", string(assetexport.CleanupPurged)).Error
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"stop", "terminalize"}) {
		t.Fatalf("disabled durable maintenance events=%v", events)
	}
	var job model.BackupAssetExportJob
	if err := db.First(&job, "id = ?", jobID).Error; err != nil || job.CleanupState != string(assetexport.CleanupPurged) {
		t.Fatalf("durable maintenance job=%+v err=%v", job, err)
	}
	if manager.Ready() || manager.graph != nil || manager.publication.current() != nil {
		t.Fatalf("ready=%v graph=%p published=%p", manager.Ready(), manager.graph, manager.publication.current())
	}
	var keyCount int64
	if err := db.Model(&model.WrappedDomainKey{}).
		Where("domain = ?", backupasset.KeyDomainExportStore).Count(&keyCount).Error; err != nil || keyCount != 0 {
		t.Fatalf("disabled durable maintenance key count=%d err=%v", keyCount, err)
	}
}

func TestManagedExportRuntimeStartupMarksUnreadableExportKEKLostBeforePublishing(t *testing.T) {
	fixture := newRuntimeExportKeyLossFixture(t)
	if err := fixture.db.Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND version = ?", backupasset.KeyDomainExportStore, fixture.keyVersion).
		Update("wrapped_key", []byte("corrupt-export-envelope")).Error; err != nil {
		t.Fatal(err)
	}

	err := fixture.runtime.Startup(context.Background())
	if !errors.Is(err, assetexport.ErrUnavailable) {
		t.Fatalf("Startup unreadable Export KEK error=%v, want unavailable", err)
	}
	assertRuntimeExportKeyLoss(t, fixture)
	if fixture.builds.Load() != 1 {
		t.Fatalf("key-loss startup builds=%d want=1", fixture.builds.Load())
	}
}

func TestManagedExportRuntimeStartupIsolatesUnreadableRetainedExportKEKAndPublishesHealthyActiveVersion(t *testing.T) {
	fixture := newRuntimeExportKeyLossFixture(t)
	lost := makeRuntimeExportKeyLossJobReady(t, fixture, strings.Repeat("1", 32))

	healthyMaterial, err := fixture.ring.Rotate(context.Background(), backupasset.KeyDomainExportStore, 0)
	if err != nil {
		t.Fatal(err)
	}
	healthy := seedRuntimeExportReadyJob(t, fixture.db, fixture.port.now, healthyMaterial.Version, runtimeExportReadyJobIDs{
		jobID: strings.Repeat("2", 32), keyID: strings.Repeat("3", 32), attemptID: strings.Repeat("4", 32),
		itemID: strings.Repeat("5", 32), artifactID: strings.Repeat("6", 32),
	})

	if err := fixture.db.Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND version = ?", backupasset.KeyDomainExportStore, fixture.keyVersion).
		Update("wrapped_key", []byte("corrupt-retained-export-envelope")).Error; err != nil {
		t.Fatal(err)
	}

	if err := fixture.runtime.Startup(context.Background()); err != nil {
		t.Fatalf("Startup should isolate the retained Export KEK and publish the healthy active version: %v", err)
	}
	if !fixture.runtime.Ready() || fixture.runtime.graph == nil || fixture.runtime.publication.current() == nil {
		t.Fatalf("isolated Export runtime did not publish: graph=%p published=%p ready=%v", fixture.runtime.graph, fixture.runtime.publication.current(), fixture.runtime.Ready())
	}
	if fixture.builds.Load() != 2 {
		t.Fatalf("isolated Export startup builds=%d, want loss cleanup then published graph", fixture.builds.Load())
	}

	if _, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, fixture.keyVersion); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("corrupt retained Export KEK remained available: %v", err)
	}
	healthyAfter, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, healthyMaterial.Version)
	if err != nil || healthyAfter.Version != healthyMaterial.Version || healthyAfter.State != backupasset.DomainKeyActive {
		t.Fatalf("healthy active Export KEK changed: material=%+v err=%v", healthyAfter, err)
	}

	assertRuntimeExportReadyJobPurgedForKeyLoss(t, fixture.db, lost)
	assertRuntimeExportReadyJobUntouched(t, fixture.db, healthy)
	if !reflect.DeepEqual(fixture.port.calls, runtimeExportKeyLossCalls(lost.jobID)) {
		t.Fatalf("isolated Export key-loss calls=%v want=%v", fixture.port.calls, runtimeExportKeyLossCalls(lost.jobID))
	}
}

func TestManagedExportRuntimeStartupCleansTemporaryKeyIsolationGraphBeforeRetry(t *testing.T) {
	fixture := newRuntimeExportKeyLossFixture(t)
	if _, err := fixture.ring.Rotate(context.Background(), backupasset.KeyDomainExportStore, 0); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND version = ?", backupasset.KeyDomainExportStore, fixture.keyVersion).
		Update("wrapped_key", []byte("corrupt-retained-export-envelope")).Error; err != nil {
		t.Fatal(err)
	}

	lifecycle, err := assetexport.NewLifecycle(assetexport.LifecycleDependencies{
		DB: fixture.db, Port: fixture.port, Now: func() time.Time { return fixture.port.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	var builds, temporaryStops, temporaryShutdowns, publishedStops, publishedShutdowns atomic.Int32
	var temporaryGraph, publishedGraph *managedExportGraph
	fixture.runtime.build = func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
		if builds.Add(1) == 1 {
			temporaryGraph = &managedExportGraph{
				store:         store,
				lifecycle:     lifecycle,
				stopAccepting: func() { temporaryStops.Add(1) },
				shutdown:      func(context.Context) error { temporaryShutdowns.Add(1); return nil },
			}
			return temporaryGraph, nil
		}
		publishedGraph = &managedExportGraph{
			store:         store,
			stopAccepting: func() { publishedStops.Add(1) },
			shutdown:      func(context.Context) error { publishedShutdowns.Add(1); return nil },
		}
		return publishedGraph, nil
	}

	if err := fixture.runtime.Startup(context.Background()); err != nil {
		t.Fatalf("Startup should retry after retained Export key isolation: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.runtime.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown published Export graph: %v", err)
		}
	})

	if builds.Load() != 2 || temporaryStops.Load() != 1 || temporaryShutdowns.Load() != 1 {
		t.Fatalf(
			"temporary Export key-isolation graph builds=%d stops=%d shutdowns=%d",
			builds.Load(), temporaryStops.Load(), temporaryShutdowns.Load(),
		)
	}
	if publishedStops.Load() != 0 || publishedShutdowns.Load() != 0 {
		t.Fatalf("published Export graph stopped=%d shutdowns=%d before Shutdown", publishedStops.Load(), publishedShutdowns.Load())
	}
	if temporaryGraph == nil || publishedGraph == nil || temporaryGraph == publishedGraph ||
		temporaryGraph.store == nil || temporaryGraph.store == publishedGraph.store {
		t.Fatalf("invalid temporary/published Export graph ownership: temporary=%p published=%p", temporaryGraph, publishedGraph)
	}
	if fixture.runtime.graph != publishedGraph || fixture.runtime.publication.current() != publishedGraph || !fixture.runtime.Ready() {
		t.Fatalf(
			"temporary Export graph leaked into ownership: runtime=%p published=%p ready=%v",
			fixture.runtime.graph, fixture.runtime.publication.current(), fixture.runtime.Ready(),
		)
	}
}

func TestManagedExportRuntimeStartupDoesNotInvalidateExportKeysWhenRewrapIsCanceled(t *testing.T) {
	fixture := newRuntimeExportKeyLossFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := fixture.runtime.Startup(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, assetexport.ErrUnavailable) {
		t.Fatalf("canceled Export key rewrap error=%v, want context cancellation without key-loss classification", err)
	}
	if fixture.runtime.Ready() || fixture.runtime.graph != nil || fixture.runtime.publication.current() != nil {
		t.Fatalf("canceled Export startup published graph=%p published=%p ready=%v", fixture.runtime.graph, fixture.runtime.publication.current(), fixture.runtime.Ready())
	}
	if fixture.builds.Load() != 0 {
		t.Fatalf("canceled Export startup builds=%d, want zero", fixture.builds.Load())
	}
	if _, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, fixture.keyVersion); err != nil {
		t.Fatalf("canceled Export rewrap invalidated active key: %v", err)
	}
	var job model.BackupAssetExportJob
	if err := fixture.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionRunning) || job.CleanupState != string(assetexport.CleanupNone) {
		t.Fatalf("canceled Export rewrap mutated job=%+v", job)
	}
}

func TestManagedExportRuntimeDoesNotInvalidateHealthyExportKeysWithoutUnreadableVersion(t *testing.T) {
	fixture := newRuntimeExportKeyLossFixture(t)
	foundUnreadable, err := fixture.runtime.invalidateUnreadableExportKeys(context.Background(), fixture.config)
	if err != nil || foundUnreadable {
		t.Fatalf("healthy Export KEK isolation found=%v err=%v", foundUnreadable, err)
	}
	if fixture.builds.Load() != 0 || len(fixture.port.calls) != 0 {
		t.Fatalf("healthy Export key isolation builds=%d lifecycle_calls=%v", fixture.builds.Load(), fixture.port.calls)
	}
	if _, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, fixture.keyVersion); err != nil {
		t.Fatalf("healthy Export key isolation changed key availability: %v", err)
	}
	var job model.BackupAssetExportJob
	if err := fixture.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionRunning) || job.CleanupState != string(assetexport.CleanupNone) {
		t.Fatalf("healthy Export key isolation mutated job=%+v", job)
	}
}

func TestManagedExportRuntimeDoesNotMarkExportKeysLostWhenVersionProbeFailsTransiently(t *testing.T) {
	fixture := newRuntimeExportKeyLossFixture(t)
	injected := errors.New("injected Export key version probe failure")
	callbackName := "test:runtime-export-key-version-probe-failure-" + strings.ReplaceAll(t.Name(), "/", "_")
	wrappedKeyQueries := 0
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "wrapped_domain_keys" {
			return
		}
		wrappedKeyQueries++
		if wrappedKeyQueries == 2 {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove Export key version probe callback: %v", err)
		}
	})

	foundUnreadable, err := fixture.runtime.invalidateUnreadableExportKeys(context.Background(), fixture.config)
	if foundUnreadable || !errors.Is(err, injected) {
		t.Fatalf("transient Export key version probe found=%v err=%v", foundUnreadable, err)
	}
	if wrappedKeyQueries != 2 || fixture.builds.Load() != 0 || len(fixture.port.calls) != 0 {
		t.Fatalf("transient Export key version probe queries=%d builds=%d lifecycle_calls=%v", wrappedKeyQueries, fixture.builds.Load(), fixture.port.calls)
	}
	if _, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, fixture.keyVersion); err != nil {
		t.Fatalf("transient Export key probe changed key availability: %v", err)
	}
	var job model.BackupAssetExportJob
	if err := fixture.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionRunning) || job.CleanupState != string(assetexport.CleanupNone) {
		t.Fatalf("transient Export key probe mutated job=%+v", job)
	}
}

func TestManagedExportRuntimeStartupRetriesOneShotTypedPreparationFailuresWithHealthyActiveKey(t *testing.T) {
	for _, test := range []struct {
		name        string
		failQueries map[int]struct{}
	}{
		{name: "rewrap", failQueries: map[int]struct{}{1: {}}},
		// Ensure first observes the typed failure from Active, then propagates it
		// from its locked row load without creating or rotating any key.
		{name: "ensure", failQueries: map[int]struct{}{2: {}, 3: {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRuntimeExportKeyLossFixture(t)
			injected := fmt.Errorf("%w: injected one-shot Export %s failure", backupasset.ErrKeyUnavailable, test.name)
			callbackName := "test:runtime-export-one-shot-" + strings.ReplaceAll(t.Name(), "/", "_")
			wrappedKeyQueries := 0
			failures := 0
			if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "wrapped_domain_keys" {
					return
				}
				wrappedKeyQueries++
				if _, shouldFail := test.failQueries[wrappedKeyQueries]; shouldFail {
					failures++
					_ = tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove one-shot Export key callback: %v", err)
				}
			})

			if err := fixture.runtime.Startup(context.Background()); err != nil {
				t.Fatalf("Startup should retry one-shot typed Export %s failure: %v", test.name, err)
			}
			if failures != len(test.failQueries) || !fixture.runtime.Ready() || fixture.runtime.graph == nil || fixture.runtime.publication.current() == nil {
				t.Fatalf("one-shot Export %s failures=%d ready=%v graph=%p published=%p", test.name, failures, fixture.runtime.Ready(), fixture.runtime.graph, fixture.runtime.publication.current())
			}
			if fixture.builds.Load() != 1 || len(fixture.port.calls) != 0 {
				t.Fatalf("one-shot Export %s builds=%d lifecycle_calls=%v", test.name, fixture.builds.Load(), fixture.port.calls)
			}
			if _, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, fixture.keyVersion); err != nil {
				t.Fatalf("one-shot Export %s changed healthy key availability: %v", test.name, err)
			}
		})
	}
}

func TestManagedExportRuntimeStartupRetriesClearedTransientExportKeyVersionProbeFailure(t *testing.T) {
	fixture := newRuntimeExportKeyLossFixture(t)
	preparationFailure := fmt.Errorf("%w: injected typed Export rewrap failure", backupasset.ErrKeyUnavailable)
	probeFailure := errors.New("injected transient Export key version probe failure")
	callbackName := "test:runtime-export-cleared-probe-" + strings.ReplaceAll(t.Name(), "/", "_")
	wrappedKeyQueries := 0
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "wrapped_domain_keys" {
			return
		}
		wrappedKeyQueries++
		switch wrappedKeyQueries {
		case 1:
			_ = tx.AddError(preparationFailure)
		case 3:
			_ = tx.AddError(probeFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove cleared Export key probe callback: %v", err)
		}
	})

	if err := fixture.runtime.Startup(context.Background()); err != nil {
		t.Fatalf("Startup should retry cleared transient Export key probe failure: %v", err)
	}
	if wrappedKeyQueries < 6 || !fixture.runtime.Ready() || fixture.runtime.graph == nil || fixture.runtime.publication.current() == nil {
		t.Fatalf("cleared Export key probe queries=%d ready=%v graph=%p published=%p", wrappedKeyQueries, fixture.runtime.Ready(), fixture.runtime.graph, fixture.runtime.publication.current())
	}
	if fixture.builds.Load() != 1 || len(fixture.port.calls) != 0 {
		t.Fatalf("cleared Export key probe builds=%d lifecycle_calls=%v", fixture.builds.Load(), fixture.port.calls)
	}
	if _, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, fixture.keyVersion); err != nil {
		t.Fatalf("cleared Export key probe changed healthy key availability: %v", err)
	}
}

func TestManagedExportRuntimeStartupStopsAfterPersistentTypedPreparationFailureWithoutKeyLoss(t *testing.T) {
	fixture := newRuntimeExportKeyLossFixture(t)
	injected := fmt.Errorf("%w: injected persistent Export rewrap failure", backupasset.ErrKeyUnavailable)
	callbackName := "test:runtime-export-persistent-preparation-" + strings.ReplaceAll(t.Name(), "/", "_")
	wrappedKeyQueries := 0
	failures := 0
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "wrapped_domain_keys" {
			return
		}
		wrappedKeyQueries++
		if wrappedKeyQueries == 1 || wrappedKeyQueries == 5 {
			failures++
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove persistent Export key callback: %v", err)
		}
	})

	err := fixture.runtime.Startup(context.Background())
	if !errors.Is(err, assetexport.ErrUnavailable) || !errors.Is(err, backupasset.ErrKeyUnavailable) {
		t.Fatalf("persistent Export preparation error=%v", err)
	}
	if failures != 2 || fixture.runtime.Ready() || fixture.runtime.graph != nil || fixture.runtime.publication.current() != nil {
		t.Fatalf("persistent Export preparation failures=%d ready=%v graph=%p published=%p", failures, fixture.runtime.Ready(), fixture.runtime.graph, fixture.runtime.publication.current())
	}
	if fixture.builds.Load() != 0 || len(fixture.port.calls) != 0 {
		t.Fatalf("persistent Export preparation builds=%d lifecycle_calls=%v", fixture.builds.Load(), fixture.port.calls)
	}
	if _, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, fixture.keyVersion); err != nil {
		t.Fatalf("persistent Export preparation changed healthy key availability: %v", err)
	}
	var job model.BackupAssetExportJob
	if err := fixture.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionRunning) || job.CleanupState != string(assetexport.CleanupNone) {
		t.Fatalf("persistent Export preparation mutated job=%+v", job)
	}
}

func TestManagedExportRuntimeExposesStableFacadesWhileDisabled(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(context.Context, backupasset.ExportConfig, *assetexport.Store) (*managedExportGraph, error) {
			return nil, errors.New("build must not run while disabled")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serviceFacade := manager.Service()
	deliveryFacade := manager.Delivery()
	archiveFacade := manager.ArchiveMember()
	if serviceFacade == nil || deliveryFacade == nil || archiveFacade == nil {
		t.Fatalf("disabled runtime returned unstable nil facades: service=%v delivery=%v archive=%v", serviceFacade, deliveryFacade, archiveFacade)
	}
	if _, err := serviceFacade.Status(context.Background(), assetexport.StatusRequest{Actor: assetexport.SelectionActor{UserID: 1}, JobID: strings.Repeat("1", 32)}); !errors.Is(err, assetexport.ErrUnavailable) {
		t.Fatalf("disabled service status error=%v, want ErrUnavailable", err)
	}
}

func TestManagedExportRuntimeTransitionSettingsPublishesOnlyAfterPersistence(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	values["backup_assets.export.root"] = root
	var builds atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			builds.Add(1)
			return &managedExportGraph{store: store}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.export.enabled"] = "true"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}
	transitioner, ok := any(manager).(interface {
		TransitionSettings(context.Context, bool, backupasset.ExportConfig, func() error) error
	})
	if !ok {
		t.Fatal("managed Export runtime does not provide settings transition")
	}
	persisted := false
	if err := transitioner.TransitionSettings(context.Background(), true, target, func() error {
		if manager.publication.current() != nil {
			t.Fatal("prospective Export graph was published before persistence")
		}
		persisted = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !persisted || builds.Load() != 1 || manager.publication.current() == nil || !manager.Ready() {
		t.Fatalf("persisted=%v builds=%d graph=%p ready=%v", persisted, builds.Load(), manager.publication.current(), manager.Ready())
	}
}

func TestManagedExportRuntimeTransitionSettingsSerializesStartupConfiguration(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	values["backup_assets.export.worker_concurrency"] = "1"

	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.export.worker_concurrency"] = "3"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}

	startupBuilt := make(chan backupasset.ExportConfig, 1)
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			startupBuilt <- config
			return &managedExportGraph{store: store}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	startupDone := make(chan error, 1)
	startupReadOldConfig := false
	err = manager.TransitionSettings(context.Background(), true, target, func() error {
		go func() { startupDone <- manager.Startup(context.Background()) }()
		select {
		case oldConfig := <-startupBuilt:
			startupReadOldConfig = oldConfig.WorkerConcurrency == 1
		case <-time.After(100 * time.Millisecond):
		}
		values["backup_assets.export.worker_concurrency"] = "3"
		return nil
	})
	if err != nil {
		t.Fatalf("transition settings: %v", err)
	}
	if startupReadOldConfig {
		t.Fatal("Startup built an obsolete Foundation snapshot before the settings transition committed")
	}
	select {
	case err := <-startupDone:
		if err != nil {
			t.Fatalf("concurrent startup: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Startup did not complete after the transition")
	}
	if manager.config != target {
		t.Fatalf("runtime config=%+v, want committed config %+v", manager.config, target)
	}
	if graph := manager.publication.current(); graph == nil || !manager.Ready() {
		t.Fatalf("published graph=%p ready=%v, want committed ready graph", graph, manager.Ready())
	}
}

func TestManagedExportRuntimeTransitionSettingsSerializesDisabledStartOwnership(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	values["backup_assets.export.root"] = root

	configForConcurrency := func(concurrency int) backupasset.ExportConfig {
		t.Helper()
		targetValues := make(runtimeSettings, len(values))
		for key, value := range values {
			targetValues[key] = value
		}
		targetValues["backup_assets.export.enabled"] = "true"
		targetValues["backup_assets.export.worker_concurrency"] = fmt.Sprintf("%d", concurrency)
		config, err := backupasset.ExportConfigFromValues(targetValues)
		if err != nil {
			t.Fatal(err)
		}
		return config
	}
	configA := configForConcurrency(3)
	configB := configForConcurrency(4)

	bBuildEntered := make(chan struct{})
	var bBuildOnce sync.Once
	var graphMu sync.Mutex
	graphConfigs := make(map[*managedExportGraph]backupasset.ExportConfig)
	var stopCalls, shutdownCalls atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			if config == configB {
				bBuildOnce.Do(func() { close(bBuildEntered) })
			}
			graph := &managedExportGraph{store: store}
			graph.stopAccepting = func() { stopCalls.Add(1) }
			graph.shutdown = func(context.Context) error {
				shutdownCalls.Add(1)
				return nil
			}
			graphMu.Lock()
			graphConfigs[graph] = config
			graphMu.Unlock()
			return graph, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.graph != nil || manager.publication.current() != nil {
		t.Fatal("fixture must begin disabled with no Export graph")
	}
	bTransitionAttempted := make(chan struct{})
	bTransitionLockBlocked := make(chan struct{})
	bTransitionLockAcquired := make(chan struct{})
	var transitionAttempts atomic.Int32
	manager.beforeTransitionLock = func() {
		if transitionAttempts.Add(1) != 2 {
			return
		}
		close(bTransitionAttempted)
		if manager.transitionMu.TryLock() {
			manager.transitionMu.Unlock()
			close(bTransitionLockAcquired)
			return
		}
		close(bTransitionLockBlocked)
	}

	var durableMu sync.Mutex
	var durableConfig backupasset.ExportConfig
	persisted := make([]backupasset.ExportConfig, 0, 2)
	recordPersist := func(config backupasset.ExportConfig) {
		durableMu.Lock()
		durableConfig = config
		persisted = append(persisted, config)
		durableMu.Unlock()
	}

	aPersistEntered := make(chan struct{})
	releaseAPersist := make(chan struct{})
	var releaseAOnce sync.Once
	releaseA := func() {
		releaseAOnce.Do(func() { close(releaseAPersist) })
	}
	bPersistEntered := make(chan struct{})
	aDone := make(chan struct{})
	bDone := make(chan struct{})
	var aErr, bErr error
	go func() {
		aErr = manager.TransitionSettings(context.Background(), true, configA, func() error {
			close(aPersistEntered)
			<-releaseAPersist
			recordPersist(configA)
			return nil
		})
		close(aDone)
	}()
	t.Cleanup(func() {
		releaseA()
		<-aDone
		<-bDone
		_ = manager.Shutdown(context.Background())
	})

	<-aPersistEntered
	go func() {
		bErr = manager.TransitionSettings(context.Background(), true, configB, func() error {
			close(bPersistEntered)
			recordPersist(configB)
			return nil
		})
		close(bDone)
	}()

	// B reached the transition boundary, but A still owns the complete
	// transition through persistence. The test hook proves that B cannot acquire
	// that ownership, and every subsequent boundary remains untouched.
	<-bTransitionAttempted
	select {
	case <-bTransitionLockBlocked:
	case <-bTransitionLockAcquired:
		t.Fatal("transition B acquired ownership while A remained in its persistence barrier")
	}
	select {
	case <-bPersistEntered:
		t.Fatal("transition B reached persistence while A remained in its persistence barrier")
	default:
	}
	select {
	case <-bBuildEntered:
		t.Fatal("transition B reached build while A remained in its persistence barrier")
	default:
	}
	if graph := manager.publication.current(); graph != nil {
		t.Fatalf("transition B published graph=%p while A remained in its persistence barrier", graph)
	}
	releaseA()
	<-aDone
	<-bDone
	if aErr != nil || bErr != nil {
		t.Fatalf("transition errors: A=%v B=%v", aErr, bErr)
	}

	durableMu.Lock()
	if len(persisted) != 2 {
		durableMu.Unlock()
		t.Fatalf("successful transition persistence order=%d, want 2", len(persisted))
	}
	want := persisted[len(persisted)-1]
	gotDurable := durableConfig
	durableMu.Unlock()
	if gotDurable != want {
		t.Fatalf("durable config=%+v, want last persisted %+v", gotDurable, want)
	}
	if manager.config != want {
		t.Fatalf("runtime config=%+v, want last persisted %+v", manager.config, want)
	}
	published := manager.publication.current()
	if published == nil || !manager.Ready() {
		t.Fatalf("published graph=%p ready=%v, want ready graph", published, manager.Ready())
	}
	graphMu.Lock()
	publishedConfig, ok := graphConfigs[published]
	graphMu.Unlock()
	if !ok || publishedConfig != want {
		t.Fatalf("published config=%+v found=%v, want last persisted %+v", publishedConfig, ok, want)
	}

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.graph != nil || manager.publication.current() != nil || manager.Ready() {
		t.Fatalf("shutdown retained graph=%p published=%p ready=%v", manager.graph, manager.publication.current(), manager.Ready())
	}
	if stopCalls.Load() != 2 || shutdownCalls.Load() != 2 {
		t.Fatalf("graph cleanup stop=%d shutdown=%d, want 2/2", stopCalls.Load(), shutdownCalls.Load())
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("abandoned Export Store resource: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedExportRuntimeTerminalizesLifecycleOnlyForSuccessfulExplicitDisable(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
		stop    func(*managedExportRuntime, backupasset.ExportConfig, func() error) error
		want    []string
	}{
		{
			name: "settings disable",
			prepare: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", 32)+".xre"), []byte("ciphertext"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			stop: func(manager *managedExportRuntime, config backupasset.ExportConfig, persist func() error) error {
				config.Enabled = false
				return manager.TransitionSettings(context.Background(), true, config, persist)
			},
			want: []string{"stop", "drain", "shutdown", "stop", "terminalize"},
		},
		{
			name: "global disable",
			prepare: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", 32)+".xre"), []byte("ciphertext"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			stop: func(manager *managedExportRuntime, config backupasset.ExportConfig, persist func() error) error {
				return manager.TransitionSettings(context.Background(), false, config, persist)
			},
			want: []string{"stop", "drain", "shutdown", "stop", "terminalize"},
		},
		{
			name: "process shutdown",
			stop: func(manager *managedExportRuntime, _ backupasset.ExportConfig, _ func() error) error {
				return manager.Shutdown(context.Background())
			},
			want: []string{"stop", "shutdown"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "export")
			events := make([]string, 0, len(test.want))
			persisted := false
			manager := newEnabledManagedExportRuntime(t, root, func(
				_ context.Context,
				_ backupasset.ExportConfig,
				store *assetexport.Store,
			) (*managedExportGraph, error) {
				return &managedExportGraph{
					store:         store,
					stopAccepting: func() { events = append(events, "stop") },
					drain:         func(context.Context) error { events = append(events, "drain"); return nil },
					shutdown:      func(context.Context) error { events = append(events, "shutdown"); return nil },
					terminalize: func(context.Context) error {
						if test.name != "process shutdown" && !persisted {
							return errors.New("Export lifecycle terminalized before settings persistence")
						}
						if err := store.PurgeBatch(nil); err != nil {
							return fmt.Errorf("Export Store unavailable during lifecycle terminalization: %w", err)
						}
						events = append(events, "terminalize")
						return nil
					},
				}, nil
			})
			if err := manager.Startup(context.Background()); err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				test.prepare(t, root)
			}
			if err := test.stop(manager, manager.config, func() error {
				persisted = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events, test.want) {
				t.Fatalf("lifecycle shutdown order=%v want=%v", events, test.want)
			}
			reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
			if err != nil {
				t.Fatalf("terminalized runtime retained Export Store lock: %v", err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedExportRuntimeTransitionSettingsReplacesExistingGraph(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	seenConfigs := make([]backupasset.ExportConfig, 0, 2)
	events := make([]string, 0, 3)
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			seenConfigs = append(seenConfigs, config)
			generation := len(seenConfigs)
			return &managedExportGraph{
				store:         store,
				stopAccepting: func() { events = append(events, fmt.Sprintf("stop-%d", generation)) },
				drain:         func(context.Context) error { events = append(events, fmt.Sprintf("drain-%d", generation)); return nil },
				shutdown: func(context.Context) error {
					events = append(events, fmt.Sprintf("shutdown-%d", generation))
					return nil
				},
				terminalize: func(context.Context) error {
					events = append(events, fmt.Sprintf("terminalize-%d", generation))
					return nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	oldGraph := manager.publication.current()
	if oldGraph == nil {
		t.Fatal("startup did not publish the initial Export graph")
	}
	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.export.worker_concurrency"] = "3"
	targetValues["backup_assets.idempotency_ttl"] = "2h"
	targetValues["backup_assets.idempotency_key_max_bytes"] = "96"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}
	transitioner, ok := any(manager).(interface {
		TransitionSettings(context.Context, bool, backupasset.ExportConfig, func() error) error
	})
	if !ok {
		t.Fatal("managed Export runtime does not provide settings transition")
	}
	if err := transitioner.TransitionSettings(context.Background(), true, target, func() error {
		if graph := manager.publication.current(); graph != nil {
			t.Fatalf("Export graph=%p was published before persistence", graph)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newGraph := manager.publication.current()
	if newGraph == nil || newGraph == oldGraph || !manager.Ready() {
		t.Fatalf("old=%p new=%p ready=%v", oldGraph, newGraph, manager.Ready())
	}
	if len(seenConfigs) != 2 || seenConfigs[0].WorkerConcurrency != 2 || seenConfigs[1].WorkerConcurrency != 3 ||
		seenConfigs[1].IdempotencyTTL != 2*time.Hour || seenConfigs[1].IdempotencyKeyMaxBytes != 96 {
		t.Fatalf("build configs=%+v", seenConfigs)
	}
	if got, want := fmt.Sprint(events), "[stop-1 drain-1 shutdown-1]"; got != want {
		t.Fatalf("transition events=%s want=%s", got, want)
	}
}

func TestManagedExportRuntimeTransitionSettingsDisablesExistingServiceAfterPersistence(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	var builds atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			builds.Add(1)
			return &managedExportGraph{store: store, service: &assetexport.Service{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	request := assetexport.StatusRequest{}
	if _, err := manager.Service().Status(context.Background(), request); !errors.Is(err, assetexport.ErrInvalidSelection) {
		t.Fatalf("published Export service error=%v, want ErrInvalidSelection", err)
	}
	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.export.enabled"] = "false"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}
	transitioner := any(manager).(interface {
		TransitionSettings(context.Context, bool, backupasset.ExportConfig, func() error) error
	})
	persisted := false
	if err := transitioner.TransitionSettings(context.Background(), true, target, func() error {
		persisted = true
		if _, err := manager.Service().Status(context.Background(), request); !errors.Is(err, assetexport.ErrUnavailable) {
			t.Fatalf("Export service error during disable persistence=%v, want ErrUnavailable", err)
		}
		if manager.Ready() {
			t.Fatal("Export runtime remained ready during disable persistence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !persisted || builds.Load() != 1 || manager.Ready() {
		t.Fatalf("persisted=%v builds=%d ready=%v", persisted, builds.Load(), manager.Ready())
	}
	if _, err := manager.Service().Status(context.Background(), request); !errors.Is(err, assetexport.ErrUnavailable) {
		t.Fatalf("Export service error after successful disable=%v, want ErrUnavailable", err)
	}
}

func TestManagedExportRuntimeTransitionSettingsRestoresFacadeAfterDrainFailure(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	var stopCalls atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			return &managedExportGraph{
				store:         store,
				service:       &assetexport.Service{},
				stopAccepting: func() { stopCalls.Add(1) },
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	request := assetexport.StatusRequest{}
	if _, err := manager.Service().Status(context.Background(), request); !errors.Is(err, assetexport.ErrInvalidSelection) {
		t.Fatalf("published Export service error=%v, want ErrInvalidSelection", err)
	}
	_, release, acquired := manager.publication.acquire()
	if !acquired {
		t.Fatal("could not hold the existing Export facade reference")
	}
	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.export.worker_concurrency"] = "3"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}
	transitioner := any(manager).(interface {
		TransitionSettings(context.Context, bool, backupasset.ExportConfig, func() error) error
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	persisted := false
	err = transitioner.TransitionSettings(ctx, true, target, func() error {
		persisted = true
		return nil
	})
	release()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transition error=%v, want context deadline exceeded", err)
	}
	if persisted {
		t.Fatal("Export settings persisted after facade drain failure")
	}
	if _, err := manager.Service().Status(context.Background(), request); !errors.Is(err, assetexport.ErrInvalidSelection) {
		t.Fatalf("existing Export service unavailable after drain failure: %v", err)
	}
	if !manager.Ready() {
		t.Fatal("Export runtime stayed unready after drain failure")
	}
	if stopCalls.Load() != 0 {
		t.Fatalf("Export graph stopped accepting before facade drain completed: calls=%d", stopCalls.Load())
	}
}

func TestManagedExportRuntimeTransitionSettingsRestoresReadyGraphAfterCanceledGraphDrain(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	var builds, drains, shutdowns atomic.Int32
	seenConfigs := make([]backupasset.ExportConfig, 0, 2)
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			generation := builds.Add(1)
			seenConfigs = append(seenConfigs, config)
			return &managedExportGraph{
				store:   store,
				service: &assetexport.Service{},
				drain: func(ctx context.Context) error {
					drains.Add(1)
					if generation == 1 {
						return ctx.Err()
					}
					return nil
				},
				shutdown: func(context.Context) error {
					shutdowns.Add(1)
					return nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	oldGraph := manager.publication.current()
	previousConfig := manager.config
	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.idempotency_ttl"] = "2h"
	targetValues["backup_assets.idempotency_key_max_bytes"] = "96"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	persisted := false
	err = manager.TransitionSettings(ctx, true, target, func() error {
		persisted = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("transition error=%v, want canceled drain error", err)
	}
	if persisted {
		t.Fatal("canceled graph drain persisted prospective idempotency settings")
	}
	newGraph := manager.publication.current()
	if newGraph == nil || newGraph == oldGraph || !manager.Ready() {
		t.Fatalf("canceled graph drain did not restore a fresh ready graph: old=%p new=%p ready=%v", oldGraph, newGraph, manager.Ready())
	}
	if builds.Load() != 2 || drains.Load() != 1 || shutdowns.Load() != 1 {
		t.Fatalf("canceled graph drain builds=%d drains=%d shutdowns=%d", builds.Load(), drains.Load(), shutdowns.Load())
	}
	if len(seenConfigs) != 2 || seenConfigs[1].IdempotencyTTL != previousConfig.IdempotencyTTL ||
		seenConfigs[1].IdempotencyKeyMaxBytes != previousConfig.IdempotencyKeyMaxBytes {
		t.Fatalf("recovery configs=%+v previous=%+v", seenConfigs, previousConfig)
	}
	if _, err := manager.Service().Status(context.Background(), assetexport.StatusRequest{}); !errors.Is(err, assetexport.ErrInvalidSelection) {
		t.Fatalf("restored Export service error=%v, want ErrInvalidSelection", err)
	}
}

func TestManagedExportRuntimeTransitionRecoveryBoundsStalledWorkerShutdownAndReleasesLock(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	delivery := newManagedExportStalledDeliveryFake()
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: &managedExportAttemptsFake{calls: &[]string{}},
				Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: managedExportLifecycleFake{},
				Delivery: delivery, Budget: &managedExportBudgetFake{},
				Cadence: 5 * time.Millisecond, SourceLeaseInterval: time.Hour,
				BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-stalled-transition-recovery",
			})
			if err != nil {
				return nil, err
			}
			return &managedExportGraph{
				store: store, stopAccepting: runner.StopAccepting,
				drain: runner.Drain, run: runner.Run, shutdown: runner.Shutdown,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		manager.Run(runCtx)
		close(runDone)
	}()
	select {
	case <-delivery.entered:
	case <-time.After(time.Second):
		t.Fatal("managed Export worker did not enter the deterministic stalled maintenance call")
	}

	transitionCtx, cancelTransition := context.WithTimeout(context.Background(), 100*time.Millisecond)
	cancelTransition()
	transitionDone := make(chan struct{})
	var transitionErr error
	go func() {
		transitionErr = manager.TransitionSettings(transitionCtx, true, manager.config, func() error {
			return errors.New("settings persistence must not run after canceled worker drain")
		})
		close(transitionDone)
	}()
	t.Cleanup(func() {
		delivery.Release()
		cancelRun()
		select {
		case <-transitionDone:
		case <-time.After(time.Second):
			t.Error("stalled Export transition did not return after releasing the worker")
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		shutdownDone := make(chan error, 1)
		go func() { shutdownDone <- manager.Shutdown(shutdownCtx) }()
		select {
		case err := <-shutdownDone:
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("cleanup stalled Export runtime: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("cleanup stalled Export shutdown did not return")
		}
		select {
		case <-runDone:
		case <-time.After(time.Second):
			t.Error("stalled Export runtime Run did not return during cleanup")
		}
	})

	select {
	case <-transitionDone:
	case <-time.After(time.Second):
		t.Fatal("stalled Export recovery retained the shutdown lock after the caller deadline")
	}
	if !errors.Is(transitionErr, context.Canceled) || !errors.Is(transitionErr, context.DeadlineExceeded) {
		t.Fatalf("stalled Export transition error=%v, want canceled transition and bounded recovery deadline", transitionErr)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelShutdown()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(shutdownCtx) }()
	select {
	case err := <-shutdownDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("subsequent shutdown error=%v, want bounded stalled worker deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("subsequent Export shutdown did not acquire the recovery-released shutdown lock")
	}
}

func TestManagedExportRuntimeTransitionSettingsBoundsBackgroundStalledDrainAndRetainsOldGraph(t *testing.T) {
	const transitionTimeout = 75 * time.Millisecond
	const transitionReturnLimit = 4 * transitionTimeout

	db, ring := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	delivery := newManagedExportStalledDeliveryFake()
	var builds, persists atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot:      func(context.Context, string) error { return nil },
		TransitionTimeout: transitionTimeout,
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			builds.Add(1)
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: &managedExportAttemptsFake{calls: &[]string{}},
				Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: managedExportLifecycleFake{},
				Delivery: delivery, Budget: &managedExportBudgetFake{},
				Cadence: 5 * time.Millisecond, SourceLeaseInterval: time.Hour,
				BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-background-stalled-transition",
			})
			if err != nil {
				return nil, err
			}
			return &managedExportGraph{
				store: store, stopAccepting: runner.StopAccepting,
				drain: runner.Drain, run: runner.Run, shutdown: runner.Shutdown,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		manager.Run(runCtx)
		close(runDone)
	}()
	select {
	case <-delivery.entered:
	case <-time.After(time.Second):
		t.Fatal("managed Export worker did not enter the deterministic stalled maintenance call")
	}

	t.Cleanup(func() {
		delivery.Release()
		cancelRun()
		select {
		case <-runDone:
		case <-time.After(time.Second):
			t.Error("stalled Export runtime Run did not return during cleanup")
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		if err := manager.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("cleanup stalled Export runtime: %v", err)
		}
	})

	oldGraph := manager.graph
	if oldGraph == nil {
		t.Fatal("startup did not retain the initial Export graph")
	}
	target := manager.config
	target.WorkerConcurrency = 3
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- manager.TransitionSettings(context.Background(), true, target, func() error {
			persists.Add(1)
			return nil
		})
	}()

	// A Background caller still needs a runtime-owned upper bound for a stalled drain.
	select {
	case err := <-transitionDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("background stalled transition error=%v, want runtime deadline exceeded", err)
		}
	case <-time.After(transitionReturnLimit):
		t.Fatal("background stalled Export transition did not return within its runtime-owned bound")
	}
	if persists.Load() != 0 {
		t.Fatalf("background stalled transition persisted target settings %d time(s)", persists.Load())
	}
	manager.mu.Lock()
	retainedGraph := manager.graph
	graphShutdown := manager.graphShutdown
	manager.mu.Unlock()
	if retainedGraph != oldGraph || graphShutdown || manager.publication.current() != nil || manager.Ready() {
		t.Fatalf("stalled transition changed old graph ownership: graph=%p old=%p shutdown=%v published=%p ready=%v",
			retainedGraph, oldGraph, graphShutdown, manager.publication.current(), manager.Ready())
	}
	if reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: root}); openErr == nil {
		_ = reopened.Close()
		t.Fatal("stalled transition closed the old Export Store before the old graph stopped")
	}

	delivery.Release()
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("released stalled Export runtime Run did not return")
	}
	if err := manager.TransitionSettings(context.Background(), true, target, func() error {
		persists.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("later transition did not acquire released transition ownership: %v", err)
	}
	if persists.Load() != 1 || builds.Load() != 2 || manager.graph == oldGraph || manager.publication.current() == nil || !manager.Ready() {
		t.Fatalf("later transition state persists=%d builds=%d graph=%p old=%p published=%p ready=%v",
			persists.Load(), builds.Load(), manager.graph, oldGraph, manager.publication.current(), manager.Ready())
	}
}

func TestManagedExportRuntimeTransitionSettingsPreservesDeadlineForPostJoinWorkerCleanup(t *testing.T) {
	const transitionTimeout = 75 * time.Millisecond
	const transitionReturnLimit = 4 * transitionTimeout

	db, ring := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
		&model.BackupAssetExportSourceLease{}, &model.RecoveryPointLease{},
	); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	delivery := newManagedExportTransitionCleanupDeliveryFake()
	var builds, persists atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot:      func(context.Context, string) error { return nil },
		TransitionTimeout: transitionTimeout,
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			if builds.Add(1) != 1 {
				return &managedExportGraph{store: store}, nil
			}
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: &managedExportAttemptsFake{calls: &[]string{}},
				Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: managedExportLifecycleFake{},
				Delivery: delivery, Budget: &managedExportBudgetFake{},
				Cadence: time.Hour, BatchSize: 1, WorkerConcurrency: 1,
				WorkerOwner: "export-worker-post-join-transition-deadline",
			})
			if err != nil {
				return nil, err
			}
			runCtx, cancelRun := context.WithCancel(context.Background())
			joined := make(chan struct{})
			go func() {
				runner.Run(runCtx)
				close(joined)
			}()
			cancelRun()
			select {
			case <-joined:
			case <-time.After(time.Second):
				return nil, errors.New("managed Export worker did not join before transition cleanup")
			}
			return &managedExportGraph{
				store: store, runner: runner, stopAccepting: runner.StopAccepting,
				drain: runner.Drain, shutdown: runner.Shutdown,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}

	transitionDone := make(chan error, 1)
	transitionFinished := false
	t.Cleanup(func() {
		delivery.Release()
		if !transitionFinished {
			select {
			case <-transitionDone:
			case <-time.After(time.Second):
				t.Error("post-join cleanup transition did not return during cleanup")
			}
		}
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Errorf("cleanup post-join transition runtime: %v", err)
		}
	})

	oldGraph := manager.graph
	if oldGraph == nil {
		t.Fatal("startup did not retain the initial Export graph")
	}
	target := manager.config
	target.WorkerConcurrency = 3
	go func() {
		transitionDone <- manager.TransitionSettings(context.Background(), true, target, func() error {
			persists.Add(1)
			return nil
		})
	}()
	select {
	case <-delivery.entered:
	case err := <-transitionDone:
		transitionFinished = true
		t.Fatalf("post-join cleanup transition ended before reconciliation blocked: %v", err)
	case <-time.After(time.Second):
		t.Fatal("post-join cleanup transition did not enter reconciliation")
	}

	select {
	case err := <-transitionDone:
		transitionFinished = true
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("post-join cleanup transition error=%v, want transition deadline exceeded", err)
		}
	case <-time.After(transitionReturnLimit):
		t.Fatal("post-join cleanup transition did not return within the transition deadline")
	}
	if persists.Load() != 0 {
		t.Fatalf("post-join cleanup transition persisted target settings %d time(s)", persists.Load())
	}
	manager.mu.Lock()
	retainedGraph := manager.graph
	graphShutdown := manager.graphShutdown
	manager.mu.Unlock()
	if retainedGraph != oldGraph || graphShutdown || manager.publication.current() != nil || manager.Ready() {
		t.Fatalf("post-join cleanup transition changed old graph ownership: graph=%p old=%p shutdown=%v published=%p ready=%v",
			retainedGraph, oldGraph, graphShutdown, manager.publication.current(), manager.Ready())
	}
	if reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: root}); openErr == nil {
		_ = reopened.Close()
		t.Fatal("post-join cleanup transition closed the old Export Store before the old graph stopped")
	}

	delivery.Release()
	if err := manager.TransitionSettings(context.Background(), true, target, func() error {
		persists.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("later post-join cleanup transition did not acquire released transition ownership: %v", err)
	}
	if persists.Load() != 1 || builds.Load() != 2 || manager.graph == oldGraph ||
		manager.publication.current() == nil || !manager.Ready() {
		t.Fatalf("later post-join cleanup state persists=%d builds=%d graph=%p old=%p published=%p ready=%v",
			persists.Load(), builds.Load(), manager.graph, oldGraph, manager.publication.current(), manager.Ready())
	}
}

func TestManagedExportRuntimeTransitionSettingsBoundsBackgroundShutdownAndRetainsOldGraph(t *testing.T) {
	const transitionTimeout = 75 * time.Millisecond
	const transitionReturnLimit = time.Second

	type shutdownContextObservation struct {
		deadline    time.Time
		hasDeadline bool
		err         error
	}

	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	releaseShutdown := make(chan struct{})
	var releaseShutdownOnce sync.Once
	release := func() { releaseShutdownOnce.Do(func() { close(releaseShutdown) }) }
	shutdownContexts := make(chan shutdownContextObservation, 8)
	shutdownEntered := make(chan struct{})
	var shutdownEnteredOnce sync.Once
	var builds, drains, persists atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot:      func(context.Context, string) error { return nil },
		TransitionTimeout: transitionTimeout,
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			builds.Add(1)
			return &managedExportGraph{
				store: store,
				drain: func(context.Context) error {
					drains.Add(1)
					return nil
				},
				shutdown: func(ctx context.Context) error {
					deadline, hasDeadline := ctx.Deadline()
					shutdownContexts <- shutdownContextObservation{
						deadline: deadline, hasDeadline: hasDeadline, err: ctx.Err(),
					}
					shutdownEnteredOnce.Do(func() { close(shutdownEntered) })
					select {
					case <-releaseShutdown:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	transitionDone := make(chan error, 1)
	transitionFinished := make(chan struct{})
	t.Cleanup(func() {
		release()
		select {
		case <-transitionFinished:
		case <-time.After(time.Second):
			t.Error("stalled Export shutdown transition did not return during cleanup")
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		if err := manager.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("cleanup stalled Export shutdown runtime: %v", err)
		}
	})

	oldGraph := manager.graph
	if oldGraph == nil {
		t.Fatal("startup did not retain the initial Export graph")
	}
	target := manager.config
	target.WorkerConcurrency = 3
	go func() {
		defer close(transitionFinished)
		transitionDone <- manager.TransitionSettings(context.Background(), true, target, func() error {
			persists.Add(1)
			return errors.New("settings persistence must not run after stalled Export shutdown")
		})
	}()
	select {
	case <-shutdownEntered:
	case <-time.After(time.Second):
		t.Fatal("Export transition did not enter the deterministic stalled shutdown")
	}
	select {
	case err := <-transitionDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("background stalled shutdown transition error=%v, want runtime deadline exceeded", err)
		}
	case <-time.After(transitionReturnLimit):
		t.Fatal("background stalled Export shutdown transition did not return within its runtime-owned bound")
	}
	if persists.Load() != 0 || drains.Load() != 1 {
		t.Fatalf("stalled shutdown persisted=%d drains=%d, want 0/1", persists.Load(), drains.Load())
	}
	firstShutdown := <-shutdownContexts
	recoveryShutdown := <-shutdownContexts
	if !firstShutdown.hasDeadline || !recoveryShutdown.hasDeadline ||
		!recoveryShutdown.deadline.Equal(firstShutdown.deadline) ||
		!errors.Is(recoveryShutdown.err, context.DeadlineExceeded) {
		t.Fatalf("shutdown contexts initial=%+v recovery=%+v", firstShutdown, recoveryShutdown)
	}
	manager.mu.Lock()
	retainedGraph := manager.graph
	graphShutdown := manager.graphShutdown
	manager.mu.Unlock()
	if retainedGraph != oldGraph || graphShutdown || manager.publication.current() != nil || manager.Ready() {
		t.Fatalf("stalled shutdown changed old graph ownership: graph=%p old=%p shutdown=%v published=%p ready=%v",
			retainedGraph, oldGraph, graphShutdown, manager.publication.current(), manager.Ready())
	}
	if reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: root}); openErr == nil {
		_ = reopened.Close()
		t.Fatal("stalled shutdown transition closed the old Export Store before the old graph stopped")
	}

	release()
	if err := manager.TransitionSettings(context.Background(), true, target, func() error {
		persists.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("later transition did not acquire released transition ownership: %v", err)
	}
	if persists.Load() != 1 || drains.Load() != 2 || builds.Load() != 2 || manager.graph == oldGraph ||
		manager.publication.current() == nil || !manager.Ready() {
		t.Fatalf("later shutdown transition state persists=%d drains=%d builds=%d graph=%p old=%p published=%p ready=%v",
			persists.Load(), drains.Load(), builds.Load(), manager.graph, oldGraph, manager.publication.current(), manager.Ready())
	}
}

func TestManagedExportRuntimeTransitionSettingsPersistenceRecoveryUsesTransitionDeadline(t *testing.T) {
	const transitionTimeout = 75 * time.Millisecond

	type contextObservation struct {
		deadline    time.Time
		hasDeadline bool
		err         error
	}

	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	drainContexts := make(chan contextObservation, 1)
	recoveryContexts := make(chan contextObservation, 1)
	drainDeadlineReached := make(chan struct{})
	var drainDeadlineOnce sync.Once
	var validateCalls, persists atomic.Int32
	persistErr := errors.New("FAKE_EXPORT_SETTINGS_PERSIST_FAILURE_AFTER_TRANSITION_DEADLINE")
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(ctx context.Context, _ string) error {
			if validateCalls.Add(1) == 1 {
				return nil
			}
			deadline, hasDeadline := ctx.Deadline()
			recoveryContexts <- contextObservation{deadline: deadline, hasDeadline: hasDeadline, err: ctx.Err()}
			if err := ctx.Err(); err != nil {
				return err
			}
			return errors.New("persistence recovery received a fresh runtime deadline")
		},
		TransitionTimeout: transitionTimeout,
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			return &managedExportGraph{
				store: store,
				drain: func(ctx context.Context) error {
					deadline, hasDeadline := ctx.Deadline()
					drainContexts <- contextObservation{deadline: deadline, hasDeadline: hasDeadline, err: ctx.Err()}
					<-ctx.Done()
					drainDeadlineOnce.Do(func() { close(drainDeadlineReached) })
					return nil
				},
				shutdown: func(context.Context) error { return nil },
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		if err := manager.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("cleanup persistence recovery runtime: %v", err)
		}
	})

	err = manager.TransitionSettings(context.Background(), true, manager.config, func() error {
		select {
		case <-drainDeadlineReached:
		case <-time.After(time.Second):
			return errors.New("transition drain did not reach its runtime deadline before persistence")
		}
		persists.Add(1)
		return persistErr
	})
	if !errors.Is(err, persistErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persistence recovery error=%v, want persist failure and expired transition deadline", err)
	}
	if persists.Load() != 1 {
		t.Fatalf("persistence calls=%d, want 1", persists.Load())
	}
	drainContext := <-drainContexts
	recoveryContext := <-recoveryContexts
	if !drainContext.hasDeadline || !recoveryContext.hasDeadline ||
		!recoveryContext.deadline.Equal(drainContext.deadline) ||
		!errors.Is(recoveryContext.err, context.DeadlineExceeded) {
		t.Fatalf("persistence recovery contexts drain=%+v recovery=%+v", drainContext, recoveryContext)
	}
	if manager.graph != nil || manager.publication.current() != nil || manager.Ready() {
		t.Fatalf("expired persistence recovery published a fresh graph: graph=%p published=%p ready=%v",
			manager.graph, manager.publication.current(), manager.Ready())
	}
}

func TestManagedExportRuntimeTransitionSettingsRestoresReadyGraphAfterOrdinaryGraphFailure(t *testing.T) {
	for _, test := range []struct {
		name          string
		failureStage  string
		wantDrains    int32
		wantShutdowns int32
	}{
		{name: "drain", failureStage: "drain", wantDrains: 1, wantShutdowns: 1},
		{name: "shutdown", failureStage: "shutdown", wantDrains: 1, wantShutdowns: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, ring := exportRuntimeKeyringFixture(t)
			root := filepath.Join(t.TempDir(), "export")
			values := runtimeFoundationSettings(true)
			values["backup_assets.export.enabled"] = "true"
			values["backup_assets.export.root"] = root
			transitionErr := errors.New("FAKE_ORDINARY_EXPORT_GRAPH_" + strings.ToUpper(test.failureStage) + "_FAILURE_FOR_TEST_ONLY")
			var builds, drains, shutdowns, persistCalls atomic.Int32
			var firstShutdownFailed atomic.Bool
			firstShutdownFailed.Store(true)
			seenConfigs := make([]backupasset.ExportConfig, 0, 2)
			manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
				DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
				ValidateRoot: func(context.Context, string) error { return nil },
				Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
					generation := builds.Add(1)
					seenConfigs = append(seenConfigs, config)
					return &managedExportGraph{
						store:   store,
						service: &assetexport.Service{},
						drain: func(context.Context) error {
							drains.Add(1)
							if generation == 1 && test.failureStage == "drain" {
								return transitionErr
							}
							return nil
						},
						shutdown: func(context.Context) error {
							shutdowns.Add(1)
							if generation == 1 && test.failureStage == "shutdown" && firstShutdownFailed.CompareAndSwap(true, false) {
								return transitionErr
							}
							return nil
						},
					}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Startup(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

			oldGraph := manager.publication.current()
			previousConfig := manager.config
			target := previousConfig
			target.WorkerConcurrency = 3
			err = manager.TransitionSettings(context.Background(), true, target, func() error {
				persistCalls.Add(1)
				return nil
			})
			if !errors.Is(err, transitionErr) {
				t.Fatalf("transition error=%v, want ordinary %s failure", err, test.failureStage)
			}
			if persistCalls.Load() != 0 {
				t.Fatalf("ordinary %s failure persisted target settings %d time(s)", test.failureStage, persistCalls.Load())
			}
			newGraph := manager.publication.current()
			if newGraph == nil || newGraph == oldGraph || !manager.Ready() {
				t.Fatalf("ordinary %s failure did not restore a fresh ready graph: old=%p new=%p ready=%v",
					test.failureStage, oldGraph, newGraph, manager.Ready())
			}
			if builds.Load() != 2 || drains.Load() != test.wantDrains || shutdowns.Load() != test.wantShutdowns {
				t.Fatalf("ordinary %s failure builds=%d drains=%d shutdowns=%d, want 2/%d/%d",
					test.failureStage, builds.Load(), drains.Load(), shutdowns.Load(), test.wantDrains, test.wantShutdowns)
			}
			if len(seenConfigs) != 2 || seenConfigs[1].WorkerConcurrency != previousConfig.WorkerConcurrency {
				t.Fatalf("ordinary %s recovery configs=%+v previous=%+v", test.failureStage, seenConfigs, previousConfig)
			}
			if _, err := manager.Service().Status(context.Background(), assetexport.StatusRequest{}); !errors.Is(err, assetexport.ErrInvalidSelection) {
				t.Fatalf("ordinary %s recovery Export service error=%v, want ErrInvalidSelection", test.failureStage, err)
			}
		})
	}
}

func TestManagedExportRuntimeTransitionSettingsRestoresOldGraphAfterPersistFailure(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	seenConfigs := make([]backupasset.ExportConfig, 0, 2)
	var terminalizations atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			seenConfigs = append(seenConfigs, config)
			return &managedExportGraph{
				store: store,
				terminalize: func(context.Context) error {
					terminalizations.Add(1)
					return nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	oldGraph := manager.publication.current()
	if oldGraph == nil {
		t.Fatal("startup did not publish the initial Export graph")
	}
	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.export.worker_concurrency"] = "3"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}
	transitioner := any(manager).(interface {
		TransitionSettings(context.Context, bool, backupasset.ExportConfig, func() error) error
	})
	persistErr := errors.New("FAKE_EXPORT_SETTINGS_PERSIST_FAILURE_FOR_TEST_ONLY")
	err = transitioner.TransitionSettings(context.Background(), true, target, func() error { return persistErr })
	if !errors.Is(err, persistErr) {
		t.Fatalf("transition error=%v, want persistence failure", err)
	}
	newGraph := manager.publication.current()
	if newGraph == nil || newGraph == oldGraph || !manager.Ready() {
		t.Fatalf("old=%p new=%p ready=%v", oldGraph, newGraph, manager.Ready())
	}
	if len(seenConfigs) != 2 || seenConfigs[0].WorkerConcurrency != 2 || seenConfigs[1].WorkerConcurrency != 2 {
		t.Fatalf("restore build configs=%+v", seenConfigs)
	}
	if terminalizations.Load() != 0 {
		t.Fatalf("failed settings persistence terminalized durable Export work: calls=%d", terminalizations.Load())
	}
}

func TestManagedExportRuntimeTransitionSettingsRestoresReadyGraphAfterFailedDisableWithoutTerminalizing(t *testing.T) {
	for _, test := range []struct {
		name          string
		globalEnabled bool
		configure     func(backupasset.ExportConfig) backupasset.ExportConfig
	}{
		{
			name:          "export disable",
			globalEnabled: true,
			configure: func(config backupasset.ExportConfig) backupasset.ExportConfig {
				config.Enabled = false
				return config
			},
		},
		{
			name:          "global disable",
			globalEnabled: false,
			configure:     func(config backupasset.ExportConfig) backupasset.ExportConfig { return config },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, ring := exportRuntimeKeyringFixture(t)
			root := filepath.Join(t.TempDir(), "export")
			values := runtimeFoundationSettings(true)
			values["backup_assets.export.enabled"] = "true"
			values["backup_assets.export.root"] = root
			var terminalizations atomic.Int32
			manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
				DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
				ValidateRoot: func(context.Context, string) error { return nil },
				Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
					return &managedExportGraph{
						store: store,
						terminalize: func(context.Context) error {
							terminalizations.Add(1)
							return nil
						},
					}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Startup(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
			targetValues := make(runtimeSettings, len(values))
			for key, value := range values {
				targetValues[key] = value
			}
			target, err := backupasset.ExportConfigFromValues(targetValues)
			if err != nil {
				t.Fatal(err)
			}
			persistErr := errors.New("FAKE_DISABLED_EXPORT_SETTINGS_PERSIST_FAILURE_FOR_TEST_ONLY")
			err = manager.TransitionSettings(context.Background(), test.globalEnabled, test.configure(target), func() error {
				return persistErr
			})
			if !errors.Is(err, persistErr) {
				t.Fatalf("transition error=%v, want persistence failure", err)
			}
			if terminalizations.Load() != 0 {
				t.Fatalf("failed disable persistence terminalized durable Export work: calls=%d", terminalizations.Load())
			}
			if manager.publication.current() == nil || !manager.Ready() {
				t.Fatalf("failed disable persistence did not restore a ready Export graph: graph=%p ready=%v",
					manager.publication.current(), manager.Ready())
			}
		})
	}
}

func TestManagedExportRuntimeTransitionSettingsRestoresReadyGraphAfterCanceledFailedDisable(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	var terminalizations atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(ctx context.Context, _ string) error {
			return ctx.Err()
		},
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			return &managedExportGraph{
				store: store,
				terminalize: func(context.Context) error {
					terminalizations.Add(1)
					return nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	target := manager.config
	target.Enabled = false
	transitionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	persistErr := errors.New("FAKE_CANCELED_DISABLED_EXPORT_SETTINGS_PERSIST_FAILURE_FOR_TEST_ONLY")
	err = manager.TransitionSettings(transitionCtx, true, target, func() error {
		cancel()
		return persistErr
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("transition error=%v, want persistence failure", err)
	}
	if terminalizations.Load() != 0 {
		t.Fatalf("canceled failed disable terminalized durable Export work: calls=%d", terminalizations.Load())
	}
	if manager.publication.current() == nil || !manager.Ready() {
		t.Fatalf("canceled failed disable did not restore a ready Export graph: graph=%p ready=%v",
			manager.publication.current(), manager.Ready())
	}
}

func TestManagedExportRuntimePinsStartupRootAcrossEnabledSettingsTransition(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	startupRoot := filepath.Join(t.TempDir(), "startup-export")
	dynamicRoot := filepath.Join(t.TempDir(), "dynamic-export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = startupRoot
	seenRoots := make([]string, 0, 2)
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			seenRoots = append(seenRoots, config.Root)
			return &managedExportGraph{store: store}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.export.root"] = dynamicRoot
	targetValues["backup_assets.export.worker_concurrency"] = "3"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.TransitionSettings(context.Background(), true, target, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, want := seenRoots, []string{startupRoot, startupRoot}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Export graph roots=%v want=%v", got, want)
	}
}

func TestManagedExportRuntimePrepareSchemaDownPinsStartupRootAfterGraphStops(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	startupRoot := filepath.Join(t.TempDir(), "startup-export")
	dynamicRoot := filepath.Join(t.TempDir(), "dynamic-export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = startupRoot
	validatedRoots := make([]string, 0, 2)
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(_ context.Context, root string) error {
			validatedRoots = append(validatedRoots, root)
			return nil
		},
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			return &managedExportGraph{store: store}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dynamicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	values["backup_assets.export.root"] = dynamicRoot
	callbacks := 0
	if err := manager.PrepareSchemaDown(context.Background(), func() error {
		callbacks++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if callbacks != 1 {
		t.Fatalf("schema-down callbacks=%d, want one", callbacks)
	}
	if got := validatedRoots[len(validatedRoots)-1]; got != startupRoot {
		t.Fatalf("schema-down validated root=%q want startup root %q", got, startupRoot)
	}
}

func TestManagedExportRuntimePrepareSchemaDownRejectsMissingPinnedRoot(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "missing-export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	values["backup_assets.export.root"] = root
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(context.Context, backupasset.ExportConfig, *assetexport.Store) (*managedExportGraph, error) {
			return nil, errors.New("disabled missing-root runtime must not build a graph")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	callbacks := 0
	err = manager.PrepareSchemaDown(context.Background(), func() error {
		callbacks++
		return nil
	})
	if !errors.Is(err, assetexport.ErrInvalidStore) {
		t.Fatalf("missing pinned root schema-down error=%v, want ErrInvalidStore", err)
	}
	if callbacks != 0 {
		t.Fatalf("missing pinned root invoked schema-down callback %d times", callbacks)
	}
}

func TestManagedExportRuntimeShutdownRetainsDurableReadyArtifactForRestartReconciliation(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	config, err := backupasset.ExportConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeExportDurableReadyFixture(t, db, ring, root, config)

	var firstTerminalizations atomic.Int32
	firstLeases := &runtimeExportDurableLeaseSpy{LeaseService: fixture.leases}
	first := newRuntimeExportDurableManager(t, db, ring, values, fixture, firstLeases, &firstTerminalizations)
	if err := first.Startup(context.Background()); err != nil {
		t.Fatalf("first runtime startup: %v", err)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatalf("first runtime shutdown: %v", err)
	}
	if firstTerminalizations.Load() != 0 {
		t.Fatalf("normal runtime shutdown terminalized durable ready Export work: calls=%d", firstTerminalizations.Load())
	}
	assertRuntimeExportDurableReadyRows(t, db, fixture)
	assertRuntimeExportArtifactReadable(t, root, fixture.locator)

	var restartTerminalizations atomic.Int32
	restartLeases := &runtimeExportDurableLeaseSpy{LeaseService: fixture.leases}
	restarted := newRuntimeExportDurableManager(t, db, ring, values, fixture, restartLeases, &restartTerminalizations)
	if err := restarted.Startup(context.Background()); err != nil {
		t.Fatalf("restarted runtime startup: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Shutdown(context.Background()) })
	if restartLeases.renewals.Load() == 0 {
		t.Fatal("restarted runtime did not renew the durable ready source lease")
	}
	if restarted.graph == nil || restarted.graph.worker == nil {
		t.Fatal("restarted runtime did not retain its durable Export worker")
	}
	result, err := restarted.graph.worker.ReconcileJob(
		context.Background(), assetexport.PersistentReconcileRequest{JobID: fixture.jobID},
	)
	if err != nil || result.ReadyIntegrity == nil {
		t.Fatalf("restarted durable ready reconciliation=%+v err=%v", result, err)
	}
	if restartTerminalizations.Load() != 0 {
		t.Fatalf("restart reconciliation terminalized durable ready Export work: calls=%d", restartTerminalizations.Load())
	}
	assertRuntimeExportDurableReadyRows(t, db, fixture)
	sealed, err := restarted.graph.store.OpenSealed(fixture.locator)
	if err != nil {
		t.Fatalf("restarted runtime artifact unavailable: %v", err)
	}
	if err := sealed.Close(); err != nil {
		t.Fatalf("close restarted sealed artifact: %v", err)
	}
}

func TestManagedExportRuntimeTransitionSettingsRetriesStoreCloseWithoutRepeatingShutdown(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	var stops, drains, shutdowns, persists atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			return &managedExportGraph{
				store:         store,
				stopAccepting: func() { stops.Add(1) },
				drain:         func(context.Context) error { drains.Add(1); return nil },
				shutdown:      func(context.Context) error { shutdowns.Add(1); return nil },
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	oldGraph := manager.graph
	if oldGraph == nil {
		t.Fatal("startup did not retain the initial Export graph")
	}

	targetValues := make(runtimeSettings, len(values))
	for key, value := range values {
		targetValues[key] = value
	}
	targetValues["backup_assets.export.enabled"] = "false"
	target, err := backupasset.ExportConfigFromValues(targetValues)
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := filepath.Join(t.TempDir(), "moved-export")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatalf("invalidate Export Store root: %v", err)
	}
	var persistBeforeCleanup bool
	persist := func() error {
		persists.Add(1)
		if manager.publication.current() != nil || manager.Ready() || manager.graph != nil {
			persistBeforeCleanup = true
		}
		return nil
	}

	err = manager.TransitionSettings(context.Background(), true, target, persist)
	if !errors.Is(err, assetexport.ErrInvalidStore) {
		t.Fatalf("first transition error=%v, want Store.Close root-identity failure", err)
	}
	if shutdowns.Load() != 1 || stops.Load() != 1 || drains.Load() != 1 || persists.Load() != 0 {
		t.Fatalf("first transition stops=%d drains=%d shutdowns=%d persists=%d", stops.Load(), drains.Load(), shutdowns.Load(), persists.Load())
	}
	if manager.graph != oldGraph {
		t.Fatal("failed Store close discarded the shutdown-complete Export graph owner")
	}
	if manager.Ready() || manager.publication.current() != nil {
		t.Fatal("failed Store close restored a shutdown-complete Export graph")
	}
	if _, release, acquired := manager.publication.acquire(); acquired {
		release()
		t.Fatal("facade acquired a shutdown-complete Export graph after Store.Close failure")
	}

	// A stale publication must not make the shutdown-complete retry wait or restore this graph.
	manager.publication.publish(oldGraph)
	_, releaseStaleFacade, acquired := manager.publication.acquire()
	if !acquired {
		t.Fatal("could not acquire deterministic stale Export facade reference")
	}
	retryContext, cancelRetry := context.WithCancel(context.Background())
	cancelRetry()
	err = manager.TransitionSettings(retryContext, true, target, persist)
	releaseStaleFacade()
	if persistBeforeCleanup {
		t.Fatal("persist ran before the Export graph cleanup completed")
	}
	if err != nil {
		t.Fatalf("retry transition after Store.Close failure: %v", err)
	}
	if shutdowns.Load() != 1 || stops.Load() != 1 || drains.Load() != 1 || persists.Load() != 1 {
		t.Fatalf("retry transition stops=%d drains=%d shutdowns=%d persists=%d", stops.Load(), drains.Load(), shutdowns.Load(), persists.Load())
	}
	if manager.graph != nil || manager.Ready() || manager.publication.current() != nil {
		t.Fatal("disabling retry retained or republished the shutdown-complete Export graph")
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: movedRoot})
	if err != nil {
		t.Fatalf("retry transition retained Export Store lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestOptionalTypedDeliveryBranchSupportsReplaceAndUnpublish(t *testing.T) {
	branch := newOptionalTypedDeliveryBranch()
	first := &fakeTypedDeliveryBranch{}
	second := &fakeTypedDeliveryBranch{}
	if err := branch.Install(first); err != nil {
		t.Fatal(err)
	}
	if err := branch.Install(second); err != nil {
		t.Fatalf("replace delivery branch: %v", err)
	}
	if got := branch.current(); got != second {
		t.Fatalf("current branch=%p, want replacement=%p", got, second)
	}
	branch.Unpublish()
	if got := branch.current(); got != nil {
		t.Fatalf("unpublished branch=%p, want nil", got)
	}
}

func TestManagedExportRuntimeRunSurvivesEnableAfterBootDisabled(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "false"
	root := filepath.Join(t.TempDir(), "export")
	values["backup_assets.export.root"] = root
	runStarted := make(chan struct{})
	var builds atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			builds.Add(1)
			return &managedExportGraph{store: store, run: func(ctx context.Context) {
				close(runStarted)
				<-ctx.Done()
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Export manager Run exited before a graph was published")
	case <-time.After(25 * time.Millisecond):
	}
	values["backup_assets.export.enabled"] = "true"
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runStarted:
	case <-time.After(time.Second):
		t.Fatal("Export manager Run did not observe the graph enabled after boot")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Export manager Run did not stop with its context")
	}
	if builds.Load() != 1 {
		t.Fatalf("builds=%d, want one", builds.Load())
	}
}

func TestManagedExportRuntimeStartsOnDemandAndProvesEmptyRootForSchemaDown(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	var builds, stops, callbacks atomic.Int32
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(_ context.Context, candidate string) error {
			if candidate != root {
				return errors.New("wrong root")
			}
			return nil
		},
		Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			if config.Root != root || store == nil {
				return nil, errors.New("invalid graph input")
			}
			builds.Add(1)
			return &managedExportGraph{store: store, stopAccepting: func() { stops.Add(1) }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !manager.Ready() || builds.Load() != 1 {
		t.Fatalf("ready=%v builds=%d", manager.Ready(), builds.Load())
	}
	if _, err := ring.Active(context.Background(), backupasset.KeyDomainExportStore); err != nil {
		t.Fatalf("Export Store key unavailable: %v", err)
	}
	if err := manager.PrepareSchemaDown(context.Background(), func() error {
		callbacks.Add(1)
		return nil
	}); err != nil || callbacks.Load() != 1 || stops.Load() != 1 || manager.Ready() {
		t.Fatalf("schema down error=%v callbacks=%d stops=%d ready=%v", err, callbacks.Load(), stops.Load(), manager.Ready())
	}
	if err := os.WriteFile(filepath.Join(root, "orphan.xre"), []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.PrepareSchemaDown(context.Background(), func() error {
		callbacks.Add(1)
		return nil
	}); !errors.Is(err, assetexport.ErrInvalidStore) || callbacks.Load() != 1 {
		t.Fatalf("orphan schema down error=%v callbacks=%d", err, callbacks.Load())
	}
	if err := os.Remove(filepath.Join(root, "orphan.xre")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("shutdown retained root lock: %v", err)
	}
	_ = reopened.Close()
}

func TestManagedExportRuntimeShutdownRetainsGraphUntilFacadeReferenceDrains(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	var shutdowns atomic.Int32
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store: store,
			shutdown: func(context.Context) error {
				shutdowns.Add(1)
				return nil
			},
		}, nil
	})
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}

	// Facades hold this reference while dispatching a request to the published graph.
	_, releaseFacadeReference, acquired := manager.publication.acquire()
	if !acquired {
		t.Fatal("acquire published Export facade reference")
	}
	t.Cleanup(func() {
		releaseFacadeReference()
		_ = manager.Shutdown(context.Background())
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := manager.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with held facade reference error=%v, want deadline exceeded", err)
	}
	if manager.graph == nil {
		t.Fatal("Shutdown discarded the graph before the facade reference drained")
	}
	if shutdowns.Load() != 0 {
		t.Fatalf("graph shutdown calls=%d, want none before facade drain", shutdowns.Load())
	}
	if reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root}); err == nil {
		_ = reopened.Close()
		t.Fatal("Shutdown released the Export Store lock before facade drain")
	}

	releaseFacadeReference()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown after facade drain: %v", err)
	}
	if shutdowns.Load() != 1 {
		t.Fatalf("graph shutdown calls=%d, want one", shutdowns.Load())
	}
	if manager.graph != nil {
		t.Fatal("Shutdown retained the graph after all shutdown phases succeeded")
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("retry Shutdown retained Export Store lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeShutdownUnpublishesNewFacadeCallsBeforeHeldReferenceDrains(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	now := time.Now().UTC().Truncate(time.Second)
	asset := content.AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		CatalogGenerationID: strings.Repeat("3", 32), Provider: backupasset.ProviderRestic,
		ProviderCapabilityRevision: 1, SourceFingerprint: "source-fingerprint-v1",
		EntryFingerprint: "entry-fingerprint-v1", FingerprintStrength: "strong", Size: 3, MediaType: "application/zip",
	}
	archiveMember, err := processing.NewArchiveMemberService(processing.ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &runtimeArchiveMemberMaintenanceCoordinator{},
		Authorize: processingRuntimeAssetAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (processing.ArchiveMemberIndexBinding, error) {
			return processing.ArchiveMemberIndexBinding{
				ArtifactID: strings.Repeat("4", 32), Revision: strings.Repeat("5", 64),
				PipelineFingerprint: "archive-inspect-pipeline-v1", SecurityPolicyRevision: processingSecurityPolicyRevision,
				AbsoluteExpiresAt: now.Add(time.Hour),
				Members: []processing.ArchiveMemberIndexEntry{{
					OpaqueID: strings.Repeat("6", 32), Ordinal: 0, DisplayName: "member.txt", Size: 3, MediaType: "text/plain",
				}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	unpublished := make(chan struct{})
	var unpublishOnce sync.Once
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil }, Build: func(
			_ context.Context,
			_ backupasset.ExportConfig,
			store *assetexport.Store,
		) (*managedExportGraph, error) {
			return &managedExportGraph{
				store: store, service: &assetexport.Service{}, delivery: &assetexport.DeliveryGateway{}, archiveMember: archiveMember,
				stopAccepting: func() { unpublishOnce.Do(func() { close(unpublished) }) },
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	lookup := processing.ArchiveMemberIndexLookup{
		Actor: content.DeliveryActor{UserID: 42, Username: "admin", Role: "admin"}, Ref: asset.Ref,
	}
	if _, err := manager.Service().Status(context.Background(), assetexport.StatusRequest{}); !errors.Is(err, assetexport.ErrInvalidSelection) {
		t.Fatalf("published Export service error=%v, want invalid selection", err)
	}
	if err := manager.Delivery().RevokeArchiveMember(context.Background(), strings.Repeat("7", 32), "runtime_shutdown"); !errors.Is(err, assetexport.ErrInvalidDeliveryRequest) {
		t.Fatalf("published Export delivery error=%v, want invalid delivery request", err)
	}
	if _, err := manager.ArchiveMember().ListIndex(context.Background(), lookup); err != nil {
		t.Fatalf("published Export archive-member facade error=%v", err)
	}

	_, releaseFacadeReference, acquired := manager.publication.acquire()
	if !acquired {
		t.Fatal("acquire published Export facade reference")
	}
	released := false
	t.Cleanup(func() {
		if !released {
			releaseFacadeReference()
		}
		_ = manager.Shutdown(context.Background())
	})
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case <-unpublished:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not unpublish before draining the held facade reference")
	}

	if _, err := manager.Service().Status(context.Background(), assetexport.StatusRequest{}); !errors.Is(err, assetexport.ErrUnavailable) {
		t.Fatalf("new Export service call after unpublish error=%v, want unavailable", err)
	}
	if err := manager.Delivery().RevokeArchiveMember(context.Background(), strings.Repeat("7", 32), "runtime_shutdown"); !errors.Is(err, assetexport.ErrUnavailable) {
		t.Fatalf("new Export delivery call after unpublish error=%v, want unavailable", err)
	}
	if _, err := manager.ArchiveMember().ListIndex(context.Background(), lookup); !errors.Is(err, processing.ErrArchiveMemberUnavailable) {
		t.Fatalf("new Export archive-member call after unpublish error=%v, want unavailable", err)
	}
	select {
	case shutdownErr := <-shutdownDone:
		t.Fatalf("Shutdown completed before the held facade reference drained: %v", shutdownErr)
	default:
	}

	releaseFacadeReference()
	released = true
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown after facade drain: %v", err)
	}
}

func TestManagedExportRuntimePrepareSchemaDownWaitsForFacadeReferencesBeforeDrain(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	var drains, callbacks atomic.Int32
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store: store,
			drain: func(context.Context) error {
				drains.Add(1)
				return nil
			},
		}, nil
	})
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}
	_, releaseFacadeReference, acquired := manager.publication.acquire()
	if !acquired {
		t.Fatal("acquire published Export facade reference")
	}
	t.Cleanup(func() {
		releaseFacadeReference()
		_ = manager.Shutdown(context.Background())
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := manager.PrepareSchemaDown(ctx, func() error {
		callbacks.Add(1)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("schema down with held facade reference error=%v, want deadline exceeded", err)
	}
	if drains.Load() != 0 || callbacks.Load() != 0 {
		t.Fatalf("schema down ran before facade drain: drains=%d callbacks=%d", drains.Load(), callbacks.Load())
	}

	releaseFacadeReference()
	if err := manager.PrepareSchemaDown(context.Background(), func() error {
		callbacks.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("schema down after facade release: %v", err)
	}
	if drains.Load() != 1 || callbacks.Load() != 1 {
		t.Fatalf("schema down retry calls drains=%d callbacks=%d, want one each", drains.Load(), callbacks.Load())
	}
}

func TestManagedExportRuntimeShutdownDoesNotCloseStoreAfterGraphShutdownFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	shutdownErr := errors.New("graph shutdown failed")
	var failShutdown atomic.Bool
	failShutdown.Store(true)
	var shutdowns atomic.Int32
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store: store,
			shutdown: func(context.Context) error {
				shutdowns.Add(1)
				if failShutdown.Load() {
					return shutdownErr
				}
				return nil
			},
		}, nil
	})
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}
	t.Cleanup(func() {
		failShutdown.Store(false)
		_ = manager.Shutdown(context.Background())
	})

	if err := manager.Shutdown(context.Background()); !errors.Is(err, shutdownErr) {
		t.Fatalf("Shutdown graph failure error=%v, want graph shutdown failure", err)
	}
	if manager.graph == nil {
		t.Fatal("Shutdown discarded the graph after graph shutdown failure")
	}
	if reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root}); err == nil {
		_ = reopened.Close()
		t.Fatal("Shutdown closed the Export Store after graph shutdown failure")
	}

	failShutdown.Store(false)
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown after graph shutdown failure: %v", err)
	}
	if shutdowns.Load() != 2 {
		t.Fatalf("graph shutdown calls=%d, want two", shutdowns.Load())
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("retry Shutdown retained Export Store lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeShutdownRetainsGraphAfterStoreCloseFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	movedRoot := filepath.Join(t.TempDir(), "moved-export")
	var shutdowns atomic.Int32
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store: store,
			shutdown: func(context.Context) error {
				shutdowns.Add(1)
				return nil
			},
		}, nil
	})
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	graph := manager.graph
	if graph == nil {
		t.Fatal("Startup did not retain the Export graph")
	}
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatalf("invalidate Export Store root: %v", err)
	}

	err := manager.Shutdown(context.Background())
	if !errors.Is(err, assetexport.ErrInvalidStore) {
		t.Fatalf("Shutdown error=%v, want Store.Close root-identity failure", err)
	}
	if shutdowns.Load() != 1 {
		t.Fatalf("graph shutdown calls=%d, want one", shutdowns.Load())
	}
	if manager.graph != graph || !manager.graphShutdown {
		t.Fatal("failed Store.Close discarded the shutdown-complete Export graph owner")
	}
	if manager.Ready() || manager.publication.current() != nil {
		t.Fatal("failed Store.Close retained a ready or published Export graph")
	}
	if _, release, acquired := manager.publication.acquire(); acquired {
		release()
		t.Fatal("facade acquired a shutdown-complete Export graph after Store.Close failure")
	}

	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown after Store.Close failure: %v", err)
	}
	if shutdowns.Load() != 1 {
		t.Fatalf("retry Shutdown calls=%d, want one completed graph shutdown", shutdowns.Load())
	}
	if manager.graph != nil || manager.graphShutdown {
		t.Fatal("retry Shutdown retained the closed Export graph")
	}
	reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: movedRoot})
	if openErr != nil {
		t.Fatalf("retry Shutdown retained Export Store lock: %v", openErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeRunReturnsAfterShutdown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	var runs atomic.Int32
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store: store,
			run: func(context.Context) {
				runs.Add(1)
			},
		}, nil
	})
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	done := make(chan struct{})
	go func() {
		manager.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run remained blocked after the runtime stopped")
	}
	if runs.Load() != 0 {
		t.Fatalf("Run started a stopped Export graph %d time(s)", runs.Load())
	}
}

func TestManagedExportRuntimeRunWaitsForStartupPublication(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	startupEntered := make(chan struct{})
	releaseStartup := make(chan struct{})
	runStarted := make(chan struct{})
	var releaseOnce sync.Once
	resumeStartup := func() { releaseOnce.Do(func() { close(releaseStartup) }) }
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store: store,
			startup: func(context.Context) error {
				close(startupEntered)
				<-releaseStartup
				return nil
			},
			run: func(ctx context.Context) {
				close(runStarted)
				<-ctx.Done()
			},
		}, nil
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		manager.Run(runCtx)
		close(runDone)
	}()
	startupDone := make(chan error, 1)
	startupReturned := false
	go func() { startupDone <- manager.Startup(context.Background()) }()
	<-startupEntered
	t.Cleanup(func() {
		resumeStartup()
		cancelRun()
		if !startupReturned {
			select {
			case <-startupDone:
			case <-time.After(time.Second):
				t.Error("unpublished Export startup did not return during cleanup")
			}
		}
		select {
		case <-runDone:
		case <-time.After(time.Second):
			t.Error("Export Run did not return during cleanup")
		}
		_ = manager.Shutdown(context.Background())
	})

	select {
	case <-runStarted:
		t.Fatal("Run started an unpublished Export graph before startup completed")
	case <-time.After(100 * time.Millisecond):
	}

	resumeStartup()
	if err := <-startupDone; err != nil {
		t.Fatalf("Startup: %v", err)
	}
	startupReturned = true
	select {
	case <-runStarted:
	case <-time.After(time.Second):
		t.Fatal("Run did not start the published Export graph")
	}
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

func TestManagedExportRuntimeShutdownCleansUnpublishedStartupGraph(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	jobID := strings.Repeat("1", 32)
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportJob{
		ID:             jobID,
		ExecutionState: string(assetexport.ExecutionQueued),
		CleanupState:   string(assetexport.CleanupNone),
		CreatedAt:      now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	startupEntered := make(chan struct{})
	startupCanceled := make(chan struct{})
	releaseStartup := make(chan struct{})
	var releaseOnce sync.Once
	resumeStartup := func() { releaseOnce.Do(func() { close(releaseStartup) }) }
	var startupCanceledOnce sync.Once
	var stops, shutdowns, reconciles atomic.Int32
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil }, Build: func(
			_ context.Context,
			_ backupasset.ExportConfig,
			store *assetexport.Store,
		) (*managedExportGraph, error) {
			return &managedExportGraph{
				store:         store,
				stopAccepting: func() { stops.Add(1) },
				startup: func(ctx context.Context) error {
					close(startupEntered)
					select {
					case <-ctx.Done():
						startupCanceledOnce.Do(func() { close(startupCanceled) })
						<-releaseStartup
						return ctx.Err()
					case <-releaseStartup:
						reconciles.Add(1)
						return db.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
							Update("execution_state", string(assetexport.ExecutionRunning)).Error
					}
				},
				shutdown: func(context.Context) error {
					shutdowns.Add(1)
					return nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	startupDone := make(chan error, 1)
	startupReturned := false
	go func() { startupDone <- manager.Startup(context.Background()) }()
	<-startupEntered
	t.Cleanup(func() {
		resumeStartup()
		if !startupReturned {
			select {
			case <-startupDone:
			case <-time.After(time.Second):
				t.Error("unpublished Export startup did not return during cleanup")
			}
		}
		_ = manager.Shutdown(context.Background())
	})

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case shutdownErr := <-shutdownDone:
		resumeStartup()
		startupErr := <-startupDone
		startupReturned = true
		var job model.BackupAssetExportJob
		if err := db.First(&job, "id = ?", jobID).Error; err != nil {
			t.Fatal(err)
		}
		if reconciles.Load() != 1 || job.ExecutionState != string(assetexport.ExecutionRunning) {
			t.Fatalf(
				"Shutdown returned before unpublished startup drained: shutdown=%v startup=%v reconciles=%d state=%s",
				shutdownErr, startupErr, reconciles.Load(), job.ExecutionState,
			)
		}
		t.Fatalf("Shutdown returned before unpublished startup observed cancellation: %v", shutdownErr)
	case <-time.After(time.Second):
	}
	select {
	case <-startupCanceled:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not cancel unpublished Export startup")
	}
	select {
	case shutdownErr := <-shutdownDone:
		t.Fatalf("Shutdown returned before canceled unpublished startup drained: %v", shutdownErr)
	default:
	}
	resumeStartup()
	err = <-startupDone
	startupReturned = true
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("Startup after Shutdown error=%v, want unavailable/stopped", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown after unpublished startup drain: %v", err)
	}
	if reconciles.Load() != 0 {
		t.Fatalf("canceled unpublished startup reconciled %d time(s)", reconciles.Load())
	}
	var job model.BackupAssetExportJob
	if err := db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionQueued) {
		t.Fatalf("canceled unpublished startup mutated durable job state=%s", job.ExecutionState)
	}
	if manager.Ready() || manager.publication.current() != nil {
		t.Fatalf("Shutdown published unpublished startup graph=%p ready=%v", manager.publication.current(), manager.Ready())
	}
	if stops.Load() != 1 || shutdowns.Load() != 1 {
		t.Fatalf("unpublished graph cleanup stops=%d shutdowns=%d, want one each", stops.Load(), shutdowns.Load())
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("unpublished graph cleanup retained Export Store lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeStartupJoinsStoreCloseErrorAfterUnpublishedShutdown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	movedRoot := filepath.Join(t.TempDir(), "moved-export")
	startupEntered := make(chan struct{})
	releaseStartup := make(chan struct{})
	shutdownFenced := make(chan struct{})
	var releaseOnce sync.Once
	var shutdownFenceOnce sync.Once
	resumeStartup := func() { releaseOnce.Do(func() { close(releaseStartup) }) }
	var stops, shutdowns atomic.Int32
	var graph *managedExportGraph
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		graph = &managedExportGraph{
			store: store,
			stopAccepting: func() {
				stops.Add(1)
				shutdownFenceOnce.Do(func() { close(shutdownFenced) })
			},
			startup: func(context.Context) error {
				close(startupEntered)
				<-releaseStartup
				return nil
			},
			shutdown: func(context.Context) error {
				shutdowns.Add(1)
				return nil
			},
		}
		return graph, nil
	})
	startupDone := make(chan error, 1)
	startupReturned := false
	go func() { startupDone <- manager.Startup(context.Background()) }()
	<-startupEntered
	t.Cleanup(func() {
		resumeStartup()
		if !startupReturned {
			select {
			case <-startupDone:
			case <-time.After(time.Second):
				t.Error("unpublished Store-close Export startup did not return during cleanup")
			}
		}
		_ = manager.Shutdown(context.Background())
	})

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case <-shutdownFenced:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not fence unpublished Export startup")
	}
	select {
	case shutdownErr := <-shutdownDone:
		t.Fatalf("Shutdown returned before unpublished startup drained: %v", shutdownErr)
	default:
	}
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatalf("invalidate unpublished Export Store root: %v", err)
	}
	resumeStartup()
	err := <-startupDone
	startupReturned = true
	if !errors.Is(err, backupasset.ErrInvalidState) || !errors.Is(err, assetexport.ErrInvalidStore) {
		t.Fatalf("Startup error=%v, want stopped and Store.Close root-identity errors", err)
	}
	if shutdownErr := <-shutdownDone; !errors.Is(shutdownErr, assetexport.ErrInvalidStore) {
		t.Fatalf("Shutdown error=%v, want Store.Close root-identity failure after startup drain", shutdownErr)
	}
	if stops.Load() != 1 || shutdowns.Load() != 1 {
		t.Fatalf("unpublished Store-close cleanup stops=%d shutdowns=%d, want one each", stops.Load(), shutdowns.Load())
	}
	if manager.graph != graph || !manager.graphShutdown {
		t.Fatal("failed unpublished Store.Close discarded the shutdown-complete Export graph owner")
	}
	if manager.Ready() || manager.publication.current() != nil {
		t.Fatal("failed unpublished Store.Close retained a ready or published Export graph")
	}
	if _, release, acquired := manager.publication.acquire(); acquired {
		release()
		t.Fatal("facade acquired a shutdown-complete Export graph after unpublished Store.Close failure")
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown after unpublished Store.Close failure: %v", err)
	}
	if shutdowns.Load() != 1 {
		t.Fatalf("retry Shutdown calls=%d, want one completed graph shutdown", shutdowns.Load())
	}
	if manager.graph != nil || manager.graphShutdown {
		t.Fatal("retry Shutdown retained the closed unpublished Export graph")
	}
	reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: movedRoot})
	if openErr != nil {
		t.Fatalf("retry Shutdown retained invalidated unpublished Export Store lock: %v", openErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeUnpublishedStartupRetainsGraphAfterShutdownFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	shutdownErr := errors.New("unpublished graph shutdown failed")
	var failShutdown atomic.Bool
	failShutdown.Store(true)
	startupEntered := make(chan struct{})
	releaseStartup := make(chan struct{})
	shutdownFenced := make(chan struct{})
	var releaseOnce sync.Once
	var shutdownFenceOnce sync.Once
	resumeStartup := func() { releaseOnce.Do(func() { close(releaseStartup) }) }
	var stops, shutdowns atomic.Int32
	var graph *managedExportGraph
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		graph = &managedExportGraph{
			store: store,
			stopAccepting: func() {
				stops.Add(1)
				shutdownFenceOnce.Do(func() { close(shutdownFenced) })
			},
			startup: func(context.Context) error {
				close(startupEntered)
				<-releaseStartup
				return nil
			},
			shutdown: func(context.Context) error {
				shutdowns.Add(1)
				if failShutdown.Load() {
					return shutdownErr
				}
				return nil
			},
		}
		return graph, nil
	})
	startupDone := make(chan error, 1)
	startupReturned := false
	go func() { startupDone <- manager.Startup(context.Background()) }()
	<-startupEntered
	t.Cleanup(func() {
		failShutdown.Store(false)
		resumeStartup()
		if !startupReturned {
			select {
			case <-startupDone:
			case <-time.After(time.Second):
				t.Error("unpublished failed-shutdown Export startup did not return during cleanup")
			}
		}
		_ = manager.Shutdown(context.Background())
	})

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case <-shutdownFenced:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not fence unpublished Export startup")
	}
	select {
	case shutdownResult := <-shutdownDone:
		t.Fatalf("Shutdown returned before unpublished startup drained: %v", shutdownResult)
	default:
	}
	resumeStartup()
	err := <-startupDone
	startupReturned = true
	if !errors.Is(err, backupasset.ErrInvalidState) || !errors.Is(err, shutdownErr) {
		t.Fatalf("Startup error=%v, want stopped and graph shutdown errors", err)
	}
	if shutdownResult := <-shutdownDone; !errors.Is(shutdownResult, shutdownErr) {
		t.Fatalf("Shutdown error=%v, want unpublished graph shutdown failure after startup drain", shutdownResult)
	}
	if stops.Load() != 1 || shutdowns.Load() != 1 {
		t.Fatalf("unpublished failed-shutdown cleanup stops=%d shutdowns=%d, want one each", stops.Load(), shutdowns.Load())
	}
	if manager.graph != graph || manager.graphShutdown {
		t.Fatal("failed unpublished graph shutdown discarded or marked its retry owner complete")
	}
	if manager.Ready() || manager.publication.current() != nil {
		t.Fatal("failed unpublished graph shutdown retained a ready or published graph")
	}
	if reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: root}); openErr == nil {
		_ = reopened.Close()
		t.Fatal("failed unpublished graph shutdown released the Export Store lock")
	}

	failShutdown.Store(false)
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown after unpublished graph shutdown failure: %v", err)
	}
	if shutdowns.Load() != 2 {
		t.Fatalf("retry Shutdown calls=%d, want two graph shutdown attempts", shutdowns.Load())
	}
	if manager.graph != nil || manager.graphShutdown {
		t.Fatal("retry Shutdown retained the recovered unpublished Export graph")
	}
	reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if openErr != nil {
		t.Fatalf("retry Shutdown retained unpublished Export Store lock: %v", openErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeStartupDoesNotPublishAfterConcurrentShutdown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	shutdownFenced := make(chan struct{})
	var shutdownFenceOnce sync.Once
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store:         store,
			stopAccepting: func() { shutdownFenceOnce.Do(func() { close(shutdownFenced) }) },
		}, nil
	})
	startupPaused := make(chan struct{})
	releaseStartup := make(chan struct{})
	var releaseOnce sync.Once
	resumeStartup := func() {
		releaseOnce.Do(func() { close(releaseStartup) })
	}
	manager.beforePublish = func() {
		close(startupPaused)
		<-releaseStartup
	}
	startupDone := make(chan error, 1)
	startupReturned := false
	go func() { startupDone <- manager.Startup(context.Background()) }()
	<-startupPaused
	t.Cleanup(func() {
		resumeStartup()
		if !startupReturned {
			select {
			case <-startupDone:
			case <-time.After(time.Second):
				t.Error("pre-publication Export startup did not return during cleanup")
			}
		}
		_ = manager.Shutdown(context.Background())
	})

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case <-shutdownFenced:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not fence the pre-publication Startup")
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before the pre-publication Startup drained: %v", err)
	default:
	}

	resumeStartup()
	select {
	case err := <-startupDone:
		startupReturned = true
		if !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("Startup after Shutdown error=%v, want unavailable/stopped", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Startup did not return after concurrent Shutdown")
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown after pre-publication startup drain: %v", err)
	}
	if manager.publication.current() != nil {
		t.Fatal("stopped Export runtime published a graph after Shutdown")
	}
	if _, release, acquired := manager.publication.acquire(); acquired {
		release()
		t.Fatal("Export facade acquired a graph after Shutdown")
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("concurrent Shutdown retained Export Store lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimePrePublicationStartupRetainsGraphAfterShutdownFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	shutdownErr := errors.New("pre-publication graph shutdown failed")
	var failShutdown atomic.Bool
	failShutdown.Store(true)
	var stops, shutdowns atomic.Int32
	shutdownFenced := make(chan struct{})
	var shutdownFenceOnce sync.Once
	var graph *managedExportGraph
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		graph = &managedExportGraph{
			store: store,
			stopAccepting: func() {
				stops.Add(1)
				shutdownFenceOnce.Do(func() { close(shutdownFenced) })
			},
			shutdown: func(context.Context) error {
				shutdowns.Add(1)
				if failShutdown.Load() {
					return shutdownErr
				}
				return nil
			},
		}
		return graph, nil
	})
	startupPaused := make(chan struct{})
	releaseStartup := make(chan struct{})
	var releaseOnce sync.Once
	resumeStartup := func() { releaseOnce.Do(func() { close(releaseStartup) }) }
	manager.beforePublish = func() {
		close(startupPaused)
		<-releaseStartup
	}
	startupDone := make(chan error, 1)
	startupReturned := false
	go func() { startupDone <- manager.Startup(context.Background()) }()
	<-startupPaused
	t.Cleanup(func() {
		failShutdown.Store(false)
		resumeStartup()
		if !startupReturned {
			select {
			case <-startupDone:
			case <-time.After(time.Second):
				t.Error("failed pre-publication Export startup did not return during cleanup")
			}
		}
		_ = manager.Shutdown(context.Background())
	})

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case <-shutdownFenced:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not fence the pre-publication Startup")
	}
	select {
	case shutdownResult := <-shutdownDone:
		t.Fatalf("Shutdown returned before pre-publication startup drained: %v", shutdownResult)
	default:
	}

	resumeStartup()
	err := <-startupDone
	startupReturned = true
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("Startup after failed Shutdown error=%v, want unavailable/stopped", err)
	}
	if shutdownResult := <-shutdownDone; !errors.Is(shutdownResult, shutdownErr) {
		t.Fatalf("Shutdown error=%v, want graph shutdown failure after startup drain", shutdownResult)
	}
	if stops.Load() != 1 || shutdowns.Load() != 1 {
		t.Fatalf("pre-publication failed-shutdown calls stops=%d shutdowns=%d, want one each", stops.Load(), shutdowns.Load())
	}
	if manager.graph != graph || manager.graphShutdown {
		t.Fatal("pre-publication handoff changed the retained failed-shutdown graph owner")
	}
	if manager.Ready() || manager.publication.current() != nil {
		t.Fatal("failed pre-publication graph shutdown retained a ready or published graph")
	}
	if reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: root}); openErr == nil {
		_ = reopened.Close()
		t.Fatal("pre-publication handoff closed the Store after failed graph shutdown")
	}

	failShutdown.Store(false)
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown after pre-publication graph shutdown failure: %v", err)
	}
	if shutdowns.Load() != 2 {
		t.Fatalf("retry Shutdown calls=%d, want two graph shutdown attempts", shutdowns.Load())
	}
	if manager.graph != nil || manager.graphShutdown {
		t.Fatal("retry Shutdown retained the recovered pre-publication Export graph")
	}
	reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if openErr != nil {
		t.Fatalf("retry Shutdown retained pre-publication Export Store lock: %v", openErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeStartupJoinsBuildAndStoreCloseErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	movedRoot := filepath.Join(t.TempDir(), "moved-export")
	buildErr := errors.New("build Export graph")
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		_ *assetexport.Store,
	) (*managedExportGraph, error) {
		if err := os.Rename(root, movedRoot); err != nil {
			t.Fatalf("invalidate Export Store root: %v", err)
		}
		return nil, buildErr
	})

	err := manager.Startup(context.Background())
	if !errors.Is(err, buildErr) {
		t.Fatalf("Startup error=%v, want build failure", err)
	}
	if !errors.Is(err, assetexport.ErrInvalidStore) {
		t.Fatalf("Startup error=%v, want Store.Close root-identity failure", err)
	}
	reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: movedRoot})
	if openErr != nil {
		t.Fatalf("Startup did not release invalidated Export Store lock: %v", openErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeStartupCleansNilStoreInvalidGraph(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	buildErr := errors.New("invalid Export graph build")
	shutdownErr := errors.New("invalid Export graph shutdown")
	var stops, shutdowns atomic.Int32
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		if store == nil {
			t.Fatal("Build received nil Export Store")
		}
		return &managedExportGraph{
			stopAccepting: func() { stops.Add(1) },
			shutdown:      func(context.Context) error { shutdowns.Add(1); return shutdownErr },
		}, buildErr
	})

	err := manager.Startup(context.Background())
	if !errors.Is(err, buildErr) || !errors.Is(err, shutdownErr) {
		t.Fatalf("Startup invalid nil-store graph error=%v, want joined build and shutdown errors", err)
	}
	if stops.Load() != 1 || shutdowns.Load() != 1 {
		t.Fatalf("invalid nil-store graph stops=%d shutdowns=%d, want one each", stops.Load(), shutdowns.Load())
	}
	if manager.graph != nil || manager.Ready() || manager.publication.current() != nil {
		t.Fatalf("invalid nil-store graph leaked ownership: graph=%p ready=%v published=%p", manager.graph, manager.Ready(), manager.publication.current())
	}
	reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if openErr != nil {
		t.Fatalf("invalid nil-store graph retained Export Store lock: %v", openErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeStartupCleansFailedGraphBeforeStoreClose(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	movedRoot := filepath.Join(t.TempDir(), "moved-export")
	startupErr := errors.New("start Export graph")
	events := make([]string, 0, 3)
	var moveRootErr error
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store: store,
			startup: func(context.Context) error {
				events = append(events, "startup")
				return startupErr
			},
			stopAccepting: func() {
				events = append(events, "stop")
			},
			shutdown: func(context.Context) error {
				events = append(events, "shutdown")
				if err := os.Rename(root, movedRoot); err != nil {
					moveRootErr = fmt.Errorf("move failed Export Store root: %w", err)
					return moveRootErr
				}
				return nil
			},
		}, nil
	})

	err := manager.Startup(context.Background())
	if moveRootErr != nil {
		t.Fatal(moveRootErr)
	}
	if _, statErr := os.Stat(movedRoot); statErr != nil {
		t.Fatalf("failed graph shutdown did not move the Export Store root: %v", statErr)
	}
	if !errors.Is(err, startupErr) || !errors.Is(err, assetexport.ErrInvalidStore) {
		t.Fatalf("Startup error=%v, want startup and Store.Close root-identity errors", err)
	}
	if !reflect.DeepEqual(events, []string{"startup", "stop", "shutdown"}) {
		t.Fatalf("failed graph cleanup events=%v, want [startup stop shutdown]", events)
	}
	if manager.graph != nil || manager.Ready() || manager.publication.current() != nil {
		t.Fatalf("failed graph startup retained graph=%p ready=%v published=%p",
			manager.graph, manager.Ready(), manager.publication.current())
	}
	reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: movedRoot})
	if openErr != nil {
		t.Fatalf("failed graph startup retained Export Store lock: %v", openErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportRuntimeFailedStartupCleanupAllowsOneShotFailureRetry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	startupErr := errors.New("pre-publication startup failed")
	cleanupErr := errors.New("pre-publication cleanup failed once")
	var builds, stops, shutdowns atomic.Int32
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		generation := builds.Add(1)
		return &managedExportGraph{
			store: store,
			startup: func(context.Context) error {
				if generation == 1 {
					return startupErr
				}
				return nil
			},
			stopAccepting: func() { stops.Add(1) },
			shutdown: func(context.Context) error {
				shutdowns.Add(1)
				if generation == 1 {
					return cleanupErr
				}
				return nil
			},
		}, nil
	})
	if err := manager.Startup(context.Background()); !errors.Is(err, startupErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("first pre-publication Startup error=%v, want startup and cleanup errors", err)
	}
	if manager.graph != nil || manager.Ready() || manager.publication.current() != nil {
		t.Fatalf("failed pre-publication cleanup retained graph=%p ready=%v published=%p", manager.graph, manager.Ready(), manager.publication.current())
	}
	reopened, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatalf("failed pre-publication cleanup retained Export Store lock: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatalf("retry pre-publication Startup: %v", err)
	}
	if builds.Load() != 2 || stops.Load() != 1 || shutdowns.Load() != 1 || !manager.Ready() {
		t.Fatalf(
			"pre-publication retry builds=%d stops=%d shutdowns=%d ready=%v",
			builds.Load(), stops.Load(), shutdowns.Load(), manager.Ready(),
		)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedExportRuntimeStartupCleansCompetingStartedGraphBeforeStoreClose(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	movedRoot := filepath.Join(t.TempDir(), "moved-export")
	shutdownErr := errors.New("competing graph shutdown failed")
	events := make([]string, 0, 3)
	startupCalled := make(chan struct{})
	startupPaused := make(chan struct{})
	releaseStartup := make(chan struct{})
	var releaseOnce sync.Once
	resumeStartup := func() { releaseOnce.Do(func() { close(releaseStartup) }) }
	winner := &managedExportGraph{}
	var invalidateStoreErr error
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{
			store: store,
			startup: func(context.Context) error {
				events = append(events, "startup")
				close(startupCalled)
				return nil
			},
			stopAccepting: func() {
				events = append(events, "stop")
			},
			shutdown: func(context.Context) error {
				events = append(events, "shutdown")
				if err := os.Rename(root, movedRoot); err != nil {
					invalidateStoreErr = fmt.Errorf("invalidate competing Export Store root after graph shutdown: %w", err)
					return errors.Join(shutdownErr, invalidateStoreErr)
				}
				return shutdownErr
			},
		}, nil
	})

	manager.beforePublish = func() {
		close(startupPaused)
		<-releaseStartup
	}
	startupDone := make(chan error, 1)
	startupReturned := false
	go func() { startupDone <- manager.Startup(context.Background()) }()
	<-startupCalled
	<-startupPaused
	t.Cleanup(func() {
		resumeStartup()
		if !startupReturned {
			select {
			case <-startupDone:
			case <-time.After(time.Second):
				t.Error("competing-graph Export startup did not return during cleanup")
			}
		}
		manager.mu.Lock()
		if manager.graph == winner {
			manager.graph = nil
			manager.signalGraphChangedLocked()
		}
		manager.mu.Unlock()
		_ = manager.Shutdown(context.Background())
	})

	manager.mu.Lock()
	manager.graph = winner
	manager.graphShutdown = false
	manager.signalGraphChangedLocked()
	manager.mu.Unlock()
	resumeStartup()
	err := <-startupDone
	startupReturned = true
	if invalidateStoreErr != nil {
		t.Fatal(invalidateStoreErr)
	}
	if !errors.Is(err, shutdownErr) || !errors.Is(err, assetexport.ErrInvalidStore) {
		t.Fatalf("Startup competing-graph error=%v, want graph shutdown and post-shutdown Store.Close root-identity failures", err)
	}
	if !reflect.DeepEqual(events, []string{"startup", "stop", "shutdown"}) {
		t.Fatalf("competing graph cleanup events=%v, want [startup stop shutdown]", events)
	}
	if manager.graph != winner || manager.Ready() || manager.stopped.Load() || manager.publication.current() != nil {
		t.Fatalf("competing winner=%p graph=%p ready=%v stopped=%v published=%p",
			winner, manager.graph, manager.Ready(), manager.stopped.Load(), manager.publication.current())
	}
	shutdownLockAcquired := make(chan struct{})
	go func() {
		manager.shutdownMu.Lock()
		defer manager.shutdownMu.Unlock()
		close(shutdownLockAcquired)
	}()
	select {
	case <-shutdownLockAcquired:
	case <-time.After(time.Second):
		t.Fatal("competing graph cleanup retained the runtime shutdown lock")
	}
	reopened, openErr := assetexport.OpenStore(assetexport.StoreConfig{Root: movedRoot})
	if openErr != nil {
		t.Fatalf("competing graph cleanup retained Export Store lock: %v", openErr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened Export Store: %v", err)
	}
}

func TestManagedExportWorkerClaimsSpoolsSealsAndPublishesQueuedJob(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	jobID, itemID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionQueued), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	attempts := &managedExportAttemptsFake{calls: &calls, claim: assetexport.AttemptClaim{
		AttemptID: strings.Repeat("3", 32), FenceToken: []byte(strings.Repeat("f", 32)),
	}}
	backend := &managedExportBackendFake{calls: &calls, seal: assetexport.PersistentSealResult{ArtifactID: strings.Repeat("4", 32)}}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 10, WorkerConcurrency: 1, WorkerOwner: "export-worker-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claim:" + jobID, "spool:" + jobID + ":" + itemID,
		"heartbeat:" + jobID, "seal:" + jobID, "publish:" + jobID,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("execution calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerStartupReconcilesMetadataWithoutExecutingQueuedExports(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItem{}, &model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("a", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionQueued), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db,
		Attempts: &managedExportAttemptsFake{calls: &calls, claim: assetexport.AttemptClaim{
			AttemptID: strings.Repeat("b", 32), FenceToken: []byte(strings.Repeat("f", 32)),
		}},
		Worker: &managedExportBackendFake{calls: &calls}, Lifecycle: managedExportLifecycleFake{},
		Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-startup-metadata",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("startup executed queued Export work: calls=%v", calls)
	}
}

func TestManagedExportRuntimeStopAcceptingStaysStickyAcrossConcurrentStartupPublication(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	manager := newEnabledManagedExportRuntime(t, root, func(
		_ context.Context,
		_ backupasset.ExportConfig,
		store *assetexport.Store,
	) (*managedExportGraph, error) {
		return &managedExportGraph{store: store}, nil
	})
	startupPaused := make(chan struct{})
	releaseStartup := make(chan struct{})
	manager.beforePublish = func() {
		close(startupPaused)
		<-releaseStartup
	}
	startupDone := make(chan error, 1)
	go func() { startupDone <- manager.Startup(context.Background()) }()
	<-startupPaused

	manager.StopAccepting()
	close(releaseStartup)
	err := <-startupDone
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("startup after sticky StopAccepting error=%v, want unavailable", err)
	}
	if manager.Ready() || manager.publication.current() != nil {
		t.Fatalf("sticky StopAccepting published graph=%p ready=%v", manager.publication.current(), manager.Ready())
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedExportWorkerDrainFencesJoinedActiveAttemptsForImmediateRestart(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	jobID, attemptID := strings.Repeat("c", 32), strings.Repeat("d", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionRunning), CurrentAttemptID: &attemptID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportAttempt{
		ID: attemptID, JobID: jobID, State: string(assetexport.AttemptActive), IsCurrent: true,
		FenceToken: []byte(strings.Repeat("e", 32)), FenceDigest: strings.Repeat("f", 64), NoncePrefix: []byte(strings.Repeat("n", 8)),
		LeaseExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	attempts := &managedExportJoinedAttemptAttempts{}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &[]string{}},
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-drain-fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts.failCalls.Load() != 1 || attempts.lastJobID != jobID || attempts.lastAttemptID != attemptID {
		t.Fatalf("joined active attempt was not fenced: calls=%d job=%s attempt=%s", attempts.failCalls.Load(), attempts.lastJobID, attempts.lastAttemptID)
	}
}

func TestManagedExportWorkerRunWakesQueuedWorkBeforeGCCadence(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}); err != nil {
		t.Fatal(err)
	}
	jobID, itemID := strings.Repeat("e", 32), strings.Repeat("f", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionQueued), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}
	attempts := &managedExportWakeAttempts{claims: make(chan string, 1)}
	backendCalls := []string{}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &backendCalls},
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-wake",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Export worker did not stop")
		}
	}()
	select {
	case claimed := <-attempts.claims:
		if claimed != jobID {
			t.Fatalf("woken Export claim=%s want=%s", claimed, jobID)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("queued Export work waited for the GC cadence")
	}
}

func TestManagedExportWorkerBackfillsPastNonclaimableActiveJob(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runningJobID, queuedJobID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	createManagedExportRuntimeJob(t, db, &model.BackupAssetExportJob{
		ID: runningJobID, ExecutionState: string(assetexport.ExecutionRunning), UpdatedAt: now.Add(-time.Second),
	})
	createManagedExportRuntimeJob(t, db, &model.BackupAssetExportJob{
		ID: queuedJobID, ExecutionState: string(assetexport.ExecutionQueued), UpdatedAt: now,
	})
	queuedItemID := strings.Repeat("3", 32)
	if err := db.Create(&model.BackupAssetExportItem{
		ID: queuedItemID, JobID: queuedJobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}

	calls := []string{}
	attempts := &managedExportClaimableBackfillAttempts{
		nonclaimableJobIDs: map[string]struct{}{runningJobID: {}},
		claim: assetexport.AttemptClaim{
			AttemptID: strings.Repeat("4", 32), FenceToken: []byte(strings.Repeat("f", 32)),
		},
		calls: &calls,
	}
	backend := &managedExportBackendFake{
		seal:  assetexport.PersistentSealResult{ArtifactID: strings.Repeat("5", 32)},
		calls: &calls,
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-claimable-backfill",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.executeQueued(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claim:" + runningJobID, "claim:" + queuedJobID,
		"spool:" + queuedJobID + ":" + queuedItemID,
		"heartbeat:" + queuedJobID, "seal:" + queuedJobID, "publish:" + queuedJobID,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("execution calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerBackfillCursorResumesAfterBoundedNonclaimableScan(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	firstRunningID, secondRunningID, queuedJobID := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	for ordinal, job := range []struct {
		id    string
		state assetexport.ExecutionState
	}{
		{id: firstRunningID, state: assetexport.ExecutionRunning},
		{id: secondRunningID, state: assetexport.ExecutionSealing},
		{id: queuedJobID, state: assetexport.ExecutionQueued},
	} {
		createManagedExportRuntimeJob(t, db, &model.BackupAssetExportJob{
			ID: job.id, ExecutionState: string(job.state), UpdatedAt: now.Add(time.Duration(ordinal-3) * time.Second),
		})
	}
	queuedItemID := strings.Repeat("4", 32)
	if err := db.Create(&model.BackupAssetExportItem{
		ID: queuedItemID, JobID: queuedJobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}

	calls := []string{}
	attempts := &managedExportClaimableBackfillAttempts{
		nonclaimableJobIDs: map[string]struct{}{firstRunningID: {}, secondRunningID: {}},
		claim: assetexport.AttemptClaim{
			AttemptID: strings.Repeat("5", 32), FenceToken: []byte(strings.Repeat("f", 32)),
		},
		calls: &calls,
	}
	backend := &managedExportBackendFake{
		seal:  assetexport.PersistentSealResult{ArtifactID: strings.Repeat("6", 32)},
		calls: &calls,
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-bounded-backfill",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.executeQueued(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"claim:" + firstRunningID, "claim:" + secondRunningID}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("first bounded sweep calls=%v want=%v", calls, want)
	}
	if err := runner.executeQueued(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claim:" + firstRunningID, "claim:" + secondRunningID, "claim:" + queuedJobID,
		"spool:" + queuedJobID + ":" + queuedItemID,
		"heartbeat:" + queuedJobID, "seal:" + queuedJobID, "publish:" + queuedJobID,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("resumed backfill calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerHonorsFrozenConcurrency(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}); err != nil {
		t.Fatal(err)
	}
	for ordinal := 0; ordinal < 3; ordinal++ {
		jobID := fmt.Sprintf("%032x", ordinal+1)
		itemID := fmt.Sprintf("%032x", ordinal+11)
		createManagedExportRuntimeJob(t, db, &model.BackupAssetExportJob{
			ID: jobID, ExecutionState: string(assetexport.ExecutionQueued), UpdatedAt: time.Now().UTC(),
		})
		if err := db.Create(&model.BackupAssetExportItem{
			ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
			PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	backend := &managedExportConcurrencyBackend{
		started: make(chan string, 3),
		release: make(chan struct{}),
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: managedExportConcurrencyAttempts{}, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 3, WorkerConcurrency: 2, WorkerOwner: "export-worker-concurrency",
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- runner.executeQueued(context.Background()) }()
	var release sync.Once
	unblock := func() { release.Do(func() { close(backend.release) }) }
	defer func() {
		unblock()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}()
	for count := 0; count < 2; count++ {
		select {
		case <-backend.started:
		case <-time.After(time.Second):
			t.Fatal("configured Export worker concurrency was not reached")
		}
	}
	select {
	case jobID := <-backend.started:
		t.Fatalf("worker exceeded frozen concurrency before release: %s", jobID)
	case <-time.After(25 * time.Millisecond):
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := backend.maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent jobs=%d want=2", got)
	}
	if got := backend.total.Load(); got != 3 {
		t.Fatalf("executed jobs=%d want=3", got)
	}
}

func TestManagedExportWorkerStopAcceptingPreventsClaimAfterConcurrencyWait(t *testing.T) {
	previousProcs := stdRuntime.GOMAXPROCS(1)
	t.Cleanup(func() { stdRuntime.GOMAXPROCS(previousProcs) })

	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}); err != nil {
		t.Fatal(err)
	}
	firstJobID, secondJobID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	for _, jobID := range []string{firstJobID, secondJobID} {
		createManagedExportRuntimeJob(t, db, &model.BackupAssetExportJob{
			ID: jobID, ExecutionState: string(assetexport.ExecutionQueued), UpdatedAt: time.Now().UTC(),
		})
		if err := db.Create(&model.BackupAssetExportItem{
			ID: strings.Repeat(jobID[:1], 32), JobID: jobID, Ordinal: 0,
			EntryType: string(backupasset.CatalogEntryFile), PathNonce: []byte{1}, PathCiphertext: []byte{2},
			State: string(assetexport.ItemPending),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	attempts := &managedExportAdmissionBoundaryAttempts{claims: make(chan string, 2)}
	backend := &managedExportConcurrencyBackend{started: make(chan string, 2), release: make(chan struct{})}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 2, WorkerConcurrency: 1, WorkerOwner: "export-worker-admission-boundary",
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- runner.executeQueued(context.Background()) }()
	select {
	case claimed := <-attempts.claims:
		if claimed != firstJobID {
			t.Fatalf("first claim=%s, want %s", claimed, firstJobID)
		}
	case <-time.After(time.Second):
		t.Fatal("first queued Export job was not claimed")
	}
	select {
	case started := <-backend.started:
		if started != firstJobID {
			t.Fatalf("first started job=%s, want %s", started, firstJobID)
		}
	case <-time.After(time.Second):
		t.Fatal("first queued Export job did not occupy worker concurrency")
	}

	runner.StopAccepting()
	close(backend.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case claimed := <-attempts.claims:
		t.Fatalf("job %s reached AttemptCoordinator.Claim after StopAccepting returned", claimed)
	default:
	}
}

func TestManagedExportRuntimeShutdownCancelsBlockedClaimBeforeAdmissionFence(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	firstJobID, secondJobID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	for _, jobID := range []string{firstJobID, secondJobID} {
		createManagedExportRuntimeJob(t, db, &model.BackupAssetExportJob{
			ID: jobID, ExecutionState: string(assetexport.ExecutionQueued), UpdatedAt: time.Now().UTC(),
		})
	}

	attempts := &managedExportBlockingClaimAttempts{
		claims: make(chan string, 2), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	backendCalls := []string{}
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &backendCalls},
				Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
				Cadence: 5 * time.Millisecond, BatchSize: 2, WorkerConcurrency: 1,
				WorkerOwner: "export-worker-blocked-claim-shutdown",
			})
			if err != nil {
				return nil, err
			}
			return &managedExportGraph{
				store: store, runner: runner, stopAccepting: runner.StopAccepting,
				drain: runner.Drain, run: runner.Run, shutdown: runner.Shutdown,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		manager.Run(runCtx)
		close(runDone)
	}()
	var releaseOnce sync.Once
	releaseClaim := func() { releaseOnce.Do(func() { close(attempts.release) }) }
	shutdownDone := make(chan error, 1)
	shutdownStarted := false
	shutdownReturned := false
	t.Cleanup(func() {
		releaseClaim()
		cancelRun()
		if shutdownStarted && !shutdownReturned {
			select {
			case <-shutdownDone:
			case <-time.After(time.Second):
				t.Error("blocked-claim Export shutdown did not return during cleanup")
			}
		} else if !shutdownStarted {
			if err := manager.Shutdown(context.Background()); err != nil {
				t.Errorf("cleanup blocked-claim Export shutdown: %v", err)
			}
		}
		select {
		case <-runDone:
		case <-time.After(time.Second):
			t.Error("blocked-claim Export runtime Run did not return during cleanup")
		}
	})

	select {
	case claimed := <-attempts.claims:
		if claimed != firstJobID {
			t.Fatalf("first blocked claim=%s, want %s", claimed, firstJobID)
		}
	case <-time.After(time.Second):
		t.Fatal("managed Export runtime did not begin the blocked claim")
	}

	shutdownStarted = true
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case <-attempts.canceled:
	case <-time.After(time.Second):
		t.Fatal("Export shutdown did not cancel the blocked claim before waiting for admission")
	}
	select {
	case err := <-shutdownDone:
		shutdownReturned = true
		if err != nil {
			t.Fatalf("blocked-claim Export shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Export shutdown remained blocked after canceling the claim")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Export runtime Run did not return after shutdown")
	}
	select {
	case claimed := <-attempts.claims:
		t.Fatalf("job %s reached AttemptCoordinator.Claim after StopAccepting returned", claimed)
	default:
	}
}

func TestManagedExportRuntimeShutdownUsesDetachedContextForPostJoinSQLiteCleanup(t *testing.T) {
	db, ring, databasePath := exportRuntimeFileBackedKeyringFixture(t)
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = filepath.Join(t.TempDir(), "export")
	foundation := backupasset.NewFoundationService(exportRuntimeSettings(values))
	config, err := foundation.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	queued := newRuntimeExportDurableQueuedFixture(
		t, db, ring, config.Root, config, clock, runtimeExportDurableFrozenItems(clock, 1),
	)
	t.Cleanup(func() {
		if err := queued.store.Close(); err != nil {
			t.Errorf("close queued Export Store: %v", err)
		}
	})
	coordinator, err := assetexport.NewAttemptCoordinator(db, func() time.Time { return queued.clock }, queued.leases)
	if err != nil {
		t.Fatal(err)
	}
	attempts := &managedExportSQLiteBusyDrainAttempts{AttemptCoordinator: coordinator}
	delivery := &managedExportCleanupContextDeliveryFake{
		entered:           make(chan struct{}),
		reconcileContexts: make(chan managedExportCleanupContextObservation, 1),
	}
	budget := &managedExportSQLiteBusyDrainBudget{db: db, entered: make(chan struct{})}
	backendCalls := []string{}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &backendCalls},
		Lifecycle: managedExportLifecycleFake{}, Delivery: delivery, Budget: budget,
		Cadence: time.Hour, HeartbeatInterval: time.Hour, SourceLeaseInterval: time.Hour,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-sqlite-busy-shutdown",
	})
	if err != nil {
		t.Fatal(err)
	}

	joinedRunCtx, cancelJoinedRun := context.WithCancel(context.Background())
	joinedDone := make(chan struct{})
	go func() {
		<-joinedRunCtx.Done()
		close(joinedDone)
	}()
	cancelJoinedRun()
	select {
	case <-joinedDone:
	case <-time.After(time.Second):
		t.Fatal("managed Export worker did not enter its joined state")
	}
	runner.mu.Lock()
	runner.cancel = cancelJoinedRun
	runner.done = joinedDone
	runner.mu.Unlock()

	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: foundation, Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(context.Context, backupasset.ExportConfig, *assetexport.Store) (*managedExportGraph, error) {
			return nil, errors.New("unexpected Export runtime build")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := &managedExportGraph{
		store: queued.store, runner: runner, stopAccepting: runner.StopAccepting,
		drain: runner.Drain, shutdown: runner.Shutdown,
	}
	manager.mu.Lock()
	manager.graph = graph
	manager.config = config
	manager.mu.Unlock()
	manager.publication.publish(graph)
	manager.ready.Store(true)

	before := loadManagedExportDrainConservativeState(t, db, queued.jobID)
	primarySQL, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	primarySQL.SetMaxOpenConns(1)
	locker, err := database.Open(configpkg.Config{DBType: "sqlite", SQLitePath: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	lockerSQL, err := locker.DB()
	if err != nil {
		t.Fatal(err)
	}
	lockerSQL.SetMaxOpenConns(1)
	holder, err := lockerSQL.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var releaseHolderOnce sync.Once
	releaseHolder := func() {
		releaseHolderOnce.Do(func() {
			if _, err := holder.ExecContext(context.Background(), "ROLLBACK"); err != nil && !errors.Is(err, sql.ErrConnDone) {
				t.Errorf("release SQLite contention holder: %v", err)
			}
			if err := holder.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
				t.Errorf("close SQLite contention holder: %v", err)
			}
		})
	}
	t.Cleanup(func() {
		releaseHolder()
		if err := lockerSQL.Close(); err != nil {
			t.Errorf("close SQLite contention locker: %v", err)
		}
	})
	if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	cleanupWaitCtx, cancelCleanupWait := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelCleanupWait()
	delivery.waitFor = cleanupWaitCtx
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- manager.Shutdown(context.Background()) }()
	select {
	case <-delivery.entered:
	case err := <-shutdownDone:
		t.Fatalf("Export shutdown ended before post-join final cleanup: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Export shutdown did not start post-join final cleanup")
	}
	var cleanupContext managedExportCleanupContextObservation
	select {
	case cleanupContext = <-delivery.reconcileContexts:
	case err := <-shutdownDone:
		t.Fatalf("Export shutdown ended before post-join final cleanup: %v", err)
	case <-time.After(time.Second):
		t.Fatal("post-join final cleanup did not begin after the worker joined")
	}
	if cleanupContext.err != nil {
		t.Fatalf("post-join cleanup context err=%v, want active detached context", cleanupContext.err)
	}
	if !cleanupContext.hasDeadline || !cleanupContext.deadline.After(time.Now()) {
		t.Fatalf("post-join cleanup deadline=%v ok=%v, want a future deadline", cleanupContext.deadline, cleanupContext.hasDeadline)
	}
	select {
	case <-budget.entered:
	case <-time.After(time.Second):
		t.Fatal("post-join final cleanup did not reach the SQLite-contended reconciliation boundary")
	}
	select {
	case err := <-shutdownDone:
		var sqliteErr sqlite3.Error
		if !errors.As(err, &sqliteErr) || (sqliteErr.Code != sqlite3.ErrBusy && sqliteErr.Code != sqlite3.ErrLocked) {
			t.Fatalf("SQLite-busy post-join Export shutdown error=%v, want typed sqlite Busy/Locked", err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("SQLite-busy post-join Export shutdown preserved caller cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Export shutdown waited for SQLite busy_timeout during post-join cleanup")
	}
	releaseHolder()
	if manager.Ready() {
		t.Fatal("SQLite-busy post-join shutdown left Export runtime ready")
	}
	if manager.publication.current() != nil {
		t.Fatal("SQLite-busy post-join shutdown left Export publication available")
	}
	if runner.accepting.Load() {
		t.Fatal("SQLite-busy post-join shutdown left Export admission open")
	}
	if _, err := manager.Service().Create(context.Background(), assetexport.CreateRequest{}); !errors.Is(err, assetexport.ErrUnavailable) {
		t.Fatalf("SQLite-busy post-join shutdown service facade error=%v, want unavailable", err)
	}
	if attempts.claimCalls.Load() != 0 || len(backendCalls) != 0 {
		t.Fatalf("SQLite-busy post-join shutdown admitted work: claims=%d backend_calls=%v", attempts.claimCalls.Load(), backendCalls)
	}
	after := loadManagedExportDrainConservativeState(t, db, queued.jobID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("SQLite-busy post-join shutdown mutated conservative durable state: before=%+v after=%+v", before, after)
	}
}

func TestManagedExportWorkerReclaimsExpiredRunningJobAfterRestart(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	jobID, itemID := strings.Repeat("d", 32), strings.Repeat("e", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionRunning), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemRead),
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	attempts := &managedExportAttemptsFake{calls: &calls, claim: assetexport.AttemptClaim{
		AttemptID: strings.Repeat("f", 32), FenceToken: []byte(strings.Repeat("1", 32)),
	}}
	backend := &managedExportBackendFake{calls: &calls, seal: assetexport.PersistentSealResult{ArtifactID: strings.Repeat("2", 32)}}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 10, WorkerConcurrency: 1, WorkerOwner: "export-worker-restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claim:" + jobID, "spool:" + jobID + ":" + itemID,
		"heartbeat:" + jobID, "seal:" + jobID, "publish:" + jobID,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("restart execution calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerStartupPublishesCurrentSealedAttemptBeforeLeaseExpiry(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	config, err := backupasset.ExportConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	fixture := newRuntimeExportDurableQueuedFixture(
		t, db, ring, root, config, clock, runtimeExportDurableFrozenItems(clock, 1),
	)
	workerCapacity := assetexport.WorkerCapacityLimits{
		WorkerConcurrency: int64(config.WorkerConcurrency),
		UserActiveJobs:    int64(config.UserActiveJobs),
	}
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			_ = fixture.store.Close()
		}
	})

	initialBudget, err := assetexport.NewAttemptBudgetService(db, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	initialBroker, err := content.NewAttemptBroker(fixture.source, initialBudget, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	initialWorker, err := assetexport.NewPersistentWorker(assetexport.PersistentWorkerDependencies{
		DB: db, Keys: ring, Broker: initialBroker, Metadata: runtimeExportDurableMetadataValidator{}, Store: fixture.store,
		SourceLeases: fixture.leases, WorkerCapacity: &workerCapacity, AttemptWork: assetexport.NewAttemptWorkRegistry(),
		Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := assetexport.NewAttemptCoordinatorWithWorkerCapacity(
		db, func() time.Time { return fixture.clock }, workerCapacity, fixture.leases,
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := coordinator.Claim(context.Background(), assetexport.AttemptClaimRequest{
		JobID: fixture.jobID, WorkerOwner: "export-worker-sealed-before-restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetExportItem
	if err := db.Where("job_id = ?", fixture.jobID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := initialWorker.SpoolItem(context.Background(), assetexport.PersistentSpoolItemRequest{
		JobID: fixture.jobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: item.ID,
	}); err != nil {
		t.Fatal(err)
	}
	sealed, err := initialWorker.SealArchive(context.Background(), assetexport.PersistentSealRequest{
		JobID: fixture.jobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}

	var jobBefore model.BackupAssetExportJob
	var attemptBefore model.BackupAssetExportAttempt
	var artifactBefore model.BackupAssetExportArtifact
	var sourceBefore model.BackupAssetExportSourceLease
	var foundationLeaseBefore model.RecoveryPointLease
	if err := db.First(&jobBefore, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&attemptBefore, "id = ?", claim.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&artifactBefore, "id = ?", sealed.ArtifactID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", fixture.jobID).Take(&sourceBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&foundationLeaseBefore, "id = ?", sourceBefore.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if jobBefore.ExecutionState != string(assetexport.ExecutionSealing) || jobBefore.CurrentAttemptID == nil ||
		*jobBefore.CurrentAttemptID != claim.AttemptID || attemptBefore.State != string(assetexport.AttemptSealing) ||
		!attemptBefore.IsCurrent || !fixture.clock.Before(attemptBefore.LeaseExpiresAt) || artifactBefore.State != "sealed" ||
		artifactBefore.ExpiresAt != nil || !fixture.clock.Before(foundationLeaseBefore.LeaseExpiresAt) {
		t.Fatalf("invalid current sealing restart fixture: job=%+v attempt=%+v artifact=%+v source=%+v foundation_lease=%+v",
			jobBefore, attemptBefore, artifactBefore, sourceBefore, foundationLeaseBefore)
	}

	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	storeClosed = true
	restartedStore, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedStore.Close() })
	budget, err := assetexport.NewAttemptBudgetService(db, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	broker, err := content.NewAttemptBroker(fixture.source, budget, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	leases := &runtimeExportDurableLeaseSpy{LeaseService: fixture.leases}
	restartedWorker, err := assetexport.NewPersistentWorker(assetexport.PersistentWorkerDependencies{
		DB: db, Keys: ring, Broker: broker, Metadata: runtimeExportDurableMetadataValidator{}, Store: restartedStore,
		SourceLeases: leases, WorkerCapacity: &workerCapacity, AttemptWork: assetexport.NewAttemptWorkRegistry(),
		Now: func() time.Time { return fixture.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &managedExportSealedRecoveryBackend{PersistentWorker: restartedWorker}
	restartedCoordinator, err := assetexport.NewAttemptCoordinatorWithWorkerCapacity(
		db, func() time.Time { return fixture.clock }, workerCapacity, leases,
	)
	if err != nil {
		t.Fatal(err)
	}
	restartedAttempts := &managedExportSealedRecoveryAttempts{AttemptCoordinator: restartedCoordinator}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: restartedAttempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, HeartbeatInterval: time.Hour, SourceLeaseInterval: time.Hour,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-current-sealed-restart",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.Startup(context.Background()); err != nil {
		t.Fatalf("restart startup: %v", err)
	}
	if len(backend.reconcileRequests) != 1 || backend.reconcileRequests[0].JobID != fixture.jobID ||
		len(backend.reconcileResults) != 1 || backend.reconcileResults[0].Action != assetexport.PersistentReconcilePublished ||
		backend.reconcileResults[0].ArtifactID != sealed.ArtifactID {
		t.Fatalf("restart reconciliation requests=%+v results=%+v", backend.reconcileRequests, backend.reconcileResults)
	}
	if restartedAttempts.claimCalls != 0 || backend.discardCalls != 0 || leases.takeovers.Load() != 0 {
		t.Fatalf("current sealed attempt was reclaimed: claims=%d discard_calls=%d source_takeovers=%d",
			restartedAttempts.claimCalls, backend.discardCalls, leases.takeovers.Load())
	}

	var jobAfter model.BackupAssetExportJob
	var attemptAfter model.BackupAssetExportAttempt
	var artifactAfter model.BackupAssetExportArtifact
	var sourceAfter model.BackupAssetExportSourceLease
	var attemptCount int64
	if err := db.First(&jobAfter, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&attemptAfter, "id = ?", claim.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&artifactAfter, "id = ?", sealed.ArtifactID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", fixture.jobID).Take(&sourceAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetExportAttempt{}).Where("job_id = ?", fixture.jobID).Count(&attemptCount).Error; err != nil {
		t.Fatal(err)
	}
	if jobAfter.ExecutionState != string(assetexport.ExecutionReady) || jobAfter.CurrentAttemptID == nil ||
		*jobAfter.CurrentAttemptID != claim.AttemptID || jobAfter.ReadyAt == nil || jobAfter.ExpiresAt == nil ||
		attemptAfter.State != string(assetexport.AttemptSealed) || attemptAfter.IsCurrent || artifactAfter.State != "sealed" ||
		artifactAfter.ExpiresAt == nil || attemptCount != 1 || sourceAfter.LeaseAttemptID != sourceBefore.LeaseAttemptID ||
		sourceAfter.FenceHash != sourceBefore.FenceHash {
		t.Fatalf("restart publication did not preserve the current sealed lineage: job=%+v attempt=%+v artifact=%+v source_before=%+v source_after=%+v attempts=%d",
			jobAfter, attemptAfter, artifactAfter, sourceBefore, sourceAfter, attemptCount)
	}
}

func TestManagedExportWorkerStartupTakesOverSourceBeforeRebuildingExpiredSealing(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	jobID, itemID := strings.Repeat("8", 32), strings.Repeat("9", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionSealing), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	attempts := &managedExportExpiredSealingAttempts{calls: &calls, claim: assetexport.AttemptClaim{
		AttemptID: strings.Repeat("a", 32), SupersededAttemptID: strings.Repeat("b", 32),
		FenceToken: []byte(strings.Repeat("f", 32)),
	}}
	backend := &managedExportExpiredSealingBackend{calls: &calls, jobID: jobID}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-sealing-restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"takeover-source:" + jobID, "reconcile:" + jobID, "claim:" + jobID,
		"discard:" + jobID + ":" + attempts.claim.SupersededAttemptID,
		"spool:" + jobID + ":" + itemID, "heartbeat:" + jobID,
		"seal:" + jobID, "publish:" + jobID, "takeover-source:" + jobID,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("expired sealing recovery calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerHeartbeatsWhileItemSpoolIsBlocked(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}); err != nil {
		t.Fatal(err)
	}
	jobID, itemID := strings.Repeat("5", 32), strings.Repeat("6", 32)
	if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(assetexport.ExecutionQueued)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}
	attempts := &managedExportHeartbeatAttempts{
		claim:     assetexport.AttemptClaim{AttemptID: strings.Repeat("7", 32), FenceToken: []byte(strings.Repeat("a", 32))},
		heartbeat: make(chan struct{}, 1),
	}
	backend := &managedExportHeartbeatBackend{started: make(chan struct{}), release: make(chan struct{})}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, HeartbeatInterval: 5 * time.Millisecond, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-heartbeat",
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.executeJob(context.Background(), jobID) }()
	var release sync.Once
	unblock := func() { release.Do(func() { close(backend.release) }) }
	finished := false
	defer func() {
		if finished {
			return
		}
		unblock()
		<-done
	}()
	<-backend.started
	select {
	case <-attempts.heartbeat:
	case <-time.After(time.Second):
		t.Fatal("no heartbeat while item spool remained blocked")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	finished = true
}

func TestManagedExportWorkerHeartbeatsWhileReadyPublicationIsBlocked(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}); err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("d", 32)
	if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(assetexport.ExecutionQueued)}).Error; err != nil {
		t.Fatal(err)
	}
	attempts := &managedExportHeartbeatAttempts{
		claim:     assetexport.AttemptClaim{AttemptID: strings.Repeat("e", 32), FenceToken: []byte(strings.Repeat("f", 32))},
		heartbeat: make(chan struct{}, 1),
	}
	backend := &managedExportHeartbeatBackend{started: make(chan struct{}), release: make(chan struct{}), blockPublish: true}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, HeartbeatInterval: 5 * time.Millisecond, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-publish-heartbeat",
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.executeJob(context.Background(), jobID) }()
	var release sync.Once
	unblock := func() { release.Do(func() { close(backend.release) }) }
	finished := false
	defer func() {
		if finished {
			return
		}
		unblock()
		<-done
	}()
	<-backend.started
	select {
	case <-attempts.heartbeat:
	default:
	}
	select {
	case <-attempts.heartbeat:
	case <-time.After(time.Second):
		t.Fatal("no heartbeat while ready publication remained blocked")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	finished = true
}

func TestManagedExportWorkerPersistsFailureAndDiscardsAttemptBeforeRetry(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}); err != nil {
		t.Fatal(err)
	}
	jobID, itemID := strings.Repeat("9", 32), strings.Repeat("a", 32)
	if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(assetexport.ExecutionQueued)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	attempts := &managedExportFailureAttempts{calls: &calls, claim: assetexport.AttemptClaim{
		AttemptID: strings.Repeat("b", 32), FenceToken: []byte(strings.Repeat("c", 32)),
	}}
	backend := &managedExportFailureBackend{calls: &calls, failure: errors.New("injected spool failure")}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.executeJob(context.Background(), jobID); err == nil || !strings.Contains(err.Error(), "injected spool failure") {
		t.Fatalf("execute failure=%v", err)
	}
	want := []string{"claim:" + jobID, "spool:" + jobID, "fail:" + jobID, "discard:" + jobID}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("failure calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerContinuesAfterRecoverablePreHeaderSpoolFailure(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportItemAttempt{}); err != nil {
		t.Fatal(err)
	}
	jobID, itemID := strings.Repeat("9", 32), strings.Repeat("a", 32)
	if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(assetexport.ExecutionQueued)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	attempts := &managedExportPreHeaderAttempts{calls: &calls, claim: assetexport.AttemptClaim{
		AttemptID: strings.Repeat("b", 32), FenceToken: []byte(strings.Repeat("c", 32)),
	}, providerBytes: 37}
	if err := db.Create(&model.BackupAssetExportItemAttempt{
		ID: strings.Repeat("d", 32), JobID: jobID, ItemID: itemID, AttemptID: attempts.claim.AttemptID,
		State: string(assetexport.ItemPending), ProviderBytes: attempts.providerBytes,
	}).Error; err != nil {
		t.Fatal(err)
	}
	backend := &managedExportPreHeaderBackend{calls: &calls, failure: assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged)}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-pre-header",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.executeJob(context.Background(), jobID); err != nil {
		t.Fatalf("execute failure=%v", err)
	}
	want := []string{"claim:" + jobID, "spool:" + jobID, "checkpoint:" + itemID, "seal:" + jobID, "publish:" + jobID}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerContinuesAfterRecoverablePreHeaderSealFailure(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportItemAttempt{}); err != nil {
		t.Fatal(err)
	}
	jobID, itemID := strings.Repeat("9", 32), strings.Repeat("a", 32)
	readAt := time.Now().UTC()
	if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(assetexport.ExecutionQueued)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemRead),
		LogicalSize: 42, LogicalBytes: 42, ProviderBytes: 37,
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	attempts := &managedExportPreHeaderAttempts{calls: &calls, claim: assetexport.AttemptClaim{
		AttemptID: strings.Repeat("b", 32), FenceToken: []byte(strings.Repeat("c", 32)),
	}, providerBytes: 37, failureCategory: "internal_failure", recoveredSpool: true}
	if err := db.Create(&model.BackupAssetExportItemAttempt{
		ID: strings.Repeat("d", 32), JobID: jobID, ItemID: itemID, AttemptID: attempts.claim.AttemptID,
		State: string(assetexport.ItemRead), SpoolDigest: strings.Repeat("e", 64), SpoolSize: 96,
		SpoolLocator: strings.Repeat("f", 32) + ".xrs", LogicalBytes: 42, ProviderBytes: attempts.providerBytes,
		ReadAt: &readAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	backend := &managedExportPreHeaderBackend{
		calls: &calls,
		sealFailure: managedExportSealingPreHeaderFailure{
			cause: assetexport.NewPreHeaderSpoolFailure(assetexport.ErrCipherTampered), itemID: itemID,
		},
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-pre-header-seal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.executeJob(context.Background(), jobID); err != nil {
		t.Fatalf("execute failure=%v", err)
	}
	want := []string{
		"claim:" + jobID, "spool:" + jobID, "seal:" + jobID, "checkpoint:" + itemID,
		"seal:" + jobID, "publish:" + jobID,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerRecoversTamperedPersistedSpoolBeforeHeader(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	config, err := backupasset.ExportConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	fixture := newRuntimeExportDurableQueuedFixture(
		t, db, ring, root, config, clock, runtimeExportDurableFrozenItems(clock, 2),
	)
	t.Cleanup(func() { _ = fixture.store.Close() })

	attempts, err := assetexport.NewAttemptCoordinator(db, func() time.Time { return fixture.clock }, fixture.leases)
	if err != nil {
		t.Fatal(err)
	}
	backend := &managedExportPauseAfterSecondSpoolBackend{
		inner:       fixture.worker,
		secondSpool: make(chan assetexport.PersistentSpoolResult, 1),
		release:     make(chan struct{}),
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, HeartbeatInterval: time.Hour, SourceLeaseInterval: time.Hour,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-tampered-persisted-spool",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.executeJob(ctx, fixture.jobID) }()
	var release sync.Once
	unblock := func() { release.Do(func() { close(backend.release) }) }
	completed := false
	defer func() {
		if completed {
			return
		}
		unblock()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("managed Export worker did not stop after the tamper test cleanup")
		}
	}()

	var tampered assetexport.PersistentSpoolResult
	select {
	case tampered = <-backend.secondSpool:
	case <-time.After(time.Second):
		t.Fatal("managed Export worker did not persist the second spool")
	}
	tamperedPath := filepath.Join(root, tampered.Locator)
	spool, err := os.OpenFile(tamperedPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var firstByte [1]byte
	if _, err := spool.ReadAt(firstByte[:], 0); err != nil {
		_ = spool.Close()
		t.Fatal(err)
	}
	firstByte[0] ^= 0x80
	if _, err := spool.WriteAt(firstByte[:], 0); err != nil {
		_ = spool.Close()
		t.Fatal(err)
	}
	if err := errors.Join(spool.Sync(), spool.Close()); err != nil {
		t.Fatal(err)
	}

	unblock()
	executeErr := <-done
	completed = true
	if executeErr != nil {
		t.Fatalf("execute tampered persisted spool: %v", executeErr)
	}
	if backend.spoolCalls.Load() != 2 || backend.sealCalls.Load() != 2 || backend.publishCalls.Load() != 1 {
		t.Fatalf("worker calls spool=%d seal=%d publish=%d, want 2/2/1",
			backend.spoolCalls.Load(), backend.sealCalls.Load(), backend.publishCalls.Load())
	}
	if _, err := os.Lstat(tamperedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered spool remains after recovery: %v", err)
	}

	var job model.BackupAssetExportJob
	if err := db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionReady) || job.ResultKind != string(assetexport.ResultPartial) ||
		job.PackedCount != 1 || job.FailedCount != 1 || job.SkippedCount != 0 {
		t.Fatalf("recovered partial Export job=%+v", job)
	}
	var items []model.BackupAssetExportItem
	if err := db.Where("job_id = ?", fixture.jobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("recovered Export items=%+v", items)
	}
	var itemAttempts []model.BackupAssetExportItemAttempt
	if err := db.Where("job_id = ?", fixture.jobID).Find(&itemAttempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(itemAttempts) != 2 {
		t.Fatalf("recovered Export item attempts=%+v", itemAttempts)
	}
	attemptByItemID := make(map[string]model.BackupAssetExportItemAttempt, len(itemAttempts))
	for _, itemAttempt := range itemAttempts {
		attemptByItemID[itemAttempt.ItemID] = itemAttempt
	}
	packed, failed := 0, 0
	for _, item := range items {
		itemAttempt, found := attemptByItemID[item.ID]
		if !found {
			t.Fatalf("missing item-attempt for item=%s", item.ID)
		}
		if item.ID == tampered.ItemID {
			failed++
			if item.State != string(assetexport.ItemFailed) || item.LogicalBytes != 0 || item.ErrorCategory != "internal_failure" ||
				itemAttempt.State != string(assetexport.ItemFailed) || itemAttempt.LogicalBytes != 0 ||
				itemAttempt.ErrorCategory != "internal_failure" || itemAttempt.SpoolDigest != "" || itemAttempt.SpoolSize != 0 ||
				itemAttempt.SpoolLocator != "" || itemAttempt.ReadAt != nil || itemAttempt.PackedAt != nil || itemAttempt.FinishedAt == nil {
				t.Fatalf("tampered spool was not durably recovered item=%+v item_attempt=%+v", item, itemAttempt)
			}
			continue
		}
		packed++
		if item.State != string(assetexport.ItemPacked) || item.ErrorCategory != "" || itemAttempt.State != string(assetexport.ItemPacked) ||
			itemAttempt.ErrorCategory != "" {
			t.Fatalf("untampered spool was not packed item=%+v item_attempt=%+v", item, itemAttempt)
		}
	}
	if packed != 1 || failed != 1 {
		t.Fatalf("recovered item counts packed=%d failed=%d", packed, failed)
	}
	var artifact model.BackupAssetExportArtifact
	if err := db.Where("job_id = ?", fixture.jobID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != "sealed" || artifact.Locator == "" {
		t.Fatalf("recovered partial Export artifact=%+v", artifact)
	}
	sealed, err := fixture.store.OpenSealed(artifact.Locator)
	if err != nil {
		t.Fatalf("open recovered partial Export artifact: %v", err)
	}
	if err := sealed.Close(); err != nil {
		t.Fatalf("close recovered partial Export artifact: %v", err)
	}
}

func TestManagedExportWorkerRecoversAbsentPersistedSpoolBeforeHeader(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	config, err := backupasset.ExportConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	fixture := newRuntimeExportDurableQueuedFixture(
		t, db, ring, root, config, clock, runtimeExportDurableFrozenItems(clock, 2),
	)
	t.Cleanup(func() { _ = fixture.store.Close() })

	attempts, err := assetexport.NewAttemptCoordinator(db, func() time.Time { return fixture.clock }, fixture.leases)
	if err != nil {
		t.Fatal(err)
	}
	backend := &managedExportPauseAfterSecondSpoolBackend{
		inner:       fixture.worker,
		secondSpool: make(chan assetexport.PersistentSpoolResult, 1),
		release:     make(chan struct{}),
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, HeartbeatInterval: time.Hour, SourceLeaseInterval: time.Hour,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-absent-persisted-spool",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.executeJob(ctx, fixture.jobID) }()
	var release sync.Once
	unblock := func() { release.Do(func() { close(backend.release) }) }
	completed := false
	defer func() {
		if completed {
			return
		}
		unblock()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("managed Export worker did not stop after the absent spool test cleanup")
		}
	}()

	var absent assetexport.PersistentSpoolResult
	select {
	case absent = <-backend.secondSpool:
	case <-time.After(time.Second):
		t.Fatal("managed Export worker did not persist the second spool")
	}
	if !strings.HasSuffix(absent.Locator, ".xrs") {
		t.Fatalf("persisted spool locator=%q, want .xrs", absent.Locator)
	}
	absentPath := filepath.Join(root, absent.Locator)
	if err := os.Remove(absentPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(absentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted spool remains before seal: %v", err)
	}

	unblock()
	executeErr := <-done
	completed = true
	if executeErr != nil {
		t.Fatalf("execute absent persisted spool: %v", executeErr)
	}
	if backend.spoolCalls.Load() != 2 || backend.sealCalls.Load() != 2 || backend.publishCalls.Load() != 1 {
		t.Fatalf("worker calls spool=%d seal=%d publish=%d, want 2/2/1",
			backend.spoolCalls.Load(), backend.sealCalls.Load(), backend.publishCalls.Load())
	}

	var job model.BackupAssetExportJob
	if err := db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionReady) || job.ResultKind != string(assetexport.ResultPartial) ||
		job.PackedCount != 1 || job.FailedCount != 1 || job.SkippedCount != 0 || job.CurrentAttemptID == nil {
		t.Fatalf("recovered partial Export job=%+v", job)
	}
	var durableAttempts []model.BackupAssetExportAttempt
	if err := db.Where("job_id = ?", fixture.jobID).Order("attempt_number ASC").Find(&durableAttempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(durableAttempts) != 1 || durableAttempts[0].ID != *job.CurrentAttemptID ||
		durableAttempts[0].AttemptNumber != 1 || durableAttempts[0].State != string(assetexport.AttemptSealed) {
		t.Fatalf("pre-header absence retried or failed whole attempt: attempts=%+v job=%+v", durableAttempts, job)
	}

	var items []model.BackupAssetExportItem
	if err := db.Where("job_id = ?", fixture.jobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("recovered Export items=%+v", items)
	}
	var itemAttempts []model.BackupAssetExportItemAttempt
	if err := db.Where("job_id = ?", fixture.jobID).Find(&itemAttempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(itemAttempts) != 2 {
		t.Fatalf("recovered Export item attempts=%+v", itemAttempts)
	}
	attemptByItemID := make(map[string]model.BackupAssetExportItemAttempt, len(itemAttempts))
	for _, itemAttempt := range itemAttempts {
		attemptByItemID[itemAttempt.ItemID] = itemAttempt
	}
	packed, failed := 0, 0
	for _, item := range items {
		itemAttempt, found := attemptByItemID[item.ID]
		if !found {
			t.Fatalf("missing item-attempt for item=%s", item.ID)
		}
		if item.ID == absent.ItemID {
			failed++
			if item.State != string(assetexport.ItemFailed) || item.LogicalBytes != 0 ||
				item.ProviderBytes != absent.ProviderBytes || item.ErrorCategory != "internal_failure" ||
				itemAttempt.State != string(assetexport.ItemFailed) || itemAttempt.LogicalBytes != 0 ||
				itemAttempt.ProviderBytes != absent.ProviderBytes || itemAttempt.ErrorCategory != "internal_failure" ||
				itemAttempt.SpoolDigest != "" || itemAttempt.SpoolSize != 0 || itemAttempt.SpoolLocator != "" ||
				itemAttempt.ReadAt != nil || itemAttempt.PackedAt != nil || itemAttempt.FinishedAt == nil {
				t.Fatalf("absent spool was not durably recovered item=%+v item_attempt=%+v", item, itemAttempt)
			}
			continue
		}
		packed++
		if item.State != string(assetexport.ItemPacked) || item.ErrorCategory != "" ||
			itemAttempt.State != string(assetexport.ItemPacked) || itemAttempt.ErrorCategory != "" {
			t.Fatalf("remaining spool was not packed item=%+v item_attempt=%+v", item, itemAttempt)
		}
	}
	if packed != 1 || failed != 1 {
		t.Fatalf("recovered item counts packed=%d failed=%d", packed, failed)
	}

	var artifact model.BackupAssetExportArtifact
	if err := db.Where("job_id = ?", fixture.jobID).Take(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != "sealed" || artifact.Locator == "" || artifact.AttemptID != durableAttempts[0].ID {
		t.Fatalf("recovered partial Export artifact=%+v", artifact)
	}
	assertManagedExportPartialZIPArtifact(t, db, ring, fixture.store, job, durableAttempts[0], artifact, absent.ItemID, absent.ProviderBytes)
}

func assertManagedExportPartialZIPArtifact(
	t *testing.T,
	db *gorm.DB,
	ring *backupasset.Keyring,
	store *assetexport.Store,
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	artifact model.BackupAssetExportArtifact,
	failedItemID string,
	failedProviderBytes int64,
) {
	t.Helper()
	var key model.BackupAssetExportKey
	if err := db.Where("id = ? AND job_id = ?", artifact.JobKeyID, job.ID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	material, err := ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, key.KEKVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(material.Key)
	dek, err := assetexport.UnwrapJobDEK(assetexport.JobKeyBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, KEKVersion: key.KEKVersion, WrapAlgorithm: key.WrapAlgorithm,
	}, material.Key, assetexport.JobKeyEnvelope{Nonce: key.EnvelopeNonce, Ciphertext: key.WrappedDEK})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(dek)
	sealed, err := store.OpenSealed(artifact.Locator)
	if err != nil {
		t.Fatal(err)
	}
	var plaintext bytes.Buffer
	_, decryptErr := assetexport.DecryptStream(context.Background(), &plaintext, sealed, dek, assetexport.CipherBinding{
		ExportID: job.ID, SelectionDigest: job.SelectionDigest, ArchiveProfile: job.ArchiveProfile,
		FormatVersion: artifact.FormatVersion, AttemptFenceDigest: attempt.FenceDigest,
		Purpose: assetexport.CipherPurposeFinalArchive,
	})
	closeErr := sealed.Close()
	if err := errors.Join(decryptErr, closeErr); err != nil {
		t.Fatalf("decrypt recovered partial Export artifact: %v", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(plaintext.Bytes()), int64(plaintext.Len()))
	if err != nil {
		t.Fatalf("open recovered partial Export ZIP: %v", err)
	}
	if len(archive.File) != 2 {
		t.Fatalf("recovered partial Export ZIP members=%d, want packed file plus report", len(archive.File))
	}
	var report assetexport.ArchiveReport
	reportFound := false
	for _, file := range archive.File {
		if file.Name != "xirang-export-report.v1.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		decodeErr := json.NewDecoder(reader).Decode(&report)
		closeErr := reader.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			t.Fatalf("decode recovered partial Export report: %v", err)
		}
		reportFound = true
	}
	if !reportFound || report.SchemaVersion != 1 || report.SelectionDigest != job.SelectionDigest ||
		report.ResultKind != assetexport.ResultPartial || report.Packed != 1 || report.Failed != 1 || report.Skipped != 0 ||
		len(report.Items) != 2 {
		t.Fatalf("recovered partial Export report=%+v", report)
	}
	for _, item := range report.Items {
		if item.ItemID != failedItemID {
			continue
		}
		if item.State != assetexport.ItemFailed || item.MemberPath != "" || item.LogicalBytes != 0 ||
			item.ProviderBytes != failedProviderBytes || item.ErrorCategory != "internal_failure" {
			t.Fatalf("recovered absent spool report item=%+v", item)
		}
		return
	}
	t.Fatalf("recovered partial Export report omitted failed item=%s", failedItemID)
}

func TestManagedExportWorkerPreHeaderCheckpointAcceptsOnlyCleanPendingAttempt(t *testing.T) {
	t.Run("clean pending", func(t *testing.T) {
		fixture := newManagedExportPreHeaderCheckpointFixture(t)
		checkpointErr, handled := fixture.runner.checkpointPreHeaderSpoolFailure(
			context.Background(), fixture.jobID, fixture.attempts.claim, fixture.itemID,
			assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), false,
		)
		if !handled || checkpointErr != nil {
			t.Fatalf("clean pending checkpoint handled=%v error=%v", handled, checkpointErr)
		}
		want := []string{"checkpoint:" + fixture.itemID}
		if !reflect.DeepEqual(*fixture.attempts.calls, want) {
			t.Fatalf("clean pending calls=%v want=%v", *fixture.attempts.calls, want)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*managedExportPreHeaderCheckpointFixture) error
	}{
		{
			name: "pending spool evidence",
			mutate: func(fixture *managedExportPreHeaderCheckpointFixture) error {
				return fixture.db.Model(&model.BackupAssetExportItemAttempt{}).
					Where("job_id = ? AND attempt_id = ? AND item_id = ?", fixture.jobID, fixture.attempts.claim.AttemptID, fixture.itemID).
					Updates(map[string]any{"spool_digest": strings.Repeat("a", 64), "spool_size": int64(96)}).Error
			},
		},
		{
			name: "non pending",
			mutate: func(fixture *managedExportPreHeaderCheckpointFixture) error {
				return fixture.db.Model(&model.BackupAssetExportItemAttempt{}).
					Where("job_id = ? AND attempt_id = ? AND item_id = ?", fixture.jobID, fixture.attempts.claim.AttemptID, fixture.itemID).
					Update("state", string(assetexport.ItemRead)).Error
			},
		},
		{
			name: "duplicate rows",
			mutate: func(fixture *managedExportPreHeaderCheckpointFixture) error {
				return fixture.db.Create(&model.BackupAssetExportItemAttempt{
					ID: strings.Repeat("e", 32), JobID: fixture.jobID, ItemID: fixture.itemID,
					AttemptID: fixture.attempts.claim.AttemptID, State: string(assetexport.ItemPending),
					ProviderBytes: fixture.attempts.providerBytes,
				}).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagedExportPreHeaderCheckpointFixture(t)
			if err := test.mutate(&fixture); err != nil {
				t.Fatal(err)
			}
			before := managedExportPreHeaderCheckpointRows(t, fixture)
			checkpointErr, handled := fixture.runner.checkpointPreHeaderSpoolFailure(
				context.Background(), fixture.jobID, fixture.attempts.claim, fixture.itemID,
				assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), false,
			)
			if !handled || !errors.Is(checkpointErr, assetexport.ErrUnavailable) {
				t.Fatalf("invalid pre-header attempt handled=%v error=%v, want ErrUnavailable", handled, checkpointErr)
			}
			if len(*fixture.attempts.calls) != 0 {
				t.Fatalf("invalid pre-header attempt checkpointed=%v", *fixture.attempts.calls)
			}
			after := managedExportPreHeaderCheckpointRows(t, fixture)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid pre-header attempt mutated rows before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestManagedExportWorkerPreHeaderCheckpointPreservesDatabaseCause(t *testing.T) {
	fixture := newManagedExportPreHeaderCheckpointFixture(t)
	injected := errors.New("injected pre-header checkpoint database failure")
	callbackName := "test:managed-export-pre-header-checkpoint-db-error-" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "backup_asset_export_item_attempts" {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	checkpointErr, handled := fixture.runner.checkpointPreHeaderSpoolFailure(
		context.Background(), fixture.jobID, fixture.attempts.claim, fixture.itemID,
		assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), false,
	)
	if removeErr := fixture.db.Callback().Query().Remove(callbackName); removeErr != nil {
		t.Fatal(removeErr)
	}
	if !handled || !errors.Is(checkpointErr, assetexport.ErrUnavailable) || !errors.Is(checkpointErr, injected) {
		t.Fatalf("database checkpoint handled=%v error=%v, want joined unavailable and injected cause", handled, checkpointErr)
	}
	if len(*fixture.attempts.calls) != 0 {
		t.Fatalf("database checkpoint called=%v", *fixture.attempts.calls)
	}
}

func TestManagedExportWorkerPreHeaderCheckpointPreservesCoordinatorDatabaseFailure(t *testing.T) {
	fixture := newManagedExportPreHeaderCheckpointFixture(t)
	injected := errors.New("injected coordinator item-attempt update failure")
	fixture.attempts.checkpointErr = errors.Join(assetexport.ErrUnavailable, injected)

	checkpointErr, handled := fixture.runner.checkpointPreHeaderSpoolFailure(
		context.Background(), fixture.jobID, fixture.attempts.claim, fixture.itemID,
		assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), false,
	)
	if !handled || !errors.Is(checkpointErr, assetexport.ErrUnavailable) || !errors.Is(checkpointErr, injected) {
		t.Fatalf("coordinator database checkpoint handled=%v error=%v, want joined unavailable and injected cause", handled, checkpointErr)
	}
	want := []string{"checkpoint:" + fixture.itemID}
	if !reflect.DeepEqual(*fixture.attempts.calls, want) {
		t.Fatalf("coordinator database checkpoint calls=%v want=%v", *fixture.attempts.calls, want)
	}
}

func TestManagedExportWorkerDoesNotRecoverAttemptFatalPreHeaderMarkers(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "context canceled", cause: errors.Join(assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), context.Canceled)},
		{name: "Export quota", cause: errors.Join(assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), assetexport.ErrQuotaExceeded)},
		{name: "Export fence", cause: errors.Join(assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), assetexport.ErrAttemptFenceLost)},
		{name: "raw Foundation fence", cause: errors.Join(assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), backupasset.ErrLeaseFenceLost)},
		{name: "wrapped Foundation fence", cause: errors.Join(assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), fmt.Errorf("wrapped Foundation fence: %w", backupasset.ErrLeaseFenceLost))},
		{name: "raw Foundation deadline", cause: errors.Join(assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), backupasset.ErrLeaseDeadlineExceeded)},
		{name: "wrapped Foundation deadline", cause: errors.Join(assetexport.NewPreHeaderSpoolFailure(content.ErrAttemptSourceChanged), fmt.Errorf("wrapped Foundation deadline: %w", backupasset.ErrLeaseDeadlineExceeded))},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := exportRuntimeKeyringFixture(t)
			if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportItemAttempt{}); err != nil {
				t.Fatal(err)
			}
			jobID, itemID := strings.Repeat("9", 32), strings.Repeat("a", 32)
			if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(assetexport.ExecutionQueued)}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.BackupAssetExportItem{
				ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
				PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
			}).Error; err != nil {
				t.Fatal(err)
			}
			calls := []string{}
			attempts := &managedExportPreHeaderAttempts{calls: &calls, claim: assetexport.AttemptClaim{
				AttemptID: strings.Repeat("b", 32), FenceToken: []byte(strings.Repeat("c", 32)),
			}}
			if err := db.Create(&model.BackupAssetExportItemAttempt{
				ID: strings.Repeat("d", 32), JobID: jobID, ItemID: itemID, AttemptID: attempts.claim.AttemptID,
				State: string(assetexport.ItemPending),
			}).Error; err != nil {
				t.Fatal(err)
			}
			backend := &managedExportPreHeaderBackend{calls: &calls, failure: test.cause}
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: attempts, Worker: backend,
				Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
				Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-pre-header-fatal",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.executeJob(context.Background(), jobID); err == nil || !errors.Is(err, test.cause) {
				t.Fatalf("execute error=%v, want cause=%v", err, test.cause)
			}
			want := []string{"claim:" + jobID, "spool:" + jobID, "fail:" + jobID, "discard:" + jobID}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

func TestManagedExportWorkerStartupReconcilesExpiredReaderReservations(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	budget := &managedExportBudgetFake{}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: &managedExportAttemptsFake{calls: &[]string{}},
		Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: managedExportLifecycleFake{},
		Delivery: managedExportDeliveryFake{}, Budget: budget,
		Cadence: time.Minute, BatchSize: 7, WorkerConcurrency: 1, WorkerOwner: "export-worker-startup-budget",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if budget.calls.Load() != 1 || budget.limit.Load() != 7 {
		t.Fatalf("reader reconciliation calls=%d limit=%d", budget.calls.Load(), budget.limit.Load())
	}
}

func TestManagedExportWorkerStartupRetriesRetiredAttemptCleanupAfterFailure(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	jobID, itemID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	attemptID, itemAttemptID := strings.Repeat("3", 32), strings.Repeat("4", 32)
	artifactID, keyID := strings.Repeat("5", 32), strings.Repeat("6", 32)
	spoolLocator, artifactLocator := strings.Repeat("7", 32)+".xrs", strings.Repeat("8", 32)+".xre"
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionFailed), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemFailed),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := now
	if err := db.Create(&model.BackupAssetExportAttempt{
		ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerOwner: "retired-worker",
		State: string(assetexport.AttemptSuperseded), FenceToken: bytes.Repeat([]byte{1}, 32),
		FenceDigest: strings.Repeat("9", 64), NoncePrefix: bytes.Repeat([]byte{2}, 8),
		LeaseExpiresAt: now.Add(-time.Minute), IsCurrent: false, StartedAt: now.Add(-time.Hour),
		FinishedAt: &finishedAt, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", attemptID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItemAttempt{
		ID: itemAttemptID, JobID: jobID, ItemID: itemID, AttemptID: attemptID,
		State: string(assetexport.ItemFailed), SpoolDigest: strings.Repeat("a", 64), SpoolSize: 128,
		SpoolLocator: spoolLocator, ErrorCategory: "archive_failed", StartedAt: now.Add(-time.Hour),
		FinishedAt: &finishedAt, CreatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportArtifact{
		ID: artifactID, JobID: jobID, AttemptID: attemptID, JobKeyID: keyID,
		State: "sealed", Locator: artifactLocator, CipherVersion: 1, ChunkBytes: 64 * 1024,
		FormatVersion: 1, NoncePrefix: bytes.Repeat([]byte{3}, 8), ChunkCount: 1,
		PlaintextDigest: strings.Repeat("b", 64), ArchiveDigest: strings.Repeat("c", 64),
		CiphertextDigest: strings.Repeat("d", 64), PlaintextSize: 64, CiphertextSize: 128,
		SealedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	backend := &managedExportRetirementBackend{db: db, failOnce: errors.New("injected retirement failure")}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: &managedExportSourceMaintenanceFake{}, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 10, WorkerConcurrency: 1, WorkerOwner: "export-worker-retirement-retry",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Startup(context.Background()); !errors.Is(err, backend.failOnce) {
		t.Fatalf("first startup retirement error=%v want=%v", err, backend.failOnce)
	}
	if err := runner.Startup(context.Background()); err != nil {
		t.Fatalf("retry startup retirement: %v", err)
	}
	if err := runner.Startup(context.Background()); err != nil {
		t.Fatalf("converged startup retirement: %v", err)
	}
	if !reflect.DeepEqual(backend.attemptIDs, []string{attemptID, attemptID}) {
		t.Fatalf("retirement attempts=%v want failed attempt plus one retry", backend.attemptIDs)
	}
	var storedAttempt model.BackupAssetExportAttempt
	var storedItemAttempt model.BackupAssetExportItemAttempt
	if err := db.First(&storedAttempt, "id = ?", attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&storedItemAttempt, "id = ?", itemAttemptID).Error; err != nil {
		t.Fatal(err)
	}
	var artifactCount int64
	if err := db.Model(&model.BackupAssetExportArtifact{}).Where("id = ?", artifactID).Count(&artifactCount).Error; err != nil {
		t.Fatal(err)
	}
	if storedAttempt.StagingLocator != "" || storedItemAttempt.SpoolLocator != "" || artifactCount != 0 {
		t.Fatalf("retirement did not converge: attempt=%+v item_attempt=%+v artifacts=%d",
			storedAttempt, storedItemAttempt, artifactCount)
	}
}

func TestManagedExportWorkerStartupMaintainsEverySourceProtectedState(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	queuedID, runningID := strings.Repeat("3", 32), strings.Repeat("4", 32)
	retryID, sealingID, readyID := strings.Repeat("5", 32), strings.Repeat("6", 32), strings.Repeat("7", 32)
	for _, job := range []model.BackupAssetExportJob{
		{ID: queuedID, ExecutionState: string(assetexport.ExecutionQueued)},
		{ID: runningID, ExecutionState: string(assetexport.ExecutionRunning)},
		{ID: retryID, ExecutionState: string(assetexport.ExecutionRetryWait)},
		{ID: sealingID, ExecutionState: string(assetexport.ExecutionSealing)},
		{ID: readyID, ExecutionState: string(assetexport.ExecutionReady)},
	} {
		createManagedExportRuntimeJob(t, db, &job)
	}
	attempts := &managedExportSourceMaintenanceFake{}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &[]string{}},
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 7, WorkerConcurrency: 1, WorkerOwner: "export-worker-source-maintenance",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attempts.maintained, []string{queuedID, runningID, retryID, sealingID, readyID}) {
		t.Fatalf("maintained jobs=%v", attempts.maintained)
	}
}

func TestManagedExportWorkerRunMaintainsSourceLeasesAtHeartbeatCadence(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}); err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("8", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionQueued),
	}).Error; err != nil {
		t.Fatal(err)
	}
	attempts := &managedExportRunSourceMaintenanceFake{maintained: make(chan string, 1)}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &[]string{}},
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, HeartbeatInterval: time.Hour, SourceLeaseInterval: 5 * time.Millisecond,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-source-heartbeat",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("managed Export worker did not stop")
		}
	}()

	select {
	case maintained := <-attempts.maintained:
		if maintained != jobID {
			t.Fatalf("maintained job=%q want=%q", maintained, jobID)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("source lease maintenance waited for the GC cadence")
	}
}

func TestManagedExportWorkerSourceMaintenanceAdvancesPastTransientFailure(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	firstID, secondID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	for _, jobID := range []string{firstID, secondID} {
		createManagedExportRuntimeJob(t, db, &model.BackupAssetExportJob{
			ID: jobID, ExecutionState: string(assetexport.ExecutionQueued), CreatedAt: now, UpdatedAt: now,
		})
	}
	seedSourceLease := func(jobID, sourceID, leaseID, recoveryPointID string, expiry time.Time) {
		t.Helper()
		attemptID := strings.Repeat("3", 32)
		fenceToken := strings.Repeat("4", 64)
		deadline := now.Add(time.Hour)
		if err := db.Create(&model.RecoveryPointLease{
			ID: leaseID, RecoveryPointID: recoveryPointID, HolderType: string(backupasset.LeaseHolderExportJob),
			OwnerID: jobID, AttemptID: attemptID, FenceToken: fenceToken, Status: string(backupasset.LeaseActive),
			LeaseExpiresAt: expiry, AbsoluteDeadline: deadline, LastHeartbeatAt: now, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.BackupAssetExportSourceLease{
			ID: sourceID, JobID: jobID, RecoveryPointID: recoveryPointID, LeaseID: leaseID,
			LeaseAttemptID: attemptID, FenceHash: strings.Repeat("5", 64), AbsoluteDeadline: deadline,
			State: "active", AcquiredAt: now, RenewedAt: now, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedSourceLease(firstID, strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), now.Add(time.Minute))
	seedSourceLease(secondID, strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 32), now.Add(2*time.Minute))
	transient := errors.New("injected transient source maintenance failure")
	attempts := &managedExportFairSourceMaintenanceFake{failOnceFor: firstID, failure: transient}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &[]string{}},
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, HeartbeatInterval: time.Hour, SourceLeaseInterval: time.Hour,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-source-fairness",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.maintainSourceLeases(context.Background()); !errors.Is(err, transient) {
		t.Fatalf("first source maintenance error=%v want=%v", err, transient)
	}
	if err := runner.maintainSourceLeases(context.Background()); err != nil {
		t.Fatalf("second source maintenance error=%v", err)
	}
	if err := runner.maintainSourceLeases(context.Background()); err != nil {
		t.Fatalf("source maintenance after sweep reset error=%v", err)
	}
	if !slices.Equal(attempts.maintained, []string{firstID, secondID, firstID}) {
		t.Fatalf("source maintenance order=%v want=[%s %s %s]", attempts.maintained, firstID, secondID, firstID)
	}
}

func TestManagedExportWorkerSourceMaintenancePrioritizesEarliestLeaseExpiry(t *testing.T) {
	db, ring := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLease{}, &model.WrappedDomainKey{},
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportAttempt{}, &model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportSourceLease{},
		&model.BackupAssetExportArtifact{}, &model.BackupAssetExportIdempotency{}, &model.BackupAssetExportQuotaBucket{},
		&model.BackupAssetExportReservation{}, &model.BackupAssetExportDeliveryGrant{},
		&model.BackupAssetExportDeliveryRequest{}, &model.BackupAssetArchiveMemberRequest{},
	); err != nil {
		t.Fatal(err)
	}

	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = filepath.Join(t.TempDir(), "export")
	values["backup_assets.export.user_active_jobs"] = "3"
	values["backup_assets.export.user_store_quota"] = "64424509440"
	config, err := backupasset.ExportConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC().Truncate(time.Second)
	leases, err := backupasset.NewLeaseService(db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: config.LeaseTTL, Heartbeat: config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainExportStore); err != nil {
		t.Fatal(err)
	}
	serviceConfig := runtimeExportServiceConfig(config)
	service, err := assetexport.NewService(assetexport.ServiceDependencies{
		DB: db, Now: func() time.Time { return clock }, Leases: leases, Keys: ring,
		Resolver: runtimeExportDurableSelectionResolver{}, Config: serviceConfig,
	})
	if err != nil {
		t.Fatal(err)
	}

	createJob := func(recoveryPointID, entryID, idempotencyKey string) string {
		t.Helper()
		retention := clock.Add(6 * time.Hour)
		item := runtimeExportDurableFrozenItems(clock, 1)[0]
		item.Ref.RecoveryPointID = recoveryPointID
		item.Ref.EntryID = entryID
		item.RetentionUntil = &retention
		if err := db.Create(&model.RecoveryPoint{
			ID: recoveryPointID, RepositoryID: strings.Repeat("9", 32),
			State: string(backupasset.RecoveryPointCommitted), Semantics: string(backupasset.PointNativeSnapshot),
			SourceFingerprint: item.SourceFingerprint, CapabilityRevision: int(item.ProviderCapabilityRevision),
			PhysicalAvailability: string(backupasset.PhysicalOnline),
			ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
			HoldState:            string(backupasset.HoldNone), RetentionUntil: &retention, CreatedAt: clock, UpdatedAt: clock,
		}).Error; err != nil {
			t.Fatal(err)
		}
		selection, err := assetexport.FreezeSelection([]assetexport.FrozenItem{item}, nil, serviceConfig.Selection)
		if err != nil {
			t.Fatal(err)
		}
		created, err := service.CommitCreate(context.Background(), assetexport.CommitCreateRequest{
			Actor: assetexport.SelectionActor{UserID: 100, Role: "admin"}, Selection: selection,
			IdempotencyKey: idempotencyKey, ArchiveFormat: assetexport.ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		return created.JobID
	}

	jobIDs := []string{
		createJob(strings.Repeat("1", 32), strings.Repeat("a", 64), "source-maintenance-urgency-a"),
		createJob(strings.Repeat("2", 32), strings.Repeat("b", 64), "source-maintenance-urgency-b"),
		createJob(strings.Repeat("3", 32), strings.Repeat("c", 64), "source-maintenance-urgency-c"),
	}
	slices.Sort(jobIDs)
	firstJobID, middleJobID, lastJobID := jobIDs[0], jobIDs[1], jobIDs[2]
	sourceLeaseInterval := config.LeaseRenewMargin
	initialFirstExpiry := clock.Add(2 * sourceLeaseInterval)
	initialMiddleExpiry := clock.Add(2 * config.LeaseTTL)
	initialLastExpiry := clock.Add(sourceLeaseInterval / 2)
	firstLastRenewal := clock.Add(config.LeaseTTL)

	setSourceExpiry := func(jobID string, expiry time.Time) string {
		t.Helper()
		var source model.BackupAssetExportSourceLease
		if err := db.Where("job_id = ?", jobID).Take(&source).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.RecoveryPointLease{}).Where("id = ?", source.LeaseID).
			Updates(map[string]any{"lease_expires_at": expiry.UTC()}).Error; err != nil {
			t.Fatal(err)
		}
		return source.LeaseID
	}
	firstLeaseID := setSourceExpiry(firstJobID, initialFirstExpiry)
	middleLeaseID := setSourceExpiry(middleJobID, initialMiddleExpiry)
	lastLeaseID := setSourceExpiry(lastJobID, initialLastExpiry)
	loadLease := func(leaseID string) model.RecoveryPointLease {
		t.Helper()
		var lease model.RecoveryPointLease
		if err := db.First(&lease, "id = ?", leaseID).Error; err != nil {
			t.Fatal(err)
		}
		return lease
	}

	attempts, err := assetexport.NewAttemptCoordinator(db, func() time.Time { return clock }, leases)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &[]string{}},
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, HeartbeatInterval: time.Hour, SourceLeaseInterval: sourceLeaseInterval,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-source-urgency",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.maintainSourceLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := loadLease(lastLeaseID).LeaseExpiresAt.UTC(), firstLastRenewal; !got.Equal(want) {
		t.Fatalf("nearest source lease expiry after first maintenance=%s want renewed %s", got, want)
	}
	if got := loadLease(firstLeaseID).LeaseExpiresAt.UTC(); !got.Equal(initialFirstExpiry) {
		t.Fatalf("later source lease renewed before its turn: got=%s want=%s", got, initialFirstExpiry)
	}
	if got := loadLease(middleLeaseID).LeaseExpiresAt.UTC(); !got.Equal(initialMiddleExpiry) {
		t.Fatalf("furthest source lease renewed before its turn: got=%s want=%s", got, initialMiddleExpiry)
	}

	clock = clock.Add(sourceLeaseInterval)
	if err := runner.maintainSourceLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := loadLease(firstLeaseID).LeaseExpiresAt.UTC(), clock.Add(config.LeaseTTL); !got.Equal(want) {
		t.Fatalf("later source lease expiry after next maintenance=%s want renewed %s", got, want)
	}
	if got := loadLease(middleLeaseID).LeaseExpiresAt.UTC(); !got.Equal(initialMiddleExpiry) {
		t.Fatalf("furthest source lease renewed before its turn: got=%s want=%s", got, initialMiddleExpiry)
	}

	clock = clock.Add(sourceLeaseInterval)
	if err := runner.maintainSourceLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := loadLease(middleLeaseID).LeaseExpiresAt.UTC(), clock.Add(config.LeaseTTL); !got.Equal(want) {
		t.Fatalf("furthest source lease expiry after completing the sweep=%s want renewed %s", got, want)
	}
	if got, want := loadLease(lastLeaseID).LeaseExpiresAt.UTC(), firstLastRenewal; !got.Equal(want) {
		t.Fatalf("nearest source lease was renewed again before the sweep completed: got=%s want=%s", got, want)
	}

	clock = clock.Add(sourceLeaseInterval)
	if err := runner.maintainSourceLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := loadLease(lastLeaseID).LeaseExpiresAt.UTC(), clock.Add(config.LeaseTTL); !got.Equal(want) {
		t.Fatalf("new source-maintenance sweep did not restart with the nearest lease: got=%s want=%s", got, want)
	}
}

func TestManagedExportWorkerHandsOffReadySourceLeaseImmediatelyAfterPublication(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}); err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("9", 32)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, ExecutionState: string(assetexport.ExecutionQueued),
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	attempts := &managedExportPublicationHandoffAttempts{
		calls: &calls,
		claim: assetexport.AttemptClaim{AttemptID: strings.Repeat("a", 32), FenceToken: []byte(strings.Repeat("b", 32))},
	}
	backend := &managedExportPublicationHandoffBackend{
		managedExportBackendFake: managedExportBackendFake{
			calls: &calls,
			seal:  assetexport.PersistentSealResult{ArtifactID: strings.Repeat("c", 32)},
		},
		db: db,
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: backend,
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Hour, HeartbeatInterval: time.Hour,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-ready-handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.executeJob(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}
	want := []string{"claim:" + jobID, "seal:" + jobID, "publish:" + jobID, "verify:" + jobID, "maintain:" + jobID}
	if !slices.Equal(calls, want) {
		t.Fatalf("publication handoff calls=%v want=%v", calls, want)
	}
}

func TestManagedExportWorkerReadyVerificationPrecedesSourceMaintenance(t *testing.T) {
	verificationFailure := errors.New("injected ready verification infrastructure failure")
	tests := []struct {
		name         string
		state        assetexport.ExecutionState
		verification assetexport.PersistentReconcileResult
		verifyErr    error
		wantCalls    []string
		wantErr      error
	}{
		{
			name: "valid ready", state: assetexport.ExecutionReady,
			verification: assetexport.PersistentReconcileResult{
				ReadyIntegrity: &assetexport.ReadyIntegrityToken{},
			},
			wantCalls: []string{"verify", "maintain"},
		},
		{
			name: "revoked ready", state: assetexport.ExecutionReady,
			verification: assetexport.PersistentReconcileResult{Action: assetexport.PersistentReconcileRevoked},
			wantCalls:    []string{"verify"},
		},
		{
			name: "verification failure", state: assetexport.ExecutionReady,
			verifyErr: verificationFailure, wantCalls: []string{"verify"}, wantErr: verificationFailure,
		},
		{
			name: "ready expired", state: assetexport.ExecutionReady,
			verifyErr: assetexport.ErrReadyExpired, wantCalls: []string{"verify"},
		},
		{
			name: "queued remains maintenance only", state: assetexport.ExecutionQueued,
			wantCalls: []string{"maintain"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, _ := exportRuntimeKeyringFixture(t)
			if err := db.AutoMigrate(&model.BackupAssetExportJob{}); err != nil {
				t.Fatal(err)
			}
			jobID := fmt.Sprintf("%032x", len(test.name)+500)
			if err := db.Create(&model.BackupAssetExportJob{
				ID: jobID, ExecutionState: string(test.state),
			}).Error; err != nil {
				t.Fatal(err)
			}
			calls := []string{}
			attempts := &managedExportReadyVerificationAttempts{calls: &calls}
			backend := &managedExportReadyVerificationBackend{
				managedExportBackendFake: managedExportBackendFake{calls: &calls},
				calls:                    &calls,
				result:                   test.verification,
				err:                      test.verifyErr,
			}
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: attempts, Worker: backend, Lifecycle: managedExportLifecycleFake{},
				Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{}, Cadence: time.Minute,
				BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "ready-verification-order",
			})
			if err != nil {
				t.Fatal(err)
			}
			err = runner.maintainSourceLeases(context.Background())
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("maintenance error=%v, want %v", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatalf("maintenance error=%v", err)
			}
			if !slices.Equal(calls, test.wantCalls) {
				t.Fatalf("calls=%v want=%v", calls, test.wantCalls)
			}
			if test.state == assetexport.ExecutionReady && test.verifyErr == nil &&
				test.verification.Action == "" && attempts.readyIntegrity != test.verification.ReadyIntegrity {
				t.Fatalf("ready integrity token was not propagated: got=%p want=%p", attempts.readyIntegrity, test.verification.ReadyIntegrity)
			}
		})
	}
}

func TestManagedExportWorkerClassifiesSourceMaintenanceFailures(t *testing.T) {
	for _, test := range []struct {
		name              string
		state             assetexport.ExecutionState
		maintenanceErr    error
		wantSourceExpired bool
		wantCategory      string
		wantReturned      bool
	}{
		{name: "source deadline", state: assetexport.ExecutionQueued, maintenanceErr: assetexport.ErrSourceDeadlineReached, wantSourceExpired: true},
		{name: "execution deadline", state: assetexport.ExecutionQueued, maintenanceErr: assetexport.ErrExecutionDeadlineReached, wantCategory: "deadline"},
		{name: "ready expiry", state: assetexport.ExecutionReady, maintenanceErr: assetexport.ErrReadyExpired},
		{name: "malformed ready aggregate", state: assetexport.ExecutionReady, maintenanceErr: assetexport.ErrUnavailable, wantCategory: "internal_failure"},
		{name: "ready key unavailable", state: assetexport.ExecutionReady, maintenanceErr: errors.Join(assetexport.ErrAttemptFenceLost, assetexport.ErrUnavailable), wantCategory: "internal_failure"},
		{name: "ready source fence loss", state: assetexport.ExecutionReady, maintenanceErr: assetexport.ErrAttemptFenceLost, wantSourceExpired: true},
		{name: "ready artifact missing", state: assetexport.ExecutionReady, maintenanceErr: errors.Join(assetexport.ErrAttemptFenceLost, assetexport.ErrStoreObjectAbsent), wantCategory: "artifact_missing"},
		{name: "ready physical tamper", state: assetexport.ExecutionReady, maintenanceErr: errors.Join(assetexport.ErrAttemptFenceLost, assetexport.ErrCipherTampered), wantCategory: "artifact_tampered"},
		{name: "ready transient cancellation", state: assetexport.ExecutionReady, maintenanceErr: context.Canceled, wantReturned: true},
		{name: "stale source fence", state: assetexport.ExecutionQueued, maintenanceErr: assetexport.ErrAttemptFenceLost, wantReturned: true},
		{name: "infrastructure failure", state: assetexport.ExecutionQueued, maintenanceErr: errors.New("injected source maintenance infrastructure failure"), wantReturned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := exportRuntimeKeyringFixture(t)
			if err := db.AutoMigrate(&model.BackupAssetExportJob{}); err != nil {
				t.Fatal(err)
			}
			jobID := fmt.Sprintf("%032x", len(test.name)+1)
			if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(test.state)}).Error; err != nil {
				t.Fatal(err)
			}
			lifecycle := &managedExportMaintenanceLifecycleFake{}
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: managedExportSourceMaintenanceErrorFake{err: test.maintenanceErr},
				Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: lifecycle,
				Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{}, Cadence: time.Minute,
				BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "maintenance-classification",
			})
			if err != nil {
				t.Fatal(err)
			}
			err = runner.maintainSourceLeases(context.Background())
			if test.wantReturned {
				if !errors.Is(err, test.maintenanceErr) {
					t.Fatalf("maintenance error=%v, want %v", err, test.maintenanceErr)
				}
			} else if err != nil {
				t.Fatalf("handled maintenance error=%v", err)
			}
			if got := slices.Contains(lifecycle.sourceExpired, jobID); got != test.wantSourceExpired {
				t.Fatalf("source-expired calls=%v want=%t", lifecycle.sourceExpired, test.wantSourceExpired)
			}
			if len(lifecycle.unpublishable) == 0 {
				if test.wantCategory != "" {
					t.Fatalf("unpublishable calls empty, want category %q", test.wantCategory)
				}
			} else if len(lifecycle.unpublishable) != 1 || lifecycle.unpublishable[0].jobID != jobID ||
				lifecycle.unpublishable[0].category != test.wantCategory {
				t.Fatalf("unpublishable calls=%+v want job=%s category=%s", lifecycle.unpublishable, jobID, test.wantCategory)
			}
		})
	}
}

func TestManagedExportWorkerReadyMaintenanceUsesExpiredAndCorruptLifecycleOutcomes(t *testing.T) {
	for _, test := range []struct {
		name           string
		maintenanceErr error
		expiresAt      *time.Time
		wantCategory   string
	}{
		{name: "planned ready expiry", maintenanceErr: assetexport.ErrReadyExpired},
		{name: "missing ready expiry", maintenanceErr: assetexport.ErrUnavailable, wantCategory: "internal_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := exportRuntimeKeyringFixture(t)
			if err := db.AutoMigrate(
				&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
				&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{}, &model.BackupAssetExportQuotaBucket{},
			); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			expiresAt := test.expiresAt
			if test.maintenanceErr == assetexport.ErrReadyExpired {
				expiresAt = &now
			}
			readyAt := now.Add(-time.Minute)
			jobID := fmt.Sprintf("%032x", len(test.name)+100)
			createManagedExportRuntimeJob(t, db, &model.BackupAssetExportJob{
				ID: jobID, ExecutionState: string(assetexport.ExecutionReady), CleanupState: string(assetexport.CleanupNone),
				ReadyAt: &readyAt, ExpiresAt: expiresAt, TransitionRevision: 1, CreatedAt: readyAt, UpdatedAt: readyAt,
			})
			seedManagedExportLifecycleScheduler(t, db)
			port := &runtimeMaintenanceLifecyclePortFake{}
			lifecycle, err := assetexport.NewLifecycle(assetexport.LifecycleDependencies{
				DB: db, Port: port, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: managedExportSourceMaintenanceErrorFake{err: test.maintenanceErr},
				Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: lifecycle,
				Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{}, Cadence: time.Minute,
				BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "ready-maintenance-outcome",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.reconcile(context.Background()); err != nil {
				t.Fatalf("ready maintenance reconciliation: %v", err)
			}
			var job model.BackupAssetExportJob
			if err := db.First(&job, "id = ?", jobID).Error; err != nil {
				t.Fatal(err)
			}
			if job.ExecutionState != string(assetexport.ExecutionExpired) ||
				job.CleanupState != string(assetexport.CleanupPurged) || job.ErrorCategory != test.wantCategory {
				t.Fatalf("ready maintenance outcome=%+v want category=%q", job, test.wantCategory)
			}
			if strings.Contains(job.ErrorCategory, "source_expired") {
				t.Fatalf("ready maintenance was reclassified as source expiry: %+v", job)
			}
		})
	}
}

func TestManagedExportWorkerRoutesTypedHeartbeatDeadlines(t *testing.T) {
	for _, test := range []struct {
		name              string
		heartbeatErr      error
		wantSourceExpired bool
		wantFailCategory  string
	}{
		{name: "source cap", heartbeatErr: assetexport.ErrSourceDeadlineReached, wantSourceExpired: true},
		{name: "execution deadline", heartbeatErr: assetexport.ErrExecutionDeadlineReached, wantFailCategory: "deadline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := exportRuntimeKeyringFixture(t)
			if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}); err != nil {
				t.Fatal(err)
			}
			jobID := fmt.Sprintf("%032x", len(test.name)+200)
			itemID := fmt.Sprintf("%032x", len(test.name)+300)
			if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(assetexport.ExecutionRunning)}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.BackupAssetExportItem{
				ID: itemID, JobID: jobID, EntryType: string(backupasset.CatalogEntryFile),
				PathNonce: []byte{}, PathCiphertext: []byte{},
			}).Error; err != nil {
				t.Fatal(err)
			}
			attempts := &managedExportTypedHeartbeatAttempts{
				claim:        assetexport.AttemptClaim{AttemptID: strings.Repeat("b", 32), FenceToken: []byte(strings.Repeat("c", 32))},
				heartbeatErr: test.heartbeatErr,
			}
			lifecycle := &managedExportMaintenanceLifecycleFake{}
			calls := []string{}
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: attempts, Worker: &managedExportFailureBackend{calls: &calls}, Lifecycle: lifecycle,
				Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{}, Cadence: time.Hour,
				HeartbeatInterval: time.Hour, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "typed-heartbeat",
			})
			if err != nil {
				t.Fatal(err)
			}
			err = runner.executeJob(context.Background(), jobID)
			if !errors.Is(err, test.heartbeatErr) {
				t.Fatalf("execute typed heartbeat error=%v want=%v", err, test.heartbeatErr)
			}
			if got := slices.Contains(lifecycle.sourceExpired, jobID); got != test.wantSourceExpired {
				t.Fatalf("source-expired calls=%v want=%t", lifecycle.sourceExpired, test.wantSourceExpired)
			}
			if attempts.failCategory != test.wantFailCategory {
				t.Fatalf("attempt fail category=%q want=%q", attempts.failCategory, test.wantFailCategory)
			}
			if test.wantSourceExpired && slices.Contains(calls, "discard:"+jobID) {
				t.Fatalf("source-expired heartbeat discarded before lifecycle ordering: calls=%v", calls)
			}
		})
	}
}

func TestManagedExportWorkerMaintenanceFinalizesCancellationSelectedBeforeLeaseLock(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(&model.BackupAssetExportJob{}, &model.BackupAssetExportArtifact{}); err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("c", 32)
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Create(&model.BackupAssetExportJob{
		ID: jobID, SelectionDigest: strings.Repeat("a", 64), ArchiveFormat: "zip", ArchiveProfile: "zip_deflate_v1",
		ExecutionState: string(assetexport.ExecutionQueued), CleanupState: string(assetexport.CleanupNone),
		AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, TransitionRevision: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	port := &runtimeMaintenanceLifecyclePortFake{}
	lifecycle, err := assetexport.NewLifecycle(assetexport.LifecycleDependencies{DB: db, Port: port, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	attempts := &managedExportSourceMaintenanceCancelFake{db: db, jobID: jobID}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: lifecycle,
		Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{}, Cadence: time.Minute,
		BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "maintenance-cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.maintainSourceLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetExportJob
	if err := db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionCanceled) || job.ErrorCategory == "source_expired" {
		t.Fatalf("maintenance cancellation result=%+v", job)
	}
	if !reflect.DeepEqual(port.calls, []string{"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key", "release_sources", "purge_ciphertext", "release_store"}) {
		t.Fatalf("maintenance cancellation cleanup calls=%v", port.calls)
	}
}

type managedExportSourceMaintenanceFake struct {
	maintained []string
}

type managedExportRunSourceMaintenanceFake struct {
	maintained chan string
}

type managedExportPublicationHandoffAttempts struct {
	claim assetexport.AttemptClaim
	calls *[]string
}

type managedExportPublicationHandoffBackend struct {
	managedExportBackendFake
	db *gorm.DB
}

type managedExportFairSourceMaintenanceFake struct {
	failOnceFor string
	failure     error
	failed      bool
	maintained  []string
}

type managedExportReadyVerificationAttempts struct {
	calls          *[]string
	readyIntegrity *assetexport.ReadyIntegrityToken
}

func (*managedExportReadyVerificationAttempts) Claim(
	context.Context, assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
}

func (*managedExportReadyVerificationAttempts) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportReadyVerificationAttempts) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (fake *managedExportReadyVerificationAttempts) MaintainSourceLeases(
	_ context.Context, request assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	*fake.calls = append(*fake.calls, "maintain")
	fake.readyIntegrity = request.ReadyIntegrity
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

type managedExportReadyVerificationBackend struct {
	managedExportBackendFake
	calls  *[]string
	result assetexport.PersistentReconcileResult
	err    error
}

func (fake *managedExportReadyVerificationBackend) ReconcileJob(
	context.Context, assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	*fake.calls = append(*fake.calls, "verify")
	return fake.result, fake.err
}

type managedExportTypedHeartbeatAttempts struct {
	claim        assetexport.AttemptClaim
	heartbeatErr error
	failCategory string
}

func (fake *managedExportTypedHeartbeatAttempts) Claim(
	context.Context, assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	return fake.claim, nil
}

func (fake *managedExportTypedHeartbeatAttempts) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, fake.heartbeatErr
}

func (fake *managedExportTypedHeartbeatAttempts) Fail(
	_ context.Context, request assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	fake.failCategory = request.Category
	return assetexport.AttemptFailureResult{}, nil
}

func (*managedExportTypedHeartbeatAttempts) MaintainSourceLeases(
	context.Context, assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

type managedExportSourceMaintenanceErrorFake struct{ err error }

func (fake managedExportSourceMaintenanceErrorFake) Claim(
	context.Context, assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
}

func (fake managedExportSourceMaintenanceErrorFake) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (fake managedExportSourceMaintenanceErrorFake) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (fake managedExportSourceMaintenanceErrorFake) MaintainSourceLeases(
	context.Context, assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, fake.err
}

type managedExportMaintenanceLifecycleCall struct {
	jobID    string
	category string
}

type managedExportMaintenanceLifecycleFake struct {
	sourceExpired []string
	unpublishable []managedExportMaintenanceLifecycleCall
}

func (*managedExportMaintenanceLifecycleFake) Reconcile(context.Context, int) (int, error) {
	return 0, nil
}

func (fake *managedExportMaintenanceLifecycleFake) FailSourceExpired(_ context.Context, jobID string) error {
	fake.sourceExpired = append(fake.sourceExpired, jobID)
	return nil
}

func (fake *managedExportMaintenanceLifecycleFake) FailUnpublishable(_ context.Context, jobID, category string) error {
	fake.unpublishable = append(fake.unpublishable, managedExportMaintenanceLifecycleCall{jobID: jobID, category: category})
	return nil
}

type managedExportSourceMaintenanceCancelFake struct {
	db    *gorm.DB
	jobID string
}

func (*managedExportSourceMaintenanceCancelFake) Claim(context.Context, assetexport.AttemptClaimRequest) (assetexport.AttemptClaim, error) {
	return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
}
func (*managedExportSourceMaintenanceCancelFake) Heartbeat(context.Context, assetexport.AttemptHeartbeatRequest) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}
func (*managedExportSourceMaintenanceCancelFake) Fail(context.Context, assetexport.AttemptFailureRequest) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}
func (fake *managedExportSourceMaintenanceCancelFake) MaintainSourceLeases(_ context.Context, request assetexport.SourceLeaseMaintenanceRequest) (assetexport.SourceLeaseMaintenanceResult, error) {
	if err := fake.db.Model(&model.BackupAssetExportJob{}).Where("id = ? AND execution_state = ?", request.JobID, string(assetexport.ExecutionQueued)).
		Updates(map[string]any{"execution_state": string(assetexport.ExecutionCancelRequested), "transition_revision": gorm.Expr("transition_revision + 1")}).Error; err != nil {
		return assetexport.SourceLeaseMaintenanceResult{}, err
	}
	return assetexport.SourceLeaseMaintenanceResult{}, assetexport.ErrAttemptFenceLost
}

type runtimeMaintenanceLifecyclePortFake struct{ calls []string }

func (fake *runtimeMaintenanceLifecyclePortFake) FenceAttempts(context.Context, string) error {
	fake.calls = append(fake.calls, "fence_attempts")
	return nil
}
func (fake *runtimeMaintenanceLifecyclePortFake) RevokeDeliveries(context.Context, string) error {
	fake.calls = append(fake.calls, "revoke_deliveries")
	return nil
}
func (fake *runtimeMaintenanceLifecyclePortFake) DrainStreams(context.Context, string) error {
	fake.calls = append(fake.calls, "drain_streams")
	return nil
}
func (fake *runtimeMaintenanceLifecyclePortFake) DestroyJobKeyAndSelection(context.Context, string) error {
	fake.calls = append(fake.calls, "destroy_key")
	return nil
}
func (fake *runtimeMaintenanceLifecyclePortFake) ReleaseSourcesAndNonStore(context.Context, string) error {
	fake.calls = append(fake.calls, "release_sources")
	return nil
}
func (fake *runtimeMaintenanceLifecyclePortFake) PurgeCiphertext(context.Context, string) error {
	fake.calls = append(fake.calls, "purge_ciphertext")
	return nil
}
func (fake *runtimeMaintenanceLifecyclePortFake) ReleaseStoreBytes(context.Context, string) error {
	fake.calls = append(fake.calls, "release_store")
	return nil
}

func (*managedExportSourceMaintenanceFake) Claim(
	context.Context, assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
}

func (*managedExportSourceMaintenanceFake) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportSourceMaintenanceFake) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (fake *managedExportSourceMaintenanceFake) MaintainSourceLeases(
	_ context.Context, request assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	fake.maintained = append(fake.maintained, request.JobID)
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

func (*managedExportRunSourceMaintenanceFake) Claim(
	context.Context, assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
}

func (*managedExportRunSourceMaintenanceFake) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportRunSourceMaintenanceFake) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (fake *managedExportRunSourceMaintenanceFake) MaintainSourceLeases(
	_ context.Context, request assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	select {
	case fake.maintained <- request.JobID:
	default:
	}
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

func (fake *managedExportPublicationHandoffAttempts) Claim(
	_ context.Context, request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	*fake.calls = append(*fake.calls, "claim:"+request.JobID)
	return fake.claim, nil
}

func (*managedExportPublicationHandoffAttempts) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportPublicationHandoffAttempts) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (fake *managedExportPublicationHandoffAttempts) MaintainSourceLeases(
	_ context.Context, request assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	if request.ReadyIntegrity == nil {
		return assetexport.SourceLeaseMaintenanceResult{}, errors.New("ready source lease handoff omitted integrity proof")
	}
	*fake.calls = append(*fake.calls, "maintain:"+request.JobID)
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

func (backend *managedExportPublicationHandoffBackend) PublishReady(
	_ context.Context, request assetexport.PersistentPublishRequest,
) (assetexport.PersistentPublishResult, error) {
	*backend.calls = append(*backend.calls, "publish:"+request.JobID)
	if err := backend.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", request.JobID).
		Update("execution_state", string(assetexport.ExecutionReady)).Error; err != nil {
		return assetexport.PersistentPublishResult{}, err
	}
	return assetexport.PersistentPublishResult{}, nil
}

func (backend *managedExportPublicationHandoffBackend) ReconcileJob(
	_ context.Context, request assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	*backend.calls = append(*backend.calls, "verify:"+request.JobID)
	return assetexport.PersistentReconcileResult{ReadyIntegrity: &assetexport.ReadyIntegrityToken{}}, nil
}

func (*managedExportFairSourceMaintenanceFake) Claim(
	context.Context, assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
}

func (*managedExportFairSourceMaintenanceFake) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportFairSourceMaintenanceFake) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (fake *managedExportFairSourceMaintenanceFake) MaintainSourceLeases(
	_ context.Context, request assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	fake.maintained = append(fake.maintained, request.JobID)
	if request.JobID == fake.failOnceFor && !fake.failed {
		fake.failed = true
		return assetexport.SourceLeaseMaintenanceResult{}, fake.failure
	}
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

type managedExportBudgetFake struct {
	calls atomic.Int32
	limit atomic.Int32
}

type managedExportSealedRecoveryBackend struct {
	*assetexport.PersistentWorker
	reconcileRequests []assetexport.PersistentReconcileRequest
	reconcileResults  []assetexport.PersistentReconcileResult
	discardCalls      int
}

func (backend *managedExportSealedRecoveryBackend) ReconcileJob(
	ctx context.Context,
	request assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	result, err := backend.PersistentWorker.ReconcileJob(ctx, request)
	backend.reconcileRequests = append(backend.reconcileRequests, request)
	backend.reconcileResults = append(backend.reconcileResults, result)
	return result, err
}

func (backend *managedExportSealedRecoveryBackend) DiscardAttempt(
	ctx context.Context,
	request assetexport.PersistentDiscardAttemptRequest,
) error {
	backend.discardCalls++
	return backend.PersistentWorker.DiscardAttempt(ctx, request)
}

type managedExportSealedRecoveryAttempts struct {
	*assetexport.AttemptCoordinator
	claimCalls int
}

func (attempts *managedExportSealedRecoveryAttempts) Claim(
	ctx context.Context,
	request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	attempts.claimCalls++
	return attempts.AttemptCoordinator.Claim(ctx, request)
}

type managedExportExpiredSealingAttempts struct {
	claim assetexport.AttemptClaim
	calls *[]string
}

func (fake *managedExportExpiredSealingAttempts) Claim(
	_ context.Context, request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	*fake.calls = append(*fake.calls, "claim:"+request.JobID)
	return fake.claim, nil
}

func (fake *managedExportExpiredSealingAttempts) Heartbeat(
	_ context.Context, request assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	*fake.calls = append(*fake.calls, "heartbeat:"+request.JobID)
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportExpiredSealingAttempts) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (fake *managedExportExpiredSealingAttempts) MaintainSourceLeases(
	_ context.Context, request assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	*fake.calls = append(*fake.calls, "takeover-source:"+request.JobID)
	return assetexport.SourceLeaseMaintenanceResult{TakenOver: true}, nil
}

type managedExportExpiredSealingBackend struct {
	calls *[]string
	jobID string
}

func (fake *managedExportExpiredSealingBackend) SpoolItem(
	_ context.Context, request assetexport.PersistentSpoolItemRequest,
) (assetexport.PersistentSpoolResult, error) {
	*fake.calls = append(*fake.calls, "spool:"+request.JobID+":"+request.ItemID)
	return assetexport.PersistentSpoolResult{}, nil
}

func (fake *managedExportExpiredSealingBackend) SealArchive(
	_ context.Context, request assetexport.PersistentSealRequest,
) (assetexport.PersistentSealResult, error) {
	*fake.calls = append(*fake.calls, "seal:"+request.JobID)
	return assetexport.PersistentSealResult{ArtifactID: strings.Repeat("c", 32)}, nil
}

func (fake *managedExportExpiredSealingBackend) PublishReady(
	_ context.Context, request assetexport.PersistentPublishRequest,
) (assetexport.PersistentPublishResult, error) {
	*fake.calls = append(*fake.calls, "publish:"+request.JobID)
	return assetexport.PersistentPublishResult{}, nil
}

func (fake *managedExportExpiredSealingBackend) DiscardAttempt(
	_ context.Context, request assetexport.PersistentDiscardAttemptRequest,
) error {
	*fake.calls = append(*fake.calls, "discard:"+request.JobID+":"+request.AttemptID)
	return nil
}

func (fake *managedExportExpiredSealingBackend) ReconcileJob(
	_ context.Context, request assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	*fake.calls = append(*fake.calls, "reconcile:"+request.JobID)
	if request.JobID != fake.jobID {
		return assetexport.PersistentReconcileResult{}, errors.New("unexpected sealing job")
	}
	return assetexport.PersistentReconcileResult{}, assetexport.ErrAttemptLeaseExpired
}

func (*managedExportExpiredSealingBackend) ReconcileOrphans(context.Context) (int, error) {
	return 0, nil
}

type managedExportRetirementBackend struct {
	db         *gorm.DB
	failOnce   error
	attemptIDs []string
}

func (*managedExportRetirementBackend) SpoolItem(
	context.Context, assetexport.PersistentSpoolItemRequest,
) (assetexport.PersistentSpoolResult, error) {
	return assetexport.PersistentSpoolResult{}, errors.New("unexpected retired-attempt spool")
}

func (*managedExportRetirementBackend) SealArchive(
	context.Context, assetexport.PersistentSealRequest,
) (assetexport.PersistentSealResult, error) {
	return assetexport.PersistentSealResult{}, errors.New("unexpected retired-attempt seal")
}

func (*managedExportRetirementBackend) PublishReady(
	context.Context, assetexport.PersistentPublishRequest,
) (assetexport.PersistentPublishResult, error) {
	return assetexport.PersistentPublishResult{}, errors.New("unexpected retired-attempt publish")
}

func (fake *managedExportRetirementBackend) DiscardAttempt(
	_ context.Context, request assetexport.PersistentDiscardAttemptRequest,
) error {
	fake.attemptIDs = append(fake.attemptIDs, request.AttemptID)
	if len(fake.attemptIDs) == 1 {
		return fake.failOnce
	}
	return fake.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", request.AttemptID).
			Update("staging_locator", "").Error; err != nil {
			return err
		}
		if err := tx.Model(&model.BackupAssetExportItemAttempt{}).Where("attempt_id = ?", request.AttemptID).
			Update("spool_locator", "").Error; err != nil {
			return err
		}
		return tx.Where("attempt_id = ? AND expires_at IS NULL", request.AttemptID).
			Delete(&model.BackupAssetExportArtifact{}).Error
	})
}

func (*managedExportRetirementBackend) ReconcileJob(
	context.Context, assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	return assetexport.PersistentReconcileResult{}, nil
}

func (*managedExportRetirementBackend) ReconcileOrphans(context.Context) (int, error) {
	return 0, nil
}

type managedExportConcurrencyAttempts struct{}

type managedExportJoinedAttemptAttempts struct {
	failCalls     atomic.Int32
	lastJobID     string
	lastAttemptID string
}

type managedExportWakeAttempts struct {
	managedExportConcurrencyAttempts
	claims chan string
}

func (attempts *managedExportWakeAttempts) Claim(_ context.Context, request assetexport.AttemptClaimRequest) (assetexport.AttemptClaim, error) {
	select {
	case attempts.claims <- request.JobID:
	default:
	}
	return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
}

func (attempts *managedExportJoinedAttemptAttempts) Claim(context.Context, assetexport.AttemptClaimRequest) (assetexport.AttemptClaim, error) {
	return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
}

func (*managedExportJoinedAttemptAttempts) Heartbeat(context.Context, assetexport.AttemptHeartbeatRequest) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (attempts *managedExportJoinedAttemptAttempts) Fail(_ context.Context, request assetexport.AttemptFailureRequest) (assetexport.AttemptFailureResult, error) {
	attempts.failCalls.Add(1)
	attempts.lastJobID = request.JobID
	attempts.lastAttemptID = request.AttemptID
	return assetexport.AttemptFailureResult{ExecutionState: assetexport.ExecutionRetryWait}, nil
}

func (*managedExportJoinedAttemptAttempts) MaintainSourceLeases(context.Context, assetexport.SourceLeaseMaintenanceRequest) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

func (managedExportConcurrencyAttempts) Claim(
	_ context.Context, request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	return assetexport.AttemptClaim{
		AttemptID:  request.JobID,
		FenceToken: []byte(strings.Repeat("f", 32)),
	}, nil
}

type managedExportAdmissionBoundaryAttempts struct {
	managedExportConcurrencyAttempts
	claims chan string
}

func (fake *managedExportAdmissionBoundaryAttempts) Claim(
	ctx context.Context, request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	fake.claims <- request.JobID
	return fake.managedExportConcurrencyAttempts.Claim(ctx, request)
}

type managedExportBlockingClaimAttempts struct {
	managedExportConcurrencyAttempts
	claims       chan string
	canceled     chan struct{}
	release      chan struct{}
	canceledOnce sync.Once
}

func (fake *managedExportBlockingClaimAttempts) Claim(
	ctx context.Context,
	request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	fake.claims <- request.JobID
	select {
	case <-ctx.Done():
		fake.canceledOnce.Do(func() { close(fake.canceled) })
		return assetexport.AttemptClaim{}, ctx.Err()
	case <-fake.release:
		return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
	}
}

type managedExportSQLiteBusyDrainAttempts struct {
	*assetexport.AttemptCoordinator
	claimCalls atomic.Int32
}

func (attempts *managedExportSQLiteBusyDrainAttempts) Claim(
	ctx context.Context,
	request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	attempts.claimCalls.Add(1)
	return attempts.AttemptCoordinator.Claim(ctx, request)
}

func (attempts *managedExportSQLiteBusyDrainAttempts) MaintainSourceLeases(
	context.Context,
	assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

func (managedExportConcurrencyAttempts) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (managedExportConcurrencyAttempts) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (managedExportConcurrencyAttempts) MaintainSourceLeases(
	context.Context, assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

type managedExportConcurrencyBackend struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
	maximum atomic.Int32
	total   atomic.Int32
}

func (backend *managedExportConcurrencyBackend) SpoolItem(
	ctx context.Context, request assetexport.PersistentSpoolItemRequest,
) (assetexport.PersistentSpoolResult, error) {
	active := backend.active.Add(1)
	defer backend.active.Add(-1)
	backend.total.Add(1)
	for {
		maximum := backend.maximum.Load()
		if active <= maximum || backend.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	backend.started <- request.JobID
	select {
	case <-backend.release:
		return assetexport.PersistentSpoolResult{}, nil
	case <-ctx.Done():
		return assetexport.PersistentSpoolResult{}, ctx.Err()
	}
}

func (*managedExportConcurrencyBackend) SealArchive(
	_ context.Context, request assetexport.PersistentSealRequest,
) (assetexport.PersistentSealResult, error) {
	return assetexport.PersistentSealResult{ArtifactID: request.JobID}, nil
}

func (*managedExportConcurrencyBackend) PublishReady(
	context.Context, assetexport.PersistentPublishRequest,
) (assetexport.PersistentPublishResult, error) {
	return assetexport.PersistentPublishResult{}, nil
}

func (*managedExportConcurrencyBackend) ReconcileJob(
	context.Context, assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	return assetexport.PersistentReconcileResult{}, nil
}

func (*managedExportConcurrencyBackend) ReconcileOrphans(context.Context) (int, error) {
	return 0, nil
}

func (*managedExportConcurrencyBackend) DiscardAttempt(
	context.Context, assetexport.PersistentDiscardAttemptRequest,
) error {
	return nil
}

func (fake *managedExportBudgetFake) ReconcileExpiredAttemptReads(_ context.Context, limit int) (int, error) {
	fake.calls.Add(1)
	fake.limit.Store(int32(limit))
	return 0, nil
}

type managedExportSQLiteBusyDrainBudget struct {
	db      *gorm.DB
	entered chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (budget *managedExportSQLiteBusyDrainBudget) ReconcileExpiredAttemptReads(ctx context.Context, _ int) (int, error) {
	budget.once.Do(func() { close(budget.entered) })
	err := database.WithSQLiteBusyRetryTx(ctx, budget.db, func(*gorm.DB) error {
		budget.calls.Add(1)
		return nil
	})
	return 0, err
}

type managedExportFailureAttempts struct {
	claim assetexport.AttemptClaim
	calls *[]string
}

func (fake *managedExportFailureAttempts) Claim(
	_ context.Context, request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	*fake.calls = append(*fake.calls, "claim:"+request.JobID)
	return fake.claim, nil
}

func (*managedExportFailureAttempts) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (fake *managedExportFailureAttempts) Fail(
	_ context.Context, request assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	*fake.calls = append(*fake.calls, "fail:"+request.JobID)
	return assetexport.AttemptFailureResult{ExecutionState: assetexport.ExecutionRetryWait}, nil
}

func (*managedExportFailureAttempts) MaintainSourceLeases(
	context.Context, assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

type managedExportFailureBackend struct {
	calls   *[]string
	failure error
}

type managedExportPreHeaderAttempts struct {
	claim           assetexport.AttemptClaim
	calls           *[]string
	providerBytes   int64
	checkpointErr   error
	failureCategory string
	recoveredSpool  bool
}

type managedExportPreHeaderCheckpointFixture struct {
	db       *gorm.DB
	runner   *managedExportWorker
	attempts *managedExportPreHeaderAttempts
	jobID    string
	itemID   string
}

func newManagedExportPreHeaderCheckpointFixture(t *testing.T) managedExportPreHeaderCheckpointFixture {
	t.Helper()
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportItemAttempt{},
	); err != nil {
		t.Fatal(err)
	}
	jobID, itemID := strings.Repeat("9", 32), strings.Repeat("a", 32)
	claim := assetexport.AttemptClaim{AttemptID: strings.Repeat("b", 32), FenceToken: []byte(strings.Repeat("c", 32))}
	if err := db.Create(&model.BackupAssetExportJob{ID: jobID, ExecutionState: string(assetexport.ExecutionQueued)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, EntryType: string(backupasset.CatalogEntryFile),
		PathNonce: []byte{1}, PathCiphertext: []byte{2}, State: string(assetexport.ItemPending),
	}).Error; err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	attempts := &managedExportPreHeaderAttempts{claim: claim, calls: &calls, providerBytes: 37}
	if err := db.Create(&model.BackupAssetExportItemAttempt{
		ID: strings.Repeat("d", 32), JobID: jobID, ItemID: itemID, AttemptID: claim.AttemptID,
		State: string(assetexport.ItemPending), ProviderBytes: attempts.providerBytes,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: attempts, Worker: &managedExportPreHeaderBackend{calls: &calls},
		Lifecycle: managedExportLifecycleFake{}, Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 1, WorkerConcurrency: 1, WorkerOwner: "export-worker-pre-header-checkpoint",
	})
	if err != nil {
		t.Fatal(err)
	}
	return managedExportPreHeaderCheckpointFixture{
		db: db, runner: runner, attempts: attempts, jobID: jobID, itemID: itemID,
	}
}

func managedExportPreHeaderCheckpointRows(
	t *testing.T, fixture managedExportPreHeaderCheckpointFixture,
) []model.BackupAssetExportItemAttempt {
	t.Helper()
	var rows []model.BackupAssetExportItemAttempt
	if err := fixture.db.Where("job_id = ? AND attempt_id = ? AND item_id = ?",
		fixture.jobID, fixture.attempts.claim.AttemptID, fixture.itemID,
	).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func (fake *managedExportPreHeaderAttempts) Claim(_ context.Context, request assetexport.AttemptClaimRequest) (assetexport.AttemptClaim, error) {
	*fake.calls = append(*fake.calls, "claim:"+request.JobID)
	return fake.claim, nil
}

func (*managedExportPreHeaderAttempts) Heartbeat(context.Context, assetexport.AttemptHeartbeatRequest) (assetexport.AttemptHeartbeatResult, error) {
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (fake *managedExportPreHeaderAttempts) Fail(_ context.Context, request assetexport.AttemptFailureRequest) (assetexport.AttemptFailureResult, error) {
	*fake.calls = append(*fake.calls, "fail:"+request.JobID)
	return assetexport.AttemptFailureResult{ExecutionState: assetexport.ExecutionRetryWait}, nil
}

func (*managedExportPreHeaderAttempts) MaintainSourceLeases(context.Context, assetexport.SourceLeaseMaintenanceRequest) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

func (fake *managedExportPreHeaderAttempts) Checkpoint(_ context.Context, checkpoint assetexport.AttemptCheckpoint) error {
	*fake.calls = append(*fake.calls, "checkpoint:"+checkpoint.ItemID)
	category := fake.failureCategory
	if category == "" {
		category = "source_changed"
	}
	if checkpoint.State != assetexport.ItemFailed || checkpoint.ErrorCategory != category || checkpoint.LogicalBytes != 0 ||
		checkpoint.ProviderBytes != fake.providerBytes || checkpoint.PreHeaderSpoolRecovered != fake.recoveredSpool {
		return errors.New("unexpected pre-header checkpoint")
	}
	return fake.checkpointErr
}

type managedExportPreHeaderBackend struct {
	calls       *[]string
	failure     error
	sealFailure error
}

type managedExportPauseAfterSecondSpoolBackend struct {
	inner        *assetexport.PersistentWorker
	secondSpool  chan assetexport.PersistentSpoolResult
	release      chan struct{}
	spoolCalls   atomic.Int32
	sealCalls    atomic.Int32
	publishCalls atomic.Int32
}

func (backend *managedExportPauseAfterSecondSpoolBackend) SpoolItem(
	ctx context.Context, request assetexport.PersistentSpoolItemRequest,
) (assetexport.PersistentSpoolResult, error) {
	result, err := backend.inner.SpoolItem(ctx, request)
	if err != nil {
		return assetexport.PersistentSpoolResult{}, err
	}
	if backend.spoolCalls.Add(1) != 2 {
		return result, nil
	}
	select {
	case backend.secondSpool <- result:
	case <-ctx.Done():
		return assetexport.PersistentSpoolResult{}, ctx.Err()
	}
	select {
	case <-backend.release:
		return result, nil
	case <-ctx.Done():
		return assetexport.PersistentSpoolResult{}, ctx.Err()
	}
}

func (backend *managedExportPauseAfterSecondSpoolBackend) SealArchive(
	ctx context.Context, request assetexport.PersistentSealRequest,
) (assetexport.PersistentSealResult, error) {
	backend.sealCalls.Add(1)
	return backend.inner.SealArchive(ctx, request)
}

func (backend *managedExportPauseAfterSecondSpoolBackend) PublishReady(
	ctx context.Context, request assetexport.PersistentPublishRequest,
) (assetexport.PersistentPublishResult, error) {
	backend.publishCalls.Add(1)
	return backend.inner.PublishReady(ctx, request)
}

func (backend *managedExportPauseAfterSecondSpoolBackend) DiscardAttempt(
	ctx context.Context, request assetexport.PersistentDiscardAttemptRequest,
) error {
	return backend.inner.DiscardAttempt(ctx, request)
}

func (backend *managedExportPauseAfterSecondSpoolBackend) ReconcileJob(
	ctx context.Context, request assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	return backend.inner.ReconcileJob(ctx, request)
}

func (backend *managedExportPauseAfterSecondSpoolBackend) ReconcileOrphans(ctx context.Context) (int, error) {
	return backend.inner.ReconcileOrphans(ctx)
}

func (fake *managedExportPreHeaderBackend) SpoolItem(_ context.Context, request assetexport.PersistentSpoolItemRequest) (assetexport.PersistentSpoolResult, error) {
	*fake.calls = append(*fake.calls, "spool:"+request.JobID)
	return assetexport.PersistentSpoolResult{}, fake.failure
}

func (fake *managedExportPreHeaderBackend) SealArchive(_ context.Context, request assetexport.PersistentSealRequest) (assetexport.PersistentSealResult, error) {
	*fake.calls = append(*fake.calls, "seal:"+request.JobID)
	if fake.sealFailure != nil {
		failure := fake.sealFailure
		fake.sealFailure = nil
		return assetexport.PersistentSealResult{}, failure
	}
	return assetexport.PersistentSealResult{ArtifactID: strings.Repeat("d", 32)}, nil
}

type managedExportSealingPreHeaderFailure struct {
	cause  error
	itemID string
}

func (failure managedExportSealingPreHeaderFailure) Error() string  { return failure.cause.Error() }
func (failure managedExportSealingPreHeaderFailure) Unwrap() error  { return failure.cause }
func (failure managedExportSealingPreHeaderFailure) ItemID() string { return failure.itemID }

func (fake *managedExportPreHeaderBackend) PublishReady(_ context.Context, request assetexport.PersistentPublishRequest) (assetexport.PersistentPublishResult, error) {
	*fake.calls = append(*fake.calls, "publish:"+request.JobID)
	return assetexport.PersistentPublishResult{}, nil
}

func (*managedExportPreHeaderBackend) ReconcileJob(context.Context, assetexport.PersistentReconcileRequest) (assetexport.PersistentReconcileResult, error) {
	return assetexport.PersistentReconcileResult{}, nil
}

func (*managedExportPreHeaderBackend) ReconcileOrphans(context.Context) (int, error) { return 0, nil }

func (fake *managedExportPreHeaderBackend) DiscardAttempt(_ context.Context, request assetexport.PersistentDiscardAttemptRequest) error {
	*fake.calls = append(*fake.calls, "discard:"+request.JobID)
	return nil
}

func (fake *managedExportFailureBackend) SpoolItem(
	_ context.Context, request assetexport.PersistentSpoolItemRequest,
) (assetexport.PersistentSpoolResult, error) {
	*fake.calls = append(*fake.calls, "spool:"+request.JobID)
	return assetexport.PersistentSpoolResult{}, fake.failure
}

func (*managedExportFailureBackend) SealArchive(
	context.Context, assetexport.PersistentSealRequest,
) (assetexport.PersistentSealResult, error) {
	return assetexport.PersistentSealResult{}, errors.New("unexpected seal")
}

func (*managedExportFailureBackend) PublishReady(
	context.Context, assetexport.PersistentPublishRequest,
) (assetexport.PersistentPublishResult, error) {
	return assetexport.PersistentPublishResult{}, errors.New("unexpected publish")
}

func (*managedExportFailureBackend) ReconcileJob(
	context.Context, assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	return assetexport.PersistentReconcileResult{}, nil
}

func (*managedExportFailureBackend) ReconcileOrphans(context.Context) (int, error) { return 0, nil }

func (fake *managedExportFailureBackend) DiscardAttempt(
	_ context.Context, request assetexport.PersistentDiscardAttemptRequest,
) error {
	*fake.calls = append(*fake.calls, "discard:"+request.JobID)
	return nil
}

type managedExportHeartbeatAttempts struct {
	claim     assetexport.AttemptClaim
	heartbeat chan struct{}
}

func (fake *managedExportHeartbeatAttempts) Claim(context.Context, assetexport.AttemptClaimRequest) (assetexport.AttemptClaim, error) {
	return fake.claim, nil
}

func (fake *managedExportHeartbeatAttempts) Heartbeat(
	context.Context, assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	select {
	case fake.heartbeat <- struct{}{}:
	default:
	}
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportHeartbeatAttempts) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (*managedExportHeartbeatAttempts) MaintainSourceLeases(
	context.Context, assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

type managedExportHeartbeatBackend struct {
	started      chan struct{}
	release      chan struct{}
	blockPublish bool
}

func (fake *managedExportHeartbeatBackend) SpoolItem(
	ctx context.Context, _ assetexport.PersistentSpoolItemRequest,
) (assetexport.PersistentSpoolResult, error) {
	if fake.blockPublish {
		return assetexport.PersistentSpoolResult{}, nil
	}
	close(fake.started)
	select {
	case <-fake.release:
		return assetexport.PersistentSpoolResult{}, nil
	case <-ctx.Done():
		return assetexport.PersistentSpoolResult{}, ctx.Err()
	}
}

func (*managedExportHeartbeatBackend) SealArchive(
	context.Context, assetexport.PersistentSealRequest,
) (assetexport.PersistentSealResult, error) {
	return assetexport.PersistentSealResult{ArtifactID: strings.Repeat("8", 32)}, nil
}

func (fake *managedExportHeartbeatBackend) PublishReady(
	ctx context.Context, _ assetexport.PersistentPublishRequest,
) (assetexport.PersistentPublishResult, error) {
	if !fake.blockPublish {
		return assetexport.PersistentPublishResult{}, nil
	}
	close(fake.started)
	select {
	case <-fake.release:
		return assetexport.PersistentPublishResult{}, nil
	case <-ctx.Done():
		return assetexport.PersistentPublishResult{}, ctx.Err()
	}
}

func (*managedExportHeartbeatBackend) ReconcileJob(
	context.Context, assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	return assetexport.PersistentReconcileResult{}, nil
}

func (*managedExportHeartbeatBackend) ReconcileOrphans(context.Context) (int, error) { return 0, nil }

func (*managedExportHeartbeatBackend) DiscardAttempt(
	context.Context, assetexport.PersistentDiscardAttemptRequest,
) error {
	return nil
}

type managedExportAttemptsFake struct {
	claim assetexport.AttemptClaim
	calls *[]string
}

func (fake *managedExportAttemptsFake) Claim(_ context.Context, request assetexport.AttemptClaimRequest) (assetexport.AttemptClaim, error) {
	*fake.calls = append(*fake.calls, "claim:"+request.JobID)
	return fake.claim, nil
}

func (fake *managedExportAttemptsFake) Heartbeat(
	_ context.Context, request assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	*fake.calls = append(*fake.calls, "heartbeat:"+request.JobID)
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportAttemptsFake) Fail(
	context.Context, assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (*managedExportAttemptsFake) MaintainSourceLeases(
	context.Context, assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

type managedExportClaimableBackfillAttempts struct {
	nonclaimableJobIDs map[string]struct{}
	claim              assetexport.AttemptClaim
	calls              *[]string
}

func (fake *managedExportClaimableBackfillAttempts) Claim(
	_ context.Context,
	request assetexport.AttemptClaimRequest,
) (assetexport.AttemptClaim, error) {
	*fake.calls = append(*fake.calls, "claim:"+request.JobID)
	if _, nonclaimable := fake.nonclaimableJobIDs[request.JobID]; nonclaimable {
		return assetexport.AttemptClaim{}, assetexport.ErrAttemptNotClaimable
	}
	return fake.claim, nil
}

func (fake *managedExportClaimableBackfillAttempts) Heartbeat(
	_ context.Context,
	request assetexport.AttemptHeartbeatRequest,
) (assetexport.AttemptHeartbeatResult, error) {
	*fake.calls = append(*fake.calls, "heartbeat:"+request.JobID)
	return assetexport.AttemptHeartbeatResult{}, nil
}

func (*managedExportClaimableBackfillAttempts) Fail(
	context.Context,
	assetexport.AttemptFailureRequest,
) (assetexport.AttemptFailureResult, error) {
	return assetexport.AttemptFailureResult{}, nil
}

func (*managedExportClaimableBackfillAttempts) MaintainSourceLeases(
	context.Context,
	assetexport.SourceLeaseMaintenanceRequest,
) (assetexport.SourceLeaseMaintenanceResult, error) {
	return assetexport.SourceLeaseMaintenanceResult{}, nil
}

type managedExportBackendFake struct {
	seal  assetexport.PersistentSealResult
	calls *[]string
}

func (fake *managedExportBackendFake) SpoolItem(
	_ context.Context, request assetexport.PersistentSpoolItemRequest,
) (assetexport.PersistentSpoolResult, error) {
	*fake.calls = append(*fake.calls, "spool:"+request.JobID+":"+request.ItemID)
	return assetexport.PersistentSpoolResult{}, nil
}

func (fake *managedExportBackendFake) SealArchive(
	_ context.Context, request assetexport.PersistentSealRequest,
) (assetexport.PersistentSealResult, error) {
	*fake.calls = append(*fake.calls, "seal:"+request.JobID)
	return fake.seal, nil
}

func (fake *managedExportBackendFake) PublishReady(
	_ context.Context, request assetexport.PersistentPublishRequest,
) (assetexport.PersistentPublishResult, error) {
	*fake.calls = append(*fake.calls, "publish:"+request.JobID)
	return assetexport.PersistentPublishResult{}, nil
}

func (*managedExportBackendFake) ReconcileJob(
	context.Context, assetexport.PersistentReconcileRequest,
) (assetexport.PersistentReconcileResult, error) {
	return assetexport.PersistentReconcileResult{ReadyIntegrity: &assetexport.ReadyIntegrityToken{}}, nil
}

func (*managedExportBackendFake) ReconcileOrphans(context.Context) (int, error) { return 0, nil }

func (*managedExportBackendFake) DiscardAttempt(
	context.Context, assetexport.PersistentDiscardAttemptRequest,
) error {
	return nil
}

type managedExportLifecycleFake struct{}

func (managedExportLifecycleFake) Reconcile(context.Context, int) (int, error)     { return 0, nil }
func (managedExportLifecycleFake) FailSourceExpired(context.Context, string) error { return nil }
func (managedExportLifecycleFake) FailUnpublishable(context.Context, string, string) error {
	return nil
}

type managedExportDeliveryFake struct{}

func (managedExportDeliveryFake) ReconcileDeliveries(context.Context) error { return nil }
func (managedExportDeliveryFake) MaintainDeliveries(context.Context) error  { return nil }

type managedExportCleanupContextObservation struct {
	err         error
	deadline    time.Time
	hasDeadline bool
}

type managedExportCleanupContextDeliveryFake struct {
	waitFor           context.Context
	entered           chan struct{}
	reconcileContexts chan managedExportCleanupContextObservation
	enteredOnce       sync.Once
	reconcileOnce     sync.Once
}

func (fake *managedExportCleanupContextDeliveryFake) ReconcileDeliveries(ctx context.Context) error {
	fake.enteredOnce.Do(func() { close(fake.entered) })
	<-fake.waitFor.Done()
	deadline, hasDeadline := ctx.Deadline()
	fake.reconcileOnce.Do(func() {
		fake.reconcileContexts <- managedExportCleanupContextObservation{
			err: ctx.Err(), deadline: deadline, hasDeadline: hasDeadline,
		}
	})
	return nil
}

func (*managedExportCleanupContextDeliveryFake) MaintainDeliveries(context.Context) error { return nil }

type managedExportTransitionCleanupDeliveryFake struct {
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newManagedExportTransitionCleanupDeliveryFake() *managedExportTransitionCleanupDeliveryFake {
	return &managedExportTransitionCleanupDeliveryFake{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (fake *managedExportTransitionCleanupDeliveryFake) ReconcileDeliveries(ctx context.Context) error {
	fake.enteredOnce.Do(func() { close(fake.entered) })
	select {
	case <-fake.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*managedExportTransitionCleanupDeliveryFake) MaintainDeliveries(context.Context) error {
	return nil
}

func (fake *managedExportTransitionCleanupDeliveryFake) Release() {
	fake.releaseOnce.Do(func() { close(fake.release) })
}

type managedExportStalledDeliveryFake struct {
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newManagedExportStalledDeliveryFake() *managedExportStalledDeliveryFake {
	return &managedExportStalledDeliveryFake{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (*managedExportStalledDeliveryFake) ReconcileDeliveries(context.Context) error { return nil }

func (fake *managedExportStalledDeliveryFake) MaintainDeliveries(context.Context) error {
	fake.enteredOnce.Do(func() { close(fake.entered) })
	<-fake.release
	return nil
}

func (fake *managedExportStalledDeliveryFake) Release() {
	fake.releaseOnce.Do(func() { close(fake.release) })
}

type managedExportDeliveryMaintenanceFake struct {
	restartCalls     atomic.Int32
	maintenanceCalls atomic.Int32
}

func (fake *managedExportDeliveryMaintenanceFake) ReconcileDeliveries(context.Context) error {
	fake.restartCalls.Add(1)
	return nil
}

func (fake *managedExportDeliveryMaintenanceFake) MaintainDeliveries(context.Context) error {
	fake.maintenanceCalls.Add(1)
	return nil
}

type managedArchiveMemberMaintenanceFake struct {
	calls atomic.Int32
	limit atomic.Int32
	err   error
}

func (fake *managedArchiveMemberMaintenanceFake) ReconcilePending(_ context.Context, limit int) (int, error) {
	fake.calls.Add(1)
	fake.limit.Store(int32(limit))
	return 0, fake.err
}

func TestManagedExportWorkerUsesRestartDeliveryReconciliationOnlyAtStartup(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	delivery := &managedExportDeliveryMaintenanceFake{}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: &managedExportAttemptsFake{calls: &[]string{}},
		Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: managedExportLifecycleFake{},
		Delivery: delivery, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 3, WorkerConcurrency: 1, WorkerOwner: "export-worker-delivery-maintenance",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Startup(context.Background()); err != nil {
		t.Fatalf("startup: %v", err)
	}
	if delivery.restartCalls.Load() != 1 || delivery.maintenanceCalls.Load() != 0 {
		t.Fatalf("startup delivery calls restart=%d maintenance=%d", delivery.restartCalls.Load(), delivery.maintenanceCalls.Load())
	}
	if err := runner.reconcileWithoutSourceMaintenance(context.Background()); err != nil {
		t.Fatalf("cadence reconciliation: %v", err)
	}
	if delivery.restartCalls.Load() != 1 || delivery.maintenanceCalls.Load() != 1 {
		t.Fatalf("cadence delivery calls restart=%d maintenance=%d", delivery.restartCalls.Load(), delivery.maintenanceCalls.Load())
	}
}

func TestManagedExportWorkerReconcilesArchiveMembersAtStartupAndCadenceWithoutFailingForUnavailableProcessing(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportAttempt{},
		&model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportArtifact{},
	); err != nil {
		t.Fatal(err)
	}
	archive := &managedArchiveMemberMaintenanceFake{err: processing.ErrNotDeployed}
	runner, err := newManagedExportWorker(managedExportWorkerDependencies{
		DB: db, Attempts: &managedExportAttemptsFake{calls: &[]string{}},
		Worker: &managedExportBackendFake{calls: &[]string{}}, Lifecycle: managedExportLifecycleFake{},
		Delivery: managedExportDeliveryFake{}, Archive: archive, Budget: &managedExportBudgetFake{},
		Cadence: time.Minute, BatchSize: 3, WorkerConcurrency: 1, WorkerOwner: "export-worker-archive-maintenance",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Startup(context.Background()); err != nil {
		t.Fatalf("startup treated unavailable Processing as fatal: %v", err)
	}
	if err := runner.reconcileWithoutSourceMaintenance(context.Background()); err != nil {
		t.Fatalf("cadence treated unavailable Processing as fatal: %v", err)
	}
	if archive.calls.Load() != 2 || archive.limit.Load() != 3 {
		t.Fatalf("archive maintenance calls=%d limit=%d", archive.calls.Load(), archive.limit.Load())
	}
}

func TestRuntimeExportSelectionResolverFreezesDirectoryAndRejectsCommitDrift(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	repositoryID := strings.Repeat("a", 32)
	pointID := strings.Repeat("b", 32)
	generationID := strings.Repeat("c", 32)
	rootID := strings.Repeat("d", 64)
	childID := strings.Repeat("e", 64)
	sourceFingerprint := strings.Repeat("1", 64)
	retentionUntil := now.Add(6 * time.Hour)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "fixture",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 7, CapabilitiesJSON: `{"open_sequential":true}`,
		ImmutabilityLevel: "provider_snapshot", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapabilityRevision: 7, PhysicalAvailability: string(backupasset.PhysicalOnline),
		ImmutabilityLevel: "provider_snapshot", HoldState: "none", RetentionUntil: &retentionUntil,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := now
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 2, WrittenEntryCount: 2,
		StartedAt: now.Add(-time.Minute), FinishedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	root := model.CatalogEntry{
		GenerationID: generationID, EntryID: rootID, RecoveryPointID: pointID, NormalizedPath: "root",
		Name: "root", EntryType: string(backupasset.CatalogEntryDirectory), Fingerprint: strings.Repeat("2", 64),
		FingerprintStrength: string(catalog.FingerprintStrong), SecurityState: "sealed", CreatedAt: now,
	}
	child := model.CatalogEntry{
		GenerationID: generationID, EntryID: childID, RecoveryPointID: pointID, ParentEntryID: &rootID,
		NormalizedPath: "root/report.txt", Name: "report.txt", EntryType: string(backupasset.CatalogEntryFile),
		Size: 4096, MimeType: "text/plain", Fingerprint: strings.Repeat("3", 64),
		FingerprintStrength: string(catalog.FingerprintStrong), SecurityState: "sealed", CreatedAt: now,
	}
	if err := db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	ownership, err := catalog.NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &runtimeExportSelectionResolver{db: db, ownership: ownership}
	actor := assetexport.SelectionActor{UserID: 41, Role: "admin"}
	frozen, err := resolver.ResolveExplicit(
		context.Background(), actor, []backupasset.AssetRef{{RecoveryPointID: pointID, EntryID: rootID}},
		assetexport.SelectionLimits{MaxItems: 10, MaxSourcePoints: 2, MaxLogicalBytes: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen.Items) != 2 || frozen.Items[0].Ref.EntryID != rootID || frozen.Items[1].Ref.EntryID != childID ||
		!reflect.DeepEqual(frozen.Items[0].ArchiveComponents, []string{"root"}) ||
		!reflect.DeepEqual(frozen.Items[1].ArchiveComponents, []string{"root", "report.txt"}) ||
		frozen.Items[1].CatalogGenerationID != generationID || frozen.Items[1].SourceFingerprint != sourceFingerprint ||
		frozen.Items[1].ProviderCapabilityRevision != 7 || frozen.Items[1].RetentionUntil == nil ||
		!frozen.Items[1].RetentionUntil.Equal(retentionUntil) {
		t.Fatalf("frozen selection=%+v", frozen)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return resolver.RevalidateFrozenTx(context.Background(), tx, actor, frozen)
	}); err != nil {
		t.Fatalf("revalidate unchanged selection: %v", err)
	}
	if err := db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND entry_id = ?", generationID, childID).
		Update("fingerprint", strings.Repeat("4", 64)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return resolver.RevalidateFrozenTx(context.Background(), tx, actor, frozen)
	}); !errors.Is(err, assetexport.ErrArchiveSource) {
		t.Fatalf("drift revalidation error=%v want archive source", err)
	}
}

func TestRuntimeExportSelectionResolverFreezesSavedSearchAndRevalidatesBinding(t *testing.T) {
	db, _ := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
		&model.BackupAssetSearchGeneration{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	searchGenerationID := strings.Repeat("4", 32)
	savedSearchID := strings.Repeat("5", 32)
	firstID := strings.Repeat("6", 64)
	secondID := strings.Repeat("7", 64)
	sourceFingerprint := strings.Repeat("8", 64)
	retentionUntil := now.Add(8 * time.Hour)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "fixture",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 9, CapabilitiesJSON: `{"open_sequential":true}`,
		ImmutabilityLevel: "provider_snapshot", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapabilityRevision: 9, PhysicalAvailability: string(backupasset.PhysicalOnline),
		ImmutabilityLevel: "provider_snapshot", HoldState: "none", RetentionUntil: &retentionUntil,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := now
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 2, WrittenEntryCount: 2,
		StartedAt: now.Add(-time.Minute), FinishedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for index, fixture := range []struct {
		id   string
		name string
	}{
		{id: firstID, name: "first.txt"},
		{id: secondID, name: "second.txt"},
	} {
		if err := db.Create(&model.CatalogEntry{
			GenerationID: generationID, EntryID: fixture.id, RecoveryPointID: pointID,
			NormalizedPath: fixture.name, Name: fixture.name, EntryType: string(backupasset.CatalogEntryFile),
			Size: int64(100 + index), MimeType: "text/plain", Fingerprint: strings.Repeat(fmt.Sprintf("%x", index+10), 64),
			FingerprintStrength: string(catalog.FingerprintStrong), SecurityState: "sealed", CreatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.BackupAssetSearchGeneration{
		ID: searchGenerationID, RecoveryPointID: pointID, CatalogGenerationID: generationID,
		Generation: 1, State: string(search.SearchGenerationComplete), IsActive: true,
		SourceFingerprint: sourceFingerprint, NormalizerVersion: search.NormalizerVersion, SearchKeyVersion: 1,
		ProjectionRevision: 3, LeaseID: strings.Repeat("9", 32), BuildAttemptID: strings.Repeat("a", 32),
		FenceTokenHash: strings.Repeat("b", 64), ExpectedDocumentCount: 2, WrittenDocumentCount: 2,
		StartedAt: now.Add(-time.Minute), FinishedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	query := search.SearchRequest{
		SchemaVersion: search.QuerySchemaVersion,
		Root:          search.QueryNode{Op: search.QueryOpTerm, Field: search.SearchFieldName, Text: "txt"},
		Scope:         search.SearchScope{Mode: search.SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          search.SearchSortNameAsc, Limit: 1,
	}
	overlayPort := &runtimeExportOverlayFake{saved: overlay.SavedSearch{
		ID: savedSearchID, OwnerUserID: 73, Query: query, Version: 4, State: overlay.SavedSearchActive,
	}}
	total := int64(2)
	index := search.SearchIndexStatus{
		RecoveryPointID: pointID, CatalogGenerationID: generationID, SearchGenerationID: searchGenerationID,
		ProjectionRevision: 3, Coverage: search.CoverageComplete, Staleness: search.StalenessFresh,
	}
	searchPort := &runtimeExportSearchFake{pages: []search.SearchResponse{
		{
			QueryGeneration: strings.Repeat("c", 64), Indexes: []search.SearchIndexStatus{index},
			Items:      []search.SearchHit{{Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: firstID}}},
			NextCursor: "signed-page-2", Total: &total, TotalRelation: search.TotalRelationExact,
			Coverage: search.SearchCoverage{Status: search.CoverageComplete}, Permissions: search.SearchPermissions{List: true},
		},
		{
			QueryGeneration: strings.Repeat("c", 64), Indexes: []search.SearchIndexStatus{index},
			Items: []search.SearchHit{{Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: secondID}}},
			Total: &total, TotalRelation: search.TotalRelationExact,
			Coverage: search.SearchCoverage{Status: search.CoverageComplete}, Permissions: search.SearchPermissions{List: true},
		},
	}}
	ownership, err := catalog.NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &runtimeExportSelectionResolver{
		db: db, ownership: ownership, overlay: overlayPort, search: searchPort, queryLimits: search.DefaultQueryLimits(),
	}
	actor := assetexport.SelectionActor{UserID: 73, Role: "admin"}
	frozen, err := resolver.ResolveSavedSearch(
		context.Background(), actor, savedSearchID, 4,
		assetexport.SelectionLimits{MaxItems: 10, MaxSourcePoints: 2, MaxLogicalBytes: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen.Items) != 2 || frozen.Items[0].Ref.EntryID != firstID || frozen.Items[1].Ref.EntryID != secondID ||
		frozen.SavedSearch == nil || frozen.SavedSearch.SavedSearchID != savedSearchID ||
		frozen.SavedSearch.ExpectedVersion != 4 || len(frozen.SavedSearch.CanonicalQueryDigest) != 64 ||
		len(frozen.SavedSearch.SearchGenerationDigest) != 64 || searchPort.calls != 2 {
		t.Fatalf("saved frozen=%+v search calls=%d", frozen, searchPort.calls)
	}
	overlayPort.validateErr = backupasset.ErrConflict
	if err := db.Transaction(func(tx *gorm.DB) error {
		return resolver.RevalidateFrozenTx(context.Background(), tx, actor, frozen)
	}); !errors.Is(err, backupasset.ErrConflict) || overlayPort.validateCalls != 1 {
		t.Fatalf("saved-search race error=%v validate calls=%d", err, overlayPort.validateCalls)
	}
	overlayPort.validateErr = nil
	if err := db.Transaction(func(tx *gorm.DB) error {
		return resolver.RevalidateFrozenTx(context.Background(), tx, actor, frozen)
	}); err != nil {
		t.Fatalf("revalidate unchanged saved selection: %v", err)
	}
}

type runtimeExportOverlayFake struct {
	saved         overlay.SavedSearch
	validateErr   error
	validateCalls int
}

func (fake *runtimeExportOverlayFake) UseSavedSearch(context.Context, overlay.Actor, string) (overlay.SavedSearch, error) {
	return fake.saved, nil
}

func (fake *runtimeExportOverlayFake) ValidateSavedSearchForExportTx(
	_ context.Context, _ *gorm.DB, _ overlay.SavedSearchExportBinding,
) error {
	fake.validateCalls++
	return fake.validateErr
}

type runtimeExportSearchFake struct {
	pages []search.SearchResponse
	calls int
}

func (fake *runtimeExportSearchFake) Search(
	_ context.Context, _ search.SearchActor, request search.SearchRequest,
) (search.SearchResponse, error) {
	if fake.calls >= len(fake.pages) {
		return search.SearchResponse{}, errors.New("unexpected Search page")
	}
	if fake.calls == 0 && request.Cursor != "" || fake.calls == 1 && request.Cursor != "signed-page-2" {
		return search.SearchResponse{}, errors.New("unexpected Search cursor")
	}
	page := fake.pages[fake.calls]
	fake.calls++
	return page, nil
}

type runtimeExportKeyLossFixture struct {
	db         *gorm.DB
	ring       *backupasset.Keyring
	runtime    *managedExportRuntime
	config     backupasset.ExportConfig
	port       *runtimeExportKeyLossPort
	builds     *atomic.Int32
	keyVersion int
	jobID      string
	keyID      string
	attemptID  string
	itemID     string
}

type runtimeExportReadyJobIDs struct {
	jobID      string
	keyID      string
	attemptID  string
	itemID     string
	artifactID string
}

type runtimeExportReadyJob struct {
	runtimeExportReadyJobIDs
	keyVersion int
}

func createManagedExportRuntimeJob(t *testing.T, db *gorm.DB, job *model.BackupAssetExportJob) {
	t.Helper()
	if db == nil || job == nil {
		t.Fatal("managed Export runtime job fixture requires a database and job")
	}
	if job.LifecycleEnqueueSequence == 0 {
		if err := db.Model(&model.BackupAssetExportJob{}).
			Select("COALESCE(MAX(lifecycle_enqueue_sequence), 0) + 1").
			Scan(&job.LifecycleEnqueueSequence).Error; err != nil {
			t.Fatalf("allocate managed Export lifecycle sequence: %v", err)
		}
	}
	if job.LifecycleEnqueueSequence <= 0 {
		t.Fatalf("invalid managed Export lifecycle sequence=%d", job.LifecycleEnqueueSequence)
	}
	if err := db.Create(job).Error; err != nil {
		t.Fatalf("seed managed Export job: %v", err)
	}
}

func seedManagedExportLifecycleScheduler(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db == nil {
		t.Fatal("managed Export lifecycle scheduler fixture requires a database")
	}
	var nextSequence int64
	if err := db.Model(&model.BackupAssetExportJob{}).
		Select("COALESCE(MAX(lifecycle_enqueue_sequence), 0) + 1").
		Scan(&nextSequence).Error; err != nil {
		t.Fatalf("derive managed Export lifecycle scheduler high water: %v", err)
	}
	if nextSequence <= 1 {
		t.Fatalf("managed Export lifecycle scheduler next sequence=%d", nextSequence)
	}
	if err := db.Create(&model.BackupAssetExportQuotaBucket{
		ID: strings.Repeat("f", 32), Scope: "global", Subject: "global", LifecycleNextSequence: nextSequence,
	}).Error; err != nil {
		t.Fatalf("seed managed Export lifecycle scheduler: %v", err)
	}
}

func newRuntimeExportKeyLossFixture(t *testing.T) runtimeExportKeyLossFixture {
	t.Helper()
	db, ring := exportRuntimeKeyringFixture(t)
	if err := db.AutoMigrate(
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportAttempt{}, &model.BackupAssetExportArtifact{}, &model.BackupAssetExportReservation{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	material, err := ring.Ensure(context.Background(), backupasset.KeyDomainExportStore)
	if err != nil {
		t.Fatal(err)
	}
	jobID, keyID, attemptID, itemID := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	job := model.BackupAssetExportJob{
		ID: jobID, OwnerUserID: 1, SelectionDigest: strings.Repeat("e", 64), SelectionSchemaVersion: 1,
		ArchiveFormat: "zip", ArchiveProfile: "zip_deflate_v1", LimitsSchemaVersion: 1,
		ExecutionState: string(assetexport.ExecutionRunning), CleanupState: string(assetexport.CleanupNone),
		CurrentAttemptID: &attemptID, AbsoluteDeadline: now.Add(time.Hour), TransitionRevision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	key := model.BackupAssetExportKey{
		ID: keyID, JobID: jobID, State: "active", WrappedDEK: []byte("wrapped-dek"), EnvelopeNonce: []byte("envelope-nonce"),
		KEKVersion: material.Version, WrapAlgorithm: assetexport.JobKeyWrapAlgorithmV1, KeyRevision: 1, CreatedAt: now,
	}
	item := model.BackupAssetExportItem{
		ID: itemID, JobID: jobID, Ordinal: 0, PathNonce: []byte("path-nonce"), PathCiphertext: []byte("path-ciphertext"),
		State: string(assetexport.ItemPending), CreatedAt: now, UpdatedAt: now,
	}
	attempt := model.BackupAssetExportAttempt{
		ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerOwner: "key-loss-worker", State: string(assetexport.AttemptActive),
		FenceToken: []byte("fence-token"), FenceDigest: strings.Repeat("f", 64), NoncePrefix: []byte("nonce-prefix"),
		LeaseExpiresAt: now.Add(time.Hour), IsCurrent: true, StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	createManagedExportRuntimeJob(t, db, &job)
	for _, row := range []any{&key, &item, &attempt} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}

	root := filepath.Join(t.TempDir(), "export")
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	foundation := backupasset.NewFoundationService(exportRuntimeSettings(values))
	config, err := foundation.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	port := &runtimeExportKeyLossPort{db: db, now: now}
	lifecycle, err := assetexport.NewLifecycle(assetexport.LifecycleDependencies{
		DB: db, Port: port, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	builds := &atomic.Int32{}
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: foundation, Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, _ backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			builds.Add(1)
			return &managedExportGraph{store: store, lifecycle: lifecycle}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtimeExportKeyLossFixture{
		db: db, ring: ring, runtime: manager, config: config, port: port, builds: builds,
		keyVersion: material.Version, jobID: jobID, keyID: keyID, attemptID: attemptID, itemID: itemID,
	}
}

func makeRuntimeExportKeyLossJobReady(
	t *testing.T,
	fixture runtimeExportKeyLossFixture,
	artifactID string,
) runtimeExportReadyJob {
	t.Helper()
	readyAt := fixture.port.now.Add(-time.Minute)
	expiresAt := fixture.port.now.Add(time.Hour)
	finishedAt := fixture.port.now.Add(-time.Second)
	ids := runtimeExportReadyJobIDs{
		jobID: fixture.jobID, keyID: fixture.keyID, attemptID: fixture.attemptID, itemID: fixture.itemID, artifactID: artifactID,
	}
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", ids.jobID).Updates(map[string]any{
			"execution_state": string(assetexport.ExecutionReady), "result_kind": string(assetexport.ResultComplete),
			"current_attempt_id": nil, "ready_at": readyAt, "expires_at": expiresAt,
			"item_count": int64(1), "packed_count": int64(1), "logical_bytes": int64(64),
			"provider_bytes": int64(64), "artifact_bytes": int64(128), "updated_at": fixture.port.now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.BackupAssetExportItem{}).Where("id = ?", ids.itemID).Updates(map[string]any{
			"state": string(assetexport.ItemPacked), "logical_bytes": int64(64), "provider_bytes": int64(64), "updated_at": fixture.port.now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetExportAttempt{}).Where("id = ?", ids.attemptID).Updates(map[string]any{
			"state": string(assetexport.AttemptSealed), "is_current": false, "finished_at": finishedAt, "updated_at": fixture.port.now,
		}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(runtimeExportReadyArtifact(ids, fixture.port.now, &finishedAt)).Error; err != nil {
		t.Fatal(err)
	}
	seedRuntimeExportStoreReservations(t, fixture.db, fixture.port.now, ids)
	return runtimeExportReadyJob{runtimeExportReadyJobIDs: ids, keyVersion: fixture.keyVersion}
}

func seedRuntimeExportReadyJob(
	t *testing.T,
	db *gorm.DB,
	now time.Time,
	keyVersion int,
	ids runtimeExportReadyJobIDs,
) runtimeExportReadyJob {
	t.Helper()
	readyAt := now.Add(-time.Minute)
	expiresAt := now.Add(time.Hour)
	finishedAt := now.Add(-time.Second)
	job := model.BackupAssetExportJob{
		ID: ids.jobID, OwnerUserID: 2, SelectionDigest: strings.Repeat("7", 64), SelectionSchemaVersion: 1,
		ArchiveFormat: "zip", ArchiveProfile: "zip_deflate_v1", LimitsSchemaVersion: 1,
		ExecutionState: string(assetexport.ExecutionReady), ResultKind: string(assetexport.ResultComplete), CleanupState: string(assetexport.CleanupNone),
		AbsoluteDeadline: now.Add(2 * time.Hour), ReadyAt: &readyAt, ExpiresAt: &expiresAt,
		ItemCount: 1, PackedCount: 1, LogicalBytes: 64, ProviderBytes: 64, ArtifactBytes: 128, TransitionRevision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	key := model.BackupAssetExportKey{
		ID: ids.keyID, JobID: ids.jobID, State: "active", WrappedDEK: []byte("healthy-wrapped-dek"), EnvelopeNonce: []byte("healthy-envelope-nonce"),
		KEKVersion: keyVersion, WrapAlgorithm: assetexport.JobKeyWrapAlgorithmV1, KeyRevision: 1, CreatedAt: now,
	}
	item := model.BackupAssetExportItem{
		ID: ids.itemID, JobID: ids.jobID, Ordinal: 0, PathNonce: []byte("healthy-path-nonce"), PathCiphertext: []byte("healthy-path-ciphertext"),
		State: string(assetexport.ItemPacked), LogicalBytes: 64, ProviderBytes: 64, CreatedAt: now, UpdatedAt: now,
	}
	attempt := model.BackupAssetExportAttempt{
		ID: ids.attemptID, JobID: ids.jobID, AttemptNumber: 1, WorkerOwner: "healthy-key-worker", State: string(assetexport.AttemptSealed),
		FenceToken: []byte("healthy-fence-token"), FenceDigest: strings.Repeat("8", 64), NoncePrefix: []byte("healthy-nonce-prefix"),
		LeaseExpiresAt: now.Add(time.Hour), IsCurrent: false, StartedAt: now.Add(-2 * time.Minute), FinishedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
	}
	createManagedExportRuntimeJob(t, db, &job)
	for _, row := range []any{&key, &item, &attempt, runtimeExportReadyArtifact(ids, now, &finishedAt)} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed healthy ready Export %T: %v", row, err)
		}
	}
	seedRuntimeExportStoreReservations(t, db, now, ids)
	return runtimeExportReadyJob{runtimeExportReadyJobIDs: ids, keyVersion: keyVersion}
}

func runtimeExportReadyArtifact(
	ids runtimeExportReadyJobIDs,
	now time.Time,
	sealedAt *time.Time,
) *model.BackupAssetExportArtifact {
	return &model.BackupAssetExportArtifact{
		ID: ids.artifactID, JobID: ids.jobID, AttemptID: ids.attemptID, JobKeyID: ids.keyID,
		State: "sealed", Locator: ids.artifactID + ".xre", CipherVersion: 1, ChunkBytes: 64 * 1024,
		FormatVersion: 1, NoncePrefix: []byte("ready-artifact-nonce"), ChunkCount: 1,
		PlaintextDigest: strings.Repeat("9", 64), ArchiveDigest: strings.Repeat("a", 64), CiphertextDigest: strings.Repeat("b", 64),
		PlaintextSize: 64, CiphertextSize: 128, SealedAt: sealedAt, CreatedAt: now, UpdatedAt: now,
	}
}

func seedRuntimeExportStoreReservations(t *testing.T, db *gorm.DB, now time.Time, ids runtimeExportReadyJobIDs) {
	t.Helper()
	jobID := ids.jobID
	for _, suffix := range []string{"c", "d"} {
		reservation := model.BackupAssetExportReservation{
			ID: ids.artifactID[:31] + suffix, BucketID: strings.Repeat(suffix, 32), JobID: &jobID,
			Kind: "store", ReservedStoreBytes: 128, LeaseOwner: "key-loss-reservation", LeaseExpiresAt: now.Add(time.Hour),
			State: "reserved", CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&reservation).Error; err != nil {
			t.Fatalf("seed ready Export store reservation: %v", err)
		}
	}
}

func assertRuntimeExportReadyJobPurgedForKeyLoss(t *testing.T, db *gorm.DB, expected runtimeExportReadyJob) {
	t.Helper()
	var job model.BackupAssetExportJob
	if err := db.First(&job, "id = ?", expected.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionExpired) || job.CleanupState != string(assetexport.CleanupPurged) || job.ErrorCategory != "key_unavailable" {
		t.Fatalf("lost ready Export job=%+v", job)
	}
	var key model.BackupAssetExportKey
	if err := db.First(&key, "id = ?", expected.keyID).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "lost" || len(key.WrappedDEK) != 0 || len(key.EnvelopeNonce) != 0 || key.DestroyedAt == nil {
		t.Fatalf("lost ready Export key=%+v", key)
	}
	var artifact model.BackupAssetExportArtifact
	if err := db.First(&artifact, "id = ?", expected.artifactID).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != "purged" || artifact.PurgedAt == nil {
		t.Fatalf("lost ready Export artifact=%+v", artifact)
	}
}

func assertRuntimeExportReadyJobUntouched(t *testing.T, db *gorm.DB, expected runtimeExportReadyJob) {
	t.Helper()
	var job model.BackupAssetExportJob
	if err := db.First(&job, "id = ?", expected.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionReady) || job.CleanupState != string(assetexport.CleanupNone) || job.ErrorCategory != "" {
		t.Fatalf("healthy ready Export job changed=%+v", job)
	}
	var key model.BackupAssetExportKey
	if err := db.First(&key, "id = ?", expected.keyID).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "active" || key.KEKVersion != expected.keyVersion || len(key.WrappedDEK) == 0 || len(key.EnvelopeNonce) == 0 || key.DestroyedAt != nil {
		t.Fatalf("healthy ready Export key changed=%+v", key)
	}
	var artifact model.BackupAssetExportArtifact
	if err := db.First(&artifact, "id = ?", expected.artifactID).Error; err != nil {
		t.Fatal(err)
	}
	if artifact.State != "sealed" || artifact.PurgedAt != nil {
		t.Fatalf("healthy ready Export artifact changed=%+v", artifact)
	}
}

func assertRuntimeExportKeyLoss(t *testing.T, fixture runtimeExportKeyLossFixture) {
	t.Helper()
	if fixture.runtime.Ready() || fixture.runtime.graph != nil || fixture.runtime.publication.current() != nil {
		t.Fatalf("key-loss runtime published graph=%p published=%p ready=%v", fixture.runtime.graph, fixture.runtime.publication.current(), fixture.runtime.Ready())
	}
	if _, err := fixture.ring.ByVersion(context.Background(), backupasset.KeyDomainExportStore, fixture.keyVersion); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("unreadable Export KEK version remained usable: %v", err)
	}
	var job model.BackupAssetExportJob
	if err := fixture.db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionFailed) || job.CleanupState != string(assetexport.CleanupPurged) || job.ErrorCategory != "key_unavailable" {
		t.Fatalf("key-loss Export job=%+v", job)
	}
	var key model.BackupAssetExportKey
	if err := fixture.db.First(&key, "id = ?", fixture.keyID).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != "lost" || len(key.WrappedDEK) != 0 || len(key.EnvelopeNonce) != 0 || key.DestroyedAt == nil {
		t.Fatalf("key-loss Export key=%+v", key)
	}
	var item model.BackupAssetExportItem
	if err := fixture.db.First(&item, "id = ?", fixture.itemID).Error; err != nil {
		t.Fatal(err)
	}
	if len(item.PathNonce) != 0 || len(item.PathCiphertext) != 0 {
		t.Fatalf("key-loss Export selection remained readable: %+v", item)
	}
	var attempt model.BackupAssetExportAttempt
	if err := fixture.db.First(&attempt, "id = ?", fixture.attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(assetexport.AttemptFailed) || attempt.IsCurrent {
		t.Fatalf("key-loss Export attempt=%+v", attempt)
	}
	wantCalls := runtimeExportKeyLossCalls(fixture.jobID)
	if !reflect.DeepEqual(fixture.port.calls, wantCalls) {
		t.Fatalf("key-loss cleanup calls=%v want=%v", fixture.port.calls, wantCalls)
	}
}

func runtimeExportKeyLossCalls(jobID string) []string {
	return []string{
		"fence_attempts:" + jobID,
		"revoke_deliveries:" + jobID,
		"drain_streams:" + jobID,
		"destroy_key:" + jobID,
		"release_sources:" + jobID,
		"purge_ciphertext:" + jobID,
		"release_store:" + jobID,
	}
}

type runtimeExportKeyLossPort struct {
	db    *gorm.DB
	now   time.Time
	calls []string
}

func (port *runtimeExportKeyLossPort) record(name, jobID string) {
	port.calls = append(port.calls, name+":"+jobID)
}

func (port *runtimeExportKeyLossPort) FenceAttempts(_ context.Context, jobID string) error {
	port.record("fence_attempts", jobID)
	return port.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportAttempt{}).
			Where("job_id = ? AND state IN ?", jobID, []string{string(assetexport.AttemptActive), string(assetexport.AttemptSealing)}).
			Updates(map[string]any{
				"state": string(assetexport.AttemptFailed), "failure_category": "key_unavailable",
				"is_current": false, "finished_at": port.now,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", jobID).
			Updates(map[string]any{"current_attempt_id": nil, "current_fence_revision": gorm.Expr("current_fence_revision + 1")}).Error
	})
}

func (port *runtimeExportKeyLossPort) RevokeDeliveries(_ context.Context, jobID string) error {
	port.record("revoke_deliveries", jobID)
	return nil
}

func (port *runtimeExportKeyLossPort) DrainStreams(_ context.Context, jobID string) error {
	port.record("drain_streams", jobID)
	return nil
}

func (port *runtimeExportKeyLossPort) DestroyJobKeyAndSelection(_ context.Context, jobID string) error {
	port.record("destroy_key", jobID)
	return port.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.BackupAssetExportKey{}).
			Where("job_id = ? AND state = ?", jobID, "active").
			Updates(map[string]any{
				"state": "destroyed", "wrapped_dek": []byte{}, "envelope_nonce": []byte{},
				"destroyed_at": port.now, "key_revision": gorm.Expr("key_revision + 1"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return assetexport.ErrUnavailable
		}
		return tx.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", jobID).
			Updates(map[string]any{"path_nonce": []byte{}, "path_ciphertext": []byte{}, "updated_at": port.now}).Error
	})
}

func (port *runtimeExportKeyLossPort) ReleaseSourcesAndNonStore(_ context.Context, jobID string) error {
	port.record("release_sources", jobID)
	return nil
}

func (port *runtimeExportKeyLossPort) PurgeCiphertext(_ context.Context, jobID string) error {
	port.record("purge_ciphertext", jobID)
	purgedAt := port.now
	return port.db.Model(&model.BackupAssetExportArtifact{}).
		Where("job_id = ? AND state <> ?", jobID, "purged").
		Updates(map[string]any{"state": "purged", "purged_at": purgedAt, "updated_at": port.now}).Error
}

func (port *runtimeExportKeyLossPort) ReleaseStoreBytes(_ context.Context, jobID string) error {
	port.record("release_store", jobID)
	return nil
}

func exportRuntimeKeyringFixture(t *testing.T) (*gorm.DB, *backupasset.Keyring) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_EXPORT_RUNTIME_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close Export runtime test database: %v", err)
		}
	})
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.RecoveryPointLease{}, &model.BackupAssetExportSourceLease{},
	); err != nil {
		t.Fatal(err)
	}
	return db, backupasset.NewKeyring(db, func() time.Time { return time.Now().UTC() })
}

func exportRuntimeFileBackedKeyringFixture(t *testing.T) (*gorm.DB, *backupasset.Keyring, string) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_EXPORT_RUNTIME_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	databasePath := filepath.Join(t.TempDir(), "export-runtime-contention.db")
	db, err := database.Open(configpkg.Config{DBType: "sqlite", SQLitePath: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close file-backed Export runtime test database: %v", err)
		}
	})
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	return db, backupasset.NewKeyring(db, func() time.Time { return time.Now().UTC() }), databasePath
}

func newEnabledManagedExportRuntime(
	t *testing.T,
	root string,
	build func(context.Context, backupasset.ExportConfig, *assetexport.Store) (*managedExportGraph, error),
) *managedExportRuntime {
	t.Helper()
	db, ring := exportRuntimeKeyringFixture(t)
	values := runtimeFoundationSettings(true)
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.export.root"] = root
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil }, Build: build,
	})
	if err != nil {
		t.Fatalf("new managed Export runtime: %v", err)
	}
	return manager
}

type runtimeExportDurableReadyFixture struct {
	jobID      string
	artifactID string
	locator    string
	clock      time.Time
	item       assetexport.FrozenItem
	source     *runtimeExportDurableSourceResolver
	leases     *backupasset.LeaseService
}

type runtimeExportDurableQueuedFixture struct {
	jobID  string
	clock  time.Time
	items  []assetexport.FrozenItem
	source *runtimeExportDurableSourceResolver
	leases *backupasset.LeaseService
	worker *assetexport.PersistentWorker
	store  *assetexport.Store
}

type managedExportDrainConservativeState struct {
	job          model.BackupAssetExportJob
	sources      []model.BackupAssetExportSourceLease
	foundation   []model.RecoveryPointLease
	keys         []model.BackupAssetExportKey
	reservations []model.BackupAssetExportReservation
}

func loadManagedExportDrainConservativeState(
	t *testing.T,
	db *gorm.DB,
	jobID string,
) managedExportDrainConservativeState {
	t.Helper()
	var state managedExportDrainConservativeState
	if err := db.First(&state.job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", jobID).Order("id ASC").Find(&state.sources).Error; err != nil {
		t.Fatal(err)
	}
	leaseIDs := make([]string, 0, len(state.sources))
	for _, source := range state.sources {
		leaseIDs = append(leaseIDs, source.LeaseID)
	}
	if len(leaseIDs) == 0 {
		t.Fatal("durable drain fixture has no source leases")
	}
	if err := db.Where("id IN ?", leaseIDs).Order("id ASC").Find(&state.foundation).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", jobID).Order("id ASC").Find(&state.keys).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", jobID).Order("id ASC").Find(&state.reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(state.foundation) == 0 || len(state.keys) == 0 || len(state.reservations) == 0 {
		t.Fatalf("durable drain fixture incomplete: sources=%d Foundation=%d keys=%d reservations=%d",
			len(state.sources), len(state.foundation), len(state.keys), len(state.reservations))
	}
	return state
}

func runtimeExportDurableFrozenItems(clock time.Time, count int) []assetexport.FrozenItem {
	retention := clock.Add(6 * time.Hour)
	items := make([]assetexport.FrozenItem, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, assetexport.FrozenItem{
			SchemaVersion: 1,
			Ref: backupasset.AssetRef{
				RecoveryPointID: strings.Repeat("1", 32),
				EntryID:         strings.Repeat(string(rune('a')+rune(index)), 64),
			},
			CatalogGenerationID:        strings.Repeat("2", 32),
			SourceFingerprint:          "runtime-durable-source-fingerprint",
			EntryFingerprint:           fmt.Sprintf("runtime-durable-entry-fingerprint-%d", index),
			FingerprintStrength:        "strong",
			ProviderCapabilityRevision: 3,
			EntryType:                  backupasset.CatalogEntryFile,
			LogicalSize:                42,
			MediaType:                  "text/plain",
			RetentionUntil:             &retention,
			SelectionRootOrdinal:       0,
			ArchiveComponents:          []string{"root", fmt.Sprintf("retained-%d.txt", index+1)},
		})
	}
	return items
}

func newRuntimeExportDurableQueuedFixture(
	t *testing.T,
	db *gorm.DB,
	ring *backupasset.Keyring,
	root string,
	config backupasset.ExportConfig,
	clock time.Time,
	items []assetexport.FrozenItem,
) runtimeExportDurableQueuedFixture {
	t.Helper()
	if len(items) == 0 {
		t.Fatal("durable queued Export fixture requires at least one item")
	}
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLease{}, &model.WrappedDomainKey{},
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportAttempt{}, &model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportSourceLease{},
		&model.BackupAssetExportArtifact{}, &model.BackupAssetExportIdempotency{}, &model.BackupAssetExportQuotaBucket{},
		&model.BackupAssetExportReservation{}, &model.BackupAssetExportDeliveryGrant{},
		&model.BackupAssetExportDeliveryRequest{}, &model.BackupAssetArchiveMemberRequest{},
	); err != nil {
		t.Fatal(err)
	}
	createdRecoveryPoints := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, exists := createdRecoveryPoints[item.Ref.RecoveryPointID]; exists {
			continue
		}
		if err := db.Create(&model.RecoveryPoint{
			ID: item.Ref.RecoveryPointID, RepositoryID: strings.Repeat("9", 32),
			State: string(backupasset.RecoveryPointCommitted), Semantics: string(backupasset.PointNativeSnapshot),
			SourceFingerprint: item.SourceFingerprint, CapabilityRevision: int(item.ProviderCapabilityRevision),
			PhysicalAvailability: string(backupasset.PhysicalOnline),
			ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
			HoldState:            string(backupasset.HoldNone), RetentionUntil: item.RetentionUntil,
			CreatedAt: clock, UpdatedAt: clock,
		}).Error; err != nil {
			t.Fatal(err)
		}
		createdRecoveryPoints[item.Ref.RecoveryPointID] = struct{}{}
	}
	leases, err := backupasset.NewLeaseService(db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: config.LeaseTTL, Heartbeat: config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainExportStore); err != nil {
		t.Fatal(err)
	}
	serviceConfig := runtimeExportServiceConfig(config)
	service, err := assetexport.NewService(assetexport.ServiceDependencies{
		DB: db, Now: func() time.Time { return clock }, Leases: leases, Keys: ring,
		Resolver: runtimeExportDurableSelectionResolver{}, Config: serviceConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := assetexport.FreezeSelection(items, nil, serviceConfig.Selection)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CommitCreate(context.Background(), assetexport.CommitCreateRequest{
		Actor: assetexport.SelectionActor{UserID: 100, Role: "admin"}, Selection: selection,
		IdempotencyKey: "runtime-durable-queued", ArchiveFormat: assetexport.ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	budget, err := assetexport.NewAttemptBudgetService(db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	sources := make([]runtimeExportDurableSource, 0, len(items))
	for index, item := range items {
		sources = append(sources, runtimeExportDurableSource{
			item: item, payload: bytes.Repeat([]byte{byte(rune('r') + rune(index))}, int(item.LogicalSize)),
		})
	}
	source := &runtimeExportDurableSourceResolver{sources: sources}
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	store, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := assetexport.NewPersistentWorker(assetexport.PersistentWorkerDependencies{
		DB: db, Keys: ring, Broker: broker, Metadata: runtimeExportDurableMetadataValidator{}, Store: store,
		SourceLeases: leases, AttemptWork: assetexport.NewAttemptWorkRegistry(), Now: func() time.Time { return clock },
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return runtimeExportDurableQueuedFixture{
		jobID: created.JobID, clock: clock, items: append([]assetexport.FrozenItem(nil), items...), source: source,
		leases: leases, worker: worker, store: store,
	}
}

func newRuntimeExportDurableReadyFixture(
	t *testing.T,
	db *gorm.DB,
	ring *backupasset.Keyring,
	root string,
	config backupasset.ExportConfig,
) runtimeExportDurableReadyFixture {
	t.Helper()
	clock := time.Now().UTC().Truncate(time.Second)
	queued := newRuntimeExportDurableQueuedFixture(t, db, ring, root, config, clock, runtimeExportDurableFrozenItems(clock, 1))
	attempts, err := assetexport.NewAttemptCoordinator(db, func() time.Time { return queued.clock }, queued.leases)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := attempts.Claim(context.Background(), assetexport.AttemptClaimRequest{
		JobID: queued.jobID, WorkerOwner: "runtime-durable-ready-worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	var itemRows []model.BackupAssetExportItem
	if err := db.Where("job_id = ?", queued.jobID).Order("ordinal ASC").Find(&itemRows).Error; err != nil {
		t.Fatal(err)
	}
	for _, itemRow := range itemRows {
		if _, err := queued.worker.SpoolItem(context.Background(), assetexport.PersistentSpoolItemRequest{
			JobID: queued.jobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemRow.ID,
		}); err != nil {
			_ = queued.store.Close()
			t.Fatal(err)
		}
	}
	sealed, err := queued.worker.SealArchive(context.Background(), assetexport.PersistentSealRequest{
		JobID: queued.jobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken,
	})
	if err != nil {
		_ = queued.store.Close()
		t.Fatal(err)
	}
	if _, err := queued.worker.PublishReady(context.Background(), assetexport.PersistentPublishRequest{
		JobID: queued.jobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ArtifactID: sealed.ArtifactID,
	}); err != nil {
		_ = queued.store.Close()
		t.Fatal(err)
	}
	if err := queued.store.Close(); err != nil {
		t.Fatal(err)
	}
	return runtimeExportDurableReadyFixture{
		jobID: queued.jobID, artifactID: sealed.ArtifactID, locator: sealed.Locator, clock: queued.clock,
		item: queued.items[0], source: queued.source, leases: queued.leases,
	}
}

func newRuntimeExportDurableManager(
	t *testing.T,
	db *gorm.DB,
	ring *backupasset.Keyring,
	values runtimeSettings,
	fixture runtimeExportDurableReadyFixture,
	leases *runtimeExportDurableLeaseSpy,
	terminalizations *atomic.Int32,
) *managedExportRuntime {
	t.Helper()
	manager, err := newManagedExportRuntime(managedExportRuntimeDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(exportRuntimeSettings(values)), Keyring: ring,
		ValidateRoot: func(context.Context, string) error { return nil },
		Build: func(_ context.Context, config backupasset.ExportConfig, store *assetexport.Store) (*managedExportGraph, error) {
			budget, err := assetexport.NewAttemptBudgetService(db, func() time.Time { return fixture.clock })
			if err != nil {
				return nil, err
			}
			broker, err := content.NewAttemptBroker(fixture.source, budget, func() time.Time { return fixture.clock })
			if err != nil {
				return nil, err
			}
			workerCapacity := assetexport.WorkerCapacityLimits{
				WorkerConcurrency: int64(config.WorkerConcurrency),
				UserActiveJobs:    int64(config.UserActiveJobs),
			}
			worker, err := assetexport.NewPersistentWorker(assetexport.PersistentWorkerDependencies{
				DB: db, Keys: ring, Broker: broker, Metadata: runtimeExportDurableMetadataValidator{}, Store: store,
				SourceLeases: leases, WorkerCapacity: &workerCapacity, AttemptWork: assetexport.NewAttemptWorkRegistry(),
				Now: func() time.Time { return fixture.clock },
			})
			if err != nil {
				return nil, err
			}
			attempts, err := assetexport.NewAttemptCoordinatorWithWorkerCapacity(
				db, func() time.Time { return fixture.clock }, workerCapacity, leases,
			)
			if err != nil {
				return nil, err
			}
			runner, err := newManagedExportWorker(managedExportWorkerDependencies{
				DB: db, Attempts: attempts, Worker: worker, Lifecycle: managedExportLifecycleFake{},
				Delivery: managedExportDeliveryFake{}, Budget: &managedExportBudgetFake{}, Cadence: time.Hour,
				HeartbeatInterval: time.Hour, SourceLeaseInterval: time.Hour, BatchSize: 10,
				WorkerConcurrency: 1, WorkerOwner: "runtime-durable-ready-restart",
			})
			if err != nil {
				return nil, err
			}
			return &managedExportGraph{
				store: store, worker: worker, runner: runner, stopAccepting: runner.StopAccepting,
				drain: runner.Drain, run: runner.Run, shutdown: runner.Shutdown, startup: runner.Startup,
				terminalize: func(context.Context) error {
					terminalizations.Add(1)
					return nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("new durable managed Export runtime: %v", err)
	}
	return manager
}

func assertRuntimeExportDurableReadyRows(t *testing.T, db *gorm.DB, fixture runtimeExportDurableReadyFixture) {
	t.Helper()
	var job model.BackupAssetExportJob
	var key model.BackupAssetExportKey
	var artifact model.BackupAssetExportArtifact
	var source model.BackupAssetExportSourceLease
	var foundationLease model.RecoveryPointLease
	if err := db.First(&job, "id = ?", fixture.jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", fixture.jobID).Take(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&artifact, "id = ?", fixture.artifactID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", fixture.jobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&foundationLease, "id = ?", source.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if job.ExecutionState != string(assetexport.ExecutionReady) || job.CleanupState != string(assetexport.CleanupNone) ||
		job.CurrentAttemptID == nil || key.State != "active" || len(key.WrappedDEK) == 0 ||
		artifact.State != "sealed" || artifact.PurgedAt != nil || source.State != "active" ||
		foundationLease.Status != string(backupasset.LeaseActive) || foundationLease.ReleasedAt != nil {
		t.Fatalf("durable ready state job=%+v key=%+v artifact=%+v source=%+v foundation_lease=%+v",
			job, key, artifact, source, foundationLease)
	}
}

func assertRuntimeExportArtifactReadable(t *testing.T, root, locator string) {
	t.Helper()
	store, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sealed, err := store.OpenSealed(locator)
	if err != nil {
		t.Fatal(err)
	}
	if err := sealed.Close(); err != nil {
		t.Fatal(err)
	}
}

type runtimeExportDurableSelectionResolver struct{}

func (runtimeExportDurableSelectionResolver) ResolveExplicit(
	context.Context,
	assetexport.SelectionActor,
	[]backupasset.AssetRef,
	assetexport.SelectionLimits,
) (assetexport.FrozenSelection, error) {
	return assetexport.FrozenSelection{}, assetexport.ErrUnavailable
}

func (runtimeExportDurableSelectionResolver) ResolveSavedSearch(
	context.Context,
	assetexport.SelectionActor,
	string,
	int64,
	assetexport.SelectionLimits,
) (assetexport.FrozenSelection, error) {
	return assetexport.FrozenSelection{}, assetexport.ErrUnavailable
}

func (runtimeExportDurableSelectionResolver) RevalidateFrozenTx(
	context.Context,
	*gorm.DB,
	assetexport.SelectionActor,
	assetexport.FrozenSelection,
) error {
	return nil
}

func (runtimeExportDurableSelectionResolver) RevalidateMetadataTx(
	context.Context,
	*gorm.DB,
	assetexport.FrozenItem,
) error {
	return nil
}

type runtimeExportDurableMetadataValidator struct{}

func (runtimeExportDurableMetadataValidator) RevalidateMetadata(context.Context, assetexport.FrozenItem) error {
	return nil
}

func (runtimeExportDurableMetadataValidator) RevalidateMetadataTx(
	context.Context,
	*gorm.DB,
	assetexport.FrozenItem,
) error {
	return nil
}

type runtimeExportDurableLeaseSpy struct {
	*backupasset.LeaseService
	renewals  atomic.Int32
	takeovers atomic.Int32
}

func (spy *runtimeExportDurableLeaseSpy) RenewTx(
	ctx context.Context,
	tx *gorm.DB,
	fence backupasset.LeaseFence,
) (backupasset.Lease, error) {
	spy.renewals.Add(1)
	return spy.LeaseService.RenewTx(ctx, tx, fence)
}

func (spy *runtimeExportDurableLeaseSpy) TakeoverTx(
	ctx context.Context,
	tx *gorm.DB,
	request backupasset.TakeoverLeaseRequest,
) (backupasset.Lease, error) {
	spy.takeovers.Add(1)
	return spy.LeaseService.TakeoverTx(ctx, tx, request)
}

type runtimeExportDurableSourceResolver struct {
	sources []runtimeExportDurableSource
}

type runtimeExportDurableSource struct {
	item    assetexport.FrozenItem
	payload []byte
}

func (resolver *runtimeExportDurableSourceResolver) OpenContentSource(
	_ context.Context,
	request content.SourceRequest,
) (content.SourceSession, error) {
	if content.ValidateSourceRequest(request) != nil {
		return nil, errors.New("unexpected durable Export source request")
	}
	for _, source := range resolver.sources {
		if request.Ref == source.item.Ref && request.CatalogGenerationID == source.item.CatalogGenerationID &&
			request.ExpectedSource == source.item.SourceFingerprint && request.ExpectedEntry == source.item.EntryFingerprint {
			return runtimeExportDurableSourceSession(source), nil
		}
	}
	return nil, errors.New("unexpected durable Export source request")
}

func (*runtimeExportDurableSourceResolver) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

type runtimeExportDurableSourceSession struct {
	item    assetexport.FrozenItem
	payload []byte
}

func (session runtimeExportDurableSourceSession) Stat() content.SourceStat {
	return content.SourceStat{
		Size: int64(len(session.payload)), MediaType: session.item.MediaType,
		SourceFingerprint: session.item.SourceFingerprint, EntryFingerprint: session.item.EntryFingerprint,
		FingerprintStrong: true,
	}
}

func (runtimeExportDurableSourceSession) Capabilities() content.SourceCapabilities {
	return content.SourceCapabilities{Provider: backupasset.ProviderRestic, Sequential: true}
}

func (session runtimeExportDurableSourceSession) Reader() content.SourceReader {
	return &runtimeExportDurableSourceReader{
		Reader: bytes.NewReader(session.payload), providerBytes: int64(len(session.payload)),
	}
}

func (runtimeExportDurableSourceSession) Revalidate(context.Context) error { return nil }
func (runtimeExportDurableSourceSession) Close() error                     { return nil }

type runtimeExportDurableSourceReader struct {
	*bytes.Reader
	providerBytes int64
}

func (*runtimeExportDurableSourceReader) Close() error                { return nil }
func (reader *runtimeExportDurableSourceReader) ProviderBytes() int64 { return reader.providerBytes }

type exportRuntimeSettings runtimeSettings

func (settings exportRuntimeSettings) GetEffective(key string) string { return settings[key] }

func (settings exportRuntimeSettings) BackupAssetSettingsSnapshot() (map[string]string, error) {
	values := make(map[string]string, len(settings))
	for key, value := range settings {
		values[key] = value
	}
	return values, nil
}

type blockingExportRuntimeSettings struct {
	values          exportRuntimeSettings
	snapshotEntered chan struct{}
	releaseSnapshot chan struct{}
	once            sync.Once
}

func (settings *blockingExportRuntimeSettings) GetEffective(key string) string {
	return settings.values.GetEffective(key)
}

func (settings *blockingExportRuntimeSettings) BackupAssetSettingsSnapshot() (map[string]string, error) {
	settings.once.Do(func() { close(settings.snapshotEntered) })
	<-settings.releaseSnapshot
	return settings.values.BackupAssetSettingsSnapshot()
}

type exportRuntimeManagerFake struct {
	events   *[]string
	service  *managedExportServiceFacade
	delivery *managedExportDeliveryFacade
}

func (fake *exportRuntimeManagerFake) Startup(context.Context) error {
	*fake.events = append(*fake.events, "export-startup")
	return nil
}
func (*exportRuntimeManagerFake) Ready() bool { return true }
func (fake *exportRuntimeManagerFake) TransitionSettings(_ context.Context, enabled bool, _ backupasset.ExportConfig, persist func() error) error {
	*fake.events = append(*fake.events, fmt.Sprintf("export-settings-%t", enabled))
	return persist()
}
func (fake *exportRuntimeManagerFake) Service() *managedExportServiceFacade {
	return fake.service
}
func (fake *exportRuntimeManagerFake) Delivery() *managedExportDeliveryFacade {
	return fake.delivery
}
func (fake *exportRuntimeManagerFake) StopAccepting() {
	*fake.events = append(*fake.events, "export-stop-accepting")
}
func (fake *exportRuntimeManagerFake) Run(context.Context) {
	*fake.events = append(*fake.events, "export-run")
}
func (fake *exportRuntimeManagerFake) Shutdown(context.Context) error {
	*fake.events = append(*fake.events, "export-shutdown")
	return nil
}
func (fake *exportRuntimeManagerFake) PrepareSchemaDown(_ context.Context, down func() error) error {
	*fake.events = append(*fake.events, "export-schema-drain")
	return down()
}

func TestRuntimeExposesCurrentExportAndArchiveMemberServices(t *testing.T) {
	publication := newManagedExportPublication()
	service := &managedExportServiceFacade{publication: publication}
	delivery := &managedExportDeliveryFacade{publication: publication}
	archiveMember := &managedArchiveMemberFacade{publication: publication}
	runtime := &Runtime{
		exportManager:        &exportRuntimeManagerFake{events: &[]string{}, service: service, delivery: delivery},
		archiveMemberService: archiveMember,
	}
	if runtime.ExportService() != service || runtime.ExportDeliveryGateway() != delivery ||
		runtime.ArchiveMemberService() != archiveMember {
		t.Fatalf("runtime accessors service=%p delivery=%p archive=%p", runtime.ExportService(), runtime.ExportDeliveryGateway(), runtime.ArchiveMemberService())
	}
	var nilRuntime *Runtime
	if nilRuntime.ExportService() != nil || nilRuntime.ExportDeliveryGateway() != nil || nilRuntime.ArchiveMemberService() != nil {
		t.Fatal("nil runtime exposed an Export or archive-member service")
	}
}

func TestRuntimeLifecycleComposesExportManagerAndSchemaDownFirst(t *testing.T) {
	events := []string{}
	exportManager := &exportRuntimeManagerFake{events: &events}
	contentManager := &runtimeContentManagerFake{events: &events}
	transitioner := &runtimeFeatureTransitionerFake{events: &events}
	runtime := &Runtime{exportManager: exportManager, contentManager: contentManager, transitioner: transitioner}

	runtime.StopAccepting()
	if err := runtime.PrepareSchemaDown(context.Background(), func() error {
		events = append(events, "schema-down")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"export-stop-accepting", "content-stop-accepting",
		"content-ready-false", "export-schema-drain", "content-schema-drain", "admission-schema-down", "schema-down",
		"export-stop-accepting", "content-stop-accepting", "export-shutdown", "content-shutdown",
	}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("runtime Export lifecycle order=%v want=%v", events, want)
	}
}

type fakeContentTicketIssuer struct {
	ticket content.IssuedTicket
	err    error
	calls  *int
}

func (issuer fakeContentTicketIssuer) Issue(context.Context, content.IssueRequest) (content.IssuedTicket, error) {
	if issuer.calls != nil {
		*issuer.calls++
	}
	return issuer.ticket, issuer.err
}

type fakeTypedDeliveryBranch struct {
	match      bool
	matchErr   error
	matchCall  int
	serveCall  int
	revokeCall int
	revokeErr  error
	body       string
}

func (branch *fakeTypedDeliveryBranch) MatchesDelivery(context.Context, string) (bool, error) {
	branch.matchCall++
	return branch.match, branch.matchErr
}

func (branch *fakeTypedDeliveryBranch) Serve(
	_ context.Context,
	_ content.GatewayRequest,
	writer http.ResponseWriter,
) error {
	branch.serveCall++
	_, _ = writer.Write([]byte(branch.body))
	return nil
}

func (branch *fakeTypedDeliveryBranch) RevokeSession(context.Context, string, string) error {
	branch.revokeCall++
	return branch.revokeErr
}

func TestContentDeliveryMuxRoutesExactlyOneTypedMatchAndRejectsCollisions(t *testing.T) {
	deliveryID := "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name             string
		contentMatch     bool
		exportMatch      bool
		wantBody         string
		wantContentServe int
		wantExportServe  int
		wantErr          error
	}{
		{name: "content only", contentMatch: true, wantBody: "content", wantContentServe: 1},
		{name: "export only", exportMatch: true, wantBody: "export", wantExportServe: 1},
		{name: "no match", wantErr: content.ErrContentNotFound},
		{name: "collision", contentMatch: true, exportMatch: true, wantErr: content.ErrContentNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contentBranch := &fakeTypedDeliveryBranch{match: test.contentMatch, body: "content"}
			exportBranch := &fakeTypedDeliveryBranch{match: test.exportMatch, body: "export"}
			mux, err := newContentDeliveryMux(fakeContentTicketIssuer{}, contentBranch, exportBranch)
			if err != nil {
				t.Fatalf("new mux: %v", err)
			}
			response := httptest.NewRecorder()
			err = mux.Serve(context.Background(), content.GatewayRequest{DeliveryID: deliveryID}, response)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Serve error=%v want=%v", err, test.wantErr)
			}
			if got := response.Body.String(); got != test.wantBody {
				t.Fatalf("response body=%q want=%q", got, test.wantBody)
			}
			if contentBranch.matchCall != 1 || exportBranch.matchCall != 1 {
				t.Fatalf("resolver calls content=%d export=%d", contentBranch.matchCall, exportBranch.matchCall)
			}
			if contentBranch.serveCall != test.wantContentServe || exportBranch.serveCall != test.wantExportServe {
				t.Fatalf("serve calls content=%d export=%d", contentBranch.serveCall, exportBranch.serveCall)
			}
		})
	}
}

func TestContentDeliveryMuxKeepsExistingTicketIssueOnContentBranch(t *testing.T) {
	calls := 0
	want := content.IssuedTicket{Descriptor: content.TicketDescriptor{SchemaVersion: 1, ContentURL: "/content"}}
	mux, err := newContentDeliveryMux(
		fakeContentTicketIssuer{ticket: want, calls: &calls},
		&fakeTypedDeliveryBranch{},
		&fakeTypedDeliveryBranch{},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mux.Issue(context.Background(), content.IssueRequest{})
	if err != nil || got.Descriptor.ContentURL != want.Descriptor.ContentURL || calls != 1 {
		t.Fatalf("ticket=%+v calls=%d err=%v", got, calls, err)
	}
}

func TestContentDeliveryMuxRevokeSessionAlwaysFansOutWithSafeAggregate(t *testing.T) {
	sessionJTI := "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name       string
		contentErr error
		exportErr  error
		wantErr    bool
	}{
		{name: "success"},
		{name: "content failure", contentErr: errors.New("FAKE_RAW_CONTENT_REVOKE_FOR_TEST_ONLY"), wantErr: true},
		{name: "export failure", exportErr: errors.New("FAKE_RAW_EXPORT_REVOKE_FOR_TEST_ONLY"), wantErr: true},
		{name: "both fail", contentErr: errors.New("FAKE_RAW_CONTENT_REVOKE_FOR_TEST_ONLY"), exportErr: errors.New("FAKE_RAW_EXPORT_REVOKE_FOR_TEST_ONLY"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contentBranch := &fakeTypedDeliveryBranch{revokeErr: test.contentErr}
			exportBranch := &fakeTypedDeliveryBranch{revokeErr: test.exportErr}
			mux, err := newContentDeliveryMux(fakeContentTicketIssuer{}, contentBranch, exportBranch)
			if err != nil {
				t.Fatal(err)
			}
			err = mux.RevokeSession(context.Background(), sessionJTI, "logout")
			if contentBranch.revokeCall != 1 || exportBranch.revokeCall != 1 {
				t.Fatalf("revoke calls content=%d export=%d", contentBranch.revokeCall, exportBranch.revokeCall)
			}
			if test.wantErr != errors.Is(err, errContentDeliverySessionRevocation) {
				t.Fatalf("error=%v want_safe=%v", err, test.wantErr)
			}
			if err != nil && (strings.Contains(err.Error(), "FAKE_RAW_") || strings.Contains(err.Error(), sessionJTI)) {
				t.Fatalf("unsafe aggregate error=%q", err)
			}
		})
	}
}

func TestContentBrokerDeliveryBranchClaimsOnly000066AndDelegates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.BackupAssetDeliveryGrant{}); err != nil {
		t.Fatal(err)
	}
	contentID := strings.Repeat("a", 32)
	if err := db.Create(&model.BackupAssetDeliveryGrant{
		ID: strings.Repeat("b", 32), DeliveryID: contentID, State: "active",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	server := &fakeTypedDeliveryBranch{body: "content"}
	branch, err := newContentBrokerDeliveryBranch(db, server)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := branch.MatchesDelivery(context.Background(), contentID)
	if err != nil || !matched {
		t.Fatalf("content match=%v err=%v", matched, err)
	}
	matched, err = branch.MatchesDelivery(context.Background(), strings.Repeat("c", 32))
	if err != nil || matched {
		t.Fatalf("foreign match=%v err=%v", matched, err)
	}
	response := httptest.NewRecorder()
	if err := branch.Serve(context.Background(), content.GatewayRequest{DeliveryID: contentID}, response); err != nil || response.Body.String() != "content" {
		t.Fatalf("serve error=%v body=%q", err, response.Body.String())
	}
	if err := branch.RevokeSession(context.Background(), strings.Repeat("d", 32), "logout"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if server.serveCall != 1 || server.revokeCall != 1 {
		t.Fatalf("serve calls=%d revoke calls=%d", server.serveCall, server.revokeCall)
	}
}

func TestOptionalExportDeliveryBranchDeniesUntilExactGatewayInstalled(t *testing.T) {
	branch := newOptionalTypedDeliveryBranch()
	deliveryID := strings.Repeat("a", 32)
	matched, err := branch.MatchesDelivery(context.Background(), deliveryID)
	if err != nil || matched {
		t.Fatalf("empty branch match=%v err=%v", matched, err)
	}
	if err := branch.Serve(context.Background(), content.GatewayRequest{DeliveryID: deliveryID}, httptest.NewRecorder()); !errors.Is(err, content.ErrContentNotFound) {
		t.Fatalf("empty branch serve error=%v", err)
	}
	if err := branch.RevokeSession(context.Background(), strings.Repeat("b", 32), "logout"); err != nil {
		t.Fatalf("empty branch revoke=%v", err)
	}

	installed := &fakeTypedDeliveryBranch{match: true, body: "export"}
	if err := branch.Install(installed); err != nil {
		t.Fatalf("install: %v", err)
	}
	matched, err = branch.MatchesDelivery(context.Background(), deliveryID)
	if err != nil || !matched {
		t.Fatalf("installed branch match=%v err=%v", matched, err)
	}
	response := httptest.NewRecorder()
	if err := branch.Serve(context.Background(), content.GatewayRequest{DeliveryID: deliveryID}, response); err != nil || response.Body.String() != "export" {
		t.Fatalf("installed branch serve error=%v body=%q", err, response.Body.String())
	}
	replacement := &fakeTypedDeliveryBranch{}
	if err := branch.Install(replacement); err != nil {
		t.Fatalf("replace export gateway: %v", err)
	}
	if current := branch.current(); current != replacement {
		t.Fatalf("current export gateway=%p want replacement=%p", current, replacement)
	}
	branch.Unpublish()
	if current := branch.current(); current != nil {
		t.Fatalf("unpublished export gateway=%p want nil", current)
	}
}

func TestRuntimeBindsStableExportDeliveryFacadeIntoExistingContentService(t *testing.T) {
	publication := newManagedExportPublication()
	exportFacade := &managedExportDeliveryFacade{publication: publication}
	mux, err := newContentDeliveryMux(
		fakeContentTicketIssuer{}, &fakeTypedDeliveryBranch{}, exportFacade,
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{contentService: mux, exportDelivery: exportFacade}
	if runtime.ContentService() != mux || runtime.exportDelivery != exportFacade || mux.exportBranch != exportFacade {
		t.Fatalf("runtime content/export facade binding is not stable")
	}
	deliveryID := strings.Repeat("a", 32)
	response := httptest.NewRecorder()
	if err := runtime.ContentService().Serve(
		context.Background(), content.GatewayRequest{DeliveryID: deliveryID}, response,
	); !errors.Is(err, content.ErrContentNotFound) || response.Body.Len() != 0 {
		t.Fatalf("unpublished facade serve err=%v body=%q", err, response.Body.String())
	}
}

func TestRuntimeArchiveMemberIndexResolverKeepsSucceededNonCurrentJobDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingAttempt{},
		&model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedBlob{},
		&model.BackupAssetDerivedArtifact{}, &model.BackupAssetDerivedBlobReference{},
	); err != nil {
		t.Fatal(err)
	}
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)}
	asset := content.AuthorizedAsset{
		Ref: ref, CatalogGenerationID: strings.Repeat("3", 32), Provider: backupasset.ProviderRestic,
		ProviderCapabilityRevision: 9, SourceFingerprint: strings.Repeat("4", 64),
		EntryFingerprint: strings.Repeat("5", 64), FingerprintStrength: "strong",
		Size: 1024, MediaType: "application/zip",
	}
	payload := []byte(`{"schema_version":1,"entries":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","display_name":"member.txt","size":3,"media_type":"text/plain"}],"expanded_bytes":3,"complete":true}`)
	digest := sha256.Sum256(payload)
	indexRevision := hex.EncodeToString(digest[:])
	jobID, attemptID, setID := strings.Repeat("6", 32), strings.Repeat("7", 32), strings.Repeat("8", 32)
	artifactID, blobID := strings.Repeat("9", 32), strings.Repeat("a", 32)
	finished := now
	deadline := now.Add(time.Hour)
	job := model.BackupAssetProcessingJob{
		ID: jobID, WorkKey: strings.Repeat("b", 64), DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
		RecoveryPointID: ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: ref.EntryID, SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		ProviderCapabilityRevision: asset.ProviderCapabilityRevision, Capability: "archive.inspect",
		CapabilitySchema: "archive.inspect.v1", PipelineFingerprint: "archive-inspect-pipeline-v1",
		OutputProfile: "archive_index_v1", SecurityPolicyRevision: processingSecurityPolicyRevision,
		PriorityClass: string(processing.PriorityInteractive), EffectivePriority: 900,
		State: string(processing.ProcessingSucceeded), TransitionRevision: 2,
		CurrentAttemptID: &attemptID, CurrentArtifactSetID: &setID, IsCurrent: false,
		QueuedAt: now, FinishedAt: &finished, AbsoluteDeadline: deadline, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	attempt := model.BackupAssetProcessingAttempt{
		ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerID: strings.Repeat("c", 32),
		SlotClass: "interactive", State: "succeeded",
		WorkerLeaseExpiresAt: deadline, LastHeartbeatAt: now,
		RecoveryPointLeaseID: strings.Repeat("d", 32), RecoveryPointAttemptID: strings.Repeat("e", 32),
		RecoveryPointFenceHash: strings.Repeat("f", 64), AbsoluteDeadline: deadline,
		IsCurrent: false, StartedAt: now, FinishedAt: &finished, CreatedAt: now, UpdatedAt: now,
	}
	set := model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: jobID, AttemptID: attemptID, WorkKey: job.WorkKey,
		RecoveryPointID: ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: ref.EntryID, SourceFingerprint: asset.SourceFingerprint,
		SecurityPolicyRevision: processingSecurityPolicyRevision, ManifestDigest: strings.Repeat("1", 64),
		State: "active", Completeness: "complete", ArtifactCount: 1, TotalPlaintextBytes: int64(len(payload)),
		CreatedAt: now, UpdatedAt: now,
	}
	blob := model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: indexRevision, PlaintextSize: int64(len(payload)), PhysicalSize: int64(len(payload)),
		CipherFormatVersion: 1, ChunkSize: 64 << 10, ChunkCount: 1, NoncePrefix: []byte("12345678"),
		OpaqueLocator: "index.xrd", WrappedDEK: []byte{1}, EnvelopeNonce: []byte{2}, DerivedKEKVersion: 1,
		State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	artifact := model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: "metadata", MediaType: "application/json",
		PlaintextSize: int64(len(payload)), PlaintextDigest: indexRevision, Completeness: "complete",
		CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: blobID, CreatedAt: now,
	}
	reference := model.BackupAssetDerivedBlobReference{
		ID: strings.Repeat("0", 32), BlobID: blobID, ArtifactID: artifactID,
		RecoveryPointID: ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: ref.EntryID, SourceFingerprint: asset.SourceFingerprint, State: "active", CreatedAt: now, UpdatedAt: now,
	}
	for _, row := range []any{&job, &attempt, &set, &blob, &artifact, &reference} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	for _, target := range []struct {
		model any
		id    string
	}{{&model.BackupAssetProcessingJob{}, jobID}, {&model.BackupAssetProcessingAttempt{}, attemptID}} {
		if err := db.Model(target.model).Where("id = ?", target.id).Update("is_current", false).Error; err != nil {
			t.Fatalf("close succeeded %T: %v", target.model, err)
		}
	}
	driftProviderRevision := false
	resolver, err := content.NewDerivedRepresentationResolver(
		db,
		func(_ context.Context, request content.DerivedArtifactRead, destination io.Writer) error {
			if request.ArtifactID != artifactID {
				return errors.New("unexpected artifact")
			}
			if driftProviderRevision {
				if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", jobID).
					Update("provider_capability_revision", asset.ProviderCapabilityRevision+1).Error; err != nil {
					return err
				}
			}
			_, writeErr := destination.Write(payload)
			return writeErr
		},
		func(context.Context, string, string) (string, error) { return job.PipelineFingerprint, nil },
		func(context.Context, content.AuthorizedAsset) (bool, error) { return true, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := (runtimeArchiveMemberIndexResolver{db: db, resolver: resolver}).Resolve(
		context.Background(), asset, indexRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ArtifactID != artifactID || binding.Revision != indexRevision ||
		!binding.AbsoluteExpiresAt.Equal(deadline) || len(binding.Members) != 1 || binding.Members[0].Ordinal != 0 {
		t.Fatalf("runtime archive index binding=%+v", binding)
	}
	t.Run("classifies provider capability revision drift as source drift", func(t *testing.T) {
		driftProviderRevision = true
		if _, err := (runtimeArchiveMemberIndexResolver{db: db, resolver: resolver}).Resolve(
			context.Background(), asset, indexRevision,
		); !errors.Is(err, backupasset.ErrNotFound) {
			t.Fatalf("provider capability revision drift error=%v, want not found", err)
		}
	})
}

func TestRuntimeArchiveMemberQueuedMaintenanceTerminalizesIndexCapabilityRevisionDriftWithoutPoll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupAssetArchiveMemberRequest{}, &model.BackupAssetExportQuotaBucket{},
		&model.BackupAssetProcessingInterest{},
		&model.BackupAssetProcessingJob{}, &model.BackupAssetDerivedArtifactSet{},
		&model.BackupAssetDerivedArtifact{}, &model.BackupAssetDerivedBlob{},
		&model.BackupAssetExportDeliveryGrant{},
	); err != nil {
		t.Fatal(err)
	}
	asset := content.AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		CatalogGenerationID: strings.Repeat("3", 32), Provider: backupasset.ProviderRestic,
		ProviderCapabilityRevision: 9, SourceFingerprint: "source-fingerprint-v1",
		EntryFingerprint: "entry-fingerprint-v1", FingerprintStrength: "strong", Size: 1024, MediaType: "application/zip",
	}
	memberID := strings.Repeat("4", 32)
	index := processing.ArchiveMemberIndexBinding{
		ArtifactID: strings.Repeat("5", 32), Revision: strings.Repeat("6", 64),
		PipelineFingerprint: "archive-inspect-pipeline-v1", SecurityPolicyRevision: processingSecurityPolicyRevision,
		AbsoluteExpiresAt: now.Add(time.Hour),
		Members: []processing.ArchiveMemberIndexEntry{{
			OpaqueID: memberID, Ordinal: 0, DisplayName: "member.txt", Size: 3, MediaType: "text/plain",
		}},
	}
	coordinator := &runtimeArchiveMemberMaintenanceCoordinator{}
	service, err := processing.NewArchiveMemberService(processing.ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: processingRuntimeAssetAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (processing.ArchiveMemberIndexBinding, error) {
			return index, nil
		},
		RevalidateIndex: func(context.Context, model.BackupAssetArchiveMemberRequest) (processing.ArchiveMemberIndexBinding, error) {
			return processing.ArchiveMemberIndexBinding{}, backupasset.ErrNotFound
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (processing.ArchiveMemberProcessingAuthority, error) {
			return processing.ArchiveMemberProcessingAuthority{
				ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
				SecurityPolicyRevision:     processingSecurityPolicyRevision,
			}, nil
		},
		ResolveExtractCapability: func(context.Context) (processing.CapabilityAdvertisement, error) {
			return processing.CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), processing.ArchiveMemberCreateRequest{
		Actor:          content.DeliveryActor{UserID: 42, Username: "admin", Role: "admin"},
		Ref:            asset.Ref,
		IdempotencyKey: "runtime-capability-revision-drift-key",
		IndexRevision:  index.Revision,
		MemberChain:    []string{memberID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("queued capability-drift reconciliation=%d err=%v", reconciled, err)
	}

	var request model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(processing.ArchiveMemberFailed) ||
		request.ErrorCategory != string(processing.ArchiveFailureUnavailable) || request.FinishedAt == nil {
		t.Fatalf("queued capability drift remained retryable: %+v", request)
	}
	if coordinator.requestCalls != 0 || coordinator.removeCalls != 0 {
		t.Fatalf("source drift created or removed unexpected Processing interest: requests=%d removals=%d", coordinator.requestCalls, coordinator.removeCalls)
	}
	var interestCount int64
	if err := db.Model(&model.BackupAssetProcessingInterest{}).
		Where("owner_key = ? AND active = ?", "archive-member:"+created.RequestID, true).
		Count(&interestCount).Error; err != nil {
		t.Fatal(err)
	}
	if interestCount != 0 {
		t.Fatalf("source-drift request retained active Processing interest count=%d", interestCount)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 0 {
		t.Fatalf("terminal capability-drift request retried without Poll: reconciled=%d err=%v", reconciled, err)
	}
}

type runtimeArchiveMemberMaintenanceCoordinator struct {
	requestCalls int
	removeCalls  int
}

func (fake *runtimeArchiveMemberMaintenanceCoordinator) RequestWork(
	context.Context,
	processing.WorkRequest,
) (processing.WorkResult, error) {
	fake.requestCalls++
	return processing.WorkResult{}, errors.New("source drift must be terminalized before requesting Processing work")
}

func (fake *runtimeArchiveMemberMaintenanceCoordinator) RemoveInterest(
	context.Context,
	string,
	processing.InterestOwnerKind,
	string,
	processing.InterestRemovedReason,
) error {
	fake.removeCalls++
	return nil
}

func TestRuntimeArchiveMemberAuthorityResolverClassifiesMissingOrDemotedOwnerAsAuthorizationLoss(t *testing.T) {
	request := model.BackupAssetArchiveMemberRequest{
		OwnerUserID: 42, RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64),
		CatalogGenerationID: strings.Repeat("3", 32), SourceFingerprint: "source-fingerprint-v1", EntryFingerprint: "entry-fingerprint-v1",
	}
	asset := content.AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID},
		CatalogGenerationID: request.CatalogGenerationID, SourceFingerprint: request.SourceFingerprint,
		EntryFingerprint: request.EntryFingerprint, ProviderCapabilityRevision: 1,
	}
	for _, testCase := range []struct {
		name string
		user *model.User
	}{
		{name: "missing owner"},
		{name: "demoted owner", user: &model.User{ID: request.OwnerUserID, Username: "archive-viewer", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "viewer"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openRuntimeTestDB(t)
			if err := db.AutoMigrate(&model.User{}); err != nil {
				t.Fatal(err)
			}
			if testCase.user != nil {
				if err := db.Create(testCase.user).Error; err != nil {
					t.Fatal(err)
				}
			}
			resolver := runtimeArchiveMemberAuthorityResolver{
				db: db, authorize: processingRuntimeAssetAuthorizerFake{asset: asset},
			}
			if _, err := resolver.resolveAsset(context.Background(), request); !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("owner authorization loss error=%v, want forbidden", err)
			}
		})
	}
}

func TestRuntimeArchiveMemberAuthorityResolverClassifiesBoundSourceDriftAsNotFound(t *testing.T) {
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	request := model.BackupAssetArchiveMemberRequest{
		OwnerUserID: 42, RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64),
		CatalogGenerationID: strings.Repeat("3", 32), SourceFingerprint: "source-fingerprint-v1", EntryFingerprint: "entry-fingerprint-v1",
	}
	if err := db.Create(&model.User{ID: request.OwnerUserID, Username: "archive-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	resolver := runtimeArchiveMemberAuthorityResolver{
		db: db,
		authorize: processingRuntimeAssetAuthorizerFake{asset: content.AuthorizedAsset{
			Ref:                 backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID},
			CatalogGenerationID: request.CatalogGenerationID, SourceFingerprint: request.SourceFingerprint,
			EntryFingerprint: "entry-fingerprint-v2", ProviderCapabilityRevision: 1,
		}},
	}
	if _, err := resolver.resolveAsset(context.Background(), request); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("bound source drift error=%v, want not found", err)
	}
}

func TestContentDeliveryMuxExportRouteRedactsAccessLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previous := logger.Log
	logger.Log = zerolog.New(&output)
	t.Cleanup(func() { logger.Log = previous })

	deliveryID := strings.Repeat("d", 32)
	cookieSecret := "FAKE_EXPORT_COOKIE_FOR_TEST_ONLY"
	mux, err := newContentDeliveryMux(
		fakeContentTicketIssuer{},
		&fakeTypedDeliveryBranch{},
		&fakeTypedDeliveryBranch{match: true, body: "export"},
	)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(middleware.StructuredLogger())
	router.GET("/api/v1/asset-content/:deliveryId", func(c *gin.Context) {
		serveErr := mux.Serve(c.Request.Context(), content.GatewayRequest{
			DeliveryID: c.Param("deliveryId"), Method: c.Request.Method,
			RawCookie: c.GetHeader("Cookie"),
		}, c.Writer)
		if serveErr != nil {
			c.Status(http.StatusNotFound)
		}
	})
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/asset-content/"+deliveryID+"?ticket=FAKE_EXPORT_QUERY_FOR_TEST_ONLY", nil)
	request.Header.Set("Cookie", cookieSecret)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "export" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	logs := output.String()
	if !strings.Contains(logs, `"path":"/api/v1/asset-content/:deliveryId"`) {
		t.Fatalf("logs=%s", logs)
	}
	for _, forbidden := range []string{deliveryID, cookieSecret, "FAKE_EXPORT_QUERY_FOR_TEST_ONLY"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("access log leaked %q: %s", forbidden, logs)
		}
	}
}
