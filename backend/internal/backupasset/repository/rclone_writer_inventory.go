package repository

import (
	"context"
	"fmt"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

// CheckFailedRcloneWriters rejects historical failed managed points for which
// the database does not contain positive evidence that every writer stopped.
// It never rewrites historical state and keeps the inventory bounded by ID.
func (service *PublicationService) CheckFailedRcloneWriters(ctx context.Context) error {
	if service == nil || service.db == nil {
		return fmt.Errorf("%w: Rclone writer inventory unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	const batchSize = 256
	lastID := ""
	for {
		var points []model.RecoveryPoint
		err := service.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
			Select("recovery_points.id", "recovery_points.consistency_json").
			Joins("JOIN backup_repositories ON backup_repositories.id = recovery_points.repository_id").
			Where("backup_repositories.provider_kind = ? AND recovery_points.state = ? AND recovery_points.semantics IN ? AND recovery_points.id > ?",
				backupasset.ProviderRclone, backupasset.RecoveryPointFailed,
				[]backupasset.PointVersionSemantics{backupasset.PointXirangManifest, backupasset.PointImportedBaseline}, lastID).
			Order("recovery_points.id ASC").Limit(batchSize).Find(&points).Error
		if err != nil {
			return fmt.Errorf("inventory failed managed Rclone writers: %w", err)
		}
		for _, point := range points {
			consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
			if err != nil {
				return fmt.Errorf("decode failed managed Rclone writer evidence: %w", err)
			}
			if consistency.Provider != "" && consistency.Provider != backupasset.ProviderRclone ||
				consistency.RcloneWriterQuiesced == nil || !*consistency.RcloneWriterQuiesced {
				return fmt.Errorf("%w: failed managed Rclone writer completion is unproven", backupasset.ErrConflict)
			}
			lastID = point.ID
		}
		if len(points) < batchSize {
			return nil
		}
	}
}
