package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	pathpkg "path"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	drillTerminalTransitionAttempts = 3
	drillRecoveryBatchSize          = 32
	drillRecoveryMaxBatches         = 4
	drillRecoveryPassTimeout        = 10 * time.Second
	activeDrillRunIndex             = "idx_task_runs_active_drill"
)

// validateDrillConfig 校验演习配置的合法性。
// 返回 nil 表示配置合法。
func (m *Manager) validateDrillConfig(policy *model.Policy, sandboxNode *model.Node) error {
	if sandboxNode == nil {
		return fmt.Errorf("沙箱节点不存在")
	}

	// 沙箱节点不能是备份源节点
	for _, n := range policy.Nodes {
		if n.ID == sandboxNode.ID {
			return fmt.Errorf("沙箱节点不能是备份源节点 (%s)", n.Name)
		}
	}

	// 校验恢复路径
	restorePath := strings.TrimSpace(policy.DrillRestorePath)
	if restorePath == "" {
		restorePath = "/tmp/xirang-drill"
	}
	if err := validateDrillSandboxPath(restorePath); err != nil {
		return fmt.Errorf("演习恢复路径无效: %w", err)
	}

	if policy.DrillTargetNodeID == nil || *policy.DrillTargetNodeID == 0 {
		return fmt.Errorf("未配置沙箱节点")
	}

	return nil
}

// validateDrillSandboxPath 校验恢复演练沙箱路径，禁止指向系统目录及其子路径。
func validateDrillSandboxPath(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("恢复路径不能为空")
	}
	if err := validateRestorePath(trimmed); err != nil {
		return err
	}
	cleanPath := pathpkg.Clean(trimmed)
	forbidden := []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot", "/dev", "/proc", "/sys", "/run", "/var/run"}
	for _, dir := range forbidden {
		if cleanPath == dir || strings.HasPrefix(cleanPath, dir+"/") {
			return fmt.Errorf("禁止恢复到系统目录: %s", dir)
		}
	}
	return nil
}

// DrillAvailable reports whether the manager has both a safe restore transport
// and the durable multi-node admission boundary required to use it.
func (m *Manager) DrillAvailable() bool {
	return m != nil && m.drillRestoreFunc != nil && m.nodeWriteAdmission != nil
}

// TriggerDrill 手动或 cron 触发一次恢复演练。
// allowedSourceNodeIDs 为 nil 时不限制源节点（admin / cron）；非 nil 时仅从这些节点的任务中选源。
// 返回创建的 drill TaskRun ID。
func (m *Manager) TriggerDrill(policyID uint, allowedSourceNodeIDs []uint) (uint, error) {
	if m.shuttingDown.Load() {
		return 0, fmt.Errorf("系统维护中，请稍候再试")
	}

	// 1. 加载策略 + 关联源节点 + 沙箱节点
	var policy model.Policy
	if err := m.db.Preload("Nodes").First(&policy, policyID).Error; err != nil {
		return 0, fmt.Errorf("策略不存在")
	}

	if !policy.DrillEnabled {
		return 0, fmt.Errorf("该策略未启用恢复演练")
	}

	if policy.DrillTargetNodeID == nil {
		return 0, fmt.Errorf("该策略未配置沙箱节点")
	}

	var sandboxNode model.Node
	if err := m.db.Preload("SSHKey").First(&sandboxNode, *policy.DrillTargetNodeID).Error; err != nil {
		return 0, fmt.Errorf("沙箱节点不存在")
	}

	// 2. 校验配置
	if err := m.validateDrillConfig(&policy, &sandboxNode); err != nil {
		return 0, err
	}

	// 3. 查找关联任务（取第一个有成功记录的 rsync 任务；可按授权源节点过滤）
	task, err := m.findTaskForPolicy(policy.ID, allowedSourceNodeIDs)
	if err != nil {
		return 0, err
	}
	if !m.DrillAvailable() {
		return 0, ErrDrillUnavailable
	}
	if m.drillRecoveryBlocked.Load() {
		return 0, fmt.Errorf("恢复演练状态对账未完成，请稍后重试")
	}

	// Drill ownership is keyed by the source task, just like ordinary and
	// restore runs. This lets Cancel and Shutdown signal the exact run before
	// it enters any remote phase.
	launchCtx, ownership, claimed := m.claimPendingRunOwnership(task.ID)
	if !claimed {
		return 0, ErrDrillAlreadyActive
	}
	scheduled := false
	defer func() {
		if scheduled {
			return
		}
		ownership.cancel()
		m.chainRunner.Delete(task.ID)
		m.pendingRuns.CompareAndDelete(task.ID, ownership)
	}()

	runCtx, runCancel := m.newRunContext(launchCtx, computeExecTimeout(task))
	ownership.addCancel(runCancel)
	if err := launchCtx.Err(); err != nil {
		return 0, err
	}
	if err := runCtx.Err(); err != nil {
		return 0, err
	}

	// 4. Atomically reserve the durable active slot and its pending Evidence.
	// Process-local ownership remains a fast path; the partial unique index is
	// authoritative when another Manager/process races this transaction.
	evidence, err := m.pendingDrillEvidence(runCtx, policy, task, sandboxNode)
	if err != nil {
		return 0, fmt.Errorf("创建演习执行记录失败")
	}
	run, err := m.reserveDrillRun(runCtx, task, evidence)
	if err != nil {
		if errors.Is(err, ErrDrillAlreadyActive) {
			return 0, ErrDrillAlreadyActive
		}
		logger.Module("task").Warn().Err(err).Uint("task_id", task.ID).Msg("创建恢复演练执行记录失败")
		return 0, fmt.Errorf("创建演习执行记录失败")
	}

	// 5. 异步执行演习 with the manager-owned cancellable context.
	scheduled = true
	m.taskWG.Add(1)
	go func() {
		defer m.taskWG.Done()
		m.executeDrillWithContext(runCtx, &policy, task, sandboxNode, run.ID, ownership, runCancel)
	}()

	return run.ID, nil
}

func (m *Manager) pendingDrillEvidence(
	ctx context.Context,
	policy model.Policy,
	task model.Task,
	sandboxNode model.Node,
) (model.RestoreDrillEvidence, error) {
	restorePath := strings.TrimSpace(policy.DrillRestorePath)
	if restorePath == "" {
		restorePath = "/tmp/xirang-drill"
	}
	sourceRunID, err := m.latestSuccessfulRunIDWithContext(ctx, task.ID, task.NodeID)
	if err != nil {
		return model.RestoreDrillEvidence{}, err
	}
	snapshotRef := ""
	if sourceRunID != nil {
		snapshotRef = fmt.Sprintf("task_run:%d", *sourceRunID)
	}
	leaseDuration := m.drillRecoveryLease
	if leaseDuration <= 0 {
		leaseDuration = defaultDrillRecoveryLease
	}
	leaseUntil := time.Now().UTC().Add(leaseDuration)
	return model.RestoreDrillEvidence{
		PolicyID:           policy.ID,
		TaskID:             task.ID,
		SourceTaskRunID:    sourceRunID,
		SnapshotRef:        snapshotRef,
		SandboxNodeID:      sandboxNode.ID,
		SandboxNodeName:    sandboxNode.Name,
		SandboxPath:        restorePath,
		Status:             model.TaskRunStatusPending,
		ConfidenceEligible: false,
		RestoreStatus:      model.TaskRunStatusPending,
		VerifyStatus:       model.TaskRunStatusPending,
		PostVerifyStatus:   model.TaskRunStatusSkipped,
		CleanupStatus:      model.TaskRunStatusSkipped,
		RecoveryOwnerID:    m.drillOwnerID,
		RecoveryLeaseUntil: &leaseUntil,
	}, nil
}

func (m *Manager) reserveDrillRun(
	ctx context.Context,
	task model.Task,
	evidence model.RestoreDrillEvidence,
) (model.TaskRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	wait := m.nodeWriteRetryWait
	if wait == nil {
		wait = waitForNodeWriteReservationRetry
	}
	for attempt := 0; attempt < nodeWriteReservationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return model.TaskRun{}, err
		}
		candidate := model.TaskRun{
			TaskID:         task.ID,
			NodeIDSnapshot: task.NodeID,
			TriggerType:    "drill",
			Status:         model.TaskRunStatusPending,
		}
		err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var locked model.Task
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", task.ID).Limit(1).Find(&locked)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
			if locked.ArchivedAt != nil {
				return ErrTaskArchived
			}
			if locked.NodeID != task.NodeID {
				return ErrNodeWriteStartLost
			}
			if m.nodeWriteAdmission == nil {
				return ErrNodeWriteUnavailable
			}
			if err := m.nodeWriteAdmission.AdmitDrillTx(ctx, tx, locked.NodeID, evidence.SandboxNodeID); err != nil {
				return err
			}
			var activeCount int64
			if err := tx.Model(&model.TaskRun{}).
				Where("task_id = ? AND trigger_type = ? AND status IN ?", task.ID, "drill", model.TaskRunActiveStatuses()).
				Count(&activeCount).Error; err != nil {
				return err
			}
			if activeCount != 0 {
				return ErrDrillAlreadyActive
			}
			if err := tx.Create(&candidate).Error; err != nil {
				return err
			}
			evidence.TaskRunID = candidate.ID
			return tx.Create(&evidence).Error
		})
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, ErrDrillAlreadyActive) || isActiveDrillReservationConflict(err) {
			return model.TaskRun{}, ErrDrillAlreadyActive
		}
		if !retryableNodeWriteReservationError(err) || attempt+1 == nodeWriteReservationAttempts {
			return model.TaskRun{}, err
		}
		if err := wait(ctx, attempt); err != nil {
			return model.TaskRun{}, err
		}
	}
	return model.TaskRun{}, ErrNodeWriteUnavailable
}

