package backupasset

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
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

func TestFoundationSearchConfigAndOverlayConfigUseOneSnapshot(t *testing.T) {
	values := cloneFoundationTestValues(staticFoundationDefaults)
	for key, value := range map[string]string{
		"backup_assets.enabled":                   "true",
		"backup_assets.search_reconcile_interval": "45s",
		"backup_assets.search_build_timeout":      "25m",
		"backup_assets.search_batch_size":         "750",
		"backup_assets.search_max_concurrency":    "3",
		"backup_assets.search_ast_max_depth":      "7",
		"backup_assets.search_ast_max_nodes":      "70",
		"backup_assets.search_values_per_node":    "24",
		"backup_assets.search_body_max_bytes":     "32768",
		"backup_assets.search_value_max_bytes":    "2048",
		"backup_assets.search_candidate_limit":    "15000",
		"backup_assets.search_query_timeout":      "4s",
		"backup_assets.search_page_size_max":      "250",
		"backup_assets.search_suggestion_limit":   "15",
		"backup_assets.saved_search_quota":        "120",
		"backup_assets.favorite_quota":            "6000",
		"backup_assets.tag_definition_quota":      "150",
		"backup_assets.tag_assignment_quota":      "12000",
		"backup_assets.overlay_bulk_max_items":    "300",
		"backup_assets.overlay_label_max_bytes":   "512",
		"backup_assets.recent_quota":              "12000",
		"backup_assets.recent_retention":          "1440h",
		"backup_assets.recent_writes_per_minute":  "240",
		"backup_assets.idempotency_ttl":           "48h",
		"backup_assets.idempotency_key_max_bytes": "192",
		"backup_assets.lease_duration":            "4m",
		"backup_assets.lease_heartbeat":           "30s",
		"backup_assets.lease_absolute_deadline":   "3h",
		"backup_assets.manifest_max_entries":      "9000000",
	} {
		values[key] = value
	}
	reader := &snapshotSettingsReader{values: values}
	searchConfig, overlayConfig, err := NewFoundationService(reader).SearchOverlayConfig()
	if err != nil {
		t.Fatalf("SearchOverlayConfig: %v", err)
	}
	if !searchConfig.Enabled || searchConfig.ReconcileInterval != 45*time.Second || searchConfig.BuildTimeout != 25*time.Minute ||
		searchConfig.BatchSize != 750 || searchConfig.MaxConcurrency != 3 || searchConfig.ASTMaxDepth != 7 ||
		searchConfig.ASTMaxNodes != 70 || searchConfig.ValuesPerNode != 24 || searchConfig.BodyMaxBytes != 32768 ||
		searchConfig.ValueMaxBytes != 2048 || searchConfig.CandidateLimit != 15000 || searchConfig.QueryTimeout != 4*time.Second ||
		searchConfig.PageSizeMax != 250 || searchConfig.SuggestionLimit != 15 || searchConfig.MaxDocuments != 9000000 ||
		searchConfig.Lease.Duration != 4*time.Minute || searchConfig.Lease.Heartbeat != 30*time.Second || searchConfig.Lease.AbsoluteDeadline != 3*time.Hour {
		t.Fatalf("SearchConfig=%+v", searchConfig)
	}
	if !overlayConfig.Enabled || overlayConfig.SavedSearchQuota != 120 || overlayConfig.FavoriteQuota != 6000 ||
		overlayConfig.TagDefinitionQuota != 150 || overlayConfig.TagAssignmentQuota != 12000 || overlayConfig.BulkMaxItems != 300 ||
		overlayConfig.LabelMaxBytes != 512 || overlayConfig.RecentQuota != 12000 || overlayConfig.RecentRetention != 1440*time.Hour ||
		overlayConfig.RecentWritesPerMinute != 240 || overlayConfig.IdempotencyTTL != 48*time.Hour || overlayConfig.IdempotencyKeyMaxBytes != 192 {
		t.Fatalf("OverlayConfig=%+v", overlayConfig)
	}
	if reader.effectiveReads != 0 || reader.snapshotReads != 1 {
		t.Fatalf("combined config mixed per-key reads: effective=%d snapshot=%d", reader.effectiveReads, reader.snapshotReads)
	}

	reader.set("backup_assets.search_page_size_max", "275")
	nextSearch, _, err := NewFoundationService(reader).SearchOverlayConfig()
	if err != nil || nextSearch.PageSizeMax != 275 {
		t.Fatalf("dynamic snapshot was not re-read: config=%+v err=%v", nextSearch, err)
	}
}

