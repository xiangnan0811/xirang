package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/escalation"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const escalationDraftPath = "/api/v1/escalation-policies/preview"

func escalationDraftPayload() []byte {
	return []byte(`{"policy":{"name":"readonly preview","description":"","min_severity":"warning","enabled":true,"levels":[{"delay_seconds":0,"integration_ids":[1,1,2,999],"severity_override":"","tags":[]},{"delay_seconds":300,"integration_ids":[1],"severity_override":"critical","tags":["escalated"]}]},"scenario":{"node_id":0,"error_code":"XR-EXEC-7","severity":"warning","tags":[],"triggered_at":"2026-10-18T10:00:00Z","evaluation_at":"2026-10-18T10:00:00Z"}}`)
}

func escalationDecode(t *testing.T, response *httptest.ResponseRecorder) escalation.PreviewResult {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data escalation.PreviewResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Steps == nil || envelope.Data.Warnings == nil {
		t.Fatalf("arrays must not be null: %s", response.Body.String())
	}
	return envelope.Data
}

func escalationSeed(t *testing.T, db *gorm.DB) (model.Alert, model.EscalationPolicy) {
	t.Helper()
	for _, integration := range []model.Integration{{ID: 1, Name: "preview enabled", Type: "webhook", Endpoint: "http://127.0.0.1:1/FAKE_ENDPOINT_FOR_TEST_ONLY", Enabled: true}, {ID: 2, Name: "preview disabled", Type: "webhook", Endpoint: "http://127.0.0.1:1/FAKE_ENDPOINT_FOR_TEST_ONLY", Enabled: true}} {
		if err := db.Create(&integration).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.Integration{}).Where("id = ?", 2).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	policy := model.EscalationPolicy{Name: "readonly preview", MinSeverity: "warning", Enabled: true, Levels: `[{"delay_seconds":0,"integration_ids":[1,1,2,999],"severity_override":"","tags":[]},{"delay_seconds":300,"integration_ids":[1],"severity_override":"critical","tags":["escalated"]}]`}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	node := model.Node{Name: "preview node", Host: "127.0.0.1", Username: "user", BackupDir: "/backup", Tags: "prod", EscalationPolicyID: &policy.ID}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	alert := model.Alert{NodeID: node.ID, NodeName: "PRIVATE_NODE_NAME", PolicyName: "PRIVATE_POLICY_NAME", ErrorCode: "XR-EXEC-7", Message: "PRIVATE_ALERT_MESSAGE", Severity: "warning", Status: "open", Tags: "[]", LastLevelFired: -1, TriggeredAt: time.Now().UTC().Add(-time.Hour), DeliveryDecision: "direct"}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatal(err)
	}
	return alert, policy
}

func escalationFixtureAdminID(t *testing.T, fixture configImportPreviewRouterFixture) uint {
	t.Helper()
	claims, err := fixture.jwt.ParseToken(fixture.tokens["admin"])
	if err != nil {
		t.Fatalf("parse fixture admin token: %v", err)
	}
	if claims.UserID == 0 {
		t.Fatal("fixture admin token has no user ID")
	}
	return claims.UserID
}

func escalationAssertSafe(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing no-store on status %d: %v", response.Code, response.Header())
	}
	for _, secret := range []string{"PRIVATE_ALERT_MESSAGE", "PRIVATE_NODE_NAME", "PRIVATE_POLICY_NAME", "FAKE_ENDPOINT_FOR_TEST_ONLY", "PRIVATE_DELIVERY_ERROR", "PRIVATE_SILENCE_NOTE", "attempt_id", "delivery_key", "last_error", "endpoint", "secret", "proxy"} {
		if strings.Contains(strings.ToLower(response.Body.String()), strings.ToLower(secret)) {
			t.Fatalf("unsafe response contains %q: %s", secret, response.Body.String())
		}
	}
}

func escalationAssertUnchanged(t *testing.T, f configImportPreviewRouterFixture, before map[string]string, writes *atomic.Int64) {
	t.Helper()
	if after := configImportPreviewRouterDatabaseSnapshot(t, f.db); !reflect.DeepEqual(before, after) {
		t.Fatalf("preview changed canonical database tables")
	}
	if writes != nil && writes.Load() != 0 {
		t.Fatalf("preview attempted %d writes", writes.Load())
	}
}

