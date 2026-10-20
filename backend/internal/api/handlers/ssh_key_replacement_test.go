package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"
)

const replacementJWTSecret = "FAKE_REPLACEMENT_JWT_SECRET_FOR_TEST_ONLY"

type replacementFixture struct {
	db      *gorm.DB
	manager *auth.JWTManager
	session rotationTestSession
	key     model.SSHKey
	old     string
	router  *gin.Engine
}

func newReplacementFixture(t *testing.T, db *gorm.DB) replacementFixture {
	t.Helper()
	user := model.User{
		Username: "replacement-admin", PasswordHash: "FAKE_REPLACEMENT_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "admin", TokenVersion: 7, TOTPEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create replacement user: %v", err)
	}
	session := rotationTestSession{User: user, Binding: middleware.SessionBinding{
		JTI: strings.Repeat("b", 32), UserID: user.ID, Role: user.Role,
		TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}}
	// Deliberately omit SetDB: the handler must use its supplied transaction,
	// not open a second connection through the manager's root database.
	manager := auth.NewJWTManager(replacementJWTSecret, time.Hour)
	old := buildSSHKeyPrivateKeyForHandlerTest(t)
	key := seedRotationKeyNamed(t, db, old, "replacement-original")
	router := replacementRouter(db, manager, session)
	return replacementFixture{db: db, manager: manager, session: session, key: key, old: old, router: router}
}

func replacementRouter(db *gorm.DB, manager *auth.JWTManager, session rotationTestSession) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, session.User.ID)
		c.Set(middleware.CtxRole, session.User.Role)
		c.Set(middleware.CtxSessionBinding, session.Binding)
		c.Next()
	})
	handler := NewSSHKeyHandler(db, manager)
	router.PUT("/ssh-keys/:id", handler.Update)
	router.POST("/ssh-keys/:id/rotate", handler.Rotate)
	return router
}

func replacementProof(t *testing.T, f replacementFixture) string {
	t.Helper()
	proof, _, err := f.manager.GenerateStepUpToken(f.session.User, auth.StepUpActionSSHKeyReplace, f.session.Binding.JTI)
	if err != nil {
		t.Fatalf("generate replacement proof: %v", err)
	}
	return proof
}

func replacementExpiringProof(t *testing.T, f replacementFixture, expires time.Time) string {
	t.Helper()
	claims, err := f.manager.ParseToken(replacementProof(t, f))
	if err != nil {
		t.Fatalf("parse replacement proof fixture: %v", err)
	}
	claims.ExpiresAt = jwt.NewNumericDate(expires)
	claims.IssuedAt = jwt.NewNumericDate(claims.ExpiresAt.Add(-auth.StepUpProofTTLForAction(auth.StepUpActionSSHKeyReplace)))
	proof, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(replacementJWTSecret))
	if err != nil {
		t.Fatalf("sign expiring replacement proof: %v", err)
	}
	return proof
}

func replacementRequest(f replacementFixture, method string, body []byte, proof string) *httptest.ResponseRecorder {
	path := fmt.Sprintf("/ssh-keys/%d", f.key.ID)
	if method == http.MethodPost {
		path += "/rotate"
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if proof != "" {
		req.Header.Set("X-Xirang-Step-Up", proof)
	}
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, req)
	return response
}

func replacementUpdateBody(t *testing.T, name string, privateKey *string) []byte {
	t.Helper()
	body := map[string]any{"name": name, "username": "root", "key_type": "auto"}
	if privateKey != nil {
		body["private_key"] = *privateKey
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal replacement update: %v", err)
	}
	return encoded
}

func replacementCiphertext(t *testing.T, db *gorm.DB, keyID uint) string {
	t.Helper()
	var value string
	if err := db.Model(&model.SSHKey{}).Where("id = ?", keyID).Pluck("private_key", &value).Error; err != nil {
		t.Fatalf("read replacement ciphertext: %v", err)
	}
	return value
}

func awaitReplacementResponse(t *testing.T, responses <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-responses:
		return response
	case <-time.After(10 * time.Second):
		t.Fatal("replacement request did not finish")
		return nil
	}
}

func runReplacementMetadataSecretOrderings(t *testing.T, engine string) {
	t.Helper()
	for _, secretFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("secret_first_%t", secretFirst), func(t *testing.T) {
			f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, engine))
			// A bound, deliberately offline node proves PUT never inherits the
			// rotation connectivity requirement. PostgreSQL inventory is nonempty.
			seedRotationNode(t, f.db, f.key.ID, "replacement-offline", "127.0.0.1", 1, false)
			used := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
			if err := f.db.Model(&model.SSHKey{}).Where("id = ?", f.key.ID).Update("last_used_at", used).Error; err != nil {
				t.Fatalf("set usage timestamp: %v", err)
			}
			candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
			metadata := func() {
				before := replacementCiphertext(t, f.db, f.key.ID)
				response := replacementRequest(f, http.MethodPut, replacementUpdateBody(t, "metadata-final", nil), "invalid-but-unneeded-proof")
				if response.Code != http.StatusOK {
					t.Fatalf("metadata status=%d", response.Code)
				}
				if replacementCiphertext(t, f.db, f.key.ID) != before {
					t.Fatal("metadata update rewrote secret ciphertext")
				}
			}
			secret := func() {
				response := replacementRequest(f, http.MethodPut, replacementUpdateBody(t, "secret-final", &candidate), replacementProof(t, f))
				if response.Code != http.StatusOK {
					t.Fatalf("replacement status=%d", response.Code)
				}
			}
			if secretFirst {
				secret()
				metadata()
			} else {
				metadata()
				secret()
			}
			stored := readRotationKey(t, f.db, f.key.ID)
			wantName := "secret-final"
			if secretFirst {
				wantName = "metadata-final"
			}
			if !sshutil.SamePrivateKeyMaterial(stored.PrivateKey, candidate) || stored.Name != wantName || stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(used) {
				t.Fatal("metadata/secret ordering lost the latest secret, name, or usage timestamp")
			}
		})
	}
}

