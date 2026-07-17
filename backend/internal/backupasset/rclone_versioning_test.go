package backupasset

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validRclonePublicationSummaryForTest() RclonePublicationSummary {
	return RclonePublicationSummary{
		Mode:                   PublicationVersionedPrefix,
		State:                  RcloneStateReady,
		ReasonCode:             RcloneReasonReady,
		TaskRevision:           "9007199254740993",
		BindingRevision:        "2",
		CapabilityRevision:     "3",
		ConsistencyClass:       RcloneConsistencyObservationallyStable,
		HashFidelity:           RcloneHashDownloadVerifiedBytes,
		EstimatedReadBytes:     "1099511627776",
		APICostClass:           RcloneCostModerate,
		StorageCostClass:       RcloneCostHigh,
		EgressCostClass:        RcloneCostLow,
		EncryptionProfile:      RcloneEncryptionNone,
		KMSKeyStatus:           RcloneKMSNotApplicable,
		RollbackLocatorPresent: true,
		RollbackCapability:     RcloneRollbackCleanAvailable,
	}
}

func TestRclonePublicationSummaryAcceptsEveryClosedAggregateValue(t *testing.T) {
	base := validRclonePublicationSummaryForTest()

	for _, mode := range []TaskPublicationMode{PublicationLegacyMutable, PublicationVersionedPrefix, PublicationNativeObjectVersions} {
		summary := base
		summary.Mode = mode
		if mode == PublicationNativeObjectVersions {
			summary.EncryptionProfile = RcloneEncryptionSSES3
		}
		if err := summary.Validate(); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
	for _, state := range []RcloneVersioningState{
		RcloneStateLegacy, RcloneStatePreflightRequired, RcloneStateCredentialSetupRequired,
		RcloneStateCapabilitySettling, RcloneStateReady, RcloneStatePreparing,
		RcloneStateVerifying, RcloneStateCommitted, RcloneStateDegraded,
		RcloneStateAtRisk, RcloneStateFailed, RcloneStateBlocked, RcloneStateRollbackPrepared,
	} {
		summary := base
		summary.State = state
		if err := summary.Validate(); err != nil {
			t.Fatalf("state %q rejected: %v", state, err)
		}
	}

	reasons := []RcloneVersioningReasonCode{
		RcloneReasonLegacy, RcloneReasonPreflightRequired, RcloneReasonReady,
		RcloneReasonCredentialSetupRequired, RcloneReasonCapabilitySettling,
		RcloneReasonPreflightExpired, RcloneReasonTaskRevisionChanged,
		RcloneReasonBindingRevisionChanged, RcloneReasonPreflightMismatch,
		RcloneReasonFeatureDisabled, RcloneReasonUnsupportedProfile,
		RcloneReasonRepositoryOffline, RcloneReasonProviderUnavailable,
		RcloneReasonProviderTimeout, RcloneReasonProviderResourceLimit,
		RcloneReasonSessionTooShort, RcloneReasonVersioningDisabled,
		RcloneReasonLifecycleConflict, RcloneReasonEncryptionUnsupported,
		RcloneReasonKMSKeyUnavailable, RcloneReasonKMSPermissionDenied,
		RcloneReasonKMSKeyRingLimit, RcloneReasonIdentityMismatch,
		RcloneReasonCredentialInvalid, RcloneReasonVerificationCostLimit,
		RcloneReasonSourceDrift, RcloneReasonExternalWriterDetected,
		RcloneReasonUnexpectedVersion, RcloneReasonManifestMismatch,
		RcloneReasonMarkerMismatch, RcloneReasonAdmissionBlocked,
		RcloneReasonOutcomeUnknown, RcloneReasonRollbackPrepared,
	}
	if len(reasons) != 33 {
		t.Fatalf("safe reason ledger has %d values, want 33", len(reasons))
	}
	for _, reason := range reasons {
		summary := base
		summary.ReasonCode = reason
		if err := summary.Validate(); err != nil {
			t.Fatalf("reason %q rejected: %v", reason, err)
		}
	}

	for _, consistency := range []RcloneConsistencyClass{
		RcloneConsistencyNotEvaluated, RcloneConsistencyObservationallyStable, RcloneConsistencyProviderStrong,
	} {
		summary := base
		summary.ConsistencyClass = consistency
		if err := summary.Validate(); err != nil {
			t.Fatalf("consistency %q rejected: %v", consistency, err)
		}
	}
	for _, fidelity := range []RcloneHashFidelity{
		RcloneHashNotEvaluated, RcloneHashProviderStrongChecksum, RcloneHashDownloadVerifiedBytes,
	} {
		summary := base
		summary.HashFidelity = fidelity
		if err := summary.Validate(); err != nil {
			t.Fatalf("hash fidelity %q rejected: %v", fidelity, err)
		}
	}
	for _, cost := range []RcloneCostClass{
		RcloneCostNotEvaluated, RcloneCostNone, RcloneCostLow, RcloneCostModerate, RcloneCostHigh,
	} {
		summary := base
		summary.APICostClass = cost
		summary.StorageCostClass = cost
		summary.EgressCostClass = cost
		if err := summary.Validate(); err != nil {
			t.Fatalf("cost %q rejected: %v", cost, err)
		}
	}
	for _, profile := range []RcloneEncryptionProfile{RcloneEncryptionNone, RcloneEncryptionSSES3, RcloneEncryptionSSEKMS} {
		summary := base
		summary.EncryptionProfile = profile
		if profile != RcloneEncryptionNone {
			summary.Mode = PublicationNativeObjectVersions
		}
		if profile == RcloneEncryptionSSEKMS {
			summary.KMSKeyStatus = RcloneKMSReady
		}
		if err := summary.Validate(); err != nil {
			t.Fatalf("encryption %q rejected: %v", profile, err)
		}
	}
	for _, status := range []RcloneKMSKeyStatus{
		RcloneKMSNotApplicable, RcloneKMSReady, RcloneKMSDegraded, RcloneKMSAtRisk, RcloneKMSBlocked,
	} {
		summary := base
		summary.KMSKeyStatus = status
		if status != RcloneKMSNotApplicable {
			summary.Mode = PublicationNativeObjectVersions
			summary.EncryptionProfile = RcloneEncryptionSSEKMS
		}
		if err := summary.Validate(); err != nil {
			t.Fatalf("KMS status %q rejected: %v", status, err)
		}
	}
	for _, capability := range []RcloneRollbackCapability{
		RcloneRollbackCleanAvailable, RcloneRollbackPreparationOnly, RcloneRollbackPrepared,
	} {
		summary := base
		summary.RollbackCapability = capability
		if err := summary.Validate(); err != nil {
			t.Fatalf("rollback capability %q rejected: %v", capability, err)
		}
	}
}

func TestRclonePublicationSummaryRejectsImpossibleEncryptionAndKMSCombinations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RclonePublicationSummary)
	}{
		{
			name: "portable encryption",
			mutate: func(value *RclonePublicationSummary) {
				value.EncryptionProfile = RcloneEncryptionSSES3
			},
		},
		{
			name: "native without encryption",
			mutate: func(value *RclonePublicationSummary) {
				value.Mode = PublicationNativeObjectVersions
			},
		},
		{
			name: "SSE-S3 with KMS status",
			mutate: func(value *RclonePublicationSummary) {
				value.Mode = PublicationNativeObjectVersions
				value.EncryptionProfile = RcloneEncryptionSSES3
				value.KMSKeyStatus = RcloneKMSReady
			},
		},
		{
			name: "SSE-S3 with retained KMS key count",
			mutate: func(value *RclonePublicationSummary) {
				value.Mode = PublicationNativeObjectVersions
				value.EncryptionProfile = RcloneEncryptionSSES3
				value.KMSReadKeyCount = 1
			},
		},
		{
			name: "SSE-KMS without KMS status",
			mutate: func(value *RclonePublicationSummary) {
				value.Mode = PublicationNativeObjectVersions
				value.EncryptionProfile = RcloneEncryptionSSEKMS
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validRclonePublicationSummaryForTest()
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatalf("impossible summary accepted: %+v", value)
			}
		})
	}
}

