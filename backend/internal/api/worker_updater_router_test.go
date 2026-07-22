package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing/updater"
)

func TestUpdaterConnContextUsesOnlyAuthenticatedUpdaterConnection(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	identity := updater.UpdaterTransportIdentity{
		Fingerprint: strings.Repeat("a", 64), PeerPID: 0, PeerUID: 10002, PeerGID: 10002,
	}
	ctx := UpdaterConnContext(context.Background(), updaterRouterAuthenticatedConn{Conn: left, identity: identity})
	if got, ok := updaterIdentityFromContext(ctx); !ok || got != identity {
		t.Fatalf("authenticated updater identity=%+v ok=%t", got, ok)
	}
	if got, ok := updaterIdentityFromContext(UpdaterConnContext(context.Background(), right)); ok {
		t.Fatalf("plain connection established updater identity: %+v", got)
	}
}

func TestWorkerUpdaterRouterIsDedicatedStrictAndTransportAuthenticated(t *testing.T) {
	service := &workerUpdaterProtocolFake{
		register: updater.RegisterCandidateResult{SchemaVersion: 1, CandidateID: strings.Repeat("1", 32)},
		pull:     updater.PullActivationResult{SchemaVersion: 1, RetryAfterSeconds: 5},
		report:   updater.ActivationReportResult{SchemaVersion: 1, Decision: updater.ActivationDecisionCommit, ActiveFingerprint: strings.Repeat("2", 64)},
	}
	router, err := NewWorkerUpdaterRouter(service, WorkerUpdaterRouterConfig{JSONMaxBytes: 4096, Now: func() time.Time {
		return time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	identity := updater.UpdaterTransportIdentity{
		Fingerprint: strings.Repeat("a", 64), PeerPID: 0, PeerUID: 10002, PeerGID: 10002,
	}
	payload := `{"schema_version":1,"active_fingerprint":""}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker-updater/activations/pull", strings.NewReader(payload))
	request = request.WithContext(ContextWithUpdaterTransportIdentity(request.Context(), identity))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.pullCalls != 1 || service.identity != identity ||
		!strings.Contains(response.Body.String(), `"retry_after_seconds":5`) {
		t.Fatalf("pull status=%d calls=%d identity=%+v body=%s", response.Code, service.pullCalls, service.identity, response.Body.String())
	}

	for _, mutate := range []func(*http.Request) *http.Request{
		func(request *http.Request) *http.Request {
			request.Header.Set("Authorization", "Bearer FAKE_UPDATER_TOKEN")
			request.Header.Set("X-Forwarded-For", "127.0.0.1")
			return request
		},
		func(request *http.Request) *http.Request {
			return request.WithContext(context.Background())
		},
	} {
		unauthenticated := httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker-updater/activations/pull", strings.NewReader(payload))
		unauthenticated = mutate(unauthenticated)
		response = httptest.NewRecorder()
		router.ServeHTTP(response, unauthenticated)
		if response.Code != http.StatusUnauthorized || service.pullCalls != 1 || strings.Contains(response.Body.String(), "FAKE_UPDATER_TOKEN") {
			t.Fatalf("unauthenticated status=%d calls=%d body=%s", response.Code, service.pullCalls, response.Body.String())
		}
	}

	negativePID := httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker-updater/activations/pull", strings.NewReader(payload))
	invalidIdentity := identity
	invalidIdentity.PeerPID = -1
	negativePID = negativePID.WithContext(ContextWithUpdaterTransportIdentity(negativePID.Context(), invalidIdentity))
	negativePID.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, negativePID)
	if response.Code != http.StatusUnauthorized || service.pullCalls != 1 {
		t.Fatalf("negative PID status=%d calls=%d body=%s", response.Code, service.pullCalls, response.Body.String())
	}

	for _, invalid := range []string{
		`{"schema_version":1,"active_fingerprint":"","active_fingerprint":""}`,
		`{"schema_version":1,"active_fingerprint":"","url":"https://forbidden"}`,
		payload + `{}`,
	} {
		request = httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker-updater/activations/pull", strings.NewReader(invalid))
		request = request.WithContext(ContextWithUpdaterTransportIdentity(request.Context(), identity))
		request.Header.Set("Content-Type", "application/json")
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || service.pullCalls != 1 {
			t.Fatalf("invalid status=%d calls=%d body=%s", response.Code, service.pullCalls, response.Body.String())
		}
	}

	publicRoutes := NewRouter(Dependencies{}).Routes()
	for _, route := range publicRoutes {
		if strings.Contains(route.Path, "/asset-worker-updater/") {
			t.Fatalf("updater route exposed on public router: %s %s", route.Method, route.Path)
		}
	}
}

type updaterRouterAuthenticatedConn struct {
	net.Conn
	identity updater.UpdaterTransportIdentity
}

func (connection updaterRouterAuthenticatedConn) UpdaterIdentity() updater.UpdaterTransportIdentity {
	return connection.identity
}

type workerUpdaterProtocolFake struct {
	register      updater.RegisterCandidateResult
	pull          updater.PullActivationResult
	report        updater.ActivationReportResult
	identity      updater.UpdaterTransportIdentity
	registerCalls int
	pullCalls     int
	reportCalls   int
}

func (fake *workerUpdaterProtocolFake) RegisterUpdaterCandidate(
	_ context.Context,
	identity updater.UpdaterTransportIdentity,
	_ updater.RegisterCandidateRequest,
) (updater.RegisterCandidateResult, error) {
	fake.identity = identity
	fake.registerCalls++
	return fake.register, nil
}

func (fake *workerUpdaterProtocolFake) PullUpdaterActivation(
	_ context.Context,
	identity updater.UpdaterTransportIdentity,
	_ updater.PullActivationRequest,
) (updater.PullActivationResult, error) {
	fake.identity = identity
	fake.pullCalls++
	return fake.pull, nil
}

func (fake *workerUpdaterProtocolFake) ReportUpdaterActivation(
	_ context.Context,
	identity updater.UpdaterTransportIdentity,
	_ updater.ActivationReportRequest,
) (updater.ActivationReportResult, error) {
	fake.identity = identity
	fake.reportCalls++
	return fake.report, nil
}
