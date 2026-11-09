package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
)

type sharedOutputSource struct{}

func (sharedOutputSource) RevalidateProcessingSource(context.Context, processing.WorkDescriptorV1) error {
	return nil
}

type sharedOutputProjection struct{}

func (sharedOutputProjection) PreparePublish(context.Context, processing.DerivedProjectionPublish) (processing.PreparedDerivedProjection, error) {
	return nil, errors.New("noop output must not request Search projection")
}
func (sharedOutputProjection) PrepareRevoke(context.Context, processing.DerivedProjectionRevoke) (processing.PreparedDerivedRevocation, error) {
	return nil, errors.New("unexpected projection revocation")
}

// The source and transport identity are controlled fixtures. The listener,
// router, client, coordinator, grants, Sink and encrypted store are real.
// This does not exercise peer credentials, a parser, or Search publication.
func TestWorkerSinkSharedOutputUnixHTTP(t *testing.T) {
	ctx := context.Background()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_WORKER_SHARED_OUTPUT_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "worker-shared.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	input := []byte("controlled-source")
	output := []byte("synthetic-noop-output")
	inputDigest := sha256.Sum256(input)
	descriptor := processing.WorkDescriptorV1{
		SchemaVersion: 1, Source: backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)},
		CatalogGenerationID: strings.Repeat("c", 32), SourceFingerprint: "source-fingerprint-v1", EntryFingerprint: hex.EncodeToString(inputDigest[:]),
		ProviderCapabilityRevision: 7, Capability: "noop", CapabilitySchema: "noop.v1", PipelineFingerprint: "pipeline-fingerprint-v1",
		OutputProfile: "noop.v1", SecurityPolicyRevision: "security-policy-v1",
		Parameters: processing.CanonicalParametersV1{SchemaVersion: 1, Width: 1280, Height: 720, Codec: "png", PageStart: 1, PageEnd: 3, Quality: 80,
			Language: "zh-CN", Model: "noop-model-v1", FontProfile: "bundled-v1", MemberStart: 0, MemberEnd: 2,
			FrameStart: 0, FrameEnd: 10, TimeStartMillis: 0, TimeEndMillis: 1000, Orientation: "auto",
			CropWidth: 1280, CropHeight: 720, MaxPages: 100, MaxPixels: 100_000_000, MaxDurationMillis: 60_000,
			MaxExpandedBytes: 1 << 30, MaxOutputBytes: 1 << 28, MaxOutputCount: 32, TruncationPolicy: "reject"},
	}
	repositoryID := strings.Repeat("f", 32)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO backup_repositories (id, provider_kind, repository_identity, display_name, description, version_mode, status, capability_revision, capabilities_json, immutability_level, created_at, updated_at) VALUES (?, 'rsync', 'worker-shared', 'worker-shared', '', 'hardlink_tree', 'online', ?, '{}', 'xirang_managed', ?, ?)`, []any{repositoryID, descriptor.ProviderCapabilityRevision, now, now}},
		{`INSERT INTO recovery_points (id, repository_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot, lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, observed_at, source_fingerprint, manifest_digest_algorithm, manifest_digest, entry_count, logical_bytes, consistency_json, fidelity_json, capability_revision, capabilities_json, immutability_level, physical_availability, hold_state, created_at, updated_at) VALUES (?, ?, '', 0, '', '{}', '', '', 'mutable_head', 'observed', ?, ?, 'sha256', '', 1, ?, '{}', '{}', ?, '{}', 'mutable', 'online', 'none', ?, ?)`, []any{descriptor.Source.RecoveryPointID, repositoryID, now, descriptor.SourceFingerprint, len(input), descriptor.ProviderCapabilityRevision, now, now}},
		{`INSERT INTO catalog_generations (id, recovery_point_id, generation, state, is_active, source_fingerprint, expected_entry_count, written_entry_count, expected_digest, written_digest, error_code, correlation_id, started_at, finished_at, created_at, updated_at) VALUES (?, ?, 1, 'complete', ?, ?, 1, 1, '', '', '', '', ?, ?, ?, ?)`, []any{descriptor.CatalogGenerationID, descriptor.Source.RecoveryPointID, true, descriptor.SourceFingerprint, now, now, now, now}},
		{`INSERT INTO catalog_entries (generation_id, entry_id, recovery_point_id, normalized_path, name, entry_type, size, mode, owner, mime_type, fingerprint, fingerprint_strength, encrypted_provider_locator, security_state, created_at) VALUES (?, ?, ?, '/input.bin', 'input.bin', 'file', ?, '', '', 'application/octet-stream', ?, 'strong', '', 'non_secret', ?)`, []any{descriptor.CatalogGenerationID, descriptor.Source.EntryID, descriptor.Source.RecoveryPointID, len(input), descriptor.EntryFingerprint, now}},
	}
	for _, statement := range statements {
		if err := db.Exec(statement.query, statement.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	workerID := strings.Repeat("8", 32)
	for _, row := range []any{
		&model.BackupAssetWorkerIdentity{ID: workerID, TransportKind: "local", TransportFingerprint: strings.Repeat("8", 64), InstanceID: workerID, IdentityRevision: 1, ProtocolVersion: 1, TrustState: "active", HealthState: "ready", InteractiveSlots: 2, BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now},
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
	coordinator, err := processing.NewCoordinator(db, lease, clock, processing.CoordinatorConfig{QueueMax: 100, InteractiveReservedSlots: 2, BackgroundSlots: 2, PullLease: 30 * time.Second, AttemptTimeout: 2 * time.Hour, RetryMax: 5, BackfillPolicySource: workerInputTestBackfillPolicy})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := processing.NewGrantService(db, lease, clock, processing.GrantConfig{TTL: 30 * time.Second,
		InputLimits: processing.GrantLimits{MaxRequests: 4, MaxBytesPerRequest: 64 << 10, MaxCumulativeBytes: 4 << 20, MaxInFlight: 1},
		SinkLimits:  processing.GrantLimits{MaxRequests: 4, MaxBytesPerRequest: 1024, MaxCumulativeBytes: 4096, MaxInFlight: 1}})
	if err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, clock)
	if _, err := keyring.Ensure(ctx, backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatal(err)
	}
	store, err := processing.NewDerivedStore(ctx, db, keyring, processing.DerivedStoreConfig{Root: filepath.Join(t.TempDir(), "derived"), ChunkSize: 64 << 10, BlobMaxBytes: 1 << 20, GlobalMaxBytes: 8 << 20, ValidateRoot: func(context.Context, string) error { return nil }}, clock)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := processing.NewDerivedLifecycle(db, store, sharedOutputProjection{}, clock, lease)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := processing.NewArtifactSink(db, lease, grants, store, lifecycle, sharedOutputSource{}, func(context.Context) (string, error) { return descriptor.SecurityPolicyRevision, nil }, func(context.Context, string, string) (string, error) { return descriptor.PipelineFingerprint, nil }, clock, processing.ArtifactSinkConfig{MaxArtifacts: 4, MaxArtifactBytes: 1024, MaxTotalBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := content.NewAttemptBroker(&workerInputHTTPSource{payload: input, rangeProven: true}, grants, clock)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := processing.NewProtocolService(db, processing.NewProductionCapabilityRegistry(), clock)
	if err != nil {
		t.Fatal(err)
	}
	service, err := processing.NewWorkerProtocolService(protocol, coordinator, grants, broker, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	router, err := NewWorkerRouter(service, WorkerRouterConfig{JSONMaxBytes: 64 << 10, ArtifactMaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	identity := processing.WorkerTransportIdentity{Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("8", 64), PeerUID: 1000}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(ContextWithWorkerTransportIdentity(r.Context(), identity)))
	}))
	_ = server.Listener.Close()
	socket := filepath.Join(t.TempDir(), "worker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	client, err := processing.NewWorkerClient(processing.WorkerClientConfig{LocalSocketPath: socket, InputMaxBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	digest := sha256.Sum256(output)
	declaration := processing.ArtifactDeclaration{Ordinal: 0, Role: processing.ArtifactRoleNoop, MediaType: "application/octet-stream", PlaintextSize: int64(len(output)), PlaintextDigest: hex.EncodeToString(digest[:]), Completeness: processing.ArtifactComplete, CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`)}
	var artifacts [2]model.BackupAssetDerivedArtifact
	var sets [2]string
	var jobs, attempts, grantIDs [2]string
	for i := range artifacts {
		descriptor.Parameters.Quality = 80 + i
		work, err := coordinator.RequestWork(ctx, processing.WorkRequest{Descriptor: descriptor, Interest: processing.InterestRequest{OwnerKind: processing.InterestSystem, OwnerKey: []string{"worker-shared-a", "worker-shared-b"}[i], PriorityClass: processing.PriorityInteractive, Priority: 100}})
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := client.Pull(ctx, processing.WorkerPullRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: workerID})
		if err != nil || envelope.JobID != work.JobID {
			t.Fatalf("pull job mismatch: %v", err)
		}
		jobs[i], attempts[i], grantIDs[i] = work.JobID, envelope.AttemptID, envelope.SinkActivation.GrantID
		activation, err := client.ActivateInput(ctx, processing.WorkerInputActivateRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: workerID, JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: envelope.TransitionRevision, GrantID: envelope.InputActivation.GrantID, Secret: envelope.InputActivation.Secret})
		if err != nil {
			t.Fatal(err)
		}
		inputBytes, err := client.ReadInput(ctx, processing.WorkerInputReadRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: workerID, SessionID: activation.SessionID, Mode: content.SourceModeRange, Length: int64(len(input))})
		if err != nil || !bytes.Equal(inputBytes, input) {
			t.Fatalf("controlled input read failed: %v", err)
		}
		revision := activation.TransitionRevision
		for _, state := range []processing.ProcessingState{processing.ProcessingProcessing, processing.ProcessingUploading} {
			transition, err := client.Transition(ctx, processing.WorkerTransitionRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: workerID, JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision, To: state})
			if err != nil {
				t.Fatal(err)
			}
			revision = transition.Revision
		}
		activated, err := client.ActivateSink(ctx, processing.WorkerSinkActivateRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: workerID, JobID: work.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision, GrantID: envelope.SinkActivation.GrantID, Secret: envelope.SinkActivation.Secret})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.UploadArtifact(ctx, processing.WorkerUploadArtifactRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: workerID, SessionID: activated.SessionID, JobID: work.JobID, AttemptID: envelope.AttemptID, Artifact: declaration}, bytes.NewReader(output)); err != nil {
			t.Fatal(err)
		}
		result, err := client.CommitManifest(ctx, processing.WorkerCommitManifestRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: workerID, SessionID: activated.SessionID, JobID: work.JobID, AttemptID: envelope.AttemptID, SecurityPolicyRevision: descriptor.SecurityPolicyRevision, Artifacts: []processing.ArtifactDeclaration{declaration}})
		if err != nil {
			t.Fatal(err)
		}
		sets[i] = result.ArtifactSetID
		if err := db.First(&artifacts[i], "artifact_set_id = ?", sets[i]).Error; err != nil {
			t.Fatal(err)
		}
		if result.ProjectionRequired {
			t.Fatal("noop output unexpectedly required Search projection")
		}
	}
	if jobs[0] == jobs[1] || attempts[0] == attempts[1] || grantIDs[0] == grantIDs[1] {
		t.Fatal("jobs, attempts, and grants must be independent")
	}
	for _, artifact := range artifacts {
		var received bytes.Buffer
		auth := processing.DerivedArtifactAuthorization{ArtifactID: artifact.ID, RecoveryPointID: descriptor.Source.RecoveryPointID, CatalogGenerationID: descriptor.CatalogGenerationID, EntryID: descriptor.Source.EntryID, SourceFingerprint: descriptor.SourceFingerprint}
		if err := lifecycle.ReadAuthorized(ctx, auth, &received); err != nil || !bytes.Equal(received.Bytes(), output) {
			t.Fatalf("authorized read failed: %v", err)
		}
	}
	if sets[0] == sets[1] || artifacts[0].ID == artifacts[1].ID || artifacts[0].BlobID != artifacts[1].BlobID {
		t.Fatal("independent publications did not share one blob")
	}
	var blob model.BackupAssetDerivedBlob
	if err := db.First(&blob, "id = ?", artifacts[0].BlobID).Error; err != nil || blob.RefCount != 2 {
		t.Fatalf("shared blob ref_count=%d: %v", blob.RefCount, err)
	}
	var references int64
	if err := db.Model(&model.BackupAssetDerivedBlobReference{}).Where("blob_id = ? AND state = ?", blob.ID, "active").Count(&references).Error; err != nil || references != 2 {
		t.Fatalf("active references=%d: %v", references, err)
	}
}
