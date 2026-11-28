package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// TestSettingsCommittedCacheVisibilityPostgres proves that the shared settings
// service observes every committed HTTP settings mutation on its next read.
func TestSettingsCommittedCacheVisibilityPostgres(t *testing.T) {
	t.Setenv("LOGIN_CAPTCHA_ENABLED", "")
	t.Setenv("LOGIN_SECOND_CAPTCHA_ENABLED", "")
	t.Setenv("LOGIN_RATE_LIMIT", "")
	t.Setenv("BACKUP_ASSETS_EXPORT_WORKER_CONCURRENCY", "")

	gin.SetMode(gin.TestMode)
	db := openMigratedRetryTransactionDB(t, "postgres")

	var fillArmed atomic.Bool
	var fillBlocked, fillRelease chan struct{}
	if err := db.Callback().Query().After("gorm:query").Register("test:settings_cache_delayed_fill", func(tx *gorm.DB) {
		if tx.Statement.Table != "system_settings" || !fillArmed.CompareAndSwap(true, false) {
			return
		}
		close(fillBlocked)
		<-fillRelease
	}); err != nil {
		t.Fatalf("register delayed fill callback: %v", err)
	}

	svc := settings.NewService(db)
	handler := NewSettingsHandler(db, svc).WithBackupAssetTransitioner(&settingsCanceledPersistenceRuntime{})
	router := gin.New()
	router.PUT("/settings", handler.BatchUpdate)
	router.DELETE("/settings/:key", handler.Delete)

	send := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	serve := func(t *testing.T, method, path, body string) {
		t.Helper()
		if response := send(method, path, body); response.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", method, path, response.Code, response.Body.String())
		}
	}
	seedAndPrime := func(t *testing.T, values map[string]string) {
		t.Helper()
		for key, value := range values {
			if err := svc.UpdateContext(t.Context(), key, value); err != nil {
				t.Fatalf("seed %s: %v", key, err)
			}
			if got := svc.GetEffective(key); got != value {
				t.Fatalf("prime %s=%q, want %q", key, got, value)
			}
		}
	}
	expect := func(t *testing.T, want map[string]string) {
		t.Helper()
		for key, value := range want {
			if got := svc.GetEffective(key); got != value {
				t.Fatalf("GetEffective(%s)=%q, want committed %q", key, got, value)
			}
		}
	}

	t.Run("ordinary_put", func(t *testing.T) {
		seedAndPrime(t, map[string]string{"login.captcha_enabled": "false", "login.second_captcha_enabled": "false"})
		serve(t, http.MethodPut, "/settings", `{"login.captcha_enabled":"true","login.second_captcha_enabled":"true"}`)
		expect(t, map[string]string{"login.captcha_enabled": "true", "login.second_captcha_enabled": "true"})
	})

	t.Run("mixed_put", func(t *testing.T) {
		seedAndPrime(t, map[string]string{"backup_assets.export.worker_concurrency": "2", "login.captcha_enabled": "false"})
		serve(t, http.MethodPut, "/settings", `{"backup_assets.export.worker_concurrency":"3","login.captcha_enabled":"true"}`)
		expect(t, map[string]string{"backup_assets.export.worker_concurrency": "3", "login.captcha_enabled": "true"})
	})

	t.Run("ordinary_delete", func(t *testing.T) {
		seedAndPrime(t, map[string]string{"login.captcha_enabled": "true"})
		serve(t, http.MethodDelete, "/settings/login.captcha_enabled", "")
		expect(t, map[string]string{"login.captcha_enabled": "false"})
	})

	t.Run("foundation_delete", func(t *testing.T) {
		seedAndPrime(t, map[string]string{"backup_assets.export.worker_concurrency": "3"})
		serve(t, http.MethodDelete, "/settings/backup_assets.export.worker_concurrency", "")
		expect(t, map[string]string{"backup_assets.export.worker_concurrency": "2"})
	})

	t.Run("overlapping_read", func(t *testing.T) {
		const key = "login.rate_limit"
		if err := svc.DeleteContext(t.Context(), key); err != nil {
			t.Fatalf("reset %s: %v", key, err)
		}
		fillBlocked, fillRelease = make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(fillRelease) }) }
		var readerDone chan string
		var putDone chan *httptest.ResponseRecorder
		var readerJoined, putJoined bool
		t.Cleanup(func() {
			fillArmed.Store(false)
			release()
			if readerDone != nil && !readerJoined {
				select {
				case <-readerDone:
				case <-time.After(5 * time.Second):
					t.Error("reader did not finish during cleanup")
				}
			}
			if putDone != nil && !putJoined {
				select {
				case <-putDone:
				case <-time.After(5 * time.Second):
					t.Error("PUT did not finish during cleanup")
				}
			}
		})

		// The reader loads the pre-commit row, then stalls before filling the cache.
		fillArmed.Store(true)
		readerDone = make(chan string, 1)
		go func() { readerDone <- svc.GetEffective(key) }()
		select {
		case <-fillBlocked:
		case <-time.After(5 * time.Second):
			t.Fatal("reader did not reach delayed cache fill")
		}

		putDone = make(chan *httptest.ResponseRecorder, 1)
		go func() { putDone <- send(http.MethodPut, "/settings", `{"login.rate_limit":"20"}`) }()
		var put *httptest.ResponseRecorder
		select {
		case put = <-putDone:
			putJoined = true
		case <-time.After(5 * time.Second):
			t.Fatal("PUT did not finish while the reader was paused")
		}
		if put.Code != http.StatusOK {
			t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
		}
		release()
		select {
		case stale := <-readerDone:
			readerJoined = true
			if stale != "10" {
				t.Fatalf("delayed reader loaded %q, want pre-commit %q", stale, "10")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reader did not finish after release")
		}
		expect(t, map[string]string{key: "20"})
	})
}