func (m *Manager) startDrillLeaseHeartbeat(
	ctx context.Context,
	drillRunID uint,
	cancelRun context.CancelFunc,
) func() {
	if m == nil || m.db == nil || drillRunID == 0 || strings.TrimSpace(m.drillOwnerID) == "" {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	leaseDuration := m.drillRecoveryLease
	if leaseDuration <= 0 {
		leaseDuration = defaultDrillRecoveryLease
	}
	interval := leaseDuration / 3
	if interval <= 0 {
		interval = time.Second
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				leaseUntil := time.Now().UTC().Add(leaseDuration)
				result := m.db.WithContext(heartbeatCtx).Model(&model.RestoreDrillEvidence{}).
					Where("task_run_id = ? AND recovery_owner_id = ? AND status IN ?",
						drillRunID, m.drillOwnerID, model.TaskRunActiveStatuses()).
					Update("recovery_lease_until", &leaseUntil)
				if result.Error != nil || result.RowsAffected != 1 {
					if heartbeatCtx.Err() == nil {
						logger.Module("task").Warn().Err(result.Error).Uint("task_run_id", drillRunID).
							Int64("rows_affected", result.RowsAffected).
							Msg("恢复演练租约已丢失，终止 runner")
					}
					if cancelRun != nil {
						cancelRun()
					}
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

type expiredDrillCandidate struct {
	RunID  uint `gorm:"column:run_id"`
	TaskID uint `gorm:"column:task_id"`
}

func (m *Manager) expiredDrillCandidates(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]expiredDrillCandidate, error) {
	var candidates []expiredDrillCandidate
	result := m.db.WithContext(ctx).Table("task_runs").
		Select("task_runs.id AS run_id, task_runs.task_id AS task_id").
		Joins("LEFT JOIN restore_drill_evidences ON restore_drill_evidences.task_run_id = task_runs.id").
		Where("task_runs.trigger_type = ?", "drill").
		Where(`(
			(task_runs.status IN ? AND (
				restore_drill_evidences.id IS NULL
				OR restore_drill_evidences.status NOT IN ?
				OR restore_drill_evidences.recovery_lease_until IS NULL
				OR restore_drill_evidences.recovery_lease_until <= ?
			))
			OR
			(task_runs.status IN ? AND restore_drill_evidences.status IN ?)
		)`, model.TaskRunActiveStatuses(), model.TaskRunActiveStatuses(), now,
			model.TaskRunTerminalStatuses(), model.TaskRunActiveStatuses()).
		Order("task_runs.id ASC").Limit(limit).Find(&candidates)
	if result.Error != nil {
		return nil, result.Error
	}
	return candidates, nil
}

func (m *Manager) reconcileExpiredDrills(ctx context.Context) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("restore drill recovery database is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	recoveryCtx, cancel := context.WithTimeout(ctx, drillRecoveryPassTimeout)
	defer cancel()
	ctx = recoveryCtx
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC()
	for batch := 0; batch < drillRecoveryMaxBatches; batch++ {
		candidates, err := m.expiredDrillCandidates(ctx, now, drillRecoveryBatchSize)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return err
			}
			won := false
			err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var run model.TaskRun
				runResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("id = ? AND task_id = ? AND trigger_type = ?",
						candidate.RunID, candidate.TaskID, "drill").
					Limit(1).Find(&run)
				if runResult.Error != nil {
					return runResult.Error
				}
				if runResult.RowsAffected == 0 {
					return nil
				}
				var evidence model.RestoreDrillEvidence
				evidenceResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("task_run_id = ? AND task_id = ?", run.ID, run.TaskID).Limit(1).Find(&evidence)
				if evidenceResult.Error != nil {
					return evidenceResult.Error
				}
				if model.IsTerminalTaskRunStatus(run.Status) {
					if evidenceResult.RowsAffected == 0 || !model.IsActiveTaskRunStatus(evidence.Status) {
						return nil
					}
					var repairErr error
					won, repairErr = repairActiveDrillEvidenceFromTerminalRunTx(tx, run, evidence)
					return repairErr
				}
				if !model.IsActiveTaskRunStatus(run.Status) {
					return nil
				}
				if evidenceResult.RowsAffected == 1 {
					if !model.IsActiveTaskRunStatus(evidence.Status) {
						var repairErr error
						won, repairErr = repairActiveDrillRunFromTerminalEvidenceTx(tx, run, evidence)
						return repairErr
					}
					if evidence.RecoveryLeaseUntil != nil && evidence.RecoveryLeaseUntil.After(now) {
						return nil
					}
				} else if err := synthesizeInterruptedDrillEvidenceTx(tx, run); err != nil {
					return err
				}
				var err error
				won, err = cancelOneDrillRunTx(tx, run.TaskID, run.ID, run.NodeIDSnapshot,
					"演习进程中断，已自动恢复", now)
				return err
			})
			if err != nil {
				return err
			}
			if won {
				m.releaseRecoveredDrillOwnership(candidate.TaskID, candidate.RunID)
			}
		}
		if len(candidates) < drillRecoveryBatchSize {
			return nil
		}
	}
	remaining, err := m.expiredDrillCandidates(ctx, now, 1)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("restore drill recovery backlog exceeds bounded pass")
	}
	return nil
}

func repairActiveDrillEvidenceFromTerminalRunTx(
	tx *gorm.DB,
	run model.TaskRun,
	evidence model.RestoreDrillEvidence,
) (bool, error) {
	if !model.IsTerminalTaskRunStatus(run.Status) || run.FinishedAt == nil || run.DurationMs < 0 {
		return false, fmt.Errorf("terminal restore drill task run is incomplete")
	}
	if run.StartedAt == nil {
		if run.DurationMs != 0 {
			return false, fmt.Errorf("terminal restore drill task run duration is inconsistent")
		}
	} else if drillDurationMs(run.StartedAt.UTC(), run.FinishedAt.UTC()) != run.DurationMs {
		return false, fmt.Errorf("terminal restore drill task run duration is inconsistent")
	}
	if run.Status == model.TaskRunStatusSuccess &&
		(evidence.RestoreStatus != model.TaskRunStatusSuccess ||
			evidence.VerifyStatus != model.TaskRunStatusSuccess ||
			(evidence.PostVerifyStatus != model.TaskRunStatusSuccess && evidence.PostVerifyStatus != model.TaskRunStatusSkipped) ||
			(evidence.CleanupStatus != model.TaskRunStatusSuccess && evidence.CleanupStatus != model.TaskRunStatusSkipped) ||
			!evidence.ConfidenceEligible || strings.TrimSpace(evidence.FailedStep) != "") {
		return false, fmt.Errorf("successful restore drill task run has incomplete active evidence")
	}

	finishedAt := run.FinishedAt.UTC()
	updates := map[string]interface{}{
		"status":               run.Status,
		"started_at":           run.StartedAt,
		"finished_at":          &finishedAt,
		"duration_ms":          run.DurationMs,
		"recovery_owner_id":    "",
		"recovery_lease_until": nil,
		"updated_at":           finishedAt,
	}
	if run.Status != model.TaskRunStatusSuccess {
		updates["confidence_eligible"] = false
		message := sanitizeTaskLastError(run.LastError)
		if message == "" {
			message = "演习终态已从 TaskRun 恢复"
		}
		addRecoveredDrillTerminalPhase(updates, evidence, run.Status, message, finishedAt)
	}
	result := tx.Model(&model.RestoreDrillEvidence{}).
		Where("id = ? AND task_run_id = ? AND task_id = ? AND status IN ?",
			evidence.ID, run.ID, run.TaskID, model.TaskRunActiveStatuses()).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, nil
	}
	return true, nil
}

