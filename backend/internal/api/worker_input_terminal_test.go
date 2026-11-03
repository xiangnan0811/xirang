package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWorkerInputTerminalStatus(t *testing.T) {
	const chunk = int64(64 << 10)
	large := make([]byte, 2<<20+17)
	for i := range large {
		large[i] = byte((i/65536*37 + i*13 + i/257) % 251)
	}
	for _, tc := range []struct {
		name        string
		payload     []byte
		mode        content.SourceMode
		fault       string
		maxRequests int64
	}{
		{"large_ranges", large, content.SourceModeRange, "", 36},
		{"sequential", large[:chunk], content.SourceModeSequential, "", 3},
		{"final_close_failure", large[:chunk+17], content.SourceModeRange, "close", 4},
		{"final_copy_failure", large[:chunk+17], content.SourceModeRange, "copy", 4},
		{"grant_budget", large[:2*chunk+17], content.SourceModeRange, "budget", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkerInputHTTPFixture(t, tc.payload, tc.mode == content.SourceModeRange, tc.fault, tc.maxRequests)
			activation := fixture.activation
			if activation.SchemaVersion != 2 || activation.Source.Size != int64(len(tc.payload)) || activation.Source.MaxBytesPerRequest != chunk || activation.Source.Range != (tc.mode == content.SourceModeRange) {
				t.Fatalf("activation=%+v", activation)
			}
			var got bytes.Buffer
			var readErr error
			for offset := int64(0); offset < int64(len(tc.payload)); offset += chunk {
				length := min(chunk, int64(len(tc.payload))-offset)
				request := processing.WorkerInputReadRequest{
					SchemaVersion: 1, WorkerID: fixture.workerID, InstanceID: fixture.instanceID,
					SessionID: activation.SessionID, Mode: tc.mode, Length: length,
				}
				if tc.mode == content.SourceModeRange {
					request.Offset = offset
				}
				var payload []byte
				payload, readErr = fixture.client.ReadInput(context.Background(), request)
				if readErr != nil {
					if len(payload) != 0 {
						t.Fatalf("failed read returned partial bytes: %d", len(payload))
					}
					break
				}
				if int64(len(payload)) != length {
					t.Fatalf("short response: %d != %d", len(payload), length)
				}
				_, _ = got.Write(payload)
			}
			if tc.fault == "" {
				if readErr != nil || !bytes.Equal(got.Bytes(), tc.payload) {
					t.Fatalf("bytes=%d err=%v", got.Len(), readErr)
				}
			} else if readErr == nil || errors.Is(readErr, io.EOF) {
				t.Fatalf("%s accepted incomplete input: %v", tc.fault, readErr)
			} else if tc.fault != "budget" && !errors.Is(readErr, processing.ErrProtocolInvalid) {
				t.Fatalf("terminal failure returned %v", readErr)
			}
			fixture.source.mu.Lock()
			requests := append([]content.SourceRequest(nil), fixture.source.requests...)
			closes := fixture.source.readerCloses
			fixture.source.mu.Unlock()
			readRequests := 0
			for _, request := range requests {
				if request.Mode == content.SourceModeStat {
					continue
				}
				if tc.mode == content.SourceModeRange {
					if request.Range == nil || request.Range.Offset != int64(readRequests)*chunk || request.Range.Length != min(chunk, int64(len(tc.payload))-int64(readRequests)*chunk) {
						t.Fatalf("range %d: %+v", readRequests, request.Range)
					}
				} else if request.Mode != content.SourceModeSequential || request.Range != nil {
					t.Fatalf("sequential source request=%+v", request)
				}
				readRequests++
			}
			if readRequests != closes {
				t.Fatalf("opened reads=%d closes=%d", readRequests, closes)
			}
			if tc.name == "large_ranges" && readRequests != 33 {
				t.Fatalf("range reads=%d, want 33", readRequests)
			}
			if tc.name == "sequential" && readRequests != 1 {
				t.Fatalf("sequential reads=%d", readRequests)
			}
			if tc.name == "grant_budget" && readRequests != 2 {
				t.Fatalf("grant did not prevent third source open: %d", readRequests)
			}
			var grant model.BackupAssetProcessingGrant
			if err := fixture.db.First(&grant, "id = ?", activation.SessionID).Error; err != nil {
				t.Fatal(err)
			}
			if grant.RequestCount != int64(readRequests+1) || grant.InFlight != 0 || grant.ReservedBytes != 0 {
				t.Fatalf("unexpected grant accounting: requests=%d in_flight=%d reserved=%d", grant.RequestCount, grant.InFlight, grant.ReservedBytes)
			}
		})
	}
	for _, fault := range []string{"", "close", "copy"} {
		t.Run("wire_"+fault, func(t *testing.T) {
			fixture := newWorkerInputHTTPFixture(t, []byte("content"), true, fault, 3)
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", fixture.socket)
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			payload, err := json.Marshal(processing.WorkerInputReadRequest{SchemaVersion: 1, WorkerID: fixture.workerID, InstanceID: fixture.instanceID, SessionID: fixture.activation.SessionID, Mode: content.SourceModeRange, Length: 7})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Post("http://worker/internal/v1/asset-worker/input-sessions/"+fixture.activation.SessionID+"/ranges", "application/json", bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			wantStatus := "ok"
			if fault != "" {
				wantStatus = "failed"
			}
			statuses := response.Trailer.Values("X-Xirang-Input-Status")
			if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || len(statuses) != 1 || statuses[0] != wantStatus || strings.Contains(string(body), "private") {
				t.Fatalf("HTTP status=%d trailer=%v body=%q read=%v close=%v", response.StatusCode, statuses, body, readErr, closeErr)
			}
		})
	}
}