func TestRclonePublicationSummaryRejectsUnsafeValuesAndNonCanonicalDecimals(t *testing.T) {
	base := validRclonePublicationSummaryForTest()
	tests := []func(*RclonePublicationSummary){
		func(value *RclonePublicationSummary) { value.Mode = "future_mode" },
		func(value *RclonePublicationSummary) { value.State = "raw_provider_error" },
		func(value *RclonePublicationSummary) { value.ReasonCode = "raw_provider_error" },
		func(value *RclonePublicationSummary) { value.ConsistencyClass = "eventually_consistent" },
		func(value *RclonePublicationSummary) { value.HashFidelity = "metadata_only" },
		func(value *RclonePublicationSummary) { value.APICostClass = "unbounded" },
		func(value *RclonePublicationSummary) { value.EncryptionProfile = "bucket_default" },
		func(value *RclonePublicationSummary) { value.KMSKeyStatus = "unknown" },
		func(value *RclonePublicationSummary) { value.RollbackCapability = "ordinary_legacy" },
		func(value *RclonePublicationSummary) { value.TaskRevision = "01" },
		func(value *RclonePublicationSummary) { value.BindingRevision = "-1" },
		func(value *RclonePublicationSummary) { value.CapabilityRevision = "" },
		func(value *RclonePublicationSummary) { value.EstimatedReadBytes = "1.5" },
	}
	for index, mutate := range tests {
		value := base
		mutate(&value)
		if err := value.Validate(); err == nil {
			t.Fatalf("unsafe summary case %d accepted: %+v", index, value)
		}
	}
}

