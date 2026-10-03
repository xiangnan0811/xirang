package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type taskFailureSummaryTestFixture struct {
	db            *gorm.DB
	now           time.Time
	handler       *TaskFailureSummaryHandler
	admin         model.User
	viewer        model.User
	operatorA     model.User
	operatorB     model.User
	emptyOperator model.User
	nodeA         model.Node
	nodeB         model.Node
}

type taskFailureSummaryTestData struct {
	Code int `json:"code"`
	Data struct {
		FailedTasks int64 `json:"failed_tasks"`
		WindowHours int   `json:"window_hours"`
	} `json:"data"`
}

func newTaskFailureSummaryTestFixture(t *testing.T, engine string) taskFailureSummaryTestFixture {
	t.Helper()
	db := openR306DB(t, engine, &model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}, &model.TaskRun{})
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)

	users := []model.User{
		{Username: "failure-summary-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"},
		{Username: "failure-summary-viewer", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "viewer"},
		{Username: "failure-summary-operator-a", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator"},
		{Username: "failure-summary-operator-b", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator"},
		{Username: "failure-summary-operator-empty", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator"},
	}
	for index := range users {
		if err := db.Create(&users[index]).Error; err != nil {
			t.Fatalf("create failure summary user %d: %v", index, err)
		}
	}

	nodes := []model.Node{
		{Name: "failure-summary-node-a", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "failure-summary-node-a"},
		{Name: "failure-summary-node-b", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "failure-summary-node-b"},
	}
	for index := range nodes {
		if err := db.Create(&nodes[index]).Error; err != nil {
			t.Fatalf("create failure summary node %d: %v", index, err)
		}
	}
	if err := db.Create(&model.NodeOwner{NodeID: nodes[0].ID, UserID: users[2].ID}).Error; err != nil {
		t.Fatalf("create node A ownership: %v", err)
	}

	return taskFailureSummaryTestFixture{
		db:            db,
		now:           now,
		handler:       NewTaskFailureSummaryHandler(db, func() time.Time { return now }),
		admin:         users[0],
		viewer:        users[1],
		operatorA:     users[2],
		operatorB:     users[3],
		emptyOperator: users[4],
		nodeA:         nodes[0],
		nodeB:         nodes[1],
	}
}

func createTaskFailureSummaryTask(t *testing.T, db *gorm.DB, name string, nodeID uint, status string) model.Task {
	t.Helper()
	task := model.Task{
		Name:         name,
		NodeID:       nodeID,
		ExecutorType: "local",
		Status:       status,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create failure summary task %s: %v", name, err)
	}
	return task
}

func createTaskFailureSummaryRun(
	t *testing.T,
	db *gorm.DB,
	taskID, nodeID uint,
	status string,
	createdAt time.Time,
	finishedAt *time.Time,
	skipHooks bool,
) model.TaskRun {
	t.Helper()
	run := model.TaskRun{
		TaskID:               taskID,
		NodeIDSnapshot:       nodeID,
		ExecutorTypeSnapshot: "local",
		TriggerType:          "manual",
		Status:               status,
		CreatedAt:            createdAt,
		UpdatedAt:            createdAt,
		FinishedAt:           finishedAt,
	}
	query := db
	if skipHooks {
		query = db.Session(&gorm.Session{SkipHooks: true})
	}
	if err := query.Create(&run).Error; err != nil {
		t.Fatalf("create failure summary run task=%d status=%s: %v", taskID, status, err)
	}
	return run
}

func performTaskFailureSummaryHandlerRequest(
	t *testing.T,
	handler *TaskFailureSummaryHandler,
	role string,
	userID uint,
	path string,
	ctx context.Context,
) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.GET("/summary", func(c *gin.Context) {
		if role != "" {
			c.Set(middleware.CtxRole, role)
		}
		if userID != 0 {
			c.Set(middleware.CtxUserID, userID)
		}
		handler.Get(c)
	})
	if ctx == nil {
		ctx = context.Background()
	}
	request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeTaskFailureSummaryResponse(t *testing.T, response *httptest.ResponseRecorder) taskFailureSummaryTestData {
	t.Helper()
	var payload taskFailureSummaryTestData
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode failure summary response: %v; body=%s", err, response.Body.String())
	}
	return payload
}

func assertTaskFailureSummary(t *testing.T, response *httptest.ResponseRecorder, want int64) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("failure summary status=%d body=%s", response.Code, response.Body.String())
	}
	payload := decodeTaskFailureSummaryResponse(t, response)
	if payload.Code != http.StatusOK || payload.Data.FailedTasks != want || payload.Data.WindowHours != 24 {
		t.Fatalf("failure summary payload=%+v want failed_tasks=%d window_hours=24", payload, want)
	}
}

