package runtime

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

func TestRuntimeRsyncCompletionRefreshesCatalogBeforeFirstColdPreview(t *testing.T) {
	if stdruntime.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_PREVIEW_FIRST_RUNTIME_DATA_KEY_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	ctx := context.Background()
	clock := time.Now().UTC().Add(time.Hour)
	// Exercise the production SQLite WAL/transaction configuration: the shared-
	// cache in-memory helper gives polling readers table locks unlike production.
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "preview.db")})
	if err != nil {
		t.Fatalf("open preview runtime database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.SystemSetting{}, &model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{},
		&model.BackupAssetDeliveryUsage{}, &model.RecoveryPointLease{},
		&model.User{}, &model.Node{}, &model.Task{},
		&model.BackupRepository{}, &model.RepositoryAccessBinding{}, &model.TaskRepositoryLink{},
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{},
		&model.CatalogGeneration{}, &model.CatalogEntry{}, &model.WrappedDomainKey{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetAuditCheckpoint{}, &model.BackupAssetAuditEvent{},
		&model.BackupAssetInstallation{}, &model.BackupAssetInventoryRun{},
		&model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatalf("migrate runtime preview schema: %v", err)
	}
	if err := db.Create(&model.User{ID: 1, Username: "preview-admin", PasswordHash: "unused", Role: "admin", TokenVersion: 1}).Error; err != nil {
		t.Fatalf("seed preview user: %v", err)
	}

	settingsService := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.enabled":               "true",
		"backup_assets.content_cache_enabled": "false",
	} {
		if err := settingsService.Update(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	readinessDigest := strings.Repeat("e", 64)
	if err := db.Create(&model.BackupAssetInstallation{
		ID: "preview-first-installation", Slot: 1, Class: string(ga.InstallationFresh),
		Readiness: string(ga.ReadinessReady), InventoryDigest: readinessDigest,
		CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed backup asset installation readiness: %v", err)
	}
	if err := db.Create(&model.BackupAssetInventoryRun{
		ID: "preview-first-inventory-run", Digest: readinessDigest,
		Status: ga.InventoryRunComplete, CountsJSON: "{}",
		CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed backup asset inventory readiness: %v", err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{}, ContentMetrics: content.NoopMetrics{},
		SessionRevocations: &runtimeSessionRevocationsFake{}, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("construct backup asset runtime: %v", err)
	}
	if _, err := runtime.keyring.EnsureRequiredDomains(ctx); err != nil {
		t.Fatalf("ensure runtime key domains: %v", err)
	}
	if err := runtime.admission.InitializeManaged(ctx); err != nil {
		t.Fatalf("initialize managed admission: %v", err)
	}
	if err := runtime.contentManager.Startup(ctx); err != nil {
		t.Fatalf("start content runtime: %v", err)
	}
	t.Cleanup(func() {
		_ = runtime.Shutdown(context.Background())
	})

	rootBase := t.TempDir()
	targetA := filepath.Join(rootBase, "target-a")
	targetB := filepath.Join(rootBase, "target-b")
	sourceA := filepath.Join(rootBase, "source-a")
	sourceB := filepath.Join(rootBase, "source-b")
	for _, directory := range []string{targetA, targetB, sourceA, sourceB} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create local Rsync directory %q: %v", directory, err)
		}
	}
	composeAInitial := []byte("services:\n  api:\n    image: nginx:first\n")
	composeAInFlight := []byte("services:\n  api:\n    image: nginx:in-flight\n")
	composeAUpdated := []byte("services:\n  api:\n    image: nginx:updated-after-overlap\n")
	composeB := []byte("services:\n  worker:\n    image: nginx:second-task\n")
	for _, fixture := range []struct {
		root    string
		source  string
		content []byte
	}{
		{targetA, sourceA, composeAInitial},
		{targetB, sourceB, composeB},
	} {
		if err := os.WriteFile(filepath.Join(fixture.root, "docker-compose.yml"), fixture.content, 0o600); err != nil {
			t.Fatalf("seed target content: %v", err)
		}
		if err := os.WriteFile(filepath.Join(fixture.source, "docker-compose.yml"), fixture.content, 0o600); err != nil {
			t.Fatalf("seed source content: %v", err)
		}
	}

	node := model.Node{
		Name: "preview-first-node", Host: "127.0.0.1", Port: 22, Username: "root",
		AuthType: "password", Password: "FAKE_PREVIEW_FIRST_NODE_PASSWORD_FOR_TEST_ONLY",
		BasePath: rootBase, BackupDir: filepath.Join(rootBase, "node-backup"),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create shared-node fixture: %v", err)
	}
	tasks := []model.Task{
		{Name: "preview-first-task-a", NodeID: node.ID, ExecutorType: string(backupasset.ProviderRsync),
			RsyncSource: sourceA, RsyncTarget: targetA, Status: "pending", Enabled: true},
		{Name: "preview-first-task-b", NodeID: node.ID, ExecutorType: string(backupasset.ProviderRsync),
			RsyncSource: sourceB, RsyncTarget: targetB, Status: "pending", Enabled: true},
	}
	for index := range tasks {
		if err := db.Create(&tasks[index]).Error; err != nil {
			t.Fatalf("create task %d: %v", index, err)
		}
	}

	connected := make([]backuprepository.ConnectResult, len(tasks))
	for index := range tasks {
		result, connectErr := runtime.RepositoryService().Connect(ctx, backuprepository.ConnectRequest{TaskID: tasks[index].ID}, backuprepository.RequestContext{
			CorrelationID: "preview-first-connect-" + string(rune('a'+index)),
		})
		if connectErr != nil || result.MutablePoint == nil {
			t.Fatalf("connect task %d result=%+v err=%v", index, result, connectErr)
		}
		connected[index] = result
	}
	if connected[0].Repository.ID == connected[1].Repository.ID || connected[0].MutablePoint.ID == connected[1].MutablePoint.ID {
		t.Fatalf("same-node tasks did not receive isolated repository/point identities: first=%+v second=%+v", connected[0], connected[1])
	}

	initialGenerations := make([]model.CatalogGeneration, len(tasks))
	for index := range tasks {
		generation, buildErr := runtime.catalogIndexer.Build(ctx, catalog.BuildRequest{
			RepositoryID: connected[index].Repository.ID, RecoveryPointID: connected[index].MutablePoint.ID,
			CorrelationID: "preview-first-initial-" + string(rune('a'+index)),
		})
		if buildErr != nil || generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
			t.Fatalf("initial Catalog build %d generation=%+v err=%v", index, generation, buildErr)
		}
		initialGenerations[index] = generation
	}

	entryAInitial := loadPreviewFirstCurrentEntry(t, runtime.CatalogService(), db, connected[0].MutablePoint.ID)
	entryBInitial := loadPreviewFirstCurrentEntry(t, runtime.CatalogService(), db, connected[1].MutablePoint.ID)
	if entryAInitial.generationID != initialGenerations[0].ID || entryBInitial.generationID != initialGenerations[1].ID {
		t.Fatalf("Catalog service selected unexpected initial generations: A=%+v B=%+v", entryAInitial, entryBInitial)
	}
	if entryAInitial.modelEntry.Size != int64(len(composeAInitial)) || entryBInitial.modelEntry.Size != int64(len(composeB)) {
		t.Fatalf("initial Catalog sizes A=%d B=%d", entryAInitial.modelEntry.Size, entryBInitial.modelEntry.Size)
	}
	catalogConfig, err := runtime.foundation.CatalogConfig()
	if err != nil {
		t.Fatalf("load runtime Catalog config: %v", err)
	}
	pausedFactory := &previewFirstPausingCatalogFactory{
		inner:   runtime.RepositoryService(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(pausedFactory.Release)
	catalogLease, err := backupasset.NewLeaseService(db, func() time.Time { return clock }, catalogConfig.Lease)
	if err != nil {
		t.Fatalf("construct overlap Catalog lease: %v", err)
	}
	pausedIndexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: db, Factory: pausedFactory, Lease: catalogLease, IdentityKeys: runtime.keyring,
		Now: func() time.Time { return clock },
		Config: catalog.IndexerConfig{
			BatchSize: catalogConfig.BatchSize, BuildTimeout: catalogConfig.BuildTimeout,
			MaxEntries: catalogConfig.MaxEntries, HeartbeatInterval: catalogConfig.Lease.Heartbeat,
		},
	})
	if err != nil {
		t.Fatalf("construct overlap Catalog indexer: %v", err)
	}
	runtime.catalogIndexer = pausedIndexer
	runtime.catalogWorker.backend = pausedIndexer
	runtime.catalogWorker.after = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	go runtime.catalogWorker.Run(workerCtx)

	rootInfo, err := os.Stat(targetA)
	if err != nil {
		t.Fatalf("stat task A target before controlled backup write: %v", err)
	}
	writePreviewFirstPayload(t, sourceA, targetA, composeAInFlight)
	var pointBefore model.RecoveryPoint
	if err := db.First(&pointBefore, "id = ?", connected[0].MutablePoint.ID).Error; err != nil {
		t.Fatalf("load task A point before controlled backup write: %v", err)
	}
	if err := runtime.RepositoryService().ObserveBackupSourceCompletion(ctx, tasks[0].ID); err != nil {
		t.Fatalf("observe task A first backup source completion: %v", err)
	}
	select {
	case <-pausedFactory.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for in-flight Catalog build to open the real source")
	}
	var inFlight model.CatalogGeneration
	if err := db.Where("recovery_point_id = ? AND state = ? AND is_active = ?",
		connected[0].MutablePoint.ID, catalog.GenerationBuilding, false).
		Order("generation DESC").First(&inFlight).Error; err != nil {
		t.Fatalf("load paused task A Catalog generation: %v", err)
	}
	if inFlight.Generation <= initialGenerations[0].Generation {
		t.Fatalf("worker did not create a newer in-flight Catalog generation: initial=%+v in-flight=%+v", initialGenerations[0], inFlight)
	}

	writePreviewFirstPayload(t, sourceA, targetA, composeAUpdated)
	if err := os.Chtimes(targetA, rootInfo.ModTime(), rootInfo.ModTime()); err != nil {
		t.Fatalf("preserve task A root metadata after overlapping write: %v", err)
	}
	if err := runtime.RepositoryService().ObserveBackupSourceCompletion(ctx, tasks[0].ID); err != nil {
		t.Fatalf("observe task A overlapping backup source completion: %v", err)
	}
	var pointAfter model.RecoveryPoint
	if err := db.First(&pointAfter, "id = ?", connected[0].MutablePoint.ID).Error; err != nil {
		t.Fatalf("load task A point after completion observation: %v", err)
	}
	if pointAfter.SourceFingerprint != pointBefore.SourceFingerprint {
		t.Fatalf("child writes changed mutable root fingerprint: before=%q after=%q", pointBefore.SourceFingerprint, pointAfter.SourceFingerprint)
	}
	var invalidated model.CatalogGeneration
	if err := db.First(&invalidated, "id = ?", inFlight.ID).Error; err != nil {
		t.Fatalf("load invalidated in-flight task A Catalog generation: %v", err)
	}
	if invalidated.State != string(catalog.GenerationSuperseded) || invalidated.IsActive ||
		invalidated.FinishedAt == nil ||
		(inFlight.FinishedAt != nil && !previewFirstOptionalTimeEqual(invalidated.FinishedAt, inFlight.FinishedAt)) {
		t.Fatalf("in-flight task A Catalog generation was not superseded with finish evidence: %+v", invalidated)
	}
	pausedFactory.Release()

	updatedGeneration, updatedEntry := waitForPreviewFirstCatalog(t, db, connected[0].MutablePoint.ID, inFlight.ID, composeAUpdated)
	if updatedGeneration.Generation <= inFlight.Generation ||
		updatedGeneration.SourceFingerprint != initialGenerations[0].SourceFingerprint ||
		updatedEntry.EntryID != entryAInitial.modelEntry.EntryID {
		t.Fatalf("task A replacement Catalog lost overlap lineage: in-flight=%+v replacement=%+v entry=%+v", inFlight, updatedGeneration, updatedEntry)
	}
	var staleBuilder model.CatalogGeneration
	if err := db.First(&staleBuilder, "id = ?", inFlight.ID).Error; err != nil {
		t.Fatalf("reload stale task A Catalog generation: %v", err)
	}
	if staleBuilder.IsActive || staleBuilder.State != string(catalog.GenerationSuperseded) {
		t.Fatalf("stale task A Catalog builder activated after source completion: %+v", staleBuilder)
	}

	entryA := loadPreviewFirstCurrentEntry(t, runtime.CatalogService(), db, connected[0].MutablePoint.ID)
	if entryA.generationID != updatedGeneration.ID || entryA.modelEntry.Size != int64(len(composeAUpdated)) {
		t.Fatalf("Catalog service did not select refreshed task A entry: %+v", entryA)
	}
	issueAndServePreviewFirst(t, runtime.ContentBroker(), db, entryA, clock, composeAUpdated)

	entryB := loadPreviewFirstCurrentEntry(t, runtime.CatalogService(), db, connected[1].MutablePoint.ID)
	if entryB.generationID != entryBInitial.generationID || entryB.modelEntry.Size != int64(len(composeB)) {
		t.Fatalf("task B Catalog changed while task A refreshed: initial=%+v current=%+v", entryBInitial, entryB)
	}
	issueAndServePreviewFirst(t, runtime.ContentBroker(), db, entryB, clock, composeB)
}

type previewFirstCatalogEntry struct {
	ref               backupasset.AssetRef
	generationID      string
	sourceFingerprint string
	modelEntry        model.CatalogEntry
}

func loadPreviewFirstCurrentEntry(t *testing.T, service *catalog.Service, db *gorm.DB, pointID string) previewFirstCatalogEntry {
	t.Helper()
	scope := catalog.AuthorizationScope{Role: "admin", UserID: 1}
	page, err := service.ListEntries(context.Background(), pointID, scope, catalog.EntryListRequest{Limit: 10})
	if err != nil {
		t.Fatalf("list current Catalog entries for point %s: %v", pointID, err)
	}
	if len(page.Items) != 1 || page.Items[0].Name != "docker-compose.yml" || page.Items[0].EntryType != backupasset.CatalogEntryFile {
		t.Fatalf("current Catalog entries for point %s=%+v", pointID, page.Items)
	}
	entryDTO, err := service.GetEntry(context.Background(), pointID, page.Items[0].EntryID, scope)
	if err != nil {
		t.Fatalf("get current Catalog entry for point %s: %v", pointID, err)
	}
	var generation model.CatalogGeneration
	if err := db.Where("recovery_point_id = ? AND state = ? AND is_active = ?", pointID, catalog.GenerationComplete, true).First(&generation).Error; err != nil {
		t.Fatalf("load active Catalog generation for point %s: %v", pointID, err)
	}
	var entry model.CatalogEntry
	if err := db.Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?", generation.ID, pointID, entryDTO.EntryID).First(&entry).Error; err != nil {
		t.Fatalf("load current Catalog entry model for point %s: %v", pointID, err)
	}
	if entry.NormalizedPath != "docker-compose.yml" || entry.EntryType != string(backupasset.CatalogEntryFile) || entry.Size != entryDTO.Size {
		t.Fatalf("current Catalog entry mismatch dto=%+v model=%+v", entryDTO, entry)
	}
	return previewFirstCatalogEntry{
		ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entry.EntryID}, generationID: generation.ID,
		sourceFingerprint: generation.SourceFingerprint, modelEntry: entry,
	}
}

