package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

func TestRuntimeBackfillQuotaSQLite(t *testing.T) {
	runRuntimeBackfillQuota(t, "sqlite", "")
}

func TestRuntimeBackfillQuotaPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runRuntimeBackfillQuota(t, "postgres", dsn)
}

func runRuntimeBackfillQuota(t *testing.T, engine, dsn string) {
	t.Run("RepeatedCollections", func(t *testing.T) { runtimeQuotaRepeated(t, newRuntimeQuotaFixture(t, engine, dsn)) })
	t.Run("TwoInstancesPolicyFreshness", func(t *testing.T) { runtimeQuotaFreshness(t, newRuntimeQuotaFixture(t, engine, dsn)) })
	t.Run("RestartPersistentWindow", func(t *testing.T) { runtimeQuotaRestart(t, newRuntimeQuotaFixture(t, engine, dsn)) })
	t.Run("ExpectedAfterDenial", func(t *testing.T) { runtimeQuotaExpected(t, newRuntimeQuotaFixture(t, engine, dsn)) })
	t.Run("ContinuationDeferral", func(t *testing.T) { runtimeQuotaContinuations(t, newRuntimeQuotaFixture(t, engine, dsn)) })
}

type runtimeQuotaFixture struct {
	engine     string
	connection string
	db         [2]*gorm.DB
	runtime    [2]*managedProcessingRuntime
	grants     [2]*processing.GrantService
	now        time.Time
	request    repository.DerivedBackfillRequest
	entries    []string
	worker     string
}

func newRuntimeQuotaFixture(t *testing.T, engine, dsn string) *runtimeQuotaFixture {
	t.Helper()
	f := &runtimeQuotaFixture{engine: engine, now: time.Date(2026, 8, 20, 9, 59, 59, 0, time.UTC)}
	if engine == "sqlite" {
		f.connection = filepath.Join(t.TempDir(), "runtime-quota.db")
	} else {
		parsed, err := url.Parse(dsn)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			t.Fatalf("invalid PostgreSQL URL: %v", err)
		}
		base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("xirang_runtime_quota_%d", time.Now().UnixNano())
		if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		q := parsed.Query()
		q.Set("search_path", schema)
		q.Set("timezone", "UTC")
		parsed.RawQuery = q.Encode()
		f.connection = parsed.String()
		t.Cleanup(func() {
			_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
			if sqlDB, err := base.DB(); err == nil {
				_ = sqlDB.Close()
			}
		})
	}
	f.db[0] = f.open(t)
	if err := database.RunMigrations(f.db[0], engine); err != nil {
		t.Fatal(err)
	}
	f.db[1] = f.open(t)
	t.Cleanup(func() {
		for _, db := range f.db {
			if db != nil {
				if sqlDB, err := db.DB(); err == nil {
					_ = sqlDB.Close()
				}
			}
		}
	})
	service := settings.NewService(f.db[0])
	for key, value := range map[string]string{
		"backup_assets.enabled":                                    "true",
		"backup_assets.processing_backfill_paused":                 "false",
		"backup_assets.processing_backfill_batch_size":             "1",
		"backup_assets.processing_backfill_jobs_per_hour":          "2",
		"backup_assets.processing_backfill_bytes_per_hour":         "65536",
		"backup_assets.processing_backfill_provider_concurrency":   "8",
		"backup_assets.processing_backfill_capability_concurrency": "8",
	} {
		if err := service.Update(key, value); err != nil {
			t.Fatalf("setting %s: %v", key, err)
		}
	}
	f.seed(t)
	f.start(t, 0)
	f.start(t, 1)
	return f
}

