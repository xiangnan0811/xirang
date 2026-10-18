package middleware

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"
)

const budgetTestRoute = "/api/v1/system/backups/web/xirang-20261017-000000.db/download"

func budgetTestServer(t *testing.T, h http.Handler, h2 bool) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.EnableHTTP2 = h2
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	if h2 {
		s.StartTLS()
	} else {
		s.Start()
	}
	t.Cleanup(s.Close)
	return s
}

func TestSystemBackupDownloadBudgetRealTransport(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		name := "http1"
		if h2 {
			name = "http2"
		}
		t.Run(name, func(t *testing.T) {
			for _, stage := range []string{"prepare", "absolute", "committed_prepare"} {
				t.Run(stage, func(t *testing.T) {
					r := setupTestRouter()
					total, prepare := 2*time.Second, 40*time.Millisecond
					if stage == "absolute" {
						total, prepare = 40*time.Millisecond, time.Second
					}
					r.Use(systemBackupDownloadBudget(total, prepare))
					r.GET(budgetTestRoute, func(c *gin.Context) {
						if stage == "committed_prepare" {
							c.Header("Content-Length", "10000")
							_, _ = c.Writer.Write([]byte("partial-tar"))
							if err := systemBackupDownloadResponseController(c).Flush(); err != nil {
								panic(err)
							}
						}
						<-c.Request.Context().Done()
						if !HandleSystemBackupDownloadBudgetFailure(c) {
							t.Error("expired budget not handled")
						}
					})
					s := budgetTestServer(t, r, h2)
					resp, err := s.Client().Get(s.URL + budgetTestRoute)
					if stage == "absolute" {
						if err == nil {
							if err := resp.Body.Close(); err != nil {
								t.Error(err)
							}
							t.Fatal("absolute expiry produced a response")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := resp.Body.Close(); err != nil {
							t.Error(err)
						}
					}()
					if h2 && resp.ProtoMajor != 2 {
						t.Fatalf("not HTTP/2: %s", resp.Proto)
					}
					body, readErr := io.ReadAll(resp.Body)
					if stage == "committed_prepare" {
						if readErr == nil || strings.Contains(string(body), "BACKUP_PREPARE_TIMEOUT") {
							t.Fatalf("stream not aborted cleanly: %q %v", body, readErr)
						}
					} else if readErr != nil || resp.StatusCode != 504 || !strings.Contains(string(body), "BACKUP_PREPARE_TIMEOUT") {
						t.Fatalf("prepare status=%d body=%q err=%v", resp.StatusCode, body, readErr)
					}
				})
			}
		})
	}
}

