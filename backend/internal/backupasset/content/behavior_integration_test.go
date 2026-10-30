package content

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestContentBehaviorSQLite(t *testing.T) {
	runContentBehaviorContract(t, openContentBehaviorSQLite(t))
}

func TestContentBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_CONTENT_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_CONTENT_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runContentBehaviorContract(t, openContentBehaviorPostgres(t, dsn))
}

type contentBehaviorFixture struct {
	db     *gorm.DB
	engine string
	clock  *budgetTestClock
}

func runContentBehaviorContract(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	seedContentBehaviorParents(t, fixture)

	t.Run("ConcurrentReservationsNeverOversellAnyScope", func(t *testing.T) {
		grant := seedContentBehaviorGrant(t, fixture, "5", 50, 20)
		limits := BudgetLimits{
			Window:   time.Minute,
			Global:   BudgetScopeLimits{WindowBytes: 50, WindowRequests: 5, MaxInFlight: 20},
			Provider: BudgetScopeLimits{WindowBytes: 50, WindowRequests: 5, MaxInFlight: 20},
			User:     BudgetScopeLimits{WindowBytes: 50, WindowRequests: 5, MaxInFlight: 20},
		}
		service := newContentBehaviorBudgetService(t, fixture, limits)
		const contenders = 12
		start := make(chan struct{})
		results := make(chan struct {
			reservation Reservation
			err         error
		}, contenders)
		var ready sync.WaitGroup
		ready.Add(contenders)
		for index := 0; index < contenders; index++ {
			go func(index int) {
				ready.Done()
				<-start
				reservation, err := service.Reserve(context.Background(), ReservationIntent{
					RequestID: fmt.Sprintf("%032x", 1_000+index), GrantID: grant.ID, Method: "GET",
					Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
				})
				results <- struct {
					reservation Reservation
					err         error
				}{reservation: reservation, err: err}
			}(index)
		}
		ready.Wait()
		close(start)

		var successes []Reservation
		for range contenders {
			result := <-results
			switch {
			case result.err == nil:
				successes = append(successes, result.reservation)
			case errors.Is(result.err, ErrBudgetExhausted):
			default:
				t.Fatalf("%s concurrent reservation error=%v", fixture.engine, result.err)
			}
		}
		if len(successes) != 5 {
			t.Fatalf("%s successful reservations=%d want=5", fixture.engine, len(successes))
		}
		for _, reservation := range successes {
			if _, err := service.Finalize(context.Background(), FinalizeIntent{
				RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
				State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 10, ResponseBytes: 10, EvidenceKnown: true,
			}); err != nil {
				t.Fatalf("%s finalize concurrent reservation: %v", fixture.engine, err)
			}
		}
		assertContentBehaviorCounters(t, fixture, grant, 5, 50, 0, 0)
		assertContentBehaviorAuditCounters(t, fixture, grant, 5, 5, 0, 0)
	})

	clearContentBehaviorState(t, fixture)
	t.Run("ReplayCancelAndCrashAreIdempotentAndConservative", func(t *testing.T) {
		grant := seedContentBehaviorGrant(t, fixture, "c", 100, 4)
		service := newContentBehaviorBudgetService(t, fixture, testBudgetLimits())

		replayed, err := service.Reserve(context.Background(), ReservationIntent{
			RequestID: strings.Repeat("d", 32), GrantID: grant.ID, Method: "GET",
			Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Reserve(context.Background(), ReservationIntent{
			RequestID: replayed.RequestID, GrantID: grant.ID, Method: "GET",
			Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
		}); !errors.Is(err, ErrReservationReplay) {
			t.Fatalf("%s replay error=%v", fixture.engine, err)
		}
		first, err := service.Finalize(context.Background(), FinalizeIntent{
			RequestID: replayed.RequestID, ExpectedRequestVersion: replayed.RequestVersion,
			State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 1, ResponseBytes: 1, EvidenceKnown: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		duplicate, err := service.Finalize(context.Background(), FinalizeIntent{
			RequestID: replayed.RequestID, ExpectedRequestVersion: replayed.RequestVersion,
			State: RequestSucceeded, HTTPStatus: 200, ProviderBytes: 1, ResponseBytes: 1, EvidenceKnown: true,
		})
		if err != nil || !duplicate.AlreadyFinalized || duplicate.ChargedBytes != first.ChargedBytes {
			t.Fatalf("%s duplicate finalize=%+v err=%v", fixture.engine, duplicate, err)
		}

		canceled, err := service.Reserve(context.Background(), ReservationIntent{
			RequestID: strings.Repeat("e", 32), GrantID: grant.ID, Method: "GET",
			Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Finalize(context.Background(), FinalizeIntent{
			RequestID: canceled.RequestID, ExpectedRequestVersion: canceled.RequestVersion,
			State: RequestCanceled, HTTPStatus: 499, FailureCode: RequestFailureClientCanceled,
			ProviderBytes: 4, ResponseBytes: 2, EvidenceKnown: true,
		}); err != nil {
			t.Fatal(err)
		}

		crashed, err := service.Reserve(context.Background(), ReservationIntent{
			RequestID: strings.Repeat("f", 32), GrantID: grant.ID, Method: "GET",
			Range: HTTPRange{Kind: HTTPRangeFull, Length: 10}, ReservedBytes: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Finalize(context.Background(), FinalizeIntent{
			RequestID: crashed.RequestID, ExpectedRequestVersion: crashed.RequestVersion,
			State: RequestReconciled, HTTPStatus: 500, FailureCode: RequestFailureReconciledCrash,
			ProviderBytes: -1, EvidenceKnown: false,
		}); err != nil {
			t.Fatal(err)
		}
		assertContentBehaviorCounters(t, fixture, grant, 3, 15, 0, 0)
		assertContentBehaviorAuditCounters(t, fixture, grant, 3, 1, 0, 2)
	})

	t.Run("FirstSuccessfulGETPerGrant", func(t *testing.T) {
		clearContentBehaviorState(t, fixture)
		testContentBehaviorFirstSuccessfulGETPerGrant(t, fixture)
	})
	t.Run("ConcurrentSuccessfulGETFinalization", func(t *testing.T) {
		clearContentBehaviorState(t, fixture)
		testContentBehaviorConcurrentSuccessfulGETFinalization(t, fixture)
	})
	t.Run("FirstSuccessfulGETRollbackDoesNotLeak", func(t *testing.T) {
		clearContentBehaviorState(t, fixture)
		testContentBehaviorFirstSuccessfulGETRollbackDoesNotLeak(t, fixture)
	})
}

func testContentBehaviorFirstSuccessfulGETPerGrant(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	grant := seedContentBehaviorGrant(t, fixture, "5", 100, 4)
	service := newContentBehaviorBudgetService(t, fixture, testBudgetLimits())

	head := reserveContentBehaviorRequest(t, service, grant.ID, 101, http.MethodHead, 0, false)
	headResult := finalizeContentBehaviorRequest(t, service, head, RequestSucceeded, http.StatusOK, "", 0)
	if headResult.FirstSuccessfulGET || headResult.AlreadyFinalized {
		t.Fatalf("%s HEAD finalization=%+v", fixture.engine, headResult)
	}
	assertContentBehaviorRequest(t, fixture, head, http.MethodHead, RequestSucceeded, http.StatusOK, 0)

	failed := reserveContentBehaviorRequest(t, service, grant.ID, 102, http.MethodGet, 10, false)
	failedResult := finalizeContentBehaviorRequest(t, service, failed, RequestFailed, http.StatusInternalServerError, RequestFailureSourceFailed, 0)
	if failedResult.FirstSuccessfulGET || failedResult.AlreadyFinalized {
		t.Fatalf("%s failed GET finalization=%+v", fixture.engine, failedResult)
	}
	assertContentBehaviorRequest(t, fixture, failed, http.MethodGet, RequestFailed, http.StatusInternalServerError, 0)

	first := reserveContentBehaviorRequest(t, service, grant.ID, 103, http.MethodGet, 10, true)
	firstResult := finalizeContentBehaviorRequest(t, service, first, RequestSucceeded, http.StatusPartialContent, "", 10)
	if !firstResult.FirstSuccessfulGET || firstResult.AlreadyFinalized {
		t.Fatalf("%s first successful GET finalization=%+v", fixture.engine, firstResult)
	}
	assertContentBehaviorRequest(t, fixture, first, http.MethodGet, RequestSucceeded, http.StatusPartialContent, 10)

	later := reserveContentBehaviorRequest(t, service, grant.ID, 104, http.MethodGet, 10, false)
	laterResult := finalizeContentBehaviorRequest(t, service, later, RequestSucceeded, http.StatusOK, "", 10)
	if laterResult.FirstSuccessfulGET || laterResult.AlreadyFinalized {
		t.Fatalf("%s later successful GET finalization=%+v", fixture.engine, laterResult)
	}
	assertContentBehaviorRequest(t, fixture, later, http.MethodGet, RequestSucceeded, http.StatusOK, 10)

	replay := finalizeContentBehaviorRequest(t, service, first, RequestSucceeded, http.StatusPartialContent, "", 10)
	if replay.FirstSuccessfulGET || !replay.AlreadyFinalized || replay.ChargedBytes != firstResult.ChargedBytes {
		t.Fatalf("%s replay finalization=%+v", fixture.engine, replay)
	}
	assertContentBehaviorCounters(t, fixture, grant, 4, 20, 0, 0)

	newGrant := seedContentBehaviorGrant(t, fixture, "a", 100, 4)
	empty := reserveContentBehaviorRequest(t, service, newGrant.ID, 105, http.MethodGet, 0, false)
	emptyResult := finalizeContentBehaviorRequest(t, service, empty, RequestSucceeded, http.StatusOK, "", 0)
	if !emptyResult.FirstSuccessfulGET || emptyResult.AlreadyFinalized {
		t.Fatalf("%s new grant empty GET finalization=%+v", fixture.engine, emptyResult)
	}
	assertContentBehaviorRequest(t, fixture, empty, http.MethodGet, RequestSucceeded, http.StatusOK, 0)

	otherStatusGrant := seedContentBehaviorGrant(t, fixture, "d", 100, 4)
	noContent := reserveContentBehaviorRequest(t, service, otherStatusGrant.ID, 106, http.MethodGet, 0, false)
	noContentResult := finalizeContentBehaviorRequest(t, service, noContent, RequestSucceeded, http.StatusNoContent, "", 0)
	if noContentResult.FirstSuccessfulGET || noContentResult.AlreadyFinalized {
		t.Fatalf("%s non-200/206 GET finalization=%+v", fixture.engine, noContentResult)
	}
	assertContentBehaviorRequest(t, fixture, noContent, http.MethodGet, RequestSucceeded, http.StatusNoContent, 0)
}

func reserveContentBehaviorRequest(
	t *testing.T, service *BudgetService, grantID string, id int, method string, length int64, partial bool,
) Reservation {
	t.Helper()
	requestRange := HTTPRange{Kind: HTTPRangeFull, Length: length}
	if partial {
		start, end := int64(0), length
		requestRange = HTTPRange{Kind: HTTPRangeNormal, Start: &start, EndExclusive: &end, Length: length}
	}
	reservation, err := service.Reserve(context.Background(), ReservationIntent{
		RequestID: fmt.Sprintf("%032x", id), GrantID: grantID, Method: method,
		Range: requestRange, ReservedBytes: length,
	})
	if err != nil {
		t.Fatalf("reserve %s request: %v", method, err)
	}
	return reservation
}

func finalizeContentBehaviorRequest(
	t *testing.T, service *BudgetService, reservation Reservation, state RequestState, status int, failure RequestFailureCode, bytes int64,
) Finalization {
	t.Helper()
	result, err := service.Finalize(context.Background(), FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
		State: state, HTTPStatus: status, FailureCode: failure,
		ProviderBytes: bytes, ResponseBytes: bytes, EvidenceKnown: true,
	})
	if err != nil {
		t.Fatalf("finalize request: %v", err)
	}
	return result
}

func assertContentBehaviorRequest(
	t *testing.T, fixture contentBehaviorFixture, reservation Reservation, method string, state RequestState, status int, bytes int64,
) {
	t.Helper()
	var request model.BackupAssetDeliveryRequest
	if err := fixture.db.First(&request, "id = ?", reservation.RequestID).Error; err != nil {
		t.Fatal(err)
	}
	if request.GrantID != reservation.GrantID || request.Method != method || request.State != string(state) ||
		request.HTTPStatus != status || request.ProviderBytes != bytes || request.ResponseBytes != bytes ||
		(request.FinishedAt == nil) != (state == RequestReserved) {
		t.Fatalf("%s persisted request=%+v", fixture.engine, request)
	}
}

type contentBehaviorFinalizationContextKey struct{}

type contentBehaviorSQLiteBeginBarrier struct {
	gorm.ConnPool
	busy   chan<- error
	resume <-chan struct{}
}

func (barrier *contentBehaviorSQLiteBeginBarrier) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	beginner, ok := barrier.ConnPool.(gorm.TxBeginner)
	if !ok {
		return nil, errors.New("SQLite connection cannot begin transactions")
	}
	tx, err := beginner.BeginTx(ctx, options)
	if err != nil && retryableBudgetError(err) {
		select {
		case barrier.busy <- err:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-barrier.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx, err
}

type contentBehaviorFinalizationResult struct {
	finalization Finalization
	err          error
}

func testContentBehaviorConcurrentSuccessfulGETFinalization(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	grant := seedContentBehaviorGrant(t, fixture, "5", 100, 4)
	service := newContentBehaviorBudgetService(t, fixture, testBudgetLimits())
	first := reserveContentBehaviorRequest(t, service, grant.ID, 301, http.MethodGet, 10, false)
	second := reserveContentBehaviorRequest(t, service, grant.ID, 302, http.MethodGet, 10, true)
	intent := func(reservation Reservation, status int) FinalizeIntent {
		return FinalizeIntent{
			RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
			State: RequestSucceeded, HTTPStatus: status, ProviderBytes: 10, ResponseBytes: 10, EvidenceKnown: true,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	holderUpdated := make(chan struct{}, 1)
	holderRelease := make(chan struct{})
	retryRelease := make(chan struct{})
	var holderReleaseOnce, retryReleaseOnce sync.Once
	releaseHolder := func() { holderReleaseOnce.Do(func() { close(holderRelease) }) }
	releaseRetry := func() { retryReleaseOnce.Do(func() { close(retryRelease) }) }
	var workers sync.WaitGroup
	const callback = "test:content_behavior_recent_finalization_holder"
	if err := fixture.db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(contentBehaviorFinalizationContextKey{}) != "holder" ||
			tx.Statement.Table != (model.BackupAssetDeliveryRequest{}).TableName() || tx.Error != nil {
			return
		}
		select {
		case holderUpdated <- struct{}{}:
		case <-ctx.Done():
			_ = tx.AddError(ctx.Err())
			return
		}
		select {
		case <-holderRelease:
		case <-ctx.Done():
			_ = tx.AddError(ctx.Err())
		}
	}); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseHolder()
		releaseRetry()
		cancel()
		workers.Wait()
		_ = fixture.db.Callback().Update().Remove(callback)
	})

	holderPID := make(chan int, 1)
	holderDone := make(chan contentBehaviorFinalizationResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		var outcome contentBehaviorFinalizationResult
		outcome.err = fixture.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			if fixture.engine == "postgres" {
				var pid int
				if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					return err
				}
				holderPID <- pid
			}
			holder, err := NewBudgetService(BudgetDependencies{
				DB: conn.Session(&gorm.Session{NewDB: true}), Now: fixture.clock.Now,
				Limits: func(context.Context) (BudgetLimits, error) { return testBudgetLimits(), nil },
			})
			if err != nil {
				return err
			}
			holderCtx := context.WithValue(ctx, contentBehaviorFinalizationContextKey{}, "holder")
			outcome.finalization, err = holder.Finalize(holderCtx, intent(first, http.StatusOK))
			return err
		})
		holderDone <- outcome
	}()
	select {
	case <-holderUpdated:
	case outcome := <-holderDone:
		t.Fatalf("%s holder ended before uncommitted update: %+v", fixture.engine, outcome)
	case <-ctx.Done():
		t.Fatalf("%s holder did not reach uncommitted update: %v", fixture.engine, ctx.Err())
	}

	contenderPID := make(chan int, 1)
	contenderBusy := make(chan error, 1)
	contenderDone := make(chan contentBehaviorFinalizationResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		var outcome contentBehaviorFinalizationResult
		outcome.err = fixture.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			if fixture.engine == "postgres" {
				var pid int
				if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					return err
				}
				contenderPID <- pid
			} else {
				if err := conn.Exec("PRAGMA busy_timeout = 0").Error; err != nil {
					return err
				}
				conn.Statement.ConnPool = &contentBehaviorSQLiteBeginBarrier{
					ConnPool: conn.Statement.ConnPool, busy: contenderBusy, resume: retryRelease,
				}
			}
			contender, err := NewBudgetService(BudgetDependencies{
				DB: conn.Session(&gorm.Session{NewDB: true}), Now: fixture.clock.Now,
				Limits: func(context.Context) (BudgetLimits, error) { return testBudgetLimits(), nil },
			})
			if err == nil {
				outcome.finalization, err = contender.Finalize(ctx, intent(second, http.StatusPartialContent))
			}
			if fixture.engine == "sqlite" {
				err = errors.Join(err, conn.Session(&gorm.Session{NewDB: true}).Exec("PRAGMA busy_timeout = 5000").Error)
			}
			return err
		})
		contenderDone <- outcome
	}()

	if fixture.engine == "postgres" {
		var holder, contender int
		select {
		case holder = <-holderPID:
		case <-ctx.Done():
			t.Fatalf("missing PostgreSQL holder PID: %v", ctx.Err())
		}
		select {
		case contender = <-contenderPID:
		case outcome := <-contenderDone:
			t.Fatalf("PostgreSQL contender ended before lock wait: %+v", outcome)
		case <-ctx.Done():
			t.Fatalf("missing PostgreSQL contender PID: %v", ctx.Err())
		}
		waitContentBehaviorPostgresLock(t, ctx, fixture.db, holder, contender, contenderDone)
	} else {
		select {
		case err := <-contenderBusy:
			message := strings.ToLower(err.Error())
			if !strings.Contains(message, "database is locked") && !strings.Contains(message, "database table is locked") {
				t.Fatalf("SQLite contention did not report BUSY/LOCKED: %v", err)
			}
		case outcome := <-contenderDone:
			t.Fatalf("SQLite contender ended before lock contention: %+v", outcome)
		case <-ctx.Done():
			t.Fatalf("SQLite contender did not reach the database lock: %v", ctx.Err())
		}
	}

	releaseHolder()
	var firstResult contentBehaviorFinalizationResult
	select {
	case firstResult = <-holderDone:
	case <-ctx.Done():
		t.Fatalf("%s holder did not commit: %v", fixture.engine, ctx.Err())
	}
	if firstResult.err != nil || !firstResult.finalization.FirstSuccessfulGET || firstResult.finalization.AlreadyFinalized {
		t.Fatalf("%s holder finalization=%+v err=%v", fixture.engine, firstResult.finalization, firstResult.err)
	}
	releaseRetry()
	var secondResult contentBehaviorFinalizationResult
	select {
	case secondResult = <-contenderDone:
	case <-ctx.Done():
		t.Fatalf("%s contender did not finish: %v", fixture.engine, ctx.Err())
	}
	if secondResult.err != nil || secondResult.finalization.FirstSuccessfulGET || secondResult.finalization.AlreadyFinalized {
		t.Fatalf("%s contender finalization=%+v err=%v", fixture.engine, secondResult.finalization, secondResult.err)
	}
	assertContentBehaviorRequest(t, fixture, first, http.MethodGet, RequestSucceeded, http.StatusOK, 10)
	assertContentBehaviorRequest(t, fixture, second, http.MethodGet, RequestSucceeded, http.StatusPartialContent, 10)
	assertContentBehaviorCounters(t, fixture, grant, 2, 20, 0, 0)
	var storedGrant model.BackupAssetDeliveryGrant
	if err := fixture.db.First(&storedGrant, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedGrant.AuditState != "pending" || storedGrant.AuditRequestCount != 2 ||
		storedGrant.AuditSuccessCount != 2 || storedGrant.AuditFailureCount != 0 ||
		storedGrant.AuditRangeCount != 1 || storedGrant.AuditRangeBytes != 10 {
		t.Fatalf("%s concurrent finalization audit counters=%+v", fixture.engine, storedGrant)
	}
}

func waitContentBehaviorPostgresLock(
	t *testing.T, ctx context.Context, db *gorm.DB, holderPID, contenderPID int,
	contenderDone <-chan contentBehaviorFinalizationResult,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var activity struct {
			WaitEventType   string
			BlockedByHolder bool
		}
		result := db.WithContext(ctx).Raw(`SELECT COALESCE(wait_event_type, '') AS wait_event_type,
			COALESCE(? = ANY(pg_blocking_pids(pid)), false) AS blocked_by_holder
			FROM pg_stat_activity WHERE pid = ?`, holderPID, contenderPID).Scan(&activity)
		if result.Error != nil {
			t.Fatalf("inspect PostgreSQL lock wait: %v", result.Error)
		}
		if result.RowsAffected == 1 && activity.WaitEventType == "Lock" && activity.BlockedByHolder {
			return
		}
		select {
		case outcome := <-contenderDone:
			t.Fatalf("PostgreSQL contender did not wait for holder: %+v", outcome)
		case <-ctx.Done():
			t.Fatalf("PostgreSQL contender pid %d did not wait for holder pid %d: %+v, %v", contenderPID, holderPID, activity, ctx.Err())
		case <-ticker.C:
		}
	}
}

