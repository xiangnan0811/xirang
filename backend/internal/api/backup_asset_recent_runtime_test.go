package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"

	"github.com/pquerna/otp/totp"
)

const recentRuntimePayload = "Welcome to the Recent access investigation.\n"
const recentRuntimeOtherPayload = "A second real catalog entry.\n"
const recentRuntimeDownload = `{"schema_version":1,"action":"download","renderer":"attachment","profile":"original_v1"}`

type recentRuntimeHTTPFixture struct {
	t           *testing.T
	base        *rcloneHTTPFixture
	server      *httptest.Server
	completions *portableHTTPContentCompletions
	refs        map[string]backupasset.AssetRef
}

type recentRuntimeResponse struct {
	status  int
	body    []byte
	cookies []*http.Cookie
	err     error
}

type recentRuntimeTicket struct {
	url     string
	cookie  *http.Cookie
	grantID string
}

// Build both entries from the real local Rsync repository and Catalog indexer.
// Limits are applied before restarting the production runtime, which snapshots
// overlay configuration when it constructs the service.
func newRecentRuntimeHTTPFixture(t *testing.T, limits map[string]string) *recentRuntimeHTTPFixture {
	t.Helper()
	base := newRcloneHTTPFixture(t, "", nil)
	if len(limits) != 0 {
		for key, value := range limits {
			if err := base.setting.Update(key, value); err != nil {
				t.Fatalf("set isolated runtime limit %s: %v", key, err)
			}
		}
		base.restart()
	}

	root := t.TempDir()
	for name, payload := range map[string]string{"readme.txt": recentRuntimePayload, "other.txt": recentRuntimeOtherPayload} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(payload), 0600); err != nil {
			t.Fatal("write isolated Rsync source")
		}
	}
	node := model.Node{Name: "recent-local", Host: "localhost", Port: 22, Username: "recent", AuthType: "password", Password: "FAKE_RECENT_FIXTURE_FOR_TEST_ONLY", BasePath: "/data", BackupDir: "recent"}
	if err := base.db.Create(&node).Error; err != nil {
		t.Fatal("create isolated node")
	}
	task := model.Task{Name: "recent-local", NodeID: node.ID, ExecutorType: "rsync", RsyncSource: root, RsyncTarget: root, Status: "pending", Enabled: true}
	if err := base.db.Create(&task).Error; err != nil {
		t.Fatal("create isolated Rsync task")
	}
	ctx := context.Background()
	connected, err := base.runtime.RepositoryService().Connect(ctx, repository.ConnectRequest{TaskID: task.ID}, repository.RequestContext{CorrelationID: "recent-runtime-test"})
	if err != nil || connected.MutablePoint == nil {
		t.Fatal("connect isolated local Rsync repository")
	}
	foundation := base.runtime.FoundationService()
	leaseConfig, err := foundation.LeaseConfig()
	if err != nil {
		t.Fatal("load lease configuration")
	}
	catalogConfig, err := foundation.CatalogConfig()
	if err != nil {
		t.Fatal("load Catalog configuration")
	}
	lease, err := backupasset.NewLeaseService(base.db, base.clock.Now, leaseConfig)
	if err != nil {
		t.Fatal("construct Catalog lease service")
	}
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: base.db, Factory: base.runtime.RepositoryService(), Lease: lease,
		IdentityKeys: backupasset.NewKeyring(base.db, base.clock.Now), Now: base.clock.Now,
		Config: catalog.IndexerConfig{BatchSize: catalogConfig.BatchSize, BuildTimeout: catalogConfig.BuildTimeout, MaxEntries: catalogConfig.MaxEntries, HeartbeatInterval: catalogConfig.Lease.Heartbeat},
	})
	if err != nil {
		t.Fatal("construct production Catalog indexer")
	}
	generation, err := indexer.Build(ctx, catalog.BuildRequest{RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID, CorrelationID: "recent-runtime-test"})
	if err != nil || !generation.IsActive || generation.State != string(catalog.GenerationComplete) {
		t.Fatal("build production Catalog from local Rsync")
	}
	page, err := base.runtime.CatalogService().ListEntries(ctx, connected.MutablePoint.ID, catalog.AuthorizationScope{Role: "admin", UserID: base.admin.ID}, catalog.EntryListRequest{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatal("read both produced Catalog entries")
	}
	refs := make(map[string]backupasset.AssetRef, len(page.Items))
	for _, entry := range page.Items {
		refs[entry.Name] = backupasset.AssetRef{RecoveryPointID: connected.MutablePoint.ID, EntryID: entry.EntryID}
	}
	if backupasset.ValidateAssetRef(refs["readme.txt"]) != nil || backupasset.ValidateAssetRef(refs["other.txt"]) != nil {
		t.Fatal("produced Catalog did not contain the expected files")
	}
	server, completions := base.newPortableHTTPContentServer()
	t.Cleanup(server.Close)
	return &recentRuntimeHTTPFixture{t: t, base: base, server: server, completions: completions, refs: refs}
}

