package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type catalogBuildSpy struct {
	requests   []catalog.BuildRequest
	generation model.CatalogGeneration
	err        error
}

func (spy *catalogBuildSpy) Build(_ context.Context, request catalog.BuildRequest) (model.CatalogGeneration, error) {
	spy.requests = append(spy.requests, request)
	return spy.generation, spy.err
}

type derivedWorkRequesterSpy struct {
	requests []processing.WorkRequest
	err      error
}

func (spy *derivedWorkRequesterSpy) RequestWork(_ context.Context, request processing.WorkRequest) (processing.WorkResult, error) {
	spy.requests = append(spy.requests, request)
	return processing.WorkResult{Created: true}, spy.err
}

func TestCatalogRebuildAdapterCallsIndexerBuild(t *testing.T) {
	builder := &catalogBuildSpy{generation: model.CatalogGeneration{ID: strings.Repeat("c", 32)}}
	adapter := newCatalogRebuildAdapter(builder)
	started, err := adapter.StartFreshCatalogGeneration(context.Background(), repository.CatalogRebuildRequest{
		RepositoryID: strings.Repeat("a", 32), RecoveryPointID: strings.Repeat("b", 32),
		ManifestDigest: strings.Repeat("d", 64), CapabilityRevision: 3,
	})
	if err != nil {
		t.Fatalf("StartFreshCatalogGeneration: %v", err)
	}
	if started.GenerationID != builder.generation.ID {
		t.Fatalf("generation=%q, want %q", started.GenerationID, builder.generation.ID)
	}
	if len(builder.requests) != 1 || builder.requests[0].RepositoryID != strings.Repeat("a", 32) ||
		builder.requests[0].RecoveryPointID != strings.Repeat("b", 32) {
		t.Fatalf("Build requests=%+v", builder.requests)
	}
}

func TestCatalogRebuildAdapterFailsClosedWhenBuildFails(t *testing.T) {
	builder := &catalogBuildSpy{err: errors.New("FAKE_CATALOG_BUILD_FAILURE_FOR_TEST_ONLY")}
	adapter := newCatalogRebuildAdapter(builder)
	started, err := adapter.StartFreshCatalogGeneration(context.Background(), repository.CatalogRebuildRequest{
		RepositoryID: strings.Repeat("a", 32), RecoveryPointID: strings.Repeat("b", 32),
	})
	if err == nil {
		t.Fatal("failed Build returned success")
	}
	if started.GenerationID != "" {
		t.Fatalf("failed Build started=%+v", started)
	}
}

func TestDerivedBackfillAdapterQueuesBackgroundWork(t *testing.T) {
	descriptor := validRebuildWorkDescriptor(t)
	requester := &derivedWorkRequesterSpy{}
	adapter := derivedBackfillAdapter{
		requestWork: requester,
		descriptors: func(context.Context, repository.DerivedBackfillRequest) ([]processing.WorkDescriptorV1, error) {
			return []processing.WorkDescriptorV1{descriptor}, nil
		},
	}
	queued, err := adapter.QueueLowPriorityDerivedBackfill(context.Background(), repository.DerivedBackfillRequest{
		RepositoryID: strings.Repeat("4", 32), RecoveryPointID: descriptor.Source.RecoveryPointID,
		CatalogGenerationID: descriptor.CatalogGenerationID, Priority: "low",
	})
	if err != nil {
		t.Fatalf("QueueLowPriorityDerivedBackfill: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued=%d, want 1", queued)
	}
	if len(requester.requests) != 1 {
		t.Fatalf("RequestWork calls=%d, want 1", len(requester.requests))
	}
	work := requester.requests[0]
	if work.Interest.PriorityClass != processing.PriorityBackground || work.Interest.OwnerKind != processing.InterestSystem {
		t.Fatalf("queued interest=%+v, want background system work", work.Interest)
	}
	if work.Descriptor != descriptor {
		t.Fatalf("queued descriptor=%+v", work.Descriptor)
	}
}

func TestDerivedBackfillAdapterTreatsNotDeployedAsNotQueued(t *testing.T) {
	descriptor := validRebuildWorkDescriptor(t)
	requester := &derivedWorkRequesterSpy{err: processing.ErrNotDeployed}
	adapter := derivedBackfillAdapter{
		requestWork: requester,
		descriptors: func(context.Context, repository.DerivedBackfillRequest) ([]processing.WorkDescriptorV1, error) {
			return []processing.WorkDescriptorV1{descriptor}, nil
		},
	}
	queued, err := adapter.QueueLowPriorityDerivedBackfill(context.Background(), repository.DerivedBackfillRequest{
		RepositoryID: strings.Repeat("4", 32), RecoveryPointID: descriptor.Source.RecoveryPointID,
		CatalogGenerationID: descriptor.CatalogGenerationID, Priority: "low",
	})
	if !errors.Is(err, processing.ErrNotDeployed) {
		t.Fatalf("ErrNotDeployed error=%v queued=%d, want wrapped processing.ErrNotDeployed", err, queued)
	}
	if queued != 0 {
		t.Fatalf("ErrNotDeployed queued=%d, want 0", queued)
	}
}

func TestDerivedBackfillAdapterTreatsMissingCoordinatorAsNotQueued(t *testing.T) {
	adapter := derivedBackfillAdapter{}
	queued, err := adapter.QueueLowPriorityDerivedBackfill(context.Background(), repository.DerivedBackfillRequest{
		RepositoryID: strings.Repeat("4", 32), RecoveryPointID: strings.Repeat("1", 32),
		CatalogGenerationID: strings.Repeat("3", 32), Priority: "low",
	})
	if !errors.Is(err, backupasset.ErrInvalidState) || queued != 0 {
		t.Fatalf("missing coordinator queued=%d err=%v, want 0 wrapped backupasset.ErrInvalidState", queued, err)
	}
}

func TestDerivedBackfillAdapterTreatsEmptyDescriptorsAsNotQueued(t *testing.T) {
	requester := &derivedWorkRequesterSpy{}
	adapter := derivedBackfillAdapter{
		requestWork: requester,
		descriptors: func(context.Context, repository.DerivedBackfillRequest) ([]processing.WorkDescriptorV1, error) {
			return nil, nil
		},
	}
	queued, err := adapter.QueueLowPriorityDerivedBackfill(context.Background(), repository.DerivedBackfillRequest{
		RepositoryID: strings.Repeat("4", 32), RecoveryPointID: strings.Repeat("1", 32),
		CatalogGenerationID: strings.Repeat("3", 32), Priority: "low",
	})
	if err != nil || queued != 0 {
		t.Fatalf("wired empty descriptors queued=%d err=%v, want 0 nil", queued, err)
	}
	if len(requester.requests) != 0 {
		t.Fatalf("wired empty descriptors requested work=%d, want 0", len(requester.requests))
	}
}

