package handlers

import "github.com/gin-gonic/gin"

// SnapshotSearchHandler 处理已退役的跨快照文件搜索入口。
type SnapshotSearchHandler struct{}

func NewSnapshotSearchHandler() *SnapshotSearchHandler {
	return &SnapshotSearchHandler{}
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
