package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/repository"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/task/scheduler"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTaskArchiveDisablesUnlinksAndPreservesHistory(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	remover := &recordingScheduleRemover{}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: remover.RemoveSchedule,
		Now:            func() time.Time { return fixture.now },
	})

	result, err := service.Archive(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if !result.Archived || !result.Unlinked || !result.ScheduleRemoved || result.ProviderBytesDeleted {
		t.Fatalf("archive result=%+v, want archived/unlinked/schedule_removed and provider_bytes_deleted=false", result)
	}
	if len(remover.calls) != 1 || remover.calls[0] != fixture.task.ID {
		t.Fatalf("schedule removals=%v, want [%d]", remover.calls, fixture.task.ID)
	}

	var archived model.Task
	if err := fixture.db.First(&archived, fixture.task.ID).Error; err != nil {
		t.Fatalf("load archived task: %v", err)
	}
	if archived.Enabled || archived.ArchivedAt == nil || !archived.ArchivedAt.Equal(fixture.now) {
		t.Fatalf("task enabled=%v archived_at=%v, want disabled at %s", archived.Enabled, archived.ArchivedAt, fixture.now)
	}
	if archived.ExecutorType != "restic" || archived.ExecutorConfig != fixture.executorConfig || archived.Command != fixture.task.Command {
		t.Fatalf("executor/history was erased: type=%q config=%q command=%q", archived.ExecutorType, archived.ExecutorConfig, archived.Command)
	}

	var link model.TaskRepositoryLink
	if err := fixture.db.First(&link, "id = ?", fixture.link.ID).Error; err != nil {
		t.Fatalf("load unlinked row: %v", err)
	}
	if link.TaskID == nil || *link.TaskID != fixture.task.ID || link.UnlinkedAt == nil || !link.UnlinkedAt.Equal(fixture.now) {
		t.Fatalf("link task_id=%v unlinked_at=%v, want task %d and %s", link.TaskID, link.UnlinkedAt, fixture.task.ID, fixture.now)
	}
	if link.TaskNameSnapshot != "archive-task" || link.NodeIDSnapshot != fixture.node.ID || link.NodeNameSnapshot != fixture.node.Name {
		t.Fatalf("link snapshots changed: %+v", link)
	}
	if link.EncryptedLegacyLocator != fixture.legacyLocator {
		t.Fatalf("encrypted protected locator changed: %q", link.EncryptedLegacyLocator)
	}

	assertTaskArchiveHistoryPreserved(t, fixture)
	if fixture.provider.calls.Load() != 0 {
		t.Fatalf("provider mutations=%d, want 0", fixture.provider.calls.Load())
	}
}

func TestTaskArchiveClearsTaskIDForNonNativePublicationModes(t *testing.T) {
	tests := []struct {
		name string
		mode backupasset.TaskPublicationMode
	}{
		{name: "legacy mutable", mode: backupasset.PublicationLegacyMutable},
		{name: "Rsync hardlink", mode: backupasset.PublicationVersionedHardlink},
		{name: "Rsync full copy", mode: backupasset.PublicationVersionedFullCopy},
		{name: "Rsync prefix", mode: backupasset.PublicationVersionedPrefix},
		{name: "Rclone native object versions", mode: backupasset.PublicationNativeObjectVersions},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTaskArchiveFixtureWithPublicationMode(t, test.mode)
			service := NewArchiveService(ArchiveDependencies{
				DB:             fixture.db,
				RemoveSchedule: func(uint) error { return nil },
				Now:            func() time.Time { return fixture.now },
			})
			result, err := service.Archive(context.Background(), fixture.task.ID)
			if err != nil {
				t.Fatalf("Archive: %v", err)
			}
			if !result.Archived || !result.Unlinked || result.ProviderBytesDeleted {
				t.Fatalf("archive result=%+v, want archived/unlinked and provider_bytes_deleted=false", result)
			}
			var link model.TaskRepositoryLink
			if err := fixture.db.First(&link, "id = ?", fixture.link.ID).Error; err != nil {
				t.Fatalf("load archived link: %v", err)
			}
			if link.TaskID != nil || link.UnlinkedAt == nil || !link.UnlinkedAt.Equal(fixture.now) {
				t.Fatalf("link mode=%q task_id=%v unlinked_at=%v, want NULL and %s", test.mode, link.TaskID, link.UnlinkedAt, fixture.now)
			}
		})
	}
}

func TestTaskArchivePreservesRepositoryPointRunAndAudit(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	if _, err := service.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	assertTaskArchiveHistoryPreserved(t, fixture)
}

