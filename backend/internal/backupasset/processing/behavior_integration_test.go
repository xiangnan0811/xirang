package processing

import (
	"bytes"
	"context"
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
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestProcessingBehaviorSQLite(t *testing.T) {
	runProcessingBehaviorContract(t, openProcessingBehaviorSQLite)
}

func TestProcessingBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_PROCESSING_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runProcessingBehaviorContract(t, func(t *testing.T) processingBehaviorFixture {
		return openProcessingBehaviorPostgres(t, dsn)
	})
}

type processingBehaviorFixture struct {
	engine      string
	db          *gorm.DB
	clock       *coordinatorClock
	lease       *backupasset.LeaseService
	coordinator *Coordinator
	grants      *GrantService
	workerID    string
}

func runProcessingBehaviorContract(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	t.Helper()

	t.Run("ConcurrentCoalescingAndFinalInterestCancellation", func(t *testing.T) {
		fixture := open(t)
		const callers = 8
		results := make(chan WorkResult, callers)
		errorsChannel := make(chan error, callers)
		var ready sync.WaitGroup
		ready.Add(callers)
		start := make(chan struct{})
		for index := 0; index < callers; index++ {
			index := index
			go func() {
				ready.Done()
				<-start
				result, err := fixture.coordinator.RequestWork(context.Background(), WorkRequest{
					Descriptor: validWorkDescriptor(),
					Interest: InterestRequest{
						OwnerKind: InterestWorkspace, OwnerKey: fmt.Sprintf("behavior-%d", index),
						PriorityClass: PriorityBackground, Priority: index,
					},
				})
				if err != nil {
					errorsChannel <- err
					return
				}
				results <- result
			}()
		}
		ready.Wait()
		close(start)
		jobID := ""
		for range callers {
			select {
			case err := <-errorsChannel:
				t.Fatalf("%s concurrent RequestWork: %v", fixture.engine, err)
			case result := <-results:
				if jobID == "" {
					jobID = result.JobID
				}
				if result.JobID != jobID {
					t.Fatalf("%s coalescing returned jobs %q and %q", fixture.engine, jobID, result.JobID)
				}
			}
		}
		var current int64
		if err := fixture.db.Model(&model.BackupAssetProcessingJob{}).Where("is_current = ?", true).Count(&current).Error; err != nil || current != 1 {
			t.Fatalf("%s current jobs=%d err=%v", fixture.engine, current, err)
		}
		for index := 0; index < callers; index++ {
			if err := fixture.coordinator.RemoveInterest(context.Background(), jobID, InterestWorkspace,
				fmt.Sprintf("behavior-%d", index), InterestRemovedCanceled); err != nil {
				t.Fatalf("%s RemoveInterest(%d): %v", fixture.engine, index, err)
			}
			var job model.BackupAssetProcessingJob
			if err := fixture.db.First(&job, "id = ?", jobID).Error; err != nil {
				t.Fatal(err)
			}
			if index < callers-1 && job.State != string(ProcessingQueued) {
				t.Fatalf("%s canceled before final interest: %+v", fixture.engine, job)
			}
			if index == callers-1 && job.State != string(ProcessingCancelRequested) {
				t.Fatalf("%s final interest did not cancel: %+v", fixture.engine, job)
			}
		}
	})

	t.Run("DualLeaseTakeoverAndOneUseActivation", func(t *testing.T) {
		fixture := open(t)
		work := processingBehaviorRequestWork(t, fixture, "lease-takeover")
		first, err := fixture.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: fixture.workerID}, fixture.grants)
		if err != nil || first.Lease.JobID != work.JobID {
			t.Fatalf("%s PullAttempt: %+v err=%v", fixture.engine, first, err)
		}
		activated, err := fixture.grants.Activate(context.Background(), ActivateGrantRequest{
			GrantID: first.Grants.Input.GrantID, Kind: GrantInput, JobID: work.JobID,
			AttemptID: first.Lease.AttemptID, WorkerID: fixture.workerID, Secret: first.Grants.Input.Secret,
		})
		if err != nil || activated.SessionID == "" {
			t.Fatalf("%s activate Input: %+v err=%v", fixture.engine, activated, err)
		}
		if _, err := fixture.grants.Activate(context.Background(), ActivateGrantRequest{
			GrantID: first.Grants.Input.GrantID, Kind: GrantInput, JobID: work.JobID,
			AttemptID: first.Lease.AttemptID, WorkerID: fixture.workerID, Secret: first.Grants.Input.Secret,
		}); !errors.Is(err, ErrGrantDenied) {
			t.Fatalf("%s reused activation error=%v", fixture.engine, err)
		}
		fixture.clock.Advance(10 * time.Second)
		heartbeat, err := fixture.coordinator.Heartbeat(context.Background(), HeartbeatRequest{
			AttemptID: first.Lease.AttemptID, WorkerID: fixture.workerID,
		})
		if err != nil || !heartbeat.WorkerLeaseExpiresAt.After(first.Lease.WorkerLeaseExpiresAt) ||
			!heartbeat.RecoveryPointLeaseExpiresAt.After(first.Lease.RecoveryPointLeaseExpiresAt) {
			t.Fatalf("%s dual heartbeat=%+v err=%v", fixture.engine, heartbeat, err)
		}
		fixture.clock.Advance(31 * time.Second)
		reconciler, err := NewReconciler(fixture.coordinator, fixture.grants, fixture.clock.Now, ReconcilerConfig{BatchSize: 100, RetryBase: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if result, err := reconciler.Reconcile(context.Background()); err != nil || result.ExpiredAttempts != 1 {
			t.Fatalf("%s reconcile=%+v err=%v", fixture.engine, result, err)
		}
		if err := fixture.lease.ValidateFence(context.Background(), first.Lease.RecoveryPointFence); !errors.Is(err, backupasset.ErrLeaseFenceLost) {
			t.Fatalf("%s old fence validation=%v", fixture.engine, err)
		}
		if _, err := fixture.grants.Reserve(context.Background(), ReserveGrantRequest{
			GrantID: first.Grants.Input.GrantID, Kind: GrantRequestStat,
		}); !errors.Is(err, ErrGrantDenied) {
			t.Fatalf("%s late grant reserve=%v", fixture.engine, err)
		}
		fixture.clock.Advance(2 * time.Second)
		if promoted, err := reconciler.PromoteRetries(context.Background()); err != nil || promoted != 1 {
			t.Fatalf("%s promote retries=%d err=%v", fixture.engine, promoted, err)
		}
		second, err := fixture.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: fixture.workerID}, fixture.grants)
		if err != nil || second.Lease.AttemptID == first.Lease.AttemptID ||
			second.Lease.RecoveryPointFence.FenceToken == first.Lease.RecoveryPointFence.FenceToken {
			t.Fatalf("%s takeover=%+v err=%v", fixture.engine, second, err)
		}
	})

	t.Run("AtomicGrantBudget", func(t *testing.T) {
		fixture := open(t)
		work := processingBehaviorRequestWork(t, fixture, "grant-budget")
		leased, err := fixture.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: fixture.workerID}, fixture.grants)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.grants.Activate(context.Background(), ActivateGrantRequest{
			GrantID: leased.Grants.Sink.GrantID, Kind: GrantSink, JobID: work.JobID,
			AttemptID: leased.Lease.AttemptID, WorkerID: fixture.workerID, Secret: leased.Grants.Sink.Secret,
		}); err != nil {
			t.Fatal(err)
		}
		const contenders = 6
		start := make(chan struct{})
		results := make(chan error, contenders)
		var ready sync.WaitGroup
		ready.Add(contenders)
		for range contenders {
			go func() {
				ready.Done()
				<-start
				_, err := fixture.grants.Reserve(context.Background(), ReserveGrantRequest{
					GrantID: leased.Grants.Sink.GrantID, Kind: GrantRequestUpload, Bytes: 64,
				})
				results <- err
			}()
		}
		ready.Wait()
		close(start)
		succeeded := 0
		for range contenders {
			err := <-results
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, ErrGrantBudgetExceeded):
			default:
				t.Fatalf("%s grant reservation error=%v", fixture.engine, err)
			}
		}
		if succeeded != 2 {
			t.Fatalf("%s grant budget admitted %d requests, want 2", fixture.engine, succeeded)
		}
	})

	t.Run("FencedManifestCryptoTamperAndSearchFirstRevoke", func(t *testing.T) {
		fixture := open(t)
		work := processingBehaviorRequestWork(t, fixture, "manifest")
		leased, err := fixture.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: fixture.workerID}, fixture.grants)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.grants.Activate(context.Background(), ActivateGrantRequest{
			GrantID: leased.Grants.Sink.GrantID, Kind: GrantSink, JobID: work.JobID,
			AttemptID: leased.Lease.AttemptID, WorkerID: fixture.workerID, Secret: leased.Grants.Sink.Secret,
		}); err != nil {
			t.Fatal(err)
		}
		processingBehaviorTransition(t, fixture, work.JobID, leased.Lease.AttemptID, ProcessingFetching, 2)
		processingBehaviorTransition(t, fixture, work.JobID, leased.Lease.AttemptID, ProcessingMaterializing, 3)
		processingBehaviorTransition(t, fixture, work.JobID, leased.Lease.AttemptID, ProcessingProcessing, 4)
		processingBehaviorTransition(t, fixture, work.JobID, leased.Lease.AttemptID, ProcessingUploading, 5)

		keyring := backupasset.NewKeyring(fixture.db, fixture.clock.Now)
		if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainDerivedStore); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(t.TempDir(), "derived")
		store, err := NewDerivedStore(context.Background(), fixture.db, keyring, DerivedStoreConfig{
			Root: root, ChunkSize: 64 * 1024, BlobMaxBytes: 1 << 20, GlobalMaxBytes: 8 << 20,
			ValidateRoot: func(context.Context, string) error { return nil },
		}, fixture.clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		projection := &processingBehaviorProjection{db: fixture.db}
		lifecycle, err := NewDerivedLifecycle(fixture.db, store, projection, fixture.clock.Now, fixture.lease)
		if err != nil {
			t.Fatal(err)
		}
		sink, err := NewArtifactSink(fixture.db, fixture.lease, fixture.grants, store, lifecycle,
			&manifestSourceRevalidator{}, func(context.Context) (string, error) {
				return validWorkDescriptor().SecurityPolicyRevision, nil
			}, func(context.Context, string, string) (string, error) {
				return validWorkDescriptor().PipelineFingerprint, nil
			}, fixture.clock.Now, ArtifactSinkConfig{MaxArtifacts: 4, MaxArtifactBytes: 1 << 20, MaxTotalBytes: 4 << 20})
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("behavior-derived-payload")
		declaration := artifactDeclaration(0, ArtifactRoleContent, "text/plain", payload)
		if _, err := sink.UploadArtifact(context.Background(), UploadArtifactRequest{
			JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: fixture.workerID,
			GrantID: leased.Grants.Sink.GrantID, Artifact: declaration,
		}, bytes.NewReader(payload)); err != nil {
			t.Fatalf("%s upload: %v", fixture.engine, err)
		}
		manifest, err := sink.CommitManifest(context.Background(), CommitManifestRequest{
			JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: fixture.workerID,
			GrantID: leased.Grants.Sink.GrantID, RecoveryPointFence: leased.Lease.RecoveryPointFence,
			SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration},
		})
		if err != nil || !manifest.ProjectionRequired || projection.publications != 1 {
			t.Fatalf("%s commit=%+v publications=%d err=%v", fixture.engine, manifest, projection.publications, err)
		}
		descriptor := validWorkDescriptor()
		capabilityService := &CapabilityService{
			db: fixture.db,
			activePipeline: func(context.Context, string, string) (string, error) {
				return descriptor.PipelineFingerprint, nil
			},
			securityPolicyRevision: func(context.Context) (string, error) {
				return descriptor.SecurityPolicyRevision, nil
			},
		}
		asset := content.AuthorizedAsset{
			Ref: descriptor.Source, CatalogGenerationID: descriptor.CatalogGenerationID,
			Provider: backupasset.ProviderRsync, ProviderCapabilityRevision: descriptor.ProviderCapabilityRevision,
			SourceFingerprint: descriptor.SourceFingerprint, EntryFingerprint: descriptor.EntryFingerprint,
			FingerprintStrength: "strong", Size: 1024, MediaType: "text/plain",
		}
		derived, found, err := capabilityService.activeDerived(context.Background(), asset, PreviewText, capabilityspec.Profile{
			Capability: descriptor.Capability, CapabilitySchema: descriptor.CapabilitySchema,
			OutputProfile: descriptor.OutputProfile,
		})
		if err != nil || !found || derived.State != ProcessingProductDerived || derived.Coverage != string(ArtifactComplete) {
			t.Fatalf("%s terminal publication reader=%+v found=%v err=%v", fixture.engine, derived, found, err)
		}
		var artifact model.BackupAssetDerivedArtifact
		if err := fixture.db.First(&artifact, "artifact_set_id = ?", manifest.ArtifactSetID).Error; err != nil {
			t.Fatal(err)
		}
		authorization := DerivedArtifactAuthorization{
			ArtifactID: artifact.ID, RecoveryPointID: validWorkDescriptor().Source.RecoveryPointID,
			CatalogGenerationID: validWorkDescriptor().CatalogGenerationID, EntryID: validWorkDescriptor().Source.EntryID,
			SourceFingerprint: validWorkDescriptor().SourceFingerprint,
		}
		var plaintext bytes.Buffer
		if err := lifecycle.ReadAuthorized(context.Background(), authorization, &plaintext); err != nil || !bytes.Equal(plaintext.Bytes(), payload) {
			t.Fatalf("%s Derived read bytes=%d err=%v", fixture.engine, plaintext.Len(), err)
		}
		var blob model.BackupAssetDerivedBlob
		if err := fixture.db.First(&blob, "id = ?", artifact.BlobID).Error; err != nil {
			t.Fatal(err)
		}
		cipherPath := filepath.Join(root, blob.OpaqueLocator)
		ciphertext, err := os.ReadFile(cipherPath)
		if err != nil || len(ciphertext) == 0 {
			t.Fatal(err)
		}
		ciphertext[len(ciphertext)-1] ^= 0x01
		if err := os.WriteFile(cipherPath, ciphertext, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := lifecycle.ReadAuthorized(context.Background(), authorization, &bytes.Buffer{}); !errors.Is(err, ErrDerivedTamper) {
			t.Fatalf("%s tamper read=%v", fixture.engine, err)
		}
		projection.onRevoke = func(tx *gorm.DB, request DerivedProjectionRevoke) error {
			var reference model.BackupAssetDerivedBlobReference
			if err := tx.First(&reference, "artifact_id = ?", artifact.ID).Error; err != nil {
				return err
			}
			var current model.BackupAssetDerivedBlob
			if err := tx.First(&current, "id = ?", artifact.BlobID).Error; err != nil {
				return err
			}
			if reference.State != "active" || current.State != "active" || len(current.WrappedDEK) == 0 {
				return errors.New("derived data changed before Search revoke")
			}
			return nil
		}
		if err := lifecycle.RevokeSetFenced(context.Background(), manifest.ArtifactSetID, DerivedRevokeExpired,
			leased.Lease.RecoveryPointFence); err != nil {
			t.Fatalf("%s revoke: %v", fixture.engine, err)
		}
		if projection.revocations != 1 {
			t.Fatalf("%s Search revocations=%d", fixture.engine, projection.revocations)
		}
		if _, found, err := capabilityService.activeDerived(context.Background(), asset, PreviewText, capabilityspec.Profile{
			Capability: descriptor.Capability, CapabilitySchema: descriptor.CapabilitySchema,
			OutputProfile: descriptor.OutputProfile,
		}); err != nil || found {
			t.Fatalf("%s revoked publication remained readable: found=%v err=%v", fixture.engine, found, err)
		}
		if err := fixture.db.First(&blob, "id = ?", artifact.BlobID).Error; err != nil || blob.State != "unavailable" || len(blob.WrappedDEK) != 0 {
			t.Fatalf("%s revoked blob=%+v err=%v", fixture.engine, blob, err)
		}
		if _, err := os.Stat(cipherPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s ciphertext survives revoke: %v", fixture.engine, err)
		}
	})

	t.Run("ContractFailureQuarantinesWorker", func(t *testing.T) {
		fixture := open(t)
		work := processingBehaviorRequestWork(t, fixture, "quarantine")
		leased, err := fixture.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: fixture.workerID}, fixture.grants)
		if err != nil {
			t.Fatal(err)
		}
		processingBehaviorTransition(t, fixture, work.JobID, leased.Lease.AttemptID, ProcessingFetching, 2)
		if _, err := fixture.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
			JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: fixture.workerID,
			ExpectedRevision: 3, To: ProcessingFailed, ErrorCode: ProcessingErrorInvalidOutput,
		}); err != nil {
			t.Fatalf("%s contract transition: %v", fixture.engine, err)
		}
		var worker model.BackupAssetWorkerIdentity
		if err := fixture.db.First(&worker, "id = ?", fixture.workerID).Error; err != nil {
			t.Fatal(err)
		}
		if worker.TrustState != "quarantined" || worker.HealthState != "draining" {
			t.Fatalf("%s Worker not quarantined: %+v", fixture.engine, worker)
		}
		var usable int64
		if err := fixture.db.Model(&model.BackupAssetProcessingGrant{}).
			Where("attempt_id = ? AND state IN ?", leased.Lease.AttemptID, []string{string(GrantIssued), string(GrantActive)}).Count(&usable).Error; err != nil || usable != 0 {
			t.Fatalf("%s usable grants=%d err=%v", fixture.engine, usable, err)
		}
	})
}

