package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestBackupAssetExportHandlerCreateStatusAndCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, jwtManager, user := backupAssetExportHandlerProofFixture(t)
	createProof, _, err := jwtManager.GenerateStepUpToken(user, auth.StepUpActionAssetExportCreate)
	if err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("d", 32)
	selectionDigest := strings.Repeat("e", 64)
	now := time.Now().UTC().Truncate(time.Second)
	service := &backupAssetExportServiceFake{status: assetexport.JobStatus{
		SchemaVersion: 1, ID: jobID, SelectionDigest: selectionDigest,
		ArchiveFormat: assetexport.ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		ExecutionState: assetexport.ExecutionQueued, CleanupState: assetexport.CleanupNone,
		ItemCount: 1, LogicalBytes: 10, CreatedAt: now, AbsoluteDeadline: now.Add(time.Hour),
		Items: []assetexport.JobItemStatus{}, PollAfterSeconds: 2, CanCancel: true,
	}}
	service.created = assetexport.CreateResult{Job: service.status}
	audit := &backupAssetExportAuditFake{}
	handler := NewBackupAssetExportHandler(service, nil, db, jwtManager, audit, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Set(middleware.CtxUsername, "export-admin")
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	router.POST("/api/v1/asset-exports", handler.Create)
	router.GET("/api/v1/asset-exports/:id", handler.Status)
	router.POST("/api/v1/asset-exports/:id/cancel", handler.Cancel)

	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	createBody := `{"schema_version":1,"selection":{"schema_version":1,"kind":"explicit","refs":[{"recovery_point_id":"` +
		pointID + `","entry_id":"` + entryID + `"}]},"archive_format":"zip","archive_profile":"zip_deflate_v1"}`
	create := httptest.NewRequest(http.MethodPost, "/api/v1/asset-exports", strings.NewReader(createBody))
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Idempotency-Key", "0123456789abcdef")
	create.Header.Set(StepUpHeaderName, createProof)
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	if created.Code != http.StatusAccepted || created.Header().Get("Location") != "/api/v1/asset-exports/"+jobID {
		t.Fatalf("create status=%d location=%q body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
	if len(service.createRequests) != 1 {
		t.Fatalf("create calls=%d", len(service.createRequests))
	}
	request := service.createRequests[0]
	if request.Actor.UserID != user.ID || request.Actor.Role != "admin" || request.IdempotencyKey != "0123456789abcdef" ||
		request.Selection.Kind != assetexport.SelectionExplicit || len(request.Selection.Refs) != 1 ||
		request.Selection.Refs[0] != (backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) {
		t.Fatalf("create request=%+v", request)
	}

	status := httptest.NewRecorder()
	router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/asset-exports/"+jobID+"?items_limit=123&items_cursor=cursor", nil))
	if status.Code != http.StatusOK || len(service.statusRequests) != 1 ||
		service.statusRequests[0].ItemsLimit != 123 || service.statusRequests[0].ItemsCursor != "cursor" {
		t.Fatalf("status=%d requests=%+v body=%s", status.Code, service.statusRequests, status.Body.String())
	}

	cancel := httptest.NewRecorder()
	router.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/api/v1/asset-exports/"+jobID+"/cancel", strings.NewReader(`{"schema_version":1}`)))
	if cancel.Code != http.StatusAccepted || len(service.cancelJobIDs) != 1 || service.cancelJobIDs[0] != jobID {
		t.Fatalf("cancel status=%d jobs=%v body=%s", cancel.Code, service.cancelJobIDs, cancel.Body.String())
	}
	if len(audit.inputs) != 2 || audit.inputs[0].Action != backupasset.AuditActionExportCreate ||
		audit.inputs[0].ExportJobID != jobID || audit.inputs[0].ItemCount != 1 ||
		audit.inputs[1].Action != backupasset.AuditActionExportCancel || audit.inputs[1].ExportJobID != jobID {
		t.Fatalf("audit=%+v", audit.inputs)
	}
	encoded, _ := json.Marshal(audit.inputs)
	for _, forbidden := range []string{pointID, entryID, createBody, "0123456789abcdef"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("audit leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestBackupAssetExportHandlerStatusAcceptsReadyZeroPoll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().Truncate(time.Second)
	jobID := strings.Repeat("d", 32)
	readyAt := now.Add(-time.Minute)
	expiresAt := now.Add(time.Hour)
	service := &backupAssetExportServiceFake{status: assetexport.JobStatus{
		SchemaVersion: 1, ID: jobID, SelectionDigest: strings.Repeat("e", 64),
		ArchiveFormat: assetexport.ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		ExecutionState: assetexport.ExecutionReady, ResultKind: assetexport.ResultComplete,
		CleanupState: assetexport.CleanupNone, ItemCount: 1, PackedCount: 1,
		LogicalBytes: 10, ProviderBytes: 10, ArtifactBytes: 32,
		CreatedAt: now.Add(-2 * time.Hour), AbsoluteDeadline: now.Add(2 * time.Hour), ReadyAt: &readyAt, ExpiresAt: &expiresAt,
		Items: []assetexport.JobItemStatus{}, PollAfterSeconds: 0, CanCancel: true, CanDownload: true,
	}}
	handler := NewBackupAssetExportHandler(service, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/asset-exports/:id", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	}, handler.Status)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/asset-exports/"+jobID, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBackupAssetExportHandlerCreateRequiresExactExportCreatePurpose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, jwtManager, user := backupAssetExportHandlerProofFixture(t)
	proofs := make(map[auth.StepUpAction]string)
	for _, action := range []auth.StepUpAction{
		auth.StepUpActionAssetDownload,
		auth.StepUpActionAssetExportCreate,
		auth.StepUpActionAssetExportDownload,
	} {
		proof, _, err := jwtManager.GenerateStepUpToken(user, action)
		if err != nil {
			t.Fatal(err)
		}
		proofs[action] = proof
	}
	jobID := strings.Repeat("d", 32)
	now := time.Now().UTC().Truncate(time.Second)
	status := assetexport.JobStatus{
		SchemaVersion: 1, ID: jobID, SelectionDigest: strings.Repeat("e", 64),
		ArchiveFormat: assetexport.ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		ExecutionState: assetexport.ExecutionQueued, CleanupState: assetexport.CleanupNone,
		ItemCount: 1, CreatedAt: now, AbsoluteDeadline: now.Add(time.Hour), Items: []assetexport.JobItemStatus{}, PollAfterSeconds: 2,
	}
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	body := `{"schema_version":1,"selection":{"schema_version":1,"kind":"explicit","refs":[{"recovery_point_id":"` +
		pointID + `","entry_id":"` + entryID + `"}]},"archive_format":"zip","archive_profile":"zip_deflate_v1"}`
	tests := []struct {
		name   string
		proof  string
		wantOK bool
	}{
		{name: "missing proof"},
		{name: "asset download proof", proof: proofs[auth.StepUpActionAssetDownload]},
		{name: "export download proof", proof: proofs[auth.StepUpActionAssetExportDownload]},
		{name: "exact export create proof", proof: proofs[auth.StepUpActionAssetExportCreate], wantOK: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := &backupAssetExportServiceFake{created: assetexport.CreateResult{Job: status}}
			handler := NewBackupAssetExportHandler(service, nil, db, jwtManager, nil, nil)
			router := gin.New()
			router.POST("/api/v1/asset-exports", func(c *gin.Context) {
				c.Set(middleware.CtxUserID, user.ID)
				c.Set(middleware.CtxUsername, user.Username)
				c.Set(middleware.CtxRole, user.Role)
				c.Next()
			}, handler.Create)
			request := httptest.NewRequest(http.MethodPost, "https://xirang.example/api/v1/asset-exports", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "0123456789abcdef")
			if testCase.proof != "" {
				request.Header.Set(StepUpHeaderName, testCase.proof)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if testCase.wantOK {
				if response.Code != http.StatusAccepted || len(service.createRequests) != 1 {
					t.Fatalf("status=%d calls=%d body=%s", response.Code, len(service.createRequests), response.Body.String())
				}
			} else if response.Code != http.StatusForbidden || len(service.createRequests) != 0 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, len(service.createRequests), response.Body.String())
			}
		})
	}
}

func TestBackupAssetExportHandlerRejectsInvalidArchivePairBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, jwtManager, user := backupAssetExportHandlerProofFixture(t)
	proof, _, err := jwtManager.GenerateStepUpToken(user, auth.StepUpActionAssetExportCreate)
	if err != nil {
		t.Fatal(err)
	}
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	selection := `{"schema_version":1,"selection":{"schema_version":1,"kind":"explicit","refs":[{"recovery_point_id":"` +
		pointID + `","entry_id":"` + entryID + `"}]}`
	tests := []struct {
		name       string
		pairFields string
	}{
		{name: "missing format", pairFields: `,"archive_profile":"zip_deflate_v1"}`},
		{name: "missing profile", pairFields: `,"archive_format":"zip"}`},
		{name: "unknown format", pairFields: `,"archive_format":"rar","archive_profile":"zip_deflate_v1"}`},
		{name: "unknown profile", pairFields: `,"archive_format":"zip","archive_profile":"future_v2"}`},
		{name: "zip crossed with tar none", pairFields: `,"archive_format":"zip","archive_profile":"tar_none_v1"}`},
		{name: "zip crossed with tar gzip", pairFields: `,"archive_format":"zip","archive_profile":"tar_gzip_v1"}`},
		{name: "tar crossed with zip", pairFields: `,"archive_format":"tar","archive_profile":"zip_deflate_v1"}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := &backupAssetExportServiceFake{}
			handler := NewBackupAssetExportHandler(service, nil, db, jwtManager, nil, nil)
			router := gin.New()
			router.POST("/api/v1/asset-exports", func(c *gin.Context) {
				c.Set(middleware.CtxUserID, user.ID)
				c.Set(middleware.CtxUsername, user.Username)
				c.Set(middleware.CtxRole, user.Role)
				c.Next()
			}, handler.Create)

			request := httptest.NewRequest(http.MethodPost, "https://xirang.example/api/v1/asset-exports",
				strings.NewReader(selection+testCase.pairFields))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "0123456789abcdef")
			request.Header.Set(StepUpHeaderName, proof)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest || len(service.createRequests) != 0 {
				t.Fatalf("status=%d service_calls=%d body=%s", response.Code, len(service.createRequests), response.Body.String())
			}
		})
	}
}

func TestBackupAssetExportHandlerRejectsEmptyExplicitSelectionBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, jwtManager, user := backupAssetExportHandlerProofFixture(t)
	proof, _, err := jwtManager.GenerateStepUpToken(user, auth.StepUpActionAssetExportCreate)
	if err != nil {
		t.Fatal(err)
	}
	service := &backupAssetExportServiceFake{}
	handler := NewBackupAssetExportHandler(service, nil, db, jwtManager, nil, nil)
	router := gin.New()
	router.POST("/api/v1/asset-exports", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Set(middleware.CtxUsername, user.Username)
		c.Set(middleware.CtxRole, user.Role)
		c.Next()
	}, handler.Create)
	request := httptest.NewRequest(http.MethodPost, "https://xirang.example/api/v1/asset-exports", strings.NewReader(
		`{"schema_version":1,"selection":{"schema_version":1,"kind":"explicit","refs":[]},"archive_format":"zip","archive_profile":"zip_deflate_v1"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "0123456789abcdef")
	request.Header.Set(StepUpHeaderName, proof)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || len(service.createRequests) != 0 {
		t.Fatalf("status=%d service_calls=%d body=%s", response.Code, len(service.createRequests), response.Body.String())
	}
}

func TestBackupAssetExportHandlerPrivateNetworkHTTPDownloadTicketRequiresExactExportDownloadPurpose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	db, jwtManager, user := backupAssetExportHandlerProofFixture(t)
	proofs := make(map[auth.StepUpAction]string)
	for _, action := range []auth.StepUpAction{
		auth.StepUpActionAssetDownload,
		auth.StepUpActionAssetExportCreate,
		auth.StepUpActionAssetExportDownload,
	} {
		proof, _, err := jwtManager.GenerateStepUpToken(user, action)
		if err != nil {
			t.Fatal(err)
		}
		proofs[action] = proof
	}
	jobID := strings.Repeat("d", 32)
	tests := []struct {
		name   string
		proof  string
		wantOK bool
	}{
		{name: "missing proof"},
		{name: "asset download proof", proof: proofs[auth.StepUpActionAssetDownload]},
		{name: "export create proof", proof: proofs[auth.StepUpActionAssetExportCreate]},
		{name: "exact export download proof", proof: proofs[auth.StepUpActionAssetExportDownload], wantOK: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			ticket := backupAssetExportHandlerTicket(t)
			ticket.Cookie.Secure = false
			delivery := &backupAssetExportDeliveryFake{ticket: ticket}
			handler := NewBackupAssetExportHandler(nil, delivery, db, jwtManager, nil, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{
					TicketTimeout: 5 * time.Second, AllowInsecurePrivateNetwork: true,
				}, nil
			}).WithSchemePolicy(policy)
			router := gin.New()
			router.POST("/api/v1/asset-exports/:id/download-ticket", func(c *gin.Context) {
				c.Set(middleware.CtxUserID, user.ID)
				c.Set(middleware.CtxUsername, user.Username)
				c.Set(middleware.CtxRole, user.Role)
				c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
					JTI: strings.Repeat("f", 32), UserID: user.ID, Role: user.Role,
					TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
				})
				c.Next()
			}, handler.DownloadTicket)
			request := httptest.NewRequest(http.MethodPost,
				"http://xirang.example/api/v1/asset-exports/"+jobID+"/download-ticket",
				strings.NewReader(`{"schema_version":1}`))
			request.RemoteAddr = "127.0.0.1:43210"
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-Proto", "http")
			request.Header.Set("X-Forwarded-For", "192.168.20.45")
			if testCase.proof != "" {
				request.Header.Set(StepUpHeaderName, testCase.proof)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if testCase.wantOK {
				if response.Code != http.StatusOK || len(delivery.requests) != 1 ||
					delivery.requests[0].Proof.Action != auth.StepUpActionAssetExportDownload ||
					delivery.requests[0].Session.JTI != strings.Repeat("f", 32) || delivery.requests[0].SecureCookie {
					t.Fatalf("status=%d requests=%+v body=%s", response.Code, delivery.requests, response.Body.String())
				}
				if cookies := response.Header().Values("Set-Cookie"); len(cookies) != 1 ||
					!strings.Contains(cookies[0], "Path=/api/v1/asset-content/") || strings.Contains(cookies[0], "Secure") {
					t.Fatalf("Set-Cookie=%v", cookies)
				}
			} else if response.Code != http.StatusForbidden || len(delivery.requests) != 0 {
				t.Fatalf("status=%d requests=%+v body=%s", response.Code, delivery.requests, response.Body.String())
			}
		})
	}
	delivery := &backupAssetExportDeliveryFake{ticket: backupAssetExportHandlerTicket(t)}
	handler := NewBackupAssetExportHandler(nil, delivery, db, jwtManager, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	})
	router := gin.New()
	router.POST("/api/v1/asset-exports/:id/download-ticket", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Set(middleware.CtxUsername, user.Username)
		c.Set(middleware.CtxRole, user.Role)
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: user.ID, Role: user.Role,
			TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		c.Next()
	}, handler.DownloadTicket)
	insecure := httptest.NewRequest(http.MethodPost,
		"http://xirang.example/api/v1/asset-exports/"+jobID+"/download-ticket",
		strings.NewReader(`{"schema_version":1}`))
	insecure.RemoteAddr = "203.0.113.5:43210"
	insecure.Header.Set("Content-Type", "application/json")
	insecure.Header.Set(StepUpHeaderName, proofs[auth.StepUpActionAssetExportDownload])
	insecureResponse := httptest.NewRecorder()
	router.ServeHTTP(insecureResponse, insecure)
	assertSecureTransportRequiredResponse(t, insecureResponse)
	if len(delivery.requests) != 0 {
		t.Fatalf("insecure Export ticket reached delivery requests=%d", len(delivery.requests))
	}
}