func TestTaskArchiveHasZeroProviderEffects(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	result, err := service.Archive(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if result.ProviderBytesDeleted {
		t.Fatal("archive result must not claim Provider bytes were deleted")
	}
	if fixture.provider.calls.Load() != 0 {
		t.Fatalf("provider mutations=%d, want 0", fixture.provider.calls.Load())
	}
	var repository model.BackupRepository
	if err := fixture.db.First(&repository, "id = ?", fixture.repository.ID).Error; err != nil {
		t.Fatalf("load repository: %v", err)
	}
	if repository.Status != fixture.repository.Status || repository.DisplayName != fixture.repository.DisplayName {
		t.Fatalf("repository mutated: %+v", repository)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", fixture.point.ID).Error; err != nil {
		t.Fatalf("load recovery point: %v", err)
	}
	if point.EncryptedProviderLocator != fixture.providerLocator || point.EncryptedRollbackLocator != fixture.rollbackLocator {
		t.Fatalf("provider locators mutated: provider=%q rollback=%q", point.EncryptedProviderLocator, point.EncryptedRollbackLocator)
	}
}

func TestTaskArchiveSoftDeletesTaskLinkRetentionPolicies(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	policyID := "dddddddddddddddddddddddddddddddd"
	policy := model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeTaskLink), ScopeID: fixture.link.ID,
		Revision: 2, RulesJSON: `{"version":1,"age":{"keep_days":14}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&policy).Error; err != nil {
		t.Fatalf("seed task-link retention policy: %v", err)
	}
	auditor := &recordingArchiveAuditor{}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
		WriteTx:        auditor.WriteTx,
	})
	if _, err := service.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	var active int64
	if err := fixture.db.Model(&model.BackupRetentionPolicy{}).
		Where("scope_kind = ? AND scope_id = ? AND status = ?", backupasset.RetentionPolicyScopeTaskLink, fixture.link.ID, backupasset.RetentionPolicyActive).
		Count(&active).Error; err != nil {
		t.Fatalf("count active policies: %v", err)
	}
	if active != 0 {
		t.Fatalf("active task-link policies=%d, want 0", active)
	}
	var stored model.BackupRetentionPolicy
	if err := fixture.db.First(&stored, "id = ?", policyID).Error; err != nil {
		t.Fatalf("reload policy: %v", err)
	}
	if stored.Status != string(backupasset.RetentionPolicyDeleted) || stored.DeletedAt == nil || stored.Revision != 3 {
		t.Fatalf("policy status=%q revision=%d deleted_at=%v, want deleted revision 3", stored.Status, stored.Revision, stored.DeletedAt)
	}
	if stored.UpdatedBy != 1 {
		t.Fatalf("updated_by=%d, want unchanged 1", stored.UpdatedBy)
	}

	var workerVisible int64
	if err := fixture.db.Model(&model.BackupRetentionPolicy{}).
		Where("status = ? AND id = ?", backupasset.RetentionPolicyActive, policyID).
		Count(&workerVisible).Error; err != nil {
		t.Fatalf("count worker-visible policies: %v", err)
	}
	if workerVisible != 0 {
		t.Fatal("worker ListActiveAfter query would still see the archived Task-link policy")
	}
	if auditor.writes != 1 || auditor.last.Action != backupasset.AuditActionRetentionPolicyDelete {
		t.Fatalf("archive policy audits writes=%d action=%q, want one retention_policy_delete", auditor.writes, auditor.last.Action)
	}
	if auditor.last.RepositoryID != fixture.repository.ID || auditor.last.Fields[backupasset.AuditFieldPolicyID] != policyID {
		t.Fatalf("delete audit=%+v, want repository %s and policy %s", auditor.last, fixture.repository.ID, policyID)
	}
	if auditor.last.Actor.Username != "system" || auditor.last.Actor.Role != "system" {
		t.Fatalf("delete audit actor=%+v, want documented system actor when Archive has no HTTP user", auditor.last.Actor)
	}
	if _, ok := auditor.last.Fields[backupasset.AuditFieldCorrelationID]; ok {
		t.Fatalf("empty archive context invented correlation=%v", auditor.last.Fields[backupasset.AuditFieldCorrelationID])
	}
}

func TestTaskArchiveRetentionPolicyDeleteAuditCopiesRequestID(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	policyID := "cccccccccccccccccccccccccccccccc"
	policy := model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeTaskLink), ScopeID: fixture.link.ID,
		Revision: 2, RulesJSON: `{"version":1,"age":{"keep_days":14}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&policy).Error; err != nil {
		t.Fatalf("seed task-link retention policy: %v", err)
	}
	auditor := &recordingArchiveAuditor{}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
		WriteTx:        auditor.WriteTx,
	})
	ctx := WithArchiveActor(context.Background(), backupasset.AuditActor{UserID: 9, Username: "archive-admin", Role: "admin"})
	ctx = WithArchiveCorrelationID(ctx, "archive-http-request-1")
	if _, err := service.Archive(ctx, fixture.task.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if auditor.writes != 1 || auditor.last.Action != backupasset.AuditActionRetentionPolicyDelete {
		t.Fatalf("archive policy audits writes=%d action=%q, want one retention_policy_delete", auditor.writes, auditor.last.Action)
	}
	if auditor.last.Fields[backupasset.AuditFieldCorrelationID] != "archive-http-request-1" {
		t.Fatalf("correlation=%v, want archive-http-request-1", auditor.last.Fields[backupasset.AuditFieldCorrelationID])
	}
	if auditor.last.Actor.Username != "archive-admin" || auditor.last.Actor.Role != "admin" {
		t.Fatalf("delete audit actor=%+v, want HTTP archive actor", auditor.last.Actor)
	}

	emptyAuditor := &recordingArchiveAuditor{}
	emptyService := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
		WriteTx:        emptyAuditor.WriteTx,
	})
	if _, err := emptyService.Archive(WithArchiveCorrelationID(context.Background(), "   "), fixture.task.ID); err != nil {
		t.Fatalf("idempotent archive: %v", err)
	}
	if emptyAuditor.writes != 0 {
		t.Fatalf("blank correlation invented %d extra policy-delete audits", emptyAuditor.writes)
	}
}

func TestTaskArchiveSucceedsWhenRetentionPolicyTableMissing(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	if err := fixture.db.Migrator().DropTable(&model.BackupRetentionPolicy{}); err != nil {
		t.Fatalf("drop retention policy table: %v", err)
	}
	if fixture.db.Migrator().HasTable(&model.BackupRetentionPolicy{}) {
		t.Fatal("policy table still present after drop")
	}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	result, err := service.Archive(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("Archive with missing retention policy table: %v", err)
	}
	if !result.Archived || !result.Unlinked || result.ProviderBytesDeleted {
		t.Fatalf("result=%+v, want archived/unlinked when policy table is absent", result)
	}

	var archived model.Task
	if err := fixture.db.First(&archived, fixture.task.ID).Error; err != nil {
		t.Fatalf("load archived task: %v", err)
	}
	if archived.Enabled || archived.ArchivedAt == nil {
		t.Fatalf("missing policy table must not block archive: enabled=%v archived_at=%v", archived.Enabled, archived.ArchivedAt)
	}
	var link model.TaskRepositoryLink
	if err := fixture.db.First(&link, "id = ?", fixture.link.ID).Error; err != nil {
		t.Fatalf("load link: %v", err)
	}
	if link.TaskID == nil || *link.TaskID != fixture.task.ID || link.UnlinkedAt == nil {
		t.Fatalf("missing policy table must still unlink with historical owner: task_id=%v unlinked_at=%v", link.TaskID, link.UnlinkedAt)
	}
}

