package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
)

// mockDrillTriggerer 实现 drillTriggerer 接口，用于测试注入。
type mockDrillTriggerer struct {
	fn                   func(policyID uint, allowedSourceNodeIDs []uint) (uint, error)
	lastAllowedNodeIDs   []uint
	sawAllowedNodeFilter bool
}

func (m *mockDrillTriggerer) TriggerDrill(policyID uint, allowedSourceNodeIDs []uint) (uint, error) {
	m.lastAllowedNodeIDs = allowedSourceNodeIDs
	m.sawAllowedNodeFilter = allowedSourceNodeIDs != nil
	if m.fn != nil {
		return m.fn(policyID, allowedSourceNodeIDs)
	}
	return 42, nil
}

// TestDrillTriggerSuccess 验证对启用 drill 的策略手动触发返回 200 + task_run_id。
func TestDrillTriggerSuccess(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	policy := model.Policy{
		Name:         "drill-enabled-policy",
		SourcePath:   "/tmp/src",
		TargetPath:   "/tmp/dst",
		CronSpec:     "@daily",
		DrillEnabled: true,
		DrillCron:    "@every 5m",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	mock := &mockDrillTriggerer{
		fn: func(policyID uint, allowedSourceNodeIDs []uint) (uint, error) {
			if policyID != policy.ID {
				return 0, fmt.Errorf("策略 ID 不匹配: %d != %d", policyID, policy.ID)
			}
			if allowedSourceNodeIDs != nil {
				return 0, fmt.Errorf("admin 不应传入源节点过滤")
			}
			return 42, nil
		},
	}
	handler.drillTriggerer = mock

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/policies/%d/drill-trigger", policy.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}

	var envelope Response
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Code != http.StatusOK {
		t.Fatalf("期望 code=200，实际: %d", envelope.Code)
	}

	data, ok := envelope.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("期望 data 为 map，实际: %T", envelope.Data)
	}
	if runID, ok := data["task_run_id"]; !ok {
		t.Fatal("响应缺少 task_run_id 字段")
	} else {
		// JSON 数字默认解析为 float64
		if runIDFloat, ok := runID.(float64); !ok || runIDFloat != 42 {
			t.Fatalf("task_run_id 应为 42，实际: %v", runID)
		}
	}
	if mock.sawAllowedNodeFilter {
		t.Fatal("admin 触发演练不应限制源节点")
	}
}

func TestDrillTriggerAllowsOperatorOwningPolicyNode(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	node := model.Node{Name: "owned-drill-node", Host: "10.0.0.10", BackupDir: "/backup/owned-drill-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := model.Policy{
		Name:         "operator-owned-drill-policy",
		SourcePath:   "/tmp/src",
		TargetPath:   "/tmp/dst",
		CronSpec:     "@daily",
		DrillEnabled: true,
		DrillCron:    "@every 5m",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("创建策略节点关联失败: %v", err)
	}
	const operatorID = uint(7)
	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: operatorID}).Error; err != nil {
		t.Fatalf("创建节点 owner 失败: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	mock := &mockDrillTriggerer{
		fn: func(policyID uint, allowedSourceNodeIDs []uint) (uint, error) {
			if policyID != policy.ID {
				return 0, fmt.Errorf("策略 ID 不匹配: %d != %d", policyID, policy.ID)
			}
			if len(allowedSourceNodeIDs) != 1 || allowedSourceNodeIDs[0] != node.ID {
				return 0, fmt.Errorf("期望仅允许 owned 源节点 %d，实际: %v", node.ID, allowedSourceNodeIDs)
			}
			return 77, nil
		},
	}
	handler.drillTriggerer = mock

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", "operator")
		c.Set("userID", operatorID)
		c.Next()
	})
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/policies/%d/drill-trigger", policy.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	data, ok := envelope.Data.(map[string]interface{})
	if !ok || data["task_run_id"] != float64(77) {
		t.Fatalf("响应 task_run_id 不符合预期: %#v", envelope.Data)
	}
	if !mock.sawAllowedNodeFilter {
		t.Fatal("operator 触发演练必须传入源节点授权过滤")
	}
}

