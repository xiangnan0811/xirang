package retention

import (
	"context"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const maxAuditRetentionBatch = 1000

type AuditRetentionConfig struct {
	DetailRetentionDays     int
	CheckpointRetentionDays int
}

type AuditRetentionDependencies struct {
	DB     *gorm.DB
	Writer *backupasset.AuditWriter
	Now    func() time.Time
	Config func() (AuditRetentionConfig, error)
}

type AuditRetention struct {
	db     *gorm.DB
	writer *backupasset.AuditWriter
	now    func() time.Time
	config func() (AuditRetentionConfig, error)
}

func NewAuditRetention(dependencies AuditRetentionDependencies) (*AuditRetention, error) {
	if dependencies.DB == nil || dependencies.Config == nil {
		return nil, fmt.Errorf("%w: audit retention dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &AuditRetention{
		db: dependencies.DB, writer: dependencies.Writer, now: dependencies.Now, config: dependencies.Config,
	}, nil
}

func (service *AuditRetention) PurgeEligibleDetails(ctx context.Context, limit int) (int, error) {
	if service == nil || service.db == nil || service.config == nil {
		return 0, fmt.Errorf("%w: audit retention is unavailable", backupasset.ErrInvalidState)
	}
	if service.writer == nil {
		// A nil production audit sink is a supported no-op configuration. It
		// must not prevent the rest of retention reconciliation from running.
		return 0, nil
	}
	if limit < 1 || limit > maxAuditRetentionBatch {
		return 0, fmt.Errorf("%w: invalid audit retention batch", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := service.config()
	if err != nil {
		return 0, err
	}
	if config.DetailRetentionDays < 1 || config.DetailRetentionDays > 3650 ||
		config.CheckpointRetentionDays < 1 || config.CheckpointRetentionDays > 36500 {
		return 0, fmt.Errorf("%w: invalid audit retention settings", backupasset.ErrInvalidState)
	}
	if !service.db.Migrator().HasTable(&model.BackupAssetAuditCheckpoint{}) {
		return 0, nil
	}
	cutoff := service.now().UTC().AddDate(0, 0, -config.DetailRetentionDays)
	var latestSegment int64
	if err := service.db.WithContext(ctx).Model(&model.BackupAssetAuditCheckpoint{}).
		Select("COALESCE(MAX(segment_no), 0)").Scan(&latestSegment).Error; err != nil {
		return 0, fmt.Errorf("load latest audit segment: %w", err)
	}
	var segments []model.BackupAssetAuditCheckpoint
	query := service.db.WithContext(ctx).
		Where("status = ? AND closed_at IS NOT NULL AND closed_at <= ?", backupasset.AuditSegmentClosed, cutoff)
	if latestSegment > 0 {
		query = query.Where("segment_no < ?", latestSegment)
	}
	if err := query.Order("segment_no ASC").Limit(limit).Find(&segments).Error; err != nil {
		return 0, fmt.Errorf("load eligible audit detail segments: %w", err)
	}
	purged := 0
	for _, segment := range segments {
		if err := ctx.Err(); err != nil {
			return purged, err
		}
		if err := service.writer.PurgeSegmentDetails(ctx, segment.SegmentNo); err != nil {
			return purged, err
		}
		purged++
	}
	return purged, nil
}