func TestSSHKeyReplacementMetadataSecretOrderings(t *testing.T) {
	runReplacementMetadataSecretOrderings(t, "sqlite")
}

func TestSSHKeyReplacementSQLiteWriterReservationNoRetry(t *testing.T) {
	db, lockDB := openSSHKeyRotationProductionSQLiteDB(t)
	f := newReplacementFixture(t, db)
	lock := lockDB.Begin()
	if lock.Error != nil {
		t.Fatalf("begin competing writer: %v", lock.Error)
	}
	defer func() { _ = lock.Rollback().Error }()
	if err := lock.Exec("UPDATE ssh_keys SET id = id WHERE 1 = 0").Error; err != nil {
		t.Fatalf("reserve competing writer: %v", err)
	}
	var begins, calls atomic.Int32
	checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
		conn.RegisterAuthorizer(func(op int, arg1, _, _ string) int {
			if op == sqlite3.SQLITE_TRANSACTION && strings.EqualFold(arg1, "BEGIN") {
				begins.Add(1)
			}
			return sqlite3.SQLITE_OK
		})
	})
	started := time.Now()
	err := withSSHKeyMutationTx(context.Background(), db, func(*gorm.DB) error {
		calls.Add(1)
		return nil
	})
	if !errors.Is(err, errSSHKeyMutationBusy) || begins.Load() != 1 || calls.Load() != 0 || time.Since(started) > 2*time.Second {
		t.Fatalf("writer contention must fail once before callback: busy=%t begins=%d callbacks=%d", errors.Is(err, errSSHKeyMutationBusy), begins.Load(), calls.Load())
	}
	if err := lock.Rollback().Error; err != nil {
		t.Fatalf("release competing writer: %v", err)
	}
	checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) { conn.RegisterAuthorizer(nil) })
	assertRotationNotSaved(t, db, f.key.ID, f.old, f.key.Name)
	if got := readSSHKeyRotationSQLiteBusyTimeout(t, db); got != 5000 {
		t.Fatalf("busy timeout=%d want 5000", got)
	}
}

func TestSSHKeyReplacementSQLiteBusyCallbackNoRetry(t *testing.T) {
	db, _ := openSSHKeyRotationProductionSQLiteDB(t)
	f := newReplacementFixture(t, db)
	var attempts atomic.Int32
	const callback = "test:replacement-update-busy"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "ssh_keys" {
			attempts.Add(1)
			_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrBusy})
		}
	}); err != nil {
		t.Fatalf("register busy callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	response := replacementRequest(f, http.MethodPut, replacementUpdateBody(t, "must-not-save", nil), "")
	if response.Code != http.StatusConflict || attempts.Load() != 1 {
		t.Fatalf("busy update status=%d attempts=%d", response.Code, attempts.Load())
	}
	assertRotationNotSaved(t, db, f.key.ID, f.old, f.key.Name)
}

func TestSSHKeyReplacementSQLiteUncertainIsNotChallenge(t *testing.T) {
	for _, fault := range []string{"commit", "rollback", "restore_after_rejection", "restore_after_commit"} {
		t.Run(fault, func(t *testing.T) {
			db, _ := openSSHKeyRotationProductionSQLiteDB(t)
			f := newReplacementFixture(t, db)
			candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
			var faultCalls atomic.Int32
			var finalized atomic.Bool
			before := checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
				if fault == "commit" {
					conn.RegisterCommitHook(func() int { faultCalls.Add(1); return 1 })
				}
				conn.RegisterAuthorizer(func(op int, arg1, arg2, _ string) int {
					if op == sqlite3.SQLITE_TRANSACTION && (strings.EqualFold(arg1, "ROLLBACK") || strings.EqualFold(arg1, "COMMIT")) {
						finalized.Store(true)
						if fault == "rollback" && strings.EqualFold(arg1, "ROLLBACK") {
							faultCalls.Add(1)
							return sqlite3.SQLITE_DENY
						}
					}
					if strings.HasPrefix(fault, "restore_") && finalized.Load() && op == sqlite3.SQLITE_PRAGMA && strings.EqualFold(arg1, "busy_timeout") && strings.TrimSpace(arg2) == "5000" {
						faultCalls.Add(1)
						return sqlite3.SQLITE_DENY
					}
					return sqlite3.SQLITE_OK
				})
			})
			proof := ""
			if fault == "commit" || fault == "restore_after_commit" {
				proof = replacementProof(t, f)
			}
			response := replacementRequest(f, http.MethodPut, replacementUpdateBody(t, "uncertain-update", &candidate), proof)
			if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), stepUpRequiredCode) || faultCalls.Load() == 0 {
				t.Fatalf("uncertain cleanup must not challenge: status=%d injected=%d", response.Code, faultCalls.Load())
			}
			after := checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
				conn.RegisterAuthorizer(nil)
				conn.RegisterCommitHook(nil)
			})
			if fault != "commit" && after == before {
				t.Fatal("unrestorable/unterminated connection was returned to the pool")
			}
			if fault == "restore_after_commit" {
				if !sshutil.SamePrivateKeyMaterial(readRotationKey(t, db, f.key.ID).PrivateKey, candidate) {
					t.Fatal("post-commit cleanup failure lost committed replacement")
				}
			} else {
				assertRotationNotSaved(t, db, f.key.ID, f.old, f.key.Name)
			}
			assertSSHKeyRotationSQLiteFreshTransaction(t, db)
		})
	}
}

// waitReplacementPostgresWaiter observes the actual blocker PID, not an assumed
// goroutine ordering. A ticker is only used to sample the database lock state.
func waitReplacementPostgresWaiter(t *testing.T, db *gorm.DB, holderPID int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int64
		if err := db.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND ? = ANY(pg_blocking_pids(pid))", holderPID).Scan(&count).Error; err != nil {
			t.Fatalf("inspect replacement lock wait: %v", err)
		}
		if count > 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("replacement did not reach held PostgreSQL lock")
		}
	}
}

