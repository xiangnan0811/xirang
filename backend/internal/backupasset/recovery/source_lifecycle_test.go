package recovery

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestRecoveryPointSourceLifecycleRecoveryCancelsExactPointInterests(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "source-lifecycle-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim recovery source job: job_id=%q attempt_id=%q found=%t err=%v",
			claim.JobID, claim.AttemptID, found, err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.AutoMigrate(&model.RecoveryPointLifecycleAttempt{}); err != nil {
		t.Fatalf("migrate lifecycle attempt: %v", err)
	}
	lifecycleAttemptID := strings.Repeat("f", 32)
	if err := fixture.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: lifecycleAttemptID, RecoveryPointID: plan.RecoveryPointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}

	resultSetID, resultID := strings.Repeat("d", 32), strings.Repeat("e", 32)
	resultSet := model.BackupAssetRecoveryResultSet{
		ID: resultSetID, JobID: job.ID, State: string(ResultSetStateReady),
		MarkerBindingDigest: strings.Repeat("a", 64), PlaintextDeadline: fixture.now.AddDate(0, 0, 1),
		HardDeadline: fixture.now.AddDate(0, 0, 2), CleanupPhase: string(CleanupPhaseClaimed),
	}
	if err := fixture.db.Create(&resultSet).Error; err != nil {
		t.Fatalf("seed RecoveryResult set: %v", err)
	}
	result := model.BackupAssetRecoveryResult{
		ID: resultID, ResultSetID: resultSetID, JobID: job.ID,
		ResultKind: string(RecoveryResultKindRegularFile), Classification: "private",
		ClassificationRevision: 1, ClassificationSourceRevision: 1,
		EncryptedRelativeLocator: "results/private", LocatorDigest: strings.Repeat("b", 64),
	}
	if err := fixture.db.Create(&result).Error; err != nil {
		t.Fatalf("seed RecoveryResult: %v", err)
	}

	owner, err := NewSourceLifecycle(fixture.db, coordinator, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: plan.RecoveryPointID, LifecycleAttemptID: lifecycleAttemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	}
	if err := owner.CancelRecoveryPointInterests(context.Background(), request); err != nil {
		t.Fatalf("prepare Recovery source lifecycle: %v", err)
	}
	assertRecoverySourceLifecycleSettled(t, fixture, claim, resultSet, result)

	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ?", lifecycleAttemptID).Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance lifecycle attempt: %v", err)
	}
	request.Stage = backupasset.SourceLifecycleCleanup
	if err := owner.CancelRecoveryPointInterests(context.Background(), request); err != nil {
		t.Fatalf("cleanup Recovery source lifecycle: %v", err)
	}
	if err := owner.CancelRecoveryPointInterests(context.Background(), request); err != nil {
		t.Fatalf("idempotent Recovery source cleanup: %v", err)
	}
	assertRecoverySourceLifecycleSettled(t, fixture, claim, resultSet, result)
}

func TestRecoveryPointSourceLifecycleRecoveryBindsAttemptPlanAndExactPointLease(t *testing.T) {
	t.Run("phase drift before cancellation leaves Recovery authority unchanged", func(t *testing.T) {
		fixture, coordinator, claim, request := newRecoverySourceLifecycleCancellationFixture(t)
		before := loadRecoverySourceCancellationState(t, fixture.db, claim)
		driftingCanceler := &recoverySourceLifecycleDriftCanceler{
			db: coordinator.db, delegate: coordinator,
			drift: func(tx *gorm.DB) error {
				return tx.Model(&model.RecoveryPointLifecycleAttempt{}).
					Where("id = ?", request.LifecycleAttemptID).
					Update("phase", backupasset.LifecyclePhaseDraining).Error
			},
		}
		owner, err := NewSourceLifecycle(fixture.db, driftingCanceler, 16)
		if err != nil {
			t.Fatalf("NewSourceLifecycle: %v", err)
		}
		err = owner.CancelRecoveryPointInterests(context.Background(), request)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Errorf("phase-drift cancellation error=%v, want conflict", err)
		}
		after := loadRecoverySourceCancellationState(t, fixture.db, claim)
		if after != before {
			t.Errorf("phase-drift cancellation changed Recovery authority: before={%s} after={%s}",
				recoverySourceCancellationStateDiagnostic(before), recoverySourceCancellationStateDiagnostic(after))
		}
	})

	t.Run("attempt drift before cancellation leaves Recovery authority unchanged", func(t *testing.T) {
		fixture, coordinator, claim, request := newRecoverySourceLifecycleCancellationFixture(t)
		before := loadRecoverySourceCancellationState(t, fixture.db, claim)
		replacementAttemptID := strings.Repeat("7", 32)
		driftingCanceler := &recoverySourceLifecycleDriftCanceler{
			db: coordinator.db, delegate: coordinator,
			drift: func(tx *gorm.DB) error {
				return tx.Model(&model.RecoveryPointLifecycleAttempt{}).
					Where("id = ?", request.LifecycleAttemptID).
					UpdateColumn("id", replacementAttemptID).Error
			},
		}
		owner, err := NewSourceLifecycle(fixture.db, driftingCanceler, 16)
		if err != nil {
			t.Fatalf("NewSourceLifecycle: %v", err)
		}
		err = owner.CancelRecoveryPointInterests(context.Background(), request)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Errorf("attempt-drift cancellation error=%v, want conflict", err)
		}
		after := loadRecoverySourceCancellationState(t, fixture.db, claim)
		if after != before {
			t.Errorf("attempt-drift cancellation changed Recovery authority: before={%s} after={%s}",
				recoverySourceCancellationStateDiagnostic(before), recoverySourceCancellationStateDiagnostic(after))
		}
	})

	t.Run("point A job cannot release point B lease", func(t *testing.T) {
		fixture, coordinator, claim, request := newRecoverySourceLifecycleCancellationFixture(t)
		var pointA model.RecoveryPoint
		if err := fixture.db.Where("id = ?", request.RecoveryPointID).Take(&pointA).Error; err != nil {
			t.Fatal(err)
		}
		pointB := pointA
		pointB.ID = strings.Repeat("9", 32)
		pointB.SourceFingerprint = strings.Repeat("8", 64)
		pointB.ManifestDigest = strings.Repeat("7", 64)
		pointB.EncryptedProviderLocator = "FAKE_CROSS_POINT_RECOVERY_LOCATOR_FOR_TEST_ONLY"
		if err := fixture.db.Create(&pointB).Error; err != nil {
			t.Fatalf("seed point B: %v", err)
		}
		if err := fixture.db.Model(&model.RecoveryPointLease{}).
			Where("id = ?", claim.SourceFence.LeaseID).
			UpdateColumn("recovery_point_id", pointB.ID).Error; err != nil {
			t.Fatalf("cross source lease onto point B: %v", err)
		}
		before := loadRecoverySourceCancellationState(t, fixture.db, claim)
		owner, err := NewSourceLifecycle(fixture.db, coordinator, 16)
		if err != nil {
			t.Fatalf("NewSourceLifecycle: %v", err)
		}
		err = owner.CancelRecoveryPointInterests(context.Background(), request)
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Errorf("cross-point cancellation error=%v, want conflict", err)
		}
		after := loadRecoverySourceCancellationState(t, fixture.db, claim)
		if after != before {
			t.Errorf("point A cancellation changed point B Recovery authority: before={%s} after={%s}",
				recoverySourceCancellationStateDiagnostic(before), recoverySourceCancellationStateDiagnostic(after))
		}
	})
}

