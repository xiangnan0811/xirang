package content

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
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

func TestBudgetScopeOrderIsGlobalProviderUser(t *testing.T) {
	grant := model.BackupAssetDeliveryGrant{OwnerUserID: 42, ProviderKind: string(backupasset.ProviderRsync)}
	want := []BudgetScopeKey{
		{Kind: BudgetScopeGlobal, ID: "global"},
		{Kind: BudgetScopeProvider, ID: "rsync"},
		{Kind: BudgetScopeUser, ID: "42"},
	}
	if got := orderedBudgetScopeKeys(grant); !reflect.DeepEqual(got, want) {
		t.Fatalf("scope lock order=%+v want=%+v", got, want)
	}
}

func TestBudgetReservationAtomicallyUpdatesGrantScopesAndRequest(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	startIdle := harness.grant.IdleExpiresAt
	rangeStart, rangeEnd := int64(10), int64(50)
	reservation, err := harness.service.Reserve(context.Background(), ReservationIntent{
		RequestID: strings.Repeat("9", 32), GrantID: harness.grant.ID, Method: "GET",
		Range:         HTTPRange{Kind: HTTPRangeNormal, Start: &rangeStart, EndExclusive: &rangeEnd, Offset: 10, Length: 40},
		ReservedBytes: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.RequestVersion != 1 || reservation.ReservedBytes != 40 {
		t.Fatalf("reservation=%+v", reservation)
	}

	grant := harness.loadGrant(t)
	if grant.RequestCount != 1 || grant.ReservedBytes != 40 || grant.InFlight != 1 ||
		!grant.IdleExpiresAt.Equal(harness.clock.Now().Add(time.Minute)) || grant.IdleExpiresAt.Equal(startIdle) {
		t.Fatalf("grant counters=%+v", grant)
	}
	for _, key := range orderedBudgetScopeKeys(grant) {
		usage := harness.loadUsage(t, key)
		if usage.RequestCount != 1 || usage.ReservedBytes != 40 || usage.DeliveredBytes != 0 || usage.InFlight != 1 {
			t.Fatalf("scope %+v usage=%+v", key, usage)
		}
	}
	var request model.BackupAssetDeliveryRequest
	if err := harness.db.First(&request, "id = ?", reservation.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	if request.State != string(RequestReserved) || request.RangeKind != string(HTTPRangeNormal) ||
		request.RangeStart == nil || *request.RangeStart != 10 || request.RangeEndExclusive == nil ||
		*request.RangeEndExclusive != 50 || request.SuffixLength != nil {
		t.Fatalf("request row=%+v", request)
	}
}

func TestBudgetReservationRejectsEveryGrantAndScopeLimitWithoutPartialCounters(t *testing.T) {
	tests := []struct {
		name         string
		grantEdit    func(*model.BackupAssetDeliveryGrant)
		limitsEdit   func(*BudgetLimits)
		usage        *model.BackupAssetDeliveryUsage
		reserveBytes int64
	}{
		{name: "per request", grantEdit: func(v *model.BackupAssetDeliveryGrant) { v.MaxBytesPerRequest = 9 }, reserveBytes: 10},
		{name: "grant cumulative", grantEdit: func(v *model.BackupAssetDeliveryGrant) { v.DeliveredBytes = 95 }, reserveBytes: 10},
		{name: "grant requests", grantEdit: func(v *model.BackupAssetDeliveryGrant) { v.RequestCount = v.MaxRequests }, reserveBytes: 1},
		{name: "grant in flight", grantEdit: func(v *model.BackupAssetDeliveryGrant) { v.InFlight = v.MaxInFlight }, reserveBytes: 1},
		{name: "global bytes", limitsEdit: func(v *BudgetLimits) { v.Global.WindowBytes = 9 }, reserveBytes: 10},
		{name: "provider bytes", limitsEdit: func(v *BudgetLimits) { v.Provider.WindowBytes = 9 }, reserveBytes: 10},
		{name: "user bytes", limitsEdit: func(v *BudgetLimits) { v.User.WindowBytes = 9 }, reserveBytes: 10},
		{name: "global requests", limitsEdit: func(v *BudgetLimits) { v.Global.WindowRequests = 1 }, usage: testBudgetUsage(BudgetScopeGlobal, "global", 1, 0, 0), reserveBytes: 1},
		{name: "provider requests", limitsEdit: func(v *BudgetLimits) { v.Provider.WindowRequests = 1 }, usage: testBudgetUsage(BudgetScopeProvider, "rsync", 1, 0, 0), reserveBytes: 1},
		{name: "user requests", limitsEdit: func(v *BudgetLimits) { v.User.WindowRequests = 1 }, usage: testBudgetUsage(BudgetScopeUser, "42", 1, 0, 0), reserveBytes: 1},
		{name: "global in flight", limitsEdit: func(v *BudgetLimits) { v.Global.MaxInFlight = 1 }, usage: testBudgetUsage(BudgetScopeGlobal, "global", 0, 0, 1), reserveBytes: 1},
		{name: "provider in flight", limitsEdit: func(v *BudgetLimits) { v.Provider.MaxInFlight = 1 }, usage: testBudgetUsage(BudgetScopeProvider, "rsync", 0, 0, 1), reserveBytes: 1},
		{name: "user in flight", limitsEdit: func(v *BudgetLimits) { v.User.MaxInFlight = 1 }, usage: testBudgetUsage(BudgetScopeUser, "42", 0, 0, 1), reserveBytes: 1},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newBudgetTestHarness(t, testCase.grantEdit, testCase.limitsEdit)
			if testCase.usage != nil {
				usage := *testCase.usage
				usage.WindowStartedAt = harness.clock.Now()
				usage.WindowExpiresAt = harness.clock.Now().Add(time.Minute)
				usage.UpdatedAt = harness.clock.Now()
				if err := harness.db.Create(&usage).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := harness.loadGrant(t)
			_, err := harness.service.Reserve(context.Background(), ReservationIntent{
				RequestID: strings.Repeat("9", 32), GrantID: harness.grant.ID, Method: "GET",
				Range: HTTPRange{Kind: HTTPRangeFull, Length: testCase.reserveBytes}, ReservedBytes: testCase.reserveBytes,
			})
			if !errors.Is(err, ErrBudgetExhausted) {
				t.Fatalf("reserve error=%v", err)
			}
			after := harness.loadGrant(t)
			if before.RequestCount != after.RequestCount || before.ReservedBytes != after.ReservedBytes || before.InFlight != after.InFlight {
				t.Fatalf("failed reservation changed grant before=%+v after=%+v", before, after)
			}
			var count int64
			if err := harness.db.Model(&model.BackupAssetDeliveryRequest{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("failed reservation request count=%d err=%v", count, err)
			}
		})
	}
}

func TestBudgetBlockedInvalidRangeConsumesRequestWindowsWithoutIdleRefresh(t *testing.T) {
	harness := newBudgetTestHarness(t, func(grant *model.BackupAssetDeliveryGrant) {
		grant.MaxRequests = 2
		grant.IdleExpiresAt = grant.LastActivityAt.Add(30 * time.Second)
	}, func(limits *BudgetLimits) {
		limits.Global.WindowRequests = 2
		limits.Provider.WindowRequests = 2
		limits.User.WindowRequests = 2
	})
	idle := harness.grant.IdleExpiresAt
	for index := 0; index < 2; index++ {
		requestID := fmt.Sprintf("%032x", 100+index)
		if err := harness.service.RecordBlocked(context.Background(), BlockedRequest{
			RequestID: requestID, GrantID: harness.grant.ID, Method: "GET",
			Status: 416, FailureCode: RequestFailureInvalidRange,
		}); err != nil {
			t.Fatalf("blocked request %d: %v", index, err)
		}
	}
	if err := harness.service.RecordBlocked(context.Background(), BlockedRequest{
		RequestID: fmt.Sprintf("%032x", 102), GrantID: harness.grant.ID, Method: "GET",
		Status: 416, FailureCode: RequestFailureInvalidRange,
	}); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("third blocked request error=%v", err)
	}
	grant := harness.loadGrant(t)
	if grant.RequestCount != 2 || grant.ReservedBytes != 0 || grant.InFlight != 0 || !grant.IdleExpiresAt.Equal(idle) {
		t.Fatalf("blocked range grant=%+v", grant)
	}
	for _, key := range orderedBudgetScopeKeys(grant) {
		usage := harness.loadUsage(t, key)
		if usage.RequestCount != 2 || usage.ReservedBytes != 0 || usage.InFlight != 0 {
			t.Fatalf("blocked range scope %+v=%+v", key, usage)
		}
	}
	var requests []model.BackupAssetDeliveryRequest
	if err := harness.db.Order("id").Find(&requests).Error; err != nil || len(requests) != 2 {
		t.Fatalf("blocked requests=%+v err=%v", requests, err)
	}
	for _, request := range requests {
		if request.State != string(RequestBlocked) || request.RangeKind != string(HTTPRangeFull) ||
			request.ReservedBytes != 0 || request.HTTPStatus != 416 || request.FailureCode != string(RequestFailureInvalidRange) ||
			request.FinishedAt == nil {
			t.Fatalf("blocked request=%+v", request)
		}
	}
}

func TestBudgetFinalizeChargesActualBytesOnceAndRejectsStaleVersion(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	reservation := harness.reserve(t, strings.Repeat("9", 32), 40)
	if _, err := harness.service.Finalize(context.Background(), FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion + 1,
		State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 30, ResponseBytes: 25, EvidenceKnown: true,
	}); !errors.Is(err, ErrReservationStale) {
		t.Fatalf("stale finalization error=%v", err)
	}

	result, err := harness.service.Finalize(context.Background(), FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
		State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 30, ResponseBytes: 25, EvidenceKnown: true,
	})
	if err != nil || result.ChargedBytes != 30 || result.AlreadyFinalized {
		t.Fatalf("finalization=%+v err=%v", result, err)
	}
	duplicate, err := harness.service.Finalize(context.Background(), FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
		State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 30, ResponseBytes: 25, EvidenceKnown: true,
	})
	if err != nil || !duplicate.AlreadyFinalized || duplicate.ChargedBytes != 30 {
		t.Fatalf("duplicate finalization=%+v err=%v", duplicate, err)
	}

	grant := harness.loadGrant(t)
	if grant.ReservedBytes != 0 || grant.InFlight != 0 || grant.DeliveredBytes != 30 {
		t.Fatalf("finalized grant=%+v", grant)
	}
	for _, key := range orderedBudgetScopeKeys(grant) {
		usage := harness.loadUsage(t, key)
		if usage.ReservedBytes != 0 || usage.InFlight != 0 || usage.DeliveredBytes != 30 {
			t.Fatalf("finalized scope %+v=%+v", key, usage)
		}
	}
}

