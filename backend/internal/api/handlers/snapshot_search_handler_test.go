package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/snapshot"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func setupSearchTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()

	db := openSearchTestDB(t)

	// 确保表存在
	if err := db.AutoMigrate(&model.Task{}, &model.SnapshotFileIndex{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	handler := NewSnapshotSearchHandler(db)

	router.GET("/tasks/:id/snapshots/search", func(c *gin.Context) {
		handler.Search(c)
	})

	return router
}

func TestSearchSnapshotFiles_EmptyQuery(t *testing.T) {
	router := setupSearchTestRouter(t)

	req := httptest.NewRequest("GET", "/tasks/1/snapshots/search?q=", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际: %d", w.Code)
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望 code=400，实际: %d", resp.Code)
	}
}

func TestSearchSnapshotFiles_MissingQuery(t *testing.T) {
	router := setupSearchTestRouter(t)

	req := httptest.NewRequest("GET", "/tasks/1/snapshots/search", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际: %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestSearchSnapshotFiles_NonExistentTask(t *testing.T) {
	router := setupSearchTestRouter(t)

	req := httptest.NewRequest("GET", "/tasks/99999/snapshots/search?q=test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际: %d", w.Code)
	}
}

func TestSearchSnapshotFiles_InvalidTaskID(t *testing.T) {
	router := setupSearchTestRouter(t)

	req := httptest.NewRequest("GET", "/tasks/abc/snapshots/search?q=test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际: %d", w.Code)
	}
}

// TestEscapeLikePatternInHandler 验证 LIKE 转义函数可正确防御通配符注入。
func TestEscapeLikePatternHandler(t *testing.T) {
	// 测试转义函数本身
	testCases := []struct {
		input    string
		expected string
	}{
		{"normal", "normal"},
		{"100%", `100\%`},
		{"file_name", `file\_name`},
		{`path\to`, `path\\to`},
		{"100%_test", `100\%\_test`},
		{"", ""},
	}

	for _, tc := range testCases {
		result := snapshot.EscapeLikePattern(tc.input)
		if result != tc.expected {
			t.Errorf("EscapeLikePattern(%q) = %q, 期望 %q", tc.input, result, tc.expected)
		}
	}
}

// TestSearchSnapshotFiles_NonResticTask 验证非 restic 类型任务返回 400。
func TestSearchSnapshotFiles_NonResticTask(t *testing.T) {
	db := openSearchTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.SnapshotFileIndex{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	// 创建一个 rsync 类型任务
	task := model.Task{
		Name:         "rsync-task",
		NodeID:       1,
		ExecutorType: "rsync",
		RsyncSource:  "/data",
		RsyncTarget:  "/backup",
		Status:       "idle",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewSnapshotSearchHandler(db)
	router.GET("/tasks/:id/snapshots/search", func(c *gin.Context) {
		handler.Search(c)
	})

	req := httptest.NewRequest("GET", fmt.Sprintf("/tasks/%d/snapshots/search?q=test", task.ID), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400 (非 restic 任务)，实际: %d (body: %s)", w.Code, w.Body.String())
	}
}
