package retention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/jackc/pgx/v5/pgconn"
	postgresgorm "gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestPolicyRulesCanonicalizationAndDigest(t *testing.T) {
	rules := PolicyRules{
		Version: PolicyRulesVersion1,
		Age:     &AgeRule{KeepDays: 30},
		Count:   &CountRule{KeepLatest: 7},
		Calendar: []CalendarRule{
			{Unit: CalendarMonth, Keep: 12},
			{Unit: CalendarDay, Keep: 14},
		},
	}

	canonical, digest, err := CanonicalizePolicyRules(rules)
	if err != nil {
		t.Fatalf("CanonicalizePolicyRules: %v", err)
	}
	want := `{"version":1,"age":{"keep_days":30},"count":{"keep_latest":7},"calendar":[{"unit":"day","keep":14},{"unit":"month","keep":12}]}`
	if canonical != want {
		t.Fatalf("canonical rules=%s, want %s", canonical, want)
	}
	wantDigest := sha256.Sum256([]byte(want))
	if digest != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("rule digest=%q, want %q", digest, hex.EncodeToString(wantDigest[:]))
	}

	parsed, err := ParsePolicyRules(canonical)
	if err != nil {
		t.Fatalf("ParsePolicyRules: %v", err)
	}
	parsedCanonical, parsedDigest, err := CanonicalizePolicyRules(parsed)
	if err != nil {
		t.Fatalf("canonicalize parsed rules: %v", err)
	}
	if parsedCanonical != canonical || parsedDigest != digest {
		t.Fatalf("round trip changed canonical rules: %q/%q", parsedCanonical, parsedDigest)
	}
}

