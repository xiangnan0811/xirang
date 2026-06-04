package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/config"
	"xirang/backend/internal/model"
	policyPkg "xirang/backend/internal/policy"
	"xirang/backend/internal/repository"
	"xirang/backend/internal/util"
)

const maxCommandLength = 4096

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

// CreateTaskInput is the input for creating or updating a task.
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
// hydration, validation, DB persistence, and cron schedule sync.
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
	if err := ValidateTaskRefs(ctx, s.nodeRepo, s.policyRepo, s.taskRepo, input, 0); err != nil {
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
	if err := s.taskRepo.Create(ctx, &taskEntity); err != nil {
		return model.Task{}, apperr.WrapDBError(err)
	}
	if s.runner != nil {
		if err := s.runner.SyncSchedule(taskEntity); err != nil {
			s.runner.RemoveSchedule(taskEntity.ID)
			if rollbackErr := s.taskRepo.Delete(ctx, taskEntity.ID); rollbackErr != nil {
				return model.Task{}, fmt.Errorf("任务调度同步失败且补偿删除失败: %w", rollbackErr)
			}
			return model.Task{}, newValidationError("任务调度失败，请检查 Cron 表达式是否正确")
		}
	}
	return taskEntity, nil
}

// ---------------------------------------------------------------------------
// UpdateTask
// ---------------------------------------------------------------------------

// UpdateTask updates an existing task. It loads the current state, applies
// defaults, validates, persists, and syncs the cron schedule.
func (s *TaskApiService) UpdateTask(ctx context.Context, id uint, input CreateTaskInput) (model.Task, error) {
	SanitizeCreateTaskInput(&input)

	taskEntity, err := s.taskRepo.FindByID(ctx, id)
	if err != nil {
		return model.Task{}, apperr.WrapDBError(err)
	}
	// Value copy for compensating rollback; the copy shares pointer fields
	// (e.g. PolicyID) — callers must not mutate through *previous.PolicyID.
	previous := *taskEntity

	HydrateTaskDefaultsFromPolicy(ctx, s.policyRepo, s.nodeRepo, &input)
	InferTaskExecutor(&input, taskEntity.ExecutorType)
	TrimTaskInput(&input)

	// Fill blanks from existing entity.
	if input.Name == "" {
		input.Name = taskEntity.Name
	}
	if input.NodeID == 0 {
		input.NodeID = taskEntity.NodeID
	}
	if input.PolicyID == nil {
		input.PolicyID = taskEntity.PolicyID
	}
	if input.RsyncSource == "" {
		input.RsyncSource = taskEntity.RsyncSource
	}
	if input.RsyncTarget == "" {
		input.RsyncTarget = taskEntity.RsyncTarget
	}
	if input.CronSpec == "" {
		input.CronSpec = taskEntity.CronSpec
	}
	if input.ExecutorType == "" {
		input.ExecutorType = taskEntity.ExecutorType
	}
	input.ExecutorConfig = mergeTaskExecutorConfigForUpdate(taskEntity.ExecutorType, input.ExecutorType, taskEntity.ExecutorConfig, input.ExecutorConfig)

	EnsureNodeTargetPrefix(ctx, s.nodeRepo, &input)
	// When node changes for rsync/restic tasks, regenerate target from new node.
	if (input.ExecutorType == "rsync" || input.ExecutorType == "restic") &&
		input.NodeID != 0 && input.NodeID != taskEntity.NodeID {
		if node, err := s.nodeRepo.FindByID(ctx, input.NodeID); err == nil && node.BackupDir != "" {
			input.RsyncTarget = policyPkg.NodeTargetPath(config.BackupRoot, node.BackupDir)
		}
	}

	if err := ValidateTaskInput(input); err != nil {
		return model.Task{}, err
	}
	if err := ValidateTaskRefs(ctx, s.nodeRepo, s.policyRepo, s.taskRepo, input, id); err != nil {
		return model.Task{}, err
	}

	taskEntity.Name = input.Name
	taskEntity.NodeID = input.NodeID
	taskEntity.PolicyID = input.PolicyID
	taskEntity.DependsOnTaskID = input.DependsOnTaskID
	taskEntity.Command = input.Command
	taskEntity.RsyncSource = input.RsyncSource
	taskEntity.RsyncTarget = input.RsyncTarget
	taskEntity.ExecutorType = input.ExecutorType
	taskEntity.ExecutorConfig = input.ExecutorConfig
	taskEntity.CronSpec = input.CronSpec

	if err := s.taskRepo.Update(ctx, taskEntity); err != nil {
		return model.Task{}, apperr.WrapDBError(err)
	}
	if s.runner != nil {
		if err := s.runner.SyncSchedule(*taskEntity); err != nil {
			s.runner.RemoveSchedule(taskEntity.ID)
			if restoreErr := s.taskRepo.Update(ctx, &previous); restoreErr != nil {
				return model.Task{}, fmt.Errorf("任务调度同步失败且补偿回滚失败: %w", restoreErr)
			}
			if restoreScheduleErr := s.runner.SyncSchedule(previous); restoreScheduleErr != nil {
				s.runner.RemoveSchedule(taskEntity.ID)
				return model.Task{}, fmt.Errorf("任务调度同步失败且补偿调度失败: %w", restoreScheduleErr)
			}
			return model.Task{}, newValidationError("任务调度失败，请检查 Cron 表达式是否正确")
		}
	}
	return *taskEntity, nil
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
		if node, err := nodeRepo.FindByID(ctx, req.NodeID); err == nil && node.BackupDir != "" {
			req.RsyncTarget = policyPkg.NodeTargetPath(config.BackupRoot, node.BackupDir)
		}
	}
	if strings.TrimSpace(req.CronSpec) == "" {
		req.CronSpec = p.CronSpec
	}
}

