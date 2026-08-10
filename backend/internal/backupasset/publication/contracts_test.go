package publication

import (
	"encoding/json"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestResticOperationLedgerIsClosedAndLegacyBlockAuditIsDeterministic(t *testing.T) {
	operations := []ResticOperation{
		OperationLegacyBackup,
		OperationLegacySnapshotList,
		OperationLegacySnapshotFiles,
		OperationLegacyIndex,
		OperationLegacySearch,
		OperationLegacyDiff,
		OperationLegacySnapshotRestore,
		OperationLegacyRestoreLatest,
		OperationLegacyAnomaly,
		OperationLegacyRetention,
		OperationEvidenceBackup,
		OperationManifest,
		OperationReconcile,
		OperationContentRead,
	}
	for _, operation := range operations {
		if err := ValidateResticOperation(operation); err != nil {
			t.Fatalf("operation %q rejected: %v", operation, err)
		}
	}
	if err := ValidateResticOperation(ResticOperation("raw-command")); err == nil {
		t.Fatal("unknown Restic operation was accepted")
	}

	runID := uint(91)
	first, err := NewSystemLegacyBlockAuditContext(42, &runID, OperationLegacySnapshotList)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSystemLegacyBlockAuditContext(42, &runID, OperationLegacySnapshotList)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("legacy block audit context is not deterministic: first=%+v second=%+v", first, second)
	}
	if first.Actor.Username != "system" || first.Actor.Role != "system" || !strings.HasPrefix(first.CorrelationID, "legblk-") {
		t.Fatalf("unsafe system legacy block context: %+v", first)
	}
	if err := backupasset.ValidatePublicationAuditContext(first); err != nil {
		t.Fatalf("legacy block context does not satisfy root audit contract: %v", err)
	}
	if _, err := NewSystemLegacyBlockAuditContext(0, nil, OperationLegacyBackup); err == nil {
		t.Fatal("zero Task ID was accepted")
	}
	if _, err := NewSystemLegacyBlockAuditContext(42, nil, ResticOperation("raw-command")); err == nil {
		t.Fatal("unknown operation was accepted for legacy block audit")
	}
}

func TestExactRecoverySourceKeepsLocatorAndDigestOffTheWire(t *testing.T) {
	fakeLocator := "FAKE_RECOVERY_LOCATOR_FOR_PUBLICATION_BOUNDARY"
	locatorDigest := strings.Repeat("a", 64)
	source := ExactRecoverySource{
		RepositoryID:    strings.Repeat("b", 32),
		RecoveryPointID: strings.Repeat("c", 32),
		Provider:        backupasset.ProviderRestic,
		Locator:         fakeLocator,
		LocatorDigest:   locatorDigest,
	}
	if err := source.Validate(); err != nil {
		t.Fatalf("ExactRecoverySource.Validate() error = %v", err)
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fakeLocator, locatorDigest} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("ExactRecoverySource JSON leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestImmutableLocatorDigestMatchesRecoveryBindingGoldenVectors(t *testing.T) {
	tests := []struct {
		name            string
		repositoryID    string
		provider        backupasset.ProviderKind
		recoveryPointID string
		locator         string
		want            string
	}{
		{
			name:            "restic exact locator",
			repositoryID:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			provider:        backupasset.ProviderRestic,
			recoveryPointID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			locator:         "FAKE_LOCATOR_FOR_DIGEST_VECTOR_A",
			want:            "da078fcf22aa14d8b8119dfb57c133139dbf47fe009aad5fd8798b45a61ac0ad",
		},
		{
			name:            "rsync literal locator bytes",
			repositoryID:    "0123456789abcdef0123456789abcdef",
			provider:        backupasset.ProviderRsync,
			recoveryPointID: "fedcba9876543210fedcba9876543210",
			locator:         "FAKE_LOCATOR_FOR_DIGEST_VECTOR_B/with-%_\\-literal",
			want:            "4ac95bf188ec11e546e16ed9c970685b3a184dcded57cc3073fb1c060bda71f8",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ImmutableLocatorDigest(
				testCase.repositoryID,
				testCase.provider,
				testCase.recoveryPointID,
				testCase.locator,
			)
			if err != nil {
				t.Fatalf("ImmutableLocatorDigest() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("ImmutableLocatorDigest() = %s, want %s", got, testCase.want)
			}
		})
	}
}
