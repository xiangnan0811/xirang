package handlers

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/apperr"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/node"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// NodeTaskTrigger 用于紧急备份触发任务执行及节点迁移时的调度管理。
type NodeTaskTrigger interface {
	TriggerManual(taskID uint) (uint, error)
	Cancel(taskID uint) error
	RemoveSchedule(taskID uint)
	SyncSchedule(task model.Task) error
}

type NodeHandler struct {
	db              *gorm.DB
	trigger         NodeTaskTrigger
	settingsSvc     *settings.Service
	alertDispatcher *alerting.Dispatcher
	svc             *node.NodeService
}

func NewNodeHandler(db *gorm.DB, trigger NodeTaskTrigger, svc *node.NodeService) *NodeHandler {
	return &NodeHandler{db: db, trigger: trigger, svc: svc}
}

func (h *NodeHandler) WithSettingsService(settingsSvc *settings.Service) *NodeHandler {
	h.settingsSvc = settingsSvc
	return h
}

func (h *NodeHandler) WithAlertDispatcher(alertDispatcher *alerting.Dispatcher) *NodeHandler {
	h.alertDispatcher = alertDispatcher
	return h
}

func (h *NodeHandler) getAlertDispatcher() *alerting.Dispatcher {
	if h.alertDispatcher == nil {
		return alerting.NewDispatcher(h.db, nil, nil)
	}
	return h.alertDispatcher
}

type nodeRequest struct {
	Name             string  `json:"name" binding:"required"`
	Host             string  `json:"host" binding:"required"`
	Port             int     `json:"port"`
	Username         string  `json:"username" binding:"required"`
	AuthType         string  `json:"auth_type"`
	Password         string  `json:"password"`
	PrivateKey       string  `json:"private_key"`
	SSHKeyID         *uint   `json:"ssh_key_id"`
	Tags             string  `json:"tags"`
	Status           string  `json:"status"`
	BasePath         string  `json:"base_path"`
	BackupDir        string  `json:"backup_dir"`
	MaintenanceStart *string `json:"maintenance_start"`
	MaintenanceEnd   *string `json:"maintenance_end"`
	ExpiryDate       *string `json:"expiry_date"`
	Archived         *bool   `json:"archived"`
	UseSudo          *bool   `json:"use_sudo"`
}

type nodeBatchDeleteRequest struct {
	IDs []uint `json:"ids"`
}

const nodeExecDisabledCode = "XR-SEC-EXEC-DISABLED"

// List godoc
// @Summary      列出节点
// @Description  返回所有节点列表，operator 仅返回自己负责的节点
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        include_archived  query     bool  false  "是否包含已归档节点"
// @Success      200               {object}  handlers.Response{data=[]model.Node}
// @Failure      401               {object}  handlers.Response
// @Router       /nodes [get]
func (h *NodeHandler) List(c *gin.Context) {
	query := h.db.Preload("SSHKey")
	if c.Query("include_archived") != "true" {
		query = query.Where("archived = ?", false)
	}
	if nodeIDs, needFilter, err := ownershipNodeFilter(c, h.db); err != nil {
		respondInternalError(c, err)
		return
	} else if needFilter {
		query = query.Where("id IN ?", nodeIDs)
	}

	var nodes []model.Node
	if err := query.Order("id asc").Find(&nodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	safeNodes := make([]model.Node, 0, len(nodes))
	for _, node := range nodes {
		safeNodes = append(safeNodes, node.Sanitized())
	}

	respondOK(c, safeNodes)
}

// Get godoc
// @Summary      获取节点详情
// @Description  返回单个节点的详细信息
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response{data=model.Node}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /nodes/{id} [get]
func (h *NodeHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, id).Error; err != nil {
		respondNotFound(c, "节点不存在")
		return
	}
	respondOK(c, node.Sanitized())
}

