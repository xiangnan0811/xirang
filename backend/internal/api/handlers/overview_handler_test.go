package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openOverviewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func migrateOverviewTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
}

func overviewGet(t *testing.T, db *gorm.DB) *httptest.ResponseRecorder {
	return overviewGetAs(t, db, "admin", 1)
}

func overviewGetAs(t *testing.T, db *gorm.DB, role string, userID uint) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
	r.GET("/overview", NewOverviewHandler(db).Get)
	req := httptest.NewRequest(http.MethodGet, "/overview", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	return resp
}

type overviewBody struct {
	Data struct {
		ActivePolicies int `json:"activePolicies"`
	} `json:"data"`
}

func parseOverview(t *testing.T, resp *httptest.ResponseRecorder) overviewBody {
	t.Helper()
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	var body overviewBody
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	return body
}

func createOverviewPolicy(t *testing.T, db *gorm.DB, name string, enabled bool) model.Policy {
	t.Helper()
	policy := model.Policy{
		Name:       name,
		SourcePath: "/src/" + name,
		TargetPath: "/dst/" + name,
		CronSpec:   "0 * * * *",
		Enabled:    true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create policy %q: %v", name, err)
	}
	if !enabled {
		if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).Update("enabled", false).Error; err != nil {
			t.Fatalf("disable policy %q: %v", name, err)
		}
		policy.Enabled = false
	}
	return policy
}

func createOverviewNode(t *testing.T, db *gorm.DB, name string) model.Node {
	t.Helper()
	node := model.Node{
		Name:      name,
		Host:      name + ".example.test",
		Username:  "backup",
		AuthType:  "key",
		BackupDir: "/backup/" + name,
		Status:    "online",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node %q: %v", name, err)
	}
	return node
}

func TestOverviewReturnsZeroActivePolicies(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	body := parseOverview(t, overviewGet(t, db))
	if body.Data.ActivePolicies != 0 {
		t.Fatalf("empty overview should return zero active policies: %+v", body.Data)
	}
}

func TestOverviewResponseContainsOnlyActivePolicies(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	resp := overviewGet(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("overview should expose exactly one retained field, data=%v", envelope.Data)
	}
	if _, ok := envelope.Data["activePolicies"]; !ok {
		t.Fatalf("overview response missing activePolicies, data=%v", envelope.Data)
	}
}

func TestOverviewCountsEnabledPoliciesForAdminAndViewer(t *testing.T) {
	for _, role := range []string{"admin", "viewer"} {
		t.Run(role, func(t *testing.T) {
			db := openOverviewTestDB(t)
			migrateOverviewTables(t, db)
			createOverviewPolicy(t, db, "enabled-attached", true)
			createOverviewPolicy(t, db, "enabled-unattached", true)
			createOverviewPolicy(t, db, "disabled", false)

			body := parseOverview(t, overviewGetAs(t, db, role, 1))
			if body.Data.ActivePolicies != 2 {
				t.Fatalf("%s overview should count every enabled policy, got activePolicies=%d", role, body.Data.ActivePolicies)
			}
		})
	}
}

func TestOverviewOperatorCountsDistinctEnabledPoliciesOnOwnedNodes(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	ownedNodeA := createOverviewNode(t, db, "owned-a")
	ownedNodeB := createOverviewNode(t, db, "owned-b")
	otherNode := createOverviewNode(t, db, "other")
	op := model.User{Username: "overview-op", Role: "operator", PasswordHash: "x"}
	if err := db.Create(&op).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}
	for _, nodeID := range []uint{ownedNodeA.ID, ownedNodeB.ID} {
		if err := db.Create(&model.NodeOwner{NodeID: nodeID, UserID: op.ID}).Error; err != nil {
			t.Fatalf("create node owner: %v", err)
		}
	}

	ownedPolicy := createOverviewPolicy(t, db, "owned-enabled", true)
	disabledPolicy := createOverviewPolicy(t, db, "owned-disabled", false)
	otherPolicy := createOverviewPolicy(t, db, "other-enabled", true)
	createOverviewPolicy(t, db, "unattached-enabled", true)
	for _, nodeID := range []uint{ownedNodeA.ID, ownedNodeB.ID} {
		if err := db.Create(&model.PolicyNode{PolicyID: ownedPolicy.ID, NodeID: nodeID}).Error; err != nil {
			t.Fatalf("attach owned policy: %v", err)
		}
	}
	if err := db.Create(&model.PolicyNode{PolicyID: disabledPolicy.ID, NodeID: ownedNodeA.ID}).Error; err != nil {
		t.Fatalf("attach disabled policy: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: otherPolicy.ID, NodeID: otherNode.ID}).Error; err != nil {
		t.Fatalf("attach other policy: %v", err)
	}

	body := parseOverview(t, overviewGetAs(t, db, "operator", op.ID))
	if body.Data.ActivePolicies != 1 {
		t.Fatalf("operator overview should count distinct enabled policies on owned nodes, got activePolicies=%d", body.Data.ActivePolicies)
	}
}

func TestOverviewOperatorWithoutOwnedNodesReturnsZeroActivePolicies(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	node := createOverviewNode(t, db, "unowned")
	policy := createOverviewPolicy(t, db, "unowned-enabled", true)
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("attach unowned policy: %v", err)
	}
	op := model.User{Username: "overview-empty-op", Role: "operator", PasswordHash: "x"}
	if err := db.Create(&op).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}

	body := parseOverview(t, overviewGetAs(t, db, "operator", op.ID))
	if body.Data.ActivePolicies != 0 {
		t.Fatalf("operator without owned nodes must receive zero active policies, got %d", body.Data.ActivePolicies)
	}
}

func TestOverviewReturnsInternalErrorWhenPolicyCountFails(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open sql database: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close sql database: %v", err)
	}

	resp := overviewGet(t, db)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("policy count failure should return 500, got %d", resp.Code)
	}
	if cc := resp.Header().Get("Cache-Control"); !strings.Contains(cc, "private") || !strings.Contains(cc, "no-store") {
		t.Fatalf("failed overview must remain private and non-cacheable, Cache-Control=%q", cc)
	}
}

func TestOverviewCacheControlIsPrivateNoStore(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	resp := overviewGet(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	cc := resp.Header().Get("Cache-Control")
	if !strings.Contains(cc, "private") || !strings.Contains(cc, "no-store") {
		t.Fatalf("overview 必须 private,no-store（防跨用户缓存泄露），实际 Cache-Control=%q", cc)
	}
	if strings.Contains(cc, "public") {
		t.Fatalf("overview 禁止 public 缓存，实际 Cache-Control=%q", cc)
	}
}
