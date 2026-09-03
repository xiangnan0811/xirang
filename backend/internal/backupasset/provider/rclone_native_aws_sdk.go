package provider

// AWS SDK request/response types are confined to this adapter. Callers use
// only provider-owned contracts from rclone_native.go.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"xirang/backend/internal/backupasset"
)

type RcloneNativeBootstrapKind string

const (
	RcloneNativeBootstrapWorkloadChain RcloneNativeBootstrapKind = "workload_chain"
	RcloneNativeBootstrapStaticSTS     RcloneNativeBootstrapKind = "static_sts_bootstrap"
)

type RcloneNativeBootstrap struct {
	Kind            RcloneNativeBootstrapKind
	AccessKeyID     string `json:"-"`
	SecretAccessKey string `json:"-"`
}

type rcloneNativeSTSAPI interface {
	AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

type rcloneNativeS3API interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	GetBucketLocation(context.Context, *s3.GetBucketLocationInput, ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error)
	GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
	GetBucketLifecycleConfiguration(context.Context, *s3.GetBucketLifecycleConfigurationInput, ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error)
	GetBucketEncryption(context.Context, *s3.GetBucketEncryptionInput, ...func(*s3.Options)) (*s3.GetBucketEncryptionOutput, error)
}

type rcloneNativeKMSAPI interface {
	DescribeKey(context.Context, *kms.DescribeKeyInput, ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
}

type RcloneNativeAWSFactory struct {
	baseConfig  aws.Config
	sts         rcloneNativeSTSAPI
	bootstrapS3 rcloneNativeS3API
	maxAttempts int
}

func NewRcloneNativeAWSFactory(ctx context.Context, bootstrap RcloneNativeBootstrap, region string, maxAttempts int) (*RcloneNativeAWSFactory, error) {
	if ctx == nil || !validRcloneNativeRegion(region) || maxAttempts < 1 || maxAttempts > 10 {
		return nil, fmt.Errorf("%w: invalid Rclone native AWS factory configuration", backupasset.ErrInvalidState)
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region), awsconfig.WithRetryMaxAttempts(maxAttempts),
	}
	switch bootstrap.Kind {
	case RcloneNativeBootstrapWorkloadChain:
		if bootstrap.AccessKeyID != "" || bootstrap.SecretAccessKey != "" {
			return nil, fmt.Errorf("%w: workload bootstrap contains static credentials", backupasset.ErrInvalidState)
		}
	case RcloneNativeBootstrapStaticSTS:
		if !validRcloneNativeSecret(bootstrap.AccessKeyID, 16, 256) || !validRcloneNativeSecret(bootstrap.SecretAccessKey, 16, 4096) {
			return nil, fmt.Errorf("%w: invalid static STS bootstrap", backupasset.ErrInvalidState)
		}
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(bootstrap.AccessKeyID, bootstrap.SecretAccessKey, ""),
		))
	default:
		return nil, fmt.Errorf("%w: unsupported Rclone native bootstrap", backupasset.ErrInvalidState)
	}
	configuration, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load official AWS configuration: %w", err)
	}
	return &RcloneNativeAWSFactory{
		baseConfig:  configuration,
		sts:         sts.NewFromConfig(configuration),
		bootstrapS3: s3.NewFromConfig(configuration, func(options *s3.Options) { options.UsePathStyle = false }),
		maxAttempts: maxAttempts,
	}, nil
}

func newRcloneNativeAWSFactoryForTest(stsClient rcloneNativeSTSAPI, bootstrapS3 rcloneNativeS3API, configuration aws.Config, maxAttempts int) *RcloneNativeAWSFactory {
	return &RcloneNativeAWSFactory{baseConfig: configuration, sts: stsClient, bootstrapS3: bootstrapS3, maxAttempts: maxAttempts}
}

func (factory *RcloneNativeAWSFactory) BootstrapCredentialsExpire(ctx context.Context) (bool, error) {
	if factory == nil || ctx == nil || factory.baseConfig.Credentials == nil {
		return false, fmt.Errorf("%w: invalid AWS bootstrap credential request", backupasset.ErrInvalidState)
	}
	credentials, err := factory.baseConfig.Credentials.Retrieve(ctx)
	if err != nil {
		return false, fmt.Errorf("retrieve AWS bootstrap credentials: %w", err)
	}
	return credentials.CanExpire, nil
}

