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
	"xirang/backend/internal/settings"

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
