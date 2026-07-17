package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
)

type productionRcloneCommandPreflightFake struct {
	portableResult provider.RclonePortableCommandPreflightResult
	portableCalls  int
	nativeResult   provider.RcloneNativeCommandPreflightResult
	nativeCalls    int
	onNativeWrite  func(provider.RcloneNativeCommandPreflightRequest)
}

func (fake *productionRcloneCommandPreflightFake) PreflightPortable(
	_ context.Context,
	_ provider.RclonePortableCommandPreflightRequest,
) (provider.RclonePortableCommandPreflightResult, error) {
	fake.portableCalls++
	return fake.portableResult, nil
}

func (fake *productionRcloneCommandPreflightFake) WriteNativeCanary(
	_ context.Context,
	request provider.RcloneNativeCommandPreflightRequest,
) (provider.RcloneNativeCommandPreflightResult, error) {
	fake.nativeCalls++
	if fake.onNativeWrite != nil {
		fake.onNativeWrite(request)
	}
	return fake.nativeResult, nil
}

func TestProductionRclonePreflighterRejectsNilNativeReceiver(t *testing.T) {
	var preflighter *productionRcloneVersioningPreflighter
	_, err := preflighter.PreflightNative(context.Background(), RcloneNativePreflightInput{})
	if !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("nil native preflighter error=%v, want ErrInvalidState", err)
	}
}

func TestProductionRclonePreflighterMapsPortableCommandEvidence(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	command := &productionRcloneCommandPreflightFake{portableResult: provider.RclonePortableCommandPreflightResult{
		ManagedRootIdentityDigest: strings.Repeat("a", 64), RepositoryMarkerDigest: strings.Repeat("b", 64),
		EvidenceDigest: strings.Repeat("c", 64), VerifiedBytes: 64,
	}}
	preflighter, err := NewProductionRcloneVersioningPreflighter(RcloneProductionPreflightDependencies{
		CommandPlane: command, IdentityKey: []byte("FAKE_RCLONE_PREFLIGHT_IDENTITY_KEY_FOR_TEST_ONLY"),
		Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 256)),
	})
	if err != nil {
		t.Fatal(err)
	}
	configBytes := []byte("[archive]\ntype = s3\nprovider = AWS\naccess_key_id = FAKE_ACCESS_KEY_FOR_TEST_ONLY\nsecret_access_key = FAKE_SECRET_ACCESS_KEY_FOR_TEST_ONLY\n")
	bound, err := provider.ValidateRcloneBoundConfigV1744(configBytes, "archive", []byte("FAKE_BOUND_CONFIG_KEY_FOR_TEST_ONLY"), int64(len(configBytes)))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := preflighter.PreflightPortable(context.Background(), RclonePortablePreflightInput{
		PreflightID: strings.Repeat("d", 32), TaskID: 7, NodeID: 8, BindingRevision: 3,
		BoundConfig: bound, TargetRemote: "archive", ManagedRootLocator: "archive:managed", LegacyLocator: "archive:legacy",
		AbsoluteDeadline: now.Add(time.Hour), LowLevelRetries: 3, ControlPayloadMaxBytes: 1 << 20,
		FullVerifyMaxBytes: 1 << 20, ManifestOptions: testRclonePreflightManifestOptions(),
	})
	if err != nil {
		t.Fatalf("portable production preflight: %v", err)
	}
	if evidence.Settling || command.portableCalls != 1 || evidence.Mode != backupasset.PublicationVersionedPrefix ||
		evidence.CapabilityRevision != 4 || evidence.ConsistencyClass != backupasset.RcloneConsistencyObservationallyStable ||
		evidence.HashFidelity != backupasset.RcloneHashDownloadVerifiedBytes || evidence.EstimatedReadBytes != 64 ||
		evidence.ManagedRootIdentityDigest != strings.Repeat("a", 64) || evidence.RepositoryMarkerDigest != strings.Repeat("b", 64) ||
		evidence.EvidenceDigest != strings.Repeat("c", 64) {
		t.Fatalf("portable production evidence=%+v calls=%d", evidence, command.portableCalls)
	}
}

