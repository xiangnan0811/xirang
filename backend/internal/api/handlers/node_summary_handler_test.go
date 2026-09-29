package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestNodeSummaryCountsOpenAlertsAndAuthoritativeRunningTasks(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(
		&model.Node{},
		&model.SSHKey{},
		&model.Alert{},
		&model.Task{},
		&model.TaskRun{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	node := model.Node{Name: "summary-node", Host: "127.0.0.1", Port: 22, Username: "root", BackupDir: "backup-summary"}
	otherNode := model.Node{Name: "other-summary-node", Host: "127.0.0.2", Port: 22, Username: "root", BackupDir: "backup-other"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := db.Create(&otherNode).Error; err != nil {
		t.Fatalf("create other node: %v", err)
	}

	now := time.Now().UTC()
	alerts := []model.Alert{
		{NodeID: node.ID, Status: "open", ErrorCode: "XR-OPEN", TriggeredAt: now},
		{NodeID: node.ID, Status: "acked", ErrorCode: "XR-ACKED", TriggeredAt: now},
		{NodeID: node.ID, Status: "resolved", ErrorCode: "XR-RESOLVED", TriggeredAt: now},
		{NodeID: otherNode.ID, Status: "open", ErrorCode: "XR-OTHER", TriggeredAt: now},
	}
	if err := db.Create(&alerts).Error; err != nil {
		t.Fatalf("create alerts: %v", err)
	}

	task := model.Task{Name: "summary-task", NodeID: node.ID, ExecutorType: "rsync", Status: "running"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	runs := []model.TaskRun{
		{TaskID: task.ID, NodeIDSnapshot: node.ID, Status: model.TaskRunStatusRunning},
		{TaskID: task.ID, NodeIDSnapshot: model.TaskRunNodeIDLegacyUnknown, Status: model.TaskRunStatusRunning},
		{TaskID: task.ID, NodeIDSnapshot: otherNode.ID, Status: model.TaskRunStatusRunning},
		{TaskID: task.ID, NodeIDSnapshot: node.ID, Status: model.TaskRunStatusRetrying},
	}
	// Historical runs retain their original node identity after a task moves.
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&runs).Error; err != nil {
		t.Fatalf("create task runs: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/nodes/:id/summary", NewNodeSummaryHandler(db).Get)
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/summary", node.ID), nil)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Code int                        `json:"code"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Code != http.StatusOK {
		t.Fatalf("envelope code=%d", envelope.Code)
	}
	if len(envelope.Data) != 2 {
		t.Fatalf("summary must contain only two counters, got %v", envelope.Data)
	}
	var openAlerts, runningTasks int64
	if err := json.Unmarshal(envelope.Data["open_alerts"], &openAlerts); err != nil {
		t.Fatalf("decode open_alerts: %v", err)
	}
	if err := json.Unmarshal(envelope.Data["running_tasks"], &runningTasks); err != nil {
		t.Fatalf("decode running_tasks: %v", err)
	}
	if openAlerts != 1 {
		t.Fatalf("open_alerts=%d, want 1", openAlerts)
	}
	if runningTasks != 1 {
		t.Fatalf("running_tasks=%d, want one authoritative run", runningTasks)
	}
}

func TestNodeSummaryRejectsInvalidNodeID(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/nodes/:id/summary", NewNodeSummaryHandler(db).Get)

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/nodes/not-a-number/summary", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
