package provider

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func validRcloneAttemptForTest(mode backupasset.TaskPublicationMode) RcloneAttemptV1 {
	value := RcloneAttemptV1{
		SchemaVersion: 1, LayoutVersion: 1, MinimumRuntimeRevision: 1,
		Provider:     backupasset.ProviderRclone,
		RepositoryID: strings.Repeat("a", 32), TaskRepositoryLinkID: strings.Repeat("b", 32),
		RecoveryPointID: strings.Repeat("c", 32), AttemptID: strings.Repeat("d", 32),
		TaskID: 7, TaskRunID: 8, Trigger: "manual", PublicationMode: mode,
		CaptureStartedAt:     time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC),
		PreparedAt:           time.Date(2026, 7, 16, 1, 0, 1, 0, time.UTC),
		PointDeadlineAt:      time.Date(2026, 7, 16, 2, 0, 0, 0, time.UTC),
		ExpectedTaskRevision: 1, BindingRevision: 2, ConfigRevision: 3,
		ConfigDigest: strings.Repeat("1", 64), CapabilityRevision: 4, CredentialRevision: 5,
		PreflightID: strings.Repeat("e", 32), PreflightRevision: 6, PreflightDigest: strings.Repeat("2", 64),
		ManifestSchemaRevision: 1, ManifestLimitsRevision: 1, ManifestLimitsDigest: strings.Repeat("3", 64),
		RepositoryIdentityDigest: strings.Repeat("4", 64), ManagedRootIdentityDigest: strings.Repeat("5", 64),
		ChildFenceDigest: strings.Repeat("6", 64), LegacyOriginEvidenceDigest: strings.Repeat("7", 64),
	}
	if mode == backupasset.PublicationVersionedPrefix {
		value.Portable = &RclonePortableAttemptV1{
			AttemptComponent: value.RecoveryPointID + "." + value.AttemptID,
			DataComponent:    "data", ControlComponent: "control", AttemptMarkerDigest: strings.Repeat("8", 64),
			ExpectedConsistencyClass: string(backupasset.RcloneConsistencyObservationallyStable),
			ExpectedHashFidelity:     string(backupasset.RcloneHashDownloadVerifiedBytes),
		}
	} else {
		value.Native = &RcloneNativeAttemptV1{
			ProfileCode:          RcloneNativeAWSS3GeneralPurposeV1,
			RegionIdentityDigest: strings.Repeat("8", 64), BucketIdentityDigest: strings.Repeat("9", 64),
			ManagedPrefixIdentityDigest: strings.Repeat("a", 64), RoleSessionIdentityDigest: strings.Repeat("b", 64),
			SessionExpiresAt: time.Date(2026, 7, 16, 2, 5, 0, 0, time.UTC),
			VersioningDigest: strings.Repeat("c", 64), LifecycleDigest: strings.Repeat("d", 64),
			CapabilityStableObservedAt: time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC),
			EncryptionProfile:          RcloneNativeSSES3V1, BucketEncryptionDigest: strings.Repeat("e", 64),
			B0VersionGraphDigest:      strings.Repeat("2", 64),
			StartMarkerIdentityDigest: strings.Repeat("3", 64), CanaryIdentityDigest: strings.Repeat("4", 64),
		}
	}
	return value
}

func TestRcloneNativeAttemptEncryptionFieldsAreClosedByProfile(t *testing.T) {
	sses3 := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
	if err := sses3.Validate(); err != nil {
		t.Fatalf("SSE-S3 attempt rejected: %v", err)
	}
	sses3.Native.ActiveKeyDigest = strings.Repeat("f", 64)
	if err := sses3.Validate(); err == nil {
		t.Fatal("SSE-S3 attempt accepted a KMS field")
	}

	kms := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
	kms.Native.EncryptionProfile = RcloneNativeSSEKMSV1
	kms.Native.ActiveKeyDigest = strings.Repeat("f", 64)
	kms.Native.RetainedReadKeySetDigest = strings.Repeat("1", 64)
	kms.Native.KMSCapabilityRevision = 1
	if err := kms.Validate(); err != nil {
		t.Fatalf("SSE-KMS attempt rejected: %v", err)
	}
	kms.Native.KMSCapabilityRevision = 0
	if err := kms.Validate(); err == nil {
		t.Fatal("SSE-KMS attempt accepted missing KMS capability revision")
	}
}

