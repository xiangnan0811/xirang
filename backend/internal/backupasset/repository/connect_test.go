package repository

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type typedAssetAuditWriterSpy struct {
	input backupasset.AuditEventInput
	err   error
}

func (spy *typedAssetAuditWriterSpy) Write(_ context.Context, input backupasset.AuditEventInput) (model.BackupAssetAuditEvent, error) {
	spy.input = input
	return model.BackupAssetAuditEvent{}, spy.err
}

func TestAssetAuditSinkAdaptsFoundationWriter(t *testing.T) {
	writer := &typedAssetAuditWriterSpy{}
	sink := NewAssetAuditSink(writer)
	input := backupasset.AuditEventInput{Action: backupasset.AuditActionRepositoryConnect, Outcome: backupasset.AuditOutcomeSuccess, RepositoryID: strings.Repeat("a", 32)}
	if err := sink.Write(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if writer.input.Action != input.Action || writer.input.RepositoryID != input.RepositoryID {
		t.Fatalf("writer input=%+v", writer.input)
	}
}

func TestConnectFeatureDisabledTouchesNoDependencies(t *testing.T) {
	service, err := NewService(Dependencies{Foundation: backupasset.NewFoundationService(repositorySettings{"backup_assets.enabled": "false"})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: 1}, RequestContext{}); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("disabled connect error=%v", err)
	}
}

func TestConnectRejectsArchivedTaskBeforeProviderProbe(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	archivedAt := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("archived_at", archivedAt).Error; err != nil {
		t.Fatal(err)
	}
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("archived Task connect error=%v", err)
	}
	if prober.calls != 0 {
		t.Fatalf("archived Task reached Provider probe %d time(s)", prober.calls)
	}
}

func TestConnectRsyncIsProbeFirstIdempotentAndEncryptedAtRest(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID, DisplayName: "backup"}, RequestContext{CorrelationID: "corr-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID, DisplayName: "ignored"}, RequestContext{CorrelationID: "corr-2"})
	if err != nil || second.Repository.ID != first.Repository.ID || second.MutablePoint == nil || first.MutablePoint == nil || second.MutablePoint.ID != first.MutablePoint.ID {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	if prober.calls != 2 {
		t.Fatalf("probe calls=%d", prober.calls)
	}
	for modelType, want := range map[any]int64{&model.BackupRepository{}: 1, &model.RepositoryAccessBinding{}: 1, &model.TaskRepositoryLink{}: 1, &model.RecoveryPoint{}: 1} {
		var count int64
		if err := db.Model(modelType).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%T count=%d want=%d err=%v", modelType, count, want, err)
		}
	}
	var encrypted string
	if err := db.Raw("SELECT encrypted_config FROM repository_access_bindings LIMIT 1").Scan(&encrypted).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, "enc:v2:") || strings.Contains(encrypted, taskEntity.RsyncTarget) {
		t.Fatalf("binding not encrypted at rest: %q", encrypted)
	}
}