func TestFoundationSearchConfigAndOverlayConfigRequireCompleteSnapshotPort(t *testing.T) {
	service := NewFoundationService(staticSettingsReader{})
	if _, _, err := service.SearchOverlayConfig(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("reader without snapshot port got %v, want ErrInvalidState", err)
	}

	reader := &snapshotSettingsReader{values: cloneFoundationTestValues(staticFoundationDefaults)}
	delete(reader.values, "backup_assets.search_candidate_limit")
	if _, _, err := NewFoundationService(reader).SearchOverlayConfig(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("incomplete snapshot got %v, want ErrInvalidState", err)
	}
}

func TestFoundationContentConfigUsesOneAtomicSnapshot(t *testing.T) {
	values := cloneFoundationTestValues(staticFoundationDefaults)
	for key, value := range map[string]string{
		"backup_assets.enabled":                           "true",
		"backup_assets.content_preview_ttl":               "3m",
		"backup_assets.content_media_ttl":                 "20m",
		"backup_assets.content_idle_ttl":                  "45s",
		"backup_assets.content_write_idle_timeout":        "25s",
		"backup_assets.content_ticket_timeout":            "15s",
		"backup_assets.content_request_max_bytes":         "33554432",
		"backup_assets.content_cumulative_max_bytes":      "268435456",
		"backup_assets.content_max_requests":              "128",
		"backup_assets.content_grant_max_in_flight":       "2",
		"backup_assets.content_user_max_concurrency":      "3",
		"backup_assets.content_provider_max_concurrency":  "3",
		"backup_assets.content_global_max_concurrency":    "12",
		"backup_assets.content_rate_window":               "2m",
		"backup_assets.content_user_window_bytes":         "536870912",
		"backup_assets.content_provider_window_bytes":     "2147483648",
		"backup_assets.content_global_window_bytes":       "4294967296",
		"backup_assets.content_user_window_requests":      "512",
		"backup_assets.content_provider_window_requests":  "2048",
		"backup_assets.content_global_window_requests":    "4096",
		"backup_assets.content_classification_scan_bytes": "131072",
		"backup_assets.content_text_preview_bytes":        "524288",
		"backup_assets.content_hex_preview_bytes":         "32768",
		"backup_assets.content_raster_max_pixels":         "50000000",
		"backup_assets.content_memory_object_bytes":       "2097152",
		"backup_assets.content_memory_user_bytes":         "8388608",
		"backup_assets.content_memory_provider_bytes":     "16777216",
		"backup_assets.content_memory_global_bytes":       "33554432",
		"backup_assets.content_cache_enabled":             "false",
		"backup_assets.content_cache_root":                "/var/cache/xirang/content-test",
		"backup_assets.content_cache_chunk_bytes":         "524288",
		"backup_assets.content_cache_object_bytes":        "268435456",
		"backup_assets.content_cache_user_bytes":          "1073741824",
		"backup_assets.content_cache_provider_bytes":      "2147483648",
		"backup_assets.content_cache_global_bytes":        "4294967296",
		"backup_assets.content_cache_object_files":        "1024",
		"backup_assets.content_cache_user_files":          "2048",
		"backup_assets.content_cache_provider_files":      "4096",
		"backup_assets.content_cache_global_files":        "8192",
		"backup_assets.content_cache_idle_ttl":            "10m",
		"backup_assets.content_cache_absolute_ttl":        "90m",
		"backup_assets.content_reconcile_interval":        "45s",
		"backup_assets.content_reconcile_batch_size":      "80",
		"backup_assets.content_audit_backlog_max":         "5000",
		"backup_assets.content_allow_insecure_loopback":   "true",
		"backup_assets.provider_max_concurrency":          "4",
	} {
		values[key] = value
	}
	reader := &snapshotSettingsReader{values: values}
	config, err := NewFoundationService(reader).ContentConfig()
	if err != nil {
		t.Fatalf("ContentConfig: %v", err)
	}
	if !config.Enabled || config.PreviewTTL != 3*time.Minute || config.MediaTTL != 20*time.Minute ||
		config.IdleTTL != 45*time.Second || config.WriteIdleTimeout != 25*time.Second || config.TicketTimeout != 15*time.Second ||
		config.RequestMaxBytes != 33554432 || config.CumulativeMaxBytes != 268435456 || config.MaxRequests != 128 ||
		config.GrantMaxInFlight != 2 || config.RateWindow != 2*time.Minute {
		t.Fatalf("ContentConfig core=%+v", config)
	}
	if config.User.MaxConcurrency != 3 || config.User.WindowBytes != 536870912 || config.User.WindowRequests != 512 ||
		config.Provider.MaxConcurrency != 3 || config.Provider.WindowBytes != 2147483648 || config.Provider.WindowRequests != 2048 ||
		config.Global.MaxConcurrency != 12 || config.Global.WindowBytes != 4294967296 || config.Global.WindowRequests != 4096 {
		t.Fatalf("ContentConfig scopes=%+v %+v %+v", config.User, config.Provider, config.Global)
	}
	if config.ClassificationScanBytes != 131072 || config.TextPreviewBytes != 524288 || config.HexPreviewBytes != 32768 ||
		config.RasterMaxPixels != 50000000 || config.Memory.ObjectBytes != 2097152 || config.Memory.UserBytes != 8388608 ||
		config.Memory.ProviderBytes != 16777216 || config.Memory.GlobalBytes != 33554432 {
		t.Fatalf("ContentConfig renderer/memory=%+v", config)
	}
	if config.Cache.Enabled || config.Cache.Root != "/var/cache/xirang/content-test" || config.Cache.ChunkBytes != 524288 ||
		config.Cache.ObjectBytes != 268435456 || config.Cache.UserBytes != 1073741824 ||
		config.Cache.ProviderBytes != 2147483648 || config.Cache.GlobalBytes != 4294967296 ||
		config.Cache.ObjectFiles != 1024 || config.Cache.UserFiles != 2048 || config.Cache.ProviderFiles != 4096 ||
		config.Cache.GlobalFiles != 8192 || config.Cache.IdleTTL != 10*time.Minute || config.Cache.AbsoluteTTL != 90*time.Minute {
		t.Fatalf("ContentConfig cache=%+v", config.Cache)
	}
	if config.ReconcileInterval != 45*time.Second || config.ReconcileBatchSize != 80 || config.AuditBacklogMax != 5000 ||
		!config.AllowInsecureLoopback {
		t.Fatalf("ContentConfig lifecycle=%+v", config)
	}
	if reader.effectiveReads != 0 || reader.snapshotReads != 1 {
		t.Fatalf("ContentConfig mixed per-key reads: effective=%d snapshot=%d", reader.effectiveReads, reader.snapshotReads)
	}
}