// This helper returns errors instead of calling testing.Fatal so the same real
// HTTP path can be used safely by the simultaneous clear/access goroutines.
func (fixture *recentRuntimeHTTPFixture) request(method, path, body, token, key, proof, byteRange string, cookie *http.Cookie) recentRuntimeResponse {
	return fixture.requestWithWriteBarrier(method, path, body, token, key, proof, byteRange, cookie, nil)
}

func (fixture *recentRuntimeHTTPFixture) requestWithWriteBarrier(method, path, body, token, key, proof, byteRange string, cookie *http.Cookie, wrote chan<- struct{}) recentRuntimeResponse {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if wrote != nil {
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
			select {
			case wrote <- struct{}{}:
			default:
			}
		}})
	}
	request, err := http.NewRequestWithContext(ctx, method, fixture.server.URL+path, strings.NewReader(body))
	if err != nil {
		return recentRuntimeResponse{err: errors.New("create HTTP request")}
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if proof != "" {
		request.Header.Set(handlers.StepUpHeaderName, proof)
	}
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	if cookie != nil {
		request.AddCookie(cookie)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	done, forget := fixture.completions.track(request)
	defer forget()
	response, err := fixture.server.Client().Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return recentRuntimeResponse{err: errors.New("HTTP transport failed")}
	}
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.TLS == nil {
		return recentRuntimeResponse{err: errors.New("read TLS response")}
	}
	select {
	case <-done:
	case <-ctx.Done():
		return recentRuntimeResponse{err: errors.New("HTTP handler did not complete")}
	}
	return recentRuntimeResponse{status: response.StatusCode, body: payload, cookies: response.Cookies()}
}

func (fixture *recentRuntimeHTTPFixture) requireResponse(response recentRuntimeResponse, status int) {
	fixture.t.Helper()
	if response.err != nil || response.status != status {
		fixture.t.Fatalf("HTTP status=%d want=%d transport=%v", response.status, status, response.err)
	}
}

func (fixture *recentRuntimeHTTPFixture) user(username, role string) (model.User, string) {
	fixture.t.Helper()
	user := model.User{Username: username, PasswordHash: fixture.base.admin.PasswordHash, Role: role, Onboarded: true}
	if err := fixture.base.db.Create(&user).Error; err != nil {
		fixture.t.Fatal("create isolated user")
	}
	token, err := fixture.base.jwt.GenerateToken(user)
	if err != nil {
		fixture.t.Fatal("create isolated session")
	}
	return user, token
}

