package processing

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"gorm.io/gorm"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

// Each case creates real attempts and grants, then proves the next job can
// acquire the same provider and capability bucket after authority is retired.
func quotaLifecycleRelease(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	t.Run("Success", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "Success") })
	t.Run("FailureRetry", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "FailureRetry") })
	t.Run("TerminalFailure", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "TerminalFailure") })
	t.Run("CancelRequested", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "CancelRequested") })
	t.Run("TerminalCancellation", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "TerminalCancellation") })
	t.Run("ExpiryBeforeReconcile", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "ExpiryBeforeReconcile") })
	t.Run("Takeover", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "Takeover") })
	t.Run("Supersede", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "Supersede") })
	t.Run("SourceInvalidation", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "SourceInvalidation") })
	t.Run("Shutdown", func(t *testing.T) { quotaLifecycleReleaseCase(t, open, "Shutdown") })
}

func quotaLifecycleReleaseCase(t *testing.T, open func(*testing.T) processingBehaviorFixture, scenario string) {
	f, p := quotaBehaviorFixture(t, open)
	p.change(func(v *BackfillPolicy) { v.ProviderConcurrency = 1; v.CapabilityConcurrency = 1 })
	second := quotaSecondPoint(t, f)
	first := quotaRequireWork(t, f, validWorkDescriptor(), "lifecycle-first", PriorityBackground)
	leased, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
	if err != nil || leased.Lease.JobID != first.JobID || leased.Grants.Input.GrantID == "" || leased.Grants.Sink.GrantID == "" {
		t.Fatalf("first pull=%+v err=%v", leased, err)
	}
	next, err := f.coordinator.RequestWork(context.Background(), WorkRequest{Descriptor: second, Interest: InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "lifecycle-next", PriorityClass: PriorityBackground, Priority: 1}})
	if err != nil {
		t.Fatal(err)
	}
	h := &coordinatorHarness{db: f.db, clock: f.clock, coordinator: f.coordinator}
	worker2 := h.registerNoopWorker(t, "c")
	if _, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: worker2}, f.grants); !errors.Is(err, ErrNoWork) {
		t.Fatalf("occupied bucket: %v", err)
	}
	ctx := context.Background()
	switch scenario {
	case "Success":
		s := sharedAuthorityFixture(t, f)
		if _, err := f.grants.Activate(ctx, ActivateGrantRequest{GrantID: leased.Grants.Sink.GrantID, Kind: GrantSink, JobID: first.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, Secret: leased.Grants.Sink.Secret}); err != nil {
			t.Fatal(err)
		}
		for n, state := range []ProcessingState{ProcessingFetching, ProcessingMaterializing, ProcessingProcessing, ProcessingUploading} {
			processingBehaviorTransition(t, f, first.JobID, leased.Lease.AttemptID, state, int64(n+2))
		}
		if _, err := s.sink.UploadArtifact(ctx, UploadArtifactRequest{JobID: first.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, GrantID: leased.Grants.Sink.GrantID, Artifact: s.declaration}, bytes.NewReader(s.payload)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.sink.CommitManifest(ctx, CommitManifestRequest{JobID: first.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, GrantID: leased.Grants.Sink.GrantID, RecoveryPointFence: leased.Lease.RecoveryPointFence, SecurityPolicyRevision: validWorkDescriptor().SecurityPolicyRevision, Artifacts: []ArtifactDeclaration{s.declaration}}); err != nil {
			t.Fatal(err)
		}
	case "FailureRetry":
		retryAt := f.clock.Now().Add(time.Minute)
		if _, err := f.coordinator.TransitionAttempt(ctx, AttemptTransitionRequest{JobID: first.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, ExpectedRevision: 2, To: ProcessingRetryWait, ErrorCode: ProcessingErrorWorkerUnavailable, RetryAt: &retryAt}); err != nil {
			t.Fatal(err)
		}
	case "TerminalFailure":
		if _, err := f.coordinator.TransitionAttempt(ctx, AttemptTransitionRequest{JobID: first.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, ExpectedRevision: 2, To: ProcessingFailed, ErrorCode: ProcessingErrorUnsupportedFormat}); err != nil {
			t.Fatal(err)
		}
	case "CancelRequested":
		if err := f.coordinator.RemoveInterest(ctx, first.JobID, InterestWorkspace, "lifecycle-first", InterestRemovedCanceled); err != nil {
			t.Fatal(err)
		}
		var job model.BackupAssetProcessingJob
		if err := f.db.First(&job, "id = ?", first.JobID).Error; err != nil || job.State != string(ProcessingCancelRequested) || !job.IsCurrent {
			t.Fatalf("cancel requested=%+v err=%v", job, err)
		}
	case "TerminalCancellation":
		if err := f.coordinator.RemoveInterest(ctx, first.JobID, InterestWorkspace, "lifecycle-first", InterestRemovedCanceled); err != nil {
			t.Fatal(err)
		}
		var job model.BackupAssetProcessingJob
		if err := f.db.First(&job, "id = ?", first.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := f.coordinator.TransitionAttempt(ctx, AttemptTransitionRequest{JobID: first.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID, ExpectedRevision: job.TransitionRevision, To: ProcessingCanceled, CancelReason: CancelReasonInterestWithdrawn}); err != nil {
			t.Fatal(err)
		}
		if err := f.db.First(&job, "id = ?", first.JobID).Error; err != nil || job.IsCurrent || job.State != string(ProcessingCanceled) {
			t.Fatalf("terminal canceled=%+v err=%v", job, err)
		}
	case "ExpiryBeforeReconcile":
		f.clock.Advance(31 * time.Second)
		var attempt model.BackupAssetProcessingAttempt
		if err := f.db.First(&attempt, "id = ?", leased.Lease.AttemptID).Error; err != nil || !attempt.IsCurrent || attempt.State != "active" {
			t.Fatalf("expiry before reconciliation=%+v err=%v", attempt, err)
		}
	case "Takeover":
		f.clock.Advance(31 * time.Second)
		r, err := NewReconciler(f.coordinator, f.grants, f.clock.Now, ReconcilerConfig{BatchSize: 100, RetryBase: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		result, err := r.Reconcile(ctx)
		if err != nil || result.ExpiredAttempts != 1 {
			t.Fatalf("reconcile=%+v err=%v", result, err)
		}
		f.clock.Advance(6 * time.Second)
		if n, err := r.PromoteRetries(ctx); err != nil || n != 1 {
			t.Fatalf("promote=%d err=%v", n, err)
		}
		taken, err := f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: worker2}, f.grants)
		if err != nil || taken.Lease.JobID != first.JobID || taken.Lease.AttemptID == leased.Lease.AttemptID || taken.Lease.RecoveryPointFence.FenceToken == leased.Lease.RecoveryPointFence.FenceToken {
			t.Fatalf("takeover=%+v err=%v", taken, err)
		}
		if err := f.lease.ValidateFence(ctx, leased.Lease.RecoveryPointFence); err == nil {
			t.Fatal("old fence remained valid")
		}
		retryAt := f.clock.Now().Add(time.Minute)
		var current model.BackupAssetProcessingJob
		if err := f.db.First(&current, "id = ?", first.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := f.coordinator.TransitionAttempt(ctx, AttemptTransitionRequest{JobID: first.JobID, AttemptID: taken.Lease.AttemptID, WorkerID: worker2, ExpectedRevision: current.TransitionRevision, To: ProcessingRetryWait, ErrorCode: ProcessingErrorWorkerUnavailable, RetryAt: &retryAt}); err != nil {
			t.Fatal(err)
		}
	case "Supersede":
		s := sharedAuthorityFixture(t, f)
		controller, err := NewInvalidationController(f.db, f.coordinator, s.lifecycle, f.clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		result, err := controller.Invalidate(ctx, InvalidationRequest{Targets: []InvalidationTarget{{Capability: "noop", OutputProfile: "noop.v1", ActivePipelineFingerprint: "pipeline-fingerprint-v2"}}, BatchSize: 32, RequeuePriority: 950})
		if err != nil || result.SupersededJobs != 2 {
			t.Fatalf("invalidate=%+v err=%v", result, err)
		}
		next = quotaRequireWork(t, f, second, "after-supersede", PriorityBackground)
	case "SourceInvalidation":
		s := sharedAuthorityFixture(t, f)
		id := strings.Repeat("a", 32)
		if err := f.db.Create(&model.RecoveryPointLifecycleAttempt{ID: id, RecoveryPointID: validWorkDescriptor().Source.RecoveryPointID, Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking)}).Error; err != nil {
			t.Fatal(err)
		}
		owner, err := NewSourceLifecycle(f.db, s.lifecycle, &processingSearchProofSpy{}, f.clock.Now, 32)
		if err != nil {
			t.Fatal(err)
		}
		if err := owner.RevokeRecoveryPoint(ctx, backupasset.SourceLifecycleRequest{RecoveryPointID: validWorkDescriptor().Source.RecoveryPointID, LifecycleAttemptID: id, Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare}); err != nil {
			t.Fatal(err)
		}
	case "Shutdown":
		r, err := NewReconciler(f.coordinator, f.grants, f.clock.Now, ReconcilerConfig{BatchSize: 100})
		if err != nil {
			t.Fatal(err)
		}
		if n, err := r.Shutdown(ctx); err != nil || n != 1 {
			t.Fatalf("shutdown=%d err=%v", n, err)
		}
		// Shutdown drains registered workers; register a fresh consumer afterward.
		worker2 = h.registerNoopWorker(t, "b")
	}
	got, err := f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: worker2}, f.grants)
	if err != nil || got.Lease.JobID != next.JobID || got.Grants.Input.GrantID == "" || got.Grants.Sink.GrantID == "" {
		t.Fatalf("consumer pull after %s=%+v err=%v", scenario, got, err)
	}
}

