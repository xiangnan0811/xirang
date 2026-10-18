package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

type systemBackupRecoveryLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (output *systemBackupRecoveryLog) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.Write(data)
}

func (output *systemBackupRecoveryLog) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func captureSystemBackupRecoveryLogs(t *testing.T) *systemBackupRecoveryLog {
	t.Helper()
	output := &systemBackupRecoveryLog{}
	previousLogger, previousGin := logger.Log, gin.DefaultErrorWriter
	logger.Log, gin.DefaultErrorWriter = zerolog.New(output), output
	t.Cleanup(func() { logger.Log, gin.DefaultErrorWriter = previousLogger, previousGin })
	return output
}

// Copy the actual router's global chain, including its installed recovery,
// early budget, request ID, CORS and structured logger. Only the terminal
// endpoint is a fault probe; no recovery implementation is replaced in tests.
func systemBackupRecoveryProbe(t *testing.T, method, path string, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	production := NewRouter(Dependencies{})
	probe := gin.New()
	probe.Use(production.Handlers...)
	probe.Handle(method, path, handler)
	return probe
}

func systemBackupRecoveryServer(t *testing.T, handler http.Handler, h2 bool, output io.Writer) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = h2
	server.Config.WriteTimeout = 30 * time.Second
	server.Config.ErrorLog = log.New(output, "transport: ", 0)
	if h2 {
		server.StartTLS()
	} else {
		server.Start()
	}
	server.Client().Timeout = 5 * time.Second
	t.Cleanup(server.Close)
	return server
}

func systemBackupRecoveryRequest(t *testing.T, server *httptest.Server, method, path string) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer FAKE_R14_BEARER_SECRET")
	request.Header.Set("X-Xirang-Step-Up", "FAKE_R14_PROOF_SECRET")
	request.Header.Set("Cookie", "FAKE_R14_COOKIE_SECRET")
	return server.Client().Do(request)
}

func assertSystemBackupRecoverySecretsAbsent(t *testing.T, output string) {
	t.Helper()
	for _, secret := range []string{"FAKE_R14_BEARER_SECRET", "FAKE_R14_PROOF_SECRET", "FAKE_R14_COOKIE_SECRET", "FAKE_R14_PANIC_SECRET"} {
		if strings.Contains(output, secret) {
			t.Fatalf("recovery leaked secret %q: %s", secret, output)
		}
	}
}

func TestSystemBackupRecoverySafePanicRealHTTP(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2=%t", h2), func(t *testing.T) {
			for _, path := range []string{
				"/api/v1/system/backups",
				"/api/v1/system/backups/invalid/shape",
				"/api/v1/system/backups/web/xirang-20261017-120000.db/download",
				"/api/v1/credential-access-grants/system-backup-download",
			} {
				t.Run(path, func(t *testing.T) {
					output := captureSystemBackupRecoveryLogs(t)
					method := http.MethodGet
					if strings.Contains(path, "credential-access-grants") {
						method = http.MethodPost
					}
					probe := systemBackupRecoveryProbe(t, method, path, func(c *gin.Context) {
						if c.Writer.Header().Get("Cache-Control") != "private, no-store" {
							t.Error("R14 cache policy was not installed at entry")
						}
						c.Header("Content-Length", "999999")
						c.Header("Content-Disposition", "attachment; filename=private.tar")
						c.Header("Content-Encoding", "gzip")
						c.Header("ETag", `"stale"`)
						panic("FAKE_R14_PANIC_SECRET")
					})
					server := systemBackupRecoveryServer(t, probe, h2, output)
					response, err := systemBackupRecoveryRequest(t, server, method, path)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := response.Body.Close(); err != nil {
							t.Error(err)
						}
					}()
					body, err := io.ReadAll(response.Body)
					if err != nil || response.StatusCode != http.StatusInternalServerError {
						t.Fatalf("status=%d body=%s err=%v", response.StatusCode, body, err)
					}
					if response.ProtoMajor != map[bool]int{false: 1, true: 2}[h2] {
						t.Fatalf("unexpected transport: %s", response.Proto)
					}
					var envelope struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
						Data    any    `json:"data"`
					}
					if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code != 500 || envelope.Message != "服务器内部错误" || envelope.Data != nil {
						t.Fatalf("unsafe error envelope: %s (%v)", body, err)
					}
					if response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("X-Request-ID") == "" {
						t.Fatalf("missing safe headers: %v", response.Header)
					}
					for _, name := range []string{"Content-Encoding", "Content-Disposition", "ETag"} {
						if response.Header.Get(name) != "" {
							t.Fatalf("stale %s on safe error", name)
						}
					}
					logs := output.String()
					if !strings.Contains(logs, `"event":"system_backup_request_panic"`) || !strings.Contains(logs, response.Header.Get("X-Request-ID")) {
						t.Fatalf("missing fixed event/request ID: %s", logs)
					}
					assertSystemBackupRecoverySecretsAbsent(t, logs+string(body))
				})
			}
		})
	}
}