// Keep the existing secret-reveal helper's semantics intact: this independently
// obtains an asset.download proof through the real authenticated TOTP route.
func (fixture *recentRuntimeHTTPFixture) downloadProof(user model.User, token string) string {
	fixture.t.Helper()
	key, err := auth.GenerateTOTPSecret("FAKE_XIRANG_ISSUER_FOR_TEST_ONLY", user.Username)
	if err != nil {
		fixture.t.Fatal("generate isolated download step-up factor")
	}
	if err := fixture.base.db.First(&user, user.ID).Error; err != nil {
		fixture.t.Fatal("load isolated download step-up user")
	}
	user.TOTPEnabled, user.TOTPSecret = true, key.Secret()
	if err := fixture.base.db.Save(&user).Error; err != nil {
		fixture.t.Fatal("save isolated download step-up factor")
	}
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		fixture.t.Fatal("generate isolated download step-up code")
	}
	payload, err := json.Marshal(map[string]any{"code": code, "step_up_action": auth.StepUpActionAssetDownload})
	if err != nil {
		fixture.t.Fatal("encode download step-up request")
	}
	response := fixture.request(http.MethodPost, "/api/v1/auth/step-up", string(payload), token, "", "", "", nil)
	fixture.requireResponse(response, http.StatusOK)
	var envelope struct {
		Data struct {
			Proof string `json:"proof"`
		} `json:"data"`
	}
	if json.Unmarshal(response.body, &envelope) != nil || envelope.Data.Proof == "" {
		fixture.t.Fatal("decode download step-up proof")
	}
	return envelope.Data.Proof
}

func (fixture *recentRuntimeHTTPFixture) issue(ref backupasset.AssetRef, ownerID uint, token, proof string) recentRuntimeTicket {
	fixture.t.Helper()
	path := "/api/v1/recovery-points/" + ref.RecoveryPointID + "/entries/" + ref.EntryID + "/delivery-tickets"
	response := fixture.request(http.MethodPost, path, recentRuntimeDownload, token, "", proof, "", nil)
	fixture.requireResponse(response, http.StatusOK)
	var envelope struct {
		Data content.TicketDescriptor `json:"data"`
	}
	if json.Unmarshal(response.body, &envelope) != nil {
		fixture.t.Fatal("decode download ticket")
	}
	descriptor := envelope.Data
	const prefix = "/api/v1/asset-content/"
	deliveryID := strings.TrimPrefix(descriptor.ContentURL, prefix)
	if !strings.HasPrefix(descriptor.ContentURL, prefix) || backupasset.ValidateOpaqueID(deliveryID) != nil ||
		descriptor.Action != content.DeliveryDownload || descriptor.Range != content.RangeSingle || len(response.cookies) != 1 {
		fixture.t.Fatal("invalid download ticket or cookie")
	}
	cookie := response.cookies[0]
	if cookie.Name != content.DeliveryCookieName || cookie.Value == "" || !cookie.Secure || !cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteStrictMode || cookie.Path != descriptor.ContentURL {
		fixture.t.Fatal("download ticket did not issue the scoped secure cookie")
	}
	var grant model.BackupAssetDeliveryGrant
	if err := fixture.base.db.Where("delivery_id = ?", deliveryID).Take(&grant).Error; err != nil ||
		grant.OwnerUserID != ownerID || grant.ResourceKind != string(content.DeliveryResourceBackupAsset) ||
		grant.RecoveryPointID == nil || *grant.RecoveryPointID != ref.RecoveryPointID || grant.EntryID == nil || *grant.EntryID != ref.EntryID {
		fixture.t.Fatal("ticket did not persist the expected owner and asset grant")
	}
	var auditCount int64
	if err := fixture.base.db.Model(&model.BackupAssetAuditEvent{}).Where("grant_id = ? AND action = ? AND outcome = ?", grant.ID, string(backupasset.AuditActionAssetDownloadTicket), string(backupasset.AuditOutcomeSuccess)).Count(&auditCount).Error; err != nil || auditCount != 1 {
		fixture.t.Fatal("required download ticket audit was not persisted")
	}
	return recentRuntimeTicket{url: descriptor.ContentURL, cookie: cookie, grantID: grant.ID}
}

