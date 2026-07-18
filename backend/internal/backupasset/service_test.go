package backupasset

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSanitizedDTOExcludesSecretsAndLocators(t *testing.T) {
	now := time.Date(2026, 7, 13, 2, 3, 4, 0, time.UTC)
	repositoryIdentity := "FAKE_REPOSITORY_IDENTITY_FOR_TEST_ONLY"
	retirementReason := string(RetirementCutover)
	repository := model.BackupRepository{
		ID:                 strings.Repeat("a", 32),
		ProviderKind:       string(ProviderRsync),
		RepositoryIdentity: &repositoryIdentity,
		DisplayName:        "backup-a",
		Description:        "safe description",
		VersionMode:        string(VersionHardlinkTree),
		Status:             string(RepositoryOnline),
		CapabilityRevision: 1,
		CapabilitiesJSON:   `{"list":true,"download":true}`,
		ImmutabilityLevel:  string(ImmutabilityXirangManaged),
		LastSeenAt:         &now,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	recoveryPoint := model.RecoveryPoint{
		ID:                       strings.Repeat("b", 32),
		RepositoryID:             repository.ID,
		LineageJSON:              `{"source_recovery_point_id":"` + strings.Repeat("d", 32) + `"}`,
		Semantics:                string(PointMutableHead),
		State:                    string(RecoveryPointRetired),
		PhysicalAvailability:     string(PhysicalOnline),
		HoldState:                string(HoldNone),
		ImmutabilityLevel:        string(ImmutabilityMutable),
		ObservedAt:               &now,
		RetirementReason:         &retirementReason,
		RetiredAt:                &now,
		EntryCount:               3,
		LogicalBytes:             42,
		CapabilityRevision:       1,
		CapabilitiesJSON:         `{"list":true,"download":true}`,
		EncryptedProviderLocator: "FAKE_ENCRYPTED_PROVIDER_LOCATOR_FOR_TEST_ONLY",
		EncryptedRollbackLocator: "FAKE_ENCRYPTED_ROLLBACK_LOCATOR_FOR_TEST_ONLY",
		CreatedAt:                now,
		UpdatedAt:                now,
	}

	repositoryDTO, err := ToRepositoryDTO(repository)
	if err != nil {
		t.Fatalf("convert repository DTO: %v", err)
	}
	recoveryPointDTO, err := ToRecoveryPointDTO(recoveryPoint, VersionMutableHead)
	if err != nil {
		t.Fatalf("convert recovery point DTO: %v", err)
	}
	if repositoryDTO.ID != repository.ID || recoveryPointDTO.ID != recoveryPoint.ID {
		t.Fatalf("safe identity missing from DTOs: %+v %+v", repositoryDTO, recoveryPointDTO)
	}

	payload, err := json.Marshal(struct {
		Repository    RepositoryDTO    `json:"repository"`
		RecoveryPoint RecoveryPointDTO `json:"recovery_point"`
	}{repositoryDTO, recoveryPointDTO})
	if err != nil {
		t.Fatalf("marshal DTOs: %v", err)
	}
	text := string(payload)
	for _, forbidden := range []string{
		repositoryIdentity,
		recoveryPoint.EncryptedProviderLocator,
		recoveryPoint.EncryptedRollbackLocator,
		"repository_identity",
		"provider_locator",
		"rollback_locator",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized DTO JSON contains forbidden value/key %q: %s", forbidden, text)
		}
	}
}

func TestToRecoveryPointDTOUsesOwningRepositoryVersionMode(t *testing.T) {
	now := time.Date(2026, 7, 13, 3, 4, 5, 0, time.UTC)
	record := model.RecoveryPoint{
		ID:                   strings.Repeat("a", 32),
		RepositoryID:         strings.Repeat("b", 32),
		Semantics:            string(PointXirangManifest),
		State:                string(RecoveryPointCommitted),
		PhysicalAvailability: string(PhysicalOnline),
		HoldState:            string(HoldNone),
		ImmutabilityLevel:    string(ImmutabilityXirangManaged),
		CapabilityRevision:   1,
		CapabilitiesJSON:     `{}`,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if _, err := ToRecoveryPointDTO(record, VersionVersionedPrefix); err != nil {
		t.Fatalf("versioned-prefix recovery point rejected: %v", err)
	}
	if _, err := ToRecoveryPointDTO(record, VersionNativeSnapshot); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("mismatched repository version got %v", err)
	}
}

