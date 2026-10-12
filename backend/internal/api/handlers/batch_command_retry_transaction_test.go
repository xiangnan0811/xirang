package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/automation"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"
	taskruntime "xirang/backend/internal/task"
	"xirang/backend/internal/task/executor"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

type retryTransactionFixture struct {
	db       *gorm.DB
	jwt      *auth.JWTManager
	handler  *BatchHandler
	router   *gin.Engine
	admin    model.User
	operator model.User
	viewer   model.User
	nodes    []batchNode
}

type retryTransactionResponse struct {
	Code int `json:"code"`
	Data struct {
		BatchID            string                       `json:"batch_id"`
		SourceBatchID      string                       `json:"source_batch_id"`
		TaskIDs            []uint                       `json:"task_ids"`
		RunIDs             []uint                       `json:"run_ids"`
		Retain             bool                         `json:"retain"`
		DispatchIncomplete bool                         `json:"dispatch_incomplete"`
		Dispatches         []model.BatchCommandDispatch `json:"dispatches"`
	} `json:"data"`
}

func newRetryTransactionFixture(t *testing.T, engine string) *retryTransactionFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := batchPersistenceDB(t, engine)
	db = db.Session(&gorm.Session{NowFunc: func() time.Time { return time.Now().UTC() }})
	if err := db.AutoMigrate(
		&model.User{}, &model.PendingAuthToken{}, &model.TokenRevocation{},
		&model.CredentialAuditEvent{}, &model.CredentialAccessGrant{},
		&model.NodeOwner{}, &model.AuditLog{},
		&model.AutomationRule{}, &model.AutomationRuleLog{},
		&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}, &model.AlertEscalationEvent{},
		&model.RestoreDrillEvidence{},
		&model.TaskLog{}, &model.TaskTrafficSample{},
		&model.TaskCronOccurrence{}, &model.TaskRepositoryLink{},
	); err != nil {
		t.Fatalf("migrate retry transaction auth tables: %v", err)
	}

	fixtureID := batchTestSequence.Add(1)
	admin := seedStepUpUser(t, db, fmt.Sprintf("r13-admin-%d", fixtureID), "admin")
	operator := seedStepUpUser(t, db, fmt.Sprintf("r13-operator-%d", fixtureID), "operator")
	viewer := seedStepUpUser(t, db, fmt.Sprintf("r13-viewer-%d", fixtureID), "viewer")
	jwtManager := newStepUpTestJWTManager(db)
	nodes := seedBatchNodes(t, db)

	handler := NewBatchHandler(db, nil).WithJWTManager(jwtManager)
	router := gin.New()
	router.Use(middleware.AuthMiddleware(jwtManager, db))
	router.POST("/batch-commands/:batch_id/retry", middleware.RBAC("tasks:write"), handler.Retry)

	return &retryTransactionFixture{
		db: db, jwt: jwtManager, handler: handler, router: router,
		admin: admin, operator: operator, viewer: viewer, nodes: nodes,
	}
}

func retryTransactionForEachEngine(t *testing.T, fn func(*testing.T, string)) {
	t.Helper()
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			fn(t, engine)
		})
	}
}

func retryTransactionIssueAuth(t *testing.T, fx *retryTransactionFixture, user model.User, action auth.StepUpAction) (string, string) {
	t.Helper()
	token := generatePrimaryToken(t, fx.jwt, user)
	proof := generateStepUpProofForAction(t, fx.jwt, user, action)
	return token, proof
}

func retryTransactionSeedGrant(t *testing.T, db *gorm.DB, user model.User, nodeID uint, action, status string, expiresAt time.Time) model.CredentialAccessGrant {
	t.Helper()
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID: user.ID, RequesterUsername: user.Username, RequesterRole: user.Role,
		Action: action, Purpose: sshutil.PurposeBatchCommand, NodeID: credentialaudit.PtrUint(nodeID),
		Reason: "R13 retry transaction test", Status: status, RequestedTTLSeconds: 600,
		RequestedAt: now, ApprovedAt: &now, ApproverUserID: credentialaudit.PtrUint(user.ID),
		ApproverUsername: user.Username, ExpiresAt: expiresAt,
	}
	if status == CredentialGrantStatusRevoked {
		revokedAt := now
		grant.RevokedAt = &revokedAt
		grant.RevokedByUserID = credentialaudit.PtrUint(user.ID)
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("seed retry credential grant: %v", err)
	}
	return grant
}

func retryTransactionSeedDispatchFailedRoot(t *testing.T, fx *retryTransactionFixture, requesterID uint, nodes []batchNode, nodeIDs []uint) (model.BatchCommand, []model.BatchCommandDispatch) {
	t.Helper()
	request := batchCommandRequest{
		NodeIDs: nodeIDs,
		Command: "printf 'r13 retry transaction'",
		Name:    "R13 retry transaction source",
		Retain:  true,
	}
	key := fmt.Sprintf("r13-root-%d", batchTestSequence.Add(1))
	batch, dispatches, created, err := fx.handler.createBatchTasks(context.Background(), requesterID, key, request, nodes)
	if err != nil {
		t.Fatalf("create retry source batch: %v", err)
	}
	if !created || len(dispatches) != len(nodes) {
		t.Fatalf("source batch created=%v dispatches=%d, want created and %d dispatches", created, len(dispatches), len(nodes))
	}
	if err := fx.handler.dispatchBatch(context.Background(), batch.ID, dispatches); err != nil {
		t.Fatalf("persist source dispatch failure: %v", err)
	}
	var persisted []model.BatchCommandDispatch
	if err := fx.db.Where("batch_id = ?", batch.ID).Order("task_id").Find(&persisted).Error; err != nil {
		t.Fatalf("reload source dispatches: %v", err)
	}
	for _, dispatch := range persisted {
		if dispatch.Status != "failed" || dispatch.RunID != 0 {
			t.Fatalf("source dispatch is not an unstarted failure: %+v", dispatch)
		}
	}
	return batch, persisted
}

func retryTransactionRequest(t *testing.T, router http.Handler, batchID, token, proof, key string, sources []batchRetrySource) *httptest.ResponseRecorder {
	t.Helper()
	requestPayload := struct {
		Sources []batchRetrySource `json:"sources"`
	}{Sources: append([]batchRetrySource(nil), sources...)}
	payload, err := json.Marshal(requestPayload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/batch-commands/"+batchID+"/retry", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(StepUpHeaderName, proof)
	req.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func retryTransactionDecodeResponse(t *testing.T, response *httptest.ResponseRecorder) retryTransactionResponse {
	t.Helper()
	var payload retryTransactionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode retry response status=%d body=%s: %v", response.Code, response.Body.String(), err)
	}
	return payload
}

func retryTransactionAssertNoChild(t *testing.T, db *gorm.DB, sourceBatchID string) {
	t.Helper()
	var childBatches, childTasks, childDispatches, sourceClaims int64
	if err := db.Unscoped().Model(&model.BatchCommand{}).Where("source_batch_id = ?", sourceBatchID).Count(&childBatches).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Model(&model.Task{}).Where("batch_id IN (SELECT id FROM batch_commands WHERE source_batch_id = ?)", sourceBatchID).Count(&childTasks).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Model(&model.BatchCommandDispatch{}).Where("batch_id IN (SELECT id FROM batch_commands WHERE source_batch_id = ?)", sourceBatchID).Count(&childDispatches).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().Model(&model.BatchCommandDispatch{}).Where("retry_of_task_id IS NOT NULL").Where("batch_id <> ?", sourceBatchID).Count(&sourceClaims).Error; err != nil {
		t.Fatal(err)
	}
	if childBatches != 0 || childTasks != 0 || childDispatches != 0 || sourceClaims != 0 {
		t.Fatalf("failed retry left rows: batches=%d tasks=%d dispatches=%d sourceClaims=%d", childBatches, childTasks, childDispatches, sourceClaims)
	}
}

func retryTransactionCreatedAudits(t *testing.T, db *gorm.DB, childBatchID string) []model.CredentialAuditEvent {
	t.Helper()
	var events []model.CredentialAuditEvent
	if err := db.Where("action = ? AND purpose = ? AND outcome = ?", string(auth.StepUpActionBatchCommandRetry), sshutil.PurposeBatchCommand, credentialaudit.OutcomeSuccess).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	created := make([]model.CredentialAuditEvent, 0, len(events))
	for _, event := range events {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
			t.Fatalf("decode retry audit metadata: %v", err)
		}
		if metadata["stage"] == "created" && metadata["batch_id"] == childBatchID {
			created = append(created, event)
		}
	}
	return created
}

