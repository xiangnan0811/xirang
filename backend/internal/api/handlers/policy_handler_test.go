package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/config"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openPolicyHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Policy{}, &model.Node{}, &model.PolicyNode{}, &model.NodeOwner{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db
}

type policySQLRecorder struct {
	statements []string
}

func (r *policySQLRecorder) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return r
}

func (r *policySQLRecorder) Info(context.Context, string, ...any)  {}
func (r *policySQLRecorder) Warn(context.Context, string, ...any)  {}
func (r *policySQLRecorder) Error(context.Context, string, ...any) {}

func (r *policySQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	if sql != "" {
		r.statements = append(r.statements, sql)
	}
}

func (r *policySQLRecorder) containsFullNodeSelect() bool {
	for _, statement := range r.statements {
		normalized := strings.ToLower(statement)
		if strings.Contains(normalized, "from `nodes`") ||
			strings.Contains(normalized, `from "nodes"`) ||
			strings.Contains(normalized, "from nodes ") {
			return true
		}
	}
	return false
}

func (r *policySQLRecorder) containsPolicyNodeSelect() bool {
	for _, statement := range r.statements {
		normalized := strings.ToLower(statement)
		if strings.Contains(normalized, "from `policy_nodes`") ||
			strings.Contains(normalized, `from "policy_nodes"`) ||
			strings.Contains(normalized, "from policy_nodes ") {
			return true
		}
	}
	return false
}

func newPolicyHandlerWithSQLRecorder(db *gorm.DB) (*PolicyHandler, *policySQLRecorder) {
	recorder := &policySQLRecorder{}
	return NewPolicyHandler(db.Session(&gorm.Session{Logger: recorder}), nil), recorder
}

