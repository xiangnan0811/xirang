package processing

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	workerCapabilities "xirang/backend/internal/backupasset/processing/capabilities"
	"xirang/backend/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var archiveMemberTestDBSequence atomic.Uint64

func TestArchiveMemberBehaviorSQLite(t *testing.T) {
	runArchiveMemberBehaviorContract(t, openProcessingBehaviorSQLite)
}

func TestArchiveMemberBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_PROCESSING_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_PROCESSING_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runArchiveMemberBehaviorContract(t, func(t *testing.T) processingBehaviorFixture {
		return openProcessingBehaviorPostgres(t, dsn)
	})
}

func runArchiveMemberBehaviorContract(t *testing.T, open func(*testing.T) processingBehaviorFixture) {
	t.Helper()

	t.Run("ConcurrentCreateLatchAndIdempotency", func(t *testing.T) {
		fixture := open(t)
		now := fixture.clock.Now()
		seedArchiveMemberBehaviorUser(t, fixture, now)

		memberID := strings.Repeat("a", 32)
		binding := archiveMemberIndexFixture(now, memberID)
		started := make(chan struct{}, 2)
		release := make(chan struct{})
		resolveIndex := func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			started <- struct{}{}
			<-release
			return binding, nil
		}
		service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
			DB: fixture.db, Coordinator: &archiveMemberCoordinatorFake{},
			Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
			ResolveIndex: resolveIndex, Now: fixture.clock.Now,
		})
		if err != nil {
			t.Fatal(err)
		}

		type outcome struct {
			result ArchiveMemberCreateResult
			err    error
		}
		outcomes := make(chan outcome, 2)
		request := archiveMemberCreateFixture(memberID)
		for range 2 {
			go func() {
				result, createErr := service.Create(context.Background(), request)
				outcomes <- outcome{result: result, err: createErr}
			}()
		}
		<-started
		<-started
		close(release)
		first, second := <-outcomes, <-outcomes
		if first.err != nil || second.err != nil {
			t.Fatalf("%s concurrent archive-member creates: first=%v second=%v", fixture.engine, first.err, second.err)
		}
		if first.result.RequestID == "" || first.result.RequestID != second.result.RequestID ||
			first.result.Replayed == second.result.Replayed {
			t.Fatalf("%s concurrent archive-member results: first=%+v second=%+v", fixture.engine, first.result, second.result)
		}

		changed := request
		changed.MemberChain = []string{strings.Repeat("b", 32)}
		if _, err := service.Create(context.Background(), changed); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("%s changed archive-member intent error=%v", fixture.engine, err)
		}
		assertArchiveMemberBehaviorRows(t, fixture, 1, 1)
	})

	t.Run("TransactionExpiryLeavesNoPermanentWrites", func(t *testing.T) {
		fixture := open(t)
		beforeExpiry := fixture.clock.Now()
		seedArchiveMemberBehaviorUser(t, fixture, beforeExpiry)
		expiresAt := beforeExpiry.Add(time.Second)
		memberID := strings.Repeat("a", 32)
		index := archiveMemberIndexFixture(beforeExpiry, memberID)
		index.AbsoluteExpiresAt = expiresAt
		var clockCalls atomic.Int32
		service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
			DB: fixture.db, Coordinator: &archiveMemberCoordinatorFake{},
			Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
			ResolveIndex: (&archiveMemberIndexFake{binding: index}).Resolve,
			Now: func() time.Time {
				if clockCalls.Add(1) == 1 {
					return beforeExpiry
				}
				return expiresAt
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID)); !errors.Is(err, ErrArchiveMemberUnavailable) {
			t.Fatalf("%s deadline-crossing create error=%v clock_calls=%d", fixture.engine, err, clockCalls.Load())
		}
		if clockCalls.Load() < 3 {
			t.Fatalf("%s deadline-crossing clock calls=%d", fixture.engine, clockCalls.Load())
		}
		assertArchiveMemberBehaviorRows(t, fixture, 0, 0)
	})

	t.Run("RequestInsertFailureRollsBackFirstLatch", func(t *testing.T) {
		fixture := open(t)
		now := fixture.clock.Now()
		memberID := strings.Repeat("a", 32)
		var latchCreates atomic.Int32
		callbackName := "test:archive-member-behavior-latch-create"
		if err := fixture.db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
			if tx.Error == nil && tx.Statement.Table == (model.BackupAssetExportQuotaBucket{}).TableName() {
				latchCreates.Add(1)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(callbackName) })
		service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
			DB: fixture.db, Coordinator: &archiveMemberCoordinatorFake{},
			Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
			ResolveIndex: (&archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}).Resolve,
			Now:          fixture.clock.Now,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, createErr := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
		if createErr == nil {
			t.Fatalf("%s missing-owner request insert unexpectedly succeeded", fixture.engine)
		}
		errorText := strings.ToLower(createErr.Error())
		if !strings.Contains(errorText, "foreign key") && !strings.Contains(errorText, "sqlstate 23503") {
			t.Fatalf("%s request insert failure=%v", fixture.engine, createErr)
		}
		if latchCreates.Load() != 1 {
			t.Fatalf("%s successful in-transaction latch creates=%d", fixture.engine, latchCreates.Load())
		}
		assertArchiveMemberBehaviorRows(t, fixture, 0, 0)
	})
}

func seedArchiveMemberBehaviorUser(t *testing.T, fixture processingBehaviorFixture, now time.Time) {
	t.Helper()
	if err := fixture.db.Create(&model.User{
		ID: 42, Username: "archive-member-behavior", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "admin", Onboarded: true, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("%s seed archive-member behavior user: %v", fixture.engine, err)
	}
}

func assertArchiveMemberBehaviorRows(
	t *testing.T,
	fixture processingBehaviorFixture,
	wantRequests int64,
	wantLatches int64,
) {
	t.Helper()
	var requestRows int64
	if err := fixture.db.Model(&model.BackupAssetArchiveMemberRequest{}).Count(&requestRows).Error; err != nil {
		t.Fatal(err)
	}
	var latchRows int64
	if err := fixture.db.Model(&model.BackupAssetExportQuotaBucket{}).
		Where("scope = ? AND subject = ?", "global", "global").Count(&latchRows).Error; err != nil {
		t.Fatal(err)
	}
	if requestRows != wantRequests || latchRows != wantLatches {
		t.Fatalf("%s archive-member durable rows: requests=%d want=%d latches=%d want=%d",
			fixture.engine, requestRows, wantRequests, latchRows, wantLatches)
	}
}

func TestArchiveMemberCreateDurablyReplaysBeforeIndexOrProcessing(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := archiveMemberCreateFixture(memberID)

	created, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.AssetRef != request.Ref || created.IndexRevision != request.IndexRevision {
		t.Fatalf("create binding=%+v request=%+v", created, request)
	}
	replayed, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.RequestID == "" || created.RequestID != replayed.RequestID || created.Replayed || !replayed.Replayed ||
		replayed.AssetRef != request.Ref || replayed.IndexRevision != request.IndexRevision {
		t.Fatalf("created=%+v replayed=%+v", created, replayed)
	}
	if index.calls != 1 || coordinator.calls != 0 {
		t.Fatalf("index calls=%d processing calls=%d", index.calls, coordinator.calls)
	}

	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.ResolvedOrdinal != 7 || !lowerHex(persisted.MemberChainDigest, 64) ||
		persisted.IndexRevision != request.IndexRevision || persisted.IndexArtifactID != index.binding.ArtifactID {
		t.Fatalf("persisted binding=%+v", persisted)
	}
	for _, value := range []string{
		persisted.Endpoint, persisted.KeyDigest, persisted.RequestIntentDigest, persisted.RecoveryPointID,
		persisted.EntryID, persisted.CatalogGenerationID, persisted.SourceFingerprint,
		persisted.EntryFingerprint, persisted.IndexArtifactID, persisted.IndexRevision,
		persisted.MemberChainDigest, persisted.State, persisted.ErrorCategory,
	} {
		if strings.Contains(value, memberID) {
			t.Fatalf("raw member identity persisted in %q", value)
		}
	}
	var latch model.BackupAssetExportQuotaBucket
	if err := db.Where("scope = ? AND subject = ?", "global", "global").Take(&latch).Error; err != nil {
		t.Fatalf("durable use latch missing: %v", err)
	}
}

func TestArchiveMemberCreateRotatesExpiredIdempotencyReceiptAndPreservesHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	request := archiveMemberCreateFixture(memberID)
	keyDigest, intentDigest, memberDigest, _, err := archiveMemberRequestDigests(request, 32)
	if err != nil {
		t.Fatal(err)
	}
	oldID := strings.Repeat("e", 32)
	if err := db.Create(&model.BackupAssetArchiveMemberRequest{
		ID: oldID, OwnerUserID: request.Actor.UserID, Endpoint: archiveMemberCreateEndpoint,
		KeyDigest: keyDigest, RequestIntentDigest: intentDigest,
		RecoveryPointID: request.Ref.RecoveryPointID, EntryID: request.Ref.EntryID,
		CatalogGenerationID: strings.Repeat("4", 32), SourceFingerprint: "source-fingerprint-v1",
		EntryFingerprint: "entry-fingerprint-v1", IndexArtifactID: strings.Repeat("5", 32),
		IndexRevision: request.IndexRevision, MemberChainDigest: memberDigest, ResolvedOrdinal: 7,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now.Add(time.Hour),
		IdempotencyExpiresAt: now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), Version: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()}, ResolveIndex: index.Resolve,
		Now: func() time.Time { return now }, IdempotencyTTL: 2 * time.Hour, IdempotencyKeyMaxBytes: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("create after expired archive receipt: %v", err)
	}
	if created.Replayed || created.RequestID == oldID {
		t.Fatalf("expired archive receipt replayed history: %+v", created)
	}
	var rows []model.BackupAssetArchiveMemberRequest
	if err := db.Where("owner_user_id = ? AND endpoint = ?", request.Actor.UserID, archiveMemberCreateEndpoint).
		Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("receipt rows=%d, want preserved history plus fresh receipt", len(rows))
	}
	var historical, fresh *model.BackupAssetArchiveMemberRequest
	for index := range rows {
		if rows[index].ID == oldID {
			historical = &rows[index]
		}
		if rows[index].ID == created.RequestID {
			fresh = &rows[index]
		}
	}
	if historical == nil || fresh == nil || historical.KeyDigest == keyDigest || fresh.KeyDigest != keyDigest ||
		!fresh.IdempotencyExpiresAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("archive receipt rotation historical=%+v fresh=%+v", historical, fresh)
	}
}

func TestArchiveMemberCreateRejectsConfiguredIdempotencyKeyCeiling(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()}, ResolveIndex: index.Resolve,
		Now: func() time.Time { return now }, IdempotencyTTL: time.Hour, IdempotencyKeyMaxBytes: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := archiveMemberCreateFixture(memberID)
	request.IdempotencyKey = strings.Repeat("k", 33)
	if _, err := service.Create(context.Background(), request); !errors.Is(err, assetexport.ErrInvalidIdempotency) {
		t.Fatalf("configured idempotency ceiling error=%v, want ErrInvalidIdempotency", err)
	}
	if index.calls != 0 {
		t.Fatalf("invalid idempotency key reached archive index %d times", index.calls)
	}
}

func TestArchiveMemberCreateReplayAfterReconcileKeepsCreateAcceptanceState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service := newArchiveMemberReconcileService(t, db, now, index, coordinator)
	request := archiveMemberCreateFixture(memberID)

	created, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberRunning) {
		t.Fatalf("reconcile did not advance durable request: %+v", persisted)
	}

	replayed, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("replay after reconcile: %v", err)
	}
	if !replayed.Replayed || replayed.RequestID != created.RequestID || replayed.State != string(ArchiveMemberQueued) {
		t.Fatalf("create acceptance replay created=%+v replayed=%+v", created, replayed)
	}

	changed := request
	changed.MemberChain = []string{strings.Repeat("b", 32)}
	if _, err := service.Create(context.Background(), changed); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("changed intent after reconcile error=%v", err)
	}
	if index.calls != 1 || coordinator.calls != 1 {
		t.Fatalf("replay or conflict reached index/processing: index=%d coordinator=%d", index.calls, coordinator.calls)
	}
}

func TestArchiveMemberPollAfterCreateReplayReportsCurrentRunningState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service := newArchiveMemberReconcileService(t, db, now, index, coordinator)
	request := archiveMemberCreateFixture(memberID)

	created, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.RequestID != created.RequestID || replayed.State != string(ArchiveMemberQueued) {
		t.Fatalf("create acceptance replay created=%+v replayed=%+v", created, replayed)
	}

	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
	job.State = string(ProcessingQueued)
	job.ErrorCode = ""
	job.FinishedAt = nil
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	status, err := service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: request.Actor, Ref: request.Ref, RequestID: replayed.RequestID, IndexRevision: request.IndexRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.RequestID != created.RequestID || status.AssetRef != request.Ref ||
		status.IndexRevision != request.IndexRevision || status.State != ArchiveMemberRunning || status.Terminal {
		t.Fatalf("current lifecycle status=%+v", status)
	}
}

func TestArchiveMemberListIndexReturnsOnlySafePublicView(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	memberID := strings.Repeat("a", 32)
	binding := archiveMemberIndexFixture(now, memberID)
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: archiveMemberTestDB(t, now), Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: (&archiveMemberIndexFake{binding: binding}).Resolve,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.ListIndex(context.Background(), ArchiveMemberIndexLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor,
		Ref:   archiveMemberAssetFixture().Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.SchemaVersion != 1 || view.IndexRevision != binding.Revision ||
		!view.ExpiresAt.Equal(binding.AbsoluteExpiresAt) || len(view.Entries) != 1 {
		t.Fatalf("index view=%+v", view)
	}
	entry := view.Entries[0]
	if entry.ID != memberID || entry.ParentID != binding.Members[0].ParentID ||
		entry.DisplayName != binding.Members[0].DisplayName || entry.Type != "file" ||
		entry.Size != binding.Members[0].Size || entry.MediaType != binding.Members[0].MediaType {
		t.Fatalf("index entry=%+v", entry)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"warning":"none"`) {
		t.Fatalf("public archive index must expose the closed none warning: %s", encoded)
	}
	for _, forbidden := range []string{binding.ArtifactID, binding.PipelineFingerprint, binding.SecurityPolicyRevision, `"ordinal"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public archive index leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestArchiveMemberListIndexRejectsUnknownEntryWarning(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	memberID := strings.Repeat("a", 32)
	binding := archiveMemberIndexFixture(now, memberID)
	binding.Members[0].Warning = "worker_diagnostic"
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: archiveMemberTestDB(t, now), Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: (&archiveMemberIndexFake{binding: binding}).Resolve,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ListIndex(context.Background(), ArchiveMemberIndexLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor,
		Ref:   archiveMemberAssetFixture().Ref,
	})
	if !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("unknown entry warning must fail closed, got %v", err)
	}
}

func TestValidArchiveMemberIndexAcceptsExactlyOneHundredThousandEntries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	index := archiveMemberIndexFixture(now, strings.Repeat("a", 32))
	index.Members = make([]ArchiveMemberIndexEntry, 100_000)
	for ordinal := range index.Members {
		index.Members[ordinal] = ArchiveMemberIndexEntry{
			OpaqueID: fmt.Sprintf("%032x", ordinal+1), Ordinal: ordinal,
			DisplayName: fmt.Sprintf("member-%d", ordinal), Size: 0, MediaType: "application/octet-stream",
		}
	}
	if !validArchiveMemberIndex(index, index.Revision, now) {
		t.Fatal("100,000 valid archive index entries must be accepted")
	}
	index.Members = append(index.Members, ArchiveMemberIndexEntry{
		OpaqueID: fmt.Sprintf("%032x", len(index.Members)+1), Ordinal: len(index.Members),
		DisplayName: "too-many", Size: 0, MediaType: "application/octet-stream",
	})
	if validArchiveMemberIndex(index, index.Revision, now) {
		t.Fatal("more than 100,000 archive index entries must be rejected")
	}
}