func (fixture *recentRuntimeHTTPFixture) serve(ticket recentRuntimeTicket, method, byteRange string, status int, expected string) {
	fixture.t.Helper()
	response := fixture.request(method, ticket.url, "", "", "", "", byteRange, ticket.cookie)
	fixture.requireResponse(response, status)
	if !bytes.Equal(response.body, []byte(expected)) {
		fixture.t.Fatalf("content bytes=%d want=%d", len(response.body), len(expected))
	}
}

// Every expected response also has to be a completed, successful persisted
// request with the corresponding status and response-byte count.
func (fixture *recentRuntimeHTTPFixture) assertRequests(ticket recentRuntimeTicket, heads, full, partial int, fullBytes int) {
	fixture.t.Helper()
	var rows []model.BackupAssetDeliveryRequest
	if err := fixture.base.db.Where("grant_id = ?", ticket.grantID).Find(&rows).Error; err != nil {
		fixture.t.Fatal("read durable delivery requests")
	}
	counts := [3]int{}
	for _, row := range rows {
		if row.State != string(content.RequestSucceeded) || row.FinishedAt == nil || row.FailureCode != "" {
			fixture.t.Fatal("delivery request was not durably finalized as successful")
		}
		switch {
		case row.Method == http.MethodHead && row.HTTPStatus == http.StatusOK && row.ResponseBytes == 0:
			counts[0]++
		case row.Method == http.MethodGet && row.HTTPStatus == http.StatusOK && row.ResponseBytes == int64(fullBytes):
			counts[1]++
		case row.Method == http.MethodGet && row.HTTPStatus == http.StatusPartialContent && row.ResponseBytes == 7:
			counts[2]++
		default:
			fixture.t.Fatalf("unexpected durable request method=%s status=%d bytes=%d", row.Method, row.HTTPStatus, row.ResponseBytes)
		}
	}
	if counts != [3]int{heads, full, partial} {
		fixture.t.Fatalf("durable HEAD/full/range counts=%v want=%v", counts, [3]int{heads, full, partial})
	}
	var grant model.BackupAssetDeliveryGrant
	if err := fixture.base.db.First(&grant, "id = ?", ticket.grantID).Error; err != nil || grant.InFlight != 0 || grant.ReservedBytes != 0 ||
		grant.AuditRequestCount != int64(heads+full+partial) || grant.AuditSuccessCount != int64(heads+full+partial) {
		fixture.t.Fatal("content budget or required audit summary did not finalize successfully")
	}
}

func (fixture *recentRuntimeHTTPFixture) recent(token string) overlay.RecentAccessPage {
	fixture.t.Helper()
	response := fixture.request(http.MethodGet, "/api/v1/asset-recent", "", token, "", "", "", nil)
	fixture.requireResponse(response, http.StatusOK)
	var envelope struct {
		Data overlay.RecentAccessPage `json:"data"`
	}
	if json.Unmarshal(response.body, &envelope) != nil {
		fixture.t.Fatal("decode Recent response")
	}
	return envelope.Data
}

func (fixture *recentRuntimeHTTPFixture) assertRecent(user model.User, token string, ref backupasset.AssetRef, count int64) {
	fixture.t.Helper()
	page := fixture.recent(token)
	var rows []model.BackupAssetRecentAccess
	if err := fixture.base.db.Where("owner_user_id = ?", user.ID).Find(&rows).Error; err != nil {
		fixture.t.Fatal("read durable owner Recent rows")
	}
	if count == 0 {
		if len(page.Items) != 0 || len(rows) != 0 {
			fixture.t.Fatalf("expected empty owner Recent: HTTP=%d DB=%d", len(page.Items), len(rows))
		}
		return
	}
	if len(page.Items) != 1 || len(rows) != 1 || page.Items[0].Ref != ref || page.Items[0].AccessCount != count ||
		rows[0].ID != page.Items[0].ID || rows[0].RecoveryPointID != ref.RecoveryPointID || rows[0].EntryID != ref.EntryID || rows[0].AccessCount != count {
		fixture.t.Fatalf("owner Recent does not match expected reference and count=%d", count)
	}
}