// TestDrillTriggerRejectsUnownedSharedSource 共享策略：operator 仅拥有部分源节点时，
// 不得用未拥有源节点的备份任务触发演练（通过 allowedSourceNodeIDs 过滤）。
func TestDrillTriggerRejectsUnownedSharedSource(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	owned := model.Node{Name: "shared-owned", Host: "10.0.0.11", BackupDir: "/backup/shared-owned"}
	unowned := model.Node{Name: "shared-unowned", Host: "10.0.0.12", BackupDir: "/backup/shared-unowned"}
	sandbox := model.Node{Name: "shared-sandbox", Host: "10.0.0.13", BackupDir: "/backup/shared-sandbox"}
	for _, n := range []*model.Node{&owned, &unowned, &sandbox} {
		if err := db.Create(n).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	sandboxID := sandbox.ID
	policy := model.Policy{
		Name:              "shared-policy-drill",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &sandboxID,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	for _, link := range []model.PolicyNode{
		{PolicyID: policy.ID, NodeID: owned.ID},
		{PolicyID: policy.ID, NodeID: unowned.ID},
	} {
		if err := db.Create(&link).Error; err != nil {
			t.Fatalf("创建策略节点关联失败: %v", err)
		}
	}
	const operatorID = uint(9)
	if err := db.Create(&model.NodeOwner{NodeID: owned.ID, UserID: operatorID}).Error; err != nil {
		t.Fatalf("创建 owned owner 失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: sandbox.ID, UserID: operatorID}).Error; err != nil {
		t.Fatalf("创建 sandbox owner 失败: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	mock := &mockDrillTriggerer{
		fn: func(policyID uint, allowedSourceNodeIDs []uint) (uint, error) {
			// Must not include unowned node — caller is responsible for filter.
			for _, id := range allowedSourceNodeIDs {
				if id == unowned.ID {
					return 0, fmt.Errorf("allowed set leaked unowned node %d", unowned.ID)
				}
			}
			if len(allowedSourceNodeIDs) == 0 {
				return 0, fmt.Errorf("expected owned nodes in filter")
			}
			return 88, nil
		},
	}
	handler.drillTriggerer = mock

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", "operator")
		c.Set("userID", operatorID)
		c.Next()
	})
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/policies/%d/drill-trigger", policy.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200（仅用 owned 源），实际: %d, body=%s", resp.Code, resp.Body.String())
	}
	// ownershipNodeFilter returns all nodes the operator owns (source + sandbox),
	// not policy-scoped — but must never include the unowned shared source.
	if len(mock.lastAllowedNodeIDs) == 0 {
		t.Fatal("operator 应传入非空 allowedSourceNodeIDs")
	}
	hasOwnedSource := false
	for _, id := range mock.lastAllowedNodeIDs {
		if id == unowned.ID {
			t.Fatalf("allowedSourceNodeIDs 含未拥有节点: %v", mock.lastAllowedNodeIDs)
		}
		if id == owned.ID {
			hasOwnedSource = true
		}
	}
	if !hasOwnedSource {
		t.Fatalf("allowedSourceNodeIDs 应包含 owned 源节点 %d，实际: %v", owned.ID, mock.lastAllowedNodeIDs)
	}
}

// TestDrillTriggerDisabled 验证对未启用 drill 的策略触发返回 400。
func TestDrillTriggerDisabled(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	policy := model.Policy{
		Name:         "drill-disabled-policy",
		SourcePath:   "/tmp/src",
		TargetPath:   "/tmp/dst",
		CronSpec:     "@daily",
		DrillEnabled: false,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/policies/%d/drill-trigger", policy.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际: %d, body=%s", resp.Code, resp.Body.String())
	}

	var envelope Response
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Message == "" {
		t.Fatal("期望错误消息不为空")
	}
}

