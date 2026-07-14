package handlers

import (
	"strings"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/snapshot"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SnapshotSearchHandler 处理跨快照文件搜索请求。
type SnapshotSearchHandler struct {
	db      *gorm.DB
	guard   publication.LineageGuard
	indexer *snapshot.Indexer
}

func NewSnapshotSearchHandler(db *gorm.DB, guard publication.LineageGuard, indexer *snapshot.Indexer) *SnapshotSearchHandler {
	return &SnapshotSearchHandler{db: db, guard: guard, indexer: indexer}
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
	if h.guard == nil || h.indexer == nil {
		respondServiceUnavailable(c, "备份资产运行时不可用")
		return
	}
	session, err := h.guard.Begin(c.Request.Context(), task.ID, publication.OperationLegacySearch)
	if err != nil || session == nil {
		respondForbidden(c, "快照搜索当前不可用")
		return
	}
	defer func() { _ = session.Close() }()

	// 确保索引已构建或触发构建
	ready, err := h.indexer.EnsureIndexed(c.Request.Context(), taskID, session)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if !ready {
		respondOK(c, gin.H{"status": "indexing", "message": "首次搜索，正在构建索引，请稍后重试"})
		return
	}

	// 执行搜索（参数化绑定，防 SQL 注入）。Exact mode is restricted to
	// committed IDs and independently requires the completion marker, so a
	// historical/partial cache row can never become search truth.
	escapedPattern := "%" + snapshot.EscapeLikePattern(q) + "%"
	var results []SearchResult
	query := h.db.WithContext(c.Request.Context()).Model(&model.SnapshotFileIndex{}).
		Select("DISTINCT snapshot_id, path, size, mtime").
		Where("task_id = ? AND path LIKE ? ESCAPE '\\'", taskID, escapedPattern)
	if session.Mode() == publication.LineageExact {
		points := session.CommittedPoints()
		ids := make([]string, 0, len(points))
		for _, point := range points {
			ids = append(ids, strings.ToLower(strings.TrimSpace(point.FullNativeID)))
		}
		if len(ids) == 0 {
			respondOK(c, []SearchResult{})
			return
		}
		query = query.Where("snapshot_id IN ? AND path <> ''", ids).
			Where("EXISTS (SELECT 1 FROM snapshot_file_indices AS completion WHERE completion.task_id = snapshot_file_indices.task_id AND completion.snapshot_id = snapshot_file_indices.snapshot_id AND completion.path = ? AND completion.mtime = ?)", "", "xirang-index-complete-v1")
	}
	if err := query.Limit(200).Find(&results).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondOK(c, results)
}
