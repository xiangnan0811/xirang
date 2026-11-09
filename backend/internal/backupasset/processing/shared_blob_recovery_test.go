package processing

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
)

const sharedRecoveryPhase = "XIRANG_TEST_SHARED_RECOVERY_PHASE"
const sharedRecoveryDatabase = "XIRANG_TEST_SHARED_RECOVERY_DATABASE"
const sharedRecoveryEngine = "XIRANG_TEST_SHARED_RECOVERY_ENGINE"
const sharedRecoveryRoot = "XIRANG_TEST_SHARED_RECOVERY_ROOT"

func sharedRecoverySignal(t *testing.T, stage string) {
	t.Helper()
	if _, err := fmt.Fprintln(os.Stdout, stage); err != nil {
		t.Fatalf("write recovery stage %s: %v", stage, err)
	}
}

func TestProcessingSharedBlobRecoverySQLite(t *testing.T) {
	sharedRecoveryContract(t, "sqlite", "")
}

func TestProcessingSharedBlobRecoveryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	sharedRecoveryContract(t, "postgres", dsn)
}

func sharedRecoveryContract(t *testing.T, engine, dsn string) {
	t.Helper()
	t.Run("ReopenStagedAndPublished", func(t *testing.T) { sharedRecoveryScenario(t, engine, dsn, "ReopenStagedAndPublished") })
	t.Run("StaleStagingRename", func(t *testing.T) { sharedRecoveryScenario(t, engine, dsn, "StaleStagingRename") })
	t.Run("DelayedOldPurge", func(t *testing.T) { sharedRecoveryScenario(t, engine, dsn, "DelayedOldPurge") })
}

func sharedRecoveryScenario(t *testing.T, engine, dsn, scenario string) {
	t.Helper()
	if phase := os.Getenv(sharedRecoveryPhase); phase != "" {
		sharedRecoveryChild(t, phase, scenario)
		return
	}
	var fixture processingBehaviorFixture
	var connection string
	if engine == "sqlite" {
		configureProcessingBehaviorEnvironment(t)
		connection = filepath.Join(t.TempDir(), "recovery.sqlite")
		db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: connection})
		if err != nil {
			t.Fatal(err)
		}
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pool.Close() })
		if err := database.RunMigrations(db, "sqlite"); err != nil {
			t.Fatal(err)
		}
		fixture = prepareProcessingBehaviorFixture(t, db, engine)
	} else {
		fixture = openProcessingBehaviorPostgres(t, dsn)
		var schema string
		if err := fixture.db.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("invalid PostgreSQL URL")
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		query.Set("timezone", "UTC")
		parsed.RawQuery = query.Encode()
		connection = parsed.String()
	}
	second := validWorkDescriptor()
	second.Source.RecoveryPointID = strings.Repeat("1", 32)
	second.CatalogGenerationID = strings.Repeat("2", 32)
	second.Source.EntryID = strings.Repeat("3", 64)
	processingBehaviorSeedSecondPoint(t, fixture, second)
	root := filepath.Join(t.TempDir(), "derived")
	runSharedRecoveryProcess(t, "checkpoint", engine, connection, root)
	runSharedRecoveryProcess(t, "restart", engine, connection, root)
}

func runSharedRecoveryProcess(t *testing.T, phase, engine, connection, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+strings.ReplaceAll(t.Name(), "/", "$/^")+"$")
	command.Env = append(os.Environ(), sharedRecoveryPhase+"="+phase, sharedRecoveryEngine+"="+engine, sharedRecoveryDatabase+"="+connection, sharedRecoveryRoot+"="+root)
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal("open child output")
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal("open child input")
	}
	if err := command.Start(); err != nil {
		t.Fatal("start recovery child")
	}
	waited := false
	defer func() {
		_ = stdin.Close()
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	stage := "START"
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		switch scanner.Text() {
		case "CHILD_OPEN", "CHILD_READY", "PUBLISHED", "CHECKPOINT", "REOPENED", "EXPIRED", "REPUBLISHED", "FIRST_REVOKED", "GENERATION", "RESTART_OK":
			stage = scanner.Text()
		}
		if phase == "checkpoint" && stage == "CHECKPOINT" {
			if err := command.Process.Kill(); err != nil {
				t.Fatal("kill checkpoint child")
			}
			_ = stdin.Close()
			err := command.Wait()
			waited = true
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal("checkpoint child did not terminate by signal")
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("checkpoint child did not receive SIGKILL")
			}
			return
		}
	}
	err = command.Wait()
	waited = true
	if phase != "restart" || err != nil || stage != "RESTART_OK" || scanner.Err() != nil {
		t.Fatalf("%s child failed after stage %s", phase, stage)
	}
}

