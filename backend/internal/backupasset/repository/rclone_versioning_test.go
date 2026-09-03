package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type rcloneVersioningPreflighterStub struct {
	portableCalls int
	portableInput RclonePortablePreflightInput
	nativeCalls   int
	nativeInput   RcloneNativePreflightInput
	evidence      RcloneVersioningPreflightEvidence
	err           error
}

func (stub *rcloneVersioningPreflighterStub) PreflightPortable(_ context.Context, input RclonePortablePreflightInput) (RcloneVersioningPreflightEvidence, error) {
	stub.portableCalls++
	stub.portableInput = input
	return stub.evidence, stub.err
}

func (stub *rcloneVersioningPreflighterStub) PreflightNative(_ context.Context, input RcloneNativePreflightInput) (RcloneVersioningPreflightEvidence, error) {
	stub.nativeCalls++
	stub.nativeInput = input
	return stub.evidence, stub.err
}

func TestRcloneVersioningSetupStoreExpiresAndConsumesExactIdentityOnce(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	clock := now
	store, err := newRcloneVersioningSetupStore(func() time.Time { return clock }, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	request := backupasset.RcloneBindingSetupRequest{TaskID: 7, ExpectedTaskRevision: 9}
	portable, err := store.create(request, rcloneBindingSetupPortable)
	if err != nil {
		t.Fatal(err)
	}
	if err := portable.Validate(false); err != nil {
		t.Fatalf("portable setup result invalid: %v", err)
	}
	if _, ok := store.consume(portable.SetupID, request.TaskID+1, request.ExpectedTaskRevision, rcloneBindingSetupPortable); ok {
		t.Fatal("setup consumed by the wrong Task")
	}
	if _, ok := store.consume(portable.SetupID, request.TaskID, request.ExpectedTaskRevision+1, rcloneBindingSetupPortable); ok {
		t.Fatal("setup consumed by the wrong Task revision")
	}
	if _, ok := store.consume(portable.SetupID, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupNative); ok {
		t.Fatal("portable setup consumed as native")
	}
	record, ok := store.consume(portable.SetupID, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupPortable)
	if !ok || record.externalID != "" {
		t.Fatalf("portable setup record=%+v ok=%v", record, ok)
	}
	if _, ok := store.consume(portable.SetupID, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupPortable); ok {
		t.Fatal("one-time setup was replayed")
	}

	native, err := store.create(request, rcloneBindingSetupNative)
	if err != nil {
		t.Fatal(err)
	}
	if err := native.Validate(true); err != nil {
		t.Fatalf("native setup result invalid: %v", err)
	}
	if native.ExternalID == "" || native.ExternalID == native.SetupID {
		t.Fatalf("native setup external ID is not independently generated: %+v", native)
	}
	clock = native.ExpiresAt
	if _, ok := store.consume(native.SetupID, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupNative); ok {
		t.Fatal("expired native setup consumed")
	}
}

func TestRcloneVersioningSetupStoreLinearizesConcurrentConsumption(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	store, err := newRcloneVersioningSetupStore(func() time.Time { return now }, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := backupasset.RcloneBindingSetupRequest{TaskID: 7, ExpectedTaskRevision: 9}
	result, err := store.create(request, rcloneBindingSetupNative)
	if err != nil {
		t.Fatal(err)
	}

	var consumed atomic.Int32
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, ok := store.consume(result.SetupID, request.TaskID, request.ExpectedTaskRevision, rcloneBindingSetupNative); ok {
				consumed.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := consumed.Load(); got != 1 {
		t.Fatalf("concurrent setup consumption successes=%d, want 1", got)
	}
}

func TestCreateRcloneBindingSetupsRequireExactLegacyTaskAndDoNotMutate(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	service.keyring = backupasset.NewKeyring(db, nil)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}

	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	request := backupasset.RcloneBindingSetupRequest{TaskID: current.ID, ExpectedTaskRevision: revision}
	portable, err := service.CreateRclonePortableBindingSetup(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := portable.Validate(false); err != nil {
		t.Fatalf("portable setup result invalid: %v", err)
	}
	native, err := service.CreateRcloneNativeBindingSetup(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := native.Validate(true); err != nil {
		t.Fatalf("native setup result invalid: %v", err)
	}
	if native.ExternalID == "" {
		t.Fatal("native setup omitted the one-time external ID")
	}

	if _, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision + 1,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale setup error=%v, want conflict", err)
	}

	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", current.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) || link.EncryptedLegacyLocator != "backup:legacy" {
		t.Fatalf("setup mutated legacy link: %+v", link)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", link.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if stored.V1 == nil || stored.ManagedRsyncV2 != nil || stored.ManagedRcloneV3 != nil {
		t.Fatalf("setup mutated legacy binding: %+v", stored)
	}
}

func TestSetRclonePortableBindingConsumesSetupAndKeepsSecretsProcessLocal(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	service.keyring = backupasset.NewKeyring(db, nil)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n"
	request := backupasset.RclonePortableBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, ExpectedBindingRevision: 0, SetupID: setup.SetupID,
		TargetRemote: "backup", ManagedRootLocator: "backup:managed/v1", BoundConfig: config,
	}
	summary, err := service.SetRclonePortableBinding(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := summary.Validate(); err != nil {
		t.Fatalf("portable binding summary invalid: %v", err)
	}
	if summary.Mode != backupasset.PublicationVersionedPrefix || summary.State != backupasset.RcloneStatePreflightRequired ||
		summary.ReasonCode != backupasset.RcloneReasonPreflightRequired || summary.BindingRevision != "1" ||
		summary.EncryptionProfile != backupasset.RcloneEncryptionNone || summary.RollbackCapability != backupasset.RcloneRollbackCleanAvailable {
		t.Fatalf("portable binding summary=%+v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{config, request.TargetRemote, request.ManagedRootLocator} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("portable binding summary leaked %q: %s", private, encoded)
		}
	}
	if _, err := service.SetRclonePortableBinding(context.Background(), request); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("replayed portable setup error=%v, want conflict", err)
	}

	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", current.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", link.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) || stored.V1 == nil || stored.ManagedRcloneV3 != nil {
		t.Fatalf("binding setup activated managed state before preflight: link=%+v binding=%+v", link, stored)
	}
}

