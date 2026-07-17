package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
)

const (
	RcloneNativeEndpointAWSRegional  = "aws_regional"
	RcloneNativeEndpointCustom       = "custom"
	RcloneNativeAddressingDNS        = "dns"
	RcloneNativeAddressingPathStyle  = "path_style"
	RcloneNativeBucketGeneralPurpose = "general_purpose"
	RcloneNativeBucketDirectory      = "directory"
	RcloneNativeBucketAccessPoint    = "access_point"

	rcloneNativeCapabilitySettle = 15 * time.Minute
	rcloneNativeRoleChainLimit   = time.Hour
	rcloneNativePolicyMaxBytes   = 2048
	rcloneNativeConfigRemote     = "xirang_native"
)

var ErrRcloneNativeAssumeRoleDenied = errors.New("rclone native AssumeRole denied")

type RcloneNativeProfile struct {
	Code           RcloneNativeProfileCode
	Region         string
	Bucket         string
	ManagedPrefix  string
	EndpointMode   string
	AddressingMode string
	BucketKind     string
	Wrapper        string
}

type rcloneNativeAdmissionError struct {
	reason backupasset.RcloneVersioningReasonCode
	cause  error
}

func (err *rcloneNativeAdmissionError) Error() string {
	if err == nil {
		return "Rclone native admission failed"
	}
	return "Rclone native admission failed: " + string(err.reason)
}

func (err *rcloneNativeAdmissionError) Unwrap() error {
	if err == nil {
		return nil
	}
	if err.cause != nil {
		return err.cause
	}
	return backupasset.ErrCapabilityUnavailable
}

func rcloneNativeError(reason backupasset.RcloneVersioningReasonCode, cause error) error {
	return &rcloneNativeAdmissionError{reason: reason, cause: cause}
}

func rcloneNativeReason(err error) backupasset.RcloneVersioningReasonCode {
	var typed *rcloneNativeAdmissionError
	if errors.As(err, &typed) {
		return typed.reason
	}
	return ""
}

func ValidateRcloneNativeProfile(value RcloneNativeProfile) error {
	if value.Code != RcloneNativeAWSS3GeneralPurposeV1 || value.EndpointMode != RcloneNativeEndpointAWSRegional ||
		value.AddressingMode != RcloneNativeAddressingDNS || value.BucketKind != RcloneNativeBucketGeneralPurpose ||
		value.Wrapper != "" || !validRcloneNativeRegion(value.Region) || !validRcloneNativeBucket(value.Bucket) ||
		!validRcloneNativePrefix(value.ManagedPrefix) {
		return rcloneNativeError(backupasset.RcloneReasonUnsupportedProfile, nil)
	}
	return nil
}

type RcloneNativeVersioningObservation struct {
	Status    string `json:"status"`
	MFADelete string `json:"mfa_delete"`
}

func CanonicalRcloneNativeVersioningDigest(value RcloneNativeVersioningObservation) (string, error) {
	if value.Status == "" || value.MFADelete == "" || strings.TrimSpace(value.Status) != value.Status || strings.TrimSpace(value.MFADelete) != value.MFADelete {
		return "", fmt.Errorf("invalid S3 versioning observation")
	}
	return canonicalRcloneNativeDigest("versioning-v1", value)
}

func ValidateRcloneNativeVersioning(value RcloneNativeVersioningObservation, previousDigest string, firstObservedAt, now time.Time) error {
	if value.Status != "Enabled" || value.MFADelete != "Disabled" {
		return rcloneNativeError(backupasset.RcloneReasonVersioningDisabled, nil)
	}
	digest, err := CanonicalRcloneNativeVersioningDigest(value)
	if err != nil {
		return rcloneNativeError(backupasset.RcloneReasonVersioningDisabled, err)
	}
	if !validRcloneNativeObservationWindow(firstObservedAt, now) || digest != previousDigest {
		return rcloneNativeError(backupasset.RcloneReasonCapabilitySettling, nil)
	}
	return nil
}

type RcloneNativeLifecycleObservation struct {
	Rules []RcloneNativeLifecycleRule `json:"rules"`
}

type RcloneNativeLifecycleRule struct {
	ID                         string   `json:"id"`
	Enabled                    bool     `json:"enabled"`
	Prefix                     string   `json:"prefix"`
	HasTagFilter               bool     `json:"has_tag_filter"`
	ExpireCurrent              bool     `json:"expire_current"`
	ExpireNoncurrent           bool     `json:"expire_noncurrent"`
	ExpiredDeleteMarkerCleanup bool     `json:"expired_delete_marker_cleanup"`
	Transitions                []string `json:"transitions"`
	UnknownAction              bool     `json:"unknown_action"`
}

