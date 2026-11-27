package recovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type recoveryRepositoryGateBarrier struct {
	arrived chan int
	release chan struct{}
	once    sync.Once
}

func (barrier *recoveryRepositoryGateBarrier) hold(ctx context.Context, tx *gorm.DB) error {
	var pid int
	if err := tx.WithContext(ctx).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
		return err
	}
	select {
	case barrier.arrived <- pid:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-barrier.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (barrier *recoveryRepositoryGateBarrier) unblock() {
	barrier.once.Do(func() { close(barrier.release) })
}

type recoveryRepositoryGateTargetRoot struct {
	eligibilityPlanLockTargetRoot
	barrier *recoveryRepositoryGateBarrier
}

func (root recoveryRepositoryGateTargetRoot) RevalidateRecoveryEligibilityTargetRootTx(
	ctx context.Context, tx *gorm.DB, binding RecoveryAuthorityBinding, observed RecoveryEligibilityTargetRootSnapshot,
) error {
	if err := root.eligibilityPlanLockTargetRoot.RevalidateRecoveryEligibilityTargetRootTx(ctx, tx, binding, observed); err != nil {
		return err
	}
	if root.barrier != nil {
		return root.barrier.hold(ctx, tx)
	}
	return nil
}

type recoveryRepositoryGateSource struct {
	RecoveryEligibilitySourcePort
}

func (source recoveryRepositoryGateSource) RevalidateRecoveryEligibilitySourceTx(
	ctx context.Context, tx *gorm.DB, binding RecoveryAuthorityBinding, observed RecoveryEligibilitySourceObservation,
) error {
	for _, row := range []struct{ table, id string }{
		{"backup_repositories", binding.RepositoryID}, {"recovery_points", binding.RecoveryPointID},
	} {
		var found struct{ ID string }
		if err := tx.WithContext(ctx).Table(row.table).Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", row.id).Take(&found).Error; err != nil {
			return err
		}
	}
	return source.RecoveryEligibilitySourcePort.RevalidateRecoveryEligibilitySourceTx(ctx, tx, binding, observed)
}

type recoveryRepositoryGateWorkerAuthority struct {
	RecoveryAuthorityRevalidator
	barrier      *recoveryRepositoryGateBarrier
	targetNodeID uint
	targetRootID string
}

func (authority recoveryRepositoryGateWorkerAuthority) RevalidateRecoveryAuthorityTx(
	ctx context.Context, tx *gorm.DB, binding RecoveryAuthorityBinding, observation RecoveryAuthorityObservation,
) error {
	if authority.barrier != nil {
		if err := authority.barrier.hold(ctx, tx); err != nil {
			return err
		}
	}
	if binding.TargetNodeID != authority.targetNodeID || binding.TargetRootID != authority.targetRootID {
		return ErrRecoveryTargetChanged
	}
	for _, row := range []struct {
		table string
		id    any
	}{{"nodes", binding.TargetNodeID}, {"eligibility_test_roots", binding.TargetRootID}} {
		var found struct{ ID string }
		if err := tx.WithContext(ctx).Table(row.table).Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", row.id).Take(&found).Error; err != nil {
			return err
		}
	}
	return authority.RecoveryAuthorityRevalidator.RevalidateRecoveryAuthorityTx(ctx, tx, binding, observation)
}

