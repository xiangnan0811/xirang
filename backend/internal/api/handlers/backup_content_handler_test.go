package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/api/docs"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBackupContentSwaggerRequiresTruncationFact(t *testing.T) {
	var document struct {
		Definitions map[string]struct {
			Required []string `json:"required"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatal(err)
	}
	schema, ok := document.Definitions["xirang_backend_internal_backupasset_content.TicketDescriptor"]
	if !ok {
		t.Fatal("ticket descriptor Swagger schema is missing")
	}
	for _, field := range schema.Required {
		if field == "truncated" {
			return
		}
	}
	t.Fatalf("ticket descriptor Swagger required fields=%v; truncated is missing", schema.Required)
}

func TestBackupContentIssueStrictJSONUsesSafeSessionAndSetsOneCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &backupContentServiceFake{}
	now := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	cookie, err := content.NewDeliveryCookie(strings.Repeat("d", 32), "v1."+strings.Repeat("A", 43), now.Add(time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	fake.ticket = content.IssuedTicket{
		Descriptor: content.TicketDescriptor{
			SchemaVersion: 1, ContentURL: cookie.Path, Action: content.DeliveryPreview,
			Renderer: content.RendererSafeRaster, Profile: content.ProfileRasterV1,
			ContentType: "image/png", ContentLength: 83, ETag: `"etag"`, Range: content.RangeSingle,
			Classification: content.ClassificationNonSecret, ExpiresAt: now.Add(time.Minute),
			IdleExpiresAt: now.Add(30 * time.Second), FallbackActions: []content.DeliveryAction{},
		},
		Cookie: cookie,
	}
	handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	})
	router := gin.New()
	router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "operator")
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 3, ExpiresAt: now,
		})
		c.Next()
	}, handler.Issue)

	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	request := httptest.NewRequest(http.MethodPost,
		"https://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/delivery-tickets",
		strings.NewReader(`{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(fake.issueRequests) != 1 {
		t.Fatalf("issue calls=%d", len(fake.issueRequests))
	}
	issued := fake.issueRequests[0]
	if issued.Ref.RecoveryPointID != pointID || issued.Ref.EntryID != entryID ||
		issued.Actor.UserID != 42 || issued.Session.JTI != strings.Repeat("f", 32) ||
		issued.Session.TokenVersion != 3 || !issued.SecureCookie || issued.Proof != nil {
		t.Fatalf("issue request=%+v", issued)
	}
	if values := response.Header().Values("Set-Cookie"); len(values) != 1 ||
		!strings.Contains(values[0], "HttpOnly") || !strings.Contains(values[0], "SameSite=Strict") ||
		!strings.Contains(values[0], "Secure") || !strings.Contains(values[0], "Path="+cookie.Path) {
		t.Fatalf("Set-Cookie=%v", values)
	}
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(envelope.Data)
	if !strings.Contains(string(encoded), `"schema_version":1`) || !strings.Contains(string(encoded), `"content_url"`) ||
		!strings.Contains(string(encoded), `"truncated":false`) ||
		strings.Contains(string(encoded), "Cookie") || strings.Contains(string(encoded), "Grant") {
		t.Fatalf("ticket envelope=%s", encoded)
	}
}

func TestBackupContentIssueAcceptsSafePreviewIntentAndReturnsResolvedProduct(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	fake := &backupContentServiceFake{ticket: backupContentHandlerTestTicket(
		t, content.DeliveryPreview, content.RendererPlainText, content.ProfileTextV2,
	)}
	fake.ticket.Descriptor.ContentType = "text/plain; charset=utf-8"
	handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	})
	router := gin.New()
	router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "operator")
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 3, ExpiresAt: now,
		})
		c.Next()
	}, handler.Issue)

	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	request := httptest.NewRequest(http.MethodPost,
		"https://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/delivery-tickets",
		strings.NewReader(`{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(fake.issueRequests) != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, len(fake.issueRequests), response.Body.String())
	}
	issued := fake.issueRequests[0]
	if issued.Action != content.DeliveryPreview || issued.PreviewIntent != content.PreviewIntentSafePreviewV1 ||
		issued.Renderer != "" || issued.Profile != "" {
		t.Fatalf("safe preview issue request=%+v", issued)
	}
	if fake.ticket.Descriptor.Renderer != content.RendererPlainText || fake.ticket.Descriptor.Profile != content.ProfileTextV2 {
		t.Fatalf("resolved descriptor=%+v", fake.ticket.Descriptor)
	}
}

func TestBackupContentIssueRejectsInvalidPreviewIntentUnionBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	path := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/delivery-tickets"
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown intent", body: `{"schema_version":1,"action":"preview","preview_intent":"future_preview_v2"}`},
		{name: "mixed intent and exact product", body: `{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1","renderer":"safe_raster","profile":"raster_v1"}`},
		{name: "intent missing action", body: `{"schema_version":1,"preview_intent":"safe_preview_v1"}`},
		{name: "exact preview missing profile", body: `{"schema_version":1,"action":"preview","renderer":"safe_raster"}`},
		{name: "exact preview missing renderer", body: `{"schema_version":1,"action":"preview","profile":"raster_v1"}`},
		{name: "download intent", body: `{"schema_version":1,"action":"download","preview_intent":"safe_preview_v1"}`},
		{name: "intent with null renderer field", body: `{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1","renderer":null}`},
		{name: "null intent with exact product", body: `{"schema_version":1,"action":"preview","preview_intent":null,"renderer":"safe_raster","profile":"raster_v1"}`},
		{name: "extra field", body: `{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1","selection":"auto"}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &backupContentServiceFake{}
			handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
			})
			router := gin.New()
			router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
				c.Set(middleware.CtxUserID, uint(42))
				c.Set(middleware.CtxUsername, "operator")
				c.Set(middleware.CtxRole, "operator")
				c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
					JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 3,
					ExpiresAt: time.Now().UTC().Add(time.Hour),
				})
				c.Next()
			}, handler.Issue)
			request := httptest.NewRequest(http.MethodPost, "https://xirang.example"+path, strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || len(fake.issueRequests) != 0 || len(response.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("status=%d calls=%d cookie=%v body=%s", response.Code, len(fake.issueRequests), response.Header().Values("Set-Cookie"), response.Body.String())
			}
		})
	}
}

func TestBackupContentIssueMapsUnsafeRendererResolutionToTypedUnprocessableEntity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	for _, issueErr := range []error{content.ErrRendererUnsupported, content.ErrMIMEConfusion} {
		fake := &backupContentServiceFake{issueErr: issueErr}
		handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
			return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
		})
		router := gin.New()
		router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
			c.Set(middleware.CtxUserID, uint(42))
			c.Set(middleware.CtxUsername, "operator")
			c.Set(middleware.CtxRole, "operator")
			c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
				JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 3,
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			})
			c.Next()
		}, handler.Issue)
		request := httptest.NewRequest(http.MethodPost,
			"https://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/delivery-tickets",
			strings.NewReader(`{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var envelope struct {
			Code int `json:"code"`
			Data struct {
				Reason struct {
					Code   string            `json:"code"`
					Params map[string]string `json:"params"`
				} `json:"reason"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode renderer error: %v body=%s", err, response.Body.String())
		}
		if response.Code != http.StatusUnprocessableEntity || envelope.Code != http.StatusUnprocessableEntity ||
			envelope.Data.Reason.Code != "preview_renderer_unsupported" || envelope.Data.Reason.Params == nil ||
			len(envelope.Data.Reason.Params) != 0 || len(response.Header().Values("Set-Cookie")) != 0 {
			t.Fatalf("issueErr=%v status=%d envelope=%+v cookie=%v body=%s", issueErr, response.Code, envelope,
				response.Header().Values("Set-Cookie"), response.Body.String())
		}
	}
}

func TestBackupContentIssueMapsTypedContentCapabilityWithoutPrivateEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name   string
		reason backupasset.CapabilityReason
	}{
		{name: "sequential read", reason: backupasset.CapabilityReason{Code: backupasset.CapabilitySequentialReadUnavailable}},
		{name: "native range", reason: backupasset.CapabilityReason{Code: backupasset.CapabilityRangeUnavailable}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Set(middleware.RequestIDKey, "safe-correlation")
			respondBackupContentIssueError(context, &content.CapabilityError{Reason: testCase.reason})

			var envelope struct {
				Code int `json:"code"`
				Data struct {
					Reason        backupasset.CapabilityReason `json:"reason"`
					CorrelationID string                       `json:"correlation_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode capability error: %v body=%s", err, response.Body.String())
			}
			if response.Code != http.StatusNotImplemented || envelope.Code != http.StatusNotImplemented ||
				envelope.Data.Reason.Code != testCase.reason.Code || envelope.Data.Reason.Params == nil ||
				len(envelope.Data.Reason.Params) != 0 ||
				envelope.Data.CorrelationID != "safe-correlation" ||
				strings.Contains(response.Body.String(), "provider locator") || strings.Contains(response.Body.String(), "private/path") {
				t.Fatalf("status=%d envelope=%+v body=%s", response.Code, envelope, response.Body.String())
			}
		})
	}
}

func TestBackupContentIssueMapsClosedSourceStagesWithoutPrivateEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		stage content.SourceFailureStage
		code  string
	}{
		{stage: content.SourceFailureOpen, code: "preview_source_open_failed"},
		{stage: content.SourceFailureRead, code: "preview_source_read_failed"},
		{stage: content.SourceFailureChanged, code: "preview_source_changed"},
		{stage: content.SourceFailureTimeout, code: "preview_source_timeout"},
		{stage: content.SourceFailureCancellation, code: "preview_source_canceled"},
		{stage: content.SourceFailureCapability, code: "preview_source_capability"},
	} {
		t.Run(string(testCase.stage), func(t *testing.T) {
			response := httptest.NewRecorder()
			requestContext, _ := gin.CreateTestContext(response)
			requestContext.Set(middleware.RequestIDKey, "safe-correlation")
			respondBackupContentIssueError(requestContext, content.NewSourceFailureError(
				testCase.stage,
				errors.New("FAKE_PRIVATE_PROVIDER_LOCATOR_AND_OUTPUT_FOR_TEST_ONLY"),
			))

			var envelope struct {
				Code int `json:"code"`
				Data struct {
					Reason struct {
						Code   string            `json:"code"`
						Params map[string]string `json:"params"`
					} `json:"reason"`
					CorrelationID string `json:"correlation_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode source-stage error: %v body=%s", err, response.Body.String())
			}
			if response.Code != http.StatusServiceUnavailable || envelope.Code != http.StatusServiceUnavailable ||
				envelope.Data.Reason.Code != testCase.code || envelope.Data.Reason.Params == nil ||
				len(envelope.Data.Reason.Params) != 0 || envelope.Data.CorrelationID != "safe-correlation" ||
				strings.Contains(response.Body.String(), "FAKE_PRIVATE") {
				t.Fatalf("stage=%s status=%d envelope=%+v body=%s", testCase.stage, response.Code, envelope, response.Body.String())
			}
		})
	}
}

func TestBackupContentIssueCapabilityStatusAndCodeSetAreClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   backupasset.CapabilityCode
	}{
		{
			name: "transient provider is retryable service unavailable",
			err: &backuprepository.CapabilityError{
				Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable},
			},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   backupasset.CapabilityProviderUnavailable,
		},
		{
			name: "out of scope restore reason is generic",
			err: &backuprepository.CapabilityError{
				Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityRestoreUnavailable},
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "repository reason with params is generic",
			err: &backuprepository.CapabilityError{Reason: backupasset.CapabilityReason{
				Code: backupasset.CapabilityProviderUnavailable, Params: map[string]string{"capability": "private-detail"},
			}},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "content reason with params is generic",
			err: &content.CapabilityError{Reason: backupasset.CapabilityReason{
				Code: backupasset.CapabilityRangeUnavailable, Params: map[string]string{"capability": "private-detail"},
			}},
			wantStatus: http.StatusServiceUnavailable,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Set(middleware.RequestIDKey, "safe-correlation")
			respondBackupContentIssueError(context, testCase.err)

			var envelope struct {
				Code int `json:"code"`
				Data *struct {
					Reason        backupasset.CapabilityReason `json:"reason"`
					CorrelationID string                       `json:"correlation_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode capability error: %v body=%s", err, response.Body.String())
			}
			if response.Code != testCase.wantStatus || envelope.Code != testCase.wantStatus {
				t.Fatalf("status=%d envelope=%+v body=%s", response.Code, envelope, response.Body.String())
			}
			if testCase.wantCode == "" {
				if envelope.Data != nil || strings.Contains(response.Body.String(), "private-detail") {
					t.Fatalf("unexpected out-of-scope capability detail envelope=%+v body=%s", envelope, response.Body.String())
				}
				return
			}
			if envelope.Data == nil || envelope.Data.Reason.Code != testCase.wantCode ||
				envelope.Data.CorrelationID != "safe-correlation" {
				t.Fatalf("typed capability envelope=%+v body=%s", envelope, response.Body.String())
			}
		})
	}
}

