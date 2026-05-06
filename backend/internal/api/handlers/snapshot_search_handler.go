package handlers

import (
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/snapshot"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SnapshotSearchHandler 处理跨快照文件搜索请求。
type SnapshotSearchHandler struct {
	db *gorm.DB
}

func NewSnapshotSearchHandler(db *gorm.DB) *SnapshotSearchHandler {
	return &SnapshotSearchHandler{db: db}
}

// SearchResult 搜索结果条目。
type SearchResult struct {
	SnapshotID string `json:"snapshot_id"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Mtime      string `json:"mtime"`
}

// Search godoc
// @Summary      搜索快照文件
// @Description  在 restic 快照的文件索引中搜索匹配路径的文件。首次搜索会触发后台索引构建。
// @Tags         snapshots
// @Security     Bearer
// @Produce      json
// @Param        id  path      int     true  "任务 ID"
// @Param        q   query     string  true  "搜索关键词"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots/search [get]
func (h *SnapshotSearchHandler) Search(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}

	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		respondBadRequest(c, "q 参数不能为空")
		return
	}

	var task model.Task
	if err := h.db.First(&task, taskID).Error; err != nil {
		respondNotFound(c, "任务不存在")
		return
	}
	if task.ExecutorType != "restic" {
		respondBadRequest(c, "仅 restic 类型任务支持快照搜索")
		return
	}

	// 确保索引已构建或触发构建
	ready, err := snapshot.EnsureIndexed(c.Request.Context(), h.db, taskID)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if !ready {
		respondOK(c, gin.H{"status": "indexing", "message": "首次搜索，正在构建索引，请稍后重试"})
		return
	}

	// 执行搜索（参数化绑定，防 SQL 注入）
	escapedPattern := "%" + snapshot.EscapeLikePattern(q) + "%"
	var results []SearchResult
	if err := h.db.Model(&model.SnapshotFileIndex{}).
		Select("DISTINCT snapshot_id, path, size, mtime").
		Where("task_id = ? AND path LIKE ? ESCAPE '\\'", taskID, escapedPattern).
		Limit(200).
		Find(&results).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondOK(c, results)
}