func addRecoveredDrillTerminalPhase(
	updates map[string]interface{},
	evidence model.RestoreDrillEvidence,
	status string,
	message string,
	finishedAt time.Time,
) {
	step := activeDrillEvidenceStep(evidence)
	updates["failed_step"] = step
	switch step {
	case "startup":
		updates["restore_status"] = status
		updates["restore_finished_at"] = &finishedAt
		updates["restore_error"] = message
		updates["verify_status"] = model.TaskRunStatusSkipped
		updates["post_verify_status"] = model.TaskRunStatusSkipped
		updates["cleanup_status"] = model.TaskRunStatusSkipped
	case "sandbox_precheck", "restore", "restore_path":
		updates["restore_status"] = status
		updates["restore_finished_at"] = &finishedAt
		updates["restore_error"] = message
	case "pre_verify", "verify":
		updates["verify_status"] = status
		updates["verify_finished_at"] = &finishedAt
		updates["verify_error"] = message
	case "post_verify":
		updates["post_verify_status"] = status
		updates["post_verify_finished_at"] = &finishedAt
		updates["post_verify_error"] = message
	case "cleanup_boundary", "cleanup":
		updates["cleanup_status"] = status
		updates["cleanup_finished_at"] = &finishedAt
		updates["cleanup_error"] = message
	}
}

func repairActiveDrillRunFromTerminalEvidenceTx(
	tx *gorm.DB,
	run model.TaskRun,
	evidence model.RestoreDrillEvidence,
) (bool, error) {
	if !model.IsTerminalTaskRunStatus(evidence.Status) || evidence.FinishedAt == nil || evidence.DurationMs < 0 {
		return false, fmt.Errorf("terminal restore drill evidence is incomplete")
	}
	if evidence.Status == model.TaskRunStatusSuccess {
		if evidence.RestoreStatus != model.TaskRunStatusSuccess ||
			evidence.VerifyStatus != model.TaskRunStatusSuccess ||
			(evidence.PostVerifyStatus != model.TaskRunStatusSuccess && evidence.PostVerifyStatus != model.TaskRunStatusSkipped) ||
			(evidence.CleanupStatus != model.TaskRunStatusSuccess && evidence.CleanupStatus != model.TaskRunStatusSkipped) ||
			!evidence.ConfidenceEligible || strings.TrimSpace(evidence.FailedStep) != "" {
			return false, fmt.Errorf("successful restore drill evidence phases are incomplete")
		}
	} else if evidence.ConfidenceEligible {
		return false, fmt.Errorf("non-success restore drill evidence is confidence eligible")
	}
	startedAt := evidence.StartedAt
	if startedAt == nil && run.Status != model.TaskRunStatusPending {
		startedAt = run.StartedAt
	}
	expectedDuration := int64(0)
	if startedAt != nil {
		expectedDuration = drillDurationMs(startedAt.UTC(), evidence.FinishedAt.UTC())
	}
	if evidence.DurationMs != expectedDuration {
		return false, fmt.Errorf("terminal restore drill evidence duration is inconsistent")
	}
	lastError := ""
	if evidence.Status != model.TaskRunStatusSuccess {
		lastError = terminalDrillEvidenceError(evidence)
		if lastError == "" {
			lastError = "演习进程中断，已自动恢复"
		}
	}
	result := tx.Model(&model.TaskRun{}).
		Where("id = ? AND task_id = ? AND trigger_type = ? AND status = ?",
			run.ID, run.TaskID, "drill", run.Status).
		Updates(map[string]interface{}{
			"status":      evidence.Status,
			"started_at":  startedAt,
			"finished_at": evidence.FinishedAt,
			"duration_ms": evidence.DurationMs,
			"last_error":  sanitizeTaskLastError(lastError),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func terminalDrillEvidenceError(evidence model.RestoreDrillEvidence) string {
	for _, message := range []string{
		evidence.CleanupError,
		evidence.PostVerifyError,
		evidence.VerifyError,
		evidence.RestoreError,
	} {
		if strings.TrimSpace(message) != "" {
			return sanitizeTaskLastError(message)
		}
	}
	return ""
}

func synthesizeInterruptedDrillEvidenceTx(tx *gorm.DB, run model.TaskRun) error {
	var taskEntity model.Task
	result := tx.Where("id = ?", run.TaskID).Limit(1).Find(&taskEntity)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("interrupted restore drill task is unavailable")
	}
	policyID := uint(0)
	sandboxNodeID := taskEntity.NodeID
	sandboxNodeName := ""
	sandboxPath := "/tmp/xirang-drill"
	if taskEntity.PolicyID != nil {
		var policy model.Policy
		if err := tx.Where("id = ?", *taskEntity.PolicyID).Limit(1).Find(&policy).Error; err != nil {
			return err
		}
		policyID = policy.ID
		if policy.DrillTargetNodeID != nil && *policy.DrillTargetNodeID != 0 {
			sandboxNodeID = *policy.DrillTargetNodeID
		}
		if strings.TrimSpace(policy.DrillRestorePath) != "" {
			sandboxPath = strings.TrimSpace(policy.DrillRestorePath)
		}
	}
	if sandboxNodeID != 0 {
		var sandbox model.Node
		if err := tx.Select("id", "name").Where("id = ?", sandboxNodeID).Limit(1).Find(&sandbox).Error; err != nil {
			return err
		}
		sandboxNodeName = sandbox.Name
	}
	evidence := model.RestoreDrillEvidence{
		PolicyID:           policyID,
		TaskID:             run.TaskID,
		TaskRunID:          run.ID,
		SandboxNodeID:      sandboxNodeID,
		SandboxNodeName:    sandboxNodeName,
		SandboxPath:        sandboxPath,
		Status:             model.TaskRunStatusPending,
		ConfidenceEligible: false,
		StartedAt:          run.StartedAt,
		RestoreStatus:      model.TaskRunStatusPending,
		VerifyStatus:       model.TaskRunStatusSkipped,
		PostVerifyStatus:   model.TaskRunStatusSkipped,
		CleanupStatus:      model.TaskRunStatusSkipped,
	}
	return tx.Create(&evidence).Error
}

func (m *Manager) releaseRecoveredDrillOwnership(taskID, runID uint) {
	value, ok := m.pendingRuns.Load(taskID)
	if !ok {
		return
	}
	ownership, ok := value.(*pendingRunOwnership)
	if !ok {
		return
	}
	handedOffRunID, handedOff := ownership.handedOffDrillRunID()
	if !handedOff || handedOffRunID != runID || !ownership.claimDrillRecoveryCleanup(runID) {
		return
	}
	m.chainRunner.Delete(taskID)
	m.pendingRuns.CompareAndDelete(taskID, ownership)
}

// findTaskForPolicy 查找策略关联的任务（优先取有成功记录的，否则取第一个 rsync 任务）。
// allowedSourceNodeIDs 非 nil 时仅在这些节点上查找（operator 授权边界）。
func (m *Manager) findTaskForPolicy(policyID uint, allowedSourceNodeIDs []uint) (model.Task, error) {
	if allowedSourceNodeIDs != nil && len(allowedSourceNodeIDs) == 0 {
		return model.Task{}, fmt.Errorf("没有可演练的已授权备份任务")
	}

	q := m.db.Preload("Node").Preload("Node.SSHKey").Preload("Policy").
		Where("policy_id = ?", policyID).
		Where("executor_type IN ?", []string{"rsync", "restic", "rclone"})
	if allowedSourceNodeIDs != nil {
		q = q.Where("node_id IN ?", allowedSourceNodeIDs)
	}

	var tasks []model.Task
	if err := q.Find(&tasks).Error; err != nil {
		return model.Task{}, fmt.Errorf("查询关联任务失败: %w", err)
	}

	if len(tasks) == 0 {
		if allowedSourceNodeIDs != nil {
			return model.Task{}, fmt.Errorf("没有可演练的已授权备份任务")
		}
		return model.Task{}, fmt.Errorf("该策略没有关联的备份任务")
	}

	// 优先取有成功记录的任务
	for _, t := range tasks {
		var count int64
		if err := m.db.Model(&model.TaskRun{}).
			Where("task_id = ? AND node_id_snapshot = ? AND status = ?", t.ID, t.NodeID, model.TaskRunStatusSuccess).
			Count(&count).Error; err != nil {
			return model.Task{}, fmt.Errorf("查询任务成功执行记录失败: %w", err)
		}
		if count > 0 {
			return t, nil
		}
	}

	// 没有成功记录，取第一个
	return tasks[0], nil
}

// executeDrill 执行演习的核心流程：恢复备份到沙箱 → 执行校验脚本 → 清理。
func (m *Manager) executeDrill(policy *model.Policy, task model.Task, sandboxNode model.Node, drillRunID uint) {
	parent := m.rootCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	m.executeDrillWithContext(ctx, policy, task, sandboxNode, drillRunID, nil, nil)
}

func copyDrillUpdates(updates map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(updates)+5)
	for key, value := range updates {
		result[key] = value
	}
	return result
}

func activeDrillEvidenceStep(evidence model.RestoreDrillEvidence) string {
	switch {
	case evidence.CleanupStatus == model.TaskRunStatusRunning:
		return "cleanup"
	case evidence.PostVerifyStatus == model.TaskRunStatusRunning:
		return "post_verify"
	case evidence.VerifyStatus == model.TaskRunStatusRunning:
		return "verify"
	case evidence.RestoreStatus == model.TaskRunStatusRunning:
		return "restore"
	case evidence.RestoreStatus == model.TaskRunStatusPending:
		return "startup"
	default:
		return "finalize"
	}
}

func addDrillCanceledPhase(
	updates map[string]interface{},
	evidence model.RestoreDrillEvidence,
	message string,
	finishedAt time.Time,
) {
	step, _ := updates["failed_step"].(string)
	if strings.TrimSpace(step) == "" {
		step = activeDrillEvidenceStep(evidence)
		updates["failed_step"] = step
	}
	switch step {
	case "startup":
		updates["restore_status"] = model.TaskRunStatusCanceled
		updates["restore_finished_at"] = &finishedAt
		updates["restore_error"] = message
		updates["verify_status"] = model.TaskRunStatusSkipped
		updates["post_verify_status"] = model.TaskRunStatusSkipped
		updates["cleanup_status"] = model.TaskRunStatusSkipped
	case "sandbox_precheck", "restore", "restore_path":
		updates["restore_status"] = model.TaskRunStatusCanceled
		updates["restore_finished_at"] = &finishedAt
		updates["restore_error"] = message
	case "pre_verify", "verify":
		updates["verify_status"] = model.TaskRunStatusCanceled
		updates["verify_finished_at"] = &finishedAt
		updates["verify_error"] = message
	case "post_verify":
		updates["post_verify_status"] = model.TaskRunStatusCanceled
		updates["post_verify_finished_at"] = &finishedAt
		updates["post_verify_error"] = message
	case "cleanup_boundary", "cleanup":
		updates["cleanup_status"] = model.TaskRunStatusCanceled
		updates["cleanup_finished_at"] = &finishedAt
		updates["cleanup_error"] = message
	}
}

func (m *Manager) startDrillRun(
	drillRunID uint,
	evidence model.RestoreDrillEvidence,
	startedAt time.Time,
) (bool, error) {
	return m.retryDrillTransition(func() (bool, error) {
		return m.startDrillRunOnce(drillRunID, evidence, startedAt)
	})
}

func (m *Manager) startDrillRunOnce(
	drillRunID uint,
	evidence model.RestoreDrillEvidence,
	startedAt time.Time,
) (bool, error) {
	startedAt = startedAt.UTC()
	started := false
	if m.nodeWriteAdmission == nil {
		return false, ErrNodeWriteUnavailable
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := m.nodeWriteAdmission.EnterDrillExecutionTx(
			context.Background(), tx, drillRunID, evidence.SandboxNodeID, startedAt,
		); err != nil {
			if errors.Is(err, ErrNodeWriteStartLost) {
				return nil
			}
			return err
		}

		var pending model.RestoreDrillEvidence
		evidenceResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_run_id = ?", drillRunID).Limit(1).Find(&pending)
		if evidenceResult.Error != nil {
			return evidenceResult.Error
		}
		if evidenceResult.RowsAffected == 0 {
			// Compatibility for rows created before the atomic reservation contract
			// and for focused runner tests that seed only the TaskRun.
			evidence.TaskRunID = drillRunID
			evidence.Status = model.TaskRunStatusRunning
			evidence.StartedAt = &startedAt
			if err := tx.Create(&evidence).Error; err != nil {
				return err
			}
		} else {
			if pending.SandboxNodeID != evidence.SandboxNodeID || pending.TaskID != evidence.TaskID {
				return ErrNodeWriteStartLost
			}
			if pending.Status != model.TaskRunStatusPending {
				return fmt.Errorf("pending restore drill evidence is unavailable")
			}
			if pending.RecoveryOwnerID != "" &&
				(pending.RecoveryOwnerID != m.drillOwnerID || pending.RecoveryLeaseUntil == nil ||
					!pending.RecoveryLeaseUntil.After(time.Now().UTC())) {
				return fmt.Errorf("pending restore drill lease is unavailable")
			}
			updates := map[string]interface{}{
				"status":     model.TaskRunStatusRunning,
				"started_at": &startedAt,
			}
			updated := tx.Model(&model.RestoreDrillEvidence{}).
				Where("id = ? AND task_run_id = ? AND status = ?", pending.ID, drillRunID, model.TaskRunStatusPending).
				Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return fmt.Errorf("restore drill evidence start transition lost")
			}
		}
		started = true
		return nil
	})
	return started, err
}

