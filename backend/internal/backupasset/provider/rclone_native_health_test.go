package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

type rcloneNativeHealthS3Fake struct {
	identity   RcloneNativeBucketIdentity
	versioning RcloneNativeVersioningObservation
	lifecycle  RcloneNativeLifecycleObservation
	encryption RcloneNativeBucketEncryption
	head       RcloneNativeExactObjectHead
	payload    []byte
	err        error
}

func (fake *rcloneNativeHealthS3Fake) BucketIdentity(context.Context, RcloneNativeProfile) (RcloneNativeBucketIdentity, error) {
	return fake.identity, fake.err
}

func (fake *rcloneNativeHealthS3Fake) GetVersioning(context.Context, RcloneNativeProfile) (RcloneNativeVersioningObservation, error) {
	return fake.versioning, fake.err
}

func (fake *rcloneNativeHealthS3Fake) GetLifecycle(context.Context, RcloneNativeProfile) (RcloneNativeLifecycleObservation, error) {
	return fake.lifecycle, fake.err
}

func (fake *rcloneNativeHealthS3Fake) GetEncryption(context.Context, RcloneNativeProfile) (RcloneNativeBucketEncryption, error) {
	return fake.encryption, fake.err
}

func (fake *rcloneNativeHealthS3Fake) HeadVersion(context.Context, RcloneNativeExactReadRequest) (RcloneNativeExactObjectHead, error) {
	return fake.head, fake.err
}

func (fake *rcloneNativeHealthS3Fake) OpenVersion(context.Context, RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	return io.NopCloser(bytes.NewReader(fake.payload)), nil
}

type rcloneNativeHealthKMSFake struct {
	keys map[string]RcloneNativeKMSKey
	err  error
}

func (fake *rcloneNativeHealthKMSFake) DescribeKey(_ context.Context, arn string) (RcloneNativeKMSKey, error) {
	if fake.err != nil {
		return RcloneNativeKMSKey{}, fake.err
	}
	return fake.keys[arn], nil
}