func TestPolicyServiceVersionedCRUDUsesAdminAndRevisionCAS(t *testing.T) {
	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 17, 7, 0, 0, 0, time.UTC)
	ids := []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB:  db,
		Now: func() time.Time { return clock },
		NewID: func() (string, error) {
			id := ids[0]
			ids = ids[1:]
			return id, nil
		},
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}

	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	operator := backupasset.AuditActor{UserID: 2, Username: "operator", Role: "operator"}
	repositoryID := strings.Repeat("c", 32)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	rules := PolicyRules{Version: PolicyRulesVersion1, Count: &CountRule{KeepLatest: 3}}

	if _, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor: operator, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID, Rules: rules,
	}); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator create error=%v, want ErrForbidden", err)
	}

	created, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID, Rules: rules,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != strings.Repeat("a", 32) || created.Revision != 1 || created.Status != backupasset.RetentionPolicyActive ||
		created.CreatedBy != admin.UserID || created.UpdatedBy != admin.UserID || created.RuleDigest == "" {
		t.Fatalf("created policy mismatch: %+v", created)
	}
	if _, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID, Rules: rules,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("duplicate active scope error=%v, want ErrConflict", err)
	}

	updatedRules := PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 90}}
	if _, err := service.Update(context.Background(), UpdatePolicyRequest{
		Actor: admin, PolicyID: created.ID, ExpectedRevision: 2, Rules: updatedRules,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale update error=%v, want ErrConflict", err)
	}
	clock = clock.Add(time.Minute)
	updated, err := service.Update(context.Background(), UpdatePolicyRequest{
		Actor: admin, PolicyID: created.ID, ExpectedRevision: 1, Rules: updatedRules,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Revision != 2 || updated.Rules.Age == nil || updated.Rules.Age.KeepDays != 90 ||
		updated.RuleDigest == created.RuleDigest || !updated.UpdatedAt.Equal(clock) {
		t.Fatalf("updated policy mismatch: %+v", updated)
	}

	if _, err := service.Delete(context.Background(), DeletePolicyRequest{
		Actor: admin, PolicyID: created.ID, ExpectedRevision: 1,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale delete error=%v, want ErrConflict", err)
	}
	clock = clock.Add(time.Minute)
	deleted, err := service.Delete(context.Background(), DeletePolicyRequest{
		Actor: admin, PolicyID: created.ID, ExpectedRevision: 2,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if deleted.Revision != 3 || deleted.Status != backupasset.RetentionPolicyDeleted || deleted.DeletedAt == nil ||
		!deleted.DeletedAt.Equal(clock) {
		t.Fatalf("deleted policy mismatch: %+v", deleted)
	}
	if _, err := service.Update(context.Background(), UpdatePolicyRequest{
		Actor: admin, PolicyID: created.ID, ExpectedRevision: 3, Rules: rules,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("deleted policy update error=%v, want ErrConflict", err)
	}
	if _, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID, Rules: rules,
	}); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
}

func TestPolicySelectionDeterministicExactScopesAndExclusions(t *testing.T) {
	db := newRetentionTestDB(t)
	evaluatedAt := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB:  db,
		Now: func() time.Time { return evaluatedAt },
		NewID: func() (string, error) {
			return nextTestOpaqueID(), nil
		},
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	repositoryID := testOpaqueID(100)
	otherRepositoryID := testOpaqueID(101)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	otherRepository := model.BackupRepository{
		ID: otherRepositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "other-retention-test",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}
	if err := db.Create(&otherRepository).Error; err != nil {
		t.Fatalf("seed other repository: %v", err)
	}
	taskID := uint(77)
	otherTaskID := uint(88)
	linkID := testOpaqueID(102)
	if err := db.Create(&model.TaskRepositoryLink{
		ID: linkID, TaskID: &taskID, RepositoryID: repositoryID, TaskNameSnapshot: "task-77",
		PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: evaluatedAt.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed Task link: %v", err)
	}

	oldTaskPointID := testOpaqueID(1)
	oldOtherTaskPointID := testOpaqueID(2)
	recentTaskPointID := testOpaqueID(3)
	heldProjectionPointID := testOpaqueID(4)
	heldRecordPointID := testOpaqueID(5)
	points := []model.RecoveryPoint{
		newSelectionPoint(oldOtherTaskPointID, repositoryID, &otherTaskID, evaluatedAt.Add(-120*24*time.Hour), 12),
		newSelectionPoint(oldTaskPointID, repositoryID, &taskID, evaluatedAt.Add(-100*24*time.Hour), 11),
		newSelectionPoint(recentTaskPointID, repositoryID, &taskID, evaluatedAt.Add(-10*24*time.Hour), 13),
		newSelectionPoint(heldProjectionPointID, repositoryID, &taskID, evaluatedAt.Add(-90*24*time.Hour), 14),
		newSelectionPoint(heldRecordPointID, repositoryID, &taskID, evaluatedAt.Add(-80*24*time.Hour), 15),
		newSelectionPoint(testOpaqueID(6), otherRepositoryID, &taskID, evaluatedAt.Add(-120*24*time.Hour), 16),
		newSelectionPoint(testOpaqueID(7), repositoryID, &taskID, evaluatedAt.Add(-120*24*time.Hour), 17),
		newSelectionPoint(testOpaqueID(8), repositoryID, &taskID, evaluatedAt.Add(-120*24*time.Hour), 18),
		newSelectionPoint(testOpaqueID(9), repositoryID, &taskID, evaluatedAt.Add(-120*24*time.Hour), 19),
	}
	points[3].HoldState = string(backupasset.HoldActive)
	points[6].PhysicalAvailability = string(backupasset.PhysicalOffline)
	points[7].State = string(backupasset.RecoveryPointExpired)
	points[8].Semantics = string(backupasset.PointMutableHead)
	points[8].State = string(backupasset.RecoveryPointObserved)
	points[8].ImmutabilityLevel = string(backupasset.ImmutabilityMutable)
	points[8].ObservedAt = points[8].CapturedAt
	points[8].CapturedAt = nil
	if err := db.Create(&points).Error; err != nil {
		t.Fatalf("seed selection points: %v", err)
	}
	if err := db.Exec(`INSERT INTO recovery_point_holds
		(id, recovery_point_id, hold_type, state, encrypted_reason, created_by, created_at, updated_at)
		VALUES (?, ?, 'legal', 'active', 'enc:v2:FAKE_CIPHERTEXT_FOR_TEST_ONLY', 1, ?, ?)`,
		testOpaqueID(103), heldRecordPointID, evaluatedAt.Add(-time.Hour), evaluatedAt.Add(-time.Hour)).Error; err != nil {
		t.Fatalf("seed durable active hold: %v", err)
	}

	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	rules := PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 30}, Count: &CountRule{KeepLatest: 1}}
	repositoryPolicy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID, Rules: rules,
	})
	if err != nil {
		t.Fatalf("create repository policy: %v", err)
	}
	repositorySelection, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: repositoryPolicy.ID, ExpectedRevision: repositoryPolicy.Revision, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		t.Fatalf("select repository policy: %v", err)
	}
	wantRepositoryPoints := []SelectedPoint{
		{RecoveryPointID: oldTaskPointID, PointRevision: 1, CapabilityRevision: 11},
		{RecoveryPointID: oldOtherTaskPointID, PointRevision: 1, CapabilityRevision: 12},
	}
	if !reflect.DeepEqual(repositorySelection.Points, wantRepositoryPoints) {
		t.Fatalf("repository selection=%+v, want %+v", repositorySelection.Points, wantRepositoryPoints)
	}
	if repositorySelection.PolicyID != repositoryPolicy.ID || repositorySelection.PolicyRevision != 1 ||
		repositorySelection.RulesJSON != repositoryPolicy.RulesJSON || repositorySelection.RuleDigest != repositoryPolicy.RuleDigest ||
		!repositorySelection.EvaluatedAt.Equal(evaluatedAt) {
		t.Fatalf("repository selection snapshot mismatch: %+v", repositorySelection)
	}

	taskPolicy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeTaskLink, ScopeID: linkID, Rules: rules,
	})
	if err != nil {
		t.Fatalf("create Task-link policy: %v", err)
	}
	taskSelection, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: taskPolicy.ID, ExpectedRevision: taskPolicy.Revision, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		t.Fatalf("select Task-link policy: %v", err)
	}
	if want := []SelectedPoint{{RecoveryPointID: oldTaskPointID, PointRevision: 1, CapabilityRevision: 11}}; !reflect.DeepEqual(taskSelection.Points, want) {
		t.Fatalf("Task-link selection=%+v, want %+v", taskSelection.Points, want)
	}

	updated, err := service.Update(context.Background(), UpdatePolicyRequest{
		Actor: admin, PolicyID: repositoryPolicy.ID, ExpectedRevision: repositoryPolicy.Revision,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 365}},
	})
	if err != nil {
		t.Fatalf("update repository policy: %v", err)
	}
	if repositorySelection.PolicyRevision != 1 || repositorySelection.RuleDigest != repositoryPolicy.RuleDigest ||
		repositorySelection.RuleDigest == updated.RuleDigest {
		t.Fatalf("historical selection snapshot changed after policy update: %+v", repositorySelection)
	}
	if _, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: repositoryPolicy.ID, ExpectedRevision: 1, EvaluatedAt: evaluatedAt,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale selection error=%v, want ErrConflict", err)
	}

	now := evaluatedAt.Add(time.Minute)
	if err := db.Model(&model.TaskRepositoryLink{}).Where("id = ?", linkID).Update("unlinked_at", now).Error; err != nil {
		t.Fatalf("unlink Task scope: %v", err)
	}
	if _, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: taskPolicy.ID, ExpectedRevision: taskPolicy.Revision, EvaluatedAt: evaluatedAt,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("unlinked Task scope selection error=%v, want ErrConflict", err)
	}
}

