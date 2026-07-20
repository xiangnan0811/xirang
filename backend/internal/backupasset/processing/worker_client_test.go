package processing

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workerCapabilities "xirang/backend/internal/backupasset/processing/capabilities"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
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

func TestWorkerRunnerAdvertisesClosedProductionSetAndExecutesInjectedNoopLifecycle(t *testing.T) {
	production := NewProductionWorkerCapabilitySet()
	advertisements := production.Advertisements()
	if len(advertisements) != 10 {
		t.Fatalf("production Worker advertisements=%d, want 10: %+v", len(advertisements), advertisements)
	}
	secretAdvertised := false
	for _, advertisement := range advertisements {
		secretAdvertised = secretAdvertised || advertisement.Capability == capabilityspec.CapabilitySecretClassify
	}
	if !secretAdvertised {
		t.Fatal("physical Worker profile set omitted optional secret.classify")
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

func TestWorkerRunnerUsesMaterializingTransitionForPathProfiles(t *testing.T) {
	capability := &workerNoopCapabilityFake{}
	capabilities, err := NewWorkerCapabilitySet([]WorkerCapability{capability})
	if err != nil {
		t.Fatal(err)
	}
	client := &workerMaterializingClientFake{workerRunnerClientFake: newWorkerRunnerClientFake()}
	runner, err := NewWorkerRunner(client, capabilities, WorkerRunnerConfig{
		InstanceID: strings.Repeat("2", 32), IdentityRevision: 1,
		InteractiveSlots: 1, BackgroundSlots: 1,
		HeartbeatInterval: time.Hour, PullBackoff: time.Millisecond, GracePeriod: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runner.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "handshake,pull,heartbeat,activate_input,transition:materializing,transition:processing,read_input,transition:uploading,activate_sink,upload_artifact,commit_manifest"
	if got := strings.Join(client.callSnapshot(), ","); got != want {
		t.Fatalf("materialized lifecycle=%s, want %s", got, want)
	}
}

func TestProductionTextCapabilityReadsBoundedInputAndEmitsVerifiedArtifacts(t *testing.T) {
	set := NewProductionWorkerCapabilitySet()
	var advertisement CapabilityAdvertisement
	for _, candidate := range set.Advertisements() {
		if candidate.Capability == "text.extract" {
			advertisement = candidate
			break
		}
	}
	if advertisement.Capability == "" {
		t.Fatal("text.extract advertisement missing")
	}
	capability, ok := set.capabilities[capabilityKey(
		advertisement.Capability, advertisement.CapabilitySchema,
		advertisement.PipelineFingerprint, advertisement.OutputProfile,
	)]
	if !ok {
		t.Fatal("text.extract implementation missing")
	}
	source := []byte("first line\nsecond line")
	original := append([]byte(nil), source...)
	input := &workerCapabilityMemoryInput{
		content: source,
		info: WorkerInputSourceInfo{
			Size: int64(len(source)), MediaType: "text/plain", FingerprintStrong: true, Sequential: true, Range: true,
		},
	}
	artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: validWorkDescriptor().Parameters,
		Input:      input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].Declaration.Role != ArtifactRoleContent ||
		artifacts[0].Declaration.MediaType != "text/plain" || artifacts[1].Declaration.Role != ArtifactRoleMetadata {
		t.Fatalf("unexpected text artifacts: %+v", artifacts)
	}
	content, err := io.ReadAll(artifacts[0].Content)
	if err != nil || !bytes.Equal(content, source) || !bytes.Equal(source, original) {
		t.Fatalf("text output/source mismatch content=%q source=%q err=%v", content, source, err)
	}
	digest := sha256.Sum256(content)
	if artifacts[0].Declaration.PlaintextDigest != fmt.Sprintf("%x", digest) ||
		artifacts[0].Declaration.PlaintextSize != int64(len(content)) {
		t.Fatalf("unverified text declaration: %+v", artifacts[0].Declaration)
	}
	metadata, err := io.ReadAll(artifacts[1].Content)
	if err != nil || !json.Valid(metadata) || bytes.Contains(bytes.ToLower(metadata), []byte("path")) {
		t.Fatalf("unsafe text metadata=%q err=%v", metadata, err)
	}
}

func TestProductionSecretClassificationExecutesPhysicalOptionalProfileWithoutPlaintextLeak(t *testing.T) {
	runner := &productionToolRunnerFake{}
	capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilitySecretClassify, runner)
	source := []byte("token=FAKE_TOKEN_FOR_TEST_ONLY")
	artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: validWorkDescriptor().Parameters,
		Input: &workerCapabilityMemoryInput{content: source, info: WorkerInputSourceInfo{
			Size: int64(len(source)), MediaType: "text/plain", FingerprintStrong: true, Sequential: true, Range: false,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.invocations) != 0 || len(artifacts) != 1 || artifacts[0].Declaration.Role != ArtifactRoleMetadata ||
		artifacts[0].Declaration.MediaType != "application/json" || artifacts[0].Declaration.Completeness != ArtifactComplete {
		t.Fatalf("secret classification path runner=%+v artifacts=%+v", runner.invocations, artifacts)
	}
	metadata, err := io.ReadAll(artifacts[0].Content)
	if err != nil || !json.Valid(metadata) || bytes.Contains(metadata, source) || bytes.Contains(bytes.ToLower(metadata), []byte("fake_token")) {
		t.Fatalf("unsafe secret metadata=%q err=%v", metadata, err)
	}
	var result struct {
		SchemaVersion int      `json:"schema_version"`
		Sensitivity   string   `json:"sensitivity"`
		Categories    []string `json:"categories"`
	}
	if err := json.Unmarshal(metadata, &result); err != nil || result.SchemaVersion != 1 || result.Sensitivity != "secret" ||
		len(result.Categories) != 1 || result.Categories[0] != "credential_pattern" {
		t.Fatalf("secret metadata=%+v err=%v", result, err)
	}
}

func TestProductionSecretClassificationNeverTurnsPartialOrInvalidInputNonSecret(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		source []byte
		strong bool
	}{
		{name: "partial source", source: []byte("ordinary text"), strong: false},
		{name: "invalid UTF-8", source: []byte{0xff, 0xfe}, strong: true},
		{name: "empty", source: nil, strong: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilitySecretClassify, &productionToolRunnerFake{})
			artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: validWorkDescriptor().Parameters,
				Input: &workerCapabilityMemoryInput{content: testCase.source, info: WorkerInputSourceInfo{
					Size: int64(len(testCase.source)), MediaType: "text/plain", FingerprintStrong: testCase.strong, Sequential: true,
				}},
			})
			if err != nil {
				return
			}
			if len(artifacts) != 1 {
				t.Fatalf("partial classification artifacts=%+v", artifacts)
			}
			metadata, readErr := io.ReadAll(artifacts[0].Content)
			if readErr != nil || bytes.Contains(metadata, []byte(`"sensitivity":"public"`)) || bytes.Contains(metadata, []byte(`"sensitivity":"non_secret"`)) {
				t.Fatalf("partial/invalid input became non-secret metadata=%q err=%v", metadata, readErr)
			}
		})
	}
}

