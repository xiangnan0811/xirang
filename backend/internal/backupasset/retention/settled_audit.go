package retention

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

var errSettledAuditPending = errors.New("settled deletion audit retry is pending")

type settledAuditWriteResult uint8

const (
	settledAuditNotCandidate settledAuditWriteResult = iota
	settledAuditEmitted
	settledAuditPending
	settledAuditTerminalEmitted
	settledAuditTerminalExisting
)

func settledDeletionAuditInput(
	attempt model.RecoveryPointLifecycleAttempt,
	point model.RecoveryPoint,
	status string,
) backupasset.AuditEventInput {
	outcome := backupasset.AuditOutcomeSuccess
	if status == "blocked" || status == "identity_conflict" {
		outcome = backupasset.AuditOutcomeBlocked
	}
	return backupasset.AuditEventInput{
		Action:          backupasset.AuditActionRepositoryPurge,
		Outcome:         outcome,
		RepositoryID:    point.RepositoryID,
		RecoveryPointID: point.ID,
		ItemCount:       1,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage:     "settled",
			backupasset.AuditFieldStatus:    status,
			backupasset.AuditFieldItemCount: int64(1),
			backupasset.AuditFieldSource:    attempt.ID,
		},
	}
}

// settledAuditCandidate is the single runtime candidate predicate. Mutable
// retirement is intentionally not included: its tombstone is a lifecycle
// history fact, not a provider deletion settlement.
func settledAuditCandidate(rows providerDeleteRows) (string, bool, error) {
	if !providerDeleteLifecycleOperation(backupasset.LifecycleOperation(rows.attempt.Operation)) {
		return "", false, nil
	}
	result, proofFound, err := validateProviderDeleteRowsProofFirst(rows)
	if err != nil {
		return "", false, err
	}
	if proofFound {
		if result.Outcome == PointDeletionAlreadyAbsent {
			return "already_absent", true, nil
		}
		return "deleted", true, nil
	}
	if backupasset.LifecyclePhase(rows.attempt.Phase) != backupasset.LifecyclePhaseBlocked {
		return "", false, nil
	}
	reason := backupasset.LifecycleBlockedReason(rows.attempt.BlockedReason)
	if !providerDeletionBlocked(reason) && reason != backupasset.LifecycleBlockedActiveHold {
		return "", false, nil
	}
	return settledDeletionStatus(reason), true, nil
}

// validateSettledAuditSlots scans the complete locked slot set before a
// matching status is allowed to serve as the idempotency proof. Slot IDs are
// opaque and therefore cannot be used to infer lifecycle chronology.
func validateSettledAuditSlots(
	slots []model.RecoveryPointLifecycleAuditSlot,
	attemptID string,
	status string,
) (bool, error) {
	duplicate := false
	terminalStatus := ""
	var terminalAt time.Time
	var latestObservationAt time.Time
	for _, slot := range slots {
		if backupasset.ValidateOpaqueID(slot.ID) != nil ||
			slot.AttemptID != attemptID ||
			slot.EmittedAt.IsZero() || slot.CreatedAt.IsZero() {
			return false, fmt.Errorf("%w: malformed settled deletion audit slot", backupasset.ErrInvalidState)
		}
		switch slot.Status {
		case "deleted", "already_absent":
			if terminalStatus != "" && terminalStatus != slot.Status {
				return false, fmt.Errorf("%w: conflicting settled deletion audit terminal slots", backupasset.ErrInvalidState)
			}
			terminalStatus = slot.Status
			if terminalAt.IsZero() || slot.EmittedAt.Before(terminalAt) {
				terminalAt = slot.EmittedAt
			}
		case "blocked", "identity_conflict":
			if latestObservationAt.IsZero() || latestObservationAt.Before(slot.EmittedAt) {
				latestObservationAt = slot.EmittedAt
			}
		default:
			return false, fmt.Errorf("%w: invalid settled deletion audit slot status", backupasset.ErrInvalidState)
		}
		if slot.Status == status {
			// Record the duplicate, but defer the no-op until every locked
			// slot has passed validation and consistency checks.
			duplicate = true
		}
	}
	if terminalStatus != "" {
		if !latestObservationAt.IsZero() && latestObservationAt.After(terminalAt) {
			return false, fmt.Errorf("%w: settled deletion audit status follows terminal", backupasset.ErrInvalidState)
		}
		if terminalStatus != status {
			return false, fmt.Errorf("%w: settled deletion audit status follows terminal", backupasset.ErrInvalidState)
		}
	}
	return duplicate, nil
}

