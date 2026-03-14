package middleware

import (
	"net/http"
	"strconv"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// CurrentUserID 从 gin 上下文获取当前登录用户 ID。
func CurrentUserID(c *gin.Context) uint {
	v, _ := c.Get(CtxUserID)
	id, _ := v.(uint)
	return id
}

// OwnershipNodeCheck 对象级 ownership 中间件——仅对 operator 角色生效。
// 从路径参数 :id 读取节点 ID，校验当前 operator 是否为该节点的 owner。
// admin 和 viewer 不受约束，直接放行。
func OwnershipNodeCheck(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := CurrentRole(c)
		if role == "admin" || role == "viewer" {
			c.Next()
			return
		}
		if role != "operator" {
			c.JSON(http.StatusForbidden, gin.H{"error": "权限不足"})
			c.Abort()
			return
		}

		nodeIDStr := c.Param("id")
		nodeID, err := strconv.ParseUint(nodeIDStr, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的节点 ID"})
			c.Abort()
			return
		}

		userID := CurrentUserID(c)
		var count int64
		if err := db.Model(&model.NodeOwner{}).
			Where("node_id = ? AND user_id = ?", nodeID, userID).
			Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
			c.Abort()
			return
		}
		if count == 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该节点"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// OwnershipTaskCheck operator 通过 task :id 访问时，校验任务所属节点是否为当前 operator 的 owner。
func OwnershipTaskCheck(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := CurrentRole(c)
		if role == "admin" || role == "viewer" {
			c.Next()
			return
		}
		if role != "operator" {
			c.JSON(http.StatusForbidden, gin.H{"error": "权限不足"})
			c.Abort()
			return
		}

		taskIDStr := c.Param("id")
		taskID, err := strconv.ParseUint(taskIDStr, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的任务 ID"})
			c.Abort()
			return
		}

		userID := CurrentUserID(c)
		// 通过 task → node_id → node_owners 联查
		var count int64
		if err := db.Table("tasks").
			Joins("JOIN node_owners ON node_owners.node_id = tasks.node_id").
			Where("tasks.id = ? AND node_owners.user_id = ?", taskID, userID).
			Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
			c.Abort()
			return
		}
		if count == 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该任务"})
			c.Abort()
			return
		}
		c.Next()
	}
}
