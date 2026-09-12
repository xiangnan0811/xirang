// Package backuphealth owns the durable, classified evidence used by backup
// freshness and reporting. It deliberately does not import task execution
// packages: execution passes immutable snapshots at the terminal/provider
// boundary, avoiding a dependency on mutable Task configuration.
package backuphealth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidCompletionFact       = errors.New("invalid backup completion fact")
	ErrImmutableCompletion         = errors.New("backup completion fact is immutable")
	ErrUnverifiedManagedCompletion = errors.New("managed completion evidence is unverified")
)

var supportedExecutors = map[string]struct{}{
	"rsync":  {},
	"restic": {},
	"rclone": {},
}

// LegacyTransferInput is the immutable handoff from a compatibility executor
// terminal transaction. ExecutorType is a snapshot captured before execution;
// callers must not derive it from a mutable Task at completion time.
type LegacyTransferInput struct {
	TaskID       uint
	TaskRunID    uint
	NodeID       uint
	ExecutorType string
	CompletedAt  time.Time
}

// ManagedCommittedInput is the immutable handoff from a committed managed
// RecoveryPoint. RecoveryPointID proves the publication side of availability;
// a preparing/verifying/degraded/warning point is not accepted.
type ManagedCommittedInput struct {
	TaskID          uint
	TaskRunID       uint
	NodeID          uint
	ExecutorType    string
	RecoveryPointID string
	CommittedAt     time.Time
}

// RecordLegacyTransferTx records a compatibility transfer in the caller's
// terminal transaction. It is idempotent for exact replay and rejects any
// attempt to reclassify an existing TaskRun.
func RecordLegacyTransferTx(ctx context.Context, tx *gorm.DB, input LegacyTransferInput) error {
	return recordVerifiedTx(ctx, tx, model.BackupCompletion{
		TaskID:         new(input.TaskID),
		TaskRunID:      new(input.TaskRunID),
		NodeID:         input.NodeID,
		ExecutorType:   normalizeExecutor(input.ExecutorType),
		FactKind:       model.BackupCompletionKindLegacyTransferCompleted,
		EvidenceStatus: model.BackupCompletionEvidenceVerified,
		CompletedAt:    input.CompletedAt.UTC(),
	})
}

// RecordManagedCommittedTx records a committed managed RecoveryPoint in the
// transaction that observes/reconciles the durable commit. It includes the
// first point and is independent from anomaly analysis.
func RecordManagedCommittedTx(ctx context.Context, tx *gorm.DB, input ManagedCommittedInput) error {
	return recordVerifiedTx(ctx, tx, model.BackupCompletion{
		TaskID:         new(input.TaskID),
		TaskRunID:      new(input.TaskRunID),
		NodeID:         input.NodeID,
		ExecutorType:   normalizeExecutor(input.ExecutorType),
		FactKind:       model.BackupCompletionKindManagedCommitted,
		EvidenceStatus: model.BackupCompletionEvidenceVerified,
		CompletedAt:    input.CommittedAt.UTC(),
		EvidenceRef:    strings.TrimSpace(input.RecoveryPointID),
	})
}

// RecordManagedCommitted is used by a replay path after the RecoveryPoint
// commit is already durable. The insert remains idempotent and the denormalized
// Node timestamp update is performed in the same transaction as the fact.
func RecordManagedCommitted(ctx context.Context, db *gorm.DB, input ManagedCommittedInput) error {
	if db == nil {
		return fmt.Errorf("%w: database is unavailable", ErrInvalidCompletionFact)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return RecordManagedCommittedTx(ctx, tx, input)
	})
}

// RecordManagedUnverified persists a durable marker for a committed-looking
// point whose immutable evidence is insufficient to prove a managed backup.
// The marker deliberately leaves TaskID and TaskRunID NULL, so a later valid
// fact for that run can still be recorded without colliding with a unique
// verified-attempt key.
func RecordManagedUnverified(ctx context.Context, db *gorm.DB, nodeID uint, evidenceRef string, markedAt time.Time) error {
	if db == nil {
		return fmt.Errorf("%w: database is unavailable", ErrInvalidCompletionFact)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return recordUnverifiedTx(ctx, tx, nodeID, evidenceRef, markedAt)
	})
}

