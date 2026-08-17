package api

import (
	"encoding/json"
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

// Post-GREEN review selector closure for the immutable Task 11 route ledger.
func TestRecoveryReviewF1AuthorityRoutes(t *testing.T) {
	TestRecoveryRBACRouterRegistersTaskNineRouteMatrix(t)
	TestRecoveryRouteMatrixEnforcesAuthenticatedAdminRecoverRBACWithClosedEnvelope(t)
}

func TestRecoveryReviewF7MixedVersionRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	jwtManager := auth.NewJWTManager("FAKE_RECOVERY_MIXED_VERSION_SIGNING_KEY_FOR_TEST_ONLY", time.Hour)
	user := model.User{
		Username: "recovery-mixed-version-admin", PasswordHash: "FAKE_HASH_FOR_TEST_ONLY",
		Role: "admin", TokenVersion: 1,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token, err := jwtManager.GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}

	// A newer frontend talking to an older/unconfigured Recovery graph sees
	// registered routes with a closed 503 envelope, never a legacy fallback.
	router := NewRouter(Dependencies{DB: db, JWTManager: jwtManager})
	id := strings.Repeat("a", 32)
	requests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/recovery-plans"},
		{http.MethodGet, "/api/v1/recovery-plans/" + id},
		{http.MethodPost, "/api/v1/recovery-jobs/" + id + "/results/cleanup"},
		{http.MethodGet, "/api/v1/settings/backup-assets/recovery/target-roots?node_id=1"},
		{http.MethodPost, "/api/v1/settings/backup-assets/recovery/downgrade-readiness"},
	}
	for _, requestCase := range requests {
		request := httptest.NewRequest(requestCase.method, requestCase.path, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("mixed-version route %s %s status=%d body=%s", requestCase.method, requestCase.path,
				response.Code, response.Body.String())
		}
		var envelope map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || len(envelope) != 3 ||
			envelope["code"] != float64(http.StatusServiceUnavailable) {
			t.Fatalf("mixed-version route %s %s envelope=%v body=%s err=%v",
				requestCase.method, requestCase.path, envelope, response.Body.String(), err)
		}
		for _, forbidden := range []string{token, id, "proof", "secret", "ticket"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("mixed-version route response leaked %q: %s", forbidden, response.Body.String())
			}
		}
	}

	for _, legacy := range []string{
		"/api/v1/recovery-plans/:id/authorizations",
		"/api/v1/recovery-jobs/:id/authorizations",
		"/api/v1/recovery-authorizations",
	} {
		if hasRoute(router.Routes(), http.MethodPost, legacy) {
			t.Fatalf("mixed-version fallback exposed legacy route POST %s", legacy)
		}
	}
}
