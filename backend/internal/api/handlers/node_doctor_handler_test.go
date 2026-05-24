package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
)

func TestNodeDoctorRejectsCustomInput(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	node := model.Node{
		Name:      "doctor-node-custom-input",
		Host:      "10.0.0.31",
		Port:      22,
		Username:  "root",
		AuthType:  "password",
		Password:  "FAKE_NODE_PASSWORD_FOR_TEST_ONLY",
		BackupDir: "doctor-node-custom-input",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil)
	r.POST("/nodes/:id/doctor", handler.RunDoctor)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/doctor", node.ID), strings.NewReader(`{"command":"whoami"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望拒绝自定义诊断输入返回 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "whoami") {
		t.Fatalf("响应不应回显任意命令，实际: %s", resp.Body.String())
	}
}

func TestNodeDoctorRejectsChunkedCustomInput(t *testing.T) {
	r := gin.New()
	handler := NewNodeHandler(openNodeHandlerTestDB(t), nil)
	r.POST("/nodes/:id/doctor", handler.RunDoctor)

	req := httptest.NewRequest(http.MethodPost, "/nodes/1/doctor", strings.NewReader(`{"checks":["ssh"]}`))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("chunked 自定义诊断输入应返回 400，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
}

func TestNodeDoctorAuthFailureSkipsSSHDependentChecks(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.Task{}, &model.Policy{}, &model.PolicyNode{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	node := model.Node{
		Name:      "doctor-node-auth-fail",
		Host:      "10.0.0.32",
		Port:      22,
		Username:  "root",
		AuthType:  "password",
		Password:  "",
		BackupDir: "doctor-node-auth-fail",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil)
	r.POST("/nodes/:id/doctor", handler.RunDoctor)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/doctor", node.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			NodeID uint                `json:"node_id"`
			Checks []doctorCheckResult `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Code != 0 || envelope.Data.NodeID != node.ID {
		t.Fatalf("响应信封不符合预期: %+v", envelope)
	}
	statuses := make(map[string]doctorCheckStatus)
	for _, check := range envelope.Data.Checks {
		statuses[check.Check] = check.Status
	}
	if statuses["auth"] != doctorStatusFail || statuses["ssh"] != doctorStatusFail {
		t.Fatalf("认证失败时 auth/ssh 应失败，实际: %+v", statuses)
	}
	for _, check := range []string{"sudo", "tools", "backup_dir", "disk"} {
		if statuses[check] != doctorStatusSkip {
			t.Fatalf("%s 应跳过，实际: %+v", check, statuses)
		}
	}
}

func TestNodeDoctorWritesSafeCredentialAuditForBlockedDiagnostics(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.Task{}, &model.Policy{}, &model.PolicyNode{}, &model.CredentialAuditEvent{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	keyID := uint(12)
	node := model.Node{
		Name:      "doctor-audit-node",
		Host:      "10.88.0.12",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		SSHKeyID:  &keyID,
		BackupDir: "doctor-audit-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	r := gin.New()
	handler := NewNodeHandler(db, nil)
	r.POST("/nodes/:id/doctor", func(c *gin.Context) {
		c.Set("userID", uint(101))
		c.Set("username", "alice")
		c.Set("role", "operator")
		handler.RunDoctor(c)
	})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/doctor", node.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("Doctor 认证配置失败应返回结构化 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "node.doctor.run").First(&event).Error; err != nil {
		t.Fatalf("应写入 node.doctor.run 审计事件: %v", err)
	}
	if event.Purpose != sshutil.PurposeNodeTest || event.Outcome != credentialaudit.OutcomeBlocked || event.NodeID == nil || *event.NodeID != node.ID || event.SSHKeyID == nil || *event.SSHKeyID != keyID {
		t.Fatalf("doctor audit event 不符合预期: %+v", event)
	}
	for _, forbidden := range []string{"FAKE_", "10.88.0.12", "doctor-audit-node", "未绑定", "私钥", "password", "PRIVATE KEY"} {
		if strings.Contains(event.Metadata, forbidden) || strings.Contains(event.ErrorMessage, forbidden) {
			t.Fatalf("doctor audit 不应复制诊断证据/主机/敏感词 %q: metadata=%s error=%s", forbidden, event.Metadata, event.ErrorMessage)
		}
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if metadata["stage"] != "complete" || metadata["check_count"] == nil || metadata["failure_count"] == nil || metadata["skip_count"] == nil {
		t.Fatalf("doctor audit metadata 缺少安全计数字段: %#v", metadata)
	}
}

func TestNodeDoctorAuditOutcomeClassifiesAuthFailureAsBlocked(t *testing.T) {
	checks := []doctorCheckResult{
		{Check: "auth", Status: doctorStatusFail, Evidence: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY", Suggestion: "检查凭据"},
		{Check: "ssh", Status: doctorStatusFail},
		{Check: "disk", Status: doctorStatusSkip},
	}
	if got := doctorAuditOutcome(checks); got != credentialaudit.OutcomeBlocked {
		t.Fatalf("auth/ssh failure 应归类为 blocked，got %s", got)
	}
}

func TestNodeDoctorProbeStatusClassification(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	now := time.Now().UTC()
	runner := nodeDoctorRunner{
		db: db,
		node: model.Node{
			Name:                "doctor-node-probe",
			Status:              "offline",
			ConsecutiveFailures: 3,
			LastProbeAt:         &now,
		},
		now: now.Add(2 * time.Minute),
	}
	runner.checkProbeStatus()
	if len(runner.checks) != 1 {
		t.Fatalf("期望生成 1 个 probe 检查，实际: %d", len(runner.checks))
	}
	check := runner.checks[0]
	if check.Check != "probe" || check.Status != doctorStatusFail {
		t.Fatalf("期望 probe fail，实际: %+v", check)
	}
}

func TestNodeDoctorUsesStorageSettingForDiskThreshold(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatalf("初始化设置表失败: %v", err)
	}
	settingsSvc := settings.NewService(db)
	if err := settingsSvc.Update("storage.min_free_gb", "42"); err != nil {
		t.Fatalf("更新测试设置失败: %v", err)
	}
	runner := nodeDoctorRunner{db: db, settingsSvc: settingsSvc}

	if got := runner.doctorMinFreeGB(); got != 42 {
		t.Fatalf("Doctor 应从 settings.Service 读取磁盘阈值，实际: %d", got)
	}
}

func TestNodeDoctorClassifiesCommonSSHErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "known_hosts", err: fmt.Errorf("knownhosts: key mismatch"), want: "known_hosts 校验失败或主机密钥冲突"},
		{name: "auth", err: fmt.Errorf("ssh: unable to authenticate, attempted methods [none]"), want: "SSH 认证失败"},
		{name: "network", err: fmt.Errorf("dial tcp 10.0.0.1:22: connection refused"), want: "SSH 网络连接失败或端口不可达"},
		{name: "handshake", err: fmt.Errorf("ssh: handshake failed"), want: "SSH 握手失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDoctorSSHEvidence(tc.err); got != tc.want {
				t.Fatalf("分类不符合预期，want=%q got=%q", tc.want, got)
			}
			if suggestion := classifyDoctorSSHSuggestion(tc.err); suggestion == "" {
				t.Fatalf("分类 %s 应给出建议", tc.name)
			}
		})
	}
}

func TestNodeDoctorAllowlistRejectsArbitraryCommands(t *testing.T) {
	allowed := []string{
		"sudo -n true 2>&1",
		"df -BG / | awk 'NR==2 {print $2\" \"$3}'",
		"command -v 'rsync' >/dev/null 2>&1",
		"test -d '/backup/node-a' && test -w '/backup/node-a'",
	}
	for _, command := range allowed {
		if !doctorCommandAllowed(command) {
			t.Fatalf("期望允许 allowlisted 命令: %s", command)
		}
	}

	rejected := []string{
		"whoami",
		"command -v 'bash; whoami' >/dev/null 2>&1",
		"test -d /backup/node-a; rm -rf /",
		"test -d '/' && test -w '/'",
		"test -d 'relative/path'",
		"cat /etc/passwd",
	}
	for _, command := range rejected {
		if doctorCommandAllowed(command) {
			t.Fatalf("期望拒绝任意命令: %s", command)
		}
	}
}

func TestNodeDoctorSanitizesSensitiveEvidence(t *testing.T) {
	input := "failed with password=FAKE_NODE_PASSWORD_FOR_TEST_ONLY and -----BEGIN PRIVATE KEY-----"
	got := sanitizeDoctorEvidence(input)
	if strings.Contains(got, "FAKE_NODE_PASSWORD_FOR_TEST_ONLY") || strings.Contains(got, "PRIVATE KEY") {
		t.Fatalf("证据未脱敏: %s", got)
	}
	if got == "" {
		t.Fatalf("脱敏后应返回占位说明")
	}
}

func TestNodeDoctorSanitizesDiagnosticEvidenceHostPathAndOutput(t *testing.T) {
	input := "sudo failed on prod-db.example.internal at 10.12.13.14:22 path /srv/backups/prod and root@backup.internal:/repo output: cat /etc/passwd"
	got := sanitizeDoctorEvidence(input)
	for _, forbidden := range []string{"prod-db.example.internal", "backup.internal", "10.12.13.14", "/srv/backups/prod", "/repo", "/etc/passwd", "cat"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("doctor evidence 泄漏 %q: %s", forbidden, got)
		}
	}
	for _, expected := range []string{"[HOST_REDACTED]", "[PATH_REDACTED]", "[REMOTE_PATH_REDACTED]", "[REDACTED_OUTPUT]"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("doctor evidence 缺少脱敏占位 %q: %s", expected, got)
		}
	}
}

func TestNodeDoctorSanitizesDiagnosticPathAssignments(t *testing.T) {
	input := "path=/srv/backups/prod home=~/restore endpoint=https://backup.example.internal:8443/api"
	got := sanitizeDoctorEvidence(input)
	for _, forbidden := range []string{"/srv/backups/prod", "~/restore", "backup.example.internal", ":8443", "/api"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("doctor evidence 泄漏路径或端点 %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[PATH_REDACTED]") || !strings.Contains(got, "[ENDPOINT_REDACTED]") {
		t.Fatalf("doctor evidence 缺少路径/端点脱敏占位: %s", got)
	}
}

func TestNodeDoctorSanitizesDiagnosticCommandText(t *testing.T) {
	input := "diagnostic failed command: cat /etc/hosts on prod-db.example.internal"
	got := sanitizeDoctorEvidence(input)
	for _, forbidden := range []string{"cat", "/etc/hosts", "prod-db.example.internal"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("doctor evidence 泄漏命令文本 %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[REDACTED_COMMAND]") {
		t.Fatalf("doctor evidence 缺少命令文本脱敏占位: %s", got)
	}
}

func TestNodeDoctorSudoEvidenceDoesNotCopyCommandOutput(t *testing.T) {
	got := classifyDoctorSudoEvidence("sudo: a password is required\noutput: cat /etc/passwd on prod-db.example.internal")
	for _, forbidden := range []string{"cat", "/etc/passwd", "prod-db.example.internal"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("sudo evidence 不应复制原始命令输出 %q: %s", forbidden, got)
		}
	}
	if got == "" || strings.Contains(got, "[REDACTED_OUTPUT]") {
		t.Fatalf("sudo evidence 应返回通用分类而非原始输出脱敏片段: %s", got)
	}
}
