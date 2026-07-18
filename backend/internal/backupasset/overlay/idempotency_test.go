package overlay

import (
	"context"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestIdempotencyReceiptCleanupStoresNoPlaintextRequestOrResponse(t *testing.T) {
	service, harness := newOverlayTestHarness(t)
	pointID := strings.Repeat("b", 32)
	harness.points[pointID] = true
	query := savedQuery(pointID, "top secret query")
	created, err := service.CreateSavedSearch(context.Background(), Actor{UserID: 601, Role: "operator"}, CreateSavedSearchRequest{
		Query: query, IdempotencyKey: "idempotency-cleanup-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw model.BackupAssetOverlayIdempotency
	if err := harness.db.Session(&gorm.Session{SkipHooks: true}).Where("owner_user_id = ?", 601).Take(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(raw.EncryptedRequestFingerprint), "top secret") ||
		strings.Contains(raw.EncryptedRequestFingerprint, created.ID) || !strings.HasPrefix(raw.EncryptedRequestFingerprint, "enc:") {
		t.Fatalf("receipt stores plaintext request/response: %+v", raw)
	}
	harness.clock.Advance(25 * time.Hour)
	audit := &overlayAuditSpy{}
	service.audit = audit
	count, err := service.CleanupIdempotency(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("CleanupIdempotency count=%d err=%v", count, err)
	}
	if len(audit.inputs) != 1 || audit.inputs[0].Action != backupasset.AuditActionOverlayCleanup ||
		audit.inputs[0].Outcome != backupasset.AuditOutcomeSuccess || audit.inputs[0].ItemCount != 1 {
		t.Fatalf("idempotency cleanup audit=%+v", audit.inputs)
	}
}
