package repository

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
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

func TestRcloneNativeProviderCommitRejectsCapabilityRevisionDrift(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	commit.Native.CapabilityRevision++
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("native Rclone capability-drift commit error=%v, want ErrConflict", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointPreparing) {
		t.Fatalf("native Rclone capability-drift commit changed point state=%q", point.State)
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
		TaskNameSnapshot: taskEntity.Name, NodeIDSnapshot: taskEntity.NodeID, NodeNameSnapshot: taskEntity.Node.Name,
		PublicationMode:        string(mode),
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

func TestRcloneProviderCommitRejectsPointCapabilityRevisionDriftBeforeMutation(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		for _, testCase := range []struct {
			name     string
			revision int
		}{
			{name: "zero", revision: 0},
			{name: "drift", revision: 2},
		} {
			t.Run(string(mode)+"/"+testCase.name, func(t *testing.T) {
				fixture := newRclonePublicationFixture(t, mode)
				execution, err := fixture.service.Prepare(context.Background(), fixture.run())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
				var commit provider.RcloneCommitV1
				if mode == backupasset.PublicationNativeObjectVersions {
					commit = validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
				} else {
					commit = validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
				}
				if testCase.name == "drift" {
					testCase.revision = int(attempt.CapabilityRevision) + 1
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", attempt.RecoveryPointID).
					Update("capability_revision", testCase.revision).Error; err != nil {
					t.Fatal(err)
				}
				var before model.RecoveryPoint
				if err := fixture.db.First(&before, "id = ?", attempt.RecoveryPointID).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); !errors.Is(err, backupasset.ErrConflict) {
					t.Fatalf("point capability revision=%d record error=%v, want ErrConflict", testCase.revision, err)
				}
				var after model.RecoveryPoint
				if err := fixture.db.First(&after, "id = ?", attempt.RecoveryPointID).Error; err != nil {
					t.Fatal(err)
				}
				if after.State != before.State || after.EncryptedProviderLocator != before.EncryptedProviderLocator ||
					after.ConsistencyJSON != before.ConsistencyJSON || after.UpdatedAt != before.UpdatedAt {
					t.Fatalf("point mutated after capability rejection: before=%+v after=%+v", before, after)
				}
				var active int64
				if err := fixture.db.Model(&model.RecoveryPointLease{}).
					Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).
					Count(&active).Error; err != nil || active != 1 {
					t.Fatalf("active lease count=%d err=%v, want one", active, err)
				}
			})
		}
	}
}

func TestRcloneProviderCommitRejectsRepositoryCapabilityRevisionDriftBeforeMutation(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		for _, testCase := range []struct {
			name     string
			revision int
		}{
			{name: "zero", revision: 0},
			{name: "drift", revision: 2},
		} {
			t.Run(string(mode)+"/"+testCase.name, func(t *testing.T) {
				fixture := newRclonePublicationFixture(t, mode)
				execution, err := fixture.service.Prepare(context.Background(), fixture.run())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
				var commit provider.RcloneCommitV1
				if mode == backupasset.PublicationNativeObjectVersions {
					commit = validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
				} else {
					commit = validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
				}
				var beforePoint model.RecoveryPoint
				if err := fixture.db.First(&beforePoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
					t.Fatal(err)
				}
				var beforeLease model.RecoveryPointLease
				if err := fixture.db.Where(
					"recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?",
					attempt.RecoveryPointID, backupasset.LeaseHolderPointPublication, publicationLeaseOwner, backupasset.LeaseActive,
				).First(&beforeLease).Error; err != nil {
					t.Fatal(err)
				}
				if beforeLease.Status != string(backupasset.LeaseActive) {
					t.Fatalf("prepared child lease status=%q, want active", beforeLease.Status)
				}
				revision := testCase.revision
				if testCase.name == "drift" {
					revision = int(attempt.CapabilityRevision) + 1
				}
				if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).
					UpdateColumn("capability_revision", revision).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); !errors.Is(err, backupasset.ErrConflict) {
					t.Fatalf("repository capability revision=%d record error=%v, want ErrConflict", revision, err)
				}
				var afterPoint model.RecoveryPoint
				if err := fixture.db.First(&afterPoint, "id = ?", beforePoint.ID).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(afterPoint, beforePoint) {
					t.Fatalf("point mutated after repository capability rejection: before=%+v after=%+v", beforePoint, afterPoint)
				}
				var afterLease model.RecoveryPointLease
				if err := fixture.db.First(&afterLease, "id = ?", beforeLease.ID).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(afterLease, beforeLease) || afterLease.Status != string(backupasset.LeaseActive) {
					t.Fatalf("child lease changed after repository capability rejection: before=%+v after=%+v", beforeLease, afterLease)
				}
				var active int64
				if err := fixture.db.Model(&model.RecoveryPointLease{}).
					Where("recovery_point_id = ? AND status = ?", attempt.RecoveryPointID, backupasset.LeaseActive).
					Count(&active).Error; err != nil || active != 1 {
					t.Fatalf("active child lease count=%d err=%v, want one", active, err)
				}
			})
		}
	}
}

