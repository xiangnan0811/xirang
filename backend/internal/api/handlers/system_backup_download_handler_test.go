package handlers

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const systemBackupDownloadTestName = "xirang-20261017-120000-test.db"

type systemBackupDownloadFixture struct {
	db                                            *gorm.DB
	manager                                       *auth.JWTManager
	user                                          model.User
	grant                                         model.CredentialAccessGrant
	token, proof, directory, name, digest, source string
	data                                          []byte
	handler                                       *SystemBackupDownloadHandler
}

func newSystemBackupDownloadFixture(t *testing.T, engine string) *systemBackupDownloadFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := (*gorm.DB)(nil)
	if engine == "postgres" {
		db = accountRecoveryAdmissionDB(t, engine)
	} else {
		db = openStepUpHandlerTestDB(t)
	}
	f := &systemBackupDownloadFixture{db: db, directory: t.TempDir(), name: systemBackupDownloadTestName, source: "web", data: bytes.Repeat([]byte("backup download same-object fixture\n"), 4096)}
	f.manager = newStepUpTestJWTManager(db)
	f.user = seedStepUpUser(t, db, "backup-download-admin", "admin")
	f.token = generatePrimaryToken(t, f.manager, f.user)
	f.proof = generateStepUpProofForAction(t, f.manager, f.user, auth.StepUpActionSystemBackupDownload)
	f.grant = model.CredentialAccessGrant{RequesterUserID: f.user.ID, RequesterUsername: f.user.Username, RequesterRole: "admin", Action: CredentialGrantActionSystemBackupDownload, Purpose: CredentialGrantPurposeSystemBackupDownload, Status: CredentialGrantStatusActive, Reason: "download test", RequestedTTLSeconds: 600, RequestedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	if err := db.Create(&f.grant).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_TYPE", engine)
	t.Setenv("DB_BACKUP_DIR", f.directory)
	t.Setenv("CRON_DB_BACKUP_DIR", f.directory)
	if engine == "postgres" {
		f.source, f.name = "cron", "xirang-postgres-20261017-120000.dump"
	}
	f.writePair(t)
	f.handler = NewSystemBackupDownloadHandler(db, f.manager)
	return f
}

func (f *systemBackupDownloadFixture) writePair(t *testing.T) {
	t.Helper()
	sum := sha256.Sum256(f.data)
	f.digest = hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(f.directory, f.name), f.data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.directory, f.name+".sha256"), []byte(f.digest+"  "+f.name+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *systemBackupDownloadFixture) router(extra ...gin.HandlerFunc) *gin.Engine {
	router := gin.New()
	// Minimal sentinel-preserving chain: production R14 recovery is integrated
	// separately. net/http must see ErrAbortHandler on real HTTP/1 and HTTP/2.
	router.Use(middleware.SystemBackupDownloadBudget())
	router.Use(middleware.AuthMiddleware(f.manager, f.db))
	router.Use(extra...)
	router.GET("/api/v1/system/backups/:source/:filename/download", middleware.RequireRole("admin"), f.handler.Download)
	return router
}

func systemBackupDownloadServer(t *testing.T, handler http.Handler, h2 bool) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = h2
	if h2 {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server
}

