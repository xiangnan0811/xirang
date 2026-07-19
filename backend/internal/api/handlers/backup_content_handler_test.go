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
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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
		strings.Contains(string(encoded), "Cookie") || strings.Contains(string(encoded), "Grant") {
		t.Fatalf("ticket envelope=%s", encoded)
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

func TestBackupContentIssueEnforcesExactCrossPurposeStepUpMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_loc=UTC"
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
	secretProof, _, err := jwt.GenerateStepUpToken(user, auth.StepUpActionAssetSecretReveal)
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
			handler := NewBackupContentHandler(fake, db, jwt, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
			})
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
				"https://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/delivery-tickets",
				strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			if testCase.proof != "" {
				request.Header.Set(StepUpHeaderName, testCase.proof)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if testCase.wantOK {
				if response.Code != http.StatusOK || len(fake.issueRequests) != 1 {
					t.Fatalf("status=%d calls=%d body=%s", response.Code, len(fake.issueRequests), response.Body.String())
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
	if action == content.DeliveryDownload {
		mediaType = "application/octet-stream"
	}
	return content.IssuedTicket{
		Descriptor: content.TicketDescriptor{
			SchemaVersion: 1, ContentURL: cookie.Path, Action: action, Renderer: renderer, Profile: profile,
			ContentType: mediaType, ContentLength: 1, ETag: `"test"`, Range: content.RangeSingle,
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

var _ BackupContentService = (*backupContentServiceFake)(nil)