func TestProductionRclonePreflighterRequiresStableNativeWindowAndExactCanary(t *testing.T) {
	clock := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	preflightID := strings.Repeat("e", 32)
	payload := bytes.Repeat([]byte{0x42}, 64)
	payloadDigest := sha256.Sum256(payload)
	rangeDigest := sha256.Sum256(payload[:16])
	profile := provider.RcloneNativeProfile{
		Code: provider.RcloneNativeAWSS3GeneralPurposeV1, Region: "us-east-1", Bucket: "xirang-preflight-bucket",
		ManagedPrefix: "managed/", EndpointMode: provider.RcloneNativeEndpointAWSRegional,
		AddressingMode: provider.RcloneNativeAddressingDNS, BucketKind: provider.RcloneNativeBucketGeneralPurpose,
	}
	s3 := &productionRclonePreflightS3Fake{
		profile: profile, now: func() time.Time { return clock }, payload: payload,
		versioning: provider.RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"},
		lifecycle:  provider.RcloneNativeLifecycleObservation{Rules: []provider.RcloneNativeLifecycleRule{}},
		encryption: provider.RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true},
	}
	command := &productionRcloneCommandPreflightFake{nativeResult: provider.RcloneNativeCommandPreflightResult{
		PayloadDigest: hex.EncodeToString(payloadDigest[:]), PayloadBytes: 64,
		RangeDigest: hex.EncodeToString(rangeDigest[:]), RangeBytes: 16,
	}}
	command.onNativeWrite = func(request provider.RcloneNativeCommandPreflightRequest) {
		s3.records = []provider.RcloneNativeVersionRecord{{
			PhysicalKey: profile.ManagedPrefix + "control/preflight/" + request.PreflightID + "/canary.bin",
			VersionID:   "opaque-canary-version", Kind: provider.RcloneNativeObjectVersion, IsLatest: true,
			Size: 64, LastModified: clock, ContentDigest: hex.EncodeToString(payloadDigest[:]),
			EncryptionProfile: provider.RcloneNativeSSES3V1,
		}}
	}
	factory := &productionRclonePreflightAWSFactoryFake{
		now: func() time.Time { return clock }, externalID: "xirang-external-id-for-test-only", s3: s3,
	}
	preflighter, err := NewProductionRcloneVersioningPreflighter(RcloneProductionPreflightDependencies{
		CommandPlane: command, IdentityKey: []byte("FAKE_RCLONE_PREFLIGHT_IDENTITY_KEY_FOR_TEST_ONLY"),
		Now: func() time.Time { return clock }, Random: bytes.NewReader(bytes.Repeat([]byte{0x33}, 2048)),
		NativeFactory: func(context.Context, provider.RcloneNativeBootstrap, string, int) (RcloneNativePreflightFactory, error) {
			return factory, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := RcloneNativePreflightInput{
		PreflightID: preflightID, TaskID: 7, NodeID: 8, BindingRevision: 3,
		Request: backupasset.RcloneNativeBindingRequest{
			TaskID: 7, ExpectedTaskRevision: 9, ExpectedBindingRevision: 2, SetupID: strings.Repeat("f", 32),
			Region: profile.Region, Bucket: profile.Bucket, ManagedPrefix: profile.ManagedPrefix,
			RoleARN:           "arn:aws:iam::123456789012:role/xirang-rclone-preflight",
			Bootstrap:         backupasset.RcloneNativeBootstrapInput{Mode: backupasset.RcloneBootstrapWorkloadChain},
			EncryptionProfile: backupasset.RcloneEncryptionSSES3,
		},
		ExternalID: factory.externalID, LegacyLocator: "legacy:bucket/root", AbsoluteDeadline: clock.Add(45 * time.Minute),
		AWSSDKMaxAttempts: 3, LowLevelRetries: 3, ControlPayloadMaxBytes: 1 << 20,
		FullVerifyMaxBytes: 1 << 20, KMSReadKeyMaxCount: 8,
		ObservationLimits: provider.RcloneNativeObservationLimits{PageSize: 100, MaxPages: 10, MaxRecords: 1000},
	}

	settling, err := preflighter.PreflightNative(context.Background(), input)
	if err != nil {
		t.Fatalf("first native observation: %v", err)
	}
	if !settling.Settling || settling.SettlingObservedAt != clock || command.nativeCalls != 0 {
		t.Fatalf("first native evidence=%+v nativeCalls=%d", settling, command.nativeCalls)
	}
	clock = clock.Add(14 * time.Minute)
	settling, err = preflighter.PreflightNative(context.Background(), input)
	if err != nil || !settling.Settling || command.nativeCalls != 0 {
		t.Fatalf("early native evidence=%+v calls=%d err=%v", settling, command.nativeCalls, err)
	}
	clock = clock.Add(time.Minute)
	input.AbsoluteDeadline = clock.Add(45 * time.Minute)
	ready, err := preflighter.PreflightNative(context.Background(), input)
	if err != nil {
		t.Fatalf("settled native preflight: %v", err)
	}
	if ready.Settling || command.nativeCalls != 1 || s3.deleteCalls != 1 || ready.Mode != backupasset.PublicationNativeObjectVersions ||
		ready.CapabilityRevision != 4 || ready.ConsistencyClass != backupasset.RcloneConsistencyProviderStrong ||
		ready.HashFidelity != backupasset.RcloneHashDownloadVerifiedBytes || ready.CredentialExpiresAt == nil ||
		ready.EncryptionProfile != backupasset.RcloneEncryptionSSES3 || ready.KMSKeyStatus != backupasset.RcloneKMSNotApplicable ||
		ready.Native == nil || ready.Native.CapabilityStableObservedAt != clock.Add(-15*time.Minute) ||
		!isLowerHex64(ready.Native.CanaryEncryptionEvidenceDigest) {
		t.Fatalf("ready native evidence=%+v calls=%d deletes=%d", ready, command.nativeCalls, s3.deleteCalls)
	}
}

type productionRclonePreflightAWSFactoryFake struct {
	now        func() time.Time
	externalID string
	s3         provider.RcloneNativeCanaryS3
}

func (*productionRclonePreflightAWSFactoryFake) BootstrapCredentialsExpire(context.Context) (bool, error) {
	return false, nil
}

func (fake *productionRclonePreflightAWSFactoryFake) AssumeRole(_ context.Context, request provider.RcloneNativeAssumeRoleRequest) (provider.RcloneNativeAssumeRoleResult, error) {
	if request.ExternalID == nil || *request.ExternalID != fake.externalID {
		return provider.RcloneNativeAssumeRoleResult{}, provider.ErrRcloneNativeAssumeRoleDenied
	}
	session, err := provider.NewRcloneNativeSession(
		"FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY", "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY",
		"FAKE_AWS_SESSION_TOKEN_FOR_TEST_ONLY", "123456789012", strings.Repeat("1", 64), fake.now().Add(request.Duration),
	)
	if err != nil {
		return provider.RcloneNativeAssumeRoleResult{}, err
	}
	return provider.RcloneNativeAssumeRoleResult{Session: session, PackedPolicySize: 10}, nil
}

func (*productionRclonePreflightAWSFactoryFake) Probe(context.Context, provider.RcloneNativeDenyProbeRequest) (provider.RcloneNativeDenyProbeResult, error) {
	return provider.RcloneNativeDenyProbeResult{Denied: true}, nil
}

func (fake *productionRclonePreflightAWSFactoryFake) S3(provider.RcloneNativeSession, provider.RcloneNativeProfile, []provider.RcloneNativeKMSKeyDigestBinding) (provider.S3Native, error) {
	return fake.s3, nil
}

func (*productionRclonePreflightAWSFactoryFake) KMS(provider.RcloneNativeSession, string) (provider.KMSKeyInspector, error) {
	return &productionRclonePreflightKMSFake{}, nil
}

type productionRclonePreflightKMSFake struct{}

func (*productionRclonePreflightKMSFake) DescribeKey(context.Context, string) (provider.RcloneNativeKMSKey, error) {
	return provider.RcloneNativeKMSKey{}, nil
}

type productionRclonePreflightS3Fake struct {
	profile     provider.RcloneNativeProfile
	now         func() time.Time
	payload     []byte
	versioning  provider.RcloneNativeVersioningObservation
	lifecycle   provider.RcloneNativeLifecycleObservation
	encryption  provider.RcloneNativeBucketEncryption
	records     []provider.RcloneNativeVersionRecord
	deleteCalls int
}

func (fake *productionRclonePreflightS3Fake) BucketIdentity(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeBucketIdentity, error) {
	return provider.RcloneNativeBucketIdentity{AccountID: "123456789012", Region: fake.profile.Region, Kind: provider.RcloneNativeBucketGeneralPurpose}, nil
}
func (fake *productionRclonePreflightS3Fake) GetVersioning(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeVersioningObservation, error) {
	return fake.versioning, nil
}
func (fake *productionRclonePreflightS3Fake) GetLifecycle(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeLifecycleObservation, error) {
	return fake.lifecycle, nil
}
func (fake *productionRclonePreflightS3Fake) GetEncryption(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeBucketEncryption, error) {
	return fake.encryption, nil
}
func (fake *productionRclonePreflightS3Fake) ListVersionPage(_ context.Context, request provider.RcloneNativeVersionPageRequest) (provider.RcloneNativeVersionPage, error) {
	result := make([]provider.RcloneNativeVersionRecord, 0, len(fake.records))
	for _, record := range fake.records {
		if strings.HasPrefix(record.PhysicalKey, request.Prefix) {
			result = append(result, record)
		}
	}
	return provider.RcloneNativeVersionPage{Records: result}, nil
}
func (fake *productionRclonePreflightS3Fake) HeadVersion(_ context.Context, request provider.RcloneNativeExactReadRequest) (provider.RcloneNativeExactObjectHead, error) {
	for _, record := range fake.records {
		if record.PhysicalKey == request.PhysicalKey && record.VersionID == request.VersionID && record.Kind == provider.RcloneNativeObjectVersion {
			return provider.RcloneNativeExactObjectHead{
				PhysicalKey: record.PhysicalKey, VersionID: record.VersionID, Size: record.Size,
				EncryptionProfile: record.EncryptionProfile, KMSKeyDigest: record.KMSKeyDigest, BucketKeyEnabled: record.BucketKeyEnabled,
			}, nil
		}
	}
	return provider.RcloneNativeExactObjectHead{}, io.EOF
}
func (fake *productionRclonePreflightS3Fake) OpenVersion(context.Context, provider.RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(fake.payload)), nil
}
func (fake *productionRclonePreflightS3Fake) OpenVersionRange(_ context.Context, request provider.RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(fake.payload[request.Offset : request.Offset+request.Length])), nil
}
func (*productionRclonePreflightS3Fake) PutControlVersion(context.Context, provider.RcloneNativeControlWriteRequest) (provider.RcloneNativeControlWriteResult, error) {
	return provider.RcloneNativeControlWriteResult{VersionID: "unused-control-version"}, nil
}
func (fake *productionRclonePreflightS3Fake) DeleteCurrentCanary(_ context.Context, request provider.RcloneNativeCurrentDeleteRequest) (provider.RcloneNativeCurrentDeleteResult, error) {
	fake.deleteCalls++
	for index := range fake.records {
		if fake.records[index].PhysicalKey == request.PhysicalKey {
			fake.records[index].IsLatest = false
		}
	}
	marker := provider.RcloneNativeVersionRecord{
		PhysicalKey: request.PhysicalKey, VersionID: "opaque-delete-marker", Kind: provider.RcloneNativeDeleteMarker,
		IsLatest: true, LastModified: fake.now(),
	}
	fake.records = append(fake.records, marker)
	return provider.RcloneNativeCurrentDeleteResult{VersionID: marker.VersionID}, nil
}

func testRclonePreflightManifestOptions() provider.RcloneManifestBuildOptions {
	return provider.RcloneManifestBuildOptions{
		Limits:        provider.ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 1 << 16, MaxDepth: 32},
		ChunkMaxBytes: 1 << 16, ChunkMaxEntries: 100, SpoolMaxBytes: 1 << 20,
	}
}

var _ provider.RclonePreflightCommandPlane = (*productionRcloneCommandPreflightFake)(nil)
var _ RcloneNativePreflightFactory = (*productionRclonePreflightAWSFactoryFake)(nil)
var _ provider.RcloneNativeCanaryS3 = (*productionRclonePreflightS3Fake)(nil)
