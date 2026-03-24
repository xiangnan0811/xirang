package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openConfigHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), strings.ReplaceAll(t.Name(), "/", "_")+".db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func TestConfigExportedDataCanBeImportedBackAsDownloadedFile(t *testing.T) {
	sourceDB := openConfigHandlerTestDB(t)
	if err := sourceDB.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化源数据库失败: %v", err)
	}

	node := model.Node{Name: "node-a", Host: "10.0.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := sourceDB.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	policy := model.Policy{Name: "policy-a", SourcePath: "/data/src", TargetPath: "/backup/node-a", CronSpec: "*/5 * * * *", Enabled: true}
	if err := sourceDB.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	taskEntity := model.Task{
		Name:         "task-a",
		NodeID:       node.ID,
		PolicyID:     &policy.ID,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/node-a",
		CronSpec:     "*/5 * * * *",
		Status:       "pending",
	}
	if err := sourceDB.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	dependentTask := model.Task{
		Name:            "task-b",
		NodeID:          node.ID,
		PolicyID:        &policy.ID,
		DependsOnTaskID: &taskEntity.ID,
		ExecutorType:    "rsync",
		RsyncSource:     "/data/dep",
		RsyncTarget:     "/backup/node-a/dep",
		Status:          "pending",
	}
	if err := sourceDB.Create(&dependentTask).Error; err != nil {
		t.Fatalf("创建依赖任务失败: %v", err)
	}

	exportHandler := NewConfigHandler(sourceDB, nil)
	exportRouter := gin.New()
	exportRouter.GET("/config/export", exportHandler.Export)

	exportResp := httptest.NewRecorder()
	exportReq := httptest.NewRequest(http.MethodGet, "/config/export", nil)
	exportRouter.ServeHTTP(exportResp, exportReq)
	if exportResp.Code != http.StatusOK {
		t.Fatalf("导出接口期望 200，实际: %d，响应: %s", exportResp.Code, exportResp.Body.String())
	}

	var exportPayload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(exportResp.Body.Bytes(), &exportPayload); err != nil {
		t.Fatalf("解析导出响应失败: %v", err)
	}

	downloadedFile, err := json.Marshal(exportPayload.Data)
	if err != nil {
		t.Fatalf("序列化下载文件失败: %v", err)
	}

	targetDB := openConfigHandlerTestDB(t)
	if err := targetDB.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化目标数据库失败: %v", err)
	}

	targetNode := model.Node{Name: "node-a", Host: "10.0.0.9", Port: 22, Username: "root", AuthType: "key", BackupDir: "node-a"}
	if err := targetDB.Create(&targetNode).Error; err != nil {
		t.Fatalf("创建目标节点失败: %v", err)
	}
	targetPolicy := model.Policy{Name: "policy-a", SourcePath: "/seed/src", TargetPath: "/seed/dst", CronSpec: "0 * * * *", Enabled: false}
	if err := targetDB.Create(&targetPolicy).Error; err != nil {
		t.Fatalf("创建目标策略失败: %v", err)
	}

	importHandler := NewConfigHandler(targetDB, nil)
	importRouter := gin.New()
	importRouter.POST("/config/import", importHandler.Import)

	importReq := httptest.NewRequest(http.MethodPost, "/config/import?conflict=skip", strings.NewReader(string(downloadedFile)))
	importReq.Header.Set("Content-Type", "application/json")
	importResp := httptest.NewRecorder()
	importRouter.ServeHTTP(importResp, importReq)
	if importResp.Code != http.StatusOK {
		t.Fatalf("导入接口期望 200，实际: %d，响应: %s", importResp.Code, importResp.Body.String())
	}

	var importedTask model.Task
	if err := targetDB.Where("name = ?", "task-a").First(&importedTask).Error; err != nil {
		t.Fatalf("导入后应存在任务记录，实际错误: %v", err)
	}
	if importedTask.NodeID != targetNode.ID {
		t.Fatalf("任务应按节点名称映射到目标节点，实际 node_id=%d，期望 %d", importedTask.NodeID, targetNode.ID)
	}
	if importedTask.PolicyID == nil || *importedTask.PolicyID != targetPolicy.ID {
		t.Fatalf("任务应按策略名称映射到目标策略，实际 policy_id=%v，期望 %d", importedTask.PolicyID, targetPolicy.ID)
	}

	var importedDependent model.Task
	if err := targetDB.Where("name = ?", "task-b").First(&importedDependent).Error; err != nil {
		t.Fatalf("导入后应存在依赖任务记录，实际错误: %v", err)
	}
	if importedDependent.DependsOnTaskID == nil || *importedDependent.DependsOnTaskID != importedTask.ID {
		t.Fatalf("导入后应恢复任务依赖关系，实际 depends_on_task_id=%v，期望 %d", importedDependent.DependsOnTaskID, importedTask.ID)
	}
}

func TestConfigImportAcceptsWrappedExportEnvelope(t *testing.T) {
	targetDB := openConfigHandlerTestDB(t)
	if err := targetDB.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}); err != nil {
		t.Fatalf("初始化目标数据库失败: %v", err)
	}

	importHandler := NewConfigHandler(targetDB, nil)
	importRouter := gin.New()
	importRouter.POST("/config/import", importHandler.Import)

	body := `{"version":"1.0","exported_at":"2026-03-24T00:00:00Z","data":{"nodes":[{"name":"node-a","host":"10.0.0.1","port":22,"username":"root","auth_type":"key"}]}}`
	importReq := httptest.NewRequest(http.MethodPost, "/config/import?conflict=skip", strings.NewReader(body))
	importReq.Header.Set("Content-Type", "application/json")
	importResp := httptest.NewRecorder()
	importRouter.ServeHTTP(importResp, importReq)

	if importResp.Code != http.StatusOK {
		t.Fatalf("导入包裹格式期望 200，实际: %d，响应: %s", importResp.Code, importResp.Body.String())
	}

	var count int64
	if err := targetDB.Model(&model.Node{}).Where("name = ?", "node-a").Count(&count).Error; err != nil {
		t.Fatalf("统计节点失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("包裹格式导入后应创建节点，实际数量: %d", count)
	}
}
