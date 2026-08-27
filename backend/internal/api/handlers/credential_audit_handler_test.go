package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCredentialAuditListFiltersPaginationAndSort(t *testing.T) {
	db := openCredentialAuditHandlerTestDB(t)
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化凭据审计表失败: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	sshKeyID := uint(10)
	nodeID := uint(20)
	taskID := uint(30)
	taskRunID := uint(40)
	policyID := uint(50)
	records := []model.CredentialAuditEvent{
		{
			UserID: 1, Username: "admin", Role: "admin", Action: "ssh_key.export", Purpose: "ssh_key_export",
			CredentialKind: "ssh_key", CredentialSource: "ssh_key_id=10", SSHKeyID: &sshKeyID, NodeID: &nodeID,
			TaskID: &taskID, TaskRunID: &taskRunID, PolicyID: &policyID, Outcome: "success", Metadata: `{"format":"json"}`,
			ClientIP: "127.0.0.1", UserAgent: "curl/8", CreatedAt: now.Add(-2 * time.Hour),
		},
		{
			UserID: 2, Username: "operator", Role: "operator", Action: "terminal.open", Purpose: "terminal",
			CredentialKind: "password", CredentialSource: "node.password", Outcome: "blocked", Metadata: `{"stage":"auth"}`,
			ClientIP: "127.0.0.2", UserAgent: "browser", CreatedAt: now.Add(-1 * time.Hour),
		},
		{
			UserID: 1, Username: "admin", Role: "admin", Action: "task.credential.use", Purpose: "task_backup",
			CredentialKind: "ssh_key", CredentialSource: "ssh_key_id=10", SSHKeyID: &sshKeyID, NodeID: &nodeID,
			TaskID: &taskID, TaskRunID: &taskRunID, PolicyID: &policyID, Outcome: "failure", Metadata: `{"stage":"dial"}`,
			ClientIP: "127.0.0.3", UserAgent: "worker", CreatedAt: now,
		},
	}
	for _, one := range records {
		if err := db.Create(&one).Error; err != nil {
			t.Fatalf("插入凭据审计数据失败: %v", err)
		}
	}

	r := gin.New()
	handler := NewCredentialAuditHandler(db)
	r.GET("/credential-audit-events", handler.List)

	from := now.Add(-30 * time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf(
		"/credential-audit-events?username=admin&role=admin&user_id=1&action=task.credential.use&purpose=task_backup&credential_kind=ssh_key&credential_source=ssh_key_id=10&outcome=failure&ssh_key_id=%d&node_id=%d&task_id=%d&task_run_id=%d&policy_id=%d&from=%s&page_size=1&sort_by=created_at&sort_order=asc",
		sshKeyID, nodeID, taskID, taskRunID, policyID, from,
	)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data     []credentialAuditEventResponse `json:"data"`
		Total    int64                          `json:"total"`
		Page     int                            `json:"page"`
		PageSize int                            `json:"page_size"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 || result.Page != 1 || result.PageSize != 1 {
		t.Fatalf("分页结果不符合预期: total=%d len=%d page=%d page_size=%d", result.Total, len(result.Data), result.Page, result.PageSize)
	}
	if got := result.Data[0].Action; got != "task.credential.use" {
		t.Fatalf("期望筛选到 task.credential.use，实际: %s", got)
	}
	if result.Data[0].TaskRunID == nil || *result.Data[0].TaskRunID != taskRunID {
		t.Fatalf("task_run_id 映射不符合预期: %#v", result.Data[0].TaskRunID)
	}
}

func TestCredentialAuditListSanitizesLegacyMetadataAndError(t *testing.T) {
	db := openCredentialAuditHandlerTestDB(t)
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化凭据审计表失败: %v", err)
	}

	rows := []model.CredentialAuditEvent{
		{
			UserID: 1, Username: "admin", Role: "admin", Action: "terminal.failure", Purpose: "terminal",
			CredentialKind: "ssh_key", CredentialSource: "ssh_key_id=1", Outcome: "failure",
			ErrorMessage: "连接失败 stdout: FAKE_REMOTE_OUTPUT_FOR_TEST_ONLY private_key=FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
			Metadata:     `{"stage":"open","path_hash":"abc123","private_key":"FAKE_PRIVATE_KEY_FOR_TEST_ONLY","safe_list":["one","token=FAKE_TOKEN_FOR_TEST_ONLY","two"],"nested":{"command":"cat /etc/passwd"},"content_hint":"FAKE_FILE_CONTENT_FOR_TEST_ONLY","note":"authorization: Bearer FAKE_TOKEN_FOR_TEST_ONLY"}`,
			CreatedAt:    time.Now().UTC(),
		},
		{
			UserID: 2, Username: "legacy", Role: "admin", Action: "ssh_key.export", Purpose: "ssh_key_export",
			CredentialKind: "ssh_key", CredentialSource: "ssh_key_id=2", Outcome: "success",
			Metadata: `not-json`, CreatedAt: time.Now().UTC(),
		},
		{
			UserID: 3, Username: "legacy-stack", Role: "admin", Action: "node.doctor.run", Purpose: "node_test",
			CredentialKind: "password", CredentialSource: "node.password", Outcome: "failure",
			ErrorMessage: "panic: runtime error\nbackend/internal/task/executor/ssh_connect.go:101",
			Metadata:     `{}`,
			CreatedAt:    time.Now().UTC(),
		},
		{
			UserID: 4, Username: "legacy-endpoint", Role: "admin", Action: "config.export", Purpose: "config_export",
			CredentialKind: "system_setting", CredentialSource: "https://example.invalid/hook/FAKE_TOKEN_FOR_TEST_ONLY?token=FAKE_TOKEN_FOR_TEST_ONLY", Outcome: "failure",
			ErrorMessage: "request failed: https://example.invalid/hook/FAKE_TOKEN_FOR_TEST_ONLY?token=FAKE_TOKEN_FOR_TEST_ONLY Authorization: Bearer FAKE_BEARER_FOR_TEST_ONLY private_key=FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
			Metadata:     `{}`,
			CreatedAt:    time.Now().UTC(),
		},
	}
	for _, row := range rows {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("插入凭据审计数据失败: %v", err)
		}
	}

	r := gin.New()
	handler := NewCredentialAuditHandler(db)
	r.GET("/credential-audit-events", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/credential-audit-events?sort_by=id&sort_order=asc", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, forbidden := range []string{"FAKE_PRIVATE_KEY_FOR_TEST_ONLY", "FAKE_TOKEN_FOR_TEST_ONLY", "FAKE_BEARER_FOR_TEST_ONLY", "FAKE_FILE_CONTENT_FOR_TEST_ONLY", "cat /etc/passwd", "private_key", "content_hint", "ssh_connect.go", "runtime error", "example.invalid", "https://"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("响应包含禁止内容 %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "[REDACTED_OUTPUT]") {
		t.Fatalf("响应应包含输出红action，body=%s", body)
	}
	if !strings.Contains(body, "[REDACTED_ERROR]") {
		t.Fatalf("响应应包含 stack-like 错误红action，body=%s", body)
	}

	var result struct {
		Data []credentialAuditEventResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(result.Data) != 4 {
		t.Fatalf("期望 4 条数据，实际: %d", len(result.Data))
	}
	metadata := result.Data[0].Metadata
	if metadata["stage"] != "open" || metadata["path_hash"] != "abc123" {
		t.Fatalf("安全 metadata 未保留: %#v", metadata)
	}
	if got, ok := metadata["safe_list"].([]any); !ok || len(got) != 2 {
		t.Fatalf("safe_list 应仅保留安全字符串，实际: %#v", metadata["safe_list"])
	}
	if len(result.Data[1].Metadata) != 0 {
		t.Fatalf("非法 JSON metadata 应映射为空对象，实际: %#v", result.Data[1].Metadata)
	}
}

func TestCredentialAuditExportCSVUsesFiltersAndSafeDTO(t *testing.T) {
	db := openCredentialAuditHandlerTestDB(t)
	if err := db.AutoMigrate(&model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化凭据审计表失败: %v", err)
	}

	now := time.Now().UTC()
	records := []model.CredentialAuditEvent{
		{
			UserID: 1, Username: "admin", Role: "admin", Action: "config.export", Purpose: "config_export",
			CredentialKind: "system_setting", CredentialSource: "settings", Outcome: "success",
			ErrorMessage: "", Metadata: `{"format":"json","payload":"FAKE_EXPORTED_CONFIG_PAYLOAD_FOR_TEST_ONLY"}`,
			ClientIP: "127.0.0.1", UserAgent: "curl/8", CreatedAt: now.Add(-time.Minute),
		},
		{
			UserID: 2, Username: "operator", Role: "operator", Action: "terminal.open", Purpose: "terminal",
			CredentialKind: "password", CredentialSource: "node.password", Outcome: "blocked",
			ErrorMessage: "blocked output: FAKE_REMOTE_OUTPUT_FOR_TEST_ONLY", Metadata: `{"stage":"auth"}`,
			ClientIP: "127.0.0.2", UserAgent: "browser", CreatedAt: now,
		},
	}
	for _, one := range records {
		if err := db.Create(&one).Error; err != nil {
			t.Fatalf("插入凭据审计数据失败: %v", err)
		}
	}

	r := gin.New()
	handler := NewCredentialAuditHandler(db)
	r.GET("/credential-audit-events/export", handler.ExportCSV)

	req := httptest.NewRequest(http.MethodGet, "/credential-audit-events/export?action=config.export&page_size=99999", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("期望 csv content-type，实际: %s", resp.Header().Get("Content-Type"))
	}
	if !strings.Contains(resp.Header().Get("Content-Disposition"), "credential-audit-events-") {
		t.Fatalf("缺少下载文件名: %s", resp.Header().Get("Content-Disposition"))
	}
	body := resp.Body.String()
	if !strings.HasPrefix(body, string([]byte{0xEF, 0xBB, 0xBF})) {
		t.Fatalf("CSV 应包含 UTF-8 BOM")
	}
	if !strings.Contains(body, "id,created_at,username,role,action,purpose,credential_kind,credential_source,outcome,user_id,ssh_key_id,node_id,task_id,task_run_id,policy_id,client_ip,user_agent,error_message,metadata") {
		t.Fatalf("CSV 头缺失，内容: %s", body)
	}
	if !strings.Contains(body, "config.export") || !strings.Contains(body, `"{""format"":""json""}"`) {
		t.Fatalf("CSV 缺少安全导出记录，内容: %s", body)
	}
	for _, forbidden := range []string{"terminal.open", "FAKE_EXPORTED_CONFIG_PAYLOAD_FOR_TEST_ONLY", "payload", "FAKE_REMOTE_OUTPUT_FOR_TEST_ONLY"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("CSV 包含禁止内容 %q: %s", forbidden, body)
		}
	}
}

func openCredentialAuditHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