func (m *Manager) retryDrillTransition(operation func() (bool, error)) (bool, error) {
	wait := m.nodeWriteRetryWait
	if wait == nil {
		wait = waitForNodeWriteReservationRetry
	}
	var lastErr error
	for attempt := 0; attempt < drillTerminalTransitionAttempts; attempt++ {
		won, err := operation()
		if err == nil {
			return won, nil
		}
		lastErr = err
		if !retryableNodeWriteReservationError(err) {
			return false, err
		}
		if attempt+1 == drillTerminalTransitionAttempts {
			break
		}
		if err := wait(context.Background(), attempt); err != nil {
			return false, err
		}
	}
	return false, lastErr
}

func (m *Manager) drillRunTerminal(drillRunID uint) (bool, error) {
	var run model.TaskRun
	result := m.db.Select("id", "status").
		Where("id = ? AND trigger_type = ?", drillRunID, "drill").
		Limit(1).Find(&run)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, fmt.Errorf("restore drill task run is unavailable")
	}
	return model.IsTerminalTaskRunStatus(run.Status), nil
}

func (m *Manager) settleUnstartedDrill(taskID, drillRunID uint, message string) error {
	won, err := m.retryDrillTransition(func() (bool, error) {
		return m.cancelOneDrillRun(taskID, drillRunID, message, time.Now().UTC())
	})
	if err != nil {
		return err
	}
	if won {
		return nil
	}
	terminal, err := m.drillRunTerminal(drillRunID)
	if err != nil {
		return err
	}
	if !terminal {
		return fmt.Errorf("restore drill startup terminal transition lost")
	}
	return nil
}

func (m *Manager) updateDrillEvidence(drillRunID uint, updates map[string]interface{}) error {
	updates = copyDrillUpdates(updates)
	now := time.Now().UTC()
	updates["updated_at"] = now
	result := m.db.Model(&model.RestoreDrillEvidence{}).
		Where(`task_run_id = ? AND status = ?
			AND (recovery_owner_id = '' OR
				(recovery_owner_id = ? AND recovery_lease_until > ?))`,
			drillRunID, model.TaskRunStatusRunning, m.drillOwnerID, now).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("persist restore drill phase evidence: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("restore drill phase evidence transition lost")
	}
	return nil
}

func (m *Manager) finalizeDrillRun(
	drillRunID uint,
	status string,
	lastError string,
	finishedAt time.Time,
	evidenceUpdates map[string]interface{},
) (bool, int64, error) {
	if !model.IsTerminalTaskRunStatus(status) {
		return false, 0, fmt.Errorf("invalid restore drill terminal status")
	}
	finishedAt = finishedAt.UTC()
	lastError = sanitizeTaskLastError(lastError)
	wait := m.nodeWriteRetryWait
	if wait == nil {
		wait = waitForNodeWriteReservationRetry
	}
	var lastErr error
	for attempt := 0; attempt < drillTerminalTransitionAttempts; attempt++ {
		won, durationMs, err := m.finalizeDrillRunOnce(drillRunID, status, lastError, finishedAt, evidenceUpdates)
		if err == nil {
			return won, durationMs, nil
		}
		lastErr = err
		if !retryableNodeWriteReservationError(err) {
			return false, 0, err
		}
		if attempt+1 == drillTerminalTransitionAttempts {
			break
		}
		if err := wait(context.Background(), attempt); err != nil {
			return false, 0, err
		}
	}
	return false, 0, lastErr
}