// EnsureNodeTargetPrefix ensures that policy-linked tasks have the node
// subdirectory in RsyncTarget. When the target is exactly the backup root
// (missing the node prefix), it appends the node's backupDir.
func EnsureNodeTargetPrefix(ctx context.Context, nodeRepo repository.NodeRepository, req *CreateTaskInput) {
	if req.NodeID == 0 {
		return
	}
	if strings.TrimSpace(req.RsyncTarget) == "" {
		return
	}
	if util.IsRemotePathSpec(req.RsyncTarget) {
		return
	}
	// If target is just the backup root without node subdirectory, append it.
	if strings.TrimRight(req.RsyncTarget, "/") == strings.TrimRight(config.BackupRoot, "/") {
		if node, err := nodeRepo.FindByID(ctx, req.NodeID); err == nil && node.BackupDir != "" {
			req.RsyncTarget = policyPkg.NodeTargetPath(config.BackupRoot, node.BackupDir)
		}
	}
}

// AutoGenerateTarget generates a target path for rsync/restic tasks when
// RsyncTarget is still empty after all other defaults have been applied.
func AutoGenerateTarget(ctx context.Context, nodeRepo repository.NodeRepository, req *CreateTaskInput) {
	if (req.ExecutorType != "rsync" && req.ExecutorType != "restic") || strings.TrimSpace(req.RsyncTarget) != "" {
		return
	}
	if node, err := nodeRepo.FindByID(ctx, req.NodeID); err == nil && node.BackupDir != "" {
		req.RsyncTarget = policyPkg.NodeTargetPath(config.BackupRoot, node.BackupDir)
	}
}

func mergeTaskExecutorConfigForUpdate(previousExecutorType, nextExecutorType, previousConfig, nextConfig string) string {
	previousExecutorType = strings.TrimSpace(strings.ToLower(previousExecutorType))
	nextExecutorType = strings.TrimSpace(strings.ToLower(nextExecutorType))
	if previousExecutorType != nextExecutorType {
		return nextConfig
	}

	trimmedNext := strings.TrimSpace(nextConfig)
	if trimmedNext == "" {
		return previousConfig
	}

	merged, ok := preserveBlankSecretConfigValues(previousConfig, trimmedNext)
	if ok {
		return merged
	}
	return nextConfig
}

