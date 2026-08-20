package retention

import (
	"context"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
)

func TestDisasterRecoveryControlPlaneRequiresDBAndMatchingKey(t *testing.T) {
	for _, fact := range []string{"policy", "hold", "audit", "overlay", "encrypted_reason", "wrapped_key"} {
		if _, err := backupasset.ClassifyDisasterRecoveryFact(fact); err != nil {
			t.Fatalf("classify %s: %v", fact, err)
		}
	}
	if _, err := backupasset.ClassifyDisasterRecoveryFact("provider_locator"); err == nil {
		t.Fatal("unknown disaster-recovery fact was admitted")
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	fresh := newRetentionTestDB(t)
	emptyPolicyService, err := NewPolicyService(PolicyServiceDependencies{DB: fresh, Now: func() time.Time { return time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := emptyPolicyService.ListActive(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("fresh control plane listed policies=%d", len(listed))
	}

	db := newRetentionTestDB(t)
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	policyIDs := []string{testOpaqueID(401)}
	policyService, err := NewPolicyService(PolicyServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) {
			id := policyIDs[0]
			policyIDs = policyIDs[1:]
			return id, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	holdIDs := []string{testOpaqueID(402)}
	holdService, err := NewHoldService(HoldServiceDependencies{
		DB: db, Now: func() time.Time { return clock },
		NewID: func() (string, error) {
			id := holdIDs[0]
			holdIDs = holdIDs[1:]
			return id, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	repositoryID := testOpaqueID(400)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(testOpaqueID(403), repositoryID, nil, clock.Add(-24*time.Hour), 1)
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	admin := backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"}
	if _, err := policyService.Create(context.Background(), CreatePolicyRequest{
		Actor: admin, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		Rules: PolicyRules{Version: PolicyRulesVersion1, Age: &AgeRule{KeepDays: 30}},
	}); err != nil {
		t.Fatal(err)
	}
	createReason := "FAKE_LEGAL_HOLD_REASON_FOR_DR"
	hold, err := holdService.Create(context.Background(), CreateHoldRequest{
		Actor: admin, RecoveryPointID: point.ID, HoldType: backupasset.RecoveryPointHoldLegal, Reason: createReason,
	})
	if err != nil {
		t.Fatal(err)
	}
	var storedReason string
	if err := db.Raw("SELECT encrypted_reason FROM recovery_point_holds WHERE id = ?", hold.ID).Scan(&storedReason).Error; err != nil {
		t.Fatal(err)
	}
	if storedReason == "" || strings.Contains(storedReason, createReason) {
		t.Fatalf("hold reason was stored in plaintext: %q", storedReason)
	}

	stillListed, err := policyService.ListActive(context.Background(), 20)
	if err != nil || len(stillListed) != 1 {
		t.Fatalf("matching key policies=%d err=%v", len(stillListed), err)
	}
	var readable model.RecoveryPointHold
	if err := db.First(&readable, "id = ?", hold.ID).Error; err != nil {
		t.Fatal(err)
	}
	if readable.EncryptedReason != createReason {
		t.Fatalf("matching key did not decrypt hold reason")
	}

	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_WRONG_DATA_ENCRYPTION_KEY_FOR_DR")
	secure.ResetForTesting()
	var unreadable model.RecoveryPointHold
	if err := db.First(&unreadable, "id = ?", hold.ID).Error; err == nil {
		t.Fatal("wrong encryption key loaded a hold without failing closed")
	}
	if unreadable.EncryptedReason == createReason {
		t.Fatal("wrong encryption key leaked the hold reason")
	}
}
