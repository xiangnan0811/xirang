package backupasset

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRepositoryStateTransitions(t *testing.T) {
	statuses := []RepositoryStatus{
		RepositoryConnecting,
		RepositoryOnline,
		RepositoryDegraded,
		RepositoryOffline,
		RepositoryDisconnected,
		RepositoryPurging,
		RepositoryPurgeBlocked,
	}
	allowed := map[[2]RepositoryStatus]bool{
		{RepositoryConnecting, RepositoryOnline}:       true,
		{RepositoryConnecting, RepositoryDegraded}:     true,
		{RepositoryConnecting, RepositoryOffline}:      true,
		{RepositoryConnecting, RepositoryDisconnected}: true,
		{RepositoryOnline, RepositoryDegraded}:         true,
		{RepositoryOnline, RepositoryOffline}:          true,
		{RepositoryOnline, RepositoryDisconnected}:     true,
		{RepositoryOnline, RepositoryPurging}:          true,
		{RepositoryDegraded, RepositoryOnline}:         true,
		{RepositoryDegraded, RepositoryOffline}:        true,
		{RepositoryDegraded, RepositoryDisconnected}:   true,
		{RepositoryDegraded, RepositoryPurging}:        true,
		{RepositoryOffline, RepositoryConnecting}:      true,
		{RepositoryOffline, RepositoryOnline}:          true,
		{RepositoryOffline, RepositoryDegraded}:        true,
		{RepositoryOffline, RepositoryDisconnected}:    true,
		{RepositoryOffline, RepositoryPurging}:         true,
		{RepositoryDisconnected, RepositoryConnecting}: true,
		{RepositoryDisconnected, RepositoryPurging}:    true,
		{RepositoryPurging, RepositoryPurgeBlocked}:    true,
		{RepositoryPurgeBlocked, RepositoryPurging}:    true,
	}

	for _, from := range statuses {
		for _, to := range statuses {
			err := ValidateRepositoryTransition(from, to)
			wantAllowed := from == to || allowed[[2]RepositoryStatus{from, to}]
			if wantAllowed && err != nil {
				t.Fatalf("expected %q -> %q to be allowed: %v", from, to, err)
			}
			if !wantAllowed && !errors.Is(err, ErrInvalidState) {
				t.Fatalf("expected %q -> %q to fail with ErrInvalidState, got %v", from, to, err)
			}
		}
	}

	if err := ValidateRepositoryTransition("future_state", RepositoryOnline); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unknown source state must fail closed, got %v", err)
	}
}

func TestImmutableRecoveryPointTransitions(t *testing.T) {
	states := []RecoveryPointState{
		RecoveryPointPreparing,
		RecoveryPointVerifying,
		RecoveryPointCommitted,
		RecoveryPointDegraded,
		RecoveryPointExpiring,
		RecoveryPointExpired,
		RecoveryPointFailed,
		RecoveryPointPurgeBlocked,
	}
	allowed := map[[2]RecoveryPointState]bool{
		{RecoveryPointPreparing, RecoveryPointVerifying}:   true,
		{RecoveryPointPreparing, RecoveryPointFailed}:      true,
		{RecoveryPointVerifying, RecoveryPointCommitted}:   true,
		{RecoveryPointVerifying, RecoveryPointFailed}:      true,
		{RecoveryPointCommitted, RecoveryPointDegraded}:    true,
		{RecoveryPointCommitted, RecoveryPointExpiring}:    true,
		{RecoveryPointDegraded, RecoveryPointCommitted}:    true,
		{RecoveryPointDegraded, RecoveryPointExpiring}:     true,
		{RecoveryPointExpiring, RecoveryPointExpired}:      true,
		{RecoveryPointExpiring, RecoveryPointPurgeBlocked}: true,
		{RecoveryPointPurgeBlocked, RecoveryPointExpiring}: true,
	}

	for _, from := range states {
		profile := validImmutableProfile()
		profile.State = from
		for _, to := range states {
			err := ValidateRecoveryPointTransition(profile, to)
			wantAllowed := from == to || allowed[[2]RecoveryPointState{from, to}]
			if wantAllowed && err != nil {
				t.Fatalf("expected %q -> %q to be allowed: %v", from, to, err)
			}
			if !wantAllowed && !errors.Is(err, ErrInvalidState) {
				t.Fatalf("expected %q -> %q to fail with ErrInvalidState, got %v", from, to, err)
			}
		}
	}
}