func TestProductionImageCapabilityReencodesStaticThumbnail(t *testing.T) {
	var source bytes.Buffer
	inputImage := image.NewRGBA(image.Rect(0, 0, 4, 2))
	inputImage.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&source, inputImage); err != nil {
		t.Fatal(err)
	}
	runner := &productionToolRunnerFake{result: workerCapabilities.ToolResult{
		Outputs: map[string][]byte{"thumbnail.png": source.Bytes()},
	}}
	capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityImageThumbnail, runner)
	artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: validWorkDescriptor().Parameters,
		Input: &workerCapabilityMemoryInput{content: source.Bytes(), info: WorkerInputSourceInfo{
			Size: int64(source.Len()), MediaType: "image/png", FingerprintStrong: true, Sequential: true, Range: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.invocations) != 1 || runner.invocations[0].ExecutableID != workerCapabilities.ExecutableVips {
		t.Fatalf("thumbnail did not use the closed libvips runner: %+v", runner.invocations)
	}
	if len(artifacts) != 2 || artifacts[0].Declaration.Role != ArtifactRoleThumbnail ||
		artifacts[0].Declaration.MediaType != "image/png" || artifacts[1].Declaration.Role != ArtifactRoleMetadata {
		t.Fatalf("unexpected thumbnail artifacts: %+v", artifacts)
	}
	thumbnail, err := io.ReadAll(artifacts[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(thumbnail))
	if err != nil || decoded.Bounds().Dx() != 4 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("invalid static thumbnail bounds=%v err=%v", decoded.Bounds(), err)
	}
}

func TestAdvertisedImageMIMEsHaveClosedExecutablePaths(t *testing.T) {
	var thumbnail bytes.Buffer
	if err := png.Encode(&thumbnail, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, mediaType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif", "image/tiff", "image/bmp"} {
		t.Run(mediaType, func(t *testing.T) {
			runner := &productionToolRunnerFake{result: workerCapabilities.ToolResult{
				Outputs: map[string][]byte{"thumbnail.png": thumbnail.Bytes()},
			}}
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityImageThumbnail, runner)
			parameters := validWorkDescriptor().Parameters
			parameters.Width, parameters.Height, parameters.Quality = 64, 64, 80
			parameters.CropWidth, parameters.CropHeight = 64, 64
			source := advertisedRasterFixture(t, mediaType)
			artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: parameters,
				Input: &workerCapabilityMemoryInput{content: source, info: WorkerInputSourceInfo{
					Size: int64(len(source)), MediaType: mediaType, FingerprintStrong: true, Sequential: true, Range: true,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.invocations) != 1 || runner.invocations[0].ExecutableID != workerCapabilities.ExecutableVips ||
				runner.invocations[0].ArgProfile != workerCapabilities.ArgsVipsThumbnail {
				t.Fatalf("%s executable path=%+v", mediaType, runner.invocations)
			}
			if len(artifacts) != 2 || artifacts[0].Declaration.Role != ArtifactRoleThumbnail ||
				artifacts[0].Declaration.MediaType != "image/png" {
				t.Fatalf("%s thumbnail artifacts=%+v", mediaType, artifacts)
			}
		})
	}
}