func retryTransactionAssertChild(t *testing.T, fx *retryTransactionFixture, source model.BatchCommand, sourceDispatch model.BatchCommandDispatch, response *httptest.ResponseRecorder, token string, key string, expectedCommand ...string) string {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}
	payload := retryTransactionDecodeResponse(t, response)
	if payload.Data.SourceBatchID != source.ID || len(payload.Data.TaskIDs) != 1 || len(payload.Data.RunIDs) != 1 || payload.Data.Retain != source.Retain {
		t.Fatalf("retry identity envelope=%+v", payload.Data)
	}
	childID := payload.Data.BatchID
	if childID == "" || childID == source.ID {
		t.Fatalf("retry child identity=%q source=%q", childID, source.ID)
	}
	var child model.BatchCommand
	if err := fx.db.First(&child, "id = ?", childID).Error; err != nil {
		t.Fatalf("load retry child: %v", err)
	}
	if child.ProvenanceVersion != 1 || child.SourceBatchID == nil || *child.SourceBatchID != source.ID || child.Retain != source.Retain || child.IdempotencyKey != key {
		t.Fatalf("retry child provenance=%+v", child)
	}
	var sourceTask model.Task
	if err := fx.db.First(&sourceTask, "id = ?", sourceDispatch.TaskID).Error; err != nil {
		t.Fatalf("load retry source task: %v", err)
	}
	var task model.Task
	if err := fx.db.First(&task, "id = ?", payload.Data.TaskIDs[0]).Error; err != nil {
		t.Fatalf("load retry child task: %v", err)
	}
	wantCommand := sourceTask.Command
	if len(expectedCommand) == 1 {
		wantCommand = expectedCommand[0]
	}
	if task.ID == sourceDispatch.TaskID || task.BatchID != child.ID || task.Command != wantCommand || task.NodeID != sourceDispatch.NodeID {
		t.Fatalf("retry child task=%+v source task=%+v dispatch=%+v", task, sourceTask, sourceDispatch)
	}
	var dispatch model.BatchCommandDispatch
	if err := fx.db.First(&dispatch, "task_id = ?", task.ID).Error; err != nil {
		t.Fatalf("load retry child dispatch: %v", err)
	}
	if dispatch.BatchID != child.ID || dispatch.NodeID != sourceDispatch.NodeID || dispatch.RetryOfTaskID == nil || *dispatch.RetryOfTaskID != sourceDispatch.TaskID || dispatch.RetryOfRunID == nil || *dispatch.RetryOfRunID != sourceDispatch.RunID {
		t.Fatalf("retry source edge dispatch=%+v source=%+v", dispatch, sourceDispatch)
	}
	if dispatch.CommandFingerprint == "" || dispatch.CommandFingerprint != model.BatchCommandTaskFingerprint(task) {
		t.Fatalf("retry child fingerprint=%q task=%q", dispatch.CommandFingerprint, model.BatchCommandTaskFingerprint(task))
	}
	if sourceDispatch.CommandFingerprint == dispatch.CommandFingerprint {
		t.Fatal("retry child must have a distinct task fingerprint")
	}
	createdAudits := retryTransactionCreatedAudits(t, fx.db, child.ID)
	if len(createdAudits) != 1 {
		t.Fatalf("retry creation audit count=%d, want 1", len(createdAudits))
	}
	if strings.Contains(createdAudits[0].Metadata, task.Command) || strings.Contains(createdAudits[0].Metadata, dispatch.CommandFingerprint) {
		t.Fatalf("retry creation audit leaked command/fingerprint: %s", createdAudits[0].Metadata)
	}
	return child.ID
}

func TestBatchCommandRetryFullAuthTransactionSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, sourceDispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
		retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		sources := []batchRetrySource{{TaskID: sourceDispatches[0].TaskID, RunID: 0}}
		response := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-full-auth-key", sources)
		childID := retryTransactionAssertChild(t, fx, root, sourceDispatches[0], response, token, "r13-full-auth-key")

		// Replaying the exact actor/key is an identity replay, not a second source claim.
		replay := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-full-auth-key", sources)
		if replay.Code != http.StatusOK || retryTransactionDecodeResponse(t, replay).Data.BatchID != childID {
			t.Fatalf("same actor/key replay status=%d body=%s", replay.Code, replay.Body.String())
		}
		if got := len(retryTransactionCreatedAudits(t, fx.db, childID)); got != 1 {
			t.Fatalf("same actor/key replay created %d creation audits, want 1", got)
		}

		// The same key cannot be used for a different source pair.
		conflict := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-full-auth-key", []batchRetrySource{{TaskID: sourceDispatches[0].TaskID, RunID: 99}})
		if conflict.Code != http.StatusConflict {
			t.Fatalf("same key different payload status=%d body=%s", conflict.Code, conflict.Body.String())
		}
		var children int64
		if err := fx.db.Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&children).Error; err != nil {
			t.Fatal(err)
		}
		if children != 1 {
			t.Fatalf("different payload created %d child batches, want 1", children)
		}
	})
}

