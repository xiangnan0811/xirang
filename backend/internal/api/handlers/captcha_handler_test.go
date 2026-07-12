package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openCaptchaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatalf("初始化 system_settings 失败: %v", err)
	}
	return db
}

func TestGenerateCaptchaDisabledReturnsNoChallenge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openCaptchaTestDB(t)
	store := NewCaptchaStore()
	svc := settings.NewService(db)
	// Defaults: login.captcha_enabled=false, second=false
	handler := NewCaptchaHandler(store).WithSettingsService(svc)

	r := gin.New()
	r.GET("/auth/captcha", handler.GenerateCaptcha)

	req := httptest.NewRequest(http.MethodGet, "/auth/captcha", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Data["enabled"] != false {
		t.Fatalf("主验证码关闭时期望 enabled=false: %+v", envelope.Data)
	}
	if envelope.Data["id"] != nil || envelope.Data["question"] != nil {
		t.Fatalf("主验证码关闭时不应返回 id/question: %+v", envelope.Data)
	}
	if envelope.Data["second_required"] != false {
		t.Fatalf("二次关闭时期望 second_required=false: %+v", envelope.Data)
	}
	if envelope.Data["second_id"] != nil || envelope.Data["second_question"] != nil {
		t.Fatalf("二次关闭时不应返回 second_*: %+v", envelope.Data)
	}
}

func TestGenerateCaptchaPrimaryOnlyWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openCaptchaTestDB(t)
	store := NewCaptchaStore()
	svc := settings.NewService(db)
	if err := svc.Update("login.captcha_enabled", "true"); err != nil {
		t.Fatalf("启用主验证码失败: %v", err)
	}
	handler := NewCaptchaHandler(store).WithSettingsService(svc)

	r := gin.New()
	r.GET("/auth/captcha", handler.GenerateCaptcha)

	req := httptest.NewRequest(http.MethodGet, "/auth/captcha", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Data["enabled"] != true {
		t.Fatalf("期望 enabled=true: %+v", envelope.Data)
	}
	if envelope.Data["id"] == nil || envelope.Data["question"] == nil {
		t.Fatalf("缺少主验证码字段: %+v", envelope.Data)
	}
	if envelope.Data["second_required"] != false {
		t.Fatalf("未开启二次时不应 second_required: %+v", envelope.Data)
	}
	if envelope.Data["second_id"] != nil || envelope.Data["second_question"] != nil {
		t.Fatalf("未开启二次验证码时不应返回 second_* 字段: %+v", envelope.Data)
	}
}

func TestGenerateCaptchaSecondOnlyDoesNotEmitPrimary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openCaptchaTestDB(t)
	store := NewCaptchaStore()
	svc := settings.NewService(db)
	if err := svc.Update("login.second_captcha_enabled", "true"); err != nil {
		t.Fatalf("启用二次验证码失败: %v", err)
	}
	// primary remains false
	handler := NewCaptchaHandler(store).WithSettingsService(svc)

	r := gin.New()
	r.GET("/auth/captcha", handler.GenerateCaptcha)

	req := httptest.NewRequest(http.MethodGet, "/auth/captcha", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Data["enabled"] != false {
		t.Fatalf("主验证码关闭时期望 enabled=false: %+v", envelope.Data)
	}
	if envelope.Data["id"] != nil || envelope.Data["question"] != nil {
		t.Fatalf("仅二次开启时不应返回主 id/question: %+v", envelope.Data)
	}
	if envelope.Data["second_required"] != true {
		t.Fatalf("期望 second_required=true: %+v", envelope.Data)
	}
	secondID, _ := envelope.Data["second_id"].(string)
	if secondID == "" || envelope.Data["second_question"] == nil {
		t.Fatalf("缺少二次验证码字段: %+v", envelope.Data)
	}
}

func TestGenerateCaptchaDualWhenBothEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openCaptchaTestDB(t)
	store := NewCaptchaStore()
	svc := settings.NewService(db)
	if err := svc.Update("login.captcha_enabled", "true"); err != nil {
		t.Fatalf("启用主验证码失败: %v", err)
	}
	if err := svc.Update("login.second_captcha_enabled", "true"); err != nil {
		t.Fatalf("启用二次验证码失败: %v", err)
	}
	handler := NewCaptchaHandler(store).WithSettingsService(svc)

	r := gin.New()
	r.GET("/auth/captcha", handler.GenerateCaptcha)

	req := httptest.NewRequest(http.MethodGet, "/auth/captcha", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	primaryID, _ := envelope.Data["id"].(string)
	secondID, _ := envelope.Data["second_id"].(string)
	if primaryID == "" || secondID == "" {
		t.Fatalf("双验证码应返回 id 与 second_id: %+v", envelope.Data)
	}
	if primaryID == secondID {
		t.Fatalf("主/二次验证码 id 不得相同")
	}
	if envelope.Data["enabled"] != true || envelope.Data["second_required"] != true {
		t.Fatalf("期望 enabled 与 second_required 均为 true: %+v", envelope.Data)
	}

	// Shared store must hold two independent one-shot challenges.
	if store.Verify(primaryID, -1) {
		t.Fatal("错误答案不应通过主挑战")
	}
	if store.Verify(primaryID, 0) {
		t.Fatal("主挑战应已一次性消费")
	}
	if store.Verify(secondID, -1) {
		t.Fatal("错误答案不应通过二次挑战")
	}
	if store.Verify(secondID, 0) {
		t.Fatal("二次挑战应已一次性消费")
	}
}