func TestSystemBackupDownloadBudgetMatchingAndUnsupportedTransport(t *testing.T) {
	for _, path := range []string{"/api/v1/system/backups", budgetTestRoute + "/extra", "/other"} {
		r := setupTestRouter()
		r.Use(SystemBackupDownloadBudget())
		r.GET(path, func(c *gin.Context) {
			if _, ok := SystemBackupDownloadBudgetFromContext(c); ok {
				t.Error("budget on unrelated route")
			}
			c.Status(204)
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 204 {
			t.Fatalf("unrelated route status=%d", w.Code)
		}
	}
	r := setupTestRouter()
	r.Use(SystemBackupDownloadBudget())
	r.GET(budgetTestRoute, func(c *gin.Context) { t.Error("entered handler without transport deadlines") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", budgetTestRoute, nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "BACKUP_TRANSPORT_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestSystemBackupDownloadBudgetTightensAndRetainsContexts(t *testing.T) {
	r := setupTestRouter()
	r.Use(systemBackupDownloadBudget(2*time.Second, time.Second))
	retainedContexts := make(chan context.Context, 1)
	r.GET(budgetTestRoute, func(c *gin.Context) {
		s, ok := SystemBackupDownloadBudgetFromContext(c)
		if !ok {
			t.Error("missing budget")
			return
		}
		initial := s.AbsoluteDeadline()
		originalPrepare := c.Request.Context()
		if err := s.LimitDeadline(c, initial.Add(time.Hour)); err != nil {
			t.Error(err)
		}
		if !s.AbsoluteDeadline().Equal(initial) {
			t.Error("deadline extended")
		}
		tightened := time.Now().Add(500 * time.Millisecond)
		if err := s.LimitDeadline(c, tightened); err != nil {
			t.Error(err)
		}
		if !s.AbsoluteDeadline().Equal(tightened) {
			t.Error("deadline not tightened")
		}
		if originalPrepare.Err() != nil {
			t.Error("old context canceled mid-chain")
		}
		if deadline, _ := c.Request.Context().Deadline(); !deadline.Equal(tightened) {
			t.Error("prepare context not rebound")
		}
		if !s.BeginTransfer(c) {
			t.Error("transfer rejected")
		}
		retained := c.Request.Context()
		if deadline, _ := retained.Deadline(); !deadline.Equal(tightened) {
			t.Error("transfer lease incorrect")
		}
		if retained.Err() != nil {
			t.Error("transfer context canceled before return")
		}
		retainedContexts <- retained
		c.Status(204)
	})
	s := budgetTestServer(t, r, false)
	resp, err := s.Client().Get(s.URL + budgetTestRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Error(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	retained := <-retainedContexts
	select {
	case <-retained.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("chain did not release contexts")
	}
}

func TestSystemBackupDownloadBudgetBeginTransferLeavesPrepareDeadline(t *testing.T) {
	r := setupTestRouter()
	r.Use(systemBackupDownloadBudget(2*time.Second, 50*time.Millisecond))
	r.GET(budgetTestRoute, func(c *gin.Context) {
		s, _ := SystemBackupDownloadBudgetFromContext(c)
		prepare := c.Request.Context()
		if !s.BeginTransfer(c) {
			t.Error("transfer rejected")
			return
		}
		deadline, _ := c.Request.Context().Deadline()
		if !deadline.Equal(s.AbsoluteDeadline()) {
			t.Error("transfer still bound to prepare")
		}
		<-prepare.Done()
		if HandleSystemBackupDownloadBudgetFailure(c) {
			t.Error("prepare timeout affected active transfer")
			return
		}
		c.Status(204)
	})
	s := budgetTestServer(t, r, false)
	resp, err := s.Client().Get(s.URL + budgetTestRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Error(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestSystemBackupDownloadBudgetClientCancellationWritesNothing(t *testing.T) {
	r := setupTestRouter()
	r.Use(systemBackupDownloadBudget(time.Second, time.Second))
	entered, finished := make(chan struct{}), make(chan struct{})
	var written atomic.Bool
	r.GET(budgetTestRoute, func(c *gin.Context) {
		close(entered)
		<-c.Request.Context().Done()
		if !HandleSystemBackupDownloadBudgetFailure(c) {
			t.Error("cancel not handled")
		}
		written.Store(c.Writer.Written())
		close(finished)
	})
	s := budgetTestServer(t, r, false)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+budgetTestRoute, nil)
	done := make(chan error, 1)
	go func() {
		resp, err := s.Client().Do(req)
		if resp != nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("client cancellation: %v", err)
	}
	<-finished
	if written.Load() {
		t.Fatal("canceled request wrote response")
	}
}

func TestSystemBackupDownloadBudgetBlockingAuthenticationQueries(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, stage := range []string{"revocation", "user"} {
			name := stage + "_http1"
			if h2 {
				name = stage + "_http2"
			}
			t.Run(name, func(t *testing.T) {
				db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				if err != nil {
					t.Fatal(err)
				}
				sqlDB, _ := db.DB()
				t.Cleanup(func() {
					if err := sqlDB.Close(); err != nil {
						t.Error(err)
					}
				})
				if err := db.AutoMigrate(&model.User{}, &model.TokenRevocation{}); err != nil {
					t.Fatal(err)
				}
				user := model.User{Username: "budget-admin", Role: "admin"}
				if err := db.Create(&user).Error; err != nil {
					t.Fatal(err)
				}
				manager := auth.NewJWTManager("test-secret-at-least-16-chars", time.Hour)
				manager.SetDB(db)
				token := generateTestToken(manager, user)
				var blocked atomic.Bool
				if err := db.Callback().Query().Before("gorm:query").Register("budget:block", func(tx *gorm.DB) {
					matches := (stage == "user" && tx.Statement.Table == "users") || (stage == "revocation" && tx.Statement.Table == "token_revocations")
					if matches {
						blocked.Store(true)
						<-tx.Statement.Context.Done()
						_ = tx.AddError(tx.Statement.Context.Err())
					}
				}); err != nil {
					t.Fatal(err)
				}
				r := setupTestRouter()
				r.Use(systemBackupDownloadBudget(2*time.Second, 40*time.Millisecond), AuthMiddleware(manager, db))
				r.GET(budgetTestRoute, func(c *gin.Context) { t.Error("authentication outlived prepare") })
				s := budgetTestServer(t, r, h2)
				req, _ := http.NewRequest("GET", s.URL+budgetTestRoute, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := s.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := resp.Body.Close(); err != nil {
						t.Error(err)
					}
				}()
				body, err := io.ReadAll(resp.Body)
				if err != nil || !blocked.Load() || resp.StatusCode != 504 || !strings.Contains(string(body), "BACKUP_PREPARE_TIMEOUT") {
					t.Fatalf("blocked=%v status=%d body=%q err=%v", blocked.Load(), resp.StatusCode, body, err)
				}
			})
		}
	}
}

type budgetDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	err       error
}

func (w *budgetDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return w.err
}

type budgetUnwrapWriter struct{ http.ResponseWriter }

func (w budgetUnwrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestSystemBackupDownloadBudgetEarlyDeadlineAndBoundedErrorWindow(t *testing.T) {
	for _, total := range []time.Duration{2 * time.Second, 10 * time.Second} {
		t.Run(total.String(), func(t *testing.T) {
			writer := &budgetDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			router := setupTestRouter()
			router.Use(systemBackupDownloadBudget(total, 20*time.Millisecond))
			router.Use(func(c *gin.Context) {
				if len(writer.deadlines) != 1 {
					t.Error("deadline not established before authentication")
				}
				state, _ := SystemBackupDownloadBudgetFromContext(c)
				if !writer.deadlines[0].Equal(state.AbsoluteDeadline()) {
					t.Error("transport deadline is not total deadline")
				}
				prepareDeadline, _ := c.Request.Context().Deadline()
				if !prepareDeadline.Before(writer.deadlines[0]) {
					t.Error("prepare incorrectly installed as transport deadline")
				}
				<-c.Request.Context().Done()
				before := time.Now()
				if !HandleSystemBackupDownloadBudgetFailure(c) {
					t.Error("prepare not handled")
				}
				if len(writer.deadlines) != 2 {
					t.Error("error write window missing")
					return
				}
				deadline := writer.deadlines[1]
				if deadline.After(before.Add(5*time.Second+100*time.Millisecond)) || deadline.After(state.AbsoluteDeadline()) || !deadline.After(before) {
					t.Errorf("invalid error write window %s", deadline)
				}
				if total == 2*time.Second && !deadline.Equal(state.AbsoluteDeadline()) {
					t.Error("error window exceeds short absolute deadline")
				}
			})
			router.GET(budgetTestRoute, func(c *gin.Context) { t.Error("aborted chain reached handler") })
			// Multiple wrappers deliberately exercise explicit Unwrap traversal.
			router.ServeHTTP(budgetUnwrapWriter{budgetUnwrapWriter{writer}}, httptest.NewRequest("GET", budgetTestRoute, nil))
			if writer.Code != 504 {
				t.Fatalf("status=%d", writer.Code)
			}
		})
	}
}

func TestSystemBackupDownloadBudgetChecksAtAuthenticationEntry(t *testing.T) {
	router := setupTestRouter()
	router.Use(systemBackupDownloadBudget(2*time.Second, 20*time.Millisecond))
	router.Use(func(c *gin.Context) { <-c.Request.Context().Done(); c.Next() })
	router.Use(AuthMiddleware(nil, nil))
	router.GET(budgetTestRoute, func(c *gin.Context) { t.Error("expired authentication entry admitted") })
	writer := &budgetDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	router.ServeHTTP(writer, httptest.NewRequest("GET", budgetTestRoute, nil))
	if writer.Code != 504 || !strings.Contains(writer.Body.String(), "BACKUP_PREPARE_TIMEOUT") {
		t.Fatalf("missing-header response won over expired prepare: status=%d body=%s", writer.Code, writer.Body)
	}
}

func TestSystemBackupDownloadBudgetDeadlineFailureStopsAdmission(t *testing.T) {
	router := setupTestRouter()
	router.Use(SystemBackupDownloadBudget())
	router.GET(budgetTestRoute, func(c *gin.Context) { t.Error("deadline failure entered handler") })
	writer := &budgetDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), err: errors.New("transport deadline failed")}
	router.ServeHTTP(writer, httptest.NewRequest("GET", budgetTestRoute, nil))
	if writer.Code != 503 {
		t.Fatalf("status=%d", writer.Code)
	}
}

func TestSystemBackupDownloadBudgetLimitDeadlineReportsTransportFailure(t *testing.T) {
	router := setupTestRouter()
	router.Use(SystemBackupDownloadBudget())
	writer := &budgetDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	router.GET(budgetTestRoute, func(c *gin.Context) {
		state, _ := SystemBackupDownloadBudgetFromContext(c)
		writer.err = errors.New("transport deadline failed")
		deadline := time.Now().Add(time.Second)
		if err := state.LimitDeadline(c, deadline); !errors.Is(err, writer.err) {
			t.Errorf("lost deadline error: %v", err)
		}
		if !state.AbsoluteDeadline().Equal(deadline) {
			t.Error("failed transport change extended context budget")
		}
		c.Status(204)
	})
	router.ServeHTTP(writer, httptest.NewRequest("GET", budgetTestRoute, nil))
}

func TestSystemBackupDownloadBudgetProductionLimits(t *testing.T) {
	router := setupTestRouter()
	router.Use(SystemBackupDownloadBudget())
	writer := &budgetDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	router.GET(budgetTestRoute, func(c *gin.Context) {
		state, _ := SystemBackupDownloadBudgetFromContext(c)
		prepare, _ := c.Request.Context().Deadline()
		if state.AbsoluteDeadline().Before(before.Add(10*time.Minute)) || state.AbsoluteDeadline().After(before.Add(10*time.Minute+time.Second)) {
			t.Error("production total budget is not ten minutes")
		}
		if prepare.Before(before.Add(120*time.Second)) || prepare.After(before.Add(121*time.Second)) {
			t.Error("production preparation budget is not 120 seconds")
		}
		c.Status(204)
	})
	router.ServeHTTP(writer, httptest.NewRequest("GET", budgetTestRoute, nil))
}

func TestSystemBackupDownloadBudgetErrorWindowFailureAbortsWithoutJSON(t *testing.T) {
	router := setupTestRouter()
	router.Use(systemBackupDownloadBudget(time.Second, 20*time.Millisecond))
	writer := &budgetDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	router.GET(budgetTestRoute, func(c *gin.Context) {
		<-c.Request.Context().Done()
		writer.err = errors.New("transport no longer writable")
		HandleSystemBackupDownloadBudgetFailure(c)
		t.Error("failed error write window did not abort")
	})
	defer func() {
		value := recover()
		err, ok := value.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Errorf("panic=%v, want ErrAbortHandler", value)
		}
		if writer.Body.Len() != 0 {
			t.Errorf("unwritable transport received JSON: %s", writer.Body)
		}
	}()
	router.ServeHTTP(writer, httptest.NewRequest("GET", budgetTestRoute, nil))
}
