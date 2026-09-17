package bootstrap

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestMigrateLegacyResticTaskConfigsCutsOverAndPreservesTaskColumns(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RESTIC_MIGRATION_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatalf("migrate tasks: %v", err)
	}
	archivedAt := time.Date(2026, 9, 1, 2, 3, 4, 0, time.UTC)
	valid := createEncryptedResticMigrationTask(t, db,
		`{"repository_password":"FAKE_RESTIC_MIGRATION_PASSWORD_FOR_TEST_ONLY","append_only":true,"exclude_patterns":["cache"],"unknown":{"keep":true}}`,
		&archivedAt)
	falseLegacy := createEncryptedResticMigrationTask(t, db,
		`{"repository_password":"FAKE_RESTIC_MIGRATION_PASSWORD_FALSE_FOR_TEST_ONLY","append_only":false,"repository_version":1,"exclude_patterns":[]}`,
		nil)

	if err := MigrateLegacyResticTaskConfigs(db); err != nil {
		t.Fatalf("migrate legacy Restic task configs: %v", err)
	}

	validConfig := readResticMigrationConfig(t, db, valid.ID)
	if got := string(validConfig["repository_version"]); got != "2" {
		t.Fatalf("append_only=true repository_version=%s, want 2", got)
	}
	if _, exists := validConfig["append_only"]; exists {
		t.Fatal("append_only remained after startup migration")
	}
	if got := string(validConfig["repository_password"]); got != `"FAKE_RESTIC_MIGRATION_PASSWORD_FOR_TEST_ONLY"` {
		t.Fatalf("repository password changed: %s", got)
	}
	if got := string(validConfig["exclude_patterns"]); got != `["cache"]` {
		t.Fatalf("exclude patterns changed: %s", got)
	}
	if got := string(validConfig["unknown"]); got != `{"keep":true}` {
		t.Fatalf("unknown config changed: %s", got)
	}

	falseConfig := readResticMigrationConfig(t, db, falseLegacy.ID)
	if _, exists := falseConfig["append_only"]; exists {
		t.Fatal("append_only=false remained after startup migration")
	}
	if _, exists := falseConfig["repository_version"]; exists {
		t.Fatal("append_only=false retained repository_version instead of default")
	}

	var persisted model.Task
	if err := db.Session(&gorm.Session{SkipHooks: true}).First(&persisted, valid.ID).Error; err != nil {
		t.Fatalf("read migrated archived task: %v", err)
	}
	if persisted.ArchivedAt == nil || !persisted.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("migration changed archived state: archived_at=%v", persisted.ArchivedAt)
	}
	if persisted.Name != valid.Name || persisted.NodeID != valid.NodeID || persisted.CronSpec != valid.CronSpec {
		t.Fatalf("migration overwrote unrelated task columns: %+v", persisted)
	}

	before := rawResticMigrationConfig(t, db, valid.ID)
	if err := MigrateLegacyResticTaskConfigs(db); err != nil {
		t.Fatalf("idempotent Restic migration: %v", err)
	}
	after := rawResticMigrationConfig(t, db, valid.ID)
	if before != after {
		t.Fatalf("idempotent migration changed ciphertext: before=%q after=%q", before, after)
	}
}

func TestMigrateLegacyResticTaskConfigsIsolatesMalformedRows(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RESTIC_MIGRATION_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	db := openBootstrapTestDB(t)
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatalf("migrate tasks: %v", err)
	}
	valid := createEncryptedResticMigrationTask(t, db, `{"append_only":true}`, nil)
	malformed := createEncryptedResticMigrationTask(t, db, `{"append_only":"not-a-bool"}`, nil)
	conflict := createEncryptedResticMigrationTask(t, db, `{"append_only":true,"repository_version":1}`, nil)

	if err := MigrateLegacyResticTaskConfigs(db); err != nil {
		t.Fatalf("malformed task must not block startup migration: %v", err)
	}
	if got := string(readResticMigrationConfig(t, db, valid.ID)["repository_version"]); got != "2" {
		t.Fatalf("valid task repository_version=%s, want 2", got)
	}
	if got := rawResticMigrationPlaintext(t, db, malformed.ID); got != `{"append_only":"not-a-bool"}` {
		t.Fatalf("malformed task changed unexpectedly: %s", got)
	}
	if got := rawResticMigrationPlaintext(t, db, conflict.ID); got != `{"append_only":true,"repository_version":1}` {
		t.Fatalf("ambiguous task changed unexpectedly: %s", got)
	}
}

func createEncryptedResticMigrationTask(t *testing.T, db *gorm.DB, config string, archivedAt *time.Time) model.Task {
	t.Helper()
	encrypted, err := secure.EncryptString(config)
	if err != nil {
		t.Fatalf("encrypt fixture config: %v", err)
	}
	task := model.Task{
		Name:   "restic-migration-" + strings.ReplaceAll(t.Name(), "/", "-"),
		NodeID: 1, ExecutorType: "restic", ExecutorConfig: encrypted,
		RsyncSource: "/data/source", RsyncTarget: "/backup/repository",
		CronSpec: "@every 1h", Status: "pending", Enabled: true, ArchivedAt: archivedAt,
	}
	// Names are unique within a test even when multiple rows are seeded.
	task.Name = fmt.Sprintf("%s-%d", task.Name, time.Now().UnixNano())
	if err := db.Session(&gorm.Session{SkipHooks: true}).Create(&task).Error; err != nil {
		t.Fatalf("create Restic migration fixture: %v", err)
	}
	return task
}

func rawResticMigrationConfig(t *testing.T, db *gorm.DB, taskID uint) string {
	t.Helper()
	var row struct {
		ExecutorConfig string `gorm:"column:executor_config"`
	}
	if err := db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("executor_config").Where("id = ?", taskID).Take(&row).Error; err != nil {
		t.Fatalf("read raw Restic config: %v", err)
	}
	return row.ExecutorConfig
}

func rawResticMigrationPlaintext(t *testing.T, db *gorm.DB, taskID uint) string {
	t.Helper()
	plain, err := secure.DecryptIfNeeded(rawResticMigrationConfig(t, db, taskID))
	if err != nil {
		t.Fatalf("decrypt raw Restic config: %v", err)
	}
	return plain
}

func readResticMigrationConfig(t *testing.T, db *gorm.DB, taskID uint) map[string]json.RawMessage {
	t.Helper()
	plain := rawResticMigrationPlaintext(t, db, taskID)
	var config map[string]json.RawMessage
	if err := json.Unmarshal([]byte(plain), &config); err != nil {
		t.Fatalf("decode migrated Restic config: %v", err)
	}
	return config
}