func TestArchiveMemberAuthorizeReadyDeliveryRequiresExactOwnerAndBinding(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("c", 32)
	row := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, RecoveryPointID: asset.Ref.RecoveryPointID, EntryID: asset.Ref.EntryID,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		EntryFingerprint: asset.EntryFingerprint, State: string(ArchiveMemberReady),
		AbsoluteExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	authorize := &archiveMemberActionAuthorizerFake{asset: asset}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: authorize,
		ResolveIndex: (&archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, strings.Repeat("a", 32))}).Resolve,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup := ArchiveMemberLookup{
		Actor: content.DeliveryActor{UserID: 42, Role: "admin"}, Ref: asset.Ref, RequestID: requestID,
	}
	got, err := service.AuthorizeReadyDelivery(context.Background(), lookup)
	if err != nil || !sameArchiveMemberAsset(got, asset) {
		t.Fatalf("authorized asset=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(authorize.actions, []content.DeliveryAction{content.DeliveryDownload}) {
		t.Fatalf("authorization actions=%v", authorize.actions)
	}
	lookup.Actor.UserID++
	if _, err := service.AuthorizeReadyDelivery(context.Background(), lookup); !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("foreign owner error=%v", err)
	}
}

func TestArchiveMemberCreateConflictsBeforeIndexForChangedIntent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := archiveMemberCreateFixture(memberID)
	if _, err := service.Create(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.MemberChain = []string{strings.Repeat("b", 32)}
	if _, err := service.Create(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("changed member error=%v", err)
	}
	if index.calls != 1 {
		t.Fatalf("conflict reached archive index calls=%d", index.calls)
	}
}

func TestArchiveMemberCreateIdempotencyReplayIsOwnerScoped(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := archiveMemberCreateFixture(memberID)
	first, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	otherOwner := request
	otherOwner.Actor.UserID++
	otherOwner.Actor.Username = "other-admin"
	second, err := service.Create(context.Background(), otherOwner)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || second.Replayed || first.RequestID == second.RequestID || first.RequestID == "" || second.RequestID == "" {
		t.Fatalf("owner-scoped creates first=%+v second=%+v", first, second)
	}
	if index.calls != 2 {
		t.Fatalf("other owner was treated as replay: index calls=%d", index.calls)
	}
	var rows int64
	if err := db.Model(&model.BackupAssetArchiveMemberRequest{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("owner-scoped durable requests=%d", rows)
	}
}

func TestArchiveMemberCreateConcurrentIdempotencyHasOneDurableWinner(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	if err := db.Exec(`CREATE UNIQUE INDEX archive_member_test_owner_key
		ON backup_asset_archive_member_requests(owner_user_id, endpoint, key_digest)`).Error; err != nil {
		t.Fatal(err)
	}
	memberID := strings.Repeat("a", 32)
	binding := archiveMemberIndexFixture(now, memberID)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	resolveIndex := func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
		started <- struct{}{}
		<-release
		return binding, nil
	}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: resolveIndex, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := archiveMemberCreateFixture(memberID)
	type outcome struct {
		result ArchiveMemberCreateResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, createErr := service.Create(context.Background(), request)
			outcomes <- outcome{result: result, err: createErr}
		}()
	}
	<-started
	<-started
	close(release)
	first, second := <-outcomes, <-outcomes
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent creates errors first=%v second=%v", first.err, second.err)
	}
	if first.result.RequestID == "" || first.result.RequestID != second.result.RequestID ||
		first.result.Replayed == second.result.Replayed {
		t.Fatalf("concurrent results first=%+v second=%+v", first.result, second.result)
	}
	var rows int64
	if err := db.Model(&model.BackupAssetArchiveMemberRequest{}).
		Where("owner_user_id = ? AND endpoint = ?", request.Actor.UserID, archiveMemberCreateEndpoint).
		Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("durable member request rows=%d", rows)
	}
}

func TestArchiveMemberCreateRetriesTransientReplayLock(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := archiveMemberCreateFixture(memberID)
	created, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	queryLocks := 0
	callbackName := "test:archive-member-transient-replay-lock"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "backup_asset_archive_member_requests" && queryLocks == 0 {
			queryLocks++
			_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrLocked})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	replayed, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("replay after transient lock: %v", err)
	}
	if queryLocks != 1 || !replayed.Replayed || replayed.RequestID != created.RequestID {
		t.Fatalf("transient replay result locks=%d created=%+v replayed=%+v", queryLocks, created, replayed)
	}
}

func TestArchiveMemberCreateRetriesTransientUseLatchLock(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	latchLocks := 0
	callbackName := "test:archive-member-transient-use-latch-lock"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.BackupAssetExportQuotaBucket{}).TableName() && latchLocks == 0 {
			latchLocks++
			_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrLocked})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatalf("create after transient use latch lock: %v", err)
	}
	if latchLocks != 1 || created.RequestID == "" || created.Replayed {
		t.Fatalf("transient use latch result locks=%d created=%+v", latchLocks, created)
	}
}

func TestArchiveMemberCreateWaitsForHeldLockAcrossMultipleAttempts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: (&archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}).Resolve,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	var held atomic.Bool
	held.Store(true)
	var attempts atomic.Int32
	callbackName := "test:archive-member-held-use-latch-lock"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.BackupAssetExportQuotaBucket{}).TableName() && held.Load() {
			attempts.Add(1)
			_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrLocked})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	release := time.AfterFunc(25*time.Millisecond, func() { held.Store(false) })
	t.Cleanup(func() { release.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	created, err := service.Create(ctx, archiveMemberCreateFixture(memberID))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("create after held lock: %v (attempts=%d elapsed=%s)", err, attempts.Load(), elapsed)
	}
	if attempts.Load() < 3 || elapsed < 20*time.Millisecond || created.RequestID == "" || created.Replayed {
		t.Fatalf("held-lock retry result attempts=%d elapsed=%s created=%+v", attempts.Load(), elapsed, created)
	}
}

func TestArchiveMemberCreateRetryDelayHonorsContextCancellation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: (&archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}).Resolve,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int32
	callbackName := "test:archive-member-canceled-use-latch-lock"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.BackupAssetExportQuotaBucket{}).TableName() {
			attempts.Add(1)
			_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrLocked})
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Millisecond)
	defer cancel()
	if _, err := service.Create(ctx, archiveMemberCreateFixture(memberID)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled retry error=%v attempts=%d", err, attempts.Load())
	}
	if attempts.Load() < 2 || attempts.Load() >= 20 {
		t.Fatalf("canceled retry attempts=%d", attempts.Load())
	}
}

func TestArchiveMemberCreateRevalidatesIndexExpiryInsideTransaction(t *testing.T) {
	beforeExpiry := time.Now().UTC().Truncate(time.Second)
	expiresAt := beforeExpiry.Add(time.Second)
	db := archiveMemberTestDB(t, beforeExpiry)
	memberID := strings.Repeat("a", 32)
	index := archiveMemberIndexFixture(beforeExpiry, memberID)
	index.AbsoluteExpiresAt = expiresAt
	var clockCalls atomic.Int32
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: (&archiveMemberIndexFake{binding: index}).Resolve,
		Now: func() time.Time {
			if clockCalls.Add(1) == 1 {
				return beforeExpiry
			}
			return expiresAt
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var durableCreateCalls atomic.Int32
	callbackName := "test:archive-member-deadline-create-boundary"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.BackupAssetExportQuotaBucket{}).TableName() ||
			tx.Statement.Table == (model.BackupAssetArchiveMemberRequest{}).TableName() {
			durableCreateCalls.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	if _, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID)); !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("deadline-crossing create error=%v clock_calls=%d", err, clockCalls.Load())
	}
	var requestRows int64
	if err := db.Model(&model.BackupAssetArchiveMemberRequest{}).Count(&requestRows).Error; err != nil {
		t.Fatal(err)
	}
	var latchRows int64
	if err := db.Model(&model.BackupAssetExportQuotaBucket{}).Count(&latchRows).Error; err != nil {
		t.Fatal(err)
	}
	if clockCalls.Load() < 3 || durableCreateCalls.Load() != 0 || requestRows != 0 || latchRows != 0 {
		t.Fatalf("deadline-crossing durable state clock_calls=%d create_calls=%d requests=%d latches=%d",
			clockCalls.Load(), durableCreateCalls.Load(), requestRows, latchRows)
	}
}

func TestArchiveMemberCreateRollsBackFirstLatchWhenRequestInsertFails(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: (&archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}).Resolve,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	var latchInsertCalls atomic.Int32
	callbackName := "test:archive-member-request-insert-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.BackupAssetExportQuotaBucket{}).TableName() {
			latchInsertCalls.Add(1)
		}
		if tx.Statement.Table == (model.BackupAssetArchiveMemberRequest{}).TableName() {
			_ = tx.AddError(errors.New("forced archive-member request insert failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	if _, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID)); err == nil || !strings.Contains(err.Error(), "forced archive-member request insert failure") {
		t.Fatalf("request insert failure error=%v", err)
	}
	var requestRows int64
	if err := db.Model(&model.BackupAssetArchiveMemberRequest{}).Count(&requestRows).Error; err != nil {
		t.Fatal(err)
	}
	var latchRows int64
	if err := db.Model(&model.BackupAssetExportQuotaBucket{}).Count(&latchRows).Error; err != nil {
		t.Fatal(err)
	}
	if latchInsertCalls.Load() != 1 || requestRows != 0 || latchRows != 0 {
		t.Fatalf("rolled-back durable state latch_inserts=%d requests=%d latches=%d",
			latchInsertCalls.Load(), requestRows, latchRows)
	}
}

func TestArchiveMemberConflictRetryClassificationIsNarrow(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "sqlite busy", err: sqlite3.Error{Code: sqlite3.ErrBusy}},
		{name: "wrapped sqlite locked", err: fmt.Errorf("create transaction: %w", sqlite3.Error{Code: sqlite3.ErrLocked})},
		{name: "postgres deadlock", err: &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}},
		{name: "wrapped postgres serialization", err: fmt.Errorf("create transaction: %w", &pgconn.PgError{Code: "40001", Message: "could not serialize access"})},
	} {
		if !retryableArchiveMemberCreateError(testCase.err) {
			t.Fatalf("expected retryable conflict %s: %v", testCase.name, testCase.err)
		}
	}
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "sqlite lock text", err: errors.New("database table is locked")},
		{name: "deadlock text", err: errors.New("upstream said deadlock detected")},
		{name: "serialization text", err: errors.New("worker said could not serialize access: serialization failure")},
		{name: "sqlstate text", err: errors.New("SQLSTATE 40P01 SQLSTATE 40001")},
		{name: "postgres unrelated code with deadlock text", err: &pgconn.PgError{Code: "23505", Message: "deadlock detected"}},
		{name: "sqlite unrelated code", err: sqlite3.Error{Code: sqlite3.ErrConstraint}},
		{name: "foreign key", err: errors.New("FOREIGN KEY constraint failed")},
		{name: "context", err: context.DeadlineExceeded},
	} {
		if retryableArchiveMemberCreateError(testCase.err) {
			t.Fatalf("unexpected retryable conflict %s: %v", testCase.name, testCase.err)
		}
	}
}

func TestArchiveMemberConflictRetryReturnsFinalDatabaseErrorWithoutDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	finalErr := fmt.Errorf("final archive-member lock: %w", sqlite3.Error{Code: sqlite3.ErrBusy})
	var finalAttemptStarted time.Time
	err := retryArchiveMemberConflicts(ctx, func() error {
		attempts++
		if attempts == archiveMemberConflictAttempts {
			finalAttemptStarted = time.Now()
			cancel()
			return finalErr
		}
		return sqlite3.Error{Code: sqlite3.ErrBusy}
	})
	if attempts != archiveMemberConflictAttempts || !errors.Is(err, finalErr) || errors.Is(err, context.Canceled) {
		t.Fatalf("final retry result attempts=%d err=%v", attempts, err)
	}
	if elapsed := time.Since(finalAttemptStarted); elapsed >= 15*time.Millisecond {
		t.Fatalf("final failed attempt slept for %s", elapsed)
	}
}

func TestArchiveMemberCreateRejectsZeroOrMultipleHopChainsWithStableNestedReason(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, chain := range [][]string{
		{},
		{strings.Repeat("a", 32), strings.Repeat("b", 32)},
	} {
		db := archiveMemberTestDB(t, now)
		index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, strings.Repeat("a", 32))}
		service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
			DB: db, Coordinator: &archiveMemberCoordinatorFake{},
			Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
			ResolveIndex: index.Resolve, Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		request := archiveMemberCreateFixture(strings.Repeat("a", 32))
		request.MemberChain = chain
		if _, err := service.Create(context.Background(), request); !errors.Is(err, ErrArchiveNestedUnsupported) {
			t.Fatalf("chain length=%d error=%v", len(chain), err)
		}
		if index.calls != 0 {
			t.Fatalf("nested request reached index: %d", index.calls)
		}
	}
}

func TestArchiveMemberReconcileCreatesRequestKeyedExtractInterestExactlyOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
		RevalidateIndex: archiveMemberIndexRevalidator(index),
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{
				ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1",
			}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	if coordinator.calls != 1 || len(coordinator.requests) != 1 {
		t.Fatalf("processing calls=%d requests=%d", coordinator.calls, len(coordinator.requests))
	}
	work := coordinator.requests[0]
	if work.Interest.OwnerKind != InterestSystem || work.Interest.OwnerKey != archiveMemberInterestOwnerKey(created.RequestID) ||
		work.Interest.PriorityClass != PriorityInteractive || work.Descriptor.Source != archiveMemberAssetFixture().Ref ||
		work.Descriptor.CatalogGenerationID != archiveMemberAssetFixture().CatalogGenerationID ||
		work.Descriptor.SourceFingerprint != archiveMemberAssetFixture().SourceFingerprint ||
		work.Descriptor.EntryFingerprint != archiveMemberAssetFixture().EntryFingerprint ||
		work.Descriptor.ProviderCapabilityRevision != 9 || work.Descriptor.Capability != "archive.extract_entry" ||
		work.Descriptor.CapabilitySchema != "archive.extract_entry.v1" || work.Descriptor.OutputProfile != "archive_member_v1" ||
		work.Descriptor.PipelineFingerprint != "archive-extract-pipeline-v1" ||
		work.Descriptor.SecurityPolicyRevision != "security-policy-v1" ||
		work.Descriptor.Parameters.MemberStart != 7 || work.Descriptor.Parameters.MemberEnd != 7 {
		t.Fatalf("extract work=%+v", work)
	}
	if encoded, err := json.Marshal(work); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(encoded), memberID) {
		t.Fatalf("Processing work leaked raw member ID: %s", encoded)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.ProcessingInterestID == nil || *persisted.ProcessingInterestID != coordinator.result.InterestID ||
		persisted.ProcessingJobID == nil || *persisted.ProcessingJobID != coordinator.result.JobID || persisted.State != "running" {
		t.Fatalf("reconciled row=%+v", persisted)
	}
}

func TestArchiveMemberReconcileRejectsIndexReplacementBeforeProcessingInterest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
		RevalidateIndex: archiveMemberIndexRevalidator(index),
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	index.binding.ArtifactID = strings.Repeat("f", 32)
	if err := service.Reconcile(context.Background(), created.RequestID); !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("index replacement error=%v", err)
	}
	if coordinator.calls != 0 {
		t.Fatalf("stale index reached Processing calls=%d", coordinator.calls)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberQueued) || persisted.ProcessingJobID != nil || persisted.ProcessingInterestID != nil {
		t.Fatalf("stale index mutated request=%+v", persisted)
	}
}

func TestArchiveMemberPollAndCancelRequireExpectedIndexRevision(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	memberID := strings.Repeat("a", 32)
	for _, testCase := range []struct {
		name string
		call func(*ArchiveMemberService, ArchiveMemberLookup) error
	}{
		{
			name: "poll",
			call: func(service *ArchiveMemberService, lookup ArchiveMemberLookup) error {
				_, err := service.Poll(context.Background(), lookup)
				return err
			},
		},
		{name: "cancel", call: func(service *ArchiveMemberService, lookup ArchiveMemberLookup) error {
			return service.Cancel(context.Background(), lookup)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := archiveMemberTestDB(t, now)
			index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
			service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
				DB: db, Coordinator: &archiveMemberCoordinatorFake{},
				Authorize:    archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
				ResolveIndex: index.Resolve, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
			if err != nil {
				t.Fatal(err)
			}
			lookup := ArchiveMemberLookup{
				Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
				RequestID: created.RequestID, IndexRevision: strings.Repeat("f", 64),
			}
			if err := testCase.call(service, lookup); !errors.Is(err, ErrArchiveMemberUnavailable) {
				t.Fatalf("mismatched expected index revision error=%v", err)
			}
			var persisted model.BackupAssetArchiveMemberRequest
			if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
				t.Fatal(err)
			}
			if persisted.State != string(ArchiveMemberQueued) || persisted.Version != 1 {
				t.Fatalf("mismatched expected index revision mutated request=%+v", persisted)
			}
		})
	}
}