func waitForPreviewFirstCatalog(t *testing.T, db *gorm.DB, pointID, oldGenerationID string, expected []byte) (model.CatalogGeneration, model.CatalogEntry) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var generations []model.CatalogGeneration
		if err := db.Where("recovery_point_id = ?", pointID).Order("generation DESC").Find(&generations).Error; err != nil {
			t.Fatalf("poll task A Catalog generations: %v", err)
		}
		for _, generation := range generations {
			if generation.ID == oldGenerationID || generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
				continue
			}
			var entry model.CatalogEntry
			result := db.Where("generation_id = ? AND recovery_point_id = ? AND normalized_path = ?", generation.ID, pointID, "docker-compose.yml").First(&entry)
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				continue
			}
			if result.Error != nil {
				t.Fatalf("poll refreshed task A Catalog entry: %v", result.Error)
			}
			if generation.Generation > 1 && entry.Size == int64(len(expected)) && generation.SourceFingerprint != "" {
				return generation, entry
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for refreshed task A Catalog generation")
	return model.CatalogGeneration{}, model.CatalogEntry{}
}

func issueAndServePreviewFirst(t *testing.T, broker *content.Broker, db *gorm.DB, entry previewFirstCatalogEntry, now time.Time, expected []byte) {
	t.Helper()
	actor := content.DeliveryActor{UserID: 1, Username: "preview-admin", Role: "admin"}
	session := content.DeliverySession{
		JTI: strings.Repeat("a", 32), UserID: actor.UserID, Role: actor.Role, TokenVersion: 1,
		ExpiresAt: now.Add(time.Hour),
	}
	ref := entry.ref
	ticket, err := broker.Issue(context.Background(), content.IssueRequest{
		Actor: actor, Session: session, Ref: ref,
		Resource: content.DeliveryResource{Kind: content.DeliveryResourceBackupAsset, Asset: &ref},
		Action:   content.DeliveryPreview, PreviewIntent: content.PreviewIntentSafePreviewV1,
	})
	if err != nil {
		t.Fatalf("first cold safe preview Issue for %s: %v", ref.RecoveryPointID, err)
	}
	if ticket.Cookie == nil || ticket.Cookie.Value == "" || ticket.Descriptor.Renderer != content.RendererPlainText ||
		ticket.Descriptor.Profile != content.ProfileTextV2 || ticket.Descriptor.Truncated ||
		ticket.Descriptor.ContentLength != int64(len(expected)) {
		t.Fatalf("safe preview ticket=%+v", ticket)
	}
	deliveryID := path.Base(ticket.Cookie.Path)
	var grant model.BackupAssetDeliveryGrant
	if err := db.Where("delivery_id = ?", deliveryID).First(&grant).Error; err != nil {
		t.Fatalf("load issued preview grant: %v", err)
	}
	writer := &previewFirstDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := broker.Serve(context.Background(), content.GatewayRequest{
		DeliveryID: grant.DeliveryID, Method: http.MethodGet,
		RawCookie: ticket.Cookie.Name + "=" + ticket.Cookie.Value,
	}, writer); err != nil {
		t.Fatalf("first cold safe preview Serve for %s: %v", ref.RecoveryPointID, err)
	}
	if writer.Code != http.StatusOK || !bytes.Equal(writer.Body.Bytes(), expected) {
		t.Fatalf("safe preview response status=%d bytes=%d body=%q want=%q", writer.Code, writer.Body.Len(), writer.Body.Bytes(), expected)
	}
}