func TestTaskArchiveRetentionPolicyDeleteAuditFailureLeavesTaskAndPolicy(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	policyID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	policy := model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeTaskLink), ScopeID: fixture.link.ID,
		Revision: 2, RulesJSON: `{"version":1,"age":{"keep_days":14}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&policy).Error; err != nil {
		t.Fatalf("seed task-link retention policy: %v", err)
	}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
		WriteTx: func(context.Context, *gorm.DB, backupasset.AuditEventInput) error {
			return errors.New("retention policy delete audit unavailable")
		},
	})
	if _, err := service.Archive(context.Background(), fixture.task.ID); err == nil {
		t.Fatal("Archive succeeded despite audit failure")
	}

	var stillLive model.Task
	if err := fixture.db.First(&stillLive, fixture.task.ID).Error; err != nil {
		t.Fatalf("load task after audit failure: %v", err)
	}
	if !stillLive.Enabled || stillLive.ArchivedAt != nil {
		t.Fatalf("audit failure must leave the task enabled, got enabled=%v archived_at=%v", stillLive.Enabled, stillLive.ArchivedAt)
	}
	var stored model.BackupRetentionPolicy
	if err := fixture.db.First(&stored, "id = ?", policyID).Error; err != nil {
		t.Fatalf("reload policy after audit failure: %v", err)
	}
	if stored.Status != string(backupasset.RetentionPolicyActive) || stored.DeletedAt != nil || stored.Revision != 2 {
		t.Fatalf("policy status=%q revision=%d deleted_at=%v, want active revision 2", stored.Status, stored.Revision, stored.DeletedAt)
	}
	var link model.TaskRepositoryLink
	if err := fixture.db.First(&link, "id = ?", fixture.link.ID).Error; err != nil {
		t.Fatalf("load link after audit failure: %v", err)
	}
	if link.TaskID == nil || *link.TaskID != fixture.task.ID || link.UnlinkedAt != nil {
		t.Fatalf("audit failure must leave the active link, got task_id=%v unlinked_at=%v", link.TaskID, link.UnlinkedAt)
	}
}

func TestTaskArchiveRejectsLiveDependent(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	dependent := model.Task{
		Name:            "live-dependent",
		NodeID:          fixture.node.ID,
		DependsOnTaskID: &fixture.task.ID,
		ExecutorType:    "command",
		Command:         "true",
		Status:          "pending",
		Enabled:         true,
	}
	if err := fixture.db.Create(&dependent).Error; err != nil {
		t.Fatalf("create live dependent: %v", err)
	}
	remover := &recordingScheduleRemover{}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: remover.RemoveSchedule,
		Now:            func() time.Time { return fixture.now },
	})

	_, err := service.Archive(context.Background(), fixture.task.ID)
	if !errors.Is(err, ErrTaskArchiveHasDependents) {
		t.Fatalf("Archive error=%v, want ErrTaskArchiveHasDependents", err)
	}
	if len(remover.calls) != 0 {
		t.Fatalf("dependent conflict removed schedule: %v", remover.calls)
	}
	var stillLive model.Task
	if err := fixture.db.First(&stillLive, fixture.task.ID).Error; err != nil {
		t.Fatalf("load task after dependent conflict: %v", err)
	}
	if !stillLive.Enabled || stillLive.ArchivedAt != nil {
		t.Fatalf("conflict must leave the task enabled, got enabled=%v archived_at=%v", stillLive.Enabled, stillLive.ArchivedAt)
	}
	var link model.TaskRepositoryLink
	if err := fixture.db.First(&link, "id = ?", fixture.link.ID).Error; err != nil {
		t.Fatalf("load link after dependent conflict: %v", err)
	}
	if link.TaskID == nil || *link.TaskID != fixture.task.ID || link.UnlinkedAt != nil {
		t.Fatalf("conflict must leave the active link, got task_id=%v unlinked_at=%v", link.TaskID, link.UnlinkedAt)
	}
}

func TestTaskArchiveAllowsArchivedDependent(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	archivedAt := fixture.now.Add(-time.Hour)
	dependent := model.Task{
		Name:            "archived-dependent",
		NodeID:          fixture.node.ID,
		DependsOnTaskID: &fixture.task.ID,
		ExecutorType:    "command",
		Command:         "true",
		Status:          "pending",
		Enabled:         false,
		ArchivedAt:      &archivedAt,
	}
	if err := fixture.db.Create(&dependent).Error; err != nil {
		t.Fatalf("create archived dependent: %v", err)
	}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	result, err := service.Archive(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("Archive with archived dependent: %v", err)
	}
	if !result.Archived {
		t.Fatalf("result=%+v, want archived", result)
	}
}

func TestTaskCreateRejectsDependencyOnArchivedTask(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	if _, err := service.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive parent: %v", err)
	}
	api := NewTaskApiService(
		gormrepo.NewTaskRepository(fixture.db),
		gormrepo.NewNodeRepository(fixture.db),
		gormrepo.NewPolicyRepository(fixture.db),
		nil,
	)
	_, err := api.CreateTask(context.Background(), CreateTaskInput{
		Name: "depends-on-archived", NodeID: fixture.node.ID, ExecutorType: "command",
		Command: "true", DependsOnTaskID: &fixture.task.ID,
	})
	if err == nil {
		t.Fatal("CreateTask accepted a dependency on an archived task")
	}
	var liveDeps int64
	if err := fixture.db.Model(&model.Task{}).
		Where("depends_on_task_id = ? AND archived_at IS NULL", fixture.task.ID).
		Count(&liveDeps).Error; err != nil {
		t.Fatalf("count live dependents: %v", err)
	}
	if liveDeps != 0 {
		t.Fatalf("live dependents=%d, want 0", liveDeps)
	}
}

func TestTaskUpdateRejectsDependencyOnArchivedTask(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	child := model.Task{
		Name: "live-child", NodeID: fixture.node.ID, ExecutorType: "command",
		Command: "true", Status: "pending", Enabled: true,
	}
	if err := fixture.db.Create(&child).Error; err != nil {
		t.Fatalf("create live child: %v", err)
	}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	if _, err := service.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive parent: %v", err)
	}
	api := NewTaskApiService(
		gormrepo.NewTaskRepository(fixture.db),
		gormrepo.NewNodeRepository(fixture.db),
		gormrepo.NewPolicyRepository(fixture.db),
		nil,
	)
	_, err := api.UpdateTask(context.Background(), child.ID, CreateTaskInput{
		Name: child.Name, NodeID: fixture.node.ID, ExecutorType: "command",
		Command: "true", DependsOnTaskID: &fixture.task.ID,
	})
	if err == nil {
		t.Fatal("UpdateTask accepted a dependency on an archived task")
	}
	var reloaded model.Task
	if err := fixture.db.First(&reloaded, child.ID).Error; err != nil {
		t.Fatalf("reload child: %v", err)
	}
	if reloaded.DependsOnTaskID != nil {
		t.Fatalf("child depends_on_task_id=%v, want nil", reloaded.DependsOnTaskID)
	}
}

func TestTaskArchiveVersusCreateDependencyLeavesNoLiveArchivedParent(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(2)
	api := NewTaskApiService(
		gormrepo.NewTaskRepository(fixture.db),
		gormrepo.NewNodeRepository(fixture.db),
		gormrepo.NewPolicyRepository(fixture.db),
		nil,
	)
	archive := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := archive.Archive(context.Background(), fixture.task.ID)
		errCh <- err
	}()
	go func() {
		defer wg.Done()
		_, err := api.CreateTask(context.Background(), CreateTaskInput{
			Name: "raced-dependent", NodeID: fixture.node.ID, ExecutorType: "command",
			Command: "true", DependsOnTaskID: &fixture.task.ID,
		})
		errCh <- err
	}()
	wg.Wait()
	close(errCh)

	var parent model.Task
	if err := fixture.db.First(&parent, fixture.task.ID).Error; err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	var liveDeps []model.Task
	if err := fixture.db.Where("depends_on_task_id = ? AND archived_at IS NULL", fixture.task.ID).Find(&liveDeps).Error; err != nil {
		t.Fatalf("load live dependents: %v", err)
	}
	if parent.ArchivedAt != nil && len(liveDeps) != 0 {
		t.Fatalf("live task depends on archived parent: parent=%+v dependents=%+v", parent, liveDeps)
	}
}

func TestTaskArchiveRemovesScheduleOnlyAfterCommit(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	observer, err := gorm.Open(sqlite.Open(fixture.dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open observer connection: %v", err)
	}
	remover := &recordingScheduleRemover{}
	remover.before = func(taskID uint) {
		var seen model.Task
		if err := observer.First(&seen, taskID).Error; err != nil {
			t.Errorf("schedule removal could not read committed task: %v", err)
			return
		}
		if seen.Enabled || seen.ArchivedAt == nil {
			t.Errorf("schedule removed before committed archive: enabled=%v archived_at=%v", seen.Enabled, seen.ArchivedAt)
		}
		var link model.TaskRepositoryLink
		if err := observer.First(&link, "id = ?", fixture.link.ID).Error; err != nil {
			t.Errorf("schedule removal could not read committed link: %v", err)
			return
		}
		if link.TaskID == nil || *link.TaskID != fixture.task.ID || link.UnlinkedAt == nil {
			t.Errorf("schedule removed before committed unlink: task_id=%v unlinked_at=%v", link.TaskID, link.UnlinkedAt)
		}
	}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: remover.RemoveSchedule,
		Now:            func() time.Time { return fixture.now },
	})
	if _, err := service.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if len(remover.calls) != 1 {
		t.Fatalf("schedule removals=%v, want 1", remover.calls)
	}
}

func TestTaskArchiveCommitFailureLeavesSchedule(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	pool := &archiveCommitFailPool{DB: sqlDB}
	pool.fail.Store(true)
	fixture.db.ConnPool = pool
	fixture.db.Statement.ConnPool = pool

	remover := &recordingScheduleRemover{}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: remover.RemoveSchedule,
		Now:            func() time.Time { return fixture.now },
	})
	if _, err := service.Archive(context.Background(), fixture.task.ID); err == nil {
		t.Fatal("Archive succeeded, want commit failure")
	}
	if len(remover.calls) != 0 {
		t.Fatalf("commit failure removed schedule: %v", remover.calls)
	}

	observer, err := gorm.Open(sqlite.Open(fixture.dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open observer connection: %v", err)
	}
	var stillLive model.Task
	if err := observer.First(&stillLive, fixture.task.ID).Error; err != nil {
		t.Fatalf("load task after commit failure: %v", err)
	}
	if !stillLive.Enabled || stillLive.ArchivedAt != nil {
		t.Fatalf("commit failure must leave the task enabled, got enabled=%v archived_at=%v", stillLive.Enabled, stillLive.ArchivedAt)
	}
}

func TestTaskArchiveScheduleFailureRemainsArchived(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	remover := &recordingScheduleRemover{err: errors.New("schedule removal not proven")}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: remover.RemoveSchedule,
		Now:            func() time.Time { return fixture.now },
	})
	result, err := service.Archive(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("Archive with unproven schedule removal: %v", err)
	}
	if !result.Archived || !result.Unlinked || result.ScheduleRemoved || result.ProviderBytesDeleted {
		t.Fatalf("result=%+v, want archived/unlinked with schedule_removed=false", result)
	}

	var archived model.Task
	if err := fixture.db.First(&archived, fixture.task.ID).Error; err != nil {
		t.Fatalf("load archived task: %v", err)
	}
	if archived.Enabled || archived.ArchivedAt == nil {
		t.Fatalf("unproven schedule removal must not roll the task back, enabled=%v archived_at=%v", archived.Enabled, archived.ArchivedAt)
	}
	if len(remover.calls) != 1 {
		t.Fatalf("schedule removals=%v, want 1 attempted proof", remover.calls)
	}
}

func TestTaskArchiveIdempotentRetryRemovesSchedule(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	first := &recordingScheduleRemover{err: errors.New("first schedule removal not proven")}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: first.RemoveSchedule,
		Now:            func() time.Time { return fixture.now },
	})
	firstResult, err := service.Archive(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("first Archive: %v", err)
	}
	if firstResult.ScheduleRemoved {
		t.Fatal("first result unexpectedly proved schedule removal")
	}

	var afterFirst model.Task
	if err := fixture.db.First(&afterFirst, fixture.task.ID).Error; err != nil {
		t.Fatalf("load after first archive: %v", err)
	}

	retryNow := fixture.now.Add(2 * time.Minute)
	retry := &recordingScheduleRemover{}
	retryService := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: retry.RemoveSchedule,
		Now:            func() time.Time { return retryNow },
	})
	retryResult, err := retryService.Archive(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("retry Archive: %v", err)
	}
	if !retryResult.Archived || retryResult.Unlinked || !retryResult.ScheduleRemoved {
		t.Fatalf("retry result=%+v, want already-archived with schedule_removed", retryResult)
	}

	var afterRetry model.Task
	if err := fixture.db.First(&afterRetry, fixture.task.ID).Error; err != nil {
		t.Fatalf("load after retry: %v", err)
	}
	if afterRetry.ArchivedAt == nil || !afterRetry.ArchivedAt.Equal(*afterFirst.ArchivedAt) {
		t.Fatalf("retry changed archived_at from %v to %v", afterFirst.ArchivedAt, afterRetry.ArchivedAt)
	}
}

func TestTaskArchiveNotFound(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	remover := &recordingScheduleRemover{}
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: remover.RemoveSchedule,
		Now:            func() time.Time { return fixture.now },
	})
	_, err := service.Archive(context.Background(), fixture.task.ID+99)
	if !errors.Is(err, ErrTaskArchiveNotFound) {
		t.Fatalf("Archive error=%v, want ErrTaskArchiveNotFound", err)
	}
	if len(remover.calls) != 0 {
		t.Fatalf("missing task removed schedule: %v", remover.calls)
	}
}

func TestTaskArchiveConservativeHardDeleteKeepsReferencedRow(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	service := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	if _, err := service.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	deleted, err := service.TryHardDelete(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("TryHardDelete: %v", err)
	}
	if deleted {
		t.Fatal("hard-delete reaper deleted a referenced archived task")
	}
	var stillThere model.Task
	if err := fixture.db.First(&stillThere, fixture.task.ID).Error; err != nil {
		t.Fatalf("archived referenced task was removed: %v", err)
	}

	empty := model.Task{
		Name:         "empty-archived",
		NodeID:       fixture.node.ID,
		ExecutorType: "command",
		Command:      "true",
		Status:       "pending",
		Enabled:      false,
		ArchivedAt:   &fixture.now,
	}
	if err := fixture.db.Create(&empty).Error; err != nil {
		t.Fatalf("create empty archived task: %v", err)
	}
	deleted, err = service.TryHardDelete(context.Background(), empty.ID)
	if err != nil {
		t.Fatalf("TryHardDelete empty archived: %v", err)
	}
	if !deleted {
		t.Fatal("empty archived task with no known owners should be reaped")
	}
}

func TestTaskArchiveDoesNotResurrectWhenUpdateRaces(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	repo := &archiveRaceTaskRepository{
		inner: gormrepo.NewTaskRepository(fixture.db),
		afterFind: func() {
			close(started)
			<-release
		},
	}
	api := NewTaskApiService(
		repo,
		gormrepo.NewNodeRepository(fixture.db),
		gormrepo.NewPolicyRepository(fixture.db),
		nil,
	)
	archive := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := api.UpdateTask(context.Background(), fixture.task.ID, CreateTaskInput{
			Name: "resurrected-after-archive", NodeID: fixture.node.ID, ExecutorType: "restic",
			RsyncSource: "/data/src", RsyncTarget: "/backup/dst", CronSpec: "*/10 * * * *",
		})
		errCh <- err
	}()
	<-started
	if _, err := archive.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive during UpdateTask: %v", err)
	}
	close(release)
	if err := <-errCh; !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("racing UpdateTask error=%v, want ErrTaskArchived", err)
	}

	var reloaded model.Task
	if err := fixture.db.First(&reloaded, fixture.task.ID).Error; err != nil {
		t.Fatalf("reload raced task: %v", err)
	}
	if reloaded.ArchivedAt == nil || reloaded.Name == "resurrected-after-archive" {
		t.Fatalf("archived task was resurrected: name=%q archived_at=%v", reloaded.Name, reloaded.ArchivedAt)
	}
}

type recordingTaskScheduleRunner struct {
	synced  []model.Task
	removed []uint
}

func (runner *recordingTaskScheduleRunner) TriggerManual(uint) (uint, error) { return 0, nil }
func (runner *recordingTaskScheduleRunner) SyncSchedule(task model.Task) error {
	runner.synced = append(runner.synced, task)
	return nil
}
func (runner *recordingTaskScheduleRunner) RemoveSchedule(taskID uint) {
	runner.removed = append(runner.removed, taskID)
}

func TestTaskSyncScheduleDoesNotRecreateCronAfterArchive(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	sched := scheduler.NewCronScheduler()
	manager := NewManager(fixture.db, stubExecutorFactory{}, nil, sched, nil, nil, 8, 90)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
	})
	if err := manager.SyncSchedule(fixture.task); err != nil {
		t.Fatalf("initial SyncSchedule: %v", err)
	}
	if !sched.HasTask(fixture.task.ID) {
		t.Fatal("cron missing after initial SyncSchedule")
	}
	if _, err := manager.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if sched.HasTask(fixture.task.ID) {
		t.Fatal("cron still present after Archive")
	}
	stale := fixture.task
	stale.ArchivedAt = nil
	stale.Enabled = true
	if err := manager.SyncSchedule(stale); err != nil {
		t.Fatalf("stale SyncSchedule: %v", err)
	}
	if sched.HasTask(fixture.task.ID) {
		t.Fatal("stale SyncSchedule recreated cron after archive")
	}
}

func TestTaskUpdateDoesNotRescheduleAfterArchiveWins(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &recordingTaskScheduleRunner{}
	repo := &archiveRaceTaskRepository{
		inner: gormrepo.NewTaskRepository(fixture.db),
		afterUpdate: func() {
			close(started)
			<-release
		},
	}
	api := NewTaskApiService(
		repo,
		gormrepo.NewNodeRepository(fixture.db),
		gormrepo.NewPolicyRepository(fixture.db),
		runner,
	)
	archive := NewArchiveService(ArchiveDependencies{
		DB: fixture.db,
		RemoveSchedule: func(id uint) error {
			runner.RemoveSchedule(id)
			return nil
		},
		Now: func() time.Time { return fixture.now },
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := api.UpdateTask(context.Background(), fixture.task.ID, CreateTaskInput{
			Name: "updated-after-archive", NodeID: fixture.node.ID, ExecutorType: "restic",
			RsyncSource: "/data/src", RsyncTarget: "/backup/dst", CronSpec: "*/10 * * * *",
		})
		errCh <- err
	}()
	<-started
	if _, err := archive.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive during UpdateTask persist: %v", err)
	}
	close(release)
	if err := <-errCh; !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("UpdateTask after archive persist error=%v, want ErrTaskArchived", err)
	}
	if len(runner.synced) != 0 {
		t.Fatalf("SyncSchedule after archive=%+v, want none", runner.synced)
	}
}

func TestTaskArchiveDoesNotCreateRestoreRunWhenTriggerRaces(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	manager := NewManager(fixture.db, stubExecutorFactory{}, nil, nil, nil, nil, 8, 90)
	manager.afterTriggerRestoreLoad = func() {
		close(started)
		<-release
	}
	for range cap(manager.semaphore) {
		manager.semaphore <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(manager.semaphore) {
			select {
			case <-manager.semaphore:
			default:
			}
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
	})

	archive := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	errCh := make(chan error, 1)
	var runID uint
	go func() {
		id, err := manager.TriggerRestore(fixture.task.ID, "/data/src")
		runID = id
		errCh <- err
	}()
	<-started
	if _, err := archive.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive during TriggerRestore: %v", err)
	}
	close(release)
	if err := <-errCh; !errors.Is(err, ErrTaskArchived) || runID != 0 {
		t.Fatalf("racing TriggerRestore runID=%d error=%v, want ErrTaskArchived", runID, err)
	}

	var restoreRuns int64
	if err := fixture.db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", fixture.task.ID, "restore").Count(&restoreRuns).Error; err != nil {
		t.Fatalf("count restore runs: %v", err)
	}
	if restoreRuns != 0 {
		t.Fatalf("archived task received %d restore run(s)", restoreRuns)
	}
	var reloaded model.Task
	if err := fixture.db.First(&reloaded, fixture.task.ID).Error; err != nil {
		t.Fatalf("reload raced restore task: %v", err)
	}
	if reloaded.ArchivedAt == nil {
		t.Fatal("racing TriggerRestore left task unarchived")
	}
}

func TestTaskApiServiceUpdateRejectsArchivedTask(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	archive := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	if _, err := archive.Archive(context.Background(), fixture.task.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	api := NewTaskApiService(
		gormrepo.NewTaskRepository(fixture.db),
		gormrepo.NewNodeRepository(fixture.db),
		gormrepo.NewPolicyRepository(fixture.db),
		nil,
	)
	_, err := api.UpdateTask(context.Background(), fixture.task.ID, CreateTaskInput{
		Name: "mutated-archived-task", NodeID: fixture.node.ID, ExecutorType: "restic",
		RsyncSource: "/data/src", RsyncTarget: "/backup/dst", CronSpec: "*/10 * * * *",
	})
	if !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("UpdateTask archived error=%v, want ErrTaskArchived", err)
	}
	var reloaded model.Task
	if err := fixture.db.First(&reloaded, fixture.task.ID).Error; err != nil {
		t.Fatalf("reload archived task: %v", err)
	}
	if reloaded.Name != "archive-task" || reloaded.CronSpec != "*/5 * * * *" || reloaded.ArchivedAt == nil {
		t.Fatalf("archived UpdateTask mutated history: name=%q cron=%q archived_at=%v", reloaded.Name, reloaded.CronSpec, reloaded.ArchivedAt)
	}
}

func TestTaskArchiveAPIServiceDelegates(t *testing.T) {
	fixture := newTaskArchiveFixture(t)
	archive := NewArchiveService(ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return fixture.now },
	})
	api := NewTaskApiService(nil, nil, nil, nil).WithArchiveService(archive)
	result, err := api.ArchiveTask(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("ArchiveTask: %v", err)
	}
	if !result.Archived || result.ProviderBytesDeleted {
		t.Fatalf("delegated result=%+v", result)
	}
}

type recordingArchiveAuditor struct {
	writes int
	last   backupasset.AuditEventInput
}

func (auditor *recordingArchiveAuditor) WriteTx(_ context.Context, _ *gorm.DB, event backupasset.AuditEventInput) error {
	auditor.writes++
	auditor.last = event
	return nil
}

type recordingScheduleRemover struct {
	calls  []uint
	err    error
	before func(uint)
}

func (r *recordingScheduleRemover) RemoveSchedule(taskID uint) error {
	if r.before != nil {
		r.before(taskID)
	}
	r.calls = append(r.calls, taskID)
	return r.err
}

type recordingProviderMutator struct {
	calls atomic.Int64
}

func (m *recordingProviderMutator) DeleteBytes() {
	m.calls.Add(1)
}

type archiveCommitFailPool struct {
	*sql.DB
	fail atomic.Bool
}

func (pool *archiveCommitFailPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := pool.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &archiveCommitFailTx{Tx: tx, pool: pool}, nil
}

type archiveCommitFailTx struct {
	*sql.Tx
	pool *archiveCommitFailPool
}

func (tx *archiveCommitFailTx) Commit() error {
	inner := tx.Tx
	if tx.pool.fail.Load() {
		if err := inner.Rollback(); err != nil {
			return err
		}
		return errors.New("forced archive commit failure")
	}
	return inner.Commit()
}

type taskArchiveFixture struct {
	db              *gorm.DB
	dsn             string
	now             time.Time
	node            model.Node
	task            model.Task
	run             model.TaskRun
	log             model.TaskLog
	repository      model.BackupRepository
	link            model.TaskRepositoryLink
	point           model.RecoveryPoint
	audit           model.CredentialAuditEvent
	assetAudit      model.BackupAssetAuditEvent
	executorConfig  string
	legacyLocator   string
	providerLocator string
	rollbackLocator string
	provider        *recordingProviderMutator
}

func newTaskArchiveFixture(t *testing.T) *taskArchiveFixture {
	return newTaskArchiveFixtureWithPublicationMode(t, backupasset.PublicationNativeSnapshot)
}

func newTaskArchiveFixtureWithPublicationMode(t *testing.T, publicationMode backupasset.TaskPublicationMode) *taskArchiveFixture {
	t.Helper()
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	dsn := filepath.Join(t.TempDir(), "task-archive.db") + "?_loc=UTC&_foreign_keys=on&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open archive fixture db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("archive fixture sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.Node{},
		&model.Task{},
		&model.TaskRun{},
		&model.TaskLog{},
		&model.TaskTrafficSample{},
		&model.BackupRepository{},
		&model.TaskRepositoryLink{},
		&model.BackupRetentionPolicy{},
		&model.RecoveryPoint{},
		&model.CredentialAuditEvent{},
		&model.CredentialAccessGrant{},
		&model.BackupAssetAuditEvent{},
		&model.Alert{},
		&model.RestoreDrillEvidence{},
		&model.SnapshotDiffHistory{},
		&model.SnapshotFileIndex{},
	); err != nil {
		t.Fatalf("migrate archive fixture: %v", err)
	}

	now := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	fixture := &taskArchiveFixture{
		db:              db,
		dsn:             dsn,
		now:             now,
		executorConfig:  `{"repository_password":"FAKE_TASK_ARCHIVE_PASSWORD_FOR_TEST_ONLY"}`,
		legacyLocator:   "sftp:archive-user@example.invalid:/protected/legacy",
		providerLocator: `{"version":1,"provider":"restic","full_snapshot_id":"abc123def456"}`,
		rollbackLocator: `{"version":1,"provider":"restic","rollback":"protected"}`,
		provider:        &recordingProviderMutator{},
	}
	fixture.node = model.Node{Name: "archive-node", Host: "10.0.14.9", Username: "root", AuthType: "key", BackupDir: "archive-node"}
	if err := db.Create(&fixture.node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	fixture.task = model.Task{
		Name:           "archive-task",
		NodeID:         fixture.node.ID,
		Command:        "restic backup /data",
		RsyncSource:    "/data/src",
		RsyncTarget:    "/backup/dst",
		ExecutorType:   "restic",
		ExecutorConfig: fixture.executorConfig,
		CronSpec:       "*/5 * * * *",
		Status:         "pending",
		Enabled:        true,
	}
	if err := db.Create(&fixture.task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	fixture.run = model.TaskRun{TaskID: fixture.task.ID, TriggerType: "manual", Status: "success"}
	if err := db.Create(&fixture.run).Error; err != nil {
		t.Fatalf("create task run: %v", err)
	}
	fixture.log = model.TaskLog{TaskID: fixture.task.ID, TaskRunID: &fixture.run.ID, Level: "info", Message: "archive history"}
	if err := db.Create(&fixture.log).Error; err != nil {
		t.Fatalf("create task log: %v", err)
	}
	repositoryID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fixture.repository = model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "archive-repo",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	}
	if err := db.Create(&fixture.repository).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	taskID := fixture.task.ID
	fixture.link = model.TaskRepositoryLink{
		ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TaskID: &taskID, RepositoryID: repositoryID,
		TaskNameSnapshot: "archive-task", NodeIDSnapshot: fixture.node.ID, NodeNameSnapshot: fixture.node.Name,
		PublicationMode: string(publicationMode), EncryptedLegacyLocator: fixture.legacyLocator,
		LinkedAt: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := db.Create(&fixture.link).Error; err != nil {
		t.Fatalf("create task repository link: %v", err)
	}
	captured := now.Add(-30 * time.Minute)
	fixture.point = model.RecoveryPoint{
		ID: "cccccccccccccccccccccccccccccccc", RepositoryID: repositoryID, ProducingTaskID: &taskID, ProducingTaskRunID: &fixture.run.ID,
		ProducingTaskNameSnapshot: "archive-task", ProducingNodeIDSnapshot: fixture.node.ID, ProducingNodeNameSnapshot: fixture.node.Name,
		LineageJSON: `{"version":1}`, EncryptedProviderLocator: fixture.providerLocator, EncryptedRollbackLocator: fixture.rollbackLocator,
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		CapturedAt: &captured, CommittedAt: &captured, PointRevision: 1, CapabilityRevision: 1, CapabilitiesJSON: `{}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone),
	}
	if err := db.Create(&fixture.point).Error; err != nil {
		t.Fatalf("create recovery point: %v", err)
	}
	fixture.audit = model.CredentialAuditEvent{
		Username: "admin", Action: "task.manual_trigger", Purpose: "task_run", CredentialKind: "ssh_key",
		CredentialSource: "node", TaskID: &taskID, TaskRunID: &fixture.run.ID, Outcome: "success", Metadata: "{}",
	}
	if err := db.Create(&fixture.audit).Error; err != nil {
		t.Fatalf("create credential audit: %v", err)
	}
	fixture.assetAudit = model.BackupAssetAuditEvent{
		SegmentNo: 1, SegmentSequence: 1, ActorUsername: "admin", ActorRole: "admin",
		Action: "repository.read", Outcome: "success", RepositoryID: repositoryID, RecoveryPointID: fixture.point.ID,
		TaskID: &taskID, TaskRunID: &fixture.run.ID, FieldsJSON: "{}", PrevHash: "p", EntryHash: "e",
	}
	if err := db.Create(&fixture.assetAudit).Error; err != nil {
		t.Fatalf("create backup-asset audit: %v", err)
	}
	return fixture
}

