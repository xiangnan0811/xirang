package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	backupruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestFullRouterSnapshotRestoreLiveAndStepUpMatrix(t *testing.T) {
	closed := newLegacyRestoreRouter(t, false)
	live := newLegacyRestoreRouter(t, true)
	body := `{"includes":["/file"],"targetPath":"/tmp/recovery"}`
	path := fmt.Sprintf("/api/v1/tasks/%d/snapshots/%s/restore", closed.taskID, strings.Repeat("a", 12))

	adminClosed := closed.request(http.MethodPost, path, closed.adminToken, closed.adminProof, body)
	if adminClosed.Code != http.StatusForbidden || !strings.Contains(adminClosed.Body.String(), "备份资产功能未启用") {
		t.Fatalf("admin live-false + step-up + grant status=%d body=%s", adminClosed.Code, adminClosed.Body.String())
	}

	adminNoProof := live.request(http.MethodPost, path, live.adminToken, "", body)
	if adminNoProof.Code != http.StatusForbidden || !strings.Contains(adminNoProof.Body.String(), "需要二次验证") {
		t.Fatalf("admin live-true without step-up status=%d body=%s", adminNoProof.Code, adminNoProof.Body.String())
	}

	adminNoGrant := live.request(http.MethodPost, path, live.adminToken, live.adminProof, body)
	if adminNoGrant.Code != http.StatusForbidden || !strings.Contains(adminNoGrant.Body.String(), "授权") {
		// grant fixture is only created on the closed router; live router has no grant
		if !strings.Contains(adminNoGrant.Body.String(), "grant") && !strings.Contains(adminNoGrant.Body.String(), "credential") &&
			!strings.Contains(adminNoGrant.Body.String(), "需要") {
			t.Fatalf("admin live-true without grant status=%d body=%s", adminNoGrant.Code, adminNoGrant.Body.String())
		}
	}

	granted := newLegacyRestoreRouter(t, true)
	createLegacyRestoreGrant(t, granted.db, granted.admin, granted.taskID)
	adminLive := granted.request(http.MethodPost, path, granted.adminToken, granted.adminProof, body)
	if adminLive.Code == http.StatusForbidden && strings.Contains(adminLive.Body.String(), "备份资产功能未启用") {
		t.Fatalf("admin live-true + step-up + grant must pass FeatureLive: %s", adminLive.Body.String())
	}
	if adminLive.Code == http.StatusUnauthorized || adminLive.Code == http.StatusGone {
		t.Fatalf("admin live-true + step-up + grant unexpected status=%d body=%s", adminLive.Code, adminLive.Body.String())
	}
}

type legacyRestoreRouterFixture struct {
	router     *gin.Engine
	db         *gorm.DB
	admin      model.User
	adminToken string
	adminProof string
	taskID     uint
}

func (fixture legacyRestoreRouterFixture) request(method, path, token, proof, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if proof != "" {
		req.Header.Set("X-Xirang-Step-Up", proof)
	}
	rec := httptest.NewRecorder()
	fixture.router.ServeHTTP(rec, req)
	return rec
}

func newLegacyRestoreRouter(t *testing.T, live bool) legacyRestoreRouterFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:%s-%t-%d?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"), live, time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}, &model.AuditLog{},
		&model.CredentialAccessGrant{}, &model.CredentialAuditEvent{}, &model.SystemSetting{},
	); err != nil {
		t.Fatal(err)
	}
	jwtManager := auth.NewJWTManager("FAKE_LEGACY_RESTORE_MATRIX_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	admin := model.User{
		Username: fmt.Sprintf("legacy-restore-admin-%t-%d", live, time.Now().UnixNano()), PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "admin", TOTPEnabled: true,
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	token, err := jwtManager.GenerateToken(admin)
	if err != nil {
		t.Fatal(err)
	}
	proof, _, err := jwtManager.GenerateStepUpToken(admin, auth.StepUpActionSnapshotRestore)
	if err != nil {
		t.Fatal(err)
	}
	node := model.Node{Name: "legacy-restore-node", Host: "10.0.0.8", Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{Name: "legacy-restore-task", NodeID: node.ID, ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	if !live {
		createLegacyRestoreGrant(t, db, admin, taskEntity.ID)
	}
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	readiness := backupruntime.ExistingInstallReadyUnacked()
	if live {
		readiness = backupruntime.FreshInstallReady()
	}
	runtime := backupruntime.EnablementRuntime(readiness, nil).
		WithFoundation(backupasset.NewFoundationService(settingsService))
	return legacyRestoreRouterFixture{
		router: NewRouter(Dependencies{DB: db, JWTManager: jwtManager, BackupAssets: runtime}),
		db:     db, admin: admin, adminToken: token, adminProof: proof, taskID: taskEntity.ID,
	}
}

func createLegacyRestoreGrant(t *testing.T, db *gorm.DB, user model.User, taskID uint) {
	t.Helper()
	now := time.Now().UTC()
	taskRef := taskID
	grant := model.CredentialAccessGrant{
		RequesterUserID: user.ID, RequesterUsername: user.Username, RequesterRole: user.Role,
		Action: string(auth.StepUpActionSnapshotRestore), Purpose: sshutil.PurposeSnapshot,
		TaskID: &taskRef, Reason: "例行恢复", Status: "active", RequestedTTLSeconds: 600,
		RequestedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
}