func TestRecoveryEligibilityRepositoryGatePostgres(t *testing.T) {
	for _, test := range []struct {
		name        string
		effectFirst bool
	}{
		{name: "effect_first", effectFirst: true},
		{name: "observation_first"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptExecute)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			coordinator.target = nil
			claim, found, err := coordinator.ClaimNext(context.Background(), "repository-gate-worker")
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim recovery job: found=%t err=%v", found, err)
			}

			observerPlanID := fixture.clonePlan(t)
			var observerPlan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", observerPlanID).Take(&observerPlan).Error; err != nil {
				t.Fatal(err)
			}
			var effectPlan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&effectPlan).Error; err != nil {
				t.Fatal(err)
			}
			if observerPlan.ID == effectPlan.ID || observerPlan.RepositoryID == "" || observerPlan.RepositoryID != effectPlan.RepositoryID {
				t.Fatal("fixture must use distinct plans for one repository")
			}
			harness := newRecoveryEligibilityHarnessOnDB(t, fixture.db)
			harness.binding.PlanID = observerPlan.ID
			harness.binding.PlanBindingDigest = observerPlan.BindingDigest
			harness.binding.PlanTransitionRevision = observerPlan.TransitionRevision
			harness.binding.RepositoryID = observerPlan.RepositoryID
			harness.binding.RecoveryPointID = effectPlan.RecoveryPointID
			harness.binding.TargetNodeID = effectPlan.TargetNodeID
			harness.binding.TargetRootID = effectPlan.TargetRootID
			harness.root.current.NodeID = effectPlan.TargetNodeID
			harness.root.current.RootID = effectPlan.TargetRootID
			harness.binding.SourceRef.PlanID = observerPlan.ID
			harness.binding.SourceRef.PlanBindingDigest = observerPlan.BindingDigest
			harness.binding.SourceRef.RepositoryID = observerPlan.RepositoryID
			harness.binding.SourceRef.RecoveryPointID = effectPlan.RecoveryPointID
			harness.source.binding = harness.binding
			harness.source.namespaceCaptured.sourceRef = harness.binding.SourceRef
			harness.source.namespaceDurable.(*lateRecoverySourceNamespaceDurable).current = harness.source.namespaceCaptured

			if observerPlan.TargetNodeID != effectPlan.TargetNodeID || observerPlan.TargetRootID != effectPlan.TargetRootID {
				t.Fatal("fixture must use the same target node and root")
			}
			if err := fixture.db.Exec("CREATE TABLE eligibility_test_roots (id TEXT PRIMARY KEY)").Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Exec("INSERT INTO eligibility_test_roots (id) VALUES (?)", harness.binding.TargetRootID).Error; err != nil {
				t.Fatal(err)
			}

			barrier := &recoveryRepositoryGateBarrier{arrived: make(chan int, 1), release: make(chan struct{})}
			root := recoveryRepositoryGateTargetRoot{eligibilityPlanLockTargetRoot: eligibilityPlanLockTargetRoot{harness.root}}
			workerAuthority := recoveryRepositoryGateWorkerAuthority{RecoveryAuthorityRevalidator: coordinator.liveRevalidator,
				targetNodeID: effectPlan.TargetNodeID, targetRootID: effectPlan.TargetRootID}
			if test.effectFirst {
				workerAuthority.barrier = barrier
			} else {
				root.barrier = barrier
			}
			harness.authority.targetRoot = root
			harness.authority.source = recoveryRepositoryGateSource{harness.source}
			coordinator.liveRevalidator = workerAuthority

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			type observeResult struct {
				observation RecoveryAuthorityObservation
				err         error
			}
			type effectResult struct {
				permit TargetWritePermit
				err    error
			}
			observed := make(chan observeResult, 1)
			prepared := make(chan effectResult, 1)
			observeDone := make(chan struct{})
			effectDone := make(chan struct{})
			observeStarted, effectStarted := false, false
			defer func() {
				cancel()
				barrier.unblock()
				if observeStarted {
					<-observeDone
				}
				if effectStarted {
					<-effectDone
				}
			}()
			startObserve := func() {
				observeStarted = true
				go func() {
					defer close(observeDone)
					observation, err := harness.authority.ObserveRecoveryAuthority(ctx, harness.binding)
					observed <- observeResult{observation: observation, err: err}
				}()
			}
			startEffect := func() {
				effectStarted = true
				go func() {
					defer close(effectDone)
					permit, err := coordinator.PrepareFirstWrite(ctx, claim)
					prepared <- effectResult{permit: permit, err: err}
				}()
			}
			if test.effectFirst {
				startEffect()
			} else {
				startObserve()
			}
			var holderPID int
			select {
			case holderPID = <-barrier.arrived:
			case result := <-observed:
				t.Fatalf("observation finished before holding the gate: %v", result.err)
			case result := <-prepared:
				t.Fatalf("first-write effect finished before holding the gate: %v", result.err)
			case <-ctx.Done():
				t.Fatalf("waiting for gate holder: %v", ctx.Err())
			}
			heldRows := []struct {
				table string
				id    any
			}{{"nodes", harness.binding.TargetNodeID}, {"eligibility_test_roots", harness.binding.TargetRootID}}
			if test.effectFirst {
				heldRows = []struct {
					table string
					id    any
				}{{"backup_repositories", effectPlan.RepositoryID}, {"recovery_points", effectPlan.RecoveryPointID}}
			}
			for _, row := range heldRows {
				lockErr := fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var found struct{ ID string }
					return tx.Table(row.table).Select("id").Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).
						Where("id = ?", row.id).Take(&found).Error
				})
				var sqlState interface{ SQLState() string }
				if !errors.As(lockErr, &sqlState) || sqlState.SQLState() != "55P03" {
					t.Fatalf("holder must lock %s row: %v", row.table, lockErr)
				}
			}
			if test.effectFirst {
				startObserve()
			} else {
				startEffect()
			}

			waiterDone := (<-chan struct{})(effectDone)
			if test.effectFirst {
				waiterDone = observeDone
			}
			waitRepositoryGateWaiter(t, ctx, fixture.db, holderPID, observerPlan.RepositoryID, waiterDone)

			if err := fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				rows := []struct {
					table string
					id    any
				}{{"nodes", harness.binding.TargetNodeID}, {"eligibility_test_roots", harness.binding.TargetRootID}}
				if !test.effectFirst {
					rows = []struct {
						table string
						id    any
					}{{"backup_repositories", effectPlan.RepositoryID}, {"recovery_points", effectPlan.RecoveryPointID}}
				}
				for _, row := range rows {
					var found struct{ ID string }
					if err := tx.Table(row.table).Select("id").Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).
						Where("id = ?", row.id).Take(&found).Error; err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("rows needed after the repository gate must remain free during advisory wait: %v", err)
			}
			barrier.unblock()

			var observation RecoveryAuthorityObservation
			select {
			case result := <-observed:
				if result.err != nil || result.observation.proof == nil || !result.observation.proof.production || result.observation.proof.bindingDigest == "" {
					t.Fatalf("observe sealed authority after gate release: %v", result.err)
				}
				observation = result.observation
			case <-ctx.Done():
				t.Fatalf("observation did not finish: %v", ctx.Err())
			}
			select {
			case result := <-prepared:
				if result.err != nil {
					t.Fatalf("prepare first write after gate release: %v", result.err)
				}
				if err := result.permit.ValidateAt(fixture.now); err != nil {
					t.Fatalf("first-write permit: %v", err)
				}
			case <-ctx.Done():
				t.Fatalf("first-write effect did not finish: %v", ctx.Err())
			}
			if err := fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return harness.authority.RevalidateRecoveryAuthorityTx(ctx, tx, harness.binding, observation)
			}); err != nil {
				t.Fatalf("revalidate sealed observation: %v", err)
			}
			var attempt model.BackupAssetRecoveryAttempt
			if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
				t.Fatal(err)
			}
			if !attempt.MutationArmed {
				t.Fatal("first-write effect did not persist mutation authority")
			}
		})
	}
}

