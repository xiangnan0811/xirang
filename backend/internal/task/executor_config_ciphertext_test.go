package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/repository"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestTaskExecutorConfigCiphertextSQLitePostgres(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		setupTaskExecutorConfigCiphertextKey(t)
		exerciseTaskExecutorConfigCiphertext(t, openMigratedBatchCommandSkipSQLiteDB(t))
	})
	t.Run("postgres", func(t *testing.T) {
		setupTaskExecutorConfigCiphertextKey(t)
		exerciseTaskExecutorConfigCiphertext(t, openMigratedBatchCommandSkipPostgresDB(t))
	})
}

func setupTaskExecutorConfigCiphertextKey(t *testing.T) {
	t.Helper()
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
}

func exerciseTaskExecutorConfigCiphertext(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	t.Setenv("RSYNC_ALLOWED_SOURCE_PREFIXES", "/data")
	t.Setenv("RSYNC_ALLOWED_TARGET_PREFIXES", "/backup")
	clock := time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC)
	db.NowFunc = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}

	const password = "FAKE_TASK_CIPHERTEXT_PASSWORD_FOR_TEST_ONLY"
	const config = `{"repository_password":"FAKE_TASK_CIPHERTEXT_PASSWORD_FOR_TEST_ONLY","exclude_patterns":["cache"],"repository_version":2}`
	node := model.Node{Name: "ciphertext-node", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "ciphertext-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	repo := gormrepo.NewTaskRepository(db)
	entity := model.Task{
		Name: "ciphertext-original", NodeID: node.ID, ExecutorType: "restic",
		RsyncSource: "/data/src", RsyncTarget: "/backup/repo", ExecutorConfig: config,
		Status: string(StatusPending), Enabled: true,
	}
	if err := repo.Create(ctx, &entity); err != nil {
		t.Fatalf("create task: %v", err)
	}

	type storedTask struct {
		Name           string
		ExecutorConfig string
		UpdatedAt      time.Time
		ArchivedAt     *time.Time
	}
	readRaw := func() storedTask {
		t.Helper()
		var row storedTask
		if err := db.Raw("SELECT name, executor_config, updated_at, archived_at FROM tasks WHERE id = ?", entity.ID).Row().Scan(&row.Name, &row.ExecutorConfig, &row.UpdatedAt, &row.ArchivedAt); err != nil {
			t.Fatalf("read raw task: %v", err)
		}
		return row
	}
	assertEncrypted := func(row storedTask, expected string) {
		t.Helper()
		if !secure.IsEncrypted(row.ExecutorConfig) || strings.Contains(row.ExecutorConfig, password) {
			t.Fatal("persisted executor configuration is not encrypted")
		}
		decoded, err := secure.DecryptIfNeeded(row.ExecutorConfig)
		if err != nil {
			t.Fatalf("decrypt persisted configuration: %v", err)
		}
		if decoded != expected {
			t.Fatal("persisted executor configuration did not decrypt to the expected bytes")
		}
	}
	assertSameRow := func(before, after storedTask) {
		t.Helper()
		if before.Name != after.Name || before.ExecutorConfig != after.ExecutorConfig || !before.UpdatedAt.Equal(after.UpdatedAt) ||
			(before.ArchivedAt == nil) != (after.ArchivedAt == nil) ||
			(before.ArchivedAt != nil && !before.ArchivedAt.Equal(*after.ArchivedAt)) {
			t.Fatal("rejected update changed the persisted task")
		}
	}

	created := readRaw()
	assertEncrypted(created, config)
	current, err := repo.FindByID(ctx, entity.ID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	api := NewTaskApiService(repo, gormrepo.NewNodeRepository(db), gormrepo.NewPolicyRepository(db), nil)
	renamed := "ciphertext-renamed"
	updated, err := api.UpdateTask(ctx, entity.ID, UpdateTaskInput{ExpectedRevision: TaskRevision(*current), Name: &renamed})
	if err != nil {
		t.Fatalf("rename task: %v", err)
	}
	if updated.Name != renamed || updated.ExecutorConfig != config {
		t.Fatal("rename did not preserve the task configuration")
	}
	settings, err := ProjectExecutorSettings(updated.ExecutorType, updated.ExecutorConfig)
	if err != nil {
		t.Fatalf("project executor settings: %v", err)
	}
	restic, ok := settings.(ResticExecutorSettingsResponse)
	if !ok || len(restic.ExcludePatterns) != 1 || restic.ExcludePatterns[0] != "cache" || restic.RepositoryVersion == nil || *restic.RepositoryVersion != 2 {
		t.Fatal("rename did not preserve Restic settings")
	}
	configured, err := ProjectExecutorSecretsConfigured(updated.ExecutorConfig)
	if err != nil || !configured.RepositoryPassword {
		t.Fatal("rename did not preserve configured secret")
	}
	afterRename := readRaw()
	assertEncrypted(afterRename, config)
	if afterRename.Name != renamed || !afterRename.UpdatedAt.After(created.UpdatedAt) {
		t.Fatal("rename did not persist the name and advance the revision")
	}

	current, err = repo.FindByID(ctx, entity.ID)
	if err != nil {
		t.Fatalf("load task before update: %v", err)
	}
	current.Name = "ciphertext-plain-update"
	current.Node = node
	current.Node.Name = "unexpected-associated-node-change"
	if err := repo.Update(ctx, current); err != nil {
		t.Fatalf("update task: %v", err)
	}
	if current.ExecutorConfig != config {
		t.Fatal("update changed the caller's executor configuration")
	}
	afterUpdate := readRaw()
	assertEncrypted(afterUpdate, config)
	if afterUpdate.Name != current.Name || !afterUpdate.UpdatedAt.After(afterRename.UpdatedAt) {
		t.Fatal("update did not persist the name and advance the revision")
	}
	var nodeName string
	if err := db.Raw("SELECT name FROM nodes WHERE id = ?", node.ID).Row().Scan(&nodeName); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if nodeName != node.Name {
		t.Fatal("task update changed an associated node")
	}

	current, err = repo.FindByID(ctx, entity.ID)
	if err != nil {
		t.Fatalf("load task before compare-and-set: %v", err)
	}
	oldRevision := current.UpdatedAt
	current.Name = "ciphertext-cas-update"
	if err := repo.UpdateWithRevision(ctx, current, oldRevision); err != nil {
		t.Fatalf("compare-and-set update: %v", err)
	}
	if current.ExecutorConfig != config {
		t.Fatal("compare-and-set changed the caller's executor configuration")
	}
	afterCAS := readRaw()
	assertEncrypted(afterCAS, config)
	if afterCAS.Name != current.Name || !afterCAS.UpdatedAt.After(afterUpdate.UpdatedAt) {
		t.Fatal("compare-and-set did not persist the name and advance the revision")
	}
	current.Name = "ciphertext-stale-update"
	if err := repo.UpdateWithRevision(ctx, current, oldRevision); !errors.Is(err, repository.ErrTaskRevisionConflict) {
		t.Fatalf("stale update returned %v, want revision conflict", err)
	}
	assertSameRow(afterCAS, readRaw())

	// Key errors must stop both update methods before any SQL can persist the
	// plaintext supplied by the caller.
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATA_ENCRYPTION_KEY", "")
	secure.ResetForTesting()
	for _, update := range []func() error{
		func() error { return repo.Update(ctx, current) },
		func() error { return repo.UpdateWithRevision(ctx, current, afterCAS.UpdatedAt) },
	} {
		err := update()
		if err == nil || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), config) {
			t.Fatal("encryption failure was missing or disclosed the configuration")
		}
		if current.ExecutorConfig != config {
			t.Fatal("failed update changed the caller's executor configuration")
		}
		assertSameRow(afterCAS, readRaw())
	}
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()

	archivedAt := time.Date(2025, 1, 2, 3, 4, 0, 0, time.UTC)
	if err := db.Model(&model.Task{}).Where("id = ?", entity.ID).UpdateColumn("archived_at", archivedAt).Error; err != nil {
		t.Fatalf("archive task: %v", err)
	}
	archived := readRaw()
	if err := repo.Update(ctx, current); !errors.Is(err, repository.ErrTaskArchived) {
		t.Fatalf("archived update returned %v, want task archived", err)
	}
	assertSameRow(archived, readRaw())
	if err := repo.UpdateWithRevision(ctx, current, archived.UpdatedAt); !errors.Is(err, repository.ErrTaskRevisionConflict) {
		t.Fatalf("archived compare-and-set returned %v, want revision conflict", err)
	}
	assertSameRow(archived, readRaw())
	assertEncrypted(archived, config)
}
