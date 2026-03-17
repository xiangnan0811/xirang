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

func openBackupHealthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func migrateBackupHealthTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
}

// backupHealthResponse 统一解析响应结构
type backupHealthResponse struct {
	Data struct {
		StaleNodes []struct {
			ID           uint       `json:"id"`
			Name         string     `json:"name"`
			LastBackupAt *time.Time `json:"last_backup_at"`
		} `json:"stale_nodes"`
		StaleNodeCount    int `json:"stale_node_count"`
		DegradedPolicies  []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"degraded_policies"`
		DegradedCount int `json:"degraded_count"`
		Trend         []struct {
			Date    string `json:"date"`
			Total   int    `json:"total"`
			Success int    `json:"success"`
		} `json:"trend"`
		Summary struct {
			TotalNodes    int64 `json:"total_nodes"`
			TotalPolicies int64 `json:"total_policies"`
			HealthyNodes  int64 `json:"healthy_nodes"`
		} `json:"summary"`
		GeneratedAt string `json:"generated_at"`
	} `json:"data"`
}

func callBackupHealth(t *testing.T, db *gorm.DB) (*httptest.ResponseRecorder, backupHealthResponse) {
	t.Helper()
	r := gin.New()
	handler := NewBackupHealthHandler(db)
	r.GET("/overview/backup-health", handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/overview/backup-health", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var result backupHealthResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v, body: %s", err, resp.Body.String())
	}
	return resp, result
}

// ---------- 过期节点查询 ----------

func TestBackupHealth_StaleNodes_NeverBackedUp(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	// 从未备份的节点（last_backup_at 为 NULL）
	nodes := []model.Node{
		{Name: "node-never", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online"},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}

	resp, result := callBackupHealth(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	if result.Data.StaleNodeCount != 1 {
		t.Fatalf("期望 stale_node_count=1（从未备份），实际: %d", result.Data.StaleNodeCount)
	}
	if result.Data.StaleNodes[0].Name != "node-never" {
		t.Fatalf("期望过期节点名称为 node-never，实际: %s", result.Data.StaleNodes[0].Name)
	}
	if result.Data.StaleNodes[0].LastBackupAt != nil {
		t.Fatalf("从未备份的节点 last_backup_at 应为 nil")
	}
}

func TestBackupHealth_StaleNodes_OlderThan48h(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now()
	staleTime := now.Add(-72 * time.Hour) // 72 小时前，超过 48 小时阈值
	freshTime := now.Add(-12 * time.Hour) // 12 小时前，未超过阈值

	nodes := []model.Node{
		{Name: "node-stale", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &staleTime},
		{Name: "node-fresh", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &freshTime},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}

	resp, result := callBackupHealth(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	if result.Data.StaleNodeCount != 1 {
		t.Fatalf("期望 stale_node_count=1（仅超过 48h 的节点），实际: %d", result.Data.StaleNodeCount)
	}
	if result.Data.StaleNodes[0].Name != "node-stale" {
		t.Fatalf("期望过期节点为 node-stale，实际: %s", result.Data.StaleNodes[0].Name)
	}
}

func TestBackupHealth_StaleNodes_MixedNullAndOld(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now()
	staleTime := now.Add(-50 * time.Hour)
	freshTime := now.Add(-1 * time.Hour)

	nodes := []model.Node{
		{Name: "node-null", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online"},
		{Name: "node-old", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &staleTime},
		{Name: "node-ok", Host: "10.0.0.3", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &freshTime},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}

	_, result := callBackupHealth(t, db)
	if result.Data.StaleNodeCount != 2 {
		t.Fatalf("期望 stale_node_count=2（NULL + 超 48h），实际: %d", result.Data.StaleNodeCount)
	}
	names := make(map[string]bool)
	for _, n := range result.Data.StaleNodes {
		names[n.Name] = true
	}
	if !names["node-null"] || !names["node-old"] {
		t.Fatalf("期望过期节点包含 node-null 和 node-old，实际: %v", names)
	}
}

// ---------- 降级策略检测 ----------

func TestBackupHealth_DegradedPolicy_AllThreeRunsFailed(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	policy := model.Policy{Name: "policy-bad", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 * * * *", Enabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	task := model.Task{Name: "task-bad", NodeID: 1, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	now := time.Now()
	for i := 0; i < 3; i++ {
		run := model.TaskRun{
			TaskID:    task.ID,
			Status:    "failed",
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
	}

	_, result := callBackupHealth(t, db)
	if result.Data.DegradedCount != 1 {
		t.Fatalf("期望 degraded_count=1，实际: %d", result.Data.DegradedCount)
	}
	if result.Data.DegradedPolicies[0].Name != "policy-bad" {
		t.Fatalf("期望降级策略为 policy-bad，实际: %s", result.Data.DegradedPolicies[0].Name)
	}
}

func TestBackupHealth_DegradedPolicy_NotDegradedIfOneSuccess(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	policy := model.Policy{Name: "policy-ok", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 * * * *", Enabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	task := model.Task{Name: "task-ok", NodeID: 1, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	now := time.Now()
	statuses := []string{"failed", "success", "failed"}
	for i, status := range statuses {
		run := model.TaskRun{
			TaskID:    task.ID,
			Status:    status,
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
	}

	_, result := callBackupHealth(t, db)
	if result.Data.DegradedCount != 0 {
		t.Fatalf("期望 degraded_count=0（有成功记录），实际: %d", result.Data.DegradedCount)
	}
}

func TestBackupHealth_DegradedPolicy_NotDegradedIfFewerThan3Runs(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	policy := model.Policy{Name: "policy-new", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 * * * *", Enabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	task := model.Task{Name: "task-new", NodeID: 1, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 只有 2 次运行记录，不足 3 次不应标记为降级
	now := time.Now()
	for i := 0; i < 2; i++ {
		run := model.TaskRun{
			TaskID:    task.ID,
			Status:    "failed",
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
	}

	_, result := callBackupHealth(t, db)
	if result.Data.DegradedCount != 0 {
		t.Fatalf("期望 degraded_count=0（不足 3 次运行），实际: %d", result.Data.DegradedCount)
	}
}

func TestBackupHealth_DegradedPolicy_DisabledPolicyIgnored(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	// GORM 的 default:true 导致 Enabled: false 被忽略，需要先创建再更新
	policy := model.Policy{Name: "policy-disabled", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 * * * *", Enabled: true}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Model(&policy).Update("enabled", false).Error; err != nil {
		t.Fatalf("禁用策略失败: %v", err)
	}

	task := model.Task{Name: "task-disabled", NodeID: 1, PolicyID: &policy.ID, ExecutorType: "rsync", Status: "failed"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	now := time.Now()
	for i := 0; i < 3; i++ {
		run := model.TaskRun{TaskID: task.ID, Status: "failed", CreatedAt: now.Add(-time.Duration(i) * time.Hour)}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
	}

	_, result := callBackupHealth(t, db)
	if result.Data.DegradedCount != 0 {
		t.Fatalf("期望 degraded_count=0（已禁用策略不参与检测），实际: %d", result.Data.DegradedCount)
	}
}

// ---------- 7 天趋势聚合 ----------

func TestBackupHealth_Trend_SevenDayAggregation(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	task := model.Task{Name: "task-trend", NodeID: 1, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 今天：2 成功 + 1 失败
	runs := []model.TaskRun{
		{TaskID: task.ID, Status: "success", CreatedAt: now.Add(-1 * time.Hour)},
		{TaskID: task.ID, Status: "success", CreatedAt: now.Add(-2 * time.Hour)},
		{TaskID: task.ID, Status: "failed", CreatedAt: now.Add(-3 * time.Hour)},
	}
	// 昨天：1 成功
	runs = append(runs, model.TaskRun{
		TaskID: task.ID, Status: "success", CreatedAt: now.AddDate(0, 0, -1).Add(-1 * time.Hour),
	})
	// 8 天前的记录不应出现在趋势中
	runs = append(runs, model.TaskRun{
		TaskID: task.ID, Status: "failed", CreatedAt: now.AddDate(0, 0, -8),
	})

	for i := range runs {
		if err := db.Create(&runs[i]).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
	}

	_, result := callBackupHealth(t, db)

	if len(result.Data.Trend) != 7 {
		t.Fatalf("期望趋势数据包含 7 天，实际: %d", len(result.Data.Trend))
	}

	// 验证趋势按日期升序排列（最早到最新）
	for i := 1; i < len(result.Data.Trend); i++ {
		if result.Data.Trend[i].Date < result.Data.Trend[i-1].Date {
			t.Fatalf("趋势数据应按日期升序排列，实际: %v → %v", result.Data.Trend[i-1].Date, result.Data.Trend[i].Date)
		}
	}

	// 查找今天和昨天的数据点
	trendByDate := make(map[string]struct{ Total, Success int })
	for _, tp := range result.Data.Trend {
		trendByDate[tp.Date] = struct{ Total, Success int }{tp.Total, tp.Success}
	}

	if todayData, ok := trendByDate[today]; !ok {
		t.Fatalf("趋势数据中找不到今天 (%s)", today)
	} else {
		if todayData.Total != 3 {
			t.Fatalf("今天总数期望 3，实际: %d", todayData.Total)
		}
		if todayData.Success != 2 {
			t.Fatalf("今天成功数期望 2，实际: %d", todayData.Success)
		}
	}

	if yesterdayData, ok := trendByDate[yesterday]; !ok {
		t.Fatalf("趋势数据中找不到昨天 (%s)", yesterday)
	} else {
		if yesterdayData.Total != 1 {
			t.Fatalf("昨天总数期望 1，实际: %d", yesterdayData.Total)
		}
		if yesterdayData.Success != 1 {
			t.Fatalf("昨天成功数期望 1，实际: %d", yesterdayData.Success)
		}
	}
}

func TestBackupHealth_Trend_EmptyReturnsZeroFilled(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	_, result := callBackupHealth(t, db)
	if len(result.Data.Trend) != 7 {
		t.Fatalf("空数据时趋势也应返回 7 天，实际: %d", len(result.Data.Trend))
	}
	for _, tp := range result.Data.Trend {
		if tp.Total != 0 || tp.Success != 0 {
			t.Fatalf("空数据时趋势数据点应全部为 0，实际: %+v", tp)
		}
	}
}

// ---------- 汇总统计 ----------

func TestBackupHealth_Summary_AllStatistics(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now()
	freshTime := now.Add(-6 * time.Hour)
	staleTime := now.Add(-72 * time.Hour)

	// 3 个节点：1 个正常，1 个从未备份，1 个过期
	nodes := []model.Node{
		{Name: "node-healthy", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &freshTime},
		{Name: "node-never", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online"},
		{Name: "node-stale", Host: "10.0.0.3", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &staleTime},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}

	// 2 个启用的策略 + 1 个禁用的策略
	policies := []model.Policy{
		{Name: "policy-a", SourcePath: "/src/a", TargetPath: "/dst/a", CronSpec: "0 * * * *", Enabled: true},
		{Name: "policy-b", SourcePath: "/src/b", TargetPath: "/dst/b", CronSpec: "0 * * * *", Enabled: true},
		{Name: "policy-c", SourcePath: "/src/c", TargetPath: "/dst/c", CronSpec: "0 * * * *", Enabled: true},
	}
	for i := range policies {
		if err := db.Create(&policies[i]).Error; err != nil {
			t.Fatalf("创建策略失败: %v", err)
		}
	}
	// GORM 的 default:true 导致 Enabled: false 被忽略，需要创建后再更新
	if err := db.Model(&policies[2]).Update("enabled", false).Error; err != nil {
		t.Fatalf("禁用策略失败: %v", err)
	}

	_, result := callBackupHealth(t, db)

	if result.Data.Summary.TotalNodes != 3 {
		t.Fatalf("期望 total_nodes=3，实际: %d", result.Data.Summary.TotalNodes)
	}
	if result.Data.Summary.TotalPolicies != 2 {
		t.Fatalf("期望 total_policies=2（仅启用的策略），实际: %d", result.Data.Summary.TotalPolicies)
	}
	if result.Data.Summary.HealthyNodes != 1 {
		t.Fatalf("期望 healthy_nodes=1（3 总数 - 2 过期），实际: %d", result.Data.Summary.HealthyNodes)
	}
	if result.Data.StaleNodeCount != 2 {
		t.Fatalf("期望 stale_node_count=2，实际: %d", result.Data.StaleNodeCount)
	}
}

func TestBackupHealth_Summary_EmptyDatabase(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	resp, result := callBackupHealth(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	if result.Data.Summary.TotalNodes != 0 {
		t.Fatalf("期望 total_nodes=0，实际: %d", result.Data.Summary.TotalNodes)
	}
	if result.Data.Summary.TotalPolicies != 0 {
		t.Fatalf("期望 total_policies=0，实际: %d", result.Data.Summary.TotalPolicies)
	}
	if result.Data.Summary.HealthyNodes != 0 {
		t.Fatalf("期望 healthy_nodes=0，实际: %d", result.Data.Summary.HealthyNodes)
	}
	if result.Data.StaleNodeCount != 0 {
		t.Fatalf("期望 stale_node_count=0，实际: %d", result.Data.StaleNodeCount)
	}
	if result.Data.DegradedCount != 0 {
		t.Fatalf("期望 degraded_count=0，实际: %d", result.Data.DegradedCount)
	}
	if len(result.Data.StaleNodes) != 0 {
		t.Fatalf("期望 stale_nodes 为空数组，实际长度: %d", len(result.Data.StaleNodes))
	}
	if len(result.Data.DegradedPolicies) != 0 {
		t.Fatalf("期望 degraded_policies 为空数组，实际长度: %d", len(result.Data.DegradedPolicies))
	}
	if result.Data.GeneratedAt == "" {
		t.Fatalf("期望 generated_at 非空")
	}
}

// ---------- 综合场景 ----------

func TestBackupHealth_FullScenario(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now()
	freshTime := now.Add(-2 * time.Hour)
	staleTime := now.Add(-96 * time.Hour)

	// 节点：2 正常 + 1 从未备份 + 1 过期
	nodes := []model.Node{
		{Name: "web-1", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &freshTime},
		{Name: "web-2", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &freshTime},
		{Name: "db-1", Host: "10.0.0.3", Port: 22, Username: "root", AuthType: "password", Status: "online"},
		{Name: "db-2", Host: "10.0.0.4", Port: 22, Username: "root", AuthType: "password", Status: "online", LastBackupAt: &staleTime},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}

	// 策略：1 个降级（最近 3 次全失败）+ 1 个健康
	policyDegraded := model.Policy{Name: "backup-db", SourcePath: "/data", TargetPath: "/backup", CronSpec: "0 2 * * *", Enabled: true}
	policyHealthy := model.Policy{Name: "backup-web", SourcePath: "/var/www", TargetPath: "/backup/web", CronSpec: "0 3 * * *", Enabled: true}
	if err := db.Create(&policyDegraded).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&policyHealthy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	taskBad := model.Task{Name: "task-db", NodeID: 1, PolicyID: &policyDegraded.ID, ExecutorType: "rsync", Status: "failed"}
	taskGood := model.Task{Name: "task-web", NodeID: 1, PolicyID: &policyHealthy.ID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&taskBad).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := db.Create(&taskGood).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 降级策略的 3 次失败
	for i := 0; i < 3; i++ {
		run := model.TaskRun{TaskID: taskBad.ID, Status: "failed", CreatedAt: now.Add(-time.Duration(i+1) * time.Hour)}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
	}
	// 健康策略的 3 次运行（2 成功 + 1 失败）
	healthyStatuses := []string{"success", "success", "failed"}
	for i, status := range healthyStatuses {
		run := model.TaskRun{TaskID: taskGood.ID, Status: status, CreatedAt: now.Add(-time.Duration(i+1) * time.Hour)}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
	}

	_, result := callBackupHealth(t, db)

	// 验证汇总
	if result.Data.Summary.TotalNodes != 4 {
		t.Fatalf("期望 total_nodes=4，实际: %d", result.Data.Summary.TotalNodes)
	}
	if result.Data.Summary.TotalPolicies != 2 {
		t.Fatalf("期望 total_policies=2，实际: %d", result.Data.Summary.TotalPolicies)
	}
	if result.Data.Summary.HealthyNodes != 2 {
		t.Fatalf("期望 healthy_nodes=2（4 - 2 过期），实际: %d", result.Data.Summary.HealthyNodes)
	}

	// 验证过期节点
	if result.Data.StaleNodeCount != 2 {
		t.Fatalf("期望 stale_node_count=2，实际: %d", result.Data.StaleNodeCount)
	}

	// 验证降级策略
	if result.Data.DegradedCount != 1 {
		t.Fatalf("期望 degraded_count=1，实际: %d", result.Data.DegradedCount)
	}
	if result.Data.DegradedPolicies[0].Name != "backup-db" {
		t.Fatalf("期望降级策略为 backup-db，实际: %s", result.Data.DegradedPolicies[0].Name)
	}

	// 验证趋势有今天的数据（6 条 task_run 都在今天）
	today := now.Format("2006-01-02")
	for _, tp := range result.Data.Trend {
		if tp.Date == today {
			if tp.Total != 6 {
				t.Fatalf("今天趋势总数期望 6，实际: %d", tp.Total)
			}
			if tp.Success != 2 {
				t.Fatalf("今天趋势成功数期望 2，实际: %d", tp.Success)
			}
			return
		}
	}
	t.Fatalf("趋势数据中未找到今天 (%s)", today)
}
