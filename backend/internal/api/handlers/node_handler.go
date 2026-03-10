package handlers

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/gorm"
)

type NodeHandler struct {
	db *gorm.DB
}

func NewNodeHandler(db *gorm.DB) *NodeHandler {
	return &NodeHandler{db: db}
}

type nodeRequest struct {
	Name       string `json:"name" binding:"required"`
	Host       string `json:"host" binding:"required"`
	Port       int    `json:"port"`
	Username   string `json:"username" binding:"required"`
	AuthType   string `json:"auth_type"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	SSHKeyID   *uint  `json:"ssh_key_id"`
	Tags       string `json:"tags"`
	Status     string `json:"status"`
	BasePath   string `json:"base_path"`
}

type nodeBatchDeleteRequest struct {
	IDs []uint `json:"ids"`
}

const nodeExecDisabledCode = "XR-SEC-EXEC-DISABLED"

var nodeHostnameRegexp = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)
var knownHostsWriteMu sync.Mutex

func sanitizeNode(node model.Node) model.Node {
	copyNode := node
	copyNode.Password = ""
	copyNode.PrivateKey = ""
	if copyNode.SSHKey != nil {
		copyKey := *copyNode.SSHKey
		copyKey.PrivateKey = ""
		copyNode.SSHKey = &copyKey
	}
	return copyNode
}

func (h *NodeHandler) List(c *gin.Context) {
	var nodes []model.Node
	if err := h.db.Preload("SSHKey").Order("id asc").Find(&nodes).Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	safeNodes := make([]model.Node, 0, len(nodes))
	for _, node := range nodes {
		safeNodes = append(safeNodes, sanitizeNode(node))
	}

	c.JSON(http.StatusOK, gin.H{"data": safeNodes})
}

func (h *NodeHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": sanitizeNode(node)})
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

func (h *NodeHandler) Create(c *gin.Context) {
	var req nodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
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
	if req.BasePath == "" {
		req.BasePath = "/"
	}
	if err := validateNodeHostPort(req.Host, req.Port); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.validateSSHRef(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
	if err := h.db.Create(&node).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.db.Preload("SSHKey").First(&node, node.ID).Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": sanitizeNode(node)})
}

func (h *NodeHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req nodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	var node model.Node
	if err := h.db.First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.validateSSHRef(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
	if err := h.db.Save(&node).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.db.Preload("SSHKey").First(&node, node.ID).Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": sanitizeNode(node)})
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

func (h *NodeHandler) BatchDelete(c *gin.Context) {
	var req nodeBatchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	nodeIDs := uniqueNodeIDs(req.IDs)
	if len(nodeIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids 不能为空"})
		return
	}
	if len(nodeIDs) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次最多删除 200 个节点"})
		return
	}

	tx := h.db.Begin()
	if tx.Error != nil {
		log.Printf("服务器内部错误: %v", tx.Error)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
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
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	notFoundIDs := diffNodeIDs(nodeIDs, existingIDs)
	if len(existingIDs) == 0 {
		tx.Rollback()
		c.JSON(http.StatusOK, gin.H{
			"deleted":       0,
			"not_found_ids": notFoundIDs,
			"message":       "no nodes deleted",
		})
		return
	}

	if err := tx.Where("node_id IN ?", existingIDs).Delete(&model.Task{}).Error; err != nil {
		tx.Rollback()
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	if err := tx.Where("node_id IN ?", existingIDs).Delete(&model.Alert{}).Error; err != nil {
		tx.Rollback()
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	deleteResult := tx.Where("id IN ?", existingIDs).Delete(&model.Node{})
	if deleteResult.Error != nil {
		tx.Rollback()
		log.Printf("服务器内部错误: %v", deleteResult.Error)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"deleted":       deleteResult.RowsAffected,
		"not_found_ids": notFoundIDs,
		"message":       "deleted",
	})
}

func (h *NodeHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	tx := h.db.Begin()
	if tx.Error != nil {
		log.Printf("服务器内部错误: %v", tx.Error)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
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
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}

	if err := tx.Where("node_id = ?", id).Delete(&model.Task{}).Error; err != nil {
		tx.Rollback()
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	if err := tx.Where("node_id = ?", id).Delete(&model.Alert{}).Error; err != nil {
		tx.Rollback()
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	if err := tx.Delete(&model.Node{}, id).Error; err != nil {
		tx.Rollback()
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func parseDiskProbe(output string) (int, int, bool) {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 2 {
		return 0, 0, false
	}

	parseGB := func(raw string) (int, bool) {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(raw, "Gi"), "G"))
		value, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	total, okTotal := parseGB(fields[0])
	used, okUsed := parseGB(fields[1])
	if !okTotal || !okUsed || total <= 0 || used < 0 || used > total {
		return 0, 0, false
	}
	return used, total, true
}

func (h *NodeHandler) resolveKeyContent(node model.Node) (string, string, error) {
	if node.SSHKey != nil {
		if key := strings.TrimSpace(node.SSHKey.PrivateKey); key != "" {
			if node.SSHKeyID != nil {
				return key, fmt.Sprintf("ssh_key_id=%d", *node.SSHKeyID), nil
			}
			return key, "ssh_key_ref", nil
		}
	}

	if node.SSHKeyID != nil {
		keyID := *node.SSHKeyID
		var key model.SSHKey
		if err := h.db.First(&key, keyID).Error; err != nil {
			return "", fmt.Sprintf("ssh_key_id=%d", keyID), fmt.Errorf("节点绑定的密钥不存在，请重新选择")
		}
		if content := strings.TrimSpace(key.PrivateKey); content != "" {
			return content, fmt.Sprintf("ssh_key_id=%d", keyID), nil
		}
		return "", fmt.Sprintf("ssh_key_id=%d", keyID), fmt.Errorf("节点绑定的密钥内容为空，请重新配置")
	}

	if content := strings.TrimSpace(node.PrivateKey); content != "" {
		return content, "node.private_key", nil
	}
	return "", "", nil
}

func (h *NodeHandler) buildSSHAuth(node model.Node) ([]ssh.AuthMethod, string, error) {
	switch node.AuthType {
	case "password":
		if node.Password == "" {
			return nil, "", fmt.Errorf("密码认证模式下请填写密码")
		}
		return []ssh.AuthMethod{ssh.Password(node.Password)}, "", nil
	case "key":
		keyContent, keySource, resolveErr := h.resolveKeyContent(node)
		if resolveErr != nil {
			return nil, "", resolveErr
		}
		if keyContent == "" {
			return nil, "", fmt.Errorf("密钥认证模式下请选择已有密钥或填写私钥内容")
		}
		preparedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		if err != nil {
			if strings.TrimSpace(keySource) == "" {
				keySource = "unknown"
			}
			return nil, "", fmt.Errorf("私钥校验失败，请检查密钥内容是否正确")
		}
		signer, err := ssh.ParsePrivateKey([]byte(preparedKey))
		if err != nil {
			return nil, "", fmt.Errorf("解析私钥失败")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, preparedKey, nil
	default:
		return nil, "", fmt.Errorf("不支持的认证方式")
	}
}

func resolveSSHHostKeyCallback() (ssh.HostKeyCallback, error) {
	strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil {
		return nil, err
	}
	if !strictHostCheck {
		log.Printf("warn: SSH 主机密钥校验已禁用，建议在生产环境启用 SSH_STRICT_HOST_KEY_CHECKING=true")
		return ssh.InsecureIgnoreHostKey(), nil
	}

	knownHostsPath, err := util.ExpandHomePath(strings.TrimSpace(util.GetEnvOrDefault("SSH_KNOWN_HOSTS_PATH", "~/.ssh/known_hosts")))
	if err != nil {
		return nil, fmt.Errorf("解析 SSH_KNOWN_HOSTS_PATH 失败")
	}
	if strings.TrimSpace(knownHostsPath) == "" {
		return nil, fmt.Errorf("SSH_KNOWN_HOSTS_PATH 不能为空")
	}
	if err := ensureKnownHostsFile(knownHostsPath); err != nil {
		return nil, fmt.Errorf("准备 known_hosts 失败")
	}

	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("加载 known_hosts 失败")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if callbackErr := callback(hostname, remote, key); callbackErr != nil {
			var keyErr *knownhosts.KeyError
			if errors.As(callbackErr, &keyErr) && len(keyErr.Want) == 0 {
				if appendErr := appendKnownHost(knownHostsPath, hostname, key); appendErr != nil {
					return fmt.Errorf("knownhosts: accept new host failed: %w", appendErr)
				}
				refreshedCallback, refreshErr := knownhosts.New(knownHostsPath)
				if refreshErr != nil {
					return fmt.Errorf("加载 known_hosts 失败")
				}
				callback = refreshedCallback
				return callback(hostname, remote, key)
			}
			return callbackErr
		}
		return nil
	}, nil
}

func ensureKnownHostsFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	knownHostsWriteMu.Lock()
	defer knownHostsWriteMu.Unlock()

	if err := ensureKnownHostsFile(path); err != nil {
		return err
	}
	entry := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if knownHostEntryExists(content, hostname, key) {
		return nil
	}
	prefix := ""
	if len(content) > 0 && content[len(content)-1] != '\n' {
		prefix = "\n"
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(prefix + entry + "\n")
	return err
}

func knownHostEntryExists(content []byte, hostname string, key ssh.PublicKey) bool {
	normalizedHost := knownhosts.Normalize(hostname)
	keyFields := strings.Fields(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	if len(keyFields) < 2 {
		return false
	}

	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		hosts := strings.Split(fields[0], ",")
		if !slices.Contains(hosts, normalizedHost) {
			continue
		}
		if fields[1] == keyFields[0] && fields[2] == keyFields[1] {
			return true
		}
	}
	return false
}
func (h *NodeHandler) Exec(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{
		"error": "节点远程执行能力已禁用",
		"code":  nodeExecDisabledCode,
	})
}

func (h *NodeHandler) TestConnection(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}

	authMethods, _, err := h.buildSSHAuth(node)
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
		c.JSON(http.StatusOK, gin.H{
			"ok":      false,
			"message": fmt.Sprintf("连接失败：%v", err),
		})
		return
	}

	address := fmt.Sprintf("%s:%d", node.Host, node.Port)
	hostKeyCallback, err := resolveSSHHostKeyCallback()
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
		c.JSON(http.StatusOK, gin.H{
			"ok":      false,
			"message": fmt.Sprintf("连接失败：%v", err),
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
		c.JSON(http.StatusOK, gin.H{
			"ok":      false,
			"message": fmt.Sprintf("连接失败：%v", err),
		})
		return
	}
	defer client.Close()

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
			if used, total, ok := parseDiskProbe(string(output)); ok {
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
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
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

	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"message":       "SSH 连通性检测成功",
		"latency_ms":    latency,
		"disk_used_gb":  node.DiskUsedGB,
		"disk_total_gb": node.DiskTotalGB,
		"probe_at":      probeAt,
	})
}

func validateNodeHostPort(host string, port int) error {
	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		return fmt.Errorf("主机地址不能为空")
	}
	if net.ParseIP(trimmedHost) == nil {
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
