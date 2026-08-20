package backupasset

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AuditSegmentStatus string

const (
	AuditSegmentOpen          AuditSegmentStatus = "open"
	AuditSegmentClosed        AuditSegmentStatus = "closed"
	AuditSegmentDetailsPurged AuditSegmentStatus = "details_purged"
)

var validAuditSegmentStatuses = setOf(AuditSegmentOpen, AuditSegmentClosed, AuditSegmentDetailsPurged)

const (
	defaultAuditSegmentMaxEvents int64 = 10_000
	defaultAuditSegmentMaxAge          = 24 * time.Hour
	maxAuditWriteAttempts              = 8
)

type AuditConfig struct {
	SegmentMaxEvents int64
	SegmentMaxAge    time.Duration
}

type AuditConfigSource func() (AuditConfig, error)

type AuditWriter struct {
	db           *gorm.DB
	keyring      *Keyring
	now          func() time.Time
	configSource AuditConfigSource
}

var auditWriterMu sync.Mutex

func NewAuditWriter(db *gorm.DB, keyring *Keyring, now func() time.Time, config AuditConfig) (*AuditWriter, error) {
	return NewAuditWriterWithConfigSource(db, keyring, now, func() (AuditConfig, error) { return config, nil })
}

func NewAuditWriterWithConfigSource(db *gorm.DB, keyring *Keyring, now func() time.Time, source AuditConfigSource) (*AuditWriter, error) {
	if db == nil || keyring == nil || keyring.db == nil {
		return nil, fmt.Errorf("%w: audit storage or keyring is unavailable", ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if _, err := resolveAuditConfig(source); err != nil {
		return nil, err
	}
	return &AuditWriter{db: db, keyring: keyring, now: now, configSource: source}, nil
}

func resolveAuditConfig(source AuditConfigSource) (AuditConfig, error) {
	if source == nil {
		return AuditConfig{}, fmt.Errorf("%w: audit config source is unavailable", ErrInvalidState)
	}
	config, err := source()
	if err != nil {
		return AuditConfig{}, err
	}
	if config.SegmentMaxEvents == 0 {
		config.SegmentMaxEvents = defaultAuditSegmentMaxEvents
	}
	if config.SegmentMaxAge == 0 {
		config.SegmentMaxAge = defaultAuditSegmentMaxAge
	}
	if config.SegmentMaxEvents < 1 || config.SegmentMaxEvents > 1_000_000 {
		return AuditConfig{}, fmt.Errorf("%w: invalid audit segment event limit", ErrInvalidState)
	}
	if config.SegmentMaxAge < time.Second || config.SegmentMaxAge > 168*time.Hour {
		return AuditConfig{}, fmt.Errorf("%w: invalid audit segment age", ErrInvalidState)
	}
	return config, nil
}

func (writer *AuditWriter) Write(ctx context.Context, input AuditEventInput) (model.BackupAssetAuditEvent, error) {
	record, prepared, config, err := writer.prepareWrite(ctx, input)
	if err != nil {
		return model.BackupAssetAuditEvent{}, err
	}

	auditWriterMu.Lock()
	defer auditWriterMu.Unlock()

	var lastErr error
	for attempt := 0; attempt < maxAuditWriteAttempts; attempt++ {
		candidate := record
		candidate.ID = 0
		candidate.SegmentNo = 0
		candidate.SegmentSequence = 0
		candidate.PrevHash = ""
		candidate.EntryHash = ""
		err = writer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return writer.writeTransaction(tx, &candidate, config)
		})
		if err == nil {
			return candidate, nil
		}
		lastErr = err
		if !retryableAuditWrite(err) {
			break
		}
	}
	writer.logWriteFailure(prepared, lastErr)
	return model.BackupAssetAuditEvent{}, fmt.Errorf("write backup asset audit event: %w", lastErr)
}

func (writer *AuditWriter) WriteTx(ctx context.Context, tx *gorm.DB, input AuditEventInput) error {
	if tx == nil {
		return fmt.Errorf("%w: audit transaction is unavailable", ErrInvalidState)
	}
	record, _, config, err := writer.prepareWrite(ctx, input)
	if err != nil {
		return err
	}
	auditWriterMu.Lock()
	defer auditWriterMu.Unlock()
	return writer.writeTransaction(tx.WithContext(ctx), &record, config)
}

