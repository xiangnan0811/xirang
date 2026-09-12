package backuphealth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openFactTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open fact database: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.BackupCompletion{}); err != nil {
		t.Fatalf("migrate fact database: %v", err)
	}
	return db
}

func factTestNode(t *testing.T, db *gorm.DB, name string) model.Node {
	t.Helper()
	node := model.Node{Name: name, Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "password", Status: "online", BackupDir: name}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create fact node: %v", err)
	}
	return node
}

func TestRecordLegacyTransferIsAtomicMonotonicAndIdempotent(t *testing.T) {
	db := openFactTestDB(t)
	node := factTestNode(t, db, "fact-legacy")
	first := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	input := LegacyTransferInput{TaskID: 11, TaskRunID: 12, NodeID: node.ID, ExecutorType: "RSYNC", CompletedAt: first}
	if err := db.Transaction(func(tx *gorm.DB) error { return RecordLegacyTransferTx(context.Background(), tx, input) }); err != nil {
		t.Fatalf("record legacy fact: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return RecordLegacyTransferTx(context.Background(), tx, input) }); err != nil {
		t.Fatalf("exact replay must be idempotent: %v", err)
	}
	var count int64
	if err := db.Model(&model.BackupCompletion{}).Where("task_run_id = ?", input.TaskRunID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("fact count=%d, want 1", count)
	}
	var nodeAfter model.Node
	if err := db.First(&nodeAfter, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if nodeAfter.LastBackupAt == nil || !nodeAfter.LastBackupAt.Equal(first) {
		t.Fatalf("node freshness=%v, want %s", nodeAfter.LastBackupAt, first)
	}

	// An older replay cannot move the denormalized timestamp backwards.
	older := input
	older.CompletedAt = first.Add(-time.Hour)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return RecordLegacyTransferTx(context.Background(), tx, LegacyTransferInput(older))
	}); !errors.Is(err, ErrImmutableCompletion) {
		t.Fatalf("changed immutable completion returned %v, want ErrImmutableCompletion", err)
	}
	if err := db.First(&nodeAfter, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if nodeAfter.LastBackupAt == nil || !nodeAfter.LastBackupAt.Equal(first) {
		t.Fatalf("failed conflicting replay changed freshness=%v", nodeAfter.LastBackupAt)
	}
}

func TestRecordManagedCommitIncludesFirstPointAndRejectsNonOpaqueEvidence(t *testing.T) {
	db := openFactTestDB(t)
	node := factTestNode(t, db, "fact-managed")
	committed := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	input := ManagedCommittedInput{TaskID: 21, TaskRunID: 22, NodeID: node.ID, ExecutorType: "restic", RecoveryPointID: "0123456789abcdef0123456789abcdef", CommittedAt: committed}
	if err := RecordManagedCommitted(context.Background(), db, input); err != nil {
		t.Fatalf("record managed commit: %v", err)
	}
	if err := RecordManagedCommitted(context.Background(), db, input); err != nil {
		t.Fatalf("managed commit replay must be idempotent: %v", err)
	}
	var row model.BackupCompletion
	if err := db.Where("task_run_id = ?", input.TaskRunID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.FactKind != model.BackupCompletionKindManagedCommitted || row.EvidenceStatus != model.BackupCompletionEvidenceVerified || row.EvidenceRef != input.RecoveryPointID {
		t.Fatalf("managed fact=%+v", row)
	}
	bad := input
	bad.TaskRunID++
	bad.RecoveryPointID = "not-a-recovery-point"
	if err := RecordManagedCommitted(context.Background(), db, bad); !errors.Is(err, ErrInvalidCompletionFact) {
		t.Fatalf("invalid managed evidence returned %v, want ErrInvalidCompletionFact", err)
	}
}

func TestLatestVerifiedForNodesIgnoresUnverifiedFacts(t *testing.T) {
	db := openFactTestDB(t)
	mixedNode := factTestNode(t, db, "fact-mixed")
	unverifiedOnlyNode := factTestNode(t, db, "fact-unverified-only")

	verifiedAt := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	unverifiedAt := verifiedAt.Add(time.Hour)
	taskID := uint(31)
	taskRunID := uint(32)
	if err := db.Create(&model.BackupCompletion{
		TaskID:         &taskID,
		TaskRunID:      &taskRunID,
		NodeID:         mixedNode.ID,
		ExecutorType:   "rsync",
		FactKind:       model.BackupCompletionKindLegacyTransferCompleted,
		EvidenceStatus: model.BackupCompletionEvidenceVerified,
		CompletedAt:    verifiedAt,
		CreatedAt:      verifiedAt,
		UpdatedAt:      verifiedAt,
	}).Error; err != nil {
		t.Fatalf("create verified fact: %v", err)
	}
	for _, fact := range []model.BackupCompletion{
		{
			NodeID:         mixedNode.ID,
			FactKind:       model.BackupCompletionKindLegacyUnverified,
			EvidenceStatus: model.BackupCompletionEvidenceUnverified,
			CompletedAt:    unverifiedAt,
			EvidenceRef:    "legacy-newer-fact",
			CreatedAt:      unverifiedAt,
			UpdatedAt:      unverifiedAt,
		},
		{
			NodeID:         unverifiedOnlyNode.ID,
			FactKind:       model.BackupCompletionKindLegacyUnverified,
			EvidenceStatus: model.BackupCompletionEvidenceUnverified,
			CompletedAt:    unverifiedAt,
			EvidenceRef:    "legacy-only-fact",
			CreatedAt:      unverifiedAt,
			UpdatedAt:      unverifiedAt,
		},
	} {
		if err := db.Create(&fact).Error; err != nil {
			t.Fatalf("create unverified fact: %v", err)
		}
	}

	facts, err := LatestVerifiedForNodes(context.Background(), db, []uint{mixedNode.ID, unverifiedOnlyNode.ID})
	if err != nil {
		t.Fatalf("load latest verified facts: %v", err)
	}
	if _, ok := facts[unverifiedOnlyNode.ID]; ok {
		t.Fatalf("unverified-only node returned an authoritative fact: %+v", facts[unverifiedOnlyNode.ID])
	}
	fact, ok := facts[mixedNode.ID]
	if !ok {
		t.Fatalf("mixed node missing its verified fact: %+v", facts)
	}
	if fact.EvidenceStatus != model.BackupCompletionEvidenceVerified || !fact.CompletedAt.Equal(verifiedAt) {
		t.Fatalf("newer unverified fact replaced verified fact: %+v", fact)
	}
}