func TestEscalationPreviewRouterAdminDraftAndAlertZeroWrites(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, policy := escalationSeed(t, f.db)
	// A disabled metadata row with corrupt encrypted credentials must still be
	// explainable: inventory is not permitted to run Integration.AfterFind.
	if err := f.db.Table("integrations").Where("id = ?", 2).Update("secret", "enc:v2:FAKE_INVALID_CIPHERTEXT_FOR_TEST_ONLY").Error; err != nil {
		t.Fatal(err)
	}
	var sends atomic.Int64
	oldTransport := http.DefaultTransport
	http.DefaultTransport = escalationTransportFunc(func(*http.Request) (*http.Response, error) {
		sends.Add(1)
		return nil, errors.New("external send forbidden")
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	smtpDone := make(chan struct{})
	go func() {
		defer close(smtpDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			sends.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-smtpDone })
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { sends.Add(1); w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(webhook.Close)
	if err := f.db.Model(&model.Integration{}).Where("id = ?", 1).Update("endpoint", webhook.URL).Error; err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SMTP_FROM", "preview@example.invalid")
	t.Setenv("SMTP_REQUIRE_TLS", "false")
	if err := f.db.Create(&model.Integration{ID: 3, Name: "preview SMTP", Type: "email", Endpoint: "preview@example.invalid", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&policy).Update("levels", strings.Replace(policy.Levels, "[1,1,2,999]", "[1,1,2,999,3]", 1)).Error; err != nil {
		t.Fatal(err)
	}
	writes := installConfigImportPreviewRouterWriteGate(t, f.db)
	before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
	for _, tc := range []struct {
		method, path string
		body         []byte
	}{{http.MethodPost, escalationDraftPath, []byte(strings.Replace(string(escalationDraftPayload()), "[1,1,2,999]", "[1,1,2,999,3]", 1))}, {http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), nil}} {
		response := configImportPreviewRouterRequest(t, f.router, tc.method, tc.path, f.tokens["admin"], tc.body, nil)
		got := escalationDecode(t, response)
		escalationAssertSafe(t, response)
		if got.Status != "projected" || len(got.Steps) != 2 || len(got.Steps[0].Channels) != 4 || got.Steps[0].Channels[0].State != "enabled" || got.Steps[0].Channels[1].State != "disabled" || got.Steps[0].Channels[2].State != "missing" || got.Steps[0].Channels[3].State != "enabled" {
			t.Fatalf("result=%+v", got)
		}
		if tc.method == http.MethodGet {
			if got.RequestedAlertID == nil || *got.RequestedAlertID != alert.ID || got.Alert == nil || got.Alert.ID == nil || *got.Alert.ID != alert.ID || got.PolicySource != "node" || got.DeliveryDecision == nil || *got.DeliveryDecision != "direct" {
				t.Fatalf("alert identity=%+v", got)
			}
		} else if got.Policy.ID != nil || got.Policy.UpdatedAt != nil || got.RequestedAlertID != nil {
			t.Fatalf("draft identity=%+v", got)
		}
		escalationAssertUnchanged(t, f, before, writes)
	}
	if sends.Load() != 0 {
		t.Fatalf("preview initiated %d external sends", sends.Load())
	}
}

type escalationTransportFunc func(*http.Request) (*http.Response, error)

func (f escalationTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestEscalationPreviewRouterAuthorizationAndStrictRequests(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, _ := escalationSeed(t, f.db)
	var admin model.User
	if err := f.db.Where("username = ?", "config-preview-admin").First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	expiredManager := auth.NewJWTManager("FAKE_CONFIG_PREVIEW_ROUTER_JWT_SECRET_FOR_TEST_ONLY", -time.Hour)
	expired, err := expiredManager.GenerateToken(admin)
	if err != nil {
		t.Fatal(err)
	}
	writes := installConfigImportPreviewRouterWriteGate(t, f.db)
	before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
	alertPath := fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID)
	for _, endpoint := range []struct {
		method, path string
		body         []byte
	}{{http.MethodPost, escalationDraftPath, escalationDraftPayload()}, {http.MethodGet, alertPath, nil}} {
		for _, tc := range []struct {
			name, token string
			want        int
		}{{"operator", f.tokens["operator"], 403}, {"viewer", f.tokens["viewer"], 403}, {"missing", "", 401}, {"revoked", f.tokens["revoked"], 401}, {"invalid", f.tokens["invalid"], 401}, {"expired", expired, 401}} {
			t.Run(endpoint.method+"/"+tc.name, func(t *testing.T) {
				response := configImportPreviewRouterRequest(t, f.router, endpoint.method, endpoint.path, tc.token, endpoint.body, nil)
				if response.Code != tc.want {
					t.Fatalf("status=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
				}
				escalationAssertSafe(t, response)
				escalationAssertUnchanged(t, f, before, writes)
			})
		}
	}
	base := string(escalationDraftPayload())
	badBodies := map[string]string{
		"root-null": "null", "root-array": "[]", "empty": "{}", "extra-object": base + ` {}`, "trailing-null": base + ` null`, "unknown-root": strings.Replace(base, `"policy":`, `"extra":1,"policy":`, 1),
		"policy-null":         strings.Replace(base, `"policy":{`, `"policy":null,"ignored":{`, 1),
		"missing-description": strings.Replace(base, `"description":"",`, "", 1), "null-description": strings.Replace(base, `"description":""`, `"description":null`, 1),
		"null-enabled": strings.Replace(base, `"enabled":true`, `"enabled":null`, 1), "wrong-enabled": strings.Replace(base, `"enabled":true`, `"enabled":"true"`, 1),
		"null-level": strings.Replace(base, `"levels":[{`, `"levels":[null,{`, 1), "null-channel": strings.Replace(base, `[1,1,2,999]`, `[1,null]`, 1), "zero-channel": strings.Replace(base, `[1,1,2,999]`, `[0]`, 1), "unsafe-channel": strings.Replace(base, `[1,1,2,999]`, `[9007199254740992]`, 1),
		"null-level-tag": strings.Replace(base, `"tags":[]`, `"tags":[null]`, 1), "unknown-level": strings.Replace(base, `"delay_seconds":0`, `"unknown":1,"delay_seconds":0`, 1),
		"negative-delay": strings.Replace(base, `"delay_seconds":0`, `"delay_seconds":-1`, 1), "overflow-delay": strings.Replace(base, `"delay_seconds":300`, `"delay_seconds":10000000000`, 1),
		"negative-node": strings.Replace(base, `"node_id":0`, `"node_id":-1`, 1), "unsafe-node": strings.Replace(base, `"node_id":0`, `"node_id":9007199254740992`, 1), "fractional-node": strings.Replace(base, `"node_id":0`, `"node_id":1.2`, 1),
		"no-zone": strings.ReplaceAll(base, "2026-10-18T10:00:00Z", "2026-10-18T10:00:00"), "bad-date": strings.ReplaceAll(base, "2026-10-18T10:00:00Z", "2026-02-30T10:00:00Z"), "saturated-date": strings.Replace(base, `"triggered_at":"2026-10-18T10:00:00Z"`, `"triggered_at":"0001-01-01T00:00:00Z"`, 1),
		"empty-error": strings.Replace(base, `"XR-EXEC-7"`, `""`, 1), "control-error": strings.Replace(base, `"XR-EXEC-7"`, `"XR-EXEC-\n"`, 1), "long-error": strings.Replace(base, `"XR-EXEC-7"`, `"`+strings.Repeat("x", 129)+`"`, 1),
		"bad-severity": strings.Replace(base, `"severity":"warning"`, `"severity":"fatal"`, 1), "null-scenario-tag": strings.Replace(base, `"severity":"warning","tags":[]`, `"severity":"warning","tags":[null]`, 1),
		"long-tag":               strings.Replace(base, `"tags":[]`, `"tags":["`+strings.Repeat("界", 11)+`"]`, 1),
		"too-many-level-tags":    strings.Replace(base, `"tags":[]`, `"tags":[`+strings.TrimSuffix(strings.Repeat(`"x",`, 11), ",")+`]`, 1),
		"too-many-scenario-tags": strings.Replace(base, `"severity":"warning","tags":[]`, `"severity":"warning","tags":[`+strings.TrimSuffix(strings.Repeat(`"x",`, 11), ",")+`]`, 1),
		"too-many-channels":      strings.Replace(base, `[1,1,2,999]`, `[`+strings.TrimSuffix(strings.Repeat("1,", 101), ",")+`]`, 1),
		"unknown-scenario":       strings.Replace(base, `"node_id":0`, `"unknown":1,"node_id":0`, 1),
		"null-policy":            strings.Replace(base, base[strings.Index(base, `"policy":`)+9:strings.Index(base, `,"scenario"`)], "null", 1),
	}
	for name, body := range badBodies {
		t.Run(name, func(t *testing.T) {
			response := configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], []byte(body), nil)
			if response.Code != 400 {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			escalationAssertSafe(t, response)
		})
	}
	for _, section := range []string{"policy", "scenario", "level"} {
		fields := map[string][]string{"policy": {"name", "description", "min_severity", "enabled", "levels"}, "scenario": {"node_id", "error_code", "severity", "tags", "triggered_at", "evaluation_at"}, "level": {"delay_seconds", "integration_ids", "severity_override", "tags"}}[section]
		for _, field := range fields {
			for _, mode := range []string{"missing", "null"} {
				var root map[string]any
				if err := json.Unmarshal(escalationDraftPayload(), &root); err != nil {
					t.Fatal(err)
				}
				var target map[string]any
				if section == "level" {
					target = root["policy"].(map[string]any)["levels"].([]any)[0].(map[string]any)
				} else {
					target = root[section].(map[string]any)
				}
				if mode == "missing" {
					delete(target, field)
				} else {
					target[field] = nil
				}
				body, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				response := configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], body, nil)
				if response.Code != 400 {
					t.Fatalf("%s.%s %s accepted: %d %s", section, field, mode, response.Code, response.Body.String())
				}
			}
		}
	}
	// Duplicate keys intentionally retain encoding/json's last-value semantics.
	duplicate := strings.Replace(base, `"enabled":true`, `"enabled":false,"enabled":true`, 1)
	if got := escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], []byte(duplicate), nil)); got.Status != "projected" {
		t.Fatalf("duplicate-key contract=%+v", got)
	}
	for _, suffix := range []string{"0", "-1", "+1", "9007199254740992", "1.0", "abc"} {
		response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, "/api/v1/alerts/"+suffix+"/escalation-preview", f.tokens["admin"], nil, nil)
		if response.Code != 400 {
			t.Fatalf("bad ID %s status=%d body=%s", suffix, response.Code, response.Body.String())
		}
	}
	for _, suffix := range []string{"?x=1", "?evaluation_at=2026-01-01T00:00:00Z", "?"} {
		response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, alertPath+suffix, f.tokens["admin"], nil, nil)
		if response.Code != 400 {
			t.Fatalf("query accepted: %s %d", suffix, response.Code)
		}
	}
	for _, body := range []string{" ", "{}", "null"} {
		response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, alertPath, f.tokens["admin"], []byte(body), nil)
		if response.Code != 400 {
			t.Fatalf("GET body accepted: %q %d", body, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, alertPath, io.NopCloser(strings.NewReader("{}")))
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	request.Header.Set("Authorization", "Bearer "+f.tokens["admin"])
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatalf("chunked GET status=%d body=%s", response.Code, response.Body.String())
	}
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		path := escalationDraftPath
		if method == http.MethodGet {
			path = alertPath
		}
		response := configImportPreviewRouterRequest(t, f.router, method, path, f.tokens["admin"], bytes.Repeat([]byte("x"), 64<<10+1), nil)
		if response.Code != 413 {
			t.Fatalf("oversize %s status=%d body=%s", method, response.Code, response.Body.String())
		}
		escalationAssertSafe(t, response)
	}
	for _, body := range []string{strings.Replace(base, `"enabled":true`, `"enabled":false`, 1), strings.ReplaceAll(base, "2026-10-18T10:00:00Z", "2026-10-18T12:00:00+02:00")} {
		response := configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], []byte(body), nil)
		got := escalationDecode(t, response)
		if !got.EvaluationAt.Equal(time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("UTC conversion=%v", got.EvaluationAt)
		}
	}
	escalationAssertUnchanged(t, f, before, writes)
}

