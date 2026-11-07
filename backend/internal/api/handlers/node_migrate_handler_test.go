package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestRequireMigrationQuiescenceRejectsEnabledAndActiveTasks(t *testing.T) {
	db := openPolicyHandlerTestDB(t)
	enabled := model.Task{ID: 1, NodeID: 1, ExecutorType: "rsync", Enabled: true, CronSpec: "*/5 * * * *"}
	if err := requireMigrationQuiescence(db, []model.Task{enabled}); err == nil {
		t.Fatal("enabled/scheduled task was accepted for migration")
	}

	paused := model.Task{Name: "paused-migration", NodeID: 1, ExecutorType: "rsync", Status: "pending"}
	if err := db.Create(&paused).Error; err != nil {
		t.Fatalf("create paused task: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", paused.ID).Updates(map[string]any{"enabled": false, "cron_spec": ""}).Error; err != nil {
		t.Fatalf("pause task: %v", err)
	}
	if err := db.Create(&model.TaskRun{TaskID: paused.ID, Status: model.TaskRunStatusPending}).Error; err != nil {
		t.Fatalf("create active run: %v", err)
	}
	var loaded model.Task
	if err := db.First(&loaded, paused.ID).Error; err != nil {
		t.Fatalf("reload paused task: %v", err)
	}
	if err := requireMigrationQuiescence(db, []model.Task{loaded}); err == nil {
		t.Fatal("task with pending durable run was accepted for migration")
	}
	if err := db.Where("task_id = ?", paused.ID).Delete(&model.TaskRun{}).Error; err != nil {
		t.Fatalf("clear active run: %v", err)
	}
	if err := requireMigrationQuiescence(db, []model.Task{loaded}); err != nil {
		t.Fatalf("quiescent task rejected: %v", err)
	}
}

func migrationStageFixture(t *testing.T) (model.Task, model.Node, []policy.TargetOwner, string) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync is not installed")
	}
	base := t.TempDir()
	source := filepath.Join(t.TempDir(), "legacy-source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "payload.txt"), []byte("migration-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	policyID := uint(41)
	policyEntity := &model.Policy{ID: policyID, Name: "migration-policy", TargetPath: base}
	targetNode := model.Node{ID: 9, Name: "target"}
	task := model.Task{ID: 7, NodeID: 3, PolicyID: &policyID, Policy: policyEntity, ExecutorType: "rsync", RsyncTarget: source}
	owners := []policy.TargetOwner{{PolicyID: policyID, NodeID: task.NodeID, TaskID: task.ID, Target: source}}
	return task, targetNode, owners, source
}

func TestStageLocalBackupDataMigrationCopiesAndVerifiesIsolatedTarget(t *testing.T) {
	task, targetNode, owners, source := migrationStageFixture(t)
	items, targets, _, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners)
	if err != nil {
		t.Fatalf("stage migration: %v", err)
	}
	wantTarget := policy.PolicyNodeTargetPath(task.Policy.TargetPath, task.Policy.ID, targetNode.ID)
	if targets[task.ID] != wantTarget {
		t.Fatalf("migration target=%q, want %q", targets[task.ID], wantTarget)
	}
	if len(items) != 1 || items[0].Status != "copied" {
		t.Fatalf("migration result=%+v, want one copied item", items)
	}
	copied, err := os.ReadFile(filepath.Join(wantTarget, "nested", "payload.txt"))
	if err != nil {
		t.Fatalf("read isolated copy: %v", err)
	}
	if string(copied) != "migration-payload" {
		t.Fatalf("copied payload=%q", copied)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("historical source was removed: %v", err)
	}
}

func TestStageLocalBackupDataMigrationRejectsSharedLegacySource(t *testing.T) {
	task, targetNode, owners, source := migrationStageFixture(t)
	owners = append(owners, policy.TargetOwner{PolicyID: 99, NodeID: 8, TaskID: 99, Target: source})
	if _, _, _, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners); err == nil {
		t.Fatal("shared legacy source was accepted")
	}
	wantTarget := policy.PolicyNodeTargetPath(task.Policy.TargetPath, task.Policy.ID, targetNode.ID)
	if _, err := os.Stat(wantTarget); !os.IsNotExist(err) {
		t.Fatalf("unsafe source created destination %q: %v", wantTarget, err)
	}
}

