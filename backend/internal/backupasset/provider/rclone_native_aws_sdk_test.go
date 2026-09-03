package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"

	aws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type rcloneNativeSTSClientFake struct {
	input  *sts.AssumeRoleInput
	output *sts.AssumeRoleOutput
	err    error
}

func (fake *rcloneNativeSTSClientFake) AssumeRole(_ context.Context, input *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	fake.input = input
	return fake.output, fake.err
}

type rcloneNativeS3ClientFake struct {
	headInput        *s3.HeadObjectInput
	headOutput       *s3.HeadObjectOutput
	headError        error
	listInput        *s3.ListObjectVersionsInput
	listOutput       *s3.ListObjectVersionsOutput
	getInput         *s3.GetObjectInput
	getOutput        *s3.GetObjectOutput
	putInput         *s3.PutObjectInput
	putOutput        *s3.PutObjectOutput
	deleteInput      *s3.DeleteObjectInput
	deleteOutput     *s3.DeleteObjectOutput
	deleteError      error
	locationInput    *s3.GetBucketLocationInput
	locationOutput   *s3.GetBucketLocationOutput
	versioningInput  *s3.GetBucketVersioningInput
	versioningOutput *s3.GetBucketVersioningOutput
	lifecycleInput   *s3.GetBucketLifecycleConfigurationInput
	lifecycleOutput  *s3.GetBucketLifecycleConfigurationOutput
	lifecycleError   error
	encryptionInput  *s3.GetBucketEncryptionInput
	encryptionOutput *s3.GetBucketEncryptionOutput
}

func (fake *rcloneNativeS3ClientFake) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	fake.headInput = input
	if fake.headOutput == nil {
		fake.headOutput = &s3.HeadObjectOutput{}
	}
	return fake.headOutput, fake.headError
}
func (fake *rcloneNativeS3ClientFake) ListObjectVersions(_ context.Context, input *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	fake.listInput = input
	return fake.listOutput, nil
}
func (fake *rcloneNativeS3ClientFake) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	fake.getInput = input
	return fake.getOutput, nil
}
func (fake *rcloneNativeS3ClientFake) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	fake.putInput = input
	return fake.putOutput, nil
}
func (fake *rcloneNativeS3ClientFake) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	fake.deleteInput = input
	return fake.deleteOutput, fake.deleteError
}
func (fake *rcloneNativeS3ClientFake) GetBucketLocation(_ context.Context, input *s3.GetBucketLocationInput, _ ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error) {
	fake.locationInput = input
	return fake.locationOutput, nil
}
func (fake *rcloneNativeS3ClientFake) GetBucketVersioning(_ context.Context, input *s3.GetBucketVersioningInput, _ ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	fake.versioningInput = input
	return fake.versioningOutput, nil
}
func (fake *rcloneNativeS3ClientFake) GetBucketLifecycleConfiguration(_ context.Context, input *s3.GetBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error) {
	fake.lifecycleInput = input
	return fake.lifecycleOutput, fake.lifecycleError
}
func (fake *rcloneNativeS3ClientFake) GetBucketEncryption(_ context.Context, input *s3.GetBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error) {
	fake.encryptionInput = input
	return fake.encryptionOutput, nil
}

type rcloneNativeKMSClientFake struct {
	input  *kms.DescribeKeyInput
	output *kms.DescribeKeyOutput
}

func (fake *rcloneNativeKMSClientFake) DescribeKey(_ context.Context, input *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	fake.input = input
	return fake.output, nil
}

