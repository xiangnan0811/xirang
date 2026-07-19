package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

func TestProcessingRuntimeDisabledOrUnconfiguredDoesNotCreateDerivedState(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		enabled bool
	}{
		{name: "feature disabled", enabled: false},
		{name: "no Worker transport", enabled: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openProcessingRuntimeTestDB(t)
			root := filepath.Join(t.TempDir(), "derived")
			foundation := processingRuntimeFoundation(t, db, root, testCase.enabled, false)
			keyring := backupasset.NewKeyring(db, time.Now)
			lease := processingRuntimeLease(t, db)
			runtime, err := newProcessingRuntime(processingRuntimeDependencies{
				DB: db, Foundation: foundation, Keyring: keyring, Lease: lease,
				Source: processingRuntimeSourceFake{}, ValidateRoot: processingRuntimeRootValidator,
				RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
			})
			if err != nil {
				t.Fatalf("newProcessingRuntime: %v", err)
			}
			if err := runtime.Startup(context.Background()); err != nil {
				t.Fatalf("Startup: %v", err)
			}
			if runtime.WorkerProtocol() != nil {
				t.Fatal("inactive runtime exposed a Worker protocol port")
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inactive runtime created Derived root: %v", err)
			}
			var count int64
			if err := db.Model(&model.WrappedDomainKey{}).
				Where("domain = ?", backupasset.KeyDomainDerivedStore).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("inactive runtime created %d Derived key rows", count)
			}
			summary, err := runtime.AdminSummary(context.Background())
			if err != nil {
				t.Fatalf("AdminSummary: %v", err)
			}
			if summary.Configured || summary.LocalEnabled || summary.RemoteEnabled || summary.Workers.Active != 0 || summary.Queue.Total != 0 {
				t.Fatalf("inactive runtime summary is noisy: %+v", summary)
			}
		})
	}
}

func TestProcessingRuntimeConfiguredBuildsOneEmptyCapabilityGraph(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	root := filepath.Join(t.TempDir(), "derived")
	foundation := processingRuntimeFoundation(t, db, root, true, true)
	keyring := backupasset.NewKeyring(db, time.Now)
	lease := processingRuntimeLease(t, db)
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: foundation, Keyring: keyring, Lease: lease,
		Source: processingRuntimeSourceFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
	})
	if err != nil {
		t.Fatalf("newProcessingRuntime: %v", err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if runtime.coordinator == nil || runtime.grants == nil || runtime.attemptBroker == nil || runtime.store == nil ||
		runtime.lifecycle == nil || runtime.sink == nil || runtime.reconciler == nil || runtime.derivedReconciler == nil || runtime.protocol == nil ||
		runtime.workerProtocol == nil {
		t.Fatal("configured runtime omitted a processing graph component")
	}
	if runtime.keyring != keyring || runtime.lease != lease {
		t.Fatal("processing runtime duplicated the shared keyring or lease service")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("configured runtime did not create Derived root: %v", err)
	}
	if _, err := keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatalf("configured runtime did not create Derived key: %v", err)
	}

	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64), PeerUID: uint32(os.Geteuid()),
	}
	protocolPort := runtime.WorkerProtocol()
	_, err = protocolPort.Handshake(context.Background(), identity, processing.HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		InstanceID: strings.Repeat("b", 32), IdentityRevision: 1, InteractiveSlots: 1,
		Capabilities: []processing.CapabilityAdvertisement{{
			SchemaVersion: 1, Capability: "thumbnail", CapabilitySchema: "thumbnail.v1",
			PipelineFingerprint: "test-pipeline", OutputProfile: "preview",
			InputModes: []processing.ProtocolInputMode{processing.ProtocolInputStat},
			Limits: processing.ProtocolCapabilityLimits{
				MaxInputBytes: 64 * 1024, MaxOutputBytes: 1, MaxOutputCount: 1,
				MaxPages: 1, MaxPixels: 1, MaxDurationMillis: 1, MaxExpandedBytes: 1,
			},
		}},
	})
	if !errors.Is(err, processing.ErrProtocolCapabilityUnsupported) {
		t.Fatalf("production runtime capability registry error=%v, want unsupported", err)
	}
	summary, err := runtime.AdminSummary(context.Background())
	if err != nil {
		t.Fatalf("AdminSummary: %v", err)
	}
	if !summary.Configured || !summary.LocalEnabled || summary.RemoteEnabled {
		t.Fatalf("configured runtime summary=%+v", summary)
	}
	runtime.StopAccepting()
	if _, err := protocolPort.Handshake(context.Background(), identity, processing.HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		InstanceID: strings.Repeat("b", 32), IdentityRevision: 1, InteractiveSlots: 1,
	}); !errors.Is(err, processing.ErrProtocolUnavailable) {
		t.Fatalf("captured Worker protocol admitted a handshake after runtime drain: %v", err)
	}
}

