package handlers

import "github.com/gin-gonic/gin"

const legacySnapshotReadRetiredMessage = "遗留快照浏览接口已退役，请使用备份资产目录与搜索"
const legacySnapshotRestoreNotLiveMessage = "备份资产功能未启用"

func respondLegacySnapshotReadRetired(c *gin.Context) {
	respondGone(c, legacySnapshotReadRetiredMessage)
}

func ensureLegacySnapshotRestoreLive(c *gin.Context, featureLive func() (bool, error)) bool {
	if featureLive == nil {
		respondForbidden(c, legacySnapshotRestoreNotLiveMessage)
		return false
	}
	live, err := featureLive()
	if err != nil {
		respondServiceUnavailable(c, "备份资产运行时不可用")
		return false
	}
	if !live {
		respondForbidden(c, legacySnapshotRestoreNotLiveMessage)
		return false
	}
	return true
}