func TestRcloneNativeCommitKMSFieldsAreAllEmptyOrAllPresent(t *testing.T) {
	sses3 := validRcloneCommitForTest(backupasset.PublicationNativeObjectVersions)
	sses3.Native.ActiveKeyDigest = ""
	sses3.Native.RetainedReadKeySetDigest = ""
	sses3.Native.KMSCapabilityRevision = 0
	if err := sses3.Validate(); err != nil {
		t.Fatalf("SSE-S3 commit rejected: %v", err)
	}
	sses3.Native.ActiveKeyDigest = strings.Repeat("7", 64)
	if err := sses3.Validate(); err == nil {
		t.Fatal("native commit accepted partial KMS evidence")
	}
}

func validRcloneCommitForTest(mode backupasset.TaskPublicationMode) RcloneCommitV1 {
	value := RcloneCommitV1{
		SchemaVersion: 1, LayoutVersion: 1, MinimumRuntimeRevision: 1,
		RepositoryID: strings.Repeat("a", 32), TaskRepositoryLinkID: strings.Repeat("b", 32),
		RecoveryPointID: strings.Repeat("c", 32), AttemptID: strings.Repeat("d", 32), PublicationMode: mode,
		PointDeadlineAt:     time.Date(2026, 7, 16, 2, 0, 0, 0, time.UTC),
		ProviderCommittedAt: time.Date(2026, 7, 16, 1, 30, 0, 0, time.UTC),
		ManifestIndexDigest: strings.Repeat("1", 64), ManifestChunkDigests: []string{strings.Repeat("2", 64), strings.Repeat("3", 64)},
		ManifestEntryCount: 2, LogicalBytes: 1024,
		SourceObservationDigest: strings.Repeat("4", 64), DestinationObservationDigest: strings.Repeat("5", 64),
		ContentProofDigest: strings.Repeat("6", 64), FidelityEvidenceDigest: strings.Repeat("7", 64),
		CostEvidenceDigest: strings.Repeat("8", 64), CapabilityEvidenceDigest: strings.Repeat("9", 64),
		ChildFenceDigest: strings.Repeat("a", 64),
	}
	if mode == backupasset.PublicationVersionedPrefix {
		value.Portable = &RclonePortableCommitV1{
			AttemptIdentityDigest: strings.Repeat("b", 64), ControlIdentityDigest: strings.Repeat("c", 64),
			DataIdentityDigest: strings.Repeat("d", 64), AttemptMarkerDigest: strings.Repeat("e", 64),
			CommitComponent: "commit.json", CommitPayloadDigest: strings.Repeat("f", 64),
			CommitAuthenticationDigest: strings.Repeat("1", 64), ConsistencyEvidenceDigest: strings.Repeat("2", 64),
			HashEvidenceDigest: strings.Repeat("3", 64), DownloadVerifiedBytes: 1024,
		}
	} else {
		value.Native = &RcloneNativeCommitV1{
			CommitKey: "FAKE_PRIVATE_COMMIT_KEY_FOR_TEST_ONLY", CommitVersionID: "FAKE_OPAQUE_VERSION_ID_FOR_TEST_ONLY",
			CommitContentDigest: strings.Repeat("b", 64), ManifestControlGraphDigest: strings.Repeat("c", 64),
			PointViewDigest: strings.Repeat("d", 64), MutationLedgerDigest: strings.Repeat("e", 64),
			B0VersionGraphDigest: strings.Repeat("f", 64), B1VersionGraphDigest: strings.Repeat("1", 64),
			ExactReadProofDigest: strings.Repeat("2", 64), VersioningDigest: strings.Repeat("3", 64),
			LifecycleDigest: strings.Repeat("4", 64), BucketEncryptionDigest: strings.Repeat("5", 64),
			EncryptionEvidenceDigest: strings.Repeat("6", 64), ActiveKeyDigest: strings.Repeat("7", 64),
			RetainedReadKeySetDigest: strings.Repeat("8", 64), RoleSessionIdentityDigest: strings.Repeat("9", 64),
			CapabilityRevision: 4, CredentialRevision: 5, KMSCapabilityRevision: 6,
			SessionExpiresAt: time.Date(2026, 7, 16, 2, 5, 0, 0, time.UTC),
		}
	}
	return value
}