func CanonicalRcloneNativeLifecycleDigest(value RcloneNativeLifecycleObservation) (string, error) {
	rules := append([]RcloneNativeLifecycleRule(nil), value.Rules...)
	for index := range rules {
		if rules[index].ID == "" || len(rules[index].ID) > 255 || !utf8.ValidString(rules[index].ID) ||
			strings.ContainsRune(rules[index].ID, '\x00') || rules[index].UnknownAction || !validRcloneNativeLifecyclePrefix(rules[index].Prefix) {
			return "", fmt.Errorf("invalid S3 lifecycle observation")
		}
		rules[index].Transitions = append([]string(nil), rules[index].Transitions...)
		sort.Strings(rules[index].Transitions)
		if slices.ContainsFunc(rules[index].Transitions, func(value string) bool { return value == "" }) {
			return "", fmt.Errorf("invalid S3 lifecycle transition")
		}
	}
	sort.Slice(rules, func(left, right int) bool { return rules[left].ID < rules[right].ID })
	for index := 1; index < len(rules); index++ {
		if rules[index-1].ID == rules[index].ID {
			return "", fmt.Errorf("duplicate S3 lifecycle rule")
		}
	}
	return canonicalRcloneNativeDigest("lifecycle-v1", RcloneNativeLifecycleObservation{Rules: rules})
}

func ValidateRcloneNativeLifecycle(value RcloneNativeLifecycleObservation, managedPrefix, previousDigest string, firstObservedAt, now time.Time) error {
	if !validRcloneNativePrefix(managedPrefix) {
		return rcloneNativeError(backupasset.RcloneReasonLifecycleConflict, nil)
	}
	for _, rule := range value.Rules {
		if !rule.Enabled {
			continue
		}
		if rule.UnknownAction || rule.ID == "" || !validRcloneNativeLifecyclePrefix(rule.Prefix) {
			return rcloneNativeError(backupasset.RcloneReasonLifecycleConflict, nil)
		}
		if !rcloneNativePrefixesOverlap(managedPrefix, rule.Prefix) {
			continue
		}
		if rule.ExpireCurrent || rule.ExpireNoncurrent || rule.ExpiredDeleteMarkerCleanup {
			return rcloneNativeError(backupasset.RcloneReasonLifecycleConflict, nil)
		}
		for _, storageClass := range rule.Transitions {
			switch storageClass {
			case "STANDARD_IA", "ONEZONE_IA", "GLACIER_IR":
			default:
				return rcloneNativeError(backupasset.RcloneReasonLifecycleConflict, nil)
			}
		}
		if rule.HasTagFilter && (rule.ExpireCurrent || rule.ExpireNoncurrent || rule.ExpiredDeleteMarkerCleanup || len(rule.Transitions) > 0) {
			return rcloneNativeError(backupasset.RcloneReasonLifecycleConflict, nil)
		}
	}
	digest, err := CanonicalRcloneNativeLifecycleDigest(value)
	if err != nil {
		return rcloneNativeError(backupasset.RcloneReasonLifecycleConflict, err)
	}
	if !validRcloneNativeObservationWindow(firstObservedAt, now) || digest != previousDigest {
		return rcloneNativeError(backupasset.RcloneReasonCapabilitySettling, nil)
	}
	return nil
}

type RcloneNativeBucketEncryption struct {
	Algorithm                   string
	KMSKeyARN                   string
	BucketKeyEnabled            bool
	BlockedEncryptionTypesKnown bool
}

type RcloneNativeEncryptionSelection struct {
	Profile             RcloneNativeEncryptionProfileCode
	ActiveKeyARN        string
	RetainedReadKeyARNs []string
}

type RcloneNativeKMSKey struct {
	ARN              string
	AccountID        string
	Region           string
	Manager          string
	Spec             string
	Usage            string
	State            string
	Origin           string
	CustomKeyStoreID string
	MultiRegion      bool
}

type RcloneNativeKMSLimits struct {
	MaxReadKeys        int
	MaxSerializedBytes int
}

type RcloneNativeEncryptionEvidence struct {
	Profile              RcloneNativeEncryptionProfileCode
	BucketKeyEnabled     bool
	ActiveKeyDigest      string
	ReadKeySetDigest     string
	RetainedReadKeyCount int
}