func TestOCRNormalizesAdvertisedWebPAndTIFFBeforeTesseract(t *testing.T) {
	var normalized bytes.Buffer
	if err := png.Encode(&normalized, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, mediaType := range []string{"image/webp", "image/tiff"} {
		t.Run(mediaType, func(t *testing.T) {
			runner := &productionToolRunnerFake{results: []workerCapabilities.ToolResult{
				{Outputs: map[string][]byte{"normalized.png": normalized.Bytes()}},
				{Outputs: map[string][]byte{"ocr.txt": []byte("recognized")}},
			}}
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityImageOCR, runner)
			parameters := validWorkDescriptor().Parameters
			parameters.Language = "eng"
			source := advertisedRasterFixture(t, mediaType)
			artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: parameters,
				Input: &workerCapabilityMemoryInput{content: source, info: WorkerInputSourceInfo{
					Size: int64(len(source)), MediaType: mediaType, FingerprintStrong: true, Sequential: true, Range: true,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.invocations) != 2 || runner.invocations[0].ExecutableID != workerCapabilities.ExecutableVips ||
				runner.invocations[0].ArgProfile != workerCapabilities.ToolArgProfile("vips_raster_normalize_v1") ||
				runner.invocations[1].ExecutableID != workerCapabilities.ExecutableTesseract ||
				runner.invocations[1].ArgProfile != workerCapabilities.ArgsTesseractOCR {
				t.Fatalf("%s normalization/OCR calls=%+v", mediaType, runner.invocations)
			}
			if len(artifacts) != 2 || artifacts[0].Declaration.Role != ArtifactRoleOCR {
				t.Fatalf("%s OCR artifacts=%+v", mediaType, artifacts)
			}
		})
	}
}

func TestImageCapabilitiesRejectMIMEMismatchActivePolyglotAndInvalidToolOutput(t *testing.T) {
	pngSource := advertisedRasterFixture(t, "image/png")
	for _, testCase := range []struct {
		name      string
		mediaType string
		source    []byte
	}{
		{name: "declared JPEG actual PNG", mediaType: "image/jpeg", source: pngSource},
		{name: "HTML polyglot", mediaType: "image/png", source: append([]byte("<html><script>"), pngSource...)},
		{name: "SVG active content", mediaType: "image/png", source: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &productionToolRunnerFake{}
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityImageThumbnail, runner)
			parameters := validWorkDescriptor().Parameters
			parameters.Width, parameters.Height, parameters.Quality = 64, 64, 80
			parameters.CropWidth, parameters.CropHeight = 64, 64
			_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: parameters,
				Input: &workerCapabilityMemoryInput{content: testCase.source, info: WorkerInputSourceInfo{
					Size: int64(len(testCase.source)), MediaType: testCase.mediaType, FingerprintStrong: true, Sequential: true, Range: true,
				}},
			})
			if err == nil || len(runner.invocations) != 0 {
				t.Fatalf("unsafe image err=%v runner=%+v", err, runner.invocations)
			}
		})
	}

	runner := &productionToolRunnerFake{result: workerCapabilities.ToolResult{
		Outputs: map[string][]byte{"thumbnail.png": []byte(`<html>active</html>`)},
	}}
	capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityImageThumbnail, runner)
	parameters := validWorkDescriptor().Parameters
	parameters.Width, parameters.Height, parameters.Quality = 64, 64, 80
	parameters.CropWidth, parameters.CropHeight = 64, 64
	_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: parameters,
		Input: &workerCapabilityMemoryInput{content: pngSource, info: WorkerInputSourceInfo{
			Size: int64(len(pngSource)), MediaType: "image/png", FingerprintStrong: true, Sequential: true, Range: true,
		}},
	})
	if !errors.Is(err, workerCapabilities.ErrInvalidToolOutput) {
		t.Fatalf("active thumbnail tool output error=%v", err)
	}
}

