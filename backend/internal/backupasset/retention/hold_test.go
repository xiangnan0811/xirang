package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestHoldCreateAuditFailureRollsBackRow(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 19, 18, 10, 0, 0, time.UTC)
	service, err := NewHoldService(HoldServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(221), nil },
		Audit: failingMutationAuditor{},
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	repositoryID := testOpaqueID(220)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(testOpaqueID(222), repositoryID, nil, clock.Add(-24*time.Hour), 1)
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed hold point: %v", err)
	}
	_, err = service.Create(context.Background(), CreateHoldRequest{
		Actor:           backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldLegal,
		Reason: "FAKE_LEGAL_HOLD_REASON_FOR_TEST_ONLY",
	})
	if err == nil {
		t.Fatal("Create succeeded despite audit failure")
	}
	var count int64
	if err := db.Model(&model.RecoveryPointHold{}).Count(&count).Error; err != nil {
		t.Fatalf("count holds: %v", err)
	}
	if count != 0 {
		t.Fatalf("hold rows=%d, want 0 after failed mutation audit", count)
	}
}

func TestHoldCreateReleaseEncryptsReasonsAndProjectsAtomically(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	ids := []string{testOpaqueID(201), testOpaqueID(202)}
	service, err := NewHoldService(HoldServiceDependencies{
		DB:  db,
		Now: func() time.Time { return clock },
		NewID: func() (string, error) {
			id := ids[0]
			ids = ids[1:]
			return id, nil
		},
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	repositoryID := testOpaqueID(200)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(testOpaqueID(203), repositoryID, nil, clock.Add(-24*time.Hour), 1)
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed held point: %v", err)
	}
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	operator := backupasset.AuditActor{UserID: 2, Username: "operator", Role: "operator"}
	expiresAt := clock.Add(48 * time.Hour)
	createReason := "FAKE_OPERATIONAL_HOLD_REASON_FOR_TEST_ONLY"
	assertHoldRequestSafeJSON(t, CreateHoldRequest{Reason: createReason}, createReason)

	if _, err := service.Create(context.Background(), CreateHoldRequest{
		Actor: operator, RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: createReason, ExpiresAt: &expiresAt,
	}); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator create error=%v, want ErrForbidden", err)
	}
	operational, err := service.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: createReason, ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatalf("create operational hold: %v", err)
	}
	if operational.ID != testOpaqueID(201) || operational.State != backupasset.HoldActive ||
		operational.ExpiresAt == nil || !operational.ExpiresAt.Equal(expiresAt) || operational.CreatedBy != admin.UserID {
		t.Fatalf("operational hold mismatch: %+v", operational)
	}
	assertHoldSafeJSON(t, operational, createReason)
	assertEncryptedHoldColumn(t, db, operational.ID, "encrypted_reason", createReason)
	assertPointHoldProjection(t, db, point.ID, backupasset.HoldActive, &expiresAt)

	if _, err := service.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: "FAKE_DUPLICATE_REASON_FOR_TEST_ONLY", ExpiresAt: &expiresAt,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("duplicate operational hold error=%v, want ErrConflict", err)
	}
	legalReason := "FAKE_LEGAL_HOLD_REASON_FOR_TEST_ONLY"
	legal, err := service.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldLegal, Reason: legalReason,
	})
	if err != nil {
		t.Fatalf("create legal hold: %v", err)
	}
	assertHoldSafeJSON(t, legal, legalReason)
	assertEncryptedHoldColumn(t, db, legal.ID, "encrypted_reason", legalReason)
	assertPointHoldProjection(t, db, point.ID, backupasset.HoldActive, nil)

	releaseReason := "FAKE_OPERATIONAL_RELEASE_REASON_FOR_TEST_ONLY"
	assertHoldRequestSafeJSON(t, ReleaseHoldRequest{Reason: releaseReason}, releaseReason)
	if _, err := service.Release(context.Background(), ReleaseHoldRequest{
		Actor: operator, RecoveryPointID: point.ID, HoldID: operational.ID, Reason: releaseReason,
	}); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator release error=%v, want ErrForbidden", err)
	}
	clock = clock.Add(time.Minute)
	releasedOperational, err := service.Release(context.Background(), ReleaseHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldID: operational.ID, Reason: releaseReason,
	})
	if err != nil {
		t.Fatalf("release operational hold: %v", err)
	}
	if releasedOperational.State != backupasset.HoldReleased || releasedOperational.ReleasedBy == nil ||
		*releasedOperational.ReleasedBy != admin.UserID || releasedOperational.ReleasedAt == nil ||
		!releasedOperational.ReleasedAt.Equal(clock) {
		t.Fatalf("released operational hold mismatch: %+v", releasedOperational)
	}
	assertHoldSafeJSON(t, releasedOperational, releaseReason)
	assertEncryptedHoldColumn(t, db, operational.ID, "encrypted_release_reason", releaseReason)
	assertPointHoldProjection(t, db, point.ID, backupasset.HoldActive, nil)
	if _, err := service.Release(context.Background(), ReleaseHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldID: operational.ID, Reason: releaseReason,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("second release error=%v, want ErrConflict", err)
	}

	clock = clock.Add(time.Minute)
	if _, err := service.Release(context.Background(), ReleaseHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldID: legal.ID, Reason: "FAKE_LEGAL_RELEASE_REASON_FOR_TEST_ONLY",
	}); err != nil {
		t.Fatalf("release legal hold: %v", err)
	}
	assertPointHoldProjection(t, db, point.ID, backupasset.HoldReleased, nil)
}