func TestRcloneProviderCommitReplayRejectsConsistencyCapabilityEvidenceDrift(t *testing.T) {
	for _, field := range []string{"capability_revision", "provider_commit_digest"} {
		t.Run(field, func(t *testing.T) {
			fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
			execution, err := fixture.service.Prepare(context.Background(), fixture.run())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
			if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
				t.Fatal(err)
			}
			var point model.RecoveryPoint
			if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
				t.Fatal(err)
			}
			consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
			if err != nil {
				t.Fatal(err)
			}
			if field == "capability_revision" {
				consistency.CapabilityRevision++
			} else {
				consistency.ProviderCommitDigest = strings.Repeat("f", 64)
			}
			encoded, err := backupasset.EncodePublicationConsistency(consistency)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
				Update("consistency_json", encoded).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("consistency %s replay error=%v, want ErrConflict", field, err)
			}
			var after model.RecoveryPoint
			if err := fixture.db.First(&after, "id = ?", point.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.State != string(backupasset.RecoveryPointVerifying) || after.ConsistencyJSON != encoded {
				t.Fatalf("replay changed point for consistency %s: %+v", field, after)
			}
		})
	}
}

func TestRcloneNativeProviderCommitRejectsInvalidControlIdentityBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*provider.RcloneCommitV1)
	}{
		{
			name: "claimed data version as control",
			mutate: func(commit *provider.RcloneCommitV1) {
				dataVersion := commit.Native.FrozenNativeVersions[0]
				commit.Native.CommitKey = dataVersion.PhysicalKey
				commit.Native.CommitVersionID = dataVersion.VersionID
			},
		},
		{
			name: "second version under deterministic control key",
			mutate: func(commit *provider.RcloneCommitV1) {
				commit.Native.FrozenNativeVersions = append(commit.Native.FrozenNativeVersions,
					provider.RcloneNativeExactVersion{
						PhysicalKey: commit.Native.CommitKey,
						VersionID:   "FAKE_SECOND_CONTROL_VERSION_FOR_TEST_ONLY",
					})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
			execution, err := fixture.service.Prepare(context.Background(), fixture.run())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
			var beforePoint model.RecoveryPoint
			if err := fixture.db.First(&beforePoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
				t.Fatal(err)
			}
			var beforeLease model.RecoveryPointLease
			if err := fixture.db.Where(
				"recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?",
				attempt.RecoveryPointID, backupasset.LeaseHolderPointPublication, publicationLeaseOwner, backupasset.LeaseActive,
			).First(&beforeLease).Error; err != nil {
				t.Fatal(err)
			}
			if beforePoint.State != string(backupasset.RecoveryPointPreparing) || beforeLease.Status != string(backupasset.LeaseActive) {
				t.Fatalf("prepared native point=%+v lease=%+v", beforePoint, beforeLease)
			}
			commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
			test.mutate(&commit)
			if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("invalid native control identity error=%v, want ErrConflict", err)
			}
			var afterPoint model.RecoveryPoint
			if err := fixture.db.First(&afterPoint, "id = ?", beforePoint.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterPoint, beforePoint) {
				t.Fatalf("point changed after invalid native control identity: before=%+v after=%+v", beforePoint, afterPoint)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.First(&afterLease, "id = ?", beforeLease.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterLease, beforeLease) || afterLease.Status != string(backupasset.LeaseActive) {
				t.Fatalf("child lease changed after invalid native control identity: before=%+v after=%+v", beforeLease, afterLease)
			}
			var evidenceRows int64
			if err := fixture.db.Model(&model.RecoveryPointRcloneNativeVersion{}).
				Where("recovery_point_id = ?", beforePoint.ID).Count(&evidenceRows).Error; err != nil {
				t.Fatal(err)
			}
			if evidenceRows != 0 {
				t.Fatalf("native evidence rows=%d after invalid control identity, want 0", evidenceRows)
			}
		})
	}
}

