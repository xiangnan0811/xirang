package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sharedSearchContextKey struct{}

func TestProcessingSharedBlobRealSearchSQLite(t *testing.T) {
	runSharedSearchContract(t, "sqlite", "")
}

func TestProcessingSharedBlobRealSearchPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST") == "1" || os.Getenv("REQUIRE_POSTGRES_SEARCH_TEST") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runSharedSearchContract(t, "postgres", dsn)
}

func runSharedSearchContract(t *testing.T, engine, dsn string) {
	t.Run("SamePointPublishRevoke", func(t *testing.T) { sharedSearchSamePoint(t, newSharedSearchFixture(t, engine, dsn)) })
	t.Run("CrossPointReferenceIsolation", func(t *testing.T) { sharedSearchCrossPoint(t, newSharedSearchFixture(t, engine, dsn)) })
}

type sharedSearchFixture struct {
	db          *gorm.DB
	lease       *backupasset.LeaseService
	coordinator *processing.Coordinator
	grants      *processing.GrantService
	sink        *processing.ArtifactSink
	lifecycle   *processing.DerivedLifecycle
	keys        *backupasset.Keyring
	root        string
	now         time.Time
	worker      string
	payload     []byte
	declaration processing.ArtifactDeclaration
}

type sharedSearchWork struct {
	d        processing.WorkDescriptorV1
	job      processing.WorkResult
	attempt  processing.LeasedAttempt
	set      string
	artifact model.BackupAssetDerivedArtifact
}