func holdReplacementPostgresKey(t *testing.T, f replacementFixture) (*sql.Tx, int) {
	t.Helper()
	pool, err := f.db.DB()
	if err != nil {
		t.Fatalf("get replacement pool: %v", err)
	}
	tx, err := pool.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin replacement lock holder: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var pid, id int
	if err := tx.QueryRow("SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("get holder PID: %v", err)
	}
	if err := tx.QueryRow("SELECT id FROM ssh_keys WHERE id = $1 FOR UPDATE", f.key.ID).Scan(&id); err != nil {
		t.Fatalf("lock replacement key: %v", err)
	}
	return tx, pid
}

func replacementWriteSecret(t *testing.T, tx *sql.Tx, keyID uint, privateKey string) {
	t.Helper()
	encrypted, err := secure.EncryptIfNeeded(privateKey)
	if err != nil {
		t.Fatalf("encrypt concurrent replacement: %v", err)
	}
	if _, err := tx.Exec("UPDATE ssh_keys SET private_key = $1, fingerprint = $2 WHERE id = $3", encrypted, generateFingerprint(privateKey), keyID); err != nil {
		t.Fatalf("write concurrent replacement: %v", err)
	}
}

func runReplacementPostgresCurrentRecord(t *testing.T) {
	for _, candidateSupplied := range []bool{false, true} {
		t.Run(fmt.Sprintf("candidate_supplied_%t", candidateSupplied), func(t *testing.T) {
			f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, "postgres"))
			seedRotationNode(t, f.db, f.key.ID, "current-record-offline", "127.0.0.1", 1, false)
			holder, pid := holdReplacementPostgresKey(t, f)
			var candidate *string
			if candidateSupplied {
				candidate = &f.old // Same before the wait, different after it.
			}
			body := replacementUpdateBody(t, "waiter-metadata", candidate)
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() { responses <- replacementRequest(f, http.MethodPut, body, "") }()
			waitReplacementPostgresWaiter(t, f.db, pid)
			concurrent := buildSSHKeyPrivateKeyForHandlerTest(t)
			replacementWriteSecret(t, holder, f.key.ID, concurrent)
			if err := holder.Commit(); err != nil {
				t.Fatalf("commit preceding replacement: %v", err)
			}
			ciphertext := replacementCiphertext(t, f.db, f.key.ID)
			response := awaitReplacementResponse(t, responses)
			stored := readRotationKey(t, f.db, f.key.ID)
			if candidateSupplied {
				assertStepUpRequiredEnvelopeForAction(t, response, auth.StepUpActionSSHKeyReplace)
				if stored.Name != f.key.Name {
					t.Fatal("proof rejection committed accompanying metadata")
				}
			} else if response.Code != http.StatusOK || stored.Name != "waiter-metadata" {
				t.Fatalf("metadata waiter status=%d", response.Code)
			}
			if !sshutil.SamePrivateKeyMaterial(stored.PrivateKey, concurrent) || replacementCiphertext(t, f.db, f.key.ID) != ciphertext {
				t.Fatal("waiting PUT overwrote or reencrypted concurrent secret")
			}
		})
	}
}

func replacementIdentityMutations() []struct {
	name   string
	status int
	mutate func(*testing.T, replacementFixture)
} {
	return []struct {
		name   string
		status int
		mutate func(*testing.T, replacementFixture)
	}{
		{"TOTP disabled", http.StatusForbidden, func(t *testing.T, f replacementFixture) {
			replacementUpdateUser(t, f, "totp_enabled", false)
		}},
		{"reenrollment", http.StatusUnauthorized, func(t *testing.T, f replacementFixture) {
			replacementUpdateUser(t, f, "totp_reenrollment_required", true)
		}},
		{"version", http.StatusUnauthorized, func(t *testing.T, f replacementFixture) {
			replacementUpdateUser(t, f, "token_version", f.session.User.TokenVersion+1)
		}},
		{"role", http.StatusUnauthorized, func(t *testing.T, f replacementFixture) {
			replacementUpdateUser(t, f, "role", "operator")
		}},
		{"durable revocation", http.StatusUnauthorized, func(t *testing.T, f replacementFixture) {
			if err := f.db.Create(&model.TokenRevocation{TokenHash: "jti:" + f.session.Binding.JTI, UserID: f.session.User.ID, ExpiresAt: f.session.Binding.ExpiresAt}).Error; err != nil {
				t.Fatalf("persist concurrent revocation: %v", err)
			}
		}},
		{"cached revocation", http.StatusUnauthorized, func(t *testing.T, f replacementFixture) {
			if err := f.manager.RevokeSession(f.session.Binding.JTI, f.session.User.ID, f.session.Binding.ExpiresAt); err != nil {
				t.Fatalf("cache concurrent revocation: %v", err)
			}
		}},
	}
}

func replacementUpdateUser(t *testing.T, f replacementFixture, column string, value any) {
	t.Helper()
	if err := f.db.Model(&model.User{}).Where("id = ?", f.session.User.ID).Update(column, value).Error; err != nil {
		t.Fatalf("update current identity %s: %v", column, err)
	}
}

func runReplacementPostgresIdentityWait(t *testing.T) {
	for _, test := range replacementIdentityMutations() {
		t.Run(test.name, func(t *testing.T) {
			f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, "postgres"))
			holder, pid := holdReplacementPostgresKey(t, f)
			candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
			body := replacementUpdateBody(t, "identity-must-not-save", &candidate)
			proof := replacementProof(t, f)
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() { responses <- replacementRequest(f, http.MethodPut, body, proof) }()
			waitReplacementPostgresWaiter(t, f.db, pid)
			test.mutate(t, f)
			if err := holder.Commit(); err != nil {
				t.Fatalf("release identity waiter: %v", err)
			}
			response := awaitReplacementResponse(t, responses)
			if response.Code != test.status {
				t.Fatalf("identity invalidation status=%d want %d", response.Code, test.status)
			}
			if test.status == http.StatusForbidden {
				assertStepUpRequiredEnvelopeForAction(t, response, auth.StepUpActionSSHKeyReplace)
			}
			assertRotationNotSaved(t, f.db, f.key.ID, f.old, f.key.Name)
		})
	}
}