func TestBudgetCancelWriteFailureAndCrashChargeConservatively(t *testing.T) {
	tests := []struct {
		name          string
		state         RequestState
		failure       RequestFailureCode
		providerBytes int64
		responseBytes int64
		evidenceKnown bool
		wantCharge    int64
	}{
		{name: "cancel actual", state: RequestCanceled, failure: RequestFailureClientCanceled, providerBytes: 12, responseBytes: 8, evidenceKnown: true, wantCharge: 12},
		{name: "write failure actual", state: RequestFailed, failure: RequestFailureWriteFailed, providerBytes: 7, responseBytes: 9, evidenceKnown: true, wantCharge: 9},
		{name: "unknown meter", state: RequestFailed, failure: RequestFailureSourceFailed, providerBytes: -1, responseBytes: 9, evidenceKnown: false, wantCharge: 40},
		{name: "provider exceeds reservation", state: RequestFailed, failure: RequestFailureSourceFailed, providerBytes: 41, responseBytes: 9, evidenceKnown: true, wantCharge: 40},
		{name: "crash reconciliation", state: RequestReconciled, failure: RequestFailureReconciledCrash, evidenceKnown: false, wantCharge: 40},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newBudgetTestHarness(t, nil, nil)
			reservation := harness.reserve(t, strings.Repeat("9", 32), 40)
			result, err := harness.service.Finalize(context.Background(), FinalizeIntent{
				RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
				State: testCase.state, HTTPStatus: 500, FailureCode: testCase.failure,
				ProviderBytes: testCase.providerBytes, ResponseBytes: testCase.responseBytes, EvidenceKnown: testCase.evidenceKnown,
			})
			if err != nil || result.ChargedBytes != testCase.wantCharge {
				t.Fatalf("finalize=%+v err=%v", result, err)
			}
			grant := harness.loadGrant(t)
			if grant.DeliveredBytes != testCase.wantCharge || grant.ReservedBytes != 0 || grant.InFlight != 0 {
				t.Fatalf("grant=%+v", grant)
			}
		})
	}
}