func TestRecoveryPointSourceLifecycleRecoveryDiagnosticsUseClosedFields(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Recovery source lifecycle test source")
	}
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, sourcePath, nil, 0)
	if err != nil {
		t.Fatalf("parse Recovery source lifecycle test source: %v", err)
	}

	requiredKinds := []string{
		"RecoveryWorkerClaim",
		"recoverySourceCancellationState",
		"BackupAssetRecoveryJob",
		"RecoveryPointLease",
	}
	foundKinds := make(map[string]bool)
	var violations []string
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil || function.Name.Name == "TestRecoveryPointSourceLifecycleRecoveryDiagnosticsUseClosedFields" {
			continue
		}
		functionKinds, functionViolations := recoveryPrivateDiagnosticViolations(files, parsed, function)
		for kind := range functionKinds {
			foundKinds[kind] = true
		}
		violations = append(violations, functionViolations...)
	}
	for _, kind := range requiredKinds {
		if !foundKinds[kind] {
			t.Fatalf("Recovery source lifecycle privacy guard did not classify %s", kind)
		}
	}
	if len(violations) != 0 {
		t.Fatalf("Recovery source lifecycle test has private full-value diagnostics at %v; use closed scalar fields", violations)
	}

	const canarySource = `package recovery

func recoverySourcePreserve[T any](value T) T {
	return value
}

func identity[T any](value T) T {
	return value
}

func recoverySourceLoadClaim() RecoveryWorkerClaim {
	return RecoveryWorkerClaim{}
}

func recoverySourceLoadState() recoverySourceCancellationState {
	return recoverySourceCancellationState{}
}

func recoverySourcePrivateDiagnosticCanary(t *testing.T, dynamicFormat string, claims []RecoveryWorkerClaim, state recoverySourceCancellationState, safeErr error) {
	claim := claims[0]
	alias := claim
	pointer := &state
	leaked := identity(claim)
	preserved := recoverySourcePreserve(claim)
	loadedClaim := recoverySourceLoadClaim()
	loadedState := recoverySourceLoadState()
	binary := claim.SourceFence.FenceToken + "-suffix"
	formatter := t.Errorf
	t.Errorf("claim alias=%v", alias)
	t.Fatalf("claim authority selector=%+v", claim.SourceFence)
	t.Logf("claim index=%[1]v", claims[0])
	fmt.Printf("state pointer=%#v", pointer)
	_ = fmt.Sprintf(dynamicFormat, identity[RecoveryWorkerClaim](RecoveryWorkerClaim(claim)))
	formatter("formatter alias=%v", claim)
	t.Errorf("identity assignment=%v", leaked)
	formatter = t.Fatalf
	formatter("binary formatter reassignment=%v", binary)
	formatter(dynamicFormat, claim)
	t.Errorf("claim string=%s", claim)
	t.Fatalf("claim selector quote=%q", claim.SourceFence)
	fmt.Printf("claim authority hex=%x", claim.SourceFence.FenceToken)
	t.Logf("preserved claim string=%s", preserved)
	t.Errorf("typed claim quote=%q", loadedClaim)
	t.Errorf("typed state hex=%x", loadedState)
	convertedAny := any(claim)
	convertedString := string(claim.SourceFence.FenceToken)
	t.Errorf("any conversion=%s", convertedAny)
	t.Errorf("string conversion=%q", convertedString)
	t.Errorf("star width=%*s", 4, claim)
	t.Errorf("star precision=%.*q", 3, claim.SourceFence)
	t.Errorf("unknown verb=%Z", claim)
	t.Errorf("invalid trailing percent=%", claim)
	t.Errorf("private star width=%*s", claim.AttemptFence, claim.JobID)
	t.Errorf("private star precision=%.*q", claim.NodeFence, state.jobState)
	t.Errorf("type extra=%T", claim, state)
	t.Errorf("percent extra=%%", claim)
	t.Errorf("multiple extra type=%T", claim, safeErr, state, loadedClaim)
	recoverySourceReportOuter(t, claim)
	privateKeyMap := map[RecoveryWorkerClaim]int{claim: 1}
	_, relayedKey := recoverySourceRelay(privateKeyMap)
	privateValueMap := map[string]recoverySourceCancellationState{"state": state}
	_, relayedValue := recoverySourceRelay(privateValueMap)
	t.Errorf("second result private map key=%v", relayedKey)
	t.Errorf("second result private map value=%v", relayedValue)
	_, joinedKey := recoverySourceJoinRelay(claim.JobID, privateKeyMap)
	_, joinedValue := recoverySourceJoinRelay(state.jobState, privateValueMap)
	t.Errorf("multi-source private map key=%v", joinedKey)
	t.Errorf("multi-source private map value=%v", joinedValue)
	for _, rangedSliceValue := range []any{claim} {
		t.Errorf("range slice Errorf=%v", rangedSliceValue)
		t.Fatalf("range slice Fatalf=%v", rangedSliceValue)
		t.Logf("range slice Logf=%v", rangedSliceValue)
		fmt.Printf("range slice Printf=%v", rangedSliceValue)
		_ = fmt.Sprintf("range slice Sprintf=%v", rangedSliceValue)
	}
	for rangedMapKey, rangedMapValue := range map[RecoveryWorkerClaim]recoverySourceCancellationState{claim: state} {
		t.Errorf("range map key=%v", rangedMapKey)
		fmt.Printf("range map value=%v", rangedMapValue)
	}
	for safeRangeIndex, safeRangeValue := range []string{claim.JobID} {
		t.Errorf("safe range index=%d value=%s", safeRangeIndex, safeRangeValue)
	}
	_, safeJoined := recoverySourceJoinRelay(claim.JobID, state.jobState)
	t.Errorf("safe joined scalar=%v", safeJoined)
	safeCount, safeTypedErr := recoverySourceSafePair(claim)
	t.Errorf("safe typed returns count=%d err=%v", safeCount, safeTypedErr)
	t.Logf("claim type=%*T precision=%.*T percent=%%", 4, claim, 3, claim)
	t.Errorf("safe id=%q state=%s revision=%d count=%d present=%t equal=%t",
		claim.JobID, state.jobState, claim.TransitionRevision, len(claims),
		claim.SourceFence.FenceToken != "", claim == loadedClaim)
	t.Errorf("safe star id=%*s state=%.*q unknown=%Z", 4, claim.JobID, 3, state.jobState, claim.TransitionRevision)
	t.Errorf("safe extras=%T %%", claim, claim.JobID, state.jobState)
	fmt.Printf(dynamicFormat, safeErr)
	ordinaryErr := validateClaim(claim)
	t.Errorf("ordinary error=%v", ordinaryErr)
}

func recoverySourceReportOuter[T any](t *testing.T, value T) {
	recoverySourceReportInner(t, any(value))
}

func recoverySourceReportInner(t *testing.T, value any) {
	t.Errorf("two-layer helper private=%s", value)
}

func recoverySourceCarry[T any](value T) (int, any) {
	return 1, value
}

func recoverySourceRelay[T any](value T) (int, any) {
	_, carried := recoverySourceCarry(value)
	return 2, carried
}

func recoverySourceSafePair[T any](value T) (int, error) {
	return 1, nil
}

func recoverySourceJoin[A, B any](first A, second B, firstBranch bool) (int, any) {
	if firstBranch {
		return 1, first
	}
	return 2, second
}

func recoverySourceJoinRelay[A, B any](first A, second B) (int, any) {
	_, carried := recoverySourceJoin(first, second, false)
	return 3, carried
}`
	canaryFiles := token.NewFileSet()
	canaryParsed, err := parser.ParseFile(canaryFiles, "recovery_source_private_diagnostic_canary.go", canarySource, 0)
	if err != nil {
		t.Fatalf("parse Recovery source private diagnostic canary: %v", err)
	}
	var canary *ast.FuncDecl
	for _, declaration := range canaryParsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "recoverySourcePrivateDiagnosticCanary" {
			canary = function
			break
		}
	}
	if canary == nil || canary.Body == nil {
		t.Fatal("locate Recovery source private diagnostic canary")
	}
	canaryKinds, canaryViolations := recoveryPrivateDiagnosticViolations(canaryFiles, canaryParsed, canary)
	for _, kind := range []string{"RecoveryWorkerClaim", "recoverySourceCancellationState"} {
		if !canaryKinds[kind] {
			t.Fatalf("Recovery source private diagnostic canary did not classify %s", kind)
		}
	}
	wantCanaryViolations := []string{
		"RecoveryWorkerClaim:29",
		"RecoveryWorkerClaim:30",
		"RecoveryWorkerClaim:31",
		"recoverySourceCancellationState:32",
		"RecoveryWorkerClaim:33",
		"RecoveryWorkerClaim:34",
		"RecoveryWorkerClaim:35",
		"RecoveryWorkerClaim:37",
		"RecoveryWorkerClaim:38",
		"RecoveryWorkerClaim:39",
		"RecoveryWorkerClaim:40",
		"RecoveryWorkerClaim:41",
		"RecoveryWorkerClaim:42",
		"RecoveryWorkerClaim:43",
		"recoverySourceCancellationState:44",
		"RecoveryWorkerClaim:47",
		"RecoveryWorkerClaim:48",
		"RecoveryWorkerClaim:49",
		"RecoveryWorkerClaim:50",
		"RecoveryWorkerClaim:51",
		"RecoveryWorkerClaim:52",
		"RecoveryWorkerClaim:53",
		"RecoveryWorkerClaim:54",
		"recoverySourceCancellationState:55",
		"RecoveryWorkerClaim:56",
		"recoverySourceCancellationState:57",
		"RecoveryWorkerClaim:57",
		"RecoveryWorkerClaim:63",
		"recoverySourceCancellationState:64",
		"RecoveryWorkerClaim:67",
		"recoverySourceCancellationState:68",
		"RecoveryWorkerClaim:70",
		"RecoveryWorkerClaim:71",
		"RecoveryWorkerClaim:72",
		"RecoveryWorkerClaim:73",
		"RecoveryWorkerClaim:74",
		"RecoveryWorkerClaim:77",
		"recoverySourceCancellationState:78",
		"RecoveryWorkerClaim:103",
	}
	if strings.Join(canaryViolations, "\n") != strings.Join(wantCanaryViolations, "\n") {
		t.Fatalf("Recovery source privacy guard canary violations=%v, want %v", canaryViolations, wantCanaryViolations)
	}
}

func TestRecoveryPointSourceLifecycleRecoveryWorkerClaimDiagnosticsUseClosedFields(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Recovery source lifecycle test source")
	}
	workerTestPath := filepath.Join(filepath.Dir(sourcePath), "worker_test.go")
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, workerTestPath, nil, 0)
	if err != nil {
		t.Fatalf("parse Recovery worker test source: %v", err)
	}

	const targetTest = "TestRecoveryWorkerCancelRecoveryPointPreservesQueuedRunningAndMutationArmedSemantics"
	var target *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == targetTest {
			target = function
			break
		}
	}
	if target == nil || target.Body == nil {
		t.Fatalf("locate %s in Recovery worker test source", targetTest)
	}
	foundKinds, violations := recoveryPrivateDiagnosticViolations(files, parsed, target)
	for _, kind := range []string{"RecoveryWorkerClaim", "recoveryWorkspaceCleanupTuple"} {
		if !foundKinds[kind] {
			t.Fatalf("locate %s binding in %s", kind, targetTest)
		}
	}
	if len(violations) != 0 {
		t.Fatalf("Recovery source-cancellation test has full private diagnostics at %v; use closed scalar fields", violations)
	}

	const canarySource = `package recovery

func recoveryWorkerPreserve[T any](value T) T {
	return value
}

func identity[T any](value T) T {
	return value
}

func recoveryWorkerLoadCleanup() recoveryWorkspaceCleanupTuple {
	return recoveryWorkspaceCleanupTuple{}
}

func recoveryPrivateDiagnosticCanary(t *testing.T, coordinator *WorkerCoordinator, job model.BackupAssetRecoveryJob) {
	claim, _, _ := coordinator.ClaimNext(context.Background(), "worker")
	cleanup := recoveryWorkspaceCleanupTupleForTest(job)
	claimAlias := claim
	cleanupAliases := []recoveryWorkspaceCleanupTuple{cleanup}
	preserved := recoveryWorkerPreserve(claim)
	loadedCleanup := recoveryWorkerLoadCleanup()
	t.Errorf("claim alias=%v", claimAlias)
	t.Fatalf("claim selector=%+v", claim.SourceFence)
	t.Logf("cleanup index=%#v", cleanupAliases[0])
	fmt.Printf("cleanup call=%v", identity(cleanup))
	_ = fmt.Sprintf("claim call selector index=%v", identity([]RecoveryWorkerClaim{claim}[0].SourceFence))
	t.Errorf("claim string=%s", claim)
	t.Fatalf("claim selector quote=%q", claim.SourceFence)
	fmt.Printf("cleanup hex=%x", cleanup)
	t.Logf("preserved claim string=%s", preserved)
	t.Errorf("typed cleanup quote=%q", loadedCleanup)
	convertedAny := any(cleanup)
	convertedString := string(claim.SourceFence.FenceToken)
	t.Errorf("any conversion=%s", convertedAny)
	t.Errorf("string conversion=%q", convertedString)
	t.Errorf("star width=%*s", 4, claim)
	t.Errorf("star precision=%.*q", 3, cleanup)
	t.Errorf("unknown verb=%Z", cleanup)
	t.Errorf("invalid trailing percent=%", claim)
	t.Errorf("private star width=%*s", claim.AttemptFence, claim.JobID)
	t.Errorf("private star precision=%.*q", cleanup.fence, cleanup.phase)
	t.Errorf("type extra=%T", claim, cleanup)
	t.Errorf("percent extra=%%", claim)
	t.Errorf("multiple extra type=%T", claim, job.ID, cleanup, loadedCleanup)
	recoveryWorkerReportOuter(t, cleanup)
	privateKeyMap := map[RecoveryWorkerClaim]int{claim: 1}
	_, relayedKey := recoveryWorkerRelay(privateKeyMap)
	privateValueMap := map[string]recoveryWorkspaceCleanupTuple{"cleanup": cleanup}
	_, relayedValue := recoveryWorkerRelay(privateValueMap)
	t.Errorf("second result private map key=%v", relayedKey)
	t.Errorf("second result private map value=%v", relayedValue)
	_, joinedKey := recoveryWorkerJoinRelay(claim.JobID, privateKeyMap)
	_, joinedValue := recoveryWorkerJoinRelay(cleanup.phase, privateValueMap)
	t.Errorf("multi-source private map key=%v", joinedKey)
	t.Errorf("multi-source private map value=%v", joinedValue)
	for _, rangedSliceValue := range []any{cleanup} {
		t.Errorf("range slice Errorf=%v", rangedSliceValue)
		t.Fatalf("range slice Fatalf=%v", rangedSliceValue)
		t.Logf("range slice Logf=%v", rangedSliceValue)
		fmt.Printf("range slice Printf=%v", rangedSliceValue)
		_ = fmt.Sprintf("range slice Sprintf=%v", rangedSliceValue)
	}
	for rangedMapKey, rangedMapValue := range map[RecoveryWorkerClaim]recoveryWorkspaceCleanupTuple{claim: cleanup} {
		t.Errorf("range map key=%v", rangedMapKey)
		fmt.Printf("range map value=%v", rangedMapValue)
	}
	for safeRangeIndex, safeRangeValue := range []string{claim.JobID} {
		t.Errorf("safe range index=%d value=%s", safeRangeIndex, safeRangeValue)
	}
	_, safeJoined := recoveryWorkerJoinRelay(claim.JobID, cleanup.phase)
	t.Errorf("safe joined scalar=%v", safeJoined)
	safeCount, safeTypedErr := recoveryWorkerSafePair(claim)
	t.Errorf("safe typed returns count=%d err=%v", safeCount, safeTypedErr)
	t.Logf("claim type=%*T precision=%.*T percent=%%", 4, claim, 3, claim)
	t.Errorf("safe id=%q phase=%s count=%d present=%t equal=%t",
		claim.JobID, cleanup.phase, len(cleanupAliases), cleanup.owner != "", cleanup.phase == loadedCleanup.phase)
	t.Errorf("safe star id=%*s phase=%.*q unknown=%Z", 4, claim.JobID, 3, cleanup.phase, len(cleanupAliases))
	t.Errorf("safe extras=%T %%", claim, claim.JobID, cleanup.phase)
}

func recoveryWorkerReportOuter[T any](t *testing.T, value T) {
	recoveryWorkerReportInner(t, any(value))
}

func recoveryWorkerReportInner(t *testing.T, value any) {
	t.Errorf("two-layer helper private=%s", value)
}

func recoveryWorkerCarry[T any](value T) (int, any) {
	return 1, value
}

func recoveryWorkerRelay[T any](value T) (int, any) {
	_, carried := recoveryWorkerCarry(value)
	return 2, carried
}

func recoveryWorkerSafePair[T any](value T) (int, error) {
	return 1, nil
}

func recoveryWorkerJoin[A, B any](first A, second B, firstBranch bool) (int, any) {
	if firstBranch {
		return 1, first
	}
	return 2, second
}

func recoveryWorkerJoinRelay[A, B any](first A, second B) (int, any) {
	_, carried := recoveryWorkerJoin(first, second, false)
	return 3, carried
}`
	canaryFiles := token.NewFileSet()
	canaryParsed, err := parser.ParseFile(canaryFiles, "recovery_private_diagnostic_canary.go", canarySource, 0)
	if err != nil {
		t.Fatalf("parse Recovery private diagnostic canary: %v", err)
	}
	var canary *ast.FuncDecl
	for _, declaration := range canaryParsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "recoveryPrivateDiagnosticCanary" {
			canary = function
			break
		}
	}
	if canary == nil || canary.Body == nil {
		t.Fatal("locate Recovery private diagnostic canary")
	}
	canaryKinds, canaryViolations := recoveryPrivateDiagnosticViolations(canaryFiles, canaryParsed, canary)
	for _, kind := range []string{"RecoveryWorkerClaim", "recoveryWorkspaceCleanupTuple"} {
		if !canaryKinds[kind] {
			t.Fatalf("Recovery private diagnostic canary did not classify %s", kind)
		}
	}
	wantCanaryViolations := []string{
		"RecoveryWorkerClaim:22",
		"RecoveryWorkerClaim:23",
		"recoveryWorkspaceCleanupTuple:24",
		"recoveryWorkspaceCleanupTuple:25",
		"RecoveryWorkerClaim:26",
		"RecoveryWorkerClaim:27",
		"RecoveryWorkerClaim:28",
		"recoveryWorkspaceCleanupTuple:29",
		"RecoveryWorkerClaim:30",
		"recoveryWorkspaceCleanupTuple:31",
		"recoveryWorkspaceCleanupTuple:34",
		"RecoveryWorkerClaim:35",
		"RecoveryWorkerClaim:36",
		"recoveryWorkspaceCleanupTuple:37",
		"recoveryWorkspaceCleanupTuple:38",
		"RecoveryWorkerClaim:39",
		"RecoveryWorkerClaim:40",
		"recoveryWorkspaceCleanupTuple:41",
		"recoveryWorkspaceCleanupTuple:42",
		"RecoveryWorkerClaim:43",
		"recoveryWorkspaceCleanupTuple:44",
		"recoveryWorkspaceCleanupTuple:44",
		"RecoveryWorkerClaim:50",
		"recoveryWorkspaceCleanupTuple:51",
		"RecoveryWorkerClaim:54",
		"recoveryWorkspaceCleanupTuple:55",
		"recoveryWorkspaceCleanupTuple:57",
		"recoveryWorkspaceCleanupTuple:58",
		"recoveryWorkspaceCleanupTuple:59",
		"recoveryWorkspaceCleanupTuple:60",
		"recoveryWorkspaceCleanupTuple:61",
		"RecoveryWorkerClaim:64",
		"recoveryWorkspaceCleanupTuple:65",
		"recoveryWorkspaceCleanupTuple:86",
	}
	if strings.Join(canaryViolations, "\n") != strings.Join(wantCanaryViolations, "\n") {
		t.Fatalf("Recovery privacy guard canary violations=%v, want %v", canaryViolations, wantCanaryViolations)
	}
}