func (writer *AuditWriter) prepareWrite(ctx context.Context, input AuditEventInput) (model.BackupAssetAuditEvent, AuditEvent, AuditConfig, error) {
	if writer == nil || writer.db == nil || writer.keyring == nil {
		return model.BackupAssetAuditEvent{}, AuditEvent{}, AuditConfig{}, fmt.Errorf("%w: audit writer is unavailable", ErrInvalidState)
	}
	config, configErr := resolveAuditConfig(writer.configSource)
	if configErr != nil {
		return model.BackupAssetAuditEvent{}, AuditEvent{}, AuditConfig{}, configErr
	}
	prepared, err := NewAuditEvent(input)
	if err != nil {
		return model.BackupAssetAuditEvent{}, AuditEvent{}, AuditConfig{}, err
	}
	fieldsJSON, err := marshalAuditFields(prepared.Fields)
	if err != nil {
		return model.BackupAssetAuditEvent{}, AuditEvent{}, AuditConfig{}, err
	}
	record := model.BackupAssetAuditEvent{
		ActorUserID:     prepared.Actor.UserID,
		ActorUsername:   prepared.Actor.Username,
		ActorRole:       prepared.Actor.Role,
		Action:          string(prepared.Action),
		Outcome:         string(prepared.Outcome),
		RepositoryID:    prepared.RepositoryID,
		RecoveryPointID: prepared.RecoveryPointID,
		EntryID:         prepared.EntryID,
		TaskID:          prepared.TaskID,
		TaskRunID:       prepared.TaskRunID,
		RecoveryJobID:   prepared.RecoveryJobID,
		ExportJobID:     prepared.ExportJobID,
		ItemCount:       prepared.ItemCount,
		ByteCount:       prepared.ByteCount,
		RangeCount:      prepared.Range.Count,
		RangeBytes:      prepared.Range.Bytes,
		StepUpAction:    prepared.StepUpAction,
		StepUpProofID:   prepared.StepUpProofID,
		GrantID:         prepared.GrantID,
		FailureCode:     prepared.FailureCode,
		FieldsJSON:      fieldsJSON,
		CreatedAt:       writer.utcNow(),
	}
	if prepared.fingerprints.Path != "" || prepared.fingerprints.Query != "" {
		material, keyErr := writer.keyring.Ensure(ctx, KeyDomainAuditFingerprint)
		if keyErr != nil {
			return model.BackupAssetAuditEvent{}, AuditEvent{}, AuditConfig{}, keyErr
		}
		version := material.Version
		record.FingerprintKeyVersion = &version
		if prepared.fingerprints.Path != "" {
			record.PathFingerprint = computeAuditFingerprint(material.Key, "path", prepared.fingerprints.Path)
		}
		if prepared.fingerprints.Query != "" {
			record.QueryFingerprint = computeAuditFingerprint(material.Key, "query", prepared.fingerprints.Query)
		}
	}
	return record, prepared, config, nil
}

func (writer *AuditWriter) writeTransaction(tx *gorm.DB, record *model.BackupAssetAuditEvent, config AuditConfig) error {
	now := record.CreatedAt.UTC()
	checkpoint, err := writer.openCheckpoint(tx, now, config)
	if err != nil {
		return err
	}

	record.SegmentNo = checkpoint.SegmentNo
	record.SegmentSequence = checkpoint.EntryCount + 1
	record.PrevHash = checkpoint.LastEntryHash
	record.EntryHash = hashAuditEvent(record)
	if err := tx.Create(record).Error; err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}

	firstEntryHash := checkpoint.FirstEntryHash
	if checkpoint.EntryCount == 0 {
		firstEntryHash = record.EntryHash
	}
	newCount := checkpoint.EntryCount + 1
	result := tx.Model(&model.BackupAssetAuditCheckpoint{}).
		Where("segment_no = ? AND status = ? AND entry_count = ? AND last_entry_hash = ?",
			checkpoint.SegmentNo, AuditSegmentOpen, checkpoint.EntryCount, checkpoint.LastEntryHash).
		Updates(map[string]any{
			"first_entry_hash": firstEntryHash,
			"last_entry_hash":  record.EntryHash,
			"entry_count":      newCount,
		})
	if result.Error != nil {
		return fmt.Errorf("advance audit checkpoint: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: audit checkpoint changed during append", ErrConflict)
	}
	checkpoint.FirstEntryHash = firstEntryHash
	checkpoint.LastEntryHash = record.EntryHash
	checkpoint.EntryCount = newCount
	if checkpoint.EntryCount >= config.SegmentMaxEvents {
		if err := closeAuditCheckpoint(tx, &checkpoint, now); err != nil {
			return err
		}
	}
	return nil
}