func TestRebuildDerivedDescriptorsPagesUnprovenEntriesWithoutNewCatalog(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	firstEntryID := strings.Repeat("4", 64)
	secondEntryID := strings.Repeat("5", 64)
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-backfill",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 2, WrittenEntryCount: 2,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	for _, entryID := range []string{firstEntryID, secondEntryID} {
		if err := db.Create(&model.CatalogEntry{
			GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "/notes/" + entryID[:8] + ".txt", Name: entryID[:8] + ".txt",
			EntryType: string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
			Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
			EncryptedProviderLocator: "FAKE_REBUILD_ENTRY_LOCATOR_FOR_TEST_ONLY",
			SecurityState:            "sealed", CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed catalog entry %s: %v", entryID, err)
		}
	}
	workerID := strings.Repeat("c", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("d", 64),
		InstanceID: strings.Repeat("e", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 1, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: capabilityspec.CapabilityTextExtract,
		CapabilitySchema: "text.extract.v1", PipelineFingerprint: "text-pipeline-v1",
		OutputProfile: capabilityspec.ProfileBoundedTextV1, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("e", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild capability: %v", err)
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			Backfill: backupasset.ProcessingBackfillConfig{
				BatchSize: 1, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	first, err := runtime.rebuildDerivedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("first rebuildDerivedDescriptors: %v", err)
	}
	if len(first) != 1 || first[0].Source.EntryID != firstEntryID {
		t.Fatalf("first descriptors=%+v, want entry %s only", first, firstEntryID)
	}
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID, CatalogGenerationID: generationID,
		EntryID: firstEntryID, SourceFingerprint: sourceFingerprint, EntryFingerprint: strings.Repeat("b", 64),
		ProviderCapabilityRevision: 1, Capability: first[0].Capability, CapabilitySchema: first[0].CapabilitySchema,
		PipelineFingerprint: first[0].PipelineFingerprint, OutputProfile: first[0].OutputProfile,
		SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
		EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
		QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed first-page processing job: %v", err)
	}

	second, err := runtime.rebuildDerivedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("second rebuildDerivedDescriptors: %v", err)
	}
	if len(second) != 1 || second[0].Source.EntryID != secondEntryID {
		t.Fatalf("second descriptors=%+v, want later catalog entry %s without a new catalog", second, secondEntryID)
	}
	source, ok := newDerivedBackfillAdapter(runtime).(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}
	expected, err := source.ExpectedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("ExpectedDescriptors: %v", err)
	}
	if len(expected) != 1 || expected[0].EntryID != secondEntryID {
		t.Fatalf("ExpectedDescriptors=%+v, want unproven second page %s", expected, secondEntryID)
	}
}