func TestSystemBackupRecoveryAbortPropagationRealHTTP(t *testing.T) {
	const path = "/api/v1/system/backups/web/xirang-20261017-120000.db/download"
	for _, h2 := range []bool{false, true} {
		for _, failure := range []string{"abort-before-headers", "wrapped-abort-before-headers", "abort-after-headers", "panic-after-headers", "absolute-deadline", "transport-deadline"} {
			t.Run(fmt.Sprintf("h2=%t/%s", h2, failure), func(t *testing.T) {
				output := captureSystemBackupRecoveryLogs(t)
				probe := systemBackupRecoveryProbe(t, http.MethodGet, path, func(c *gin.Context) {
					budget, ok := middleware.SystemBackupDownloadBudgetFromContext(c)
					if !ok {
						t.Error("effective chain omitted early download budget")
						panic(http.ErrAbortHandler)
					}
					switch failure {
					case "abort-before-headers":
						panic(http.ErrAbortHandler)
					case "wrapped-abort-before-headers":
						panic(fmt.Errorf("FAKE_R14_PANIC_SECRET: %w", http.ErrAbortHandler))
					case "absolute-deadline":
						if err := budget.LimitDeadline(c, time.Now().Add(20*time.Millisecond)); err != nil {
							t.Error(err)
						}
						<-c.Request.Context().Done()
						// HTTP/2 may cancel the original request context when its
						// transport deadline fires before the budget timer. Both
						// paths must stop the chain; the client below must still
						// observe an aborted response, never a completed body.
						if !middleware.HandleSystemBackupDownloadBudgetFailure(c) {
							t.Error("absolute deadline did not stop the request")
						}
						return
					}
					c.Header("Content-Type", "application/x-tar")
					c.Header("Content-Length", "10000")
					if _, err := c.Writer.Write([]byte("partial-data")); err != nil {
						t.Error(err)
					}
					controller := systemBackupRecoveryResponseController(c.Writer)
					if err := controller.Flush(); err != nil {
						t.Error(err)
					}
					if failure == "transport-deadline" {
						if err := controller.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
							t.Error(err)
						}
						_, _ = c.Writer.Write([]byte("unflushed-data"))
						if err := controller.Flush(); err == nil {
							t.Error("expired real transport deadline was ignored")
						}
					}
					if failure == "panic-after-headers" {
						panic("FAKE_R14_PANIC_SECRET")
					}
					panic(http.ErrAbortHandler)
				})
				server := systemBackupRecoveryServer(t, probe, h2, output)
				response, err := systemBackupRecoveryRequest(t, server, http.MethodGet, path)
				if response == nil {
					if err == nil {
						t.Fatal("abort returned neither response nor error")
					}
				} else {
					defer func() {
						if err := response.Body.Close(); err != nil {
							t.Error(err)
						}
					}()
					body, readErr := io.ReadAll(response.Body)
					if readErr == nil {
						t.Fatalf("aborted response completed: status=%d body=%q", response.StatusCode, body)
					}
					if strings.Contains(string(body), `"code"`) || strings.Contains(string(body), "服务器内部错误") {
						t.Fatalf("recovery appended JSON: %q", body)
					}
					if strings.Contains(failure, "before-headers") || failure == "absolute-deadline" {
						t.Fatalf("preheader abort committed a response: %d %q", response.StatusCode, body)
					}
				}
				assertSystemBackupRecoverySecretsAbsent(t, output.String())
			})
		}
	}
}

// This wrapper only fails the final flush; all Write and deadline calls use the
// real server transport. It intentionally exposes FlushError, not Gin's Flush.
type systemBackupFinalFlushFailure struct {
	http.ResponseWriter
	flushes int
}

func (writer *systemBackupFinalFlushFailure) SetWriteDeadline(deadline time.Time) error {
	return http.NewResponseController(writer.ResponseWriter).SetWriteDeadline(deadline)
}

func (writer *systemBackupFinalFlushFailure) FlushError() error {
	writer.flushes++
	if writer.flushes == 2 {
		return errors.New("FAKE_R14_PANIC_SECRET")
	}
	return http.NewResponseController(writer.ResponseWriter).Flush()
}