func runReplacementPostgresProofExpiry(t *testing.T) {
	f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, "postgres"))
	holder, pid := holdReplacementPostgresKey(t, f)
	candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
	// Cross a JWT second boundary while the actual key lock is held. This
	// remains shorter than the production 1500ms lock timeout.
	expires := replacementNextProofExpiry()
	proof := replacementExpiringProof(t, f, expires)
	body := replacementUpdateBody(t, "expired-proof-metadata", &candidate)
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- replacementRequest(f, http.MethodPut, body, proof) }()
	waitReplacementPostgresWaiter(t, f.db, pid)
	if _, err := f.manager.ParseToken(proof); err != nil {
		t.Fatal("proof fixture expired before the observed key-lock wait")
	}
	timer := time.NewTimer(time.Until(expires) + 10*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if err := holder.Commit(); err != nil {
		t.Fatalf("release expiring proof waiter: %v", err)
	}
	assertStepUpRequiredEnvelopeForAction(t, awaitReplacementResponse(t, responses), auth.StepUpActionSSHKeyReplace)
	assertRotationNotSaved(t, f.db, f.key.ID, f.old, f.key.Name)
}

func TestSSHKeyReplacementPostgres(t *testing.T) {
	t.Run("metadata and secret orderings with nonempty inventory", func(t *testing.T) { runReplacementMetadataSecretOrderings(t, "postgres") })
	t.Run("current locked record", runReplacementPostgresCurrentRecord)
	t.Run("identity invalidated while waiting", runReplacementPostgresIdentityWait)
	t.Run("proof expires while waiting", runReplacementPostgresProofExpiry)
	t.Run("metadata commits before secret waiter", runReplacementPostgresMetadataBeforeSecret)
	t.Run("rotation authorization during SSH", func(t *testing.T) { runReplacementRotationIdentityWait(t, "postgres") })
	t.Run("rotation proof expires during SSH", func(t *testing.T) { runReplacementRotationProofExpiry(t, "postgres") })
	t.Run("import commits during rotation", func(t *testing.T) { runReplacementRotationImportDrift(t, "postgres") })
	t.Run("durable revocation crosses commit fence", runReplacementPostgresRevocationFence)
	t.Run("busy PUT is single attempt", runReplacementPostgresBusy)
	t.Run("deadlock is not retried", runReplacementPostgresDeadlock)
	t.Run("commit and rollback faults mask business identities", runReplacementPostgresUncertain)
}

func TestSSHKeyReplacementUpdateReturnsCurrentIdentity(t *testing.T) {
	f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, "sqlite"))
	response := replacementRequest(f, http.MethodPut, replacementUpdateBody(t, "identity-response", nil), "")
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode safe update: %v", err)
	}
	if response.Code != http.StatusOK || envelope.Code != http.StatusOK || envelope.Data.ID.String() != strconv.FormatUint(uint64(f.key.ID), 10) || envelope.Data.Name != "identity-response" || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("update did not return committed safe current record")
	}
}

func newReplacementRotationFixture(t *testing.T, engine, candidate string) (replacementFixture, *rotationSSHServer, model.Node) {
	t.Helper()
	return bindReplacementRotationFixture(t, newReplacementFixture(t, openSSHKeyRotationTestDB(t, engine)), candidate)
}

func bindReplacementRotationFixture(t *testing.T, f replacementFixture, candidate string) (replacementFixture, *rotationSSHServer, model.Node) {
	t.Helper()
	server := startRotationSSHServer(t, candidate, true, true)
	configureRotationKnownHosts(t, server)
	host, portText, err := net.SplitHostPort(server.address)
	if err != nil {
		t.Fatalf("split replacement SSH address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse replacement SSH port: %v", err)
	}
	node := seedRotationNode(t, f.db, f.key.ID, "replacement-verified-node", host, port, false)
	setRotationKeyScope(t, f.db, f.key.ID, []uint{node.ID}, false, f.key.ExpiresAt)
	return f, server, node
}

func runReplacementRotationIdentityWait(t *testing.T, engine string) {
	t.Helper()
	for _, test := range replacementIdentityMutations() {
		t.Run(test.name, func(t *testing.T) {
			candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
			f, server, _ := newReplacementRotationFixture(t, engine, candidate)
			body := rotationRequestBody(t, candidate, "auto", "rotation-must-not-save")
			proof := replacementProof(t, f)
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() { responses <- replacementRequest(f, http.MethodPost, body, proof) }()
			waitRotationSSHAuth(t, server, 1)
			test.mutate(t, f)
			attempts := server.authAttempts.Load()
			releaseRotationSSHServer(server)
			response := awaitReplacementResponse(t, responses)
			if response.Code != test.status {
				t.Fatalf("rotation identity invalidation status=%d want %d", response.Code, test.status)
			}
			if test.status == http.StatusForbidden {
				assertStepUpRequiredEnvelopeForAction(t, response, auth.StepUpActionSSHKeyReplace)
			}
			if server.authAttempts.Load() > attempts+1 {
				t.Fatal("authorization rejection retried SSH validation")
			}
			assertRotationNotSaved(t, f.db, f.key.ID, f.old, f.key.Name)
			assertRotationAuditReason(t, f.db, f.key.ID, "request_rejected")
			assertRotationAuditSafe(t, f.db, f.key.ID, response.Body.String(), f.old, candidate, proof)
		})
	}
}

func TestSSHKeyReplacementRotationIdentityDuringSSH(t *testing.T) {
	runReplacementRotationIdentityWait(t, "sqlite")
}