type quotaRaceContextKey struct{}

func quotaInvalidationBatchAtomicity(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	t.Run("TwoPointPrelockAndPartialDenial", func(t *testing.T) {
		f, p := quotaBehaviorFixture(t, open)
		low := quotaSecondPoint(t, f)
		high := validWorkDescriptor()
		quotaSize(t, f, low, 32)
		quotaSize(t, f, high, 32)
		oldLow := quotaRequireWork(t, f, low, "old-low", PriorityBackground)
		oldHigh := quotaRequireWork(t, f, high, "old-high", PriorityBackground)
		const pipeline = "pipeline-fingerprint-v2"
		var capability model.BackupAssetWorkerCapability
		if err := f.db.First(&capability, "worker_id = ?", f.workerID).Error; err != nil {
			t.Fatal(err)
		}
		capability.ID = strings.Repeat("9", 32)
		capability.PipelineFingerprint = pipeline
		if err := f.db.Create(&capability).Error; err != nil {
			t.Fatal(err)
		}
		s := sharedAuthorityFixture(t, f)
		controller, err := NewInvalidationController(f.db, f.coordinator, s.lifecycle, f.clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		request := InvalidationRequest{Targets: []InvalidationTarget{{Capability: high.Capability, OutputProfile: high.OutputProfile, ActivePipelineFingerprint: pipeline}}, BatchSize: 32, RequeuePriority: 950}
		expectedJobs, expectedBytes := int64(3), int64(96)
		var result InvalidationResult
		if f.engine == "postgres" {
			p.change(func(v *BackfillPolicy) { v.JobsPerHour = 4 })
			reached, release := make(chan int, 1), make(chan struct{})
			f.coordinator.config.BackfillPolicySource = func(c context.Context, tx *gorm.DB) (BackfillPolicy, error) {
				if c.Value(quotaRaceContextKey{}) != nil {
					var pid int
					if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
						return BackfillPolicy{}, err
					}
					reached <- pid
					select {
					case <-release:
					case <-c.Done():
						return BackfillPolicy{}, c.Err()
					}
				}
				return p.read(c, tx)
			}
			concurrent := quotaDifferentDescriptor(high)
			concurrent.PipelineFingerprint = pipeline
			workResult := make(chan error, 1)
			go func() {
				_, err := quotaRequest(context.WithValue(ctx, quotaRaceContextKey{}, true), f, concurrent, "independent-high", PriorityBackground)
				workResult <- err
			}()
			var holder int
			select {
			case holder = <-reached:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			invalidated := make(chan struct {
				result InvalidationResult
				err    error
			}, 1)
			go func() {
				r, err := controller.Invalidate(ctx, request)
				invalidated <- struct {
					result InvalidationResult
					err    error
				}{r, err}
			}()
			db, err := f.db.DB()
			if err != nil {
				t.Fatal(err)
			}
			quotaWaitAnyPGBlocked(t, ctx, db, holder)
			close(release)
			select {
			case err := <-workResult:
				if err != nil {
					t.Fatalf("independent RequestWork: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case outcome := <-invalidated:
				result, err = outcome.result, outcome.err
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err != nil {
				t.Fatalf("invalidation: %v", err)
			}
			expectedJobs, expectedBytes = 4, 128
		} else {
			p.change(func(v *BackfillPolicy) { v.JobsPerHour = 3 })
			result, err = controller.Invalidate(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
		}
		if result.SupersededJobs != 2 || result.RequeuedJobs != 1 || result.NotDeployed != 0 {
			t.Fatalf("invalidation=%+v", result)
		}
		for _, id := range []string{oldLow.JobID, oldHigh.JobID} {
			var job model.BackupAssetProcessingJob
			if err := f.db.First(&job, "id = ?", id).Error; err != nil || job.IsCurrent || job.State != string(ProcessingSuperseded) {
				t.Fatalf("old invalidation=%+v err=%v", job, err)
			}
		}
		var replacementJobs, replacementInterests int64
		if err := f.db.Model(&model.BackupAssetProcessingJob{}).Where("pipeline_fingerprint = ? AND priority_class = ?", pipeline, string(PriorityBackground)).Count(&replacementJobs).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&model.BackupAssetProcessingInterest{}).Where("owner_kind = ? AND owner_key = ?", InterestSystem, invalidationOwnerKey(request.Targets[0])).Count(&replacementInterests).Error; err != nil {
			t.Fatal(err)
		}
		expectedReplacementJobs := int64(1)
		if f.engine == "postgres" {
			expectedReplacementJobs++
		}
		if replacementJobs != expectedReplacementJobs || replacementInterests != 1 {
			t.Fatalf("replacement jobs=%d interests=%d", replacementJobs, replacementInterests)
		}
		low.PipelineFingerprint, high.PipelineFingerprint = pipeline, pipeline
		var accepted int
		for _, descriptor := range []WorkDescriptorV1{low, high} {
			key, err := ComputeWorkKey(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			var jobs []model.BackupAssetProcessingJob
			if err := f.db.Where("work_key = ?", key).Find(&jobs).Error; err != nil {
				t.Fatal(err)
			}
			if len(jobs) > 1 {
				t.Fatalf("duplicate replacement jobs: %d", len(jobs))
			}
			if len(jobs) == 1 {
				accepted++
				var interests int64
				if !jobs[0].BackfillCharged {
					t.Fatal("replacement was not charged")
				}
				if err := f.db.Model(&model.BackupAssetProcessingInterest{}).Where("job_id = ? AND owner_kind = ?", jobs[0].ID, InterestSystem).Count(&interests).Error; err != nil || interests != 1 {
					t.Fatalf("replacement interests=%d err=%v", interests, err)
				}
			}
		}
		if accepted != 1 {
			t.Fatalf("replacement keys with a job=%d", accepted)
		}
		quotaLedger(t, f, expectedJobs, expectedBytes)
	})
}

func quotaAdmissionSetVersionRaces(t *testing.T, dsn string) {
	open := func(t *testing.T) processingBehaviorFixture { return openProcessingBehaviorPostgres(t, dsn) }
	t.Run("AdmissionFirst", func(t *testing.T) {
		f, _ := quotaBehaviorFixture(t, open)
		quotaSize(t, f, validWorkDescriptor(), 32)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		db, err := f.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
		if err != nil {
			t.Fatal(err)
		}
		reached, release := make(chan int, 1), make(chan struct{})
		const callback = "test:quota-admission-before-interest"
		if err := f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table != "backup_asset_processing_interests" || tx.Statement.Context.Value(quotaRaceContextKey{}) == nil {
				return
			}
			var pid int
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				tx.AddError(err)
				return
			}
			var count struct {
				Jobs    int64
				Bytes   int64
				Charged int64
			}
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT l.jobs_count AS jobs, l.bytes_count AS bytes, (SELECT count(*) FROM backup_asset_processing_jobs WHERE backfill_charged) AS charged FROM backup_asset_processing_backfill_ledger l WHERE l.id = 1").Scan(&count).Error; err != nil {
				tx.AddError(err)
				return
			}
			if count.Jobs != 1 || count.Bytes != 32 || count.Charged != 1 {
				tx.AddError(errors.New("uncommitted charge was not present at migration barrier"))
				return
			}
			reached <- pid
			select {
			case <-release:
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.db.Callback().Create().Remove(callback) })
		admitted := make(chan error, 1)
		go func() {
			_, err := quotaRequest(context.WithValue(ctx, quotaRaceContextKey{}, true), f, validWorkDescriptor(), "admission-first", PriorityBackground)
			admitted <- err
		}()
		var holder int
		select {
		case holder = <-reached:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		down := make(chan error, 1)
		go func() { down <- driver.SetVersion(98, true) }()
		quotaWaitAnyPGBlocked(t, ctx, db, holder)
		close(release)
		select {
		case err := <-admitted:
			if err != nil {
				t.Fatalf("admission: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case err := <-down:
			if err == nil || !strings.Contains(err.Error(), "000099 downgrade blocked: backfill quota used") {
				t.Fatalf("SetVersion guard error=%v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		var version int
		var dirty bool
		if err := db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != 99 || dirty {
			t.Fatalf("metadata=%d/%t err=%v", version, dirty, err)
		}
		quotaLedger(t, f, 1, 32)
		var jobs, interests int64
		if err := f.db.Model(&model.BackupAssetProcessingJob{}).Where("backfill_charged = ?", true).Count(&jobs).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&model.BackupAssetProcessingInterest{}).Count(&interests).Error; err != nil || jobs != 1 || interests != 1 {
			t.Fatalf("charged jobs=%d interests=%d err=%v", jobs, interests, err)
		}
	})
	t.Run("DowngradeFirst", func(t *testing.T) {
		f, _ := quotaBehaviorFixture(t, open)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		db, err := f.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		barrier, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		defer barrier.Rollback()
		var blocker int
		if err := barrier.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
			t.Fatal(err)
		}
		if _, err := barrier.ExecContext(ctx, "SELECT id FROM backup_asset_processing_backfill_ledger WHERE id = 1 FOR UPDATE"); err != nil {
			t.Fatal(err)
		}
		down := make(chan error, 1)
		go func() { down <- driver.SetVersion(98, true) }()
		migrator := quotaLifecycleWaitBlockedPID(t, ctx, db, blocker)
		admitted := make(chan error, 1)
		go func() {
			_, err := quotaRequest(ctx, f, validWorkDescriptor(), "downgrade-first", PriorityBackground)
			admitted <- err
		}()
		quotaWaitAnyPGBlocked(t, ctx, db, migrator)
		if err := barrier.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-down:
			if err != nil {
				t.Fatalf("SetVersion: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case err := <-admitted:
			if err == nil || errors.Is(err, ErrBackfillDenied) {
				t.Fatalf("admission after dirty downgrade=%v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		var version int
		var dirty bool
		if err := db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != 98 || !dirty {
			t.Fatalf("metadata=%d/%t err=%v", version, dirty, err)
		}
		var jobs, interests int64
		if err := f.db.Model(&model.BackupAssetProcessingJob{}).Count(&jobs).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&model.BackupAssetProcessingInterest{}).Count(&interests).Error; err != nil || jobs != 0 || interests != 0 {
			t.Fatalf("downgrade left jobs=%d interests=%d err=%v", jobs, interests, err)
		}
		quotaLedger(t, f, 0, 0)
		if err := driver.SetVersion(99, false); err != nil {
			t.Fatalf("restore disposable metadata: %v", err)
		}
	})
}

func quotaLifecycleWaitBlockedPID(t *testing.T, ctx context.Context, db *sql.DB, blocker int) int {
	t.Helper()
	for {
		var pid int
		err := db.QueryRowContext(ctx, "SELECT pid FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)) ORDER BY pid LIMIT 1", blocker).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("lock probe: %v", err)
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
}