func equalUintSlices(a, b []uint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPolicyListNodeIDsUseJoinTableWithoutLoadingNodes(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	nodeA := model.Node{Name: "node-a", Host: "10.10.0.1", BackupDir: "/backup/node-a"}
	nodeB := model.Node{Name: "node-b", Host: "10.10.0.2", BackupDir: "/backup/node-b"}
	nodeC := model.Node{Name: "node-c", Host: "10.10.0.3", BackupDir: "/backup/node-c"}
	for _, node := range []*model.Node{&nodeA, &nodeB, &nodeC} {
		if err := db.Create(node).Error; err != nil {
			t.Fatalf("创建测试节点失败: %v", err)
		}
	}

	noNodes := model.Policy{Name: "policy-no-nodes", SourcePath: "/data/none", TargetPath: config.BackupRoot, CronSpec: "0 1 * * *"}
	oneNode := model.Policy{Name: "policy-one-node", SourcePath: "/data/one", TargetPath: config.BackupRoot, CronSpec: "0 2 * * *"}
	multipleNodes := model.Policy{Name: "policy-multiple-nodes", SourcePath: "/data/multiple", TargetPath: config.BackupRoot, CronSpec: "0 3 * * *"}
	for _, policyEntity := range []*model.Policy{&noNodes, &oneNode, &multipleNodes} {
		if err := db.Create(policyEntity).Error; err != nil {
			t.Fatalf("创建测试策略失败: %v", err)
		}
	}
	for _, link := range []model.PolicyNode{
		{PolicyID: oneNode.ID, NodeID: nodeA.ID},
		{PolicyID: multipleNodes.ID, NodeID: nodeB.ID},
		{PolicyID: multipleNodes.ID, NodeID: nodeC.ID},
	} {
		if err := db.Create(&link).Error; err != nil {
			t.Fatalf("创建策略节点关联失败: %v", err)
		}
	}

	handler, recorder := newPolicyHandlerWithSQLRecorder(db)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.GET("/policies", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/policies", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Data []struct {
			ID      uint   `json:"id"`
			NodeIDs []uint `json:"node_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, resp.Body.String())
	}
	if len(envelope.Data) != 3 {
		t.Fatalf("期望 3 条策略，实际: %d", len(envelope.Data))
	}

	wantNodeIDs := map[uint][]uint{
		noNodes.ID:       {},
		oneNode.ID:       {nodeA.ID},
		multipleNodes.ID: {nodeB.ID, nodeC.ID},
	}
	for _, item := range envelope.Data {
		if !equalUintSlices(item.NodeIDs, wantNodeIDs[item.ID]) {
			t.Fatalf("policy %d node_ids 期望 %v，实际 %v", item.ID, wantNodeIDs[item.ID], item.NodeIDs)
		}
	}
	if !recorder.containsPolicyNodeSelect() {
		t.Fatalf("期望通过 policy_nodes 查询 node_ids，实际 SQL: %#v", recorder.statements)
	}
	if recorder.containsFullNodeSelect() {
		t.Fatalf("列表响应不应加载完整 nodes 记录，实际 SQL: %#v", recorder.statements)
	}
}

func TestPolicyGetNodeIDsUseJoinTableWithoutLoadingNodes(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	nodeA := model.Node{Name: "detail-node-a", Host: "10.20.0.1", BackupDir: "/backup/detail-a"}
	nodeB := model.Node{Name: "detail-node-b", Host: "10.20.0.2", BackupDir: "/backup/detail-b"}
	for _, node := range []*model.Node{&nodeA, &nodeB} {
		if err := db.Create(node).Error; err != nil {
			t.Fatalf("创建测试节点失败: %v", err)
		}
	}
	policyEntity := model.Policy{Name: "policy-detail-node-ids", SourcePath: "/data/detail", TargetPath: config.BackupRoot, CronSpec: "0 4 * * *"}
	if err := db.Create(&policyEntity).Error; err != nil {
		t.Fatalf("创建测试策略失败: %v", err)
	}
	for _, link := range []model.PolicyNode{
		{PolicyID: policyEntity.ID, NodeID: nodeA.ID},
		{PolicyID: policyEntity.ID, NodeID: nodeB.ID},
	} {
		if err := db.Create(&link).Error; err != nil {
			t.Fatalf("创建策略节点关联失败: %v", err)
		}
	}

	handler, recorder := newPolicyHandlerWithSQLRecorder(db)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.GET("/policies/:id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/policies/%d", policyEntity.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Data struct {
			ID      uint   `json:"id"`
			NodeIDs []uint `json:"node_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, resp.Body.String())
	}
	if envelope.Data.ID != policyEntity.ID {
		t.Fatalf("期望 policy id=%d，实际=%d", policyEntity.ID, envelope.Data.ID)
	}
	wantNodeIDs := []uint{nodeA.ID, nodeB.ID}
	if !equalUintSlices(envelope.Data.NodeIDs, wantNodeIDs) {
		t.Fatalf("详情 node_ids 期望 %v，实际 %v", wantNodeIDs, envelope.Data.NodeIDs)
	}
	if !recorder.containsPolicyNodeSelect() {
		t.Fatalf("期望通过 policy_nodes 查询 node_ids，实际 SQL: %#v", recorder.statements)
	}
	if recorder.containsFullNodeSelect() {
		t.Fatalf("详情响应不应加载完整 nodes 记录，实际 SQL: %#v", recorder.statements)
	}
}

// TestPolicyUpdateWarningUsesEnvelope reproduces the regression where toggling a
// policy whose stored target_path differs from /backup returned a raw
// {"data": ..., "warning": ...} payload instead of the standard
// {"code", "message", "data"} envelope. The frontend's auto-unwrap relies on
// the "code" field, so the raw shape leaked the wrapper into PolicyResponse,
// surfacing as `Cannot read properties of undefined (reading 'trim')` inside
// describeCron when naturalLanguage was rebuilt.
func TestPolicyUpdateWarningUsesEnvelope(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	legacy := model.Policy{
		Name:       "legacy-policy",
		SourcePath: "/srv/data",
		TargetPath: "/legacy/backup",
		CronSpec:   "0 */2 * * *",
		Enabled:    true,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("创建测试策略失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewPolicyHandler(db, nil)
	r.PUT("/policies/:id", handler.Update)

	body := map[string]any{
		"name":        legacy.Name,
		"source_path": legacy.SourcePath,
		"cron_spec":   legacy.CronSpec,
		"enabled":     false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/policies/%d", legacy.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}

	// 顶层结构必须遵循统一信封 {code, message, data}；前端 request() 依赖顶层
	// code 字段决定是否自动解包。任何顶层 warning 字段都意味着旧的非信封格式。
	var top map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &top); err != nil {
		t.Fatalf("解析顶层响应失败: %v, body=%s", err, resp.Body.String())
	}
	if _, ok := top["code"]; !ok {
		t.Fatalf("响应缺少顶层 'code' 字段，违反统一信封约定，body=%s", resp.Body.String())
	}
	if _, ok := top["warning"]; ok {
		t.Fatalf("响应不应在顶层暴露 'warning'，应该放进 envelope.message，body=%s", resp.Body.String())
	}

	var envelope struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, resp.Body.String())
	}

	if envelope.Code != http.StatusOK {
		t.Fatalf("期望 envelope code=200，实际: %d", envelope.Code)
	}
	if envelope.Data == nil {
		t.Fatalf("envelope.data 不应为空，body=%s", resp.Body.String())
	}
	if cron, ok := envelope.Data["cron_spec"].(string); !ok || cron != legacy.CronSpec {
		t.Fatalf("期望 data.cron_spec=%q，实际: %v", legacy.CronSpec, envelope.Data["cron_spec"])
	}
	if target, ok := envelope.Data["target_path"].(string); !ok || target != config.BackupRoot {
		t.Fatalf("期望 data.target_path=%q，实际: %v", config.BackupRoot, envelope.Data["target_path"])
	}

	// 警告信息必须保留，建议放进 envelope.message，便于前端用 toast 提示。
	if !strings.Contains(envelope.Message, "/legacy/backup") {
		t.Fatalf("期望 envelope.message 包含旧路径 '/legacy/backup'，实际: %q", envelope.Message)
	}
}

func TestPolicyHooksHiddenFromNonAdminResponses(t *testing.T) {
	db := openPolicyHandlerTestDB(t)
	policyWithHook := model.Policy{
		Name:       "hooked-policy",
		SourcePath: "/srv/data",
		TargetPath: config.BackupRoot,
		CronSpec:   "0 2 * * *",
		PreHook:    "echo FAKE_HOOK_SECRET_FOR_TEST_ONLY",
		PostHook:   "echo cleanup",
	}
	if err := db.Create(&policyWithHook).Error; err != nil {
		t.Fatalf("创建带 hook 的策略失败: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	makeRouter := func(role string) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("role", role); c.Next() })
		r.GET("/policies/:id", handler.Get)
		return r
	}

	viewerReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/policies/%d", policyWithHook.ID), nil)
	viewerResp := httptest.NewRecorder()
	makeRouter("viewer").ServeHTTP(viewerResp, viewerReq)
	if viewerResp.Code != http.StatusOK {
		t.Fatalf("viewer 读取策略应成功，实际: %d %s", viewerResp.Code, viewerResp.Body.String())
	}
	var viewerEnvelope struct {
		Data struct {
			PreHook  string `json:"pre_hook"`
			PostHook string `json:"post_hook"`
		} `json:"data"`
	}
	if err := json.Unmarshal(viewerResp.Body.Bytes(), &viewerEnvelope); err != nil {
		t.Fatalf("解析 viewer 响应失败: %v", err)
	}
	if viewerEnvelope.Data.PreHook != "" || viewerEnvelope.Data.PostHook != "" {
		t.Fatalf("非 admin 响应不应暴露 hook，data=%+v", viewerEnvelope.Data)
	}

	adminReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/policies/%d", policyWithHook.ID), nil)
	adminResp := httptest.NewRecorder()
	makeRouter("admin").ServeHTTP(adminResp, adminReq)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("admin 读取策略应成功，实际: %d %s", adminResp.Code, adminResp.Body.String())
	}
	var adminEnvelope struct {
		Data struct {
			PreHook string `json:"pre_hook"`
		} `json:"data"`
	}
	if err := json.Unmarshal(adminResp.Body.Bytes(), &adminEnvelope); err != nil {
		t.Fatalf("解析 admin 响应失败: %v", err)
	}
	if adminEnvelope.Data.PreHook != "echo FAKE_HOOK_SECRET_FOR_TEST_ONLY" {
		t.Fatalf("admin 响应应保留 hook，data=%+v", adminEnvelope.Data)
	}
}

func TestPolicyUpdateByNonAdminDoesNotClearHiddenHooks(t *testing.T) {
	db := openPolicyHandlerTestDB(t)
	preHook := "echo FAKE_OPERATOR_HIDDEN_PRE_HOOK_FOR_TEST_ONLY"
	postHook := "echo FAKE_OPERATOR_HIDDEN_POST_HOOK_FOR_TEST_ONLY"
	policyWithHook := model.Policy{
		Name:       "operator-edit-policy",
		SourcePath: "/srv/data",
		TargetPath: config.BackupRoot,
		CronSpec:   "0 2 * * *",
		Enabled:    true,
		PreHook:    preHook,
		PostHook:   postHook,
	}
	if err := db.Create(&policyWithHook).Error; err != nil {
		t.Fatalf("创建带 hook 的策略失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "operator"); c.Next() })
	r.PUT("/policies/:id", NewPolicyHandler(db, nil).Update)

	body := map[string]any{
		"name":        "operator-edited-policy",
		"source_path": policyWithHook.SourcePath,
		"cron_spec":   policyWithHook.CronSpec,
		"enabled":     false,
		"pre_hook":    "",
		"post_hook":   "",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/policies/%d", policyWithHook.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("operator 更新策略应成功，实际: %d %s", resp.Code, resp.Body.String())
	}

	var updated model.Policy
	if err := db.First(&updated, policyWithHook.ID).Error; err != nil {
		t.Fatalf("重新读取策略失败: %v", err)
	}
	if updated.PreHook != preHook || updated.PostHook != postHook {
		t.Fatalf("非 admin 更新不应清空隐藏 hook，pre=%q post=%q", updated.PreHook, updated.PostHook)
	}
}

// DrillConfig 测试辅助函数：创建 Gin 路由并设置 admin 角色
func setupDrillTestRouter(handler *PolicyHandler) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.POST("/policies", handler.Create)
	r.GET("/policies/:id", handler.Get)
	r.PUT("/policies/:id", handler.Update)
	return r
}

// TestDrillCreateValid 测试创建启用 drill 的有效策略
func TestPolicyCreatePersistsEscalationPolicy(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))
	escalationPolicyID := uint(42)
	body := map[string]any{
		"name":                 "policy-escalation",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"escalation_policy_id": escalationPolicyID,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			ID                 uint  `json:"id"`
			EscalationPolicyID *uint `json:"escalation_policy_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Data.EscalationPolicyID == nil || *envelope.Data.EscalationPolicyID != escalationPolicyID {
		t.Fatalf("期望响应包含 escalation_policy_id=%d，实际: %#v", escalationPolicyID, envelope.Data.EscalationPolicyID)
	}
	var policyEntity model.Policy
	if err := db.First(&policyEntity, envelope.Data.ID).Error; err != nil {
		t.Fatalf("查询策略失败: %v", err)
	}
	if policyEntity.EscalationPolicyID == nil || *policyEntity.EscalationPolicyID != escalationPolicyID {
		t.Fatalf("期望持久化 escalation_policy_id=%d，实际: %#v", escalationPolicyID, policyEntity.EscalationPolicyID)
	}
}

func TestPolicyRejectsInvalidRetryConfig(t *testing.T) {
	db := openPolicyHandlerTestDB(t)
	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "negative max retries",
			body: map[string]any{"name": "bad-retry-count", "source_path": "/data", "cron_spec": "0 2 * * *", "max_retries": -1},
		},
		{
			name: "zero retry base",
			body: map[string]any{"name": "bad-retry-base", "source_path": "/data", "cron_spec": "0 2 * * *", "retry_base_seconds": 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/policies", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestDrillCreateValid(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	sandbox := model.Node{Name: "sandbox-node", Host: "10.0.0.100", BackupDir: "/backup/sandbox-node"}
	db.Create(&sandbox)
	source := model.Node{Name: "source-node", Host: "10.0.0.1", BackupDir: "/backup/source-node"}
	db.Create(&source)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":                 "drill-policy-valid",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"drill_enabled":        true,
		"drill_cron":           "0 3 * * *",
		"drill_target_node_id": sandbox.ID,
		"drill_restore_path":   "/tmp/drill-test",
		"drill_verify":         "echo ok",
		"node_ids":             []uint{source.ID},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["drill_enabled"] != true {
		t.Error("expected drill_enabled=true")
	}
	if envelope.Data["drill_cron"] != "0 3 * * *" {
		t.Errorf("expected drill_cron='0 3 * * *', got %v", envelope.Data["drill_cron"])
	}
	if envelope.Data["drill_target_node_id"] != float64(sandbox.ID) {
		t.Errorf("expected drill_target_node_id=%d", sandbox.ID)
	}
}

// TestDrillCreateMissingCron 测试启用 drill 但未设置 cron 时报错
func TestDrillCreateMissingCron(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	sandbox := model.Node{Name: "sandbox-node", Host: "10.0.0.100", BackupDir: "/backup/sandbox-cron"}
	db.Create(&sandbox)
	source := model.Node{Name: "source-node", Host: "10.0.0.1", BackupDir: "/backup/source-cron"}
	db.Create(&source)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":                 "drill-no-cron",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"drill_enabled":        true,
		"drill_target_node_id": sandbox.ID,
		"drill_restore_path":   "/tmp/drill-test",
		"node_ids":             []uint{source.ID},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestDrillCreateSandboxEqualsSource 测试沙箱节点与源节点相同时报错
func TestDrillCreateSandboxEqualsSource(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	node := model.Node{Name: "dual-role-node", Host: "10.0.0.1", BackupDir: "/backup/dual-role"}
	db.Create(&node)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":                 "drill-same-node",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"drill_enabled":        true,
		"drill_cron":           "0 3 * * *",
		"drill_target_node_id": node.ID,
		"drill_restore_path":   "/tmp/drill-test",
		"node_ids":             []uint{node.ID},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestDrillCreateRejectsForbiddenSystemSubpaths 测试恢复演练禁止写入系统目录及其子路径。
func TestDrillCreateRejectsForbiddenSystemSubpaths(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	sandbox := model.Node{Name: "sandbox-forbidden-path", Host: "10.0.0.100", BackupDir: "/backup/sandbox-forbidden"}
	if err := db.Create(&sandbox).Error; err != nil {
		t.Fatalf("创建沙箱节点失败: %v", err)
	}
	source := model.Node{Name: "source-forbidden-path", Host: "10.0.0.1", BackupDir: "/backup/source-forbidden"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("创建源节点失败: %v", err)
	}

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))
	for _, restorePath := range []string{"/dev", "/dev/xirang", "/proc/self", "/sys/fs", "/run/xirang", "/var/run/xirang"} {
		t.Run(restorePath, func(t *testing.T) {
			body := map[string]any{
				"name":                 "drill-forbidden-path",
				"source_path":          "/data",
				"cron_spec":            "0 2 * * *",
				"drill_enabled":        true,
				"drill_cron":           "0 3 * * *",
				"drill_target_node_id": sandbox.ID,
				"drill_restore_path":   restorePath,
				"node_ids":             []uint{source.ID},
			}
			raw, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/policies", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)

			if resp.Code != http.StatusBadRequest {
				t.Fatalf("期望禁止路径 %s 返回 400，实际: %d, body=%s", restorePath, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestDrillCreateDisabled(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	// drill_enabled=false, drill_cron 留空 → 应该成功
	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":          "drill-disabled",
		"source_path":   "/data",
		"cron_spec":     "0 2 * * *",
		"drill_enabled": false,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestDrillUpdateAddConfig 测试更新策略添加 drill 配置
func TestDrillUpdateAddConfig(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	sandbox := model.Node{Name: "sandbox-node", Host: "10.0.0.100", BackupDir: "/backup/sandbox-update"}
	db.Create(&sandbox)
	source := model.Node{Name: "source-node", Host: "10.0.0.1", BackupDir: "/backup/source-update"}
	db.Create(&source)

	// 先创建一个不含 drill 的策略
	p := model.Policy{
		Name:       "no-drill-initially",
		SourcePath: "/data",
		CronSpec:   "0 2 * * *",
		Enabled:    true,
	}
	db.Create(&p)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	// 更新策略添加 drill 配置
	body := map[string]any{
		"name":                 "no-drill-initially",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"drill_enabled":        true,
		"drill_cron":           "0 4 * * *",
		"drill_target_node_id": sandbox.ID,
		"drill_restore_path":   "/tmp/drill-update",
		"drill_verify":         "echo updated",
		"drill_auto_cleanup":   false,
		"node_ids":             []uint{source.ID},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", fmt.Sprintf("/policies/%d", p.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["drill_enabled"] != true {
		t.Error("expected drill_enabled=true after update")
	}
	if envelope.Data["drill_auto_cleanup"] != false {
		t.Error("expected drill_auto_cleanup=false after update")
	}
}

// TestDrillUpdateWithoutDrillFieldsPreservesExistingConfig 测试局部更新不会清空既有 drill 配置。
func TestDrillUpdateWithoutDrillFieldsPreservesExistingConfig(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	sandboxID := uint(42)
	// Plaintext expected values (BeforeSave encrypts in-place on Create; do not
	// compare against the mutated policyEntity fields).
	const (
		wantDrillCron       = "0 4 * * *"
		wantDrillPath       = "/tmp/drill-preserve"
		wantDrillPreVerify  = "test -d /tmp/drill-preserve"
		wantDrillVerify     = "test -f /tmp/drill-preserve/ok"
		wantDrillPostVerify = "true"
	)
	policyEntity := model.Policy{
		Name:              "preserve-drill-config",
		SourcePath:        "/data",
		TargetPath:        config.BackupRoot,
		CronSpec:          "0 2 * * *",
		Enabled:           true,
		DrillEnabled:      true,
		DrillCron:         wantDrillCron,
		DrillTargetNodeID: &sandboxID,
		DrillRestorePath:  wantDrillPath,
		DrillPreVerify:    wantDrillPreVerify,
		DrillVerify:       wantDrillVerify,
		DrillPostVerify:   wantDrillPostVerify,
		// GORM default:true on bool zero-value stores true on Create; use true for seed.
		DrillAutoCleanup:    true,
		MaxConcurrent:       1,
		RetentionDays:       7,
		VerifyEnabled:       true,
		VerifySampleRate:    0,
		HookTimeoutSeconds:  300,
		MaxExecutionSeconds: 0,
		RetentionMode:       "simple",
	}
	if err := db.Create(&policyEntity).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))
	body := map[string]any{
		"name":        "preserve-drill-config-renamed",
		"source_path": "/data-renamed",
		"cron_spec":   "0 5 * * *",
		"enabled":     true,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/policies/%d", policyEntity.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}

	var updated model.Policy
	if err := db.First(&updated, policyEntity.ID).Error; err != nil {
		t.Fatalf("查询更新后策略失败: %v", err)
	}
	if !updated.DrillEnabled || updated.DrillCron != wantDrillCron || updated.DrillRestorePath != wantDrillPath {
		t.Fatalf("drill 基础配置未保留: %+v", updated)
	}
	if updated.DrillTargetNodeID == nil || *updated.DrillTargetNodeID != sandboxID {
		t.Fatalf("drill_target_node_id 未保留: %#v", updated.DrillTargetNodeID)
	}
	if updated.DrillPreVerify != wantDrillPreVerify || updated.DrillVerify != wantDrillVerify || updated.DrillPostVerify != wantDrillPostVerify {
		t.Fatalf("drill 校验脚本未保留: %+v", updated)
	}
	if !updated.DrillAutoCleanup {
		t.Fatalf("drill_auto_cleanup 未保留为 true: %v", updated.DrillAutoCleanup)
	}
}

// TestDrillGetIncludesFields 测试获取策略时响应包含 drill 字段
func TestDrillGetIncludesFields(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	p := model.Policy{
		Name:             "drill-inspect",
		SourcePath:       "/data",
		CronSpec:         "0 2 * * *",
		Enabled:          true,
		DrillEnabled:     true,
		DrillCron:        "0 5 * * *",
		DrillRestorePath: "/tmp/drill-inspect",
		DrillVerify:      "echo inspect",
	}
	db.Create(&p)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	req := httptest.NewRequest("GET", fmt.Sprintf("/policies/%d", p.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)

	// 检查 drill 字段存在
	if _, ok := envelope.Data["drill_enabled"]; !ok {
		t.Error("response missing drill_enabled")
	}
	if _, ok := envelope.Data["drill_cron"]; !ok {
		t.Error("response missing drill_cron")
	}
	if _, ok := envelope.Data["drill_target_node_id"]; !ok {
		t.Error("response missing drill_target_node_id")
	}
	if _, ok := envelope.Data["drill_restore_path"]; !ok {
		t.Error("response missing drill_restore_path")
	}
	if _, ok := envelope.Data["drill_pre_verify"]; !ok {
		t.Error("response missing drill_pre_verify")
	}
	if _, ok := envelope.Data["drill_verify"]; !ok {
		t.Error("response missing drill_verify")
	}
	if _, ok := envelope.Data["drill_post_verify"]; !ok {
		t.Error("response missing drill_post_verify")
	}
	if _, ok := envelope.Data["drill_auto_cleanup"]; !ok {
		t.Error("response missing drill_auto_cleanup")
	}
}

// TestGFSCreateValid 测试创建 GFS 保留模式的策略
func TestPolicyListIncludesLatestDrillSummary(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	policyEntity := model.Policy{Name: "drill-summary", SourcePath: "/data", TargetPath: config.BackupRoot, CronSpec: "0 2 * * *", Enabled: true}
	if err := db.Create(&policyEntity).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	taskEntity := model.Task{Name: "task-drill-summary", PolicyID: &policyEntity.ID, NodeID: 1, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	oldRun := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "drill", Status: "success"}
	if err := db.Create(&oldRun).Error; err != nil {
		t.Fatalf("创建旧演练记录失败: %v", err)
	}
	oldStartedAt := time.Now().Add(-2 * time.Hour).UTC()
	if err := db.Create(&model.RestoreDrillEvidence{
		PolicyID:           policyEntity.ID,
		TaskID:             taskEntity.ID,
		TaskRunID:          oldRun.ID,
		SandboxNodeID:      2,
		SandboxNodeName:    "sandbox-old",
		SandboxPath:        "/tmp/drill-old",
		Status:             "success",
		ConfidenceEligible: true,
		StartedAt:          &oldStartedAt,
		FinishedAt:         &oldStartedAt,
		DurationMs:         1000,
	}).Error; err != nil {
		t.Fatalf("创建旧演练证据失败: %v", err)
	}
	newRun := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "drill", Status: "failed"}
	if err := db.Create(&newRun).Error; err != nil {
		t.Fatalf("创建新演练记录失败: %v", err)
	}
	newStartedAt := time.Now().Add(-time.Hour).UTC()
	newFinishedAt := newStartedAt.Add(5 * time.Second)
	if err := db.Create(&model.RestoreDrillEvidence{
		PolicyID:           policyEntity.ID,
		TaskID:             taskEntity.ID,
		TaskRunID:          newRun.ID,
		SandboxNodeID:      2,
		SandboxNodeName:    "sandbox-new",
		SandboxPath:        "/tmp/drill-new",
		Status:             "failed",
		FailedStep:         "verify",
		ConfidenceEligible: false,
		StartedAt:          &newStartedAt,
		FinishedAt:         &newFinishedAt,
		DurationMs:         5000,
	}).Error; err != nil {
		t.Fatalf("创建新演练证据失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.GET("/policies", NewPolicyHandler(db, nil).List)

	req := httptest.NewRequest(http.MethodGet, "/policies", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int              `json:"code"`
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("期望 1 条策略，实际: %d", len(envelope.Data))
	}
	latest, ok := envelope.Data[0]["latest_drill"].(map[string]any)
	if !ok || latest == nil {
		t.Fatalf("期望 latest_drill 对象，实际: %#v", envelope.Data[0]["latest_drill"])
	}
	if latest["task_run_id"] != float64(newRun.ID) {
		t.Fatalf("期望返回最新演练 run #%d，实际: %v", newRun.ID, latest["task_run_id"])
	}
	if latest["status"] != "failed" {
		t.Fatalf("期望 latest_drill.status=failed，实际: %v", latest["status"])
	}
	if latest["failed_step"] != "verify" {
		t.Fatalf("期望 latest_drill.failed_step=verify，实际: %v", latest["failed_step"])
	}
	if latest["confidence_eligible"] != false {
		t.Fatalf("失败演练不应作为正向可信证据，实际: %v", latest["confidence_eligible"])
	}
	if latest["duration_ms"] != float64(5000) {
		t.Fatalf("期望 latest_drill.duration_ms=5000，实际: %v", latest["duration_ms"])
	}
}

func TestGFSCreateValid(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":           "gfs-policy-valid",
		"source_path":    "/data",
		"cron_spec":      "0 2 * * *",
		"retention_mode": "gfs",
		"keep_daily":     7,
		"keep_weekly":    4,
		"keep_monthly":   12,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["retention_mode"] != "gfs" {
		t.Errorf("expected retention_mode='gfs', got %v", envelope.Data["retention_mode"])
	}
	if envelope.Data["keep_daily"] != float64(7) {
		t.Errorf("expected keep_daily=7, got %v", envelope.Data["keep_daily"])
	}
	if envelope.Data["keep_weekly"] != float64(4) {
		t.Errorf("expected keep_weekly=4, got %v", envelope.Data["keep_weekly"])
	}
}

// TestGFSCreateNoKeepValues 测试 GFS 模式但未设置任何 keep 值 → 400
func TestGFSCreateNoKeepValues(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":           "gfs-no-keep",
		"source_path":    "/data",
		"cron_spec":      "0 2 * * *",
		"retention_mode": "gfs",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for GFS without keep values, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestRPORTOFieldsInResponse 测试创建带 RPO/RTO 目标的策略 → 响应包含这些字段
func TestRPORTOFieldsInResponse(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":        "rpo-rto-policy",
		"source_path": "/data",
		"cron_spec":   "0 2 * * *",
		"rpo_minutes": 60,
		"rto_minutes": 120,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["rpo_minutes"] != float64(60) {
		t.Errorf("expected rpo_minutes=60, got %v", envelope.Data["rpo_minutes"])
	}
	if envelope.Data["rto_minutes"] != float64(120) {
		t.Errorf("expected rto_minutes=120, got %v", envelope.Data["rto_minutes"])
	}
	if envelope.Data["retention_mode"] != "simple" {
		t.Errorf("expected default retention_mode='simple', got %v", envelope.Data["retention_mode"])
	}
}

// TestGFSUpdateAddKeepValues 测试更新策略添加 GFS 保留配置
func TestGFSUpdateAddKeepValues(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	p := model.Policy{
		Name:       "gfs-update-test",
		SourcePath: "/data",
		CronSpec:   "0 2 * * *",
		Enabled:    true,
	}
	db.Create(&p)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":           "gfs-update-test",
		"source_path":    "/data",
		"cron_spec":      "0 2 * * *",
		"retention_mode": "gfs",
		"keep_daily":     14,
		"keep_weekly":    8,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", fmt.Sprintf("/policies/%d", p.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["retention_mode"] != "gfs" {
		t.Errorf("expected retention_mode='gfs' after update, got %v", envelope.Data["retention_mode"])
	}
	if envelope.Data["keep_daily"] != float64(14) {
		t.Errorf("expected keep_daily=14, got %v", envelope.Data["keep_daily"])
	}
}

// TestInvalidRetentionMode 测试非法 retention_mode → 400
func TestInvalidRetentionMode(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":           "invalid-mode",
		"source_path":    "/data",
		"cron_spec":      "0 2 * * *",
		"retention_mode": "invalid",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid retention_mode, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestPolicyListGetOperatorFiltersUnownedNodesAndDrillSummary ensures shared
// policies only expose node_ids and latest_drill the operator is authorized to see.
func TestPolicyListGetOperatorFiltersUnownedNodesAndDrillSummary(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	owned := model.Node{Name: "op-owned", Host: "10.1.0.1", BackupDir: "/backup/op-owned"}
	unowned := model.Node{Name: "op-unowned", Host: "10.1.0.2", BackupDir: "/backup/op-unowned"}
	for _, n := range []*model.Node{&owned, &unowned} {
		if err := db.Create(n).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	policyEntity := model.Policy{Name: "shared-policy-leak", SourcePath: "/data", TargetPath: config.BackupRoot, CronSpec: "0 2 * * *", Enabled: true}
	if err := db.Create(&policyEntity).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	for _, link := range []model.PolicyNode{
		{PolicyID: policyEntity.ID, NodeID: owned.ID},
		{PolicyID: policyEntity.ID, NodeID: unowned.ID},
	} {
		if err := db.Create(&link).Error; err != nil {
			t.Fatalf("创建策略节点关联失败: %v", err)
		}
	}

	ownedTask := model.Task{Name: "owned-task", PolicyID: &policyEntity.ID, NodeID: owned.ID, ExecutorType: "rsync", Status: "success"}
	unownedTask := model.Task{Name: "unowned-task", PolicyID: &policyEntity.ID, NodeID: unowned.ID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&ownedTask).Error; err != nil {
		t.Fatalf("创建 owned task 失败: %v", err)
	}
	if err := db.Create(&unownedTask).Error; err != nil {
		t.Fatalf("创建 unowned task 失败: %v", err)
	}

	// Only unowned task has drill evidence — must not appear for operator.
	unownedRun := model.TaskRun{TaskID: unownedTask.ID, TriggerType: "drill", Status: "success"}
	if err := db.Create(&unownedRun).Error; err != nil {
		t.Fatalf("创建 unowned drill run 失败: %v", err)
	}
	started := time.Now().UTC()
	if err := db.Create(&model.RestoreDrillEvidence{
		PolicyID:           policyEntity.ID,
		TaskID:             unownedTask.ID,
		TaskRunID:          unownedRun.ID,
		SandboxNodeID:      unowned.ID,
		SandboxNodeName:    "sandbox-unowned",
		SandboxPath:        "/tmp/drill-unowned",
		Status:             "success",
		ConfidenceEligible: true,
		StartedAt:          &started,
		FinishedAt:         &started,
		DurationMs:         1000,
	}).Error; err != nil {
		t.Fatalf("创建 unowned drill evidence 失败: %v", err)
	}

	const operatorID = uint(42)
	if err := db.Create(&model.NodeOwner{NodeID: owned.ID, UserID: operatorID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", "operator")
		c.Set("userID", operatorID)
		c.Next()
	})
	r.GET("/policies", handler.List)
	r.GET("/policies/:id", handler.Get)

	// List
	listReq := httptest.NewRequest(http.MethodGet, "/policies", nil)
	listResp := httptest.NewRecorder()
	r.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("List 期望 200，实际: %d body=%s", listResp.Code, listResp.Body.String())
	}
	var listEnvelope struct {
		Data []struct {
			ID          uint           `json:"id"`
			NodeIDs     []uint         `json:"node_ids"`
			LatestDrill map[string]any `json:"latest_drill"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listEnvelope); err != nil {
		t.Fatalf("解析 List 失败: %v", err)
	}
	if len(listEnvelope.Data) != 1 {
		t.Fatalf("List 期望 1 条策略，实际: %d", len(listEnvelope.Data))
	}
	if !equalUintSlices(listEnvelope.Data[0].NodeIDs, []uint{owned.ID}) {
		t.Fatalf("List node_ids 应仅含 owned=%d，实际: %v", owned.ID, listEnvelope.Data[0].NodeIDs)
	}
	if listEnvelope.Data[0].LatestDrill != nil {
		t.Fatalf("List 不应泄露 unowned latest_drill: %#v", listEnvelope.Data[0].LatestDrill)
	}

	// Get
	getReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/policies/%d", policyEntity.ID), nil)
	getResp := httptest.NewRecorder()
	r.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("Get 期望 200，实际: %d body=%s", getResp.Code, getResp.Body.String())
	}
	var getEnvelope struct {
		Data struct {
			NodeIDs     []uint         `json:"node_ids"`
			LatestDrill map[string]any `json:"latest_drill"`
		} `json:"data"`
	}
	if err := json.Unmarshal(getResp.Body.Bytes(), &getEnvelope); err != nil {
		t.Fatalf("解析 Get 失败: %v", err)
	}
	if !equalUintSlices(getEnvelope.Data.NodeIDs, []uint{owned.ID}) {
		t.Fatalf("Get node_ids 应仅含 owned=%d，实际: %v", owned.ID, getEnvelope.Data.NodeIDs)
	}
	if getEnvelope.Data.LatestDrill != nil {
		t.Fatalf("Get 不应泄露 unowned latest_drill: %#v", getEnvelope.Data.LatestDrill)
	}
}

