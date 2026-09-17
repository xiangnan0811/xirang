package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backuphealth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openBackupHealthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func openBackupHealthPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN required")
	}
	t.Setenv("APP_ENV", "development")
	base, err := gorm.Open(postgres.Open(withServiceMonitorPostgresTimezone(dsn)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL backup health base: %v", err)
	}
	schema := fmt.Sprintf("xirang_backup_health_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create PostgreSQL backup health schema: %v", err)
	}
	db, err := gorm.Open(postgres.Open(withServiceMonitorPostgresSchema(dsn, schema)), &gorm.Config{})
	if err != nil {
		_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if sqlDB, dbErr := base.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		t.Fatalf("open PostgreSQL backup health schema: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if sqlDB, dbErr := base.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func migrateBackupHealthTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.BackupCompletion{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
}
func addBackupHealthFact(t *testing.T, db *gorm.DB, nodeID uint, completedAt time.Time) {
	t.Helper()
	taskID := nodeID*1000 + 1
	taskRunID := nodeID*1000 + 2
	fact := model.BackupCompletion{
		TaskID: &taskID, TaskRunID: &taskRunID, NodeID: nodeID,
		ExecutorType: "rsync", FactKind: model.BackupCompletionKindLegacyTransferCompleted,
		EvidenceStatus: model.BackupCompletionEvidenceVerified, CompletedAt: completedAt,
		CreatedAt: completedAt, UpdatedAt: completedAt,
	}
	if err := db.Create(&fact).Error; err != nil {
		t.Fatalf("创建备份完成事实失败: %v", err)
	}
}
func addBackupHealthRunFact(t *testing.T, db *gorm.DB, run model.TaskRun) {
	t.Helper()
	completedAt := run.CreatedAt
	if run.FinishedAt != nil {
		completedAt = *run.FinishedAt
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return backuphealth.RecordLegacyTransferTx(context.Background(), tx, backuphealth.LegacyTransferInput{
			TaskID:       run.TaskID,
			TaskRunID:    run.ID,
			NodeID:       run.NodeIDSnapshot,
			ExecutorType: run.ExecutorTypeSnapshot,
			CompletedAt:  completedAt,
		})
	}); err != nil {
		t.Fatalf("创建执行备份完成事实失败: %v", err)
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
		StaleNodeCount   int `json:"stale_node_count"`
		DegradedPolicies []struct {
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
	return callBackupHealthWithRole(t, db, "admin", 1)
}

func callBackupHealthWithRole(t *testing.T, db *gorm.DB, role string, userID uint) (*httptest.ResponseRecorder, backupHealthResponse) {
	t.Helper()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
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
		{Name: "node-never", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-never"},
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
func TestBackupHealth_CommandSuccessDoesNotCountAsBackup(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour)
	node := model.Node{
		Name: "command-health-node", Host: "10.0.0.9", Port: 22, Username: "root",
		AuthType: "password", Status: "online", BackupDir: "command-health-node",
		LastBackupAt: &old,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	task := model.Task{Name: "maintenance-command", NodeID: node.ID, ExecutorType: "command", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create command task: %v", err)
	}
	finished := now
	if err := db.Create(&model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "command",
		TriggerType: "manual", Status: "success", FinishedAt: &finished, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create command run: %v", err)
	}

	resp, result := callBackupHealth(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if result.Data.StaleNodeCount != 1 || len(result.Data.StaleNodes) != 1 {
		t.Fatalf("ordinary command must not make node fresh: stale=%+v", result.Data.StaleNodes)
	}
	if result.Data.StaleNodes[0].LastBackupAt != nil {
		t.Fatalf("stale response must not trust denormalized historical timestamp: %v", result.Data.StaleNodes[0].LastBackupAt)
	}
	for _, point := range result.Data.Trend {
		if point.Total != 0 || point.Success != 0 {
			t.Fatalf("ordinary command must not enter backup trend: %+v", point)
		}
	}
}

func TestBackupHealth_ClassifiedSuccessWithoutFactDoesNotCountAsBackup(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour)
	node := model.Node{
		Name: "unproven-health-node", Host: "10.0.0.10", Port: 22, Username: "root",
		AuthType: "password", Status: "online", BackupDir: "unproven-health-node",
		LastBackupAt: &old,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	task := model.Task{Name: "unproven-rsync", NodeID: node.ID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create rsync task: %v", err)
	}
	started := now.Add(-time.Minute)
	finished := now
	if err := db.Create(&model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "rsync",
		TriggerType: "manual", Status: "success", StartedAt: &started, FinishedAt: &finished,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create rsync run: %v", err)
	}

	resp, result := callBackupHealth(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if result.Data.StaleNodeCount != 1 || result.Data.StaleNodes[0].LastBackupAt != nil {
		t.Fatalf("unproven classified run must not make node fresh: stale=%+v", result.Data.StaleNodes)
	}
	var total, success int
	for _, point := range result.Data.Trend {
		total += point.Total
		success += point.Success
	}
	if total != 1 || success != 0 {
		t.Fatalf("unproven classified run must remain an unsuccessful backup attempt: total=%d success=%d", total, success)
	}
}

func TestBackupHealth_TrendSeparatesFactAndUnprovenSameDayRuns(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	node := model.Node{
		Name: "mixed-trend-node", Host: "10.0.0.11", Port: 22, Username: "root",
		AuthType: "password", Status: "online", BackupDir: "mixed-trend-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	task := model.Task{Name: "mixed-trend-task", NodeID: node.ID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	finished := now
	factRun := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "rsync",
		TriggerType: "manual", Status: "success", StartedAt: &now, FinishedAt: &finished,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&factRun).Error; err != nil {
		t.Fatalf("create fact-backed run: %v", err)
	}
	addBackupHealthRunFact(t, db, factRun)
	unprovenRun := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "rsync",
		TriggerType: "manual", Status: "success", StartedAt: &now, FinishedAt: &finished,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&unprovenRun).Error; err != nil {
		t.Fatalf("create unproven run: %v", err)
	}

	resp, result := callBackupHealth(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var todayTotal, todaySuccess int
	today := now.Format("2006-01-02")
	for _, point := range result.Data.Trend {
		if point.Date == today {
			todayTotal = point.Total
			todaySuccess = point.Success
			break
		}
	}
	if todayTotal != 2 || todaySuccess != 1 {
		t.Fatalf("same-day fact and unproven attempts must remain separate: total=%d success=%d trend=%+v", todayTotal, todaySuccess, result.Data.Trend)
	}
}

func TestBackupHealth_TrendSeparatesFactAndUnprovenSameDayRunsPostgres(t *testing.T) {
	db := openBackupHealthPostgresTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	node := model.Node{
		Name: "mixed-trend-postgres-node", Host: "10.0.0.12", Port: 22, Username: "root",
		AuthType: "password", Status: "online", BackupDir: "mixed-trend-postgres-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create PostgreSQL node: %v", err)
	}
	task := model.Task{Name: "mixed-trend-postgres-task", NodeID: node.ID, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create PostgreSQL task: %v", err)
	}
	finished := now
	factRun := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "rsync",
		TriggerType: "manual", Status: "success", StartedAt: &now, FinishedAt: &finished,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&factRun).Error; err != nil {
		t.Fatalf("create PostgreSQL fact-backed run: %v", err)
	}
	addBackupHealthRunFact(t, db, factRun)
	unprovenRun := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "rsync",
		TriggerType: "manual", Status: "success", StartedAt: &now, FinishedAt: &finished,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&unprovenRun).Error; err != nil {
		t.Fatalf("create PostgreSQL unproven run: %v", err)
	}

	resp, result := callBackupHealth(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected PostgreSQL 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var todayTotal, todaySuccess int
	today := now.Format("2006-01-02")
	for _, point := range result.Data.Trend {
		if point.Date == today {
			todayTotal = point.Total
			todaySuccess = point.Success
			break
		}
	}
	if todayTotal != 2 || todaySuccess != 1 {
		t.Fatalf("PostgreSQL same-day fact and unproven attempts must remain separate: total=%d success=%d trend=%+v", todayTotal, todaySuccess, result.Data.Trend)
	}
}

func TestBackupHealth_OperatorOnlySeesOwnedStaleNodes(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	owned := model.Node{Name: "owned-stale", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "owned-stale"}
	other := model.Node{Name: "other-stale", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "other-stale"}
	if err := db.Create(&owned).Error; err != nil {
		t.Fatalf("create owned: %v", err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other: %v", err)
	}
	op := model.User{Username: "op-bh", Role: "operator", PasswordHash: "x"}
	if err := db.Create(&op).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: owned.ID, UserID: op.ID}).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}

	resp, result := callBackupHealthWithRole(t, db, "operator", op.ID)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d body=%s", resp.Code, resp.Body.String())
	}
	if result.Data.StaleNodeCount != 1 {
		t.Fatalf("operator 应只看到自己拥有的过期节点，stale_node_count=%d", result.Data.StaleNodeCount)
	}
	if len(result.Data.StaleNodes) != 1 || result.Data.StaleNodes[0].Name != "owned-stale" {
		t.Fatalf("期望仅 owned-stale，实际 %+v", result.Data.StaleNodes)
	}
}

func TestBackupHealth_StaleNodes_OlderThan48h(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now()
	staleTime := now.Add(-72 * time.Hour) // 72 小时前，超过 48 小时阈值
	freshTime := now.Add(-12 * time.Hour) // 12 小时前，未超过阈值

	nodes := []model.Node{
		{Name: "node-stale", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-stale", LastBackupAt: &staleTime},
		{Name: "node-fresh", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-fresh", LastBackupAt: &freshTime},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	addBackupHealthFact(t, db, nodes[0].ID, staleTime)
	addBackupHealthFact(t, db, nodes[1].ID, freshTime)

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
		{Name: "node-null", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-null"},
		{Name: "node-old", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-old", LastBackupAt: &staleTime},
		{Name: "node-ok", Host: "10.0.0.3", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-ok", LastBackupAt: &freshTime},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	addBackupHealthFact(t, db, nodes[1].ID, staleTime)
	addBackupHealthFact(t, db, nodes[2].ID, freshTime)

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
func TestBackupHealth_StaleNodes_IgnoreUnverifiedCompletionFacts(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := time.Now().UTC()
	verifiedAt := now.Add(-72 * time.Hour)
	unverifiedAt := now.Add(-time.Hour)
	nodes := []model.Node{
		{
			Name: "unverified-only-health-node", Host: "10.0.0.13", Port: 22, Username: "root",
			AuthType: "password", Status: "online", BackupDir: "unverified-only-health-node",
			LastBackupAt: &unverifiedAt,
		},
		{
			Name: "mixed-health-node", Host: "10.0.0.14", Port: 22, Username: "root",
			AuthType: "password", Status: "online", BackupDir: "mixed-health-node",
			LastBackupAt: &unverifiedAt,
		},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("create node: %v", err)
		}
	}
	addBackupHealthFact(t, db, nodes[1].ID, verifiedAt)
	for _, fact := range []model.BackupCompletion{
		{
			NodeID:         nodes[0].ID,
			FactKind:       model.BackupCompletionKindLegacyUnverified,
			EvidenceStatus: model.BackupCompletionEvidenceUnverified,
			CompletedAt:    unverifiedAt,
			EvidenceRef:    "legacy-only-health-fact",
			CreatedAt:      unverifiedAt,
			UpdatedAt:      unverifiedAt,
		},
		{
			NodeID:         nodes[1].ID,
			FactKind:       model.BackupCompletionKindLegacyUnverified,
			EvidenceStatus: model.BackupCompletionEvidenceUnverified,
			CompletedAt:    unverifiedAt,
			EvidenceRef:    "legacy-newer-health-fact",
			CreatedAt:      unverifiedAt,
			UpdatedAt:      unverifiedAt,
		},
	} {
		if err := db.Create(&fact).Error; err != nil {
			t.Fatalf("create unverified fact: %v", err)
		}
	}

	resp, result := callBackupHealth(t, db)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if result.Data.StaleNodeCount != len(nodes) || len(result.Data.StaleNodes) != len(nodes) {
		t.Fatalf("unverified facts must not make nodes fresh: stale=%+v", result.Data.StaleNodes)
	}
	seen := make(map[string]*time.Time, len(result.Data.StaleNodes))
	for _, stale := range result.Data.StaleNodes {
		seen[stale.Name] = stale.LastBackupAt
	}
	if lastBackupAt := seen[nodes[0].Name]; lastBackupAt != nil {
		t.Fatalf("unverified-only node reported a freshness timestamp: %v", lastBackupAt)
	}
	if lastBackupAt := seen[nodes[1].Name]; lastBackupAt == nil || !lastBackupAt.Equal(verifiedAt) {
		t.Fatalf("newer unverified fact replaced older verified freshness: %v, want %s", lastBackupAt, verifiedAt)
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
			TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync",
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
			TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync",
			Status:    status,
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
		if status == "success" {
			addBackupHealthRunFact(t, db, run)
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
			TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync",
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
		run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync", Status: "failed", CreatedAt: now.Add(-time.Duration(i) * time.Hour)}
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

// todayNoon 返回当前日的正午，确保 now-1h / now-2h / now-3h 等相对偏移不会跨越日界。
// 避免 UTC 午夜附近的 CI 运行导致的时区边界 flake。
func todayNoon() time.Time {
	t := time.Now()
	return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, t.Location())
}

func TestBackupHealth_Trend_SevenDayAggregation(t *testing.T) {
	db := openBackupHealthTestDB(t)
	migrateBackupHealthTables(t, db)

	now := todayNoon()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	task := model.Task{Name: "task-trend", NodeID: 1, ExecutorType: "rsync", Status: "success"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 今天：2 成功 + 1 失败
	runs := []model.TaskRun{
		{TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync", Status: "success", CreatedAt: now.Add(-1 * time.Hour)},
		{TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync", Status: "success", CreatedAt: now.Add(-2 * time.Hour)},
		{TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync", Status: "failed", CreatedAt: now.Add(-3 * time.Hour)},
	}
	runs = append(runs, model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync",
		Status: "success", CreatedAt: now.AddDate(0, 0, -1).Add(-1 * time.Hour),
	})
	// 8 天前的记录不应出现在趋势中
	runs = append(runs, model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, ExecutorTypeSnapshot: "rsync",
		Status: "failed", CreatedAt: now.AddDate(0, 0, -8),
	})

	for i := range runs {
		if err := db.Create(&runs[i]).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
		if runs[i].Status == "success" && runs[i].CreatedAt.Before(now.AddDate(0, 0, -7)) == false {
			addBackupHealthRunFact(t, db, runs[i])
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
		{Name: "node-healthy", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-healthy", LastBackupAt: &freshTime},
		{Name: "node-never", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-never"},
		{Name: "node-stale", Host: "10.0.0.3", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "node-stale", LastBackupAt: &staleTime},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	addBackupHealthFact(t, db, nodes[0].ID, freshTime)
	addBackupHealthFact(t, db, nodes[2].ID, staleTime)

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

	now := todayNoon()
	freshTime := now.Add(-2 * time.Hour)
	staleTime := now.Add(-96 * time.Hour)

	// 节点：2 正常 + 1 从未备份 + 1 过期
	nodes := []model.Node{
		{Name: "web-1", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "web-1", LastBackupAt: &freshTime},
		{Name: "web-2", Host: "10.0.0.2", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "web-2", LastBackupAt: &freshTime},
		{Name: "db-1", Host: "10.0.0.3", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "db-1"},
		{Name: "db-2", Host: "10.0.0.4", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: "db-2", LastBackupAt: &staleTime},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}
	addBackupHealthFact(t, db, nodes[0].ID, freshTime)
	addBackupHealthFact(t, db, nodes[1].ID, freshTime)
	addBackupHealthFact(t, db, nodes[3].ID, staleTime)

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
		run := model.TaskRun{TaskID: taskBad.ID, NodeIDSnapshot: taskBad.NodeID, ExecutorTypeSnapshot: "rsync", Status: "failed", CreatedAt: now.Add(-time.Duration(i+1) * time.Hour)}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
	}
	// 健康策略的 3 次运行（2 成功 + 1 失败）
	healthyStatuses := []string{"success", "success", "failed"}
	for i, status := range healthyStatuses {
		run := model.TaskRun{TaskID: taskGood.ID, NodeIDSnapshot: taskGood.NodeID, ExecutorTypeSnapshot: "rsync", Status: status, CreatedAt: now.Add(-time.Duration(i+1) * time.Hour)}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("创建 task_run 失败: %v", err)
		}
		if status == "success" {
			addBackupHealthRunFact(t, db, run)
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