func runReplacementRotationProofExpiry(t *testing.T, engine string) {
	t.Helper()
	candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
	f, server, _ := newReplacementRotationFixture(t, engine, candidate)
	expires := time.Now().UTC().Truncate(time.Second).Add(3 * time.Second)
	proof := replacementExpiringProof(t, f, expires)
	body := rotationRequestBody(t, candidate, "auto", "expired-rotation")
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- replacementRequest(f, http.MethodPost, body, proof) }()
	waitRotationSSHAuth(t, server, 1)
	timer := time.NewTimer(time.Until(expires) + 10*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	releaseRotationSSHServer(server)
	response := awaitReplacementResponse(t, responses)
	assertStepUpRequiredEnvelopeForAction(t, response, auth.StepUpActionSSHKeyReplace)
	assertRotationNotSaved(t, f.db, f.key.ID, f.old, f.key.Name)
	assertRotationAuditReason(t, f.db, f.key.ID, "request_rejected")
}

func TestSSHKeyReplacementRotationProofExpiresDuringSSH(t *testing.T) {
	runReplacementRotationProofExpiry(t, "sqlite")
}

func runReplacementRotationImportDrift(t *testing.T, engine string) {
	t.Helper()
	candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
	f, server, node := newReplacementRotationFixture(t, engine, candidate)
	migrateP1ConfigImport(t, f.db)
	body := rotationRequestBody(t, candidate, "auto", "rotation-loses-to-import")
	proof := replacementProof(t, f)
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- replacementRequest(f, http.MethodPost, body, proof) }()
	waitRotationSSHAuth(t, server, 1)
	imported := buildSSHKeyPrivateKeyForHandlerTest(t)
	response, result := serveP1ConfigImport(t, f.db, nil, "overwrite", map[string]any{
		"ssh_keys": []any{map[string]any{
			"name": f.key.Name, "username": "imported-user", "key_type": "auto", "private_key": imported,
			"allowed_node_names": []string{node.Name}, "allowed_purposes": sshutil.PurposeSSHKeyTest,
		}},
	})
	if response.Code != http.StatusOK || result.Updated != 1 || result.Rejected != 0 {
		t.Fatalf("concurrent real import status=%d updated=%d rejected=%d", response.Code, result.Updated, result.Rejected)
	}
	importCiphertext := replacementCiphertext(t, f.db, f.key.ID)
	releaseRotationSSHServer(server)
	rotation := awaitReplacementResponse(t, responses)
	if rotation.Code != http.StatusOK {
		t.Fatalf("rotation after import status=%d", rotation.Code)
	}
	outcome := decodeRotationResponse(t, rotation)
	if outcome.Status != "not_saved" || outcome.Reason != "conflict" || len(outcome.Results) != 1 || outcome.Results[0].Status != "verified" {
		t.Fatal("committed import did not invalidate verified rotation snapshot")
	}
	stored := readRotationKey(t, f.db, f.key.ID)
	if !sshutil.SamePrivateKeyMaterial(stored.PrivateKey, imported) || stored.Username != "imported-user" || replacementCiphertext(t, f.db, f.key.ID) != importCiphertext {
		t.Fatal("rotation overwrote committed import")
	}
}

func TestSSHKeyReplacementRotationImportDrift(t *testing.T) {
	runReplacementRotationImportDrift(t, "sqlite")
}

func runReplacementPostgresMetadataBeforeSecret(t *testing.T) {
	f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, "postgres"))
	seedRotationNode(t, f.db, f.key.ID, "metadata-first-offline", "127.0.0.1", 1, false)
	holder, pid := holdReplacementPostgresKey(t, f)
	candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
	body := replacementUpdateBody(t, "secret-after-metadata", &candidate)
	proof := replacementProof(t, f)
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- replacementRequest(f, http.MethodPut, body, proof) }()
	waitReplacementPostgresWaiter(t, f.db, pid)
	used := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := holder.Exec("UPDATE ssh_keys SET allowed_node_tags = $1, last_used_at = $2 WHERE id = $3", "metadata-committed-first", used, f.key.ID); err != nil {
		t.Fatalf("write preceding metadata: %v", err)
	}
	if err := holder.Commit(); err != nil {
		t.Fatalf("commit preceding metadata: %v", err)
	}
	response := awaitReplacementResponse(t, responses)
	if response.Code != http.StatusOK {
		t.Fatalf("secret waiter status=%d", response.Code)
	}
	stored := readRotationKey(t, f.db, f.key.ID)
	if !sshutil.SamePrivateKeyMaterial(stored.PrivateKey, candidate) || stored.Name != "secret-after-metadata" || stored.AllowedNodeTags != "metadata-committed-first" || stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(used) {
		t.Fatal("replacement waiter lost preceding metadata or usage update")
	}
}

func runReplacementPostgresRevocationFence(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
			var f replacementFixture
			var server *rotationSSHServer
			if method == http.MethodPost {
				f, server, _ = newReplacementRotationFixture(t, "postgres", candidate)
			} else {
				f = newReplacementFixture(t, openSSHKeyRotationTestDB(t, "postgres"))
				seedRotationNode(t, f.db, f.key.ID, "revocation-fence-offline", "127.0.0.1", 1, false)
			}
			proof := replacementProof(t, f)
			body := replacementUpdateBody(t, "revocation-fence-rejected", &candidate)
			if method == http.MethodPost {
				body = rotationRequestBody(t, candidate, "auto", "revocation-fence-rejected")
			}
			pool, err := f.db.DB()
			if err != nil {
				t.Fatalf("get revocation-fence pool: %v", err)
			}
			holder, err := pool.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("begin revocation writer: %v", err)
			}
			t.Cleanup(func() { _ = holder.Rollback() })
			var pid int
			if err := holder.QueryRow("SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatalf("get revocation writer PID: %v", err)
			}
			// The uncommitted insert is invisible to admission reads, but its
			// table lock blocks the final SHARE fence. READ COMMITTED must
			// read the newly committed fact in a statement after that fence.
			if _, err := holder.Exec("INSERT INTO token_revocations (token_hash, user_id, expires_at, created_at) VALUES ($1, $2, $3, $4)",
				"jti:"+f.session.Binding.JTI, f.session.User.ID, f.session.Binding.ExpiresAt, time.Now().UTC()); err != nil {
				t.Fatalf("insert uncommitted revocation: %v", err)
			}
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() { responses <- replacementRequest(f, method, body, proof) }()
			if server != nil {
				waitRotationSSHAuth(t, server, 1)
				releaseRotationSSHServer(server)
			}
			waitReplacementPostgresWaiter(t, f.db, pid)
			if err := holder.Commit(); err != nil {
				t.Fatalf("commit revocation ahead of final read: %v", err)
			}
			response := awaitReplacementResponse(t, responses)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("post-fence revocation status=%d want 401", response.Code)
			}
			assertRotationNotSaved(t, f.db, f.key.ID, f.old, f.key.Name)
		})
	}
}