func TestRcloneNativeAWSSDKAssumeRoleMapsOnlyTemporarySession(t *testing.T) {
	expires := time.Date(2026, 7, 16, 11, 0, 0, 0, time.UTC)
	client := &rcloneNativeSTSClientFake{output: &sts.AssumeRoleOutput{
		Credentials: &ststypes.Credentials{
			AccessKeyId:     aws.String("FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY"),
			SecretAccessKey: aws.String("FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY"),
			SessionToken:    aws.String("FAKE_AWS_SESSION_TOKEN_FOR_TEST_ONLY"), Expiration: &expires,
		},
		AssumedRoleUser:  &ststypes.AssumedRoleUser{Arn: aws.String("arn:aws:sts::123456789012:assumed-role/xirang/test")},
		PackedPolicySize: aws.Int32(17),
	}}
	factory := newRcloneNativeAWSFactoryForTest(client, nil, aws.Config{Region: "us-east-1"}, 3)
	externalID := "FAKE_EXTERNAL_ID_FOR_TEST_ONLY"
	result, err := factory.AssumeRole(context.Background(), RcloneNativeAssumeRoleRequest{
		RoleARN: "arn:aws:iam::123456789012:role/xirang", ExternalID: &externalID,
		Duration: 47 * time.Minute, SessionPolicy: `{"Version":"2012-10-17","Statement":[]}`,
		SessionName: "xirang-rclone-publication",
	})
	if err != nil || !result.Session.valid() || result.Session.AccountID() != "123456789012" || result.PackedPolicySize != 17 {
		t.Fatalf("AssumeRole result=%+v err=%v", result, err)
	}
	if client.input == nil || aws.ToString(client.input.RoleArn) == "" || aws.ToString(client.input.ExternalId) != externalID ||
		aws.ToInt32(client.input.DurationSeconds) != 47*60 || aws.ToString(client.input.Policy) == "" {
		t.Fatalf("AssumeRole input=%+v", client.input)
	}

	denied := newRcloneNativeAWSFactoryForTest(&rcloneNativeSTSClientFake{err: &smithy.GenericAPIError{Code: "AccessDenied"}}, nil, aws.Config{Region: "us-east-1"}, 3)
	if _, err := denied.AssumeRole(context.Background(), RcloneNativeAssumeRoleRequest{
		RoleARN: "arn:aws:iam::123456789012:role/xirang", Duration: 15 * time.Minute,
		SessionPolicy: `{"Version":"2012-10-17","Statement":[]}`, SessionName: "xirang-rclone-publication",
	}); !errors.Is(err, ErrRcloneNativeAssumeRoleDenied) {
		t.Fatalf("AccessDenied error=%v", err)
	}
}

func TestRcloneNativeAWSSDKReportsBootstrapExpiryWithoutExposingCredentials(t *testing.T) {
	now := time.Now().UTC()
	staticFactory := newRcloneNativeAWSFactoryForTest(nil, nil, aws.Config{
		Region: "us-east-1",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			"FAKE_STATIC_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_STATIC_SECRET_KEY_FOR_TEST_ONLY", "",
		)),
	}, 3)
	temporaryFactory := newRcloneNativeAWSFactoryForTest(nil, nil, aws.Config{
		Region: "us-east-1",
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			"FAKE_TEMP_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_TEMP_SECRET_KEY_FOR_TEST_ONLY", "FAKE_TEMP_TOKEN_FOR_TEST_ONLY",
		)),
	}, 3)
	temporaryFactory.baseConfig.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "FAKE_TEMP_ACCESS_KEY_FOR_TEST_ONLY", SecretAccessKey: "FAKE_TEMP_SECRET_KEY_FOR_TEST_ONLY",
			SessionToken: "FAKE_TEMP_TOKEN_FOR_TEST_ONLY", CanExpire: true, Expires: now.Add(time.Hour)}, nil
	})
	for name, test := range map[string]struct {
		factory *RcloneNativeAWSFactory
		want    bool
	}{"static": {staticFactory, false}, "temporary": {temporaryFactory, true}} {
		t.Run(name, func(t *testing.T) {
			got, err := test.factory.BootstrapCredentialsExpire(context.Background())
			if err != nil || got != test.want {
				t.Fatalf("bootstrap expiry=%t err=%v want=%t", got, err, test.want)
			}
		})
	}
}

func TestRcloneNativeAWSSDKBootstrapProbeIsReadOnlyAndOwnerBound(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	for _, test := range []struct {
		name   string
		err    error
		denied bool
	}{
		{"AccessDenied", &smithy.GenericAPIError{Code: "AccessDenied"}, true},
		{"NotFound proves access", &smithy.GenericAPIError{Code: "NotFound"}, false},
		{"success proves access", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &rcloneNativeS3ClientFake{headError: test.err}
			factory := newRcloneNativeAWSFactoryForTest(nil, client, aws.Config{Region: profile.Region}, 3)
			result, err := factory.Probe(context.Background(), RcloneNativeDenyProbeRequest{Profile: profile, ExpectedAccountID: "123456789012"})
			if err != nil || result.Denied != test.denied {
				t.Fatalf("probe=%+v err=%v", result, err)
			}
			if client.headInput == nil || aws.ToString(client.headInput.Bucket) != profile.Bucket ||
				aws.ToString(client.headInput.ExpectedBucketOwner) != "123456789012" ||
				!strings.HasPrefix(aws.ToString(client.headInput.Key), profile.ManagedPrefix) {
				t.Fatalf("HeadObject input=%+v", client.headInput)
			}
		})
	}
}

