package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAccountRecoveryPrimaryAdmissionRequiresCurrentUnrestrictedIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admission.sqlite")+"?_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatal("open admission database")
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal("get admission database connection")
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}); err != nil {
		t.Fatal("migrate admission database")
	}
	user := model.User{Username: "recovery-admission", Role: "admin", TokenVersion: 9}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal("seed admission identity")
	}
	const secret = "account-recovery-admission-test-signing-key"
	manager := auth.NewJWTManager(secret, time.Hour)
	manager.SetDB(db)

	for _, tc := range []struct {
		name     string
		required bool
		enabled  bool
		purpose  string
		version  uint
		want     int
	}{
		{name: "current nonzero version", version: 9, want: http.StatusNoContent},
		{name: "required current primary", required: true, version: 9, want: http.StatusUnauthorized},
		{name: "contradictory enabled required", required: true, enabled: true, version: 9, want: http.StatusUnauthorized},
		{name: "stale primary", version: 8, want: http.StatusUnauthorized},
		{name: "recovery purpose", purpose: auth.PurposeRecoveryEnrollment, version: 9, want: http.StatusUnauthorized},
		{name: "pending purpose", purpose: auth.Purpose2FAPending, version: 9, want: http.StatusUnauthorized},
		{name: "step-up purpose", purpose: auth.PurposeStepUp, version: 9, want: http.StatusUnauthorized},
		{name: "whitespace is not empty purpose", purpose: " ", version: 9, want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
				"totp_reenrollment_required": tc.required, "totp_enabled": tc.enabled,
			}).Error; err != nil {
				t.Fatal("update admission identity")
			}
			now := time.Now().UTC()
			claims := auth.Claims{
				UserID: user.ID, Username: user.Username, Role: user.Role,
				TokenVersion: tc.version, Purpose: tc.purpose,
				RegisteredClaims: jwt.RegisteredClaims{
					ID: strings.Repeat("a", 32), Subject: "1",
					IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
				},
			}
			// Sign directly to exercise admission even if an issuer is bypassed.
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
			if err != nil {
				t.Fatal("sign admission fixture")
			}
			called := false
			router := gin.New()
			router.Use(AuthMiddleware(manager, db))
			router.GET("/protected", func(c *gin.Context) {
				binding, ok := CurrentSessionBinding(c)
				if !ok || binding.UserID != user.ID || binding.Role != user.Role || binding.TokenVersion != tc.version {
					t.Error("primary admission lost authoritative identity binding")
				}
				called = true
				c.Status(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.want || called != (tc.want == http.StatusNoContent) {
				t.Fatalf("admission status=%d called=%v, want status=%d", response.Code, called, tc.want)
			}
		})
	}
}