func TestTaskFailureSummarySQLite(t *testing.T) {
	testTaskFailureSummaryBehavior(t, "sqlite")
}

func TestTaskFailureSummaryPostgres(t *testing.T) {
	testTaskFailureSummaryBehavior(t, "postgres")
}

func testTaskFailureSummaryBehavior(t *testing.T, engine string) {
	t.Run("retained status and time matrix", func(t *testing.T) {
		fixture := newTaskFailureSummaryTestFixture(t, engine)
		start := fixture.now.Add(-24 * time.Hour)
		end := fixture.now

		oldCurrentFailed := createTaskFailureSummaryTask(t, fixture.db, "old-current-failed", fixture.nodeA.ID, model.TaskRunStatusFailed)
		createTaskFailureSummaryRun(t, fixture.db, oldCurrentFailed.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-25*time.Hour), new(fixture.now.Add(-25*time.Hour)), false)

		recentThenSuccess := createTaskFailureSummaryTask(t, fixture.db, "recent-then-success", fixture.nodeA.ID, model.TaskRunStatusSuccess)
		createTaskFailureSummaryRun(t, fixture.db, recentThenSuccess.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-2*time.Hour), new(fixture.now.Add(-2*time.Hour)), false)
		createTaskFailureSummaryRun(t, fixture.db, recentThenSuccess.ID, fixture.nodeA.ID, model.TaskRunStatusSuccess, fixture.now.Add(-time.Hour), new(fixture.now.Add(-time.Hour)), false)

		duplicateFailures := createTaskFailureSummaryTask(t, fixture.db, "duplicate-failures", fixture.nodeA.ID, model.TaskRunStatusFailed)
		createTaskFailureSummaryRun(t, fixture.db, duplicateFailures.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-3*time.Hour), new(fixture.now.Add(-3*time.Hour)), false)
		createTaskFailureSummaryRun(t, fixture.db, duplicateFailures.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-30*time.Minute), new(fixture.now.Add(-30*time.Minute)), false)

		differentTask := createTaskFailureSummaryTask(t, fixture.db, "different-task", fixture.nodeA.ID, model.TaskRunStatusFailed)
		createTaskFailureSummaryRun(t, fixture.db, differentTask.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-4*time.Hour), new(fixture.now.Add(-4*time.Hour)), false)

		boundaryStart := createTaskFailureSummaryTask(t, fixture.db, "boundary-start", fixture.nodeA.ID, model.TaskRunStatusSuccess)
		createTaskFailureSummaryRun(t, fixture.db, boundaryStart.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, start, new(start), false)

		boundaryEnd := createTaskFailureSummaryTask(t, fixture.db, "boundary-end", fixture.nodeA.ID, model.TaskRunStatusSuccess)
		createTaskFailureSummaryRun(t, fixture.db, boundaryEnd.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, end, new(end), false)

		outsideWindow := createTaskFailureSummaryTask(t, fixture.db, "outside-window", fixture.nodeA.ID, model.TaskRunStatusFailed)
		createTaskFailureSummaryRun(t, fixture.db, outsideWindow.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-25*time.Hour), new(fixture.now.Add(-25*time.Hour)), false)

		futureRun := createTaskFailureSummaryTask(t, fixture.db, "future-run", fixture.nodeA.ID, model.TaskRunStatusSuccess)
		createTaskFailureSummaryRun(t, fixture.db, futureRun.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(time.Hour), new(fixture.now.Add(time.Hour)), false)

		nullFinished := createTaskFailureSummaryTask(t, fixture.db, "null-finished", fixture.nodeA.ID, model.TaskRunStatusSuccess)
		createTaskFailureSummaryRun(t, fixture.db, nullFinished.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-time.Hour), nil, false)

		for _, status := range []string{model.TaskRunStatusWarning, model.TaskRunStatusCanceled, model.TaskRunStatusSkipped, model.TaskRunStatusSuccess} {
			task := createTaskFailureSummaryTask(t, fixture.db, fmt.Sprintf("non-failed-%s", status), fixture.nodeA.ID, status)
			finished := fixture.now.Add(-time.Hour)
			createTaskFailureSummaryRun(t, fixture.db, task.ID, fixture.nodeA.ID, status, finished, new(finished), false)
		}

		createdLongAgo := createTaskFailureSummaryTask(t, fixture.db, "created-long-ago", fixture.nodeA.ID, model.TaskRunStatusSuccess)
		createTaskFailureSummaryRun(t, fixture.db, createdLongAgo.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-365*24*time.Hour), new(fixture.now.Add(-2*time.Hour)), false)

		legacyTask := createTaskFailureSummaryTask(t, fixture.db, "legacy-unknown", fixture.nodeA.ID, model.TaskRunStatusFailed)
		createTaskFailureSummaryRun(t, fixture.db, legacyTask.ID, model.TaskRunNodeIDLegacyUnknown, model.TaskRunStatusFailed, fixture.now.Add(-time.Hour), new(fixture.now.Add(-time.Hour)), true)

		adminResponse := performTaskFailureSummaryHandlerRequest(t, fixture.handler, "admin", fixture.admin.ID, "/summary?window_hours=1", nil)
		assertTaskFailureSummary(t, adminResponse, 6)
		if got := adminResponse.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("Cache-Control=%q want private, no-store", got)
		}
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "viewer", fixture.viewer.ID, "/summary", nil), 6)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "operator", fixture.operatorA.ID, "/summary", nil), 5)

		if err := fixture.db.Where("task_id = ?", createdLongAgo.ID).Delete(&model.TaskRun{}).Error; err != nil {
			t.Fatalf("delete final created-long-ago failure: %v", err)
		}
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "admin", fixture.admin.ID, "/summary", nil), 5)

		if err := fixture.db.Where("task_id = ?", duplicateFailures.ID).Delete(&model.TaskRun{}).Error; err != nil {
			t.Fatalf("delete duplicate failure history: %v", err)
		}
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "admin", fixture.admin.ID, "/summary", nil), 4)
	})

	t.Run("snapshot ownership and legacy unknown", func(t *testing.T) {
		fixture := newTaskFailureSummaryTestFixture(t, engine)
		finished := fixture.now.Add(-time.Hour)
		migrated := createTaskFailureSummaryTask(t, fixture.db, "migrated-task", fixture.nodeA.ID, model.TaskRunStatusSuccess)
		createTaskFailureSummaryRun(t, fixture.db, migrated.ID, fixture.nodeA.ID, model.TaskRunStatusFailed, fixture.now.Add(-2*time.Hour), new(fixture.now.Add(-2*time.Hour)), false)
		if err := fixture.db.Model(&model.Task{}).Where("id = ?", migrated.ID).Update("node_id", fixture.nodeB.ID).Error; err != nil {
			t.Fatalf("move migrated task to node B: %v", err)
		}
		if err := fixture.db.Create(&model.NodeOwner{NodeID: fixture.nodeB.ID, UserID: fixture.operatorB.ID}).Error; err != nil {
			t.Fatalf("create node B ownership: %v", err)
		}

		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "admin", fixture.admin.ID, "/summary", nil), 1)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "viewer", fixture.viewer.ID, "/summary", nil), 1)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "operator", fixture.operatorA.ID, "/summary", nil), 1)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "operator", fixture.operatorB.ID, "/summary", nil), 0)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "operator", fixture.emptyOperator.ID, "/summary", nil), 0)

		createTaskFailureSummaryRun(t, fixture.db, migrated.ID, fixture.nodeB.ID, model.TaskRunStatusFailed, finished, new(finished), false)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "admin", fixture.admin.ID, "/summary", nil), 1)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "operator", fixture.operatorA.ID, "/summary", nil), 1)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "operator", fixture.operatorB.ID, "/summary", nil), 1)

		legacyTask := createTaskFailureSummaryTask(t, fixture.db, "legacy-only-task", fixture.nodeB.ID, model.TaskRunStatusSuccess)
		createTaskFailureSummaryRun(t, fixture.db, legacyTask.ID, model.TaskRunNodeIDLegacyUnknown, model.TaskRunStatusFailed, fixture.now.Add(-time.Hour), new(fixture.now.Add(-time.Hour)), true)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "admin", fixture.admin.ID, "/summary", nil), 2)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "viewer", fixture.viewer.ID, "/summary", nil), 2)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "operator", fixture.operatorA.ID, "/summary", nil), 1)
		assertTaskFailureSummary(t, performTaskFailureSummaryHandlerRequest(t, fixture.handler, "operator", fixture.operatorB.ID, "/summary", nil), 1)
	})

	t.Run("safe errors and cancellation", func(t *testing.T) {
		noRoleFixture := newTaskFailureSummaryTestFixture(t, engine)
		noRoleResponse := performTaskFailureSummaryHandlerRequest(t, noRoleFixture.handler, "", 0, "/summary", nil)
		if noRoleResponse.Code != http.StatusInternalServerError {
			t.Fatalf("missing role status=%d body=%s", noRoleResponse.Code, noRoleResponse.Body.String())
		}
		if got := noRoleResponse.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("missing-role Cache-Control=%q want private, no-store", got)
		}

		ownershipErrorFixture := newTaskFailureSummaryTestFixture(t, engine)
		if err := ownershipErrorFixture.db.Migrator().DropTable(&model.NodeOwner{}); err != nil {
			t.Fatalf("drop node ownership table: %v", err)
		}
		ownershipErrorResponse := performTaskFailureSummaryHandlerRequest(t, ownershipErrorFixture.handler, "operator", ownershipErrorFixture.operatorA.ID, "/summary", nil)
		if ownershipErrorResponse.Code != http.StatusInternalServerError {
			t.Fatalf("ownership query error status=%d body=%s", ownershipErrorResponse.Code, ownershipErrorResponse.Body.String())
		}

		statisticsErrorFixture := newTaskFailureSummaryTestFixture(t, engine)
		sqlDB, err := statisticsErrorFixture.db.DB()
		if err != nil {
			t.Fatalf("get statistics error sql db: %v", err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("close statistics error sql db: %v", err)
		}
		statisticsErrorResponse := performTaskFailureSummaryHandlerRequest(t, statisticsErrorFixture.handler, "admin", statisticsErrorFixture.admin.ID, "/summary", nil)
		if statisticsErrorResponse.Code != http.StatusInternalServerError {
			t.Fatalf("statistics query error status=%d body=%s", statisticsErrorResponse.Code, statisticsErrorResponse.Body.String())
		}

		canceledFixture := newTaskFailureSummaryTestFixture(t, engine)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		canceledResponse := performTaskFailureSummaryHandlerRequest(t, canceledFixture.handler, "admin", canceledFixture.admin.ID, "/summary", ctx)
		if canceledResponse.Code != 499 {
			t.Fatalf("canceled request status=%d body=%s", canceledResponse.Code, canceledResponse.Body.String())
		}
	})
}