func TestSafeRclonePublicationSummaryFailsClosedForUnknownWireValues(t *testing.T) {
	unsafe := validRclonePublicationSummaryForTest()
	unsafe.Mode = "future_mode"
	unsafe.State = "future_state"
	unsafe.ReasonCode = "future_reason"
	unsafe.EncryptionProfile = "future_encryption"
	unsafe.KMSKeyStatus = "future_kms_status"
	unsafe.RollbackCapability = "future_rollback"

	got := SafeRclonePublicationSummary(unsafe)
	if got.Mode != PublicationNativeObjectVersions || got.State != RcloneStateBlocked || got.ReasonCode != RcloneReasonUnsupportedProfile {
		t.Fatalf("unsafe summary did not fail closed: %+v", got)
	}
	if got.EncryptionProfile != RcloneEncryptionSSEKMS || got.KMSKeyStatus != RcloneKMSBlocked || got.RollbackCapability != RcloneRollbackPreparationOnly {
		t.Fatalf("unsafe capability projection became permissive: %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("safe blocked projection invalid: %v", err)
	}
}

func TestRcloneVersioningRequestsAcceptOnlyManagedModesAndChoices(t *testing.T) {
	preflight := RcloneVersioningPreflightRequest{
		TaskID: 7, ExpectedTaskRevision: 9, RequestedMode: PublicationVersionedPrefix,
	}
	if err := preflight.Validate(); err != nil {
		t.Fatalf("valid portable preflight rejected: %v", err)
	}
	preflight.RequestedMode = PublicationNativeObjectVersions
	if err := preflight.Validate(); err != nil {
		t.Fatalf("valid native preflight rejected: %v", err)
	}
	preflight.RequestedMode = PublicationLegacyMutable
	if err := preflight.Validate(); err == nil {
		t.Fatal("legacy mode accepted for managed Rclone preflight")
	}

	activation := RcloneVersioningActivationRequest{
		TaskID: 7, ExpectedTaskRevision: 9, PreflightID: "0123456789abcdef0123456789abcdef",
		MigrationChoice: RcloneFirstNewPoint,
	}
	if err := activation.Validate(); err != nil {
		t.Fatalf("valid activation rejected: %v", err)
	}
	activation.MigrationChoice = RcloneImportedBaseline
	if err := activation.Validate(); err != nil {
		t.Fatalf("valid baseline activation rejected: %v", err)
	}
	activation.MigrationChoice = "metadata_only"
	if err := activation.Validate(); err == nil {
		t.Fatal("metadata-only baseline accepted")
	}
}

func TestRclonePublicationLineageUsesTaglessManagedModes(t *testing.T) {
	for _, mode := range []TaskPublicationMode{PublicationVersionedPrefix, PublicationNativeObjectVersions} {
		lineage := PublicationLineageV1{
			Version: 1, TaskRepositoryLinkID: "0123456789abcdef0123456789abcdef",
			TaskID: 7, TaskRunID: 8, Trigger: "manual", PublicationMode: string(mode),
			PointCodecVersion: 1, TagCodecVersion: 0,
			StartedAt:       time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC),
			PreparedAt:      time.Date(2026, 7, 16, 1, 0, 1, 0, time.UTC),
			PointDeadlineAt: time.Date(2026, 7, 16, 2, 0, 0, 0, time.UTC),
		}
		if _, err := EncodePublicationLineage(lineage); err != nil {
			t.Fatalf("managed Rclone lineage mode %q rejected: %v", mode, err)
		}
	}
}

