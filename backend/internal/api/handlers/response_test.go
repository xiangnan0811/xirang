package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"

	"github.com/gin-gonic/gin"
)

func TestRespondBackupCapabilityErrorAcceptsOnlyValidated501And503(t *testing.T) {
	for _, status := range []int{http.StatusNotImplemented, http.StatusServiceUnavailable} {
		router := setupTestRouter(func(c *gin.Context) {
			respondBackupCapabilityError(c, status, backupasset.CapabilityReason{Code: backupasset.CapabilityProviderProtocolIncompatible}, "corr-response")
		})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
		if response.Code != status || !strings.Contains(response.Body.String(), "corr-response") || !strings.Contains(response.Body.String(), string(backupasset.CapabilityProviderProtocolIncompatible)) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	for _, test := range []struct {
		status int
		reason backupasset.CapabilityReason
	}{{http.StatusBadRequest, backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}}, {http.StatusServiceUnavailable, backupasset.CapabilityReason{Code: "future_raw_code"}}} {
		router := setupTestRouter(func(c *gin.Context) { respondBackupCapabilityError(c, test.status, test.reason, "corr-response") })
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "future_raw_code") {
			t.Fatalf("invalid helper input status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func setupTestRouter(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/test", handler)
	return r
}

func TestRespondOK(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondOK(c, gin.H{"id": 1, "name": "test"})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际 %d", w.Code)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 code=%d，实际 %d", http.StatusOK, resp.Code)
	}
	if resp.Message != "ok" {
		t.Fatalf("期望 message=ok，实际 %s", resp.Message)
	}
}

func TestRespondMessage(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondMessage(c, "删除成功")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	var resp Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp) //nolint:errcheck
	if resp.Code != http.StatusOK || resp.Message != "删除成功" || resp.Data != nil {
		t.Fatalf("响应不符合预期: %+v", resp)
	}
}

func TestRespondBadRequest(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondBadRequest(c, "参数不合法")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际 %d", w.Code)
	}
	var resp Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp) //nolint:errcheck
	if resp.Code != 400 || resp.Message != "参数不合法" {
		t.Fatalf("响应不符合预期: %+v", resp)
	}
}

func TestRespondInternalError(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondInternalError(c, fmt.Errorf("db connection failed"))
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("期望状态码 500，实际 %d", w.Code)
	}
	var resp Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp) //nolint:errcheck
	if resp.Code != 500 {
		t.Fatalf("期望 code=500，实际 %d", resp.Code)
	}
	if resp.Message != "服务器内部错误" {
		t.Fatalf("不应暴露内部错误: %s", resp.Message)
	}
}

func TestRespondPaginated(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondPaginated(c, []string{"a", "b"}, 10, 1, 20)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	var resp PaginatedResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp) //nolint:errcheck
	if resp.Code != http.StatusOK || resp.Total != 10 || resp.Page != 1 || resp.PageSize != 20 {
		t.Fatalf("分页响应不符合预期: %+v", resp)
	}
}

func TestRespondGone(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondGone(c, "遗留快照浏览接口已退役，请使用备份资产目录与搜索")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusGone {
		t.Fatalf("期望状态码 410，实际 %d", w.Code)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if resp.Code != http.StatusGone || !strings.Contains(resp.Message, "备份资产") || resp.Data != nil {
		t.Fatalf("410 响应不符合预期: %+v", resp)
	}
}

func TestRespondCreated(t *testing.T) {
	r := setupTestRouter(func(c *gin.Context) {
		respondCreated(c, gin.H{"id": 42})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("期望状态码 201，实际 %d", w.Code)
	}
}