func recordUnverifiedTx(ctx context.Context, tx *gorm.DB, nodeID uint, evidenceRef string, markedAt time.Time) error {
	if tx == nil {
		return fmt.Errorf("%w: database is unavailable", ErrInvalidCompletionFact)
	}
	evidenceRef = strings.TrimSpace(evidenceRef)
	if evidenceRef == "" || len(evidenceRef) > 64 {
		return fmt.Errorf("%w: unverified managed evidence reference is required", ErrInvalidCompletionFact)
	}
	if markedAt.IsZero() {
		markedAt = time.Now().UTC()
	} else {
		markedAt = markedAt.UTC()
	}
	fact := model.BackupCompletion{
		NodeID: nodeID, FactKind: model.BackupCompletionKindLegacyUnverified,
		EvidenceStatus: model.BackupCompletionEvidenceUnverified, CompletedAt: markedAt,
		EvidenceRef: evidenceRef, CreatedAt: markedAt, UpdatedAt: markedAt,
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&fact).Error; err != nil {
		return fmt.Errorf("insert unverified completion marker: %w", err)
	}
	var existing model.BackupCompletion
	query := tx.WithContext(ctx).Where("evidence_status = ? AND evidence_ref = ?", model.BackupCompletionEvidenceUnverified, evidenceRef).First(&existing)
	if query.Error != nil {
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: unverified completion marker was not persisted", ErrInvalidCompletionFact)
		}
		return fmt.Errorf("load unverified completion marker: %w", query.Error)
	}
	if existing.NodeID != nodeID || existing.FactKind != model.BackupCompletionKindLegacyUnverified ||
		existing.EvidenceStatus != model.BackupCompletionEvidenceUnverified || existing.EvidenceRef != evidenceRef {
		return fmt.Errorf("%w: unverified completion marker changed", ErrImmutableCompletion)
	}
	return nil
}

func recordVerifiedTx(ctx context.Context, tx *gorm.DB, fact model.BackupCompletion) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateVerifiedFact(fact); err != nil {
		return err
	}
	if tx == nil {
		return fmt.Errorf("%w: database is unavailable", ErrInvalidCompletionFact)
	}
	// Do not probe then Create: on PostgreSQL a losing unique insert aborts the
	// transaction, making a subsequent replay query impossible. ON CONFLICT
	// keeps exact concurrent replays in the same valid transaction.
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&fact).Error; err != nil {
		return fmt.Errorf("insert completion fact: %w", err)
	}
	var existing model.BackupCompletion
	query := tx.WithContext(ctx).Where("task_run_id = ?", *fact.TaskRunID).First(&existing)
	if query.Error != nil {
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: completion fact insert was not persisted", ErrInvalidCompletionFact)
		}
		return fmt.Errorf("load completion fact: %w", query.Error)
	}
	if err := compareImmutableFact(existing, fact); err != nil {
		return err
	}
	return advanceNodeFreshnessTx(ctx, tx, existing.NodeID, existing.CompletedAt)
}

func validateVerifiedFact(fact model.BackupCompletion) error {
	if fact.TaskID == nil || *fact.TaskID == 0 || fact.TaskRunID == nil || *fact.TaskRunID == 0 || fact.NodeID == 0 || fact.CompletedAt.IsZero() {
		return fmt.Errorf("%w: task, run, node and completion time are required", ErrInvalidCompletionFact)
	}
	executor := normalizeExecutor(fact.ExecutorType)
	if _, ok := supportedExecutors[executor]; !ok {
		return fmt.Errorf("%w: unsupported executor %q", ErrInvalidCompletionFact, fact.ExecutorType)
	}
	if fact.EvidenceStatus != model.BackupCompletionEvidenceVerified {
		return fmt.Errorf("%w: verified fact requires verified evidence status", ErrInvalidCompletionFact)
	}
	switch fact.FactKind {
	case model.BackupCompletionKindLegacyTransferCompleted:
		if strings.TrimSpace(fact.EvidenceRef) != "" {
			return fmt.Errorf("%w: legacy transfer cannot carry recovery point evidence", ErrInvalidCompletionFact)
		}
	case model.BackupCompletionKindManagedCommitted:
		if !validOpaqueReference(fact.EvidenceRef) {
			return fmt.Errorf("%w: managed commit requires recovery point evidence", ErrInvalidCompletionFact)
		}
	default:
		return fmt.Errorf("%w: unsupported fact kind %q", ErrInvalidCompletionFact, fact.FactKind)
	}
	return nil
}