func (writer *AuditWriter) openCheckpoint(tx *gorm.DB, now time.Time, config AuditConfig) (model.BackupAssetAuditCheckpoint, error) {
	var openRows []model.BackupAssetAuditCheckpoint
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("status = ?", AuditSegmentOpen).
		Order("segment_no DESC").
		Limit(2).
		Find(&openRows).Error; err != nil {
		return model.BackupAssetAuditCheckpoint{}, fmt.Errorf("load open audit checkpoint: %w", err)
	}
	if len(openRows) > 1 {
		return model.BackupAssetAuditCheckpoint{}, fmt.Errorf("%w: multiple open audit segments", ErrInvalidState)
	}
	if len(openRows) == 1 {
		checkpoint := openRows[0]
		if checkpoint.EntryCount > 0 && (checkpoint.EntryCount >= config.SegmentMaxEvents || now.Sub(checkpoint.OpenedAt.UTC()) >= config.SegmentMaxAge) {
			if err := closeAuditCheckpoint(tx, &checkpoint, now); err != nil {
				return model.BackupAssetAuditCheckpoint{}, err
			}
			return createAuditCheckpoint(tx, now)
		}
		return checkpoint, nil
	}
	return createAuditCheckpoint(tx, now)
}

func createAuditCheckpoint(tx *gorm.DB, now time.Time) (model.BackupAssetAuditCheckpoint, error) {
	var latest model.BackupAssetAuditCheckpoint
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Order("segment_no DESC").First(&latest).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BackupAssetAuditCheckpoint{}, fmt.Errorf("load latest audit checkpoint: %w", err)
	}
	nextSegment := int64(1)
	previousHash := ""
	if err == nil {
		status := AuditSegmentStatus(latest.Status)
		if status == AuditSegmentOpen || latest.CheckpointHash == "" {
			return model.BackupAssetAuditCheckpoint{}, fmt.Errorf("%w: another writer created the open audit segment", ErrConflict)
		}
		nextSegment = latest.SegmentNo + 1
		previousHash = latest.CheckpointHash
	}
	checkpoint := model.BackupAssetAuditCheckpoint{
		SegmentNo:              nextSegment,
		Status:                 string(AuditSegmentOpen),
		PreviousCheckpointHash: previousHash,
		OpenedAt:               now.UTC(),
	}
	if err := tx.Create(&checkpoint).Error; err != nil {
		return model.BackupAssetAuditCheckpoint{}, fmt.Errorf("create audit checkpoint: %w", err)
	}
	return checkpoint, nil
}

func closeAuditCheckpoint(tx *gorm.DB, checkpoint *model.BackupAssetAuditCheckpoint, closedAt time.Time) error {
	if checkpoint == nil || checkpoint.Status != string(AuditSegmentOpen) || checkpoint.EntryCount <= 0 {
		return fmt.Errorf("%w: invalid open audit checkpoint", ErrInvalidState)
	}
	closedAt = closedAt.UTC()
	checkpoint.ClosedAt = &closedAt
	checkpoint.CheckpointHash = hashAuditCheckpoint(checkpoint)
	result := tx.Model(&model.BackupAssetAuditCheckpoint{}).
		Where("segment_no = ? AND status = ? AND entry_count = ? AND last_entry_hash = ?",
			checkpoint.SegmentNo, AuditSegmentOpen, checkpoint.EntryCount, checkpoint.LastEntryHash).
		Updates(map[string]any{
			"status":          AuditSegmentClosed,
			"closed_at":       closedAt,
			"checkpoint_hash": checkpoint.CheckpointHash,
		})
	if result.Error != nil {
		return fmt.Errorf("close audit checkpoint: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: audit checkpoint changed during closure", ErrConflict)
	}
	checkpoint.Status = string(AuditSegmentClosed)
	return nil
}

