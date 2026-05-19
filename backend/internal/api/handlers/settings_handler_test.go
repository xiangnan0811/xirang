package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSettingsAnomalySmokeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.AnomalyEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newSettingsAnomalySmokeRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	settingsSvc := settings.NewService(db)
	settingsHandler := NewSettingsHandler(db, settingsSvc)
	anomalyHandler := NewAnomalyHandler(db)
	inject := func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(1))
		c.Set("role", "admin")
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.PUT("/settings", middleware.RequireRole("admin"), settingsHandler.BatchUpdate)
	g.GET("/anomaly-events", middleware.RBAC("nodes:read"), anomalyHandler.List)
	return r
}

func doSettingsAnomalySmoke(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestSettingsSecurityRiskSummaryCountsAdvisorySignals(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")

	db := openSettingsAnomalySmokeDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.NodeOwner{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("migrate security risk tables: %v", err)
	}
	now := time.Now().UTC()
	old := now.Add(-120 * 24 * time.Hour)
	expiredAt := now.Add(-24 * time.Hour)
	key := model.SSHKey{
		Name:            "risk-shared-key",
		Username:        "root",
		KeyType:         "auto",
		PrivateKey:      "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		Fingerprint:     "SHA256:risk-shared-key",
		LastUsedAt:      &old,
		AllowedPurposes: sshutil.PurposeTerminal,
		AllowedNodeIDs:  "1",
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("创建 SSH key 失败: %v", err)
	}
	disabledKey := model.SSHKey{
		Name:            "risk-disabled-key",
		Username:        "ops",
		KeyType:         "auto",
		PrivateKey:      "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		Fingerprint:     "SHA256:risk-disabled-key",
		Disabled:        true,
		AllowedPurposes: sshutil.PurposeTaskCommand,
		AllowedNodeIDs:  "3",
		LastUsedAt:      &old,
	}
	if err := db.Create(&disabledKey).Error; err != nil {
		t.Fatalf("创建禁用 SSH key 失败: %v", err)
	}
	expiredKey := model.SSHKey{
		Name:            "risk-expired-key",
		Username:        "ops",
		KeyType:         "auto",
		PrivateKey:      "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		Fingerprint:     "SHA256:risk-expired-key",
		ExpiresAt:       &expiredAt,
		AllowedPurposes: sshutil.PurposeTaskCommand,
		AllowedNodeIDs:  "4",
	}
	if err := db.Create(&expiredKey).Error; err != nil {
		t.Fatalf("创建过期 SSH key 失败: %v", err)
	}
	broadKey := model.SSHKey{
		Name:        "risk-broad-key",
		Username:    "ops",
		KeyType:     "auto",
		PrivateKey:  "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		Fingerprint: "SHA256:risk-broad-key",
	}
	if err := db.Create(&broadKey).Error; err != nil {
		t.Fatalf("创建宽范围 SSH key 失败: %v", err)
	}
	for i := 1; i <= 2; i++ {
		node := model.Node{
			Name:      fmt.Sprintf("risk-root-%d", i),
			Host:      fmt.Sprintf("10.10.0.%d", i),
			Port:      22,
			Username:  "root",
			AuthType:  "key",
			SSHKeyID:  &key.ID,
			BackupDir: fmt.Sprintf("risk-root-%d", i),
			UseSudo:   i == 1,
		}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	for _, node := range []model.Node{
		{Name: "risk-disabled-node", Host: "10.10.0.3", Port: 22, Username: "ops", AuthType: "key", SSHKeyID: &disabledKey.ID, BackupDir: "risk-disabled-node"},
		{Name: "risk-expired-node", Host: "10.10.0.4", Port: 22, Username: "ops", AuthType: "key", SSHKeyID: &expiredKey.ID, BackupDir: "risk-expired-node"},
	} {
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建引用风险 key 的节点失败: %v", err)
		}
	}
	if err := db.Create(&model.CredentialAuditEvent{
		Action:           "ssh_key.export",
		Purpose:          sshutil.PurposeSSHKeyExport,
		CredentialKind:   "ssh_key",
		CredentialSource: "ssh_key_export",
		Outcome:          "success",
		Metadata:         `{"scope":"all"}`,
		CreatedAt:        now,
	}).Error; err != nil {
		t.Fatalf("创建凭据审计事件失败: %v", err)
	}

	handler := NewSettingsHandler(db, settings.NewService(db))
	r := gin.New()
	r.GET("/settings/security-risk-summary", handler.SecurityRiskSummary)
	resp := doSettingsAnomalySmoke(r, http.MethodGet, "/settings/security-risk-summary", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("security risk summary status=%d body=%s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Data securityRiskSummaryResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析安全风险摘要失败: %v", err)
	}
	byCode := make(map[string]securityRiskItem, len(envelope.Data.Items))
	for _, item := range envelope.Data.Items {
		byCode[item.Code] = item
	}
	if byCode["root_ssh_users"].Count != 2 {
		t.Fatalf("root SSH 风险数量应为 2，实际: %+v", byCode["root_ssh_users"])
	}
	if byCode["reused_ssh_keys"].Count != 1 || !strings.Contains(strings.Join(byCode["reused_ssh_keys"].Examples, ","), "risk-shared-key") {
		t.Fatalf("SSH key 复用风险不符合预期: %+v", byCode["reused_ssh_keys"])
	}
	if byCode["sudo_enabled_nodes"].Count != 1 {
		t.Fatalf("sudo 风险数量应为 1，实际: %+v", byCode["sudo_enabled_nodes"])
	}
	if byCode["broad_scope_ssh_keys"].Count != 1 || !strings.Contains(strings.Join(byCode["broad_scope_ssh_keys"].Examples, ","), "risk-broad-key") {
		t.Fatalf("宽范围 SSH key 风险不符合预期: %+v", byCode["broad_scope_ssh_keys"])
	}
	if byCode["disabled_ssh_keys_in_use"].Count != 1 || !strings.Contains(strings.Join(byCode["disabled_ssh_keys_in_use"].Examples, ","), "risk-disabled-key") {
		t.Fatalf("禁用 key 引用风险不符合预期: %+v", byCode["disabled_ssh_keys_in_use"])
	}
	if byCode["expired_ssh_keys_in_use"].Count != 1 || !strings.Contains(strings.Join(byCode["expired_ssh_keys_in_use"].Examples, ","), "risk-expired-key") {
		t.Fatalf("过期 key 引用风险不符合预期: %+v", byCode["expired_ssh_keys_in_use"])
	}
	if byCode["stale_ssh_keys"].Count != 2 {
		t.Fatalf("长期未使用 key 风险数量应为 2，实际: %+v", byCode["stale_ssh_keys"])
	}
	if byCode["recent_credential_operations"].Count != 1 || !strings.Contains(strings.Join(byCode["recent_credential_operations"].Examples, ","), "SSH Key 导出") {
		t.Fatalf("近期凭据操作风险不符合预期: %+v", byCode["recent_credential_operations"])
	}
	if byCode["weak_security_defaults"].Count == 0 {
		t.Fatalf("弱安全默认项应至少包含测试环境设置，实际: %+v", byCode["weak_security_defaults"])
	}
	body := resp.Body.String()
	for _, forbidden := range []string{
		"PrivateKey",
		"FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		"FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		"FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		"FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		"10.10.0.",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("安全风险摘要不应暴露敏感字段 %q，实际: %s", forbidden, body)
		}
	}
}

func TestSettingsUpdateAnomalyEnabledKeepsAnomalyEventsEndpointAvailable(t *testing.T) {
	db := openSettingsAnomalySmokeDB(t)
	r := newSettingsAnomalySmokeRouter(t, db)

	w := doSettingsAnomalySmoke(r, "PUT", "/api/v1/settings", `{"anomaly.enabled":"true"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("settings update status=%d body=%s", w.Code, w.Body.String())
	}

	w = doSettingsAnomalySmoke(r, "GET", "/api/v1/anomaly-events", "")
	if w.Code != http.StatusOK {
		t.Fatalf("anomaly events status=%d body=%s", w.Code, w.Body.String())
	}
}