func openProcessingBehaviorSQLite(t *testing.T) processingBehaviorFixture {
	t.Helper()
	configureProcessingBehaviorEnvironment(t)
	path := filepath.Join(t.TempDir(), "processing-behavior.db")
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(16)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return prepareProcessingBehaviorFixture(t, db, "sqlite")
}

func openProcessingBehaviorPostgres(t *testing.T, dsn string) processingBehaviorFixture {
	t.Helper()
	configureProcessingBehaviorEnvironment(t)
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("open PostgreSQL behavior base: %v", err)
	}
	schema := fmt.Sprintf("xirang_processing_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "postgres"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		if sqlDB, dbErr := base.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return prepareProcessingBehaviorFixture(t, db, "postgres")
}

func configureProcessingBehaviorEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_PROCESSING_BEHAVIOR_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func prepareProcessingBehaviorFixture(t *testing.T, db *gorm.DB, engine string) processingBehaviorFixture {
	t.Helper()
	clock := &coordinatorClock{now: time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)}
	seedProcessingBehaviorParents(t, db, engine, clock.Now())
	lease, err := backupasset.NewLeaseService(db, clock.Now, backupasset.LeaseConfig{
		Duration: 30 * time.Second, Heartbeat: 10 * time.Second, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(db, lease, clock.Now, CoordinatorConfig{
		QueueMax: 100, InteractiveReservedSlots: 2, BackgroundSlots: 2,
		PullLease: 30 * time.Second, AttemptTimeout: 2 * time.Hour, RetryMax: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(db, lease, clock.Now, GrantConfig{
		TTL:         30 * time.Second,
		InputLimits: GrantLimits{MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2},
		SinkLimits:  GrantLimits{MaxRequests: 4, MaxBytesPerRequest: 64, MaxCumulativeBytes: 128, MaxInFlight: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	harness := &coordinatorHarness{db: db, clock: clock, coordinator: coordinator}
	workerID := harness.registerNoopWorker(t, "d")
	return processingBehaviorFixture{
		engine: engine, db: db, clock: clock, lease: lease, coordinator: coordinator, grants: grants, workerID: workerID,
	}
}

func seedProcessingBehaviorParents(t *testing.T, db *gorm.DB, engine string, now time.Time) {
	t.Helper()
	descriptor := validWorkDescriptor()
	repositoryID := strings.Repeat("e", 32)
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `INSERT INTO backup_repositories
				(id, provider_kind, repository_identity, display_name, description, version_mode,
				 status, capability_revision, capabilities_json, immutability_level, created_at, updated_at)
				VALUES (?, 'rsync', 'processing-behavior-repository', 'processing-behavior', '', 'hardlink_tree',
				 'online', ?, '{}', 'xirang_managed', ?, ?)`,
			args: []any{repositoryID, descriptor.ProviderCapabilityRevision, now, now},
		},
		{
			query: `INSERT INTO recovery_points
				(id, repository_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
				 lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, observed_at,
				 source_fingerprint, manifest_digest_algorithm, manifest_digest, entry_count, logical_bytes,
				 consistency_json, fidelity_json, capability_revision, capabilities_json, immutability_level,
				 physical_availability, hold_state, created_at, updated_at)
				VALUES (?, ?, '', 0, '', '{}', '', '', 'mutable_head', 'observed', ?, ?,
				 'sha256', '', 1, 1024, '{}', '{}', ?, '{}', 'mutable', 'online', 'none', ?, ?)`,
			args: []any{descriptor.Source.RecoveryPointID, repositoryID, now, descriptor.SourceFingerprint,
				descriptor.ProviderCapabilityRevision, now, now},
		},
		{
			query: `INSERT INTO catalog_generations
				(id, recovery_point_id, generation, state, is_active, source_fingerprint,
				 expected_entry_count, written_entry_count, expected_digest, written_digest,
				 error_code, correlation_id, started_at, finished_at, created_at, updated_at)
				VALUES (?, ?, 1, 'complete', ?, ?, 1, 1, '', '', '', '', ?, ?, ?, ?)`,
			args: []any{descriptor.CatalogGenerationID, descriptor.Source.RecoveryPointID, true,
				descriptor.SourceFingerprint, now, now, now, now},
		},
		{
			query: `INSERT INTO catalog_entries
				(generation_id, entry_id, recovery_point_id, normalized_path, name, entry_type,
				 size, mode, owner, mime_type, fingerprint, fingerprint_strength,
				 encrypted_provider_locator, security_state, created_at)
				VALUES (?, ?, ?, '/processing/asset.bin', 'asset.bin', 'file', 1024, '', '',
				 'application/octet-stream', ?, 'strong', '', 'non_secret', ?)`,
			args: []any{descriptor.CatalogGenerationID, descriptor.Source.EntryID,
				descriptor.Source.RecoveryPointID, descriptor.EntryFingerprint, now},
		},
	}
	for _, statement := range statements {
		if err := db.Exec(statement.query, statement.args...).Error; err != nil {
			t.Fatalf("seed %s Processing behavior parent: %v", engine, err)
		}
	}
}

func processingBehaviorRequestWork(t *testing.T, fixture processingBehaviorFixture, owner string) WorkResult {
	t.Helper()
	result, err := fixture.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest: InterestRequest{
			OwnerKind: InterestSystem, OwnerKey: owner, PriorityClass: PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatalf("%s RequestWork: %v", fixture.engine, err)
	}
	return result
}

func processingBehaviorTransition(t *testing.T, fixture processingBehaviorFixture, jobID, attemptID string, to ProcessingState, revision int64) {
	t.Helper()
	if _, err := fixture.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{
		JobID: jobID, AttemptID: attemptID, WorkerID: fixture.workerID, ExpectedRevision: revision, To: to,
	}); err != nil {
		t.Fatalf("%s transition to %s: %v", fixture.engine, to, err)
	}
}

type processingBehaviorProjection struct {
	db           *gorm.DB
	publications int
	revocations  int
	onRevoke     func(*gorm.DB, DerivedProjectionRevoke) error
}

type processingBehaviorPreparedPublish struct {
	projection *processingBehaviorProjection
	request    DerivedProjectionPublish
}

type processingBehaviorPreparedRevoke struct {
	projection *processingBehaviorProjection
	request    DerivedProjectionRevoke
}

func (projection *processingBehaviorProjection) PreparePublish(_ context.Context, request DerivedProjectionPublish) (PreparedDerivedProjection, error) {
	return &processingBehaviorPreparedPublish{projection: projection, request: request}, nil
}

func (prepared *processingBehaviorPreparedPublish) PublishTx(_ context.Context, _ *gorm.DB) (DerivedProjectionPublication, error) {
	projection := prepared.projection
	request := prepared.request
	projection.publications++
	return DerivedProjectionPublication{ArtifactSetID: request.ArtifactSetID, Revision: int64(projection.publications)}, nil
}

func (projection *processingBehaviorProjection) PrepareRevoke(_ context.Context, request DerivedProjectionRevoke) (PreparedDerivedRevocation, error) {
	return &processingBehaviorPreparedRevoke{projection: projection, request: request}, nil
}

func (prepared *processingBehaviorPreparedRevoke) RevokeTx(_ context.Context, tx *gorm.DB) error {
	projection := prepared.projection
	projection.revocations++
	if projection.onRevoke != nil {
		return projection.onRevoke(tx, prepared.request)
	}
	return nil
}
