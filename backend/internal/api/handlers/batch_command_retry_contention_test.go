package handlers

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// retrySQLiteBeginProbe observes a real sqlite3 BEGIN IMMEDIATE on a marked
// physical connection. The marker comes from the transaction context, so an
// unrelated background transaction cannot satisfy a contender's barrier.
type retrySQLiteBeginProbe struct {
	expectation atomic.Pointer[retrySQLiteBeginMarker]
	staging     atomic.Pointer[retrySQLiteBeginStage]
	begins      chan string
}

type retrySQLiteBeginStage struct {
	marker    string
	reached   chan struct{}
	releaseCh chan struct{}
	claimed   atomic.Bool
	release   sync.Once
}

func (stage *retrySQLiteBeginStage) releaseBeforeNativeBegin() {
	if stage != nil {
		stage.release.Do(func() { close(stage.releaseCh) })
	}
}

type retrySQLiteBeginMarker struct {
	value string
}

func newRetrySQLiteBeginProbe() *retrySQLiteBeginProbe {
	return &retrySQLiteBeginProbe{begins: make(chan string, 32)}
}

func (p *retrySQLiteBeginProbe) arm(marker string) {
	if p == nil {
		return
	}
	p.expectation.Store(nil)
	p.drain()
	if marker == "" {
		return
	}
	p.expectation.Store(&retrySQLiteBeginMarker{value: marker})
}

// stageBeforeNativeBegin pauses a marked transaction after its connection is
// selected but before SQLite executes BEGIN IMMEDIATE; it owns no writer lock.
func (p *retrySQLiteBeginProbe) stageBeforeNativeBegin(marker string) *retrySQLiteBeginStage {
	if p == nil || marker == "" {
		return nil
	}
	p.expectation.Store(nil)
	p.drain()
	previous := p.staging.Load()
	p.staging.Store(nil)
	if previous != nil {
		previous.releaseBeforeNativeBegin()
	}
	stage := &retrySQLiteBeginStage{
		marker:    marker,
		reached:   make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
	p.staging.Store(stage)
	return stage
}

func (p *retrySQLiteBeginProbe) releaseStaging() {
	if p == nil {
		return
	}
	stage := p.staging.Load()
	p.staging.Store(nil)
	if stage != nil {
		stage.releaseBeforeNativeBegin()
	}
}

func (p *retrySQLiteBeginProbe) waitStaging(t *testing.T, stage *retrySQLiteBeginStage, label string) {
	t.Helper()
	if stage == nil {
		t.Fatalf("SQLite begin staging is unavailable for %s", label)
	}
	select {
	case <-stage.reached:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for SQLite %s to reach pre-BEGIN staging", label)
	}
}

func (p *retrySQLiteBeginProbe) disarm() {
	if p != nil {
		p.expectation.Store(nil)
		p.releaseStaging()
	}
}

func (p *retrySQLiteBeginProbe) drain() {
	if p == nil {
		return
	}
	for {
		select {
		case <-p.begins:
		default:
			return
		}
	}
}

func (p *retrySQLiteBeginProbe) record(marker string) {
	if p == nil {
		return
	}
	expected := p.expectation.Load()
	if expected == nil || expected.value != marker {
		return
	}
	select {
	case p.begins <- marker:
	default:
	}
}

func (p *retrySQLiteBeginProbe) wait(t *testing.T, label string) string {
	t.Helper()
	if p == nil {
		t.Fatalf("SQLite begin probe is unavailable for %s", label)
	}
	select {
	case marker := <-p.begins:
		return marker
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for SQLite %s to attempt BEGIN IMMEDIATE", label)
		return ""
	}
}

type retrySQLiteBeginProbeConnState struct {
	probe        *retrySQLiteBeginProbe
	activeMarker atomic.Pointer[retrySQLiteBeginMarker]
}

// retrySQLiteBeginProbeConn embeds the native SQLite connection so all
// optional driver interfaces remain promoted; only BeginTx is wrapped to make
// the request/consumer marker visible to the per-connection authorizer.
type retrySQLiteBeginProbeConn struct {
	*sqlite3.SQLiteConn
	state *retrySQLiteBeginProbeConnState
}

func (conn *retrySQLiteBeginProbeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	marker := retryTransactionContentionMarker(ctx)
	if marker == "" || conn.state == nil {
		return conn.SQLiteConn.BeginTx(ctx, opts)
	}
	if conn.state.probe != nil {
		if stage := conn.state.probe.staging.Load(); stage != nil && stage.marker == marker && stage.claimed.CompareAndSwap(false, true) {
			close(stage.reached)
			<-stage.releaseCh
		}
	}
	active := &retrySQLiteBeginMarker{value: marker}
	conn.state.activeMarker.Store(active)
	defer conn.state.activeMarker.Store(nil)
	return conn.SQLiteConn.BeginTx(ctx, opts)
}

type retrySQLiteBeginProbeDriver struct {
	probe *retrySQLiteBeginProbe
}

func (d retrySQLiteBeginProbeDriver) Open(name string) (driver.Conn, error) {
	raw, err := (&sqlite3.SQLiteDriver{}).Open(name)
	if err != nil {
		return nil, err
	}
	physical, ok := raw.(*sqlite3.SQLiteConn)
	if !ok {
		_ = raw.Close()
		return nil, fmt.Errorf("SQLite probe driver received %T, want *sqlite3.SQLiteConn", raw)
	}
	state := &retrySQLiteBeginProbeConnState{probe: d.probe}
	physical.RegisterAuthorizer(func(op int, arg1, _, _ string) int {
		if op != sqlite3.SQLITE_TRANSACTION || arg1 != "BEGIN" {
			return sqlite3.SQLITE_OK
		}
		active := state.activeMarker.Load()
		if active != nil && state.probe != nil {
			state.probe.record(active.value)
		}
		return sqlite3.SQLITE_OK
	})
	return &retrySQLiteBeginProbeConn{SQLiteConn: physical, state: state}, nil
}

func openMigratedRetryTransactionSQLiteBeginProbeDB(t *testing.T, probe *retrySQLiteBeginProbe) *gorm.DB {
	t.Helper()
	driverName := fmt.Sprintf("xirang_r13_retry_sqlite_%d", batchTestSequence.Add(1))
	stdsql.Register(driverName, retrySQLiteBeginProbeDriver{probe: probe})
	dsn := filepath.Join(t.TempDir(), "batch-command-retry-migrated.sqlite") + "?_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.New(sqlite.Config{DriverName: driverName, DSN: dsn}), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("open instrumented migrated SQLite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get instrumented migrated SQLite database: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("run instrumented SQLite migrations: %v", err)
	}
	return db
}