func TestHoldListReturnsActiveHoldsWithoutEncryptedReasons(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	ids := []string{testOpaqueID(211), testOpaqueID(212)}
	service, err := NewHoldService(HoldServiceDependencies{
		DB:  db,
		Now: func() time.Time { return clock },
		NewID: func() (string, error) {
			id := ids[0]
			ids = ids[1:]
			return id, nil
		},
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	repositoryID := testOpaqueID(210)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(testOpaqueID(213), repositoryID, nil, clock.Add(-24*time.Hour), 1)
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed held point: %v", err)
	}
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	operator := backupasset.AuditActor{UserID: 2, Username: "operator", Role: "operator"}
	createReason := "FAKE_LIST_HOLD_REASON_FOR_TEST_ONLY"
	created, err := service.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldLegal, Reason: createReason,
	})
	if err != nil {
		t.Fatalf("create hold: %v", err)
	}
	if _, err := service.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: "FAKE_OPERATIONAL_LIST_REASON_FOR_TEST_ONLY", ExpiresAt: func() *time.Time { value := clock.Add(time.Hour); return &value }(),
	}); err != nil {
		t.Fatalf("create operational hold: %v", err)
	}
	if _, err := service.Release(context.Background(), ReleaseHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldID: created.ID, Reason: "FAKE_RELEASE_LIST_REASON_FOR_TEST_ONLY",
	}); err != nil {
		t.Fatalf("release legal hold: %v", err)
	}

	if _, err := service.List(context.Background(), operator, point.ID); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator list error=%v, want ErrForbidden", err)
	}
	if _, err := service.List(context.Background(), admin, testOpaqueID(299)); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("missing point list error=%v, want ErrNotFound", err)
	}
	holds, err := service.List(context.Background(), admin, point.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(holds) != 1 || holds[0].ID != testOpaqueID(212) || holds[0].State != backupasset.HoldActive ||
		holds[0].HoldType != backupasset.RecoveryPointHoldOperational {
		t.Fatalf("listed holds=%+v, want one active operational hold", holds)
	}
	assertHoldSafeJSON(t, holds[0], createReason)
	assertHoldSafeJSON(t, holds[0], "FAKE_OPERATIONAL_LIST_REASON_FOR_TEST_ONLY")
}

