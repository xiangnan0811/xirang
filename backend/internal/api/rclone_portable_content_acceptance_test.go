package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"github.com/pquerna/otp/totp"
)

const portableHTTPCompletionHeader = "X-Xirang-Test-Content-Request"

type portableHTTPContentCompletion struct {
	method string
	uri    string
	done   chan struct{}
}

type portableHTTPContentCompletions struct {
	mu      sync.Mutex
	next    uint64
	pending map[string]portableHTTPContentCompletion
}

func (completions *portableHTTPContentCompletions) track(request *http.Request) (<-chan struct{}, func()) {
	completions.mu.Lock()
	completions.next++
	id := strconv.FormatUint(completions.next, 10)
	done := make(chan struct{})
	completions.pending[id] = portableHTTPContentCompletion{method: request.Method, uri: request.URL.RequestURI(), done: done}
	completions.mu.Unlock()
	request.Header.Set(portableHTTPCompletionHeader, id)
	return done, func() {
		completions.mu.Lock()
		delete(completions.pending, id)
		completions.mu.Unlock()
	}
}

func (completions *portableHTTPContentCompletions) finish(request *http.Request) {
	id := request.Header.Get(portableHTTPCompletionHeader)
	if id == "" {
		return
	}
	completions.mu.Lock()
	waiter, ok := completions.pending[id]
	if ok && waiter.method == request.Method && waiter.uri == request.URL.RequestURI() {
		delete(completions.pending, id)
	} else {
		ok = false
	}
	completions.mu.Unlock()
	if ok {
		close(waiter.done)
	}
}

func readPortableHTTPResponseBody(response *http.Response, maxBytes int64) (body []byte, readErr, closeErr error) {
	defer func() { closeErr = response.Body.Close() }()
	body, readErr = io.ReadAll(io.LimitReader(response.Body, maxBytes))
	return
}

// Wire the same runtime content service and live handler configuration as the
// production router. A real TLS server also provides the write-deadline support
// required by the content gateway.
func (fixture *rcloneHTTPFixture) newPortableHTTPContentServer() (*httptest.Server, *portableHTTPContentCompletions) {
	fixture.t.Helper()
	runtime := fixture.runtime
	config, err := runtime.ContentConfig()
	if err != nil || !config.Enabled || config.TicketTimeout != 20*time.Second || runtime.ContentBroker() == nil || runtime.ContentService() == nil {
		fixture.t.Fatal("production Content runtime or its default 20s ticket budget is unavailable")
	}
	if live, err := runtime.FeatureLive(); err != nil || !live {
		fixture.t.Fatal("production Content feature readiness is unavailable")
	}
	router := NewRouter(Dependencies{
		DB: fixture.db, JWTManager: fixture.jwt, SettingsService: fixture.setting, BackupAssets: runtime,
		BackupContent: runtime.ContentService(),
		BackupContentConfig: func(context.Context) (handlers.BackupContentHandlerConfig, error) {
			config, err := runtime.ContentConfig()
			if err != nil {
				return handlers.BackupContentHandlerConfig{}, err
			}
			return handlers.BackupContentHandlerConfig{
				TicketTimeout: config.TicketTimeout, AllowInsecureLoopback: config.AllowInsecureLoopback,
				AllowInsecurePrivateNetwork: config.AllowInsecurePrivateNetwork,
			}, nil
		},
	})
	completions := &portableHTTPContentCompletions{pending: make(map[string]portableHTTPContentCompletion)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer completions.finish(request)
		router.ServeHTTP(writer, request)
	}))
	return server, completions
}