func TestBudgetReplayDoesNotReserveTwice(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	requestID := strings.Repeat("9", 32)
	harness.reserve(t, requestID, 20)
	if _, err := harness.service.Reserve(context.Background(), ReservationIntent{
		RequestID: requestID, GrantID: harness.grant.ID, Method: "GET",
		Range: HTTPRange{Kind: HTTPRangeFull, Length: 20}, ReservedBytes: 20,
	}); !errors.Is(err, ErrReservationReplay) {
		t.Fatalf("replay error=%v", err)
	}
	grant := harness.loadGrant(t)
	if grant.RequestCount != 1 || grant.ReservedBytes != 20 || grant.InFlight != 1 {
		t.Fatalf("replay counters=%+v", grant)
	}
}

func TestBudgetIdleRefreshNeverExceedsAbsoluteTTLAndExpiredGrantFailsClosed(t *testing.T) {
	harness := newBudgetTestHarness(t, func(grant *model.BackupAssetDeliveryGrant) {
		grant.IdleTTLSeconds = 20
		grant.IdleExpiresAt = grant.LastActivityAt.Add(10 * time.Second)
		grant.AbsoluteExpiresAt = grant.LastActivityAt.Add(30 * time.Second)
		grant.SessionExpiresAt = grant.LastActivityAt.Add(time.Hour)
		grant.MaxInFlight = 3
	}, nil)
	first := harness.reserve(t, strings.Repeat("9", 32), 1)
	if got := harness.loadGrant(t).IdleExpiresAt; !got.Equal(harness.clock.Now().Add(20 * time.Second)) {
		t.Fatalf("first idle refresh=%s", got)
	}
	if _, err := harness.service.Finalize(context.Background(), FinalizeIntent{
		RequestID: first.RequestID, ExpectedRequestVersion: first.RequestVersion,
		State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 1, ResponseBytes: 1, EvidenceKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(15 * time.Second)
	second := harness.reserve(t, strings.Repeat("a", 32), 1)
	if got := harness.loadGrant(t).IdleExpiresAt; !got.Equal(harness.grant.AbsoluteExpiresAt) {
		t.Fatalf("idle refresh exceeded absolute expiry: %s", got)
	}
	if _, err := harness.service.Finalize(context.Background(), FinalizeIntent{
		RequestID: second.RequestID, ExpectedRequestVersion: second.RequestVersion,
		State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 1, ResponseBytes: 1, EvidenceKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	harness.clock.Advance(15 * time.Second)
	if _, err := harness.service.Reserve(context.Background(), ReservationIntent{
		RequestID: strings.Repeat("b", 32), GrantID: harness.grant.ID, Method: "GET",
		Range: HTTPRange{Kind: HTTPRangeFull, Length: 1}, ReservedBytes: 1,
	}); !errors.Is(err, ErrGrantUnavailable) {
		t.Fatalf("absolute-expired grant error=%v", err)
	}
}

func TestBudgetExpiredWindowResetsOnlyWithoutInflightWork(t *testing.T) {
	harness := newBudgetTestHarness(t, nil, nil)
	expired := model.BackupAssetDeliveryUsage{
		ScopeKind: string(BudgetScopeGlobal), ScopeID: "global",
		WindowStartedAt: harness.clock.Now().Add(-2 * time.Minute), WindowExpiresAt: harness.clock.Now().Add(-time.Minute),
		RequestCount: 90, DeliveredBytes: 90, InFlight: 0, Version: 1, UpdatedAt: harness.clock.Now().Add(-time.Minute),
	}
	if err := harness.db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}
	reservation := harness.reserve(t, strings.Repeat("9", 32), 5)
	usage := harness.loadUsage(t, BudgetScopeKey{Kind: BudgetScopeGlobal, ID: "global"})
	if usage.RequestCount != 1 || usage.DeliveredBytes != 0 || usage.ReservedBytes != 5 ||
		!usage.WindowStartedAt.Equal(harness.clock.Now()) {
		t.Fatalf("reset usage=%+v", usage)
	}
	if _, err := harness.service.Finalize(context.Background(), FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
		State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 5, ResponseBytes: 5, EvidenceKnown: true,
	}); err != nil {
		t.Fatal(err)

	}

	harness = newBudgetTestHarness(t, nil, nil)
	expired.ScopeKind, expired.ScopeID = string(BudgetScopeGlobal), "global"
	expired.InFlight, expired.ReservedBytes = 1, 5
	if err := harness.db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Reserve(context.Background(), ReservationIntent{
		RequestID: strings.Repeat("9", 32), GrantID: harness.grant.ID, Method: "GET",
		Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
	}); err != nil {
		t.Fatal(err)
	}
	usage = harness.loadUsage(t, BudgetScopeKey{Kind: BudgetScopeGlobal, ID: "global"})
	if usage.RequestCount != 91 || usage.DeliveredBytes != 90 || usage.ReservedBytes != 15 || usage.InFlight != 2 {
		t.Fatalf("inflight expired window reset unsafely: %+v", usage)
	}
}

func TestBudgetReservationBytesIncludesProviderProbeAndRejectsOverflow(t *testing.T) {
	tests := []struct {
		response, provider int64
		probe              bool
		want               int64
	}{
		{response: 10, provider: 8, want: 10},
		{response: 8, provider: 10, want: 10},
		{response: 10, provider: 10, probe: true, want: 11},
		{response: 12, provider: 10, probe: true, want: 12},
		{response: 0, provider: 0, want: 0},
	}
	for _, testCase := range tests {
		got, err := ComputeReservationBytes(testCase.response, testCase.provider, testCase.probe)
		if err != nil || got != testCase.want {
			t.Fatalf("ComputeReservationBytes(%+v)=%d,%v", testCase, got, err)
		}
	}
	for _, testCase := range []struct {
		response, provider int64
		probe              bool
	}{
		{response: -1}, {provider: -1}, {provider: math.MaxInt64, probe: true},
	} {
		if _, err := ComputeReservationBytes(testCase.response, testCase.provider, testCase.probe); !errors.Is(err, ErrInvalidReservation) {
			t.Fatalf("invalid reservation bytes %+v error=%v", testCase, err)
		}
	}
}

type budgetTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *budgetTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *budgetTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

type budgetTestHarness struct {
	db      *gorm.DB
	service *BudgetService
	clock   *budgetTestClock
	grant   model.BackupAssetDeliveryGrant
}

func newBudgetTestHarness(
	t *testing.T,
	grantEdit func(*model.BackupAssetDeliveryGrant),
	limitsEdit func(*BudgetLimits),
) *budgetTestHarness {
	t.Helper()
	clock := &budgetTestClock{now: time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)}
	dsn := filepath.Join(t.TempDir(), "budget.db") + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: clock.Now, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{}, &model.BackupAssetDeliveryUsage{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	grant := testBudgetGrant(clock.Now())
	if grantEdit != nil {
		grantEdit(&grant)
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	limits := testBudgetLimits()
	if limitsEdit != nil {
		limitsEdit(&limits)
	}
	service, err := NewBudgetService(BudgetDependencies{
		DB: db, Now: clock.Now, Limits: func(context.Context) (BudgetLimits, error) { return limits, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &budgetTestHarness{db: db, service: service, clock: clock, grant: grant}
}

func testBudgetGrant(now time.Time) model.BackupAssetDeliveryGrant {
	pointID, catalogID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 64)
	return model.BackupAssetDeliveryGrant{
		ID: strings.Repeat("4", 32), DeliveryID: strings.Repeat("5", 32), ResourceKind: string(DeliveryResourceBackupAsset),
		RecoveryPointID: &pointID, CatalogGenerationID: &catalogID, EntryID: &entryID,
		OwnerUserID: 42, SessionJTI: strings.Repeat("6", 32), SessionRole: "operator", SessionExpiresAt: now.Add(time.Hour),
		Action: string(DeliveryPreview), MethodPolicy: string(MethodGetHead), RangePolicy: string(RangeSingle),
		Renderer: string(RendererSafeRaster), Profile: string(ProfileRasterV1), Classification: string(ClassificationNonSecret),
		ClassificationRevision: 1, ClassificationSourceRevision: 1, ProviderKind: string(backupasset.ProviderRsync),
		SourceFingerprint: "source-v1", EntryFingerprint: "entry-v1", FingerprintStrength: "strong",
		RepresentationETag: `"content-v1"`, SourceSize: 100, DetectedMediaType: "image/png",
		RepresentationSourceBytes: 100, RepresentationSize: 100, CookieSecretHash: strings.Repeat("7", 64),
		State: string(DeliveryActive), LeaseID: strings.Repeat("8", 32), LeaseAttemptID: strings.Repeat("a", 32),
		LeaseFenceTokenHash: strings.Repeat("b", 64), AbsoluteExpiresAt: now.Add(30 * time.Minute),
		IdleExpiresAt: now.Add(30 * time.Second), IdleTTLSeconds: 60, LastActivityAt: now,
		MaxBytesPerRequest: 100, MaxCumulativeBytes: 100, MaxRequests: 100, MaxInFlight: 2,
		Version: 1, AuditState: "none", CreatedAt: now, UpdatedAt: now,
	}
}

func testBudgetLimits() BudgetLimits {
	return BudgetLimits{
		Window:   time.Minute,
		Global:   BudgetScopeLimits{WindowBytes: 1_000, WindowRequests: 100, MaxInFlight: 10},
		Provider: BudgetScopeLimits{WindowBytes: 1_000, WindowRequests: 100, MaxInFlight: 10},
		User:     BudgetScopeLimits{WindowBytes: 1_000, WindowRequests: 100, MaxInFlight: 10},
	}
}

func testBudgetUsage(kind BudgetScopeKind, id string, requests, delivered, inFlight int64) *model.BackupAssetDeliveryUsage {
	return &model.BackupAssetDeliveryUsage{
		ScopeKind: string(kind), ScopeID: id, RequestCount: requests, DeliveredBytes: delivered,
		InFlight: inFlight, Version: 1,
	}
}

func (harness *budgetTestHarness) reserve(t *testing.T, requestID string, bytes int64) Reservation {
	t.Helper()
	reservation, err := harness.service.Reserve(context.Background(), ReservationIntent{
		RequestID: requestID, GrantID: harness.grant.ID, Method: "GET",
		Range: HTTPRange{Kind: HTTPRangeFull, Length: bytes}, ReservedBytes: bytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reservation
}

func (harness *budgetTestHarness) loadGrant(t *testing.T) model.BackupAssetDeliveryGrant {
	t.Helper()
	var grant model.BackupAssetDeliveryGrant
	if err := harness.db.First(&grant, "id = ?", harness.grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	return grant
}

func (harness *budgetTestHarness) loadUsage(t *testing.T, key BudgetScopeKey) model.BackupAssetDeliveryUsage {
	t.Helper()
	var usage model.BackupAssetDeliveryUsage
	if err := harness.db.First(&usage, "scope_kind = ? AND scope_id = ?", key.Kind, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	return usage
}