func TestRclonePublicationFailureCodesAreDurableAndClosed(t *testing.T) {
	for _, code := range []PublicationFailureCode{
		FailureSourceDrift, FailureExternalWriterDetected, FailureUnexpectedVersion,
		FailureMarkerMismatch, FailureManifestMismatch,
	} {
		if err := ValidatePublicationFailureCode(code); err != nil {
			t.Fatalf("Rclone failure code %q rejected: %v", code, err)
		}
	}
	if err := ValidatePublicationFailureCode("raw_aws_error"); err == nil {
		t.Fatal("unregistered raw provider failure code accepted")
	}
}

func TestRcloneVersioningBindingRequestsAreClosedAndWriteOnly(t *testing.T) {
	setupID := "0123456789abcdef0123456789abcdef"
	portable := RclonePortableBindingRequest{
		TaskID: 7, ExpectedTaskRevision: 9, ExpectedBindingRevision: 0, SetupID: setupID,
		TargetRemote: "backup", ManagedRootLocator: "backup:managed/v1",
		BoundConfig: "[backup]\ntype = b2\naccount = FAKE_B2_ACCOUNT_FOR_TEST_ONLY\nkey = FAKE_B2_KEY_FOR_TEST_ONLY\n",
	}
	if err := portable.Validate(); err != nil {
		t.Fatalf("valid portable binding request rejected: %v", err)
	}

	native := RcloneNativeBindingRequest{
		TaskID: 7, ExpectedTaskRevision: 9, ExpectedBindingRevision: 0, SetupID: setupID,
		Region: "us-east-1", Bucket: "xirang-managed-test", ManagedPrefix: "managed/v1/",
		RoleARN:           "arn:aws:iam::123456789012:role/xirang-backup-test",
		Bootstrap:         RcloneNativeBootstrapInput{Mode: RcloneBootstrapWorkloadChain},
		EncryptionProfile: RcloneEncryptionSSES3,
	}
	if err := native.Validate(); err != nil {
		t.Fatalf("valid native workload binding request rejected: %v", err)
	}

	native.Bootstrap = RcloneNativeBootstrapInput{
		Mode: RcloneBootstrapStaticSTS, AccessKeyID: "FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY",
		SecretAccessKey: "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY",
	}
	native.EncryptionProfile = RcloneEncryptionSSEKMS
	native.KMSKeyARN = "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-KEY-FOR-TEST-ONLY"
	if err := native.Validate(); err != nil {
		t.Fatalf("valid native static/KMS binding request rejected: %v", err)
	}

	encoded, err := json.Marshal(struct {
		Portable RclonePortableBindingRequest `json:"portable"`
		Native   RcloneNativeBindingRequest   `json:"native"`
	}{Portable: portable, Native: native})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		portable.TargetRemote, portable.ManagedRootLocator, portable.BoundConfig,
		native.Region, native.Bucket, native.ManagedPrefix, native.RoleARN,
		native.Bootstrap.AccessKeyID, native.Bootstrap.SecretAccessKey, native.KMSKeyARN,
	} {
		if secret != "" && strings.Contains(string(encoded), secret) {
			t.Fatalf("write-only Rclone binding input leaked through JSON: %s", encoded)
		}
	}

	invalid := native
	invalid.Bootstrap = RcloneNativeBootstrapInput{Mode: RcloneBootstrapWorkloadChain, AccessKeyID: "FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("workload bootstrap accepted static credential fields")
	}
	invalid = native
	invalid.EncryptionProfile = RcloneEncryptionSSES3
	if err := invalid.Validate(); err == nil {
		t.Fatal("SSE-S3 request accepted a KMS key ARN")
	}
}

