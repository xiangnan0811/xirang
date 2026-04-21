package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openLogCfgTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newLogCfgRouter(t *testing.T, db *gorm.DB, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewNodeLogConfigHandler(db)
	inject := func(c *gin.Context) {
		c.Set("userID", uint(1))
		c.Set("role", role)
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.GET("/nodes/:id/log-config", middleware.RBAC("logs:read"), h.Get)
	g.PATCH("/nodes/:id/log-config", middleware.RBAC("logs:write"), h.Patch)
	return r
}

func doLogCfg(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var rb *bytes.Buffer
	if body != "" {
		rb = bytes.NewBufferString(body)
	} else {
		rb = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, rb)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestNodeLogConfig_GetReturnsDefaults(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u", LogJournalctlEnabled: true})
	r := newLogCfgRouter(t, db, "operator")
	w := doLogCfg(r, "GET", "/api/v1/nodes/1/log-config", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			LogPaths             []string `json:"log_paths"`
			LogJournalctlEnabled bool     `json:"log_journalctl_enabled"`
			LogRetentionDays     int      `json:"log_retention_days"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Data.LogJournalctlEnabled {
		t.Fatal("expected default true")
	}
}

func TestNodeLogConfig_PatchAppliesWhitelist(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "operator")
	body := `{"log_paths":["/var/log/nginx/access.log"],"log_journalctl_enabled":false,"log_retention_days":14}`
	w := doLogCfg(r, "PATCH", "/api/v1/nodes/1/log-config", body)
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	var n model.Node
	db.First(&n, 1)
	if n.LogPaths == "" || !strings.Contains(n.LogPaths, "/var/log/nginx/access.log") {
		t.Fatalf("log_paths not stored: %q", n.LogPaths)
	}
	if n.LogJournalctlEnabled {
		t.Fatal("expected false")
	}
	if n.LogRetentionDays != 14 {
		t.Fatalf("days=%d", n.LogRetentionDays)
	}
}

func TestNodeLogConfig_RejectsNonAbsolutePath(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "operator")
	body := `{"log_paths":["var/log/relative"],"log_journalctl_enabled":true,"log_retention_days":0}`
	w := doLogCfg(r, "PATCH", "/api/v1/nodes/1/log-config", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
}

func TestNodeLogConfig_RejectsDenyListPath(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "operator")
	for _, p := range []string{"/etc/passwd", "/proc/cpuinfo", "/sys/class", "/dev/null", "/boot/grub", "/root/.ssh/id_rsa"} {
		body := fmt.Sprintf(`{"log_paths":[%q],"log_journalctl_enabled":true,"log_retention_days":0}`, p)
		w := doLogCfg(r, "PATCH", "/api/v1/nodes/1/log-config", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("path %q: %d", p, w.Code)
		}
	}
}

func TestNodeLogConfig_RejectsShellMetaChars(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "operator")
	for _, p := range []string{
		"/var/log/$(id).log",
		"/var/log/`whoami`.log",
		"/var/log/foo\"bar",
		"/var/log/foo\\bar",
		"/var/log/foo'bar",
	} {
		body := fmt.Sprintf(`{"log_paths":[%q],"log_journalctl_enabled":true,"log_retention_days":0}`, p)
		w := doLogCfg(r, "PATCH", "/api/v1/nodes/1/log-config", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("path %q: %d", p, w.Code)
		}
	}
}

func TestNodeLogConfig_RejectsWildcards(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "operator")
	body := `{"log_paths":["/var/log/*.log"],"log_journalctl_enabled":true,"log_retention_days":0}`
	w := doLogCfg(r, "PATCH", "/api/v1/nodes/1/log-config", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
}

func TestNodeLogConfig_RejectsTooMany(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "operator")
	var paths []string
	for i := 0; i < 21; i++ {
		paths = append(paths, fmt.Sprintf("/var/log/f%d", i))
	}
	b, _ := json.Marshal(paths)
	body := fmt.Sprintf(`{"log_paths":%s,"log_journalctl_enabled":true,"log_retention_days":0}`, b)
	w := doLogCfg(r, "PATCH", "/api/v1/nodes/1/log-config", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
}

func TestNodeLogConfig_RejectsDaysOutOfRange(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "operator")
	body := `{"log_paths":[],"log_journalctl_enabled":true,"log_retention_days":500}`
	w := doLogCfg(r, "PATCH", "/api/v1/nodes/1/log-config", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
}

func TestNodeLogConfig_RequiresLogsRead(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "guest")
	w := doLogCfg(r, "GET", "/api/v1/nodes/1/log-config", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("%d", w.Code)
	}
}

func TestNodeLogConfig_PatchRequiresLogsWrite(t *testing.T) {
	db := openLogCfgTestDB(t)
	db.Create(&model.Node{Name: "n", Host: "h", Username: "u"})
	r := newLogCfgRouter(t, db, "viewer")
	body := `{"log_paths":[],"log_journalctl_enabled":true,"log_retention_days":0}`
	w := doLogCfg(r, "PATCH", "/api/v1/nodes/1/log-config", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("%d", w.Code)
	}
}