func newMigratedRetryTransactionFixtureWithSQLiteBeginProbe(t *testing.T, engine string) (*retryTransactionFixture, *retrySQLiteBeginProbe) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	probe := newRetrySQLiteBeginProbe()
	var db *gorm.DB
	if engine == "sqlite" {
		db = openMigratedRetryTransactionSQLiteBeginProbeDB(t, probe)
	} else {
		db = openMigratedRetryTransactionDB(t, engine)
	}
	fixtureID := batchTestSequence.Add(1)
	admin := seedStepUpUser(t, db, fmt.Sprintf("r13-contention-admin-%d", fixtureID), "admin")
	operator := seedStepUpUser(t, db, fmt.Sprintf("r13-contention-operator-%d", fixtureID), "operator")
	viewer := seedStepUpUser(t, db, fmt.Sprintf("r13-contention-viewer-%d", fixtureID), "viewer")
	jwtManager := newStepUpTestJWTManager(db)
	nodes := seedBatchNodes(t, db)

	handler := NewBatchHandler(db, nil).WithJWTManager(jwtManager)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(jwtManager, db))
	router.POST("/batch-commands/:batch_id/retry", middleware.RBAC("tasks:write"), handler.Retry)
	router.GET("/batch-commands/:batch_id", middleware.RBAC("tasks:read"), handler.Get)
	router.DELETE("/batch-commands/:batch_id", middleware.RBAC("tasks:write"), handler.Delete)
	return &retryTransactionFixture{
		db: db, jwt: jwtManager, handler: handler, router: router,
		admin: admin, operator: operator, viewer: viewer, nodes: nodes,
	}, probe
}

type retryTransactionContentionContextKey struct{}

func retryTransactionContentionMarker(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	marker, _ := ctx.Value(retryTransactionContentionContextKey{}).(string)
	return marker
}

func retryTransactionMarkedContext(marker string) context.Context {
	return context.WithValue(context.Background(), retryTransactionContentionContextKey{}, marker)
}

