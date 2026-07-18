package backupasset

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
)

type SettingsReader interface {
	GetEffective(key string) string
}

type BackupAssetSettingsSnapshotReader interface {
	BackupAssetSettingsSnapshot() (map[string]string, error)
}

type FoundationService struct {
	settings SettingsReader
}

type ProviderConfig struct {
	OperationTimeout   time.Duration
	MaxConcurrency     int
	MetadataLimitBytes int64
}

type CatalogConfig struct {
	Enabled           bool
	BatchSize         int
	BuildTimeout      time.Duration
	ReconcileInterval time.Duration
	MaxConcurrency    int
	MaxEntries        int64
	Lease             LeaseConfig
}

type SearchConfig struct {
	Enabled           bool
	ReconcileInterval time.Duration
	BuildTimeout      time.Duration
	BatchSize         int
	MaxConcurrency    int
	ASTMaxDepth       int
	ASTMaxNodes       int
	ValuesPerNode     int
	BodyMaxBytes      int
	ValueMaxBytes     int
	CandidateLimit    int
	QueryTimeout      time.Duration
	PageSizeMax       int
	SuggestionLimit   int
	MaxDocuments      int64
	Lease             LeaseConfig
}

type OverlayConfig struct {
	Enabled                bool
	SavedSearchQuota       int64
	FavoriteQuota          int64
	TagDefinitionQuota     int64
	TagAssignmentQuota     int64
	BulkMaxItems           int
	LabelMaxBytes          int
	RecentQuota            int64
	RecentRetention        time.Duration
	RecentWritesPerMinute  int64
	IdempotencyTTL         time.Duration
	IdempotencyKeyMaxBytes int
}

func NewFoundationService(reader SettingsReader) *FoundationService {
	return &FoundationService{settings: reader}
}

func (service *FoundationService) Enabled() bool {
	enabled, err := service.FeatureEnabled()
	return err == nil && enabled
}

// FeatureEnabled returns the effective dynamic feature value together with a
// validation error. Callers that enforce admission transitions must not treat a
// settings-resolution failure as a harmless disabled value.
func (service *FoundationService) FeatureEnabled() (bool, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return false, err
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(values["backup_assets.enabled"]))
	if err != nil {
		return false, fmt.Errorf("%w: parse backup_assets.enabled: %v", ErrInvalidState, err)
	}
	return enabled, nil
}

func (service *FoundationService) LeaseConfig() (LeaseConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return LeaseConfig{}, err
	}
	duration, err := parseFoundationDuration(values, "backup_assets.lease_duration")
	if err != nil {
		return LeaseConfig{}, err
	}
	heartbeat, err := parseFoundationDuration(values, "backup_assets.lease_heartbeat")
	if err != nil {
		return LeaseConfig{}, err
	}
	absoluteDeadline, err := parseFoundationDuration(values, "backup_assets.lease_absolute_deadline")
	if err != nil {
		return LeaseConfig{}, err
	}
	return LeaseConfig{Duration: duration, Heartbeat: heartbeat, AbsoluteDeadline: absoluteDeadline}, nil
}

func (service *FoundationService) ProviderConfig() (ProviderConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return ProviderConfig{}, err
	}
	operationTimeout, err := parseFoundationDuration(values, "backup_assets.provider_operation_timeout")
	if err != nil {
		return ProviderConfig{}, err
	}
	maxConcurrency, err := parseFoundationInt(values, "backup_assets.provider_max_concurrency")
	if err != nil {
		return ProviderConfig{}, err
	}
	metadataLimitBytes, err := parseFoundationInt64(values, "backup_assets.provider_metadata_limit_bytes")
	if err != nil {
		return ProviderConfig{}, err
	}
	return ProviderConfig{
		OperationTimeout:   operationTimeout,
		MaxConcurrency:     maxConcurrency,
		MetadataLimitBytes: metadataLimitBytes,
	}, nil
}