func TestRcloneNativeAWSSDKMapsExpectedOwnerVersioningLifecycleAndEncryption(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	prefix := "managed/"
	id := "safe-online"
	client := &rcloneNativeS3ClientFake{
		locationOutput:   &s3.GetBucketLocationOutput{},
		versioningOutput: &s3.GetBucketVersioningOutput{Status: s3types.BucketVersioningStatusEnabled, MFADelete: s3types.MFADeleteStatusDisabled},
		lifecycleOutput: &s3.GetBucketLifecycleConfigurationOutput{Rules: []s3types.LifecycleRule{{
			ID: &id, Status: s3types.ExpirationStatusEnabled,
			Filter:      &s3types.LifecycleRuleFilter{Prefix: &prefix},
			Transitions: []s3types.Transition{{StorageClass: s3types.TransitionStorageClassGlacierIr}},
		}}},
		encryptionOutput: &s3.GetBucketEncryptionOutput{ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{Rules: []s3types.ServerSideEncryptionRule{{
			ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{SSEAlgorithm: s3types.ServerSideEncryptionAwsKms, KMSMasterKeyID: aws.String("arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-FOR-TEST-ONLY")},
			BucketKeyEnabled:                   aws.Bool(true), BlockedEncryptionTypes: &s3types.BlockedEncryptionTypes{EncryptionType: []s3types.EncryptionType{s3types.EncryptionTypeSseC}},
		}}}},
	}
	adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
	identity, err := adapter.BucketIdentity(context.Background(), profile)
	if err != nil || identity.AccountID != "123456789012" || identity.Region != "us-east-1" || identity.Kind != RcloneNativeBucketGeneralPurpose {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	versioning, err := adapter.GetVersioning(context.Background(), profile)
	if err != nil || versioning.Status != "Enabled" || versioning.MFADelete != "Disabled" {
		t.Fatalf("versioning=%+v err=%v", versioning, err)
	}
	lifecycle, err := adapter.GetLifecycle(context.Background(), profile)
	if err != nil || len(lifecycle.Rules) != 1 || lifecycle.Rules[0].Prefix != prefix || lifecycle.Rules[0].Transitions[0] != "GLACIER_IR" {
		t.Fatalf("lifecycle=%+v err=%v", lifecycle, err)
	}
	encryption, err := adapter.GetEncryption(context.Background(), profile)
	if err != nil || encryption.Algorithm != "aws:kms" || !encryption.BucketKeyEnabled || !encryption.BlockedEncryptionTypesKnown {
		t.Fatalf("encryption=%+v err=%v", encryption, err)
	}
	for _, owner := range []*string{client.locationInput.ExpectedBucketOwner, client.versioningInput.ExpectedBucketOwner, client.lifecycleInput.ExpectedBucketOwner, client.encryptionInput.ExpectedBucketOwner} {
		if aws.ToString(owner) != "123456789012" {
			t.Fatalf("missing ExpectedBucketOwner: location=%+v versioning=%+v lifecycle=%+v encryption=%+v", client.locationInput, client.versioningInput, client.lifecycleInput, client.encryptionInput)
		}
	}
}

func TestRcloneNativeAWSSDKMapsNoLifecycleAndKMSMetadataWithoutLeakingSDKTypes(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	s3Client := &rcloneNativeS3ClientFake{lifecycleError: &smithy.GenericAPIError{Code: "NoSuchLifecycleConfiguration"}}
	lifecycle, err := newRcloneNativeS3SDK(s3Client, "123456789012", profile, nil).GetLifecycle(context.Background(), profile)
	if err != nil || len(lifecycle.Rules) != 0 {
		t.Fatalf("no lifecycle=%+v err=%v", lifecycle, err)
	}
	arn := "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-FOR-TEST-ONLY"
	kmsClient := &rcloneNativeKMSClientFake{output: &kms.DescribeKeyOutput{KeyMetadata: &kmstypes.KeyMetadata{
		Arn: &arn, AWSAccountId: aws.String("123456789012"), KeyManager: kmstypes.KeyManagerTypeCustomer,
		KeySpec: kmstypes.KeySpecSymmetricDefault, KeyUsage: kmstypes.KeyUsageTypeEncryptDecrypt,
		KeyState: kmstypes.KeyStateEnabled, Origin: kmstypes.OriginTypeAwsKms, MultiRegion: aws.Bool(false),
	}}}
	key, err := newRcloneNativeKMSSDK(kmsClient).DescribeKey(context.Background(), arn)
	if err != nil || key.ARN != arn || key.Region != "us-east-1" || key.Manager != "CUSTOMER" || key.State != "Enabled" {
		t.Fatalf("KMS key=%+v err=%v", key, err)
	}
	if kmsClient.input == nil || aws.ToString(kmsClient.input.KeyId) != arn {
		t.Fatalf("DescribeKey input=%+v", kmsClient.input)
	}
}

func TestRcloneNativeAWSSDKMapsDoubleMarkerVersionsAndExactReads(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	payload := []byte("FAKE_EXACT_OBJECT_BYTES_FOR_TEST_ONLY")
	client := &rcloneNativeS3ClientFake{
		listOutput: &s3.ListObjectVersionsOutput{
			IsTruncated:         aws.Bool(true),
			NextKeyMarker:       aws.String(profile.ManagedPrefix + "data/a"),
			NextVersionIdMarker: aws.String("opaque-v1"),
			Versions: []s3types.ObjectVersion{{
				Key: aws.String(profile.ManagedPrefix + "data/a"), VersionId: aws.String("opaque-v2"),
				IsLatest: aws.Bool(true), LastModified: &now, Size: aws.Int64(int64(len(payload))),
			}},
			DeleteMarkers: []s3types.DeleteMarkerEntry{{
				Key: aws.String(profile.ManagedPrefix + "data/deleted"), VersionId: aws.String("opaque-d1"),
				IsLatest: aws.Bool(true), LastModified: &now,
			}},
		},
		headOutput: &s3.HeadObjectOutput{
			ContentLength: aws.Int64(int64(len(payload))), VersionId: aws.String("opaque-v2"),
			ServerSideEncryption: s3types.ServerSideEncryptionAes256,
		},
		getOutput: &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(payload)), VersionId: aws.String("opaque-v2")},
	}
	adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
	previousKeyMarker := profile.ManagedPrefix + "previous-key"
	page, err := adapter.ListVersionPage(context.Background(), RcloneNativeVersionPageRequest{
		Prefix: profile.ManagedPrefix, KeyMarker: previousKeyMarker, VersionIDMarker: "previous-version", MaxKeys: 100,
	})
	if err != nil || !page.Truncated || len(page.Records) != 2 || page.Records[0].Kind == page.Records[1].Kind ||
		page.NextKeyMarker == "" || page.NextVersionIDMarker == "" {
		t.Fatalf("version page=%+v err=%v", page, err)
	}
	if client.listInput == nil || aws.ToString(client.listInput.Bucket) != profile.Bucket ||
		aws.ToString(client.listInput.ExpectedBucketOwner) != "123456789012" || aws.ToString(client.listInput.Prefix) != profile.ManagedPrefix ||
		aws.ToString(client.listInput.KeyMarker) != previousKeyMarker || aws.ToString(client.listInput.VersionIdMarker) != "previous-version" {
		t.Fatalf("ListObjectVersions input=%+v", client.listInput)
	}

	request := RcloneNativeExactReadRequest{PhysicalKey: profile.ManagedPrefix + "data/a", VersionID: "opaque-v2"}
	head, err := adapter.HeadVersion(context.Background(), request)
	if err != nil || head.PhysicalKey != request.PhysicalKey || head.VersionID != request.VersionID || head.Size != uint64(len(payload)) ||
		head.EncryptionProfile != RcloneNativeSSES3V1 {
		t.Fatalf("exact head=%+v err=%v", head, err)
	}
	body, err := adapter.OpenVersion(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("exact body=%q read=%v close=%v", got, readErr, closeErr)
	}
	if client.getInput == nil || aws.ToString(client.getInput.Key) != request.PhysicalKey || aws.ToString(client.getInput.VersionId) != request.VersionID ||
		aws.ToString(client.getInput.ExpectedBucketOwner) != "123456789012" || client.getInput.Range != nil {
		t.Fatalf("GetObject input=%+v", client.getInput)
	}

	client.getOutput = &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(payload[5:10])), VersionId: aws.String("opaque-v2")}
	rangeBody, err := adapter.OpenVersionRange(context.Background(), RcloneNativeExactRangeRequest{
		PhysicalKey: request.PhysicalKey, VersionID: request.VersionID, Offset: 5, Length: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rangeBody)
	if err := rangeBody.Close(); err != nil {
		t.Fatal(err)
	}
	if client.getInput == nil || aws.ToString(client.getInput.Range) != "bytes=5-9" || aws.ToString(client.getInput.VersionId) != request.VersionID {
		t.Fatalf("range GetObject input=%+v", client.getInput)
	}
}