func TestBackupAssetModelHooksEncryptLocators(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	type hookCase struct {
		name       string
		beforeSave func() error
		afterFind  func() error
		values     func() []string
	}

	binding := model.RepositoryAccessBinding{EncryptedConfig: "FAKE_ACCESS_CONFIG_FOR_TEST_ONLY"}
	link := model.TaskRepositoryLink{EncryptedLegacyLocator: "FAKE_LEGACY_LOCATOR_FOR_TEST_ONLY"}
	point := model.RecoveryPoint{
		EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY",
		EncryptedRollbackLocator: "FAKE_ROLLBACK_LOCATOR_FOR_TEST_ONLY",
	}
	manifest := model.RecoveryPointManifest{EncryptedCommitEvidence: "FAKE_COMMIT_EVIDENCE_FOR_TEST_ONLY"}
	entry := model.CatalogEntry{EncryptedProviderLocator: "FAKE_ENTRY_LOCATOR_FOR_TEST_ONLY"}
	values := []hookCase{
		{"access config", func() error { return binding.BeforeSave(nil) }, func() error { return binding.AfterFind(nil) }, func() []string { return []string{binding.EncryptedConfig} }},
		{"legacy locator", func() error { return link.BeforeSave(nil) }, func() error { return link.AfterFind(nil) }, func() []string { return []string{link.EncryptedLegacyLocator} }},
		{"recovery point locators", func() error { return point.BeforeSave(nil) }, func() error { return point.AfterFind(nil) }, func() []string { return []string{point.EncryptedProviderLocator, point.EncryptedRollbackLocator} }},
		{"commit evidence", func() error { return manifest.BeforeSave(nil) }, func() error { return manifest.AfterFind(nil) }, func() []string { return []string{manifest.EncryptedCommitEvidence} }},
		{"entry locator", func() error { return entry.BeforeSave(nil) }, func() error { return entry.AfterFind(nil) }, func() []string { return []string{entry.EncryptedProviderLocator} }},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			plain := append([]string(nil), tt.values()...)
			if err := tt.beforeSave(); err != nil {
				t.Fatalf("BeforeSave: %v", err)
			}
			for index, encrypted := range tt.values() {
				if !strings.HasPrefix(encrypted, "enc:v2:") || strings.Contains(encrypted, plain[index]) {
					t.Fatalf("value was not encrypted at rest: %q", encrypted)
				}
			}
			if err := tt.afterFind(); err != nil {
				t.Fatalf("AfterFind: %v", err)
			}
			for index, decrypted := range tt.values() {
				if decrypted != plain[index] {
					t.Fatalf("decrypt mismatch: got %q want %q", decrypted, plain[index])
				}
			}
		})
	}
}

func TestBackupAssetSettingsFoundationServiceIsDisabledByDefault(t *testing.T) {
	reader := staticSettingsReader{
		"backup_assets.enabled":                 "false",
		"backup_assets.lease_duration":          "5m",
		"backup_assets.lease_heartbeat":         "60s",
		"backup_assets.lease_absolute_deadline": "168h",
	}
	service := NewFoundationService(reader)
	if service.Enabled() {
		t.Fatal("foundation service unexpectedly enabled")
	}
	leaseConfig, err := service.LeaseConfig()
	if err != nil {
		t.Fatalf("LeaseConfig: %v", err)
	}
	if leaseConfig.Duration != 5*time.Minute || leaseConfig.Heartbeat != time.Minute || leaseConfig.AbsoluteDeadline != 168*time.Hour {
		t.Fatalf("lease settings mapping mismatch: %+v", leaseConfig)
	}
	reader["backup_assets.enabled"] = "true"
	if !service.Enabled() {
		t.Fatal("explicit true feature setting was not honored")
	}
	reader["backup_assets.enabled"] = "invalid"
	if service.Enabled() {
		t.Fatal("malformed feature setting must fail closed")
	}
}

func TestFoundationServiceProviderConfig(t *testing.T) {
	service := NewFoundationService(staticSettingsReader{
		"backup_assets.provider_operation_timeout":    "3m",
		"backup_assets.provider_max_concurrency":      "6",
		"backup_assets.provider_metadata_limit_bytes": "8388608",
	})
	config, err := service.ProviderConfig()
	if err != nil || config.OperationTimeout != 3*time.Minute || config.MaxConcurrency != 6 || config.MetadataLimitBytes != 8<<20 {
		t.Fatalf("ProviderConfig=%+v err=%v", config, err)
	}
}