func TestBackupAssetExportHandlerDownloadTicketRejectsUnsafeIssuedTicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, jwtManager, user := backupAssetExportHandlerProofFixture(t)
	proof, _, err := jwtManager.GenerateStepUpToken(user, auth.StepUpActionAssetExportDownload)
	if err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("d", 32)
	tests := []struct {
		name   string
		mutate func(*assetexport.IssuedDeliveryTicket)
	}{
		{name: "same site lax", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Cookie.SameSite = http.SameSiteLaxMode
		}},
		{name: "same site none", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Cookie.SameSite = http.SameSiteNoneMode
		}},
		{name: "cookie domain", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Cookie.Domain = "example.invalid"
		}},
		{name: "expired ticket", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			expired := time.Now().UTC().Add(-time.Minute)
			ticket.Descriptor.ExpiresAt = expired
			ticket.Descriptor.IdleExpiresAt = expired
			ticket.Cookie.Expires = expired
		}},
		{name: "cookie expiry mismatch", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Cookie.Expires = ticket.Cookie.Expires.Add(time.Second)
		}},
		{name: "content url query", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.ContentURL += "?ticket=unexpected"
			ticket.Cookie.Path = ticket.Descriptor.ContentURL
		}},
		{name: "content url child path", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.ContentURL += "/unexpected"
			ticket.Cookie.Path = ticket.Descriptor.ContentURL
		}},
		{name: "range none", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.Range = content.RangeNone
		}},
		{name: "unsafe content type", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.ContentType = "text/html"
		}},
		{name: "weak etag", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.ETag = `W/"` + strings.Repeat("e", 64) + `"`
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			ticket := backupAssetExportHandlerTicket(t)
			testCase.mutate(&ticket)
			delivery := &backupAssetExportDeliveryFake{ticket: ticket}
			handler := NewBackupAssetExportHandler(nil, delivery, db, jwtManager, nil, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
			})
			router := gin.New()
			router.POST("/api/v1/asset-exports/:id/download-ticket", func(c *gin.Context) {
				c.Set(middleware.CtxUserID, user.ID)
				c.Set(middleware.CtxUsername, user.Username)
				c.Set(middleware.CtxRole, user.Role)
				c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
					JTI: strings.Repeat("f", 32), UserID: user.ID, Role: user.Role,
					TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
				})
				c.Next()
			}, handler.DownloadTicket)
			request := httptest.NewRequest(http.MethodPost,
				"https://xirang.example/api/v1/asset-exports/"+jobID+"/download-ticket",
				strings.NewReader(`{"schema_version":1}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(StepUpHeaderName, proof)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || len(delivery.requests) != 1 ||
				len(response.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("status=%d requests=%+v set_cookie=%v body=%s", response.Code, delivery.requests,
					response.Header().Values("Set-Cookie"), response.Body.String())
			}
		})
	}
}

func TestBackupAssetExportHandlerRejectsMalformedAndHidesForeignJob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &backupAssetExportServiceFake{statusErr: assetexport.ErrNotFound}
	handler := NewBackupAssetExportHandler(service, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	router.POST("/api/v1/asset-exports", handler.Create)
	router.GET("/api/v1/asset-exports/:id", handler.Status)

	bad := httptest.NewRequest(http.MethodPost, "/api/v1/asset-exports?selection=raw", strings.NewReader(`{"schema_version":1}`))
	bad.Header.Add("Idempotency-Key", "0123456789abcdef")
	bad.Header.Add("Idempotency-Key", "fedcba9876543210")
	badResponse := httptest.NewRecorder()
	router.ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest || len(service.createRequests) != 0 {
		t.Fatalf("malformed status=%d calls=%d", badResponse.Code, len(service.createRequests))
	}

	foreign := httptest.NewRecorder()
	router.ServeHTTP(foreign, httptest.NewRequest(http.MethodGet, "/api/v1/asset-exports/"+strings.Repeat("f", 32), nil))
	if foreign.Code != http.StatusNotFound || strings.Contains(foreign.Body.String(), "foreign") {
		t.Fatalf("foreign status=%d body=%s", foreign.Code, foreign.Body.String())
	}
}

type backupAssetExportServiceFake struct {
	created        assetexport.CreateResult
	status         assetexport.JobStatus
	createErr      error
	statusErr      error
	cancelErr      error
	createRequests []assetexport.CreateRequest
	statusRequests []assetexport.StatusRequest
	cancelActors   []assetexport.SelectionActor
	cancelJobIDs   []string
}

func (fake *backupAssetExportServiceFake) Create(_ context.Context, request assetexport.CreateRequest) (assetexport.CreateResult, error) {
	fake.createRequests = append(fake.createRequests, request)
	return fake.created, fake.createErr
}

func (fake *backupAssetExportServiceFake) Status(_ context.Context, request assetexport.StatusRequest) (assetexport.JobStatus, error) {
	fake.statusRequests = append(fake.statusRequests, request)
	return fake.status, fake.statusErr
}

func (fake *backupAssetExportServiceFake) Cancel(_ context.Context, actor assetexport.SelectionActor, jobID string) (assetexport.JobStatus, error) {
	fake.cancelActors = append(fake.cancelActors, actor)
	fake.cancelJobIDs = append(fake.cancelJobIDs, jobID)
	return fake.status, fake.cancelErr
}

type backupAssetExportAuditFake struct {
	inputs []backupasset.AuditEventInput
}

type backupAssetExportDeliveryFake struct {
	ticket   assetexport.IssuedDeliveryTicket
	err      error
	requests []assetexport.ExportDeliveryIssueRequest
}

func (fake *backupAssetExportDeliveryFake) IssueExport(
	_ context.Context,
	request assetexport.ExportDeliveryIssueRequest,
) (assetexport.IssuedDeliveryTicket, error) {
	fake.requests = append(fake.requests, request)
	return fake.ticket, fake.err
}

func backupAssetExportHandlerTicket(t *testing.T) assetexport.IssuedDeliveryTicket {
	t.Helper()
	now := time.Now().UTC()
	cookie, err := content.NewDeliveryCookie(
		strings.Repeat("c", 32), "v1."+strings.Repeat("A", 43), now.Add(time.Minute), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	return assetexport.IssuedDeliveryTicket{
		Descriptor: assetexport.DeliveryTicketDescriptor{
			SchemaVersion: 1, ContentURL: cookie.Path, ContentType: "application/zip",
			ContentLength: 10, ETag: `"` + strings.Repeat("e", 64) + `"`, Range: content.RangeSingle,
			ExpiresAt: cookie.Expires, IdleExpiresAt: now.Add(30 * time.Second),
		},
		Cookie: cookie,
	}
}

func (fake *backupAssetExportAuditFake) Write(_ context.Context, input backupasset.AuditEventInput) error {
	fake.inputs = append(fake.inputs, input)
	return nil
}

func backupAssetExportHandlerProofFixture(t *testing.T) (*gorm.DB, *auth.JWTManager, model.User) {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Username: "export-proof-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "admin", TokenVersion: 1, TOTPEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return db, auth.NewJWTManager("FAKE_EXPORT_PROOF_JWT_SECRET_FOR_TEST_ONLY", time.Hour), user
}