type previewFirstDeadlineRecorder struct {
	*httptest.ResponseRecorder
}

func (*previewFirstDeadlineRecorder) SetWriteDeadline(time.Time) error { return nil }
func writePreviewFirstPayload(t *testing.T, sourceRoot, targetRoot string, expected []byte) {
	t.Helper()
	sourcePath := filepath.Join(sourceRoot, "docker-compose.yml")
	targetPath := filepath.Join(targetRoot, "docker-compose.yml")
	if err := os.WriteFile(sourcePath, expected, 0o600); err != nil {
		t.Fatalf("write controlled source payload: %v", err)
	}
	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read controlled source payload: %v", err)
	}
	if err := os.WriteFile(targetPath, payload, 0o600); err != nil {
		t.Fatalf("write controlled target payload: %v", err)
	}
}

type previewFirstPausingCatalogFactory struct {
	inner   catalog.PointReadFactory
	entered chan struct{}
	release chan struct{}

	enteredOnce sync.Once
	releaseOnce sync.Once
}

func (factory *previewFirstPausingCatalogFactory) OpenCatalogRead(
	ctx context.Context,
	request catalog.PointReadRequest,
) (provider.CatalogReadSession, error) {
	if factory == nil || factory.inner == nil {
		return nil, errors.New("overlap Catalog factory unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := factory.inner.OpenCatalogRead(ctx, request)
	if err != nil {
		return nil, err
	}
	factory.enteredOnce.Do(func() { close(factory.entered) })
	select {
	case <-factory.release:
		return session, nil
	case <-ctx.Done():
		_ = session.Close()
		return nil, ctx.Err()
	}
}

func (factory *previewFirstPausingCatalogFactory) RefreshMutableObservation(
	ctx context.Context,
	request catalog.PointReadRequest,
) error {
	if factory == nil || factory.inner == nil {
		return errors.New("overlap Catalog factory unavailable")
	}
	return factory.inner.RefreshMutableObservation(ctx, request)
}

func (factory *previewFirstPausingCatalogFactory) Release() {
	if factory == nil {
		return
	}
	factory.releaseOnce.Do(func() { close(factory.release) })
}

func previewFirstOptionalTimeEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
