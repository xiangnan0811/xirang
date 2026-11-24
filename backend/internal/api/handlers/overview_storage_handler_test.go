package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"golang.org/x/sys/unix"
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

func TestStorageUsagePreservesPersistedTaskTargetWhenPolicyDisabled(t *testing.T) {
	db := openStorageUsageTestDB(t)

	disabledDir := t.TempDir()
	templateDir := t.TempDir()

	node := model.Node{Name: "archive-node", Host: "10.0.12.1", Username: "root", AuthType: "key", BackupDir: "archive-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	disabledPolicy := model.Policy{
		Name: "disabled-with-data", SourcePath: "/src/d", TargetPath: disabledDir,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: false, IsTemplate: false,
	}
	templatePolicy := model.Policy{
		Name: "template-only", SourcePath: "/src/t", TargetPath: templateDir,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true, IsTemplate: true,
	}
	for _, p := range []*model.Policy{&disabledPolicy, &templatePolicy} {
		if err := model.CreatePolicyWithExplicitValues(db, p, model.PolicyCreateExplicitColumns()...); err != nil {
			t.Fatalf("创建策略失败: %v", err)
		}
	}
	persistedTask := model.Task{
		Name:         "disabled-policy-task",
		NodeID:       node.ID,
		PolicyID:     &disabledPolicy.ID,
		ExecutorType: "rsync",
		RsyncTarget:  disabledDir,
		Status:       "success",
	}
	if err := db.Create(&persistedTask).Error; err != nil {
		t.Fatalf("创建历史任务失败: %v", err)
	}

	resp, body := callStorageUsage(t, db, "admin", 1)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d", resp.Code)
	}

	if len(body.Data.MountPoints) != 1 || body.Data.MountPoints[0].Path != disabledDir {
		t.Fatalf("禁用策略已有落盘任务时 mount_points 应保留 %q 并排除模板 %q，实际: %+v", disabledDir, templateDir, body.Data.MountPoints)
	}
	if len(body.Data.PerNode) != 1 || body.Data.PerNode[0].Path != disabledDir || body.Data.PerNode[0].NodeName != "archive-node" {
		t.Fatalf("per_node 应显式映射任务目标与节点名，实际: %+v", body.Data.PerNode)
	}
}

func TestStorageUsageDeduplicatesMountPointsByFilesystemDevice(t *testing.T) {
	db := openStorageUsageTestDB(t)

	rootDir := t.TempDir()
	subDirA := filepath.Join(rootDir, "node-1")
	subDirB := filepath.Join(rootDir, "node-2")
	for _, dir := range []string{subDirA, subDirB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建子目录失败: %v", err)
		}
	}

	node := model.Node{Name: "same-vol-node", Host: "10.0.13.1", Username: "root", AuthType: "key", BackupDir: "same-vol-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	activePolicy := model.Policy{
		Name: "same-vol-policy", SourcePath: "/src/v", TargetPath: rootDir,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true, IsTemplate: false,
	}
	if err := model.CreatePolicyWithExplicitValues(db, &activePolicy, model.PolicyCreateExplicitColumns()...); err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	for idx, dir := range []string{subDirA, subDirB} {
		task := model.Task{
			Name:         fmt.Sprintf("same-vol-task-%d", idx),
			NodeID:       node.ID,
			PolicyID:     &activePolicy.ID,
			ExecutorType: "rsync",
			RsyncTarget:  dir,
			Status:       "success",
		}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
	}

	resp, body := callStorageUsage(t, db, "admin", 1)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d", resp.Code)
	}
	if len(body.Data.MountPoints) != 1 || body.Data.MountPoints[0].Path != rootDir {
		t.Fatalf("同一卷上的多个任务目录不得重复上报整卷容量，期望仅 %q，实际: %+v", rootDir, body.Data.MountPoints)
	}
	if len(body.Data.PerNode) != 2 {
		t.Fatalf("per_node 仍应保留 2 个任务目录，实际: %+v", body.Data.PerNode)
	}
}

