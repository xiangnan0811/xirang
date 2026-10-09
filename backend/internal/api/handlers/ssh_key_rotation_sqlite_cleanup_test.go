package handlers

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

type sshKeyRotationSQLiteGate struct {
	ch   chan struct{}
	once sync.Once
}

func newSSHKeyRotationSQLiteGate() *sshKeyRotationSQLiteGate {
	return &sshKeyRotationSQLiteGate{ch: make(chan struct{})}
}

func (gate *sshKeyRotationSQLiteGate) release() {
	gate.once.Do(func() { close(gate.ch) })
}

func waitSSHKeyRotationSQLiteGate(t *testing.T, gate <-chan struct{}, label string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-gate:
	case <-timer.C:
		t.Fatalf("timed out waiting for SQLite %s barrier", label)
	}
}

func checkoutSSHKeyRotationSQLiteConn(t *testing.T, db *gorm.DB, configure func(*sqlite3.SQLiteConn)) *sqlite3.SQLiteConn {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQLite pool: %v", err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("checkout SQLite connection: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("return SQLite connection: %v", err)
		}
	}()

	var physical *sqlite3.SQLiteConn
	if err := conn.Raw(func(driverConn any) error {
		var ok bool
		physical, ok = driverConn.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("SQLite pool returned an unexpected driver connection")
		}
		if configure != nil {
			configure(physical)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect SQLite connection: %v", err)
	}
	return physical
}

func readSSHKeyRotationSQLiteBusyTimeout(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var busyTimeout int64
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("read SQLite busy timeout: %v", err)
	}
	return busyTimeout
}

func assertSSHKeyRotationSQLiteFreshTransaction(t *testing.T, db *gorm.DB) {
	t.Helper()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin fresh SQLite transaction: %v", tx.Error)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatalf("rollback fresh SQLite transaction: %v", err)
	}
}

func assertSSHKeyRotationSQLiteBusinessError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("rotation helper unexpectedly succeeded")
	}
	for _, businessErr := range []error{
		errSSHKeyRotationBusy,
		errSSHKeyRotationConflict,
		errSSHKeyRotationScopeBlocked,
		errSSHKeyRotationSessionInvalid,
	} {
		if errors.Is(err, businessErr) {
			t.Fatalf("rotation helper classified cleanup failure as %v: %v", businessErr, err)
		}
	}
}

func prepareSSHKeyRotationSQLiteCommit(t *testing.T, db *gorm.DB, oldPrivate, candidatePrivate string) (model.SSHKey, model.SSHKey, sshKeyRotationSnapshot, rotationTestSession) {
	t.Helper()
	key := seedRotationKey(t, db, oldPrivate)
	session := seedRotationSession(t, db)
	current := readRotationKey(t, db, key.ID)
	candidate, _, err := prepareSSHKeyRotationCandidate(current, sshKeyRotationRequest{
		PrivateKey: candidatePrivate,
		KeyType:    sshutil.SSHKeyTypeAuto,
	})
	if err != nil {
		t.Fatalf("prepare SQLite rotation candidate: %v", err)
	}
	return key, candidate, buildSSHKeyRotationSnapshot(current, nil), session
}

