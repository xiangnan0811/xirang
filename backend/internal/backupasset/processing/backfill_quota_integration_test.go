package processing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/model"
)

type quotaPolicyFixture struct {
	mu     sync.Mutex
	policy BackfillPolicy
	err    error
}

func (p *quotaPolicyFixture) read(_ context.Context, _ *gorm.DB) (BackfillPolicy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy, p.err
}

func (p *quotaPolicyFixture) change(fn func(*BackfillPolicy)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(&p.policy)
}

func quotaBehaviorFixture(t *testing.T, open func(*testing.T) processingBehaviorFixture) (processingBehaviorFixture, *quotaPolicyFixture) {
	t.Helper()
	f := open(t)
	p := &quotaPolicyFixture{policy: BackfillPolicy{BatchSize: 100, JobsPerHour: 100, BytesPerHour: 1 << 20, ProviderConcurrency: 32, CapabilityConcurrency: 32, RecentWindow: 720 * time.Hour, HistoryAgingStep: 24 * time.Hour}}
	c, err := NewCoordinator(f.db, f.lease, f.clock.Now, CoordinatorConfig{
		QueueMax: 100, InteractiveReservedSlots: 2, BackgroundSlots: 2,
		PullLease: 30 * time.Second, AttemptTimeout: 2 * time.Hour, RetryMax: 5,
		BackfillPolicySource: p.read,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.coordinator = c
	return f, p
}

func quotaRequest(ctx context.Context, f processingBehaviorFixture, d WorkDescriptorV1, owner string, priority PriorityClass) (WorkResult, error) {
	return f.coordinator.RequestWork(ctx, WorkRequest{Descriptor: d, Interest: InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: owner, PriorityClass: priority, Priority: 100}})
}

func quotaSize(t *testing.T, f processingBehaviorFixture, d WorkDescriptorV1, size int64) {
	t.Helper()
	if err := f.db.Exec("UPDATE catalog_entries SET size = ? WHERE generation_id = ? AND entry_id = ? AND recovery_point_id = ?", size, d.CatalogGenerationID, d.Source.EntryID, d.Source.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
}

func quotaLedger(t *testing.T, f processingBehaviorFixture, jobs, bytes int64) model.BackupAssetProcessingBackfillLedger {
	t.Helper()
	var ledger model.BackupAssetProcessingBackfillLedger
	if err := f.db.First(&ledger, "id = ?", 1).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.JobsCount != jobs || ledger.BytesCount != bytes {
		t.Fatalf("ledger jobs=%d bytes=%d; want %d/%d", ledger.JobsCount, ledger.BytesCount, jobs, bytes)
	}
	return ledger
}

func quotaRequireWork(t *testing.T, f processingBehaviorFixture, d WorkDescriptorV1, owner string, priority PriorityClass) WorkResult {
	t.Helper()
	result, err := quotaRequest(context.Background(), f, d, owner, priority)
	if err != nil {
		t.Fatalf("RequestWork(%s): %v", owner, err)
	}
	return result
}

func quotaDifferentDescriptor(d WorkDescriptorV1) WorkDescriptorV1 { d.Parameters.Quality++; return d }

func TestProcessingBackfillQuotaSQLite(t *testing.T) {
	runProcessingBackfillQuota(t, openProcessingBehaviorSQLite)
}

func TestProcessingBackfillQuotaPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runProcessingBackfillQuota(t, func(t *testing.T) processingBehaviorFixture { return openProcessingBehaviorPostgres(t, dsn) })
}

func runProcessingBackfillQuota(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	t.Helper()
	t.Run("HourlyCreationAndCoalescing", func(t *testing.T) {
		f, p := quotaBehaviorFixture(t, open)
		d := validWorkDescriptor()
		quotaSize(t, f, d, 32)
		p.change(func(v *BackfillPolicy) { v.JobsPerHour = 2; v.BytesPerHour = 64 })
		first := quotaRequireWork(t, f, d, "first", PriorityBackground)
		if !first.Created {
			t.Fatal("first job was not created")
		}
		second := quotaRequireWork(t, f, d, "second", PriorityBackground)
		if second.Created || second.JobID != first.JobID || second.InterestID == first.InterestID {
			t.Fatalf("coalescing: %+v %+v", first, second)
		}
		const n = 6
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		type outcome struct {
			result WorkResult
			err    error
		}
		start := make(chan struct{})
		results := make(chan outcome, n)
		for i := range n {
			go func(i int) {
				<-start
				r, err := quotaRequest(ctx, f, d, fmt.Sprintf("concurrent-%d", i), PriorityBackground)
				results <- outcome{r, err}
			}(i)
		}
		close(start)
		for range n {
			select {
			case o := <-results:
				if o.err != nil || o.result.JobID != first.JobID || o.result.Created {
					t.Fatalf("concurrent coalescing: %+v %v", o.result, o.err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		quotaLedger(t, f, 1, 32)
		other := quotaRequireWork(t, f, quotaDifferentDescriptor(d), "other", PriorityBackground)
		if !other.Created {
			t.Fatal("second distinct job not created")
		}
		_, usage, err := f.coordinator.BackfillSnapshot(ctx)
		if err != nil || usage.Jobs != 2 || usage.Bytes != 64 || usage.ProviderActive["rsync"] != 2 || usage.CapabilityActive["noop"] != 2 {
			t.Fatalf("queued snapshot=%+v err=%v", usage, err)
		}
		third := quotaDifferentDescriptor(quotaDifferentDescriptor(d))
		if _, err := quotaRequest(ctx, f, third, "denied", PriorityBackground); !errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("quota denial: %v", err)
		}
		p.change(func(v *BackfillPolicy) { v.Paused = true })
		reused := quotaRequireWork(t, f, d, "paused-reuse", PriorityBackground)
		if reused.Created || reused.JobID != first.JobID {
			t.Fatalf("paused coalescing=%+v", reused)
		}
		quotaLedger(t, f, 2, 64)
		var interests int64
		if err := f.db.Model(&model.BackupAssetProcessingInterest{}).Where("job_id = ? AND active = ?", first.JobID, true).Count(&interests).Error; err != nil || interests != n+3 {
			t.Fatalf("interests=%d err=%v", interests, err)
		}
	})
	t.Run("DeniedTransactionRollback", func(t *testing.T) {
		f, p := quotaBehaviorFixture(t, open)
		d := validWorkDescriptor()
		quotaSize(t, f, d, 32)
		p.change(func(v *BackfillPolicy) { v.Paused = true })
		if _, err := quotaRequest(context.Background(), f, d, "paused", PriorityBackground); !errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("paused error: %v", err)
		}
		quotaLedger(t, f, 0, 0)
		p.change(func(v *BackfillPolicy) { v.Paused = false })
		injected := errors.New("policy snapshot unavailable")
		p.mu.Lock()
		p.err = injected
		p.mu.Unlock()
		if _, err := quotaRequest(context.Background(), f, d, "fault", PriorityBackground); !errors.Is(err, injected) || errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("policy failure: %v", err)
		}
		quotaLedger(t, f, 0, 0)
		var jobs, interests int64
		if err := f.db.Model(&model.BackupAssetProcessingJob{}).Count(&jobs).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&model.BackupAssetProcessingInterest{}).Count(&interests).Error; err != nil {
			t.Fatal(err)
		}
		if jobs != 0 || interests != 0 {
			t.Fatalf("rollback left jobs=%d interests=%d", jobs, interests)
		}
		p.mu.Lock()
		p.err = nil
		p.policy.BytesPerHour = 32
		p.mu.Unlock()
		const callback = "test:quota-interest-create-failure"
		injectedWrite := errors.New("injected interest write failure")
		if err := f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "backup_asset_processing_interests" {
				_ = tx.AddError(injectedWrite)
			}
		}); err != nil {
			t.Fatal(err)
		}
		_, err := quotaRequest(context.Background(), f, d, "write-fault", PriorityBackground)
		if removeErr := f.db.Callback().Create().Remove(callback); removeErr != nil {
			t.Fatal(removeErr)
		}
		if !errors.Is(err, injectedWrite) {
			t.Fatalf("post-charge write error=%v", err)
		}
		quotaLedger(t, f, 0, 0)
		if err := f.db.Model(&model.BackupAssetProcessingJob{}).Count(&jobs).Error; err != nil || jobs != 0 {
			t.Fatalf("post-charge jobs=%d err=%v", jobs, err)
		}
		quotaRequireWork(t, f, d, "fits", PriorityBackground)
		_, err = quotaRequest(context.Background(), f, quotaDifferentDescriptor(d), "over-bytes", PriorityBackground)
		var denial *BackfillAdmissionError
		if !errors.As(err, &denial) || denial.Reason != BackfillAdmissionByteQuota {
			t.Fatalf("byte quota error=%v", err)
		}
		quotaLedger(t, f, 1, 32)
		if err := f.db.Exec("UPDATE schema_migrations SET dirty = ?", true).Error; err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.coordinator.BackfillSnapshot(context.Background()); err == nil || errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("dirty schema snapshot error=%v", err)
		}
		if _, err := quotaRequest(context.Background(), f, quotaDifferentDescriptor(d), "dirty-schema", PriorityBackground); err == nil || errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("dirty schema request error=%v", err)
		}
		if err := f.db.Exec("UPDATE schema_migrations SET dirty = ?", false).Error; err != nil {
			t.Fatal(err)
		}
		quotaLedger(t, f, 1, 32)
	})
	t.Run("RetryCancelReplacement", func(t *testing.T) {
		f, _ := quotaBehaviorFixture(t, open)
		d := validWorkDescriptor()
		quotaSize(t, f, d, 32)
		work := quotaRequireWork(t, f, d, "retry", PriorityBackground)
		first, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
		if err != nil {
			t.Fatal(err)
		}
		retryAt := f.clock.Now().Add(time.Second)
		if _, err := f.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{JobID: work.JobID, AttemptID: first.Lease.AttemptID, WorkerID: f.workerID, ExpectedRevision: 2, To: ProcessingRetryWait, ErrorCode: ProcessingErrorWorkerUnavailable, RetryAt: &retryAt}); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(2 * time.Second)
		reconciler, err := NewReconciler(f.coordinator, f.grants, f.clock.Now, ReconcilerConfig{BatchSize: 100, RetryBase: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if n, err := reconciler.PromoteRetries(context.Background()); err != nil || n != 1 {
			t.Fatalf("promote=%d err=%v", n, err)
		}
		second, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
		if err != nil || second.Lease.JobID != work.JobID || second.Lease.AttemptID == first.Lease.AttemptID {
			t.Fatalf("retry pull=%+v err=%v", second, err)
		}
		quotaLedger(t, f, 1, 32)
		if err := f.coordinator.RemoveInterest(context.Background(), work.JobID, InterestWorkspace, "retry", InterestRemovedCanceled); err != nil {
			t.Fatal(err)
		}
		var job model.BackupAssetProcessingJob
		if err := f.db.First(&job, "id = ?", work.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := f.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{JobID: work.JobID, AttemptID: second.Lease.AttemptID, WorkerID: f.workerID, ExpectedRevision: job.TransitionRevision, To: ProcessingCanceled, CancelReason: CancelReasonInterestWithdrawn}); err != nil {
			t.Fatal(err)
		}
		if err := f.db.First(&job, "id = ?", work.JobID).Error; err != nil || job.IsCurrent || job.State != string(ProcessingCanceled) {
			t.Fatalf("terminal cancellation job=%+v err=%v", job, err)
		}
		replacement := quotaRequireWork(t, f, d, "replacement", PriorityBackground)
		if !replacement.Created || replacement.JobID == work.JobID {
			t.Fatalf("replacement=%+v", replacement)
		}
		quotaLedger(t, f, 2, 64)
	})
	t.Run("UtcWindowAndClockRollback", func(t *testing.T) {
		f, p := quotaBehaviorFixture(t, open)
		d := validWorkDescriptor()
		quotaSize(t, f, d, 32)
		p.change(func(v *BackfillPolicy) { v.JobsPerHour = 1 })
		f.clock.Advance(59*time.Minute + 59*time.Second)
		quotaRequireWork(t, f, d, "before", PriorityBackground)
		previous := quotaLedger(t, f, 1, 32)
		f.clock.Advance(time.Second)
		_, snapshot, err := f.coordinator.BackfillSnapshot(context.Background())
		if err != nil || snapshot.Jobs != 0 || snapshot.Bytes != 0 {
			t.Fatalf("new window snapshot=%+v err=%v", snapshot, err)
		}
		quotaLedger(t, f, 1, 32)
		quotaRequireWork(t, f, quotaDifferentDescriptor(d), "after", PriorityBackground)
		current := quotaLedger(t, f, 1, 32)
		if !current.WindowStartedAt.Equal(previous.WindowStartedAt.Add(time.Hour)) {
			t.Fatalf("window before=%s after=%s", previous.WindowStartedAt, current.WindowStartedAt)
		}
		f.clock.Advance(-time.Hour)
		if _, err := quotaRequest(context.Background(), f, quotaDifferentDescriptor(quotaDifferentDescriptor(d)), "backward", PriorityBackground); !errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("clock rollback error=%v", err)
		}
		if _, _, err := f.coordinator.BackfillSnapshot(context.Background()); !errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("future ledger snapshot error=%v", err)
		}
		quotaLedger(t, f, 1, 32)
	})
	t.Run("ProviderAndCapabilityLastSlot", func(t *testing.T) {
		t.Run("Provider", func(t *testing.T) { quotaLastSlot(t, open, true) })
		t.Run("Capability", func(t *testing.T) { quotaLastSlot(t, open, false) })
	})
	t.Run("InteractivePromotionSlots", func(t *testing.T) {
		f, p := quotaBehaviorFixture(t, open)
		p.change(func(v *BackfillPolicy) { v.ProviderConcurrency = 1; v.CapabilityConcurrency = 1 })
		d := validWorkDescriptor()
		other := quotaSecondPoint(t, f)
		background := quotaRequireWork(t, f, d, "background", PriorityBackground)
		first, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
		if err != nil || first.Lease.SlotClass != SlotBackground {
			t.Fatalf("background pull=%+v err=%v", first, err)
		}
		promoted := quotaRequireWork(t, f, d, "interactive", PriorityInteractive)
		if promoted.Created || promoted.JobID != background.JobID {
			t.Fatalf("promotion=%+v", promoted)
		}
		if err := f.coordinator.RemoveInterest(context.Background(), background.JobID, InterestWorkspace, "interactive", InterestRemovedCanceled); err != nil {
			t.Fatal(err)
		}
		var job model.BackupAssetProcessingJob
		var attempt model.BackupAssetProcessingAttempt
		if err := f.db.First(&job, "id = ?", background.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.First(&attempt, "id = ?", first.Lease.AttemptID).Error; err != nil {
			t.Fatal(err)
		}
		if job.PriorityClass != string(PriorityInteractive) || attempt.SlotClass != string(SlotBackground) {
			t.Fatalf("promotion pin: job=%+v attempt=%+v", job, attempt)
		}
		next := quotaRequireWork(t, f, other, "other", PriorityBackground)
		leased, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
		if err != nil || leased.Lease.JobID != next.JobID || leased.Lease.SlotClass != SlotBackground {
			t.Fatalf("released bucket: %+v err=%v", leased, err)
		}
		quotaLedger(t, f, 2, 2048)
	})
	t.Run("SkipSaturatedBucket", func(t *testing.T) { quotaSkipSaturatedBucket(t, open) })
	t.Run("LifecycleRelease", func(t *testing.T) { quotaLifecycleRelease(t, open) })
	t.Run("InvalidationBatchAtomicity", func(t *testing.T) { quotaInvalidationBatchAtomicity(t, open) })
	t.Run("SourceMetadata", func(t *testing.T) {
		f, _ := quotaBehaviorFixture(t, open)
		d := validWorkDescriptor()
		quotaSize(t, f, d, 0)
		first := quotaRequireWork(t, f, d, "zero", PriorityBackground)
		var job model.BackupAssetProcessingJob
		if err := f.db.First(&job, "id = ?", first.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if job.ProviderKind != "rsync" || job.EstimatedInputBytes != 0 || !job.BackfillCharged {
			t.Fatalf("source metadata=%+v", job)
		}
		quotaLedger(t, f, 1, 0)
		derived := quotaDifferentDescriptor(d)
		derived.EntryFingerprint = strings.Repeat("a", 64)
		derived.Capability, derived.CapabilitySchema, derived.OutputProfile = "secret.classify", "secret.classify.v1", "secret.classify.v1"
		var capability model.BackupAssetWorkerCapability
		if err := f.db.First(&capability, "worker_id = ?", f.workerID).Error; err != nil {
			t.Fatal(err)
		}
		capability.ID = strings.Repeat("8", 32)
		capability.Capability = derived.Capability
		capability.CapabilitySchema = derived.CapabilitySchema
		capability.OutputProfile = derived.OutputProfile
		if err := f.db.Create(&capability).Error; err != nil {
			t.Fatal(err)
		}
		second := quotaRequireWork(t, f, derived, "derived-digest", PriorityInteractive)
		job = model.BackupAssetProcessingJob{}
		if err := f.db.First(&job, "id = ?", second.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if job.ProviderKind != "rsync" || job.EstimatedInputBytes != 0 || job.BackfillCharged {
			t.Fatalf("interactive source metadata=%+v", job)
		}
		missing := quotaDifferentDescriptor(derived)
		missing.Source.EntryID = strings.Repeat("9", 64)
		if _, err := quotaRequest(context.Background(), f, missing, "missing", PriorityBackground); !errors.Is(err, ErrInvalidContract) || errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("missing source error=%v", err)
		}
		quotaLedger(t, f, 1, 0)
	})
}

func quotaSecondPoint(t *testing.T, f processingBehaviorFixture) WorkDescriptorV1 {
	t.Helper()
	d := validWorkDescriptor()
	d.Source.RecoveryPointID = strings.Repeat("1", 32)
	d.CatalogGenerationID = strings.Repeat("2", 32)
	d.Source.EntryID = strings.Repeat("3", 64)
	processingBehaviorSeedSecondPoint(t, f, d)
	return d
}

func quotaLastSlot(t *testing.T, open func(*testing.T) processingBehaviorFixture, provider bool) {
	t.Helper()
	f, p := quotaBehaviorFixture(t, open)
	p.change(func(v *BackfillPolicy) {
		if provider {
			v.ProviderConcurrency = 1
		} else {
			v.CapabilityConcurrency = 1
		}
	})
	second := quotaSecondPoint(t, f)
	harness := &coordinatorHarness{db: f.db, clock: f.clock, coordinator: f.coordinator}
	worker2 := harness.registerNoopWorker(t, "c")
	quotaReviewAssignWorkerFingerprint(t, f, worker2, &second)
	quotaRequireWork(t, f, validWorkDescriptor(), "one", PriorityBackground)
	quotaRequireWork(t, f, second, "two", PriorityBackground)
	if f.engine == "postgres" {
		quotaReviewLastSlotPostgres(t, f, worker2)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type outcome struct {
		leased LeasedAttempt
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, worker := range []string{f.workerID, worker2} {
		go func(worker string) {
			<-start
			leased, err := f.coordinator.PullAttempt(ctx, PullRequest{WorkerID: worker}, f.grants)
			results <- outcome{leased, err}
		}(worker)
	}
	close(start)
	successes := 0
	for range 2 {
		select {
		case result := <-results:
			if result.err == nil {
				successes++
				if result.leased.Lease.AttemptID == "" || result.leased.Grants.Input.GrantID == "" || result.leased.Grants.Sink.GrantID == "" {
					t.Fatal("successful pull lacked real attempt or grants")
				}
			} else if !errors.Is(result.err, ErrNoWork) {
				t.Fatalf("pull race: %v", result.err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if successes != 1 {
		t.Fatalf("last slot admitted %d attempts", successes)
	}
	var attempts, grants int64
	if err := f.db.Model(&model.BackupAssetProcessingAttempt{}).Where("is_current = ?", true).Count(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.BackupAssetProcessingGrant{}).Count(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || grants != 2 {
		t.Fatalf("attempts=%d grants=%d", attempts, grants)
	}
}

func quotaSkipSaturatedBucket(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	t.Helper()
	f, p := quotaBehaviorFixture(t, open)
	p.change(func(v *BackfillPolicy) { v.CapabilityConcurrency = 1 })
	first := validWorkDescriptor()
	alternate := quotaSecondPoint(t, f)
	alternate.Capability = "noop.other"
	alternate.CapabilitySchema = "noop.other.v1"
	alternate.OutputProfile = "noop.other.v1"
	var capability model.BackupAssetWorkerCapability
	if err := f.db.First(&capability, "worker_id = ?", f.workerID).Error; err != nil {
		t.Fatal(err)
	}
	capability.ID = strings.Repeat("8", 32)
	capability.Capability = alternate.Capability
	capability.CapabilitySchema = alternate.CapabilitySchema
	capability.OutputProfile = alternate.OutputProfile
	if err := f.db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	quotaRequireWork(t, f, first, "running", PriorityBackground)
	if _, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants); err != nil {
		t.Fatal(err)
	}
	saturated := quotaRequireWork(t, f, quotaDifferentDescriptor(first), "saturated", PriorityBackground)
	next, err := f.coordinator.RequestWork(context.Background(), WorkRequest{Descriptor: alternate, Interest: InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: "other-bucket", PriorityClass: PriorityBackground, Priority: 50}})
	if err != nil {
		t.Fatal(err)
	}
	pulled, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
	if err != nil || pulled.Lease.JobID != next.JobID || pulled.Lease.JobID == saturated.JobID {
		t.Fatalf("cross-bucket pull=%+v err=%v", pulled, err)
	}
	p.change(func(v *BackfillPolicy) { v.Paused = true })
	interactive := quotaDifferentDescriptor(alternate)
	interactiveWork := quotaRequireWork(t, f, interactive, "reserved", PriorityInteractive)
	pulled, err = f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
	if err != nil || pulled.Lease.JobID != interactiveWork.JobID || pulled.Lease.SlotClass != SlotInteractive {
		t.Fatalf("reserved interactive pull=%+v err=%v", pulled, err)
	}
}