func retryTransactionMarkedRetryRequest(t *testing.T, router http.Handler, batchID, token, proof, key string, sources []batchRetrySource, marker string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := jsonMarshalRetrySources(sources)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/batch-commands/"+batchID+"/retry", payload)
	req = req.WithContext(context.WithValue(req.Context(), retryTransactionContentionContextKey{}, marker))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(StepUpHeaderName, proof)
	req.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func retryTransactionMarkedDeleteRequest(t *testing.T, router http.Handler, batchID, token, marker string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/batch-commands/"+batchID, nil)
	req = req.WithContext(context.WithValue(req.Context(), retryTransactionContentionContextKey{}, marker))
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func jsonMarshalRetrySources(sources []batchRetrySource) (*strings.Reader, error) {
	payload := struct {
		Sources []batchRetrySource `json:"sources"`
	}{Sources: append([]batchRetrySource(nil), sources...)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(string(encoded)), nil
}

// retryTransactionLockGate captures the exact holder connection for
// PostgreSQL. SQLite uses the begin probe above; both gates block the real
// production callback after its contested row query has acquired the lock.
type retryTransactionLockGate struct {
	reached   <-chan struct{}
	release   func()
	holderPID <-chan int
}

func newRetryTransactionLockGate(t *testing.T, db *gorm.DB, table string, marker string, childBatch bool) retryTransactionLockGate {
	t.Helper()
	reached := make(chan struct{})
	releaseCh := make(chan struct{})
	var claimed atomic.Bool
	var releaseOnce sync.Once
	holderPID := make(chan int, 1)
	callbackName := fmt.Sprintf("retry-transaction-contention-%s-%d", table, batchTestSequence.Add(1))
	isPostgres := db.Name() == "postgres"
	callback := func(tx *gorm.DB) {
		if tx.Statement == nil || !retryTransactionIsSQLTx(tx) {
			return
		}
		tableName := tx.Statement.Table
		if tableName == "" && tx.Statement.Schema != nil {
			tableName = tx.Statement.Schema.Table
		}
		if tableName != table || tx.Error != nil {
			return
		}
		if marker != "" && tx.Statement.Context.Value(retryTransactionContentionContextKey{}) != marker {
			return
		}
		if childBatch {
			batch, ok := tx.Statement.Dest.(*model.BatchCommand)
			if !ok || batch.SourceBatchID == nil {
				return
			}
		} else if table == "batch_commands" {
			if _, ok := tx.Statement.Dest.(*model.BatchCommand); !ok {
				return
			}
		}
		if !childBatch {
			if _, ok := tx.Statement.Clauses["FOR"]; !ok {
				return
			}
		}
		if !claimed.CompareAndSwap(false, true) {
			return
		}
		if isPostgres {
			var pid int
			if err := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				_ = tx.AddError(err)
				return
			}
			holderPID <- pid
		}
		close(reached)
		<-releaseCh
	}
	if childBatch {
		createCallbackName := callbackName + "-create"
		if err := db.Callback().Create().After("gorm:create").Register(createCallbackName, callback); err != nil {
			t.Fatalf("register %s contention gate: %v", table, err)
		}
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(releaseCh) })
			_ = db.Callback().Create().Remove(createCallbackName)
		})
	} else {
		if err := db.Callback().Query().After("gorm:query").Register(callbackName, callback); err != nil {
			t.Fatalf("register %s contention gate: %v", table, err)
		}
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(releaseCh) })
			_ = db.Callback().Query().Remove(callbackName)
		})
	}
	return retryTransactionLockGate{
		reached:   reached,
		release:   func() { releaseOnce.Do(func() { close(releaseCh) }) },
		holderPID: holderPID,
	}
}

func retryTransactionIsSQLTx(tx *gorm.DB) bool {
	if tx == nil || tx.Statement == nil {
		return false
	}
	_, ok := tx.Statement.ConnPool.(*stdsql.Tx)
	return ok
}

func retryTransactionInstallRetryEffectObservationGate(t *testing.T, db *gorm.DB, marker string, hold bool) (<-chan struct{}, <-chan int64, func()) {
	t.Helper()
	reached := make(chan struct{})
	countObserved := make(chan int64, 1)
	releaseCh := make(chan struct{})
	var claimed atomic.Bool
	var releaseOnce sync.Once
	callbackName := fmt.Sprintf("retry-transaction-contention-effect-observation-%d", batchTestSequence.Add(1))
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || !retryTransactionIsSQLTx(tx) || tx.Error != nil {
			return
		}
		if tx.Statement.Context.Value(retryTransactionContentionContextKey{}) != marker {
			return
		}
		tableName := tx.Statement.Table
		if tableName == "" && tx.Statement.Schema != nil {
			tableName = tx.Statement.Schema.Table
		}
		if tableName != "task_run_effects" {
			return
		}
		count, ok := tx.Statement.Dest.(*int64)
		if !ok {
			return
		}
		if !claimed.CompareAndSwap(false, true) {
			return
		}
		countObserved <- *count
		close(reached)
		if hold {
			<-releaseCh
		}
	}); err != nil {
		t.Fatalf("register retry effect observation gate: %v", err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCh) })
		_ = db.Callback().Query().Remove(callbackName)
	})
	return reached, countObserved, func() { releaseOnce.Do(func() { close(releaseCh) }) }
}

func retryTransactionWaitForPostgresBlock(t *testing.T, db *gorm.DB, holderPID int, table, label string) {
	t.Helper()
	if holderPID <= 0 {
		t.Fatalf("invalid PostgreSQL holder PID %d for %s", holderPID, label)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL contention observer for %s: %v", label, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pattern := "%" + table + "%"
	for {
		var waiterPID int
		err := sqlDB.QueryRowContext(ctx, `
			SELECT COALESCE(MIN(pid), 0)
			FROM pg_stat_activity
			WHERE pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND $1 = ANY(pg_blocking_pids(pid))
			  AND query ILIKE $2
			  AND query ILIKE '%FOR UPDATE%'`, holderPID, pattern).Scan(&waiterPID)
		if err != nil {
			t.Fatalf("inspect PostgreSQL %s contention: %v", label, err)
		}
		if waiterPID != 0 {
			if waiterPID == holderPID {
				t.Fatalf("PostgreSQL %s waiter reused holder PID %d", label, holderPID)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for PostgreSQL %s to block on holder PID %d", label, holderPID)
		default:
			runtime.Gosched()
		}
	}
}

func retryTransactionWaitForMarkedAttempt(t *testing.T, reached <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for retry contender %s to reach its DB operation", label)
	}
}

func retryTransactionAwaitResponse(t *testing.T, responses <-chan *httptest.ResponseRecorder, label string) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-responses:
		return response
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s response", label)
		return nil
	}
}