func TestSystemBackupRecoveryFinalFlushFailureRealHTTP(t *testing.T) {
	const path = "/api/v1/system/backups/web/xirang-20261017-120000.db/download"
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2=%t", h2), func(t *testing.T) {
			output := captureSystemBackupRecoveryLogs(t)
			probe := systemBackupRecoveryProbe(t, http.MethodGet, path, func(c *gin.Context) {
				c.Header("Content-Length", fmt.Sprint(len("data-member")+len("checksum-and-footer")))
				if _, err := c.Writer.Write([]byte("data-member")); err != nil {
					t.Error(err)
				}
				controller := systemBackupRecoveryResponseController(c.Writer)
				if err := controller.Flush(); err != nil {
					t.Error(err)
				}
				if _, err := c.Writer.Write([]byte("checksum-and-footer")); err != nil {
					t.Error(err)
				}
				if err := controller.Flush(); err == nil {
					t.Error("final flush failure was hidden")
				}
				panic(http.ErrAbortHandler)
			})
			server := systemBackupRecoveryServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				probe.ServeHTTP(&systemBackupFinalFlushFailure{ResponseWriter: writer}, request)
			}), h2, output)
			response, err := systemBackupRecoveryRequest(t, server, http.MethodGet, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			body, err := io.ReadAll(response.Body)
			if err == nil || strings.Contains(string(body), `"code"`) {
				t.Fatalf("final flush failure did not abort cleanly: body=%q err=%v", body, err)
			}
			assertSystemBackupRecoverySecretsAbsent(t, output.String())
		})
	}
}

func TestSystemBackupRecoveryOrdinaryRoutesKeepGinRecovery(t *testing.T) {
	for _, path := range []string{"/api/v1/ordinary", "/api/v1/system/backups-other", "/api/v1/credential-access-grants/system-backup-download-other"} {
		t.Run(path, func(t *testing.T) {
			output := captureSystemBackupRecoveryLogs(t)
			probe := systemBackupRecoveryProbe(t, http.MethodGet, path, func(*gin.Context) { panic("ordinary-recovery-marker") })
			server := systemBackupRecoveryServer(t, probe, false, output)
			response, err := server.Client().Get(server.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != 500 || len(body) != 0 || response.Header.Get("Cache-Control") != "" {
				t.Fatalf("ordinary recovery changed: status=%d body=%q err=%v", response.StatusCode, body, err)
			}
			if !strings.Contains(output.String(), "ordinary-recovery-marker") || strings.Contains(output.String(), "system_backup_request_panic") {
				t.Fatalf("ordinary path did not use Gin recovery: %s", output.String())
			}
		})
	}
}

type systemBackupRecoveryUnwritable struct {
	http.ResponseWriter
}

func (*systemBackupRecoveryUnwritable) SetWriteDeadline(time.Time) error {
	return http.ErrNotSupported
}

func TestSystemBackupRecoveryDoesNotWriteWhenClientOrTransportUnavailable(t *testing.T) {
	const path = "/api/v1/system/backups"
	for _, h2 := range []bool{false, true} {
		for _, unavailable := range []string{"canceled-client", "unsupported-deadline"} {
			t.Run(fmt.Sprintf("h2=%t/%s", h2, unavailable), func(t *testing.T) {
				output := captureSystemBackupRecoveryLogs(t)
				probe := systemBackupRecoveryProbe(t, http.MethodGet, path, func(*gin.Context) {
					panic("FAKE_R14_PANIC_SECRET")
				})
				server := systemBackupRecoveryServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if unavailable == "canceled-client" {
						ctx, cancel := context.WithCancel(request.Context())
						cancel()
						request = request.WithContext(ctx)
					} else {
						writer = &systemBackupRecoveryUnwritable{ResponseWriter: writer}
					}
					probe.ServeHTTP(writer, request)
				}), h2, output)
				response, err := systemBackupRecoveryRequest(t, server, http.MethodGet, path)
				if response != nil {
					if err := response.Body.Close(); err != nil {
						t.Error(err)
					}
					t.Fatalf("unavailable transport committed status %d", response.StatusCode)
				}
				if err == nil {
					t.Fatal("unavailable transport did not abort")
				}
				assertSystemBackupRecoverySecretsAbsent(t, output.String())
			})
		}
	}
}

func TestSystemBackupRecoveryUnexpectedPanicAfterAbsoluteDeadline(t *testing.T) {
	const path = "/api/v1/system/backups/web/xirang-20261017-120000.db/download"
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2=%t", h2), func(t *testing.T) {
			output := captureSystemBackupRecoveryLogs(t)
			probe := systemBackupRecoveryProbe(t, http.MethodGet, path, func(c *gin.Context) {
				budget, ok := middleware.SystemBackupDownloadBudgetFromContext(c)
				if !ok {
					t.Error("missing download budget")
					panic(http.ErrAbortHandler)
				}
				if err := budget.LimitDeadline(c, time.Now().Add(20*time.Millisecond)); err != nil {
					t.Error(err)
				}
				<-c.Request.Context().Done()
				panic("FAKE_R14_PANIC_SECRET")
			})
			server := systemBackupRecoveryServer(t, probe, h2, output)
			response, err := systemBackupRecoveryRequest(t, server, http.MethodGet, path)
			if response != nil {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
				t.Fatalf("absolute deadline was extended for an error response: %d", response.StatusCode)
			}
			if err == nil {
				t.Fatal("expired budget did not abort")
			}
			assertSystemBackupRecoverySecretsAbsent(t, output.String())
		})
	}
}