func TestRcloneNativeProviderCommitReplayRejectsTransientEvidenceMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*provider.RcloneCommitV1)
	}{
		{
			name: "commit key",
			mutate: func(commit *provider.RcloneCommitV1) {
				commit.Native.CommitKey = "managed/v1/control/points/replayed/commit.json"
			},
		},
		{
			name: "commit version ID",
			mutate: func(commit *provider.RcloneCommitV1) {
				commit.Native.CommitVersionID = "FAKE_REPLAYED_COMMIT_VERSION_FOR_TEST_ONLY"
			},
		},
		{
			name: "frozen native versions",
			mutate: func(commit *provider.RcloneCommitV1) {
				commit.Native.FrozenNativeVersions[0].VersionID = "FAKE_REPLAYED_DATA_VERSION_FOR_TEST_ONLY"
			},
		},
		{
			name: "frozen native references",
			mutate: func(commit *provider.RcloneCommitV1) {
				commit.Native.FrozenNativeReferences[0].VersionID = "FAKE_REPLAYED_REFERENCE_VERSION_FOR_TEST_ONLY"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
			execution, err := fixture.service.Prepare(context.Background(), fixture.run())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
			commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
			if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
				t.Fatal(err)
			}
			var beforePoint model.RecoveryPoint
			if err := fixture.db.First(&beforePoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
				t.Fatal(err)
			}
			beforeRows := loadRcloneNativeEvidenceRows(t, fixture.db, beforePoint.ID)
			if len(beforeRows) == 0 {
				t.Fatal("valid native commit persisted no evidence rows")
			}
			replayed := commit
			test.mutate(&replayed)
			if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(replayed)); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("native %s replay error=%v, want ErrConflict", test.name, err)
			}
			var afterPoint model.RecoveryPoint
			if err := fixture.db.First(&afterPoint, "id = ?", beforePoint.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterPoint, beforePoint) {
				t.Fatalf("point changed after native %s replay: before=%+v after=%+v", test.name, beforePoint, afterPoint)
			}
			afterRows := loadRcloneNativeEvidenceRows(t, fixture.db, beforePoint.ID)
			if !reflect.DeepEqual(afterRows, beforeRows) {
				t.Fatalf("native evidence rows changed after %s replay: before=%+v after=%+v", test.name, beforeRows, afterRows)
			}
		})
	}
}

func loadRcloneNativeEvidenceRows(t *testing.T, db *gorm.DB, pointID string) []model.RecoveryPointRcloneNativeVersion {
	t.Helper()
	var rows []model.RecoveryPointRcloneNativeVersion
	if err := db.Where("recovery_point_id = ?", pointID).
		Order("evidence_role ASC, ordinal ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func marshalManagedRclonePointLocatorForTest(locator managedRclonePointLocatorV1) ([]byte, error) {
	payload, err := json.Marshal(locator)
	if err != nil || locator.Version != managedRclonePointLocatorLegacyVersion {
		return payload, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil {
		return nil, err
	}
	for _, member := range []string{
		"frozen_native_version_count", "frozen_native_versions_digest",
		"frozen_native_reference_count", "frozen_native_references_digest",
	} {
		delete(members, member)
	}
	return json.Marshal(members)
}

func TestRcloneNativePrepareRespectsDeletionReservations(t *testing.T) {
	for _, test := range []struct {
		name          string
		phase         backupasset.LifecyclePhase
		blockedReason backupasset.LifecycleBlockedReason
		wantConflict  bool
	}{
		{name: "provider delete", phase: backupasset.LifecyclePhaseProviderDelete, wantConflict: true},
		{
			name: "blocked unproven", phase: backupasset.LifecyclePhaseBlocked,
			blockedReason: backupasset.LifecycleBlockedProviderDeleteUnproven, wantConflict: true,
		},
		{
			name: "blocked active hold", phase: backupasset.LifecyclePhaseBlocked,
			blockedReason: backupasset.LifecycleBlockedActiveHold, wantConflict: true,
		},
		{
			name: "blocked identity conflict", phase: backupasset.LifecyclePhaseBlocked,
			blockedReason: backupasset.LifecycleBlockedProviderIdentityConflict, wantConflict: true,
		},
		{
			name: "blocked native version reference dependency", phase: backupasset.LifecyclePhaseBlocked,
			blockedReason: backupasset.LifecycleBlockedProviderNativeVersionReferenced, wantConflict: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
			deletingPoint := model.RecoveryPoint{
				ID: strings.Repeat("c", 32), RepositoryID: fixture.repository.ID,
				ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.task.NodeID, ProducingNodeNameSnapshot: fixture.task.Node.Name,
				LineageJSON: "{}", EncryptedProviderLocator: "", EncryptedRollbackLocator: "",
				Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointExpiring),
				ManifestDigestAlgorithm: "sha256", ConsistencyJSON: "{}", FidelityJSON: "{}",
				PointRevision: 1, CapabilityRevision: fixture.repository.CapabilityRevision,
				CapabilitiesJSON:     fixture.repository.CapabilitiesJSON,
				ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
				PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
				CreatedAt: fixture.now, UpdatedAt: fixture.now,
			}
			if err := fixture.db.Create(&deletingPoint).Error; err != nil {
				t.Fatal(err)
			}
			reservation := model.RecoveryPointLifecycleAttempt{
				ID: strings.Repeat("d", 32), RecoveryPointID: deletingPoint.ID,
				Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(test.phase),
				TransitionRevision: 1, BlockedReason: string(test.blockedReason),
				CreatedAt: fixture.now, UpdatedAt: fixture.now,
			}
			if err := fixture.db.Create(&reservation).Error; err != nil {
				t.Fatal(err)
			}

			execution, err := fixture.service.Prepare(context.Background(), fixture.run())
			if test.wantConflict {
				if execution != nil || !errors.Is(err, backupasset.ErrConflict) {
					t.Fatalf("native prepare during deletion reservation execution=%T error=%v, want nil ErrConflict", execution, err)
				}
				var created int64
				if err := fixture.db.Model(&model.RecoveryPoint{}).
					Where("producing_task_run_id = ?", fixture.taskRun.ID).Count(&created).Error; err != nil {
					t.Fatal(err)
				}
				if created != 0 {
					t.Fatalf("native prepare during deletion reservation created %d recovery points", created)
				}
				return
			}
			if execution == nil || err != nil {
				t.Fatalf("native prepare with reference dependency block execution=%T error=%v, want admitted execution", execution, err)
			}
			if abandonErr := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); abandonErr != nil {
				t.Fatalf("abandon admitted native publication: %v", abandonErr)
			}
		})
	}
}