func TestRcloneNativeAWSSDKHeadMapsOnlyBoundFullKMSARNToDigest(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	keyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-FOR-TEST-ONLY"
	keyDigest := strings.Repeat("a", 64)
	request := RcloneNativeExactReadRequest{PhysicalKey: profile.ManagedPrefix + "data/a", VersionID: "opaque-v1"}
	client := &rcloneNativeS3ClientFake{headOutput: &s3.HeadObjectOutput{
		ContentLength: aws.Int64(5), VersionId: aws.String(request.VersionID),
		ServerSideEncryption: s3types.ServerSideEncryptionAwsKms, SSEKMSKeyId: aws.String(keyARN), BucketKeyEnabled: aws.Bool(true),
	}}
	adapter := newRcloneNativeS3SDK(client, "123456789012", profile, []RcloneNativeKMSKeyDigestBinding{{KeyARN: keyARN, Digest: keyDigest}})
	head, err := adapter.HeadVersion(context.Background(), request)
	if err != nil || head.EncryptionProfile != RcloneNativeSSEKMSV1 || head.KMSKeyDigest != keyDigest || !head.BucketKeyEnabled {
		t.Fatalf("KMS head=%+v err=%v", head, err)
	}
	if client.headInput == nil || aws.ToString(client.headInput.VersionId) != request.VersionID ||
		aws.ToString(client.headInput.ExpectedBucketOwner) != "123456789012" {
		t.Fatalf("KMS HeadObject input=%+v", client.headInput)
	}

	unbound := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
	if _, err := unbound.HeadVersion(context.Background(), request); rcloneNativeReason(err) != backupasset.RcloneReasonIdentityMismatch {
		t.Fatalf("unbound KMS head error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

func TestRcloneNativeAWSSDKBaselineClientReadsOnlyExactSourcePrefixAndKeepsKeyARNPrivate(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	sourcePrefix := "legacy/current/"
	sourceKeyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-SOURCE-KMS-FOR-TEST-ONLY"
	now := time.Now().UTC()
	payload := []byte("FAKE_BASELINE_SOURCE_BYTES_FOR_TEST_ONLY")
	client := &rcloneNativeS3ClientFake{
		listOutput: &s3.ListObjectVersionsOutput{Versions: []s3types.ObjectVersion{{
			Key: aws.String(sourcePrefix + "config.json"), VersionId: aws.String("opaque-source-v1"),
			IsLatest: aws.Bool(true), LastModified: &now, Size: aws.Int64(int64(len(payload))),
		}}},
		headOutput: &s3.HeadObjectOutput{
			ContentLength: aws.Int64(int64(len(payload))), VersionId: aws.String("opaque-source-v1"),
			ServerSideEncryption: s3types.ServerSideEncryptionAwsKms, SSEKMSKeyId: aws.String(sourceKeyARN),
		},
		getOutput: &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(payload)), VersionId: aws.String("opaque-source-v1")},
	}
	adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil, sourcePrefix)
	page, err := adapter.ListVersionPage(context.Background(), RcloneNativeVersionPageRequest{Prefix: sourcePrefix, MaxKeys: 100})
	if err != nil || len(page.Records) != 1 || page.Records[0].PhysicalKey != sourcePrefix+"config.json" {
		t.Fatalf("baseline source page=%+v err=%v", page, err)
	}
	request := RcloneNativeExactReadRequest{PhysicalKey: sourcePrefix + "config.json", VersionID: "opaque-source-v1"}
	head, err := adapter.HeadBaselineVersion(context.Background(), request)
	if err != nil || head.EncryptionProfile != RcloneNativeSSEKMSV1 || head.KMSKeyARN != sourceKeyARN || head.Size != uint64(len(payload)) {
		t.Fatalf("baseline source head=%+v err=%v", head, err)
	}
	encoded, err := json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), sourceKeyARN) {
		t.Fatalf("baseline source head leaked KMS ARN: %s", encoded)
	}
	body, err := adapter.OpenBaselineVersion(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("baseline source body=%q read=%v close=%v", got, readErr, closeErr)
	}
	if _, err := adapter.ListVersionPage(context.Background(), RcloneNativeVersionPageRequest{
		Prefix: "other/", MaxKeys: 100,
	}); err == nil {
		t.Fatal("baseline source client listed an unbound prefix")
	}
	if _, err := adapter.PutControlVersion(context.Background(), RcloneNativeControlWriteRequest{
		PhysicalKey: sourcePrefix + "control.json", Payload: []byte("forbidden"), MaxBytes: 1024,
		EncryptionProfile: RcloneNativeSSES3V1,
	}); err == nil {
		t.Fatal("baseline source client wrote outside the managed prefix")
	}
}