func TestRcloneTaggedAttemptsRoundTripAndRejectEveryCrossVariant(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		want := validRcloneAttemptForTest(mode)
		encoded, err := EncodePublicationAttempt(NewRclonePublicationAttempt(want))
		if err != nil {
			t.Fatalf("encode %q attempt: %v", mode, err)
		}
		got, err := DecodeRcloneAttemptV1(encoded)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("attempt %q round trip got=%+v err=%v want=%+v", mode, got, err, want)
		}
		for name, raw := range map[string]string{
			"null":          `{"provider":"rclone","version":1,"rclone":null}`,
			"restic branch": strings.TrimSuffix(encoded, "}") + `,"restic":{}}`,
			"rsync branch":  strings.TrimSuffix(encoded, "}") + `,"rsync_tree":{}}`,
			"unknown":       strings.TrimSuffix(encoded, "}") + `,"future":{}}`,
			"duplicate":     strings.Replace(encoded, `"provider":"rclone"`, `"provider":"rclone","provider":"rclone"`, 1),
			"trailing":      encoded + ` {}`,
		} {
			t.Run(string(mode)+"/"+name, func(t *testing.T) {
				if _, err := DecodePublicationAttempt(raw); err == nil {
					t.Fatalf("unsafe Rclone attempt accepted: %s", raw)
				}
			})
		}
		cross := want
		if mode == backupasset.PublicationVersionedPrefix {
			cross.Native = validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions).Native
		} else {
			cross.Portable = validRcloneAttemptForTest(backupasset.PublicationVersionedPrefix).Portable
		}
		if err := NewRclonePublicationAttempt(cross).Validate(); err == nil {
			t.Fatalf("cross-variant %q attempt accepted", mode)
		}
	}
}

func TestRcloneTaggedCommitsHideNativeLocatorAndRejectCrossVariants(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		want := validRcloneCommitForTest(mode)
		encoded, err := EncodeProviderCommit(NewRcloneProviderCommit(want))
		if err != nil {
			t.Fatalf("encode %q commit: %v", mode, err)
		}
		if strings.Contains(encoded, "FAKE_PRIVATE_COMMIT_KEY") || strings.Contains(encoded, "FAKE_OPAQUE_VERSION_ID") {
			t.Fatalf("native private locator leaked into provider commit: %s", encoded)
		}
		got, err := DecodeRcloneCommitV1(encoded)
		if err != nil {
			t.Fatalf("decode %q commit: %v", mode, err)
		}
		want.Native = withoutPrivateRcloneNativeLocator(want.Native)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("commit %q round trip got=%+v want=%+v", mode, got, want)
		}
		cross := validRcloneCommitForTest(mode)
		if mode == backupasset.PublicationVersionedPrefix {
			cross.Native = validRcloneCommitForTest(backupasset.PublicationNativeObjectVersions).Native
		} else {
			cross.Portable = validRcloneCommitForTest(backupasset.PublicationVersionedPrefix).Portable
		}
		if err := NewRcloneProviderCommit(cross).Validate(); err == nil {
			t.Fatalf("cross-variant %q commit accepted", mode)
		}
	}
}

