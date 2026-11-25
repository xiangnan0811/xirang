package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backuphealth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openHealthIncidentTimelineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
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
		&model.BackupCompletion{},
		&model.Alert{},
		&model.AlertDelivery{},
		&model.AnomalyEvent{},
	); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
}
func recordTimelineCompletion(t *testing.T, db *gorm.DB, task model.Task, run model.TaskRun, completedAt time.Time) {
	t.Helper()
	if err := db.Transaction(func(tx *gorm.DB) error {
		return backuphealth.RecordLegacyTransferTx(context.Background(), tx, backuphealth.LegacyTransferInput{
			TaskID:       task.ID,
			TaskRunID:    run.ID,
			NodeID:       run.NodeIDSnapshot,
			ExecutorType: run.ExecutorTypeSnapshot,
			CompletedAt:  completedAt,
		})
	}); err != nil {
		t.Fatalf("创建备份完成事实失败: %v", err)
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
	freshStarted := recentBackup.Add(-5 * time.Minute)
	freshRunA := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: nodeA.ID, ExecutorTypeSnapshot: "rsync",
		TriggerType: "cron", Status: "success", StartedAt: &freshStarted,
		FinishedAt: &recentBackup, CreatedAt: recentBackup, UpdatedAt: recentBackup,
	}
	if err := db.Create(&freshRunA).Error; err != nil {
		t.Fatalf("创建节点 A 成功 task_run 失败: %v", err)
	}
	recordTimelineCompletion(t, db, task, freshRunA, recentBackup)
	taskB := model.Task{Name: "backup-node-b", NodeID: nodeB.ID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&taskB).Error; err != nil {
		t.Fatalf("创建任务 B 失败: %v", err)
	}
	freshRunB := model.TaskRun{
		TaskID: taskB.ID, NodeIDSnapshot: nodeB.ID, ExecutorTypeSnapshot: "rsync",
		TriggerType: "cron", Status: "success", StartedAt: &freshStarted,
		FinishedAt: &recentBackup, CreatedAt: recentBackup, UpdatedAt: recentBackup,
	}
	if err := db.Create(&freshRunB).Error; err != nil {
		t.Fatalf("创建节点 B 成功 task_run 失败: %v", err)
	}
	recordTimelineCompletion(t, db, taskB, freshRunB, recentBackup)

	run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: nodeA.ID, ExecutorTypeSnapshot: "rsync", Status: "failed", LastError: "rsync exited with code 23", CreatedAt: failureTime, UpdatedAt: failureTime, FinishedAt: &failureTime}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建失败 task_run 失败: %v", err)
	}

	alertTime := now.Add(-1 * time.Hour)
	alert := model.Alert{NodeID: nodeA.ID, NodeName: nodeA.Name, TaskID: &task.ID, TaskRunID: &run.ID, PolicyName: policy.Name, Severity: "warning", Status: "open", ErrorCode: "XR-TASK-FAILED", Message: "任务失败告警", TriggeredAt: alertTime}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	anomalyTime := now.Add(-10 * time.Minute)
	anomaly := model.AnomalyEvent{
		NodeID: nodeB.ID, Detector: "snapshot_diff", Metric: "content",
		Severity: "critical", ObservedValue: 96, BaselineValue: 20,
		Details: "{}", FiredAt: anomalyTime,
	}
	if err := db.Create(&anomaly).Error; err != nil {
		t.Fatalf("创建 snapshot-diff 异常事件失败: %v", err)
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
		t.Fatalf("期望最新事件组为 node-b 异常，实际: %+v", data.Groups[0].Resource)
	}
	if !data.Groups[0].LastSeenAt.After(data.Groups[1].LastSeenAt) {
		t.Fatalf("事件组应按 last_seen_at 倒序排列: %v <= %v", data.Groups[0].LastSeenAt, data.Groups[1].LastSeenAt)
	}
	if !healthIncidentHasSource(data.Groups[0], "anomaly") {
		t.Fatalf("snapshot-diff 应作为 anomaly 来源保留，实际 sources=%v", data.Groups[0].SourceTypes)
	}
	for _, group := range data.Groups {
		for _, sourceType := range group.SourceTypes {
			if sourceType == "probe" || sourceType == "metric" {
				t.Fatalf("退役资源来源不应出现在时间线: %v", sourceType)
			}
		}
		for _, action := range group.NextActions {
			if action.Code == "view_node_metrics" {
				t.Fatal("时间线不应提供已退役节点指标动作")
			}
		}
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
	if taskGroup.CauseCode != healthIncidentCauseTaskExecutionFailed || taskGroup.CauseParams != nil {
		t.Fatalf("task failure cause metadata must match the winning diagnostic: %+v", taskGroup)
	}
	if data.Groups[0].CauseCode != healthIncidentCauseAnomalyDetected || data.Groups[0].CauseParams == nil ||
		*data.Groups[0].CauseParams.ObservedValue != 96 || *data.Groups[0].CauseParams.BaselineValue != 20 {
		t.Fatalf("anomaly cause metadata missing: %+v", data.Groups[0])
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
		{NodeID: 0, NodeName: "platform", Severity: "critical", Status: "open", ErrorCode: "XR-PLATFORM-TEST", Message: "platform alert", TriggeredAt: now.Add(-1 * time.Minute)},
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

	alert := model.Alert{NodeID: 0, NodeName: "platform", Severity: "critical", Status: "open", ErrorCode: "XR-PLATFORM-TEST", Message: "platform alert", TriggeredAt: time.Now().UTC()}
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
func TestHealthIncidentTimelineExcludesRetiredDeliveriesBeforeLimit(t *testing.T) {
	db := openHealthIncidentTimelineTestDB(t)
	migrateHealthIncidentTimelineTables(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	const retiredCount = maxHealthIncidentSourceRows
	retiredAlerts := make([]model.Alert, retiredCount)
	for i := range retiredAlerts {
		createdAt := now.Add(-time.Duration(i+1) * time.Minute)
		retiredAlerts[i] = model.Alert{
			NodeID:            1,
			NodeName:          "retired-node",
			Severity:          "critical",
			Status:            "resolved",
			ErrorCode:         "XR-NODE-1",
			Message:           "retired node probe failure",
			TriggeredAt:       createdAt,
			DeliveryDecision:  model.AlertDeliveryDecisionUnknown,
			DeliveryReason:    "feature_retired",
			DeliveryDecidedAt: &createdAt,
			CreatedAt:         createdAt,
			UpdatedAt:         createdAt,
		}
	}
	if err := db.Create(&retiredAlerts).Error; err != nil {
		t.Fatalf("创建退役告警失败: %v", err)
	}

	retiredDeliveries := make([]model.AlertDelivery, retiredCount)
	retiredDeliveryIDs := make(map[uint]struct{}, retiredCount)
	for i := range retiredDeliveries {
		createdAt := retiredAlerts[i].CreatedAt
		retiredDeliveries[i] = model.AlertDelivery{
			AlertID:       retiredAlerts[i].ID,
			IntegrationID: 1,
			Status:        model.AlertDeliveryStatusFailed,
			Decision:      model.AlertDeliveryDecisionUnknown,
			AttemptCount:  1,
			LastError:     "retired transport failure",
			CreatedAt:     createdAt,
			UpdatedAt:     createdAt,
		}
	}
	if err := db.Create(&retiredDeliveries).Error; err != nil {
		t.Fatalf("创建退役投递失败: %v", err)
	}
	for _, delivery := range retiredDeliveries {
		retiredDeliveryIDs[delivery.ID] = struct{}{}
	}

	genuineTime := now.Add(-12 * time.Hour)
	genuineAlert := model.Alert{
		NodeID:           2,
		NodeName:         "retained-node",
		Severity:         "critical",
		Status:           "resolved",
		ErrorCode:        "XR-TASK-FAILED",
		Message:          "retained notification failure",
		TriggeredAt:      genuineTime,
		DeliveryDecision: model.AlertDeliveryDecisionUnknown,
		DeliveryReason:   "transport_failure",
		CreatedAt:        genuineTime,
		UpdatedAt:        genuineTime,
	}
	if err := db.Create(&genuineAlert).Error; err != nil {
		t.Fatalf("创建保留告警失败: %v", err)
	}
	genuineDelivery := model.AlertDelivery{
		AlertID:       genuineAlert.ID,
		IntegrationID: 1,
		Status:        model.AlertDeliveryStatusFailed,
		Decision:      model.AlertDeliveryDecisionUnknown,
		AttemptCount:  2,
		LastError:     "retained transport failure",
		CreatedAt:     genuineTime,
		UpdatedAt:     genuineTime,
	}
	if err := db.Create(&genuineDelivery).Error; err != nil {
		t.Fatalf("创建保留投递失败: %v", err)
	}

	resp, data := callHealthIncidentTimeline(t, db, "admin", 1)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var foundGenuine bool
	for _, group := range data.Groups {
		for _, signal := range group.Signals {
			if signal.Type != "notification_failure" {
				continue
			}
			if _, retired := retiredDeliveryIDs[signal.DeliveryID]; retired {
				t.Fatalf("退役投递不应出现在时间线: delivery_id=%d", signal.DeliveryID)
			}
			if signal.DeliveryID == genuineDelivery.ID {
				foundGenuine = true
			}
		}
	}
	if !foundGenuine {
		t.Fatalf("保留的 unknown 决策通知失败应可见，实际 groups=%+v", data.Groups)
	}
}

func TestHealthIncidentCauseWinnerIsAtomicBeyondSignalLimit(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	acc := newHealthIncidentAccumulator(now)
	resource := nodeHealthIncidentResource(1, "node")
	for i := range maxHealthIncidentSignalsPerGroup {
		acc.addSignal(resource, healthIncidentSignal{Type: "alert", Severity: "warning", OccurredAt: now.Add(time.Duration(i) * time.Minute)},
			healthIncidentCause{Text: "健康信号需要关注", Code: healthIncidentCauseAlertTriggered}, nil)
	}
	zero := 0.0
	params := &healthIncidentCauseParams{ObservedValue: &zero, BaselineValue: &zero}
	acc.addSignal(resource, healthIncidentSignal{Type: "anomaly", Severity: "critical", OccurredAt: now.Add(-time.Hour)},
		healthIncidentCause{Text: "sixth winner", Code: healthIncidentCauseSnapshotChurnAnomaly, Params: params}, nil)
	group := acc.response(72).Groups[0]
	if len(group.Signals) != 5 || group.EventCount != 6 || group.LikelyCause != "sixth winner" ||
		group.CauseCode != healthIncidentCauseSnapshotChurnAnomaly || group.CauseParams != params {
		t.Fatalf("sixth signal must replace all cause fields without changing signal cap: %+v", group)
	}
	acc.addSignal(resource, healthIncidentSignal{Type: "alert", Severity: "critical", OccurredAt: now},
		healthIncidentCause{Text: "newer same severity", Code: healthIncidentCauseAlertTriggered}, nil)
	acc.addSignal(resource, healthIncidentSignal{Type: "task_failure", Severity: "critical", OccurredAt: now.Add(time.Hour)},
		healthIncidentCause{Text: " ", Code: healthIncidentCauseTaskExecutionFailed, Params: params}, nil)
	acc.addSignal(resource, healthIncidentSignal{Type: "anomaly", Severity: "warning", OccurredAt: now.Add(2 * time.Hour)},
		healthIncidentCause{Text: "lower severity", Code: healthIncidentCauseAnomalyDetected, Params: params}, nil)
	group = acc.response(72).Groups[0]
	if group.LikelyCause != "newer same severity" || group.CauseCode != healthIncidentCauseAlertTriggered || group.CauseParams != nil {
		t.Fatalf("new winner must clear old params; empty/lower severity cannot replace it: %+v", group)
	}
	empty := newHealthIncidentAccumulator(now)
	empty.addSignal(resource, healthIncidentSignal{Type: "alert"}, healthIncidentCause{}, nil)
	if group := empty.response(72).Groups[0]; group.CauseCode != healthIncidentCauseHealthAttentionRequired || group.CauseParams != nil {
		t.Fatalf("unselected cause must keep generic classification: %+v", group)
	}
}

func TestHealthIncidentTimelineCauseProducers(t *testing.T) {
	for _, diagnostic := range []string{"", "original safe diagnostic"} {
		t.Run("alert/"+diagnostic, func(t *testing.T) {
			db := openHealthIncidentTimelineTestDB(t)
			migrateHealthIncidentTimelineTables(t, db)
			alert := model.Alert{Status: "open", Severity: "critical", Message: diagnostic, TriggeredAt: time.Now().UTC()}
			if err := db.Create(&alert).Error; err != nil {
				t.Fatal(err)
			}
			resp, data := callHealthIncidentTimeline(t, db, "admin", 1)
			if resp.Code != http.StatusOK || len(data.Groups) != 1 || data.Groups[0].CauseCode != healthIncidentCauseAlertTriggered {
				t.Fatalf("alert classification must not depend on raw message: %s", resp.Body.String())
			}
			if diagnostic != "" && data.Groups[0].LikelyCause != diagnostic {
				t.Fatal("raw diagnostic lost")
			}
		})
		t.Run("task/"+diagnostic, func(t *testing.T) {
			db := openHealthIncidentTimelineTestDB(t)
			migrateHealthIncidentTimelineTables(t, db)
			task := model.Task{Name: "backup", NodeID: 1, ExecutorType: "rsync"}
			if err := db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: 1, Status: "failed", LastError: diagnostic, CreatedAt: now, UpdatedAt: now}
			if err := db.Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			resp, data := callHealthIncidentTimeline(t, db, "admin", 1)
			if resp.Code != http.StatusOK || len(data.Groups) != 1 || data.Groups[0].CauseCode != healthIncidentCauseTaskExecutionFailed {
				t.Fatalf("task classification must not depend on raw error: %s", resp.Body.String())
			}
			if diagnostic != "" && data.Groups[0].LikelyCause != diagnostic {
				t.Fatal("raw diagnostic lost")
			}
		})
	}
	for _, status := range []string{"failed", "retrying"} {
		t.Run("delivery/"+status, func(t *testing.T) {
			db := openHealthIncidentTimelineTestDB(t)
			migrateHealthIncidentTimelineTables(t, db)
			now := time.Now().UTC()
			alert := model.Alert{Status: "resolved", Severity: "warning", TriggeredAt: now}
			if err := db.Create(&alert).Error; err != nil {
				t.Fatal(err)
			}
			delivery := model.AlertDelivery{AlertID: alert.ID, IntegrationID: 1, Status: status, CreatedAt: now}
			if err := db.Create(&delivery).Error; err != nil {
				t.Fatal(err)
			}
			resp, data := callHealthIncidentTimeline(t, db, "admin", 1)
			if resp.Code != http.StatusOK || len(data.Groups) != 1 {
				t.Fatalf("delivery missing: %s", resp.Body.String())
			}
			group := data.Groups[0]
			if group.CauseCode != healthIncidentCauseNotificationDeliveryIssue || group.CauseParams == nil || group.CauseParams.Status != status {
				t.Fatalf("wrong delivery classification: %+v", group)
			}
		})
	}
	for _, tc := range []struct {
		detector string
		metric   string
		code     healthIncidentCauseCode
	}{
		{"snapshot_diff", "snapshot_churn", healthIncidentCauseSnapshotChurnAnomaly},
		{"snapshot_diff", "ransomware_pattern", healthIncidentCauseSnapshotRansomwarePattern},
		{"snapshot_diff", "other", healthIncidentCauseAnomalyDetected},
		{"other", "snapshot_churn", healthIncidentCauseAnomalyDetected},
	} {
		t.Run(tc.detector+"/"+tc.metric, func(t *testing.T) {
			db := openHealthIncidentTimelineTestDB(t)
			migrateHealthIncidentTimelineTables(t, db)
			row := model.AnomalyEvent{NodeID: 1, Detector: tc.detector, Metric: tc.metric, Severity: "critical", ObservedValue: 1.5, BaselineValue: 0, Details: "{}", FiredAt: time.Now().UTC()}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			resp, data := callHealthIncidentTimeline(t, db, "admin", 1)
			if resp.Code != http.StatusOK || len(data.Groups) != 1 {
				t.Fatalf("anomaly missing: %s", resp.Body.String())
			}
			group := data.Groups[0]
			if group.CauseCode != tc.code || group.CauseParams == nil || group.CauseParams.BaselineValue == nil || *group.CauseParams.BaselineValue != 0 {
				t.Fatalf("wrong anomaly classification or lost zero baseline: %+v", group)
			}
		})
	}
}

func TestHealthIncidentAnomalyOmitsInvalidMeasurements(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 9007199254740992, -9007199254740992} {
		cause := anomalyHealthIncidentCause(healthIncidentAnomalyRow{ObservedValue: value, BaselineValue: value}, "original diagnostic")
		if cause.Params != nil || cause.Text != "original diagnostic" {
			t.Fatalf("invalid measurements must be absent without losing diagnostic: %+v", cause)
		}
		if _, err := json.Marshal(cause); err != nil {
			t.Fatalf("non-finite measurements must never break JSON: %v", err)
		}
	}
	for _, value := range []float64{0, -1.5, 9007199254740991, -9007199254740991} {
		if got := healthIncidentMeasurement(value); got == nil || *got != value {
			t.Fatalf("valid measurement lost: %v", value)
		}
	}
}

func TestHealthIncidentBackupCauseProducers(t *testing.T) {
	db := openHealthIncidentTimelineTestDB(t)
	migrateHealthIncidentTimelineTables(t, db)
	t.Setenv("BACKUP_STALE_THRESHOLD_HOURS", "48")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	missing := model.Node{Name: "missing", Host: "127.0.0.1", Port: 22, Username: "fixture", AuthType: "key", BackupDir: "/fixture/missing"}
	stale := model.Node{Name: "stale", Host: "127.0.0.1", Port: 22, Username: "fixture", AuthType: "key", BackupDir: "/fixture/stale"}
	for _, node := range []*model.Node{&missing, &stale} {
		if err := db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	policy := model.Policy{Name: "policy", Enabled: true, SourcePath: "/fixture", TargetPath: "/fixture", CronSpec: "0 * * * *"}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{Name: "backup", NodeID: stale.ID, PolicyID: &policy.ID, ExecutorType: "rsync"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	completed := now.Add(-72 * time.Hour)
	run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: stale.ID, ExecutorTypeSnapshot: "rsync", TriggerType: "cron", Status: "success", CreatedAt: completed, UpdatedAt: completed, FinishedAt: &completed}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	recordTimelineCompletion(t, db, task, run, completed)
	for i := range 3 {
		failedAt := now.Add(-time.Duration(i+1) * time.Hour)
		failed := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: stale.ID, ExecutorTypeSnapshot: "rsync", TriggerType: "cron", Status: "failed", CreatedAt: failedAt, UpdatedAt: failedAt}
		if err := db.Create(&failed).Error; err != nil {
			t.Fatal(err)
		}
	}
	acc := newHealthIncidentAccumulator(now)
	handler := NewHealthIncidentTimelineHandler(db)
	if err := handler.addBackupStaleSignals(db, acc, now, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := handler.addDegradedPolicySignals(db, acc, nil, false); err != nil {
		t.Fatal(err)
	}
	codes := map[healthIncidentCauseCode]healthIncidentGroup{}
	for _, group := range acc.response(72).Groups {
		codes[group.CauseCode] = group
		for _, action := range group.NextActions {
			if action.Code == "view_backup_confidence" && action.Href != "/app/backups/overview" {
				t.Fatalf("confidence action must use read-only overview: %+v", action)
			}
		}
	}
	if _, ok := codes[healthIncidentCauseVerifiedBackupMissing]; !ok {
		t.Fatal("missing verified backup classification absent")
	}
	if group, ok := codes[healthIncidentCauseVerifiedBackupStale]; !ok || group.CauseParams == nil ||
		group.CauseParams.ElapsedHours == nil || *group.CauseParams.ElapsedHours != 72 {
		t.Fatalf("stale backup hours missing: %+v", group)
	}
	if group, ok := codes[healthIncidentCausePolicyBackupFailures]; !ok || group.CauseParams != nil {
		t.Fatalf("three eligible failures require fixed cause without failure_count: %+v", group)
	}
}

// TestHealthIncidentTimelineDegradedPolicyRanksLatestThreePerPolicy drives the
// real handler on both engines. Unrelated low-ID policy history must not crowd
// a later policy out of degradation, operator ownership applies before the
// per-policy latest-three ranking, and equal created_at ties prefer higher IDs.
func TestHealthIncidentTimelineDegradedPolicyRanksLatestThreePerPolicy(t *testing.T) {
	type targetRun struct {
		ago     time.Duration
		failed  bool
		unowned bool
	}
	failedAt := func(hours ...int) []targetRun {
		runs := make([]targetRun, 0, len(hours))
		for _, h := range hours {
			runs = append(runs, targetRun{ago: time.Duration(h) * time.Hour, failed: true})
		}
		return runs
	}
	cases := []struct {
		name         string
		lowIDRuns    int
		operator     bool
		runs         []targetRun // creation order; later entries receive higher IDs
		wantDegraded bool
		wantAt       time.Duration
	}{
		{name: "control_997_low_id_runs", lowIDRuns: 997, runs: failedAt(1, 2, 3), wantDegraded: true, wantAt: time.Hour},
		{name: "crowding_1000_low_id_runs", lowIDRuns: 1000, runs: failedAt(1, 2, 3), wantDegraded: true, wantAt: time.Hour},
		{name: "older_fourth_success_ignored", runs: append(failedAt(1, 2, 3), targetRun{ago: 4 * time.Hour}), wantDegraded: true, wantAt: time.Hour},
		{name: "newest_success_breaks_streak", runs: append([]targetRun{{ago: time.Hour}}, failedAt(2, 3, 4)...)},
		{name: "equal_time_higher_id_failed", runs: append(failedAt(1, 2), targetRun{ago: 3 * time.Hour}, targetRun{ago: 3 * time.Hour, failed: true}), wantDegraded: true, wantAt: time.Hour},
		{name: "equal_time_higher_id_success", runs: append(failedAt(1, 2, 3), targetRun{ago: 3 * time.Hour})},
		{name: "operator_unowned_newer_run_filtered_before_rank", operator: true, runs: append([]targetRun{{ago: time.Hour, unowned: true}}, failedAt(2, 3, 4)...), wantDegraded: true, wantAt: 2 * time.Hour},
	}
	for _, engine := range []struct {
		name string
		open func(*testing.T) *gorm.DB
	}{
		{"sqlite", openHealthIncidentTimelineTestDB},
		{"postgres", openBackupHealthPostgresTestDB},
	} {
		for _, tc := range cases {
			t.Run(engine.name+"/"+tc.name, func(t *testing.T) {
				db := engine.open(t)
				migrateHealthIncidentTimelineTables(t, db)

				now := time.Now().UTC().Truncate(time.Second)
				ownedNode := model.Node{Name: "incident-node", Host: "127.0.0.1", Port: 22, Username: "fixture", AuthType: "key", BackupDir: "/fixture/incident"}
				otherNode := model.Node{Name: "incident-other-node", Host: "127.0.0.2", Port: 22, Username: "fixture", AuthType: "key", BackupDir: "/fixture/other"}
				for _, node := range []*model.Node{&ownedNode, &otherNode} {
					if err := db.Create(node).Error; err != nil {
						t.Fatalf("create node %s: %v", node.Name, err)
					}
				}
				role, userID := "admin", uint(1)
				if tc.operator {
					if err := db.AutoMigrate(&model.User{}); err != nil {
						t.Fatalf("migrate users: %v", err)
					}
					operator := model.User{Username: "incident-operator", Role: "operator", PasswordHash: "x"}
					if err := db.Create(&operator).Error; err != nil {
						t.Fatalf("create operator: %v", err)
					}
					if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: operator.ID}).Error; err != nil {
						t.Fatalf("create node owner: %v", err)
					}
					role, userID = "operator", operator.ID
				}
				lowPolicy := model.Policy{Name: "incident-low-policy", SourcePath: "/fixture/source", TargetPath: "/fixture/target", CronSpec: "0 * * * *", Enabled: true, IsTemplate: false}
				highPolicy := model.Policy{Name: "incident-high-policy", SourcePath: "/fixture/source", TargetPath: "/fixture/target", CronSpec: "0 * * * *", Enabled: true, IsTemplate: false}
				for _, policy := range []*model.Policy{&lowPolicy, &highPolicy} {
					if err := db.Create(policy).Error; err != nil {
						t.Fatalf("create policy %s: %v", policy.Name, err)
					}
				}
				if lowPolicy.ID >= highPolicy.ID {
					t.Fatalf("fixture requires increasing policy IDs: low=%d high=%d", lowPolicy.ID, highPolicy.ID)
				}
				lowTask := model.Task{Name: "incident-low-task", NodeID: ownedNode.ID, PolicyID: &lowPolicy.ID, ExecutorType: "rsync", Status: "success"}
				highTask := model.Task{Name: "incident-high-task", NodeID: ownedNode.ID, PolicyID: &highPolicy.ID, ExecutorType: "rsync", Status: "failed"}
				otherTask := model.Task{Name: "incident-other-task", NodeID: otherNode.ID, PolicyID: &highPolicy.ID, ExecutorType: "rsync", Status: "success"}
				for _, task := range []*model.Task{&lowTask, &highTask, &otherTask} {
					if err := db.Create(task).Error; err != nil {
						t.Fatalf("create task %s: %v", task.Name, err)
					}
				}

				// Successful transfers without completion evidence classify as incomplete,
				// and thus cannot create an unrelated low-policy degraded signal.
				if tc.lowIDRuns > 0 {
					old := now.Add(-10 * 24 * time.Hour)
					lowRuns := make([]model.TaskRun, tc.lowIDRuns)
					for i := range lowRuns {
						lowRuns[i] = model.TaskRun{
							TaskID: lowTask.ID, NodeIDSnapshot: ownedNode.ID, ExecutorTypeSnapshot: "rsync",
							TriggerType: "cron", Status: "success", CreatedAt: old, UpdatedAt: old, FinishedAt: &old,
						}
					}
					if err := db.CreateInBatches(&lowRuns, 100).Error; err != nil {
						t.Fatalf("create %d low-ID runs: %v", tc.lowIDRuns, err)
					}
				}
				for i, one := range tc.runs {
					at := now.Add(-one.ago)
					run := model.TaskRun{
						TaskID: highTask.ID, NodeIDSnapshot: ownedNode.ID, ExecutorTypeSnapshot: "rsync",
						TriggerType: "cron", Status: "success", CreatedAt: at, UpdatedAt: at, FinishedAt: &at,
					}
					if one.unowned {
						run.TaskID, run.NodeIDSnapshot = otherTask.ID, otherNode.ID
					}
					if one.failed {
						run.Status = "failed"
					}
					if err := db.Create(&run).Error; err != nil {
						t.Fatalf("create target run %d: %v", i, err)
					}
				}

				resp, result := callHealthIncidentTimeline(t, db, role, userID)
				if resp.Code != http.StatusOK {
					t.Fatalf("timeline status=%d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
				}
				var degraded []healthIncidentSignal
				var failureGroups int
				for _, group := range result.Groups {
					if group.Resource.Type == "policy" && group.Resource.ID == highPolicy.ID {
						for _, signal := range group.Signals {
							if signal.Type == "backup_degraded" {
								degraded = append(degraded, signal)
							}
						}
					}
					if group.Resource.Type == "task" && group.Resource.ID == highTask.ID && healthIncidentHasSource(group, "task_failure") {
						failureGroups++
					}
				}
				counts := fmt.Sprintf("low-ID runs=%d, target backup_degraded signals=%d, target task_failure groups=%d, all groups=%d", tc.lowIDRuns, len(degraded), failureGroups, len(result.Groups))
				if failureGroups != 1 {
					t.Fatalf("expected the target task's separate task_failure group; %s", counts)
				}
				if !tc.wantDegraded {
					if len(degraded) != 0 {
						t.Fatalf("latest three ranked runs are not all failed; %s", counts)
					}
					return
				}
				if len(degraded) != 1 {
					t.Fatalf("expected one backup_degraded signal for the target policy; %s", counts)
				}
				if want := now.Add(-tc.wantAt); !degraded[0].OccurredAt.Equal(want) || degraded[0].TaskID != highTask.ID || degraded[0].NodeID != ownedNode.ID {
					t.Fatalf("backup_degraded must cite the most recent ranked run at %s on task %d node %d: %+v", want, highTask.ID, ownedNode.ID, degraded[0])
				}
			})
		}
	}
}

// TestHealthIncidentTimelineSourceTruncation exercises the real handler at the
// cap, including equal-time ordering and filters applied before the cap.
func TestHealthIncidentTimelineSourceTruncation(t *testing.T) {
	for _, engine := range []struct {
		name string
		open func(*testing.T) *gorm.DB
	}{
		{"sqlite", openHealthIncidentTimelineTestDB},
		{"postgres", openBackupHealthPostgresTestDB},
	} {
		for _, source := range []string{"alert", "task_failure", "notification_failure", "anomaly"} {
			t.Run(engine.name+"/"+source, func(t *testing.T) {
				db := engine.open(t)
				migrateHealthIncidentTimelineTables(t, db)
				if err := db.AutoMigrate(&model.User{}); err != nil {
					t.Fatalf("migrate user: %v", err)
				}
				user := model.User{Username: "cap-operator", Role: "operator", PasswordHash: "x"}
				if err := db.Create(&user).Error; err != nil {
					t.Fatalf("create user: %v", err)
				}
				nodes := []model.Node{
					{Name: "cap-retained", Host: "a", Username: "test", BackupDir: "cap-a", Archived: true},
					{Name: "cap-first", Host: "b", Username: "test", BackupDir: "cap-b", Archived: true},
					{Name: "cap-unowned", Host: "c", Username: "test", BackupDir: "cap-c", Archived: true},
				}
				if err := db.Create(&nodes).Error; err != nil {
					t.Fatalf("create nodes: %v", err)
				}
				owners := []model.NodeOwner{{NodeID: nodes[0].ID, UserID: user.ID}, {NodeID: nodes[1].ID, UserID: user.ID}}
				if err := db.Create(&owners).Error; err != nil {
					t.Fatalf("create owners: %v", err)
				}
				tasks := []model.Task{
					{Name: "cap-retained-task", NodeID: nodes[0].ID, ExecutorType: "rsync", Status: "failed"},
					{Name: "cap-first-task", NodeID: nodes[1].ID, ExecutorType: "rsync", Status: "failed"},
					{Name: "cap-unowned-task", NodeID: nodes[2].ID, ExecutorType: "rsync", Status: "failed"},
				}
				if err := db.Create(&tasks).Error; err != nil {
					t.Fatalf("create tasks: %v", err)
				}
				at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
				old := at.Add(-7 * 24 * time.Hour)
				insert := func(nodeID, taskID uint, when time.Time, eligible bool, count int) {
					t.Helper()
					switch source {
					case "alert":
						rows := make([]model.Alert, count)
						for i := range rows {
							status := "open"
							if !eligible {
								status = "resolved"
							}
							rows[i] = model.Alert{NodeID: nodeID, NodeName: "node", Severity: "warning", Status: status, ErrorCode: "CAP", Message: "alert", TriggeredAt: when}
						}
						if err := db.CreateInBatches(&rows, 100).Error; err != nil {
							t.Fatalf("create alerts: %v", err)
						}
					case "task_failure":
						rows := make([]model.TaskRun, count)
						for i := range rows {
							status := "failed"
							if !eligible {
								status = "success"
							}
							rows[i] = model.TaskRun{TaskID: taskID, NodeIDSnapshot: nodeID, ExecutorTypeSnapshot: "rsync", Status: status, CreatedAt: when, UpdatedAt: when}
						}
						if err := db.CreateInBatches(&rows, 100).Error; err != nil {
							t.Fatalf("create runs: %v", err)
						}
					case "notification_failure":
						reason := "transport_failure"
						if !eligible {
							reason = "feature_retired"
						}
						alert := model.Alert{NodeID: nodeID, NodeName: "node", Severity: "warning", Status: "resolved", ErrorCode: "CAP", Message: "alert", TriggeredAt: when, DeliveryReason: reason}
						if err := db.Create(&alert).Error; err != nil {
							t.Fatalf("create delivery alert: %v", err)
						}
						rows := make([]model.AlertDelivery, count)
						for i := range rows {
							rows[i] = model.AlertDelivery{AlertID: alert.ID, IntegrationID: 1, Status: "failed", CreatedAt: when, UpdatedAt: when}
						}
						if err := db.CreateInBatches(&rows, 100).Error; err != nil {
							t.Fatalf("create deliveries: %v", err)
						}
					case "anomaly":
						rows := make([]model.AnomalyEvent, count)
						for i := range rows {
							rows[i] = model.AnomalyEvent{NodeID: nodeID, Detector: "fixture", Metric: "fixture", Severity: "warning", Details: "{}", FiredAt: when}
						}
						if err := db.CreateInBatches(&rows, 100).Error; err != nil {
							t.Fatalf("create anomalies: %v", err)
						}
					}
				}
				insert(nodes[1].ID, tasks[1].ID, at, true, 1)
				insert(nodes[0].ID, tasks[0].ID, at, true, maxHealthIncidentSourceRows-1)
				// Newer excluded rows cannot consume the cap or set the flag.
				insert(nodes[2].ID, tasks[2].ID, at.Add(time.Minute), true, maxHealthIncidentSourceRows+1)
				insert(nodes[0].ID, tasks[0].ID, old, true, 1)
				if source != "anomaly" {
					insert(nodes[0].ID, tasks[0].ID, at.Add(time.Minute), false, 1)
				}
				if source == "notification_failure" {
					alert := model.Alert{NodeID: nodes[0].ID, NodeName: "node", Severity: "warning", Status: "resolved", ErrorCode: "CAP", Message: "alert", TriggeredAt: at}
					if err := db.Create(&alert).Error; err != nil {
						t.Fatalf("create sent-delivery alert: %v", err)
					}
					if err := db.Create(&model.AlertDelivery{AlertID: alert.ID, IntegrationID: 1, Status: "sent", CreatedAt: at.Add(time.Minute)}).Error; err != nil {
						t.Fatalf("create sent delivery: %v", err)
					}
				}
				if source == "task_failure" {
					// Move tasks after creating the runs so visibility must follow the historical snapshots.
					if err := db.Model(&tasks[1]).Update("node_id", nodes[2].ID).Error; err != nil {
						t.Fatalf("move task out of scope: %v", err)
					}
					if err := db.Model(&tasks[2]).Update("node_id", nodes[0].ID).Error; err != nil {
						t.Fatalf("move task into scope: %v", err)
					}
				}
				check := func(wantTruncated bool) {
					t.Helper()
					resp, data := callHealthIncidentTimeline(t, db, "operator", user.ID)
					if resp.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
					}
					var envelope struct {
						Data map[string]json.RawMessage `json:"data"`
					}
					if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
						t.Fatalf("decode response: %v", err)
					}
					value, present := envelope.Data["truncated"]
					if data.Truncated != wantTruncated || present != wantTruncated || (present && string(value) != "true") {
						t.Fatalf("truncation: want %t, got %t wire=%s", wantTruncated, data.Truncated, resp.Body.String())
					}
					firstID, retainedID := nodes[1].ID, nodes[0].ID
					if source == "task_failure" {
						firstID, retainedID = tasks[1].ID, tasks[0].ID
					}
					counts := map[uint]int{}
					for _, group := range data.Groups {
						if !healthIncidentHasSource(group, source) {
							t.Fatalf("unexpected group: %+v", group)
						}
						if group.EventCount >= maxHealthIncidentSignalsPerGroup && len(group.Signals) != maxHealthIncidentSignalsPerGroup {
							t.Fatalf("signal preview changed: %+v", group)
						}
						counts[group.Resource.ID] = group.EventCount
						if source == "task_failure" && group.Resource.NodeID != nodes[0].ID && group.Resource.NodeID != nodes[1].ID {
							t.Fatalf("task failure did not use historical node snapshot: %+v", group.Resource)
						}
					}
					wantFirst, wantRetained := 1, maxHealthIncidentSourceRows-1
					if wantTruncated {
						wantFirst, wantRetained = 0, maxHealthIncidentSourceRows
					}
					wantGroups := 2
					if wantTruncated {
						wantGroups = 1
					}
					if counts[firstID] != wantFirst || counts[retainedID] != wantRetained || len(data.Groups) != wantGroups || data.Summary.Total != wantGroups {
						t.Fatalf("retained counts=%v summary=%+v, want first=%d retained=%d groups=%d", counts, data.Summary, wantFirst, wantRetained, wantGroups)
					}
					if source == "task_failure" || source == "notification_failure" {
						if data.Summary.Critical != wantGroups {
							t.Fatalf("critical summary=%+v, want %d groups", data.Summary, wantGroups)
						}
					} else if data.Summary.Warning != wantGroups {
						t.Fatalf("warning summary=%+v, want %d groups", data.Summary, wantGroups)
					}
				}
				check(false)
				insert(nodes[0].ID, tasks[0].ID, at, true, 1)
				check(true)
			})
		}
	}
}