func TestPolicySelectionCalendarKeepsOneRepresentativePerUTCPeriod(t *testing.T) {
	evaluatedAt := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	points := []model.RecoveryPoint{
		newSelectionPoint(testOpaqueID(1), testOpaqueID(100), nil, evaluatedAt.Add(-time.Hour), 1),
		newSelectionPoint(testOpaqueID(2), testOpaqueID(100), nil, evaluatedAt.Add(-2*time.Hour), 2),
		newSelectionPoint(testOpaqueID(3), testOpaqueID(100), nil, evaluatedAt.Add(-25*time.Hour), 3),
		newSelectionPoint(testOpaqueID(4), testOpaqueID(100), nil, evaluatedAt.Add(-49*time.Hour), 4),
	}

	selected, err := selectRecoveryPoints(selectionFacts(points...), PolicyRules{
		Version: PolicyRulesVersion1,
		Calendar: []CalendarRule{
			{Unit: CalendarDay, Keep: 2},
		},
	}, evaluatedAt)
	if err != nil {
		t.Fatalf("selectRecoveryPoints: %v", err)
	}
	want := []SelectedPoint{
		{RecoveryPointID: testOpaqueID(2), PointRevision: 1, CapabilityRevision: 2},
		{RecoveryPointID: testOpaqueID(4), PointRevision: 1, CapabilityRevision: 4},
	}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("calendar selection=%+v, want %+v", selected, want)
	}
}

func TestPolicySelectionLocksRowsAndBindsIndependentRevision(t *testing.T) {
	db := newRetentionTestDB(t)
	evaluatedAt := time.Date(2026, 8, 17, 8, 30, 0, 0, time.UTC)
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return evaluatedAt }, NewID: func() (string, error) { return testOpaqueID(410), nil },
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	repositoryID := testOpaqueID(400)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	oldPoint := newSelectionPoint(testOpaqueID(401), repositoryID, nil, evaluatedAt.Add(-48*time.Hour), 7)
	oldPoint.PointRevision = 41
	recentPoint := newSelectionPoint(testOpaqueID(402), repositoryID, nil, evaluatedAt.Add(-time.Hour), 8)
	recentPoint.PointRevision = 42
	if err := db.Create(&[]model.RecoveryPoint{oldPoint, recentPoint}).Error; err != nil {
		t.Fatalf("seed independently revisioned points: %v", err)
	}
	policy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Count: &CountRule{KeepLatest: 1}},
	})
	if err != nil {
		t.Fatalf("create locking policy: %v", err)
	}

	pointQueryLocked := false
	if err := db.Callback().Query().Before("gorm:query").Register("retention:test_point_lock", func(tx *gorm.DB) {
		if tx.Statement.Table != "recovery_points" {
			return
		}
		lockingClause, ok := tx.Statement.Clauses["FOR"]
		locking, isLocking := lockingClause.Expression.(clause.Locking)
		pointQueryLocked = ok && isLocking && locking.Strength == "UPDATE"
	}); err != nil {
		t.Fatalf("register point lock observer: %v", err)
	}
	selection, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		t.Fatalf("select independently revisioned point: %v", err)
	}
	if !pointQueryLocked {
		t.Fatal("retention point selection did not request a FOR UPDATE row lock")
	}
	want := []SelectedPoint{{RecoveryPointID: oldPoint.ID, PointRevision: 41, CapabilityRevision: 7}}
	if !reflect.DeepEqual(selection.Points, want) {
		t.Fatalf("revision-bound selection=%+v, want %+v", selection.Points, want)
	}

	invalidPointRevision := oldPoint
	invalidPointRevision.PointRevision = 0
	if _, err := selectRecoveryPoints(selectionFacts(invalidPointRevision), PolicyRules{
		Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1},
	}, evaluatedAt); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("zero point revision error=%v, want ErrInvalidState", err)
	}
	invalidCapabilityRevision := oldPoint
	invalidCapabilityRevision.CapabilityRevision = 0
	if _, err := selectRecoveryPoints(selectionFacts(invalidCapabilityRevision), PolicyRules{
		Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1},
	}, evaluatedAt); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("zero capability revision error=%v, want ErrInvalidState", err)
	}
}