func TestRcloneNativeHealthRevalidatesCapabilityAndExactReferencedVersion(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	versioning := RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"}
	versioningDigest, err := CanonicalRcloneNativeVersioningDigest(versioning)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := RcloneNativeLifecycleObservation{Rules: []RcloneNativeLifecycleRule{{ID: "safe", Enabled: true, Prefix: profile.ManagedPrefix}}}
	lifecycleDigest, err := CanonicalRcloneNativeLifecycleDigest(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	encryption := RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true}
	encryptionDigest, err := CanonicalRcloneNativeBucketEncryptionDigest(encryption)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("FAKE_HEALTH_REFERENCE_BYTES_FOR_TEST_ONLY")
	entry := RcloneNativePointViewEntry{
		LogicalPath: "health", PhysicalKey: profile.ManagedPrefix + "data/health", VersionID: "opaque-health-v1",
		Kind: RcloneNativeObjectVersion, Size: uint64(len(payload)), ContentDigest: sha256Hex(payload),
		EncryptionProfile: RcloneNativeSSES3V1,
	}
	s3 := &rcloneNativeHealthS3Fake{
		identity:   RcloneNativeBucketIdentity{AccountID: "123456789012", Region: profile.Region, Kind: RcloneNativeBucketGeneralPurpose},
		versioning: versioning, lifecycle: lifecycle, encryption: encryption, payload: payload,
		head: RcloneNativeExactObjectHead{
			PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID, Size: entry.Size, EncryptionProfile: entry.EncryptionProfile,
		},
	}
	result, err := CheckRcloneNativeHealth(context.Background(), RcloneNativeHealthDependencies{S3: s3}, RcloneNativeHealthRequest{
		Profile: profile, ExpectedAccountID: "123456789012", StableObservedAt: now.Add(-time.Hour), CheckedAt: now,
		VersioningDigest: versioningDigest, LifecycleDigest: lifecycleDigest, BucketEncryptionDigest: encryptionDigest,
		Encryption:         RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
		ExpectedEncryption: RcloneNativeEncryptionEvidence{Profile: RcloneNativeSSES3V1},
		References:         []RcloneNativeHealthReference{{Entry: entry}}, MaxVerifyBytes: uint64(len(payload)),
	})
	if err != nil || result.Reason != backupasset.RcloneReasonReady || result.EvidenceDigest == "" || result.VerifiedReferenceCount != 1 {
		t.Fatalf("health=%+v err=%v", result, err)
	}

	s3.err = errors.New("FAKE_S3_OUTAGE_FOR_TEST_ONLY")
	if _, err := CheckRcloneNativeHealth(context.Background(), RcloneNativeHealthDependencies{S3: s3}, RcloneNativeHealthRequest{
		Profile: profile, ExpectedAccountID: "123456789012", StableObservedAt: now.Add(-time.Hour), CheckedAt: now,
		VersioningDigest: versioningDigest, LifecycleDigest: lifecycleDigest, BucketEncryptionDigest: encryptionDigest,
		Encryption:         RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
		ExpectedEncryption: RcloneNativeEncryptionEvidence{Profile: RcloneNativeSSES3V1}, References: []RcloneNativeHealthReference{{Entry: entry}}, MaxVerifyBytes: uint64(len(payload)),
	}); rcloneNativeReason(err) != backupasset.RcloneReasonProviderUnavailable {
		t.Fatalf("outage error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

func TestRcloneNativeHealthMarksUnavailableHistoricalKMSKeyAtRisk(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	versioning := RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"}
	versioningDigest, _ := CanonicalRcloneNativeVersioningDigest(versioning)
	lifecycle := RcloneNativeLifecycleObservation{}
	lifecycleDigest, _ := CanonicalRcloneNativeLifecycleDigest(lifecycle)
	keyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-HEALTH-FOR-TEST-ONLY"
	key := RcloneNativeKMSKey{
		ARN: keyARN, AccountID: "123456789012", Region: profile.Region, Manager: "CUSTOMER", Spec: "SYMMETRIC_DEFAULT",
		Usage: "ENCRYPT_DECRYPT", State: "Disabled", Origin: "AWS_KMS",
	}
	encryption := RcloneNativeBucketEncryption{Algorithm: "aws:kms", KMSKeyARN: keyARN, BlockedEncryptionTypesKnown: true}
	encryptionDigest, _ := CanonicalRcloneNativeBucketEncryptionDigest(encryption)
	request := RcloneNativeHealthRequest{
		Profile: profile, ExpectedAccountID: "123456789012", StableObservedAt: now.Add(-time.Hour), CheckedAt: now,
		VersioningDigest: versioningDigest, LifecycleDigest: lifecycleDigest, BucketEncryptionDigest: encryptionDigest,
		Encryption:         RcloneNativeEncryptionSelection{Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: keyARN},
		ExpectedEncryption: RcloneNativeEncryptionEvidence{Profile: RcloneNativeSSEKMSV1, ActiveKeyDigest: strings.Repeat("a", 64), ReadKeySetDigest: strings.Repeat("b", 64)},
		KMSLimits:          RcloneNativeKMSLimits{MaxReadKeys: 8, MaxSerializedBytes: 4096},
		References:         []RcloneNativeHealthReference{{KMSKeyDigest: strings.Repeat("c", 64), Entry: RcloneNativePointViewEntry{}}}, MaxVerifyBytes: 1024,
	}
	s3 := &rcloneNativeHealthS3Fake{
		identity:   RcloneNativeBucketIdentity{AccountID: "123456789012", Region: profile.Region, Kind: RcloneNativeBucketGeneralPurpose},
		versioning: versioning, lifecycle: lifecycle, encryption: encryption,
	}
	if _, err := CheckRcloneNativeHealth(context.Background(), RcloneNativeHealthDependencies{
		S3: s3, KMS: &rcloneNativeHealthKMSFake{keys: map[string]RcloneNativeKMSKey{keyARN: key}},
	}, request); rcloneNativeReason(err) != backupasset.RcloneReasonKMSKeyUnavailable {
		t.Fatalf("disabled KMS error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

var _ RcloneNativeHealthS3 = (*rcloneNativeHealthS3Fake)(nil)
var _ KMSKeyInspector = (*rcloneNativeHealthKMSFake)(nil)
