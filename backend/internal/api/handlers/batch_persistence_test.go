package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func performIdempotentBatchRequest(router http.Handler, token, proof, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/batch-commands", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(StepUpHeaderName, proof)
	req.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

var batchTestSequence atomic.Uint64

func batchPersistenceDB(t *testing.T, engine string) *gorm.DB {
	t.Helper()
	var dialector gorm.Dialector
	if engine == "postgres" {
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN required for PostgreSQL batch contracts")
		}
		base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		schema := fmt.Sprintf("batch_audit_%d_%d", os.Getpid(), batchTestSequence.Add(1))
		if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
			conn, _ := base.DB()
			if conn != nil {
				_ = conn.Close()
			}
		})
		if strings.Contains(dsn, "://") {
			separator := "?"
			if strings.Contains(dsn, "?") {
				separator = "&"
			}
			dsn += separator + "search_path=" + schema
		} else {
			dsn += " search_path=" + schema
		}
		dialector = postgres.Open(dsn)
	} else {
		dialector = sqlite.Open(filepath.Join(t.TempDir(), "batch.sqlite") + "?_busy_timeout=5000&_txlock=immediate")
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.TaskRunEffect{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}, &model.BatchCommand{}, &model.BatchCommandDispatch{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedBatchNodes(t *testing.T, db *gorm.DB) []batchNode {
	t.Helper()
	var result []batchNode
	for i := range 2 {
		node := model.Node{Name: fmt.Sprintf("batch-node-%d", i), Host: fmt.Sprintf("10.0.0.%d", i+1), Port: 22, Username: "reader", AuthType: "key", BackupDir: fmt.Sprintf("batch-node-%d", i)}
		if err := db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
		result = append(result, batchNode{ID: node.ID, Name: node.Name})
	}
	return result
}

func TestBatchCreationRollbackAndConcurrentIdempotency(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := batchPersistenceDB(t, engine)
			nodes := seedBatchNodes(t, db)
			handler := NewBatchHandler(db, nil)
			req := batchCommandRequest{NodeIDs: []uint{nodes[0].ID, nodes[1].ID}, Command: "echo approved", Name: "atomic batch"}
			var inserts atomic.Int32
			if err := db.Callback().Create().After("gorm:create").Register("batch_second_insert_failure", func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "tasks" && inserts.Add(1) == 2 {
					_ = tx.AddError(errors.New("injected second task failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := handler.createBatchTasks(context.Background(), 7, "batch-rollback-key", req, nodes); err == nil {
				t.Fatal("second task failure must fail whole creation")
			}
			for _, entity := range []any{&model.Task{}, &model.BatchCommand{}, &model.BatchCommandDispatch{}} {
				var count int64
				if err := db.Model(entity).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("failed creation left rows: %T count=%d err=%v", entity, count, err)
				}
			}
			if err := db.Callback().Create().Remove("batch_second_insert_failure"); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			ids := make([]string, 2)
			errs := make([]error, 2)
			for i := range ids {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					batch, _, _, err := handler.createBatchTasks(context.Background(), 7, "batch-concurrent-key", req, nodes)
					ids[i], errs[i] = batch.ID, err
				}(i)
			}
			close(start)
			wg.Wait()
			if errs[0] != nil || errs[1] != nil || ids[0] != ids[1] {
				t.Fatalf("same request must share one committed batch: ids=%v errors=%v", ids, errs)
			}
			var count int64
			if err := db.Model(&model.Task{}).Count(&count).Error; err != nil || count != 2 {
				t.Fatalf("duplicate tasks: count=%d err=%v", count, err)
			}
			req.Command = "echo different"
			if _, _, _, err := handler.createBatchTasks(context.Background(), 7, "batch-concurrent-key", req, nodes); !errors.Is(err, errBatchIdempotencyConflict) {
				t.Fatalf("same key different command must conflict: %v", err)
			}
		})
	}
}

type batchFailingRunner struct{ calls atomic.Int32 }

func (runner *batchFailingRunner) TriggerManual(uint) (uint, error) {
	runner.calls.Add(1)
	return 0, errors.New("FAKE_SECRET_IN_DISPATCH_FAILURE")
}
func (*batchFailingRunner) RemoveSchedule(uint) {}

func TestBatchDispatchFailureIsQueryableAndReplayDoesNotRetrigger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := batchPersistenceDB(t, "sqlite")
	nodes := seedBatchNodes(t, db)
	runner := &batchFailingRunner{}
	handler := NewBatchHandler(db, runner)
	req := batchCommandRequest{NodeIDs: []uint{nodes[0].ID, nodes[1].ID}, Command: "echo approved"}
	batch, dispatches, _, err := handler.createBatchTasks(context.Background(), 7, "batch-failed-dispatch", req, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.dispatchBatch(context.Background(), batch.ID, dispatches); err != nil {
		t.Fatal(err)
	}
	_, replayed, created, err := handler.createBatchTasks(context.Background(), 7, "batch-failed-dispatch", req, nodes)
	if err != nil || created {
		t.Fatalf("replay err=%v created=%v", err, created)
	}
	if err := handler.dispatchBatch(context.Background(), batch.ID, replayed); err != nil {
		t.Fatal(err)
	}
	if runner.calls.Load() != 2 {
		t.Fatalf("replay repeated remote dispatch: %d", runner.calls.Load())
	}
	for _, dispatch := range replayed {
		if dispatch.Status != "failed" || dispatch.RunID != 0 || dispatch.LastError == "" || strings.Contains(dispatch.LastError, "FAKE_SECRET") {
			t.Fatalf("unsafe or untraceable dispatch result: %+v", dispatch)
		}
	}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/batch-commands/"+batch.ID, nil)
	ctx.Params = gin.Params{{Key: "batch_id", Value: batch.ID}}
	ctx.Set(middleware.CtxRole, "admin")
	ctx.Set(middleware.CtxUserID, uint(7))
	handler.Delete(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("delete failed: %d %s", response.Code, response.Body.String())
	}
	if _, _, _, err := handler.createBatchTasks(context.Background(), 7, "batch-failed-dispatch", req, nodes); !errors.Is(err, errBatchDeleted) {
		t.Fatalf("deleted batch must remain non-replayable: %v", err)
	}
	if runner.calls.Load() != 2 {
		t.Fatal("deletion replay re-executed command")
	}
}
