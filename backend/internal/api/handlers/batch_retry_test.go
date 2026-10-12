package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func retryTestContext(body string) *gin.Context {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/batch-commands/source/retry", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	return context
}

func TestBatchCommandRetryStrictSourcesRequest(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		want    []batchRetrySource
	}{
		{name: "sorted valid sources", body: `{"sources":[{"task_id":9,"run_id":0},{"task_id":3,"run_id":17}]}`, want: []batchRetrySource{{TaskID: 3, RunID: 17}, {TaskID: 9, RunID: 0}}},
		{name: "unknown field", body: `{"sources":[{"task_id":3,"run_id":0,"command":"echo unsafe"}]}`, wantErr: true},
		{name: "trailing json", body: `{"sources":[{"task_id":3,"run_id":0}]}{}`, wantErr: true},
		{name: "duplicate json member", body: `{"sources":[{"task_id":3,"run_id":0}],"sources":[]}`, wantErr: true},
		{name: "duplicate task", body: `{"sources":[{"task_id":3,"run_id":0},{"task_id":3,"run_id":2}]}`, wantErr: true},
		{name: "missing run id", body: `{"sources":[{"task_id":3}]}`, wantErr: true},
		{name: "zero task id", body: `{"sources":[{"task_id":0,"run_id":0}]}`, wantErr: true},
		{name: "empty sources", body: `{"sources":[]}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeBatchRetryRequest(retryTestContext(test.body))
			if test.wantErr {
				if err == nil {
					t.Fatalf("decode succeeded: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("sources=%+v, want %+v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("source[%d]=%+v, want %+v", i, got[i], test.want[i])
				}
			}
		})
	}
}

func TestBatchCommandRetryDispatchFailureEligibilityAndChangedRoot(t *testing.T) {
	db := batchPersistenceDB(t, "sqlite")
	nodes := seedBatchNodes(t, db)
	handler := NewBatchHandler(db, nil)
	root, dispatches, _, err := handler.createBatchTasks(t.Context(), 1, "retry-eligibility-root", batchCommandRequest{
		NodeIDs: []uint{nodes[0].ID}, Command: "printf 'retry eligibility'", Name: "retry eligibility",
	}, []batchNode{nodes[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("dispatch count=%d, want 1", len(dispatches))
	}
	if err := db.Model(&model.BatchCommandDispatch{}).Where("task_id = ?", dispatches[0].TaskID).Updates(map[string]any{
		"status": "failed", "run_id": 0,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var persistedDispatch model.BatchCommandDispatch
	if err := db.First(&persistedDispatch, dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := db.First(&task, dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	candidate, err := assessBatchRetryEligibility(db, root, task, persistedDispatch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !candidate.Eligible || candidate.Reason != batchRetryReasonEligible || candidate.FailureKind == nil || *candidate.FailureKind != batchRetryFailureKindDispatch {
		t.Fatalf("unstarted dispatch eligibility=%+v", candidate)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("command", "printf 'changed root'").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	candidate, err = assessBatchRetryEligibility(db, root, task, persistedDispatch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Eligible || candidate.Reason != batchRetryReasonSourceChanged {
		t.Fatalf("changed root eligibility=%+v", candidate)
	}
}

func TestBatchCommandRetryLatestCleanedRunFailsClosed(t *testing.T) {
	db := batchPersistenceDB(t, "sqlite")
	nodes := seedBatchNodes(t, db)
	handler := NewBatchHandler(db, nil)
	root, dispatches, _, err := handler.createBatchTasks(t.Context(), 1, "retry-cleaned-root", batchCommandRequest{
		NodeIDs: []uint{nodes[0].ID}, Command: "printf 'cleaned run'", Name: "cleaned run",
	}, []batchNode{nodes[0]})
	if err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := db.First(&task, dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskRunStatusFailed).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "command",
		TriggerType: "manual", Status: model.TaskRunStatusPending,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BatchCommandDispatch{}).Where("task_id = ?", task.ID).Updates(map[string]any{
		"status": "accepted", "run_id": run.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var evidenceCount int64
	if err := db.Model(&model.BatchCommandRunEvidence{}).Where("run_id = ?", run.ID).Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 1 {
		t.Fatalf("TaskRun hook did not create pending evidence: %d", evidenceCount)
	}
	if err := db.Delete(&model.TaskRun{}, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	var persistedDispatch model.BatchCommandDispatch
	if err := db.First(&persistedDispatch, dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	candidate, err := assessBatchRetryEligibility(db, root, task, persistedDispatch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Eligible || candidate.Reason != batchRetryReasonHistoryUnverified || candidate.RunID != 0 {
		t.Fatalf("cleaned latest run was not fail-closed: %+v", candidate)
	}
}

func TestBatchCommandRetryGetIncludesDeletedChildLink(t *testing.T) {
	db := batchPersistenceDB(t, "sqlite")
	nodes := seedBatchNodes(t, db)
	handler := NewBatchHandler(db, nil)
	root, dispatches, _, err := handler.createBatchTasks(t.Context(), 1, "retry-link-root", batchCommandRequest{
		NodeIDs: []uint{nodes[0].ID}, Command: "printf 'retry link'", Name: "retry link",
	}, []batchNode{nodes[0]})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BatchCommandDispatch{}).Where("task_id = ?", dispatches[0].TaskID).Updates(map[string]any{
		"status": "failed", "run_id": 0,
	}).Error; err != nil {
		t.Fatal(err)
	}
	childSourceID := root.ID
	child := model.BatchCommand{
		ID: "retry-link-child", RequesterID: 1, IdempotencyKey: "retry-link-child-key",
		RequestHash: strings.Repeat("a", 64), ProvenanceVersion: 1, SourceBatchID: &childSourceID,
		Retain: root.Retain,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&child).Error; err != nil {
			return err
		}
		_, err := insertBatchCommandTasksTx(tx, child, batchCommandRequest{Command: "printf 'retry link'", Name: "重试批次 " + root.ID}, []batchNode{nodes[0]}, []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Now().UTC()
	if err := db.Model(&model.BatchCommand{}).Where("id = ?", child.ID).Update("deleted_at", deletedAt).Error; err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	router.GET("/batch-commands/:batch_id", handler.Get)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/batch-commands/"+root.ID, nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			RetrySources []batchRetryEligibilityDTO `json:"retry_sources"`
			RetryLinks   []batchRetryLinkDTO        `json:"retry_links"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.RetrySources) != 1 || envelope.Data.RetrySources[0].Reason != batchRetryReasonAlreadyRetried || envelope.Data.RetrySources[0].RetryBatchID == nil || *envelope.Data.RetrySources[0].RetryBatchID != child.ID {
		t.Fatalf("retry source metadata=%+v", envelope.Data.RetrySources)
	}
	if len(envelope.Data.RetryLinks) != 1 || envelope.Data.RetryLinks[0].TaskID != dispatches[0].TaskID || envelope.Data.RetryLinks[0].RunID != 0 || envelope.Data.RetryLinks[0].BatchID != child.ID || !envelope.Data.RetryLinks[0].Deleted {
		t.Fatalf("retry links=%+v", envelope.Data.RetryLinks)
	}
	if strings.Contains(response.Body.String(), "command_fingerprint") {
		t.Fatal("GET exposed private command fingerprint")
	}
}

