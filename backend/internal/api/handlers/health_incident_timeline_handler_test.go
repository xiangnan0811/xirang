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

func openHealthIncidentTimelineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func migrateHealthIncidentTimelineTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.Node{},
		&model.NodeOwner{},
		&model.Policy{},
		&model.Task{},
		&model.TaskRun{},
		&model.Alert{},
		&model.AlertDelivery{},
		&model.NodeMetricSample{},
		&model.AnomalyEvent{},
	); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
}

func callHealthIncidentTimeline(t *testing.T, db *gorm.DB, role string, userID uint) (*httptest.ResponseRecorder, healthIncidentTimelineResponse) {
	t.Helper()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
	r.GET("/overview/health-incident-timeline", NewHealthIncidentTimelineHandler(db).Get)

	req := httptest.NewRequest(http.MethodGet, "/overview/health-incident-timeline?window_hours=72", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var result struct {
		Data healthIncidentTimelineResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v, body: %s", err, resp.Body.String())
	}
	return resp, result.Data
}

func healthIncidentHasSource(group healthIncidentGroup, sourceType string) bool {
	for _, one := range group.SourceTypes {
		if one == sourceType {
			return true
		}
	}
	return false
}

func healthIncidentHasAction(group healthIncidentGroup) bool {
	for _, action := range group.NextActions {
		if strings.TrimSpace(action.Code) != "" && strings.TrimSpace(action.Href) != "" {
			return true
		}
	}
	return false
}

func TestHealthIncidentTimelineAggregatesSortsSeverityAndTaskResource(t *testing.T) {
	db := openHealthIncidentTimelineTestDB(t)
	migrateHealthIncidentTimelineTables(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	recentBackup := now.Add(-1 * time.Hour)
	nodeA := model.Node{Name: "node-a", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-a", LastBackupAt: &recentBackup}
	nodeB := model.Node{Name: "node-b", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-b", LastBackupAt: &recentBackup}
	if err := db.Create(&nodeA).Error; err != nil {
		t.Fatalf("创建节点 A 失败: %v", err)
	}
	if err := db.Create(&nodeB).Error; err != nil {
		t.Fatalf("创建节点 B 失败: %v", err)
	}

	policy := model.Policy{Name: "daily-backup", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 * * * *", Enabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	task := model.Task{Name: "backup-node-a", NodeID: nodeA.ID, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	failureTime := now.Add(-2 * time.Hour)
	run := model.TaskRun{TaskID: task.ID, Status: "failed", LastError: "rsync exited with code 23", CreatedAt: failureTime, UpdatedAt: failureTime, FinishedAt: &failureTime}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建失败 task_run 失败: %v", err)
	}

	alertTime := now.Add(-1 * time.Hour)
	alert := model.Alert{NodeID: nodeA.ID, NodeName: nodeA.Name, TaskID: &task.ID, TaskRunID: &run.ID, PolicyName: policy.Name, Severity: "warning", Status: "open", ErrorCode: "XR-TASK-FAILED", Message: "任务失败告警", TriggeredAt: alertTime}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	metricTime := now.Add(-10 * time.Minute)
	metric := model.NodeMetricSample{NodeID: nodeB.ID, CpuPct: 12, MemPct: 20, DiskPct: 96, ProbeOK: true, SampledAt: metricTime}
	if err := db.Create(&metric).Error; err != nil {
		t.Fatalf("创建指标样本失败: %v", err)
	}

	resp, data := callHealthIncidentTimeline(t, db, "admin", 1)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	if data.Summary.Total != 2 || data.Summary.Critical != 2 {
		t.Fatalf("期望 2 个 critical 事件组，实际 summary=%+v", data.Summary)
	}
	if len(data.Groups) != 2 {
		t.Fatalf("期望 2 个事件组，实际: %d", len(data.Groups))
	}
	if data.Groups[0].Resource.Type != "node" || data.Groups[0].Resource.ID != nodeB.ID {
		t.Fatalf("期望最新事件组为 node-b 指标异常，实际: %+v", data.Groups[0].Resource)
	}
	if !data.Groups[0].LastSeenAt.After(data.Groups[1].LastSeenAt) {
		t.Fatalf("事件组应按 last_seen_at 倒序排列: %v <= %v", data.Groups[0].LastSeenAt, data.Groups[1].LastSeenAt)
	}

	taskGroup := data.Groups[1]
	if taskGroup.Resource.Type != "task" || taskGroup.Resource.ID != task.ID {
		t.Fatalf("任务失败应通过 task_runs -> tasks 关联到 task 资源，实际: %+v", taskGroup.Resource)
	}
	if taskGroup.Resource.NodeID != nodeA.ID || taskGroup.Resource.PolicyID != policy.ID {
		t.Fatalf("任务资源应携带 node/policy 关联，实际: %+v", taskGroup.Resource)
	}
	if taskGroup.Severity != "critical" {
		t.Fatalf("warning 告警 + critical task_failure 应升级为 critical，实际: %s", taskGroup.Severity)
	}
	if taskGroup.EventCount != 2 || !healthIncidentHasSource(taskGroup, "alert") || !healthIncidentHasSource(taskGroup, "task_failure") {
		t.Fatalf("任务组应合并 alert 与 task_failure，实际 count=%d sources=%v", taskGroup.EventCount, taskGroup.SourceTypes)
	}
	if !strings.Contains(taskGroup.LikelyCause, "rsync exited") {
		t.Fatalf("likely_cause 应来自更高严重度的任务失败，实际: %s", taskGroup.LikelyCause)
	}
	if !healthIncidentHasAction(taskGroup) {
		t.Fatalf("每个事件组应至少包含一个 next action: %+v", taskGroup.NextActions)
	}
}

func TestHealthIncidentTimelineOperatorOwnershipFiltersNodeScopedAndPlatformAlerts(t *testing.T) {
	db := openHealthIncidentTimelineTestDB(t)
	migrateHealthIncidentTimelineTables(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	recentBackup := now.Add(-1 * time.Hour)
	ownedNode := model.Node{Name: "owned-node", Host: "10.0.1.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "owned-node", LastBackupAt: &recentBackup}
	otherNode := model.Node{Name: "other-node", Host: "10.0.1.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "other-node", LastBackupAt: &recentBackup}
	if err := db.Create(&ownedNode).Error; err != nil {
		t.Fatalf("创建 owned node 失败: %v", err)
	}
	if err := db.Create(&otherNode).Error; err != nil {
		t.Fatalf("创建 other node 失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: 99}).Error; err != nil {
		t.Fatalf("创建节点 owner 失败: %v", err)
	}

	alerts := []model.Alert{
		{NodeID: ownedNode.ID, NodeName: ownedNode.Name, Severity: "warning", Status: "open", ErrorCode: "XR-OWNED", Message: "owned alert", TriggeredAt: now.Add(-10 * time.Minute)},
		{NodeID: otherNode.ID, NodeName: otherNode.Name, Severity: "critical", Status: "open", ErrorCode: "XR-OTHER", Message: "other alert", TriggeredAt: now.Add(-5 * time.Minute)},
		{NodeID: 0, NodeName: "status-page", Severity: "critical", Status: "open", ErrorCode: "XR-SERVICE-DOWN", Message: "service down", TriggeredAt: now.Add(-1 * time.Minute)},
	}
	if err := db.Create(&alerts).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	resp, data := callHealthIncidentTimeline(t, db, "operator", 99)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	if data.Summary.Total != 1 || len(data.Groups) != 1 {
		t.Fatalf("operator 只能看到已负责节点事件，实际 summary=%+v groups=%+v", data.Summary, data.Groups)
	}
	if data.Groups[0].Resource.Type != "node" || data.Groups[0].Resource.ID != ownedNode.ID {
		t.Fatalf("operator 不应看到未负责节点或 node_id=0 平台告警，实际: %+v", data.Groups[0].Resource)
	}
}

func TestHealthIncidentTimelineOperatorWithNoOwnedNodesReturnsEmpty(t *testing.T) {
	db := openHealthIncidentTimelineTestDB(t)
	migrateHealthIncidentTimelineTables(t, db)

	alert := model.Alert{NodeID: 0, NodeName: "platform", Severity: "critical", Status: "open", ErrorCode: "XR-PLATFORM", Message: "platform alert", TriggeredAt: time.Now().UTC()}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建平台告警失败: %v", err)
	}

	resp, data := callHealthIncidentTimeline(t, db, "operator", 404)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	if data.Summary.Total != 0 || len(data.Groups) != 0 {
		t.Fatalf("无 owner 的 operator 应返回空时间线，实际 summary=%+v groups=%+v", data.Summary, data.Groups)
	}
}
