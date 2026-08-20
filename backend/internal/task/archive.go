package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTaskArchiveNotFound      = errors.New("task not found")
	ErrTaskArchiveHasDependents = errors.New("task has live dependents")
	ErrTaskArchived             = errors.New("task archived")
)

// ArchiveResult is the safe HTTP/service envelope for Task archive/unlink.
// ProviderBytesDeleted is always false: archive never deletes Provider bytes.
type ArchiveResult struct {
	Archived             bool `json:"archived"`
	Unlinked             bool `json:"unlinked"`
	ScheduleRemoved      bool `json:"schedule_removed"`
	ProviderBytesDeleted bool `json:"provider_bytes_deleted"`
}

// ArchiveDependencies constructs ArchiveService. There is no Provider port:
// archive/unlink is a control-plane metadata operation only.
type ArchiveDependencies struct {
	DB             *gorm.DB
	RemoveSchedule func(taskID uint) error
	Now            func() time.Time
	WriteTx        func(context.Context, *gorm.DB, backupasset.AuditEventInput) error
}

// ArchiveService owns the Task archive transaction and post-commit schedule removal.
type ArchiveService struct {
	db             *gorm.DB
	removeSchedule func(taskID uint) error
	now            func() time.Time
	writeTx        func(context.Context, *gorm.DB, backupasset.AuditEventInput) error
}

func NewArchiveService(deps ArchiveDependencies) *ArchiveService {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &ArchiveService{db: deps.DB, removeSchedule: deps.RemoveSchedule, now: now, writeTx: deps.WriteTx}
}

func (s *ArchiveService) Archive(ctx context.Context, taskID uint) (ArchiveResult, error) {
	if s == nil || s.db == nil {
		return ArchiveResult{}, fmt.Errorf("任务归档服务未初始化")
	}
	now := s.now()
	if now.Location() != time.UTC {
		now = now.UTC()
	}
	var unlinked bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Write-lock the target and current dependents in one statement so
		// SQLite does not upgrade a shared SELECT into a reserved lock while
		// create/update already holds the parent row.
		if err := tx.Model(&model.Task{}).
			Where("id = ? OR depends_on_task_id = ?", taskID, taskID).
			UpdateColumn("name", gorm.Expr("name")).Error; err != nil {
			return err
		}
		var taskEntity model.Task
		lookup := tx.Where("id = ?", taskID).Limit(1).Find(&taskEntity)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 0 {
			return ErrTaskArchiveNotFound
		}
		var dependents []model.Task
		if err := tx.Where("depends_on_task_id = ?", taskID).Find(&dependents).Error; err != nil {
			return err
		}
		lockIDs := []uint{taskID}
		for i := range dependents {
			lockIDs = append(lockIDs, dependents[i].ID)
		}
		if err := gormrepo.LockTaskIDsForUpdate(tx, lockIDs); err != nil {
			return err
		}
		if err := tx.Where("depends_on_task_id = ?", taskID).Find(&dependents).Error; err != nil {
			return err
		}
		for i := range dependents {
			if dependents[i].ArchivedAt == nil {
				return ErrTaskArchiveHasDependents
			}
		}

		var links []model.TaskRepositoryLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND unlinked_at IS NULL", taskID).
			Find(&links).Error; err != nil {
			return err
		}

		updates := map[string]any{"enabled": false}
		if taskEntity.ArchivedAt == nil {
			updates["archived_at"] = now
		}
		if err := tx.Model(&model.Task{}).Where("id = ?", taskID).Updates(updates).Error; err != nil {
			return err
		}

		if len(links) == 0 {
			return nil
		}
		linkIDs := make([]string, 0, len(links))
		for i := range links {
			linkIDs = append(linkIDs, links[i].ID)
		}
		if err := tx.Model(&model.TaskRepositoryLink{}).Where("id IN ?", linkIDs).Updates(map[string]any{
			"task_id":     nil,
			"unlinked_at": now,
		}).Error; err != nil {
			return err
		}
		if err := settleTaskLinkRetentionPolicies(ctx, tx, s.writeTx, archiveActorFromContext(ctx), links, now); err != nil {
			return err
		}
		unlinked = true
		return nil
	})
	if err != nil {
		return ArchiveResult{}, err
	}

	scheduleRemoved := true
	if s.removeSchedule != nil {
		if remErr := s.removeSchedule(taskID); remErr != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Err(remErr).Msg("archived task schedule removal was not proven")
			scheduleRemoved = false
		}
	}
	return ArchiveResult{
		Archived:             true,
		Unlinked:             unlinked,
		ScheduleRemoved:      scheduleRemoved,
		ProviderBytesDeleted: false,
	}, nil
}

