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
		dialector = sqlite.Open(filepath.Join(t.TempDir(), "batch.sqlite") + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate&_loc=UTC")
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
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}, &model.TaskRun{}, &model.TaskRunEffect{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}, &model.BatchCommand{}, &model.BatchCommandDispatch{}, &model.BatchCommandRunEvidence{}); err != nil {
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

func TestBatchCommandRetryCreationPersistsFingerprint(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := batchPersistenceDB(t, engine)
			nodes := seedBatchNodes(t, db)
			rawCommand := "  printf 'retry raw\\n'  "
			req := batchCommandRequest{
				NodeIDs: []uint{nodes[0].ID, nodes[1].ID},
				Command: rawCommand,
				Name:    "retry source",
			}
			batch, dispatches, created, err := NewBatchHandler(db, nil).createBatchTasks(context.Background(), 7, "retry-source-fingerprint-"+engine, req, nodes)
			if err != nil {
				t.Fatal(err)
			}
			if !created || batch.ProvenanceVersion != 1 || batch.SourceBatchID != nil {
				t.Fatalf("unexpected root batch provenance: created=%v batch=%+v", created, batch)
			}
			if len(dispatches) != len(nodes) {
				t.Fatalf("dispatch count=%d, want %d", len(dispatches), len(nodes))
			}
			var task model.Task
			if err := db.First(&task, "id = ?", dispatches[0].TaskID).Error; err != nil {
				t.Fatal(err)
			}
			if task.Command != rawCommand {
				t.Fatalf("task command=%q, want original raw command %q", task.Command, rawCommand)
			}
			wantFingerprint := model.BatchCommandTaskFingerprint(task)
			if wantFingerprint == "" || dispatches[0].CommandFingerprint != wantFingerprint {
				t.Fatalf("dispatch fingerprint=%q, want task fingerprint %q", dispatches[0].CommandFingerprint, wantFingerprint)
			}
			trimmed := task
			trimmed.Command = strings.TrimSpace(task.Command)
			if wantFingerprint == model.BatchCommandTaskFingerprint(trimmed) {
				t.Fatal("persisted fingerprint must remain tied to raw command bytes")
			}
			var persisted model.BatchCommandDispatch
			if err := db.First(&persisted, "task_id = ?", task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if persisted.CommandFingerprint != wantFingerprint {
				t.Fatalf("persisted dispatch fingerprint=%q, want %q", persisted.CommandFingerprint, wantFingerprint)
			}
		})
	}
}

func TestBatchCommandRetryChildInsertPersistsDistinctSource(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := batchPersistenceDB(t, engine)
			nodes := seedBatchNodes(t, db)
			rawCommand := "  printf 'retry child\\n'  "
			rootReq := batchCommandRequest{NodeIDs: []uint{nodes[0].ID, nodes[1].ID}, Command: rawCommand, Name: "retry root"}
			root, rootDispatches, _, err := NewBatchHandler(db, nil).createBatchTasks(context.Background(), 7, "retry-child-root-"+engine, rootReq, nodes)
			if err != nil {
				t.Fatal(err)
			}
			sourceBatchID := root.ID
			child := model.BatchCommand{
				ID:                "retry-child-" + engine,
				RequesterID:       8,
				IdempotencyKey:    "retry-child-key-" + engine,
				RequestHash:       strings.Repeat("a", 64),
				ProvenanceVersion: 1,
				SourceBatchID:     &sourceBatchID,
				Retain:            root.Retain,
			}
			childReq := batchCommandRequest{Command: rawCommand, Name: "重试批次 " + root.ID}
			var childDispatches []model.BatchCommandDispatch
			if err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&child).Error; err != nil {
					return err
				}
				var err error
				childDispatches, err = insertBatchCommandTasksTx(tx, child, childReq, []batchNode{nodes[0]}, []batchRetrySource{{TaskID: rootDispatches[0].TaskID, RunID: rootDispatches[0].RunID}})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(childDispatches) != 1 {
				t.Fatalf("child dispatch count=%d, want 1", len(childDispatches))
			}
			var rootTask, childTask model.Task
			if err := db.First(&rootTask, "id = ?", rootDispatches[0].TaskID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&childTask, "batch_id = ?", child.ID).Error; err != nil {
				t.Fatal(err)
			}
			if childTask.ID == rootTask.ID {
				t.Fatalf("child task reused root task id %d", childTask.ID)
			}
			rootFingerprint := model.BatchCommandTaskFingerprint(rootTask)
			childFingerprint := model.BatchCommandTaskFingerprint(childTask)
			if rootFingerprint == "" || childFingerprint == "" || rootFingerprint == childFingerprint {
				t.Fatalf("root/child fingerprints must be distinct: root=%q child=%q", rootFingerprint, childFingerprint)
			}
			var childDispatch model.BatchCommandDispatch
			if err := db.First(&childDispatch, "task_id = ?", childTask.ID).Error; err != nil {
				t.Fatal(err)
			}
			if childDispatch.CommandFingerprint != childFingerprint {
				t.Fatalf("child dispatch fingerprint=%q, want %q", childDispatch.CommandFingerprint, childFingerprint)
			}
			if childDispatch.RetryOfTaskID == nil || *childDispatch.RetryOfTaskID != rootDispatches[0].TaskID {
				t.Fatalf("child source task edge=%v, want %d", childDispatch.RetryOfTaskID, rootDispatches[0].TaskID)
			}
			if childDispatch.RetryOfRunID == nil || *childDispatch.RetryOfRunID != rootDispatches[0].RunID {
				t.Fatalf("child source run edge=%v, want %d", childDispatch.RetryOfRunID, rootDispatches[0].RunID)
			}
		})
	}
}