func recoveryPrivateDiagnosticViolations(
	files *token.FileSet,
	source *ast.File,
	target *ast.FuncDecl,
) (map[string]bool, []string) {
	functions := recoveryPrivateFunctionSummaries(source)
	states := map[*ast.FuncDecl]*recoveryPrivateFunctionState{
		target: recoveryPrivateNewFunctionState(target, functions),
	}
	for changed := true; changed; {
		changed = false
		for _, declaration := range source.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			state := states[function]
			if state == nil {
				continue
			}
			if recoveryPrivatePropagateFunctionState(state) {
				changed = true
			}
			if recoveryPrivatePropagateFunctionResults(state, functions) {
				changed = true
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := recoveryPrivateLocalCallName(call.Fun)
				summary, ok := functions[name]
				if !ok || summary.declaration == nil {
					return true
				}
				callee := states[summary.declaration]
				for index, argument := range call.Args {
					if index >= len(summary.parameterNames) || summary.parameterNames[index] == "" {
						continue
					}
					kind := recoveryPrivateExpressionKind(argument, &state.context, true)
					if kind == "" {
						continue
					}
					if callee == nil {
						callee = recoveryPrivateNewFunctionState(summary.declaration, functions)
						states[summary.declaration] = callee
						changed = true
					}
					parameter := summary.parameterNames[index]
					if callee.context.privateVariables[parameter] == "" {
						callee.context.privateVariables[parameter] = kind
						changed = true
					}
				}
				return true
			})
		}
	}
	foundKinds := make(map[string]bool)
	for _, state := range states {
		for _, kind := range state.context.privateVariables {
			foundKinds[kind] = true
		}
	}

	var violations []string
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		state := states[function]
		if state == nil {
			continue
		}
		violations = append(violations, recoveryPrivateFunctionFormatViolations(files, state)...)
	}
	return foundKinds, violations
}

type recoveryPrivateFunctionState struct {
	function         *ast.FuncDecl
	context          recoveryPrivateExpressionContext
	formatterAliases map[string]bool
}

func recoveryPrivateNewFunctionState(
	function *ast.FuncDecl,
	functions map[string]recoveryPrivateFunctionSummary,
) *recoveryPrivateFunctionState {
	return &recoveryPrivateFunctionState{
		function: function,
		context: recoveryPrivateExpressionContext{
			privateVariables: make(map[string]string),
			functions:        functions,
		},
		formatterAliases: make(map[string]bool),
	}
}

func recoveryPrivatePropagateFunctionState(state *recoveryPrivateFunctionState) bool {
	changedAny := false
	if state.function.Type.Params != nil {
		for _, parameter := range state.function.Type.Params.List {
			kind := recoveryPrivateTypeKind(parameter.Type)
			for _, name := range parameter.Names {
				if kind != "" && state.context.privateVariables[name.Name] == "" {
					state.context.privateVariables[name.Name] = kind
					changedAny = true
				}
			}
		}
	}
	ast.Inspect(state.function.Body, func(node ast.Node) bool {
		switch declaration := node.(type) {
		case *ast.AssignStmt:
			if len(declaration.Lhs) == 0 {
				return true
			}
			for _, expression := range declaration.Rhs {
				call, ok := expression.(*ast.CallExpr)
				if !ok {
					continue
				}
				variable, ok := declaration.Lhs[0].(*ast.Ident)
				if !ok {
					continue
				}
				switch function := call.Fun.(type) {
				case *ast.SelectorExpr:
					if function.Sel.Name == "ClaimNext" && state.context.privateVariables[variable.Name] == "" {
						state.context.privateVariables[variable.Name] = "RecoveryWorkerClaim"
						changedAny = true
					}
				case *ast.Ident:
					if function.Name == "recoveryWorkspaceCleanupTupleForTest" && state.context.privateVariables[variable.Name] == "" {
						state.context.privateVariables[variable.Name] = "recoveryWorkspaceCleanupTuple"
						changedAny = true
					}
				}
			}
		case *ast.ValueSpec:
			if kind := recoveryPrivateTypeKind(declaration.Type); kind != "" {
				for _, variable := range declaration.Names {
					if state.context.privateVariables[variable.Name] == "" {
						state.context.privateVariables[variable.Name] = kind
						changedAny = true
					}
				}
			}
		}
		return true
	})
	for changed := true; changed; {
		changed = false
		ast.Inspect(state.function.Body, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.AssignStmt:
				for index, left := range declaration.Lhs {
					variable, ok := left.(*ast.Ident)
					if !ok || len(declaration.Rhs) == 0 {
						continue
					}
					rightIndex := index
					if len(declaration.Rhs) == 1 {
						rightIndex = 0
					}
					if rightIndex >= len(declaration.Rhs) {
						continue
					}
					kind := recoveryPrivateAssignedExpressionKind(declaration.Rhs, index, &state.context)
					if kind != "" && state.context.privateVariables[variable.Name] == "" {
						state.context.privateVariables[variable.Name] = kind
						changed = true
						changedAny = true
					}
					if recoveryPrivateFormatterExpression(declaration.Rhs[rightIndex], state.formatterAliases) && !state.formatterAliases[variable.Name] {
						state.formatterAliases[variable.Name] = true
						changed = true
						changedAny = true
					}
				}
			case *ast.RangeStmt:
				keyKind, valueKind := recoveryPrivateRangeBindingKinds(declaration.X, &state.context)
				for binding, kind := range map[ast.Expr]string{
					declaration.Key:   keyKind,
					declaration.Value: valueKind,
				} {
					variable, ok := binding.(*ast.Ident)
					if !ok || variable.Name == "_" || kind == "" || state.context.privateVariables[variable.Name] != "" {
						continue
					}
					state.context.privateVariables[variable.Name] = kind
					changed = true
					changedAny = true
				}
			case *ast.ValueSpec:
				for index, variable := range declaration.Names {
					if len(declaration.Values) == 0 {
						continue
					}
					valueIndex := index
					if len(declaration.Values) == 1 {
						valueIndex = 0
					}
					if valueIndex >= len(declaration.Values) {
						continue
					}
					kind := recoveryPrivateAssignedExpressionKind(declaration.Values, index, &state.context)
					if kind != "" && state.context.privateVariables[variable.Name] == "" {
						state.context.privateVariables[variable.Name] = kind
						changed = true
						changedAny = true
					}
					if recoveryPrivateFormatterExpression(declaration.Values[valueIndex], state.formatterAliases) && !state.formatterAliases[variable.Name] {
						state.formatterAliases[variable.Name] = true
						changed = true
						changedAny = true
					}
				}
			}
			return true
		})
	}
	return changedAny
}

func recoveryPrivatePropagateFunctionResults(
	state *recoveryPrivateFunctionState,
	functions map[string]recoveryPrivateFunctionSummary,
) bool {
	if state == nil || state.function == nil || state.function.Name == nil {
		return false
	}
	name := state.function.Name.Name
	summary, ok := functions[name]
	if !ok || len(summary.privateResultKinds) == 0 {
		return false
	}
	changed := false
	ast.Inspect(state.function.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(value.Results) == 1 && len(summary.privateResultKinds) > 1 {
				for resultIndex := range summary.privateResultKinds {
					if summary.privateResultKinds[resultIndex] != "" || len(summary.resultParameterSources[resultIndex]) > 0 {
						continue
					}
					kind := recoveryPrivateExpressionResultKind(value.Results[0], resultIndex, &state.context, false)
					if kind != "" {
						summary.privateResultKinds[resultIndex] = kind
						changed = true
					}
				}
				return true
			}
			for resultIndex, result := range value.Results {
				if resultIndex >= len(summary.privateResultKinds) || summary.privateResultKinds[resultIndex] != "" ||
					len(summary.resultParameterSources[resultIndex]) > 0 {
					continue
				}
				kind := recoveryPrivateExpressionResultKind(result, 0, &state.context, false)
				if kind != "" {
					summary.privateResultKinds[resultIndex] = kind
					changed = true
				}
			}
		}
		return true
	})
	if changed {
		functions[name] = summary
	}
	return changed
}

func recoveryPrivateFunctionFormatViolations(
	files *token.FileSet,
	state *recoveryPrivateFunctionState,
) []string {
	var violations []string
	ast.Inspect(state.function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if !recoveryPrivateFormatterExpression(call.Fun, state.formatterAliases) {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			for _, argument := range call.Args[1:] {
				if privateKind := recoveryPrivateExpressionKind(argument, &state.context, true); privateKind != "" {
					violations = append(violations, fmt.Sprintf("%s:%d", privateKind, files.Position(call.Pos()).Line))
				}
			}
			return true
		}
		format, err := strconv.Unquote(literal.Value)
		if err != nil {
			violations = append(violations, fmt.Sprintf("invalid_format:%d", files.Position(literal.Pos()).Line))
			return true
		}
		usage := recoveryPrivateFormatArgumentUsage(format)
		for _, argumentIndex := range usage.unsafe {
			callIndex := argumentIndex + 1
			if callIndex >= len(call.Args) {
				continue
			}
			if kind := recoveryPrivateExpressionKind(call.Args[callIndex], &state.context, true); kind != "" {
				violations = append(violations, fmt.Sprintf("%s:%d", kind, files.Position(literal.Pos()).Line))
			}
		}
		for argumentIndex, argument := range call.Args[1:] {
			if usage.consumed[argumentIndex] {
				continue
			}
			if kind := recoveryPrivateExpressionKind(argument, &state.context, true); kind != "" {
				violations = append(violations, fmt.Sprintf("%s:%d", kind, files.Position(literal.Pos()).Line))
			}
		}
		return true
	})
	return violations
}