func (writer *AuditWriter) Verify(ctx context.Context) error {
	if writer == nil || writer.db == nil {
		return fmt.Errorf("%w: audit writer is unavailable", ErrInvalidState)
	}
	auditWriterMu.Lock()
	defer auditWriterMu.Unlock()
	return writer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var checkpoints []model.BackupAssetAuditCheckpoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Order("segment_no ASC").Find(&checkpoints).Error; err != nil {
			return fmt.Errorf("load audit checkpoints: %w", err)
		}
		for index := range checkpoints {
			checkpoint := &checkpoints[index]
			if checkpoint.SegmentNo != int64(index+1) {
				return fmt.Errorf("%w: non-contiguous audit segment", ErrInvalidState)
			}
			if index == 0 {
				if checkpoint.PreviousCheckpointHash != "" {
					return fmt.Errorf("%w: first audit segment has a predecessor", ErrInvalidState)
				}
			} else {
				previous := checkpoints[index-1]
				if previous.CheckpointHash == "" || checkpoint.PreviousCheckpointHash != previous.CheckpointHash {
					return fmt.Errorf("%w: audit checkpoint link mismatch", ErrInvalidState)
				}
			}
			if err := verifyAuditCheckpoint(tx, checkpoint, index == len(checkpoints)-1); err != nil {
				return err
			}
		}
		return nil
	})
}

func verifyAuditCheckpoint(tx *gorm.DB, checkpoint *model.BackupAssetAuditCheckpoint, isLast bool) error {
	status := AuditSegmentStatus(checkpoint.Status)
	if !validAuditSegmentStatuses[status] {
		return fmt.Errorf("%w: unknown audit segment status", ErrInvalidState)
	}
	if checkpoint.EntryCount < 0 {
		return fmt.Errorf("%w: negative audit checkpoint count", ErrInvalidState)
	}
	if status == AuditSegmentOpen {
		if !isLast || checkpoint.ClosedAt != nil || checkpoint.CheckpointHash != "" || checkpoint.DetailsPurgedAt != nil {
			return fmt.Errorf("%w: invalid open audit checkpoint", ErrInvalidState)
		}
	} else {
		if checkpoint.ClosedAt == nil || checkpoint.CheckpointHash == "" || checkpoint.CheckpointHash != hashAuditCheckpoint(checkpoint) {
			return fmt.Errorf("%w: audit checkpoint hash mismatch", ErrInvalidState)
		}
	}
	if status == AuditSegmentDetailsPurged {
		if checkpoint.DetailsPurgedAt == nil {
			return fmt.Errorf("%w: purged audit segment lacks purge time", ErrInvalidState)
		}
		var count int64
		if err := tx.Model(&model.BackupAssetAuditEvent{}).Where("segment_no = ?", checkpoint.SegmentNo).Count(&count).Error; err != nil {
			return fmt.Errorf("count purged audit events: %w", err)
		}
		if count != 0 {
			return fmt.Errorf("%w: purged audit segment retains details", ErrInvalidState)
		}
		return nil
	}
	return verifyAuditSegmentDetails(tx, checkpoint)
}