func TestRcloneNativeAWSSDKWritesVersionedControlWithExplicitEncryption(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	payload := []byte("FAKE_CONTROL_PAYLOAD_FOR_TEST_ONLY")
	client := &rcloneNativeS3ClientFake{putOutput: &s3.PutObjectOutput{VersionId: aws.String("opaque-control-v1")}}
	adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
	result, err := adapter.PutControlVersion(context.Background(), RcloneNativeControlWriteRequest{
		PhysicalKey: profile.ManagedPrefix + "control/commit.json", Payload: payload, MaxBytes: 1024,
		EncryptionProfile: RcloneNativeSSES3V1,
	})
	if err != nil || result.VersionID != "opaque-control-v1" {
		t.Fatalf("SSE-S3 write=%+v err=%v", result, err)
	}
	if client.putInput == nil || aws.ToString(client.putInput.Bucket) != profile.Bucket ||
		aws.ToString(client.putInput.ExpectedBucketOwner) != "123456789012" || client.putInput.ServerSideEncryption != s3types.ServerSideEncryptionAes256 ||
		client.putInput.SSEKMSKeyId != nil || aws.ToBool(client.putInput.BucketKeyEnabled) {
		t.Fatalf("SSE-S3 PutObject input=%+v", client.putInput)
	}
	written, readErr := io.ReadAll(client.putInput.Body)
	if readErr != nil || !bytes.Equal(written, payload) {
		t.Fatalf("SSE-S3 payload=%q err=%v", written, readErr)
	}

	keyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-FOR-TEST-ONLY"
	keyDigest := strings.Repeat("a", 64)
	kmsAdapter := newRcloneNativeS3SDK(client, "123456789012", profile, []RcloneNativeKMSKeyDigestBinding{{KeyARN: keyARN, Digest: keyDigest}})
	result, err = kmsAdapter.PutControlVersion(context.Background(), RcloneNativeControlWriteRequest{
		PhysicalKey: profile.ManagedPrefix + "control/manifest-index.json", Payload: payload, MaxBytes: 1024,
		EncryptionProfile: RcloneNativeSSEKMSV1, KMSKeyARN: keyARN, KMSKeyDigest: keyDigest, BucketKeyEnabled: true,
	})
	if err != nil || result.VersionID == "" || client.putInput.ServerSideEncryption != s3types.ServerSideEncryptionAwsKms ||
		aws.ToString(client.putInput.SSEKMSKeyId) != keyARN || !aws.ToBool(client.putInput.BucketKeyEnabled) {
		t.Fatalf("SSE-KMS write=%+v input=%+v err=%v", result, client.putInput, err)
	}

	if _, err := kmsAdapter.PutControlVersion(context.Background(), RcloneNativeControlWriteRequest{
		PhysicalKey: profile.ManagedPrefix + "control/commit.json", Payload: payload, MaxBytes: 1024,
		EncryptionProfile: RcloneNativeSSEKMSV1, KMSKeyARN: keyARN, KMSKeyDigest: strings.Repeat("b", 64), BucketKeyEnabled: true,
	}); rcloneNativeReason(err) != backupasset.RcloneReasonIdentityMismatch {
		t.Fatalf("wrong KMS digest error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

func TestRcloneNativeExactVersionMethodsRejectKeysOutsideManagedPrefix(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	outside := RcloneNativeExactVersion{PhysicalKey: "other/v1/data/file.bin", VersionID: "v-outside-1"}
	owned := RcloneNativeExactVersion{PhysicalKey: profile.ManagedPrefix + "data/file.bin", VersionID: "v-owned-1"}

	t.Run("outside_prefix_probe_and_delete_never_call_s3", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), outside)
		if !errors.Is(err, backupasset.ErrInvalidState) || probe.Present || probe.Locked {
			t.Fatalf("outside ProbeExactVersion probe=%+v err=%v, want ErrInvalidState", probe, err)
		}
		if client.headInput != nil {
			t.Fatalf("outside ProbeExactVersion called HeadObject: %+v", client.headInput)
		}
		if err := adapter.DeleteExactVersion(context.Background(), outside); !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("outside DeleteExactVersion err=%v, want ErrInvalidState", err)
		}
		if client.headInput != nil || client.deleteInput != nil {
			t.Fatalf("outside DeleteExactVersion called S3 head=%+v delete=%+v", client.headInput, client.deleteInput)
		}
	})

	t.Run("owned_prefix_probe_present", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headOutput: &s3.HeadObjectOutput{}}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err != nil || !probe.Present || probe.Locked {
			t.Fatalf("owned ProbeExactVersion probe=%+v err=%v", probe, err)
		}
		if client.headInput == nil || aws.ToString(client.headInput.Key) != owned.PhysicalKey ||
			aws.ToString(client.headInput.VersionId) != owned.VersionID {
			t.Fatalf("owned HeadObject input=%+v", client.headInput)
		}
	})

	t.Run("owned_prefix_already_absent", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headError: &smithy.GenericAPIError{Code: "NotFound"}}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err != nil || probe.Present || probe.Locked {
			t.Fatalf("absent ProbeExactVersion probe=%+v err=%v", probe, err)
		}
		if err := adapter.DeleteExactVersion(context.Background(), owned); err != nil {
			t.Fatalf("absent DeleteExactVersion err=%v", err)
		}
		if client.deleteInput != nil {
			t.Fatalf("absent DeleteExactVersion still called DeleteObject: %+v", client.deleteInput)
		}
	})

	t.Run("owned_prefix_worm", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headOutput: &s3.HeadObjectOutput{ObjectLockMode: s3types.ObjectLockModeCompliance}}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err != nil || !probe.Present || !probe.Locked {
			t.Fatalf("WORM ProbeExactVersion probe=%+v err=%v", probe, err)
		}
		if err := adapter.DeleteExactVersion(context.Background(), owned); !errors.Is(err, ErrDeletePointWORM) {
			t.Fatalf("WORM DeleteExactVersion err=%v, want ErrDeletePointWORM", err)
		}
		if client.deleteInput != nil {
			t.Fatalf("WORM DeleteExactVersion called DeleteObject: %+v", client.deleteInput)
		}
	})

	t.Run("owned_prefix_delete", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{
			headOutput:   &s3.HeadObjectOutput{},
			deleteOutput: &s3.DeleteObjectOutput{VersionId: aws.String("v-owned-1")},
		}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		if err := adapter.DeleteExactVersion(context.Background(), owned); err != nil {
			t.Fatalf("owned DeleteExactVersion: %v", err)
		}
		if client.deleteInput == nil || aws.ToString(client.deleteInput.Key) != owned.PhysicalKey ||
			aws.ToString(client.deleteInput.VersionId) != owned.VersionID {
			t.Fatalf("owned DeleteObject input=%+v", client.deleteInput)
		}
	})
}

