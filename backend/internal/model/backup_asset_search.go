package model

import "time"

type BackupAssetSearchGeneration struct {
	ID                    string     `gorm:"primaryKey;size:32" json:"id"`
	RecoveryPointID       string     `gorm:"size:32;not null" json:"recovery_point_id"`
	CatalogGenerationID   string     `gorm:"size:32;not null" json:"-"`
	Generation            int        `gorm:"not null" json:"generation"`
	State                 string     `gorm:"size:16;not null" json:"state"`
	IsActive              bool       `gorm:"not null;default:false" json:"is_active"`
	SourceFingerprint     string     `gorm:"size:128;not null;default:''" json:"-"`
	NormalizerVersion     int        `gorm:"not null" json:"normalizer_version"`
	SearchKeyVersion      int        `gorm:"not null" json:"-"`
	ProjectionRevision    int64      `gorm:"not null;default:1" json:"projection_revision"`
	LeaseID               string     `gorm:"size:32;not null" json:"-"`
	BuildAttemptID        string     `gorm:"size:32;not null" json:"-"`
	FenceTokenHash        string     `gorm:"size:64;not null" json:"-"`
	ExpectedDocumentCount int64      `gorm:"not null;default:0" json:"expected_document_count"`
	WrittenDocumentCount  int64      `gorm:"not null;default:0" json:"written_document_count"`
	ErrorCode             string     `gorm:"size:64;not null;default:''" json:"error_code,omitempty"`
	CorrelationID         string     `gorm:"size:64;not null;default:''" json:"-"`
	StartedAt             time.Time  `gorm:"not null" json:"started_at"`
	FinishedAt            *time.Time `json:"finished_at,omitempty"`
	CreatedAt             time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt             time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetSearchGeneration) TableName() string {
	return "backup_asset_search_generations"
}

type BackupAssetSearchDocument struct {
	SearchGenerationID     string     `gorm:"primaryKey;size:32" json:"-"`
	DocumentID             string     `gorm:"primaryKey;size:64" json:"-"`
	RecoveryPointID        string     `gorm:"size:32;not null" json:"recovery_point_id"`
	CatalogGenerationID    string     `gorm:"size:32;not null" json:"-"`
	EntryID                string     `gorm:"size:64;not null" json:"entry_id"`
	Sensitivity            string     `gorm:"size:16;not null" json:"sensitivity"`
	ClassificationRevision int        `gorm:"not null" json:"classification_revision"`
	MetadataRevision       int        `gorm:"not null" json:"metadata_revision"`
	EntryType              string     `gorm:"size:16;not null" json:"entry_type"`
	ModifiedAt             *time.Time `json:"modified_at,omitempty"`
	LineageToken           string     `gorm:"size:64;not null" json:"-"`
	PathGroupToken         string     `gorm:"size:64;not null" json:"-"`
	PathSortKey            string     `gorm:"type:text;not null" json:"-"`
	NameSortKey            string     `gorm:"type:text;not null" json:"-"`
	CreatedAt              time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt              time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetSearchDocument) TableName() string {
	return "backup_asset_search_documents"
}

type BackupAssetSearchPosting struct {
	SearchGenerationID string `gorm:"size:32;not null" json:"-"`
	DocumentID         string `gorm:"size:64;not null" json:"-"`
	Field              string `gorm:"size:32;not null" json:"-"`
	TokenKind          string `gorm:"size:16;not null" json:"-"`
	KeyVersion         int    `gorm:"not null" json:"-"`
	TokenHMAC          string `gorm:"size:64;not null" json:"-"`
	TermFrequency      int    `gorm:"not null" json:"-"`
}

func (BackupAssetSearchPosting) TableName() string {
	return "backup_asset_search_postings"
}

type BackupAssetSearchDocumentField struct {
	SearchGenerationID     string    `gorm:"size:32;not null" json:"-"`
	DocumentID             string    `gorm:"size:64;not null" json:"-"`
	Field                  string    `gorm:"size:32;not null" json:"field"`
	State                  string    `gorm:"size:16;not null" json:"state"`
	CoverageRevision       int       `gorm:"not null" json:"coverage_revision"`
	ClassificationRevision int       `gorm:"not null" json:"classification_revision"`
	PipelineRevision       int       `gorm:"not null" json:"pipeline_revision"`
	IndexRevision          int       `gorm:"not null" json:"index_revision"`
	SourceFingerprint      string    `gorm:"size:128;not null;default:''" json:"-"`
	ExcerptRef             *string   `gorm:"size:32" json:"-"`
	UpdatedAt              time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupAssetSearchDocumentField) TableName() string {
	return "backup_asset_search_document_fields"
}