func runReplacementPostgresBusy(t *testing.T) {
	f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, "postgres"))
	holder, pid := holdReplacementPostgresKey(t, f)
	var reads atomic.Int32
	const callback = "test:replacement-postgres-busy-read-count"
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "ssh_keys" {
			reads.Add(1)
		}
	}); err != nil {
		t.Fatalf("register key-read counter: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	body := replacementUpdateBody(t, "busy-metadata", nil)
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- replacementRequest(f, http.MethodPut, body, "") }()
	waitReplacementPostgresWaiter(t, f.db, pid)
	response := awaitReplacementResponse(t, responses)
	if response.Code != http.StatusConflict || reads.Load() != 1 {
		t.Fatalf("busy PUT status=%d key reads=%d want 409/single attempt", response.Code, reads.Load())
	}
	if err := holder.Rollback(); err != nil {
		t.Fatalf("release busy key holder: %v", err)
	}
	assertRotationNotSaved(t, f.db, f.key.ID, f.old, f.key.Name)
}

func runReplacementPostgresDeadlock(t *testing.T) {
	f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, "postgres"))
	other := seedRotationKeyNamed(t, f.db, buildSSHKeyPrivateKeyForHandlerTest(t), "replacement-deadlock-other")
	ready := make(chan error, 2)
	release := newSSHKeyRotationSQLiteGate()
	t.Cleanup(release.release)
	results := make(chan error, 2)
	var firstCalls, secondCalls atomic.Int32
	run := func(first, second uint, calls *atomic.Int32) {
		results <- withSSHKeyMutationTx(context.Background(), f.db, func(tx *gorm.DB) error {
			calls.Add(1)
			err := tx.Exec("SELECT id FROM ssh_keys WHERE id = ? FOR UPDATE", first).Error
			ready <- err
			if err != nil {
				return err
			}
			<-release.ch
			return tx.Exec("SELECT id FROM ssh_keys WHERE id = ? FOR UPDATE", second).Error
		})
	}
	go run(f.key.ID, other.ID, &firstCalls)
	go run(other.ID, f.key.ID, &secondCalls)
	for range 2 {
		select {
		case err := <-ready:
			if err != nil {
				t.Fatalf("acquire first deadlock row: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("deadlock participant did not acquire its first row")
		}
	}
	release.release()
	busy, succeeded := 0, 0
	for range 2 {
		select {
		case err := <-results:
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, errSSHKeyMutationBusy):
				busy++
			default:
				t.Fatalf("deadlock result was not a definite single-attempt rejection: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("deadlock transaction did not finish")
		}
	}
	if succeeded != 1 || busy != 1 || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("deadlock retry/result mismatch: success=%d busy=%d first=%d second=%d", succeeded, busy, firstCalls.Load(), secondCalls.Load())
	}
}

func TestSSHKeyReplacementSQLiteUncertainDropsBusinessIdentity(t *testing.T) {
	for _, fault := range []string{"rollback", "restore"} {
		t.Run(fault, func(t *testing.T) {
			db, _ := openSSHKeyRotationProductionSQLiteDB(t)
			var finalized atomic.Bool
			var injected atomic.Int32
			before := checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
				conn.RegisterAuthorizer(func(op int, arg1, arg2, _ string) int {
					if op == sqlite3.SQLITE_TRANSACTION && strings.EqualFold(arg1, "ROLLBACK") {
						finalized.Store(true)
						if fault == "rollback" {
							injected.Add(1)
							return sqlite3.SQLITE_DENY
						}
					}
					if fault == "restore" && finalized.Load() && op == sqlite3.SQLITE_PRAGMA && strings.EqualFold(arg1, "busy_timeout") && strings.TrimSpace(arg2) == "5000" {
						injected.Add(1)
						return sqlite3.SQLITE_DENY
					}
					return sqlite3.SQLITE_OK
				})
			})
			var calls atomic.Int32
			err := withSSHKeyMutationTx(context.Background(), db, func(*gorm.DB) error {
				calls.Add(1)
				return errors.Join(ErrStepUpProofInvalid, errSSHKeyMutationBusy, errSSHKeyMutationSessionInvalid)
			})
			if !errors.Is(err, errSSHKeyMutationUncertain) || injected.Load() == 0 || calls.Load() != 1 {
				t.Fatalf("cleanup failure classification: uncertain=%t faults=%d calls=%d", errors.Is(err, errSSHKeyMutationUncertain), injected.Load(), calls.Load())
			}
			for _, business := range []error{ErrStepUpProofInvalid, errSSHKeyMutationBusy, errSSHKeyMutationSessionInvalid} {
				if errors.Is(err, business) {
					t.Fatalf("uncertain cleanup retained misleading business identity %v", business)
				}
			}
			if checkoutSSHKeyRotationSQLiteConn(t, db, nil) == before {
				t.Fatal("uncertain cleanup retained unsafe physical connection")
			}
			assertSSHKeyRotationSQLiteFreshTransaction(t, db)
		})
	}
}

