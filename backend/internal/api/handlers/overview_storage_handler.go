package handlers

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"
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

// Get godoc
// @Summary      获取存储用量
// @Description  收集本地备份目标路径的挂载点用量和按节点分布统计
// @Tags         overview
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /overview/storage-usage [get]
func (h *StorageUsageHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	ownedIDs, needFilter, ownErr := ownershipNodeFilter(c, h.db)
	if ownErr != nil {
		respondInternalError(c, ownErr)
		return
	}
	if needFilter && len(ownedIDs) == 0 {
		respondOK(c, gin.H{"mount_points": []any{}, "per_node": []any{}})
		return
	}

	// 收集策略的目标路径（operator 仅关联 owned 节点的策略）
	var policies []model.Policy
	policyQ := h.db.Select("id, name, target_path")
	if needFilter {
		policyQ = policyQ.Where("id IN (SELECT policy_id FROM policy_nodes WHERE node_id IN ?)", ownedIDs)
	}
	if err := policyQ.Find(&policies).Error; err != nil {
		respondInternalError(c, err)
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

	// Mount-point totals reflect the whole volume. Operators must not learn
	// shared-disk capacity/usage outside their nodes — only admin/viewer get
	// Statfs aggregates; operators receive empty mount_points.
	mountPoints := make([]mountPointInfo, 0)
	if !needFilter {
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
	}

	perNode := make([]perNodeUsage, 0)
	// 按任务的实际物理目标统计目录大小。Task.RsyncTarget 是历史事实，
	// 因此旧路径保持可见；只有没有任务记录的策略才使用新的
	// policy-ID/node-ID 默认布局，绝不按 mutable BackupDir 反推旧数据。
	type taskTargetRef struct {
		NodeID uint
		Target string
	}
	var taskTargets []taskTargetRef
	taskQ := h.db.Model(&model.Task{}).Select("node_id, rsync_target").
		Where("rsync_target <> '' AND executor_type IN ?", []string{"rsync", "restic", "rclone"})
	if needFilter {
		taskQ = taskQ.Where("node_id IN ?", ownedIDs)
	}
	if err := taskQ.Find(&taskTargets).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	seenTaskTargets := make(map[string]struct{})
	for _, task := range taskTargets {
		target := strings.TrimSpace(task.Target)
		if target == "" || strings.Contains(target, ":") || !filepath.IsAbs(target) {
			continue
		}
		key := fmt.Sprintf("%d:%s", task.NodeID, target)
		if _, exists := seenTaskTargets[key]; exists {
			continue
		}
		seenTaskTargets[key] = struct{}{}
		perNode = append(perNode, perNodeUsage{
			NodeID: task.NodeID,
			Path:   target,
			UsedGB: round2(dirSizeGB(ctx, target)),
		})
	}

	// If a policy has no materialized task yet, expose its isolated new target
	// without treating the policy base path as a shared node directory.
	var policyNodes []struct {
		PolicyID uint
		Target   string
		NodeID   uint
		NodeName string
	}
	policyNodeQ := h.db.Table("policies p").
		Select("p.id AS policy_id, p.target_path AS target, n.id AS node_id, n.name AS node_name").
		Joins("JOIN policy_nodes pn ON pn.policy_id = p.id JOIN nodes n ON n.id = pn.node_id").
		Where("p.enabled = ? AND p.is_template = ?", true, false)
	if needFilter {
		policyNodeQ = policyNodeQ.Where("n.id IN ?", ownedIDs)
	}
	if err := policyNodeQ.Scan(&policyNodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	for _, candidate := range policyNodes {
		target := policy.PolicyNodeTargetPath(candidate.Target, candidate.PolicyID, candidate.NodeID)
		if target == "" {
			continue
		}
		key := fmt.Sprintf("%d:%s", candidate.NodeID, target)
		if _, exists := seenTaskTargets[key]; exists {
			continue
		}
		seenTaskTargets[key] = struct{}{}
		perNode = append(perNode, perNodeUsage{
			NodeID: candidate.NodeID, NodeName: candidate.NodeName,
			Path: target, UsedGB: round2(dirSizeGB(ctx, target)),
		})
	}

	respondOK(c, gin.H{
		"mount_points": mountPoints,
		"per_node":     perNode,
	})
}

func dirSizeGB(ctx context.Context, path string) float64 {
	var totalSize int64
	var fileCount int
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil {
				totalSize += info.Size()
			}
		}
		fileCount++
		if fileCount%1000 == 0 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return nil
	})
	return float64(totalSize) / (1024 * 1024 * 1024)
}

func round2(v float64) float64 {
	return float64(int(v*100)) / 100
}