func TestProcessingRuntimeReconcilePublishesBoundedMetrics(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	metrics := &processingRuntimeMetricsFake{}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, filepath.Join(t.TempDir(), "derived"), true, true),
		Keyring: backupasset.NewKeyring(db, time.Now), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{}, Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if metrics.queueCalls == 0 || metrics.workerCalls == 0 || metrics.slotCalls == 0 || metrics.derivedCalls == 0 {
		t.Fatalf("Processing reconcile did not publish aggregate metrics: %+v", metrics)
	}
}

func TestProcessingRuntimePublishMetricsReadsSQLiteQueueAge(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	fixedNow := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	metrics := &processingRuntimeMetricsFake{}
	runtime := &managedProcessingRuntime{
		db: db, metrics: metrics, now: func() time.Time { return fixedNow },
		config: backupasset.ProcessingConfig{
			InteractiveSlots: 2, BackgroundSlots: 2,
			DerivedStore: backupasset.ProcessingDerivedStoreConfig{GlobalMaxBytes: 1024},
		},
	}
	job := model.BackupAssetProcessingJob{
		ID: strings.Repeat("1", 32), WorkKey: strings.Repeat("2", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{"schema_version":1}`), RecoveryPointID: strings.Repeat("3", 32),
		CatalogGenerationID: strings.Repeat("4", 32), EntryID: strings.Repeat("5", 64), SourceFingerprint: "source",
		ProviderCapabilityRevision: 1, Capability: "noop", CapabilitySchema: "noop.v1", PipelineFingerprint: "pipeline",
		OutputProfile: "noop.v1", SecurityPolicyRevision: "policy", PriorityClass: string(processing.PriorityInteractive),
		EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
		QueuedAt: fixedNow.Add(-90 * time.Second), AbsoluteDeadline: fixedNow.Add(time.Hour),
		CreatedAt: fixedNow.Add(-90 * time.Second), UpdatedAt: fixedNow.Add(-90 * time.Second), Version: 1,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	runtime.publishMetrics(context.Background())
	value := metrics.queueValues[string(processing.PriorityInteractive)+"/"+string(processing.ProcessingQueued)]
	if value.count != 1 || value.age < 89*time.Second || value.age > 91*time.Second {
		t.Fatalf("SQLite queue metrics=%+v, want count=1 age=90s", value)
	}
}

func TestProcessingRuntimeReconcileCompletesPendingProjectionBeforeLeaseExpiry(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	projection := &processingRuntimeRecoveringProjectionFake{}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, filepath.Join(t.TempDir(), "derived"), true, true),
		Keyring: backupasset.NewKeyring(db, time.Now), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: projection,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	descriptor := processingRuntimeWorkDescriptor()
	now := time.Now().UTC()
	workerID := strings.Repeat("4", 32)
	worker := model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("4", 64),
		InstanceID: strings.Repeat("4", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", InteractiveSlots: 1, BackgroundSlots: 1,
		LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	capability := model.BackupAssetWorkerCapability{
		ID: strings.Repeat("5", 32), WorkerID: workerID, Capability: descriptor.Capability,
		CapabilitySchema: descriptor.CapabilitySchema, PipelineFingerprint: descriptor.PipelineFingerprint,
		OutputProfile: descriptor.OutputProfile, InputModes: "stat,sequential", LimitsCanonical: []byte{1},
		AdvertisementDigest: strings.Repeat("5", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&worker).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	work, err := runtime.coordinator.RequestWork(context.Background(), processing.WorkRequest{
		Descriptor: descriptor,
		Interest: processing.InterestRequest{
			OwnerKind: processing.InterestSystem, OwnerKey: "runtime-projection-recovery",
			PriorityClass: processing.PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := runtime.coordinator.PullAttempt(context.Background(), processing.PullRequest{WorkerID: workerID}, runtime.grants)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := runtime.grants.Activate(context.Background(), processing.ActivateGrantRequest{
		GrantID: leased.Grants.Sink.GrantID, Kind: processing.GrantSink, JobID: work.JobID,
		AttemptID: leased.Lease.AttemptID, WorkerID: workerID, Secret: leased.Grants.Sink.Secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", work.JobID).
		Updates(map[string]any{"state": string(processing.ProcessingUploading), "transition_revision": int64(5)}).Error; err != nil {
		t.Fatal(err)
	}
	payload := []byte("runtime-pending-projection")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	declaration := processing.ArtifactDeclaration{
		Ordinal: 0, Role: processing.ArtifactRoleContent, MediaType: "text/plain", PlaintextSize: int64(len(payload)),
		PlaintextDigest: digest, Completeness: processing.ArtifactComplete,
		CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`),
	}
	if _, err := runtime.sink.UploadArtifact(context.Background(), processing.UploadArtifactRequest{
		JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: workerID,
		GrantID: activated.SessionID, Artifact: declaration,
	}, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.sink.CommitManifest(context.Background(), processing.CommitManifestRequest{
		JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: workerID,
		GrantID: activated.SessionID, RecoveryPointFence: leased.Lease.RecoveryPointFence,
		SecurityPolicyRevision: descriptor.SecurityPolicyRevision, Artifacts: []processing.ArtifactDeclaration{declaration},
	}); err == nil {
		t.Fatal("first projection publication unexpectedly acknowledged")
	}
	if err := runtime.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	var set model.BackupAssetDerivedArtifactSet
	if err := db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&set, "id = ?", job.CurrentArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if projection.calls != 2 || job.State != string(processing.ProcessingSucceeded) || !set.ProjectionPublished || set.ProjectionRevision != 17 {
		t.Fatalf("runtime did not recover pending projection: calls=%d job=%+v set=%+v", projection.calls, job, set)
	}
}

func TestProcessingRuntimeMarksUnreadableDerivedKeyLostAndStaysUnavailable(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	root := filepath.Join(t.TempDir(), "derived")
	keyring := backupasset.NewKeyring(db, time.Now)
	material, err := keyring.Ensure(context.Background(), backupasset.KeyDomainDerivedStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND version = ?", backupasset.KeyDomainDerivedStore, material.Version).
		Update("wrapped_key", []byte("corrupt-derived-envelope")).Error; err != nil {
		t.Fatal(err)
	}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, root, true, true), Keyring: keyring,
		Lease: processingRuntimeLease(t, db), Source: processingRuntimeSourceFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); !errors.Is(err, processing.ErrDerivedStoreUnavailable) {
		t.Fatalf("Startup unreadable Derived key error=%v, want unavailable", err)
	}
	if runtime.WorkerProtocol() != nil {
		t.Fatal("runtime exposed Worker protocol after Derived key loss")
	}
	if _, err := keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("unreadable Derived key was not recorded lost: %v", err)
	}
}