func TestSSHKeyReplacementSQLiteConcurrentOrderings(t *testing.T) {
	for _, ordering := range []string{"secret_first", "metadata_first", "same_candidate_becomes_replacement"} {
		t.Run(ordering, func(t *testing.T) {
			db, contenderDB := openSSHKeyRotationProductionSQLiteDB(t)
			f := newReplacementFixture(t, db)
			seedRotationNode(t, db, f.key.ID, "concurrent-offline", "127.0.0.1", 1, false)
			contender := f
			contender.db = contenderDB
			contender.router = replacementRouter(contenderDB, f.manager, f.session)
			candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
			proof := replacementProof(t, f)
			firstBody := replacementUpdateBody(t, "first-secret", &candidate)
			secondBody := replacementUpdateBody(t, "last-metadata", nil)
			firstProof, secondProof := proof, ""
			switch ordering {
			case "metadata_first":
				firstBody = []byte(`{"name":"first-metadata","username":"root","key_type":"auto","allowed_node_tags":"concurrent-metadata"}`)
				secondBody = replacementUpdateBody(t, "last-secret", &candidate)
				firstProof, secondProof = "", proof
			case "same_candidate_becomes_replacement":
				secondBody = replacementUpdateBody(t, "must-not-revert", &f.old)
			}
			updateReached := newSSHKeyRotationSQLiteGate()
			release := newSSHKeyRotationSQLiteGate()
			t.Cleanup(release.release)
			const firstCallback = "test:replacement-sqlite-hold-first-update"
			if err := db.Callback().Update().After("gorm:after_update").Register(firstCallback, func(tx *gorm.DB) {
				if tx.Statement.Table == "ssh_keys" {
					updateReached.release()
					<-release.ch
				}
			}); err != nil {
				t.Fatalf("register first mutation barrier: %v", err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(firstCallback) })
			firstDone := make(chan *httptest.ResponseRecorder, 1)
			secondDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { firstDone <- replacementRequest(f, http.MethodPut, firstBody, firstProof) }()
			waitSSHKeyRotationSQLiteGate(t, updateReached.ch, "first real mutation")
			// Both fixture handles share GORM's callback registry. Start counting
			// only after the owner has reserved its writer and reached its update,
			// so the exact single-attempt assertion belongs to the contender.
			writerBusy := newSSHKeyRotationSQLiteGate()
			const contenderCallback = "test:replacement-sqlite-observe-contender-busy"
			var reservationAttempts atomic.Int32
			if err := contenderDB.Callback().Raw().After("gorm:raw").Register(contenderCallback, func(tx *gorm.DB) {
				if strings.HasPrefix(tx.Statement.SQL.String(), "UPDATE ssh_keys SET id = id WHERE") {
					reservationAttempts.Add(1)
					if isSSHKeyMutationLockError(tx.Error) {
						writerBusy.release()
					}
				}
			}); err != nil {
				t.Fatalf("register competing reservation observer: %v", err)
			}
			t.Cleanup(func() { _ = contenderDB.Callback().Raw().Remove(contenderCallback) })
			// The owner's transaction remains uncommitted until the contender
			// reaches the real SQLite writer reservation and observes BUSY.
			go func() { secondDone <- replacementRequest(contender, http.MethodPut, secondBody, secondProof) }()
			waitSSHKeyRotationSQLiteGate(t, writerBusy.ch, "competing real writer reservation")
			release.release()
			if response := awaitReplacementResponse(t, firstDone); response.Code != http.StatusOK {
				t.Fatalf("first mutation status=%d", response.Code)
			}
			if response := awaitReplacementResponse(t, secondDone); response.Code != http.StatusConflict || reservationAttempts.Load() != 1 {
				t.Fatalf("competing mutation status=%d attempts=%d", response.Code, reservationAttempts.Load())
			}
			before := replacementCiphertext(t, db, f.key.ID)
			// This is a separate explicit submission after the definite 409,
			// never a retry performed by the mutation implementation.
			response := replacementRequest(contender, http.MethodPut, secondBody, secondProof)
			stored := readRotationKey(t, db, f.key.ID)
			if ordering == "same_candidate_becomes_replacement" {
				assertStepUpRequiredEnvelopeForAction(t, response, auth.StepUpActionSSHKeyReplace)
				if stored.Name != "first-secret" {
					t.Fatal("reclassified replacement committed its metadata")
				}
			} else if response.Code != http.StatusOK {
				t.Fatalf("new explicit submission status=%d", response.Code)
			}
			if !sshutil.SamePrivateKeyMaterial(stored.PrivateKey, candidate) {
				t.Fatal("concurrent metadata/reclassification reverted the saved secret")
			}
			if ordering != "metadata_first" && replacementCiphertext(t, db, f.key.ID) != before {
				t.Fatal("metadata or rejected proof rewrote the saved ciphertext")
			}
			if ordering == "metadata_first" && stored.AllowedNodeTags != "concurrent-metadata" {
				t.Fatal("replacement lost preceding metadata scope")
			}
		})
	}
}

func replacementNextProofExpiry() time.Time {
	expires := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	if time.Until(expires) < 750*time.Millisecond {
		// Start near a signed JWT second boundary, so admission has enough
		// headroom without crossing the 1500ms PostgreSQL lock timeout.
		timer := time.NewTimer(time.Until(expires) + 5*time.Millisecond)
		<-timer.C
		timer.Stop()
		expires = expires.Add(time.Second)
	}
	return expires
}