func recoveryPrivateFormatterExpression(expression ast.Expr, aliases map[string]bool) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return aliases[value.Name]
	case *ast.SelectorExpr:
		switch value.Sel.Name {
		case "Errorf", "Fatalf", "Logf", "Printf", "Sprintf":
			return true
		}
	case *ast.ParenExpr:
		return recoveryPrivateFormatterExpression(value.X, aliases)
	}
	return false
}

type recoveryPrivateExpressionContext struct {
	privateVariables map[string]string
	functions        map[string]recoveryPrivateFunctionSummary
}

type recoveryPrivateFunctionSummary struct {
	declaration            *ast.FuncDecl
	parameterNames         []string
	privateResultKinds     []string
	resultParameterSources [][]int
	preservedParameter     int
	hasPreservedParameter  bool
}

func recoveryPrivateFunctionSummaries(source *ast.File) map[string]recoveryPrivateFunctionSummary {
	summaries := make(map[string]recoveryPrivateFunctionSummary)
	if source == nil {
		return summaries
	}
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name == nil {
			continue
		}
		summary := recoveryPrivateFunctionSummary{
			declaration:        function,
			parameterNames:     recoveryPrivateFunctionParameterNames(function),
			privateResultKinds: recoveryPrivateFunctionResultKinds(function),
		}
		summary.resultParameterSources = make([][]int, len(summary.privateResultKinds))
		if parameter, ok := recoveryPrivatePreservedParameter(function); ok {
			summary.preservedParameter = parameter
			summary.hasPreservedParameter = true
		}
		summaries[function.Name.Name] = summary
	}
	for changed := true; changed; {
		changed = false
		for _, declaration := range source.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name == nil {
				continue
			}
			if recoveryPrivatePropagateFunctionResultParameters(function, summaries) {
				changed = true
			}
		}
	}
	return summaries
}

func recoveryPrivateFunctionParameterNames(function *ast.FuncDecl) []string {
	if function == nil || function.Type == nil || function.Type.Params == nil {
		return nil
	}
	var names []string
	for _, parameter := range function.Type.Params.List {
		if len(parameter.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, name := range parameter.Names {
			names = append(names, name.Name)
		}
	}
	return names
}

func recoveryPrivatePropagateFunctionResultParameters(
	function *ast.FuncDecl,
	functions map[string]recoveryPrivateFunctionSummary,
) bool {
	if function == nil || function.Name == nil || function.Body == nil {
		return false
	}
	summary, ok := functions[function.Name.Name]
	if !ok || len(summary.resultParameterSources) == 0 {
		return false
	}
	variables := make(map[string][]int)
	for index, name := range summary.parameterNames {
		if name != "" {
			variables[name] = []int{index}
		}
	}
	for changed := true; changed; {
		changed = false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.FuncLit:
				return false
			case *ast.AssignStmt:
				for index, left := range declaration.Lhs {
					variable, ok := left.(*ast.Ident)
					if !ok || variable.Name == "_" {
						continue
					}
					sources := recoveryPrivateAssignedExpressionParameterSources(declaration.Rhs, index, variables, functions)
					if merged, added := recoveryPrivateMergeParameterSources(variables[variable.Name], sources); added {
						variables[variable.Name] = merged
						changed = true
					}
				}
			case *ast.RangeStmt:
				keySources, valueSources := recoveryPrivateRangeBindingParameterSources(declaration.X, variables, functions)
				for binding, sources := range map[ast.Expr][]int{
					declaration.Key:   keySources,
					declaration.Value: valueSources,
				} {
					variable, ok := binding.(*ast.Ident)
					if !ok || variable.Name == "_" {
						continue
					}
					if merged, added := recoveryPrivateMergeParameterSources(variables[variable.Name], sources); added {
						variables[variable.Name] = merged
						changed = true
					}
				}
			case *ast.ValueSpec:
				for index, variable := range declaration.Names {
					sources := recoveryPrivateAssignedExpressionParameterSources(declaration.Values, index, variables, functions)
					if merged, added := recoveryPrivateMergeParameterSources(variables[variable.Name], sources); added {
						variables[variable.Name] = merged
						changed = true
					}
				}
			}
			return true
		})
	}
	changed := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(value.Results) == 1 && len(summary.resultParameterSources) > 1 {
				for resultIndex := range summary.resultParameterSources {
					sources := recoveryPrivateExpressionResultParameterSources(value.Results[0], resultIndex, variables, functions)
					if merged, added := recoveryPrivateMergeParameterSources(summary.resultParameterSources[resultIndex], sources); added {
						summary.resultParameterSources[resultIndex] = merged
						changed = true
					}
				}
				return true
			}
			for resultIndex, result := range value.Results {
				if resultIndex >= len(summary.resultParameterSources) {
					continue
				}
				sources := recoveryPrivateExpressionResultParameterSources(result, 0, variables, functions)
				if merged, added := recoveryPrivateMergeParameterSources(summary.resultParameterSources[resultIndex], sources); added {
					summary.resultParameterSources[resultIndex] = merged
					changed = true
				}
			}
		}
		return true
	})
	if changed {
		functions[function.Name.Name] = summary
	}
	return changed
}

func recoveryPrivateMergeParameterSources(current, additional []int) ([]int, bool) {
	merged := append([]int(nil), current...)
	changed := false
	for _, source := range additional {
		found := false
		for _, existing := range merged {
			if existing == source {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, source)
			changed = true
		}
	}
	return merged, changed
}

func recoveryPrivateRangeBindingParameterSources(
	expression ast.Expr,
	variables map[string][]int,
	functions map[string]recoveryPrivateFunctionSummary,
) ([]int, []int) {
	if parenthesized, ok := expression.(*ast.ParenExpr); ok {
		return recoveryPrivateRangeBindingParameterSources(parenthesized.X, variables, functions)
	}
	composite, ok := expression.(*ast.CompositeLit)
	if !ok {
		sources := recoveryPrivateExpressionParameterSources(expression, variables, functions)
		return sources, sources
	}
	switch composite.Type.(type) {
	case *ast.ArrayType:
		var valueSources []int
		for _, element := range composite.Elts {
			if keyed, ok := element.(*ast.KeyValueExpr); ok {
				element = keyed.Value
			}
			elementSources := recoveryPrivateExpressionParameterSources(element, variables, functions)
			valueSources, _ = recoveryPrivateMergeParameterSources(valueSources, elementSources)
		}
		return nil, valueSources
	case *ast.MapType:
		var keySources, valueSources []int
		for _, element := range composite.Elts {
			keyed, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			elementKeySources := recoveryPrivateExpressionParameterSources(keyed.Key, variables, functions)
			keySources, _ = recoveryPrivateMergeParameterSources(keySources, elementKeySources)
			elementValueSources := recoveryPrivateExpressionParameterSources(keyed.Value, variables, functions)
			valueSources, _ = recoveryPrivateMergeParameterSources(valueSources, elementValueSources)
		}
		return keySources, valueSources
	case *ast.ChanType:
		return recoveryPrivateExpressionParameterSources(expression, variables, functions), nil
	default:
		sources := recoveryPrivateExpressionParameterSources(expression, variables, functions)
		return sources, sources
	}
}

func recoveryPrivateAssignedExpressionParameterSources(
	expressions []ast.Expr,
	leftIndex int,
	variables map[string][]int,
	functions map[string]recoveryPrivateFunctionSummary,
) []int {
	if len(expressions) == 0 {
		return nil
	}
	expressionIndex := leftIndex
	resultIndex := 0
	if len(expressions) == 1 {
		expressionIndex = 0
		resultIndex = leftIndex
	}
	if expressionIndex >= len(expressions) {
		return nil
	}
	return recoveryPrivateExpressionResultParameterSources(expressions[expressionIndex], resultIndex, variables, functions)
}

func recoveryPrivateExpressionResultParameterSources(
	expression ast.Expr,
	resultIndex int,
	variables map[string][]int,
	functions map[string]recoveryPrivateFunctionSummary,
) []int {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		if resultIndex != 0 {
			return nil
		}
		return recoveryPrivateExpressionParameterSources(expression, variables, functions)
	}
	if resultIndex == 0 && len(call.Args) == 1 &&
		(recoveryPrivateBuiltinConversion(call.Fun) || recoveryPrivateTypeKind(call.Fun) != "") {
		return recoveryPrivateExpressionParameterSources(call.Args[0], variables, functions)
	}
	summary := functions[recoveryPrivateCallFunctionName(call.Fun)]
	if resultIndex >= len(summary.resultParameterSources) {
		return nil
	}
	var sources []int
	for _, parameter := range summary.resultParameterSources[resultIndex] {
		if parameter >= len(call.Args) {
			continue
		}
		argumentSources := recoveryPrivateExpressionParameterSources(call.Args[parameter], variables, functions)
		sources, _ = recoveryPrivateMergeParameterSources(sources, argumentSources)
	}
	return sources
}

func recoveryPrivateExpressionParameterSources(
	expression ast.Expr,
	variables map[string][]int,
	functions map[string]recoveryPrivateFunctionSummary,
) []int {
	switch value := expression.(type) {
	case *ast.Ident:
		return variables[value.Name]
	case *ast.ParenExpr:
		return recoveryPrivateExpressionParameterSources(value.X, variables, functions)
	case *ast.SelectorExpr:
		return recoveryPrivateExpressionParameterSources(value.X, variables, functions)
	case *ast.IndexExpr:
		sources := recoveryPrivateExpressionParameterSources(value.X, variables, functions)
		indexSources := recoveryPrivateExpressionParameterSources(value.Index, variables, functions)
		sources, _ = recoveryPrivateMergeParameterSources(sources, indexSources)
		return sources
	case *ast.IndexListExpr:
		sources := recoveryPrivateExpressionParameterSources(value.X, variables, functions)
		for _, index := range value.Indices {
			indexSources := recoveryPrivateExpressionParameterSources(index, variables, functions)
			sources, _ = recoveryPrivateMergeParameterSources(sources, indexSources)
		}
		return sources
	case *ast.BinaryExpr:
		if recoveryPrivateClosedDiagnosticBinary(value.Op) {
			return nil
		}
		sources := recoveryPrivateExpressionParameterSources(value.X, variables, functions)
		rightSources := recoveryPrivateExpressionParameterSources(value.Y, variables, functions)
		sources, _ = recoveryPrivateMergeParameterSources(sources, rightSources)
		return sources
	case *ast.CallExpr:
		return recoveryPrivateExpressionResultParameterSources(value, 0, variables, functions)
	case *ast.CompositeLit:
		var sources []int
		for _, element := range value.Elts {
			elementSources := recoveryPrivateExpressionParameterSources(element, variables, functions)
			sources, _ = recoveryPrivateMergeParameterSources(sources, elementSources)
		}
		return sources
	case *ast.KeyValueExpr:
		sources := recoveryPrivateExpressionParameterSources(value.Key, variables, functions)
		valueSources := recoveryPrivateExpressionParameterSources(value.Value, variables, functions)
		sources, _ = recoveryPrivateMergeParameterSources(sources, valueSources)
		return sources
	case *ast.StarExpr:
		return recoveryPrivateExpressionParameterSources(value.X, variables, functions)
	case *ast.UnaryExpr:
		return recoveryPrivateExpressionParameterSources(value.X, variables, functions)
	case *ast.SliceExpr:
		return recoveryPrivateExpressionParameterSources(value.X, variables, functions)
	case *ast.TypeAssertExpr:
		return recoveryPrivateExpressionParameterSources(value.X, variables, functions)
	}
	return nil
}

func recoveryPrivateFunctionResultKinds(function *ast.FuncDecl) []string {
	if function == nil || function.Type == nil || function.Type.Results == nil {
		return nil
	}
	var kinds []string
	for _, result := range function.Type.Results.List {
		count := len(result.Names)
		if count == 0 {
			count = 1
		}
		kind := recoveryPrivateTypeKind(result.Type)
		for range count {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func recoveryPrivatePreservedParameter(function *ast.FuncDecl) (int, bool) {
	if function == nil || function.Type == nil || function.Type.Params == nil || function.Body == nil {
		return 0, false
	}
	var parameterNames []string
	for _, parameter := range function.Type.Params.List {
		for _, name := range parameter.Names {
			parameterNames = append(parameterNames, name.Name)
		}
	}
	if len(parameterNames) != 1 || len(recoveryPrivateFunctionResultKinds(function)) != 1 {
		return 0, false
	}
	foundReturn := false
	preserves := true
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			foundReturn = true
			if len(value.Results) != 1 || recoveryPrivateReturnedIdentifier(value.Results[0]) != parameterNames[0] {
				preserves = false
			}
		}
		return true
	})
	return 0, foundReturn && preserves
}