func TestFoundationServiceAuditConfigUsesEffectiveSettings(t *testing.T) {
	service := NewFoundationService(staticSettingsReader{
		"backup_assets.audit_segment_max_events": "4321",
		"backup_assets.audit_segment_max_age":    "36h",
	})
	config, err := service.AuditConfig()
	if err != nil || config.SegmentMaxEvents != 4321 || config.SegmentMaxAge != 36*time.Hour {
		t.Fatalf("AuditConfig=%+v err=%v", config, err)
	}
}

func TestFoundationConfigGettersUseFullEffectiveLeaseAndPublicationValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:foundation-publication-config?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	reader := settings.NewService(db)
	if err := reader.Update("backup_assets.lease_duration", "71s"); err != nil {
		t.Fatalf("seed lease duration DB value: %v", err)
	}
	if err := reader.Update("backup_assets.lease_heartbeat", "60s"); err != nil {
		t.Fatalf("seed lease heartbeat DB value: %v", err)
	}
	if err := reader.Update("backup_assets.lease_absolute_deadline", "2h"); err != nil {
		t.Fatalf("seed absolute deadline DB value: %v", err)
	}
	t.Setenv("BACKUP_ASSETS_PUBLICATION_MISSING_GRACE", "71s")
	t.Setenv("BACKUP_ASSETS_PUBLICATION_WORKER_CONCURRENCY", "3")
	t.Setenv("BACKUP_ASSETS_MANIFEST_TIMEOUT", "1h")
	service := NewFoundationService(reader)

	leaseConfig, err := service.LeaseConfig()
	if err != nil || leaseConfig.Duration != 71*time.Second || leaseConfig.Heartbeat != 60*time.Second || leaseConfig.AbsoluteDeadline != 2*time.Hour {
		t.Fatalf("LeaseConfig=%+v err=%v", leaseConfig, err)
	}
	providerConfig, err := service.ProviderConfig()
	if err != nil || providerConfig.OperationTimeout != 2*time.Minute || providerConfig.MaxConcurrency != 4 || providerConfig.MetadataLimitBytes != 16777216 {
		t.Fatalf("ProviderConfig=%+v err=%v", providerConfig, err)
	}
	auditConfig, err := service.AuditConfig()
	if err != nil || auditConfig.SegmentMaxEvents != 10000 || auditConfig.SegmentMaxAge != 24*time.Hour {
		t.Fatalf("AuditConfig=%+v err=%v", auditConfig, err)
	}
	publicationConfig, err := service.PublicationConfig()
	if err != nil {
		t.Fatalf("PublicationConfig: %v", err)
	}
	if publicationConfig.ReconcileInterval != 5*time.Minute || publicationConfig.WorkerConcurrency != 3 || publicationConfig.MissingGrace != 71*time.Second ||
		publicationConfig.BackupStreamMaxBytes != 268435456 || publicationConfig.ManifestTimeout != time.Hour || publicationConfig.ManifestMaxBytes != 4294967296 ||
		publicationConfig.ManifestMaxEntries != 10000000 || publicationConfig.ManifestMaxRecordBytes != 1048576 || publicationConfig.ManifestMaxDepth != 4096 ||
		publicationConfig.Rclone.PreflightTTL != 30*time.Minute || publicationConfig.Rclone.PortableDeadline != 24*time.Hour ||
		publicationConfig.Rclone.NativeDeadline != 45*time.Minute || publicationConfig.Rclone.BoundConfigMaxBytes != 65536 ||
		publicationConfig.Rclone.ControlPayloadMaxBytes != 8388608 || publicationConfig.Rclone.FullVerifyMaxBytes != 1099511627776 ||
		publicationConfig.Rclone.ManifestChunkMaxBytes != 8388608 || publicationConfig.Rclone.LowLevelRetries != 3 ||
		publicationConfig.Rclone.StagingOrphanAge != 24*time.Hour || publicationConfig.Rclone.StagingScanLimit != 256 ||
		publicationConfig.Rclone.KMSReadKeyMaxCount != 8 || publicationConfig.Rclone.HealthInterval != 15*time.Minute ||
		publicationConfig.Rclone.HealthBatchSize != 100 || publicationConfig.Rclone.AWSSDKMaxAttempts != 3 {
		t.Fatalf("PublicationConfig=%+v", publicationConfig)
	}
}

