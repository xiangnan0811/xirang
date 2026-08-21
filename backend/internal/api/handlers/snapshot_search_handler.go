package handlers

import (
	"xirang/backend/internal/backupasset/publication"
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
// @Success      410  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots/search [get]
func (h *SnapshotSearchHandler) Search(c *gin.Context) {
	respondLegacySnapshotReadRetired(c)
}