func recoveryPrivateReturnedIdentifier(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.ParenExpr:
		return recoveryPrivateReturnedIdentifier(value.X)
	}
	return ""
}

func recoveryPrivateAssignedExpressionKind(
	expressions []ast.Expr,
	leftIndex int,
	context *recoveryPrivateExpressionContext,
) string {
	if len(expressions) == 0 {
		return ""
	}
	expressionIndex := leftIndex
	resultIndex := 0
	if len(expressions) == 1 {
		expressionIndex = 0
		resultIndex = leftIndex
	}
	if expressionIndex >= len(expressions) {
		return ""
	}
	return recoveryPrivateExpressionResultKind(expressions[expressionIndex], resultIndex, context, false)
}

func recoveryPrivateRangeBindingKinds(
	expression ast.Expr,
	context *recoveryPrivateExpressionContext,
) (string, string) {
	if parenthesized, ok := expression.(*ast.ParenExpr); ok {
		return recoveryPrivateRangeBindingKinds(parenthesized.X, context)
	}
	composite, ok := expression.(*ast.CompositeLit)
	if !ok {
		kind := recoveryPrivateExpressionKind(expression, context, false)
		return kind, kind
	}
	switch compositeType := composite.Type.(type) {
	case *ast.ArrayType:
		valueKind := recoveryPrivateTypeKind(compositeType.Elt)
		for _, element := range composite.Elts {
			if keyed, ok := element.(*ast.KeyValueExpr); ok {
				element = keyed.Value
			}
			if kind := recoveryPrivateExpressionKind(element, context, false); valueKind == "" && kind != "" {
				valueKind = kind
			}
		}
		return "", valueKind
	case *ast.MapType:
		keyKind := recoveryPrivateTypeKind(compositeType.Key)
		valueKind := recoveryPrivateTypeKind(compositeType.Value)
		for _, element := range composite.Elts {
			keyed, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if kind := recoveryPrivateExpressionKind(keyed.Key, context, false); keyKind == "" && kind != "" {
				keyKind = kind
			}
			if kind := recoveryPrivateExpressionKind(keyed.Value, context, false); valueKind == "" && kind != "" {
				valueKind = kind
			}
		}
		return keyKind, valueKind
	case *ast.ChanType:
		kind := recoveryPrivateTypeKind(compositeType.Value)
		if kind == "" {
			kind = recoveryPrivateExpressionKind(expression, context, false)
		}
		return kind, ""
	default:
		kind := recoveryPrivateExpressionKind(expression, context, false)
		return kind, kind
	}
}

func recoveryPrivateExpressionResultKind(
	expression ast.Expr,
	resultIndex int,
	context *recoveryPrivateExpressionContext,
	followCalls bool,
) string {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		if resultIndex != 0 {
			return ""
		}
		return recoveryPrivateExpressionKind(expression, context, followCalls)
	}
	function := context.functions[recoveryPrivateCallFunctionName(call.Fun)]
	if resultIndex < len(function.privateResultKinds) && function.privateResultKinds[resultIndex] != "" {
		return function.privateResultKinds[resultIndex]
	}
	if resultIndex < len(function.resultParameterSources) {
		for _, parameter := range function.resultParameterSources[resultIndex] {
			if parameter >= len(call.Args) {
				continue
			}
			if kind := recoveryPrivateExpressionKind(call.Args[parameter], context, followCalls); kind != "" {
				return kind
			}
		}
	}
	if resultIndex != 0 {
		return ""
	}
	return recoveryPrivateExpressionKind(expression, context, followCalls)
}

func recoveryPrivateExpressionKind(
	expression ast.Expr,
	context *recoveryPrivateExpressionContext,
	followCalls bool,
) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return context.privateVariables[value.Name]
	case *ast.ParenExpr:
		return recoveryPrivateExpressionKind(value.X, context, followCalls)
	case *ast.SelectorExpr:
		kind := recoveryPrivateExpressionKind(value.X, context, followCalls)
		if authorityKind := recoveryPrivateAuthorityFieldKind(value.Sel.Name); authorityKind != "" {
			if kind != "" {
				return kind
			}
			return authorityKind
		}
		if recoveryPrivateClosedDiagnosticField(value.Sel.Name) {
			return ""
		}
		return kind
	case *ast.IndexExpr:
		if kind := recoveryPrivateExpressionKind(value.X, context, followCalls); kind != "" {
			return kind
		}
		return recoveryPrivateExpressionKind(value.Index, context, followCalls)
	case *ast.IndexListExpr:
		if kind := recoveryPrivateExpressionKind(value.X, context, followCalls); kind != "" {
			return kind
		}
		for _, index := range value.Indices {
			if kind := recoveryPrivateExpressionKind(index, context, followCalls); kind != "" {
				return kind
			}
		}
	case *ast.BinaryExpr:
		if recoveryPrivateClosedDiagnosticBinary(value.Op) {
			return ""
		}
		if kind := recoveryPrivateExpressionKind(value.X, context, followCalls); kind != "" {
			return kind
		}
		return recoveryPrivateExpressionKind(value.Y, context, followCalls)
	case *ast.CallExpr:
		if kind := recoveryPrivateTypeKind(value.Fun); kind != "" {
			return kind
		}
		if recoveryPrivateBuiltinConversion(value.Fun) && len(value.Args) == 1 {
			return recoveryPrivateExpressionKind(value.Args[0], context, followCalls)
		}
		functionName := recoveryPrivateCallFunctionName(value.Fun)
		if functionName == "len" || functionName == "cap" {
			return ""
		}
		function := context.functions[functionName]
		if len(function.privateResultKinds) > 0 && function.privateResultKinds[0] != "" {
			return function.privateResultKinds[0]
		}
		if len(function.resultParameterSources) > 0 {
			for _, parameter := range function.resultParameterSources[0] {
				if parameter >= len(value.Args) {
					continue
				}
				if kind := recoveryPrivateExpressionKind(value.Args[parameter], context, followCalls); kind != "" {
					return kind
				}
			}
		}
		if function.hasPreservedParameter && function.preservedParameter < len(value.Args) {
			return recoveryPrivateExpressionKind(value.Args[function.preservedParameter], context, followCalls)
		}
		return ""
	case *ast.CompositeLit:
		if kind := recoveryPrivateTypeKind(value.Type); kind != "" {
			return kind
		}
		for _, element := range value.Elts {
			if kind := recoveryPrivateExpressionKind(element, context, followCalls); kind != "" {
				return kind
			}
		}
	case *ast.KeyValueExpr:
		if kind := recoveryPrivateExpressionKind(value.Key, context, followCalls); kind != "" {
			return kind
		}
		return recoveryPrivateExpressionKind(value.Value, context, followCalls)
	case *ast.StarExpr:
		return recoveryPrivateExpressionKind(value.X, context, followCalls)
	case *ast.UnaryExpr:
		return recoveryPrivateExpressionKind(value.X, context, followCalls)
	case *ast.SliceExpr:
		return recoveryPrivateExpressionKind(value.X, context, followCalls)
	case *ast.TypeAssertExpr:
		return recoveryPrivateExpressionKind(value.X, context, followCalls)
	}
	return ""
}

func recoveryPrivateBuiltinConversion(expression ast.Expr) bool {
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return false
	}
	switch identifier.Name {
	case "any", "bool", "byte", "complex64", "complex128", "float32", "float64",
		"int", "int8", "int16", "int32", "int64", "rune", "string",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
		return true
	}
	return false
}

func recoveryPrivateCallFunctionName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.ParenExpr:
		return recoveryPrivateCallFunctionName(value.X)
	case *ast.IndexExpr:
		return recoveryPrivateCallFunctionName(value.X)
	case *ast.IndexListExpr:
		return recoveryPrivateCallFunctionName(value.X)
	}
	return ""
}

func recoveryPrivateLocalCallName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.ParenExpr:
		return recoveryPrivateLocalCallName(value.X)
	case *ast.IndexExpr:
		return recoveryPrivateLocalCallName(value.X)
	case *ast.IndexListExpr:
		return recoveryPrivateLocalCallName(value.X)
	}
	return ""
}

func recoveryPrivateTypeKind(expression ast.Expr) string {
	if expression == nil {
		return ""
	}
	switch value := expression.(type) {
	case *ast.Ident:
		if recoveryPrivateTypeNames()[value.Name] {
			return value.Name
		}
	case *ast.SelectorExpr:
		if recoveryPrivateTypeNames()[value.Sel.Name] {
			return value.Sel.Name
		}
	case *ast.ParenExpr:
		return recoveryPrivateTypeKind(value.X)
	case *ast.StarExpr:
		return recoveryPrivateTypeKind(value.X)
	case *ast.ArrayType:
		return recoveryPrivateTypeKind(value.Elt)
	case *ast.MapType:
		if kind := recoveryPrivateTypeKind(value.Key); kind != "" {
			return kind
		}
		return recoveryPrivateTypeKind(value.Value)
	case *ast.ChanType:
		return recoveryPrivateTypeKind(value.Value)
	case *ast.Ellipsis:
		return recoveryPrivateTypeKind(value.Elt)
	case *ast.IndexExpr:
		if kind := recoveryPrivateTypeKind(value.X); kind != "" {
			return kind
		}
		return recoveryPrivateTypeKind(value.Index)
	case *ast.IndexListExpr:
		if kind := recoveryPrivateTypeKind(value.X); kind != "" {
			return kind
		}
		for _, index := range value.Indices {
			if kind := recoveryPrivateTypeKind(index); kind != "" {
				return kind
			}
		}
	}
	return ""
}

func recoveryPrivateTypeNames() map[string]bool {
	return map[string]bool{
		"RecoveryWorkerClaim":             true,
		"recoverySourceCancellationState": true,
		"recoveryWorkspaceCleanupTuple":   true,
		"RecoveryPoint":                   true,
		"RecoveryPointLease":              true,
		"RecoveryPointLifecycleAttempt":   true,
		"BackupAssetRecoveryJob":          true,
		"BackupAssetRecoveryAttempt":      true,
		"BackupAssetRecoveryNodeLease":    true,
		"BackupAssetRecoveryResultSet":    true,
		"BackupAssetRecoveryResult":       true,
		"LeaseFence":                      true,
		"TargetWritePermit":               true,
	}
}

func recoveryPrivateAuthorityFieldKind(field string) string {
	privateFields := map[string]bool{
		"FenceToken":                        true,
		"SourceFence":                       true,
		"AttemptFence":                      true,
		"NodeFence":                         true,
		"WorkerID":                          true,
		"OwnerID":                           true,
		"WorkspaceOwner":                    true,
		"WorkspaceFence":                    true,
		"WorkspaceCleanupOwner":             true,
		"WorkspaceCleanupFence":             true,
		"WorkspaceCleanupNodeLeaseID":       true,
		"WorkspaceCleanupNodeFence":         true,
		"WorkspaceCleanupAttempt":           true,
		"CleanupOwner":                      true,
		"CleanupFence":                      true,
		"CleanupNodeLeaseID":                true,
		"CleanupAttempt":                    true,
		"EncryptedWorkspaceRelativeLocator": true,
		"EncryptedRelativeLocator":          true,
		"EncryptedProviderLocator":          true,
		"LocatorDigest":                     true,
		"MarkerBindingDigest":               true,
		"WorkspaceBindingDigest":            true,
		"WorkspaceMarkerBindingDigest":      true,
		"ManifestDigest":                    true,
		"SourceFingerprint":                 true,
		"PathDigest":                        true,
	}
	if privateFields[field] {
		return "RecoveryAuthorityField"
	}
	return ""
}

func recoveryPrivateClosedDiagnosticField(field string) bool {
	switch field {
	case "ID", "JobID", "AttemptID", "RecoveryPointID", "ResultSetID", "PlanID", "LifecycleAttemptID", "sourcePointID",
		"FailureCategory", "jobFailureCategory", "Operation", "ResultKind", "Classification":
		return true
	}
	lowerField := strings.ToLower(field)
	return strings.HasSuffix(lowerField, "state") ||
		strings.HasSuffix(lowerField, "status") ||
		strings.HasSuffix(lowerField, "phase") ||
		strings.HasSuffix(lowerField, "revision") ||
		strings.HasSuffix(lowerField, "count")
}

