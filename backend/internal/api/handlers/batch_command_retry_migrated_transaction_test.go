package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	taskruntime "xirang/backend/internal/task"
	"xirang/backend/internal/task/executor"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openMigratedRetryTransactionDB(t *testing.T, engine string) *gorm.DB {
	t.Helper()
	if engine == "sqlite" {
		db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "batch-command-retry-migrated.sqlite")+"?_busy_timeout=5000&_txlock=immediate"), &gorm.Config{
			NowFunc: func() time.Time { return time.Now().UTC() },
		})
		if err != nil {
			t.Fatalf("open migrated SQLite database: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("get migrated SQLite database: %v", err)
		}
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		if err := database.RunMigrations(db, "sqlite"); err != nil {
			t.Fatalf("run SQLite migrations: %v", err)
		}
		return db
	}

	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL base connection: %v", err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL base connection: %v", err)
	}
	schema := fmt.Sprintf("xirang_r13_retry_migrated_%d_%d", os.Getpid(), batchTestSequence.Add(1))
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = baseSQL.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
		}
		_ = baseSQL.Close()
	})
	scoped := *parsed
	query := scoped.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	scoped.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(scoped.String()), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL connection: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get isolated PostgreSQL connection: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.RunMigrations(db, "postgres"); err != nil {
		t.Fatalf("run PostgreSQL migrations: %v", err)
	}
	return db
}

func newMigratedRetryTransactionFixture(t *testing.T, engine string) *retryTransactionFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := openMigratedRetryTransactionDB(t, engine)
	fixtureID := batchTestSequence.Add(1)
	admin := seedStepUpUser(t, db, fmt.Sprintf("r13-migrated-admin-%d", fixtureID), "admin")
	operator := seedStepUpUser(t, db, fmt.Sprintf("r13-migrated-operator-%d", fixtureID), "operator")
	viewer := seedStepUpUser(t, db, fmt.Sprintf("r13-migrated-viewer-%d", fixtureID), "viewer")
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
	}
}

func TestBatchCommandRetryFullFlowUnderMigratedSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newMigratedRetryTransactionFixture(t, engine)
		root, sourceDispatch, _ := retryTransactionSeedKnownFailureRoot(t, fx)
		retryTransactionSeedGrant(t, fx.db, fx.admin, sourceDispatch.NodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		key := "r13-migrated-full-flow"
		source := []batchRetrySource{{TaskID: sourceDispatch.TaskID, RunID: sourceDispatch.RunID}}
		response := retryTransactionRequest(t, fx.router, root.ID, token, proof, key, source)
		childID := retryTransactionAssertChild(t, fx, root, sourceDispatch, response, token, key)

		deleteRequest := httptest.NewRequest(http.MethodDelete, "/batch-commands/"+childID, nil)
		deleteRequest.Header.Set("Authorization", "Bearer "+token)
		deleteResponse := httptest.NewRecorder()
		fx.router.ServeHTTP(deleteResponse, deleteRequest)
		if deleteResponse.Code != http.StatusOK {
			t.Fatalf("migrated child cleanup status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
		}
		var deletedChild model.BatchCommand
		if err := fx.db.Unscoped().First(&deletedChild, "id = ?", childID).Error; err != nil {
			t.Fatalf("load migrated deleted child: %v", err)
		}
		if deletedChild.DeletedAt == nil {
			t.Fatalf("migrated child cleanup did not persist tombstone: %+v", deletedChild)
		}

		replay := retryTransactionRequest(t, fx.router, root.ID, token, proof, key, source)
		if replay.Code != http.StatusConflict {
			t.Fatalf("migrated deleted-child replay status=%d body=%s", replay.Code, replay.Body.String())
		}
		var childCount int64
		if err := fx.db.Unscoped().Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&childCount).Error; err != nil {
			t.Fatal(err)
		}
		if childCount != 1 {
			t.Fatalf("migrated replay created %d child identities, want exactly one", childCount)
		}
	})
}

func retryTransactionInstallOneShotEffectClaimFailure(t *testing.T, db *gorm.DB) <-chan struct{} {
	t.Helper()
	reached := make(chan struct{})
	var once sync.Once
	callbackName := fmt.Sprintf("retry-transaction-effect-claim-failure-%d", batchTestSequence.Add(1))
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		tableName := tx.Statement.Table
		if tableName == "" && tx.Statement.Schema != nil {
			tableName = tx.Statement.Schema.Table
		}
		if tableName != "task_run_effects" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		once.Do(func() {
			close(reached)
			_ = tx.AddError(errors.New("test: first effect claim unavailable"))
		})
	}); err != nil {
		t.Fatalf("register one-shot effect claim failure: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	return reached
}

func retryTransactionSeedRealFailureWithPendingEffect(t *testing.T, fx *retryTransactionFixture) (*taskruntime.Manager, model.BatchCommand, model.BatchCommandDispatch) {
	t.Helper()
	if err := fx.db.Model(&model.Node{}).Where("id = ?", fx.nodes[0].ID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	manager := taskruntime.NewManager(fx.db, executor.NewFactory(""), nil, nil, nil, nil, 8, 90)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	handler := NewBatchHandler(fx.db, manager)
	root, dispatches, created, err := handler.createBatchTasks(context.Background(), fx.admin.ID, fmt.Sprintf("r13-real-effect-%d", batchTestSequence.Add(1)), batchCommandRequest{
		NodeIDs: []uint{fx.nodes[0].ID}, Command: "printf 'r13 real effect'", Name: "R13 real effect", Retain: true,
	}, fx.nodes[:1])
	if err != nil || !created || len(dispatches) != 1 {
		t.Fatalf("create real-effect source created=%v dispatches=%d err=%v", created, len(dispatches), err)
	}
	if err := fx.db.Model(&model.Task{}).Where("id = ?", dispatches[0].TaskID).Update("retry_count", 2).Error; err != nil {
		t.Fatalf("disable automatic retry for real-effect source: %v", err)
	}
	claimAttempted := retryTransactionInstallOneShotEffectClaimFailure(t, fx.db)
	if err := handler.dispatchBatch(context.Background(), root.ID, dispatches); err != nil {
		t.Fatalf("dispatch real-effect source: %v", err)
	}
	retryTransactionWaitForGate(t, claimAttempted)
	var persisted model.BatchCommandDispatch
	if err := fx.db.First(&persisted, "task_id = ?", dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != "accepted" || persisted.RunID == 0 {
		t.Fatalf("real-effect source dispatch=%+v", persisted)
	}
	retryTransactionWaitForTerminalRun(t, fx.db, persisted.RunID)
	var pending int64
	if err := fx.db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND status <> ?", persisted.RunID, model.TaskRunEffectStatusSucceeded).Count(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending == 0 {
		t.Fatalf("real terminalization did not leave an effect for recovery")
	}
	return manager, root, persisted
}