func TestRcloneVersioningWorkflowRequestsAndResultsValidateSafeFacts(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	setup := RcloneBindingSetupRequest{TaskID: 7, ExpectedTaskRevision: 9}
	if err := setup.Validate(); err != nil {
		t.Fatalf("valid setup request rejected: %v", err)
	}

	setupResult := RcloneBindingSetupResult{
		SetupID: "0123456789abcdef0123456789abcdef", ExpiresAt: now.Add(30 * time.Minute),
		ExternalID: "FAKE_EXTERNAL_ID_FOR_TEST_ONLY",
	}
	if err := setupResult.Validate(true); err != nil {
		t.Fatalf("valid native setup result rejected: %v", err)
	}
	if err := (RcloneBindingSetupResult{SetupID: setupResult.SetupID, ExpiresAt: setupResult.ExpiresAt}).Validate(false); err != nil {
		t.Fatalf("valid portable setup result rejected: %v", err)
	}

	summary := validRclonePublicationSummaryForTest()
	preflight := RcloneVersioningPreflightResult{
		PreflightID: setupResult.SetupID, ExpiresAt: setupResult.ExpiresAt, Summary: summary,
	}
	if err := preflight.Validate(); err != nil {
		t.Fatalf("valid preflight result rejected: %v", err)
	}
	activation := RcloneVersioningActivationResult{Summary: summary, MigrationChoice: RcloneFirstNewPoint}
	if err := activation.Validate(); err != nil {
		t.Fatalf("valid activation result rejected: %v", err)
	}

	clean := RcloneVersioningCleanRollbackRequest{TaskID: 7, ExpectedTaskRevision: 9, ExpectedBindingRevision: 2}
	prepare := RcloneVersioningRollbackPreparationRequest{TaskID: 7, ExpectedTaskRevision: 9, ExpectedBindingRevision: 2}
	if err := clean.Validate(); err != nil {
		t.Fatalf("valid clean rollback request rejected: %v", err)
	}
	if err := prepare.Validate(); err != nil {
		t.Fatalf("valid rollback preparation request rejected: %v", err)
	}
	if err := (RcloneVersioningRollbackResult{Summary: summary}).Validate(); err != nil {
		t.Fatalf("valid rollback result rejected: %v", err)
	}

	preflight.ExpiresAt = preflight.ExpiresAt.In(time.FixedZone("not-utc", 3600))
	if err := preflight.Validate(); err == nil {
		t.Fatal("non-UTC preflight expiry accepted")
	}
}
