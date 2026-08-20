package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/retention"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLifecycleDependentCleanupAggregateSeparatesPrepareAndOrderedCleanup(t *testing.T) {
	order := make([]string, 0, 10)
	searchCleaned := false
	owners := []*retentionLifecycleOwnerFake{
		{name: "Content", order: &order, active: 1, leases: 1, payload: true},
		{name: "Catalog", order: &order, active: 1, leases: 1, payload: true},
		{name: "Search", order: &order, active: 1, leases: 1, payload: true, searchCleaned: &searchCleaned},
		{name: "Processing", order: &order, active: 1, leases: 1, payload: true, requireSearch: &searchCleaned},
		{name: "Export", order: &order, active: 1, leases: 1, payload: true},
		{name: "Recovery", order: &order, active: 1, leases: 1, payload: true, preserveCleanupPayload: true},
	}
	overlayOwner := &retentionLifecycleOverlayFake{
		order: &order,
		results: []overlay.LifecycleResult{
			{Favorites: 1},
			{},
		},
	}
	aggregate, err := NewRetentionLifecycle(RetentionLifecycleDependencies{
		Content: owners[0], Catalog: owners[1], Search: owners[2], Processing: owners[3],
		Export: owners[4], Recovery: owners[5], Overlay: overlayOwner,
		OverlayBatchSize: 1, OverlayMaxPasses: 3,
	})
	if err != nil {
		t.Fatalf("NewRetentionLifecycle: %v", err)
	}
	request := retention.LifecyclePointRequest{
		RecoveryPointID: "11111111111111111111111111111111",
		AttemptID:       "22222222222222222222222222222222",
		Operation:       backupasset.LifecycleRetentionExpire,
	}
	if err := aggregate.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("prepare aggregate: %v", err)
	}
	wantPrepare := []string{"Content:prepare", "Catalog:prepare", "Search:prepare", "Processing:prepare", "Export:prepare", "Recovery:prepare"}
	if !reflect.DeepEqual(order, wantPrepare) {
		t.Fatalf("prepare order=%v, want %v", order, wantPrepare)
	}
	for _, owner := range owners {
		if owner.active != 0 || owner.leases != 0 || !owner.payload {
			t.Fatalf("%s prepare active/leases/payload=%d/%d/%t, want 0/0/preserved", owner.name, owner.active, owner.leases, owner.payload)
		}
	}
	if overlayOwner.calls != 0 {
		t.Fatalf("prepare reconciled Overlay %d times", overlayOwner.calls)
	}

	order = order[:0]
	if err := aggregate.CleanupRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("cleanup aggregate: %v", err)
	}
	wantCleanup := []string{
		"Content:cleanup", "Catalog:cleanup", "Search:cleanup", "Processing:cleanup",
		"Export:cleanup", "Recovery:cleanup", "Overlay:cleanup", "Overlay:cleanup",
	}
	if !reflect.DeepEqual(order, wantCleanup) {
		t.Fatalf("cleanup order=%v, want %v", order, wantCleanup)
	}
	for _, owner := range owners[:5] {
		if owner.payload {
			t.Fatalf("%s cleanup retained owner payload", owner.name)
		}
	}
	if !owners[5].payload {
		t.Fatal("Recovery cleanup destroyed Child13-owned result payload")
	}
	if !searchCleaned || overlayOwner.calls != 2 || overlayOwner.reason != overlay.SourceExpiring {
		t.Fatalf("cleanup search/Overlay proof=%t calls=%d reason=%q", searchCleaned, overlayOwner.calls, overlayOwner.reason)
	}
	if overlayOwner.request.Stage != backupasset.SourceLifecycleCleanup ||
		overlayOwner.request.Operation != backupasset.LifecycleRetentionExpire {
		t.Fatalf("Overlay request=%+v", overlayOwner.request)
	}
}