// TestDrillTriggerNotFound 验证对不存在的策略触发返回 404。
func TestDrillTriggerNotFound(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	handler := NewPolicyHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	req := httptest.NewRequest(http.MethodPost, "/policies/99999/drill-trigger", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际: %d, body=%s", resp.Code, resp.Body.String())
	}
}

// TestDrillTriggerNoTaskManager 验证当 drillTriggerer 不可用时返回 500。
func TestDrillTriggerNoTaskManager(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	policy := model.Policy{
		Name:         "drill-no-tm-policy",
		SourcePath:   "/tmp/src",
		TargetPath:   "/tmp/dst",
		CronSpec:     "@daily",
		DrillEnabled: true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	// 注意：不设置 drillTriggerer 字段（为 nil）
	handler := NewPolicyHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/policies/%d/drill-trigger", policy.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("期望 500，实际: %d, body=%s", resp.Code, resp.Body.String())
	}
}

// TestDrillTriggerForbidden 验证非管理员且非 owner 无权触发演练。
func TestDrillTriggerForbidden(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	policy := model.Policy{
		Name:         "drill-forbidden-policy",
		SourcePath:   "/tmp/src",
		TargetPath:   "/tmp/dst",
		CronSpec:     "@daily",
		DrillEnabled: true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	handler.drillTriggerer = &mockDrillTriggerer{}
	r := gin.New()
	// operator 角色，策略无关联节点（不属于任何 owner）→ 应被拒绝
	r.Use(func(c *gin.Context) {
		c.Set("role", "operator")
		c.Set("userID", uint(1))
		c.Next()
	})
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/policies/%d/drill-trigger", policy.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际: %d, body=%s", resp.Code, resp.Body.String())
	}
}

// TestDrillTriggerBadRequest_WithBody 验证带 body 的 POST 不影响参数解析。
func TestDrillTriggerBadRequest_WithBody(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	policy := model.Policy{
		Name:         "drill-body-policy",
		SourcePath:   "/tmp/src",
		TargetPath:   "/tmp/dst",
		CronSpec:     "@daily",
		DrillEnabled: false,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	handler := NewPolicyHandler(db, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	body := map[string]interface{}{"extra": "ignored"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/policies/%d/drill-trigger", policy.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	// 策略未启用 drill → 应返回 400
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际: %d, body=%s", resp.Code, resp.Body.String())
	}
}

type unavailableDrillTriggerer struct {
	called bool
}

func (m *unavailableDrillTriggerer) TriggerDrill(uint, []uint) (uint, error) {
	m.called = true
	return 0, nil
}

func (*unavailableDrillTriggerer) DrillAvailable() bool {
	return false
}

func TestDrillTriggerChecksGlobalAvailabilityBeforePolicyValidation(t *testing.T) {
	db := openPolicyHandlerTestDB(t)
	policy := model.Policy{
		Name:             "drill-transport-unavailable",
		SourcePath:       "/tmp/src",
		TargetPath:       "/tmp/dst",
		CronSpec:         "@daily",
		DrillEnabled:     false,
		DrillRestorePath: "/etc/should-never-be-validated",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	triggerer := &unavailableDrillTriggerer{}
	handler := NewPolicyHandler(db, nil)
	handler.drillTriggerer = triggerer
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", "admin")
		c.Next()
	})
	r.POST("/policies/:id/drill-trigger", handler.TriggerDrill)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/policies/%d/drill-trigger", policy.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("全局传输不可用时应优先返回 503，实际: %d, body=%s", resp.Code, resp.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Code != http.StatusServiceUnavailable || envelope.Message != "恢复演练功能暂不可用" {
		t.Fatalf("响应未使用安全 503 合约: %+v", envelope)
	}
	if triggerer.called {
		t.Fatal("全局传输不可用时不应调用演练触发器")
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Count(&runCount).Error; err != nil {
		t.Fatalf("统计 TaskRun 失败: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("全局传输不可用时不应创建 TaskRun，实际 %d", runCount)
	}
}