func verifyAuditSegmentDetails(tx *gorm.DB, checkpoint *model.BackupAssetAuditCheckpoint) error {
	var events []model.BackupAssetAuditEvent
	if err := tx.Where("segment_no = ?", checkpoint.SegmentNo).Order("segment_sequence ASC").Find(&events).Error; err != nil {
		return fmt.Errorf("load audit segment events: %w", err)
	}
	if int64(len(events)) != checkpoint.EntryCount {
		return fmt.Errorf("%w: audit segment count mismatch", ErrInvalidState)
	}
	previousHash := ""
	for index := range events {
		event := &events[index]
		if event.SegmentNo != checkpoint.SegmentNo || event.SegmentSequence != int64(index+1) || event.PrevHash != previousHash {
			return fmt.Errorf("%w: audit event sequence or predecessor mismatch", ErrInvalidState)
		}
		if event.EntryHash != hashAuditEvent(event) {
			return fmt.Errorf("%w: audit event hash mismatch", ErrInvalidState)
		}
		previousHash = event.EntryHash
	}
	if len(events) == 0 {
		if checkpoint.FirstEntryHash != "" || checkpoint.LastEntryHash != "" {
			return fmt.Errorf("%w: empty audit segment has entry anchors", ErrInvalidState)
		}
		return nil
	}
	if checkpoint.FirstEntryHash != events[0].EntryHash || checkpoint.LastEntryHash != events[len(events)-1].EntryHash {
		return fmt.Errorf("%w: audit segment anchor mismatch", ErrInvalidState)
	}
	return nil
}

func (writer *AuditWriter) PurgeSegmentDetails(ctx context.Context, segmentNo int64) error {
	if writer == nil || writer.db == nil || segmentNo <= 0 {
		return fmt.Errorf("%w: invalid audit segment", ErrInvalidState)
	}
	auditWriterMu.Lock()
	defer auditWriterMu.Unlock()
	return writer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var checkpoint model.BackupAssetAuditCheckpoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&checkpoint, "segment_no = ?", segmentNo).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: audit segment", ErrNotFound)
			}
			return fmt.Errorf("load audit segment for purge: %w", err)
		}
		if checkpoint.Status != string(AuditSegmentClosed) || checkpoint.ClosedAt == nil || checkpoint.CheckpointHash != hashAuditCheckpoint(&checkpoint) {
			return fmt.Errorf("%w: audit segment is not safely closed", ErrInvalidState)
		}
		if err := verifyAuditSegmentDetails(tx, &checkpoint); err != nil {
			return err
		}
		result := tx.Where("segment_no = ?", segmentNo).Delete(&model.BackupAssetAuditEvent{})
		if result.Error != nil {
			return fmt.Errorf("delete audit segment details: %w", result.Error)
		}
		if result.RowsAffected != checkpoint.EntryCount {
			return fmt.Errorf("%w: audit detail purge count mismatch", ErrConflict)
		}
		now := writer.utcNow()
		result = tx.Model(&model.BackupAssetAuditCheckpoint{}).
			Where("segment_no = ? AND status = ? AND checkpoint_hash = ?", segmentNo, AuditSegmentClosed, checkpoint.CheckpointHash).
			Updates(map[string]any{
				"status":            AuditSegmentDetailsPurged,
				"details_purged_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("mark audit details purged: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: audit checkpoint changed during purge", ErrConflict)
		}
		return nil
	})
}

func marshalAuditFields(fields map[AuditField]any) (string, error) {
	if len(fields) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("marshal safe audit fields: %w", err)
	}
	return string(encoded), nil
}