func TestMutableHeadLifecyclePreservesStableID(t *testing.T) {
	now := time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)
	head := MutableHead{
		ID:                strings.Repeat("a", 32),
		RepositoryID:      strings.Repeat("b", 32),
		State:             RecoveryPointObserved,
		SourceFingerprint: "first",
		ObservedAt:        now.Add(-time.Hour),
		Availability:      PhysicalOnline,
	}

	updated, err := ApplyMutableObservation(head, MutableObservation{
		SourceFingerprint: "second",
		ObservedAt:        now,
		Availability:      PhysicalOnline,
		CatalogGeneration: strings.Repeat("c", 32),
	})
	if err != nil {
		t.Fatalf("apply mutable observation: %v", err)
	}
	if updated.ID != head.ID {
		t.Fatalf("mutable-head ID changed: got %q want %q", updated.ID, head.ID)
	}
	if updated.SourceFingerprint != "second" || !updated.ObservedAt.Equal(now) {
		t.Fatalf("observation was not refreshed: %+v", updated)
	}
	if head.SourceFingerprint != "first" {
		t.Fatal("input mutable head was mutated in place")
	}
}

func TestMutableHeadRetirementAndPhysicalPurgeAreDistinct(t *testing.T) {
	now := time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)
	base := validMutableProfile()

	retired := base
	retired.State = RecoveryPointRetired
	retired.RetirementReason = RetirementCutover
	retired.RetiredAt = &now
	retired.HasEncryptedRollbackLocator = true
	if err := ValidateRecoveryPointProfile(retired); err != nil {
		t.Fatalf("valid non-destructive retirement rejected: %v", err)
	}
	if err := ValidateRecoveryPointTransition(retired, RecoveryPointObserved); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("retired mutable head must not revive: %v", err)
	}
	if err := ValidateRecoveryPointTransition(retired, RecoveryPointExpiring); err != nil {
		t.Fatalf("retired mutable head must enter explicit purge lifecycle: %v", err)
	}

	if err := ValidateRecoveryPointTransition(base, RecoveryPointExpired); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("physical purge must not skip the expiring transition: %v", err)
	}
}

func TestMutableRecoveryPointTransitionsCoverEveryStatePair(t *testing.T) {
	states := []RecoveryPointState{
		RecoveryPointObserved,
		RecoveryPointRetired,
		RecoveryPointExpiring,
		RecoveryPointExpired,
		RecoveryPointPurgeBlocked,
	}
	allowed := map[[2]RecoveryPointState]bool{
		{RecoveryPointObserved, RecoveryPointRetired}:      true,
		{RecoveryPointObserved, RecoveryPointExpiring}:     true,
		{RecoveryPointRetired, RecoveryPointExpiring}:      true,
		{RecoveryPointExpiring, RecoveryPointExpired}:      true,
		{RecoveryPointExpiring, RecoveryPointPurgeBlocked}: true,
		{RecoveryPointPurgeBlocked, RecoveryPointExpiring}: true,
	}

	for _, from := range states {
		profile := validMutableProfile()
		profile.State = from
		if from == RecoveryPointRetired {
			now := time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)
			profile.RetirementReason = RetirementCutover
			profile.RetiredAt = &now
			profile.HasEncryptedRollbackLocator = true
		}
		for _, to := range states {
			err := ValidateRecoveryPointTransition(profile, to)
			wantAllowed := from == to || allowed[[2]RecoveryPointState{from, to}]
			if wantAllowed && err != nil {
				t.Fatalf("expected mutable %q -> %q to be allowed: %v", from, to, err)
			}
			if !wantAllowed && !errors.Is(err, ErrInvalidState) {
				t.Fatalf("expected mutable %q -> %q to fail with ErrInvalidState, got %v", from, to, err)
			}
		}
	}
}