func TestValidIssuedBackupContentTicketAcceptsOnlyResolvedExactSafePreviewProduct(t *testing.T) {
	var payload backupContentTicketPayload
	if err := json.Unmarshal([]byte(`{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1"}`), &payload); err != nil {
		t.Fatal(err)
	}
	plain := backupContentHandlerTestTicket(t, content.DeliveryPreview, content.RendererPlainText, content.ProfileTextV2)
	if !validIssuedBackupContentTicket(plain, payload, true) {
		t.Fatalf("faithful plain-text ticket rejected: %+v", plain.Descriptor)
	}
	truncated := plain
	truncated.Descriptor.Truncated = true
	if !validIssuedBackupContentTicket(truncated, payload, true) {
		t.Fatalf("truncated faithful text ticket rejected: %+v", truncated.Descriptor)
	}
	for _, mutate := range []func(*content.IssuedTicket){
		func(ticket *content.IssuedTicket) {
			ticket.Descriptor.Renderer = content.Renderer(content.PreviewIntentSafePreviewV1)
		},
		func(ticket *content.IssuedTicket) { ticket.Descriptor.Profile = "" },
		func(ticket *content.IssuedTicket) { ticket.Descriptor.Range = content.RangeSingle },
	} {
		ticket := plain
		mutate(&ticket)
		if validIssuedBackupContentTicket(ticket, payload, true) {
			t.Fatalf("unresolved or invalid safe-preview ticket accepted: %+v", ticket.Descriptor)
		}
	}
	native := backupContentHandlerTestTicket(t, content.DeliveryPreview, content.RendererSafeRaster, content.ProfileRasterV1)
	native.Descriptor.Truncated = true
	if validIssuedBackupContentTicket(native, payload, true) {
		t.Fatalf("truncated native ticket accepted: %+v", native.Descriptor)
	}
	native.Descriptor.Truncated = false
	native.Descriptor.Range = content.RangeNone
	if validIssuedBackupContentTicket(native, payload, true) {
		t.Fatalf("safe native ticket without Range accepted: %+v", native.Descriptor)
	}
}