func TestSSHKeyRotationSQLiteCleanupCancellationAfterUpdate(t *testing.T) {
	db, _ := openSSHKeyRotationProductionSQLiteDB(t)
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	key, candidate, snapshot, session := prepareSSHKeyRotationSQLiteCommit(t, db, oldPrivate, candidatePrivate)
	before := checkoutSSHKeyRotationSQLiteConn(t, db, nil)
	originalBusyTimeout := readSSHKeyRotationSQLiteBusyTimeout(t, db)
	if originalBusyTimeout != 5000 {
		t.Fatalf("production SQLite busy timeout=%d want 5000", originalBusyTimeout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updateReached := make(chan struct{})
	callbackRelease := newSSHKeyRotationSQLiteGate()
	callbackReturned := make(chan struct{})
	rollbackReached := make(chan struct{})
	rollbackRelease := newSSHKeyRotationSQLiteGate()
	var updateSeen, callbackDone, rollbackSeen sync.Once
	var rollbackCalls atomic.Int32

	callbackName := "test:ssh-key-rotation-sqlite-cancel-after-update"
	if err := db.Callback().Update().After("gorm:after_update").Register(callbackName, func(tx *gorm.DB) {
		updateSeen.Do(func() { close(updateReached) })
		cancel()
		<-callbackRelease.ch
		_ = tx.AddError(context.Canceled)
		callbackDone.Do(func() { close(callbackReturned) })
	}); err != nil {
		t.Fatalf("register scoped update callback: %v", err)
	}
	defer func() {
		callbackRelease.release()
		rollbackRelease.release()
		if err := db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove scoped update callback: %v", err)
		}
	}()

	physical := checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
		conn.RegisterRollbackHook(func() {
			rollbackCalls.Add(1)
			rollbackSeen.Do(func() { close(rollbackReached) })
			<-rollbackRelease.ch
		})
	})
	if physical != before {
		t.Fatalf("rollback hook installed on different physical SQLite connection")
	}

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- commitSSHKeyRotation(ctx, db, session.Binding, snapshot, candidate)
	}()
	waitSSHKeyRotationSQLiteGate(t, updateReached, "real update")
	waitSSHKeyRotationSQLiteGate(t, rollbackReached, "driver rollback")

	// The async database/sql rollback has claimed the transaction while its
	// driver hook is gated. Let the scoped callback return so the helper's
	// explicit Rollback sees sql.ErrTxDone, then release the driver hook.
	callbackRelease.release()
	waitSSHKeyRotationSQLiteGate(t, callbackReturned, "after-update callback")
	rollbackRelease.release()
	commitErr := <-resultCh
	assertSSHKeyRotationSQLiteBusinessError(t, commitErr)
	if rollbackCalls.Load() != 1 {
		t.Fatalf("SQLite rollback hook calls=%d want exactly one async rollback", rollbackCalls.Load())
	}
	assertRotationNotSaved(t, db, key.ID, oldPrivate, key.Name)

	after := checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
		conn.RegisterRollbackHook(nil)
	})
	busyTimeout := readSSHKeyRotationSQLiteBusyTimeout(t, db)
	if after == physical {
		if busyTimeout != originalBusyTimeout {
			t.Fatalf("same physical SQLite connection busy timeout=%d want %d", busyTimeout, originalBusyTimeout)
		}
	} else if busyTimeout != 5000 {
		t.Fatalf("replacement SQLite connection busy timeout=%d want production value 5000", busyTimeout)
	}
	assertSSHKeyRotationSQLiteFreshTransaction(t, db)
}

func TestSSHKeyRotationSQLiteCleanupSuccessfulRestoration(t *testing.T) {
	db, _ := openSSHKeyRotationProductionSQLiteDB(t)
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	key, candidate, snapshot, session := prepareSSHKeyRotationSQLiteCommit(t, db, oldPrivate, candidatePrivate)
	before := checkoutSSHKeyRotationSQLiteConn(t, db, nil)
	originalBusyTimeout := readSSHKeyRotationSQLiteBusyTimeout(t, db)
	if originalBusyTimeout != 5000 {
		t.Fatalf("production SQLite busy timeout=%d want 5000", originalBusyTimeout)
	}

	if err := commitSSHKeyRotation(context.Background(), db, session.Binding, snapshot, candidate); err != nil {
		t.Fatalf("successful SQLite rotation: %v", err)
	}
	if got := readRotationKey(t, db, key.ID); got.PrivateKey != candidate.PrivateKey || got.Fingerprint != candidate.Fingerprint {
		t.Fatalf("successful SQLite rotation did not persist candidate key")
	}
	after := checkoutSSHKeyRotationSQLiteConn(t, db, nil)
	if after != before {
		t.Fatalf("successful SQLite rotation replaced physical connection")
	}
	if busyTimeout := readSSHKeyRotationSQLiteBusyTimeout(t, db); busyTimeout != originalBusyTimeout {
		t.Fatalf("successful SQLite rotation busy timeout=%d want %d", busyTimeout, originalBusyTimeout)
	}
	assertSSHKeyRotationSQLiteFreshTransaction(t, db)
}