func TestBatchCommandRetryCompetingActorsConsumeSourceOnceSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
		retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		retryTransactionSeedGrant(t, fx.db, fx.operator, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		// Admin is owner-independent; operator needs explicit ownership of the node.
		if err := fx.db.Create(&model.NodeOwner{NodeID: fx.nodes[0].ID, UserID: fx.operator.ID}).Error; err != nil {
			t.Fatal(err)
		}
		adminToken, adminProof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		operatorToken, operatorProof := retryTransactionIssueAuth(t, fx, fx.operator, auth.StepUpActionBatchCommandRetry)
		source := []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}}

		start := make(chan struct{})
		responses := make(chan *httptest.ResponseRecorder, 2)
		var wg sync.WaitGroup
		for _, request := range []struct{ token, proof, key string }{
			{adminToken, adminProof, "r13-competing-admin"},
			{operatorToken, operatorProof, "r13-competing-operator"},
		} {
			request := request
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				responses <- retryTransactionRequest(t, fx.router, root.ID, request.token, request.proof, request.key, source)
			}()
		}
		close(start)
		wg.Wait()
		close(responses)
		var successes, conflicts int
		for response := range responses {
			switch response.Code {
			case http.StatusOK:
				successes++
			case http.StatusConflict:
				conflicts++
			default:
				t.Fatalf("competing retry status=%d body=%s", response.Code, response.Body.String())
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("competing retry successes=%d conflicts=%d, want one each", successes, conflicts)
		}
		var children, claims int64
		if err := fx.db.Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&children).Error; err != nil {
			t.Fatal(err)
		}
		if err := fx.db.Model(&model.BatchCommandDispatch{}).Where("retry_of_task_id = ? AND retry_of_run_id = 0", dispatches[0].TaskID).Count(&claims).Error; err != nil {
			t.Fatal(err)
		}
		if children != 1 || claims != 1 {
			t.Fatalf("competing retry children=%d claims=%d, want one each", children, claims)
		}
	})
}

func TestBatchCommandRetrySourceAndChildDeletionReplaySemanticsSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
		retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		sources := []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}}
		first := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-delete-replay-key", sources)
		childID := retryTransactionAssertChild(t, fx, root, dispatches[0], first, token, "r13-delete-replay-key")

		deletedAt := time.Now().UTC()
		if err := fx.db.Model(&model.BatchCommand{}).Where("id = ?", root.ID).Update("deleted_at", deletedAt).Error; err != nil {
			t.Fatal(err)
		}
		replayAfterSourceDelete := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-delete-replay-key", sources)
		if replayAfterSourceDelete.Code != http.StatusOK || retryTransactionDecodeResponse(t, replayAfterSourceDelete).Data.BatchID != childID {
			t.Fatalf("source-deleted replay status=%d body=%s", replayAfterSourceDelete.Code, replayAfterSourceDelete.Body.String())
		}

		if err := fx.db.Model(&model.BatchCommand{}).Where("id = ?", childID).Update("deleted_at", time.Now().UTC()).Error; err != nil {
			t.Fatal(err)
		}
		deletedChildReplay := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-delete-replay-key", sources)
		if deletedChildReplay.Code != http.StatusConflict {
			t.Fatalf("deleted child replay status=%d body=%s", deletedChildReplay.Code, deletedChildReplay.Body.String())
		}
		var children int64
		if err := fx.db.Unscoped().Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&children).Error; err != nil {
			t.Fatal(err)
		}
		if children != 1 {
			t.Fatalf("deleted child replay created another child; tombstoned children=%d", children)
		}
	})
}

func TestBatchCommandRetryIneligibleGroupRollsBackAllRowsSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes, []uint{fx.nodes[0].ID, fx.nodes[1].ID})
		if err := fx.db.Model(&model.Task{}).Where("id = ?", dispatches[1].TaskID).Update("command", "printf 'r13 changed source'").Error; err != nil {
			t.Fatal(err)
		}
		for _, node := range fx.nodes {
			retryTransactionSeedGrant(t, fx.db, fx.admin, node.ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		}
		token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		sources := []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}, {TaskID: dispatches[1].TaskID, RunID: 0}}
		response := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-ineligible-group-key", sources)
		if response.Code != http.StatusConflict {
			t.Fatalf("ineligible group status=%d body=%s", response.Code, response.Body.String())
		}
		retryTransactionAssertNoChild(t, fx.db, root.ID)
		var sourceTasks int64
		if err := fx.db.Model(&model.Task{}).Where("batch_id = ?", root.ID).Count(&sourceTasks).Error; err != nil {
			t.Fatal(err)
		}
		if sourceTasks != 2 {
			t.Fatalf("ineligible group changed source task count=%d", sourceTasks)
		}
	})
}

func TestBatchCommandRetryDuplicateSelectedNodeRefusedSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, []batchNode{fx.nodes[0], fx.nodes[0]}, []uint{fx.nodes[0].ID, fx.nodes[0].ID})
		retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		response := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-duplicate-node-key", []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}, {TaskID: dispatches[1].TaskID, RunID: 0}})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("duplicate node status=%d body=%s", response.Code, response.Body.String())
		}
		retryTransactionAssertNoChild(t, fx.db, root.ID)
	})
}

func TestBatchCommandRetryAuditTaskDispatchRollbackSQLitePostgres(t *testing.T) {
	for _, failure := range []string{"task", "dispatch", "audit"} {
		t.Run(failure, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				fx := newRetryTransactionFixture(t, engine)
				root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
				retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
				injected := errors.New("R13_RETRY_TRANSACTION_INJECTED_FAILURE")
				callbackName := fmt.Sprintf("retry-transaction-%s-failure-%d", failure, batchTestSequence.Add(1))
				var callbackErr error
				switch failure {
				case "task":
					callbackErr = fx.db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
						if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "tasks" {
							_ = tx.AddError(injected)
						}
					})
				case "dispatch":
					callbackErr = fx.db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
						if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "batch_command_dispatches" {
							_ = tx.AddError(injected)
						}
					})
				default:
					callbackErr = fx.db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
						record, ok := tx.Statement.Dest.(*model.CredentialAuditEvent)
						if ok && record.Action == string(auth.StepUpActionBatchCommandRetry) && strings.Contains(record.Metadata, `"stage":"created"`) {
							_ = tx.AddError(injected)
						}
					})
				}
				if callbackErr != nil {
					t.Fatal(callbackErr)
				}
				t.Cleanup(func() { _ = fx.db.Callback().Create().Remove(callbackName) })

				token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
				response := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-rollback-"+failure, []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}})
				if response.Code != http.StatusInternalServerError {
					t.Fatalf("%s injection status=%d body=%s", failure, response.Code, response.Body.String())
				}
				retryTransactionAssertNoChild(t, fx.db, root.ID)
				if len(retryTransactionCreatedAudits(t, fx.db, "never-created")) != 0 {
					t.Fatal("failed retry unexpectedly wrote creation audit")
				}
			})
		})
	}
}