func TestRcloneNativeProviderCommitRejectsBlockedUnprovenReservationBeforeMutation(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
	reservation := model.RecoveryPointLifecycleAttempt{
		ID: strings.Repeat("d", 32), RecoveryPointID: attempt.RecoveryPointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseBlocked),
		TransitionRevision: 1, BlockedReason: string(backupasset.LifecycleBlockedProviderDeleteUnproven),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&reservation).Error; err != nil {
		t.Fatal(err)
	}
	var beforePoint model.RecoveryPoint
	if err := fixture.db.First(&beforePoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	beforeRows := loadRcloneNativeEvidenceRows(t, fixture.db, attempt.RecoveryPointID)
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("native provider commit during provider deletion error=%v, want ErrConflict", err)
	}
	var afterPoint model.RecoveryPoint
	if err := fixture.db.First(&afterPoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterPoint, beforePoint) {
		t.Fatalf("native provider deletion reservation changed point: before=%+v after=%+v", beforePoint, afterPoint)
	}
	afterRows := loadRcloneNativeEvidenceRows(t, fixture.db, attempt.RecoveryPointID)
	if !reflect.DeepEqual(afterRows, beforeRows) {
		t.Fatalf("native provider deletion reservation changed evidence: before=%+v after=%+v", beforeRows, afterRows)
	}
}

func TestRcloneNativeProviderCommitExactReplayIgnoresLaterDeletionReservation(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
	commit := validRcloneNativeRepositoryCommit(
		attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute),
	)
	outcome, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit))
	if err != nil {
		t.Fatal(err)
	}
	deletingPoint := model.RecoveryPoint{
		ID: strings.Repeat("c", 32), RepositoryID: fixture.repository.ID,
		ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.task.NodeID, ProducingNodeNameSnapshot: fixture.task.Node.Name,
		LineageJSON: "{}", EncryptedProviderLocator: "", EncryptedRollbackLocator: "",
		Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointExpiring),
		ManifestDigestAlgorithm: "sha256", ConsistencyJSON: "{}", FidelityJSON: "{}",
		PointRevision: 1, CapabilityRevision: fixture.repository.CapabilityRevision,
		CapabilitiesJSON:     fixture.repository.CapabilitiesJSON,
		ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&deletingPoint).Error; err != nil {
		t.Fatal(err)
	}
	reservation := model.RecoveryPointLifecycleAttempt{
		ID: strings.Repeat("d", 32), RecoveryPointID: deletingPoint.ID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseProviderDelete),
		TransitionRevision: 1, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&reservation).Error; err != nil {
		t.Fatal(err)
	}
	replayed, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit))
	if err != nil || replayed != outcome {
		t.Fatalf("native exact replay under deletion reservation outcome=%+v error=%v want=%+v", replayed, err, outcome)
	}
}

