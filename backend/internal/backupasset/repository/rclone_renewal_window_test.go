package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

// Only the AWS and command boundaries are controlled; the service and native
// production preflighter retain their normal candidate and observation logic.
func TestRcloneVersioningRenewalNativeProductionObservationWindow(t *testing.T) {
	fixture := newRcloneNativeVersioningWorkflowFixture(t)
	clock := fixture.now
	profile := provider.RcloneNativeProfile{Code: provider.RcloneNativeAWSS3GeneralPurposeV1, Region: "us-east-1", Bucket: "xirang-managed-test", ManagedPrefix: "managed/v1/", EndpointMode: provider.RcloneNativeEndpointAWSRegional, AddressingMode: provider.RcloneNativeAddressingDNS, BucketKind: provider.RcloneNativeBucketGeneralPurpose}
	payload := bytes.Repeat([]byte{0x42}, 64)
	payloadDigest, rangeDigest := sha256.Sum256(payload), sha256.Sum256(payload[:16])
	s3 := &productionRclonePreflightS3Fake{profile: profile, now: func() time.Time { return clock }, payload: payload,
		versioning: provider.RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"},
		lifecycle:  provider.RcloneNativeLifecycleObservation{}, encryption: provider.RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true}}
	command := &productionRcloneCommandPreflightFake{nativeResult: provider.RcloneNativeCommandPreflightResult{PayloadDigest: hex.EncodeToString(payloadDigest[:]), PayloadBytes: 64, RangeDigest: hex.EncodeToString(rangeDigest[:]), RangeBytes: 16}}
	command.onNativeWrite = func(request provider.RcloneNativeCommandPreflightRequest) {
		s3.records = []provider.RcloneNativeVersionRecord{{PhysicalKey: profile.ManagedPrefix + "control/preflight/" + request.PreflightID + "/canary.bin", VersionID: "opaque-renewal-version", Kind: provider.RcloneNativeObjectVersion, IsLatest: true, Size: 64, LastModified: clock, ContentDigest: hex.EncodeToString(payloadDigest[:]), EncryptionProfile: provider.RcloneNativeSSES3V1}}
	}
	record, ok := fixture.service.rclonePreflights.get(fixture.preflight.PreflightID)
	if !ok {
		t.Fatal("missing initial preflight")
	}
	factory := &productionRclonePreflightAWSFactoryFake{now: func() time.Time { return clock }, externalID: record.candidate.native.externalID, s3: s3}
	production, err := NewProductionRcloneVersioningPreflighter(RcloneProductionPreflightDependencies{CommandPlane: command,
		IdentityKey: []byte("FAKE_RCLONE_PREFLIGHT_IDENTITY_KEY_FOR_TEST_ONLY"), Now: func() time.Time { return clock },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x33}, 4096)), NativeFactory: func(context.Context, provider.RcloneNativeBootstrap, string, int) (RcloneNativePreflightFactory, error) {
			return factory, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := production.(*productionRcloneVersioningPreflighter).digest("native-managed-root-v1", profile)
	if err != nil {
		t.Fatal(err)
	}
	record.evidence.ManagedRootIdentityDigest = identity
	if err := fixture.service.rclonePreflights.put(record); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint})
	if err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := fixture.db.First(&task, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, _ := managedRsyncTaskRevision(task)
	plan, err := fixture.service.loadRcloneBindingWorkflowPlan(context.Background(), task.ID, revision, rcloneBindingSetupNative)
	if err != nil {
		t.Fatal(err)
	}
	pointID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	committedAt := fixture.now
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: plan.repository.ID, ProducingTaskID: &task.ID,
		ProducingTaskNameSnapshot: task.Name, ProducingNodeIDSnapshot: task.NodeID, ProducingNodeNameSnapshot: "node",
		Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalUnknown),
		HoldState: string(backupasset.HoldNone), CapabilityRevision: int(plan.managedBinding.CapabilityRevision), CommittedAt: &committedAt,
	}
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	clock = plan.managedBinding.PreflightExpiresAt.Add(time.Second)
	fixture.service.now = func() time.Time { return clock }
	fixture.service.rclonePreflighter = production
	expired, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, expired, backupasset.RcloneStatePreflightRequired, backupasset.RcloneReasonPreflightExpired, backupasset.RcloneRollbackPreparationOnly)
	request := backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationNativeObjectVersions}
	for _, advance := range []time.Duration{0, 14 * time.Minute, time.Minute} {
		clock = clock.Add(advance)
		result, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if result.Purpose != backupasset.RclonePreflightPurposeRenewal {
			t.Fatalf("purpose=%s", result.Purpose)
		}
		expectedState := backupasset.RcloneStateCapabilitySettling
		expectedReason := backupasset.RcloneReasonCapabilitySettling
		if advance == time.Minute {
			expectedState = backupasset.RcloneStateReady
			expectedReason = backupasset.RcloneReasonReady
		}
		assertRcloneRollbackSummary(t, result.Summary, expectedState, expectedReason, backupasset.RcloneRollbackPreparationOnly)
		progress, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertRcloneRollbackSummary(t, progress, expectedState, expectedReason, backupasset.RcloneRollbackPreparationOnly)
		if advance != time.Minute {
			if result.Summary.State != backupasset.RcloneStateCapabilitySettling || command.nativeCalls != 0 {
				t.Fatalf("early renewal summary=%+v canaries=%d", result.Summary, command.nativeCalls)
			}
			continue
		}
		if result.Summary.State != backupasset.RcloneStateReady || command.nativeCalls != 1 {
			t.Fatalf("settled renewal summary=%+v canaries=%d", result.Summary, command.nativeCalls)
		}
		renewed, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{TaskID: task.ID, ExpectedTaskRevision: revision, PreflightID: result.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint})
		if err != nil {
			t.Fatal(err)
		}
		assertRcloneRollbackSummary(t, renewed.Summary, backupasset.RcloneStateCommitted, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
		persisted, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertRcloneRollbackSummary(t, persisted, backupasset.RcloneStateCommitted, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
	}
}