func TestFoundationContentConfigRequiresCompleteAtomicSnapshot(t *testing.T) {
	if _, err := NewFoundationService(staticSettingsReader{}).ContentConfig(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("reader without snapshot port got %v, want ErrInvalidState", err)
	}
	reader := &snapshotSettingsReader{values: cloneFoundationTestValues(staticFoundationDefaults)}
	delete(reader.values, "backup_assets.content_ticket_timeout")
	if _, err := NewFoundationService(reader).ContentConfig(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("incomplete content snapshot got %v, want ErrInvalidState", err)
	}
}

type snapshotSettingsReader struct {
	mu             sync.Mutex
	values         map[string]string
	effectiveReads int
	snapshotReads  int
}

func (reader *snapshotSettingsReader) GetEffective(key string) string {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.effectiveReads++
	return reader.values[key]
}

func (reader *snapshotSettingsReader) BackupAssetSettingsSnapshot() (map[string]string, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.snapshotReads++
	return cloneFoundationTestValues(reader.values), nil
}

func (reader *snapshotSettingsReader) set(key, value string) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.values[key] = value
}

func cloneFoundationTestValues(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

type staticSettingsReader map[string]string

func (reader staticSettingsReader) GetEffective(key string) string {
	if value, ok := reader[key]; ok {
		return value
	}
	return staticFoundationDefaults[key]
}

var staticFoundationDefaults = map[string]string{
	"backup_assets.enabled":                           "false",
	"backup_assets.content_preview_ttl":               "2m",
	"backup_assets.content_media_ttl":                 "15m",
	"backup_assets.content_idle_ttl":                  "60s",
	"backup_assets.content_write_idle_timeout":        "30s",
	"backup_assets.content_ticket_timeout":            "20s",
	"backup_assets.content_request_max_bytes":         "67108864",
	"backup_assets.content_cumulative_max_bytes":      "536870912",
	"backup_assets.content_max_requests":              "256",
	"backup_assets.content_grant_max_in_flight":       "2",
	"backup_assets.content_user_max_concurrency":      "4",
	"backup_assets.content_provider_max_concurrency":  "4",
	"backup_assets.content_global_max_concurrency":    "16",
	"backup_assets.content_rate_window":               "1m",
	"backup_assets.content_user_window_bytes":         "1073741824",
	"backup_assets.content_provider_window_bytes":     "4294967296",
	"backup_assets.content_global_window_bytes":       "8589934592",
	"backup_assets.content_user_window_requests":      "1024",
	"backup_assets.content_provider_window_requests":  "4096",
	"backup_assets.content_global_window_requests":    "8192",
	"backup_assets.content_classification_scan_bytes": "262144",
	"backup_assets.content_text_preview_bytes":        "1048576",
	"backup_assets.content_hex_preview_bytes":         "65536",
	"backup_assets.content_raster_max_pixels":         "100000000",
	"backup_assets.content_memory_global_bytes":       "67108864",
	"backup_assets.content_memory_object_bytes":       "4194304",
	"backup_assets.content_memory_user_bytes":         "16777216",
	"backup_assets.content_memory_provider_bytes":     "33554432",
	"backup_assets.content_cache_enabled":             "true",
	"backup_assets.content_cache_root":                "/var/cache/xirang/asset-content",
	"backup_assets.content_cache_chunk_bytes":         "1048576",
	"backup_assets.content_cache_object_bytes":        "536870912",
	"backup_assets.content_cache_user_bytes":          "2147483648",
	"backup_assets.content_cache_provider_bytes":      "4294967296",
	"backup_assets.content_cache_global_bytes":        "8589934592",
	"backup_assets.content_cache_object_files":        "1024",
	"backup_assets.content_cache_user_files":          "4096",
	"backup_assets.content_cache_provider_files":      "8192",
	"backup_assets.content_cache_global_files":        "16384",
	"backup_assets.content_cache_idle_ttl":            "15m",
	"backup_assets.content_cache_absolute_ttl":        "2h",
	"backup_assets.content_reconcile_interval":        "1m",
	"backup_assets.content_reconcile_batch_size":      "100",
	"backup_assets.content_audit_backlog_max":         "10000",
	"backup_assets.content_allow_insecure_loopback":   "false",
	"backup_assets.catalog_batch_size":                "2000",
	"backup_assets.catalog_build_timeout":             "30m",
	"backup_assets.repository_reconcile_interval":     "15m",
	"backup_assets.audit_segment_max_events":          "10000",
	"backup_assets.audit_segment_max_age":             "24h",
	"backup_assets.audit_detail_retention_days":       "180",
	"backup_assets.audit_checkpoint_retention_days":   "2555",
	"backup_assets.lease_duration":                    "5m",
	"backup_assets.lease_heartbeat":                   "60s",
	"backup_assets.lease_absolute_deadline":           "168h",
	"backup_assets.provider_operation_timeout":        "2m",
	"backup_assets.provider_max_concurrency":          "4",
	"backup_assets.provider_metadata_limit_bytes":     "16777216",
	"backup_assets.publication_reconcile_interval":    "5m",
	"backup_assets.publication_reconcile_batch_size":  "100",
	"backup_assets.publication_worker_concurrency":    "2",
	"backup_assets.publication_missing_grace":         "30m",
	"backup_assets.publication_stream_max_bytes":      "268435456",
	"backup_assets.manifest_timeout":                  "2h",
	"backup_assets.manifest_max_bytes":                "4294967296",
	"backup_assets.manifest_max_entries":              "10000000",
	"backup_assets.manifest_max_record_bytes":         "1048576",
	"backup_assets.manifest_max_depth":                "4096",
	"backup_assets.rclone_preflight_ttl":              "30m",
	"backup_assets.rclone_portable_deadline":          "24h",
	"backup_assets.rclone_native_deadline":            "45m",
	"backup_assets.rclone_bound_config_max_bytes":     "65536",
	"backup_assets.rclone_control_payload_max_bytes":  "8388608",
	"backup_assets.rclone_full_verify_max_bytes":      "1099511627776",
	"backup_assets.rclone_manifest_chunk_max_bytes":   "8388608",
	"backup_assets.rclone_low_level_retries":          "3",
	"backup_assets.rclone_staging_orphan_age":         "24h",
	"backup_assets.rclone_staging_scan_limit":         "256",
	"backup_assets.rclone_kms_read_key_max_count":     "8",
	"backup_assets.rclone_health_interval":            "15m",
	"backup_assets.rclone_health_batch_size":          "100",
	"backup_assets.rclone_aws_sdk_max_attempts":       "3",
	"backup_assets.search_reconcile_interval":         "1m",
	"backup_assets.search_build_timeout":              "30m",
	"backup_assets.search_batch_size":                 "500",
	"backup_assets.search_max_concurrency":            "2",
	"backup_assets.search_ast_max_depth":              "8",
	"backup_assets.search_ast_max_nodes":              "64",
	"backup_assets.search_values_per_node":            "32",
	"backup_assets.search_body_max_bytes":             "65536",
	"backup_assets.search_value_max_bytes":            "1024",
	"backup_assets.search_candidate_limit":            "10000",
	"backup_assets.search_query_timeout":              "5s",
	"backup_assets.search_page_size_max":              "200",
	"backup_assets.search_suggestion_limit":           "20",
	"backup_assets.saved_search_quota":                "100",
	"backup_assets.favorite_quota":                    "5000",
	"backup_assets.tag_definition_quota":              "100",
	"backup_assets.tag_assignment_quota":              "10000",
	"backup_assets.overlay_bulk_max_items":            "200",
	"backup_assets.overlay_label_max_bytes":           "256",
	"backup_assets.recent_quota":                      "10000",
	"backup_assets.recent_retention":                  "720h",
	"backup_assets.recent_writes_per_minute":          "120",
	"backup_assets.idempotency_ttl":                   "24h",
	"backup_assets.idempotency_key_max_bytes":         "128",
}