func TestProcessingRuntimeShutdownRetiresAttemptsGrantsAndRecoveryLease(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	root := filepath.Join(t.TempDir(), "derived")
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, root, true, true),
		Keyring: backupasset.NewKeyring(db, time.Now), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	descriptor := processingRuntimeWorkDescriptor()
	now := time.Now().UTC()
	workerID := strings.Repeat("6", 32)
	worker := model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("6", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", InteractiveSlots: 1, BackgroundSlots: 1,
		LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	capability := model.BackupAssetWorkerCapability{
		ID: strings.Repeat("7", 32), WorkerID: workerID, Capability: descriptor.Capability,
		CapabilitySchema: descriptor.CapabilitySchema, PipelineFingerprint: descriptor.PipelineFingerprint,
		OutputProfile: descriptor.OutputProfile, InputModes: "stat,sequential", LimitsCanonical: []byte{1},
		AdvertisementDigest: strings.Repeat("8", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&worker).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	work, err := runtime.coordinator.RequestWork(context.Background(), processing.WorkRequest{
		Descriptor: descriptor,
		Interest: processing.InterestRequest{
			OwnerKind: processing.InterestSystem, OwnerKey: "runtime-shutdown",
			PriorityClass: processing.PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := runtime.workerProtocol.Pull(context.Background(), processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: worker.TransportFingerprint, PeerUID: uint32(os.Geteuid()),
	}, processing.WorkerPullRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: worker.InstanceID})
	if err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var lease model.RecoveryPointLease
	var grants []model.BackupAssetProcessingGrant
	if err := db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&attempt, "id = ?", envelope.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&lease, "id = ?", envelope.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("attempt_id = ?", envelope.AttemptID).Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(processing.ProcessingRetryWait) || job.CurrentAttemptID != nil ||
		attempt.State != "canceled" || attempt.IsCurrent || lease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("shutdown left Processing authority active: job=%+v attempt=%+v lease=%+v", job, attempt, lease)
	}
	if len(grants) != 2 {
		t.Fatalf("shutdown grant count=%d, want 2", len(grants))
	}
	for _, grant := range grants {
		if grant.State != string(processing.GrantRevoked) || grant.RevocationReason != "shutdown" {
			t.Fatalf("shutdown left grant active: %+v", grant)
		}
	}
}

func processingRuntimeWorkDescriptor() processing.WorkDescriptorV1 {
	return processing.WorkDescriptorV1{
		SchemaVersion: 1,
		Source: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64),
		},
		CatalogGenerationID: strings.Repeat("c", 32), SourceFingerprint: "runtime-source-v1",
		EntryFingerprint: "runtime-entry-v1", ProviderCapabilityRevision: 1,
		Capability: "noop", CapabilitySchema: "noop.v1", PipelineFingerprint: "runtime-noop-v1",
		OutputProfile: "noop.v1", SecurityPolicyRevision: "security-policy-v1",
		Parameters: processing.CanonicalParametersV1{
			SchemaVersion: 1, Width: 1, Height: 1, Codec: "noop", PageStart: 1, PageEnd: 1,
			Quality: 1, Language: "none", Model: "noop", FontProfile: "none",
			Orientation: "none", CropWidth: 1, CropHeight: 1,
			MaxPages: 1, MaxPixels: 1, MaxDurationMillis: 1, MaxExpandedBytes: 1,
			MaxOutputBytes: 1, MaxOutputCount: 1, TruncationPolicy: "reject",
		},
	}
}

func openProcessingRuntimeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{},
		&model.BackupAssetProcessingGrantRequest{}, &model.BackupAssetProcessingUpload{},
		&model.BackupAssetWorkerIdentity{}, &model.BackupAssetWorkerCapability{},
		&model.BackupAssetDerivedBlob{}, &model.BackupAssetDerivedArtifactSet{},
		&model.BackupAssetDerivedArtifact{}, &model.BackupAssetDerivedBlobReference{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func processingRuntimeFoundation(t *testing.T, db *gorm.DB, root string, enabled, local bool) *backupasset.FoundationService {
	t.Helper()
	service := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.derived_store_root":   root,
		"backup_assets.enabled":              fmt.Sprintf("%t", enabled),
		"backup_assets.worker_local_enabled": fmt.Sprintf("%t", local),
	} {
		if err := service.Update(key, value); err != nil {
			t.Fatalf("update %s: %v", key, err)
		}
	}
	return backupasset.NewFoundationService(service)
}

func processingRuntimeLease(t *testing.T, db *gorm.DB) *backupasset.LeaseService {
	t.Helper()
	service, err := backupasset.NewLeaseService(db, time.Now, backupasset.LeaseConfig{
		Duration: 2 * time.Minute, Heartbeat: 20 * time.Second, AbsoluteDeadline: 3 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type processingRuntimeSourceFake struct{}

func (processingRuntimeSourceFake) OpenContentSource(context.Context, content.SourceRequest) (content.SourceSession, error) {
	return nil, errors.New("not used")
}

func (processingRuntimeSourceFake) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

type processingRuntimeSourceRevalidatorFake struct{}

func (processingRuntimeSourceRevalidatorFake) RevalidateProcessingSource(context.Context, processing.WorkDescriptorV1) error {
	return nil
}

type processingRuntimeProjectionFake struct{}

func (processingRuntimeProjectionFake) Publish(_ context.Context, request processing.DerivedProjectionPublish) (processing.DerivedProjectionPublication, error) {
	return processing.DerivedProjectionPublication{ArtifactSetID: request.ArtifactSetID, Revision: 1}, nil
}

func (processingRuntimeProjectionFake) Revoke(context.Context, processing.DerivedProjectionRevoke) error {
	return nil
}

type processingRuntimeRecoveringProjectionFake struct {
	calls int
}

func (fake *processingRuntimeRecoveringProjectionFake) Publish(_ context.Context, request processing.DerivedProjectionPublish) (processing.DerivedProjectionPublication, error) {
	fake.calls++
	if fake.calls == 1 {
		return processing.DerivedProjectionPublication{}, errors.New("projection acknowledgement lost")
	}
	return processing.DerivedProjectionPublication{ArtifactSetID: request.ArtifactSetID, Revision: 17}, nil
}

func (*processingRuntimeRecoveringProjectionFake) Revoke(context.Context, processing.DerivedProjectionRevoke) error {
	return nil
}

type processingRuntimeMetricsFake struct {
	queueCalls   int
	workerCalls  int
	slotCalls    int
	derivedCalls int
	queueValues  map[string]processingRuntimeQueueMetric
}

type processingRuntimeQueueMetric struct {
	count int64
	age   time.Duration
}

func (metrics *processingRuntimeMetricsFake) ObserveJob(processing.PriorityClass, processing.ProcessingState, processing.ProcessingErrorCategory) {
}
func (metrics *processingRuntimeMetricsFake) ObserveLeaseLoss() {}
func (metrics *processingRuntimeMetricsFake) ObserveJobDuration(processing.PriorityClass, processing.ProcessingState, time.Duration) {
}
func (metrics *processingRuntimeMetricsFake) SetWorkers(processing.WorkerTrustClass, processing.WorkerHealthClass, int64) {
	metrics.workerCalls++
}
func (metrics *processingRuntimeMetricsFake) SetSlots(processing.SlotClass, processing.SlotMetricKind, int64) {
	metrics.slotCalls++
}
func (metrics *processingRuntimeMetricsFake) SetQueue(priority processing.PriorityClass, state processing.ProcessingState, count int64, age time.Duration) {
	metrics.queueCalls++
	if metrics.queueValues == nil {
		metrics.queueValues = make(map[string]processingRuntimeQueueMetric)
	}
	metrics.queueValues[string(priority)+"/"+string(state)] = processingRuntimeQueueMetric{count: count, age: age}
}
func (metrics *processingRuntimeMetricsFake) AddSinkBytes(int64) {}
func (metrics *processingRuntimeMetricsFake) SetDerived(processing.DerivedMetricKind, int64) {
	metrics.derivedCalls++
}
func (metrics *processingRuntimeMetricsFake) ObserveDerived(processing.DerivedMetricEvent) {}

func processingRuntimeRootValidator(context.Context, string) error { return nil }
