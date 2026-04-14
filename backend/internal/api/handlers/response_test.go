package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

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
	if resp.Code != 0 {
		t.Fatalf("期望 code=0，实际 %d", resp.Code)
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
	if resp.Code != 0 || resp.Message != "删除成功" || resp.Data != nil {
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
	if resp.Code != 0 || resp.Total != 10 || resp.Page != 1 || resp.PageSize != 20 {
		t.Fatalf("分页响应不符合预期: %+v", resp)
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
