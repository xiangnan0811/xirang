package handlers

import (
	"context"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SnapshotDiffHandler 处理 restic 快照差异比较。
type SnapshotDiffHandler struct {
	db     *gorm.DB
	guard  publication.LineageGuard
	runner SnapshotDiffRunner
}

type SnapshotDiffRunner interface {
	RunSnapshotDiff(context.Context, model.Task, string, string) (string, error)
}

func NewSnapshotDiffHandler(db *gorm.DB, guard publication.LineageGuard, runner SnapshotDiffRunner) *SnapshotDiffHandler {
	return &SnapshotDiffHandler{db: db, guard: guard, runner: runner}
}

// Diff godoc
// @Summary      比较快照差异
// @Description  比较两个 restic 快照之间的文件变更差异
// @Tags         snapshots
// @Security     Bearer
// @Produce      json
// @Param        id     path      int     true  "任务 ID"
// @Param        snap1  query     string  true  "快照 ID 1"
// @Param        snap2  query     string  true  "快照 ID 2"
// @Success      410  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots/diff [get]
func (h *SnapshotDiffHandler) Diff(c *gin.Context) {
	respondLegacySnapshotReadRetired(c)
}
