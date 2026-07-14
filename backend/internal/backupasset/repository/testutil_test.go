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
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.Task{}, &model.TaskRun{}, &model.BackupRepository{}, &model.RepositoryAccessBinding{}, &model.TaskRepositoryLink{}, &model.RecoveryPoint{}, &model.RecoveryPointManifest{}, &model.RecoveryPointLease{}, &model.NodeOwner{}, &model.WrappedDomainKey{}); err != nil {
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

func completeRepositoryFoundationSettings(enabled bool) repositorySettings {
	values := make(repositorySettings, len(repositoryFoundationDefaults))
	for key, value := range repositoryFoundationDefaults {
		values[key] = value
	}
	values["backup_assets.enabled"] = fmt.Sprintf("%t", enabled)
	return values
}

var repositoryFoundationDefaults = repositorySettings{
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