type sharedRecoveryRuntime struct {
	fixture   processingBehaviorFixture
	store     *DerivedStore
	lifecycle *DerivedLifecycle
	sink      *ArtifactSink
}

func sharedRecoveryChild(t *testing.T, phase, scenario string) {
	t.Helper()
	configureProcessingBehaviorEnvironment(t)
	engine, connection := os.Getenv(sharedRecoveryEngine), os.Getenv(sharedRecoveryDatabase)
	cfg := config.Config{DBType: engine}
	if engine == "sqlite" {
		cfg.SQLitePath = connection
	} else {
		cfg.PostgresDSN = connection
	}
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal("open child database")
	}
	sharedRecoverySignal(t, "CHILD_OPEN")
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	clock := &coordinatorClock{now: time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)}
	lease, err := backupasset.NewLeaseService(db, clock.Now, backupasset.LeaseConfig{Duration: 30 * time.Second, Heartbeat: 10 * time.Second, AbsoluteDeadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(db, lease, clock.Now, CoordinatorConfig{QueueMax: 100, InteractiveReservedSlots: 2, BackgroundSlots: 2, PullLease: 30 * time.Second, AttemptTimeout: 2 * time.Hour, RetryMax: 5, BackfillPolicySource: testBackfillPolicySource})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrantService(db, lease, clock.Now, GrantConfig{TTL: 30 * time.Second, InputLimits: GrantLimits{MaxRequests: 8, MaxBytesPerRequest: 64, MaxCumulativeBytes: 256, MaxInFlight: 2}, SinkLimits: GrantLimits{MaxRequests: 4, MaxBytesPerRequest: 64, MaxCumulativeBytes: 128, MaxInFlight: 2}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := processingBehaviorFixture{engine: engine, db: db, clock: clock, lease: lease, coordinator: coordinator, grants: grants, workerID: strings.Repeat("d", 32)}
	keyring := backupasset.NewKeyring(db, clock.Now)
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatal(err)
	}
	store, err := NewDerivedStore(context.Background(), db, keyring, DerivedStoreConfig{Root: os.Getenv(sharedRecoveryRoot), ChunkSize: 64 << 10, BlobMaxBytes: 1 << 20, GlobalMaxBytes: 8 << 20, ValidateRoot: func(context.Context, string) error { return nil }}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	projection := &processingBehaviorProjection{db: db}
	lifecycle, err := NewDerivedLifecycle(db, store, projection, clock.Now, lease)
	if err != nil {
		t.Fatal(err)
	}
	d := validWorkDescriptor()
	sink, err := NewArtifactSink(db, lease, grants, store, lifecycle, &manifestSourceRevalidator{}, func(context.Context) (string, error) { return d.SecurityPolicyRevision, nil }, func(context.Context, string, string) (string, error) { return d.PipelineFingerprint, nil }, clock.Now, ArtifactSinkConfig{MaxArtifacts: 4, MaxArtifactBytes: 1 << 20, MaxTotalBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	runtime := sharedRecoveryRuntime{fixture: fixture, store: store, lifecycle: lifecycle, sink: sink}
	sharedRecoverySignal(t, "CHILD_READY")
	if phase == "checkpoint" {
		first := validWorkDescriptor()
		second := first
		second.Source.RecoveryPointID = strings.Repeat("1", 32)
		second.CatalogGenerationID = strings.Repeat("2", 32)
		second.Source.EntryID = strings.Repeat("3", 64)
		sharedRecoveryUpload(t, runtime, first, "recovery-published", true, nil)
		sharedRecoverySignal(t, "PUBLISHED")
		sharedRecoveryUpload(t, runtime, second, "recovery-staged", false, nil)
		var set model.BackupAssetDerivedArtifactSet
		var artifact model.BackupAssetDerivedArtifact
		var upload model.BackupAssetProcessingUpload
		var blob model.BackupAssetDerivedBlob
		if err := db.Where("state = ?", "active").First(&set).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&artifact, "artifact_set_id = ?", set.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Where("state = ?", "staged").First(&upload).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&blob, "id = ?", artifact.BlobID).Error; err != nil {
			t.Fatal(err)
		}
		if upload.StagingID != blob.ID || blob.State != "active" || blob.RefCount != 1 {
			t.Fatal("checkpoint lacks published and staged holders")
		}
		sharedRecoverySignal(t, "CHECKPOINT")
		_, _ = io.Copy(io.Discard, os.Stdin)
		t.Fatal("checkpoint process was not killed")
	}
	if phase != "restart" {
		t.Fatal("invalid recovery phase")
	}
	sharedRecoveryRestart(t, runtime, scenario)
	sharedRecoverySignal(t, "RESTART_OK")
}

var sharedRecoveryPayload = []byte("shared-derived-output")

func sharedRecoveryUpload(t *testing.T, runtime sharedRecoveryRuntime, descriptor WorkDescriptorV1, owner string, publish bool, existing *LeasedAttempt) LeasedAttempt {
	t.Helper()
	ctx := context.Background()
	f := runtime.fixture
	var leased LeasedAttempt
	if existing == nil {
		work, err := f.coordinator.RequestWork(ctx, WorkRequest{Descriptor: descriptor, Interest: InterestRequest{OwnerKind: InterestSystem, OwnerKey: owner, PriorityClass: PriorityInteractive, Priority: 100}})
		if err != nil {
			t.Fatal(err)
		}
		leased, err = f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: f.workerID}, f.grants)
		if err != nil || leased.Lease.JobID != work.JobID {
			t.Fatalf("pull failed: %v", err)
		}
	} else {
		leased = *existing
	}
	if _, err := f.grants.Activate(ctx, ActivateGrantRequest{GrantID: leased.Grants.Sink.GrantID, Kind: GrantSink, JobID: leased.Lease.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, Secret: leased.Grants.Sink.Secret}); err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	if err := f.db.First(&job, "id = ?", leased.Lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	revision := job.TransitionRevision
	for _, state := range []ProcessingState{ProcessingFetching, ProcessingMaterializing, ProcessingProcessing, ProcessingUploading} {
		result, err := f.coordinator.TransitionAttempt(ctx, AttemptTransitionRequest{JobID: leased.Lease.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, ExpectedRevision: revision, To: state})
		if err != nil {
			t.Fatal(err)
		}
		revision = result.Revision
	}
	declaration := artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", sharedRecoveryPayload)
	if _, err := runtime.sink.UploadArtifact(ctx, UploadArtifactRequest{JobID: leased.Lease.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, GrantID: leased.Grants.Sink.GrantID, Artifact: declaration}, bytes.NewReader(sharedRecoveryPayload)); err != nil {
		t.Fatal(err)
	}
	if publish {
		if _, err := runtime.sink.CommitManifest(ctx, CommitManifestRequest{JobID: leased.Lease.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, GrantID: leased.Grants.Sink.GrantID, RecoveryPointFence: leased.Lease.RecoveryPointFence, SecurityPolicyRevision: descriptor.SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{declaration}}); err != nil {
			t.Fatal(err)
		}
	}
	return leased
}

func sharedRecoveryRestart(t *testing.T, runtime sharedRecoveryRuntime, scenario string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := runtime.fixture
	var published model.BackupAssetDerivedArtifactSet
	if err := f.db.Where("state = ?", "active").First(&published).Error; err != nil {
		t.Fatal(err)
	}
	var artifact model.BackupAssetDerivedArtifact
	if err := f.db.First(&artifact, "artifact_set_id = ?", published.ID).Error; err != nil {
		t.Fatal(err)
	}
	var staged model.BackupAssetProcessingUpload
	if err := f.db.First(&staged, "state = ?", "staged").Error; err != nil {
		t.Fatal(err)
	}
	if staged.StagingID != artifact.BlobID {
		t.Fatal("checkpoint did not share the blob")
	}
	var oldAttempt model.BackupAssetProcessingAttempt
	if err := f.db.First(&oldAttempt, "id = ?", staged.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	read := func(set model.BackupAssetDerivedArtifactSet, artifact model.BackupAssetDerivedArtifact) {
		var out bytes.Buffer
		auth := DerivedArtifactAuthorization{ArtifactID: artifact.ID, RecoveryPointID: set.RecoveryPointID, CatalogGenerationID: set.CatalogGenerationID, EntryID: set.EntryID, SourceFingerprint: set.SourceFingerprint}
		if err := runtime.lifecycle.ReadAuthorized(ctx, auth, &out); err != nil || !bytes.Equal(out.Bytes(), sharedRecoveryPayload) {
			t.Fatalf("reopened authorized read failed: %v", err)
		}
	}
	read(published, artifact)
	sharedRecoverySignal(t, "REOPENED")
	f.clock.Advance(31 * time.Second)
	reconciler, err := NewReconciler(f.coordinator, f.grants, f.clock.Now, ReconcilerConfig{BatchSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := reconciler.Reconcile(ctx); err != nil || result.ExpiredAttempts != 1 {
		t.Fatalf("expire: %+v %v", result, err)
	}
	declaration := artifactDeclaration(0, ArtifactRoleNoop, "application/octet-stream", sharedRecoveryPayload)
	if _, err := runtime.sink.UploadArtifact(ctx, UploadArtifactRequest{JobID: staged.JobID, AttemptID: staged.AttemptID, WorkerID: f.workerID, GrantID: staged.GrantID, Artifact: declaration}, bytes.NewReader(sharedRecoveryPayload)); !errors.Is(err, ErrGrantDenied) {
		t.Fatal("old grant was not denied")
	}
	if err := f.db.First(&staged, "id = ?", staged.ID).Error; err != nil || staged.State != "orphaned" {
		t.Fatal("expired upload was not orphaned")
	}
	sharedRecoverySignal(t, "EXPIRED")
	f.clock.Advance(6 * time.Second)
	if count, err := reconciler.PromoteRetries(ctx); err != nil || count != 1 {
		t.Fatalf("promote=%d: %v", count, err)
	}
	fresh, err := f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: f.workerID}, f.grants)
	if err != nil || fresh.Lease.JobID != staged.JobID || fresh.Lease.AttemptID == staged.AttemptID || hashFence(fresh.Lease.RecoveryPointFence.FenceToken) == oldAttempt.RecoveryPointFenceHash {
		t.Fatalf("fresh attempt failed: %v", err)
	}
	second := validWorkDescriptor()
	second.Source.RecoveryPointID = strings.Repeat("1", 32)
	second.CatalogGenerationID = strings.Repeat("2", 32)
	second.Source.EntryID = strings.Repeat("3", 64)
	sharedRecoveryUpload(t, runtime, second, "", true, &fresh)
	var secondSet model.BackupAssetDerivedArtifactSet
	if err := f.db.First(&secondSet, "job_id = ?", staged.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var secondArtifact model.BackupAssetDerivedArtifact
	if err := f.db.First(&secondArtifact, "artifact_set_id = ?", secondSet.ID).Error; err != nil {
		t.Fatal(err)
	}
	if secondArtifact.BlobID != artifact.BlobID {
		t.Fatal("fresh publication did not reuse shared blob")
	}
	read(published, artifact)
	read(secondSet, secondArtifact)
	sharedRecoverySignal(t, "REPUBLISHED")
	var oldBlob model.BackupAssetDerivedBlob
	if err := f.db.First(&oldBlob, "id = ?", artifact.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if oldBlob.State != "active" || oldBlob.RefCount != 2 {
		t.Fatal("two publications did not retain two references")
	}
	oldPath := filepath.Join(runtime.store.config.Root, oldBlob.OpaqueLocator)
	oldBytes, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	revoke := func(set model.BackupAssetDerivedArtifactSet) error {
		lease, err := f.lease.Acquire(ctx, backupasset.AcquireLeaseRequest{RecoveryPointID: set.RecoveryPointID, HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: set.JobID})
		if err != nil {
			return err
		}
		err = runtime.lifecycle.RevokeSetFenced(ctx, set.ID, DerivedRevokeExpired, lease.Fence)
		return errors.Join(err, f.lease.Release(ctx, lease.Fence))
	}
	if err := revoke(published); err != nil {
		t.Fatal(err)
	}
	var afterFirst model.BackupAssetDerivedBlob
	if err := f.db.First(&afterFirst, "id = ?", oldBlob.ID).Error; err != nil || afterFirst.State != "active" || afterFirst.RefCount != 1 {
		t.Fatal("first revoke damaged surviving holder")
	}
	read(secondSet, secondArtifact)
	sharedRecoverySignal(t, "FIRST_REVOKED")
	var observed model.BackupAssetDerivedBlob
	stagingPath := filepath.Join(runtime.store.config.Root, ".staging-"+strings.TrimSuffix(oldBlob.OpaqueLocator, ".xrd"))
	if scenario == "StaleStagingRename" {
		// Recreate the durable staged-install crash window, then read the actual
		// persisted row before any later incarnation can be installed.
		if err := os.Rename(oldPath, stagingPath); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state = ? AND opaque_locator = ?", oldBlob.ID, "active", oldBlob.OpaqueLocator).
			Updates(map[string]any{"state": "staged", "updated_at": f.clock.Now().Add(-time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.First(&observed, "id = ?", oldBlob.ID).Error; err != nil || observed.State != "staged" {
			t.Fatal("staged observation was not persisted")
		}
	}
	var releasePurge chan struct{}
	var purgeDone chan error
	if scenario == "DelayedOldPurge" {
		entered := make(chan struct{})
		releasePurge = make(chan struct{})
		purgeDone = make(chan error, 1)
		release := releasePurge
		remove := runtime.store.removeFile
		runtime.store.removeFile = func(path string) error {
			if path == oldPath {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return remove(path)
		}
		defer func() {
			if releasePurge != nil {
				close(releasePurge)
			}
			if purgeDone != nil {
				select {
				case <-purgeDone:
				case <-ctx.Done():
				}
			}
		}()
		done := purgeDone
		go func() { done <- revoke(secondSet) }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("old purge did not reach the unlink barrier")
		}
	} else if err := revoke(secondSet); err != nil {
		t.Fatal(err)
	}
	var unavailable model.BackupAssetDerivedBlob
	if err := f.db.First(&unavailable, "id = ?", oldBlob.ID).Error; err != nil || unavailable.State != "unavailable" || unavailable.RefCount != 0 || len(unavailable.WrappedDEK) != 0 {
		t.Fatal("last holder cleanup failed")
	}
	if scenario == "ReopenStagedAndPublished" {
		if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("old ciphertext not removed")
		}
		return
	}
	third := validWorkDescriptor()
	third.Parameters.Quality++
	sharedRecoveryUpload(t, runtime, third, "recovery-new-generation", true, nil)
	var current model.BackupAssetDerivedBlob
	if err := f.db.First(&current, "id = ?", oldBlob.ID).Error; err != nil || current.State != "active" || current.OpaqueLocator == oldBlob.OpaqueLocator {
		t.Fatal("reactivation failed")
	}
	sharedRecoverySignal(t, "GENERATION")
	derived, err := NewDerivedReconciler(runtime.store, runtime.lifecycle, 32)
	if err != nil {
		t.Fatal(err)
	}
	if scenario == "StaleStagingRename" {
		if recovered, err := derived.recoverStagedBlob(ctx, observed); err != nil || recovered {
			t.Fatal("stale staged recovery was not fenced")
		}
		var staleResult DerivedReconcileResult
		if err := derived.reconcileBlob(ctx, observed, &staleResult); err != nil || staleResult.RecoveredStaging != 0 {
			t.Fatal("stale staged reconciliation was not fenced")
		}
		remaining, err := os.ReadFile(stagingPath)
		if err != nil || !bytes.Equal(remaining, oldBytes) {
			t.Fatal("old staging inode changed")
		}
		if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("old staging file was renamed")
		}
	} else {
		close(releasePurge)
		releasePurge = nil
		select {
		case err := <-purgeDone:
			purgeDone = nil
			if err != nil {
				t.Fatal("old purge failed")
			}
		case <-ctx.Done():
			t.Fatal("old purge did not finish")
		}
		if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("old ciphertext not removed")
		}
		if err := derived.reconcileBlob(ctx, oldBlob, &DerivedReconcileResult{}); err != nil {
			t.Fatal(err)
		}
		if err := runtime.store.discardBlobIfUnreferencedObserved(ctx, oldBlob.ID, oldBlob.OpaqueLocator); err != nil {
			t.Fatal(err)
		}
	}
	var newSet model.BackupAssetDerivedArtifactSet
	if err := f.db.Where("state = ?", "active").First(&newSet).Error; err != nil {
		t.Fatal(err)
	}
	var newArtifact model.BackupAssetDerivedArtifact
	if err := f.db.First(&newArtifact, "artifact_set_id = ?", newSet.ID).Error; err != nil {
		t.Fatal(err)
	}
	read(newSet, newArtifact)
	if err := f.db.First(&current, "id = ?", oldBlob.ID).Error; err != nil || current.State != "active" || current.RefCount != 1 {
		t.Fatalf("old generation damaged new generation: %v", err)
	}
	lease, err := f.lease.Acquire(ctx, backupasset.AcquireLeaseRequest{RecoveryPointID: newSet.RecoveryPointID, HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: newSet.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.lifecycle.RevokeSetFenced(ctx, newSet.ID, DerivedRevokeExpired, lease.Fence); err != nil {
		t.Fatal(err)
	}
	if err := f.lease.Release(ctx, lease.Fence); err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&current, "id = ?", oldBlob.ID).Error; err != nil || current.State != "unavailable" || len(current.WrappedDEK) != 0 {
		t.Fatalf("new generation cleanup failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtime.store.config.Root, current.OpaqueLocator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new generation ciphertext not removed")
	}
}