func TestRecoveryEligibilityRepositoryGatePostgresOperationalError(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptExecute)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	coordinator.target = nil
	claim, found, err := coordinator.ClaimNext(context.Background(), "repository-gate-error-worker")
	if err != nil || !found {
		t.Fatalf("claim recovery job: found=%t err=%v", found, err)
	}
	var plan model.BackupAssetRecoveryPlan
	var beforeJob model.BackupAssetRecoveryJob
	var beforeAttempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&beforeJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&beforeAttempt).Error; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	holder := fixture.db.WithContext(ctx).Begin()
	if holder.Error != nil {
		cancel()
		t.Fatal(holder.Error)
	}
	var holderPID int
	if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
		_ = holder.Rollback().Error
		cancel()
		t.Fatal(err)
	}
	key := "xirang:recovery-authority-repository:v1:" + plan.RepositoryID
	if err := holder.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
		_ = holder.Rollback().Error
		cancel()
		t.Fatal(err)
	}
	results := make(chan error, 1)
	done := make(chan struct{})
	defer func() {
		cancel()
		_ = holder.Rollback().Error
		<-done
	}()
	go func() {
		defer close(done)
		_, err := coordinator.PrepareFirstWrite(ctx, claim)
		results <- err
	}()

	waiterPID := waitRepositoryGateWaiter(t, ctx, fixture.db, holderPID, plan.RepositoryID, done)
	var canceled bool
	if err := fixture.db.WithContext(ctx).Raw("SELECT pg_cancel_backend(?)", waiterPID).Scan(&canceled).Error; err != nil || !canceled {
		t.Fatalf("cancel waiting advisory statement: canceled=%t err=%v", canceled, err)
	}
	select {
	case err := <-results:
		if !errors.Is(err, ErrRecoveryWorkerUnavailable) || errors.Is(err, ErrRecoverySourceChanged) {
			t.Fatalf("advisory database error = %v, want worker unavailable without source drift", err)
		}
	case <-ctx.Done():
		t.Fatalf("first-write effect did not return after SQL cancellation: %v", ctx.Err())
	}
	var afterJob model.BackupAssetRecoveryJob
	var afterAttempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&afterJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&afterAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if afterJob.State != beforeJob.State || afterJob.FailureCategory != beforeJob.FailureCategory ||
		afterJob.TransitionRevision != beforeJob.TransitionRevision || afterAttempt.State != beforeAttempt.State ||
		afterAttempt.MutationArmed != beforeAttempt.MutationArmed {
		t.Fatal("advisory database error changed job or attempt authority")
	}
}
