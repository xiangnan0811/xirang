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
	t.Setenv("JWT_SECRET", "change-me-in-production")
	t.Setenv("DATA_ENCRYPTION_KEY", "change-me-encryption-key")
	t.Setenv("ADMIN_INITIAL_PASSWORD", "change-me-admin-password")
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")

	db := openSettingsAnomalySmokeDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SSHKey{}, &model.NodeOwner{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate security risk tables: %v", err)
	}
	now := time.Now().UTC()
	old := now.Add(-120 * 24 * time.Hour)
	expiredAt := now.Add(-24 * time.Hour)
	for _, user := range []model.User{
		{Username: "risk-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TOTPEnabled: false, TOTPSecret: "FAKE_TOTP_SECRET_FOR_TEST_ONLY", RecoveryCodes: "FAKE_RECOVERY_CODE_FOR_TEST_ONLY"},
		{Username: "risk-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator", TOTPEnabled: false},
		{Username: "risk-viewer", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "viewer", TOTPEnabled: false},
		{Username: "risk-ready-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TOTPEnabled: true},
	} {
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}
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
	createAudit := func(action, purpose, metadata, errorMessage string) {
		t.Helper()
		if err := db.Create(&model.CredentialAuditEvent{
			Action:           action,
			Purpose:          purpose,
			CredentialKind:   "ssh_key",
			CredentialSource: "ssh_key_export",
			Outcome:          "success",
			Metadata:         metadata,
			ErrorMessage:     errorMessage,
			CreatedAt:        now,
		}).Error; err != nil {
			t.Fatalf("创建凭据审计事件失败: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		createAudit("ssh_key.export", sshutil.PurposeSSHKeyExport, `{"scope":"all"}`, "")
	}
	for i := 0; i < 2; i++ {
		createAudit("file_browser.preview", sshutil.PurposeFileBrowser, `{"path":"/safe/FAKE_FILE_NAME_FOR_TEST_ONLY","content":"FAKE_FILE_CONTENT_FOR_TEST_ONLY"}`, "preview failed: output: FAKE_SFTP_OUTPUT_FOR_TEST_ONLY")
	}
	createAudit("docker_volumes.discover", sshutil.PurposeDockerVolumes, `{"volume":"volume-prod-data","output":"FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY"}`, "docker failed: output: FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY")
	for _, row := range []model.AuditLog{
		{Username: "raw-audit-admin", Role: "admin", Method: http.MethodPost, Path: "/api/v1/secret/path", StatusCode: 200, ClientIP: "198.51.100.10", UserAgent: "raw-audit-agent", EntryHash: "hash-a", CreatedAt: now},
		{Username: "raw-audit-operator", Role: "operator", Method: http.MethodPut, Path: "/api/v1/another/secret", StatusCode: 500, ClientIP: "198.51.100.11", UserAgent: "raw-audit-agent", PrevHash: "wrong-hash", EntryHash: "hash-b", CreatedAt: now},
		{Username: "raw-audit-viewer", Role: "viewer", Method: http.MethodDelete, Path: "/api/v1/delete/secret", StatusCode: 204, ClientIP: "198.51.100.12", UserAgent: "raw-audit-agent", CreatedAt: now},
		{Username: "raw-audit-maintainer", Role: "admin", Method: http.MethodPatch, Path: "/api/v1/maintain/secret", StatusCode: 202, ClientIP: "198.51.100.13", UserAgent: "raw-audit-agent", PrevHash: "hash-c", EntryHash: "hash-d", CreatedAt: now},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("创建审计日志失败: %v", err)
		}
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
	recentExamples := strings.Join(byCode["recent_credential_operations"].Examples, ",")
	if byCode["recent_credential_operations"].Count != 6 || !strings.Contains(recentExamples, "SSH Key 导出") || !strings.Contains(recentExamples, "节点文件预览") || !strings.Contains(recentExamples, "Docker 卷发现") {
		t.Fatalf("近期凭据操作风险不符合预期: %+v", byCode["recent_credential_operations"])
	}
	privilegedExamples := strings.Join(byCode["privileged_users_without_totp"].Examples, ",")
	if byCode["privileged_users_without_totp"].Count != 2 || !strings.Contains(privilegedExamples, "risk-admin") || !strings.Contains(privilegedExamples, "risk-operator") || strings.Contains(privilegedExamples, "risk-viewer") || strings.Contains(privilegedExamples, "risk-ready-admin") {
		t.Fatalf("高权限用户强认证风险不符合预期: %+v", byCode["privileged_users_without_totp"])
	}
	auditIntegrityExamples := strings.Join(byCode["audit_log_integrity_posture"].Examples, ",")
	if byCode["audit_log_integrity_posture"].Severity != "critical" || byCode["audit_log_integrity_posture"].Count != 4 || !strings.Contains(auditIntegrityExamples, "审计日志存在缺失的完整性哈希") || !strings.Contains(auditIntegrityExamples, "审计日志存在缺失的前序哈希") || !strings.Contains(auditIntegrityExamples, "审计日志哈希链存在断点") {
		t.Fatalf("审计日志完整性姿态风险不符合预期: %+v", byCode["audit_log_integrity_posture"])
	}
	for _, forbidden := range []string{"raw-audit-admin", "raw-audit-operator", "raw-audit-viewer", "raw-audit-maintainer", "198.51.100.", "/api/v1/", "raw-audit-agent"} {
		if strings.Contains(auditIntegrityExamples, forbidden) {
			t.Fatalf("审计日志完整性姿态不应暴露原始审计字段 %q: %+v", forbidden, byCode["audit_log_integrity_posture"])
		}
	}
	hostKeyExamples := strings.Join(byCode["ssh_host_key_trust_posture"].Examples, ",")
	if byCode["ssh_host_key_trust_posture"].Count != 1 || !strings.Contains(hostKeyExamples, "SSH 主机密钥校验已关闭") {
		t.Fatalf("SSH 主机密钥信任姿态风险不符合预期: %+v", byCode["ssh_host_key_trust_posture"])
	}
	if strings.Contains(hostKeyExamples, "10.10.0.") || strings.Contains(hostKeyExamples, "/") || strings.Contains(hostKeyExamples, "SHA256:") {
		t.Fatalf("SSH 主机密钥信任姿态不应暴露主机、路径或指纹: %+v", byCode["ssh_host_key_trust_posture"])
	}
	deploymentSecretExamples := strings.Join(byCode["deployment_secret_posture"].Examples, ",")
	if byCode["deployment_secret_posture"].Count != 4 || len(byCode["deployment_secret_posture"].Examples) != maxSecurityRiskExamples || !strings.Contains(deploymentSecretExamples, "运行环境仍处于开发模式") || !strings.Contains(deploymentSecretExamples, "JWT 签名密钥缺失或强度不足") || !strings.Contains(deploymentSecretExamples, "数据加密密钥缺失或强度不足") {
		t.Fatalf("部署密钥姿态风险不符合预期: %+v", byCode["deployment_secret_posture"])
	}
	for _, forbidden := range []string{"change-me-in-production", "change-me-encryption-key", "change-me-admin-password", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY"} {
		if strings.Contains(deploymentSecretExamples, forbidden) {
			t.Fatalf("部署密钥姿态不应暴露原始环境值 %q: %+v", forbidden, byCode["deployment_secret_posture"])
		}
	}
	if strings.Contains(strings.Join(byCode["weak_security_defaults"].Examples, ","), "SSH 主机密钥") {
		t.Fatalf("弱安全默认项不应重复 SSH 主机密钥姿态: %+v", byCode["weak_security_defaults"])
	}
	body := resp.Body.String()
	for _, forbidden := range []string{
		"PrivateKey",
		"FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
		"FAKE_TOTP_SECRET_FOR_TEST_ONLY",
		"FAKE_RECOVERY_CODE_FOR_TEST_ONLY",
		"FAKE_FILE_NAME_FOR_TEST_ONLY",
		"FAKE_FILE_CONTENT_FOR_TEST_ONLY",
		"FAKE_SFTP_OUTPUT_FOR_TEST_ONLY",
		"FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY",
		"volume-prod-data",
		"10.10.0.",
		"raw-audit-admin",
		"raw-audit-operator",
		"raw-audit-viewer",
		"raw-audit-maintainer",
		"198.51.100.",
		"/api/v1/secret",
		"/api/v1/maintain",
		"raw-audit-agent",
		"change-me-in-production",
		"change-me-encryption-key",
		"change-me-admin-password",
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