// Create godoc
// @Summary      创建节点
// @Description  添加新的服务器节点
// @Tags         nodes
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      nodeRequest  true  "创建节点请求"
// @Success      201   {object}  handlers.Response{data=model.Node}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /nodes [post]
func (h *NodeHandler) Create(c *gin.Context) {
	var req nodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	nodeObj, err := h.svc.Create(c.Request.Context(), node.CreateNodeInput{
		Name:             req.Name,
		Host:             req.Host,
		Port:             req.Port,
		Username:         req.Username,
		AuthType:         req.AuthType,
		Password:         req.Password,
		PrivateKey:       req.PrivateKey,
		SSHKeyID:         req.SSHKeyID,
		Tags:             req.Tags,
		Status:           req.Status,
		BasePath:         req.BasePath,
		BackupDir:        req.BackupDir,
		MaintenanceStart: req.MaintenanceStart,
		MaintenanceEnd:   req.MaintenanceEnd,
		ExpiryDate:       req.ExpiryDate,
		Archived:         req.Archived,
		UseSudo:          req.UseSudo,
	})
	if err != nil {
		handleNodeServiceError(c, err, req.BackupDir)
		return
	}

	respondCreated(c, nodeObj.Sanitized())
}

// Update godoc
// @Summary      更新节点
// @Description  更新节点配置信息
// @Tags         nodes
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int          true  "节点 ID"
// @Param        body  body      nodeRequest  true  "更新节点请求"
// @Success      200   {object}  handlers.Response{data=model.Node}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /nodes/{id} [put]
func (h *NodeHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req nodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	nodeObj, oldBackupDir, err := h.svc.Update(c.Request.Context(), id, node.CreateNodeInput{
		Name:             req.Name,
		Host:             req.Host,
		Port:             req.Port,
		Username:         req.Username,
		AuthType:         req.AuthType,
		Password:         req.Password,
		PrivateKey:       req.PrivateKey,
		SSHKeyID:         req.SSHKeyID,
		Tags:             req.Tags,
		Status:           req.Status,
		BasePath:         req.BasePath,
		BackupDir:        req.BackupDir,
		MaintenanceStart: req.MaintenanceStart,
		MaintenanceEnd:   req.MaintenanceEnd,
		ExpiryDate:       req.ExpiryDate,
		Archived:         req.Archived,
		UseSudo:          req.UseSudo,
	})
	if err != nil {
		handleNodeServiceError(c, err, req.BackupDir)
		return
	}

	resp := gin.H{"data": nodeObj.Sanitized()}
	if oldBackupDir != "" && req.BackupDir != oldBackupDir {
		resp["warning"] = fmt.Sprintf("备份目录标识已更改，旧路径 /backup/%s 下的数据不会自动迁移", oldBackupDir)
	}
	respondOK(c, resp)
}

// handleNodeServiceError maps errors from NodeService to appropriate HTTP responses.
func handleNodeServiceError(c *gin.Context, err error, backupDir string) {
	if errors.Is(err, apperr.ErrValidation) {
		msg := err.Error()
		prefix := apperr.ErrValidation.Error() + ": "
		if after, ok := strings.CutPrefix(msg, prefix); ok {
			msg = after
		}
		respondBadRequest(c, msg)
		return
	}
	if errors.Is(err, apperr.ErrDuplicate) {
		if backupDir != "" {
			respondConflict(c, fmt.Sprintf("备份目录标识 '%s' 已被其他节点使用，请更换", backupDir))
		} else {
			respondConflict(c, "资源已存在")
		}
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondNotFound(c, "节点不存在")
		return
	}
	respondInternalError(c, err)
}

// BatchDelete godoc
// @Summary      批量删除节点
// @Description  批量删除多个节点及其关联的策略关联、任务、告警
// @Tags         nodes
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      nodeBatchDeleteRequest  true  "节点 ID 列表"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /nodes/batch-delete [post]
func (h *NodeHandler) BatchDelete(c *gin.Context) {
	var req nodeBatchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	nodeIDs := make([]uint, 0, len(req.IDs))
	seen := make(map[uint]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		nodeIDs = append(nodeIDs, id)
	}

	if len(nodeIDs) == 0 {
		respondBadRequest(c, "ids 不能为空")
		return
	}
	if len(nodeIDs) > 200 {
		respondBadRequest(c, "单次最多删除 200 个节点")
		return
	}

	// operator 只能删除自己拥有的节点
	ownedIDs, needFilter, err := ownershipNodeFilter(c, h.db)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if needFilter {
		ownedSet := make(map[uint]struct{}, len(ownedIDs))
		for _, id := range ownedIDs {
			ownedSet[id] = struct{}{}
		}
		filtered := make([]uint, 0, len(nodeIDs))
		for _, id := range nodeIDs {
			if _, ok := ownedSet[id]; ok {
				filtered = append(filtered, id)
			}
		}
		nodeIDs = filtered
		if len(nodeIDs) == 0 {
			respondForbidden(c, "无权删除这些节点")
			return
		}
	}

	deleted, notFoundIDs, err := h.svc.BatchDelete(c.Request.Context(), nodeIDs)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	if deleted == 0 {
		respondOK(c, gin.H{
			"deleted":       0,
			"not_found_ids": notFoundIDs,
			"message":       "no nodes deleted",
		})
		return
	}

	respondOK(c, gin.H{
		"deleted":       deleted,
		"not_found_ids": notFoundIDs,
		"message":       "deleted",
	})
}

