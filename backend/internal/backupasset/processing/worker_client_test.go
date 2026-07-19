package processing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerClientHandshakeUsesFixedRouteAndStrictResponse(t *testing.T) {
	var responseCase atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/internal/v1/asset-worker/handshake" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("X-Forwarded-For") != "" {
			t.Errorf("Worker client emitted header trust material: %v", request.Header)
		}
		response.Header().Set("Content-Type", "application/json")
		switch responseCase.Load() {
		case 0:
			_, _ = response.Write([]byte(`{"schema_version":1,"code":"ok","data":{"worker_id":"` + strings.Repeat("a", 32) + `","trust_state":"active","health_state":"ready","capability_count":0}}`))
		case 1:
			_, _ = response.Write([]byte(`{"schema_version":1,"code":"ok","unknown":true,"data":{}}`))
		case 2:
			_, _ = response.Write([]byte(`{"schema_version":1,"code":"ok","code":"ok","data":{}}`))
		case 3:
			_, _ = response.Write([]byte(`{"schema_version":1,"code":"ok","data":{}} {}`))
		}
	}))
	t.Cleanup(server.Close)

	client, err := newWorkerClient(server.Client(), server.URL, 64<<10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	request := HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: WorkerProtocolVersion,
		InstanceID: strings.Repeat("b", 32), IdentityRevision: 1,
		InteractiveSlots: 1,
	}
	result, err := client.Handshake(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerID != strings.Repeat("a", 32) || result.TrustState != "active" || result.CapabilityCount != 0 {
		t.Fatalf("unexpected handshake result: %+v", result)
	}

	for testCase := int32(1); testCase <= 3; testCase++ {
		responseCase.Store(testCase)
		if _, err := client.Handshake(context.Background(), request); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("response case %d error=%v, want ErrProtocolInvalid", testCase, err)
		}
	}
}

func TestWorkerClientPullMapsNoWorkAndSanitizesRemoteErrors(t *testing.T) {
	var failure atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if !failure.Load() {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte(`{"schema_version":1,"code":"temporarily_unavailable","data":{"secret":"DO_NOT_ECHO"}}`))
	}))
	t.Cleanup(server.Close)
	client, err := newWorkerClient(server.Client(), server.URL, 64<<10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	request := WorkerPullRequest{SchemaVersion: 1, WorkerID: strings.Repeat("a", 32), InstanceID: strings.Repeat("b", 32)}
	if _, err := client.Pull(context.Background(), request); !errors.Is(err, ErrNoWork) {
		t.Fatalf("no-work error=%v", err)
	}
	failure.Store(true)
	if _, err := client.Pull(context.Background(), request); !errors.Is(err, ErrWorkerTemporarilyUnavailable) || strings.Contains(err.Error(), "DO_NOT_ECHO") {
		t.Fatalf("unsafe or incorrectly mapped remote error: %v", err)
	}
}

func TestNewWorkerClientRejectsNonUDSNonMTLSTransports(t *testing.T) {
	tests := []WorkerClientConfig{
		{},
		{LocalSocketPath: "/run/xirang/asset-worker.sock", RemoteEndpoint: "https://127.0.0.1:9443"},
		{RemoteEndpoint: "http://127.0.0.1:9443"},
		{RemoteEndpoint: "https://worker.example:9443"},
	}
	for index, config := range tests {
		if _, err := NewWorkerClient(config); !errors.Is(err, ErrWorkerTransportUnsafe) {
			t.Fatalf("config %d error=%v, want ErrWorkerTransportUnsafe", index, err)
		}
	}
}

