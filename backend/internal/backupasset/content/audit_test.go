package content

import (
	"context"
	"errors"
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

func TestContentAuditAggregatesConcurrentRangesAndFlushesOneInternalGrantSummary(t *testing.T) {
	db := newContentAuditTestDB(t)
	grant := testBudgetGrant(time.Date(2026, 7, 18, 14, 0, 0, 0, time.UTC))
	grant.AuditState = "none"
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	writer := &contentFoundationAuditWriterFake{}
	service, err := NewContentAuditService(ContentAuditDependencies{DB: db, Writer: writer, Now: time.Now, BacklogMax: 10})
	if err != nil {
		t.Fatal(err)
	}
	const requests = 20
	start := make(chan struct{})
	errs := make(chan error, requests)
	for index := 0; index < requests; index++ {
		go func(index int) {
			<-start
			outcome := backupasset.AuditOutcomeSuccess
			if index%5 == 0 {
				outcome = backupasset.AuditOutcomeBlocked
			}
			errs <- service.RecordRead(context.Background(), ReadAuditSummary{
				GrantID: grant.ID, Outcome: outcome, Bytes: 10, Range: true,
			})
		}(index)
	}
	close(start)
	for range requests {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var aggregated model.BackupAssetDeliveryGrant
	if err := db.First(&aggregated, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if aggregated.AuditState != "pending" || aggregated.AuditRequestCount != requests ||
		aggregated.AuditRangeCount != requests || aggregated.AuditRangeBytes != requests*10 ||
		aggregated.AuditSuccessCount != 16 || aggregated.AuditBlockedCount != 4 {
		t.Fatalf("aggregate=%+v", aggregated)
	}
	if err := service.FlushGrant(context.Background(), grant.ID); err != nil {
		t.Fatal(err)
	}
	if len(writer.inputs) != 1 {
		t.Fatalf("audit writes=%d", len(writer.inputs))
	}
	written := writer.inputs[0]
	if written.GrantID != grant.ID || written.Action != backupasset.AuditActionPreviewRead ||
		written.Range.Count != requests || written.Range.Bytes != requests*10 ||
		written.ByteCount != requests*10 || written.Outcome != backupasset.AuditOutcomeBlocked {
		t.Fatalf("audit input=%+v", written)
	}
	if strings.Contains(written.Fields[backupasset.AuditFieldSource].(string), "delivery") {
		t.Fatalf("public delivery fact entered audit: %+v", written.Fields)
	}
	if err := db.First(&aggregated, "id = ?", grant.ID).Error; err != nil || aggregated.AuditState != "emitted" {
		t.Fatalf("flushed grant=%+v err=%v", aggregated, err)
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
		grant.AuditState = "pending"
		if err := db.Create(&grant).Error; err != nil {
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
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.BackupAssetAuditEvent{}, &model.BackupAssetDeliveryGrant{}); err != nil {
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
