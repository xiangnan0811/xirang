package handlers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
)

// MigratePreflightRequest 迁移预检请求
type MigratePreflightRequest struct {
	TargetNodeID uint `json:"targetNodeId" binding:"required"`
}

// PreflightCheckItem 单项预检结果
type PreflightCheckItem struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass / fail / warn / skip
	Message string `json:"message"`
}

// PreflightNodeInfo 预检节点摘要
type PreflightNodeInfo struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Status      string `json:"status"`
	DiskUsedGB  int    `json:"diskUsedGb"`
	DiskTotalGB int    `json:"diskTotalGb"`
}

// PreflightPolicy 受影响的策略摘要
type PreflightPolicy struct {
	ID           uint   `json:"id"`
	Name         string `json:"name"`
	SourcePath   string `json:"sourcePath"`
	ExecutorType string `json:"executorType"`
}

// MigratePreflightResponse 迁移预检响应
type MigratePreflightResponse struct {
	SourceNode     PreflightNodeInfo    `json:"sourceNode"`
	TargetNode     PreflightNodeInfo    `json:"targetNode"`
	Policies       []PreflightPolicy    `json:"policies"`
	TaskCount      int                  `json:"taskCount"`
	Checks         []PreflightCheckItem `json:"checks"`
	CanProceed     bool                 `json:"canProceed"`
	DataMigratable bool                 `json:"dataMigratable"` // 是否有可迁移的本地备份数据
	DataSizeMB     int64                `json:"dataSizeMb"`     // 可迁移数据的估算大小(MB)
}