func assertTaskArchiveHistoryPreserved(t *testing.T, fixture *taskArchiveFixture) {
	t.Helper()
	var repository model.BackupRepository
	if err := fixture.db.First(&repository, "id = ?", fixture.repository.ID).Error; err != nil {
		t.Fatalf("repository missing after archive: %v", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", fixture.point.ID).Error; err != nil {
		t.Fatalf("recovery point missing after archive: %v", err)
	}
	if point.ProducingTaskID == nil || *point.ProducingTaskID != fixture.task.ID || point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != fixture.run.ID {
		t.Fatalf("recovery point producing refs changed: %+v", point)
	}
	var run model.TaskRun
	if err := fixture.db.First(&run, fixture.run.ID).Error; err != nil {
		t.Fatalf("task run missing after archive: %v", err)
	}
	var log model.TaskLog
	if err := fixture.db.First(&log, fixture.log.ID).Error; err != nil {
		t.Fatalf("task log missing after archive: %v", err)
	}
	var audit model.CredentialAuditEvent
	if err := fixture.db.First(&audit, fixture.audit.ID).Error; err != nil {
		t.Fatalf("credential audit missing after archive: %v", err)
	}
	if audit.TaskID == nil || *audit.TaskID != fixture.task.ID {
		t.Fatalf("credential audit task_id changed: %+v", audit)
	}
	var assetAudit model.BackupAssetAuditEvent
	if err := fixture.db.First(&assetAudit, fixture.assetAudit.ID).Error; err != nil {
		t.Fatalf("backup-asset audit missing after archive: %v", err)
	}
	raw, _ := json.Marshal(point)
	if string(raw) == "" {
		t.Fatal("recovery point marshaled empty")
	}
}

type archiveRaceTaskRepository struct {
	inner       repository.TaskRepository
	afterFind   func()
	afterUpdate func()
}

func (r *archiveRaceTaskRepository) FindByID(ctx context.Context, id uint) (*model.Task, error) {
	task, err := r.inner.FindByID(ctx, id)
	if err == nil && r.afterFind != nil {
		r.afterFind()
	}
	return task, err
}

func (r *archiveRaceTaskRepository) FindByIDFields(ctx context.Context, id uint, fields ...string) (*model.Task, error) {
	return r.inner.FindByIDFields(ctx, id, fields...)
}

func (r *archiveRaceTaskRepository) List(ctx context.Context) ([]model.Task, error) {
	return r.inner.List(ctx)
}

func (r *archiveRaceTaskRepository) Create(ctx context.Context, task *model.Task) error {
	return r.inner.Create(ctx, task)
}

func (r *archiveRaceTaskRepository) Update(ctx context.Context, task *model.Task) error {
	return r.inner.Update(ctx, task)
}

func (r *archiveRaceTaskRepository) Delete(ctx context.Context, id uint) error {
	return r.inner.Delete(ctx, id)
}

func (r *archiveRaceTaskRepository) ExistsByID(ctx context.Context, id uint) (bool, error) {
	return r.inner.ExistsByID(ctx, id)
}

func (r *archiveRaceTaskRepository) ExistsLiveByID(ctx context.Context, id uint) (bool, error) {
	return r.inner.ExistsLiveByID(ctx, id)
}

func (r *archiveRaceTaskRepository) LockIDsForUpdate(ctx context.Context, ids []uint) error {
	return r.inner.LockIDsForUpdate(ctx, ids)
}

func (r *archiveRaceTaskRepository) RunInTransaction(ctx context.Context, fn func(context.Context, repository.TaskRepository) error) error {
	err := r.inner.RunInTransaction(ctx, fn)
	if err == nil && r.afterUpdate != nil {
		r.afterUpdate()
	}
	return err
}

func (r *archiveRaceTaskRepository) CountByID(ctx context.Context, id uint) (int64, error) {
	return r.inner.CountByID(ctx, id)
}

func (r *archiveRaceTaskRepository) FindByIDsFields(ctx context.Context, ids []uint, fields ...string) ([]model.Task, error) {
	return r.inner.FindByIDsFields(ctx, ids, fields...)
}