func recoveryPrivateClosedDiagnosticBinary(operator token.Token) bool {
	switch operator {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
		return true
	}
	return false
}

type recoveryPrivateFormatArguments struct {
	consumed map[int]bool
	unsafe   []int
	seen     map[int]bool
}

func recoveryPrivateFormatArgumentUsage(format string) recoveryPrivateFormatArguments {
	usage := recoveryPrivateFormatArguments{
		consumed: make(map[int]bool),
		seen:     make(map[int]bool),
	}
	markUnsafe := func(index int) {
		usage.consumed[index] = true
		if !usage.seen[index] {
			usage.unsafe = append(usage.unsafe, index)
			usage.seen[index] = true
		}
	}
	nextArgument := 0
	for offset := 0; offset < len(format); {
		if format[offset] != '%' {
			offset++
			continue
		}
		offset++
		if offset >= len(format) {
			markUnsafe(nextArgument)
			break
		}
		if format[offset] == '%' {
			offset++
			continue
		}

		valueIndex := nextArgument
		valueIndexSet := false
		leadingIndex, afterLeadingIndex, hasLeadingIndex := recoveryFormatExplicitIndex(format, offset)
		if hasLeadingIndex {
			offset = afterLeadingIndex
		}
		for offset < len(format) && strings.ContainsRune("#0+- '", rune(format[offset])) {
			offset++
		}
		if hasLeadingIndex && offset < len(format) && format[offset] == '*' {
			markUnsafe(leadingIndex)
			nextArgument = leadingIndex + 1
			offset++
		} else if hasLeadingIndex {
			valueIndex = leadingIndex
			valueIndexSet = true
		} else if explicit, next, ok := recoveryFormatExplicitIndex(format, offset); ok {
			if next < len(format) && format[next] == '*' {
				markUnsafe(explicit)
				nextArgument = explicit + 1
				offset = next + 1
			} else {
				valueIndex = explicit
				valueIndexSet = true
				offset = next
			}
		} else if offset < len(format) && format[offset] == '*' {
			markUnsafe(nextArgument)
			nextArgument++
			offset++
		}
		for offset < len(format) && format[offset] >= '0' && format[offset] <= '9' {
			offset++
		}
		if offset < len(format) && format[offset] == '.' {
			offset++
			if explicit, next, ok := recoveryFormatExplicitIndex(format, offset); ok && next < len(format) && format[next] == '*' {
				markUnsafe(explicit)
				nextArgument = explicit + 1
				offset = next + 1
			} else if offset < len(format) && format[offset] == '*' {
				markUnsafe(nextArgument)
				nextArgument++
				offset++
			} else {
				for offset < len(format) && format[offset] >= '0' && format[offset] <= '9' {
					offset++
				}
			}
		}
		if explicit, next, ok := recoveryFormatExplicitIndex(format, offset); ok {
			valueIndex = explicit
			valueIndexSet = true
			offset = next
		}
		if !valueIndexSet {
			valueIndex = nextArgument
		}
		if offset >= len(format) {
			markUnsafe(valueIndex)
			break
		}
		if recoveryPrivateConsumingFormatVerb(format[offset]) {
			markUnsafe(valueIndex)
		} else if format[offset] == 'T' {
			usage.consumed[valueIndex] = true
		}
		offset++
		nextArgument = valueIndex + 1
	}
	return usage
}

func recoveryPrivateConsumingFormatVerb(verb byte) bool {
	if verb == 'T' {
		return false
	}
	return verb != '%'
}

func recoveryFormatExplicitIndex(format string, offset int) (int, int, bool) {
	if offset >= len(format) || format[offset] != '[' {
		return 0, offset, false
	}
	end := offset + 1
	for end < len(format) && format[end] >= '0' && format[end] <= '9' {
		end++
	}
	if end == offset+1 || end >= len(format) || format[end] != ']' {
		return 0, offset, false
	}
	parsed, err := strconv.Atoi(format[offset+1 : end])
	if err != nil || parsed <= 0 {
		return 0, offset, false
	}
	return parsed - 1, end + 1, true
}

func TestRecoveryPointSourceLifecycleRecoveryCancellationAfterPrecheckUsesBoundedDetachedContext(t *testing.T) {
	fixture, coordinator, _, request := newRecoverySourceLifecycleCancellationFixture(t)
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	afterPrecheck := make(chan struct{})
	canceler := &recoverySourceLifecycleCancelAfterPrecheck{
		delegate: coordinator, cancel: cancelCaller, afterPrecheck: afterPrecheck,
	}
	owner, err := NewSourceLifecycle(fixture.db, canceler, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}

	const maxDetachedCancellation = 5 * time.Second
	type deadlineObservation struct {
		deadline    time.Time
		observedAt  time.Time
		hasDeadline bool
	}
	type expirationObservation struct {
		err       error
		expiredAt time.Time
	}
	deadlineObserved := make(chan deadlineObservation, 1)
	contextExpired := make(chan expirationObservation, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCallback := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(releaseCallback)

	errMissingDeadline := errors.New("Recovery source cancellation transaction has no deadline")
	errReleasedBeforeDeadline := errors.New("Recovery source cancellation callback released before its deadline")
	callbackName := "recovery:bounded-source-cancellation:" + strings.ReplaceAll(t.Name(), "/", "_")
	var callbackOnce sync.Once
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != (model.BackupAssetRecoveryJob{}).TableName() {
			return
		}
		select {
		case <-afterPrecheck:
		default:
			return
		}
		callbackOnce.Do(func() {
			observedAt := time.Now()
			deadline, hasDeadline := tx.Statement.Context.Deadline()
			deadlineObserved <- deadlineObservation{
				deadline: deadline, observedAt: observedAt, hasDeadline: hasDeadline,
			}
			if !hasDeadline {
				<-release
				_ = tx.AddError(errMissingDeadline)
				return
			}
			select {
			case <-tx.Statement.Context.Done():
				contextExpired <- expirationObservation{err: tx.Statement.Context.Err(), expiredAt: time.Now()}
				_ = tx.AddError(tx.Statement.Context.Err())
			case <-release:
				_ = tx.AddError(errReleasedBeforeDeadline)
			}
		})
	}); err != nil {
		t.Fatalf("register bounded source-cancellation callback: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove bounded source-cancellation callback: %v", err)
		}
	})

	type cancellationResult struct {
		err        error
		returnedAt time.Time
	}
	result := make(chan cancellationResult, 1)
	go func() {
		cancelErr := owner.CancelRecoveryPointInterests(callerCtx, request)
		result <- cancellationResult{err: cancelErr, returnedAt: time.Now()}
	}()

	var observed deadlineObservation
	select {
	case observed = <-deadlineObserved:
	case completed := <-result:
		t.Fatalf("source cancellation returned before the detached transaction callback: %v", completed.err)
	case <-time.After(2 * time.Second):
		releaseCallback()
		t.Fatal("source cancellation did not race from its precheck into cancelJob")
	}
	if !errors.Is(callerCtx.Err(), context.Canceled) {
		releaseCallback()
		completed := <-result
		t.Fatalf("source cancellation callback observed caller context=%v, want canceled: %v", callerCtx.Err(), completed.err)
	}
	if !observed.hasDeadline {
		releaseCallback()
		completed := <-result
		t.Fatalf("source cancellation detached unbounded database work after its precheck: %v", completed.err)
	}
	remaining := observed.deadline.Sub(observed.observedAt)
	if remaining <= 0 || remaining > maxDetachedCancellation {
		releaseCallback()
		completed := <-result
		t.Fatalf("source cancellation detached deadline remaining=%s, want (0, %s]: %v",
			remaining, maxDetachedCancellation, completed.err)
	}

	returnMargin := 2 * time.Second
	returnTimer := time.NewTimer(time.Until(observed.deadline) + returnMargin)
	defer returnTimer.Stop()
	var completed cancellationResult
	select {
	case completed = <-result:
	case <-returnTimer.C:
		releaseCallback()
		t.Fatalf("source cancellation did not release owner/coordinator by its deadline plus %s", returnMargin)
	}
	if !errors.Is(completed.err, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("expired source cancellation error=%v, want ErrRecoveryWorkerUnavailable", completed.err)
	}
	expired := <-contextExpired
	if !errors.Is(expired.err, context.DeadlineExceeded) || expired.expiredAt.Before(observed.deadline) ||
		completed.returnedAt.Before(expired.expiredAt) {
		t.Fatalf("source cancellation expiration=%v at %s deadline=%s return=%s",
			expired.err, expired.expiredAt, observed.deadline, completed.returnedAt)
	}
}