// Delete godoc
// @Summary      删除节点
// @Description  删除指定节点及其关联的策略关联、任务、告警
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /nodes/{id} [delete]
func (h *NodeHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "节点不存在")
			return
		}
		respondInternalError(c, err)
		return
	}

	respondMessage(c, "deleted")
}

func (h *NodeHandler) Exec(c *gin.Context) {
	respondForbiddenData(c, "节点远程执行能力已禁用", gin.H{"error_code": nodeExecDisabledCode})
}

// TestConnection godoc
// @Summary      测试节点 SSH 连接
// @Description  测试节点的 SSH 连通性，成功时更新延迟和磁盘信息
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /nodes/{id}/test [post]
func (h *NodeHandler) TestConnection(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, id).Error; err != nil {
		respondNotFound(c, "节点不存在")
		return
	}
	nodeLog := logger.Module("api").With().Uint("node_id", node.ID).Logger()

	authMethods, _, credential, err := sshutil.BuildSSHAuthWithKeyForPurpose(node, h.db, sshutil.PurposeNodeTest)
	if err != nil {
		probeAt := time.Now()
		node.Status = "offline"
		node.ConnectionLatency = 0
		node.LastSeenAt = &probeAt
		if saveErr := h.db.Save(&node).Error; saveErr != nil {
			nodeLog.Warn().Err(saveErr).Msg("更新节点探测状态失败")
		}
		if alertErr := h.getAlertDispatcher().RaiseNodeProbeFailure(node, fmt.Sprintf("连接失败：%v", err)); alertErr != nil {
			nodeLog.Warn().Err(alertErr).Msg("创建节点探测告警失败")
		}
		nodeLog.Warn().Err(err).Msg("SSH 连接测试失败")
		writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
			Action:           "node.credential.test_connection",
			Purpose:          sshutil.PurposeNodeTest,
			CredentialKind:   credential.Kind,
			CredentialSource: credential.Source,
			SSHKeyID:         credential.KeyID,
			NodeID:           credentialaudit.PtrUint(node.ID),
			Outcome:          credentialaudit.OutcomeBlocked,
			ErrorMessage:     err.Error(),
			Metadata: map[string]any{
				"stage": "auth_build",
			},
		})
		respondOK(c, gin.H{
			"ok":      false,
			"message": "SSH 连接失败，请检查主机地址、端口、认证配置",
		})
		return
	}

	address := fmt.Sprintf("%s:%d", node.Host, node.Port)
	hostKeyCallback, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		probeAt := time.Now()
		node.Status = "offline"
		node.ConnectionLatency = 0
		node.LastSeenAt = &probeAt
		if saveErr := h.db.Save(&node).Error; saveErr != nil {
			nodeLog.Warn().Err(saveErr).Msg("更新节点探测状态失败")
		}
		if alertErr := h.getAlertDispatcher().RaiseNodeProbeFailure(node, fmt.Sprintf("连接失败：%v", err)); alertErr != nil {
			nodeLog.Warn().Err(alertErr).Msg("创建节点探测告警失败")
		}
		nodeLog.Warn().Err(err).Msg("SSH 连接测试失败")
		writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
			Action:           "node.credential.test_connection",
			Purpose:          sshutil.PurposeNodeTest,
			CredentialKind:   credential.Kind,
			CredentialSource: credential.Source,
			SSHKeyID:         credential.KeyID,
			NodeID:           credentialaudit.PtrUint(node.ID),
			Outcome:          credentialaudit.OutcomeFailure,
			ErrorMessage:     err.Error(),
			Metadata: map[string]any{
				"stage": "host_key",
			},
		})
		respondOK(c, gin.H{
			"ok":      false,
			"message": "SSH 连接失败，请检查主机地址、端口、认证配置",
		})
		return
	}

	start := time.Now()
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            node.Username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         5 * time.Second,
	})
	probeAt := time.Now()
	if err != nil {
		node.Status = "offline"
		node.ConnectionLatency = 0
		node.LastSeenAt = &probeAt
		if saveErr := h.db.Save(&node).Error; saveErr != nil {
			nodeLog.Warn().Err(saveErr).Msg("更新节点探测状态失败")
		}
		if alertErr := h.getAlertDispatcher().RaiseNodeProbeFailure(node, fmt.Sprintf("连接失败：%v", err)); alertErr != nil {
			nodeLog.Warn().Err(alertErr).Msg("创建节点探测告警失败")
		}
		nodeLog.Warn().Err(err).Msg("SSH 连接测试失败")
		writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
			Action:           "node.credential.test_connection",
			Purpose:          sshutil.PurposeNodeTest,
			CredentialKind:   credential.Kind,
			CredentialSource: credential.Source,
			SSHKeyID:         credential.KeyID,
			NodeID:           credentialaudit.PtrUint(node.ID),
			Outcome:          credentialaudit.OutcomeFailure,
			ErrorMessage:     err.Error(),
			Metadata: map[string]any{
				"stage":      "dial",
				"latency_ms": int(time.Since(start).Milliseconds()),
			},
		})
		respondOK(c, gin.H{
			"ok":      false,
			"message": "SSH 连接失败，请检查主机地址、端口、认证配置",
		})
		return
	}
	defer client.Close() //nolint:errcheck // close error not actionable on deferred cleanup

	latency := int(time.Since(start).Milliseconds())
	if latency <= 0 {
		latency = 1
	}

	node.Status = "online"
	node.ConnectionLatency = latency
	node.LastSeenAt = &probeAt

	if session, err := client.NewSession(); err == nil {
		output, runErr := session.Output("df -BG / | awk 'NR==2 {print $2\" \"$3}'")
		_ = session.Close()
		if runErr == nil {
			if used, total, ok := sshutil.ParseDiskProbe(string(output)); ok {
				node.DiskUsedGB = used
				node.DiskTotalGB = total
			}
		}
	}

	if node.DiskTotalGB > 0 {
		if node.DiskUsedGB < 0 {
			node.DiskUsedGB = 0
		}
		if node.DiskUsedGB > node.DiskTotalGB {
			node.DiskUsedGB = node.DiskTotalGB
		}
	} else {
		node.DiskUsedGB = 0
	}
	if err := h.db.Save(&node).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if resolveErr := h.getAlertDispatcher().ResolveNodeAlerts(node.ID, "节点探测恢复正常"); resolveErr != nil {
		nodeLog.Warn().Err(resolveErr).Msg("恢复节点探测告警失败")
	}

	lastUsedUpdated := false
	if node.SSHKeyID != nil {
		now := time.Now()
		if err := h.db.Model(&model.SSHKey{}).Where("id = ?", *node.SSHKeyID).Update("last_used_at", &now).Error; err != nil {
			nodeLog.Warn().Uint("ssh_key_id", *node.SSHKeyID).Err(err).Msg("更新 SSH Key 最近使用时间失败")
		} else {
			lastUsedUpdated = true
		}
	}

	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:           "node.credential.test_connection",
		Purpose:          sshutil.PurposeNodeTest,
		CredentialKind:   credential.Kind,
		CredentialSource: credential.Source,
		SSHKeyID:         credential.KeyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		Outcome:          credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"stage":                "success",
			"latency_ms":           latency,
			"disk_probe_success":   node.DiskTotalGB > 0,
			"last_used_at_updated": lastUsedUpdated,
		},
	})

	respondOK(c, gin.H{
		"ok":            true,
		"message":       "SSH 连通性检测成功",
		"latency_ms":    latency,
		"disk_used_gb":  node.DiskUsedGB,
		"disk_total_gb": node.DiskTotalGB,
		"probe_at":      probeAt,
	})
}

