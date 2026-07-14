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

type FoundationService struct {
	settings SettingsReader
}

type ProviderConfig struct {
	OperationTimeout   time.Duration
	MaxConcurrency     int
	MetadataLimitBytes int64
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
	}, nil
}

func (service *FoundationService) effectiveFoundationValues() (map[string]string, error) {
	if service == nil || service.settings == nil {
		return nil, fmt.Errorf("%w: settings service is unavailable", ErrInvalidState)
	}
	values := make(map[string]string, len(settings.BackupAssetFoundationSettingKeys()))
	for _, key := range settings.BackupAssetFoundationSettingKeys() {
		values[key] = service.settings.GetEffective(key)
	}
	if err := settings.ValidateBackupAssetFoundationConfig(values); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	return values, nil
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