func TestRecoveryPointSourceLifecycleRecoveryPrepareFirstWritePostgresLockOrder(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptExecute)
	var serverVersion int
	if err := fixture.db.Raw("SHOW server_version_num").Scan(&serverVersion).Error; err != nil {
		t.Fatalf("load PostgreSQL version: %v", err)
	}
	if serverVersion/10000 != 17 {
		t.Fatalf("PostgreSQL major=%d, want 17", serverVersion/10000)
	}

	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute PostgreSQL Recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	// The lock-order regression ends at the durable first-write transaction. A
	// target-side workspace call would add a second transaction unrelated to the
	// plan/job/point cancellation order under test.
	coordinator.target = nil
	claim, found, err := coordinator.ClaimNext(context.Background(), "source-lifecycle-postgres-lock-order")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim PostgreSQL Recovery source job: job_id=%q attempt_id=%q found=%t err=%v",
			claim.JobID, claim.AttemptID, found, err)
	}

	var initialJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&initialJob).Error; err != nil {
		t.Fatal(err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", initialJob.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	request := seedRecoverySourceLifecycleRequest(t, fixture.db, plan.RecoveryPointID)

	var targetPoint model.RecoveryPoint
	if err := fixture.db.Where("id = ?", request.RecoveryPointID).Take(&targetPoint).Error; err != nil {
		t.Fatal(err)
	}
	otherPoint := targetPoint
	otherPoint.ID = strings.Repeat("9", 32)
	otherPoint.SourceFingerprint = strings.Repeat("8", 64)
	otherPoint.ManifestDigest = strings.Repeat("7", 64)
	otherPoint.EncryptedProviderLocator = "FAKE_OTHER_POINT_RECOVERY_LOCATOR_FOR_LOCK_ORDER_TEST_ONLY"
	if err := fixture.db.Create(&otherPoint).Error; err != nil {
		t.Fatalf("seed unrelated PostgreSQL RecoveryPoint: %v", err)
	}
	otherAttempt := model.RecoveryPointLifecycleAttempt{
		ID: strings.Repeat("8", 32), RecoveryPointID: otherPoint.ID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
	}
	if err := fixture.db.Create(&otherAttempt).Error; err != nil {
		t.Fatalf("seed unrelated PostgreSQL lifecycle attempt: %v", err)
	}

	resultSet := model.BackupAssetRecoveryResultSet{
		ID: strings.Repeat("d", 32), JobID: claim.JobID, State: string(ResultSetStateReady),
		MarkerBindingDigest: strings.Repeat("a", 64), PlaintextDeadline: fixture.now.AddDate(0, 0, 1),
		HardDeadline: fixture.now.AddDate(0, 0, 2), CleanupPhase: string(CleanupPhaseClaimed),
	}
	if err := fixture.db.Create(&resultSet).Error; err != nil {
		t.Fatalf("seed PostgreSQL RecoveryResult set: %v", err)
	}
	result := model.BackupAssetRecoveryResult{
		ID: strings.Repeat("e", 32), ResultSetID: resultSet.ID, JobID: claim.JobID,
		ResultKind: string(RecoveryResultKindRegularFile), Classification: "private",
		ClassificationRevision: 1, ClassificationSourceRevision: 1,
		EncryptedRelativeLocator: "results/private", LocatorDigest: strings.Repeat("b", 64),
	}
	if err := fixture.db.Create(&result).Error; err != nil {
		t.Fatalf("seed PostgreSQL RecoveryResult: %v", err)
	}

	type lockProbeContextKey struct{}
	const (
		prepareProbe = "prepare_first_write"
		cancelProbe  = "cancel_recovery_point"
		beforeProbe  = "test:recovery_source_prepare_cancel_lock_order:before"
		afterProbe   = "test:recovery_source_prepare_cancel_lock_order:after"
		updateProbe  = "test:recovery_source_prepare_cancel_lock_order:update"
	)
	preparePlanLocked := make(chan struct{})
	cancelPlanStarted := make(chan struct{})
	cancelPointLocked := make(chan struct{})
	releasePrepare := make(chan struct{})
	var preparePlanOnce sync.Once
	var cancelPlanOnce sync.Once
	var cancelPointOnce sync.Once
	var observerMu sync.Mutex
	lockAttempts := map[string]map[string]int{
		prepareProbe: {},
		cancelProbe:  {},
	}
	lockSequences := map[string][]string{
		prepareProbe: nil,
		cancelProbe:  nil,
	}
	var sqlStates []string
	var retryableQueryErrors int
	var cancellationValidated bool
	var mutationBeforeValidation bool
	lockedTable := func(callbackDB *gorm.DB) (string, string, bool) {
		if callbackDB.Statement == nil || callbackDB.Statement.Context == nil {
			return "", "", false
		}
		label, _ := callbackDB.Statement.Context.Value(lockProbeContextKey{}).(string)
		table := callbackDB.Statement.Table
		if table == "" && callbackDB.Statement.Schema != nil {
			table = callbackDB.Statement.Schema.Table
		}
		_, locked := callbackDB.Statement.Clauses["FOR"]
		return label, table, locked
	}
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(beforeProbe, func(callbackDB *gorm.DB) {
		label, table, locked := lockedTable(callbackDB)
		if label == "" || !locked {
			return
		}
		observerMu.Lock()
		lockAttempts[label][table]++
		observerMu.Unlock()
		if label == cancelProbe && table == (model.BackupAssetRecoveryPlan{}).TableName() {
			cancelPlanOnce.Do(func() { close(cancelPlanStarted) })
		}
		if label == prepareProbe && table == (model.RecoveryPoint{}).TableName() {
			select {
			case <-cancelPlanStarted:
			case <-callbackDB.Statement.Context.Done():
			}
		}
	}); err != nil {
		t.Fatalf("register PostgreSQL Recovery lock-order before probe: %v", err)
	}
	if err := fixture.db.Callback().Query().After("gorm:query").Register(afterProbe, func(callbackDB *gorm.DB) {
		label, table, locked := lockedTable(callbackDB)
		if label == "" || !locked {
			return
		}
		if callbackDB.Error != nil {
			code := "unclassified"
			var postgresError *pgconn.PgError
			if errors.As(callbackDB.Error, &postgresError) {
				code = postgresError.Code
			}
			observerMu.Lock()
			sqlStates = append(sqlStates, code)
			if code == "40P01" || code == "40001" {
				retryableQueryErrors++
			}
			observerMu.Unlock()
			return
		}
		if callbackDB.RowsAffected == 0 {
			return
		}
		observerMu.Lock()
		lockSequences[label] = append(lockSequences[label], table)
		if label == cancelProbe && table == (model.RecoveryPointLifecycleAttempt{}).TableName() {
			cancellationValidated = true
		}
		observerMu.Unlock()
		if label == prepareProbe && table == (model.BackupAssetRecoveryPlan{}).TableName() {
			preparePlanOnce.Do(func() {
				close(preparePlanLocked)
				select {
				case <-releasePrepare:
				case <-callbackDB.Statement.Context.Done():
				}
			})
		}
		if label == cancelProbe && table == (model.RecoveryPoint{}).TableName() {
			cancelPointOnce.Do(func() { close(cancelPointLocked) })
		}
	}); err != nil {
		_ = fixture.db.Callback().Query().Remove(beforeProbe)
		t.Fatalf("register PostgreSQL Recovery lock-order SQLSTATE observer: %v", err)
	}
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(updateProbe, func(callbackDB *gorm.DB) {
		if callbackDB.Statement == nil || callbackDB.Statement.Context == nil {
			return
		}
		label, _ := callbackDB.Statement.Context.Value(lockProbeContextKey{}).(string)
		if label != cancelProbe {
			return
		}
		observerMu.Lock()
		if !cancellationValidated {
			mutationBeforeValidation = true
		}
		observerMu.Unlock()
	}); err != nil {
		_ = fixture.db.Callback().Query().Remove(afterProbe)
		_ = fixture.db.Callback().Query().Remove(beforeProbe)
		t.Fatalf("register PostgreSQL Recovery pre-mutation validation probe: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Update().Remove(updateProbe); err != nil {
			t.Errorf("remove PostgreSQL Recovery mutation probe: %v", err)
		}
		if err := fixture.db.Callback().Query().Remove(afterProbe); err != nil {
			t.Errorf("remove PostgreSQL Recovery SQLSTATE observer: %v", err)
		}
		if err := fixture.db.Callback().Query().Remove(beforeProbe); err != nil {
			t.Errorf("remove PostgreSQL Recovery lock-order before probe: %v", err)
		}
	})

	testCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type prepareOutcome struct {
		permit TargetWritePermit
		err    error
	}
	prepares := make(chan prepareOutcome, 1)
	cancellations := make(chan error, 1)
	go func() {
		permit, prepareErr := coordinator.PrepareFirstWrite(
			context.WithValue(testCtx, lockProbeContextKey{}, prepareProbe), claim,
		)
		prepares <- prepareOutcome{permit: permit, err: prepareErr}
	}()
	select {
	case <-preparePlanLocked:
	case <-testCtx.Done():
		t.Fatalf("PostgreSQL PrepareFirstWrite did not hold the plan lock: %v", testCtx.Err())
	}
	go func() {
		cancellations <- coordinator.CancelRecoveryPoint(
			context.WithValue(testCtx, lockProbeContextKey{}, cancelProbe), request, claim.JobID,
		)
	}()
	select {
	case <-cancelPlanStarted:
		// Canonical plan-first cancellation waits behind PrepareFirstWrite.
	case <-cancelPointLocked:
		// The old point-first cancellation now holds the inverse side of the cycle.
	case <-testCtx.Done():
		t.Fatalf("PostgreSQL cancellation reached neither plan nor exact point lock: %v", testCtx.Err())
	}
	close(releasePrepare)

	var prepared prepareOutcome
	select {
	case prepared = <-prepares:
	case <-testCtx.Done():
		t.Fatalf("PostgreSQL PrepareFirstWrite did not complete: %v", testCtx.Err())
	}
	var cancellationErr error
	select {
	case cancellationErr = <-cancellations:
	case <-testCtx.Done():
		t.Fatalf("PostgreSQL source cancellation did not complete: %v", testCtx.Err())
	}

	observerMu.Lock()
	observedStates := append([]string(nil), sqlStates...)
	retryCount := retryableQueryErrors
	prepareSequence := append([]string(nil), lockSequences[prepareProbe]...)
	cancelSequence := append([]string(nil), lockSequences[cancelProbe]...)
	prepareAttempts := cloneRecoverySourceLifecycleLockAttempts(lockAttempts[prepareProbe])
	cancelAttempts := cloneRecoverySourceLifecycleLockAttempts(lockAttempts[cancelProbe])
	mutatedBeforeValidation := mutationBeforeValidation
	observerMu.Unlock()
	for _, state := range observedStates {
		if state == "40P01" {
			t.Fatalf("PostgreSQL PrepareFirstWrite/source cancellation observed SQLSTATE 40P01 instead of one lock order: states=%v prepare=%v cancel=%v errors=%v/%v",
				observedStates, prepareSequence, cancelSequence, prepared.err, cancellationErr)
		}
	}
	if retryCount != 0 || len(observedStates) != 0 {
		t.Fatalf("PostgreSQL PrepareFirstWrite/source cancellation hid a retryable query: retries=%d states=%v", retryCount, observedStates)
	}
	if prepared.err != nil || cancellationErr != nil {
		t.Fatalf("PostgreSQL PrepareFirstWrite/source cancellation errors=%v/%v, want clean serialization", prepared.err, cancellationErr)
	}
	if mutatedBeforeValidation {
		t.Fatal("PostgreSQL source cancellation mutated Recovery authority before exact lifecycle validation")
	}

	prepareOrder := []string{
		(model.BackupAssetRecoveryPlan{}).TableName(),
		(model.BackupAssetRecoveryJob{}).TableName(),
		(model.RecoveryPoint{}).TableName(),
		(model.BackupAssetRecoveryAttempt{}).TableName(),
	}
	cancelOrder := []string{
		(model.BackupAssetRecoveryPlan{}).TableName(),
		(model.BackupAssetRecoveryJob{}).TableName(),
		(model.RecoveryPoint{}).TableName(),
		(model.RecoveryPointLifecycleAttempt{}).TableName(),
		(model.BackupAssetRecoveryAttempt{}).TableName(),
	}
	assertRecoverySourceLifecycleLockOrder(t, "PrepareFirstWrite", prepareSequence, prepareOrder)
	assertRecoverySourceLifecycleLockOrder(t, "source cancellation", cancelSequence, cancelOrder)
	for label, attempts := range map[string]map[string]int{
		"PrepareFirstWrite":   prepareAttempts,
		"source cancellation": cancelAttempts,
	} {
		for _, table := range []string{
			(model.BackupAssetRecoveryPlan{}).TableName(),
			(model.BackupAssetRecoveryJob{}).TableName(),
			(model.RecoveryPoint{}).TableName(),
		} {
			if attempts[table] != 1 {
				t.Fatalf("%s PostgreSQL %s lock attempts=%d, want exactly one with no hidden retry; all=%v", label, table, attempts[table], attempts)
			}
		}
	}

	var finalJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&finalJob).Error; err != nil {
		t.Fatal(err)
	}
	if finalJob.State != string(JobStateNeedsAttention) ||
		finalJob.FailureCategory != recoveryCancellationAfterMutationArmFailureCategory ||
		finalJob.TransitionRevision != claim.TransitionRevision+2 ||
		finalJob.WorkspaceCleanupPhase != initialJob.WorkspaceCleanupPhase ||
		finalJob.WorkspaceCleanupOwner != initialJob.WorkspaceCleanupOwner ||
		finalJob.WorkspaceCleanupFence != initialJob.WorkspaceCleanupFence {
		t.Fatalf("PostgreSQL exact-point cancellation crossed RecoveryResult/workspace ownership: before_job_id=%q after_job_id=%q before_state=%q after_state=%q before_failure=%q after_failure=%q before_revision=%d after_revision=%d before_cleanup_phase=%q after_cleanup_phase=%q cleanup_owner_preserved=%t cleanup_fence_preserved=%t",
			initialJob.ID, finalJob.ID, initialJob.State, finalJob.State,
			initialJob.FailureCategory, finalJob.FailureCategory,
			initialJob.TransitionRevision, finalJob.TransitionRevision,
			initialJob.WorkspaceCleanupPhase, finalJob.WorkspaceCleanupPhase,
			finalJob.WorkspaceCleanupOwner == initialJob.WorkspaceCleanupOwner,
			finalJob.WorkspaceCleanupFence == initialJob.WorkspaceCleanupFence)
	}
	if err := prepared.permit.ValidateAt(fixture.now); !errorsIsTargetPermit(err) {
		t.Fatalf("PostgreSQL cancellation retained a usable first-write permit: %v", err)
	}
	var sourceLease model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	if sourceLease.RecoveryPointID != request.RecoveryPointID || sourceLease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("PostgreSQL cancellation source lease: lease_id=%q point_id=%q status=%q want_point_id=%q want_status=%q",
			sourceLease.ID, sourceLease.RecoveryPointID, sourceLease.Status,
			request.RecoveryPointID, backupasset.LeaseReleased)
	}
	var preservedOtherPoint model.RecoveryPoint
	if err := fixture.db.Where("id = ?", otherPoint.ID).Take(&preservedOtherPoint).Error; err != nil {
		t.Fatal(err)
	}
	var preservedOtherAttempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.Where("id = ?", otherAttempt.ID).Take(&preservedOtherAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if preservedOtherPoint.State != otherPoint.State || preservedOtherPoint.PointRevision != otherPoint.PointRevision ||
		preservedOtherAttempt.RecoveryPointID != otherPoint.ID || preservedOtherAttempt.Phase != otherAttempt.Phase ||
		preservedOtherAttempt.Operation != otherAttempt.Operation {
		t.Fatalf("PostgreSQL exact-point cancellation changed unrelated point/attempt: point_id=%q point_state=%q point_revision=%d attempt_id=%q attempt_point_id=%q attempt_operation=%q attempt_phase=%q",
			preservedOtherPoint.ID, preservedOtherPoint.State, preservedOtherPoint.PointRevision,
			preservedOtherAttempt.ID, preservedOtherAttempt.RecoveryPointID,
			preservedOtherAttempt.Operation, preservedOtherAttempt.Phase)
	}
	var preservedResultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ?", resultSet.ID).Take(&preservedResultSet).Error; err != nil {
		t.Fatal(err)
	}
	var preservedResult model.BackupAssetRecoveryResult
	if err := fixture.db.Where("id = ?", result.ID).Take(&preservedResult).Error; err != nil {
		t.Fatal(err)
	}
	if preservedResultSet.State != resultSet.State || preservedResultSet.CleanupPhase != resultSet.CleanupPhase ||
		preservedResultSet.CleanupOwner != resultSet.CleanupOwner || preservedResultSet.CleanupFence != resultSet.CleanupFence ||
		preservedResult.ID != result.ID || preservedResult.ResultSetID != result.ResultSetID ||
		preservedResult.JobID != result.JobID || preservedResult.LocatorDigest != result.LocatorDigest {
		t.Fatalf("PostgreSQL source cancellation changed Child 13 RecoveryResult ownership: set={%s} result={%s} cleanup_owner_preserved=%t cleanup_fence_preserved=%t locator_digest_preserved=%t",
			recoveryResultSetDiagnostic(preservedResultSet), recoveryResultDiagnostic(preservedResult),
			preservedResultSet.CleanupOwner == resultSet.CleanupOwner,
			preservedResultSet.CleanupFence == resultSet.CleanupFence,
			preservedResult.LocatorDigest == result.LocatorDigest)
	}
	t.Logf("PostgreSQL PrepareFirstWrite/source cancellation lock order prepare=%v cancel=%v retries=%d sqlstates=%v", prepareSequence, cancelSequence, retryCount, observedStates)
}