func TestConnectWithoutReplacementProbesAndRefreshesRetainedBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	oldSecret := "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"
	newSecret := "FAKE_NEW_RESTIC_PASSWORD_FOR_TEST_ONLY"
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"`+oldSecret+`"}`)
	nativeIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("a", 64)
	revision := "restic-reader:v1"
	prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		if string(binding.Secret) != oldSecret {
			t.Fatalf("connect without replace probed unretained access secret")
		}
		observation := testObservation(backupasset.ProviderRestic, nativeIdentity)
		observation.AdapterRevision = revision
		return observation, nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_config", `{"repository_password":"`+newSecret+`"}`).Error; err != nil {
		t.Fatal(err)
	}
	revision = "restic-reader:v2"
	second, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Repository.CapabilityRevision != first.Repository.CapabilityRevision+1 {
		t.Fatalf("adapter revision did not advance capability revision: first=%d second=%d", first.Repository.CapabilityRevision, second.Repository.CapabilityRevision)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", first.Repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if document.Secret != oldSecret || document.AdapterRevision != revision {
		t.Fatalf("retained binding was replaced or observation metadata stayed stale: %+v", document)
	}
}

func TestConnectProbeFailureWritesNothing(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := &scriptedProber{err: errors.New("provider unavailable")}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err == nil {
		t.Fatal("probe failure accepted")
	}
	var count int64
	if err := db.Model(&model.BackupRepository{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("repository count=%d err=%v", count, err)
	}
}

func TestConnectRejectsScopedIdentityThatDoesNotMatchBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRsync, provider.ScopedIdentityPrefix(backupasset.ProviderRsync)+strings.Repeat("d", 64))}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("mismatched scoped identity error=%v", err)
	}
	var count int64
	if err := db.Model(&model.BackupRepository{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("repository count=%d err=%v", count, err)
	}
}

func TestConnectSameTaskDifferentIdentityConflictsWithoutMutation(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	newTarget := t.TempDir()
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_target", newTarget).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("identity conflict error=%v", err)
	}
	var repository model.BackupRepository
	if err := db.First(&repository, "id = ?", first.Repository.ID).Error; err != nil {
		t.Fatal(err)
	}
	if repository.RepositoryIdentity == nil || !strings.HasPrefix(*repository.RepositoryIdentity, provider.ScopedIdentityPrefix(backupasset.ProviderRsync)) {
		t.Fatalf("identity changed: %+v", repository)
	}
}

func TestConnectSharedResticIdentityReusesRepositoryWithoutLineageExpansion(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-a", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := "restic-native:v1:" + strings.Repeat("c", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{})
	if err != nil || first.Repository.ID != second.Repository.ID || first.MutablePoint != nil || second.MutablePoint != nil {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	var repositories, bindings, links, points int64
	db.Model(&model.BackupRepository{}).Count(&repositories)
	db.Model(&model.RepositoryAccessBinding{}).Count(&bindings)
	db.Model(&model.TaskRepositoryLink{}).Count(&links)
	db.Model(&model.RecoveryPoint{}).Count(&points)
	if repositories != 1 || bindings != 1 || links != 2 || points != 0 {
		t.Fatalf("repositories=%d bindings=%d links=%d points=%d", repositories, bindings, links, points)
	}
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", first.Repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if document.AdapterRevision != "test-reader:v1" || document.NativeRepositoryID != strings.Repeat("c", 64) {
		t.Fatalf("Restic binding facts=%+v", document)
	}
}

func TestConnectSharedResticRejectsArchivedRetainedBindingWithoutReplacement(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-a", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)})
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := time.Date(2026, 7, 13, 15, 0, 0, 0, time.UTC)
	if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("archived_at", archivedAt).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("connect with archived retained binding error=%v", err)
	}
	var links int64
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", connected.Repository.ID).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
}

func TestConnectRejectsRetainedBindingAfterTaskProviderDrift(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("9", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "rclone").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("connect after Task Provider drift error=%v", err)
	}
	if prober.calls != 1 {
		t.Fatalf("Provider drift reached retained probe: calls=%d", prober.calls)
	}
}

func TestConnectSharedResticRejectsProviderDriftedRetainedBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-a", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("7", 64)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)})
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("executor_type", "rclone").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("shared connect with Provider-drifted retained binding error=%v", err)
	}
	var links int64
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", connected.Repository.ID).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
}

func TestConnectSharedResticRejectsNodeDriftedRetainedBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	firstTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-a", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	secondTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("5", 64)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)})
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("node_id", secondTask.NodeID).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("shared connect with Node-drifted retained binding error=%v", err)
	}
	var links int64
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", connected.Repository.ID).Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
}

func TestConnectPropagatesCorrelationIntoRemoteCredentialAuditContext(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		runtimeAccess, ok := binding.AdapterData.(provider.ResticRuntimeAccess)
		if !ok || runtimeAccess.Command == nil {
			t.Fatalf("missing remote command runtime: %+v", binding.AdapterData)
		}
		audit := runtimeAccess.Command.Audit
		if audit.CorrelationID != "corr-remote" || audit.UserID != 42 || audit.Username != "admin-user" || audit.Role != "admin" || audit.TaskID == nil || *audit.TaskID != taskEntity.ID || audit.Action != "" {
			t.Fatalf("remote audit context=%+v", audit)
		}
		return testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+strings.Repeat("7", 64)), nil
	}}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	_, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{
		CorrelationID: "corr-remote", Actor: backupasset.AuditActor{UserID: 42, Username: "admin-user", Role: "admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConnectMutableTasksNeverMergeEvenWithMatchingEndpoint(t *testing.T) {
	db := newRepositoryTestDB(t)
	target := t.TempDir()
	firstTask := seedTask(t, db, "rsync", target, "")
	secondTask := seedTask(t, db, "rsync", target, "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: firstTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Connect(context.Background(), ConnectRequest{TaskID: secondTask.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Repository.ID == second.Repository.ID || first.MutablePoint == nil || second.MutablePoint == nil || first.MutablePoint.ID == second.MutablePoint.ID {
		t.Fatalf("mutable Tasks merged: first=%+v second=%+v", first, second)
	}
	var repositories, points int64
	if err := db.Model(&model.BackupRepository{}).Count(&repositories).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Count(&points).Error; err != nil {
		t.Fatal(err)
	}
	if repositories != 2 || points != 2 {
		t.Fatalf("repositories=%d points=%d", repositories, points)
	}
}

func TestConnectReplacesOnlyExplicitlyTargetedAccessBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	first, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	var original model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", first.Repository.ID, bindingStatusActive).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID, RepositoryID: first.Repository.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var beforeReplace int64
	if err := db.Model(&model.RepositoryAccessBinding{}).Where("repository_id = ?", first.Repository.ID).Count(&beforeReplace).Error; err != nil || beforeReplace != 1 {
		t.Fatalf("bindings before replace=%d err=%v", beforeReplace, err)
	}
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID, RepositoryID: first.Repository.ID, ReplaceAccess: true}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	var bindings []model.RepositoryAccessBinding
	if err := db.Where("repository_id = ?", first.Repository.ID).Order("created_at ASC").Find(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[0].ID != original.ID || bindings[0].Status != bindingStatusRevoked || bindings[0].RevokedAt == nil || bindings[1].Status != bindingStatusActive || bindings[1].ID == original.ID {
		t.Fatalf("bindings after replace=%+v", bindings)
	}
}

func TestConnectRollsBackEveryRowWhenLinkInsertFails(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	injected := errors.New("injected link insert failure")
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail_repository_link", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.TaskRepositoryLink{}).TableName() {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); !errors.Is(err, injected) {
		t.Fatalf("connect error=%v", err)
	}
	for modelType := range map[any]struct{}{
		&model.BackupRepository{}: {}, &model.RepositoryAccessBinding{}: {}, &model.TaskRepositoryLink{}: {}, &model.RecoveryPoint{}: {},
	} {
		var count int64
		if err := db.Model(modelType).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%T count=%d err=%v", modelType, count, err)
		}
	}
}

func TestConnectRetriesKnownUniquenessRaceWithoutReprobing(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	var injected atomic.Bool
	if err := db.Callback().Create().Before("gorm:create").Register("test:inject_repository_identity_race", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupRepository{}).TableName() && injected.CompareAndSwap(false, true) {
			_ = tx.AddError(errors.New("UNIQUE constraint failed: backup_repositories.provider_kind, backup_repositories.repository_identity"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	result, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || result.Repository.ID == "" || result.MutablePoint == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if prober.calls != 1 {
		t.Fatalf("probe calls=%d want=1", prober.calls)
	}
}

func TestConnectConstraintClassifierIsIndexScoped(t *testing.T) {
	for _, message := range []string{
		"UNIQUE constraint failed: backup_repositories.provider_kind, backup_repositories.repository_identity",
		`duplicate key value violates unique constraint "idx_backup_repositories_provider_identity" (SQLSTATE 23505)`,
		"UNIQUE constraint failed: repository_access_bindings.repository_id",
		`duplicate key value violates unique constraint "idx_repository_access_bindings_active" (SQLSTATE 23505)`,
		"UNIQUE constraint failed: task_repository_links.task_id",
		`duplicate key value violates unique constraint "idx_task_repository_links_active_task" (SQLSTATE 23505)`,
		"UNIQUE constraint failed: recovery_points.repository_id",
		`duplicate key value violates unique constraint "idx_recovery_points_mutable_head" (SQLSTATE 23505)`,
	} {
		if !isConnectConstraintConflict(errors.New(message)) {
			t.Fatalf("known connect constraint not classified: %s", message)
		}
	}
	for _, message := range []string{
		"UNIQUE constraint failed: users.email",
		"FOREIGN KEY constraint failed",
		`duplicate key value violates unique constraint "unrelated_index" (SQLSTATE 23505)`,
	} {
		if isConnectConstraintConflict(errors.New(message)) {
			t.Fatalf("unrelated constraint classified: %s", message)
		}
	}
}

func TestConnectClampsRecordLimitToValidMetadataCeiling(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	prober := scopedObservationProber(backupasset.ProviderRsync)
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, prober)
	service.foundation = backupasset.NewFoundationService(repositorySettings{
		"backup_assets.enabled":                       "true",
		"backup_assets.provider_operation_timeout":    "5s",
		"backup_assets.provider_max_concurrency":      "1",
		"backup_assets.provider_metadata_limit_bytes": "65536",
	})
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	if err := prober.limits.Validate(); err != nil || prober.limits.MaxRecordBytes != 65536 {
		t.Fatalf("probe limits=%+v err=%v", prober.limits, err)
	}
}