func retryTransactionAwaitError(t *testing.T, results <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s completion", label)
		return nil
	}
}

func retryTransactionAwaitHolderPID(t *testing.T, pids <-chan int, label string) int {
	t.Helper()
	select {
	case pid := <-pids:
		return pid
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s holder PID", label)
		return 0
	}
}

func TestBatchCommandRetrySQLiteBeginProbeAttribution(t *testing.T) {
	probe := newRetrySQLiteBeginProbe()
	db := openMigratedRetryTransactionSQLiteBeginProbeDB(t, probe)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQLite probe database: %v", err)
	}

	marked := "sqlite-begin-probe-attribution"
	probe.arm(marked)
	t.Cleanup(probe.disarm)
	unmarked, err := sqlDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin unmarked SQLite holder: %v", err)
	}
	select {
	case marker := <-probe.begins:
		t.Fatalf("unmarked BEGIN produced probe notification %q", marker)
	default:
	}

	type beginResult struct {
		tx  *stdsql.Tx
		err error
	}
	markedDone := make(chan beginResult, 1)
	markedResultHandled := false
	defer func() {
		if markedResultHandled {
			return
		}
		_ = unmarked.Rollback()
		select {
		case result := <-markedDone:
			if result.tx != nil {
				_ = result.tx.Rollback()
			}
		case <-time.After(10 * time.Second):
			t.Log("marked SQLite contender did not finish during cleanup")
		}
	}()
	go func() {
		tx, beginErr := sqlDB.BeginTx(retryTransactionMarkedContext(marked), nil)
		markedDone <- beginResult{tx: tx, err: beginErr}
	}()
	if got := probe.wait(t, "marked contender"); got != marked {
		t.Fatalf("SQLite begin probe marker=%q, want %q", got, marked)
	}
	if err := unmarked.Rollback(); err != nil {
		t.Fatalf("release unmarked SQLite holder: %v", err)
	}
	select {
	case result := <-markedDone:
		markedResultHandled = true
		if result.err != nil {
			t.Fatalf("marked SQLite contender begin: %v", result.err)
		}
		if result.tx == nil {
			t.Fatal("marked SQLite contender returned nil transaction")
		}
		if err := result.tx.Rollback(); err != nil {
			t.Fatalf("rollback marked SQLite contender: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for marked SQLite contender")
	}
}