// Metrics godoc
// @Summary      获取节点资源采样
// @Description  返回节点最近的 CPU/内存/磁盘/负载资源采样数据，用于趋势图
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id     path      int     true   "节点 ID"
// @Param        limit  query     int     false  "返回条数（默认 288，最大 2016）"
// @Param        since  query     string  false  "时间范围，如 24h、7d（默认 24h）"
// @Success      200    {object}  handlers.Response
// @Failure      401    {object}  handlers.Response
// @Failure      404    {object}  handlers.Response
// @Router       /nodes/{id}/metrics [get]
func (h *NodeHandler) Metrics(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}

	limit := 288 // 24h * 12 samples/hour (5min interval)
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 2016 {
			limit = v
		}
	}

	// since=24h, 7d, etc.
	since := 24 * time.Hour
	if raw := c.Query("since"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			since = d
		}
	}

	cutoff := time.Now().UTC().Add(-since)
	var samples []model.NodeMetricSample
	if err := h.db.Where("node_id = ? AND sampled_at >= ?", nodeID, cutoff).
		Order("sampled_at asc").
		Limit(limit).
		Find(&samples).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, gin.H{"items": samples})
}

// ListOwners godoc
// @Summary      列出节点负责人
// @Description  返回节点的所有负责人列表（admin only）
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /nodes/{id}/owners [get]
func (h *NodeHandler) ListOwners(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var owners []model.NodeOwner
	if err := h.db.Preload("User").Where("node_id = ?", nodeID).Find(&owners).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	type item struct {
		UserID   uint   `json:"user_id"`
		Username string `json:"username"`
	}
	result := make([]item, 0, len(owners))
	for _, o := range owners {
		result = append(result, item{UserID: o.UserID, Username: o.User.Username})
	}
	respondOK(c, result)
}

