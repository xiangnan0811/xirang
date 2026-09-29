package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/taskstats"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openTaskStatisticsHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRun{}, &model.TaskTrafficSample{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedTaskStatisticsHandlerTask(t *testing.T, db *gorm.DB, id, nodeID uint, name string) {
	t.Helper()
	if err := db.Create(&model.Task{
		ID: id, Name: name, NodeID: nodeID, ExecutorType: "local", Status: "success",
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

func seedTaskStatisticsHandlerRun(t *testing.T, db *gorm.DB, taskID uint, status string, finishedAt time.Time) {
	t.Helper()
	if err := db.Create(&model.TaskRun{
		TaskID: taskID, Status: status, FinishedAt: &finishedAt, DurationMs: 100,
	}).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

func newTaskStatisticsHandlerRouter(t *testing.T, db *gorm.DB, userID uint, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewTaskStatisticsHandler(db)
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, userID)
		c.Set(middleware.CtxRole, role)
		c.Next()
	})
	router.POST("/api/v1/tasks/statistics/query", middleware.RBAC("tasks:read"), handler.Query)
	return router
}

func performTaskStatisticsRequest(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/statistics/query", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

type taskStatisticsResponseEnvelope struct {
	Code int                     `json:"code"`
	Data taskstats.QueryResponse `json:"data"`
}

func TestTaskStatisticsHandlerOperatorEmptyFilterOnlyOwnedTasks(t *testing.T) {
	db := openTaskStatisticsHandlerDB(t)
	seedTaskStatisticsHandlerTask(t, db, 1, 1, "owned")
	seedTaskStatisticsHandlerTask(t, db, 2, 2, "other")
	if err := db.Create(&model.NodeOwner{NodeID: 1, UserID: 7}).Error; err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedTaskStatisticsHandlerRun(t, db, 1, "success", base.Add(10*time.Second))
	seedTaskStatisticsHandlerRun(t, db, 2, "failed", base.Add(10*time.Second))

	router := newTaskStatisticsHandlerRouter(t, db, 7, "operator")
	body := fmt.Sprintf(`{"metric":"task.success_rate","filters":{},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	response := performTaskStatisticsRequest(t, router, body)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope taskStatisticsResponseEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Code != http.StatusOK || len(envelope.Data.Series) != 1 || len(envelope.Data.Series[0].Points) != 1 {
		t.Fatalf("unexpected response: %+v", envelope)
	}
	if got := envelope.Data.Series[0].Points[0].Value; got != 1 {
		t.Fatalf("owned task rate: got %v want 1", got)
	}
}

func TestTaskStatisticsHandlerOperatorExplicitTaskOwnershipIsAllOrNothing(t *testing.T) {
	db := openTaskStatisticsHandlerDB(t)
	seedTaskStatisticsHandlerTask(t, db, 1, 1, "owned")
	seedTaskStatisticsHandlerTask(t, db, 2, 2, "other")
	if err := db.Create(&model.NodeOwner{NodeID: 1, UserID: 7}).Error; err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	body := fmt.Sprintf(`{"metric":"task.success_rate","filters":{"task_ids":[1,2]},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	response := performTaskStatisticsRequest(t, newTaskStatisticsHandlerRouter(t, db, 7, "operator"), body)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskStatisticsHandlerMissingExplicitTaskDoesNotFallbackToAll(t *testing.T) {
	db := openTaskStatisticsHandlerDB(t)
	seedTaskStatisticsHandlerTask(t, db, 1, 1, "owned")
	if err := db.Create(&model.NodeOwner{NodeID: 1, UserID: 7}).Error; err != nil {
		t.Fatalf("seed ownership: %v", err)
	}
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	body := fmt.Sprintf(`{"metric":"task.success_rate","filters":{"task_ids":[999]},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	response := performTaskStatisticsRequest(t, newTaskStatisticsHandlerRouter(t, db, 7, "operator"), body)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskStatisticsHandlerStrictTaskOnlySchema(t *testing.T) {
	db := openTaskStatisticsHandlerDB(t)
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	router := newTaskStatisticsHandlerRouter(t, db, 1, "admin")
	cases := []string{
		fmt.Sprintf(`{"metric":"task.success_rate","filters":{"node_ids":[1]},"aggregation":"avg","start":"%s","end":"%s"}`, base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339)),
		fmt.Sprintf(`{"metric":"task.success_rate","filters":{},"aggregation":"avg","start":"%s","end":"%s","ownership_scoped":true}`, base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339)),
		fmt.Sprintf(`{"metric":"task.duration_p95","filters":{},"aggregation":"p95","start":"%s","end":"%s"}`, base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339)),
	}
	for _, body := range cases {
		response := performTaskStatisticsRequest(t, router, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s request=%s", response.Code, response.Body.String(), body)
		}
	}
}

func TestTaskStatisticsHandlerRejectsTrailingJSON(t *testing.T) {
	db := openTaskStatisticsHandlerDB(t)
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	body := fmt.Sprintf(`{"metric":"task.success_rate","filters":{},"aggregation":"avg","start":"%s","end":"%s"}{}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	response := performTaskStatisticsRequest(t, newTaskStatisticsHandlerRouter(t, db, 1, "admin"), body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTaskStatisticsHandlerRejectsTimeWindowOverThirtyDays(t *testing.T) {
	db := openTaskStatisticsHandlerDB(t)
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	body := fmt.Sprintf(`{"metric":"task.success_rate","filters":{},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(taskstats.MaxQueryDuration+time.Nanosecond).Format(time.RFC3339Nano))
	response := performTaskStatisticsRequest(t, newTaskStatisticsHandlerRouter(t, db, 1, "admin"), body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
