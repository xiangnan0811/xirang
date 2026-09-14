package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/config"
	"xirang/backend/internal/cronutil"
	"xirang/backend/internal/model"
	policyPkg "xirang/backend/internal/policy"
	"xirang/backend/internal/repository"
	"xirang/backend/internal/rsyncconfinement"
	"xirang/backend/internal/util"
)

const maxCommandLength = 4096

// ErrTaskScheduleSyncUnavailable indicates that the Task update committed,
// but the process-local schedule could not be synchronized. The durable Task
// row remains authoritative and is repaired by the manager's idempotent
// schedule reconciliation.
var ErrTaskScheduleSyncUnavailable = errors.New("task saved but schedule synchronization unavailable")

// TaskRunner is a subset of Manager methods needed by TaskApiService.
type TaskRunner interface {
	TriggerManual(taskID uint) (uint, error)
	SyncSchedule(task model.Task) error
	RemoveSchedule(taskID uint)
}

// TaskApiService handles task CRUD business logic for HTTP handlers.
// It is separate from the runtime Manager; it handles input validation,
// defaults hydration, DB persistence, and schedule sync.
type TaskApiService struct {
	taskRepo   repository.TaskRepository
	nodeRepo   repository.NodeRepository
	policyRepo repository.PolicyRepository
	runner     TaskRunner
	archive    *ArchiveService
}

// NewTaskApiService creates a new TaskApiService.
func NewTaskApiService(
	taskRepo repository.TaskRepository,
	nodeRepo repository.NodeRepository,
	policyRepo repository.PolicyRepository,
	runner TaskRunner,
) *TaskApiService {
	return &TaskApiService{
		taskRepo:   taskRepo,
		nodeRepo:   nodeRepo,
		policyRepo: policyRepo,
		runner:     runner,
	}
}

// WithArchiveService installs the Task archive/unlink owner used by HTTP delete.
func (s *TaskApiService) WithArchiveService(archive *ArchiveService) *TaskApiService {
	if s != nil {
		s.archive = archive
	}
	return s
}

// ArchiveTask disables and unlinks a Task without Provider side effects.
func (s *TaskApiService) ArchiveTask(ctx context.Context, taskID uint) (ArchiveResult, error) {
	if s == nil || s.archive == nil {
		return ArchiveResult{}, fmt.Errorf("任务归档服务未初始化")
	}
	return s.archive.Archive(ctx, taskID)
}

// CreateTaskInput is the input for creating a task. Creation intentionally
// keeps the historical raw executor_config boundary because imports and
// policy hydration still construct complete configurations.
type CreateTaskInput struct {
	Name            string
	NodeID          uint
	PolicyID        *uint
	DependsOnTaskID *uint
	Command         string
	RsyncSource     string
	RsyncTarget     string
	ExecutorType    string
	ExecutorConfig  string
	CronSpec        string
}

// UpdateTaskInput is the presence-aware edit contract. A nil scalar is
// omitted and retains its durable value. PolicyID/DependsOnTaskID use the
// corresponding *Set bit so a present JSON null can unlink a relationship.
// Executor settings and secrets are separate from the persisted encrypted
// configuration: settings are a closed non-secret patch and secrets are
// write-only values.
type UpdateTaskInput struct {
	ExpectedRevision string

	Name               *string
	NodeID             *uint
	PolicyID           *uint
	PolicyIDSet        bool
	DependsOnTaskID    *uint
	DependsOnTaskIDSet bool
	Command            *string
	RsyncSource        *string
	RsyncTarget        *string
	ExecutorType       *string
	CronSpec           *string

	ExecutorSettings *ExecutorSettingsPatch
	ExecutorSecrets  *ExecutorSecretsPatch
}

// ExecutorSettingsPatch is the closed, non-secret update projection. A Set
// bit distinguishes an omitted field from an explicit zero/empty value.
type ExecutorSettingsPatch struct {
	ExcludePatterns    []string
	ExcludePatternsSet bool

	RepositoryVersion    *int
	RepositoryVersionSet bool

	BandwidthLimit    string
	BandwidthLimitSet bool
	Transfers         int
	TransfersSet      bool
}

// ExecutorSecretsPatch is deliberately write-only. RepositoryPassword is
// never returned by a response projection. A blank replacement retains an
// existing configured value for compatibility with the editor contract.
type ExecutorSecretsPatch struct {
	RepositoryPassword    string
	RepositoryPasswordSet bool
}

// ResticExecutorSettingsResponse and RcloneExecutorSettingsResponse are the
// only executor configuration fields that cross the task read boundary.
type ResticExecutorSettingsResponse struct {
	ExcludePatterns   []string `json:"exclude_patterns"`
	RepositoryVersion *int     `json:"repository_version"`
}

type RcloneExecutorSettingsResponse struct {
	BandwidthLimit string `json:"bandwidth_limit"`
	Transfers      int    `json:"transfers"`
}

type ExecutorSecretsConfiguredResponse struct {
	RepositoryPassword bool `json:"repository_password"`
}

// ErrTaskRevisionConflict identifies a stale task edit.
var ErrTaskRevisionConflict = repository.ErrTaskRevisionConflict

// TaskRevision returns the exact decimal UnixNano token used by task edit
// compare-and-set operations.
func TaskRevision(task model.Task) string {
	return strconv.FormatInt(task.UpdatedAt.UnixNano(), 10)
}

