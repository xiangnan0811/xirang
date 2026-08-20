package repository

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestRclonePreparePersistsStablePointFreshAttemptAndKeepsTaskRunTruthSeparate(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatalf("prepare managed Rclone publication: %v", err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	if execution.Mode() != publication.ModeEvidence || execution.Attempt() == nil {
		t.Fatalf("managed Rclone execution=%+v", execution)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil || attempt.Portable == nil || attempt.Native != nil || attempt.TaskRunID != fixture.taskRun.ID ||
		attempt.PointDeadlineAt != fixture.binding.PreflightExpiresAt || attempt.ConfigDigest != fixture.binding.Portable.ConfigDigest ||
		attempt.BindingRevision != fixture.binding.BindingRevision || attempt.CapabilityRevision != fixture.binding.CapabilityRevision {
		t.Fatalf("managed Rclone attempt=%+v err=%v", attempt, err)
	}
	inputProvider, ok := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	})
	if !ok {
		t.Fatalf("managed Rclone execution=%T has no typed input", execution)
	}
	input, err := inputProvider.RclonePublicationInput()
	if err != nil || input.PortableRequest == nil || input.NativeRequest != nil || input.PortableRequest.Attempt.AttemptID != attempt.AttemptID ||
		input.PortableRequest.Source != (provider.RclonePrivateLocator{}) || input.PortableRequest.Runtime.Node.ID != fixture.task.NodeID {
		t.Fatalf("managed Rclone input=%+v err=%v", input, err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	preparedAttempt, childFenceDigest, err := decodeManagedRclonePreparedAttemptRecord(point.EncryptedProviderLocator)
	if err != nil || preparedAttempt.AttemptID != attempt.AttemptID || childFenceDigest != attempt.ChildFenceDigest ||
		point.State != string(backupasset.RecoveryPointPreparing) || point.SourceFingerprint != "" {
		t.Fatalf("prepared point=%+v attempt=%+v fence=%q err=%v", point, preparedAttempt, childFenceDigest, err)
	}
	var taskRun model.TaskRun
	if err := fixture.db.First(&taskRun, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskRun.Status != "running" || taskRun.FinishedAt != nil || taskRun.LastError != "" {
		t.Fatalf("publication prepare rewrote TaskRun truth: %+v", taskRun)
	}
}

func TestRcloneNativePrepareRejectsSecondUnresolvedPhysicalWriter(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: fixture.link.ID, TaskID: fixture.task.ID, TaskRunID: fixture.taskRun.ID,
		Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeObjectVersions), PointCodecVersion: 1,
		StartedAt: *fixture.taskRun.StartedAt, PreparedAt: fixture.now, PointDeadlineAt: fixture.now.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	unresolved := model.RecoveryPoint{
		ID: strings.Repeat("8", 32), RepositoryID: fixture.repository.ID, ProducingTaskID: &fixture.task.ID,
		ProducingTaskRunID: &fixture.taskRun.ID, ProducingTaskNameSnapshot: fixture.task.Name,
		ProducingNodeIDSnapshot: fixture.task.NodeID, ProducingNodeNameSnapshot: fixture.task.Node.Name,
		LineageJSON: lineage, Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointPreparing),
		ManifestDigestAlgorithm: "sha256", ConsistencyJSON: consistency, FidelityJSON: "{}",
		CapabilityRevision: fixture.repository.CapabilityRevision, CapabilitiesJSON: fixture.repository.CapabilitiesJSON,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalUnknown),
		HoldState: string(backupasset.HoldNone), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&unresolved).Error; err != nil {
		t.Fatal(err)
	}
	secondRun := model.TaskRun{
		TaskID: fixture.task.ID, TriggerType: "manual", Status: "running",
		StartedAt: timePointer(fixture.now.Add(time.Minute)), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&secondRun).Error; err != nil {
		t.Fatal(err)
	}
	run := fixture.run()
	run.TaskRunID = secondRun.ID
	run.StartedAt = *secondRun.StartedAt
	_, err = fixture.service.Prepare(context.Background(), run)
	if !errors.Is(err, backupasset.ErrPublicationInProgress) && !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("second unresolved native writer error=%v", err)
	}
	var count int64
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("repository_id = ? AND state IN ?", fixture.repository.ID,
		[]string{string(backupasset.RecoveryPointPreparing), string(backupasset.RecoveryPointVerifying)}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("unresolved native point count=%d, want 1", count)
	}
}