func TestRecoveryPointProfileRejectsCrossEnumDrift(t *testing.T) {
	tests := []struct {
		name    string
		profile RecoveryPointProfile
	}{
		{
			name: "mutable semantics with immutable version mode",
			profile: func() RecoveryPointProfile {
				p := validMutableProfile()
				p.VersionMode = VersionHardlinkTree
				return p
			}(),
		},
		{
			name: "mutable head committed",
			profile: func() RecoveryPointProfile {
				p := validMutableProfile()
				p.State = RecoveryPointCommitted
				return p
			}(),
		},
		{
			name: "immutable point uses mutable immutability",
			profile: func() RecoveryPointProfile {
				p := validImmutableProfile()
				p.Immutability = ImmutabilityMutable
				return p
			}(),
		},
		{
			name: "retirement metadata on immutable point",
			profile: func() RecoveryPointProfile {
				p := validImmutableProfile()
				p.RetirementReason = RetirementWithdrawn
				return p
			}(),
		},
		{
			name: "observed time on immutable point",
			profile: func() RecoveryPointProfile {
				p := validImmutableProfile()
				now := time.Now().UTC()
				p.ObservedAt = &now
				return p
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateRecoveryPointProfile(tt.profile); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("expected ErrInvalidState, got %v", err)
			}
		})
	}

	if err := ValidateRecoveryPointProfile(validMutableProfile()); err != nil {
		t.Fatalf("valid mutable profile rejected: %v", err)
	}
	if err := ValidateRecoveryPointProfile(validImmutableProfile()); err != nil {
		t.Fatalf("valid immutable profile rejected: %v", err)
	}
}

func TestPublicationModeMapping(t *testing.T) {
	tests := []struct {
		name      string
		provider  ProviderKind
		mode      TaskPublicationMode
		wantMode  VersionMode
		wantPoint PointVersionSemantics
		wantState RecoveryPointState
		wantErr   error
	}{
		{"restic snapshot", ProviderRestic, PublicationNativeSnapshot, VersionNativeSnapshot, PointNativeSnapshot, RecoveryPointPreparing, nil},
		{"rsync mutable", ProviderRsync, PublicationLegacyMutable, VersionMutableHead, PointMutableHead, RecoveryPointObserved, nil},
		{"rclone mutable", ProviderRclone, PublicationLegacyMutable, VersionMutableHead, PointMutableHead, RecoveryPointObserved, nil},
		{"rsync hardlink", ProviderRsync, PublicationVersionedHardlink, VersionHardlinkTree, PointXirangManifest, RecoveryPointPreparing, nil},
		{"rsync full copy", ProviderRsync, PublicationVersionedFullCopy, VersionFullCopyTree, PointXirangManifest, RecoveryPointPreparing, nil},
		{"rclone prefix", ProviderRclone, PublicationVersionedPrefix, VersionVersionedPrefix, PointXirangManifest, RecoveryPointPreparing, nil},
		{"rclone native versions", ProviderRclone, PublicationNativeObjectVersions, VersionNativeObjectVersions, PointXirangManifest, RecoveryPointPreparing, nil},
		{"command no contract", ProviderCommand, PublicationLegacyMutable, "", "", "", ErrCapabilityUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMode, gotPoint, gotState, err := MapPublicationMode(tt.provider, tt.mode)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotMode != tt.wantMode || gotPoint != tt.wantPoint || gotState != tt.wantState {
				t.Fatalf("mapping got (%q,%q,%q), want (%q,%q,%q)", gotMode, gotPoint, gotState, tt.wantMode, tt.wantPoint, tt.wantState)
			}
		})
	}
}

func TestPublicationModeMappingRequiresNativeSnapshotForRestic(t *testing.T) {
	mode, semantics, state, err := MapPublicationMode(ProviderRestic, PublicationNativeSnapshot)
	if err != nil {
		t.Fatalf("map native Restic publication: %v", err)
	}
	if mode != VersionNativeSnapshot || semantics != PointNativeSnapshot || state != RecoveryPointPreparing {
		t.Fatalf("unexpected Restic mapping: %s %s %s", mode, semantics, state)
	}
	if _, _, _, err := MapPublicationMode(ProviderRestic, PublicationNativeObjectVersions); err == nil {
		t.Fatal("Restic accepted the Rclone native-object-version mode")
	}
}

func TestPublicationLeaseHolderIsValid(t *testing.T) {
	if !validLeaseHolderTypes[LeaseHolderPointPublication] {
		t.Fatal("point_publication lease holder is not valid")
	}
}