func TestHoldCreateSamplesClockOnceAcrossExpiryBoundary(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := newRetentionTestDB(t)
	validatedAt := time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)
	clockSamples := []time.Time{validatedAt, validatedAt.Add(2 * time.Second)}
	clockCalls := 0
	service, err := NewHoldService(HoldServiceDependencies{
		DB: db,
		Now: func() time.Time {
			index := clockCalls
			clockCalls++
			if index >= len(clockSamples) {
				return clockSamples[len(clockSamples)-1]
			}
			return clockSamples[index]
		},
		NewID: func() (string, error) { return testOpaqueID(220), nil },
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	repositoryID := testOpaqueID(221)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(testOpaqueID(222), repositoryID, nil, validatedAt.Add(-time.Hour), 1)
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed clock-sampling hold point: %v", err)
	}
	expiresAt := validatedAt.Add(time.Second)
	hold, err := service.Create(context.Background(), CreateHoldRequest{
		Actor:           backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		RecoveryPointID: point.ID,
		HoldType:        backupasset.RecoveryPointHoldOperational,
		Reason:          "FAKE_CLOCK_BOUNDARY_HOLD_REASON_FOR_TEST_ONLY",
		ExpiresAt:       &expiresAt,
	})
	if err != nil {
		t.Fatalf("create boundary operational hold: %v", err)
	}
	if clockCalls != 1 || !hold.CreatedAt.Equal(validatedAt) || !hold.UpdatedAt.Equal(validatedAt) ||
		hold.ExpiresAt == nil || !hold.ExpiresAt.After(hold.CreatedAt) {
		t.Fatalf("hold clock samples=%d record=%+v; want one sample at %s before expiry", clockCalls, hold, validatedAt)
	}
}

