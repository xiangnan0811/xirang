package handlers

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// snapshotIDPattern 校验快照 ID 格式（十六进制字符串，4-64 位）。
var snapshotIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{4,64}$`)

// dangerousRestorePaths 禁止恢复到的系统目录
var dangerousRestorePaths = []string{
	"/bin", "/sbin", "/usr", "/lib", "/lib64",
	"/boot", "/dev", "/proc", "/sys", "/run",
	"/etc", "/var/run",
}

// validateRestoreTargetPath 校验恢复目标路径安全性
func validateRestoreTargetPath(targetPath string) bool {
	cleaned := filepath.Clean(targetPath)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	if cleaned == "/" {
		return false
	}
	for _, prefix := range dangerousRestorePaths {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return false
		}
	}
	return true
}

// SnapshotHandler 处理 restic 快照浏览和恢复
type SnapshotHandler struct {
	db          *gorm.DB
	guard       publication.LineageGuard
	restic      LegacyResticSnapshots
	featureLive func() (bool, error)
}

// LegacyResticSnapshots is the narrow legacy command surface retained for
// pristine compatibility. Exact lineage checks remain in the handler/session
// and no caller may supply a raw snapshot ID to a Provider command in exact
// mode.
type LegacyResticSnapshots interface {
	ListSnapshots(context.Context, model.Task) ([]executor.ResticSnapshot, error)
	ListSnapshotsByLinkTag(context.Context, model.Task, string) ([]executor.ResticSnapshot, error)
	ListFiles(context.Context, model.Task, string, string) ([]executor.ResticEntry, error)
	RestoreFiles(context.Context, model.Task, string, []string, string) error
}

// NewSnapshotHandler receives the explicit runtime ports. Nil ports are
// accepted only for isolated route tests and fail closed before Provider I/O.
func NewSnapshotHandler(db *gorm.DB, guard publication.LineageGuard, restic LegacyResticSnapshots) *SnapshotHandler {
	return &SnapshotHandler{db: db, guard: guard, restic: restic}
}

func (h *SnapshotHandler) WithFeatureLive(featureLive func() (bool, error)) *SnapshotHandler {
	if h != nil {
		h.featureLive = featureLive
	}
	return h
}

// ListSnapshots godoc
// @Summary      列出快照
// @Description  列出 restic 类型任务的所有备份快照
// @Tags         snapshots
// @Security     Bearer
// @Produce      json
// @Param        id  path      int  true  "任务 ID"
// @Success      410  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots [get]
func (h *SnapshotHandler) ListSnapshots(c *gin.Context) {
	respondLegacySnapshotReadRetired(c)
}

// ListFiles godoc
// @Summary      列出快照文件
// @Description  列出指定 restic 快照中的文件和目录
// @Tags         snapshots
// @Security     Bearer
// @Produce      json
// @Param        id    path      int     true   "任务 ID"
// @Param        sid   path      string  true   "快照 ID"
// @Param        path  query     string  false  "目录路径（默认 /）"
// @Success      410  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots/{sid}/files [get]
func (h *SnapshotHandler) ListFiles(c *gin.Context) {
	respondLegacySnapshotReadRetired(c)
}

type restoreRequest struct {
	Includes   []string `json:"includes" binding:"required"`
	TargetPath string   `json:"targetPath" binding:"required"`
}

func snapshotRestoreAuditMetadata(stage, snapshotID string, includeCount int, targetSet bool) map[string]any {
	shortID := strings.TrimSpace(snapshotID)
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	return map[string]any{
		"stage":          stage,
		"include_count":  includeCount,
		"target_set":     targetSet,
		"snapshot_short": shortID,
	}
}

func (h *SnapshotHandler) writeSnapshotRestoreAudit(c *gin.Context, taskID uint, nodeID *uint, outcome, stage, snapshotID string, includeCount int, targetSet bool, err error) {
	event := credentialaudit.Event{
		Action:           "snapshot.restore",
		Purpose:          sshutil.PurposeSnapshot,
		CredentialKind:   "snapshot",
		CredentialSource: "restic_snapshot",
		TaskID:           credentialaudit.PtrUint(taskID),
		NodeID:           nodeID,
		Outcome:          outcome,
		Metadata:         snapshotRestoreAuditMetadata(stage, snapshotID, includeCount, targetSet),
	}
	if err != nil {
		event.ErrorMessage = credentialAuditSafeError(stage, err)
	}
	writeCredentialAuditFromGin(c, h.db, event)
}

// Restore godoc
// @Summary      恢复快照文件
// @Description  从指定 restic 快照恢复选定的文件到目标路径
// @Tags         snapshots
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int     true  "任务 ID"
// @Param        sid   path      string  true  "快照 ID"
// @Param        body  body      object  true  "恢复请求（includes 列表 + targetPath）"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots/{sid}/restore [post]
func (h *SnapshotHandler) Restore(c *gin.Context) {
	if !ensureLegacySnapshotRestoreLive(c, h.featureLive) {
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	snapshotID := c.Param("sid")
	if !snapshotIDPattern.MatchString(snapshotID) {
		respondBadRequest(c, "快照 ID 格式无效")
		return
	}

	var req restoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数无效")
		return
	}

	if !validateRestoreTargetPath(req.TargetPath) {
		h.writeSnapshotRestoreAudit(c, taskID, nil, credentialaudit.OutcomeBlocked, "target", snapshotID, len(req.Includes), strings.TrimSpace(req.TargetPath) != "", nil)
		respondBadRequest(c, "恢复目标路径不安全，不允许恢复到系统目录")
		return
	}

	var task model.Task
	if err := h.db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		respondNotFound(c, "任务不存在")
		return
	}
	nodeID := credentialaudit.PtrUint(task.NodeID)
	if task.ExecutorType != "restic" {
		h.writeSnapshotRestoreAudit(c, taskID, nodeID, credentialaudit.OutcomeBlocked, "executor", snapshotID, len(req.Includes), strings.TrimSpace(req.TargetPath) != "", nil)
		respondBadRequest(c, "仅 restic 类型任务支持快照恢复")
		return
	}

	session, ok := h.beginSnapshotLineage(c, task.ID, publication.OperationLegacySnapshotRestore)
	if !ok {
		return
	}
	defer func() { _ = session.Close() }()
	resolvedID, err := resolveLineageSnapshotID(session, snapshotID)
	if err != nil {
		h.writeSnapshotRestoreAudit(c, taskID, nodeID, credentialaudit.OutcomeBlocked, "lineage", snapshotID, len(req.Includes), strings.TrimSpace(req.TargetPath) != "", err)
		respondBadRequest(c, "快照 ID 不属于当前任务")
		return
	}
	if err := h.restic.RestoreFiles(c.Request.Context(), task, resolvedID, req.Includes, req.TargetPath); err != nil {
		h.writeSnapshotRestoreAudit(c, taskID, nodeID, credentialaudit.OutcomeFailure, "restore", snapshotID, len(req.Includes), strings.TrimSpace(req.TargetPath) != "", err)
		respondInternalError(c, err)
		return
	}
	h.writeSnapshotRestoreAudit(c, taskID, nodeID, credentialaudit.OutcomeSuccess, "restore", snapshotID, len(req.Includes), strings.TrimSpace(req.TargetPath) != "", nil)
	respondMessage(c, "恢复成功")
}

func (h *SnapshotHandler) beginSnapshotLineage(c *gin.Context, taskID uint, operation publication.ResticOperation) (publication.LineageSession, bool) {
	if h == nil || h.guard == nil || h.restic == nil {
		respondServiceUnavailable(c, "备份资产运行时不可用")
		return nil, false
	}
	session, err := h.guard.Begin(c.Request.Context(), taskID, operation)
	if err != nil {
		respondForbidden(c, "快照操作当前不可用")
		return nil, false
	}
	return session, true
}

func resolveLineageSnapshotID(session publication.LineageSession, snapshotID string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("lineage session unavailable")
	}
	if session.Mode() == publication.LineageCompatibility {
		return snapshotID, nil
	}
	return session.ResolveNativeID(strings.ToLower(snapshotID))
}

func filterCommittedResticSnapshots(snapshots []executor.ResticSnapshot, points []publication.CommittedPoint) []executor.ResticSnapshot {
	allowed := make(map[string]struct{}, len(points))
	for _, point := range points {
		allowed[strings.ToLower(point.FullNativeID)] = struct{}{}
	}
	filtered := make([]executor.ResticSnapshot, 0, len(snapshots))
	seen := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		fullID := strings.ToLower(strings.TrimSpace(snapshot.ID))
		if _, ok := allowed[fullID]; !ok {
			continue
		}
		if _, duplicate := seen[fullID]; duplicate {
			continue
		}
		seen[fullID] = struct{}{}
		filtered = append(filtered, snapshot)
	}
	return filtered
}