func (service *FoundationService) CatalogConfig() (CatalogConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return CatalogConfig{}, err
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(values["backup_assets.enabled"]))
	if err != nil {
		return CatalogConfig{}, fmt.Errorf("%w: parse backup_assets.enabled: %v", ErrInvalidState, err)
	}
	batchSize, err := parseFoundationInt(values, "backup_assets.catalog_batch_size")
	if err != nil {
		return CatalogConfig{}, err
	}
	buildTimeout, err := parseFoundationDuration(values, "backup_assets.catalog_build_timeout")
	if err != nil {
		return CatalogConfig{}, err
	}
	reconcileInterval, err := parseFoundationDuration(values, "backup_assets.repository_reconcile_interval")
	if err != nil {
		return CatalogConfig{}, err
	}
	maxConcurrency, err := parseFoundationInt(values, "backup_assets.provider_max_concurrency")
	if err != nil {
		return CatalogConfig{}, err
	}
	maxEntries, err := parseFoundationInt64(values, "backup_assets.manifest_max_entries")
	if err != nil {
		return CatalogConfig{}, err
	}
	leaseDuration, err := parseFoundationDuration(values, "backup_assets.lease_duration")
	if err != nil {
		return CatalogConfig{}, err
	}
	leaseHeartbeat, err := parseFoundationDuration(values, "backup_assets.lease_heartbeat")
	if err != nil {
		return CatalogConfig{}, err
	}
	leaseDeadline, err := parseFoundationDuration(values, "backup_assets.lease_absolute_deadline")
	if err != nil {
		return CatalogConfig{}, err
	}
	if batchSize < 1 || batchSize > 100000 || buildTimeout < time.Minute || buildTimeout > 24*time.Hour ||
		reconcileInterval < time.Minute || reconcileInterval > 24*time.Hour || maxConcurrency < 1 || maxConcurrency > 32 ||
		maxEntries < 1 || leaseDuration < 30*time.Second || leaseDuration > 30*time.Minute ||
		leaseHeartbeat < 10*time.Second || leaseHeartbeat >= leaseDuration || leaseDeadline < leaseDuration || leaseDeadline > 168*time.Hour {
		return CatalogConfig{}, fmt.Errorf("%w: invalid Catalog settings", ErrInvalidState)
	}
	return CatalogConfig{
		Enabled: enabled, BatchSize: batchSize, BuildTimeout: buildTimeout, ReconcileInterval: reconcileInterval,
		MaxConcurrency: maxConcurrency, MaxEntries: maxEntries,
		Lease: LeaseConfig{Duration: leaseDuration, Heartbeat: leaseHeartbeat, AbsoluteDeadline: leaseDeadline},
	}, nil
}