func TestBatchCommandRetryActualBatchDeleteContentionSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		for _, order := range []string{"delete_first", "retry_first"} {
			t.Run(order, func(t *testing.T) {
				fx, probe := newMigratedRetryTransactionFixtureWithSQLiteBeginProbe(t, engine)
				root, sourceDispatch, _ := retryTransactionSeedKnownFailureRoot(t, fx)
				retryTransactionSeedGrant(t, fx.db, fx.admin, sourceDispatch.NodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
				token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
				source := []batchRetrySource{{TaskID: sourceDispatch.TaskID, RunID: sourceDispatch.RunID}}
				deleteMarker := fmt.Sprintf("batch-delete-%s-%s-delete", engine, order)
				retryMarker := fmt.Sprintf("batch-delete-%s-%s-retry", engine, order)
				var retryStage *retrySQLiteBeginStage
				if engine == "sqlite" {
					t.Cleanup(probe.disarm)
					if order == "delete_first" {
						// Grant-use auditing uses the base DB context. Stage the
						// marked retry transaction before the DELETE holder so
						// that audit I/O cannot be mistaken for retry contention.
						retryStage = probe.stageBeforeNativeBegin(retryMarker)
					} else {
						probe.arm(retryMarker)
					}
				}

				if order == "delete_first" {
					var retryDone chan *httptest.ResponseRecorder
					if engine == "sqlite" {
						retryDone = make(chan *httptest.ResponseRecorder, 1)
						go func() {
							retryDone <- retryTransactionMarkedRetryRequest(t, fx.router, root.ID, token, proof, "r13-actual-delete-delete-first", source, retryMarker)
						}()
						probe.waitStaging(t, retryStage, "retry behind batch delete")
					}
					deleteGate := newRetryTransactionLockGate(t, fx.db, "tasks", deleteMarker, false)
					deleteDone := make(chan *httptest.ResponseRecorder, 1)
					go func() {
						deleteDone <- retryTransactionMarkedDeleteRequest(t, fx.router, root.ID, token, deleteMarker)
					}()
					retryTransactionWaitForGate(t, deleteGate.reached)
					if engine == "postgres" {
						deletePID := retryTransactionAwaitHolderPID(t, deleteGate.holderPID, "actual batch delete")
						retryDone = make(chan *httptest.ResponseRecorder, 1)
						go func() {
							retryDone <- retryTransactionMarkedRetryRequest(t, fx.router, root.ID, token, proof, "r13-actual-delete-delete-first", source, retryMarker)
						}()
						retryTransactionWaitForPostgresBlock(t, fx.db, deletePID, "batch_commands", "retry behind batch delete")
					} else {
						probe.arm(retryMarker)
						probe.releaseStaging()
						if got := probe.wait(t, "retry behind batch delete"); got != retryMarker {
							t.Fatalf("retry begin marker=%q, want %q", got, retryMarker)
						}
					}
					deleteGate.release()
					deleteResponse := retryTransactionAwaitResponse(t, deleteDone, "actual batch delete winner")
					retryResponse := retryTransactionAwaitResponse(t, retryDone, "retry behind actual batch delete")
					retryTransactionAssertActualBatchDeleteWinner(t, fx, root, sourceDispatch, deleteResponse, retryResponse, true)
					return
				}

				retryGate := newRetryTransactionLockGate(t, fx.db, "batch_commands", retryMarker, true)
				retryDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					retryDone <- retryTransactionMarkedRetryRequest(t, fx.router, root.ID, token, proof, "r13-actual-delete-retry-first", source, retryMarker)
				}()
				retryTransactionWaitForGate(t, retryGate.reached)
				if engine == "postgres" {
					retryPID := retryTransactionAwaitHolderPID(t, retryGate.holderPID, "retry transaction")
					deleteDone := make(chan *httptest.ResponseRecorder, 1)
					go func() {
						deleteDone <- retryTransactionMarkedDeleteRequest(t, fx.router, root.ID, token, deleteMarker)
					}()
					retryTransactionWaitForPostgresBlock(t, fx.db, retryPID, "batch_commands", "batch delete behind retry")
					retryGate.release()
					retryResponse := retryTransactionAwaitResponse(t, retryDone, "retry winner before actual batch delete")
					deleteResponse := retryTransactionAwaitResponse(t, deleteDone, "actual batch delete behind retry")
					retryTransactionAssertActualBatchDeleteWinner(t, fx, root, sourceDispatch, deleteResponse, retryResponse, false)
				} else {
					probe.arm(deleteMarker)
					deleteDone := make(chan *httptest.ResponseRecorder, 1)
					go func() {
						deleteDone <- retryTransactionMarkedDeleteRequest(t, fx.router, root.ID, token, deleteMarker)
					}()
					if got := probe.wait(t, "batch delete behind retry"); got != deleteMarker {
						t.Fatalf("delete begin marker=%q, want %q", got, deleteMarker)
					}
					retryGate.release()
					retryResponse := retryTransactionAwaitResponse(t, retryDone, "retry winner before actual batch delete")
					deleteResponse := retryTransactionAwaitResponse(t, deleteDone, "actual batch delete behind retry")
					retryTransactionAssertActualBatchDeleteWinner(t, fx, root, sourceDispatch, deleteResponse, retryResponse, false)
				}
			})
		}
	})
}

