package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLegacySnapshotReadsGoneForAdminOperatorViewer(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.Task{}, &model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	jwtManager := auth.NewJWTManager("FAKE_LEGACY_SNAPSHOT_HTTP_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	tokens := map[string]string{}
	users := map[string]model.User{}
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{
			Username: "legacy-snapshot-" + role, PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
			Role: role, TOTPEnabled: true,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		token, tokenErr := jwtManager.GenerateToken(user)
		if tokenErr != nil {
			t.Fatal(tokenErr)
		}
		tokens[role] = token
		users[role] = user
	}
	node := model.Node{Name: "legacy-snapshot-node", Host: "10.0.0.8", Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: node.ID, UserID: users["operator"].ID}).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{Name: "legacy-snapshot-task", NodeID: node.ID, ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	sid := strings.Repeat("a", 12)
	reads := []string{
		fmt.Sprintf("/api/v1/tasks/%d/snapshots", taskEntity.ID),
		fmt.Sprintf("/api/v1/tasks/%d/snapshots/%s/files", taskEntity.ID, sid),
		fmt.Sprintf("/api/v1/tasks/%d/snapshots/search?q=secret", taskEntity.ID),
		fmt.Sprintf("/api/v1/tasks/%d/snapshots/diff?snap1=%s&snap2=%s", taskEntity.ID, sid, strings.Repeat("b", 12)),
	}
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range reads {
		if response := request(http.MethodGet, path, "", ""); response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s status=%d body=%s", path, response.Code, response.Body.String())
		}
		for _, role := range []string{"admin", "operator", "viewer"} {
			response := request(http.MethodGet, path, tokens[role], "")
			if response.Code != http.StatusGone {
				t.Fatalf("%s %s status=%d body=%s", role, path, response.Code, response.Body.String())
			}
		}
	}
	restorePath := fmt.Sprintf("/api/v1/tasks/%d/snapshots/%s/restore", taskEntity.ID, sid)
	body := `{"includes":["/file"],"targetPath":"/tmp/recovery"}`
	for _, role := range []string{"operator", "viewer"} {
		response := request(http.MethodPost, restorePath, tokens[role], body)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s restore status=%d body=%s", role, response.Code, response.Body.String())
		}
	}
	adminRestore := request(http.MethodPost, restorePath, tokens["admin"], body)
	if adminRestore.Code != http.StatusForbidden {
		t.Fatalf("admin restore without FeatureLive status=%d body=%s", adminRestore.Code, adminRestore.Body.String())
	}
}

func TestNewAssetSearchStaysAvailableWhenLegacySnapshotSearchIsGone(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	response := performBackupAssetRBACRequest(t, fixture, http.MethodPost, "/api/v1/asset-search", `{"query":{"type":"match_all"}}`, fixture.tokens["admin"])
	if response.Code == http.StatusGone {
		t.Fatalf("catalog search must not inherit leftover 410: %s", response.Body.String())
	}
	if response.Code != http.StatusServiceUnavailable && response.Code != http.StatusOK && response.Code != http.StatusBadRequest {
		t.Fatalf("asset-search status=%d body=%s", response.Code, response.Body.String())
	}
}