func compareImmutableFact(existing, expected model.BackupCompletion) error {
	if existing.TaskID == nil || expected.TaskID == nil || existing.TaskRunID == nil || expected.TaskRunID == nil ||
		*existing.TaskID != *expected.TaskID || *existing.TaskRunID != *expected.TaskRunID ||
		existing.NodeID != expected.NodeID || normalizeExecutor(existing.ExecutorType) != normalizeExecutor(expected.ExecutorType) ||
		existing.FactKind != expected.FactKind || existing.EvidenceStatus != expected.EvidenceStatus ||
		!existing.CompletedAt.UTC().Equal(expected.CompletedAt.UTC()) || strings.TrimSpace(existing.EvidenceRef) != strings.TrimSpace(expected.EvidenceRef) {
		return fmt.Errorf("%w: TaskRun %d has conflicting immutable evidence", ErrImmutableCompletion, *expected.TaskRunID)
	}
	return nil
}

func advanceNodeFreshnessTx(ctx context.Context, tx *gorm.DB, nodeID uint, completedAt time.Time) error {
	if tx == nil || nodeID == 0 || completedAt.IsZero() {
		return nil
	}
	var latest model.BackupCompletion
	query := tx.WithContext(ctx).Where("node_id = ? AND evidence_status = ?", nodeID, model.BackupCompletionEvidenceVerified).
		Order("completed_at DESC, id DESC").First(&latest)
	if errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	if query.Error != nil {
		return fmt.Errorf("load latest completion timestamp: %w", query.Error)
	}
	result := tx.WithContext(ctx).Model(&model.Node{}).
		Where("id = ? AND (last_backup_at IS NULL OR last_backup_at < ?)", nodeID, latest.CompletedAt.UTC()).
		Update("last_backup_at", latest.CompletedAt.UTC())
	if result.Error != nil {
		return fmt.Errorf("advance node backup freshness: %w", result.Error)
	}
	return nil
}

func normalizeExecutor(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

func validOpaqueReference(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) != 32 {
		return false
	}
	for _, ch := range raw {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

// ClassifiedAttemptPredicate is shared by trend, confidence and reporting
// queries. The caller supplies a trusted SQL table alias (or an empty string)
// and must still apply any date/scope predicate separately.
func ClassifiedAttemptPredicate(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = "task_runs"
	}
	return fmt.Sprintf("LOWER(COALESCE(%s.executor_type_snapshot, '')) IN ('rsync', 'restic', 'rclone') AND LOWER(COALESCE(%s.trigger_type, '')) NOT IN ('restore', 'drill')", alias, alias)
}

// LatestVerifiedForNodes returns at most one authoritative completion per node.
// Unverified historical rows are intentionally excluded.
func LatestVerifiedForNodes(ctx context.Context, db *gorm.DB, nodeIDs []uint) (map[uint]model.BackupCompletion, error) {
	result := make(map[uint]model.BackupCompletion)
	if db == nil || len(nodeIDs) == 0 {
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var rows []model.BackupCompletion
	if err := db.WithContext(ctx).Where("node_id IN ? AND evidence_status = ?", nodeIDs, model.BackupCompletionEvidenceVerified).Order("completed_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query latest backup completion facts: %w", err)
	}
	for _, row := range rows {
		if _, exists := result[row.NodeID]; !exists {
			result[row.NodeID] = row
		}
	}
	return result, nil
}

// LatestVerifiedForTasks returns the newest authoritative completion across
// the supplied tasks. The result is nil when no durable fact exists.
func LatestVerifiedForTasks(ctx context.Context, db *gorm.DB, taskIDs []uint) (*model.BackupCompletion, error) {
	if db == nil || len(taskIDs) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var row model.BackupCompletion
	query := db.WithContext(ctx).Where("task_id IN ? AND evidence_status = ?", taskIDs, model.BackupCompletionEvidenceVerified).Order("completed_at DESC, id DESC").First(&row)
	if errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if query.Error != nil {
		return nil, fmt.Errorf("query task backup completion fact: %w", query.Error)
	}
	return &row, nil
}

// VerifiedForTasks returns authoritative facts for RPO/reporting calculations.
func VerifiedForTasks(ctx context.Context, db *gorm.DB, taskIDs []uint, start, end *time.Time, limit int) ([]model.BackupCompletion, error) {
	if db == nil || len(taskIDs) == 0 {
		return []model.BackupCompletion{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	query := db.WithContext(ctx).Where("task_id IN ? AND evidence_status = ?", taskIDs, model.BackupCompletionEvidenceVerified)
	if start != nil {
		query = query.Where("completed_at >= ?", start.UTC())
	}
	if end != nil {
		query = query.Where("completed_at < ?", end.UTC())
	}
	query = query.Order("completed_at DESC, id DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var rows []model.BackupCompletion
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query backup completion facts: %w", err)
	}
	return rows, nil
}
