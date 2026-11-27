package recovery

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// waitRepositoryGateWaiter identifies the exact advisory key and holder, rather
// than inferring the wait from timing or from another lock in the transaction.
func waitRepositoryGateWaiter(t *testing.T, ctx context.Context, db *gorm.DB, holderPID int, repositoryID string, done <-chan struct{}) int {
	t.Helper()
	key := "xirang:recovery-authority-repository:v1:" + repositoryID
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiterPID int
		if err := db.WithContext(ctx).Raw(`SELECT COALESCE((
			SELECT waiting.pid FROM pg_locks waiting
			JOIN pg_locks held ON held.locktype = waiting.locktype AND held.database = waiting.database
				AND held.classid = waiting.classid AND held.objid = waiting.objid AND held.objsubid = waiting.objsubid
			WHERE waiting.locktype = 'advisory' AND NOT waiting.granted AND waiting.mode = 'ExclusiveLock'
				AND held.granted AND held.mode = 'ExclusiveLock' AND held.pid = ?
				AND held.pid = ANY(pg_blocking_pids(waiting.pid)) AND waiting.objsubid = 1
				AND waiting.classid::bigint = ((hashtextextended(?, 0) >> 32) & 4294967295)
				AND waiting.objid::bigint = (hashtextextended(?, 0) & 4294967295)
			LIMIT 1), 0)`, holderPID, key, key).Scan(&waiterPID).Error; err != nil {
			t.Fatalf("inspect exact repository advisory wait: %v", err)
		}
		if waiterPID != 0 {
			return waiterPID
		}
		select {
		case <-done:
			t.Fatal("operation finished before waiting on the repository gate")
		case <-ctx.Done():
			t.Fatalf("waiting for repository gate: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestRecoveryEligibilityRepositoryGatePostgresPlanCreation(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCount(t, AuthorizationReceiptWriteAuthorize, false, false, 0)
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var hasNodeFK bool
	if err := fixture.db.Raw(`SELECT EXISTS (
		SELECT 1 FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		WHERE c.contype = 'f' AND c.conrelid = 'backup_asset_recovery_plans'::regclass
			AND c.confrelid = 'nodes'::regclass AND a.attname = 'target_node_id'
	)`).Scan(&hasNodeFK).Error; err != nil || !hasNodeFK {
		t.Fatalf("migrated plan target Node foreign key: present=%t err=%v", hasNodeFK, err)
	}
	validator, err := NewSourceValidator(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("plan_id = ?", plan.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: plan.RepositoryID, RecoveryPointID: plan.RecoveryPointID, CatalogGenerationID: plan.CatalogGenerationID,
		AssetRefs: []backupasset.AssetRef{{RecoveryPointID: plan.RecoveryPointID, EntryID: item.EntryID}}, MaxItems: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := newRecoveryTargetRootResolverFake(t, plan.TargetNodeID, plan.TargetRootID, "/srv/FAKE_RECOVERY_TARGET_ROOT_FOR_TEST_ONLY")
	service, err := NewPlanService(PlanServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now }, TargetRootResolver: resolver,
		Policy:          PlanPolicy{MaxSelectionItems: exactSelectionMaxItems, MaxLogicalBytes: recoveryPlanMaxLogicalBytes},
		PreflightPolicy: PreflightPolicy{TTL: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := CreatePlanRequest{
		RequesterID: plan.RequesterID, Endpoint: "recovery_plan_create", IdempotencyKey: "repository-gate-plan-create-key",
		Selection: selection, AuthorityCategory: AuthorityWrite, EstimatedItems: plan.EstimatedItems, EstimatedBytes: plan.EstimatedBytes,
		Plan: RecoveryPlan{Binding: PlanBinding{
			SchemaVersion: 1, SelectionDigest: selection.SelectionDigest, RepositoryID: selection.RepositoryID,
			RecoveryPointID: selection.RecoveryPointID, SourceRevisionDigest: selection.SourceRevisionDigest, SourceRevision: selection.SourceRevision,
			Target: TargetBinding{Mode: TargetMode(plan.TargetMode), NodeID: plan.TargetNodeID, RootID: plan.TargetRootID,
				EncryptedRelativePath: plan.EncryptedTargetRelativePath, RootLocatorDigest: plan.RootLocatorDigest, PathDigest: plan.PathDigest,
				BaseNodeRevision: plan.TargetBaseRevision, CredentialScopeRevision: plan.CredentialScopeRevision,
				RootRevision: plan.RootRevision, FilesystemRevision: plan.FilesystemRevision},
			ConflictPolicy: ConflictPolicy(plan.ConflictPolicy), OperationSetDigest: plan.OperationSetDigest, DeleteSetDigest: plan.DeleteSetDigest,
			CapabilityRevision: plan.CapabilityRevision,
			SecurityDecision: SecurityDecision{Kind: SecurityDecisionKind(plan.SecurityDecision), DecisionDigest: plan.SecurityDecisionDigest,
				FindingSetDigest: plan.SecurityFindingSetDigest, PolicyRevision: plan.SecurityPolicyRevision},
			PreflightRevision: plan.PreflightRevision,
		}},
	}
	harness := newRecoveryEligibilityHarnessOnDB(t, fixture.db)
	harness.binding.TargetNodeID, harness.binding.TargetRootID = plan.TargetNodeID, plan.TargetRootID
	harness.binding.RecoveryPointID = plan.RecoveryPointID
	harness.binding.SourceRef.RecoveryPointID = plan.RecoveryPointID
	harness.source.binding = harness.binding
	harness.source.namespaceCaptured.sourceRef = harness.binding.SourceRef
	harness.source.namespaceDurable.(*lateRecoverySourceNamespaceDurable).current = harness.source.namespaceCaptured
	harness.authority.source = recoveryRepositoryGateSource{harness.source}
	harness.root.current.NodeID, harness.root.current.RootID = plan.TargetNodeID, plan.TargetRootID
	if err := fixture.db.Exec("CREATE TABLE eligibility_test_roots (id TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Exec("INSERT INTO eligibility_test_roots (id) VALUES (?)", plan.TargetRootID).Error; err != nil {
		t.Fatal(err)
	}
	barrier := &recoveryRepositoryGateBarrier{arrived: make(chan int, 1), release: make(chan struct{})}
	harness.authority.targetRoot = recoveryRepositoryGateTargetRoot{eligibilityPlanLockTargetRoot: eligibilityPlanLockTargetRoot{harness.root}, barrier: barrier}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	observed := make(chan error, 1)
	observeDone := make(chan struct{})
	created := make(chan error, 1)
	createDone := make(chan struct{})
	createStarted := false
	defer func() {
		cancel()
		barrier.unblock()
		<-observeDone
		if createStarted {
			<-createDone
		}
	}()
	go func() {
		defer close(observeDone)
		_, err := harness.authority.ObserveRecoveryAuthority(ctx, harness.binding)
		observed <- err
	}()
	var holderPID int
	select {
	case holderPID = <-barrier.arrived:
	case err := <-observed:
		t.Fatalf("observation finished before holding target: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	createStarted = true
	go func() {
		defer close(createDone)
		result, err := service.CreatePlan(ctx, request)
		if err == nil && (result.PlanID == "" || result.Replay) {
			err = errors.New("plan creation did not create a new plan")
		}
		created <- err
	}()
	waitRepositoryGateWaiter(t, ctx, fixture.db, holderPID, plan.RepositoryID, createDone)
	if err := fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range []struct{ table, id string }{{"backup_repositories", plan.RepositoryID}, {"recovery_points", plan.RecoveryPointID}} {
			var found struct{ ID string }
			if err := tx.Table(row.table).Select("id").Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("id = ?", row.id).Take(&found).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("plan creation must wait before locking repository or point: %v", err)
	}
	barrier.unblock()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatalf("observation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-created:
		if err != nil {
			t.Fatalf("create migrated-schema plan: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

type recoveryRepositoryGateAuthorizationObserver struct {
	*RecoveryEligibilityAuthority
	binding RecoveryAuthorityBinding
}

func (observer recoveryRepositoryGateAuthorizationObserver) ObserveRecoveryAuthority(ctx context.Context, _ RecoveryAuthorityBinding) (RecoveryAuthorityObservation, error) {
	return observer.RecoveryEligibilityAuthority.ObserveRecoveryAuthority(ctx, observer.binding)
}

func TestRecoveryEligibilityRepositoryGatePostgresAuthorizationStatementCancellation(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCount(t, AuthorizationReceiptWriteAuthorize, false, false, 0)
	harness := newRecoveryEligibilityHarnessOnDB(t, fixture.db)
	fixture.service.liveRevalidator = recoveryRepositoryGateAuthorizationObserver{RecoveryEligibilityAuthority: harness.authority, binding: harness.binding}
	before := fixture.effectCounts(t)
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
	key := "xirang:recovery-authority-repository:v1:" + harness.binding.RepositoryID
	if err := holder.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
		_ = holder.Rollback().Error
		cancel()
		t.Fatal(err)
	}
	result := make(chan error, 1)
	done := make(chan struct{})
	defer func() { cancel(); _ = holder.Rollback().Error; <-done }()
	go func() {
		defer close(done)
		_, err := fixture.service.Authorize(ctx, fixture.request)
		result <- err
	}()
	waiterPID := waitRepositoryGateWaiter(t, ctx, fixture.db, holderPID, harness.binding.RepositoryID, done)
	var canceled bool
	if err := fixture.db.WithContext(ctx).Raw("SELECT pg_cancel_backend(?)", waiterPID).Scan(&canceled).Error; err != nil || !canceled {
		t.Fatalf("cancel observer statement: canceled=%t err=%v", canceled, err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrAuthorizationUnavailable) || errors.Is(err, ErrAuthorizationDenied) {
			t.Fatalf("authorization after observer SQL cancellation = %v, want unavailable", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if after := fixture.effectCounts(t); after != before {
		t.Fatalf("observer SQL cancellation left effects: before=%+v after=%+v", before, after)
	}
}

func TestRecoveryEligibilityRepositoryGatePostgresAuthorizationExpiry(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCount(t, AuthorizationReceiptWriteAuthorize, false, false, 0)
	var now atomic.Int64
	now.Store(fixture.now.UnixNano())
	fixture.dependencies.Now = func() time.Time { return time.Unix(0, now.Load()).UTC() }
	fixture.service = fixture.restartService(t)
	before := fixture.effectCounts(t)
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
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
	result := make(chan error, 1)
	done := make(chan struct{})
	defer func() { cancel(); _ = holder.Rollback().Error; <-done }()
	go func() {
		defer close(done)
		_, err := fixture.service.Authorize(ctx, fixture.request)
		result <- err
	}()
	waitRepositoryGateWaiter(t, ctx, fixture.db, holderPID, plan.RepositoryID, done)
	now.Store(fixture.request.Proof.ExpiresAt.UnixNano())
	if err := holder.Commit().Error; err != nil {
		t.Fatalf("release repository gate: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrAuthorizationDenied) {
			t.Fatalf("authorization after proof expiry while waiting = %v, want denied", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if after := fixture.effectCounts(t); after != before {
		t.Fatalf("expired authorization left effects: before=%+v after=%+v", before, after)
	}
	var afterPlan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", plan.ID).Take(&afterPlan).Error; err != nil {
		t.Fatal(err)
	}
	if afterPlan.State != plan.State || afterPlan.TransitionRevision != plan.TransitionRevision {
		t.Fatalf("expired authorization changed plan state or revision: before=%s/%d after=%s/%d",
			plan.State, plan.TransitionRevision, afterPlan.State, afterPlan.TransitionRevision)
	}
}
