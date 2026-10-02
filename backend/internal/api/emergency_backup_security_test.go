package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task"
	taskexec "xirang/backend/internal/task/executor"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const emergencyBackupTestJWTSecret = "FAKE_EMERGENCY_BACKUP_JWT_SECRET_FOR_TEST_ONLY"

type emergencyBackupExecutorFactory struct {
	executor taskexec.Executor
}

func (f emergencyBackupExecutorFactory) Resolve(string) taskexec.Executor {
	return f.executor
}

type emergencyBackupRecordingExecutor struct {
	mu      sync.Mutex
	calls   []uint
	callCh  chan uint
	errByID map[uint]error
}

func (e *emergencyBackupRecordingExecutor) Run(_ context.Context, taskEntity model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	e.mu.Lock()
	e.calls = append(e.calls, taskEntity.ID)
	e.mu.Unlock()
	if e.callCh != nil {
		select {
		case e.callCh <- taskEntity.ID:
		default:
		}
	}
	if err := e.errByID[taskEntity.ID]; err != nil {
		return 0, err
	}
	return 0, nil
}

func (e *emergencyBackupRecordingExecutor) snapshotCalls() []uint {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]uint(nil), e.calls...)
}

type emergencyBackupRouterFixture struct {
	db       *gorm.DB
	jwt      *auth.JWTManager
	manager  *task.Manager
	router   *gin.Engine
	executor *emergencyBackupRecordingExecutor
	users    map[string]model.User
	tokens   map[string]string
}

func openEmergencyBackupRouterDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	dsn := filepath.Join(t.TempDir(), "emergency-backup.db") + "?_busy_timeout=5000&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("打开紧急备份路由测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取紧急备份路由测试数据库连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(
		&model.User{}, &model.TokenRevocation{}, &model.AuditLog{},
		&model.Node{}, &model.NodeOwner{}, &model.Policy{}, &model.Task{}, &model.TaskRun{},
		&model.TaskCronOccurrence{}, &model.BackupCompletion{}, &model.TaskRunEffect{},
		&model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{}, &model.CredentialAccessGrant{},
		&model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{},
	); err != nil {
		t.Fatalf("初始化紧急备份路由测试数据库失败: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_task_runs_resource_active_unique
		ON task_runs(resource_key)
		WHERE resource_key <> '' AND backup_generation_state IN ('writing', 'unknown')`).Error; err != nil {
		t.Fatalf("初始化任务运行唯一索引失败: %v", err)
	}
	return db
}

func newEmergencyBackupRouterFixture(t *testing.T) *emergencyBackupRouterFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openEmergencyBackupRouterDB(t)
	executor := &emergencyBackupRecordingExecutor{
		callCh:  make(chan uint, 16),
		errByID: make(map[uint]error),
	}
	manager := task.NewManager(db, emergencyBackupExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("关闭紧急备份测试任务管理器失败: %v", err)
		}
	})

	jwtManager := auth.NewJWTManager(emergencyBackupTestJWTSecret, time.Hour)
	jwtManager.SetDB(db)
	users := map[string]model.User{
		"admin":    {ID: 1, Username: "emergency-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TOTPEnabled: true},
		"operator": {ID: 2, Username: "emergency-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator", TOTPEnabled: true},
		"viewer":   {ID: 3, Username: "emergency-viewer", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "viewer", TOTPEnabled: true},
		"other":    {ID: 4, Username: "emergency-other-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TOTPEnabled: true},
	}
	tokens := make(map[string]string, len(users))
	for key, user := range users {
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("创建 %s 测试用户失败: %v", key, err)
		}
		users[key] = user
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("生成 %s 测试 token 失败: %v", key, err)
		}
		tokens[key] = token
	}

	return &emergencyBackupRouterFixture{
		db:       db,
		jwt:      jwtManager,
		manager:  manager,
		router:   NewRouter(Dependencies{DB: db, JWTManager: jwtManager, TaskManager: manager}),
		executor: executor,
		users:    users,
		tokens:   tokens,
	}
}

func seedEmergencyBackupNode(t *testing.T, db *gorm.DB, id uint) model.Node {
	t.Helper()
	node := model.Node{
		ID:        id,
		Name:      fmt.Sprintf("emergency-node-%d", id),
		Host:      "redacted",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: fmt.Sprintf("emergency-node-%d", id),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建紧急备份节点失败: %v", err)
	}
	return node
}

func seedEmergencyBackupTask(t *testing.T, db *gorm.DB, id, nodeID uint, enabled bool) model.Task {
	t.Helper()
	taskEntity := model.Task{
		ID:           id,
		Name:         fmt.Sprintf("emergency-task-%d", id),
		NodeID:       nodeID,
		ExecutorType: "rsync",
		Status:       model.TaskRunStatusPending,
		Source:       "policy",
		Enabled:      enabled,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建紧急备份任务 %d 失败: %v", id, err)
	}
	return taskEntity
}

func seedEmergencyBackupGrant(t *testing.T, db *gorm.DB, user model.User, taskID uint, action, purpose, status string, expiresAt time.Time) model.CredentialAccessGrant {
	t.Helper()
	taskIDCopy := taskID
	now := time.Now().UTC()
	grant := model.CredentialAccessGrant{
		RequesterUserID:     user.ID,
		RequesterUsername:   user.Username,
		RequesterRole:       user.Role,
		Action:              action,
		Purpose:             purpose,
		TaskID:              &taskIDCopy,
		Reason:              "紧急备份安全回归",
		Status:              status,
		RequestedTTLSeconds: 600,
		RequestedAt:         now,
		ExpiresAt:           expiresAt,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("创建紧急备份 grant 失败: %v", err)
	}
	return grant
}

func generateEmergencyBackupProof(t *testing.T, fixture *emergencyBackupRouterFixture, userKey string, action auth.StepUpAction) string {
	t.Helper()
	proof, _, err := fixture.jwt.GenerateStepUpToken(fixture.users[userKey], action)
	if err != nil {
		t.Fatalf("生成 %s step-up proof 失败: %v", action, err)
	}
	return proof
}

func generateExpiredEmergencyBackupProof(t *testing.T, user model.User, action auth.StepUpAction) string {
	t.Helper()
	expiresAt := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	issuedAt := expiresAt.Add(-5 * time.Minute)
	claims := auth.Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		Purpose:      auth.PurposeStepUp,
		StepUpAction: action,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        strings.Repeat("a", 32),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Subject:   fmt.Sprintf("%d", user.ID),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(emergencyBackupTestJWTSecret))
	if err != nil {
		t.Fatalf("生成过期 step-up proof 失败: %v", err)
	}
	return token
}

type emergencyBackupResponse struct {
	Code int `json:"code"`
	Data struct {
		Triggered int      `json:"triggered"`
		TaskIDs   []uint   `json:"task_ids"`
		Errors    []string `json:"errors"`
	} `json:"data"`
}

func emergencyBackupRequest(t *testing.T, router http.Handler, nodeID uint, token, proof string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/emergency-backup", nodeID), nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if proof != "" {
		req.Header.Set(handlers.StepUpHeaderName, proof)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func manualTriggerRequest(t *testing.T, router http.Handler, taskID uint, token, proof string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/trigger", taskID), nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if proof != "" {
		req.Header.Set(handlers.StepUpHeaderName, proof)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func decodeEmergencyBackupResponse(t *testing.T, resp *httptest.ResponseRecorder) emergencyBackupResponse {
	t.Helper()
	var payload emergencyBackupResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析紧急备份响应失败: status=%d body=%s err=%v", resp.Code, resp.Body.String(), err)
	}
	return payload
}

func countTaskRuns(t *testing.T, db *gorm.DB, taskIDs ...uint) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.TaskRun{}).Where("task_id IN ?", taskIDs).Count(&count).Error; err != nil {
		t.Fatalf("统计任务运行记录失败: %v", err)
	}
	return count
}

func assertNoEmergencyBackupExecution(t *testing.T, fixture *emergencyBackupRouterFixture, taskIDs ...uint) {
	t.Helper()
	if got := countTaskRuns(t, fixture.db, taskIDs...); got != 0 {
		t.Fatalf("授权拒绝后不应创建 TaskRun，实际 %d", got)
	}
	if calls := fixture.executor.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("授权拒绝后不应调用 executor，实际调用 %v", calls)
	}
}

func TestEmergencyBackupRouterRequiresStepUpForEmptyTaskSet(t *testing.T) {
	fixture := newEmergencyBackupRouterFixture(t)
	node := seedEmergencyBackupNode(t, fixture.db, 100)

	resp := emergencyBackupRequest(t, fixture.router, node.ID, fixture.tokens["admin"], "")
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "STEP_UP_REQUIRED") {
		t.Fatalf("空任务紧急备份缺少 step-up 时应拒绝，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}
	assertNoEmergencyBackupExecution(t, fixture)
}

func TestEmergencyBackupRouterUsesRealRBACAndOwnership(t *testing.T) {
	tests := []struct {
		name       string
		userKey    string
		addOwner   bool
		withAuth   bool
		wantStatus int
	}{
		{name: "admin_allowed", userKey: "admin", withAuth: true, wantStatus: http.StatusOK},
		{name: "owned_operator_allowed", userKey: "operator", addOwner: true, withAuth: true, wantStatus: http.StatusOK},
		{name: "viewer_denied_by_rbac", userKey: "viewer", withAuth: true, wantStatus: http.StatusForbidden},
		{name: "unowned_operator_denied", userKey: "operator", withAuth: true, wantStatus: http.StatusForbidden},
		{name: "missing_authentication", wantStatus: http.StatusUnauthorized},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newEmergencyBackupRouterFixture(t)
			node := seedEmergencyBackupNode(t, fixture.db, 100)
			taskEntity := seedEmergencyBackupTask(t, fixture.db, 500, node.ID, true)
			if testCase.addOwner {
				if err := fixture.db.Create(&model.NodeOwner{NodeID: node.ID, UserID: fixture.users[testCase.userKey].ID}).Error; err != nil {
					t.Fatalf("创建节点负责人失败: %v", err)
				}
			}

			token, proof := "", ""
			if testCase.withAuth {
				token = fixture.tokens[testCase.userKey]
				proof = generateEmergencyBackupProof(t, fixture, testCase.userKey, auth.StepUpActionTaskManualTrigger)
				seedEmergencyBackupGrant(t, fixture.db, fixture.users[testCase.userKey], taskEntity.ID,
					handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand,
					handlers.CredentialGrantStatusActive, time.Now().UTC().Add(10*time.Minute))
			}

			resp := emergencyBackupRequest(t, fixture.router, node.ID, token, proof)
			if resp.Code != testCase.wantStatus {
				t.Fatalf("紧急备份权限结果 status=%d body=%s，want %d", resp.Code, resp.Body.String(), testCase.wantStatus)
			}
			if testCase.wantStatus != http.StatusOK {
				assertNoEmergencyBackupExecution(t, fixture, taskEntity.ID)
			}
		})
	}
}

func TestEmergencyBackupRouterChecksAllTaskGrantsBeforeManagerTrigger(t *testing.T) {
	fixture := newEmergencyBackupRouterFixture(t)
	node := seedEmergencyBackupNode(t, fixture.db, 100)
	first := seedEmergencyBackupTask(t, fixture.db, 500, node.ID, true)
	second := seedEmergencyBackupTask(t, fixture.db, 501, node.ID, true)
	user := fixture.users["admin"]
	seedEmergencyBackupGrant(t, fixture.db, user, first.ID,
		handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand,
		handlers.CredentialGrantStatusActive, time.Now().UTC().Add(10*time.Minute))
	proof := generateEmergencyBackupProof(t, fixture, "admin", auth.StepUpActionTaskManualTrigger)

	resp := emergencyBackupRequest(t, fixture.router, node.ID, fixture.tokens["admin"], proof)
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "CREDENTIAL_GRANT_REQUIRED") {
		t.Fatalf("两项任务仅一项授权时应在触发前拒绝，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}
	assertNoEmergencyBackupExecution(t, fixture, first.ID, second.ID)
}

func TestEmergencyBackupRouterReturnsSuccessfulTaskIDsAndPartialErrors(t *testing.T) {
	fixture := newEmergencyBackupRouterFixture(t)
	node := seedEmergencyBackupNode(t, fixture.db, 100)
	first := seedEmergencyBackupTask(t, fixture.db, 500, node.ID, true)
	second := seedEmergencyBackupTask(t, fixture.db, 501, node.ID, false)
	dependsOn := first.ID
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", second.ID).Update("depends_on_task_id", dependsOn).Error; err != nil {
		t.Fatalf("将第二任务置为依赖任务以触发既有手动触发保护失败: %v", err)
	}
	user := fixture.users["admin"]
	for _, taskEntity := range []model.Task{first, second} {
		seedEmergencyBackupGrant(t, fixture.db, user, taskEntity.ID,
			handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand,
			handlers.CredentialGrantStatusActive, time.Now().UTC().Add(10*time.Minute))
	}
	proof := generateEmergencyBackupProof(t, fixture, "admin", auth.StepUpActionTaskManualTrigger)

	resp := emergencyBackupRequest(t, fixture.router, node.ID, fixture.tokens["admin"], proof)
	if resp.Code != http.StatusOK {
		t.Fatalf("完整授权的紧急备份应返回 200，实际 status=%d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeEmergencyBackupResponse(t, resp)
	if payload.Data.Triggered != 1 || len(payload.Data.TaskIDs) != 1 || payload.Data.TaskIDs[0] != first.ID {
		t.Fatalf("紧急备份成功响应必须返回成功 Task ID 而非 Run ID: %+v", payload.Data)
	}
	if len(payload.Data.Errors) != 1 || !strings.Contains(payload.Data.Errors[0], fmt.Sprint(second.ID)) {
		t.Fatalf("第二项触发失败应保留部分失败错误: %+v", payload.Data.Errors)
	}
	if got := countTaskRuns(t, fixture.db, first.ID); got != 1 {
		t.Fatalf("成功任务应创建 1 条 TaskRun，实际 %d", got)
	}
	if got := countTaskRuns(t, fixture.db, second.ID); got != 0 {
		t.Fatalf("暂停任务不应创建 TaskRun，实际 %d", got)
	}
	select {
	case calledTaskID := <-fixture.executor.callCh:
		if calledTaskID != first.ID {
			t.Fatalf("executor 应执行成功任务 %d，实际 %d", first.ID, calledTaskID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("等待成功任务进入 executor 超时")
	}
}

func TestTaskAndEmergencyRoutesRejectInvalidStepUpProofs(t *testing.T) {
	for _, routeName := range []string{"ordinary", "emergency"} {
		for _, proofCase := range []string{"missing", "wrong_action", "expired"} {
			t.Run(routeName+"/"+proofCase, func(t *testing.T) {
				fixture := newEmergencyBackupRouterFixture(t)
				node := seedEmergencyBackupNode(t, fixture.db, 100)
				taskEntity := seedEmergencyBackupTask(t, fixture.db, 500, node.ID, true)
				user := fixture.users["admin"]
				seedEmergencyBackupGrant(t, fixture.db, user, taskEntity.ID,
					handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand,
					handlers.CredentialGrantStatusActive, time.Now().UTC().Add(10*time.Minute))

				proof := ""
				switch proofCase {
				case "wrong_action":
					proof = generateEmergencyBackupProof(t, fixture, "admin", auth.StepUpActionTaskBatchTrigger)
				case "expired":
					proof = generateExpiredEmergencyBackupProof(t, user, auth.StepUpActionTaskManualTrigger)
				}

				var resp *httptest.ResponseRecorder
				if routeName == "ordinary" {
					resp = manualTriggerRequest(t, fixture.router, taskEntity.ID, fixture.tokens["admin"], proof)
				} else {
					resp = emergencyBackupRequest(t, fixture.router, node.ID, fixture.tokens["admin"], proof)
				}
				if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "STEP_UP_REQUIRED") {
					t.Fatalf("%s 路由 %s proof 应拒绝，实际 status=%d body=%s", routeName, proofCase, resp.Code, resp.Body.String())
				}
				assertNoEmergencyBackupExecution(t, fixture, taskEntity.ID)
			})
		}
	}
}

func TestTaskAndEmergencyRoutesRejectInvalidGrantTuples(t *testing.T) {
	for _, routeName := range []string{"ordinary", "emergency"} {
		for _, grantCase := range []string{"missing", "wrong_action", "wrong_task", "wrong_user", "wrong_purpose", "expired", "revoked"} {
			t.Run(routeName+"/"+grantCase, func(t *testing.T) {
				fixture := newEmergencyBackupRouterFixture(t)
				node := seedEmergencyBackupNode(t, fixture.db, 100)
				taskEntity := seedEmergencyBackupTask(t, fixture.db, 500, node.ID, true)
				user := fixture.users["admin"]
				proof := generateEmergencyBackupProof(t, fixture, "admin", auth.StepUpActionTaskManualTrigger)

				switch grantCase {
				case "wrong_action":
					seedEmergencyBackupGrant(t, fixture.db, user, taskEntity.ID,
						handlers.CredentialGrantActionTaskBatchTrigger, sshutil.PurposeTaskCommand,
						handlers.CredentialGrantStatusActive, time.Now().UTC().Add(10*time.Minute))
				case "wrong_task":
					seedEmergencyBackupGrant(t, fixture.db, user, taskEntity.ID+1,
						handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand,
						handlers.CredentialGrantStatusActive, time.Now().UTC().Add(10*time.Minute))
				case "wrong_user":
					seedEmergencyBackupGrant(t, fixture.db, fixture.users["other"], taskEntity.ID,
						handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand,
						handlers.CredentialGrantStatusActive, time.Now().UTC().Add(10*time.Minute))
				case "wrong_purpose":
					seedEmergencyBackupGrant(t, fixture.db, user, taskEntity.ID,
						handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskBackup,
						handlers.CredentialGrantStatusActive, time.Now().UTC().Add(10*time.Minute))
				case "expired":
					seedEmergencyBackupGrant(t, fixture.db, user, taskEntity.ID,
						handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand,
						handlers.CredentialGrantStatusActive, time.Now().UTC().Add(-time.Minute))
				case "revoked":
					seedEmergencyBackupGrant(t, fixture.db, user, taskEntity.ID,
						handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand,
						handlers.CredentialGrantStatusRevoked, time.Now().UTC().Add(10*time.Minute))
				}

				var resp *httptest.ResponseRecorder
				if routeName == "ordinary" {
					resp = manualTriggerRequest(t, fixture.router, taskEntity.ID, fixture.tokens["admin"], proof)
				} else {
					resp = emergencyBackupRequest(t, fixture.router, node.ID, fixture.tokens["admin"], proof)
				}
				if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "CREDENTIAL_GRANT_REQUIRED") {
					t.Fatalf("%s 路由 %s grant tuple 应拒绝，实际 status=%d body=%s", routeName, grantCase, resp.Code, resp.Body.String())
				}
				assertNoEmergencyBackupExecution(t, fixture, taskEntity.ID)
			})
		}
	}
}
