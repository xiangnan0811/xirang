package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/snapshot"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
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

	handler := NewSnapshotSearchHandler(db, nil, nil)

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

	if w.Code != http.StatusGone {
		t.Fatalf("期望 410，实际: %d", w.Code)
	}
}

func TestSearchSnapshotFiles_MissingQuery(t *testing.T) {
	router := setupSearchTestRouter(t)

	req := httptest.NewRequest("GET", "/tasks/1/snapshots/search", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("期望 410，实际: %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestSearchSnapshotFiles_NonExistentTask(t *testing.T) {
	router := setupSearchTestRouter(t)

	req := httptest.NewRequest("GET", "/tasks/99999/snapshots/search?q=test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("期望 410，实际: %d", w.Code)
	}
}

func TestSearchSnapshotFiles_InvalidTaskID(t *testing.T) {
	router := setupSearchTestRouter(t)

	req := httptest.NewRequest("GET", "/tasks/abc/snapshots/search?q=test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("期望 410，实际: %d", w.Code)
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
	handler := NewSnapshotSearchHandler(db, nil, nil)
	router.GET("/tasks/:id/snapshots/search", func(c *gin.Context) {
		handler.Search(c)
	})

	req := httptest.NewRequest("GET", fmt.Sprintf("/tasks/%d/snapshots/search?q=test", task.ID), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("期望 410 (遗留搜索已退役)，实际: %d (body: %s)", w.Code, w.Body.String())
	}
}

type searchLineageSession struct {
	publication.LineageSession
	mode   publication.LineageMode
	points []publication.CommittedPoint
	closed int32
}

func (session *searchLineageSession) Mode() publication.LineageMode { return session.mode }
func (session *searchLineageSession) CommittedPoints() []publication.CommittedPoint {
	return append([]publication.CommittedPoint(nil), session.points...)
}
func (session *searchLineageSession) Close() error {
	atomic.AddInt32(&session.closed, 1)
	return nil
}

type searchLineageGuard struct {
	session   publication.LineageSession
	operation publication.ResticOperation
	calls     int
}

func (guard *searchLineageGuard) Begin(_ context.Context, _ uint, operation publication.ResticOperation) (publication.LineageSession, error) {
	guard.calls++
	guard.operation = operation
	return guard.session, nil
}

var _ publication.LineageGuard = (*searchLineageGuard)(nil)

func TestSnapshotSearchFiltersHistoricalContaminationAndHoldsSearchAdmission(t *testing.T) {
	db := openSearchTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.SnapshotFileIndex{}); err != nil {
		t.Fatalf("initialize tables: %v", err)
	}
	taskEntity := model.Task{Name: "exact-search", ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	allowed := strings.Repeat("a", 64)
	foreign := strings.Repeat("b", 64)
	partial := strings.Repeat("c", 64)
	marker := func(snapshotID string, count int64) model.SnapshotFileIndex {
		return model.SnapshotFileIndex{TaskID: taskEntity.ID, SnapshotID: snapshotID, Path: "", Size: count, Mtime: "xirang-index-complete-v1"}
	}
	rows := []model.SnapshotFileIndex{
		marker(allowed, 1),
		{TaskID: taskEntity.ID, SnapshotID: allowed, Path: "/allowed-secret.txt", Size: 1, Mtime: "2026-07-14T12:00:00Z"},
		marker(foreign, 1),
		{TaskID: taskEntity.ID, SnapshotID: foreign, Path: "/foreign-secret.txt", Size: 1, Mtime: "2026-07-14T12:00:00Z"},
		{TaskID: taskEntity.ID, SnapshotID: partial, Path: "/partial-secret.txt", Size: 1, Mtime: "2026-07-14T12:00:00Z"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	session := &searchLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{{
		RecoveryPointID: strings.Repeat("1", 32), FullNativeID: allowed, CapturedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	}}}
	guard := &searchLineageGuard{session: session}
	indexer := snapshot.NewIndexer(db, nil, nil)
	handler := NewSnapshotSearchHandler(db, guard, indexer)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/tasks/:id/snapshots/search", handler.Search)
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/tasks/%d/snapshots/search?q=secret", taskEntity.ID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusGone {
		t.Fatalf("retired search status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "/allowed-secret.txt") || strings.Contains(response.Body.String(), "/foreign-secret.txt") {
		t.Fatalf("retired search returned paths: %s", response.Body.String())
	}
	if guard.calls != 0 {
		t.Fatalf("retired search opened lineage admission calls=%d", guard.calls)
	}
}
