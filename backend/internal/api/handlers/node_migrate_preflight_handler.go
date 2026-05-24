package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

// MigratePreflight godoc
// @Summary      迁移预检
// @Description  执行节点迁移前的预检查，包括 SSH 连通性、工具安装、磁盘空间等
// @Tags         node-migration
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                                true  "源节点 ID"
// @Param        body  body      handlers.MigratePreflightRequest   true  "预检请求"
// @Success      200  {object}  handlers.Response{data=handlers.MigratePreflightResponse}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /nodes/{id}/migrate-preflight [post]
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "源节点不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	if err := h.db.Preload("SSHKey").First(&targetNode, req.TargetNodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "目标节点不存在")
			return
		}
		respondInternalError(c, err)
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
		if err := h.db.Model(&model.NodeOwner{}).Where("node_id = ? AND user_id = ?", req.TargetNodeID, userID).Count(&count).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if count == 0 {
			respondForbidden(c, "无权操作该目标节点")
			return
		}
	}

	// 收集受影响的策略
	var policies []model.Policy
	if err := h.db.Joins("JOIN policy_nodes ON policy_nodes.policy_id = policies.id").
		Where("policy_nodes.node_id = ?", sourceID).
		Find(&policies).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	// 收集受影响的任务（用于工具检测和计数）
	var policyIDs []uint
	for _, p := range policies {
		policyIDs = append(policyIDs, p.ID)
	}

	var tasks []model.Task
	if len(policyIDs) > 0 {
		if err := h.db.Where("node_id = ? AND source = ? AND policy_id IN ?", sourceID, "policy", policyIDs).Find(&tasks).Error; err != nil {
			respondInternalError(c, err)
			return
		}
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
			SourcePath:   sanitizeDiagnosticPathField(p.SourcePath),
			ExecutorType: et,
		})
	}

	resp := MigratePreflightResponse{
		SourceNode: PreflightNodeInfo{
			ID: sourceNode.ID, Name: sourceNode.Name, Host: sanitizeDiagnosticHostField(sourceNode.Host),
			Status: sourceNode.Status, DiskUsedGB: sourceNode.DiskUsedGB, DiskTotalGB: sourceNode.DiskTotalGB,
		},
		TargetNode: PreflightNodeInfo{
			ID: targetNode.ID, Name: targetNode.Name, Host: sanitizeDiagnosticHostField(targetNode.Host),
			Status: targetNode.Status, DiskUsedGB: targetNode.DiskUsedGB, DiskTotalGB: targetNode.DiskTotalGB,
		},
		Policies:   policyInfos,
		TaskCount:  len(tasks),
		CanProceed: true,
	}

	var checks []PreflightCheckItem

	// === 检查 1: SSH 连通性 ===
	sshFailed := false
	auditCredential := sshutil.ResolvedCredential{}
	probe, probeCredential, probeErr := sshutil.ProbeNodeForPurpose(targetNode, h.db, sshutil.PurposeNodeMigration)
	if probeCredential.Kind != "" || probeCredential.Source != "" || probeCredential.KeyID != nil {
		auditCredential = probeCredential
	}
	if probeErr != nil {
		checks = append(checks, PreflightCheckItem{
			Name: "ssh", Status: "fail",
			Message: fmt.Sprintf("SSH 连接目标节点失败: %s", classifyDoctorSSHEvidence(probeErr)),
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
		for _, tool := range sortedPreflightTools(toolSet) {
			checks = append(checks, PreflightCheckItem{
				Name: "tool_" + tool, Status: "skip", Message: "SSH 不通，跳过工具检测",
			})
		}
	} else if len(toolSet) > 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		tools := sortedPreflightTools(toolSet)
		client, dialErr := executor.DialSSHForNodePurpose(ctx, targetNode, sshutil.PurposeNodeMigration)
		if dialErr != nil {
			if resolved := resolveNodeCredentialForAudit(targetNode, h.db, sshutil.PurposeNodeMigration); resolved.Kind != "" || resolved.Source != "" || resolved.KeyID != nil {
				auditCredential = resolved
			}
			for _, tool := range tools {
				checks = append(checks, PreflightCheckItem{
					Name: "tool_" + tool, Status: "fail",
					Message: fmt.Sprintf("无法建立 SSH 会话检测工具: %s", classifyDoctorSSHEvidence(dialErr)),
				})
				resp.CanProceed = false
			}
		} else {
			defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup
			for _, tool := range tools {
				cmd := fmt.Sprintf("command -v %s >/dev/null 2>&1", executor.ShellEscape(tool))
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
				checkCmd := fmt.Sprintf("test -d %s", executor.ShellEscape(path))
				if _, err := executor.RunSSHCommandOutput(ctx, client, checkCmd); err != nil {
					checks = append(checks, PreflightCheckItem{
						Name: "path", Status: "warn",
						Message: "目标节点路径不存在或不可访问: [PATH_REDACTED]",
					})
				} else {
					checks = append(checks, PreflightCheckItem{
						Name: "path", Status: "pass",
						Message: "路径存在: [PATH_REDACTED]",
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
	if err := h.db.Model(&model.Task{}).
		Where("node_id = ? AND status IN ?", sourceID, []string{"running", "retrying"}).
		Count(&runningCount).Error; err != nil {
		respondInternalError(c, err)
		return
	}
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
	h.writeMigrationPreflightAudit(c, targetNode, auditCredential, preflightAuditOutcome(checks), checks, map[string]any{
		"source_node_id":  sourceNode.ID,
		"target_node_id":  targetNode.ID,
		"policy_count":    len(policies),
		"task_count":      len(tasks),
		"tool_count":      len(toolSet),
		"can_proceed":     resp.CanProceed,
		"data_migratable": resp.DataMigratable,
	})
	respondOK(c, resp)
}

func sanitizeDiagnosticHostField(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	return "[HOST_REDACTED]"
}

func sanitizeDiagnosticPathField(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	return "[PATH_REDACTED]"
}

func resolveNodeCredentialForAudit(node model.Node, db *gorm.DB, purpose string) sshutil.ResolvedCredential {
	_, credential, err := sshutil.BuildSSHAuthForPurpose(node, db, purpose)
	if err != nil {
		return credential
	}
	return credential
}

func (h *NodeHandler) writeMigrationPreflightAudit(c *gin.Context, targetNode model.Node, credential sshutil.ResolvedCredential, outcome string, checks []PreflightCheckItem, metadata map[string]any) {
	fallbackKind, fallbackSource, fallbackKeyID := nodeCredentialFallback(targetNode)
	kind, source, keyID := eventCredentialFields(credential, fallbackKind, fallbackSource)
	if keyID == nil {
		keyID = fallbackKeyID
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	passCount, warnCount, failCount, skipCount := preflightCheckCounts(checks)
	metadata["stage"] = "complete"
	metadata["check_count"] = len(checks)
	metadata["pass_count"] = passCount
	metadata["warn_count"] = warnCount
	metadata["failure_count"] = failCount
	metadata["skip_count"] = skipCount
	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:           "node_migration.preflight",
		Purpose:          sshutil.PurposeNodeMigration,
		CredentialKind:   kind,
		CredentialSource: source,
		SSHKeyID:         keyID,
		NodeID:           credentialaudit.PtrUint(targetNode.ID),
		Outcome:          outcome,
		Metadata:         metadata,
	})
}

func preflightAuditOutcome(checks []PreflightCheckItem) string {
	_, _, failCount, _ := preflightCheckCounts(checks)
	if failCount == 0 {
		return credentialaudit.OutcomeSuccess
	}
	for _, check := range checks {
		if check.Status == "fail" && check.Name == "ssh" {
			return credentialaudit.OutcomeBlocked
		}
	}
	return credentialaudit.OutcomeFailure
}

func sortedPreflightTools(toolSet map[string]struct{}) []string {
	preferred := []string{"rsync", "restic", "rclone"}
	tools := make([]string, 0, len(toolSet))
	seen := make(map[string]struct{}, len(toolSet))
	for _, tool := range preferred {
		if _, ok := toolSet[tool]; ok {
			tools = append(tools, tool)
			seen[tool] = struct{}{}
		}
	}
	for tool := range toolSet {
		if _, ok := seen[tool]; ok {
			continue
		}
		tools = append(tools, tool)
	}
	return tools
}

func preflightCheckCounts(checks []PreflightCheckItem) (passCount, warnCount, failCount, skipCount int) {
	for _, check := range checks {
		switch check.Status {
		case "pass":
			passCount++
		case "warn":
			warnCount++
		case "fail":
			failCount++
		case "skip":
			skipCount++
		}
	}
	return passCount, warnCount, failCount, skipCount
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