func (m *Manager) finalizeDrillRunOnce(
	drillRunID uint,
	status string,
	lastError string,
	finishedAt time.Time,
	evidenceUpdates map[string]interface{},
) (bool, int64, error) {
	won := false
	durationMs := int64(0)
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var run model.TaskRun
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND trigger_type = ? AND status = ?", drillRunID, "drill", model.TaskRunStatusRunning).
			Limit(1).Find(&run)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if run.StartedAt == nil {
			return fmt.Errorf("active restore drill task run has no start time")
		}

		var evidence model.RestoreDrillEvidence
		now := time.Now().UTC()
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(`task_run_id = ? AND status = ?
				AND (recovery_owner_id = '' OR
					(recovery_owner_id = ? AND recovery_lease_until > ?))`,
				drillRunID, model.TaskRunStatusRunning, m.drillOwnerID, now).
			Limit(1).Find(&evidence)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("active restore drill evidence is unavailable")
		}
		if status == model.TaskRunStatusSuccess {
			confidenceEligible, eligibleOK := evidenceUpdates["confidence_eligible"].(bool)
			if evidence.RestoreStatus != model.TaskRunStatusSuccess ||
				evidence.VerifyStatus != model.TaskRunStatusSuccess ||
				(evidence.PostVerifyStatus != model.TaskRunStatusSuccess && evidence.PostVerifyStatus != model.TaskRunStatusSkipped) ||
				(evidence.CleanupStatus != model.TaskRunStatusSuccess && evidence.CleanupStatus != model.TaskRunStatusSkipped) ||
				!eligibleOK || !confidenceEligible {
				return fmt.Errorf("restore drill success evidence phases are incomplete")
			}
		}

		durationMs = drillDurationMs(run.StartedAt.UTC(), finishedAt)
		runResult := tx.Model(&model.TaskRun{}).
			Where("id = ? AND trigger_type = ? AND status = ?", drillRunID, "drill", model.TaskRunStatusRunning).
			Updates(map[string]interface{}{
				"status":      status,
				"finished_at": &finishedAt,
				"duration_ms": durationMs,
				"last_error":  lastError,
			})
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected != 1 {
			return nil
		}

		updates := copyDrillUpdates(evidenceUpdates)
		if status == model.TaskRunStatusCanceled {
			addDrillCanceledPhase(updates, evidence, lastError, finishedAt)
		}
		updates["status"] = status
		updates["finished_at"] = &finishedAt
		updates["duration_ms"] = durationMs
		updates["recovery_owner_id"] = ""
		updates["recovery_lease_until"] = nil
		updates["updated_at"] = finishedAt
		evidenceResult := tx.Model(&model.RestoreDrillEvidence{}).
			Where(`id = ? AND task_run_id = ? AND status = ?
				AND (recovery_owner_id = '' OR
					(recovery_owner_id = ? AND recovery_lease_until > ?))`,
				evidence.ID, drillRunID, model.TaskRunStatusRunning, m.drillOwnerID, now).
			Updates(updates)
		if evidenceResult.Error != nil {
			return evidenceResult.Error
		}
		if evidenceResult.RowsAffected != 1 {
			return fmt.Errorf("restore drill evidence terminal transition lost")
		}
		won = true
		return nil
	})
	return won, durationMs, err
}