func TestRcloneNativeDeletionReservationJoinsAnySameRepositoryPoint(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	otherPoint := model.RecoveryPoint{
		ID: strings.Repeat("8", 32), RepositoryID: fixture.repository.ID,
		ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.task.NodeID, ProducingNodeNameSnapshot: fixture.task.Node.Name,
		LineageJSON: "{}", EncryptedProviderLocator: "", EncryptedRollbackLocator: "",
		Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointPreparing),
		ManifestDigestAlgorithm: "sha256", ConsistencyJSON: "{}", FidelityJSON: "{}",
		PointRevision: 1, CapabilityRevision: fixture.repository.CapabilityRevision, CapabilitiesJSON: fixture.repository.CapabilitiesJSON,
		ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&otherPoint).Error; err != nil {
		t.Fatal(err)
	}
	reservation := model.RecoveryPointLifecycleAttempt{
		ID: strings.Repeat("e", 32), RecoveryPointID: otherPoint.ID,
		Operation: string(backupasset.LifecycleExplicitPurge), Phase: string(backupasset.LifecyclePhaseProviderDelete),
		TransitionRevision: 1, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&reservation).Error; err != nil {
		t.Fatal(err)
	}
	var beforePoint model.RecoveryPoint
	if err := fixture.db.First(&beforePoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	beforeRows := loadRcloneNativeEvidenceRows(t, fixture.db, attempt.RecoveryPointID)
	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		return rejectManagedRcloneNativeDeletionReservationTx(context.Background(), tx, fixture.repository.ID)
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("same-repository provider deletion reservation error=%v, want ErrConflict", err)
	}
	var afterPoint model.RecoveryPoint
	if err := fixture.db.First(&afterPoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterPoint, beforePoint) {
		t.Fatalf("same-repository reservation changed point: before=%+v after=%+v", beforePoint, afterPoint)
	}
	afterRows := loadRcloneNativeEvidenceRows(t, fixture.db, attempt.RecoveryPointID)
	if !reflect.DeepEqual(afterRows, beforeRows) {
		t.Fatalf("same-repository reservation changed evidence: before=%+v after=%+v", beforeRows, afterRows)
	}
}