func TestRcloneNativeDeleteExactVersionMapsOnlyExplicitWORNErrors(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	version := RcloneNativeExactVersion{PhysicalKey: profile.ManagedPrefix + "data/file.bin", VersionID: "v-delete-error-1"}
	for _, test := range []struct {
		name     string
		code     string
		wantWORM bool
	}{
		{name: "generic access denied is retryable", code: "AccessDenied"},
		{name: "explicit object lock remains WORM", code: "ObjectLocked", wantWORM: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &rcloneNativeS3ClientFake{
				headOutput:  &s3.HeadObjectOutput{},
				deleteError: &smithy.GenericAPIError{Code: test.code},
			}
			adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
			err := adapter.DeleteExactVersion(context.Background(), version)
			if test.wantWORM {
				if !errors.Is(err, ErrDeletePointWORM) {
					t.Fatalf("DeleteExactVersion error=%v, want ErrDeletePointWORM", err)
				}
				return
			}
			if err == nil || errors.Is(err, ErrDeletePointWORM) {
				t.Fatalf("DeleteExactVersion error=%v, want retryable non-WORM error", err)
			}
			if reason := rcloneNativeReason(err); reason != backupasset.RcloneReasonProviderUnavailable {
				t.Fatalf("DeleteExactVersion error=%v reason=%q, want provider_unavailable", err, reason)
			}
		})
	}
}