func (service *FoundationService) SearchOverlayConfig() (SearchConfig, OverlayConfig, error) {
	if service == nil || service.settings == nil {
		return SearchConfig{}, OverlayConfig{}, fmt.Errorf("%w: settings service is unavailable", ErrInvalidState)
	}
	reader, ok := service.settings.(BackupAssetSettingsSnapshotReader)
	if !ok {
		return SearchConfig{}, OverlayConfig{}, fmt.Errorf("%w: atomic backup asset settings snapshot is unavailable", ErrInvalidState)
	}
	values, err := reader.BackupAssetSettingsSnapshot()
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, fmt.Errorf("%w: read backup asset settings snapshot: %v", ErrInvalidState, err)
	}
	for _, key := range settings.BackupAssetFoundationSettingKeys() {
		if _, exists := values[key]; !exists {
			return SearchConfig{}, OverlayConfig{}, fmt.Errorf("%w: incomplete backup asset settings snapshot", ErrInvalidState)
		}
	}
	if err := settings.ValidateBackupAssetFoundationConfig(values); err != nil {
		return SearchConfig{}, OverlayConfig{}, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}

	enabled, err := parseFoundationBool(values, "backup_assets.enabled")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	reconcileInterval, err := parseFoundationDuration(values, "backup_assets.search_reconcile_interval")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	buildTimeout, err := parseFoundationDuration(values, "backup_assets.search_build_timeout")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	batchSize, err := parseFoundationInt(values, "backup_assets.search_batch_size")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	maxConcurrency, err := parseFoundationInt(values, "backup_assets.search_max_concurrency")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	astMaxDepth, err := parseFoundationInt(values, "backup_assets.search_ast_max_depth")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	astMaxNodes, err := parseFoundationInt(values, "backup_assets.search_ast_max_nodes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	valuesPerNode, err := parseFoundationInt(values, "backup_assets.search_values_per_node")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	bodyMaxBytes, err := parseFoundationInt(values, "backup_assets.search_body_max_bytes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	valueMaxBytes, err := parseFoundationInt(values, "backup_assets.search_value_max_bytes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	candidateLimit, err := parseFoundationInt(values, "backup_assets.search_candidate_limit")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	queryTimeout, err := parseFoundationDuration(values, "backup_assets.search_query_timeout")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	pageSizeMax, err := parseFoundationInt(values, "backup_assets.search_page_size_max")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	suggestionLimit, err := parseFoundationInt(values, "backup_assets.search_suggestion_limit")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	maxDocuments, err := parseFoundationInt64(values, "backup_assets.manifest_max_entries")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	leaseDuration, err := parseFoundationDuration(values, "backup_assets.lease_duration")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	leaseHeartbeat, err := parseFoundationDuration(values, "backup_assets.lease_heartbeat")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	leaseDeadline, err := parseFoundationDuration(values, "backup_assets.lease_absolute_deadline")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}

	savedSearchQuota, err := parseFoundationInt64(values, "backup_assets.saved_search_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	favoriteQuota, err := parseFoundationInt64(values, "backup_assets.favorite_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	tagDefinitionQuota, err := parseFoundationInt64(values, "backup_assets.tag_definition_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	tagAssignmentQuota, err := parseFoundationInt64(values, "backup_assets.tag_assignment_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	bulkMaxItems, err := parseFoundationInt(values, "backup_assets.overlay_bulk_max_items")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	labelMaxBytes, err := parseFoundationInt(values, "backup_assets.overlay_label_max_bytes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	recentQuota, err := parseFoundationInt64(values, "backup_assets.recent_quota")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	recentRetention, err := parseFoundationDuration(values, "backup_assets.recent_retention")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	recentWritesPerMinute, err := parseFoundationInt64(values, "backup_assets.recent_writes_per_minute")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	idempotencyTTL, err := parseFoundationDuration(values, "backup_assets.idempotency_ttl")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}
	idempotencyKeyMaxBytes, err := parseFoundationInt(values, "backup_assets.idempotency_key_max_bytes")
	if err != nil {
		return SearchConfig{}, OverlayConfig{}, err
	}

	searchConfig := SearchConfig{
		Enabled: enabled, ReconcileInterval: reconcileInterval, BuildTimeout: buildTimeout,
		BatchSize: batchSize, MaxConcurrency: maxConcurrency, ASTMaxDepth: astMaxDepth, ASTMaxNodes: astMaxNodes,
		ValuesPerNode: valuesPerNode, BodyMaxBytes: bodyMaxBytes, ValueMaxBytes: valueMaxBytes,
		CandidateLimit: candidateLimit, QueryTimeout: queryTimeout, PageSizeMax: pageSizeMax, SuggestionLimit: suggestionLimit,
		MaxDocuments: maxDocuments, Lease: LeaseConfig{Duration: leaseDuration, Heartbeat: leaseHeartbeat, AbsoluteDeadline: leaseDeadline},
	}
	overlayConfig := OverlayConfig{
		Enabled: enabled, SavedSearchQuota: savedSearchQuota, FavoriteQuota: favoriteQuota,
		TagDefinitionQuota: tagDefinitionQuota, TagAssignmentQuota: tagAssignmentQuota,
		BulkMaxItems: bulkMaxItems, LabelMaxBytes: labelMaxBytes, RecentQuota: recentQuota,
		RecentRetention: recentRetention, RecentWritesPerMinute: recentWritesPerMinute,
		IdempotencyTTL: idempotencyTTL, IdempotencyKeyMaxBytes: idempotencyKeyMaxBytes,
	}
	return searchConfig, overlayConfig, nil
}

func (service *FoundationService) SearchConfig() (SearchConfig, error) {
	config, _, err := service.SearchOverlayConfig()
	return config, err
}

func (service *FoundationService) OverlayConfig() (OverlayConfig, error) {
	_, config, err := service.SearchOverlayConfig()
	return config, err
}

func (service *FoundationService) AuditConfig() (AuditConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return AuditConfig{}, err
	}
	segmentMaxEvents, err := parseFoundationInt64(values, "backup_assets.audit_segment_max_events")
	if err != nil {
		return AuditConfig{}, err
	}
	segmentMaxAge, err := parseFoundationDuration(values, "backup_assets.audit_segment_max_age")
	if err != nil {
		return AuditConfig{}, err
	}
	return AuditConfig{SegmentMaxEvents: segmentMaxEvents, SegmentMaxAge: segmentMaxAge}, nil
}

func (service *FoundationService) PublicationConfig() (PublicationConfig, error) {
	values, err := service.effectiveFoundationValues()
	if err != nil {
		return PublicationConfig{}, err
	}
	reconcileInterval, err := parseFoundationDuration(values, "backup_assets.publication_reconcile_interval")
	if err != nil {
		return PublicationConfig{}, err
	}
	reconcileBatchSize, err := parseFoundationInt(values, "backup_assets.publication_reconcile_batch_size")
	if err != nil {
		return PublicationConfig{}, err
	}
	workerConcurrency, err := parseFoundationInt(values, "backup_assets.publication_worker_concurrency")
	if err != nil {
		return PublicationConfig{}, err
	}
	missingGrace, err := parseFoundationDuration(values, "backup_assets.publication_missing_grace")
	if err != nil {
		return PublicationConfig{}, err
	}
	backupStreamMaxBytes, err := parseFoundationInt64(values, "backup_assets.publication_stream_max_bytes")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestTimeout, err := parseFoundationDuration(values, "backup_assets.manifest_timeout")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestMaxBytes, err := parseFoundationInt64(values, "backup_assets.manifest_max_bytes")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestMaxEntries, err := parseFoundationInt64(values, "backup_assets.manifest_max_entries")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestMaxRecordBytes, err := parseFoundationInt(values, "backup_assets.manifest_max_record_bytes")
	if err != nil {
		return PublicationConfig{}, err
	}
	manifestMaxDepth, err := parseFoundationInt(values, "backup_assets.manifest_max_depth")
	if err != nil {
		return PublicationConfig{}, err
	}
	rcloneConfig, err := parseRclonePublicationConfig(values)
	if err != nil {
		return PublicationConfig{}, err
	}
	return PublicationConfig{
		ReconcileInterval:      reconcileInterval,
		ReconcileBatchSize:     reconcileBatchSize,
		WorkerConcurrency:      workerConcurrency,
		MissingGrace:           missingGrace,
		BackupStreamMaxBytes:   backupStreamMaxBytes,
		ManifestTimeout:        manifestTimeout,
		ManifestMaxBytes:       manifestMaxBytes,
		ManifestMaxEntries:     manifestMaxEntries,
		ManifestMaxRecordBytes: manifestMaxRecordBytes,
		ManifestMaxDepth:       manifestMaxDepth,
		Rclone:                 rcloneConfig,
	}, nil
}

func parseRclonePublicationConfig(values map[string]string) (RclonePublicationConfig, error) {
	preflightTTL, err := parseFoundationDuration(values, "backup_assets.rclone_preflight_ttl")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	portableDeadline, err := parseFoundationDuration(values, "backup_assets.rclone_portable_deadline")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	nativeDeadline, err := parseFoundationDuration(values, "backup_assets.rclone_native_deadline")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	boundConfigMaxBytes, err := parseFoundationInt64(values, "backup_assets.rclone_bound_config_max_bytes")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	controlPayloadMaxBytes, err := parseFoundationInt64(values, "backup_assets.rclone_control_payload_max_bytes")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	fullVerifyMaxBytes, err := parseFoundationInt64(values, "backup_assets.rclone_full_verify_max_bytes")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	manifestChunkMaxBytes, err := parseFoundationInt64(values, "backup_assets.rclone_manifest_chunk_max_bytes")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	lowLevelRetries, err := parseFoundationInt(values, "backup_assets.rclone_low_level_retries")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	stagingOrphanAge, err := parseFoundationDuration(values, "backup_assets.rclone_staging_orphan_age")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	stagingScanLimit, err := parseFoundationInt(values, "backup_assets.rclone_staging_scan_limit")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	kmsReadKeyMaxCount, err := parseFoundationInt(values, "backup_assets.rclone_kms_read_key_max_count")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	healthInterval, err := parseFoundationDuration(values, "backup_assets.rclone_health_interval")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	healthBatchSize, err := parseFoundationInt(values, "backup_assets.rclone_health_batch_size")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	awsSDKMaxAttempts, err := parseFoundationInt(values, "backup_assets.rclone_aws_sdk_max_attempts")
	if err != nil {
		return RclonePublicationConfig{}, err
	}
	return RclonePublicationConfig{
		PreflightTTL: preflightTTL, PortableDeadline: portableDeadline, NativeDeadline: nativeDeadline,
		BoundConfigMaxBytes: boundConfigMaxBytes, ControlPayloadMaxBytes: controlPayloadMaxBytes,
		FullVerifyMaxBytes: fullVerifyMaxBytes, ManifestChunkMaxBytes: manifestChunkMaxBytes,
		LowLevelRetries: lowLevelRetries, StagingOrphanAge: stagingOrphanAge, StagingScanLimit: stagingScanLimit,
		KMSReadKeyMaxCount: kmsReadKeyMaxCount, HealthInterval: healthInterval, HealthBatchSize: healthBatchSize,
		AWSSDKMaxAttempts: awsSDKMaxAttempts,
	}, nil
}

func (service *FoundationService) effectiveFoundationValues() (map[string]string, error) {
	if service == nil || service.settings == nil {
		return nil, fmt.Errorf("%w: settings service is unavailable", ErrInvalidState)
	}
	values := make(map[string]string, len(settings.BackupAssetCoreSettingKeys()))
	for _, key := range settings.BackupAssetCoreSettingKeys() {
		values[key] = service.settings.GetEffective(key)
	}
	if err := settings.ValidateBackupAssetFoundationConfig(values); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	return values, nil
}

func parseFoundationBool(values map[string]string, key string) (bool, error) {
	value, err := strconv.ParseBool(strings.TrimSpace(values[key]))
	if err != nil {
		return false, fmt.Errorf("%w: parse %s: %v", ErrInvalidState, key, err)
	}
	return value, nil
}

func parseFoundationDuration(values map[string]string, key string) (time.Duration, error) {
	value, err := time.ParseDuration(values[key])
	if err != nil {
		return 0, fmt.Errorf("%w: parse %s: %v", ErrInvalidState, key, err)
	}
	return value, nil
}

func parseFoundationInt64(values map[string]string, key string) (int64, error) {
	value, err := strconv.ParseInt(values[key], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: parse %s: %v", ErrInvalidState, key, err)
	}
	return value, nil
}

func parseFoundationInt(values map[string]string, key string) (int, error) {
	value, err := parseFoundationInt64(values, key)
	if err != nil {
		return 0, err
	}
	if value > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("%w: %s exceeds int range", ErrInvalidState, key)
	}
	return int(value), nil
}

type RepositoryDTO struct {
	ID                 string            `json:"id"`
	Provider           ProviderKind      `json:"provider_kind"`
	DisplayName        string            `json:"display_name"`
	Description        string            `json:"description"`
	VersionMode        VersionMode       `json:"version_mode"`
	Status             RepositoryStatus  `json:"status"`
	CapabilityRevision int               `json:"capability_revision"`
	Capabilities       CapabilitySet     `json:"capabilities"`
	Immutability       ImmutabilityLevel `json:"immutability_level"`
	LastSeenAt         *time.Time        `json:"last_seen_at,omitempty"`
	LastReconciledAt   *time.Time        `json:"last_reconciled_at,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type RecoveryPointLineageSummary struct {
	ProducingTaskID    *uint  `json:"producing_task_id,omitempty"`
	ProducingTaskRunID *uint  `json:"producing_task_run_id,omitempty"`
	SourcePointID      string `json:"source_recovery_point_id,omitempty"`
}

type RecoveryPointDTO struct {
	ID                 string                      `json:"id"`
	RepositoryID       string                      `json:"repository_id"`
	Lineage            RecoveryPointLineageSummary `json:"lineage"`
	Semantics          PointVersionSemantics       `json:"semantics"`
	State              RecoveryPointState          `json:"state"`
	Availability       PhysicalAvailability        `json:"physical_availability"`
	Hold               HoldState                   `json:"hold_state"`
	Immutability       ImmutabilityLevel           `json:"immutability_level"`
	ManifestDigest     string                      `json:"manifest_digest,omitempty"`
	EntryCount         int64                       `json:"entry_count"`
	LogicalBytes       int64                       `json:"logical_bytes"`
	CapturedAt         *time.Time                  `json:"captured_at,omitempty"`
	CommittedAt        *time.Time                  `json:"committed_at,omitempty"`
	ObservedAt         *time.Time                  `json:"observed_at,omitempty"`
	CapabilityRevision int                         `json:"capability_revision"`
	Capabilities       CapabilitySet               `json:"capabilities"`
	CreatedAt          time.Time                   `json:"created_at"`
	UpdatedAt          time.Time                   `json:"updated_at"`
}

func ToRepositoryDTO(record model.BackupRepository) (RepositoryDTO, error) {
	provider := ProviderKind(record.ProviderKind)
	versionMode := VersionMode(record.VersionMode)
	status := RepositoryStatus(record.Status)
	immutability := ImmutabilityLevel(record.ImmutabilityLevel)
	if err := ValidateOpaqueID(record.ID); err != nil || !validRepositoryProviderKinds[provider] ||
		!validVersionModes[versionMode] || !validRepositoryStatuses[status] || !validImmutabilityLevels[immutability] ||
		record.CapabilityRevision <= 0 {
		return RepositoryDTO{}, fmt.Errorf("%w: invalid repository model", ErrInvalidState)
	}
	capabilities, err := decodeCapabilities(record.CapabilitiesJSON)
	if err != nil {
		return RepositoryDTO{}, err
	}
	return RepositoryDTO{
		ID:                 record.ID,
		Provider:           provider,
		DisplayName:        record.DisplayName,
		Description:        record.Description,
		VersionMode:        versionMode,
		Status:             status,
		CapabilityRevision: record.CapabilityRevision,
		Capabilities:       capabilities,
		Immutability:       immutability,
		LastSeenAt:         utcTimePointer(record.LastSeenAt),
		LastReconciledAt:   utcTimePointer(record.LastReconciledAt),
		CreatedAt:          record.CreatedAt.UTC(),
		UpdatedAt:          record.UpdatedAt.UTC(),
	}, nil
}

func ToRecoveryPointDTO(record model.RecoveryPoint, repositoryVersion VersionMode) (RecoveryPointDTO, error) {
	if ValidateOpaqueID(record.ID) != nil || ValidateOpaqueID(record.RepositoryID) != nil ||
		record.EntryCount < 0 || record.LogicalBytes < 0 || record.CapabilityRevision <= 0 {
		return RecoveryPointDTO{}, fmt.Errorf("%w: invalid recovery point model", ErrInvalidState)
	}

	semantics := PointVersionSemantics(record.Semantics)
	state := RecoveryPointState(record.State)
	availability := PhysicalAvailability(record.PhysicalAvailability)
	hold := HoldState(record.HoldState)
	immutability := ImmutabilityLevel(record.ImmutabilityLevel)
	retirementReason := RetirementReason("")
	if record.RetirementReason != nil {
		retirementReason = RetirementReason(*record.RetirementReason)
	}
	profile := RecoveryPointProfile{
		VersionMode:                 repositoryVersion,
		Semantics:                   semantics,
		State:                       state,
		Immutability:                immutability,
		Availability:                availability,
		Hold:                        hold,
		ObservedAt:                  record.ObservedAt,
		RetirementReason:            retirementReason,
		RetiredAt:                   record.RetiredAt,
		HasEncryptedRollbackLocator: strings.TrimSpace(record.EncryptedRollbackLocator) != "",
	}
	if err := ValidateRecoveryPointProfile(profile); err != nil {
		return RecoveryPointDTO{}, err
	}

	var lineage RecoveryPointLineageSummary
	if strings.TrimSpace(record.LineageJSON) != "" {
		if err := json.Unmarshal([]byte(record.LineageJSON), &lineage); err != nil {
			return RecoveryPointDTO{}, fmt.Errorf("%w: invalid recovery point lineage", ErrInvalidState)
		}
	}
	if lineage.SourcePointID != "" && ValidateOpaqueID(lineage.SourcePointID) != nil {
		return RecoveryPointDTO{}, fmt.Errorf("%w: invalid lineage source", ErrInvalidState)
	}
	if lineage.ProducingTaskID == nil {
		lineage.ProducingTaskID = record.ProducingTaskID
	}
	if lineage.ProducingTaskRunID == nil {
		lineage.ProducingTaskRunID = record.ProducingTaskRunID
	}

	capabilities, err := decodeCapabilities(record.CapabilitiesJSON)
	if err != nil {
		return RecoveryPointDTO{}, err
	}
	return RecoveryPointDTO{
		ID:                 record.ID,
		RepositoryID:       record.RepositoryID,
		Lineage:            lineage,
		Semantics:          semantics,
		State:              state,
		Availability:       availability,
		Hold:               hold,
		Immutability:       immutability,
		ManifestDigest:     record.ManifestDigest,
		EntryCount:         record.EntryCount,
		LogicalBytes:       record.LogicalBytes,
		CapturedAt:         utcTimePointer(record.CapturedAt),
		CommittedAt:        utcTimePointer(record.CommittedAt),
		ObservedAt:         utcTimePointer(record.ObservedAt),
		CapabilityRevision: record.CapabilityRevision,
		Capabilities:       capabilities,
		CreatedAt:          record.CreatedAt.UTC(),
		UpdatedAt:          record.UpdatedAt.UTC(),
	}, nil
}

func decodeCapabilities(raw string) (CapabilitySet, error) {
	var capabilities CapabilitySet
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
		return CapabilitySet{}, fmt.Errorf("%w: invalid capability snapshot", ErrInvalidState)
	}
	if capabilities.Reason != nil {
		if err := ValidateCapabilityReason(*capabilities.Reason); err != nil {
			return CapabilitySet{}, err
		}
	}
	return capabilities, nil
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

var validRepositoryProviderKinds = setOf(ProviderRestic, ProviderRsync, ProviderRclone, ProviderCommand)