func TestPolicySelectionPostgresLocksPointStateAndHoldDrift(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is required for the focused PostgreSQL point-lock regression")
	}
	db, err := gorm.Open(postgresgorm.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL retention test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("PostgreSQL retention sql database: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.User{}, &model.BackupRepository{}, &model.TaskRepositoryLink{}, &model.RecoveryPoint{},
		&model.BackupRetentionPolicy{}, &model.RecoveryPointHold{},
	); err != nil {
		t.Fatalf("migrate PostgreSQL retention test database: %v", err)
	}

	evaluatedAt := time.Date(2026, 8, 17, 8, 45, 0, 0, time.UTC)
	repositoryID := testOpaqueID(450)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	selectedPoint := newSelectionPoint(testOpaqueID(451), repositoryID, nil, evaluatedAt.Add(-48*time.Hour), 17)
	selectedPoint.PointRevision = 71
	recentPoint := newSelectionPoint(testOpaqueID(452), repositoryID, nil, evaluatedAt.Add(-time.Hour), 18)
	recentPoint.PointRevision = 72
	if err := db.Create(&[]model.RecoveryPoint{selectedPoint, recentPoint}).Error; err != nil {
		t.Fatalf("seed PostgreSQL locking points: %v", err)
	}
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return evaluatedAt }, NewID: func() (string, error) { return testOpaqueID(453), nil },
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	policy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Count: &CountRule{KeepLatest: 1}},
	})
	if err != nil {
		t.Fatalf("create PostgreSQL locking policy: %v", err)
	}

	selectionTx := db.Begin()
	if selectionTx.Error != nil {
		t.Fatalf("begin PostgreSQL selection transaction: %v", selectionTx.Error)
	}
	selection, err := service.SelectWithTx(context.Background(), selectionTx, SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		_ = selectionTx.Rollback().Error
		t.Fatalf("select PostgreSQL locked point: %v", err)
	}
	want := []SelectedPoint{{RecoveryPointID: selectedPoint.ID, PointRevision: 71, CapabilityRevision: 17}}
	if !reflect.DeepEqual(selection.Points, want) {
		_ = selectionTx.Rollback().Error
		t.Fatalf("PostgreSQL locked selection=%+v, want %+v", selection.Points, want)
	}

	writerPID := make(chan int, 1)
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- db.Transaction(func(tx *gorm.DB) error {
			var backendPID int
			if err := tx.Raw("SELECT pg_backend_pid()").Scan(&backendPID).Error; err != nil {
				return err
			}
			writerPID <- backendPID
			if err := tx.Model(&model.RecoveryPoint{}).Where("id = ?", selectedPoint.ID).
				Update("state", backupasset.RecoveryPointDegraded).Error; err != nil {
				return err
			}
			return tx.Create(&model.RecoveryPointHold{
				ID: testOpaqueID(454), RecoveryPointID: selectedPoint.ID,
				HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
				EncryptedReason: "enc:v2:FAKE_CIPHERTEXT_FOR_TEST_ONLY", CreatedBy: 1,
				CreatedAt: evaluatedAt, UpdatedAt: evaluatedAt,
			}).Error
		})
	}()
	var backendPID int
	select {
	case backendPID = <-writerPID:
	case err := <-writerDone:
		_ = selectionTx.Rollback().Error
		t.Fatalf("concurrent point state/hold writer failed before locking: %v", err)
	case <-time.After(3 * time.Second):
		_ = selectionTx.Rollback().Error
		t.Fatal("concurrent point state/hold writer did not start within the bound")
	}
	lockDeadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-writerDone:
			_ = selectionTx.Rollback().Error
			t.Fatalf("concurrent point state/hold drift was not fenced: %v", err)
		default:
		}
		var waitEventType string
		if err := db.Raw(`SELECT COALESCE(wait_event_type, '') FROM pg_stat_activity WHERE pid = ?`, backendPID).
			Scan(&waitEventType).Error; err != nil {
			_ = selectionTx.Rollback().Error
			t.Fatalf("inspect concurrent point writer lock: %v", err)
		}
		if waitEventType == "Lock" {
			break
		}
		if time.Now().After(lockDeadline) {
			_ = selectionTx.Rollback().Error
			t.Fatal("concurrent point state/hold writer did not reach the expected row lock within the bound")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := selectionTx.Rollback().Error; err != nil {
		t.Fatalf("release PostgreSQL selection lock: %v", err)
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("apply point state/hold drift after lock release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent point state/hold drift remained blocked after selection transaction ended")
	}
}

func TestPolicySelectionPostgresLocksTaskLinkDrift(t *testing.T) {
	db := newIsolatedRetentionPostgresTestDB(t)
	evaluatedAt := time.Date(2026, 8, 17, 8, 50, 0, 0, time.UTC)
	repositoryID := testOpaqueID(460)
	reboundRepositoryID := testOpaqueID(461)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	if err := db.Create(&model.BackupRepository{
		ID: reboundRepositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "retention-rebound-test",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed PostgreSQL rebound repository: %v", err)
	}
	taskID := uint(460)
	reboundTaskID := uint(461)
	linkID := testOpaqueID(462)
	if err := db.Create(&model.TaskRepositoryLink{
		ID: linkID, TaskID: &taskID, RepositoryID: repositoryID, TaskNameSnapshot: "task-460",
		PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: evaluatedAt.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed PostgreSQL Task link: %v", err)
	}
	selectedPoint := newSelectionPoint(testOpaqueID(463), repositoryID, &taskID, evaluatedAt.Add(-48*time.Hour), 21)
	recentPoint := newSelectionPoint(testOpaqueID(464), repositoryID, &taskID, evaluatedAt.Add(-time.Hour), 22)
	if err := db.Create(&[]model.RecoveryPoint{selectedPoint, recentPoint}).Error; err != nil {
		t.Fatalf("seed PostgreSQL Task-link points: %v", err)
	}
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return evaluatedAt }, NewID: func() (string, error) { return testOpaqueID(465), nil },
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	policy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeTaskLink, ScopeID: linkID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Count: &CountRule{KeepLatest: 1}},
	})
	if err != nil {
		t.Fatalf("create PostgreSQL Task-link policy: %v", err)
	}

	selectionTx := db.Begin()
	if selectionTx.Error != nil {
		t.Fatalf("begin PostgreSQL Task-link selection transaction: %v", selectionTx.Error)
	}
	selection, err := service.SelectWithTx(context.Background(), selectionTx, SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		_ = selectionTx.Rollback().Error
		t.Fatalf("select PostgreSQL Task-link policy: %v", err)
	}
	want := []SelectedPoint{{RecoveryPointID: selectedPoint.ID, PointRevision: 1, CapabilityRevision: 21}}
	if !reflect.DeepEqual(selection.Points, want) {
		_ = selectionTx.Rollback().Error
		t.Fatalf("PostgreSQL Task-link selection=%+v, want %+v", selection.Points, want)
	}

	writerPID := make(chan int, 1)
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- db.Transaction(func(tx *gorm.DB) error {
			var backendPID int
			if err := tx.Raw("SELECT pg_backend_pid()").Scan(&backendPID).Error; err != nil {
				return err
			}
			writerPID <- backendPID
			return tx.Model(&model.TaskRepositoryLink{}).Where("id = ?", linkID).Updates(map[string]any{
				"repository_id": reboundRepositoryID,
				"task_id":       reboundTaskID,
				"unlinked_at":   evaluatedAt,
			}).Error
		})
	}()
	var backendPID int
	select {
	case backendPID = <-writerPID:
	case err := <-writerDone:
		_ = selectionTx.Rollback().Error
		t.Fatalf("concurrent Task-link writer failed before locking: %v", err)
	case <-time.After(3 * time.Second):
		_ = selectionTx.Rollback().Error
		t.Fatal("concurrent Task-link writer did not start within the bound")
	}
	lockDeadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-writerDone:
			_ = selectionTx.Rollback().Error
			t.Fatalf("concurrent Task-link unlink/rebind was not fenced: %v", err)
		default:
		}
		var waitEventType string
		if err := db.Raw(`SELECT COALESCE(wait_event_type, '') FROM pg_stat_activity WHERE pid = ?`, backendPID).
			Scan(&waitEventType).Error; err != nil {
			_ = selectionTx.Rollback().Error
			t.Fatalf("inspect concurrent Task-link writer lock: %v", err)
		}
		if waitEventType == "Lock" {
			break
		}
		if time.Now().After(lockDeadline) {
			_ = selectionTx.Rollback().Error
			t.Fatal("concurrent Task-link writer did not reach the expected row lock within the bound")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := selectionTx.Rollback().Error; err != nil {
		t.Fatalf("release PostgreSQL Task-link selection lock: %v", err)
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("apply Task-link unlink/rebind after lock release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent Task-link writer remained blocked after selection transaction ended")
	}
}

