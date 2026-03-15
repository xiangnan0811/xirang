package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// ownershipNodeFilter 返回当前 operator 拥有的节点 ID 列表。
// admin/viewer 返回 nil, false（无需过滤）。operator 返回 owned IDs, true。
func ownershipNodeFilter(c *gin.Context, db *gorm.DB) ([]uint, bool, error) {
	role := middleware.CurrentRole(c)
	if role == "admin" || role == "viewer" {
		return nil, false, nil
	}
	userID := middleware.CurrentUserID(c)
	ids, err := middleware.OwnedNodeIDs(db, userID)
	if err != nil {
		return nil, false, err
	}
	return ids, true, nil
}

// checkOwnershipByNodeID 检查 operator 是否拥有指定节点。
func checkOwnershipByNodeID(c *gin.Context, db *gorm.DB, nodeID uint) bool {
	role := middleware.CurrentRole(c)
	if role == "admin" || role == "viewer" {
		return true
	}
	userID := middleware.CurrentUserID(c)
	var count int64
	db.Table("node_owners").Where("node_id = ? AND user_id = ?", nodeID, userID).Count(&count)
	return count > 0
}

// checkOwnershipByPolicyNodes 检查 operator 是否拥有策略关联的任意节点（union 规则）。
// 策略已通过 Preload("Nodes") 加载节点列表。
func checkOwnershipByPolicyNodes(c *gin.Context, db *gorm.DB, p model.Policy) bool {
	role := middleware.CurrentRole(c)
	if role == "admin" || role == "viewer" {
		return true
	}
	if len(p.Nodes) == 0 {
		return false
	}
	userID := middleware.CurrentUserID(c)
	nodeIDs := make([]uint, len(p.Nodes))
	for i, n := range p.Nodes {
		nodeIDs[i] = n.ID
	}
	var count int64
	db.Table("node_owners").Where("user_id = ? AND node_id IN ?", userID, nodeIDs).Count(&count)
	return count > 0
}

var standardCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func parseID(c *gin.Context, field string) (uint, bool) {
	raw := c.Param(field)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID 格式错误"})
		return 0, false
	}
	return uint(id), true
}

func validateCronSpec(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if _, err := standardCronParser.Parse(trimmed); err != nil {
		return fmt.Errorf("cron 表达式不合法")
	}
	return nil
}

func parseCSVEnvList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, one := range parts {
		value := strings.TrimSpace(one)
		if value == "" {
			continue
		}
		result = append(result, filepath.Clean(value))
	}
	return result
}

func validatePathByPrefix(path string, prefixes []string, label string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("%s 不能为空", label)
	}
	if len(prefixes) == 0 {
		return nil
	}

	normalizedPath := filepath.Clean(trimmed)
	for _, prefix := range prefixes {
		normalizedPrefix := filepath.Clean(strings.TrimSpace(prefix))
		if normalizedPrefix == "." || normalizedPrefix == "" {
			continue
		}
		if normalizedPath == normalizedPrefix || strings.HasPrefix(normalizedPath, normalizedPrefix+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("%s 不在允许路径范围内", label)
}
