package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/config"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

func TestBatchRetryNodeLockWaitRechecksPostgres(t *testing.T) {
	for _, mode := range []string{"archived", "expired_grant"} {
		t.Run(mode, func(t *testing.T) {
			fx := newRetryTransactionFixture(t, "postgres")
			root, dispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
			grant := retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)

			holder := fx.db.Begin()
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			defer holder.Rollback()
			var node model.Node
			if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&node, fx.nodes[0].ID).Error; err != nil {
				t.Fatal(err)
			}
			var holderPID int
			if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
				t.Fatal(err)
			}
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				responses <- retryTransactionRequest(t, fx.router, root.ID, token, proof, "node-wait-"+mode, []batchRetrySource{{TaskID: dispatches[0].TaskID, RunID: 0}})
			}()
			retryTransactionWaitForPostgresBlock(t, fx.db, holderPID, "nodes", mode)
			if mode == "archived" {
				if err := holder.Model(&model.Node{}).Where("id = ?", node.ID).Update("archived", true).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := holder.Model(&model.CredentialAccessGrant{}).Where("id = ?", grant.ID).Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := holder.Commit().Error; err != nil {
				t.Fatal(err)
			}
			response := retryTransactionAwaitResponse(t, responses, mode)
			want := http.StatusConflict
			if mode == "expired_grant" {
				want = http.StatusForbidden
			}
			if response.Code != want {
				t.Fatalf("%s status=%d body=%s, want %d", mode, response.Code, response.Body.String(), want)
			}
			retryTransactionAssertNoChild(t, fx.db, root.ID)
			var successAudits []model.CredentialAuditEvent
			if err := fx.db.Where("action = ? AND purpose = ? AND outcome = ?", string(auth.StepUpActionBatchCommandRetry), sshutil.PurposeBatchCommand, credentialaudit.OutcomeSuccess).Find(&successAudits).Error; err != nil {
				t.Fatal(err)
			}
			for _, event := range successAudits {
				var metadata map[string]any
				if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
					t.Fatal(err)
				}
				if metadata["stage"] == "created" {
					t.Fatalf("%s wrote a retry creation audit", mode)
				}
			}
		})
	}
}

func TestBatchCreateRetryRequesterKeyNodeLockOrderPostgres(t *testing.T) {
	for _, first := range []string{"create", "retry"} {
		t.Run(first, func(t *testing.T) {
			fx := newRetryTransactionFixture(t, "postgres")
			root, sourceDispatches := retryTransactionSeedDispatchFailedRoot(t, fx, fx.admin.ID, fx.nodes[:1], []uint{fx.nodes[0].ID})
			retryTransactionSeedGrant(t, fx.db, fx.admin, fx.nodes[0].ID, CredentialGrantActionBatchCommandRetry, CredentialGrantStatusActive, time.Now().UTC().Add(time.Hour))
			token, proof := retryTransactionIssueAuth(t, fx, fx.admin, auth.StepUpActionBatchCommandRetry)
			key := fmt.Sprintf("create-retry-lock-order-%d", batchTestSequence.Add(1))
			marker := "lock-order-" + first
			gate := newRetryTransactionLockGate(t, fx.db, "nodes", marker, false)
			type createResult struct {
				batch   model.BatchCommand
				created bool
				err     error
			}
			creates := make(chan createResult, 1)
			retries := make(chan *httptest.ResponseRecorder, 1)
			startCreate := func(mark string) {
				go func() {
					ctx := context.Background()
					if mark != "" {
						ctx = retryTransactionMarkedContext(mark)
					}
					batch, _, created, err := fx.handler.createBatchTasks(ctx, fx.admin.ID, key, batchCommandRequest{NodeIDs: []uint{fx.nodes[0].ID}, Command: "true"}, fx.nodes[:1])
					creates <- createResult{batch: batch, created: created, err: err}
				}()
			}
			startRetry := func(mark string) {
				go func() {
					retries <- retryTransactionMarkedRetryRequest(t, fx.router, root.ID, token, proof, key, []batchRetrySource{{TaskID: sourceDispatches[0].TaskID}}, mark)
				}()
			}
			if first == "create" {
				startCreate(marker)
			} else {
				startRetry(marker)
			}
			retryTransactionWaitForMarkedAttempt(t, gate.reached, first+" node lock")
			holderPID := retryTransactionAwaitHolderPID(t, gate.holderPID, first+" node lock")
			if first == "create" {
				startRetry("")
			} else {
				startCreate("")
			}
			retryTransactionWaitForPostgresBlock(t, fx.db, holderPID, "nodes", "competing "+first)
			gate.release()
			var create createResult
			select {
			case create = <-creates:
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for batch create")
			}
			retry := retryTransactionAwaitResponse(t, retries, "batch retry")
			var winner model.BatchCommand
			if err := fx.db.Where("requester_id = ? AND idempotency_key = ?", fx.admin.ID, key).First(&winner).Error; err != nil {
				t.Fatal(err)
			}
			if first == "create" {
				if create.err != nil || !create.created || winner.ID != create.batch.ID || retry.Code != http.StatusConflict {
					t.Fatalf("create=%+v winner=%q retry status=%d body=%s", create, winner.ID, retry.Code, retry.Body.String())
				}
			} else if !errors.Is(create.err, errBatchIdempotencyConflict) || create.created || retry.Code != http.StatusOK || winner.SourceBatchID == nil || *winner.SourceBatchID != root.ID {
				t.Fatalf("create=%+v winner=%+v retry status=%d body=%s", create, winner, retry.Code, retry.Body.String())
			}
		})
	}
}

func TestPolicyUpdateLocksTaskInventoryBeforeNodesPostgres(t *testing.T) {
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")
	fx := newRetryTransactionFixture(t, "postgres")
	if err := fx.db.AutoMigrate(&model.Policy{}, &model.PolicyNode{}); err != nil {
		t.Fatal(err)
	}
	p := model.Policy{Name: "lock-order-policy", SourcePath: "/data/source", TargetPath: config.BackupRoot, CronSpec: "0 * * * *", Enabled: true}
	if err := fx.db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	// An unrelated task is also locked by target-ownership validation.
	task := model.Task{Name: "lock-order-unrelated", NodeID: fx.nodes[0].ID, ExecutorType: "command", Command: "true", Source: "manual"}
	if err := fx.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	holder := fx.db.Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	var lockedTask model.Task
	if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	var node model.Node
	if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&node, fx.nodes[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	router.PUT("/policies/:id", NewPolicyHandler(fx.db, nil).Update)
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		body := fmt.Sprintf(`{"name":"lock-order-policy","source_path":"/data/source","cron_spec":"0 * * * *","node_ids":[%d]}`, node.ID)
		req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/policies/%d", p.ID), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		responses <- response
	}()
	retryTransactionWaitForPostgresBlock(t, fx.db, holderPID, "tasks", "policy update task inventory")
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}
	response := retryTransactionAwaitResponse(t, responses, "policy update")
	if response.Code != http.StatusOK {
		t.Fatalf("policy update status=%d body=%s", response.Code, response.Body.String())
	}
}