func TestArchiveMemberRatioBombPersistsGenericClosedLimitProduct(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	part, err := writer.Create("ratio-bomb.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("a"), 1<<20)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, capabilityErr := workerCapabilities.InspectArchive(archive.Bytes(), "application/zip", workerCapabilities.ArchiveLimits{
		MaxEntries: 100_000, MaxDepth: 16, MaxExpandedBytes: 8 << 30,
		MaxCompressionRatio: 2, MaxMemberBytes: 256 << 20,
	})
	if !errors.Is(capabilityErr, workerCapabilities.ErrInputLimit) {
		t.Fatalf("ratio-bomb capability error=%v", capabilityErr)
	}
	persistedCode := mapCapabilityError(capabilityErr)
	if persistedCode != ProcessingErrorInputTooLarge {
		t.Fatalf("persisted Worker code=%q", persistedCode)
	}

	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service := newArchiveMemberReconcileService(t, db, now, index, coordinator)
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, persistedCode)
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	interest := model.BackupAssetProcessingInterest{
		ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
		OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
		PriorityClass: string(PriorityInteractive), Priority: 900, Active: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&interest).Error; err != nil {
		t.Fatal(err)
	}
	status, err := service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != ArchiveMemberFailed || status.FailureProduct != ArchiveFailureLimit || !status.Terminal {
		t.Fatalf("ratio-bomb status=%+v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "ratio") {
		t.Fatalf("non-persisted ratio diagnostic escaped: %s", encoded)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.ErrorCategory != string(ArchiveFailureLimit) {
		t.Fatalf("persisted member failure=%+v", persisted)
	}
	if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
		t.Fatal(err)
	}
	if interest.Active || interest.RemovedReason != string(InterestRemovedCompleted) || interest.RemovedAt == nil {
		t.Fatalf("failed member interest=%+v", interest)
	}
}

func TestArchiveMemberFailureProjectsAuthorizedOriginalDownloadFallbackWithoutLeak(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, testCase := range []struct {
		name        string
		code        ProcessingErrorCode
		downloadErr error
		wantAction  ArchiveFallbackAction
		wantReason  ArchiveFallbackReason
	}{
		{name: "encrypted allowed", code: ProcessingErrorEncryptedArchive, wantAction: ArchiveFallbackDownloadOriginal},
		{name: "unsupported allowed", code: ProcessingErrorUnsupportedFormat, wantAction: ArchiveFallbackDownloadOriginal},
		{name: "generic limit allowed", code: ProcessingErrorInputTooLarge, wantAction: ArchiveFallbackDownloadOriginal},
		{name: "download denied", code: ProcessingErrorInputTooLarge, downloadErr: backupasset.ErrForbidden, wantReason: ArchiveFallbackOriginalUnavailable},
		{name: "outer content offline", code: ProcessingErrorUnsupportedFormat, downloadErr: backupasset.ErrCapabilityUnavailable, wantReason: ArchiveFallbackOriginalUnavailable},
		{name: "outer content unavailable", code: ProcessingErrorEncryptedArchive, downloadErr: content.ErrContentUnavailable, wantReason: ArchiveFallbackOriginalUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := archiveMemberTestDB(t, now)
			memberID := strings.Repeat("a", 32)
			index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
			coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
				JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
			}}
			authorizer := &archiveMemberActionAuthorizerFake{
				asset: archiveMemberAssetFixture(), downloadErr: testCase.downloadErr,
			}
			service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
				DB: db, Coordinator: coordinator, Authorize: authorizer, ResolveIndex: index.Resolve,
				RevalidateIndex: archiveMemberIndexRevalidator(index),
				Now:             func() time.Time { return now },
				ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
					return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
				},
				ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
					return CapabilityAdvertisement{
						SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
						PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
					}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
				t.Fatal(err)
			}
			job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, testCase.code)
			if err := db.Create(&job).Error; err != nil {
				t.Fatal(err)
			}
			interest := model.BackupAssetProcessingInterest{
				ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
				OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
				PriorityClass: string(PriorityInteractive), Priority: 900, Active: true,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&interest).Error; err != nil {
				t.Fatal(err)
			}
			status, err := service.Poll(context.Background(), ArchiveMemberLookup{
				Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
				RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
			})
			if err != nil {
				t.Fatal(err)
			}
			if status.Fallback.Action != testCase.wantAction || status.Fallback.Reason != testCase.wantReason {
				t.Fatalf("fallback=%+v", status.Fallback)
			}
			if len(authorizer.actions) != 3 || authorizer.actions[0] != content.DeliveryPreview ||
				authorizer.actions[1] != content.DeliveryPreview || authorizer.actions[2] != content.DeliveryDownload {
				t.Fatalf("authorization actions=%v", authorizer.actions)
			}
		})
	}
}

func TestArchiveMemberPollValidatesExactSucceededOutputBeforeReady(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	resolveCalls := 0
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
		RevalidateIndex: archiveMemberIndexRevalidator(index),
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{
				ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1",
			}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
		ResolveOutput: func(_ context.Context, request content.ArchiveMemberArtifactRequest) (content.ResolvedArchiveMemberArtifact, error) {
			resolveCalls++
			asset := archiveMemberAssetFixture()
			if request.OwnerUserID != 42 || request.Asset != asset || backupasset.ValidateOpaqueID(request.RequestID) != nil {
				t.Fatalf("output request=%+v", request)
			}
			return content.ResolvedArchiveMemberArtifact{
				MemberRequestID: request.RequestID, OwnerUserID: request.OwnerUserID, Ref: asset.Ref,
				CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
				EntryFingerprint:  asset.EntryFingerprint,
				MemberChainDigest: content.ArchiveMemberChainDigest(asset.Ref, strings.Repeat("3", 64), memberID),
				ProcessingJobID:   coordinator.result.JobID, ProcessingAttemptID: strings.Repeat("9", 32),
				DerivedArtifactSetID: strings.Repeat("b", 32), DerivedArtifactID: strings.Repeat("c", 32),
				DerivedBlobID: strings.Repeat("d", 32), DerivedDigest: strings.Repeat("e", 64),
				DerivedSize: 3, MediaType: "text/plain", AbsoluteExpiresAt: now.Add(time.Hour),
				Provider: asset.Provider, ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
				FingerprintStrength: asset.FingerprintStrength, SourceSize: asset.Size,
				SourceMediaType: asset.MediaType, SecurityPolicyRevision: "security-policy-v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
	job.State = string(ProcessingSucceeded)
	job.ErrorCode = ""
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	interest := model.BackupAssetProcessingInterest{
		ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
		OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
		PriorityClass: string(PriorityInteractive), Priority: 900, Active: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&interest).Error; err != nil {
		t.Fatal(err)
	}

	status, err := service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolveCalls != 1 || status.State != ArchiveMemberReady || !status.Terminal || status.FailureProduct != ArchiveFailureNone {
		t.Fatalf("resolve calls=%d status=%+v", resolveCalls, status)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberReady) || persisted.FinishedAt == nil || !persisted.FinishedAt.Equal(now) {
		t.Fatalf("persisted ready request=%+v", persisted)
	}
	if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
		t.Fatal(err)
	}
	if interest.Active || interest.RemovedReason != string(InterestRemovedCompleted) ||
		interest.RemovedAt == nil || !interest.RemovedAt.Equal(now) {
		t.Fatalf("terminal interest=%+v", interest)
	}
}

func TestArchiveMemberPollRejectsIndexReplacementAfterProcessingSucceeded(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	resolveCalls := 0
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
		ResolveOutput: func(_ context.Context, request content.ArchiveMemberArtifactRequest) (content.ResolvedArchiveMemberArtifact, error) {
			resolveCalls++
			return archiveMemberOutputFixture(now, memberID, coordinator.result.JobID, request), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
	job.State, job.ErrorCode = string(ProcessingSucceeded), ""
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	interest := model.BackupAssetProcessingInterest{
		ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
		OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
		PriorityClass: string(PriorityInteractive), Priority: 900, Active: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&interest).Error; err != nil {
		t.Fatal(err)
	}
	coordinator.removeInterest = func(
		ctx context.Context,
		jobID string,
		ownerKind InterestOwnerKind,
		ownerKey string,
		reason InterestRemovedReason,
	) error {
		if jobID != interest.JobID || ownerKind != InterestSystem ||
			ownerKey != archiveMemberInterestOwnerKey(created.RequestID) || reason != InterestRemovedSuperseded {
			t.Fatalf("index replacement removal job=%q owner=%q reason=%q", jobID, ownerKey, reason)
		}
		return db.WithContext(ctx).Model(&model.BackupAssetProcessingInterest{}).
			Where("id = ? AND job_id = ? AND active = ?", interest.ID, interest.JobID, true).
			Updates(map[string]any{
				"active": false, "removed_reason": string(reason), "removed_at": now, "updated_at": now,
			}).Error
	}

	index.binding.ArtifactID = strings.Repeat("f", 32)
	_, err = service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	})
	if !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("index replacement poll error=%v", err)
	}
	if resolveCalls != 0 {
		t.Fatalf("index replacement resolved Processing output calls=%d", resolveCalls)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) ||
		persisted.ErrorCategory != string(ArchiveFailureUnavailable) || persisted.FinishedAt == nil {
		t.Fatalf("index replacement request=%+v", persisted)
	}
	if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
		t.Fatal(err)
	}
	if interest.Active || interest.RemovedReason != string(InterestRemovedSuperseded) || interest.RemovedAt == nil {
		t.Fatalf("index replacement interest=%+v", interest)
	}
}

func TestArchiveMemberPollSourceDriftRemovesInterestAndFailsClosed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	authorizer := &archiveMemberActionAuthorizerFake{asset: archiveMemberAssetFixture()}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: authorizer, ResolveIndex: index.Resolve,
		RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	authorizer.asset.EntryFingerprint = "entry-fingerprint-v2"
	_, err = service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	})
	if !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("source drift error=%v", err)
	}
	if len(coordinator.removals) != 1 || coordinator.removals[0].jobID != coordinator.result.JobID ||
		coordinator.removals[0].ownerKey != archiveMemberInterestOwnerKey(created.RequestID) ||
		coordinator.removals[0].reason != InterestRemovedSuperseded {
		t.Fatalf("source drift removals=%+v", coordinator.removals)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.ErrorCategory != string(ArchiveFailureUnavailable) ||
		persisted.FinishedAt == nil {
		t.Fatalf("source drift request=%+v", persisted)
	}
}

func TestArchiveMemberPollLeavesRunningRequestOnTransientPreviewAuthorizationFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	authorizer := &archiveMemberActionAuthorizerFake{asset: archiveMemberAssetFixture()}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: authorizer, ResolveIndex: index.Resolve,
		RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
	job.State, job.ErrorCode, job.FinishedAt = string(ProcessingProcessing), "", nil
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	interest := model.BackupAssetProcessingInterest{
		ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
		OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
		PriorityClass: string(PriorityInteractive), Priority: 900, Active: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&interest).Error; err != nil {
		t.Fatal(err)
	}

	authorizer.previewErr = ErrArchiveMemberUnavailable
	_, err = service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	})
	if !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("transient preview authorization error=%v", err)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberRunning) || persisted.FinishedAt != nil {
		t.Fatalf("transient preview authorization terminalized request: %+v", persisted)
	}
	if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
		t.Fatal(err)
	}
	if !interest.Active || interest.RemovedAt != nil || len(coordinator.removals) != 0 {
		t.Fatalf("transient preview authorization changed Processing interest: interest=%+v removals=%+v", interest, coordinator.removals)
	}
}

func TestArchiveMemberPollSensitivityAndMalwareReauthorizationRemoveInterest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, reason := range []string{"sensitivity", "malware"} {
		t.Run(reason, func(t *testing.T) {
			db := archiveMemberTestDB(t, now)
			memberID := strings.Repeat("a", 32)
			index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
			coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
				JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
			}}
			authorizer := &archiveMemberActionAuthorizerFake{asset: archiveMemberAssetFixture()}
			service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
				DB: db, Coordinator: coordinator, Authorize: authorizer, ResolveIndex: index.Resolve,
				RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
				ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
					return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
				},
				ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
					return CapabilityAdvertisement{
						SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
						PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
					}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
				t.Fatal(err)
			}
			authorizer.previewErr = backupasset.ErrForbidden
			_, err = service.Poll(context.Background(), ArchiveMemberLookup{
				Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
				RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
			})
			if !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("reauthorization error=%v", err)
			}
			if len(coordinator.removals) != 1 || coordinator.removals[0].reason != InterestRemovedSuperseded {
				t.Fatalf("reauthorization removals=%+v", coordinator.removals)
			}
			var persisted model.BackupAssetArchiveMemberRequest
			if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
				t.Fatal(err)
			}
			if persisted.State != string(ArchiveMemberFailed) ||
				persisted.ErrorCategory != string(ArchiveFailureUnavailable) || persisted.FinishedAt == nil {
				t.Fatalf("reauthorization request=%+v", persisted)
			}
		})
	}
}

func TestArchiveMemberPollPolicyDriftRemovesInterestAndFailsClosed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	policy := "security-policy-v1"
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: policy}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
	job.State = string(ProcessingProcessing)
	job.FinishedAt = nil
	job.ErrorCode = ""
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	policy = "security-policy-v2"
	_, err = service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	})
	if !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("policy drift error=%v", err)
	}
	if len(coordinator.removals) != 1 || coordinator.removals[0].reason != InterestRemovedSuperseded {
		t.Fatalf("policy drift removals=%+v", coordinator.removals)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.ErrorCategory != string(ArchiveFailureUnavailable) {
		t.Fatalf("policy drift request=%+v", persisted)
	}
}

func TestArchiveMemberReadyCancelRevokesDeliveryBeforeDerivedOutput(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	processingJobID := strings.Repeat("6", 32)
	processingInterestID := strings.Repeat("7", 32)
	finishedAt := now
	row := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: "archive_member", KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 0,
		ProcessingInterestID: &processingInterestID, ProcessingJobID: &processingJobID,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finishedAt, Version: 2,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	output := content.ResolvedArchiveMemberArtifact{
		MemberRequestID: requestID, OwnerUserID: 42, Ref: asset.Ref,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		EntryFingerprint: asset.EntryFingerprint, MemberChainDigest: row.MemberChainDigest,
		ProcessingJobID: processingJobID, ProcessingAttemptID: strings.Repeat("9", 32),
		DerivedArtifactSetID: strings.Repeat("a", 32), DerivedArtifactID: strings.Repeat("b", 32),
		DerivedBlobID: strings.Repeat("c", 32), DerivedDigest: strings.Repeat("d", 64),
		DerivedSize: 14, MediaType: "text/plain", AbsoluteExpiresAt: row.AbsoluteExpiresAt,
		Provider: asset.Provider, ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		FingerprintStrength: asset.FingerprintStrength, SourceSize: asset.Size,
		SourceMediaType: asset.MediaType, SecurityPolicyRevision: "security-policy-v1",
	}
	var order []string
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize: archiveMemberAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveOutput: func(_ context.Context, _ content.ArchiveMemberArtifactRequest) (content.ResolvedArchiveMemberArtifact, error) {
			t.Fatal("ready cancellation used the running-output validator")
			return content.ResolvedArchiveMemberArtifact{}, ErrArchiveMemberUnavailable
		},
		ResolveReadyOutput: func(_ context.Context, request content.ArchiveMemberArtifactRequest) (content.ResolvedArchiveMemberArtifact, error) {
			if request.RequestID != requestID || request.OwnerUserID != 42 || request.Asset != asset {
				t.Fatalf("resolve request=%+v", request)
			}
			return output, nil
		},
		RevokeDeliveries: func(_ context.Context, gotRequestID, reason string) error {
			order = append(order, "delivery")
			if gotRequestID != requestID || reason != "member_canceled" {
				t.Fatalf("delivery revoke request=%q reason=%q", gotRequestID, reason)
			}
			return nil
		},
		RevokeOutput: func(_ context.Context, artifactSetID string, reason DerivedRevokeReason) error {
			order = append(order, "derived")
			if artifactSetID != output.DerivedArtifactSetID || reason != DerivedRevokeExplicit {
				t.Fatalf("Derived revoke set=%q reason=%q", artifactSetID, reason)
			}
			return nil
		},
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup := ArchiveMemberLookup{
		Actor: content.DeliveryActor{UserID: 42, Role: "admin"}, Ref: asset.Ref,
		RequestID: requestID, IndexRevision: row.IndexRevision,
	}
	if err := service.Cancel(context.Background(), lookup); err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(context.Background(), lookup); err != nil {
		t.Fatalf("idempotent ready cancel: %v", err)
	}
	if strings.Join(order, ",") != "delivery,derived" {
		t.Fatalf("revoke order=%v", order)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberCanceled) || persisted.FinishedAt == nil ||
		!persisted.FinishedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("persisted=%+v", persisted)
	}
}