type workerInputHTTPFixture struct {
	db         *gorm.DB
	socket     string
	client     *processing.WorkerClient
	activation processing.WorkerInputActivation
	workerID   string
	instanceID string
	source     *workerInputHTTPSource
}

func newWorkerInputHTTPFixture(t *testing.T, payload []byte, rangeProven bool, fault string, maxRequests int64) workerInputHTTPFixture {
	t.Helper()
	const capBytes = int64(64 << 10)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "worker.sqlite")+"?_busy_timeout=5000&_txlock=immediate&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetWorkerIdentity{}, &model.BackupAssetWorkerCapability{}, &model.BackupAssetProcessingJob{},
		&model.BackupAssetProcessingInterest{}, &model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{}, &model.BackupAssetProcessingGrantRequest{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	workerID := strings.Repeat("8", 32)
	instanceID := workerID
	digest := sha256.Sum256(payload)
	descriptor := processing.WorkDescriptorV1{
		SchemaVersion: 1, Source: backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)},
		CatalogGenerationID: strings.Repeat("c", 32), SourceFingerprint: "source-fingerprint-v1", EntryFingerprint: fmt.Sprintf("%x", digest),
		ProviderCapabilityRevision: 7, Capability: "noop", CapabilitySchema: "noop.v1", PipelineFingerprint: "pipeline-fingerprint-v1",
		OutputProfile: "noop.v1", SecurityPolicyRevision: "security-policy-v1",
		Parameters: processing.CanonicalParametersV1{
			SchemaVersion: 1, Width: 1280, Height: 720, Codec: "png", PageStart: 1, PageEnd: 3, Quality: 80,
			Language: "zh-CN", Model: "noop-model-v1", FontProfile: "bundled-v1", MemberStart: 0, MemberEnd: 2,
			FrameStart: 0, FrameEnd: 10, TimeStartMillis: 0, TimeEndMillis: 1000, Orientation: "auto",
			CropX: 0, CropY: 0, CropWidth: 1280, CropHeight: 720, MaxPages: 100, MaxPixels: 100_000_000,
			MaxDurationMillis: 60_000, MaxExpandedBytes: 1 << 30, MaxOutputBytes: 1 << 28, MaxOutputCount: 32, TruncationPolicy: "reject",
		},
	}
	for _, row := range []any{
		&model.RecoveryPoint{ID: descriptor.Source.RecoveryPointID, RepositoryID: strings.Repeat("f", 32), Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted)},
		&model.BackupAssetWorkerIdentity{ID: workerID, TransportKind: "local", TransportFingerprint: strings.Repeat("8", 64), InstanceID: instanceID, IdentityRevision: 1, ProtocolVersion: 1, TrustState: "active", HealthState: "ready", InteractiveSlots: 2, BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now},
		&model.BackupAssetWorkerCapability{ID: strings.Repeat("8", 31) + "f", WorkerID: workerID, Capability: descriptor.Capability, CapabilitySchema: descriptor.CapabilitySchema, PipelineFingerprint: descriptor.PipelineFingerprint, OutputProfile: descriptor.OutputProfile, InputModes: "stat,sequential,range", LimitsCanonical: []byte{1}, AdvertisementDigest: strings.Repeat("a", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	lease, err := backupasset.NewLeaseService(db, clock, backupasset.LeaseConfig{Duration: 30 * time.Second, Heartbeat: 10 * time.Second, AbsoluteDeadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := processing.NewCoordinator(db, lease, clock, processing.CoordinatorConfig{QueueMax: 100, InteractiveReservedSlots: 2, BackgroundSlots: 2, PullLease: 30 * time.Second, AttemptTimeout: 2 * time.Hour, RetryMax: 5})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := processing.NewGrantService(db, lease, clock, processing.GrantConfig{
		TTL: 30 * time.Second, InputLimits: processing.GrantLimits{MaxRequests: maxRequests, MaxBytesPerRequest: capBytes, MaxCumulativeBytes: 4 << 20, MaxInFlight: 1},
		SinkLimits: processing.GrantLimits{MaxRequests: 4, MaxBytesPerRequest: 1024, MaxCumulativeBytes: 4096, MaxInFlight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := &workerInputHTTPSource{payload: payload, rangeProven: rangeProven, fault: fault}
	broker, err := content.NewAttemptBroker(source, grants, clock)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := processing.NewProtocolService(db, processing.NewProductionCapabilityRegistry(), clock)
	if err != nil {
		t.Fatal(err)
	}
	service, err := processing.NewWorkerProtocolService(protocol, coordinator, grants, broker, workerInputUnusedSink{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	work, err := coordinator.RequestWork(context.Background(), processing.WorkRequest{Descriptor: descriptor, Interest: processing.InterestRequest{OwnerKind: processing.InterestSystem, OwnerKey: "worker-input-http", PriorityClass: processing.PriorityInteractive, Priority: 100}})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewWorkerRouter(service, WorkerRouterConfig{JSONMaxBytes: 64 << 10, ArtifactMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	identity := processing.WorkerTransportIdentity{Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("8", 64), PeerUID: 1000}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(ContextWithWorkerTransportIdentity(r.Context(), identity)))
	}))
	_ = server.Listener.Close()
	socket := filepath.Join(t.TempDir(), "w.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	client, err := processing.NewWorkerClient(processing.WorkerClientConfig{LocalSocketPath: socket, InputMaxBytes: capBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	envelope, err := client.Pull(context.Background(), processing.WorkerPullRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID})
	if err != nil || envelope.JobID != work.JobID {
		t.Fatalf("pull=%+v err=%v", envelope, err)
	}
	activation, err := client.ActivateInput(context.Background(), processing.WorkerInputActivateRequest{
		SchemaVersion: 1, WorkerID: workerID, InstanceID: instanceID, JobID: work.JobID, AttemptID: envelope.AttemptID,
		ExpectedRevision: envelope.TransitionRevision, GrantID: envelope.InputActivation.GrantID, Secret: envelope.InputActivation.Secret,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	return workerInputHTTPFixture{db: db, socket: socket, client: client, activation: activation, workerID: workerID, instanceID: instanceID, source: source}
}

type workerInputUnusedSink struct{}

func (workerInputUnusedSink) UploadArtifact(context.Context, processing.UploadArtifactRequest, io.Reader) (processing.UploadedArtifact, error) {
	return processing.UploadedArtifact{}, errors.New("unused")
}
func (workerInputUnusedSink) CommitManifest(context.Context, processing.CommitManifestRequest) (processing.CommitManifestResult, error) {
	return processing.CommitManifestResult{}, errors.New("unused")
}

type workerInputHTTPSource struct {
	mu           sync.Mutex
	payload      []byte
	rangeProven  bool
	fault        string
	requests     []content.SourceRequest
	readerCloses int
}

func (source *workerInputHTTPSource) OpenContentSource(_ context.Context, request content.SourceRequest) (content.SourceSession, error) {
	source.mu.Lock()
	source.requests = append(source.requests, request)
	source.mu.Unlock()
	start, length := int64(0), int64(0)
	switch request.Mode {
	case content.SourceModeSequential:
		length = request.MaxBytes
	case content.SourceModeRange:
		start, length = request.Range.Offset, request.Range.Length
	}
	end := min(int64(len(source.payload)), start+length)
	last := request.Mode != content.SourceModeStat && end == int64(len(source.payload))
	reader := &workerInputHTTPSourceReader{owner: source, reader: bytes.NewReader(source.payload[start:end]), fault: source.fault, last: last}
	return &workerInputHTTPSession{source: source, request: request, reader: reader}, nil
}
func (*workerInputHTTPSource) ValidateContentCacheRoot(context.Context, string) error { return nil }

type workerInputHTTPSession struct {
	source  *workerInputHTTPSource
	request content.SourceRequest
	reader  *workerInputHTTPSourceReader
}

func (session *workerInputHTTPSession) Stat() content.SourceStat {
	return content.SourceStat{Size: int64(len(session.source.payload)), MediaType: "application/octet-stream", SourceFingerprint: session.request.ExpectedSource, EntryFingerprint: session.request.ExpectedEntry, FingerprintStrong: true}
}
func (session *workerInputHTTPSession) Capabilities() content.SourceCapabilities {
	return content.SourceCapabilities{Sequential: true, Range: session.source.rangeProven}
}
func (session *workerInputHTTPSession) Reader() content.SourceReader { return session.reader }
func (*workerInputHTTPSession) Revalidate(context.Context) error     { return nil }
func (*workerInputHTTPSession) Close() error                         { return nil }

type workerInputHTTPSourceReader struct {
	owner  *workerInputHTTPSource
	reader *bytes.Reader
	fault  string
	last   bool
	read   int64
}

func (reader *workerInputHTTPSourceReader) Read(p []byte) (int, error) {
	n, err := reader.reader.Read(p)
	reader.read += int64(n)
	if reader.last && reader.fault == "copy" && reader.reader.Len() == 0 {
		return n, errors.New("private copy failure")
	}
	return n, err
}
func (reader *workerInputHTTPSourceReader) Close() error {
	reader.owner.mu.Lock()
	reader.owner.readerCloses++
	reader.owner.mu.Unlock()
	if reader.last && reader.fault == "close" {
		return errors.New("private close failure")
	}
	return nil
}
func (reader *workerInputHTTPSourceReader) ProviderBytes() int64 { return reader.read }
