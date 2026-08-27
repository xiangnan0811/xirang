package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openBackupConfidenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.NodeOwner{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.Alert{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	return db
}

type backupConfidenceTestResponse struct {
	Data struct {
		Summary struct {
			Healthy      int `json:"healthy"`
			Warning      int `json:"warning"`
			AtRisk       int `json:"at_risk"`
			Insufficient int `json:"insufficient"`
			Total        int `json:"total"`
		} `json:"summary"`
		Items []struct {
			ID         string `json:"id"`
			Scope      string `json:"scope"`
			PolicyID   uint   `json:"policy_id"`
			PolicyName string `json:"policy_name"`
			NodeID     uint   `json:"node_id"`
			NodeName   string `json:"node_name"`
			Status     string `json:"status"`
			Score      int    `json:"score"`
			Reasons    []struct {
				Code     string `json:"code"`
				Severity string `json:"severity"`
				Message  string `json:"message"`
			} `json:"reasons"`
			Evidence []struct {
				Type      string `json:"type"`
				Status    string `json:"status"`
				Message   string `json:"message"`
				TaskRunID uint   `json:"task_run_id"`
				AlertID   uint   `json:"alert_id"`
			} `json:"evidence"`
			NextSteps []struct {
				Code  string `json:"code"`
				Label string `json:"label"`
			} `json:"next_steps"`
			Targets []struct {
				NodeID       uint       `json:"node_id"`
				NodeName     string     `json:"node_name"`
				LastBackupAt *time.Time `json:"last_backup_at"`
			} `json:"targets"`
		} `json:"items"`
	} `json:"data"`
}

func callBackupConfidence(t *testing.T, db *gorm.DB) (*httptest.ResponseRecorder, backupConfidenceTestResponse) {
	t.Helper()
	return callBackupConfidenceWithUser(t, db, "admin", 0)
}

func callBackupConfidenceWithUser(t *testing.T, db *gorm.DB, role string, userID uint) (*httptest.ResponseRecorder, backupConfidenceTestResponse) {
	t.Helper()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		if userID != 0 {
			c.Set(middleware.CtxUserID, userID)
		}
		c.Next()
	})
	r.GET("/overview/backup-confidence", NewBackupConfidenceHandler(db).Get)

	req := httptest.NewRequest(http.MethodGet, "/overview/backup-confidence", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var result backupConfidenceTestResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, resp.Body.String())
	}
	return resp, result
}