func TestArchiveMemberReadyCancelFansOutRevocationFailuresAndPersistsCanceledState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	processingJobID := strings.Repeat("6", 32)
	processingInterestID := strings.Repeat("7", 32)
	finishedAt := now
	row := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: archiveMemberCreateEndpoint, KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 0,
		ProcessingInterestID: &processingInterestID, ProcessingJobID: &processingJobID,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finishedAt, Version: 2,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	output := content.ResolvedArchiveMemberArtifact{
		MemberRequestID: requestID, OwnerUserID: 42, Ref: asset.Ref,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		EntryFingerprint: asset.EntryFingerprint, MemberChainDigest: row.MemberChainDigest,
		ProcessingJobID: processingJobID, ProcessingAttemptID: strings.Repeat("9", 32),
		DerivedArtifactSetID: strings.Repeat("a", 32), DerivedArtifactID: strings.Repeat("b", 32),
		DerivedBlobID: strings.Repeat("c", 32), DerivedDigest: strings.Repeat("d", 64),
		DerivedSize: 14, MediaType: "text/plain", AbsoluteExpiresAt: row.AbsoluteExpiresAt,
		Provider: asset.Provider, ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		FingerprintStrength: asset.FingerprintStrength, SourceSize: asset.Size,
		SourceMediaType: asset.MediaType, SecurityPolicyRevision: "security-policy-v1",
	}
	deliveryErr := errors.New("delivery cancel revoke failed")
	outputErr := errors.New("Derived cancel revoke failed")
	var order []string
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveReadyOutput: func(_ context.Context, request content.ArchiveMemberArtifactRequest) (content.ResolvedArchiveMemberArtifact, error) {
			if request.RequestID != requestID || request.OwnerUserID != 42 || request.Asset != asset {
				t.Fatalf("ready cancellation output request=%+v", request)
			}
			return output, nil
		},
		RevokeDeliveries: func(_ context.Context, gotRequestID, reason string) error {
			order = append(order, "delivery")
			if gotRequestID != requestID || reason != "member_canceled" {
				t.Fatalf("delivery revoke request=%q reason=%q", gotRequestID, reason)
			}
			return deliveryErr
		},
		RevokeOutput: func(_ context.Context, artifactSetID string, reason DerivedRevokeReason) error {
			order = append(order, "derived")
			if artifactSetID != output.DerivedArtifactSetID || reason != DerivedRevokeExplicit {
				t.Fatalf("Derived revoke set=%q reason=%q", artifactSetID, reason)
			}
			return outputErr
		},
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = service.Cancel(context.Background(), ArchiveMemberLookup{
		Actor: content.DeliveryActor{UserID: 42, Role: "admin"}, Ref: asset.Ref,
		RequestID: requestID, IndexRevision: row.IndexRevision,
	})
	if !errors.Is(err, deliveryErr) || !errors.Is(err, outputErr) {
		t.Fatalf("cancel error=%v, want both revoke errors", err)
	}
	if strings.Join(order, ",") != "delivery,derived" {
		t.Fatalf("cancel partial revoke order=%v", order)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberCanceled) || persisted.FinishedAt == nil {
		t.Fatalf("partial cancel left request servable: %+v", persisted)
	}
}

func TestArchiveMemberReadyKeyLossRevokesDeliveryBeforeExactDerivedOutput(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	processingJobID := strings.Repeat("6", 32)
	processingInterestID := strings.Repeat("7", 32)
	artifactSetID := strings.Repeat("a", 32)
	finishedAt := now
	row := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: "archive_member", KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 0,
		ProcessingInterestID: &processingInterestID, ProcessingJobID: &processingJobID,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finishedAt, Version: 2,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, processingJobID, "")
	job.State = string(ProcessingSucceeded)
	job.ErrorCode = ""
	job.CurrentArtifactSetID = &artifactSetID
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	var order []string
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		RevokeDeliveries: func(_ context.Context, gotRequestID, reason string) error {
			order = append(order, "delivery")
			if gotRequestID != requestID || reason != "key_loss" {
				t.Fatalf("delivery revoke request=%q reason=%q", gotRequestID, reason)
			}
			return nil
		},
		RevokeOutput: func(_ context.Context, gotArtifactSetID string, reason DerivedRevokeReason) error {
			order = append(order, "derived")
			if gotArtifactSetID != artifactSetID || reason != DerivedRevokeKeyLoss {
				t.Fatalf("Derived revoke set=%q reason=%q", gotArtifactSetID, reason)
			}
			return nil
		},
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Invalidate(context.Background(), requestID, DerivedRevokeKeyLoss); err != nil {
		t.Fatal(err)
	}
	if err := service.Invalidate(context.Background(), requestID, DerivedRevokeKeyLoss); err != nil {
		t.Fatalf("idempotent key-loss invalidation: %v", err)
	}
	if strings.Join(order, ",") != "delivery,derived" {
		t.Fatalf("key-loss revoke order=%v", order)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.ErrorCategory != string(ArchiveFailureUnavailable) ||
		persisted.FinishedAt == nil {
		t.Fatalf("key-loss request=%+v", persisted)
	}
}

func TestArchiveMemberCancelRemovesExactInterestAndPersistsIdempotentTerminalState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service := newArchiveMemberReconcileService(t, db, now, index, coordinator)
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	lookup := ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	}
	if err := service.Cancel(context.Background(), lookup); err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(context.Background(), lookup); err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if len(coordinator.removals) != 1 {
		t.Fatalf("interest removals=%+v", coordinator.removals)
	}
	removal := coordinator.removals[0]
	if removal.jobID != coordinator.result.JobID || removal.ownerKind != InterestSystem ||
		removal.ownerKey != archiveMemberInterestOwnerKey(created.RequestID) || removal.reason != InterestRemovedCanceled {
		t.Fatalf("interest removal=%+v", removal)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberCanceled) || persisted.FinishedAt == nil || !persisted.FinishedAt.Equal(now) ||
		persisted.ErrorCategory != "" {
		t.Fatalf("canceled request=%+v", persisted)
	}
}

func TestArchiveMemberPollExpiresAndRemovesInterestBeforeLoadingProcessingOutput(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	now := start
	db := archiveMemberTestDB(t, start)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(start, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
		RevalidateIndex: archiveMemberIndexRevalidator(index),
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{
				ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1",
			}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	now = start.Add(2 * time.Hour)
	status, err := service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != ArchiveMemberExpired || !status.Terminal || status.FailureProduct != ArchiveFailureNone {
		t.Fatalf("expired status=%+v", status)
	}
	if len(coordinator.removals) != 1 || coordinator.removals[0].reason != InterestRemovedExpired {
		t.Fatalf("expiry removals=%+v", coordinator.removals)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberExpired) || persisted.FinishedAt == nil || !persisted.FinishedAt.Equal(now) {
		t.Fatalf("expired request=%+v", persisted)
	}
}

func TestArchiveMemberPollReconcilesCanceledProcessingAfterPostRemovalCrash(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service := newArchiveMemberReconcileService(t, db, now, index, coordinator)
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(context.Background(), created.RequestID); err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
	job.State = string(ProcessingCanceled)
	job.ErrorCode = ""
	job.CancelReason = string(CancelReasonInterestWithdrawn)
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	removedAt := now
	interest := model.BackupAssetProcessingInterest{
		ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
		OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
		PriorityClass: string(PriorityInteractive), Priority: 900, Active: false,
		RemovedReason: string(InterestRemovedCanceled), RemovedAt: &removedAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&interest).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingInterest{}).Where("id = ?", interest.ID).Updates(map[string]any{
		"active": false, "removed_reason": string(InterestRemovedCanceled), "removed_at": removedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	status, err := service.Poll(context.Background(), ArchiveMemberLookup{
		Actor: archiveMemberCreateFixture(memberID).Actor, Ref: archiveMemberAssetFixture().Ref,
		RequestID: created.RequestID, IndexRevision: archiveMemberCreateFixture(memberID).IndexRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != ArchiveMemberCanceled || !status.Terminal || status.FailureProduct != ArchiveFailureNone {
		t.Fatalf("reconciled canceled status=%+v", status)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberCanceled) || persisted.FinishedAt == nil {
		t.Fatalf("reconciled canceled request=%+v", persisted)
	}
}

func TestArchiveMemberMaintenanceBindsQueuedAndProjectsSucceededWithoutPoll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	authorizer := &archiveMemberActionAuthorizerFake{asset: archiveMemberAssetFixture()}
	outputCalls := 0
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: authorizer, ResolveIndex: index.Resolve,
		RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return archiveMemberAssetFixture(), nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
		ResolveOutput: func(_ context.Context, request content.ArchiveMemberArtifactRequest) (content.ResolvedArchiveMemberArtifact, error) {
			outputCalls++
			return archiveMemberOutputFixture(now, memberID, coordinator.result.JobID, request), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("bind queued reconciled=%d err=%v", reconciled, err)
	}
	var bound model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&bound).Error; err != nil {
		t.Fatal(err)
	}
	if bound.State != string(ArchiveMemberRunning) || bound.ProcessingJobID == nil || bound.ProcessingInterestID == nil {
		t.Fatalf("queued request was not bound: %+v", bound)
	}
	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
	job.State, job.ErrorCode = string(ProcessingSucceeded), ""
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	interest := model.BackupAssetProcessingInterest{
		ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
		OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
		PriorityClass: string(PriorityInteractive), Priority: 900, Active: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&interest).Error; err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("project ready reconciled=%d err=%v", reconciled, err)
	}
	var ready model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&ready).Error; err != nil {
		t.Fatal(err)
	}
	if ready.State != string(ArchiveMemberReady) || ready.FinishedAt == nil || outputCalls != 1 {
		t.Fatalf("maintenance did not project ready request=%+v outputCalls=%d", ready, outputCalls)
	}
	if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
		t.Fatal(err)
	}
	if interest.Active || interest.RemovedReason != string(InterestRemovedCompleted) || interest.RemovedAt == nil {
		t.Fatalf("maintenance did not remove terminal interest: %+v", interest)
	}
	if len(authorizer.actions) != 1 || authorizer.actions[0] != content.DeliveryPreview {
		t.Fatalf("maintenance used HTTP authorization/fallback path: %v", authorizer.actions)
	}
}

func TestArchiveMemberMaintenanceExpiresRunningRequestBeforeProcessingProjection(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	now := start
	db := archiveMemberTestDB(t, start)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(start, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return archiveMemberAssetFixture(), nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReconcilePending(context.Background(), 1); err != nil {
		t.Fatalf("bind queued: %v", err)
	}
	now = start.Add(2 * time.Hour)
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("expire running reconciled=%d err=%v", reconciled, err)
	}
	var expired model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if expired.State != string(ArchiveMemberExpired) || expired.FinishedAt == nil || !expired.FinishedAt.Equal(now) {
		t.Fatalf("maintenance did not expire request: %+v", expired)
	}
	if len(coordinator.removals) != 1 || coordinator.removals[0].reason != InterestRemovedExpired {
		t.Fatalf("expiry did not remove interest first: %+v", coordinator.removals)
	}
}

func TestArchiveMemberMaintenanceRevokesReadyDeliveryAndOutputAfterAuthorizationLossWithoutPoll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	processingJobID := strings.Repeat("6", 32)
	processingInterestID := strings.Repeat("7", 32)
	artifactSetID := strings.Repeat("a", 32)
	finished := now
	ready := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: archiveMemberCreateEndpoint, KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 7,
		ProcessingInterestID: &processingInterestID, ProcessingJobID: &processingJobID,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finished, Version: 2,
	}
	terminal := ready
	terminal.ID = strings.Repeat("0", 32)
	terminal.OwnerUserID = 43
	terminal.KeyDigest = strings.Repeat("9", 64)
	terminal.RequestIntentDigest = strings.Repeat("b", 64)
	terminal.State = string(ArchiveMemberFailed)
	terminal.ErrorCategory = string(ArchiveFailureUnavailable)
	if err := db.Create(&ready).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&terminal).Error; err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, processingJobID, "")
	job.State, job.ErrorCode, job.CurrentArtifactSetID = string(ProcessingSucceeded), "", &artifactSetID
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	var order []string
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return content.AuthorizedAsset{}, backupasset.ErrForbidden
		},
		RevokeDeliveries: func(_ context.Context, gotRequestID, reason string) error {
			order = append(order, "delivery")
			if gotRequestID != requestID || reason != "policy_changed" {
				t.Fatalf("delivery revoke request=%q reason=%q", gotRequestID, reason)
			}
			return nil
		},
		RevokeOutput: func(_ context.Context, gotArtifactSetID string, reason DerivedRevokeReason) error {
			order = append(order, "derived")
			if gotArtifactSetID != artifactSetID || reason != DerivedRevokePolicyChanged {
				t.Fatalf("Derived revoke set=%q reason=%q", gotArtifactSetID, reason)
			}
			return nil
		},
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("ready reconciliation=%d err=%v", reconciled, err)
	}
	if strings.Join(order, ",") != "delivery,derived" {
		t.Fatalf("ready revoke order=%v", order)
	}
	var persistedReady model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&persistedReady).Error; err != nil {
		t.Fatal(err)
	}
	if persistedReady.State != string(ArchiveMemberFailed) || persistedReady.ErrorCategory != string(ArchiveFailureUnavailable) || persistedReady.FinishedAt == nil {
		t.Fatalf("ready authorization loss was not terminalized: %+v", persistedReady)
	}
	var persistedTerminal model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", terminal.ID).Take(&persistedTerminal).Error; err != nil {
		t.Fatal(err)
	}
	if persistedTerminal.State != string(ArchiveMemberFailed) || !persistedTerminal.FinishedAt.Equal(*terminal.FinishedAt) {
		t.Fatalf("terminal row was reopened: %+v", persistedTerminal)
	}
}

func TestArchiveMemberMaintenanceExpiresReadyRequestAndRevokesDeliveryAndOutputWithoutPoll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	processingJobID := strings.Repeat("6", 32)
	processingInterestID := strings.Repeat("7", 32)
	artifactSetID := strings.Repeat("a", 32)
	finished := now
	ready := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: archiveMemberCreateEndpoint, KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 7,
		ProcessingInterestID: &processingInterestID, ProcessingJobID: &processingJobID,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finished, Version: 2,
	}
	if err := db.Create(&ready).Error; err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, processingJobID, "")
	job.State, job.ErrorCode, job.CurrentArtifactSetID = string(ProcessingSucceeded), "", &artifactSetID
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	var order []string
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			t.Fatal("ready expiry resolved a runtime asset before revocation")
			return content.AuthorizedAsset{}, nil
		},
		RevokeDeliveries: func(_ context.Context, gotRequestID, reason string) error {
			order = append(order, "delivery")
			if gotRequestID != requestID || reason != "member_expired" {
				t.Fatalf("delivery revoke request=%q reason=%q", gotRequestID, reason)
			}
			return nil
		},
		RevokeOutput: func(_ context.Context, gotArtifactSetID string, reason DerivedRevokeReason) error {
			order = append(order, "derived")
			if gotArtifactSetID != artifactSetID || reason != DerivedRevokeExpired {
				t.Fatalf("Derived revoke set=%q reason=%q", gotArtifactSetID, reason)
			}
			return nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("ready expiry reconciliation=%d err=%v", reconciled, err)
	}
	if strings.Join(order, ",") != "delivery,derived" {
		t.Fatalf("ready expiry revoke order=%v", order)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberExpired) || persisted.FinishedAt == nil || !persisted.FinishedAt.Equal(now) {
		t.Fatalf("ready expiry did not close request: %+v", persisted)
	}
	if _, err := service.AuthorizeReadyDelivery(context.Background(), ArchiveMemberLookup{
		Actor: content.DeliveryActor{UserID: 42, Role: "admin"}, Ref: asset.Ref, RequestID: requestID,
	}); !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("expired ready request remained deliverable: %v", err)
	}
}

