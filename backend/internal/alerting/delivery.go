package alerting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"gorm.io/gorm"
)

// deliveryLeaseDuration bounds the time for which a process owns a delivery
// attempt. Network I/O is performed after claimDelivery commits, so an expired
// lease can be taken over by another worker without allowing the old result to
// overwrite the newer attempt.
const deliveryLeaseDuration = 2 * time.Minute

func deliveryIntentKey(alertID, integrationID uint) string {
	return fmt.Sprintf("%d:%d", alertID, integrationID)
}

func newDeliveryAttemptID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate delivery attempt id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// claimDelivery atomically changes one logical intent into a leased sending
// attempt. The UPDATE predicate, rather than an in-memory mutex, is the
// concurrency authority and works for both SQLite and PostgreSQL. A contender
// observing zero rows affected simply lost the race and must not send.
func claimDelivery(ctx context.Context, db *gorm.DB, deliveryID uint, now time.Time, force bool) (model.AlertDelivery, bool, error) {
	var claimed model.AlertDelivery
	if db == nil || deliveryID == 0 {
		return claimed, false, errors.New("claim delivery: persistence unavailable")
	}
	attemptID, err := newDeliveryAttemptID()
	if err != nil {
		return claimed, false, err
	}

	query := db.WithContext(ctx).Model(&model.AlertDelivery{}).
		Where("id = ?", deliveryID).
		Where("(decision = ? OR decision = '' OR decision IS NULL)", "deliver")
	if force {
		query = query.Where(
			"status IN ? OR (status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?)",
			[]string{model.AlertDeliveryStatusPending, model.AlertDeliveryStatusRetrying, model.AlertDeliveryStatusFailed},
			model.AlertDeliveryStatusSending, now,
		)
	} else {
		query = query.Where(
			"status = ? OR (status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR (status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?)",
			model.AlertDeliveryStatusPending,
			model.AlertDeliveryStatusRetrying, now,
			model.AlertDeliveryStatusSending, now,
		)
	}
	// A retrying row should never retain a live lease, but this extra predicate
	// makes a malformed/manual row fail closed instead of being double-sent.
	query = query.Where("lease_expires_at IS NULL OR lease_expires_at <= ?", now)
	result := query.Updates(map[string]interface{}{
		"status":           model.AlertDeliveryStatusSending,
		"attempt_id":       attemptID,
		"lease_expires_at": now.Add(deliveryLeaseDuration),
		"attempt_count":    gorm.Expr("attempt_count + ?", 1),
		"next_retry_at":    nil,
		"last_error":       "",
		"updated_at":       now,
	})
	if result.Error != nil {
		return claimed, false, result.Error
	}
	if result.RowsAffected != 1 {
		return claimed, false, nil
	}
	claimed, ok, err := loadClaimedDelivery(ctx, db, deliveryID, attemptID, now)
	if err != nil || !ok {
		return model.AlertDelivery{}, ok, err
	}
	return claimed, true, nil
}

func loadClaimedDelivery(ctx context.Context, db *gorm.DB, deliveryID uint, attemptID string, now time.Time) (model.AlertDelivery, bool, error) {
	var claimed model.AlertDelivery
	load := db.WithContext(ctx).
		Where("id = ? AND status = ? AND attempt_id = ? AND lease_expires_at > ?", deliveryID, model.AlertDeliveryStatusSending, attemptID, now).
		First(&claimed)
	if errors.Is(load.Error, gorm.ErrRecordNotFound) {
		return model.AlertDelivery{}, false, nil
	}
	if load.Error != nil {
		return model.AlertDelivery{}, false, load.Error
	}
	return claimed, true, nil
}

// completeDelivery commits a result only for the currently leased attempt.
// RowsAffected==0 is deliberately not an error: it means a lease expired and
// another worker won, so a stale network result must be ignored.
func completeDelivery(ctx context.Context, db *gorm.DB, deliveryID uint, attemptID, status string, nextRetryAt *time.Time, lastError string) error {
	if db == nil || deliveryID == 0 || attemptID == "" {
		return errors.New("complete delivery: invalid attempt")
	}
	updates := map[string]interface{}{
		"status":           status,
		"lease_expires_at": nil,
		"next_retry_at":    nextRetryAt,
		"last_error":       lastError,
		"updated_at":       time.Now(),
	}
	result := db.WithContext(ctx).Model(&model.AlertDelivery{}).
		Where("id = ? AND status = ? AND attempt_id = ?", deliveryID, model.AlertDeliveryStatusSending, attemptID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func retryResultForFailure(attemptCount int, now time.Time, err error, terminal bool) (string, *time.Time, string) {
	lastError := util.SanitizeError(err)
	if terminal || attemptCount >= maxAttempts {
		return model.AlertDeliveryStatusFailed, nil, lastError
	}
	next := now.Add(backoffDuration(attemptCount))
	return model.AlertDeliveryStatusRetrying, &next, lastError
}

// runDeliveryAttempt is shared by first delivery, automatic retry, and manual
// retry. It intentionally performs all database claim/finalization work around
// the network call, never across it.
func runDeliveryAttempt(
	ctx context.Context,
	db *gorm.DB,
	candidate model.AlertDelivery,
	sendFn func(model.Integration, model.Alert) error,
	force bool,
) error {
	claimed, ok, err := claimDelivery(ctx, db, candidate.ID, time.Now(), force)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if sendFn == nil {
		sendFn = dispatchSingle
	}

	var integration model.Integration
	if err := db.WithContext(ctx).First(&integration, claimed.IntegrationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if completeErr := completeDelivery(ctx, db, claimed.ID, claimed.AttemptID, model.AlertDeliveryStatusFailed, nil, "integration deleted"); completeErr != nil {
				return completeErr
			}
			logger.Module("alerting").Warn().
				Uint("delivery_id", claimed.ID).
				Uint("integration_id", claimed.IntegrationID).
				Msg("integration 已删除，投递标记为 failed")
			return nil
		}
		status, next, lastError := retryResultForFailure(claimed.AttemptCount, time.Now(), err, false)
		return completeDelivery(ctx, db, claimed.ID, claimed.AttemptID, status, next, lastError)
	}

	var alert model.Alert
	if err := db.WithContext(ctx).First(&alert, claimed.AlertID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if completeErr := completeDelivery(ctx, db, claimed.ID, claimed.AttemptID, model.AlertDeliveryStatusFailed, nil, "alert deleted"); completeErr != nil {
				return completeErr
			}
			logger.Module("alerting").Warn().
				Uint("delivery_id", claimed.ID).
				Uint("alert_id", claimed.AlertID).
				Msg("alert 已删除，投递标记为 failed")
			return nil
		}
		status, next, lastError := retryResultForFailure(claimed.AttemptCount, time.Now(), err, false)
		return completeDelivery(ctx, db, claimed.ID, claimed.AttemptID, status, next, lastError)
	}

	// The lease is intentionally not held by any Go mutex during this call.
	sendErr := sendFn(integration, alert)
	if sendErr == nil {
		return completeDelivery(ctx, db, claimed.ID, claimed.AttemptID, model.AlertDeliveryStatusSent, nil, "")
	}
	status, next, lastError := retryResultForFailure(claimed.AttemptCount, time.Now(), sendErr, force)
	return completeDelivery(ctx, db, claimed.ID, claimed.AttemptID, status, next, lastError)
}
