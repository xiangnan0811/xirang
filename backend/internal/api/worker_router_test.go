package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
)

func TestWorkerConnContextUsesOnlyAuthenticatedConnectionIdentity(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	t.Cleanup(func() {
		_ = serverSide.Close()
		_ = clientSide.Close()
	})
	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64), PeerUID: 1000,
	}
	authenticated := workerRouterAuthenticatedConn{Conn: serverSide, identity: identity}
	ctx := WorkerConnContext(context.Background(), authenticated)
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker/leases/pull", nil).WithContext(ctx)
	got, ok := workerIdentity(request)
	if !ok || got != identity {
		t.Fatalf("authenticated connection identity got=%+v ok=%t", got, ok)
	}

	ctx = WorkerConnContext(context.Background(), clientSide)
	request = request.WithContext(ctx)
	if got, ok = workerIdentity(request); ok {
		t.Fatalf("plain connection established Worker identity: %+v", got)
	}
}

type workerRouterAuthenticatedConn struct {
	net.Conn
	identity processing.WorkerTransportIdentity
}

func (connection workerRouterAuthenticatedConn) WorkerIdentity() processing.WorkerTransportIdentity {
	return connection.identity
}

func TestWorkerRouterIsDedicatedStrictAndTransportAuthenticated(t *testing.T) {
	service := &workerProtocolAPIFake{
		pullResult: processing.WorkerJobEnvelope{
			SchemaVersion: 1, ProtocolVersion: processing.WorkerProtocolVersion,
			JobID: strings.Repeat("1", 32), AttemptID: strings.Repeat("2", 32),
		},
	}
	router, err := NewWorkerRouter(service, WorkerRouterConfig{JSONMaxBytes: 512, ArtifactMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64), PeerUID: 1000,
	}
	payload := `{"schema_version":1,"worker_id":"` + strings.Repeat("b", 32) + `","instance_id":"` + strings.Repeat("c", 32) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker/leases/pull", strings.NewReader(payload))
	request = request.WithContext(ContextWithWorkerTransportIdentity(request.Context(), identity))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://browser.example")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || service.pullCalls != 1 || service.identity != identity ||
		service.pullRequest.WorkerID != strings.Repeat("b", 32) {
		t.Fatalf("authenticated pull status=%d calls=%d identity=%+v request=%+v body=%s",
			recorder.Code, service.pullCalls, service.identity, service.pullRequest, recorder.Body.String())
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("dedicated Worker router emitted browser CORS: %v", recorder.Header())
	}

	unauthenticated := httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker/leases/pull", strings.NewReader(payload))
	unauthenticated.Header.Set("Authorization", "Bearer FAKE_WORKER_TOKEN_FOR_TEST_ONLY")
	unauthenticated.Header.Set("X-Forwarded-For", "127.0.0.1")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, unauthenticated)
	if recorder.Code != http.StatusUnauthorized || service.pullCalls != 1 ||
		strings.Contains(recorder.Body.String(), "FAKE_WORKER_TOKEN_FOR_TEST_ONLY") {
		t.Fatalf("header identity bypass status=%d calls=%d body=%s", recorder.Code, service.pullCalls, recorder.Body.String())
	}

	invalidPayloads := []string{
		`{"schema_version":1,"worker_id":"` + strings.Repeat("b", 32) + `","worker_id":"` + strings.Repeat("b", 32) + `","instance_id":"` + strings.Repeat("c", 32) + `"}`,
		`{"schema_version":1,"worker_id":"` + strings.Repeat("b", 32) + `","instance_id":"` + strings.Repeat("c", 32) + `","unknown":true}`,
		payload + ` {}`,
	}
	for index, invalid := range invalidPayloads {
		request = httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker/leases/pull", strings.NewReader(invalid))
		request = request.WithContext(ContextWithWorkerTransportIdentity(request.Context(), identity))
		recorder = httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest || service.pullCalls != 1 {
			t.Fatalf("invalid payload %d status=%d calls=%d body=%s", index, recorder.Code, service.pullCalls, recorder.Body.String())
		}
	}

	request = httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker/leases/pull", strings.NewReader(strings.Repeat("x", 513)))
	request = request.WithContext(ContextWithWorkerTransportIdentity(request.Context(), identity))
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge || !strings.Contains(recorder.Body.String(), `"body_too_large"`) || service.pullCalls != 1 {
		t.Fatalf("oversized payload status=%d calls=%d body=%s", recorder.Code, service.pullCalls, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/asset-worker/leases/pull", strings.NewReader(payload))
	request = request.WithContext(ContextWithWorkerTransportIdentity(request.Context(), identity))
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || service.pullCalls != 1 {
		t.Fatalf("Worker route escaped dedicated surface: status=%d calls=%d", recorder.Code, service.pullCalls)
	}
}

func TestWorkerRouterHandshakeAndJobControlsUseFixedPaths(t *testing.T) {
	service := &workerProtocolAPIFake{
		handshakeResult: processing.HandshakeResult{
			WorkerID: strings.Repeat("1", 32), TrustState: "active", HealthState: "ready", CapabilityCount: 1,
		},
		heartbeatResult: processing.WorkerHeartbeatResult{
			SchemaVersion: 1, EffectiveLeaseExpiresAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
		},
		transitionResult: processing.WorkerTransitionResult{
			SchemaVersion: 1, State: processing.ProcessingProcessing, Revision: 4,
		},
	}
	router, err := NewWorkerRouter(service, WorkerRouterConfig{JSONMaxBytes: 64 << 10, ArtifactMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64), PeerUID: 1000,
	}
	handshake := `{"schema_version":1,"protocol_version":1,"instance_id":"` + strings.Repeat("2", 32) +
		`","identity_revision":1,"interactive_slots":1,"background_slots":1,"capabilities":[{` +
		`"schema_version":1,"capability":"noop","capability_schema":"noop.v1","pipeline_fingerprint":"noop-pipeline-v1",` +
		`"output_profile":"noop.v1","input_modes":["stat","sequential","range"],"limits":{` +
		`"max_input_bytes":1048576,"max_output_bytes":1048576,"max_output_count":1,"max_pages":1,"max_pixels":1,` +
		`"max_duration_millis":1000,"max_expanded_bytes":1048576}}]}`
	response := performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/handshake", handshake)
	if response.Code != http.StatusOK || service.handshakeCalls != 1 || service.identity != identity ||
		service.handshakeRequest.InstanceID != strings.Repeat("2", 32) {
		t.Fatalf("handshake status=%d calls=%d request=%+v body=%s", response.Code, service.handshakeCalls, service.handshakeRequest, response.Body.String())
	}

	jobID := strings.Repeat("3", 32)
	attemptID := strings.Repeat("4", 32)
	workerID := strings.Repeat("1", 32)
	instanceID := strings.Repeat("2", 32)
	heartbeat := `{"schema_version":1,"worker_id":"` + workerID + `","instance_id":"` + instanceID + `","attempt_id":"` + attemptID + `"}`
	response = performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/jobs/"+jobID+"/heartbeat", heartbeat)
	if response.Code != http.StatusOK || service.heartbeatCalls != 1 || service.heartbeatRequest.AttemptID != attemptID {
		t.Fatalf("heartbeat status=%d calls=%d request=%+v body=%s", response.Code, service.heartbeatCalls, service.heartbeatRequest, response.Body.String())
	}

	transition := `{"schema_version":1,"worker_id":"` + workerID + `","instance_id":"` + instanceID +
		`","job_id":"` + jobID + `","attempt_id":"` + attemptID +
		`","expected_revision":3,"to":"processing","error_code":"","retry_at":null,"cancel_reason":"","supersede_reason":"","expiry_reason":""}`
	response = performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/jobs/"+jobID+"/transitions", transition)
	if response.Code != http.StatusOK || service.transitionCalls != 1 || service.transitionRequest.JobID != jobID || service.transitionRequest.To != processing.ProcessingProcessing {
		t.Fatalf("transition status=%d calls=%d request=%+v body=%s", response.Code, service.transitionCalls, service.transitionRequest, response.Body.String())
	}

	wrongPath := strings.Repeat("5", 32)
	response = performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/jobs/"+wrongPath+"/transitions", transition)
	if response.Code != http.StatusBadRequest || service.transitionCalls != 1 {
		t.Fatalf("path/body mismatch status=%d calls=%d body=%s", response.Code, service.transitionCalls, response.Body.String())
	}
}

func TestWorkerRouterHandshakeUsesConfiguredBodyLimit(t *testing.T) {
	service := &workerProtocolAPIFake{}
	router, err := NewWorkerRouter(service, WorkerRouterConfig{JSONMaxBytes: 64, ArtifactMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64), PeerUID: 1000,
	}
	response := performWorkerRequest(router, identity, http.MethodPost,
		"/internal/v1/asset-worker/handshake", strings.Repeat("x", 65))
	if response.Code != http.StatusRequestEntityTooLarge || service.handshakeCalls != 0 {
		t.Fatalf("configured handshake limit status=%d calls=%d body=%s", response.Code, service.handshakeCalls, response.Body.String())
	}
}

func TestWorkerRouterStreamsBoundedInputArtifactAndOneManifest(t *testing.T) {
	jobID := strings.Repeat("1", 32)
	attemptID := strings.Repeat("2", 32)
	workerID := strings.Repeat("3", 32)
	instanceID := strings.Repeat("4", 32)
	inputSessionID := strings.Repeat("5", 32)
	sinkSessionID := strings.Repeat("6", 32)
	service := &workerProtocolAPIFake{
		inputActivationResult: processing.WorkerInputActivation{
			SchemaVersion: 1, SessionID: inputSessionID,
		},
		inputPayload: []byte("bounded-range"),
		sinkActivationResult: processing.WorkerSinkActivation{
			SchemaVersion: 1, SessionID: sinkSessionID, MaxArtifacts: 4, MaxArtifactBytes: 1024, MaxTotalBytes: 4096, MaxInFlight: 1,
		},
		uploadResult: processing.UploadedArtifact{
			UploadID: strings.Repeat("7", 32), BlobID: strings.Repeat("8", 32), Ordinal: 0,
		},
		manifestResult: processing.WorkerCommitManifestResult{
			SchemaVersion: 1, ArtifactSetID: strings.Repeat("9", 32), ManifestDigest: strings.Repeat("a", 64),
		},
	}
	router, err := NewWorkerRouter(service, WorkerRouterConfig{JSONMaxBytes: 64 << 10, ArtifactMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("b", 64), PeerUID: 1000,
	}
	inputActivation := `{"schema_version":1,"worker_id":"` + workerID + `","instance_id":"` + instanceID +
		`","job_id":"` + jobID + `","attempt_id":"` + attemptID + `","expected_revision":2,"grant_id":"` +
		inputSessionID + `","secret":"` + strings.Repeat("c", 64) + `"}`
	response := performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/jobs/"+jobID+"/input/activate", inputActivation)
	if response.Code != http.StatusOK || service.inputActivationCalls != 1 || service.inputActivationRequest.JobID != jobID {
		t.Fatalf("input activation status=%d calls=%d request=%+v body=%s", response.Code, service.inputActivationCalls, service.inputActivationRequest, response.Body.String())
	}

	read := `{"schema_version":1,"worker_id":"` + workerID + `","instance_id":"` + instanceID +
		`","session_id":"` + inputSessionID + `","mode":"range","offset":2,"length":4}`
	response = performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/input-sessions/"+inputSessionID+"/ranges", read)
	if response.Code != http.StatusOK || response.Body.String() != "bounded-range" ||
		response.Header().Get("Content-Type") != "application/octet-stream" || service.inputReadCalls != 1 {
		t.Fatalf("input stream status=%d calls=%d headers=%v body=%q", response.Code, service.inputReadCalls, response.Header(), response.Body.String())
	}

	sinkActivation := `{"schema_version":1,"worker_id":"` + workerID + `","instance_id":"` + instanceID +
		`","job_id":"` + jobID + `","attempt_id":"` + attemptID + `","expected_revision":5,"grant_id":"` +
		sinkSessionID + `","secret":"` + strings.Repeat("d", 64) + `"}`
	response = performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/jobs/"+jobID+"/sink/activate", sinkActivation)
	if response.Code != http.StatusOK || service.sinkActivationCalls != 1 || service.sinkActivationRequest.JobID != jobID {
		t.Fatalf("Sink activation status=%d calls=%d request=%+v body=%s", response.Code, service.sinkActivationCalls, service.sinkActivationRequest, response.Body.String())
	}

	artifactPayload := []byte("passive-noop")
	metadata := `{"schema_version":1,"worker_id":"` + workerID + `","instance_id":"` + instanceID +
		`","session_id":"` + sinkSessionID + `","job_id":"` + jobID + `","attempt_id":"` + attemptID + `","artifact":{` +
		`"ordinal":0,"role":"noop","media_type":"application/octet-stream","plaintext_size":12,"plaintext_digest":"` +
		strings.Repeat("e", 64) + `","completeness":"complete","coverage_canonical":"eyJzY2hlbWFfdmVyc2lvbiI6MSwia2luZCI6ImFsbCJ9"}}`
	body := &bytes.Buffer{}
	multipartWriter := multipart.NewWriter(body)
	metadataPart, err := multipartWriter.CreateFormField("metadata")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(metadataPart, metadata)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="content"`)
	header.Set("Content-Type", "application/octet-stream")
	contentPart, err := multipartWriter.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = contentPart.Write(artifactPayload)
	if err := multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker/sink-sessions/"+sinkSessionID+"/artifacts", body)
	request = request.WithContext(ContextWithWorkerTransportIdentity(request.Context(), identity))
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.uploadCalls != 1 || string(service.uploadPayload) != string(artifactPayload) ||
		service.uploadRequest.SessionID != sinkSessionID || service.uploadRequest.Artifact.MediaType != "application/octet-stream" {
		t.Fatalf("artifact stream status=%d calls=%d request=%+v payload=%q body=%s",
			response.Code, service.uploadCalls, service.uploadRequest, service.uploadPayload, response.Body.String())
	}

	manifestRequest := processing.WorkerCommitManifestRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, SessionID: sinkSessionID,
		JobID: jobID, AttemptID: attemptID, SecurityPolicyRevision: "security-policy-v1",
		Artifacts: []processing.ArtifactDeclaration{service.uploadRequest.Artifact},
	}
	manifestPayload, err := json.Marshal(manifestRequest)
	if err != nil {
		t.Fatal(err)
	}
	response = performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/sink-sessions/"+sinkSessionID+"/manifest", string(manifestPayload))
	if response.Code != http.StatusOK || service.manifestCalls != 1 || service.manifestRequest.SessionID != sinkSessionID ||
		len(service.manifestRequest.Artifacts) != 1 {
		t.Fatalf("manifest status=%d calls=%d request=%+v body=%s", response.Code, service.manifestCalls, service.manifestRequest, response.Body.String())
	}

	drain := `{"schema_version":1,"worker_id":"` + workerID + `","instance_id":"` + instanceID + `"}`
	response = performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/drain", drain)
	if response.Code != http.StatusOK || service.drainCalls != 1 {
		t.Fatalf("drain status=%d calls=%d body=%s", response.Code, service.drainCalls, response.Body.String())
	}
}

func TestWorkerRouterRejectsArtifactOverflowAndTrailingPartsBeforeServiceSuccess(t *testing.T) {
	sessionID := strings.Repeat("6", 32)
	workerID := strings.Repeat("3", 32)
	instanceID := strings.Repeat("4", 32)
	jobID := strings.Repeat("1", 32)
	attemptID := strings.Repeat("2", 32)
	metadata := `{"schema_version":1,"worker_id":"` + workerID + `","instance_id":"` + instanceID +
		`","session_id":"` + sessionID + `","job_id":"` + jobID + `","attempt_id":"` + attemptID + `","artifact":{` +
		`"ordinal":0,"role":"noop","media_type":"application/octet-stream","plaintext_size":4,"plaintext_digest":"` +
		strings.Repeat("e", 64) + `","completeness":"complete","coverage_canonical":"eyJzY2hlbWFfdmVyc2lvbiI6MSwia2luZCI6ImFsbCJ9"}}`
	service := &workerProtocolAPIFake{uploadResult: processing.UploadedArtifact{
		UploadID: strings.Repeat("7", 32), BlobID: strings.Repeat("8", 32), Ordinal: 0,
	}}
	router, err := NewWorkerRouter(service, WorkerRouterConfig{JSONMaxBytes: 64 << 10, ArtifactMaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	identity := processing.WorkerTransportIdentity{Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("b", 64), PeerUID: 1000}

	contentType, body := workerArtifactMultipart(t, metadata, []byte("four"), []byte("trailing"))
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker/sink-sessions/"+sessionID+"/artifacts", body)
	request = request.WithContext(ContextWithWorkerTransportIdentity(request.Context(), identity))
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || service.uploadReadErr == nil {
		t.Fatalf("trailing part reached service success status=%d readErr=%v body=%s", recorder.Code, service.uploadReadErr, recorder.Body.String())
	}

	service.uploadReadErr = nil
	contentType, body = workerArtifactMultipart(t, metadata, []byte("five!"), nil)
	request = httptest.NewRequest(http.MethodPost, "/internal/v1/asset-worker/sink-sessions/"+sessionID+"/artifacts", body)
	request = request.WithContext(ContextWithWorkerTransportIdentity(request.Context(), identity))
	request.Header.Set("Content-Type", contentType)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge || service.uploadReadErr == nil {
		t.Fatalf("overflow reached service success status=%d readErr=%v body=%s", recorder.Code, service.uploadReadErr, recorder.Body.String())
	}
}

func workerArtifactMultipart(t *testing.T, metadata string, content, trailing []byte) (string, *bytes.Buffer) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	metadataPart, err := writer.CreateFormField("metadata")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(metadataPart, metadata)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="content"`)
	header.Set("Content-Type", "application/octet-stream")
	contentPart, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = contentPart.Write(content)
	if trailing != nil {
		trailingPart, err := writer.CreateFormField("trailing")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = trailingPart.Write(trailing)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), body
}

func TestWorkerRouterEnforcesFixedPerIdentityRateLimits(t *testing.T) {
	service := &workerProtocolAPIFake{pullResult: processing.WorkerJobEnvelope{SchemaVersion: 1}}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	router, err := NewWorkerRouter(service, WorkerRouterConfig{
		JSONMaxBytes: 512, ArtifactMaxBytes: 1024, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64), PeerUID: 1000,
	}
	payload := `{"schema_version":1,"worker_id":"` + strings.Repeat("b", 32) + `","instance_id":"` + strings.Repeat("c", 32) + `"}`
	for requestIndex := 0; requestIndex < 120; requestIndex++ {
		response := performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/leases/pull", payload)
		if response.Code != http.StatusOK {
			t.Fatalf("pull %d unexpectedly limited: status=%d body=%s", requestIndex, response.Code, response.Body.String())
		}
	}
	response := performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/leases/pull", payload)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" ||
		!strings.Contains(response.Body.String(), `"rate_limited"`) || service.pullCalls != 120 {
		t.Fatalf("rate limit status=%d retry=%q calls=%d body=%s", response.Code, response.Header().Get("Retry-After"), service.pullCalls, response.Body.String())
	}
	other := identity
	other.Fingerprint = strings.Repeat("d", 64)
	response = performWorkerRequest(router, other, http.MethodPost, "/internal/v1/asset-worker/leases/pull", payload)
	if response.Code != http.StatusOK || service.pullCalls != 121 {
		t.Fatalf("rate limit crossed identities: status=%d calls=%d body=%s", response.Code, service.pullCalls, response.Body.String())
	}
	now = now.Add(time.Minute)
	response = performWorkerRequest(router, identity, http.MethodPost, "/internal/v1/asset-worker/leases/pull", payload)
	if response.Code != http.StatusOK || service.pullCalls != 122 {
		t.Fatalf("rate window did not reset: status=%d calls=%d body=%s", response.Code, service.pullCalls, response.Body.String())
	}
}

func performWorkerRequest(handler http.Handler, identity processing.WorkerTransportIdentity, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request = request.WithContext(ContextWithWorkerTransportIdentity(request.Context(), identity))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type workerProtocolAPIFake struct {
	identity               processing.WorkerTransportIdentity
	handshakeRequest       processing.HandshakeRequest
	handshakeResult        processing.HandshakeResult
	handshakeErr           error
	handshakeCalls         int
	pullRequest            processing.WorkerPullRequest
	pullResult             processing.WorkerJobEnvelope
	pullErr                error
	pullCalls              int
	heartbeatRequest       processing.WorkerHeartbeatRequest
	heartbeatResult        processing.WorkerHeartbeatResult
	heartbeatErr           error
	heartbeatCalls         int
	transitionRequest      processing.WorkerTransitionRequest
	transitionResult       processing.WorkerTransitionResult
	transitionErr          error
	transitionCalls        int
	inputActivationRequest processing.WorkerInputActivateRequest
	inputActivationResult  processing.WorkerInputActivation
	inputActivationErr     error
	inputActivationCalls   int
	inputReadRequest       processing.WorkerInputReadRequest
	inputPayload           []byte
	inputReadErr           error
	inputReadCalls         int
	sinkActivationRequest  processing.WorkerSinkActivateRequest
	sinkActivationResult   processing.WorkerSinkActivation
	sinkActivationErr      error
	sinkActivationCalls    int
	uploadRequest          processing.WorkerUploadArtifactRequest
	uploadPayload          []byte
	uploadReadErr          error
	uploadResult           processing.UploadedArtifact
	uploadErr              error
	uploadCalls            int
	manifestRequest        processing.WorkerCommitManifestRequest
	manifestResult         processing.WorkerCommitManifestResult
	manifestErr            error
	manifestCalls          int
	drainRequest           processing.WorkerDrainRequest
	drainErr               error
	drainCalls             int
}

func (fake *workerProtocolAPIFake) Handshake(_ context.Context, identity processing.WorkerTransportIdentity, request processing.HandshakeRequest) (processing.HandshakeResult, error) {
	fake.identity = identity
	fake.handshakeRequest = request
	fake.handshakeCalls++
	return fake.handshakeResult, fake.handshakeErr
}

func (fake *workerProtocolAPIFake) Pull(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerPullRequest) (processing.WorkerJobEnvelope, error) {
	fake.identity = identity
	fake.pullRequest = request
	fake.pullCalls++
	return fake.pullResult, fake.pullErr
}

func (fake *workerProtocolAPIFake) Heartbeat(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerHeartbeatRequest) (processing.WorkerHeartbeatResult, error) {
	fake.identity = identity
	fake.heartbeatRequest = request
	fake.heartbeatCalls++
	return fake.heartbeatResult, fake.heartbeatErr
}

func (fake *workerProtocolAPIFake) Transition(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerTransitionRequest) (processing.WorkerTransitionResult, error) {
	fake.identity = identity
	fake.transitionRequest = request
	fake.transitionCalls++
	return fake.transitionResult, fake.transitionErr
}

func (fake *workerProtocolAPIFake) ActivateInput(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerInputActivateRequest) (processing.WorkerInputActivation, error) {
	fake.identity = identity
	fake.inputActivationRequest = request
	fake.inputActivationCalls++
	return fake.inputActivationResult, fake.inputActivationErr
}

func (fake *workerProtocolAPIFake) OpenInput(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerInputReadRequest) (content.AttemptReadHandle, error) {
	fake.identity = identity
	fake.inputReadRequest = request
	fake.inputReadCalls++
	if fake.inputReadErr != nil {
		return nil, fake.inputReadErr
	}
	return &workerRouterReadHandle{Reader: bytes.NewReader(fake.inputPayload)}, nil
}

func (fake *workerProtocolAPIFake) ActivateSink(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerSinkActivateRequest) (processing.WorkerSinkActivation, error) {
	fake.identity = identity
	fake.sinkActivationRequest = request
	fake.sinkActivationCalls++
	return fake.sinkActivationResult, fake.sinkActivationErr
}

func (fake *workerProtocolAPIFake) UploadArtifact(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerUploadArtifactRequest, body io.Reader) (processing.UploadedArtifact, error) {
	fake.identity = identity
	fake.uploadRequest = request
	fake.uploadCalls++
	payload, err := io.ReadAll(body)
	fake.uploadPayload = payload
	fake.uploadReadErr = err
	if err != nil {
		return processing.UploadedArtifact{}, err
	}
	return fake.uploadResult, fake.uploadErr
}

func (fake *workerProtocolAPIFake) CommitManifest(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerCommitManifestRequest) (processing.WorkerCommitManifestResult, error) {
	fake.identity = identity
	fake.manifestRequest = request
	fake.manifestCalls++
	return fake.manifestResult, fake.manifestErr
}

func (fake *workerProtocolAPIFake) Drain(_ context.Context, identity processing.WorkerTransportIdentity, request processing.WorkerDrainRequest) error {
	fake.identity = identity
	fake.drainRequest = request
	fake.drainCalls++
	return fake.drainErr
}

type workerRouterReadHandle struct {
	*bytes.Reader
}

func (*workerRouterReadHandle) Close() error { return nil }