// ParseTaskRevision parses only the canonical decimal representation emitted
// by TaskRevision. This prevents alternate spellings from becoming aliases
// for the same optimistic-concurrency token.
func ParseTaskRevision(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("expected_revision is required")
	}
	if strings.TrimSpace(raw) != raw {
		return time.Time{}, fmt.Errorf("expected_revision must be an exact decimal UnixNano value")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strconv.FormatInt(value, 10) != raw {
		return time.Time{}, fmt.Errorf("expected_revision must be an exact decimal UnixNano value")
	}
	return time.Unix(0, value).UTC(), nil
}

// BulkTriggerResult is the result of triggering a single task in a batch.
type BulkTriggerResult struct {
	TaskID uint   `json:"task_id"`
	RunID  uint   `json:"run_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// validationError is a domain-level validation error that should result in a 400 response.
type validationError struct {
	message string
}

func (e *validationError) Error() string {
	return e.message
}

// IsTaskValidationError checks if an error is a domain validation error (400-worthy).
func IsTaskValidationError(err error) bool {
	_, ok := err.(*validationError)
	return ok
}

func newValidationError(message string) error {
	return &validationError{message: message}
}

// ---------------------------------------------------------------------------
// CreateTask
// ---------------------------------------------------------------------------

// CreateTask creates a new task from the given input. It handles defaults
func (s *TaskApiService) CreateTask(ctx context.Context, input CreateTaskInput) (model.Task, error) {
	SanitizeCreateTaskInput(&input)

	HydrateTaskDefaultsFromPolicy(ctx, s.policyRepo, s.nodeRepo, &input)
	InferTaskExecutor(&input, "")
	TrimTaskInput(&input)
	EnsureNodeTargetPrefix(ctx, s.nodeRepo, &input)
	AutoGenerateTarget(ctx, s.nodeRepo, &input)

	if err := ValidateTaskInput(input); err != nil {
		return model.Task{}, err
	}

	taskEntity := model.Task{
		Name:            input.Name,
		NodeID:          input.NodeID,
		PolicyID:        input.PolicyID,
		DependsOnTaskID: input.DependsOnTaskID,
		Command:         input.Command,
		RsyncSource:     input.RsyncSource,
		RsyncTarget:     input.RsyncTarget,
		ExecutorType:    input.ExecutorType,
		ExecutorConfig:  input.ExecutorConfig,
		CronSpec:        input.CronSpec,
		Status:          string(StatusPending),
	}
	if err := validateTaskNodeAndPolicy(ctx, s.nodeRepo, s.policyRepo, input); err != nil {
		return model.Task{}, err
	}
	persist := func(ctx context.Context, repo repository.TaskRepository) error {
		if err := s.validateTargetOwnership(ctx, repo, input, 0); err != nil {
			return err
		}
		if input.DependsOnTaskID != nil {
			if err := repo.LockIDsForUpdate(ctx, []uint{*input.DependsOnTaskID}); err != nil {
				return err
			}
		}
		if err := validateTaskDependencyRefs(ctx, repo, input, 0); err != nil {
			return err
		}
		return repo.Create(ctx, &taskEntity)
	}
	if err := s.taskRepo.RunInTransaction(ctx, persist); err != nil {
		return model.Task{}, err
	}
	// Return the row as stored by the database. Some backends normalize
	// timestamp precision on write; the response revision must match that
	// durable UpdatedAt value exactly for the next compare-and-set edit.
	persisted, err := s.taskRepo.FindByID(ctx, taskEntity.ID)
	if err != nil {
		return model.Task{}, apperr.WrapDBError(err)
	}
	taskEntity = *persisted
	if s.runner != nil {
		if err := s.runner.SyncSchedule(taskEntity); err != nil {
			s.runner.RemoveSchedule(taskEntity.ID)
			if rollbackErr := s.taskRepo.Delete(ctx, taskEntity.ID); rollbackErr != nil {
				return model.Task{}, fmt.Errorf("任务调度同步失败且补偿删除失败: %w", rollbackErr)
			}
			return model.Task{}, newValidationError("任务调度失败，请检查 Cron 表达式是否正确")
		}
		// SyncSchedule may initialize the durable cron cursor, which advances
		// UpdatedAt. Return that post-sync row so the revision cannot be stale
		// before the client makes its first edit.
		persisted, err = s.taskRepo.FindByID(ctx, taskEntity.ID)
		if err != nil {
			return model.Task{}, apperr.WrapDBError(err)
		}
		taskEntity = *persisted
	}
	return taskEntity, nil
}

// ---------------------------------------------------------------------------
// UpdateTask
// ---------------------------------------------------------------------------

// UpdateTask updates an existing task using the presence-aware edit contract.
// It validates and merges against the transaction-locked fresh row, then
// compares the exact durable UpdatedAt UnixNano revision before writing. The
// committed database row remains authoritative if schedule sync fails.
func (s *TaskApiService) UpdateTask(ctx context.Context, id uint, input UpdateTaskInput) (model.Task, error) {
	expected, err := ParseTaskRevision(input.ExpectedRevision)
	if err != nil {
		return model.Task{}, newValidationError(err.Error())
	}
	if s == nil || s.taskRepo == nil {
		return model.Task{}, fmt.Errorf("任务服务未初始化")
	}
	normalizeUpdateTaskInput(&input)

	// Read only the reference IDs before opening the write transaction. The
	// transaction-scoped repository then locks policy -> task -> node in the
	// same order used by config import and node migration, and the fresh row
	// below remains authoritative for the revision and merge.
	seed, err := s.taskRepo.FindByID(ctx, id)
	if err != nil {
		return model.Task{}, apperr.WrapDBError(err)
	}
	seedNodeID := seed.NodeID
	if input.NodeID != nil {
		seedNodeID = *input.NodeID
	}
	seedPolicyID := seed.PolicyID
	if input.PolicyIDSet || input.PolicyID != nil {
		seedPolicyID = input.PolicyID
	}

	var updated model.Task
	err = s.taskRepo.RunInTransaction(ctx, func(ctx context.Context, txRepo repository.TaskRepository) error {
		if err := txRepo.LockTaskUpdateReferences(ctx, id, seedNodeID, seedPolicyID); err != nil {
			switch {
			case errors.Is(err, repository.ErrTaskNodeNotFound):
				return newValidationError("所选节点不存在，请重新选择")
			case errors.Is(err, repository.ErrTaskPolicyNotFound):
				return newValidationError("所选策略不存在，请重新选择")
			default:
				return err
			}
		}
		fresh, err := txRepo.FindByID(ctx, id)
		if err != nil {
			return apperr.WrapDBError(err)
		}
		if fresh.ArchivedAt != nil {
			return ErrTaskArchived
		}
		if fresh.UpdatedAt.UnixNano() != expected.UnixNano() {
			return ErrTaskRevisionConflict
		}

		previousExecutorType := strings.TrimSpace(strings.ToLower(fresh.ExecutorType))
		candidate := *fresh
		if input.Name != nil {
			candidate.Name = *input.Name
		}
		if input.NodeID != nil {
			candidate.NodeID = *input.NodeID
		}
		if input.PolicyIDSet || input.PolicyID != nil {
			candidate.PolicyID = input.PolicyID
		}
		if input.DependsOnTaskIDSet || input.DependsOnTaskID != nil {
			candidate.DependsOnTaskID = input.DependsOnTaskID
		}
		if input.Command != nil {
			candidate.Command = *input.Command
		}
		if input.RsyncSource != nil {
			candidate.RsyncSource = *input.RsyncSource
		}
		if input.RsyncTarget != nil {
			candidate.RsyncTarget = *input.RsyncTarget
		}
		if input.ExecutorType != nil {
			candidate.ExecutorType = *input.ExecutorType
		}
		if input.CronSpec != nil {
			candidate.CronSpec = *input.CronSpec
			// Presence is the provenance boundary: an explicit task edit,
			// including an empty value, opts the row out of policy inheritance.
			candidate.CronOverride = true
		}

		nextExecutorType := strings.TrimSpace(strings.ToLower(candidate.ExecutorType))
		if input.ExecutorType != nil && previousExecutorType != nextExecutorType &&
			input.ExecutorSettings == nil && input.ExecutorSecrets == nil {
			// A type transition without an explicit configuration must not
			// carry provider-specific fields or secrets into the new executor.
			candidate.ExecutorConfig = ""
		} else if nextExecutorType == "restic" || input.ExecutorSettings != nil || input.ExecutorSecrets != nil {
			merged, mergeErr := mergeExecutorConfigPatch(
				previousExecutorType, nextExecutorType, candidate.ExecutorConfig,
				input.ExecutorSettings, input.ExecutorSecrets,
			)
			if mergeErr != nil {
				return mergeErr
			}
			candidate.ExecutorConfig = merged
		}

		candidateInput := CreateTaskInput{
			Name: candidate.Name, NodeID: candidate.NodeID, PolicyID: candidate.PolicyID,
			DependsOnTaskID: candidate.DependsOnTaskID, Command: candidate.Command,
			RsyncSource: candidate.RsyncSource, RsyncTarget: candidate.RsyncTarget,
			ExecutorType: candidate.ExecutorType, ExecutorConfig: candidate.ExecutorConfig,
			CronSpec: candidate.CronSpec,
		}
		if err := ValidateTaskInput(candidateInput); err != nil {
			return err
		}
		if policyPkg.IsCoreLocalTarget(candidateInput.ExecutorType, candidateInput.RsyncTarget) {
			if err := txRepo.LockTargetOwnership(ctx); err != nil {
				return err
			}
		}
		if candidate.DependsOnTaskID != nil {
			if err := txRepo.LockIDsForUpdate(ctx, []uint{*candidate.DependsOnTaskID}); err != nil {
				return err
			}
		}
		if err := s.validateTargetOwnership(ctx, txRepo, candidateInput, id); err != nil {
			return err
		}
		if err := validateTaskDependencyRefs(ctx, txRepo, candidateInput, id); err != nil {
			return err
		}

		cronChanged := strings.TrimSpace(fresh.CronSpec) != strings.TrimSpace(candidate.CronSpec)
		if cronChanged {
			retryCursorMode := model.TaskRunCronCursorModeLegacy
			if strings.EqualFold(strings.TrimSpace(fresh.Status), model.TaskRunStatusRetrying) {
				retryCursorMode, err = txRepo.TaskRetryCronCursorMode(ctx, id)
				if err != nil {
					return fmt.Errorf("load task retry cron cursor provenance: %w", err)
				}
			}
			if strings.EqualFold(strings.TrimSpace(fresh.Status), model.TaskRunStatusRetrying) {
				if retryCursorMode == model.TaskRunCronCursorModeRegularV1 {
					if !fresh.Enabled || strings.TrimSpace(candidate.CronSpec) == "" {
						candidate.NextRunAt = nil
					} else {
						candidate.NextRunAt = cronutil.Next(candidate.CronSpec)
					}
				}
				// Legacy retrying tasks retain Task.NextRunAt as their
				// historical retry deadline, even when cron is removed.
			} else if !fresh.Enabled || strings.TrimSpace(candidate.CronSpec) == "" {
				// A paused task and an explicitly cleared cron have no active
				// schedule generation. Resume can initialize a new cursor.
				candidate.NextRunAt = nil
			} else {
				// Reset a due deadline atomically with the new generation.
				candidate.NextRunAt = cronutil.Next(candidate.CronSpec)
			}
		}

		if err := txRepo.UpdateWithRevision(ctx, &candidate, expected); err != nil {
			if errors.Is(err, repository.ErrTaskArchived) {
				return ErrTaskArchived
			}
			if errors.Is(err, repository.ErrTaskRevisionConflict) {
				return ErrTaskRevisionConflict
			}
			return apperr.WrapDBError(err)
		}
		persisted, err := txRepo.FindByID(ctx, id)
		if err != nil {
			return apperr.WrapDBError(err)
		}
		updated = *persisted
		return nil
	})
	if err != nil {
		return model.Task{}, err
	}
	if s.runner != nil {
		current, findErr := s.taskRepo.FindByID(ctx, id)
		if findErr != nil {
			return model.Task{}, apperr.WrapDBError(findErr)
		}
		if current.ArchivedAt != nil {
			s.runner.RemoveSchedule(id)
			return *current, ErrTaskArchived
		}
		if err := s.runner.SyncSchedule(*current); err != nil {
			// The Task commit is authoritative. Do not roll back a complete
			// before-image here; another request may have changed runtime
			// status, pause state, diagnostics, or archival state already.
			// Manager startup/periodic reconciliation converges the schedule.
			return model.Task{}, fmt.Errorf("%w: %v", ErrTaskScheduleSyncUnavailable, err)
		}
		// SyncSchedule may initialize or advance the durable cron cursor,
		// which updates UpdatedAt. Return the post-sync row so the response
		// revision remains valid for the next compare-and-set edit.
		current, findErr = s.taskRepo.FindByID(ctx, id)
		if findErr != nil {
			return model.Task{}, apperr.WrapDBError(findErr)
		}
		return *current, nil
	}
	return updated, nil
}

// ---------------------------------------------------------------------------
// TriggerTask
// ---------------------------------------------------------------------------

// TriggerTask manually triggers a task and returns the new run ID.
func (s *TaskApiService) TriggerTask(id uint) (uint, error) {
	if s.runner == nil {
		return 0, fmt.Errorf("任务执行器未初始化")
	}
	return s.runner.TriggerManual(id)
}

// ---------------------------------------------------------------------------
// BulkTrigger
// ---------------------------------------------------------------------------

// BulkTriggerTasks triggers multiple tasks by their IDs. It looks up tasks
// from the DB, reports not-found errors, and triggers each one sequentially.
func (s *TaskApiService) BulkTriggerTasks(ctx context.Context, taskIDs []uint) []BulkTriggerResult {
	results := make([]BulkTriggerResult, 0, len(taskIDs))

	// Batch query to avoid N+1.
	tasks, err := s.taskRepo.FindByIDsFields(ctx, taskIDs, "id", "node_id")
	if err != nil {
		for _, tid := range taskIDs {
			results = append(results, BulkTriggerResult{TaskID: tid, Error: err.Error()})
		}
		return results
	}
	taskMap := make(map[uint]model.Task, len(tasks))
	for _, t := range tasks {
		taskMap[t.ID] = t
	}

	for _, tid := range taskIDs {
		_, found := taskMap[tid]
		if !found {
			results = append(results, BulkTriggerResult{TaskID: tid, Error: "任务不存在"})
			continue
		}
		runID, err := s.runner.TriggerManual(tid)
		if err != nil {
			results = append(results, BulkTriggerResult{TaskID: tid, Error: err.Error()})
			continue
		}
		results = append(results, BulkTriggerResult{TaskID: tid, RunID: runID})
	}
	return results
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// SanitizeCreateTaskInput sets defaults for empty fields before processing.
func SanitizeCreateTaskInput(input *CreateTaskInput) {
	input.Name = strings.TrimSpace(input.Name)
	input.ExecutorType = strings.TrimSpace(strings.ToLower(input.ExecutorType))
	if input.ExecutorType == "" {
		input.ExecutorType = "rsync"
	}
}

// TrimTaskInput trims whitespace from all string fields.
func TrimTaskInput(req *CreateTaskInput) {
	req.Name = strings.TrimSpace(req.Name)
	req.Command = strings.TrimSpace(req.Command)
	req.RsyncSource = strings.TrimSpace(req.RsyncSource)
	req.RsyncTarget = strings.TrimSpace(req.RsyncTarget)
	req.ExecutorType = strings.TrimSpace(strings.ToLower(req.ExecutorType))
	req.CronSpec = strings.TrimSpace(req.CronSpec)
}

// InferTaskExecutor sets the executor type from the fallback if not explicitly provided.
func InferTaskExecutor(req *CreateTaskInput, fallback string) {
	if strings.TrimSpace(req.ExecutorType) != "" {
		req.ExecutorType = strings.TrimSpace(strings.ToLower(req.ExecutorType))
		return
	}
	if fallback != "" {
		req.ExecutorType = fallback
	} else {
		req.ExecutorType = "rsync"
	}
}

// HydrateTaskDefaultsFromPolicy fills task defaults from the associated policy.
func HydrateTaskDefaultsFromPolicy(ctx context.Context, policyRepo repository.PolicyRepository, nodeRepo repository.NodeRepository, req *CreateTaskInput) {
	if req.PolicyID == nil {
		return
	}
	p, err := policyRepo.FindByID(ctx, *req.PolicyID)
	if err != nil {
		return
	}
	if strings.TrimSpace(req.RsyncSource) == "" {
		req.RsyncSource = p.SourcePath
	}
	if strings.TrimSpace(req.RsyncTarget) == "" && req.NodeID != 0 {
		req.RsyncTarget = policyPkg.PolicyNodeTargetPath(p.TargetPath, p.ID, req.NodeID)
	}
	if strings.TrimSpace(req.CronSpec) == "" {
		req.CronSpec = p.CronSpec
	}
	_ = nodeRepo
}

// EnsureNodeTargetPrefix is retained as an input-normalization boundary. It
// intentionally does not rewrite explicit targets: persisted Task.RsyncTarget
// is historical data and callers must opt into a migration to move it.
func EnsureNodeTargetPrefix(_ context.Context, _ repository.NodeRepository, _ *CreateTaskInput) {}

// AutoGenerateTarget generates an isolated target for tasks without a policy.
func AutoGenerateTarget(_ context.Context, _ repository.NodeRepository, req *CreateTaskInput) {
	if (req.ExecutorType != "rsync" && req.ExecutorType != "restic") || strings.TrimSpace(req.RsyncTarget) != "" {
		return
	}
	if req.PolicyID == nil {
		req.RsyncTarget = policyPkg.ManualNodeTargetPath(config.BackupRoot, req.NodeID)
	}
}

func (s *TaskApiService) validateTargetOwnership(ctx context.Context, taskRepo repository.TaskRepository, req CreateTaskInput, taskID uint) error {
	target := strings.TrimSpace(req.RsyncTarget)
	if !policyPkg.IsCoreLocalTarget(req.ExecutorType, target) {
		return nil
	}
	canonicalTarget, err := policyPkg.CanonicalTargetPath(target)
	if err != nil {
		return newValidationError("备份目标路径无法安全锁定: " + err.Error())
	}
	if err := taskRepo.LockTargetOwnership(ctx); err != nil {
		return fmt.Errorf("锁定任务目标归属失败: %w", err)
	}
	tasks, err := taskRepo.List(ctx)
	if err != nil {
		return fmt.Errorf("查询任务目标归属失败: %w", err)
	}
	claims := make([]policyPkg.TargetOwner, 0, len(tasks))
	for _, task := range tasks {
		if !policyPkg.IsCoreLocalTarget(task.ExecutorType, task.RsyncTarget) {
			continue
		}
		claim := policyPkg.TargetOwner{
			NodeID: task.NodeID,
			TaskID: task.ID,
			Target: task.RsyncTarget,
		}
		if task.PolicyID != nil {
			claim.PolicyID = *task.PolicyID
		}
		claims = append(claims, claim)
	}
	owner := policyPkg.TargetOwner{
		NodeID: req.NodeID,
		TaskID: taskID,
		Target: canonicalTarget,
	}
	if req.PolicyID != nil {
		owner.PolicyID = *req.PolicyID
	}
	if _, err := policyPkg.ValidateTargetOwnership(target, owner, claims); err != nil {
		return newValidationError("备份目标路径与已有任务重叠或无法证明归属: " + err.Error())
	}
	return nil
}

func normalizeUpdateTaskInput(input *UpdateTaskInput) {
	if input == nil {
		return
	}
	trim := func(value *string) {
		if value != nil {
			*value = strings.TrimSpace(*value)
		}
	}
	trim(input.Name)
	trim(input.Command)
	trim(input.RsyncSource)
	trim(input.RsyncTarget)
	trim(input.CronSpec)
	if input.ExecutorType != nil {
		*input.ExecutorType = strings.TrimSpace(strings.ToLower(*input.ExecutorType))
	}
}

// ParseExecutorSettingsPatch decodes the closed non-secret settings object
// accepted by task edits. Unknown fields and null values (except the
// repository_version reset) are rejected rather than silently dropped.
func ParseExecutorSettingsPatch(raw []byte) (ExecutorSettingsPatch, error) {
	object, err := decodeTaskJSONObject(raw)
	if err != nil {
		return ExecutorSettingsPatch{}, newValidationError("executor_settings 必须是合法的 JSON 对象")
	}
	var patch ExecutorSettingsPatch
	for key, value := range object {
		switch key {
		case "exclude_patterns":
			var patterns []string
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &patterns) != nil || patterns == nil {
				return ExecutorSettingsPatch{}, newValidationError("executor_settings.exclude_patterns 必须是字符串数组")
			}
			// Preserve an explicit [] as a non-nil slice so marshaling the
			// merged configuration retains the caller's clear operation.
			patch.ExcludePatterns = append([]string{}, patterns...)
			patch.ExcludePatternsSet = true
		case "repository_version":
			patch.RepositoryVersionSet = true
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				continue
			}
			var version int
			if json.Unmarshal(value, &version) != nil || (version != 1 && version != 2) {
				return ExecutorSettingsPatch{}, newValidationError("executor_settings.repository_version 仅支持 1 或 2")
			}
			patch.RepositoryVersion = &version
		case "bandwidth_limit":
			var bandwidth string
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &bandwidth) != nil {
				return ExecutorSettingsPatch{}, newValidationError("executor_settings.bandwidth_limit 必须是字符串")
			}
			patch.BandwidthLimit = bandwidth
			patch.BandwidthLimitSet = true
		case "transfers":
			var transfers int
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &transfers) != nil {
				return ExecutorSettingsPatch{}, newValidationError("executor_settings.transfers 必须是整数")
			}
			patch.Transfers = transfers
			patch.TransfersSet = true
		default:
			return ExecutorSettingsPatch{}, newValidationError("executor_settings 包含不支持的字段")
		}
	}
	return patch, nil
}

// ParseExecutorSecretsPatch decodes the write-only secret update object.
// Null is rejected because this API has no safe implicit secret-erasure
// operation; an empty string retains an existing configured secret.
func ParseExecutorSecretsPatch(raw []byte) (ExecutorSecretsPatch, error) {
	object, err := decodeTaskJSONObject(raw)
	if err != nil {
		return ExecutorSecretsPatch{}, newValidationError("executor_secrets 必须是合法的 JSON 对象")
	}
	var patch ExecutorSecretsPatch
	for key, value := range object {
		if key != "repository_password" {
			return ExecutorSecretsPatch{}, newValidationError("executor_secrets 包含不支持的字段")
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ExecutorSecretsPatch{}, newValidationError("executor_secrets.repository_password 不支持 null")
		}
		var password string
		if json.Unmarshal(value, &password) != nil {
			return ExecutorSecretsPatch{}, newValidationError("executor_secrets.repository_password 必须是字符串")
		}
		patch.RepositoryPassword = password
		patch.RepositoryPasswordSet = true
	}
	return patch, nil
}

func decodeTaskJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("expected JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid object key")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("object key must be a string")
		}
		if _, exists := object[key]; exists {
			return nil, fmt.Errorf("duplicate object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid object value")
		}
		object[key] = append(json.RawMessage(nil), value...)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("invalid object terminator")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return object, nil
}

func mergeExecutorConfigPatch(
	previousExecutorType, nextExecutorType, previousConfig string,
	settings *ExecutorSettingsPatch,
	secrets *ExecutorSecretsPatch,
) (string, error) {
	previousExecutorType = strings.TrimSpace(strings.ToLower(previousExecutorType))
	nextExecutorType = strings.TrimSpace(strings.ToLower(nextExecutorType))
	if secrets != nil && secrets.RepositoryPasswordSet && nextExecutorType != "restic" {
		return "", newValidationError("executor_secrets.repository_password 仅适用于 restic")
	}
	if settings != nil && settings.hasRsyncFields() && nextExecutorType != "restic" {
		return "", newValidationError("executor_settings.exclude_patterns/repository_version 仅适用于 restic")
	}
	if settings != nil && settings.hasRcloneFields() && nextExecutorType != "rclone" {
		return "", newValidationError("executor_settings.bandwidth_limit/transfers 仅适用于 rclone")
	}

	sameType := previousExecutorType == nextExecutorType
	config := make(map[string]json.RawMessage)
	changed := false
	if sameType && strings.TrimSpace(previousConfig) != "" {
		var err error
		config, err = decodeTaskJSONObject([]byte(previousConfig))
		if err != nil {
			return "", newValidationError("现有 executor_config 无法安全解析，拒绝覆盖")
		}
	}

	if settings != nil {
		if settings.ExcludePatternsSet {
			raw, _ := json.Marshal(settings.ExcludePatterns)
			if !bytes.Equal(config["exclude_patterns"], raw) {
				config["exclude_patterns"] = raw
				changed = true
			}
		}
		if settings.RepositoryVersionSet {
			if settings.RepositoryVersion == nil {
				if _, exists := config["repository_version"]; exists {
					delete(config, "repository_version")
					changed = true
				}
			} else {
				raw, _ := json.Marshal(*settings.RepositoryVersion)
				if !bytes.Equal(config["repository_version"], raw) {
					config["repository_version"] = raw
					changed = true
				}
			}
		}
		if settings.BandwidthLimitSet {
			raw, _ := json.Marshal(settings.BandwidthLimit)
			if !bytes.Equal(config["bandwidth_limit"], raw) {
				config["bandwidth_limit"] = raw
				changed = true
			}
		}
		if settings.TransfersSet {
			raw, _ := json.Marshal(settings.Transfers)
			if !bytes.Equal(config["transfers"], raw) {
				config["transfers"] = raw
				changed = true
			}
		}
	}
	if secrets != nil && secrets.RepositoryPasswordSet &&
		strings.TrimSpace(secrets.RepositoryPassword) != "" {
		raw, _ := json.Marshal(secrets.RepositoryPassword)
		if !bytes.Equal(config["repository_password"], raw) {
			config["repository_password"] = raw
			changed = true
		}
	}
	if !changed {
		if !sameType {
			return "", nil
		}
		return previousConfig, nil
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode executor config: %w", err)
	}
	return string(encoded), nil
}

func (patch ExecutorSettingsPatch) hasRsyncFields() bool {
	return patch.ExcludePatternsSet || patch.RepositoryVersionSet
}

func (patch ExecutorSettingsPatch) hasRcloneFields() bool {
	return patch.BandwidthLimitSet || patch.TransfersSet
}

// ProjectExecutorSettings returns only the non-secret settings that are safe
// for task read responses. A malformed stored object returns an error rather
// than a fabricated zero/default projection.
func ProjectExecutorSettings(executorType, raw string) (any, error) {
	executorType = strings.TrimSpace(strings.ToLower(executorType))
	if strings.TrimSpace(raw) == "" {
		switch executorType {
		case "restic":
			return ResticExecutorSettingsResponse{ExcludePatterns: []string{}, RepositoryVersion: nil}, nil
		case "rclone":
			return RcloneExecutorSettingsResponse{}, nil
		default:
			return map[string]any{}, nil
		}
	}
	config, err := decodeTaskJSONObject([]byte(raw))
	if err != nil {
		return nil, err
	}
	switch executorType {
	case "restic":
		patterns := []string{}
		if value, exists := config["exclude_patterns"]; exists {
			if json.Unmarshal(value, &patterns) != nil || patterns == nil {
				return nil, fmt.Errorf("invalid Restic exclude_patterns")
			}
		}
		var version *int
		if value, exists := config["repository_version"]; exists &&
			!bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			var decoded int
			if json.Unmarshal(value, &decoded) != nil || (decoded != 1 && decoded != 2) {
				return nil, fmt.Errorf("invalid Restic repository_version")
			}
			version = &decoded
		}
		return ResticExecutorSettingsResponse{ExcludePatterns: patterns, RepositoryVersion: version}, nil
	case "rclone":
		var bandwidth string
		if value, exists := config["bandwidth_limit"]; exists && json.Unmarshal(value, &bandwidth) != nil {
			return nil, fmt.Errorf("invalid Rclone bandwidth_limit")
		}
		transfers := 0
		if value, exists := config["transfers"]; exists && json.Unmarshal(value, &transfers) != nil {
			return nil, fmt.Errorf("invalid Rclone transfers")
		}
		return RcloneExecutorSettingsResponse{BandwidthLimit: bandwidth, Transfers: transfers}, nil
	default:
		return map[string]any{}, nil
	}
}

// ProjectExecutorSecretsConfigured reports configured status without exposing
// secret bytes. Malformed stored configuration is surfaced to the caller.
func ProjectExecutorSecretsConfigured(raw string) (ExecutorSecretsConfiguredResponse, error) {
	if strings.TrimSpace(raw) == "" {
		return ExecutorSecretsConfiguredResponse{}, nil
	}
	config, err := decodeTaskJSONObject([]byte(raw))
	if err != nil {
		return ExecutorSecretsConfiguredResponse{}, err
	}
	var configured bool
	if value, exists := config["repository_password"]; exists {
		var password string
		if json.Unmarshal(value, &password) != nil {
			return ExecutorSecretsConfiguredResponse{}, fmt.Errorf("invalid repository_password")
		}
		configured = password != ""
	}
	return ExecutorSecretsConfiguredResponse{RepositoryPassword: configured}, nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// ValidateTaskInput validates all fields of a CreateTaskInput.
func ValidateTaskInput(req CreateTaskInput) error {
	if err := validateTaskIdentityAndSchedule(req); err != nil {
		return err
	}

	if req.ExecutorType == "command" {
		command := strings.TrimSpace(req.Command)
		if command == "" {
			return newValidationError("命令类型任务必须填写命令内容")
		}
		if len(command) > maxCommandLength {
			return newValidationError(fmt.Sprintf("命令长度不能超过 %d 字符", maxCommandLength))
		}
		if isDangerousCommand(command) {
			return newValidationError("该命令被安全策略拦截，禁止执行")
		}
	} else {
		if strings.TrimSpace(req.RsyncSource) == "" || strings.TrimSpace(req.RsyncTarget) == "" {
			return newValidationError("同步任务必须填写源路径和目标路径")
		}
		// Reject known shell injection characters (NUL/CR/LF/backtick/$(...)).
		// Defense-in-depth: the executor already ShellEscapes all user input,
		// but we additionally block obviously malicious input at the API layer.
		if !util.IsRemotePathSpec(req.RsyncSource) {
			if err := validatePathChars(req.RsyncSource, "rsync_source"); err != nil {
				return newValidationError(err.Error())
			}
		}
		if !util.IsRemotePathSpec(req.RsyncTarget) {
			if err := validatePathChars(req.RsyncTarget, "rsync_target"); err != nil {
				return newValidationError(err.Error())
			}
		}
	}

	if req.ExecutorType == "rsync" {
		publicationConfig, err := ParseRsyncPublicationConfigV1(req.ExecutorConfig)
		if err != nil {
			return err
		}
		if publicationConfig.PublicationMode != backupasset.PublicationLegacyMutable {
			return newValidationError("rsync 版本化发布必须通过预检迁移流程启用")
		}
	} else if req.ExecutorType == "rclone" {
		publicationConfig, err := ParseRcloneTaskConfigV1(req.ExecutorConfig)
		if err != nil {
			return err
		}
		if publicationConfig.PublicationMode != backupasset.PublicationLegacyMutable {
			return newValidationError("rclone 版本化发布必须通过预检激活流程启用")
		}
	} else if cfg := strings.TrimSpace(req.ExecutorConfig); cfg != "" {
		if !json.Valid([]byte(cfg)) {
			return newValidationError("executor_config 必须是合法的 JSON 格式")
		}
	}

	if req.ExecutorType != "command" {
		pathPolicy, err := rsyncconfinement.LoadPolicyFromEnv()
		if err != nil {
			return newValidationError("Rsync 路径策略配置无效: " + err.Error())
		}
		if !util.IsRemotePathSpec(req.RsyncSource) {
			if err := pathPolicy.ValidateSource(req.RsyncSource, "rsync_source"); err != nil {
				return newValidationError(err.Error())
			}
		}
		if !util.IsRemotePathSpec(req.RsyncTarget) {
			if err := pathPolicy.ValidateTarget(req.RsyncTarget, "rsync_target"); err != nil {
				return newValidationError(err.Error())
			}
		}
	}

	return nil
}

func validateTaskIdentityAndSchedule(req CreateTaskInput) error {
	if req.Name == "" {
		return newValidationError("任务名称不能为空")
	}
	if req.NodeID == 0 {
		return newValidationError("请选择目标节点")
	}
	switch req.ExecutorType {
	case "rsync", "command", "restic", "rclone":
	default:
		return newValidationError("不支持的执行器类型，仅允许 rsync / command / restic / rclone")
	}
	if req.CronSpec != "" {
		if err := validateCronSpec(req.CronSpec); err != nil {
			return newValidationError(err.Error())
		}
	}
	return nil
}

// ValidateTaskRefs validates that referenced nodes, policies, and dependency tasks exist.
func ValidateTaskRefs(ctx context.Context, nodeRepo repository.NodeRepository, policyRepo repository.PolicyRepository, taskRepo repository.TaskRepository, req CreateTaskInput, selfID uint) error {
	if err := validateTaskNodeAndPolicy(ctx, nodeRepo, policyRepo, req); err != nil {
		return err
	}
	return validateTaskDependencyRefs(ctx, taskRepo, req, selfID)
}

func validateTaskNodeAndPolicy(ctx context.Context, nodeRepo repository.NodeRepository, policyRepo repository.PolicyRepository, req CreateTaskInput) error {
	if req.NodeID != 0 {
		exists, err := nodeRepo.ExistsByID(ctx, req.NodeID)
		if err != nil {
			return fmt.Errorf("校验节点失败: %w", err)
		}
		if !exists {
			return newValidationError("所选节点不存在，请重新选择")
		}
	}
	if req.PolicyID != nil {
		exists, err := policyRepo.ExistsByID(ctx, *req.PolicyID)
		if err != nil {
			return fmt.Errorf("校验策略失败: %w", err)
		}
		if !exists {
			return newValidationError("所选策略不存在，请重新选择")
		}
	}
	return nil
}

func validateTaskDependencyRefs(ctx context.Context, taskRepo repository.TaskRepository, req CreateTaskInput, selfID uint) error {
	if req.DependsOnTaskID == nil {
		return nil
	}
	if strings.TrimSpace(req.CronSpec) != "" {
		return newValidationError("设置了前置任务的任务不能同时设置定时调度")
	}
	if selfID != 0 && *req.DependsOnTaskID == selfID {
		return newValidationError("任务不能依赖自身")
	}
	exists, err := taskRepo.ExistsLiveByID(ctx, *req.DependsOnTaskID)
	if err != nil {
		return fmt.Errorf("校验前置任务失败: %w", err)
	}
	if !exists {
		return newValidationError("所选前置任务不存在，请重新选择")
	}
	if selfID != 0 {
		if err := detectDependencyCycle(ctx, taskRepo, selfID, *req.DependsOnTaskID, 10); err != nil {
			return err
		}
	}
	return nil
}

// detectDependencyCycle walks the depends_on_task_id chain from startID
// upward. If it reaches selfID, a cycle exists.
func detectDependencyCycle(ctx context.Context, taskRepo repository.TaskRepository, selfID, startID uint, maxDepth int) error {
	current := startID
	for i := 0; i < maxDepth; i++ {
		t, err := taskRepo.FindByIDFields(ctx, current, "id", "depends_on_task_id")
		if err != nil {
			return nil // task not found, cannot continue
		}
		if t.DependsOnTaskID == nil {
			return nil
		}
		if *t.DependsOnTaskID == selfID {
			return newValidationError("检测到循环依赖，请检查前置任务配置")
		}
		current = *t.DependsOnTaskID
	}
	return nil
}

// ---------------------------------------------------------------------------
// Pure validation utilities (no DB)
// ---------------------------------------------------------------------------

func validateCronSpec(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := cronutil.Validate(raw); err != nil {
		return fmt.Errorf("cron 表达式格式不正确: %w", err)
	}
	return nil
}
func parseCSVEnvList(key string) []string {
	roots, err := rsyncconfinement.ParseRoots(os.Getenv(key))
	if err == nil {
		return roots
	}
	// Preserve an invalid configured value as a non-matching root. Callers
	// validate through rsyncconfinement.ValidatePath, which fails closed.
	return []string{os.Getenv(key)}
}

func validatePathByPrefix(path string, prefixes []string, label string) error {
	return rsyncconfinement.ValidatePath(path, prefixes, label)
}
func validatePathChars(path, label string) error {
	for _, ch := range path {
		switch ch {
		case '\x00':
			return fmt.Errorf("%s 包含非法字符 NUL", label)
		case '\r':
			return fmt.Errorf("%s 包含非法字符 CR", label)
		case '\n':
			return fmt.Errorf("%s 包含非法字符 LF", label)
		case '`':
			return fmt.Errorf("%s 包含非法字符反引号", label)
		case '$':
			return fmt.Errorf("%s 包含非法字符 $，请使用 ShellEscape 转义", label)
		}
	}
	return nil
}

func isDangerousCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return true
	}
	dangerousPrefixes := []string{
		"rm ", "rm\t", "rm\n", "rm\\",
		"mkfs.", "mkswap", "dd if=",
		">/dev/sd", ">/dev/nvme", ">/dev/xvd", ">/dev/vd", ">/dev/mmcblk",
		"> /dev/sd", "> /dev/nvme", "> /dev/xvd", "> /dev/vd", "> /dev/mmcblk",
		":(){ :|:& };:", "chmod 777 /", "chmod -R 777 /",
		"wget ", "curl ",
	}
	for _, prefix := range dangerousPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}