type contentBehaviorLookupFailureKey struct{}

func testContentBehaviorFirstSuccessfulGETRollbackDoesNotLeak(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	grant := seedContentBehaviorGrant(t, fixture, "5", 100, 4)
	service := newContentBehaviorBudgetService(t, fixture, testBudgetLimits())
	reservation := reserveContentBehaviorRequest(t, service, grant.ID, 201, http.MethodGet, 10, false)
	intent := FinalizeIntent{
		RequestID: reservation.RequestID, ExpectedRequestVersion: reservation.RequestVersion,
		State: RequestSucceeded, HTTPStatus: http.StatusOK, ProviderBytes: 10, ResponseBytes: 10, EvidenceKnown: true,
	}

	lookupFailure := errors.New("injected successful GET lookup failure")
	const callback = "test:content_behavior_recent_lookup_failure"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(contentBehaviorLookupFailureKey{}) == true &&
			tx.Statement.Table == (model.BackupAssetDeliveryRequest{}).TableName() &&
			len(tx.Statement.Selects) == 1 && tx.Statement.Selects[0] == "id" {
			_ = tx.AddError(lookupFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	callbackInstalled := true
	t.Cleanup(func() {
		if callbackInstalled {
			_ = fixture.db.Callback().Query().Remove(callback)
		}
	})
	lookupCtx := context.WithValue(context.Background(), contentBehaviorLookupFailureKey{}, true)
	result, err := service.Finalize(lookupCtx, intent)
	if !errors.Is(err, lookupFailure) || result != (Finalization{}) {
		t.Fatalf("%s lookup failure finalization=%+v err=%v", fixture.engine, result, err)
	}
	if err := fixture.db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	callbackInstalled = false
	assertContentBehaviorRequest(t, fixture, reservation, http.MethodGet, RequestReserved, 0, 0)
	assertContentBehaviorCounters(t, fixture, grant, 1, 0, 10, 1)

	const triggerName = "test_content_recent_reject_success"
	dropTrigger := "DROP TRIGGER IF EXISTS " + triggerName
	if fixture.engine == "postgres" {
		dropTrigger += " ON backup_asset_delivery_requests"
		mustContentBehaviorExec(t, fixture, `CREATE FUNCTION test_content_recent_reject_success() RETURNS trigger
			LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected successful GET update failure'; END; $$`)
		t.Cleanup(func() {
			if err := fixture.db.Exec("DROP FUNCTION IF EXISTS test_content_recent_reject_success()").Error; err != nil {
				t.Errorf("drop injected trigger function: %v", err)
			}
		})
	}
	t.Cleanup(func() {
		if err := fixture.db.Exec(dropTrigger).Error; err != nil {
			t.Errorf("drop injected trigger: %v", err)
		}
	})
	if fixture.engine == "postgres" {
		mustContentBehaviorExec(t, fixture, fmt.Sprintf(`CREATE TRIGGER %s
			BEFORE UPDATE OF state ON backup_asset_delivery_requests
			FOR EACH ROW WHEN (NEW.id = '%s' AND NEW.state = 'succeeded')
			EXECUTE FUNCTION test_content_recent_reject_success()`, triggerName, reservation.RequestID))
	} else {
		mustContentBehaviorExec(t, fixture, fmt.Sprintf(`CREATE TRIGGER %s
			BEFORE UPDATE OF state ON backup_asset_delivery_requests
			WHEN NEW.id = '%s' AND NEW.state = 'succeeded'
			BEGIN SELECT RAISE(ABORT, 'injected successful GET update failure'); END`, triggerName, reservation.RequestID))
	}
	result, err = service.Finalize(context.Background(), intent)
	if err == nil || !strings.Contains(err.Error(), "injected successful GET update failure") || result != (Finalization{}) {
		t.Fatalf("%s update failure finalization=%+v err=%v", fixture.engine, result, err)
	}
	assertContentBehaviorRequest(t, fixture, reservation, http.MethodGet, RequestReserved, 0, 0)
	assertContentBehaviorCounters(t, fixture, grant, 1, 0, 10, 1)
	var storedGrant model.BackupAssetDeliveryGrant
	if err := fixture.db.First(&storedGrant, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedGrant.AuditState != "none" || storedGrant.AuditRequestCount != 0 || storedGrant.AuditSuccessCount != 0 {
		t.Fatalf("%s failed transaction persisted audit counters=%+v", fixture.engine, storedGrant)
	}

	mustContentBehaviorExec(t, fixture, dropTrigger)
	result, err = service.Finalize(context.Background(), intent)
	if err != nil || !result.FirstSuccessfulGET || result.AlreadyFinalized {
		t.Fatalf("%s retry after rollback finalization=%+v err=%v", fixture.engine, result, err)
	}
	assertContentBehaviorRequest(t, fixture, reservation, http.MethodGet, RequestSucceeded, http.StatusOK, 10)
	assertContentBehaviorCounters(t, fixture, grant, 1, 10, 0, 0)
	assertContentBehaviorAuditCounters(t, fixture, grant, 1, 1, 0, 0)
	replay, err := service.Finalize(context.Background(), intent)
	if err != nil || !replay.AlreadyFinalized || replay.FirstSuccessfulGET {
		t.Fatalf("%s retry replay finalization=%+v err=%v", fixture.engine, replay, err)
	}
}

func assertContentBehaviorAuditCounters(
	t *testing.T,
	fixture contentBehaviorFixture,
	grant model.BackupAssetDeliveryGrant,
	wantRequests, wantSuccess, wantBlocked, wantFailure int64,
) {
	t.Helper()
	var stored model.BackupAssetDeliveryGrant
	if err := fixture.db.First(&stored, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AuditState != "pending" || stored.AuditRequestCount != wantRequests ||
		stored.AuditSuccessCount != wantSuccess || stored.AuditBlockedCount != wantBlocked ||
		stored.AuditFailureCount != wantFailure || stored.AuditRangeCount != 0 || stored.AuditRangeBytes != 0 {
		t.Fatalf("%s audit counters=%+v", fixture.engine, stored)
	}
}

func openContentBehaviorSQLite(t *testing.T) contentBehaviorFixture {
	t.Helper()
	configureContentBehaviorEnvironment(t)
	clock := &budgetTestClock{now: time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)}
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "content-behavior.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeContentBehaviorDB(db) })
	return contentBehaviorFixture{db: db, engine: "sqlite", clock: clock}
}

func openContentBehaviorPostgres(t *testing.T, dsn string) contentBehaviorFixture {
	t.Helper()
	configureContentBehaviorEnvironment(t)
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("xirang_content_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db, "postgres"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeContentBehaviorDB(db)
		_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		closeContentBehaviorDB(base)
	})
	return contentBehaviorFixture{
		db: db, engine: "postgres",
		clock: &budgetTestClock{now: time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)},
	}
}

func configureContentBehaviorEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_CONTENT_BEHAVIOR_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func seedContentBehaviorParents(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	now := fixture.clock.Now()
	mustContentBehaviorExec(t, fixture, `INSERT INTO users
		(id, username, password_hash, role, totp_secret, totp_enabled, recovery_codes,
		 token_version, onboarded, created_at, updated_at)
		VALUES (?, 'content-behavior-user', 'hash', 'operator', '', ?, '', 0, ?, ?, ?)`, 42, false, true, now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO backup_repositories
		(id, provider_kind, repository_identity, display_name, description, version_mode,
		 status, capability_revision, capabilities_json, immutability_level, created_at, updated_at)
		VALUES (?, 'rsync', 'content-behavior-repository', 'content-behavior', '', 'hardlink_tree',
		 'online', 1, '{}', 'xirang_managed', ?, ?)`, strings.Repeat("1", 32), now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO recovery_points
		(id, repository_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
		 lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, observed_at,
		 source_fingerprint, manifest_digest_algorithm, manifest_digest, entry_count, logical_bytes,
		 consistency_json, fidelity_json, capability_revision, capabilities_json, immutability_level,
		 physical_availability, hold_state, created_at, updated_at)
		VALUES (?, ?, '', 0, '', '{}', '', '', 'mutable_head', 'observed', ?, 'content-source-v1',
		 'sha256', '', 1, 100, '{}', '{}', 1, '{}', 'mutable', 'online', 'none', ?, ?)`,
		strings.Repeat("2", 32), strings.Repeat("1", 32), now, now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO catalog_generations
		(id, recovery_point_id, generation, state, is_active, source_fingerprint,
		 expected_entry_count, written_entry_count, expected_digest, written_digest,
		 error_code, correlation_id, started_at, finished_at, created_at, updated_at)
		VALUES (?, ?, 1, 'complete', ?, 'content-source-v1', 1, 1, '', '', '', '', ?, ?, ?, ?)`,
		strings.Repeat("3", 32), strings.Repeat("2", 32), true, now, now, now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO catalog_entries
		(generation_id, entry_id, recovery_point_id, normalized_path, name, entry_type,
		 size, mode, owner, mime_type, fingerprint, fingerprint_strength,
		 encrypted_provider_locator, security_state, created_at)
		VALUES (?, ?, ?, '/content/asset.bin', 'asset.bin', 'file', 100, '', '',
		 'application/octet-stream', 'entry-v1', 'strong', '', 'non_secret', ?)`,
		strings.Repeat("3", 32), strings.Repeat("4", 64), strings.Repeat("2", 32), now)
}

func seedContentBehaviorGrant(
	t *testing.T,
	fixture contentBehaviorFixture,
	marker string,
	maxCumulative, maxInFlight int64,
) model.BackupAssetDeliveryGrant {
	t.Helper()
	now := fixture.clock.Now()
	grantID := strings.Repeat(marker, 32)
	leaseID := strings.Repeat(nextHexMarker(marker), 32)
	deliveryID := strings.Repeat(nextHexMarker(nextHexMarker(marker)), 32)
	mustContentBehaviorExec(t, fixture, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'content_session', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		leaseID, strings.Repeat("2", 32), grantID, strings.Repeat("8", 32), strings.Repeat("9", 64),
		now.Add(5*time.Minute), now.Add(time.Hour), now, now, now)
	mustContentBehaviorExec(t, fixture, `INSERT INTO backup_asset_delivery_grants
		(id, delivery_id, resource_kind, recovery_point_id, catalog_generation_id, entry_id,
		 owner_user_id, session_jti, session_token_version, session_role, session_expires_at,
		 action, method_policy, range_policy, renderer, profile, classification,
		 classification_revision, classification_source_revision, provider_kind,
		 source_fingerprint, entry_fingerprint, fingerprint_strength, representation_etag,
		 source_size, source_modified_at, detected_media_type, representation_source_bytes,
		 representation_size, representation_truncated, cookie_secret_hash,
		 state, lease_id, lease_attempt_id, lease_fence_token_hash,
		 absolute_expires_at, idle_expires_at, idle_ttl_seconds, last_activity_at,
		 max_bytes_per_request, max_cumulative_bytes, max_requests, max_in_flight,
		 version, created_at, updated_at)
		VALUES (?, ?, 'backup_asset', ?, ?, ?, 42, ?, 0, 'operator', ?,
		 'preview', 'get_head', 'single', 'safe_raster', 'raster_v1', 'non_secret',
		 1, 1, 'rsync', 'content-source-v1', 'entry-v1', 'strong', '"content-etag"',
		 100, ?, 'application/octet-stream', 100, 100, ?, ?, 'active', ?, ?, ?,
		 ?, ?, 60, ?, 10, ?, 100, ?, 1, ?, ?)`,
		grantID, deliveryID, strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 64),
		strings.Repeat("a", 32), now.Add(time.Hour), now, false, strings.Repeat("b", 64), leaseID,
		strings.Repeat("8", 32), strings.Repeat("c", 64), now.Add(30*time.Minute), now.Add(time.Minute), now,
		maxCumulative, maxInFlight, now, now)
	return model.BackupAssetDeliveryGrant{
		ID: grantID, OwnerUserID: 42, ProviderKind: "rsync",
	}
}