func seedConfidencePolicy(t *testing.T, db *gorm.DB, name string, opts ...func(*model.Policy)) (model.Policy, model.Node, model.Task) {
	t.Helper()
	node := model.Node{Name: name + "-node", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: name + "-backup"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := model.Policy{Name: name, SourcePath: "/srv/" + name, TargetPath: "/backup", CronSpec: "0 * * * *", Enabled: true, VerifyEnabled: true, DrillEnabled: true}
	for _, opt := range opts {
		opt(&policy)
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("创建策略节点关联失败: %v", err)
	}
	task := model.Task{Name: name + "-task", NodeID: node.ID, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "success", VerifyStatus: "passed", Enabled: true}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return policy, node, task
}

func addRun(t *testing.T, db *gorm.DB, taskID uint, status string, trigger string, finishedAt time.Time, verifyStatus string) model.TaskRun {
	t.Helper()
	startedAt := finishedAt.Add(-5 * time.Minute)
	run := model.TaskRun{
		TaskID:       taskID,
		TriggerType:  trigger,
		Status:       status,
		StartedAt:    &startedAt,
		FinishedAt:   &finishedAt,
		DurationMs:   int64(5 * time.Minute / time.Millisecond),
		VerifyStatus: verifyStatus,
		Progress:     100,
		CreatedAt:    finishedAt,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}
	return run
}

func addDrillEvidence(t *testing.T, db *gorm.DB, policy model.Policy, task model.Task, run model.TaskRun, status string, confidenceEligible bool, failedStep string) model.RestoreDrillEvidence {
	t.Helper()
	startedAt := run.CreatedAt.Add(-10 * time.Minute)
	finishedAt := run.CreatedAt
	evidence := model.RestoreDrillEvidence{
		PolicyID:           policy.ID,
		TaskID:             task.ID,
		TaskRunID:          run.ID,
		SandboxNodeID:      task.NodeID,
		SandboxNodeName:    "sandbox-node",
		SandboxPath:        "/tmp/xirang-drill",
		Status:             status,
		FailedStep:         failedStep,
		ConfidenceEligible: confidenceEligible,
		StartedAt:          &startedAt,
		FinishedAt:         &finishedAt,
		DurationMs:         int64(10 * time.Minute / time.Millisecond),
		RestoreStatus:      status,
		VerifyStatus:       status,
		PostVerifyStatus:   "skipped",
		CleanupStatus:      "success",
		CreatedAt:          finishedAt,
	}
	if err := db.Create(&evidence).Error; err != nil {
		t.Fatalf("创建恢复演练证据失败: %v", err)
	}
	return evidence
}

func reasonCodes(item struct {
	ID         string `json:"id"`
	Scope      string `json:"scope"`
	PolicyID   uint   `json:"policy_id"`
	PolicyName string `json:"policy_name"`
	NodeID     uint   `json:"node_id"`
	NodeName   string `json:"node_name"`
	Status     string `json:"status"`
	Score      int    `json:"score"`
	Reasons    []struct {
		Code     string `json:"code"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	} `json:"reasons"`
	Evidence []struct {
		Type      string `json:"type"`
		Status    string `json:"status"`
		Message   string `json:"message"`
		TaskRunID uint   `json:"task_run_id"`
		AlertID   uint   `json:"alert_id"`
	} `json:"evidence"`
	NextSteps []struct {
		Code  string `json:"code"`
		Label string `json:"label"`
	} `json:"next_steps"`
	Targets []struct {
		NodeID       uint       `json:"node_id"`
		NodeName     string     `json:"node_name"`
		LastBackupAt *time.Time `json:"last_backup_at"`
	} `json:"targets"`
}) map[string]bool {
	codes := make(map[string]bool, len(item.Reasons))
	for _, reason := range item.Reasons {
		codes[reason.Code] = true
	}
	return codes
}

func TestBackupConfidenceHealthyWithBackupAndEligibleDrill(t *testing.T) {
	db := openBackupConfidenceTestDB(t)
	policy, _, task := seedConfidencePolicy(t, db, "policy-healthy", func(p *model.Policy) { p.RPOMinutes = 24 * 60 })
	now := time.Now()
	backupRun := addRun(t, db, task.ID, "success", "cron", now.Add(-time.Hour), "passed")
	drillRun := addRun(t, db, task.ID, "success", "drill", now.Add(-30*time.Minute), "passed")
	addDrillEvidence(t, db, policy, task, drillRun, "success", true, "")

	resp, result := callBackupConfidence(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}
	if result.Data.Summary.Healthy != 1 || len(result.Data.Items) != 1 {
		t.Fatalf("期望 1 个健康项，summary=%+v items=%d", result.Data.Summary, len(result.Data.Items))
	}
	item := result.Data.Items[0]
	if item.Status != "healthy" {
		t.Fatalf("期望 healthy，实际: %s, reasons=%+v", item.Status, item.Reasons)
	}
	if item.Score != 100 {
		t.Fatalf("期望 score=100，实际: %d", item.Score)
	}
	if item.Evidence[0].TaskRunID != backupRun.ID {
		t.Fatalf("期望备份证据引用 run %d，实际: %+v", backupRun.ID, item.Evidence)
	}
	encoded := resp.Body.String()
	for _, sensitive := range []string{"password", "private_key", "executor_config", "Host", "10.0.0.1"} {
		if strings.Contains(encoded, sensitive) {
			t.Fatalf("confidence 响应不应暴露敏感/连接字段 %q: %s", sensitive, encoded)
		}
	}
}

func TestBackupConfidenceRecentFailureAndRPOExceeded(t *testing.T) {
	db := openBackupConfidenceTestDB(t)
	policy, _, task := seedConfidencePolicy(t, db, "policy-risk", func(p *model.Policy) { p.RPOMinutes = 60 })
	now := time.Now()
	addRun(t, db, task.ID, "success", "cron", now.Add(-3*time.Hour), "passed")
	addRun(t, db, task.ID, "failed", "manual", now.Add(-20*time.Minute), "failed")
	drillRun := addRun(t, db, task.ID, "success", "drill", now.Add(-10*time.Minute), "passed")
	addDrillEvidence(t, db, policy, task, drillRun, "success", true, "")

	_, result := callBackupConfidence(t, db)
	item := result.Data.Items[0]
	codes := reasonCodes(item)
	if item.Status != "at_risk" {
		t.Fatalf("期望 at_risk，实际: %s reasons=%+v", item.Status, item.Reasons)
	}
	if !codes["recent_backup_failed"] || !codes["rpo_exceeded"] {
		t.Fatalf("期望包含 recent_backup_failed 与 rpo_exceeded，实际: %+v", codes)
	}
	if len(item.NextSteps) == 0 {
		t.Fatalf("非健康状态必须提供下一步")
	}
}

func TestBackupConfidenceMissingDrillIsInsufficient(t *testing.T) {
	db := openBackupConfidenceTestDB(t)
	_, _, task := seedConfidencePolicy(t, db, "policy-no-drill")
	addRun(t, db, task.ID, "success", "cron", time.Now().Add(-time.Hour), "passed")

	_, result := callBackupConfidence(t, db)
	item := result.Data.Items[0]
	codes := reasonCodes(item)
	if item.Status != "insufficient" {
		t.Fatalf("缺少恢复演练应为 insufficient，实际: %s", item.Status)
	}
	if !codes["drill_missing"] {
		t.Fatalf("期望包含 drill_missing，实际: %+v", codes)
	}
	if len(item.NextSteps) == 0 {
		t.Fatalf("非健康状态必须提供下一步")
	}
}

func TestBackupConfidenceFailedDrillAndIntegrityAlert(t *testing.T) {
	db := openBackupConfidenceTestDB(t)
	policy, node, task := seedConfidencePolicy(t, db, "policy-drill-failed")
	now := time.Now()
	addRun(t, db, task.ID, "success", "cron", now.Add(-time.Hour), "passed")
	drillRun := addRun(t, db, task.ID, "failed", "drill", now.Add(-20*time.Minute), "failed")
	addDrillEvidence(t, db, policy, task, drillRun, "failed", false, "verify")
	alert := model.Alert{
		NodeID:      node.ID,
		NodeName:    node.Name,
		PolicyName:  policy.Name,
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   fmt.Sprintf("XR-INTG-%d", policy.ID),
		Message:     "完整性校验失败，password=FAKE_PASSWORD_FOR_TEST_ONLY，路径 /var/run/app/token",
		Retryable:   false,
		TriggeredAt: now,
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	resp, result := callBackupConfidence(t, db)
	item := result.Data.Items[0]
	codes := reasonCodes(item)
	if item.Status != "at_risk" {
		t.Fatalf("期望 at_risk，实际: %s reasons=%+v", item.Status, item.Reasons)
	}
	if !codes["drill_failed"] || !codes["integrity_alert"] {
		t.Fatalf("期望包含 drill_failed 与 integrity_alert，实际: %+v", codes)
	}
	if len(item.NextSteps) == 0 {
		t.Fatalf("非健康状态必须提供下一步")
	}
	encoded := resp.Body.String()
	for _, sensitive := range []string{"FAKE_PASSWORD_FOR_TEST_ONLY", "/var/run/app/token"} {
		if strings.Contains(encoded, sensitive) {
			t.Fatalf("confidence 告警证据应脱敏 %q: %s", sensitive, encoded)
		}
	}
}

func TestBackupConfidenceRecentFailedDrillOverridesMissingProof(t *testing.T) {
	db := openBackupConfidenceTestDB(t)
	policy, _, task := seedConfidencePolicy(t, db, "policy-failed-drill-risk")
	now := time.Now()
	addRun(t, db, task.ID, "success", "cron", now.Add(-time.Hour), "passed")
	drillRun := addRun(t, db, task.ID, "failed", "drill", now.Add(-10*time.Minute), "failed")
	addDrillEvidence(t, db, policy, task, drillRun, "failed", false, "cleanup")

	_, result := callBackupConfidence(t, db)
	item := result.Data.Items[0]
	codes := reasonCodes(item)
	if item.Status != "at_risk" {
		t.Fatalf("失败演练应强于证据不足并返回 at_risk，实际: %s reasons=%+v", item.Status, item.Reasons)
	}
	if !codes["drill_failed"] {
		t.Fatalf("期望包含 drill_failed，实际: %+v", codes)
	}
}

func TestBackupConfidenceOperatorOnlySeesOwnedPolicyTargets(t *testing.T) {
	db := openBackupConfidenceTestDB(t)
	_, ownedNode, ownedTask := seedConfidencePolicy(t, db, "policy-owned")
	_, unownedNode, unownedTask := seedConfidencePolicy(t, db, "policy-unowned")
	now := time.Now()
	addRun(t, db, ownedTask.ID, "success", "cron", now.Add(-time.Hour), "passed")
	addRun(t, db, unownedTask.ID, "success", "cron", now.Add(-time.Hour), "passed")
	user := model.User{Username: "operator-confidence", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: user.ID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}

	_, result := callBackupConfidenceWithUser(t, db, "operator", user.ID)
	if len(result.Data.Items) != 1 {
		t.Fatalf("operator 仅应看到 1 个拥有节点的策略，实际: %d", len(result.Data.Items))
	}
	item := result.Data.Items[0]
	if item.PolicyName != "policy-owned" || item.NodeID != ownedNode.ID {
		t.Fatalf("operator 看到了非拥有策略/节点: policy=%s node=%d unowned=%d", item.PolicyName, item.NodeID, unownedNode.ID)
	}
	if len(item.Targets) != 1 || item.Targets[0].NodeID != ownedNode.ID {
		t.Fatalf("operator 响应 targets 不应包含非拥有节点: %+v", item.Targets)
	}
}

// TestBackupConfidenceNoVisibleTasksDoesNotLeakUnownedDrill: operator sees a
// shared policy via an owned node that has no tasks, while unowned sibling has
// drill evidence — must not surface that evidence.
func TestBackupConfidenceNoVisibleTasksDoesNotLeakUnownedDrill(t *testing.T) {
	db := openBackupConfidenceTestDB(t)

	ownedNode := model.Node{Name: "shared-owned-node", Host: "10.2.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "shared-owned"}
	unownedNode := model.Node{Name: "shared-unowned-node", Host: "10.2.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "shared-unowned"}
	for _, n := range []*model.Node{&ownedNode, &unownedNode} {
		if err := db.Create(n).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	policy := model.Policy{Name: "shared-confidence", SourcePath: "/srv/shared", TargetPath: "/backup", CronSpec: "0 * * * *", Enabled: true, VerifyEnabled: true, DrillEnabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	for _, link := range []model.PolicyNode{
		{PolicyID: policy.ID, NodeID: ownedNode.ID},
		{PolicyID: policy.ID, NodeID: unownedNode.ID},
	} {
		if err := db.Create(&link).Error; err != nil {
			t.Fatalf("创建策略节点关联失败: %v", err)
		}
	}
	// Only unowned node has a task + successful drill.
	unownedTask := model.Task{Name: "unowned-task", NodeID: unownedNode.ID, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "success", VerifyStatus: "passed", Enabled: true}
	if err := db.Create(&unownedTask).Error; err != nil {
		t.Fatalf("创建 unowned task 失败: %v", err)
	}
	now := time.Now()
	backupRun := addRun(t, db, unownedTask.ID, "success", "cron", now.Add(-time.Hour), "passed")
	_ = backupRun
	drillRun := addRun(t, db, unownedTask.ID, "success", "drill", now.Add(-30*time.Minute), "passed")
	addDrillEvidence(t, db, policy, unownedTask, drillRun, "success", true, "")

	user := model.User{Username: "operator-shared-confidence", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: user.ID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}

	resp, result := callBackupConfidenceWithUser(t, db, "operator", user.ID)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
	}
	if len(result.Data.Items) != 1 {
		t.Fatalf("期望 1 项，实际: %d", len(result.Data.Items))
	}
	item := result.Data.Items[0]
	codes := reasonCodes(item)
	// Must treat drill as missing (no visible tasks), not healthy from unowned drill.
	if !codes["drill_missing"] {
		t.Fatalf("无可见任务时不应泄露 unowned 演练；期望 drill_missing，实际 reasons=%+v evidence=%+v", item.Reasons, item.Evidence)
	}
	for _, ev := range item.Evidence {
		if ev.Type == "drill" && ev.Status != "missing" {
			t.Fatalf("不应返回 unowned drill 证据: %+v", ev)
		}
	}
	if item.Status == "healthy" {
		t.Fatalf("无可见备份/演练时不应 healthy: status=%s", item.Status)
	}
}

// TestBackupConfidenceDrillRequiresSandboxOwnership: owned source task with
// unowned sandbox must not surface drill evidence.
func TestBackupConfidenceDrillRequiresSandboxOwnership(t *testing.T) {
	db := openBackupConfidenceTestDB(t)

	source := model.Node{Name: "conf-src", Host: "10.4.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "conf-src"}
	sandbox := model.Node{Name: "conf-sbx", Host: "10.4.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "conf-sbx"}
	for _, n := range []*model.Node{&source, &sandbox} {
		if err := db.Create(n).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	policy := model.Policy{Name: "conf-both-ends", SourcePath: "/srv/conf", TargetPath: "/backup", CronSpec: "0 * * * *", Enabled: true, VerifyEnabled: true, DrillEnabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: source.ID}).Error; err != nil {
		t.Fatalf("创建策略节点关联失败: %v", err)
	}
	task := model.Task{Name: "conf-task", NodeID: source.ID, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "success", VerifyStatus: "passed", Enabled: true}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	now := time.Now()
	addRun(t, db, task.ID, "success", "cron", now.Add(-time.Hour), "passed")
	drillRun := addRun(t, db, task.ID, "success", "drill", now.Add(-30*time.Minute), "passed")
	// Sandbox is unowned by the operator below.
	evidence := model.RestoreDrillEvidence{
		PolicyID:           policy.ID,
		TaskID:             task.ID,
		TaskRunID:          drillRun.ID,
		SandboxNodeID:      sandbox.ID,
		SandboxNodeName:    "conf-sbx",
		SandboxPath:        "/tmp/xirang-drill",
		Status:             "success",
		ConfidenceEligible: true,
		StartedAt:          &now,
		FinishedAt:         &now,
		DurationMs:         1000,
		RestoreStatus:      "success",
		VerifyStatus:       "success",
		PostVerifyStatus:   "skipped",
		CleanupStatus:      "success",
		CreatedAt:          now,
	}
	if err := db.Create(&evidence).Error; err != nil {
		t.Fatalf("创建演练证据失败: %v", err)
	}

	user := model.User{Username: "operator-src-only", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: source.ID, UserID: user.ID}).Error; err != nil {
		t.Fatalf("创建 ownership 失败: %v", err)
	}

	resp, result := callBackupConfidenceWithUser(t, db, "operator", user.ID)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
	}
	if len(result.Data.Items) != 1 {
		t.Fatalf("期望 1 项，实际: %d", len(result.Data.Items))
	}
	item := result.Data.Items[0]
	codes := reasonCodes(item)
	if !codes["drill_missing"] {
		t.Fatalf("仅拥有源节点时不应看到沙箱演练证据，期望 drill_missing，实际: %+v evidence=%+v", item.Reasons, item.Evidence)
	}
	for _, ev := range item.Evidence {
		if ev.Type == "drill" && ev.Status != "missing" {
			t.Fatalf("不应返回 unowned-sandbox drill 证据: %+v", ev)
		}
	}
}