func retryTransactionAssertActualBatchDeleteWinner(t *testing.T, fx *retryTransactionFixture, root model.BatchCommand, sourceDispatch model.BatchCommandDispatch, deleteResponse, retryResponse *httptest.ResponseRecorder, deleteWon bool) {
	t.Helper()
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("actual authenticated batch delete status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	var deletedRoot model.BatchCommand
	if err := fx.db.Unscoped().First(&deletedRoot, "id = ?", root.ID).Error; err != nil {
		t.Fatalf("load deleted source batch: %v", err)
	}
	if deletedRoot.DeletedAt == nil {
		t.Fatalf("actual batch delete did not tombstone source batch: %+v", deletedRoot)
	}
	var sourceTasks, sourceRuns, sourceEvidence int64
	if err := fx.db.Model(&model.Task{}).Where("batch_id = ?", root.ID).Count(&sourceTasks).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Model(&model.TaskRun{}).Where("task_id = ?", sourceDispatch.TaskID).Count(&sourceRuns).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Model(&model.BatchCommandRunEvidence{}).Where("run_id = ?", sourceDispatch.RunID).Count(&sourceEvidence).Error; err != nil {
		t.Fatal(err)
	}
	if sourceTasks != 0 || sourceRuns != 0 || sourceEvidence != 1 {
		t.Fatalf("batch delete cleanup tasks=%d runs=%d permanent_evidence=%d", sourceTasks, sourceRuns, sourceEvidence)
	}

	var childBatches, childTasks, childDispatches, childRuns, sourceClaims int64
	if err := fx.db.Unscoped().Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&childBatches).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Unscoped().Model(&model.Task{}).Where("batch_id IN (SELECT id FROM batch_commands WHERE source_batch_id = ?)", root.ID).Count(&childTasks).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Unscoped().Model(&model.BatchCommandDispatch{}).Where("batch_id IN (SELECT id FROM batch_commands WHERE source_batch_id = ?)", root.ID).Count(&childDispatches).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Unscoped().Model(&model.TaskRun{}).Where("task_id IN (SELECT id FROM tasks WHERE batch_id IN (SELECT id FROM batch_commands WHERE source_batch_id = ?))", root.ID).Count(&childRuns).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Unscoped().Model(&model.BatchCommandDispatch{}).Where("retry_of_task_id = ? AND retry_of_run_id = ?", sourceDispatch.TaskID, sourceDispatch.RunID).Count(&sourceClaims).Error; err != nil {
		t.Fatal(err)
	}
	if deleteWon {
		if retryResponse.Code != http.StatusNotFound {
			t.Fatalf("retry after actual delete status=%d body=%s, want 404", retryResponse.Code, retryResponse.Body.String())
		}
		if childBatches != 0 || childTasks != 0 || childDispatches != 0 || childRuns != 0 || sourceClaims != 0 {
			t.Fatalf("delete-first retry left child identity batches=%d tasks=%d dispatches=%d runs=%d claims=%d", childBatches, childTasks, childDispatches, childRuns, sourceClaims)
		}
		return
	}
	if retryResponse.Code != http.StatusOK {
		t.Fatalf("retry-first response status=%d body=%s", retryResponse.Code, retryResponse.Body.String())
	}
	if childBatches != 1 || childTasks != 1 || childDispatches != 1 || childRuns != 0 || sourceClaims != 1 {
		t.Fatalf("retry-first identity batches=%d tasks=%d dispatches=%d runs=%d claims=%d", childBatches, childTasks, childDispatches, childRuns, sourceClaims)
	}
	var childDispatch model.BatchCommandDispatch
	if err := fx.db.Where("batch_id IN (SELECT id FROM batch_commands WHERE source_batch_id = ?)", root.ID).First(&childDispatch).Error; err != nil {
		t.Fatal(err)
	}
	if childDispatch.RetryOfTaskID == nil || *childDispatch.RetryOfTaskID != sourceDispatch.TaskID || childDispatch.RetryOfRunID == nil || *childDispatch.RetryOfRunID != sourceDispatch.RunID {
		t.Fatalf("retry-first source edge=%+v", childDispatch)
	}
}