func TestHoldCreateRejectsLateLifecycleAdmissionBeforeWrite(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	fixture := newClaimedExpiryFixture(t, 790)
	attempt, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseRevoking {
		t.Fatalf("advance lifecycle to revoking attempt=%+v error=%v", attempt, err)
	}
	if _, err := fixture.coordinator.block(context.Background(), attempt.ID, backupasset.LifecycleBlockedOwnerCleanupUnproven); err != nil {
		t.Fatalf("block lifecycle before late hold: %v", err)
	}

	holds, err := NewHoldService(HoldServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.clock },
		NewID: func() (string, error) { return testOpaqueID(794), nil },
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	holds.SetLifecycleHoldAdmission(fixture.coordinator)

	_, err = holds.Create(context.Background(), CreateHoldRequest{
		Actor:           backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		RecoveryPointID: fixture.pointID,
		HoldType:        backupasset.RecoveryPointHoldLegal,
		Reason:          "FAKE_LATE_LIFECYCLE_HOLD_REASON_FOR_TEST_ONLY",
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("late lifecycle hold error=%v, want ErrConflict", err)
	}
	var holdCount int64
	if err := fixture.db.Model(&model.RecoveryPointHold{}).Where("recovery_point_id = ?", fixture.pointID).Count(&holdCount).Error; err != nil {
		t.Fatalf("count late lifecycle holds: %v", err)
	}
	if holdCount != 0 {
		t.Fatalf("late lifecycle admission persisted %d holds", holdCount)
	}
}

func TestHoldOperationalExpiryIsBoundedAdminOnlyAndNeverReleasesLegal(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	idValue := uint64(300)
	service, err := NewHoldService(HoldServiceDependencies{
		DB:  db,
		Now: func() time.Time { return clock },
		NewID: func() (string, error) {
			idValue++
			return testOpaqueID(idValue), nil
		},
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	repositoryID := testOpaqueID(300)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	operator := backupasset.AuditActor{UserID: 2, Username: "operator", Role: "operator"}
	type holdFixture struct {
		point  model.RecoveryPoint
		type_  backupasset.RecoveryPointHoldType
		expiry *time.Time
	}
	firstExpiry := clock.Add(time.Hour)
	secondExpiry := clock.Add(2 * time.Hour)
	futureExpiry := clock.Add(10 * time.Hour)
	fixtures := []holdFixture{
		{newSelectionPoint(testOpaqueID(310), repositoryID, nil, clock.Add(-time.Hour), 1), backupasset.RecoveryPointHoldOperational, &firstExpiry},
		{newSelectionPoint(testOpaqueID(311), repositoryID, nil, clock.Add(-time.Hour), 1), backupasset.RecoveryPointHoldOperational, &secondExpiry},
		{newSelectionPoint(testOpaqueID(312), repositoryID, nil, clock.Add(-time.Hour), 1), backupasset.RecoveryPointHoldOperational, &futureExpiry},
		{newSelectionPoint(testOpaqueID(313), repositoryID, nil, clock.Add(-time.Hour), 1), backupasset.RecoveryPointHoldLegal, nil},
	}
	for index := range fixtures {
		if err := db.Create(&fixtures[index].point).Error; err != nil {
			t.Fatalf("seed expiry point %d: %v", index, err)
		}
		if _, err := service.Create(context.Background(), CreateHoldRequest{
			Actor: admin, RecoveryPointID: fixtures[index].point.ID, HoldType: fixtures[index].type_,
			Reason: "FAKE_EXPIRY_REASON_FOR_TEST_ONLY", ExpiresAt: fixtures[index].expiry,
		}); err != nil {
			t.Fatalf("create expiry hold %d: %v", index, err)
		}
	}
	clock = clock.Add(3 * time.Hour)

	if _, err := service.ExpireOperational(context.Background(), operator, 1); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator expiry error=%v, want ErrForbidden", err)
	}
	firstBatch, err := service.ExpireOperational(context.Background(), admin, 1)
	if err != nil {
		t.Fatalf("expire first bounded batch: %v", err)
	}
	if len(firstBatch) != 1 || firstBatch[0].RecoveryPointID != fixtures[0].point.ID || firstBatch[0].State != backupasset.HoldReleased {
		t.Fatalf("first expiry batch mismatch: %+v", firstBatch)
	}
	assertHoldSafeJSON(t, firstBatch[0], "operational hold expired")
	assertPointHoldProjection(t, db, fixtures[0].point.ID, backupasset.HoldReleased, nil)
	assertPointHoldProjection(t, db, fixtures[1].point.ID, backupasset.HoldActive, &secondExpiry)

	secondBatch, err := service.ExpireOperational(context.Background(), admin, 10)
	if err != nil {
		t.Fatalf("expire second bounded batch: %v", err)
	}
	if len(secondBatch) != 1 || secondBatch[0].RecoveryPointID != fixtures[1].point.ID {
		t.Fatalf("second expiry batch mismatch: %+v", secondBatch)
	}
	assertPointHoldProjection(t, db, fixtures[2].point.ID, backupasset.HoldActive, &futureExpiry)
	assertPointHoldProjection(t, db, fixtures[3].point.ID, backupasset.HoldActive, nil)
	var legalState string
	if err := db.Raw("SELECT state FROM recovery_point_holds WHERE recovery_point_id = ?", fixtures[3].point.ID).Scan(&legalState).Error; err != nil {
		t.Fatalf("load legal hold state: %v", err)
	}
	if legalState != string(backupasset.HoldActive) {
		t.Fatalf("legal hold auto-released: %q", legalState)
	}
}

func TestHoldOperationalExpiryWritesHoldReleaseAudit(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	auditor := &recordingMutationAuditor{}
	service, err := NewHoldService(HoldServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(521), nil },
		Audit: auditor,
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	repositoryID := testOpaqueID(520)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(testOpaqueID(522), repositoryID, nil, clock.Add(-time.Hour), 1)
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed expiry point: %v", err)
	}
	expiresAt := clock.Add(time.Hour)
	if _, err := service.Create(context.Background(), CreateHoldRequest{
		Actor:           backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: "FAKE_EXPIRY_AUDIT_REASON_FOR_TEST_ONLY", ExpiresAt: &expiresAt,
	}); err != nil {
		t.Fatalf("create operational hold: %v", err)
	}
	createWrites := auditor.writes
	clock = clock.Add(2 * time.Hour)
	released, err := service.ExpireOperationalMaintenance(context.Background(), 10)
	if err != nil {
		t.Fatalf("ExpireOperationalMaintenance: %v", err)
	}
	if len(released) != 1 || released[0].State != backupasset.HoldReleased {
		t.Fatalf("released=%+v, want one released hold", released)
	}
	if auditor.writes != createWrites+1 || auditor.last.Action != backupasset.AuditActionHoldRelease {
		t.Fatalf("expiry audits writes=%d action=%q, want hold_release after create", auditor.writes, auditor.last.Action)
	}
	if auditor.last.RecoveryPointID != point.ID || auditor.last.Actor.Role != "system" || auditor.last.Actor.Username != "system" {
		t.Fatalf("expiry audit=%+v, want system actor and opaque point", auditor.last)
	}
	if _, ok := auditor.last.Fields[backupasset.AuditFieldCorrelationID]; ok {
		t.Fatalf("expiry audit leaked correlation: %+v", auditor.last.Fields)
	}
	body, _ := json.Marshal(auditor.last.Fields)
	if strings.Contains(string(body), "operational hold expired") || strings.Contains(string(body), "FAKE_EXPIRY") {
		t.Fatalf("expiry audit contained raw reason: %s", body)
	}
}

func TestHoldOperationalExpiryAuditFailureLeavesHoldActive(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)
	creator, err := NewHoldService(HoldServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(531), nil },
	})
	if err != nil {
		t.Fatalf("NewHoldService create: %v", err)
	}
	expirer, err := NewHoldService(HoldServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(533), nil },
		Audit: failingMutationAuditor{},
	})
	if err != nil {
		t.Fatalf("NewHoldService expire: %v", err)
	}
	repositoryID := testOpaqueID(530)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(testOpaqueID(532), repositoryID, nil, clock.Add(-time.Hour), 1)
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed expiry point: %v", err)
	}
	expiresAt := clock.Add(time.Hour)
	if _, err := creator.Create(context.Background(), CreateHoldRequest{
		Actor:           backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
		RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: "FAKE_EXPIRY_FAIL_REASON_FOR_TEST_ONLY", ExpiresAt: &expiresAt,
	}); err != nil {
		t.Fatalf("create operational hold: %v", err)
	}
	clock = clock.Add(2 * time.Hour)
	if _, err := expirer.ExpireOperationalMaintenance(context.Background(), 10); err == nil {
		t.Fatal("expiry succeeded despite audit failure")
	}
	var state string
	if err := db.Raw("SELECT state FROM recovery_point_holds WHERE recovery_point_id = ?", point.ID).Scan(&state).Error; err != nil {
		t.Fatalf("load hold state: %v", err)
	}
	if state != string(backupasset.HoldActive) {
		t.Fatalf("hold state=%q, want active after failed expiry audit", state)
	}
}