func TestEscalationPreviewRouterErrorsAndCapacityZeroWrites(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, _ := escalationSeed(t, f.db)
	writes := installConfigImportPreviewRouterWriteGate(t, f.db)
	before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
	for _, tc := range []struct {
		method, path string
		body         []byte
		want         int
	}{{http.MethodGet, "/api/v1/alerts/999999/escalation-preview", nil, 404}, {http.MethodPost, escalationDraftPath, []byte(strings.Replace(string(escalationDraftPayload()), `"node_id":0`, `"node_id":999999`, 1)), 404}} {
		response := configImportPreviewRouterRequest(t, f.router, tc.method, tc.path, f.tokens["admin"], tc.body, nil)
		if response.Code != tc.want {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		escalationAssertSafe(t, response)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], escalationDraftPayload(), ctx)
	if response.Code != 503 {
		t.Fatalf("cancelled=%d %s", response.Code, response.Body.String())
	}
	escalationAssertSafe(t, response)
	injected := errors.New("PRIVATE_SQL_PREVIEW_FAILURE")
	name := "test:escalation-preview-database-error"
	observed := false
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "nodes" {
			observed = true
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	response = configImportPreviewRouterRequest(t, f.router, http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), f.tokens["admin"], nil, nil)
	_ = f.db.Callback().Query().Remove(name)
	if !observed || response.Code != 500 || strings.Contains(response.Body.String(), injected.Error()) {
		t.Fatalf("strict resolver DB failure observed=%v status=%d body=%s", observed, response.Code, response.Body.String())
	}
	escalationAssertSafe(t, response)
	escalationAssertUnchanged(t, f, before, writes)
	// A clean request after cancellation proves the single-connection pool is usable.
	escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], escalationDraftPayload(), nil))
}