func TestLifecycleClosedEnumsAndValidators(t *testing.T) {
	for _, scope := range []RetentionPolicyScopeKind{RetentionPolicyScopeRepository, RetentionPolicyScopeTaskLink} {
		if err := ValidateRetentionPolicyScope(scope, strings.Repeat("a", 32)); err != nil {
			t.Fatalf("valid retention scope %q rejected: %v", scope, err)
		}
	}
	for _, status := range []RetentionPolicyStatus{RetentionPolicyActive, RetentionPolicyDeleted} {
		if err := ValidateRetentionPolicyStatus(status); err != nil {
			t.Fatalf("valid retention policy status %q rejected: %v", status, err)
		}
	}
	for _, holdType := range []RecoveryPointHoldType{RecoveryPointHoldOperational, RecoveryPointHoldLegal} {
		if err := ValidateRecoveryPointHoldType(holdType); err != nil {
			t.Fatalf("valid hold type %q rejected: %v", holdType, err)
		}
	}
	for _, state := range []HoldState{HoldActive, HoldReleased} {
		if err := ValidateRecoveryPointHoldRecordState(state); err != nil {
			t.Fatalf("valid recovery point hold record state %q rejected: %v", state, err)
		}
	}
	for _, operation := range []LifecycleOperation{LifecycleRetentionExpire, LifecycleExplicitPurge, LifecycleMutableRetire} {
		if err := ValidateLifecycleOperation(operation); err != nil {
			t.Fatalf("valid lifecycle operation %q rejected: %v", operation, err)
		}
	}
	for _, reason := range []LifecycleBlockedReason{
		LifecycleBlockedActiveHold, LifecycleBlockedLeaseLive, LifecycleBlockedLeaseDrainUnproven,
		LifecycleBlockedOwnerCleanupUnproven, LifecycleBlockedProviderWORM, LifecycleBlockedProviderUnavailable,
		LifecycleBlockedProviderIdentityConflict, LifecycleBlockedProviderDeleteUnproven,
		LifecycleBlockedProviderNativeVersionReferenced,
		LifecycleBlockedDeletionUnavailable, LifecycleBlockedFenceLost,
	} {
		if err := ValidateLifecycleBlockedReason(reason); err != nil {
			t.Fatalf("valid lifecycle blocked reason %q rejected: %v", reason, err)
		}
	}
	for _, phase := range []LifecyclePhase{
		LifecyclePhaseSelected, LifecyclePhaseRevoking, LifecyclePhaseDraining, LifecyclePhaseCleaning,
		LifecyclePhaseProviderDelete, LifecyclePhaseTombstoning, LifecyclePhaseBlocked, LifecyclePhaseComplete,
	} {
		if err := ValidateLifecyclePhase(phase); err != nil {
			t.Fatalf("valid lifecycle phase %q rejected: %v", phase, err)
		}
	}
	for _, state := range []ImportReviewState{ImportReviewPending, ImportReviewAccepted, ImportReviewRejected} {
		if err := ValidateImportReviewState(state); err != nil {
			t.Fatalf("valid import review state %q rejected: %v", state, err)
		}
	}
	for _, status := range []PurgePlanStatus{
		PurgePlanReady, PurgePlanBound, PurgePlanExecuting, PurgePlanConsumed, PurgePlanInvalidated,
	} {
		if err := ValidatePurgePlanStatus(status); err != nil {
			t.Fatalf("valid purge plan status %q rejected: %v", status, err)
		}
	}
	for _, kind := range []ImportCandidateKind{
		ImportCandidateNativeSnapshot, ImportCandidateXirangManifest, ImportCandidateImportedBaseline, ImportCandidateMutableHead,
	} {
		if err := ValidateImportCandidateKind(kind); err != nil {
			t.Fatalf("valid import kind %q rejected: %v", kind, err)
		}
	}
	for _, kind := range []ConfigImportEntityKind{
		ConfigImportRepository, ConfigImportTaskLink, ConfigImportRetentionPolicy, ConfigImportHold,
	} {
		if err := ValidateConfigImportEntityKind(kind); err != nil {
			t.Fatalf("valid config import kind %q rejected: %v", kind, err)
		}
	}

	invalidCases := []struct {
		name string
		err  error
	}{
		{name: "scope", err: ValidateRetentionPolicyScope("future", strings.Repeat("a", 32))},
		{name: "scope id", err: ValidateRetentionPolicyScope(RetentionPolicyScopeRepository, "7")},
		{name: "policy status", err: ValidateRetentionPolicyStatus("future")},
		{name: "hold", err: ValidateRecoveryPointHoldType("future")},
		{name: "hold none", err: ValidateRecoveryPointHoldRecordState(HoldNone)},
		{name: "hold state", err: ValidateRecoveryPointHoldRecordState("future")},
		{name: "operation", err: ValidateLifecycleOperation("future")},
		{name: "phase", err: ValidateLifecyclePhase("future")},
		{name: "blocked reason", err: ValidateLifecycleBlockedReason("future")},
		{name: "import", err: ValidateImportCandidateKind("future")},
		{name: "import review", err: ValidateImportReviewState("future")},
		{name: "purge plan", err: ValidatePurgePlanStatus("future")},
		{name: "config", err: ValidateConfigImportEntityKind("future")},
	}
	for _, testCase := range invalidCases {
		if !errors.Is(testCase.err, ErrInvalidState) {
			t.Fatalf("invalid %s returned %v, want ErrInvalidState", testCase.name, testCase.err)
		}
	}
}