func (m *Manager) executeDrillWithContext(
	ctx context.Context,
	policy *model.Policy,
	task model.Task,
	sandboxNode model.Node,
	drillRunID uint,
	ownership *pendingRunOwnership,
	runCancel context.CancelFunc,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	executionCtx, cancelExecution := context.WithCancel(ctx)
	ctx = executionCtx
	defer cancelExecution()
	if runCancel != nil {
		defer runCancel()
	}
	drillCompleted := false
	releaseOwnership := true
	if ownership != nil {
		defer func() {
			if !drillCompleted {
				releaseOwnership = false
			}
			if releaseOwnership {
				m.chainRunner.Delete(task.ID)
				m.pendingRuns.CompareAndDelete(task.ID, ownership)
				return
			}
			// A concurrent Cancel may have durably terminalized the run after the
			// runner exhausted startup compensation. In that case the runner can
			// safely release ownership itself. Otherwise it records an explicit
			// recovery handoff; a later Cancel verifies the durable terminal row
			// before removing the process-local trigger barrier.
			ownership.handoffDrillRecovery(drillRunID)
			terminal, err := m.drillRunTerminal(drillRunID)
			if err == nil && terminal && ownership.claimDrillRecoveryCleanup(drillRunID) {
				m.chainRunner.Delete(task.ID)
				m.pendingRuns.CompareAndDelete(task.ID, ownership)
			}
		}()
	}
	stopLeaseHeartbeat := m.startDrillLeaseHeartbeat(ctx, drillRunID, cancelExecution)
	defer stopLeaseHeartbeat()
	runIDPtr := &drillRunID
	restorePath := strings.TrimSpace(policy.DrillRestorePath)
	if restorePath == "" {
		restorePath = "/tmp/xirang-drill"
	}
	sourceRunID, err := m.latestSuccessfulRunID(task.ID, task.NodeID)
	if err != nil {
		logger.Module("task").Warn().Uint("task_id", task.ID).Err(err).Msg("查询恢复演练来源执行记录失败")
	}
	snapshotRef := ""
	if sourceRunID != nil {
		snapshotRef = fmt.Sprintf("task_run:%d", *sourceRunID)
	}
	leaseDuration := m.drillRecoveryLease
	if leaseDuration <= 0 {
		leaseDuration = defaultDrillRecoveryLease
	}
	leaseUntil := time.Now().UTC().Add(leaseDuration)

	evidence := model.RestoreDrillEvidence{
		PolicyID:           policy.ID,
		TaskID:             task.ID,
		TaskRunID:          drillRunID,
		SourceTaskRunID:    sourceRunID,
		SnapshotRef:        snapshotRef,
		SandboxNodeID:      sandboxNode.ID,
		SandboxNodeName:    sandboxNode.Name,
		SandboxPath:        restorePath,
		Status:             model.TaskRunStatusRunning,
		FailedStep:         "",
		ConfidenceEligible: false,
		RestoreStatus:      model.TaskRunStatusPending,
		VerifyStatus:       model.TaskRunStatusPending,
		PostVerifyStatus:   model.TaskRunStatusSkipped,
		CleanupStatus:      model.TaskRunStatusSkipped,
		RecoveryOwnerID:    m.drillOwnerID,
		RecoveryLeaseUntil: &leaseUntil,
	}
	finalizeRun := func(
		status string,
		lastError string,
		finishedAt time.Time,
		updates map[string]interface{},
	) (bool, int64, error) {
		won, durationMs, err := m.finalizeDrillRun(drillRunID, status, lastError, finishedAt, updates)
		if err != nil {
			logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("原子终结恢复演练失败")
		}
		return won, durationMs, err
	}

	cancelDrill := func(step string) {
		if drillCompleted {
			return
		}
		finishedAt := time.Now()
		const canceledMessage = "演习已取消"
		updates := map[string]interface{}{
			"failed_step":         step,
			"confidence_eligible": false,
		}
		won, _, finalizeErr := finalizeRun(model.TaskRunStatusCanceled, canceledMessage, finishedAt, updates)
		drillCompleted = finalizeErr == nil
		if won {
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "warn", canceledMessage, model.TaskRunStatusCanceled)
		}
	}
	checkCanceled := func(step string) bool {
		if ctx.Err() == nil {
			return false
		}
		cancelDrill(step)
		return true
	}

	writePhaseAudit := func(phase string, node model.Node, outcome string, startedAt time.Time, err error, metadata map[string]any) {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["phase"] = phase
		metadata["duration_ms"] = drillDurationMs(startedAt, time.Now())
		metadata["sandbox_node_id"] = sandboxNode.ID
		if sourceRunID != nil {
			metadata["source_task_run_id"] = *sourceRunID
		}
		m.writeDrillPhaseCredentialAudit(policy, task, node, drillRunID, outcome, err, metadata)
	}
	failDrill := func(step string, err error, alertCode string, messagePrefix string) {
		if ctx.Err() != nil {
			cancelDrill(step)
			return
		}
		finishedAt := time.Now()
		errorMsg := messagePrefix
		if err != nil {
			errorMsg = fmt.Sprintf("%s: %v", messagePrefix, err)
		}
		sanitizedErr := sanitizeTaskLastError(errorMsg)
		updates := map[string]interface{}{
			"failed_step":         step,
			"confidence_eligible": false,
		}
		switch step {
		case "sandbox_precheck", "restore":
			updates["restore_status"] = model.TaskRunStatusFailed
			updates["restore_finished_at"] = &finishedAt
			updates["restore_error"] = sanitizedErr
		case "pre_verify", "verify":
			updates["verify_status"] = model.TaskRunStatusFailed
			updates["verify_finished_at"] = &finishedAt
			updates["verify_error"] = sanitizedErr
		case "post_verify":
			updates["post_verify_status"] = model.TaskRunStatusFailed
			updates["post_verify_finished_at"] = &finishedAt
			updates["post_verify_error"] = sanitizedErr
		case "restore_path":
			updates["restore_status"] = model.TaskRunStatusFailed
			updates["restore_finished_at"] = &finishedAt
			updates["restore_error"] = sanitizedErr
		case "cleanup_boundary", "cleanup":
			updates["cleanup_status"] = model.TaskRunStatusFailed
			updates["cleanup_finished_at"] = &finishedAt
			updates["cleanup_error"] = sanitizedErr
		}
		won, _, finalizeErr := finalizeRun(model.TaskRunStatusFailed, sanitizedErr, finishedAt, updates)
		drillCompleted = finalizeErr == nil
		if won {
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "error", sanitizedErr, "")
			_ = m.alertDispatcher.RaiseDrillFailure(policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID, alertCode, sanitizedErr)
			m.dispatchDrillFailure(policy.ID, drillRunID)
		}
	}
	persistEvidence := func(step string, updates map[string]interface{}) bool {
		if err := m.updateDrillEvidence(drillRunID, updates); err != nil {
			logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("更新恢复演练证据失败")
			failDrill(step, nil, "drill_evidence_persist_failed", "演习阶段证据保存失败")
			return false
		}
		return true
	}

	// Ensure an unexpected return still attempts one atomic terminal CAS. A
	// terminal transaction that committed first remains authoritative.
	defer func() {
		if !drillCompleted {
			if ctx.Err() != nil {
				cancelDrill("unknown")
				return
			}
			finishedAt := time.Now()
			_, _, finalizeErr := finalizeRun(model.TaskRunStatusFailed, "演习终态保存失败", finishedAt, map[string]interface{}{
				"failed_step":         "finalize",
				"confidence_eligible": false,
			})
			drillCompleted = finalizeErr == nil
		}
	}()

	// The pending -> running transition and Evidence creation share one
	// transaction. If cancellation won first, no orphan Evidence can be created.
	if ctx.Err() != nil {
		if err := m.settleUnstartedDrill(task.ID, drillRunID, "演习已取消"); err != nil {
			logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("取消尚未启动的恢复演练失败")
			releaseOwnership = false
		}
		drillCompleted = true
		return
	}
	now := time.Now().UTC()
	started, startErr := m.startDrillRun(drillRunID, evidence, now)
	if startErr != nil {
		logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(startErr).Msg("原子启动恢复演练失败")
		if err := m.settleUnstartedDrill(task.ID, drillRunID, "演习启动失败"); err != nil {
			logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("补偿未启动的恢复演练失败")
			releaseOwnership = false
		}
		drillCompleted = true
		return
	}
	if !started {
		terminal, terminalErr := m.drillRunTerminal(drillRunID)
		if terminalErr != nil || !terminal {
			if err := m.settleUnstartedDrill(task.ID, drillRunID, "演习启动状态冲突"); err != nil {
				logger.Module("task").Warn().Uint("task_run_id", drillRunID).Err(err).Msg("恢复演练启动状态补偿失败")
				releaseOwnership = false
			}
		}
		drillCompleted = true
		return
	}

	m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "开始恢复演练", "")

	if err := validateDrillSandboxPath(restorePath); err != nil {
		failDrill("restore_path", err, "drill_restore_path_invalid", "演习恢复路径无效")
		return
	}
	if checkCanceled("restore_path") {
		return
	}

	// ---- Step 0: 沙箱节点连通性预检 ----
	restoreStartedAt := time.Now()
	if !persistEvidence("restore", map[string]interface{}{
		"restore_status":     "running",
		"restore_started_at": &restoreStartedAt,
	}) {
		return
	}
	m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "正在检查沙箱节点连通性...", "")
	precheckCtx := m.drillAuditContext(ctx, policy, task, sandboxNode, drillRunID, "sandbox_precheck", map[string]any{"has_script": false})
	if err := m.runDrillScript(precheckCtx, sandboxNode, "true"); err != nil {
		writePhaseAudit("sandbox_precheck", sandboxNode, credentialaudit.OutcomeFailure, restoreStartedAt, err, map[string]any{"has_script": false})
		failDrill("sandbox_precheck", err, "drill_sandbox_unreachable", fmt.Sprintf("沙箱节点 (%s) 不可达", sandboxNode.Name))
		return
	}
	if checkCanceled("sandbox_precheck") {
		return
	}
	writePhaseAudit("sandbox_precheck", sandboxNode, credentialaudit.OutcomeSuccess, restoreStartedAt, nil, map[string]any{"has_script": false})
	m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "沙箱节点连通性检查通过", "")

	// ---- Step 1: 恢复备份到沙箱 ----
	m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "正在恢复备份到沙箱节点...", "")
	restoreCtx := m.drillAuditContext(ctx, policy, task, sandboxNode, drillRunID, "restore", map[string]any{"path_hash": hashDrillPath(restorePath)})
	restoreErr := m.runDrillRestore(restoreCtx, task, sandboxNode, restorePath, func(level, msg string) {
		if runIDPtr != nil {
			m.logDispatcher.Dispatch(task.ID, runIDPtr, level, msg, "")
		}
	})
	restoreFinishedAt := time.Now()
	if checkCanceled("restore") {
		return
	}
	if restoreErr != nil {
		if strings.Contains(restoreErr.Error(), "跨节点传输已禁用") {
			writePhaseAudit("cross_node_transfer", sandboxNode, credentialaudit.OutcomeBlocked, restoreStartedAt, restoreErr, map[string]any{
				"path_hash":      hashDrillPath(restorePath),
				"source_node_id": task.NodeID,
			})
		}
		writePhaseAudit("restore", sandboxNode, credentialaudit.OutcomeFailure, restoreStartedAt, restoreErr, map[string]any{"path_hash": hashDrillPath(restorePath)})
		failDrill("restore", restoreErr, "drill_restore_failed", "恢复备份到沙箱失败")
		return
	}
	if checkCanceled("restore") {
		return
	}
	writePhaseAudit("restore", sandboxNode, credentialaudit.OutcomeSuccess, restoreStartedAt, nil, map[string]any{"path_hash": hashDrillPath(restorePath)})
	if !persistEvidence("restore", map[string]interface{}{
		"restore_status":      "success",
		"restore_finished_at": &restoreFinishedAt,
		"restore_error":       "",
	}) {
		return
	}
	m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "备份恢复至沙箱完成", "")
	if checkCanceled("restore") {
		return
	}

	// ---- Step 2: 执行校验脚本 ----
	verifyStartedAt := time.Now()
	if !persistEvidence("verify", map[string]interface{}{
		"verify_status":     "running",
		"verify_started_at": &verifyStartedAt,
	}) {
		return
	}
	verifyFailed := false
	failedStep := ""
	var verifyErr error

	// pre_verify
	if strings.TrimSpace(policy.DrillPreVerify) != "" {
		m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "执行 pre_verify 脚本", "")
		phaseStartedAt := time.Now()
		phaseCtx := m.drillAuditContext(ctx, policy, task, sandboxNode, drillRunID, "pre_verify", map[string]any{"has_script": true})
		verifyErr = m.runDrillScript(phaseCtx, sandboxNode, policy.DrillPreVerify)
		if checkCanceled("pre_verify") {
			return
		}
		if verifyErr != nil {
			writePhaseAudit("pre_verify", sandboxNode, credentialaudit.OutcomeFailure, phaseStartedAt, verifyErr, map[string]any{"has_script": true})
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "error", "pre_verify 失败: "+sanitizeTaskLastError(verifyErr.Error()), "")
			verifyFailed = true
			failedStep = "pre_verify"
		} else {
			writePhaseAudit("pre_verify", sandboxNode, credentialaudit.OutcomeSuccess, phaseStartedAt, nil, map[string]any{"has_script": true})
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "pre_verify 成功", "")
		}
	}

	// verify（仅在 pre_verify 成功时执行）
	if !verifyFailed && strings.TrimSpace(policy.DrillVerify) != "" {
		m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "执行 verify 脚本", "")
		phaseStartedAt := time.Now()
		phaseCtx := m.drillAuditContext(ctx, policy, task, sandboxNode, drillRunID, "verify", map[string]any{"has_script": true})
		verifyErr = m.runDrillScript(phaseCtx, sandboxNode, policy.DrillVerify)
		if checkCanceled("verify") {
			return
		}
		if verifyErr != nil {
			writePhaseAudit("verify", sandboxNode, credentialaudit.OutcomeFailure, phaseStartedAt, verifyErr, map[string]any{"has_script": true})
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "error", "verify 失败: "+sanitizeTaskLastError(verifyErr.Error()), "")
			verifyFailed = true
			failedStep = "verify"
		} else {
			writePhaseAudit("verify", sandboxNode, credentialaudit.OutcomeSuccess, phaseStartedAt, nil, map[string]any{"has_script": true})
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "verify 成功", "")
		}
	}

	if checkCanceled("verify") {
		return
	}
	verifyFinishedAt := time.Now()
	if verifyFailed {
		finalError := "pre_verify 执行失败"
		if failedStep == "verify" {
			finalError = "演习校验失败"
		}
		failDrill(failedStep, verifyErr, "drill_verify_failed", finalError)
		return
	}
	if checkCanceled("verify") {
		return
	}
	if !persistEvidence("verify", map[string]interface{}{
		"verify_status":      "success",
		"verify_finished_at": &verifyFinishedAt,
		"verify_error":       "",
	}) {
		return
	}

	// post_verify（无论成败都执行；失败会使演练不可作为正向可信证据）
	postVerifyStatus := "skipped"
	postVerifyError := ""
	if strings.TrimSpace(policy.DrillPostVerify) != "" {
		m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "执行 post_verify 脚本", "")
		phaseStartedAt := time.Now()
		phaseCtx := m.drillAuditContext(ctx, policy, task, sandboxNode, drillRunID, "post_verify", map[string]any{"has_script": true})
		err := m.runDrillScript(phaseCtx, sandboxNode, policy.DrillPostVerify)
		if checkCanceled("post_verify") {
			return
		}
		if err != nil {
			writePhaseAudit("post_verify", sandboxNode, credentialaudit.OutcomeFailure, phaseStartedAt, err, map[string]any{"has_script": true})
			postVerifyStatus = "failed"
			postVerifyError = sanitizeTaskLastError(err.Error())
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "error", "post_verify 失败: "+postVerifyError, "")
		} else {
			writePhaseAudit("post_verify", sandboxNode, credentialaudit.OutcomeSuccess, phaseStartedAt, nil, map[string]any{"has_script": true})
			postVerifyStatus = "success"
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "post_verify 成功", "")
		}
		postVerifyFinishedAt := time.Now()
		if checkCanceled("post_verify") {
			return
		}
		if !persistEvidence("post_verify", map[string]interface{}{
			"post_verify_status":      postVerifyStatus,
			"post_verify_finished_at": &postVerifyFinishedAt,
			"post_verify_error":       postVerifyError,
		}) {
			return
		}
	}
	if checkCanceled("post_verify") {
		return
	}

	// ---- Step 3: 自动清理 ----
	cleanupStatus := "skipped"
	cleanupError := ""
	var cleanupStartedAt *time.Time
	var cleanupFinishedAt *time.Time
	if policy.DrillAutoCleanup {
		started := time.Now()
		cleanupStartedAt = &started
		cleanupStatus = "running"
		if !persistEvidence("cleanup", map[string]interface{}{
			"cleanup_status":     cleanupStatus,
			"cleanup_started_at": cleanupStartedAt,
		}) {
			return
		}
		if checkCanceled("cleanup") {
			return
		}
		m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "执行自动清理", "")
		if err := validateDrillSandboxPath(restorePath); err != nil {
			cleanupStatus = "failed"
			cleanupError = sanitizeTaskLastError("清理路径不在演习沙箱安全边界内: " + err.Error())
			writePhaseAudit("cleanup", sandboxNode, credentialaudit.OutcomeBlocked, started, err, map[string]any{"path_hash": hashDrillPath(restorePath), "auto_cleanup": true})
			m.logDispatcher.Dispatch(task.ID, runIDPtr, "error", cleanupError, "")
		} else {
			cleanupCmd := fmt.Sprintf("rm -rf %s", executor.ShellEscape(restorePath))
			cleanupCtx := m.drillAuditContext(ctx, policy, task, sandboxNode, drillRunID, "cleanup", map[string]any{"path_hash": hashDrillPath(restorePath), "auto_cleanup": true})
			err := m.runDrillScript(cleanupCtx, sandboxNode, cleanupCmd)
			if checkCanceled("cleanup") {
				return
			}
			if err != nil {
				cleanupStatus = "failed"
				cleanupError = sanitizeTaskLastError(err.Error())
				writePhaseAudit("cleanup", sandboxNode, credentialaudit.OutcomeFailure, started, err, map[string]any{"path_hash": hashDrillPath(restorePath), "auto_cleanup": true})
				m.logDispatcher.Dispatch(task.ID, runIDPtr, "error", "清理失败: "+cleanupError, "")
			} else {
				cleanupStatus = "success"
				writePhaseAudit("cleanup", sandboxNode, credentialaudit.OutcomeSuccess, started, nil, map[string]any{"path_hash": hashDrillPath(restorePath), "auto_cleanup": true})
				m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", "清理完成", "")
			}
		}
		if checkCanceled("cleanup") {
			return
		}
		finished := time.Now()
		cleanupFinishedAt = &finished
		if !persistEvidence("cleanup", map[string]interface{}{
			"cleanup_status":      cleanupStatus,
			"cleanup_finished_at": cleanupFinishedAt,
			"cleanup_error":       cleanupError,
		}) {
			return
		}
	}
	if checkCanceled("cleanup") {
		return
	}

	// ---- Step 4: 记录结果 ----
	if checkCanceled("finalize") {
		return
	}
	finishedAt := time.Now()

	finalStatus := "success"
	finalError := ""
	failedStep = ""
	confidenceEligible := true
	if postVerifyStatus == "failed" {
		finalStatus = "failed"
		finalError = "演习 post_verify 失败: " + postVerifyError
		failedStep = "post_verify"
		confidenceEligible = false
	}
	if cleanupStatus == "failed" {
		finalStatus = "failed"
		finalError = "演习清理失败: " + cleanupError
		failedStep = "cleanup"
		confidenceEligible = false
	}

	won, duration, finalizeErr := finalizeRun(finalStatus, finalError, finishedAt, map[string]interface{}{
		"failed_step":         failedStep,
		"confidence_eligible": confidenceEligible,
	})
	drillCompleted = finalizeErr == nil
	if finalizeErr != nil || !won {
		return
	}

	if finalStatus == "failed" {
		if postVerifyStatus == "failed" {
			_ = m.alertDispatcher.RaiseDrillFailure(policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID, "drill_post_verify_failed", finalError)
		}
		if cleanupStatus == "failed" {
			_ = m.alertDispatcher.RaiseDrillFailure(policy.ID, policy.Name, sandboxNode.Name, sandboxNode.ID, "drill_cleanup_failed", finalError)
		}
		m.dispatchDrillFailure(policy.ID, drillRunID)
	}

	rtoSeconds := float64(duration) / 1000.0
	m.logDispatcher.Dispatch(task.ID, runIDPtr, "info", fmt.Sprintf(
		"恢复演练完成: status=%s, RTO=%.1fs",
		finalStatus, rtoSeconds), "")
}