func TestEscalationPreviewRouterStoredStateAndDeliveryCounts(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, policy := escalationSeed(t, f.db)
	for i, status := range []string{"pending", "sending", "retrying", "sent", "failed", "legacy_unknown"} {
		delivery := model.AlertDelivery{AlertID: alert.ID, IntegrationID: 1, Status: status, DeliveryKey: fmt.Sprintf("preview-fixture-%d", i), AttemptID: "PRIVATE_ATTEMPT", LastError: "PRIVATE_DELIVERY_ERROR"}
		if err := f.db.Create(&delivery).Error; err != nil {
			t.Fatal(err)
		}
	}
	path := fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID)
	for _, status := range []string{"acked", "resolved"} {
		if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Update("status", status).Error; err != nil {
			t.Fatal(err)
		}
		before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
		response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil)
		got := escalationDecode(t, response)
		if got.Status != "not_open" || len(got.Steps) != 0 || got.DeliveryCounts == nil || *got.DeliveryCounts != (escalation.DeliveryCounts{Pending: 1, Sending: 1, Retrying: 1, Sent: 1, Failed: 1, Unknown: 1}) {
			t.Fatalf("resolved counts=%+v", got)
		}
		escalationAssertSafe(t, response)
		escalationAssertUnchanged(t, f, before, nil)
	}
	if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Updates(map[string]any{"status": "open", "last_level_fired": 0}).Error; err != nil {
		t.Fatal(err)
	}
	event := model.AlertEscalationEvent{AlertID: alert.ID, LevelIndex: 0, EscalationPolicyID: nil, SeverityBefore: "warning", SeverityAfter: "warning", FiredAt: alert.TriggeredAt}
	if err := f.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	got := escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil))
	if got.Status != "projected" || !reflect.DeepEqual(got.Warnings, []string{"policy_changed_since_history"}) || len(got.Steps) != 1 || got.Steps[0].LevelIndex != 1 {
		t.Fatalf("history changed=%+v", got)
	}
	for _, cursor := range []int{2, 999, 1<<53 - 1} {
		if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Update("last_level_fired", cursor).Error; err != nil {
			t.Fatal(err)
		}
		got = escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil))
		if got.Status != "complete" || len(got.Steps) != 0 || containsEscalationWarning(got.Warnings, "event_cursor_conflict") {
			t.Fatalf("completed cursor %d must not wrap to pending=%+v", cursor, got)
		}
	}
	if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Update("last_level_fired", -1).Error; err != nil {
		t.Fatal(err)
	}
	got = escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil))
	if got.Status != "unsupported_state" || got.Alert != nil || got.Policy != nil || got.RequestedAlertID == nil || *got.RequestedAlertID != alert.ID || !containsEscalationWarning(got.Warnings, "event_cursor_conflict") {
		t.Fatalf("conflicting cursor=%+v", got)
	}
	if err := f.db.Delete(&event).Error; err != nil {
		t.Fatal(err)
	}
	for _, raw := range []any{"", " \n ", "null", "[]"} {
		if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Update("tags", raw).Error; err != nil {
			t.Fatal(err)
		}
		got = escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil))
		if got.Status != "projected" || got.Alert.Tags == nil {
			t.Fatalf("legacy tags %q =%+v", raw, got)
		}
	}
	for _, tc := range []struct {
		column  string
		value   any
		warning string
	}{{"tags", `[null]`, "unsupported_alert_state"}, {"severity", "fatal", "unsupported_alert_state"}, {"status", "closed", "unsupported_alert_state"}, {"last_level_fired", -2, "unsupported_alert_state"}, {"delivery_reason", "feature_retired", "unsupported_alert_state"}} {
		if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Update(tc.column, tc.value).Error; err != nil {
			t.Fatal(err)
		}
		before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
		got = escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil))
		if got.Status != "unsupported_state" || got.Policy != nil || got.Alert != nil || got.DeliveryCounts != nil || len(got.Steps) != 0 || !containsEscalationWarning(got.Warnings, tc.warning) {
			t.Fatalf("unsupported %s =%+v", tc.column, got)
		}
		escalationAssertUnchanged(t, f, before, nil)
		reset := map[string]any{"tags": "[]", "severity": "warning", "status": "open", "last_level_fired": -1, "delivery_reason": ""}
		if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Updates(reset).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`null`, `[]`, `[{`, `[{"delay_seconds":10000000000,"integration_ids":[1],"tags":[]}]`} {
		if err := f.db.Model(&policy).Update("levels", raw).Error; err != nil {
			t.Fatal(err)
		}
		got = escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil))
		if got.Status != "unsupported_state" || got.Policy != nil || got.Alert != nil {
			t.Fatalf("unsupported policy=%+v", got)
		}
	}
}

func containsEscalationWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}

func TestEscalationPreviewRouterSilenceBoundAndLegacyNULL(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, _ := escalationSeed(t, f.db)
	createdBy := escalationFixtureAdminID(t, f)
	windowStart := time.Now().UTC().Add(-time.Hour)
	windowEnd := windowStart.Add(3 * time.Hour)
	silence := model.Silence{Name: "PRIVATE_SILENCE_NAME", Note: "PRIVATE_SILENCE_NOTE", StartsAt: windowStart, EndsAt: windowEnd, MatchTags: "[]", CreatedBy: createdBy}
	if err := f.db.Create(&silence).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID)
	for _, raw := range []any{nil, "", " \n ", "null", "[]"} {
		if err := f.db.Model(&silence).Update("match_tags", raw).Error; err != nil {
			t.Fatal(err)
		}
		response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil)
		got := escalationDecode(t, response)
		if got.Status != "projected" || got.Steps[0].Status != "silenced" {
			t.Fatalf("legacy silence=%+v", got)
		}
		escalationAssertSafe(t, response)
	}
	for _, raw := range []string{`[null]`, `[1]`, `{}`, `"prod"`, `[`} {
		if err := f.db.Model(&silence).Update("match_tags", raw).Error; err != nil {
			t.Fatal(err)
		}
		got := escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil))
		if got.Status != "unsupported_state" || got.Alert != nil || got.Policy != nil {
			t.Fatalf("bad silence=%+v", got)
		}
	}
	if err := f.db.Model(&silence).Update("match_tags", "[]").Error; err != nil {
		t.Fatal(err)
	}
	rows := make([]model.Silence, 1000)
	for i := range rows {
		rows[i] = model.Silence{Name: fmt.Sprintf("bound-%d", i), StartsAt: windowStart, EndsAt: windowEnd, MatchTags: "[]", CreatedBy: createdBy}
	}
	if err := f.db.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	writes := installConfigImportPreviewRouterWriteGate(t, f.db)
	before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
	for _, endpoint := range []struct {
		method, path string
		body         []byte
	}{{http.MethodGet, path, nil}, {http.MethodPost, escalationDraftPath, []byte(strings.ReplaceAll(string(escalationDraftPayload()), "2026-10-18T10:00:00Z", time.Now().UTC().Format(time.RFC3339)))}} {
		response := configImportPreviewRouterRequest(t, f.router, endpoint.method, endpoint.path, f.tokens["admin"], endpoint.body, nil)
		if response.Code != 503 {
			t.Fatalf("overlimit status=%d body=%s", response.Code, response.Body.String())
		}
		escalationAssertSafe(t, response)
		escalationAssertUnchanged(t, f, before, writes)
	}
}

func TestEscalationPreviewRouterRateLimitZeroWrites(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	escalationSeed(t, f.db)
	writes := installConfigImportPreviewRouterWriteGate(t, f.db)
	before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
	limited := false
	for range 205 {
		response := configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], []byte(`{}`), nil)
		if response.Code == 429 {
			limited = true
			escalationAssertSafe(t, response)
			break
		}
		if response.Code != 400 {
			t.Fatalf("before rate limit=%d %s", response.Code, response.Body.String())
		}
	}
	if !limited {
		t.Fatal("preview route did not enforce 200/min rate limit")
	}
	escalationAssertUnchanged(t, f, before, writes)
}