func TestRcloneManifestPrepareAndReconcileBranchesAreClosed(t *testing.T) {
	portableAttempt := validRcloneAttemptForTest(backupasset.PublicationVersionedPrefix)
	attempt := NewRclonePublicationAttempt(portableAttempt)
	input := &RclonePublicationInput{
		ManifestLimits:  ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 10, MaxRecordBytes: 1024, MaxDepth: 10},
		PortableRequest: &RclonePortablePublicationRequest{Attempt: portableAttempt},
	}
	request := PublicationPrepareRequest{Attempt: attempt, RcloneInput: input}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid Rclone prepare request rejected: %v", err)
	}
	request.ResticInput = &ResticBackupInput{Source: "/source"}
	if err := request.Validate(); err == nil {
		t.Fatal("mixed Rclone/Restic prepare request accepted")
	}

	nativeAttempt := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
	nativeInput := &RclonePublicationInput{
		ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 10, MaxRecordBytes: 1024, MaxDepth: 10},
		NativeRequest:  &RcloneNativePublicationRequest{Attempt: nativeAttempt},
	}
	if err := (PublicationPrepareRequest{Attempt: NewRclonePublicationAttempt(nativeAttempt), RcloneInput: nativeInput}).Validate(); err != nil {
		t.Fatalf("valid native Rclone prepare request rejected: %v", err)
	}
	nativeInput.PortableRequest = &RclonePortablePublicationRequest{Attempt: nativeAttempt}
	if err := (PublicationPrepareRequest{Attempt: NewRclonePublicationAttempt(nativeAttempt), RcloneInput: nativeInput}).Validate(); err == nil {
		t.Fatal("mixed portable/native Rclone prepare input accepted")
	}

	manifest := RcloneManifestV1{
		ManifestIndexDigest: strings.Repeat("1", 64), ManifestChunkDigests: []string{strings.Repeat("2", 64), strings.Repeat("3", 64)},
		EntryCount: 2, LogicalBytes: 1024, FidelityEvidenceDigest: strings.Repeat("7", 64),
	}
	result := ManifestResult{Provider: backupasset.ProviderRclone, Version: taggedPublicationSchemaV1, Rclone: &manifest}
	if got, err := result.RcloneManifest(); err != nil || !reflect.DeepEqual(got, manifest) {
		t.Fatalf("Rclone manifest branch got=%+v err=%v", got, err)
	}
	result.RsyncTree = &RsyncTreeManifestV1{}
	if _, err := result.RcloneManifest(); err == nil {
		t.Fatal("mixed Rclone/Rsync manifest result accepted")
	}

	reconcile := RcloneReconcileV1{State: RcloneReconcileProviderCommitted, Commit: ptrRcloneCommitForTest(validRcloneCommitForTest(backupasset.PublicationVersionedPrefix)), Manifest: &manifest}
	closed := PublicationReconcileResult{Rclone: &reconcile}
	if got, err := closed.RcloneResult(); err != nil || got.State != RcloneReconcileProviderCommitted {
		t.Fatalf("Rclone reconcile branch got=%+v err=%v", got, err)
	}
	closed.ResticObservations = []ResticSnapshotObservation{{}}
	if _, err := closed.RcloneResult(); err == nil {
		t.Fatal("mixed Rclone/Restic reconciliation accepted")
	}
}

func TestAddingRcloneBranchKeepsResticWireBytesCompatible(t *testing.T) {
	value := ResticCommitV1{
		RepositoryIdentity: NativeResticIdentityPrefix + strings.Repeat("f", 64), NativePointID: strings.Repeat("a", 64),
		CaptureStartedAt: time.Date(2026, 7, 16, 3, 0, 0, 0, time.UTC), CaptureFinishedAt: time.Date(2026, 7, 16, 3, 0, 1, 0, time.UTC),
		FilesProcessed: 7, LogicalBytes: 16384,
	}
	got, err := EncodeProviderCommit(NewResticProviderCommit(value))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"provider":"restic","version":1,"restic":{"repository_identity":"restic-native:v1:` + strings.Repeat("f", 64) + `","native_point_id":"` + strings.Repeat("a", 64) + `","capture_started_at":"2026-07-16T03:00:00Z","capture_finished_at":"2026-07-16T03:00:01Z","files_processed":7,"logical_bytes":16384}}`
	if got != want {
		t.Fatalf("legacy Restic commit wire drifted:\n got: %s\nwant: %s", got, want)
	}
}

func withoutPrivateRcloneNativeLocator(value *RcloneNativeCommitV1) *RcloneNativeCommitV1 {
	if value == nil {
		return nil
	}
	copy := *value
	copy.CommitKey = ""
	copy.CommitVersionID = ""
	return &copy
}

func ptrRcloneCommitForTest(value RcloneCommitV1) *RcloneCommitV1 { return &value }