func TestArchiveMemberMaintenanceRevalidatesReadyAuthorityDriftWithoutPoll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	processingJobID := strings.Repeat("6", 32)
	processingInterestID := strings.Repeat("7", 32)
	artifactSetID := strings.Repeat("a", 32)
	finished := now
	ready := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: archiveMemberCreateEndpoint, KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 7,
		ProcessingInterestID: &processingInterestID, ProcessingJobID: &processingJobID,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finished, Version: 2,
	}
	if err := db.Create(&ready).Error; err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, processingJobID, "")
	job.State, job.ErrorCode, job.CurrentArtifactSetID = string(ProcessingSucceeded), "", &artifactSetID
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	var order []string
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return asset, nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{
				ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
				SecurityPolicyRevision:     "security-policy-v2",
			}, nil
		},
		RevokeDeliveries: func(_ context.Context, gotRequestID, reason string) error {
			order = append(order, "delivery")
			if gotRequestID != requestID || reason != "policy_changed" {
				t.Fatalf("delivery revoke request=%q reason=%q", gotRequestID, reason)
			}
			return nil
		},
		RevokeOutput: func(_ context.Context, gotArtifactSetID string, reason DerivedRevokeReason) error {
			order = append(order, "derived")
			if gotArtifactSetID != artifactSetID || reason != DerivedRevokePolicyChanged {
				t.Fatalf("Derived revoke set=%q reason=%q", gotArtifactSetID, reason)
			}
			return nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("ready authority reconciliation=%d err=%v", reconciled, err)
	}
	if strings.Join(order, ",") != "delivery,derived" {
		t.Fatalf("ready authority revoke order=%v", order)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.ErrorCategory != string(ArchiveFailureUnavailable) || persisted.FinishedAt == nil {
		t.Fatalf("ready authority drift remained deliverable: %+v", persisted)
	}
}

func TestArchiveMemberMaintenanceRetriesTerminalAuthorityRevocationWithoutResurrectingRequest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	processingJobID := strings.Repeat("6", 32)
	processingInterestID := strings.Repeat("7", 32)
	artifactSetID := strings.Repeat("a", 32)
	finished := now
	request := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: archiveMemberCreateEndpoint, KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 7,
		ProcessingInterestID: &processingInterestID, ProcessingJobID: &processingJobID,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finished, Version: 2,
	}
	if err := db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, processingJobID, "")
	job.State, job.ErrorCode, job.CurrentArtifactSetID = string(ProcessingSucceeded), "", &artifactSetID
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: artifactSetID, JobID: processingJobID, AttemptID: strings.Repeat("b", 32), WorkKey: job.WorkKey,
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: asset.Ref.EntryID, SourceFingerprint: asset.SourceFingerprint,
		SecurityPolicyRevision: "security-policy-v1", ManifestDigest: strings.Repeat("c", 64),
		State: "active", Completeness: "complete", ArtifactCount: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	var deliveryCalls, outputCalls int
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return asset, nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{}, backupasset.ErrForbidden
		},
		RevokeDeliveries: func(_ context.Context, gotRequestID, reason string) error {
			deliveryCalls++
			if gotRequestID != requestID || reason != "policy_changed" {
				t.Fatalf("delivery revoke request=%q reason=%q", gotRequestID, reason)
			}
			return nil
		},
		RevokeOutput: func(_ context.Context, gotArtifactSetID string, reason DerivedRevokeReason) error {
			outputCalls++
			if gotArtifactSetID != artifactSetID || reason != DerivedRevokePolicyChanged {
				t.Fatalf("Derived revoke set=%q reason=%q", gotArtifactSetID, reason)
			}
			if outputCalls == 1 {
				return ErrArchiveMemberUnavailable
			}
			return db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ? AND state = ?", artifactSetID, "active").
				Updates(map[string]any{"state": "superseded", "revocation_reason": string(reason), "updated_at": now}).Error
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 ||
		!errors.Is(err, ErrArchiveMemberUnavailable) || !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("first terminal authority reconciliation=%d err=%v", reconciled, err)
	}
	var terminalized model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&terminalized).Error; err != nil {
		t.Fatal(err)
	}
	if terminalized.State != string(ArchiveMemberFailed) || terminalized.ErrorCategory != string(ArchiveFailureUnavailable) || terminalized.FinishedAt == nil {
		t.Fatalf("authority loss did not terminalize request: %+v", terminalized)
	}
	if _, err := service.AuthorizeReadyDelivery(context.Background(), ArchiveMemberLookup{
		Actor: content.DeliveryActor{UserID: 42, Role: "admin"}, Ref: asset.Ref, RequestID: requestID,
	}); !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("terminal authority request remained directly deliverable: %v", err)
	}
	var pendingSet model.BackupAssetDerivedArtifactSet
	if err := db.Where("id = ?", artifactSetID).Take(&pendingSet).Error; err != nil {
		t.Fatal(err)
	}
	if pendingSet.State != "active" || outputCalls != 1 {
		t.Fatalf("retryable Derived revoke was not durably pending: set=%+v output_calls=%d", pendingSet, outputCalls)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("retry terminal authority reconciliation=%d err=%v", reconciled, err)
	}
	var repaired model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&repaired).Error; err != nil {
		t.Fatal(err)
	}
	if repaired.State != string(ArchiveMemberFailed) || repaired.ErrorCategory != string(ArchiveFailureUnavailable) ||
		repaired.FinishedAt == nil || repaired.Version != terminalized.Version || !repaired.FinishedAt.Equal(*terminalized.FinishedAt) {
		t.Fatalf("retry resurrected or rewrote terminal request: before=%+v after=%+v", terminalized, repaired)
	}
	if outputCalls != 2 || deliveryCalls != 2 {
		t.Fatalf("terminal cleanup calls: delivery=%d output=%d", deliveryCalls, outputCalls)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 0 {
		t.Fatalf("completed terminal cleanup remained pending: reconciled=%d err=%v", reconciled, err)
	}
}

func TestArchiveMemberMaintenanceReportsCleanupFailureAfterRevisionOrFingerprintDrift(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		asset     content.AuthorizedAsset
		authority ArchiveMemberProcessingAuthority
	}{
		{
			name:  "provider_capability_revision",
			asset: archiveMemberAssetFixture(),
			authority: ArchiveMemberProcessingAuthority{
				ProviderCapabilityRevision: 10,
				SecurityPolicyRevision:     "security-policy-v1",
			},
		},
		{
			name: "entry_fingerprint",
			asset: func() content.AuthorizedAsset {
				asset := archiveMemberAssetFixture()
				asset.EntryFingerprint = "entry-fingerprint-v2"
				return asset
			}(),
			authority: ArchiveMemberProcessingAuthority{
				ProviderCapabilityRevision: 9,
				SecurityPolicyRevision:     "security-policy-v1",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			db := archiveMemberTestDB(t, now)
			fixture := archiveMemberMaintenanceCleanupFixture(now, now.Add(time.Hour))
			if err := db.Create(&fixture.request).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&fixture.job).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&fixture.artifactSet).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&fixture.deliveryGrant).Error; err != nil {
				t.Fatal(err)
			}

			cleanupErr := errors.New("Derived revoke temporarily unavailable")
			var deliveryCalls, outputCalls int
			service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
				DB: db, Coordinator: &archiveMemberCoordinatorFake{},
				Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
				ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
					return ArchiveMemberIndexBinding{}, nil
				},
				ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
					return testCase.asset, nil
				},
				ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
					return testCase.authority, nil
				},
				RevokeDeliveries: func(_ context.Context, requestID, reason string) error {
					deliveryCalls++
					return db.Model(&model.BackupAssetExportDeliveryGrant{}).
						Where("member_request_id = ? AND state = ?", requestID, "active").
						Updates(map[string]any{"state": "revoked", "revoke_reason": reason, "updated_at": now}).Error
				},
				RevokeOutput: func(_ context.Context, artifactSetID string, reason DerivedRevokeReason) error {
					outputCalls++
					if outputCalls == 1 {
						return errors.Join(ErrArchiveMemberUnavailable, cleanupErr)
					}
					return db.Model(&model.BackupAssetDerivedArtifactSet{}).
						Where("id = ? AND state = ?", artifactSetID, "active").
						Updates(map[string]any{"state": "superseded", "revocation_reason": string(reason), "updated_at": now}).Error
				},
				Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}

			if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 ||
				!errors.Is(err, cleanupErr) || !errors.Is(err, ErrArchiveMemberUnavailable) {
				t.Fatalf("first terminal cleanup reconciliation=%d err=%v", reconciled, err)
			}
			var terminalized model.BackupAssetArchiveMemberRequest
			if err := db.Where("id = ?", fixture.request.ID).Take(&terminalized).Error; err != nil {
				t.Fatal(err)
			}
			if terminalized.State != string(ArchiveMemberFailed) || terminalized.ErrorCategory != string(ArchiveFailureUnavailable) ||
				terminalized.FinishedAt == nil {
				t.Fatalf("drift cleanup did not terminalize request: %+v", terminalized)
			}
			if _, err := service.AuthorizeReadyDelivery(context.Background(), ArchiveMemberLookup{
				Actor: content.DeliveryActor{UserID: fixture.request.OwnerUserID, Role: "admin"},
				Ref:   fixture.asset.Ref, RequestID: fixture.request.ID,
			}); !errors.Is(err, ErrArchiveMemberUnavailable) {
				t.Fatalf("terminalized drift request remained deliverable: %v", err)
			}
			var activeSet model.BackupAssetDerivedArtifactSet
			if err := db.Where("id = ?", fixture.artifactSet.ID).Take(&activeSet).Error; err != nil {
				t.Fatal(err)
			}
			if activeSet.State != "active" || outputCalls != 1 || deliveryCalls != 1 {
				t.Fatalf("failed cleanup was not durably pending: set=%+v output_calls=%d delivery_calls=%d",
					activeSet, outputCalls, deliveryCalls)
			}

			if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
				t.Fatalf("retry terminal cleanup reconciliation=%d err=%v", reconciled, err)
			}
			var repaired model.BackupAssetArchiveMemberRequest
			if err := db.Where("id = ?", fixture.request.ID).Take(&repaired).Error; err != nil {
				t.Fatal(err)
			}
			if repaired.State != terminalized.State || repaired.ErrorCategory != terminalized.ErrorCategory ||
				repaired.Version != terminalized.Version || repaired.FinishedAt == nil ||
				!repaired.FinishedAt.Equal(*terminalized.FinishedAt) {
				t.Fatalf("terminal cleanup retried by reopening request: before=%+v after=%+v", terminalized, repaired)
			}
			if err := db.Where("id = ?", fixture.artifactSet.ID).Take(&activeSet).Error; err != nil {
				t.Fatal(err)
			}
			if activeSet.State != "superseded" || outputCalls != 2 || deliveryCalls != 2 {
				t.Fatalf("next maintenance pass did not clear pending output: set=%+v output_calls=%d delivery_calls=%d",
					activeSet, outputCalls, deliveryCalls)
			}
			if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 0 {
				t.Fatalf("completed drift cleanup remained pending: reconciled=%d err=%v", reconciled, err)
			}
		})
	}
}

func TestArchiveMemberMaintenanceKeepsDeliveryOnlyTerminalCleanupObservableUntilSuccess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	fixture := archiveMemberMaintenanceCleanupFixture(now, now.Add(time.Hour))
	fixture.request.State = string(ArchiveMemberFailed)
	fixture.request.ErrorCategory = string(ArchiveFailureUnavailable)
	fixture.request.Version = 3
	fixture.artifactSet.State = "superseded"
	if err := db.Create(&fixture.request).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.artifactSet).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.deliveryGrant).Error; err != nil {
		t.Fatal(err)
	}

	cleanupErr := errors.New("delivery revoke temporarily unavailable")
	deliveryCalls := 0
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize: archiveMemberAuthorizerFake{asset: fixture.asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		RevokeDeliveries: func(_ context.Context, requestID, reason string) error {
			deliveryCalls++
			if deliveryCalls < 3 {
				return errors.Join(ErrArchiveMemberUnavailable, cleanupErr)
			}
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).
				Where("member_request_id = ? AND state = ?", requestID, "active").
				Updates(map[string]any{"state": "revoked", "revoke_reason": reason, "updated_at": now}).Error
		},
		RevokeOutput: func(context.Context, string, DerivedRevokeReason) error {
			t.Fatal("delivery-only cleanup attempted Derived revocation")
			return nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	var terminalized model.BackupAssetArchiveMemberRequest
	for attempt := 1; attempt <= 2; attempt++ {
		if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 ||
			!errors.Is(err, cleanupErr) || !errors.Is(err, ErrArchiveMemberUnavailable) {
			t.Fatalf("delivery-only cleanup attempt=%d reconciled=%d err=%v", attempt, reconciled, err)
		}
		var current model.BackupAssetArchiveMemberRequest
		if err := db.Where("id = ?", fixture.request.ID).Take(&current).Error; err != nil {
			t.Fatal(err)
		}
		if current.State != string(ArchiveMemberFailed) || current.ErrorCategory != string(ArchiveFailureUnavailable) ||
			current.FinishedAt == nil || current.Version != fixture.request.Version {
			t.Fatalf("delivery-only retry changed terminal request: %+v", current)
		}
		if attempt == 1 {
			terminalized = current
		}
		if current.FinishedAt == nil || !current.FinishedAt.Equal(*terminalized.FinishedAt) {
			t.Fatalf("delivery-only retry rewrote terminal completion: before=%+v after=%+v", terminalized, current)
		}
		if _, err := service.AuthorizeReadyDelivery(context.Background(), ArchiveMemberLookup{
			Actor: content.DeliveryActor{UserID: fixture.request.OwnerUserID, Role: "admin"},
			Ref:   fixture.asset.Ref, RequestID: fixture.request.ID,
		}); !errors.Is(err, ErrArchiveMemberUnavailable) {
			t.Fatalf("delivery-only terminal request remained deliverable: %v", err)
		}
		var pendingGrant model.BackupAssetExportDeliveryGrant
		if err := db.Where("id = ?", fixture.deliveryGrant.ID).Take(&pendingGrant).Error; err != nil {
			t.Fatal(err)
		}
		if pendingGrant.State != "active" {
			t.Fatalf("failed delivery-only cleanup was not durably pending: %+v", pendingGrant)
		}
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("delivery-only cleanup success reconciliation=%d err=%v", reconciled, err)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", fixture.deliveryGrant.ID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.State != "revoked" || deliveryCalls != 3 {
		t.Fatalf("delivery-only cleanup was not completed: grant=%+v delivery_calls=%d", grant, deliveryCalls)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 0 {
		t.Fatalf("completed delivery-only cleanup remained pending: reconciled=%d err=%v", reconciled, err)
	}
}

func TestArchiveMemberMaintenanceRetriesExpiredCleanupAndReportsFailures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	fixture := archiveMemberMaintenanceCleanupFixture(now, now)
	if err := db.Create(&fixture.request).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.artifactSet).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.deliveryGrant).Error; err != nil {
		t.Fatal(err)
	}

	deliveryErr := errors.New("expiry delivery revoke temporarily unavailable")
	outputErr := errors.New("expiry Derived revoke temporarily unavailable")
	var deliveryCalls, outputCalls int
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize: archiveMemberAuthorizerFake{asset: fixture.asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			t.Fatal("expired request resolved runtime asset before revocation")
			return content.AuthorizedAsset{}, nil
		},
		RevokeDeliveries: func(_ context.Context, requestID, reason string) error {
			deliveryCalls++
			if deliveryCalls < 3 {
				return errors.Join(ErrArchiveMemberUnavailable, deliveryErr)
			}
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).
				Where("member_request_id = ? AND state = ?", requestID, "active").
				Updates(map[string]any{"state": "revoked", "revoke_reason": reason, "updated_at": now}).Error
		},
		RevokeOutput: func(_ context.Context, artifactSetID string, reason DerivedRevokeReason) error {
			outputCalls++
			if outputCalls < 3 {
				return errors.Join(ErrArchiveMemberUnavailable, outputErr)
			}
			return db.Model(&model.BackupAssetDerivedArtifactSet{}).
				Where("id = ? AND state = ?", artifactSetID, "active").
				Updates(map[string]any{"state": "superseded", "revocation_reason": string(reason), "updated_at": now}).Error
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	var expired model.BackupAssetArchiveMemberRequest
	for attempt := 1; attempt <= 2; attempt++ {
		if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 ||
			!errors.Is(err, deliveryErr) || !errors.Is(err, outputErr) || !errors.Is(err, ErrArchiveMemberUnavailable) {
			t.Fatalf("expired cleanup attempt=%d reconciled=%d err=%v", attempt, reconciled, err)
		}
		var current model.BackupAssetArchiveMemberRequest
		if err := db.Where("id = ?", fixture.request.ID).Take(&current).Error; err != nil {
			t.Fatal(err)
		}
		if current.State != string(ArchiveMemberExpired) || current.ErrorCategory != "" || current.FinishedAt == nil {
			t.Fatalf("expiry did not remain terminal: %+v", current)
		}
		if attempt == 1 {
			expired = current
		}
		if current.Version != expired.Version || !current.FinishedAt.Equal(*expired.FinishedAt) {
			t.Fatalf("expiry cleanup reopened or rewrote request: before=%+v after=%+v", expired, current)
		}
		if _, err := service.AuthorizeReadyDelivery(context.Background(), ArchiveMemberLookup{
			Actor: content.DeliveryActor{UserID: fixture.request.OwnerUserID, Role: "admin"},
			Ref:   fixture.asset.Ref, RequestID: fixture.request.ID,
		}); !errors.Is(err, ErrArchiveMemberUnavailable) {
			t.Fatalf("expired request remained deliverable: %v", err)
		}
		var pendingSet model.BackupAssetDerivedArtifactSet
		if err := db.Where("id = ?", fixture.artifactSet.ID).Take(&pendingSet).Error; err != nil {
			t.Fatal(err)
		}
		var pendingGrant model.BackupAssetExportDeliveryGrant
		if err := db.Where("id = ?", fixture.deliveryGrant.ID).Take(&pendingGrant).Error; err != nil {
			t.Fatal(err)
		}
		if pendingSet.State != "active" || pendingGrant.State != "active" {
			t.Fatalf("failed expiry cleanup was not durably pending: set=%+v grant=%+v", pendingSet, pendingGrant)
		}
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("expired cleanup success reconciliation=%d err=%v", reconciled, err)
	}
	var activeSet model.BackupAssetDerivedArtifactSet
	if err := db.Where("id = ?", fixture.artifactSet.ID).Take(&activeSet).Error; err != nil {
		t.Fatal(err)
	}
	var grant model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", fixture.deliveryGrant.ID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if activeSet.State != "superseded" || grant.State != "revoked" || outputCalls != 3 || deliveryCalls != 3 {
		t.Fatalf("expired cleanup did not clear durable downstream rows: set=%+v grant=%+v output_calls=%d delivery_calls=%d",
			activeSet, grant, outputCalls, deliveryCalls)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 0 {
		t.Fatalf("completed expired cleanup remained pending: reconciled=%d err=%v", reconciled, err)
	}
}