// Pause after the policy SELECT establishes the read snapshot. The independent
// writer must commit while it is held, and all subsequent inventory must remain
// from that same snapshot rather than mixing old policy with new channel/rules.
func escalationConcurrentSnapshot(t *testing.T, f configImportPreviewRouterFixture, alert model.Alert, policy model.EscalationPolicy, postgresDB bool) {
	t.Helper()
	createdBy := escalationFixtureAdminID(t, f)
	started := make(chan struct{})
	release := make(chan struct{})
	var once, releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	name := "test:escalation-preview-snapshot"
	var isolation, readOnly string
	var inspectionErr error
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != "escalation_policies" {
			return
		}
		once.Do(func() {
			if postgresDB {
				inspectionErr = tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SHOW transaction_isolation").Scan(&isolation)
				if inspectionErr == nil {
					inspectionErr = tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SHOW transaction_read_only").Scan(&readOnly)
				}
			}
			close(started)
			<-release
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseGate(); _ = f.db.Callback().Query().Remove(name) })
	responseCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseCh <- configImportPreviewRouterRequest(t, f.router, http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), f.tokens["admin"], nil, nil)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseGate()
		t.Fatal("preview did not reach policy read boundary")
	}
	writerCh := make(chan error, 1)
	go func() {
		writerCh <- f.writer.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.EscalationPolicy{}).Where("id = ?", policy.ID).Update("name", "new policy snapshot").Error; err != nil {
				return err
			}
			if err := tx.Model(&model.Integration{}).Where("id = ?", 1).Update("enabled", false).Error; err != nil {
				return err
			}
			return tx.Create(&model.Silence{Name: "new silence snapshot", MatchTags: "[]", StartsAt: alert.TriggeredAt.Add(-time.Hour), EndsAt: time.Now().UTC().Add(time.Hour), CreatedBy: createdBy}).Error
		})
	}()
	select {
	case err := <-writerCh:
		if err != nil {
			releaseGate()
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		releaseGate()
		t.Fatal("snapshot reserved writer lock; independent writer could not commit")
	}
	committed := escalationSnapshotForDialect(t, f.writer, postgresDB)
	releaseGate()
	response := <-responseCh
	got := escalationDecode(t, response)
	if postgresDB && (inspectionErr != nil || isolation != "repeatable read" || readOnly != "on") {
		t.Fatalf("transaction isolation=%q readonly=%q error=%v", isolation, readOnly, inspectionErr)
	}
	if got.Policy.Name != policy.Name || got.Steps[0].Channels[0].State != "enabled" || got.Steps[0].Status != "would_enqueue" || len(got.Steps[0].MatchedSilenceIDs) != 0 {
		t.Fatalf("mixed snapshot=%+v", got)
	}
	_ = f.db.Callback().Query().Remove(name)
	got = escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), f.tokens["admin"], nil, nil))
	if got.Policy.Name != "new policy snapshot" || got.Steps[0].Channels[0].State != "disabled" || got.Steps[0].Status != "silenced" {
		t.Fatalf("subsequent snapshot did not see committed changes=%+v", got)
	}
	if !reflect.DeepEqual(committed, escalationSnapshotForDialect(t, f.db, postgresDB)) {
		t.Fatal("preview wrote state in addition to the independent writer transaction")
	}
}

func TestEscalationPreviewRouterSQLiteWALSnapshotConcurrentWriter(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, policy := escalationSeed(t, f.db)
	escalationConcurrentSnapshot(t, f, alert, policy, false)
}

func escalationPostgresSnapshot(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var tables []string
	if err := db.Raw("SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename").Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(tables))
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		rows, err := db.Raw("SELECT * FROM " + quoted).Rows()
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		values := []string{}
		for rows.Next() {
			raw := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			encoded := make([]string, len(raw))
			for i, value := range raw {
				encoded[i] = configImportPreviewRouterCanonicalValue(value)
			}
			values = append(values, strings.Join(encoded, "\x1f"))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(values)
		result[table] = strings.Join(values, "\x1e")
	}
	return result
}

func TestEscalationPreviewPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("REQUIRE_POSTGRES_ESCALATION_PREVIEW_TEST") == "1" {
			t.Fatal("TEST_POSTGRES_DSN required for escalation preview acceptance")
		}
		t.Skip("PostgreSQL unverified: TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be PostgreSQL URL: %v", err)
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "xirang_escalation_preview_" + hex.EncodeToString(random[:])
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := base.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
		_ = baseSQL.Close()
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	open := func(maxConnections int) *gorm.DB {
		db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(maxConnections)
		sqlDB.SetMaxIdleConns(maxConnections)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	// The migration driver retains a dedicated connection. Never reuse or
	// shrink its pool for the single-connection request cleanup assertions.
	migrationDB := open(8)
	if err := database.RunMigrations(migrationDB, "postgres"); err != nil {
		t.Fatal(err)
	}
	migrationPool, err := migrationDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrationPool.Close(); err != nil {
		t.Fatal(err)
	}
	// Fresh request/writer pools have no migration-owned connection, so the
	// next request must reuse a clean single-connection pool after cancellation.
	db, writer := open(1), open(1)
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_ESCALATION_PREVIEW_POSTGRES_ENCRYPTION_KEY_FOR_TEST_ONLY")
	gin.SetMode(gin.TestMode)
	manager := auth.NewJWTManager("FAKE_ESCALATION_PREVIEW_POSTGRES_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	tokens := map[string]string{}
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{Username: "preview-" + role, PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: role}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		token, err := manager.GenerateToken(user)
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = token
	}
	f := configImportPreviewRouterFixture{db: db, writer: writer, jwt: manager, tokens: tokens, router: NewRouter(Dependencies{DB: db, JWTManager: manager, SettingsService: settings.NewService(db)})}
	alert, policy := escalationSeed(t, db)
	before := escalationPostgresSnapshot(t, db)
	for _, endpoint := range []struct {
		method, path string
		body         []byte
	}{{http.MethodPost, escalationDraftPath, escalationDraftPayload()}, {http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), nil}} {
		for _, role := range []string{"admin", "operator", "viewer", ""} {
			want := 403
			if role == "admin" {
				want = 200
			}
			if role == "" {
				want = 401
			}
			response := configImportPreviewRouterRequest(t, f.router, endpoint.method, endpoint.path, tokens[role], endpoint.body, nil)
			if response.Code != want {
				t.Fatalf("PG role=%q status=%d body=%s", role, response.Code, response.Body.String())
			}
			escalationAssertSafe(t, response)
			if want == 200 {
				got := escalationDecode(t, response)
				if got.Status != "projected" {
					t.Fatalf("PG preview=%+v", got)
				}
			}
		}
	}
	if !reflect.DeepEqual(before, escalationPostgresSnapshot(t, db)) {
		t.Fatal("PostgreSQL preview or auth middleware wrote persistent state")
	}
	escalationConcurrentSnapshot(t, f, alert, policy, true)
	if err := db.Model(&model.Integration{}).Where("id = ?", 1).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("name = ?", "new silence snapshot").Delete(&model.Silence{}).Error; err != nil {
		t.Fatal(err)
	}
	escalationConcurrentDraftSnapshot(t, f, alert, true)
	before = escalationPostgresSnapshot(t, db)
	// Cancel inside the active raw transaction, not merely before middleware.
	ctx, cancel := context.WithCancel(context.Background())
	name := "test:escalation-preview-postgres-cancel"
	observed := false
	if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "escalation_policies" {
			observed = true
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), tokens["admin"], nil, ctx)
	_ = db.Callback().Query().Remove(name)
	cancel()
	if !observed || response.Code != 503 {
		t.Fatalf("PG cancellation observed=%v status=%d body=%s", observed, response.Code, response.Body.String())
	}
	escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), tokens["admin"], nil, nil))
	var inTransaction string
	if err := db.Raw("SHOW transaction_read_only").Scan(&inTransaction).Error; err != nil {
		t.Fatal(err)
	}
	if inTransaction != "off" {
		t.Fatalf("pool retains readonly transaction: %q", inTransaction)
	}
	if !reflect.DeepEqual(before, escalationPostgresSnapshot(t, db)) {
		t.Fatal("cancelled PG preview changed persistent state")
	}
}

func TestEscalationPreviewRouterSelectedMissingAndNodeMissing(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, policy := escalationSeed(t, f.db)
	createdBy := escalationFixtureAdminID(t, f)
	path := fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID)
	missing := uint(999999)
	// The higher selected strategy is unavailable; node fallback must not mask it.
	task := model.Task{Name: "preview selected missing", NodeID: alert.NodeID, EscalationPolicyID: &policy.ID}
	if err := f.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Update("task_id", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	// Corrupt legacy reference deliberately bypasses FK enforcement only on the
	// independent fixture writer. Production remains fully constrained.
	if err := f.writer.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.writer.Model(&model.Task{}).Where("id = ?", task.ID).Update("escalation_policy_id", missing).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.writer.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}
	before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
	response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil)
	if response.Code != 404 {
		t.Fatalf("selected missing status=%d body=%s", response.Code, response.Body.String())
	}
	escalationAssertUnchanged(t, f, before, nil)
	if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Updates(map[string]any{"task_id": nil, "node_id": missing}).Error; err != nil {
		t.Fatal(err)
	}
	slo := model.SLODefinition{Name: "preview missing node SLO", MetricType: "success_rate", Threshold: 0.9, EscalationPolicyID: &policy.ID, CreatedBy: createdBy}
	if err := f.db.Create(&slo).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Update("slo_id", slo.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.Silence{Name: "node-tags-only", MatchTags: `["prod"]`, StartsAt: alert.TriggeredAt.Add(-time.Hour), EndsAt: time.Now().UTC().Add(time.Hour), CreatedBy: createdBy}).Error; err != nil {
		t.Fatal(err)
	}
	before = configImportPreviewRouterDatabaseSnapshot(t, f.db)
	got := escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodGet, path, f.tokens["admin"], nil, nil))
	if got.Status != "projected" || got.PolicySource != "slo" || !containsEscalationWarning(got.Warnings, "node_missing") || got.Steps[0].Status != "would_enqueue" {
		t.Fatalf("missing node snapshot=%+v", got)
	}
	escalationAssertUnchanged(t, f, before, nil)
}

func TestEscalationPreviewRouterCancellationInsideSQLiteSnapshot(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, _ := escalationSeed(t, f.db)
	writes := installConfigImportPreviewRouterWriteGate(t, f.db)
	before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	name := "test:escalation-preview-sqlite-cancel"
	observed := false
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "escalation_policies" {
			observed = true
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), f.tokens["admin"], nil, ctx)
	_ = f.db.Callback().Query().Remove(name)
	if !observed || response.Code != 503 {
		t.Fatalf("active cancellation observed=%v status=%d body=%s", observed, response.Code, response.Body.String())
	}
	escalationAssertSafe(t, response)
	escalationAssertUnchanged(t, f, before, writes)
	escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], escalationDraftPayload(), nil))
	escalationAssertUnchanged(t, f, before, writes)
}