func TestHoldProjectionAdvancesPointRevisionIndependently(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	idValue := uint64(500)
	service, err := NewHoldService(HoldServiceDependencies{
		DB: db, Now: func() time.Time { return clock }, NewID: func() (string, error) {
			idValue++
			return testOpaqueID(idValue), nil
		},
	})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	repositoryID := testOpaqueID(500)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	releasePoint := newSelectionPoint(testOpaqueID(510), repositoryID, nil, clock.Add(-time.Hour), 20)
	releasePoint.PointRevision = 10
	expiryPoint := newSelectionPoint(testOpaqueID(511), repositoryID, nil, clock.Add(-time.Hour), 40)
	expiryPoint.PointRevision = 30
	if err := db.Create(&[]model.RecoveryPoint{releasePoint, expiryPoint}).Error; err != nil {
		t.Fatalf("seed revision projection points: %v", err)
	}
	assertRevisions := func(pointID string, wantPoint int64, wantCapability int) {
		t.Helper()
		var point model.RecoveryPoint
		if err := db.Select("point_revision", "capability_revision").First(&point, "id = ?", pointID).Error; err != nil {
			t.Fatalf("load hold projection revisions: %v", err)
		}
		if point.PointRevision != wantPoint || point.CapabilityRevision != wantCapability {
			t.Fatalf("hold projection revisions=%d/%d, want %d/%d", point.PointRevision, point.CapabilityRevision, wantPoint, wantCapability)
		}
	}

	releaseExpiry := clock.Add(2 * time.Hour)
	hold, err := service.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: releasePoint.ID, HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: "FAKE_REVISION_HOLD_REASON_FOR_TEST_ONLY", ExpiresAt: &releaseExpiry,
	})
	if err != nil {
		t.Fatalf("create revision hold: %v", err)
	}
	assertRevisions(releasePoint.ID, 11, 20)
	clock = clock.Add(time.Minute)
	if _, err := service.Release(context.Background(), ReleaseHoldRequest{
		Actor: admin, RecoveryPointID: releasePoint.ID, HoldID: hold.ID,
		Reason: "FAKE_REVISION_RELEASE_REASON_FOR_TEST_ONLY",
	}); err != nil {
		t.Fatalf("release revision hold: %v", err)
	}
	assertRevisions(releasePoint.ID, 12, 20)

	expiresAt := clock.Add(time.Minute)
	if _, err := service.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: expiryPoint.ID, HoldType: backupasset.RecoveryPointHoldOperational,
		Reason: "FAKE_REVISION_EXPIRY_REASON_FOR_TEST_ONLY", ExpiresAt: &expiresAt,
	}); err != nil {
		t.Fatalf("create expiring revision hold: %v", err)
	}
	assertRevisions(expiryPoint.ID, 31, 40)
	clock = clock.Add(2 * time.Minute)
	if expired, err := service.ExpireOperational(context.Background(), admin, 10); err != nil {
		t.Fatalf("expire revision hold: %v", err)
	} else if len(expired) != 1 || expired[0].RecoveryPointID != expiryPoint.ID {
		t.Fatalf("expired revision holds=%+v, want point %s", expired, expiryPoint.ID)
	}
	assertRevisions(expiryPoint.ID, 32, 40)
}