func TestExpectedDescriptorsStayUnprovenWhenBackfillAdmissionDenies(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	firstEntryID := strings.Repeat("4", 64)
	secondEntryID := strings.Repeat("5", 64)
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-backfill-paused",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 2, WrittenEntryCount: 2,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	for _, entryID := range []string{firstEntryID, secondEntryID} {
		if err := db.Create(&model.CatalogEntry{
			GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "/notes/" + entryID[:8] + ".txt", Name: entryID[:8] + ".txt",
			EntryType: string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
			Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
			EncryptedProviderLocator: "FAKE_REBUILD_ENTRY_LOCATOR_FOR_TEST_ONLY",
			SecurityState:            "sealed", CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed catalog entry %s: %v", entryID, err)
		}
	}
	workerID := strings.Repeat("c", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("d", 64),
		InstanceID: strings.Repeat("e", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 1, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: capabilityspec.CapabilityTextExtract,
		CapabilitySchema: "text.extract.v1", PipelineFingerprint: "text-pipeline-v1",
		OutputProfile: capabilityspec.ProfileBoundedTextV1, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("e", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild capability: %v", err)
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			Backfill: backupasset.ProcessingBackfillConfig{
				Paused: true, BatchSize: 1, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	admitted, err := runtime.rebuildDerivedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("paused rebuildDerivedDescriptors: %v", err)
	}
	if len(admitted) != 0 {
		t.Fatalf("paused rebuildDerivedDescriptors=%+v, want admission deny", admitted)
	}

	adapter := newDerivedBackfillAdapter(runtime)
	source, ok := adapter.(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}
	expected, err := source.ExpectedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("ExpectedDescriptors: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("admission deny treated remaining unproven catalog entries as proven-complete")
	}
	if expected[0].EntryID != firstEntryID {
		t.Fatalf("ExpectedDescriptors=%+v, want unproven first catalog entry %s", expected, firstEntryID)
	}
}

func TestRebuildDerivedDescriptorsKeepsUnprovenCapabilitiesWhenSiblingJobExists(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	entryID := strings.Repeat("4", 64)
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-sibling-job",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 1, WrittenEntryCount: 1,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	if err := db.Create(&model.CatalogEntry{
		GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
		NormalizedPath: "/notes/sibling.txt", Name: "sibling.txt",
		EntryType: string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
		Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
		EncryptedProviderLocator: "FAKE_REBUILD_SIBLING_ENTRY_LOCATOR_FOR_TEST_ONLY",
		SecurityState:            "sealed", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog entry: %v", err)
	}
	workerID := strings.Repeat("c", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("d", 64),
		InstanceID: strings.Repeat("e", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	textProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, false)
	if !ok {
		t.Fatal("text extract profile missing")
	}
	malwareProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1, false)
	if !ok {
		t.Fatal("malware scan profile missing")
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: textProfile.Capability,
		CapabilitySchema: textProfile.CapabilitySchema, PipelineFingerprint: "text-pipeline-v1",
		OutputProfile: textProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("e", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed text extract capability: %v", err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("9", 32), WorkerID: workerID, Capability: malwareProfile.Capability,
		CapabilitySchema: malwareProfile.CapabilitySchema, PipelineFingerprint: "malware-pipeline-v1",
		OutputProfile: malwareProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("8", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed malware scan capability: %v", err)
	}
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID, CatalogGenerationID: generationID,
		EntryID: entryID, SourceFingerprint: sourceFingerprint, EntryFingerprint: strings.Repeat("b", 64),
		ProviderCapabilityRevision: 1, Capability: textProfile.Capability, CapabilitySchema: textProfile.CapabilitySchema,
		PipelineFingerprint: "text-pipeline-v1", OutputProfile: textProfile.OutputProfile,
		SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
		EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
		QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed sibling text-extract job: %v", err)
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			Backfill: backupasset.ProcessingBackfillConfig{
				BatchSize: 4, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	queued, err := runtime.rebuildDerivedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("rebuildDerivedDescriptors: %v", err)
	}
	if len(queued) == 0 {
		t.Fatal("sibling job for text.extract skipped the catalog entry and left malware.scan unproven")
	}
	if len(queued) != 1 || queued[0].Source.EntryID != entryID || queued[0].Capability != malwareProfile.Capability {
		t.Fatalf("queued=%+v, want only unproven malware.scan for entry %s", queued, entryID)
	}

	source, ok := newDerivedBackfillAdapter(runtime).(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}
	expected, err := source.ExpectedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("ExpectedDescriptors: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("sibling job for text.extract skipped ExpectedDescriptors and left malware.scan unproven")
	}
	if len(expected) != 1 || expected[0].EntryID != entryID || expected[0].Capability != malwareProfile.Capability {
		t.Fatalf("ExpectedDescriptors=%+v, want only unproven malware.scan for entry %s", expected, entryID)
	}
}

func TestRebuildDerivedDescriptorsPagesPastInapplicableAdvertisedCapabilities(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	textEntryID := strings.Repeat("4", 64)
	imageEntryID := strings.Repeat("5", 64)
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-media-mismatch",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 2, WrittenEntryCount: 2,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	if err := db.Create(&model.CatalogEntry{
		GenerationID: generationID, EntryID: textEntryID, RecoveryPointID: pointID,
		NormalizedPath: "/notes/done.txt", Name: "done.txt",
		EntryType: string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
		Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
		EncryptedProviderLocator: "FAKE_REBUILD_TEXT_ENTRY_LOCATOR_FOR_TEST_ONLY",
		SecurityState:            "sealed", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed text catalog entry: %v", err)
	}
	if err := db.Create(&model.CatalogEntry{
		GenerationID: generationID, EntryID: imageEntryID, RecoveryPointID: pointID,
		NormalizedPath: "/photos/later.jpg", Name: "later.jpg",
		EntryType: string(backupasset.CatalogEntryFile), Size: 64, MimeType: "image/jpeg",
		Fingerprint: strings.Repeat("c", 64), FingerprintStrength: "strong",
		EncryptedProviderLocator: "FAKE_REBUILD_IMAGE_ENTRY_LOCATOR_FOR_TEST_ONLY",
		SecurityState:            "sealed", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed image catalog entry: %v", err)
	}
	workerID := strings.Repeat("d", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("e", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	textProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, false)
	if !ok {
		t.Fatal("text extract profile missing")
	}
	ocrProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityImageOCR, capabilityspec.ProfileTesseractTextV1, false)
	if !ok {
		t.Fatal("image ocr profile missing")
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: textProfile.Capability,
		CapabilitySchema: textProfile.CapabilitySchema, PipelineFingerprint: "text-pipeline-v1",
		OutputProfile: textProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("7", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed text extract capability: %v", err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("8", 32), WorkerID: workerID, Capability: ocrProfile.Capability,
		CapabilitySchema: ocrProfile.CapabilitySchema, PipelineFingerprint: "ocr-pipeline-v1",
		OutputProfile: ocrProfile.OutputProfile, InputModes: "stat,sequential,range",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("9", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed image ocr capability: %v", err)
	}
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: strings.Repeat("a", 32), WorkKey: strings.Repeat("b", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID, CatalogGenerationID: generationID,
		EntryID: textEntryID, SourceFingerprint: sourceFingerprint, EntryFingerprint: strings.Repeat("b", 64),
		ProviderCapabilityRevision: 1, Capability: textProfile.Capability, CapabilitySchema: textProfile.CapabilitySchema,
		PipelineFingerprint: "text-pipeline-v1", OutputProfile: textProfile.OutputProfile,
		SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
		EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
		QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed proven text-extract job: %v", err)
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			Backfill: backupasset.ProcessingBackfillConfig{
				BatchSize: 1, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	queued, err := runtime.rebuildDerivedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("rebuildDerivedDescriptors: %v", err)
	}
	if len(queued) == 0 {
		t.Fatal("inapplicable leftover advertised capability skipped later catalog work and left image.ocr unproven")
	}
	if len(queued) != 1 || queued[0].Source.EntryID != imageEntryID || queued[0].Capability != ocrProfile.Capability {
		t.Fatalf("queued=%+v, want only unproven image.ocr for later entry %s", queued, imageEntryID)
	}

	source, ok := newDerivedBackfillAdapter(runtime).(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}
	expected, err := source.ExpectedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("ExpectedDescriptors: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("empty ExpectedDescriptors treated later image.ocr work as proven because COUNT stayed below advertised")
	}
	if len(expected) != 1 || expected[0].EntryID != imageEntryID || expected[0].Capability != ocrProfile.Capability {
		t.Fatalf("ExpectedDescriptors=%+v, want only unproven image.ocr for later entry %s", expected, imageEntryID)
	}
}

func TestRebuildDerivedDescriptorsInspectedLimitKeepsLeftoverWalkUnproven(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	leftoverIDs := []string{
		strings.Repeat("4", 63) + "0",
		strings.Repeat("4", 63) + "1",
		strings.Repeat("4", 63) + "2",
		strings.Repeat("4", 63) + "3",
	}
	imageEntryID := strings.Repeat("5", 64)
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-leftover-limit",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 5, WrittenEntryCount: 5,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.CatalogEntry{
			GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "/notes/leftover-" + entryID[len(entryID)-1:] + ".txt",
			Name:           "leftover-" + entryID[len(entryID)-1:] + ".txt",
			EntryType:      string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
			Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
			EncryptedProviderLocator: "FAKE_REBUILD_LEFTOVER_ENTRY_LOCATOR_FOR_TEST_ONLY",
			SecurityState:            "sealed", CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover catalog entry %d: %v", index, err)
		}
	}
	if err := db.Create(&model.CatalogEntry{
		GenerationID: generationID, EntryID: imageEntryID, RecoveryPointID: pointID,
		NormalizedPath: "/photos/later.jpg", Name: "later.jpg",
		EntryType: string(backupasset.CatalogEntryFile), Size: 64, MimeType: "image/jpeg",
		Fingerprint: strings.Repeat("c", 64), FingerprintStrength: "strong",
		EncryptedProviderLocator: "FAKE_REBUILD_IMAGE_ENTRY_LOCATOR_FOR_TEST_ONLY",
		SecurityState:            "sealed", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed image catalog entry: %v", err)
	}
	workerID := strings.Repeat("d", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("e", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	textProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, false)
	if !ok {
		t.Fatal("text extract profile missing")
	}
	ocrProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityImageOCR, capabilityspec.ProfileTesseractTextV1, false)
	if !ok {
		t.Fatal("image ocr profile missing")
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: textProfile.Capability,
		CapabilitySchema: textProfile.CapabilitySchema, PipelineFingerprint: "text-pipeline-v1",
		OutputProfile: textProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("7", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed text extract capability: %v", err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("8", 32), WorkerID: workerID, Capability: ocrProfile.Capability,
		CapabilitySchema: ocrProfile.CapabilitySchema, PipelineFingerprint: "ocr-pipeline-v1",
		OutputProfile: ocrProfile.OutputProfile, InputModes: "stat,sequential,range",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("9", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed image ocr capability: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.BackupAssetProcessingJob{
			ID: strings.Repeat("a", 31) + string(rune('0'+index)), WorkKey: strings.Repeat("b", 63) + string(rune('0'+index)),
			DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID,
			CatalogGenerationID: generationID, EntryID: entryID, SourceFingerprint: sourceFingerprint,
			EntryFingerprint: strings.Repeat("b", 64), ProviderCapabilityRevision: 1,
			Capability: textProfile.Capability, CapabilitySchema: textProfile.CapabilitySchema,
			PipelineFingerprint: "text-pipeline-v1", OutputProfile: textProfile.OutputProfile,
			SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
			EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
			QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover text-extract job %d: %v", index, err)
		}
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			Backfill: backupasset.ProcessingBackfillConfig{
				BatchSize: 1, InspectedLimit: 2, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	source, ok := newDerivedBackfillAdapter(runtime).(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}

	firstInspected := 0
	stopCount := countCatalogEntriesInspected(t, db, &firstInspected, "first")
	firstExpected, err := source.ExpectedDescriptors(context.Background(), request)
	stopCount()
	if err != nil {
		t.Fatalf("first ExpectedDescriptors: %v", err)
	}
	if expectedContainsEntry(firstExpected, imageEntryID) {
		t.Fatalf("first ExpectedDescriptors scanned jpeg %s in one leftover walk, inspected=%d expected=%+v",
			imageEntryID, firstInspected, firstExpected)
	}
	if firstInspected > 2 {
		t.Fatalf("first leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", firstInspected)
	}
	if len(firstExpected) != 1 || firstExpected[0].EntryID != leftoverWalkUnprovenEntryID ||
		firstExpected[0].Capability != leftoverWalkUnprovenCapability {
		t.Fatalf("first ExpectedDescriptors=%+v, want leftover-walk-unproven marker so empty expected cannot look proven", firstExpected)
	}

	requester := &derivedWorkRequesterSpy{}
	queueAdapter := derivedBackfillAdapter{
		requestWork: requester,
		descriptors: runtime.rebuildDerivedDescriptors,
		expected:    runtime.collectUnprovenRebuildDerivedDescriptors,
	}
	queuedAfterMarker, err := queueAdapter.QueueLowPriorityDerivedBackfill(context.Background(), request)
	if err != nil {
		t.Fatalf("Queue after leftover-walk-unproven Expected: %v", err)
	}
	for _, work := range requester.requests {
		if work.Descriptor.Source.EntryID == leftoverWalkUnprovenEntryID {
			t.Fatalf("Queue tried to enqueue leftover-walk-unproven marker: %+v", work.Descriptor)
		}
	}
	if queuedAfterMarker != 0 {
		t.Fatalf("Queue after leftover-walk-unproven queued=%d, want 0 leftover work this page", queuedAfterMarker)
	}

	var resumed []repository.ExpectedDerivedDescriptor
	for attempt := 0; attempt < 4; attempt++ {
		inspected := 0
		stop := countCatalogEntriesInspected(t, db, &inspected, fmt.Sprintf("resume-%d", attempt))
		expected, resumeErr := source.ExpectedDescriptors(context.Background(), request)
		stop()
		if resumeErr != nil {
			t.Fatalf("resumed ExpectedDescriptors: %v", resumeErr)
		}
		if inspected > 2 {
			t.Fatalf("resumed leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", inspected)
		}
		if expectedContainsEntry(expected, imageEntryID) {
			resumed = expected
			break
		}
		if len(expected) == 0 {
			t.Fatal("resumed ExpectedDescriptors returned empty before reaching later image.ocr")
		}
	}
	if len(resumed) != 1 || resumed[0].EntryID != imageEntryID || resumed[0].Capability != ocrProfile.Capability {
		t.Fatalf("resumed ExpectedDescriptors=%+v, want unproven image.ocr for later entry %s", resumed, imageEntryID)
	}

	queued, err := runtime.rebuildDerivedDescriptors(context.Background(), request)
	if err != nil {
		t.Fatalf("Queue-path rebuildDerivedDescriptors after Expected leftover walk: %v", err)
	}
	if len(queued) != 1 || queued[0].Source.EntryID != imageEntryID || queued[0].Capability != ocrProfile.Capability {
		t.Fatalf("Expected leftover cursor skipped Queue-path image.ocr: queued=%+v", queued)
	}
}

func TestRebuildDerivedDescriptorsCompleteWalkReopensWhenAdvertisementAddsUnprovenWork(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	leftoverIDs := []string{
		strings.Repeat("4", 63) + "0",
		strings.Repeat("4", 63) + "1",
		strings.Repeat("4", 63) + "2",
		strings.Repeat("4", 63) + "3",
	}
	imageEntryID := strings.Repeat("5", 64)
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-leftover-ad-change",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 5, WrittenEntryCount: 5,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.CatalogEntry{
			GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "/notes/leftover-" + entryID[len(entryID)-1:] + ".txt",
			Name:           "leftover-" + entryID[len(entryID)-1:] + ".txt",
			EntryType:      string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
			Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
			EncryptedProviderLocator: "FAKE_REBUILD_LEFTOVER_AD_ENTRY_LOCATOR_FOR_TEST_ONLY",
			SecurityState:            "sealed", CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover catalog entry %d: %v", index, err)
		}
	}
	if err := db.Create(&model.CatalogEntry{
		GenerationID: generationID, EntryID: imageEntryID, RecoveryPointID: pointID,
		NormalizedPath: "/photos/later.jpg", Name: "later.jpg",
		EntryType: string(backupasset.CatalogEntryFile), Size: 64, MimeType: "image/jpeg",
		Fingerprint: strings.Repeat("c", 64), FingerprintStrength: "strong",
		EncryptedProviderLocator: "FAKE_REBUILD_IMAGE_AD_ENTRY_LOCATOR_FOR_TEST_ONLY",
		SecurityState:            "sealed", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed image catalog entry: %v", err)
	}
	workerID := strings.Repeat("d", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("e", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	textProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, false)
	if !ok {
		t.Fatal("text extract profile missing")
	}
	ocrProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityImageOCR, capabilityspec.ProfileTesseractTextV1, false)
	if !ok {
		t.Fatal("image ocr profile missing")
	}
	malwareProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1, false)
	if !ok {
		t.Fatal("malware scan profile missing")
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: textProfile.Capability,
		CapabilitySchema: textProfile.CapabilitySchema, PipelineFingerprint: "text-pipeline-v1",
		OutputProfile: textProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("7", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed text extract capability: %v", err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("8", 32), WorkerID: workerID, Capability: ocrProfile.Capability,
		CapabilitySchema: ocrProfile.CapabilitySchema, PipelineFingerprint: "ocr-pipeline-v1",
		OutputProfile: ocrProfile.OutputProfile, InputModes: "stat,sequential,range",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("9", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed image ocr capability: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.BackupAssetProcessingJob{
			ID: strings.Repeat("a", 31) + string(rune('0'+index)), WorkKey: strings.Repeat("b", 63) + string(rune('0'+index)),
			DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID,
			CatalogGenerationID: generationID, EntryID: entryID, SourceFingerprint: sourceFingerprint,
			EntryFingerprint: strings.Repeat("b", 64), ProviderCapabilityRevision: 1,
			Capability: textProfile.Capability, CapabilitySchema: textProfile.CapabilitySchema,
			PipelineFingerprint: "text-pipeline-v1", OutputProfile: textProfile.OutputProfile,
			SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
			EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
			QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover text-extract job %d: %v", index, err)
		}
	}
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: strings.Repeat("c", 32), WorkKey: strings.Repeat("d", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID, CatalogGenerationID: generationID,
		EntryID: imageEntryID, SourceFingerprint: sourceFingerprint, EntryFingerprint: strings.Repeat("c", 64),
		ProviderCapabilityRevision: 1, Capability: ocrProfile.Capability, CapabilitySchema: ocrProfile.CapabilitySchema,
		PipelineFingerprint: "ocr-pipeline-v1", OutputProfile: ocrProfile.OutputProfile,
		SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
		EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
		QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed proven image.ocr job: %v", err)
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			Backfill: backupasset.ProcessingBackfillConfig{
				BatchSize: 1, InspectedLimit: 2, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	source, ok := newDerivedBackfillAdapter(runtime).(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}

	completed := false
	for attempt := 0; attempt < 6; attempt++ {
		expected, err := source.ExpectedDescriptors(context.Background(), request)
		if err != nil {
			t.Fatalf("pre-advertisement ExpectedDescriptors: %v", err)
		}
		if len(expected) == 0 {
			completed = true
			break
		}
		if expectedContainsEntry(expected, leftoverWalkUnprovenEntryID) {
			continue
		}
		t.Fatalf("pre-advertisement ExpectedDescriptors=%+v, want leftover-walk-unproven or completed empty", expected)
	}
	if !completed {
		t.Fatal("leftover walk never reached complete empty expected before advertisement change")
	}

	sameAdsInspected := 0
	stopSame := countCatalogEntriesInspected(t, db, &sameAdsInspected, "same-ads-complete")
	sameExpected, err := source.ExpectedDescriptors(context.Background(), request)
	stopSame()
	if err != nil {
		t.Fatalf("same-advertisement ExpectedDescriptors: %v", err)
	}
	if len(sameExpected) != 0 {
		t.Fatalf("completed leftover walk with unchanged ads returned %+v, want empty proven", sameExpected)
	}
	if sameAdsInspected != 0 {
		t.Fatalf("completed leftover walk rescanned %d catalog rows on a later same-ad tick", sameAdsInspected)
	}

	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("e", 32), WorkerID: workerID, Capability: malwareProfile.Capability,
		CapabilitySchema: malwareProfile.CapabilitySchema, PipelineFingerprint: "malware-pipeline-v1",
		OutputProfile: malwareProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("0", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed later malware.scan advertisement: %v", err)
	}

	reopenInspected := 0
	stopReopen := countCatalogEntriesInspected(t, db, &reopenInspected, "reopen")
	reopened, err := source.ExpectedDescriptors(context.Background(), request)
	stopReopen()
	if err != nil {
		t.Fatalf("post-advertisement ExpectedDescriptors: %v", err)
	}
	if len(reopened) == 0 {
		t.Fatal("walk.complete stayed proven after later malware.scan advertisement added leftover unproven work")
	}
	if reopenInspected > 2 {
		t.Fatalf("reopened leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", reopenInspected)
	}
	if expectedContainsEntry(reopened, leftoverWalkUnprovenEntryID) {
		if reopenInspected == 0 {
			t.Fatal("reopened leftover-walk-unproven without inspecting leftover catalog rows")
		}
	} else if reopened[0].EntryID != leftoverIDs[0] || reopened[0].Capability != malwareProfile.Capability {
		t.Fatalf("reopened ExpectedDescriptors=%+v, want leftover text malware.scan or leftover-walk-unproven", reopened)
	}

	requester := &derivedWorkRequesterSpy{}
	queueAdapter := derivedBackfillAdapter{
		requestWork: requester,
		descriptors: runtime.rebuildDerivedDescriptors,
		expected:    runtime.collectUnprovenRebuildDerivedDescriptors,
	}
	if _, err := queueAdapter.QueueLowPriorityDerivedBackfill(context.Background(), request); err != nil {
		t.Fatalf("Queue after advertisement reopen: %v", err)
	}
	for _, work := range requester.requests {
		if work.Descriptor.Source.EntryID == leftoverWalkUnprovenEntryID {
			t.Fatalf("Queue tried to enqueue leftover-walk-unproven after advertisement reopen: %+v", work.Descriptor)
		}
	}
}

func TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobFails(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	leftoverIDs := []string{
		strings.Repeat("4", 63) + "0",
		strings.Repeat("4", 63) + "1",
		strings.Repeat("4", 63) + "2",
		strings.Repeat("4", 63) + "3",
	}
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-leftover-job-fail",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 4, WrittenEntryCount: 4,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.CatalogEntry{
			GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "/notes/leftover-" + entryID[len(entryID)-1:] + ".txt",
			Name:           "leftover-" + entryID[len(entryID)-1:] + ".txt",
			EntryType:      string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
			Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
			EncryptedProviderLocator: "FAKE_REBUILD_LEFTOVER_FAIL_ENTRY_LOCATOR_FOR_TEST_ONLY",
			SecurityState:            "sealed", CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover catalog entry %d: %v", index, err)
		}
	}
	workerID := strings.Repeat("d", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("e", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	textProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, false)
	if !ok {
		t.Fatal("text extract profile missing")
	}
	ocrProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityImageOCR, capabilityspec.ProfileTesseractTextV1, false)
	if !ok {
		t.Fatal("image ocr profile missing")
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: textProfile.Capability,
		CapabilitySchema: textProfile.CapabilitySchema, PipelineFingerprint: "text-pipeline-v1",
		OutputProfile: textProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("7", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed text extract capability: %v", err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("8", 32), WorkerID: workerID, Capability: ocrProfile.Capability,
		CapabilitySchema: ocrProfile.CapabilitySchema, PipelineFingerprint: "ocr-pipeline-v1",
		OutputProfile: ocrProfile.OutputProfile, InputModes: "stat,sequential,range",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("9", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed image ocr capability: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.BackupAssetProcessingJob{
			ID: strings.Repeat("a", 31) + string(rune('0'+index)), WorkKey: strings.Repeat("b", 63) + string(rune('0'+index)),
			DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID,
			CatalogGenerationID: generationID, EntryID: entryID, SourceFingerprint: sourceFingerprint,
			EntryFingerprint: strings.Repeat("b", 64), ProviderCapabilityRevision: 1,
			Capability: textProfile.Capability, CapabilitySchema: textProfile.CapabilitySchema,
			PipelineFingerprint: "text-pipeline-v1", OutputProfile: textProfile.OutputProfile,
			SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
			EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
			QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover text-extract job %d: %v", index, err)
		}
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			Backfill: backupasset.ProcessingBackfillConfig{
				BatchSize: 1, InspectedLimit: 2, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	source, ok := newDerivedBackfillAdapter(runtime).(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}

	completed := false
	for attempt := 0; attempt < 6; attempt++ {
		expected, err := source.ExpectedDescriptors(context.Background(), request)
		if err != nil {
			t.Fatalf("pre-failure ExpectedDescriptors: %v", err)
		}
		if len(expected) == 0 {
			completed = true
			break
		}
		if expectedContainsEntry(expected, leftoverWalkUnprovenEntryID) {
			continue
		}
		t.Fatalf("pre-failure ExpectedDescriptors=%+v, want leftover-walk-unproven or completed empty", expected)
	}
	if !completed {
		t.Fatal("leftover walk never reached complete empty expected before job failure")
	}

	sameAdsInspected := 0
	stopSame := countCatalogEntriesInspected(t, db, &sameAdsInspected, "same-ads-complete")
	sameExpected, err := source.ExpectedDescriptors(context.Background(), request)
	stopSame()
	if err != nil {
		t.Fatalf("same-advertisement ExpectedDescriptors: %v", err)
	}
	if len(sameExpected) != 0 {
		t.Fatalf("completed leftover walk with unchanged ads returned %+v, want empty proven", sameExpected)
	}
	if sameAdsInspected != 0 {
		t.Fatalf("completed leftover walk rescanned %d catalog rows on a later same-ad tick", sameAdsInspected)
	}

	failedAt := now.Add(time.Second)
	if err := db.Model(&model.BackupAssetProcessingJob{}).
		Where("id = ?", strings.Repeat("a", 31)+"0").
		Updates(map[string]any{
			"state": string(processing.ProcessingFailed), "is_current": false, "updated_at": failedAt,
		}).Error; err != nil {
		t.Fatalf("mark leftover text-extract job failed: %v", err)
	}

	reopenInspected := 0
	stopReopen := countCatalogEntriesInspected(t, db, &reopenInspected, "reopen-failed")
	reopened, err := source.ExpectedDescriptors(context.Background(), request)
	stopReopen()
	if err != nil {
		t.Fatalf("post-failure ExpectedDescriptors: %v", err)
	}
	if len(reopened) == 0 {
		t.Fatal("walk.complete stayed proven after a failed leftover text.extract job became unproven")
	}
	if reopenInspected > 2 {
		t.Fatalf("reopened leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", reopenInspected)
	}
	if !expectedContainsCapability(reopened, leftoverIDs[0], textProfile.Capability) {
		if expectedContainsEntry(reopened, leftoverWalkUnprovenEntryID) {
			if reopenInspected == 0 {
				t.Fatal("reopened leftover-walk-unproven without inspecting leftover catalog rows")
			}
		} else {
			t.Fatalf("reopened ExpectedDescriptors=%+v, want unproven text.extract for %s or leftover-walk-unproven",
				reopened, leftoverIDs[0])
		}
		found := false
		for attempt := 0; attempt < 4; attempt++ {
			inspected := 0
			stop := countCatalogEntriesInspected(t, db, &inspected, fmt.Sprintf("resume-failed-%d", attempt))
			expected, resumeErr := source.ExpectedDescriptors(context.Background(), request)
			stop()
			if resumeErr != nil {
				t.Fatalf("resumed post-failure ExpectedDescriptors: %v", resumeErr)
			}
			if inspected > 2 {
				t.Fatalf("resumed leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", inspected)
			}
			if expectedContainsCapability(expected, leftoverIDs[0], textProfile.Capability) {
				found = true
				break
			}
			if len(expected) == 0 {
				t.Fatal("resumed ExpectedDescriptors returned empty before failed text.extract reappeared as unproven")
			}
		}
		if !found {
			t.Fatalf("failed leftover text.extract never reappeared as unproven after walk.complete")
		}
	}

	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: strings.Repeat("c", 32), WorkKey: strings.Repeat("d", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID, CatalogGenerationID: generationID,
		EntryID: leftoverIDs[0], SourceFingerprint: sourceFingerprint, EntryFingerprint: strings.Repeat("b", 64),
		ProviderCapabilityRevision: 1, Capability: textProfile.Capability, CapabilitySchema: textProfile.CapabilitySchema,
		PipelineFingerprint: "text-pipeline-v1", OutputProfile: textProfile.OutputProfile,
		SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
		EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 2, IsCurrent: true,
		QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now.Add(2 * time.Second),
	}).Error; err != nil {
		t.Fatalf("seed replacement current text-extract job: %v", err)
	}

	completedAfterReplay := false
	for attempt := 0; attempt < 6; attempt++ {
		expected, completeErr := source.ExpectedDescriptors(context.Background(), request)
		if completeErr != nil {
			t.Fatalf("post-replay ExpectedDescriptors: %v", completeErr)
		}
		if len(expected) == 0 {
			completedAfterReplay = true
			break
		}
		if expectedContainsEntry(expected, leftoverWalkUnprovenEntryID) {
			continue
		}
		t.Fatalf("post-replay ExpectedDescriptors=%+v, want leftover-walk-unproven or completed empty", expected)
	}
	if !completedAfterReplay {
		t.Fatal("leftover walk never reached complete empty expected after replacement current job")
	}

	stickyInspected := 0
	stopSticky := countCatalogEntriesInspected(t, db, &stickyInspected, "sticky-after-old-failed")
	stickyExpected, err := source.ExpectedDescriptors(context.Background(), request)
	stopSticky()
	if err != nil {
		t.Fatalf("sticky ExpectedDescriptors: %v", err)
	}
	if len(stickyExpected) != 0 {
		t.Fatalf("completed leftover walk reopened for historical failed job: %+v", stickyExpected)
	}
	if stickyInspected != 0 {
		t.Fatalf("historical failed job made leftover walk inspect %d catalog rows", stickyInspected)
	}

	failedOtherAt := now.Add(3 * time.Second)
	if err := db.Model(&model.BackupAssetProcessingJob{}).
		Where("id = ?", strings.Repeat("a", 31)+"1").
		Updates(map[string]any{
			"state": string(processing.ProcessingFailed), "is_current": false, "updated_at": failedOtherAt,
		}).Error; err != nil {
		t.Fatalf("mark later leftover text-extract job failed: %v", err)
	}
	otherInspected := 0
	stopOther := countCatalogEntriesInspected(t, db, &otherInspected, "other-failed-after-sticky")
	otherExpected, err := source.ExpectedDescriptors(context.Background(), request)
	stopOther()
	if err != nil {
		t.Fatalf("post-other-failure ExpectedDescriptors: %v", err)
	}
	if len(otherExpected) == 0 {
		t.Fatal("walk.complete hid a newly failed leftover entry behind a historical failed revision")
	}
	if otherInspected > 2 {
		t.Fatalf("other-failure leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", otherInspected)
	}
	if !expectedContainsCapability(otherExpected, leftoverIDs[1], textProfile.Capability) &&
		!expectedContainsEntry(otherExpected, leftoverWalkUnprovenEntryID) {
		t.Fatalf("other-failure ExpectedDescriptors=%+v, want unproven text.extract for %s or leftover-walk-unproven",
			otherExpected, leftoverIDs[1])
	}
}

func TestRebuildDerivedDescriptorsCompleteWalkReopensWhenJobCanceledDuringIncompleteWalk(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 17, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	leftoverIDs := []string{
		strings.Repeat("4", 63) + "0",
		strings.Repeat("4", 63) + "1",
		strings.Repeat("4", 63) + "2",
		strings.Repeat("4", 63) + "3",
	}
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-leftover-job-cancel-midwalk",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 4, WrittenEntryCount: 4,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.CatalogEntry{
			GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "/notes/leftover-" + entryID[len(entryID)-1:] + ".txt",
			Name:           "leftover-" + entryID[len(entryID)-1:] + ".txt",
			EntryType:      string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
			Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
			EncryptedProviderLocator: "FAKE_REBUILD_LEFTOVER_CANCEL_ENTRY_LOCATOR_FOR_TEST_ONLY",
			SecurityState:            "sealed", CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover catalog entry %d: %v", index, err)
		}
	}
	workerID := strings.Repeat("d", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("e", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	textProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, false)
	if !ok {
		t.Fatal("text extract profile missing")
	}
	ocrProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilityImageOCR, capabilityspec.ProfileTesseractTextV1, false)
	if !ok {
		t.Fatal("image ocr profile missing")
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: textProfile.Capability,
		CapabilitySchema: textProfile.CapabilitySchema, PipelineFingerprint: "text-pipeline-v1",
		OutputProfile: textProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("7", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed text extract capability: %v", err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("8", 32), WorkerID: workerID, Capability: ocrProfile.Capability,
		CapabilitySchema: ocrProfile.CapabilitySchema, PipelineFingerprint: "ocr-pipeline-v1",
		OutputProfile: ocrProfile.OutputProfile, InputModes: "stat,sequential,range",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("9", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed image ocr capability: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.BackupAssetProcessingJob{
			ID: strings.Repeat("a", 31) + string(rune('0'+index)), WorkKey: strings.Repeat("b", 63) + string(rune('0'+index)),
			DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`), RecoveryPointID: pointID,
			CatalogGenerationID: generationID, EntryID: entryID, SourceFingerprint: sourceFingerprint,
			EntryFingerprint: strings.Repeat("b", 64), ProviderCapabilityRevision: 1,
			Capability: textProfile.Capability, CapabilitySchema: textProfile.CapabilitySchema,
			PipelineFingerprint: "text-pipeline-v1", OutputProfile: textProfile.OutputProfile,
			SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
			EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
			QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover text-extract job %d: %v", index, err)
		}
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			Backfill: backupasset.ProcessingBackfillConfig{
				BatchSize: 1, InspectedLimit: 2, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	source, ok := newDerivedBackfillAdapter(runtime).(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}

	firstInspected := 0
	stopFirst := countCatalogEntriesInspected(t, db, &firstInspected, "first-incomplete")
	firstExpected, err := source.ExpectedDescriptors(context.Background(), request)
	stopFirst()
	if err != nil {
		t.Fatalf("first ExpectedDescriptors: %v", err)
	}
	if !expectedContainsEntry(firstExpected, leftoverWalkUnprovenEntryID) {
		t.Fatalf("first ExpectedDescriptors=%+v, want leftover-walk-unproven after scanning already-proven leftover text", firstExpected)
	}
	if firstInspected > 2 {
		t.Fatalf("first leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", firstInspected)
	}

	canceledAt := now.Add(time.Second)
	if err := db.Model(&model.BackupAssetProcessingJob{}).
		Where("id = ?", strings.Repeat("a", 31)+"0").
		Updates(map[string]any{
			"state": string(processing.ProcessingCanceled), "is_current": false, "updated_at": canceledAt,
		}).Error; err != nil {
		t.Fatalf("mark already-scanned leftover text-extract job canceled: %v", err)
	}

	found := false
	completedEmpty := false
	for attempt := 0; attempt < 6; attempt++ {
		inspected := 0
		stop := countCatalogEntriesInspected(t, db, &inspected, fmt.Sprintf("resume-canceled-%d", attempt))
		expected, resumeErr := source.ExpectedDescriptors(context.Background(), request)
		stop()
		if resumeErr != nil {
			t.Fatalf("post-cancel ExpectedDescriptors: %v", resumeErr)
		}
		if inspected > 2 {
			t.Fatalf("post-cancel leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", inspected)
		}
		if expectedContainsCapability(expected, leftoverIDs[0], textProfile.Capability) {
			found = true
			break
		}
		if expectedContainsEntry(expected, leftoverWalkUnprovenEntryID) {
			continue
		}
		if len(expected) == 0 {
			completedEmpty = true
			break
		}
		t.Fatalf("post-cancel ExpectedDescriptors=%+v, want canceled text.extract, leftover-walk-unproven, or empty", expected)
	}
	if completedEmpty {
		stickyInspected := 0
		stopSticky := countCatalogEntriesInspected(t, db, &stickyInspected, "sticky-after-midwalk-cancel")
		stickyExpected, stickyErr := source.ExpectedDescriptors(context.Background(), request)
		stopSticky()
		if stickyErr != nil {
			t.Fatalf("sticky ExpectedDescriptors after mid-walk cancel: %v", stickyErr)
		}
		if len(stickyExpected) == 0 && stickyInspected == 0 {
			t.Fatal("walk.complete baked in a leftover job canceled after that entry was already scanned")
		}
	}
	if !found {
		t.Fatal("canceled leftover text.extract never reappeared as unproven after the incomplete walk finished")
	}

	requester := &derivedWorkRequesterSpy{}
	queueAdapter := derivedBackfillAdapter{
		requestWork: requester,
		descriptors: runtime.rebuildDerivedDescriptors,
		expected:    runtime.collectUnprovenRebuildDerivedDescriptors,
	}
	queued, err := queueAdapter.QueueLowPriorityDerivedBackfill(context.Background(), request)
	if err != nil {
		t.Fatalf("Queue after mid-walk cancel: %v", err)
	}
	if queued < 1 {
		t.Fatal("Queue stayed empty after a leftover job canceled behind the leftover cursor")
	}
	foundQueued := false
	for _, work := range requester.requests {
		if work.Descriptor.Source.EntryID == leftoverWalkUnprovenEntryID {
			t.Fatalf("Queue tried to enqueue leftover-walk-unproven after mid-walk cancel: %+v", work.Descriptor)
		}
		if work.Descriptor.Capability == textProfile.Capability && work.Descriptor.Source.EntryID == leftoverIDs[0] {
			foundQueued = true
		}
	}
	if !foundQueued {
		t.Fatalf("Queue requests=%+v, want text.extract for canceled leftover %s", requester.requests, leftoverIDs[0])
	}
}

func TestRebuildDerivedDescriptorsCompleteWalkReopensWhenSecretClassifyEnabled(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.CatalogGeneration{}, &model.CatalogEntry{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 16, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("1", 32)
	pointID := strings.Repeat("2", 32)
	generationID := strings.Repeat("3", 32)
	leftoverIDs := []string{
		strings.Repeat("4", 63) + "0",
		strings.Repeat("4", 63) + "1",
		strings.Repeat("4", 63) + "2",
		strings.Repeat("4", 63) + "3",
	}
	sourceFingerprint := strings.Repeat("a", 64)
	if err := db.Create(&model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "derived-leftover-secret-switch",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed rebuild repository: %v", err)
	}
	captured := now.Add(-time.Hour)
	if err := db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID, Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: sourceFingerprint,
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild point: %v", err)
	}
	if err := db.Create(&model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete),
		IsActive: true, SourceFingerprint: sourceFingerprint, ExpectedEntryCount: 4, WrittenEntryCount: 4,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed catalog generation: %v", err)
	}
	for index, entryID := range leftoverIDs {
		if err := db.Create(&model.CatalogEntry{
			GenerationID: generationID, EntryID: entryID, RecoveryPointID: pointID,
			NormalizedPath: "/notes/leftover-" + entryID[len(entryID)-1:] + ".txt",
			Name:           "leftover-" + entryID[len(entryID)-1:] + ".txt",
			EntryType:      string(backupasset.CatalogEntryFile), Size: 32, MimeType: "text/plain",
			Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong",
			EncryptedProviderLocator: "FAKE_REBUILD_LEFTOVER_SECRET_ENTRY_LOCATOR_FOR_TEST_ONLY",
			SecurityState:            "sealed", CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed leftover catalog entry %d: %v", index, err)
		}
	}
	workerID := strings.Repeat("d", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("e", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed rebuild worker: %v", err)
	}
	secretProfile, ok := capabilityspec.Lookup(capabilityspec.CapabilitySecretClassify, capabilityspec.ProfileBoundedSecretV1, true)
	if !ok {
		t.Fatal("secret classify profile missing")
	}
	if secretProfile.EnabledByDefault {
		t.Fatal("secret.classify must stay opt-in; Lookup under the switch still has EnabledByDefault=false")
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("f", 32), WorkerID: workerID, Capability: secretProfile.Capability,
		CapabilitySchema: secretProfile.CapabilitySchema, PipelineFingerprint: "secret-pipeline-v1",
		OutputProfile: secretProfile.OutputProfile, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("7", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed secret.classify advertisement: %v", err)
	}

	runtime := &managedProcessingRuntime{
		db:          db,
		coordinator: &processing.Coordinator{},
		config: backupasset.ProcessingConfig{
			SecretClassify: false,
			Backfill: backupasset.ProcessingBackfillConfig{
				BatchSize: 1, InspectedLimit: 2, JobsPerHour: 100, BytesPerHour: 1 << 30,
				ProviderConcurrency: 4, CapabilityConcurrency: 4,
				RecentWindow: 24 * time.Hour, HistoryAgingStep: time.Hour,
			},
		},
		now: func() time.Time { return now },
	}
	request := repository.DerivedBackfillRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID, Priority: "low",
	}
	source, ok := newDerivedBackfillAdapter(runtime).(repository.DerivedExpectationSource)
	if !ok {
		t.Fatal("production derived backfill adapter does not expose ExpectedDescriptors")
	}

	completed := false
	for attempt := 0; attempt < 6; attempt++ {
		expected, err := source.ExpectedDescriptors(context.Background(), request)
		if err != nil {
			t.Fatalf("pre-switch ExpectedDescriptors: %v", err)
		}
		if len(expected) == 0 {
			completed = true
			break
		}
		if expectedContainsEntry(expected, leftoverWalkUnprovenEntryID) {
			continue
		}
		t.Fatalf("pre-switch ExpectedDescriptors=%+v, want leftover-walk-unproven or completed empty", expected)
	}
	if !completed {
		t.Fatal("leftover walk never reached complete empty expected before SecretClassify enable")
	}

	sameSwitchInspected := 0
	stopSame := countCatalogEntriesInspected(t, db, &sameSwitchInspected, "same-secret-off")
	sameExpected, err := source.ExpectedDescriptors(context.Background(), request)
	stopSame()
	if err != nil {
		t.Fatalf("same-switch ExpectedDescriptors: %v", err)
	}
	if len(sameExpected) != 0 {
		t.Fatalf("completed leftover walk with SecretClassify=false returned %+v, want empty", sameExpected)
	}
	if sameSwitchInspected != 0 {
		t.Fatalf("completed leftover walk rescanned %d catalog rows before SecretClassify flipped", sameSwitchInspected)
	}

	runtime.mu.Lock()
	runtime.config.SecretClassify = true
	runtime.mu.Unlock()

	reopenInspected := 0
	stopReopen := countCatalogEntriesInspected(t, db, &reopenInspected, "reopen-secret")
	reopened, err := source.ExpectedDescriptors(context.Background(), request)
	stopReopen()
	if err != nil {
		t.Fatalf("post-switch ExpectedDescriptors: %v", err)
	}
	if len(reopened) == 0 {
		t.Fatal("walk.complete stayed proven after SecretClassify enabled advertised leftover secret.classify work")
	}
	if reopenInspected > 2 {
		t.Fatalf("reopened leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", reopenInspected)
	}
	if !expectedContainsCapability(reopened, leftoverIDs[0], secretProfile.Capability) {
		if expectedContainsEntry(reopened, leftoverWalkUnprovenEntryID) {
			if reopenInspected == 0 {
				t.Fatal("reopened leftover-walk-unproven without inspecting leftover catalog rows")
			}
		} else {
			t.Fatalf("reopened ExpectedDescriptors=%+v, want leftover text secret.classify or leftover-walk-unproven", reopened)
		}
		found := false
		for attempt := 0; attempt < 4; attempt++ {
			inspected := 0
			stop := countCatalogEntriesInspected(t, db, &inspected, fmt.Sprintf("resume-secret-%d", attempt))
			expected, resumeErr := source.ExpectedDescriptors(context.Background(), request)
			stop()
			if resumeErr != nil {
				t.Fatalf("resumed post-switch ExpectedDescriptors: %v", resumeErr)
			}
			if inspected > 2 {
				t.Fatalf("resumed leftover walk inspected=%d catalog rows, want <= InspectedLimit 2", inspected)
			}
			if expectedContainsCapability(expected, leftoverIDs[0], secretProfile.Capability) {
				found = true
				break
			}
			if len(expected) == 0 {
				t.Fatal("resumed ExpectedDescriptors returned empty before leftover secret.classify appeared")
			}
		}
		if !found {
			t.Fatal("SecretClassify enable never emitted leftover secret.classify as unproven")
		}
	}

	requester := &derivedWorkRequesterSpy{}
	queueAdapter := derivedBackfillAdapter{
		requestWork: requester,
		descriptors: runtime.rebuildDerivedDescriptors,
		expected:    runtime.collectUnprovenRebuildDerivedDescriptors,
	}
	queued, err := queueAdapter.QueueLowPriorityDerivedBackfill(context.Background(), request)
	if err != nil {
		t.Fatalf("Queue after SecretClassify enable: %v", err)
	}
	if queued < 1 {
		t.Fatal("Queue stayed empty after SecretClassify enabled leftover secret.classify work")
	}
	foundQueuedSecret := false
	for _, work := range requester.requests {
		if work.Descriptor.Source.EntryID == leftoverWalkUnprovenEntryID {
			t.Fatalf("Queue tried to enqueue leftover-walk-unproven after SecretClassify enable: %+v", work.Descriptor)
		}
		if work.Descriptor.Capability == secretProfile.Capability && work.Descriptor.Source.EntryID == leftoverIDs[0] {
			foundQueuedSecret = true
		}
	}
	if !foundQueuedSecret {
		t.Fatalf("Queue requests=%+v, want secret.classify for leftover text/plain %s", requester.requests, leftoverIDs[0])
	}
}

func countCatalogEntriesInspected(t *testing.T, db *gorm.DB, inspected *int, suffix string) func() {
	t.Helper()
	name := "count_rebuild_catalog_entries_" + t.Name() + "_" + suffix
	callback := func(tx *gorm.DB) {
		if tx == nil || tx.Statement == nil {
			return
		}
		entries, ok := tx.Statement.Dest.(*[]model.CatalogEntry)
		if !ok || entries == nil {
			return
		}
		*inspected += len(*entries)
	}
	if err := db.Callback().Query().After("gorm:query").Register(name, callback); err != nil {
		t.Fatalf("register catalog inspect counter: %v", err)
	}
	return func() {
		_ = db.Callback().Query().Remove(name)
	}
}

func expectedContainsEntry(expected []repository.ExpectedDerivedDescriptor, entryID string) bool {
	for _, descriptor := range expected {
		if descriptor.EntryID == entryID {
			return true
		}
	}
	return false
}

func expectedContainsCapability(expected []repository.ExpectedDerivedDescriptor, entryID, capability string) bool {
	for _, descriptor := range expected {
		if descriptor.EntryID == entryID && descriptor.Capability == capability {
			return true
		}
	}
	return false
}

func validRebuildWorkDescriptor(t *testing.T) processing.WorkDescriptorV1 {
	t.Helper()
	profile, ok := capabilityspec.Lookup(capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, false)
	if !ok {
		t.Fatal("text extract profile missing")
	}
	return processing.WorkDescriptorV1{
		SchemaVersion: 1,
		Source: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64),
		},
		CatalogGenerationID:        strings.Repeat("3", 32),
		SourceFingerprint:          strings.Repeat("a", 64),
		EntryFingerprint:           strings.Repeat("b", 64),
		ProviderCapabilityRevision: 1,
		Capability:                 profile.Capability,
		CapabilitySchema:           profile.CapabilitySchema,
		PipelineFingerprint:        strings.Repeat("c", 64),
		OutputProfile:              profile.OutputProfile,
		SecurityPolicyRevision:     processingSecurityPolicyRevision,
		Parameters:                 processing.CanonicalProductionParameters(profile),
	}
}
