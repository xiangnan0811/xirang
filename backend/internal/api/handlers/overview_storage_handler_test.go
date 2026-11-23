package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openStorageUsageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db
}

func callStorageUsage(t *testing.T, db *gorm.DB, role string, userID uint) (*httptest.ResponseRecorder, struct {
	Data struct {
		MountPoints []mountPointInfo `json:"mount_points"`
		PerNode     []perNodeUsage   `json:"per_node"`
	} `json:"data"`
}) {
	t.Helper()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, role)
		c.Set(middleware.CtxUserID, userID)
		c.Next()
	})
	r.GET("/overview/storage-usage", NewStorageUsageHandler(db).Get)
	req := httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var body struct {
		Data struct {
			MountPoints []mountPointInfo `json:"mount_points"`
			PerNode     []perNodeUsage   `json:"per_node"`
		} `json:"data"`
	}
	if resp.Code == http.StatusOK {
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v body=%s", err, resp.Body.String())
		}
	}
	return resp, body
}

func TestStorageUsageExcludesDisabledAndTemplatePoliciesFromMountPoints(t *testing.T) {
	db := openStorageUsageTestDB(t)

	activeDir := t.TempDir()
	disabledDir := t.TempDir()
	templateDir := t.TempDir()

	activePolicy := model.Policy{
		Name: "storage-active", SourcePath: "/src/a", TargetPath: activeDir,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true, IsTemplate: false,
	}
	disabledPolicy := model.Policy{
		Name: "storage-disabled", SourcePath: "/src/d", TargetPath: disabledDir,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: false, IsTemplate: false,
	}
	templatePolicy := model.Policy{
		Name: "storage-template", SourcePath: "/src/t", TargetPath: templateDir,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true, IsTemplate: true,
	}
	for _, p := range []*model.Policy{&activePolicy, &disabledPolicy, &templatePolicy} {
		if err := model.CreatePolicyWithExplicitValues(db, p, model.PolicyCreateExplicitColumns()...); err != nil {
			t.Fatalf("创建策略 %q 失败: %v", p.Name, err)
		}
	}

	for _, role := range []string{"admin", "viewer"} {
		resp, body := callStorageUsage(t, db, role, 1)
		if resp.Code != http.StatusOK {
			t.Fatalf("%s 期望 200，实际 %d", role, resp.Code)
		}
		if len(body.Data.MountPoints) != 1 || body.Data.MountPoints[0].Path != activeDir {
			t.Fatalf("%s mount_points 应仅包含启用的非模板策略路径 %q，实际: %+v", role, activeDir, body.Data.MountPoints)
		}
	}

	// Operator 不得获得全局磁盘容量/挂载点信息。
	op := model.User{Username: "storage-op", Role: "operator", PasswordHash: "x"}
	if err := db.Create(&op).Error; err != nil {
		t.Fatalf("创建 operator 失败: %v", err)
	}
	node := model.Node{Name: "storage-node", Host: "10.0.11.1", Username: "root", AuthType: "key", BackupDir: "storage-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: op.ID}).Error; err != nil {
		t.Fatalf("绑定节点归属失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: activePolicy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("绑定策略节点失败: %v", err)
	}

	opResp, opBody := callStorageUsage(t, db, "operator", op.ID)
	if opResp.Code != http.StatusOK {
		t.Fatalf("operator 期望 200，实际 %d", opResp.Code)
	}
	if len(opBody.Data.MountPoints) != 0 {
		t.Fatalf("operator mount_points 必须为空，实际: %+v", opBody.Data.MountPoints)
	}
}

func TestStorageUsageReturnsInternalErrorOnDBFailure(t *testing.T) {
	db := openStorageUsageTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层 DB 失败: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("关闭 DB 失败: %v", err)
	}

	resp, _ := callStorageUsage(t, db, "admin", 1)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("数据库错误应返回 500，实际: %d", resp.Code)
	}
}