func clearContentBehaviorState(t *testing.T, fixture contentBehaviorFixture) {
	t.Helper()
	for _, statement := range []string{
		"DELETE FROM backup_asset_delivery_requests",
		"DELETE FROM backup_asset_delivery_grants",
		"DELETE FROM backup_asset_delivery_usage",
		"DELETE FROM recovery_point_leases WHERE holder_type = 'content_session'",
	} {
		mustContentBehaviorExec(t, fixture, statement)
	}
}

func newContentBehaviorBudgetService(t *testing.T, fixture contentBehaviorFixture, limits BudgetLimits) *BudgetService {
	t.Helper()
	service, err := NewBudgetService(BudgetDependencies{
		DB: fixture.db, Now: fixture.clock.Now,
		Limits: func(context.Context) (BudgetLimits, error) { return limits, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertContentBehaviorCounters(
	t *testing.T,
	fixture contentBehaviorFixture,
	grant model.BackupAssetDeliveryGrant,
	requests, delivered, reserved, inFlight int64,
) {
	t.Helper()
	if err := fixture.db.First(&grant, "id = ?", grant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.RequestCount != requests || grant.DeliveredBytes != delivered ||
		grant.ReservedBytes != reserved || grant.InFlight != inFlight {
		t.Fatalf("%s grant counters=%+v", fixture.engine, grant)
	}
	for _, key := range orderedBudgetScopeKeys(grant) {
		var usage model.BackupAssetDeliveryUsage
		if err := fixture.db.First(&usage, "scope_kind = ? AND scope_id = ?", key.Kind, key.ID).Error; err != nil {
			t.Fatal(err)
		}
		if usage.RequestCount != requests || usage.DeliveredBytes != delivered ||
			usage.ReservedBytes != reserved || usage.InFlight != inFlight ||
			usage.RequestCount < 0 || usage.DeliveredBytes < 0 || usage.ReservedBytes < 0 || usage.InFlight < 0 {
			t.Fatalf("%s scope %+v counters=%+v", fixture.engine, key, usage)
		}
	}
}

func mustContentBehaviorExec(t *testing.T, fixture contentBehaviorFixture, statement string, args ...any) {
	t.Helper()
	if err := fixture.db.Exec(statement, args...).Error; err != nil {
		t.Fatalf("%s content behavior SQL failed: %v", fixture.engine, err)
	}
}

func nextHexMarker(marker string) string {
	const digits = "0123456789abcdef"
	index := strings.Index(digits, marker)
	if index < 0 || index == len(digits)-1 {
		return "0"
	}
	return digits[index+1 : index+2]
}

func closeContentBehaviorDB(db *gorm.DB) {
	if db == nil {
		return
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