func TestFoundationCatalogConfigUsesRegisteredSettingsAndBounds(t *testing.T) {
	reader := staticSettingsReader{
		"backup_assets.enabled":                       "true",
		"backup_assets.catalog_batch_size":            "4096",
		"backup_assets.catalog_build_timeout":         "45m",
		"backup_assets.repository_reconcile_interval": "11m",
		"backup_assets.provider_max_concurrency":      "7",
		"backup_assets.manifest_max_entries":          "9000000",
		"backup_assets.lease_duration":                "4m",
		"backup_assets.lease_heartbeat":               "30s",
		"backup_assets.lease_absolute_deadline":       "3h",
	}
	config, err := NewFoundationService(reader).CatalogConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || config.BatchSize != 4096 || config.BuildTimeout != 45*time.Minute ||
		config.ReconcileInterval != 11*time.Minute || config.MaxConcurrency != 7 || config.MaxEntries != 9000000 ||
		config.Lease.Duration != 4*time.Minute || config.Lease.Heartbeat != 30*time.Second || config.Lease.AbsoluteDeadline != 3*time.Hour {
		t.Fatalf("CatalogConfig=%+v", config)
	}

	reader["backup_assets.catalog_batch_size"] = "0"
	if _, err := NewFoundationService(reader).CatalogConfig(); err == nil {
		t.Fatal("invalid Catalog batch size was accepted")
	}
	reader["backup_assets.catalog_batch_size"] = "4096"
	reader["backup_assets.lease_heartbeat"] = "5m"
	if _, err := NewFoundationService(reader).CatalogConfig(); err == nil {
		t.Fatal("Catalog heartbeat outside lease duration was accepted")
	}
}

type staticSettingsReader map[string]string

func (reader staticSettingsReader) GetEffective(key string) string {
	if value, ok := reader[key]; ok {
		return value
	}
	return staticFoundationDefaults[key]
}

var staticFoundationDefaults = map[string]string{
	"backup_assets.enabled":                          "false",
	"backup_assets.catalog_batch_size":               "2000",
	"backup_assets.catalog_build_timeout":            "30m",
	"backup_assets.repository_reconcile_interval":    "15m",
	"backup_assets.audit_segment_max_events":         "10000",
	"backup_assets.audit_segment_max_age":            "24h",
	"backup_assets.audit_detail_retention_days":      "180",
	"backup_assets.audit_checkpoint_retention_days":  "2555",
	"backup_assets.lease_duration":                   "5m",
	"backup_assets.lease_heartbeat":                  "60s",
	"backup_assets.lease_absolute_deadline":          "168h",
	"backup_assets.provider_operation_timeout":       "2m",
	"backup_assets.provider_max_concurrency":         "4",
	"backup_assets.provider_metadata_limit_bytes":    "16777216",
	"backup_assets.publication_reconcile_interval":   "5m",
	"backup_assets.publication_reconcile_batch_size": "100",
	"backup_assets.publication_worker_concurrency":   "2",
	"backup_assets.publication_missing_grace":        "30m",
	"backup_assets.publication_stream_max_bytes":     "268435456",
	"backup_assets.manifest_timeout":                 "2h",
	"backup_assets.manifest_max_bytes":               "4294967296",
	"backup_assets.manifest_max_entries":             "10000000",
	"backup_assets.manifest_max_record_bytes":        "1048576",
	"backup_assets.manifest_max_depth":               "4096",
	"backup_assets.rclone_preflight_ttl":             "30m",
	"backup_assets.rclone_portable_deadline":         "24h",
	"backup_assets.rclone_native_deadline":           "45m",
	"backup_assets.rclone_bound_config_max_bytes":    "65536",
	"backup_assets.rclone_control_payload_max_bytes": "8388608",
	"backup_assets.rclone_full_verify_max_bytes":     "1099511627776",
	"backup_assets.rclone_manifest_chunk_max_bytes":  "8388608",
	"backup_assets.rclone_low_level_retries":         "3",
	"backup_assets.rclone_staging_orphan_age":        "24h",
	"backup_assets.rclone_staging_scan_limit":        "256",
	"backup_assets.rclone_kms_read_key_max_count":    "8",
	"backup_assets.rclone_health_interval":           "15m",
	"backup_assets.rclone_health_batch_size":         "100",
	"backup_assets.rclone_aws_sdk_max_attempts":      "3",
}