func TestBatchCommandRetryAuthDenialsSQLitePostgres(t *testing.T) {
	cases := []struct {
		name       string
		user       func(*retryTransactionFixture) model.User
		proof      func(*testing.T, *retryTransactionFixture, model.User) string
		grant      func(*testing.T, *retryTransactionFixture, model.User, uint)
		prepare    func(*testing.T, *retryTransactionFixture)
		wantStatus int
	}{
		{
			name: "wrong_action_proof",
			user: func(fx *retryTransactionFixture) model.User { return fx.admin },
			proof: func(t *testing.T, fx *retryTransactionFixture, user model.User) string {
				return generateStepUpProofForAction(t, fx.jwt, user, auth.StepUpActionBatchCommandCreate)
			},
			grant: func(t *testing.T, fx *retryTransactionFixture, user model.User, nodeID uint) {
				retryTransactionSeedGrant(t, fx.db, user, nodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "wrong_action_grant",
			user: func(fx *retryTransactionFixture) model.User { return fx.admin },
			proof: func(t *testing.T, fx *retryTransactionFixture, user model.User) string {
				return generateStepUpProofForAction(t, fx.jwt, user, auth.StepUpActionBatchCommandRetry)
			},
			grant: func(t *testing.T, fx *retryTransactionFixture, user model.User, nodeID uint) {
				retryTransactionSeedGrant(t, fx.db, user, nodeID, CredentialGrantActionBatchCommand, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "viewer_rbac",
			user: func(fx *retryTransactionFixture) model.User { return fx.viewer },
			proof: func(t *testing.T, fx *retryTransactionFixture, user model.User) string {
				return generateStepUpProofForAction(t, fx.jwt, user, auth.StepUpActionBatchCommandRetry)
			},
			grant: func(t *testing.T, fx *retryTransactionFixture, user model.User, nodeID uint) {
				retryTransactionSeedGrant(t, fx.db, user, nodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "cross_owner_operator",
			user: func(fx *retryTransactionFixture) model.User { return fx.operator },
			proof: func(t *testing.T, fx *retryTransactionFixture, user model.User) string {
				return generateStepUpProofForAction(t, fx.jwt, user, auth.StepUpActionBatchCommandRetry)
			},
			grant: func(t *testing.T, fx *retryTransactionFixture, user model.User, nodeID uint) {
				retryTransactionSeedGrant(t, fx.db, user, nodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "expired_grant",
			user: func(fx *retryTransactionFixture) model.User { return fx.admin },
			proof: func(t *testing.T, fx *retryTransactionFixture, user model.User) string {
				return generateStepUpProofForAction(t, fx.jwt, user, auth.StepUpActionBatchCommandRetry)
			},
			grant: func(t *testing.T, fx *retryTransactionFixture, user model.User, nodeID uint) {
				retryTransactionSeedGrant(t, fx.db, user, nodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(-time.Minute))
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name: "revoked_grant",
			user: func(fx *retryTransactionFixture) model.User { return fx.admin },
			proof: func(t *testing.T, fx *retryTransactionFixture, user model.User) string {
				return generateStepUpProofForAction(t, fx.jwt, user, auth.StepUpActionBatchCommandRetry)
			},
			grant: func(t *testing.T, fx *retryTransactionFixture, user model.User, nodeID uint) {
				retryTransactionSeedGrant(t, fx.db, user, nodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusRevoked, time.Now().UTC().Add(time.Hour))
			},
			wantStatus: http.StatusForbidden,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				fx := newRetryTransactionFixture(t, engine)
				root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
				if testCase.prepare != nil {
					testCase.prepare(t, fx)
				}
				user := testCase.user(fx)
				if testCase.name == "cross_owner_operator" {
					if err := fx.db.Create(&model.NodeOwner{NodeID: fx.nodes[0].ID, UserID: fx.admin.ID}).Error; err != nil {
						t.Fatal(err)
					}
				}
				testCase.grant(t, fx, user, fx.nodes[0].ID)
				token := generatePrimaryToken(t, fx.jwt, user)
				proof := testCase.proof(t, fx, user)
				response := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-auth-denial-"+testCase.name, []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}})
				if response.Code != testCase.wantStatus {
					t.Fatalf("%s status=%d body=%s", testCase.name, response.Code, response.Body.String())
				}
				retryTransactionAssertNoChild(t, fx.db, root.ID)
			})
		})
	}
}

func retryTransactionInstallForUpdateGate(t *testing.T, db *gorm.DB, table string) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	callbackName := fmt.Sprintf("retry-transaction-for-update-gate-%s-%d", table, batchTestSequence.Add(1))
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		tableName := tx.Statement.Table
		if tableName == "" && tx.Statement.Schema != nil {
			tableName = tx.Statement.Schema.Table
		}
		if tableName != table {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; !ok {
			return
		}
		once.Do(func() {
			close(reached)
			<-release
		})
	}); err != nil {
		t.Fatalf("register production lock gate: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	return reached, release
}

// retryTransactionInstallTaskMutationGate synchronizes after a real
// production task write-lock statement has executed. Archive and UpdateTask
// deliberately use a same-row UPDATE as SQLite's write-lock primitive, so
// this gate watches the actual UPDATE callback rather than request entry.
func retryTransactionInstallTaskMutationGate(t *testing.T, db *gorm.DB) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	updateName := fmt.Sprintf("retry-transaction-task-mutation-update-%d", batchTestSequence.Add(1))
	signal := func() {
		once.Do(func() {
			close(reached)
			<-release
		})
	}
	if err := db.Callback().Update().After("gorm:update").Register(updateName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		tableName := tx.Statement.Table
		if tableName == "" && tx.Statement.Schema != nil {
			tableName = tx.Statement.Schema.Table
		}
		if tableName == "tasks" {
			signal()
		}
	}); err != nil {
		t.Fatalf("register task mutation update gate: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(updateName) })
	return reached, release
}

func retryTransactionInstallTaskRunCreateGate(t *testing.T, db *gorm.DB) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	callbackName := fmt.Sprintf("retry-transaction-task-run-create-%d", batchTestSequence.Add(1))
	if err := db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		tableName := tx.Statement.Table
		if tableName == "" && tx.Statement.Schema != nil {
			tableName = tx.Statement.Schema.Table
		}
		if tableName != "task_runs" {
			return
		}
		if _, ok := tx.Statement.Dest.(*model.TaskRun); !ok {
			return
		}
		once.Do(func() {
			close(reached)
			<-release
		})
	}); err != nil {
		t.Fatalf("register task run create gate: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	return reached, release
}

func retryTransactionInstallChildBatchGate(t *testing.T, db *gorm.DB) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	callbackName := fmt.Sprintf("retry-transaction-child-batch-gate-%d", batchTestSequence.Add(1))
	if err := db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		batch, ok := tx.Statement.Dest.(*model.BatchCommand)
		if !ok || batch.SourceBatchID == nil || tx.Error != nil {
			return
		}
		once.Do(func() {
			close(reached)
			<-release
		})
	}); err != nil {
		t.Fatalf("register child batch write gate: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	return reached, release
}

func retryTransactionInstallProductionMutationGate(t *testing.T, db *gorm.DB, race string) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	switch race {
	case "archive", "edit":
		return retryTransactionInstallTaskMutationGate(t, db)
	case "manual_reservation":
		return retryTransactionInstallTaskRunCreateGate(t, db)
	default:
		t.Fatalf("unknown production mutation race %q", race)
		return nil, nil
	}
}

func retryTransactionWaitForGate(t *testing.T, reached <-chan struct{}) {
	t.Helper()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("retry transaction did not reach deterministic lock gate")
	}
}

func TestBatchCommandRetryLockWaitProofAndRoleInvalidationSQLitePostgres(t *testing.T) {
	for _, mode := range []string{"proof_expiry", "role_transition"} {
		t.Run(mode, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				fx := newRetryTransactionFixture(t, engine)
				root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
				retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
				token := generatePrimaryToken(t, fx.jwt, fx.admin)
				proof := generateStepUpProofForAction(t, fx.jwt, fx.admin, auth.StepUpActionBatchCommandRetry)
				var proofExpiresAt time.Time
				var reached <-chan struct{}
				var release chan<- struct{}
				if mode == "proof_expiry" {
					proof, proofExpiresAt = retryTransactionShortLivedProof(t, fx.admin, auth.StepUpActionBatchCommandRetry, 3*time.Second)
					reached, release = retryTransactionInstallForUpdateGate(t, fx.db, "tasks")
				} else {
					// AuthMiddleware has already admitted the primary token when this
					// gate fires. Persisting the role change while the request is
					// blocked then exercises the handler's transaction-time role
					// check, rather than mutating Gin context state.
					reachedGate := make(chan struct{})
					releaseGate := make(chan struct{})
					fx.router = gin.New()
					fx.router.Use(middleware.AuthMiddleware(fx.jwt, fx.db))
					fx.router.Use(func(c *gin.Context) {
						close(reachedGate)
						<-releaseGate
						c.Next()
					})
					fx.router.POST("/batch-commands/:batch_id/retry", middleware.RBAC("tasks:write"), fx.handler.Retry)
					reached, release = reachedGate, releaseGate
				}
				responseCh := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					responseCh <- retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-lock-wait-"+mode, []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}})
				}()
				retryTransactionWaitForGate(t, reached)
				if mode == "proof_expiry" {
					// Wait for the proof's encoded expiry boundary, not an ordering sleep.
					wait := time.Until(proofExpiresAt.Add(100 * time.Millisecond))
					if wait > 0 {
						timer := time.NewTimer(wait)
						<-timer.C
						timer.Stop()
					}
				} else {
					if err := fx.db.Model(&model.User{}).Where("id = ?", fx.admin.ID).Update("role", "viewer").Error; err != nil {
						t.Fatal(err)
					}
				}
				close(release)
				response := <-responseCh
				if response.Code != http.StatusForbidden {
					t.Fatalf("lock-wait %s status=%d body=%s", mode, response.Code, response.Body.String())
				}
				retryTransactionAssertNoChild(t, fx.db, root.ID)
			})
		})
	}
}