// TestPolicyLatestDrillRequiresBothSourceAndSandboxOwnership: owning only the
// source task node or only the sandbox must not reveal latest_drill metadata.
func TestPolicyLatestDrillRequiresBothSourceAndSandboxOwnership(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	source := model.Node{Name: "drill-src", Host: "10.3.0.1", BackupDir: "/backup/drill-src"}
	sandbox := model.Node{Name: "drill-sbx", Host: "10.3.0.2", BackupDir: "/backup/drill-sbx"}
	for _, n := range []*model.Node{&source, &sandbox} {
		if err := db.Create(n).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	policyEntity := model.Policy{Name: "both-ends-policy", SourcePath: "/data", TargetPath: config.BackupRoot, CronSpec: "0 2 * * *", Enabled: true}
	if err := db.Create(&policyEntity).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policyEntity.ID, NodeID: source.ID}).Error; err != nil {
		t.Fatalf("创建策略节点关联失败: %v", err)
	}
	taskEntity := model.Task{Name: "both-ends-task", PolicyID: &policyEntity.ID, NodeID: source.ID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	run := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "drill", Status: "failed"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建 drill run 失败: %v", err)
	}
	started := time.Now().UTC()
	if err := db.Create(&model.RestoreDrillEvidence{
		PolicyID:           policyEntity.ID,
		TaskID:             taskEntity.ID,
		TaskRunID:          run.ID,
		SandboxNodeID:      sandbox.ID,
		SandboxNodeName:    "drill-sbx",
		SandboxPath:        "/tmp/drill-both",
		Status:             "failed",
		FailedStep:         "verify",
		ConfidenceEligible: false,
		StartedAt:          &started,
		FinishedAt:         &started,
		DurationMs:         2500,
	}).Error; err != nil {
		t.Fatalf("创建演练证据失败: %v", err)
	}

	const sourceOnlyOp = uint(51)
	const bothOp = uint(53)
	if err := db.Create(&model.NodeOwner{NodeID: source.ID, UserID: sourceOnlyOp}).Error; err != nil {
		t.Fatalf("source-only ownership: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: source.ID, UserID: bothOp}).Error; err != nil {
		t.Fatalf("both source ownership: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: sandbox.ID, UserID: bothOp}).Error; err != nil {
		t.Fatalf("both sandbox ownership: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	getLatest := func(userID uint) map[string]any {
		t.Helper()
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("role", "operator")
			c.Set("userID", userID)
			c.Next()
		})
		r.GET("/policies/:id", handler.Get)
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/policies/%d", policyEntity.ID), nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("user %d 期望 200，实际 %d body=%s", userID, resp.Code, resp.Body.String())
		}
		var envelope struct {
			Data struct {
				LatestDrill map[string]any `json:"latest_drill"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		return envelope.Data.LatestDrill
	}

	// Source-only: can see policy (union ownership) but must not see drill summary.
	if drill := getLatest(sourceOnlyOp); drill != nil {
		t.Fatalf("仅拥有源节点不应看到 latest_drill: %#v", drill)
	}
	// Sandbox-only cannot access policy at all — covered by
	// TestPolicyGetSandboxOnlyCannotAccessPolicy.
	both := getLatest(bothOp)
	if both == nil {
		t.Fatal("同时拥有源节点与沙箱应看到 latest_drill")
	}
	if both["task_run_id"] != float64(run.ID) || both["status"] != "failed" || both["failed_step"] != "verify" {
		t.Fatalf("双端归属应返回完整摘要，实际: %#v", both)
	}
}

// TestPolicyGetSandboxOnlyCannotAccessPolicy: operator owning only sandbox
// (no source node) must not access the policy at all.
func TestPolicyGetSandboxOnlyCannotAccessPolicy(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	source := model.Node{Name: "sbx-only-src", Host: "10.3.1.1", BackupDir: "/backup/sbx-only-src"}
	sandbox := model.Node{Name: "sbx-only-sbx", Host: "10.3.1.2", BackupDir: "/backup/sbx-only-sbx"}
	for _, n := range []*model.Node{&source, &sandbox} {
		if err := db.Create(n).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	policyEntity := model.Policy{Name: "sbx-only-policy", SourcePath: "/data", TargetPath: config.BackupRoot, CronSpec: "0 2 * * *", Enabled: true}
	if err := db.Create(&policyEntity).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policyEntity.ID, NodeID: source.ID}).Error; err != nil {
		t.Fatalf("创建策略节点关联失败: %v", err)
	}
	const sandboxOnlyOp = uint(61)
	if err := db.Create(&model.NodeOwner{NodeID: sandbox.ID, UserID: sandboxOnlyOp}).Error; err != nil {
		t.Fatalf("sandbox-only ownership: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", "operator")
		c.Set("userID", sandboxOnlyOp)
		c.Next()
	})
	r.GET("/policies/:id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/policies/%d", policyEntity.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("仅拥有沙箱应 403，实际 %d body=%s", resp.Code, resp.Body.String())
	}
}