func preserveBlankSecretConfigValues(previousConfig, nextConfig string) (string, bool) {
	var previous map[string]interface{}
	var next map[string]interface{}
	if err := json.Unmarshal([]byte(previousConfig), &previous); err != nil {
		return "", false
	}
	if err := json.Unmarshal([]byte(nextConfig), &next); err != nil {
		return "", false
	}

	changed := false
	for key, value := range next {
		if !isSecretConfigKey(key) {
			continue
		}
		if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
			if previousValue, exists := previous[key]; exists {
				next[key] = previousValue
				changed = true
			}
		}
	}
	if !changed {
		return nextConfig, true
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func isSecretConfigKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "api_key") ||
		strings.Contains(normalized, "access_key")
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// ValidateTaskInput validates all fields of a CreateTaskInput.
func ValidateTaskInput(req CreateTaskInput) error {
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

	if cfg := strings.TrimSpace(req.ExecutorConfig); cfg != "" {
		if !json.Valid([]byte(cfg)) {
			return newValidationError("executor_config 必须是合法的 JSON 格式")
		}
	}

	sourceAllowList := parseCSVEnvList("RSYNC_ALLOWED_SOURCE_PREFIXES")
	targetAllowList := parseCSVEnvList("RSYNC_ALLOWED_TARGET_PREFIXES")

	if !util.IsRemotePathSpec(req.RsyncSource) {
		if err := validatePathByPrefix(req.RsyncSource, sourceAllowList, "rsync_source"); err != nil {
			return newValidationError(err.Error())
		}
	}
	if !util.IsRemotePathSpec(req.RsyncTarget) {
		if err := validatePathByPrefix(req.RsyncTarget, targetAllowList, "rsync_target"); err != nil {
			return newValidationError(err.Error())
		}
	}

	return nil
}

// ValidateTaskRefs validates that referenced nodes, policies, and dependency tasks exist.
func ValidateTaskRefs(ctx context.Context, nodeRepo repository.NodeRepository, policyRepo repository.PolicyRepository, taskRepo repository.TaskRepository, req CreateTaskInput, selfID uint) error {
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
	if req.DependsOnTaskID != nil {
		// cron and dependency are mutually exclusive.
		if strings.TrimSpace(req.CronSpec) != "" {
			return newValidationError("设置了前置任务的任务不能同时设置定时调度")
		}
		// A task cannot depend on itself.
		if selfID != 0 && *req.DependsOnTaskID == selfID {
			return newValidationError("任务不能依赖自身")
		}
		// Check that the dependency exists.
		exists, err := taskRepo.ExistsByID(ctx, *req.DependsOnTaskID)
		if err != nil {
			return fmt.Errorf("校验前置任务失败: %w", err)
		}
		if !exists {
			return newValidationError("所选前置任务不存在，请重新选择")
		}
		// Cycle detection: trace upward from the dependency, max depth 10.
		if selfID != 0 {
			if err := detectDependencyCycle(ctx, taskRepo, selfID, *req.DependsOnTaskID, 10); err != nil {
				return err
			}
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
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "@every ") {
		return nil
	}
	// Simple sanity check: must have at least 5 fields.
	fields := strings.Fields(raw)
	if len(fields) < 5 || len(fields) > 6 {
		return fmt.Errorf("cron 表达式格式不正确，需要 5 个字段（分 时 日 月 星期）")
	}
	return nil
}

func parseCSVEnvList(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func validatePathByPrefix(path string, prefixes []string, label string) error {
	if len(prefixes) == 0 {
		return nil
	}
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return nil
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(cleaned, prefix) {
			return nil
		}
	}
	return fmt.Errorf("%s 路径前缀不在允许列表中: %s", label, cleaned)
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