func computeAuditFingerprint(key []byte, scope, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(scope))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func hashAuditEvent(record *model.BackupAssetAuditEvent) string {
	payload := struct {
		SegmentNo             int64  `json:"segment_no"`
		SegmentSequence       int64  `json:"segment_sequence"`
		ActorUserID           uint   `json:"actor_user_id"`
		ActorUsername         string `json:"actor_username"`
		ActorRole             string `json:"actor_role"`
		Action                string `json:"action"`
		Outcome               string `json:"outcome"`
		RepositoryID          string `json:"repository_id"`
		RecoveryPointID       string `json:"recovery_point_id"`
		EntryID               string `json:"entry_id"`
		TaskID                *uint  `json:"task_id"`
		TaskRunID             *uint  `json:"task_run_id"`
		RecoveryJobID         string `json:"recovery_job_id"`
		ExportJobID           string `json:"export_job_id"`
		ItemCount             int64  `json:"item_count"`
		ByteCount             int64  `json:"byte_count"`
		RangeCount            int64  `json:"range_count"`
		RangeBytes            int64  `json:"range_bytes"`
		FingerprintKeyVersion *int   `json:"fingerprint_key_version"`
		PathFingerprint       string `json:"path_fingerprint"`
		QueryFingerprint      string `json:"query_fingerprint"`
		StepUpAction          string `json:"step_up_action"`
		StepUpProofID         string `json:"step_up_proof_id"`
		GrantID               string `json:"grant_id"`
		FailureCode           string `json:"failure_code"`
		FieldsJSON            string `json:"fields_json"`
		PrevHash              string `json:"prev_hash"`
		CreatedAt             string `json:"created_at"`
	}{
		SegmentNo:             record.SegmentNo,
		SegmentSequence:       record.SegmentSequence,
		ActorUserID:           record.ActorUserID,
		ActorUsername:         record.ActorUsername,
		ActorRole:             record.ActorRole,
		Action:                record.Action,
		Outcome:               record.Outcome,
		RepositoryID:          record.RepositoryID,
		RecoveryPointID:       record.RecoveryPointID,
		EntryID:               record.EntryID,
		TaskID:                record.TaskID,
		TaskRunID:             record.TaskRunID,
		RecoveryJobID:         record.RecoveryJobID,
		ExportJobID:           record.ExportJobID,
		ItemCount:             record.ItemCount,
		ByteCount:             record.ByteCount,
		RangeCount:            record.RangeCount,
		RangeBytes:            record.RangeBytes,
		FingerprintKeyVersion: record.FingerprintKeyVersion,
		PathFingerprint:       record.PathFingerprint,
		QueryFingerprint:      record.QueryFingerprint,
		StepUpAction:          record.StepUpAction,
		StepUpProofID:         record.StepUpProofID,
		GrantID:               record.GrantID,
		FailureCode:           record.FailureCode,
		FieldsJSON:            record.FieldsJSON,
		PrevHash:              record.PrevHash,
		CreatedAt:             record.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func hashAuditCheckpoint(checkpoint *model.BackupAssetAuditCheckpoint) string {
	closedAt := ""
	if checkpoint.ClosedAt != nil {
		closedAt = checkpoint.ClosedAt.UTC().Format(time.RFC3339Nano)
	}
	payload := struct {
		SegmentNo              int64  `json:"segment_no"`
		PreviousCheckpointHash string `json:"previous_checkpoint_hash"`
		FirstEntryHash         string `json:"first_entry_hash"`
		LastEntryHash          string `json:"last_entry_hash"`
		EntryCount             int64  `json:"entry_count"`
		OpenedAt               string `json:"opened_at"`
		ClosedAt               string `json:"closed_at"`
	}{
		SegmentNo:              checkpoint.SegmentNo,
		PreviousCheckpointHash: checkpoint.PreviousCheckpointHash,
		FirstEntryHash:         checkpoint.FirstEntryHash,
		LastEntryHash:          checkpoint.LastEntryHash,
		EntryCount:             checkpoint.EntryCount,
		OpenedAt:               checkpoint.OpenedAt.UTC().Format(time.RFC3339Nano),
		ClosedAt:               closedAt,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func retryableAuditWrite(err error) bool {
	if errors.Is(err, ErrConflict) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "database is locked") ||
		strings.Contains(lower, "database table is locked") ||
		strings.Contains(lower, "unique constraint") ||
		strings.Contains(lower, "duplicate key")
}

func (writer *AuditWriter) logWriteFailure(event AuditEvent, err error) {
	if err == nil {
		return
	}
	logEvent := logger.Module("backup_asset_audit").Warn().
		Str("action", string(event.Action)).
		Str("error_category", auditErrorCategory(err))
	if correlation, ok := event.Fields[AuditFieldCorrelationID].(string); ok && correlation != "" {
		logEvent = logEvent.Str("correlation_id", correlation)
	}
	logEvent.Msg("备份资产审计写入失败")
}

func auditErrorCategory(err error) string {
	switch {
	case errors.Is(err, ErrKeyUnavailable), errors.Is(err, ErrKeyLost):
		return "key_unavailable"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	default:
		return "storage_failure"
	}
}

func (writer *AuditWriter) utcNow() time.Time {
	return writer.now().UTC()
}
