package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type nodeSummaryRouterRBACFixture struct {
	db          *gorm.DB
	router      *gin.Engine
	tokens      map[string]string
	nodeID      uint
	otherNodeID uint
}

func setupNodeSummaryRouterRBACFixture(t *testing.T) nodeSummaryRouterRBACFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open test SQL database: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.User{}, &model.TokenRevocation{}, &model.AuditLog{},
		&model.Node{}, &model.NodeOwner{}, &model.Alert{}, &model.Task{}, &model.TaskRun{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	node := model.Node{Name: "summary-rbac-node", Host: "127.0.0.1", Port: 22, Username: "root", BackupDir: "backup-summary-rbac"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	otherNode := model.Node{Name: "summary-rbac-other", Host: "127.0.0.2", Port: 22, Username: "root", BackupDir: "backup-summary-other"}
	if err := db.Create(&otherNode).Error; err != nil {
		t.Fatalf("create other node: %v", err)
	}
	if err := db.Create(&model.Alert{NodeID: node.ID, Status: "open", ErrorCode: "XR-SUMMARY-OPEN", TriggeredAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create open alert: %v", err)
	}
	if err := db.Create(&model.Alert{NodeID: node.ID, Status: "resolved", ErrorCode: "XR-SUMMARY-RESOLVED", TriggeredAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create resolved alert: %v", err)
	}
	task := model.Task{Name: "summary-rbac-task", NodeID: node.ID, ExecutorType: "rsync", Status: "running"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := db.Create(&model.TaskRun{TaskID: task.ID, NodeIDSnapshot: node.ID, Status: model.TaskRunStatusRunning}).Error; err != nil {
		t.Fatalf("create running task run: %v", err)
	}

	jwtManager := auth.NewJWTManager("FAKE_NODE_SUMMARY_RBAC_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	jwtManager.SetDB(db)
	tokens := make(map[string]string, 4)
	users := make(map[string]model.User, 4)
	for _, role := range []string{"admin", "operator", "viewer", "guest"} {
		user := model.User{
			Username:     "node-summary-rbac-" + role,
			PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create %s user: %v", role, err)
		}
		users[role] = user
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("generate %s token: %v", role, err)
		}
		tokens[role] = token
	}
	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: users["operator"].ID}).Error; err != nil {
		t.Fatalf("create node owner: %v", err)
	}

	return nodeSummaryRouterRBACFixture{
		db: db, router: NewRouter(Dependencies{DB: db, JWTManager: jwtManager}), tokens: tokens,
		nodeID: node.ID, otherNodeID: otherNode.ID,
	}
}

func performNodeSummaryRouterRequest(t *testing.T, router *gin.Engine, nodeID uint, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/summary", nodeID), nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestNodeSummaryRouteUsesNodesReadAndOwnershipAuthorization(t *testing.T) {
	fixture := setupNodeSummaryRouterRBACFixture(t)

	if response := performNodeSummaryRouterRequest(t, fixture.router, fixture.nodeID, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}
	for _, role := range []string{"admin", "viewer", "operator"} {
		response := performNodeSummaryRouterRequest(t, fixture.router, fixture.nodeID, fixture.tokens[role])
		if response.Code != http.StatusOK {
			t.Fatalf("role=%s status=%d body=%s", role, response.Code, response.Body.String())
		}
		var envelope struct {
			Data struct {
				OpenAlerts   int64 `json:"open_alerts"`
				RunningTasks int64 `json:"running_tasks"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("role=%s decode response: %v", role, err)
		}
		if envelope.Data.OpenAlerts != 1 || envelope.Data.RunningTasks != 1 {
			t.Fatalf("role=%s summary=%+v, want open_alerts=1 running_tasks=1", role, envelope.Data)
		}
	}
	if response := performNodeSummaryRouterRequest(t, fixture.router, fixture.otherNodeID, fixture.tokens["operator"]); response.Code != http.StatusForbidden {
		t.Fatalf("operator unowned node status=%d body=%s", response.Code, response.Body.String())
	}
	if response := performNodeSummaryRouterRequest(t, fixture.router, fixture.nodeID, fixture.tokens["guest"]); response.Code != http.StatusForbidden {
		t.Fatalf("guest status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNodeSummaryRouteReplacesRetiredResourceRoutes(t *testing.T) {
	routes := NewRouter(Dependencies{}).Routes()
	if !hasRoute(routes, http.MethodGet, "/api/v1/nodes/:id/summary") {
		t.Fatal("node summary route is missing")
	}
	for _, retired := range []string{
		"/api/v1/nodes/:id/metrics",
		"/api/v1/nodes/:id/status",
		"/api/v1/nodes/:id/metric-series",
		"/api/v1/nodes/:id/disk-forecast",
	} {
		if hasRoute(routes, http.MethodGet, retired) {
			t.Fatalf("retired resource route remains registered: %s", retired)
		}
	}
}
