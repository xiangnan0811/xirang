package publication

import (
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
