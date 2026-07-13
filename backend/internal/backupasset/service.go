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
	if service == nil || service.settings == nil {
		return false
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(service.settings.GetEffective("backup_assets.enabled")))
	return err == nil && enabled
}

func (service *FoundationService) LeaseConfig() (LeaseConfig, error) {
	if service == nil || service.settings == nil {
		return LeaseConfig{}, fmt.Errorf("%w: settings service is unavailable", ErrInvalidState)
	}
	values := map[string]string{
		"backup_assets.lease_duration":          service.settings.GetEffective("backup_assets.lease_duration"),
		"backup_assets.lease_heartbeat":         service.settings.GetEffective("backup_assets.lease_heartbeat"),
		"backup_assets.lease_absolute_deadline": service.settings.GetEffective("backup_assets.lease_absolute_deadline"),
	}
	if err := settings.ValidateBackupAssetFoundationConfig(values); err != nil {
		return LeaseConfig{}, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	duration, _ := time.ParseDuration(values["backup_assets.lease_duration"])
	heartbeat, _ := time.ParseDuration(values["backup_assets.lease_heartbeat"])
	absoluteDeadline, _ := time.ParseDuration(values["backup_assets.lease_absolute_deadline"])
	return LeaseConfig{Duration: duration, Heartbeat: heartbeat, AbsoluteDeadline: absoluteDeadline}, nil
}

func (service *FoundationService) ProviderConfig() (ProviderConfig, error) {
	if service == nil || service.settings == nil {
		return ProviderConfig{}, fmt.Errorf("%w: settings service is unavailable", ErrInvalidState)
	}
	values := map[string]string{
		"backup_assets.provider_operation_timeout":    service.settings.GetEffective("backup_assets.provider_operation_timeout"),
		"backup_assets.provider_max_concurrency":      service.settings.GetEffective("backup_assets.provider_max_concurrency"),
		"backup_assets.provider_metadata_limit_bytes": service.settings.GetEffective("backup_assets.provider_metadata_limit_bytes"),
	}
	if err := settings.ValidateBackupAssetFoundationConfig(values); err != nil {
		return ProviderConfig{}, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	operationTimeout, _ := time.ParseDuration(values["backup_assets.provider_operation_timeout"])
	maxConcurrency, _ := strconv.Atoi(values["backup_assets.provider_max_concurrency"])
	metadataLimitBytes, _ := strconv.ParseInt(values["backup_assets.provider_metadata_limit_bytes"], 10, 64)
	return ProviderConfig{
		OperationTimeout:   operationTimeout,
		MaxConcurrency:     maxConcurrency,
		MetadataLimitBytes: metadataLimitBytes,
	}, nil
}

func (service *FoundationService) AuditConfig() (AuditConfig, error) {
	if service == nil || service.settings == nil {
		return AuditConfig{}, fmt.Errorf("%w: settings service is unavailable", ErrInvalidState)
	}
	values := map[string]string{
		"backup_assets.audit_segment_max_events": service.settings.GetEffective("backup_assets.audit_segment_max_events"),
		"backup_assets.audit_segment_max_age":    service.settings.GetEffective("backup_assets.audit_segment_max_age"),
	}
	if err := settings.ValidateBackupAssetFoundationConfig(values); err != nil {
		return AuditConfig{}, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	segmentMaxEvents, _ := strconv.ParseInt(values["backup_assets.audit_segment_max_events"], 10, 64)
	segmentMaxAge, _ := time.ParseDuration(values["backup_assets.audit_segment_max_age"])
	return AuditConfig{SegmentMaxEvents: segmentMaxEvents, SegmentMaxAge: segmentMaxAge}, nil
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