func TestSetRcloneNativeBindingUsesSetupExternalIDAndReturnsOnlySafeKMSFacts(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	service.keyring = backupasset.NewKeyring(db, nil)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRcloneNativeBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := backupasset.RcloneNativeBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, ExpectedBindingRevision: 0, SetupID: setup.SetupID,
		Region: "us-east-1", Bucket: "xirang-managed-test", ManagedPrefix: "managed/v1/",
		RoleARN: "arn:aws:iam::123456789012:role/xirang-backup-test",
		Bootstrap: backupasset.RcloneNativeBootstrapInput{
			Mode: backupasset.RcloneBootstrapStaticSTS, AccessKeyID: "FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY",
			SecretAccessKey: "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY",
		},
		EncryptionProfile: backupasset.RcloneEncryptionSSEKMS,
		KMSKeyARN:         "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-KEY-FOR-TEST-ONLY",
	}
	summary, err := service.SetRcloneNativeBinding(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := summary.Validate(); err != nil {
		t.Fatalf("native binding summary invalid: %v", err)
	}
	if summary.Mode != backupasset.PublicationNativeObjectVersions || summary.State != backupasset.RcloneStatePreflightRequired ||
		summary.BindingRevision != "1" || summary.EncryptionProfile != backupasset.RcloneEncryptionSSEKMS ||
		summary.KMSKeyStatus != backupasset.RcloneKMSReady || summary.KMSReadKeyCount != 0 {
		t.Fatalf("native binding summary=%+v", summary)
	}
	encoded, err := json.Marshal(struct {
		Summary backupasset.RclonePublicationSummary   `json:"summary"`
		Request backupasset.RcloneNativeBindingRequest `json:"request"`
	}{Summary: summary, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		request.Region, request.Bucket, request.ManagedPrefix, request.RoleARN,
		request.Bootstrap.AccessKeyID, request.Bootstrap.SecretAccessKey, request.KMSKeyARN, setup.ExternalID,
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("native binding result leaked %q: %s", private, encoded)
		}
	}
	candidate, ok := service.rcloneCandidates.get(current.ID)
	if !ok || candidate.native == nil || candidate.native.externalID != setup.ExternalID {
		t.Fatalf("native candidate did not bind setup external ID: %+v ok=%v", candidate, ok)
	}
}

func TestCreateRclonePortablePreflightBindsExactCandidateAndLeavesDatabaseLegacy(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	service.keyring = backupasset.NewKeyring(db, nil)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.Preload("Node").First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n"
	if _, err := service.SetRclonePortableBinding(context.Background(), backupasset.RclonePortableBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, SetupID: setup.SetupID,
		TargetRemote: "backup", ManagedRootLocator: "backup:managed/v1", BoundConfig: config,
	}); err != nil {
		t.Fatal(err)
	}
	stub := &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationVersionedPrefix, CapabilityRevision: 2,
		ConsistencyClass: backupasset.RcloneConsistencyObservationallyStable,
		HashFidelity:     backupasset.RcloneHashDownloadVerifiedBytes, EstimatedReadBytes: 4096,
		APICostClass: backupasset.RcloneCostModerate, StorageCostClass: backupasset.RcloneCostLow,
		EgressCostClass:           backupasset.RcloneCostHigh,
		ManagedRootIdentityDigest: strings.Repeat("a", 64), RepositoryMarkerDigest: strings.Repeat("b", 64),
		EvidenceDigest: strings.Repeat("c", 64),
	}}
	service.rclonePreflighter = stub

	result, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("portable preflight result invalid: %v", err)
	}
	if stub.portableCalls != 1 || stub.portableInput.TaskID != current.ID || stub.portableInput.BindingRevision != 1 ||
		stub.portableInput.BoundConfig.KeyedDigest() == "" || stub.portableInput.ManagedRootLocator != "backup:managed/v1" {
		t.Fatalf("portable preflight input=%+v calls=%d", stub.portableInput, stub.portableCalls)
	}
	if result.Summary.State != backupasset.RcloneStateReady || result.Summary.ReasonCode != backupasset.RcloneReasonReady ||
		result.Summary.CapabilityRevision != "2" || result.Summary.EstimatedReadBytes != "4096" ||
		result.Summary.RollbackCapability != backupasset.RcloneRollbackCleanAvailable {
		t.Fatalf("portable preflight summary=%+v", result.Summary)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{config, "backup:legacy", "backup:managed/v1", "backup"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("portable preflight result leaked %q: %s", private, encoded)
		}
	}

	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", current.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	var points int64
	if err := db.Model(&model.RecoveryPoint{}).
		Where("producing_task_id = ? AND semantics IN ?", current.ID, []string{
			string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
		}).Count(&points).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) || points != 0 {
		t.Fatalf("preflight activated or reserved managed state: link=%+v points=%d", link, points)
	}
}

func TestCreateRcloneNativePreflightRequiresCompleteExactAdmissionEvidence(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	service.keyring = backupasset.NewKeyring(db, nil)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRcloneNativeBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	bindingRequest := backupasset.RcloneNativeBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, SetupID: setup.SetupID,
		Region: "us-east-1", Bucket: "xirang-managed-test", ManagedPrefix: "managed/v1/",
		RoleARN: "arn:aws:iam::123456789012:role/xirang-backup-test",
		Bootstrap: backupasset.RcloneNativeBootstrapInput{
			Mode: backupasset.RcloneBootstrapWorkloadChain,
		},
		EncryptionProfile: backupasset.RcloneEncryptionSSEKMS,
		KMSKeyARN:         "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-KEY-FOR-TEST-ONLY",
	}
	if _, err := service.SetRcloneNativeBinding(context.Background(), bindingRequest); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	stub := &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationNativeObjectVersions, CapabilityRevision: 2,
		ConsistencyClass: backupasset.RcloneConsistencyProviderStrong,
		HashFidelity:     backupasset.RcloneHashDownloadVerifiedBytes, EstimatedReadBytes: 8192,
		APICostClass: backupasset.RcloneCostHigh, StorageCostClass: backupasset.RcloneCostModerate,
		EgressCostClass: backupasset.RcloneCostLow, CredentialExpiresAt: &expiresAt,
		EncryptionProfile: backupasset.RcloneEncryptionSSEKMS, KMSKeyStatus: backupasset.RcloneKMSReady,
		ManagedRootIdentityDigest: strings.Repeat("a", 64), RepositoryMarkerDigest: strings.Repeat("b", 64),
		EvidenceDigest: strings.Repeat("c", 64),
		Native: &rcloneNativePreflightEvidence{
			VersioningDigest: strings.Repeat("d", 64), LifecycleDigest: strings.Repeat("e", 64),
			CapabilityStableObservedAt: time.Date(2026, 7, 16, 11, 30, 0, 0, time.UTC),
			BucketEncryptionDigest:     strings.Repeat("f", 64), CanaryEncryptionEvidenceDigest: strings.Repeat("1", 64),
			ActiveKMSKeyDigest: strings.Repeat("2", 64), KMSCapabilityRevision: 3,
		},
	}}
	service.rclonePreflighter = stub
	result, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationNativeObjectVersions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("native preflight result invalid: %v", err)
	}
	if stub.nativeCalls != 1 || stub.nativeInput.ExternalID != setup.ExternalID ||
		stub.nativeInput.Request.KMSKeyARN != bindingRequest.KMSKeyARN || result.Summary.EncryptionProfile != backupasset.RcloneEncryptionSSEKMS ||
		result.Summary.KMSKeyStatus != backupasset.RcloneKMSReady || result.Summary.CredentialExpiresAt == nil {
		t.Fatalf("native preflight input=%+v calls=%d summary=%+v", stub.nativeInput, stub.nativeCalls, result.Summary)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{bindingRequest.Region, bindingRequest.Bucket, bindingRequest.ManagedPrefix, bindingRequest.RoleARN, bindingRequest.KMSKeyARN, setup.ExternalID} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("native preflight result leaked %q: %s", private, encoded)
		}
	}

	incomplete := *stub
	incomplete.evidence.Native = nil
	service.rclonePreflighter = &incomplete
	if _, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationNativeObjectVersions,
	}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("incomplete native evidence error=%v, want invalid state", err)
	}
}

