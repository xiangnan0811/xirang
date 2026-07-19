package repository

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type repositorySettings map[string]string

var repositorySeedSequence atomic.Uint64
var repositoryDBSequence atomic.Uint64

func (settings repositorySettings) GetEffective(key string) string {
	if value, ok := settings[key]; ok {
		return value
	}
	return repositoryFoundationDefaults[key]
}

func (settings repositorySettings) BackupAssetSettingsSnapshot() (map[string]string, error) {
	values := make(map[string]string, len(settings))
	for key, value := range settings {
		values[key] = value
	}
	return values, nil
}

type scriptedProber struct {
	observation provider.RepositoryObservation
	err         error
	calls       int
	limits      provider.OperationLimits
	probe       func(provider.AccessBinding) (provider.RepositoryObservation, error)
}

func (prober *scriptedProber) Probe(_ context.Context, binding provider.AccessBinding, limits provider.OperationLimits) (provider.RepositoryObservation, error) {
	prober.calls++
	prober.limits = limits
	if prober.probe != nil {
		return prober.probe(binding)
	}
	return prober.observation, prober.err
}

type auditSpy struct {
	inputs []backupasset.AuditEventInput
	err    error
}

func (spy *auditSpy) Write(_ context.Context, input backupasset.AuditEventInput) error {
	spy.inputs = append(spy.inputs, input)
	return spy.err
}

func newRepositoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_loc=UTC&_foreign_keys=1", strings.ReplaceAll(t.Name(), "/", "_"), repositoryDBSequence.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Task{}, &model.TaskRun{}, &model.BackupRepository{}, &model.RepositoryAccessBinding{}, &model.TaskRepositoryLink{}, &model.RecoveryPoint{}, &model.RecoveryPointManifest{}, &model.RecoveryPointLease{}, &model.BackupAssetManagedHistoryLatch{}, &model.CatalogGeneration{}, &model.CatalogEntry{}, &model.NodeOwner{}, &model.WrappedDomainKey{}); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_repositories_provider_identity ON backup_repositories(provider_kind, repository_identity) WHERE repository_identity IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_repository_access_bindings_active ON repository_access_bindings(repository_id) WHERE status = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_task_repository_links_active_task ON task_repository_links(task_id) WHERE task_id IS NOT NULL AND unlinked_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_points_mutable_head ON recovery_points(repository_id) WHERE semantics = 'mutable_head'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_points_producing_task_run_unique ON recovery_points(producing_task_run_id) WHERE producing_task_run_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_points_native_source_unique ON recovery_points(repository_id, source_fingerprint) WHERE semantics = 'native_snapshot' AND source_fingerprint <> ''`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func enabledFoundation() *backupasset.FoundationService {
	return backupasset.NewFoundationService(completeRepositoryFoundationSettings(true))
}

func TestRepositoryFoundationSettingsFixtureCoversSearchOverlayConfig(t *testing.T) {
	searchConfig, overlayConfig, err := enabledFoundation().SearchOverlayConfig()
	if err != nil {
		t.Fatalf("SearchOverlayConfig: %v", err)
	}
	if !searchConfig.Enabled || searchConfig.CandidateLimit != 10000 || searchConfig.PageSizeMax != 200 ||
		overlayConfig.FavoriteQuota != 5000 || overlayConfig.IdempotencyKeyMaxBytes != 128 {
		t.Fatalf("incomplete Search/Overlay fixture: search=%+v overlay=%+v", searchConfig, overlayConfig)
	}
}

func completeRepositoryFoundationSettings(enabled bool) repositorySettings {
	values := make(repositorySettings, len(repositoryFoundationDefaults))
	for key, value := range repositoryFoundationDefaults {
		values[key] = value
	}
	values["backup_assets.enabled"] = fmt.Sprintf("%t", enabled)
	return values
}

var repositoryFoundationDefaults = repositorySettings{
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

func testObservation(kind backupasset.ProviderKind, identity string) provider.RepositoryObservation {
	version := backupasset.VersionMutableHead
	identityClass := provider.IdentityTaskScopedEndpoint
	if kind == backupasset.ProviderRestic {
		version = backupasset.VersionNativeSnapshot
		identityClass = provider.IdentityNativeRepository
	}
	return provider.RepositoryObservation{
		Provider: kind, IdentityClass: identityClass, RepositoryIdentity: identity, VersionMode: version,
		Capabilities: providerCapabilities(kind), AdapterRevision: "test-reader:v1", SourceRevision: strings.Repeat("a", 64),
		Availability: backupasset.PhysicalOnline, ObservedAt: time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC), ConfigFingerprint: strings.Repeat("b", 64),
	}
}

func providerCapabilities(kind backupasset.ProviderKind) backupasset.CapabilitySet {
	return backupasset.CapabilitySet{List: true, OpenSequential: true, OpenRange: kind != backupasset.ProviderRestic}
}

func scopedObservationProber(kind backupasset.ProviderKind) *scriptedProber {
	return &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		facts := append([]string(nil), binding.EndpointFacts...)
		if kind == backupasset.ProviderRclone {
			facts = append(facts, "backend:s3")
		}
		identity, err := provider.DeriveScopedIdentity(binding.IdentitySalt, provider.ScopedIdentityDocument{
			Provider: kind, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: facts,
		})
		if err != nil {
			return provider.RepositoryObservation{}, err
		}
		observation := testObservation(kind, identity)
		if kind == backupasset.ProviderRclone {
			observation.InternalProviderFacts = map[string]string{"backend": "s3"}
		}
		return observation, nil
	}}
}

func newRepositoryServiceForTest(t *testing.T, db *gorm.DB, kind backupasset.ProviderKind, prober *scriptedProber) *Service {
	t.Helper()
	registry := provider.NewRegistry()
	if err := registry.Register(kind, provider.Registration{Prober: prober}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: registry, Now: func() time.Time { return time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func seedTask(t *testing.T, db *gorm.DB, executorType, target, executorConfig string) model.Task {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", executorType, repositorySeedSequence.Add(1))
	node := model.Node{Name: "node-" + suffix, Host: "example.invalid", Port: 22, Username: "reader", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY", BasePath: "/data", BackupDir: "backup-" + suffix}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{Name: "task-" + suffix, NodeID: node.ID, ExecutorType: executorType, RsyncSource: "/source", RsyncTarget: target, ExecutorConfig: executorConfig, Status: "pending", Enabled: true}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	return taskEntity
}

func resticAttemptForExecution(t *testing.T, execution publication.Execution) provider.ResticAttemptV1 {
	t.Helper()
	if execution == nil || execution.Attempt() == nil {
		t.Fatal("publication execution has no attempt")
	}
	attempt, err := execution.Attempt().ResticAttempt()
	if err != nil {
		t.Fatalf("publication attempt is not Restic: %v", err)
	}
	return attempt
}

func resticProviderCommit(value provider.ResticCommitV1) provider.ProviderCommit {
	return provider.NewResticProviderCommit(value)
}
