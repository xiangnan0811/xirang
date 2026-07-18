package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const (
	actionSavedSearchCreate = "saved_search_create"
	actionSavedSearchUpdate = "saved_search_update"
	actionSavedSearchDelete = "saved_search_delete"
	actionFavoriteAdd       = "favorite_add"
	actionFavoriteRemove    = "favorite_remove"
	actionTagCreate         = "tag_create"
	actionTagUpdate         = "tag_update"
	actionTagDelete         = "tag_delete"
	actionTagAssign         = "tag_assign"
	actionTagUnassign       = "tag_unassign"
	actionRecentClear       = "recent_clear"

	resourceSavedSearch   = "saved_search"
	resourceFavorite      = "favorite"
	resourceTagDefinition = "tag_definition"
	resourceTagAssignment = "tag_assignment"
	resourceRecent        = "recent"
	resourceNone          = "none"
)

func (service *Service) loadIdempotency(
	tx *gorm.DB,
	ownerID uint,
	action, key, fingerprint string,
) (model.BackupAssetOverlayIdempotency, bool, error) {
	if !service.validIdempotencyKey(key) {
		return model.BackupAssetOverlayIdempotency{}, false, ErrInvalidOverlay
	}
	if _, err := service.lockUsage(tx, ownerID); err != nil {
		return model.BackupAssetOverlayIdempotency{}, false, err
	}
	keyHash := hashIdempotencyKey(key)
	var row model.BackupAssetOverlayIdempotency
	err := tx.Where("owner_user_id = ? AND action = ? AND key_hash = ?", ownerID, action, keyHash).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	if !service.utcNow().Before(row.ExpiresAt.UTC()) {
		if err := tx.Delete(&row).Error; err != nil {
			return row, false, err
		}
		return model.BackupAssetOverlayIdempotency{}, false, nil
	}
	if row.EncryptedRequestFingerprint != fingerprint {
		return row, false, ErrIdempotencyConflict
	}
	return row, true, nil
}

func (service *Service) createIdempotency(
	tx *gorm.DB,
	ownerID uint,
	action, key, fingerprint, resourceType, resourceID string,
	version int,
) error {
	if !service.validIdempotencyKey(key) {
		return ErrInvalidOverlay
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return err
	}
	now := service.utcNow()
	row := model.BackupAssetOverlayIdempotency{
		ID: id, OwnerUserID: ownerID, Action: action, KeyHash: hashIdempotencyKey(key),
		EncryptedRequestFingerprint: fingerprint, ResultResourceType: resourceType,
		ResultResourceID: resourceID, ResultVersion: version, CreatedAt: now, ExpiresAt: now.Add(service.config.IdempotencyTTL),
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("create overlay idempotency receipt: %w", err)
	}
	return nil
}

func (service *Service) CleanupIdempotency(ctx context.Context, limit int) (int64, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return 0, err
	}
	if limit <= 0 || limit > 100000 {
		return 0, ErrInvalidOverlay
	}
	var ids []string
	if err := service.db.WithContext(ctx).Model(&model.BackupAssetOverlayIdempotency{}).
		Where("expires_at <= ?", service.utcNow()).Order("expires_at ASC, id ASC").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := service.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.BackupAssetOverlayIdempotency{})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected > 0 {
		service.writeAudit(ctx, backupasset.AuditEventInput{
			Action: backupasset.AuditActionOverlayCleanup, Outcome: backupasset.AuditOutcomeSuccess, ItemCount: result.RowsAffected,
		})
	}
	return result.RowsAffected, nil
}

func (service *Service) validIdempotencyKey(value string) bool {
	if service == nil || len(value) < 16 || len(value) > service.config.IdempotencyKeyMaxBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' && character != '~' {
			return false
		}
	}
	return true
}

func hashIdempotencyKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
