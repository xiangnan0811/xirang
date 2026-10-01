package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	nodePkg "xirang/backend/internal/node"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
)

type emergencyBackupTriggerSpy struct {
	calls   []uint
	errByID map[uint]error
}

func (s *emergencyBackupTriggerSpy) TriggerManual(taskID uint) (uint, error) {
	s.calls = append(s.calls, taskID)
	if err := s.errByID[taskID]; err != nil {
		return 0, err
	}
	return 101, nil
}

func (*emergencyBackupTriggerSpy) Cancel(uint) error             { return nil }
func (*emergencyBackupTriggerSpy) RemoveSchedule(uint)           {}
func (*emergencyBackupTriggerSpy) SyncSchedule(model.Task) error { return nil }

func TestEmergencyBackupHandlerReturnsTaskIDsAndSanitizesPartialErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(
		&model.User{}, &model.Node{}, &model.Task{}, &model.CredentialAccessGrant{},
		&model.CredentialAuditEvent{}, &model.AuditLog{},
	); err != nil {
		t.Fatalf("初始化紧急备份 handler 测试表失败: %v", err)
	}

	user := model.User{ID: 7, Username: "emergency-handler-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建 handler 测试用户失败: %v", err)
	}
	node := model.Node{ID: 42, Name: "emergency-handler-node", Host: "redacted", Port: 22, Username: "root", AuthType: "key", BackupDir: "emergency-handler-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建 handler 测试节点失败: %v", err)
	}
	first := model.Task{ID: 7, Name: "emergency-handler-task-7", NodeID: node.ID, Source: "policy", ExecutorType: "rsync", Status: model.TaskRunStatusPending, Enabled: true}
	second := model.Task{ID: 8, Name: "emergency-handler-task-8", NodeID: node.ID, Source: "policy", ExecutorType: "restic", Status: model.TaskRunStatusPending, Enabled: true}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("创建第一个紧急备份任务失败: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("创建第二个紧急备份任务失败: %v", err)
	}
	now := time.Now().UTC()
	for _, taskID := range []uint{first.ID, second.ID} {
		taskIDCopy := taskID
		grant := model.CredentialAccessGrant{
			RequesterUserID:     user.ID,
			RequesterUsername:   user.Username,
			RequesterRole:       user.Role,
			Action:              CredentialGrantActionTaskManualTrigger,
			Purpose:             sshutil.PurposeTaskCommand,
			TaskID:              &taskIDCopy,
			Reason:              "紧急备份 handler 回归",
			Status:              CredentialGrantStatusActive,
			RequestedTTLSeconds: 600,
			RequestedAt:         now,
			ExpiresAt:           now.Add(10 * time.Minute),
		}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatalf("创建任务 %d grant 失败: %v", taskID, err)
		}
	}

	spy := &emergencyBackupTriggerSpy{
		errByID: map[uint]error{8: errors.New("token=FAKE_REMOTE_TOKEN_FOR_TEST_ONLY https://user:pass@remote.example/secret")},
	}
	handler := NewNodeHandler(db, spy, nodePkg.NewNodeService(gormrepo.NewNodeRepository(db)))
	router := gin.New()
	router.POST("/nodes/:id/emergency-backup", func(c *gin.Context) {
		c.Set("userID", user.ID)
		c.Set("username", user.Username)
		c.Set("role", user.Role)
		c.Next()
	}, handler.EmergencyBackup)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/emergency-backup", node.ID), nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("完整 grant 的紧急备份应返回 200，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Data struct {
			Triggered int      `json:"triggered"`
			TaskIDs   []uint   `json:"task_ids"`
			Errors    []string `json:"errors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析紧急备份响应失败: %v", err)
	}
	if payload.Data.Triggered != 1 || len(payload.Data.TaskIDs) != 1 || payload.Data.TaskIDs[0] != first.ID {
		t.Fatalf("成功响应应返回 Task ID=7 而非 Run ID=101: %+v", payload.Data)
	}
	if len(payload.Data.Errors) != 1 || !strings.Contains(payload.Data.Errors[0], fmt.Sprint(second.ID)) {
		t.Fatalf("第二任务失败应保留部分失败错误: %+v", payload.Data.Errors)
	}
	for _, forbidden := range []string{"FAKE_REMOTE_TOKEN_FOR_TEST_ONLY", "https://user:pass@remote.example/secret"} {
		if strings.Contains(resp.Body.String(), forbidden) {
			t.Fatalf("紧急备份错误响应泄漏 %q: %s", forbidden, resp.Body.String())
		}
	}
	if len(spy.calls) != 2 || spy.calls[0] != first.ID || spy.calls[1] != second.ID {
		t.Fatalf("handler 应按任务 ID 顺序逐项触发，实际调用=%v", spy.calls)
	}
}

func TestEmergencyBackupHandlerAllowsEmptyTaskSetAfterRouteAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Task{}); err != nil {
		t.Fatalf("初始化空任务紧急备份 handler 测试表失败: %v", err)
	}
	node := model.Node{ID: 42, Name: "emergency-empty-node", Host: "redacted", Port: 22, Username: "root", AuthType: "key", BackupDir: "emergency-empty-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建空任务测试节点失败: %v", err)
	}
	spy := &emergencyBackupTriggerSpy{errByID: map[uint]error{}}
	handler := NewNodeHandler(db, spy, nodePkg.NewNodeService(gormrepo.NewNodeRepository(db)))
	router := gin.New()
	router.POST("/nodes/:id/emergency-backup", handler.EmergencyBackup)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/nodes/%d/emergency-backup", node.ID), nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"triggered":0`) || !strings.Contains(resp.Body.String(), `"task_ids":[]`) {
		t.Fatalf("路由准入后的空任务紧急备份应返回空成功结果，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}
	if len(spy.calls) != 0 {
		t.Fatalf("空任务不应调用 trigger，实际=%v", spy.calls)
	}
}