type archiveActorContextKey struct{}
type archiveCorrelationContextKey struct{}

// WithArchiveActor attaches the authenticated archive actor. Callers without
// an HTTP user keep the documented system actor.
func WithArchiveActor(ctx context.Context, actor backupasset.AuditActor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, archiveActorContextKey{}, actor)
}

// WithArchiveCorrelationID copies an HTTP request ID onto archive mutation
// audits. Empty values are ignored so callers never invent a correlation ID.
func WithArchiveCorrelationID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, archiveCorrelationContextKey{}, requestID)
}

func ArchiveCorrelationID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(archiveCorrelationContextKey{}).(string)
	return strings.TrimSpace(value)
}

func archiveActorFromContext(ctx context.Context) backupasset.AuditActor {
	if ctx != nil {
		if actor, ok := ctx.Value(archiveActorContextKey{}).(backupasset.AuditActor); ok {
			if actor.UserID != 0 || strings.TrimSpace(actor.Username) != "" || strings.TrimSpace(actor.Role) != "" {
				return actor
			}
		}
	}
	return backupasset.AuditActor{Username: "system", Role: "system"}
}

// NewArchiveAuditWriteTx builds a transactional backup-asset audit writer when
// audit storage exists. A nil writer is fail-closed once a policy is deleted.
func NewArchiveAuditWriteTx(db *gorm.DB, now func() time.Time) func(context.Context, *gorm.DB, backupasset.AuditEventInput) error {
	if db == nil || !db.Migrator().HasTable(&model.BackupAssetAuditEvent{}) ||
		!db.Migrator().HasTable(&model.BackupAssetAuditCheckpoint{}) {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	writer, err := backupasset.NewAuditWriter(db, backupasset.NewKeyring(db, now), now, backupasset.AuditConfig{})
	if err != nil {
		return nil
	}
	return writer.WriteTx
}

func settleTaskLinkRetentionPolicies(
	ctx context.Context,
	tx *gorm.DB,
	writeTx func(context.Context, *gorm.DB, backupasset.AuditEventInput) error,
	actor backupasset.AuditActor,
	links []model.TaskRepositoryLink,
	now time.Time,
) error {
	if tx == nil || len(links) == 0 {
		return nil
	}
	if !retentionPolicyTableReady(tx) {
		return nil
	}
	linkIDs := make([]string, 0, len(links))
	repositoryByLink := make(map[string]string, len(links))
	for i := range links {
		linkIDs = append(linkIDs, links[i].ID)
		repositoryByLink[links[i].ID] = links[i].RepositoryID
	}
	var policies []model.BackupRetentionPolicy
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("scope_kind = ? AND scope_id IN ? AND status = ?", backupasset.RetentionPolicyScopeTaskLink, linkIDs, backupasset.RetentionPolicyActive).
		Find(&policies).Error; err != nil {
		if missingRetentionPolicyTable(err) {
			return nil
		}
		return err
	}
	for i := range policies {
		result := tx.Model(&model.BackupRetentionPolicy{}).
			Where("id = ? AND revision = ? AND status = ?", policies[i].ID, policies[i].Revision, backupasset.RetentionPolicyActive).
			Updates(map[string]any{
				"revision":   policies[i].Revision + 1,
				"status":     backupasset.RetentionPolicyDeleted,
				"updated_at": now,
				"deleted_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("task archive retention policy revision conflict")
		}
		if writeTx == nil {
			return fmt.Errorf("task archive retention policy delete audit is unavailable")
		}
		if err := writeTx(ctx, tx, archiveRetentionPolicyDeleteAudit(ctx, actor, repositoryByLink[policies[i].ScopeID], policies[i].ID)); err != nil {
			return err
		}
	}
	return nil
}

// retentionPolicyTableReady is the same-TX equivalent of HasTable. GORM
// Migrator.HasTable opens a second pool connection and deadlocks SQLite
// fixtures that set MaxOpenConns(1) while another writer holds the only conn.
func retentionPolicyTableReady(tx *gorm.DB) bool {
	if tx == nil || tx.Dialector == nil {
		return false
	}
	table := model.BackupRetentionPolicy{}.TableName()
	var name string
	switch tx.Name() {
	case "sqlite":
		err := tx.Raw("SELECT name FROM sqlite_master WHERE type = ? AND name = ?", "table", table).Scan(&name).Error
		return err == nil && strings.TrimSpace(name) != ""
	case "postgres":
		err := tx.Raw("SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename = ?", table).Scan(&name).Error
		return err == nil && strings.TrimSpace(name) != ""
	default:
		return true
	}
}

func missingRetentionPolicyTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, model.BackupRetentionPolicy{}.TableName()) {
		return false
	}
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "doesn't exist")
}