func TestProductionArchiveInspectEmitsOpaqueBoundedIndex(t *testing.T) {
	var source bytes.Buffer
	archive := zip.NewWriter(&source)
	member, err := archive.Create("folder/private-name.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte("member")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	artifacts, err := executeProductionCapabilityForTest(t, "archive.inspect", "application/zip", source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Declaration.Role != ArtifactRoleMetadata {
		t.Fatalf("unexpected archive artifacts: %+v", artifacts)
	}
	metadata, err := io.ReadAll(artifacts[0].Content)
	if err != nil || !json.Valid(metadata) || bytes.Contains(metadata, []byte("folder/")) {
		t.Fatalf("unsafe archive index=%q err=%v", metadata, err)
	}
	var decoded struct {
		Entries []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(metadata, &decoded); err != nil || len(decoded.Entries) != 1 ||
		len(decoded.Entries[0].ID) != 32 || decoded.Entries[0].DisplayName != "private-name.txt" {
		t.Fatalf("invalid opaque archive index=%+v err=%v", decoded, err)
	}
}

func TestProductionArchiveExtractEntryReturnsOneValidatedMember(t *testing.T) {
	var source bytes.Buffer
	archive := zip.NewWriter(&source)
	member, err := archive.Create("folder/member.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte("member content")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	artifacts, err := executeProductionCapabilityForTest(t, "archive.extract_entry", "application/zip", source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].Declaration.Role != ArtifactRoleContent ||
		artifacts[0].Declaration.MediaType != "text/plain" || artifacts[1].Declaration.Role != ArtifactRoleMetadata {
		t.Fatalf("unexpected archive member artifacts: %+v", artifacts)
	}
	content, err := io.ReadAll(artifacts[0].Content)
	if err != nil || string(content) != "member content" {
		t.Fatalf("archive member content=%q err=%v", content, err)
	}
	metadata, err := io.ReadAll(artifacts[1].Content)
	if err != nil || !json.Valid(metadata) || bytes.Contains(metadata, []byte("folder/")) {
		t.Fatalf("unsafe archive member metadata=%q err=%v", metadata, err)
	}
}

func TestProductionCompressedTARInspectAndExtractStreamThroughClosedRunner(t *testing.T) {
	tarPayload := makeWorkerTAR(t, "folder/member.txt", []byte("member content"))
	for _, testCase := range []struct {
		mediaType string
		source    []byte
		execID    workerCapabilities.ExecutableID
	}{
		{mediaType: "application/gzip", source: makeWorkerGzip(t, tarPayload), execID: workerCapabilities.ExecutableID("gzip")},
		{mediaType: "application/x-xz", source: decodeWorkerCompressedFixture(t, "/Td6WFoAAATm1rRGBMA0gFAhARYAAAAAAAAAAHI4l+XgJ/8ALF0AAG/9//+jt/9HPkgVcjlhUbiSKOajhgf57uQegtMvxTo8AUuxfsmKXDIbZAAArxgSsy9VzDMAAVCAUAAAABpjMLWxxGf7AgAAAAAEWVo="), execID: workerCapabilities.ExecutableID("xz")},
		{mediaType: "application/zstd", source: decodeWorkerCompressedFixture(t, "KLUv/QRYTQAAEAAAAQD7hwdYvL1+1g=="), execID: workerCapabilities.ExecutableID("zstd")},
	} {
		t.Run(testCase.mediaType+" inspect", func(t *testing.T) {
			runner := &productionToolRunnerFake{stream: tarPayload}
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityArchiveInspect, runner)
			artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: validWorkDescriptor().Parameters,
				Input: &workerCapabilityMemoryInput{content: testCase.source, info: WorkerInputSourceInfo{
					Size: int64(len(testCase.source)), MediaType: testCase.mediaType, FingerprintStrong: true, Sequential: true, Range: true,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if runner.streamCalls != 1 || !runner.streamJoined || runner.invocation.ExecutableID != testCase.execID ||
				runner.invocation.InputMode != workerCapabilities.ToolInputPipe || len(artifacts) != 1 {
				t.Fatalf("compressed inspect runner=%+v calls=%d joined=%t artifacts=%+v", runner.invocation, runner.streamCalls, runner.streamJoined, artifacts)
			}
		})
		t.Run(testCase.mediaType+" extract", func(t *testing.T) {
			runner := &productionToolRunnerFake{stream: tarPayload}
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityArchiveExtractEntry, runner)
			parameters := validWorkDescriptor().Parameters
			parameters.MemberStart, parameters.MemberEnd = 0, 0
			artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: parameters,
				Input: &workerCapabilityMemoryInput{content: testCase.source, info: WorkerInputSourceInfo{
					Size: int64(len(testCase.source)), MediaType: testCase.mediaType, FingerprintStrong: true, Sequential: true, Range: true,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			content, readErr := io.ReadAll(artifacts[0].Content)
			if readErr != nil || string(content) != "member content" || runner.streamCalls != 2 || !runner.streamJoined {
				t.Fatalf("compressed extract content=%q readErr=%v calls=%d joined=%t", content, readErr, runner.streamCalls, runner.streamJoined)
			}
		})
	}
}

func TestProductionCompressedTARRejectsDeclaredMIMEMagicMismatchBeforeRunner(t *testing.T) {
	tarPayload := makeWorkerTAR(t, "member.txt", []byte("member"))
	for _, testCase := range []struct {
		declaredMediaType string
		source            []byte
	}{
		{declaredMediaType: "application/gzip", source: decodeWorkerCompressedFixture(t, "/Td6WFoAAATm1rRGBMA0gFAhARYAAAAAAAAAAHI4l+XgJ/8ALF0AAG/9//+jt/9HPkgVcjlhUbiSKOajhgf57uQegtMvxTo8AUuxfsmKXDIbZAAArxgSsy9VzDMAAVCAUAAAABpjMLWxxGf7AgAAAAAEWVo=")},
		{declaredMediaType: "application/x-xz", source: decodeWorkerCompressedFixture(t, "KLUv/QRYTQAAEAAAAQD7hwdYvL1+1g==")},
		{declaredMediaType: "application/zstd", source: makeWorkerGzip(t, tarPayload)},
	} {
		t.Run(testCase.declaredMediaType, func(t *testing.T) {
			runner := &productionToolRunnerFake{stream: tarPayload}
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityArchiveInspect, runner)
			_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: validWorkDescriptor().Parameters,
				Input: &workerCapabilityMemoryInput{content: testCase.source, info: WorkerInputSourceInfo{
					Size: int64(len(testCase.source)), MediaType: testCase.declaredMediaType,
					FingerprintStrong: true, Sequential: true, Range: true,
				}},
			})
			if !errors.Is(err, workerCapabilities.ErrInvalidToolOutput) || runner.streamCalls != 0 {
				t.Fatalf("declared MIME/magic mismatch err=%v calls=%d", err, runner.streamCalls)
			}
		})
	}
}

func TestProductionCompressedTARRejectsMalformedStreamsBombsAndCancellation(t *testing.T) {
	tarBomb := makeWorkerTAR(t, "bomb.txt", bytes.Repeat([]byte("x"), 64<<10))
	t.Run("ratio bomb", func(t *testing.T) {
		runner := &productionToolRunnerFake{stream: tarBomb}
		capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityArchiveInspect, runner)
		source := append([]byte{0x28, 0xb5, 0x2f, 0xfd}, []byte("tiny")...)
		_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
			Parameters: validWorkDescriptor().Parameters,
			Input: &workerCapabilityMemoryInput{content: source, info: WorkerInputSourceInfo{
				Size: int64(len(source)), MediaType: "application/zstd", FingerprintStrong: true, Sequential: true, Range: true,
			}},
		})
		if !errors.Is(err, workerCapabilities.ErrInputLimit) || runner.streamCalls != 1 || !runner.streamJoined {
			t.Fatalf("compressed ratio bomb err=%v calls=%d joined=%t", err, runner.streamCalls, runner.streamJoined)
		}
	})
	tarPayload := makeWorkerTAR(t, "member.txt", []byte("member"))
	for _, format := range []struct {
		mediaType string
		source    []byte
	}{
		{mediaType: "application/gzip", source: makeWorkerGzip(t, tarPayload)},
		{mediaType: "application/x-xz", source: decodeWorkerCompressedFixture(t, "/Td6WFoAAATm1rRGBMA0gFAhARYAAAAAAAAAAHI4l+XgJ/8ALF0AAG/9//+jt/9HPkgVcjlhUbiSKOajhgf57uQegtMvxTo8AUuxfsmKXDIbZAAArxgSsy9VzDMAAVCAUAAAABpjMLWxxGf7AgAAAAAEWVo=")},
		{mediaType: "application/zstd", source: decodeWorkerCompressedFixture(t, "KLUv/QRYTQAAEAAAAQD7hwdYvL1+1g==")},
	} {
		for _, malformed := range []struct {
			name   string
			source func([]byte) []byte
		}{
			{name: "truncated", source: func(source []byte) []byte { return append([]byte(nil), source[:len(source)-1]...) }},
			{name: "trailing", source: func(source []byte) []byte { return append(append([]byte(nil), source...), []byte("TRAILING")...) }},
		} {
			t.Run(format.mediaType+" "+malformed.name, func(t *testing.T) {
				source := malformed.source(format.source)
				runner := &productionToolRunnerFake{stream: tarPayload}
				capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityArchiveInspect, runner)
				_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
					Parameters: validWorkDescriptor().Parameters,
					Input: &workerCapabilityMemoryInput{content: source, info: WorkerInputSourceInfo{
						Size: int64(len(source)), MediaType: format.mediaType, FingerprintStrong: true, Sequential: true, Range: true,
					}},
				})
				if !errors.Is(err, workerCapabilities.ErrInvalidToolOutput) || runner.streamCalls != 1 || !runner.streamJoined {
					t.Fatalf("malformed stream err=%v calls=%d joined=%t", err, runner.streamCalls, runner.streamJoined)
				}
			})
		}
	}
	t.Run("cancel and join", func(t *testing.T) {
		runner := &productionToolRunnerFake{streamErr: context.Canceled}
		capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityArchiveInspect, runner)
		source := makeWorkerGzip(t, makeWorkerTAR(t, "member.txt", []byte("member")))
		_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
			Parameters: validWorkDescriptor().Parameters,
			Input: &workerCapabilityMemoryInput{content: source, info: WorkerInputSourceInfo{
				Size: int64(len(source)), MediaType: "application/gzip", FingerprintStrong: true, Sequential: true, Range: true,
			}},
		})
		if !errors.Is(err, context.Canceled) || runner.streamCalls != 1 || !runner.streamJoined {
			t.Fatalf("compressed cancellation err=%v calls=%d joined=%t", err, runner.streamCalls, runner.streamJoined)
		}
	})
}

