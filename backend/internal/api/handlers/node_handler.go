package handlers

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
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
	db      *gorm.DB
	trigger NodeTaskTrigger
}

func NewNodeHandler(db *gorm.DB, trigger NodeTaskTrigger) *NodeHandler {
	return &NodeHandler{db: db, trigger: trigger}
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

var nodeHostnameRegexp = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)
var consecutiveDashRegexp = regexp.MustCompile(`-{2,}`)

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

func (h *NodeHandler) validateSSHRef(req nodeRequest) error {
	switch req.AuthType {
	case "password":
		if req.Password == "" {
			return fmt.Errorf("密码认证模式下请填写密码")
		}
		return nil
	case "key":
		if req.SSHKeyID == nil && req.PrivateKey == "" {
			return fmt.Errorf("密钥认证模式下请选择已有密钥或填写私钥内容")
		}
		if req.SSHKeyID != nil {
			var key model.SSHKey
			if err := h.db.First(&key, *req.SSHKeyID).Error; err != nil {
				return fmt.Errorf("所选密钥不存在，请重新选择")
			}
		}
		return nil
	default:
		return fmt.Errorf("不支持的认证方式")
	}
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
	if req.Port == 0 {
		req.Port = 22
	}
	if req.AuthType == "" {
		req.AuthType = "key"
	}
	if req.Status == "" {
		req.Status = "offline"
	}
	// BasePath 不设置默认值 "/"，避免文件浏览器白名单开放整台机器
	if err := validateNodeName(req.Name); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := validateNodeHostPort(req.Host, req.Port); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := h.validateSSHRef(req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	node := model.Node{
		Name:        req.Name,
		Host:        req.Host,
		Port:        req.Port,
		Username:    req.Username,
		AuthType:    req.AuthType,
		Tags:        req.Tags,
		Status:      req.Status,
		BasePath:    req.BasePath,
		DiskTotalGB: 0,
		DiskUsedGB:  0,
	}

	switch req.AuthType {
	case "password":
		node.Password = req.Password
		node.SSHKeyID = nil
		node.PrivateKey = ""
	case "key":
		node.Password = ""
		node.SSHKeyID = req.SSHKeyID
		if req.SSHKeyID == nil {
			node.PrivateKey = req.PrivateKey
		} else {
			node.PrivateKey = ""
		}
	}
	if req.MaintenanceStart != nil {
		if *req.MaintenanceStart == "" {
			node.MaintenanceStart = nil
		} else if t, err := time.Parse(time.RFC3339, *req.MaintenanceStart); err == nil {
			node.MaintenanceStart = &t
		}
	}
	if req.MaintenanceEnd != nil {
		if *req.MaintenanceEnd == "" {
			node.MaintenanceEnd = nil
		} else if t, err := time.Parse(time.RFC3339, *req.MaintenanceEnd); err == nil {
			node.MaintenanceEnd = &t
		}
	}
	if req.ExpiryDate != nil {
		if *req.ExpiryDate == "" {
			node.ExpiryDate = nil
		} else if t, err := time.Parse(time.RFC3339, *req.ExpiryDate); err == nil {
			node.ExpiryDate = &t
		}
	}
	if req.Archived != nil {
		node.Archived = *req.Archived
	}
	if req.UseSudo != nil {
		node.UseSudo = *req.UseSudo
	}
	// BackupDir: auto-generate from name if empty
	if strings.TrimSpace(req.BackupDir) == "" {
		req.BackupDir = sanitizeBackupDir(req.Name)
	}
	if strings.TrimSpace(req.BackupDir) == "" {
		respondBadRequest(c, "节点名称无法自动生成备份目录标识，请手动指定 backup_dir（仅允许英文字母、数字、连字符、下划线）")
		return
	}
	if err := validateBackupDir(req.BackupDir); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	node.BackupDir = req.BackupDir
	if err := h.db.Create(&node).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "duplicate") {
			respondBadRequest(c, fmt.Sprintf("备份目录标识 '%s' 已被其他节点使用，请更换", req.BackupDir))
			return
		}
		respondBadRequest(c, err.Error())
		return
	}

	if err := h.db.Preload("SSHKey").First(&node, node.ID).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondCreated(c, node.Sanitized())
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
	var node model.Node
	if err := h.db.First(&node, id).Error; err != nil {
		respondNotFound(c, "节点不存在")
		return
	}

	if req.Port == 0 {
		req.Port = 22
	}
	if req.AuthType == "" {
		req.AuthType = node.AuthType
	}
	if req.Status == "" {
		req.Status = node.Status
	}
	if req.BasePath == "" {
		req.BasePath = node.BasePath
	}
	oldBackupDir := node.BackupDir
	if strings.TrimSpace(req.BackupDir) == "" {
		req.BackupDir = node.BackupDir
	}
	if err := validateBackupDir(req.BackupDir); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := validateNodeName(req.Name); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if req.SSHKeyID == nil {
		req.SSHKeyID = node.SSHKeyID
	}
	if req.Password == "" {
		req.Password = node.Password
	}
	if req.PrivateKey == "" {
		req.PrivateKey = node.PrivateKey
	}

	if err := validateNodeHostPort(req.Host, req.Port); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := h.validateSSHRef(req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	node.Name = req.Name
	node.Host = req.Host
	node.Port = req.Port
	node.Username = req.Username
	node.AuthType = req.AuthType
	node.Tags = req.Tags
	node.Status = req.Status
	node.BasePath = req.BasePath
	node.BackupDir = req.BackupDir

	switch req.AuthType {
	case "password":
		node.Password = req.Password
		node.SSHKeyID = nil
		node.PrivateKey = ""
	case "key":
		node.Password = ""
		node.SSHKeyID = req.SSHKeyID
		if req.SSHKeyID == nil {
			node.PrivateKey = req.PrivateKey
		} else {
			node.PrivateKey = ""
		}
	}
	if req.MaintenanceStart != nil {
		if *req.MaintenanceStart == "" {
			node.MaintenanceStart = nil
		} else if t, err := time.Parse(time.RFC3339, *req.MaintenanceStart); err == nil {
			node.MaintenanceStart = &t
		}
	}
	if req.MaintenanceEnd != nil {
		if *req.MaintenanceEnd == "" {
			node.MaintenanceEnd = nil
		} else if t, err := time.Parse(time.RFC3339, *req.MaintenanceEnd); err == nil {
			node.MaintenanceEnd = &t
		}
	}
	if req.ExpiryDate != nil {
		if *req.ExpiryDate == "" {
			node.ExpiryDate = nil
		} else if t, err := time.Parse(time.RFC3339, *req.ExpiryDate); err == nil {
			node.ExpiryDate = &t
		}
	}
	if req.Archived != nil {
		node.Archived = *req.Archived
	}
	if req.UseSudo != nil {
		node.UseSudo = *req.UseSudo
	}
	if err := h.db.Save(&node).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "duplicate") {
			respondBadRequest(c, fmt.Sprintf("备份目录标识 '%s' 已被其他节点使用，请更换", req.BackupDir))
			return
		}
		respondBadRequest(c, err.Error())
		return
	}

	if err := h.db.Preload("SSHKey").First(&node, node.ID).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	resp := gin.H{"data": node.Sanitized()}
	if oldBackupDir != "" && req.BackupDir != oldBackupDir {
		resp["warning"] = fmt.Sprintf("备份目录标识已更改，旧路径 /backup/%s 下的数据不会自动迁移", oldBackupDir)
	}
	respondOK(c, resp)
}

func uniqueNodeIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	result := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func diffNodeIDs(source []uint, existing []uint) []uint {
	exists := make(map[uint]struct{}, len(existing))
	for _, id := range existing {
		exists[id] = struct{}{}
	}
	diff := make([]uint, 0, len(source))
	for _, id := range source {
		if _, ok := exists[id]; !ok {
			diff = append(diff, id)
		}
	}
	return diff
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

	nodeIDs := uniqueNodeIDs(req.IDs)
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

	tx := h.db.Begin()
	if tx.Error != nil {
		respondInternalError(c, tx.Error)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var existingIDs []uint
	if err := tx.Model(&model.Node{}).Where("id IN ?", nodeIDs).Pluck("id", &existingIDs).Error; err != nil {
		tx.Rollback()
		respondInternalError(c, err)
		return
	}

	notFoundIDs := diffNodeIDs(nodeIDs, existingIDs)
	if len(existingIDs) == 0 {
		tx.Rollback()
		respondOK(c, gin.H{
			"deleted":       0,
			"not_found_ids": notFoundIDs,
			"message":       "no nodes deleted",
		})
		return
	}

	if err := tx.Where("node_id IN ?", existingIDs).Delete(&model.PolicyNode{}).Error; err != nil {
		tx.Rollback()
		respondInternalError(c, err)
		return
	}

	if err := tx.Where("node_id IN ?", existingIDs).Delete(&model.Task{}).Error; err != nil {
		tx.Rollback()
		respondInternalError(c, err)
		return
	}

	if err := tx.Where("node_id IN ?", existingIDs).Delete(&model.Alert{}).Error; err != nil {
		tx.Rollback()
		respondInternalError(c, err)
		return
	}

	deleteResult := tx.Where("id IN ?", existingIDs).Delete(&model.Node{})
	if deleteResult.Error != nil {
		tx.Rollback()
		respondInternalError(c, deleteResult.Error)
		return
	}

	if err := tx.Commit().Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondOK(c, gin.H{
		"deleted":       deleteResult.RowsAffected,
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

	tx := h.db.Begin()
	if tx.Error != nil {
		respondInternalError(c, tx.Error)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var node model.Node
	if err := tx.First(&node, id).Error; err != nil {
		tx.Rollback()
		respondNotFound(c, "节点不存在")
		return
	}

	if err := tx.Where("node_id = ?", id).Delete(&model.PolicyNode{}).Error; err != nil {
		tx.Rollback()
		respondInternalError(c, err)
		return
	}

	if err := tx.Where("node_id = ?", id).Delete(&model.Task{}).Error; err != nil {
		tx.Rollback()
		respondInternalError(c, err)
		return
	}

	if err := tx.Where("node_id = ?", id).Delete(&model.Alert{}).Error; err != nil {
		tx.Rollback()
		respondInternalError(c, err)
		return
	}

	if err := tx.Delete(&model.Node{}, id).Error; err != nil {
		tx.Rollback()
		respondInternalError(c, err)
		return
	}

	if err := tx.Commit().Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondMessage(c, "deleted")
}

func (h *NodeHandler) Exec(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{
		"error": "节点远程执行能力已禁用",
		"code":  nodeExecDisabledCode,
	})
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

	authMethods, _, err := sshutil.BuildSSHAuthWithKey(node, h.db)
	if err != nil {
		probeAt := time.Now()
		node.Status = "offline"
		node.ConnectionLatency = 0
		node.LastSeenAt = &probeAt
		if saveErr := h.db.Save(&node).Error; saveErr != nil {
			log.Printf("更新节点探测状态失败(node_id=%d): %v", node.ID, saveErr)
		}
		if alertErr := alerting.RaiseNodeProbeFailure(h.db, node, fmt.Sprintf("连接失败：%v", err)); alertErr != nil {
			log.Printf("创建节点探测告警失败(node_id=%d): %v", node.ID, alertErr)
		}
		log.Printf("SSH 连接测试失败(node_id=%d): %v", node.ID, err)
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
			log.Printf("更新节点探测状态失败(node_id=%d): %v", node.ID, saveErr)
		}
		if alertErr := alerting.RaiseNodeProbeFailure(h.db, node, fmt.Sprintf("连接失败：%v", err)); alertErr != nil {
			log.Printf("创建节点探测告警失败(node_id=%d): %v", node.ID, alertErr)
		}
		log.Printf("SSH 连接测试失败(node_id=%d): %v", node.ID, err)
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
			log.Printf("更新节点探测状态失败(node_id=%d): %v", node.ID, saveErr)
		}
		if alertErr := alerting.RaiseNodeProbeFailure(h.db, node, fmt.Sprintf("连接失败：%v", err)); alertErr != nil {
			log.Printf("创建节点探测告警失败(node_id=%d): %v", node.ID, alertErr)
		}
		log.Printf("SSH 连接测试失败(node_id=%d): %v", node.ID, err)
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
	if resolveErr := alerting.ResolveNodeAlerts(h.db, node.ID, "节点探测恢复正常"); resolveErr != nil {
		log.Printf("恢复节点探测告警失败(node_id=%d): %v", node.ID, resolveErr)
	}

	if node.SSHKeyID != nil {
		now := time.Now()
		if err := h.db.Model(&model.SSHKey{}).Where("id = ?", *node.SSHKeyID).Update("last_used_at", &now).Error; err != nil {
			log.Printf("更新 SSH Key 最近使用时间失败(ssh_key_id=%d): %v", *node.SSHKeyID, err)
		}
	}

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

// validateNodeName 校验节点名称，防止路径遍历攻击。
func validateNodeName(name string) error {
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("节点名称不能包含 /、\\、.. 或空字符")
	}
	return nil
}

func validateNodeHostPort(host string, port int) error {
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return fmt.Errorf("主机地址不能为空")
	}
	// 拒绝 localhost / 回环地址，防止 SSRF 或误操作管理服务器自身
	lower := strings.ToLower(trimmedHost)
	if lower == "localhost" || lower == "localhost.localdomain" {
		return fmt.Errorf("不允许将管理服务器自身（localhost）添加为节点")
	}
	if ip := net.ParseIP(trimmedHost); ip != nil {
		if ip.IsLoopback() {
			return fmt.Errorf("不允许将回环地址添加为节点")
		}
	} else {
		// 不是 IP，检查是否是合法的 hostname
		if len(trimmedHost) > 253 {
			return fmt.Errorf("主机名过长")
		}
		if !nodeHostnameRegexp.MatchString(trimmedHost) {
			return fmt.Errorf("主机地址格式不合法")
		}
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口号必须在 1-65535 之间")
	}
	return nil
}

// sanitizeBackupDir generates a filesystem-safe backup directory identifier from a name.
func sanitizeBackupDir(name string) string {
	s := strings.ToLower(name)
	var buf strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			buf.WriteRune(r)
		} else {
			buf.WriteByte('-')
		}
	}
	// collapse consecutive dashes
	result := consecutiveDashRegexp.ReplaceAllString(buf.String(), "-")
	result = strings.Trim(result, "-")
	if len(result) < 2 {
		return ""
	}
	return result
}

// validateBackupDir checks that a backup directory identifier is safe for filesystem use.
func validateBackupDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("备份目录标识不能为空")
	}
	if len(dir) > 128 {
		return fmt.Errorf("备份目录标识长度不能超过 128 个字符")
	}
	if strings.ContainsAny(dir, "/\\") || strings.Contains(dir, "..") || strings.ContainsRune(dir, 0) {
		return fmt.Errorf("备份目录标识不能包含 /、\\、.. 或空字符")
	}
	return nil
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