func retryTransactionShortLivedProof(t *testing.T, user model.User, action auth.StepUpAction, lifetime time.Duration) (string, time.Time) {
	t.Helper()
	if lifetime <= 0 {
		lifetime = time.Second
	}
	expiresAt := time.Now().UTC().Add(lifetime).Truncate(time.Second)
	issuedAt := expiresAt.Add(-auth.StepUpProofTTLForAction(action))
	claims := auth.Claims{
		UserID: user.ID, Username: user.Username, Role: user.Role,
		Purpose: auth.PurposeStepUp, StepUpAction: action, TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID: strings.Repeat("e", 32), IssuedAt: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt), Subject: fmt.Sprintf("%d", user.ID),
		},
	}
	return signStepUpClaimsForTest(t, claims), expiresAt
}

func retryTransactionSeedExecutionHistory(t *testing.T, fx *retryTransactionFixture, outcome string) (model.BatchCommand, model.Task, model.BatchCommandDispatch, model.TaskRun) {
	t.Helper()
	root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
	var sourceTask model.Task
	if err := fx.db.First(&sourceTask, "id = ?", dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{TaskID: sourceTask.ID, NodeIDSnapshot: sourceTask.NodeID, ExecutorTypeSnapshot: "command", TriggerType: "manual", Status: model.TaskRunStatusPending}
	if err := fx.db.Create(&run).Error; err != nil {
		t.Fatalf("create authoritative retry history run: %v", err)
	}
	finishedAt := time.Now().UTC()
	if err := fx.db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.TaskRunStatusFailed, "finished_at": finishedAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Model(&model.Task{}).Where("id = ?", sourceTask.ID).Update("status", model.TaskRunStatusFailed).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.First(&sourceTask, "id = ?", sourceTask.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Model(&model.BatchCommandDispatch{}).Where("task_id = ?", sourceTask.ID).Updates(map[string]any{"status": "accepted", "run_id": run.ID}).Error; err != nil {
		t.Fatal(err)
	}
	var evidence model.BatchCommandRunEvidence
	if err := fx.db.First(&evidence, "run_id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	fingerprint := model.BatchCommandTaskFingerprint(sourceTask)
	updates := map[string]any{"outcome": outcome, "finished_at": finishedAt, "command_fingerprint": fingerprint, "exit_code": nil}
	switch outcome {
	case "exit_failure":
		code := 7
		updates["exit_code"] = code
	case "empty_fingerprint":
		updates["outcome"] = "exit_failure"
		updates["command_fingerprint"] = ""
		code := 7
		updates["exit_code"] = code
	case "mismatched_fingerprint":
		updates["outcome"] = "exit_failure"
		wrong := strings.Repeat("0", 64)
		if wrong == fingerprint {
			wrong = strings.Repeat("1", 64)
		}
		updates["command_fingerprint"] = wrong
		code := 7
		updates["exit_code"] = code
	case "pending":
		updates["finished_at"] = nil
	case "unknown", "success":
		// Keep the authoritative run terminal while preserving the deliberately
		// non-eligible evidence state under test.
	default:
		t.Fatalf("unsupported retry history outcome %q", outcome)
	}
	if err := fx.db.Model(&model.BatchCommandRunEvidence{}).Where("run_id = ?", run.ID).Updates(updates).Error; err != nil {
		t.Fatal(err)
	}
	var persistedDispatch model.BatchCommandDispatch
	if err := fx.db.First(&persistedDispatch, "task_id = ?", sourceTask.ID).Error; err != nil {
		t.Fatal(err)
	}
	return root, sourceTask, persistedDispatch, run
}

func TestBatchCommandRetryEvidenceHistoryFailsClosedAfterRunCleanupSQLitePostgres(t *testing.T) {
	for _, outcome := range []string{"unknown", "pending", "success", "empty_fingerprint", "mismatched_fingerprint"} {
		t.Run(outcome, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				fx := newRetryTransactionFixture(t, engine)
				root, sourceTask, dispatch, run := retryTransactionSeedExecutionHistory(t, fx, outcome)
				requestedRunID := run.ID
				candidate, err := assessBatchRetryEligibility(fx.db, root, sourceTask, dispatch, &requestedRunID)
				if err != nil {
					t.Fatal(err)
				}
				if candidate.Eligible || candidate.Reason != batchRetryReasonHistoryUnverified {
					t.Fatalf("retained %s evidence eligibility=%+v", outcome, candidate)
				}
				if err := fx.db.Delete(&model.TaskRun{}, run.ID).Error; err != nil {
					t.Fatal(err)
				}
				candidate, err = assessBatchRetryEligibility(fx.db, root, sourceTask, dispatch, &requestedRunID)
				if err != nil {
					t.Fatal(err)
				}
				if candidate.Eligible || candidate.Reason != batchRetryReasonHistoryUnverified || candidate.RunID != 0 {
					t.Fatalf("cleaned %s evidence eligibility=%+v", outcome, candidate)
				}
			})
		})
	}
}