func TestHealthIncidentTimelineNodeTruncation(t *testing.T) {
	for _, engine := range []struct {
		name string
		open func(*testing.T) *gorm.DB
	}{
		{"sqlite", openHealthIncidentTimelineTestDB},
		{"postgres", openBackupHealthPostgresTestDB},
	} {
		t.Run(engine.name, func(t *testing.T) {
			db := engine.open(t)
			migrateHealthIncidentTimelineTables(t, db)
			if err := db.AutoMigrate(&model.User{}); err != nil {
				t.Fatalf("migrate user: %v", err)
			}
			user := model.User{Username: "node-cap-operator", Role: "operator", PasswordHash: "x"}
			if err := db.Create(&user).Error; err != nil {
				t.Fatalf("create user: %v", err)
			}
			nodes := make([]model.Node, maxHealthIncidentSourceRows)
			for i := range nodes {
				nodes[i] = model.Node{Name: fmt.Sprintf("node-cap-%d", i), Host: "node", Username: "test", BackupDir: fmt.Sprintf("node-cap-%d", i)}
			}
			if err := db.CreateInBatches(&nodes, 100).Error; err != nil {
				t.Fatalf("create nodes: %v", err)
			}
			owners := make([]model.NodeOwner, len(nodes))
			for i, node := range nodes {
				owners[i] = model.NodeOwner{NodeID: node.ID, UserID: user.ID}
			}
			if err := db.CreateInBatches(&owners, 100).Error; err != nil {
				t.Fatalf("create owners: %v", err)
			}
			archived := model.Node{Name: "node-cap-archived", Host: "node", Username: "test", BackupDir: "node-cap-archived", Archived: true}
			unowned := model.Node{Name: "node-cap-unowned", Host: "node", Username: "test", BackupDir: "node-cap-unowned"}
			if err := db.Create(&archived).Error; err != nil {
				t.Fatalf("create archived node: %v", err)
			}
			if err := db.Create(&model.NodeOwner{NodeID: archived.ID, UserID: user.ID}).Error; err != nil {
				t.Fatalf("own archived node: %v", err)
			}
			if err := db.Create(&unowned).Error; err != nil {
				t.Fatalf("create unowned node: %v", err)
			}
			check := func(wantTruncated bool, wantCount int) healthIncidentTimelineResponse {
				t.Helper()
				resp, data := callHealthIncidentTimeline(t, db, "operator", user.ID)
				if resp.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
				}
				var envelope struct {
					Data map[string]json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				value, present := envelope.Data["truncated"]
				if data.Truncated != wantTruncated || present != wantTruncated || (present && string(value) != "true") || len(data.Groups) != wantCount || data.Summary.Total != wantCount || data.Summary.Warning != wantCount {
					t.Fatalf("truncation=%t summary=%+v groups=%d body=%s", data.Truncated, data.Summary, len(data.Groups), resp.Body.String())
				}
				return data
			}
			check(false, maxHealthIncidentSourceRows)
			extra := model.Node{Name: "node-cap-extra", Host: "node", Username: "test", BackupDir: "node-cap-extra"}
			if err := db.Create(&extra).Error; err != nil {
				t.Fatalf("create extra node: %v", err)
			}
			if err := db.Create(&model.NodeOwner{NodeID: extra.ID, UserID: user.ID}).Error; err != nil {
				t.Fatalf("own extra node: %v", err)
			}
			data := check(true, maxHealthIncidentSourceRows)
			seen := make(map[uint]bool, len(data.Groups))
			for _, group := range data.Groups {
				if !healthIncidentHasSource(group, "backup_stale") || group.EventCount != 1 {
					t.Fatalf("unexpected node group: %+v", group)
				}
				seen[group.Resource.ID] = true
			}
			for _, node := range nodes {
				if !seen[node.ID] {
					t.Fatalf("ascending node cap omitted node %d", node.ID)
				}
			}
			if seen[extra.ID] || seen[archived.ID] || seen[unowned.ID] {
				t.Fatalf("excluded nodes present: extra=%t archived=%t unowned=%t", seen[extra.ID], seen[archived.ID], seen[unowned.ID])
			}
			// Overflow reports incomplete inputs even when retained candidates
			// are all fresh and produce no backup-stale signals.
			now := time.Now().UTC().Truncate(time.Second)
			facts := make([]model.BackupCompletion, len(nodes))
			for i, node := range nodes {
				facts[i] = model.BackupCompletion{NodeID: node.ID, ExecutorType: "rsync", FactKind: model.BackupCompletionKindLegacyTransferCompleted, EvidenceStatus: model.BackupCompletionEvidenceVerified, CompletedAt: now, CreatedAt: now, UpdatedAt: now}
			}
			if err := db.CreateInBatches(&facts, 100).Error; err != nil {
				t.Fatalf("create fresh facts: %v", err)
			}
			check(true, 0)
		})
	}
}

func TestHealthIncidentTimelineQueryFailureReturnsNoPartialResult(t *testing.T) {
	db := openHealthIncidentTimelineTestDB(t)
	migrateHealthIncidentTimelineTables(t, db)
	at := time.Now().UTC().Add(-time.Hour)
	alerts := make([]model.Alert, maxHealthIncidentSourceRows+1)
	for i := range alerts {
		alerts[i] = model.Alert{NodeID: 1, NodeName: "node", Severity: "warning", Status: "open", ErrorCode: "CAP", Message: "alert", TriggeredAt: at}
	}
	if err := db.CreateInBatches(&alerts, 100).Error; err != nil {
		t.Fatalf("create alerts: %v", err)
	}
	if err := db.Migrator().DropTable(&model.AnomalyEvent{}); err != nil {
		t.Fatalf("remove anomaly table: %v", err)
	}
	resp, _ := callHealthIncidentTimeline(t, db, "admin", 1)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500: %s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data) != 0 && string(envelope.Data) != "null" {
		t.Fatalf("failure returned partial data: %s", resp.Body.String())
	}
}
