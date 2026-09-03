package provider

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func validRcloneNativeProfileForTest() RcloneNativeProfile {
	return RcloneNativeProfile{
		Code: RcloneNativeAWSS3GeneralPurposeV1, Region: "us-east-1", Bucket: "xirang-native-test",
		ManagedPrefix: "managed/v1/", EndpointMode: RcloneNativeEndpointAWSRegional,
		AddressingMode: RcloneNativeAddressingDNS, BucketKind: RcloneNativeBucketGeneralPurpose,
	}
}

func TestRcloneNativeAdmissionRejectsEveryUncertifiedProfileShape(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*RcloneNativeProfile)
	}{
		{"custom endpoint", func(value *RcloneNativeProfile) { value.EndpointMode = RcloneNativeEndpointCustom }},
		{"path style", func(value *RcloneNativeProfile) { value.AddressingMode = RcloneNativeAddressingPathStyle }},
		{"directory bucket", func(value *RcloneNativeProfile) { value.BucketKind = RcloneNativeBucketDirectory }},
		{"access point", func(value *RcloneNativeProfile) { value.BucketKind = RcloneNativeBucketAccessPoint }},
		{"wrapper", func(value *RcloneNativeProfile) { value.Wrapper = "crypt" }},
		{"wrong profile", func(value *RcloneNativeProfile) { value.Code = "future_profile" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := validRcloneNativeProfileForTest()
			test.mutate(&profile)
			if err := ValidateRcloneNativeProfile(profile); rcloneNativeReason(err) != backupasset.RcloneReasonUnsupportedProfile {
				t.Fatalf("profile error=%v reason=%q", err, rcloneNativeReason(err))
			}
		})
	}
	if err := ValidateRcloneNativeProfile(validRcloneNativeProfileForTest()); err != nil {
		t.Fatalf("certified profile rejected: %v", err)
	}
}

func TestRcloneNativeVersioningAndLifecycleRequireStableSafeObservations(t *testing.T) {
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	versioning := RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"}
	digest, err := CanonicalRcloneNativeVersioningDigest(versioning)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRcloneNativeVersioning(versioning, digest, now.Add(-15*time.Minute), now); err != nil {
		t.Fatalf("settled enabled versioning rejected: %v", err)
	}
	if err := ValidateRcloneNativeVersioning(versioning, digest, now.Add(-15*time.Minute+time.Second), now); rcloneNativeReason(err) != backupasset.RcloneReasonCapabilitySettling {
		t.Fatalf("unsettled versioning error=%v", err)
	}
	for _, invalid := range []RcloneNativeVersioningObservation{
		{Status: "Suspended", MFADelete: "Disabled"},
		{Status: "Enabled", MFADelete: "Enabled"},
		{Status: "Enabled", MFADelete: ""},
	} {
		if err := ValidateRcloneNativeVersioning(invalid, digest, now.Add(-time.Hour), now); rcloneNativeReason(err) != backupasset.RcloneReasonVersioningDisabled {
			t.Fatalf("invalid versioning=%+v error=%v", invalid, err)
		}
	}

	safe := RcloneNativeLifecycleObservation{}
	safeDigest, err := CanonicalRcloneNativeLifecycleDigest(safe)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRcloneNativeLifecycle(safe, "managed/v1/", safeDigest, now.Add(-15*time.Minute), now); err != nil {
		t.Fatalf("no lifecycle rejected: %v", err)
	}
	for _, rule := range []RcloneNativeLifecycleRule{
		{ID: "expire-current", Enabled: true, Prefix: "managed/", ExpireCurrent: true},
		{ID: "expire-noncurrent", Enabled: true, Prefix: "managed/v1/", ExpireNoncurrent: true},
		{ID: "delete-marker", Enabled: true, Prefix: "", ExpiredDeleteMarkerCleanup: true},
		{ID: "archive", Enabled: true, Prefix: "managed/", Transitions: []string{"GLACIER"}},
		{ID: "intelligent", Enabled: true, Prefix: "managed/", Transitions: []string{"INTELLIGENT_TIERING"}},
		{ID: "tag-exemption", Enabled: true, Prefix: "managed/", HasTagFilter: true, ExpireNoncurrent: true},
		{ID: "unknown", Enabled: true, Prefix: "managed/", UnknownAction: true},
	} {
		observation := RcloneNativeLifecycleObservation{Rules: []RcloneNativeLifecycleRule{rule}}
		digest, err := CanonicalRcloneNativeLifecycleDigest(observation)
		if err != nil && !rule.UnknownAction {
			t.Fatal(err)
		}
		if err := ValidateRcloneNativeLifecycle(observation, "managed/v1/", digest, now.Add(-time.Hour), now); rcloneNativeReason(err) != backupasset.RcloneReasonLifecycleConflict {
			t.Fatalf("unsafe lifecycle %+v error=%v reason=%q", rule, err, rcloneNativeReason(err))
		}
	}
	allowed := RcloneNativeLifecycleObservation{Rules: []RcloneNativeLifecycleRule{{
		ID: "online", Enabled: true, Prefix: "managed/", Transitions: []string{"STANDARD_IA", "ONEZONE_IA", "GLACIER_IR"},
	}}}
	allowedDigest, err := CanonicalRcloneNativeLifecycleDigest(allowed)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRcloneNativeLifecycle(allowed, "managed/v1/", allowedDigest, now.Add(-time.Hour), now); err != nil {
		t.Fatalf("online lifecycle transitions rejected: %v", err)
	}
}