func TestBatchCommandRetryCompetingSourceRollsBackGroup(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := batchPersistenceDB(t, engine)
			nodes := seedBatchNodes(t, db)
			rootReq := batchCommandRequest{NodeIDs: []uint{nodes[0].ID, nodes[1].ID}, Command: "  printf 'retry source\\n'  ", Name: "retry root"}
			root, rootDispatches, _, err := NewBatchHandler(db, nil).createBatchTasks(context.Background(), 7, "retry-compete-root-"+engine, rootReq, nodes)
			if err != nil {
				t.Fatal(err)
			}
			rootSourceID := root.ID
			claimed := model.BatchCommand{
				ID:                "retry-claimed-" + engine,
				RequesterID:       8,
				IdempotencyKey:    "retry-claimed-key-" + engine,
				RequestHash:       strings.Repeat("b", 64),
				ProvenanceVersion: 1,
				SourceBatchID:     &rootSourceID,
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&claimed).Error; err != nil {
					return err
				}
				_, err := insertBatchCommandTasksTx(tx, claimed, batchCommandRequest{Command: rootReq.Command, Name: "claimed"}, []batchNode{nodes[0]}, []batchRetrySource{{TaskID: rootDispatches[0].TaskID, RunID: rootDispatches[0].RunID}})
				return err
			}); err != nil {
				t.Fatal(err)
			}

			candidateID := "retry-competing-" + engine
			candidateSourceID := root.ID
			candidate := model.BatchCommand{
				ID:                candidateID,
				RequesterID:       9,
				IdempotencyKey:    "retry-competing-key-" + engine,
				RequestHash:       strings.Repeat("c", 64),
				ProvenanceVersion: 1,
				SourceBatchID:     &candidateSourceID,
			}
			err = db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&candidate).Error; err != nil {
					return err
				}
				_, err := insertBatchCommandTasksTx(
					tx,
					candidate,
					batchCommandRequest{Command: rootReq.Command, Name: "competing"},
					nodes,
					[]batchRetrySource{
						{TaskID: rootDispatches[0].TaskID, RunID: rootDispatches[0].RunID},
						{TaskID: rootDispatches[1].TaskID, RunID: rootDispatches[1].RunID},
					},
				)
				return err
			})
			if !errors.Is(err, errBatchRetryAlreadyClaimed) {
				t.Fatalf("competing source error=%v, want errBatchRetryAlreadyClaimed", err)
			}
			var batchCount int64
			if err := db.Model(&model.BatchCommand{}).Where("id = ?", candidateID).Count(&batchCount).Error; err != nil {
				t.Fatal(err)
			}
			if batchCount != 0 {
				t.Fatalf("rolled-back candidate left batch row: %d", batchCount)
			}
			for _, entity := range []any{&model.Task{}, &model.BatchCommandDispatch{}} {
				var count int64
				if err := db.Model(entity).Where("batch_id = ?", candidateID).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("rolled-back candidate left %T rows: %d", entity, count)
				}
			}
			var claimedDispatchCount int64
			if err := db.Model(&model.BatchCommandDispatch{}).Where("batch_id = ?", claimed.ID).Count(&claimedDispatchCount).Error; err != nil {
				t.Fatal(err)
			}
			if claimedDispatchCount != 1 {
				t.Fatalf("committed competing claimant dispatch count=%d, want 1", claimedDispatchCount)
			}
		})
	}
}

func TestBatchCommandRetryRootCreationRollsBackOnDispatchInsertFailure(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := batchPersistenceDB(t, engine)
			nodes := seedBatchNodes(t, db)
			var dispatchInserts atomic.Int32
			callbackName := "batch_retry_dispatch_insert_failure_" + engine
			if err := db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "batch_command_dispatches" && dispatchInserts.Add(1) == 2 {
					_ = tx.AddError(errors.New("injected batch dispatch insert failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = db.Callback().Create().Remove(callbackName)
			})
			req := batchCommandRequest{NodeIDs: []uint{nodes[0].ID, nodes[1].ID}, Command: "  printf 'dispatch failure\\n'  ", Name: "dispatch failure"}
			_, _, created, err := NewBatchHandler(db, nil).createBatchTasks(context.Background(), 7, "retry-dispatch-failure-"+engine, req, nodes)
			if err == nil {
				t.Fatal("injected dispatch failure must fail root creation")
			}
			if created {
				t.Fatal("failed root creation must not report created")
			}
			for _, entity := range []any{&model.BatchCommand{}, &model.Task{}, &model.BatchCommandDispatch{}} {
				var count int64
				if err := db.Model(entity).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("failed root creation left %T rows: %d", entity, count)
				}
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
