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
// @Summary      已退役的快照搜索入口
// @Description  此兼容入口始终返回 410，不搜索文件或触发旧索引构建。请使用备份资产 Search 搜索文件，或使用 Catalog 浏览恢复点。
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