func TestManagedRcloneLocatorCompatibility(t *testing.T) {
	t.Run("portable v1", func(t *testing.T) {
		fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
		execution, err := fixture.service.Prepare(context.Background(), fixture.run())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
		markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), attempt.RepositoryID)
		if err != nil {
			t.Fatal(err)
		}
		payload, current, err := encodeManagedRclonePointLocator(
			attempt, fixture.binding, markerKey,
			validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute)),
		)
		if err != nil {
			t.Fatal(err)
		}
		if current.Version != managedRclonePointLocatorLegacyVersion {
			t.Fatalf("new portable Rclone locator version=%d, want %d", current.Version, managedRclonePointLocatorLegacyVersion)
		}
		for _, member := range []string{
			"frozen_native_version_count", "frozen_native_versions_digest",
			"frozen_native_reference_count", "frozen_native_references_digest",
		} {
			if strings.Contains(payload, `"`+member+`"`) {
				t.Fatalf("portable v1 locator emitted forbidden v2 aggregate member %q: %s", member, payload)
			}
		}
		var legacyWire struct {
			Version                 int                             `json:"version"`
			Provider                backupasset.ProviderKind        `json:"provider"`
			RepositoryID            string                          `json:"repository_id"`
			RecoveryPointID         string                          `json:"recovery_point_id"`
			AttemptID               string                          `json:"attempt_id"`
			PublicationMode         backupasset.TaskPublicationMode `json:"publication_mode"`
			TaggedAttempt           string                          `json:"tagged_attempt"`
			TaggedCommit            string                          `json:"tagged_commit"`
			ChildFenceDigest        string                          `json:"child_fence_digest"`
			CommitPayloadDigest     string                          `json:"commit_payload_digest"`
			PortableAttemptRoot     string                          `json:"portable_attempt_root,omitempty"`
			PhysicalIdentityDigest  string                          `json:"physical_identity_digest"`
			ProviderCommitDigest    string                          `json:"provider_commit_digest"`
			ManifestControlIdentity string                          `json:"manifest_control_identity"`
		}
		legacyDecoder := json.NewDecoder(strings.NewReader(payload))
		legacyDecoder.DisallowUnknownFields()
		if err := legacyDecoder.Decode(&legacyWire); err != nil {
			t.Fatalf("portable payload does not decode as strict historical v1 wire: %v; payload=%s", err, payload)
		}
		if err := legacyDecoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			t.Fatalf("portable payload has trailing data: %v", err)
		}
		if legacyWire.Version != managedRclonePointLocatorLegacyVersion ||
			legacyWire.Provider != backupasset.ProviderRclone ||
			legacyWire.PublicationMode != backupasset.PublicationVersionedPrefix ||
			legacyWire.PortableAttemptRoot == "" {
			t.Fatalf("strict historical portable wire=%+v", legacyWire)
		}
		decoded, err := decodeManagedRclonePointLocator(payload)
		if err != nil || decoded.Version != managedRclonePointLocatorLegacyVersion ||
			decoded.PublicationMode != backupasset.PublicationVersionedPrefix {
			t.Fatalf("legacy portable locator=%+v err=%v", decoded, err)
		}
		unknown := strings.TrimSuffix(payload, "}") + `,"unknown":true}`
		if _, err := decodeManagedRclonePointLocator(unknown); err == nil {
			t.Fatal("legacy portable locator with unknown field was accepted")
		}
		for _, testCase := range []struct {
			member string
			value  string
		}{
			{member: "frozen_native_version_count", value: "0"},
			{member: "frozen_native_versions_digest", value: `""`},
			{member: "frozen_native_reference_count", value: "0"},
			{member: "frozen_native_references_digest", value: `""`},
		} {
			t.Run("zero-valued forbidden "+testCase.member, func(t *testing.T) {
				hybrid := strings.TrimSuffix(payload, "}") + `,"` + testCase.member + `":` + testCase.value + "}"
				if _, err := decodeManagedRclonePointLocator(hybrid); err == nil {
					t.Fatalf("portable v1 locator accepted forbidden zero-valued member %q", testCase.member)
				}
			})
		}
	})

	t.Run("native v1", func(t *testing.T) {
		fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
		execution, err := fixture.service.Prepare(context.Background(), fixture.run())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
		commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
		markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), attempt.RepositoryID)
		if err != nil {
			t.Fatal(err)
		}
		currentPayload, current, err := encodeManagedRclonePointLocator(attempt, fixture.binding, markerKey, commit)
		if err != nil {
			t.Fatal(err)
		}
		for _, member := range []string{
			"frozen_native_version_count", "frozen_native_versions_digest",
			"frozen_native_reference_count", "frozen_native_references_digest",
		} {
			if !strings.Contains(currentPayload, `"`+member+`"`) {
				t.Fatalf("native v2 locator omitted explicit aggregate member %q: %s", member, currentPayload)
			}
		}
		if _, err := decodeManagedRclonePointLocator(currentPayload); err != nil {
			t.Fatalf("native v2 locator failed strict decode: %v", err)
		}
		legacy := current
		legacy.Version = managedRclonePointLocatorLegacyVersion
		legacy.NativeCommitKey = commit.Native.CommitKey
		legacy.NativeCommitVersionID = commit.Native.CommitVersionID
		legacy.FrozenNativeVersions = make([]managedRcloneFrozenNativeVersion, 0, len(commit.Native.FrozenNativeVersions))
		for _, version := range commit.Native.FrozenNativeVersions {
			legacy.FrozenNativeVersions = append(legacy.FrozenNativeVersions, managedRcloneFrozenNativeVersion{
				PhysicalKey: version.PhysicalKey, VersionID: version.VersionID,
			})
		}
		legacy.FrozenNativeVersionCount = 0
		legacy.FrozenNativeVersionsDigest = ""
		legacy.FrozenNativeReferenceCount = 0
		legacy.FrozenNativeReferencesDigest = ""
		legacy.PhysicalIdentityDigest = hex.EncodeToString(rcloneOwnershipDigest(
			markerKey, "xirang.rclone.native-point-identity.v1", attempt.RepositoryID,
			legacy.NativeCommitKey, legacy.NativeCommitVersionID, legacy.CommitPayloadDigest,
		))
		legacyJSON, err := json.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		var legacyMembers map[string]json.RawMessage
		if err := json.Unmarshal(legacyJSON, &legacyMembers); err != nil {
			t.Fatal(err)
		}
		for _, member := range []string{
			"frozen_native_version_count", "frozen_native_versions_digest",
			"frozen_native_reference_count", "frozen_native_references_digest",
		} {
			delete(legacyMembers, member)
		}
		payloadBytes, err := json.Marshal(legacyMembers)
		if err != nil {
			t.Fatal(err)
		}
		payload := string(payloadBytes)
		decoded, err := decodeManagedRclonePointLocator(payload)
		if err != nil || decoded.Version != managedRclonePointLocatorLegacyVersion ||
			decoded.NativeCommitKey != commit.Native.CommitKey ||
			len(decoded.FrozenNativeVersions) != len(commit.Native.FrozenNativeVersions) {
			t.Fatalf("legacy native locator=%+v err=%v", decoded, err)
		}
		for _, testCase := range []struct {
			member string
			value  string
		}{
			{member: "portable_attempt_root", value: `""`},
			{member: "native_commit_key", value: `""`},
			{member: "native_commit_version_id", value: `""`},
			{member: "frozen_native_versions", value: "[]"},
		} {
			t.Run("v2 zero-valued forbidden "+testCase.member, func(t *testing.T) {
				hybrid := strings.TrimSuffix(currentPayload, "}") + `,"` + testCase.member + `":` + testCase.value + "}"
				if _, err := decodeManagedRclonePointLocator(hybrid); err == nil {
					t.Fatalf("native v2 locator accepted forbidden zero-valued legacy member %q", testCase.member)
				}
			})
		}
		owned, references, err := loadManagedRcloneNativeVersionEvidenceTx(
			context.Background(), fixture.db, attempt.RepositoryID, attempt.RecoveryPointID,
			markerKey, decoded, managedRcloneNativeControlCommitKey(fixture.binding, attempt),
		)
		if err != nil || !reflect.DeepEqual(owned, references) ||
			len(owned) != len(commit.Native.FrozenNativeVersions) {
			t.Fatalf("legacy native evidence owned=%+v references=%+v err=%v", owned, references, err)
		}
	})
}