func TestSSHKeyRotationSQLiteCleanupRestorationFailureDiscardsConnection(t *testing.T) {
	db, _ := openSSHKeyRotationProductionSQLiteDB(t)
	oldPrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	candidatePrivate := buildSSHKeyPrivateKeyForHandlerTest(t)
	key, candidate, snapshot, session := prepareSSHKeyRotationSQLiteCommit(t, db, oldPrivate, candidatePrivate)
	before := checkoutSSHKeyRotationSQLiteConn(t, db, nil)
	originalBusyTimeout := readSSHKeyRotationSQLiteBusyTimeout(t, db)
	if originalBusyTimeout != 5000 {
		t.Fatalf("production SQLite busy timeout=%d want 5000", originalBusyTimeout)
	}

	var armed atomic.Bool
	var pragmaReads atomic.Int32
	var pragmaZeroWrites atomic.Int32
	var deniedRestores atomic.Int32
	if physical := checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
		conn.RegisterAuthorizer(func(op int, arg1, arg2, _ string) int {
			if op != sqlite3.SQLITE_PRAGMA || !strings.EqualFold(arg1, "busy_timeout") {
				// COMMIT and every non-target action remain allowed.
				return sqlite3.SQLITE_OK
			}
			switch strings.TrimSpace(arg2) {
			case "":
				pragmaReads.Add(1)
			case "0":
				pragmaZeroWrites.Add(1)
			case strconv.FormatInt(originalBusyTimeout, 10):
				if armed.Load() {
					deniedRestores.Add(1)
					return sqlite3.SQLITE_DENY
				}
			}
			return sqlite3.SQLITE_OK
		})
	}); physical != before {
		t.Fatalf("authorizer installed on different physical SQLite connection")
	}

	callbackName := "test:ssh-key-rotation-sqlite-arm-restore-denial"
	if err := db.Callback().Update().After("gorm:after_update").Register(callbackName, func(*gorm.DB) {
		armed.Store(true)
	}); err != nil {
		t.Fatalf("register restoration-denial callback: %v", err)
	}
	defer func() {
		if err := db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove restoration-denial callback: %v", err)
		}
	}()

	commitErr := commitSSHKeyRotation(context.Background(), db, session.Binding, snapshot, candidate)
	assertSSHKeyRotationSQLiteBusinessError(t, commitErr)
	if !armed.Load() {
		t.Fatal("restoration-denial callback did not observe the real key update")
	}
	if pragmaReads.Load() == 0 || pragmaZeroWrites.Load() == 0 || deniedRestores.Load() == 0 {
		t.Fatalf("authorizer did not observe read/set-zero/denied-restore PRAGMAs: reads=%d set_zero=%d denied=%d", pragmaReads.Load(), pragmaZeroWrites.Load(), deniedRestores.Load())
	}
	if got := readRotationKey(t, db, key.ID); got.PrivateKey != candidate.PrivateKey || got.Fingerprint != candidate.Fingerprint {
		t.Fatalf("restoration failure did not preserve committed key update")
	}
	after := checkoutSSHKeyRotationSQLiteConn(t, db, nil)
	if after == before {
		t.Fatal("restoration failure returned the original physical SQLite connection")
	}
	if busyTimeout := readSSHKeyRotationSQLiteBusyTimeout(t, db); busyTimeout != 5000 {
		t.Fatalf("replacement SQLite connection busy timeout=%d want production value 5000", busyTimeout)
	}
	assertSSHKeyRotationSQLiteFreshTransaction(t, db)
}