// emitSettledDeletionAuditTx is the only settled-audit writer. It re-derives
// lifecycle truth while holding the repository-first lock set, checks the
// durable retry gate, and commits WriteTx plus the permanent slot together.
func (coordinator *Coordinator) emitSettledDeletionAuditTx(
	ctx context.Context,
	attemptID string,
) (settledAuditWriteResult, error) {
	if coordinator == nil || coordinator.audit == nil {
		return settledAuditNotCandidate, nil
	}
	if backupasset.ValidateOpaqueID(attemptID) != nil {
		return settledAuditNotCandidate, fmt.Errorf("%w: settled deletion audit identifiers", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result settledAuditWriteResult
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if providerDeleteLifecycleOperation(backupasset.LifecycleOperation(rows.attempt.Operation)) {
			if _, _, err := coordinator.validateProviderDeleteRowsAuthority(
				rows, rows.claimFound && !rows.tombstoneFound, true,
			); err != nil {
				return err
			}
		}
		status, candidate, candidateErr := settledAuditCandidate(rows)
		if candidateErr != nil {
			return candidateErr
		}
		if !candidate {
			result = settledAuditNotCandidate
			return nil
		}
		if err := requireProviderDeleteLease(rows); err != nil {
			return err
		}
		if backupasset.ValidateOpaqueID(rows.repository.ID) != nil ||
			backupasset.ValidateOpaqueID(rows.point.ID) != nil {
			return fmt.Errorf("%w: settled deletion audit identifiers", backupasset.ErrInvalidState)
		}

		var slots []model.RecoveryPointLifecycleAuditSlot
		loaded := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("attempt_id = ?", rows.attempt.ID).
			Order("id ASC").
			Find(&slots)
		if loaded.Error != nil {
			return fmt.Errorf("load settled deletion audit slots: %w", loaded.Error)
		}
		duplicate, err := validateSettledAuditSlots(slots, rows.attempt.ID, status)
		if err != nil {
			return err
		}
		now := coordinator.now().UTC()
		retryPending := rows.attempt.RetryAt != nil &&
			now.Before(rows.attempt.RetryAt.UTC())
		if duplicate {
			// The immutable slot is the idempotency proof. A duplicate
			// caller must not invoke WriteTx again. A terminal slot also
			// proves that provider proof may be progressed despite a stale
			// retry gate; an observation slot remains gated until due.
			if status == "deleted" || status == "already_absent" {
				result = settledAuditTerminalExisting
			} else if retryPending {
				result = settledAuditPending
			} else {
				result = settledAuditNotCandidate
			}
			return nil
		}
		if retryPending {
			result = settledAuditPending
			return nil
		}
		input := settledDeletionAuditInput(rows.attempt, rows.point, status)
		if err := coordinator.audit.WriteTx(ctx, tx, input); err != nil {
			return fmt.Errorf("%s: %w", providerDeleteStageAudit, err)
		}
		slotID, err := backupasset.NewOpaqueID()
		if err != nil {
			return fmt.Errorf("generate settled deletion audit slot id: %w", err)
		}
		slot := model.RecoveryPointLifecycleAuditSlot{
			ID: slotID, AttemptID: rows.attempt.ID, Status: status,
			EmittedAt: now, CreatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&slot).Error; err != nil {
			return fmt.Errorf("persist settled deletion audit slot: %w", err)
		}
		if status == "deleted" || status == "already_absent" {
			result = settledAuditTerminalEmitted
		} else {
			result = settledAuditEmitted
		}
		return nil
	})
	if err != nil {
		return settledAuditNotCandidate, err
	}
	return result, nil
}