func (f *runtimeQuotaFixture) open(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := config.Config{DBType: f.engine}
	if f.engine == "sqlite" {
		cfg.SQLitePath = f.connection
	} else {
		cfg.PostgresDSN = f.connection
	}
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func (f *runtimeQuotaFixture) start(t *testing.T, index int) {
	t.Helper()
	db := f.db[index]
	clock := func() time.Time { return f.now }
	lease, err := backupasset.NewLeaseService(db, clock, backupasset.LeaseConfig{Duration: 2 * time.Minute, Heartbeat: 20 * time.Second, AbsoluteDeadline: 3 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	service := settings.NewService(db)
	runtime := &managedProcessingRuntime{db: db, settings: service, foundation: backupasset.NewFoundationService(service), lease: lease, now: clock, authorize: processingRuntimeAssetAuthorizerFake{}}
	cfg, err := runtime.foundation.ProcessingConfig()
	if err != nil {
		t.Fatal(err)
	}
	runtime.config = cfg
	if err := runtime.initializeControlPlane(cfg); err != nil {
		t.Fatal(err)
	}
	grants, err := processing.NewGrantService(db, lease, clock, processing.GrantConfig{TTL: time.Minute, InputLimits: processing.GrantLimits{MaxRequests: 8, MaxBytesPerRequest: 65536, MaxCumulativeBytes: 131072, MaxInFlight: 2}, SinkLimits: processing.GrantLimits{MaxRequests: 4, MaxBytesPerRequest: 65536, MaxCumulativeBytes: 131072, MaxInFlight: 2}})
	if err != nil {
		t.Fatal(err)
	}
	f.runtime[index], f.grants[index] = runtime, grants
}

func (f *runtimeQuotaFixture) seed(t *testing.T) {
	t.Helper()
	now := f.now
	repo, point, generation := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	f.entries = []string{strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("6", 64), strings.Repeat("7", 64)}
	fingerprint := strings.Repeat("a", 64)
	captured := now.Add(-time.Hour)
	rows := []any{
		&model.BackupRepository{ID: repo, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repo, DisplayName: "runtime-quota", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now},
		&model.RecoveryPoint{ID: point, RepositoryID: repo, Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: fingerprint, CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now},
		&model.CatalogGeneration{ID: generation, RecoveryPointID: point, Generation: 1, State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: fingerprint, ExpectedEntryCount: 4, WrittenEntryCount: 4, StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now},
	}
	for _, row := range rows {
		if err := f.db[0].Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range f.entries {
		row := model.CatalogEntry{GenerationID: generation, EntryID: entry, RecoveryPointID: point, NormalizedPath: "/notes/" + entry[:8] + ".txt", Name: entry[:8] + ".txt", EntryType: string(backupasset.CatalogEntryFile), Size: 32768, MimeType: "text/plain", Fingerprint: strings.Repeat("b", 64), FingerprintStrength: "strong", SecurityState: "sealed", CreatedAt: now}
		if err := f.db[0].Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.worker = strings.Repeat("c", 32)
	worker := model.BackupAssetWorkerIdentity{ID: f.worker, TransportKind: "local", TransportFingerprint: strings.Repeat("d", 64), InstanceID: strings.Repeat("e", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion, TrustState: "active", HealthState: "ready", InteractiveSlots: 2, BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now}
	capability := model.BackupAssetWorkerCapability{ID: strings.Repeat("f", 32), WorkerID: f.worker, Capability: capabilityspec.CapabilityTextExtract, CapabilitySchema: "text.extract.v1", PipelineFingerprint: "text-pipeline-v1", OutputProfile: capabilityspec.ProfileBoundedTextV1, InputModes: "stat,sequential", LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("e", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now}
	for _, row := range []any{&worker, &capability} {
		if err := f.db[0].Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.request = repository.DerivedBackfillRequest{RepositoryID: repo, RecoveryPointID: point, CatalogGenerationID: generation, Priority: "low"}
}

func (f *runtimeQuotaFixture) policy(t *testing.T, paused bool, jobs int, bytes int64, provider, capability int) {
	t.Helper()
	current, err := f.runtime[0].ProcessingBackfillPolicy()
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.runtime[0].UpdateProcessingBackfillPolicy(context.Background(), ProcessingBackfillPolicyUpdate{ExpectedRevision: current.Revision, Paused: paused, BatchSize: 1, JobsPerHour: jobs, BytesPerHour: bytes, ProviderConcurrency: provider, CapabilityConcurrency: capability})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *runtimeQuotaFixture) queue(t *testing.T, index, want int) {
	t.Helper()
	got, err := f.runtime[index].QueueLowPriorityDerivedBackfill(context.Background(), f.request)
	if err != nil || got != want {
		t.Fatalf("queue=%d err=%v want=%d", got, err, want)
	}
}

func (f *runtimeQuotaFixture) usage(t *testing.T, index int, jobs, bytes int64) {
	t.Helper()
	_, usage, err := f.runtime[index].coordinator.BackfillSnapshot(context.Background())
	if err != nil || usage.Jobs != jobs || usage.Bytes != bytes {
		t.Fatalf("usage=%+v err=%v want=%d/%d", usage, err, jobs, bytes)
	}
}

func runtimeQuotaRepeated(t *testing.T, f *runtimeQuotaFixture) {
	f.policy(t, false, 3, 65536, 8, 8)
	f.queue(t, 0, 1)
	f.usage(t, 1, 1, 32768)
	f.queue(t, 1, 1)
	f.usage(t, 0, 2, 65536)
	f.queue(t, 0, 0)
	f.usage(t, 1, 2, 65536)
	f.policy(t, false, 3, 131072, 8, 8)
	f.queue(t, 1, 1)
	f.usage(t, 0, 3, 98304)
	f.queue(t, 0, 0)
	f.usage(t, 1, 3, 98304)
	attempt, err := f.runtime[1].coordinator.PullAttempt(context.Background(), processing.PullRequest{WorkerID: f.worker}, f.grants[1])
	if err != nil || attempt.Lease.JobID == "" {
		t.Fatalf("pull=%+v err=%v", attempt, err)
	}
}

func runtimeQuotaFreshness(t *testing.T, f *runtimeQuotaFixture) {
	ctx := context.Background()
	f.queue(t, 1, 1)
	descriptors, err := f.runtime[1].rebuildDerivedDescriptors(ctx, f.request)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("descriptors=%+v err=%v", descriptors, err)
	}
	request := processing.WorkRequest{Descriptor: descriptors[0], Interest: processing.InterestRequest{OwnerKind: processing.InterestSystem, OwnerKey: "quota-freshness", PriorityClass: processing.PriorityBackground, Priority: 100}}
	f.policy(t, false, 1, 65536, 8, 8)
	f.queue(t, 1, 0)
	if _, err := f.runtime[1].RequestWork(ctx, request); !errors.Is(err, processing.ErrBackfillDenied) {
		t.Fatalf("lowered quota request: %v", err)
	}
	f.policy(t, true, 3, 131072, 8, 8)
	if _, err := f.runtime[1].coordinator.PullAttempt(ctx, processing.PullRequest{WorkerID: f.worker}, f.grants[1]); !errors.Is(err, processing.ErrNoWork) {
		t.Fatalf("paused pull: %v", err)
	}
	f.queue(t, 1, 0)
	if _, err := f.runtime[1].RequestWork(ctx, request); !errors.Is(err, processing.ErrBackfillDenied) {
		t.Fatalf("paused request: %v", err)
	}
	f.usage(t, 1, 1, 32768)
	f.policy(t, false, 3, 131072, 8, 8)
	f.queue(t, 1, 1)
	if _, err := f.runtime[1].coordinator.PullAttempt(ctx, processing.PullRequest{WorkerID: f.worker}, f.grants[1]); err != nil {
		t.Fatalf("resumed pull: %v", err)
	}
	f.usage(t, 1, 2, 65536)
	f.policy(t, false, 3, 131072, 1, 1)
	if _, err := f.runtime[1].coordinator.PullAttempt(ctx, processing.PullRequest{WorkerID: f.worker}, f.grants[1]); !errors.Is(err, processing.ErrNoWork) {
		t.Fatalf("lowered concurrency pull: %v", err)
	}
	f.policy(t, false, 3, 131072, 8, 8)
	if _, err := f.runtime[1].coordinator.PullAttempt(ctx, processing.PullRequest{WorkerID: f.worker}, f.grants[1]); err != nil {
		t.Fatalf("raised concurrency pull: %v", err)
	}
	f.usage(t, 1, 2, 65536)
}

func runtimeQuotaRestart(t *testing.T, f *runtimeQuotaFixture) {
	f.queue(t, 0, 1)
	f.queue(t, 1, 1)
	for i := range f.db {
		sqlDB, err := f.db[i].DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
		f.db[i] = f.open(t)
		f.start(t, i)
	}
	f.usage(t, 1, 2, 65536)
	f.queue(t, 1, 0)
	f.now = f.now.Add(time.Second)
	f.usage(t, 1, 0, 0)
	f.queue(t, 1, 1)
	f.usage(t, 0, 1, 32768)
}

type runtimeQuotaRequestBarrier struct {
	runtime *managedProcessingRuntime
	arrived chan struct{}
	release chan struct{}
}

func (barrier runtimeQuotaRequestBarrier) RequestWork(ctx context.Context, request processing.WorkRequest) (processing.WorkResult, error) {
	close(barrier.arrived)
	select {
	case <-barrier.release:
		return barrier.runtime.RequestWork(ctx, request)
	case <-ctx.Done():
		return processing.WorkResult{}, ctx.Err()
	}
}

func runtimeQuotaExpected(t *testing.T, f *runtimeQuotaFixture) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	barrier := runtimeQuotaRequestBarrier{runtime: f.runtime[1], arrived: make(chan struct{}), release: make(chan struct{})}
	adapter := derivedBackfillAdapter{requestWork: barrier, descriptors: f.runtime[1].rebuildDerivedDescriptors, expected: f.runtime[1].collectUnprovenRebuildDerivedDescriptors}
	type result struct {
		queued int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		queued, err := adapter.QueueLowPriorityDerivedBackfill(ctx, f.request)
		done <- result{queued, err}
	}()
	select {
	case <-barrier.arrived:
	case <-ctx.Done():
		t.Fatal("request did not reach barrier")
	}
	f.policy(t, true, 2, 65536, 8, 8)
	close(barrier.release)
	select {
	case got := <-done:
		if got.err != nil || got.queued != 0 {
			t.Fatalf("racing queue=%d err=%v", got.queued, got.err)
		}
	case <-ctx.Done():
		t.Fatal("racing queue did not finish")
	}
	f.queue(t, 1, 0)
	f.usage(t, 1, 0, 0)
	for _, table := range []any{&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{}} {
		var count int64
		if err := f.db[1].Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("denied transaction retained %T rows=%d err=%v", table, count, err)
		}
	}
	expected, err := adapter.ExpectedDescriptors(ctx, f.request)
	if err != nil || len(expected) == 0 || expected[0].EntryID != f.entries[0] {
		t.Fatalf("expected=%+v err=%v", expected, err)
	}
	walk := f.runtime[1].loadDerivedBackfillWalk(f.request.CatalogGenerationID, f.request.RecoveryPointID)
	if walk.complete || walk.afterEntryID != "" {
		t.Fatalf("denial advanced walk: %+v", walk)
	}
	f.policy(t, false, 2, 65536, 8, 8)
	f.queue(t, 1, 1)
}

func runtimeQuotaContinuations(t *testing.T, f *runtimeQuotaFixture) {
	ctx := context.Background()
	for i := range 2 {
		f.seedSecretCandidate(t, i)
	}
	if err := f.runtime[0].settings.Update("backup_assets.processing_secret_classify", "true"); err != nil {
		t.Fatal(err)
	}
	secret, ok := capabilityspec.Lookup(capabilityspec.CapabilitySecretClassify, capabilityspec.ProfileBoundedSecretV1, true)
	if !ok {
		t.Fatal("secret profile missing")
	}
	f.advertise(t, "00000000000000000000000000000091", secret, "secret-pipeline-v1")
	now := f.now
	metadata := model.BackupAssetUpdaterMetadata{ID: "00000000000000000000000000000092", SourceKind: "admin_registered", SourceID: "runtime-quota", Version: "1.0.0", ManifestDigest: strings.Repeat("a", 64), SigningKeyFingerprint: strings.Repeat("b", 64), BundleFingerprint: strings.Repeat("c", 64), State: "active", VerifiedAt: &now, ActivatedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := f.db[0].Create(&metadata).Error; err != nil {
		t.Fatal(err)
	}
	profile, ok := capabilityspec.Lookup(capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1, false)
	if !ok {
		t.Fatal("malware profile missing")
	}
	pipeline, err := f.runtime[1].activePipelineFingerprint(ctx, profile.Capability, profile.OutputProfile)
	if err != nil || pipeline == "" {
		t.Fatalf("malware pipeline=%q err=%v", pipeline, err)
	}
	f.advertise(t, "00000000000000000000000000000093", profile, pipeline)
	f.policy(t, true, 1, 65536, 8, 8)
	asset := content.AuthorizedAsset{Ref: backupasset.AssetRef{RecoveryPointID: f.request.RecoveryPointID, EntryID: f.entries[2]}, CatalogGenerationID: f.request.CatalogGenerationID, SourceFingerprint: strings.Repeat("a", 64), EntryFingerprint: strings.Repeat("b", 64), ProviderCapabilityRevision: 1, Size: 32768}
	decision, err := f.runtime[1].malwareSafetyForAsset(ctx, asset)
	if err != nil || !decision.Active || decision.Safe || decision.Status != capabilityspec.ScanNotScanned {
		t.Fatalf("malware decision=%+v err=%v", decision, err)
	}
	if scheduled, err := f.runtime[1].scheduleSecretContinuations(ctx); err != nil || scheduled != 0 {
		t.Fatalf("paused secret scheduled=%d err=%v", scheduled, err)
	}
	var count int64
	if err := f.db[1].Model(&model.BackupAssetProcessingJob{}).Where("capability IN ?", []string{profile.Capability, secret.Capability}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("denied jobs=%d err=%v", count, err)
	}
	f.usage(t, 1, 0, 0)
	f.policy(t, false, 1, 65536, 8, 8)
	if scheduled, err := f.runtime[1].scheduleSecretContinuations(ctx); err != nil || scheduled != 1 {
		t.Fatalf("partial secret scheduled=%d err=%v", scheduled, err)
	}
	if scheduled, err := f.runtime[1].scheduleSecretContinuations(ctx); err != nil || scheduled != 0 {
		t.Fatalf("exhausted secret scheduled=%d err=%v", scheduled, err)
	}
	f.usage(t, 1, 1, 32768)
	decision, err = f.runtime[1].malwareSafetyForAsset(ctx, asset)
	if err != nil || decision.Safe || decision.Status != capabilityspec.ScanNotScanned {
		t.Fatalf("exhausted malware decision=%+v err=%v", decision, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.runtime[1].requestMalwareContinuation(canceled, asset, profile, pipeline); err == nil {
		t.Fatal("malware hid a database/context error")
	}
	if _, err := f.runtime[1].scheduleSecretContinuations(canceled); err == nil {
		t.Fatal("secret scheduler hid a database/context error")
	}
}

func (f *runtimeQuotaFixture) advertise(t *testing.T, id string, profile capabilityspec.Profile, pipeline string) {
	t.Helper()
	row := model.BackupAssetWorkerCapability{ID: id, WorkerID: f.worker, Capability: profile.Capability, CapabilitySchema: profile.CapabilitySchema, PipelineFingerprint: pipeline, OutputProfile: profile.OutputProfile, InputModes: "stat,sequential,range", LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("a", 64), HealthState: "ready", CreatedAt: f.now, UpdatedAt: f.now}
	if err := f.db[0].Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

// These published rows are controlled inputs to the continuation query. The
// jobs and attempts themselves are created through the real coordinator.
func (f *runtimeQuotaFixture) seedSecretCandidate(t *testing.T, index int) {
	t.Helper()
	ctx := context.Background()
	descriptors, err := f.runtime[0].rebuildDerivedDescriptors(ctx, f.request)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("text descriptor=%+v err=%v", descriptors, err)
	}
	d := descriptors[0]
	job, err := f.runtime[0].RequestWork(ctx, processing.WorkRequest{Descriptor: d, Interest: processing.InterestRequest{OwnerKind: processing.InterestSystem, OwnerKey: fmt.Sprintf("secret-parent-%d", index), PriorityClass: processing.PriorityInteractive, Priority: 100}})
	if err != nil || !job.Created {
		t.Fatalf("parent job=%+v err=%v", job, err)
	}
	attempt, err := f.runtime[0].coordinator.PullAttempt(ctx, processing.PullRequest{WorkerID: f.worker}, f.grants[0])
	if err != nil || attempt.Lease.JobID != job.JobID {
		t.Fatalf("parent attempt=%+v err=%v", attempt, err)
	}
	if err := f.runtime[0].lease.Release(ctx, attempt.Lease.RecoveryPointFence); err != nil {
		t.Fatal(err)
	}
	now := f.now
	if err := f.db[0].Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attempt.Lease.AttemptID).Updates(map[string]any{"state": "succeeded", "is_current": false, "finished_at": now, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db[0].Model(&model.BackupAssetProcessingJob{}).Where("id = ?", job.JobID).Updates(map[string]any{"state": "succeeded", "is_current": false, "finished_at": now, "updated_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	setID := fmt.Sprintf("%032x", 256+index)
	blobID := fmt.Sprintf("%032x", 512+index)
	artifactID := fmt.Sprintf("%032x", 768+index)
	refID := fmt.Sprintf("%032x", 1024+index)
	digest := fmt.Sprintf("%064x", 10000+index)
	set := model.BackupAssetDerivedArtifactSet{ID: setID, JobID: job.JobID, AttemptID: attempt.Lease.AttemptID, WorkKey: job.WorkKey, RecoveryPointID: d.Source.RecoveryPointID, CatalogGenerationID: d.CatalogGenerationID, EntryID: d.Source.EntryID, SourceFingerprint: d.SourceFingerprint, SecurityPolicyRevision: processingSecurityPolicyRevision, ManifestDigest: fmt.Sprintf("%064x", 20000+index), State: "active", Completeness: string(processing.ArtifactComplete), ArtifactCount: 1, TotalPlaintextBytes: 32, ProjectionRequired: true, ProjectionPublished: true, ProjectionRevision: 1, CreatedAt: now, UpdatedAt: now}
	blob := model.BackupAssetDerivedBlob{ID: blobID, PlaintextDigest: digest, PlaintextSize: 32, PhysicalSize: 64, CipherFormatVersion: 1, ChunkSize: 65536, ChunkCount: 1, NoncePrefix: make([]byte, 8), OpaqueLocator: fmt.Sprintf("quota-blob-%d", index), WrappedDEK: []byte{1}, EnvelopeNonce: make([]byte, 12), DerivedKEKVersion: 1, State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now}
	artifact := model.BackupAssetDerivedArtifact{ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: string(processing.ArtifactRoleContent), MediaType: "text/plain", PlaintextSize: 32, PlaintextDigest: digest, Completeness: string(processing.ArtifactComplete), CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`), BlobID: blobID, ExcerptRef: artifactID, CreatedAt: now}
	ref := model.BackupAssetDerivedBlobReference{ID: refID, BlobID: blobID, ArtifactID: artifactID, RecoveryPointID: d.Source.RecoveryPointID, CatalogGenerationID: d.CatalogGenerationID, EntryID: d.Source.EntryID, SourceFingerprint: d.SourceFingerprint, State: "active", CreatedAt: now, UpdatedAt: now}
	for _, row := range []any{&set, &blob, &artifact, &ref} {
		if err := f.db[0].Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}
