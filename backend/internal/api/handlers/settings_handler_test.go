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
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SSHKey{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate security risk tables: %v", err)
	}
	now := time.Now().UTC()
	old := now.Add(-120 * 24 * time.Hour)
	expiredAt := now.Add(-24 * time.Hour)
	for _, user := range []model.User{
		{Username: "risk-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TOTPEnabled: false, TOTPSecret: "FAKE_TOTP_SECRET_FOR_TEST_ONLY", RecoveryCodes: "FAKE_RECOVERY_CODE_FOR_TEST_ONLY"},
		{Username: "risk-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator", TOTPEnabled: false},
		{Username: "risk-viewer", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "viewer", TOTPEnabled: false},
		{Username: "risk-ready-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TOTPEnabled: true, TOTPSecret: "FAKE_READY_TOTP_SECRET_FOR_TEST_ONLY"},
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
		createAudit("file_browser.preview", sshutil.PurposeFileBrowser, `{"path":"/safe/FAKE_FILE_NAME_FOR_TEST_ONLY","content":"FAKE_FILE_CONTENT_FOR_TEST_ONLY","login_token":"FAKE_LOGIN_TOKEN_FOR_TEST_ONLY","step_up_proof":"FAKE_STEP_UP_PROOF_FOR_TEST_ONLY","token_version":"FAKE_TOKEN_VERSION_FOR_TEST_ONLY"}`, "preview failed: raw error FAKE_RAW_ERROR_FOR_TEST_ONLY output: FAKE_SFTP_OUTPUT_FOR_TEST_ONLY")
	}
	createAudit("docker_volumes.discover", sshutil.PurposeDockerVolumes, `{"volume":"volume-prod-data","output":"FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY"}`, "docker failed: output: FAKE_DOCKER_OUTPUT_FOR_TEST_ONLY")
	backupPolicy := model.Policy{Name: "FAKE_BACKUP_POLICY_NAME_FOR_TEST_ONLY", SourcePath: "/srv/FAKE_SOURCE_PATH_FOR_TEST_ONLY", TargetPath: "/backup/FAKE_TARGET_PATH_FOR_TEST_ONLY", CronSpec: "0 2 * * *", Enabled: true, VerifyEnabled: true, RPOMinutes: 60, DrillEnabled: true}
	if err := db.Create(&backupPolicy).Error; err != nil {
		t.Fatalf("创建备份策略失败: %v", err)
	}
	backupNode := model.Node{Name: "FAKE_BACKUP_NODE_NAME_FOR_TEST_ONLY", Host: "backup.internal.example", Port: 22, Username: "ops", AuthType: "key", BackupDir: "FAKE_BACKUP_DIR_FOR_TEST_ONLY"}
	if err := db.Create(&backupNode).Error; err != nil {
		t.Fatalf("创建备份节点失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: backupPolicy.ID, NodeID: backupNode.ID}).Error; err != nil {
		t.Fatalf("创建策略节点关联失败: %v", err)
	}
	backupTask := model.Task{Name: "FAKE_BACKUP_TASK_NAME_FOR_TEST_ONLY", NodeID: backupNode.ID, PolicyID: &backupPolicy.ID, ExecutorType: "rsync", Status: "failed", Source: "policy", VerifyStatus: "failed", ExecutorConfig: `{"password":"FAKE_EXECUTOR_PASSWORD_FOR_TEST_ONLY","path":"/srv/FAKE_EXECUTOR_PATH_FOR_TEST_ONLY"}`}
	if err := db.Create(&backupTask).Error; err != nil {
		t.Fatalf("创建备份任务失败: %v", err)
	}
	failedRun := model.TaskRun{TaskID: backupTask.ID, TriggerType: "manual", Status: "failed", VerifyStatus: "failed", LastError: "backup failed on backup.internal.example path /srv/FAKE_SOURCE_PATH_FOR_TEST_ONLY output FAKE_BACKUP_OUTPUT_FOR_TEST_ONLY", CreatedAt: now, StartedAt: &now, FinishedAt: &now}
	if err := db.Create(&failedRun).Error; err != nil {
		t.Fatalf("创建失败备份执行失败: %v", err)
	}
	failedDrill := model.RestoreDrillEvidence{PolicyID: backupPolicy.ID, TaskID: backupTask.ID, TaskRunID: failedRun.ID, SnapshotRef: "FAKE_SNAPSHOT_REF_FOR_TEST_ONLY", SandboxNodeID: backupNode.ID, SandboxNodeName: "FAKE_SANDBOX_NODE_NAME_FOR_TEST_ONLY", SandboxPath: "/tmp/FAKE_SANDBOX_PATH_FOR_TEST_ONLY", Status: "failed", FailedStep: "verify", ConfidenceEligible: false, RestoreStatus: "failed", RestoreError: "restore failed path /tmp/FAKE_SANDBOX_PATH_FOR_TEST_ONLY output FAKE_RESTORE_OUTPUT_FOR_TEST_ONLY", VerifyStatus: "failed", VerifyError: "verify failed host backup.internal.example", CreatedAt: now, StartedAt: &now, FinishedAt: &now}
	if err := db.Create(&failedDrill).Error; err != nil {
		t.Fatalf("创建恢复演练证据失败: %v", err)
	}
	if err := db.Create(&model.Alert{NodeID: backupNode.ID, NodeName: "FAKE_BACKUP_NODE_NAME_FOR_TEST_ONLY", TaskID: &backupTask.ID, TaskRunID: &failedRun.ID, PolicyName: "FAKE_BACKUP_POLICY_NAME_FOR_TEST_ONLY", Severity: "critical", Status: "firing", ErrorCode: fmt.Sprintf("XR-INTG-%d", backupPolicy.ID), Message: "integrity failed at /backup/FAKE_TARGET_PATH_FOR_TEST_ONLY output FAKE_INTEGRITY_OUTPUT_FOR_TEST_ONLY", TriggeredAt: now, CreatedAt: now}).Error; err != nil {
		t.Fatalf("创建完整性告警失败: %v", err)
	}

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
	adminRecoveryExamples := strings.Join(byCode["admin_recovery_posture"].Examples, ",")
	if byCode["admin_recovery_posture"].Severity != "warning" || byCode["admin_recovery_posture"].Count != 1 || !strings.Contains(adminRecoveryExamples, "存在启用两步验证但缺少恢复码证据的管理员") {
		t.Fatalf("管理员恢复姿态风险不符合预期: %+v", byCode["admin_recovery_posture"])
	}
	for _, forbidden := range []string{"risk-admin", "risk-operator", "risk-ready-admin", "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", "FAKE_TOTP_SECRET_FOR_TEST_ONLY", "FAKE_READY_TOTP_SECRET_FOR_TEST_ONLY", "FAKE_RECOVERY_CODE_FOR_TEST_ONLY"} {
		if strings.Contains(adminRecoveryExamples, forbidden) {
			t.Fatalf("管理员恢复姿态不应暴露原始用户或恢复字段 %q: %+v", forbidden, byCode["admin_recovery_posture"])
		}
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
	backupRestoreExamples := strings.Join(byCode["backup_restore_posture"].Examples, ",")
	if byCode["backup_restore_posture"].Severity != "critical" || byCode["backup_restore_posture"].Count < 5 || len(byCode["backup_restore_posture"].Examples) != maxSecurityRiskExamples || !strings.Contains(backupRestoreExamples, "存在启用策略缺少成功备份证据") || !strings.Contains(backupRestoreExamples, "存在最近备份失败的策略") || !strings.Contains(backupRestoreExamples, "存在备份校验失败证据") {
		t.Fatalf("备份恢复姿态风险不符合预期: %+v", byCode["backup_restore_posture"])
	}
	for _, forbidden := range []string{"FAKE_BACKUP_POLICY_NAME_FOR_TEST_ONLY", "FAKE_BACKUP_NODE_NAME_FOR_TEST_ONLY", "backup.internal.example", "FAKE_SOURCE_PATH_FOR_TEST_ONLY", "FAKE_TARGET_PATH_FOR_TEST_ONLY", "FAKE_BACKUP_TASK_NAME_FOR_TEST_ONLY", "FAKE_EXECUTOR_PASSWORD_FOR_TEST_ONLY", "FAKE_EXECUTOR_PATH_FOR_TEST_ONLY", "FAKE_BACKUP_OUTPUT_FOR_TEST_ONLY", "FAKE_SNAPSHOT_REF_FOR_TEST_ONLY", "FAKE_SANDBOX_NODE_NAME_FOR_TEST_ONLY", "FAKE_SANDBOX_PATH_FOR_TEST_ONLY", "FAKE_RESTORE_OUTPUT_FOR_TEST_ONLY", "FAKE_INTEGRITY_OUTPUT_FOR_TEST_ONLY"} {
		if strings.Contains(backupRestoreExamples, forbidden) {
			t.Fatalf("备份恢复姿态不应暴露原始证据字段 %q: %+v", forbidden, byCode["backup_restore_posture"])
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
		"FAKE_READY_TOTP_SECRET_FOR_TEST_ONLY",
		"FAKE_RECOVERY_CODE_FOR_TEST_ONLY",
		"FAKE_FILE_NAME_FOR_TEST_ONLY",
		"FAKE_FILE_CONTENT_FOR_TEST_ONLY",
		"FAKE_LOGIN_TOKEN_FOR_TEST_ONLY",
		"FAKE_STEP_UP_PROOF_FOR_TEST_ONLY",
		"FAKE_TOKEN_VERSION_FOR_TEST_ONLY",
		"FAKE_RAW_ERROR_FOR_TEST_ONLY",
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
		"FAKE_BACKUP_POLICY_NAME_FOR_TEST_ONLY",
		"FAKE_BACKUP_NODE_NAME_FOR_TEST_ONLY",
		"backup.internal.example",
		"FAKE_SOURCE_PATH_FOR_TEST_ONLY",
		"FAKE_TARGET_PATH_FOR_TEST_ONLY",
		"FAKE_BACKUP_TASK_NAME_FOR_TEST_ONLY",
		"FAKE_EXECUTOR_PASSWORD_FOR_TEST_ONLY",
		"FAKE_EXECUTOR_PATH_FOR_TEST_ONLY",
		"FAKE_BACKUP_OUTPUT_FOR_TEST_ONLY",
		"FAKE_SNAPSHOT_REF_FOR_TEST_ONLY",
		"FAKE_SANDBOX_NODE_NAME_FOR_TEST_ONLY",
		"FAKE_SANDBOX_PATH_FOR_TEST_ONLY",
		"FAKE_RESTORE_OUTPUT_FOR_TEST_ONLY",
		"FAKE_INTEGRITY_OUTPUT_FOR_TEST_ONLY",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("安全风险摘要不应暴露敏感字段 %q，实际: %s", forbidden, body)
		}
	}
}

func TestSettingsSecurityRiskSummaryAdminRecoveryPostureCriticalWhenNoAdmin(t *testing.T) {
	db := openSettingsAnomalySmokeDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SSHKey{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate admin recovery posture tables: %v", err)
	}
	if err := db.Create(&model.User{Username: "FAKE_OPERATOR_USERNAME_FOR_TEST_ONLY", PasswordHash: "FAKE_OPERATOR_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator", TOTPEnabled: true, TOTPSecret: "FAKE_OPERATOR_TOTP_SECRET_FOR_TEST_ONLY", RecoveryCodes: "FAKE_OPERATOR_RECOVERY_CODE_FOR_TEST_ONLY"}).Error; err != nil {
		t.Fatalf("创建无管理员场景用户失败: %v", err)
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
	item := byCode["admin_recovery_posture"]
	examples := strings.Join(item.Examples, ",")
	if item.Severity != "critical" || item.Count != 1 || !strings.Contains(examples, "未发现管理员账号") {
		t.Fatalf("无管理员账号时应返回 critical 汇总: %+v", item)
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"FAKE_OPERATOR_USERNAME_FOR_TEST_ONLY", "FAKE_OPERATOR_PASSWORD_HASH_FOR_TEST_ONLY", "FAKE_OPERATOR_TOTP_SECRET_FOR_TEST_ONLY", "FAKE_OPERATOR_RECOVERY_CODE_FOR_TEST_ONLY"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("管理员恢复姿态不应暴露无管理员场景原始字段 %q，实际: %s", forbidden, body)
		}
	}
}

func TestSettingsSecurityRiskSummaryAdminRecoveryPostureInfoWhenHealthy(t *testing.T) {
	db := openSettingsAnomalySmokeDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SSHKey{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate admin recovery posture tables: %v", err)
	}
	for _, user := range []model.User{
		{Username: "healthy-admin-a", PasswordHash: "FAKE_HEALTHY_ADMIN_HASH_FOR_TEST_ONLY", Role: "admin", TOTPEnabled: true, TOTPSecret: "FAKE_HEALTHY_ADMIN_TOTP_FOR_TEST_ONLY", RecoveryCodes: `["FAKE_HEALTHY_RECOVERY_CODE_FOR_TEST_ONLY"]`},
		{Username: "healthy-admin-b", PasswordHash: "FAKE_HEALTHY_ADMIN_HASH_FOR_TEST_ONLY", Role: "admin", TOTPEnabled: true, TOTPSecret: "FAKE_HEALTHY_ADMIN_TOTP_FOR_TEST_ONLY", RecoveryCodes: `["FAKE_HEALTHY_RECOVERY_CODE_FOR_TEST_ONLY"]`},
		{Username: "healthy-operator", PasswordHash: "FAKE_HEALTHY_OPERATOR_HASH_FOR_TEST_ONLY", Role: "operator", TOTPEnabled: true, TOTPSecret: "FAKE_HEALTHY_OPERATOR_TOTP_FOR_TEST_ONLY", RecoveryCodes: `["FAKE_HEALTHY_OPERATOR_RECOVERY_CODE_FOR_TEST_ONLY"]`},
	} {
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建健康管理员恢复姿态用户失败: %v", err)
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
	item := byCode["admin_recovery_posture"]
	if item.Severity != "info" || item.Count != 0 || len(item.Examples) != 0 {
		t.Fatalf("健康管理员恢复姿态应为 info 且无风险: %+v", item)
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"healthy-admin-a", "healthy-admin-b", "healthy-operator", "FAKE_HEALTHY_ADMIN_HASH_FOR_TEST_ONLY", "FAKE_HEALTHY_ADMIN_TOTP_FOR_TEST_ONLY", "FAKE_HEALTHY_RECOVERY_CODE_FOR_TEST_ONLY", "FAKE_HEALTHY_OPERATOR_HASH_FOR_TEST_ONLY", "FAKE_HEALTHY_OPERATOR_TOTP_FOR_TEST_ONLY", "FAKE_HEALTHY_OPERATOR_RECOVERY_CODE_FOR_TEST_ONLY"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("健康管理员恢复姿态不应暴露原始字段 %q，实际: %s", forbidden, body)
		}
	}
}

func TestSettingsSecurityRiskSummaryWeakSecurityDefaultsReportsLocalHardeningSignals(t *testing.T) {
	t.Setenv("WS_ALLOW_EMPTY_ORIGIN", "false")
	t.Setenv("LOGIN_CAPTCHA_ENABLED", "true")
	t.Setenv("LOGIN_SECOND_CAPTCHA_ENABLED", "true")
	t.Setenv("METRICS_TOKEN", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "*")
	t.Setenv("JWT_TTL", "72h")

	item := NewSettingsHandler(nil, nil).weakSecurityDefaultsRiskItem()
	examples := strings.Join(item.Examples, ",")
	if item.Severity != "warning" || item.Count != 3 || !strings.Contains(examples, "Metrics 抓取端点未配置 token 保护") || !strings.Contains(examples, "CORS 允许来源包含通配符") || !strings.Contains(examples, "JWT 会话有效期偏长") {
		t.Fatalf("弱安全默认项本地硬化信号不符合预期: %+v", item)
	}
	for _, forbidden := range []string{"*", "72h", "FAKE_METRICS_TOKEN_FOR_TEST_ONLY", "https://admin.example.com"} {
		if strings.Contains(examples, forbidden) {
			t.Fatalf("弱安全默认项不应暴露原始配置值 %q: %+v", forbidden, item)
		}
	}
}

func TestSettingsSecurityRiskSummaryWeakSecurityDefaultsInfoWhenHardened(t *testing.T) {
	t.Setenv("WS_ALLOW_EMPTY_ORIGIN", "false")
	t.Setenv("LOGIN_CAPTCHA_ENABLED", "true")
	t.Setenv("LOGIN_SECOND_CAPTCHA_ENABLED", "true")
	t.Setenv("METRICS_TOKEN", "FAKE_METRICS_TOKEN_FOR_TEST_ONLY")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://admin.example.com")
	t.Setenv("JWT_TTL", "8h")

	item := NewSettingsHandler(nil, nil).weakSecurityDefaultsRiskItem()
	if item.Severity != "info" || item.Count != 0 || len(item.Examples) != 0 {
		t.Fatalf("健康弱安全默认项应为 info 且无风险: %+v", item)
	}
}

func TestSettingsSecurityRiskSummaryBackupRestorePostureWarningWhenNoPolicies(t *testing.T) {
	db := openSettingsAnomalySmokeDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SSHKey{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate backup restore posture tables: %v", err)
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
	item := byCode["backup_restore_posture"]
	if item.Severity != "warning" || item.Count != 1 || len(item.Examples) != 1 || item.Examples[0] != "尚未配置启用中的备份策略" {
		t.Fatalf("无启用备份策略时应返回 warning 汇总: %+v", item)
	}
}

func TestSettingsSecurityRiskSummaryBackupRestorePostureAvoidsDuplicateMissingSuccessCount(t *testing.T) {
	db := openSettingsAnomalySmokeDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SSHKey{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate backup restore posture tables: %v", err)
	}
	policy := model.Policy{Name: "missing-success-policy", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 2 * * *", Enabled: true, DrillEnabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建无执行证据策略失败: %v", err)
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
	item := byCode["backup_restore_posture"]
	if item.Count != 3 {
		t.Fatalf("无执行记录策略应只计一次缺少成功备份证据，实际: %+v", item)
	}
	examples := strings.Join(item.Examples, ",")
	if strings.Count(examples, "存在启用策略缺少成功备份证据") != 1 {
		t.Fatalf("缺少成功备份证据示例不应重复: %+v", item)
	}
}

func TestSettingsSecurityRiskSummaryBackupRestorePostureInfoWhenHealthy(t *testing.T) {
	db := openSettingsAnomalySmokeDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SSHKey{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}, &model.SystemSetting{}, &model.CredentialAuditEvent{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate backup restore posture tables: %v", err)
	}
	now := time.Now().UTC()
	node := model.Node{Name: "healthy-backup-node", Host: "healthy.example", Port: 22, Username: "ops", AuthType: "key", BackupDir: "healthy-backup-node", LastBackupAt: &now}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建健康备份节点失败: %v", err)
	}
	policy := model.Policy{Name: "healthy-backup-policy", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 2 * * *", Enabled: true, VerifyEnabled: true, RPOMinutes: 1440, DrillEnabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建健康备份策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("创建健康策略节点关联失败: %v", err)
	}
	task := model.Task{Name: "healthy-backup-task", NodeID: node.ID, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "success", Source: "policy", VerifyStatus: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建健康备份任务失败: %v", err)
	}
	run := model.TaskRun{TaskID: task.ID, TriggerType: "manual", Status: "success", VerifyStatus: "success", CreatedAt: now, StartedAt: &now, FinishedAt: &now}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建健康备份执行失败: %v", err)
	}
	drill := model.RestoreDrillEvidence{PolicyID: policy.ID, TaskID: task.ID, TaskRunID: run.ID, SandboxNodeID: node.ID, SandboxPath: "/tmp/xirang-drill", Status: "success", ConfidenceEligible: true, RestoreStatus: "success", VerifyStatus: "success", CleanupStatus: "success", CreatedAt: now, StartedAt: &now, FinishedAt: &now}
	if err := db.Create(&drill).Error; err != nil {
		t.Fatalf("创建健康恢复演练证据失败: %v", err)
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
	item := byCode["backup_restore_posture"]
	if item.Severity != "info" || item.Count != 0 || len(item.Examples) != 0 {
		t.Fatalf("健康备份恢复姿态应为 info 且无风险: %+v", item)
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