func TestStageLocalBackupDataMigrationRejectsExistingDestination(t *testing.T) {
	task, targetNode, owners, _ := migrationStageFixture(t)
	wantTarget := policy.PolicyNodeTargetPath(task.Policy.TargetPath, task.Policy.ID, targetNode.ID)
	if err := os.MkdirAll(wantTarget, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(wantTarget, "existing.txt")
	if err := os.WriteFile(marker, []byte("do-not-merge"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners); err == nil {
		t.Fatal("existing destination was merged")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "do-not-merge" {
		t.Fatalf("existing destination changed: %q err=%v", got, err)
	}
}
func TestDirectoryTreeSnapshotsEqualRejectsSameSizeAndMetadataDrift(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	if err := os.WriteFile(filepath.Join(left, "payload"), []byte("AAAA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "payload"), []byte("AAAA"), 0o600); err != nil {
		t.Fatal(err)
	}
	leftSnap, err := snapshotDirectoryTree(left)
	if err != nil {
		t.Fatalf("snapshot left tree: %v", err)
	}
	rightSnap, err := snapshotDirectoryTree(right)
	if err != nil {
		t.Fatalf("snapshot right tree: %v", err)
	}
	if !directoryTreeSnapshotsEqual(leftSnap, rightSnap) {
		t.Fatal("identical trees were rejected by directoryTreeSnapshotsEqual")
	}

	// 1. 等长字节篡改（"AAAA" -> "BBBB"）必须被拒绝
	if err := os.WriteFile(filepath.Join(right, "payload"), []byte("BBBB"), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptSnap, err := snapshotDirectoryTree(right)
	if err != nil {
		t.Fatalf("snapshot equal-sized corrupted tree: %v", err)
	}
	if directoryTreeSnapshotsEqual(leftSnap, corruptSnap) {
		t.Fatal("same-size files with different bytes were accepted as equal")
	}

	// 2. 恢复相同字节但改变文件权限位（0600 -> 0640）必须被拒绝
	if err := os.WriteFile(filepath.Join(right, "payload"), []byte("AAAA"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(right, "payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	modeDriftSnap, err := snapshotDirectoryTree(right)
	if err != nil {
		t.Fatalf("snapshot mode-drift tree: %v", err)
	}
	if directoryTreeSnapshotsEqual(leftSnap, modeDriftSnap) {
		t.Fatal("files with different permission modes were accepted as equal")
	}

	// 3. 符号链接目标漂移必须被拒绝
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(right, "payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("payload", filepath.Join(left, "alias")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("other-target", filepath.Join(right, "alias")); err != nil {
			t.Fatal(err)
		}
		leftLinkSnap, err := snapshotDirectoryTree(left)
		if err != nil {
			t.Fatalf("snapshot left symlink tree: %v", err)
		}
		rightLinkSnap, err := snapshotDirectoryTree(right)
		if err != nil {
			t.Fatalf("snapshot right symlink tree: %v", err)
		}
		if directoryTreeSnapshotsEqual(leftLinkSnap, rightLinkSnap) {
			t.Fatal("symlinks with different targets were accepted as equal")
		}
	}
}

func installFakeMigrationRsync(t *testing.T, scriptBody string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script wrapper not supported on Windows")
	}
	realRsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	binDir := t.TempDir()
	wrapperPath := filepath.Join(binDir, "rsync")
	content := "#!/bin/sh\nset -eu\nREAL_RSYNC=" + fmt.Sprintf("%q", realRsync) + "\n" + scriptBody + "\n"
	if err := os.WriteFile(wrapperPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake rsync wrapper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestNodeMigrateHTTP_DirectoryTreeComparisonRealRoute 沿真实 POST /api/v1/nodes/:id/migrate
// 路由（挂载 RBAC("nodes:write") 与 OwnershipNodeCheck）验证：
//  1. 正常静默任务开启 migrateData=true 时，复制本地备份目录并通过 directoryTreeSnapshotsEqual 校验，
//     提交新目录、清除临时身份标记并更新 PolicyNode 与 Task.RsyncTarget（HTTP 200）；
//  2. 当复制出的暂存目录发生等长字节篡改（sourceAfter != stageSnapshot）时，真实 Migrate 入口在进入
//     数据库事务前以 HTTP 400（"验证迁移数据不一致"）拒绝，清理暂存目录、不产生目标目录残留、不改写数据库；
//  3. 当源目录在复制期间被等长篡改（sourceBefore != sourceAfter）时，真实 Migrate 入口以 HTTP 400
//     （"复制期间源备份目录发生变化，已拒绝迁移"）拒绝，清理暂存目录且数据库零写入。
func TestNodeMigrateHTTP_DirectoryTreeComparisonRealRoute(t *testing.T) {
	setupMigrateRouter := func(t *testing.T) (*gorm.DB, *gin.Engine, model.Node, model.Node, model.Policy, model.Task, string, string) {
		t.Helper()
		if _, err := exec.LookPath("rsync"); err != nil {
			t.Skip("rsync is not installed")
		}
		db := openPolicyHandlerTestDB(t)
		if err := db.AutoMigrate(&model.Node{}, &model.NodeOwner{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{}, &model.TaskRunEffect{}, &model.SystemSetting{}); err != nil {
			t.Fatalf("migrate tables: %v", err)
		}
		baseTargetDir := t.TempDir()
		sourceBackupDir := filepath.Join(t.TempDir(), "source-backup")
		if err := os.MkdirAll(filepath.Join(sourceBackupDir, "nested"), 0o750); err != nil {
			t.Fatalf("mkdir sourceBackupDir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sourceBackupDir, "nested", "payload.txt"), []byte("AAAA"), 0o600); err != nil {
			t.Fatalf("write payload.txt: %v", err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Symlink("payload.txt", filepath.Join(sourceBackupDir, "nested", "alias.lnk")); err != nil {
				t.Fatalf("symlink alias.lnk: %v", err)
			}
		}

		sourceNode := model.Node{Name: "src-node", Host: "10.0.10.1", Port: 22, Username: "root", AuthType: "password", Password: "FAKE_PASSWORD_FOR_TEST_ONLY", BackupDir: "/backup/src"}
		targetNode := model.Node{Name: "dst-node", Host: "10.0.10.2", Port: 22, Username: "root", AuthType: "password", Password: "FAKE_PASSWORD_FOR_TEST_ONLY", BackupDir: "/backup/dst"}
		if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&sourceNode).Error; err != nil {
			t.Fatalf("create sourceNode: %v", err)
		}
		if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&targetNode).Error; err != nil {
			t.Fatalf("create targetNode: %v", err)
		}
		policyEntity := model.Policy{Name: "mig-policy", SourcePath: "/data", TargetPath: baseTargetDir, CronSpec: "0 2 * * *", Enabled: false}
		if err := db.Create(&policyEntity).Error; err != nil {
			t.Fatalf("create policy: %v", err)
		}
		if err := db.Create(&model.PolicyNode{PolicyID: policyEntity.ID, NodeID: sourceNode.ID}).Error; err != nil {
			t.Fatalf("create policyNode: %v", err)
		}
		taskEntity := model.Task{
			Name:         "mig-policy - src-node",
			NodeID:       sourceNode.ID,
			PolicyID:     &policyEntity.ID,
			Source:       "policy",
			ExecutorType: "rsync",
			RsyncSource:  "/data",
			RsyncTarget:  sourceBackupDir,
			CronSpec:     "",
			Enabled:      false,
			Status:       "pending",
		}
		if err := db.Create(&taskEntity).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{"enabled": false, "cron_spec": ""}).Error; err != nil {
			t.Fatalf("ensure task paused: %v", err)
		}

		gin.SetMode(gin.TestMode)
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.CtxUserID, uint(1))
			c.Set(middleware.CtxUsername, "admin")
			c.Set(middleware.CtxRole, "admin")
			c.Next()
		})
		handler := NewNodeHandler(db, nil, nil)
		router.POST("/api/v1/nodes/:id/migrate", middleware.RBAC("nodes:write"), middleware.OwnershipNodeCheck(db), handler.Migrate)

		wantTarget := policy.PolicyNodeTargetPath(policyEntity.TargetPath, policyEntity.ID, targetNode.ID)
		return db, router, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget
	}
	assertNoStageResidueAndUnchangedDB := func(t *testing.T, db *gorm.DB, sourceNode, targetNode model.Node, policyEntity model.Policy, taskEntity model.Task, sourceBackupDir, wantTarget string) {
		t.Helper()
		if _, err := os.Stat(wantTarget); !os.IsNotExist(err) {
			t.Fatalf("rejected migration left target %s: err=%v", wantTarget, err)
		}
		parentEntries, err := os.ReadDir(filepath.Dir(wantTarget))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read target parent dir: %v", err)
		}
		for _, entry := range parentEntries {
			if strings.HasPrefix(entry.Name(), ".xirang-migration-") {
				t.Fatalf("rejected migration left staging directory residue %s in %s", entry.Name(), filepath.Dir(wantTarget))
			}
		}
		var unchangedTask model.Task
		if err := db.First(&unchangedTask, taskEntity.ID).Error; err != nil {
			t.Fatalf("reload task: %v", err)
		}
		if unchangedTask.NodeID != sourceNode.ID || unchangedTask.RsyncTarget != sourceBackupDir {
			t.Fatalf("DB task was modified despite migration rejection: %+v", unchangedTask)
		}
		var sourcePolicyNodes, targetPolicyNodes int64
		if err := db.Model(&model.PolicyNode{}).Where("policy_id = ? AND node_id = ?", policyEntity.ID, sourceNode.ID).Count(&sourcePolicyNodes).Error; err != nil {
			t.Fatalf("count source policy_nodes: %v", err)
		}
		if err := db.Model(&model.PolicyNode{}).Where("policy_id = ? AND node_id = ?", policyEntity.ID, targetNode.ID).Count(&targetPolicyNodes).Error; err != nil {
			t.Fatalf("count target policy_nodes: %v", err)
		}
		if sourcePolicyNodes != 1 || targetPolicyNodes != 0 {
			t.Fatalf("PolicyNode rows changed despite migration rejection: source=%d target=%d", sourcePolicyNodes, targetPolicyNodes)
		}
		var reloadedSource model.Node
		if err := db.First(&reloadedSource, sourceNode.ID).Error; err != nil {
			t.Fatalf("reload sourceNode: %v", err)
		}
		if reloadedSource.Archived {
			t.Fatalf("sourceNode was archived despite migration rejection")
		}
	}

	t.Run("success_copies_and_verifies_tree", func(t *testing.T) {
		db, router, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget := setupMigrateRouter(t)
		reqBody := fmt.Sprintf(`{"targetNodeId":%d,"archiveSource":true,"pausePolicies":true,"migrateData":true}`, targetNode.ID)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/migrate", sourceNode.ID), strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 on valid migration, got %d body=%s", resp.Code, resp.Body.String())
		}
		copiedBytes, err := os.ReadFile(filepath.Join(wantTarget, "nested", "payload.txt"))
		if err != nil || string(copiedBytes) != "AAAA" {
			t.Fatalf("expected copied payload AAAA at %s, got %q err=%v", wantTarget, copiedBytes, err)
		}
		// 确认迁移身份标记在事务提交后已被 finalizeOwnedMigrationDestinations 清除
		entries, err := os.ReadDir(wantTarget)
		if err != nil {
			t.Fatalf("read wantTarget: %v", err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), migrationIdentityMarkerPrefix) {
				t.Fatalf("migration identity marker %s was not finalized/removed", entry.Name())
			}
		}
		// 源目录仍完好保留
		if origBytes, err := os.ReadFile(filepath.Join(sourceBackupDir, "nested", "payload.txt")); err != nil || string(origBytes) != "AAAA" {
			t.Fatalf("source payload changed: %q err=%v", origBytes, err)
		}
		var updatedTask model.Task
		if err := db.First(&updatedTask, taskEntity.ID).Error; err != nil {
			t.Fatalf("reload task: %v", err)
		}
		if updatedTask.NodeID != targetNode.ID || updatedTask.RsyncTarget != wantTarget {
			t.Fatalf("unexpected migrated task state: %+v", updatedTask)
		}
		var sourcePolicyNodes, targetPolicyNodes int64
		if err := db.Model(&model.PolicyNode{}).Where("policy_id = ? AND node_id = ?", policyEntity.ID, sourceNode.ID).Count(&sourcePolicyNodes).Error; err != nil {
			t.Fatalf("count source policy_nodes: %v", err)
		}
		if err := db.Model(&model.PolicyNode{}).Where("policy_id = ? AND node_id = ?", policyEntity.ID, targetNode.ID).Count(&targetPolicyNodes).Error; err != nil {
			t.Fatalf("count target policy_nodes: %v", err)
		}
		if sourcePolicyNodes != 0 || targetPolicyNodes != 1 {
			t.Fatalf("expected PolicyNode cutover to targetNode (0/1), got source=%d target=%d", sourcePolicyNodes, targetPolicyNodes)
		}
	})

	t.Run("rejects_stage_same_size_corruption_before_cutover", func(t *testing.T) {
		db, router, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget := setupMigrateRouter(t)
		installFakeMigrationRsync(t, `
"$REAL_RSYNC" "$@"
eval "dst=\${$#}"
printf 'BBBB' > "${dst%/}/nested/payload.txt"
`)
		reqBody := fmt.Sprintf(`{"targetNodeId":%d,"archiveSource":true,"pausePolicies":true,"migrateData":true}`, targetNode.ID)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/migrate", sourceNode.ID), strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "验证迁移数据不一致") {
			t.Fatalf("expected 400 验证迁移数据不一致 on same-size stage corruption, got %d body=%s", resp.Code, resp.Body.String())
		}
		assertNoStageResidueAndUnchangedDB(t, db, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget)
	})

	t.Run("rejects_source_mutation_during_copy_before_cutover", func(t *testing.T) {
		db, router, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget := setupMigrateRouter(t)
		installFakeMigrationRsync(t, `
"$REAL_RSYNC" "$@"
printf 'BBBB' > `+fmt.Sprintf("%q", filepath.Join(sourceBackupDir, "nested", "payload.txt"))+`
`)
		reqBody := fmt.Sprintf(`{"targetNodeId":%d,"archiveSource":true,"pausePolicies":true,"migrateData":true}`, targetNode.ID)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/migrate", sourceNode.ID), strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "复制期间源备份目录发生变化，已拒绝迁移") {
			t.Fatalf("expected 400 复制期间源备份目录发生变化 on mid-copy source mutation, got %d body=%s", resp.Code, resp.Body.String())
		}
		assertNoStageResidueAndUnchangedDB(t, db, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget)
		var env Response
		if err := json.Unmarshal(resp.Body.Bytes(), &env); err != nil || env.Code != http.StatusBadRequest {
			t.Fatalf("unexpected response envelope: %+v err=%v", env, err)
		}
	})

	t.Run("rejects_stage_permission_mode_drift_before_cutover", func(t *testing.T) {
		db, router, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget := setupMigrateRouter(t)
		installFakeMigrationRsync(t, `
"$REAL_RSYNC" "$@"
eval "dst=\${$#}"
chmod 0640 "${dst%/}/nested/payload.txt"
`)
		reqBody := fmt.Sprintf(`{"targetNodeId":%d,"archiveSource":true,"pausePolicies":true,"migrateData":true}`, targetNode.ID)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/migrate", sourceNode.ID), strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "验证迁移数据不一致") {
			t.Fatalf("expected 400 验证迁移数据不一致 on stage permission mode drift, got %d body=%s", resp.Code, resp.Body.String())
		}
		assertNoStageResidueAndUnchangedDB(t, db, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget)
	})

	t.Run("rejects_source_permission_mode_drift_during_copy_before_cutover", func(t *testing.T) {
		db, router, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget := setupMigrateRouter(t)
		installFakeMigrationRsync(t, `
"$REAL_RSYNC" "$@"
chmod 0640 `+fmt.Sprintf("%q", filepath.Join(sourceBackupDir, "nested", "payload.txt"))+`
`)
		reqBody := fmt.Sprintf(`{"targetNodeId":%d,"archiveSource":true,"pausePolicies":true,"migrateData":true}`, targetNode.ID)
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/migrate", sourceNode.ID), strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)

		if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "复制期间源备份目录发生变化，已拒绝迁移") {
			t.Fatalf("expected 400 复制期间源备份目录发生变化 on mid-copy source mode drift, got %d body=%s", resp.Code, resp.Body.String())
		}
		assertNoStageResidueAndUnchangedDB(t, db, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget)
	})

	if runtime.GOOS != "windows" {
		t.Run("rejects_stage_symlink_target_drift_before_cutover", func(t *testing.T) {
			db, router, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget := setupMigrateRouter(t)
			installFakeMigrationRsync(t, `
"$REAL_RSYNC" "$@"
eval "dst=\${$#}"
ln -snf other.txt "${dst%/}/nested/alias.lnk"
`)
			reqBody := fmt.Sprintf(`{"targetNodeId":%d,"archiveSource":true,"pausePolicies":true,"migrateData":true}`, targetNode.ID)
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/migrate", sourceNode.ID), strings.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)

			if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "验证迁移数据不一致") {
				t.Fatalf("expected 400 验证迁移数据不一致 on stage symlink target drift, got %d body=%s", resp.Code, resp.Body.String())
			}
			assertNoStageResidueAndUnchangedDB(t, db, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget)
		})

		t.Run("rejects_source_symlink_target_drift_during_copy_before_cutover", func(t *testing.T) {
			db, router, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget := setupMigrateRouter(t)
			installFakeMigrationRsync(t, `
"$REAL_RSYNC" "$@"
ln -snf other.txt `+fmt.Sprintf("%q", filepath.Join(sourceBackupDir, "nested", "alias.lnk"))+`
`)
			reqBody := fmt.Sprintf(`{"targetNodeId":%d,"archiveSource":true,"pausePolicies":true,"migrateData":true}`, targetNode.ID)
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/migrate", sourceNode.ID), strings.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)

			if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "复制期间源备份目录发生变化，已拒绝迁移") {
				t.Fatalf("expected 400 复制期间源备份目录发生变化 on mid-copy source symlink drift, got %d body=%s", resp.Code, resp.Body.String())
			}
			assertNoStageResidueAndUnchangedDB(t, db, sourceNode, targetNode, policyEntity, taskEntity, sourceBackupDir, wantTarget)
		})
	}
}