func (fixture *rcloneHTTPFixture) assertPortableHTTPBrokerContent(
	ctx context.Context, server *httptest.Server, completions *portableHTTPContentCompletions, trace *rcloneHTTPAcceptanceTransport,
	stage string, point model.RecoveryPoint, generation model.CatalogGeneration, entry model.CatalogEntry, expected []byte,
) {
	fixture.t.Helper()
	if generation.ID != entry.GenerationID || point.ID != entry.RecoveryPointID ||
		entry.ModifiedAt == nil || entry.Size != int64(len(expected)) {
		fixture.t.Fatalf("%s Content acceptance did not receive the produced Catalog entry", stage)
	}
	token, err := fixture.jwt.GenerateToken(fixture.admin)
	if err != nil {
		fixture.t.Fatalf("%s Content acceptance could not create a fixture session", stage)
	}
	claims, err := fixture.jwt.ParseToken(token)
	if err != nil || claims == nil || claims.UserID != fixture.admin.ID || backupasset.ValidateOpaqueID(claims.ID) != nil {
		fixture.t.Fatalf("%s Content acceptance could not validate the fixture session", stage)
	}
	issuePath := server.URL + "/api/v1/recovery-points/" + point.ID + "/entries/" + entry.EntryID + "/delivery-tickets"
	issue := func(proof string) (*http.Response, []byte) {
		fixture.t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuePath,
			strings.NewReader(`{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1"}`))
		if err != nil {
			fixture.t.Fatalf("%s Content ticket request could not be created", stage)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		if proof != "" {
			request.Header.Set(handlers.StepUpHeaderName, proof)
		}
		startIndex := len(trace.snapshot())
		started := time.Now()
		fixture.t.Logf("%s Content ticket started with the production 20s budget", stage)
		response, err := server.Client().Do(request)
		if err != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			fixture.logPortableHTTPContentTrace(trace, startIndex, started, stage+" ticket")
			fixture.t.Fatalf("%s Content ticket transport failed", stage)
		}
		body, readErr, closeErr := readPortableHTTPResponseBody(response, 64<<10)
		fixture.logPortableHTTPContentTrace(trace, startIndex, started, stage+" ticket")
		if readErr != nil || closeErr != nil || response.TLS == nil {
			fixture.t.Fatalf("%s Content ticket TLS response could not be read", stage)
		}
		return response, body
	}

	response, body := issue("")
	proofUsed := false
	if response.StatusCode == http.StatusForbidden && portableHTTPContentReason(body) == "secret_reveal_required" {
		// The actual classifier can require a session-bound proof. Obtain it from
		// the real authenticated step-up route using a fixture-owned TOTP factor.
		proof := fixture.portableHTTPSecretRevealProof(ctx, server, token)
		response, body = issue(proof)
		proofUsed = true
	}
	if response.StatusCode != http.StatusOK {
		availability := "unavailable"
		var current model.RecoveryPoint
		if err := fixture.db.Select("physical_availability").First(&current, "id = ?", point.ID).Error; err == nil {
			switch backupasset.PhysicalAvailability(current.PhysicalAvailability) {
			case backupasset.PhysicalOnline, backupasset.PhysicalOffline, backupasset.PhysicalMissing, backupasset.PhysicalUnknown:
				availability = current.PhysicalAvailability
			}
		}
		fixture.t.Fatalf("%s Content ticket failed: status=%d reason=%s point_availability=%s", stage, response.StatusCode, portableHTTPContentReason(body), availability)
	}
	var envelope struct {
		Data content.TicketDescriptor `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		fixture.t.Fatalf("%s Content ticket descriptor could not be decoded", stage)
	}
	descriptor := envelope.Data
	const gatewayPrefix = "/api/v1/asset-content/"
	deliveryID := strings.TrimPrefix(descriptor.ContentURL, gatewayPrefix)
	if !strings.HasPrefix(descriptor.ContentURL, gatewayPrefix) || backupasset.ValidateOpaqueID(deliveryID) != nil ||
		descriptor.SchemaVersion != 1 || descriptor.Action != content.DeliveryPreview ||
		descriptor.Renderer != content.RendererPlainText || descriptor.Profile != content.ProfileTextV2 ||
		descriptor.Range != content.RangeNone || descriptor.Truncated || descriptor.ContentLength != int64(len(expected)) ||
		descriptor.LastModified == nil || !descriptor.LastModified.Equal(*entry.ModifiedAt) ||
		(proofUsed && descriptor.Classification == content.ClassificationNonSecret) ||
		(!proofUsed && descriptor.Classification != content.ClassificationNonSecret) {
		fixture.t.Fatalf("%s Content ticket did not describe the exact source preview", stage)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != content.DeliveryCookieName || cookies[0].Value == "" ||
		!cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode ||
		cookies[0].Path != descriptor.ContentURL {
		fixture.t.Fatalf("%s Content ticket did not return the exact secure gateway cookie", stage)
	}
	var grant model.BackupAssetDeliveryGrant
	if err := fixture.db.Where("delivery_id = ?", deliveryID).First(&grant).Error; err != nil ||
		grant.ResourceKind != string(content.DeliveryResourceBackupAsset) || grant.RecoveryPointID == nil || *grant.RecoveryPointID != point.ID ||
		grant.CatalogGenerationID == nil || *grant.CatalogGenerationID != generation.ID || grant.EntryID == nil || *grant.EntryID != entry.EntryID ||
		grant.OwnerUserID != fixture.admin.ID || grant.SessionJTI != claims.ID || grant.ProviderKind != string(backupasset.ProviderRclone) ||
		grant.SourceFingerprint != point.SourceFingerprint || grant.EntryFingerprint != entry.Fingerprint ||
		grant.SourceModifiedAt == nil || !grant.SourceModifiedAt.Equal(*entry.ModifiedAt) {
		fixture.t.Fatalf("%s Content ticket was not bound to the authenticated session and produced Catalog entry", stage)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+descriptor.ContentURL, nil)
	if err != nil {
		fixture.t.Fatalf("%s raw Content request could not be created", stage)
	}
	request.AddCookie(cookies[0])
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	finished, forget := completions.track(request)
	defer forget()
	startIndex := len(trace.snapshot())
	started := time.Now()
	fixture.t.Logf("%s raw Content Serve started", stage)
	servedResponse, err := server.Client().Do(request)
	if err != nil {
		if servedResponse != nil && servedResponse.Body != nil {
			_ = servedResponse.Body.Close()
		}
		fixture.logPortableHTTPContentTrace(trace, startIndex, started, stage+" serve")
		fixture.t.Fatalf("%s raw Content transport failed", stage)
	}
	payload, readErr, closeErr := readPortableHTTPResponseBody(servedResponse, int64(len(expected))+1)
	select {
	case <-finished:
	case <-ctx.Done():
		fixture.t.Fatalf("%s raw Content handler did not finish", stage)
	}
	fixture.logPortableHTTPContentTrace(trace, startIndex, started, stage+" serve")
	if servedResponse.TLS == nil || servedResponse.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || !bytes.Equal(payload, expected) {
		fixture.t.Fatalf("%s raw Content did not serve the exact bytes: status=%d bytes=%d", stage, servedResponse.StatusCode, len(payload))
	}
	// A response can be written before the source is closed. The persisted
	// terminal state proves the broker also completed its close verification.
	var requests []model.BackupAssetDeliveryRequest
	if err := fixture.db.Where("grant_id = ?", grant.ID).Find(&requests).Error; err != nil || len(requests) != 1 ||
		requests[0].State != string(content.RequestSucceeded) || requests[0].HTTPStatus != http.StatusOK ||
		requests[0].ResponseBytes != int64(len(expected)) || requests[0].FinishedAt == nil || requests[0].FailureCode != "" {
		fixture.t.Fatalf("%s raw Content did not complete broker source verification", stage)
	}
	// End this isolated login while the lease is still live, before later
	// runtime restarts and the application clock advance.
	logout, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/auth/logout", nil)
	if err != nil {
		fixture.t.Fatalf("%s Content session logout could not be created", stage)
	}
	logout.Header.Set("Authorization", "Bearer "+token)
	logoutResponse, err := server.Client().Do(logout)
	if err != nil {
		if logoutResponse != nil && logoutResponse.Body != nil {
			_ = logoutResponse.Body.Close()
		}
		fixture.t.Fatalf("%s Content session logout failed", stage)
	}
	_, readErr, closeErr = readPortableHTTPResponseBody(logoutResponse, 64<<10)
	if logoutResponse.TLS == nil || logoutResponse.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
		fixture.t.Fatalf("%s Content session logout failed: status=%d", stage, logoutResponse.StatusCode)
	}
	var lease model.RecoveryPointLease
	if err := fixture.db.First(&lease, "id = ?", grant.LeaseID).Error; err != nil || lease.Status != string(backupasset.LeaseReleased) {
		fixture.t.Fatalf("%s Content session did not release its lease", stage)
	}
}

func (fixture *rcloneHTTPFixture) portableHTTPSecretRevealProof(ctx context.Context, server *httptest.Server, token string) string {
	fixture.t.Helper()
	key, err := auth.GenerateTOTPSecret("FAKE_XIRANG_ISSUER_FOR_TEST_ONLY", fixture.admin.Username)
	if err != nil {
		fixture.t.Fatal("generate isolated Content step-up factor")
	}
	var user model.User
	if err := fixture.db.First(&user, fixture.admin.ID).Error; err != nil {
		fixture.t.Fatal("load isolated Content step-up user")
	}
	user.TOTPEnabled, user.TOTPSecret = true, key.Secret()
	if err := fixture.db.Save(&user).Error; err != nil {
		fixture.t.Fatal("save isolated Content step-up factor")
	}
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		fixture.t.Fatal("generate isolated Content step-up code")
	}
	payload, err := json.Marshal(map[string]any{"code": code, "step_up_action": auth.StepUpActionAssetSecretReveal})
	if err != nil {
		fixture.t.Fatal("encode isolated Content step-up request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/auth/step-up", bytes.NewReader(payload))
	if err != nil {
		fixture.t.Fatal("create isolated Content step-up request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		fixture.t.Fatal("isolated Content step-up transport failed")
	}
	body, readErr, closeErr := readPortableHTTPResponseBody(response, 64<<10)
	var envelope struct {
		Data struct {
			Proof string `json:"proof"`
		} `json:"data"`
	}
	if response.TLS == nil || response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil ||
		json.Unmarshal(body, &envelope) != nil || envelope.Data.Proof == "" {
		fixture.t.Fatalf("isolated Content step-up failed: status=%d", response.StatusCode)
	}
	return envelope.Data.Proof
}

func portableHTTPContentReason(body []byte) string {
	var envelope struct {
		Data struct {
			Reason backupasset.CapabilityReason `json:"reason"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return "unclassified"
	}
	if envelope.Data.Reason.Params == nil || len(envelope.Data.Reason.Params) != 0 {
		return "unclassified"
	}
	switch string(envelope.Data.Reason.Code) {
	case "secret_reveal_required", "secure_transport_required", "preview_renderer_unsupported",
		"preview_source_open_failed", "preview_source_read_failed", "preview_source_changed",
		"preview_source_timeout", "preview_source_canceled", "preview_source_capability":
		return string(envelope.Data.Reason.Code)
	default:
		if backupasset.ValidateCapabilityReason(envelope.Data.Reason) == nil {
			return string(envelope.Data.Reason.Code)
		}
		return "unclassified"
	}
}

// These timings expose only closed operation names, durations, and success.
// Open timings cover setup; the total includes stream consumption and close.
func (fixture *rcloneHTTPFixture) logPortableHTTPContentTrace(trace *rcloneHTTPAcceptanceTransport, from int, started time.Time, stage string) {
	fixture.t.Helper()
	events := trace.snapshot()
	fixture.t.Logf("%s finished: elapsed=%s commands=%d", stage, time.Since(started).Round(time.Millisecond), len(events)-from)
	for _, event := range events[from:] {
		operation := "other"
		switch event.operation {
		case provider.OperationRcloneManagedCat, provider.OperationRcloneManagedRecursiveList,
			provider.OperationRcloneManagedExactStat, provider.OperationRcloneCat,
			provider.OperationRcloneStat, provider.OperationRcloneList:
			operation = string(event.operation)
		}
		fixture.t.Logf("%s command=%s start=%s setup=%s succeeded=%t", stage, operation,
			event.started.Sub(started).Round(time.Millisecond), event.elapsed.Round(time.Millisecond), event.succeeded)
	}
}