func TestBatchCommandRetryProductionEffectConsumerContentionSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		t.Run("effect_consumer_first", func(t *testing.T) {
			fx, probe := newMigratedRetryTransactionFixtureWithSQLiteBeginProbe(t, engine)
			manager, root, sourceDispatch := retryTransactionSeedRealFailureWithPendingEffect(t, fx)
			retryTransactionSeedGrant(t, fx.db, fx.admin, sourceDispatch.NodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
			retryMarker := fmt.Sprintf("effect-consumer-first-%s-retry", engine)
			effectMarker := fmt.Sprintf("effect-consumer-first-%s-effect", engine)
			effectGate := newRetryTransactionLockGate(t, fx.db, "task_run_effects", effectMarker, false)
			var retryStage *retrySQLiteBeginStage
			if engine == "sqlite" {
				t.Cleanup(probe.disarm)
				// Hold the marked retry transaction before native BEGIN so
				// preflight credential-audit writes cannot become the witness.
				retryStage = probe.stageBeforeNativeBegin(retryMarker)
			}
			consumerDone := make(chan error, 1)
			var retryDone chan *httptest.ResponseRecorder
			if engine == "sqlite" {
				retryDone = make(chan *httptest.ResponseRecorder, 1)
				go func() {
					retryDone <- retryTransactionMarkedRetryRequest(t, fx.router, root.ID, token, proof, "r13-effect-consumer-first-overlap", []batchRetrySource{{TaskID: sourceDispatch.TaskID, RunID: sourceDispatch.RunID}}, retryMarker)
				}()
				probe.waitStaging(t, retryStage, "retry against effect consumer")
			}
			go func() {
				consumerDone <- manager.LoadSchedules(retryTransactionMarkedContext(effectMarker))
			}()
			retryTransactionWaitForGate(t, effectGate.reached)
			if engine == "postgres" {
				_ = retryTransactionAwaitHolderPID(t, effectGate.holderPID, "production effect consumer")
			}
			var retryEffectsObserved <-chan struct{}
			var retryEffectsCount <-chan int64
			var releaseRetryEffects func()
			if engine == "sqlite" {
				probe.arm(retryMarker)
				_, retryEffectsCount, releaseRetryEffects = retryTransactionInstallRetryEffectObservationGate(t, fx.db, retryMarker, false)
				probe.releaseStaging()
				if got := probe.wait(t, "retry against effect consumer"); got != retryMarker {
					t.Fatalf("retry begin marker=%q, want %q", got, retryMarker)
				}
				// The SQLite authorizer fires when the contender prepares its
				// BEGIN IMMEDIATE; release the real consumer transaction only
				// after that boundary is observed.
				effectGate.release()
			} else {
				retryEffectsObserved, retryEffectsCount, releaseRetryEffects = retryTransactionInstallRetryEffectObservationGate(t, fx.db, retryMarker, true)
				retryDone = make(chan *httptest.ResponseRecorder, 1)
				go func() {
					retryDone <- retryTransactionMarkedRetryRequest(t, fx.router, root.ID, token, proof, "r13-effect-consumer-first-overlap", []batchRetrySource{{TaskID: sourceDispatch.TaskID, RunID: sourceDispatch.RunID}}, retryMarker)
				}()
				retryTransactionWaitForMarkedAttempt(t, retryEffectsObserved, "effect consumer pending-effect observation")
			}
			retryPending := int64(0)
			select {
			case retryPending = <-retryEffectsCount:
			case <-time.After(10 * time.Second):
				t.Fatalf("timed out waiting for retry pending-effect count")
			}
			if engine == "postgres" && retryPending == 0 {
				t.Fatal("PostgreSQL retry did not observe the pending effect while the production claim was held")
			}
			releaseRetryEffects()
			retryResponse := retryTransactionAwaitResponse(t, retryDone, "retry overlapping production effect recovery")
			wantRetryStatus := http.StatusOK
			if retryPending > 0 {
				wantRetryStatus = http.StatusConflict
			}
			if retryResponse.Code != wantRetryStatus {
				t.Fatalf("retry observed pending production effect count=%d status=%d body=%s, want %d", retryPending, retryResponse.Code, retryResponse.Body.String(), wantRetryStatus)
			}
			if engine == "postgres" {
				// Release the retry observation before the effect claim: the
				// consumer's alert dispatcher reads the source Task, so holding
				// the retry transaction open would create a test-only cycle.
				effectGate.release()
			}
			if err := retryTransactionAwaitError(t, consumerDone, "production effect recovery"); err != nil {
				t.Fatalf("production effect recovery: %v", err)
			}
			var effectCount int64
			if err := fx.db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND status <> ?", sourceDispatch.RunID, model.TaskRunEffectStatusSucceeded).Count(&effectCount).Error; err != nil {
				t.Fatal(err)
			}
			if effectCount != 0 {
				t.Fatalf("production effect consumer left %d unfinished effects", effectCount)
			}
			var evidenceCount int64
			if err := fx.db.Model(&model.BatchCommandRunEvidence{}).Where("run_id = ?", sourceDispatch.RunID).Count(&evidenceCount).Error; err != nil {
				t.Fatal(err)
			}
			if evidenceCount != 1 {
				t.Fatalf("production effect recovery changed permanent evidence count=%d, want one", evidenceCount)
			}
			if retryPending > 0 {
				// A pending effect was observed in the overlapping request, so
				// the source remains single-consumed; only a later explicit
				// request may create its child after recovery settles.
				freshToken, freshProof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
				retryResponse = retryTransactionRequest(t, fx.router, root.ID, freshToken, freshProof, "r13-effect-consumer-first-recovered", []batchRetrySource{{TaskID: sourceDispatch.TaskID, RunID: sourceDispatch.RunID}})
				retryTransactionAssertChild(t, fx, root, sourceDispatch, retryResponse, freshToken, "r13-effect-consumer-first-recovered")
			} else {
				retryTransactionAssertChild(t, fx, root, sourceDispatch, retryResponse, token, "r13-effect-consumer-first-overlap")
			}
			var children, claims, childRuns, sourceRuns int64
			if err := fx.db.Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&children).Error; err != nil {
				t.Fatal(err)
			}
			if err := fx.db.Model(&model.BatchCommandDispatch{}).Where("retry_of_task_id = ? AND retry_of_run_id = ?", sourceDispatch.TaskID, sourceDispatch.RunID).Count(&claims).Error; err != nil {
				t.Fatal(err)
			}
			if err := fx.db.Model(&model.TaskRun{}).Where("task_id = ?", sourceDispatch.TaskID).Count(&sourceRuns).Error; err != nil {
				t.Fatal(err)
			}
			if err := fx.db.Model(&model.TaskRun{}).Where("task_id IN (SELECT id FROM tasks WHERE batch_id IN (SELECT id FROM batch_commands WHERE source_batch_id = ?))", root.ID).Count(&childRuns).Error; err != nil {
				t.Fatal(err)
			}
			if children != 1 || claims != 1 || childRuns != 0 || sourceRuns != 1 {
				t.Fatalf("effect-first recovered retry children=%d claims=%d child_runs=%d source_runs=%d, want one child/claim and one source run", children, claims, childRuns, sourceRuns)
			}
		})

		t.Run("retry_first_pending_effect_consumer", func(t *testing.T) {
			fx, probe := newMigratedRetryTransactionFixtureWithSQLiteBeginProbe(t, engine)
			manager, root, sourceDispatch := retryTransactionSeedRealFailureWithPendingEffect(t, fx)
			retryTransactionSeedGrant(t, fx.db, fx.admin, sourceDispatch.NodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
			retryMarker := fmt.Sprintf("retry-first-effect-consumer-%s-retry", engine)
			effectMarker := fmt.Sprintf("retry-first-effect-consumer-%s-effect", engine)
			retryGate := newRetryTransactionLockGate(t, fx.db, "tasks", retryMarker, false)
			if engine == "sqlite" {
				probe.arm(retryMarker)
				t.Cleanup(probe.disarm)
			}
			retryDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				retryDone <- retryTransactionMarkedRetryRequest(t, fx.router, root.ID, token, proof, "r13-retry-first-effect-consumer", []batchRetrySource{{TaskID: sourceDispatch.TaskID, RunID: sourceDispatch.RunID}}, retryMarker)
			}()
			retryTransactionWaitForGate(t, retryGate.reached)
			effectGate := newRetryTransactionLockGate(t, fx.db, "task_run_effects", effectMarker, false)
			consumerDone := make(chan error, 1)
			if engine == "postgres" {
				retryPID := retryTransactionAwaitHolderPID(t, retryGate.holderPID, "retry transaction")
				go func() { consumerDone <- manager.LoadSchedules(retryTransactionMarkedContext(effectMarker)) }()
				retryTransactionWaitForGate(t, effectGate.reached)
				effectPID := retryTransactionAwaitHolderPID(t, effectGate.holderPID, "production effect consumer")
				if retryPID == effectPID {
					t.Fatalf("retry and production effect consumer reused PostgreSQL backend PID %d", retryPID)
				}
				retryGate.release()
				retryResponse := retryTransactionAwaitResponse(t, retryDone, "retry behind pending effect consumer")
				if retryResponse.Code != http.StatusConflict {
					t.Fatalf("retry behind pending production effect status=%d body=%s, want 409", retryResponse.Code, retryResponse.Body.String())
				}
				effectGate.release()
				if err := retryTransactionAwaitError(t, consumerDone, "production effect consumer after retry"); err != nil {
					t.Fatalf("production effect consumer after retry: %v", err)
				}
			} else {
				probe.arm(effectMarker)
				go func() { consumerDone <- manager.LoadSchedules(retryTransactionMarkedContext(effectMarker)) }()
				if got := probe.wait(t, "production effect consumer behind retry"); got != effectMarker {
					t.Fatalf("production effect consumer SQLite marker=%q, want %q", got, effectMarker)
				}
				retryGate.release()
				retryResponse := retryTransactionAwaitResponse(t, retryDone, "retry behind pending effect consumer")
				if retryResponse.Code != http.StatusConflict {
					t.Fatalf("retry behind pending production effect status=%d body=%s, want 409", retryResponse.Code, retryResponse.Body.String())
				}
				retryTransactionWaitForGate(t, effectGate.reached)
				effectGate.release()
				if err := retryTransactionAwaitError(t, consumerDone, "production effect consumer after retry"); err != nil {
					t.Fatalf("production effect consumer after retry: %v", err)
				}
			}
			var pending int64
			if err := fx.db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND status <> ?", sourceDispatch.RunID, model.TaskRunEffectStatusSucceeded).Count(&pending).Error; err != nil {
				t.Fatal(err)
			}
			if pending != 0 {
				t.Fatalf("reverse-order production effect consumer left %d unfinished effects", pending)
			}
			freshToken, freshProof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
			response := retryTransactionRequest(t, fx.router, root.ID, freshToken, freshProof, "r13-retry-first-effect-consumer-recovered", []batchRetrySource{{TaskID: sourceDispatch.TaskID, RunID: sourceDispatch.RunID}})
			retryTransactionAssertChild(t, fx, root, sourceDispatch, response, freshToken, "r13-retry-first-effect-consumer-recovered")
			var children, claims, sourceRuns, childRuns int64
			if err := fx.db.Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&children).Error; err != nil {
				t.Fatal(err)
			}
			if err := fx.db.Model(&model.BatchCommandDispatch{}).Where("retry_of_task_id = ? AND retry_of_run_id = ?", sourceDispatch.TaskID, sourceDispatch.RunID).Count(&claims).Error; err != nil {
				t.Fatal(err)
			}
			if err := fx.db.Model(&model.TaskRun{}).Where("task_id = ?", sourceDispatch.TaskID).Count(&sourceRuns).Error; err != nil {
				t.Fatal(err)
			}
			if err := fx.db.Model(&model.TaskRun{}).Where("task_id IN (SELECT id FROM tasks WHERE batch_id IN (SELECT id FROM batch_commands WHERE source_batch_id = ?))", root.ID).Count(&childRuns).Error; err != nil {
				t.Fatal(err)
			}
			if children != 1 || claims != 1 || sourceRuns != 1 || childRuns != 0 {
				t.Fatalf("reverse-order effect/retry children=%d claims=%d source_runs=%d child_runs=%d", children, claims, sourceRuns, childRuns)
			}
			var evidenceCount int64
			if err := fx.db.Model(&model.BatchCommandRunEvidence{}).Where("run_id = ?", sourceDispatch.RunID).Count(&evidenceCount).Error; err != nil {
				t.Fatal(err)
			}
			if evidenceCount != 1 {
				t.Fatalf("reverse-order effect/retry permanent evidence count=%d, want one", evidenceCount)
			}
		})
	})
}
