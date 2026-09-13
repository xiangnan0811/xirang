package backuphealth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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
func TestLatestVerifiedForNodesPushesTopOneToDatabase(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		runLatestVerifiedForNodesHistory(t, openFactTestDB(t), "sqlite", 3, 3, false)
	})
	t.Run("postgres", func(t *testing.T) {
		runLatestVerifiedForNodesHistory(t, openBackupMetricPostgresTestDB(t), "postgres", 3, 3, false)
	})
}

func TestLatestVerifiedForNodesLargeHistoryEvidence(t *testing.T) {
	if os.Getenv("RUN_R7_04_SCALE_TEST") != "1" {
		t.Skip("set RUN_R7_04_SCALE_TEST=1 to run the 100,000-row EXPLAIN/allocation evidence")
	}
	t.Run("sqlite", func(t *testing.T) {
		runLatestVerifiedForNodesHistory(t, openFactTestDB(t), "sqlite", 10, 10_000, true)
	})
	t.Run("postgres", func(t *testing.T) {
		runLatestVerifiedForNodesHistory(t, openBackupMetricPostgresTestDB(t), "postgres", 10, 10_000, true)
	})
}

func runLatestVerifiedForNodesHistory(t *testing.T, db *gorm.DB, engine string, nodeCount, historyPerNode int, captureEvidence bool) {
	nodes := make([]model.Node, 0, nodeCount+1)
	nodeIDs := make([]uint, 0, nodeCount)
	for index := range nodeCount {
		node := factTestNode(t, db, fmt.Sprintf("fact-history-%02d", index))
		nodes = append(nodes, node)
		nodeIDs = append(nodeIDs, node.ID)
	}
	unauthorized := factTestNode(t, db, "fact-history-unauthorized")
	nodes = append(nodes, unauthorized)

	completedAt := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	facts := make([]model.BackupCompletion, 0, nodeCount*historyPerNode)
	for nodeIndex, node := range nodes[:nodeCount] {
		for historyIndex := range historyPerNode {
			taskID := uint(nodeIndex*historyPerNode + historyIndex + 1)
			taskRunID := uint(1_000_000 + nodeIndex*historyPerNode + historyIndex + 1)
			facts = append(facts, model.BackupCompletion{
				TaskID:         &taskID,
				TaskRunID:      &taskRunID,
				NodeID:         node.ID,
				ExecutorType:   "rsync",
				FactKind:       model.BackupCompletionKindLegacyTransferCompleted,
				EvidenceStatus: model.BackupCompletionEvidenceVerified,
				CompletedAt:    completedAt,
				CreatedAt:      completedAt,
				UpdatedAt:      completedAt,
			})
		}
	}
	if err := db.CreateInBatches(&facts, 1_000).Error; err != nil {
		t.Fatalf("create %s completion history: %v", engine, err)
	}
	expectedIDs := make(map[uint]uint, nodeCount)
	for nodeIndex, node := range nodes[:nodeCount] {
		expectedIDs[node.ID] = facts[(nodeIndex+1)*historyPerNode-1].ID
	}
	// A newer unverified fact must not displace the verified top-1.
	unverifiedAt := completedAt.Add(time.Hour)
	if err := db.Create(&model.BackupCompletion{
		NodeID:         nodes[0].ID,
		FactKind:       model.BackupCompletionKindLegacyUnverified,
		EvidenceStatus: model.BackupCompletionEvidenceUnverified,
		CompletedAt:    unverifiedAt,
		EvidenceRef:    "large-history-unverified",
		CreatedAt:      unverifiedAt,
		UpdatedAt:      unverifiedAt,
	}).Error; err != nil {
		t.Fatalf("create %s unverified history: %v", engine, err)
	}
	// An authorized node list must not be widened by unrelated facts.
	unauthorizedTaskID := uint(9_000_001)
	unauthorizedRunID := uint(9_000_002)
	if err := db.Create(&model.BackupCompletion{
		TaskID:         &unauthorizedTaskID,
		TaskRunID:      &unauthorizedRunID,
		NodeID:         unauthorized.ID,
		ExecutorType:   "rsync",
		FactKind:       model.BackupCompletionKindLegacyTransferCompleted,
		EvidenceStatus: model.BackupCompletionEvidenceVerified,
		CompletedAt:    completedAt,
		CreatedAt:      completedAt,
		UpdatedAt:      completedAt,
	}).Error; err != nil {
		t.Fatalf("create %s unauthorized fact: %v", engine, err)
	}

	var historyRows int64
	if err := db.Model(&model.BackupCompletion{}).
		Where("node_id IN ?", nodeIDs).
		Count(&historyRows).Error; err != nil {
		t.Fatalf("count %s completion history: %v", engine, err)
	}
	if historyRows != int64(nodeCount*historyPerNode+1) {
		t.Fatalf("%s history rows=%d, want %d", engine, historyRows, nodeCount*historyPerNode+1)
	}

	if captureEvidence {
		explainLatestVerifiedForNodes(t, db, engine, nodeIDs)
	}

	var queryRows []model.BackupCompletion
	queryResult := latestVerifiedForNodesQuery(context.Background(), db, nodeIDs).Scan(&queryRows)
	if queryResult.Error != nil {
		t.Fatalf("query %s latest facts: %v", engine, queryResult.Error)
	}
	if queryResult.RowsAffected != int64(nodeCount) {
		t.Fatalf("%s database returned rows=%d, want %d", engine, queryResult.RowsAffected, nodeCount)
	}
	if len(queryRows) != nodeCount {
		t.Fatalf("%s application scan rows=%d, want %d", engine, len(queryRows), nodeCount)
	}
	for _, row := range queryRows {
		if row.EvidenceStatus != model.BackupCompletionEvidenceVerified || row.CompletedAt.UTC() != completedAt {
			t.Fatalf("%s returned non-authoritative row=%+v", engine, row)
		}
		if wantID := expectedIDs[row.NodeID]; row.ID != wantID {
			t.Fatalf("%s node %d selected id=%d, want tie-break id=%d", engine, row.NodeID, row.ID, wantID)
		}
	}

	if captureEvidence {
		var allocationErr error
		allocs := testing.AllocsPerRun(1, func() {
			var got map[uint]model.BackupCompletion
			got, allocationErr = LatestVerifiedForNodes(context.Background(), db, nodeIDs)
			if allocationErr == nil && len(got) != nodeCount {
				allocationErr = fmt.Errorf("application map rows=%d, want %d", len(got), nodeCount)
			}
		})
		if allocationErr != nil {
			t.Fatalf("%s repeated latest query: %v", engine, allocationErr)
		}
		t.Logf("%s history_rows=%d database_return_rows=%d application_rows=%d allocations_per_call=%.0f", engine, historyRows, queryResult.RowsAffected, len(queryRows), allocs)
	}
}

func explainLatestVerifiedForNodes(t *testing.T, db *gorm.DB, engine string, nodeIDs []uint) {
	t.Helper()
	prefix := "EXPLAIN "
	if engine == "sqlite" {
		prefix = "EXPLAIN QUERY PLAN "
	}
	rows, err := db.Raw(prefix+latestVerifiedForNodesSQL, nodeIDs, model.BackupCompletionEvidenceVerified).Rows()
	if err != nil {
		t.Fatalf("explain %s latest facts: %v", engine, err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		if engine == "sqlite" {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				t.Fatalf("scan sqlite latest-facts explain: %v", err)
			}
			details = append(details, detail)
			continue
		}
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatalf("scan postgres latest-facts explain: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s latest-facts explain: %v", engine, err)
	}
	t.Logf("%s EXPLAIN latest_verified_for_nodes: %s", engine, strings.Join(details, " | "))
}