func (f *systemBackupDownloadFixture) request(t *testing.T, server *httptest.Server, alter func(*http.Request)) (*http.Response, []byte, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/system/backups/"+f.source+"/"+f.name+"/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set(StepUpHeaderName, f.proof)
	req.Header.Set("If-Match", `"`+f.digest+`"`)
	if alter != nil {
		alter(req)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	return response, data, errors.Join(err, closeErr)
}

func assertSystemBackupArchive(t *testing.T, f *systemBackupDownloadFixture, response *http.Response, body []byte, h2 bool) {
	t.Helper()
	if response == nil || response.StatusCode != 200 {
		t.Fatalf("download response: %+v body=%s", response, body)
	}
	if h2 && response.ProtoMajor != 2 {
		t.Fatalf("not real HTTP/2: %s", response.Proto)
	}
	if response.ContentLength != systemBackupPackagedSize(int64(len(f.data))) || int64(len(body)) != response.ContentLength {
		t.Fatalf("archive length=%d header=%d", len(body), response.ContentLength)
	}
	if response.Header.Get("Content-Type") != "application/x-tar" || response.Header.Get("Cache-Control") != "private, no-store" || response.Header.Get("ETag") != `"`+f.digest+`"` {
		t.Fatalf("unsafe headers: %v", response.Header)
	}
	if response.Header.Get("Content-Disposition") != `attachment; filename="`+f.source+"-"+f.name+`.tar"` {
		t.Fatal("unexpected attachment name")
	}
	reader := tar.NewReader(bytes.NewReader(body))
	for index, name := range []string{f.name, f.name + ".sha256"} {
		header, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != name || header.Format != tar.FormatUSTAR || header.Mode != 0600 || header.Typeflag != tar.TypeReg || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || !header.ModTime.Equal(time.Unix(0, 0)) {
			t.Fatalf("noncanonical header: %+v", header)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		want := f.data
		if index == 1 {
			want = []byte(f.digest + "  " + f.name + "\n")
		}
		if !bytes.Equal(data, want) {
			t.Fatalf("archive member %s differs", name)
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("extra archive entry: %v", err)
	}
}

func TestSystemBackupDownloadCanonicalArchiveHTTP1AndHTTP2(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprint(h2), func(t *testing.T) {
			f := newSystemBackupDownloadFixture(t, "sqlite")
			server := systemBackupDownloadServer(t, f.router(), h2)
			response, data, err := f.request(t, server, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertSystemBackupArchive(t, f, response, data, h2)
		})
	}
}

func TestSystemBackupDownloadPostgres(t *testing.T) {
	f := newSystemBackupDownloadFixture(t, "postgres")
	server := systemBackupDownloadServer(t, f.router(), true)
	response, body, err := f.request(t, server, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSystemBackupArchive(t, f, response, body, true)
	f.source, f.name = "web", systemBackupDownloadTestName
	response, body, err = f.request(t, server, nil)
	if err != nil || response.StatusCode != 501 || !bytes.Contains(body, []byte("BACKUP_SOURCE_UNSUPPORTED")) {
		t.Fatalf("PostgreSQL web gate: %v %v %s", response, err, body)
	}
}

func TestSystemBackupDownloadCronSQLite(t *testing.T) {
	f := newSystemBackupDownloadFixture(t, "sqlite")
	f.source, f.name = "cron", "xirang-sqlite-20261017-120000.db"
	f.writePair(t)
	server := systemBackupDownloadServer(t, f.router(), false)
	response, body, err := f.request(t, server, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSystemBackupArchive(t, f, response, body, false)
}

func TestSystemBackupDownloadAdmissionAndPreconditions(t *testing.T) {
	for _, test := range []struct {
		name   string
		alter  func(*http.Request)
		status int
		code   string
	}{
		{"no session", func(r *http.Request) { r.Header.Del("Authorization") }, 401, ""},
		{"no proof", func(r *http.Request) { r.Header.Del(StepUpHeaderName) }, 403, "STEP_UP_REQUIRED"},
		{"no match", func(r *http.Request) { r.Header.Del("If-Match") }, 428, "BACKUP_PRECONDITION_REQUIRED"},
		{"weak", func(r *http.Request) { r.Header.Set("If-Match", `W/"`+strings.Repeat("a", 64)+`"`) }, 400, "BACKUP_REQUEST_INVALID"},
		{"wildcard", func(r *http.Request) { r.Header.Set("If-Match", "*") }, 400, "BACKUP_REQUEST_INVALID"},
		{"multiple", func(r *http.Request) { r.Header.Add("If-Match", `"`+strings.Repeat("a", 64)+`"`) }, 400, "BACKUP_REQUEST_INVALID"},
		{"changed", func(r *http.Request) { r.Header.Set("If-Match", `"`+strings.Repeat("a", 64)+`"`) }, 412, "BACKUP_CHANGED"},
		{"range", func(r *http.Request) { r.Header.Set("Range", "bytes=0-1") }, 400, "BACKUP_RANGE_UNSUPPORTED"},
		{"unknown source", func(r *http.Request) { r.URL.Path = strings.Replace(r.URL.Path, "/web/", "/other/", 1) }, 400, "BACKUP_REQUEST_INVALID"},
		{"encoded residual", func(r *http.Request) {
			r.URL.Path = strings.Replace(r.URL.Path, systemBackupDownloadTestName, "%2e%2e", 1)
		}, 400, "BACKUP_REQUEST_INVALID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSystemBackupDownloadFixture(t, "sqlite")
			server := systemBackupDownloadServer(t, f.router(), false)
			response, body, err := f.request(t, server, test.alter)
			if err != nil || response.StatusCode != test.status || !bytes.Contains(body, []byte(test.code)) {
				t.Fatalf("got %v %v %s", response, err, body)
			}
		})
	}
}

func TestSystemBackupDownloadDedicatedProofAndExactGrantScope(t *testing.T) {
	for _, kind := range []string{"proof action", "grant action", "grant purpose", "grant user", "grant role", "node", "task", "policy", "expired", "revoked", "operator", "viewer", "reenrollment"} {
		t.Run(kind, func(t *testing.T) {
			f := newSystemBackupDownloadFixture(t, "sqlite")
			update := map[string]any{}
			switch kind {
			case "proof action":
				f.proof = generateStepUpProofForAction(t, f.manager, f.user, auth.StepUpActionConfigExport)
			case "grant action":
				update["action"] = CredentialGrantActionConfigExport
			case "grant purpose":
				update["purpose"] = CredentialGrantPurposeConfigExport
			case "grant user":
				update["requester_user_id"] = f.user.ID + 999
			case "grant role":
				update["requester_role"] = "operator"
			case "node":
				update["node_id"] = uint(1)
			case "task":
				update["task_id"] = uint(1)
			case "policy":
				update["policy_id"] = uint(1)
			case "expired":
				update["expires_at"] = time.Now().Add(-time.Hour)
			case "revoked":
				update["status"] = CredentialGrantStatusRevoked
			case "operator", "viewer":
				if err := f.db.Model(&model.User{}).Where("id = ?", f.user.ID).Update("role", kind).Error; err != nil {
					t.Fatal(err)
				}
				f.user.Role = kind
				f.token = generatePrimaryToken(t, f.manager, f.user)
			case "reenrollment":
				if err := f.db.Model(&model.User{}).Where("id = ?", f.user.ID).Update("totp_reenrollment_required", true).Error; err != nil {
					t.Fatal(err)
				}
			}
			if len(update) > 0 {
				if err := f.db.Model(&f.grant).Updates(update).Error; err != nil {
					t.Fatal(err)
				}
			}
			// Missing content must not turn an authorization rejection into 404.
			if err := os.RemoveAll(f.directory); err != nil {
				t.Fatal(err)
			}
			server := systemBackupDownloadServer(t, f.router(), false)
			response, body, err := f.request(t, server, nil)
			want := 403
			if kind == "reenrollment" {
				want = 401
			}
			if err != nil || response.StatusCode != want {
				t.Fatalf("authorization %v %v %s", response, err, body)
			}
		})
	}
}

func assertSystemBackupDownloadAdmissionAudit(t *testing.T, f *systemBackupDownloadFixture, outcome, reason string, grantID uint, proofOutcome string) {
	t.Helper()
	var events []model.CredentialAuditEvent
	if err := f.db.Where("action = ?", CredentialGrantActionSystemBackupDownload).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	wantMetadata := map[string]any{
		"size": float64(0), "sent_bytes": float64(0),
		"stage": "admission", "reason_code": reason, "grant_id": float64(grantID),
	}
	results, proofs := 0, 0
	for _, event := range events {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
			t.Fatal(err)
		}
		if _, ok := metadata["sent_bytes"]; ok {
			results++
			if event.Outcome != outcome {
				t.Fatalf("download result outcome=%s; want %s", event.Outcome, outcome)
			}
			for key, want := range wantMetadata {
				if metadata[key] != want {
					t.Fatalf("download result metadata[%s]=%v; want %v (metadata=%v)", key, metadata[key], want, metadata)
				}
			}
			if event.UserID != f.user.ID || event.Purpose != CredentialGrantPurposeSystemBackupDownload {
				t.Fatalf("download result lost authenticated identity or scope: %+v", event)
			}
		}
		if metadata["stage"] == "step_up" {
			proofs++
			if proofOutcome == "" || event.Outcome != proofOutcome {
				t.Fatalf("unexpected proof audit: outcome=%s metadata=%v", event.Outcome, metadata)
			}
		}
	}
	wantProofs := 0
	if proofOutcome != "" {
		wantProofs = 1
	}
	if results != 1 || proofs != wantProofs {
		t.Fatalf("audit counts: results=%d proofs=%d; want results=1 proofs=%d", results, proofs, wantProofs)
	}
}

func TestSystemBackupDownloadAdmissionInfrastructureAndProofAudit(t *testing.T) {
	for _, kind := range []string{"proof storage", "invalid proof", "missing budget", "missing session"} {
		t.Run(kind, func(t *testing.T) {
			f := newSystemBackupDownloadFixture(t, "sqlite")
			const sentinel = "PRIVATE_PROOF_DATABASE_PATH_FOR_TEST_ONLY"
			status, code := http.StatusServiceUnavailable, "BACKUP_AUTHORIZATION_UNAVAILABLE"
			outcome, reason, proofOutcome := "failure", "authorization_unavailable", ""
			var grantID uint
			var router *gin.Engine
			switch kind {
			case "proof storage":
				// Arm only after real primary authentication, so this failure
				// belongs to Download's proof verifier, not AuthMiddleware.
				router = f.router(func(c *gin.Context) {
					if err := f.db.Callback().Query().Before("gorm:query").Register("backup_download_proof_unavailable", func(tx *gorm.DB) {
						if tx.Statement.Table == "users" {
							_ = tx.AddError(errors.New(sentinel))
						}
					}); err != nil {
						t.Error(err)
						c.AbortWithStatus(http.StatusInternalServerError)
						return
					}
					c.Next()
				})
			case "invalid proof":
				f.proof = "FAKE_INVALID_PROOF_FOR_TEST_ONLY"
				router = f.router()
				status, code = http.StatusForbidden, "STEP_UP_REQUIRED"
				outcome, reason, proofOutcome = "blocked", "authorization_required", "blocked"
			case "missing budget":
				router = gin.New()
				router.Use(middleware.AuthMiddleware(f.manager, f.db))
				router.GET("/api/v1/system/backups/:source/:filename/download", middleware.RequireRole("admin"), f.handler.Download)
				reason = "budget_unavailable"
			case "missing session":
				// The earlier grant check needs a valid session. Remove it only
				// after that real query to reach the later defensive branch.
				router = f.router(func(c *gin.Context) {
					if err := f.db.Callback().Query().After("gorm:query").Register("backup_download_lose_binding", func(tx *gorm.DB) {
						if tx.Statement.Table == "credential_access_grants" {
							c.Set(middleware.CtxSessionBinding, nil)
						}
					}); err != nil {
						t.Error(err)
						c.AbortWithStatus(http.StatusInternalServerError)
						return
					}
					c.Next()
				})
				grantID, proofOutcome = f.grant.ID, "success"
			}
			finished := make(chan struct{})
			server := systemBackupDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				router.ServeHTTP(w, r)
			}), false)
			response, body, err := f.request(t, server, nil)
			<-finished
			if err != nil || response.StatusCode != status {
				t.Fatalf("admission response %v err=%v body=%s", response, err, body)
			}
			var envelope struct {
				Code int `json:"code"`
				Data struct {
					ErrorCode string `json:"error_code"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code != status || envelope.Data.ErrorCode != code {
				t.Fatalf("unsafe or incorrect admission envelope: %s err=%v", body, err)
			}
			if bytes.Contains(body, []byte(sentinel)) || bytes.Contains(body, []byte(f.token)) || bytes.Contains(body, []byte(f.proof)) ||
				bytes.Contains(body, f.data[:64]) || response.Header.Get("Content-Type") == "application/x-tar" {
				t.Fatal("admission failure leaked private data or archive content")
			}
			assertSystemBackupDownloadAdmissionAudit(t, f, outcome, reason, grantID, proofOutcome)
		})
	}
}

func TestSystemBackupDownloadTimeoutAuditBeforeContextCancellation(t *testing.T) {
	for _, status := range []int{http.StatusGatewayTimeout, http.StatusForbidden} {
		t.Run(fmt.Sprintf("status=%d", status), func(t *testing.T) {
			f := newSystemBackupDownloadFixture(t, "sqlite")
			router := gin.New()
			router.Use(middleware.AuthMiddleware(f.manager, f.db))
			router.GET("/api/v1/system/backups/:source/:filename/download", func(c *gin.Context) {
				// The budget can observe its wall-clock deadline before its
				// context timer runs. Audit the already selected HTTP outcome.
				c.AbortWithStatusJSON(status, Response{Code: status})
				if err := c.Request.Context().Err(); err != nil {
					t.Fatalf("fixture context already cancelled: %v", err)
				}
				result := systemBackupDownloadResult{outcome: "blocked", stage: "admission", reason: "authorization_required"}
				f.handler.auditResult(c, &result)
			})
			request := httptest.NewRequest(http.MethodGet, "/api/v1/system/backups/web/"+f.name+"/download", nil)
			request.Header.Set("Authorization", "Bearer "+f.token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != status {
				t.Fatalf("status=%d; want %d", response.Code, status)
			}
			outcome, reason := "blocked", "authorization_required"
			if status == http.StatusGatewayTimeout {
				outcome, reason = "failure", "budget_exhausted"
			}
			assertSystemBackupDownloadAdmissionAudit(t, f, outcome, reason, 0, "")
		})
	}
}

func TestSystemBackupDownloadFileFailures(t *testing.T) {
	for _, kind := range []string{"missing", "sidecar missing", "sidecar malformed", "sidecar large", "symlink", "empty", "integrity", "too large"} {
		t.Run(kind, func(t *testing.T) {
			f := newSystemBackupDownloadFixture(t, "sqlite")
			name := filepath.Join(f.directory, f.name)
			status, code := 409, "BACKUP_NOT_READY"
			var err error
			switch kind {
			case "missing":
				err = os.Remove(name)
				status, code = 404, "BACKUP_NOT_FOUND"
			case "sidecar missing":
				err = os.Remove(name + ".sha256")
			case "sidecar malformed":
				err = os.WriteFile(name+".sha256", []byte("invalid"), 0600)
			case "sidecar large":
				err = os.WriteFile(name+".sha256", bytes.Repeat([]byte("x"), 4097), 0600)
			case "symlink":
				if err = os.Remove(name); err == nil {
					err = os.Symlink("target", name)
				}
			case "empty":
				err = os.Truncate(name, 0)
			case "integrity":
				err = os.WriteFile(name, bytes.Repeat([]byte("x"), len(f.data)), 0600)
				code = "BACKUP_INTEGRITY_FAILED"
			case "too large":
				err = os.Truncate(name, systemBackupMaxDataBytes+1)
				status, code = 413, "BACKUP_TOO_LARGE"
			}
			if err != nil {
				t.Fatal(err)
			}
			server := systemBackupDownloadServer(t, f.router(), false)
			response, body, err := f.request(t, server, nil)
			if err != nil || response.StatusCode != status || !bytes.Contains(body, []byte(code)) {
				t.Fatalf("file rejection %v %v %s", response, err, body)
			}
		})
	}
}

// The outer writer is a real transport adapter, not ResponseRecorder. It exposes
// errors that Gin.Flush intentionally discards and delegates deadlines to net/http.
type systemBackupFaultWriter struct {
	http.ResponseWriter
	once          sync.Once
	firstWrite    func()
	failWrite     bool
	failFlush     bool
	flushObserved chan struct{}
}

func (w *systemBackupFaultWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		if w.firstWrite != nil {
			w.firstWrite()
		}
	})
	if w.failWrite {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(p)
}
func (w *systemBackupFaultWriter) SetWriteDeadline(deadline time.Time) error {
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}
func (w *systemBackupFaultWriter) FlushError() error {
	if w.flushObserved != nil {
		close(w.flushObserved)
	}
	if w.failFlush {
		return io.ErrClosedPipe
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func TestSystemBackupDownloadPinnedObjectAndTransferAbortHTTP1AndHTTP2(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, kind := range []string{"unlink replace", "rewrite", "grow", "truncate", "grant revoked", "session revoked", "token version", "reenrollment", "write failure", "flush failure"} {
			t.Run(fmt.Sprintf("h2=%v/%s", h2, kind), func(t *testing.T) {
				f := newSystemBackupDownloadFixture(t, "sqlite")
				done := make(chan struct{})
				flushObserved := make(chan struct{})
				router := f.router()
				server := systemBackupDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(done)
					fault := &systemBackupFaultWriter{ResponseWriter: w, failWrite: kind == "write failure", failFlush: kind == "flush failure", flushObserved: flushObserved}
					fault.firstWrite = func() {
						name := filepath.Join(f.directory, f.name)
						var err error
						switch kind {
						case "unlink replace":
							if err = os.Remove(name); err == nil {
								err = os.WriteFile(name, []byte("replacement must never be read"), 0600)
							}
						case "rewrite":
							err = os.WriteFile(name, bytes.Repeat([]byte("z"), len(f.data)), 0600)
						case "grow":
							err = os.Truncate(name, int64(len(f.data)+1))
						case "truncate":
							err = os.Truncate(name, 1)
						case "grant revoked":
							err = f.db.Model(&model.CredentialAccessGrant{}).Where("id = ?", f.grant.ID).Update("status", CredentialGrantStatusRevoked).Error
							// A later valid grant must not replace the selected original.
							other := f.grant
							other.ID = 0
							other.ExpiresAt = time.Now().Add(time.Hour)
							if err == nil {
								err = f.db.Create(&other).Error
							}
						case "session revoked":
							claims, parseErr := f.manager.ParseToken(f.token)
							err = parseErr
							if err == nil {
								err = f.manager.RevokeSession(claims.ID, claims.UserID, claims.ExpiresAt.Time)
							}
						case "token version":
							err = f.db.Model(&model.User{}).Where("id = ?", f.user.ID).Update("token_version", f.user.TokenVersion+1).Error
						case "reenrollment":
							err = f.db.Model(&model.User{}).Where("id = ?", f.user.ID).Update("totp_reenrollment_required", true).Error
						}
						if err != nil {
							t.Error(err)
						}
					}
					router.ServeHTTP(fault, r)
				}), h2)
				response, body, err := f.request(t, server, nil)
				<-done
				if kind == "unlink replace" {
					if err != nil {
						t.Fatal(err)
					}
					assertSystemBackupArchive(t, f, response, body, h2)
				} else {
					// A final flush can fail after a complete footer reached the peer.
					// The observable contract is failure audit and transport abort,
					// not retroactive removal of those already-written bytes.
					if err == nil && kind != "flush failure" {
						t.Fatalf("failed transfer returned complete HTTP response: status=%v length=%d", response, len(body))
					}
					if kind != "flush failure" && bytes.Contains(body, []byte(f.name+".sha256")) {
						t.Fatal("failed transfer emitted checksum member")
					}
					if bytes.Contains(body, []byte("error_code")) {
						t.Fatal("JSON appended to archive")
					}
					if kind == "flush failure" {
						select {
						case <-flushObserved:
						default:
							t.Fatal("error-visible final flush not invoked")
						}
					}
				}
				if len(f.handler.permits) != 0 {
					t.Fatal("transfer leaked permit")
				}
				var events []model.CredentialAuditEvent
				if err := f.db.Where("action = ?", CredentialGrantActionSystemBackupDownload).Find(&events).Error; err != nil {
					t.Fatal(err)
				}
				results := 0
				for _, event := range events {
					var metadata map[string]any
					if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
						t.Fatal(err)
					}
					if _, ok := metadata["sent_bytes"]; ok {
						results++
						if len(metadata) != 8 {
							t.Fatalf("unexpected result metadata: %v", metadata)
						}
						if kind != "unlink replace" && event.Outcome != "failure" {
							t.Fatalf("failed transfer audit=%s", event.Outcome)
						}
					}
				}
				if results != 1 {
					t.Fatalf("result audit count=%d", results)
				}
			})
		}
	}
}

func TestSystemBackupDownloadTwoPermitsAndCancellation(t *testing.T) {
	f := newSystemBackupDownloadFixture(t, "sqlite")
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	router := f.router()
	serverFinished := make(chan struct{}, 3)
	server := systemBackupDownloadServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { serverFinished <- struct{}{} }()
		fault := &systemBackupFaultWriter{ResponseWriter: w, firstWrite: func() {
			if w.Header().Get("Content-Type") != "application/x-tar" {
				return
			}
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}}
		router.ServeHTTP(fault, r)
	}), false)
	var cancel []context.CancelFunc
	finished := make(chan struct{}, 2)
	for range 2 {
		ctx, stop := context.WithCancel(context.Background())
		cancel = append(cancel, stop)
		go func() {
			defer func() { finished <- struct{}{} }()
			_, _, err := f.request(t, server, func(r *http.Request) { *r = *r.WithContext(ctx) })
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled download error=%v; want context.Canceled", err)
			}
		}()
	}
	defer func() {
		for _, stop := range cancel {
			stop()
		}
		close(release)
	}()
	for range 2 {
		<-entered
	}
	response, body, err := f.request(t, server, nil)
	if err != nil || response.StatusCode != 429 || response.Header.Get("Retry-After") != "5" || !bytes.Contains(body, []byte("BACKUP_DOWNLOAD_BUSY")) {
		t.Fatalf("capacity rejection %v %v %s", response, err, body)
	}
	for _, stop := range cancel {
		stop()
	}
	for range 2 {
		<-finished
	}
	for range 3 {
		<-serverFinished
	}
	if len(f.handler.permits) != 0 {
		t.Fatal("cancelled downloads leaked permits")
	}
}

func TestSystemBackupDownloadFixedGrantRevalidationAndQueryFailures(t *testing.T) {
	f := newSystemBackupDownloadFixture(t, "sqlite")
	if err := f.handler.validateSession(context.Background(), f.token, f.grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&f.grant).Update("status", CredentialGrantStatusRevoked).Error; err != nil {
		t.Fatal(err)
	}
	other := f.grant
	other.ID = 0
	other.Status = CredentialGrantStatusActive
	other.ExpiresAt = time.Now().Add(time.Hour)
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.handler.validateSession(context.Background(), f.token, f.grant.ID); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("replacement grant accepted: %v", err)
	}
	if err := f.db.Callback().Query().Before("gorm:query").Register("backup_download_query_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "credential_access_grants" {
			_ = tx.AddError(errors.New("private database failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.handler.validateSession(context.Background(), f.token, other.ID); !errors.Is(err, errSystemBackupAuthorizationUnavailable) {
		t.Fatalf("grant query failure: %v", err)
	}
}

func TestSystemBackupDownloadAuthorizationCheckBoundAndInterval(t *testing.T) {
	f := newSystemBackupDownloadFixture(t, "sqlite")
	if err := f.db.Callback().Query().Before("gorm:query").Register("backup_download_query_deadline", func(tx *gorm.DB) {
		if tx.Statement.Table == "credential_access_grants" {
			deadline, ok := tx.Statement.Context.Deadline()
			if !ok || time.Until(deadline) > systemBackupAuthorizationTimeout {
				t.Error("unbounded authorization query")
			}
			<-tx.Statement.Context.Done()
			_ = tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.handler.validateSession(context.Background(), f.token, f.grant.ID); !errors.Is(err, errSystemBackupAuthorizationUnavailable) {
		t.Fatalf("query timeout: %v", err)
	}
	if err := f.db.Callback().Query().Remove("backup_download_query_deadline"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&f.grant).Update("status", CredentialGrantStatusRevoked).Error; err != nil {
		t.Fatal(err)
	}
	var sent int64
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	writer := &systemBackupTransferWriter{c: c, handler: f.handler, token: f.token, grantID: f.grant.ID, absolute: time.Now().Add(time.Minute), lastCheck: time.Now().Add(-systemBackupAuthorizationInterval), sent: &sent}
	if err := writer.checkAuthorization(false); !errors.Is(err, ErrCredentialGrantRequired) {
		t.Fatalf("five-second check not performed: %v", err)
	}
}

func TestSystemBackupDownloadGrantAbsoluteDeadlineAbortsHTTP1AndHTTP2(t *testing.T) {
	f := newSystemBackupDownloadFixture(t, "sqlite")
	if err := f.db.Callback().Query().Before("gorm:query").Register("backup_download_absolute_grant", func(tx *gorm.DB) {
		if tx.Statement.Table == "credential_access_grants" {
			<-tx.Statement.Context.Done()
			_ = tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, h2 := range []bool{false, true} {
		router := f.router(func(c *gin.Context) {
			budget, ok := middleware.SystemBackupDownloadBudgetFromContext(c)
			if !ok {
				t.Error("missing download budget")
				c.Abort()
				return
			}
			if err := budget.LimitDeadline(c, time.Now().Add(100*time.Millisecond)); err != nil {
				t.Error(err)
				c.Abort()
				return
			}
			c.Next()
		})
		server := systemBackupDownloadServer(t, router, h2)
		response, body, err := f.request(t, server, nil)
		if err == nil {
			t.Fatalf("absolute deadline returned response %v %s", response, body)
		}
	}
}

func TestSystemBackupDownloadGrantStorageFailureIsSafe503(t *testing.T) {
	f := newSystemBackupDownloadFixture(t, "sqlite")
	if err := f.db.Callback().Query().Before("gorm:query").Register("backup_download_grant_unavailable", func(tx *gorm.DB) {
		if tx.Statement.Table == "credential_access_grants" {
			_ = tx.AddError(errors.New("PRIVATE_GRANT_DATABASE_PATH_FOR_TEST_ONLY"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	server := systemBackupDownloadServer(t, f.router(), true)
	response, body, err := f.request(t, server, nil)
	if err != nil || response.StatusCode != 503 || !bytes.Contains(body, []byte("BACKUP_AUTHORIZATION_UNAVAILABLE")) || bytes.Contains(body, []byte("PRIVATE_GRANT")) {
		t.Fatalf("storage failure response %v %v %s", response, err, body)
	}
}

func TestSystemBackupDownloadSessionRevalidatedBeforeHeaders(t *testing.T) {
	f := newSystemBackupDownloadFixture(t, "sqlite")
	var once sync.Once
	if err := f.db.Callback().Query().After("gorm:query").Register("backup_download_revoke_after_selection", func(tx *gorm.DB) {
		if tx.Statement.Table == "credential_access_grants" {
			once.Do(func() {
				claims, err := f.manager.ParseToken(f.token)
				if err == nil {
					err = f.manager.RevokeSession(claims.ID, claims.UserID, claims.ExpiresAt.Time)
				}
				if err != nil {
					t.Error(err)
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	server := systemBackupDownloadServer(t, f.router(), true)
	response, body, err := f.request(t, server, nil)
	if err != nil || response.StatusCode != 503 || !bytes.Contains(body, []byte("BACKUP_AUTHORIZATION_UNAVAILABLE")) {
		t.Fatalf("session revalidation %v %v %s", response, err, body)
	}
	if response.Header.Get("Content-Type") == "application/x-tar" {
		t.Fatal("tar headers committed before session revalidation")
	}
}

func TestSystemBackupDownloadReadPassDetectsChangesAndCancellation(t *testing.T) {
	for _, kind := range []string{"rewrite", "grow", "truncate", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f := newSystemBackupDownloadFixture(t, "sqlite")
			pair, err := pinSystemBackupFilePair(f.directory, systemBackupSourceWeb, "sqlite", f.name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := pair.Close(); err != nil {
					t.Error(err)
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "rewrite":
				err = os.WriteFile(filepath.Join(f.directory, f.name), bytes.Repeat([]byte("x"), len(f.data)), 0600)
			case "grow":
				err = os.Truncate(filepath.Join(f.directory, f.name), int64(len(f.data)+1))
			case "truncate":
				err = os.Truncate(filepath.Join(f.directory, f.name), 1)
			case "cancel":
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			var buffer [32 << 10]byte
			if err := systemBackupReadPass(ctx, pair, nil, buffer[:]); err == nil {
				t.Fatal("changed or cancelled read accepted")
			}
		})
	}
}