func TestManagedRclonePortableLocatorRejectsEvidenceMismatches(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
	markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), attempt.RepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	_, baseline, err := encodeManagedRclonePointLocator(attempt, fixture.binding, markerKey, commit)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*managedRclonePointLocatorV1) error
	}{
		{
			name: "attempt marker",
			mutate: func(locator *managedRclonePointLocatorV1) error {
				mutatedAttempt := attempt
				mutatedPortable := *attempt.Portable
				mutatedPortable.AttemptMarkerDigest = strings.Repeat("b", 64)
				mutatedAttempt.Portable = &mutatedPortable
				tagged, encodeErr := provider.EncodePublicationAttempt(provider.NewRclonePublicationAttempt(mutatedAttempt))
				if encodeErr != nil {
					return encodeErr
				}
				locator.TaggedAttempt = tagged
				return nil
			},
		},
		{
			name: "commit payload",
			mutate: func(locator *managedRclonePointLocatorV1) error {
				mutatedCommit := commit
				mutatedPortable := *commit.Portable
				mutatedPortable.CommitPayloadDigest = strings.Repeat("e", 64)
				mutatedCommit.Portable = &mutatedPortable
				tagged, encodeErr := provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(mutatedCommit))
				if encodeErr != nil {
					return encodeErr
				}
				locator.TaggedCommit = tagged
				locator.ProviderCommitDigest = digestText(tagged)
				return nil
			},
		},
		{
			name: "control identity",
			mutate: func(locator *managedRclonePointLocatorV1) error {
				mutatedCommit := commit
				mutatedPortable := *commit.Portable
				mutatedPortable.ControlIdentityDigest = strings.Repeat("f", 64)
				mutatedCommit.Portable = &mutatedPortable
				tagged, encodeErr := provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(mutatedCommit))
				if encodeErr != nil {
					return encodeErr
				}
				locator.TaggedCommit = tagged
				locator.ProviderCommitDigest = digestText(tagged)
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator := baseline
			if err := test.mutate(&locator); err != nil {
				t.Fatal(err)
			}
			payload, err := marshalManagedRclonePointLocatorForTest(locator)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeManagedRclonePointLocator(string(payload)); err == nil {
				t.Fatalf("%s mismatch unexpectedly accepted", test.name)
			}
		})
	}
}

