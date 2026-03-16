package handlers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StorageUsageHandler 提供存储用量概览。
type StorageUsageHandler struct {
	db *gorm.DB
}

func NewStorageUsageHandler(db *gorm.DB) *StorageUsageHandler {
	return &StorageUsageHandler{db: db}
}

type mountPointInfo struct {
	Path    string  `json:"path"`
	UsedGB  float64 `json:"used_gb"`
	TotalGB float64 `json:"total_gb"`
	Pct     float64 `json:"pct"`
}

type perNodeUsage struct {
	NodeID   uint    `json:"node_id"`
	NodeName string  `json:"node_name"`
	Path     string  `json:"path"`
	UsedGB   float64 `json:"used_gb"`
}

// Get 收集本地备份目标路径的存储用量。
// GET /overview/storage-usage
func (h *StorageUsageHandler) Get(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	// 收集所有策略的目标路径
	var policies []model.Policy
	if err := h.db.Select("id, name, target_path").Find(&policies).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询策略失败"})
		return
	}

	// 去重挂载点
	mountPointSet := make(map[string]bool)
	targetPaths := make([]string, 0)
	for _, p := range policies {
		tp := strings.TrimSpace(p.TargetPath)
		if tp == "" || strings.Contains(tp, ":") {
			// 跳过远程路径（如 s3:bucket/path）
			continue
		}
		if !mountPointSet[tp] {
			mountPointSet[tp] = true
			targetPaths = append(targetPaths, tp)
		}
	}

	mountPoints := make([]mountPointInfo, 0)
	for _, tp := range targetPaths {
		if ctx.Err() != nil {
			break
		}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(tp, &stat); err != nil {
			continue
		}
		totalGB := float64(stat.Blocks) * float64(stat.Bsize) / (1024 * 1024 * 1024)
		freeGB := float64(stat.Bavail) * float64(stat.Bsize) / (1024 * 1024 * 1024)
		usedGB := totalGB - freeGB
		pct := 0.0
		if totalGB > 0 {
			pct = usedGB / totalGB * 100
		}
		mountPoints = append(mountPoints, mountPointInfo{
			Path:    tp,
			UsedGB:  round2(usedGB),
			TotalGB: round2(totalGB),
			Pct:     round2(pct),
		})
	}

	// 按节点统计目录大小
	perNode := make([]perNodeUsage, 0)
	for _, tp := range targetPaths {
		if ctx.Err() != nil {
			break
		}
		entries, err := os.ReadDir(tp)
		if err != nil {
			continue
		}
		// 查找该路径关联的策略及节点
		var policyIDs []uint
		for _, p := range policies {
			if strings.TrimSpace(p.TargetPath) == tp {
				policyIDs = append(policyIDs, p.ID)
			}
		}
		if len(policyIDs) == 0 {
			continue
		}
		// 获取关联节点
		type nodeRef struct {
			NodeID   uint
			NodeName string
		}
		var nodeRefs []nodeRef
		h.db.Raw("SELECT DISTINCT n.id as node_id, n.name as node_name FROM nodes n "+
			"INNER JOIN policy_nodes pn ON pn.node_id = n.id "+
			"WHERE pn.policy_id IN ?", policyIDs).Scan(&nodeRefs)

		nodeNameMap := make(map[string]nodeRef)
		for _, nr := range nodeRefs {
			nodeNameMap[nr.NodeName] = nr
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if nr, ok := nodeNameMap[entry.Name()]; ok {
				dirPath := filepath.Join(tp, entry.Name())
				sizeGB := dirSizeGB(dirPath)
				perNode = append(perNode, perNodeUsage{
					NodeID:   nr.NodeID,
					NodeName: nr.NodeName,
					Path:     dirPath,
					UsedGB:   round2(sizeGB),
				})
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"mount_points": mountPoints,
		"per_node":     perNode,
	})
}

func dirSizeGB(path string) float64 {
	var totalSize int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	return float64(totalSize) / (1024 * 1024 * 1024)
}

func round2(v float64) float64 {
	return float64(int(v*100)) / 100
}

// respondStorageError 用于内部错误响应（避免引用不存在的包级函数）。
func respondStorageError(c *gin.Context, msg string) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": msg})
}

// placeholder to suppress unused import warning
var _ = fmt.Sprintf
