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

// paginationParams 统一分页参数。
type paginationParams struct {
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	SortBy   string `json:"sort_by"`
	SortOrder string `json:"sort_order"`
}

// parsePagination 从查询参数中解析统一分页参数。
// 优先使用 page/page_size，向后兼容 limit/offset。
// defaultSort 为默认排序字段（如 "id"），allowedSorts 为允许的排序字段白名单。
func parsePagination(c *gin.Context, defaultPageSize int, defaultSort string, allowedSorts map[string]bool) paginationParams {
	pageSize := defaultPageSize
	page := 1

	// 优先使用新参数 page/page_size
	if raw := strings.TrimSpace(c.Query("page_size")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 500 {
			pageSize = v
		}
	} else if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		// 向后兼容 limit
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 500 {
			pageSize = v
		}
	}

	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			page = v
		}
	} else if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		// 向后兼容 offset → 转换为 page
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 && pageSize > 0 {
			page = v/pageSize + 1
		}
	}

	sortBy := defaultSort
	if raw := strings.TrimSpace(c.Query("sort_by")); raw != "" {
		if allowedSorts[raw] {
			sortBy = raw
		}
	}
	sortOrder := "desc"
	if raw := strings.TrimSpace(c.Query("sort_order")); raw == "asc" {
		sortOrder = "asc"
	}
	return paginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
}

// applyPagination 将分页参数应用到 GORM 查询。
func applyPagination(query *gorm.DB, p paginationParams) *gorm.DB {
	offset := (p.Page - 1) * p.PageSize
	return query.Order(p.SortBy + " " + p.SortOrder).Offset(offset).Limit(p.PageSize)
}

// paginatedResponse 统一分页响应。
func paginatedResponse(c *gin.Context, data interface{}, total int64, p paginationParams) {
	c.JSON(http.StatusOK, gin.H{
		"data":      data,
		"total":     total,
		"page":      p.Page,
		"page_size": p.PageSize,
	})
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