func TestBatchCommandRetryCleanedNewestRunCannotFallBackSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
		var sourceTask model.Task
		if err := fx.db.First(&sourceTask, dispatches[0].TaskID).Error; err != nil {
			t.Fatal(err)
		}
		makeRun := func(trigger string) model.TaskRun {
			run := model.TaskRun{TaskID: sourceTask.ID, NodeIDSnapshot: sourceTask.NodeID, ExecutorTypeSnapshot: "command", TriggerType: trigger, Status: model.TaskRunStatusPending}
			if err := fx.db.Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			finishedAt := time.Now().UTC()
			if err := fx.db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.TaskRunStatusFailed, "finished_at": finishedAt}).Error; err != nil {
				t.Fatal(err)
			}
			var evidence model.BatchCommandRunEvidence
			if err := fx.db.First(&evidence, "run_id = ?", run.ID).Error; err != nil {
				t.Fatal(err)
			}
			code := 7
			if err := fx.db.Model(&model.BatchCommandRunEvidence{}).Where("run_id = ?", run.ID).Updates(map[string]any{"outcome": "exit_failure", "exit_code": code, "finished_at": finishedAt, "command_fingerprint": model.BatchCommandTaskFingerprint(sourceTask)}).Error; err != nil {
				t.Fatal(err)
			}
			return run
		}
		older := makeRun("manual")
		newer := makeRun("retry")
		if err := fx.db.Model(&model.Task{}).Where("id = ?", sourceTask.ID).Update("status", model.TaskRunStatusFailed).Error; err != nil {
			t.Fatal(err)
		}
		if err := fx.db.First(&sourceTask, "id = ?", sourceTask.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := fx.db.Model(&model.BatchCommandDispatch{}).Where("task_id = ?", sourceTask.ID).Updates(map[string]any{"status": "accepted", "run_id": newer.ID}).Error; err != nil {
			t.Fatal(err)
		}
		if err := fx.db.Delete(&model.TaskRun{}, newer.ID).Error; err != nil {
			t.Fatal(err)
		}
		var dispatch model.BatchCommandDispatch
		if err := fx.db.First(&dispatch, "task_id = ?", sourceTask.ID).Error; err != nil {
			t.Fatal(err)
		}
		candidate, err := assessBatchRetryEligibility(fx.db, root, sourceTask, dispatch, nil)
		if err != nil {
			t.Fatal(err)
		}
		if candidate.Eligible || candidate.Reason != batchRetryReasonHistoryUnverified || candidate.RunID != 0 {
			t.Fatalf("cleaned newest run fell back to older run=%d: %+v", older.ID, candidate)
		}
	})
}

func retryTransactionSeedKnownFailureRoot(t *testing.T, fx *retryTransactionFixture) (model.BatchCommand, model.BatchCommandDispatch, model.BatchCommandRunEvidence) {
	t.Helper()
	if err := fx.db.Model(&model.Node{}).Where("id = ?", fx.nodes[0].ID).Update("host", "").Error; err != nil {
		t.Fatal(err)
	}
	manager := taskruntime.NewManager(fx.db, executor.NewFactory(""), nil, nil, nil, nil, 8, 90)
	manager.SetAutomationDispatcher(automation.NewDispatcher(fx.db))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	handler := NewBatchHandler(fx.db, manager)
	root, dispatches, created, err := handler.createBatchTasks(context.Background(), fx.admin.ID, fmt.Sprintf("r13-known-failure-%d", batchTestSequence.Add(1)), batchCommandRequest{NodeIDs: []uint{fx.nodes[0].ID}, Command: "printf 'r13 known failure'", Name: "R13 known failure", Retain: true}, fx.nodes[:1])
	if err != nil || !created || len(dispatches) != 1 {
		t.Fatalf("create known failure source created=%v dispatches=%d err=%v", created, len(dispatches), err)
	}
	if err := fx.db.Model(&model.Task{}).Where("id = ?", dispatches[0].TaskID).Update("retry_count", 2).Error; err != nil {
		t.Fatalf("disable automatic retry window: %v", err)
	}
	if err := handler.dispatchBatch(context.Background(), root.ID, dispatches); err != nil {
		t.Fatal(err)
	}
	var persistedDispatch model.BatchCommandDispatch
	if err := fx.db.First(&persistedDispatch, "task_id = ?", dispatches[0].TaskID).Error; err != nil {
		t.Fatal(err)
	}
	if persistedDispatch.Status != "accepted" || persistedDispatch.RunID == 0 {
		t.Fatalf("known failure dispatch=%+v", persistedDispatch)
	}
	retryTransactionWaitForTerminalRun(t, fx.db, persistedDispatch.RunID)
	// The real runner records a no-start terminal evidence row. The retry
	// count is preloaded to the default terminal limit before dispatch, so
	// no retry intent is published; wait for all actual terminal effects to
	// settle before checking admission.
	retryTransactionWaitForTerminalEffects(t, fx.db, persistedDispatch.RunID)
	var settledTask model.Task
	if err := fx.db.First(&settledTask, persistedDispatch.TaskID).Error; err != nil {
		t.Fatalf("reload settled known failure task: %v", err)
	}
	if settledTask.Status != model.TaskRunStatusFailed || settledTask.NextRunAt != nil {
		t.Fatalf("real no-start task did not settle failed: %+v", settledTask)
	}
	var evidence model.BatchCommandRunEvidence
	if err := fx.db.First(&evidence, "run_id = ?", persistedDispatch.RunID).Error; err != nil {
		t.Fatalf("reload known failure evidence: %v", err)
	}
	if evidence.Outcome != "not_started" || evidence.FinishedAt == nil {
		t.Fatalf("real no-start evidence=%+v", evidence)
	}
	var sourceTask model.Task
	if err := fx.db.First(&sourceTask, persistedDispatch.TaskID).Error; err != nil {
		t.Fatalf("reload known failure task: %v", err)
	}
	requestedRunID := persistedDispatch.RunID
	candidate, err := assessBatchRetryEligibility(fx.db, root, sourceTask, persistedDispatch, &requestedRunID)
	if err != nil {
		t.Fatalf("assess known failure eligibility: %v", err)
	}
	if !candidate.Eligible {
		t.Fatalf("real known failure is not eligible: %+v", candidate)
	}
	return root, persistedDispatch, evidence
}

func retryTransactionWaitForTerminalRun(t *testing.T, db *gorm.DB, runID uint) model.TaskRun {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var run model.TaskRun
		if err := db.First(&run, "id = ?", runID).Error; err != nil {
			t.Fatal(err)
		}
		if model.IsTerminalTaskRunStatus(run.Status) && run.FinishedAt != nil {
			return run
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("TaskRun %d did not reach a terminal state", runID)
		}
	}
}

func retryTransactionWaitForTerminalEffects(t *testing.T, db *gorm.DB, runID uint) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pending int64
		if err := db.Model(&model.TaskRunEffect{}).
			Where("task_run_id = ? AND status <> ?", runID, model.TaskRunEffectStatusSucceeded).
			Count(&pending).Error; err != nil {
			t.Fatalf("count terminal effects for run %d: %v", runID, err)
		}
		if pending == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			var effects []model.TaskRunEffect
			if err := db.Where("task_run_id = ? AND status <> ?", runID, model.TaskRunEffectStatusSucceeded).Find(&effects).Error; err != nil {
				t.Fatalf("load remaining terminal effects: %v", err)
			}
			t.Fatalf("TaskRun %d retained %d non-success effects: %+v", runID, pending, effects)
		}
	}
}

func TestBatchCommandRetryKnownFailureAdmissionSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, sourceDispatch, evidence := retryTransactionSeedKnownFailureRoot(t, fx)
		retryTransactionSeedGrant(t, fx.db, fx.admin, sourceDispatch.NodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		response := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-known-failure-admission", []batchRetrySource{{TaskID: sourceDispatch.TaskID, RunID: sourceDispatch.RunID}})
		if evidence.Outcome != "not_started" {
			t.Fatalf("known failure evidence changed before admission: %+v", evidence)
		}
		retryTransactionAssertChild(t, fx, root, sourceDispatch, response, token, "r13-known-failure-admission")
	})
}

