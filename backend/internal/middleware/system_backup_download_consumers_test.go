package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const systemBackupConsumerRoute = "/api/v1/system/backups/web/xirang-20261017-000000.db/download"

type systemBackupConsumerFixture struct {
	db      *gorm.DB
	manager *auth.JWTManager
	user    model.User
	token   string
	proof   string
}

func newSystemBackupConsumerFixture(t *testing.T) *systemBackupConsumerFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "consumers.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}, &model.CredentialAccessGrant{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	// No TOTP secret is read by these consumers: use the real proof producer
	// and the persisted enabled flag without introducing encryption fixtures.
	user := model.User{Username: "backup-consumer-admin", Role: "admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", TOTPEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	manager := auth.NewJWTManager("FAKE_BACKUP_CONSUMER_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	token, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}
	proof, _, err := manager.GenerateStepUpToken(user, auth.StepUpActionSystemBackupDownload)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID: user.ID, RequesterUsername: user.Username, RequesterRole: user.Role,
		Action: handlers.CredentialGrantActionSystemBackupDownload, Purpose: handlers.CredentialGrantPurposeSystemBackupDownload,
		Status: handlers.CredentialGrantStatusActive, Reason: "consumer prepare timeout", RequestedTTLSeconds: 600,
		RequestedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	return &systemBackupConsumerFixture{db: db, manager: manager, user: user, token: token, proof: proof}
}

func TestSystemBackupDownloadProofConsumerPrepareTimeout(t *testing.T) {
	testSystemBackupDownloadConsumerPrepareTimeout(t, "proof")
}

func TestSystemBackupDownloadGrantConsumerPrepareTimeout(t *testing.T) {
	testSystemBackupDownloadConsumerPrepareTimeout(t, "grant")
}

func testSystemBackupDownloadConsumerPrepareTimeout(t *testing.T, consumer string) {
	t.Helper()
	for _, protocol := range []struct {
		name  string
		h2    bool
		major int
	}{
		{name: "http1", major: 1},
		{name: "http2", h2: true, major: 2},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			f := newSystemBackupConsumerFixture(t)
			var armed, reached atomic.Bool
			if err := f.db.Callback().Query().Before("gorm:query").Register("backup_consumer:block", func(tx *gorm.DB) {
				matches := consumer == "proof" && tx.Statement.Table == "users" || consumer == "grant" && tx.Statement.Table == "credential_access_grants"
				if armed.Load() && matches {
					reached.Store(true)
					// Wait on the actual request prepare context, not a sleep or
					// LimitDeadline (which would also expire the transport lease).
					<-tx.Statement.Context.Done()
					_ = tx.AddError(tx.Statement.Context.Err())
				}
			}); err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.Use(middleware.SystemBackupDownloadBudgetForTest(2*time.Second, 250*time.Millisecond))
			router.Use(middleware.AuthMiddleware(f.manager, f.db))
			router.Use(func(c *gin.Context) {
				// This is reached only after primary auth's revocation/user
				// queries succeed. In proof mode the next user query is the
				// real Download step-up verifier, not AuthMiddleware.
				armed.Store(true)
				c.Next()
			})
			handler := handlers.NewSystemBackupDownloadHandler(f.db, f.manager)
			router.GET(systemBackupConsumerRoute, middleware.RequireRole("admin"), handler.Download)
			finished := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				router.ServeHTTP(w, r)
			}))
			server.EnableHTTP2 = protocol.h2
			if protocol.h2 {
				server.StartTLS()
			} else {
				server.Start()
			}
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+systemBackupConsumerRoute, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+f.token)
			req.Header.Set(handlers.StepUpHeaderName, f.proof)
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			// The result audit is deferred after the error write; wait for the
			// complete chain before reading it, including on real HTTP/2.
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("download chain did not finish")
			}
			if !armed.Load() || !reached.Load() {
				t.Fatal("request never reached the intended post-auth consumer query")
			}
			if response.ProtoMajor != protocol.major {
				t.Fatalf("expected HTTP/%d, got %s", protocol.major, response.Proto)
			}
			var envelope struct {
				Code int `json:"code"`
				Data struct {
					ErrorCode string `json:"error_code"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil || response.StatusCode != http.StatusGatewayTimeout ||
				envelope.Code != http.StatusGatewayTimeout || envelope.Data.ErrorCode != "BACKUP_PREPARE_TIMEOUT" {
				t.Fatalf("prepare timeout status=%d body=%s err=%v", response.StatusCode, body, err)
			}
			if response.Header.Get("Content-Type") == "application/x-tar" || bytes.Contains(body, []byte(f.token)) || bytes.Contains(body, []byte(f.proof)) {
				t.Fatal("prepare timeout committed archive headers or leaked credentials")
			}
			assertSystemBackupConsumerTimeoutAudits(t, f, consumer)
		})
	}
}

func assertSystemBackupConsumerTimeoutAudits(t *testing.T, f *systemBackupConsumerFixture, consumer string) {
	t.Helper()
	var events []model.CredentialAuditEvent
	if err := f.db.Where("action = ?", handlers.CredentialGrantActionSystemBackupDownload).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	wantMetadata := map[string]any{
		"size": float64(0), "sent_bytes": float64(0),
		"stage": "admission", "reason_code": "budget_exhausted", "grant_id": float64(0),
	}
	results, proofs := 0, 0
	for _, event := range events {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
			t.Fatal(err)
		}
		if _, ok := metadata["sent_bytes"]; ok {
			results++
			if event.Outcome != "failure" || event.UserID != f.user.ID {
				t.Fatalf("incorrect timeout result audit: outcome=%s user=%d metadata=%v", event.Outcome, event.UserID, metadata)
			}
			for key, want := range wantMetadata {
				if metadata[key] != want {
					t.Fatalf("timeout result metadata[%s]=%v; want %v (metadata=%v)", key, metadata[key], want, metadata)
				}
			}
		} else if metadata["stage"] == "step_up" {
			proofs++
			// A grant timeout follows a genuinely satisfied proof. A proof
			// timeout must never become proof success or an invalid challenge.
			if consumer != "grant" || event.Outcome != "success" || metadata["proof"] != "satisfied" {
				t.Fatalf("incorrect timeout proof audit: outcome=%s metadata=%v", event.Outcome, metadata)
			}
		} else {
			t.Fatalf("unexpected admission audit on timeout: %+v", event)
		}
	}
	wantProofs := 0
	if consumer == "grant" {
		wantProofs = 1
	}
	if results != 1 || proofs != wantProofs {
		t.Fatalf("timeout audit counts results=%d proofs=%d; want results=1 proofs=%d", results, proofs, wantProofs)
	}
}