func TestWorkerClientCoversFixedInputSinkAndControlRoutes(t *testing.T) {
	workerID := strings.Repeat("1", 32)
	instanceID := strings.Repeat("2", 32)
	jobID := strings.Repeat("3", 32)
	attemptID := strings.Repeat("4", 32)
	inputSessionID := strings.Repeat("5", 32)
	sinkSessionID := strings.Repeat("6", 32)
	uploadID := strings.Repeat("7", 32)
	blobID := strings.Repeat("8", 32)
	artifactSetID := strings.Repeat("9", 32)
	manifestDigest := strings.Repeat("a", 64)
	artifactPayload := []byte("passive-noop-artifact")

	seen := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		seen[request.URL.Path]++
		switch request.URL.Path {
		case "/internal/v1/asset-worker/jobs/" + jobID + "/heartbeat":
			writeWorkerClientTestResponse(t, response, WorkerHeartbeatResult{SchemaVersion: 1, TransitionRevision: 1})
		case "/internal/v1/asset-worker/jobs/" + jobID + "/transitions":
			writeWorkerClientTestResponse(t, response, WorkerTransitionResult{SchemaVersion: 1, State: ProcessingProcessing, Revision: 3})
		case "/internal/v1/asset-worker/jobs/" + jobID + "/input/activate":
			writeWorkerClientTestResponse(t, response, WorkerInputActivation{
				SchemaVersion: 1, SessionID: inputSessionID, TransitionRevision: 2,
				ExpiresAt: time.Now().Add(time.Minute).UTC(), Source: WorkerInputSourceInfo{Size: 5, Sequential: true, Range: true},
			})
		case "/internal/v1/asset-worker/input-sessions/" + inputSessionID + "/ranges":
			response.Header().Set("Content-Type", "application/octet-stream")
			response.Header().Set("Cache-Control", "no-store")
			_, _ = response.Write([]byte("input"))
		case "/internal/v1/asset-worker/jobs/" + jobID + "/sink/activate":
			writeWorkerClientTestResponse(t, response, WorkerSinkActivation{
				SchemaVersion: 1, SessionID: sinkSessionID, ExpiresAt: time.Now().Add(time.Minute).UTC(),
				MaxArtifacts: 1, MaxArtifactBytes: 1024, MaxTotalBytes: 1024, MaxInFlight: 1,
			})
		case "/internal/v1/asset-worker/sink-sessions/" + sinkSessionID + "/artifacts":
			parts, err := request.MultipartReader()
			if err != nil {
				t.Errorf("MultipartReader: %v", err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			metadataPart, err := parts.NextPart()
			if err != nil || metadataPart.FormName() != "metadata" || metadataPart.FileName() != "" {
				t.Errorf("invalid metadata part name=%q filename=%q err=%v", metadataPart.FormName(), metadataPart.FileName(), err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			var metadata WorkerUploadArtifactRequest
			if err := json.NewDecoder(metadataPart).Decode(&metadata); err != nil || metadata.SessionID != sinkSessionID {
				t.Errorf("invalid upload metadata: %+v err=%v", metadata, err)
			}
			contentPart, err := parts.NextPart()
			if err != nil || contentPart.FormName() != "content" || contentPart.FileName() != "" {
				t.Errorf("invalid content part name=%q filename=%q err=%v", contentPart.FormName(), contentPart.FileName(), err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			content, err := io.ReadAll(contentPart)
			if err != nil || !bytes.Equal(content, artifactPayload) {
				t.Errorf("artifact content=%q err=%v", content, err)
			}
			if trailing, err := parts.NextPart(); err != io.EOF || trailing != nil {
				t.Errorf("unexpected trailing multipart part=%v err=%v", trailing, err)
			}
			writeWorkerClientTestResponse(t, response, UploadedArtifact{UploadID: uploadID, BlobID: blobID, Ordinal: 0})
		case "/internal/v1/asset-worker/sink-sessions/" + sinkSessionID + "/manifest":
			writeWorkerClientTestResponse(t, response, WorkerCommitManifestResult{
				SchemaVersion: 1, ArtifactSetID: artifactSetID, ManifestDigest: manifestDigest,
			})
		case "/internal/v1/asset-worker/drain":
			writeWorkerClientTestResponse(t, response, struct{}{})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := newWorkerClient(server.Client(), server.URL, 64<<10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := client.Heartbeat(context.Background(), jobID, WorkerHeartbeatRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, AttemptID: attemptID,
	})
	if err != nil || heartbeat.SchemaVersion != 1 {
		t.Fatalf("Heartbeat result=%+v err=%v", heartbeat, err)
	}
	transition, err := client.Transition(context.Background(), WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, JobID: jobID,
		AttemptID: attemptID, ExpectedRevision: 2, To: ProcessingProcessing,
	})
	if err != nil || transition.Revision != 3 {
		t.Fatalf("Transition result=%+v err=%v", transition, err)
	}
	input, err := client.ActivateInput(context.Background(), WorkerInputActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, JobID: jobID,
		AttemptID: attemptID, ExpectedRevision: 1, GrantID: strings.Repeat("b", 32), Secret: "one-use-input",
	})
	if err != nil || input.SessionID != inputSessionID {
		t.Fatalf("ActivateInput result=%+v err=%v", input, err)
	}
	inputBytes, err := client.ReadInput(context.Background(), WorkerInputReadRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, SessionID: inputSessionID,
		Mode: "range", Offset: 0, Length: 5,
	})
	if err != nil || string(inputBytes) != "input" {
		t.Fatalf("ReadInput bytes=%q err=%v", inputBytes, err)
	}
	sink, err := client.ActivateSink(context.Background(), WorkerSinkActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, JobID: jobID,
		AttemptID: attemptID, ExpectedRevision: 3, GrantID: strings.Repeat("c", 32), Secret: "one-use-sink",
	})
	if err != nil || sink.SessionID != sinkSessionID {
		t.Fatalf("ActivateSink result=%+v err=%v", sink, err)
	}
	declaration := ArtifactDeclaration{
		Ordinal: 0, Role: ArtifactRoleNoop, MediaType: "application/octet-stream",
		PlaintextSize: int64(len(artifactPayload)), PlaintextDigest: strings.Repeat("d", 64), Completeness: ArtifactComplete,
	}
	uploaded, err := client.UploadArtifact(context.Background(), WorkerUploadArtifactRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, SessionID: sinkSessionID,
		JobID: jobID, AttemptID: attemptID, Artifact: declaration,
	}, bytes.NewReader(artifactPayload))
	if err != nil || uploaded.UploadID != uploadID || uploaded.BlobID != blobID {
		t.Fatalf("UploadArtifact result=%+v err=%v", uploaded, err)
	}
	manifest, err := client.CommitManifest(context.Background(), WorkerCommitManifestRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, SessionID: sinkSessionID,
		JobID: jobID, AttemptID: attemptID, SecurityPolicyRevision: "policy-v1", Artifacts: []ArtifactDeclaration{declaration},
	})
	if err != nil || manifest.ArtifactSetID != artifactSetID {
		t.Fatalf("CommitManifest result=%+v err=%v", manifest, err)
	}
	if err := client.Drain(context.Background(), WorkerDrainRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID}); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(seen) != 8 {
		t.Fatalf("fixed routes seen=%v", seen)
	}
}