func TestBackupContentIssueRejectsUnknownTrailingQueryAndMissingSessionBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	path := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/delivery-tickets"
	tests := []struct {
		name       string
		target     string
		body       string
		setSession bool
	}{
		{name: "unknown field", target: path, body: `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1","ticket":"bad"}`, setSession: true},
		{name: "duplicate field", target: path, body: `{"schema_version":1,"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`, setSession: true},
		{name: "trailing JSON", target: path, body: `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}{}`, setSession: true},
		{name: "query", target: path + "?token=bad", body: `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`, setSession: true},
		{name: "missing session", target: path, body: `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &backupContentServiceFake{}
			handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
			})
			router := gin.New()
			router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
				if testCase.setSession {
					now := time.Now().UTC().Add(time.Hour)
					c.Set(middleware.CtxUserID, uint(42))
					c.Set(middleware.CtxRole, "operator")
					c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
						JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 1, ExpiresAt: now,
					})
				}
				c.Next()
			}, handler.Issue)
			request := httptest.NewRequest(http.MethodPost, "https://xirang.example"+testCase.target, strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code < 400 || len(fake.issueRequests) != 0 || len(response.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("status=%d issue calls=%d cookie=%v body=%s", response.Code, len(fake.issueRequests), response.Header().Values("Set-Cookie"), response.Body.String())
			}
		})
	}
}

func TestBackupContentIssuePrivateNetworkHTTPEnforcesExactCrossPurposeStepUpMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	dsn := filepath.Join(t.TempDir(), "content-proof.db") + "?_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Username: "content-proof-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "admin", TokenVersion: 1, TOTPEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTManager("FAKE_CONTENT_PROOF_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	secretProof, _, err := jwt.GenerateStepUpToken(user, auth.StepUpActionAssetSecretReveal, strings.Repeat("f", 32))
	if err != nil {
		t.Fatal(err)
	}
	downloadProof, _, err := jwt.GenerateStepUpToken(user, auth.StepUpActionAssetDownload)
	if err != nil {
		t.Fatal(err)
	}
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	tests := []struct {
		name       string
		body       string
		proof      string
		wantOK     bool
		wantAction auth.StepUpAction
	}{
		{name: "nonsecret preview no proof", body: `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`, wantOK: true},
		{name: "secret preview exact proof", body: `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`, proof: secretProof, wantOK: true, wantAction: auth.StepUpActionAssetSecretReveal},
		{name: "preview rejects download proof", body: `{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`, proof: downloadProof},
		{name: "download requires proof", body: `{"schema_version":1,"action":"download","renderer":"attachment","profile":"original_v1"}`},
		{name: "download rejects secret proof", body: `{"schema_version":1,"action":"download","renderer":"attachment","profile":"original_v1"}`, proof: secretProof},
		{name: "download exact proof", body: `{"schema_version":1,"action":"download","renderer":"attachment","profile":"original_v1"}`, proof: downloadProof, wantOK: true, wantAction: auth.StepUpActionAssetDownload},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &backupContentServiceFake{}
			isDownload := strings.Contains(testCase.body, `"action":"download"`)
			action, renderer, profile := content.DeliveryPreview, content.RendererSafeRaster, content.ProfileRasterV1
			if isDownload {
				action, renderer, profile = content.DeliveryDownload, content.RendererAttachment, content.ProfileOriginalV1
			}
			fake.ticket = backupContentHandlerTestTicket(t, action, renderer, profile)
			fake.ticket.Cookie.Secure = false
			handler := NewBackupContentHandler(fake, db, jwt, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{
					TicketTimeout: 5 * time.Second, AllowInsecurePrivateNetwork: true,
				}, nil
			}).WithSchemePolicy(policy)
			router := gin.New()
			router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
				c.Set(middleware.CtxUserID, user.ID)
				c.Set(middleware.CtxUsername, user.Username)
				c.Set(middleware.CtxRole, user.Role)
				c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
					JTI: strings.Repeat("f", 32), UserID: user.ID, Role: user.Role,
					TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
				})
				c.Next()
			}, handler.Issue)
			request := httptest.NewRequest(http.MethodPost,
				"http://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/delivery-tickets",
				strings.NewReader(testCase.body))
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
				if response.Code != http.StatusOK || len(fake.issueRequests) != 1 {
					t.Fatalf("status=%d calls=%d body=%s", response.Code, len(fake.issueRequests), response.Body.String())
				}
				if fake.issueRequests[0].SecureCookie {
					t.Fatal("private-network HTTP issue requested a Secure cookie")
				}
				proof := fake.issueRequests[0].Proof
				if testCase.wantAction == "" && proof != nil || testCase.wantAction != "" && (proof == nil || proof.Action != testCase.wantAction) {
					t.Fatalf("forwarded proof=%+v want=%q", proof, testCase.wantAction)
				}
			} else if response.Code != http.StatusForbidden || len(fake.issueRequests) != 0 {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, len(fake.issueRequests), response.Body.String())
			}
		})
	}
}

func TestBackupContentRejectedAttachedSecretProofReturnsTypedFreshRetryReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/delivery-tickets", nil)
	c.Request.Header.Set(StepUpHeaderName, "malformed-proof")
	c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
		JTI: stepUpTestSessionID, UserID: 42, Role: "admin", TokenVersion: 1,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	handler := NewBackupContentHandler(
		&backupContentServiceFake{},
		openStepUpHandlerTestDB(t),
		auth.NewJWTManager(stepUpTestJWTSecret, time.Hour),
		nil,
	)
	proof, ok := handler.deliveryProof(c, backupContentTicketPayload{Action: content.DeliveryPreview}, content.DeliveryActor{
		UserID: 42, Role: "admin",
	})
	if ok || proof != nil || response.Code != http.StatusForbidden {
		t.Fatalf("rejected proof ok=%v proof=%+v status=%d body=%s", ok, proof, response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Reason struct {
				Code   string            `json:"code"`
				Params map[string]string `json:"params"`
			} `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode rejected proof response: %v body=%s", err, response.Body.String())
	}
	if envelope.Data.Reason.Code != "secret_reveal_required" || envelope.Data.Reason.Params == nil ||
		len(envelope.Data.Reason.Params) != 0 || strings.Contains(response.Body.String(), "malformed-proof") {
		t.Fatalf("typed fresh-retry response=%+v body=%s", envelope, response.Body.String())
	}
}

func TestBackupContentIssueRejectsOperatorSecretRevealProof(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &backupContentServiceFake{}
	handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	})
	router := gin.New()
	router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(9))
		c.Set(middleware.CtxUsername, "content-proof-operator")
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: 9, Role: "operator",
			TokenVersion: 1, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		c.Next()
	}, handler.Issue)
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	request := httptest.NewRequest(http.MethodPost,
		"https://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/delivery-tickets",
		strings.NewReader(`{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(StepUpHeaderName, "opaque-proof")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(fake.issueRequests) != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, len(fake.issueRequests), response.Body.String())
	}
}

func TestBackupContentPrivateNetworkHTTPRecoveryResultDownloadTicketUsesExactResourceAndPurpose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	dsn := "file:" + handlerTestDBName(t) + "?mode=memory&cache=shared&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Username: "recovery-result-admin", PasswordHash: "FAKE_HASH", Role: "admin", TokenVersion: 2, TOTPEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTManager("FAKE_RECOVERY_RESULT_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	proof, _, err := jwt.GenerateStepUpToken(user, auth.StepUpActionRecoveryResultDownload)
	if err != nil {
		t.Fatal(err)
	}
	fake := &backupContentServiceFake{ticket: backupContentHandlerTestTicket(
		t, content.DeliveryDownload, content.RendererAttachment, content.ProfileOriginalV1,
	)}
	fake.ticket.Cookie.Secure = false
	handler := NewBackupContentHandler(fake, db, jwt, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{
			TicketTimeout: 5 * time.Second, AllowInsecurePrivateNetwork: true,
		}, nil
	}).WithSchemePolicy(policy)
	router := gin.New()
	router.POST("/api/v1/recovery-jobs/:id/results/:resultId/download-ticket", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Set(middleware.CtxUsername, user.Username)
		c.Set(middleware.CtxRole, user.Role)
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: user.ID, Role: user.Role,
			TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		c.Next()
	}, handler.IssueRecoveryResult)
	jobID, resultID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	request := httptest.NewRequest(http.MethodPost,
		"http://xirang.example/api/v1/recovery-jobs/"+jobID+"/results/"+resultID+"/download-ticket",
		strings.NewReader(`{"schema_version":1}`))
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-Proto", "http")
	request.Header.Set("X-Forwarded-For", "192.168.20.45")
	request.Header.Set(StepUpHeaderName, proof)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(fake.issueRequests) != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, len(fake.issueRequests), response.Body.String())
	}
	issued := fake.issueRequests[0]
	if issued.Resource.Kind != content.DeliveryResourceRecoveryResult || issued.Resource.RecoveryResult == nil ||
		issued.Resource.RecoveryResult.RecoveryJobID != jobID || issued.Resource.RecoveryResult.ResultID != resultID ||
		issued.Ref != (backupasset.AssetRef{}) || issued.Action != content.DeliveryDownload ||
		issued.Proof == nil || issued.Proof.Action != auth.StepUpActionRecoveryResultDownload || issued.SecureCookie {
		t.Fatalf("recovery issue request=%+v", issued)
	}
	aliased := httptest.NewRequest(http.MethodPost,
		"https://xirang.example/api/v1/recovery-jobs/%20"+jobID+"%20/results/%20"+resultID+"%20/download-ticket",
		strings.NewReader(`{"schema_version":1}`))
	aliased.Header.Set("Content-Type", "application/json")
	aliased.Header.Set(StepUpHeaderName, proof)
	aliasedResponse := httptest.NewRecorder()
	router.ServeHTTP(aliasedResponse, aliased)
	if aliasedResponse.Code != http.StatusBadRequest || len(fake.issueRequests) != 1 {
		t.Fatalf("whitespace-aliased Recovery result status=%d calls=%d body=%s",
			aliasedResponse.Code, len(fake.issueRequests), aliasedResponse.Body.String())
	}
	wrappedProof := httptest.NewRequest(http.MethodPost,
		"https://xirang.example/api/v1/recovery-jobs/"+jobID+"/results/"+resultID+"/download-ticket",
		strings.NewReader(`{"schema_version":1}`))
	wrappedProof.Header.Set("Content-Type", "application/json")
	wrappedProof.Header.Set(StepUpHeaderName, " "+proof+" ")
	wrappedProofResponse := httptest.NewRecorder()
	router.ServeHTTP(wrappedProofResponse, wrappedProof)
	if wrappedProofResponse.Code != http.StatusForbidden || len(fake.issueRequests) != 1 {
		t.Fatalf("whitespace-wrapped Recovery result proof status=%d calls=%d body=%s",
			wrappedProofResponse.Code, len(fake.issueRequests), wrappedProofResponse.Body.String())
	}
	invalidContentType := httptest.NewRequest(http.MethodPost,
		"https://xirang.example/api/v1/recovery-jobs/"+jobID+"/results/"+resultID+"/download-ticket",
		strings.NewReader(`{"schema_version":1}`))
	invalidContentType.Header.Set("Content-Type", "application/json; boundary=unsafe")
	invalidContentType.Header.Set(StepUpHeaderName, proof)
	invalidContentTypeResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidContentTypeResponse, invalidContentType)
	if invalidContentTypeResponse.Code != http.StatusBadRequest || len(fake.issueRequests) != 1 {
		t.Fatalf("invalid Recovery result content type status=%d calls=%d body=%s",
			invalidContentTypeResponse.Code, len(fake.issueRequests), invalidContentTypeResponse.Body.String())
	}
	insecure := httptest.NewRequest(http.MethodPost,
		"http://xirang.example/api/v1/recovery-jobs/"+jobID+"/results/"+resultID+"/download-ticket",
		strings.NewReader(`{"schema_version":1}`))
	insecure.RemoteAddr = "203.0.113.5:43210"
	insecure.Header.Set("Content-Type", "application/json")
	insecure.Header.Set(StepUpHeaderName, proof)
	insecureResponse := httptest.NewRecorder()
	router.ServeHTTP(insecureResponse, insecure)
	assertSecureTransportRequiredResponse(t, insecureResponse)
	if len(fake.issueRequests) != 1 {
		t.Fatalf("insecure Recovery result reached service calls=%d", len(fake.issueRequests))
	}
}

func TestBackupContentServeEnforcesBrowserBoundaryAndDelegatesCookieOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &backupContentServiceFake{}
	handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	})
	router := gin.New()
	router.GET("/api/v1/asset-content/:deliveryId", handler.Serve)
	deliveryID := strings.Repeat("d", 32)
	request := httptest.NewRequest(http.MethodGet, "https://xirang.example/api/v1/asset-content/"+deliveryID, nil)
	request.Header.Set("Cookie", content.DeliveryCookieName+"=v1."+strings.Repeat("A", 43))
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Origin", "https://xirang.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "safe-content" || len(fake.gatewayRequests) != 1 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, len(fake.gatewayRequests), response.Body.String())
	}
	served := fake.gatewayRequests[0]
	if served.DeliveryID != deliveryID || served.Method != http.MethodGet || served.RawCookie == "" ||
		len(served.RangeHeaders) != 0 || len(served.IfRangeHeaders) != 0 {
		t.Fatalf("gateway request=%+v", served)
	}
	for name, value := range map[string]string{
		"X-Content-Type-Options": "nosniff", "Cross-Origin-Resource-Policy": "same-origin",
		"Referrer-Policy": "no-referrer", "X-Frame-Options": "SAMEORIGIN", "Cache-Control": "private, no-store",
	} {
		if response.Header().Get(name) != value {
			t.Fatalf("%s=%q", name, response.Header().Get(name))
		}
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("content response inherited CORS: %v", response.Header())
	}
}

func TestBackupContentServeUsesTrustedProxySchemeForOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	fake := &backupContentServiceFake{}
	handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	}).WithSchemePolicy(policy)
	router := gin.New()
	router.GET("/api/v1/asset-content/:deliveryId", handler.Serve)
	deliveryID := strings.Repeat("d", 32)
	request := httptest.NewRequest(http.MethodGet, "http://xirang.example/api/v1/asset-content/"+deliveryID, nil)
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-For", "198.51.100.77")
	request.Header.Set("Cookie", content.DeliveryCookieName+"=v1."+strings.Repeat("A", 43))
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Origin", "https://xirang.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(fake.gatewayRequests) != 1 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, len(fake.gatewayRequests), response.Body.String())
	}
}

func TestBackupContentServeBlocksQueryAuthorizationAndCrossSiteBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deliveryID := strings.Repeat("d", 32)
	tests := []struct {
		name    string
		target  string
		headers map[string]string
	}{
		{name: "query", target: "/api/v1/asset-content/" + deliveryID + "?jwt=bad"},
		{name: "authorization", target: "/api/v1/asset-content/" + deliveryID, headers: map[string]string{"Authorization": "Bearer bad"}},
		{name: "cross site", target: "/api/v1/asset-content/" + deliveryID, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{name: "wrong origin", target: "/api/v1/asset-content/" + deliveryID, headers: map[string]string{"Origin": "https://evil.example"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &backupContentServiceFake{}
			handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
			})
			router := gin.New()
			router.GET("/api/v1/asset-content/:deliveryId", handler.Serve)
			request := httptest.NewRequest(http.MethodGet, "https://xirang.example"+testCase.target, nil)
			request.Header.Set("Cookie", content.DeliveryCookieName+"=v1."+strings.Repeat("A", 43))
			for name, value := range testCase.headers {
				request.Header.Set(name, value)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code < 400 || len(fake.gatewayRequests) != 0 || strings.Contains(response.Body.String(), "bad") {
				t.Fatalf("status=%d calls=%d body=%q", response.Code, len(fake.gatewayRequests), response.Body.String())
			}
		})
	}
}

func TestBackupContentSecureCookieAllowsOnlyExplicitLoopbackHTTP(t *testing.T) {
	httpsRequest := httptest.NewRequest(http.MethodPost, "https://xirang.example/ticket", nil)
	if secure, err := backupContentSecureCookie(httpsRequest, false); err != nil || !secure {
		t.Fatalf("HTTPS secure=%v err=%v", secure, err)
	}
	loopback := httptest.NewRequest(http.MethodPost, "http://localhost:10761/ticket", nil)
	loopback.RemoteAddr = "127.0.0.1:43210"
	if secure, err := backupContentSecureCookie(loopback, true); err != nil || secure {
		t.Fatalf("explicit loopback HTTP secure=%v err=%v", secure, err)
	}
	for _, mutate := range []func(*http.Request){
		func(request *http.Request) { request.RemoteAddr = "192.0.2.10:43210" },
		func(request *http.Request) { request.Host = "xirang.example" },
		func(request *http.Request) { request.Header.Set("Forwarded", "proto=http;host=localhost") },
	} {
		request := httptest.NewRequest(http.MethodPost, "http://localhost:10761/ticket", nil)
		request.RemoteAddr = "127.0.0.1:43210"
		mutate(request)
		if _, err := backupContentSecureCookie(request, true); err == nil {
			t.Fatalf("unsafe HTTP request accepted: host=%s remote=%s headers=%v", request.Host, request.RemoteAddr, request.Header)
		}
	}
	if _, err := backupContentSecureCookie(loopback, false); err == nil {
		t.Fatal("loopback HTTP accepted without explicit setting")
	}
}

func TestBackupContentSchemePolicyRequiresTrustedProxyForForwardedHTTPS(t *testing.T) {
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1", "10.0.0.0/8", "2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}

	directTLS := httptest.NewRequest(http.MethodPost, "https://xirang.example/ticket", nil)
	if secure, err := policy.SecureCookie(directTLS, BackupContentTransportOptions{}); err != nil || !secure {
		t.Fatalf("direct TLS secure=%v err=%v", secure, err)
	}

	tests := []struct {
		name   string
		remote string
		tls    bool
		xfp    []string
		xff    []string
		fwd    []string
		wantOK bool
	}{
		{name: "trusted IPv4", remote: "127.0.0.1:43210", xfp: []string{"https"}, xff: []string{"198.51.100.77"}, wantOK: true},
		{name: "trusted CIDR", remote: "10.23.4.5:43210", xfp: []string{"https"}, xff: []string{"198.51.100.77"}, wantOK: true},
		{name: "trusted IPv6 CIDR", remote: "[2001:db8::42]:43210", xfp: []string{"https"}, xff: []string{"198.51.100.77"}, wantOK: true},
		{name: "untrusted peer", remote: "192.0.2.10:43210", xfp: []string{"https"}},
		{name: "forwarded HTTPS without XFF", remote: "127.0.0.1:43210", xfp: []string{"https"}},
		{name: "missing forwarded proto", remote: "127.0.0.1:43210"},
		{name: "empty forwarded proto", remote: "127.0.0.1:43210", xfp: []string{""}},
		{name: "multiple forwarded proto", remote: "127.0.0.1:43210", xfp: []string{"https", "https"}},
		{name: "comma forwarded proto", remote: "127.0.0.1:43210", xfp: []string{"https,http"}},
		{name: "noncanonical case", remote: "127.0.0.1:43210", xfp: []string{"HTTPS"}},
		{name: "ambiguous Forwarded", remote: "127.0.0.1:43210", xfp: []string{"https"}, fwd: []string{"proto=https"}},
		{name: "TLS contradiction", remote: "127.0.0.1:43210", tls: true, xfp: []string{"http"}},
		{name: "TLS untrusted forwarded header", remote: "192.0.2.10:43210", tls: true, xfp: []string{"https"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			scheme := "http"
			if testCase.tls {
				scheme = "https"
			}
			request := httptest.NewRequest(http.MethodPost, scheme+"://xirang.example/ticket", nil)
			request.RemoteAddr = testCase.remote
			for _, value := range testCase.xfp {
				request.Header.Add("X-Forwarded-Proto", value)
			}
			for _, value := range testCase.xff {
				request.Header.Add("X-Forwarded-For", value)
			}
			for _, value := range testCase.fwd {
				request.Header.Add("Forwarded", value)
			}
			secure, err := policy.SecureCookie(request, BackupContentTransportOptions{})
			if testCase.wantOK {
				if err != nil || !secure {
					t.Fatalf("secure=%v err=%v", secure, err)
				}
				return
			}
			if err == nil || secure {
				t.Fatalf("unsafe scheme accepted: secure=%v remote=%q headers=%v", secure, request.RemoteAddr, request.Header)
			}
		})
	}

	if _, err := NewBackupContentSchemePolicy([]string{"127.0.0.1", "not-a-cidr"}); err == nil {
		t.Fatal("invalid trusted proxy entry accepted")
	}
}

func TestBackupContentSchemePolicyPrivateNetworkMatrix(t *testing.T) {
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	allowPrivate := BackupContentTransportOptions{AllowInsecurePrivateNetwork: true}
	tooManyHops := strings.TrimSuffix(strings.Repeat("192.168.1.2,", 17), ",")
	overLimitXFF := "192.168.1.2" + strings.Repeat(" ", maxBackupContentForwardedForBytes)
	overLimitXFP := "http" + strings.Repeat(" ", 1024)
	tests := []struct {
		name       string
		target     string
		remote     string
		xfp        []string
		xff        []string
		forwarded  string
		options    BackupContentTransportOptions
		wantOK     bool
		wantSecure bool
	}{
		{name: "direct TLS", target: "https://xirang.example/ticket", remote: "203.0.113.5:4000", wantOK: true, wantSecure: true},
		{name: "trusted proxy HTTPS", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"https"}, xff: []string{"198.51.100.77"}, wantOK: true, wantSecure: true},
		{name: "direct RFC1918", target: "http://xirang.example/ticket", remote: "192.168.4.5:4000", options: allowPrivate, wantOK: true},
		{name: "direct ULA", target: "http://xirang.example/ticket", remote: "[fd12:3456::5]:4000", options: allowPrivate, wantOK: true},
		{name: "direct loopback", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", options: allowPrivate, wantOK: true},
		{name: "trusted proxy RFC1918", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{"192.168.4.5"}, options: allowPrivate, wantOK: true},
		{name: "trusted proxy ULA", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{"fd12:3456::5"}, options: allowPrivate, wantOK: true},
		{name: "public IPv4", target: "http://xirang.example/ticket", remote: "203.0.113.5:4000", options: allowPrivate},
		{name: "public IPv6", target: "http://xirang.example/ticket", remote: "[2001:db8::5]:4000", options: allowPrivate},
		{name: "CGNAT", target: "http://xirang.example/ticket", remote: "100.64.0.5:4000", options: allowPrivate},
		{name: "link local IPv4", target: "http://xirang.example/ticket", remote: "169.254.1.5:4000", options: allowPrivate},
		{name: "link local IPv6", target: "http://xirang.example/ticket", remote: "[fe80::5]:4000", options: allowPrivate},
		{name: "unspecified", target: "http://xirang.example/ticket", remote: "0.0.0.0:4000", options: allowPrivate},
		{name: "multicast", target: "http://xirang.example/ticket", remote: "224.0.0.5:4000", options: allowPrivate},
		{name: "zone qualified", target: "http://xirang.example/ticket", remote: "[fd00::5%eth0]:4000", options: allowPrivate},
		{name: "malformed bracketed peer", target: "http://xirang.example/ticket", remote: "[[192.168.4.5]]", options: allowPrivate},
		{name: "forwarded HTTP without XFF", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, options: allowPrivate},
		{name: "spoofed leftmost private", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{"192.168.1.2, 203.0.113.5"}, options: allowPrivate},
		{name: "all trusted no client", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{"127.0.0.1"}, options: allowPrivate},
		{name: "malformed XFF", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{"192.168.1.2, bad"}, options: allowPrivate},
		{name: "multiple XFF headers", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{"192.168.1.2", "192.168.1.3"}, options: allowPrivate},
		{name: "too many XFF hops", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{tooManyHops}, options: allowPrivate},
		{name: "empty XFF item", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{"192.168.1.2,"}, options: allowPrivate},
		{name: "over-limit XFF", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http"}, xff: []string{overLimitXFF}, options: allowPrivate},
		{name: "over-limit XFP", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{overLimitXFP}, xff: []string{"192.168.1.2"}, options: allowPrivate},
		{name: "untrusted proxy headers", target: "http://xirang.example/ticket", remote: "203.0.113.5:4000", xfp: []string{"http"}, xff: []string{"192.168.1.2"}, options: allowPrivate},
		{name: "compound XFP", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http,https"}, xff: []string{"192.168.1.2"}, options: allowPrivate},
		{name: "duplicate XFP", target: "http://xirang.example/ticket", remote: "127.0.0.1:4000", xfp: []string{"http", "http"}, xff: []string{"192.168.1.2"}, options: allowPrivate},
		{name: "Forwarded is rejected", target: "http://xirang.example/ticket", remote: "192.168.1.2:4000", forwarded: "for=192.168.1.2;proto=http", options: allowPrivate},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, testCase.target, nil)
			request.RemoteAddr = testCase.remote
			for _, value := range testCase.xfp {
				request.Header.Add("X-Forwarded-Proto", value)
			}
			for _, value := range testCase.xff {
				request.Header.Add("X-Forwarded-For", value)
			}
			if testCase.forwarded != "" {
				request.Header.Set("Forwarded", testCase.forwarded)
			}
			secure, secureErr := policy.SecureCookie(request, testCase.options)
			if testCase.wantOK {
				if secureErr != nil || secure != testCase.wantSecure {
					t.Fatalf("secure=%v err=%v", secure, secureErr)
				}
				return
			}
			if secureErr == nil {
				t.Fatalf("request unexpectedly accepted with secure=%v", secure)
			}
		})
	}
}

func TestBackupContentServeRechecksPrivateNetworkSettingBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	allowPrivate := true
	fake := &backupContentServiceFake{}
	handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{
			TicketTimeout: 5 * time.Second, AllowInsecurePrivateNetwork: allowPrivate,
		}, nil
	}).WithSchemePolicy(policy)
	router := gin.New()
	router.GET("/api/v1/asset-content/:deliveryId", handler.Serve)
	deliveryID := strings.Repeat("d", 32)
	issueRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodGet, "http://xirang.example/api/v1/asset-content/"+deliveryID, nil)
		request.RemoteAddr = "127.0.0.1:43210"
		request.Header.Set("X-Forwarded-Proto", "http")
		request.Header.Set("X-Forwarded-For", "192.168.20.45")
		request.Header.Set("Origin", "http://xirang.example")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.Header.Set("Cookie", content.DeliveryCookieName+"=v1."+strings.Repeat("A", 43))
		return request
	}

	allowed := httptest.NewRecorder()
	router.ServeHTTP(allowed, issueRequest())
	if allowed.Code != http.StatusOK || len(fake.gatewayRequests) != 1 {
		t.Fatalf("enabled status=%d calls=%d", allowed.Code, len(fake.gatewayRequests))
	}
	allowPrivate = false
	denied := httptest.NewRecorder()
	router.ServeHTTP(denied, issueRequest())
	if denied.Code != http.StatusServiceUnavailable || denied.Body.Len() != 0 || len(fake.gatewayRequests) != 1 {
		t.Fatalf("disabled status=%d body=%q calls=%d", denied.Code, denied.Body.String(), len(fake.gatewayRequests))
	}
}

func TestBackupContentIssueRejectsForwardedHTTPSFromUntrustedPeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	fake := &backupContentServiceFake{}
	handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	}).WithSchemePolicy(policy)
	now := time.Now().UTC()
	router := gin.New()
	router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "operator")
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 3,
			ExpiresAt: now.Add(time.Hour),
		})
		c.Next()
	}, handler.Issue)
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	request := httptest.NewRequest(http.MethodPost,
		"http://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/delivery-tickets",
		strings.NewReader(`{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`))
	request.RemoteAddr = "192.0.2.10:43210"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || len(fake.issueRequests) != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, len(fake.issueRequests), response.Body.String())
	}
	assertSecureTransportRequiredResponse(t, response)
}

func TestBackupContentIssueAllowsPrivateNetworkHTTPThroughTrustedProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	fake := &backupContentServiceFake{}
	fake.ticket = backupContentHandlerTestTicket(
		t, content.DeliveryPreview, content.RendererSafeRaster, content.ProfileRasterV1,
	)
	fake.ticket.Cookie.Secure = false
	handler := NewBackupContentHandler(fake, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{
			TicketTimeout: 5 * time.Second, AllowInsecurePrivateNetwork: true,
		}, nil
	}).WithSchemePolicy(policy)
	now := time.Now().UTC()
	router := gin.New()
	router.POST("/api/v1/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "operator")
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: 42, Role: "operator", TokenVersion: 3,
			ExpiresAt: now.Add(time.Hour),
		})
		c.Next()
	}, handler.Issue)
	pointID, entryID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	request := httptest.NewRequest(http.MethodPost,
		"http://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/delivery-tickets",
		strings.NewReader(`{"schema_version":1,"action":"preview","renderer":"safe_raster","profile":"raster_v1"}`))
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-Proto", "http")
	request.Header.Set("X-Forwarded-For", "192.168.20.45")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || len(fake.issueRequests) != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, len(fake.issueRequests), response.Body.String())
	}
	if fake.issueRequests[0].SecureCookie {
		t.Fatal("private-network HTTP issue requested a Secure cookie")
	}
	setCookie := response.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "HttpOnly") || !strings.Contains(setCookie, "SameSite=Strict") ||
		strings.Contains(setCookie, "Secure") || !strings.Contains(setCookie, "Path="+fake.ticket.Cookie.Path) {
		t.Fatalf("Set-Cookie=%q", setCookie)
	}
}

type backupContentServiceFake struct {
	ticket          content.IssuedTicket
	issueErr        error
	serveErr        error
	issueRequests   []content.IssueRequest
	gatewayRequests []content.GatewayRequest
}

func backupContentHandlerTestTicket(
	t *testing.T,
	action content.DeliveryAction,
	renderer content.Renderer,
	profile content.RendererProfile,
) content.IssuedTicket {
	t.Helper()
	now := time.Now().UTC()
	deliveryID := strings.Repeat("d", 32)
	cookie, err := content.NewDeliveryCookie(deliveryID, "v1."+strings.Repeat("A", 43), now.Add(time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	mediaType := "image/png"
	rangePolicy := content.RangeSingle
	if action == content.DeliveryDownload {
		mediaType = "application/octet-stream"
	} else if renderer == content.RendererEscapedText || renderer == content.RendererPlainText || renderer == content.RendererMetadataHex {
		mediaType = "text/plain; charset=utf-8"
		rangePolicy = content.RangeNone
	}
	return content.IssuedTicket{
		Descriptor: content.TicketDescriptor{
			SchemaVersion: 1, ContentURL: cookie.Path, Action: action, Renderer: renderer, Profile: profile,
			ContentType: mediaType, ContentLength: 1, ETag: `"test"`, Range: rangePolicy,
			Classification: content.ClassificationNonSecret, ExpiresAt: cookie.Expires,
			IdleExpiresAt: now.Add(30 * time.Second), FallbackActions: []content.DeliveryAction{},
		},
		Cookie: cookie,
	}
}

func (fake *backupContentServiceFake) Issue(_ context.Context, request content.IssueRequest) (content.IssuedTicket, error) {
	fake.issueRequests = append(fake.issueRequests, request)
	return fake.ticket, fake.issueErr
}

func (fake *backupContentServiceFake) Serve(_ context.Context, request content.GatewayRequest, writer http.ResponseWriter) error {
	fake.gatewayRequests = append(fake.gatewayRequests, request)
	if fake.serveErr != nil {
		return fake.serveErr
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("safe-content"))
	return nil
}

func (*backupContentServiceFake) RevokeSession(context.Context, string, string) error { return nil }

func assertSecureTransportRequiredResponse(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Reason struct {
				Code   string            `json:"code"`
				Params map[string]string `json:"params"`
			} `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode secure transport response: %v body=%s", err, response.Body.String())
	}
	if envelope.Code != http.StatusServiceUnavailable || envelope.Data.Reason.Code != "secure_transport_required" ||
		envelope.Data.Reason.Params == nil || len(envelope.Data.Reason.Params) != 0 {
		t.Fatalf("secure transport response=%+v body=%s", envelope, response.Body.String())
	}
}

var _ BackupContentService = (*backupContentServiceFake)(nil)