func TestProductionCompressedTARRejectsEmptySecondCompressedStream(t *testing.T) {
	tarPayload := makeWorkerTAR(t, "member.txt", []byte("member"))
	for _, testCase := range []struct {
		mediaType string
		source    []byte
		empty     []byte
	}{
		{
			mediaType: "application/gzip",
			source:    makeWorkerGzip(t, tarPayload),
			empty:     makeWorkerGzip(t, nil),
		},
		{
			mediaType: "application/x-xz",
			source:    decodeWorkerCompressedFixture(t, "/Td6WFoAAATm1rRGBMA0gFAhARYAAAAAAAAAAHI4l+XgJ/8ALF0AAG/9//+jt/9HPkgVcjlhUbiSKOajhgf57uQegtMvxTo8AUuxfsmKXDIbZAAArxgSsy9VzDMAAVCAUAAAABpjMLWxxGf7AgAAAAAEWVo="),
			empty:     decodeWorkerCompressedFixture(t, "/Td6WFoAAATm1rRGAAAAABzfRCEftvN9AQAAAAAEWVo="),
		},
		{
			mediaType: "application/zstd",
			source:    decodeWorkerCompressedFixture(t, "KLUv/QRYTQAAEAAAAQD7hwdYvL1+1g=="),
			empty:     decodeWorkerCompressedFixture(t, "KLUv/SQAAQAAmenYUQ=="),
		},
	} {
		t.Run(testCase.mediaType, func(t *testing.T) {
			runner := &productionToolRunnerFake{stream: tarPayload}
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityArchiveInspect, runner)
			source := append(append([]byte(nil), testCase.source...), testCase.empty...)
			_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: validWorkDescriptor().Parameters,
				Input: &workerCapabilityMemoryInput{content: source, info: WorkerInputSourceInfo{
					Size: int64(len(source)), MediaType: testCase.mediaType, FingerprintStrong: true, Sequential: true, Range: true,
				}},
			})
			if !errors.Is(err, workerCapabilities.ErrInvalidToolOutput) || runner.streamCalls != 1 || !runner.streamJoined {
				t.Fatalf("empty second stream err=%v calls=%d joined=%t", err, runner.streamCalls, runner.streamJoined)
			}
		})
	}
}

func makeWorkerTAR(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var payload bytes.Buffer
	writer := tar.NewWriter(&payload)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}

func makeWorkerGzip(t *testing.T, content []byte) []byte {
	t.Helper()
	var payload bytes.Buffer
	writer := gzip.NewWriter(&payload)
	writer.Name = ""
	writer.Comment = ""
	writer.ModTime = time.Unix(0, 0).UTC()
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return payload.Bytes()
}