func (factory *RcloneNativeAWSFactory) AssumeRole(ctx context.Context, request RcloneNativeAssumeRoleRequest) (RcloneNativeAssumeRoleResult, error) {
	if factory == nil || factory.sts == nil || ctx == nil || len(request.SessionPolicy) == 0 || len(request.SessionPolicy) > rcloneNativePolicyMaxBytes ||
		request.SessionName == "" || request.Duration < 15*time.Minute || request.Duration > 12*time.Hour {
		return RcloneNativeAssumeRoleResult{}, fmt.Errorf("%w: invalid AWS AssumeRole request", backupasset.ErrInvalidState)
	}
	accountID, ok := parseRcloneNativeRoleARN(request.RoleARN)
	if !ok || (request.ExternalID != nil && !validRcloneNativeExternalID(*request.ExternalID)) {
		return RcloneNativeAssumeRoleResult{}, fmt.Errorf("%w: invalid AWS AssumeRole identity", backupasset.ErrInvalidState)
	}
	durationSeconds := int32((request.Duration + time.Second - 1) / time.Second)
	input := &sts.AssumeRoleInput{
		RoleArn: aws.String(request.RoleARN), RoleSessionName: aws.String(request.SessionName),
		DurationSeconds: aws.Int32(durationSeconds), Policy: aws.String(request.SessionPolicy),
	}
	if request.ExternalID != nil {
		input.ExternalId = aws.String(*request.ExternalID)
	}
	output, err := factory.sts.AssumeRole(ctx, input)
	if err != nil {
		if rcloneNativeAWSErrorCode(err, "AccessDenied", "AccessDeniedException") {
			return RcloneNativeAssumeRoleResult{}, ErrRcloneNativeAssumeRoleDenied
		}
		return RcloneNativeAssumeRoleResult{}, fmt.Errorf("AWS STS AssumeRole failed: %w", err)
	}
	if output == nil || output.Credentials == nil || output.AssumedRoleUser == nil || output.Credentials.Expiration == nil {
		return RcloneNativeAssumeRoleResult{}, fmt.Errorf("%w: incomplete AWS STS response", backupasset.ErrInvalidState)
	}
	expiresAt := output.Credentials.Expiration.UTC()
	identityDigest, err := canonicalRcloneNativeDigest("assumed-role-session-v1", struct {
		AccountID string    `json:"account_id"`
		RoleARN   string    `json:"role_arn"`
		AccessKey string    `json:"access_key"`
		ExpiresAt time.Time `json:"expires_at"`
	}{
		AccountID: accountID, RoleARN: aws.ToString(output.AssumedRoleUser.Arn),
		AccessKey: aws.ToString(output.Credentials.AccessKeyId), ExpiresAt: expiresAt,
	})
	if err != nil {
		return RcloneNativeAssumeRoleResult{}, fmt.Errorf("derive assumed-role identity: %w", err)
	}
	session := newRcloneNativeSession(
		aws.ToString(output.Credentials.AccessKeyId), aws.ToString(output.Credentials.SecretAccessKey),
		aws.ToString(output.Credentials.SessionToken), accountID, identityDigest, expiresAt,
	)
	if !session.valid() {
		return RcloneNativeAssumeRoleResult{}, fmt.Errorf("%w: invalid AWS temporary session", backupasset.ErrInvalidState)
	}
	packedPolicySize := 0
	if output.PackedPolicySize != nil {
		packedPolicySize = int(*output.PackedPolicySize)
	}
	return RcloneNativeAssumeRoleResult{Session: session, PackedPolicySize: packedPolicySize}, nil
}

func (factory *RcloneNativeAWSFactory) Probe(ctx context.Context, request RcloneNativeDenyProbeRequest) (RcloneNativeDenyProbeResult, error) {
	if factory == nil || factory.bootstrapS3 == nil || ctx == nil || ValidateRcloneNativeProfile(request.Profile) != nil ||
		!validRcloneNativeAccountID(request.ExpectedAccountID) {
		return RcloneNativeDenyProbeResult{}, fmt.Errorf("%w: invalid bootstrap deny probe", backupasset.ErrInvalidState)
	}
	_, err := factory.bootstrapS3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:              aws.String(request.Profile.Bucket),
		Key:                 aws.String(request.Profile.ManagedPrefix + "control/bootstrap-deny-probe"),
		ExpectedBucketOwner: aws.String(request.ExpectedAccountID),
	})
	if err == nil {
		return RcloneNativeDenyProbeResult{Denied: false}, nil
	}
	if rcloneNativeAWSErrorCode(err, "AccessDenied", "AccessDeniedException", "Forbidden", "403") {
		return RcloneNativeDenyProbeResult{Denied: true}, nil
	}
	if rcloneNativeAWSErrorCode(err, "NotFound", "NoSuchKey", "404") {
		return RcloneNativeDenyProbeResult{Denied: false}, nil
	}
	return RcloneNativeDenyProbeResult{}, fmt.Errorf("AWS bootstrap deny probe failed: %w", err)
}

func (factory *RcloneNativeAWSFactory) S3(session RcloneNativeSession, profile RcloneNativeProfile, keyDigests []RcloneNativeKMSKeyDigestBinding) (S3Native, error) {
	if factory == nil || !session.valid() || ValidateRcloneNativeProfile(profile) != nil || profile.Region != factory.baseConfig.Region {
		return nil, fmt.Errorf("%w: invalid assumed-role S3 client request", backupasset.ErrInvalidState)
	}
	configuration := factory.assumedRoleConfig(session, profile.Region)
	client := s3.NewFromConfig(configuration, func(options *s3.Options) { options.UsePathStyle = false })
	adapter := newRcloneNativeS3SDK(client, session.AccountID(), profile, keyDigests)
	if err := adapter.validateConfiguration(); err != nil {
		return nil, err
	}
	return adapter, nil
}