func newSharedSearchFixture(t *testing.T, engine, dsn string) *sharedSearchFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SHARED_SEARCH_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	var db *gorm.DB
	var err error
	if engine == "sqlite" {
		db, err = database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "shared-search.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		})
	} else {
		parsed, err := url.Parse(dsn)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			t.Fatalf("invalid PostgreSQL URL: %v", err)
		}
		base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("xirang_shared_search_%d", time.Now().UnixNano())
		if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		q := parsed.Query()
		q.Set("search_path", schema)
		q.Set("timezone", "UTC")
		parsed.RawQuery = q.Encode()
		db, err = database.Open(config.Config{DBType: "postgres", PostgresDSN: parsed.String()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
			_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
			if sqlDB, err := base.DB(); err == nil {
				_ = sqlDB.Close()
			}
		})
	}
	if err := database.RunMigrations(db, engine); err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(16)
	} else {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	lease, err := backupasset.NewLeaseService(db, clock, backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := processing.NewCoordinator(db, lease, clock, processing.CoordinatorConfig{QueueMax: 100, InteractiveReservedSlots: 2, BackgroundSlots: 2, PullLease: 5 * time.Minute, AttemptTimeout: time.Hour, RetryMax: 3, BackfillPolicySource: processingRuntimeTestPolicy})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := processing.NewGrantService(db, lease, clock, processing.GrantConfig{TTL: 5 * time.Minute, InputLimits: processing.GrantLimits{MaxRequests: 8, MaxBytesPerRequest: 1024, MaxCumulativeBytes: 4096, MaxInFlight: 2}, SinkLimits: processing.GrantLimits{MaxRequests: 4, MaxBytesPerRequest: 1024, MaxCumulativeBytes: 4096, MaxInFlight: 2}})
	if err != nil {
		t.Fatal(err)
	}
	keys := backupasset.NewKeyring(db, clock)
	if _, err := keys.Ensure(context.Background(), backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Ensure(context.Background(), backupasset.KeyDomainSearchToken); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "derived")
	store, err := processing.NewDerivedStore(context.Background(), db, keys, processing.DerivedStoreConfig{Root: root, ChunkSize: 64 << 10, BlobMaxBytes: 1 << 20, GlobalMaxBytes: 8 << 20, ValidateRoot: func(context.Context, string) error { return nil }}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ingest, err := search.NewContentIngestService(search.ContentIngestDependencies{DB: db, Keys: keys, Lease: lease, Now: clock, Limits: search.DefaultContentIngestLimits()})
	if err != nil {
		t.Fatal(err)
	}
	port := runtimeDerivedProjectionPort{db: db, ingest: ingest, pipelineRevisions: func(context.Context) (runtimeProjectionRevisions, error) {
		return runtimeProjectionRevisions{Content: 2, OCR: 2}, nil
	}}
	lifecycle, err := processing.NewDerivedLifecycle(db, store, port, clock, lease)
	if err != nil {
		t.Fatal(err)
	}
	d := processingRuntimeWorkDescriptor()
	sink, err := processing.NewArtifactSink(db, lease, grants, store, lifecycle, processingRuntimeSourceRevalidatorFake{}, func(context.Context) (string, error) { return d.SecurityPolicyRevision, nil }, func(context.Context, string, string) (string, error) { return d.PipelineFingerprint, nil }, clock, processing.ArtifactSinkConfig{MaxArtifacts: 4, MaxArtifactBytes: 1 << 20, MaxTotalBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	workerID := strings.Repeat("d", 32)
	worker := model.BackupAssetWorkerIdentity{ID: workerID, TransportKind: "local", TransportFingerprint: strings.Repeat("d", 64), InstanceID: workerID, IdentityRevision: 1, ProtocolVersion: 1, TrustState: "active", HealthState: "ready", InteractiveSlots: 2, BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now}
	capability := model.BackupAssetWorkerCapability{ID: strings.Repeat("d", 31) + "f", WorkerID: workerID, Capability: d.Capability, CapabilitySchema: d.CapabilitySchema, PipelineFingerprint: d.PipelineFingerprint, OutputProfile: d.OutputProfile, InputModes: "stat,sequential,range", LimitsCanonical: []byte{1}, AdvertisementDigest: strings.Repeat("a", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&worker).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	payload := []byte("shared search needle")
	digest := sha256.Sum256(payload)
	decl := processing.ArtifactDeclaration{Ordinal: 0, Role: processing.ArtifactRoleContent, MediaType: "text/plain", PlaintextSize: int64(len(payload)), PlaintextDigest: hex.EncodeToString(digest[:]), Completeness: processing.ArtifactComplete, CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`)}
	return &sharedSearchFixture{db: db, lease: lease, coordinator: coordinator, grants: grants, sink: sink, lifecycle: lifecycle, keys: keys, root: root, now: now, worker: workerID, payload: payload, declaration: decl}
}

func (f *sharedSearchFixture) seedPoint(t *testing.T, d processing.WorkDescriptorV1, repositoryID, searchID string, entries []string) {
	t.Helper()
	now := f.now
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO backup_repositories (id, provider_kind, repository_identity, display_name, description, version_mode, status, capability_revision, capabilities_json, immutability_level, created_at, updated_at) VALUES (?, 'rsync', ?, 'shared-search', '', 'hardlink_tree', 'online', ?, '{}', 'xirang_managed', ?, ?)`, []any{repositoryID, repositoryID, d.ProviderCapabilityRevision, now, now}},
		{`INSERT INTO recovery_points (id, repository_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot, lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, observed_at, source_fingerprint, manifest_digest_algorithm, manifest_digest, entry_count, logical_bytes, consistency_json, fidelity_json, capability_revision, capabilities_json, immutability_level, physical_availability, hold_state, created_at, updated_at) VALUES (?, ?, '', 0, '', '{}', '', '', 'mutable_head', 'observed', ?, ?, 'sha256', '', ?, 1024, '{}', '{}', ?, '{}', 'mutable', 'online', 'none', ?, ?)`, []any{d.Source.RecoveryPointID, repositoryID, now, d.SourceFingerprint, len(entries), d.ProviderCapabilityRevision, now, now}},
		{`INSERT INTO catalog_generations (id, recovery_point_id, generation, state, is_active, source_fingerprint, expected_entry_count, written_entry_count, expected_digest, written_digest, error_code, correlation_id, started_at, finished_at, created_at, updated_at) VALUES (?, ?, 1, 'complete', ?, ?, ?, ?, '', '', '', '', ?, ?, ?, ?)`, []any{d.CatalogGenerationID, d.Source.RecoveryPointID, true, d.SourceFingerprint, len(entries), len(entries), now, now, now, now}},
	}
	for _, statement := range statements {
		if err := f.db.Exec(statement.query, statement.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, entry := range entries {
		if err := f.db.Exec(`INSERT INTO catalog_entries (generation_id, entry_id, recovery_point_id, normalized_path, name, entry_type, size, mode, owner, mime_type, fingerprint, fingerprint_strength, encrypted_provider_locator, security_state, created_at) VALUES (?, ?, ?, ?, ?, 'file', 1024, '', '', 'text/plain', ?, 'strong', '', 'non_secret', ?)`, d.CatalogGenerationID, entry, d.Source.RecoveryPointID, fmt.Sprintf("/shared/%d.txt", i), fmt.Sprintf("%d.txt", i), d.EntryFingerprint, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	key, err := f.keys.Ensure(context.Background(), backupasset.KeyDomainSearchToken)
	if err != nil {
		t.Fatal(err)
	}
	generation := model.BackupAssetSearchGeneration{ID: searchID, RecoveryPointID: d.Source.RecoveryPointID, CatalogGenerationID: d.CatalogGenerationID, Generation: 1, State: string(search.SearchGenerationComplete), IsActive: true, SourceFingerprint: d.SourceFingerprint, NormalizerVersion: search.NormalizerVersion, SearchKeyVersion: key.Version, ProjectionRevision: 1, LeaseID: strings.Repeat("9", 32), BuildAttemptID: strings.Repeat("8", 32), FenceTokenHash: strings.Repeat("7", 64), ExpectedDocumentCount: int64(len(entries)), WrittenDocumentCount: int64(len(entries)), StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := f.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		doc := model.BackupAssetSearchDocument{SearchGenerationID: searchID, DocumentID: entry, RecoveryPointID: d.Source.RecoveryPointID, CatalogGenerationID: d.CatalogGenerationID, EntryID: entry, Sensitivity: string(search.SensitivityNonSecret), ClassificationRevision: 1, MetadataRevision: 1, EntryType: "file", LineageToken: strings.Repeat("c", 64), PathGroupToken: strings.Repeat("d", 64), PathSortKey: entry, NameSortKey: entry, CreatedAt: now, UpdatedAt: now}
		field := model.BackupAssetSearchDocumentField{SearchGenerationID: searchID, DocumentID: entry, Field: string(search.SearchFieldContent), State: string(search.FieldCoverageUnavailable), CoverageRevision: 1, ClassificationRevision: 1, PipelineRevision: 1, IndexRevision: 1, SourceFingerprint: d.SourceFingerprint, UpdatedAt: now}
		if err := f.db.Create(&doc).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Create(&field).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func (f *sharedSearchFixture) prepare(t *testing.T, d processing.WorkDescriptorV1, owner string) sharedSearchWork {
	t.Helper()
	ctx := context.Background()
	job, err := f.coordinator.RequestWork(ctx, processing.WorkRequest{Descriptor: d, Interest: processing.InterestRequest{OwnerKind: processing.InterestSystem, OwnerKey: owner, PriorityClass: processing.PriorityInteractive, Priority: 100}})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := f.coordinator.PullAttempt(ctx, processing.PullRequest{WorkerID: f.worker}, f.grants)
	if err != nil || attempt.Lease.JobID != job.JobID {
		t.Fatalf("pull: %+v %v", attempt, err)
	}
	if _, err := f.grants.Activate(ctx, processing.ActivateGrantRequest{GrantID: attempt.Grants.Sink.GrantID, Kind: processing.GrantSink, JobID: job.JobID, AttemptID: attempt.Lease.AttemptID, WorkerID: f.worker, Secret: attempt.Grants.Sink.Secret}); err != nil {
		t.Fatal(err)
	}
	for i, state := range []processing.ProcessingState{processing.ProcessingFetching, processing.ProcessingMaterializing, processing.ProcessingProcessing, processing.ProcessingUploading} {
		if _, err := f.coordinator.TransitionAttempt(ctx, processing.AttemptTransitionRequest{JobID: job.JobID, AttemptID: attempt.Lease.AttemptID, WorkerID: f.worker, ExpectedRevision: int64(i + 2), To: state}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.sink.UploadArtifact(ctx, processing.UploadArtifactRequest{JobID: job.JobID, AttemptID: attempt.Lease.AttemptID, WorkerID: f.worker, GrantID: attempt.Grants.Sink.GrantID, Artifact: f.declaration}, bytes.NewReader(f.payload)); err != nil {
		t.Fatal(err)
	}
	return sharedSearchWork{d: d, job: job, attempt: attempt}
}

func (f *sharedSearchFixture) publish(ctx context.Context, w sharedSearchWork) (processing.CommitManifestResult, error) {
	return f.sink.CommitManifest(ctx, processing.CommitManifestRequest{JobID: w.job.JobID, AttemptID: w.attempt.Lease.AttemptID, WorkerID: f.worker, GrantID: w.attempt.Grants.Sink.GrantID, RecoveryPointFence: w.attempt.Lease.RecoveryPointFence, SecurityPolicyRevision: w.d.SecurityPolicyRevision, Artifacts: []processing.ArtifactDeclaration{f.declaration}})
}

func (f *sharedSearchFixture) complete(t *testing.T, w *sharedSearchWork, result processing.CommitManifestResult) {
	t.Helper()
	if !result.ProjectionRequired {
		t.Fatal("publication omitted Search")
	}
	w.set = result.ArtifactSetID
	if err := f.db.First(&w.artifact, "artifact_set_id = ?", w.set).Error; err != nil {
		t.Fatal(err)
	}
}

func (f *sharedSearchFixture) auth(w sharedSearchWork) processing.DerivedArtifactAuthorization {
	return processing.DerivedArtifactAuthorization{ArtifactID: w.artifact.ID, RecoveryPointID: w.d.Source.RecoveryPointID, CatalogGenerationID: w.d.CatalogGenerationID, EntryID: w.d.Source.EntryID, SourceFingerprint: w.d.SourceFingerprint}
}

func (f *sharedSearchFixture) assertRead(t *testing.T, w sharedSearchWork) {
	t.Helper()
	var out bytes.Buffer
	if err := f.lifecycle.ReadAuthorized(context.Background(), f.auth(w), &out); err != nil || !bytes.Equal(out.Bytes(), f.payload) {
		t.Fatalf("authorized read: %v", err)
	}
}

func (f *sharedSearchFixture) assertSearch(t *testing.T, w sharedSearchWork, present bool) {
	t.Helper()
	var count int64
	if err := f.db.Model(&model.BackupAssetSearchPosting{}).Where("document_id = ? AND field = ?", w.d.Source.EntryID, search.SearchFieldContent).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	var field model.BackupAssetSearchDocumentField
	if err := f.db.Where("document_id = ? AND field = ?", w.d.Source.EntryID, search.SearchFieldContent).First(&field).Error; err != nil {
		t.Fatal(err)
	}
	if present && (count == 0 || field.State != string(search.FieldCoverageComplete) || field.ExcerptRef == nil || *field.ExcerptRef != w.artifact.ID) {
		t.Fatalf("Search publication missing: postings=%d field=%+v", count, field)
	}
	if !present && (count != 0 || field.ExcerptRef != nil) {
		t.Fatalf("Search revocation incomplete: postings=%d field=%+v", count, field)
	}
}

func (f *sharedSearchFixture) revokeFence(t *testing.T, w sharedSearchWork) backupasset.Lease {
	t.Helper()
	lease, err := f.lease.Acquire(context.Background(), backupasset.AcquireLeaseRequest{RecoveryPointID: w.d.Source.RecoveryPointID, HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: w.job.JobID})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func sharedSearchSamePoint(t *testing.T, f *sharedSearchFixture) {
	d1 := processingRuntimeWorkDescriptor()
	d2 := d1
	d2.Source.EntryID = strings.Repeat("e", 64)
	f.seedPoint(t, d1, strings.Repeat("1", 32), strings.Repeat("2", 32), []string{d1.Source.EntryID, d2.Source.EntryID})
	a := f.prepare(t, d1, "search-same-a")
	result, err := f.publish(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	f.complete(t, &a, result)
	f.assertSearch(t, a, true)
	b := f.prepare(t, d2, "search-same-b")
	fence := f.revokeFence(t, a)
	defer f.lease.Release(context.Background(), fence.Fence)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if f.db.Dialector.Name() == "sqlite" {
		result, err = f.publish(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.lifecycle.RevokeSetFenced(ctx, a.set, processing.DerivedRevokeExpired, fence.Fence); err != nil {
			t.Fatal(err)
		}
	} else {
		control := f.db.WithContext(ctx).Begin()
		if control.Error != nil {
			t.Fatal(control.Error)
		}
		defer control.Rollback()
		var point model.RecoveryPoint
		if err := control.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", d1.Source.RecoveryPointID).Error; err != nil {
			t.Fatal(err)
		}
		publishedReady, revokedReady := make(chan struct{}), make(chan struct{})
		var publishedOnce, revokedOnce sync.Once
		const callback = "test:shared-search-point-barrier"
		if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement == nil || tx.Statement.Context == nil || tx.Statement.Table != "recovery_points" {
				return
			}
			if _, ok := tx.Statement.Clauses["FOR"]; !ok {
				return
			}
			switch tx.Statement.Context.Value(sharedSearchContextKey{}) {
			case "publish":
				publishedOnce.Do(func() { close(publishedReady) })
			case "revoke":
				revokedOnce.Do(func() { close(revokedReady) })
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
		var deadlock atomic.Bool
		observe := func(tx *gorm.DB) {
			if tx.Error == nil || tx.Statement == nil || tx.Statement.Context == nil {
				return
			}
			label, _ := tx.Statement.Context.Value(sharedSearchContextKey{}).(string)
			if label != "publish" && label != "revoke" {
				return
			}
			var pgErr *pgconn.PgError
			if errors.As(tx.Error, &pgErr) && pgErr.Code == "40P01" {
				deadlock.Store(true)
			}
		}
		for _, probe := range []struct {
			name     string
			register func(string, func(*gorm.DB)) error
			remove   func(string) error
		}{
			{"test:shared-search-query-error", func(n string, cb func(*gorm.DB)) error {
				return f.db.Callback().Query().After("gorm:query").Register(n, cb)
			}, f.db.Callback().Query().Remove},
			{"test:shared-search-update-error", func(n string, cb func(*gorm.DB)) error {
				return f.db.Callback().Update().After("gorm:update").Register(n, cb)
			}, f.db.Callback().Update().Remove},
			{"test:shared-search-create-error", func(n string, cb func(*gorm.DB)) error {
				return f.db.Callback().Create().After("gorm:create").Register(n, cb)
			}, f.db.Callback().Create().Remove},
			{"test:shared-search-delete-error", func(n string, cb func(*gorm.DB)) error {
				return f.db.Callback().Delete().After("gorm:delete").Register(n, cb)
			}, f.db.Callback().Delete().Remove},
			{"test:shared-search-raw-error", func(n string, cb func(*gorm.DB)) error {
				return f.db.Callback().Raw().After("gorm:raw").Register(n, cb)
			}, f.db.Callback().Raw().Remove},
			{"test:shared-search-row-error", func(n string, cb func(*gorm.DB)) error {
				return f.db.Callback().Row().After("gorm:row").Register(n, cb)
			}, f.db.Callback().Row().Remove},
		} {
			if err := probe.register(probe.name, observe); err != nil {
				t.Fatal(err)
			}
			probe := probe
			t.Cleanup(func() { _ = probe.remove(probe.name) })
		}
		type publishResult struct {
			result processing.CommitManifestResult
			err    error
		}
		publishDone := make(chan publishResult, 1)
		revokeDone := make(chan error, 1)
		go func() {
			result, err := f.publish(context.WithValue(ctx, sharedSearchContextKey{}, "publish"), b)
			publishDone <- publishResult{result, err}
		}()
		go func() {
			revokeDone <- f.lifecycle.RevokeSetFenced(context.WithValue(ctx, sharedSearchContextKey{}, "revoke"), a.set, processing.DerivedRevokeExpired, fence.Fence)
		}()
		for _, ready := range []<-chan struct{}{publishedReady, revokedReady} {
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatalf("point barrier: %v", ctx.Err())
			}
		}
		if err := control.Commit().Error; err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-publishDone:
			result, err = got.result, got.err
			if err != nil {
				t.Fatalf("Search publish: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case err := <-revokeDone:
			if err != nil {
				t.Fatalf("Search revoke: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if deadlock.Load() {
			t.Fatal("publication or revocation encountered PostgreSQL deadlock 40P01")
		}
	}
	f.complete(t, &b, result)
	if a.artifact.BlobID != b.artifact.BlobID {
		t.Fatal("outputs did not share blob")
	}
	if err := f.lifecycle.ReadAuthorized(ctx, f.auth(a), &bytes.Buffer{}); !errors.Is(err, processing.ErrDerivedUnauthorized) {
		t.Fatalf("revoked read: %v", err)
	}
	f.assertRead(t, b)
	f.assertSearch(t, a, false)
	f.assertSearch(t, b, true)
	var blob model.BackupAssetDerivedBlob
	if err := f.db.First(&blob, "id = ?", b.artifact.BlobID).Error; err != nil || blob.RefCount != 1 || blob.State != "active" {
		t.Fatalf("shared blob: %+v %v", blob, err)
	}
}

func sharedSearchCrossPoint(t *testing.T, f *sharedSearchFixture) {
	d1 := processingRuntimeWorkDescriptor()
	d2 := d1
	d2.Source.RecoveryPointID = strings.Repeat("3", 32)
	d2.CatalogGenerationID = strings.Repeat("4", 32)
	d2.Source.EntryID = strings.Repeat("5", 64)
	f.seedPoint(t, d1, strings.Repeat("1", 32), strings.Repeat("2", 32), []string{d1.Source.EntryID})
	f.seedPoint(t, d2, strings.Repeat("6", 32), strings.Repeat("7", 32), []string{d2.Source.EntryID})
	a, b := f.prepare(t, d1, "search-cross-a"), f.prepare(t, d2, "search-cross-b")
	for _, w := range []*sharedSearchWork{&a, &b} {
		result, err := f.publish(context.Background(), *w)
		if err != nil {
			t.Fatal(err)
		}
		f.complete(t, w, result)
		f.assertRead(t, *w)
		f.assertSearch(t, *w, true)
	}
	if a.set == b.set || a.artifact.ID == b.artifact.ID || a.artifact.BlobID != b.artifact.BlobID {
		t.Fatal("shared publication identities invalid")
	}
	var count int64
	if err := f.db.Model(&model.BackupAssetDerivedBlobReference{}).Where("blob_id = ? AND state = ?", a.artifact.BlobID, "active").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("active references=%d err=%v", count, err)
	}
	wrong := f.auth(b)
	wrong.ArtifactID = a.artifact.ID
	if err := f.lifecycle.ReadAuthorized(context.Background(), wrong, &bytes.Buffer{}); !errors.Is(err, processing.ErrDerivedUnauthorized) {
		t.Fatalf("cross-source read: %v", err)
	}
	var blob model.BackupAssetDerivedBlob
	if err := f.db.First(&blob, "id = ?", a.artifact.BlobID).Error; err != nil || blob.RefCount != 2 {
		t.Fatalf("shared blob: %+v %v", blob, err)
	}
	if err := f.db.Model(&model.BackupAssetDerivedBlob{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("physical blob rows=%d err=%v", count, err)
	}
	files, err := os.ReadDir(f.root)
	if err != nil || len(files) != 1 {
		t.Fatalf("ciphertext files=%d err=%v", len(files), err)
	}
	ciphertext, err := os.ReadFile(filepath.Join(f.root, blob.OpaqueLocator))
	if err != nil || bytes.Contains(ciphertext, f.payload) {
		t.Fatalf("ciphertext contains plaintext or is unreadable: %v", err)
	}
	for index, w := range []sharedSearchWork{a, b} {
		fence := f.revokeFence(t, w)
		if err := f.lifecycle.RevokeSetFenced(context.Background(), w.set, processing.DerivedRevokeExpired, fence.Fence); err != nil {
			t.Fatal(err)
		}
		if err := f.lease.Release(context.Background(), fence.Fence); err != nil {
			t.Fatal(err)
		}
		if err := f.lifecycle.ReadAuthorized(context.Background(), f.auth(w), &bytes.Buffer{}); !errors.Is(err, processing.ErrDerivedUnauthorized) {
			t.Fatalf("revoked read: %v", err)
		}
		f.assertSearch(t, w, false)
		if err := f.db.First(&blob, "id = ?", blob.ID).Error; err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if blob.State != "active" || blob.RefCount != 1 || len(blob.WrappedDEK) == 0 {
				t.Fatalf("survivor blob: %+v", blob)
			}
			f.assertRead(t, b)
			f.assertSearch(t, b, true)
		} else {
			if blob.State != "unavailable" || blob.RefCount != 0 || len(blob.WrappedDEK) != 0 {
				t.Fatalf("last reference retained key: %+v", blob)
			}
			if _, err := os.Stat(filepath.Join(f.root, blob.OpaqueLocator)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ciphertext remains: %v", err)
			}
		}
	}
}