func TestBatchCommandRetryArchiveEditManualRacesSQLitePostgres(t *testing.T) {
	for _, race := range []string{"archive", "edit", "manual_reservation"} {
		t.Run(race, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				for _, order := range []string{"before_retry", "after_retry"} {
					t.Run(order, func(t *testing.T) {
						fx := newRetryTransactionFixture(t, engine)
						root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
						retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
						token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
						source := []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}}
						if order == "before_retry" {
							response := retryTransactionRunMutationFirst(t, fx.db, dispatches[0], race, func() *httptest.ResponseRecorder {
								return retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-race-"+race+"-"+order, source)
							})
							if response.Code == http.StatusOK {
								t.Fatalf("%s mutation-first race unexpectedly created a child: %s", race, response.Body.String())
							}
							retryTransactionAssertNoChild(t, fx.db, root.ID)
							return
						}
						reached, release := retryTransactionInstallChildBatchGate(t, fx.db)
						responseCh := make(chan *httptest.ResponseRecorder, 1)
						go func() {
							responseCh <- retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-race-"+race+"-"+order, source)
						}()
						retryTransactionWaitForGate(t, reached)
						var mutationReached <-chan struct{}
						var mutationRelease chan<- struct{}
						mutationReached, mutationRelease = retryTransactionInstallProductionMutationGate(t, fx.db, race)
						mutationDone := retryTransactionRunProductionMutation(t, fx.db, dispatches[0], race)
						close(release)
						retryTransactionWaitForGate(t, mutationReached)
						close(mutationRelease)
						response := <-responseCh
						if err := <-mutationDone; err != nil {
							t.Fatal(err)
						}
						switch race {
						case "archive":
							if response.Code != http.StatusOK {
								t.Fatalf("retry-first archive race status=%d body=%s", response.Code, response.Body.String())
							}
							var childCount int64
							if err := fx.db.Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&childCount).Error; err != nil {
								t.Fatal(err)
							}
							if childCount != 1 {
								t.Fatalf("retry-first archive race child count=%d, want 1", childCount)
							}
						case "edit":
							if response.Code != http.StatusOK {
								t.Fatalf("retry-first edit race status=%d body=%s", response.Code, response.Body.String())
							}
							retryTransactionAssertChild(t, fx, root, dispatches[0], response, token, "r13-race-edit-after_retry", "printf 'r13 retry transaction'")
						default:
							if response.Code != http.StatusOK {
								t.Fatalf("retry-first manual reservation race status=%d body=%s", response.Code, response.Body.String())
							}
							var runCount int64
							if err := fx.db.Model(&model.TaskRun{}).Where("task_id = ?", dispatches[0].TaskID).Count(&runCount).Error; err != nil {
								t.Fatal(err)
							}
							if runCount != 1 {
								t.Fatalf("manual reservation race run count=%d, want 1", runCount)
							}
						}
					})
				}
			})
		})
	}
}

func retryTransactionRunMutationFirst(t *testing.T, db *gorm.DB, dispatch model.BatchCommandDispatch, race string, runRetry func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	reached, release := retryTransactionInstallProductionMutationGate(t, db, race)
	mutationDone := retryTransactionRunProductionMutation(t, db, dispatch, race)
	retryTransactionWaitForGate(t, reached)
	responseCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseCh <- runRetry()
	}()
	close(release)
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	return <-responseCh
}

func retryTransactionRunProductionMutation(t *testing.T, db *gorm.DB, dispatch model.BatchCommandDispatch, race string) chan error {
	t.Helper()
	done := make(chan error, 1)
	var manager *taskruntime.Manager
	if race == "manual_reservation" {
		manager = taskruntime.NewManager(db, executor.NewFactory(""), nil, nil, nil, nil, 8, 90)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = manager.Shutdown(ctx)
		})
	}
	go func() {
		switch race {
		case "archive":
			archive := taskruntime.NewArchiveService(taskruntime.ArchiveDependencies{DB: db})
			_, err := archive.Archive(context.Background(), dispatch.TaskID)
			done <- err
		case "edit":
			var current model.Task
			if err := db.First(&current, dispatch.TaskID).Error; err != nil {
				done <- err
				return
			}
			command := "printf 'r13 edit before retry'"
			_, err := taskruntime.NewTaskApiService(
				gormrepo.NewTaskRepository(db),
				gormrepo.NewNodeRepository(db),
				gormrepo.NewPolicyRepository(db),
				nil,
			).UpdateTask(context.Background(), dispatch.TaskID, taskruntime.UpdateTaskInput{
				ExpectedRevision: taskruntime.TaskRevision(current),
				Command:          &command,
			})
			done <- err
		case "manual_reservation":
			_, err := manager.TriggerManual(dispatch.TaskID)
			done <- err
		default:
			done <- fmt.Errorf("unknown retry mutation race %q", race)
		}
	}()
	return done
}

func TestBatchCommandRetryExecutionSourceRequiresPositiveLatestRunSQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, sourceTask, sourceDispatch, run := retryTransactionSeedExecutionHistory(t, fx, "exit_failure")
		retryTransactionSeedGrant(t, fx.db, fx.admin, sourceDispatch.NodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		source := []batchRetrySource{{TaskID: sourceTask.ID, RunID: 0}}

		zeroRun := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-execution-zero-run", source)
		if zeroRun.Code != http.StatusConflict {
			t.Fatalf("zero run execution source status=%d body=%s", zeroRun.Code, zeroRun.Body.String())
		}
		retryTransactionAssertNoChild(t, fx.db, root.ID)

		legitimate := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-execution-zero-run", []batchRetrySource{{TaskID: sourceTask.ID, RunID: run.ID}})
		childID := retryTransactionAssertChild(t, fx, root, sourceDispatch, legitimate, token, "r13-execution-zero-run")
		replay := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-execution-zero-run", []batchRetrySource{{TaskID: sourceTask.ID, RunID: run.ID}})
		if replay.Code != http.StatusOK || retryTransactionDecodeResponse(t, replay).Data.BatchID != childID {
			t.Fatalf("legitimate source replay status=%d body=%s", replay.Code, replay.Body.String())
		}
		var children int64
		if err := fx.db.Model(&model.BatchCommand{}).Where("source_batch_id = ?", root.ID).Count(&children).Error; err != nil {
			t.Fatal(err)
		}
		if children != 1 {
			t.Fatalf("execution source created %d child batches, want exactly one", children)
		}
	})
}