func TestPolicyConcurrentCreatePostgresMapsActiveScopeConflict(t *testing.T) {
	db := newIsolatedRetentionPostgresTestDB(t)
	if err := db.Exec(`CREATE UNIQUE INDEX idx_backup_retention_policies_active_scope
		ON backup_retention_policies(scope_kind, scope_id) WHERE status = 'active'`).Error; err != nil {
		t.Fatalf("create PostgreSQL active policy scope index: %v", err)
	}
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(469)
	seedRetentionUsersAndRepository(t, db, repositoryID)

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	allocations := make(chan struct{}, 2)
	go func() {
		select {
		case <-arrived:
		case <-time.After(2 * time.Second):
			close(release)
			return
		}
		select {
		case <-arrived:
		case <-time.After(500 * time.Millisecond):
		}
		close(release)
	}()
	newService := func(policyID string) *PolicyService {
		service, err := NewPolicyService(PolicyServiceDependencies{
			DB: db, Now: func() time.Time { return now },
			NewID: func() (string, error) {
				allocations <- struct{}{}
				arrived <- struct{}{}
				<-release
				return policyID, nil
			},
		})
		if err != nil {
			t.Fatalf("NewPolicyService: %v", err)
		}
		return service
	}
	services := []*PolicyService{newService(testOpaqueID(470)), newService(testOpaqueID(471))}
	request := CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Count: &CountRule{KeepLatest: 1}},
	}
	start := make(chan struct{})
	results := make(chan error, len(services))
	for _, service := range services {
		go func(service *PolicyService) {
			<-start
			_, err := service.Create(context.Background(), request)
			results <- err
		}(service)
	}
	close(start)

	var successes, conflicts int
	var unexpected []error
	for range services {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, backupasset.ErrConflict):
			conflicts++
		default:
			unexpected = append(unexpected, err)
		}
	}
	var activeCount int64
	if err := db.Model(&model.BackupRetentionPolicy{}).
		Where("scope_kind = ? AND scope_id = ? AND status = ?", request.ScopeKind, request.ScopeID, backupasset.RetentionPolicyActive).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count PostgreSQL active policies: %v", err)
	}
	if successes != 1 || conflicts != 1 || len(unexpected) != 0 || activeCount != 1 || len(allocations) != 1 {
		t.Fatalf("concurrent policy creates: successes=%d conflicts=%d unexpected=%v active=%d ID allocations=%d; want 1/1/none/1/1",
			successes, conflicts, unexpected, activeCount, len(allocations))
	}
	exactScopeConflict := &pgconn.PgError{Code: "23505", ConstraintName: "idx_backup_retention_policies_active_scope"}
	if mapped := mapPolicyCreateError(db, request.ScopeKind, request.ScopeID, exactScopeConflict); !errors.Is(mapped, backupasset.ErrConflict) {
		t.Fatalf("exact PostgreSQL active-scope conflict mapping=%v, want ErrConflict", mapped)
	}
	unrelatedConflict := &pgconn.PgError{Code: "23505", ConstraintName: "idx_unrelated_unique_constraint"}
	if mapped := mapPolicyCreateError(db, request.ScopeKind, request.ScopeID, unrelatedConflict); errors.Is(mapped, backupasset.ErrConflict) || !errors.Is(mapped, unrelatedConflict) {
		t.Fatalf("unrelated PostgreSQL unique mapping=%v, want preserved source error without ErrConflict", mapped)
	}
}

func selectionFacts(points ...model.RecoveryPoint) []selectionFact {
	facts := make([]selectionFact, 0, len(points))
	for _, point := range points {
		facts = append(facts, selectionFact{
			id: point.ID, pointRevision: point.PointRevision,
			capabilityRevision: point.CapabilityRevision, capturedAt: point.CapturedAt,
		})
	}
	return facts
}

func newSelectionPoint(id, repositoryID string, taskID *uint, capturedAt time.Time, capabilityRevision int) model.RecoveryPoint {
	return model.RecoveryPoint{
		ID: id, RepositoryID: repositoryID, ProducingTaskID: taskID,
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		CapturedAt: &capturedAt, CommittedAt: &capturedAt, PointRevision: 1, CapabilityRevision: capabilityRevision,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
	}
}

var testOpaqueIDCounter uint64 = 1000

func nextTestOpaqueID() string {
	testOpaqueIDCounter++
	return testOpaqueID(testOpaqueIDCounter)
}