// AddOwner godoc
// @Summary      添加节点负责人
// @Description  为节点添加一个负责人（admin only）
// @Tags         nodes
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int   true  "节点 ID"
// @Param        body  body      object  true  "user_id"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Router       /nodes/{id}/owners [post]
func (h *NodeHandler) AddOwner(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req struct {
		UserID uint `json:"user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	owner := model.NodeOwner{NodeID: nodeID, UserID: req.UserID}
	if err := h.db.Where(owner).FirstOrCreate(&owner).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "已添加负责人")
}

// RemoveOwner godoc
// @Summary      移除节点负责人
// @Description  移除节点的指定负责人（admin only）
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id       path      int  true  "节点 ID"
// @Param        user_id  path      int  true  "用户 ID"
// @Success      200      {object}  handlers.Response
// @Failure      400      {object}  handlers.Response
// @Failure      401      {object}  handlers.Response
// @Failure      403      {object}  handlers.Response
// @Router       /nodes/{id}/owners/{user_id} [delete]
func (h *NodeHandler) RemoveOwner(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}
	userIDStr := c.Param("user_id")
	userID, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil {
		respondBadRequest(c, "无效的用户 ID")
		return
	}
	if err := h.db.Where("node_id = ? AND user_id = ?", nodeID, uint(userID)).
		Delete(&model.NodeOwner{}).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "已移除负责人")
}

// EmergencyBackup godoc
// @Summary      紧急备份
// @Description  触发节点上所有备份任务的立即执行
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /nodes/{id}/emergency-backup [post]
func (h *NodeHandler) EmergencyBackup(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var tasks []model.Task
	if err := h.db.Where("node_id = ? AND source = ? AND executor_type IN ?",
		id, "policy", []string{"rsync", "restic", "rclone"}).Find(&tasks).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	if len(tasks) == 0 {
		respondOK(c, gin.H{"triggered": 0, "task_ids": []uint{}, "errors": []string{}})
		return
	}

	triggered := 0
	taskIDs := make([]uint, 0)
	errors := make([]string, 0)

	for _, t := range tasks {
		runID, err := h.trigger.TriggerManual(t.ID)
		if err != nil {
			errors = append(errors, fmt.Sprintf("任务 %d 触发失败: %v", t.ID, err))
			continue
		}
		triggered++
		taskIDs = append(taskIDs, runID)
	}

	respondOK(c, gin.H{
		"triggered": triggered,
		"task_ids":  taskIDs,
		"errors":    errors,
	})
}
