package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/auth"

	"github.com/gin-gonic/gin"
)

func TestTOTPFailureResponseRetryClassification(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid code", auth.ErrTOTPCodeInvalid, http.StatusBadRequest, "TOTP_CODE_INVALID"},
		{"missing enrollment", auth.ErrTOTPEnrollmentRequired, http.StatusBadRequest, "TOTP_ENROLLMENT_REQUIRED"},
		{"expired enrollment", auth.ErrTOTPEnrollmentExpired, http.StatusBadRequest, "TOTP_ENROLLMENT_EXPIRED"},
		{"replaced enrollment", auth.ErrTOTPEnrollmentConflict, http.StatusBadRequest, "TOTP_ENROLLMENT_CONFLICT"},
		{"already enabled", auth.ErrTOTPAlreadyEnabled, http.StatusBadRequest, ""},
		{"security conflict", auth.ErrSecurityConflict, http.StatusBadRequest, ""},
		{"unknown commit", fmt.Errorf("private database failure"), http.StatusInternalServerError, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := setupTestRouter(func(c *gin.Context) {
				respondTOTPVerifyFailure(c, fmt.Errorf("%w", test.err))
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
			var envelope struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Data    *struct {
					ErrorCode string `json:"error_code"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || envelope.Code != test.status {
				t.Fatalf("HTTP/envelope status = %d/%d, want %d", response.Code, envelope.Code, test.status)
			}
			if test.code == "" {
				if envelope.Data != nil {
					t.Fatal("uncertain activation must not advertise a retryable error code")
				}
			} else if envelope.Data == nil || envelope.Data.ErrorCode != test.code {
				t.Fatalf("missing or incorrect deterministic error code: want %s", test.code)
			}
			if test.status == http.StatusInternalServerError && envelope.Message != "服务器内部错误" {
				t.Fatal("unknown commit response leaked internal failure details")
			}
		})
	}
}