func TestRcloneNativeProbeExactVersionExpiredRetainUntilIsUnlocked(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	owned := RcloneNativeExactVersion{PhysicalKey: profile.ManagedPrefix + "data/file.bin", VersionID: "v-expired-worm-1"}
	expired := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)

	t.Run("expired_retain_until_without_legal_hold_is_unlocked", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headOutput: &s3.HeadObjectOutput{
			ObjectLockMode:            s3types.ObjectLockModeCompliance,
			ObjectLockRetainUntilDate: aws.Time(expired),
		}}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err != nil || !probe.Present || probe.Locked {
			t.Fatalf("expired retain-until probe=%+v err=%v, want present unlocked", probe, err)
		}
		if err := adapter.DeleteExactVersion(context.Background(), owned); err != nil {
			t.Fatalf("expired retain-until DeleteExactVersion: %v", err)
		}
	})

	t.Run("missing_retain_until_with_mode_stays_locked", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headOutput: &s3.HeadObjectOutput{
			ObjectLockMode: s3types.ObjectLockModeCompliance,
		}}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err != nil || !probe.Present || !probe.Locked {
			t.Fatalf("missing retain-until probe=%+v err=%v, want locked", probe, err)
		}
	})

	t.Run("future_retain_until_stays_locked", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headOutput: &s3.HeadObjectOutput{
			ObjectLockMode:            s3types.ObjectLockModeGovernance,
			ObjectLockRetainUntilDate: aws.Time(future),
		}}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err != nil || !probe.Present || !probe.Locked {
			t.Fatalf("future retain-until probe=%+v err=%v, want locked", probe, err)
		}
	})

	t.Run("legal_hold_stays_locked_after_retain_until_expires", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headOutput: &s3.HeadObjectOutput{
			ObjectLockLegalHoldStatus: s3types.ObjectLockLegalHoldStatusOn,
			ObjectLockMode:            s3types.ObjectLockModeCompliance,
			ObjectLockRetainUntilDate: aws.Time(expired),
		}}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err != nil || !probe.Present || !probe.Locked {
			t.Fatalf("legal hold probe=%+v err=%v, want locked", probe, err)
		}
	})
}