func TestArchiveMemberMaintenanceRetriesCanceledReadyCleanupAndReportsFailures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	fixture := archiveMemberMaintenanceCleanupFixture(now, now.Add(time.Hour))
	if err := db.Create(&fixture.request).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.artifactSet).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.deliveryGrant).Error; err != nil {
		t.Fatal(err)
	}

	readyOutput := archiveMemberOutputFixture(now, strings.Repeat("a", 32), fixture.job.ID, content.ArchiveMemberArtifactRequest{
		RequestID: fixture.request.ID, OwnerUserID: fixture.request.OwnerUserID, Asset: fixture.asset,
	})
	readyOutput.MemberChainDigest = fixture.request.MemberChainDigest
	readyOutput.ProcessingAttemptID = fixture.artifactSet.AttemptID
	readyOutput.DerivedArtifactSetID = fixture.artifactSet.ID
	deliveryErr := errors.New("canceled delivery revoke temporarily unavailable")
	outputErr := errors.New("canceled Derived revoke temporarily unavailable")
	var deliveryCalls, outputCalls int
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize: archiveMemberAuthorizerFake{asset: fixture.asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveReadyOutput: func(_ context.Context, request content.ArchiveMemberArtifactRequest) (content.ResolvedArchiveMemberArtifact, error) {
			if request.RequestID != fixture.request.ID || request.OwnerUserID != fixture.request.OwnerUserID || request.Asset != fixture.asset {
				t.Fatalf("canceled ready output request=%+v", request)
			}
			return readyOutput, nil
		},
		RevokeDeliveries: func(_ context.Context, requestID, reason string) error {
			deliveryCalls++
			if deliveryCalls < 3 {
				return errors.Join(ErrArchiveMemberUnavailable, deliveryErr)
			}
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).
				Where("member_request_id = ? AND state = ?", requestID, "active").
				Updates(map[string]any{"state": "revoked", "revoke_reason": reason, "updated_at": now}).Error
		},
		RevokeOutput: func(_ context.Context, artifactSetID string, reason DerivedRevokeReason) error {
			outputCalls++
			if outputCalls < 3 {
				return errors.Join(ErrArchiveMemberUnavailable, outputErr)
			}
			return db.Model(&model.BackupAssetDerivedArtifactSet{}).
				Where("id = ? AND state = ?", artifactSetID, "active").
				Updates(map[string]any{"state": "superseded", "revocation_reason": string(reason), "updated_at": now}).Error
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup := ArchiveMemberLookup{
		Actor: content.DeliveryActor{UserID: fixture.request.OwnerUserID, Role: "admin"},
		Ref:   fixture.asset.Ref, RequestID: fixture.request.ID, IndexRevision: fixture.request.IndexRevision,
	}
	if err := service.Cancel(context.Background(), lookup); !errors.Is(err, deliveryErr) || !errors.Is(err, outputErr) {
		t.Fatalf("cancel cleanup error=%v", err)
	}
	var canceled model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", fixture.request.ID).Take(&canceled).Error; err != nil {
		t.Fatal(err)
	}
	if canceled.State != string(ArchiveMemberCanceled) || canceled.FinishedAt == nil {
		t.Fatalf("cancel did not terminalize request: %+v", canceled)
	}
	if _, err := service.AuthorizeReadyDelivery(context.Background(), lookup); !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("canceled request remained deliverable: %v", err)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 ||
		!errors.Is(err, deliveryErr) || !errors.Is(err, outputErr) || !errors.Is(err, ErrArchiveMemberUnavailable) ||
		!archiveMemberMaintenanceRetryable(err) {
		t.Fatalf("canceled cleanup retry=%d err=%v", reconciled, err)
	}
	var retried model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", fixture.request.ID).Take(&retried).Error; err != nil {
		t.Fatal(err)
	}
	if retried.State != canceled.State || retried.Version != canceled.Version || retried.FinishedAt == nil ||
		!retried.FinishedAt.Equal(*canceled.FinishedAt) {
		t.Fatalf("canceled cleanup reopened or rewrote request: before=%+v after=%+v", canceled, retried)
	}
	var pendingSet model.BackupAssetDerivedArtifactSet
	if err := db.Where("id = ?", fixture.artifactSet.ID).Take(&pendingSet).Error; err != nil {
		t.Fatal(err)
	}
	var pendingGrant model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", fixture.deliveryGrant.ID).Take(&pendingGrant).Error; err != nil {
		t.Fatal(err)
	}
	if pendingSet.State != "active" || pendingGrant.State != "active" || outputCalls != 2 || deliveryCalls != 2 {
		t.Fatalf("failed canceled cleanup was not durable: set=%+v grant=%+v output_calls=%d delivery_calls=%d",
			pendingSet, pendingGrant, outputCalls, deliveryCalls)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("canceled cleanup success=%d err=%v", reconciled, err)
	}
	if err := db.Where("id = ?", fixture.artifactSet.ID).Take(&pendingSet).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", fixture.deliveryGrant.ID).Take(&pendingGrant).Error; err != nil {
		t.Fatal(err)
	}
	if pendingSet.State != "superseded" || pendingGrant.State != "revoked" || outputCalls != 3 || deliveryCalls != 3 {
		t.Fatalf("canceled cleanup did not clear downstream rows: set=%+v grant=%+v output_calls=%d delivery_calls=%d",
			pendingSet, pendingGrant, outputCalls, deliveryCalls)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 0 {
		t.Fatalf("completed canceled cleanup remained pending: reconciled=%d err=%v", reconciled, err)
	}
}

func TestArchiveMemberMaintenanceRetriesDerivedPhysicalCleanupAfterDurableRevocation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	fixture := archiveMemberMaintenanceCleanupFixture(now, now.Add(time.Hour))
	fixture.request.State = string(ArchiveMemberFailed)
	fixture.request.ErrorCategory = string(ArchiveFailureUnavailable)
	fixture.request.Version = 3
	if err := db.Create(&fixture.request).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.artifactSet).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.deliveryGrant).Error; err != nil {
		t.Fatal(err)
	}

	blobID := strings.Repeat("0", 32)
	if err := db.Create(&model.BackupAssetDerivedArtifact{
		ID: strings.Repeat("f", 32), ArtifactSetID: fixture.artifactSet.ID, Ordinal: 0,
		Role: "content", MediaType: "application/octet-stream", PlaintextSize: 3,
		PlaintextDigest: strings.Repeat("1", 64), Completeness: "complete", CoverageCanonical: []byte(`{}`), BlobID: blobID,
		CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: strings.Repeat("1", 64), PlaintextSize: 3, PhysicalSize: 3,
		CipherFormatVersion: 1, ChunkSize: 3, ChunkCount: 1, NoncePrefix: make([]byte, 12),
		OpaqueLocator: "derived-blob", WrappedDEK: []byte{1}, EnvelopeNonce: make([]byte, 12),
		DerivedKEKVersion: 1, State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	physicalErr := errors.New("Derived physical cleanup failed")
	outputCalls := 0
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: fixture.asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		RevokeDeliveries: func(_ context.Context, requestID, reason string) error {
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).
				Where("member_request_id = ? AND state = ?", requestID, "active").
				Updates(map[string]any{"state": "revoked", "revoke_reason": reason, "updated_at": now}).Error
		},
		RevokeOutput: func(_ context.Context, artifactSetID string, reason DerivedRevokeReason) error {
			outputCalls++
			if artifactSetID != fixture.artifactSet.ID || reason != DerivedRevokePolicyChanged {
				t.Fatalf("Derived revocation set=%q reason=%q", artifactSetID, reason)
			}
			if outputCalls == 1 {
				if err := db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", artifactSetID).
						Updates(map[string]any{"state": "superseded", "revocation_reason": string(reason), "updated_at": now}).Error; err != nil {
						return err
					}
					return tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", blobID).
						Updates(map[string]any{"state": "purge_failed", "updated_at": now}).Error
				}); err != nil {
					return err
				}
				return physicalErr
			}
			if outputCalls == 2 {
				return db.Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state = ?", blobID, "purge_failed").
					Updates(map[string]any{"state": "purged", "updated_at": now}).Error
			}
			t.Fatalf("unexpected Derived cleanup retry count=%d", outputCalls)
			return nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 || !errors.Is(err, physicalErr) {
		t.Fatalf("first Derived cleanup reconciliation=%d err=%v", reconciled, err)
	}
	var terminalized model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", fixture.request.ID).Take(&terminalized).Error; err != nil {
		t.Fatal(err)
	}
	var partialSet model.BackupAssetDerivedArtifactSet
	if err := db.Where("id = ?", fixture.artifactSet.ID).Take(&partialSet).Error; err != nil {
		t.Fatal(err)
	}
	var partialBlob model.BackupAssetDerivedBlob
	if err := db.Where("id = ?", blobID).Take(&partialBlob).Error; err != nil {
		t.Fatal(err)
	}
	if partialSet.State != "superseded" || partialBlob.State != "purge_failed" {
		t.Fatalf("physical failure was not durably represented: set=%+v blob=%+v", partialSet, partialBlob)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 || err != nil {
		t.Fatalf("durable Derived cleanup retry=%d err=%v", reconciled, err)
	}
	var retried model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", fixture.request.ID).Take(&retried).Error; err != nil {
		t.Fatal(err)
	}
	if retried.State != terminalized.State || retried.Version != terminalized.Version || retried.FinishedAt == nil ||
		!retried.FinishedAt.Equal(*terminalized.FinishedAt) || outputCalls != 2 {
		t.Fatalf("Derived cleanup retry rewrote request: before=%+v after=%+v output_calls=%d", terminalized, retried, outputCalls)
	}
	if err := db.Where("id = ?", blobID).Take(&partialBlob).Error; err != nil {
		t.Fatal(err)
	}
	if partialBlob.State != "purged" {
		t.Fatalf("Derived physical cleanup was not repaired: %+v", partialBlob)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 0 || err != nil {
		t.Fatalf("completed Derived cleanup remained pending: reconciled=%d err=%v", reconciled, err)
	}
}