func archiveRetentionPolicyDeleteAudit(ctx context.Context, actor backupasset.AuditActor, repositoryID, policyID string) backupasset.AuditEventInput {
	fields := map[backupasset.AuditField]any{
		backupasset.AuditFieldStage:     "request",
		backupasset.AuditFieldStatus:    "success",
		backupasset.AuditFieldItemCount: int64(1),
	}
	if correlationID := ArchiveCorrelationID(ctx); correlationID != "" {
		fields[backupasset.AuditFieldCorrelationID] = correlationID
	}
	if backupasset.ValidateOpaqueID(policyID) == nil {
		fields[backupasset.AuditFieldPolicyID] = policyID
	}
	return backupasset.AuditEventInput{
		Actor:        actor,
		Action:       backupasset.AuditActionRetentionPolicyDelete,
		Outcome:      backupasset.AuditOutcomeSuccess,
		RepositoryID: repositoryID,
		ItemCount:    1,
		Fields:       fields,
	}
}

// TryHardDelete may delete only an archived Task with no known remaining owners.
// Any query, delete, or unknown-owner uncertainty keeps the row.
func (s *ArchiveService) TryHardDelete(ctx context.Context, taskID uint) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("任务归档服务未初始化")
	}
	var taskEntity model.Task
	if err := s.db.WithContext(ctx).First(&taskEntity, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrTaskArchiveNotFound
		}
		return false, nil
	}
	if taskEntity.ArchivedAt == nil {
		return false, nil
	}

	owners := []struct {
		model any
		query string
	}{
		{&model.Task{}, "depends_on_task_id = ?"},
		{&model.TaskRun{}, "task_id = ?"},
		{&model.TaskLog{}, "task_id = ?"},
		{&model.TaskTrafficSample{}, "task_id = ?"},
		{&model.TaskRepositoryLink{}, "task_id = ?"},
		{&model.CredentialAuditEvent{}, "task_id = ?"},
		{&model.CredentialAccessGrant{}, "task_id = ?"},
		{&model.BackupAssetAuditEvent{}, "task_id = ?"},
		{&model.RecoveryPoint{}, "producing_task_id = ?"},
		{&model.Alert{}, "task_id = ?"},
		{&model.RestoreDrillEvidence{}, "task_id = ?"},
		{&model.SnapshotDiffHistory{}, "task_id = ?"},
		{&model.SnapshotFileIndex{}, "task_id = ?"},
	}
	for _, owner := range owners {
		var count int64
		if err := s.db.WithContext(ctx).Model(owner.model).Where(owner.query, taskID).Count(&count).Error; err != nil {
			return false, nil
		}
		if count > 0 {
			return false, nil
		}
	}
	if err := s.db.WithContext(ctx).Delete(&model.Task{}, taskID).Error; err != nil {
		return false, nil
	}
	return true, nil
}