func TestLifecycleDependentCleanupAggregateFailsClosedWithoutOverlayZeroResult(t *testing.T) {
	order := make([]string, 0, 10)
	owners := []*retentionLifecycleOwnerFake{
		{name: "Content", order: &order}, {name: "Catalog", order: &order},
		{name: "Search", order: &order}, {name: "Processing", order: &order},
		{name: "Export", order: &order}, {name: "Recovery", order: &order},
	}
	overlayOwner := &retentionLifecycleOverlayFake{order: &order, repeat: overlay.LifecycleResult{Favorites: 1}}
	aggregate, err := NewRetentionLifecycle(RetentionLifecycleDependencies{
		Content: owners[0], Catalog: owners[1], Search: owners[2], Processing: owners[3],
		Export: owners[4], Recovery: owners[5], Overlay: overlayOwner,
		OverlayBatchSize: 1, OverlayMaxPasses: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = aggregate.CleanupRecoveryPoint(context.Background(), retention.LifecyclePointRequest{
		RecoveryPointID: "33333333333333333333333333333333",
		AttemptID:       "44444444444444444444444444444444",
		Operation:       backupasset.LifecycleExplicitPurge,
	})
	if !errors.Is(err, backupasset.ErrConflict) || overlayOwner.calls != 2 {
		t.Fatalf("bounded Overlay cleanup error=%v calls=%d, want ErrConflict/2", err, overlayOwner.calls)
	}
}

func TestLifecycleDependentCleanupProcessingSearchProofBridge(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/processing-search-proof.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetSearchDocumentField{}, &model.BackupAssetSearchPosting{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 14, 56, 0, 0, time.UTC)
	pointID, attemptID, generationID := strings.Repeat("5", 32), strings.Repeat("6", 32), strings.Repeat("7", 32)
	if err := db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("8", 32)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseCleaning),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetSearchGeneration{
		ID: generationID, RecoveryPointID: pointID, State: string(assetsearch.SearchGenerationComplete),
		IsActive: true, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetSearchDocument{
		SearchGenerationID: generationID, DocumentID: strings.Repeat("9", 64), RecoveryPointID: pointID,
		CatalogGenerationID: strings.Repeat("b", 32), EntryID: strings.Repeat("a", 64), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: time.Minute, Heartbeat: 10 * time.Second, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	searchIndexer, err := assetsearch.NewIndexer(assetsearch.IndexerDependencies{
		DB: db, Lease: leaseService, Keys: backupasset.NewKeyring(db, func() time.Time { return now }),
		Now: func() time.Time { return now },
		Config: assetsearch.IndexerConfig{
			BatchSize: 1, BuildTimeout: time.Minute, MaxDocuments: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	searchOwner, err := assetsearch.NewSourceLifecycle(db, searchIndexer, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewProcessingSearchRevocationProof(searchOwner)
	if err != nil {
		t.Fatal(err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
	}
	if err := bridge.ProveRecoveryPointRevoked(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("premature Search proof error=%v, want ErrConflict", err)
	}
	if err := searchOwner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("clean Search source: %v", err)
	}
	if err := bridge.ProveRecoveryPointRevoked(context.Background(), request); err != nil {
		t.Fatalf("concrete Processing-to-Search proof: %v", err)
	}
}

type retentionLifecycleOwnerFake struct {
	name                   string
	order                  *[]string
	active                 int
	leases                 int
	payload                bool
	searchCleaned          *bool
	requireSearch          *bool
	preserveCleanupPayload bool
}

func (fake *retentionLifecycleOwnerFake) run(request backupasset.SourceLifecycleRequest) error {
	if fake.order != nil {
		*fake.order = append(*fake.order, fake.name+":"+string(request.Stage))
	}
	if request.Stage == backupasset.SourceLifecyclePrepare {
		fake.active, fake.leases = 0, 0
		return nil
	}
	if fake.requireSearch != nil && !*fake.requireSearch {
		return errors.New("Processing destruction preceded Search proof")
	}
	if fake.searchCleaned != nil {
		*fake.searchCleaned = true
	}
	if !fake.preserveCleanupPayload {
		fake.payload = false
	}
	return nil
}

func (fake *retentionLifecycleOwnerFake) RevokeAndDrainRecoveryPoint(_ context.Context, request backupasset.SourceLifecycleRequest) error {
	return fake.run(request)
}
func (fake *retentionLifecycleOwnerFake) RetireRecoveryPoint(_ context.Context, request backupasset.SourceLifecycleRequest) error {
	return fake.run(request)
}
func (fake *retentionLifecycleOwnerFake) RevokeRecoveryPoint(_ context.Context, request backupasset.SourceLifecycleRequest) error {
	return fake.run(request)
}
func (fake *retentionLifecycleOwnerFake) ExpireRecoveryPoint(_ context.Context, request backupasset.SourceLifecycleRequest) error {
	return fake.run(request)
}
func (fake *retentionLifecycleOwnerFake) CancelRecoveryPointInterests(_ context.Context, request backupasset.SourceLifecycleRequest) error {
	return fake.run(request)
}

type retentionLifecycleOverlayFake struct {
	order   *[]string
	results []overlay.LifecycleResult
	repeat  overlay.LifecycleResult
	calls   int
	reason  overlay.SourceReason
	request backupasset.SourceLifecycleRequest
}

func TestRuntimeOwnerFallbackProofsFailClosedWithoutSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:retention-fallback?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	pointID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := proveNoOutstandingRecoverySource(context.Background(), db, pointID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("missing Recovery schema error=%v, want ErrConflict", err)
	}
	if err := proveNoOutstandingProcessingSource(context.Background(), db, pointID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("missing Processing schema error=%v, want ErrConflict", err)
	}
	if err := proveNoOutstandingExportSource(context.Background(), db, pointID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("missing Export schema error=%v, want ErrConflict", err)
	}
	if err := proveNoOutstandingContent(context.Background(), db, pointID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("missing Content schema error=%v, want ErrConflict", err)
	}
	if err := proveNoOutstandingContent(context.Background(), nil, pointID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("nil Content db error=%v, want ErrConflict", err)
	}
}

func TestRuntimeOwnerFallbackProofsHonorCurrentSourceRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:retention-fallback-rows?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetProcessingJob{},
		&model.BackupAssetExportJob{},
		&model.BackupAssetExportItem{},
		&model.BackupAssetRecoveryPlan{},
		&model.BackupAssetRecoveryJob{},
	); err != nil {
		t.Fatal(err)
	}
	pointID := strings.Repeat("a", 32)
	otherID := strings.Repeat("b", 32)
	now := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	if err := proveNoOutstandingProcessingSource(context.Background(), db, pointID); err != nil {
		t.Fatalf("empty Processing schema should settle: %v", err)
	}
	if err := proveNoOutstandingExportSource(context.Background(), db, pointID); err != nil {
		t.Fatalf("empty Export schema should settle: %v", err)
	}
	if err := proveNoOutstandingRecoverySource(context.Background(), db, pointID); err != nil {
		t.Fatalf("empty Recovery schema should settle: %v", err)
	}

	terminalJobID := strings.Repeat("1", 32)
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: terminalJobID, DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
		RecoveryPointID: pointID, CatalogGenerationID: strings.Repeat("c", 32), EntryID: "entry",
		SourceFingerprint: "src", Capability: "cap", CapabilitySchema: "cap.v1",
		State: "succeeded", QueuedAt: now, AbsoluteDeadline: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		"UPDATE backup_asset_processing_jobs SET is_current = 0, current_attempt_id = NULL WHERE id = ?",
		terminalJobID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := proveNoOutstandingProcessingSource(context.Background(), db, pointID); err != nil {
		t.Fatalf("terminal Processing row should settle: %v", err)
	}
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: strings.Repeat("2", 32), DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
		RecoveryPointID: pointID, CatalogGenerationID: strings.Repeat("c", 32), EntryID: "entry",
		SourceFingerprint: "src", Capability: "cap", CapabilitySchema: "cap.v1",
		IsCurrent: true, State: "running", QueuedAt: now, AbsoluteDeadline: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := proveNoOutstandingProcessingSource(context.Background(), db, pointID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("current Processing row error=%v, want ErrConflict", err)
	}

	if err := db.Create(&model.BackupAssetExportJob{
		ID: strings.Repeat("3", 32), ExecutionState: "queued", CleanupState: "none", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: strings.Repeat("4", 32), JobID: strings.Repeat("3", 32), RecoveryPointID: otherID,
		EntryID: "entry", CatalogGenerationID: strings.Repeat("c", 32),
		PathNonce: []byte("nonce"), PathCiphertext: []byte("path"), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := proveNoOutstandingExportSource(context.Background(), db, pointID); err != nil {
		t.Fatalf("Export item on another point should settle: %v", err)
	}
	if err := db.Create(&model.BackupAssetExportItem{
		ID: strings.Repeat("5", 32), JobID: strings.Repeat("3", 32), RecoveryPointID: pointID,
		EntryID: "entry", CatalogGenerationID: strings.Repeat("c", 32),
		PathNonce: []byte("nonce"), PathCiphertext: []byte("path"), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := proveNoOutstandingExportSource(context.Background(), db, pointID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("active Export item error=%v, want ErrConflict", err)
	}

	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&model.BackupAssetRecoveryPlan{
		ID: strings.Repeat("6", 32), RecoveryPointID: otherID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&model.BackupAssetRecoveryJob{
		ID: strings.Repeat("7", 32), PlanID: strings.Repeat("6", 32), State: "running", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := proveNoOutstandingRecoverySource(context.Background(), db, pointID); err != nil {
		t.Fatalf("Recovery job on another plan point should settle: %v", err)
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&model.BackupAssetRecoveryPlan{
		ID: strings.Repeat("8", 32), RecoveryPointID: pointID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&model.BackupAssetRecoveryJob{
		ID: strings.Repeat("9", 32), PlanID: strings.Repeat("8", 32), State: "verifying", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := proveNoOutstandingRecoverySource(context.Background(), db, pointID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("active Recovery job error=%v, want ErrConflict", err)
	}
}

func (fake *retentionLifecycleOverlayFake) ReconcileSourceLifecycle(
	_ context.Context,
	request backupasset.SourceLifecycleRequest,
	source overlay.SourceLifecycle,
	_ int,
) (overlay.LifecycleResult, error) {
	if fake.order != nil {
		*fake.order = append(*fake.order, "Overlay:cleanup")
	}
	fake.calls++
	fake.reason, fake.request = source.Reason, request
	if len(fake.results) == 0 {
		return fake.repeat, nil
	}
	result := fake.results[0]
	fake.results = fake.results[1:]
	return result, nil
}