func (coordinator *Coordinator) writeSettledDeletionAudit(
	ctx context.Context,
	attempt LifecycleAttempt,
) error {
	result, err := coordinator.emitSettledDeletionAuditTx(ctx, attempt.ID)
	if err != nil {
		return err
	}
	if result == settledAuditPending {
		return errSettledAuditPending
	}
	return nil
}

func (coordinator *Coordinator) flushDueSettledAuditBeforeHeartbeat(
	ctx context.Context,
	attempt LifecycleAttempt,
) (bool, error) {
	if coordinator == nil || coordinator.audit == nil {
		return false, nil
	}
	result, err := coordinator.emitSettledDeletionAuditTx(ctx, attempt.ID)
	if err != nil {
		return false, err
	}
	if result == settledAuditPending {
		// A future retry gate is authoritative. Do not progress provider proof
		// or mutate any lifecycle-owned row before the gate is due.
		return true, nil
	}
	if result != settledAuditTerminalEmitted && result != settledAuditTerminalExisting {
		return false, nil
	}
	if _, err := coordinator.progressProviderProof(ctx, attempt.ID); err != nil {
		return false, err
	}
	return false, nil
}

// scheduleSettledAuditRetry is the sole retry_at writer for settled-audit
// failures. It only CASes retry_at; facts and audit slots are written by the
// locked emission transaction above.
func (coordinator *Coordinator) scheduleSettledAuditRetry(
	ctx context.Context,
	attemptID string,
) (LifecycleAttempt, error) {
	if coordinator == nil || coordinator.db == nil ||
		backupasset.ValidateOpaqueID(attemptID) != nil {
		return LifecycleAttempt{}, fmt.Errorf("%w: invalid settled audit retry attempt", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var scheduled LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if providerDeleteLifecycleOperation(backupasset.LifecycleOperation(rows.attempt.Operation)) {
			if _, _, err := coordinator.validateProviderDeleteRowsAuthority(
				rows, rows.claimFound && !rows.tombstoneFound, true,
			); err != nil {
				return err
			}
		}
		status, candidate, err := settledAuditCandidate(rows)
		if err != nil {
			return err
		}
		scheduled = lifecycleAttemptFromModel(rows.attempt)
		if !candidate {
			return nil
		}
		if err := requireProviderDeleteLease(rows); err != nil {
			return err
		}
		var slots []model.RecoveryPointLifecycleAuditSlot
		loaded := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("attempt_id = ?", rows.attempt.ID).
			Order("id ASC").
			Find(&slots)
		if loaded.Error != nil {
			return fmt.Errorf("load settled deletion audit slots: %w", loaded.Error)
		}
		duplicate, err := validateSettledAuditSlots(slots, rows.attempt.ID, status)
		if err != nil {
			return err
		}
		if duplicate {
			return nil
		}
		now := coordinator.now().UTC()
		if rows.attempt.RetryAt != nil && now.Before(rows.attempt.RetryAt.UTC()) {
			return nil
		}
		retryAt := now.Add(coordinator.retryDelay)
		update := tx.WithContext(ctx).
			Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND transition_revision = ?",
				rows.attempt.ID, rows.attempt.TransitionRevision).
			Update("retry_at", retryAt)
		if update.Error != nil {
			return fmt.Errorf("schedule settled deletion audit retry: %w", update.Error)
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
		}
		rows.attempt.RetryAt = &retryAt
		scheduled = lifecycleAttemptFromModel(rows.attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return scheduled, nil
}
