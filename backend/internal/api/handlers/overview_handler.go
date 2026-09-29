package handlers

import (
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type OverviewHandler struct {
	db *gorm.DB
}

func NewOverviewHandler(db *gorm.DB) *OverviewHandler {
	return &OverviewHandler{db: db}
}

// Get godoc
// @Summary      获取总览数据
// @Description  返回当前身份可见的启用策略数
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /overview [get]
func (h *OverviewHandler) Get(c *gin.Context) {
	// User-scoped (operator ownership filters apply). Never allow shared
	// caches/CDN to reuse one user's overview for another.
	c.Header("Cache-Control", "private, no-store")

	ownedIDs, needFilter, err := ownershipNodeFilter(c, h.db)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	var activePolicies int64
	if needFilter {
		// Operators can only see enabled policies attached to their owned nodes.
		// An empty ownership set must remain empty rather than becoming unfiltered.
		if len(ownedIDs) > 0 {
			if err := h.db.Model(&model.Policy{}).
				Where("enabled = ? AND id IN (SELECT policy_id FROM policy_nodes WHERE node_id IN ?)", true, ownedIDs).
				Count(&activePolicies).Error; err != nil {
				respondInternalError(c, err)
				return
			}
		}
	} else {
		// Admins and viewers can see every enabled policy.
		if err := h.db.Model(&model.Policy{}).Where("enabled = ?", true).Count(&activePolicies).Error; err != nil {
			respondInternalError(c, err)
			return
		}
	}

	respondOK(c, gin.H{
		"activePolicies": activePolicies,
	})
}