func TestAssetRefRequiresRecoveryPointAndEntry(t *testing.T) {
	valid := AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)}
	if err := ValidateAssetRef(valid); err != nil {
		t.Fatalf("valid AssetRef rejected: %v", err)
	}

	for _, ref := range []AssetRef{
		{},
		{RecoveryPointID: valid.RecoveryPointID},
		{EntryID: valid.EntryID},
		{RecoveryPointID: "1", EntryID: valid.EntryID},
		{RecoveryPointID: valid.RecoveryPointID, EntryID: strings.Repeat("G", 64)},
	} {
		if err := ValidateAssetRef(ref); !errors.Is(err, ErrInvalidAssetRef) {
			t.Fatalf("invalid AssetRef %+v returned %v", ref, err)
		}
	}
}

func TestCommandCapabilityRequiresArtifactContract(t *testing.T) {
	withoutContract := CapabilitiesForTask(TaskArtifactContract{Provider: ProviderCommand})
	if withoutContract.List || withoutContract.Download || withoutContract.Restore {
		t.Fatalf("command task without artifact contract gained capabilities: %+v", withoutContract)
	}
	if withoutContract.Reason == nil || withoutContract.Reason.Code != CapabilityTaskArtifactContractMissing {
		t.Fatalf("missing stable capability reason: %+v", withoutContract.Reason)
	}

	withContract := CapabilitiesForTask(TaskArtifactContract{
		Provider:            ProviderCommand,
		HasArtifactContract: true,
		PublicationMode:     PublicationVersionedFullCopy,
	})
	if !withContract.List || !withContract.OpenSequential || !withContract.Download || !withContract.Restore {
		t.Fatalf("explicit command artifact contract was not honored: %+v", withContract)
	}
}

func TestUnknownProviderCapabilityDoesNotEchoRawProviderValue(t *testing.T) {
	rawProvider := ProviderKind("s3://private-bucket/customer-path")
	capabilities := CapabilitiesForTask(TaskArtifactContract{Provider: rawProvider})
	if capabilities.Reason == nil || capabilities.Reason.Code != CapabilityProviderUnavailable {
		t.Fatalf("unknown provider did not fail closed: %+v", capabilities)
	}
	if len(capabilities.Reason.Params) != 0 {
		t.Fatalf("unknown provider value leaked into capability params: %+v", capabilities.Reason.Params)
	}
}

func TestProviderCapabilityReasonCodesAreValidated(t *testing.T) {
	codes := []CapabilityCode{
		CapabilityRepositoryIdentityUnavailable,
		CapabilityProviderProtocolIncompatible,
		CapabilityProviderOperationTimeout,
		CapabilityProviderResourceLimit,
	}
	for _, code := range codes {
		if err := ValidateCapabilityReason(CapabilityReason{Code: code}); err != nil {
			t.Fatalf("code %q rejected: %v", code, err)
		}
	}
}

func TestTaskCapabilitiesDoNotClaimUnprovenRange(t *testing.T) {
	contracts := []TaskArtifactContract{
		{Provider: ProviderRestic},
		{Provider: ProviderRsync, PublicationMode: PublicationLegacyMutable},
		{Provider: ProviderRclone, PublicationMode: PublicationLegacyMutable},
	}
	for _, contract := range contracts {
		if capabilities := CapabilitiesForTask(contract); capabilities.OpenRange {
			t.Fatalf("provider %q claimed unproven range capability: %+v", contract.Provider, capabilities)
		}
	}
}