func TestBatchCommandRetryExecutorConfigSourceParitySQLitePostgres(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		fx := newRetryTransactionFixture(t, engine)
		root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
		if err := fx.db.Model(&model.Task{}).Where("id = ?", dispatches[0].TaskID).UpdateColumn("executor_config", `{"private":"R13_CONFIG_SOURCE_CHANGED"}`).Error; err != nil {
			t.Fatal(err)
		}
		fx.router.GET("/batch-commands/:batch_id", fx.handler.Get)
		token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
		getRequest := httptest.NewRequest(http.MethodGet, "/batch-commands/"+root.ID, nil)
		getRequest.Header.Set("Authorization", "Bearer "+token)
		getResponse := httptest.NewRecorder()
		fx.router.ServeHTTP(getResponse, getRequest)
		if getResponse.Code != http.StatusOK {
			t.Fatalf("GET source config parity status=%d body=%s", getResponse.Code, getResponse.Body.String())
		}
		var getPayload struct {
			Data struct {
				RetrySources []batchRetryEligibilityDTO `json:"retry_sources"`
			} `json:"data"`
		}
		if err := json.Unmarshal(getResponse.Body.Bytes(), &getPayload); err != nil {
			t.Fatal(err)
		}
		if len(getPayload.Data.RetrySources) != 1 || getPayload.Data.RetrySources[0].Reason != batchRetryReasonSourceChanged || getPayload.Data.RetrySources[0].Eligible {
			t.Fatalf("GET source config qualification=%+v", getPayload.Data.RetrySources)
		}
		if strings.Contains(getResponse.Body.String(), "R13_CONFIG_SOURCE_CHANGED") || strings.Contains(getResponse.Body.String(), "executor_config") {
			t.Fatalf("GET exposed private executor qualification input: %s", getResponse.Body.String())
		}

		retryTransactionSeedGrant(t, fx.db, fx.admin, dispatches[0].NodeID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
		postResponse := retryTransactionRequest(t, fx.router, root.ID, token, proof, "r13-source-config-parity", []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}})
		if postResponse.Code != http.StatusConflict {
			t.Fatalf("POST source config parity status=%d body=%s", postResponse.Code, postResponse.Body.String())
		}
		retryTransactionAssertNoChild(t, fx.db, root.ID)
	})
}

func retryTransactionInstallBatchKeyCreateBarrier(t *testing.T, db *gorm.DB, key string) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	callbackName := fmt.Sprintf("retry-transaction-batch-key-barrier-%d", batchTestSequence.Add(1))
	if err := db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		tableName := tx.Statement.Table
		if tableName == "" && tx.Statement.Schema != nil {
			tableName = tx.Statement.Schema.Table
		}
		if tableName != "batch_commands" {
			return
		}
		batch, ok := tx.Statement.Dest.(*model.BatchCommand)
		if !ok || batch.IdempotencyKey != key {
			return
		}
		once.Do(func() {
			close(reached)
			<-release
		})
	}); err != nil {
		t.Fatalf("register batch key barrier: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	return reached, release
}

func retryTransactionCreateRequest(t *testing.T, router http.Handler, nodeID uint, token, proof, key string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"node_ids":[%d],"command":"printf 'r13 create conflict'"}`, nodeID)
	request := httptest.NewRequest(http.MethodPost, "/batch-commands", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(StepUpHeaderName, proof)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestBatchCommandRetryCrossSourceSameActorKeyBarrierPostgres(t *testing.T) {
	fx := newRetryTransactionFixture(t, "postgres")
	rootA, dispatchesA := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
	rootB, dispatchesB := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
	retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
	token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
	key := "r13-cross-source-same-actor-key"
	reached, release := retryTransactionInstallBatchKeyCreateBarrier(t, fx.db, key)
	responses := make(chan *httptest.ResponseRecorder, 2)
	go func() {
		responses <- retryTransactionRequest(t, fx.router, rootA.ID, token, proof, key, []batchRetrySource{{TaskID: dispatchesA[0].TaskID, RunID: 0}})
	}()
	retryTransactionWaitForGate(t, reached)
	go func() {
		responses <- retryTransactionRequest(t, fx.router, rootB.ID, token, proof, key, []batchRetrySource{{TaskID: dispatchesB[0].TaskID, RunID: 0}})
	}()
	close(release)
	first, second := <-responses, <-responses
	successes, conflicts := 0, 0
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		switch response.Code {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("cross-source same-key status=%d body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "duplicate key") || strings.Contains(response.Body.String(), "UNIQUE") || strings.Contains(response.Body.String(), "constraint") {
			t.Fatalf("cross-source same-key leaked SQL conflict: %s", response.Body.String())
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("cross-source same-key successes=%d conflicts=%d, want one each", successes, conflicts)
	}
	var winnerCount int64
	if err := fx.db.Model(&model.BatchCommand{}).Where("requester_id = ? AND idempotency_key = ?", fx.admin.ID, key).Count(&winnerCount).Error; err != nil {
		t.Fatal(err)
	}
	if winnerCount != 1 {
		t.Fatalf("cross-source same-key durable identities=%d, want one", winnerCount)
	}
}

func TestBatchCommandRetryCreateConflictBarrierPostgres(t *testing.T) {
	for _, first := range []string{"create", "retry"} {
		t.Run(first+"-first", func(t *testing.T) {
			fx := newRetryTransactionFixture(t, "postgres")
			root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
			retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommand, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			fx.router.POST("/batch-commands", middleware.RBAC("tasks:write"), fx.handler.Create)
			retryToken, retryProof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
			createToken := generatePrimaryToken(t, fx.jwt, fx.admin)
			createProof := generateStepUpProofForAction(t, fx.jwt, fx.admin, auth.StepUpActionBatchCommandCreate)
			key := "r13-create-vs-retry-" + first
			reached, release := retryTransactionInstallBatchKeyCreateBarrier(t, fx.db, key)
			responses := make(chan *httptest.ResponseRecorder, 2)
			if first == "create" {
				go func() {
					responses <- retryTransactionCreateRequest(t, fx.router, fx.nodes[0].ID, createToken, createProof, key)
				}()
				retryTransactionWaitForGate(t, reached)
				go func() {
					responses <- retryTransactionRequest(t, fx.router, root.ID, retryToken, retryProof, key, []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}})
				}()
			} else {
				go func() {
					responses <- retryTransactionRequest(t, fx.router, root.ID, retryToken, retryProof, key, []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}})
				}()
				retryTransactionWaitForGate(t, reached)
				go func() {
					responses <- retryTransactionCreateRequest(t, fx.router, fx.nodes[0].ID, createToken, createProof, key)
				}()
			}
			close(release)
			firstResponse, secondResponse := <-responses, <-responses
			successes, conflicts := 0, 0
			for _, response := range []*httptest.ResponseRecorder{firstResponse, secondResponse} {
				switch response.Code {
				case http.StatusOK:
					successes++
				case http.StatusConflict:
					conflicts++
				default:
					t.Fatalf("create-vs-retry %s-first status=%d body=%s", first, response.Code, response.Body.String())
				}
				if strings.Contains(response.Body.String(), "duplicate key") || strings.Contains(response.Body.String(), "UNIQUE") || strings.Contains(response.Body.String(), "constraint") {
					t.Fatalf("create-vs-retry leaked SQL conflict: %s", response.Body.String())
				}
			}
			if successes != 1 || conflicts != 1 {
				t.Fatalf("create-vs-retry %s-first successes=%d conflicts=%d, want one each", first, successes, conflicts)
			}
			var winnerCount int64
			if err := fx.db.Model(&model.BatchCommand{}).Where("requester_id = ? AND idempotency_key = ?", fx.admin.ID, key).Count(&winnerCount).Error; err != nil {
				t.Fatal(err)
			}
			if winnerCount != 1 {
				t.Fatalf("create-vs-retry %s-first durable identities=%d, want one", first, winnerCount)
			}
		})
	}
}