func TestStageLocalBackupDataMigrationCleansEarlierDestinationOnLaterFailure(t *testing.T) {
	firstTask, targetNode, owners, firstSource := migrationStageFixture(t)
	secondPolicyID := uint(42)
	secondSource := filepath.Join(t.TempDir(), "missing-source")
	secondTask := model.Task{
		ID: 8, NodeID: 4, PolicyID: &secondPolicyID,
		Policy:       &model.Policy{ID: secondPolicyID, Name: "second-policy", TargetPath: firstTask.Policy.TargetPath},
		ExecutorType: "rsync", RsyncTarget: secondSource,
	}
	owners = append(owners, policy.TargetOwner{
		PolicyID: secondPolicyID, NodeID: secondTask.NodeID, TaskID: secondTask.ID, Target: secondSource,
	})
	if _, _, _, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{firstTask, secondTask}, targetNode, owners); err == nil {
		t.Fatal("later copy failure was accepted")
	}
	firstTarget := policy.PolicyNodeTargetPath(firstTask.Policy.TargetPath, firstTask.Policy.ID, targetNode.ID)
	if _, err := os.Stat(firstTarget); !os.IsNotExist(err) {
		t.Fatalf("earlier destination survived failed batch: %s err=%v", firstTarget, err)
	}
	if _, err := os.Stat(filepath.Join(firstSource, "nested", "payload.txt")); err != nil {
		t.Fatalf("original source was removed after failed batch: %v", err)
	}
}
func TestMigrationDestinationCleanupAfterCutoverRefusal(t *testing.T) {
	task, targetNode, owners, source := migrationStageFixture(t)
	_, targets, claims, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners)
	if err != nil {
		t.Fatalf("stage migration: %v", err)
	}
	cleanupOwnedMigrationDestinations(claims)
	if _, err := os.Stat(targets[task.ID]); !os.IsNotExist(err) {
		t.Fatalf("cutover refusal left owned destination: %s err=%v", targets[task.ID], err)
	}
	if _, err := os.Stat(filepath.Join(source, "nested", "payload.txt")); err != nil {
		t.Fatalf("cutover refusal removed original source: %v", err)
	}
}

func TestMigrationDestinationCleanupPreservesReplacedDirectory(t *testing.T) {
	task, targetNode, owners, _ := migrationStageFixture(t)
	_, targets, claims, err := stageLocalBackupDataMigrationWithClaims(context.Background(), []model.Task{task}, targetNode, owners)
	if err != nil {
		t.Fatalf("stage migration: %v", err)
	}
	target := targets[task.ID]
	if err := os.RemoveAll(target); err != nil {
		t.Fatalf("remove staged target: %v", err)
	}
	if err := os.MkdirAll(target, 0750); err != nil {
		t.Fatalf("create replacement target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "replacement.txt"), []byte("foreign"), 0600); err != nil {
		t.Fatalf("write replacement target: %v", err)
	}
	cleanupOwnedMigrationDestinations(claims)
	if _, err := os.Stat(filepath.Join(target, "replacement.txt")); err != nil {
		t.Fatalf("cleanup removed replacement target: %v", err)
	}
}