func writeWorkerClientTestResponse(t *testing.T, response http.ResponseWriter, data any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(map[string]any{"schema_version": 1, "code": "ok", "data": data}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestWorkerRunnerExecutesInjectedNoopLifecycleWhileProductionRegistryIsEmpty(t *testing.T) {
	production := NewProductionWorkerCapabilitySet()
	if got := production.Advertisements(); len(got) != 0 {
		t.Fatalf("production Worker advertised capabilities: %+v", got)
	}
	capability := &workerNoopCapabilityFake{}
	capabilities, err := NewWorkerCapabilitySet([]WorkerCapability{capability})
	if err != nil {
		t.Fatal(err)
	}
	client := newWorkerRunnerClientFake()
	runner, err := NewWorkerRunner(client, capabilities, WorkerRunnerConfig{
		InstanceID: strings.Repeat("2", 32), IdentityRevision: 1,
		InteractiveSlots: 1, BackgroundSlots: 1,
		HeartbeatInterval: time.Hour, PullBackoff: time.Millisecond, GracePeriod: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.handshake(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	wantCalls := []string{
		"handshake", "pull", "heartbeat", "activate_input", "transition:processing", "read_input",
		"transition:uploading", "activate_sink", "upload_artifact", "commit_manifest",
	}
	if calls := client.callSnapshot(); strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("Worker lifecycle calls=%v want=%v", calls, wantCalls)
	}
	if !capability.executed || capability.input != "input" {
		t.Fatalf("fake/no-op capability executed=%t input=%q", capability.executed, capability.input)
	}
	if client.handshake.Capabilities[0].Capability != "noop" || client.upload.Artifact.Role != ArtifactRoleNoop ||
		client.manifest.SecurityPolicyRevision != "policy-v1" {
		t.Fatalf("protocol lifecycle handshake=%+v upload=%+v manifest=%+v", client.handshake, client.upload, client.manifest)
	}
}

func TestWorkerRunnerCancellationStopsPullsAndDrainsWithinGrace(t *testing.T) {
	client := &workerIdleClientFake{
		workerRunnerClientFake: newWorkerRunnerClientFake(),
		pullStarted:            make(chan struct{}, 1),
	}
	runner, err := NewWorkerRunner(client, NewProductionWorkerCapabilitySet(), WorkerRunnerConfig{
		InstanceID: strings.Repeat("2", 32), IdentityRevision: 1,
		InteractiveSlots: 1, BackgroundSlots: 1,
		HeartbeatInterval: time.Second, PullBackoff: time.Hour, GracePeriod: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-client.pullStarted:
	case <-time.After(time.Second):
		t.Fatal("Worker runner did not begin pulling")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Worker runner exceeded graceful shutdown bound")
	}
	if got := strings.Join(client.callSnapshot(), ","); got != "handshake,pull,drain" {
		t.Fatalf("graceful lifecycle calls=%s", got)
	}
}

func TestWorkerRunnerHeartbeatCancellationUsesCurrentRevisionAndClosedReason(t *testing.T) {
	capability := &workerBlockingCapabilityFake{started: make(chan struct{})}
	capabilities, err := NewWorkerCapabilitySet([]WorkerCapability{capability})
	if err != nil {
		t.Fatal(err)
	}
	client := &workerCancelClientFake{workerRunnerClientFake: newWorkerRunnerClientFake()}
	runner, err := NewWorkerRunner(client, capabilities, WorkerRunnerConfig{
		InstanceID: strings.Repeat("2", 32), IdentityRevision: 1,
		InteractiveSlots: 1, BackgroundSlots: 1,
		HeartbeatInterval: time.Millisecond, PullBackoff: time.Millisecond, GracePeriod: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce cancellation: %v", err)
	}
	client.terminalMu.Lock()
	terminal := client.terminal
	client.terminalMu.Unlock()
	if terminal.To != ProcessingCanceled || terminal.ExpectedRevision != 4 || terminal.CancelReason != CancelReasonInterestWithdrawn || terminal.ErrorCode != "" {
		t.Fatalf("heartbeat cancellation used stale or open transition: %+v", terminal)
	}
}

type workerNoopCapabilityFake struct {
	executed bool
	input    string
}

type workerBlockingCapabilityFake struct{ started chan struct{} }

func (capability *workerBlockingCapabilityFake) Advertisement() CapabilityAdvertisement {
	return (&workerNoopCapabilityFake{}).Advertisement()
}

func (capability *workerBlockingCapabilityFake) Execute(ctx context.Context, _ WorkerCapabilityJob) ([]WorkerCapabilityArtifact, error) {
	close(capability.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (capability *workerNoopCapabilityFake) Advertisement() CapabilityAdvertisement {
	return CapabilityAdvertisement{
		SchemaVersion: 1, Capability: "noop", CapabilitySchema: "noop.v1",
		PipelineFingerprint: "noop-pipeline-v1", OutputProfile: "noop.v1",
		InputModes: []ProtocolInputMode{ProtocolInputStat, ProtocolInputRange},
		Limits: ProtocolCapabilityLimits{
			MaxInputBytes: 64 << 10, MaxOutputBytes: 1024, MaxOutputCount: 1,
			MaxPages: 1, MaxPixels: 1, MaxDurationMillis: 1000, MaxExpandedBytes: 64 << 10,
		},
	}
}

func (capability *workerNoopCapabilityFake) Execute(ctx context.Context, job WorkerCapabilityJob) ([]WorkerCapabilityArtifact, error) {
	payload, err := job.Input.ReadRange(ctx, 0, 5)
	if err != nil {
		return nil, err
	}
	capability.executed = true
	capability.input = string(payload)
	content := []byte("passive-noop-artifact")
	return []WorkerCapabilityArtifact{{
		Declaration: ArtifactDeclaration{
			Ordinal: 0, Role: ArtifactRoleNoop, MediaType: "application/octet-stream",
			PlaintextSize: int64(len(content)), PlaintextDigest: strings.Repeat("d", 64), Completeness: ArtifactComplete,
		},
		Content: bytes.NewReader(content),
	}}, nil
}

type workerRunnerClientFake struct {
	mu        sync.Mutex
	calls     []string
	handshake HandshakeRequest
	upload    WorkerUploadArtifactRequest
	manifest  WorkerCommitManifestRequest
}

func (client *workerRunnerClientFake) record(call string) {
	client.mu.Lock()
	client.calls = append(client.calls, call)
	client.mu.Unlock()
}

func (client *workerRunnerClientFake) callSnapshot() []string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]string(nil), client.calls...)
}

type workerIdleClientFake struct {
	*workerRunnerClientFake
	pullStarted chan struct{}
}

type workerCancelClientFake struct {
	*workerRunnerClientFake
	heartbeats atomic.Int32
	terminalMu sync.Mutex
	terminal   WorkerTransitionRequest
}

func (client *workerCancelClientFake) Heartbeat(context.Context, string, WorkerHeartbeatRequest) (WorkerHeartbeatResult, error) {
	client.record("heartbeat")
	if client.heartbeats.Add(1) == 1 {
		return WorkerHeartbeatResult{SchemaVersion: 1, TransitionRevision: 1}, nil
	}
	return WorkerHeartbeatResult{
		SchemaVersion: 1, TransitionRevision: 4,
		CancelRequested: true, CancelReason: CancelReasonInterestWithdrawn,
	}, nil
}

func (client *workerCancelClientFake) Transition(ctx context.Context, request WorkerTransitionRequest) (WorkerTransitionResult, error) {
	if request.To == ProcessingCanceled || request.To == ProcessingFailed {
		client.terminalMu.Lock()
		client.terminal = request
		client.terminalMu.Unlock()
	}
	return client.workerRunnerClientFake.Transition(ctx, request)
}

func (client *workerIdleClientFake) Pull(context.Context, WorkerPullRequest) (WorkerJobEnvelope, error) {
	client.record("pull")
	select {
	case client.pullStarted <- struct{}{}:
	default:
	}
	return WorkerJobEnvelope{}, ErrNoWork
}

func newWorkerRunnerClientFake() *workerRunnerClientFake { return &workerRunnerClientFake{} }

func (client *workerRunnerClientFake) Handshake(_ context.Context, request HandshakeRequest) (HandshakeResult, error) {
	client.record("handshake")
	client.handshake = request
	return HandshakeResult{WorkerID: strings.Repeat("1", 32), TrustState: "active", HealthState: "ready", CapabilityCount: len(request.Capabilities)}, nil
}

func (client *workerRunnerClientFake) Pull(context.Context, WorkerPullRequest) (WorkerJobEnvelope, error) {
	client.record("pull")
	descriptor := validWorkDescriptor()
	descriptor.Capability = "noop"
	descriptor.CapabilitySchema = "noop.v1"
	descriptor.PipelineFingerprint = "noop-pipeline-v1"
	descriptor.OutputProfile = "noop.v1"
	descriptor.SecurityPolicyRevision = "policy-v1"
	return WorkerJobEnvelope{
		SchemaVersion: 1, ProtocolVersion: WorkerProtocolVersion,
		JobID: strings.Repeat("3", 32), AttemptID: strings.Repeat("4", 32), TransitionRevision: 1,
		Descriptor:      descriptor,
		InputActivation: WorkerActivationMaterial{GrantID: strings.Repeat("a", 32), Secret: "input-secret"},
		SinkActivation:  WorkerActivationMaterial{GrantID: strings.Repeat("b", 32), Secret: "sink-secret"},
	}, nil
}

func (client *workerRunnerClientFake) Heartbeat(context.Context, string, WorkerHeartbeatRequest) (WorkerHeartbeatResult, error) {
	client.record("heartbeat")
	return WorkerHeartbeatResult{SchemaVersion: 1, TransitionRevision: 1}, nil
}

func (client *workerRunnerClientFake) Transition(_ context.Context, request WorkerTransitionRequest) (WorkerTransitionResult, error) {
	client.record("transition:" + string(request.To))
	return WorkerTransitionResult{SchemaVersion: 1, State: request.To, Revision: request.ExpectedRevision + 1}, nil
}

func (client *workerRunnerClientFake) ActivateInput(context.Context, WorkerInputActivateRequest) (WorkerInputActivation, error) {
	client.record("activate_input")
	return WorkerInputActivation{
		SchemaVersion: 1, SessionID: strings.Repeat("5", 32), TransitionRevision: 2,
		ExpiresAt: time.Now().Add(time.Minute), Source: WorkerInputSourceInfo{Size: 5, Range: true},
	}, nil
}

func (client *workerRunnerClientFake) ReadInput(context.Context, WorkerInputReadRequest) ([]byte, error) {
	client.record("read_input")
	return []byte("input"), nil
}

func (client *workerRunnerClientFake) ActivateSink(context.Context, WorkerSinkActivateRequest) (WorkerSinkActivation, error) {
	client.record("activate_sink")
	return WorkerSinkActivation{
		SchemaVersion: 1, SessionID: strings.Repeat("6", 32), ExpiresAt: time.Now().Add(time.Minute),
		MaxArtifacts: 1, MaxArtifactBytes: 1024, MaxTotalBytes: 1024, MaxInFlight: 1,
	}, nil
}

func (client *workerRunnerClientFake) UploadArtifact(_ context.Context, request WorkerUploadArtifactRequest, body io.Reader) (UploadedArtifact, error) {
	client.record("upload_artifact")
	client.upload = request
	_, _ = io.ReadAll(body)
	return UploadedArtifact{UploadID: strings.Repeat("7", 32), BlobID: strings.Repeat("8", 32), Ordinal: request.Artifact.Ordinal}, nil
}

func (client *workerRunnerClientFake) CommitManifest(_ context.Context, request WorkerCommitManifestRequest) (WorkerCommitManifestResult, error) {
	client.record("commit_manifest")
	client.manifest = request
	return WorkerCommitManifestResult{SchemaVersion: 1, ArtifactSetID: strings.Repeat("9", 32), ManifestDigest: strings.Repeat("c", 64)}, nil
}

func (client *workerRunnerClientFake) Drain(context.Context, WorkerDrainRequest) error {
	client.record("drain")
	return nil
}

func (client *workerRunnerClientFake) CloseIdleConnections() {}