// MigratePreflight 执行迁移前的预检查
func (h *NodeHandler) MigratePreflight(c *gin.Context) {
	sourceID, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req MigratePreflightRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数无效")
		return
	}

	if sourceID == req.TargetNodeID {
		respondBadRequest(c, "源节点和目标节点不能相同")
		return
	}

	// 加载源节点和目标节点
	var sourceNode, targetNode model.Node
	if err := h.db.Preload("SSHKey").First(&sourceNode, sourceID).Error; err != nil {
		respondNotFound(c, "源节点不存在")
		return
	}
	if err := h.db.Preload("SSHKey").First(&targetNode, req.TargetNodeID).Error; err != nil {
		respondNotFound(c, "目标节点不存在")
		return
	}
	if targetNode.Archived {
		respondBadRequest(c, "目标节点已归档")
		return
	}
	if sourceNode.Archived {
		respondBadRequest(c, "源节点已归档")
		return
	}

	// operator 角色需对目标节点有 ownership
	if middleware.CurrentRole(c) == "operator" {
		userID := middleware.CurrentUserID(c)
		var count int64
		h.db.Model(&model.NodeOwner{}).Where("node_id = ? AND user_id = ?", req.TargetNodeID, userID).Count(&count)
		if count == 0 {
			respondForbidden(c, "无权操作该目标节点")
			return
		}
	}

	// 收集受影响的策略
	var policies []model.Policy
	h.db.Joins("JOIN policy_nodes ON policy_nodes.policy_id = policies.id").
		Where("policy_nodes.node_id = ?", sourceID).
		Find(&policies)

	// 收集受影响的任务（用于工具检测和计数）
	var policyIDs []uint
	for _, p := range policies {
		policyIDs = append(policyIDs, p.ID)
	}

	var tasks []model.Task
	if len(policyIDs) > 0 {
		h.db.Where("node_id = ? AND source = ? AND policy_id IN ?", sourceID, "policy", policyIDs).Find(&tasks)
	}

	// 去重收集 executor_type
	toolSet := make(map[string]struct{})
	for _, t := range tasks {
		if t.ExecutorType != "" && t.ExecutorType != "command" {
			toolSet[t.ExecutorType] = struct{}{}
		}
	}

	// 构建策略摘要
	policyInfos := make([]PreflightPolicy, 0, len(policies))
	policyExecutorType := make(map[uint]string) // policyID -> 主要 executor_type
	for _, t := range tasks {
		if t.PolicyID != nil {
			if _, exists := policyExecutorType[*t.PolicyID]; !exists {
				policyExecutorType[*t.PolicyID] = t.ExecutorType
			}
		}
	}
	for _, p := range policies {
		et := policyExecutorType[p.ID]
		if et == "" {
			et = "rsync"
		}
		policyInfos = append(policyInfos, PreflightPolicy{
			ID:           p.ID,
			Name:         p.Name,
			SourcePath:   p.SourcePath,
			ExecutorType: et,
		})
	}

	resp := MigratePreflightResponse{
		SourceNode: PreflightNodeInfo{
			ID: sourceNode.ID, Name: sourceNode.Name, Host: sourceNode.Host,
			Status: sourceNode.Status, DiskUsedGB: sourceNode.DiskUsedGB, DiskTotalGB: sourceNode.DiskTotalGB,
		},
		TargetNode: PreflightNodeInfo{
			ID: targetNode.ID, Name: targetNode.Name, Host: targetNode.Host,
			Status: targetNode.Status, DiskUsedGB: targetNode.DiskUsedGB, DiskTotalGB: targetNode.DiskTotalGB,
		},
		Policies:   policyInfos,
		TaskCount:  len(tasks),
		CanProceed: true,
	}

	var checks []PreflightCheckItem

	// === 检查 1: SSH 连通性 ===
	sshFailed := false
	probe, probeErr := sshutil.ProbeNode(targetNode, h.db)
	if probeErr != nil {
		checks = append(checks, PreflightCheckItem{
			Name: "ssh", Status: "fail",
			Message: fmt.Sprintf("SSH 连接目标节点失败: %s", probeErr.Error()),
		})
		sshFailed = true
		resp.CanProceed = false
	} else {
		checks = append(checks, PreflightCheckItem{
			Name: "ssh", Status: "pass",
			Message: fmt.Sprintf("SSH 连接成功，延迟 %dms", probe.Latency),
		})
		// 更新目标节点磁盘信息
		resp.TargetNode.DiskUsedGB = probe.DiskUsed
		resp.TargetNode.DiskTotalGB = probe.DiskTotal
	}

	// === 检查 2: 工具检测 ===
	if sshFailed {
		for tool := range toolSet {
			checks = append(checks, PreflightCheckItem{
				Name: "tool_" + tool, Status: "skip", Message: "SSH 不通，跳过工具检测",
			})
		}
	} else if len(toolSet) > 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		client, dialErr := executor.DialSSHForNode(ctx, targetNode)
		if dialErr != nil {
			for tool := range toolSet {
				checks = append(checks, PreflightCheckItem{
					Name: "tool_" + tool, Status: "fail",
					Message: fmt.Sprintf("无法建立 SSH 会话检测工具: %s", dialErr.Error()),
				})
				resp.CanProceed = false
			}
		} else {
			defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup
			for tool := range toolSet {
				cmd := fmt.Sprintf("which %s 2>/dev/null || command -v %s 2>/dev/null", tool, tool)
				if _, err := executor.RunSSHCommandOutput(ctx, client, cmd); err != nil {
					checks = append(checks, PreflightCheckItem{
						Name: "tool_" + tool, Status: "fail",
						Message: fmt.Sprintf("目标节点未安装 %s", tool),
					})
					resp.CanProceed = false
				} else {
					checks = append(checks, PreflightCheckItem{
						Name: "tool_" + tool, Status: "pass",
						Message: fmt.Sprintf("%s 已安装", tool),
					})
				}
			}

			// === 检查 3: 路径存在 ===
			checkedPaths := make(map[string]struct{})
			for _, p := range policies {
				path := strings.TrimSpace(p.SourcePath)
				if path == "" {
					continue
				}
				if _, done := checkedPaths[path]; done {
					continue
				}
				checkedPaths[path] = struct{}{}
				checkCmd := fmt.Sprintf("test -d %s && echo EXISTS || echo MISSING", executor.ShellEscape(path))
				out, _ := executor.RunSSHCommandOutput(ctx, client, checkCmd)
				if strings.Contains(out, "MISSING") || !strings.Contains(out, "EXISTS") {
					checks = append(checks, PreflightCheckItem{
						Name: "path", Status: "warn",
						Message: fmt.Sprintf("目标节点路径不存在: %s", path),
					})
				} else {
					checks = append(checks, PreflightCheckItem{
						Name: "path", Status: "pass",
						Message: fmt.Sprintf("路径存在: %s", path),
					})
				}
			}
		}
	}

	// === 检查 4: 磁盘空间 ===
	if !sshFailed && probe.DiskTotal > 0 {
		freeGB := probe.DiskTotal - probe.DiskUsed
		if freeGB < sourceNode.DiskUsedGB {
			checks = append(checks, PreflightCheckItem{
				Name: "disk", Status: "warn",
				Message: fmt.Sprintf("目标节点可用空间 %dGB，可能不足（源节点已用 %dGB）", freeGB, sourceNode.DiskUsedGB),
			})
		} else {
			checks = append(checks, PreflightCheckItem{
				Name: "disk", Status: "pass",
				Message: fmt.Sprintf("目标节点可用空间 %dGB", freeGB),
			})
		}
	} else if sshFailed {
		checks = append(checks, PreflightCheckItem{
			Name: "disk", Status: "skip", Message: "SSH 不通，跳过磁盘检查",
		})
	}

	// === 检查 5: 运行中的任务 ===
	var runningCount int64
	h.db.Model(&model.Task{}).
		Where("node_id = ? AND status IN ?", sourceID, []string{"running", "retrying"}).
		Count(&runningCount)
	if runningCount > 0 {
		checks = append(checks, PreflightCheckItem{
			Name: "running_tasks", Status: "warn",
			Message: fmt.Sprintf("源节点有 %d 个运行中的任务，迁移时将被取消", runningCount),
		})
	} else {
		checks = append(checks, PreflightCheckItem{
			Name: "running_tasks", Status: "pass",
			Message: "无运行中的任务",
		})
	}

	// === 检查 6: 本地备份数据可迁移性（基于任务的实际 rsync_target 路径） ===
	var totalDataSizeMB int64
	dataMigratableCount := 0
	checkedDataPaths := make(map[string]struct{})
	for _, t := range tasks {
		oldDir := t.RsyncTarget
		if oldDir == "" || util.IsRemotePathSpec(oldDir) {
			continue
		}
		if _, done := checkedDataPaths[oldDir]; done {
			continue
		}
		checkedDataPaths[oldDir] = struct{}{}

		info, statErr := os.Stat(oldDir)
		if statErr != nil || !info.IsDir() {
			continue
		}
		dataMigratableCount++
		totalDataSizeMB += estimateDirSizeMB(c.Request.Context(), oldDir)
	}
	if dataMigratableCount > 0 {
		resp.DataMigratable = true
		resp.DataSizeMB = totalDataSizeMB
		checks = append(checks, PreflightCheckItem{
			Name: "backup_data", Status: "pass",
			Message: fmt.Sprintf("发现 %d 个本地备份目录可迁移，约 %dMB", dataMigratableCount, totalDataSizeMB),
		})
	} else {
		checks = append(checks, PreflightCheckItem{
			Name: "backup_data", Status: "pass",
			Message: "无本地备份数据需要迁移",
		})
	}

	resp.Checks = checks
	respondOK(c, resp)
}

// estimateDirSizeMB 使用 du -sm 估算目录大小（MB），5 秒超时，失败返回 0。
func estimateDirSizeMB(ctx context.Context, path string) int64 {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "du", "-sm", path).Output()
	if err != nil {
		return 0
	}
	// du -sm 输出格式: "123\t/path/to/dir\n"
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	mb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return mb
}