func TestActivateRcloneVersioningFirstNewPointAtomicallyInstallsV3WithoutHistory(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	service.keyring = backupasset.NewKeyring(db, nil)
	service.admission = &rsyncVersioningTransitioner{}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n"
	if _, err := service.SetRclonePortableBinding(context.Background(), backupasset.RclonePortableBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, SetupID: setup.SetupID,
		TargetRemote: "backup", ManagedRootLocator: "backup:managed/v1", BoundConfig: config,
	}); err != nil {
		t.Fatal(err)
	}
	service.rclonePreflighter = &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationVersionedPrefix, CapabilityRevision: 2,
		ConsistencyClass: backupasset.RcloneConsistencyObservationallyStable,
		HashFidelity:     backupasset.RcloneHashDownloadVerifiedBytes, EstimatedReadBytes: 4096,
		APICostClass: backupasset.RcloneCostModerate, StorageCostClass: backupasset.RcloneCostLow,
		EgressCostClass:           backupasset.RcloneCostHigh,
		ManagedRootIdentityDigest: strings.Repeat("a", 64), RepositoryMarkerDigest: strings.Repeat("b", 64),
		EvidenceDigest: strings.Repeat("c", 64),
	}}
	preflight, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("activation result invalid: %v", err)
	}
	if result.MigrationChoice != backupasset.RcloneFirstNewPoint || result.Summary.Mode != backupasset.PublicationVersionedPrefix ||
		result.Summary.State != backupasset.RcloneStateReady || result.Summary.RollbackCapability != backupasset.RcloneRollbackCleanAvailable {
		t.Fatalf("activation result=%+v", result)
	}

	var activated model.Task
	if err := db.First(&activated, current.ID).Error; err != nil {
		t.Fatal(err)
	}
	if activated.Enabled || activated.RsyncTarget != "" || activated.ExecutorConfig != `{"version":1,"publication_mode":"versioned_prefix","bandwidth_limit":"10M","transfers":4}` {
		t.Fatalf("activated Task=%+v", activated)
	}
	activatedRevision, err := managedRsyncTaskRevision(activated)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.TaskRevision != strconv.FormatUint(activatedRevision, 10) {
		t.Fatalf("activation summary revision=%q persisted=%d", result.Summary.TaskRevision, activatedRevision)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", current.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationVersionedPrefix) || link.EncryptedLegacyLocator != "backup:legacy" {
		t.Fatalf("activated link=%+v", link)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		t.Fatal(err)
	}
	if repository.VersionMode != string(backupasset.VersionVersionedPrefix) || repository.ImmutabilityLevel != string(backupasset.ImmutabilityXirangManaged) || repository.RepositoryIdentity == nil {
		t.Fatalf("activated repository=%+v", repository)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ManagedRcloneV3 == nil || stored.V1 != nil || stored.ManagedRcloneV3.PreflightID != preflight.PreflightID ||
		stored.ManagedRcloneV3.Portable == nil || stored.ManagedRcloneV3.Portable.BoundConfig != config {
		t.Fatalf("activated binding=%+v", stored)
	}
	var managedPoints int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ? AND semantics IN ?", repository.ID, []string{
		string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
	}).Count(&managedPoints).Error; err != nil {
		t.Fatal(err)
	}
	var latches int64
	if err := db.Model(&model.BackupAssetManagedHistoryLatch{}).Count(&latches).Error; err != nil {
		t.Fatal(err)
	}
	if managedPoints != 0 || latches != 0 {
		t.Fatalf("first-new activation created history: points=%d latches=%d", managedPoints, latches)
	}
	if _, err := service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RcloneFirstNewPoint,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("replayed activation error=%v, want conflict", err)
	}
}

type rclonePortableVersioningWorkflowFixture struct {
	db        *gorm.DB
	now       time.Time
	task      model.Task
	revision  uint64
	service   *Service
	strategy  *rcloneRepositoryStrategyStub
	preflight backupasset.RcloneVersioningPreflightResult
}

type rcloneNativeVersioningWorkflowFixture struct {
	db            *gorm.DB
	now           time.Time
	task          model.Task
	revision      uint64
	service       *Service
	strategy      *rcloneRepositoryStrategyStub
	preflight     backupasset.RcloneVersioningPreflightResult
	nativeFactory *rcloneNativeRepositoryFactoryFake
	sourceKeyARN  string
	sourcePayload []byte
}