func TestSSHKeyReplacementRotationSQLiteUncertainIsNotChallenge(t *testing.T) {
	for _, fault := range []string{"commit", "rollback", "restore_after_rejection", "restore_after_commit"} {
		t.Run(fault, func(t *testing.T) {
			db, _ := openSSHKeyRotationProductionSQLiteDB(t)
			candidate := buildSSHKeyPrivateKeyForHandlerTest(t)
			f, server, _ := bindReplacementRotationFixture(t, newReplacementFixture(t, db), candidate)
			proof := replacementProof(t, f)
			body := rotationRequestBody(t, candidate, "auto", "uncertain-rotation")
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() { responses <- replacementRequest(f, http.MethodPost, body, proof) }()
			waitRotationSSHAuth(t, server, 1)
			if fault == "rollback" || fault == "restore_after_rejection" {
				replacementUpdateUser(t, f, "totp_enabled", false)
			}
			var finalized atomic.Bool
			var injected atomic.Int32
			checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
				if fault == "commit" {
					conn.RegisterCommitHook(func() int { injected.Add(1); return 1 })
				}
				conn.RegisterAuthorizer(func(op int, arg1, arg2, _ string) int {
					if op == sqlite3.SQLITE_TRANSACTION && (strings.EqualFold(arg1, "ROLLBACK") || strings.EqualFold(arg1, "COMMIT")) {
						finalized.Store(true)
						if fault == "rollback" && strings.EqualFold(arg1, "ROLLBACK") {
							injected.Add(1)
							return sqlite3.SQLITE_DENY
						}
					}
					if strings.HasPrefix(fault, "restore_") && finalized.Load() && op == sqlite3.SQLITE_PRAGMA && strings.EqualFold(arg1, "busy_timeout") && strings.TrimSpace(arg2) == "5000" {
						injected.Add(1)
						return sqlite3.SQLITE_DENY
					}
					return sqlite3.SQLITE_OK
				})
			})
			releaseRotationSSHServer(server)
			response := awaitReplacementResponse(t, responses)
			if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), stepUpRequiredCode) || strings.Contains(response.Body.String(), "not_saved") || injected.Load() == 0 {
				t.Fatalf("uncertain rotation must not invite resubmission: status=%d faults=%d", response.Code, injected.Load())
			}
			checkoutSSHKeyRotationSQLiteConn(t, db, func(conn *sqlite3.SQLiteConn) {
				conn.RegisterAuthorizer(nil)
				conn.RegisterCommitHook(nil)
			})
			if fault == "restore_after_commit" {
				if !sshutil.SamePrivateKeyMaterial(readRotationKey(t, db, f.key.ID).PrivateKey, candidate) {
					t.Fatal("rotation cleanup failure lost the committed secret")
				}
			} else {
				assertRotationNotSaved(t, db, f.key.ID, f.old, f.key.Name)
			}
			assertSSHKeyRotationSQLiteFreshTransaction(t, db)
		})
	}
}

// Faults are reported only after the real PostgreSQL transaction method has
// executed. This exercises the wrapper's ambiguous-result handling without
// replacing persistence, statement execution, or the transaction itself.
type replacementPostgresFaultPool struct {
	gorm.ConnPool
	committer     gorm.TxCommitter
	failCommit    bool
	failRollback  bool
	commitCalls   int
	rollbackCalls int
}

func (pool *replacementPostgresFaultPool) Commit() error {
	pool.commitCalls++
	if err := pool.committer.Commit(); err != nil {
		return err
	}
	if pool.failCommit {
		return errors.New("FAKE_POSTGRES_COMMIT_RESULT_LOST_FOR_TEST_ONLY")
	}
	return nil
}

func (pool *replacementPostgresFaultPool) Rollback() error {
	pool.rollbackCalls++
	if err := pool.committer.Rollback(); err != nil {
		return err
	}
	if pool.failRollback {
		return errors.New("FAKE_POSTGRES_ROLLBACK_RESULT_LOST_FOR_TEST_ONLY")
	}
	return nil
}

func runReplacementPostgresUncertain(t *testing.T) {
	for _, fault := range []string{"commit", "rollback"} {
		t.Run(fault, func(t *testing.T) {
			f := newReplacementFixture(t, openSSHKeyRotationTestDB(t, "postgres"))
			var calls int
			var physical *replacementPostgresFaultPool
			err := withSSHKeyMutationTx(context.Background(), f.db, func(tx *gorm.DB) error {
				calls++
				if err := tx.Exec("UPDATE ssh_keys SET name = ? WHERE id = ?", "postgres-uncertain", f.key.ID).Error; err != nil {
					return err
				}
				committer, ok := tx.Statement.ConnPool.(gorm.TxCommitter)
				if !ok {
					return errors.New("PostgreSQL mutation did not expose its real transaction")
				}
				physical = &replacementPostgresFaultPool{
					ConnPool: tx.Statement.ConnPool, committer: committer,
					failCommit: fault == "commit", failRollback: fault == "rollback",
				}
				tx.Statement.ConnPool = physical
				if fault == "rollback" {
					return errors.Join(ErrStepUpProofInvalid, errSSHKeyMutationBusy, errSSHKeyMutationSessionInvalid)
				}
				return nil
			})
			if !errors.Is(err, errSSHKeyMutationUncertain) || calls != 1 || physical == nil {
				t.Fatalf("PostgreSQL lost transaction result: uncertain=%t callbacks=%d", errors.Is(err, errSSHKeyMutationUncertain), calls)
			}
			for _, business := range []error{ErrStepUpProofInvalid, errSSHKeyMutationBusy, errSSHKeyMutationSessionInvalid} {
				if errors.Is(err, business) {
					t.Fatalf("PostgreSQL uncertain result retained misleading %v", business)
				}
			}
			stored := readRotationKey(t, f.db, f.key.ID)
			wantName := f.key.Name
			if fault == "commit" {
				wantName = "postgres-uncertain"
				if physical.commitCalls != 1 || physical.rollbackCalls != 0 {
					t.Fatal("PostgreSQL ambiguous commit was retried or rolled back as a definite rejection")
				}
			} else if physical.rollbackCalls != 1 || physical.commitCalls != 0 {
				t.Fatal("PostgreSQL rollback failure was retried or committed")
			}
			if stored.Name != wantName || stored.PrivateKey != f.old {
				t.Fatal("PostgreSQL fault fixture did not preserve the actual transaction outcome")
			}
		})
	}
}