func (factory *RcloneNativeAWSFactory) BaselineS3(session RcloneNativeSession, profile RcloneNativeProfile, sourcePrefixes []string) (RcloneNativeBaselineS3, error) {
	if factory == nil || !session.valid() || ValidateRcloneNativeProfile(profile) != nil || profile.Region != factory.baseConfig.Region {
		return nil, fmt.Errorf("%w: invalid assumed-role baseline S3 client request", backupasset.ErrInvalidState)
	}
	configuration := factory.assumedRoleConfig(session, profile.Region)
	client := s3.NewFromConfig(configuration, func(options *s3.Options) { options.UsePathStyle = false })
	adapter := newRcloneNativeS3SDK(client, session.AccountID(), profile, nil, sourcePrefixes...)
	if err := adapter.validateConfiguration(); err != nil {
		return nil, err
	}
	return adapter, nil
}

func (factory *RcloneNativeAWSFactory) KMS(session RcloneNativeSession, region string) (KMSKeyInspector, error) {
	if factory == nil || !session.valid() || !validRcloneNativeRegion(region) || region != factory.baseConfig.Region {
		return nil, fmt.Errorf("%w: invalid assumed-role KMS client request", backupasset.ErrInvalidState)
	}
	return newRcloneNativeKMSSDK(kms.NewFromConfig(factory.assumedRoleConfig(session, region))), nil
}

func (factory *RcloneNativeAWSFactory) assumedRoleConfig(session RcloneNativeSession, region string) aws.Config {
	configuration := factory.baseConfig.Copy()
	configuration.Region = region
	configuration.Credentials = aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
		session.accessKeyID, session.secretAccessKey, session.sessionToken,
	))
	return configuration
}

type rcloneNativeS3SDK struct {
	client            rcloneNativeS3API
	expectedAccountID string
	profile           RcloneNativeProfile
	keyDigests        map[string]string
	baselinePrefixes  []string
	configurationErr  error
}

func newRcloneNativeS3SDK(client rcloneNativeS3API, expectedAccountID string, profile RcloneNativeProfile, bindings []RcloneNativeKMSKeyDigestBinding, baselinePrefixes ...string) *rcloneNativeS3SDK {
	adapter := &rcloneNativeS3SDK{
		client: client, expectedAccountID: expectedAccountID, profile: profile,
		keyDigests: make(map[string]string, len(bindings)), baselinePrefixes: append([]string(nil), baselinePrefixes...),
	}
	sort.Strings(adapter.baselinePrefixes)
	for index, prefix := range adapter.baselinePrefixes {
		if !validRcloneNativePrefix(prefix) || rcloneNativePrefixesOverlap(profile.ManagedPrefix, prefix) ||
			(index > 0 && adapter.baselinePrefixes[index-1] == prefix) {
			adapter.configurationErr = fmt.Errorf("%w: invalid native S3 baseline prefix", backupasset.ErrInvalidState)
		}
	}
	for _, binding := range bindings {
		region, accountID, ok := parseRcloneNativeKMSKeyARN(binding.KeyARN)
		if !ok || region != profile.Region || accountID != expectedAccountID || !validRcloneNativeDigest(binding.Digest) {
			adapter.configurationErr = fmt.Errorf("%w: invalid native S3 KMS digest binding", backupasset.ErrInvalidState)
			continue
		}
		if _, exists := adapter.keyDigests[binding.KeyARN]; exists {
			adapter.configurationErr = fmt.Errorf("%w: duplicate native S3 KMS digest binding", backupasset.ErrInvalidState)
			continue
		}
		adapter.keyDigests[binding.KeyARN] = binding.Digest
	}
	return adapter
}