func TestStorageUsageCompletesDBQueriesBeforeScanBudgetTimeout(t *testing.T) {
	db := openStorageUsageTestDB(t)

	taskDir := t.TempDir()
	node := model.Node{Name: "timeout-node", Host: "10.0.14.1", Username: "root", AuthType: "key", BackupDir: "timeout-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	activePolicy := model.Policy{
		Name: "timeout-policy", SourcePath: "/src/t", TargetPath: taskDir,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true, IsTemplate: false,
	}
	if err := model.CreatePolicyWithExplicitValues(db, &activePolicy, model.PolicyCreateExplicitColumns()...); err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: activePolicy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("绑定策略节点失败: %v", err)
	}
	if err := db.Create(&model.Task{
		Name:         "timeout-task",
		NodeID:       node.ID,
		PolicyID:     &activePolicy.ID,
		ExecutorType: "rsync",
		RsyncTarget:  taskDir,
		Status:       "success",
	}).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	h := NewStorageUsageHandler(db)
	h.newScanContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	r.GET("/overview/storage-usage", h.Get)

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("扫描预算耗尽时数据库元数据已读取完成，应返回 200 而非 %d: %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Data struct {
			MountPoints           []mountPointInfo `json:"mount_points"`
			PerNode               []perNodeUsage   `json:"per_node"`
			Incomplete            bool             `json:"incomplete"`
			MountPointsIncomplete bool             `json:"mount_points_incomplete"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !body.Data.Incomplete || !body.Data.MountPointsIncomplete {
		t.Fatalf("扫描超时后必须透出 incomplete=true 和 mount_points_incomplete=true，实际: %+v", body.Data)
	}
	if len(body.Data.PerNode) != 2 {
		t.Fatalf("扫描超时后应保留任务与策略候选条目并标记 incomplete，实际: %+v", body.Data.PerNode)
	}
	for _, entry := range body.Data.PerNode {
		if !entry.Incomplete {
			t.Fatalf("扫描预算耗尽的 per_node 条目应标记 incomplete=true: %+v", entry)
		}
	}
}

func TestStorageUsageReportsMidScanTimeoutsIncludingLastMountPoint(t *testing.T) {
	t.Run("last mount point probe exhausts scan budget and still reports mount_points_incomplete", func(t *testing.T) {
		db := openStorageUsageTestDB(t)

		volA := t.TempDir()
		volB := t.TempDir()
		for idx, dir := range []string{volA, volB} {
			p := model.Policy{
				Name: fmt.Sprintf("mount-timeout-%d", idx), SourcePath: "/src", TargetPath: dir,
				CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true,
			}
			if err := model.CreatePolicyWithExplicitValues(db, &p, model.PolicyCreateExplicitColumns()...); err != nil {
				t.Fatalf("创建策略失败: %v", err)
			}
		}

		var cancelScan context.CancelFunc
		h := NewStorageUsageHandler(db)
		h.newScanContext = func(parent context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(parent)
			cancelScan = cancel
			return ctx, cancel
		}
		h.statMount = func(path string) (string, unix.Statfs_t, error) {
			if path == volA {
				return "sb:259:1", unix.Statfs_t{Bsize: 4096, Blocks: 26214400, Bavail: 13107200}, nil
			}
			// 最后一个挂载点探测中途耗尽预算并返回错误。
			cancelScan()
			return "", unix.Statfs_t{}, context.DeadlineExceeded
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.CtxRole, "admin")
			c.Set(middleware.CtxUserID, uint(1))
			c.Next()
		})
		r.GET("/overview/storage-usage", h.Get)

		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
		}
		var body struct {
			Data struct {
				MountPoints           []mountPointInfo `json:"mount_points"`
				Incomplete            bool             `json:"incomplete"`
				MountPointsIncomplete bool             `json:"mount_points_incomplete"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if len(body.Data.MountPoints) != 1 || body.Data.MountPoints[0].Path != volA {
			t.Fatalf("已成功读取的第一个挂载点应保留，实际: %+v", body.Data.MountPoints)
		}
		if !body.Data.MountPointsIncomplete || !body.Data.Incomplete {
			t.Fatalf("最后一个挂载点耗尽预算时必须上报 mount_points_incomplete=true 和 incomplete=true，实际: %+v", body.Data)
		}
	})

	t.Run("mid-scan per-node timeout keeps mount points complete and marks only unfinished directory incomplete", func(t *testing.T) {
		db := openStorageUsageTestDB(t)

		dirA := t.TempDir()
		dirB := t.TempDir()
		if err := os.WriteFile(filepath.Join(dirA, "ok.bin"), make([]byte, 1024), 0o644); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dirB, "pending.bin"), make([]byte, 1024), 0o644); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}

		node := model.Node{Name: "midscan-node", Host: "10.0.15.1", Username: "root", AuthType: "key", BackupDir: "midscan-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
		for idx, dir := range []string{dirA, dirB} {
			if err := db.Create(&model.Task{
				Name: fmt.Sprintf("midscan-task-%d", idx), NodeID: node.ID,
				ExecutorType: "rsync", RsyncTarget: dir, Status: "success",
			}).Error; err != nil {
				t.Fatalf("创建任务失败: %v", err)
			}
		}

		var cancelScan context.CancelFunc
		h := NewStorageUsageHandler(db)
		h.newScanContext = func(parent context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(parent)
			cancelScan = cancel
			return ctx, cancel
		}
		h.scanDir = func(ctx context.Context, path string, allowMissingRoot bool) (float64, bool) {
			used, complete := dirSizeGB(ctx, path, allowMissingRoot)
			if path == dirA {
				// 第一个目录完整扫描后取消扫描上下文，模拟第二个目录中途超时。
				cancelScan()
			}
			return used, complete
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.CtxRole, "admin")
			c.Set(middleware.CtxUserID, uint(1))
			c.Next()
		})
		r.GET("/overview/storage-usage", h.Get)

		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
		}
		var body struct {
			Data struct {
				MountPoints           []mountPointInfo `json:"mount_points"`
				PerNode               []perNodeUsage   `json:"per_node"`
				Incomplete            bool             `json:"incomplete"`
				MountPointsIncomplete bool             `json:"mount_points_incomplete"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if body.Data.MountPointsIncomplete {
			t.Fatalf("挂载点已全部扫描完成，不应标记 mount_points_incomplete=true")
		}
		if !body.Data.Incomplete || len(body.Data.PerNode) != 2 {
			t.Fatalf("应返回 2 条节点用量且顶层 incomplete=true，实际: %+v", body.Data)
		}
		if body.Data.PerNode[0].Incomplete {
			t.Fatalf("先完成的 dirA 不应标记 incomplete: %+v", body.Data.PerNode[0])
		}
		if !body.Data.PerNode[1].Incomplete {
			t.Fatalf("超时中断的 dirB 必须标记 incomplete=true: %+v", body.Data.PerNode[1])
		}
	})
}

func TestStorageUsageMarksUnreadableDirectoriesIncompleteAndDistinguishesUncreatedTargets(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 进程不受 chmod 0000 权限限制")
	}

	db := openStorageUsageTestDB(t)
	node := model.Node{Name: "perm-node", Host: "10.0.16.1", Username: "root", AuthType: "key", BackupDir: "perm-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	// 1. 尚未物化的策略目录（ErrNotExist）：应视为完整 0 GB，不报错。
	uncreatedRoot := filepath.Join(t.TempDir(), "not-created-yet")
	uncreatedPolicy := model.Policy{
		Name: "uncreated-policy", SourcePath: "/src/u", TargetPath: uncreatedRoot,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true,
	}
	if err := model.CreatePolicyWithExplicitValues(db, &uncreatedPolicy, model.PolicyCreateExplicitColumns()...); err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: uncreatedPolicy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("绑定策略节点失败: %v", err)
	}

	// 2. 根不可读的已物化任务目录：不得伪报完整 0 GB，必须标记 incomplete=true。
	unreadableRoot := filepath.Join(t.TempDir(), "locked-root")
	if err := os.MkdirAll(unreadableRoot, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unreadableRoot, "secret.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	if err := os.Chmod(unreadableRoot, 0o000); err != nil {
		t.Fatalf("设置目录权限失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadableRoot, 0o755) })

	// 3. 子目录不可读的已物化任务目录：读取可读文件后仍必须标记 incomplete=true。
	partialRoot := filepath.Join(t.TempDir(), "partial-root")
	lockedSub := filepath.Join(partialRoot, "locked-sub")
	if err := os.MkdirAll(lockedSub, 0o755); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(partialRoot, "readable.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("写入可读文件失败: %v", err)
	}
	if err := os.Chmod(lockedSub, 0o000); err != nil {
		t.Fatalf("设置子目录权限失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedSub, 0o755) })

	// 4. 可列目录但不可 stat 子项（chmod 0400，无执行位）：直接覆盖 DirEntry.Info() 非 ENOENT 失败分支。
	noExecRoot := filepath.Join(t.TempDir(), "no-exec-root")
	noExecSub := filepath.Join(noExecRoot, "listable-only")
	if err := os.MkdirAll(noExecSub, 0o755); err != nil {
		t.Fatalf("创建无执行位子目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(noExecSub, "unstatable.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("写入子文件失败: %v", err)
	}
	if err := os.Chmod(noExecSub, 0o400); err != nil {
		t.Fatalf("设置 0400 权限失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(noExecSub, 0o755) })

	for idx, dir := range []string{unreadableRoot, partialRoot, noExecRoot} {
		if err := db.Create(&model.Task{
			Name: fmt.Sprintf("perm-task-%d", idx), NodeID: node.ID,
			ExecutorType: "rsync", RsyncTarget: dir, Status: "success",
		}).Error; err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
	}

	h := NewStorageUsageHandler(db)
	h.statMount = func(path string) (string, unix.Statfs_t, error) {
		if path == unreadableRoot {
			return "", unix.Statfs_t{}, errors.New("permission denied")
		}
		if path == uncreatedRoot {
			return "", unix.Statfs_t{}, os.ErrNotExist
		}
		return "sb:259:1", unix.Statfs_t{Bsize: 4096, Blocks: 26214400, Bavail: 13107200}, nil
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	r.GET("/overview/storage-usage", h.Get)

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Data struct {
			PerNode               []perNodeUsage `json:"per_node"`
			Incomplete            bool           `json:"incomplete"`
			MountPointsIncomplete bool           `json:"mount_points_incomplete"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !body.Data.Incomplete || !body.Data.MountPointsIncomplete {
		t.Fatalf("存在不可读目录与挂载探测非 ENOENT 失败时应标记 incomplete 与 mount_points_incomplete，实际: %+v", body.Data)
	}

	byPath := make(map[string]perNodeUsage, len(body.Data.PerNode))
	for _, item := range body.Data.PerNode {
		byPath[item.Path] = item
	}
	if entry, ok := byPath[unreadableRoot]; !ok || !entry.Incomplete {
		t.Fatalf("根不可读目录必须标记 incomplete=true，实际: %+v", byPath)
	}
	if entry, ok := byPath[partialRoot]; !ok || !entry.Incomplete {
		t.Fatalf("子目录不可读时必须标记 incomplete=true，实际: %+v", byPath)
	}
	if entry, ok := byPath[noExecRoot]; !ok || !entry.Incomplete {
		t.Fatalf("DirEntry.Info() 权限失败时必须标记 incomplete=true，实际: %+v", byPath)
	}
	foundUncreated := false
	for path, entry := range byPath {
		if strings.HasPrefix(path, uncreatedRoot) {
			foundUncreated = true
			if entry.Incomplete {
				t.Fatalf("尚未创建的策略目标目录不应误判为读取失败: %+v", entry)
			}
		}
	}
	if !foundUncreated {
		t.Fatalf("未找到尚未创建的策略目标条目: %+v", byPath)
	}
}

func TestStorageUsageFlagsVanishedExecutedTaskTargetWhileAllowingUncreatedCandidates(t *testing.T) {
	db := openStorageUsageTestDB(t)
	if err := db.AutoMigrate(&model.TaskRun{}); err != nil {
		t.Fatalf("迁移 task_runs 失败: %v", err)
	}

	node := model.Node{Name: "unmount-node", Host: "10.0.17.1", Username: "root", AuthType: "key", BackupDir: "unmount-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	now := time.Now().UTC()
	createEstablishedRun := func(task model.Task) {
		t.Helper()
		task.Node = node
		fp := model.TaskRunBackupConfigFingerprint(task)
		if fp == "" {
			t.Fatalf("生成任务指纹失败: %+v", task)
		}
		if err := db.Create(&model.TaskRun{
			TaskID:                  task.ID,
			NodeIDSnapshot:          node.ID,
			ExecutorTypeSnapshot:    "rsync",
			TriggerType:             "cron",
			Status:                  model.TaskRunStatusSuccess,
			StartedAt:               &now,
			FinishedAt:              &now,
			BackupGenerationState:   model.TaskRunGenerationStateVerified,
			BackupConfigFingerprint: fp,
		}).Error; err != nil {
			t.Fatalf("创建已建立目标的 TaskRun 失败: %v", err)
		}
	}

	// 1. 已针对当前目标完成备份的任务，备份卷卸载后目标目录消失（ENOENT）：
	// 不得当作完整 0 GB，挂载点与节点用量均必须标记 incomplete。
	vanishedDir := filepath.Join(t.TempDir(), "unmounted-volume", "node-1")
	executedTask := model.Task{
		Name: "executed-vanished-task", NodeID: node.ID, ExecutorType: "rsync",
		RsyncTarget: vanishedDir, Status: "success", LastRunAt: &now,
	}
	if err := db.Create(&executedTask).Error; err != nil {
		t.Fatalf("创建已执行任务失败: %v", err)
	}
	createEstablishedRun(executedTask)

	// 2. 曾针对当前目标成功建立目录、随后被重置为 pending 的任务，目标消失同样必须标记 incomplete。
	vanishedRunHistoryDir := filepath.Join(t.TempDir(), "unmounted-volume-2", "node-1")
	resetTask := model.Task{
		Name: "reset-with-run-history", NodeID: node.ID, ExecutorType: "rsync",
		RsyncTarget: vanishedRunHistoryDir, Status: "pending",
	}
	if err := db.Create(&resetTask).Error; err != nil {
		t.Fatalf("创建带历史运行任务失败: %v", err)
	}
	createEstablishedRun(resetTask)

	// 3. 从未执行过的 pending 任务，目标尚未创建：应保持完整 0 GB，不误报 incomplete。
	pendingNeverRanDir := filepath.Join(t.TempDir(), "never-ran-pending")
	pendingTask := model.Task{
		Name: "never-ran-pending-task", NodeID: node.ID, ExecutorType: "rsync",
		RsyncTarget: pendingNeverRanDir, Status: "pending",
	}
	if err := db.Create(&pendingTask).Error; err != nil {
		t.Fatalf("创建未执行 pending 任务失败: %v", err)
	}

	// 4. 尚未物化的策略候选目录：应保持完整 0 GB，不误报 incomplete。
	uncreatedPolicyDir := filepath.Join(t.TempDir(), "uncreated-policy-base")
	uncreatedPolicy := model.Policy{
		Name: "uncreated-candidate-policy", SourcePath: "/src/c", TargetPath: uncreatedPolicyDir,
		CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true,
	}
	if err := model.CreatePolicyWithExplicitValues(db, &uncreatedPolicy, model.PolicyCreateExplicitColumns()...); err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: uncreatedPolicy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("绑定策略节点失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	r.GET("/overview/storage-usage", NewStorageUsageHandler(db).Get)

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Data struct {
			MountPoints           []mountPointInfo `json:"mount_points"`
			PerNode               []perNodeUsage   `json:"per_node"`
			Incomplete            bool             `json:"incomplete"`
			MountPointsIncomplete bool             `json:"mount_points_incomplete"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !body.Data.Incomplete || !body.Data.MountPointsIncomplete {
		t.Fatalf("已建立目录的任务目标消失（卸载卷）必须标记 incomplete=true 和 mount_points_incomplete=true，实际: %+v", body.Data)
	}

	byPath := make(map[string]perNodeUsage, len(body.Data.PerNode))
	for _, entry := range body.Data.PerNode {
		byPath[entry.Path] = entry
	}
	if got, ok := byPath[vanishedDir]; !ok || !got.Incomplete {
		t.Fatalf("已执行任务的消失目录不得报告为完整 0 GB: %+v", byPath)
	}
	if got, ok := byPath[vanishedRunHistoryDir]; !ok || !got.Incomplete {
		t.Fatalf("有当前目标 TaskRun 历史任务的消失目录不得报告为完整 0 GB: %+v", byPath)
	}
	if got, ok := byPath[pendingNeverRanDir]; !ok || got.Incomplete {
		t.Fatalf("从未执行的 pending 任务尚未创建目录时不应误报 incomplete: %+v", byPath)
	}
	foundCandidate := false
	for path, entry := range byPath {
		if strings.HasPrefix(path, uncreatedPolicyDir) {
			foundCandidate = true
			if entry.Incomplete {
				t.Fatalf("尚未物化的策略候选目录不应误报 incomplete: %+v", entry)
			}
		}
	}
	if !foundCandidate {
		t.Fatalf("未找到策略候选目录条目: %+v", byPath)
	}
}

func TestStorageUsageExcludesRemoteExecutorAbsoluteTargetsAndHandlesUncreatedOrMergedTargets(t *testing.T) {
	t.Run("excludes executed restic and rclone remote-node absolute targets without false incomplete", func(t *testing.T) {
		db := openStorageUsageTestDB(t)
		node := model.Node{Name: "remote-exec-node", Host: "10.0.18.1", Username: "root", AuthType: "key", BackupDir: "remote-exec-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
		now := time.Now().UTC()
		// 远端节点解释的绝对路径在 Core 本机并不存在，不得进入本机存储探测或触发 incomplete。
		for _, task := range []model.Task{
			{Name: "restic-remote-abs", NodeID: node.ID, ExecutorType: "restic", RsyncTarget: "/nonexistent/remote-restic-repo", Status: "success", LastRunAt: &now},
			{Name: "rclone-remote-abs", NodeID: node.ID, ExecutorType: "rclone", RsyncTarget: "/nonexistent/remote-rclone-dir", Status: "success", LastRunAt: &now},
		} {
			if err := db.Create(&task).Error; err != nil {
				t.Fatalf("创建远端执行器任务失败: %v", err)
			}
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.CtxRole, "admin")
			c.Set(middleware.CtxUserID, uint(1))
			c.Next()
		})
		r.GET("/overview/storage-usage", NewStorageUsageHandler(db).Get)

		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
		}
		var body struct {
			Data struct {
				MountPoints           []mountPointInfo `json:"mount_points"`
				PerNode               []perNodeUsage   `json:"per_node"`
				Incomplete            bool             `json:"incomplete"`
				MountPointsIncomplete bool             `json:"mount_points_incomplete"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if body.Data.Incomplete || body.Data.MountPointsIncomplete {
			t.Fatalf("远端节点解释的 restic/rclone 绝对路径不得触发 incomplete: %+v", body.Data)
		}
		if len(body.Data.MountPoints) != 0 || len(body.Data.PerNode) != 0 {
			t.Fatalf("远端执行器路径不应进入 Core 本地挂载点或节点用量列表，实际: %+v", body.Data)
		}
	})

	t.Run("isolated uncreated policy and never-run pending task report neither incomplete nor mount_points_incomplete", func(t *testing.T) {
		db := openStorageUsageTestDB(t)
		node := model.Node{Name: "fresh-node", Host: "10.0.18.2", Username: "root", AuthType: "key", BackupDir: "fresh-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}

		uncreatedBase := filepath.Join(t.TempDir(), "fresh-policy-base")
		p := model.Policy{
			Name: "fresh-policy", SourcePath: "/src", TargetPath: uncreatedBase,
			CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true,
		}
		if err := model.CreatePolicyWithExplicitValues(db, &p, model.PolicyCreateExplicitColumns()...); err != nil {
			t.Fatalf("创建策略失败: %v", err)
		}
		if err := db.Create(&model.PolicyNode{PolicyID: p.ID, NodeID: node.ID}).Error; err != nil {
			t.Fatalf("绑定策略节点失败: %v", err)
		}
		pendingDir := filepath.Join(t.TempDir(), "fresh-pending-task")
		if err := db.Create(&model.Task{
			Name: "fresh-pending", NodeID: node.ID, ExecutorType: "rsync",
			RsyncTarget: pendingDir, Status: "pending",
		}).Error; err != nil {
			t.Fatalf("创建 pending 任务失败: %v", err)
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.CtxRole, "admin")
			c.Set(middleware.CtxUserID, uint(1))
			c.Next()
		})
		r.GET("/overview/storage-usage", NewStorageUsageHandler(db).Get)

		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
		}
		var body struct {
			Data struct {
				PerNode               []perNodeUsage `json:"per_node"`
				Incomplete            bool           `json:"incomplete"`
				MountPointsIncomplete bool           `json:"mount_points_incomplete"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if body.Data.Incomplete || body.Data.MountPointsIncomplete {
			t.Fatalf("仅含未创建策略与未执行 pending 任务时不得误报 incomplete 或 mount_points_incomplete: %+v", body.Data)
		}
		if len(body.Data.PerNode) != 2 {
			t.Fatalf("期望返回 2 条未物化候选记录，实际: %+v", body.Data.PerNode)
		}
		for _, entry := range body.Data.PerNode {
			if entry.Incomplete {
				t.Fatalf("未创建目录不应标记 incomplete: %+v", entry)
			}
		}
	})

	t.Run("duplicate target merges expectedToExist regardless of pending or policy order", func(t *testing.T) {
		db := openStorageUsageTestDB(t)
		if err := db.AutoMigrate(&model.TaskRun{}); err != nil {
			t.Fatalf("迁移 task_runs 失败: %v", err)
		}
		node := model.Node{Name: "merge-node", Host: "10.0.18.3", Username: "root", AuthType: "key", BackupDir: "merge-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
		sharedVanishedDir := filepath.Join(t.TempDir(), "vanished-shared-dir")
		// 先插入同名策略与 pending 任务，再插入已建立目标的已执行任务，验证合并顺序无关。
		p := model.Policy{
			Name: "merge-policy", SourcePath: "/src", TargetPath: sharedVanishedDir,
			CronSpec: "0 * * * *", RetentionMode: "simple", Enabled: true,
		}
		if err := model.CreatePolicyWithExplicitValues(db, &p, model.PolicyCreateExplicitColumns()...); err != nil {
			t.Fatalf("创建策略失败: %v", err)
		}
		now := time.Now().UTC()
		if err := db.Create(&model.Task{
			Name: "pending-first", NodeID: node.ID, ExecutorType: "rsync",
			RsyncTarget: sharedVanishedDir, Status: "pending",
		}).Error; err != nil {
			t.Fatalf("创建 pending 任务失败: %v", err)
		}
		executedTask := model.Task{
			Name: "executed-second", NodeID: node.ID, ExecutorType: "rsync",
			RsyncTarget: sharedVanishedDir, Status: "success", LastRunAt: &now,
		}
		if err := db.Create(&executedTask).Error; err != nil {
			t.Fatalf("创建已执行任务失败: %v", err)
		}
		executedTask.Node = node
		if err := db.Create(&model.TaskRun{
			TaskID:                  executedTask.ID,
			NodeIDSnapshot:          node.ID,
			ExecutorTypeSnapshot:    "rsync",
			TriggerType:             "manual",
			Status:                  model.TaskRunStatusSuccess,
			StartedAt:               &now,
			FinishedAt:              &now,
			BackupGenerationState:   model.TaskRunGenerationStateVerified,
			BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(executedTask),
		}).Error; err != nil {
			t.Fatalf("创建已建立目标的 TaskRun 失败: %v", err)
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.CtxRole, "admin")
			c.Set(middleware.CtxUserID, uint(1))
			c.Next()
		})
		r.GET("/overview/storage-usage", NewStorageUsageHandler(db).Get)

		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
		}
		var body struct {
			Data struct {
				PerNode               []perNodeUsage `json:"per_node"`
				Incomplete            bool           `json:"incomplete"`
				MountPointsIncomplete bool           `json:"mount_points_incomplete"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if !body.Data.Incomplete || !body.Data.MountPointsIncomplete {
			t.Fatalf("同路径后续存在已建立目标的任务时必须合并 expectedToExist=true: %+v", body.Data)
		}
		if len(body.Data.PerNode) != 1 || !body.Data.PerNode[0].Incomplete {
			t.Fatalf("合并后的同路径节点条目必须标记 incomplete=true: %+v", body.Data.PerNode)
		}
	})

	t.Run("does not misreport incomplete for pre-execution canceled first run or retargeted uncreated destination", func(t *testing.T) {
		db := openStorageUsageTestDB(t)
		if err := db.AutoMigrate(&model.TaskRun{}); err != nil {
			t.Fatalf("迁移 task_runs 失败: %v", err)
		}
		node := model.Node{Name: "uninit-node", Host: "10.0.18.4", Username: "root", AuthType: "key", BackupDir: "uninit-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
		now := time.Now().UTC()

		// 场景 A：首次定时运行在执行前被取消（started_at = NULL，目录从未创建）。
		canceledBeforeStartDir := filepath.Join(t.TempDir(), "canceled-before-start")
		canceledTask := model.Task{
			Name: "canceled-before-exec", NodeID: node.ID, ExecutorType: "rsync",
			RsyncTarget: canceledBeforeStartDir, Status: model.TaskRunStatusCanceled,
		}
		if err := db.Create(&canceledTask).Error; err != nil {
			t.Fatalf("创建取消任务失败: %v", err)
		}
		canceledTask.Node = node
		if err := db.Create(&model.TaskRun{
			TaskID:                  canceledTask.ID,
			NodeIDSnapshot:          node.ID,
			ExecutorTypeSnapshot:    "rsync",
			TriggerType:             "cron",
			Status:                  model.TaskRunStatusCanceled,
			StartedAt:               nil,
			FinishedAt:              &now,
			BackupGenerationState:   model.TaskRunGenerationStateNoStart,
			BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(canceledTask),
		}).Error; err != nil {
			t.Fatalf("创建执行前取消 TaskRun 失败: %v", err)
		}

		// 场景 B：任务曾成功备份到目标 A，随后用户将目标改写为尚未创建的 B；
		// 任务行保留原 success 状态与 last_run_at，旧 TaskRun 指纹仍属于 A，不得把 B 误判为扫描失败。
		oldTargetA := filepath.Join(t.TempDir(), "old-target-a")
		newTargetB := filepath.Join(t.TempDir(), "new-uncreated-target-b")
		retargetedTask := model.Task{
			Name: "retargeted-a-to-b", NodeID: node.ID, ExecutorType: "rsync",
			RsyncTarget: oldTargetA, Status: model.TaskRunStatusSuccess, LastRunAt: &now,
		}
		if err := db.Create(&retargetedTask).Error; err != nil {
			t.Fatalf("创建改目标任务失败: %v", err)
		}
		retargetedTask.Node = node
		fpForTargetA := model.TaskRunBackupConfigFingerprint(retargetedTask)
		if err := db.Create(&model.TaskRun{
			TaskID:                  retargetedTask.ID,
			NodeIDSnapshot:          node.ID,
			ExecutorTypeSnapshot:    "rsync",
			TriggerType:             "manual",
			Status:                  model.TaskRunStatusSuccess,
			StartedAt:               &now,
			FinishedAt:              &now,
			BackupGenerationState:   model.TaskRunGenerationStateVerified,
			BackupConfigFingerprint: fpForTargetA,
		}).Error; err != nil {
			t.Fatalf("创建旧目标 A 的 TaskRun 失败: %v", err)
		}
		// 模拟用户将任务目标从 A 改为尚未创建的 B（保留原 status=success 与 last_run_at）。
		if err := db.Model(&model.Task{}).Where("id = ?", retargetedTask.ID).
			Update("rsync_target", newTargetB).Error; err != nil {
			t.Fatalf("更新任务目标到 B 失败: %v", err)
		}

		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.CtxRole, "admin")
			c.Set(middleware.CtxUserID, uint(1))
			c.Next()
		})
		r.GET("/overview/storage-usage", NewStorageUsageHandler(db).Get)

		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
		}
		var body struct {
			Data struct {
				PerNode               []perNodeUsage `json:"per_node"`
				Incomplete            bool           `json:"incomplete"`
				MountPointsIncomplete bool           `json:"mount_points_incomplete"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if body.Data.Incomplete || body.Data.MountPointsIncomplete {
			t.Fatalf("执行前取消或修改目标为未创建 B 时不得误报 incomplete 或 mount_points_incomplete: %+v", body.Data)
		}
		if len(body.Data.PerNode) != 2 {
			t.Fatalf("期望返回 2 条未初始化目录记录，实际: %+v", body.Data.PerNode)
		}
		for _, entry := range body.Data.PerNode {
			if entry.Incomplete {
				t.Fatalf("尚未初始化目录不应标记 incomplete=true: %+v", entry)
			}
		}
	})

	t.Run("requires completed current legacy evidence", func(t *testing.T) {
		cases := []struct {
			name        string
			change      func(*model.TaskRun)
			established bool
		}{
			{name: "success verified", established: true},
			{name: "warning dirty", change: func(r *model.TaskRun) {
				r.Status, r.BackupGenerationState = model.TaskRunStatusWarning, model.TaskRunGenerationStateDirty
			}, established: true},
			{name: "older missing executor snapshot", change: func(r *model.TaskRun) { r.ExecutorTypeSnapshot = "" }, established: true},
			{name: "dirty armed before executor", change: func(r *model.TaskRun) {
				r.Status, r.BackupGenerationState = model.TaskRunStatusCanceled, model.TaskRunGenerationStateDirty
			}},
			{name: "writing alone", change: func(r *model.TaskRun) { r.BackupGenerationState = model.TaskRunGenerationStateWriting }},
			{name: "managed success without generation", change: func(r *model.TaskRun) { r.BackupGenerationState = "" }},
			{name: "success dirty", change: func(r *model.TaskRun) { r.BackupGenerationState = model.TaskRunGenerationStateDirty }},
			{name: "warning verified", change: func(r *model.TaskRun) { r.Status = model.TaskRunStatusWarning }},
			{name: "missing start", change: func(r *model.TaskRun) { r.StartedAt = nil }},
			{name: "missing finish", change: func(r *model.TaskRun) { r.FinishedAt = nil }},
			{name: "no start generation", change: func(r *model.TaskRun) { r.BackupGenerationState = model.TaskRunGenerationStateNoStart }},
			{name: "other node", change: func(r *model.TaskRun) { r.NodeIDSnapshot++ }},
			{name: "zero node", change: func(r *model.TaskRun) { r.NodeIDSnapshot = 0 }},
			{name: "restore", change: func(r *model.TaskRun) { r.TriggerType = "restore" }},
			{name: "drill", change: func(r *model.TaskRun) { r.TriggerType = "drill" }},
			{name: "migration", change: func(r *model.TaskRun) { r.TriggerType = "migration" }},
			{name: "other executor", change: func(r *model.TaskRun) { r.ExecutorTypeSnapshot = "restic" }},
			{name: "old fingerprint", change: func(r *model.TaskRun) { r.BackupConfigFingerprint = "old" }},
			{name: "missing fingerprint", change: func(r *model.TaskRun) { r.BackupConfigFingerprint = "" }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				db := openStorageUsageTestDB(t)
				if err := db.AutoMigrate(&model.TaskRun{}); err != nil {
					t.Fatalf("迁移 task_runs 失败: %v", err)
				}
				node := model.Node{Name: "evidence-node", Host: "10.0.18.5", Username: "root", AuthType: "key", BackupDir: "evidence-node"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatalf("创建节点失败: %v", err)
				}
				p := model.Policy{
					Name: "evidence-policy", SourcePath: "/src", TargetPath: "/unused",
					CronSpec: "0 * * * *", ExcludeRules: "*.tmp", BwLimit: 42,
					PreHook: "echo before", PostHook: "echo after", HookTimeoutSeconds: 30,
					MaxExecutionSeconds: 60, BandwidthSchedule: "schedule", AppProfile: "test",
				}
				if err := model.CreatePolicyWithExplicitValues(db, &p, model.PolicyCreateExplicitColumns()...); err != nil {
					t.Fatalf("创建策略失败: %v", err)
				}
				target := filepath.Join(t.TempDir(), "missing")
				task := model.Task{Name: "evidence-task", NodeID: node.ID, PolicyID: &p.ID,
					ExecutorType: "rsync", RsyncSource: "/src", RsyncTarget: target, Status: "success"}
				if err := db.Create(&task).Error; err != nil {
					t.Fatalf("创建任务失败: %v", err)
				}
				task.Node, task.Policy = node, &p
				now := time.Now().UTC()
				run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: node.ID, ExecutorTypeSnapshot: "rsync",
					TriggerType: "cron", Status: model.TaskRunStatusSuccess, StartedAt: &now, FinishedAt: &now,
					BackupGenerationState:   model.TaskRunGenerationStateVerified,
					BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(task)}
				if run.BackupConfigFingerprint == "" {
					t.Fatal("生成任务指纹失败")
				}
				if tc.change != nil {
					tc.change(&run)
				}
				// Historical snapshots can differ from the current task. Preserve these rows as stored.
				if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&run).Error; err != nil {
					t.Fatalf("创建 TaskRun 失败: %v", err)
				}
				resp, body := callStorageUsage(t, db, "admin", 1)
				if resp.Code != http.StatusOK {
					t.Fatalf("期望 200，实际 %d: %s", resp.Code, resp.Body.String())
				}
				if len(body.Data.PerNode) != 1 || body.Data.PerNode[0].Path != target || body.Data.PerNode[0].Incomplete != tc.established {
					t.Fatalf("节点目录证据判断错误: established=%v, data=%+v", tc.established, body.Data)
				}
				var flags struct {
					Data struct {
						Incomplete            bool `json:"incomplete"`
						MountPointsIncomplete bool `json:"mount_points_incomplete"`
					} `json:"data"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &flags); err != nil {
					t.Fatalf("解析响应失败: %v", err)
				}
				if flags.Data.Incomplete != tc.established || flags.Data.MountPointsIncomplete != tc.established {
					t.Fatalf("挂载点证据判断错误: established=%v, flags=%+v", tc.established, flags.Data)
				}
			})
		}
	})

	t.Run("ignores unreadable protected fields on remote rsync targets", func(t *testing.T) {
		db := openStorageUsageTestDB(t)
		if err := db.AutoMigrate(&model.TaskRun{}); err != nil {
			t.Fatalf("迁移 task_runs 失败: %v", err)
		}
		node := model.Node{Name: "remote-rsync-node", Host: "10.0.18.8", Username: "root", AuthType: "key", BackupDir: "remote-rsync-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
		p := model.Policy{Name: "remote-rsync-policy", SourcePath: "/src", TargetPath: "/unused", CronSpec: "0 * * * *"}
		if err := model.CreatePolicyWithExplicitValues(db, &p, model.PolicyCreateExplicitColumns()...); err != nil {
			t.Fatalf("创建策略失败: %v", err)
		}
		if err := db.Model(&model.Policy{}).Where("id = ?", p.ID).UpdateColumn("pre_hook", "enc:v1:invalid").Error; err != nil {
			t.Fatalf("写入损坏策略密文失败: %v", err)
		}
		remoteConfig := model.Task{Name: "remote-corrupt-config", NodeID: node.ID, ExecutorType: "rsync",
			RsyncTarget: "remote:/backup", Status: "pending"}
		remotePolicy := model.Task{Name: "remote-corrupt-policy", NodeID: node.ID, PolicyID: &p.ID, ExecutorType: "rsync",
			RsyncTarget: "remote:/other", Status: "pending"}
		localTarget := filepath.Join(t.TempDir(), "missing")
		local := model.Task{Name: "local-task", NodeID: node.ID, ExecutorType: "rsync", RsyncTarget: localTarget, Status: "pending"}
		for _, task := range []*model.Task{&remoteConfig, &remotePolicy, &local} {
			if err := db.Create(task).Error; err != nil {
				t.Fatalf("创建任务失败: %v", err)
			}
		}
		if err := db.Model(&model.Task{}).Where("id = ?", remoteConfig.ID).UpdateColumn("executor_config", "enc:v1:invalid").Error; err != nil {
			t.Fatalf("写入损坏执行器密文失败: %v", err)
		}
		resp, body := callStorageUsage(t, db, "admin", 1)
		if resp.Code != http.StatusOK || len(body.Data.PerNode) != 1 || body.Data.PerNode[0].Path != localTarget || body.Data.PerNode[0].Incomplete {
			t.Fatalf("远端密文不得影响本地用量: status=%d data=%+v", resp.Code, body.Data)
		}
	})

	t.Run("batches current candidates", func(t *testing.T) {
		db := openStorageUsageTestDB(t)
		if err := db.AutoMigrate(&model.TaskRun{}); err != nil {
			t.Fatalf("迁移 task_runs 失败: %v", err)
		}
		base := t.TempDir()
		var last model.Task
		var lastNode model.Node
		var lastPolicy model.Policy
		for i := range 101 {
			node := model.Node{Name: fmt.Sprintf("batch-node-%d", i), Host: "10.0.18.6", Username: "root",
				AuthType: "key", BackupDir: fmt.Sprintf("batch-node-%d", i)}
			if err := db.Create(&node).Error; err != nil {
				t.Fatalf("创建节点失败: %v", err)
			}
			p := model.Policy{Name: fmt.Sprintf("batch-policy-%d", i), SourcePath: "/src", TargetPath: "/unused",
				CronSpec: "0 * * * *"}
			if err := model.CreatePolicyWithExplicitValues(db, &p, model.PolicyCreateExplicitColumns()...); err != nil {
				t.Fatalf("创建策略失败: %v", err)
			}
			last = model.Task{Name: fmt.Sprintf("batch-%d", i), NodeID: node.ID, PolicyID: &p.ID, ExecutorType: "rsync",
				RsyncTarget: filepath.Join(base, fmt.Sprintf("missing-%d", i)), Status: "pending"}
			if err := db.Create(&last).Error; err != nil {
				t.Fatalf("创建任务失败: %v", err)
			}
			lastNode, lastPolicy = node, p
		}
		last.Node, last.Policy = lastNode, &lastPolicy
		now := time.Now().UTC()
		if err := db.Create(&model.TaskRun{TaskID: last.ID, NodeIDSnapshot: lastNode.ID, TriggerType: "cron",
			Status: model.TaskRunStatusSuccess, StartedAt: &now, FinishedAt: &now,
			BackupGenerationState:   model.TaskRunGenerationStateVerified,
			BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(last)}).Error; err != nil {
			t.Fatalf("创建 TaskRun 失败: %v", err)
		}
		preloadQueries := map[string]int{}
		if err := db.Callback().Query().After("gorm:query").Register("storage-test-preload-bounds", func(tx *gorm.DB) {
			if tx.Statement.Table != "nodes" && tx.Statement.Table != "policies" {
				return
			}
			sql := tx.Statement.SQL.String()
			if !strings.Contains(sql, " IN ") && len(tx.Statement.Vars) != 1 {
				return
			}
			preloadQueries[tx.Statement.Table]++
			if len(tx.Statement.Vars) > 100 {
				t.Errorf("关联预加载参数超过批量上限: table=%s count=%d", tx.Statement.Table, len(tx.Statement.Vars))
			}
		}); err != nil {
			t.Fatalf("注册查询回调失败: %v", err)
		}
		resp, body := callStorageUsage(t, db, "admin", 1)
		if resp.Code != http.StatusOK || len(body.Data.PerNode) != 101 {
			t.Fatalf("分批查询失败: status=%d data=%+v", resp.Code, body.Data)
		}
		for _, entry := range body.Data.PerNode {
			if entry.Incomplete != (entry.Path == last.RsyncTarget) {
				t.Fatalf("分批查询证据判断错误: %+v", entry)
			}
		}
		if preloadQueries["nodes"] < 2 || preloadQueries["policies"] < 2 {
			t.Fatalf("节点和策略应分别分批加载: %+v", preloadQueries)
		}
	})

	t.Run("propagates evidence query errors", func(t *testing.T) {
		db := openStorageUsageTestDB(t)
		if err := db.Exec("CREATE TABLE task_runs (id INTEGER PRIMARY KEY)").Error; err != nil {
			t.Fatalf("创建不完整 task_runs 失败: %v", err)
		}
		node := model.Node{Name: "error-node", Host: "10.0.18.7", Username: "root", AuthType: "key", BackupDir: "error-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
		if err := db.Create(&model.Task{Name: "error-task", NodeID: node.ID, ExecutorType: "rsync",
			RsyncTarget: filepath.Join(t.TempDir(), "missing"), Status: "pending"}).Error; err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
		resp, _ := callStorageUsage(t, db, "admin", 1)
		if resp.Code != http.StatusInternalServerError {
			t.Fatalf("证据查询错误应返回 500，实际: %d", resp.Code)
		}
	})
}

func TestStorageUsageReportsRootSymlinkIncompleteAndSkipsChildSymlink(t *testing.T) {
	db := openStorageUsageTestDB(t)
	node := model.Node{Name: "symlink-node", Host: "10.0.17.1", Username: "root", AuthType: "key", BackupDir: "symlink-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	op := model.User{Username: "symlink-operator", Role: "operator", PasswordHash: "x"}
	if err := db.Create(&op).Error; err != nil {
		t.Fatalf("create operator: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: op.ID}).Error; err != nil {
		t.Fatalf("create ownership: %v", err)
	}

	root := t.TempDir()
	writeSizedFile := func(path string, size int64) {
		t.Helper()
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create file: %v", err)
		}
		if err := f.Truncate(size); err != nil {
			_ = f.Close()
			t.Fatalf("size file: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close file: %v", err)
		}
	}
	writeSizedFile(filepath.Join(root, "included"), 1<<25)
	external := filepath.Join(t.TempDir(), "excluded")
	writeSizedFile(external, 1<<26)
	if err := os.Symlink(external, filepath.Join(root, "child-link")); err != nil {
		t.Fatalf("create child symlink: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}
	slashedAlias := alias + string(os.PathSeparator)
	dottedAlias := alias + string(os.PathSeparator) + "." + string(os.PathSeparator)
	dangling := filepath.Join(t.TempDir(), "dangling-link")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), dangling); err != nil {
		t.Fatalf("create dangling symlink: %v", err)
	}
	for i, target := range []string{alias, slashedAlias, dottedAlias, dangling, root} {
		if err := db.Create(&model.Task{
			Name: fmt.Sprintf("symlink-task-%d", i), NodeID: node.ID,
			ExecutorType: "rsync", RsyncTarget: target, Status: "success",
		}).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	for _, tc := range []struct {
		role       string
		userID     uint
		wantMounts int
		wantNodes  int
	}{
		{role: "admin", userID: 1, wantMounts: 1, wantNodes: 5},
		{role: "operator", userID: op.ID, wantNodes: 5},
		{role: "operator", userID: op.ID + 1000},
	} {
		t.Run(fmt.Sprintf("%s-%d", tc.role, tc.userID), func(t *testing.T) {
			statCalls := 0
			h := NewStorageUsageHandler(db)
			h.statMount = func(path string) (string, unix.Statfs_t, error) {
				statCalls++
				if path == dangling {
					return "", unix.Statfs_t{}, os.ErrNotExist
				}
				return "same-volume", unix.Statfs_t{Bsize: 4096, Blocks: 262144, Bavail: 131072}, nil
			}
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(middleware.CtxRole, tc.role)
				c.Set(middleware.CtxUserID, tc.userID)
				c.Next()
			})
			r.GET("/overview/storage-usage", h.Get)
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/storage-usage", nil))
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
			var body struct {
				Data struct {
					MountPoints []mountPointInfo `json:"mount_points"`
					PerNode     []struct {
						NodeID     uint    `json:"node_id"`
						Path       string  `json:"path"`
						UsedGB     float64 `json:"used_gb"`
						Incomplete *bool   `json:"incomplete"`
					} `json:"per_node"`
					Incomplete            *bool `json:"incomplete"`
					MountPointsIncomplete *bool `json:"mount_points_incomplete"`
				} `json:"data"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Data.MountPoints == nil || len(body.Data.MountPoints) != tc.wantMounts || body.Data.PerNode == nil || len(body.Data.PerNode) != tc.wantNodes {
				t.Fatalf("unexpected arrays: %+v", body.Data)
			}
			if tc.role == "operator" && statCalls != 0 {
				t.Fatalf("operator must not probe mount statistics; calls = %d", statCalls)
			}
			if tc.role == "admin" && statCalls != 5 {
				t.Fatalf("admin must probe all mount candidates; calls = %d", statCalls)
			}
			if body.Data.MountPointsIncomplete != nil {
				t.Fatalf("successful mount scan must omit mount_points_incomplete: %+v", body.Data)
			}
			if tc.wantNodes == 0 {
				if body.Data.Incomplete != nil {
					t.Fatalf("empty response must omit incomplete: %+v", body.Data)
				}
				return
			}
			if body.Data.Incomplete == nil || !*body.Data.Incomplete {
				t.Fatalf("root symlink must mark response incomplete: %+v", body.Data)
			}
			seen := make(map[string]bool)
			for _, entry := range body.Data.PerNode {
				if entry.NodeID != node.ID {
					t.Fatalf("unexpected node: %+v", entry)
				}
				if seen[entry.Path] {
					t.Fatalf("duplicate target: %+v", entry)
				}
				seen[entry.Path] = true
				switch entry.Path {
				case alias, slashedAlias, dottedAlias, dangling:
					if entry.Incomplete == nil || !*entry.Incomplete || entry.UsedGB != 0 {
						t.Fatalf("root symlink must be incomplete without following it: %+v", entry)
					}
				case root:
					if entry.Incomplete != nil || entry.UsedGB != 0.03 {
						t.Fatalf("normal root must be complete and exclude child symlink: %+v", entry)
					}
				default:
					t.Fatalf("unexpected target: %+v", entry)
				}
			}
		})
	}
}

func TestDirSizeGBPreservesIntermediateSymlinkAndParentResolution(t *testing.T) {
	base := t.TempDir()
	resolvedBase := t.TempDir()
	resolvedChild := filepath.Join(resolvedBase, "child")
	resolvedTarget := filepath.Join(resolvedBase, "target")
	lexicalTarget := filepath.Join(base, "target")
	for _, dir := range []string{resolvedChild, filepath.Join(resolvedTarget, "nested"), lexicalTarget} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create directory: %v", err)
		}
	}
	writeSizedFile := func(path string, size int64) {
		t.Helper()
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create file: %v", err)
		}
		if err := f.Truncate(size); err != nil {
			_ = f.Close()
			t.Fatalf("size file: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close file: %v", err)
		}
	}
	writeSizedFile(filepath.Join(resolvedTarget, "nested", "included"), 1<<25)
	writeSizedFile(filepath.Join(lexicalTarget, "wrong-target"), 1<<26)
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(resolvedChild, alias); err != nil {
		t.Fatalf("create intermediate symlink: %v", err)
	}
	// Do not use filepath.Join here: it would clean away the symlink/.. pair.
	path := alias + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "target"
	used, complete := dirSizeGB(context.Background(), path, false)
	if !complete || used != float64(1<<25)/(1024*1024*1024) {
		t.Fatalf("scan must follow OS path resolution into the nested directory: used=%v complete=%v", used, complete)
	}
}

func TestDirSizeGBCountsFilesUnderNonUTF8DirectoryNames(t *testing.T) {
	root := t.TempDir()
	first := string([]byte{'d', 0xff})
	second := string([]byte{'n', 0xfe})
	dir := filepath.Join(root, first, second)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create directories with non-UTF-8 names: %v", err)
	}
	content := []byte("backup contents")
	if err := os.WriteFile(filepath.Join(dir, "backup"), content, 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	used, complete := dirSizeGB(context.Background(), root, false)
	want := float64(len(content)) / (1024 * 1024 * 1024)
	if !complete || used != want {
		t.Fatalf("scan must count files beneath non-UTF-8 directories: used=%v want=%v complete=%v", used, want, complete)
	}
}