func TestArchiveMemberMaintenanceRetriesRevokedDeliveryAuditCleanup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	fixture := archiveMemberMaintenanceCleanupFixture(now, now.Add(time.Hour))
	fixture.request.State = string(ArchiveMemberFailed)
	fixture.request.ErrorCategory = string(ArchiveFailureUnavailable)
	fixture.request.Version = 3
	fixture.artifactSet.State = "superseded"
	fixture.deliveryGrant.AuditState = "pending"
	if err := db.Create(&fixture.request).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.artifactSet).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.deliveryGrant).Error; err != nil {
		t.Fatal(err)
	}

	auditErr := errors.New("delivery audit cleanup failed")
	deliveryCalls := 0
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: fixture.asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		RevokeDeliveries: func(_ context.Context, requestID, reason string) error {
			deliveryCalls++
			if requestID != fixture.request.ID || reason != "policy_changed" {
				t.Fatalf("delivery revocation request=%q reason=%q", requestID, reason)
			}
			if deliveryCalls == 1 {
				if err := db.Model(&model.BackupAssetExportDeliveryGrant{}).
					Where("member_request_id = ? AND state = ?", requestID, "active").
					Updates(map[string]any{"state": "revoked", "revoke_reason": reason, "audit_state": "retry_wait", "updated_at": now}).Error; err != nil {
					return err
				}
				return auditErr
			}
			if deliveryCalls == 2 {
				return db.Model(&model.BackupAssetExportDeliveryGrant{}).
					Where("member_request_id = ? AND state = ? AND audit_state = ?", requestID, "revoked", "retry_wait").
					Updates(map[string]any{"audit_state": "emitted", "updated_at": now}).Error
			}
			t.Fatalf("unexpected delivery cleanup retry count=%d", deliveryCalls)
			return nil
		},
		RevokeOutput: func(context.Context, string, DerivedRevokeReason) error {
			t.Fatal("delivery-only audit cleanup attempted Derived revocation")
			return nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 || !errors.Is(err, auditErr) {
		t.Fatalf("first delivery cleanup reconciliation=%d err=%v", reconciled, err)
	}
	var revoked model.BackupAssetExportDeliveryGrant
	if err := db.Where("id = ?", fixture.deliveryGrant.ID).Take(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if revoked.State != "revoked" || revoked.AuditState != "retry_wait" {
		t.Fatalf("delivery audit failure was not durably represented: %+v", revoked)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 || err != nil {
		t.Fatalf("revoked delivery audit retry=%d err=%v", reconciled, err)
	}
	if err := db.Where("id = ?", fixture.deliveryGrant.ID).Take(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if revoked.AuditState != "emitted" || deliveryCalls != 2 {
		t.Fatalf("revoked delivery audit was not repaired: grant=%+v calls=%d", revoked, deliveryCalls)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 0 || err != nil {
		t.Fatalf("completed delivery audit cleanup remained pending: reconciled=%d err=%v", reconciled, err)
	}
}

func TestArchiveMemberMaintenanceReportsJoinedAuthorityFailureWithPendingCleanup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	fixture := archiveMemberMaintenanceCleanupFixture(now, now.Add(time.Hour))
	if err := db.Create(&fixture.request).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.artifactSet).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fixture.deliveryGrant).Error; err != nil {
		t.Fatal(err)
	}

	cleanupErr := errors.New("joined authority cleanup failure")
	outputCalls := 0
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{},
		Authorize: archiveMemberAuthorizerFake{asset: fixture.asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return fixture.asset, nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{}, errors.Join(backupasset.ErrForbidden, ErrArchiveMemberUnavailable)
		},
		RevokeDeliveries: func(_ context.Context, requestID, reason string) error {
			return db.Model(&model.BackupAssetExportDeliveryGrant{}).
				Where("member_request_id = ? AND state = ?", requestID, "active").
				Updates(map[string]any{"state": "revoked", "revoke_reason": reason, "updated_at": now}).Error
		},
		RevokeOutput: func(_ context.Context, artifactSetID string, reason DerivedRevokeReason) error {
			outputCalls++
			if outputCalls == 1 {
				return errors.Join(ErrArchiveMemberUnavailable, cleanupErr)
			}
			return db.Model(&model.BackupAssetDerivedArtifactSet{}).
				Where("id = ? AND state = ?", artifactSetID, "active").
				Updates(map[string]any{"state": "superseded", "revocation_reason": string(reason), "updated_at": now}).Error
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	reconciled, reconcileErr := service.ReconcilePending(context.Background(), 1)
	if reconciled != 1 || !errors.Is(reconcileErr, backupasset.ErrForbidden) ||
		!errors.Is(reconcileErr, ErrArchiveMemberUnavailable) || !errors.Is(reconcileErr, cleanupErr) {
		t.Fatalf("joined authority cleanup reconciliation=%d err=%v", reconciled, reconcileErr)
	}
	if !archiveMemberMaintenanceRetryable(reconcileErr) {
		t.Fatalf("joined authority terminal cleanup was not retained as retryable maintenance work: %v", reconcileErr)
	}
	var terminalized model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", fixture.request.ID).Take(&terminalized).Error; err != nil {
		t.Fatal(err)
	}
	if terminalized.State != string(ArchiveMemberFailed) || terminalized.ErrorCategory != string(ArchiveFailureUnavailable) ||
		terminalized.FinishedAt == nil {
		t.Fatalf("joined authority request was not terminalized: %+v", terminalized)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("joined authority cleanup retry=%d err=%v", reconciled, err)
	}
	var repaired model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", fixture.request.ID).Take(&repaired).Error; err != nil {
		t.Fatal(err)
	}
	if repaired.State != terminalized.State || repaired.ErrorCategory != terminalized.ErrorCategory ||
		repaired.Version != terminalized.Version || repaired.FinishedAt == nil ||
		!repaired.FinishedAt.Equal(*terminalized.FinishedAt) || outputCalls != 2 {
		t.Fatalf("joined authority retry reopened terminal request: before=%+v after=%+v output_calls=%d",
			terminalized, repaired, outputCalls)
	}
}

func TestArchiveMemberReadyTerminalizationFansOutRevocationFailuresAndClosesDelivery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	processingJobID := strings.Repeat("6", 32)
	processingInterestID := strings.Repeat("7", 32)
	artifactSetID := strings.Repeat("a", 32)
	finished := now
	ready := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: archiveMemberCreateEndpoint, KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 7,
		ProcessingInterestID: &processingInterestID, ProcessingJobID: &processingJobID,
		State: string(ArchiveMemberReady), AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finished, Version: 2,
	}
	if err := db.Create(&ready).Error; err != nil {
		t.Fatal(err)
	}
	job := archiveMemberProcessingJobFixture(now, processingJobID, "")
	job.State, job.ErrorCode, job.CurrentArtifactSetID = string(ProcessingSucceeded), "", &artifactSetID
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	deliveryErr := errors.New("delivery revoke failed")
	outputErr := errors.New("Derived revoke failed")
	var order []string
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: &archiveMemberCoordinatorFake{}, Authorize: archiveMemberAuthorizerFake{asset: asset},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
			return ArchiveMemberIndexBinding{}, nil
		},
		RevokeDeliveries: func(_ context.Context, gotRequestID, reason string) error {
			order = append(order, "delivery")
			if gotRequestID != requestID || reason != "key_loss" {
				t.Fatalf("delivery revoke request=%q reason=%q", gotRequestID, reason)
			}
			return deliveryErr
		},
		RevokeOutput: func(_ context.Context, gotArtifactSetID string, reason DerivedRevokeReason) error {
			order = append(order, "derived")
			if gotArtifactSetID != artifactSetID || reason != DerivedRevokeKeyLoss {
				t.Fatalf("Derived revoke set=%q reason=%q", gotArtifactSetID, reason)
			}
			return outputErr
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = service.Invalidate(context.Background(), requestID, DerivedRevokeKeyLoss)
	if !errors.Is(err, deliveryErr) || !errors.Is(err, outputErr) {
		t.Fatalf("terminalization error=%v, want both revoke errors", err)
	}
	if strings.Join(order, ",") != "delivery,derived" {
		t.Fatalf("partial revoke order=%v", order)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", requestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.FinishedAt == nil {
		t.Fatalf("partial revoke left request servable: %+v", persisted)
	}
	if _, err := service.AuthorizeReadyDelivery(context.Background(), ArchiveMemberLookup{
		Actor: content.DeliveryActor{UserID: 42, Role: "admin"}, Ref: asset.Ref, RequestID: requestID,
	}); !errors.Is(err, ErrArchiveMemberUnavailable) {
		t.Fatalf("partially revoked request remained deliverable: %v", err)
	}
}

func TestArchiveMemberMaintenanceInvalidatesRunningRequestAfterAuthorizationRevocationWithoutPoll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	revoked := false
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			if revoked {
				return content.AuthorizedAsset{}, backupasset.ErrForbidden
			}
			return archiveMemberAssetFixture(), nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("bind running reconciled=%d err=%v", reconciled, err)
	}
	revoked = true
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("reconcile revoked request reconciled=%d err=%v", reconciled, err)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.ErrorCategory != string(ArchiveFailureUnavailable) || persisted.FinishedAt == nil {
		t.Fatalf("revoked request stayed runnable without Poll: %+v", persisted)
	}
	if len(coordinator.removals) != 1 || coordinator.removals[0].jobID != coordinator.result.JobID ||
		coordinator.removals[0].reason != InterestRemovedSuperseded {
		t.Fatalf("authorization revocation did not remove Processing interest: %+v", coordinator.removals)
	}
}

func TestArchiveMemberMaintenanceKeepsRunningRequestRetryableAfterTransientAuthorityError(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "unavailable", err: ErrArchiveMemberUnavailable},
		{name: "not deployed", err: ErrNotDeployed},
		{name: "revision conflict", err: ErrRevisionConflict},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			db := archiveMemberTestDB(t, now)
			memberID := strings.Repeat("a", 32)
			index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
			coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
				JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
			}}
			transient := false
			service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
				DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
				ResolveIndex: index.Resolve, RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
				ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
					return archiveMemberAssetFixture(), nil
				},
				ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
					if transient {
						return ArchiveMemberProcessingAuthority{}, testCase.err
					}
					return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
				},
				ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
					return CapabilityAdvertisement{
						SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
						PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
					}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
			if err != nil {
				t.Fatal(err)
			}
			if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
				t.Fatalf("bind running reconciled=%d err=%v", reconciled, err)
			}
			job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
			job.State, job.ErrorCode, job.FinishedAt = string(ProcessingProcessing), "", nil
			if err := db.Create(&job).Error; err != nil {
				t.Fatal(err)
			}
			interest := model.BackupAssetProcessingInterest{
				ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
				OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
				PriorityClass: string(PriorityInteractive), Priority: 900, Active: true, CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&interest).Error; err != nil {
				t.Fatal(err)
			}
			transient = true
			if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
				t.Fatalf("transient authority reconciliation=%d err=%v", reconciled, err)
			}
			var persisted model.BackupAssetArchiveMemberRequest
			if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
				t.Fatal(err)
			}
			if persisted.State != string(ArchiveMemberRunning) || persisted.FinishedAt != nil {
				t.Fatalf("transient authority error terminalized request: %+v", persisted)
			}
			if len(coordinator.removals) != 0 {
				t.Fatalf("transient authority error removed interest: %+v", coordinator.removals)
			}
			if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
				t.Fatal(err)
			}
			if !interest.Active || interest.RemovedAt != nil {
				t.Fatalf("transient authority error changed interest: %+v", interest)
			}
		})
	}
}

func TestArchiveMemberMaintenanceTreatsAuthorityInvalidationAsCompletedWithoutPoll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	revoked := false
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return archiveMemberAssetFixture(), nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			if revoked {
				return ArchiveMemberProcessingAuthority{}, backupasset.ErrForbidden
			}
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("bind running reconciled=%d err=%v", reconciled, err)
	}
	job := archiveMemberProcessingJobFixture(now, coordinator.result.JobID, "")
	job.State, job.ErrorCode, job.FinishedAt = string(ProcessingProcessing), "", nil
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	interest := model.BackupAssetProcessingInterest{
		ID: coordinator.result.InterestID, JobID: coordinator.result.JobID,
		OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(created.RequestID),
		PriorityClass: string(PriorityInteractive), Priority: 900, Active: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&interest).Error; err != nil {
		t.Fatal(err)
	}
	coordinator.removeInterest = func(
		ctx context.Context,
		jobID string,
		ownerKind InterestOwnerKind,
		ownerKey string,
		reason InterestRemovedReason,
	) error {
		if jobID != interest.JobID || ownerKind != InterestSystem ||
			ownerKey != archiveMemberInterestOwnerKey(created.RequestID) || reason != InterestRemovedSuperseded {
			t.Fatalf("authority revocation removal job=%q owner=%q reason=%q", jobID, ownerKey, reason)
		}
		return db.WithContext(ctx).Model(&model.BackupAssetProcessingInterest{}).
			Where("id = ? AND job_id = ? AND active = ?", interest.ID, interest.JobID, true).
			Updates(map[string]any{"active": false, "removed_reason": string(reason), "removed_at": now, "updated_at": now}).Error
	}
	revoked = true
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("reconcile revoked authority reconciled=%d err=%v", reconciled, err)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.ErrorCategory != string(ArchiveFailureUnavailable) || persisted.FinishedAt == nil {
		t.Fatalf("authority-revoked request=%+v", persisted)
	}
	if len(coordinator.removals) != 1 || coordinator.removals[0].jobID != coordinator.result.JobID ||
		coordinator.removals[0].reason != InterestRemovedSuperseded {
		t.Fatalf("authority revocation did not remove Processing interest: %+v", coordinator.removals)
	}
	if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
		t.Fatal(err)
	}
	if interest.Active || interest.RemovedReason != string(InterestRemovedSuperseded) || interest.RemovedAt == nil {
		t.Fatalf("authority revocation did not persist Processing interest removal: %+v", interest)
	}
}

func TestArchiveMemberMaintenanceInvalidatesRunningRequestAfterSourceDriftWithoutPoll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	asset := archiveMemberAssetFixture()
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return asset, nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("bind running reconciled=%d err=%v", reconciled, err)
	}
	asset.EntryFingerprint = "entry-fingerprint-v2"
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("reconcile drifted request reconciled=%d err=%v", reconciled, err)
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(ArchiveMemberFailed) || persisted.ErrorCategory != string(ArchiveFailureUnavailable) || persisted.FinishedAt == nil {
		t.Fatalf("drifted request stayed runnable without Poll: %+v", persisted)
	}
	if len(coordinator.removals) != 1 || coordinator.removals[0].jobID != coordinator.result.JobID ||
		coordinator.removals[0].reason != InterestRemovedSuperseded {
		t.Fatalf("source drift did not remove Processing interest: %+v", coordinator.removals)
	}
}

func TestArchiveMemberMaintenanceLeavesQueuedRequestRetryableWhenProcessingIsUnavailable(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{result: WorkResult{
		JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
	}}
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, RevalidateIndex: archiveMemberIndexRevalidator(index), Now: func() time.Time { return now },
		ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
			return archiveMemberAssetFixture(), nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{}, ErrNotDeployed
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := service.ReconcilePending(context.Background(), 1); err != nil || reconciled != 1 {
		t.Fatalf("unavailable processing reconciled=%d err=%v", reconciled, err)
	}
	var queued model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if queued.State != string(ArchiveMemberQueued) || queued.ProcessingJobID != nil || queued.ProcessingInterestID != nil || coordinator.calls != 0 {
		t.Fatalf("processing unavailable was not kept retryable request=%+v calls=%d", queued, coordinator.calls)
	}
}

func TestArchiveMemberMaintenanceLeavesQueuedRequestRetryableWhenQueueIsFull(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	db := archiveMemberTestDB(t, now)
	memberID := strings.Repeat("a", 32)
	index := &archiveMemberIndexFake{binding: archiveMemberIndexFixture(now, memberID)}
	coordinator := &archiveMemberCoordinatorFake{requestErr: ErrQueueFull}
	service := newArchiveMemberReconcileService(t, db, now, index, coordinator)
	created, err := service.Create(context.Background(), archiveMemberCreateFixture(memberID))
	if err != nil {
		t.Fatal(err)
	}

	if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 || err != nil {
		t.Fatalf("queue-full maintenance reconciliation=%d err=%v", reconciled, err)
	}
	var queued model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", created.RequestID).Take(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if queued.State != string(ArchiveMemberQueued) || queued.ProcessingJobID != nil || queued.ProcessingInterestID != nil ||
		coordinator.calls != 1 {
		t.Fatalf("queue-full maintenance did not preserve queued work: request=%+v calls=%d", queued, coordinator.calls)
	}
	if !archiveMemberMaintenanceRetryable(ErrQueueFull) {
		t.Fatal("queue-full work was not classified retryable")
	}
}