func TestRcloneNativePrepareFreezesSessionB0AndEncryptionEvidence(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatalf("prepare native Rclone publication: %v", err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil || attempt.Native == nil || attempt.Portable != nil ||
		attempt.Native.ProfileCode != fixture.binding.Native.ProfileCode ||
		attempt.Native.RoleSessionIdentityDigest != fixture.nativeFactory.session.IdentityDigest() ||
		!attempt.Native.SessionExpiresAt.Equal(fixture.nativeFactory.session.ExpiresAt()) ||
		attempt.Native.B0VersionGraphDigest == "" || attempt.Native.EncryptionProfile != provider.RcloneNativeSSES3V1 ||
		attempt.Native.ActiveKeyDigest != "" || attempt.Native.RetainedReadKeySetDigest != "" || attempt.Native.KMSCapabilityRevision != 0 {
		t.Fatalf("native Rclone attempt=%+v err=%v", attempt, err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil || input.NativeRequest == nil || input.PortableRequest != nil ||
		input.NativeRequest.Source != (provider.RclonePrivateLocator{}) ||
		input.NativeRequest.Session.IdentityDigest() != attempt.Native.RoleSessionIdentityDigest ||
		input.NativeRequest.ClientFactory != fixture.nativeFactory ||
		input.NativeRequest.EncryptionEvidence.Profile != provider.RcloneNativeSSES3V1 ||
		len(input.NativeRequest.RcloneConfig) == 0 || digestText(string(input.NativeRequest.RcloneConfig)) != attempt.ConfigDigest {
		t.Fatalf("native Rclone input=%+v err=%v", input.NativeRequest, err)
	}
	if fixture.nativeFactory.assumeCalls != 3 || fixture.nativeFactory.listCalls != 2 || fixture.nativeFactory.probeCalls != 1 {
		t.Fatalf("native admission calls assume=%d list=%d probe=%d", fixture.nativeFactory.assumeCalls, fixture.nativeFactory.listCalls, fixture.nativeFactory.probeCalls)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	persisted, _, err := decodeManagedRclonePreparedAttemptRecord(point.EncryptedProviderLocator)
	if err != nil || persisted.Native == nil || persisted.Native.B0VersionGraphDigest != attempt.Native.B0VersionGraphDigest ||
		persisted.Native.RoleSessionIdentityDigest != attempt.Native.RoleSessionIdentityDigest {
		t.Fatalf("persisted native attempt=%+v err=%v", persisted, err)
	}
}

func TestRcloneProviderCommitAdvancesOnlyToVerifyingAndIsExactReplay(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	outcome, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit))
	if err != nil || outcome.State != backupasset.RecoveryPointVerifying || !outcome.ProviderCommitRecorded {
		t.Fatalf("record Rclone provider commit outcome=%+v err=%v", outcome, err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil || locator.AttemptID != attempt.AttemptID || locator.CommitPayloadDigest != commit.Portable.CommitPayloadDigest ||
		point.State != string(backupasset.RecoveryPointVerifying) || point.ManifestDigest != commit.ManifestIndexDigest ||
		point.EntryCount != int64(commit.ManifestEntryCount) || point.LogicalBytes != int64(commit.LogicalBytes) || point.SourceFingerprint == "" {
		t.Fatalf("verifying Rclone point=%+v locator=%+v err=%v", point, locator, err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil || consistency.Provider != backupasset.ProviderRclone || consistency.ProviderCommitDigest == "" ||
		consistency.CapabilityRevision != point.CapabilityRevision {
		t.Fatalf("Rclone provider consistency=%+v err=%v", consistency, err)
	}
	var taskRun model.TaskRun
	if err := fixture.db.First(&taskRun, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskRun.Status != "running" || taskRun.FinishedAt != nil || taskRun.LastError != "" {
		t.Fatalf("Rclone provider commit rewrote TaskRun truth: %+v", taskRun)
	}
	var latchCount int64
	if err := fixture.db.Model(&model.BackupAssetManagedHistoryLatch{}).Count(&latchCount).Error; err != nil {
		t.Fatal(err)
	}
	if latchCount != 2 {
		t.Fatalf("managed Rclone history latch count=%d, want 2", latchCount)
	}
	replayed, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit))
	if err != nil || replayed != outcome {
		t.Fatalf("exact Rclone commit replay=%+v err=%v want=%+v", replayed, err, outcome)
	}
	drifted := commit
	drifted.Portable = &provider.RclonePortableCommitV1{}
	*drifted.Portable = *commit.Portable
	drifted.Portable.CommitPayloadDigest = strings.Repeat("9", 64)
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(drifted)); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("drifted Rclone commit replay error=%v", err)
	}
}

func TestRcloneDeferralPersistsTypedOutcomeWithoutRewritingTaskRun(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	deferral := publication.Deferral{Completion: backupasset.CompletionOutcomeUnknown, Code: backupasset.FailureProviderTimeout}
	if err := execution.Defer(context.Background(), deferral); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil || consistency.Completion != deferral.Completion || consistency.Code != deferral.Code ||
		consistency.PublicationRevision != 1 || consistency.AttemptCount != 1 || consistency.LastAttemptAt == nil ||
		point.State != string(backupasset.RecoveryPointPreparing) {
		t.Fatalf("deferred Rclone point=%+v consistency=%+v err=%v", point, consistency, err)
	}
	if err := execution.Defer(context.Background(), deferral); err != nil {
		t.Fatal(err)
	}
	var replayed model.RecoveryPoint
	if err := fixture.db.First(&replayed, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	if replayed.ConsistencyJSON != point.ConsistencyJSON {
		t.Fatalf("exact Rclone deferral replay changed evidence: before=%s after=%s", point.ConsistencyJSON, replayed.ConsistencyJSON)
	}
	var taskRun model.TaskRun
	if err := fixture.db.First(&taskRun, fixture.taskRun.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskRun.Status != "running" || taskRun.FinishedAt != nil || taskRun.LastError != "" {
		t.Fatalf("Rclone deferral rewrote TaskRun truth: %+v", taskRun)
	}
}

type rclonePublicationFixture struct {
	t             *testing.T
	db            *gorm.DB
	now           time.Time
	service       *PublicationService
	task          model.Task
	taskRun       model.TaskRun
	repository    model.BackupRepository
	link          model.TaskRepositoryLink
	binding       managedRcloneBindingDocumentV3
	strategy      *rcloneRepositoryStrategyStub
	nativeFactory *rcloneNativeRepositoryFactoryFake
}

func validRcloneRepositoryCommit(attempt provider.RcloneAttemptV1, costDigest string, committedAt time.Time) provider.RcloneCommitV1 {
	return provider.RcloneCommitV1{
		SchemaVersion: 1, LayoutVersion: attempt.LayoutVersion, MinimumRuntimeRevision: attempt.MinimumRuntimeRevision,
		RepositoryID: attempt.RepositoryID, TaskRepositoryLinkID: attempt.TaskRepositoryLinkID,
		RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID, PublicationMode: attempt.PublicationMode,
		PointDeadlineAt: attempt.PointDeadlineAt, ProviderCommittedAt: committedAt.UTC(),
		ManifestIndexDigest: strings.Repeat("1", 64), ManifestChunkDigests: []string{strings.Repeat("2", 64)},
		ManifestEntryCount: 1, LogicalBytes: 5, SourceObservationDigest: strings.Repeat("3", 64),
		DestinationObservationDigest: strings.Repeat("4", 64), ContentProofDigest: strings.Repeat("5", 64),
		FidelityEvidenceDigest: strings.Repeat("6", 64), CostEvidenceDigest: costDigest,
		CapabilityEvidenceDigest: attempt.PreflightDigest, ChildFenceDigest: attempt.ChildFenceDigest,
		Portable: &provider.RclonePortableCommitV1{
			AttemptIdentityDigest: strings.Repeat("7", 64), ControlIdentityDigest: strings.Repeat("8", 64),
			DataIdentityDigest: strings.Repeat("9", 64), AttemptMarkerDigest: attempt.Portable.AttemptMarkerDigest,
			CommitComponent: "commit.json", CommitPayloadDigest: strings.Repeat("a", 64),
			CommitAuthenticationDigest: strings.Repeat("b", 64), ConsistencyEvidenceDigest: strings.Repeat("c", 64),
			HashEvidenceDigest: strings.Repeat("d", 64), DownloadVerifiedBytes: 5,
		},
	}
}

func newRclonePublicationFixture(t *testing.T, mode backupasset.TaskPublicationMode) *rclonePublicationFixture {
	t.Helper()
	db := newRepositoryTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	legacyPolicy := `{"version":1,"publication_mode":"legacy_mutable","bandwidth_limit":"10M","transfers":4}`
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", legacyPolicy)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]any{
		"rsync_target": "", "updated_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Preload("Node").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	taskRun := model.TaskRun{
		TaskID: taskEntity.ID, TriggerType: "manual", Status: "running",
		StartedAt: timePointer(now.Add(-time.Minute)), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&taskRun).Error; err != nil {
		t.Fatal(err)
	}
	var binding managedRcloneBindingDocumentV3
	if mode == backupasset.PublicationVersionedPrefix {
		binding = validManagedRclonePortableBindingForTest(t)
	} else {
		binding = validManagedRcloneNativeBindingForTest(t, provider.RcloneNativeSSES3V1)
		versioningDigest, digestErr := provider.CanonicalRcloneNativeVersioningDigest(
			provider.RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"},
		)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		binding.Native.VersioningDigest = versioningDigest
		lifecycleDigest, digestErr := provider.CanonicalRcloneNativeLifecycleDigest(provider.RcloneNativeLifecycleObservation{})
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		binding.Native.LifecycleDigest = lifecycleDigest
		bucketEncryptionDigest, digestErr := provider.CanonicalRcloneNativeBucketEncryptionDigest(
			provider.RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true},
		)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		binding.Native.BucketEncryptionDigest = bucketEncryptionDigest
	}
	binding.TaskID = taskEntity.ID
	binding.NodeID = taskEntity.NodeID
	binding.PreflightExpiresAt = now.Add(30 * time.Minute)
	binding.LegacyTaskPolicy = legacyPolicy
	salt, err := hex.DecodeString(binding.IdentitySalt)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := encodeBindingDocument(bindingDocument{
		Version: bindingDocumentVersion, Provider: backupasset.ProviderRclone,
		IdentityClass: provider.IdentityTaskScopedEndpoint, TaskID: taskEntity.ID, NodeID: taskEntity.NodeID,
		IdentitySalt: binding.IdentitySalt, Locator: "backup:legacy",
		EndpointFacts: []string{"task:" + uintString(taskEntity.ID), "node:" + uintString(taskEntity.NodeID), "remote:backup:legacy"},
		ConfigSource:  provider.RcloneConfigNodeDefault,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding.LegacyBindingV1 = legacy
	binding.LegacyLocatorDigest = managedRcloneBindingDigest(salt, "legacy-locator", "backup:legacy")
	binding.LegacyBindingDigest = managedRcloneBindingDigest(salt, "legacy-binding", legacy)
	binding.LegacyTaskPolicyDigest = managedRcloneBindingDigest(salt, "legacy-task-policy", legacyPolicy)
	identity, err := managedRcloneRepositoryIdentity(binding)
	if err != nil {
		t.Fatal(err)
	}
	repository := model.BackupRepository{
		ID: binding.RepositoryID, ProviderKind: string(backupasset.ProviderRclone), RepositoryIdentity: &identity,
		DisplayName: "managed-rclone-repository", VersionMode: string(versionModeForRclonePublication(mode)),
		Status: string(backupasset.RepositoryOnline), CapabilityRevision: int(binding.CapabilityRevision),
		CapabilitiesJSON:  `{"list":true,"open_sequential":true,"open_range":true}`,
		ImmutabilityLevel: string(rcloneImmutability(mode)), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	link := model.TaskRepositoryLink{
		ID: binding.TaskRepositoryLinkID, TaskID: &taskEntity.ID, RepositoryID: repository.ID,
		TaskNameSnapshot: taskEntity.Name, NodeIDSnapshot: taskEntity.NodeID, PublicationMode: string(mode),
		EncryptedLegacyLocator: "backup:legacy", LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := encodeManagedRcloneBindingDocumentV3(binding)
	if err != nil {
		t.Fatal(err)
	}
	access := model.RepositoryAccessBinding{
		ID: strings.Repeat("3", 32), RepositoryID: repository.ID, BindingKind: "managed_rclone_v3",
		EncryptedConfig: payload, ConfigFingerprint: strings.Repeat("f", 64), Status: bindingStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&access).Error; err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry()
	strategy := &rcloneRepositoryStrategyStub{}
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{Prober: &scriptedProber{}, PublicationStrategy: strategy}); err != nil {
		t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 168 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.NewRcloneNativeSession(
		"FAKE_REPOSITORY_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_REPOSITORY_SECRET_KEY_FOR_TEST_ONLY",
		"FAKE_REPOSITORY_SESSION_TOKEN_FOR_TEST_ONLY", "123456789012", strings.Repeat("a", 64), now.Add(40*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	nativeFactory := &rcloneNativeRepositoryFactoryFake{session: session}
	service, err := NewPublicationService(PublicationDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(completeRepositoryFoundationSettings(true)), Registry: registry,
		Keyring: backupasset.NewKeyring(db, func() time.Time { return now }), Lease: lease,
		Admission: &publicationAdmission{mode: publication.AdmissionManaged, generation: 1},
		Metrics:   publication.NoopMetrics{}, Audit: &auditSpy{}, History: history, Now: func() time.Time { return now },
		RcloneNativeFactoryBuilder: func(context.Context, provider.RcloneNativeBootstrap, string, int) (RcloneNativeFactory, error) {
			return nativeFactory, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rclonePublicationFixture{
		t: t, db: db, now: now, service: service, task: taskEntity, taskRun: taskRun,
		repository: repository, link: link, binding: binding, strategy: strategy, nativeFactory: nativeFactory,
	}
}

type rcloneNativeRepositoryFactoryFake struct {
	session            provider.RcloneNativeSession
	expectedExternalID string
	assumeCalls        int
	assumeRequests     []provider.RcloneNativeAssumeRoleRequest
	probeCalls         int
	listCalls          int
	baselinePrefixes   [][]string
	baselineRecords    []provider.RcloneNativeVersionRecord
	baselineHeads      map[string]provider.RcloneNativeBaselineObjectHead
	baselinePayloads   map[string][]byte
	kmsKeys            map[string]provider.RcloneNativeKMSKey
	baselineHeadCalls  int
	baselineOpenCalls  int
	describeKeyCalls   int
	headVersionCalls   int
}

func (fake *rcloneNativeRepositoryFactoryFake) BootstrapCredentialsExpire(context.Context) (bool, error) {
	return false, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) AssumeRole(_ context.Context, request provider.RcloneNativeAssumeRoleRequest) (provider.RcloneNativeAssumeRoleResult, error) {
	fake.assumeCalls++
	fake.assumeRequests = append(fake.assumeRequests, request)
	expectedExternalID := fake.expectedExternalID
	if expectedExternalID == "" {
		expectedExternalID = "FAKE_EXTERNAL_ID_FOR_TEST_ONLY"
	}
	if request.ExternalID == nil || *request.ExternalID != expectedExternalID {
		return provider.RcloneNativeAssumeRoleResult{}, provider.ErrRcloneNativeAssumeRoleDenied
	}
	return provider.RcloneNativeAssumeRoleResult{Session: fake.session, PackedPolicySize: 10}, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) Probe(context.Context, provider.RcloneNativeDenyProbeRequest) (provider.RcloneNativeDenyProbeResult, error) {
	fake.probeCalls++
	return provider.RcloneNativeDenyProbeResult{Denied: true}, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) S3(provider.RcloneNativeSession, provider.RcloneNativeProfile, []provider.RcloneNativeKMSKeyDigestBinding) (provider.S3Native, error) {
	return fake, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) BaselineS3(_ provider.RcloneNativeSession, _ provider.RcloneNativeProfile, prefixes []string) (provider.RcloneNativeBaselineS3, error) {
	fake.baselinePrefixes = append(fake.baselinePrefixes, append([]string(nil), prefixes...))
	return fake, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) KMS(provider.RcloneNativeSession, string) (provider.KMSKeyInspector, error) {
	if fake.kmsKeys != nil {
		return fake, nil
	}
	return nil, errors.New("FAKE_UNEXPECTED_KMS_CLIENT_FOR_TEST_ONLY")
}

func (*rcloneNativeRepositoryFactoryFake) BucketIdentity(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeBucketIdentity, error) {
	return provider.RcloneNativeBucketIdentity{AccountID: "123456789012", Region: "us-east-1", Kind: "general_purpose"}, nil
}

func (*rcloneNativeRepositoryFactoryFake) GetVersioning(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeVersioningObservation, error) {
	return provider.RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"}, nil
}

func (*rcloneNativeRepositoryFactoryFake) GetLifecycle(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeLifecycleObservation, error) {
	return provider.RcloneNativeLifecycleObservation{}, nil
}

func (*rcloneNativeRepositoryFactoryFake) GetEncryption(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeBucketEncryption, error) {
	return provider.RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true}, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) ListVersionPage(_ context.Context, request provider.RcloneNativeVersionPageRequest) (provider.RcloneNativeVersionPage, error) {
	fake.listCalls++
	records := make([]provider.RcloneNativeVersionRecord, 0, len(fake.baselineRecords))
	for _, record := range fake.baselineRecords {
		if strings.HasPrefix(record.PhysicalKey, request.Prefix) {
			records = append(records, record)
		}
	}
	return provider.RcloneNativeVersionPage{Records: records}, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) HeadBaselineVersion(_ context.Context, request provider.RcloneNativeExactReadRequest) (provider.RcloneNativeBaselineObjectHead, error) {
	fake.baselineHeadCalls++
	head, exists := fake.baselineHeads[request.PhysicalKey+"\x00"+request.VersionID]
	if !exists {
		return provider.RcloneNativeBaselineObjectHead{}, errors.New("FAKE_NATIVE_BASELINE_HEAD_NOT_FOUND_FOR_TEST_ONLY")
	}
	return head, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) OpenBaselineVersion(_ context.Context, request provider.RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	fake.baselineOpenCalls++
	payload, exists := fake.baselinePayloads[request.PhysicalKey+"\x00"+request.VersionID]
	if !exists {
		return nil, errors.New("FAKE_NATIVE_BASELINE_PAYLOAD_NOT_FOUND_FOR_TEST_ONLY")
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func (fake *rcloneNativeRepositoryFactoryFake) DescribeKey(_ context.Context, arn string) (provider.RcloneNativeKMSKey, error) {
	fake.describeKeyCalls++
	key, exists := fake.kmsKeys[arn]
	if !exists {
		return provider.RcloneNativeKMSKey{}, errors.New("FAKE_NATIVE_BASELINE_KMS_KEY_NOT_FOUND_FOR_TEST_ONLY")
	}
	return key, nil
}

func (fake *rcloneNativeRepositoryFactoryFake) HeadVersion(context.Context, provider.RcloneNativeExactReadRequest) (provider.RcloneNativeExactObjectHead, error) {
	fake.headVersionCalls++
	return provider.RcloneNativeExactObjectHead{}, errors.New("FAKE_UNEXPECTED_HEAD_FOR_TEST_ONLY")
}

func (*rcloneNativeRepositoryFactoryFake) OpenVersion(context.Context, provider.RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	return nil, errors.New("FAKE_UNEXPECTED_READ_FOR_TEST_ONLY")
}

func (*rcloneNativeRepositoryFactoryFake) OpenVersionRange(context.Context, provider.RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	return nil, errors.New("FAKE_UNEXPECTED_RANGE_FOR_TEST_ONLY")
}

func (*rcloneNativeRepositoryFactoryFake) PutControlVersion(context.Context, provider.RcloneNativeControlWriteRequest) (provider.RcloneNativeControlWriteResult, error) {
	return provider.RcloneNativeControlWriteResult{}, errors.New("FAKE_UNEXPECTED_WRITE_FOR_TEST_ONLY")
}

func (*rcloneNativeRepositoryFactoryFake) ProbeExactVersion(context.Context, provider.RcloneNativeExactVersion) (provider.RcloneNativeVersionProbe, error) {
	return provider.RcloneNativeVersionProbe{}, nil
}

func (*rcloneNativeRepositoryFactoryFake) DeleteExactVersion(context.Context, provider.RcloneNativeExactVersion) error {
	return nil
}

func (fixture *rclonePublicationFixture) run() publication.Run {
	return publication.Run{
		Task: fixture.task, TaskRunID: fixture.taskRun.ID, Trigger: fixture.taskRun.TriggerType,
		StartedAt: *fixture.taskRun.StartedAt,
		Audit: backupasset.PublicationAuditContext{
			Actor:         backupasset.AuditActor{UserID: 9, Username: "operator", Role: "operator"},
			CorrelationID: "managed-rclone-publication-prepare",
		},
	}
}

func uintString(value uint) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return string(digits)
}

type rcloneRepositoryStrategyStub struct {
	reconcile      provider.RcloneReconcileV1
	reconcileInput *provider.RcloneReconcileInput
	prepare        func(context.Context, provider.PublicationPrepareRequest) (provider.PreparedPublication, error)
	execute        func(context.Context, provider.PreparedPublication, provider.PublicationProgress) (provider.ProviderExecutionResult, error)
	record         func(context.Context, provider.PreparedPublication, provider.ProviderExecutionResult) (provider.ProviderCommit, error)
	verify         func(context.Context, provider.PreparedPublication, provider.ProviderCommit, provider.ManifestLimits) (provider.ManifestResult, error)
	reconcileCall  func(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error)
}

func (*rcloneRepositoryStrategyStub) Kind() backupasset.ProviderKind {
	return backupasset.ProviderRclone
}
func (stub *rcloneRepositoryStrategyStub) Prepare(ctx context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
	if stub.prepare != nil {
		return stub.prepare(ctx, request)
	}
	return provider.PreparedPublication{}, nil
}
func (stub *rcloneRepositoryStrategyStub) Execute(ctx context.Context, prepared provider.PreparedPublication, progress provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
	if stub.execute != nil {
		return stub.execute(ctx, prepared, progress)
	}
	return provider.ProviderExecutionResult{}, nil
}
func (stub *rcloneRepositoryStrategyStub) RecordCommit(ctx context.Context, prepared provider.PreparedPublication, result provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
	if stub.record != nil {
		return stub.record(ctx, prepared, result)
	}
	return provider.ProviderCommit{}, nil
}
func (stub *rcloneRepositoryStrategyStub) VerifyOrBuildManifest(ctx context.Context, prepared provider.PreparedPublication, commit provider.ProviderCommit, limits provider.ManifestLimits) (provider.ManifestResult, error) {
	if stub.verify != nil {
		return stub.verify(ctx, prepared, commit, limits)
	}
	return provider.ManifestResult{}, nil
}
func (stub *rcloneRepositoryStrategyStub) Reconcile(ctx context.Context, request provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
	stub.reconcileInput = request.RcloneInput
	if stub.reconcileCall != nil {
		return stub.reconcileCall(ctx, request)
	}
	value := stub.reconcile
	return provider.PublicationReconcileResult{Rclone: &value}, nil
}

var _ provider.PublicationStrategy = (*rcloneRepositoryStrategyStub)(nil)