func TestRcloneNativeProbeExactVersionTreats405DeleteMarkerAsPresentUnlocked(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	owned := RcloneNativeExactVersion{PhysicalKey: profile.ManagedPrefix + "data/file.bin", VersionID: "v-delete-marker-1"}

	t.Run("405_delete_marker_present_unlocked", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headError: rcloneNativeHeadMethodNotAllowedError("true")}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err != nil || !probe.Present || probe.Locked {
			t.Fatalf("delete-marker ProbeExactVersion probe=%+v err=%v, want present unlocked", probe, err)
		}
		if rcloneNativeReason(err) == backupasset.RcloneReasonProviderUnavailable {
			t.Fatal("delete-marker Head 405 mapped to provider unavailable")
		}
		if err := adapter.DeleteExactVersion(context.Background(), owned); err != nil {
			t.Fatalf("delete-marker DeleteExactVersion: %v", err)
		}
		if client.deleteInput == nil || aws.ToString(client.deleteInput.VersionId) != owned.VersionID {
			t.Fatalf("delete-marker DeleteObject version=%q", aws.ToString(client.deleteInput.VersionId))
		}
	})

	t.Run("405_without_delete_marker_stays_unavailable", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headError: rcloneNativeHeadMethodNotAllowedError("")}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err == nil || probe.Present || rcloneNativeReason(err) != backupasset.RcloneReasonProviderUnavailable {
			t.Fatalf("bare 405 probe=%+v err=%v reason=%q, want unavailable", probe, err, rcloneNativeReason(err))
		}
		if err := adapter.DeleteExactVersion(context.Background(), owned); rcloneNativeReason(err) != backupasset.RcloneReasonProviderUnavailable {
			t.Fatalf("bare 405 DeleteExactVersion err=%v, want unavailable", err)
		}
		if client.deleteInput != nil {
			t.Fatalf("bare 405 still called DeleteObject: %+v", client.deleteInput)
		}
	})

	t.Run("405_delete_marker_false_stays_unavailable", func(t *testing.T) {
		client := &rcloneNativeS3ClientFake{headError: rcloneNativeHeadMethodNotAllowedError("false")}
		adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
		probe, err := adapter.ProbeExactVersion(context.Background(), owned)
		if err == nil || probe.Present || rcloneNativeReason(err) != backupasset.RcloneReasonProviderUnavailable {
			t.Fatalf("delete-marker=false probe=%+v err=%v reason=%q, want unavailable", probe, err, rcloneNativeReason(err))
		}
	})
}

func rcloneNativeHeadMethodNotAllowedError(deleteMarker string) error {
	header := make(http.Header)
	if deleteMarker != "" {
		header.Set("x-amz-delete-marker", deleteMarker)
	}
	return &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusMethodNotAllowed, Header: header}},
		Err:      &smithy.GenericAPIError{Code: "MethodNotAllowed", Message: "Method Not Allowed"},
	}
}

func TestRcloneNativeAWSSDKDeletesOnlyCurrentCanaryAndReturnsDeleteMarkerVersion(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	client := &rcloneNativeS3ClientFake{deleteOutput: &s3.DeleteObjectOutput{
		DeleteMarker: aws.Bool(true), VersionId: aws.String("opaque-delete-marker-v1"),
	}}
	adapter := newRcloneNativeS3SDK(client, "123456789012", profile, nil)
	result, err := adapter.DeleteCurrentCanary(context.Background(), RcloneNativeCurrentDeleteRequest{
		Profile: profile, PhysicalKey: profile.ManagedPrefix + "control/preflight/canary.bin",
	})
	if err != nil || result.VersionID != "opaque-delete-marker-v1" {
		t.Fatalf("delete current canary=%+v err=%v", result, err)
	}
	if client.deleteInput == nil || aws.ToString(client.deleteInput.Bucket) != profile.Bucket ||
		aws.ToString(client.deleteInput.Key) != profile.ManagedPrefix+"control/preflight/canary.bin" ||
		aws.ToString(client.deleteInput.ExpectedBucketOwner) != "123456789012" {
		t.Fatalf("DeleteObject input=%+v", client.deleteInput)
	}
}

var _ rcloneNativeSTSAPI = (*rcloneNativeSTSClientFake)(nil)
var _ rcloneNativeS3API = (*rcloneNativeS3ClientFake)(nil)
var _ rcloneNativeKMSAPI = (*rcloneNativeKMSClientFake)(nil)