func validRcloneNativeKMSKeyForTest(arn string) RcloneNativeKMSKey {
	return RcloneNativeKMSKey{
		ARN: arn, AccountID: "123456789012", Region: "us-east-1", Manager: "CUSTOMER",
		Spec: "SYMMETRIC_DEFAULT", Usage: "ENCRYPT_DECRYPT", State: "Enabled", Origin: "AWS_KMS",
	}
}

type rcloneNativeSourceKMSInspectorFake struct {
	keys     map[string]RcloneNativeKMSKey
	requests []string
}

func (fake *rcloneNativeSourceKMSInspectorFake) DescribeKey(_ context.Context, arn string) (RcloneNativeKMSKey, error) {
	fake.requests = append(fake.requests, arn)
	key, exists := fake.keys[arn]
	if !exists {
		return RcloneNativeKMSKey{}, errors.New("FAKE_SOURCE_KMS_KEY_NOT_FOUND_FOR_TEST_ONLY")
	}
	return key, nil
}

func TestValidateRcloneNativeSourceKMSKeysFreezesBoundDecryptOnlyEvidence(t *testing.T) {
	firstARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-FIRST-SOURCE-KMS-KEY-FOR-TEST-ONLY"
	secondARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-SECOND-SOURCE-KMS-KEY-FOR-TEST-ONLY"
	inspector := &rcloneNativeSourceKMSInspectorFake{keys: map[string]RcloneNativeKMSKey{
		firstARN: validRcloneNativeKMSKeyForTest(firstARN), secondARN: validRcloneNativeKMSKeyForTest(secondARN),
	}}
	result, err := ValidateRcloneNativeSourceKMSKeys(context.Background(), inspector, RcloneNativeSourceKMSValidationRequest{
		KeyARNs: []string{secondARN, firstARN},
		SessionPolicy: RcloneNativeSessionPolicyRequest{
			Profile: validRcloneNativeProfileForTest(), AccountID: "123456789012",
			Encryption:         RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
			SourceReadPrefixes: []string{"legacy/current/"},
		},
		Limits: RcloneNativeKMSLimits{MaxReadKeys: 2, MaxSerializedBytes: 4096},
	})
	if err != nil || len(result.KeyBindings) != 2 || result.KeySetDigest == "" || result.SessionPolicy == "" {
		t.Fatalf("source KMS evidence=%+v err=%v", result, err)
	}
	if result.KeyBindings[0].KeyARN != firstARN || result.KeyBindings[1].KeyARN != secondARN ||
		!slices.Equal(inspector.requests, []string{firstARN, secondARN}) {
		t.Fatalf("source KMS order bindings=%+v requests=%+v", result.KeyBindings, inspector.requests)
	}
	for _, required := range []string{firstARN, secondARN, "kms:Decrypt", "legacy/current/"} {
		if !strings.Contains(result.SessionPolicy, required) {
			t.Fatalf("source KMS policy missing %q: %s", required, result.SessionPolicy)
		}
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{firstARN, secondARN, "legacy/current/", result.SessionPolicy} {
		if strings.Contains(string(payload), private) {
			t.Fatalf("source KMS evidence leaked %q: %s", private, payload)
		}
	}
}

func TestValidateRcloneNativeSourceKMSKeysRejectsUnsafeOrUnboundedKeys(t *testing.T) {
	validARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-VALID-SOURCE-KMS-KEY-FOR-TEST-ONLY"
	baseRequest := RcloneNativeSourceKMSValidationRequest{
		KeyARNs: []string{validARN},
		SessionPolicy: RcloneNativeSessionPolicyRequest{
			Profile: validRcloneNativeProfileForTest(), AccountID: "123456789012",
			Encryption:         RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
			SourceReadPrefixes: []string{"legacy/current/"},
		},
		Limits: RcloneNativeKMSLimits{MaxReadKeys: 2, MaxSerializedBytes: 4096},
	}
	for name, mutate := range map[string]func(*RcloneNativeSourceKMSValidationRequest, *rcloneNativeSourceKMSInspectorFake){
		"duplicate": func(request *RcloneNativeSourceKMSValidationRequest, _ *rcloneNativeSourceKMSInspectorFake) {
			request.KeyARNs = append(request.KeyARNs, validARN)
		},
		"count limit": func(request *RcloneNativeSourceKMSValidationRequest, _ *rcloneNativeSourceKMSInspectorFake) {
			request.Limits.MaxReadKeys = 0
		},
		"serialized limit": func(request *RcloneNativeSourceKMSValidationRequest, _ *rcloneNativeSourceKMSInspectorFake) {
			request.Limits.MaxSerializedBytes = len(validARN) - 1
		},
		"cross region": func(_ *RcloneNativeSourceKMSValidationRequest, inspector *rcloneNativeSourceKMSInspectorFake) {
			key := inspector.keys[validARN]
			key.Region = "us-west-2"
			inspector.keys[validARN] = key
		},
		"AWS managed": func(_ *RcloneNativeSourceKMSValidationRequest, inspector *rcloneNativeSourceKMSInspectorFake) {
			key := inspector.keys[validARN]
			key.Manager = "AWS"
			inspector.keys[validARN] = key
		},
		"disabled": func(_ *RcloneNativeSourceKMSValidationRequest, inspector *rcloneNativeSourceKMSInspectorFake) {
			key := inspector.keys[validARN]
			key.State = "Disabled"
			inspector.keys[validARN] = key
		},
		"prepopulated policy keys": func(request *RcloneNativeSourceKMSValidationRequest, _ *rcloneNativeSourceKMSInspectorFake) {
			request.SessionPolicy.SourceDecryptKeyARNs = []string{validARN}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := baseRequest
			request.KeyARNs = append([]string(nil), baseRequest.KeyARNs...)
			request.SessionPolicy.SourceReadPrefixes = append([]string(nil), baseRequest.SessionPolicy.SourceReadPrefixes...)
			inspector := &rcloneNativeSourceKMSInspectorFake{keys: map[string]RcloneNativeKMSKey{
				validARN: validRcloneNativeKMSKeyForTest(validARN),
			}}
			mutate(&request, inspector)
			if _, err := ValidateRcloneNativeSourceKMSKeys(context.Background(), inspector, request); err == nil {
				t.Fatal("unsafe source KMS key set unexpectedly accepted")
			}
		})
	}

	longARN := "arn:aws:kms:us-east-1:123456789012:key/" + strings.Repeat("x", 1500)
	request := baseRequest
	request.KeyARNs = []string{longARN}
	request.Limits.MaxSerializedBytes = 4096
	request.SessionPolicy.SourceReadPrefixes = []string{strings.Repeat("p", 1000) + "/"}
	inspector := &rcloneNativeSourceKMSInspectorFake{keys: map[string]RcloneNativeKMSKey{
		longARN: validRcloneNativeKMSKeyForTest(longARN),
	}}
	if _, err := ValidateRcloneNativeSourceKMSKeys(context.Background(), inspector, request); rcloneNativeReason(err) != backupasset.RcloneReasonKMSKeyRingLimit {
		t.Fatalf("oversized source KMS policy error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

func TestRcloneNativeEncryptionProfilesAndKMSKeyRingAreClosed(t *testing.T) {
	bucketSSES3 := RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true}
	if _, err := ValidateRcloneNativeEncryption(RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, bucketSSES3, nil, RcloneNativeKMSLimits{}); err != nil {
		t.Fatalf("SSE-S3 rejected: %v", err)
	}
	activeARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-ACTIVE-KMS-KEY-FOR-TEST-ONLY"
	oldARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-OLD-KMS-KEY-FOR-TEST-ONLY"
	selection := RcloneNativeEncryptionSelection{Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: activeARN, RetainedReadKeyARNs: []string{oldARN}}
	keys := []RcloneNativeKMSKey{validRcloneNativeKMSKeyForTest(activeARN), validRcloneNativeKMSKeyForTest(oldARN)}
	bucketKMS := RcloneNativeBucketEncryption{Algorithm: "aws:kms", KMSKeyARN: activeARN, BucketKeyEnabled: true, BlockedEncryptionTypesKnown: true}
	result, err := ValidateRcloneNativeEncryption(selection, bucketKMS, keys, RcloneNativeKMSLimits{MaxReadKeys: 8, MaxSerializedBytes: 4096})
	if err != nil || result.ActiveKeyDigest == "" || result.ReadKeySetDigest == "" {
		t.Fatalf("KMS result=%+v err=%v", result, err)
	}
	activeBinding, err := RcloneNativeKMSKeyBinding(keys[0])
	if err != nil || activeBinding.KeyARN != activeARN || activeBinding.Digest != result.ActiveKeyDigest {
		t.Fatalf("active KMS binding=%+v err=%v evidence=%+v", activeBinding, err, result)
	}
	if _, err := ValidateRcloneNativeEncryption(RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, bucketKMS, nil, RcloneNativeKMSLimits{}); err != nil {
		t.Fatalf("explicit SSE-S3 rejected only because bucket default is SSE-KMS: %v", err)
	}
	result, err = ValidateRcloneNativeEncryption(selection, bucketSSES3, keys, RcloneNativeKMSLimits{MaxReadKeys: 8, MaxSerializedBytes: 4096})
	if err != nil || result.Profile != RcloneNativeSSEKMSV1 || result.BucketKeyEnabled {
		t.Fatalf("explicit SSE-KMS rejected only because bucket default is SSE-S3: result=%+v err=%v", result, err)
	}

	for _, test := range []struct {
		name      string
		selection RcloneNativeEncryptionSelection
		bucket    RcloneNativeBucketEncryption
		keys      []RcloneNativeKMSKey
	}{
		{"DSSE", RcloneNativeEncryptionSelection{Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: activeARN}, RcloneNativeBucketEncryption{Algorithm: "aws:kms:dsse", BlockedEncryptionTypesKnown: true}, keys[:1]},
		{"SSE-C unknown", RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, RcloneNativeBucketEncryption{Algorithm: "AES256"}, nil},
		{"alias", RcloneNativeEncryptionSelection{Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: "arn:aws:kms:us-east-1:123456789012:alias/not-allowed"}, bucketKMS, keys[:1]},
		{"AWS managed", selection, bucketKMS, []RcloneNativeKMSKey{func() RcloneNativeKMSKey { value := keys[0]; value.Manager = "AWS"; return value }(), keys[1]}},
		{"disabled", selection, bucketKMS, []RcloneNativeKMSKey{func() RcloneNativeKMSKey { value := keys[0]; value.State = "Disabled"; return value }(), keys[1]}},
		{"external origin", selection, bucketKMS, []RcloneNativeKMSKey{func() RcloneNativeKMSKey { value := keys[0]; value.Origin = "EXTERNAL"; return value }(), keys[1]}},
		{"cross account", selection, bucketKMS, []RcloneNativeKMSKey{func() RcloneNativeKMSKey { value := keys[0]; value.AccountID = "210987654321"; return value }(), keys[1]}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ValidateRcloneNativeEncryption(test.selection, test.bucket, test.keys, RcloneNativeKMSLimits{MaxReadKeys: 8, MaxSerializedBytes: 4096}); rcloneNativeReason(err) != backupasset.RcloneReasonEncryptionUnsupported && rcloneNativeReason(err) != backupasset.RcloneReasonKMSKeyUnavailable {
				t.Fatalf("unsafe encryption error=%v reason=%q", err, rcloneNativeReason(err))
			}
		})
	}
}

func TestBuildRcloneNativeEncryptionEvidenceUsesExactFrozenBindings(t *testing.T) {
	activeARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-ACTIVE-EVIDENCE-KEY-FOR-TEST-ONLY"
	retainedARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-RETAINED-EVIDENCE-KEY-FOR-TEST-ONLY"
	selection := RcloneNativeEncryptionSelection{
		Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: activeARN, RetainedReadKeyARNs: []string{retainedARN},
	}
	bindings := []RcloneNativeKMSKeyDigestBinding{
		{KeyARN: retainedARN, Digest: strings.Repeat("b", 64)},
		{KeyARN: activeARN, Digest: strings.Repeat("a", 64)},
	}
	evidence, err := BuildRcloneNativeEncryptionEvidence(selection, bindings, true)
	if err != nil || evidence.Profile != RcloneNativeSSEKMSV1 || evidence.ActiveKeyDigest != strings.Repeat("a", 64) ||
		evidence.ReadKeySetDigest == "" || evidence.RetainedReadKeyCount != 1 || !evidence.BucketKeyEnabled {
		t.Fatalf("frozen KMS evidence=%+v err=%v", evidence, err)
	}
	if _, err := BuildRcloneNativeEncryptionEvidence(RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, nil, false); err != nil {
		t.Fatalf("frozen SSE-S3 evidence: %v", err)
	}
	for _, invalid := range [][]RcloneNativeKMSKeyDigestBinding{
		bindings[:1],
		{bindings[0], bindings[0]},
		{{KeyARN: activeARN, Digest: "not-a-digest"}, {KeyARN: retainedARN, Digest: strings.Repeat("b", 64)}},
	} {
		if _, err := BuildRcloneNativeEncryptionEvidence(selection, invalid, true); rcloneNativeReason(err) != backupasset.RcloneReasonEncryptionUnsupported {
			t.Fatalf("unsafe frozen KMS bindings error=%v reason=%q", err, rcloneNativeReason(err))
		}
	}
}

func TestRcloneNativeSessionPolicyAndGeneratedConfigStayExactAndPrivate(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	activeARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-ACTIVE-KMS-KEY-FOR-TEST-ONLY"
	selection := RcloneNativeEncryptionSelection{Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: activeARN}
	policy, err := BuildRcloneNativeSessionPolicy(RcloneNativeSessionPolicyRequest{
		Profile: profile, AccountID: "123456789012", Encryption: selection, BucketKeyEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"s3:ResourceAccount", "123456789012", "arn:aws:s3:::xirang-native-test/managed/v1/*", activeARN, "kms:ViaService", "s3.us-east-1.amazonaws.com"} {
		if !strings.Contains(policy, required) {
			t.Fatalf("session policy missing %q: %s", required, policy)
		}
	}
	if len(policy) > 2048 {
		t.Fatalf("session policy length=%d", len(policy))
	}
	var document rcloneNativePolicyDocument
	if err := json.Unmarshal([]byte(policy), &document); err != nil {
		t.Fatal(err)
	}
	assertKMSContext := func(t *testing.T, statements []rcloneNativePolicyStatement, operator, value string) {
		t.Helper()
		for _, statement := range statements {
			if slices.Contains(statement.Action, "kms:GenerateDataKey") {
				if statement.Condition[operator]["kms:EncryptionContext:aws:s3:arn"] != value {
					t.Fatalf("KMS context condition=%+v want %s=%q", statement.Condition, operator, value)
				}
				return
			}
		}
		t.Fatal("KMS cryptographic statement missing")
	}
	assertKMSContext(t, document.Statement, "StringLike", "arn:aws:s3:::xirang-native-test/managed/v1/*")
	bucketKeyPolicy, err := BuildRcloneNativeSessionPolicy(RcloneNativeSessionPolicyRequest{
		Profile: profile, AccountID: "123456789012", Encryption: selection, BucketKeyEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(bucketKeyPolicy), &document); err != nil {
		t.Fatal(err)
	}
	assertKMSContext(t, document.Statement, "StringEquals", "arn:aws:s3:::xirang-native-test")

	session := newRcloneNativeSession(
		"FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY", "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY",
		"FAKE_AWS_SESSION_TOKEN_FOR_TEST_ONLY", "123456789012", strings.Repeat("a", 64),
		time.Date(2026, 7, 16, 11, 0, 0, 0, time.UTC),
	)
	config, err := BuildRcloneNativeRcloneConfig(profile, selection, session)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"type = s3", "provider = AWS", "env_auth = false", "server_side_encryption = aws:kms", "sse_kms_key_id = " + activeARN} {
		if !strings.Contains(string(config), required) {
			t.Fatalf("Rclone config missing %q", required)
		}
	}
	if strings.Contains(string(config), "endpoint =") || strings.Contains(string(config), "force_path_style = true") {
		t.Fatalf("generated config widened certified profile: %s", config)
	}
}

func TestRcloneNativeSessionPolicyScopesExactVersionDeleteToManagedObjects(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	policy, err := BuildRcloneNativeSessionPolicy(RcloneNativeSessionPolicyRequest{
		Profile: profile, AccountID: "123456789012",
		Encryption:         RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
		SourceReadPrefixes: []string{"legacy/current/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var document rcloneNativePolicyDocument
	if err := json.Unmarshal([]byte(policy), &document); err != nil {
		t.Fatal(err)
	}
	managedObjectARN := "arn:aws:s3:::xirang-native-test/managed/v1/*"
	found := false
	for _, statement := range document.Statement {
		if !slices.Contains(statement.Action, "s3:DeleteObjectVersion") {
			continue
		}
		if len(statement.Resource) != 1 || statement.Resource[0] != managedObjectARN {
			t.Fatalf("exact version delete widened beyond managed object scope: %+v", statement)
		}
		found = true
	}
	if !found {
		t.Fatalf("managed object policy missing s3:DeleteObjectVersion: %s", policy)
	}
}

func TestRcloneNativeBaselineSessionPolicyAddsExactSourceReadAndDecryptOnly(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	sourceKeyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-SOURCE-KMS-KEY-FOR-TEST-ONLY"
	policy, err := BuildRcloneNativeSessionPolicy(RcloneNativeSessionPolicyRequest{
		Profile: profile, AccountID: "123456789012",
		Encryption:           RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
		SourceReadPrefixes:   []string{"legacy/current/"},
		SourceDecryptKeyARNs: []string{sourceKeyARN},
	})
	if err != nil {
		t.Fatal(err)
	}
	var document rcloneNativePolicyDocument
	if err := json.Unmarshal([]byte(policy), &document); err != nil {
		t.Fatal(err)
	}
	sourceObjectARN := "arn:aws:s3:::xirang-native-test/legacy/current/*"
	sourceReadFound := false
	sourceDecryptFound := false
	sourceDescribeFound := false
	for _, statement := range document.Statement {
		if slices.Contains(statement.Resource, sourceObjectARN) {
			sourceReadFound = slices.Contains(statement.Action, "s3:GetObject") && slices.Contains(statement.Action, "s3:GetObjectVersion")
			for _, forbidden := range []string{"s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:AbortMultipartUpload"} {
				if slices.Contains(statement.Action, forbidden) {
					t.Fatalf("baseline source statement grants %s: %+v", forbidden, statement)
				}
			}
		}
		if slices.Contains(statement.Resource, sourceKeyARN) && slices.Contains(statement.Action, "kms:DescribeKey") {
			sourceDescribeFound = true
		}
		if slices.Contains(statement.Resource, sourceKeyARN) && slices.Contains(statement.Action, "kms:Decrypt") {
			sourceDecryptFound = statement.Condition["StringLike"]["kms:EncryptionContext:aws:s3:arn"] == sourceObjectARN
			if slices.Contains(statement.Action, "kms:GenerateDataKey") {
				t.Fatalf("baseline source key grants GenerateDataKey: %+v", statement)
			}
		}
	}
	if !sourceReadFound || !sourceDescribeFound || !sourceDecryptFound {
		t.Fatalf("baseline source policy incomplete: %s", policy)
	}
	if len(policy) > 2048 {
		t.Fatalf("baseline source policy length=%d", len(policy))
	}
	for _, invalid := range []RcloneNativeSessionPolicyRequest{
		{
			Profile: profile, AccountID: "123456789012", Encryption: RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
			SourceReadPrefixes: []string{"managed/v1/legacy/"},
		},
		{
			Profile: profile, AccountID: "123456789012", Encryption: RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
			SourceReadPrefixes: []string{"legacy/current/"}, SourceDecryptKeyARNs: []string{"*"},
		},
	} {
		if _, err := BuildRcloneNativeSessionPolicy(invalid); err == nil {
			t.Fatalf("unsafe baseline source policy accepted: %+v", invalid)
		}
	}
}

func TestNewRcloneNativeSessionReturnsOnlyOpaqueSafeFacts(t *testing.T) {
	expiresAt := time.Date(2026, 7, 16, 11, 0, 0, 0, time.UTC)
	session, err := NewRcloneNativeSession(
		"FAKE_OPAQUE_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_OPAQUE_SECRET_KEY_FOR_TEST_ONLY",
		"FAKE_OPAQUE_SESSION_TOKEN_FOR_TEST_ONLY", "123456789012", strings.Repeat("a", 64), expiresAt,
	)
	if err != nil || session.IdentityDigest() != strings.Repeat("a", 64) || session.AccountID() != "123456789012" || !session.ExpiresAt().Equal(expiresAt) {
		t.Fatalf("opaque session facts identity=%q account=%q expiry=%s err=%v", session.IdentityDigest(), session.AccountID(), session.ExpiresAt(), err)
	}
	if _, err := BuildRcloneNativeRcloneConfig(validRcloneNativeProfileForTest(), RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, session); err != nil {
		t.Fatalf("opaque session cannot build private config: %v", err)
	}
	if _, err := NewRcloneNativeSession("short", "short", "short", "bad", "bad", time.Time{}); err == nil {
		t.Fatal("invalid opaque session accepted")
	}
}

type scriptedRcloneNativeSTS struct {
	results  []RcloneNativeAssumeRoleResult
	errors   []error
	requests []RcloneNativeAssumeRoleRequest
}

func (fake *scriptedRcloneNativeSTS) AssumeRole(_ context.Context, request RcloneNativeAssumeRoleRequest) (RcloneNativeAssumeRoleResult, error) {
	fake.requests = append(fake.requests, request)
	index := len(fake.requests) - 1
	if index < len(fake.errors) && fake.errors[index] != nil {
		return RcloneNativeAssumeRoleResult{}, fake.errors[index]
	}
	return fake.results[index], nil
}

type rcloneNativeDenyProbeFake struct {
	denied bool
	err    error
}

func (fake rcloneNativeDenyProbeFake) Probe(_ context.Context, _ RcloneNativeDenyProbeRequest) (RcloneNativeDenyProbeResult, error) {
	return RcloneNativeDenyProbeResult{Denied: fake.denied}, fake.err
}

func TestRcloneNativeSTSContractRequiresCorrectAndRejectedNegativeExternalIDs(t *testing.T) {
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	session := newRcloneNativeSession(
		"FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY", "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY",
		"FAKE_AWS_SESSION_TOKEN_FOR_TEST_ONLY", "123456789012", strings.Repeat("a", 64), now.Add(50*time.Minute),
	)
	sts := &scriptedRcloneNativeSTS{
		results: []RcloneNativeAssumeRoleResult{{Session: session}, {}, {}},
		errors:  []error{nil, ErrRcloneNativeAssumeRoleDenied, ErrRcloneNativeAssumeRoleDenied},
	}
	request := RcloneNativeSessionRequest{
		Profile: validRcloneNativeProfileForTest(), RoleARN: "arn:aws:iam::123456789012:role/xirang-backup-test",
		ExternalID: "FAKE_EXTERNAL_ID_FOR_TEST_ONLY", PointDeadlineAt: now.Add(45 * time.Minute),
		SessionMargin: 2 * time.Minute, BootstrapTemporary: true,
		Encryption: RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
	}
	result, err := EstablishRcloneNativeSession(context.Background(), sts, rcloneNativeDenyProbeFake{denied: true}, request, now, strings.NewReader(strings.Repeat("x", 64)))
	if err != nil || result.Session.IdentityDigest() != strings.Repeat("a", 64) || len(sts.requests) != 3 {
		t.Fatalf("session result=%+v calls=%d err=%v", result, len(sts.requests), err)
	}
	if sts.requests[0].ExternalID == nil || sts.requests[1].ExternalID != nil || sts.requests[2].ExternalID == nil || *sts.requests[2].ExternalID == request.ExternalID {
		t.Fatalf("external-ID probe sequence=%+v", sts.requests)
	}
	bucketKeySTS := &scriptedRcloneNativeSTS{
		results: []RcloneNativeAssumeRoleResult{{Session: session}, {}, {}},
		errors:  []error{nil, ErrRcloneNativeAssumeRoleDenied, ErrRcloneNativeAssumeRoleDenied},
	}
	bucketKeyRequest := request
	bucketKeyRequest.Encryption = RcloneNativeEncryptionSelection{
		Profile:      RcloneNativeSSEKMSV1,
		ActiveKeyARN: "arn:aws:kms:us-east-1:123456789012:key/FAKE-BUCKET-KEY-FOR-TEST-ONLY",
	}
	bucketKeyRequest.BucketKeyEnabled = true
	if _, err := EstablishRcloneNativeSession(context.Background(), bucketKeySTS, rcloneNativeDenyProbeFake{denied: true}, bucketKeyRequest, now, strings.NewReader(strings.Repeat("k", 64))); err != nil {
		t.Fatalf("bucket-key session establishment failed: %v", err)
	}
	if len(bucketKeySTS.requests) != 3 || !strings.Contains(bucketKeySTS.requests[0].SessionPolicy,
		`"StringEquals":{"kms:EncryptionContext:aws:s3:arn":"arn:aws:s3:::xirang-native-test","kms:ViaService":"s3.us-east-1.amazonaws.com"}`) {
		t.Fatalf("bucket-key mode missing from actual STS policy: %+v", bucketKeySTS.requests)
	}

	for _, test := range []struct {
		name   string
		sts    *scriptedRcloneNativeSTS
		probe  rcloneNativeDenyProbeFake
		reason backupasset.RcloneVersioningReasonCode
	}{
		{"missing ID accepted", &scriptedRcloneNativeSTS{results: []RcloneNativeAssumeRoleResult{{Session: session}, {Session: session}}, errors: []error{nil, nil}}, rcloneNativeDenyProbeFake{denied: true}, backupasset.RcloneReasonCredentialInvalid},
		{"wrong ID accepted", &scriptedRcloneNativeSTS{results: []RcloneNativeAssumeRoleResult{{Session: session}, {}, {Session: session}}, errors: []error{nil, ErrRcloneNativeAssumeRoleDenied, nil}}, rcloneNativeDenyProbeFake{denied: true}, backupasset.RcloneReasonCredentialInvalid},
		{"bootstrap S3 allowed", &scriptedRcloneNativeSTS{results: []RcloneNativeAssumeRoleResult{{Session: session}, {}, {}}, errors: []error{nil, ErrRcloneNativeAssumeRoleDenied, ErrRcloneNativeAssumeRoleDenied}}, rcloneNativeDenyProbeFake{denied: false}, backupasset.RcloneReasonCredentialInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := EstablishRcloneNativeSession(context.Background(), test.sts, test.probe, request, now, strings.NewReader(strings.Repeat("y", 64)))
			if rcloneNativeReason(err) != test.reason {
				t.Fatalf("session error=%v reason=%q", err, rcloneNativeReason(err))
			}
		})
	}

	tooLong := request
	tooLong.PointDeadlineAt = now.Add(59 * time.Minute)
	if _, err := EstablishRcloneNativeSession(context.Background(), sts, rcloneNativeDenyProbeFake{denied: true}, tooLong, now, strings.NewReader(strings.Repeat("z", 64))); rcloneNativeReason(err) != backupasset.RcloneReasonSessionTooShort {
		t.Fatalf("role-chain deadline error=%v", err)
	}
}

var _ STSAssumer = (*scriptedRcloneNativeSTS)(nil)
var _ BootstrapDenyProbe = rcloneNativeDenyProbeFake{}
var _ = errors.Is