func (m *Manager) drillAuditContext(ctx context.Context, policy *model.Policy, task model.Task, node model.Node, runID uint, phase string, metadata map[string]any) context.Context {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["phase"] = phase
	metadata["sandbox_node_id"] = node.ID
	metadata["trigger_type"] = "drill"
	metadata["executor_type"] = strings.ToLower(strings.TrimSpace(task.ExecutorType))
	event := credentialaudit.Event{
		Username:  "system",
		Role:      "system",
		Action:    "task.credential.use",
		Purpose:   sshutil.PurposeDrill,
		NodeID:    credentialaudit.PtrUint(node.ID),
		TaskID:    credentialaudit.PtrUint(task.ID),
		TaskRunID: credentialaudit.PtrUint(runID),
		Metadata:  metadata,
	}
	if policy != nil {
		event.PolicyID = credentialaudit.PtrUint(policy.ID)
	}
	return credentialaudit.WithRuntimeContext(ctx, m.db, event)
}

func (m *Manager) writeDrillPhaseCredentialAudit(policy *model.Policy, task model.Task, node model.Node, runID uint, outcome string, err error, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["trigger_type"] = "drill"
	metadata["executor_type"] = strings.ToLower(strings.TrimSpace(task.ExecutorType))
	event := credentialaudit.Event{
		Username:         "system",
		Role:             "system",
		Action:           "drill.phase",
		Purpose:          sshutil.PurposeDrill,
		CredentialKind:   "node_credential",
		CredentialSource: safeDrillCredentialSource(node),
		SSHKeyID:         node.SSHKeyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		TaskID:           credentialaudit.PtrUint(task.ID),
		TaskRunID:        credentialaudit.PtrUint(runID),
		Outcome:          outcome,
		Metadata:         metadata,
	}
	if policy != nil {
		event.PolicyID = credentialaudit.PtrUint(policy.ID)
	}
	if err != nil {
		event.ErrorMessage = sanitizeTaskLastError(err.Error())
	}
	if writeErr := credentialaudit.Write(m.db, event); writeErr != nil {
		logger.Module("credential_audit").Warn().Err(writeErr).
			Str("action", event.Action).
			Str("purpose", event.Purpose).
			Msg("恢复演练阶段凭据审计事件写入失败")
	}
}

func safeDrillCredentialSource(node model.Node) string {
	if node.SSHKeyID != nil && *node.SSHKeyID != 0 {
		return fmt.Sprintf("ssh_key_id=%d", *node.SSHKeyID)
	}
	switch strings.ToLower(strings.TrimSpace(node.AuthType)) {
	case "password":
		return "node.password"
	case "key":
		return "node.private_key"
	default:
		return "node.credential"
	}
}

func hashDrillPath(path string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(path)))
	return hex.EncodeToString(sum[:])
}

func drillDurationMs(startedAt, finishedAt time.Time) int64 {
	duration := finishedAt.Sub(startedAt).Milliseconds()
	if duration < 0 {
		return 0
	}
	return duration
}

func (m *Manager) latestSuccessfulRunID(taskID, nodeID uint) (*uint, error) {
	return m.latestSuccessfulRunIDWithContext(context.Background(), taskID, nodeID)
}

