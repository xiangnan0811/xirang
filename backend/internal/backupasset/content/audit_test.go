package content

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestContentAuditAcceptsUniqueConflictOnlyForExactPersistedProjection(t *testing.T) {
	db := newContentAuditTestDB(t)
	input := testContentAuditInput(backupasset.AuditActionPreviewTicket, backupasset.AuditOutcomeSuccess)
	existing := model.BackupAssetAuditEvent{
		SegmentNo: 1, SegmentSequence: 1, ActorUserID: input.Actor.UserID, ActorUsername: input.Actor.Username,
		ActorRole: input.Actor.Role, Action: string(input.Action), Outcome: string(input.Outcome),
		RepositoryID: input.RepositoryID, RecoveryPointID: input.RecoveryPointID, EntryID: input.EntryID,
		ItemCount: input.ItemCount, ByteCount: input.ByteCount, RangeCount: input.Range.Count, RangeBytes: input.Range.Bytes,
		StepUpAction: input.StepUpAction, StepUpProofID: input.StepUpProofID, GrantID: input.GrantID,
		FailureCode: input.FailureCode, FieldsJSON: `{"profile":"raster_v1","renderer":"safe_raster"}`,
		PrevHash: strings.Repeat("a", 64), EntryHash: strings.Repeat("b", 64), CreatedAt: time.Now().UTC(),
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	writer := &contentFoundationAuditWriterFake{err: errors.New("unique violation")}
	service, err := NewContentAuditService(ContentAuditDependencies{DB: db, Writer: writer, Now: time.Now, BacklogMax: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Write(context.Background(), input); err != nil {
		t.Fatalf("exact idempotent projection rejected: %v", err)
	}

	if err := db.Model(&model.BackupAssetAuditEvent{}).Where("id = ?", existing.ID).
		Update("outcome", string(backupasset.AuditOutcomeFailure)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.Write(context.Background(), input); !errors.Is(err, ErrContentAuditMismatch) {
		t.Fatalf("mismatched unique collision error=%v", err)
	}
}

func TestContentAuditAggregatesRequestLedgerAndFlushesOneInternalGrantSummary(t *testing.T) {
	harness := newBudgetTestHarness(t, func(grant *model.BackupAssetDeliveryGrant) {
		grant.MaxBytesPerRequest = 1_000
		grant.MaxCumulativeBytes = 1_000
		grant.MaxRequests = 30
	}, nil)
	const requests = 20
	for index := 0; index < requests; index++ {
		requestID := fmt.Sprintf("%032x", 500+index)
		if index%5 == 0 {
			if err := harness.service.RecordBlocked(context.Background(), BlockedRequest{
				RequestID: requestID, GrantID: harness.grant.ID, Method: "GET", Status: 416,
				FailureCode: RequestFailureInvalidRange, RangeRequested: true,
			}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		start, end := int64(index*10), int64(index*10+10)
		reservation, err := harness.service.Reserve(context.Background(), ReservationIntent{
			RequestID: requestID, GrantID: harness.grant.ID, Method: "GET",
			Range: HTTPRange{
				Kind: HTTPRangeNormal, Start: &start, EndExclusive: &end,
				Offset: start, Length: 10,
			},
			ReservedBytes: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := harness.service.Finalize(context.Background(), FinalizeIntent{
			RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
			State: RequestSucceeded, HTTPStatus: 206, ProviderBytes: 10, ResponseBytes: 10, EvidenceKnown: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	now := harness.clock.Now()
	if err := harness.db.Model(&model.BackupAssetDeliveryGrant{}).Where("id = ?", harness.grant.ID).
		Updates(map[string]any{
			"state": DeliveryRevoked, "revocation_reason": "shutdown", "revoked_at": now,
		}).Error; err != nil {
		t.Fatal(err)
	}
	writer := &contentFoundationAuditWriterFake{}
	service, err := NewContentAuditService(ContentAuditDependencies{
		DB: harness.db, Writer: writer, Now: harness.clock.Now, BacklogMax: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	var aggregated model.BackupAssetDeliveryGrant
	if err := harness.db.First(&aggregated, "id = ?", harness.grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if aggregated.AuditState != "pending" || aggregated.AuditRequestCount != requests ||
		aggregated.AuditRangeCount != requests || aggregated.AuditRangeBytes != 16*10 ||
		aggregated.AuditSuccessCount != 16 || aggregated.AuditBlockedCount != 4 {
		t.Fatalf("aggregate=%+v", aggregated)
	}
	if err := service.FlushGrant(context.Background(), harness.grant.ID); err != nil {
		t.Fatal(err)
	}
	if len(writer.inputs) != 1 {
		t.Fatalf("audit writes=%d", len(writer.inputs))
	}
	written := writer.inputs[0]
	if written.GrantID != harness.grant.ID || written.Action != backupasset.AuditActionPreviewRead ||
		written.Range.Count != requests || written.Range.Bytes != 16*10 ||
		written.ByteCount != 16*10 || written.Outcome != backupasset.AuditOutcomeBlocked {
		t.Fatalf("audit input=%+v", written)
	}
	if strings.Contains(written.Fields[backupasset.AuditFieldSource].(string), "delivery") {
		t.Fatalf("public delivery fact entered audit: %+v", written.Fields)
	}
	if err := harness.db.First(&aggregated, "id = ?", harness.grant.ID).Error; err != nil || aggregated.AuditState != "emitted" {
		t.Fatalf("flushed grant=%+v err=%v", aggregated, err)
	}
}

func TestContentAggregateAuditUsesRecoveryResultActionAndJobBinding(t *testing.T) {
	jobID := strings.Repeat("a", 32)
	resultID := strings.Repeat("b", 32)
	grant := model.BackupAssetDeliveryGrant{
		ID: strings.Repeat("c", 32), ResourceKind: string(DeliveryResourceRecoveryResult),
		RecoveryJobID: &jobID, RecoveryResultID: &resultID,
		OwnerUserID: 42, SessionRole: "admin", Action: string(DeliveryDownload),
		Renderer: string(RendererAttachment), Profile: string(ProfileOriginalV1),
		Classification: string(ClassificationUnknown),
	}
	input := aggregateAuditInput(grant, 12)
	if input.Action != backupasset.AuditActionRecoveryResultDownload || input.RecoveryJobID != jobID ||
		input.RecoveryPointID != "" || input.EntryID != "" || input.ByteCount != 12 || input.GrantID != grant.ID {
		t.Fatalf("recovery aggregate audit=%+v", input)
	}
}

func TestContentAuditFailureQueuesBoundedRetryAndBacklogBlocksNewTickets(t *testing.T) {
	db := newContentAuditTestDB(t)
	now := time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)
	for index := 0; index < 2; index++ {
		grant := testBudgetGrant(now)
		grant.ID = strings.Repeat(string(rune('a'+index)), 32)
		grant.DeliveryID = strings.Repeat(string(rune('c'+index)), 32)
		grant.LeaseID = strings.Repeat(string(rune('e'+index)), 32)
		grant.State = string(DeliveryRevoked)
		grant.RevocationReason = "shutdown"
		grant.RevokedAt = &now
		grant.AuditState = "pending"
		grant.RequestCount = 1
		grant.AuditRequestCount = 1
		grant.AuditSuccessCount = 1
		if err := db.Create(&grant).Error; err != nil {
			t.Fatal(err)
		}
		request := model.BackupAssetDeliveryRequest{
			ID: fmt.Sprintf("%032x", 700+index), GrantID: grant.ID, Method: "GET",
			RangeKind: string(HTTPRangeFull), State: string(RequestSucceeded), HTTPStatus: 200,
			StartedAt: now, LastProgressAt: now, FinishedAt: &now,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		if err := db.Create(&request).Error; err != nil {
			t.Fatal(err)
		}
	}
	writer := &contentFoundationAuditWriterFake{err: errors.New("audit unavailable")}
	metrics := newBrokerMetricsFake()
	service, err := NewContentAuditService(ContentAuditDependencies{
		DB: db, Writer: writer, Now: func() time.Time { return now }, BacklogMax: 2, Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BacklogAvailable(context.Background()); !errors.Is(err, ErrContentAuditBacklogFull) {
		t.Fatalf("backlog error=%v", err)
	}
	if err := service.FlushGrant(context.Background(), strings.Repeat("a", 32)); err == nil {
		t.Fatal("flush unexpectedly succeeded")
	}
	var grant model.BackupAssetDeliveryGrant
	if err := db.First(&grant, "id = ?", strings.Repeat("a", 32)).Error; err != nil {
		t.Fatal(err)
	}
	if grant.AuditState != "retry_wait" || grant.AuditAttemptCount != 1 || grant.AuditNextAttemptAt == nil ||
		!grant.AuditNextAttemptAt.After(now) {
		t.Fatalf("retry grant=%+v", grant)
	}
	if backlog, retries := metrics.auditState(); backlog != 2 || retries != 1 {
		t.Fatalf("audit metrics backlog=%d retries=%d", backlog, retries)
	}
}

func TestContentAuditDefersActiveGrantFinalSummary(t *testing.T) {
	db := newContentAuditTestDB(t)
	grant := testBudgetGrant(time.Date(2026, 7, 18, 16, 0, 0, 0, time.UTC))
	grant.AuditState = "pending"
	grant.RequestCount = 1
	grant.AuditRequestCount = 1
	grant.AuditSuccessCount = 1
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	writer := &contentFoundationAuditWriterFake{}
	service, err := NewContentAuditService(ContentAuditDependencies{
		DB: db, Writer: writer, Now: time.Now, BacklogMax: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.FlushGrant(context.Background(), grant.ID); err == nil {
		t.Fatal("active grant final audit unexpectedly flushed")
	}
	if len(writer.inputs) != 0 {
		t.Fatalf("active grant audit writes=%d", len(writer.inputs))
	}
}

func TestContentAuditFullReadReportsTotalBytesOutsideRangeSummary(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	reservation := harness.reserve(t, strings.Repeat("9", 32), 40)
	if _, err := harness.service.Finalize(context.Background(), FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
		State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 30, ResponseBytes: 25, EvidenceKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := harness.clock.Now()
	if err := harness.db.Model(&model.BackupAssetDeliveryGrant{}).Where("id = ?", harness.grant.ID).
		Updates(map[string]any{
			"state": DeliveryRevoked, "revocation_reason": "shutdown", "revoked_at": now,
		}).Error; err != nil {
		t.Fatal(err)
	}
	writer := &contentFoundationAuditWriterFake{}
	service, err := NewContentAuditService(ContentAuditDependencies{
		DB: harness.db, Writer: writer, Now: harness.clock.Now, BacklogMax: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.FlushGrant(context.Background(), harness.grant.ID); err != nil {
		t.Fatal(err)
	}
	if len(writer.inputs) != 1 || writer.inputs[0].ByteCount != 25 ||
		writer.inputs[0].Range.Count != 0 || writer.inputs[0].Range.Bytes != 0 {
		t.Fatalf("full read audit=%+v", writer.inputs)
	}
}

func TestContentAuditReturnsRetryPersistenceFailure(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	reservation := harness.reserve(t, strings.Repeat("9", 32), 10)
	if _, err := harness.service.Finalize(context.Background(), FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
		State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 10, ResponseBytes: 10, EvidenceKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := harness.clock.Now()
	if err := harness.db.Model(&model.BackupAssetDeliveryGrant{}).Where("id = ?", harness.grant.ID).
		Updates(map[string]any{
			"state": DeliveryRevoked, "revocation_reason": "shutdown", "revoked_at": now,
		}).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewContentAuditService(ContentAuditDependencies{
		DB: harness.db, Writer: &contentFoundationAuditWriterFake{err: errors.New("audit unavailable")},
		Now: harness.clock.Now, BacklogMax: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	retryPersistenceErr := errors.New("retry persistence unavailable")
	if err := harness.db.Callback().Update().Before("gorm:update").Register(
		"test:block_content_audit_retry", func(tx *gorm.DB) { _ = tx.AddError(retryPersistenceErr) },
	); err != nil {
		t.Fatal(err)
	}
	if err := service.FlushGrant(context.Background(), harness.grant.ID); !errors.Is(err, retryPersistenceErr) {
		t.Fatalf("retry persistence error=%v", err)
	}
}

func TestContentAuditStaleRetryCannotReopenEmittedSummary(t *testing.T) {
	db := newContentAuditTestDB(t)
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	grant := testBudgetGrant(now)
	grant.State = string(DeliveryRevoked)
	grant.RevocationReason = "shutdown"
	grant.RevokedAt = &now
	grant.AuditState = "pending"
	grant.RequestCount = 1
	grant.AuditRequestCount = 1
	grant.AuditSuccessCount = 1
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	var stale model.BackupAssetDeliveryGrant
	if err := db.First(&stale, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDeliveryGrant{}).Where("id = ?", grant.ID).Updates(map[string]any{
		"audit_state": "emitted", "version": gorm.Expr("version + 1"), "updated_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewContentAuditService(ContentAuditDependencies{
		DB: db, Writer: &contentFoundationAuditWriterFake{}, Now: func() time.Time { return now }, BacklogMax: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.queueAuditRetry(context.Background(), stale); !errors.Is(err, ErrContentAuditUnavailable) {
		t.Fatalf("stale retry error=%v", err)
	}
	var stored model.BackupAssetDeliveryGrant
	if err := db.First(&stored, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AuditState != "emitted" || stored.AuditAttemptCount != 0 || stored.AuditNextAttemptAt != nil {
		t.Fatalf("stale retry reopened emitted audit: %+v", stored)
	}
}

type contentFoundationAuditWriterFake struct {
	mu     sync.Mutex
	inputs []backupasset.AuditEventInput
	err    error
}

func (writer *contentFoundationAuditWriterFake) Write(_ context.Context, input backupasset.AuditEventInput) (model.BackupAssetAuditEvent, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.inputs = append(writer.inputs, input)
	return model.BackupAssetAuditEvent{}, writer.err
}

func newContentAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + contentTestDBName(t) + "?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetAuditEvent{}, &model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func testContentAuditInput(action backupasset.AuditAction, outcome backupasset.AuditOutcome) backupasset.AuditEventInput {
	return backupasset.AuditEventInput{
		Actor:  backupasset.AuditActor{UserID: 42, Username: "operator", Role: "operator"},
		Action: action, Outcome: outcome, RepositoryID: strings.Repeat("1", 32),
		RecoveryPointID: strings.Repeat("2", 32), EntryID: strings.Repeat("3", 64),
		ItemCount: 1, ByteCount: 10, Range: backupasset.NewRangeSummary(1, 10),
		GrantID: strings.Repeat("4", 32),
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldRenderer: "safe_raster", backupasset.AuditFieldProfile: "raster_v1",
		},
	}
}