func TestBatchCommandRetrySuccessfulCurrentRunIsNotFailed(t *testing.T) {
	db := batchPersistenceDB(t, "sqlite")
	nodes := seedBatchNodes(t, db)
	handler := NewBatchHandler(db, nil)
	root, dispatches, _, err := handler.createBatchTasks(t.Context(), 1, "retry-success-current-root", batchCommandRequest{
		NodeIDs: []uint{nodes[0].ID}, Command: "printf 'retry success current'", Name: "retry success current",
	}, []batchNode{nodes[0]})
	if err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := db.First(&task, dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "command",
		TriggerType: "manual", Status: model.TaskRunStatusPending,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now().UTC()
	if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": model.TaskRunStatusSuccess, "finished_at": finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskRunStatusSuccess).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BatchCommandDispatch{}).Where("task_id = ?", task.ID).Updates(map[string]any{
		"status": "accepted", "run_id": run.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BatchCommandRunEvidence{}).Where("run_id = ?", run.ID).Updates(map[string]any{
		"outcome": "success", "finished_at": finishedAt, "command_fingerprint": model.BatchCommandTaskFingerprint(task),
	}).Error; err != nil {
		t.Fatal(err)
	}
	var persistedDispatch model.BatchCommandDispatch
	if err := db.First(&persistedDispatch, dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	requestedRunID := run.ID
	candidate, err := assessBatchRetryEligibility(db, root, task, persistedDispatch, &requestedRunID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Eligible || candidate.Reason != batchRetryReasonNotFailed || candidate.FailureKind != nil || candidate.RunID != run.ID {
		t.Fatalf("successful current run eligibility=%+v", candidate)
	}
	discovered, err := assessBatchRetryEligibility(db, root, task, persistedDispatch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if discovered.Eligible || discovered.Reason != batchRetryReasonNotFailed || discovered.FailureKind != nil || discovered.RunID != run.ID {
		t.Fatalf("successful discovered run eligibility=%+v", discovered)
	}
	getContext := retryTestContext("")
	getContext.Request = httptest.NewRequest(http.MethodGet, "/batch-commands/"+root.ID, nil)
	metadata, _, err := handler.buildBatchRetryMetadata(getContext, root, []model.Task{task}, []model.BatchCommandDispatch{persistedDispatch})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 1 || metadata[0].Eligible || metadata[0].Reason != batchRetryReasonNotFailed || metadata[0].FailureKind != nil || metadata[0].RunID != run.ID {
		t.Fatalf("successful GET metadata=%+v", metadata)
	}
}
