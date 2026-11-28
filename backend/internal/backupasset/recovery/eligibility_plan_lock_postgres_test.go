package recovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// This adapter models the durable target revalidation lock order without any
// network access. Both rows are independent from the held recovery plan.
type eligibilityPlanLockTargetRoot struct {
	*statefulRecoveryEligibilityTargetRoot
}

func (root eligibilityPlanLockTargetRoot) RevalidateRecoveryEligibilityTargetRootTx(
	ctx context.Context, tx *gorm.DB, binding RecoveryAuthorityBinding, observed RecoveryEligibilityTargetRootSnapshot,
) error {
	for _, row := range []struct {
		table string
		id    any
	}{
		{table: "nodes", id: binding.TargetNodeID},
		{table: "eligibility_test_roots", id: binding.TargetRootID},
	} {
		var found struct{ ID string }
		loaded := tx.WithContext(ctx).Table(row.table).Select("id").
			Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", row.id).Take(&found)
		if loaded.Error != nil {
			return loaded.Error
		}
	}
	return root.statefulRecoveryEligibilityTargetRoot.RevalidateRecoveryEligibilityTargetRootTx(ctx, tx, binding, observed)
}

func TestRecoveryEligibilityObservePlanFirstPostgres(t *testing.T) {
	db := newAuthorizationReceiptPostgresScopedDB(t)
	harness := newRecoveryEligibilityHarnessOnDB(t, db)
	if err := db.Exec("CREATE TABLE nodes (id BIGINT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE eligibility_test_roots (id TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO nodes (id) VALUES (?)", harness.binding.TargetNodeID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO eligibility_test_roots (id) VALUES (?)", harness.binding.TargetRootID).Error; err != nil {
		t.Fatal(err)
	}
	harness.authority.targetRoot = eligibilityPlanLockTargetRoot{harness.root}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	holder := db.WithContext(ctx).Begin()
	if holder.Error != nil {
		cancel()
		t.Fatal(holder.Error)
	}
	holderOpen := true
	var done <-chan struct{}
	defer func() {
		cancel()
		if holderOpen {
			_ = holder.Rollback().Error
		}
		if done != nil {
			<-done
		}
	}()
	var holderPID int
	if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
		t.Fatal(err)
	}
	var held struct{ ID string }
	locked := holder.Table("backup_asset_recovery_plans").Select("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND binding_digest = ? AND transition_revision = ?",
			harness.binding.PlanID, harness.binding.PlanBindingDigest, harness.binding.PlanTransitionRevision).Take(&held)
	if locked.Error != nil || held.ID != harness.binding.PlanID {
		t.Fatalf("hold exact plan: id=%q error=%v", held.ID, locked.Error)
	}

	targetObserved := make(chan struct{}, 1)
	harness.target.afterObserve = func() { targetObserved <- struct{}{} }
	type observationResult struct {
		observation RecoveryAuthorityObservation
		err         error
	}
	result := make(chan observationResult, 1)
	finished := make(chan struct{})
	done = finished
	go func() {
		defer close(finished)
		observation, err := harness.authority.ObserveRecoveryAuthority(ctx, harness.binding)
		result <- observationResult{observation: observation, err: err}
	}()
	select {
	case <-targetObserved:
	case observed := <-result:
		t.Fatalf("observation finished before target observation: %v", observed.err)
	case <-ctx.Done():
		t.Fatalf("waiting for target observation: %v", ctx.Err())
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := db.WithContext(ctx).Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND ? = ANY(pg_blocking_pids(pid))
		)`, holderPID).Scan(&blocked).Error; err != nil {
			t.Fatalf("inspect plan lock wait: %v", err)
		}
		if blocked {
			break
		}
		select {
		case observed := <-result:
			t.Fatalf("observation finished before waiting on the plan: %v", observed.err)
		case <-ctx.Done():
			t.Fatalf("observation did not wait on the plan: %v", ctx.Err())
		case <-ticker.C:
		}
	}

	// The observer is blocked on the plan before acquiring either target row.
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range []struct {
			table string
			id    any
		}{
			{table: "nodes", id: harness.binding.TargetNodeID},
			{table: "eligibility_test_roots", id: harness.binding.TargetRootID},
		} {
			var found struct{ ID string }
			if err := tx.Table(row.table).Select("id").
				Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).
				Where("id = ?", row.id).Take(&found).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("target rows must remain independently lockable while waiting on plan: %v", err)
	}
	if err := holder.Commit().Error; err != nil {
		t.Fatalf("release plan: %v", err)
	}
	holderOpen = false
	select {
	case observed := <-result:
		if observed.err != nil {
			t.Fatalf("observe after plan release: %v", observed.err)
		}
		if observed.observation.proof == nil || !observed.observation.proof.production || observed.observation.proof.bindingDigest == "" {
			t.Fatal("observation after plan release was not sealed")
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return harness.authority.RevalidateRecoveryAuthorityTx(ctx, tx, harness.binding, observed.observation)
		}); err != nil {
			t.Fatalf("revalidate sealed observation: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for observation after releasing plan: %v", ctx.Err())
	}
}

func TestRecoveryEligibilityObserveRejectsPlanRevisionChangeAfterTargetObservation(t *testing.T) {
	harness := newRecoveryEligibilityHarness(t)
	var updateErr error
	harness.target.afterObserve = func() {
		updateErr = harness.authority.db.Table("backup_asset_recovery_plans").
			Where("id = ?", harness.binding.PlanID).
			Update("transition_revision", harness.binding.PlanTransitionRevision+1).Error
	}
	_, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
	if updateErr != nil {
		t.Fatalf("change persisted plan revision: %v", updateErr)
	}
	if !errors.Is(err, ErrRecoveryTargetChanged) {
		t.Fatalf("observation with changed plan revision: %v, want target changed", err)
	}
	if harness.source.lastClose == nil || harness.source.lastClose.closeCalls != 1 {
		t.Fatal("source capability was not closed after plan revision changed")
	}
}