func decodeWorkerCompressedFixture(t *testing.T, encoded string) []byte {
	t.Helper()
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestCapabilityErrorsMapToClosedProcessingCodes(t *testing.T) {
	tests := []struct {
		err  error
		want ProcessingErrorCode
	}{
		{err: workerCapabilities.ErrInputLimit, want: ProcessingErrorInputTooLarge},
		{err: workerCapabilities.ErrSecureWorkspaceUnavailable, want: ProcessingErrorMaterializationDisabled},
		{err: workerCapabilities.ErrToolTimeout, want: ProcessingErrorTimeout},
		{err: workerCapabilities.ErrToolFailed, want: ProcessingErrorWorkerCrash},
		{err: workerCapabilities.ErrInvalidToolOutput, want: ProcessingErrorInvalidOutput},
		{err: capabilityspec.ErrUnsupportedMedia, want: ProcessingErrorUnsupportedFormat},
		{err: workerCapabilities.ErrArchiveEncrypted, want: ProcessingErrorEncryptedArchive},
	}
	for _, testCase := range tests {
		if got := mapCapabilityError(testCase.err); got != testCase.want {
			t.Fatalf("mapCapabilityError(%v)=%q, want %q", testCase.err, got, testCase.want)
		}
	}
}

func TestProductionExternalCapabilityExecutesOnlyThroughInjectedToolRunner(t *testing.T) {
	toolRunner := &productionToolRunnerFake{result: workerCapabilities.ToolResult{
		Outputs: map[string][]byte{"ocr.txt": []byte("recognized text")},
	}}
	set, err := NewProductionWorkerCapabilitySetWithOptions(ProductionWorkerCapabilityOptions{ToolRunner: toolRunner})
	if err != nil {
		t.Fatal(err)
	}
	var advertisement CapabilityAdvertisement
	for _, candidate := range set.Advertisements() {
		if candidate.Capability == "image.ocr" {
			advertisement = candidate
			break
		}
	}
	capability, ok := set.capabilities[capabilityKey(
		advertisement.Capability, advertisement.CapabilitySchema,
		advertisement.PipelineFingerprint, advertisement.OutputProfile,
	)]
	if !ok {
		t.Fatal("image.ocr implementation missing")
	}
	parameters := validWorkDescriptor().Parameters
	parameters.Language = "eng"
	var sourceBuffer bytes.Buffer
	if err := png.Encode(&sourceBuffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	source := sourceBuffer.Bytes()
	artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: parameters,
		Input: &workerCapabilityMemoryInput{
			content: source,
			info: WorkerInputSourceInfo{
				Size: int64(len(source)), MediaType: "image/png", Sequential: true, Range: true,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if toolRunner.invocation.ExecutableID != workerCapabilities.ExecutableTesseract ||
		toolRunner.invocation.ArgProfile != workerCapabilities.ArgsTesseractOCR || !bytes.Equal(toolRunner.input, source) {
		t.Fatalf("unexpected runner call invocation=%+v input=%q", toolRunner.invocation, toolRunner.input)
	}
	if len(artifacts) != 2 || artifacts[0].Declaration.Role != ArtifactRoleOCR || artifacts[0].Declaration.MediaType != "text/plain" {
		t.Fatalf("unexpected OCR artifacts: %+v", artifacts)
	}
}

func TestProductionExternalCapabilityRejectsMalformedInputBeforeToolRunner(t *testing.T) {
	toolRunner := &productionToolRunnerFake{}
	capability := productionCapabilityWithRunnerForTest(t, "media.probe", toolRunner)
	source := []byte("not-an-mp4")
	_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: validWorkDescriptor().Parameters,
		Input: &workerCapabilityMemoryInput{
			content: source,
			info:    WorkerInputSourceInfo{Size: int64(len(source)), MediaType: "video/mp4", Sequential: true, Range: true},
		},
	})
	if !errors.Is(err, workerCapabilities.ErrInvalidToolOutput) {
		t.Fatalf("malformed media error=%v", err)
	}
	if toolRunner.input != nil {
		t.Fatalf("malformed media reached tool runner: %q", toolRunner.input)
	}
}

func TestProductionDocumentPreflightBlocksActivePackagesBeforeLibreOffice(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mediaType string
		entries   map[string]string
	}{
		{
			name: "OOXML external relationship", mediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			entries: map[string]string{
				"[Content_Types].xml":          `<Types/>`,
				"word/_rels/document.xml.rels": `<Relationships><Relationship TargetMode="External" Target="https://FAKE_EXTERNAL_FOR_TEST_ONLY"/></Relationships>`,
			},
		},
		{
			name: "ODF script", mediaType: "application/vnd.oasis.opendocument.text",
			entries: map[string]string{
				"mimetype":         "application/vnd.oasis.opendocument.text",
				"content.xml":      `<office:document-content/>`,
				"Scripts/start.py": "pass",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var source bytes.Buffer
			writer := zip.NewWriter(&source)
			for name, content := range testCase.entries {
				header := &zip.FileHeader{Name: name, Method: zip.Deflate}
				if name == "mimetype" {
					header.Method = zip.Store
				}
				part, err := writer.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(part, content); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			runner := &productionToolRunnerFake{}
			capability := productionCapabilityWithRunnerForTest(t, capabilityspec.CapabilityDocumentConvert, runner)
			_, err := capability.Execute(context.Background(), WorkerCapabilityJob{
				Parameters: validWorkDescriptor().Parameters,
				Input: &workerCapabilityMemoryInput{content: source.Bytes(), info: WorkerInputSourceInfo{
					Size: int64(source.Len()), MediaType: testCase.mediaType, FingerprintStrong: true, Sequential: true, Range: true,
				}},
			})
			if err == nil || len(runner.invocations) != 0 {
				t.Fatalf("active document err=%v runner=%+v", err, runner.invocations)
			}
		})
	}
}

func TestProductionMalwarePositiveIsSuccessfulSanitizedFinding(t *testing.T) {
	toolRunner := &productionToolRunnerFake{result: workerCapabilities.ToolResult{
		ExitCode: 1,
		Stdout:   "/run/xirang/asset-jobs/job-secret/input.bin: Test.Signature.Name FOUND\n",
	}}
	set, err := NewProductionWorkerCapabilitySetWithOptions(ProductionWorkerCapabilityOptions{
		ToolRunner: toolRunner, MalwareBundleFingerprint: strings.Repeat("a", 64),
		Now: func() time.Time { return time.Now().UTC().Truncate(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	var capability WorkerCapability
	for _, advertisement := range set.Advertisements() {
		if advertisement.Capability == "malware.scan" {
			capability = set.capabilities[capabilityKey(
				advertisement.Capability, advertisement.CapabilitySchema,
				advertisement.PipelineFingerprint, advertisement.OutputProfile,
			)]
			break
		}
	}
	if capability == nil {
		t.Fatal("malware.scan implementation missing")
	}
	source := []byte("harmless test marker")
	artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: validWorkDescriptor().Parameters,
		Input: &workerCapabilityMemoryInput{
			content: source,
			info:    WorkerInputSourceInfo{Size: int64(len(source)), MediaType: "application/octet-stream", Sequential: true, Range: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Declaration.Role != ArtifactRoleMetadata {
		t.Fatalf("unexpected malware artifacts: %+v", artifacts)
	}
	metadata, err := io.ReadAll(artifacts[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	lower := bytes.ToLower(metadata)
	for _, forbidden := range [][]byte{[]byte("job-secret"), []byte("input.bin"), []byte("test.signature.name")} {
		if bytes.Contains(lower, forbidden) {
			t.Fatalf("malware metadata leaked raw tool content %q: %s", forbidden, metadata)
		}
	}
	var result capabilityspec.MalwareResult
	if err := json.Unmarshal(metadata, &result); err != nil || result.Validate() != nil ||
		result.Result != capabilityspec.ScanFinding || result.ProcessingOutcome() != capabilityspec.OutcomeSucceeded {
		t.Fatalf("invalid malware finding=%+v err=%v", result, err)
	}
}

func TestProductionDocumentCapabilityRevalidatesRenderedPage(t *testing.T) {
	var page bytes.Buffer
	pageImage := image.NewRGBA(image.Rect(0, 0, 2, 3))
	pageImage.Set(0, 0, color.RGBA{G: 255, A: 255})
	if err := png.Encode(&page, pageImage); err != nil {
		t.Fatal(err)
	}
	toolRunner := &productionToolRunnerFake{result: workerCapabilities.ToolResult{
		Outputs: map[string][]byte{"page-01.png": page.Bytes()},
	}}
	capability := productionCapabilityWithRunnerForTest(t, "document.convert", toolRunner)
	source := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n%%EOF")
	artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: validWorkDescriptor().Parameters,
		Input: &workerCapabilityMemoryInput{
			content: source,
			info:    WorkerInputSourceInfo{Size: int64(len(source)), MediaType: "application/pdf", Sequential: true, Range: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].Declaration.Role != ArtifactRoleThumbnail ||
		artifacts[1].Declaration.Role != ArtifactRoleMetadata {
		t.Fatalf("unexpected document artifacts: %+v", artifacts)
	}
}

func TestProductionMediaProbeCanonicalizesBoundedToolOutput(t *testing.T) {
	toolRunner := &productionToolRunnerFake{result: workerCapabilities.ToolResult{Stdout: `{
  "streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"duration":"12.5"}],
  "format":{"duration":"12.5"}
}`}}
	capability := productionCapabilityWithRunnerForTest(t, "media.probe", toolRunner)
	source := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0}
	artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: validWorkDescriptor().Parameters,
		Input: &workerCapabilityMemoryInput{
			content: source,
			info:    WorkerInputSourceInfo{Size: int64(len(source)), MediaType: "video/mp4", Sequential: true, Range: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Declaration.Role != ArtifactRoleMetadata {
		t.Fatalf("unexpected media probe artifacts: %+v", artifacts)
	}
	metadata, err := io.ReadAll(artifacts[0].Content)
	if err != nil || !json.Valid(metadata) || bytes.Contains(bytes.ToLower(metadata), []byte("path")) {
		t.Fatalf("unsafe media metadata=%q err=%v", metadata, err)
	}
}

func TestProductionMediaTranscodeRevalidatesClosedPreviewOutput(t *testing.T) {
	preview := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0}
	toolRunner := &productionToolRunnerFake{result: workerCapabilities.ToolResult{
		Outputs: map[string][]byte{"preview.mp4": preview},
	}}
	capability := productionCapabilityWithRunnerForTest(t, "media.transcode", toolRunner)
	source := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0}
	parameters := validWorkDescriptor().Parameters
	parameters.MaxDurationMillis = 1_800_000
	artifacts, err := capability.Execute(context.Background(), WorkerCapabilityJob{
		Parameters: parameters,
		Input: &workerCapabilityMemoryInput{
			content: source,
			info:    WorkerInputSourceInfo{Size: int64(len(source)), MediaType: "video/mp4", Sequential: true, Range: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].Declaration.Role != ArtifactRoleContent ||
		artifacts[0].Declaration.MediaType != "video/mp4" || artifacts[1].Declaration.Role != ArtifactRoleMetadata {
		t.Fatalf("unexpected media preview artifacts: %+v", artifacts)
	}
}

func productionCapabilityWithRunnerForTest(t *testing.T, name string, runner ProductionToolRunner) WorkerCapability {
	t.Helper()
	set, err := NewProductionWorkerCapabilitySetWithOptions(ProductionWorkerCapabilityOptions{ToolRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	for _, advertisement := range set.Advertisements() {
		if advertisement.Capability == name {
			return set.capabilities[capabilityKey(
				advertisement.Capability, advertisement.CapabilitySchema,
				advertisement.PipelineFingerprint, advertisement.OutputProfile,
			)]
		}
	}
	t.Fatalf("%s capability missing", name)
	return nil
}

func advertisedRasterFixture(t *testing.T, mediaType string) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	var payload bytes.Buffer
	switch mediaType {
	case "image/png":
		if err := png.Encode(&payload, picture); err != nil {
			t.Fatal(err)
		}
	case "image/jpeg":
		if err := jpeg.Encode(&payload, picture, &jpeg.Options{Quality: 80}); err != nil {
			t.Fatal(err)
		}
	case "image/gif":
		if err := gif.Encode(&payload, picture, nil); err != nil {
			t.Fatal(err)
		}
	case "image/webp":
		result := make([]byte, 30)
		copy(result[0:4], "RIFF")
		binary.LittleEndian.PutUint32(result[4:8], uint32(len(result)-8))
		copy(result[8:12], "WEBP")
		copy(result[12:16], "VP8X")
		binary.LittleEndian.PutUint32(result[16:20], 10)
		result[24] = 1
		result[27] = 1
		return result
	case "image/tiff":
		result := make([]byte, 38)
		copy(result[0:4], []byte{'I', 'I', 42, 0})
		binary.LittleEndian.PutUint32(result[4:8], 8)
		binary.LittleEndian.PutUint16(result[8:10], 2)
		binary.LittleEndian.PutUint16(result[10:12], 256)
		binary.LittleEndian.PutUint16(result[12:14], 4)
		binary.LittleEndian.PutUint32(result[14:18], 1)
		binary.LittleEndian.PutUint32(result[18:22], 2)
		binary.LittleEndian.PutUint16(result[22:24], 257)
		binary.LittleEndian.PutUint16(result[24:26], 4)
		binary.LittleEndian.PutUint32(result[26:30], 1)
		binary.LittleEndian.PutUint32(result[30:34], 2)
		return result
	case "image/bmp":
		result := make([]byte, 70)
		copy(result[0:2], "BM")
		binary.LittleEndian.PutUint32(result[2:6], uint32(len(result)))
		binary.LittleEndian.PutUint32(result[10:14], 54)
		binary.LittleEndian.PutUint32(result[14:18], 40)
		binary.LittleEndian.PutUint32(result[18:22], 2)
		binary.LittleEndian.PutUint32(result[22:26], 2)
		binary.LittleEndian.PutUint16(result[26:28], 1)
		binary.LittleEndian.PutUint16(result[28:30], 24)
		binary.LittleEndian.PutUint32(result[34:38], 16)
		return result
	default:
		t.Fatalf("unsupported raster fixture MIME %q", mediaType)
	}
	return payload.Bytes()
}

type productionToolRunnerFake struct {
	invocation   workerCapabilities.ToolInvocation
	input        []byte
	result       workerCapabilities.ToolResult
	err          error
	invocations  []workerCapabilities.ToolInvocation
	inputs       [][]byte
	results      []workerCapabilities.ToolResult
	stream       []byte
	streamErr    error
	streamCalls  int
	streamJoined bool
}

func (runner *productionToolRunnerFake) RunInput(
	_ context.Context,
	invocation workerCapabilities.ToolInvocation,
	input io.Reader,
) (workerCapabilities.ToolResult, error) {
	runner.invocation = invocation
	runner.input, _ = io.ReadAll(input)
	runner.invocations = append(runner.invocations, invocation)
	runner.inputs = append(runner.inputs, append([]byte(nil), runner.input...))
	if len(runner.results) > 0 {
		result := runner.results[0]
		runner.results = runner.results[1:]
		return result, runner.err
	}
	return runner.result, runner.err
}

func (runner *productionToolRunnerFake) RunInputStream(
	ctx context.Context,
	invocation workerCapabilities.ToolInvocation,
	input io.Reader,
	consume func(io.Reader) error,
) (workerCapabilities.ToolResult, error) {
	runner.streamCalls++
	runner.invocation = invocation
	runner.input, _ = io.ReadAll(input)
	runner.invocations = append(runner.invocations, invocation)
	runner.inputs = append(runner.inputs, append([]byte(nil), runner.input...))
	defer func() { runner.streamJoined = true }()
	if runner.streamErr != nil {
		return workerCapabilities.ToolResult{}, runner.streamErr
	}
	if err := ctx.Err(); err != nil {
		return workerCapabilities.ToolResult{}, err
	}
	if consume == nil {
		return workerCapabilities.ToolResult{}, workerCapabilities.ErrInvalidInvocation
	}
	if err := consume(bytes.NewReader(runner.stream)); err != nil {
		return workerCapabilities.ToolResult{}, err
	}
	return runner.result, nil
}

func executeProductionCapabilityForTest(t *testing.T, capabilityName, mediaType string, source []byte) ([]WorkerCapabilityArtifact, error) {
	t.Helper()
	set := NewProductionWorkerCapabilitySet()
	for _, advertisement := range set.Advertisements() {
		if advertisement.Capability != capabilityName {
			continue
		}
		capability, ok := set.capabilities[capabilityKey(
			advertisement.Capability, advertisement.CapabilitySchema,
			advertisement.PipelineFingerprint, advertisement.OutputProfile,
		)]
		if !ok {
			t.Fatalf("%s implementation missing", capabilityName)
		}
		parameters := validWorkDescriptor().Parameters
		if capabilityName == "archive.extract_entry" {
			parameters.MemberStart = 0
			parameters.MemberEnd = 0
		}
		return capability.Execute(context.Background(), WorkerCapabilityJob{
			Parameters: parameters,
			Input: &workerCapabilityMemoryInput{
				content: append([]byte(nil), source...),
				info: WorkerInputSourceInfo{
					Size: int64(len(source)), MediaType: mediaType, FingerprintStrong: true, Sequential: true, Range: true,
				},
			},
		})
	}
	t.Fatalf("%s advertisement missing", capabilityName)
	return nil, nil
}

type workerCapabilityMemoryInput struct {
	content []byte
	info    WorkerInputSourceInfo
	offset  int64
}

func (input *workerCapabilityMemoryInput) Info() WorkerInputSourceInfo { return input.info }

func (input *workerCapabilityMemoryInput) ReadSequential(_ context.Context, length int64) ([]byte, error) {
	if length <= 0 || input.offset >= int64(len(input.content)) {
		return nil, nil
	}
	end := min(int64(len(input.content)), input.offset+length)
	result := append([]byte(nil), input.content[input.offset:end]...)
	input.offset = end
	return result, nil
}

func (input *workerCapabilityMemoryInput) ReadRange(_ context.Context, offset, length int64) ([]byte, error) {
	if offset < 0 || length <= 0 || offset >= int64(len(input.content)) {
		return nil, nil
	}
	end := min(int64(len(input.content)), offset+length)
	return append([]byte(nil), input.content[offset:end]...), nil
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

type workerMaterializingClientFake struct{ *workerRunnerClientFake }

func (client *workerMaterializingClientFake) Pull(ctx context.Context, request WorkerPullRequest) (WorkerJobEnvelope, error) {
	envelope, err := client.workerRunnerClientFake.Pull(ctx, request)
	envelope.Descriptor.Parameters.RequiresMaterialization = true
	return envelope, err
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