func TestRcloneNativeCrossPointGuardIncludesLegacyV1References(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var ownerPoint model.RecoveryPoint
	if err := fixture.db.First(&ownerPoint, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	ownerLocator, err := decodeManagedRclonePointLocator(ownerPoint.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), attempt.RepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	legacyPointID := strings.Repeat("e", 32)
	legacyAttemptID := strings.Repeat("f", 32)
	legacyRun := model.TaskRun{
		TaskID: fixture.task.ID, TriggerType: "manual", Status: "success",
		StartedAt: timePointer(fixture.now.Add(-2 * time.Minute)), FinishedAt: timePointer(fixture.now.Add(-time.Minute)),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&legacyRun).Error; err != nil {
		t.Fatal(err)
	}
	legacyAttempt := attempt
	shared := commit.Native.FrozenNativeVersions[0]
	legacyAttempt.RecoveryPointID = legacyPointID
	legacyAttempt.AttemptID = legacyAttemptID
	legacyAttempt.TaskRunID = legacyRun.ID
	legacyCommit := commit
	legacyCommit.RecoveryPointID = legacyPointID
	legacyCommit.AttemptID = legacyAttemptID
	legacyCommit.Native = cloneRcloneNativeCommitForTest(commit.Native)
	legacyCommit.Native.CommitKey = managedRcloneNativeControlCommitKey(fixture.binding, legacyAttempt)
	legacyCommit.Native.CommitVersionID = "FAKE_LEGACY_CONTROL_VERSION_FOR_TEST_ONLY"
	legacyCommit.Native.FrozenNativeVersions = []provider.RcloneNativeExactVersion{
		shared,
		{PhysicalKey: legacyCommit.Native.CommitKey, VersionID: legacyCommit.Native.CommitVersionID},
	}
	legacyCommit.Native.FrozenNativeReferences = []provider.RcloneNativeExactVersion{shared}
	legacyTaggedAttempt, err := provider.EncodePublicationAttempt(provider.NewRclonePublicationAttempt(legacyAttempt))
	if err != nil {
		t.Fatal(err)
	}
	legacyTaggedCommit, err := provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(legacyCommit))
	if err != nil {
		t.Fatal(err)
	}
	legacyLocator := managedRclonePointLocatorV1{
		Version: managedRclonePointLocatorLegacyVersion, Provider: backupasset.ProviderRclone,
		RepositoryID: attempt.RepositoryID, RecoveryPointID: legacyPointID, AttemptID: legacyAttemptID,
		PublicationMode: backupasset.PublicationNativeObjectVersions, TaggedAttempt: legacyTaggedAttempt,
		TaggedCommit: legacyTaggedCommit, ChildFenceDigest: legacyAttempt.ChildFenceDigest,
		CommitPayloadDigest: legacyCommit.Native.CommitContentDigest,
		NativeCommitKey:     legacyCommit.Native.CommitKey, NativeCommitVersionID: legacyCommit.Native.CommitVersionID,
		FrozenNativeVersions: []managedRcloneFrozenNativeVersion{
			{PhysicalKey: shared.PhysicalKey, VersionID: shared.VersionID},
			{PhysicalKey: legacyCommit.Native.CommitKey, VersionID: legacyCommit.Native.CommitVersionID},
		},
		PhysicalIdentityDigest: hex.EncodeToString(rcloneOwnershipDigest(
			markerKey, "xirang.rclone.native-point-identity.v1", attempt.RepositoryID,
			legacyCommit.Native.CommitKey, legacyCommit.Native.CommitVersionID, legacyCommit.Native.CommitContentDigest,
		)),
		ProviderCommitDigest:    digestText(legacyTaggedCommit),
		ManifestControlIdentity: legacyCommit.Native.ManifestControlGraphDigest,
	}
	if err := validateManagedRclonePointLocator(legacyLocator); err != nil {
		t.Fatal(err)
	}
	legacyPayload, err := marshalManagedRclonePointLocatorForTest(legacyLocator)
	if err != nil {
		t.Fatal(err)
	}
	legacyPoint := model.RecoveryPoint{
		ID: legacyPointID, RepositoryID: attempt.RepositoryID,
		ProducingTaskID: &legacyAttempt.TaskID, ProducingTaskRunID: &legacyAttempt.TaskRunID,
		ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.task.NodeID,
		ProducingNodeNameSnapshot: fixture.task.Node.Name, LineageJSON: `{}`,
		EncryptedProviderLocator: string(legacyPayload), Semantics: string(backupasset.PointXirangManifest),
		State: string(backupasset.RecoveryPointCommitted), ManifestDigestAlgorithm: "sha256",
		ManifestDigest: legacyCommit.ManifestIndexDigest, EntryCount: int64(legacyCommit.ManifestEntryCount),
		LogicalBytes: int64(legacyCommit.LogicalBytes), SourceFingerprint: legacyLocator.PhysicalIdentityDigest,
		ConsistencyJSON: ownerPoint.ConsistencyJSON, FidelityJSON: ownerPoint.FidelityJSON,
		CapabilitiesJSON: fixture.repository.CapabilitiesJSON, CapabilityRevision: fixture.repository.CapabilityRevision,
		ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&legacyPoint).Error; err != nil {
		t.Fatal(err)
	}
	owned, _, err := loadManagedRcloneNativeVersionEvidenceTx(
		context.Background(), fixture.db, attempt.RepositoryID, ownerPoint.ID,
		markerKey, ownerLocator, managedRcloneNativeControlCommitKey(fixture.binding, attempt),
	)
	if err != nil {
		t.Fatal(err)
	}
	guardErr := rejectManagedRcloneNativeReferenceIntersectionTx(
		context.Background(), fixture.db, attempt.RepositoryID, ownerPoint.ID,
		owned, markerKey, fixture.binding,
	)
	if !errors.Is(guardErr, provider.ErrDeletePointNativeVersionReferenced) {
		t.Fatalf("legacy cross-point reference guard error=%v, want native-version reference dependency", guardErr)
	}
	if errors.Is(guardErr, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("legacy cross-point reference guard aliased native-version reference dependency to identity conflict: %v", guardErr)
	}
}

func cloneRcloneNativeCommitForTest(value *provider.RcloneNativeCommitV1) *provider.RcloneNativeCommitV1 {
	if value == nil {
		return nil
	}
	copy := *value
	copy.FrozenNativeVersions = append([]provider.RcloneNativeExactVersion(nil), value.FrozenNativeVersions...)
	copy.FrozenNativeReferences = append([]provider.RcloneNativeExactVersion(nil), value.FrozenNativeReferences...)
	return &copy
}
