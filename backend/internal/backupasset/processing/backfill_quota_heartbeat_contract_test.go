package processing

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/model"
)

func TestProcessingBackfillHeartbeatContractSQLite(t *testing.T) {
	quotaHeartbeatContract(t, openProcessingBehaviorSQLite)
}

func TestProcessingBackfillHeartbeatContractPostgres(t *testing.T) {
	dsn := sharedAuthorityDSN(t)
	open := func(t *testing.T) processingBehaviorFixture { return openProcessingBehaviorPostgres(t, dsn) }
	quotaHeartbeatContract(t, open)
	t.Run("WorkerOrdering", func(t *testing.T) {
		for _, name := range []string{"HeartbeatBeforePull", "PullBeforeHeartbeat", "HeartbeatBeforeSecurityTransition", "SecurityTransitionBeforeHeartbeat", "TransitionBeforeQuarantine", "QuarantineBeforeTransition"} {
			t.Run(name, func(t *testing.T) { quotaHeartbeatWorkerOrdering(t, open, name) })
		}
	})
}

type quotaHeartbeatAuthority struct {
	attempt model.BackupAssetProcessingAttempt
	lease   model.RecoveryPointLease
	grants  []model.BackupAssetProcessingGrant
}

func quotaHeartbeatReadAuthority(t *testing.T, f processingBehaviorFixture, leased LeasedAttempt) quotaHeartbeatAuthority {
	t.Helper()
	var a quotaHeartbeatAuthority
	if err := f.db.First(&a.attempt, "id = ?", leased.Lease.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&a.lease, "id = ?", a.attempt.RecoveryPointLeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("attempt_id = ?", a.attempt.ID).Order("id").Find(&a.grants).Error; err != nil {
		t.Fatal(err)
	}
	if len(a.grants) != 2 {
		t.Fatalf("expected two real grants, got %d", len(a.grants))
	}
	return a
}

func quotaHeartbeatStart(t *testing.T, f processingBehaviorFixture, priority PriorityClass) LeasedAttempt {
	t.Helper()
	d := validWorkDescriptor()
	quotaSize(t, f, d, 32)
	work := quotaRequireWork(t, f, d, "heartbeat-contract", priority)
	leased, err := f.coordinator.PullAttempt(context.Background(), PullRequest{WorkerID: f.workerID}, f.grants)
	if err != nil || leased.Lease.JobID != work.JobID || leased.Grants.Input.GrantID == "" || leased.Grants.Sink.GrantID == "" {
		t.Fatalf("real PullAttempt: %+v: %v", leased, err)
	}
	return leased
}

func quotaHeartbeatContract(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	t.Helper()
	t.Run("PolicyIndependent", func(t *testing.T) {
		for _, name := range []string{"Paused", "LoweredLimit", "FutureWindow", "InvalidPolicy"} {
			t.Run(name, func(t *testing.T) { quotaHeartbeatBehaviorCase(t, open, name) })
		}
	})
	for _, name := range []string{"MissingSeed", "DirtyMetadata", "InteractiveBypass", "DrainingWorker"} {
		t.Run(name, func(t *testing.T) { quotaHeartbeatBehaviorCase(t, open, name) })
	}
	t.Run("RejectedSecurityRetry", func(t *testing.T) { quotaHeartbeatRejectedSecurityRetry(t, open) })
}

func quotaHeartbeatBehaviorCase(t *testing.T, open func(*testing.T) processingBehaviorFixture, name string) {
	f, policy := quotaBehaviorFixture(t, open)
	priority := PriorityBackground
	if name == "InteractiveBypass" {
		priority = PriorityInteractive
	}
	leased := quotaHeartbeatStart(t, f, priority)
	before := quotaHeartbeatReadAuthority(t, f, leased)
	f.clock.Advance(10 * time.Second)
	switch name {
	case "Paused":
		policy.change(func(p *BackfillPolicy) { p.Paused = true })
	case "LoweredLimit":
		policy.change(func(p *BackfillPolicy) {
			p.JobsPerHour = 1
			p.BytesPerHour = 1
			p.ProviderConcurrency = 1
			p.CapabilityConcurrency = 1
		})
	case "FutureWindow":
		if err := f.db.Model(&model.BackupAssetProcessingBackfillLedger{}).Where("id = ?", 1).Update("window_started_at", f.clock.Now().UTC().Truncate(time.Hour).Add(time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
	case "InvalidPolicy":
		policy.mu.Lock()
		policy.policy = BackfillPolicy{}
		policy.err = errors.New("policy source must not be consulted by heartbeat")
		policy.mu.Unlock()
	case "MissingSeed", "InteractiveBypass":
		if err := f.db.Exec("DELETE FROM backup_asset_processing_backfill_ledger WHERE id = 1").Error; err != nil {
			t.Fatal(err)
		}
		if name == "InteractiveBypass" {
			policy.mu.Lock()
			policy.err = errors.New("interactive heartbeat must bypass policy")
			policy.mu.Unlock()
		}
	case "DirtyMetadata":
		if err := f.db.Exec("UPDATE schema_migrations SET dirty = ?", true).Error; err != nil {
			t.Fatal(err)
		}
	case "DrainingWorker":
		protocol, err := NewProtocolService(f.db, NewProductionCapabilityRegistry(), f.clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		if err := protocol.SetDraining(context.Background(), f.workerID); err != nil {
			t.Fatal(err)
		}
	}
	var ledgerBefore model.BackupAssetProcessingBackfillLedger
	if name != "MissingSeed" && name != "InteractiveBypass" {
		ledgerBefore = quotaLedger(t, f, 1, 32)
	}
	if name == "FutureWindow" && !ledgerBefore.WindowStartedAt.After(f.clock.Now()) {
		t.Fatal("fixture did not place the ledger in the future")
	}
	got, err := f.coordinator.HeartbeatAttempt(context.Background(), HeartbeatRequest{AttemptID: leased.Lease.AttemptID, WorkerID: f.workerID}, f.grants)
	after := quotaHeartbeatReadAuthority(t, f, leased)
	if name == "MissingSeed" || name == "DirtyMetadata" {
		if err == nil || errors.Is(err, ErrBackfillDenied) {
			t.Fatalf("expected real serialization failure, got %v", err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("failed heartbeat committed authority changes: before=%+v after=%+v", before, after)
		}
	} else {
		if err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		if !after.attempt.WorkerLeaseExpiresAt.After(before.attempt.WorkerLeaseExpiresAt) || !after.lease.LeaseExpiresAt.After(before.lease.LeaseExpiresAt) || !after.attempt.LastHeartbeatAt.After(before.attempt.LastHeartbeatAt) || !after.lease.LastHeartbeatAt.After(before.lease.LastHeartbeatAt) {
			t.Fatalf("attempt/point authority was not extended: before=%+v after=%+v", before, after)
		}
		for i := range before.grants {
			if after.grants[i].ID != before.grants[i].ID || !after.grants[i].ExpiresAt.After(before.grants[i].ExpiresAt) || after.grants[i].State != before.grants[i].State {
				t.Fatalf("grant not renewed: before=%+v after=%+v", before.grants[i], after.grants[i])
			}
		}
		if !got.WorkerLeaseExpiresAt.Equal(after.attempt.WorkerLeaseExpiresAt) || !got.RecoveryPointLeaseExpiresAt.Equal(after.lease.LeaseExpiresAt) || !got.EffectiveLeaseExpiresAt.Equal(got.WorkerLeaseExpiresAt) || !got.GrantExpiresAt.Equal(after.grants[0].ExpiresAt) || got.TransitionRevision != 2 || got.WorkerDraining != (name == "DrainingWorker") {
			t.Fatalf("heartbeat response does not reflect authority: %+v", got)
		}
		if name == "DrainingWorker" {
			var worker model.BackupAssetWorkerIdentity
			if err := f.db.First(&worker, "id = ?", f.workerID).Error; err != nil {
				t.Fatal(err)
			}
			if worker.TrustState != "active" || worker.HealthState != "draining" {
				t.Fatalf("draining heartbeat changed worker eligibility: %+v", worker)
			}
		}
	}
	if name != "MissingSeed" && name != "InteractiveBypass" {
		afterLedger := quotaLedger(t, f, 1, 32)
		if !reflect.DeepEqual(ledgerBefore, afterLedger) {
			t.Fatalf("heartbeat changed ledger: before=%+v after=%+v", ledgerBefore, afterLedger)
		}
	} else {
		var n int64
		if err := f.db.Model(&model.BackupAssetProcessingBackfillLedger{}).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("heartbeat seeded ledger: count=%d err=%v", n, err)
		}
	}
}

type quotaHeartbeatOrderContextKey struct{}

type quotaHeartbeatOrderResult struct {
	heartbeat  HeartbeatResult
	lease      LeasedAttempt
	transition AttemptTransitionResult
	err        error
}

// The first real operation pauses immediately after locking its Worker. The
// second must be observed by PostgreSQL waiting on that Worker, without any
// downstream row-locking query having acquired a table lock.
func quotaHeartbeatWorkerOrdering(t *testing.T, open func(*testing.T) processingBehaviorFixture, name string) {
	t.Helper()
	f, _ := quotaBehaviorFixture(t, open)
	active := quotaHeartbeatStart(t, f, PriorityBackground)
	if strings.Contains(name, "Pull") {
		other := quotaSecondPoint(t, f)
		quotaRequireWork(t, f, other, "ordering-second", PriorityBackground)
	}
	protocol, err := NewProtocolService(f.db, NewProductionCapabilityRegistry(), f.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(10 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	release := make(chan struct{})
	firstLocked := make(chan int, 1)
	secondQuery := make(chan int, 1)
	var releaseOnce, firstOnce, secondOnce sync.Once
	var workers sync.WaitGroup
	defer func() { releaseOnce.Do(func() { close(release) }); cancel(); workers.Wait() }()
	const beforeName = "test:quota-heartbeat-worker-before"
	const afterName = "test:quota-heartbeat-worker-after"
	if err := f.db.Callback().Query().Before("gorm:query").Register(beforeName, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(quotaHeartbeatOrderContextKey{}) != "second" || processingCallbackTable(tx) != "backup_asset_worker_identities" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		secondOnce.Do(func() {
			var pid int
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				tx.AddError(err)
				return
			}
			secondQuery <- pid
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(beforeName) })
	if err := f.db.Callback().Query().After("gorm:query").Register(afterName, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(quotaHeartbeatOrderContextKey{}) != "first" || processingCallbackTable(tx) != "backup_asset_worker_identities" || tx.Error != nil || tx.RowsAffected != 1 {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		firstOnce.Do(func() {
			var pid int
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				tx.AddError(err)
				return
			}
			firstLocked <- pid
			select {
			case <-release:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(afterName) })

	heartbeat := func(c context.Context) quotaHeartbeatOrderResult {
		r, e := f.coordinator.HeartbeatAttempt(c, HeartbeatRequest{AttemptID: active.Lease.AttemptID, WorkerID: f.workerID}, f.grants)
		return quotaHeartbeatOrderResult{heartbeat: r, err: e}
	}
	pull := func(c context.Context) quotaHeartbeatOrderResult {
		r, e := f.coordinator.PullAttempt(c, PullRequest{WorkerID: f.workerID}, f.grants)
		return quotaHeartbeatOrderResult{lease: r, err: e}
	}
	transition := func(c context.Context) quotaHeartbeatOrderResult {
		req := AttemptTransitionRequest{JobID: active.Lease.JobID, AttemptID: active.Lease.AttemptID, WorkerID: f.workerID, ExpectedRevision: 2, To: ProcessingFetching}
		if strings.Contains(name, "SecurityTransition") {
			req.To = ProcessingFailed
			req.ErrorCode = ProcessingErrorInvalidOutput
		}
		r, e := f.coordinator.TransitionAttempt(c, req)
		return quotaHeartbeatOrderResult{transition: r, err: e}
	}
	quarantine := func(c context.Context) quotaHeartbeatOrderResult {
		return quotaHeartbeatOrderResult{err: protocol.Quarantine(c, f.workerID, ProcessingErrorInvalidOutput)}
	}
	var first, second func(context.Context) quotaHeartbeatOrderResult
	switch name {
	case "HeartbeatBeforePull":
		first, second = heartbeat, pull
	case "PullBeforeHeartbeat":
		first, second = pull, heartbeat
	case "HeartbeatBeforeSecurityTransition":
		first, second = heartbeat, transition
	case "SecurityTransitionBeforeHeartbeat":
		first, second = transition, heartbeat
	case "TransitionBeforeQuarantine":
		first, second = transition, quarantine
	case "QuarantineBeforeTransition":
		first, second = quarantine, transition
	default:
		t.Fatalf("unknown ordering %q", name)
	}
	firstDone, secondDone := make(chan quotaHeartbeatOrderResult, 1), make(chan quotaHeartbeatOrderResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		firstDone <- first(context.WithValue(ctx, quotaHeartbeatOrderContextKey{}, "first"))
	}()
	var holder int
	select {
	case holder = <-firstLocked:
	case result := <-firstDone:
		t.Fatalf("first operation ended before Worker barrier: %v", result.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		secondDone <- second(context.WithValue(ctx, quotaHeartbeatOrderContextKey{}, "second"))
	}()
	var waiter int
	select {
	case waiter = <-secondQuery:
	case result := <-secondDone:
		t.Fatalf("second operation ended before Worker query: %v", result.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if holder <= 0 || waiter <= 0 || holder == waiter {
		t.Fatalf("invalid backend identities %d/%d", holder, waiter)
	}
	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case result := <-secondDone:
			t.Fatalf("second operation did not wait for Worker: %v", result.err)
		case <-ctx.Done():
			t.Fatalf("waiting for PostgreSQL Worker blocker: %v", ctx.Err())
		default:
		}
		var blocked bool
		var query string
		var downstream int64
		err := sqlDB.QueryRowContext(ctx, `SELECT
			$1 = ANY(pg_blocking_pids(a.pid)) AND EXISTS (
				SELECT 1 FROM pg_locks h WHERE h.pid = $1 AND h.granted AND h.relation = 'backup_asset_worker_identities'::regclass AND h.mode = 'RowShareLock'),
			a.query,
			(SELECT count(*) FROM pg_locks l WHERE l.pid = a.pid AND l.granted AND l.relation IN
			 ('backup_asset_processing_jobs'::regclass, 'backup_asset_processing_attempts'::regclass, 'backup_asset_processing_grants'::regclass, 'recovery_point_leases'::regclass)
			 AND l.mode IN ('RowShareLock','RowExclusiveLock','ShareLock','ShareRowExclusiveLock','ExclusiveLock','AccessExclusiveLock'))
			FROM pg_stat_activity a WHERE a.pid = $2`, holder, waiter).Scan(&blocked, &query, &downstream)
		if err != nil {
			t.Fatalf("Worker lock observation: %v", err)
		}
		if blocked {
			if !strings.Contains(strings.ToLower(query), "backup_asset_worker_identities") || !strings.Contains(strings.ToLower(query), "for update") || downstream != 0 {
				t.Fatalf("wrong wait or downstream locks: query=%q downstream=%d", query, downstream)
			}
			break
		}
	}
	releaseOnce.Do(func() { close(release) })
	var a, b quotaHeartbeatOrderResult
	select {
	case a = <-firstDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case b = <-secondDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if a.err != nil {
		t.Fatalf("first operation failed: %v", a.err)
	}
	switch name {
	case "HeartbeatBeforePull", "PullBeforeHeartbeat":
		if b.err != nil {
			t.Fatalf("second operation failed: %v", b.err)
		}
		hb, pulled := a.heartbeat, b.lease
		if name == "PullBeforeHeartbeat" {
			hb, pulled = b.heartbeat, a.lease
		}
		if !hb.WorkerLeaseExpiresAt.After(active.Lease.WorkerLeaseExpiresAt) || pulled.Lease.AttemptID == "" || pulled.Lease.AttemptID == active.Lease.AttemptID || pulled.Grants.Input.GrantID == "" || pulled.Grants.Sink.GrantID == "" {
			t.Fatalf("pull/heartbeat products invalid: heartbeat=%+v pull=%+v", hb, pulled)
		}
		stored := quotaHeartbeatReadAuthority(t, f, active)
		if !stored.attempt.WorkerLeaseExpiresAt.Equal(hb.WorkerLeaseExpiresAt) || !stored.lease.LeaseExpiresAt.Equal(hb.RecoveryPointLeaseExpiresAt) {
			t.Fatalf("committed heartbeat authority differs from response: %+v", stored)
		}
		for _, grant := range stored.grants {
			if grant.State != string(GrantIssued) || !grant.ExpiresAt.Equal(hb.GrantExpiresAt) {
				t.Fatalf("heartbeat grant authority invalid: %+v", grant)
			}
		}
		secondAuthority := quotaHeartbeatReadAuthority(t, f, pulled)
		if secondAuthority.attempt.State != "active" || !secondAuthority.attempt.IsCurrent || secondAuthority.lease.Status != "active" {
			t.Fatalf("second pull did not create live authority: %+v", secondAuthority)
		}
	case "HeartbeatBeforeSecurityTransition":
		if b.err != nil || b.transition.State != ProcessingFailed || !a.heartbeat.WorkerLeaseExpiresAt.After(active.Lease.WorkerLeaseExpiresAt) {
			t.Fatalf("heartbeat/security transition: heartbeat=%+v transition=%+v err=%v", a.heartbeat, b.transition, b.err)
		}
	case "SecurityTransitionBeforeHeartbeat":
		if a.transition.State != ProcessingFailed || !errors.Is(b.err, ErrAttemptLost) {
			t.Fatalf("security transition/heartbeat: transition=%+v heartbeat error=%v", a.transition, b.err)
		}
	case "TransitionBeforeQuarantine", "QuarantineBeforeTransition":
		if b.err != nil {
			t.Fatalf("second operation failed: %v", b.err)
		}
		tr := a.transition
		if name == "QuarantineBeforeTransition" {
			tr = b.transition
		}
		if tr.State != ProcessingFetching || tr.Revision != 3 {
			t.Fatalf("transition after identity serialization: %+v", tr)
		}
	}
	var worker model.BackupAssetWorkerIdentity
	if err := f.db.First(&worker, "id = ?", f.workerID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(name, "SecurityTransition") || strings.Contains(name, "Quarantine") {
		if worker.TrustState != "quarantined" || worker.QuarantineCode != string(ProcessingErrorInvalidOutput) {
			t.Fatalf("worker trust outcome: %+v", worker)
		}
		if worker.HealthState != "draining" {
			t.Fatalf("quarantined worker is not draining: %+v", worker)
		}
		stored := quotaHeartbeatReadAuthority(t, f, active)
		var job model.BackupAssetProcessingJob
		if err := f.db.First(&job, "id = ?", active.Lease.JobID).Error; err != nil {
			t.Fatal(err)
		}
		if strings.Contains(name, "SecurityTransition") {
			if job.State != string(ProcessingFailed) || job.IsCurrent || stored.attempt.State != "failed" || stored.attempt.IsCurrent || stored.lease.Status != "released" {
				t.Fatalf("security transition left authority live: job=%+v attempt=%+v lease=%+v", job, stored.attempt, stored.lease)
			}
		} else if job.State != string(ProcessingFetching) || !job.IsCurrent || stored.attempt.State != "active" || !stored.attempt.IsCurrent || stored.lease.Status != "active" {
			t.Fatalf("quarantine changed transition eligibility: job=%+v attempt=%+v lease=%+v", job, stored.attempt, stored.lease)
		}
		for _, grant := range stored.grants {
			if grant.State != string(GrantRevoked) || grant.ActivationSecretHash != "" {
				t.Fatalf("grant survived quarantine: %+v", grant)
			}
		}
	} else if worker.TrustState != "active" {
		t.Fatalf("unexpected trust state: %s", worker.TrustState)
	}
}

func quotaHeartbeatRejectedSecurityRetry(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	f, _ := quotaBehaviorFixture(t, open)
	active := quotaHeartbeatStart(t, f, PriorityBackground)
	if err := f.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", active.Lease.JobID).Update("retry_count", 5).Error; err != nil {
		t.Fatal(err)
	}
	before := quotaHeartbeatReadAuthority(t, f, active)
	var jobBefore, jobAfter model.BackupAssetProcessingJob
	var workerBefore, workerAfter model.BackupAssetWorkerIdentity
	if err := f.db.First(&jobBefore, "id = ?", active.Lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&workerBefore, "id = ?", f.workerID).Error; err != nil {
		t.Fatal(err)
	}
	ledgerBefore := quotaLedger(t, f, 1, 32)
	retryAt := f.clock.Now().Add(time.Minute)
	_, err := f.coordinator.TransitionAttempt(context.Background(), AttemptTransitionRequest{JobID: active.Lease.JobID, AttemptID: active.Lease.AttemptID, WorkerID: f.workerID, ExpectedRevision: 2, To: ProcessingRetryWait, ErrorCode: ProcessingErrorInvalidOutput, RetryAt: &retryAt})
	if !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("exhausted security retry: %v", err)
	}
	after := quotaHeartbeatReadAuthority(t, f, active)
	if err := f.db.First(&jobAfter, "id = ?", active.Lease.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&workerAfter, "id = ?", f.workerID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(jobBefore, jobAfter) || !reflect.DeepEqual(workerBefore, workerAfter) || !reflect.DeepEqual(ledgerBefore, quotaLedger(t, f, 1, 32)) {
		t.Fatalf("rejected security retry mutated authority, job, worker or ledger")
	}
}