func cloneRecoverySourceLifecycleLockAttempts(source map[string]int) map[string]int {
	cloned := make(map[string]int, len(source))
	for table, count := range source {
		cloned[table] = count
	}
	return cloned
}

func assertRecoverySourceLifecycleLockOrder(t *testing.T, label string, sequence, required []string) {
	t.Helper()
	next := 0
	for _, table := range sequence {
		if next < len(required) && table == required[next] {
			next++
		}
	}
	if next != len(required) {
		t.Fatalf("%s PostgreSQL lock sequence=%v, want ordered subsequence %v", label, sequence, required)
	}
}

type recoverySourceLifecycleDriftCanceler struct {
	db       *gorm.DB
	delegate *WorkerCoordinator
	drift    func(*gorm.DB) error
}

type recoverySourceLifecycleCancelAfterPrecheck struct {
	delegate      recoveryPointSourceCanceler
	cancel        context.CancelFunc
	afterPrecheck chan struct{}
	once          sync.Once
}

func (canceler *recoverySourceLifecycleCancelAfterPrecheck) CancelRecoveryPoint(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
	jobID string,
) error {
	canceler.once.Do(func() {
		close(canceler.afterPrecheck)
		canceler.cancel()
	})
	return canceler.delegate.CancelRecoveryPoint(ctx, request, jobID)
}

func (canceler *recoverySourceLifecycleDriftCanceler) CancelJob(ctx context.Context, jobID string) error {
	if err := canceler.applyDrift(ctx); err != nil {
		return err
	}
	return canceler.delegate.CancelJob(ctx, jobID)
}

func (canceler *recoverySourceLifecycleDriftCanceler) CancelRecoveryPoint(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
	jobID string,
) error {
	if err := canceler.applyDrift(ctx); err != nil {
		return err
	}
	delegate, ok := any(canceler.delegate).(recoveryPointSourceCanceler)
	if !ok {
		return fmt.Errorf("%w: Recovery source cancellation handoff is unavailable", backupasset.ErrInvalidState)
	}
	return delegate.CancelRecoveryPoint(ctx, request, jobID)
}

func (canceler *recoverySourceLifecycleDriftCanceler) applyDrift(ctx context.Context) error {
	return canceler.db.WithContext(ctx).Transaction(canceler.drift)
}

type recoverySourceCancellationState struct {
	jobState                     string
	jobTransitionRevision        uint64
	jobFailureCategory           string
	jobWorkspacePhase            string
	jobWorkspaceCleanupPhase     string
	jobWorkspaceCleanupOwner     string
	jobWorkspaceCleanupExpiry    string
	jobWorkspaceCleanupFence     uint64
	jobWorkspaceCleanupNodeLease string
	jobWorkspaceCleanupNodeFence uint64
	jobWorkspaceCleanupAttempt   uint64
	attemptState                 string
	attemptClosedAt              string
	sourcePointID                string
	sourceStatus                 string
	sourceReleasedAt             string
	nodeState                    string
	nodeReleasedAt               string
}

func newRecoverySourceLifecycleCancellationFixture(
	t *testing.T,
) (*authorizationReceiptServiceFixture, *WorkerCoordinator, RecoveryWorkerClaim, backupasset.SourceLifecycleRequest) {
	t.Helper()
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "source-lifecycle-binding-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim Recovery source job: job_id=%q attempt_id=%q found=%t err=%v",
			claim.JobID, claim.AttemptID, found, err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	return fixture, coordinator, claim, seedRecoverySourceLifecycleRequest(t, fixture.db, plan.RecoveryPointID)
}

func seedRecoverySourceLifecycleRequest(
	t *testing.T,
	db *gorm.DB,
	recoveryPointID string,
) backupasset.SourceLifecycleRequest {
	t.Helper()
	if err := db.AutoMigrate(&model.RecoveryPointLifecycleAttempt{}); err != nil {
		t.Fatalf("migrate lifecycle attempt: %v", err)
	}
	lifecycleAttemptID := strings.Repeat("f", 32)
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: lifecycleAttemptID, RecoveryPointID: recoveryPointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
	}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	return backupasset.SourceLifecycleRequest{
		RecoveryPointID: recoveryPointID, LifecycleAttemptID: lifecycleAttemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	}
}

func loadRecoverySourceCancellationState(
	t *testing.T,
	db *gorm.DB,
	claim RecoveryWorkerClaim,
) recoverySourceCancellationState {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var source model.RecoveryPointLease
	if err := db.Where("id = ?", claim.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", claim.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatal(err)
	}
	return recoverySourceCancellationState{
		jobState: job.State, jobTransitionRevision: job.TransitionRevision,
		jobFailureCategory: job.FailureCategory, jobWorkspacePhase: job.WorkspacePhase,
		jobWorkspaceCleanupPhase: job.WorkspaceCleanupPhase, jobWorkspaceCleanupOwner: job.WorkspaceCleanupOwner,
		jobWorkspaceCleanupExpiry:    recoverySourceCancellationTime(job.WorkspaceCleanupLeaseExpiresAt),
		jobWorkspaceCleanupFence:     job.WorkspaceCleanupFence,
		jobWorkspaceCleanupNodeLease: recoverySourceCancellationString(job.WorkspaceCleanupNodeLeaseID),
		jobWorkspaceCleanupNodeFence: job.WorkspaceCleanupNodeFence,
		jobWorkspaceCleanupAttempt:   job.WorkspaceCleanupAttempt,
		attemptState:                 attempt.State, attemptClosedAt: recoverySourceCancellationTime(attempt.ClosedAt),
		sourcePointID: source.RecoveryPointID, sourceStatus: source.Status,
		sourceReleasedAt: recoverySourceCancellationTime(source.ReleasedAt),
		nodeState:        node.State, nodeReleasedAt: recoverySourceCancellationTime(node.ReleasedAt),
	}
}

func recoverySourceCancellationTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func recoverySourceCancellationString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func assertRecoverySourceLifecycleSettled(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
	claim RecoveryWorkerClaim,
	wantResultSet model.BackupAssetRecoveryResultSet,
	wantResult model.BackupAssetRecoveryResult,
) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if JobState(job.State) != JobStateCanceled ||
		job.WorkspaceCleanupPhase != string(CleanupPhaseClaimed) ||
		job.WorkspaceCleanupOwner != "" || job.WorkspaceCleanupFence != 0 {
		t.Fatalf("Recovery source cancellation crossed workspace cleanup ownership: job_id=%q state=%q cleanup_phase=%q cleanup_owner_set=%t cleanup_fence_set=%t",
			job.ID, job.State, job.WorkspaceCleanupPhase,
			job.WorkspaceCleanupOwner != "", job.WorkspaceCleanupFence != 0)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if AttemptState(attempt.State) != AttemptStateCanceled {
		t.Fatalf("Recovery attempt state=%q, want canceled", attempt.State)
	}
	var sourceLease model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	if sourceLease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("Recovery source lease state=%q, want released", sourceLease.Status)
	}

	var resultSet model.BackupAssetRecoveryResultSet
	if err := fixture.db.Where("id = ?", wantResultSet.ID).Take(&resultSet).Error; err != nil {
		t.Fatal(err)
	}
	if resultSet.State != wantResultSet.State || resultSet.CleanupPhase != wantResultSet.CleanupPhase ||
		resultSet.CleanupOwner != wantResultSet.CleanupOwner || resultSet.CleanupFence != wantResultSet.CleanupFence {
		t.Fatalf("RecoveryResult cleanup ownership changed: before={%s} after={%s} cleanup_owner_preserved=%t cleanup_fence_preserved=%t",
			recoveryResultSetDiagnostic(wantResultSet), recoveryResultSetDiagnostic(resultSet),
			resultSet.CleanupOwner == wantResultSet.CleanupOwner,
			resultSet.CleanupFence == wantResultSet.CleanupFence)
	}
	var result model.BackupAssetRecoveryResult
	if err := fixture.db.Where("id = ?", wantResult.ID).Take(&result).Error; err != nil {
		t.Fatal(err)
	}
	if result.ID != wantResult.ID || result.ResultSetID != wantResult.ResultSetID ||
		result.JobID != wantResult.JobID || result.LocatorDigest != wantResult.LocatorDigest {
		t.Fatalf("RecoveryResult changed: before={%s} after={%s} locator_digest_preserved=%t",
			recoveryResultDiagnostic(wantResult), recoveryResultDiagnostic(result),
			result.LocatorDigest == wantResult.LocatorDigest)
	}
}

func recoverySourceCancellationStateDiagnostic(state recoverySourceCancellationState) string {
	return fmt.Sprintf(
		"job_state=%q job_revision=%d job_failure=%q workspace_phase=%q cleanup_phase=%q cleanup_owner_set=%t cleanup_expiry_set=%t cleanup_fence_set=%t cleanup_node_lease_set=%t cleanup_node_fence_set=%t cleanup_attempt_present=%t attempt_state=%q attempt_closed=%t source_point_id=%q source_status=%q source_released=%t node_state=%q node_released=%t",
		state.jobState, state.jobTransitionRevision, state.jobFailureCategory, state.jobWorkspacePhase,
		state.jobWorkspaceCleanupPhase, state.jobWorkspaceCleanupOwner != "", state.jobWorkspaceCleanupExpiry != "",
		state.jobWorkspaceCleanupFence != 0, state.jobWorkspaceCleanupNodeLease != "",
		state.jobWorkspaceCleanupNodeFence != 0, state.jobWorkspaceCleanupAttempt != 0,
		state.attemptState, state.attemptClosedAt != "", state.sourcePointID, state.sourceStatus,
		state.sourceReleasedAt != "", state.nodeState, state.nodeReleasedAt != "",
	)
}

func recoveryResultSetDiagnostic(resultSet model.BackupAssetRecoveryResultSet) string {
	return fmt.Sprintf(
		"id=%q job_id=%q state=%q cleanup_phase=%q cleanup_owner_set=%t cleanup_fence_set=%t",
		resultSet.ID, resultSet.JobID, resultSet.State, resultSet.CleanupPhase,
		resultSet.CleanupOwner != "", resultSet.CleanupFence != 0,
	)
}

func recoveryResultDiagnostic(result model.BackupAssetRecoveryResult) string {
	return fmt.Sprintf(
		"id=%q result_set_id=%q job_id=%q kind=%q classification=%q",
		result.ID, result.ResultSetID, result.JobID, result.ResultKind, result.Classification,
	)
}