func (fixture *recentRuntimeHTTPFixture) clear(token, key string) int64 {
	fixture.t.Helper()
	response := fixture.request(http.MethodPost, "/api/v1/asset-recent/clear", "", token, key, "", "", nil)
	fixture.requireResponse(response, http.StatusOK)
	var envelope struct {
		Data struct {
			Count int64 `json:"cleared_count"`
		} `json:"data"`
	}
	if json.Unmarshal(response.body, &envelope) != nil {
		fixture.t.Fatal("decode Recent clear response")
	}
	return envelope.Data.Count
}

func TestBackupAssetRecentRuntimeHTTP(t *testing.T) {
	t.Run("OwnerGrantClearReplayAndAuthorization", func(t *testing.T) {
		fixture := newRecentRuntimeHTTPFixture(t, nil)
		owner, token := fixture.base.admin, fixture.base.token
		ref := fixture.refs["readme.txt"]
		proof := fixture.downloadProof(owner, token)
		other, otherToken := fixture.user("recent-other-admin", "admin")
		operator, operatorToken := fixture.user("recent-unassigned-operator", "operator")
		fixture.assertRecent(owner, token, ref, 0)
		fixture.assertRecent(other, otherToken, ref, 0)

		base := "/api/v1/recovery-points/" + ref.RecoveryPointID + "/entries"
		fixture.requireResponse(fixture.request(http.MethodGet, base, "", token, "", "", "", nil), http.StatusOK)
		fixture.requireResponse(fixture.request(http.MethodGet, base+"/"+ref.EntryID, "", token, "", "", "", nil), http.StatusOK)
		fixture.assertRecent(owner, token, ref, 0)
		ticket := fixture.issue(ref, owner.ID, token, proof)
		fixture.assertRecent(owner, token, ref, 0)
		fixture.serve(ticket, http.MethodHead, "", http.StatusOK, "")
		fixture.assertRequests(ticket, 1, 0, 0, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 0)
		fixture.serve(ticket, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(ticket, 1, 1, 0, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 1)
		fixture.assertRecent(other, otherToken, ref, 0)
		fixture.serve(ticket, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.serve(ticket, http.MethodGet, "bytes=0-6", http.StatusPartialContent, recentRuntimePayload[:7])
		fixture.assertRequests(ticket, 1, 2, 1, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 1)

		otherProof := fixture.downloadProof(other, otherToken)
		otherTicket := fixture.issue(ref, other.ID, otherToken, otherProof)
		fixture.serve(otherTicket, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(otherTicket, 0, 1, 0, len(recentRuntimePayload))
		fixture.assertRecent(other, otherToken, ref, 1)
		fixture.assertRecent(owner, token, ref, 1)

		var assignments int64
		if err := fixture.base.db.Model(&model.NodeOwner{}).Where("user_id = ?", operator.ID).Count(&assignments).Error; err != nil || assignments != 0 {
			t.Fatal("operator fixture unexpectedly has node ownership")
		}
		fixture.requireResponse(fixture.request(http.MethodGet, base+"/"+ref.EntryID, "", operatorToken, "", "", "", nil), http.StatusNotFound)
		fixture.requireResponse(fixture.request(http.MethodPost, base+"/"+ref.EntryID+"/delivery-tickets", `{"schema_version":1,"action":"preview","preview_intent":"safe_preview_v1"}`, operatorToken, "", "", "", nil), http.StatusNotFound)
		var operatorGrants int64
		if err := fixture.base.db.Model(&model.BackupAssetDeliveryGrant{}).Where("owner_user_id = ?", operator.ID).Count(&operatorGrants).Error; err != nil || operatorGrants != 0 {
			t.Fatal("unassigned operator received a persisted delivery grant")
		}
		fixture.assertRecent(operator, operatorToken, ref, 0)

		const clearKey = "recent-runtime-owner-clear"
		if count := fixture.clear(token, clearKey); count != 1 {
			t.Fatalf("initial clear count=%d", count)
		}
		fixture.assertRecent(owner, token, ref, 0)
		fixture.assertRecent(other, otherToken, ref, 1)
		fixture.serve(ticket, http.MethodGet, "bytes=0-6", http.StatusPartialContent, recentRuntimePayload[:7])
		fixture.serve(ticket, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(ticket, 1, 3, 2, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 0)
		newTicket := fixture.issue(ref, owner.ID, token, proof)
		if newTicket.grantID == ticket.grantID {
			t.Fatal("second ticket reused the first grant")
		}
		fixture.serve(newTicket, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(newTicket, 0, 1, 0, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 1)
		if count := fixture.clear(token, clearKey); count != 1 {
			t.Fatalf("replayed clear count=%d", count)
		}
		fixture.assertRecent(owner, token, ref, 1)
		fixture.assertRecent(other, otherToken, ref, 1)
	})

	t.Run("QuotaFailureDoesNotRetryGrant", func(t *testing.T) {
		fixture := newRecentRuntimeHTTPFixture(t, map[string]string{"backup_assets.recent_quota": "1"})
		owner, token := fixture.base.admin, fixture.base.token
		proof := fixture.downloadProof(owner, token)
		firstRef, secondRef := fixture.refs["readme.txt"], fixture.refs["other.txt"]
		first := fixture.issue(firstRef, owner.ID, token, proof)
		fixture.serve(first, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(first, 0, 1, 0, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, firstRef, 1)
		var usage model.BackupAssetOverlayUsage
		if err := fixture.base.db.First(&usage, "owner_user_id = ?", owner.ID).Error; err != nil || usage.RecentCount != 1 {
			t.Fatal("real first asset did not fill the isolated Recent quota")
		}
		blocked := fixture.issue(secondRef, owner.ID, token, proof)
		fixture.serve(blocked, http.MethodGet, "", http.StatusOK, recentRuntimeOtherPayload)
		fixture.assertRequests(blocked, 0, 1, 0, len(recentRuntimeOtherPayload))
		fixture.assertRecent(owner, token, firstRef, 1)
		if err := fixture.base.db.First(&usage, "owner_user_id = ?", owner.ID).Error; err != nil || usage.RecentCount != 1 || usage.RecentRateWindowWriteCount != 1 {
			t.Fatal("quota-rejected Recent attempt changed owner usage")
		}
		if count := fixture.clear(token, "recent-runtime-quota-clear"); count != 1 {
			t.Fatalf("quota clear count=%d", count)
		}
		fixture.serve(blocked, http.MethodGet, "", http.StatusOK, recentRuntimeOtherPayload)
		fixture.assertRequests(blocked, 0, 2, 0, len(recentRuntimeOtherPayload))
		fixture.assertRecent(owner, token, secondRef, 0)
		newTicket := fixture.issue(secondRef, owner.ID, token, proof)
		fixture.serve(newTicket, http.MethodGet, "", http.StatusOK, recentRuntimeOtherPayload)
		fixture.assertRequests(newTicket, 0, 1, 0, len(recentRuntimeOtherPayload))
		fixture.assertRecent(owner, token, secondRef, 1)
	})

	t.Run("RateFailureDoesNotRetryGrant", func(t *testing.T) {
		fixture := newRecentRuntimeHTTPFixture(t, map[string]string{
			"backup_assets.recent_writes_per_minute": "1",
			"backup_assets.content_idle_ttl":         "2m",
		})
		owner, token := fixture.base.admin, fixture.base.token
		ref := fixture.refs["readme.txt"]
		proof := fixture.downloadProof(owner, token)
		first := fixture.issue(ref, owner.ID, token, proof)
		fixture.serve(first, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(first, 0, 1, 0, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 1)
		var usage model.BackupAssetOverlayUsage
		if err := fixture.base.db.First(&usage, "owner_user_id = ?", owner.ID).Error; err != nil || usage.RecentRateWindowWriteCount != 1 || usage.RecentRateWindowStartedAt.IsZero() {
			t.Fatal("real first access did not fill the Recent rate window")
		}
		blocked := fixture.issue(ref, owner.ID, token, proof)
		fixture.serve(blocked, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(blocked, 0, 1, 0, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 1)
		if err := fixture.base.db.First(&usage, "owner_user_id = ?", owner.ID).Error; err != nil || usage.RecentRateWindowWriteCount != 1 || usage.RecentCount != 1 {
			t.Fatal("rate-rejected Recent attempt changed owner usage")
		}
		fixture.base.clock.Advance(61 * time.Second)
		fixture.serve(blocked, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(blocked, 0, 2, 0, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 1)
		newTicket := fixture.issue(ref, owner.ID, token, proof)
		fixture.serve(newTicket, http.MethodGet, "", http.StatusOK, recentRuntimePayload)
		fixture.assertRequests(newTicket, 0, 1, 0, len(recentRuntimePayload))
		fixture.assertRecent(owner, token, ref, 2)
	})

	t.Run("ConcurrentFirstAccessAndClear", func(t *testing.T) {
		fixture := newRecentRuntimeHTTPFixture(t, nil)
		owner, token := fixture.base.admin, fixture.base.token
		ref := fixture.refs["readme.txt"]
		proof := fixture.downloadProof(owner, token)
		ticket := fixture.issue(ref, owner.ID, token, proof)
		fixture.assertRecent(owner, token, ref, 0)
		// The fixture opens SQLite with _txlock=immediate. Hold its writer lock
		// until both HTTP requests have actually been transmitted; neither can
		// commit while the other has yet to be sent.
		lock := fixture.base.db.Begin()
		if lock.Error != nil {
			t.Fatal("acquire isolated SQLite writer barrier")
		}
		defer lock.Rollback()
		start := make(chan struct{})
		ready := make(chan struct{}, 2)
		accessWritten := make(chan struct{}, 1)
		clearWritten := make(chan struct{}, 1)
		results := make(chan recentRuntimeResponse, 2)
		go func() {
			ready <- struct{}{}
			<-start
			results <- fixture.requestWithWriteBarrier(http.MethodGet, ticket.url, "", "", "", "", "", ticket.cookie, accessWritten)
		}()
		go func() {
			ready <- struct{}{}
			<-start
			results <- fixture.requestWithWriteBarrier(http.MethodPost, "/api/v1/asset-recent/clear", "", token, "recent-runtime-concurrent-clear", "", "", nil, clearWritten)
		}()
		<-ready
		<-ready
		close(start)
		for _, written := range []<-chan struct{}{accessWritten, clearWritten} {
			select {
			case <-written:
			case response := <-results:
				t.Fatalf("request completed before the concurrent barrier: status=%d transport=%v", response.status, response.err)
			case <-time.After(10 * time.Second):
				t.Fatal("concurrent HTTP request was not transmitted")
			}
		}
		select {
		case response := <-results:
			t.Fatalf("request completed while the writer barrier was held: status=%d transport=%v", response.status, response.err)
		default:
		}
		if err := lock.Rollback().Error; err != nil {
			t.Fatal("release isolated SQLite writer barrier")
		}
		responses := []recentRuntimeResponse{<-results, <-results}
		var clearCount int64 = -1
		for _, response := range responses {
			fixture.requireResponse(response, http.StatusOK)
			if bytes.Equal(response.body, []byte(recentRuntimePayload)) {
				continue
			}
			var envelope struct {
				Data struct {
					Count int64 `json:"cleared_count"`
				} `json:"data"`
			}
			if json.Unmarshal(response.body, &envelope) != nil || clearCount != -1 {
				t.Fatal("concurrent clear response was invalid")
			}
			clearCount = envelope.Data.Count
		}
		fixture.assertRequests(ticket, 0, 1, 0, len(recentRuntimePayload))
		switch clearCount {
		case 0:
			fixture.assertRecent(owner, token, ref, 1)
		case 1:
			fixture.assertRecent(owner, token, ref, 0)
		default:
			t.Fatalf("concurrent clear count=%d", clearCount)
		}
		var usage model.BackupAssetOverlayUsage
		if err := fixture.base.db.First(&usage, "owner_user_id = ?", owner.ID).Error; err != nil || usage.RecentCount != 1-clearCount || usage.RecentRateWindowWriteCount != 1 {
			t.Fatal("concurrent outcome was inconsistent with the committed owner usage")
		}
		var receipts []model.BackupAssetOverlayIdempotency
		if err := fixture.base.db.Where("owner_user_id = ? AND action = ?", owner.ID, "recent_clear").Find(&receipts).Error; err != nil || len(receipts) != 1 || int64(receipts[0].ResultVersion) != clearCount {
			t.Fatal("concurrent clear receipt did not persist its committed result")
		}
		if replay := fixture.clear(token, "recent-runtime-concurrent-clear"); replay != clearCount {
			t.Fatal("concurrent clear replay changed its committed result")
		}
		fixture.assertRecent(owner, token, ref, 1-clearCount)
	})

	t.Run("FeatureDisabled", func(t *testing.T) {
		fixture := newRecentRuntimeHTTPFixture(t, nil)
		owner, token := fixture.base.admin, fixture.base.token
		ref := fixture.refs["readme.txt"]
		proof := fixture.downloadProof(owner, token)
		ticket := fixture.issue(ref, owner.ID, token, proof)
		fixture.assertRecent(owner, token, ref, 0)
		if err := fixture.base.runtime.TransitionFeature(context.Background(), false, func() error {
			return fixture.base.setting.Update("backup_assets.enabled", "false")
		}); err != nil {
			t.Fatal("disable isolated production feature")
		}
		if live, err := fixture.base.runtime.FeatureLive(); err != nil || live {
			t.Fatal("runtime feature remained live")
		}
		fixture.requireResponse(fixture.request(http.MethodGet, "/api/v1/asset-recent", "", token, "", "", "", nil), http.StatusServiceUnavailable)
		path := "/api/v1/recovery-points/" + ref.RecoveryPointID + "/entries/" + ref.EntryID + "/delivery-tickets"
		fixture.requireResponse(fixture.request(http.MethodPost, path, recentRuntimeDownload, token, "", proof, "", nil), http.StatusServiceUnavailable)
		fixture.requireResponse(fixture.request(http.MethodGet, ticket.url, "", "", "", "", "", ticket.cookie), http.StatusNotFound)
		var requests, rows, grants int64
		if err := fixture.base.db.Model(&model.BackupAssetDeliveryRequest{}).Where("grant_id = ?", ticket.grantID).Count(&requests).Error; err != nil {
			t.Fatal("read disabled delivery requests")
		}
		if err := fixture.base.db.Model(&model.BackupAssetRecentAccess{}).Where("owner_user_id = ?", owner.ID).Count(&rows).Error; err != nil {
			t.Fatal("read disabled Recent rows")
		}
		if err := fixture.base.db.Model(&model.BackupAssetDeliveryGrant{}).Where("owner_user_id = ?", owner.ID).Count(&grants).Error; err != nil {
			t.Fatal("read disabled grants")
		}
		if requests != 0 || rows != 0 || grants != 1 {
			t.Fatalf("disabled feature persisted requests=%d recent=%d grants=%d", requests, rows, grants)
		}
	})
}