func testOpaqueID(value uint64) string {
	return fmt.Sprintf("%032x", value)
}

func enableRetentionTestEncryption(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func newRetentionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	enableRetentionTestEncryption(t)
	dsn := filepath.Join(t.TempDir(), "retention.db") + "?_loc=UTC&_foreign_keys=on&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open retention test database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.BackupRepository{},
		&model.TaskRepositoryLink{},
		&model.RecoveryPoint{},
		&model.BackupRetentionPolicy{},
		&model.RecoveryPointHold{},
	); err != nil {
		t.Fatalf("migrate retention test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("retention test sql database: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newIsolatedRetentionPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	enableRetentionTestEncryption(t)
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is required for focused PostgreSQL retention regressions")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	adminDB, err := gorm.Open(postgresgorm.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL retention fixture database: %v", err)
	}
	digest := sha256.Sum256([]byte(t.Name()))
	schema := fmt.Sprintf("retention_%x", digest[:8])
	if err := adminDB.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create isolated PostgreSQL retention schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgresgorm.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		_ = adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		t.Fatalf("open isolated PostgreSQL retention schema: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("isolated PostgreSQL retention sql database: %v", err)
	}
	adminSQLDB, err := adminDB.DB()
	if err != nil {
		t.Fatalf("PostgreSQL retention fixture sql database: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		_ = adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = adminSQLDB.Close()
	})
	if err := db.AutoMigrate(
		&model.User{}, &model.BackupRepository{}, &model.TaskRepositoryLink{}, &model.RecoveryPoint{},
		&model.BackupRetentionPolicy{}, &model.RecoveryPointHold{},
	); err != nil {
		t.Fatalf("migrate isolated PostgreSQL retention schema: %v", err)
	}
	return db
}

func seedRetentionUsersAndRepository(t *testing.T, db *gorm.DB, repositoryID string) {
	t.Helper()
	users := []model.User{
		{ID: 1, Username: "admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"},
		{ID: 2, Username: "operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator"},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatalf("seed retention users: %v", err)
	}
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "retention-test",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("seed retention repository: %v", err)
	}
}

func TestPolicyServiceListActiveAfterUsesKeysetCursor(t *testing.T) {
	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	ids := []string{testOpaqueID(11), testOpaqueID(12), testOpaqueID(13)}
	next := 0
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) {
			id := ids[next]
			next++
			return id, nil
		},
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	seedRetentionUsersAndRepository(t, db, testOpaqueID(21))
	if err := db.Create(&[]model.BackupRepository{
		{
			ID: testOpaqueID(22), ProviderKind: string(backupasset.ProviderRestic), DisplayName: "repo-b",
			VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
			CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		},
		{
			ID: testOpaqueID(23), ProviderKind: string(backupasset.ProviderRestic), DisplayName: "repo-c",
			VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
			CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		},
	}).Error; err != nil {
		t.Fatalf("seed extra repositories: %v", err)
	}
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	for _, repositoryID := range []string{testOpaqueID(21), testOpaqueID(22), testOpaqueID(23)} {
		if _, err := service.Create(context.Background(), CreatePolicyRequest{
			Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
			Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 30}},
		}); err != nil {
			t.Fatalf("create policy for %s: %v", repositoryID, err)
		}
	}
	first, err := service.ListActiveAfter(context.Background(), 1, "")
	if err != nil || len(first) != 1 || first[0].ID != testOpaqueID(11) {
		t.Fatalf("first page=%+v err=%v, want id %s", first, err, testOpaqueID(11))
	}
	second, err := service.ListActiveAfter(context.Background(), 1, first[0].ID)
	if err != nil || len(second) != 1 || second[0].ID != testOpaqueID(12) {
		t.Fatalf("second page=%+v err=%v, want id %s", second, err, testOpaqueID(12))
	}
	third, err := service.ListActiveAfter(context.Background(), 1, second[0].ID)
	if err != nil || len(third) != 1 || third[0].ID != testOpaqueID(13) {
		t.Fatalf("third page=%+v err=%v, want id %s", third, err, testOpaqueID(13))
	}
}

func TestPolicySelectWithTxDoesNotAccumulateEveryRecoveryPoint(t *testing.T) {
	db := newRetentionTestDB(t)
	evaluatedAt := time.Date(2026, 8, 19, 8, 45, 0, 0, time.UTC)
	repositoryID := testOpaqueID(51)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	const pointCount = 8
	points := make([]model.RecoveryPoint, 0, pointCount)
	for index := 0; index < pointCount; index++ {
		point := newSelectionPoint(testOpaqueID(uint64(60+index)), repositoryID, nil, evaluatedAt.Add(-48*time.Hour), 4)
		point.PointRevision = int64(10 + index)
		points = append(points, point)
	}
	if err := db.Create(&points).Error; err != nil {
		t.Fatalf("seed streamed selection points: %v", err)
	}
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return evaluatedAt },
		NewID: func() (string, error) { return testOpaqueID(59), nil },
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	service.selectionPageSize = 3
	var pageLengths []int
	service.selectionPageObserver = func(pageLen int) {
		pageLengths = append(pageLengths, pageLen)
	}
	policy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1}},
	})
	if err != nil {
		t.Fatalf("create streamed selection policy: %v", err)
	}
	selection, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(selection.Points) != pointCount {
		t.Fatalf("streamed Select points=%d, want %d", len(selection.Points), pointCount)
	}
	if len(pageLengths) != 3 || pageLengths[0] != 3 || pageLengths[1] != 3 || pageLengths[2] != 2 {
		t.Fatalf("selection pages=%v, want [3 3 2] without one accumulated RecoveryPoint slice", pageLengths)
	}
}