func TestArchiveMemberMaintenanceRetriesActiveTerminalInterestRemoval(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		initialState  ArchiveMemberState
		expiresAt     func(time.Time) time.Time
		expectedState ArchiveMemberState
		reason        InterestRemovedReason
	}{
		{
			name: "authority_invalidation", initialState: ArchiveMemberRunning,
			expiresAt:     func(now time.Time) time.Time { return now.Add(time.Hour) },
			expectedState: ArchiveMemberFailed, reason: InterestRemovedSuperseded,
		},
		{
			name: "expiry", initialState: ArchiveMemberRunning,
			expiresAt:     func(now time.Time) time.Time { return now },
			expectedState: ArchiveMemberExpired, reason: InterestRemovedExpired,
		},
		{
			name: "canceled", initialState: ArchiveMemberCanceled,
			expiresAt:     func(now time.Time) time.Time { return now.Add(time.Hour) },
			expectedState: ArchiveMemberCanceled, reason: InterestRemovedCanceled,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			db := archiveMemberTestDB(t, now)
			fixture := archiveMemberMaintenanceCleanupFixture(now, testCase.expiresAt(now))
			fixture.request.State = string(testCase.initialState)
			fixture.request.Version = 2
			fixture.artifactSet.State = "superseded"
			fixture.deliveryGrant.State = "revoked"
			fixture.deliveryGrant.AuditState = "emitted"
			if testCase.initialState == ArchiveMemberRunning {
				fixture.request.FinishedAt = nil
				fixture.job.State, fixture.job.FinishedAt = string(ProcessingProcessing), nil
			} else {
				fixture.request.ErrorCategory = ""
			}
			if err := db.Create(&fixture.request).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&fixture.job).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&fixture.artifactSet).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&fixture.deliveryGrant).Error; err != nil {
				t.Fatal(err)
			}
			interest := model.BackupAssetProcessingInterest{
				ID: *fixture.request.ProcessingInterestID, JobID: *fixture.request.ProcessingJobID,
				OwnerKind: string(InterestSystem), OwnerKey: archiveMemberInterestOwnerKey(fixture.request.ID),
				PriorityClass: string(PriorityInteractive), Priority: 900, Active: true, CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&interest).Error; err != nil {
				t.Fatal(err)
			}

			removalErr := errors.New("remove Processing interest temporarily failed")
			removalCalls := 0
			coordinator := &archiveMemberCoordinatorFake{}
			coordinator.removeInterest = func(
				ctx context.Context,
				jobID string,
				ownerKind InterestOwnerKind,
				ownerKey string,
				reason InterestRemovedReason,
			) error {
				removalCalls++
				if jobID != *fixture.request.ProcessingJobID || ownerKind != InterestSystem ||
					ownerKey != archiveMemberInterestOwnerKey(fixture.request.ID) || reason != testCase.reason {
					t.Fatalf("terminal interest removal call=%d job=%q owner=%q reason=%q", removalCalls, jobID, ownerKey, reason)
				}
				if removalCalls == 1 {
					return removalErr
				}
				return db.WithContext(ctx).Model(&model.BackupAssetProcessingInterest{}).
					Where("id = ? AND job_id = ? AND active = ?", interest.ID, interest.JobID, true).
					Updates(map[string]any{"active": false, "removed_reason": string(reason), "removed_at": now, "updated_at": now}).Error
			}
			service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
				DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: fixture.asset},
				ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
					return ArchiveMemberIndexBinding{}, nil
				},
				ResolveRuntimeAsset: func(context.Context, model.BackupAssetArchiveMemberRequest) (content.AuthorizedAsset, error) {
					if testCase.initialState != ArchiveMemberRunning || testCase.expectedState != ArchiveMemberFailed {
						t.Fatal("expiry or terminal-interest maintenance resolved runtime asset")
					}
					return content.AuthorizedAsset{}, backupasset.ErrForbidden
				},
				Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}

			if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 || !errors.Is(err, removalErr) {
				t.Fatalf("first terminal interest maintenance=%d err=%v", reconciled, err)
			}
			var terminalized model.BackupAssetArchiveMemberRequest
			if err := db.Where("id = ?", fixture.request.ID).Take(&terminalized).Error; err != nil {
				t.Fatal(err)
			}
			if terminalized.State != string(testCase.expectedState) || terminalized.FinishedAt == nil {
				t.Fatalf("terminal interest failure did not close request: %+v", terminalized)
			}
			if testCase.expectedState == ArchiveMemberFailed && terminalized.ErrorCategory != string(ArchiveFailureUnavailable) {
				t.Fatalf("authority invalidation category=%q", terminalized.ErrorCategory)
			}
			if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
				t.Fatal(err)
			}
			if !interest.Active || interest.RemovedAt != nil {
				t.Fatalf("failed removal changed interest: %+v", interest)
			}

			if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 1 || err != nil {
				t.Fatalf("terminal interest retry=%d err=%v", reconciled, err)
			}
			var repaired model.BackupAssetArchiveMemberRequest
			if err := db.Where("id = ?", fixture.request.ID).Take(&repaired).Error; err != nil {
				t.Fatal(err)
			}
			if repaired.State != terminalized.State || repaired.Version != terminalized.Version || repaired.FinishedAt == nil ||
				!repaired.FinishedAt.Equal(*terminalized.FinishedAt) || removalCalls != 2 {
				t.Fatalf("terminal interest retry rewrote request: before=%+v after=%+v calls=%d", terminalized, repaired, removalCalls)
			}
			if err := db.Where("id = ?", interest.ID).Take(&interest).Error; err != nil {
				t.Fatal(err)
			}
			if interest.Active || interest.RemovedReason != string(testCase.reason) || interest.RemovedAt == nil {
				t.Fatalf("terminal interest was not removed with the closed reason: %+v", interest)
			}
			if reconciled, err := service.ReconcilePending(context.Background(), 1); reconciled != 0 || err != nil {
				t.Fatalf("completed terminal interest cleanup remained pending: reconciled=%d err=%v", reconciled, err)
			}
		})
	}
}

type archiveMemberCoordinatorFake struct {
	calls          int
	result         WorkResult
	requestErr     error
	requests       []WorkRequest
	removals       []archiveMemberInterestRemoval
	removeInterest func(context.Context, string, InterestOwnerKind, string, InterestRemovedReason) error
}

type archiveMemberInterestRemoval struct {
	jobID     string
	ownerKind InterestOwnerKind
	ownerKey  string
	reason    InterestRemovedReason
}

func (fake *archiveMemberCoordinatorFake) RequestWork(_ context.Context, request WorkRequest) (WorkResult, error) {
	fake.calls++
	fake.requests = append(fake.requests, request)
	if fake.requestErr != nil {
		return WorkResult{}, fake.requestErr
	}
	return fake.result, nil
}

func (fake *archiveMemberCoordinatorFake) RemoveInterest(
	ctx context.Context,
	jobID string,
	ownerKind InterestOwnerKind,
	ownerKey string,
	reason InterestRemovedReason,
) error {
	fake.removals = append(fake.removals, archiveMemberInterestRemoval{
		jobID: jobID, ownerKind: ownerKind, ownerKey: ownerKey, reason: reason,
	})
	if fake.removeInterest != nil {
		return fake.removeInterest(ctx, jobID, ownerKind, ownerKey, reason)
	}
	return nil
}

type archiveMemberAuthorizerFake struct {
	asset content.AuthorizedAsset
}

func (fake archiveMemberAuthorizerFake) Authorize(context.Context, content.DeliveryActor, backupasset.AssetRef, content.DeliveryAction) (content.AuthorizedAsset, error) {
	return fake.asset, nil
}

type archiveMemberActionAuthorizerFake struct {
	asset       content.AuthorizedAsset
	downloadErr error
	previewErr  error
	actions     []content.DeliveryAction
}

func (fake *archiveMemberActionAuthorizerFake) Authorize(
	_ context.Context,
	_ content.DeliveryActor,
	_ backupasset.AssetRef,
	action content.DeliveryAction,
) (content.AuthorizedAsset, error) {
	fake.actions = append(fake.actions, action)
	if action == content.DeliveryPreview && fake.previewErr != nil {
		return content.AuthorizedAsset{}, fake.previewErr
	}
	if action == content.DeliveryDownload && fake.downloadErr != nil {
		return content.AuthorizedAsset{}, fake.downloadErr
	}
	return fake.asset, nil
}

type archiveMemberIndexFake struct {
	binding ArchiveMemberIndexBinding
	calls   int
}

func (fake *archiveMemberIndexFake) Resolve(context.Context, content.AuthorizedAsset, string) (ArchiveMemberIndexBinding, error) {
	fake.calls++
	return fake.binding, nil
}

func archiveMemberIndexRevalidator(index *archiveMemberIndexFake) ArchiveMemberIndexRevalidator {
	return func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberIndexBinding, error) {
		return index.binding, nil
	}
}

func archiveMemberTestDB(t *testing.T, now time.Time) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:%s-%d?mode=memory&cache=shared&_busy_timeout=5000&_foreign_keys=ON&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"),
		archiveMemberTestDBSequence.Add(1),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		NowFunc:                                  func() time.Time { return now },
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.BackupAssetArchiveMemberRequest{}, &model.BackupAssetExportQuotaBucket{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedArtifact{}, &model.BackupAssetDerivedBlob{},
		&model.BackupAssetExportDeliveryGrant{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_archive_member_test_idempotency_slot
		ON backup_asset_archive_member_requests(owner_user_id, endpoint, key_digest)`).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newArchiveMemberReconcileService(
	t *testing.T,
	db *gorm.DB,
	now time.Time,
	index *archiveMemberIndexFake,
	coordinator *archiveMemberCoordinatorFake,
) *ArchiveMemberService {
	t.Helper()
	service, err := NewArchiveMemberService(ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: archiveMemberAuthorizerFake{asset: archiveMemberAssetFixture()},
		ResolveIndex: index.Resolve, Now: func() time.Time { return now },
		RevalidateIndex: archiveMemberIndexRevalidator(index),
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (ArchiveMemberProcessingAuthority, error) {
			return ArchiveMemberProcessingAuthority{
				ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1",
			}, nil
		},
		ResolveExtractCapability: func(context.Context) (CapabilityAdvertisement, error) {
			return CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type archiveMemberMaintenanceCleanupRows struct {
	asset         content.AuthorizedAsset
	request       model.BackupAssetArchiveMemberRequest
	job           model.BackupAssetProcessingJob
	artifactSet   model.BackupAssetDerivedArtifactSet
	deliveryGrant model.BackupAssetExportDeliveryGrant
}

func archiveMemberMaintenanceCleanupFixture(
	now time.Time,
	expiresAt time.Time,
) archiveMemberMaintenanceCleanupRows {
	asset := archiveMemberAssetFixture()
	requestID := strings.Repeat("1", 32)
	jobID := strings.Repeat("6", 32)
	interestID := strings.Repeat("7", 32)
	artifactSetID := strings.Repeat("a", 32)
	attemptID := strings.Repeat("b", 32)
	derivedArtifactID := strings.Repeat("f", 32)
	derivedBlobID := strings.Repeat("0", 32)
	finished := now
	request := model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 42, Endpoint: archiveMemberCreateEndpoint, KeyDigest: strings.Repeat("2", 64),
		RequestIntentDigest: strings.Repeat("3", 64), RecoveryPointID: asset.Ref.RecoveryPointID,
		EntryID: asset.Ref.EntryID, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		IndexArtifactID: strings.Repeat("4", 32), IndexRevision: strings.Repeat("5", 64),
		MemberChainDigest: strings.Repeat("8", 64), ResolvedOrdinal: 7,
		ProcessingInterestID: &interestID, ProcessingJobID: &jobID,
		State: string(ArchiveMemberReady), IdempotencyExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: expiresAt,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, FinishedAt: &finished, Version: 2,
	}
	job := archiveMemberProcessingJobFixture(now, jobID, "")
	job.State, job.ErrorCode, job.CurrentArtifactSetID = string(ProcessingSucceeded), "", &artifactSetID
	artifactSet := model.BackupAssetDerivedArtifactSet{
		ID: artifactSetID, JobID: jobID, AttemptID: attemptID, WorkKey: job.WorkKey,
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: asset.Ref.EntryID, SourceFingerprint: asset.SourceFingerprint,
		SecurityPolicyRevision: "security-policy-v1", ManifestDigest: strings.Repeat("c", 64),
		State: "active", Completeness: "complete", ArtifactCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	deliveryID := strings.Repeat("d", 32)
	deliveryGrant := model.BackupAssetExportDeliveryGrant{
		ID: deliveryID, DeliveryID: strings.Repeat("e", 32), ResourceKind: "archive_member",
		MemberRequestID: &requestID, OuterRecoveryPointID: asset.Ref.RecoveryPointID, OuterEntryID: asset.Ref.EntryID,
		OuterSourceFingerprint: asset.SourceFingerprint, OuterEntryFingerprint: asset.EntryFingerprint,
		MemberChainDigest: request.MemberChainDigest, ProcessingJobID: &jobID, ProcessingAttemptID: &attemptID,
		DerivedArtifactSetID: &artifactSetID, DerivedArtifactID: &derivedArtifactID,
		DerivedBlobID: &derivedBlobID, DerivedDigest: strings.Repeat("1", 64), DerivedSize: 3,
		OwnerUserID: request.OwnerUserID, SessionJTI: strings.Repeat("2", 32), TokenVersion: 1, RoleRevision: 1,
		ProofAction: "asset.archive_member_download", ProofID: strings.Repeat("3", 32), ProofExpiresAt: now.Add(time.Hour),
		CookieSecretHash: strings.Repeat("4", 64), Action: "archive_member_download",
		CanonicalPath: "/api/v1/asset-content/" + deliveryID, MethodPolicy: "get_head", RangePolicy: "none",
		State: "active", IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(time.Hour),
		MaxRequests: 1, MaxCumulativeBytes: 3, MaxInFlight: 1, IssuedAt: now, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	return archiveMemberMaintenanceCleanupRows{
		asset: asset, request: request, job: job, artifactSet: artifactSet, deliveryGrant: deliveryGrant,
	}
}

func archiveMemberProcessingJobFixture(now time.Time, jobID string, code ProcessingErrorCode) model.BackupAssetProcessingJob {
	finished := now
	return model.BackupAssetProcessingJob{
		ID: jobID, WorkKey: strings.Repeat("8", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{"schema_version":1,"parameters":{"member_start":7,"member_end":7}}`),
		RecoveryPointID:     strings.Repeat("1", 32), CatalogGenerationID: strings.Repeat("4", 32),
		EntryID: strings.Repeat("2", 64), SourceFingerprint: "source-fingerprint-v1",
		EntryFingerprint: "entry-fingerprint-v1", ProviderCapabilityRevision: 9,
		Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
		PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
		SecurityPolicyRevision: "security-policy-v1", PriorityClass: string(PriorityInteractive), EffectivePriority: 900,
		State: string(ProcessingFailed), TransitionRevision: 2, ErrorCode: string(code), IsCurrent: true,
		QueuedAt: now, FinishedAt: &finished, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, Version: 1,
	}
}

func archiveMemberCreateFixture(memberID string) ArchiveMemberCreateRequest {
	return ArchiveMemberCreateRequest{
		Actor:          content.DeliveryActor{UserID: 42, Username: "admin", Role: "admin"},
		Ref:            backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		IdempotencyKey: "archive-member-idempotency-key",
		IndexRevision:  strings.Repeat("3", 64), MemberChain: []string{memberID},
	}
}

func archiveMemberAssetFixture() content.AuthorizedAsset {
	return content.AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		CatalogGenerationID: strings.Repeat("4", 32), Provider: backupasset.ProviderRestic,
		ProviderCapabilityRevision: 9, SourceFingerprint: "source-fingerprint-v1",
		EntryFingerprint: "entry-fingerprint-v1", FingerprintStrength: "strong",
		Size: 1024, MediaType: "application/zip",
	}
}

func archiveMemberIndexFixture(now time.Time, memberID string) ArchiveMemberIndexBinding {
	return ArchiveMemberIndexBinding{
		ArtifactID: strings.Repeat("5", 32), Revision: strings.Repeat("3", 64),
		PipelineFingerprint: "archive-inspect-pipeline-v1", SecurityPolicyRevision: "security-policy-v1",
		AbsoluteExpiresAt: now.Add(time.Hour),
		Members:           []ArchiveMemberIndexEntry{{OpaqueID: memberID, Ordinal: 7, DisplayName: "member.txt", Size: 3, MediaType: "text/plain"}},
	}
}

func archiveMemberOutputFixture(
	now time.Time,
	memberID string,
	processingJobID string,
	request content.ArchiveMemberArtifactRequest,
) content.ResolvedArchiveMemberArtifact {
	asset := archiveMemberAssetFixture()
	return content.ResolvedArchiveMemberArtifact{
		MemberRequestID: request.RequestID, OwnerUserID: request.OwnerUserID, Ref: asset.Ref,
		CatalogGenerationID: asset.CatalogGenerationID, SourceFingerprint: asset.SourceFingerprint,
		EntryFingerprint:  asset.EntryFingerprint,
		MemberChainDigest: content.ArchiveMemberChainDigest(asset.Ref, strings.Repeat("3", 64), memberID),
		ProcessingJobID:   processingJobID, ProcessingAttemptID: strings.Repeat("9", 32),
		DerivedArtifactSetID: strings.Repeat("b", 32), DerivedArtifactID: strings.Repeat("c", 32),
		DerivedBlobID: strings.Repeat("d", 32), DerivedDigest: strings.Repeat("e", 64),
		DerivedSize: 3, MediaType: "text/plain", AbsoluteExpiresAt: now.Add(time.Hour),
		Provider: asset.Provider, ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		FingerprintStrength: asset.FingerprintStrength, SourceSize: asset.Size,
		SourceMediaType: asset.MediaType, SecurityPolicyRevision: "security-policy-v1",
	}
}