func RcloneNativeKMSKeyBinding(key RcloneNativeKMSKey) (RcloneNativeKMSKeyDigestBinding, error) {
	region, accountID, valid := parseRcloneNativeKMSKeyARN(key.ARN)
	if !valid || key.Region != region || key.AccountID != accountID || key.Manager != "CUSTOMER" ||
		key.Spec != "SYMMETRIC_DEFAULT" || key.Usage != "ENCRYPT_DECRYPT" || key.State != "Enabled" ||
		key.Origin != "AWS_KMS" || key.CustomKeyStoreID != "" {
		return RcloneNativeKMSKeyDigestBinding{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	digest := rcloneNativeKMSKeyDigest(key)
	if !validRcloneNativeDigest(digest) {
		return RcloneNativeKMSKeyDigestBinding{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
	}
	return RcloneNativeKMSKeyDigestBinding{KeyARN: key.ARN, Digest: digest}, nil
}

type RcloneNativeSourceKMSValidationRequest struct {
	KeyARNs       []string                         `json:"-"`
	SessionPolicy RcloneNativeSessionPolicyRequest `json:"-"`
	Limits        RcloneNativeKMSLimits
}

type RcloneNativeSourceKMSValidation struct {
	KeyBindings   []RcloneNativeKMSKeyDigestBinding `json:"-"`
	KeySetDigest  string                            `json:"key_set_digest"`
	SessionPolicy string                            `json:"-"`
}

func ValidateRcloneNativeSourceKMSKeys(
	ctx context.Context,
	inspector KMSKeyInspector,
	request RcloneNativeSourceKMSValidationRequest,
) (RcloneNativeSourceKMSValidation, error) {
	if ctx == nil || request.Limits.MaxReadKeys <= 0 || request.Limits.MaxSerializedBytes <= 0 ||
		len(request.SessionPolicy.SourceReadPrefixes) == 0 || len(request.SessionPolicy.SourceDecryptKeyARNs) != 0 {
		return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, nil)
	}
	arns := append([]string(nil), request.KeyARNs...)
	sort.Strings(arns)
	if len(arns) > request.Limits.MaxReadKeys {
		return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, nil)
	}
	serializedBytes := 0
	for index, arn := range arns {
		region, accountID, valid := parseRcloneNativeKMSKeyARN(arn)
		if !valid || region != request.SessionPolicy.Profile.Region || accountID != request.SessionPolicy.AccountID ||
			(index > 0 && arns[index-1] == arn) {
			return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		serializedBytes += len(arn) + sha256.Size*2
		if serializedBytes > request.Limits.MaxSerializedBytes {
			return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, nil)
		}
	}
	if len(arns) > 0 && inspector == nil {
		return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
	}

	bindings := make([]RcloneNativeKMSKeyDigestBinding, 0, len(arns))
	for _, arn := range arns {
		key, err := inspector.DescribeKey(ctx, arn)
		if err != nil {
			if rcloneNativeReason(err) != "" {
				return RcloneNativeSourceKMSValidation{}, err
			}
			return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonKMSPermissionDenied, err)
		}
		if key.ARN != arn || key.Region != request.SessionPolicy.Profile.Region || key.AccountID != request.SessionPolicy.AccountID ||
			key.Manager != "CUSTOMER" || key.Spec != "SYMMETRIC_DEFAULT" || key.Usage != "ENCRYPT_DECRYPT" ||
			key.Origin != "AWS_KMS" || key.CustomKeyStoreID != "" {
			return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		if key.State != "Enabled" {
			return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
		}
		digest := rcloneNativeKMSKeyDigest(key)
		if !validRcloneNativeDigest(digest) {
			return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
		}
		bindings = append(bindings, RcloneNativeKMSKeyDigestBinding{KeyARN: arn, Digest: digest})
	}
	keySetDigest, err := canonicalRcloneNativeDigest("baseline-source-kms-key-set-v1", bindings)
	if err != nil {
		return RcloneNativeSourceKMSValidation{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, err)
	}
	policyRequest := request.SessionPolicy
	policyRequest.SourceReadPrefixes = append([]string(nil), request.SessionPolicy.SourceReadPrefixes...)
	policyRequest.SourceDecryptKeyARNs = arns
	policy, err := BuildRcloneNativeSessionPolicy(policyRequest)
	if err != nil {
		return RcloneNativeSourceKMSValidation{}, err
	}
	return RcloneNativeSourceKMSValidation{
		KeyBindings: bindings, KeySetDigest: keySetDigest, SessionPolicy: policy,
	}, nil
}

func ValidateRcloneNativeEncryption(selection RcloneNativeEncryptionSelection, bucket RcloneNativeBucketEncryption, keys []RcloneNativeKMSKey, limits RcloneNativeKMSLimits) (RcloneNativeEncryptionEvidence, error) {
	if !bucket.BlockedEncryptionTypesKnown {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	effectiveBucketKeyEnabled := false
	switch bucket.Algorithm {
	case "AES256":
		if bucket.KMSKeyARN != "" || bucket.BucketKeyEnabled {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
	case "aws:kms":
		if _, _, ok := parseRcloneNativeKMSKeyARN(bucket.KMSKeyARN); !ok {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		effectiveBucketKeyEnabled = bucket.BucketKeyEnabled
	default:
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	switch selection.Profile {
	case RcloneNativeSSES3V1:
		if selection.ActiveKeyARN != "" || len(selection.RetainedReadKeyARNs) != 0 || len(keys) != 0 {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		return RcloneNativeEncryptionEvidence{Profile: RcloneNativeSSES3V1}, nil
	case RcloneNativeSSEKMSV1:
	default:
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	region, account, ok := parseRcloneNativeKMSKeyARN(selection.ActiveKeyARN)
	if !ok {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	if limits.MaxReadKeys <= 0 {
		limits.MaxReadKeys = 8
	}
	if limits.MaxSerializedBytes <= 0 {
		limits.MaxSerializedBytes = 16 << 10
	}
	if len(selection.RetainedReadKeyARNs) > limits.MaxReadKeys {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, nil)
	}
	wantARNs := append([]string{selection.ActiveKeyARN}, selection.RetainedReadKeyARNs...)
	if len(keys) != len(wantARNs) {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
	}
	keyByARN := make(map[string]RcloneNativeKMSKey, len(keys))
	for _, key := range keys {
		if _, exists := keyByARN[key.ARN]; exists {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		keyByARN[key.ARN] = key
	}
	seen := make(map[string]struct{}, len(wantARNs))
	serializedBytes := 0
	keyDigests := make([]string, 0, len(wantARNs))
	for _, arn := range wantARNs {
		if _, exists := seen[arn]; exists {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		seen[arn] = struct{}{}
		arnRegion, arnAccount, valid := parseRcloneNativeKMSKeyARN(arn)
		key, exists := keyByARN[arn]
		if !valid || !exists || arnRegion != region || arnAccount != account || key.ARN != arn || key.Region != region || key.AccountID != account ||
			key.Manager != "CUSTOMER" || key.Spec != "SYMMETRIC_DEFAULT" || key.Usage != "ENCRYPT_DECRYPT" || key.Origin != "AWS_KMS" || key.CustomKeyStoreID != "" {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		if key.State != "Enabled" {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
		}
		serializedBytes += len(arn)
		keyDigests = append(keyDigests, rcloneNativeKMSKeyDigest(key))
	}
	if serializedBytes > limits.MaxSerializedBytes {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, nil)
	}
	readDigests := append([]string(nil), keyDigests[1:]...)
	sort.Strings(readDigests)
	readSetDigest, err := canonicalRcloneNativeDigest("kms-read-key-set-v1", readDigests)
	if err != nil {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, err)
	}
	return RcloneNativeEncryptionEvidence{
		Profile: RcloneNativeSSEKMSV1, BucketKeyEnabled: effectiveBucketKeyEnabled,
		ActiveKeyDigest: keyDigests[0], ReadKeySetDigest: readSetDigest, RetainedReadKeyCount: len(readDigests),
	}, nil
}

func BuildRcloneNativeEncryptionEvidence(
	selection RcloneNativeEncryptionSelection,
	bindings []RcloneNativeKMSKeyDigestBinding,
	bucketKeyEnabled bool,
) (RcloneNativeEncryptionEvidence, error) {
	switch selection.Profile {
	case RcloneNativeSSES3V1:
		if selection.ActiveKeyARN != "" || len(selection.RetainedReadKeyARNs) != 0 || len(bindings) != 0 || bucketKeyEnabled {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		return RcloneNativeEncryptionEvidence{Profile: RcloneNativeSSES3V1}, nil
	case RcloneNativeSSEKMSV1:
	default:
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}

	region, accountID, ok := parseRcloneNativeKMSKeyARN(selection.ActiveKeyARN)
	if !ok || len(bindings) != 1+len(selection.RetainedReadKeyARNs) {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	bindingByARN := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		bindingRegion, bindingAccountID, valid := parseRcloneNativeKMSKeyARN(binding.KeyARN)
		if !valid || bindingRegion != region || bindingAccountID != accountID || !validRcloneNativeDigest(binding.Digest) {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		if _, exists := bindingByARN[binding.KeyARN]; exists {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		bindingByARN[binding.KeyARN] = binding.Digest
	}
	activeDigest, exists := bindingByARN[selection.ActiveKeyARN]
	if !exists {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	retainedDigests := make([]string, 0, len(selection.RetainedReadKeyARNs))
	seen := map[string]struct{}{selection.ActiveKeyARN: {}}
	for _, arn := range selection.RetainedReadKeyARNs {
		arnRegion, arnAccountID, valid := parseRcloneNativeKMSKeyARN(arn)
		digest, found := bindingByARN[arn]
		if !valid || arnRegion != region || arnAccountID != accountID || !found {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		if _, duplicate := seen[arn]; duplicate {
			return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		seen[arn] = struct{}{}
		retainedDigests = append(retainedDigests, digest)
	}
	sort.Strings(retainedDigests)
	readSetDigest, err := canonicalRcloneNativeDigest("kms-read-key-set-v1", retainedDigests)
	if err != nil {
		return RcloneNativeEncryptionEvidence{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, err)
	}
	return RcloneNativeEncryptionEvidence{
		Profile: RcloneNativeSSEKMSV1, BucketKeyEnabled: bucketKeyEnabled,
		ActiveKeyDigest: activeDigest, ReadKeySetDigest: readSetDigest, RetainedReadKeyCount: len(retainedDigests),
	}, nil
}

type RcloneNativeSessionPolicyRequest struct {
	Profile              RcloneNativeProfile
	AccountID            string
	Encryption           RcloneNativeEncryptionSelection
	BucketKeyEnabled     bool
	SourceReadPrefixes   []string
	SourceDecryptKeyARNs []string
}

type rcloneNativePolicyDocument struct {
	Version   string                        `json:"Version"`
	Statement []rcloneNativePolicyStatement `json:"Statement"`
}

type rcloneNativePolicyStatement struct {
	Effect    string                       `json:"Effect"`
	Action    []string                     `json:"Action"`
	Resource  []string                     `json:"Resource"`
	Condition map[string]map[string]string `json:"Condition,omitempty"`
}

func BuildRcloneNativeSessionPolicy(request RcloneNativeSessionPolicyRequest) (string, error) {
	if err := ValidateRcloneNativeProfile(request.Profile); err != nil || !validRcloneNativeAccountID(request.AccountID) {
		return "", rcloneNativeError(backupasset.RcloneReasonUnsupportedProfile, nil)
	}
	sourcePrefixes, sourceKeys, err := canonicalRcloneNativeBaselineSources(request)
	if err != nil {
		return "", err
	}
	bucketARN := "arn:aws:s3:::" + request.Profile.Bucket
	objectARN := bucketARN + "/" + request.Profile.ManagedPrefix + "*"
	accountCondition := map[string]map[string]string{"StringEquals": {"s3:ResourceAccount": request.AccountID}}
	document := rcloneNativePolicyDocument{Version: "2012-10-17", Statement: []rcloneNativePolicyStatement{
		{Effect: "Allow", Action: []string{
			"s3:GetBucketLocation", "s3:GetBucketVersioning", "s3:GetLifecycleConfiguration", "s3:GetEncryptionConfiguration",
			"s3:ListBucket", "s3:ListBucketVersions", "s3:ListBucketMultipartUploads",
		}, Resource: []string{bucketARN}, Condition: accountCondition},
		{Effect: "Allow", Action: []string{
			"s3:GetObject", "s3:GetObjectVersion", "s3:PutObject", "s3:DeleteObject", "s3:ListMultipartUploadParts", "s3:AbortMultipartUpload",
		}, Resource: []string{objectARN}, Condition: accountCondition},
	}}
	if len(sourcePrefixes) > 0 {
		sourceResources := make([]string, 0, len(sourcePrefixes))
		for _, prefix := range sourcePrefixes {
			sourceResources = append(sourceResources, bucketARN+"/"+prefix+"*")
		}
		document.Statement = append(document.Statement, rcloneNativePolicyStatement{
			Effect: "Allow", Action: []string{"s3:GetObject", "s3:GetObjectVersion"}, Resource: sourceResources, Condition: accountCondition,
		})
	}
	if request.Encryption.Profile == RcloneNativeSSEKMSV1 {
		activeRegion, activeAccount, ok := parseRcloneNativeKMSKeyARN(request.Encryption.ActiveKeyARN)
		if !ok || activeRegion != request.Profile.Region || activeAccount != request.AccountID {
			return "", rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		allKeys := append([]string{request.Encryption.ActiveKeyARN}, request.Encryption.RetainedReadKeyARNs...)
		if len(allKeys) == 0 || len(allKeys) > 33 {
			return "", rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, nil)
		}
		for _, arn := range allKeys {
			region, account, valid := parseRcloneNativeKMSKeyARN(arn)
			if !valid || region != activeRegion || account != activeAccount {
				return "", rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
			}
		}
		document.Statement = append(document.Statement,
			rcloneNativePolicyStatement{Effect: "Allow", Action: []string{"kms:DescribeKey"}, Resource: allKeys},
		)
		contextARN := objectARN
		if request.BucketKeyEnabled {
			contextARN = bucketARN
		}
		kmsCondition := map[string]map[string]string{"StringEquals": {
			"kms:ViaService": "s3." + request.Profile.Region + ".amazonaws.com",
		}}
		if request.BucketKeyEnabled {
			kmsCondition["StringEquals"]["kms:EncryptionContext:aws:s3:arn"] = contextARN
		} else {
			kmsCondition["StringLike"] = map[string]string{"kms:EncryptionContext:aws:s3:arn": contextARN}
		}
		document.Statement = append(document.Statement, rcloneNativePolicyStatement{
			Effect: "Allow", Action: []string{"kms:GenerateDataKey", "kms:Decrypt"}, Resource: []string{request.Encryption.ActiveKeyARN},
			Condition: kmsCondition,
		})
		if len(request.Encryption.RetainedReadKeyARNs) > 0 {
			document.Statement = append(document.Statement, rcloneNativePolicyStatement{
				Effect: "Allow", Action: []string{"kms:Decrypt"}, Resource: append([]string(nil), request.Encryption.RetainedReadKeyARNs...),
				Condition: kmsCondition,
			})
		}
	} else if request.Encryption.Profile != RcloneNativeSSES3V1 {
		return "", rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	if len(sourceKeys) > 0 {
		document.Statement = append(document.Statement, rcloneNativePolicyStatement{
			Effect: "Allow", Action: []string{"kms:DescribeKey"}, Resource: append([]string(nil), sourceKeys...),
		})
		for _, prefix := range sourcePrefixes {
			sourceObjectARN := bucketARN + "/" + prefix + "*"
			document.Statement = append(document.Statement, rcloneNativePolicyStatement{
				Effect: "Allow", Action: []string{"kms:Decrypt"}, Resource: append([]string(nil), sourceKeys...),
				Condition: map[string]map[string]string{"StringLike": {
					"kms:ViaService":                   "s3." + request.Profile.Region + ".amazonaws.com",
					"kms:EncryptionContext:aws:s3:arn": sourceObjectARN,
				}},
			})
		}
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, err)
	}
	if len(payload) > rcloneNativePolicyMaxBytes {
		return "", rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, nil)
	}
	return string(payload), nil
}

func canonicalRcloneNativeBaselineSources(request RcloneNativeSessionPolicyRequest) ([]string, []string, error) {
	if len(request.SourceReadPrefixes) == 0 {
		if len(request.SourceDecryptKeyARNs) != 0 {
			return nil, nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		return nil, nil, nil
	}
	if len(request.SourceReadPrefixes) > 8 || len(request.SourceDecryptKeyARNs) > 8 {
		return nil, nil, rcloneNativeError(backupasset.RcloneReasonKMSKeyRingLimit, nil)
	}
	prefixes := append([]string(nil), request.SourceReadPrefixes...)
	sort.Strings(prefixes)
	for index, prefix := range prefixes {
		if !validRcloneNativePrefix(prefix) || rcloneNativePrefixesOverlap(request.Profile.ManagedPrefix, prefix) ||
			(index > 0 && prefixes[index-1] == prefix) {
			return nil, nil, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
		}
	}
	keys := append([]string(nil), request.SourceDecryptKeyARNs...)
	sort.Strings(keys)
	for index, keyARN := range keys {
		region, accountID, valid := parseRcloneNativeKMSKeyARN(keyARN)
		if !valid || region != request.Profile.Region || accountID != request.AccountID ||
			(index > 0 && keys[index-1] == keyARN) {
			return nil, nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
	}
	return prefixes, keys, nil
}

type RcloneNativeSession struct {
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	accountID       string
	identityDigest  string
	expiresAt       time.Time
}

func newRcloneNativeSession(accessKeyID, secretAccessKey, sessionToken, accountID, identityDigest string, expiresAt time.Time) RcloneNativeSession {
	return RcloneNativeSession{
		accessKeyID: accessKeyID, secretAccessKey: secretAccessKey, sessionToken: sessionToken,
		accountID: accountID, identityDigest: identityDigest, expiresAt: expiresAt,
	}
}

func NewRcloneNativeSession(
	accessKeyID, secretAccessKey, sessionToken, accountID, identityDigest string,
	expiresAt time.Time,
) (RcloneNativeSession, error) {
	session := newRcloneNativeSession(accessKeyID, secretAccessKey, sessionToken, accountID, identityDigest, expiresAt)
	if !session.valid() {
		return RcloneNativeSession{}, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, nil)
	}
	return session, nil
}

func (value RcloneNativeSession) IdentityDigest() string { return value.identityDigest }
func (value RcloneNativeSession) AccountID() string      { return value.accountID }
func (value RcloneNativeSession) ExpiresAt() time.Time   { return value.expiresAt }

func (value RcloneNativeSession) valid() bool {
	return validRcloneNativeSecret(value.accessKeyID, 16, 256) && validRcloneNativeSecret(value.secretAccessKey, 16, 4096) &&
		validRcloneNativeSecret(value.sessionToken, 16, 16<<10) && validRcloneNativeAccountID(value.accountID) &&
		validRcloneNativeDigest(value.identityDigest) && validRcloneNativeUTCTime(value.expiresAt)
}

func BuildRcloneNativeRcloneConfig(profile RcloneNativeProfile, selection RcloneNativeEncryptionSelection, session RcloneNativeSession) ([]byte, error) {
	if err := ValidateRcloneNativeProfile(profile); err != nil || !session.valid() {
		return nil, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, nil)
	}
	lines := []string{
		"[" + rcloneNativeConfigRemote + "]",
		"type = s3",
		"provider = AWS",
		"env_auth = false",
		"access_key_id = " + session.accessKeyID,
		"secret_access_key = " + session.secretAccessKey,
		"session_token = " + session.sessionToken,
		"region = " + profile.Region,
	}
	switch selection.Profile {
	case RcloneNativeSSES3V1:
		if selection.ActiveKeyARN != "" || len(selection.RetainedReadKeyARNs) != 0 {
			return nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		lines = append(lines, "server_side_encryption = AES256")
	case RcloneNativeSSEKMSV1:
		region, account, ok := parseRcloneNativeKMSKeyARN(selection.ActiveKeyARN)
		if !ok || region != profile.Region || account != session.accountID {
			return nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		lines = append(lines, "server_side_encryption = aws:kms", "sse_kms_key_id = "+selection.ActiveKeyARN)
	default:
		return nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

type RcloneNativeAssumeRoleRequest struct {
	RoleARN       string
	ExternalID    *string
	Duration      time.Duration
	SessionPolicy string
	SessionName   string
}

type RcloneNativeAssumeRoleResult struct {
	Session          RcloneNativeSession
	PackedPolicySize int
}

type STSAssumer interface {
	AssumeRole(context.Context, RcloneNativeAssumeRoleRequest) (RcloneNativeAssumeRoleResult, error)
}

type RcloneNativeDenyProbeRequest struct {
	Profile           RcloneNativeProfile
	ExpectedAccountID string
}

type RcloneNativeDenyProbeResult struct {
	Denied bool
}

type BootstrapDenyProbe interface {
	Probe(context.Context, RcloneNativeDenyProbeRequest) (RcloneNativeDenyProbeResult, error)
}

type RcloneNativeSessionRequest struct {
	Profile              RcloneNativeProfile
	RoleARN              string
	ExternalID           string
	PointDeadlineAt      time.Time
	SessionMargin        time.Duration
	BootstrapTemporary   bool
	Encryption           RcloneNativeEncryptionSelection
	BucketKeyEnabled     bool
	SourceReadPrefixes   []string
	SourceDecryptKeyARNs []string
}

type RcloneNativeSessionResult struct {
	Session       RcloneNativeSession
	SessionPolicy string
	RcloneConfig  []byte `json:"-"`
}

func EstablishRcloneNativeSession(ctx context.Context, assumer STSAssumer, denyProbe BootstrapDenyProbe, request RcloneNativeSessionRequest, now time.Time, random io.Reader) (RcloneNativeSessionResult, error) {
	if assumer == nil || denyProbe == nil || random == nil || ValidateRcloneNativeProfile(request.Profile) != nil ||
		!validRcloneNativeUTCTime(now) || !validRcloneNativeUTCTime(request.PointDeadlineAt) || !request.PointDeadlineAt.After(now) ||
		request.SessionMargin <= 0 || !validRcloneNativeExternalID(request.ExternalID) {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, nil)
	}
	accountID, ok := parseRcloneNativeRoleARN(request.RoleARN)
	if !ok {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonUnsupportedProfile, nil)
	}
	duration := request.PointDeadlineAt.Add(request.SessionMargin).Sub(now)
	if request.BootstrapTemporary && duration > rcloneNativeRoleChainLimit {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonSessionTooShort, nil)
	}
	policy, err := BuildRcloneNativeSessionPolicy(RcloneNativeSessionPolicyRequest{
		Profile: request.Profile, AccountID: accountID, Encryption: request.Encryption,
		BucketKeyEnabled: request.BucketKeyEnabled, SourceReadPrefixes: request.SourceReadPrefixes,
		SourceDecryptKeyARNs: request.SourceDecryptKeyARNs,
	})
	if err != nil {
		return RcloneNativeSessionResult{}, err
	}
	externalID := request.ExternalID
	correctRequest := RcloneNativeAssumeRoleRequest{
		RoleARN: request.RoleARN, ExternalID: &externalID, Duration: duration, SessionPolicy: policy,
		SessionName: "xirang-rclone-publication",
	}
	correct, err := assumer.AssumeRole(ctx, correctRequest)
	if err != nil {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, err)
	}
	if !correct.Session.valid() || correct.Session.accountID != accountID ||
		correct.Session.expiresAt.Before(request.PointDeadlineAt.Add(request.SessionMargin)) || correct.PackedPolicySize < 0 || correct.PackedPolicySize >= 100 {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonSessionTooShort, nil)
	}
	missing := correctRequest
	missing.ExternalID = nil
	if _, err := assumer.AssumeRole(ctx, missing); !errors.Is(err, ErrRcloneNativeAssumeRoleDenied) {
		if err == nil {
			return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, nil)
		}
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	wrongBytes := make([]byte, 32)
	if _, err := io.ReadFull(random, wrongBytes); err != nil {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, err)
	}
	wrongID := "xirang-wrong-" + hex.EncodeToString(wrongBytes)
	if wrongID == request.ExternalID {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, nil)
	}
	wrong := correctRequest
	wrong.ExternalID = &wrongID
	if _, err := assumer.AssumeRole(ctx, wrong); !errors.Is(err, ErrRcloneNativeAssumeRoleDenied) {
		if err == nil {
			return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, nil)
		}
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	probe, err := denyProbe.Probe(ctx, RcloneNativeDenyProbeRequest{Profile: request.Profile, ExpectedAccountID: accountID})
	if err != nil {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	if !probe.Denied {
		return RcloneNativeSessionResult{}, rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, nil)
	}
	config, err := BuildRcloneNativeRcloneConfig(request.Profile, request.Encryption, correct.Session)
	if err != nil {
		return RcloneNativeSessionResult{}, err
	}
	return RcloneNativeSessionResult{Session: correct.Session, SessionPolicy: policy, RcloneConfig: config}, nil
}

// S3Native and KMSKeyInspector intentionally expose provider-owned values.
// AWS SDK types remain confined to the production adapter.
type S3Native interface {
	BucketIdentity(context.Context, RcloneNativeProfile) (RcloneNativeBucketIdentity, error)
	GetVersioning(context.Context, RcloneNativeProfile) (RcloneNativeVersioningObservation, error)
	GetLifecycle(context.Context, RcloneNativeProfile) (RcloneNativeLifecycleObservation, error)
	GetEncryption(context.Context, RcloneNativeProfile) (RcloneNativeBucketEncryption, error)
	RcloneNativeVersionEnumerator
	RcloneNativeExactReader
	RcloneNativeExactRangeReader
	RcloneNativeControlWriter
}

// RcloneNativeCanaryS3 is the only production surface that may issue an S3
// delete outside Rclone sync. It deletes the current preflight canary without
// a VersionId so S3 creates a delete marker; exact-version deletion is not part
// of this interface or Child 5 runtime.
type RcloneNativeCanaryS3 interface {
	S3Native
	DeleteCurrentCanary(context.Context, RcloneNativeCurrentDeleteRequest) (RcloneNativeCurrentDeleteResult, error)
}

type RcloneNativeCurrentDeleteRequest struct {
	Profile     RcloneNativeProfile
	PhysicalKey string
}

type RcloneNativeCurrentDeleteResult struct {
	VersionID string
}

type RcloneNativeBucketIdentity struct {
	AccountID string
	Region    string
	Kind      string
}

type KMSKeyInspector interface {
	DescribeKey(context.Context, string) (RcloneNativeKMSKey, error)
}

type RcloneNativeClientFactory interface {
	S3(RcloneNativeSession, RcloneNativeProfile, []RcloneNativeKMSKeyDigestBinding) (S3Native, error)
	KMS(RcloneNativeSession, string) (KMSKeyInspector, error)
}

type RcloneNativeBaselineClientFactory interface {
	BaselineS3(RcloneNativeSession, RcloneNativeProfile, []string) (RcloneNativeBaselineS3, error)
}

func canonicalRcloneNativeDigest(domain string, value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = io.WriteString(digest, "xirang-rclone-native-"+domain+"\n")
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func rcloneNativeKMSKeyDigest(value RcloneNativeKMSKey) string {
	digest, _ := canonicalRcloneNativeDigest("kms-key-v1", value)
	return digest
}

func validRcloneNativeObservationWindow(firstObservedAt, now time.Time) bool {
	return validRcloneNativeUTCTime(firstObservedAt) && validRcloneNativeUTCTime(now) &&
		!firstObservedAt.After(now) && now.Sub(firstObservedAt) >= rcloneNativeCapabilitySettle
}

func validRcloneNativeUTCTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(value.UTC())
}

func validRcloneNativeRegion(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' || strings.Count(value, "-") < 2 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validRcloneNativeBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validRcloneNativePrefix(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.HasPrefix(value, "/") && strings.HasSuffix(value, "/") &&
		utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00') &&
		!strings.Contains(value, "//") && !strings.Contains(value, "../")
}

func validRcloneNativeLifecyclePrefix(value string) bool {
	return value == "" || (len(value) <= 1024 && !strings.HasPrefix(value, "/") && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00') && !strings.Contains(value, "../"))
}

func rcloneNativePrefixesOverlap(managedPrefix, rulePrefix string) bool {
	return rulePrefix == "" || strings.HasPrefix(managedPrefix, rulePrefix) || strings.HasPrefix(rulePrefix, managedPrefix)
}

func parseRcloneNativeKMSKeyARN(value string) (string, string, bool) {
	parts := strings.SplitN(value, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[1] != "aws" || parts[2] != "kms" ||
		!validRcloneNativeRegion(parts[3]) || !validRcloneNativeAccountID(parts[4]) || !strings.HasPrefix(parts[5], "key/") || len(parts[5]) <= len("key/") || strings.ContainsAny(value, "\r\n\x00") {
		return "", "", false
	}
	return parts[3], parts[4], true
}

func parseRcloneNativeRoleARN(value string) (string, bool) {
	const prefix = "arn:aws:iam::"
	if !strings.HasPrefix(value, prefix) || strings.ContainsAny(value, "\r\n\x00") {
		return "", false
	}
	remainder := strings.TrimPrefix(value, prefix)
	separator := strings.Index(remainder, ":role/")
	if separator != 12 || len(remainder) <= separator+len(":role/") {
		return "", false
	}
	accountID := remainder[:separator]
	return accountID, validRcloneNativeAccountID(accountID)
}

func validRcloneNativeAccountID(value string) bool {
	if len(value) != 12 {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func validRcloneNativeExternalID(value string) bool {
	if len(value) < 2 || len(value) > 1224 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func validRcloneNativeSecret(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00') && !strings.ContainsAny(value, "\r\n")
}

func validRcloneNativeDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}