func TestEscalationPreviewRouterEverySnapshotQueryFailureIsSafe(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, _ := escalationSeed(t, f.db)
	backup := model.Policy{Name: "preview query backup", SourcePath: "/a", TargetPath: "/b", CronSpec: "* * * * *"}
	if err := f.db.Create(&backup).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{Name: "preview query task", NodeID: alert.NodeID, PolicyID: &backup.ID}
	if err := f.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	slo := model.SLODefinition{Name: "preview query SLO", MetricType: "success_rate", Threshold: 0.9, CreatedBy: escalationFixtureAdminID(t, f)}
	if err := f.db.Create(&slo).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Alert{}).Where("id = ?", alert.ID).Updates(map[string]any{"task_id": task.ID, "slo_id": slo.ID}).Error; err != nil {
		t.Fatal(err)
	}
	writes := installConfigImportPreviewRouterWriteGate(t, f.db)
	before := configImportPreviewRouterDatabaseSnapshot(t, f.db)
	for _, table := range []string{"alerts", "tasks", "policies", "slo_definitions", "nodes", "escalation_policies", "alert_escalation_events", "alert_deliveries", "integrations", "silences"} {
		injected := errors.New("PRIVATE_QUERY_FAILURE_" + table)
		observed := false
		callback := func(tx *gorm.DB) {
			if tx.Statement.Table == table {
				observed = true
				_ = tx.AddError(injected)
			}
		}
		name := "test:escalation-preview-query-" + table
		if err := f.db.Callback().Query().Before("gorm:query").Register(name, callback); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Callback().Row().Before("gorm:row").Register(name, callback); err != nil {
			t.Fatal(err)
		}
		response := configImportPreviewRouterRequest(t, f.router, http.MethodGet, fmt.Sprintf("/api/v1/alerts/%d/escalation-preview", alert.ID), f.tokens["admin"], nil, nil)
		_ = f.db.Callback().Query().Remove(name)
		_ = f.db.Callback().Row().Remove(name)
		if !observed || response.Code != 500 || strings.Contains(response.Body.String(), injected.Error()) {
			t.Fatalf("table=%s observed=%v status=%d body=%s", table, observed, response.Code, response.Body.String())
		}
		escalationAssertSafe(t, response)
		escalationAssertUnchanged(t, f, before, writes)
	}
}

func escalationConcurrentDraftSnapshot(t *testing.T, f configImportPreviewRouterFixture, alert model.Alert, postgresDB bool) {
	t.Helper()
	createdBy := escalationFixtureAdminID(t, f)
	started, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	name := "test:escalation-preview-draft-snapshot"
	var isolation, readOnly string
	var inspectionErr error
	if err := f.db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != "nodes" {
			return
		}
		once.Do(func() {
			if postgresDB {
				inspectionErr = tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SHOW transaction_isolation").Scan(&isolation)
				if inspectionErr == nil {
					inspectionErr = tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "SHOW transaction_read_only").Scan(&readOnly)
				}
			}
			close(started)
			<-release
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseGate(); _ = f.db.Callback().Query().Remove(name) })
	now := time.Now().UTC().Truncate(time.Second)
	body := strings.Replace(string(escalationDraftPayload()), `"node_id":0`, fmt.Sprintf(`"node_id":%d`, alert.NodeID), 1)
	body = strings.ReplaceAll(body, "2026-10-18T10:00:00Z", now.Format(time.RFC3339))
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responses <- configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], []byte(body), nil)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseGate()
		t.Fatal("draft did not establish its node read snapshot")
	}
	written := make(chan error, 1)
	go func() {
		written <- f.writer.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.Integration{}).Where("id = ?", 1).Update("enabled", false).Error; err != nil {
				return err
			}
			return tx.Create(&model.Silence{Name: "new draft silence snapshot", MatchTags: "[]", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), CreatedBy: createdBy}).Error
		})
	}()
	select {
	case err := <-written:
		if err != nil {
			releaseGate()
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		releaseGate()
		t.Fatal("draft snapshot prevented independent writer commit")
	}
	committed := escalationSnapshotForDialect(t, f.writer, postgresDB)
	releaseGate()
	got := escalationDecode(t, <-responses)
	if postgresDB && (inspectionErr != nil || isolation != "repeatable read" || readOnly != "on") {
		t.Fatalf("draft transaction isolation=%q readonly=%q err=%v", isolation, readOnly, inspectionErr)
	}
	if got.Status != "projected" || got.Steps[0].Channels[0].State != "enabled" || got.Steps[0].Status != "would_enqueue" {
		t.Fatalf("draft mixed snapshot=%+v", got)
	}
	_ = f.db.Callback().Query().Remove(name)
	got = escalationDecode(t, configImportPreviewRouterRequest(t, f.router, http.MethodPost, escalationDraftPath, f.tokens["admin"], []byte(body), nil))
	if got.Steps[0].Channels[0].State != "disabled" || got.Steps[0].Status != "silenced" {
		t.Fatalf("draft did not observe subsequent commit=%+v", got)
	}
	if !reflect.DeepEqual(committed, escalationSnapshotForDialect(t, f.db, postgresDB)) {
		t.Fatal("draft preview wrote state in addition to the independent writer transaction")
	}
}

func TestEscalationPreviewRouterSQLiteDraftSnapshotConcurrentWriter(t *testing.T) {
	f := setupConfigImportPreviewRouterFixture(t)
	alert, _ := escalationSeed(t, f.db)
	escalationConcurrentDraftSnapshot(t, f, alert, false)
}

func escalationSnapshotForDialect(t *testing.T, db *gorm.DB, postgresDB bool) map[string]string {
	t.Helper()
	if postgresDB {
		return escalationPostgresSnapshot(t, db)
	}
	return configImportPreviewRouterDatabaseSnapshot(t, db)
}