func assertHoldRequestSafeJSON(t *testing.T, request any, forbidden string) {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal hold request: %v", err)
	}
	if strings.Contains(string(payload), forbidden) || strings.Contains(strings.ToLower(string(payload)), "reason") {
		t.Fatalf("hold request JSON contains plaintext reason: %s", payload)
	}
	for _, rendered := range []string{fmt.Sprint(request), fmt.Sprintf("%+v", request), fmt.Sprintf("%#v", request)} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("hold request formatting contains plaintext reason: %s", rendered)
		}
	}
}

func assertHoldSafeJSON(t *testing.T, hold HoldRecord, forbidden string) {
	t.Helper()
	payload, err := json.Marshal(hold)
	if err != nil {
		t.Fatalf("marshal hold record: %v", err)
	}
	text := string(payload)
	for _, value := range []string{forbidden, "reason", "encrypted_reason", "release_reason"} {
		if strings.Contains(text, value) {
			t.Fatalf("hold JSON contains forbidden value/key %q: %s", value, text)
		}
	}
}

func assertEncryptedHoldColumn(t *testing.T, db interface {
	Raw(string, ...any) *gorm.DB
}, holdID, column, plaintext string) {
	t.Helper()
	var stored string
	if err := db.Raw("SELECT "+column+" FROM recovery_point_holds WHERE id = ?", holdID).Scan(&stored).Error; err != nil {
		t.Fatalf("read raw hold column %s: %v", column, err)
	}
	if !strings.HasPrefix(stored, "enc:v2:") || strings.Contains(stored, plaintext) {
		t.Fatalf("hold column %s was not encrypted at rest: %q", column, stored)
	}
}

func assertPointHoldProjection(t *testing.T, db *gorm.DB, pointID string, state backupasset.HoldState, until *time.Time) {
	t.Helper()
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load point hold projection: %v", err)
	}
	if point.HoldState != string(state) {
		t.Fatalf("point hold state=%q, want %q", point.HoldState, state)
	}
	if until == nil {
		if point.HoldUntil != nil {
			t.Fatalf("point hold until=%v, want nil", point.HoldUntil)
		}
	} else if point.HoldUntil == nil || !point.HoldUntil.Equal(*until) {
		t.Fatalf("point hold until=%v, want %v", point.HoldUntil, until)
	}
}
