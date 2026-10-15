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