func TestPolicySelectWithTxPagesEveryScopedPoint(t *testing.T) {
	db := newRetentionTestDB(t)
	evaluatedAt := time.Date(2026, 8, 19, 8, 30, 0, 0, time.UTC)
	repositoryID := testOpaqueID(31)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	points := make([]model.RecoveryPoint, 0, 5)
	for index := 0; index < 5; index++ {
		point := newSelectionPoint(testOpaqueID(uint64(40+index)), repositoryID, nil, evaluatedAt.Add(-48*time.Hour), 4)
		point.PointRevision = int64(10 + index)
		points = append(points, point)
	}
	if err := db.Create(&points).Error; err != nil {
		t.Fatalf("seed paged selection points: %v", err)
	}
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return evaluatedAt },
		NewID: func() (string, error) { return testOpaqueID(39), nil },
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	service.selectionPageSize = 2
	policy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1}},
	})
	if err != nil {
		t.Fatalf("create paged selection policy: %v", err)
	}
	selection, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(selection.Points) != 5 {
		t.Fatalf("paged Select points=%d, want 5 across page size 2", len(selection.Points))
	}
	seen := map[string]bool{}
	for _, point := range selection.Points {
		seen[point.RecoveryPointID] = true
	}
	for index := 0; index < 5; index++ {
		id := testOpaqueID(uint64(40 + index))
		if !seen[id] {
			t.Fatalf("paged Select omitted expire-eligible point %s", id)
		}
	}
}

func TestPolicySelectInspectedBudgetReturnsCursorForKeptOnlyPage(t *testing.T) {
	db := newRetentionTestDB(t)
	evaluatedAt := time.Date(2026, 8, 19, 21, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(71)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	const keptCount = 6
	points := make([]model.RecoveryPoint, 0, keptCount)
	for index := 0; index < keptCount; index++ {
		point := newSelectionPoint(testOpaqueID(uint64(80+index)), repositoryID, nil, evaluatedAt.Add(-time.Duration(index+1)*time.Hour), 4)
		point.PointRevision = int64(20 + index)
		points = append(points, point)
	}
	if err := db.Create(&points).Error; err != nil {
		t.Fatalf("seed kept-only selection points: %v", err)
	}
	nextPolicyID := uint64(79)
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return evaluatedAt },
		NewID: func() (string, error) {
			id := testOpaqueID(nextPolicyID)
			nextPolicyID++
			return id, nil
		},
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	service.selectionPageSize = 200
	var pageLengths []int
	service.selectionPageObserver = func(pageLen int) {
		pageLengths = append(pageLengths, pageLen)
	}
	policy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 365}},
	})
	if err != nil {
		t.Fatalf("create kept-only selection policy: %v", err)
	}
	first, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt,
		Limit: 50, InspectedLimit: 2,
	})
	if err != nil {
		t.Fatalf("kept-only Select: %v", err)
	}
	if len(first.Points) != 0 {
		t.Fatalf("kept-only page selected=%d, want 0", len(first.Points))
	}
	if first.NextCursor == "" {
		t.Fatal("kept-only inspected budget returned empty cursor")
	}
	if first.Inspected != 2 {
		t.Fatalf("kept-only inspected=%d, want 2", first.Inspected)
	}
	if len(pageLengths) != 1 || pageLengths[0] != 2 {
		t.Fatalf("kept-only pages=%v, want one FOR UPDATE page of 2 not the remaining table", pageLengths)
	}

	pageLengths = nil
	second, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt,
		Limit: 50, InspectedLimit: 2, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("resume kept-only Select: %v", err)
	}
	if len(second.Points) != 0 || second.NextCursor == "" || second.Inspected != 2 {
		t.Fatalf("resume kept-only selected=%d inspected=%d cursor=%q", len(second.Points), second.Inspected, second.NextCursor)
	}

	expireRepo := testOpaqueID(72)
	if err := db.Create(&model.BackupRepository{
		ID: expireRepo, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "expire-limit-repo",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}).Error; err != nil {
		t.Fatalf("seed expire repository: %v", err)
	}
	expirePoints := make([]model.RecoveryPoint, 0, 5)
	for index := 0; index < 5; index++ {
		point := newSelectionPoint(testOpaqueID(uint64(90+index)), expireRepo, nil, evaluatedAt.Add(-48*time.Hour), 4)
		point.PointRevision = int64(30 + index)
		expirePoints = append(expirePoints, point)
	}
	if err := db.Create(&expirePoints).Error; err != nil {
		t.Fatalf("seed expire selection points: %v", err)
	}
	expirePolicy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: expireRepo,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 1}},
	})
	if err != nil {
		t.Fatalf("create expire selection policy: %v", err)
	}
	pageLengths = nil
	expireSelection, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: expirePolicy.ID, ExpectedRevision: expirePolicy.Revision, EvaluatedAt: evaluatedAt,
		Limit: 2, InspectedLimit: 10,
	})
	if err != nil {
		t.Fatalf("expire Limit Select: %v", err)
	}
	if len(expireSelection.Points) != 2 {
		t.Fatalf("expire Limit selected=%d, want 2", len(expireSelection.Points))
	}
	if expireSelection.NextCursor == "" {
		t.Fatal("expire Limit should still encode a continuation cursor")
	}
}