func TestBindImportedRepositoryIdentity(t *testing.T) {
	observed := "restic:native:config-v2-shared-identity"
	placeholder := FormatImportedRepositoryIdentity(ImportedIdentityRef(observed))

	bound, err := BindImportedRepositoryIdentity(&placeholder, observed)
	if err != nil || bound != observed {
		t.Fatalf("placeholder bind=%q err=%v", bound, err)
	}
	if _, err := BindImportedRepositoryIdentity(&placeholder, "restic:native:other"); err == nil {
		t.Fatal("mismatched placeholder identity should conflict")
	}
	bound, err = BindImportedRepositoryIdentity(nil, observed)
	if err != nil || bound != observed {
		t.Fatalf("nil identity bind=%q err=%v", bound, err)
	}
	same := observed
	bound, err = BindImportedRepositoryIdentity(&same, observed)
	if err != nil || bound != observed {
		t.Fatalf("exact identity bind=%q err=%v", bound, err)
	}
	other := "restic:native:other"
	if _, err := BindImportedRepositoryIdentity(&other, observed); err == nil {
		t.Fatal("exact identity mismatch should conflict")
	}
}

func TestOpaqueIDFormatAndEntropySourceFailure(t *testing.T) {
	id, err := NewOpaqueID()
	if err != nil {
		t.Fatalf("NewOpaqueID: %v", err)
	}
	if err := ValidateOpaqueID(id); err != nil {
		t.Fatalf("generated ID invalid: %q: %v", id, err)
	}
	if len(id) != 32 || id != strings.ToLower(id) {
		t.Fatalf("unexpected opaque ID format: %q", id)
	}

	second, err := NewOpaqueID()
	if err != nil {
		t.Fatalf("second NewOpaqueID: %v", err)
	}
	if id == second {
		t.Fatal("two opaque IDs unexpectedly matched")
	}

	if _, err := newOpaqueIDFrom(bytes.NewReader(make([]byte, 15))); err == nil {
		t.Fatal("short entropy source unexpectedly succeeded")
	}
	for _, candidate := range []string{"", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("0", 33)} {
		if err := ValidateOpaqueID(candidate); err == nil {
			t.Fatalf("invalid opaque ID accepted: %q", candidate)
		}
	}
}

func TestRecoveryPointSourceLifecycleRequestClosedContract(t *testing.T) {
	request := SourceLifecycleRequest{
		RecoveryPointID: strings.Repeat("a", 32), LifecycleAttemptID: strings.Repeat("b", 32),
		Operation: LifecycleMutableRetire, Stage: SourceLifecyclePrepare,
	}
	if err := ValidateSourceLifecycleRequest(request); err != nil {
		t.Fatalf("valid source lifecycle request rejected: %v", err)
	}
	request.Stage = SourceLifecycleCleanup
	if err := ValidateSourceLifecycleRequest(request); err != nil {
		t.Fatalf("valid cleanup source lifecycle request rejected: %v", err)
	}
	for _, mutate := range []func(*SourceLifecycleRequest){
		func(value *SourceLifecycleRequest) { value.RecoveryPointID = "bad" },
		func(value *SourceLifecycleRequest) { value.LifecycleAttemptID = "bad" },
		func(value *SourceLifecycleRequest) { value.Operation = "future_operation" },
		func(value *SourceLifecycleRequest) { value.Stage = "future_stage" },
	} {
		invalid := request
		mutate(&invalid)
		if err := ValidateSourceLifecycleRequest(invalid); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("invalid source lifecycle request error=%v, want ErrInvalidState", err)
		}
	}
}

func validMutableProfile() RecoveryPointProfile {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	return RecoveryPointProfile{
		VersionMode:  VersionMutableHead,
		Semantics:    PointMutableHead,
		State:        RecoveryPointObserved,
		Immutability: ImmutabilityMutable,
		Availability: PhysicalOnline,
		ObservedAt:   &now,
	}
}

func validImmutableProfile() RecoveryPointProfile {
	return RecoveryPointProfile{
		VersionMode:  VersionHardlinkTree,
		Semantics:    PointXirangManifest,
		State:        RecoveryPointPreparing,
		Immutability: ImmutabilityXirangManaged,
		Availability: PhysicalOnline,
	}
}
