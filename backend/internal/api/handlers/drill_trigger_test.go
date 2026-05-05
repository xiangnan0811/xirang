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
	fn func(policyID uint) (uint, error)
}

func (m *mockDrillTriggerer) TriggerDrill(policyID uint) (uint, error) {
	if m.fn != nil {
		return m.fn(policyID)
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
	handler.drillTriggerer = &mockDrillTriggerer{
		fn: func(policyID uint) (uint, error) {
			if policyID != policy.ID {
				return 0, fmt.Errorf("策略 ID 不匹配: %d != %d", policyID, policy.ID)
			}
			return 42, nil
		},
	}

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
	if envelope.Code != 0 {
		t.Fatalf("期望 code=0，实际: %d", envelope.Code)
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