func (adapter *rcloneNativeS3SDK) BucketIdentity(ctx context.Context, profile RcloneNativeProfile) (RcloneNativeBucketIdentity, error) {
	if err := adapter.validate(ctx, profile); err != nil {
		return RcloneNativeBucketIdentity{}, err
	}
	output, err := adapter.client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{
		Bucket: aws.String(profile.Bucket), ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil || output == nil {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return RcloneNativeBucketIdentity{}, fmt.Errorf("read AWS S3 bucket identity: %w", err)
	}
	region := string(output.LocationConstraint)
	switch region {
	case "":
		region = "us-east-1"
	case "EU":
		region = "eu-west-1"
	}
	if region != profile.Region {
		return RcloneNativeBucketIdentity{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}
	return RcloneNativeBucketIdentity{AccountID: adapter.expectedAccountID, Region: region, Kind: RcloneNativeBucketGeneralPurpose}, nil
}

func (adapter *rcloneNativeS3SDK) GetVersioning(ctx context.Context, profile RcloneNativeProfile) (RcloneNativeVersioningObservation, error) {
	if err := adapter.validate(ctx, profile); err != nil {
		return RcloneNativeVersioningObservation{}, err
	}
	output, err := adapter.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(profile.Bucket), ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil || output == nil {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return RcloneNativeVersioningObservation{}, fmt.Errorf("read AWS S3 versioning: %w", err)
	}
	return RcloneNativeVersioningObservation{Status: string(output.Status), MFADelete: string(output.MFADelete)}, nil
}

func (adapter *rcloneNativeS3SDK) GetLifecycle(ctx context.Context, profile RcloneNativeProfile) (RcloneNativeLifecycleObservation, error) {
	if err := adapter.validate(ctx, profile); err != nil {
		return RcloneNativeLifecycleObservation{}, err
	}
	output, err := adapter.client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{
		Bucket: aws.String(profile.Bucket), ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil {
		if rcloneNativeAWSErrorCode(err, "NoSuchLifecycleConfiguration", "NoSuchLifecycle") {
			return RcloneNativeLifecycleObservation{}, nil
		}
		return RcloneNativeLifecycleObservation{}, fmt.Errorf("read AWS S3 lifecycle: %w", err)
	}
	if output == nil {
		return RcloneNativeLifecycleObservation{}, fmt.Errorf("%w: empty AWS S3 lifecycle response", backupasset.ErrInvalidState)
	}
	rules := make([]RcloneNativeLifecycleRule, 0, len(output.Rules))
	for _, raw := range output.Rules {
		rules = append(rules, mapRcloneNativeLifecycleRule(raw))
	}
	return RcloneNativeLifecycleObservation{Rules: rules}, nil
}

func (adapter *rcloneNativeS3SDK) GetEncryption(ctx context.Context, profile RcloneNativeProfile) (RcloneNativeBucketEncryption, error) {
	if err := adapter.validate(ctx, profile); err != nil {
		return RcloneNativeBucketEncryption{}, err
	}
	output, err := adapter.client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{
		Bucket: aws.String(profile.Bucket), ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil || output == nil || output.ServerSideEncryptionConfiguration == nil || len(output.ServerSideEncryptionConfiguration.Rules) != 1 {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return RcloneNativeBucketEncryption{}, fmt.Errorf("read AWS S3 encryption: %w", err)
	}
	rule := output.ServerSideEncryptionConfiguration.Rules[0]
	if rule.ApplyServerSideEncryptionByDefault == nil {
		return RcloneNativeBucketEncryption{}, fmt.Errorf("%w: incomplete AWS S3 encryption response", backupasset.ErrInvalidState)
	}
	blockedKnown := rule.BlockedEncryptionTypes != nil
	if blockedKnown {
		for _, encryptionType := range rule.BlockedEncryptionTypes.EncryptionType {
			if encryptionType != s3types.EncryptionTypeNone && encryptionType != s3types.EncryptionTypeSseC {
				blockedKnown = false
			}
		}
	}
	return RcloneNativeBucketEncryption{
		Algorithm:        string(rule.ApplyServerSideEncryptionByDefault.SSEAlgorithm),
		KMSKeyARN:        aws.ToString(rule.ApplyServerSideEncryptionByDefault.KMSMasterKeyID),
		BucketKeyEnabled: aws.ToBool(rule.BucketKeyEnabled), BlockedEncryptionTypesKnown: blockedKnown,
	}, nil
}

func (adapter *rcloneNativeS3SDK) ListVersionPage(ctx context.Context, request RcloneNativeVersionPageRequest) (RcloneNativeVersionPage, error) {
	if err := adapter.validateBoundRequest(ctx); err != nil {
		return RcloneNativeVersionPage{}, err
	}
	if !validRcloneNativePrefix(request.Prefix) || !adapter.allowsReadPrefix(request.Prefix) || request.MaxKeys <= 0 || request.MaxKeys > 1000 ||
		(request.KeyMarker == "") != (request.VersionIDMarker == "") ||
		(request.KeyMarker != "" && (!validRcloneNativePhysicalKey(request.KeyMarker) || !strings.HasPrefix(request.KeyMarker, request.Prefix) || !validRcloneNativeVersionID(request.VersionIDMarker))) {
		return RcloneNativeVersionPage{}, fmt.Errorf("%w: invalid AWS S3 version page request", backupasset.ErrInvalidState)
	}
	input := &s3.ListObjectVersionsInput{
		Bucket: aws.String(adapter.profile.Bucket), Prefix: aws.String(request.Prefix), MaxKeys: aws.Int32(int32(request.MaxKeys)),
		ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	}
	if request.KeyMarker != "" {
		input.KeyMarker = aws.String(request.KeyMarker)
		input.VersionIdMarker = aws.String(request.VersionIDMarker)
	}
	output, err := adapter.client.ListObjectVersions(ctx, input)
	if err != nil || output == nil {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return RcloneNativeVersionPage{}, fmt.Errorf("list AWS S3 object versions: %w", err)
	}
	page := RcloneNativeVersionPage{
		Records:   make([]RcloneNativeVersionRecord, 0, len(output.Versions)+len(output.DeleteMarkers)),
		Truncated: aws.ToBool(output.IsTruncated), NextKeyMarker: aws.ToString(output.NextKeyMarker),
		NextVersionIDMarker: aws.ToString(output.NextVersionIdMarker),
	}
	for _, version := range output.Versions {
		if version.LastModified == nil || version.Size == nil || aws.ToInt64(version.Size) < 0 {
			return RcloneNativeVersionPage{}, fmt.Errorf("%w: incomplete AWS S3 object version", backupasset.ErrInvalidState)
		}
		record := RcloneNativeVersionRecord{
			PhysicalKey: aws.ToString(version.Key), VersionID: aws.ToString(version.VersionId), Kind: RcloneNativeObjectVersion,
			IsLatest: aws.ToBool(version.IsLatest), Size: uint64(aws.ToInt64(version.Size)), LastModified: version.LastModified.UTC(),
		}
		if !validRcloneNativeVersionRecord(record) {
			return RcloneNativeVersionPage{}, fmt.Errorf("%w: invalid AWS S3 object version", backupasset.ErrInvalidState)
		}
		page.Records = append(page.Records, record)
	}
	for _, marker := range output.DeleteMarkers {
		if marker.LastModified == nil {
			return RcloneNativeVersionPage{}, fmt.Errorf("%w: incomplete AWS S3 delete marker", backupasset.ErrInvalidState)
		}
		record := RcloneNativeVersionRecord{
			PhysicalKey: aws.ToString(marker.Key), VersionID: aws.ToString(marker.VersionId), Kind: RcloneNativeDeleteMarker,
			IsLatest: aws.ToBool(marker.IsLatest), LastModified: marker.LastModified.UTC(),
		}
		if !validRcloneNativeVersionRecord(record) {
			return RcloneNativeVersionPage{}, fmt.Errorf("%w: invalid AWS S3 delete marker", backupasset.ErrInvalidState)
		}
		page.Records = append(page.Records, record)
	}
	return page, nil
}

func (adapter *rcloneNativeS3SDK) HeadVersion(ctx context.Context, request RcloneNativeExactReadRequest) (RcloneNativeExactObjectHead, error) {
	if err := adapter.validateExactRead(ctx, request); err != nil {
		return RcloneNativeExactObjectHead{}, err
	}
	output, err := adapter.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(adapter.profile.Bucket), Key: aws.String(request.PhysicalKey), VersionId: aws.String(request.VersionID),
		ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil || output == nil || output.ContentLength == nil || aws.ToInt64(output.ContentLength) < 0 || aws.ToString(output.VersionId) != request.VersionID {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return RcloneNativeExactObjectHead{}, fmt.Errorf("head exact AWS S3 object version: %w", err)
	}
	head := RcloneNativeExactObjectHead{
		PhysicalKey: request.PhysicalKey, VersionID: request.VersionID, Size: uint64(aws.ToInt64(output.ContentLength)),
		BucketKeyEnabled: aws.ToBool(output.BucketKeyEnabled),
	}
	switch output.ServerSideEncryption {
	case s3types.ServerSideEncryptionAes256:
		if aws.ToString(output.SSEKMSKeyId) != "" || head.BucketKeyEnabled {
			return RcloneNativeExactObjectHead{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
		}
		head.EncryptionProfile = RcloneNativeSSES3V1
	case s3types.ServerSideEncryptionAwsKms:
		keyARN := aws.ToString(output.SSEKMSKeyId)
		digest, exists := adapter.keyDigests[keyARN]
		if !exists || !validRcloneNativeDigest(digest) {
			return RcloneNativeExactObjectHead{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
		}
		head.EncryptionProfile = RcloneNativeSSEKMSV1
		head.KMSKeyDigest = digest
	default:
		return RcloneNativeExactObjectHead{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	return head, nil
}

func (adapter *rcloneNativeS3SDK) HeadBaselineVersion(ctx context.Context, request RcloneNativeExactReadRequest) (RcloneNativeBaselineObjectHead, error) {
	if err := adapter.validateBaselineExactRead(ctx, request); err != nil {
		return RcloneNativeBaselineObjectHead{}, err
	}
	output, err := adapter.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(adapter.profile.Bucket), Key: aws.String(request.PhysicalKey), VersionId: aws.String(request.VersionID),
		ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil || output == nil || output.ContentLength == nil || aws.ToInt64(output.ContentLength) < 0 || aws.ToString(output.VersionId) != request.VersionID {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return RcloneNativeBaselineObjectHead{}, fmt.Errorf("head baseline AWS S3 object version: %w", err)
	}
	head := RcloneNativeBaselineObjectHead{
		PhysicalKey: request.PhysicalKey, VersionID: request.VersionID, Size: uint64(aws.ToInt64(output.ContentLength)),
		BucketKeyEnabled: aws.ToBool(output.BucketKeyEnabled),
	}
	switch output.ServerSideEncryption {
	case s3types.ServerSideEncryptionAes256:
		if aws.ToString(output.SSEKMSKeyId) != "" || head.BucketKeyEnabled {
			return RcloneNativeBaselineObjectHead{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		head.EncryptionProfile = RcloneNativeSSES3V1
	case s3types.ServerSideEncryptionAwsKms:
		keyARN := aws.ToString(output.SSEKMSKeyId)
		region, accountID, ok := parseRcloneNativeKMSKeyARN(keyARN)
		if !ok || region != adapter.profile.Region || accountID != adapter.expectedAccountID {
			return RcloneNativeBaselineObjectHead{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		head.EncryptionProfile = RcloneNativeSSEKMSV1
		head.KMSKeyARN = keyARN
	default:
		return RcloneNativeBaselineObjectHead{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	return head, nil
}

func (adapter *rcloneNativeS3SDK) OpenVersion(ctx context.Context, request RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	return adapter.openVersion(ctx, request, "", false)
}

func (adapter *rcloneNativeS3SDK) OpenVersionRange(ctx context.Context, request RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	if request.Length == 0 || request.Offset > math.MaxUint64-request.Length {
		return nil, fmt.Errorf("%w: invalid exact AWS S3 range", backupasset.ErrInvalidState)
	}
	readRequest := RcloneNativeExactReadRequest{PhysicalKey: request.PhysicalKey, VersionID: request.VersionID}
	end := request.Offset + request.Length - 1
	return adapter.openVersion(ctx, readRequest, "bytes="+strconv.FormatUint(request.Offset, 10)+"-"+strconv.FormatUint(end, 10), false)
}

func (adapter *rcloneNativeS3SDK) OpenBaselineVersion(ctx context.Context, request RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	return adapter.openVersion(ctx, request, "", true)
}

func (adapter *rcloneNativeS3SDK) PutControlVersion(ctx context.Context, request RcloneNativeControlWriteRequest) (RcloneNativeControlWriteResult, error) {
	if err := adapter.validateBoundRequest(ctx); err != nil {
		return RcloneNativeControlWriteResult{}, err
	}
	relativeKey := strings.TrimPrefix(request.PhysicalKey, adapter.profile.ManagedPrefix)
	if !validRcloneNativePhysicalKey(request.PhysicalKey) || relativeKey == request.PhysicalKey ||
		!strings.Contains("/"+relativeKey, "/control/") || len(request.Payload) == 0 || request.MaxBytes == 0 ||
		request.MaxBytes > math.MaxInt64 || uint64(len(request.Payload)) > request.MaxBytes {
		return RcloneNativeControlWriteResult{}, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(adapter.profile.Bucket), Key: aws.String(request.PhysicalKey),
		Body: bytes.NewReader(append([]byte(nil), request.Payload...)), ContentLength: aws.Int64(int64(len(request.Payload))),
		ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	}
	switch request.EncryptionProfile {
	case RcloneNativeSSES3V1:
		if request.KMSKeyARN != "" || request.KMSKeyDigest != "" || request.BucketKeyEnabled {
			return RcloneNativeControlWriteResult{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		input.ServerSideEncryption = s3types.ServerSideEncryptionAes256
	case RcloneNativeSSEKMSV1:
		region, accountID, ok := parseRcloneNativeKMSKeyARN(request.KMSKeyARN)
		digest, exists := adapter.keyDigests[request.KMSKeyARN]
		if !ok || region != adapter.profile.Region || accountID != adapter.expectedAccountID || !exists || digest != request.KMSKeyDigest {
			return RcloneNativeControlWriteResult{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
		}
		input.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
		input.SSEKMSKeyId = aws.String(request.KMSKeyARN)
		input.BucketKeyEnabled = aws.Bool(request.BucketKeyEnabled)
	default:
		return RcloneNativeControlWriteResult{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	output, err := adapter.client.PutObject(ctx, input)
	if err != nil || output == nil || !validRcloneNativeVersionID(aws.ToString(output.VersionId)) {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return RcloneNativeControlWriteResult{}, fmt.Errorf("write versioned AWS S3 control object: %w", err)
	}
	return RcloneNativeControlWriteResult{VersionID: aws.ToString(output.VersionId)}, nil
}

func (adapter *rcloneNativeS3SDK) DeleteCurrentCanary(
	ctx context.Context,
	request RcloneNativeCurrentDeleteRequest,
) (RcloneNativeCurrentDeleteResult, error) {
	if adapter == nil || adapter.client == nil || adapter.validate(ctx, request.Profile) != nil ||
		!validRcloneNativePhysicalKey(request.PhysicalKey) ||
		!strings.HasPrefix(request.PhysicalKey, request.Profile.ManagedPrefix+"control/preflight/") {
		return RcloneNativeCurrentDeleteResult{}, rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
	}
	result, err := adapter.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(request.Profile.Bucket), Key: aws.String(request.PhysicalKey),
		ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil {
		return RcloneNativeCurrentDeleteResult{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	if result == nil || !aws.ToBool(result.DeleteMarker) || !validRcloneNativeVersionID(aws.ToString(result.VersionId)) {
		return RcloneNativeCurrentDeleteResult{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
	}
	return RcloneNativeCurrentDeleteResult{VersionID: aws.ToString(result.VersionId)}, nil
}

func (adapter *rcloneNativeS3SDK) ProbeExactVersion(ctx context.Context, version RcloneNativeExactVersion) (RcloneNativeVersionProbe, error) {
	if err := adapter.validateExactVersion(ctx, version); err != nil {
		return RcloneNativeVersionProbe{}, err
	}
	output, err := adapter.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(adapter.profile.Bucket), Key: aws.String(version.PhysicalKey), VersionId: aws.String(version.VersionID),
		ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil {
		if rcloneNativeAWSErrorCode(err, "NotFound", "NoSuchKey", "404", "NoSuchVersion") {
			return RcloneNativeVersionProbe{}, nil
		}
		if rcloneNativeHeadDeleteMarkerPresent(err) {
			return RcloneNativeVersionProbe{Present: true}, nil
		}
		return RcloneNativeVersionProbe{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	if output == nil {
		return RcloneNativeVersionProbe{}, nil
	}
	locked := output.ObjectLockLegalHoldStatus == s3types.ObjectLockLegalHoldStatusOn
	if output.ObjectLockMode != "" {
		if output.ObjectLockRetainUntilDate == nil || output.ObjectLockRetainUntilDate.After(time.Now().UTC()) {
			locked = true
		}
	}
	return RcloneNativeVersionProbe{Present: true, Locked: locked}, nil
}

func (adapter *rcloneNativeS3SDK) DeleteExactVersion(ctx context.Context, version RcloneNativeExactVersion) error {
	if version.VersionID == "" {
		return invalidDeletePointRequest("unversioned current object delete is forbidden")
	}
	if err := adapter.validateExactVersion(ctx, version); err != nil {
		return err
	}
	probe, err := adapter.ProbeExactVersion(ctx, version)
	if err != nil {
		return err
	}
	if probe.Locked {
		return ErrDeletePointWORM
	}
	if !probe.Present {
		return nil
	}
	_, err = adapter.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(adapter.profile.Bucket), Key: aws.String(version.PhysicalKey), VersionId: aws.String(version.VersionID),
		ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	})
	if err != nil {
		if rcloneNativeAWSErrorCode(err, "ObjectLocked") {
			return ErrDeletePointWORM
		}
		return rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	return nil
}

func (adapter *rcloneNativeS3SDK) openVersion(ctx context.Context, request RcloneNativeExactReadRequest, byteRange string, baseline bool) (io.ReadCloser, error) {
	validationErr := adapter.validateExactRead(ctx, request)
	if baseline {
		validationErr = adapter.validateBaselineExactRead(ctx, request)
	}
	if validationErr != nil {
		return nil, validationErr
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(adapter.profile.Bucket), Key: aws.String(request.PhysicalKey), VersionId: aws.String(request.VersionID),
		ExpectedBucketOwner: aws.String(adapter.expectedAccountID),
	}
	if byteRange != "" {
		input.Range = aws.String(byteRange)
	}
	output, err := adapter.client.GetObject(ctx, input)
	if err != nil || output == nil || output.Body == nil || aws.ToString(output.VersionId) != request.VersionID {
		if output != nil && output.Body != nil {
			_ = output.Body.Close()
		}
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return nil, fmt.Errorf("open exact AWS S3 object version: %w", err)
	}
	return output.Body, nil
}

func (adapter *rcloneNativeS3SDK) validate(ctx context.Context, profile RcloneNativeProfile) error {
	if err := adapter.validateConfiguration(); err != nil || ctx == nil || ValidateRcloneNativeProfile(profile) != nil || profile != adapter.profile {
		return fmt.Errorf("%w: invalid AWS S3 native adapter request", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *rcloneNativeS3SDK) validateConfiguration() error {
	if adapter == nil || adapter.client == nil || !validRcloneNativeAccountID(adapter.expectedAccountID) ||
		ValidateRcloneNativeProfile(adapter.profile) != nil || adapter.configurationErr != nil {
		return fmt.Errorf("%w: invalid AWS S3 native adapter configuration", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *rcloneNativeS3SDK) validateBoundRequest(ctx context.Context) error {
	if err := adapter.validateConfiguration(); err != nil || ctx == nil {
		return fmt.Errorf("%w: invalid AWS S3 native adapter request", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *rcloneNativeS3SDK) validateExactVersion(ctx context.Context, version RcloneNativeExactVersion) error {
	if err := adapter.validateBoundRequest(ctx); err != nil ||
		!validRcloneNativePhysicalKey(version.PhysicalKey) || !validRcloneNativeVersionID(version.VersionID) ||
		!strings.HasPrefix(version.PhysicalKey, adapter.profile.ManagedPrefix) {
		return fmt.Errorf("%w: exact native object version is outside the managed prefix", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *rcloneNativeS3SDK) validateExactRead(ctx context.Context, request RcloneNativeExactReadRequest) error {
	if err := adapter.validateBoundRequest(ctx); err != nil ||
		!validRcloneNativeVersionIdentity(request.PhysicalKey, request.VersionID, RcloneNativeObjectVersion) ||
		!strings.HasPrefix(request.PhysicalKey, adapter.profile.ManagedPrefix) {
		return fmt.Errorf("%w: invalid exact AWS S3 object request", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *rcloneNativeS3SDK) validateBaselineExactRead(ctx context.Context, request RcloneNativeExactReadRequest) error {
	if err := adapter.validateBoundRequest(ctx); err != nil ||
		!validRcloneNativeVersionIdentity(request.PhysicalKey, request.VersionID, RcloneNativeObjectVersion) ||
		!adapter.isBaselinePrefix(request.PhysicalKey) {
		return fmt.Errorf("%w: invalid baseline AWS S3 object request", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *rcloneNativeS3SDK) allowsReadPrefix(value string) bool {
	return strings.HasPrefix(value, adapter.profile.ManagedPrefix) || adapter.isBaselinePrefix(value)
}

func (adapter *rcloneNativeS3SDK) isBaselinePrefix(value string) bool {
	for _, prefix := range adapter.baselinePrefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func mapRcloneNativeLifecycleRule(raw s3types.LifecycleRule) RcloneNativeLifecycleRule {
	legacyPrefix := rcloneNativeLegacyLifecyclePrefix(raw)
	value := RcloneNativeLifecycleRule{
		ID: aws.ToString(raw.ID), Enabled: raw.Status == s3types.ExpirationStatusEnabled,
		ExpireCurrent: raw.Expiration != nil, ExpireNoncurrent: raw.NoncurrentVersionExpiration != nil,
		UnknownAction: raw.AbortIncompleteMultipartUpload != nil,
	}
	if raw.Expiration != nil {
		value.ExpiredDeleteMarkerCleanup = aws.ToBool(raw.Expiration.ExpiredObjectDeleteMarker)
	}
	if legacyPrefix != nil {
		value.Prefix = aws.ToString(legacyPrefix)
	}
	if raw.Filter != nil {
		set := 0
		if raw.Filter.Prefix != nil {
			value.Prefix = aws.ToString(raw.Filter.Prefix)
			set++
		}
		if raw.Filter.Tag != nil {
			value.HasTagFilter = true
			set++
		}
		if raw.Filter.ObjectSizeGreaterThan != nil || raw.Filter.ObjectSizeLessThan != nil {
			value.HasTagFilter = true
			set++
		}
		if raw.Filter.And != nil {
			set++
			value.Prefix = aws.ToString(raw.Filter.And.Prefix)
			value.HasTagFilter = len(raw.Filter.And.Tags) > 0 || raw.Filter.And.ObjectSizeGreaterThan != nil || raw.Filter.And.ObjectSizeLessThan != nil
		}
		if set > 1 || legacyPrefix != nil {
			value.UnknownAction = true
		}
	}
	for _, transition := range raw.Transitions {
		value.Transitions = append(value.Transitions, string(transition.StorageClass))
	}
	for _, transition := range raw.NoncurrentVersionTransitions {
		value.Transitions = append(value.Transitions, string(transition.StorageClass))
	}
	return value
}

func rcloneNativeLegacyLifecyclePrefix(raw s3types.LifecycleRule) *string {
	//nolint:staticcheck // AWS still returns legacy top-level Prefix rules; omitting them could admit destructive lifecycle drift.
	return raw.Prefix
}

type rcloneNativeKMSSDK struct {
	client rcloneNativeKMSAPI
}

func newRcloneNativeKMSSDK(client rcloneNativeKMSAPI) *rcloneNativeKMSSDK {
	return &rcloneNativeKMSSDK{client: client}
}

func (adapter *rcloneNativeKMSSDK) DescribeKey(ctx context.Context, keyARN string) (RcloneNativeKMSKey, error) {
	if adapter == nil || adapter.client == nil || ctx == nil {
		return RcloneNativeKMSKey{}, fmt.Errorf("%w: invalid AWS KMS adapter request", backupasset.ErrInvalidState)
	}
	region, accountID, ok := parseRcloneNativeKMSKeyARN(keyARN)
	if !ok {
		return RcloneNativeKMSKey{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	output, err := adapter.client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyARN)})
	if err != nil || output == nil || output.KeyMetadata == nil {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return RcloneNativeKMSKey{}, fmt.Errorf("describe AWS KMS key: %w", err)
	}
	metadata := output.KeyMetadata
	if aws.ToString(metadata.Arn) != keyARN || aws.ToString(metadata.AWSAccountId) != accountID {
		return RcloneNativeKMSKey{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}
	return RcloneNativeKMSKey{
		ARN: keyARN, AccountID: accountID, Region: region, Manager: string(metadata.KeyManager),
		Spec: string(metadata.KeySpec), Usage: string(metadata.KeyUsage), State: string(metadata.KeyState),
		Origin: string(metadata.Origin), CustomKeyStoreID: aws.ToString(metadata.CustomKeyStoreId),
		MultiRegion: aws.ToBool(metadata.MultiRegion),
	}, nil
}

func rcloneNativeAWSErrorCode(err error, codes ...string) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	for _, code := range codes {
		if apiError.ErrorCode() == code {
			return true
		}
	}
	return false
}

func rcloneNativeHeadDeleteMarkerPresent(err error) bool {
	var httpErr *smithyhttp.ResponseError
	if !errors.As(err, &httpErr) || httpErr == nil || httpErr.Response == nil {
		return false
	}
	if httpErr.HTTPStatusCode() != http.StatusMethodNotAllowed {
		return false
	}
	return strings.EqualFold(httpErr.Response.Header.Get("x-amz-delete-marker"), "true")
}

var _ STSAssumer = (*RcloneNativeAWSFactory)(nil)
var _ BootstrapDenyProbe = (*RcloneNativeAWSFactory)(nil)
var _ RcloneNativeClientFactory = (*RcloneNativeAWSFactory)(nil)
var _ S3Native = (*rcloneNativeS3SDK)(nil)
var _ RcloneNativeCanaryS3 = (*rcloneNativeS3SDK)(nil)
var _ RcloneNativeExactVersionDeleter = (*rcloneNativeS3SDK)(nil)
var _ KMSKeyInspector = (*rcloneNativeKMSSDK)(nil)