func newRclonePortableVersioningWorkflowFixture(t *testing.T) *rclonePortableVersioningWorkflowFixture {
	t.Helper()
	db := newRepositoryTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	foundation := backupasset.NewFoundationService(completeRepositoryFoundationSettings(true))
	registry := provider.NewRegistry()
	strategy := &rcloneRepositoryStrategyStub{}
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{
		Prober: scopedObservationProber(backupasset.ProviderRclone), PublicationStrategy: strategy,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	admission := &rsyncVersioningManagedAdmission{publicationAdmission: &publicationAdmission{
		mode: publication.AdmissionPristineLegacy, generation: 1,
	}}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 168 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	publisher, err := NewPublicationService(PublicationDependencies{
		DB: db, Foundation: foundation, Registry: registry, Keyring: keyring, Lease: lease,
		Admission: admission, Metrics: publication.NoopMetrics{}, Audit: &auditSpy{}, History: history,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: foundation, Registry: registry, Keyring: keyring, Now: func() time.Time { return now },
		Admission: admission, History: history, Metrics: publication.NoopMetrics{}, Publication: publisher,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n"
	if _, err := service.SetRclonePortableBinding(context.Background(), backupasset.RclonePortableBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, SetupID: setup.SetupID,
		TargetRemote: "backup", ManagedRootLocator: "backup:managed/v1", BoundConfig: config,
	}); err != nil {
		t.Fatal(err)
	}
	service.rclonePreflighter = &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationVersionedPrefix, CapabilityRevision: 2,
		ConsistencyClass: backupasset.RcloneConsistencyObservationallyStable,
		HashFidelity:     backupasset.RcloneHashDownloadVerifiedBytes, EstimatedReadBytes: 4096,
		APICostClass: backupasset.RcloneCostModerate, StorageCostClass: backupasset.RcloneCostLow,
		EgressCostClass:           backupasset.RcloneCostHigh,
		ManagedRootIdentityDigest: strings.Repeat("a", 64), RepositoryMarkerDigest: strings.Repeat("b", 64),
		EvidenceDigest: strings.Repeat("c", 64),
	}}
	preflight, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rclonePortableVersioningWorkflowFixture{
		db: db, now: now, task: current, revision: revision, service: service, strategy: strategy, preflight: preflight,
	}
}