func TestPolicySelectCursorRejectsPolicySnapshotMismatch(t *testing.T) {
	db := newRetentionTestDB(t)
	evaluatedAt := time.Date(2026, 8, 19, 21, 15, 0, 0, time.UTC)
	repositoryID := testOpaqueID(111)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	points := make([]model.RecoveryPoint, 0, 4)
	for index := 0; index < 4; index++ {
		point := newSelectionPoint(testOpaqueID(uint64(120+index)), repositoryID, nil, evaluatedAt.Add(-time.Duration(30+index*31)*24*time.Hour), 4)
		point.PointRevision = int64(40 + index)
		points = append(points, point)
	}
	if err := db.Create(&points).Error; err != nil {
		t.Fatalf("seed snapshot cursor points: %v", err)
	}
	nextPolicyID := uint64(119)
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return evaluatedAt },
		NewID: func() (string, error) {
			id := testOpaqueID(nextPolicyID)
			nextPolicyID++
			return id, nil
		},
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	policy, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Calendar: []CalendarRule{{Unit: CalendarMonth, Keep: 1}}},
	})
	if err != nil {
		t.Fatalf("create monthly policy: %v", err)
	}
	first, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt, Limit: 1,
	})
	if err != nil || first.NextCursor == "" || len(first.Points) != 1 {
		t.Fatalf("first monthly page selected=%d cursor=%q err=%v", len(first.Points), first.NextCursor, err)
	}
	resumed, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt, Limit: 1, Cursor: first.NextCursor,
	})
	if err != nil || len(resumed.Points) != 1 || resumed.Points[0].RecoveryPointID == first.Points[0].RecoveryPointID {
		t.Fatalf("matching snapshot resume selected=%+v err=%v", resumed.Points, err)
	}

	later := evaluatedAt.Add(time.Hour)
	if _, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: later, Limit: 1, Cursor: first.NextCursor,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("EvaluatedAt mismatch error=%v, want ErrConflict", err)
	}

	updated, err := service.Update(context.Background(), UpdatePolicyRequest{
		Actor: admin, PolicyID: policy.ID, ExpectedRevision: policy.Revision,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Calendar: []CalendarRule{{Unit: CalendarYear, Keep: 1}}},
	})
	if err != nil {
		t.Fatalf("switch policy to yearly: %v", err)
	}
	if _, err := service.Select(context.Background(), SelectionRequest{
		PolicyID: updated.ID, ExpectedRevision: updated.Revision, EvaluatedAt: evaluatedAt, Limit: 1, Cursor: first.NextCursor,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("rule digest mismatch error=%v, want ErrConflict", err)
	}
}

type failingMutationAuditor struct{}

func (failingMutationAuditor) WriteTx(context.Context, *gorm.DB, backupasset.AuditEventInput) error {
	return errors.New("mutation audit unavailable")
}

type recordingMutationAuditor struct {
	writes int
	last   backupasset.AuditEventInput
}

func (auditor *recordingMutationAuditor) WriteTx(_ context.Context, _ *gorm.DB, event backupasset.AuditEventInput) error {
	auditor.writes++
	auditor.last = event
	return nil
}

func TestPolicyCreateAuditFailureRollsBackRow(t *testing.T) {
	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 19, 18, 0, 0, 0, time.UTC)
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(81), nil },
		Audit: failingMutationAuditor{},
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	repositoryID := testOpaqueID(80)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	_, err = service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 14}},
	})
	if err == nil {
		t.Fatal("Create succeeded despite audit failure")
	}
	var count int64
	if err := db.Model(&model.BackupRetentionPolicy{}).Count(&count).Error; err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if count != 0 {
		t.Fatalf("policy rows=%d, want 0 after failed mutation audit", count)
	}
}

func TestPolicyCreateWritesMutationAuditOnSuccess(t *testing.T) {
	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 19, 18, 5, 0, 0, time.UTC)
	auditor := &recordingMutationAuditor{}
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(83), nil },
		Audit: auditor,
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	repositoryID := testOpaqueID(82)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	created, err := service.Create(context.Background(), CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 14}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" || auditor.writes != 1 {
		t.Fatalf("created=%+v audit writes=%d, want 1", created, auditor.writes)
	}
	if correlation, _ := auditor.last.Fields[backupasset.AuditFieldCorrelationID].(string); correlation != "" {
		t.Fatalf("empty request context leaked correlation=%v", auditor.last.Fields[backupasset.AuditFieldCorrelationID])
	}
	if auditor.last.Fields[backupasset.AuditField("policy_id")] != created.ID {
		t.Fatalf("policy audit fields=%+v, want policy_id=%s", auditor.last.Fields, created.ID)
	}
	if auditor.last.RepositoryID != repositoryID {
		t.Fatalf("repository audit id=%q, want %q", auditor.last.RepositoryID, repositoryID)
	}
}

func TestPolicyCreateWritesMutationAuditRequestAndTaskLinkTarget(t *testing.T) {
	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 19, 18, 10, 0, 0, time.UTC)
	auditor := &recordingMutationAuditor{}
	service, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(85), nil },
		Audit: auditor,
	})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	repositoryID := testOpaqueID(84)
	linkID := testOpaqueID(86)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	taskID := uint(91)
	if err := db.Create(&model.TaskRepositoryLink{
		ID: linkID, TaskID: &taskID, RepositoryID: repositoryID, TaskNameSnapshot: "audit-link",
		PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: clock.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed Task link: %v", err)
	}
	ctx := WithRequestCorrelationID(context.Background(), "corr-retention-create")
	created, err := service.Create(ctx, CreatePolicyRequest{
		Actor:     backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		ScopeKind: backupasset.RetentionPolicyScopeTaskLink, ScopeID: linkID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 14}},
	})
	if err != nil {
		t.Fatalf("Create task-link policy: %v", err)
	}
	if auditor.writes != 1 {
		t.Fatalf("audit writes=%d, want 1", auditor.writes)
	}
	if auditor.last.Fields[backupasset.AuditFieldCorrelationID] != "corr-retention-create" {
		t.Fatalf("correlation=%v, want corr-retention-create", auditor.last.Fields[backupasset.AuditFieldCorrelationID])
	}
	if auditor.last.Fields[backupasset.AuditField("policy_id")] != created.ID {
		t.Fatalf("policy_id=%v, want %s", auditor.last.Fields[backupasset.AuditField("policy_id")], created.ID)
	}
	if auditor.last.RepositoryID != repositoryID {
		t.Fatalf("task-link repository id=%q, want %q", auditor.last.RepositoryID, repositoryID)
	}
}