func (m *Manager) latestSuccessfulRunIDWithContext(ctx context.Context, taskID, nodeID uint) (*uint, error) {
	if !model.IsTaskRunNodeSnapshotAuthoritative(nodeID) {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var run model.TaskRun
	err := m.db.WithContext(ctx).Select("id").
		Where("task_id = ? AND node_id_snapshot = ? AND status = ?", taskID, nodeID, model.TaskRunStatusSuccess).
		Order("finished_at desc, id desc").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run.ID, nil
}

func (m *Manager) runDrillScript(ctx context.Context, node model.Node, script string) error {
	if m.drillSSHScriptFunc != nil {
		return m.drillSSHScriptFunc(ctx, node, script)
	}
	return m.runDrillSSHScript(ctx, node, script)
}

func (m *Manager) runDrillRestore(ctx context.Context, srcTask model.Task, sandboxNode model.Node, drillPath string, logf func(string, string)) error {
	if m.drillRestoreFunc != nil {
		return m.drillRestoreFunc(ctx, srcTask, sandboxNode, drillPath, logf)
	}
	return m.restoreBackupToSandbox(ctx, srcTask, sandboxNode, drillPath, logf)
}

// restoreBackupToSandbox 将备份恢复到沙箱节点。
// 当前安全基线禁用旧跨节点传输路径，因此默认实现会在任何远端写操作前失败。
func (m *Manager) restoreBackupToSandbox(ctx context.Context, srcTask model.Task, sandboxNode model.Node, drillPath string, logf func(string, string)) error {
	if err := validateDrillCrossNodeTransferAllowed(srcTask.Node, sandboxNode); err != nil {
		return err
	}

	// Step A: 在源节点上将备份数据恢复到临时路径
	tempPath := fmt.Sprintf("/tmp/xirang-drill-src-%d", time.Now().UnixNano())
	logf("info", "在源节点恢复到临时路径")

	// 构建恢复任务（源→临时路径）
	restoreTask := srcTask
	restoreTask.RsyncSource = srcTask.RsyncTarget // 备份目的地作为源
	restoreTask.RsyncTarget = tempPath            // 临时恢复路径

	// 同步执行恢复
	if err := m.executeSyncRestore(ctx, restoreTask, logf); err != nil {
		// 清理临时目录
		_ = m.runDrillSSHScript(ctx, srcTask.Node, fmt.Sprintf("rm -rf %s", executor.ShellEscape(tempPath)))
		return fmt.Errorf("恢复备份到临时目录失败: %w", err)
	}

	// Step B: 将恢复的数据从源节点传输到沙箱节点
	logf("info", "传输恢复数据到沙箱节点")

	if err := m.transferFilesToSandbox(ctx, srcTask.Node, tempPath, sandboxNode, drillPath, logf); err != nil {
		// 清理两边的临时文件；沙箱路径必须再次通过边界校验后才允许 rm -rf。
		_ = m.runDrillSSHScript(ctx, srcTask.Node, fmt.Sprintf("rm -rf %s", executor.ShellEscape(tempPath)))
		if validateErr := validateDrillSandboxPath(drillPath); validateErr == nil {
			_ = m.runDrillSSHScript(ctx, sandboxNode, fmt.Sprintf("rm -rf %s", executor.ShellEscape(drillPath)))
		} else {
			logger.Module("task").Warn().Err(validateErr).Msg("跳过恢复演练传输失败后的沙箱清理：路径不在安全边界内")
		}
		return fmt.Errorf("传输文件到沙箱失败: %w", err)
	}

	// 清理源节点临时目录
	_ = m.runDrillSSHScript(ctx, srcTask.Node, fmt.Sprintf("rm -rf %s", executor.ShellEscape(tempPath)))

	return nil
}

// executeSyncRestore 在源节点上同步执行备份恢复。
func (m *Manager) executeSyncRestore(ctx context.Context, restoreTask model.Task, logf func(string, string)) error {
	// 确保远程目标路径可用
	if err := executor.EnsureRemoteTargetReadyForPurpose(ctx, restoreTask.Node, restoreTask.RsyncTarget, sshutil.PurposeDrill); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("恢复前检查已取消")
		}
		return fmt.Errorf("恢复前检查失败（目标路径）: %w", err)
	}

	logf("info", "开始执行源节点临时恢复")

	// 使用 RunSSHCommandOutput 执行 rsync 恢复（复用 runRemoteRestore 的核心逻辑）
	client, err := executor.DialSSHForNodePurpose(ctx, restoreTask.Node, sshutil.PurposeDrill)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	rsyncBin := "rsync"
	if executor.NeedsSudo(restoreTask.Node) {
		rsyncBin = "sudo rsync"
	}
	rsyncCmd := fmt.Sprintf("%s -avz --info=progress2 -- %s %s",
		rsyncBin,
		executor.ShellEscape(restoreTask.RsyncSource),
		executor.ShellEscape(restoreTask.RsyncTarget))

	logf("info", "执行 rsync 恢复命令")
	output, err := executor.RunSSHCommandOutput(ctx, client, rsyncCmd)
	if err != nil {
		return fmt.Errorf("恢复执行失败: %s", sanitizeTaskLastError(err.Error()+", 输出: "+output))
	}

	return nil
}

// validateDrillCrossNodeTransferAllowed 阻断旧的恢复演练跨节点传输路径。
// 旧实现会把源节点 SSH 私钥写入沙箱文件系统，并在沙箱上关闭主机密钥校验后
// 执行 rsync pull。在安全的凭据代理/中转设计落地前，这里必须在任何远端
// 写操作前失败，避免演练把源节点凭据扩散到沙箱节点。
func validateDrillCrossNodeTransferAllowed(_ model.Node, _ model.Node) error {
	return fmt.Errorf("恢复演练跨节点传输已禁用：当前安全基线禁止将源节点 SSH 凭据写入沙箱节点，请等待后续安全传输实现后再启用该演练")
}

// transferFilesToSandbox 将源节点上的文件传输到沙箱节点。
func (m *Manager) transferFilesToSandbox(_ context.Context, srcNode model.Node, _ string, dstNode model.Node, dstPath string, _ func(string, string)) error {
	if err := validateDrillSandboxPath(dstPath); err != nil {
		return fmt.Errorf("沙箱目标路径不安全: %w", err)
	}
	return validateDrillCrossNodeTransferAllowed(srcNode, dstNode)
}

// runDrillSSHScript 在指定节点上通过 SSH 执行一段脚本命令。
func (m *Manager) runDrillSSHScript(ctx context.Context, node model.Node, script string) error {
	client, err := executor.DialSSHForNodePurpose(ctx, node, sshutil.PurposeDrill)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer client.Close() //nolint:errcheck

	output, err := executor.RunSSHCommandOutput(ctx, client, script)
	if err != nil {
		return fmt.Errorf("演练脚本执行失败: %s", sanitizeTaskLastError(err.Error()+", 输出: "+output))
	}
	return nil
}

// ============================================================================
// Drill Scheduler
// ============================================================================

// StartDrillLoop 启动演习 cron 扫描循环。
func (m *Manager) StartDrillLoop() {
	m.drillLoopMu.Lock()
	if m.drillLoopCancel != nil {
		m.drillLoopMu.Unlock()
		return
	}
	parent := m.rootCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.drillLoopCancel = cancel
	m.drillLoopWG.Add(1)
	m.drillLoopMu.Unlock()

	go func() {
		defer m.drillLoopWG.Done()
		m.drillLoopWithContext(ctx)
	}()
	logger.Module("task").Info().Msg("演习调度循环已启动")
}

// stopDrillLoop cancels and joins the scheduler before the rest of Manager
// shutdown waits for active drill runs.
func (m *Manager) stopDrillLoop(ctx context.Context) error {
	m.drillLoopMu.Lock()
	cancel := m.drillLoopCancel
	m.drillLoopMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() {
		m.drillLoopWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// drillLoop 定期扫描启用了 drill 的 Policy，匹配 cron 触发演习。
func (m *Manager) drillLoopWithContext(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runDrillScan()
		}
	}
}

// runDrillScan 扫描数据库，触发 cron 到期的演习策略。
func (m *Manager) runDrillScan() {
	if !m.DrillAvailable() {
		return
	}
	var policies []model.Policy
	if err := m.db.Where("drill_enabled = ?", true).Find(&policies).Error; err != nil {
		logger.Module("task").Warn().Err(err).Msg("drill scan: 查询策略失败")
		return
	}

	now := time.Now()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

	for _, p := range policies {
		if strings.TrimSpace(p.DrillCron) == "" {
			continue
		}

		sched, err := parser.Parse(p.DrillCron)
		if err != nil {
			logger.Module("task").Warn().
				Uint("policy_id", p.ID).
				Str("drill_cron", p.DrillCron).
				Err(err).Msg("drill scan: 解析 cron 表达式失败")
			continue
		}

		// 检查 cron 是否在当前 tick 窗口内触发（匹配上次扫描到现在之间的时间点）
		nextRun := sched.Next(now.Add(-61 * time.Second))
		if nextRun.Before(now) || nextRun.Equal(now) {
			logger.Module("task").Info().
				Uint("policy_id", p.ID).
				Str("policy_name", p.Name).
				Msg("触发恢复演练 (cron)")

			if _, err := m.TriggerDrill(p.ID, nil); err != nil {
				logger.Module("task").Warn().
					Uint("policy_id", p.ID).
					Err(err).Msg("触发恢复演练失败")
			}
		}
	}
}