func newRcloneNativeVersioningWorkflowFixture(t *testing.T) *rcloneNativeVersioningWorkflowFixture {
	t.Helper()
	db := newRepositoryTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	legacyLocator := "legacy_remote:xirang-managed-test/legacy/current/"
	taskEntity := seedTask(t, db, "rclone", legacyLocator, `{"bandwidth_limit":"10M","transfers":4}`)
	foundation := backupasset.NewFoundationService(completeRepositoryFoundationSettings(true))
	registry := provider.NewRegistry()
	strategy := &rcloneRepositoryStrategyStub{}
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{
		Prober: scopedObservationProber(backupasset.ProviderRclone), PublicationStrategy: strategy,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	admission := &rsyncVersioningManagedAdmission{publicationAdmission: &publicationAdmission{
		mode: publication.AdmissionPristineLegacy, generation: 1,
	}}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 168 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := provider.NewRcloneNativeSession(
		"FAKE_BASELINE_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_BASELINE_SECRET_KEY_FOR_TEST_ONLY",
		"FAKE_BASELINE_SESSION_TOKEN_FOR_TEST_ONLY", "123456789012", strings.Repeat("a", 64), now.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	sourcePayload := []byte("native imported baseline payload")
	sourceKeyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-NATIVE-BASELINE-SOURCE-KMS-FOR-TEST-ONLY"
	sourceRecord := provider.RcloneNativeVersionRecord{
		PhysicalKey: "legacy/current/database.dump", VersionID: "opaque-legacy-version-v1",
		Kind: provider.RcloneNativeObjectVersion, IsLatest: true, Size: uint64(len(sourcePayload)), LastModified: now.Add(-time.Hour),
	}
	sourceIdentity := sourceRecord.PhysicalKey + "\x00" + sourceRecord.VersionID
	nativeFactory := &rcloneNativeRepositoryFactoryFake{
		session: session, baselineRecords: []provider.RcloneNativeVersionRecord{sourceRecord},
		baselineHeads: map[string]provider.RcloneNativeBaselineObjectHead{sourceIdentity: {
			PhysicalKey: sourceRecord.PhysicalKey, VersionID: sourceRecord.VersionID, Size: sourceRecord.Size,
			EncryptionProfile: provider.RcloneNativeSSEKMSV1, KMSKeyARN: sourceKeyARN,
		}},
		baselinePayloads: map[string][]byte{sourceIdentity: sourcePayload},
		kmsKeys: map[string]provider.RcloneNativeKMSKey{sourceKeyARN: {
			ARN: sourceKeyARN, AccountID: "123456789012", Region: "us-east-1", Manager: "CUSTOMER",
			Spec: "SYMMETRIC_DEFAULT", Usage: "ENCRYPT_DECRYPT", State: "Enabled", Origin: "AWS_KMS",
		}},
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	publisher, err := NewPublicationService(PublicationDependencies{
		DB: db, Foundation: foundation, Registry: registry, Keyring: keyring, Lease: lease,
		Admission: admission, Metrics: publication.NoopMetrics{}, Audit: &auditSpy{}, History: history,
		Now: func() time.Time { return now },
		RcloneNativeFactoryBuilder: func(context.Context, provider.RcloneNativeBootstrap, string, int) (RcloneNativeFactory, error) {
			return nativeFactory, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: foundation, Registry: registry, Keyring: keyring, Now: func() time.Time { return now },
		Admission: admission, History: history, Metrics: publication.NoopMetrics{}, Publication: publisher,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRcloneNativeBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	nativeFactory.expectedExternalID = setup.ExternalID
	if _, err := service.SetRcloneNativeBinding(context.Background(), backupasset.RcloneNativeBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, SetupID: setup.SetupID,
		Region: "us-east-1", Bucket: "xirang-managed-test", ManagedPrefix: "managed/v1/",
		RoleARN:           "arn:aws:iam::123456789012:role/xirang-backup-test",
		Bootstrap:         backupasset.RcloneNativeBootstrapInput{Mode: backupasset.RcloneBootstrapWorkloadChain},
		EncryptionProfile: backupasset.RcloneEncryptionSSES3,
	}); err != nil {
		t.Fatal(err)
	}
	versioningDigest, err := provider.CanonicalRcloneNativeVersioningDigest(provider.RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycleDigest, err := provider.CanonicalRcloneNativeLifecycleDigest(provider.RcloneNativeLifecycleObservation{})
	if err != nil {
		t.Fatal(err)
	}
	bucketEncryptionDigest, err := provider.CanonicalRcloneNativeBucketEncryptionDigest(provider.RcloneNativeBucketEncryption{
		Algorithm: "AES256", BlockedEncryptionTypesKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialExpiresAt := now.Add(time.Hour)
	service.rclonePreflighter = &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationNativeObjectVersions, CapabilityRevision: 2,
		ConsistencyClass: backupasset.RcloneConsistencyProviderStrong,
		HashFidelity:     backupasset.RcloneHashDownloadVerifiedBytes, EstimatedReadBytes: uint64(len(sourcePayload)),
		APICostClass: backupasset.RcloneCostHigh, StorageCostClass: backupasset.RcloneCostModerate,
		EgressCostClass: backupasset.RcloneCostLow, CredentialExpiresAt: &credentialExpiresAt,
		EncryptionProfile: backupasset.RcloneEncryptionSSES3, KMSKeyStatus: backupasset.RcloneKMSNotApplicable,
		ManagedRootIdentityDigest: strings.Repeat("b", 64), RepositoryMarkerDigest: strings.Repeat("c", 64),
		EvidenceDigest: strings.Repeat("d", 64),
		Native: &rcloneNativePreflightEvidence{
			VersioningDigest: versioningDigest, LifecycleDigest: lifecycleDigest,
			CapabilityStableObservedAt: now.Add(-20 * time.Minute), BucketEncryptionDigest: bucketEncryptionDigest,
			CanaryEncryptionEvidenceDigest: strings.Repeat("e", 64),
		},
	}}
	preflight, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationNativeObjectVersions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rcloneNativeVersioningWorkflowFixture{
		db: db, now: now, task: current, revision: revision, service: service, strategy: strategy,
		preflight: preflight, nativeFactory: nativeFactory, sourceKeyARN: sourceKeyARN, sourcePayload: sourcePayload,
	}
}

func TestActivateRcloneVersioningImportedBaselineReservesBeforePortableProviderMutation(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	db := fixture.db
	current := fixture.task
	service := fixture.service
	strategy := fixture.strategy
	preflight := fixture.preflight
	revision := fixture.revision

	providerStopped := errors.New("FAKE_PORTABLE_BASELINE_PROVIDER_STOP_FOR_TEST_ONLY")
	providerCalled := false
	strategy.prepare = func(_ context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
		providerCalled = true
		attempt, attemptErr := request.Attempt.RcloneAttempt()
		if attemptErr != nil || !attempt.ImportedBaseline || attempt.PublicationMode != backupasset.PublicationVersionedPrefix {
			t.Fatalf("imported baseline attempt=%+v err=%v", attempt, attemptErr)
		}
		legacySource, sourceErr := provider.NewRclonePrivateLocator("backup:legacy")
		if sourceErr != nil {
			t.Fatal(sourceErr)
		}
		if request.RcloneInput == nil || request.RcloneInput.PortableRequest == nil ||
			request.RcloneInput.NativeRequest != nil || request.RcloneInput.PortableRequest.Source != legacySource {
			t.Fatalf("imported baseline provider input=%+v", request.RcloneInput)
		}
		var migrationRun model.TaskRun
		if queryErr := db.Where("id = ? AND task_id = ? AND trigger_type = ?", attempt.TaskRunID, current.ID, "migration").First(&migrationRun).Error; queryErr != nil {
			t.Fatalf("provider began before migration TaskRun commit: %v", queryErr)
		}
		var point model.RecoveryPoint
		if queryErr := db.First(&point, "id = ?", attempt.RecoveryPointID).Error; queryErr != nil {
			t.Fatalf("provider began before point reservation commit: %v", queryErr)
		}
		var activeLeases int64
		if queryErr := db.Model(&model.RecoveryPointLease{}).Where(
			"recovery_point_id = ? AND holder_type = ? AND status = ?",
			point.ID, backupasset.LeaseHolderPointPublication, backupasset.LeaseActive,
		).Count(&activeLeases).Error; queryErr != nil || activeLeases != 1 {
			t.Fatalf("provider began without exact active lease: count=%d err=%v", activeLeases, queryErr)
		}
		if point.Semantics != string(backupasset.PointImportedBaseline) || point.State != string(backupasset.RecoveryPointPreparing) {
			t.Fatalf("provider began before imported baseline reservation: %+v", point)
		}
		return provider.PreparedPublication{}, providerStopped
	}

	_, err := service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID,
		MigrationChoice: backupasset.RcloneImportedBaseline,
	})
	if !errors.Is(err, providerStopped) {
		t.Fatalf("imported baseline activation error=%v, want provider stop", err)
	}
	if !providerCalled {
		t.Fatal("portable provider mutation path was not reached after reservation commit")
	}
	var activated model.Task
	if err := db.First(&activated, current.ID).Error; err != nil {
		t.Fatal(err)
	}
	if activated.Enabled || activated.RsyncTarget != "" {
		t.Fatalf("failed imported baseline did not keep Task paused: %+v", activated)
	}
	var migrationRun model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", current.ID, "migration").First(&migrationRun).Error; err != nil {
		t.Fatal(err)
	}
	if migrationRun.Status != "failed" || migrationRun.FinishedAt == nil {
		t.Fatalf("failed imported baseline TaskRun=%+v", migrationRun)
	}
	var point model.RecoveryPoint
	if err := db.Where("producing_task_run_id = ?", migrationRun.ID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if point.Semantics != string(backupasset.PointImportedBaseline) || point.State != string(backupasset.RecoveryPointFailed) || point.EncryptedProviderLocator == "" {
		t.Fatalf("failed imported baseline evidence=%+v activationErr=%v", point, err)
	}
}

func TestActivateRcloneVersioningImportedBaselineNativeUsesExactTemporarySourceAdmission(t *testing.T) {
	fixture := newRcloneNativeVersioningWorkflowFixture(t)
	providerStopped := errors.New("FAKE_NATIVE_BASELINE_PROVIDER_STOP_FOR_TEST_ONLY")
	providerCalled := false
	var preparedAttempt provider.RcloneAttemptV1
	var preparedInput provider.RclonePublicationInput
	fixture.strategy.prepare = func(_ context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
		providerCalled = true
		var err error
		preparedAttempt, err = request.Attempt.RcloneAttempt()
		if err != nil {
			return provider.PreparedPublication{}, err
		}
		if request.RcloneInput != nil {
			preparedInput = *request.RcloneInput
		}
		return provider.PreparedPublication{}, providerStopped
	}

	_, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID,
		MigrationChoice: backupasset.RcloneImportedBaseline,
	})
	if !errors.Is(err, providerStopped) {
		t.Fatalf("native imported baseline activation error=%v, want provider stop", err)
	}
	if !providerCalled || !preparedAttempt.ImportedBaseline || preparedAttempt.PublicationMode != backupasset.PublicationNativeObjectVersions ||
		preparedInput.NativeRequest == nil || preparedInput.PortableRequest != nil {
		t.Fatalf("native imported baseline provider attempt=%+v input=%+v called=%v", preparedAttempt, preparedInput, providerCalled)
	}
	expectedSource, err := provider.NewRclonePrivateLocator("xirang_native:xirang-managed-test/legacy/current/")
	if err != nil {
		t.Fatal(err)
	}
	if preparedInput.NativeRequest.Source != expectedSource || preparedInput.NativeRequest.Encryption.Profile != provider.RcloneNativeSSES3V1 ||
		len(preparedInput.NativeRequest.KMSKeyBindings) != 0 || strings.Contains(string(preparedInput.NativeRequest.RcloneConfig), fixture.sourceKeyARN) {
		t.Fatalf("native imported baseline source/encryption input=%+v", preparedInput.NativeRequest)
	}
	var migrationRun model.TaskRun
	if err := fixture.db.Where("task_id = ? AND trigger_type = ?", fixture.task.ID, "migration").First(&migrationRun).Error; err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.Where("producing_task_run_id = ?", migrationRun.ID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if migrationRun.Status != "failed" || point.Semantics != string(backupasset.PointImportedBaseline) ||
		point.State != string(backupasset.RecoveryPointFailed) || preparedAttempt.RecoveryPointID != point.ID {
		t.Fatalf("native imported baseline reservation run=%+v point=%+v attempt=%+v", migrationRun, point, preparedAttempt)
	}
	var access model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", point.RepositoryID, bindingStatusActive).First(&access).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(access.EncryptedConfig)
	if err != nil || stored.ManagedRcloneV3 == nil || stored.ManagedRcloneV3.Native == nil {
		t.Fatalf("native imported baseline stored binding=%+v err=%v", stored, err)
	}
	if preparedAttempt.LegacyOriginEvidenceDigest == stored.ManagedRcloneV3.LegacyBindingDigest ||
		stored.ManagedRcloneV3.Native.ActiveKMSKeyARN != "" || len(stored.ManagedRcloneV3.Native.RetainedReadKeys) != 0 {
		t.Fatalf("native imported baseline origin/key release attempt=%+v binding=%+v", preparedAttempt, stored.ManagedRcloneV3.Native)
	}

	correctPolicies := make([]string, 0, 2)
	for _, request := range fixture.nativeFactory.assumeRequests {
		if request.ExternalID != nil && *request.ExternalID == fixture.nativeFactory.expectedExternalID {
			correctPolicies = append(correctPolicies, request.SessionPolicy)
		}
	}
	if fixture.nativeFactory.assumeCalls != 6 || fixture.nativeFactory.probeCalls != 2 || len(correctPolicies) != 2 ||
		len(fixture.nativeFactory.baselinePrefixes) != 2 || fixture.nativeFactory.baselineHeadCalls != 2 ||
		fixture.nativeFactory.baselineOpenCalls != 1 || fixture.nativeFactory.describeKeyCalls != 1 {
		t.Fatalf("native baseline admission calls assume=%d probe=%d policies=%d prefixes=%v head=%d open=%d describe=%d list=%d",
			fixture.nativeFactory.assumeCalls, fixture.nativeFactory.probeCalls, len(correctPolicies), fixture.nativeFactory.baselinePrefixes,
			fixture.nativeFactory.baselineHeadCalls, fixture.nativeFactory.baselineOpenCalls, fixture.nativeFactory.describeKeyCalls,
			fixture.nativeFactory.listCalls)
	}
	if !strings.Contains(correctPolicies[0], "legacy/current/") || strings.Contains(correctPolicies[0], fixture.sourceKeyARN) ||
		!strings.Contains(correctPolicies[1], "legacy/current/") || !strings.Contains(correctPolicies[1], fixture.sourceKeyARN) ||
		!strings.Contains(correctPolicies[1], "kms:Decrypt") {
		t.Fatalf("native baseline two-stage policies=%v", correctPolicies)
	}
}

func TestActivateRcloneVersioningImportedBaselineNativeCommitsAndReleasesSourceKeys(t *testing.T) {
	fixture := newRcloneNativeVersioningWorkflowFixture(t)
	var commit provider.RcloneCommitV1
	fixture.strategy.prepare = func(_ context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
		attempt, err := request.Attempt.RcloneAttempt()
		if err != nil || !attempt.ImportedBaseline || request.RcloneInput == nil || request.RcloneInput.NativeRequest == nil ||
			request.RcloneInput.PortableRequest != nil {
			t.Fatalf("native imported baseline prepare attempt=%+v input=%+v err=%v", attempt, request.RcloneInput, err)
		}
		return provider.PreparedPublication{Attempt: request.Attempt, RcloneInput: request.RcloneInput}, nil
	}
	fixture.strategy.execute = func(_ context.Context, prepared provider.PreparedPublication, _ provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
		attempt, err := prepared.Attempt.RcloneAttempt()
		if err != nil || prepared.RcloneInput == nil || prepared.RcloneInput.NativeRequest == nil {
			t.Fatalf("native imported baseline execute attempt=%+v input=%+v err=%v", attempt, prepared.RcloneInput, err)
		}
		commit = validRcloneNativeRepositoryCommit(attempt, prepared.RcloneInput.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
		fixture.strategy.reconcile = provider.RcloneReconcileV1{
			State: provider.RcloneReconcileProviderCommitted, Commit: &commit,
			Manifest: &provider.RcloneManifestV1{
				ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
				EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes,
				FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
			},
		}
		tagged := provider.NewRcloneProviderCommit(commit)
		return provider.ProviderExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &tagged}, nil
	}
	fixture.strategy.record = func(_ context.Context, _ provider.PreparedPublication, result provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
		if result.ProviderCommit == nil {
			t.Fatal("native imported baseline execution omitted Provider commit")
		}
		return *result.ProviderCommit, nil
	}

	result, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID,
		MigrationChoice: backupasset.RcloneImportedBaseline,
	})
	if err != nil {
		t.Fatalf("activate native imported baseline: %v", err)
	}
	if result.Summary.State != backupasset.RcloneStateCommitted || result.Summary.RollbackCapability != backupasset.RcloneRollbackPreparationOnly ||
		result.Summary.EncryptionProfile != backupasset.RcloneEncryptionSSES3 || result.Summary.KMSReadKeyCount != 0 {
		t.Fatalf("native imported baseline activation=%+v", result)
	}
	var migrationRun model.TaskRun
	if err := fixture.db.Where("task_id = ? AND trigger_type = ?", fixture.task.ID, "migration").First(&migrationRun).Error; err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.Where("producing_task_run_id = ?", migrationRun.ID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if migrationRun.Status != "success" || migrationRun.FinishedAt == nil || migrationRun.LastError != "" ||
		point.State != string(backupasset.RecoveryPointCommitted) || point.Semantics != string(backupasset.PointImportedBaseline) ||
		point.ManifestDigest != commit.ManifestIndexDigest || point.SourceFingerprint == "" {
		t.Fatalf("native imported baseline completion run=%+v point=%+v", migrationRun, point)
	}
	if fixture.nativeFactory.baselineHeadCalls != 2 || fixture.nativeFactory.baselineOpenCalls != 1 ||
		fixture.nativeFactory.describeKeyCalls != 1 || fixture.nativeFactory.assumeCalls != 9 {
		t.Fatalf("native imported baseline completion admission assume=%d head=%d open=%d describe=%d",
			fixture.nativeFactory.assumeCalls, fixture.nativeFactory.baselineHeadCalls,
			fixture.nativeFactory.baselineOpenCalls, fixture.nativeFactory.describeKeyCalls)
	}
	var access model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", point.RepositoryID, bindingStatusActive).First(&access).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(access.EncryptedConfig)
	if err != nil || stored.ManagedRcloneV3 == nil || stored.ManagedRcloneV3.Native == nil ||
		stored.ManagedRcloneV3.Native.ActiveKMSKeyARN != "" || len(stored.ManagedRcloneV3.Native.RetainedReadKeys) != 0 {
		t.Fatalf("native imported baseline retained source key binding=%+v err=%v", stored, err)
	}
}

func TestActivateRcloneVersioningImportedBaselinePortableCommitsMigrationRunAndClosesCleanRollback(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	var commit provider.RcloneCommitV1
	fixture.strategy.prepare = func(_ context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
		attempt, err := request.Attempt.RcloneAttempt()
		if err != nil || !attempt.ImportedBaseline || request.RcloneInput == nil || request.RcloneInput.PortableRequest == nil {
			t.Fatalf("portable imported baseline prepare attempt=%+v input=%+v err=%v", attempt, request.RcloneInput, err)
		}
		legacySource, err := provider.NewRclonePrivateLocator("backup:legacy")
		if err != nil {
			t.Fatal(err)
		}
		if request.RcloneInput.PortableRequest.Source != legacySource {
			t.Fatalf("portable imported baseline source=%+v", request.RcloneInput.PortableRequest.Source)
		}
		return provider.PreparedPublication{Attempt: request.Attempt, RcloneInput: request.RcloneInput}, nil
	}
	fixture.strategy.execute = func(_ context.Context, prepared provider.PreparedPublication, _ provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
		attempt, err := prepared.Attempt.RcloneAttempt()
		if err != nil || prepared.RcloneInput == nil || prepared.RcloneInput.PortableRequest == nil {
			t.Fatalf("portable imported baseline execute attempt=%+v input=%+v err=%v", attempt, prepared.RcloneInput, err)
		}
		commit = validRcloneRepositoryCommit(attempt, prepared.RcloneInput.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
		fixture.strategy.reconcile = provider.RcloneReconcileV1{
			State:  provider.RcloneReconcileProviderCommitted,
			Commit: &commit,
			Manifest: &provider.RcloneManifestV1{
				ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
				EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes,
				FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
			},
		}
		tagged := provider.NewRcloneProviderCommit(commit)
		return provider.ProviderExecutionResult{
			ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &tagged,
		}, nil
	}
	fixture.strategy.record = func(_ context.Context, _ provider.PreparedPublication, result provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
		if result.ProviderCommit == nil {
			t.Fatal("portable imported baseline execution omitted Provider commit")
		}
		return *result.ProviderCommit, nil
	}

	result, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID,
		MigrationChoice: backupasset.RcloneImportedBaseline,
	})
	if err != nil {
		t.Fatalf("activate portable imported baseline: %v", err)
	}
	if result.MigrationChoice != backupasset.RcloneImportedBaseline || result.Summary.State != backupasset.RcloneStateCommitted ||
		result.Summary.RollbackCapability != backupasset.RcloneRollbackPreparationOnly {
		t.Fatalf("portable imported baseline activation=%+v", result)
	}
	var migrationRun model.TaskRun
	if err := fixture.db.Where("task_id = ? AND trigger_type = ?", fixture.task.ID, "migration").First(&migrationRun).Error; err != nil {
		t.Fatal(err)
	}
	if migrationRun.Status != "success" || migrationRun.FinishedAt == nil || migrationRun.LastError != "" {
		t.Fatalf("portable imported baseline TaskRun=%+v", migrationRun)
	}
	var point model.RecoveryPoint
	if err := fixture.db.Where("producing_task_run_id = ?", migrationRun.ID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if point.Semantics != string(backupasset.PointImportedBaseline) || point.State != string(backupasset.RecoveryPointCommitted) ||
		point.ManifestDigest != commit.ManifestIndexDigest || point.SourceFingerprint == "" {
		t.Fatalf("portable imported baseline point=%+v", point)
	}
	var activeLeases int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where(
		"recovery_point_id = ? AND status = ?", point.ID, backupasset.LeaseActive,
	).Count(&activeLeases).Error; err != nil || activeLeases != 0 {
		t.Fatalf("portable imported baseline active leases=%d err=%v", activeLeases, err)
	}
	var activated model.Task
	if err := fixture.db.First(&activated, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if activated.Enabled || activated.RsyncTarget != "" {
		t.Fatalf("portable imported baseline activated Task=%+v", activated)
	}
	activatedRevision, err := managedRsyncTaskRevision(activated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CleanRollbackRcloneVersioning(context.Background(), backupasset.RcloneVersioningCleanRollbackRequest{
		TaskID: activated.ID, ExpectedTaskRevision: activatedRevision, ExpectedBindingRevision: 1,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("portable imported baseline clean rollback error=%v, want conflict", err)
	}
}

func TestCleanRollbackRestoresExactLegacyStateOnlyBeforeFirstReservation(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	service.keyring = backupasset.NewKeyring(db, nil)
	service.history, _ = NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	service.admission = &rsyncVersioningTransitioner{}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{TaskID: current.ID, ExpectedTaskRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetRclonePortableBinding(context.Background(), backupasset.RclonePortableBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, SetupID: setup.SetupID, TargetRemote: "backup",
		ManagedRootLocator: "backup:managed/v1",
		BoundConfig:        "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n",
	}); err != nil {
		t.Fatal(err)
	}
	service.rclonePreflighter = &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationVersionedPrefix, CapabilityRevision: 2,
		ConsistencyClass: backupasset.RcloneConsistencyObservationallyStable,
		HashFidelity:     backupasset.RcloneHashDownloadVerifiedBytes,
		APICostClass:     backupasset.RcloneCostModerate, StorageCostClass: backupasset.RcloneCostLow, EgressCostClass: backupasset.RcloneCostHigh,
		ManagedRootIdentityDigest: strings.Repeat("a", 64), RepositoryMarkerDigest: strings.Repeat("b", 64), EvidenceDigest: strings.Repeat("c", 64),
	}}
	preflight, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	activation, err := service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	activatedRevision, err := strconv.ParseUint(activation.Summary.TaskRevision, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CleanRollbackRcloneVersioning(context.Background(), backupasset.RcloneVersioningCleanRollbackRequest{
		TaskID: current.ID, ExpectedTaskRevision: activatedRevision, ExpectedBindingRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("clean rollback result invalid: %v", err)
	}
	if result.Summary.Mode != backupasset.PublicationLegacyMutable || result.Summary.State != backupasset.RcloneStateLegacy ||
		result.Summary.ReasonCode != backupasset.RcloneReasonLegacy || result.Summary.RollbackLocatorPresent {
		t.Fatalf("clean rollback summary=%+v", result.Summary)
	}
	var restored model.Task
	if err := db.First(&restored, current.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.Enabled || restored.RsyncTarget != "backup:legacy" || restored.ExecutorConfig != `{"bandwidth_limit":"10M","transfers":4}` {
		t.Fatalf("clean rollback Task=%+v", restored)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", current.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		t.Fatal(err)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) || repository.VersionMode != string(backupasset.VersionMutableHead) ||
		repository.ImmutabilityLevel != string(backupasset.ImmutabilityMutable) || stored.V1 == nil || stored.ManagedRcloneV3 != nil {
		t.Fatalf("clean rollback state link=%+v repository=%+v binding=%+v", link, repository, stored)
	}
}

func TestFirstReservationBlocksCleanRollbackAndPreparationPreservesEvidence(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{"bandwidth_limit":"10M","transfers":4}`)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	service.keyring = backupasset.NewKeyring(db, nil)
	service.history, _ = NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	service.admission = &rsyncVersioningTransitioner{}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var current model.Task
	if err := db.First(&current, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	var currentNode model.Node
	if err := db.First(&currentNode, current.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(current)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.CreateRclonePortableBindingSetup(context.Background(), backupasset.RcloneBindingSetupRequest{TaskID: current.ID, ExpectedTaskRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetRclonePortableBinding(context.Background(), backupasset.RclonePortableBindingRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, SetupID: setup.SetupID, TargetRemote: "backup",
		ManagedRootLocator: "backup:managed/v1",
		BoundConfig:        "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n",
	}); err != nil {
		t.Fatal(err)
	}
	service.rclonePreflighter = &rcloneVersioningPreflighterStub{evidence: RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationVersionedPrefix, CapabilityRevision: 2,
		ConsistencyClass: backupasset.RcloneConsistencyObservationallyStable,
		HashFidelity:     backupasset.RcloneHashDownloadVerifiedBytes,
		APICostClass:     backupasset.RcloneCostModerate, StorageCostClass: backupasset.RcloneCostLow, EgressCostClass: backupasset.RcloneCostHigh,
		ManagedRootIdentityDigest: strings.Repeat("a", 64), RepositoryMarkerDigest: strings.Repeat("b", 64), EvidenceDigest: strings.Repeat("c", 64),
	}}
	preflight, err := service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	activation, err := service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: current.ID, ExpectedTaskRevision: revision, PreflightID: preflight.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	activatedRevision, err := strconv.ParseUint(activation.Summary.TaskRevision, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", current.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	pointID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: link.RepositoryID, ProducingTaskID: &current.ID,
		ProducingTaskNameSnapshot: current.Name, ProducingNodeIDSnapshot: current.NodeID, ProducingNodeNameSnapshot: currentNode.Name,
		Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointFailed),
		ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), PhysicalAvailability: string(backupasset.PhysicalUnknown),
		HoldState: string(backupasset.HoldNone), CapabilityRevision: 2,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.CleanRollbackRcloneVersioning(context.Background(), backupasset.RcloneVersioningCleanRollbackRequest{
		TaskID: current.ID, ExpectedTaskRevision: activatedRevision, ExpectedBindingRevision: 1,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("clean rollback with failed point error=%v, want conflict", err)
	}
	prepared, err := service.PrepareRcloneVersioningRollback(context.Background(), backupasset.RcloneVersioningRollbackPreparationRequest{
		TaskID: current.ID, ExpectedTaskRevision: activatedRevision, ExpectedBindingRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("rollback preparation result invalid: %v", err)
	}
	if prepared.Summary.State != backupasset.RcloneStateRollbackPrepared || prepared.Summary.ReasonCode != backupasset.RcloneReasonRollbackPrepared ||
		prepared.Summary.RollbackCapability != backupasset.RcloneRollbackPrepared || !prepared.Summary.RollbackLocatorPresent {
		t.Fatalf("rollback preparation summary=%+v", prepared.Summary)
	}
	var taskAfter model.Task
	if err := db.First(&taskAfter, current.ID).Error; err != nil {
		t.Fatal(err)
	}
	if taskAfter.Enabled || taskAfter.RsyncTarget != "backup:legacy" {
		t.Fatalf("rollback-prepared Task=%+v", taskAfter)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", link.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ManagedRcloneV3 == nil || !stored.ManagedRcloneV3.RollbackPrepared {
		t.Fatalf("rollback-prepared binding=%+v", stored)
	}
	var preserved model.RecoveryPoint
	if err := db.First(&preserved, "id = ?", point.ID).Error; err != nil || preserved.State != string(backupasset.RecoveryPointFailed) {
		t.Fatalf("rollback preparation lost failed evidence: point=%+v err=%v", preserved, err)
	}
}

func TestRcloneVersioningSummaryProjectsReadyAndFailsClosedForStoredModeDrift(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)

	ready, err := fixture.service.RcloneVersioningSummary(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("preflight summary: %v", err)
	}
	if ready.State != backupasset.RcloneStateReady || ready.ReasonCode != backupasset.RcloneReasonReady ||
		ready.TaskRevision != strconv.FormatUint(fixture.revision, 10) || ready.BindingRevision != "1" ||
		ready.EstimatedReadBytes != "4096" || ready.RollbackCapability != backupasset.RcloneRollbackCleanAvailable {
		t.Fatalf("preflight summary=%+v", ready)
	}

	activation, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID,
		MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if err != nil {
		t.Fatalf("activate first-new Rclone versioning: %v", err)
	}
	activated, err := fixture.service.RcloneVersioningSummary(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("activated summary: %v", err)
	}
	if activated.State != activation.Summary.State || activated.Mode != backupasset.PublicationVersionedPrefix ||
		activated.BindingRevision != "1" || activated.CapabilityRevision != "2" {
		t.Fatalf("activated summary=%+v activation=%+v", activated, activation.Summary)
	}

	if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("task_id = ?", fixture.task.ID).
		Update("publication_mode", "unknown_rclone_mode").Error; err != nil {
		t.Fatal(err)
	}
	blocked, err := fixture.service.RcloneVersioningSummary(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("drift summary: %v", err)
	}
	if blocked.State != backupasset.RcloneStateBlocked || blocked.ReasonCode != backupasset.RcloneReasonUnsupportedProfile ||
		blocked.RollbackCapability != backupasset.RcloneRollbackPreparationOnly {
		t.Fatalf("drift summary=%+v", blocked)
	}
}
